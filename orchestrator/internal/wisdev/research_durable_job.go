package wisdev

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

const (
	researchDurableJobStatusRunning   = "running"
	researchDurableJobStatusCompleted = "completed"
	researchDurableJobStatusFailed    = "failed"
	researchDurableJobStatusCancelled = "cancelled"
)

type ResearchBudgetUsage struct {
	PlannedQueries            int  `json:"plannedQueries"`
	ExecutedQueries           int  `json:"executedQueries"`
	WorkerSearchBudget        int  `json:"workerSearchBudget,omitempty"`
	FollowUpSearchBudget      int  `json:"followUpSearchBudget,omitempty"`
	RecursiveGapDepth         int  `json:"recursiveGapDepth,omitempty"`
	Papers                    int  `json:"papers"`
	Claims                    int  `json:"claims,omitempty"`
	OpenLedgerCount           int  `json:"openLedgerCount"`
	SourceAcquisitionAttempts int  `json:"sourceAcquisitionAttempts,omitempty"`
	FetchFailures             int  `json:"fetchFailures,omitempty"`
	PythonPDFExtractions      int  `json:"pythonPdfExtractions,omitempty"`
	Exhausted                 bool `json:"exhausted,omitempty"`
}

type ResearchDurableJobState struct {
	JobID                       string                         `json:"jobId"`
	SessionID                   string                         `json:"sessionId"`
	Query                       string                         `json:"query"`
	Domain                      string                         `json:"domain,omitempty"`
	Plane                       ResearchExecutionPlane         `json:"plane"`
	Mode                        string                         `json:"mode,omitempty"`
	Required                    bool                           `json:"required"`
	Status                      string                         `json:"status"`
	Storage                     string                         `json:"storage"`
	Replayable                  bool                           `json:"replayable"`
	ResumeSupported             bool                           `json:"resumeSupported"`
	CancellationSupported       bool                           `json:"cancellationSupported"`
	ResumeToken                 string                         `json:"resumeToken,omitempty"`
	StartedAt                   int64                          `json:"startedAt"`
	UpdatedAt                   int64                          `json:"updatedAt"`
	Budget                      *ResearchBudgetDecision        `json:"budget,omitempty"`
	BudgetUsed                  ResearchBudgetUsage            `json:"budgetUsed"`
	ReasoningRuntime            map[string]any                 `json:"reasoningRuntime,omitempty"`
	Tasks                       []ResearchDurableTaskState     `json:"tasks,omitempty"`
	StopReason                  string                         `json:"stopReason,omitempty"`
	FailureReason               string                         `json:"failureReason,omitempty"`
	DisableProgrammaticPlanning bool                           `json:"disableProgrammaticPlanning,omitempty"`
	DisableHypothesisGeneration bool                           `json:"disableHypothesisGeneration,omitempty"`
	SourceSummaries             []ResearchDurableSourceSummary `json:"sourceSummaries,omitempty"`
	// AttachedSourcesUsed is how many of the user's uploaded/connected papers
	// reached the research output, for provenance display.
	AttachedSourcesUsed int `json:"attachedSourcesUsed,omitempty"`
}

type ResearchDurableSourceSummary struct {
	SourceIDHash string `json:"sourceIdHash,omitempty"`
	// Attached marks a summary as a user-provided (uploaded or connected) source.
	Attached                 bool     `json:"attached,omitempty"`
	AuthorNames              []string `json:"authorNames,omitempty"`
	CitationCount            int      `json:"citationCount,omitempty"`
	InfluentialCitationCount int      `json:"influentialCitationCount,omitempty"`
	Year                     int      `json:"year,omitempty"`
	SourceApis               []string `json:"sourceApis,omitempty"`
}

func (rt *UnifiedResearchRuntime) WithDurableResearchState(store *RuntimeStateStore, journal *RuntimeJournal) *UnifiedResearchRuntime {
	if rt == nil {
		return nil
	}
	rt.stateStore = store
	rt.journal = journal
	return rt
}

func researchPlaneRequiresDurableJob(plane ResearchExecutionPlane) bool {
	switch plane {
	case ResearchExecutionPlaneDeep, ResearchExecutionPlaneMultiAgent, ResearchExecutionPlaneQuest, ResearchExecutionPlaneJob:
		return true
	default:
		return false
	}
}

func newResearchDurableJobState(state *ResearchSessionState, req LoopRequest) *ResearchDurableJobState {
	if state == nil || !researchPlaneRequiresDurableJob(state.Plane) {
		return nil
	}
	now := time.Now().UnixMilli()
	sessionID := strings.TrimSpace(state.SessionID)
	jobID := strings.TrimSpace(req.DurableJobID)
	if jobID == "" {
		jobID = stableWisDevID("research_job", sessionID, string(state.Plane), state.Query)
	}
	if sessionID == "" {
		sessionID = jobID
	}
	return &ResearchDurableJobState{
		JobID:                 jobID,
		SessionID:             sessionID,
		Query:                 strings.TrimSpace(state.Query),
		Domain:                strings.TrimSpace(state.Domain),
		Plane:                 state.Plane,
		Mode:                  string(NormalizeWisDevMode(req.Mode)),
		Required:              true,
		Status:                researchDurableJobStatusRunning,
		Storage:               "pending",
		Replayable:            true,
		ResumeSupported:       true,
		CancellationSupported: true,
		ResumeToken:           sessionID,
		StartedAt:             now,
		UpdatedAt:             now,
		Budget:                state.Budget,
		BudgetUsed: ResearchBudgetUsage{
			PlannedQueries:       len(req.SeedQueries),
			WorkerSearchBudget:   budgetWorkerSearches(state.Budget),
			FollowUpSearchBudget: budgetFollowUpSearches(state.Budget),
			RecursiveGapDepth:    budgetRecursiveGapDepth(state.Budget),
		},
		ReasoningRuntime:            BuildResearchReasoningRuntimeMetadata(req, state.Plane, state.Budget),
		DisableProgrammaticPlanning: req.DisableProgrammaticPlanning,
		DisableHypothesisGeneration: req.DisableHypothesisGeneration,
	}
}

func budgetWorkerSearches(budget *ResearchBudgetDecision) int {
	if budget == nil {
		return 0
	}
	return budget.WorkerSearchBudget
}

func budgetFollowUpSearches(budget *ResearchBudgetDecision) int {
	if budget == nil {
		return 0
	}
	return budget.FollowUpSearchBudget
}

func budgetRecursiveGapDepth(budget *ResearchBudgetDecision) int {
	if budget == nil {
		return 0
	}
	return budget.RecursiveGapDepth
}

func updateResearchDurableJobBudget(job *ResearchDurableJobState, state *ResearchSessionState, loopResult *LoopResult) {
	if job == nil {
		return
	}
	if state != nil {
		job.Budget = state.Budget
		job.Tasks = cloneResearchDurableTasks(state.DurableTasks)
		job.DisableProgrammaticPlanning = state.DisableProgrammaticPlanning
		job.DisableHypothesisGeneration = state.DisableHypothesisGeneration
		job.ReasoningRuntime = BuildResearchReasoningRuntimeMetadata(LoopRequest{
			Query:                       state.Query,
			Domain:                      state.Domain,
			ProjectID:                   state.SessionID,
			Mode:                        job.Mode,
			DisableProgrammaticPlanning: state.DisableProgrammaticPlanning,
			DisableHypothesisGeneration: state.DisableHypothesisGeneration,
		}, state.Plane, state.Budget)
		job.BudgetUsed.PlannedQueries = len(state.PlannedQueries)
		job.BudgetUsed.OpenLedgerCount = countOpenCoverageLedgerEntries(state.CoverageLedger)
		if state.ClaimVerification != nil {
			job.BudgetUsed.Claims = len(state.ClaimVerification.Records)
		}
		if state.SourceAcquisition != nil {
			job.BudgetUsed.SourceAcquisitionAttempts = len(state.SourceAcquisition.Attempts)
			job.BudgetUsed.FetchFailures = state.SourceAcquisition.FetchFailures
			job.BudgetUsed.PythonPDFExtractions = state.SourceAcquisition.RequiredPythonExtractions
		}
	}
	if loopResult != nil {
		job.BudgetUsed.ExecutedQueries = len(loopResult.ExecutedQueries)
		job.BudgetUsed.Papers = len(loopResult.Papers)
		job.SourceSummaries = researchDurableSourceSummaries(loopResult.Papers, 5)
		job.AttachedSourcesUsed = CountAttachedSourcesUsed(loopResult.Papers)
		if loopResult.GapAnalysis != nil {
			job.BudgetUsed.OpenLedgerCount = countOpenCoverageLedgerEntries(loopResult.GapAnalysis.Ledger)
		}
	}
	if job.Budget != nil {
		job.BudgetUsed.Exhausted = job.Budget.SearchTermBudget > 0 && job.BudgetUsed.ExecutedQueries >= job.Budget.SearchTermBudget && job.BudgetUsed.OpenLedgerCount > 0
	}
	job.UpdatedAt = time.Now().UnixMilli()
}

func researchDurableSourceSummaries(papers []search.Paper, limit int) []ResearchDurableSourceSummary {
	if len(papers) == 0 || limit <= 0 {
		return nil
	}
	out := make([]ResearchDurableSourceSummary, 0, limit)
	seen := map[string]struct{}{}
	for _, paper := range papers {
		sourceIDHash := researchDurableSourceHash(paper)
		if sourceIDHash == "" {
			continue
		}
		if _, exists := seen[sourceIDHash]; exists {
			continue
		}
		seen[sourceIDHash] = struct{}{}
		summary := ResearchDurableSourceSummary{
			SourceIDHash:             sourceIDHash,
			Attached:                 IsAttachedSourceProvenance(paper.Source, paper.SourceApis),
			AuthorNames:              researchDurableSourceStrings(paper.Authors, 3),
			CitationCount:            nonNegativeResearchDurableInt(paper.CitationCount),
			InfluentialCitationCount: nonNegativeResearchDurableInt(paper.InfluentialCitationCount),
			Year:                     nonNegativeResearchDurableInt(paper.Year),
			SourceApis:               researchDurableSourceStrings(append([]string{paper.Source}, paper.SourceApis...), 4),
		}
		if !summary.Attached && len(summary.AuthorNames) == 0 && summary.CitationCount == 0 && summary.InfluentialCitationCount == 0 && summary.Year == 0 && len(summary.SourceApis) == 0 {
			continue
		}
		out = append(out, summary)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func researchDurableSourceHash(paper search.Paper) string {
	seed := firstNonEmpty(paper.DOI, paper.ID, paper.ArxivID, paper.Link, paper.Title)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(seed))))
	return hex.EncodeToString(sum[:])[:16]
}

func researchDurableSourceStrings(values []string, limit int) []string {
	if len(values) == 0 || limit <= 0 {
		return nil
	}
	out := make([]string, 0, minInt(len(values), limit))
	seen := map[string]struct{}{}
	for _, value := range values {
		cleaned := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if cleaned == "" {
			continue
		}
		key := strings.ToLower(cleaned)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func nonNegativeResearchDurableInt(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func completeResearchDurableJob(job *ResearchDurableJobState, state *ResearchSessionState, loopResult *LoopResult) {
	if job == nil {
		return
	}
	updateResearchDurableJobBudget(job, state, loopResult)
	job.Status = researchDurableJobStatusCompleted
	if state != nil {
		job.StopReason = strings.TrimSpace(state.StopReason)
	}
	if job.BudgetUsed.Exhausted && job.StopReason == "" {
		job.StopReason = "budget_exhausted_with_open_gaps"
	}
	job.UpdatedAt = time.Now().UnixMilli()
}

func failResearchDurableJob(job *ResearchDurableJobState, err error, cancelled bool) {
	if job == nil {
		return
	}
	if cancelled {
		job.Status = researchDurableJobStatusCancelled
		job.StopReason = "cancelled"
	} else {
		job.Status = researchDurableJobStatusFailed
		job.StopReason = "failed"
	}
	if err != nil {
		job.FailureReason = err.Error()
	}
	job.UpdatedAt = time.Now().UnixMilli()
}

func failResearchDurableJobWithLoopResult(job *ResearchDurableJobState, state *ResearchSessionState, loopResult *LoopResult, err error, cancelled bool) {
	updateResearchDurableJobBudget(job, state, loopResult)
	failResearchDurableJob(job, err, cancelled)
}

func researchDurableJobFailureEventType(cancelled bool) string {
	if cancelled {
		return "research_job_cancelled"
	}
	return "research_job_failed"
}

func researchDurableJobPayload(job *ResearchDurableJobState) map[string]any {
	if job == nil {
		return nil
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return map[string]any{
			"jobId":     strings.TrimSpace(job.JobID),
			"sessionId": strings.TrimSpace(job.SessionID),
			"status":    strings.TrimSpace(job.Status),
		}
	}
	payload := map[string]any{}
	_ = json.Unmarshal(raw, &payload)
	return payload
}

func (rt *UnifiedResearchRuntime) persistResearchDurableJob(state *ResearchSessionState, eventType string) error {
	if rt == nil || state == nil || state.DurableJob == nil {
		return nil
	}
	job := state.DurableJob
	if rt.stateStore != nil {
		job.Storage = "runtime_state_store"
		payload := researchDurableJobPayload(job)
		if len(payload) == 0 {
			return nil
		}
		if err := rt.stateStore.SaveResearchJob(job.JobID, payload); err != nil {
			job.Storage = "persist_failed"
			return err
		}
	} else {
		job.Storage = "memory_only"
		job.ResumeSupported = false
	}
	payload := researchDurableJobPayload(job)
	if len(payload) == 0 {
		return nil
	}
	if rt.journal != nil {
		now := time.Now().UnixMilli()
		if strings.TrimSpace(eventType) == "" {
			eventType = "research_job_updated"
		}
		rt.journal.Append(RuntimeJournalEntry{
			EventID:   stableWisDevID("research_job_event", job.JobID, eventType, job.Status, nowString(now)),
			TraceID:   NewTraceID(),
			SessionID: strings.TrimSpace(job.SessionID),
			PlanID:    strings.TrimSpace(job.JobID),
			EventType: eventType,
			Path:      "/runtime/research",
			Status:    strings.TrimSpace(job.Status),
			CreatedAt: now,
			Summary:   "durable research job state updated",
			Payload:   payload,
			Metadata: map[string]any{
				"component":     "wisdev.runtime",
				"runtime":       "go",
				"researchPlane": job.Plane,
				"replayable":    job.Replayable,
			},
		})
	}
	return nil
}

func nowString(value int64) string {
	return strings.TrimSpace(time.UnixMilli(value).UTC().Format(time.RFC3339Nano))
}
