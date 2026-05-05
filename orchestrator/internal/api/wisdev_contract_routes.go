package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

var (
	wisdevFollowUpDecisionTimeout        = 14 * time.Second
	wisdevFollowUpDecisionLLMGraceWindow = 2 * time.Second
	wisdevInteractiveStructuredTimeout   = 40 * time.Second
	wisdevInteractiveStructuredGrace     = 4 * time.Second
)

var researchTokenStopwords = map[string]struct{}{
	"about": {}, "after": {}, "among": {}, "and": {}, "are": {}, "for": {}, "from": {}, "into": {}, "that": {}, "the": {}, "their": {}, "them": {}, "these": {}, "this": {}, "using": {}, "with": {},
}

var researchPhraseHeads = map[string]struct{}{
	"agent": {}, "agents": {}, "alignment": {}, "analysis": {}, "benchmark": {}, "benchmarks": {}, "classification": {}, "control": {}, "data": {}, "dataset": {}, "datasets": {}, "detection": {}, "discovery": {}, "evaluation": {}, "evaluations": {}, "extraction": {}, "feedback": {}, "generation": {}, "inference": {}, "learning": {}, "method": {}, "methods": {}, "model": {}, "models": {}, "modeling": {}, "network": {}, "networks": {}, "optimization": {}, "planning": {}, "policy": {}, "reasoning": {}, "retrieval": {}, "review": {}, "reviews": {}, "robustness": {}, "safety": {}, "search": {}, "segmentation": {}, "simulation": {}, "summarization": {}, "synthesis": {}, "system": {}, "systems": {}, "training": {}, "translation": {}, "tuning": {}, "verification": {}, "workflow": {}, "workflows": {},
}

func wisdevFollowUpDecisionSidecarBackstopBudget() time.Duration {
	base := wisdevFollowUpDecisionTimeout + wisdevFollowUpDecisionLLMGraceWindow
	if base <= 0 {
		return 2 * time.Second
	}
	return base
}

func interactiveStructuredClient(ctx context.Context, client *llm.Client) *llm.Client {
	if client == nil || client.VertexDirect == nil {
		return client
	}

	backstop := wisdevInteractiveStructuredTimeout + wisdevInteractiveStructuredGrace
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			backstop = remaining + wisdevInteractiveStructuredGrace
		}
	}
	if backstop <= 0 {
		backstop = wisdevInteractiveStructuredGrace
	}

	return client.WithoutVertexDirect().WithTimeout(backstop)
}

func interactiveStructuredRequest(ctx context.Context, client *llm.Client) (context.Context, *llm.Client, context.CancelFunc) {
	requestCtx := ctx
	cancel := func() {}
	if wisdevInteractiveStructuredTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, wisdevInteractiveStructuredTimeout)
	}
	return requestCtx, interactiveStructuredClient(requestCtx, client), cancel
}

func logInteractiveStructuredFallback(operation string, stage string, query string, err error) {
	attrs := []any{
		"component", "api.wisdev",
		"operation", operation,
		"stage", stage,
		"query_hash", searchQueryFingerprint(query),
		"timeout_ms", wisdevInteractiveStructuredTimeout.Milliseconds(),
		"fallback_source", "heuristic_fallback",
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	slog.Warn("wisdev interactive structured fallback", attrs...)
}

func (s *wisdevServer) registerContractRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/wisdev/plan/revision", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID   string           `json:"sessionId"`
			UserID      string           `json:"userId"`
			StepID      string           `json:"stepId"`
			Reason      string           `json:"reason"`
			Query       string           `json:"query"`
			Context     map[string]any   `json:"context"`
			CurrentPlan []map[string]any `json:"currentPlan"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.Reason) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "reason is required", map[string]any{"field": "reason"})
			return
		}
		userID, err := resolveAuthorizedUserID(r, req.UserID)
		if err != nil {
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}
		tasks, revisionSource := buildPlanRevisionTasks(r.Context(), agentGateway, req.StepID, req.Reason, req.Context)
		payload := map[string]any{
			"applied": true,
			"message": buildPlanRevisionMessage(req.StepID, req.Reason),
			"brainState": map[string]any{
				"revisionSource":    revisionSource,
				"sessionId":         strings.TrimSpace(req.SessionID),
				"userId":            userID,
				"failedStepId":      strings.TrimSpace(req.StepID),
				"reason":            strings.TrimSpace(req.Reason),
				"tasks":             uniqueStrings(tasks),
				"basePlan":          req.CurrentPlan,
				"recommendedAction": chooseRecommendedAction(req.Reason, req.Context),
				"updatedAt":         time.Now().UnixMilli(),
			},
		}
		traceID := writeEnvelope(w, "planRevision", payload)
		s.journalEvent("plan_revision", "/wisdev/plan/revision", traceID, req.SessionID, userID, "", strings.TrimSpace(req.StepID), "WisDev plan revision requested.", payload, map[string]any{"query": strings.TrimSpace(req.Query)})
	})

	mux.HandleFunc("/wisdev/subtopics/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query     string `json:"query"`
			Domain    string `json:"domain"`
			Limit     int    `json:"limit"`
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
			return
		}
		subtopics, keywords, variations, source, explanation := buildSubtopicsResponse(r.Context(), agentGateway, req.Query, req.Domain, req.Limit)
		payload := map[string]any{
			"subtopics":       subtopics,
			"keywords":        keywords,
			"queryVariations": variations,
			"source":          source,
			"explanation":     explanation,
			"coverageHint":    fmt.Sprintf("Generated %d scoped subtopics from the current research objective.", len(subtopics)),
		}
		traceID := writeEnvelope(w, "subtopics", payload)
		s.journalEvent("subtopics_generate", "/wisdev/subtopics/generate", traceID, req.SessionID, "", "", "", "Generated WisDev subtopics.", payload, map[string]any{"domain": strings.TrimSpace(req.Domain)})
	})

	mux.HandleFunc("/wisdev/study-types/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query     string   `json:"query"`
			Domain    string   `json:"domain"`
			Subtopics []string `json:"subtopics"`
			Limit     int      `json:"limit"`
			SessionID string   `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
			return
		}
		studyTypes, signals, source, explanation := buildStudyTypesResponse(r.Context(), agentGateway, req.Query, req.Domain, req.Subtopics, req.Limit)
		payload := map[string]any{
			"studyTypes":     studyTypes,
			"source":         source,
			"explanation":    explanation,
			"matchedSignals": signals,
			"coverageHint":   "Study types selected to widen methodological coverage while preserving query relevance.",
		}
		traceID := writeEnvelope(w, "studyTypes", payload)
		s.journalEvent("study_types_generate", "/wisdev/study-types/generate", traceID, req.SessionID, "", "", "", "Generated WisDev study types.", payload, map[string]any{"domain": strings.TrimSpace(req.Domain)})
	})

	mux.HandleFunc("/wisdev/recommended-answers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			QuestionID    string         `json:"questionId"`
			Query         string         `json:"query"`
			Domain        string         `json:"domain"`
			OptionValues  []string       `json:"optionValues"`
			QueryAnalysis map[string]any `json:"queryAnalysis"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.QuestionID) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "questionId is required", map[string]any{"field": "questionId"})
			return
		}

		values := make([]string, 0, 3)
		suggestedDomains := make([]string, 0)
		if raw, ok := req.QueryAnalysis["suggestedDomains"].([]any); ok {
			for _, item := range raw {
				value := strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", item)))
				if value != "" {
					suggestedDomains = append(suggestedDomains, value)
				}
			}
		}
		if domain := strings.TrimSpace(strings.ToLower(req.Domain)); domain != "" {
			suggestedDomains = append(suggestedDomains, domain)
		}

		if strings.Contains(strings.ToLower(req.QuestionID), "domain") && len(req.OptionValues) > 0 {
			for _, option := range req.OptionValues {
				normalizedOption := strings.TrimSpace(strings.ToLower(option))
				for _, suggestion := range suggestedDomains {
					if suggestion == normalizedOption || strings.Contains(normalizedOption, suggestion) || strings.Contains(suggestion, normalizedOption) {
						values = append(values, option)
						break
					}
				}
				if len(values) > 0 {
					break
				}
			}
		}

		if len(values) == 0 {
			for _, option := range req.OptionValues {
				option = strings.TrimSpace(option)
				if option == "" {
					continue
				}
				values = append(values, option)
				if len(values) >= 3 {
					break
				}
			}
		}

		traceID := writeEnvelope(w, "recommendedAnswers", map[string]any{
			"questionId":  strings.TrimSpace(req.QuestionID),
			"values":      values,
			"explanation": "Go-owned heuristic recommendation set.",
			"source":      "heuristic",
		})
		s.journalEvent("recommended_answers", "/wisdev/recommended-answers", traceID, "", "", "", "", "Generated recommended answers.", map[string]any{
			"questionId": strings.TrimSpace(req.QuestionID),
			"values":     values,
		}, nil)
	})

	mux.HandleFunc("/wisdev/research-path/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query          string   `json:"query"`
			Domain         string   `json:"domain"`
			Subtopics      []string `json:"subtopics"`
			StudyTypes     []string `json:"studyTypes"`
			CoverageScore  float64  `json:"coverageScore"`
			TargetCoverage float64  `json:"targetCoverage"`
			SessionID      string   `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{"field": "query"})
			return
		}
		target := req.TargetCoverage
		if target <= 0 {
			target = 0.7
		}
		score, pathReasoning, source := buildResearchPathScore(r.Context(), agentGateway, req.Query, req.Domain, req.Subtopics, req.StudyTypes, req.CoverageScore, target)
		needsRevision := score < target || len(req.Subtopics) < 2 || len(req.StudyTypes) == 0
		status := "healthy"
		if needsRevision {
			status = "revise"
		}
		payload := map[string]any{
			"pathScore":             clampScore(score),
			"score":                 clampScore(score),
			"level":                 inferExpertiseLevel(score, req.Domain),
			"needsRevision":         needsRevision,
			"status":                status,
			"strengths":             buildResearchPathStrengths(req.Subtopics, req.StudyTypes, score),
			"gaps":                  buildResearchPathGaps(req.Subtopics, req.StudyTypes, score, target),
			"recommendedNextStep":   buildRecommendedNextStep(req.Subtopics, req.StudyTypes, score, target),
			"recommendedSubtopics":  recommendAdditionalSubtopics(req.Query, req.Domain, req.Subtopics),
			"recommendedStudyTypes": recommendAdditionalStudyTypes(req.Query, req.Domain, req.StudyTypes),
			"source":                source,
			"reasoning":             pathReasoning,
			"brainState": map[string]any{
				"status":         status,
				"coverageScore":  clampScore(score),
				"targetCoverage": target,
				"sessionId":      strings.TrimSpace(req.SessionID),
				"updatedAt":      time.Now().UnixMilli(),
			},
			"signals": buildResearchPathSignals(req.Domain, req.Subtopics, req.StudyTypes, score),
		}
		traceID := writeEnvelope(w, "researchPath", payload)
		s.journalEvent("research_path_evaluate", "/wisdev/research-path/evaluate", traceID, req.SessionID, "", "", "", "Evaluated WisDev research path.", payload, nil)
	})

	mux.HandleFunc("/wisdev/search-coverage/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query          string           `json:"query"`
			Queries        []string         `json:"queries"`
			Results        []map[string]any `json:"results"`
			Papers         []map[string]any `json:"papers"`
			TargetCoverage float64          `json:"targetCoverage"`
			SessionID      string           `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		if len(req.Queries) == 0 && strings.TrimSpace(req.Query) == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query or queries is required", map[string]any{"field": "query"})
			return
		}
		coverage, missingTerms, recommendedQueries, source := buildCoverageEvaluation(r.Context(), agentGateway, req.Query, req.Queries, req.Results, req.Papers)
		status := "strong"
		if coverage < 0.75 {
			status = "partial"
		}
		if coverage < 0.45 {
			status = "weak"
		}
		resultCount := len(req.Results)
		if resultCount == 0 {
			resultCount = len(req.Papers)
		}
		payload := map[string]any{
			"coverage":           coverage,
			"coverageScore":      coverage,
			"score":              coverage,
			"coverageStatus":     status,
			"uniqueQueryCount":   len(uniqueStrings(req.Queries)),
			"resultCount":        resultCount,
			"missingTerms":       missingTerms,
			"gaps":               missingTerms,
			"recommendedQueries": recommendedQueries,
			"source":             source,
			"supportedSignals": []string{
				"query_diversity",
				"result_volume",
				"term_coverage",
			},
		}
		traceID := writeEnvelope(w, "searchCoverage", payload)
		s.journalEvent("search_coverage_evaluate", "/wisdev/search-coverage/evaluate", traceID, req.SessionID, "", "", "", "Evaluated WisDev search coverage.", payload, nil)
	})

	_ = agentGateway
}

func buildPlanRevisionMessage(stepID string, reason string) string {
	step := strings.TrimSpace(stepID)
	if step == "" {
		return fmt.Sprintf("Plan revised to account for: %s.", strings.TrimSpace(reason))
	}
	return fmt.Sprintf("Plan revised after %s because %s.", step, strings.TrimSpace(reason))
}

func buildPlanRevisionTasks(ctx context.Context, agentGateway *wisdev.AgentGateway, stepID string, reason string, contextData map[string]any) ([]string, string) {
	if agentGateway != nil && agentGateway.Brain != nil {
		if tasks, err := agentGateway.Brain.CoordinateReplan(ctx, strings.TrimSpace(stepID), strings.TrimSpace(reason), contextData, ""); err == nil && len(tasks) > 0 {
			out := make([]string, 0, len(tasks))
			for _, task := range tasks {
				label := strings.TrimSpace(task.Reason)
				if label == "" {
					label = strings.TrimSpace(task.Action)
				}
				if label == "" {
					continue
				}
				out = append(out, label)
			}
			if len(out) > 0 {
				return uniqueStrings(out), "brain_coordinate_replan"
			}
		}
	}
	tasks := make([]string, 0, 4)
	if step := strings.TrimSpace(stepID); step != "" {
		tasks = append(tasks, "Re-evaluate failure at step "+step)
	}
	tasks = append(tasks,
		"Re-rank supporting evidence for the current objective",
		"Regenerate the next bounded search move",
		"Record revised execution guidance in brain state",
	)
	return uniqueStrings(tasks), "heuristic_fallback"
}

func chooseRecommendedAction(reason string, context map[string]any) string {
	reasonLower := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(reasonLower, "coverage"):
		return "broaden_queries"
	case strings.Contains(reasonLower, "conflict"), strings.Contains(reasonLower, "contradict"):
		return "collect_counterevidence"
	case strings.Contains(reasonLower, "timeout"), strings.Contains(reasonLower, "latency"):
		return "reduce_search_depth"
	case len(context) > 0:
		return "regenerate_next_step"
	default:
		return "replan"
	}
}

func deriveSubtopics(query string, domain string, limit int) ([]string, []string, []string) {
	if limit <= 0 {
		limit = 6
	}
	tokens := tokenizeResearchText(query)
	keywords := uniqueStrings(tokens)
	subtopics := deriveQuerySubtopics(query, limit)
	if len(subtopics) == 0 {
		subtopics = append(subtopics, defaultDomainSubtopics(domain)...)
	} else {
		subtopics = appendQueryAnchoredDomainSubtopics(domain, subtopics, limit)
	}
	subtopics = trimStrings(uniqueStrings(subtopics), limit)
	if len(subtopics) > limit {
		subtopics = subtopics[:limit]
	}
	variations := make([]string, 0, len(subtopics))
	for _, subtopic := range subtopics {
		variations = append(variations, strings.TrimSpace(query)+" "+strings.ToLower(subtopic))
	}
	if len(variations) > limit {
		variations = variations[:limit]
	}
	return subtopics, keywords, variations
}

func buildSubtopicsResponse(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, limit int) ([]string, []string, []string, string, string) {
	return buildSubtopicsResponseWithExclusions(ctx, agentGateway, query, domain, limit, nil)
}

func normalizeDynamicOptionValues(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, trimmed)
	}
	return uniqueStrings(cleaned)
}

func filterNovelDynamicOptionValues(candidates []string, excluded []string, limit int) []string {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, value := range normalizeDynamicOptionValues(excluded) {
		excludedSet[strings.ToLower(value)] = struct{}{}
	}

	filtered := make([]string, 0, len(candidates))
	for _, candidate := range normalizeDynamicOptionValues(candidates) {
		if _, exists := excludedSet[strings.ToLower(candidate)]; exists {
			continue
		}
		filtered = append(filtered, candidate)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func avoidRepeatedDynamicOptions(generated []string, excluded []string, limit int, replenish func(selected []string) []string) []string {
	novel := filterNovelDynamicOptionValues(generated, excluded, limit)
	if (limit <= 0 || len(novel) < limit) && replenish != nil {
		selected := normalizeDynamicOptionValues(append(append([]string{}, excluded...), novel...))
		for _, candidate := range normalizeDynamicOptionValues(replenish(selected)) {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			alreadyIncluded := false
			for _, existing := range novel {
				if strings.EqualFold(existing, candidate) {
					alreadyIncluded = true
					break
				}
			}
			if alreadyIncluded {
				continue
			}
			skipped := false
			for _, blocked := range selected {
				if strings.EqualFold(blocked, candidate) {
					skipped = true
					break
				}
			}
			if skipped {
				continue
			}
			novel = append(novel, candidate)
			selected = append(selected, candidate)
			if limit > 0 && len(novel) >= limit {
				break
			}
		}
	}
	if len(novel) == 0 {
		return trimStrings(uniqueStrings(generated), limit)
	}
	return trimStrings(novel, limit)
}

func deriveQuerySubtopics(query string, limit int) []string {
	rawTokens := tokenizeResearchTermsPreserveCase(query)
	if len(rawTokens) == 0 {
		return nil
	}

	candidates := make([]string, 0, limit)
	seen := map[string]struct{}{}
	addCandidate := func(candidate string) bool {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			return false
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
		candidates = append(candidates, trimmed)
		return true
	}

	for _, token := range rawTokens {
		if !looksLikeResearchAcronym(token) {
			continue
		}
		addCandidate(normalizeResearchDisplayToken(token))
		if limit > 0 && len(candidates) >= limit {
			return trimStrings(candidates, limit)
		}
	}

	usedPhraseTokens := map[string]struct{}{}
	for size := 3; size >= 2; size-- {
		if len(rawTokens) < size {
			continue
		}
		for index := 0; index+size <= len(rawTokens); index++ {
			window := rawTokens[index : index+size]
			if !windowFormsResearchPhrase(window) {
				continue
			}
			phraseParts := make([]string, 0, len(window))
			for _, token := range window {
				phraseParts = append(phraseParts, normalizeResearchDisplayToken(token))
			}
			if addCandidate(strings.Join(phraseParts, " ")) {
				for _, token := range window {
					usedPhraseTokens[strings.ToLower(strings.TrimSpace(token))] = struct{}{}
				}
			}
			if limit > 0 && len(candidates) >= limit {
				return trimStrings(candidates, limit)
			}
		}
	}

	for _, token := range rawTokens {
		lower := strings.ToLower(strings.TrimSpace(token))
		if len(lower) < 5 {
			continue
		}
		if _, used := usedPhraseTokens[lower]; used {
			continue
		}
		addCandidate(normalizeResearchDisplayToken(token))
		if limit > 0 && len(candidates) >= limit {
			break
		}
	}

	return trimStrings(candidates, limit)
}

func appendQueryAnchoredDomainSubtopics(domain string, selected []string, limit int) []string {
	augmented := trimStrings(uniqueStrings(selected), limit)
	if limit > 0 && len(augmented) >= limit {
		return augmented
	}

	defaults := defaultDomainSubtopics(domain)
	if len(defaults) == 0 {
		return augmented
	}

	seen := map[string]struct{}{}
	for _, item := range augmented {
		seen[strings.ToLower(strings.TrimSpace(item))] = struct{}{}
	}

	addCandidate := func(candidate string) bool {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			return false
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
		augmented = append(augmented, trimmed)
		return true
	}

	anchors := make([]string, 0, len(augmented))
	for _, item := range augmented {
		if strings.Contains(item, " ") {
			anchors = append(anchors, item)
		}
	}
	for _, item := range augmented {
		if !strings.Contains(item, " ") {
			anchors = append(anchors, item)
		}
	}

	for _, anchor := range anchors {
		anchorLower := strings.ToLower(strings.TrimSpace(anchor))
		if anchorLower == "" {
			continue
		}
		for _, facet := range defaults {
			facetLower := strings.ToLower(strings.TrimSpace(facet))
			if facetLower == "" {
				continue
			}
			if strings.Contains(anchorLower, facetLower) || strings.Contains(facetLower, anchorLower) {
				continue
			}
			addCandidate(strings.TrimSpace(anchor + " " + facet))
			if limit > 0 && len(augmented) >= limit {
				return trimStrings(augmented, limit)
			}
		}
	}

	for _, facet := range defaults {
		addCandidate(facet)
		if limit > 0 && len(augmented) >= limit {
			break
		}
	}

	return trimStrings(augmented, limit)
}

func buildSubtopicsResponseWithExclusions(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, limit int, excluded []string) ([]string, []string, []string, string, string) {
	if limit <= 0 {
		limit = 6
	}
	fallbackSource := "heuristic_fallback"
	fallbackExplanation := ""
	avoidLine := ""
	if filteredExcluded := normalizeDynamicOptionValues(excluded); len(filteredExcluded) > 0 {
		avoidLine = fmt.Sprintf("Avoid repeating any of these already shown subtopics: %s.", strings.Join(filteredExcluded, ", "))
	}
	if agentGateway != nil && agentGateway.LLMClient != nil {
		requestCtx, structuredClient, cancel := interactiveStructuredRequest(ctx, agentGateway.LLMClient)
		defer cancel()
		prompt := fmt.Sprintf(`You are WisDev, an academic research planner.
Query: %q
Domain: %q
Generate exactly %d distinct research subtopics for this query. Aim for methodological, thematic, and contextual diversity to reach %d — if the topic is narrow, include related methods, datasets, population angles, or contextual dimensions. Return retrieval keywords, query variations, and a brief explanation of your choices.
%s
%s`, query, domain, limit, limit, avoidLine, structuredOutputSchemaInstruction)
		schema := fmt.Sprintf(`{"type":"object","properties":{"subtopics":{"type":"array","items":{"type":"string"},"maxItems":%d},"keywords":{"type":"array","items":{"type":"string"},"maxItems":%d},"queryVariations":{"type":"array","items":{"type":"string"},"maxItems":%d},"explanation":{"type":"string"}},"required":["subtopics","keywords","queryVariations","explanation"]}`, limit, limit, limit)
		resp, err := structuredClient.StructuredOutput(requestCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveLightModel(),
			JsonSchema: schema,
		}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
			RequestedTier: "light",
			Structured:    true,
			HighValue:     false,
		})))
		if err == nil {
			var parsed struct {
				Subtopics       []string `json:"subtopics"`
				Keywords        []string `json:"keywords"`
				QueryVariations []string `json:"queryVariations"`
				Explanation     string   `json:"explanation"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				subtopics := avoidRepeatedDynamicOptions(parsed.Subtopics, excluded, limit, func(selected []string) []string {
					return recommendAdditionalSubtopics(query, domain, selected)
				})
				if len(subtopics) > 0 {
					explanation := strings.TrimSpace(parsed.Explanation)
					if len(normalizeDynamicOptionValues(excluded)) > 0 && explanation != "" {
						explanation += " Regenerated to avoid repeating prior options."
					}
					return trimStrings(subtopics, limit), trimStrings(uniqueStrings(parsed.Keywords), limit), trimStrings(uniqueStrings(parsed.QueryVariations), limit), "llm_structured", explanation
				}
				logInteractiveStructuredFallback("subtopics_generate", "llm_empty_response", query, nil)
				fallbackSource = "structured_invalid_fallback"
				fallbackExplanation = "Regenerated heuristic options because model structured output was empty."
			} else {
				logInteractiveStructuredFallback("subtopics_generate", "llm_invalid_response", query, fmt.Errorf("structured output JSON decode failed"))
				fallbackSource = "structured_invalid_fallback"
				fallbackExplanation = "Regenerated heuristic options because model structured output was invalid."
			}
		} else {
			logInteractiveStructuredFallback("subtopics_generate", "llm_request_failed", query, err)
		}
	} else {
		logInteractiveStructuredFallback("subtopics_generate", "llm_unavailable", query, nil)
	}
	subtopics, keywords, variations := deriveSubtopics(query, domain, limit)
	subtopics = avoidRepeatedDynamicOptions(subtopics, excluded, limit, func(selected []string) []string {
		return recommendAdditionalSubtopics(query, domain, selected)
	})
	explanation := ""
	if len(normalizeDynamicOptionValues(excluded)) > 0 {
		explanation = "Regenerated heuristic options while avoiding previously shown values."
	}
	if fallbackExplanation != "" {
		if explanation != "" {
			explanation += " "
		}
		explanation += fallbackExplanation
	}
	return subtopics, keywords, variations, fallbackSource, explanation
}

func deriveStudyTypes(query string, domain string, subtopics []string, limit int) ([]string, []string) {
	if limit <= 0 {
		limit = 5
	}
	studyTypes := defaultStudyTypes(domain)
	signals := []string{}
	queryLower := strings.ToLower(query)
	if strings.Contains(queryLower, "review") || strings.Contains(queryLower, "survey") {
		studyTypes = append([]string{"systematic review", "meta-analysis"}, studyTypes...)
		signals = append(signals, "review_intent")
	}
	if strings.Contains(queryLower, "compare") || strings.Contains(queryLower, "versus") {
		studyTypes = append([]string{"comparative study", "benchmark"}, studyTypes...)
		signals = append(signals, "comparative_intent")
	}
	if len(subtopics) >= 3 {
		studyTypes = append(studyTypes, "mixed methods")
		signals = append(signals, "broad_topic_surface")
	}
	studyTypes = uniqueStrings(studyTypes)
	if len(studyTypes) > limit {
		studyTypes = studyTypes[:limit]
	}
	return studyTypes, uniqueStrings(signals)
}

func buildStudyTypesResponse(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, subtopics []string, limit int) ([]string, []string, string, string) {
	return buildStudyTypesResponseWithExclusions(ctx, agentGateway, query, domain, subtopics, limit, nil)
}

func buildStudyTypesResponseWithExclusions(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, subtopics []string, limit int, excluded []string) ([]string, []string, string, string) {
	if limit <= 0 {
		limit = 5
	}
	fallbackSource := "heuristic_fallback"
	fallbackExplanation := ""
	avoidLine := ""
	if filteredExcluded := normalizeDynamicOptionValues(excluded); len(filteredExcluded) > 0 {
		avoidLine = fmt.Sprintf("Avoid repeating any of these already shown study types: %s.", strings.Join(filteredExcluded, ", "))
	}
	if agentGateway != nil && agentGateway.LLMClient != nil {
		requestCtx, structuredClient, cancel := interactiveStructuredRequest(ctx, agentGateway.LLMClient)
		defer cancel()
		prompt := fmt.Sprintf(`You are WisDev, a research methods advisor.
Query: %q
Domain: %q
Subtopics: %s
Generate exactly %d study types that best cover this research space. Aim for methodological diversity to reach %d — if the topic is narrow, include related designs, evaluation strategies, or secondary evidence types. Return the signals that justify each type and a brief explanation.
%s
%s`, query, domain, strings.Join(subtopics, ", "), limit, limit, avoidLine, structuredOutputSchemaInstruction)
		schema := fmt.Sprintf(`{"type":"object","properties":{"studyTypes":{"type":"array","items":{"type":"string"},"maxItems":%d},"matchedSignals":{"type":"array","items":{"type":"string"},"maxItems":%d},"explanation":{"type":"string"}},"required":["studyTypes","matchedSignals","explanation"]}`, limit, limit)
		resp, err := structuredClient.StructuredOutput(requestCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveLightModel(),
			JsonSchema: schema,
		}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
			RequestedTier: "light",
			Structured:    true,
			HighValue:     false,
		})))
		if err == nil {
			var parsed struct {
				StudyTypes     []string `json:"studyTypes"`
				MatchedSignals []string `json:"matchedSignals"`
				Explanation    string   `json:"explanation"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				studyTypes := avoidRepeatedDynamicOptions(parsed.StudyTypes, excluded, limit, func(selected []string) []string {
					return recommendAdditionalStudyTypes(query, domain, selected)
				})
				if len(studyTypes) > 0 {
					explanation := strings.TrimSpace(parsed.Explanation)
					if len(normalizeDynamicOptionValues(excluded)) > 0 && explanation != "" {
						explanation += " Regenerated to avoid repeating prior options."
					}
					return trimStrings(studyTypes, limit), trimStrings(uniqueStrings(parsed.MatchedSignals), limit), "llm_structured", explanation
				}
				logInteractiveStructuredFallback("study_types_generate", "llm_empty_response", query, nil)
				fallbackSource = "structured_invalid_fallback"
				fallbackExplanation = "Regenerated heuristic study types because model structured output was empty."
			} else {
				logInteractiveStructuredFallback("study_types_generate", "llm_invalid_response", query, fmt.Errorf("structured output JSON decode failed"))
				fallbackSource = "structured_invalid_fallback"
				fallbackExplanation = "Regenerated heuristic study types because model structured output was invalid."
			}
		} else {
			logInteractiveStructuredFallback("study_types_generate", "llm_request_failed", query, err)
		}
	} else {
		logInteractiveStructuredFallback("study_types_generate", "llm_unavailable", query, nil)
	}
	studyTypes, signals := deriveStudyTypes(query, domain, subtopics, limit)
	studyTypes = avoidRepeatedDynamicOptions(studyTypes, excluded, limit, func(selected []string) []string {
		return recommendAdditionalStudyTypes(query, domain, selected)
	})
	explanation := ""
	if len(normalizeDynamicOptionValues(excluded)) > 0 {
		explanation = "Regenerated heuristic options while avoiding previously shown values."
	}
	if fallbackExplanation != "" {
		if explanation != "" {
			explanation += " "
		}
		explanation += fallbackExplanation
	}
	return studyTypes, signals, fallbackSource, explanation
}

func estimatePathScore(query string, subtopics []string, studyTypes []string) float64 {
	score := 0.35
	if strings.TrimSpace(query) != "" {
		score += 0.15
	}
	score += 0.1 * float64(wisdev.MinInt(len(uniqueStrings(subtopics)), 3))
	score += 0.1 * float64(wisdev.MinInt(len(uniqueStrings(studyTypes)), 2))
	return clampScore(score)
}

func buildResearchPathScore(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, subtopics []string, studyTypes []string, score float64, target float64) (float64, string, string) {
	if score > 0 {
		return clampScore(score), "Client supplied coverage score reused.", "client_supplied"
	}
	if agentGateway != nil && agentGateway.LLMClient != nil {
		requestCtx, structuredClient, cancel := interactiveStructuredRequest(ctx, agentGateway.LLMClient)
		defer cancel()
		prompt := fmt.Sprintf(`You are WisDev, a research architect.
Query: %q
Domain: %q
Subtopics: %s
Study types: %s
Target coverage: %.2f
Assess whether the current research path is ready, then provide the readiness score and concise reasoning.
%s`, query, domain, strings.Join(subtopics, ", "), strings.Join(studyTypes, ", "), target, structuredOutputSchemaInstruction)
		resp, err := structuredClient.StructuredOutput(requestCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveStandardModel(),
			JsonSchema: `{"type":"object","properties":{"pathScore":{"type":"number"},"reasoning":{"type":"string"}},"required":["pathScore","reasoning"]}`,
		}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
			RequestedTier: "standard",
			Structured:    true,
			HighValue:     true,
		})))
		if err == nil {
			var parsed struct {
				PathScore float64 `json:"pathScore"`
				Reasoning string  `json:"reasoning"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				reasoning := strings.TrimSpace(parsed.Reasoning)
				if reasoning == "" {
					reasoning = "Structured readiness assessment completed."
				}
				return clampScore(parsed.PathScore), reasoning, "llm_structured"
			} else {
				logInteractiveStructuredFallback("research_path_evaluate", "llm_invalid_response", query, fmt.Errorf("structured output JSON decode failed"))
				fallback := estimatePathScore(query, subtopics, studyTypes)
				return fallback, "Heuristic score used because model structured output was invalid.", "structured_invalid_fallback"
			}
		} else {
			logInteractiveStructuredFallback("research_path_evaluate", "llm_request_failed", query, err)
		}
	}
	fallback := estimatePathScore(query, subtopics, studyTypes)
	return fallback, "Heuristic score based on topic breadth and method diversity.", "heuristic_fallback"
}

func buildResearchPathStrengths(subtopics []string, studyTypes []string, score float64) []string {
	strengths := []string{}
	if len(subtopics) >= 2 {
		strengths = append(strengths, "Topic decomposition is broad enough for multi-angle evidence collection.")
	}
	if len(studyTypes) > 0 {
		strengths = append(strengths, "Methodological diversity is represented in the planned evidence mix.")
	}
	if score >= 0.75 {
		strengths = append(strengths, "Current path is strong enough to move into evidence collection without replanning.")
	}
	if len(strengths) == 0 {
		strengths = append(strengths, "A minimal research path exists and can be strengthened with one more refinement pass.")
	}
	return strengths
}

func buildResearchPathGaps(subtopics []string, studyTypes []string, score float64, target float64) []string {
	gaps := []string{}
	if len(subtopics) < 2 {
		gaps = append(gaps, "Topic surface is too narrow.")
	}
	if len(studyTypes) == 0 {
		gaps = append(gaps, "No study-type constraints were selected.")
	}
	if score < target {
		gaps = append(gaps, "Coverage score is below the target threshold.")
	}
	return gaps
}

func buildRecommendedNextStep(subtopics []string, studyTypes []string, score float64, target float64) string {
	switch {
	case len(subtopics) < 2:
		return "Add one or two more focused subtopics before searching."
	case len(studyTypes) == 0:
		return "Add at least one study-type constraint to balance evidence quality."
	case score < target:
		return "Broaden the query set and collect another round of evidence."
	default:
		return "Proceed to retrieval and evidence synthesis."
	}
}

func recommendAdditionalSubtopics(query string, domain string, selected []string) []string {
	// derive a larger primary pool from the query, then fall back to the domain
	// defaults so we always have enough candidates to fill the caller's limit.
	candidates, _, _ := deriveSubtopics(query, domain, 10)
	candidates = append(candidates, defaultDomainSubtopics(domain)...)
	candidates = uniqueStrings(candidates)
	selectedSet := map[string]struct{}{}
	for _, item := range selected {
		selectedSet[strings.ToLower(strings.TrimSpace(item))] = struct{}{}
	}
	recommended := []string{}
	for _, candidate := range candidates {
		if _, exists := selectedSet[strings.ToLower(strings.TrimSpace(candidate))]; exists {
			continue
		}
		recommended = append(recommended, candidate)
	}
	return recommended
}

func recommendAdditionalStudyTypes(query string, domain string, selected []string) []string {
	// derive a larger primary pool from the query, then augment with domain
	// defaults so we can always fill the caller's requested limit.
	candidates, _ := deriveStudyTypes(query, domain, nil, 10)
	candidates = append(candidates, defaultStudyTypes(domain)...)
	candidates = uniqueStrings(candidates)
	selectedSet := map[string]struct{}{}
	for _, item := range selected {
		selectedSet[strings.ToLower(strings.TrimSpace(item))] = struct{}{}
	}
	recommended := []string{}
	for _, candidate := range candidates {
		if _, exists := selectedSet[strings.ToLower(strings.TrimSpace(candidate))]; exists {
			continue
		}
		recommended = append(recommended, candidate)
	}
	return recommended
}

func buildResearchPathSignals(domain string, subtopics []string, studyTypes []string, score float64) []string {
	signals := []string{"query_present"}
	if strings.TrimSpace(domain) != "" {
		signals = append(signals, "domain:"+strings.ToLower(strings.TrimSpace(domain)))
	}
	if len(subtopics) >= 2 {
		signals = append(signals, "subtopics_sufficient")
	}
	if len(studyTypes) > 0 {
		signals = append(signals, "study_types_present")
	}
	if score >= 0.7 {
		signals = append(signals, "coverage_ready")
	}
	return uniqueStrings(signals)
}

func evaluateCoverage(query string, queries []string, results []map[string]any, papers []map[string]any) (float64, []string, []string) {
	uniqueQueries := uniqueStrings(queries)
	tokens := tokenizeResearchText(query)
	matchedTerms := map[string]struct{}{}
	observeText := func(text string) {
		lower := strings.ToLower(text)
		for _, token := range tokens {
			if strings.Contains(lower, token) {
				matchedTerms[token] = struct{}{}
			}
		}
	}
	for _, q := range uniqueQueries {
		observeText(q)
	}
	for _, item := range results {
		observeText(fmt.Sprintf("%v %v %v", item["title"], item["summary"], item["abstract"]))
	}
	for _, paper := range papers {
		observeText(fmt.Sprintf("%v %v %v", paper["title"], paper["summary"], paper["abstract"]))
	}
	termCoverage := 0.0
	if len(tokens) > 0 {
		termCoverage = float64(len(matchedTerms)) / float64(len(tokens))
	}
	resultCount := len(results)
	if resultCount == 0 {
		resultCount = len(papers)
	}
	queryScore := clampScore(float64(wisdev.MinInt(len(uniqueQueries), 4)) / 4.0)
	resultScore := clampScore(float64(wisdev.MinInt(resultCount, 12)) / 12.0)
	coverage := clampScore((termCoverage * 0.45) + (queryScore * 0.25) + (resultScore * 0.30))
	missingTerms := []string{}
	for _, token := range tokens {
		if _, ok := matchedTerms[token]; !ok {
			missingTerms = append(missingTerms, token)
		}
	}
	sort.Strings(missingTerms)
	recommendedQueries := []string{}
	base := strings.TrimSpace(query)
	for _, missing := range missingTerms {
		recommendedQueries = append(recommendedQueries, strings.TrimSpace(base+" "+missing))
		if len(recommendedQueries) >= 3 {
			break
		}
	}
	return coverage, missingTerms, uniqueStrings(recommendedQueries)
}

func buildCoverageEvaluation(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, queries []string, results []map[string]any, papers []map[string]any) (float64, []string, []string, string) {
	coverage, missingTerms, recommendedQueries := evaluateCoverage(query, queries, results, papers)
	if agentGateway != nil && agentGateway.LLMClient != nil {
		requestCtx, structuredClient, cancel := interactiveStructuredRequest(ctx, agentGateway.LLMClient)
		defer cancel()
		prompt := fmt.Sprintf(`You are WisDev, a search quality evaluator.
Primary query: %q
Executed queries: %s
Result count: %d
Missing terms detected heuristically: %s
Return an improved coverageScore, missingTerms, and recommendedQueries.
%s`, query, strings.Join(uniqueStrings(queries), " | "), wisdev.MaxInt(len(results), len(papers)), strings.Join(missingTerms, ", "), structuredOutputSchemaInstruction)
		resp, err := structuredClient.StructuredOutput(requestCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveLightModel(),
			JsonSchema: `{"type":"object","properties":{"coverageScore":{"type":"number"},"missingTerms":{"type":"array","items":{"type":"string"}},"recommendedQueries":{"type":"array","items":{"type":"string"}}},"required":["coverageScore","missingTerms","recommendedQueries"]}`,
		}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
			RequestedTier: "light",
			Structured:    true,
			HighValue:     false,
		})))
		if err == nil {
			var parsed struct {
				CoverageScore      float64  `json:"coverageScore"`
				MissingTerms       []string `json:"missingTerms"`
				RecommendedQueries []string `json:"recommendedQueries"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				return clampScore(parsed.CoverageScore), trimStrings(parsed.MissingTerms, 6), trimStrings(parsed.RecommendedQueries, 3), "llm_structured"
			} else {
				logInteractiveStructuredFallback("search_coverage_evaluate", "llm_invalid_response", query, fmt.Errorf("structured output JSON decode failed"))
				return coverage, missingTerms, recommendedQueries, "structured_invalid_fallback"
			}
		} else {
			logInteractiveStructuredFallback("search_coverage_evaluate", "llm_request_failed", query, err)
		}
	}
	return coverage, missingTerms, recommendedQueries, "heuristic_fallback"
}

func followUpOptionSignature(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", " ", "_", " ", "/", " ").Replace(normalized)
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return ""
	}
	stop := map[string]struct{}{
		"and": {}, "or": {}, "the": {}, "a": {}, "an": {}, "of": {}, "for": {}, "in": {}, "on": {},
		"reinforcement": {}, "learning": {}, "ai": {}, "ml": {}, "model": {}, "models": {},
	}
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, skip := stop[field]; skip {
			continue
		}
		kept = append(kept, field)
	}
	if len(kept) == 0 {
		return strings.Join(fields, " ")
	}
	return strings.Join(kept, " ")
}

func buildFollowUpOptionPayload(value string, query string, domain string) map[string]any {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	queryLower := strings.ToLower(query)

	label := toTitlePhrase(trimmed)
	description := "Expand retrieval around this unresolved coverage gap."
	switch {
	case strings.Contains(lower, "rlhf"):
		label = "RLHF methods and reward modeling"
		description = "Focus on preference learning, reward models, human feedback, and alignment methods."
	case strings.Contains(lower, "benchmark") || strings.Contains(lower, "evaluation"):
		label = "Evaluation benchmarks and generalization"
		description = "Prioritize benchmark suites, evaluation protocols, and out-of-distribution performance."
	case strings.Contains(lower, "training data") || strings.Contains(lower, "dataset") || strings.Contains(lower, "data"):
		label = "Training data and feedback quality"
		description = "Search for dataset construction, feedback sources, data quality, and annotation effects."
	case strings.Contains(lower, "reinforcement learning") && strings.Contains(queryLower, "rlhf"):
		label = "RL optimization methods"
		description = "Cover policy optimization and reinforcement-learning methods that support RLHF systems."
	case strings.EqualFold(strings.TrimSpace(domain), "cs") || strings.EqualFold(strings.TrimSpace(domain), "ai"):
		description = "Add a distinct computer-science angle for the next retrieval pass."
	}

	return map[string]any{
		"value":       label,
		"label":       label,
		"description": description,
	}
}

func appendFollowUpOption(options []map[string]any, seen map[string]struct{}, value string, query string, domain string, limit int) []map[string]any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (limit > 0 && len(options) >= limit) {
		return options
	}
	payload := buildFollowUpOptionPayload(trimmed, query, domain)
	label := strings.TrimSpace(wisdev.AsOptionalString(payload["label"]))
	signature := followUpOptionSignature(label)
	if signature == "" {
		signature = strings.ToLower(label)
	}
	if _, exists := seen[signature]; exists {
		return options
	}
	seen[signature] = struct{}{}
	options = append(options, payload)
	return options
}

func buildFollowUpOptions(query string, domain string, candidates []string, limit int) []map[string]any {
	options := []map[string]any{}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		options = appendFollowUpOption(options, seen, candidate, query, domain, limit)
		if limit > 0 && len(options) >= limit {
			break
		}
	}
	return options
}

func buildFollowUpQuestion(query string, domain string, missingTerms []string, subtopics []string, studyTypes []string) map[string]any {
	options := []map[string]any{}
	targetQuestionID := "q4_subtopics"
	options = buildFollowUpOptions(query, domain, missingTerms, 4)
	// If enough subtopics are already chosen, skip re-asking about subtopics
	// and route directly to study type selection.
	if len(options) == 0 && len(subtopics) >= 3 {
		targetQuestionID = "q5_study_types"
		options = buildFollowUpOptions(query, domain, recommendAdditionalStudyTypes(query, domain, studyTypes), 4)
	}
	if len(options) == 0 {
		options = buildFollowUpOptions(query, domain, recommendAdditionalSubtopics(query, domain, subtopics), 4)
	}
	if len(options) == 0 {
		targetQuestionID = "q5_study_types"
		options = buildFollowUpOptions(query, domain, recommendAdditionalStudyTypes(query, domain, studyTypes), 4)
	}
	return map[string]any{
		"id":               "follow_up_refinement",
		"type":             "clarification",
		"question":         "Which focus should the next search pass prioritize?",
		"isMultiSelect":    true,
		"isRequired":       true,
		"options":          options,
		"helpText":         "Pick the focus areas that should guide the next retrieval pass.",
		"targetQuestionId": targetQuestionID,
	}
}

func buildFollowUpQuestionExplanation(domain string, coverageScore float64, pathScore float64, target float64, missingTerms []string, subtopics []string, studyTypes []string) string {
	trimmedMissing := trimStrings(missingTerms, 3)
	switch {
	case len(trimmedMissing) > 0:
		return fmt.Sprintf("WisDev found unresolved coverage gaps around %s before expanding the next search pass.", strings.Join(trimmedMissing, ", "))
	case len(subtopics) < 2:
		return "WisDev needs one more clarification on the most important subtopics before expanding the next search pass."
	case strings.EqualFold(strings.TrimSpace(domain), "medicine") && len(studyTypes) == 0:
		return "WisDev needs one more clarification on the study designs to prioritize before expanding the next search pass."
	case coverageScore < target || pathScore < target:
		return "WisDev flagged the current research path as under-covered and queued one more clarification step before synthesis."
	default:
		return "WisDev queued one more clarification step to keep the next search iteration focused."
	}
}

func sanitizeFollowUpQuestionOptions(question map[string]any, query string, domain string) {
	if len(questionOptionValues(question["options"])) == 0 {
		return
	}
	question["options"] = buildFollowUpOptions(query, domain, questionOptionValues(question["options"]), 4)
}

func sanitizeFollowUpQuestionText(question map[string]any) {
	text := strings.TrimSpace(wisdev.AsOptionalString(question["question"]))
	lower := strings.ToLower(text)
	if text == "" || strings.Contains(lower, "missing area") || strings.Contains(lower, "wisdev expand") {
		question["question"] = "Which focus should the next search pass prioritize?"
	}
	if strings.TrimSpace(wisdev.AsOptionalString(question["helpText"])) == "" {
		question["helpText"] = "Pick the focus areas that should guide the next retrieval pass."
	}
}

func annotateFollowUpQuestion(question map[string]any, source string, explanation string, targetQuestionID string, query string, domain string) map[string]any {
	if len(question) == 0 {
		return nil
	}
	annotated := cloneAnyMap(question)
	sanitizeFollowUpQuestionText(annotated)
	sanitizeFollowUpQuestionOptions(annotated, query, domain)
	if strings.TrimSpace(wisdev.AsOptionalString(annotated["id"])) == "" {
		annotated["id"] = "follow_up_refinement"
	}
	if strings.TrimSpace(wisdev.AsOptionalString(annotated["type"])) == "" {
		annotated["type"] = "clarification"
	}
	if strings.TrimSpace(wisdev.AsOptionalString(annotated["questionSource"])) == "" && strings.TrimSpace(source) != "" {
		annotated["questionSource"] = source
	}
	if strings.TrimSpace(wisdev.AsOptionalString(annotated["questionExplanation"])) == "" && strings.TrimSpace(explanation) != "" {
		annotated["questionExplanation"] = explanation
	}
	if strings.TrimSpace(wisdev.AsOptionalString(annotated["targetQuestionId"])) == "" && strings.TrimSpace(targetQuestionID) != "" {
		annotated["targetQuestionId"] = targetQuestionID
	}
	if len(questionOptionValues(annotated["options"])) > 0 {
		if strings.TrimSpace(wisdev.AsOptionalString(annotated["optionsSource"])) == "" && strings.TrimSpace(source) != "" {
			annotated["optionsSource"] = source
		}
		if strings.TrimSpace(wisdev.AsOptionalString(annotated["optionsExplanation"])) == "" && strings.TrimSpace(explanation) != "" {
			annotated["optionsExplanation"] = explanation
		}
	}
	return annotated
}

type followUpDecisionLLMResult struct {
	needsFollowUp    bool
	followUpQuestion map[string]any
	err              error
}

func requestFollowUpDecisionWithLLM(ctx context.Context, agentGateway *wisdev.AgentGateway, prompt string) (bool, map[string]any, error) {
	if agentGateway == nil || agentGateway.LLMClient == nil {
		return false, nil, fmt.Errorf("llm client unavailable")
	}

	backstopBudget := wisdevFollowUpDecisionSidecarBackstopBudget()
	resultCh := make(chan followUpDecisionLLMResult, 1)
	go func() {
		llmCtxBase := context.WithoutCancel(ctx)
		llmCtx, llmCancel := context.WithTimeout(llmCtxBase, backstopBudget)
		defer llmCancel()

		sidecarClient := agentGateway.LLMClient.WithoutVertexDirect().WithTimeout(backstopBudget)
		resp, err := sidecarClient.StructuredOutput(llmCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveStandardModel(),
			JsonSchema: `{"type":"object","properties":{"needsFollowUp":{"type":"boolean"},"followUpQuestion":{"type":["object","null"]}},"required":["needsFollowUp","followUpQuestion"]}`,
		}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
			RequestedTier: "standard",
			Structured:    true,
			HighValue:     true,
		})))
		if err != nil {
			resultCh <- followUpDecisionLLMResult{err: err}
			return
		}

		var parsed struct {
			NeedsFollowUp    bool           `json:"needsFollowUp"`
			FollowUpQuestion map[string]any `json:"followUpQuestion"`
		}
		if err := json.Unmarshal([]byte(resp.JsonResult), &parsed); err != nil {
			resultCh <- followUpDecisionLLMResult{
				err: fmt.Errorf("structured output JSON decode failed: %w", err),
			}
			return
		}

		resultCh <- followUpDecisionLLMResult{
			needsFollowUp:    parsed.NeedsFollowUp,
			followUpQuestion: parsed.FollowUpQuestion,
		}
	}()

	waitCtx := ctx
	waitCancel := func() {}
	if wisdevFollowUpDecisionTimeout > 0 {
		waitCtx, waitCancel = context.WithTimeout(ctx, wisdevFollowUpDecisionTimeout)
	}
	defer waitCancel()

	select {
	case result := <-resultCh:
		return result.needsFollowUp, result.followUpQuestion, result.err
	case <-waitCtx.Done():
		return false, nil, waitCtx.Err()
	}
}

func buildFollowUpDecision(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, coverageScore float64, pathScore float64, target float64, missingTerms []string, subtopics []string, studyTypes []string) (bool, map[string]any, string) {
	needsFollowUp := coverageScore < target || pathScore < target || len(missingTerms) > 0 || len(subtopics) < 2
	explanation := buildFollowUpQuestionExplanation(domain, coverageScore, pathScore, target, missingTerms, subtopics, studyTypes)
	fallbackQuestion := annotateFollowUpQuestion(
		buildFollowUpQuestion(query, domain, missingTerms, subtopics, studyTypes),
		"heuristic_fallback",
		explanation,
		"",
		query,
		domain,
	)
	targetQuestionID := strings.TrimSpace(wisdev.AsOptionalString(fallbackQuestion["targetQuestionId"]))
	if agentGateway != nil && agentGateway.LLMClient != nil && needsFollowUp {
		prompt := fmt.Sprintf(`You are WisDev, an interactive research guide.
Query: %q
Domain: %q
Coverage score: %.2f
Path score: %.2f
Missing terms: %s
Return whether follow-up is needed and a single best follow-up question object. If you return options, make them 2-4 distinct, actionable research facets with short descriptions; do not return broad duplicates such as an acronym, its expansion, and minor variants as separate choices.
%s`, query, domain, coverageScore, pathScore, strings.Join(missingTerms, ", "), structuredOutputSchemaInstruction)
		parsedNeedsFollowUp, parsedQuestion, err := requestFollowUpDecisionWithLLM(ctx, agentGateway, prompt)
		if err == nil {
			if parsedNeedsFollowUp && len(parsedQuestion) > 0 {
				return true, annotateFollowUpQuestion(parsedQuestion, "llm_structured", explanation, targetQuestionID, query, domain), "llm_structured"
			}
		} else {
			slog.Warn("wisdev follow-up decision ai fallback",
				"component", "api.wisdev",
				"operation", "follow_up_decision",
				"stage", "llm_request_failed",
				"query_hash", searchQueryFingerprint(query),
				"target_question_id", targetQuestionID,
				"timeout_ms", wisdevFollowUpDecisionTimeout.Milliseconds(),
				"error", err.Error(),
				"fallback_source", "heuristic_fallback",
			)
		}
	} else if needsFollowUp {
		slog.Warn("wisdev follow-up decision ai fallback",
			"component", "api.wisdev",
			"operation", "follow_up_decision",
			"stage", "llm_unavailable",
			"query_hash", searchQueryFingerprint(query),
			"target_question_id", targetQuestionID,
			"fallback_source", "heuristic_fallback",
		)
	}
	if needsFollowUp {
		return true, fallbackQuestion, "heuristic_fallback"
	}
	return false, nil, "heuristic_fallback"
}

func tokenizeResearchText(value string) []string {
	parts := tokenizeResearchTermsPreserveCase(value)
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		tokens = append(tokens, strings.ToLower(strings.TrimSpace(part)))
	}
	return uniqueStrings(tokens)
}

func tokenizeResearchTermsPreserveCase(value string) []string {
	replacer := strings.NewReplacer(",", " ", ".", " ", ":", " ", ";", " ", "/", " ", "-", " ", "(", " ", ")", " ", "\"", " ", "'", " ")
	text := replacer.Replace(value)
	parts := strings.Fields(text)
	tokens := []string{}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if len(trimmed) < 3 {
			continue
		}
		if _, blocked := researchTokenStopwords[strings.ToLower(trimmed)]; blocked {
			continue
		}
		tokens = append(tokens, trimmed)
	}
	return uniqueStrings(tokens)
}

func normalizeResearchDisplayToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if looksLikeResearchAcronym(trimmed) || trimmed != strings.ToLower(trimmed) {
		return trimmed
	}
	return toTitlePhrase(strings.ToLower(trimmed))
}

func looksLikeResearchAcronym(token string) bool {
	trimmed := strings.TrimSpace(token)
	if len(trimmed) < 2 || len(trimmed) > 12 {
		return false
	}
	hasUpper := false
	hasLetter := false
	for _, char := range trimmed {
		switch {
		case char >= 'A' && char <= 'Z':
			hasUpper = true
			hasLetter = true
		case char >= 'a' && char <= 'z':
			return false
		case char >= '0' && char <= '9':
		case char == '-' || char == '+' || char == '/':
		default:
			return false
		}
	}
	return hasUpper && hasLetter
}

func windowFormsResearchPhrase(window []string) bool {
	if len(window) < 2 || len(window) > 3 {
		return false
	}
	for _, token := range window {
		trimmed := strings.TrimSpace(token)
		if len(trimmed) < 3 || looksLikeResearchAcronym(trimmed) {
			return false
		}
	}

	last := strings.ToLower(strings.TrimSpace(window[len(window)-1]))
	if _, ok := researchPhraseHeads[last]; !ok {
		return false
	}

	if len(window) == 3 {
		first := strings.ToLower(strings.TrimSpace(window[0]))
		second := strings.ToLower(strings.TrimSpace(window[1]))
		if _, firstIsHead := researchPhraseHeads[first]; firstIsHead {
			if _, secondIsHead := researchPhraseHeads[second]; !secondIsHead {
				return false
			}
		}
	}

	return true
}

func defaultDomainSubtopics(domain string) []string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "healthcare":
		return []string{"Clinical Outcomes", "Patient Selection", "Safety Signals", "Adverse Events", "Biomarkers", "Treatment Protocols", "Epidemiology", "Diagnostic Accuracy"}
	case "cs", "computer_science", "ai":
		return []string{"Benchmarks", "Training Data", "Evaluation", "Model Architecture", "Inference Efficiency", "Robustness", "Generalization", "Reproducibility"}
	case "social", "social_sciences":
		return []string{"Survey Methods", "Behavioral Outcomes", "Policy Implications", "Longitudinal Studies", "Demographic Analysis", "Qualitative Analysis", "Cross-Cultural Comparison"}
	case "climate", "environment":
		return []string{"Emissions Modeling", "Climate Projections", "Adaptation Strategies", "Ecosystem Impacts", "Policy Interventions", "Remote Sensing", "Carbon Cycling"}
	case "neuro", "neuroscience":
		return []string{"Neural Circuits", "Cognitive Function", "Brain Imaging", "Synaptic Plasticity", "Behavioral Paradigms", "Connectomics", "Neurodegeneration"}
	case "physics", "engineering":
		return []string{"Experimental Validation", "Simulation Methods", "Material Properties", "Energy Systems", "Control Theory", "Quantum Effects", "Scalability"}
	case "biology", "life_sciences":
		return []string{"Molecular Mechanisms", "Genomics", "Protein Function", "Cell Signaling", "Evolution", "Metabolic Pathways", "Model Organisms"}
	case "humanities":
		return []string{"Historical Context", "Textual Analysis", "Cultural Interpretation", "Archival Sources", "Comparative Literature", "Ethical Dimensions", "Theoretical Frameworks"}
	default:
		return []string{"Methods", "Limitations", "Applications", "Theoretical Background", "Empirical Evidence", "Comparative Analysis", "Future Directions"}
	}
}

func defaultStudyTypes(domain string) []string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "healthcare":
		return []string{"randomized controlled trial", "systematic review", "observational study", "meta-analysis", "cohort study", "case-control study"}
	case "cs", "computer_science", "ai":
		return []string{"benchmark", "ablation study", "system evaluation", "user study", "theoretical analysis", "empirical study"}
	case "social", "social_sciences":
		return []string{"survey", "qualitative study", "field experiment", "natural experiment", "longitudinal study", "comparative study"}
	case "climate", "environment":
		return []string{"simulation study", "observational study", "remote sensing analysis", "scenario modeling", "meta-analysis", "field experiment"}
	case "neuro", "neuroscience":
		return []string{"animal study", "neuroimaging study", "clinical study", "electrophysiology", "computational modeling", "behavioral study"}
	case "physics", "engineering":
		return []string{"experimental study", "simulation", "theoretical analysis", "case study", "prototype evaluation", "comparative benchmark"}
	case "biology", "life_sciences":
		return []string{"experimental study", "genomic study", "in vitro study", "in vivo study", "computational study", "systematic review"}
	case "humanities":
		return []string{"textual analysis", "archival research", "case study", "comparative analysis", "ethnography", "discourse analysis"}
	default:
		return []string{"empirical study", "review", "comparative analysis", "theoretical analysis", "case study", "meta-analysis"}
	}
}

func toTitlePhrase(value string) string {
	parts := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func clampScore(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func inferExpertiseLevel(score float64, domain string) string {
	switch {
	case score < 0.45:
		return "beginner"
	case score > 0.8 && strings.TrimSpace(domain) != "":
		return "expert"
	default:
		return "intermediate"
	}
}

func trimStrings(values []string, limit int) []string {
	values = uniqueStrings(values)
	if limit > 0 && len(values) > limit {
		return values[:limit]
	}
	return values
}
