package api

import (
	"net/http"
	"sort"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type deepResearchBudget struct {
	maxSearchTerms int
	hitsPerSearch  int
}

func normalizeDeepResearchQualityMode(raw string) string {
	return wisdev.NormalizeResearchQualityMode(raw)
}

func getDeepResearchBudget(mode string) deepResearchBudget {
	budget := wisdev.ResolveSearchBudget(normalizeDeepResearchQualityMode(mode), wisdev.WisDevModeGuided)
	return deepResearchBudget{
		maxSearchTerms: budget.MaxSearchTerms,
		hitsPerSearch:  budget.HitsPerSearch,
	}
}

func (h *WisDevHandler) legacySessionManager() *wisdev.SessionManager {
	if h.sessions != nil {
		return h.sessions
	}
	if h.gateway != nil && h.gateway.WisdevSessions != nil {
		return h.gateway.WisdevSessions
	}
	return nil
}

func (h *WisDevHandler) legacyGuidedFlow() *wisdev.GuidedFlow {
	if h.guided != nil {
		return h.guided
	}
	if h.gateway != nil && h.gateway.WisdevGuided != nil {
		return h.gateway.WisdevGuided
	}
	return nil
}

func legacyQuestionIndex() map[string]wisdev.Question {
	questions := wisdev.DefaultQuestionFlow()
	index := make(map[string]wisdev.Question, len(questions))
	for _, question := range questions {
		index[question.ID] = question
	}
	return index
}

func legacyQuestionOptions(session *wisdev.Session, question wisdev.Question) []wisdev.QuestionOption {
	if len(question.Options) > 0 && question.ID != "q6_exclusions" {
		return append([]wisdev.QuestionOption(nil), question.Options...)
	}

	query := ""
	domain := ""
	if session != nil {
		query = strings.TrimSpace(firstNonEmpty(session.CorrectedQuery, session.OriginalQuery))
		domain = strings.ToLower(strings.TrimSpace(session.DetectedDomain))
	}

	switch question.ID {
	case "q4_subtopics":
		options := optionsFromQueryKeywords(query, 4)
		if len(options) > 0 {
			return options
		}
		return []wisdev.QuestionOption{
			{Value: "background", Label: "Background"},
			{Value: "methods", Label: "Methods"},
			{Value: "outcomes", Label: "Outcomes"},
		}
	case "q5_study_types":
		if domain == "medicine" {
			return []wisdev.QuestionOption{
				{Value: "systematic_review", Label: "Systematic Review"},
				{Value: "randomized_trial", Label: "Randomized Trial"},
				{Value: "cohort_study", Label: "Cohort Study"},
			}
		}
		return []wisdev.QuestionOption{
			{Value: "benchmark", Label: "Benchmark Study"},
			{Value: "empirical", Label: "Empirical Evaluation"},
			{Value: "survey", Label: "Survey"},
		}
	case "q6_exclusions":
		if domain == "medicine" {
			return []wisdev.QuestionOption{
				{Value: "animal_studies", Label: "Animal Studies"},
				{Value: "preprints", Label: "Preprints"},
				{Value: "non_english", Label: "Non-English"},
			}
		}
		return []wisdev.QuestionOption{
			{Value: "preprints", Label: "Preprints"},
			{Value: "non_peer_reviewed", Label: "Non-peer-reviewed"},
			{Value: "tutorials", Label: "Tutorials"},
		}
	default:
		return []wisdev.QuestionOption{}
	}
}

func hydrateLegacyQuestion(session *wisdev.Session, question wisdev.Question) wisdev.Question {
	question.Options = legacyQuestionOptions(session, question)
	return question
}

func optionsFromQueryKeywords(query string, limit int) []wisdev.QuestionOption {
	if limit <= 0 {
		limit = 4
	}
	stopwords := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "effects": {}, "for": {}, "from": {}, "how": {}, "in": {},
		"memory": {}, "of": {}, "on": {}, "or": {}, "outcomes": {}, "sleep": {}, "study": {},
		"the": {}, "to": {}, "using": {}, "with": {},
	}
	seen := map[string]struct{}{}
	options := make([]wisdev.QuestionOption, 0, limit)
	for _, raw := range strings.Fields(strings.ToLower(query)) {
		token := strings.Trim(raw, ".,:;()[]{}\"'`")
		if len(token) < 4 {
			continue
		}
		if _, blocked := stopwords[token]; blocked {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		options = append(options, wisdev.QuestionOption{
			Value: token,
			Label: strings.Title(token),
		})
		if len(options) >= limit {
			break
		}
	}
	sort.SliceStable(options, func(i, j int) bool {
		return options[i].Value < options[j].Value
	})
	return options
}

func recommendedLegacyQuestionValues(session *wisdev.Session, question wisdev.Question) []string {
	options := legacyQuestionOptions(session, question)
	if len(options) == 0 {
		return []string{}
	}
	limit := 1
	if question.IsMultiSelect {
		limit = 3
	}
	values := make([]string, 0, limit)
	for _, option := range options {
		if strings.TrimSpace(option.Value) == "" {
			continue
		}
		values = append(values, option.Value)
		if len(values) >= limit {
			break
		}
	}
	return values
}

func (h *WisDevHandler) HandleInitialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	var req struct {
		UserID string `json:"userId"`
		Query  string `json:"query"`
	}
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	session, err := sessions.CreateSession(r.Context(), strings.TrimSpace(req.UserID), strings.TrimSpace(req.Query))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to initialize session", map[string]any{
			"error": err.Error(),
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, session)
}

func (h *WisDevHandler) HandleGetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, session)
}

func (h *WisDevHandler) HandleProcessAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	sessions := h.legacySessionManager()
	guided := h.legacyGuidedFlow()
	if sessions == nil || guided == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "guided questioning is not initialized", nil)
		return
	}

	var req struct {
		SessionID     string   `json:"sessionId"`
		QuestionID    string   `json:"questionId"`
		Values        []string `json:"values"`
		DisplayValues []string `json:"displayValues"`
	}
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.QuestionID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", nil)
		return
	}

	session, err := sessions.GetSession(r.Context(), strings.TrimSpace(req.SessionID))
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": req.SessionID,
		})
		return
	}

	answer := wisdev.Answer{
		QuestionID:    strings.TrimSpace(req.QuestionID),
		Values:        append([]string(nil), req.Values...),
		DisplayValues: append([]string(nil), req.DisplayValues...),
		AnsweredAt:    wisdev.NowMillis(),
	}
	if err := guided.ProcessAnswer(r.Context(), session, answer); err != nil {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
			"sessionId":  req.SessionID,
			"questionId": req.QuestionID,
		})
		return
	}
	if err := sessions.SaveSession(r.Context(), session); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": req.SessionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, session)
}

func (h *WisDevHandler) HandleNextQuestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	sessions := h.legacySessionManager()
	guided := h.legacyGuidedFlow()
	if sessions == nil || guided == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "guided questioning is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}
	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	question, ok := guided.GetNextQuestion(session)
	if !ok || question == nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "no next question available", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	hydrated := hydrateLegacyQuestion(session, *question)
	writeJSONResponse(w, http.StatusOK, hydrated)
}

func (h *WisDevHandler) HandleCompleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}
	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	session.Status = wisdev.StatusComplete
	if err := sessions.SaveSession(r.Context(), session); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to persist session", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]any{"status": "complete"})
}

func (h *WisDevHandler) HandleQuestionOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
	if sessionID == "" || questionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", nil)
		return
	}
	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	question, ok := legacyQuestionIndex()[questionID]
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "question not found", map[string]any{
			"questionId": questionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]any{
		"questionId": questionID,
		"options":    legacyQuestionOptions(session, question),
	})
}

func (h *WisDevHandler) HandleQuestionRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
	if sessionID == "" || questionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", nil)
		return
	}
	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}
	question, ok := legacyQuestionIndex()[questionID]
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "question not found", map[string]any{
			"questionId": questionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]any{
		"questionId": questionID,
		"values":     recommendedLegacyQuestionValues(session, question),
	})
}

func (h *WisDevHandler) HandleRegenerateQuestionOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	var req struct {
		SessionID  string `json:"sessionId"`
		QuestionID string `json:"questionId"`
	}
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.QuestionID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", nil)
		return
	}
	session, err := sessions.GetSession(r.Context(), strings.TrimSpace(req.SessionID))
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": req.SessionID,
		})
		return
	}
	question, ok := legacyQuestionIndex()[strings.TrimSpace(req.QuestionID)]
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "question not found", map[string]any{
			"questionId": req.QuestionID,
		})
		return
	}

	writeJSONResponse(w, http.StatusOK, map[string]any{
		"questionId": req.QuestionID,
		"options":    legacyQuestionOptions(session, question),
	})
}

func (h *WisDevHandler) HandleGenerateQueries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	sessions := h.legacySessionManager()
	if sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "session manager is not initialized", nil)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeStrictJSONBody(r.Body, &req); err != nil {
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
		sessionID = strings.TrimSpace(req.SessionID)
	}
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}
	session, err := sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	query := strings.TrimSpace(wisdev.ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
	logWisdevRouteLifecycle(r, "wisdev_generate_queries_legacy", "request_received", query,
		"session_id", sessionID,
		"result", "accepted",
	)

	payload := wisdev.GenerateSearchQueries(session)
	logWisdevRouteLifecycle(r, "wisdev_generate_queries_legacy", "response_ready", query,
		"session_id", sessionID,
		"query_count", payload.QueryCount,
		"estimated_results", payload.EstimatedResults,
		"result", "success",
	)

	writeJSONResponse(w, http.StatusOK, payload)
}

func (h *WisDevHandler) HandleDeepResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.gateway == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "agent gateway is not initialized", nil)
		return
	}

	var req struct {
		Query       string   `json:"query"`
		Categories  []string `json:"categories"`
		DomainHint  string   `json:"domainHint"`
		SessionID   string   `json:"sessionId"`
		QualityMode string   `json:"quality_mode"`
		Session     struct {
			SessionID      string `json:"sessionId"`
			Query          string `json:"query"`
			OriginalQuery  string `json:"originalQuery"`
			CorrectedQuery string `json:"correctedQuery"`
			DetectedDomain string `json:"detectedDomain"`
		} `json:"session"`
		Plan struct {
			Query string `json:"query"`
		} `json:"plan"`
	}
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	sessionID := strings.TrimSpace(firstNonEmpty(req.SessionID, req.Session.SessionID))
	sessionQuery := ""
	sessionDomain := ""
	if sessionID != "" {
		if sessions := h.legacySessionManager(); sessions != nil {
			if session, err := sessions.GetSession(r.Context(), sessionID); err == nil && session != nil {
				sessionQuery = strings.TrimSpace(wisdev.ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
				sessionDomain = strings.TrimSpace(session.DetectedDomain)
			} else if err != nil {
				logWisdevRouteError(r, "failed to load legacy session during deep research query resolution",
					"operation", "wisdev_deep_research_legacy",
					"session_id", sessionID,
					"error", err.Error(),
				)
			}
		}
	}

	query := strings.TrimSpace(firstNonEmpty(
		req.Query,
		wisdev.ResolveSessionSearchQuery(req.Session.Query, req.Session.CorrectedQuery, req.Session.OriginalQuery),
		req.Plan.Query,
		sessionQuery,
	))
	if query == "" {
		logWisdevRouteLifecycle(r, "wisdev_deep_research_legacy", "request_rejected", query,
			"session_id", sessionID,
			"has_explicit_query", strings.TrimSpace(req.Query) != "",
			"has_session_query", strings.TrimSpace(wisdev.ResolveSessionSearchQuery(req.Session.Query, req.Session.CorrectedQuery, req.Session.OriginalQuery)) != "",
			"has_plan_query", strings.TrimSpace(req.Plan.Query) != "",
			"has_stored_session_query", sessionQuery != "",
			"result", "missing_query",
		)
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	qualityMode := normalizeDeepResearchQualityMode(req.QualityMode)
	budget := getDeepResearchBudget(qualityMode)
	profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(wisdev.WisDevModeGuided), qualityMode, true, 0)
	domainHint := strings.TrimSpace(firstNonEmpty(req.DomainHint, req.Session.DetectedDomain, sessionDomain))
	logWisdevRouteLifecycle(r, "wisdev_deep_research_legacy", "request_received", query,
		"session_id", sessionID,
		"domain_hint", domainHint,
		"quality_mode", qualityMode,
		"category_count", len(req.Categories),
		"result", "accepted",
	)

	warnings := []string{}
	papers := []wisdev.Source{}
	var deepLoopResult *wisdev.LoopResult
	runtime := resolveUnifiedResearchRuntime(h.gateway)
	if runtime == nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev unified runtime is required for deep research", map[string]any{
			"error": "wisdev_unified_runtime_unavailable",
		})
		return
	}
	seedQueries := buildDeepResearchSeedQueries(query, req.Categories, domainHint)
	loopReq := wisdev.LoopRequest{
		Query:           query,
		SeedQueries:     seedQueries,
		Domain:          domainHint,
		ProjectID:       firstNonEmpty(sessionID, "deep_"+wisdev.NewTraceID()),
		MaxIterations:   profile.MaxIterations,
		MaxSearchTerms:  profile.SearchBudget.MaxSearchTerms,
		HitsPerSearch:   profile.SearchBudget.HitsPerSearch,
		MaxUniquePapers: profile.SearchBudget.MaxUniquePapers,
		AllocatedTokens: profile.AllocatedTokens,
		Mode:            string(profile.Mode),
	}
	traceEmitter := buildResearchLoopTraceEmitter(h.gateway, loopReq.ProjectID, GetUserID(r), "legacyDeepResearch", wisdev.ResearchExecutionPlaneDeep, resolveWisdevRouteTraceID(r, ""), query)
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
	applyLegacyResearchEnvelopeFields(
		payload,
		qualityMode,
		budget,
		warnings,
		"go_canonical_runtime",
	)
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		enrichResearchMetadataWithRuntimeState(metadata, deepLoopResult)
	}
	attachResearchEvidence(h.gateway, payload, "deep", sessionID, query, "", papers)
	logWisdevRouteLifecycle(r, "wisdev_deep_research_legacy", "response_ready", query,
		"session_id", sessionID,
		"domain_hint", domainHint,
		"quality_mode", qualityMode,
		"paper_count", len(papers),
		"warning_count", len(warnings),
		"result", "success",
	)
	writeEnvelope(w, "deepResearch", payload)
}

func (h *WisDevHandler) HandleAutonomousResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.gateway == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "agent gateway is not initialized", nil)
		return
	}

	var req struct {
		Query       string `json:"query"`
		SessionID   string `json:"sessionId"`
		QualityMode string `json:"quality_mode"`
		Session     struct {
			SessionID      string `json:"sessionId"`
			Query          string `json:"query"`
			OriginalQuery  string `json:"originalQuery"`
			CorrectedQuery string `json:"correctedQuery"`
			DetectedDomain string `json:"detectedDomain"`
		} `json:"session"`
		Plan struct {
			Query string `json:"query"`
		} `json:"plan"`
	}
	if err := decodeStrictJSONBody(r.Body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	sessionID := strings.TrimSpace(firstNonEmpty(req.SessionID, req.Session.SessionID))
	sessionQuery := ""
	sessionDomain := ""
	if sessionID != "" {
		if sessions := h.legacySessionManager(); sessions != nil {
			if session, err := sessions.GetSession(r.Context(), sessionID); err == nil && session != nil {
				sessionQuery = strings.TrimSpace(wisdev.ResolveSessionSearchQuery(session.Query, session.CorrectedQuery, session.OriginalQuery))
				sessionDomain = strings.TrimSpace(session.DetectedDomain)
			}
		}
	}

	query := strings.TrimSpace(firstNonEmpty(
		req.Query,
		wisdev.ResolveSessionSearchQuery(req.Session.Query, req.Session.CorrectedQuery, req.Session.OriginalQuery),
		req.Plan.Query,
		sessionQuery,
	))
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	domainHint := strings.TrimSpace(firstNonEmpty(req.Session.DetectedDomain, sessionDomain))
	qualityMode := normalizeDeepResearchQualityMode(req.QualityMode)
	budget := getDeepResearchBudget(qualityMode)
	profile := wisdev.BuildResearchExecutionProfile(r.Context(), query, string(wisdev.WisDevModeGuided), qualityMode, false, 0)

	warnings := []string{}
	papers := []wisdev.Source{}
	results := map[string]any(nil)
	loopBacked := false
	var autonomousLoopResult *wisdev.LoopResult
	runtime := resolveUnifiedResearchRuntime(h.gateway)
	if runtime == nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev unified runtime is required for autonomous research", map[string]any{
			"error": "wisdev_unified_runtime_unavailable",
		})
		return
	}
	loopReq := wisdev.LoopRequest{
		Query:           query,
		Domain:          domainHint,
		ProjectID:       firstNonEmpty(sessionID, "auto_"+wisdev.NewTraceID()),
		MaxIterations:   profile.MaxIterations,
		MaxSearchTerms:  profile.SearchBudget.MaxSearchTerms,
		HitsPerSearch:   profile.SearchBudget.HitsPerSearch,
		MaxUniquePapers: profile.SearchBudget.MaxUniquePapers,
		AllocatedTokens: profile.AllocatedTokens,
		Mode:            string(profile.Mode),
	}
	traceEmitter := buildResearchLoopTraceEmitter(h.gateway, loopReq.ProjectID, GetUserID(r), "legacyAutonomousResearch", wisdev.ResearchExecutionPlaneAutonomous, resolveWisdevRouteTraceID(r, ""), query)
	loopResult, loopErr := runUnifiedResearchLoop(r.Context(), runtime, wisdev.ResearchExecutionPlaneAutonomous, loopReq, traceEmitter)
	if loopErr != nil {
		writeWisdevResearchLoopError(w, "wisdev autonomous research loop failed", loopErr)
		return
	}
	if loopResult == nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "wisdev autonomous research loop returned no result", map[string]any{
			"error": "wisdev_unified_runtime_empty",
		})
		return
	}
	papers = searchPapersToWisdevSources(loopResult.Papers)
	results = buildAutonomousResearchLoopPayload(query, domainHint, loopResult, nil, true)
	applyLegacyResearchEnvelopeFields(
		results,
		qualityMode,
		budget,
		warnings,
		"go_canonical_runtime",
	)
	if hypothesisPayloads := buildAutonomousHypothesisPayloadsFromLoop(query, loopResult); len(hypothesisPayloads) > 0 {
		results["hypotheses"] = hypothesisPayloads
	}
	loopBacked = true
	autonomousLoopResult = loopResult
	attachResearchEvidence(h.gateway, results, "auto", sessionID, query, "", papers)
	if metadata, ok := results["metadata"].(map[string]any); ok {
		metadata["loopBacked"] = loopBacked
		enrichResearchMetadataWithRuntimeState(metadata, autonomousLoopResult)
	}
	writeEnvelope(w, "autonomousResearch", results)
}
