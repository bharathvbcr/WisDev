package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

var runUnifiedResearchLoop = func(
	ctx context.Context,
	runtime *wisdev.UnifiedResearchRuntime,
	plane wisdev.ResearchExecutionPlane,
	req wisdev.LoopRequest,
	onEvent func(wisdev.PlanExecutionEvent),
) (*wisdev.LoopResult, error) {
	result, err := runtime.RunLoop(ctx, req, plane, onEvent)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return result.LoopResult, nil
}

func (s *wisdevServer) registerResearchRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	mux.HandleFunc("/wisdev/iterative-search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Queries           []string `json:"queries"`
			SessionID         string   `json:"sessionId"`
			MaxIterations     int      `json:"maxIterations"`
			CoverageThreshold float64  `json:"coverageThreshold"`
			TraceID           string   `json:"traceId,omitempty"`
			LegacyTraceID     string   `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		result, err := wisdev.IterativeResearch(r.Context(), req.Queries, req.SessionID, req.MaxIterations, req.CoverageThreshold)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "iterative research failed", map[string]any{
				"error": err.Error(),
			})
			return
		}
		traceID := resolveResearchRouteTraceID(r, req.TraceID, req.LegacyTraceID)
		payload := map[string]any{
			"result": result,
		}
		w.Header().Set("X-Trace-Id", traceID)
		traceID = writeEnvelopeWithTraceID(w, traceID, "iterativeSearch", payload)
		s.journalEvent("iterative_search", "/wisdev/iterative-search", traceID, req.SessionID, "", "", "", "Iterative search completed.", payload, nil)
	})

	mux.HandleFunc("/wisdev/research/deep", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query               string   `json:"query"`
			Categories          []string `json:"categories"`
			UserID              string   `json:"userId"`
			ProjectID           string   `json:"projectId"`
			IncludeDomains      []string `json:"include_domains"`
			IncludeDomainsCamel []string `json:"includeDomains"`
			DomainHint          string   `json:"domainHint"`
			SessionID           string   `json:"sessionId"`
			QualityMode         string   `json:"qualityMode,omitempty"`
			QualityModeSnake    string   `json:"quality_mode,omitempty"`
			MaxIterations       int      `json:"maxIterations,omitempty"`
			TraceID             string   `json:"traceId,omitempty"`
			LegacyTraceID       string   `json:"trace_id,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		userID, authErr := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if authErr != nil {
			logWisdevRouteError(r, "wisdev deep research authorization failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"project_id", strings.TrimSpace(req.ProjectID),
				"error", authErr,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, authErr.Error(), nil)
			return
		}

		includeDomains := req.IncludeDomains
		if len(includeDomains) == 0 {
			includeDomains = req.IncludeDomainsCamel
		}

		qualityMode := wisdev.NormalizeResearchQualityMode(firstNonEmpty(req.QualityMode, req.QualityModeSnake, "balanced"))
		profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(wisdev.WisDevModeGuided), qualityMode, true, req.MaxIterations)

		for _, d := range includeDomains {
			if strings.TrimSpace(d) == "" || d == "invalid" {
				WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "invalid domain list", nil)
				return
			}
		}

		papers := []wisdev.Source{}
		warnings := make([]string, 0, 1)

		domainHint := strings.TrimSpace(req.DomainHint)
		if domainHint == "" && len(includeDomains) > 0 {
			domainHint = strings.Join(includeDomains, ",")
		}
		traceID := resolveResearchRouteTraceID(r, req.TraceID, req.LegacyTraceID)
		slog.Info("wisdev deep research profile resolved",
			"component", "wisdev.research",
			"operation", "deep_research",
			"stage", "profile_resolved",
			"trace_id", traceID,
			"quality_mode", profile.QualityMode,
			"max_iterations", profile.MaxIterations,
			"max_search_terms", profile.SearchBudget.MaxSearchTerms,
			"hits_per_search", profile.SearchBudget.HitsPerSearch,
			"max_unique_papers", profile.SearchBudget.MaxUniquePapers,
		)

		var deepLoopResult *wisdev.LoopResult
		seedQueries := buildDeepResearchSeedQueries(query, req.Categories, domainHint)
		loopReq := wisdev.LoopRequest{
			Query:           query,
			SeedQueries:     seedQueries,
			Domain:          domainHint,
			ProjectID:       firstNonEmpty(req.SessionID, "deep_"+wisdev.NewTraceID()),
			MaxIterations:   profile.MaxIterations,
			MaxSearchTerms:  profile.SearchBudget.MaxSearchTerms,
			HitsPerSearch:   profile.SearchBudget.HitsPerSearch,
			MaxUniquePapers: profile.SearchBudget.MaxUniquePapers,
			AllocatedTokens: profile.AllocatedTokens,
			Mode:            string(profile.Mode),
		}
		runtime := resolveUnifiedResearchRuntime(agentGateway)
		if runtime == nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev unified runtime is required for deep research", map[string]any{
				"error": "wisdev_unified_runtime_unavailable",
			})
			return
		}
		traceEmitter := buildResearchLoopTraceEmitter(agentGateway, loopReq.ProjectID, userID, "deepResearch", wisdev.ResearchExecutionPlaneDeep, traceID, query)
		loopResult, loopErr := runUnifiedResearchLoop(r.Context(), runtime, wisdev.ResearchExecutionPlaneDeep, loopReq, traceEmitter)
		if loopErr != nil {
			writeWisdevResearchLoopError(w, "wisdev deep research loop failed", loopErr)
			return
		}
		if loopResult == nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev deep research loop returned no result", map[string]any{
				"error": "wisdev_unified_runtime_empty",
			})
			return
		}
		payload := buildDeepResearchLoopPayload(query, req.Categories, domainHint, loopResult)
		papers = searchPapersToWisdevSources(loopResult.Papers)
		deepLoopResult = loopResult
		payload["warnings"] = warnings
		deepMetadata := map[string]any{
			"backend":             "go-wisdev-deep",
			"executionPlane":      "go_canonical_runtime",
			"traceId":             traceID,
			"traceJournalEnabled": agentGateway != nil && agentGateway.Journal != nil,
			"qualityMode":         profile.QualityMode,
			"maxIterations":       profile.MaxIterations,
			"searchBudget": map[string]any{
				"maxSearchTerms":  profile.SearchBudget.MaxSearchTerms,
				"hitsPerSearch":   profile.SearchBudget.HitsPerSearch,
				"maxUniquePapers": profile.SearchBudget.MaxUniquePapers,
			},
		}
		attachResearchRuntimeMetadata(deepMetadata, agentGateway)
		enrichResearchMetadataWithRuntimeState(deepMetadata, deepLoopResult)
		payload["metadata"] = deepMetadata
		attachResearchEvidence(agentGateway, payload, "deep", req.SessionID, query, userID, papers)
		if agentGateway != nil && agentGateway.ResearchMemory != nil {
			_ = agentGateway.ResearchMemory.ConsolidateDossierPayload(r.Context(), userID, strings.TrimSpace(req.ProjectID), mapAny(payload["evidenceDossier"]), includeDomains)
		}
		w.Header().Set("X-Trace-Id", traceID)
		traceID = writeEnvelopeWithTraceID(w, traceID, "deepResearch", payload)
		s.journalEvent(
			"deep_research",
			"/wisdev/research/deep",
			traceID,
			req.SessionID,
			userID,
			"",
			"",
			"Deep research completed.",
			payload,
			map[string]any{
				"categories":       req.Categories,
				"includeDomains":   includeDomains,
				"warnings":         warnings,
				"serviceTier":      profile.ServiceTier,
				"primaryModel":     profile.PrimaryModelName,
				"primaryModelTier": profile.PrimaryModelTier,
				"allocatedTokens":  profile.AllocatedTokens,
			},
		)
	})

	mux.HandleFunc("/wisdev/research/autonomous", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query                    string   `json:"query"`
			UserID                   string   `json:"userId"`
			ProjectID                string   `json:"projectId"`
			SessionID                string   `json:"sessionId"`
			MaxIterations            int      `json:"maxIterations"`
			Mode                     string   `json:"mode"`
			EnableWisdevTools        *bool    `json:"enableWisdevTools"`
			AllowlistedTools         []string `json:"allowlistedTools"`
			RequireHumanConfirmation *bool    `json:"requireHumanConfirmation"`
			TraceID                  string   `json:"traceId,omitempty"`
			LegacyTraceID            string   `json:"trace_id,omitempty"`
			Plan                     struct {
				Queries     []string            `json:"queries"`
				CoverageMap map[string][]string `json:"coverageMap"`
			} `json:"plan"`
			Session struct {
				SessionID      string `json:"sessionId"`
				Query          string `json:"query"`
				OriginalQuery  string `json:"originalQuery"`
				CorrectedQuery string `json:"correctedQuery"`
				DetectedDomain string `json:"detectedDomain"`
				Mode           string `json:"mode"`
			} `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}

		query := strings.TrimSpace(req.Query)
		if query == "" {
			query = wisdev.ResolveSessionSearchQuery(req.Session.Query, req.Session.CorrectedQuery, req.Session.OriginalQuery)
		}
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		userID, authErr := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if authErr != nil {
			logWisdevRouteError(r, "wisdev autonomous research authorization failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"project_id", strings.TrimSpace(req.ProjectID),
				"session_id", strings.TrimSpace(req.SessionID),
				"error", authErr,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, authErr.Error(), nil)
			return
		}
		mode := resolveAutonomousExecutionMode(req.Mode, req.Session.Mode)
		policy := resolveAutonomousExecutionPolicy(
			agentGateway,
			string(mode),
			req.EnableWisdevTools,
			req.AllowlistedTools,
			req.RequireHumanConfirmation,
		)
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(req.Session.SessionID)
		}
		traceID := resolveResearchRouteTraceID(r, req.TraceID, req.LegacyTraceID)
		if sessionID != "" && agentGateway != nil && agentGateway.Store != nil {
			if loaded, err := agentGateway.Store.Get(r.Context(), sessionID); err == nil {
				if !requireOwnerAccess(w, r, loaded.UserID) {
					return
				}
			}
		}
		runtime := resolveUnifiedResearchRuntime(agentGateway)
		if runtime == nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev unified runtime is required for autonomous research", map[string]any{
				"error": "wisdev_unified_runtime_unavailable",
			})
			return
		}
		plannedQueries := normalizeResearchPlanQueries(append([]string{query}, req.Plan.Queries...))
		programmaticLoopMetadata := map[string]any(nil)
		allowProgrammaticPlanning, blockedProgrammaticAction, blockedProgrammaticReason := autonomousProgrammaticPlanningAllowed(agentGateway, policy)
		if !allowProgrammaticPlanning {
			slog.Info("skipping autonomous programmatic loop due to deep-agents policy",
				"action", blockedProgrammaticAction,
				"reason", blockedProgrammaticReason,
				"mode", policy.Mode,
			)
			programmaticLoopMetadata = skippedAutonomousProgrammaticLoopMetadata(blockedProgrammaticAction, blockedProgrammaticReason, policy)
		}
		qualityMode := "balanced"
		if mode == wisdev.WisDevModeYOLO {
			qualityMode = "quality"
		}
		profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(mode), qualityMode, false, req.MaxIterations)
		transportMetadata := map[string]any{
			"backend":                "go-wisdev-autonomous",
			"executionPlane":         "go_canonical_runtime",
			"providerParallelSearch": true,
			"fallbackTriggered":      false,
		}
		attachResearchRuntimeMetadata(transportMetadata, agentGateway)
		var canonicalLoopResult *wisdev.LoopResult

		var results map[string]any
		if results == nil {
			allowLoopHypothesisGeneration, _ := autonomousActionAllowed(
				agentGateway,
				policy,
				wisdev.ActionResearchProposeHypotheses,
			)
			loopReq := wisdev.LoopRequest{
				Query:                       query,
				SeedQueries:                 plannedQueries,
				Domain:                      strings.TrimSpace(req.Session.DetectedDomain),
				ProjectID:                   sessionID,
				MaxIterations:               profile.MaxIterations,
				MaxSearchTerms:              profile.SearchBudget.MaxSearchTerms,
				HitsPerSearch:               profile.SearchBudget.HitsPerSearch,
				MaxUniquePapers:             profile.SearchBudget.MaxUniquePapers,
				AllocatedTokens:             profile.AllocatedTokens,
				Mode:                        string(profile.Mode),
				DisableProgrammaticPlanning: !allowProgrammaticPlanning,
				DisableHypothesisGeneration: !allowLoopHypothesisGeneration,
			}
			var loopResult *wisdev.LoopResult
			var loopErr error
			traceEmitter := buildResearchLoopTraceEmitter(agentGateway, loopReq.ProjectID, userID, "autonomousResearch", wisdev.ResearchExecutionPlaneAutonomous, traceID, query)
			loopResult, loopErr = runUnifiedResearchLoop(r.Context(), runtime, wisdev.ResearchExecutionPlaneAutonomous, loopReq, traceEmitter)
			if loopErr == nil && loopResult != nil {
				canonicalLoopResult = loopResult
				transportMetadata["executionPlane"] = "go_canonical_runtime"
				transportMetadata["loopBacked"] = true
				transportMetadata["traceJournalEnabled"] = agentGateway != nil && agentGateway.Journal != nil
				executedQueries := normalizeResearchPlanQueries(loopResult.ExecutedQueries)
				hypothesisQueries := normalizeResearchPlanQueries(append([]string{query}, executedQueries...))
				hypothesisPayloads := buildAutonomousHypothesisPayloads(r.Context(), agentGateway, query, hypothesisQueries, loopResult, policy)
				results = buildAutonomousResearchLoopPayload(
					query,
					strings.TrimSpace(req.Session.DetectedDomain),
					loopResult,
					req.Plan.CoverageMap,
					runtime != nil,
				)
				if !allowLoopHypothesisGeneration {
					redactAutonomousReasoningGraphHypotheses(results)
				}
				results["primaryModel"] = map[string]any{
					"name": profile.PrimaryModelName,
					"tier": profile.PrimaryModelTier,
				}
				results["complexity"] = map[string]any{
					"score":           profile.ComplexityScore,
					"estimatedTokens": profile.EstimatedTokens,
				}
				results["warnings"] = []string{}
				if len(hypothesisPayloads) > 0 {
					results["hypotheses"] = hypothesisPayloads
				}
				if len(programmaticLoopMetadata) > 0 {
					results["programmaticLoop"] = programmaticLoopMetadata
				}
			} else if loopErr != nil {
				writeWisdevResearchLoopError(w, "wisdev autonomous research loop failed", loopErr)
				return
			} else {
				WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev autonomous research loop returned no result", map[string]any{
					"error": "wisdev_unified_runtime_empty",
				})
				return
			}
		}

		var dossierPapers []wisdev.Source
		if papersPayload, ok := results["papers"].([]map[string]any); ok {
			dossierPapers = make([]wisdev.Source, 0, len(papersPayload))
			for _, paper := range papersPayload {
				dossierPapers = append(dossierPapers, wisdev.Source{
					ID:          wisdev.AsOptionalString(paper["id"]),
					Title:       wisdev.AsOptionalString(paper["title"]),
					Summary:     wisdev.AsOptionalString(paper["abstract"]),
					Link:        wisdev.AsOptionalString(paper["link"]),
					DOI:         wisdev.AsOptionalString(paper["doi"]),
					Publication: wisdev.AsOptionalString(paper["publication"]),
					Source:      wisdev.AsOptionalString(paper["source"]),
				})
			}
		}
		if len(dossierPapers) == 0 {
			if papersValue, ok := results["papers"].([]wisdev.Source); ok {
				dossierPapers = papersValue
			}
		}
		attachResearchEvidence(agentGateway, results, "auto", sessionID, query, userID, dossierPapers)
		if agentGateway != nil && agentGateway.ResearchMemory != nil {
			preferredSources := make([]string, 0, len(dossierPapers))
			for _, paper := range dossierPapers {
				preferredSources = append(preferredSources, firstNonEmpty(paper.Source, paper.Publication))
			}
			_ = agentGateway.ResearchMemory.ConsolidateDossierPayload(r.Context(), userID, strings.TrimSpace(req.ProjectID), mapAny(results["evidenceDossier"]), preferredSources)
		}
		enrichResearchMetadataWithRuntimeState(transportMetadata, canonicalLoopResult)
		transportMetadata["traceId"] = traceID
		results["metadata"] = transportMetadata
		w.Header().Set("X-Trace-Id", traceID)
		traceID = writeEnvelopeWithTraceID(w, traceID, "autonomousResearch", results)
		s.journalEvent("autonomous_research", "/wisdev/research/autonomous", traceID, sessionID, userID, "", "", "Autonomous research completed.", results, map[string]any{
			"primaryModel":     profile.PrimaryModelName,
			"primaryModelTier": profile.PrimaryModelTier,
			"serviceTier":      profile.ServiceTier,
			"allocatedTokens":  profile.AllocatedTokens,
			"complexityScore":  profile.ComplexityScore,
			"policyMode":       policy.Mode,
			"toolsEnabled":     policy.EnableWisdevTools,
			"allowlistedCount": len(policy.AllowlistedTools),
			"requireConfirm":   policy.RequireHumanConfirmation,
		})
	})
}

func resolveResearchRouteTraceID(r *http.Request, traceID string, legacyTraceID string) string {
	if requested := strings.TrimSpace(traceID); requested != "" {
		return resolveWisdevRouteTraceID(r, requested)
	}
	return resolveWisdevRouteTraceID(r, strings.TrimSpace(legacyTraceID))
}

func attachResearchRuntimeMetadata(metadata map[string]any, agentGateway *wisdev.AgentGateway) {
	if metadata == nil || agentGateway == nil {
		return
	}
	runtimeMetadata := agentGateway.RuntimeMetadata()
	if len(runtimeMetadata) == 0 {
		return
	}
	metadata["adkRunnerReady"] = boolValue(runtimeMetadata["runnerReady"])
	if subAgents := normalizeStringSlice(runtimeMetadata["subAgents"]); len(subAgents) > 0 {
		metadata["configuredSubAgents"] = subAgents
	}
}

func redactAutonomousReasoningGraphHypotheses(payload map[string]any) {
	if payload == nil {
		return
	}
	graph, ok := payload["reasoningGraph"].(*wisdev.ReasoningGraph)
	if !ok || graph == nil || len(graph.Nodes) == 0 {
		return
	}
	filtered := *graph
	filtered.Nodes = make([]wisdev.ReasoningNode, 0, len(graph.Nodes))
	removed := make(map[string]struct{})
	for _, node := range graph.Nodes {
		if node.Type == wisdev.ReasoningNodeHypothesis {
			if strings.TrimSpace(node.ID) != "" {
				removed[node.ID] = struct{}{}
			}
			continue
		}
		filtered.Nodes = append(filtered.Nodes, node)
	}
	if len(removed) == 0 {
		return
	}
	if len(graph.Edges) > 0 {
		filtered.Edges = make([]wisdev.ReasoningEdge, 0, len(graph.Edges))
		for _, edge := range graph.Edges {
			if _, ok := removed[edge.From]; ok {
				continue
			}
			if _, ok := removed[edge.To]; ok {
				continue
			}
			filtered.Edges = append(filtered.Edges, edge)
		}
	}
	payload["reasoningGraph"] = &filtered
}

func searchPapersToWisdevSources(papers []search.Paper) []wisdev.Source {
	if len(papers) == 0 {
		return []wisdev.Source{}
	}
	out := make([]wisdev.Source, 0, len(papers))
	for _, paper := range papers {
		out = append(out, wisdev.Source{
			ID:            paper.ID,
			Title:         paper.Title,
			Summary:       paper.Abstract,
			Link:          paper.Link,
			DOI:           paper.DOI,
			Source:        paper.Source,
			SourceApis:    append([]string(nil), paper.SourceApis...),
			Authors:       append([]string(nil), paper.Authors...),
			Year:          paper.Year,
			Publication:   paper.Venue,
			Keywords:      append([]string(nil), paper.Keywords...),
			Score:         paper.Score,
			CitationCount: paper.CitationCount,
		})
	}
	return out
}

func sessionIDFromAutonomousRequest(explicitSessionID string, nestedSessionID string) string {
	sessionID := strings.TrimSpace(explicitSessionID)
	if sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(nestedSessionID)
}

func buildDeepResearchSeedQueries(query string, categories []string, domainHint string) []string {
	seeds := make([]string, 0, len(categories)+2)
	baseQuery := strings.TrimSpace(query)
	for _, category := range normalizeDeepResearchCategories(categories, domainHint) {
		trimmedCategory := strings.TrimSpace(category)
		if trimmedCategory == "" {
			continue
		}
		if baseQuery != "" {
			seeds = append(seeds, baseQuery+" "+trimmedCategory)
			continue
		}
		seeds = append(seeds, trimmedCategory)
	}
	if trimmedDomain := strings.TrimSpace(domainHint); trimmedDomain != "" && !strings.Contains(strings.ToLower(baseQuery), strings.ToLower(trimmedDomain)) {
		seeds = append(seeds, strings.TrimSpace(baseQuery+" "+trimmedDomain))
	}
	return normalizeResearchPlanQueries(seeds)
}

func enhanceAutonomousPlannedQueries(
	ctx context.Context,
	agentGateway *wisdev.AgentGateway,
	session *wisdev.AgentSession,
	query string,
	domain string,
	mode string,
	plannedQueries []string,
	policy wisdev.DeepAgentsExecutionPolicy,
) ([]string, map[string]any) {
	if agentGateway == nil {
		return plannedQueries, nil
	}
	execFn := agentGateway.ProgrammaticLoopExecutor()
	if execFn == nil {
		return plannedQueries, nil
	}
	allowed, reason := autonomousActionAllowed(agentGateway, policy, wisdev.ActionResearchQueryDecompose)
	if !allowed {
		slog.Info("skipping autonomous programmatic loop due to deep-agents policy",
			"action", wisdev.ActionResearchQueryDecompose,
			"reason", reason,
			"mode", policy.Mode,
		)
		return plannedQueries, skippedAutonomousProgrammaticLoopMetadata(wisdev.ActionResearchQueryDecompose, reason, policy)
	}
	allowed, reason = autonomousActionAllowed(agentGateway, policy, wisdev.ActionResearchGenerateThoughts)
	if !allowed {
		slog.Info("skipping autonomous programmatic loop due to deep-agents policy",
			"action", wisdev.ActionResearchGenerateThoughts,
			"reason", reason,
			"mode", policy.Mode,
		)
		return plannedQueries, skippedAutonomousProgrammaticLoopMetadata(wisdev.ActionResearchGenerateThoughts, reason, policy)
	}

	payload := map[string]any{
		"query": query,
	}
	if strings.TrimSpace(domain) != "" {
		payload["domain"] = strings.TrimSpace(domain)
	}
	if strings.TrimSpace(mode) != "" {
		payload["mode"] = strings.TrimSpace(mode)
	}

	tree := wisdev.RunProgrammaticTreeLoop(
		ctx,
		execFn,
		session,
		wisdev.ActionResearchQueryDecompose,
		payload,
		2,
		nil,
	)
	loopQueries := extractAutonomousProgrammaticQueries(tree.Final)
	if len(loopQueries) == 0 {
		for _, iteration := range tree.Iterations {
			if len(loopQueries) > 0 {
				break
			}
			loopQueries = extractAutonomousProgrammaticQueries(iteration.Output)
		}
	}

	enhancedQueries := normalizeResearchPlanQueries(append(append([]string{}, plannedQueries...), loopQueries...))
	metadata := map[string]any{
		"action":               wisdev.ActionResearchQueryDecompose,
		"completed":            tree.Completed,
		"bestConfidence":       tree.BestConfidence,
		"additionalQueryCount": len(loopQueries),
		"executionPlane":       "go_programmatic_loop",
	}
	attachResearchRuntimeMetadata(metadata, agentGateway)
	if len(loopQueries) > 0 {
		metadata["additionalQueries"] = loopQueries
	}
	return enhancedQueries, metadata
}

func autonomousProgrammaticPlanningAllowed(
	agentGateway *wisdev.AgentGateway,
	policy wisdev.DeepAgentsExecutionPolicy,
) (bool, string, string) {
	allowed, reason := autonomousActionAllowed(agentGateway, policy, wisdev.ActionResearchQueryDecompose)
	if !allowed {
		return false, wisdev.ActionResearchQueryDecompose, reason
	}
	allowed, reason = autonomousActionAllowed(agentGateway, policy, wisdev.ActionResearchGenerateThoughts)
	if !allowed {
		return false, wisdev.ActionResearchGenerateThoughts, reason
	}
	return true, "", ""
}

func skippedAutonomousProgrammaticLoopMetadata(action string, reason string, policy wisdev.DeepAgentsExecutionPolicy) map[string]any {
	return map[string]any{
		"action":     action,
		"completed":  false,
		"skipped":    true,
		"skipReason": reason,
		"policyMode": policy.Mode,
	}
}

func extractAutonomousProgrammaticQueries(result map[string]any) []string {
	if len(result) == 0 {
		return nil
	}

	queries := make([]string, 0)
	appendQuery := func(candidate string) {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			return
		}
		queries = append(queries, trimmed)
	}

	switch tasks := result["tasks"].(type) {
	case []wisdev.ResearchTask:
		for _, task := range tasks {
			appendQuery(task.Name)
		}
	case []map[string]any:
		for _, task := range tasks {
			appendQuery(firstNonEmptyString(
				wisdev.AsOptionalString(task["name"]),
				wisdev.AsOptionalString(task["label"]),
				wisdev.AsOptionalString(task["query"]),
			))
		}
	case []any:
		for _, rawTask := range tasks {
			task := mapAny(rawTask)
			if len(task) == 0 {
				continue
			}
			appendQuery(firstNonEmptyString(
				wisdev.AsOptionalString(task["name"]),
				wisdev.AsOptionalString(task["label"]),
				wisdev.AsOptionalString(task["query"]),
			))
		}
	}

	return normalizeResearchPlanQueries(queries)
}
