package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redismock/v9"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	internalsearch "github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

type testMemoryStore struct {
	working   map[string][]MemoryEntry
	longTerm  map[string][]MemoryEntry
	artifacts map[string][]MemoryEntry
	prefs     map[string][]MemoryEntry
}

func newTestMemoryStore() *testMemoryStore {
	return &testMemoryStore{
		working:   map[string][]MemoryEntry{},
		longTerm:  map[string][]MemoryEntry{},
		artifacts: map[string][]MemoryEntry{},
		prefs:     map[string][]MemoryEntry{},
	}
}

func (s *testMemoryStore) SaveWorkingMemory(_ context.Context, sessionID string, entries []MemoryEntry, _ time.Duration) error {
	s.working[sessionID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *testMemoryStore) LoadWorkingMemory(_ context.Context, sessionID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.working[sessionID]...), nil
}

func (s *testMemoryStore) SaveLongTermVector(_ context.Context, userID string, entries []MemoryEntry, _ time.Duration) error {
	s.longTerm[userID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *testMemoryStore) LoadLongTermVector(_ context.Context, userID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.longTerm[userID]...), nil
}

func (s *testMemoryStore) AppendLongTermVector(_ context.Context, userID string, entries []MemoryEntry, _ time.Duration) error {
	s.longTerm[userID] = mergeEntries(s.longTerm[userID], entries)
	return nil
}

func (s *testMemoryStore) SaveArtifacts(_ context.Context, sessionID string, entries []MemoryEntry) error {
	s.artifacts[sessionID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *testMemoryStore) LoadArtifacts(_ context.Context, sessionID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.artifacts[sessionID]...), nil
}

func (s *testMemoryStore) SaveUserPreferences(_ context.Context, userID string, entries []MemoryEntry) error {
	s.prefs[userID] = append([]MemoryEntry(nil), entries...)
	return nil
}

func (s *testMemoryStore) LoadUserPreferences(_ context.Context, userID string) ([]MemoryEntry, error) {
	return append([]MemoryEntry(nil), s.prefs[userID]...), nil
}

func (s *testMemoryStore) SaveTiers(_ context.Context, sessionID, userID string, tiers *MemoryTierState, _, _ time.Duration) error {
	if tiers == nil {
		return nil
	}
	s.working[sessionID] = append([]MemoryEntry(nil), tiers.ShortTermWorking...)
	s.longTerm[userID] = append([]MemoryEntry(nil), tiers.LongTermVector...)
	s.artifacts[sessionID] = append([]MemoryEntry(nil), tiers.ArtifactMemory...)
	s.prefs[userID] = append([]MemoryEntry(nil), tiers.UserPersonalized...)
	return nil
}

func (s *testMemoryStore) LoadTiers(_ context.Context, sessionID, userID string) (*MemoryTierState, error) {
	return &MemoryTierState{
		ShortTermWorking: append([]MemoryEntry(nil), s.working[sessionID]...),
		LongTermVector:   append([]MemoryEntry(nil), s.longTerm[userID]...),
		ArtifactMemory:   append([]MemoryEntry(nil), s.artifacts[sessionID]...),
		UserPersonalized: append([]MemoryEntry(nil), s.prefs[userID]...),
	}, nil
}

func TestResearchQuestRuntimeHonorsPreferenceOptIn(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
			Status:        "promoted",
			Promoted:      true,
			VerifiedCount: 1,
		}))

		quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
			UserID:      "user-1",
			Query:       "test quest",
			QualityMode: "quality",
		})
		require.NoError(t, err)

		assert.False(t, quest.PersistUserPreferences)
		assert.Empty(t, quest.Memory.UserPersonalized)
		assert.Equal(t, false, quest.Memory.PromotionRules["userPreferenceOptIn"])
	})

	t.Run("enabled only with explicit opt-in", func(t *testing.T) {
		runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
			Status:        "promoted",
			Promoted:      true,
			VerifiedCount: 1,
		}))

		quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
			UserID:                 "user-2",
			Query:                  "test quest opt in",
			QualityMode:            "quality",
			PersistUserPreferences: true,
		})
		require.NoError(t, err)

		assert.True(t, quest.PersistUserPreferences)
		assert.Len(t, quest.Memory.UserPersonalized, 1)
		assert.Equal(t, true, quest.Memory.PromotionRules["userPreferenceOptIn"])
	})
}

func TestResearchQuestRuntimeEscalatesHeavyModelForLargeRetrievalSets(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(51), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 51,
	}))

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "user-3",
		Query:       "evidence retrieval breadth",
		QualityMode: "balanced",
	})
	require.NoError(t, err)

	assert.Equal(t, 51, quest.RetrievedCount)
	assert.True(t, quest.HeavyModelRequired)
	assert.Equal(t, ModelTierHeavy, quest.DecisionModelTier)
	assert.Equal(t, ModelTierHeavy, quest.ExecutionProfile.PrimaryModelTier)
	assert.Contains(t, questAgentRoles(quest.AgentAssignments), "arbiter")
}

func TestResearchQuestRuntimeCritiqueReopensRetrievalForCoverageGaps(t *testing.T) {
	var retrieveCalls int
	hooks := stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	})
	hooks.RetrieveFn = func(_ context.Context, _ *ResearchQuest) ([]Source, map[string]any, error) {
		retrieveCalls++
		if retrieveCalls == 1 {
			return []Source{{
				ID:      "paper-1",
				Title:   "Paper 1",
				Summary: "Sleep improves declarative memory recall.",
				DOI:     "10.1000/test-1",
				Source:  "crossref",
				Score:   0.9,
			}}, map[string]any{"count": 1}, nil
		}
		return []Source{
			{
				ID:      "paper-1",
				Title:   "Paper 1",
				Summary: "Sleep improves declarative memory recall.",
				DOI:     "10.1000/test-1",
				Source:  "crossref",
				Score:   0.9,
			},
			{
				ID:      "paper-2",
				Title:   "Paper 2",
				Summary: "Independent replication supports overnight consolidation.",
				DOI:     "10.1000/test-2",
				Source:  "openalex",
				Score:   0.88,
			},
		}, map[string]any{"count": 2}, nil
	}
	hooks.CritiqueFn = func(_ context.Context, _ *ResearchQuest, _ []Source, _ CitationVerdict) (string, error) {
		return "more evidence needed for source diversity", nil
	}

	runtime := newResearchQuestRuntimeForTest(t, hooks)
	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "coverage-user",
		Query:       "sleep consolidation",
		QualityMode: "balanced",
	})
	require.NoError(t, err)

	assert.Equal(t, 2, retrieveCalls)
	assert.Equal(t, QuestStatusComplete, quest.Status)
	assert.GreaterOrEqual(t, quest.CurrentIteration, 1)
	assert.NotEmpty(t, quest.CoverageLedger)
	assert.Contains(t, quest.ReviewerNotes, "more evidence needed for source diversity")
	assert.Len(t, quest.Papers, 2)
}

func TestResearchQuestRuntimeBlocksPublicationOnCitationConflicts(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:              "blocked",
		Promoted:            false,
		VerifiedCount:       0,
		AmbiguousCount:      1,
		RejectedCount:       0,
		BlockingIssues:      []string{"ambiguous citation metadata"},
		ConflictNote:        "resolver conflict detected",
		RequiresHumanReview: true,
		PromotionGate: map[string]any{
			"promoted":       false,
			"blockingIssues": []string{"ambiguous citation metadata"},
		},
	}))

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "user-4",
		Query:       "citation conflict review",
		QualityMode: "quality",
	})
	require.NoError(t, err)

	assert.Equal(t, QuestStatusBlocked, quest.Status)
	assert.True(t, quest.HeavyModelRequired)
	assert.Equal(t, ModelTierHeavy, quest.DecisionModelTier)
	assert.Contains(t, questAgentRoles(quest.AgentAssignments), "arbiter")
	assert.Contains(t, quest.FinalAnswer, "Citation gate blocked publication")
	assert.Contains(t, quest.BlockingIssues, "ambiguous citation metadata")
	assert.Empty(t, quest.Memory.LongTermVector)
}

func TestResearchQuestRuntimeResumeReusesCheckpointedStages(t *testing.T) {
	var retrieveCalls int
	var draftCalls int
	hooks := stubQuestHooks(testQuestSources(2), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 2,
	})
	hooks.RetrieveFn = func(_ context.Context, _ *ResearchQuest) ([]Source, map[string]any, error) {
		retrieveCalls++
		return testQuestSources(2), map[string]any{
			"count":   2,
			"traceId": "quest-test-trace",
		}, nil
	}
	hooks.DraftFn = func(_ context.Context, _ *ResearchQuest, _ []Source, _ map[string]any) (string, error) {
		draftCalls++
		if draftCalls == 1 {
			return "", fmt.Errorf("draft unavailable")
		}
		return "quest draft", nil
	}

	runtime := newResearchQuestRuntimeForTest(t, hooks)
	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "resume-user",
		Query:       "resume checkpoint path",
		QualityMode: "quality",
	})
	require.NoError(t, err)
	require.Equal(t, QuestStageDraft, quest.CurrentStage)
	require.Equal(t, 1, retrieveCalls)

	checkpoint, err := runtime.loadQuestCheckpoint(context.Background(), quest.QuestID)
	require.NoError(t, err)
	require.NotNil(t, checkpoint)
	assert.Equal(t, quest.QuestID, checkpoint.QuestID)
	assert.Equal(t, 2, checkpoint.RetrievedCount)
	assert.Len(t, checkpoint.Papers, 2)

	resumed, err := runtime.ResumeQuest(context.Background(), quest.QuestID, ResearchQuestRequest{})
	require.NoError(t, err)
	assert.Equal(t, QuestStatusComplete, resumed.Status)
	assert.GreaterOrEqual(t, retrieveCalls, 1)
	assert.GreaterOrEqual(t, draftCalls, 2)
}

func TestResearchQuestRuntimeCheckpointsBeforeFirstStageWork(t *testing.T) {
	hooks := stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	})
	var runtime *ResearchQuestRuntime
	var sawInitialCheckpoint bool
	hooks.DecomposeFn = func(ctx context.Context, quest *ResearchQuest) (map[string]any, error) {
		checkpoint, err := runtime.loadQuestCheckpoint(ctx, quest.QuestID)
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		assert.Equal(t, QuestStatusRunning, checkpoint.Status)
		assert.Equal(t, QuestStageInit, checkpoint.CurrentStage)
		require.NotEmpty(t, checkpoint.Events)
		assert.Equal(t, "quest_started", checkpoint.Events[len(checkpoint.Events)-1].Type)
		sawInitialCheckpoint = true
		return map[string]any{
			"tasks": []map[string]any{
				{"id": "t1", "name": "initial checkpoint proof"},
			},
		}, nil
	}
	runtime = newResearchQuestRuntimeForTest(t, hooks)

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "checkpoint-before-work-user",
		Query:       "checkpoint before first stage",
		QualityMode: "quality",
	})
	require.NoError(t, err)
	require.Equal(t, QuestStatusComplete, quest.Status)
	assert.True(t, sawInitialCheckpoint)
}

func TestResearchQuestRuntimeDefaultRetrieveRequiresUnifiedRuntime(t *testing.T) {
	original := questRunRetrievePapers
	defer func() {
		questRunRetrievePapers = original
	}()

	questRunRetrievePapers = func(context.Context, redis.UniversalClient, string, SearchOptions) ([]Source, map[string]any, error) {
		t.Fatal("quest retrieval must not bypass UnifiedResearchRuntime")
		return nil, nil, nil
	}

	runtime := NewResearchQuestRuntime(&AgentGateway{})
	papers, payload, err := runtime.defaultRetrieve(context.Background(), &ResearchQuest{
		QuestID: "quest-runtime-required",
		UserID:  "retrieval-user",
		Query:   "canonical runtime required",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "UnifiedResearchRuntime")
	require.Nil(t, papers)
	require.Nil(t, payload)
}

func TestResearchQuestRuntimePersistsCanonicalRuntimeAcrossQuestStages(t *testing.T) {
	original := questRunUnifiedResearchLoop
	t.Cleanup(func() {
		questRunUnifiedResearchLoop = original
	})

	questRunUnifiedResearchLoop = func(_ context.Context, _ *UnifiedResearchRuntime, req LoopRequest) (*UnifiedResearchResult, error) {
		require.Equal(t, "canonical quest query", req.Query)
		return &UnifiedResearchResult{
			State: &ResearchSessionState{
				SessionID:       "quest-canonical",
				Query:           req.Query,
				Plane:           ResearchExecutionPlaneQuest,
				PlannedQueries:  []string{req.Query, "canonical follow up"},
				ExecutedQueries: []string{req.Query},
				BranchPlans: []ResearchBranchPlan{
					{ID: "branch-canonical", Query: req.Query, Hypothesis: "Canonical branch hypothesis", Status: "selected"},
				},
				BranchEvaluations: []ResearchBranchEvaluation{
					{ID: "branch-canonical", Query: req.Query, Status: "promote", OverallScore: 0.94, VerifierVerdict: "promote"},
				},
				VerifierDecision: &ResearchVerifierDecision{
					Role:             ResearchWorkerIndependentVerifier,
					Verdict:          "promote",
					StopReason:       "verified_final",
					PromotedClaimIDs: []string{"canonical-finding"},
					Confidence:       0.94,
					EvidenceOnly:     true,
				},
				Workers: []ResearchWorkerState{
					{Role: ResearchWorkerScout, Status: "completed", PlannedQueries: []string{req.Query}},
				},
				Blackboard: &ResearchBlackboard{
					ReadyForSynthesis: true,
					OpenLedgerCount:   0,
				},
				StopReason: "verified_final",
				CoverageLedger: []CoverageLedgerEntry{
					{
						ID:                "ledger-canonical",
						Category:          "source_diversity",
						Status:            coverageLedgerStatusResolved,
						Title:             "Canonical coverage closed",
						SupportingQueries: []string{req.Query},
						SourceFamilies:    []string{"crossref"},
						Confidence:        0.91,
					},
				},
			},
			LoopResult: &LoopResult{
				FinalAnswer:     "canonical final answer",
				ExecutedQueries: []string{req.Query},
				Papers: []internalsearch.Paper{
					{ID: "canonical-paper", Title: "Canonical Paper", Abstract: "Canonical evidence", Source: "crossref", DOI: "10.1000/canonical"},
				},
				Evidence: []EvidenceFinding{
					{ID: "canonical-finding", Claim: "Canonical runtime evidence survived fusion.", SourceID: "canonical-paper", Status: "accepted", Confidence: 0.93},
				},
				FinalizationGate: &ResearchFinalizationGate{
					Status:           "promote",
					Ready:            true,
					Provisional:      false,
					StopReason:       "verified_final",
					VerifierVerdict:  "promote",
					PromotedClaimIDs: []string{"canonical-finding"},
				},
				StopReason: "verified_final",
				GapAnalysis: &LoopGapState{
					Sufficient:             true,
					ObservedSourceFamilies: []string{"crossref"},
					ObservedEvidenceCount:  1,
					Ledger: []CoverageLedgerEntry{
						{ID: "ledger-canonical", Category: "source_diversity", Status: coverageLedgerStatusResolved, Title: "Canonical coverage closed"},
					},
					Coverage: LoopCoverageState{PlannedQueryCount: 2, ExecutedQueryCount: 1, CoveredQueryCount: 1, UniquePaperCount: 1},
				},
			},
		}, nil
	}

	runtime := newResearchQuestRuntimeWithMemoryStore(t, newTestMemoryStore(), ResearchQuestHooks{
		CitationFn: func(_ context.Context, _ *ResearchQuest, papers []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error) {
			return []CitationAuthorityRecord{{Authority: "crossref", CanonicalID: papers[0].DOI, DOI: papers[0].DOI, Title: papers[0].Title, Resolved: true, Verified: true}}, CitationVerdict{
				Status:        "promoted",
				Promoted:      true,
				VerifiedCount: 1,
			}, map[string]any{"promotionGate": map[string]any{"promoted": true}}, nil
		},
		CritiqueFn: func(_ context.Context, _ *ResearchQuest, _ []Source, _ CitationVerdict) (string, error) {
			return "canonical critique", nil
		},
	})
	runtime.gateway.Runtime = NewUnifiedResearchRuntime(nil, nil, nil, nil)

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID: "canonical-user",
		Query:  "canonical quest query",
	})
	require.NoError(t, err)
	require.Equal(t, QuestStatusComplete, quest.Status)
	require.Equal(t, "canonical final answer", quest.FinalAnswer)
	require.Len(t, quest.AcceptedClaims, 1)
	require.Equal(t, "canonical-finding", quest.AcceptedClaims[0].ID)

	canonicalPayload := asMap(quest.Artifacts[questCanonicalRuntimeArtifactKey])
	require.Equal(t, "unified_research_runtime", AsOptionalString(canonicalPayload["engine"]))
	require.Equal(t, "canonical final answer", AsOptionalString(canonicalPayload["finalAnswer"]))
	require.Equal(t, "verified", AsOptionalString(canonicalPayload["answerStatus"]))
	require.Equal(t, "verified_final", AsOptionalString(canonicalPayload["stopReason"]))
	require.Len(t, firstArtifactMaps(canonicalPayload["evidence"]), 1)
	require.Len(t, canonicalPayload["branchPlans"].([]any), 1)
	require.Len(t, canonicalPayload["branchEvaluations"].([]any), 1)
	require.Len(t, canonicalPayload["workerReports"].([]any), 1)
	require.Equal(t, "promote", AsOptionalString(asMap(canonicalPayload["verifierDecision"])["verdict"]))
	require.Equal(t, "promote", AsOptionalString(asMap(canonicalPayload["finalizationGate"])["status"]))
	require.Equal(t, "canonical final answer", quest.ResearchScratchpad["canonicalFinalAnswer"])
}

func TestResearchQuestRuntimeLoadQuestFallsBackToCheckpoint(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	}))

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "checkpoint-user",
		Query:       "checkpoint fallback",
		QualityMode: "quality",
	})
	require.NoError(t, err)

	stateFile := filepath.Join(runtime.stateStore.BaseDir(), "quest_state_"+quest.QuestID+".json")
	require.NoError(t, os.Remove(stateFile))

	loaded, err := runtime.LoadQuest(context.Background(), quest.QuestID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, quest.QuestID, loaded.QuestID)
	assert.Equal(t, quest.FinalAnswer, loaded.FinalAnswer)
}

func TestResearchQuestRuntimeLoadsRedisCheckpointAfterProcessStateLoss(t *testing.T) {
	db, mock := redismock.NewClientMock()
	quest := &ResearchQuest{
		SessionID:    "redis-quest",
		QuestID:      "redis-quest",
		UserID:       "redis-user",
		Query:        "redis checkpoint recovery",
		Status:       QuestStatusRunning,
		CurrentStage: QuestStageRetrieve,
		Artifacts:    map[string]any{},
		Events: []QuestEvent{
			{Type: "quest_started", Stage: QuestStageInit, Summary: "Research quest started"},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}
	body, err := json.Marshal(quest)
	require.NoError(t, err)
	mock.ExpectGet("wisdev_checkpoint:redis-quest").SetVal(string(body))

	runtime := NewResearchQuestRuntime(&AgentGateway{
		Checkpoints: NewRedisCheckpointStore(db),
		MemoryStore: &NoopMemoryStore{},
	})
	loaded, err := runtime.LoadQuest(context.Background(), "redis-quest")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "redis-quest", loaded.QuestID)
	assert.Equal(t, QuestStageRetrieve, loaded.CurrentStage)
	assert.Equal(t, QuestStatusRunning, loaded.Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestResearchQuestRuntimeGracefullyRejectsWhenResourceGovernorCritical(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	}))
	runtime.gateway.ResourceGovernor = resilience.NewResourceGovernorWithInterval(100, 100, 0)
	runtime.gateway.ResourceGovernor.SetStatus("critical")

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID: "overload-user",
		Query:  "overloaded research loop",
	})
	require.NoError(t, err)
	require.Equal(t, QuestStatusBlocked, quest.Status)
	assert.Contains(t, quest.BlockingIssues, "system_overload")
	require.NotEmpty(t, quest.Events)
	assert.Equal(t, "resource_rejected", quest.Events[len(quest.Events)-1].Type)
}

func TestResearchQuestRuntimeReleasesResourceGovernorSlot(t *testing.T) {
	runtime := newResearchQuestRuntimeForTest(t, stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	}))
	runtime.gateway.ResourceGovernor = resilience.NewResourceGovernorWithInterval(100, 1, 0)
	runtime.gateway.ResourceGovernor.SetStatus("healthy")

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID: "release-user",
		Query:  "resource slot release",
	})
	require.NoError(t, err)
	require.Equal(t, QuestStatusComplete, quest.Status)
	assert.Equal(t, 0, runtime.gateway.ResourceGovernor.ActiveTasksForTest())
}

func TestResearchQuestRuntimeSeedsReplayContextFromLongTermMemory(t *testing.T) {
	store := newTestMemoryStore()
	store.longTerm["memory-user"] = []MemoryEntry{
		{
			ID:        "ltm-1",
			Type:      "verified_claim",
			Content:   "Sleep consolidation improves declarative memory performance.",
			CreatedAt: time.Now().Add(-time.Hour).UnixMilli(),
		},
	}
	runtime := newResearchQuestRuntimeWithMemoryStore(t, store, stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:        "promoted",
		Promoted:      true,
		VerifiedCount: 1,
	}))

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "memory-user",
		Query:       "sleep memory consolidation",
		QualityMode: "quality",
	})
	require.NoError(t, err)

	replayContext := asMap(quest.Artifacts["replayContext"])
	findings := firstArtifactMaps(replayContext["findings"])
	require.NotEmpty(t, findings)
	assert.Contains(t, AsOptionalString(findings[0]["content"]), "declarative memory")
	assert.NotEmpty(t, quest.Memory.LongTermVector)

	storeEntries, err := store.LoadLongTermVector(context.Background(), "memory-user")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(storeEntries), 2)
}

func TestResearchQuestRuntimeResumeRecoversBlockedCitationVerdict(t *testing.T) {
	var citationCalls int
	hooks := stubQuestHooks(testQuestSources(1), CitationVerdict{
		Status:         "blocked",
		Promoted:       false,
		AmbiguousCount: 1,
		BlockingIssues: []string{"missing canonical confirmation"},
	})
	hooks.CitationFn = func(_ context.Context, _ *ResearchQuest, papers []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error) {
		citationCalls++
		verdict := CitationVerdict{
			Status:         "blocked",
			Promoted:       false,
			AmbiguousCount: 1,
			BlockingIssues: []string{"missing canonical confirmation"},
		}
		if citationCalls > 1 {
			verdict = CitationVerdict{
				Status:        "promoted",
				Promoted:      true,
				VerifiedCount: len(papers),
			}
		}
		authorities := make([]CitationAuthorityRecord, 0, len(papers))
		for _, paper := range papers {
			authorities = append(authorities, CitationAuthorityRecord{
				Authority:        "crossref",
				CanonicalID:      paper.DOI,
				DOI:              paper.DOI,
				Title:            paper.Title,
				Resolved:         verdict.Promoted,
				Verified:         verdict.Promoted,
				AgreementCount:   map[bool]int{true: 2, false: 1}[verdict.Promoted],
				ResolutionEngine: "test",
			})
		}
		return authorities, verdict, map[string]any{
			"promotionGate": map[string]any{
				"promoted":       verdict.Promoted,
				"blockingIssues": verdict.BlockingIssues,
			},
		}, nil
	}

	runtime := newResearchQuestRuntimeForTest(t, hooks)
	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "recover-user",
		Query:       "citation recovery path",
		QualityMode: "quality",
	})
	require.NoError(t, err)
	assert.Equal(t, QuestStatusBlocked, quest.Status)

	resumed, err := runtime.ResumeQuest(context.Background(), quest.QuestID, ResearchQuestRequest{ForceResume: true})
	require.NoError(t, err)
	assert.Equal(t, QuestStatusComplete, resumed.Status)
	assert.True(t, resumed.CitationVerdict.Promoted)
	assert.GreaterOrEqual(t, citationCalls, 2)
}

func TestResearchQuestRuntimeDefaultCitationVerifierCompletesLocally(t *testing.T) {
	retrieveCalls := 0
	hooks := stubQuestHooks(testQuestSources(1), CitationVerdict{})
	runtime := newResearchQuestRuntimeWithDefaultCitationBroker(t, hooks, &retrieveCalls)

	quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
		UserID:      "outage-user",
		Query:       "citation broker outage recovery",
		QualityMode: "quality",
	})
	require.NoError(t, err)
	assert.Equal(t, QuestStageComplete, quest.CurrentStage)
	assert.Equal(t, QuestStatusComplete, quest.Status)
	assert.Equal(t, 1, retrieveCalls)
	assert.True(t, quest.CitationVerdict.Promoted)
	assert.GreaterOrEqual(t, retrieveCalls, 1)
	if assert.NotEmpty(t, quest.CitationAuthorities) {
		assert.Equal(t, "go-local", quest.CitationAuthorities[0].ResolutionEngine)
	}
}

func TestResearchQuestRuntimeConcurrentDefaultCitationVerifierCompletesLocally(t *testing.T) {
	runtimes := make([]*ResearchQuestRuntime, 0, 4)
	quests := make([]*ResearchQuest, 0, 4)
	for idx := 0; idx < 4; idx++ {
		runtime := newResearchQuestRuntimeWithDefaultCitationBroker(t, stubQuestHooks(testQuestSources(1), CitationVerdict{}), nil)
		quest, err := runtime.StartQuest(context.Background(), ResearchQuestRequest{
			UserID:      fmt.Sprintf("concurrent-user-%d", idx),
			Query:       fmt.Sprintf("concurrent citation outage %d", idx),
			QualityMode: "quality",
		})
		require.NoError(t, err)
		require.Equal(t, QuestStageComplete, quest.CurrentStage)
		require.Equal(t, QuestStatusComplete, quest.Status)
		require.True(t, quest.CitationVerdict.Promoted)
		runtimes = append(runtimes, runtime)
		quests = append(quests, quest)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(runtimes))
	for idx := range runtimes {
		wg.Add(1)
		go func(runtime *ResearchQuestRuntime, questID string) {
			defer wg.Done()
			resumed, err := runtime.ResumeQuest(context.Background(), questID, ResearchQuestRequest{ForceResume: true})
			if err != nil {
				errCh <- err
				return
			}
			if resumed.Status != QuestStatusComplete || !resumed.CitationVerdict.Promoted {
				errCh <- fmt.Errorf("quest %s did not remain complete", questID)
			}
		}(runtimes[idx], quests[idx].QuestID)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}
}

func newResearchQuestRuntimeForTest(t *testing.T, hooks ResearchQuestHooks) *ResearchQuestRuntime {
	t.Helper()

	return newResearchQuestRuntimeWithMemoryStore(t, newTestMemoryStore(), hooks)
}

func newResearchQuestRuntimeWithMemoryStore(t *testing.T, store MemoryStore, hooks ResearchQuestHooks) *ResearchQuestRuntime {
	t.Helper()

	t.Setenv("WISDEV_STATE_DIR", t.TempDir())
	stateStore := NewRuntimeStateStore(nil, nil)
	journal := NewRuntimeJournal(nil)
	gateway := &AgentGateway{
		StateStore:    stateStore,
		Journal:       journal,
		Checkpoints:   NewInMemoryCheckpointStore(),
		CheckpointTTL: time.Hour,
		MemoryStore:   store,
	}
	gateway.Memory = NewMemoryConsolidator(nil, store)

	runtime := NewResearchQuestRuntime(gateway)
	runtime.dossierFn = nil
	return runtime.ApplyHooks(hooks)
}

func newResearchQuestRuntimeWithDefaultCitationBroker(t *testing.T, hooks ResearchQuestHooks, retrieveCalls *int) *ResearchQuestRuntime {
	t.Helper()

	runtime := newResearchQuestRuntimeWithMemoryStore(t, newTestMemoryStore(), ResearchQuestHooks{})
	if hooks.DecomposeFn != nil {
		runtime.decomposeFn = hooks.DecomposeFn
	}
	if hooks.RetrieveFn != nil {
		runtime.retrieveFn = func(ctx context.Context, quest *ResearchQuest) ([]Source, map[string]any, error) {
			if retrieveCalls != nil {
				*retrieveCalls = *retrieveCalls + 1
			}
			return hooks.RetrieveFn(ctx, quest)
		}
	}
	if hooks.HypothesisFn != nil {
		runtime.hypothesisFn = hooks.HypothesisFn
	}
	if hooks.BranchFn != nil {
		runtime.branchFn = hooks.BranchFn
	}
	if hooks.DraftFn != nil {
		runtime.draftFn = hooks.DraftFn
	}
	if hooks.CritiqueFn != nil {
		runtime.critiqueFn = hooks.CritiqueFn
	}
	if hooks.DossierFn != nil {
		runtime.dossierFn = hooks.DossierFn
	}
	runtime.citationFn = runtime.defaultCitationVerdict
	return runtime
}

func stubQuestHooks(papers []Source, verdict CitationVerdict) ResearchQuestHooks {
	return ResearchQuestHooks{
		RetrieveFn: func(_ context.Context, _ *ResearchQuest) ([]Source, map[string]any, error) {
			return papers, map[string]any{
				"count":   len(papers),
				"traceId": "quest-test-trace",
			}, nil
		},
		HypothesisFn: func(_ context.Context, quest *ResearchQuest, _ []Source) ([]Hypothesis, error) {
			return []Hypothesis{
				{
					ID:    stableWisDevID("hypothesis", quest.Query, "test"),
					Query: quest.Query,
					Claim: "Supported by the current evidence slice.",
				},
			}, nil
		},
		BranchFn: func(_ context.Context, _ *ResearchQuest, papers []Source, _ []Hypothesis) (branchReasoningOutcome, error) {
			return branchReasoningOutcome{
				AcceptedClaims: []EvidenceFinding{
					{
						ID:         "finding-1",
						Claim:      "Supported by the current evidence slice.",
						Snippet:    "Supported by the current evidence slice.",
						PaperTitle: papers[0].Title,
						SourceID:   firstNonEmpty(papers[0].DOI, papers[0].ID),
						Confidence: 0.91,
						Status:     "accepted",
					},
				},
				Payload: map[string]any{
					"finalSummary": "quest summary",
				},
			}, nil
		},
		CitationFn: func(_ context.Context, _ *ResearchQuest, papers []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error) {
			authorities := make([]CitationAuthorityRecord, 0, len(papers))
			for _, paper := range papers {
				authorities = append(authorities, CitationAuthorityRecord{
					Authority:        firstNonEmpty(strings.TrimSpace(paper.Source), "crossref"),
					CanonicalID:      firstNonEmpty(strings.TrimSpace(paper.DOI), strings.TrimSpace(paper.ID)),
					DOI:              strings.TrimSpace(paper.DOI),
					Title:            strings.TrimSpace(paper.Title),
					Resolved:         verdict.Promoted,
					Verified:         verdict.Promoted,
					AgreementCount:   2,
					ResolutionEngine: "test",
				})
			}
			return authorities, verdict, map[string]any{
				"promotionGate": map[string]any{
					"promoted":       verdict.Promoted,
					"blockingIssues": verdict.BlockingIssues,
				},
			}, nil
		},
		DraftFn: func(_ context.Context, _ *ResearchQuest, _ []Source, _ map[string]any) (string, error) {
			return "quest draft", nil
		},
		CritiqueFn: func(_ context.Context, _ *ResearchQuest, _ []Source, _ CitationVerdict) (string, error) {
			return "quest critique", nil
		},
	}
}

func testQuestSources(count int) []Source {
	sources := make([]Source, 0, count)
	for i := 0; i < count; i++ {
		sources = append(sources, Source{
			ID:      fmt.Sprintf("paper-%d", i+1),
			Title:   fmt.Sprintf("Paper %d", i+1),
			Summary: fmt.Sprintf("Summary %d", i+1),
			DOI:     fmt.Sprintf("10.1000/test-%d", i+1),
			Source:  "crossref",
			Score:   0.9,
			Year:    2025,
		})
	}
	return sources
}

func questAgentRoles(assignments []AgentAssignment) []string {
	roles := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		roles = append(roles, assignment.Role)
	}
	return roles
}
