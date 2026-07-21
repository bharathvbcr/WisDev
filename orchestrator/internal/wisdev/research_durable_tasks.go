package wisdev

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	researchDurableTaskWorker        = "worker_task"
	researchDurableTaskSearchBatch   = "search_batch"
	researchDurableTaskCitationGraph = "citation_graph_step"
	researchDurableTaskBranch        = "branch_expansion"
	researchDurableTaskVerifier      = "verifier_pass"
)

func cloneResearchDurableTasks(tasks []ResearchDurableTaskState) []ResearchDurableTaskState {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]ResearchDurableTaskState, 0, len(tasks))
	for _, task := range tasks {
		clone := task
		clone.Artifacts = cloneAnyMap(task.Artifacts)
		clone.RetryPolicy.RetryableErrorCodes = append([]string(nil), task.RetryPolicy.RetryableErrorCodes...)
		out = append(out, clone)
	}
	return out
}

func upsertResearchDurableTask(state *ResearchSessionState, task ResearchDurableTaskState) {
	if state == nil {
		return
	}
	task.TaskKey = strings.TrimSpace(task.TaskKey)
	if task.TaskKey == "" {
		return
	}
	task.Operation = strings.TrimSpace(task.Operation)
	task.Status = strings.TrimSpace(firstNonEmpty(task.Status, "completed"))
	task.CheckpointKey = strings.TrimSpace(firstNonEmpty(task.CheckpointKey, stableWisDevID("research-task-checkpoint", state.SessionID, task.TaskKey)))
	task.TraceID = strings.TrimSpace(firstNonEmpty(task.TraceID, stableWisDevID("research-task-trace", state.SessionID, task.TaskKey)))
	task.Attempt = maxInt(task.Attempt, 1)
	if task.RetryPolicy.MaxAttempts == 0 {
		task.RetryPolicy = researchDurableRetryPolicy(task.Operation)
	}
	if task.StartedAt == 0 {
		task.StartedAt = NowMillis()
	}
	if task.FinishedAt == 0 && task.Status != "active" {
		task.FinishedAt = NowMillis()
	}
	for idx := range state.DurableTasks {
		if state.DurableTasks[idx].TaskKey == task.TaskKey {
			state.DurableTasks[idx] = task
			syncResearchDurableJobTasks(state)
			return
		}
	}
	state.DurableTasks = append(state.DurableTasks, task)
	syncResearchDurableJobTasks(state)
}

func syncResearchDurableJobTasks(state *ResearchSessionState) {
	if state == nil || state.DurableJob == nil {
		return
	}
	state.DurableJob.Tasks = cloneResearchDurableTasks(state.DurableTasks)
}

func researchDurableRetryPolicy(operation string) ResearchRetryPolicy {
	switch strings.TrimSpace(operation) {
	case researchDurableTaskSearchBatch, researchDurableTaskCitationGraph:
		return ResearchRetryPolicy{
			MaxAttempts:         2,
			BackoffMs:           750,
			RetryableErrorCodes: []string{"timeout", "provider_unavailable", "rate_limited"},
		}
	case researchDurableTaskVerifier:
		return ResearchRetryPolicy{
			MaxAttempts:         1,
			BackoffMs:           0,
			RetryableErrorCodes: []string{"ledger_incomplete"},
		}
	default:
		return ResearchRetryPolicy{
			MaxAttempts:         1,
			BackoffMs:           0,
			RetryableErrorCodes: []string{"cancelled"},
		}
	}
}

func researchDurableTaskTimeoutMs(role ResearchWorkerRole, operation string) int {
	switch strings.TrimSpace(operation) {
	case researchDurableTaskSearchBatch:
		return int(workerSearchTimeout(role) / time.Millisecond)
	case researchDurableTaskCitationGraph:
		return int((8 * time.Second) / time.Millisecond)
	case researchDurableTaskVerifier:
		return 10_000
	case researchDurableTaskBranch:
		return 5_000
	default:
		timeout := workerSearchTimeout(role)
		if timeout <= 0 {
			return 10_000
		}
		return int(timeout / time.Millisecond)
	}
}

func researchDurableTaskKey(state *ResearchSessionState, role ResearchWorkerRole, operation string, parts ...string) string {
	sessionID := ""
	if state != nil {
		sessionID = strings.TrimSpace(state.SessionID)
	}
	args := []string{"research-task", sessionID, string(role), strings.TrimSpace(operation)}
	args = append(args, canonicalTaskKeyParts(parts...)...)
	return stableWisDevID(args[0], args[1:]...)
}

func canonicalTaskKeyParts(parts ...string) []string {
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func recordResearchWorkerDurableTasks(state *ResearchSessionState, result researchWorkerExecution) {
	if state == nil || result.Role == "" {
		return
	}
	status := strings.TrimSpace(firstNonEmpty(result.Status, "completed"))
	failureReason := ""
	if result.Err != nil {
		failureReason = result.Err.Error()
		if status == "completed" {
			status = "failed"
		}
	}
	baseArtifacts := map[string]any{
		"plannedQueryCount":  len(result.Queries),
		"executedQueryCount": len(result.ExecutedQueries),
		"paperCount":         len(result.Papers),
		"evidenceCount":      len(result.Evidence),
		"openLedgerCount":    countOpenCoverageLedgerEntries(result.Ledger),
	}
	upsertResearchDurableTask(state, ResearchDurableTaskState{
		TaskKey:       researchDurableTaskKey(state, result.Role, researchDurableTaskWorker),
		CheckpointKey: stableWisDevID("research-worker-checkpoint", state.SessionID, string(result.Role), status),
		Operation:     researchDurableTaskWorker,
		Role:          result.Role,
		Status:        status,
		TimeoutMs:     researchDurableTaskTimeoutMs(result.Role, researchDurableTaskWorker),
		RetryPolicy:   researchDurableRetryPolicy(researchDurableTaskWorker),
		Attempt:       1,
		StartedAt:     result.StartedAt,
		FinishedAt:    result.FinishedAt,
		FailureReason: failureReason,
		Artifacts:     baseArtifacts,
	})
	if result.Role != ResearchWorkerSynthesizer && result.Role != ResearchWorkerIndependentVerifier {
		searchQueryKeys := canonicalTaskKeyParts(result.Queries...)
		searchStatus := status
		if len(result.ExecutedQueries) == 0 && status == "completed" {
			searchStatus = "skipped"
		}
		upsertResearchDurableTask(state, ResearchDurableTaskState{
			TaskKey:       researchDurableTaskKey(state, result.Role, researchDurableTaskSearchBatch, searchQueryKeys...),
			CheckpointKey: stableWisDevID("research-search-checkpoint", state.SessionID, string(result.Role), fmt.Sprintf("%d", len(result.ExecutedQueries))),
			Operation:     researchDurableTaskSearchBatch,
			Role:          result.Role,
			Status:        searchStatus,
			TimeoutMs:     researchDurableTaskTimeoutMs(result.Role, researchDurableTaskSearchBatch),
			RetryPolicy:   researchDurableRetryPolicy(researchDurableTaskSearchBatch),
			Attempt:       1,
			StartedAt:     result.StartedAt,
			FinishedAt:    result.FinishedAt,
			FailureReason: failureReason,
			Artifacts: map[string]any{
				"queries":         append([]string(nil), result.Queries...),
				"executedQueries": append([]string(nil), result.ExecutedQueries...),
				"parallelism":     workerSearchParallelism(result.Role),
				"searchBudget":    result.Artifacts["searchBudget"],
			},
		})
	}
	if result.Role == ResearchWorkerCitationGraph {
		graph, _ := result.Artifacts["citationGraph"].(*ResearchCitationGraph)
		graphStatus := status
		if graph == nil || len(graph.Edges) == 0 {
			graphStatus = "checkpointed_open"
		}
		upsertResearchDurableTask(state, ResearchDurableTaskState{
			TaskKey:       researchDurableTaskKey(state, result.Role, researchDurableTaskCitationGraph),
			CheckpointKey: stableWisDevID("research-citation-graph-checkpoint", state.SessionID, fmt.Sprintf("%d", len(result.Papers))),
			Operation:     researchDurableTaskCitationGraph,
			Role:          result.Role,
			Status:        graphStatus,
			TimeoutMs:     researchDurableTaskTimeoutMs(result.Role, researchDurableTaskCitationGraph),
			RetryPolicy:   researchDurableRetryPolicy(researchDurableTaskCitationGraph),
			Attempt:       1,
			StartedAt:     result.StartedAt,
			FinishedAt:    result.FinishedAt,
			Artifacts: map[string]any{
				"nodeCount":             citationGraphNodeCount(graph),
				"edgeCount":             citationGraphEdgeCount(graph),
				"identityConflictCount": citationGraphIdentityConflictCount(graph),
			},
		})
	}
}

func recordResearchBranchDurableTasks(state *ResearchSessionState) {
	if state == nil {
		return
	}
	for _, branch := range state.BranchEvaluations {
		status := "completed"
		if branch.VerifierVerdict != "promote" {
			status = "checkpointed_open"
		}
		upsertResearchDurableTask(state, ResearchDurableTaskState{
			TaskKey:       researchDurableTaskKey(state, ResearchWorkerScout, researchDurableTaskBranch, branch.ID),
			CheckpointKey: firstNonEmpty(branch.CheckpointKey, stableWisDevID("research-branch-checkpoint", branch.ID)),
			Operation:     researchDurableTaskBranch,
			Role:          ResearchWorkerScout,
			Status:        status,
			TimeoutMs:     researchDurableTaskTimeoutMs(ResearchWorkerScout, researchDurableTaskBranch),
			RetryPolicy:   researchDurableRetryPolicy(researchDurableTaskBranch),
			Attempt:       1,
			FailureReason: branch.StopReason,
			Artifacts: map[string]any{
				"branchId":        branch.ID,
				"query":           branch.Query,
				"branchScore":     branch.BranchScore,
				"verifierVerdict": branch.VerifierVerdict,
				"openGapCount":    len(branch.OpenGaps),
			},
		})
	}
}

func recordResearchVerifierDurableTask(state *ResearchSessionState) {
	if state == nil || state.VerifierDecision == nil {
		return
	}
	status := "completed"
	if state.VerifierDecision.Verdict != "promote" {
		status = "checkpointed_open"
	}
	upsertResearchDurableTask(state, ResearchDurableTaskState{
		TaskKey:       researchDurableTaskKey(state, ResearchWorkerIndependentVerifier, researchDurableTaskVerifier, state.VerifierDecision.Verdict),
		CheckpointKey: stableWisDevID("research-verifier-checkpoint", state.SessionID, state.VerifierDecision.Verdict, state.VerifierDecision.StopReason),
		Operation:     researchDurableTaskVerifier,
		Role:          ResearchWorkerIndependentVerifier,
		Status:        status,
		TimeoutMs:     researchDurableTaskTimeoutMs(ResearchWorkerIndependentVerifier, researchDurableTaskVerifier),
		RetryPolicy:   researchDurableRetryPolicy(researchDurableTaskVerifier),
		Attempt:       1,
		FailureReason: state.VerifierDecision.StopReason,
		Artifacts: map[string]any{
			"verdict":          state.VerifierDecision.Verdict,
			"confidence":       state.VerifierDecision.Confidence,
			"promotedClaims":   len(state.VerifierDecision.PromotedClaimIDs),
			"rejectedClaims":   len(state.VerifierDecision.RejectedClaimIDs),
			"revisionReasons":  append([]string(nil), state.VerifierDecision.RevisionReasons...),
			"evidenceOnly":     state.VerifierDecision.EvidenceOnly,
			"openLedgerCount":  countOpenCoverageLedgerEntries(state.CoverageLedger),
			"branchEvaluation": len(state.BranchEvaluations),
		},
	})
}

func citationGraphNodeCount(graph *ResearchCitationGraph) int {
	if graph == nil {
		return 0
	}
	return len(graph.Nodes)
}

func citationGraphEdgeCount(graph *ResearchCitationGraph) int {
	if graph == nil {
		return 0
	}
	return len(graph.Edges)
}

func citationGraphIdentityConflictCount(graph *ResearchCitationGraph) int {
	if graph == nil {
		return 0
	}
	return len(graph.IdentityConflicts) + len(graph.DuplicateSourceIDs)
}
