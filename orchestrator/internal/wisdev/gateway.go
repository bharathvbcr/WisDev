package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/rag"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
)

type ExecutionRunner interface {
	RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult
	CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error)
	Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent)
}

type AgentGateway struct {
	Store         SessionStore
	Checkpoints   CheckpointStore
	Registry      *ToolRegistry
	ADKRuntime    *ADKRuntime
	Executor      ExecutionRunner
	LLMClient     *llm.Client
	PythonExecute func(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error)
	Idempotency   *IdempotencyStore
	PolicyConfig  policy.PolicyConfig
	SessionTTL    time.Duration
	CheckpointTTL time.Duration
	Journal       *RuntimeJournal
	DB            DBProvider
	StateStore    *RuntimeStateStore
	Redis         redis.UniversalClient

	// New modular WisDev components
	WisdevSessions *SessionManager
	WisdevGuided   *GuidedFlow
	Brain          *BrainCapabilities
	Gate           *rag.EvidenceGate
	Loop           *AutonomousLoop
}

const defaultPythonSidecarURL = "http://127.0.0.1:8090"

func ResolvePythonBase() string {
	for _, key := range []string{"PYTHON_SIDECAR_URL"} {
		if explicit := strings.TrimSpace(os.Getenv(key)); explicit != "" {
			return strings.TrimSuffix(explicit, "/")
		}
	}
	return defaultPythonSidecarURL
}

func (gw *AgentGateway) defaultPythonExecutor(ctx context.Context, action string, payload map[string]any, session *AgentSession) (map[string]any, error) {
	// 1. Check if the action can be handled natively by Go BrainCapabilities, EvidenceGate, or AutonomousLoop
	switch action {
	case "research.queryDecompose":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		domain, _ := payload["domain"].(string)
		model, _ := payload["model"].(string)
		tasks, err := gw.Brain.DecomposeTask(ctx, query, domain, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tasks": tasks}, nil

	case "research.proposeHypotheses":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		intent, _ := payload["intent"].(string)
		model, _ := payload["model"].(string)
		hypotheses, err := gw.Brain.ProposeHypotheses(ctx, query, intent, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"hypotheses": hypotheses}, nil

	case "research.coordinateReplan":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		failedID, _ := payload["failedStepId"].(string)
		reason, _ := payload["reason"].(string)
		contextData, _ := payload["context"].(map[string]any)
		model, _ := payload["model"].(string)
		tasks, err := gw.Brain.CoordinateReplan(ctx, failedID, reason, contextData, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tasks": tasks}, nil

	case "research.synthesize-answer":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		model, _ := payload["model"].(string)

		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				abs := ""
				if a, ok := pm["abstract"].(string); ok && a != "" {
					abs = a
				} else if s, ok := pm["summary"].(string); ok {
					abs = s
				}
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: abs,
				})
			}
		}

		answer, err := gw.Brain.SynthesizeAnswer(ctx, query, papers, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": answer}, nil

	case "research.evaluate-evidence":
		if gw.Gate == nil {
			return nil, fmt.Errorf("EvidenceGate not initialised for action %s", action)
		}
		text, _ := payload["synthesis_text"].(string)
		papersRaw, _ := payload["sources"].([]any)

		var papers []search.Paper
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				abs := ""
				if a, ok := pm["abstract"].(string); ok && a != "" {
					abs = a
				} else if s, ok := pm["summary"].(string); ok {
					abs = s
				}
				papers = append(papers, search.Paper{
					ID:       fmt.Sprintf("%v", pm["paperId"]),
					Title:    fmt.Sprintf("%v", pm["title"]),
					Abstract: abs,
				})
			}
		}

		result, err := gw.Gate.Run(ctx, text, papers)
		if err != nil {
			return nil, err
		}

		// Convert result to map for JSON compatibility
		b, _ := json.Marshal(result)
		var m map[string]any
		json.Unmarshal(b, &m)
		return m, nil

	case "research.execute-loop":
		if gw.Loop == nil {
			return nil, fmt.Errorf("AutonomousLoop not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		domain, _ := payload["domain"].(string)
		maxIter, _ := payload["maxIterations"].(float64)
		budget, _ := payload["budgetCents"].(float64)

		loopReq := LoopRequest{
			Query:         query,
			Domain:        domain,
			MaxIterations: int(maxIter),
			BudgetCents:   int(budget),
		}

		result, err := gw.Loop.Run(ctx, loopReq)
		if err != nil {
			return nil, err
		}

		// Convert result to map
		b, _ := json.Marshal(result)
		var m map[string]any
		json.Unmarshal(b, &m)
		return m, nil

	case "research.verifyCitations":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:    fmt.Sprintf("%v", pm["id"]),
					Title: fmt.Sprintf("%v", pm["title"]),
					DOI:   AsOptionalString(pm["doi"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyCitations(ctx, papers, model)

	case "research.snowballCitations":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)
		
		var allNewPapers []Source
		// For each seed paper, fetch its citations from the graph
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				paperID := fmt.Sprintf("%v", pm["id"])
				
				// Real Citation Graph Lookup via Registry
				citations, err := gw.Loop.searchReg.GetCitations(ctx, paperID, 5)
				if err != nil {
					slog.Warn("Citation lookup failed for paper", "id", paperID, "error", err)
					continue
				}

				for _, c := range citations {
					allNewPapers = append(allNewPapers, Source{
						ID:            c.ID,
						Title:         c.Title,
						Summary:       c.Abstract,
						Link:          c.Link,
						Source:        c.Source,
						DOI:           c.DOI,
						CitationCount: c.CitationCount,
						Year:          c.Year,
					})
				}
			}
		}

		// If we found real citations, return them. 
		// If not, fallback to LLM-generated exploratory queries.
		if len(allNewPapers) > 0 {
			return map[string]any{"papers": allNewPapers}, nil
		}

		// Fallback to query generation
		var seedPapers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				seedPapers = append(seedPapers, Source{
					ID:    fmt.Sprintf("%v", pm["id"]),
					Title: fmt.Sprintf("%v", pm["title"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		queries, err := gw.Brain.GenerateSnowballQueries(ctx, seedPapers, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"exploratory_queries": queries}, nil

	case "research.buildClaimEvidenceTable":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.BuildClaimEvidenceTable(ctx, query, papers, model)

	case "research.generateThoughts":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		model, _ := payload["model"].(string)
		return gw.Brain.GenerateThoughts(ctx, payload, model)

	case "research.detectContradictions":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.DetectContradictions(ctx, papers, model)

	case "research.verifyClaims":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		text, _ := payload["synthesis_text"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.VerifyClaims(ctx, text, papers, model)

	case "research.systematicReviewPrisma", "review.systematicReviewPrisma":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.SystematicReviewPrisma(ctx, query, papers, model)

	case "query.enhanceAcademic":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		model, _ := payload["model"].(string)
		enhanced, err := gw.Brain.EnhanceAcademicQuery(ctx, query, model)
		if err != nil {
			return nil, err
		}
		return map[string]any{"enhanced_query": enhanced}, nil

	case "retrieval.selectPrimarySource":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		papersRaw, _ := payload["papers"].([]any)
		var papers []Source
		for _, p := range papersRaw {
			if pm, ok := p.(map[string]any); ok {
				papers = append(papers, Source{
					ID:      fmt.Sprintf("%v", pm["id"]),
					Title:   fmt.Sprintf("%v", pm["title"]),
					Summary: AsOptionalString(pm["abstract"]),
				})
			}
		}
		model, _ := payload["model"].(string)
		return gw.Brain.SelectPrimarySource(ctx, query, papers, model)

	case "clarify.askFollowUpIfAmbiguous":
		if gw.Brain == nil {
			return nil, fmt.Errorf("BrainCapabilities not initialised for action %s", action)
		}
		query, _ := payload["query"].(string)
		model, _ := payload["model"].(string)
		return gw.Brain.AskFollowUpIfAmbiguous(ctx, query, model)
	}

	// 2. Fallback to Python HTTP for remaining specialized endpoints (if any)
	// Currently all core research actions are moved to Go.
	// We keep this for future expansion or non-core tasks.
	return nil, fmt.Errorf("unknown action: %s", action)
}

func NewAgentGateway(db DBProvider, rdb redis.UniversalClient, journal *RuntimeJournal) *AgentGateway {
	policyCfg := policy.DefaultPolicyConfig()
	registry := NewToolRegistry()
	adkRuntime := LoadADKRuntime(registry)
	llmClient := llm.NewClient()
	memStore := NewInMemorySessionStore()
	memCheckpoints := NewInMemoryCheckpointStore()
	var sessionStore SessionStore = memStore
	var checkpointStore CheckpointStore = memCheckpoints

	if db != nil {
		sessionStore = NewPostgresSessionStore(db)
		if rdb != nil {
			sessionStore = NewFallbackSessionStore(sessionStore, NewRedisSessionStore(rdb))
		}
		sessionStore = NewFallbackSessionStore(sessionStore, memStore)
	} else if rdb != nil {
		sessionStore = NewFallbackSessionStore(NewRedisSessionStore(rdb), memStore)
		checkpointStore = NewFallbackCheckpointStore(NewRedisCheckpointStore(rdb), memCheckpoints)
	}

	idempotencyWindow := 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("WISDEV_IDEMPOTENCY_WINDOW_MINUTES")); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes >= 1 && minutes <= 120 {
			idempotencyWindow = time.Duration(minutes) * time.Minute
		}
	}
	gw := &AgentGateway{
		Store:          sessionStore,
		Checkpoints:    checkpointStore,
		Registry:       registry,
		ADKRuntime:     adkRuntime,
		LLMClient:      llmClient,
		Idempotency:    NewIdempotencyStore(idempotencyWindow),
		PolicyConfig:   policyCfg,
		SessionTTL:     24 * time.Hour,
		CheckpointTTL:  30 * 24 * time.Hour,
		Journal:        journal,
		DB:             db,
		StateStore:     NewRuntimeStateStore(db, journal),
		Redis:          rdb,
		WisdevSessions: NewSessionManager(""),
		WisdevGuided:   NewGuidedFlow(),
		Brain:          NewBrainCapabilities(llmClient),
		Gate:           rag.NewEvidenceGate(llmClient),
		Loop:           NewAutonomousLoop(search.NewProviderRegistry(), llmClient),
	}
	if gw.ADKRuntime != nil {
		gw.ADKRuntime.Bind(context.Background(), gw)
	}
	gw.PythonExecute = gw.defaultPythonExecutor
	gw.Executor = NewPlanExecutor(registry, policyCfg, llmClient, gw.Brain, rdb, gw.PythonExecute, adkRuntime)
	return gw
}

func (gw *AgentGateway) RuntimeMetadata() map[string]any {
	if gw == nil || gw.ADKRuntime == nil {
		return map[string]any{
			"enabled": false,
		}
	}
	meta := gw.ADKRuntime.Metadata()
	if card := gw.ADKRuntime.BuildA2ACard(); card != nil {
		meta["a2aCard"] = card
	}
	return meta
}

func (gw *AgentGateway) ensureADKSession(sessionID string, query string, domain string) *AgentSession {
	now := time.Now().UnixMilli()
	if strings.TrimSpace(sessionID) == "" {
		sessionID = NewTraceID()
	}
	sessionState, err := gw.GetSession(context.Background(), sessionID)
	if err == nil && sessionState != nil {
		return sessionState
	}
	return &AgentSession{
		SchemaVersion:  "adk-go-v1",
		PolicyVersion:  gw.PolicyConfig.PolicyVersion,
		SessionID:      sessionID,
		OriginalQuery:  strings.TrimSpace(query),
		CorrectedQuery: strings.TrimSpace(query),
		DetectedDomain: strings.TrimSpace(domain),
		Status:         SessionQuestioning,
		Answers:        map[string]QuestionAnswer{},
		FailureMemory:  map[string]int{},
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func (gw *AgentGateway) ExecuteADKAction(ctx context.Context, toolDef ToolDefinition, payload map[string]any, sessionState *AgentSession) (map[string]any, error) {
	if sessionState == nil {
		sessionState = gw.ensureADKSession("", AsOptionalString(payload["query"]), AsOptionalString(payload["domain"]))
	}
	switch toolDef.ExecutionTarget {
	case ExecutionTargetGoNative:
		query := strings.TrimSpace(AsOptionalString(payload["query"]))
		if query == "" {
			query = resolveSessionQuery(sessionState)
		}
		limit := 10
		if rawLimit, ok := payload["limit"].(float64); ok && int(rawLimit) > 0 {
			limit = int(rawLimit)
		}
		papers, err := FastParallelSearch(ctx, gw.Redis, query, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"papers": papers,
			"query":  query,
			"count":  len(papers),
		}, nil
	default:
		return gw.defaultPythonExecutor(ctx, toolDef.Name, payload, sessionState)
	}
}

func (gw *AgentGateway) CreateSession(ctx context.Context, userID string, query string) (*AgentSession, error) {
	sessionID := NewTraceID()
	now := time.Now().UnixMilli()
	session := &AgentSession{
		SessionID:     sessionID,
		UserID:        userID,
		OriginalQuery: query,
		Status:        SessionQuestioning,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := gw.Store.Put(ctx, session, gw.SessionTTL); err != nil {
		return nil, err
	}
	return session, nil
}

func (gw *AgentGateway) GetSession(ctx context.Context, sessionID string) (*AgentSession, error) {
	return gw.Store.Get(ctx, sessionID)
}
