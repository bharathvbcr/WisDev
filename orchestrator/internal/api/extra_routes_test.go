package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

func TestExtraRoutes(t *testing.T) {
	is := assert.New(t)
	tempDir, err := os.MkdirTemp("", "extra_routes_state")
	is.NoError(err)
	defer os.RemoveAll(tempDir)
	is.NoError(os.Setenv("WISDEV_STATE_DIR", tempDir))
	defer os.Unsetenv("WISDEV_STATE_DIR")
	journalPath := tempDir + "/wisdev_journal.jsonl"
	is.NoError(os.Setenv("WISDEV_JOURNAL_PATH", journalPath))
	defer os.Unsetenv("WISDEV_JOURNAL_PATH")

	mux := http.NewServeMux()
	server := &wisdevServer{}
	gw := &wisdev.AgentGateway{
		StateStore: wisdev.NewRuntimeStateStore(nil, nil),
		Journal:    wisdev.NewRuntimeJournal(nil),
		ADKRuntime: &wisdev.ADKRuntime{
			Config:    wisdev.DefaultADKRuntimeConfig(),
			InitError: "missing GOOGLE_API_KEY",
		},
	}
	server.registerExtraRoutes(mux, gw)
	withUser := func(req *http.Request, userID string) *http.Request {
		ctx := context.WithValue(req.Context(), contextKey("user_id"), userID)
		return req.WithContext(ctx)
	}

	t.Run("/outcomes/recent", func(t *testing.T) {
		gw.Journal.Append(wisdev.RuntimeJournalEntry{
			EventID:   "evt-success",
			TraceID:   "trace-success",
			UserID:    "u1",
			SessionID: "s-outcomes",
			EventType: "execute",
			Path:      "/wisdev/execute",
			Status:    "ok",
			Metadata:  map[string]any{"action": "tool.search"},
			Payload:   map[string]any{"applied": true},
		})
		gw.Journal.Append(wisdev.RuntimeJournalEntry{
			EventID:   "evt-failure",
			TraceID:   "trace-failure",
			UserID:    "u1",
			SessionID: "s-outcomes",
			EventType: "execute",
			Path:      "/wisdev/execute",
			Status:    "error",
			Metadata:  map[string]any{"action": "tool.fail"},
			Payload:   map[string]any{"applied": "false"},
		})

		w := httptest.NewRecorder()
		req := withUser(httptest.NewRequest("GET", "/outcomes/recent?userId=u1&maxResults=2", nil), "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		outcomes, ok := envelope["outcomes"].(map[string]any)
		is.True(ok)
		is.Equal(float64(2), outcomes["totalOutcomes"])
		is.Equal(0.5, outcomes["avgReward"])
		is.Contains(outcomes["successfulTools"], "tool.search")
		is.Contains(outcomes["failedTools"], "tool.fail")

		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("GET", "/outcomes/recent", nil)
		mux.ServeHTTP(w2, req2)
		is.Equal(http.StatusForbidden, w2.Code)
	})

	t.Run("/outcomes/recent invalid maxResults falls back", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withUser(httptest.NewRequest("GET", "/outcomes/recent?userId=u1&maxResults=not-a-number", nil), "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("/feedback/save", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"userId":    "u1",
			"sessionId": "s1",
			"feedback":  "good",
		})
		w := httptest.NewRecorder()
		req := withUser(httptest.NewRequest("POST", "/feedback/save", bytes.NewReader(body)), "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("POST", "/feedback/save", bytes.NewReader([]byte("bad")))
		mux.ServeHTTP(w2, req2)
		is.Equal(http.StatusBadRequest, w2.Code)
	})

	t.Run("/feedback/analytics unauthorized", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/feedback/analytics", bytes.NewReader([]byte(`{"userId":"u2"}`)))
		req = withUser(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusForbidden, w.Code)
	})

	t.Run("/feedback/analytics success", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/feedback/analytics", bytes.NewReader([]byte(`{"userId":"u1"}`)))
		req = withUser(req, "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)
	})

	t.Run("/memory/profile/get", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withUser(httptest.NewRequest("GET", "/memory/profile/get?userId=u1", nil), "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		profileEnvelope, ok := envelope["profile"].(map[string]any)
		is.True(ok)
		is.Equal(false, profileEnvelope["found"])
		_, ok = profileEnvelope["profile"]
		is.False(ok)
	})

	t.Run("/memory/profile/learn", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"userId": "u1",
			"conversation": map[string]any{
				"sessionId":      "s1",
				"detectedDomain": "biology",
				"expertiseLevel": "expert",
				"status":         "complete",
				"totalTimeMs":    180000,
				"refinementsTaken": []string{
					"gene editing",
				},
				"answers": map[string]any{
					"q2_scope": map[string]any{
						"values": []string{"comprehensive"},
					},
					"q5_study_types": map[string]any{
						"values": []string{"rct", "meta_analysis"},
					},
					"q6_exclusions": map[string]any{
						"values": []string{"preprints"},
					},
					"q7_evidence_quality": map[string]any{
						"values": []string{"peer_reviewed", "methods_transparency"},
					},
					"q8_output_focus": map[string]any{
						"values": []string{"method_comparison"},
					},
				},
			},
		})
		w := httptest.NewRecorder()
		req := withUser(httptest.NewRequest("POST", "/memory/profile/learn", bytes.NewReader(body)), "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		traceID, _ := envelope["traceId"].(string)
		is.NotEmpty(traceID)
		result, ok := envelope["result"].(map[string]any)
		is.True(ok)
		is.Equal(true, result["updated"])
		profile, ok := result["profile"].(map[string]any)
		is.True(ok)
		is.Equal("u1", profile["userId"])
		is.Equal("expert", profile["expertiseLevel"])
		is.Equal("comprehensive", profile["typicalScope"])
		is.Contains(profile["preferredDomains"], "biology")
		is.Contains(profile["preferredStudyTypes"], "rct")
		is.Contains(profile["commonExclusions"], "preprints")
		is.Contains(profile["preferredEvidenceQuality"], "peer_reviewed")
		is.Contains(profile["preferredOutputFocus"], "method_comparison")

		entries := gw.Journal.ReadSession("s1", 10)
		require.NotEmpty(t, entries)
		foundProfileLearn := false
		for _, entry := range entries {
			if entry.EventType != wisdev.EventProfileLearn {
				continue
			}
			foundProfileLearn = true
			is.Equal(traceID, entry.TraceID)
			is.Equal("u1", entry.UserID)
			break
		}
		is.True(foundProfileLearn)

		w2 := httptest.NewRecorder()
		req2 := withUser(httptest.NewRequest("GET", "/memory/profile/get?userId=u1", nil), "u1")
		mux.ServeHTTP(w2, req2)
		is.Equal(http.StatusOK, w2.Code)

		var getEnvelope map[string]any
		is.NoError(json.NewDecoder(w2.Body).Decode(&getEnvelope))
		getProfileEnvelope, ok := getEnvelope["profile"].(map[string]any)
		is.True(ok)
		is.Equal(true, getProfileEnvelope["found"])
		getProfile, ok := getProfileEnvelope["profile"].(map[string]any)
		is.True(ok)
		is.Equal("u1", getProfile["userId"])
	})

	t.Run("/feedback/analytics invalid body", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/feedback/analytics", bytes.NewBufferString(`{`)), "u1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusBadRequest, w.Code)
	})

	t.Run("user scoped memory routes deny mismatched owner", func(t *testing.T) {
		cases := []struct {
			name   string
			method string
			path   string
			body   string
		}{
			{name: "profile get", method: http.MethodGet, path: "/memory/profile/get?userId=u2"},
			{name: "profile learn", method: http.MethodPost, path: "/memory/profile/learn", body: `{"userId":"u2","conversation":{"sessionId":"s1"}}`},
			{name: "session summaries upsert", method: http.MethodPost, path: "/memory/session-summaries/upsert", body: `{"userId":"u2","summaries":[]}`},
			{name: "session summaries get", method: http.MethodPost, path: "/memory/session-summaries/get", body: `{"userId":"u2"}`},
			{name: "project workspace upsert", method: http.MethodPost, path: "/memory/project-workspace/upsert", body: `{"userId":"u2","projectId":"p1","workspace":{}}`},
			{name: "project workspace get", method: http.MethodPost, path: "/memory/project-workspace/get", body: `{"userId":"u2","projectId":"p1"}`},
			{name: "research memory query", method: http.MethodPost, path: "/research/memory/query", body: `{"userId":"u2","query":"crispr"}`},
			{name: "research memory backfill", method: http.MethodPost, path: "/research/memory/backfill", body: `{"userId":"u2"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(tc.body)))
				req = withUser(req, "u1")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				is.Equal(http.StatusForbidden, rec.Code)
			})
		}
	})

	t.Run("simple success routes", func(t *testing.T) {
		routes := []string{
			"/telemetry/delete-session",
			"/evaluate/replay",
			"/evaluate/shadow",
			"/evaluate/canary",
			"/evaluate/rubric",
			"/verifier/blind-contract",
		}
		for _, path := range routes {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path, nil)
			mux.ServeHTTP(w, req)
			is.Equal(http.StatusOK, w.Code)
		}
	})

	t.Run("/policy/gates/get payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/policy/gates/get", nil)
		req = withUser(req, "internal-service")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		gates, ok := envelope["gates"].([]any)
		is.True(ok)
		is.Len(gates, 0)
	})

	t.Run("/policy/canary-config/get payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/policy/canary-config/get", nil)
		req = withUser(req, "internal-service")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		config, ok := envelope["config"].(map[string]any)
		is.True(ok)
		is.NotNil(config)
	})

	t.Run("/policy/gates/get denies non-internal caller", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/policy/gates/get", nil)
		req = withUser(req, "u1")
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusForbidden, w.Code)

		var resp APIError
		is.NoError(json.NewDecoder(w.Body).Decode(&resp))
		is.Equal(ErrUnauthorized, resp.Error.Code)
	})

	t.Run("/policy/canary-config/get denies anonymous caller", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/policy/canary-config/get", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusForbidden, w.Code)

		var resp APIError
		is.NoError(json.NewDecoder(w.Body).Decode(&resp))
		is.Equal(ErrUnauthorized, resp.Error.Code)
	})

	t.Run("/runtime/retention/run", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/runtime/retention/run", bytes.NewReader([]byte(`{"retentionDays":5}`)))
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		result, ok := envelope["result"].(map[string]any)
		is.True(ok)
		is.Equal(float64(5), result["retentionDays"])
	})

	t.Run("/runtime/retention/run invalid body and unavailable", func(t *testing.T) {
		badReq := httptest.NewRequest(http.MethodPost, "/runtime/retention/run", bytes.NewBufferString(`{`))
		badRec := httptest.NewRecorder()
		mux.ServeHTTP(badRec, badReq)
		is.Equal(http.StatusBadRequest, badRec.Code)

		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		unavailableReq := httptest.NewRequest(http.MethodPost, "/runtime/retention/run", bytes.NewReader([]byte(`{"retentionDays":7}`)))
		unavailableRec := httptest.NewRecorder()
		nilMux.ServeHTTP(unavailableRec, unavailableReq)
		is.Equal(http.StatusServiceUnavailable, unavailableRec.Code)
	})

	t.Run("/evaluate/rubric payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/evaluate/rubric", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		result, ok := envelope["result"].(map[string]any)
		is.True(ok)
		rubrics, ok := result["rubrics"].([]any)
		is.True(ok)
		is.GreaterOrEqual(len(rubrics), 3)
	})

	t.Run("/evaluate/replay payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/evaluate/replay", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		result, ok := envelope["result"].(map[string]any)
		is.True(ok)
		_, ok = result["evalReport"].(map[string]any)
		is.True(ok)
	})

	t.Run("/evaluate/replay/report payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/evaluate/replay/report", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		result, ok := envelope["result"].(map[string]any)
		is.True(ok)
		evalReport, ok := result["evalReport"].(map[string]any)
		is.True(ok)
		is.NotNil(evalReport["scenarioCount"])
	})

	t.Run("/verifier/blind-contract payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/verifier/blind-contract", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		contract, ok := envelope["contract"].(map[string]any)
		is.True(ok)
		is.Equal("lineage_only", contract["mode"])
		is.Equal(true, contract["independent"])
		is.Equal(false, contract["usesWriterContent"])
	})

	t.Run("/agent/registration-contract payload", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/agent/registration-contract", nil)
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		contract, ok := envelope["contract"].(map[string]any)
		is.True(ok)
		discoveryEndpoints, ok := contract["discoveryEndpoints"].([]any)
		is.True(ok)
		is.NotEmpty(discoveryEndpoints)
		registration, ok := contract["registration"].(map[string]any)
		is.True(ok)
		is.Equal("blocked_by_runtime_init", registration["externalConsumerFit"])
	})

	t.Run("/quest/review payload", func(t *testing.T) {
		store := wisdev.NewRuntimeStateStore(nil, nil)
		questGateway := &wisdev.AgentGateway{StateStore: store}
		runtime := wisdev.NewResearchQuestRuntime(questGateway).ApplyHooks(wisdev.ResearchQuestHooks{
			RetrieveFn: func(context.Context, *wisdev.ResearchQuest) ([]wisdev.Source, map[string]any, error) {
				return []wisdev.Source{{ID: "paper-1", Title: "Paper 1", DOI: "10.1000/example"}}, map[string]any{"queryUsed": "test"}, nil
			},
			CitationFn: func(context.Context, *wisdev.ResearchQuest, []wisdev.Source) ([]wisdev.CitationAuthorityRecord, wisdev.CitationVerdict, map[string]any, error) {
				verdict := wisdev.CitationVerdict{
					Status:         "blocked",
					Promoted:       false,
					BlockingIssues: []string{"needs manual review"},
					PromotionGate: map[string]any{
						"promoted":       false,
						"blockingIssues": []any{"needs manual review"},
					},
				}
				return nil, verdict, map[string]any{"promotionGate": verdict.PromotionGate}, nil
			},
		})
		questGateway.QuestRuntime = runtime
		server := &wisdevServer{}
		mux := http.NewServeMux()
		server.registerExtraRoutes(mux, questGateway)

		quest, err := runtime.StartQuest(context.Background(), wisdev.ResearchQuestRequest{
			UserID: "u1",
			Query:  "test quest",
		})
		is.NoError(err)
		is.NotNil(quest)

		body, _ := json.Marshal(map[string]any{"questId": quest.QuestID})
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/quest/review", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		mux.ServeHTTP(w, req)
		is.Equal(http.StatusOK, w.Code)

		var envelope map[string]any
		is.NoError(json.NewDecoder(w.Body).Decode(&envelope))
		review, ok := envelope["review"].(map[string]any)
		is.True(ok)
		questPayload, ok := review["quest"].(map[string]any)
		is.True(ok)
		is.Equal("test quest", questPayload["query"])
		promotionGate, ok := review["promotionGate"].(map[string]any)
		is.True(ok)
		is.Equal(false, promotionGate["promoted"])
	})
}

func TestWisDevSearchRoutesFilterWebSearchResults(t *testing.T) {
	server := &wisdevServer{}
	mux := http.NewServeMux()
	server.registerSearchRoutes(mux, nil)

	t.Run("method must be post", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wisdev/filter-web-search-results", nil)
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("invalid body returns 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/wisdev/filter-web-search-results", bytes.NewReader([]byte(`invalid`)))
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("filters with derived policy and returns results", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"query": "machine learning",
			"results": []map[string]any{
				{
					"title":   "Academic paper",
					"link":    "https://arxiv.org/paper",
					"snippet": "A paper on learning",
				},
				{
					"title":   "Blocked social feed",
					"link":    "https://pinterest.com/example",
					"snippet": "random post",
				},
			},
			"policy": map[string]any{},
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/wisdev/filter-web-search-results", bytes.NewReader(body))
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		assert.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.True(t, resp["ok"].(bool))
		result, ok := resp["result"].(map[string]any)
		assert.True(t, ok)
		results, ok := result["results"].([]any)
		assert.True(t, ok)
		assert.Len(t, results, 1)
		telemetry, ok := result["telemetry"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, float64(1), telemetry["keptCount"])
		assert.Equal(t, float64(1), telemetry["filteredCount"])
		reasons, ok := telemetry["filterReasons"].(map[string]any)
		assert.True(t, ok)
		assert.Equal(t, float64(1), reasons["blocked_domain"])
	})
}

func TestExtraRoutesBenchmarkHelpers(t *testing.T) {
	t.Run("load helpers succeed on repo benchmarks", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		tmpDir := t.TempDir()
		benchDir := filepath.Join(tmpDir, "tests", "benchmarks")
		require.NoError(t, os.MkdirAll(benchDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(benchDir, "wisdev_benchmark_report.json"), []byte(`{"title":"report","score":0.9}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(benchDir, "wisdev_benchmark_transcript.json"), []byte(`{"baseline":[{"sampleId":"b1"}],"candidate":[{"id":"c1"}]}`), 0o644))
		require.NoError(t, os.Chdir(tmpDir))
		t.Cleanup(func() { _ = os.Chdir(cwd) })

		report, ok := loadRubricBenchmarkReport()
		require.True(t, ok)
		require.NotEmpty(t, report)

		transcript, ok := loadReplayBenchmarkTranscript()
		require.True(t, ok)
		require.NotEmpty(t, transcript)

		summary := summarizeReplayTranscript(map[string]any{
			"baseline": []any{
				map[string]any{"sampleId": "b1"},
				map[string]any{"id": "b2"},
				map[string]any{"sampleId": "b3"},
				map[string]any{"sampleId": "b4"},
			},
			"candidate": []any{
				map[string]any{"id": "c1"},
			},
		})
		baseline := summary["baseline"].(map[string]any)
		assert.Equal(t, 4, baseline["count"])
		assert.Equal(t, []any{"b1", "b2", "b3"}, baseline["sampleIds"])
		candidate := summary["candidate"].(map[string]any)
		assert.Equal(t, 1, candidate["count"])
		assert.Equal(t, []any{"c1"}, candidate["sampleIds"])
	})

	t.Run("load helpers fail outside repo", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		tmpDir := t.TempDir()
		require.NoError(t, os.Chdir(tmpDir))
		t.Cleanup(func() { _ = os.Chdir(cwd) })

		report, ok := loadRubricBenchmarkReport()
		assert.False(t, ok)
		assert.Nil(t, report)

		transcript, ok := loadReplayBenchmarkTranscript()
		assert.False(t, ok)
		assert.Nil(t, transcript)
	})
}

func TestExtraRoutesAdditionalBranches(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "extra_routes_more")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	require.NoError(t, os.Setenv("WISDEV_STATE_DIR", tempDir))
	defer os.Unsetenv("WISDEV_STATE_DIR")

	mux := http.NewServeMux()
	server := &wisdevServer{}
	gw := &wisdev.AgentGateway{
		StateStore: wisdev.NewRuntimeStateStore(nil, nil),
		Journal:    wisdev.NewRuntimeJournal(nil),
	}
	gw.ResearchMemory = wisdev.NewResearchMemoryCompiler(gw.StateStore, gw.Journal)
	server.registerExtraRoutes(mux, gw)

	t.Run("/memory/session-summaries/upsert success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId": "u1",
			"summaries": []map[string]any{
				{"sessionId": "s1", "summary": "ok"},
			},
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/memory/session-summaries/upsert", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("/memory/session-summaries/upsert unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		req := httptest.NewRequest(http.MethodPost, "/memory/session-summaries/upsert", bytes.NewReader([]byte(`{"userId":"u1","summaries":[]}`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("/memory/session-summaries/get success", func(t *testing.T) {
		require.NoError(t, gw.StateStore.SaveSessionSummaries("u1", []map[string]any{
			{"sessionId": "s1", "summary": "stored"},
		}))
		req := httptest.NewRequest(http.MethodPost, "/memory/session-summaries/get", bytes.NewReader([]byte(`{"userId":"u1"}`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("/memory/session-summaries/get unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		req := httptest.NewRequest(http.MethodPost, "/memory/session-summaries/get", bytes.NewReader([]byte(`{"userId":"u1"}`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("/memory/profile/get query fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/memory/profile/get?userId=u1", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"profile"`)
	})

	t.Run("/memory/profile/learn unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		body, err := json.Marshal(map[string]any{
			"userId": "u1",
			"conversation": map[string]any{
				"sessionId": "s1",
			},
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/memory/profile/learn", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"updated":false`)
	})

	t.Run("/memory/profile/learn success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId": "u1",
			"conversation": map[string]any{
				"sessionId": "s1",
				"messages":  []any{map[string]any{"role": "user", "content": "research question"}},
			},
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/memory/profile/learn", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"updated":true`)
	})

	t.Run("/memory/profile/learn invalid body", func(t *testing.T) {
		req := withUser(httptest.NewRequest(http.MethodPost, "/memory/profile/learn", bytes.NewReader([]byte("bad"))), "u1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("/memory/project-workspace/get not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/memory/project-workspace/get", bytes.NewReader([]byte(`{"userId":"u1","projectId":"missing"}`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("/memory/project-workspace/upsert success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId":    "u1",
			"projectId": "p2",
			"workspace": map[string]any{"section": "draft"},
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/memory/project-workspace/upsert", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("/memory/project-workspace/upsert invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/memory/project-workspace/upsert", bytes.NewReader([]byte(`bad`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("/memory/project-workspace/upsert unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		body, err := json.Marshal(map[string]any{
			"userId":    "u1",
			"projectId": "p1",
			"workspace": map[string]any{},
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/memory/project-workspace/upsert", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("/memory/project-workspace/get unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		req := withUser(httptest.NewRequest(http.MethodPost, "/memory/project-workspace/get", bytes.NewBufferString(`{"userId":"u1","projectId":"p1"}`)), "u1")
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("/research/memory/query empty response", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId": "u1",
			"query":  "crispr",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/research/memory/query", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var envelope map[string]any
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
		result, ok := envelope["researchMemory"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, result, "findings")
		assert.NotNil(t, result["findings"])
		assert.Len(t, result["findings"].([]any), 0)
	})

	t.Run("/research/memory/query unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		req := withUser(httptest.NewRequest(http.MethodPost, "/research/memory/query", bytes.NewBufferString(`{"userId":"u1","query":"crispr"}`)), "u1")
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"researchMemory"`)
	})

	t.Run("/research/memory/query invalid key", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId": "u1/../bad",
			"query":  "crispr",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/research/memory/query", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1/../bad"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("/research/memory/backfill empty response", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId":    "u1",
			"projectId": "p1",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/research/memory/backfill", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var envelope map[string]any
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&envelope))
		result, ok := envelope["result"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "u1", result["userId"])
		assert.Equal(t, "p1", result["projectId"])
	})

	t.Run("/research/memory/backfill invalid key", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId": "u1/../bad",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/research/memory/backfill", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1/../bad"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("/research/memory/backfill unavailable", func(t *testing.T) {
		nilMux := http.NewServeMux()
		server.registerExtraRoutes(nilMux, &wisdev.AgentGateway{})
		req := withUser(httptest.NewRequest(http.MethodPost, "/research/memory/backfill", bytes.NewBufferString(`{"userId":"u1","projectId":"p1"}`)), "u1")
		rec := httptest.NewRecorder()
		nilMux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"userId":"u1"`)
	})

	t.Run("/runtime/retention/run invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/runtime/retention/run", bytes.NewReader([]byte(`bad`)))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("/feedback/save invalid body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/feedback/save", bytes.NewReader([]byte(`bad`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("/feedback/get success", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"userId":    "u1",
			"sessionId": "s1",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/feedback/get", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("benchmark loader helpers success", func(t *testing.T) {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		tmpDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "tests", "benchmarks"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "tests", "benchmarks", "wisdev_benchmark_report.json"), []byte(`{"ok":true}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "tests", "benchmarks", "wisdev_benchmark_transcript.json"), []byte(`{"baseline":[{"sampleId":"b1"}],"candidate":[{"id":"c1"}]}`), 0o644))
		require.NoError(t, os.Chdir(tmpDir))
		t.Cleanup(func() { _ = os.Chdir(cwd) })

		report, ok := loadRubricBenchmarkReport()
		assert.True(t, ok)
		assert.Equal(t, true, report["ok"])

		transcript, ok := loadReplayBenchmarkTranscript()
		assert.True(t, ok)
		summary := summarizeReplayTranscript(transcript)
		require.Contains(t, summary, "baseline")
		require.Contains(t, summary, "candidate")
	})
}
