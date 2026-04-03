package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/wisdev"
	"strings"
	"time"

)

type WisDevHandler struct {
	sessions     *wisdev.SessionManager
	guided       *wisdev.GuidedFlow
	autonomous   *wisdev.AutonomousWorker
	gateway      *wisdev.AgentGateway
	brainCaps    *wisdev.BrainCapabilities
	compiler     *wisdev.Paper2SkillCompiler
}

type deepResearchBudget struct {
	qualityMode     string
	maxSearchTerms  int
	hitsPerSearch   int
	maxUniquePapers int
}

func NewWisDevHandler(sessions *wisdev.SessionManager, guided *wisdev.GuidedFlow, autonomous *wisdev.AutonomousWorker, gateway *wisdev.AgentGateway, brainCaps *wisdev.BrainCapabilities, compiler *wisdev.Paper2SkillCompiler) *WisDevHandler {
	return &WisDevHandler{
		sessions:   sessions,
		guided:     guided,
		autonomous: autonomous,
		gateway:    gateway,
		brainCaps:  brainCaps,
		compiler:   compiler,
	}
}

func normalizeDeepResearchQualityMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fast":
		return "fast"
	case "quality", "high", "deep", "thorough":
		return "quality"
	default:
		return "balanced"
	}
}

func getDeepResearchBudget(qualityMode string) deepResearchBudget {
	switch normalizeDeepResearchQualityMode(qualityMode) {
	case "fast":
		return deepResearchBudget{
			qualityMode:     "fast",
			maxSearchTerms:  2,
			hitsPerSearch:   4,
			maxUniquePapers: 12,
		}
	case "quality":
		return deepResearchBudget{
			qualityMode:     "quality",
			maxSearchTerms:  8,
			hitsPerSearch:   12,
			maxUniquePapers: 48,
		}
	default:
		return deepResearchBudget{
			qualityMode:     "balanced",
			maxSearchTerms:  4,
			hitsPerSearch:   8,
			maxUniquePapers: 24,
		}
	}
}

func (h *WisDevHandler) journalEvent(
	eventType string,
	path string,
	traceID string,
	sessionID string,
	userID string,
	planID string,
	stepID string,
	summary string,
	payload map[string]any,
	metadata map[string]any,
) {
	if h.gateway == nil || h.gateway.Journal == nil {
		return
	}
	h.gateway.Journal.Append(wisdev.RuntimeJournalEntry{
		EventID:   wisdev.NewTraceID(),
		TraceID:   traceID,
		SessionID: strings.TrimSpace(sessionID),
		UserID:    strings.TrimSpace(userID),
		PlanID:    strings.TrimSpace(planID),
		StepID:    strings.TrimSpace(stepID),
		EventType: eventType,
		Path:      path,
		Status:    "ok",
		CreatedAt: time.Now().UnixMilli(),
		Summary:   summary,
		Payload:   cloneAnyMap(payload),
		Metadata:  cloneAnyMap(metadata),
	})
}

func (h *WisDevHandler) HandleDeepResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.gateway == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "gateway is not initialized", nil)
		return
	}

	var req struct {
		Query               string   `json:"query"`
		Categories          []string `json:"categories"`
		IncludeDomains      []string `json:"include_domains"`
		IncludeDomainsCamel []string `json:"includeDomains"`
		QualityMode         string   `json:"quality_mode"`
		QualityModeCamel    string   `json:"qualityMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	includeDomains := req.IncludeDomains
	if len(includeDomains) == 0 {
		includeDomains = req.IncludeDomainsCamel
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}
	qualityMode := req.QualityMode
	if strings.TrimSpace(qualityMode) == "" {
		qualityMode = req.QualityModeCamel
	}
	budget := getDeepResearchBudget(qualityMode)

	var payload map[string]any
	var searchPasses int
	err := withConcurrencyGuard("deep_research", wisdev.EnvInt("WISDEV_DEEP_RESEARCH_CONCURRENCY", 4), func() error {
		searchTerms := []string{query}
		for _, category := range req.Categories {
			category = strings.TrimSpace(strings.ReplaceAll(category, "_", " "))
			if category != "" {
				searchTerms = append(searchTerms, fmt.Sprintf("%s %s", query, category))
			}
		}
		if len(includeDomains) > 0 {
			searchTerms = append(searchTerms, fmt.Sprintf("%s %s", query, strings.Join(includeDomains, " ")))
		}
		searchTerms = uniqueStrings(searchTerms)
		if len(searchTerms) > budget.maxSearchTerms {
			searchTerms = searchTerms[:budget.maxSearchTerms]
		}
		searchPasses = len(searchTerms)

		papers := make([]wisdev.Source, 0, budget.maxUniquePapers)
		seen := map[string]struct{}{}
		for _, term := range searchTerms {
			hits, err := wisdev.FastParallelSearch(r.Context(), h.gateway.Redis, term, budget.hitsPerSearch)
			if err != nil {
				continue
			}
			for _, hit := range hits {
				key := strings.TrimSpace(hit.ID + "|" + hit.Title)
				if key == "|" {
					key = strings.TrimSpace(hit.Title)
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				papers = append(papers, hit)
				if len(papers) >= budget.maxUniquePapers {
					break
				}
			}
			if len(papers) >= budget.maxUniquePapers {
				break
			}
		}
		if len(papers) == 0 {
			return fmt.Errorf("no sources retrieved for deep research")
		}

		domainHint := ""
		if len(includeDomains) > 0 {
			domainHint = strings.Join(includeDomains, ",")
		}
		payload = buildDeepResearchPayload(query, req.Categories, domainHint, papers)
		return nil
	})
	if err != nil {
		code := ErrWisdevFailed
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "concurrency limit reached") {
			code = ErrConcurrencyLimit
			status = http.StatusTooManyRequests
		}
		WriteError(w, status, code, "deep research failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	traceID := writeV2Envelope(w, "deepResearch", payload)
	h.journalEvent("deep_research", r.URL.Path, traceID, "", "", "", "", "Deep research completed with multi-pass retrieval.", payload, map[string]any{
		"query":           query,
		"categoryCount":   len(req.Categories),
		"searchPasses":    searchPasses,
		"qualityMode":     budget.qualityMode,
		"hitsPerSearch":   budget.hitsPerSearch,
		"maxUniquePapers": budget.maxUniquePapers,
		"maxSearchTerms":  budget.maxSearchTerms,
	})
}

func (h *WisDevHandler) HandleAutonomousResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.gateway == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "gateway is not initialized", nil)
		return
	}

	var req struct {
		Session map[string]any `json:"session"`
		Plan    map[string]any `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	query := strings.TrimSpace(fmt.Sprintf("%v", req.Session["correctedQuery"]))
	if query == "" {
		query = strings.TrimSpace(fmt.Sprintf("%v", req.Session["originalQuery"]))
	}
	if query == "" {
		query = strings.TrimSpace(fmt.Sprintf("%v", req.Plan["query"]))
	}
	if query == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "session.correctedQuery or equivalent query is required", nil)
		return
	}

	papers, err := wisdev.FastParallelSearch(r.Context(), h.gateway.Redis, query, 8)
	warnings := make([]map[string]any, 0, 1)
	if err != nil {
		warnings = append(warnings, map[string]any{
			"code":    "AUTONOMOUS_SEARCH_DEGRADED",
			"message": err.Error(),
		})
		papers = nil
	}

	payload := map[string]any{
		"artifacts": []map[string]any{
			{
				"type":    "source_bundle",
				"summary": fmt.Sprintf("Autonomous research assembled %d wisdev.Source(s).", len(papers)),
			},
		},
		"prismaReport": map[string]any{
			"screened": len(papers),
			"included": len(papers),
		},
		"gaps": []map[string]any{
			{
				"type":    "coverage_gap",
				"summary": "Need stronger contradiction scan.",
			},
		},
		"hypotheses": []map[string]any{
			{
				"title":      fmt.Sprintf("Initial hypothesis for %s", query),
				"confidence": wisdev.ClampFloat(0.7, 0.5, 0.95),
			},
		},
		"coverageMap": map[string]any{
			"primary_query": buildCommitteePapers(papers),
		},
		"executionMs": 0,
		"warnings":    warnings,
		"iterations": []map[string]any{
			{"phase": "retrieve", "status": "completed"},
			{"phase": "synthesize", "status": "completed"},
		},
	}

	traceID := writeV2Envelope(w, "autonomousResearch", payload)
	h.journalEvent("autonomous_research", r.URL.Path, traceID, "", "", "", "", "Autonomous research completed.", payload, map[string]any{"query": query})
}

func (h *WisDevHandler) HandleInitialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		UserID        string `json:"userId"`
		OriginalQuery string `json:"originalQuery"`
		Query         string `json:"query"` // alias accepted from frontend
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	// Accept either originalQuery or query (frontend sends query)
	if strings.TrimSpace(req.OriginalQuery) == "" {
		req.OriginalQuery = strings.TrimSpace(req.Query)
	}
	if req.OriginalQuery == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
			"field": "query",
		})
		return
	}

	session, err := h.sessions.CreateSession(r.Context(), req.UserID, req.OriginalQuery)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to initialize session", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func (h *WisDevHandler) HandleGetSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	session, err := h.sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func (h *WisDevHandler) HandleProcessAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		SessionID     string   `json:"sessionId"`
		QuestionID    string   `json:"questionId"`
		Values        []string `json:"values"`
		DisplayValues []string `json:"displayValues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	session, err := h.sessions.GetSession(r.Context(), req.SessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": req.SessionID,
		})
		return
	}

	answer := wisdev.Answer{
		QuestionID:    req.QuestionID,
		Values:        req.Values,
		DisplayValues: req.DisplayValues,
		AnsweredAt:    time.Now().UnixMilli(),
	}

	if err := h.guided.ProcessAnswer(r.Context(), session, answer); err != nil {
		// If the session's guided flow is already complete, treat extra answers as no-ops
		// and return the current session state rather than erroring.
		errMsg := err.Error()
		if strings.Contains(errMsg, "no more questions") || strings.Contains(errMsg, "already complete") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(session)
			return
		}
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "failed to process answer", map[string]any{
			"error": errMsg,
		})
		return
	}

	if err := h.sessions.SaveSession(r.Context(), session); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to save session", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func (h *WisDevHandler) HandleNextQuestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	session, err := h.sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	question, ok := h.guided.GetNextQuestion(session)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(question)
}

func (h *WisDevHandler) HandleGenerateQueries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	session, err := h.sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	resp := wisdev.GenerateSearchQueries(session)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *WisDevHandler) HandleCompleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	session, err := h.sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	session.Status = wisdev.StatusComplete
	if err := h.sessions.SaveSession(r.Context(), session); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to save session", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(session)
}

func (h *WisDevHandler) HandleAbandonSession(w http.ResponseWriter, r *http.Request) {
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
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	session, err := h.sessions.GetSession(r.Context(), req.SessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": req.SessionID,
		})
		return
	}
	session.Status = wisdev.StatusAbandoned
	if err := h.sessions.SaveSession(r.Context(), session); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to save session", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"session": session,
	})
}

func (h *WisDevHandler) HandleQuestionOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
	if sessionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
			"field": "sessionId",
		})
		return
	}

	// Empty questionId: return empty options rather than erroring
	if questionID == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"questionId": "",
			"options":    []any{},
		})
		return
	}

	if _, err := h.sessions.GetSession(r.Context(), sessionID); err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	questionMap := make(map[string]wisdev.Question, len(h.guided.Questions))
	for _, question := range h.guided.Questions {
		questionMap[question.ID] = question
	}
	question, ok := questionMap[questionID]
	if !ok {
		// Unknown questionId: return empty options gracefully
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"questionId": questionID,
			"options":    []any{},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"questionId": questionID,
		"options":    question.Options,
	})
}

func (h *WisDevHandler) HandleQuestionRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
	if sessionID == "" || questionID == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
			"fields": []string{"sessionId", "questionId"},
		})
		return
	}

	session, err := h.sessions.GetSession(r.Context(), sessionID)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrNotFound, "session not found", map[string]any{
			"sessionId": sessionID,
		})
		return
	}

	questionMap := make(map[string]wisdev.Question, len(h.guided.Questions))
	for _, question := range h.guided.Questions {
		questionMap[question.ID] = question
	}
	question, ok := questionMap[questionID]
	if !ok {
		WriteError(w, http.StatusNotFound, ErrNotFound, "question not found", map[string]any{
			"questionId": questionID,
		})
		return
	}

	values := make([]string, 0, 2)
	if answer, ok := session.Answers[questionID]; ok && len(answer.Values) > 0 {
		values = append(values, answer.Values...)
	}
	if len(values) == 0 && len(question.Options) > 0 {
		limit := 1
		if question.IsMultiSelect {
			limit = min(3, len(question.Options))
		}
		for i := 0; i < limit; i++ {
			values = append(values, question.Options[i].Value)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"questionId":  questionID,
		"values":      values,
		"explanation": "",
		"source":      "heuristic",
	})
}

func (h *WisDevHandler) HandleRegenerateQuestionOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		SessionID  string `json:"sessionId"`
		QuestionID string `json:"questionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.QuestionID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
			"fields": []string{"sessionId", "questionId"},
		})
		return
	}

	r.URL.RawQuery = "sessionId=" + req.SessionID + "&questionId=" + req.QuestionID
	h.HandleQuestionOptions(w, r)
}

// HandleDecomposeTask uses BrainCapabilities to break a research query into a
// parallelizable DAG of typed research tasks (heavy / balanced / light tiers).
func (h *WisDevHandler) HandleDecomposeTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		Query  string `json:"query"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	tasks, err := h.brainCaps.DecomposeTask(r.Context(), req.Query, req.Domain, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to decompose task", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
}

// HandleProposeHypotheses uses BrainCapabilities to generate 3-5 falsifiable
// research hypotheses with confidence thresholds for the given query.
func (h *WisDevHandler) HandleProposeHypotheses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		Query  string `json:"query"`
		Intent string `json:"intent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	hypotheses, err := h.brainCaps.ProposeHypotheses(r.Context(), req.Query, req.Intent, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to propose hypotheses", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"hypotheses": hypotheses})
}

// HandleCoordinateReplan uses BrainCapabilities to recover from a failed DAG step
// by producing replacement tasks (retry / skip / pivot strategy).
func (h *WisDevHandler) HandleCoordinateReplan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.brainCaps == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "brain capabilities are not initialized", nil)
		return
	}

	var req struct {
		FailedStepID string         `json:"failedStepId"`
		Reason       string         `json:"reason"`
		Context      map[string]any `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.FailedStepID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "failedStepId is required", map[string]any{
			"field": "failedStepId",
		})
		return
	}

	tasks, err := h.brainCaps.CoordinateReplan(r.Context(), req.FailedStepID, req.Reason, req.Context, "")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to coordinate replan", map[string]any{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"tasks": tasks})
}

// HandleGetTraces returns journal trace entries for a session or recent user activity.
// Query params:
//   - sessionId (optional): return entries for a specific session
//   - userId    (optional): scope recent-outcomes to a user
//   - limit     (optional, default 50): cap returned entries
func (h *WisDevHandler) HandleGetTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodGet,
		})
		return
	}

	// No journal available — return empty slice without error so the frontend
	// degrades gracefully (it only uses traces for debug display).
	if h.gateway == nil || h.gateway.Journal == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	userID := strings.TrimSpace(r.URL.Query().Get("userId"))
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := fmt.Sscanf(raw, "%d", &limit); n != 1 || err != nil || limit <= 0 {
			limit = 50
		}
	}
	if limit > 200 {
		limit = 200
	}

	var result any
	if sessionID != "" {
		entries := h.gateway.Journal.ReadSession(sessionID, limit)
		if entries == nil {
			entries = []wisdev.RuntimeJournalEntry{}
		}
		result = entries
	} else {
		summary := h.gateway.Journal.SummarizeRecentOutcomes(userID, limit)
		result = summary
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleAnalyzeQuery provides basic query analysis for the frontend's
// initializeWisDevFlow path. Returns a structural analysis of the query
// without requiring an LLM call, matching the Python fallback response shape.
func (h *WisDevHandler) HandleAnalyzeQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}

	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
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

	words := strings.Fields(query)
	entities := words
	if len(entities) > 5 {
		entities = entities[:5]
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"intent":             "broad_topic",
		"entities":           entities,
		"research_questions": []string{fmt.Sprintf("What is known about %s?", query)},
		"complexity":         "moderate",
		"ambiguity_score":    0.5,
		"suggested_domains":  []string{"general"},
		"methodology_hints":  []string{},
		"reasoning":          "Go-primary structural extraction",
		"cache_hit":          false,
	})
}

// HandlePaper2Skill handles POST /v2/wisdev/paper2skill.
func (h *WisDevHandler) HandlePaper2Skill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
			"allowedMethod": http.MethodPost,
		})
		return
	}
	if h.compiler == nil {
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, "compiler is not initialized", nil)
		return
	}

	var req struct {
		ArxivID string `json:"arxiv_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if strings.TrimSpace(req.ArxivID) == "" {
		WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "arxiv_id is required", map[string]any{
			"field": "arxiv_id",
		})
		return
	}

	// In production, call Paper2SkillCompiler
	schema, err := h.compiler.CompileArxivID(r.Context(), req.ArxivID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to compile skill from paper", map[string]any{
			"error":   err.Error(),
			"arxivId": req.ArxivID,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":   "completed",
		"arxiv_id": req.ArxivID,
		"skill":    schema,
	})
}
