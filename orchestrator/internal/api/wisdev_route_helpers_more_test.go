package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

type capturingSessionStore struct {
	put    *wisdev.AgentSession
	putTTL int64
	err    error
}

func (s *capturingSessionStore) Get(context.Context, string) (*wisdev.AgentSession, error) {
	return nil, errors.New("not implemented")
}

func (s *capturingSessionStore) Put(_ context.Context, session *wisdev.AgentSession, _ time.Duration) error {
	s.put = session
	return s.err
}

func (s *capturingSessionStore) Delete(context.Context, string) error { return nil }
func (s *capturingSessionStore) List(context.Context, string) ([]*wisdev.AgentSession, error) {
	return nil, nil
}

func TestFullPaperHelpersMore(t *testing.T) {
	is := assert.New(t)

	t.Run("fullPaperHasTerminalStatus", func(t *testing.T) {
		is.True(fullPaperHasTerminalStatus("completed"))
		is.True(fullPaperHasTerminalStatus("failed"))
		is.True(fullPaperHasTerminalStatus("cancelled"))
		is.False(fullPaperHasTerminalStatus("running"))
		is.False(fullPaperHasTerminalStatus("paused"))
	})

	t.Run("isAllowedFullPaperCheckpointAction", func(t *testing.T) {
		job := map[string]any{"status": "running"}
		// No pending checkpoint
		err := isAllowedFullPaperCheckpointAction(job, "s1", "approve")
		is.Error(err)
		is.Contains(err.Error(), "no pending checkpoint")

		// Wrong stage
		job["pendingCheckpoint"] = map[string]any{"stageId": "s2"}
		err = isAllowedFullPaperCheckpointAction(job, "s1", "approve")
		is.Error(err)
		is.Contains(err.Error(), "checkpoint is not for stage s1")

		// Correct stage and advertised action
		job["pendingCheckpoint"] = map[string]any{"stageId": "s1", "actions": []string{"approve", "skip"}}
		err = isAllowedFullPaperCheckpointAction(job, "s1", "approve")
		is.NoError(err)

		err = isAllowedFullPaperCheckpointAction(job, "s1", "request_revision")
		is.Error(err)
		is.Contains(err.Error(), "not allowed")

		// Legacy checkpoints without an action list use the historical action set.
		job["pendingCheckpoint"] = map[string]any{"stageId": "s1"}
		err = isAllowedFullPaperCheckpointAction(job, "s1", "request_revision")
		is.NoError(err)

		// Terminal status
		job["status"] = "completed"
		err = isAllowedFullPaperCheckpointAction(job, "s1", "approve")
		is.Error(err)
		is.Contains(err.Error(), "terminal status")
	})

	t.Run("applyFullPaperCheckpointAction", func(t *testing.T) {
		job := map[string]any{
			"status":            "pending_approval",
			"pendingCheckpoint": map[string]any{"stageId": "s1"},
		}
		applyFullPaperCheckpointAction(job, "s1", "approve", nil)
		is.Equal("running", job["status"])
		is.Nil(job["pendingCheckpoint"])
		is.NotZero(job["updatedAt"])
	})

	t.Run("isAllowedFullPaperControlAction", func(t *testing.T) {
		job := map[string]any{"status": "running"}

		// Pause allowed when running
		err := isAllowedFullPaperControlAction(job, "pause", "")
		is.NoError(err)

		// Resume not allowed when running
		err = isAllowedFullPaperControlAction(job, "resume", "")
		is.Error(err)

		// Pause not allowed when paused
		job["status"] = "paused"
		err = isAllowedFullPaperControlAction(job, "pause", "")
		is.Error(err)

		// Resume allowed when paused
		err = isAllowedFullPaperControlAction(job, "resume", "")
		is.NoError(err)
	})

	t.Run("applyFullPaperControlAction", func(t *testing.T) {
		job := map[string]any{"status": "running"}
		applyFullPaperControlAction(job, "pause")
		is.Equal("paused", job["status"])

		applyFullPaperControlAction(job, "resume")
		is.Equal("running", job["status"])
	})
}

func TestWisDevRouteHelpersPriorityTargets(t *testing.T) {
	t.Run("resolveAgentSessionQueryMap respects query precedence", func(t *testing.T) {
		assert.Equal(t, "primary", resolveAgentSessionQueryMap(map[string]any{
			"query":          "primary",
			"correctedQuery": "corrected",
			"originalQuery":  "original",
		}))

		assert.Equal(t, "corrected", resolveAgentSessionQueryMap(map[string]any{
			"query":          "   ",
			"correctedQuery": "corrected",
			"originalQuery":  "original",
		}))

		assert.Equal(t, "original", resolveAgentSessionQueryMap(map[string]any{
			"query":          "   ",
			"correctedQuery": "   ",
			"originalQuery":  "original",
		}))

		assert.Equal(t, "", resolveAgentSessionQueryMap(map[string]any{}))
	})

	t.Run("full-paper state load/save helpers", func(t *testing.T) {
		tempDir := t.TempDir()
		t.Setenv("WISDEV_STATE_DIR", tempDir)
		stateStore := wisdev.NewRuntimeStateStore(nil, nil)
		gateway := &wisdev.AgentGateway{StateStore: stateStore}

		documentID := "job-1"
		jobPayload := map[string]any{
			"jobId":  documentID,
			"userId": "owner-1",
			"status": "running",
		}
		require.NoError(t, stateStore.SaveFullPaperJob(documentID, jobPayload))

		loaded, err := loadFullPaperJobState(gateway, documentID)
		require.NoError(t, err)
		assert.Equal(t, documentID, wisdev.AsOptionalString(loaded["jobId"]))

		_, err = loadFullPaperJobState(gateway, "missing-job")
		assert.Error(t, err)
	})

	t.Run("saveFullPaperJobState validates store and id fields and persists evidence dossier", func(t *testing.T) {
		assert.Error(t, saveFullPaperJobState(nil, map[string]any{"jobId": "job-without-store"}))

		filePath := filepath.Join(t.TempDir(), "invalid-state")
		require.NoError(t, os.WriteFile(filePath, []byte("read-only-marker"), 0o644))
		t.Setenv("WISDEV_STATE_DIR", filePath)
		failStore := wisdev.NewRuntimeStateStore(nil, nil)
		err := saveFullPaperJobState(&wisdev.AgentGateway{StateStore: failStore}, map[string]any{"jobId": "job-without-dir"})
		assert.Error(t, err)

		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		okStore := wisdev.NewRuntimeStateStore(nil, nil)
		require.NoError(t, saveFullPaperJobState(&wisdev.AgentGateway{StateStore: okStore}, map[string]any{
			"jobId":  "good-job",
			"userId": "owner-2",
		}))

		require.Error(t, saveFullPaperJobState(&wisdev.AgentGateway{StateStore: okStore}, map[string]any{"userId": "owner-2"}))

		require.NoError(t, saveFullPaperJobState(&wisdev.AgentGateway{StateStore: okStore}, map[string]any{
			"jobId":  "job-dossier",
			"userId": "owner-3",
			"evidenceDossier": map[string]any{
				"title": "Evidence dossier for tests",
			},
		}))
		dossier, err := okStore.LoadEvidenceDossier("job-dossier")
		require.NoError(t, err)
		assert.Equal(t, "job-dossier", wisdev.AsOptionalString(dossier["jobId"]))
		assert.Equal(t, "owner-3", wisdev.AsOptionalString(dossier["userId"]))
		assert.Equal(t, "Evidence dossier for tests", wisdev.AsOptionalString(dossier["title"]))
	})

	t.Run("upsertDraftingState updates outline, sections, and claim packets", func(t *testing.T) {
		t.Run("returns error for missing state store", func(t *testing.T) {
			err := upsertDraftingState(&wisdev.AgentGateway{}, "job-1", map[string]any{"items": []any{}}, "section-1", nil)
			assert.Error(t, err)
		})

		t.Run("merges outline and section payloads", func(t *testing.T) {
			t.Setenv("WISDEV_STATE_DIR", t.TempDir())
			stateStore := wisdev.NewRuntimeStateStore(nil, nil)
			documentID := "document-1"
			err := stateStore.SaveFullPaperJob(documentID, map[string]any{
				"documentId": documentID,
				"userId":     "owner-4",
				"workspace": map[string]any{
					"drafting": map[string]any{
						"sectionArtifactIds": []string{"s-dup", "s-dup"},
						"claimPacketIds":     []string{"existing-claim"},
						"sections":           map[string]any{},
					},
				},
			})
			require.NoError(t, err)

			outline := map[string]any{"items": []any{
				map[string]any{"id": "intro"},
				map[string]any{"id": "methods"},
			}}
			section := map[string]any{
				"claimPacketIds":    []string{"section-claim", "existing-claim"},
				"claimPacketId":     "extra-claim",
				"evidencePacketIds": []string{"ev-1", "section-claim"},
				"evidencePacketId":  "ev-2",
			}
			require.NoError(t, upsertDraftingState(&wisdev.AgentGateway{StateStore: stateStore}, documentID, outline, "methods", section))

			updated, err := stateStore.LoadFullPaperJob(documentID)
			require.NoError(t, err)
			workspace := mapAny(updated["workspace"])
			drafting := mapAny(workspace["drafting"])

			assert.Equal(t, outline, mapAny(drafting["outline"]))
			assert.Equal(t, []string{"intro", "methods"}, sliceStrings(drafting["sectionOrder"]))
			assert.ElementsMatch(t, []string{"s-dup", "methods"}, sliceStrings(drafting["sectionArtifactIds"]))
			assert.Equal(t, []string{"existing-claim", "section-claim", "extra-claim", "ev-1", "ev-2"}, sliceStrings(drafting["claimPacketIds"]))
			sections := mapAny(drafting["sections"])
			actualSection := mapAny(sections["methods"])
			assert.Equal(t, section["claimPacketId"], actualSection["claimPacketId"])
			assert.Equal(t, section["evidencePacketId"], actualSection["evidencePacketId"])
			assert.ElementsMatch(t, []string{"section-claim", "existing-claim"}, sliceStrings(actualSection["claimPacketIds"]))
			assert.ElementsMatch(t, []string{"ev-1", "section-claim"}, sliceStrings(actualSection["evidencePacketIds"]))
		})
	})
}

func TestLoadOwnedFullPaperJobStateAuthorization(t *testing.T) {
	t.Run("returns 404 when job is not found", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		store := wisdev.NewRuntimeStateStore(nil, nil)
		rec := httptest.NewRecorder()
		req := withTestUserID(httptest.NewRequest(http.MethodGet, "/test", nil), "user-1")
		job, ok := loadOwnedFullPaperJobState(rec, req, &wisdev.AgentGateway{StateStore: store}, "missing-job")
		assert.False(t, ok)
		assert.Nil(t, job)
		assert.Equal(t, http.StatusNotFound, rec.Code)
		var payload APIError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.Equal(t, ErrNotFound, payload.Error.Code)
	})

	t.Run("returns 403 when owner is missing or unmatched", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		store := wisdev.NewRuntimeStateStore(nil, nil)
		require.NoError(t, store.SaveFullPaperJob("job-no-owner", map[string]any{
			"jobId":  "job-no-owner",
			"status": "running",
		}))

		rec := httptest.NewRecorder()
		reqNoOwner := withTestUserID(httptest.NewRequest(http.MethodGet, "/test", nil), "owner-1")
		job, ok := loadOwnedFullPaperJobState(rec, reqNoOwner, &wisdev.AgentGateway{StateStore: store}, "job-no-owner")
		assert.False(t, ok)
		assert.Nil(t, job)
		assert.Equal(t, http.StatusForbidden, rec.Code)

		require.NoError(t, store.SaveFullPaperJob("job-owned", map[string]any{
			"jobId":  "job-owned",
			"userId": "owner-2",
			"status": "running",
		}))
		rec = httptest.NewRecorder()
		reqWrong := withTestUserID(httptest.NewRequest(http.MethodGet, "/test", nil), "owner-1")
		job, ok = loadOwnedFullPaperJobState(rec, reqWrong, &wisdev.AgentGateway{StateStore: store}, "job-owned")
		assert.False(t, ok)
		assert.Nil(t, job)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("returns true when job owner matches request user", func(t *testing.T) {
		t.Setenv("WISDEV_STATE_DIR", t.TempDir())
		store := wisdev.NewRuntimeStateStore(nil, nil)
		require.NoError(t, store.SaveFullPaperJob("job-owned", map[string]any{
			"jobId":  "job-owned",
			"userId": "owner-3",
			"status": "running",
		}))

		rec := httptest.NewRecorder()
		req := withTestUserID(httptest.NewRequest(http.MethodGet, "/test", nil), "owner-3")
		job, ok := loadOwnedFullPaperJobState(rec, req, &wisdev.AgentGateway{StateStore: store}, "job-owned")
		assert.True(t, ok)
		assert.NotNil(t, job)
		assert.Equal(t, "job-owned", wisdev.AsOptionalString(job["jobId"]))
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestWisDevRouteHelperBranchCoverage(t *testing.T) {
	t.Run("primitive normalization helpers", func(t *testing.T) {
		assert.Equal(t, 0.0, coerceFloatValue(nil))
		assert.Equal(t, 1.25, coerceFloatValue(float64(1.25)))
		assert.Equal(t, 2.5, coerceFloatValue(float32(2.5)))
		assert.Equal(t, 3.0, coerceFloatValue(3))
		assert.Equal(t, 4.0, coerceFloatValue(int64(4)))
		assert.Equal(t, 5.75, coerceFloatValue(" 5.75 "))
		assert.Equal(t, wisdev.SessionExecutingPlan, normalizeAgentSessionStatus("running"))
		assert.Equal(t, wisdev.SessionComplete, normalizeAgentSessionStatus("complete"))
		assert.Equal(t, wisdev.SessionFailed, normalizeAgentSessionStatus("failed"))
		assert.Equal(t, wisdev.SessionPaused, normalizeAgentSessionStatus("paused"))
		assert.Equal(t, wisdev.SessionQuestioning, normalizeAgentSessionStatus("other"))
		assert.Equal(t, "medicine", normalizeAgentQuestionDomainHint("Clinical medicine"))
		assert.Equal(t, "cs", normalizeAgentQuestionDomainHint("machine learning"))
		assert.Equal(t, "biology", normalizeAgentQuestionDomainHint("life sciences"))
		assert.Equal(t, "architecture", normalizeAgentQuestionDomainHint("architecture"))
		assert.Equal(t, "advanced", agentQuestionExpertiseLevel("biology"))
		assert.Equal(t, "intermediate", agentQuestionExpertiseLevel("physics"))
	})

	t.Run("boundedInt and validatePayloadSize", func(t *testing.T) {
		assert.Equal(t, 5, boundedInt(5, 5, 1, 10))
		assert.Equal(t, 5, boundedInt(-1, 5, 1, 10))
		assert.Equal(t, 10, boundedInt(50, 5, 1, 10))
		assert.NoError(t, validatePayloadSize(map[string]any{"a": 1}, "payload", 0))
		assert.NoError(t, validatePayloadSize(map[string]any{"a": 1}, "payload", 32))
		assert.Error(t, validatePayloadSize(map[string]any{"a": "1234567890"}, "payload", 4))
	})

	t.Run("idempotency helpers", func(t *testing.T) {
		assert.Nil(t, normalizeIdempotencyStrings([]string{}))
		assert.Equal(t, []string{"a", "b"}, normalizeIdempotencyStrings([]string{" a ", "", "b"}))
		assert.Nil(t, normalizedStringSet([]string{" ", ""}))
		assert.True(t, equalNormalizedStringSets([]string{"b", "a"}, []string{"a", "b"}))
		assert.False(t, equalNormalizedStringSets([]string{"a"}, []string{"b"}))
	})

	t.Run("pending follow-up helpers", func(t *testing.T) {
		assert.Equal(t, "q5_study_types", inferPendingAgentFollowUpTargetQuestionID(map[string]any{
			"question":            "study design",
			"questionExplanation": "why",
		}))
		assert.Equal(t, "q4_subtopics", inferPendingAgentFollowUpTargetQuestionID(map[string]any{
			"question": "broader question",
		}))

		session := map[string]any{"answers": map[string]any{}}
		target := mirrorPendingAgentFollowUpAnswer(session, map[string]any{"id": "q3_timeframe", "targetQuestionId": "q5_study_types"}, []string{"rct"}, []string{"RCT"})
		assert.Equal(t, "q5_study_types", target)
		assert.Contains(t, session["answers"], "q5_study_types")
		assert.True(t, agentAnswerAlreadyApplied(session, "q5_study_types", []string{"rct"}, []string{"RCT"}))
		assert.False(t, agentAnswerAlreadyApplied(session, "q5_study_types", []string{"meta_analysis"}, []string{"Meta"}))

		assert.Equal(t, "", inferPendingAgentFollowUpTargetQuestionID(map[string]any{"targetQuestionId": "q1_domain"}))
		assert.Equal(t, "", mirrorPendingAgentFollowUpAnswer(session, map[string]any{"targetQuestionId": "q1_domain"}, []string{"medicine"}, []string{"Medicine"}))
		assert.NotContains(t, session["answers"], "q1_domain")

		blankSession := map[string]any{"answers": map[string]any{}}
		assert.Equal(t, "", mirrorPendingAgentFollowUpAnswer(blankSession, map[string]any{"targetQuestionId": "q4_subtopics"}, []string{"  "}, nil))
		assert.Empty(t, mapAny(blankSession["answers"]))

		inferredSession := map[string]any{"answers": map[string]any{}}
		inferredTarget := mirrorPendingAgentFollowUpAnswer(inferredSession, map[string]any{
			"id":       "follow_up_refinement",
			"helpText": "Pick study type evidence to prioritize",
		}, []string{"randomized controlled trial"}, nil)
		assert.Equal(t, "q5_study_types", inferredTarget)
		inferredAnswer := mapAny(mapAny(inferredSession["answers"])["q5_study_types"])
		assert.Equal(t, []string{"randomized controlled trial"}, sliceStrings(inferredAnswer["values"]))
		assert.Equal(t, []string{"randomized controlled trial"}, sliceStrings(inferredAnswer["displayValues"]))

		requiredSession := map[string]any{
			"questionSequence": []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			"questions": []any{
				map[string]any{"id": "q4_subtopics", "isRequired": true},
				map[string]any{"id": "q5_study_types", "isRequired": false},
			},
			"pendingFollowUpQuestion": map[string]any{
				"id":               "follow_up_refinement",
				"targetQuestionId": "q4_subtopics",
				"isRequired":       true,
			},
		}
		assert.True(t, isAgentQuestionRequired(requiredSession, "q4_subtopics"))
		assert.False(t, isAgentQuestionRequired(requiredSession, "q5_study_types"))
		assert.True(t, isAgentQuestionRequired(requiredSession, "follow_up_refinement"))
		assert.False(t, hasNonEmptyAnswerValues([]string{}))
		assert.False(t, hasNonEmptyAnswerValues([]string{"  "}))
		assert.True(t, hasNonEmptyAnswerValues([]string{"sleep"}))
	})

	t.Run("canonical session helpers", func(t *testing.T) {
		sessionMap := map[string]any{
			"sessionId":            "session-1",
			"userId":               "user-1",
			"query":                "machine learning",
			"originalQuery":        " machine learning ",
			"correctedQuery":       "machine learning",
			"detectedDomain":       "cs",
			"secondaryDomains":     []string{"biology"},
			"status":               "running",
			"currentQuestionIndex": 2,
			"questionSequence":     []string{"q1_domain", "q2_scope"},
			"minQuestions":         2,
			"maxQuestions":         4,
			"complexityScore":      "1.5",
			"clarificationBudget":  3,
			"questionStopReason":   "user_proceed",
			"answers": map[string]any{
				"q1_domain": map[string]any{"values": []string{"cs"}, "answeredAt": int64(1)},
			},
			"mode":        "guided",
			"serviceTier": "standard",
			"createdAt":   int64(10),
			"updatedAt":   int64(20),
		}
		canonical := buildCanonicalAgentSession(sessionMap)
		require.NotNil(t, canonical)
		assert.Equal(t, "session-1", canonical.SessionID)
		assert.Equal(t, wisdev.SessionExecutingPlan, canonical.Status)
		assert.Equal(t, 1.5, canonical.ComplexityScore)
		assert.Equal(t, "cs", canonical.DetectedDomain)
		assert.Equal(t, "machine learning", canonical.CorrectedQuery)
		assert.NotNil(t, canonical.ReasoningGraph)
		assert.NotNil(t, canonical.MemoryTiers)

		assert.Nil(t, buildCanonicalAgentSession(map[string]any{}))
		assert.Nil(t, buildCanonicalAgentSession(map[string]any{
			"sessionId":      "s2",
			"query":          "",
			"originalQuery":  "",
			"correctedQuery": "",
		}))
	})

	t.Run("planning helpers", func(t *testing.T) {
		questions, sequence, minQuestions, maxQuestions := defaultAgentQuestionPlan("simple query", "physics", nil)
		require.NotEmpty(t, questions)
		assert.Equal(t, []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics", "q5_study_types", "q6_exclusions"}, sequence)
		assert.Equal(t, 6, minQuestions)
		assert.Equal(t, len(sequence), maxQuestions)

		questions, sequence, minQuestions, maxQuestions = defaultAgentQuestionPlan(
			"systematic review meta-analysis reproducibility causal longitudinal and versus evidence synthesis comparison of treatment outcomes across randomized cohorts",
			"medicine",
			[]string{"biology", "chemistry"},
		)
		require.NotEmpty(t, questions)
		assert.Contains(t, sequence, "q4_subtopics")
		assert.Contains(t, sequence, "q5_study_types")
		assert.Contains(t, sequence, "q6_exclusions")
		assert.Contains(t, sequence, "q7_evidence_quality")
		assert.Contains(t, sequence, "q8_output_focus")
		assert.GreaterOrEqual(t, minQuestions, 8)
		assert.Equal(t, len(sequence), maxQuestions)
	})

	t.Run("adaptive follow-up helpers", func(t *testing.T) {
		assert.False(t, replanAgentSessionForDomainAnswer(nil))
		assert.False(t, replanAgentSessionForDomainAnswer(map[string]any{}))

		session := map[string]any{
			"query": "systematic review meta-analysis reproducibility causal longitudinal and versus evidence synthesis comparison of treatment outcomes",
			"answers": map[string]any{
				"q1_domain": map[string]any{"values": []string{"medicine"}},
			},
		}
		assert.True(t, replanAgentSessionForDomainAnswer(session))
		assert.Equal(t, "medicine", session["detectedDomain"])
		assert.NotEmpty(t, session["questions"])

		pending := map[string]any{
			"status": "questioning",
			"answers": map[string]any{
				"q3_timeframe": map[string]any{"values": []string{"5years"}},
			},
			"questionSequence": []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"},
			"complexityScore":  0.8,
			"query":            "systematic review of therapy outcomes and vs comparison",
		}
		pending["pendingFollowUpQuestion"] = map[string]any{"id": "follow_up"}

		pending["pendingFollowUpQuestion"] = map[string]any{"question": "refinement"}
		question := buildAgentQuestionPayload(pending, false)
		require.NotNil(t, question)
		assert.Equal(t, "follow_up_refinement", wisdev.AsOptionalString(question["id"]))

		pending["questionSequence"] = []string{"q1_domain", "q2_scope", "q3_timeframe"}
		pending["pendingFollowUpQuestion"] = map[string]any{
			"question":         "Stale follow-up from an older base session",
			"targetQuestionId": "q4_subtopics",
		}
		assert.Nil(t, getPendingAgentFollowUpQuestion(pending))

		pending["questionSequence"] = []string{"q1_domain", "q2_scope", "q3_timeframe", "q4_subtopics"}

		pending["pendingFollowUpQuestion"] = map[string]any{
			"question": "Which missing area should WisDev expand next?",
			"options": []any{
				map[string]any{"value": "RLHF", "label": "RLHF"},
				map[string]any{"value": "Reinforcement Learning", "label": "Reinforcement Learning"},
				map[string]any{"value": "Reinforcement Learning Benchmarks", "label": "Reinforcement Learning Benchmarks"},
				map[string]any{"value": "Reinforcement Learning Training Data", "label": "Reinforcement Learning Training Data"},
			},
			"targetQuestionId": "q4_subtopics",
		}
		pending["query"] = "RLHF reinforcement learning"
		question = buildAgentQuestionPayload(pending, false)
		require.NotNil(t, question)
		assert.Equal(t, "Which focus should the next search pass prioritize?", wisdev.AsOptionalString(question["question"]))
		assert.Equal(t, []string{
			"RLHF methods and reward modeling",
			"RL optimization methods",
			"Evaluation benchmarks and generalization",
			"Training data and feedback quality",
		}, questionOptionValues(question["options"]))

		pending["pendingFollowUpQuestion"] = map[string]any{
			"question":         "Invalid stored follow-up",
			"targetQuestionId": "q1_domain",
		}
		assert.Nil(t, getPendingAgentFollowUpQuestion(pending))
	})

	t.Run("question domain normalization and payload branches", func(t *testing.T) {
		primary, secondary := normalizeAgentQuestionDomains([]string{"  Biology ", "cs", "Biology", " ", "physics"})
		assert.Equal(t, "biology", primary)
		assert.Equal(t, []string{"cs", "physics"}, secondary)

		pendingSession := map[string]any{
			"status":           "questioning",
			"questionSequence": []string{"q1_domain", "q2_scope"},
			"pendingFollowUpQuestion": map[string]any{
				"question":         "Need more refinement",
				"targetQuestionId": "q4_subtopics",
			},
			"questions": []any{
				map[string]any{"id": "q1_domain"},
				map[string]any{"id": "q2_scope"},
			},
		}
		pendingQuestion := buildAgentQuestionPayload(pendingSession, false)
		require.NotNil(t, pendingQuestion)
		assert.Equal(t, "q1_domain", wisdev.AsOptionalString(pendingQuestion["id"]))

		canonicalSession := map[string]any{
			"status":           "questioning",
			"query":            "systematic review of interventions",
			"questionSequence": []string{"q1_domain", "q2_scope"},
			"questions": []any{
				map[string]any{"id": "q1_domain"},
				map[string]any{"id": "q2_scope"},
			},
			"answers": map[string]any{
				"q1_domain": map[string]any{"values": []string{"medicine"}},
			},
		}
		canonicalQuestion := buildAgentQuestionPayload(canonicalSession, false)
		require.NotNil(t, canonicalQuestion)
		assert.Equal(t, "q1_domain", wisdev.AsOptionalString(canonicalQuestion["id"]))

		indexFallbackSession := map[string]any{
			"status":               "questioning",
			"currentQuestionIndex": 1,
			"questions": []any{
				map[string]any{"id": "q1_domain"},
				map[string]any{"id": "q2_scope"},
			},
		}
		indexFallbackQuestion := buildAgentQuestionPayload(indexFallbackSession, false)
		require.NotNil(t, indexFallbackQuestion)
		assert.Equal(t, "q2_scope", wisdev.AsOptionalString(indexFallbackQuestion["id"]))

		assert.Nil(t, buildAgentQuestionPayload(map[string]any{"status": "questioning"}, false))
	})

	t.Run("syncCanonicalSessionStore and attachEvidenceDossier", func(t *testing.T) {
		store := &capturingSessionStore{}
		gateway := &wisdev.AgentGateway{Store: store}
		sessionMap := map[string]any{
			"sessionId":      "session-2",
			"userId":         "user-2",
			"query":          "biology",
			"originalQuery":  "biology",
			"correctedQuery": "biology",
		}
		require.NoError(t, syncCanonicalSessionStore(gateway, sessionMap))
		require.NotNil(t, store.put)
		assert.Equal(t, "session-2", store.put.SessionID)

		gatewayNilStore := &wisdev.AgentGateway{}
		assert.NoError(t, syncCanonicalSessionStore(gatewayNilStore, sessionMap))
		assert.Error(t, syncCanonicalSessionStore(gateway, map[string]any{"sessionId": "bad"}))

		tmpDir := t.TempDir()
		t.Setenv("WISDEV_STATE_DIR", tmpDir)
		stateStore := wisdev.NewRuntimeStateStore(nil, nil)
		payload := map[string]any{}
		attachEvidenceDossier(&wisdev.AgentGateway{StateStore: stateStore}, payload, "job-1", "machine learning safety", "user-1", []wisdev.Source{{ID: "p1", Title: "Paper 1", DOI: "10.1/example"}})
		dossier, ok := payload["evidenceDossier"].(map[string]any)
		require.True(t, ok)
		require.NotEmpty(t, dossier["dossierId"])
		loaded, err := stateStore.LoadEvidenceDossier(wisdev.AsOptionalString(dossier["dossierId"]))
		require.NoError(t, err)
		assert.Equal(t, "job-1", loaded["jobId"])

		emptyPayload := map[string]any{}
		attachEvidenceDossier(nil, emptyPayload, "", "", "", nil)
		assert.Empty(t, emptyPayload)
	})
}
