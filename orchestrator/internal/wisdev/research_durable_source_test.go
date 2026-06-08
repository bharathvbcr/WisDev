package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

func TestResearchSourceAcquisitionPlanTracksPDFWorkerAndFetchFailure(t *testing.T) {
	plan := buildResearchSourceAcquisitionPlan("transformer interpretability replication", []search.Paper{
		{
			ID:      "arxiv:2401.12345",
			Title:   "Transformer Interpretability Replication",
			ArxivID: "2401.12345",
			Source:  "arxiv",
			PdfUrl:  "https://arxiv.org/pdf/2401.12345.pdf",
		},
		{
			Title:  "Unresolvable Workshop Abstract",
			Source: "workshop",
		},
	})

	if plan == nil {
		t.Fatalf("expected source acquisition plan")
	}
	if len(plan.Attempts) != 3 {
		t.Fatalf("expected 3 acquisition attempts, got %d: %#v", len(plan.Attempts), plan.Attempts)
	}
	if plan.RequiredPythonExtractions != 1 {
		t.Fatalf("expected one Python PDF extraction, got %d", plan.RequiredPythonExtractions)
	}
	if plan.FetchFailures != 1 {
		t.Fatalf("expected one fetch failure, got %d", plan.FetchFailures)
	}
	if countOpenCoverageLedgerEntries(plan.CoverageLedger) != 2 {
		t.Fatalf("expected full-text and fetch-failure ledger gaps, got %#v", plan.CoverageLedger)
	}
	if !sourceAcquisitionHasAttempt(plan, "pdf", "python_docling") {
		t.Fatalf("expected PDF attempt assigned to Python worker, got %#v", plan.Attempts)
	}
}

func TestResearchDurableJobPersistsBudgetAccounting(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	store := NewRuntimeStateStore(nil, nil)
	rt := NewUnifiedResearchRuntime(nil, nil, nil, nil).WithDurableResearchState(store, nil)
	state := newResearchSessionState("oncology biomarker reproducibility", "medicine", "sess_durable", ResearchExecutionPlaneDeep)
	state.Budget = &ResearchBudgetDecision{
		SearchTermBudget:     1,
		WorkerSearchBudget:   2,
		FollowUpSearchBudget: 3,
		RecursiveGapDepth:    2,
	}
	state.PlannedQueries = []string{"oncology biomarker reproducibility"}
	state.SourceAcquisition = buildResearchSourceAcquisitionPlan(state.Query, []search.Paper{{
		ID:     "doi:10.1000/example",
		Title:  "Cohort Validation",
		DOI:    "10.1000/example",
		Source: "pubmed",
	}})
	state.CoverageLedger = []CoverageLedgerEntry{{
		ID:       "gap_budget",
		Category: "budget",
		Status:   coverageLedgerStatusOpen,
		Title:    "Budget exhausted before coverage closure",
	}}
	state.DurableJob = newResearchDurableJobState(state, LoopRequest{
		Query:       state.Query,
		Domain:      state.Domain,
		SeedQueries: state.PlannedQueries,
	})
	loopResult := &LoopResult{
		Papers:          []search.Paper{{ID: "doi:10.1000/example", Title: "Cohort Validation", DOI: "10.1000/example", Source: "pubmed"}},
		ExecutedQueries: []string{"oncology biomarker reproducibility"},
		GapAnalysis:     &LoopGapState{Ledger: state.CoverageLedger},
	}
	completeResearchDurableJob(state.DurableJob, state, loopResult)
	if err := rt.persistResearchDurableJob(state, "research_job_completed"); err != nil {
		t.Fatalf("persistResearchDurableJob returned error: %v", err)
	}

	loaded, err := store.LoadResearchJob(state.DurableJob.JobID)
	if err != nil {
		t.Fatalf("LoadResearchJob returned error: %v", err)
	}
	if loaded["status"] != researchDurableJobStatusCompleted {
		t.Fatalf("expected completed status, got %#v", loaded["status"])
	}
	if loaded["storage"] != "runtime_state_store" {
		t.Fatalf("expected runtime_state_store storage, got %#v", loaded["storage"])
	}
	budgetUsed, ok := loaded["budgetUsed"].(map[string]any)
	if !ok {
		t.Fatalf("expected budgetUsed map, got %#v", loaded["budgetUsed"])
	}
	if budgetUsed["exhausted"] != true {
		t.Fatalf("expected exhausted budget accounting, got %#v", budgetUsed)
	}
}

func TestResearchDurableJobUsesExplicitRuntimeJobID(t *testing.T) {
	state := newResearchSessionState("durable job identity", "systems", "session-explicit-job", ResearchExecutionPlaneJob)
	state.DurableJob = newResearchDurableJobState(state, LoopRequest{
		Query:        state.Query,
		Domain:       state.Domain,
		DurableJobID: "wisdev-job-route-id",
	})

	if state.DurableJob == nil {
		t.Fatalf("expected durable job state")
	}
	if state.DurableJob.JobID != "wisdev-job-route-id" {
		t.Fatalf("expected explicit durable job id, got %q", state.DurableJob.JobID)
	}
	if state.DurableJob.ResumeToken != "session-explicit-job" {
		t.Fatalf("expected resume token to stay session-scoped, got %q", state.DurableJob.ResumeToken)
	}
}

func sourceAcquisitionHasAttempt(plan *ResearchSourceAcquisitionPlan, sourceType string, workerPlane string) bool {
	if plan == nil {
		return false
	}
	for _, attempt := range plan.Attempts {
		if attempt.SourceType == sourceType && attempt.WorkerPlane == workerPlane {
			return true
		}
	}
	return false
}
