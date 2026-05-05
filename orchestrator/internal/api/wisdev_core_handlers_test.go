package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/policy"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestWisDev_CoreHandlers(t *testing.T) {
	gw := &wisdev.AgentGateway{
		Store:        wisdev.NewInMemorySessionStore(),
		PolicyConfig: policy.DefaultPolicyConfig(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	t.Run("POST /wisdev/decide - Success with session", func(t *testing.T) {
		session, _ := gw.CreateSession(context.Background(), "u1", "test query")
		session.Plan = &wisdev.PlanState{
			PlanID: "p1",
			Steps: []wisdev.PlanStep{
				{ID: "s1", Action: "search", Risk: "low"},
			},
		}
		_ = gw.Store.Put(context.Background(), session, gw.SessionTTL)

		body := map[string]any{
			"sessionId": session.SessionID,
			"plan": map[string]any{
				"planId": "p1",
				"steps": []map[string]any{
					{"id": "s1", "risk": "low"},
				},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/decide", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)

		result := resp["decision"].(map[string]any)
		assert.Equal(t, "s1", result["selectedStepId"])
	})

	t.Run("POST /wisdev/critique - Success", func(t *testing.T) {
		body := map[string]any{
			"query": "climate change",
			"decision": map[string]any{
				"rationale":  "testing",
				"confidence": 0.8,
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/critique", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.NotNil(t, resp["critique"])
	})

	t.Run("POST /wisdev/decide - invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/decide", bytes.NewReader([]byte(`{bad`)))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrBadRequest, resp.Error.Code)
	})

	t.Run("POST /wisdev/critique - missing query", func(t *testing.T) {
		body := map[string]any{
			"decision": map[string]any{
				"rationale": "",
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/critique", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		var resp APIError
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, ErrInvalidParameters, resp.Error.Code)
	})

	t.Run("POST /wisdev/decide - guided verification gate applies confirmation", func(t *testing.T) {
		body := map[string]any{
			"sessionId":     "guided-verification",
			"executionMode": "guided",
			"plan": map[string]any{
				"planId": "p-verify",
				"steps": []map[string]any{
					{
						"id":                   "verify-step",
						"action":               "research.evaluateEvidence",
						"risk":                 "medium",
						"verificationRequired": true,
						"expectedImpact":       0.4,
					},
					{
						"id":             "draft-step",
						"action":         "research.synthesizeAnswer",
						"risk":           "low",
						"expectedImpact": 0.9,
					},
				},
				"liveState": map[string]any{
					"completedStepIds": []string{},
				},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/decide", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		result := resp["decision"].(map[string]any)
		assert.Equal(t, "verify-step", result["selectedStepId"])
		assert.Equal(t, "guided", result["executionMode"])
		assert.Equal(t, true, result["requiresConfirmation"])
		assert.Equal(t, "medium_risk_confirmation_required", result["guardrailReason"])
	})

	t.Run("POST /wisdev/decide - yolo fans out top ranked candidates", func(t *testing.T) {
		body := map[string]any{
			"sessionId":     "yolo-fanout",
			"executionMode": "yolo",
			"plan": map[string]any{
				"planId": "p-yolo",
				"steps": []map[string]any{
					{"id": "s1", "action": "research.retrievePapers", "expectedImpact": 0.95},
					{"id": "s2", "action": "research.evaluateEvidence", "expectedImpact": 0.85},
					{"id": "s3", "action": "research.buildClaimEvidenceTable", "expectedImpact": 0.75},
					{"id": "s4", "action": "research.generateIdeas", "expectedImpact": 0.2},
				},
			},
		}
		jsonBody, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/decide", bytes.NewReader(jsonBody))
		rec := httptest.NewRecorder()

		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		result := resp["decision"].(map[string]any)
		assert.Equal(t, "s1", result["selectedStepId"])
		assert.Equal(t, "yolo", result["executionMode"])
		assert.Equal(t, []any{"s1", "s2", "s3"}, result["selectedParallelStepIds"])
	})
}

func TestWisDevQuestionOptionsDynamicGeneration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_opts_gen")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
		// No LLMClient — tests that the handler gracefully returns empty
		// when the LLM is unavailable.
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-opts-gen"
	userID := "u-opts"
	sessionPayload := map[string]any{
		"sessionId":      sessionID,
		"userId":         userID,
		"query":          "RLHF reinforcement learning from human feedback",
		"originalQuery":  "RLHF reinforcement learning from human feedback",
		"correctedQuery": "RLHF reinforcement learning from human feedback",
		"questions": []any{
			// q4_subtopics stored with empty options — simulates session init state.
			map[string]any{
				"id":            "q4_subtopics",
				"isMultiSelect": true,
				"options":       []any{},
			},
			// q2_scope with pre-seeded options.
			map[string]any{
				"id":            "q2_scope",
				"isMultiSelect": false,
				"options": []any{
					map[string]any{"value": "focused", "label": "Focused"},
					map[string]any{"value": "comprehensive", "label": "Comprehensive"},
				},
			},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	t.Run("question with empty options falls back to heuristic generation when LLM unavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=q4_subtopics", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		options, ok := resp["options"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, options)
		assert.Equal(t, "heuristic_fallback", resp["source"])
		assert.Equal(t, true, resp["fallbackTriggered"])
		assert.Equal(t, "heuristic_fallback", resp["fallbackReason"])
		assert.Equal(t, "heuristic_fallback", rec.Header().Get("X-Fallback-Reason"))
	})

	t.Run("question with pre-seeded options returns them correctly", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=q2_scope", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		options, ok := resp["options"].([]any)
		require.True(t, ok)
		assert.Equal(t, 2, len(options))
		// source and explanation fields must be present in the response.
		assert.NotNil(t, resp["source"])
		assert.NotNil(t, resp["explanation"])
	})

	t.Run("pre-seeded LLM options carry optionsSource provenance", func(t *testing.T) {
		// Simulate a session where options were seeded by the LLM (optionsSource set).
		seededSessionID := "test-opts-seeded"
		seededPayload := map[string]any{
			"sessionId": seededSessionID,
			"userId":    userID,
			"questions": []any{
				map[string]any{
					"id":                 "q4_subtopics",
					"isMultiSelect":      true,
					"options":            []any{map[string]any{"value": "reward modeling", "label": "reward modeling"}},
					"optionsSource":      "llm_structured",
					"optionsExplanation": "RLHF focuses on reward modeling and policy optimization.",
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, stateStore.PersistAgentSessionMutation(seededSessionID, userID, seededPayload, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+seededSessionID+"&questionId=q4_subtopics", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Equal(t, "llm_structured", resp["source"], "provenance should reflect LLM generation")
		assert.Equal(t, "RLHF focuses on reward modeling and policy optimization.", resp["explanation"])
		assert.Equal(t, false, resp["fallbackTriggered"])
		assert.Equal(t, "", resp["fallbackReason"])
		assert.Empty(t, rec.Header().Get("X-Fallback-Reason"))
	})

	t.Run("recommendations hydrate dynamic options before picking values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=q4_subtopics", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		values, ok := resp["values"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, values)
		assert.Equal(t, "heuristic", resp["source"])
	})

	t.Run("recommendations include pending follow-up questions", func(t *testing.T) {
		followUpSessionID := "test-follow-up-recs"
		followUpPayload := map[string]any{
			"sessionId": followUpSessionID,
			"userId":    userID,
			"questions": []any{
				map[string]any{
					"id":            "q4_subtopics",
					"isMultiSelect": true,
					"options": []any{
						map[string]any{"value": "sleep_quality", "label": "Sleep Quality"},
					},
				},
			},
			"questionSequence": []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			"pendingFollowUpQuestion": map[string]any{
				"id":               "follow_up_refinement",
				"type":             "clarification",
				"question":         "Which focus should the next search pass prioritize?",
				"targetQuestionId": "q4_subtopics",
				"isMultiSelect":    true,
				"options": []any{
					map[string]any{"value": "memory_consolidation", "label": "Memory Consolidation"},
					map[string]any{"value": "patient_selection", "label": "Patient Selection"},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, stateStore.PersistAgentSessionMutation(followUpSessionID, userID, followUpPayload, wisdev.RuntimeJournalEntry{}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+followUpSessionID+"&questionId=follow_up_refinement", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		values, ok := resp["values"].([]any)
		require.True(t, ok)
		require.Len(t, values, 2)
		assert.Equal(t, "heuristic", resp["source"])
	})
}

func TestWisDevQuestionAnswerCompletesBaseOnlySessionAfterTimeframe(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_follow_up")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-answer-follow-up"
	userID := "u-answer"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "social",
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 2,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"},
		"minQuestions":         6,
		"maxQuestions":         6,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
			map[string]any{"id": "q4_subtopics", "question": "Which subtopics matter most?", "text": "Which subtopics matter most?"},
			map[string]any{"id": "q5_study_types", "question": "What study types should be included?", "text": "What study types should be included?"},
			map[string]any{"id": "q6_exclusions", "question": "Are there any specific exclusions?", "text": "Are there any specific exclusions?"},
		},
		"answers": map[string]any{
			"q1_domain": map[string]any{
				"questionId":    "q1_domain",
				"values":        []string{"social"},
				"displayValues": []string{"Social Sciences"},
			},
			"q2_scope": map[string]any{
				"questionId":    "q2_scope",
				"values":        []string{"focused"},
				"displayValues": []string{"Focused"},
			},
		},
		"questionStopReason": "",
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	requestBody, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"questionId":    "q3_timeframe",
		"values":        []string{"5years"},
		"displayValues": []string{"Last 5 years"},
		"proceed":       false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(requestBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, false, resp["completed"])

	question := mapAny(resp["question"])
	assert.Equal(t, "q4_subtopics", wisdev.AsOptionalString(question["id"]))

	responseSession := mapAny(resp["session"])
	assert.Equal(t, "questioning", wisdev.AsOptionalString(responseSession["status"]))
	assert.Equal(t, 3, wisdev.IntValue(responseSession["currentQuestionIndex"]))

	pending := mapAny(responseSession["pendingFollowUpQuestion"])
	assert.Empty(t, pending)

	questioning := mapAny(resp["questioning"])
	assert.Empty(t, wisdev.AsOptionalString(questioning["pendingQuestionId"]))
	assert.NotContains(t, sliceStrings(questioning["remainingQuestionIds"]), "follow_up_refinement")

	loaded, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, "questioning", wisdev.AsOptionalString(loaded["status"]))
	assert.Equal(t, 3, wisdev.IntValue(loaded["currentQuestionIndex"]))

	loadedPending := mapAny(loaded["pendingFollowUpQuestion"])
	assert.Empty(t, loadedPending)
}

func TestWisDevQuestionAnswerDoesNotOpenFollowUpWhenLLMDeclines(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_llm_declines")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"needsFollowUp":false,"followUpQuestion":null}`,
			"modelUsed":  "test-follow-up-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
		LLMClient:  llm.NewClient(),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-answer-follow-up-llm-decline"
	userID := "u-answer"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "general",
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 2,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"},
		"minQuestions":         6,
		"maxQuestions":         6,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
			map[string]any{"id": "q4_subtopics", "question": "Which subtopics matter most?", "text": "Which subtopics matter most?"},
			map[string]any{"id": "q5_study_types", "question": "What study types should be included?", "text": "What study types should be included?"},
			map[string]any{"id": "q6_exclusions", "question": "Are there any specific exclusions?", "text": "Are there any specific exclusions?"},
		},
		"answers": map[string]any{
			"q1_domain": map[string]any{
				"questionId":    "q1_domain",
				"values":        []string{"social"},
				"displayValues": []string{"Social Sciences"},
			},
			"q2_scope": map[string]any{
				"questionId":    "q2_scope",
				"values":        []string{"focused"},
				"displayValues": []string{"Focused"},
			},
		},
		"questionStopReason": "",
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	requestBody, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"questionId":    "q3_timeframe",
		"values":        []string{"5years"},
		"displayValues": []string{"Last 5 years"},
		"proceed":       false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(requestBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["completed"])

	question := mapAny(resp["question"])
	assert.Equal(t, "q4_subtopics", wisdev.AsOptionalString(question["id"]))

	responseSession := mapAny(resp["session"])
	assert.Equal(t, "questioning", wisdev.AsOptionalString(responseSession["status"]))

	pending := mapAny(responseSession["pendingFollowUpQuestion"])
	assert.Empty(t, pending)
}

func TestWisDevQuestionAnswerCompletesQuicklyWhenFollowUpLLMIsSlow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_llm_slow")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	t.Setenv("INTERNAL_SERVICE_KEY", "test-key")

	previousTimeout := wisdevFollowUpDecisionTimeout
	wisdevFollowUpDecisionTimeout = 150 * time.Millisecond
	t.Cleanup(func() {
		wisdevFollowUpDecisionTimeout = previousTimeout
	})

	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/llm/structured-output", r.URL.Path)
		time.Sleep(2 * time.Second)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"needsFollowUp":true,"followUpQuestion":{"id":"llm_follow_up","question":"Slow LLM question"}}`,
			"modelUsed":  "test-follow-up-llm",
		}))
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
		LLMClient:  llm.NewClientWithTimeout(5 * time.Second),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-answer-follow-up-llm-timeout"
	userID := "u-answer"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "general",
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 2,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe"},
		"minQuestions":         3,
		"maxQuestions":         3,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
		},
		"answers": map[string]any{
			"q1_domain": map[string]any{
				"questionId":    "q1_domain",
				"values":        []string{"social"},
				"displayValues": []string{"Social Sciences"},
			},
			"q2_scope": map[string]any{
				"questionId":    "q2_scope",
				"values":        []string{"focused"},
				"displayValues": []string{"Focused"},
			},
		},
		"questionStopReason": "",
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	requestBody, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"questionId":    "q3_timeframe",
		"values":        []string{"5years"},
		"displayValues": []string{"Last 5 years"},
		"proceed":       false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(requestBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()

	start := time.Now()
	mux.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Less(t, elapsed, time.Second)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["completed"])

	question := mapAny(resp["question"])
	assert.Empty(t, question)
	assert.NotEqual(t, "llm_follow_up", wisdev.AsOptionalString(question["id"]))

	responseSession := mapAny(resp["session"])
	pending := mapAny(responseSession["pendingFollowUpQuestion"])
	assert.Empty(t, pending)
}

func TestWisDevQuestionAnswerMirrorsPendingFollowUpIntoCanonicalAnswer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_follow_up_mirror")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-follow-up-mirror"
	userID := "u-follow-up"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "social",
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 3,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"},
		"minQuestions":         6,
		"maxQuestions":         6,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
			map[string]any{"id": "q4_subtopics", "question": "Which subtopics matter most?", "text": "Which subtopics matter most?"},
			map[string]any{"id": "q5_study_types", "question": "What study types should be included?", "text": "What study types should be included?"},
			map[string]any{"id": "q6_exclusions", "question": "Are there any specific exclusions?", "text": "Are there any specific exclusions?"},
		},
		"pendingFollowUpQuestion": map[string]any{
			"id":               "follow_up_refinement",
			"type":             "clarification",
			"question":         "Which focus should the next search pass prioritize?",
			"questionSource":   "heuristic_fallback",
			"targetQuestionId": "q4_subtopics",
			"options": []any{
				map[string]any{"value": "memory consolidation", "label": "Memory Consolidation"},
			},
		},
		"answers": map[string]any{
			"q1_domain": map[string]any{
				"questionId":    "q1_domain",
				"values":        []string{"social"},
				"displayValues": []string{"Social Sciences"},
			},
			"q2_scope": map[string]any{
				"questionId":    "q2_scope",
				"values":        []string{"focused"},
				"displayValues": []string{"Focused"},
			},
			"q3_timeframe": map[string]any{
				"questionId":    "q3_timeframe",
				"values":        []string{"5years"},
				"displayValues": []string{"Last 5 years"},
			},
			"q5_study_types": map[string]any{
				"questionId":    "q5_study_types",
				"values":        []string{"review"},
				"displayValues": []string{"Review"},
			},
			"q6_exclusions": map[string]any{
				"questionId":    "q6_exclusions",
				"values":        []string{"none"},
				"displayValues": []string{"No exclusions"},
			},
		},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	requestBody, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"questionId":    "follow_up_refinement",
		"values":        []string{"memory consolidation"},
		"displayValues": []string{"Memory Consolidation"},
		"proceed":       false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(requestBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["completed"])

	responseSession := mapAny(resp["session"])
	assert.Equal(t, "ready", wisdev.AsOptionalString(responseSession["status"]))
	assert.Empty(t, mapAny(responseSession["pendingFollowUpQuestion"]))

	loaded, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)

	answers := mapAny(loaded["answers"])
	mirrored := mapAny(answers["q4_subtopics"])
	require.NotEmpty(t, mirrored)
	assert.Equal(t, "q4_subtopics", wisdev.AsOptionalString(mirrored["questionId"]))
	assert.Equal(t, []string{"memory consolidation"}, sliceStrings(mirrored["values"]))
	assert.Equal(t, []string{"Memory Consolidation"}, sliceStrings(mirrored["displayValues"]))
	assert.Equal(t, "follow_up_refinement", wisdev.AsOptionalString(mirrored["mirroredFromQuestionId"]))
}

func TestWisDevQuestionAnswerReplansSessionForDomainCorrection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_domain_replan")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-domain-replan"
	userID := "u-domain"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "general",
		"secondaryDomains":     []string{},
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 0,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe"},
		"minQuestions":         3,
		"maxQuestions":         4,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	requestBody, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"questionId":    "q1_domain",
		"values":        []string{"medicine", "biology"},
		"displayValues": []string{"Medicine & Healthcare", "Biology & Life Sciences"},
		"proceed":       false,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(requestBody))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	responseSession := mapAny(resp["session"])
	assert.Equal(t, "medicine", wisdev.AsOptionalString(responseSession["detectedDomain"]))
	assert.Equal(t, []string{"biology"}, sliceStrings(responseSession["secondaryDomains"]))
	assert.Equal(t, "advanced", wisdev.AsOptionalString(responseSession["expertiseLevel"]))
	assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality"}, sliceStrings(responseSession["questionSequence"]))
	assert.Equal(t, 1, wisdev.IntValue(responseSession["currentQuestionIndex"]))
	assert.Equal(t, 7, wisdev.IntValue(responseSession["maxQuestions"]))

	nextQuestion := mapAny(resp["question"])
	assert.Equal(t, "q2_scope", wisdev.AsOptionalString(nextQuestion["id"]))
	assert.Contains(t, sliceStrings(mapAny(resp["questioning"])["remainingQuestionIds"]), "q5_study_types")

	loaded, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, "medicine", wisdev.AsOptionalString(loaded["detectedDomain"]))
	assert.Equal(t, []string{"biology"}, sliceStrings(loaded["secondaryDomains"]))
	assert.Equal(t, "advanced", wisdev.AsOptionalString(loaded["expertiseLevel"]))
	assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality"}, sliceStrings(loaded["questionSequence"]))
}

func TestWisDevQuestionAnswerIdempotencyChangesWithAnswerPayload(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_idempotency_payload")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore:  stateStore,
		Journal:     journal,
		Idempotency: wisdev.NewIdempotencyStore(10 * time.Minute),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-answer-idempotency-payload"
	userID := "u-idempotency"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "general",
		"complexityScore":      0.4,
		"status":               "questioning",
		"currentQuestionIndex": 0,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe"},
		"minQuestions":         3,
		"maxQuestions":         3,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	sendAnswer := func(values []string, displayValues []string) map[string]any {
		body, marshalErr := json.Marshal(map[string]any{
			"sessionId":     sessionID,
			"questionId":    "q1_domain",
			"values":        values,
			"displayValues": displayValues,
			"proceed":       false,
		})
		require.NoError(t, marshalErr)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp
	}

	first := sendAnswer([]string{"biology"}, []string{"Biology & Life Sciences"})
	second := sendAnswer([]string{"biology"}, []string{"Biology & Life Sciences"})
	changed := sendAnswer([]string{"medicine"}, []string{"Medicine & Healthcare"})

	firstTraceID := wisdev.AsOptionalString(first["traceId"])
	secondTraceID := wisdev.AsOptionalString(second["traceId"])
	changedTraceID := wisdev.AsOptionalString(changed["traceId"])

	require.NotEmpty(t, firstTraceID)
	assert.Equal(t, firstTraceID, secondTraceID)
	assert.NotEqual(t, firstTraceID, changedTraceID)
	assert.Equal(t, "medicine", wisdev.AsOptionalString(mapAny(changed["session"])["detectedDomain"]))
}

func TestWisDevQuestionAnswerIdempotencyReplaysWithExpectedUpdatedAt(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_idempotency_expected_updated_at")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore:  stateStore,
		Journal:     journal,
		Idempotency: wisdev.NewIdempotencyStore(10 * time.Minute),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	userID := "u-idempotency"
	initBody := initializeWisDevSpecSession(t, mux, userID, "sleep interventions adults")
	initSession := mapAny(initBody["session"])
	sessionID := wisdev.AsOptionalString(initSession["sessionId"])
	require.NotEmpty(t, sessionID)
	loadedInitSession, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	expectedUpdatedAt := wisdev.IntValue64(loadedInitSession["updatedAt"])

	sendAnswer := func() *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{
			"sessionId":         sessionID,
			"questionId":        "q1_domain",
			"values":            []string{"biology"},
			"displayValues":     []string{"Biology & Life Sciences"},
			"expectedUpdatedAt": expectedUpdatedAt,
			"proceed":           false,
		})
		require.NoError(t, marshalErr)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := sendAnswer()
	require.Equal(t, http.StatusOK, first.Code)

	var firstBody map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstBody))
	firstTraceID := wisdev.AsOptionalString(firstBody["traceId"])
	require.NotEmpty(t, firstTraceID)
	idempotencyKey := makeAgentAnswerIdempotencyKey(
		sessionID,
		"q1_domain",
		[]string{"biology"},
		[]string{"Biology & Life Sciences"},
		false,
		expectedUpdatedAt,
	)
	if status, _, ok := gw.Idempotency.Get(idempotencyKey); !ok || status != http.StatusOK {
		t.Fatalf("expected idempotency cache entry for key %q after first answer", idempotencyKey)
	}
	loadedAfterFirst, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	if !agentAnswerAlreadyApplied(
		loadedAfterFirst,
		"q1_domain",
		[]string{"biology"},
		[]string{"Biology & Life Sciences"},
	) {
		t.Fatalf("expected stored q1_domain answer to match retry payload, got %#v", mapAny(mapAny(loadedAfterFirst["answers"])["q1_domain"]))
	}

	retry := sendAnswer()
	require.Equal(t, http.StatusOK, retry.Code)

	var retryBody map[string]any
	require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &retryBody))
	retryTraceID := wisdev.AsOptionalString(retryBody["traceId"])
	assert.NotEmpty(t, retryTraceID)
	assert.Equal(t, retryTraceID, retry.Header().Get("X-Trace-Id"))
	assert.Equal(t, "biology", wisdev.AsOptionalString(mapAny(retryBody["session"])["detectedDomain"]))
}

func TestWisDevQuestionAnswerIdempotencyNormalizesMultiSelectOrdering(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_answer_idempotency_multiselect_order")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore:  stateStore,
		Journal:     journal,
		Idempotency: wisdev.NewIdempotencyStore(10 * time.Minute),
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	userID := "u-idempotency-order"
	initBody := initializeWisDevSpecSession(t, mux, userID, "sleep interventions adults")
	initSession := mapAny(initBody["session"])
	sessionID := wisdev.AsOptionalString(initSession["sessionId"])
	require.NotEmpty(t, sessionID)
	loadedInitSession, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	expectedUpdatedAt := wisdev.IntValue64(loadedInitSession["updatedAt"])

	sendAnswer := func(values []string, displayValues []string) *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{
			"sessionId":         sessionID,
			"questionId":        "q1_domain",
			"values":            values,
			"displayValues":     displayValues,
			"expectedUpdatedAt": expectedUpdatedAt,
			"proceed":           false,
		})
		require.NoError(t, marshalErr)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	first := sendAnswer(
		[]string{"biology", "medicine"},
		[]string{"Biology & Life Sciences", "Medicine & Healthcare"},
	)
	require.Equal(t, http.StatusOK, first.Code)

	var firstBody map[string]any
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstBody))
	firstTraceID := wisdev.AsOptionalString(firstBody["traceId"])
	require.NotEmpty(t, firstTraceID)

	retry := sendAnswer(
		[]string{"medicine", "biology"},
		[]string{"Medicine & Healthcare", "Biology & Life Sciences"},
	)
	require.Equal(t, http.StatusOK, retry.Code)

	var retryBody map[string]any
	require.NoError(t, json.Unmarshal(retry.Body.Bytes(), &retryBody))
	assert.Equal(t, firstTraceID, wisdev.AsOptionalString(retryBody["traceId"]))
	assert.Equal(t, firstTraceID, retry.Header().Get("X-Trace-Id"))
}

func TestWisDevQuestionNextDoesNotAdvanceSessionVersion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_question_next_version")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	userID := "u-next-version"
	initBody := initializeWisDevSpecSession(t, mux, userID, "sleep interventions adults")
	initSession := mapAny(initBody["session"])
	sessionID := wisdev.AsOptionalString(initSession["sessionId"])
	require.NotEmpty(t, sessionID)

	before, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	beforeUpdatedAt := wisdev.IntValue64(before["updatedAt"])
	require.NotZero(t, beforeUpdatedAt)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/next", bytes.NewReader(encodeJSON(t, map[string]any{
		"sessionId": sessionID,
	})))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	after, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	assert.Equal(t, beforeUpdatedAt, wisdev.IntValue64(after["updatedAt"]))
}

func TestWisDevQuestionRegeneratePersistsFreshOptions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_question_regenerate_persist")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	t.Setenv("WISDEV_STATE_DIR", tmpDir)

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	sessionID := "test-regenerate-persist"
	userID := "u-regenerate"
	sessionPayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"query":                "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"correctedQuery":       "sleep interventions adults",
		"detectedDomain":       "medicine",
		"status":               "questioning",
		"currentQuestionIndex": 3,
		"questionSequence":     []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
		"minQuestions":         4,
		"maxQuestions":         4,
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
			map[string]any{"id": "q3_timeframe", "question": "What timeframe matters most?", "text": "What timeframe matters most?"},
			map[string]any{
				"id":       "q4_subtopics",
				"question": "Which subtopics matter most?",
				"text":     "Which subtopics matter most?",
				"options": []any{
					map[string]any{"value": "legacy topic", "label": "Legacy Topic"},
				},
				"optionsSource": "stored",
			},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))
	before, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	beforeUpdatedAt := wisdev.IntValue64(before["updatedAt"])

	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewReader(encodeJSON(t, map[string]any{
		"sessionId":  sessionID,
		"questionId": "q4_subtopics",
	})))
	req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	responseOptions, ok := resp["options"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, responseOptions)
	assert.Equal(t, true, resp["fallbackTriggered"])
	assert.Equal(t, "heuristic_fallback", resp["fallbackReason"])
	assert.Equal(t, "heuristic_fallback", rec.Header().Get("X-Fallback-Reason"))

	loaded, err := stateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	assert.Greater(t, wisdev.IntValue64(loaded["updatedAt"]), beforeUpdatedAt)

	var storedOptions []map[string]any
	for _, question := range sliceAnyMap(loaded["questions"]) {
		if wisdev.AsOptionalString(question["id"]) == "q4_subtopics" {
			storedOptions = questionOptionPayloads(question["options"])
			break
		}
	}
	require.NotEmpty(t, storedOptions)
	var storedOptionsAny []any
	for _, option := range storedOptions {
		storedOptionsAny = append(storedOptionsAny, option)
	}
	assert.Equal(t, responseOptions, storedOptionsAny)
	assert.NotEqual(t, []map[string]any{{"value": "legacy topic", "label": "Legacy Topic"}}, storedOptions)
}

func TestWisDevQuestionRecommendationCap(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_rec_cap")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	os.Setenv("WISDEV_STATE_DIR", tmpDir)
	defer os.Unsetenv("WISDEV_STATE_DIR")

	stateStore := wisdev.NewRuntimeStateStore(nil, nil)
	journal := wisdev.NewRuntimeJournal(nil)
	gw := &wisdev.AgentGateway{
		StateStore: stateStore,
		Journal:    journal,
	}
	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	// Build a session payload with a multi-select question (q1_domain) and a single-select question (q2_scope).
	sessionID := "test-rec-cap-session"
	userID := "u-test"
	sessionPayload := map[string]any{
		"sessionId": sessionID,
		"userId":    userID,
		"questions": []any{
			map[string]any{
				"id":            "q1_domain",
				"isMultiSelect": true,
				"options": []any{
					map[string]any{"value": "medicine"},
					map[string]any{"value": "cs"},
					map[string]any{"value": "social"},
					map[string]any{"value": "climate"},
					map[string]any{"value": "neuro"},
				},
			},
			map[string]any{
				"id":            "q2_scope",
				"isMultiSelect": false,
				"options": []any{
					map[string]any{"value": "focused"},
					map[string]any{"value": "comprehensive"},
					map[string]any{"value": "exhaustive"},
				},
			},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, stateStore.PersistAgentSessionMutation(sessionID, userID, sessionPayload, wisdev.RuntimeJournalEntry{}))

	t.Run("multi-select question recommends at most 3", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=q1_domain", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		values, ok := resp["values"].([]any)
		require.True(t, ok, "expected values array in response")
		assert.LessOrEqual(t, len(values), 3, "multi-select question should recommend at most 3 options")
		assert.Equal(t, "heuristic", resp["source"])
	})

	t.Run("single-select question recommends exactly 1", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=q2_scope", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, userID))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		values, ok := resp["values"].([]any)
		require.True(t, ok, "expected values array in response")
		assert.Equal(t, 1, len(values), "single-select question should recommend exactly 1 option")
		assert.Equal(t, "heuristic", resp["source"])
	})
}
