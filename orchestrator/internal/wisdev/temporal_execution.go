package wisdev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	defaultTemporalNamespace = "default"
	defaultTemporalTaskQueue = "wisdev-execution"
	wisdevTemporalWorkflow   = "wisdev.session.execution"
	wisdevTemporalActivity   = "wisdev.execute.session.activity"
)

type TemporalConfig struct {
	Enabled   bool
	Address   string
	Namespace string
	TaskQueue string
}

type SessionWorkflowInput struct {
	SessionID string `json:"sessionId"`
}

type SessionWorkflowResult struct {
	SessionID       string        `json:"sessionId"`
	Status          SessionStatus `json:"status"`
	PendingApproval bool          `json:"pendingApproval"`
}

type TemporalExecutionService struct {
	gateway   *AgentGateway
	client    client.Client
	taskQueue string
}

type temporalActivities struct {
	gateway *AgentGateway
}

func ResolveTemporalConfig() TemporalConfig {
	cfg := TemporalConfig{
		Enabled:   strings.EqualFold(strings.TrimSpace(os.Getenv("TEMPORAL_ENABLED")), "1") || strings.EqualFold(strings.TrimSpace(os.Getenv("TEMPORAL_ENABLED")), "true"),
		Address:   strings.TrimSpace(os.Getenv("TEMPORAL_ADDRESS")),
		Namespace: strings.TrimSpace(os.Getenv("TEMPORAL_NAMESPACE")),
		TaskQueue: strings.TrimSpace(os.Getenv("TEMPORAL_TASK_QUEUE")),
	}
	if cfg.Namespace == "" {
		cfg.Namespace = defaultTemporalNamespace
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = defaultTemporalTaskQueue
	}
	if cfg.Address != "" {
		cfg.Enabled = true
	}
	return cfg
}

func NewTemporalClient(cfg TemporalConfig) (client.Client, error) {
	if !cfg.Enabled || cfg.Address == "" {
		return nil, fmt.Errorf("temporal is not configured")
	}
	return client.Dial(client.Options{
		HostPort:  cfg.Address,
		Namespace: cfg.Namespace,
	})
}

func StartTemporalWorker(gateway *AgentGateway, temporalClient client.Client, cfg TemporalConfig) (func(), error) {
	if gateway == nil {
		return nil, fmt.Errorf("gateway is required")
	}
	if temporalClient == nil {
		return nil, fmt.Errorf("temporal client is required")
	}
	w := worker.New(temporalClient, cfg.TaskQueue, worker.Options{})

	// Register Workflows
	w.RegisterWorkflowWithOptions(WisdevSessionWorkflow, workflow.RegisterOptions{Name: wisdevTemporalWorkflow})

	// Register Activities
	activities := &temporalActivities{gateway: gateway}
	w.RegisterActivity(activities.ExecuteSessionActivity)
	w.RegisterActivity(activities.GetSessionActivity)
	w.RegisterActivity(activities.UpdateSessionActivity)
	w.RegisterActivity(activities.ResearchSearchActivity)
	w.RegisterActivity(activities.SufficiencyActivity)
	w.RegisterActivity(activities.ReasoningRefreshActivity)
	w.RegisterActivity(activities.SynthesisActivity)

	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

func NewTemporalExecutionService(gateway *AgentGateway, temporalClient client.Client, cfg TemporalConfig) *TemporalExecutionService {
	return &TemporalExecutionService{
		gateway:   gateway,
		client:    temporalClient,
		taskQueue: cfg.TaskQueue,
	}
}

func (s *TemporalExecutionService) Start(ctx context.Context, sessionID string) (*ExecutionStartResult, error) {
	if s == nil || s.gateway == nil || s.client == nil {
		return nil, fmt.Errorf("temporal execution service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sessionId is required")
	}
	session, err := s.gateway.Store.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.Plan == nil {
		session.Plan = BuildDefaultPlan(session)
		if err := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); err != nil {
			return nil, err
		}
	}
	_, err = s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       temporalWorkflowID(sessionID),
		TaskQueue:                                s.taskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, wisdevTemporalWorkflow, SessionWorkflowInput{SessionID: sessionID})
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) {
			return &ExecutionStartResult{
				SessionID:      sessionID,
				Status:         session.Status,
				ExecutionID:    temporalWorkflowID(sessionID),
				AlreadyRunning: true,
			}, nil
		}
		return nil, err
	}
	return &ExecutionStartResult{
		SessionID:   sessionID,
		Status:      session.Status,
		ExecutionID: temporalWorkflowID(sessionID),
	}, nil
}

func (s *TemporalExecutionService) Cancel(ctx context.Context, sessionID string) error {
	if s == nil || s.gateway == nil || s.client == nil {
		return fmt.Errorf("temporal execution service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	err := s.client.CancelWorkflow(ctx, temporalWorkflowID(sessionID), "")
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return err
		}
	}
	session, getErr := s.gateway.Store.Get(ctx, sessionID)
	if getErr != nil {
		return getErr
	}
	session.Status = SessionPaused
	session.UpdatedAt = NowMillis()
	if putErr := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); putErr != nil {
		return putErr
	}
	appendExecutionEvent(s.gateway, session, PlanExecutionEvent{
		Type:      EventProgress,
		TraceID:   NewTraceID(),
		SessionID: sessionID,
		PlanID:    session.PlanID(),
		Message:   "execution paused",
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status": "paused",
		},
	})
	return nil
}

func (s *TemporalExecutionService) Abandon(ctx context.Context, sessionID string) error {
	if s == nil || s.gateway == nil || s.client == nil {
		return fmt.Errorf("temporal execution service unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId is required")
	}
	err := s.client.CancelWorkflow(ctx, temporalWorkflowID(sessionID), "")
	if err != nil {
		var notFound *serviceerror.NotFound
		if !errors.As(err, &notFound) {
			return err
		}
	}
	session, getErr := s.gateway.Store.Get(ctx, sessionID)
	if getErr != nil {
		return getErr
	}
	session.Status = StatusAbandoned
	session.UpdatedAt = NowMillis()
	if putErr := s.gateway.Store.Put(ctx, session, s.gateway.SessionTTL); putErr != nil {
		return putErr
	}
	appendExecutionEvent(s.gateway, session, PlanExecutionEvent{
		Type:      EventPlanCancelled,
		TraceID:   NewTraceID(),
		SessionID: sessionID,
		PlanID:    session.PlanID(),
		Message:   "Plan cancelled",
		CreatedAt: NowMillis(),
		Payload: map[string]any{
			"status": "cancelled",
		},
	})
	return nil
}

func (s *TemporalExecutionService) Stream(ctx context.Context, sessionID string, emit func(PlanExecutionEvent) error) error {
	fallback := NewDurableExecutionService(s.gateway)
	return fallback.Stream(ctx, sessionID, emit)
}

func WisdevSessionWorkflow(ctx workflow.Context, input SessionWorkflowInput) (*SessionWorkflowResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout:    60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	logger := workflow.GetLogger(ctx)

	// 1. Fetch Session State
	var session AgentSession
	if err := workflow.ExecuteActivity(ctx, "GetSessionActivity", input.SessionID).Get(ctx, &session); err != nil {
		return nil, err
	}

	// 2. Initialize Loop State
	maxIterations := 5
	if session.Mode == "yolo" {
		maxIterations = 10
	}

	var papers []search.Paper
	queryCoverage := make(map[string][]search.Paper)
	var lastAnalysis *sufficiencyAnalysis

	// 3. Distributed Research Loop
	for i := 0; i < maxIterations; i++ {
		logger.Info("Starting workflow iteration", "iteration", i+1)

		// A. Research Search Activity
		var searchOut ResearchSearchOutput
		searchIn := ResearchSearchInput{
			SessionID:   input.SessionID,
			Queries:     []string{session.Query}, // simplified for now
			Parallelism: 3,
		}
		if err := workflow.ExecuteActivity(ctx, "ResearchSearchActivity", searchIn).Get(ctx, &searchOut); err != nil {
			return nil, err
		}
		papers = append(papers, searchOut.Papers...)
		for q, p := range searchOut.QueryCoverage {
			queryCoverage[q] = p
		}

		// B. Sufficiency Analysis Activity
		var suffOut SufficiencyOutput
		suffIn := SufficiencyInput{
			SessionID:     input.SessionID,
			OriginalQuery: session.Query,
			Papers:        papers,
		}
		if err := workflow.ExecuteActivity(ctx, "SufficiencyActivity", suffIn).Get(ctx, &suffOut); err != nil {
			return nil, err
		}
		lastAnalysis = suffOut.Analysis

		// C. Reasoning Refresh Activity
		var refreshOut ReasoningRefreshOutput
		refreshIn := ReasoningRefreshInput{
			SessionID:     input.SessionID,
			Request:       LoopRequest{Query: session.Query},
			Papers:        papers,
			QueryCoverage: queryCoverage,
		}
		if err := workflow.ExecuteActivity(ctx, "ReasoningRefreshActivity", refreshIn).Get(ctx, &refreshOut); err != nil {
			return nil, err
		}
		logger.Info("Reasoning refreshed", "findings", len(refreshOut.Findings), "hypotheses", len(refreshOut.Hypotheses))

		// Check for convergence
		if lastAnalysis != nil && lastAnalysis.Sufficient {
			logger.Info("Sufficiency reached, breaking loop")
			break
		}

		// Update Session State (Checkpoint)
		session.BeliefState = nil // Manager will rebuild it next time
		if err := workflow.ExecuteActivity(ctx, "UpdateSessionActivity", &session).Get(ctx, nil); err != nil {
			logger.Warn("Failed to checkpoint session", "error", err)
		}
	}

	// 4. Synthesis Activity
	var finalAnswer string
	synthIn := SufficiencyInput{
		SessionID:     input.SessionID,
		OriginalQuery: session.Query,
		Papers:        papers,
	}
	if err := workflow.ExecuteActivity(ctx, "SynthesisActivity", synthIn).Get(ctx, &finalAnswer); err != nil {
		return nil, err
	}

	return &SessionWorkflowResult{
		SessionID: input.SessionID,
		Status:    "complete",
	}, nil
}

func (a *temporalActivities) ExecuteSessionActivity(ctx context.Context, input SessionWorkflowInput) (*SessionWorkflowResult, error) {
	session, err := executeSessionRun(ctx, a.gateway, input.SessionID)
	if err != nil {
		return nil, err
	}
	pendingApproval := session != nil && session.Plan != nil && strings.TrimSpace(session.Plan.PendingApprovalID) != ""
	return &SessionWorkflowResult{
		SessionID:       input.SessionID,
		Status:          session.Status,
		PendingApproval: pendingApproval,
	}, nil
}

func (a *temporalActivities) GetSessionActivity(ctx context.Context, sessionID string) (*AgentSession, error) {
	return a.gateway.Store.Get(ctx, sessionID)
}

func (a *temporalActivities) UpdateSessionActivity(ctx context.Context, session *AgentSession) error {
	return a.gateway.Store.Put(ctx, session, a.gateway.SessionTTL)
}

func temporalWorkflowID(sessionID string) string {
	return "wisdev-session-" + strings.TrimSpace(sessionID)
}
