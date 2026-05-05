package wisdev

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ResearchBranchPlan is the durable form of a tree/MCTS research branch. Query
// strings remain exposed as a compatibility projection, but branch plans are the
// canonical scheduler input and blackboard artifact.
type ResearchBranchPlan struct {
	ID                      string   `json:"id"`
	Query                   string   `json:"query"`
	Hypothesis              string   `json:"hypothesis,omitempty"`
	RetrievalPlan           []string `json:"retrievalPlan,omitempty"`
	ReasoningStrategy       string   `json:"reasoningStrategy,omitempty"`
	FalsifiabilityCondition string   `json:"falsifiabilityCondition,omitempty"`
	ClosureCondition        string   `json:"closureCondition,omitempty"`
	ParentID                string   `json:"parentId,omitempty"`
	Depth                   int      `json:"depth,omitempty"`
	SearchWeight            float64  `json:"searchWeight,omitempty"`
	Status                  string   `json:"status,omitempty"`
	StopReason              string   `json:"stopReason,omitempty"`
}

func (rt *UnifiedResearchRuntime) planProgrammaticBranches(ctx context.Context, session *AgentSession, query string, domain string, mode string) []ResearchBranchPlan {
	if rt == nil || rt.exec == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	payload := map[string]any{
		"query": strings.TrimSpace(query),
		"prioritySubtopics": []any{
			"primary evidence",
			"source diversity",
			"citation integrity",
			"counter evidence",
			"replication",
		},
	}
	if strings.TrimSpace(domain) != "" {
		payload["domain"] = strings.TrimSpace(domain)
	}
	if strings.TrimSpace(mode) != "" {
		payload["mode"] = strings.TrimSpace(mode)
	}
	if NormalizeWisDevMode(mode) == WisDevModeYOLO {
		payload["branchWidth"] = float64(4)
		payload["maxDepth"] = float64(3)
	} else {
		payload["branchWidth"] = float64(3)
	}
	tree := RunProgrammaticTreeLoop(ctx, rt.exec, session, ActionResearchQueryDecompose, payload, 4, nil)
	return extractProgrammaticBranchPlansFromTreeResult(query, tree)
}

func extractProgrammaticBranchPlansFromTreeResult(rootQuery string, result treeLoopResult) []ResearchBranchPlan {
	plans := extractProgrammaticBranchPlans(rootQuery, result.Final, "final", 0, result.BestConfidence, "planned", "")
	for _, iteration := range result.Iterations {
		iterationPlans := extractProgrammaticBranchPlans(
			rootQuery,
			iteration.Output,
			fmt.Sprintf("branch-%d-iter-%d", iteration.BranchID, iteration.Iteration),
			iteration.BranchID,
			maxFloat(iteration.Score, iteration.Confidence),
			iteration.Status,
			iteration.Reason,
		)
		plans = append(plans, iterationPlans...)
	}
	if len(plans) == 0 {
		return researchBranchPlansFromQueries(rootQuery, extractProgrammaticQueriesFromTreeResult(result))
	}
	return normalizeResearchBranchPlans(rootQuery, plans)
}

func extractProgrammaticBranchPlans(rootQuery string, result map[string]any, sourceID string, branchID int, score float64, status string, stopReason string) []ResearchBranchPlan {
	if len(result) == 0 {
		return nil
	}
	plans := make([]ResearchBranchPlan, 0)
	appendPlan := func(plan ResearchBranchPlan) {
		if strings.TrimSpace(plan.Query) == "" {
			return
		}
		if strings.TrimSpace(plan.ID) == "" {
			plan.ID = fmt.Sprintf("%s-%03d", firstNonEmpty(sourceID, "branch"), len(plans)+1)
		}
		if strings.TrimSpace(plan.Status) == "" {
			plan.Status = firstNonEmpty(status, "planned")
		}
		if strings.TrimSpace(plan.StopReason) == "" {
			plan.StopReason = firstNonEmpty(stopReason, "pending_retrieval")
		}
		if plan.SearchWeight <= 0 {
			plan.SearchWeight = ClampFloat(firstPositive(score, 0.5), 0.05, 1)
		}
		if plan.Depth <= 0 {
			plan.Depth = 1
		}
		plans = append(plans, plan)
	}

	for idx, task := range branchPlanMaps(result["tasks"]) {
		appendPlan(branchPlanFromMap(rootQuery, task, fmt.Sprintf("%s-task-%d", sourceID, idx+1), branchID, score, status, stopReason))
	}
	for idx, branch := range branchPlanMaps(result["branches"]) {
		parentPlan := branchPlanFromMap(rootQuery, branch, fmt.Sprintf("%s-branch-%d", sourceID, idx+1), branchID, score, status, stopReason)
		if strings.TrimSpace(parentPlan.Query) != "" {
			appendPlan(parentPlan)
		}
		for nodeIdx, node := range branchPlanMaps(branch["nodes"]) {
			nodePlan := branchPlanFromMap(rootQuery, node, fmt.Sprintf("%s-branch-%d-node-%d", sourceID, idx+1, nodeIdx+1), branchID, score, status, stopReason)
			if nodePlan.ParentID == "" {
				nodePlan.ParentID = parentPlan.ID
			}
			appendPlan(nodePlan)
		}
	}
	for idx, query := range branchPlanStringSlice(result["queries"]) {
		appendPlan(defaultResearchBranchPlan(rootQuery, query, fmt.Sprintf("%s-query-%d", sourceID, idx+1)))
	}
	return normalizeResearchBranchPlans(rootQuery, plans)
}

func branchPlanFromMap(rootQuery string, raw map[string]any, id string, branchID int, score float64, status string, stopReason string) ResearchBranchPlan {
	query := firstNonEmpty(
		AsOptionalString(raw["query"]),
		AsOptionalString(raw["name"]),
		AsOptionalString(raw["label"]),
		AsOptionalString(raw["title"]),
	)
	if strings.TrimSpace(query) == "" {
		return ResearchBranchPlan{}
	}
	retrievalPlan := branchPlanStringSlice(firstPresent(raw, "retrievalPlan", "retrieval_plan", "plannedQueries", "planned_queries", "queries"))
	if len(retrievalPlan) == 0 {
		retrievalPlan = []string{query}
	}
	weight := branchPlanFloat(firstPresent(raw, "searchWeight", "search_weight", "weight", "confidence", "score"))
	if weight <= 0 {
		weight = firstPositive(score, 0.5)
	}
	depth := toInt(firstPresent(raw, "depth", "branchDepth", "branch_depth"))
	if depth <= 0 {
		depth = 1
	}
	if branchID > 0 && !strings.Contains(id, fmt.Sprintf("%d", branchID)) {
		id = fmt.Sprintf("%s-%d", id, branchID)
	}
	return ResearchBranchPlan{
		ID:                      strings.TrimSpace(id),
		Query:                   strings.TrimSpace(query),
		Hypothesis:              firstNonEmpty(AsOptionalString(raw["hypothesis"]), AsOptionalString(raw["claim"]), fmt.Sprintf("Investigate %s", query)),
		RetrievalPlan:           normalizeLoopQueries(rootQuery, retrievalPlan),
		ReasoningStrategy:       firstNonEmpty(AsOptionalString(raw["reasoningStrategy"]), AsOptionalString(raw["reasoning_strategy"]), AsOptionalString(raw["strategy"]), "evidence_grounded_retrieval"),
		FalsifiabilityCondition: firstNonEmpty(AsOptionalString(raw["falsifiabilityCondition"]), AsOptionalString(raw["falsifiability_condition"]), AsOptionalString(raw["falsification_condition"]), "credible contradictory or missing grounded evidence invalidates this branch"),
		ClosureCondition:        firstNonEmpty(AsOptionalString(raw["closureCondition"]), AsOptionalString(raw["closure_condition"]), "grounded evidence, source diversity, citation identity, and contradiction checks are resolved"),
		ParentID:                firstNonEmpty(AsOptionalString(raw["parentId"]), AsOptionalString(raw["parent_id"])),
		Depth:                   depth,
		SearchWeight:            ClampFloat(weight, 0.05, 1),
		Status:                  firstNonEmpty(AsOptionalString(raw["status"]), status, "planned"),
		StopReason:              firstNonEmpty(AsOptionalString(raw["stopReason"]), AsOptionalString(raw["stop_reason"]), stopReason, "pending_retrieval"),
	}
}

func researchBranchPlansFromQueries(rootQuery string, queries []string) []ResearchBranchPlan {
	queries = normalizeLoopQueries(rootQuery, queries)
	plans := make([]ResearchBranchPlan, 0, len(queries))
	for idx, query := range queries {
		plans = append(plans, defaultResearchBranchPlan(rootQuery, query, fmt.Sprintf("branch-%03d", idx+1)))
	}
	return plans
}

func defaultResearchBranchPlan(rootQuery string, query string, id string) ResearchBranchPlan {
	query = strings.TrimSpace(query)
	return ResearchBranchPlan{
		ID:                      strings.TrimSpace(id),
		Query:                   query,
		Hypothesis:              fmt.Sprintf("Investigate %s", query),
		RetrievalPlan:           normalizeLoopQueries(rootQuery, []string{query}),
		ReasoningStrategy:       "evidence_grounded_retrieval",
		FalsifiabilityCondition: "credible contradictory or missing grounded evidence invalidates this branch",
		ClosureCondition:        "grounded evidence, source diversity, citation identity, and contradiction checks are resolved",
		Depth:                   1,
		SearchWeight:            0.5,
		Status:                  "planned",
		StopReason:              "pending_retrieval",
	}
}

func normalizeResearchBranchPlans(rootQuery string, plans []ResearchBranchPlan) []ResearchBranchPlan {
	seen := map[string]struct{}{}
	out := make([]ResearchBranchPlan, 0, len(plans))
	for idx, plan := range plans {
		plan.Query = strings.TrimSpace(plan.Query)
		if plan.Query == "" {
			continue
		}
		key := strings.ToLower(plan.Query)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(plan.ID) == "" {
			plan.ID = fmt.Sprintf("branch-%03d", idx+1)
		}
		if len(plan.RetrievalPlan) == 0 {
			plan.RetrievalPlan = []string{plan.Query}
		}
		plan.RetrievalPlan = normalizeLoopQueries(rootQuery, plan.RetrievalPlan)
		if strings.TrimSpace(plan.Hypothesis) == "" {
			plan.Hypothesis = fmt.Sprintf("Investigate %s", plan.Query)
		}
		if strings.TrimSpace(plan.ReasoningStrategy) == "" {
			plan.ReasoningStrategy = "evidence_grounded_retrieval"
		}
		if strings.TrimSpace(plan.FalsifiabilityCondition) == "" {
			plan.FalsifiabilityCondition = "credible contradictory or missing grounded evidence invalidates this branch"
		}
		if strings.TrimSpace(plan.ClosureCondition) == "" {
			plan.ClosureCondition = "grounded evidence, source diversity, citation identity, and contradiction checks are resolved"
		}
		if plan.Depth <= 0 {
			plan.Depth = 1
		}
		if plan.SearchWeight <= 0 {
			plan.SearchWeight = 0.5
		}
		plan.SearchWeight = ClampFloat(plan.SearchWeight, 0.05, 1)
		if strings.TrimSpace(plan.Status) == "" {
			plan.Status = "planned"
		}
		if strings.TrimSpace(plan.StopReason) == "" {
			plan.StopReason = "pending_retrieval"
		}
		out = append(out, plan)
	}
	return out
}

func researchBranchPlanQueries(plans []ResearchBranchPlan) []string {
	queries := make([]string, 0, len(plans))
	for _, plan := range plans {
		queries = append(queries, plan.Query)
	}
	return normalizeLoopQueries("", queries)
}

func researchBranchPlansFromWorkerReports(rootQuery string, workers []ResearchWorkerState) []ResearchBranchPlan {
	var plans []ResearchBranchPlan
	for _, worker := range workers {
		raw, ok := worker.Artifacts["branchPlans"]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case []ResearchBranchPlan:
			plans = append(plans, typed...)
		case []map[string]any:
			for idx, item := range typed {
				plans = append(plans, branchPlanFromMap(rootQuery, item, fmt.Sprintf("%s-artifact-%d", worker.Role, idx+1), 0, 0.5, "planned", "pending_retrieval"))
			}
		case []any:
			for idx, item := range typed {
				plan := branchPlanFromMap(rootQuery, asMap(item), fmt.Sprintf("%s-artifact-%d", worker.Role, idx+1), 0, 0.5, "planned", "pending_retrieval")
				if strings.TrimSpace(plan.Query) != "" {
					plans = append(plans, plan)
				}
			}
		}
	}
	return normalizeResearchBranchPlans(rootQuery, plans)
}

func findResearchBranchPlanByQuery(plans []ResearchBranchPlan, query string) any {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	for _, plan := range plans {
		if strings.EqualFold(strings.TrimSpace(plan.Query), query) {
			return plan
		}
	}
	return nil
}

func branchPlanMaps(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped := asMap(item); len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	}
	return nil
}

func branchPlanStringSlice(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value := strings.TrimSpace(anyToString(item)); value != "" {
				out = append(out, value)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{strings.TrimSpace(typed)}
	}
	return nil
}

func firstPresent(raw map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			return value
		}
	}
	return nil
}

func branchPlanFloat(raw any) float64 {
	switch typed := raw.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0
		}
		return value
	}
	return 0
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func maxFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
