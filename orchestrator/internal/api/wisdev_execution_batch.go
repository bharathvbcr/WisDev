package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

const maxExecuteParallelStepCount = 3

func resolveExecutePlanSteps(session *wisdev.AgentSession, stepID string, selectedParallelStepIDs []string) ([]wisdev.PlanStep, error) {
	if session == nil || session.Plan == nil {
		return nil, fmt.Errorf("session has no plan")
	}

	primary, ok := findExecutablePlanStep(session.Plan, stepID)
	if !ok {
		return nil, fmt.Errorf("no executable step found")
	}

	orderedIDs := orderedExecutionStepIDs(primary.ID, selectedParallelStepIDs)
	steps := []wisdev.PlanStep{primary}
	if len(orderedIDs) == 1 || !primary.Parallelizable || primary.RequiresHumanCheckpoint {
		return steps, nil
	}

	for _, candidateID := range orderedIDs[1:] {
		if len(steps) >= maxExecuteParallelStepCount {
			break
		}
		candidate, ok := findExecutablePlanStep(session.Plan, candidateID)
		if !ok || !candidate.Parallelizable {
			continue
		}
		if candidate.RequiresHumanCheckpoint {
			continue
		}
		if !parallelExecutionCompatible(primary, candidate) {
			continue
		}
		steps = append(steps, candidate)
	}

	return steps, nil
}

func findExecutablePlanStep(plan *wisdev.PlanState, stepID string) (wisdev.PlanStep, bool) {
	if plan == nil {
		return wisdev.PlanStep{}, false
	}
	targetID := strings.TrimSpace(stepID)
	if targetID == "" {
		return wisdev.PlanStep{}, false
	}
	for _, step := range plan.Steps {
		if step.ID != targetID {
			continue
		}
		if plan.CompletedStepIDs[step.ID] || plan.FailedStepIDs[step.ID] != "" {
			return wisdev.PlanStep{}, false
		}
		if !executionDependenciesSatisfied(step, plan.CompletedStepIDs) {
			return wisdev.PlanStep{}, false
		}
		return step, true
	}
	return wisdev.PlanStep{}, false
}

func executionDependenciesSatisfied(step wisdev.PlanStep, completed map[string]bool) bool {
	for _, dependency := range step.DependsOnStepIDs {
		if !completed[dependency] {
			return false
		}
	}
	return true
}

func orderedExecutionStepIDs(primaryID string, selectedParallelStepIDs []string) []string {
	ordered := make([]string, 0, len(selectedParallelStepIDs)+1)
	seen := make(map[string]struct{}, len(selectedParallelStepIDs)+1)
	appendID := func(raw string) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}

	appendID(primaryID)
	for _, stepID := range selectedParallelStepIDs {
		appendID(stepID)
	}

	if len(ordered) > maxExecuteParallelStepCount {
		return append([]string(nil), ordered[:maxExecuteParallelStepCount]...)
	}
	return ordered
}

func parallelExecutionCompatible(primary wisdev.PlanStep, candidate wisdev.PlanStep) bool {
	primaryGroup := strings.TrimSpace(primary.ParallelGroup)
	candidateGroup := strings.TrimSpace(candidate.ParallelGroup)
	if primaryGroup == "" || candidateGroup == "" {
		return true
	}
	return primaryGroup == candidateGroup
}

func runExecutePlanSteps(ctx context.Context, gateway *wisdev.AgentGateway, session *wisdev.AgentSession, steps []wisdev.PlanStep) []wisdev.StepResult {
	if len(steps) == 0 {
		return nil
	}
	sessionID := ""
	planID := ""
	if session != nil {
		sessionID = session.SessionID
		if session.Plan != nil {
			planID = session.Plan.PlanID
		}
	}
	if session == nil || session.Plan == nil {
		slog.Error("wisdev execute route cannot launch plan step batch without session plan",
			"component", "api.plan_routes",
			"operation", "execute",
			"stage", "plan_step_batch_failed",
			"sessionId", sessionID,
			"planId", planID,
			"stepIds", planStepIDs(steps),
		)
		results := make([]wisdev.StepResult, 0, len(steps))
		for idx, step := range steps {
			results = append(results, wisdev.StepResult{
				Step:    step,
				Attempt: 1,
				LaneID:  idx + 1,
				Err:     fmt.Errorf("session plan unavailable"),
			})
		}
		return results
	}
	if gateway == nil || gateway.Executor == nil {
		slog.Error("wisdev execute route cannot launch plan step batch without executor",
			"component", "api.plan_routes",
			"operation", "execute",
			"stage", "plan_step_batch_failed",
			"sessionId", sessionID,
			"planId", planID,
			"stepIds", planStepIDs(steps),
		)
		results := make([]wisdev.StepResult, 0, len(steps))
		for idx, step := range steps {
			results = append(results, wisdev.StepResult{
				Step:    step,
				Attempt: 1,
				LaneID:  idx + 1,
				Err:     fmt.Errorf("execution runtime unavailable"),
			})
		}
		return results
	}

	slog.Info("wisdev execute route launching plan step batch",
		"component", "api.plan_routes",
		"operation", "execute",
		"stage", "plan_step_batch_start",
		"sessionId", sessionID,
		"planId", planID,
		"stepCount", len(steps),
		"stepIds", planStepIDs(steps),
	)

	resultsCh := make(chan wisdev.StepResult, len(steps))
	var wg sync.WaitGroup
	for idx, step := range steps {
		laneID := idx + 1
		wg.Add(1)
		go func(step wisdev.PlanStep, laneID int) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("wisdev execute route recovered lane panic",
						"component", "api.plan_routes",
						"operation", "execute",
						"stage", "plan_step_batch_panic",
						"sessionId", sessionID,
						"planId", planID,
						"stepId", step.ID,
						"laneId", laneID,
						"panic", fmt.Sprintf("%v", recovered),
					)
					resultsCh <- wisdev.StepResult{
						Step:    step,
						Attempt: 1,
						LaneID:  laneID,
						Err:     fmt.Errorf("step execution panic: %v", recovered),
					}
				}
			}()
			result := gateway.Executor.RunStepWithRecovery(ctx, cloneExecutionSession(session), step, laneID)
			if strings.TrimSpace(result.Step.ID) == "" {
				result.Step = step
			}
			if result.Attempt <= 0 {
				result.Attempt = 1
			}
			if result.LaneID <= 0 {
				result.LaneID = laneID
			}
			resultsCh <- result
		}(step, laneID)
	}

	wg.Wait()
	close(resultsCh)

	results := make([]wisdev.StepResult, 0, len(steps))
	for result := range resultsCh {
		results = append(results, result)
	}
	if len(results) < len(steps) {
		seen := make(map[string]struct{}, len(results))
		for _, result := range results {
			if stepID := strings.TrimSpace(result.Step.ID); stepID != "" {
				seen[stepID] = struct{}{}
			}
		}
		for idx, step := range steps {
			if _, ok := seen[strings.TrimSpace(step.ID)]; ok {
				continue
			}
			results = append(results, wisdev.StepResult{
				Step:    step,
				Attempt: 1,
				LaneID:  idx + 1,
				Err:     fmt.Errorf("executor returned no result"),
			})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].LaneID < results[j].LaneID
	})
	return results
}

func cloneExecutionSession(session *wisdev.AgentSession) *wisdev.AgentSession {
	if session == nil {
		return nil
	}
	body, err := json.Marshal(session)
	if err != nil {
		clone := *session
		return &clone
	}
	var clone wisdev.AgentSession
	if err := json.Unmarshal(body, &clone); err != nil {
		cloneFallback := *session
		return &cloneFallback
	}
	return &clone
}

func applyExecutePlanStepResults(session *wisdev.AgentSession, results []wisdev.StepResult) ([]string, map[string]string, map[string]string) {
	if session == nil || session.Plan == nil {
		return nil, nil, nil
	}
	ensureExecutePlanMaps(session)

	completedStepIDs := make([]string, 0, len(results))
	failedStepIDs := make(map[string]string)
	confirmationRequired := make(map[string]string)

	for _, result := range results {
		step := result.Step
		session.Plan.StepAttempts[step.ID] += result.Attempt
		session.Plan.StepConfidences[step.ID] = result.Confidence

		if result.Err == nil {
			policy.ApplyBudgetUsage(&session.Budget, step.ExecutionTarget == wisdev.ExecutionTargetPythonSandbox, step.EstimatedCostCents)
			session.Plan.CompletedStepIDs[step.ID] = true
			completedStepIDs = append(completedStepIDs, step.ID)
			continue
		}

		errMsg := result.Err.Error()
		if strings.HasPrefix(errMsg, "CONFIRMATION_REQUIRED:") {
			confirmationRequired[step.ID] = strings.TrimSpace(strings.TrimPrefix(errMsg, "CONFIRMATION_REQUIRED:"))
			continue
		}

		session.Plan.FailedStepIDs[step.ID] = errMsg
		session.Plan.StepFailureCount[step.ID]++
		session.FailureMemory[step.Action]++
		failedStepIDs[step.ID] = errMsg
	}

	if len(failedStepIDs) == 0 {
		failedStepIDs = nil
	}
	if len(confirmationRequired) == 0 {
		confirmationRequired = nil
	}
	return completedStepIDs, failedStepIDs, confirmationRequired
}

func createPendingApproval(session *wisdev.AgentSession, gateway *wisdev.AgentGateway, step wisdev.PlanStep, rationale string) map[string]any {
	ensureExecutePlanMaps(session)

	token, tokenHash, tokenErr := wisdev.NewApprovalToken()
	if tokenErr != nil {
		token = fmt.Sprintf("approve_%s_%d", step.ID, time.Now().UnixMilli())
		tokenHash = wisdev.HashApprovalToken(token)
	}

	timeout := 10 * time.Minute
	if gateway != nil && gateway.ADKRuntime != nil {
		timeout = gateway.ADKRuntime.HITLTimeout()
	}

	session.Plan.PendingApprovalID = wisdev.NewTraceID()
	session.Plan.PendingApprovalTokenHash = tokenHash
	session.Plan.PendingApprovalStepID = step.ID
	session.Plan.PendingApprovalExpiresAt = time.Now().Add(timeout).UnixMilli()
	session.Status = wisdev.SessionPaused
	session.UpdatedAt = wisdev.NowMillis()

	payload := map[string]any{
		"approvalId":     session.Plan.PendingApprovalID,
		"approvalToken":  token,
		"stepId":         step.ID,
		"expiresAt":      session.Plan.PendingApprovalExpiresAt,
		"action":         step.Action,
		"allowedActions": wisdev.ConfirmationActions(),
	}
	if gateway != nil && gateway.ADKRuntime != nil {
		payload = gateway.ADKRuntime.BuildHITLRequest(token, step, rationale)
		payload["approvalId"] = session.Plan.PendingApprovalID
		payload["stepId"] = step.ID
		payload["expiresAt"] = session.Plan.PendingApprovalExpiresAt
	}
	return payload
}

func clearExpiredPendingApproval(ctx context.Context, gateway *wisdev.AgentGateway, session *wisdev.AgentSession) (bool, error) {
	if session == nil || session.Plan == nil {
		return false, nil
	}
	pendingApprovalID := strings.TrimSpace(session.Plan.PendingApprovalID)
	if pendingApprovalID == "" {
		return false, nil
	}
	expiresAt := session.Plan.PendingApprovalExpiresAt
	if expiresAt <= 0 || wisdev.NowMillis() <= expiresAt {
		return false, nil
	}
	session.Plan.PendingApprovalID = ""
	session.Plan.PendingApprovalTokenHash = ""
	session.Plan.PendingApprovalStepID = ""
	session.Plan.PendingApprovalExpiresAt = 0
	session.UpdatedAt = wisdev.NowMillis()
	if gateway != nil && gateway.Store != nil {
		if err := gateway.Store.Put(ctx, session, gateway.SessionTTL); err != nil {
			return false, err
		}
	}
	return true, nil
}

func ensureExecutePlanMaps(session *wisdev.AgentSession) {
	if session == nil || session.Plan == nil {
		return
	}
	if session.Plan.CompletedStepIDs == nil {
		session.Plan.CompletedStepIDs = make(map[string]bool)
	}
	if session.Plan.FailedStepIDs == nil {
		session.Plan.FailedStepIDs = make(map[string]string)
	}
	if session.Plan.StepAttempts == nil {
		session.Plan.StepAttempts = make(map[string]int)
	}
	if session.Plan.StepFailureCount == nil {
		session.Plan.StepFailureCount = make(map[string]int)
	}
	if session.Plan.StepConfidences == nil {
		session.Plan.StepConfidences = make(map[string]float64)
	}
	if session.FailureMemory == nil {
		session.FailureMemory = make(map[string]int)
	}
}

func planStepIDs(steps []wisdev.PlanStep) []string {
	ids := make([]string, 0, len(steps))
	for _, step := range steps {
		if id := strings.TrimSpace(step.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func sortedExecuteMapKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	sort.Strings(keys)
	return keys
}
