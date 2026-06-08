package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

var wisdevAnalyzeQueryHandlerTimeout = 20 * time.Second
var wisdevQuestionRecommendationTimeout = 25 * time.Second

type dynamicQuestionOptionsResult struct {
	options     []any
	source      string
	explanation string
}

type dynamicQuestionOptionsInflightCall struct {
	done   chan struct{}
	result dynamicQuestionOptionsResult
}

func dynamicQuestionOptionsSingleflightKey(sessionID string, questionID string, session map[string]any) string {
	normalizedSessionID := strings.TrimSpace(sessionID)
	normalizedQuestionID := strings.TrimSpace(questionID)
	optionContext := "default"
	if normalizedQuestionID == "q5_study_types" {
		subtopics := uniqueStrings(answeredAgentQuestionValues(session, "q4_subtopics"))
		if len(subtopics) > 0 {
			sort.Strings(subtopics)
			optionContext = "q4:" + strings.Join(subtopics, "|")
		} else {
			optionContext = "speculative"
		}
	}
	return normalizedSessionID + ":" + normalizedQuestionID + ":" + optionContext
}

// wisdevAnalyzeQueryBudget returns an adaptive handler timeout that accounts
// for cold-start conditions. During the cold-start window, sidecar
// initialization and ADC token acquisition can consume 5-15s, so the budget
// is extended to prevent premature heuristic fallbacks.
func wisdevAnalyzeQueryBudget() time.Duration {
	if llm.IsColdStartWindow() {
		return wisdevAnalyzeQueryHandlerTimeout + 20*time.Second
	}
	return wisdevAnalyzeQueryHandlerTimeout
}

func defaultEvidenceQualityOptions(query string, domain string) []string {
	text := strings.ToLower(strings.TrimSpace(query + " " + domain))
	options := []string{
		"Peer-reviewed evidence",
		"Transparent methods and data",
		"Replication or validation evidence",
		"High citation signal",
		"Recent evidence",
		"Open data or code availability",
	}
	if strings.Contains(text, "clinical") || strings.Contains(text, "medicine") || strings.Contains(text, "health") {
		options = append([]string{"Randomized or controlled evidence", "Systematic review evidence"}, options...)
	}
	if strings.Contains(text, "benchmark") || strings.Contains(text, "model") || strings.Contains(text, "ai") || strings.Contains(text, "computer") {
		options = append([]string{"Reproducible benchmarks", "Ablation-backed evidence"}, options...)
	}
	return uniqueStrings(options)
}

func defaultOutputFocusOptions(query string, domain string) []string {
	text := strings.ToLower(strings.TrimSpace(query + " " + domain))
	options := []string{
		"Best papers first",
		"Broad coverage map",
		"Evidence gaps and limitations",
		"Method comparison",
		"Contradictions and disagreements",
		"Practical implications",
	}
	if strings.Contains(text, "benchmark") || strings.Contains(text, "compare") || strings.Contains(text, "model") {
		options = append([]string{"Benchmark comparison", "Method tradeoffs"}, options...)
	}
	if strings.Contains(text, "clinical") || strings.Contains(text, "medicine") || strings.Contains(text, "health") {
		options = append([]string{"Clinical relevance", "Safety and adverse effects"}, options...)
	}
	return uniqueStrings(options)
}

func normalizeQuestionOptionValue(label string) string {
	value := strings.ToLower(strings.TrimSpace(label))
	replacer := strings.NewReplacer("&", " and ", "/", " ", "-", " ", "(", " ", ")", " ", ",", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), "_")
}

func (s *wisdevServer) registerQuestioningRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	var questionRecommendationBrainMu sync.Mutex
	var dynamicOptionInflightMu sync.Mutex
	dynamicOptionInflight := map[string]*dynamicQuestionOptionsInflightCall{}

	buildDynamicQuestionOptions := func(ctx context.Context, session map[string]any, questionID string, previousOptions []string) ([]any, string, string) {
		if len(session) == 0 {
			return nil, "", ""
		}
		query := wisdev.ResolveSessionQueryText(
			wisdev.AsOptionalString(session["correctedQuery"]),
			wisdev.AsOptionalString(session["originalQuery"]),
		)
		domain := wisdev.AsOptionalString(session["detectedDomain"])
		switch strings.TrimSpace(questionID) {
		case "q4_subtopics":
			subtopics, _, _, source, explanation := buildSubtopicsResponseWithExclusions(ctx, agentGateway, query, domain, 8, previousOptions)
			options := make([]any, 0, len(subtopics))
			for _, subtopic := range subtopics {
				options = append(options, map[string]any{"value": subtopic, "label": subtopic})
			}
			return options, source, explanation
		case "q5_study_types":
			existingSubtopics := answeredAgentQuestionValues(session, "q4_subtopics")
			studyTypes, _, source, explanation := buildStudyTypesResponseWithExclusions(ctx, agentGateway, query, domain, existingSubtopics, 6, previousOptions)
			options := make([]any, 0, len(studyTypes))
			for _, studyType := range studyTypes {
				options = append(options, map[string]any{"value": studyType, "label": studyType})
			}
			return options, source, explanation
		case "q7_evidence_quality":
			qualityBars := avoidRepeatedDynamicOptions(defaultEvidenceQualityOptions(query, domain), previousOptions, 4, func(selected []string) []string {
				return defaultEvidenceQualityOptions(query+" "+strings.Join(selected, " "), domain)
			})
			options := make([]any, 0, len(qualityBars))
			for _, qualityBar := range qualityBars {
				options = append(options, map[string]any{"value": normalizeQuestionOptionValue(qualityBar), "label": qualityBar})
			}
			return options, "heuristic", "WisDev refreshed evidence-quality options from the current query and domain."
		case "q8_output_focus":
			outputFocus := avoidRepeatedDynamicOptions(defaultOutputFocusOptions(query, domain), previousOptions, 4, func(selected []string) []string {
				return defaultOutputFocusOptions(query+" "+strings.Join(selected, " "), domain)
			})
			options := make([]any, 0, len(outputFocus))
			for _, focus := range outputFocus {
				options = append(options, map[string]any{"value": normalizeQuestionOptionValue(focus), "label": focus})
			}
			return options, "heuristic", "WisDev refreshed output-focus options from the current query and domain."
		default:
			return nil, "", ""
		}
	}

	buildDynamicQuestionOptionsOnce := func(ctx context.Context, sessionID string, session map[string]any, questionID string, previousOptions []string) ([]any, string, string) {
		if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(questionID) == "" || len(previousOptions) > 0 {
			return buildDynamicQuestionOptions(ctx, session, questionID, previousOptions)
		}
		key := dynamicQuestionOptionsSingleflightKey(sessionID, questionID, session)

		dynamicOptionInflightMu.Lock()
		if call, ok := dynamicOptionInflight[key]; ok {
			dynamicOptionInflightMu.Unlock()
			select {
			case <-call.done:
				return call.result.options, call.result.source, call.result.explanation
			case <-ctx.Done():
				return nil, "options_unavailable", "Timed out while waiting for dynamic option generation already in progress."
			}
		}
		call := &dynamicQuestionOptionsInflightCall{done: make(chan struct{})}
		dynamicOptionInflight[key] = call
		dynamicOptionInflightMu.Unlock()

		call.result.options, call.result.source, call.result.explanation = buildDynamicQuestionOptions(ctx, session, questionID, nil)

		dynamicOptionInflightMu.Lock()
		delete(dynamicOptionInflight, key)
		dynamicOptionInflightMu.Unlock()
		close(call.done)
		return call.result.options, call.result.source, call.result.explanation
	}

	patchDynamicQuestionOptions := func(session map[string]any, questionID string, options []any, source string, explanation string, overwrite bool) bool {
		if len(session) == 0 || len(options) == 0 {
			return false
		}
		questions := sliceAnyMap(session["questions"])
		if len(questions) == 0 {
			return false
		}
		patched := false
		for i, question := range questions {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			if len(questionOptionValues(question["options"])) > 0 && !overwrite {
				break
			}
			questions[i]["options"] = options
			questions[i]["optionsSource"] = source
			questions[i]["optionsExplanation"] = explanation
			patched = true
			break
		}
		if patched {
			session["questions"] = questions
		}
		return patched
	}

	persistDynamicQuestionOptions := func(sessionID, userID, questionID string, options []any, source string, explanation string, overwrite bool) {
		if agentGateway == nil || agentGateway.StateStore == nil {
			return
		}
		latest, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			return
		}
		if !patchDynamicQuestionOptions(latest, questionID, options, source, explanation, overwrite) {
			return
		}
		previousUpdatedAt := wisdev.IntValue64(latest["updatedAt"])
		nextUpdatedAt := time.Now().UnixMilli()
		if nextUpdatedAt <= previousUpdatedAt {
			nextUpdatedAt = previousUpdatedAt + 1
		}
		latest["updatedAt"] = nextUpdatedAt
		_ = agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, latest, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			SessionID: sessionID,
			UserID:    userID,
			StepID:    questionID,
			EventType: "agent_session_options_patch",
			Status:    "completed",
			Summary:   "Question options patched via dynamic generation.",
			Payload: map[string]any{
				"questionId":  questionID,
				"optionCount": len(options),
				"overwrite":   overwrite,
				"source":      source,
			},
		})
	}

	appendQuestionRouteJournalEntry := func(entry wisdev.RuntimeJournalEntry) {
		if agentGateway == nil || agentGateway.Journal == nil {
			return
		}
		if strings.TrimSpace(entry.EventID) == "" {
			entry.EventID = wisdev.NewTraceID()
		}
		if strings.TrimSpace(entry.TraceID) == "" {
			entry.TraceID = wisdev.NewTraceID()
		}
		if entry.CreatedAt == 0 {
			entry.CreatedAt = time.Now().UnixMilli()
		}
		if strings.TrimSpace(entry.Status) == "" {
			entry.Status = "completed"
		}
		agentGateway.Journal.Append(entry)
	}

	ensureQuestionRecommendationBrain := func() *wisdev.BrainCapabilities {
		if agentGateway == nil {
			return nil
		}
		if agentGateway.Brain != nil {
			return agentGateway.Brain
		}
		if agentGateway.LLMClient == nil {
			return nil
		}

		questionRecommendationBrainMu.Lock()
		defer questionRecommendationBrainMu.Unlock()

		if agentGateway.Brain == nil && agentGateway.LLMClient != nil {
			agentGateway.Brain = wisdev.NewBrainCapabilities(agentGateway.LLMClient)
			slog.Info("wisdev question recommendations brain initialised",
				"component", "api.wisdev",
				"operation", "question_recommendations",
				"stage", "brain_initialised",
			)
		}
		return agentGateway.Brain
	}

	logQuestionRecommendationFallback := func(r *http.Request, sessionID string, questionID string, stage string, attrs ...any) {
		base := []any{
			"component", "api.wisdev",
			"operation", "question_recommendations",
			"stage", stage,
			"path", r.URL.Path,
			"session_id", sessionID,
			"question_id", questionID,
			"fallback_source", "heuristic",
		}
		base = append(base, attrs...)
		slog.Warn("wisdev question recommendations fallback", base...)
	}

	describeQuestionRecommendationFallback := func(stage string) string {
		switch strings.TrimSpace(stage) {
		case "ai_request_failed":
			return "AI option ranking was unavailable, so WisDev used fallback recommendations from the current option set."
		case "ai_unavailable":
			return "AI option ranking is not configured for this request, so WisDev used fallback recommendations from the current option set."
		case "ai_empty_response":
			return "AI option ranking returned no usable matches, so WisDev used fallback recommendations from the current option set."
		case "options_unavailable":
			return "No dynamic options were available to rank for this question."
		default:
			return ""
		}
	}

	deriveQuestionOptionFallback := func(source string) (bool, string) {
		switch strings.TrimSpace(strings.ToLower(source)) {
		case "heuristic", "heuristic_fallback", "fallback":
			return true, "heuristic_fallback"
		case "ai_request_failed", "ai_unavailable", "ai_empty_response", "options_unavailable":
			normalized := strings.TrimSpace(strings.ToLower(source))
			return true, normalized
		default:
			return false, ""
		}
	}

	writeQuestionOptionResponse := func(w http.ResponseWriter, questionID string, options any, source string, explanation string) {
		fallbackTriggered, fallbackReason := deriveQuestionOptionFallback(source)
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"questionId":        questionID,
			"options":           options,
			"source":            source,
			"explanation":       explanation,
			"fallbackTriggered": fallbackTriggered,
			"fallbackReason":    fallbackReason,
		})
	}

	requireQuestioningStateStore := func(w http.ResponseWriter, r *http.Request, operation string) bool {
		reason := ""
		message := ""
		switch {
		case agentGateway == nil:
			reason = "agent_gateway_unavailable"
			message = "agent gateway is not initialized"
		case agentGateway.StateStore == nil:
			reason = "state_store_unavailable"
			message = "runtime state store unavailable"
		default:
			return true
		}

		logWisdevRouteError(r, "wisdev "+operation+" unavailable",
			"operation", operation,
			"reason", reason,
		)
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, message, map[string]any{
			"operation": operation,
			"reason":    reason,
		})
		return false
	}

	handleProcessAnswer := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID         string   `json:"sessionId"`
			QuestionID        string   `json:"questionId"`
			Values            []string `json:"values"`
			DisplayValues     []string `json:"displayValues"`
			Proceed           bool     `json:"proceed"`
			ExpectedUpdatedAt int64    `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev process answer decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := validateRequiredString(req.QuestionID, "questionId", 80); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "questionId",
			})
			return
		}
		if err := validateStringSlice(req.Values, "values", 8, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "values",
			})
			return
		}
		if err := validateStringSlice(req.DisplayValues, "displayValues", 8, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "displayValues",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "process_answer") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev process answer load failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		questionID := strings.TrimSpace(req.QuestionID)
		normalizedValues, normalizedDisplayValues := normalizeAgentQuestionAnswerValues(session, questionID, req.Values, req.DisplayValues)
		idempotencyKey := makeAgentAnswerIdempotencyKey(
			req.SessionID,
			questionID,
			normalizedValues,
			normalizedDisplayValues,
			req.Proceed,
			req.ExpectedUpdatedAt,
		)
		if status, cached, ok := enforceIdempotency(r, agentGateway, idempotencyKey); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}
		if req.ExpectedUpdatedAt > 0 &&
			wisdev.IntValue64(session["updatedAt"]) != req.ExpectedUpdatedAt &&
			agentAnswerAlreadyApplied(session, questionID, normalizedValues, normalizedDisplayValues) {
			traceID := wisdev.NewTraceID()
			responseBody := buildAgentQuestioningEnvelopeBody(traceID, session, false)
			if body, err := json.Marshal(responseBody); err == nil {
				storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Trace-Id", traceID)
			_ = json.NewEncoder(w).Encode(responseBody)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, session) {
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			logWisdevRouteError(r, "wisdev process answer rejected immutable session",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		pendingFollowUp := getPendingAgentFollowUpQuestion(session)
		isPendingFollowUpAnswer := len(pendingFollowUp) > 0 &&
			questionID == strings.TrimSpace(wisdev.AsOptionalString(pendingFollowUp["id"]))
		if isAgentQuestionRequired(session, questionID) && !hasNonEmptyAnswerValues(normalizedValues) {
			logWisdevRouteError(r, "wisdev process answer rejected empty required answer",
				"session_id", req.SessionID,
				"question_id", questionID,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "required question answer must include at least one value", map[string]any{
				"field":      "values",
				"sessionId":  req.SessionID,
				"questionId": questionID,
			})
			return
		}
		answers := mapAny(session["answers"])
		answers[questionID] = map[string]any{
			"questionId":    questionID,
			"values":        normalizedValues,
			"displayValues": normalizedDisplayValues,
			"answeredAt":    time.Now().UTC().Format(time.RFC3339),
		}
		session["answers"] = answers
		if questionID == "q1_domain" {
			replanAgentSessionForDomainAnswer(session)
		}
		if isPendingFollowUpAnswer {
			mirrorPendingAgentFollowUpAnswer(session, pendingFollowUp, normalizedValues, normalizedDisplayValues)
		}
		nextIndex := wisdev.IntValue(session["currentQuestionIndex"])
		if !isPendingFollowUpAnswer {
			nextIndex += 1
		}
		stopReason := wisdev.QuestionStopReasonNone
		status := string(wisdev.SessionQuestioning)
		if isPendingFollowUpAnswer {
			clearPendingAgentFollowUpQuestion(session)
		}
		if req.Proceed {
			stopReason = wisdev.QuestionStopReasonUserProceed
			status = "ready"
		} else if canonical := buildCanonicalAgentSession(session); canonical != nil {
			shouldStop, reason := wisdev.ShouldStopQuestioning(canonical)
			if shouldStop {
				stopReason = reason
				status = "ready"
			}
			nextQuestionID := strings.TrimSpace(wisdev.FindNextQuestionID(canonical))
			if nextQuestionID == "" {
				nextIndex = len(sliceStrings(session["questionSequence"]))
			} else {
				for index, questionID := range sliceStrings(session["questionSequence"]) {
					if questionID == nextQuestionID {
						nextIndex = index
						break
					}
				}
			}
		}
		if len(getPendingAgentFollowUpQuestion(session)) > 0 {
			session["questionStopReason"] = ""
			session["status"] = string(wisdev.SessionQuestioning)
		} else if status == "ready" {
			session["questionStopReason"] = string(stopReason)
			session["status"] = "ready"
		} else {
			session["questionStopReason"] = ""
			session["status"] = status
		}
		session["currentQuestionIndex"] = nextIndex
		session["updatedAt"] = time.Now().UnixMilli()
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(req.SessionID, wisdev.AsOptionalString(session["userId"]), session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    req.QuestionID,
			EventType: "agent_session_answer",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Your answer was saved for the next search pass.",
			Payload:   cloneAnyMap(session),
			Metadata:  map[string]any{"proceed": req.Proceed},
		}); err != nil {
			logWisdevRouteError(r, "wisdev process answer persist failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist processed answer", map[string]any{
				"error":      err.Error(),
				"sessionId":  req.SessionID,
				"questionId": req.QuestionID,
			})
			return
		}
		if err := syncCanonicalSessionStore(agentGateway, session); err != nil {
			logWisdevRouteError(r, "wisdev process answer canonical sync failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to sync canonical session", map[string]any{
				"error":     err.Error(),
				"sessionId": req.SessionID,
			})
			return
		}
		responseBody := buildAgentQuestioningEnvelopeBody(traceID, session, false)
		// Only cache the idempotent response when Marshal succeeds; a nil body
		// would cause subsequent replays to return an empty HTTP response.
		if body, err := json.Marshal(responseBody); err == nil {
			storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		_ = json.NewEncoder(w).Encode(responseBody)

		// Background pre-warm: generate dynamic options for the next dynamic
		// question(s) so that when the user arrives at q4/q5 the options are
		// already stored and the endpoint responds immediately without blocking
		// on an LLM call. Uses a detached context so it outlives the request.
		answeredID := strings.TrimSpace(req.QuestionID)
		if agentGateway != nil {
			sessionSnap := cloneAnyMap(session)
			sessionID := req.SessionID
			userID := wisdev.AsOptionalString(session["userId"])
			if answeredID == "q2_scope" || answeredID == "q3_timeframe" {
				// Pre-warm q4_subtopics then immediately chain into q5_study_types
				// in the same goroutine, so both questions are fully ready before
				// the user reaches them. q5 uses the q4 LLM output as subtopic
				// context so it gets contextually accurate study type options even
				// though the user hasn't answered q4 yet.
				q4HasOptions := func() bool {
					for _, q := range sliceAnyMap(sessionSnap["questions"]) {
						if wisdev.AsOptionalString(q["id"]) == "q4_subtopics" {
							return len(questionOptionValues(q["options"])) > 0
						}
					}
					return false
				}()
				if !q4HasOptions {
					go func() {
						// Budget covers two sequential LLM calls: q4 (~40s) + q5 (~40s)
						budget := 100 * time.Second
						if llm.IsColdStartWindow() {
							budget = 140 * time.Second
						}
						pwCtx, pwCancel := context.WithTimeout(context.Background(), budget)
						defer pwCancel()

						// Step 1: Generate q4 subtopic options via LLM.
						q4Opts, q4Src, q4Expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, sessionSnap, "q4_subtopics", nil)
						if len(q4Opts) > 0 {
							persistDynamicQuestionOptions(sessionID, userID, "q4_subtopics", q4Opts, q4Src, q4Expl, false)
							slog.Info("wisdev q4_subtopics options pre-warmed",
								"component", "api.wisdev",
								"operation", "prewarm_options",
								"stage", "completed",
								"session_id", sessionID,
								"triggered_by", answeredID,
								"option_count", len(q4Opts),
								"source", q4Src,
							)

							// Step 2: Chain q5 study types immediately using q4 generated
							// options as subtopic context (better than waiting for user's q4 answer).
							q5AlreadyStored := func() bool {
								latest, err := agentGateway.StateStore.LoadAgentSession(sessionID)
								if err != nil {
									return false
								}
								for _, q := range sliceAnyMap(latest["questions"]) {
									if wisdev.AsOptionalString(q["id"]) == "q5_study_types" {
										return len(questionOptionValues(q["options"])) > 0
									}
								}
								return false
							}()
							if !q5AlreadyStored {
								q4SubtopicValues := make([]string, 0, len(q4Opts))
								for _, opt := range q4Opts {
									if om, ok := opt.(map[string]any); ok {
										if v := wisdev.AsOptionalString(om["value"]); v != "" {
											q4SubtopicValues = append(q4SubtopicValues, v)
										}
									}
								}
								q5SessionSnap := cloneAnyMap(sessionSnap)
								answers := mapAny(q5SessionSnap["answers"])
								if answers == nil {
									answers = map[string]any{}
								}
								answers["q4_subtopics"] = map[string]any{
									"questionId": "q4_subtopics",
									"values":     q4SubtopicValues,
								}
								q5SessionSnap["answers"] = answers
								q5Opts, q5Src, q5Expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, q5SessionSnap, "q5_study_types", nil)
								if len(q5Opts) > 0 {
									if q5Src == "" {
										q5Src = "heuristic"
									}
									persistDynamicQuestionOptions(sessionID, userID, "q5_study_types", q5Opts, q5Src, q5Expl, false)
									slog.Info("wisdev q5_study_types options pre-warmed (chained from q4)",
										"component", "api.wisdev",
										"operation", "prewarm_options",
										"stage", "completed",
										"session_id", sessionID,
										"triggered_by", answeredID,
										"option_count", len(q5Opts),
										"source", q5Src,
									)
								}
							}
						}
					}()
				}
			} else if answeredID == "q4_subtopics" {
				// Re-warm q5_study_types using the user's actual q4 answer selections.
				// This overrides any previously pre-warmed q5 options so they reflect
				// the user's real subtopic choices rather than the LLM pre-generated ones.
				go func() {
					budget := 50 * time.Second
					if llm.IsColdStartWindow() {
						budget = 70 * time.Second
					}
					pwCtx, pwCancel := context.WithTimeout(context.Background(), budget)
					defer pwCancel()
					opts, src, expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, sessionSnap, "q5_study_types", nil)
					if len(opts) > 0 {
						persistDynamicQuestionOptions(sessionID, userID, "q5_study_types", opts, src, expl, true)
						slog.Info("wisdev q5_study_types options re-warmed from q4 selection",
							"component", "api.wisdev",
							"operation", "prewarm_options",
							"stage", "completed",
							"session_id", sessionID,
							"triggered_by", answeredID,
							"option_count", len(opts),
							"source", src,
						)
					}
				}()
			}
		}
	}
	for _, path := range wisdevAnswerPaths {
		mux.HandleFunc(path, handleProcessAnswer)
	}

	handleNextQuestion := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		var req struct {
			SessionID           string `json:"sessionId"`
			UseAdaptiveOrdering bool   `json:"useAdaptiveOrdering"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				logWisdevRouteError(r, "wisdev next question decode failed", "error", err)
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if !requireQuestioningStateStore(w, r, "next_question") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev next question load failed",
				"session_id", req.SessionID,
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			logWisdevRouteError(r, "wisdev next question rejected immutable session",
				"session_id", req.SessionID,
				"error", err,
			)
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		payload := buildAgentQuestionPayload(session, req.UseAdaptiveOrdering)
		traceID := wisdev.NewTraceID()
		appendQuestionRouteJournalEntry(wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    wisdev.AsOptionalString(payload["id"]),
			EventType: "agent_next_question",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Next agent question requested.",
			Payload:   cloneAnyMap(payload),
			Metadata:  map[string]any{"adaptive": req.UseAdaptiveOrdering},
		})
		writeEnvelopeWithTraceID(w, traceID, "question", payload)
	}
	for _, path := range wisdevQuestionNextPaths {
		mux.HandleFunc(path, handleNextQuestion)
	}

	handleSessionPreliminarySearch := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev preliminary search decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := validateRequiredString(req.SessionID, "sessionId", 120); err != nil {
			logWisdevRouteError(r, "wisdev preliminary search validation failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"error", err,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "sessionId",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "preliminary_search") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev preliminary search load failed", "session_id", strings.TrimSpace(req.SessionID), "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		requestUserID, authErr := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if authErr != nil {
			logWisdevRouteError(r, "wisdev preliminary search authorization failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"request_user_id", strings.TrimSpace(req.UserID),
				"error", authErr,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, authErr.Error(), nil)
			return
		}
		ownerID := wisdev.AsOptionalString(session["userId"])
		if requestUserID != ownerID && requestUserID != "admin" && requestUserID != "internal-service" {
			logWisdevRouteError(r, "wisdev preliminary search owner mismatch",
				"session_id", strings.TrimSpace(req.SessionID),
				"request_user_id", requestUserID,
				"owner_id", ownerID,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
			return
		}

		payload := buildAgentSessionPreliminarySearchPayload(r.Context(), agentGateway.SearchRegistry, session)
		writeEnvelope(w, "preliminarySearch", payload)
	}
	for _, path := range wisdevSessionPreliminarySearchPaths {
		mux.HandleFunc(path, handleSessionPreliminarySearch)
	}

	handleQuestionOptions := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
		if sessionID == "" {
			logWisdevRouteError(r, "wisdev question options validation failed", "reason", "missing_session_id")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
				"field": "sessionId",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_options") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev question options load failed", "session_id", sessionID, "question_id", questionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if questionID == "" {
			writeQuestionOptionResponse(w, "", []any{}, "", "")
			return
		}
		for _, question := range sliceAnyMap(session["questions"]) {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			options := questionOptionPayloads(question["options"])
			// Read stored provenance so pre-seeded LLM options are correctly
			// identified as AI-generated on subsequent fetches (BUG-1 fix).
			source := wisdev.AsOptionalString(question["optionsSource"])
			if source == "" {
				source = "stored"
			}
			explanation := wisdev.AsOptionalString(question["optionsExplanation"])

			// If stored options are empty, generate them on-demand with a tight
			// timeout. The generation helpers already fall back heuristically,
			// so degraded or non-LLM sessions still get dynamic options.
			if len(options) == 0 && agentGateway != nil {
				genCtx, genCancel := context.WithTimeout(r.Context(), 45*time.Second)
				generatedOptions, generatedSource, generatedExplanation := buildDynamicQuestionOptionsOnce(genCtx, sessionID, session, questionID, nil)
				genCancel()
				if generatedSource != "" {
					source = generatedSource
				}
				if generatedExplanation != "" {
					explanation = generatedExplanation
				}
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, source, explanation, false) {
					persistDynamicQuestionOptions(sessionID, wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, source, explanation, false)
				}
				options = questionOptionPayloads(generatedOptions)
			}

			writeQuestionOptionResponse(w, questionID, options, source, explanation)
			return
		}
		writeQuestionOptionResponse(w, questionID, []any{}, "", "")
	}
	for _, path := range wisdevQuestionOptionsPaths {
		mux.HandleFunc(path, handleQuestionOptions)
	}

	handleQuestionRecommendations := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
		if sessionID == "" || questionID == "" {
			logWisdevRouteError(r, "wisdev question recommendations validation failed",
				"session_id", sessionID,
				"question_id", questionID,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
				"fields": []string{"sessionId", "questionId"},
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_recommendations") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev question recommendations load failed", "session_id", sessionID, "question_id", questionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		answers := mapAny(session["answers"])
		if answer, ok := answers[questionID].(map[string]any); ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"questionId":        questionID,
				"values":            sliceStrings(answer["values"]),
				"explanation":       "",
				"source":            "session",
				"fallbackTriggered": false,
				"fallbackReason":    "",
			})
			return
		}
		recommended := []string{}
		explanation := ""
		source := "heuristic"
		fallbackTriggered := false
		fallbackReason := ""
		questions := sliceAnyMap(session["questions"])
		findRecommendationQuestion := func() (map[string]any, int) {
			if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 &&
				wisdev.AsOptionalString(pending["id"]) == questionID {
				return pending, -1
			}
			for i, question := range questions {
				if wisdev.AsOptionalString(question["id"]) == questionID {
					return question, i
				}
			}
			return nil, -1
		}

		question, questionIndex := findRecommendationQuestion()
		if len(question) > 0 {
			if questionIndex >= 0 && len(questionOptionValues(question["options"])) == 0 && agentGateway != nil {
				genCtx, genCancel := context.WithTimeout(r.Context(), 12*time.Second)
				generatedOptions, generatedSource, generatedExplanation := buildDynamicQuestionOptionsOnce(genCtx, sessionID, session, questionID, nil)
				genCancel()
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, generatedSource, generatedExplanation, false) {
					persistDynamicQuestionOptions(sessionID, wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, generatedSource, generatedExplanation, false)
					questions = sliceAnyMap(session["questions"])
					question = questions[questionIndex]
				}
			}
			allOptionValues := questionOptionValues(question["options"])
			isMultiSelect, _ := question["isMultiSelect"].(bool)
			limit := 1
			if isMultiSelect {
				limit = 3
			}
			if len(allOptionValues) == 0 {
				fallbackTriggered = true
				fallbackReason = "options_unavailable"
				explanation = describeQuestionRecommendationFallback("options_unavailable")
				logQuestionRecommendationFallback(r, sessionID, questionID, "options_unavailable")
			}

			// AI-first path: use Brain.SuggestQuestionValues when available.
			// The LLM picks the most relevant options for the query; we fall
			// back to the heuristic slice on any error.
			// Keep this bounded, but give the sidecar enough room for healthy
			// Gemini structured responses during local warm-up and contention.
			brain := ensureQuestionRecommendationBrain()
			if brain != nil && len(allOptionValues) > 0 {
				query := wisdev.ResolveSessionQueryText(
					wisdev.AsOptionalString(session["correctedQuery"]),
					wisdev.AsOptionalString(session["originalQuery"]),
				)
				questionLabel := wisdev.AsOptionalString(question["text"])
				if questionLabel == "" {
					questionLabel = wisdev.AsOptionalString(question["question"])
				}
				if questionLabel == "" {
					questionLabel = questionID
				}
				recCtx, recCancel := context.WithTimeout(r.Context(), wisdevQuestionRecommendationTimeout)
				aiValues, aiExplanation, aiErr := brain.SuggestQuestionValues(
					recCtx, query, questionID, questionLabel, allOptionValues, limit, "",
				)
				recCancel()
				if aiErr == nil && len(aiValues) > 0 {
					recommended = aiValues
					explanation = aiExplanation
					source = "ai"
				} else {
					fallbackStage := "ai_empty_response"
					if aiErr != nil {
						fallbackStage = "ai_request_failed"
						logQuestionRecommendationFallback(r, sessionID, questionID, fallbackStage,
							"error", aiErr.Error(),
							"option_count", len(allOptionValues),
						)
					} else {
						logQuestionRecommendationFallback(r, sessionID, questionID, fallbackStage,
							"option_count", len(allOptionValues),
						)
					}
					fallbackTriggered = true
					fallbackReason = fallbackStage
					// Heuristic fallback: return the first N options.
					recommended = allOptionValues
					if len(recommended) > limit {
						recommended = recommended[:limit]
					}
					if explanation == "" {
						explanation = describeQuestionRecommendationFallback(fallbackStage)
					}
				}
			} else if len(allOptionValues) > 0 {
				fallbackTriggered = true
				fallbackReason = "ai_unavailable"
				logQuestionRecommendationFallback(r, sessionID, questionID, "ai_unavailable",
					"brain_available", agentGateway != nil && agentGateway.Brain != nil,
					"llm_client_available", agentGateway != nil && agentGateway.LLMClient != nil,
					"option_count", len(allOptionValues),
				)
				// Heuristic fallback: return the first N options.
				recommended = allOptionValues
				if len(recommended) > limit {
					recommended = recommended[:limit]
				}
				if explanation == "" {
					explanation = describeQuestionRecommendationFallback("ai_unavailable")
				}
			}
		}
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"questionId":        questionID,
			"values":            recommended,
			"explanation":       explanation,
			"source":            source,
			"fallbackTriggered": fallbackTriggered,
			"fallbackReason":    fallbackReason,
		})
	}
	for _, path := range wisdevQuestionRecommendationsPaths {
		mux.HandleFunc(path, handleQuestionRecommendations)
	}

	handleQuestionRegenerate := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID       string   `json:"sessionId"`
			QuestionID      string   `json:"questionId"`
			PreviousOptions []string `json:"previousOptions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev question regenerate decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.QuestionID) == "" {
			logWisdevRouteError(r, "wisdev question regenerate validation failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"question_id", strings.TrimSpace(req.QuestionID),
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
				"fields": []string{"sessionId", "questionId"},
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_regenerate") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(strings.TrimSpace(req.SessionID))
		if err != nil {
			logWisdevRouteError(r, "wisdev question regenerate load failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		questionID := strings.TrimSpace(req.QuestionID)
		if err := validateStringSlice(req.PreviousOptions, "previousOptions", 12, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "previousOptions",
			})
			return
		}

		// Generate fresh dynamic options with a per-call timeout so the user
		// actually gets different options when they click "Regenerate".
		var options []map[string]any
		source := "stored"
		explanation := ""
		existingOptionValues := []string{}
		for _, question := range sliceAnyMap(session["questions"]) {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			existingOptionValues = questionOptionValues(question["options"])
			break
		}
		previousOptions := uniqueStrings(append(existingOptionValues, req.PreviousOptions...))
		if agentGateway != nil {
			// Explicit cancel (not defer) so the context is released immediately
			// after the LLM call returns, not when the entire handler returns.
			genCtx, genCancel := context.WithTimeout(r.Context(), 12*time.Second)
			generatedOptions, generatedSource, generatedExplanation := buildDynamicQuestionOptions(genCtx, session, questionID, previousOptions)
			genCancel()
			if generatedSource != "" {
				source = generatedSource
			}
			if generatedExplanation != "" {
				explanation = generatedExplanation
			}
			if len(generatedOptions) > 0 {
				options = questionOptionPayloads(generatedOptions)
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, source, explanation, true) {
					persistDynamicQuestionOptions(strings.TrimSpace(req.SessionID), wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, source, explanation, true)
				}
			}
		}

		// Fall back to stored options if LLM was unavailable or returned nothing.
		if len(options) == 0 {
			for _, question := range sliceAnyMap(session["questions"]) {
				if wisdev.AsOptionalString(question["id"]) == questionID {
					options = questionOptionPayloads(question["options"])
					source = wisdev.AsOptionalString(question["optionsSource"])
					if source == "" {
						source = "stored"
					}
					explanation = wisdev.AsOptionalString(question["optionsExplanation"])
					break
				}
			}
		}

		writeQuestionOptionResponse(w, req.QuestionID, options, source, explanation)
	}
	for _, path := range wisdevQuestionRegeneratePaths {
		mux.HandleFunc(path, handleQuestionRegenerate)
	}

	mux.HandleFunc("/wisdev/analyze-query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query   string `json:"query"`
			TraceID string `json:"traceId"`
			// UserID is accepted for forward-compatibility with the frontend transport
			// but is not used here — the canonical user identity is resolved from the
			// authenticated JWT context via GetUserID(r), not from the request body.
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev analyze query decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID)
		query := wisdev.ResolveSessionQueryText(req.Query, "")
		if query == "" {
			logWisdevRouteError(r, "wisdev analyze query validation failed", "trace_id", traceID, "reason", "missing_query")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}

		logWisdevRouteLifecycle(r, "wisdev_analyze_query", "request_received", query,
			"trace_id", traceID,
			"result", "accepted",
		)
		// Wrap the request context with a longer deadline so Gemini 2.5 thinking
		// has room to complete without tripping the heuristic fallback on healthy
		// requests. buildAnalyzeQueryPayloadWithAI
		// runs the actual LLM call in a goroutine and selects on ctx.Done(), so
		// this deadline is respected even when the Go oauth2 library's Token()
		// refresh is blocking (Token() has no context parameter). The frontend
		// keeps a slightly larger fast-path budget so network and middleware
		// overhead do not force unnecessary fallbacks. The background goroutine
		// continues on its own bounded timeout, so no goroutine leak occurs if the
		// handler fires first.
		analyzeCtx, analyzeCancel := context.WithTimeout(r.Context(), wisdevAnalyzeQueryBudget())
		defer analyzeCancel()
		payload := buildAnalyzeQueryPayloadWithAI(analyzeCtx, agentGateway, query, traceID)
		entities, _ := payload["entities"].([]string)
		researchQuestions, _ := payload["research_questions"].([]string)
		entityCount := len(entities)
		researchQuestionCount := len(researchQuestions)

		// Expose whether the response is AI-derived or a heuristic so the
		// frontend can log the true analysis source without parsing the body.
		analysisSource := "ai"
		fallbackReason, _ := payload["fallbackReason"].(string)
		if ft, _ := payload["fallbackTriggered"].(bool); ft {
			analysisSource = "heuristic"
		}

		logWisdevRouteLifecycle(r, "wisdev_analyze_query", "response_ready", query,
			"trace_id", traceID,
			"entity_count", entityCount,
			"research_question_count", researchQuestionCount,
			"analysis_source", analysisSource,
			"fallback_reason", fallbackReason,
			"fallback_detail", payload["fallbackDetail"],
			"result", "success",
		)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		w.Header().Set("X-Analysis-Source", analysisSource)
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func buildAgentSessionPreliminarySearchPayload(ctx context.Context, registry *internalsearch.ProviderRegistry, session map[string]any) map[string]any {
	payload := map[string]any{
		"totalCount":  0,
		"perSubtopic": map[string]int{},
	}
	if registry == nil || session == nil {
		return payload
	}

	query := wisdev.ResolveSessionQueryText(
		wisdev.AsOptionalString(session["correctedQuery"]),
		wisdev.AsOptionalString(session["originalQuery"]),
	)
	if query == "" {
		return payload
	}

	subtopicsQuestion := map[string]any{}
	for _, question := range sliceAnyMap(session["questions"]) {
		if strings.EqualFold(strings.TrimSpace(wisdev.AsOptionalString(question["type"])), "subtopics") {
			subtopicsQuestion = question
			break
		}
	}

	subtopicOptions := sliceAnyMap(subtopicsQuestion["options"])
	if len(subtopicOptions) > 5 {
		subtopicOptions = subtopicOptions[:5]
	}

	perSubtopic := make(map[string]int, len(subtopicOptions))
	for _, option := range subtopicOptions {
		key := strings.TrimSpace(wisdev.AsOptionalString(option["value"]))
		if key != "" {
			perSubtopic[key] = 0
		}
	}

	type outcome struct {
		key    string
		count  int
		isMain bool
	}

	results := make(chan outcome, len(subtopicOptions)+1)
	var wg sync.WaitGroup
	runSearch := func(key string, searchQuery string, limit int, isMain bool) {
		defer wg.Done()
		if strings.TrimSpace(searchQuery) == "" {
			results <- outcome{key: key, count: 0, isMain: isMain}
			return
		}
		result := internalsearch.ParallelSearch(ctx, registry, searchQuery, internalsearch.SearchOpts{
			Limit:       limit,
			QualitySort: true,
		})
		results <- outcome{key: key, count: len(result.Papers), isMain: isMain}
	}

	wg.Add(1)
	go runSearch("", query, 30, true)

	for _, option := range subtopicOptions {
		key := strings.TrimSpace(wisdev.AsOptionalString(option["value"]))
		label := strings.TrimSpace(wisdev.AsOptionalString(option["label"]))
		if key == "" {
			key = strings.TrimSpace(wisdev.AsOptionalString(option["id"]))
		}
		if label == "" {
			label = key
		}
		if key == "" || label == "" {
			continue
		}

		// Build the search query for this subtopic, but strip structural section
		// header labels ("Background", "Methods", "Overview", etc.) that produce
		// near-zero results when appended to the base query — the same guard
		// applied in composeTopicTreePathQuery.
		searchQuery := query
		if !isStructuralTopicLabel(label) {
			searchQuery = strings.TrimSpace(query + " " + label)
		}

		wg.Add(1)
		go runSearch(key, searchQuery, 10, false)
	}

	wg.Wait()
	close(results)

	totalCount := 0
	for item := range results {
		if item.isMain {
			totalCount = item.count
			continue
		}
		if item.key != "" {
			perSubtopic[item.key] = item.count
		}
	}

	payload["totalCount"] = totalCount
	payload["perSubtopic"] = perSubtopic
	return payload
}
