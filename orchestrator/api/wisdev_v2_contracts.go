package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

func (s *wisdevV2Server) registerContractRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/v2/wisdev/plan/revision", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID   string         `json:"sessionId"`
			UserID      string         `json:"userId"`
			StepID      string         `json:"stepId"`
			Reason      string         `json:"reason"`
			Query       string         `json:"query"`
			Context     map[string]any `json:"context"`
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
		traceID := writeV2Envelope(w, "planRevision", payload)
		s.journalEvent("plan_revision", "/v2/wisdev/plan/revision", traceID, req.SessionID, userID, "", strings.TrimSpace(req.StepID), "WisDev plan revision requested.", payload, map[string]any{"query": strings.TrimSpace(req.Query)})
	})

	mux.HandleFunc("/v2/wisdev/subtopics/generate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query    string `json:"query"`
			Domain   string `json:"domain"`
			Limit    int    `json:"limit"`
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
		subtopics, keywords, variations, source := buildSubtopicsResponse(r.Context(), agentGateway, req.Query, req.Domain, req.Limit)
		payload := map[string]any{
			"subtopics":       subtopics,
			"keywords":        keywords,
			"queryVariations": variations,
			"source":          source,
			"coverageHint":    fmt.Sprintf("Generated %d scoped subtopics from the current research objective.", len(subtopics)),
		}
		traceID := writeV2Envelope(w, "subtopics", payload)
		s.journalEvent("subtopics_generate", "/v2/wisdev/subtopics/generate", traceID, req.SessionID, "", "", "", "Generated WisDev subtopics.", payload, map[string]any{"domain": strings.TrimSpace(req.Domain)})
	})

	mux.HandleFunc("/v2/wisdev/study-types/generate", func(w http.ResponseWriter, r *http.Request) {
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
		studyTypes, signals, source := buildStudyTypesResponse(r.Context(), agentGateway, req.Query, req.Domain, req.Subtopics, req.Limit)
		payload := map[string]any{
			"studyTypes":    studyTypes,
			"source":        source,
			"matchedSignals": signals,
			"coverageHint":  "Study types selected to widen methodological coverage while preserving query relevance.",
		}
		traceID := writeV2Envelope(w, "studyTypes", payload)
		s.journalEvent("study_types_generate", "/v2/wisdev/study-types/generate", traceID, req.SessionID, "", "", "", "Generated WisDev study types.", payload, map[string]any{"domain": strings.TrimSpace(req.Domain)})
	})

	mux.HandleFunc("/v2/wisdev/research-path/evaluate", func(w http.ResponseWriter, r *http.Request) {
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
		traceID := writeV2Envelope(w, "researchPath", payload)
		s.journalEvent("research_path_evaluate", "/v2/wisdev/research-path/evaluate", traceID, req.SessionID, "", "", "", "Evaluated WisDev research path.", payload, nil)
	})

	mux.HandleFunc("/v2/wisdev/search-coverage/evaluate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			Query          string         `json:"query"`
			Queries        []string       `json:"queries"`
			Results        []map[string]any `json:"results"`
			Papers         []map[string]any `json:"papers"`
			TargetCoverage float64        `json:"targetCoverage"`
			SessionID      string         `json:"sessionId"`
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
			"coverage":          coverage,
			"coverageScore":     coverage,
			"score":             coverage,
			"coverageStatus":    status,
			"uniqueQueryCount":  len(uniqueStrings(req.Queries)),
			"resultCount":       resultCount,
			"missingTerms":      missingTerms,
			"gaps":              missingTerms,
			"recommendedQueries": recommendedQueries,
			"source":             source,
			"supportedSignals": []string{
				"query_diversity",
				"result_volume",
				"term_coverage",
			},
		}
		traceID := writeV2Envelope(w, "searchCoverage", payload)
		s.journalEvent("search_coverage_evaluate", "/v2/wisdev/search-coverage/evaluate", traceID, req.SessionID, "", "", "", "Evaluated WisDev search coverage.", payload, nil)
	})

	mux.HandleFunc("/v2/wisdev/follow-up/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{"allowedMethod": http.MethodPost})
			return
		}
		var req struct {
			UserID         string   `json:"userId"`
			SessionID      string   `json:"sessionId"`
			Query          string   `json:"query"`
			Domain         string   `json:"domain"`
			CoverageScore  float64  `json:"coverageScore"`
			TargetCoverage float64  `json:"targetCoverage"`
			Subtopics      []string `json:"subtopics"`
			StudyTypes     []string `json:"studyTypes"`
			MissingTerms   []string `json:"missingTerms"`
			PathScore      float64  `json:"pathScore"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		target := req.TargetCoverage
		if target <= 0 {
			target = 0.7
		}
		needsFollowUp, followUpQuestion, source := buildFollowUpDecision(r.Context(), agentGateway, req.Query, req.Domain, req.CoverageScore, req.PathScore, target, req.MissingTerms, req.Subtopics, req.StudyTypes)
		payload := map[string]any{
			"needsFollowUp":     needsFollowUp,
			"required":          needsFollowUp,
			"reason":            followUpReason(req.CoverageScore, req.PathScore, req.MissingTerms),
			"suggestedNextStep": buildRecommendedNextStep(req.Subtopics, req.StudyTypes, req.PathScore, target),
			"signals":           buildFollowUpSignals(req.CoverageScore, req.PathScore, req.MissingTerms),
			"source":            source,
		}
		if needsFollowUp {
			payload["followUpQuestion"] = followUpQuestion
		} else {
			payload["followUpQuestion"] = nil
		}
		traceID := writeV2Envelope(w, "followUp", payload)
		s.journalEvent("follow_up_check", "/v2/wisdev/follow-up/check", traceID, req.SessionID, strings.TrimSpace(req.UserID), "", "", "Checked WisDev follow-up requirement.", payload, nil)
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
	subtopics := make([]string, 0, limit)
	for _, keyword := range keywords {
		if len(keyword) < 5 {
			continue
		}
		subtopics = append(subtopics, toTitlePhrase(keyword))
		if len(subtopics) >= limit {
			break
		}
	}
	if len(subtopics) == 0 {
		subtopics = append(subtopics, defaultDomainSubtopics(domain)...)
	}
	subtopics = uniqueStrings(append(subtopics, defaultDomainSubtopics(domain)...))
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

func buildSubtopicsResponse(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, limit int) ([]string, []string, []string, string) {
	if limit <= 0 {
		limit = 6
	}
	if agentGateway != nil && agentGateway.LLMClient != nil {
		prompt := fmt.Sprintf(`You are WisDev, ScholarLM's academic research planner.
Query: %q
Domain: %q
Return concise research subtopics, retrieval keywords, and query variations.
Limit each list to at most %d items.
Return JSON only.`, query, domain, limit)
		schema := fmt.Sprintf(`{"type":"object","properties":{"subtopics":{"type":"array","items":{"type":"string"},"maxItems":%d},"keywords":{"type":"array","items":{"type":"string"},"maxItems":%d},"queryVariations":{"type":"array","items":{"type":"string"},"maxItems":%d}},"required":["subtopics","keywords","queryVariations"]}`, limit, limit, limit)
		resp, err := agentGateway.LLMClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveLightModel(),
			JsonSchema: schema,
		})
		if err == nil {
			var parsed struct {
				Subtopics       []string `json:"subtopics"`
				Keywords        []string `json:"keywords"`
				QueryVariations []string `json:"queryVariations"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				subtopics := uniqueStrings(parsed.Subtopics)
				if len(subtopics) > 0 {
					return trimStrings(subtopics, limit), trimStrings(uniqueStrings(parsed.Keywords), limit), trimStrings(uniqueStrings(parsed.QueryVariations), limit), "llm_structured"
				}
			}
		}
	}
	subtopics, keywords, variations := deriveSubtopics(query, domain, limit)
	return subtopics, keywords, variations, "heuristic_fallback"
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

func buildStudyTypesResponse(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, subtopics []string, limit int) ([]string, []string, string) {
	if limit <= 0 {
		limit = 5
	}
	if agentGateway != nil && agentGateway.LLMClient != nil {
		prompt := fmt.Sprintf(`You are WisDev, ScholarLM's methods advisor.
Query: %q
Domain: %q
Subtopics: %s
Return the best study types to cover this research space and the signals that justify them.
Return JSON only.`, query, domain, strings.Join(subtopics, ", "))
		schema := fmt.Sprintf(`{"type":"object","properties":{"studyTypes":{"type":"array","items":{"type":"string"},"maxItems":%d},"matchedSignals":{"type":"array","items":{"type":"string"},"maxItems":%d}},"required":["studyTypes","matchedSignals"]}`, limit, limit)
		resp, err := agentGateway.LLMClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveLightModel(),
			JsonSchema: schema,
		})
		if err == nil {
			var parsed struct {
				StudyTypes     []string `json:"studyTypes"`
				MatchedSignals []string `json:"matchedSignals"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				studyTypes := uniqueStrings(parsed.StudyTypes)
				if len(studyTypes) > 0 {
					return trimStrings(studyTypes, limit), trimStrings(uniqueStrings(parsed.MatchedSignals), limit), "llm_structured"
				}
			}
		}
	}
	studyTypes, signals := deriveStudyTypes(query, domain, subtopics, limit)
	return studyTypes, signals, "heuristic_fallback"
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
		prompt := fmt.Sprintf(`You are WisDev, ScholarLM's research architect.
Query: %q
Domain: %q
Subtopics: %s
Study types: %s
Target coverage: %.2f
Assess whether the current research path is ready. Return JSON only.`, query, domain, strings.Join(subtopics, ", "), strings.Join(studyTypes, ", "), target)
		resp, err := agentGateway.LLMClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt: prompt,
			Model:  llm.ResolveBalancedModel(),
			JsonSchema: `{"type":"object","properties":{"pathScore":{"type":"number"},"reasoning":{"type":"string"}},"required":["pathScore","reasoning"]}`,
		})
		if err == nil {
			var parsed struct {
				PathScore float64 `json:"pathScore"`
				Reasoning string  `json:"reasoning"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				return clampScore(parsed.PathScore), strings.TrimSpace(parsed.Reasoning), "llm_structured"
			}
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
	candidates, _, _ := deriveSubtopics(query, domain, 6)
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
		if len(recommended) >= 3 {
			break
		}
	}
	return recommended
}

func recommendAdditionalStudyTypes(query string, domain string, selected []string) []string {
	candidates, _ := deriveStudyTypes(query, domain, nil, 6)
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
		if len(recommended) >= 3 {
			break
		}
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
		prompt := fmt.Sprintf(`You are WisDev, ScholarLM's search quality evaluator.
Primary query: %q
Executed queries: %s
Result count: %d
Missing terms detected heuristically: %s
Return JSON only with an improved coverageScore, missingTerms, and recommendedQueries.`, query, strings.Join(uniqueStrings(queries), " | "), wisdev.MaxInt(len(results), len(papers)), strings.Join(missingTerms, ", "))
		resp, err := agentGateway.LLMClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt: prompt,
			Model:  llm.ResolveLightModel(),
			JsonSchema: `{"type":"object","properties":{"coverageScore":{"type":"number"},"missingTerms":{"type":"array","items":{"type":"string"}},"recommendedQueries":{"type":"array","items":{"type":"string"}}},"required":["coverageScore","missingTerms","recommendedQueries"]}`,
		})
		if err == nil {
			var parsed struct {
				CoverageScore     float64  `json:"coverageScore"`
				MissingTerms      []string `json:"missingTerms"`
				RecommendedQueries []string `json:"recommendedQueries"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				return clampScore(parsed.CoverageScore), uniqueStrings(parsed.MissingTerms), uniqueStrings(parsed.RecommendedQueries), "llm_structured"
			}
		}
	}
	return coverage, missingTerms, recommendedQueries, "heuristic_fallback"
}

func followUpReason(coverageScore float64, pathScore float64, missingTerms []string) string {
	switch {
	case len(missingTerms) > 0:
		return "Important query terms are not yet covered by the current evidence set."
	case coverageScore < 0.7:
		return "Evidence coverage is below the target threshold."
	case pathScore < 0.7:
		return "The research path still needs refinement before execution."
	default:
		return "No follow-up is required."
	}
}

func buildFollowUpSignals(coverageScore float64, pathScore float64, missingTerms []string) []string {
	signals := []string{}
	if coverageScore < 0.7 {
		signals = append(signals, "low_coverage")
	}
	if pathScore < 0.7 {
		signals = append(signals, "weak_path")
	}
	if len(missingTerms) > 0 {
		signals = append(signals, "missing_terms")
	}
	if len(signals) == 0 {
		signals = append(signals, "ready_to_proceed")
	}
	return signals
}

func buildFollowUpQuestion(query string, domain string, missingTerms []string, subtopics []string, studyTypes []string) map[string]any {
	options := []map[string]any{}
	for _, term := range missingTerms {
		options = append(options, map[string]any{
			"value": term,
			"label": toTitlePhrase(term),
		})
		if len(options) >= 4 {
			break
		}
	}
	if len(options) == 0 {
		for _, item := range recommendAdditionalSubtopics(query, domain, subtopics) {
			options = append(options, map[string]any{
				"value": strings.ToLower(strings.ReplaceAll(item, " ", "_")),
				"label": item,
			})
			if len(options) >= 4 {
				break
			}
		}
	}
	if len(options) == 0 {
		for _, item := range recommendAdditionalStudyTypes(query, domain, studyTypes) {
			options = append(options, map[string]any{
				"value": strings.ToLower(strings.ReplaceAll(item, " ", "_")),
				"label": toTitlePhrase(item),
			})
			if len(options) >= 4 {
				break
			}
		}
	}
	return map[string]any{
		"id":            "follow_up_refinement",
		"type":          "clarification",
		"question":      "Which missing area should WisDev expand next?",
		"isMultiSelect": true,
		"isRequired":    true,
		"options":       options,
		"helpText":      "Choose the gap that matters most so the next search iteration stays focused.",
	}
}

func buildFollowUpDecision(ctx context.Context, agentGateway *wisdev.AgentGateway, query string, domain string, coverageScore float64, pathScore float64, target float64, missingTerms []string, subtopics []string, studyTypes []string) (bool, map[string]any, string) {
	needsFollowUp := coverageScore < target || pathScore < target || len(missingTerms) > 0 || len(subtopics) < 2
	fallbackQuestion := buildFollowUpQuestion(query, domain, missingTerms, subtopics, studyTypes)
	if agentGateway != nil && agentGateway.LLMClient != nil {
		prompt := fmt.Sprintf(`You are WisDev, ScholarLM's interactive research guide.
Query: %q
Domain: %q
Coverage score: %.2f
Path score: %.2f
Missing terms: %s
Return JSON only with whether follow-up is needed and a single best follow-up question object.`, query, domain, coverageScore, pathScore, strings.Join(missingTerms, ", "))
		resp, err := agentGateway.LLMClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
			Prompt: prompt,
			Model:  llm.ResolveBalancedModel(),
			JsonSchema: `{"type":"object","properties":{"needsFollowUp":{"type":"boolean"},"followUpQuestion":{"type":["object","null"]}},"required":["needsFollowUp","followUpQuestion"]}`,
		})
		if err == nil {
			var parsed struct {
				NeedsFollowUp  bool           `json:"needsFollowUp"`
				FollowUpQuestion map[string]any `json:"followUpQuestion"`
			}
			if json.Unmarshal([]byte(resp.JsonResult), &parsed) == nil {
				if parsed.NeedsFollowUp && len(parsed.FollowUpQuestion) > 0 {
					return true, parsed.FollowUpQuestion, "llm_structured"
				}
				return parsed.NeedsFollowUp, nil, "llm_structured"
			}
		}
	}
	if needsFollowUp {
		return true, fallbackQuestion, "heuristic_fallback"
	}
	return false, nil, "heuristic_fallback"
}

func tokenizeResearchText(value string) []string {
	replacer := strings.NewReplacer(",", " ", ".", " ", ":", " ", ";", " ", "/", " ", "-", " ", "(", " ", ")", " ", "\"", " ", "'", " ")
	text := strings.ToLower(replacer.Replace(value))
	parts := strings.Fields(text)
	stopwords := map[string]struct{}{
		"about": {}, "after": {}, "among": {}, "and": {}, "are": {}, "for": {}, "from": {}, "into": {}, "that": {}, "the": {}, "their": {}, "them": {}, "these": {}, "this": {}, "using": {}, "with": {},
	}
	tokens := []string{}
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		if _, blocked := stopwords[part]; blocked {
			continue
		}
		tokens = append(tokens, part)
	}
	return uniqueStrings(tokens)
}

func defaultDomainSubtopics(domain string) []string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "healthcare":
		return []string{"Clinical Outcomes", "Patient Selection", "Safety Signals"}
	case "cs", "computer_science", "ai":
		return []string{"Benchmarks", "Training Data", "Evaluation"}
	default:
		return []string{"Methods", "Limitations", "Applications"}
	}
}

func defaultStudyTypes(domain string) []string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "medicine", "healthcare":
		return []string{"randomized trial", "systematic review", "observational study"}
	case "cs", "computer_science", "ai":
		return []string{"benchmark", "ablation study", "system evaluation"}
	default:
		return []string{"empirical study", "review", "comparative analysis"}
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
