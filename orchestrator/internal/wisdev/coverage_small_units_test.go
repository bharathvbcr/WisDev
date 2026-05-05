package wisdev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
)

func TestRewriteWisdevRequestPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/rewritten", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rewritten" || r.URL.RawPath != "/rewritten" {
			t.Fatalf("expected rewritten path, got path=%q raw=%q", r.URL.Path, r.URL.RawPath)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodGet, "/original?q=1", nil)
	rec := httptest.NewRecorder()

	rewriteWisdevRequestPath(mux, "/rewritten", rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}
	if req.URL.Path != "/original" {
		t.Fatalf("expected original request path to remain unchanged, got %q", req.URL.Path)
	}
}

func TestIdempotencyStoreRoundTripAndExpiry(t *testing.T) {
	store := NewIdempotencyStore(15 * time.Millisecond)
	store.Put("ok", http.StatusCreated, map[string]any{"value": "saved"})

	status, body, ok := store.Get("ok")
	if !ok {
		t.Fatal("expected cached record")
	}
	if status != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, status)
	}

	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if payload["value"] != "saved" {
		t.Fatalf("unexpected payload: %#v", payload)
	}

	body[0] = '{'
	_, bodyAgain, ok := store.Get("ok")
	if !ok {
		t.Fatal("expected cached record on second read")
	}
	if string(bodyAgain) != `{"value":"saved"}` {
		t.Fatalf("expected defensive copy, got %q", string(bodyAgain))
	}

	time.Sleep(20 * time.Millisecond)
	if _, _, ok := store.Get("ok"); ok {
		t.Fatal("expected expired record to be evicted")
	}
}

func TestIdempotencyStoreSkipsUnmarshalablePayload(t *testing.T) {
	store := NewIdempotencyStore(time.Minute)
	store.Put("bad", http.StatusOK, map[string]any{"ch": make(chan int)})
	if _, _, ok := store.Get("bad"); ok {
		t.Fatal("expected marshal failure to skip cache write")
	}
}

func TestIdempotencyStoreMissingKeyAndNilPayload(t *testing.T) {
	store := NewIdempotencyStore(time.Minute)

	if _, _, ok := store.Get("missing"); ok {
		t.Fatal("expected missing key to return not found")
	}

	store.Put("nil", http.StatusOK, nil)
	status, body, ok := store.Get("nil")
	if !ok {
		t.Fatal("expected nil payload to be cached")
	}
	if status != http.StatusOK {
		t.Fatalf("unexpected status for nil payload: %d", status)
	}
	if string(body) != "null" {
		t.Fatalf("expected json null payload body, got %q", string(body))
	}
}

func TestDetectExpertiseLevelAdditionalHeuristics(t *testing.T) {
	if got := DetectExpertiseLevel(""); got != ExpertiseBeginner {
		t.Fatalf("empty query should be beginner, got %s", got)
	}
	if got := DetectExpertiseLevel("what is GAN?"); got != ExpertiseBeginner {
		t.Fatalf("basic question should be beginner, got %s", got)
	}
	expertQuery := "Compare transformer attention mechanism versus CNN benchmarks on ImageNet with BLEU and ROUGE analysis"
	if got := DetectExpertiseLevel(expertQuery); got != ExpertiseExpert {
		t.Fatalf("technical comparative query should be expert, got %s", got)
	}
	if got := DetectExpertiseLevel("compare Bayesian priors in experiments"); got != ExpertiseIntermediate && got != ExpertiseExpert {
		t.Fatalf("expected non-beginner for technical comparative query, got %s", got)
	}
}

func TestExtractDiscoverySignalsLimitsDedupesAndNormalizes(t *testing.T) {
	text := "AlphaFoldModel appears in arXiv:2401.12345 by Smith et al. AlphaFoldModel is compared again."

	resp := ExtractDiscoverySignals(text, 50)

	if len(resp.Signals) != 3 {
		t.Fatalf("expected 3 unique signals, got %d (%v)", len(resp.Signals), resp.Signals)
	}
	if resp.Signals[0] != "model:AlphaFoldModel" {
		t.Fatalf("unexpected first signal: %v", resp.Signals[0])
	}
	if resp.Signals[1] != "arxiv:2401.12345" {
		t.Fatalf("unexpected second signal: %v", resp.Signals[1])
	}
	if resp.Signals[2] != "author:Smith" {
		t.Fatalf("unexpected third signal: %v", resp.Signals[2])
	}

	minResp := ExtractDiscoverySignals(text, 0)
	if len(minResp.Signals) != 3 {
		t.Fatalf("expected default limit to retain signals, got %d", len(minResp.Signals))
	}
}

func TestBuildDecisionCandidatesAndParallelSelection(t *testing.T) {

	cfg := policy.DefaultPolicyConfig()
	budget := policy.BudgetState{
		MaxToolCalls:  10,
		MaxScriptRuns: 5,
		MaxCostCents:  100,
	}
	plan := &PlanState{
		CompletedStepIDs: map[string]bool{"done": true},
		FailedStepIDs:    map[string]string{"failed": "boom"},
		Steps: []PlanStep{
			{
				ID:                 "done",
				Action:             "retrieve existing evidence",
				Risk:               RiskLevelLow,
				ExecutionTarget:    ExecutionTargetGoNative,
				EstimatedCostCents: 1,
			},
			{
				ID:               "blocked",
				Action:           "verify dependent citation",
				Risk:             RiskLevelLow,
				ExecutionTarget:  ExecutionTargetGoNative,
				DependsOnStepIDs: []string{"missing"},
			},
			{
				ID:                 "failed",
				Action:             "retrieve failed branch",
				Risk:               RiskLevelLow,
				ExecutionTarget:    ExecutionTargetGoNative,
				EstimatedCostCents: 1,
			},
			{
				ID:                 "parallel",
				Action:             "retrieve supporting evidence",
				Risk:               RiskLevelLow,
				ExecutionTarget:    ExecutionTargetGoNative,
				Parallelizable:     true,
				EstimatedCostCents: 5,
			},
			{
				ID:                 "approval",
				Action:             "verify contested citation",
				Risk:               RiskLevelMedium,
				ExecutionTarget:    ExecutionTargetGoNative,
				Parallelizable:     true,
				EstimatedCostCents: 5,
			},
			{
				ID:                 "dependent",
				Action:             "claim synthesis",
				Risk:               RiskLevelLow,
				ExecutionTarget:    ExecutionTargetGoNative,
				DependsOnStepIDs:   []string{"done"},
				EstimatedCostCents: 5,
			},
			{
				ID:                 "denied",
				Action:             "draft expensive report",
				Risk:               RiskLevelLow,
				ExecutionTarget:    ExecutionTargetGoNative,
				EstimatedCostCents: 500,
			},
		},
	}

	candidates := BuildDecisionCandidates(plan, budget, cfg)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 actionable candidates, got %d", len(candidates))
	}
	if candidates[0].StepID != "parallel" {
		t.Fatalf("expected parallel step to rank first, got %s", candidates[0].StepID)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.StepID] = true
	}
	if !seen["dependent"] || !seen["approval"] {
		t.Fatalf("expected dependent and approval candidates to remain, got %#v", candidates)
	}
	if !candidates[2].RequiresApproval || candidates[2].StepID != "approval" {
		t.Fatalf("expected medium-risk step to require approval, got %#v", candidates[2])
	}

	selected := SelectParallelCandidates(plan, candidates, 2)
	if len(selected) != 1 || selected[0] != "parallel" {
		t.Fatalf("expected only approval-free parallel step, got %v", selected)
	}
	if got := SelectParallelCandidates(nil, candidates, 2); got != nil {
		t.Fatalf("expected nil selection for nil plan, got %v", got)
	}
}

func TestTransitionSessionStatusAdditionalCases(t *testing.T) {
	session := &AgentSession{Status: SessionQuestioning}
	before := session.UpdatedAt

	if err := transitionSessionStatus(session, SessionGeneratingTree); err != nil {
		t.Fatalf("expected valid transition, got %v", err)
	}
	if session.Status != SessionGeneratingTree {
		t.Fatalf("expected status update, got %s", session.Status)
	}
	if session.UpdatedAt < before {
		t.Fatalf("expected updated timestamp, got before=%d after=%d", before, session.UpdatedAt)
	}

	unchanged := session.UpdatedAt
	if err := transitionSessionStatus(session, SessionGeneratingTree); err != nil {
		t.Fatalf("same-state transition should be allowed, got %v", err)
	}
	if session.UpdatedAt != unchanged {
		t.Fatalf("expected unchanged timestamp on no-op transition, got before=%d after=%d", unchanged, session.UpdatedAt)
	}

	if err := transitionSessionStatus(session, SessionQuestioning); err == nil {
		t.Fatal("expected invalid reverse transition to fail")
	}
}

func TestTaskQueueSubmitShutdownAndPanicRecovery(t *testing.T) {
	queue := NewTaskQueue(1, 1)
	defer queue.Shutdown()

	var ran sync.WaitGroup
	ran.Add(1)
	if err := queue.Submit(func() {
		ran.Done()
	}); err != nil {
		t.Fatalf("submit should succeed, got %v", err)
	}
	ran.Wait()

	fullQueue := &TaskQueue{tasks: make(chan func(), 1)}
	if err := fullQueue.Submit(func() {}); err != nil {
		t.Fatalf("first submit should fit buffer, got %v", err)
	}
	if err := fullQueue.Submit(func() {}); err == nil {
		t.Fatal("expected full queue error")
	}

	worker := &AutonomousWorker{
		queue: NewTaskQueue(1, 1),
	}
	defer worker.queue.Shutdown()

	done := make(chan error, 1)
	if err := worker.RunAsync(context.Background(), LoopRequest{Query: "panic"}, func(PlanExecutionEvent) {}, func(_ *LoopResult, err error) {
		done <- err
	}); err != nil {
		t.Fatalf("RunAsync submit failed: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected panic recovery error from nil loop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for panic recovery callback")
	}

	nilCtxWorker := &AutonomousWorker{
		loop:  nil,
		queue: NewTaskQueue(1, 1),
	}
	defer nilCtxWorker.queue.Shutdown()
	done = make(chan error, 1)
	if err := nilCtxWorker.RunAsync(context.Background(), LoopRequest{Query: "panic"}, func(PlanExecutionEvent) {}, func(_ *LoopResult, err error) {
		done <- err
	}); err != nil {
		t.Fatalf("RunAsync with nil context submit failed: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for nil-context callback")
	}
}
