package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// researchLoopRequestTimeout bounds the synchronous research routes
// (/wisdev/research/deep and /wisdev/research/autonomous) so a stalled loop
// cannot hold the HTTP request open indefinitely. Override with
// WISDEV_RESEARCH_LOOP_TIMEOUT_SECONDS (60–3600).
var researchLoopRequestTimeout = resolveResearchLoopRequestTimeout()

func resolveResearchLoopRequestTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("WISDEV_RESEARCH_LOOP_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 60 && seconds <= 3600 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 10 * time.Minute
}

var runUnifiedResearchLoop = func(
	ctx context.Context,
	runtime *wisdev.UnifiedResearchRuntime,
	plane wisdev.ResearchExecutionPlane,
	req wisdev.LoopRequest,
	onEvent func(wisdev.PlanExecutionEvent),
) (*wisdev.LoopResult, error) {
	if researchLoopRequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, researchLoopRequestTimeout)
		defer cancel()
	}
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
		traceID := resolveWisdevRouteTraceID(r, req.TraceID)
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
			Mode                string   `json:"mode,omitempty"`
			ExecutionMode       string   `json:"executionMode,omitempty"`
			ExecutionModeSnake  string   `json:"execution_mode,omitempty"`
			ServiceTier         string   `json:"serviceTier,omitempty"`
			IncludeDomains      []string `json:"include_domains"`
			IncludeDomainsCamel []string `json:"includeDomains"`
			DomainHint          string   `json:"domainHint"`
			SessionID           string   `json:"sessionId"`
			QualityMode         string   `json:"qualityMode,omitempty"`
			QualityModeSnake    string   `json:"quality_mode,omitempty"`
			MaxIterations       int      `json:"maxIterations,omitempty"`
			TraceID             string   `json:"traceId,omitempty"`
			Session             struct {
				SessionID   string `json:"sessionId"`
				Mode        string `json:"mode"`
				ServiceTier string `json:"serviceTier"`
			} `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		rawQuery := strings.TrimSpace(req.Query)
		if rawQuery == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		var llmClient *llm.Client
		if agentGateway != nil {
			llmClient = agentGateway.LLMClient
		}
		originalQuery, query, _ := wisdev.PrepareJobResearchQuery(r.Context(), rawQuery, strings.TrimSpace(req.DomainHint), llmClient, false)
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

		mode := resolveDeepResearchExecutionMode(req.Mode, req.ExecutionMode, req.ExecutionModeSnake, req.Session.Mode)
		qualityMode := wisdev.NormalizeResearchQualityMode(firstNonEmpty(req.QualityMode, req.QualityModeSnake, "balanced"))
		profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(mode), qualityMode, true, req.MaxIterations)
		serviceTier := profile.ServiceTier
		if requested := wisdev.NormalizeServiceTier(firstNonEmpty(req.ServiceTier, req.Session.ServiceTier)); requested != "" {
			serviceTier = requested
		}

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
		traceID := resolveWisdevRouteTraceID(r, req.TraceID)
		slog.Info("wisdev deep research profile resolved",
			"component", "wisdev.research",
			"operation", "deep_research",
			"stage", "profile_resolved",
			"trace_id", traceID,
			"session_id", firstNonEmpty(strings.TrimSpace(req.SessionID), strings.TrimSpace(req.Session.SessionID), strings.TrimSpace(req.ProjectID)),
			"mode", string(mode),
			"execution_mode", string(mode),
			"research_plane", string(wisdev.ResearchExecutionPlaneDeep),
			"execution_plane", "go_canonical_runtime",
			"quality_mode", profile.QualityMode,
			"service_tier", serviceTier,
			"max_iterations", profile.MaxIterations,
			"max_search_terms", profile.SearchBudget.MaxSearchTerms,
			"hits_per_search", profile.SearchBudget.HitsPerSearch,
			"max_unique_papers", profile.SearchBudget.MaxUniquePapers,
		)

		var deepLoopResult *wisdev.LoopResult
		seedQueries := buildDeepResearchSeedQueries(query, req.Categories, domainHint)
		loopReq := wisdev.LoopRequest{
			Query:           query,
			OriginalQuery:   originalQuery,
			SeedQueries:     seedQueries,
			Domain:          domainHint,
			ProjectID:       firstNonEmpty(req.SessionID, "deep_"+wisdev.NewTraceID()),
			MaxIterations:   profile.MaxIterations,
			MaxSearchTerms:  profile.SearchBudget.MaxSearchTerms,
			HitsPerSearch:   profile.SearchBudget.HitsPerSearch,
			MaxUniquePapers: profile.SearchBudget.MaxUniquePapers,
			AllocatedTokens: profile.AllocatedTokens,
			Mode:            string(profile.Mode),
			ServiceTier:     serviceTier,
			TraceID:         traceID,
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
		searchTrace, _ := payload["searchRunTrace"].(map[string]any)
		slog.Info("wisdev deep research loop completed",
			append([]any{
				"component", "wisdev.research",
				"operation", "deep_research",
				"stage", "loop_completed",
				"trace_id", traceID,
				"session_id", firstNonEmpty(strings.TrimSpace(req.SessionID), strings.TrimSpace(req.Session.SessionID), strings.TrimSpace(req.ProjectID)),
				"domain_hint", domainHint,
				"quality_mode", profile.QualityMode,
				"result", "success",
			}, append(deepResearchSearchRunTraceLogAttrs(searchTrace), deepResearchPayloadOrganizationLogAttrs(payload)...)...)...,
		)
		papers = searchPapersToWisdevSources(loopResult.Papers)
		deepLoopResult = loopResult
		payload["warnings"] = warnings
		deepMetadata := map[string]any{
			"backend":             "go-wisdev-deep",
			"executionPlane":      "go_canonical_runtime",
			"traceId":             traceID,
			"traceJournalEnabled": agentGateway != nil && agentGateway.Journal != nil,
			"qualityMode":         profile.QualityMode,
			"serviceTier":         serviceTier,
			"mode":                string(mode),
			"executionMode":       string(mode),
			"researchPlane":       string(wisdev.ResearchExecutionPlaneDeep),
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
				"mode":             string(mode),
				"executionMode":    string(mode),
				"researchPlane":    string(wisdev.ResearchExecutionPlaneDeep),
				"serviceTier":      serviceTier,
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
			ServiceTier              string   `json:"serviceTier,omitempty"`
			EnableWisdevTools        *bool    `json:"enableWisdevTools"`
			AllowlistedTools         []string `json:"allowlistedTools"`
			RequireHumanConfirmation *bool    `json:"requireHumanConfirmation"`
			TraceID                  string   `json:"traceId,omitempty"`
			TraceIDLegacy            string   `json:"trace_id,omitempty"`
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
				ServiceTier    string `json:"serviceTier"`
			} `json:"session"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}

		originalQuery := wisdev.ResolveSessionQueryText("", wisdev.AsOptionalString(req.Session.OriginalQuery))
		if originalQuery == "" {
			originalQuery = strings.TrimSpace(req.Query)
		}
		if originalQuery == "" {
			originalQuery = wisdev.ResolveSessionSearchQuery(req.Session.Query, req.Session.CorrectedQuery, req.Session.OriginalQuery)
		}
		if originalQuery == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", nil)
			return
		}
		var llmClient *llm.Client
		if agentGateway != nil {
			llmClient = agentGateway.LLMClient
		}
		originalQuery, query, _ := wisdev.PrepareJobResearchQuery(r.Context(), originalQuery, strings.TrimSpace(req.Session.DetectedDomain), llmClient, false)
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
		traceID := resolveWisdevRouteTraceID(r, req.TraceID, req.TraceIDLegacy)
		slog.Info("wisdev autonomous research request validated",
			"component", "wisdev.research",
			"operation", "autonomous_research",
			"stage", "request_validated",
			"trace_id", traceID,
			"session_id", sessionID,
			"user_present", userID != "",
			"mode", string(mode),
			"execution_mode", string(mode),
			"research_plane", string(wisdev.ResearchExecutionPlaneAutonomous),
			"query_length", len(query),
			"plan_query_count", len(req.Plan.Queries),
		)
		slog.Info("wisdev autonomous research policy resolved",
			"component", "wisdev.research",
			"operation", "autonomous_research",
			"stage", "policy_resolved",
			"trace_id", traceID,
			"session_id", sessionID,
			"mode", string(mode),
			"policy_mode", policy.Mode,
			"tools_enabled", policy.EnableWisdevTools,
			"allowlisted_count", len(policy.AllowlistedTools),
			"require_confirmation", policy.RequireHumanConfirmation,
		)
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
		profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(mode), qualityMode, false, req.MaxIterations)
		serviceTier := profile.ServiceTier
		if requested := wisdev.NormalizeServiceTier(firstNonEmpty(req.ServiceTier, req.Session.ServiceTier)); requested != "" {
			serviceTier = requested
		}
		slog.Info("wisdev autonomous research profile resolved",
			"component", "wisdev.research",
			"operation", "autonomous_research",
			"stage", "profile_resolved",
			"trace_id", traceID,
			"session_id", sessionID,
			"mode", string(mode),
			"execution_mode", string(mode),
			"research_plane", string(wisdev.ResearchExecutionPlaneAutonomous),
			"execution_plane", "go_canonical_runtime",
			"quality_mode", profile.QualityMode,
			"service_tier", serviceTier,
			"max_iterations", profile.MaxIterations,
			"max_search_terms", profile.SearchBudget.MaxSearchTerms,
			"hits_per_search", profile.SearchBudget.HitsPerSearch,
			"max_unique_papers", profile.SearchBudget.MaxUniquePapers,
		)
		transportMetadata := map[string]any{
			"backend":                "go-wisdev-autonomous",
			"executionPlane":         "go_canonical_runtime",
			"mode":                   string(mode),
			"executionMode":          string(mode),
			"researchPlane":          string(wisdev.ResearchExecutionPlaneAutonomous),
			"serviceTier":            serviceTier,
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
				OriginalQuery:               originalQuery,
				SeedQueries:                 plannedQueries,
				Domain:                      strings.TrimSpace(req.Session.DetectedDomain),
				ProjectID:                   sessionID,
				MaxIterations:               profile.MaxIterations,
				MaxSearchTerms:              profile.SearchBudget.MaxSearchTerms,
				HitsPerSearch:               profile.SearchBudget.HitsPerSearch,
				MaxUniquePapers:             profile.SearchBudget.MaxUniquePapers,
				AllocatedTokens:             profile.AllocatedTokens,
				Mode:                        string(profile.Mode),
				ServiceTier:                 serviceTier,
				TraceID:                     traceID,
				DisableProgrammaticPlanning: !allowProgrammaticPlanning,
				DisableHypothesisGeneration: !allowLoopHypothesisGeneration,
			}
			var loopResult *wisdev.LoopResult
			var loopErr error
			traceEmitter := buildResearchLoopTraceEmitter(agentGateway, loopReq.ProjectID, userID, "autonomousResearch", wisdev.ResearchExecutionPlaneAutonomous, traceID, query)
			slog.Info("wisdev autonomous research loop dispatch",
				"component", "wisdev.research",
				"operation", "autonomous_research",
				"stage", "loop_dispatch",
				"trace_id", traceID,
				"session_id", sessionID,
				"mode", string(mode),
				"seed_query_count", len(loopReq.SeedQueries),
				"max_iterations", loopReq.MaxIterations,
				"disable_programmatic_planning", loopReq.DisableProgrammaticPlanning,
				"disable_hypothesis_generation", loopReq.DisableHypothesisGeneration,
			)
			loopResult, loopErr = runUnifiedResearchLoop(r.Context(), runtime, wisdev.ResearchExecutionPlaneAutonomous, loopReq, traceEmitter)
			if loopErr == nil && loopResult != nil {
				canonicalLoopResult = loopResult
				slog.Info("wisdev autonomous research loop completed",
					"component", "wisdev.research",
					"operation", "autonomous_research",
					"stage", "loop_completed",
					"trace_id", traceID,
					"session_id", sessionID,
					"mode", string(mode),
					"executed_query_count", len(loopResult.ExecutedQueries),
					"paper_count", len(loopResult.Papers),
					"trace_event_count", len(loopResult.ReasoningTrace),
				)
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
		slog.Info("wisdev autonomous research response emitted",
			"component", "wisdev.research",
			"operation", "autonomous_research",
			"stage", "response_emitted",
			"trace_id", traceID,
			"session_id", sessionID,
			"mode", string(mode),
			"artifact_count", researchPayloadSliceLen(results["artifacts"]),
			"paper_count", researchPayloadSliceLen(results["papers"]),
			"trace_journal_enabled", transportMetadata["traceJournalEnabled"],
		)
		s.journalEvent("autonomous_research", "/wisdev/research/autonomous", traceID, sessionID, userID, "", "", "Autonomous research completed.", results, map[string]any{
			"primaryModel":     profile.PrimaryModelName,
			"primaryModelTier": profile.PrimaryModelTier,
			"serviceTier":      serviceTier,
			"allocatedTokens":  profile.AllocatedTokens,
			"complexityScore":  profile.ComplexityScore,
			"mode":             string(mode),
			"executionMode":    string(mode),
			"researchPlane":    string(wisdev.ResearchExecutionPlaneAutonomous),
			"policyMode":       policy.Mode,
			"toolsEnabled":     policy.EnableWisdevTools,
			"allowlistedCount": len(policy.AllowlistedTools),
			"requireConfirm":   policy.RequireHumanConfirmation,
		})
	})
}

func researchPayloadSliceLen(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	case []wisdev.Source:
		return len(typed)
	default:
		return 0
	}
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
