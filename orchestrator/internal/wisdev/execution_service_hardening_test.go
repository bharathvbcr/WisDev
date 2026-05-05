package wisdev

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type durableContextKey string

func newIsolatedRuntimeJournal(t *testing.T) *RuntimeJournal {
	t.Helper()
	t.Setenv("WISDEV_JOURNAL_PATH", filepath.Join(t.TempDir(), "wisdev_journal.jsonl"))
	return NewRuntimeJournal(nil)
}

type countingExecutionRunner struct {
	started chan struct{}
	calls   int
}

func (r *countingExecutionRunner) RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult {
	return StepResult{}
}

func (r *countingExecutionRunner) CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error) {
	return "CONTINUE", nil
}

func (r *countingExecutionRunner) Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent) {
	r.calls++
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	<-ctx.Done()
	out <- PlanExecutionEvent{
		Type:      EventProgress,
		TraceID:   NewTraceID(),
		SessionID: session.SessionID,
		Message:   "stopped",
		CreatedAt: NowMillis(),
	}
}

func TestDurableExecutionServiceStartIsSingleFlightPerSession(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	runner := &countingExecutionRunner{started: make(chan struct{}, 1)}
	gateway.Executor = runner
	service := NewDurableExecutionService(gateway)
	gateway.Execution = service

	session := &AgentSession{
		SessionID:     "single-flight",
		UserID:        "u1",
		OriginalQuery: "sleep memory consolidation",
		Status:        SessionPaused,
		Plan: &PlanState{
			PlanID:           "plan-1",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	first, err := service.Start(context.Background(), session.SessionID)
	require.NoError(t, err)
	require.False(t, first.AlreadyRunning)
	<-runner.started

	second, err := service.Start(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.True(t, second.AlreadyRunning)
	assert.Equal(t, 1, runner.calls)

	require.NoError(t, service.Cancel(context.Background(), session.SessionID))
}

func TestDurableExecutionServiceStartRejectsMissingSessionQuery(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	runner := &countingExecutionRunner{started: make(chan struct{}, 1)}
	gateway.Executor = runner
	service := NewDurableExecutionService(gateway)

	session := &AgentSession{
		SessionID: "missing-query",
		UserID:    "u1",
		Status:    SessionPaused,
		Plan: &PlanState{
			PlanID:           "plan-missing-query",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	result, err := service.Start(context.Background(), session.SessionID)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "session query is required")
	assert.Equal(t, 0, runner.calls)
	assert.False(t, service.IsActive(session.SessionID))
}

func TestDurableExecutionServiceStartAcceptsPlanningQuery(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	runner := &countingExecutionRunner{started: make(chan struct{}, 1)}
	gateway.Executor = runner
	service := NewDurableExecutionService(gateway)
	gateway.Execution = service

	session := &AgentSession{
		SessionID: "planning-query",
		UserID:    "u1",
		Query:     "planner canonical query",
		Status:    SessionPaused,
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	result, err := service.Start(context.Background(), session.SessionID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.AlreadyRunning)
	<-runner.started

	require.NoError(t, service.Cancel(context.Background(), session.SessionID))
}

func TestNewApprovalTokenProducesDistinctHashes(t *testing.T) {
	tokenA, hashA, err := NewApprovalToken()
	require.NoError(t, err)
	tokenB, hashB, err := NewApprovalToken()
	require.NoError(t, err)

	assert.NotEmpty(t, tokenA)
	assert.NotEmpty(t, tokenB)
	assert.NotEqual(t, tokenA, tokenB)
	assert.Equal(t, HashApprovalToken(tokenA), hashA)
	assert.Equal(t, HashApprovalToken(tokenB), hashB)
	assert.NotEqual(t, hashA, hashB)
}

func TestDurableExecutionServiceStreamReplaysJournalWithoutExecution(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Journal = newIsolatedRuntimeJournal(t)
	session := &AgentSession{
		SessionID: "stream-session",
		UserID:    "u1",
		Status:    SessionComplete,
		Plan: &PlanState{
			PlanID:           "plan-stream",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))
	appendExecutionEvent(gateway, session, PlanExecutionEvent{
		Type:      EventCompleted,
		TraceID:   "trace-stream",
		SessionID: session.SessionID,
		PlanID:    session.Plan.PlanID,
		Message:   "done",
		CreatedAt: NowMillis(),
	})

	service := NewDurableExecutionService(gateway)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var events []PlanExecutionEvent
	err := service.Stream(ctx, session.SessionID, func(event PlanExecutionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, EventCompleted, events[0].Type)
	assert.Equal(t, "trace-stream", events[0].EventID)
	assert.Equal(t, "trace-stream", events[0].TraceID)
	assert.Equal(t, "trace-stream", events[0].Payload["eventId"])
	assert.Equal(t, "done", events[0].Message)
}

func TestDurableExecutionServiceStreamSynthesizesCompletedEventWhenSessionIsTerminalWithoutJournalEvent(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Journal = newIsolatedRuntimeJournal(t)
	session := &AgentSession{
		SessionID:     "stream-complete-synthetic",
		UserID:        "u1",
		OriginalQuery: "reward modeling stability",
		Status:        SessionComplete,
		Plan: &PlanState{
			PlanID:           "plan-complete-synthetic",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	service := NewDurableExecutionService(gateway)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var events []PlanExecutionEvent
	err := service.Stream(ctx, session.SessionID, func(event PlanExecutionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, EventCompleted, events[0].Type)
	assert.Equal(t, "Plan completed", events[0].Message)
	assert.Equal(t, "completed", events[0].Payload["status"])
	assert.Equal(t, true, events[0].Payload["synthetic"])
}

func TestDurableExecutionServiceStreamSynthesizesFailedEventWhenSessionFailsWithoutJournalEvent(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Journal = newIsolatedRuntimeJournal(t)
	session := &AgentSession{
		SessionID:     "stream-failed-synthetic",
		UserID:        "u1",
		OriginalQuery: "reward modeling stability",
		Status:        SessionFailed,
		Plan: &PlanState{
			PlanID:           "plan-failed-synthetic",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	service := NewDurableExecutionService(gateway)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var events []PlanExecutionEvent
	err := service.Stream(ctx, session.SessionID, func(event PlanExecutionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, EventStepFailed, events[0].Type)
	assert.Equal(t, "Plan failed", events[0].Message)
	assert.Equal(t, "failed", events[0].Payload["status"])
	assert.Equal(t, true, events[0].Payload["synthetic"])
}

func TestDurableExecutionServiceStreamSynthesizesCancelledEventWhenSessionIsAbandonedWithoutJournalEvent(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Journal = newIsolatedRuntimeJournal(t)
	session := &AgentSession{
		SessionID:     "stream-cancelled-synthetic",
		UserID:        "u1",
		OriginalQuery: "reward modeling stability",
		Status:        StatusAbandoned,
		Plan: &PlanState{
			PlanID:           "plan-cancelled-synthetic",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	service := NewDurableExecutionService(gateway)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var events []PlanExecutionEvent
	err := service.Stream(ctx, session.SessionID, func(event PlanExecutionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, EventPlanCancelled, events[0].Type)
	assert.Equal(t, "Plan cancelled", events[0].Message)
	assert.Equal(t, "cancelled", events[0].Payload["status"])
	assert.Equal(t, true, events[0].Payload["synthetic"])
}

func TestDurableExecutionServiceAbandonPersistsCancelledEventWithoutPausedEvent(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Journal = newIsolatedRuntimeJournal(t)
	service := NewDurableExecutionService(gateway)
	session := &AgentSession{
		SessionID:     "abandon-cancelled-event",
		UserID:        "u1",
		OriginalQuery: "reward modeling stability",
		Status:        SessionExecutingPlan,
		Plan: &PlanState{
			PlanID:           "plan-abandon-cancelled-event",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	require.NoError(t, service.Abandon(context.Background(), session.SessionID))

	loaded, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, StatusAbandoned, loaded.Status)

	entries := gateway.Journal.ReadSession(session.SessionID, 10)
	require.Len(t, entries, 1)
	assert.Equal(t, string(EventPlanCancelled), entries[0].EventType)
	assert.Equal(t, "Plan cancelled", entries[0].Summary)
	assert.Equal(t, "cancelled", entries[0].Payload["status"])
	assert.NotEqual(t, "execution paused", entries[0].Summary)
}

// failingExecutionRunner causes executeSessionRun to fail immediately by
// returning an error-type event. This exercises the P3-9 fix: run() must
// surface the error and mark the session as failed so the streaming poll
// can detect the terminal state instead of spinning indefinitely.
type failingExecutionRunner struct{}

func (r *failingExecutionRunner) RunStepWithRecovery(ctx context.Context, session *AgentSession, step PlanStep, laneID int) StepResult {
	return StepResult{Err: fmt.Errorf("step failed")}
}

func (r *failingExecutionRunner) CoordinateAgentFeedback(ctx context.Context, session *AgentSession, outcomes []PlanOutcome) (string, error) {
	return "STOP", nil
}

func (r *failingExecutionRunner) Execute(ctx context.Context, session *AgentSession, out chan<- PlanExecutionEvent) {
	out <- PlanExecutionEvent{
		Type:      EventStepFailed,
		TraceID:   NewTraceID(),
		SessionID: session.SessionID,
		Message:   "executor failed",
		CreatedAt: NowMillis(),
	}
}

func TestDurableExecutionService_run_propagatesErrorToSessionStatus(t *testing.T) {
	// Regression for P3-9: run() previously discarded executeSessionRun errors
	// with `_, _`, leaving the session in whatever status it had before Start()
	// was called. A frontend streaming poll would then spin until its own
	// deadline expired with no way to know execution had failed. After the fix,
	// run() marks the session as SessionFailed when executeSessionRun returns
	// an error (e.g. because the session query was cleared between Start()'s
	// validation and executeSessionRun's own Get).

	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Executor = &failingExecutionRunner{}
	service := NewDurableExecutionService(gateway)
	gateway.Execution = service

	// Create a valid session so Start() succeeds (the query passes validation).
	session := &AgentSession{
		SessionID:     "p3-9-failing-run",
		UserID:        "u1",
		OriginalQuery: "some research query",
		Status:        SessionQuestioning,
		Plan: &PlanState{
			PlanID:           "plan-p3-9",
			ApprovedStepIDs:  map[string]bool{},
			CompletedStepIDs: map[string]bool{},
			FailedStepIDs:    map[string]string{},
			StepAttempts:     map[string]int{},
			StepFailureCount: map[string]int{},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	// Remove the query fields to simulate the TOCTOU window: Start() validated
	// the session but between Start() and run()'s Get call the session was
	// mutated to have an empty query. executeSessionRun returns an error,
	// which run() must surface by marking the session as SessionFailed.
	session.OriginalQuery = ""
	session.CorrectedQuery = ""
	session.Query = ""
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	// Restore OriginalQuery so Start() validation succeeds (it reads from the
	// store once before launching the goroutine, so we patch it back for the
	// Start() call only by reading fresh first).
	sessionForStart := &AgentSession{
		SessionID:     "p3-9-failing-run",
		UserID:        "u1",
		OriginalQuery: "some research query", // valid for Start()
		Status:        SessionQuestioning,
		Plan:          session.Plan,
		CreatedAt:     session.CreatedAt,
		UpdatedAt:     session.UpdatedAt,
	}
	require.NoError(t, gateway.Store.Put(context.Background(), sessionForStart, gateway.SessionTTL))

	// Start succeeds (query is valid in the store at this point).
	_, err := service.Start(context.Background(), session.SessionID)
	require.NoError(t, err)

	// Now overwrite the session in the store with an empty query so
	// executeSessionRun's Get() sees an empty query and returns an error.
	sessionNoQuery := &AgentSession{
		SessionID:      "p3-9-failing-run",
		UserID:         "u1",
		OriginalQuery:  "", // empty — triggers error in executeSessionRun
		CorrectedQuery: "",
		Query:          "",
		Status:         SessionQuestioning,
		Plan:           session.Plan,
		CreatedAt:      session.CreatedAt,
		UpdatedAt:      session.UpdatedAt,
	}
	require.NoError(t, gateway.Store.Put(context.Background(), sessionNoQuery, gateway.SessionTTL))

	// Wait for the goroutine to finish. run() will call executeSessionRun,
	// which will Get() the session with empty query and return an error.
	// The fix then marks the session as SessionFailed.
	deadline := time.Now().Add(5 * time.Second)
	var finalSession *AgentSession
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if service.IsActive(session.SessionID) {
			continue
		}
		finalSession, err = gateway.Store.Get(context.Background(), session.SessionID)
		require.NoError(t, err)
		break
	}
	require.NotNil(t, finalSession, "timed out waiting for run() to finish")
	assert.Equal(t, SessionFailed, finalSession.Status,
		"run() must mark session as SessionFailed so the streaming loop can terminate")
}

func TestDurableExecutionRootContextPreservesValuesWithoutCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), durableContextKey("trace"), "trace-123"))
	cancel()

	root := durableExecutionRootContext(ctx)
	assert.Equal(t, "trace-123", root.Value(durableContextKey("trace")))
	assert.NoError(t, root.Err())
}

func TestDurableExecutionServiceRunDoesNotOverwriteCancelledSessionAsFailed(t *testing.T) {
	gateway := NewAgentGateway(nil, nil, nil)
	gateway.Executor = &failingExecutionRunner{}
	service := NewDurableExecutionService(gateway)

	session := &AgentSession{
		SessionID: "cancelled-run",
		UserID:    "u1",
		Status:    SessionPaused,
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	require.NoError(t, gateway.Store.Put(context.Background(), session, gateway.SessionTTL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service.run(ctx, session.SessionID)

	loaded, err := gateway.Store.Get(context.Background(), session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, SessionPaused, loaded.Status)
}
