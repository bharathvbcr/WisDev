package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/telemetry"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func normalizeSessionMode(raw string) wisdev.WisDevMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "yolo_bounded", "yolo_full":
		return wisdev.WisDevModeYOLO
	default:
		return wisdev.WisDevModeGuided
	}
}

func ensureSessionQuestState(session map[string]any) map[string]any {
	if session == nil {
		return nil
	}
	if strings.TrimSpace(wisdev.AsOptionalString(session["questId"])) == "" {
		sessionID := strings.TrimSpace(wisdev.AsOptionalString(session["sessionId"]))
		if sessionID != "" {
			session["questId"] = "quest_" + sessionID
		}
	}
	return session
}

func persistSessionQuestState(store *wisdev.RuntimeStateStore, session map[string]any, status string) {
	if store == nil || session == nil {
		return
	}
	questID := strings.TrimSpace(wisdev.AsOptionalString(session["questId"]))
	if questID == "" {
		return
	}
	payload := map[string]any{
		"questId":         questID,
		"userId":          wisdev.AsOptionalString(session["userId"]),
		"originalQuery":   wisdev.AsOptionalString(session["originalQuery"]),
		"correctedQuery":  wisdev.AsOptionalString(session["correctedQuery"]),
		"detectedDomain":  wisdev.AsOptionalString(session["detectedDomain"]),
		"latestSessionId": wisdev.AsOptionalString(session["sessionId"]),
		"status":          strings.TrimSpace(status),
		"updatedAt":       time.Now().UnixMilli(),
	}
	if secondary, ok := session["secondaryDomains"].([]string); ok && len(secondary) > 0 {
		payload["secondaryDomains"] = secondary
	} else if secondaryAny, ok := session["secondaryDomains"].([]any); ok && len(secondaryAny) > 0 {
		payload["secondaryDomains"] = secondaryAny
	}
	_ = store.SaveQuestState(questID, payload)
}

func hydrateSessionQuestState(store *wisdev.RuntimeStateStore, session map[string]any) map[string]any {
	if store == nil || session == nil {
		return session
	}
	questID := strings.TrimSpace(wisdev.AsOptionalString(session["questId"]))
	if questID == "" {
		return session
	}
	questState, err := store.LoadQuestState(questID)
	if err == nil && len(questState) > 0 {
		session["questState"] = questState
	}
	return session
}

func ensureSessionArchitectureState(session map[string]any) map[string]any {
	if session == nil {
		return nil
	}

	mode := normalizeSessionMode(wisdev.AsOptionalString(session["mode"]))
	query := wisdev.ResolveSessionSearchQuery(
		wisdev.AsOptionalString(session["query"]),
		wisdev.AsOptionalString(session["correctedQuery"]),
		wisdev.AsOptionalString(session["originalQuery"]),
	)

	session["mode"] = string(mode)

	if strings.TrimSpace(wisdev.AsOptionalString(session["serviceTier"])) == "" {
		session["serviceTier"] = string(wisdev.ResolveServiceTier(mode, false))
	}

	if _, ok := session["reasoningGraph"].(map[string]any); !ok {
		session["reasoningGraph"] = map[string]any{
			"query": query,
			"nodes": []any{},
			"edges": []any{},
		}
	}

	if _, ok := session["memoryTiers"].(map[string]any); !ok {
		session["memoryTiers"] = map[string]any{
			"shortTermWorking": []any{},
			"longTermVector":   []any{},
			"userPersonalized": []any{},
			"artifactMemory":   []any{},
		}
	}

	if _, ok := session["modeManifest"].(map[string]any); !ok {
		session["modeManifest"] = wisdev.BuildModeManifestMap(mode, wisdev.ServiceTier(strings.TrimSpace(wisdev.AsOptionalString(session["serviceTier"]))))
	}

	return session
}

func sessionPayloadForResponse(agentGateway *wisdev.AgentGateway, session *wisdev.AgentSession) (map[string]any, error) {
	if session == nil || strings.TrimSpace(session.SessionID) == "" {
		return nil, fmt.Errorf("session is required")
	}

	var sessionPayload map[string]any
	if agentGateway != nil && agentGateway.StateStore != nil {
		loaded, err := agentGateway.StateStore.LoadAgentSession(session.SessionID)
		if err == nil && len(loaded) > 0 {
			sessionPayload = loaded
		}
	}
	if len(sessionPayload) == 0 {
		raw, err := json.Marshal(session)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &sessionPayload); err != nil {
			return nil, err
		}
	}

	sessionPayload = ensureSessionQuestState(sessionPayload)
	sessionPayload = ensureSessionArchitectureState(sessionPayload)
	if agentGateway != nil && agentGateway.StateStore != nil {
		sessionPayload = hydrateSessionQuestState(agentGateway.StateStore, sessionPayload)
	}
	return sessionPayload, nil
}

func newAgentSessionID() string {
	id := strings.TrimPrefix(strings.TrimSpace(wisdev.NewTraceID()), "trace_")
	if id == "" || id == "fallback" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "ws_" + id
}

func (s *wisdevServer) registerSessionRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	handleWisdevPlan := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			UserID        string         `json:"userId"`
			SessionID     string         `json:"sessionId"`
			Query         string         `json:"query"`
			OperationMode string         `json:"operationMode"`
			Metadata      map[string]any `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev initialize session decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		query := strings.TrimSpace(req.Query)
		if query == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}
		userID, err := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if err != nil {
			logWisdevRouteError(r, "wisdev plan authorization failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"error", err,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}

		session, err := agentGateway.CreateSession(r.Context(), userID, query)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to create session", map[string]any{
				"error": err.Error(),
			})
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID != "" {
			session.SessionID = sessionID
		}
		synthesized := wisdev.SynthesizePlanCandidates(session, query)
		allCandidates := make([]wisdev.PlanCandidate, 0, len(synthesized.Alternates)+1)
		allCandidates = append(allCandidates, synthesized.Selected)
		allCandidates = append(allCandidates, synthesized.Alternates...)
		reranked := wisdev.RerankPlanCandidatesWithVerifier(r.Context(), agentGateway.LLMClient, session, query, allCandidates)
		if len(reranked) > 0 {
			synthesized.Selected = reranked[0]
		}
		session.Plan = synthesized.Selected.Plan
		session.Status = wisdev.SessionExecutingPlan

		traceID := wisdev.NewTraceID()
		planID := ""
		if session.Plan != nil {
			planID = session.Plan.PlanID
		}
		s.journalEvent("plan_created", r.URL.Path, traceID, session.SessionID, userID, planID, "", "Research plan created and selected.", map[string]any{"plan": session.Plan}, nil)
		writeEnvelopeWithTraceID(w, traceID, "plan", session.Plan)
	}
	for _, path := range wisdevPlanPaths {
		mux.HandleFunc(path, handleWisdevPlan)
	}

	handleInitializeSession := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			UserID           string   `json:"userId"`
			OriginalQuery    string   `json:"originalQuery"`
			Query            string   `json:"query"`
			CorrectedQuery   string   `json:"correctedQuery"`
			DetectedDomain   string   `json:"detectedDomain"`
			SecondaryDomains []string `json:"secondaryDomains"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		originalQuery := strings.TrimSpace(req.OriginalQuery)
		if originalQuery == "" {
			originalQuery = strings.TrimSpace(req.Query)
		}
		authUserID := strings.TrimSpace(GetUserID(r))
		userID := authUserID
		switch authUserID {
		case "", "anonymous":
			logWisdevRouteError(r, "wisdev initialize session authorization failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"error", "authentication required",
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "authentication required", nil)
			return
		case "admin", "internal-service":
			if strings.TrimSpace(req.UserID) != "" {
				userID = strings.TrimSpace(req.UserID)
			}
		}
		if originalQuery == "" {
			logWisdevRouteError(r, "wisdev initialize session validation failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"resolved_user_id", userID,
				"has_original_query", originalQuery != "",
			)
			telemetry.Logger().WarnContext(r.Context(), "session init rejected: missing required fields",
				"component", "session_routes",
				"operation", "handleInitializeSession",
				"stage", "validation_failed",
				"has_user_id", userID != "",
				"has_original_query", originalQuery != "",
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId and originalQuery are required", map[string]any{
				"requiredFields": []string{"userId", "originalQuery"},
			})
			return
		}
		telemetry.Logger().InfoContext(r.Context(), "session init: query received and validated",
			"component", "session_routes",
			"operation", "handleInitializeSession",
			"stage", "validation_passed",
			"user_id", userID,
			"query_length", len(originalQuery),
			"query_preview", firstNChars(originalQuery, 80),
			"has_query", strings.TrimSpace(req.Query) != "",
			"has_corrected_query", strings.TrimSpace(req.CorrectedQuery) != "",
			"detected_domain", strings.TrimSpace(req.DetectedDomain),
		)
		if status, cached, ok := enforceIdempotency(r, agentGateway, "wisdev_agent_initialize:"+userID+":"+originalQuery); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}

		now := time.Now().UnixMilli()
		sessionID := newAgentSessionID()
		correctedQuery := strings.TrimSpace(req.CorrectedQuery)
		if correctedQuery == "" {
			correctedQuery = originalQuery
		}
		planningQuery := strings.TrimSpace(req.Query)
		if planningQuery == "" {
			planningQuery = wisdev.ResolveSessionQueryText(correctedQuery, originalQuery)
		}
		detectedDomain := strings.TrimSpace(req.DetectedDomain)
		questions, questionSequence, minQuestions, maxQuestions := defaultAgentQuestionPlan(correctedQuery, detectedDomain, req.SecondaryDomains)
		complexityScore := wisdev.EstimateComplexityScore(correctedQuery)
		expertiseLevel := "intermediate"
		if strings.Contains(strings.ToLower(detectedDomain), "biology") || strings.Contains(strings.ToLower(detectedDomain), "medicine") {
			expertiseLevel = "advanced"
		}
		session := map[string]any{
			"sessionId":            sessionID,
			"userId":               userID,
			"query":                planningQuery,
			"originalQuery":        originalQuery,
			"correctedQuery":       correctedQuery,
			"detectedDomain":       detectedDomain,
			"secondaryDomains":     req.SecondaryDomains,
			"answers":              map[string]any{},
			"questions":            questions,
			"questionSequence":     questionSequence,
			"currentQuestionIndex": 0,
			"minQuestions":         minQuestions,
			"maxQuestions":         maxQuestions,
			"complexityScore":      complexityScore,
			"clarificationBudget":  maxQuestions,
			"questionStopReason":   "",
			"status":               string(wisdev.SessionQuestioning),
			"expertiseLevel":       expertiseLevel,
			"autoRefine":           true,
			"createdAt":            now,
			"updatedAt":            now,
		}
		session = ensureSessionQuestState(session)
		session = ensureSessionArchitectureState(session)

		// Proactive Triage — assess complexity in the background without
		// blocking the response. Uses a targeted field-level mutation instead
		// of a full Load+Overwrite to avoid clobbering answers that a fast
		// user may have submitted between session creation and the 15-second
		// triage deadline.
		if originalQuery != "" && agentGateway.Brain != nil {
			// Capture by value to avoid data races: if agentGateway fields
			// are ever reconfigured, the goroutine uses the snapshot it saw
			// at launch time, not the mutated post-launch values.
			brain := agentGateway.Brain
			store := agentGateway.StateStore
			go func(sid, uid string, q string) {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Warn("wisdev triage goroutine recovered from panic",
							"session_id", sid,
							"error", fmt.Sprintf("%v", rec),
						)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				complexity, _ := brain.AssessResearchComplexityInteractive(ctx, q)
				if complexity == "" {
					return
				}
				// Reload the current session state so we only patch the
				// assessedComplexity field on top of whatever the session
				// looks like now, rather than overwriting the whole document
				// with the snapshot we had at creation time.
				s, err := store.LoadAgentSession(sid)
				if err != nil {
					return
				}
				// Only patch if the field is still absent — a concurrent
				// triage run or a frontend update may have already set it.
				if s["assessedComplexity"] != nil && s["assessedComplexity"] != "" {
					return
				}
				s["assessedComplexity"] = complexity
				store.PersistAgentSessionMutation(sid, uid, s, wisdev.RuntimeJournalEntry{
					EventID:   wisdev.NewTraceID(),
					SessionID: sid,
					UserID:    uid,
					EventType: "agent_complexity_triage",
					Status:    "completed",
					Summary:   "Search depth selected from the query analysis.",
					Payload:   map[string]any{"assessedComplexity": complexity},
				})
			}(sessionID, userID, originalQuery)
		}

		// Proactively seed dynamic options for q4_subtopics in the background so
		// they are ready when the user reaches that question. q5_study_types is
		// intentionally seeded by the answer-driven q2/q3/q4 prewarm path, where
		// the longer budget and subtopic context avoid a premature fallback.
		// Uses the same snapshot-and-patch pattern as the complexity triage above.
		// Use correctedQuery (already defaults to originalQuery when empty) so
		// dynamic generation uses the same query as the on-demand and regenerate
		// paths. Do not require a live LLM client here because the generation
		// helpers already fall back heuristically.
		if correctedQuery != "" && agentGateway != nil {
			go func(sid, uid, q, domain string) {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Warn("wisdev dynamic options seeding goroutine recovered from panic",
							"session_id", sid,
							"error", fmt.Sprintf("%v", rec),
						)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()

				subtopics, _, _, subtopicSrc, subtopicExpl := buildSubtopicsResponse(ctx, agentGateway, q, domain, 8)

				s, err := agentGateway.StateStore.LoadAgentSession(sid)
				if err != nil {
					return
				}
				// NOTE: sliceAnyMap returns deep copies; s["questions"] = questions
				// must be reassigned after modifying any element.
				questions := sliceAnyMap(s["questions"])
				patched := false
				for i, question := range questions {
					qid := wisdev.AsOptionalString(question["id"])
					if qid == "q4_subtopics" && len(subtopics) > 0 {
						existing := questionOptionValues(question["options"])
						if len(existing) == 0 {
							opts := make([]any, len(subtopics))
							for j, sub := range subtopics {
								opts[j] = map[string]any{"value": sub, "label": sub}
							}
							questions[i]["options"] = opts
							questions[i]["optionsSource"] = subtopicSrc
							questions[i]["optionsExplanation"] = subtopicExpl
							patched = true
						}
					}
				}
				if !patched {
					return
				}
				s["questions"] = questions
				_ = agentGateway.StateStore.PersistAgentSessionMutation(sid, uid, s, wisdev.RuntimeJournalEntry{
					EventID:   wisdev.NewTraceID(),
					SessionID: sid,
					UserID:    uid,
					EventType: "agent_session_subtopic_options_seed",
					Status:    "completed",
					Summary:   "Subtopic question options seeded via LLM.",
					Payload: map[string]any{
						"subtopicCount":  len(subtopics),
						"studyTypeCount": 0,
					},
				})
			}(sessionID, userID, correctedQuery, detectedDomain)
		}

		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: sessionID,
			UserID:    userID,
			EventType: "agent_session_initialize",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Agent session initialized.",
			Payload:   cloneAnyMap(session),
			Metadata:  nil,
		}); err != nil {
			logWisdevRouteError(r, "wisdev initialize session persist failed",
				"session_id", sessionID,
				"resolved_user_id", userID,
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist agent session", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := syncCanonicalSessionStore(agentGateway, session); err != nil {
			logWisdevRouteError(r, "wisdev initialize session canonical sync failed",
				"session_id", sessionID,
				"resolved_user_id", userID,
				"error", err,
			)
			// The StateStore entry was already written above. We cannot easily
			// roll it back because RuntimeStateStore has no Delete method.
			// Mark the session status as "abandoned" so any subsequent call
			// to LoadAgentSession + ensureAgentSessionMutable will reject it
			// rather than silently using a session the canonical store doesn't
			// know about.
			if s, loadErr := agentGateway.StateStore.LoadAgentSession(sessionID); loadErr == nil {
				s["status"] = string(wisdev.StatusAbandoned)
				s["questionStopReason"] = "canonical_sync_failed"
				_ = agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, s, wisdev.RuntimeJournalEntry{
					EventID:   wisdev.NewTraceID(),
					SessionID: sessionID,
					UserID:    userID,
					EventType: "agent_session_init_rollback",
					Status:    "abandoned",
					Summary:   "Session abandoned due to canonical sync failure during initialization.",
				})
			}
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to sync canonical session", map[string]any{
				"error":     err.Error(),
				"sessionId": sessionID,
			})
			return
		}
		if manifest, ok := session["modeManifest"].(map[string]any); ok {
			_ = agentGateway.StateStore.SaveModeManifest(sessionID, manifest)
		}
		persistSessionQuestState(agentGateway.StateStore, session, "active")
		telemetry.Logger().InfoContext(r.Context(), "session init: session created and persisted",
			"component", "session_routes",
			"operation", "handleInitializeSession",
			"stage", "session_created",
			"session_id", sessionID,
			"user_id", userID,
			"planning_query_preview", firstNChars(planningQuery, 80),
			"query_preview", firstNChars(originalQuery, 80),
			"corrected_query_changed", correctedQuery != originalQuery,
			"detected_domain", strings.TrimSpace(req.DetectedDomain),
			"question_count", len(questions),
			"complexity_score", complexityScore,
		)
		responseBody := buildAgentQuestioningEnvelopeBody(traceID, session, false)
		body, _ := json.Marshal(responseBody)
		storeIdempotentResponse(agentGateway, r, "wisdev_agent_initialize:"+userID+":"+originalQuery, body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		_ = json.NewEncoder(w).Encode(responseBody)
	}
	for _, path := range wisdevSessionInitializePaths {
		mux.HandleFunc(path, handleInitializeSession)
	}

	handleGetSession := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				logWisdevRouteError(r, "wisdev get session decode failed", "error", err)
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if strings.TrimSpace(req.UserID) == "" {
			req.UserID = strings.TrimSpace(r.URL.Query().Get("userId"))
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": "sessionId is required",
			})
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev get session load failed",
				"session_id", sessionID,
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		session = ensureSessionQuestState(session)
		session = ensureSessionArchitectureState(session)
		session = hydrateSessionQuestState(agentGateway.StateStore, session)
		traceID := wisdev.NewTraceID()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		_ = json.NewEncoder(w).Encode(buildAgentQuestioningEnvelopeBody(traceID, session, false))
		s.journalEvent("agent_session_get", r.URL.Path, traceID, sessionID, strings.TrimSpace(req.UserID), "", "", "Agent session requested.", session, nil)
	}
	for _, path := range wisdevSessionGetPaths {
		mux.HandleFunc(path, handleGetSession)
	}

	handleListSessions := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		if agentGateway == nil || agentGateway.Store == nil {
			logWisdevRouteError(r, "wisdev list sessions unavailable", "reason", "canonical_store_uninitialized")
			WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "canonical session store is not initialized", nil)
			return
		}

		var req struct {
			UserID string `json:"userId"`
			Limit  int    `json:"limit"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				logWisdevRouteError(r, "wisdev list sessions decode failed", "error", err)
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
		if strings.TrimSpace(req.UserID) == "" {
			req.UserID = strings.TrimSpace(r.URL.Query().Get("userId"))
		}
		if req.Limit <= 0 {
			if parsedLimit, parseErr := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); parseErr == nil {
				req.Limit = parsedLimit
			}
		}
		userID, err := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if err != nil {
			logWisdevRouteError(r, "wisdev list sessions authorization failed",
				"request_user_id", strings.TrimSpace(req.UserID),
				"error", err,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, err.Error(), nil)
			return
		}
		if userID == "" {
			logWisdevRouteError(r, "wisdev list sessions validation failed", "reason", "missing_user_id")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "userId is required", map[string]any{
				"field": "userId",
			})
			return
		}
		if req.Limit <= 0 {
			req.Limit = 10
		}
		if req.Limit > 100 {
			req.Limit = 100
		}

		sessions, err := agentGateway.Store.List(r.Context(), userID)
		if err != nil {
			logWisdevRouteError(r, "wisdev list sessions load failed",
				"resolved_user_id", userID,
				"limit", req.Limit,
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to list agent sessions", map[string]any{
				"error":  err.Error(),
				"userId": userID,
			})
			return
		}
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].UpdatedAt > sessions[j].UpdatedAt
		})

		payloads := make([]map[string]any, 0, len(sessions))
		seenSessionIDs := make(map[string]struct{}, len(sessions))
		for _, session := range sessions {
			if session == nil || strings.TrimSpace(session.SessionID) == "" {
				continue
			}
			if _, exists := seenSessionIDs[session.SessionID]; exists {
				continue
			}
			sessionPayload, payloadErr := sessionPayloadForResponse(agentGateway, session)
			if payloadErr != nil {
				continue
			}
			seenSessionIDs[session.SessionID] = struct{}{}
			payloads = append(payloads, sessionPayload)
			if len(payloads) >= req.Limit {
				break
			}
		}

		traceID := wisdev.NewTraceID()
		writeEnvelopeWithTraceID(w, traceID, "sessions", payloads)
		s.journalEvent("agent_session_list", r.URL.Path, traceID, "", userID, "", "", "Agent sessions listed.", map[string]any{
			"userId":       userID,
			"limit":        req.Limit,
			"sessionCount": len(payloads),
		}, nil)
	}
	for _, path := range wisdevSessionListPaths {
		mux.HandleFunc(path, handleListSessions)
	}

	handleCompleteSession := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev complete session decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if sessionID == "" {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", nil)
			return
		}
		logWisdevRouteLifecycle(r, "wisdev_complete_session", "entry", "",
			"session_id", sessionID,
		)
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev complete session load failed",
				"session_id", sessionID,
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if status, cached, ok := enforceIdempotency(r, agentGateway, "wisdev_agent_complete:"+sessionID); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}
		if strings.TrimSpace(wisdev.AsOptionalString(session["status"])) == "completed" {
			WriteError(w, http.StatusConflict, ErrInvalidParameters, "agent session is already completed", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if _, ok := session["orchestrationPlan"].(map[string]any); !ok {
			sessionQuery := wisdev.ResolveSessionSearchQuery(
				wisdev.AsOptionalString(session["query"]),
				wisdev.AsOptionalString(session["correctedQuery"]),
				wisdev.AsOptionalString(session["originalQuery"]),
			)
			if sessionQuery == "" {
				// Cannot build a useful orchestration plan with no query.
				// Return 400 so the frontend can surface a user-visible error
				// rather than completing the session with zero search queries,
				// which permanently marks the session complete with no retry path.
				telemetry.Logger().WarnContext(r.Context(), "handleCompleteSession: session has no resolvable query — rejecting completion",
					"component", "session_routes",
					"operation", "handleCompleteSession",
					"stage", "complete_empty_query_rejected",
					"session_id", sessionID,
				)
				WriteError(w, http.StatusBadRequest, ErrInvalidParameters,
					"cannot complete session: session has no resolvable query",
					map[string]any{"sessionId": sessionID, "field": "query"})
				return
			}
			session["orchestrationPlan"] = buildAgentOrchestrationPlan(session)
		}
		session["status"] = "completed"
		session["updatedAt"] = time.Now().UnixMilli()
		if strings.TrimSpace(wisdev.AsOptionalString(session["questionStopReason"])) == "" {
			session["questionStopReason"] = "evidence_sufficient"
		}
		session = ensureSessionQuestState(session)
		session = ensureSessionArchitectureState(session)
		traceID := wisdev.NewTraceID()
		userID := wisdev.AsOptionalString(session["userId"])
		if err := agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: sessionID,
			UserID:    userID,
			EventType: "agent_complete_session",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Agent session completed.",
			Payload:   cloneAnyMap(session),
			Metadata:  nil,
		}); err != nil {
			logWisdevRouteError(r, "wisdev complete session persist failed",
				"session_id", sessionID,
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist completed session", map[string]any{
				"error":     err.Error(),
				"sessionId": sessionID,
			})
			return
		}
		if err := syncCanonicalSessionStore(agentGateway, session); err != nil {
			logWisdevRouteError(r, "wisdev complete session canonical sync failed",
				"session_id", sessionID,
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to sync canonical session", map[string]any{
				"error":     err.Error(),
				"sessionId": sessionID,
			})
			return
		}
		persistSessionQuestState(agentGateway.StateStore, session, "completed")

		// Echo all query fields back so the frontend session mapper can
		// populate WisDevSession.query / .originalQuery / .correctedQuery
		// after the completion call. Without this, the mapper receives a
		// partial payload and all query fields default to "", which is the
		// root cause of the "session lost its search query" error in the
		// handleWisDevSearch path.
		query := wisdev.AsOptionalString(session["query"])
		originalQuery := wisdev.AsOptionalString(session["originalQuery"])
		correctedQuery := wisdev.AsOptionalString(session["correctedQuery"])
		// If the stored session somehow has no query field, fall back so the
		// frontend always receives a usable value.
		if query == "" {
			query = wisdev.ResolveSessionQueryText(correctedQuery, originalQuery)
		}

		payload := map[string]any{
			"ok":                 true,
			"sessionId":          sessionID,
			"status":             session["status"],
			"questionStopReason": session["questionStopReason"],
			"orchestrationPlan":  session["orchestrationPlan"],
			// Query fields echoed for frontend session mapper
			"query":          query,
			"originalQuery":  originalQuery,
			"correctedQuery": correctedQuery,
		}

		telemetry.Logger().InfoContext(r.Context(), "session complete: response prepared",
			"component", "session_routes",
			"operation", "handleCompleteSession",
			"stage", "complete_response_ready",
			"session_id", sessionID,
			"has_query", query != "",
			"has_original_query", originalQuery != "",
			"has_orchestration_plan", session["orchestrationPlan"] != nil,
		)

		body, _ := json.Marshal(buildEnvelopeBody(traceID, "completion", payload))
		storeIdempotentResponse(agentGateway, r, "wisdev_agent_complete:"+sessionID, body)
		writeEnvelopeWithTraceID(w, traceID, "completion", payload)
	}
	for _, path := range wisdevSessionCompletePaths {
		mux.HandleFunc(path, handleCompleteSession)
	}

	handleGenerateSearchQueries := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev generate search queries decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{"error": err.Error()})
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if sessionID == "" {
			logWisdevRouteError(r, "wisdev generate search queries validation failed", "reason", "missing_session_id")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", nil)
			return
		}
		logWisdevRouteLifecycle(r, "wisdev_generate_search_queries", "load_session", "",
			"session_id", sessionID,
			"stage", "load_session",
		)
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev generate search queries load failed", "session_id", sessionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		canonical := buildCanonicalAgentSession(session)
		if canonical == nil {
			logWisdevRouteError(r, "wisdev generate search queries canonical build failed", "session_id", sessionID)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to build canonical session for query generation", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		resolvedQuery := wisdev.ResolveSessionSearchQuery(canonical.Query, canonical.CorrectedQuery, canonical.OriginalQuery)
		if resolvedQuery == "" {
			telemetry.Logger().WarnContext(r.Context(), "session search-queries: session has no resolvable query — returning 400",
				"component", "session_routes",
				"operation", "handleGenerateSearchQueries",
				"stage", "empty_session_query",
				"session_id", sessionID,
				"session_query", canonical.Query,
				"session_corrected_query", canonical.CorrectedQuery,
				"session_original_query", canonical.OriginalQuery,
			)
			// Return 400 so the frontend receives an error it can handle
			// rather than HTTP 200 with an empty queries array, which causes
			// a silent zero-tab search. Previously the code fell through to
			// GenerateSearchQueries which returned []string{} on empty base.
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters,
				"session has no resolvable query; cannot generate search queries",
				map[string]any{"sessionId": sessionID, "field": "query"})
			return
		}
		generated := wisdev.GenerateSearchQueries(canonical.ToSession())
		traceID := wisdev.NewTraceID()
		if generated.QueryCount == 0 {
			telemetry.Logger().WarnContext(r.Context(), "session search-queries: generated zero queries — frontend will have nothing to search",
				"component", "session_routes",
				"operation", "handleGenerateSearchQueries",
				"stage", "empty_generated_queries",
				"session_id", sessionID,
				"resolved_query", wisdev.QueryPreview(resolvedQuery),
				"trace_id", traceID,
			)
		}
		s.journalEvent("agent_generate_search_queries", r.URL.Path, traceID, sessionID, wisdev.AsOptionalString(session["userId"]), "", "", "Generated search queries for agent session.", map[string]any{
			"sessionId":        sessionID,
			"originalQuery":    canonical.OriginalQuery,
			"correctedQuery":   canonical.CorrectedQuery,
			"queryUsed":        generated.QueryUsed,
			"queryCount":       generated.QueryCount,
			"estimatedResults": generated.EstimatedResults,
			"coverageMap":      generated.CoverageMap,
		}, nil)
		logWisdevRouteLifecycle(r, "wisdev_generate_search_queries", "response_ready", wisdev.ResolveSessionSearchQuery(wisdev.AsOptionalString(session["query"]), wisdev.AsOptionalString(session["correctedQuery"]), wisdev.AsOptionalString(session["originalQuery"])),
			"session_id", sessionID,
			"trace_id", traceID,
			"query_used", wisdev.QueryPreview(generated.QueryUsed),
			"query_count", generated.QueryCount,
			"estimated_results", generated.EstimatedResults,
			"result", "success",
		)
		writeEnvelopeWithTraceID(w, traceID, "queries", generated)
	}
	for _, path := range wisdevSessionSearchQueriesPaths {
		mux.HandleFunc(path, handleGenerateSearchQueries)
	}

	handleApplyOrchestrationPlan := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID         string              `json:"sessionId"`
			Queries           []string            `json:"queries"`
			CoverageMap       map[string][]string `json:"coverageMap"`
			GeneratedFromTree bool                `json:"generatedFromTree"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev apply orchestration plan decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		sessionID := strings.TrimSpace(req.SessionID)
		if sessionID == "" {
			sessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if sessionID == "" {
			logWisdevRouteError(r, "wisdev apply orchestration plan validation failed", "reason", "missing_session_id")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", nil)
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev apply orchestration plan load failed", "session_id", sessionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			logWisdevRouteError(r, "wisdev apply orchestration plan rejected immutable session", "session_id", sessionID, "error", err)
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		normalizedQueries := normalizeResearchPlanQueries(req.Queries)
		if len(normalizedQueries) == 0 {
			logWisdevRouteError(r, "wisdev apply orchestration plan validation failed",
				"session_id", sessionID,
				"reason", "empty_queries",
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "queries must contain at least one non-empty query", map[string]any{
				"field": "queries",
			})
			return
		}
		coverageMap := map[string]any{}
		for key, values := range req.CoverageMap {
			normalizedValues := normalizeResearchPlanQueries(values)
			if len(normalizedValues) == 0 {
				continue
			}
			coverageMap[strings.TrimSpace(key)] = normalizedValues
		}
		session["orchestrationPlan"] = buildAgentOrchestrationPlanWithQueries(session, normalizedQueries, coverageMap, req.GeneratedFromTree)
		session["updatedAt"] = time.Now().UnixMilli()
		session = ensureSessionQuestState(session)
		session = ensureSessionArchitectureState(session)
		traceID := wisdev.NewTraceID()
		userID := wisdev.AsOptionalString(session["userId"])
		if err := agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: sessionID,
			UserID:    userID,
			EventType: "agent_apply_orchestration_plan",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Applied orchestration plan to agent session.",
			Payload:   cloneAnyMap(session),
			Metadata: map[string]any{
				"generatedFromTree": req.GeneratedFromTree,
				"queryCount":        len(normalizedQueries),
			},
		}); err != nil {
			logWisdevRouteError(r, "wisdev apply orchestration plan persist failed",
				"session_id", sessionID,
				"query_count", len(normalizedQueries),
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist orchestration plan", map[string]any{
				"error":     err.Error(),
				"sessionId": sessionID,
			})
			return
		}
		if err := syncCanonicalSessionStore(agentGateway, session); err != nil {
			logWisdevRouteError(r, "wisdev apply orchestration plan canonical sync failed",
				"session_id", sessionID,
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to sync canonical session", map[string]any{
				"error":     err.Error(),
				"sessionId": sessionID,
			})
			return
		}
		persistSessionQuestState(agentGateway.StateStore, session, wisdev.AsOptionalString(session["status"]))

		// Ensure query fields are always present in the response so the
		// frontend session mapper (mapRuntimeSessionToWisDevSession) can
		// populate WisDevSession.query / .originalQuery / .correctedQuery
		// without relying on queryRef.current as a fallback. This mirrors
		// the same fix applied to handleCompleteSession (bug E-2).
		if wisdev.AsOptionalString(session["query"]) == "" {
			session["query"] = wisdev.ResolveSessionQueryText(
				wisdev.AsOptionalString(session["correctedQuery"]),
				wisdev.AsOptionalString(session["originalQuery"]),
			)
		}

		writeEnvelopeWithTraceID(w, traceID, "session", session)
	}
	for _, path := range wisdevSessionOrchestrationPlanPaths {
		mux.HandleFunc(path, handleApplyOrchestrationPlan)
	}
}
