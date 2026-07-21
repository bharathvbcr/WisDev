package wisdev

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
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
	DependsOnPlanIDs        []string `json:"dependsOnPlanIds,omitempty"`
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
	payload["branchWidth"] = float64(3)
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
		DependsOnPlanIDs:        branchPlanStringSlice(firstPresent(raw, "dependsOnPlanIds", "depends_on_plan_ids", "dependsOn", "depends_on")),
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
	return attachResearchBranchPlanDependencies(rootQuery, plans)
}

func attachResearchBranchPlanDependencies(rootQuery string, plans []ResearchBranchPlan) []ResearchBranchPlan {
	if len(plans) == 0 {
		return nil
	}
	rootQuery = strings.TrimSpace(rootQuery)
	rootID := strings.TrimSpace(plans[0].ID)
	for idx := range plans {
		if strings.EqualFold(strings.TrimSpace(plans[idx].Query), rootQuery) {
			rootID = strings.TrimSpace(plans[idx].ID)
			break
		}
	}
	if rootID == "" {
		rootID = "branch-001"
	}

	primaryEvidenceIDs := make([]string, 0, len(plans))
	facetEvidenceIDs := make([]string, 0, len(plans))
	for _, plan := range plans {
		if strings.TrimSpace(plan.ID) == "" {
			continue
		}
		lower := strings.ToLower(plan.Query)
		switch {
		case strings.EqualFold(strings.TrimSpace(plan.Query), rootQuery):
			continue
		case isPrimaryEvidenceAgendaQuery(lower):
			primaryEvidenceIDs = appendUniquePlanID(primaryEvidenceIDs, plan.ID)
		}
		if isFacetEvidenceAgendaQuery(lower) {
			facetEvidenceIDs = appendUniquePlanID(facetEvidenceIDs, plan.ID)
		}
	}
	if len(primaryEvidenceIDs) == 0 {
		primaryEvidenceIDs = appendUniquePlanID(primaryEvidenceIDs, rootID)
	}

	for idx := range plans {
		query := strings.TrimSpace(plans[idx].Query)
		if query == "" || strings.EqualFold(query, rootQuery) {
			plans[idx].ReasoningStrategy = firstNonEmpty(plans[idx].ReasoningStrategy, "root_research_goal")
			continue
		}
		plans[idx].ParentID = firstNonEmpty(strings.TrimSpace(plans[idx].ParentID), rootID)
		plans[idx].DependsOnPlanIDs = appendUniquePlanID(plans[idx].DependsOnPlanIDs, plans[idx].ParentID)
		plans[idx].ReasoningStrategy = firstNonEmpty(strings.TrimSpace(plans[idx].ReasoningStrategy), "recursive_query_decomposition")
		plans[idx].Depth = maxInt(plans[idx].Depth, 1)

		lower := strings.ToLower(query)
		if isComparativeSynthesisAgendaQuery(lower) && len(facetEvidenceIDs) > 0 {
			plans[idx].ParentID = facetEvidenceIDs[0]
			plans[idx].DependsOnPlanIDs = uniqueStrings(append(append([]string(nil), facetEvidenceIDs...), rootID))
			plans[idx].ReasoningStrategy = "dependent_comparative_synthesis"
			plans[idx].Depth = maxInt(plans[idx].Depth, 2)
			continue
		}
		if isVerificationAgendaQuery(lower) {
			plans[idx].ParentID = primaryEvidenceIDs[0]
			plans[idx].DependsOnPlanIDs = uniqueStrings(append(append([]string(nil), primaryEvidenceIDs...), rootID))
			plans[idx].ReasoningStrategy = "dependent_verification"
			plans[idx].Depth = maxInt(plans[idx].Depth, 2)
		}
	}
	return normalizeResearchBranchPlans(rootQuery, plans)
}

func isPrimaryEvidenceAgendaQuery(lowerQuery string) bool {
	return strings.Contains(lowerQuery, "primary evidence") ||
		strings.Contains(lowerQuery, "mechanism evidence") ||
		strings.Contains(lowerQuery, "systematic review") ||
		strings.Contains(lowerQuery, "evidence and deployment") ||
		strings.Contains(lowerQuery, "pre-prints evidence") ||
		strings.Contains(lowerQuery, "materials synthesis")
}

func isFacetEvidenceAgendaQuery(lowerQuery string) bool {
	return strings.Contains(lowerQuery, "rag-based systems evidence") ||
		strings.Contains(lowerQuery, "fine-tuned model deployment evidence") ||
		strings.Contains(lowerQuery, "recent pre-prints evidence") ||
		strings.Contains(lowerQuery, "historical failed replications")
}

func isComparativeSynthesisAgendaQuery(lowerQuery string) bool {
	return strings.Contains(lowerQuery, "comparative tradeoffs") ||
		strings.Contains(lowerQuery, "versus") ||
		strings.Contains(lowerQuery, "cross-referencing")
}

func isVerificationAgendaQuery(lowerQuery string) bool {
	return strings.Contains(lowerQuery, "limitations") ||
		strings.Contains(lowerQuery, "contradictory") ||
		strings.Contains(lowerQuery, "citation graph") ||
		strings.Contains(lowerQuery, "failed replication") ||
		strings.Contains(lowerQuery, "experimental pitfall") ||
		strings.Contains(lowerQuery, "falsification") ||
		strings.Contains(lowerQuery, "bias")
}

func appendUniquePlanID(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return values
		}
	}
	return append(values, value)
}

func researchBranchPlansFromHypotheses(rootQuery string, hypotheses []Hypothesis) []ResearchBranchPlan {
	if len(hypotheses) == 0 {
		return nil
	}
	plans := make([]ResearchBranchPlan, 0, len(hypotheses))
	for idx, hypothesis := range hypotheses {
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		falsifiability := strings.TrimSpace(hypothesis.FalsifiabilityCondition)
		if falsifiability == "" {
			falsifiability = "credible contradictory or missing grounded evidence invalidates this hypothesis"
		}
		evidenceQuery := buildResearchWorkerQuery(rootQuery, "hypothesis evidence: "+claim)
		falsificationQuery := buildResearchWorkerQuery(rootQuery, "falsification check: "+falsifiability)
		contradictionQuery := buildResearchWorkerQuery(rootQuery, "contradiction and bias check: "+claim)
		planID := strings.TrimSpace(hypothesis.ID)
		if planID == "" {
			planID = stableWisDevID("pre-retrieval-hypothesis", rootQuery, claim)
		}
		plans = append(plans, ResearchBranchPlan{
			ID:                      firstNonEmpty(planID, fmt.Sprintf("hypothesis-branch-%03d", idx+1)),
			Query:                   evidenceQuery,
			Hypothesis:              claim,
			RetrievalPlan:           normalizeLoopQueries(rootQuery, []string{evidenceQuery, falsificationQuery, contradictionQuery}),
			ReasoningStrategy:       "pre_retrieval_hypothesis_test",
			FalsifiabilityCondition: falsifiability,
			ClosureCondition:        "supporting evidence, falsification probes, and contradiction checks are represented before synthesis",
			Depth:                   1,
			SearchWeight:            ClampFloat(firstNonEmptyFloat(hypothesis.ConfidenceScore, hypothesis.ConfidenceThreshold, 0.65), 0.05, 1),
			Status:                  firstNonEmpty(strings.TrimSpace(hypothesis.Status), "planned"),
			StopReason:              "pending_retrieval",
		})
	}
	return normalizeResearchBranchPlans(rootQuery, plans)
}

func researchBranchPlansWithExecutionStatus(rootQuery string, plannedQueries []string, executedQueries []string, queryCoverage map[string][]search.Paper) []ResearchBranchPlan {
	return applyResearchBranchPlanExecutionStatus(researchBranchPlansFromQueries(rootQuery, plannedQueries), executedQueries, queryCoverage)
}

// applyResearchBranchPlanExecutionStatus overlays executed/retrieved status onto
// existing branch plans without rebuilding them, so richer plan metadata (for
// example pre-retrieval hypothesis branches) is preserved.
func applyResearchBranchPlanExecutionStatus(plans []ResearchBranchPlan, executedQueries []string, queryCoverage map[string][]search.Paper) []ResearchBranchPlan {
	if len(plans) == 0 {
		return plans
	}
	executed := make(map[string]struct{}, len(executedQueries))
	for _, query := range executedQueries {
		key := loopQueryMatchKey(query)
		if key != "" {
			executed[key] = struct{}{}
		}
	}
	coverageByKey := make(map[string][]search.Paper, len(queryCoverage))
	for query, papers := range queryCoverage {
		key := loopQueryMatchKey(query)
		if key == "" {
			continue
		}
		coverageByKey[key] = appendUniqueSearchPapers(coverageByKey[key], papers)
	}
	for idx := range plans {
		key := loopQueryMatchKey(plans[idx].Query)
		if _, ok := executed[key]; !ok {
			continue
		}
		plans[idx].Status = "executed"
		if len(coverageByKey[key]) > 0 {
			plans[idx].Status = "retrieved"
			plans[idx].StopReason = "sources_found"
			continue
		}
		plans[idx].StopReason = "no_sources"
	}
	return plans
}

func loopQueryMatchKey(query string) string {
	return strings.ToLower(strings.TrimSpace(applyResearchQueryCorrections(normalizeResearchQueryText(query))))
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
		plan.DependsOnPlanIDs = uniqueStrings(plan.DependsOnPlanIDs)
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

func mergeResearchBranchPlans(rootQuery string, groups ...[]ResearchBranchPlan) []ResearchBranchPlan {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	merged := make([]ResearchBranchPlan, 0, total)
	for _, group := range groups {
		merged = append(merged, group...)
	}
	return normalizeResearchBranchPlans(rootQuery, merged)
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
