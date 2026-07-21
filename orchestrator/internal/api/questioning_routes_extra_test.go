package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	internalsearch "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isolateQuestioningTestStateDir(t *testing.T) {
	t.Helper()
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
}

func TestDynamicQuestionOptionsSingleflightKey(t *testing.T) {
	assert.Equal(t, "s1:q4_subtopics:default", dynamicQuestionOptionsSingleflightKey(" s1 ", "q4_subtopics", nil))
	assert.Equal(t, "s1:q5_study_types:speculative", dynamicQuestionOptionsSingleflightKey("s1", "q5_study_types", map[string]any{}))

	session := map[string]any{
		"answers": map[string]any{
			"q4_subtopics": map[string]any{
				"values": []any{"reward modeling", "policy optimization", "reward modeling"},
			},
		},
	}
	assert.Equal(t, "s1:q5_study_types:q4:policy optimization|reward modeling", dynamicQuestionOptionsSingleflightKey("s1", "q5_study_types", session))

	sameSelectionsDifferentOrder := map[string]any{
		"answers": map[string]any{
			"q4_subtopics": map[string]any{
				"values": []any{"policy optimization", "reward modeling"},
			},
		},
	}
	assert.Equal(t,
		dynamicQuestionOptionsSingleflightKey("s1", "q5_study_types", session),
		dynamicQuestionOptionsSingleflightKey("s1", "q5_study_types", sameSelectionsDifferentOrder),
	)

	evidenceContext := map[string]any{
		"answers": map[string]any{
			"q4_subtopics": map[string]any{
				"values": []any{"safety signals", "patient selection"},
			},
			"q5_study_types": map[string]any{
				"values": []any{"randomized controlled trial", "cohort study"},
			},
		},
	}
	assert.Equal(t,
		"s1:q7_evidence_quality:q4:patient selection|safety signals;q5:cohort study|randomized controlled trial",
		dynamicQuestionOptionsSingleflightKey("s1", "q7_evidence_quality", evidenceContext),
	)
	assert.Equal(t,
		"s1:q8_output_focus:q4:patient selection|safety signals;q5:cohort study|randomized controlled trial",
		dynamicQuestionOptionsSingleflightKey("s1", "q8_output_focus", evidenceContext),
	)

	changedEvidenceContext := map[string]any{
		"answers": map[string]any{
			"q4_subtopics": map[string]any{
				"values": []any{"safety signals", "patient selection"},
			},
			"q5_study_types": map[string]any{
				"values": []any{"observational study"},
			},
		},
	}
	assert.NotEqual(t,
		dynamicQuestionOptionsSingleflightKey("s1", "q7_evidence_quality", evidenceContext),
		dynamicQuestionOptionsSingleflightKey("s1", "q7_evidence_quality", changedEvidenceContext),
	)

	staleQuestion := map[string]any{
		"id":                "q7_evidence_quality",
		"optionsContextKey": "speculative",
		"options": []any{
			map[string]any{"value": "peer_reviewed_evidence", "label": "Peer-reviewed evidence"},
		},
	}
	assert.True(t, dynamicQuestionOptionsNeedRefresh(evidenceContext, staleQuestion, "q7_evidence_quality"))
	staleQuestion["optionsContextKey"] = "q4:patient selection|safety signals;q5:cohort study|randomized controlled trial"
	assert.False(t, dynamicQuestionOptionsNeedRefresh(evidenceContext, staleQuestion, "q7_evidence_quality"))
}

func TestAgentSessionIncludesQuestion(t *testing.T) {
	session := map[string]any{
		"questions": []any{
			map[string]any{"id": "q5_study_types"},
		},
		"questionSequence": []any{"q7_evidence_quality"},
	}

	assert.True(t, agentSessionIncludesQuestion(session, "q5_study_types"))
	assert.True(t, agentSessionIncludesQuestion(session, "q7_evidence_quality"))
	assert.False(t, agentSessionIncludesQuestion(session, "q8_output_focus"))
	assert.False(t, agentSessionIncludesQuestion(session, " "))
	assert.False(t, agentSessionIncludesQuestion(nil, "q5_study_types"))
}

func assertOptionsHaveDescriptions(t *testing.T, options []map[string]any) {
	t.Helper()
	require.NotEmpty(t, options)
	for _, option := range options {
		assert.NotEmpty(t, wisdev.AsOptionalString(option["label"]))
		assert.NotEmpty(t, wisdev.AsOptionalString(option["description"]), "option %q should include a UI description", wisdev.AsOptionalString(option["label"]))
	}
}

func optionLabels(options []map[string]any) []string {
	labels := make([]string, 0, len(options))
	for _, option := range options {
		if label := strings.TrimSpace(wisdev.AsOptionalString(option["label"])); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func TestDynamicQuestionOptionPayloadAddsDescriptionsForQ4ThroughQ8(t *testing.T) {
	for _, tc := range []struct {
		name       string
		questionID string
		value      string
		label      string
	}{
		{name: "q4 subtopic", questionID: "q4_subtopics", value: "Reward Modeling", label: "Reward Modeling"},
		{name: "q5 study type", questionID: "q5_study_types", value: "benchmark study", label: "Benchmark Study"},
		{name: "q7 evidence quality", questionID: "q7_evidence_quality", value: "peer_reviewed_evidence", label: "Peer-reviewed evidence"},
		{name: "q8 output focus", questionID: "q8_output_focus", value: "best_papers_first", label: "Best papers first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			option := dynamicQuestionOptionPayload(tc.questionID, tc.value, tc.label, "RLHF benchmark comparison", "ai")
			assert.Equal(t, tc.value, option["value"])
			assert.Equal(t, tc.label, option["label"])
			assert.NotEmpty(t, wisdev.AsOptionalString(option["description"]))
		})
	}
}

func TestDynamicQuestionFallbackOptionsUseQuestionContext(t *testing.T) {
	subtopics := []string{"reward modeling", "policy optimization"}
	studyTypes := []string{"benchmark study"}

	qualityOptions := defaultEvidenceQualityOptions("RLHF benchmark comparison", "ai", subtopics, studyTypes)
	assert.Contains(t, qualityOptions, "Human preference label reliability")
	assert.Contains(t, qualityOptions, "Reward model validation benchmarks")
	assert.Contains(t, qualityOptions, "Policy-optimization ablation evidence")
	assert.Contains(t, qualityOptions, "Benchmark protocol reproducibility")
	assert.Contains(t, qualityOptions, "Reward Modeling validation evidence")
	assert.Contains(t, qualityOptions, "Reward Modeling vs Policy Optimization evidence quality")
	assert.NotContains(t, qualityOptions, "Peer-reviewed evidence")
	assert.NotContains(t, qualityOptions, "Recent evidence")
	assert.NotEqual(t, "Peer-reviewed evidence", qualityOptions[0])

	outputOptions := defaultOutputFocusOptions("RLHF benchmark comparison", "ai", subtopics, studyTypes)
	assert.Contains(t, outputOptions, "Reward-model comparison takeaways")
	assert.Contains(t, outputOptions, "Preference-data failure modes")
	assert.Contains(t, outputOptions, "Benchmark leaderboard caveats")
	assert.Contains(t, outputOptions, "Reward Modeling evidence map")
	assert.Contains(t, outputOptions, "Reward Modeling vs Policy Optimization comparison")
	assert.NotContains(t, outputOptions, "Best papers first")
	assert.NotContains(t, outputOptions, "Broad coverage map")
	assert.NotEqual(t, "Best papers first", outputOptions[0])

	bioQuality := defaultEvidenceQualityOptions("EGFR inhibitor response in lung cancer", "biomedical", []string{"EGFR inhibitor response"})
	assert.Contains(t, bioQuality, "EGFR Inhibitor Response validation evidence")
	assert.NotContains(t, bioQuality, "Peer-reviewed evidence")

	bioOutput := defaultOutputFocusOptions("EGFR inhibitor response in lung cancer", "biomedical", []string{"EGFR inhibitor response"})
	assert.Contains(t, bioOutput, "EGFR Inhibitor Response evidence map")
	assert.NotContains(t, bioOutput, "Best papers first")
}

func TestDynamicQuestionOptionDescriptionsAreDomainSpecific(t *testing.T) {
	assert.Contains(t,
		dynamicQuestionOptionDescription("q4_subtopics", "Reward Modeling", "RLHF benchmark comparison", "ai"),
		"reward models",
	)
	assert.Contains(t,
		dynamicQuestionOptionDescription("q5_study_types", "human evaluation study", "RLHF benchmark comparison", "ai"),
		"human judgments",
	)
	assert.Contains(t,
		dynamicQuestionOptionDescription("q7_evidence_quality", "Reward model validation benchmarks", "RLHF benchmark comparison", "ai"),
		"held-out preferences",
	)
	assert.Contains(t,
		dynamicQuestionOptionDescription("q8_output_focus", "Benchmark leaderboard caveats", "RLHF benchmark comparison", "ai"),
		"headline benchmark scores",
	)
}

func TestRegisterQuestioningRoutes_NegativePaths(t *testing.T) {
	mux := http.NewServeMux()
	(&wisdevServer{}).registerQuestioningRoutes(mux, nil)

	t.Run("question options missing session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?questionId=q4_subtopics", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("question options method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/options", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("question recommendations missing ids", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId=s1", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("question options unavailable without runtime", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId=s1&questionId=q4_subtopics", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"SERVICE_UNAVAILABLE"`)
	})

	t.Run("question recommendations unavailable without runtime", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId=s1&questionId=q4_subtopics", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"SERVICE_UNAVAILABLE"`)
	})

	t.Run("regenerate invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("question regenerate unavailable without runtime", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"s1","questionId":"q4_subtopics"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"SERVICE_UNAVAILABLE"`)
	})
}

func TestRegisterQuestioningRoutes_AnalyzeQueryAllowsSlowHealthyAI(t *testing.T) {
	t.Setenv("PYTHON_SIDECAR_LLM_TRANSPORT", "http-json")
	llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llm/structured-output" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(3500 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonResult": `{"suggestedDomains":["cs"],"complexity":"simple","intent":"review","methodologyHints":["systematic comparison"],"reasoning":"slow-but-healthy sidecar response"}`,
		})
	}))
	defer llmServer.Close()
	t.Setenv("PYTHON_SIDECAR_HTTP_URL", llmServer.URL)

	gateway := &wisdev.AgentGateway{LLMClient: llm.NewClientWithTimeout(10 * time.Second)}
	mux := http.NewServeMux()
	(&wisdevServer{gateway: gateway}).registerQuestioningRoutes(mux, gateway)

	req := httptest.NewRequest(http.MethodPost, "/wisdev/analyze-query", bytes.NewBufferString(`{"query":"graph neural networks in medicine","traceId":"trace-slow-ai"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ai", rec.Header().Get("X-Analysis-Source"))
	assert.Empty(t, rec.Header().Get("X-Fallback-Reason"))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, false, payload["fallbackTriggered"])
	assert.Equal(t, "", payload["fallbackReason"])
	assert.Equal(t, "review", payload["intent"])
	assert.Equal(t, "simple", payload["complexity"])
	assert.Equal(t, []any{"cs"}, payload["suggested_domains"])
}

func TestBuildAgentSessionPreliminarySearchPayload_Extra(t *testing.T) {
	t.Run("nil inputs return defaults", func(t *testing.T) {
		payload := buildAgentSessionPreliminarySearchPayload(context.Background(), nil, nil)
		assert.Equal(t, 0, payload["totalCount"])
		assert.Empty(t, payload["perSubtopic"])
	})

	t.Run("empty query returns defaults", func(t *testing.T) {
		payload := buildAgentSessionPreliminarySearchPayload(context.Background(), internalsearch.NewProviderRegistry(), map[string]any{
			"correctedQuery": "   ",
			"originalQuery":  "",
		})
		assert.Equal(t, 0, payload["totalCount"])
		assert.Empty(t, payload["perSubtopic"])
	})

	t.Run("truncates subtopics to five keys", func(t *testing.T) {
		reg := internalsearch.NewProviderRegistry()
		session := map[string]any{
			"correctedQuery": "machine learning",
			"questions": []any{
				map[string]any{
					"type": "subtopics",
					"options": []any{
						map[string]any{"value": "one"},
						map[string]any{"value": "two"},
						map[string]any{"value": "three"},
						map[string]any{"value": "four"},
						map[string]any{"value": "five"},
						map[string]any{"value": "six"},
					},
				},
			},
		}

		payload := buildAgentSessionPreliminarySearchPayload(context.Background(), reg, session)
		assert.Equal(t, 0, payload["totalCount"])

		perSubtopic, ok := payload["perSubtopic"].(map[string]int)
		assert.True(t, ok)
		assert.Len(t, perSubtopic, 5)
		assert.Contains(t, perSubtopic, "one")
		assert.Contains(t, perSubtopic, "five")
		assert.NotContains(t, perSubtopic, "six")
	})

	t.Run("uses id fallback for blank values and skips fully blank options", func(t *testing.T) {
		reg := internalsearch.NewProviderRegistry()
		session := map[string]any{
			"correctedQuery": "machine learning",
			"questions": []any{
				map[string]any{
					"type": "subtopics",
					"options": []any{
						map[string]any{"id": "fallback-id", "label": "Fallback Label"},
						map[string]any{"value": "label-only", "label": ""},
						map[string]any{"value": "background", "label": "Background"},
						map[string]any{"id": "", "value": "", "label": ""},
					},
				},
			},
		}

		payload := buildAgentSessionPreliminarySearchPayload(context.Background(), reg, session)
		assert.Equal(t, 0, payload["totalCount"])

		perSubtopic, ok := payload["perSubtopic"].(map[string]int)
		require.True(t, ok)
		assert.Contains(t, perSubtopic, "fallback-id")
		assert.Contains(t, perSubtopic, "label-only")
		assert.Contains(t, perSubtopic, "background")
		assert.NotContains(t, perSubtopic, "")
		assert.Len(t, perSubtopic, 3)
	})
}

func TestRegisterQuestioningRoutes_WithSeededSession(t *testing.T) {
	isolateQuestioningTestStateDir(t)
	gw := wisdev.NewAgentGateway(nil, nil, nil)
	require.NotNil(t, gw.StateStore)

	sessionID := "sess-questioning-extra"
	userID := "u1"
	payload := map[string]any{
		"sessionId":      sessionID,
		"userId":         userID,
		"correctedQuery": "machine learning",
		"originalQuery":  "machine learning",
		"detectedDomain": "cs",
		"questions": []any{
			map[string]any{
				"id":            "q4_subtopics",
				"type":          "subtopics",
				"options":       []any{},
				"optionsSource": "stored",
			},
			map[string]any{
				"id":                "q5_study_types",
				"type":              "study_types",
				"optionsContextKey": "q4:background",
				"options": []any{
					map[string]any{"value": "empirical", "label": "Empirical Evaluation"},
				},
			},
		},
		"answers": map[string]any{
			"q4_subtopics": map[string]any{
				"values": []any{"background"},
			},
		},
	}
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, userID, payload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-questioning-extra",
		SessionID: sessionID,
		UserID:    userID,
		Status:    "completed",
	}))

	mux := http.NewServeMux()
	(&wisdevServer{gateway: gw}).registerQuestioningRoutes(mux, gw)

	t.Run("question options empty id returns empty payload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID, nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":""`)
	})

	t.Run("question options for stored question", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q5_study_types"`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assertOptionsHaveDescriptions(t, sliceAnyMap(payload["options"]))
	})

	t.Run("question options generate dynamically for subtopics", func(t *testing.T) {
		originalClient := gw.LLMClient
		gw.LLMClient = nil
		t.Cleanup(func() { gw.LLMClient = originalClient })

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=q4_subtopics", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q4_subtopics"`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assertOptionsHaveDescriptions(t, sliceAnyMap(payload["options"]))
	})

	t.Run("question options refresh stale context-sensitive stored options", func(t *testing.T) {
		originalClient := gw.LLMClient
		gw.LLMClient = nil
		t.Cleanup(func() { gw.LLMClient = originalClient })

		staleSessionID := "sess-questioning-stale-q5-options"
		stalePayload := map[string]any{
			"sessionId":      staleSessionID,
			"userId":         userID,
			"correctedQuery": "RLHF benchmark comparison",
			"originalQuery":  "RLHF benchmark comparison",
			"detectedDomain": "ai",
			"questions": []any{
				map[string]any{
					"id":                "q5_study_types",
					"type":              "study_types",
					"optionsContextKey": "speculative",
					"options": []any{
						map[string]any{"value": "generic_stale", "label": "Generic stale option"},
					},
				},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"reward modeling", "policy optimization"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(staleSessionID, userID, stalePayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-stale-q5-options",
			SessionID: staleSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+staleSessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "Generic stale option")

		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		options := sliceAnyMap(payload["options"])
		assertOptionsHaveDescriptions(t, options)
		assert.NotEmpty(t, options)

		latest, err := gw.StateStore.LoadAgentSession(staleSessionID)
		require.NoError(t, err)
		for _, question := range sliceAnyMap(latest["questions"]) {
			if wisdev.AsOptionalString(question["id"]) == "q5_study_types" {
				assert.Equal(t, "q4:policy optimization|reward modeling", wisdev.AsOptionalString(question["optionsContextKey"]))
				assert.NotEmpty(t, sliceAnyMap(question["options"]))
				return
			}
		}
		t.Fatal("q5_study_types question not persisted")
	})

	t.Run("question options first read uses structured ai for subtopics and study types", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
		q4SessionID := "sess-questioning-ai-q4-first-read"
		q4Payload := map[string]any{
			"sessionId":      q4SessionID,
			"userId":         userID,
			"correctedQuery": "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"originalQuery":  "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"detectedDomain": "biomedical research",
			"questions": []any{
				map[string]any{"id": "q4_subtopics", "type": "subtopics", "options": []any{}},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(q4SessionID, userID, q4Payload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-ai-q4-first-read",
			SessionID: q4SessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		q5SessionID := "sess-questioning-ai-q5-first-read"
		q5Payload := map[string]any{
			"sessionId":      q5SessionID,
			"userId":         userID,
			"correctedQuery": "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"originalQuery":  "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"detectedDomain": "biomedical research",
			"questions": []any{
				map[string]any{"id": "q5_study_types", "type": "study_types", "options": []any{}},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"single-cell perturbation screens", "glioblastoma biomarkers"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(q5SessionID, userID, q5Payload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-ai-q5-first-read",
			SessionID: q5SessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		prompts := []string{}
		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			var captured structuredRequestCapture
			require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			prompts = append(prompts, captured.Prompt)
			switch {
			case strings.Contains(captured.Prompt, "research subtopics"):
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"jsonResult": `{"subtopics":["Single-cell perturbation screens","Glioblastoma biomarker validation","Tumor subtype context"],"keywords":["CRISPR","single-cell","glioblastoma"],"queryVariations":["single-cell CRISPR perturbation biomarkers validation","glioblastoma subtype biomarker perturbation screens"],"explanation":"AI inferred subtopics from the biomedical query."}`,
					"modelUsed":  "mock-biomedical-q4",
				}))
			case strings.Contains(captured.Prompt, "study types"):
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"jsonResult": `{"studyTypes":["single-cell perturbation experiment","prospective cohort validation study","case-control biomarker study"],"matchedSignals":["single-cell screens","biomarker validation"],"explanation":"AI inferred study designs from selected subtopics."}`,
					"modelUsed":  "mock-biomedical-q5",
				}))
			default:
				t.Fatalf("unexpected structured prompt: %s", captured.Prompt)
			}
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalClient := gw.LLMClient
		gw.LLMClient = client
		t.Cleanup(func() { gw.LLMClient = originalClient })

		q4Req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+q4SessionID+"&questionId=q4_subtopics", nil)
		q4Req = q4Req.WithContext(context.WithValue(q4Req.Context(), contextKey("user_id"), userID))
		q4W := httptest.NewRecorder()
		mux.ServeHTTP(q4W, q4Req)
		require.Equal(t, http.StatusOK, q4W.Code)
		var q4Response map[string]any
		require.NoError(t, json.Unmarshal(q4W.Body.Bytes(), &q4Response))
		assert.Equal(t, "llm_structured", q4Response["source"])
		assert.Contains(t, optionLabels(sliceAnyMap(q4Response["options"])), "Single-cell perturbation screens")

		q5Req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+q5SessionID+"&questionId=q5_study_types", nil)
		q5Req = q5Req.WithContext(context.WithValue(q5Req.Context(), contextKey("user_id"), userID))
		q5W := httptest.NewRecorder()
		mux.ServeHTTP(q5W, q5Req)
		require.Equal(t, http.StatusOK, q5W.Code)
		var q5Response map[string]any
		require.NoError(t, json.Unmarshal(q5W.Body.Bytes(), &q5Response))
		assert.Equal(t, "llm_structured", q5Response["source"])
		assert.Contains(t, optionLabels(sliceAnyMap(q5Response["options"])), "single-cell perturbation experiment")

		require.Len(t, prompts, 2)
		assert.Contains(t, prompts[0], "single-cell CRISPR perturbation biomarkers")
		assert.Contains(t, prompts[1], "single-cell perturbation screens")
	})

	t.Run("question options missing question returns empty payload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=missing_question", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"missing_question"`)
		assert.Contains(t, w.Body.String(), `"options":[]`)
	})

	t.Run("question recommendations use stored answer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=q4_subtopics", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"source":"session"`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, false, payload["fallbackTriggered"])
		assert.Equal(t, "", payload["fallbackReason"])
	})

	t.Run("question recommendations missing question uses heuristic empty payload", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=missing_question", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"missing_question"`)
		assert.Contains(t, w.Body.String(), `"values":[]`)
	})

	t.Run("question recommendations fallback heuristically", func(t *testing.T) {
		originalBrain := gw.Brain
		originalClient := gw.LLMClient
		gw.Brain = nil
		gw.LLMClient = nil
		t.Cleanup(func() {
			gw.Brain = originalBrain
			gw.LLMClient = originalClient
		})

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+sessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q5_study_types"`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "ai_unavailable", payload["fallbackReason"])
		assert.Equal(t, "ai_unavailable", w.Header().Get("X-Fallback-Reason"))
	})

	t.Run("question recommendations use ai suggestions", func(t *testing.T) {
		aiSessionID := "sess-questioning-extra-ai"
		aiPayload := map[string]any{
			"sessionId":      aiSessionID,
			"userId":         userID,
			"correctedQuery": "machine learning",
			"originalQuery":  "machine learning",
			"detectedDomain": "cs",
			"questions": []any{
				map[string]any{
					"id":   "q5_study_types",
					"type": "study_types",
					"options": []any{
						map[string]any{"value": "benchmark", "label": "Benchmark"},
						map[string]any{"value": "empirical", "label": "Empirical Evaluation"},
					},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(aiSessionID, userID, aiPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-extra-ai",
			SessionID: aiSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonResult":   `{"values":["empirical"],"explanation":"best match"}`,
				"modelUsed":    "mock-model",
				"inputTokens":  1,
				"outputTokens": 1,
				"schemaValid":  true,
				"latencyMs":    1,
			})
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalBrain := gw.Brain
		gw.Brain = wisdev.NewBrainCapabilities(client)
		t.Cleanup(func() { gw.Brain = originalBrain })

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+aiSessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"source":"ai"`)
		assert.Contains(t, w.Body.String(), `"values":["empirical"]`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, false, payload["fallbackTriggered"])
		assert.Equal(t, "", payload["fallbackReason"])
	})

	t.Run("question recommendations rehydrate brain from llm client", func(t *testing.T) {
		aiSessionID := "sess-questioning-extra-ai-rehydrate"
		aiPayload := map[string]any{
			"sessionId":      aiSessionID,
			"userId":         userID,
			"correctedQuery": "machine learning",
			"originalQuery":  "machine learning",
			"detectedDomain": "cs",
			"questions": []any{
				map[string]any{
					"id":   "q5_study_types",
					"type": "study_types",
					"options": []any{
						map[string]any{"value": "benchmark", "label": "Benchmark"},
						map[string]any{"value": "empirical", "label": "Empirical Evaluation"},
					},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(aiSessionID, userID, aiPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-extra-ai-rehydrate",
			SessionID: aiSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonResult":   `{"values":["empirical"],"explanation":"rehydrated brain"}`,
				"modelUsed":    "mock-model",
				"inputTokens":  1,
				"outputTokens": 1,
				"schemaValid":  true,
				"latencyMs":    1,
			})
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)

		originalBrain := gw.Brain
		originalClient := gw.LLMClient
		gw.Brain = nil
		gw.LLMClient = client
		t.Cleanup(func() {
			gw.Brain = originalBrain
			gw.LLMClient = originalClient
		})

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+aiSessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"source":"ai"`)
		assert.Contains(t, w.Body.String(), `"values":["empirical"]`)
		require.NotNil(t, gw.Brain)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, false, payload["fallbackTriggered"])
		assert.Equal(t, "", payload["fallbackReason"])
	})

	t.Run("question recommendations ai fallback on error", func(t *testing.T) {
		aiSessionID := "sess-questioning-extra-ai-fallback"
		aiPayload := map[string]any{
			"sessionId":      aiSessionID,
			"userId":         userID,
			"correctedQuery": "machine learning",
			"originalQuery":  "machine learning",
			"detectedDomain": "cs",
			"questions": []any{
				map[string]any{
					"id":   "q5_study_types",
					"type": "study_types",
					"options": []any{
						map[string]any{"value": "benchmark", "label": "Benchmark"},
						map[string]any{"value": "empirical", "label": "Empirical Evaluation"},
					},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(aiSessionID, userID, aiPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-extra-ai-fallback",
			SessionID: aiSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalBrain := gw.Brain
		gw.Brain = wisdev.NewBrainCapabilities(client)
		t.Cleanup(func() { gw.Brain = originalBrain })

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+aiSessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"source":"heuristic"`)
		assert.Contains(t, w.Body.String(), `fallback recommendations from the current option set`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "ai_request_failed", payload["fallbackReason"])
		assert.Equal(t, "ai_request_failed", w.Header().Get("X-Fallback-Reason"))
	})

	t.Run("question recommendations honors bounded ai timeout", func(t *testing.T) {
		previousTimeout := wisdevQuestionRecommendationTimeout
		wisdevQuestionRecommendationTimeout = 25 * time.Millisecond
		t.Cleanup(func() {
			wisdevQuestionRecommendationTimeout = previousTimeout
		})

		aiSessionID := "sess-questioning-extra-ai-timeout"
		aiPayload := map[string]any{
			"sessionId":      aiSessionID,
			"userId":         userID,
			"correctedQuery": "machine learning",
			"originalQuery":  "machine learning",
			"detectedDomain": "cs",
			"questions": []any{
				map[string]any{
					"id":   "q5_study_types",
					"type": "study_types",
					"options": []any{
						map[string]any{"value": "benchmark", "label": "Benchmark"},
						map[string]any{"value": "empirical", "label": "Empirical Evaluation"},
					},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(aiSessionID, userID, aiPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-extra-ai-timeout",
			SessionID: aiSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			select {
			case <-time.After(200 * time.Millisecond):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonResult": `{"values":["empirical"],"explanation":"late match"}`,
					"modelUsed":  "mock-model",
				})
			case <-r.Context().Done():
				return
			}
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalBrain := gw.Brain
		gw.Brain = wisdev.NewBrainCapabilities(client)
		t.Cleanup(func() { gw.Brain = originalBrain })

		start := time.Now()
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+aiSessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		elapsed := time.Since(start)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Less(t, elapsed, time.Second)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, "heuristic", payload["source"])
		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "ai_request_failed", payload["fallbackReason"])
		assert.Equal(t, "ai_request_failed", w.Header().Get("X-Fallback-Reason"))
	})

	t.Run("question recommendations dynamically seed empty options", func(t *testing.T) {
		originalBrain := gw.Brain
		originalClient := gw.LLMClient
		gw.Brain = nil
		gw.LLMClient = nil
		t.Cleanup(func() {
			gw.Brain = originalBrain
			gw.LLMClient = originalClient
		})

		dynSessionID := "sess-questioning-dynamic-rec"
		dynPayload := map[string]any{
			"sessionId":      dynSessionID,
			"userId":         userID,
			"correctedQuery": "machine learning",
			"originalQuery":  "machine learning",
			"questions": []any{
				map[string]any{
					"id":      "q6_exclusions",
					"type":    "exclusions",
					"options": []any{},
				},
			},
			"answers": map[string]any{},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(dynSessionID, userID, dynPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-dyn-rec",
			SessionID: dynSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+dynSessionID+"&questionId=q6_exclusions", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q6_exclusions"`)
		assert.Contains(t, w.Body.String(), `"source":"heuristic"`)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, true, payload["fallbackTriggered"])
		assert.Equal(t, "ai_unavailable", payload["fallbackReason"])
		assert.Equal(t, "ai_unavailable", w.Header().Get("X-Fallback-Reason"))
		assert.NotEmpty(t, sliceStrings(payload["values"]))
	})

	t.Run("question regenerate returns stored options", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q5_study_types"}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q5_study_types"`)
	})

	t.Run("question regenerate seeds dynamic options on empty question", func(t *testing.T) {
		dynSessionID := "sess-questioning-dynamic-regen"
		dynPayload := map[string]any{
			"sessionId":      dynSessionID,
			"userId":         userID,
			"correctedQuery": "alignment robustness benchmarks evaluation interpretability",
			"originalQuery":  "alignment robustness benchmarks evaluation interpretability",
			"questions": []any{
				map[string]any{
					"id":      "q4_subtopics",
					"type":    "subtopics",
					"options": []any{},
				},
				map[string]any{
					"id":      "q5_study_types",
					"type":    "study_types",
					"options": []any{},
				},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"alignment", "robustness"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(dynSessionID, userID, dynPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-dyn-regen",
			SessionID: dynSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q4_subtopics","previousOptions":["Alignment","Robustness"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q4_subtopics"`)
		assert.Contains(t, w.Body.String(), `"options":`)
		assert.NotContains(t, w.Body.String(), `"label":"Alignment"`)
		assert.NotContains(t, w.Body.String(), `"label":"Robustness"`)
		var q4Payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &q4Payload))
		assertOptionsHaveDescriptions(t, sliceAnyMap(q4Payload["options"]))

		q5Req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q5_study_types"}`))
		q5Req = q5Req.WithContext(context.WithValue(q5Req.Context(), contextKey("user_id"), userID))
		q5w := httptest.NewRecorder()
		mux.ServeHTTP(q5w, q5Req)
		assert.Equal(t, http.StatusOK, q5w.Code)
		assert.Contains(t, q5w.Body.String(), `"questionId":"q5_study_types"`)
		var q5Payload map[string]any
		require.NoError(t, json.Unmarshal(q5w.Body.Bytes(), &q5Payload))
		q5Options := sliceAnyMap(q5Payload["options"])
		assertOptionsHaveDescriptions(t, q5Options)
		assert.NotEmpty(t, q5Options)
	})

	t.Run("question regenerate supports evidence quality and output focus", func(t *testing.T) {
		originalBrain := gw.Brain
		originalClient := gw.LLMClient
		gw.Brain = nil
		gw.LLMClient = nil
		t.Cleanup(func() {
			gw.Brain = originalBrain
			gw.LLMClient = originalClient
		})

		dynSessionID := "sess-questioning-q7-q8-regen"
		dynPayload := map[string]any{
			"sessionId":      dynSessionID,
			"userId":         userID,
			"correctedQuery": "clinical AI benchmark comparison safety",
			"originalQuery":  "clinical AI benchmark comparison safety",
			"detectedDomain": "medicine",
			"questions": []any{
				map[string]any{
					"id":      "q7_evidence_quality",
					"type":    "clarification",
					"options": []any{},
				},
				map[string]any{
					"id":      "q8_output_focus",
					"type":    "clarification",
					"options": []any{},
				},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"patient selection", "safety signals"},
				},
				"q5_study_types": map[string]any{
					"values": []any{"randomized controlled trial"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(dynSessionID, userID, dynPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-q7-q8-regen",
			SessionID: dynSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q7_evidence_quality","previousOptions":["Peer-reviewed evidence"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q7_evidence_quality"`)
		assert.Contains(t, w.Body.String(), `"options":`)
		assert.NotContains(t, w.Body.String(), `"label":"Peer-reviewed evidence"`)
		var q7Payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &q7Payload))
		q7Options := sliceAnyMap(q7Payload["options"])
		assertOptionsHaveDescriptions(t, q7Options)
		assert.Contains(t, optionLabels(q7Options), "Benchmark protocol reproducibility")
		assert.Contains(t, optionLabels(q7Options), "Randomized or controlled clinical evidence")

		req2 := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q8_output_focus","previousOptions":["Best papers first"]}`))
		req2 = req2.WithContext(context.WithValue(req2.Context(), contextKey("user_id"), userID))
		w2 := httptest.NewRecorder()
		mux.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)
		assert.Contains(t, w2.Body.String(), `"questionId":"q8_output_focus"`)
		assert.Contains(t, w2.Body.String(), `"options":`)
		assert.NotContains(t, w2.Body.String(), `"label":"Best papers first"`)
		var q8Payload map[string]any
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &q8Payload))
		q8Options := sliceAnyMap(q8Payload["options"])
		assertOptionsHaveDescriptions(t, q8Options)
		assert.Contains(t, optionLabels(q8Options), "Benchmark leaderboard caveats")
		assert.Contains(t, optionLabels(q8Options), "Clinical relevance and safety")
	})

	t.Run("question regenerate uses structured ai for biomedical evidence and output options", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
		dynSessionID := "sess-questioning-biomedical-ai-q7-q8"
		dynPayload := map[string]any{
			"sessionId":      dynSessionID,
			"userId":         userID,
			"correctedQuery": "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"originalQuery":  "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"detectedDomain": "biomedical research",
			"questions": []any{
				map[string]any{"id": "q7_evidence_quality", "type": "clarification", "options": []any{}},
				map[string]any{"id": "q8_output_focus", "type": "clarification", "options": []any{}},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"single-cell perturbation screens", "glioblastoma biomarkers"},
				},
				"q5_study_types": map[string]any{
					"values": []any{"experimental study", "cohort study"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(dynSessionID, userID, dynPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-biomedical-ai-q7-q8",
			SessionID: dynSessionID,
			UserID:    userID,
			Status:    "completed",
		}))
		getSessionID := "sess-questioning-biomedical-ai-q7-get"
		getPayload := map[string]any{
			"sessionId":      getSessionID,
			"userId":         userID,
			"correctedQuery": "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"originalQuery":  "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"detectedDomain": "biomedical research",
			"questions": []any{
				map[string]any{"id": "q7_evidence_quality", "type": "clarification", "options": []any{}},
			},
			"answers": dynPayload["answers"],
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(getSessionID, userID, getPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-biomedical-ai-q7-get",
			SessionID: getSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		prompts := []string{}
		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			var captured structuredRequestCapture
			require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			prompts = append(prompts, captured.Prompt)
			switch {
			case strings.Contains(captured.Prompt, "q7_evidence_quality"):
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"jsonResult": `{"options":[{"label":"Single-cell assay reproducibility","description":"Check whether perturbation effects replicate across cells, batches, and orthogonal assays."},{"label":"CRISPR perturbation specificity","description":"Prefer studies that control off-target effects and validate guide-level specificity."},{"label":"Tumor cohort annotation quality","description":"Prioritize biomarker evidence tied to clear glioblastoma cohort metadata and pathology labels."},{"label":"Mechanism validation evidence","description":"Favor papers that connect candidate biomarkers to functional tumor biology."}],"explanation":"AI inferred biomedical evidence-quality criteria from the query."}`,
					"modelUsed":  "mock-biomedical-options",
				}))
			case strings.Contains(captured.Prompt, "q8_output_focus"):
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"jsonResult": `{"options":[{"label":"Mechanism-to-translation synthesis","description":"Organize the answer around which perturbation findings plausibly translate to biomarker use."},{"label":"Assay and cohort limitations","description":"Separate biological signal from single-cell assay limits and cohort composition risks."},{"label":"Biomarker validation roadmap","description":"End with validation steps needed before clinical or translational use."},{"label":"Conflicting perturbation signals","description":"Call out disagreements across perturbation screens, cell states, and tumor contexts."}],"explanation":"AI inferred biomedical output-focus criteria from the query."}`,
					"modelUsed":  "mock-biomedical-options",
				}))
			default:
				t.Fatalf("unexpected structured prompt: %s", captured.Prompt)
			}
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalClient := gw.LLMClient
		gw.LLMClient = client
		t.Cleanup(func() { gw.LLMClient = originalClient })

		getReq := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+getSessionID+"&questionId=q7_evidence_quality", nil)
		getReq = getReq.WithContext(context.WithValue(getReq.Context(), contextKey("user_id"), userID))
		getW := httptest.NewRecorder()
		mux.ServeHTTP(getW, getReq)
		require.Equal(t, http.StatusOK, getW.Code)
		var q7GetPayload map[string]any
		require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &q7GetPayload))
		assert.Equal(t, "llm_structured", q7GetPayload["source"])
		assert.Equal(t, false, q7GetPayload["fallbackTriggered"])
		assert.Contains(t, optionLabels(sliceAnyMap(q7GetPayload["options"])), "Single-cell assay reproducibility")

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q7_evidence_quality","previousOptions":["Peer-reviewed evidence"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var q7Payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &q7Payload))
		assert.Equal(t, "llm_structured", q7Payload["source"])
		assert.Equal(t, false, q7Payload["fallbackTriggered"])
		q7Options := sliceAnyMap(q7Payload["options"])
		assertOptionsHaveDescriptions(t, q7Options)
		assert.Contains(t, optionLabels(q7Options), "Single-cell assay reproducibility")
		assert.NotContains(t, optionLabels(q7Options), "Peer-reviewed evidence")

		req2 := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+dynSessionID+`","questionId":"q8_output_focus","previousOptions":["Best papers first"]}`))
		req2 = req2.WithContext(context.WithValue(req2.Context(), contextKey("user_id"), userID))
		w2 := httptest.NewRecorder()
		mux.ServeHTTP(w2, req2)
		require.Equal(t, http.StatusOK, w2.Code)
		var q8Payload map[string]any
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &q8Payload))
		assert.Equal(t, "llm_structured", q8Payload["source"])
		assert.Equal(t, false, q8Payload["fallbackTriggered"])
		q8Options := sliceAnyMap(q8Payload["options"])
		assertOptionsHaveDescriptions(t, q8Options)
		assert.Contains(t, optionLabels(q8Options), "Mechanism-to-translation synthesis")
		assert.NotContains(t, optionLabels(q8Options), "Best papers first")

		require.Len(t, prompts, 3)
		for _, prompt := range prompts {
			assert.Contains(t, prompt, "Do not hard-code domain playbooks")
			assert.Contains(t, prompt, "biomedical research")
			assert.Contains(t, prompt, "single-cell CRISPR perturbation biomarkers")
		}
	})

	t.Run("question options refresh exclusions with structured ai for biomedical context", func(t *testing.T) {
		t.Setenv("INTERNAL_SERVICE_KEY", "test-key")
		dynSessionID := "sess-questioning-biomedical-ai-q6"
		dynPayload := map[string]any{
			"sessionId":      dynSessionID,
			"userId":         userID,
			"correctedQuery": "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"originalQuery":  "single-cell CRISPR perturbation biomarkers in glioblastoma",
			"detectedDomain": "biomedical research",
			"questions": []any{
				map[string]any{
					"id":                "q6_exclusions",
					"type":              "exclusions",
					"optionsContextKey": "",
					"options": []any{
						map[string]any{"value": "animal_studies", "label": "Exclude animal studies"},
					},
				},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"single-cell perturbation screens", "glioblastoma biomarkers"},
				},
				"q5_study_types": map[string]any{
					"values": []any{"single-cell perturbation experiment", "prospective cohort study"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(dynSessionID, userID, dynPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-biomedical-ai-q6",
			SessionID: dynSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		var captured structuredRequestCapture
		llmServer := newLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/llm/structured-output", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"jsonResult": `{"options":[{"label":"Exclude xenograft-only evidence","description":"Filter animal-only tumor models unless they directly validate the perturbation mechanism."},{"label":"Exclude cohorts without tumor subtype labels","description":"Down-rank biomarker studies that lack glioblastoma subtype or pathology metadata."},{"label":"Exclude unvalidated assay-only findings","description":"Filter exploratory single-cell signals without orthogonal validation."}],"explanation":"AI inferred biomedical exclusions from the query and selected study types."}`,
				"modelUsed":  "mock-biomedical-exclusions",
			}))
		}))
		defer llmServer.Close()

		client := llm.NewClientWithTimeout(2 * time.Second)
		clientValue := reflect.ValueOf(client).Elem()
		transportField := clientValue.FieldByName("transport")
		reflect.NewAt(transportField.Type(), unsafe.Pointer(transportField.UnsafeAddr())).Elem().SetString("http-json")
		baseURLField := clientValue.FieldByName("httpBaseURL")
		reflect.NewAt(baseURLField.Type(), unsafe.Pointer(baseURLField.UnsafeAddr())).Elem().SetString(llmServer.URL)
		originalClient := gw.LLMClient
		gw.LLMClient = client
		t.Cleanup(func() { gw.LLMClient = originalClient })

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+dynSessionID+"&questionId=q6_exclusions", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		assert.Equal(t, "llm_structured", payload["source"])
		assert.Equal(t, false, payload["fallbackTriggered"])
		options := sliceAnyMap(payload["options"])
		assertOptionsHaveDescriptions(t, options)
		assert.Contains(t, optionLabels(options), "Exclude xenograft-only evidence")
		assert.NotContains(t, optionLabels(options), "Exclude animal studies")
		assert.Contains(t, captured.Prompt, "q6_exclusions")
		assert.Contains(t, captured.Prompt, "fixed checklist")
	})

	t.Run("question recommendations refresh stale evidence quality options", func(t *testing.T) {
		originalBrain := gw.Brain
		originalClient := gw.LLMClient
		gw.Brain = nil
		gw.LLMClient = nil
		t.Cleanup(func() {
			gw.Brain = originalBrain
			gw.LLMClient = originalClient
		})

		staleSessionID := "sess-questioning-stale-q7-rec"
		stalePayload := map[string]any{
			"sessionId":      staleSessionID,
			"userId":         userID,
			"correctedQuery": "clinical AI benchmark comparison safety",
			"originalQuery":  "clinical AI benchmark comparison safety",
			"detectedDomain": "medicine",
			"questions": []any{
				map[string]any{
					"id":                "q7_evidence_quality",
					"type":              "clarification",
					"isMultiSelect":     true,
					"optionsContextKey": "speculative",
					"options": []any{
						map[string]any{"value": "peer_reviewed_evidence", "label": "Peer-reviewed evidence"},
					},
				},
			},
			"answers": map[string]any{
				"q4_subtopics": map[string]any{
					"values": []any{"patient selection", "safety signals"},
				},
				"q5_study_types": map[string]any{
					"values": []any{"randomized controlled trial"},
				},
			},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(staleSessionID, userID, stalePayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-questioning-stale-q7-rec",
			SessionID: staleSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/recommendations?sessionId="+staleSessionID+"&questionId=q7_evidence_quality", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var payload map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
		values := sliceStrings(payload["values"])
		assert.Contains(t, values, "benchmark_protocol_reproducibility")
		assert.NotContains(t, values, "peer_reviewed_evidence")

		latest, err := gw.StateStore.LoadAgentSession(staleSessionID)
		require.NoError(t, err)
		for _, question := range sliceAnyMap(latest["questions"]) {
			if wisdev.AsOptionalString(question["id"]) == "q7_evidence_quality" {
				assert.Equal(t, "q4:patient selection|safety signals;q5:randomized controlled trial", wisdev.AsOptionalString(question["optionsContextKey"]))
				assert.Contains(t, optionLabels(sliceAnyMap(question["options"])), "Benchmark protocol reproducibility")
				return
			}
		}
		t.Fatal("q7_evidence_quality question not persisted")
	})

	t.Run("question regenerate missing ids rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/regenerate", bytes.NewBufferString(`{"sessionId":"`+sessionID+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("preliminary search owner denied", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/session/preliminary-search", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","userId":"someone-else"}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("preliminary search ok", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/session/preliminary-search", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","userId":"`+userID+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"preliminarySearch"`)
		assert.Contains(t, w.Body.String(), `"totalCount":`)
	})

	t.Run("question options unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/options?sessionId="+sessionID+"&questionId=q5_study_types", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("next question via get uses query session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/next?sessionId="+sessionID+"&useAdaptiveOrdering=true", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"question":`)
		assert.Contains(t, w.Body.String(), `"ok":true`)
	})

	t.Run("next question via post falls back to query session id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/next?sessionId="+sessionID, bytes.NewBufferString(`{"useAdaptiveOrdering":true}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"question":`)
	})

	t.Run("next question owner denied", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/next?sessionId="+sessionID, nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("next question missing session returns not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wisdev/question/next?sessionId=missing", nil)
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("next question invalid body is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/next", bytes.NewBufferString(`{`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("next question method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/wisdev/question/next", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestRegisterQuestioningRoutes_ProcessAnswerValidationBranches(t *testing.T) {
	isolateQuestioningTestStateDir(t)
	gw := wisdev.NewAgentGateway(nil, nil, nil)
	require.NotNil(t, gw.StateStore)

	sessionID := "sess-question-answer-branches"
	userID := "u-answer"
	basePayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"correctedQuery":       "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"detectedDomain":       "social",
		"status":               "questioning",
		"currentQuestionIndex": 0,
		"questionSequence":     []string{"q1_domain", "q2_scope"},
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, userID, basePayload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-answer-branches",
		SessionID: sessionID,
		UserID:    userID,
		Status:    "completed",
	}))

	immutableSessionID := "sess-question-answer-immutable"
	immutablePayload := map[string]any{
		"sessionId":            immutableSessionID,
		"userId":               userID,
		"correctedQuery":       "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"detectedDomain":       "social",
		"status":               "completed",
		"currentQuestionIndex": 1,
		"questionSequence":     []string{"q1_domain", "q2_scope"},
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(immutableSessionID, userID, immutablePayload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-answer-branches-immutable",
		SessionID: immutableSessionID,
		UserID:    userID,
		Status:    "completed",
	}))

	mux := http.NewServeMux()
	(&wisdevServer{gateway: gw}).registerQuestioningRoutes(mux, gw)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/wisdev/question/answer", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("missing question id is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","values":["a"],"displayValues":["A"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid values slice is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q1_domain","values":[" "],"displayValues":["A"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid display values slice is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q1_domain","values":["a"],"displayValues":[" "]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("empty required answer is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q1_domain","values":[],"displayValues":[]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "required question answer must include at least one value")
	})

	t.Run("empty optional answer is accepted", func(t *testing.T) {
		optionalSessionID := "sess-question-answer-optional-empty"
		optionalPayload := cloneAnyMap(basePayload)
		optionalPayload["sessionId"] = optionalSessionID
		optionalPayload["currentQuestionIndex"] = 1
		optionalPayload["questionSequence"] = []string{"q1_domain", "q2_scope"}
		optionalPayload["questions"] = []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?", "isRequired": true},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?", "isRequired": false},
		}
		require.NoError(t, gw.StateStore.PersistAgentSessionMutation(optionalSessionID, userID, optionalPayload, wisdev.RuntimeJournalEntry{
			EventID:   "evt-answer-branches-optional-empty",
			SessionID: optionalSessionID,
			UserID:    userID,
			Status:    "completed",
		}))

		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+optionalSessionID+`","questionId":"q2_scope","values":[],"displayValues":[]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("missing session returns not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"missing","questionId":"q1_domain","values":["a"],"displayValues":["A"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("owner denied returns forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q1_domain","values":["a"],"displayValues":["A"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), "u2"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("immutable session returns conflict", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+immutableSessionID+`","questionId":"q1_domain","values":["a"],"displayValues":["A"]}`))
		req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestRegisterQuestioningRoutes_ProcessAnswerSuccessBranches(t *testing.T) {
	isolateQuestioningTestStateDir(t)
	gw := wisdev.NewAgentGateway(nil, nil, nil)
	require.NotNil(t, gw.StateStore)

	sessionID := "sess-question-answer-success"
	userID := "u-answer-success"
	basePayload := map[string]any{
		"sessionId":            sessionID,
		"userId":               userID,
		"correctedQuery":       "sleep interventions adults",
		"originalQuery":        "sleep interventions adults",
		"detectedDomain":       "social",
		"status":               "questioning",
		"currentQuestionIndex": 0,
		"questionSequence":     []string{"q1_domain", "q2_scope"},
		"questions": []any{
			map[string]any{"id": "q1_domain", "question": "Which domain fits best?", "text": "Which domain fits best?"},
			map[string]any{"id": "q2_scope", "question": "How broad should the search be?", "text": "How broad should the search be?"},
		},
		"answers": map[string]any{},
	}
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(sessionID, userID, basePayload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-answer-success",
		SessionID: sessionID,
		UserID:    userID,
		Status:    "completed",
	}))

	mux := http.NewServeMux()
	(&wisdevServer{gateway: gw}).registerQuestioningRoutes(mux, gw)

	session, err := gw.StateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	expectedUpdatedAt := wisdev.IntValue64(session["updatedAt"])

	body := `{"sessionId":"` + sessionID + `","questionId":"q1_domain","values":["medicine"],"displayValues":["Medicine"],"expectedUpdatedAt":` + strconv.FormatInt(expectedUpdatedAt, 10) + `}`
	req := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), contextKey("user_id"), userID))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	updated, err := gw.StateStore.LoadAgentSession(sessionID)
	require.NoError(t, err)
	answers := mapAny(updated["answers"])
	require.Contains(t, answers, "q1_domain")

	singleSelectSessionID := "sess-question-answer-single-select"
	singleSelectPayload := cloneAnyMap(basePayload)
	singleSelectPayload["sessionId"] = singleSelectSessionID
	singleSelectPayload["currentQuestionIndex"] = 2
	singleSelectPayload["questionSequence"] = []string{"q1_domain", "q2_scope", "q3_timeframe"}
	singleSelectPayload["questions"] = []any{
		map[string]any{"id": "q1_domain", "type": "domain", "question": "Which domain fits best?", "text": "Which domain fits best?", "isMultiSelect": true},
		map[string]any{"id": "q2_scope", "type": "scope", "question": "How broad should the search be?", "text": "How broad should the search be?", "isMultiSelect": false},
		map[string]any{"id": "q3_timeframe", "type": "timeframe", "question": "What timeframe should be prioritized?", "text": "What timeframe should be prioritized?", "isMultiSelect": false},
	}
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(singleSelectSessionID, userID, singleSelectPayload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-answer-success-single-select",
		SessionID: singleSelectSessionID,
		UserID:    userID,
		Status:    "completed",
	}))
	singleSelectReq := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+singleSelectSessionID+`","questionId":"q3_timeframe","values":["1year","5years"],"displayValues":["Last Year","Last 5 Years"]}`))
	singleSelectReq = singleSelectReq.WithContext(context.WithValue(singleSelectReq.Context(), contextKey("user_id"), userID))
	singleSelectRec := httptest.NewRecorder()
	mux.ServeHTTP(singleSelectRec, singleSelectReq)
	assert.Equal(t, http.StatusOK, singleSelectRec.Code)
	singleSelectUpdated, err := gw.StateStore.LoadAgentSession(singleSelectSessionID)
	require.NoError(t, err)
	singleSelectAnswers := mapAny(singleSelectUpdated["answers"])
	singleSelectAnswer := mapAny(singleSelectAnswers["q3_timeframe"])
	assert.Equal(t, []string{"1year"}, sliceStrings(singleSelectAnswer["values"]))
	assert.Equal(t, []string{"Last Year"}, sliceStrings(singleSelectAnswer["displayValues"]))

	replay := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(body))
	replay = replay.WithContext(context.WithValue(replay.Context(), contextKey("user_id"), userID))
	replayRec := httptest.NewRecorder()
	mux.ServeHTTP(replayRec, replay)
	assert.Equal(t, http.StatusOK, replayRec.Code)
	assert.Contains(t, replayRec.Body.String(), `"questionId":"q1_domain"`)

	proceedSessionID := "sess-question-answer-proceed"
	proceedPayload := cloneAnyMap(basePayload)
	proceedPayload["sessionId"] = proceedSessionID
	require.NoError(t, gw.StateStore.PersistAgentSessionMutation(proceedSessionID, userID, proceedPayload, wisdev.RuntimeJournalEntry{
		EventID:   "evt-answer-success-proceed",
		SessionID: proceedSessionID,
		UserID:    userID,
		Status:    "completed",
	}))
	proceedSession, err := gw.StateStore.LoadAgentSession(proceedSessionID)
	require.NoError(t, err)
	proceedUpdatedAt := wisdev.IntValue64(proceedSession["updatedAt"])

	proceedReq := httptest.NewRequest(http.MethodPost, "/wisdev/question/answer", bytes.NewBufferString(`{"sessionId":"`+proceedSessionID+`","questionId":"q1_domain","values":["medicine"],"displayValues":["Medicine"],"proceed":true,"expectedUpdatedAt":`+strconv.FormatInt(proceedUpdatedAt, 10)+`}`))
	proceedReq = proceedReq.WithContext(context.WithValue(proceedReq.Context(), contextKey("user_id"), userID))
	proceedRec := httptest.NewRecorder()
	mux.ServeHTTP(proceedRec, proceedReq)
	assert.Equal(t, http.StatusOK, proceedRec.Code)
	finalSession, err := gw.StateStore.LoadAgentSession(proceedSessionID)
	require.NoError(t, err)
	assert.Equal(t, "ready", wisdev.AsOptionalString(finalSession["status"]))
	assert.Equal(t, string(wisdev.QuestionStopReasonUserProceed), wisdev.AsOptionalString(finalSession["questionStopReason"]))
}
