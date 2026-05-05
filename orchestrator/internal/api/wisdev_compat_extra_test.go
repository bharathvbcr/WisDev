package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLegacyCompatHandler(t *testing.T, withSession bool) (*WisDevHandler, *wisdev.SessionManager, string) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "wisdev_compat_extra")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	sessions := wisdev.NewSessionManager(tempDir)
	if withSession {
		session, err := sessions.CreateSession(context.Background(), "u1", "machine learning")
		require.NoError(t, err)
		return NewWisDevHandler(sessions, wisdev.NewGuidedFlow(), nil, nil, nil, nil, nil), sessions, session.ID
	}
	return NewWisDevHandler(sessions, wisdev.NewGuidedFlow(), nil, nil, nil, nil, nil), sessions, ""
}

func sessionManagerBaseDir(t *testing.T, sessions *wisdev.SessionManager) string {
	t.Helper()
	v := reflect.ValueOf(sessions).Elem().FieldByName("baseDir")
	require.True(t, v.IsValid())
	return v.String()
}

func TestWisDevHandler_LegacyCompatNegativePaths(t *testing.T) {
	t.Run("CompleteSession", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/complete", nil)
			w := httptest.NewRecorder()
			h.HandleCompleteSession(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/complete?sessionId=s1", nil)
			w := httptest.NewRecorder()
			h.HandleCompleteSession(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("missing session id", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/complete", nil)
			w := httptest.NewRecorder()
			h.HandleCompleteSession(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/complete?sessionId=missing", nil)
			w := httptest.NewRecorder()
			h.HandleCompleteSession(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})

	t.Run("QuestionOptions", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/options", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionOptions(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/options?sessionId=s1&questionId=q1", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionOptions(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("missing ids", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/options", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionOptions(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/options?sessionId=missing&questionId=q4_subtopics", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionOptions(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("question not found", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			_ = sessions
			req := httptest.NewRequest(http.MethodGet, "/wisdev/options?sessionId="+sessionID+"&questionId=missing", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionOptions(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})

	t.Run("Recommendations", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/recommendations", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionRecommendations(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId=s1&questionId=q1_domain", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionRecommendations(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("missing ids", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionRecommendations(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId=missing&questionId=q1_domain", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionRecommendations(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("question not found", func(t *testing.T) {
			h, _, sessionID := newLegacyCompatHandler(t, true)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId="+sessionID+"&questionId=missing", nil)
			w := httptest.NewRecorder()
			h.HandleQuestionRecommendations(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("legacy question branches", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			session, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			session.Query = "machine learning graph neural networks"
			session.OriginalQuery = session.Query
			session.CorrectedQuery = session.Query
			session.DetectedDomain = "medicine"
			require.NoError(t, sessions.SaveSession(context.Background(), session))

			t.Run("q4 subtopics from query keywords", func(t *testing.T) {
				session.Answers = map[string]wisdev.Answer{}
				require.NoError(t, sessions.SaveSession(context.Background(), session))
				req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId="+sessionID+"&questionId=q4_subtopics", nil)
				w := httptest.NewRecorder()
				h.HandleQuestionRecommendations(w, req)
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Body.String(), `"questionId":"q4_subtopics"`)
				assert.Contains(t, w.Body.String(), `"values"`)
			})

			t.Run("q5 study types medicine branch", func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId="+sessionID+"&questionId=q5_study_types", nil)
				w := httptest.NewRecorder()
				h.HandleQuestionRecommendations(w, req)
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Body.String(), `"questionId":"q5_study_types"`)
				assert.Contains(t, w.Body.String(), `"systematic_review"`)
			})

			t.Run("q6 exclusions medicine branch", func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/wisdev/recommendations?sessionId="+sessionID+"&questionId=q6_exclusions", nil)
				w := httptest.NewRecorder()
				h.HandleQuestionRecommendations(w, req)
				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Body.String(), `"questionId":"q6_exclusions"`)
				assert.Contains(t, w.Body.String(), `"animal_studies"`)
			})
		})
	})

	t.Run("RegenerateQuestionOptions", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/regenerate", nil)
			w := httptest.NewRecorder()
			h.HandleRegenerateQuestionOptions(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate", bytes.NewBufferString(`{"sessionId":"s1","questionId":"q1"}`))
			w := httptest.NewRecorder()
			h.HandleRegenerateQuestionOptions(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("invalid body", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate", bytes.NewBufferString(`{`))
			w := httptest.NewRecorder()
			h.HandleRegenerateQuestionOptions(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("missing ids", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate", bytes.NewBufferString(`{"sessionId":"","questionId":""}`))
			w := httptest.NewRecorder()
			h.HandleRegenerateQuestionOptions(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("question not found", func(t *testing.T) {
			h, _, sessionID := newLegacyCompatHandler(t, true)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"missing"}`))
			w := httptest.NewRecorder()
			h.HandleRegenerateQuestionOptions(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})

	t.Run("GenerateQueries", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/generate-queries", nil)
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries", nil)
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("invalid body", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries", bytes.NewBufferString(`{`))
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("missing session id", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries", nil)
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries?sessionId=missing", nil)
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("success", func(t *testing.T) {
			h, _, sessionID := newLegacyCompatHandler(t, true)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/generate-queries?sessionId="+sessionID, nil)
			w := httptest.NewRecorder()
			h.HandleGenerateQueries(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), `"queryCount"`)
		})
	})

	t.Run("ProcessAnswer", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/answer", nil)
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"s1","questionId":"q1","values":["v"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("invalid body", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("missing ids", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"","questionId":"","values":["v"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"missing","questionId":"q1_domain","values":["v"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("guided flow rejects unexpected question", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"not-the-next-question","values":["v"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)

			updated, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			assert.Equal(t, 0, updated.CurrentQuestionIndex)
		})

		t.Run("single-select answers are clamped", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			session, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			session.DetectedDomain = "cs"
			session.QuestionSequence = []string{"q1_domain", "q2_scope", "q3_timeframe"}
			session.CurrentQuestionIndex = 2
			require.NoError(t, sessions.SaveSession(context.Background(), session))

			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q3_timeframe","values":["1year","5years"],"displayValues":["Last Year","Last 5 Years"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusOK, w.Code)

			updated, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			assert.Equal(t, []string{"1year"}, updated.Answers["q3_timeframe"].Values)
			assert.Equal(t, []string{"Last Year"}, updated.Answers["q3_timeframe"].DisplayValues)
		})

		t.Run("save session failure returns internal error", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			session, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			session.ID = "bad/name"
			session.QuestionSequence = []string{"q1_domain", "q2_scope", "q3_timeframe"}
			session.CurrentQuestionIndex = 0
			questionPath := filepath.Join(sessionManagerBaseDir(t, sessions), sessionID+".json")
			raw, err := json.Marshal(session)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(questionPath, raw, 0644))

			req := httptest.NewRequest(http.MethodPost, "/wisdev/answer", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q1_domain","values":["biology"],"displayValues":["Biology"]}`))
			w := httptest.NewRecorder()
			h.HandleProcessAnswer(w, req)
			assert.Equal(t, http.StatusInternalServerError, w.Code)
		})
	})

	t.Run("RegenerateQuestionOptions success", func(t *testing.T) {
		h, _, sessionID := newLegacyCompatHandler(t, true)
		req := httptest.NewRequest(http.MethodPost, "/wisdev/regenerate", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","questionId":"q5_study_types"}`))
		w := httptest.NewRecorder()
		h.HandleRegenerateQuestionOptions(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"questionId":"q5_study_types"`)
	})

	t.Run("NextQuestion", func(t *testing.T) {
		t.Run("method not allowed", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodPost, "/wisdev/next", nil)
			w := httptest.NewRecorder()
			h.HandleNextQuestion(w, req)
			assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
		})

		t.Run("session manager missing", func(t *testing.T) {
			h := NewWisDevHandler(nil, nil, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/next?sessionId=s1", nil)
			w := httptest.NewRecorder()
			h.HandleNextQuestion(w, req)
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		})

		t.Run("missing session id", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/next", nil)
			w := httptest.NewRecorder()
			h.HandleNextQuestion(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})

		t.Run("session not found", func(t *testing.T) {
			h, _, _ := newLegacyCompatHandler(t, false)
			req := httptest.NewRequest(http.MethodGet, "/wisdev/next?sessionId=missing", nil)
			w := httptest.NewRecorder()
			h.HandleNextQuestion(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})

		t.Run("no next question available", func(t *testing.T) {
			h, sessions, sessionID := newLegacyCompatHandler(t, true)
			session, err := sessions.GetSession(context.Background(), sessionID)
			require.NoError(t, err)
			session.CurrentQuestionIndex = 999
			require.NoError(t, sessions.SaveSession(context.Background(), session))

			req := httptest.NewRequest(http.MethodGet, "/wisdev/next?sessionId="+sessionID, nil)
			w := httptest.NewRecorder()
			h.HandleNextQuestion(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	})
}
