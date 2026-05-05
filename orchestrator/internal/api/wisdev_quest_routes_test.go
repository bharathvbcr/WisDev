package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWisDevQuestRoutes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "wisdev_quest_routes")
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
	gw.QuestRuntime = wisdev.NewResearchQuestRuntime(gw).ApplyHooks(wisdev.ResearchQuestHooks{
		RetrieveFn: func(_ context.Context, quest *wisdev.ResearchQuest) ([]wisdev.Source, map[string]any, error) {
			papers := []wisdev.Source{
				{
					ID:      "paper-1",
					Title:   "Quest Evidence Paper",
					Summary: "The paper provides direct evidence for the test quest.",
					DOI:     "10.1000/test-doi",
					Source:  "crossref",
					Score:   0.91,
					Year:    2025,
				},
			}
			return papers, map[string]any{
				"papers":   []any{map[string]any{"id": "paper-1", "title": "Quest Evidence Paper"}},
				"count":    len(papers),
				"traceId":  "trace-test",
				"pipeline": []string{"parallel_search", "citation_gate"},
				"query":    quest.Query,
			}, nil
		},
		CitationFn: func(_ context.Context, _ *wisdev.ResearchQuest, papers []wisdev.Source) ([]wisdev.CitationAuthorityRecord, wisdev.CitationVerdict, map[string]any, error) {
			authorities := make([]wisdev.CitationAuthorityRecord, 0, len(papers))
			for _, paper := range papers {
				authorities = append(authorities, wisdev.CitationAuthorityRecord{
					Authority:        "crossref",
					CanonicalID:      paper.DOI,
					DOI:              paper.DOI,
					Title:            paper.Title,
					Resolved:         true,
					Verified:         true,
					AgreementCount:   2,
					ResolutionEngine: "research_quest_branch",
				})
			}
			return authorities, wisdev.CitationVerdict{
					Status:           "promoted",
					Promoted:         true,
					VerifiedCount:    len(papers),
					AgreementSources: []string{"crossref", "semantic_scholar"},
				}, map[string]any{
					"promotionGate": map[string]any{
						"promoted":         true,
						"agreementSources": []string{"crossref", "semantic_scholar"},
						"blockingIssues":   []string{},
					},
				}, nil
		},
	})

	mux := http.NewServeMux()
	RegisterWisDevRoutes(mux, gw, nil, nil)

	createReq := httptest.NewRequest(http.MethodPost, "/wisdev/quests", bytes.NewBufferString(`{"query":"test research quest","qualityMode":"quality","persistUserPreferences":true}`))
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), ctxUserID, "u1"))
	createRec := httptest.NewRecorder()

	mux.ServeHTTP(createRec, createReq)

	require.Equal(t, http.StatusOK, createRec.Code)

	var createBody map[string]any
	require.NoError(t, json.Unmarshal(createRec.Body.Bytes(), &createBody))
	questPayload, ok := createBody["quest"].(map[string]any)
	require.True(t, ok)
	questID := wisdev.AsOptionalString(questPayload["questId"])
	require.NotEmpty(t, questID)
	assert.Equal(t, "complete", wisdev.AsOptionalString(questPayload["status"]))
	assert.Equal(t, true, questPayload["persistUserPreferences"])
	memoryPayload, ok := questPayload["memory"].(map[string]any)
	require.True(t, ok)
	promotionRules, ok := memoryPayload["promotionRules"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, promotionRules["userPreferenceOptIn"])
	userPersonalized, ok := memoryPayload["userPersonalized"].([]any)
	require.True(t, ok)
	assert.Len(t, userPersonalized, 1)

	eventsReq := httptest.NewRequest(http.MethodGet, "/wisdev/quests/"+questID+"/events", nil)
	eventsReq = eventsReq.WithContext(context.WithValue(eventsReq.Context(), ctxUserID, "u1"))
	eventsRec := httptest.NewRecorder()

	mux.ServeHTTP(eventsRec, eventsReq)

	require.Equal(t, http.StatusOK, eventsRec.Code)
	var eventsBody map[string]any
	require.NoError(t, json.Unmarshal(eventsRec.Body.Bytes(), &eventsBody))
	events, ok := eventsBody["events"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, events)

	artifactsReq := httptest.NewRequest(http.MethodGet, "/wisdev/quests/"+questID+"/artifacts", nil)
	artifactsReq = artifactsReq.WithContext(context.WithValue(artifactsReq.Context(), ctxUserID, "u1"))
	artifactsRec := httptest.NewRecorder()

	mux.ServeHTTP(artifactsRec, artifactsReq)

	require.Equal(t, http.StatusOK, artifactsRec.Code)
	var artifactsBody map[string]any
	require.NoError(t, json.Unmarshal(artifactsRec.Body.Bytes(), &artifactsBody))
	artifacts, ok := artifactsBody["artifacts"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, artifacts, "retrieval")
	assert.Contains(t, artifacts, "citationVerdict")
	assert.Contains(t, artifacts, "routing")

	resumeReq := httptest.NewRequest(http.MethodPost, "/wisdev/quests/"+questID+"/resume", bytes.NewBufferString(`{"forceResume":true}`))
	resumeReq = resumeReq.WithContext(context.WithValue(resumeReq.Context(), ctxUserID, "u1"))
	resumeRec := httptest.NewRecorder()

	mux.ServeHTTP(resumeRec, resumeReq)

	require.Equal(t, http.StatusOK, resumeRec.Code)
	var resumeBody map[string]any
	require.NoError(t, json.Unmarshal(resumeRec.Body.Bytes(), &resumeBody))
	resumedQuest, ok := resumeBody["quest"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, questID, wisdev.AsOptionalString(resumedQuest["questId"]))

	resumeMethodReq := httptest.NewRequest(http.MethodGet, "/wisdev/quests/"+questID+"/resume", nil)
	resumeMethodReq = resumeMethodReq.WithContext(context.WithValue(resumeMethodReq.Context(), ctxUserID, "u1"))
	resumeMethodRec := httptest.NewRecorder()

	mux.ServeHTTP(resumeMethodRec, resumeMethodReq)

	assert.Equal(t, http.StatusMethodNotAllowed, resumeMethodRec.Code)

	forbiddenReq := httptest.NewRequest(http.MethodGet, "/wisdev/quests/"+questID+"/artifacts", nil)
	forbiddenReq = forbiddenReq.WithContext(context.WithValue(forbiddenReq.Context(), ctxUserID, "u2"))
	forbiddenRec := httptest.NewRecorder()

	mux.ServeHTTP(forbiddenRec, forbiddenReq)

	assert.Equal(t, http.StatusForbidden, forbiddenRec.Code)
}

func TestWisDevQuestRouteHelpers(t *testing.T) {
	t.Run("parseQuestRoute", func(t *testing.T) {
		questID, action, ok := parseQuestRoute("/wisdev/quests/q1/events")
		assert.True(t, ok)
		assert.Equal(t, "q1", questID)
		assert.Equal(t, "events", action)

		_, _, ok = parseQuestRoute("/wisdev/quests")
		assert.False(t, ok)

		_, _, ok = parseQuestRoute("/wisdev/quests/q1")
		assert.False(t, ok)
	})

	t.Run("resolveQuestRuntime", func(t *testing.T) {
		assert.Nil(t, resolveQuestRuntime(nil))

		gateway := &wisdev.AgentGateway{}
		runtime := resolveQuestRuntime(gateway)
		assert.NotNil(t, runtime)
		assert.Same(t, runtime, gateway.QuestRuntime)
		assert.Same(t, runtime, resolveQuestRuntime(gateway))
	})
}

func TestWisDevQuestRoutes_NegativeBranches(t *testing.T) {
	t.Run("missing runtime returns unavailable", func(t *testing.T) {
		mux := http.NewServeMux()
		server := &wisdevServer{}
		server.registerQuestRoutes(mux, nil)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/wisdev/quests", bytes.NewBufferString(`{"query":"test"}`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("bad quest route returns not found", func(t *testing.T) {
		mux := http.NewServeMux()
		server := &wisdevServer{}
		gateway := &wisdev.AgentGateway{}
		server.registerQuestRoutes(mux, gateway)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/wisdev/quests/invalid", nil)
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestWisDevQuestRoutes_MoreBranches(t *testing.T) {
	t.Run("invalid body on create quest", func(t *testing.T) {
		mux := http.NewServeMux()
		server := &wisdevServer{}
		gateway := &wisdev.AgentGateway{}
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
		server.registerQuestRoutes(mux, gateway)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/quests", bytes.NewBufferString(`not-json`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("load failure returns internal error", func(t *testing.T) {
		mux := http.NewServeMux()
		server := &wisdevServer{}
		gateway := &wisdev.AgentGateway{}
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
		server.registerQuestRoutes(mux, gateway)

		req := httptest.NewRequest(http.MethodGet, "/wisdev/quests/q-missing/events", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("resume invalid body", func(t *testing.T) {
		store := wisdev.NewRuntimeStateStore(nil, nil)
		gateway := &wisdev.AgentGateway{StateStore: store}
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
		quest, err := gateway.QuestRuntime.StartQuest(context.Background(), wisdev.ResearchQuestRequest{
			UserID: "u1",
			Query:  "test quest",
		})
		require.NoError(t, err)
		require.NotNil(t, quest)

		mux := http.NewServeMux()
		server := &wisdevServer{}
		server.registerQuestRoutes(mux, gateway)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/quests/"+quest.QuestID+"/resume", bytes.NewBufferString(`bad-json`))
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("events method not allowed after successful load", func(t *testing.T) {
		store := wisdev.NewRuntimeStateStore(nil, nil)
		require.NoError(t, store.SaveQuestState("q-ok", map[string]any{
			"questId":      "q-ok",
			"sessionId":    "s-ok",
			"userId":       "u1",
			"query":        "test quest",
			"status":       "running",
			"currentStage": "init",
			"createdAt":    int64(1),
			"updatedAt":    int64(1),
		}))

		mux := http.NewServeMux()
		server := &wisdevServer{}
		gateway := &wisdev.AgentGateway{StateStore: store}
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
		quest, err := gateway.QuestRuntime.StartQuest(context.Background(), wisdev.ResearchQuestRequest{
			UserID: "u1",
			Query:  "test quest",
		})
		require.NoError(t, err)
		require.NotNil(t, quest)
		server.registerQuestRoutes(mux, gateway)

		req := httptest.NewRequest(http.MethodPost, "/wisdev/quests/"+quest.QuestID+"/events", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("quest route method guards", func(t *testing.T) {
		store := wisdev.NewRuntimeStateStore(nil, nil)
		gateway := &wisdev.AgentGateway{StateStore: store}
		gateway.QuestRuntime = wisdev.NewResearchQuestRuntime(gateway)
		quest, err := gateway.QuestRuntime.StartQuest(context.Background(), wisdev.ResearchQuestRequest{
			UserID: "u1",
			Query:  "method guard quest",
		})
		require.NoError(t, err)
		require.NotNil(t, quest)

		mux := http.NewServeMux()
		server := &wisdevServer{}
		server.registerQuestRoutes(mux, gateway)

		cases := []struct {
			name   string
			path   string
			method string
			want   int
		}{
			{name: "create quest get", path: "/wisdev/quests", method: http.MethodGet, want: http.StatusMethodNotAllowed},
			{name: "events post", path: "/wisdev/quests/" + quest.QuestID + "/events", method: http.MethodPost, want: http.StatusMethodNotAllowed},
			{name: "resume get", path: "/wisdev/quests/" + quest.QuestID + "/resume", method: http.MethodGet, want: http.StatusMethodNotAllowed},
			{name: "artifacts post", path: "/wisdev/quests/" + quest.QuestID + "/artifacts", method: http.MethodPost, want: http.StatusMethodNotAllowed},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				req = req.WithContext(context.WithValue(req.Context(), ctxUserID, "u1"))
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				assert.Equal(t, tc.want, rec.Code)
			})
		}
	})
}
