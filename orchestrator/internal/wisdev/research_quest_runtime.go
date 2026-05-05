package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

const allowGoCitationFallbackEnv = "WISDEV_GO_CITATION_LOCAL"
const questCanonicalRuntimeArtifactKey = "canonicalRuntime"

type CitationBrokerGateConfig struct {
	Mode            string   `json:"mode"`
	AllowGoFallback bool     `json:"allowGoFallback"`
	Warnings        []string `json:"warnings,omitempty"`
}

func (cfg CitationBrokerGateConfig) Map() map[string]any {
	return map[string]any{
		"mode":            cfg.Mode,
		"allowGoFallback": cfg.AllowGoFallback,
		"warnings":        stringSliceToAny(cfg.Warnings),
	}
}

var questRunRetrievePapers = func(ctx context.Context, rdb redis.UniversalClient, query string, opts SearchOptions) ([]Source, map[string]any, error) {
	return runRetrievePapers(ctx, rdb, query, opts)
}

var questRunUnifiedResearchLoop = func(ctx context.Context, runtime *UnifiedResearchRuntime, req LoopRequest) (*UnifiedResearchResult, error) {
	return runtime.RunLoop(ctx, req, ResearchExecutionPlaneQuest, nil)
}

func NewResearchQuestRuntime(gateway *AgentGateway) *ResearchQuestRuntime {
	var stateStore *RuntimeStateStore
	var checkpoints CheckpointStore
	var memoryStore MemoryStore
	var memory *MemoryConsolidator
	if gateway != nil {
		stateStore = gateway.StateStore
		checkpoints = gateway.Checkpoints
		memoryStore = gateway.MemoryStore
		memory = gateway.Memory
	}
	if stateStore == nil {
		stateStore = NewRuntimeStateStore(nil, nil)
	}
	if checkpoints == nil {
		checkpoints = NewInMemoryCheckpointStore()
	}
	if memoryStore == nil {
		memoryStore = &NoopMemoryStore{}
	}
	if memory == nil {
		memory = NewMemoryConsolidator(nil, memoryStore)
	}

	rt := &ResearchQuestRuntime{
		gateway:       gateway,
		stateStore:    stateStore,
		checkpoints:   checkpoints,
		memoryStore:   memoryStore,
		memory:        memory,
		workingTTL:    DefaultWorkingTTL,
		longTermTTL:   DefaultLongTermTTL,
		checkpointTTL: time.Hour,
	}
	rt.decomposeFn = rt.defaultDecompose
	rt.retrieveFn = rt.defaultRetrieve
	rt.hypothesisFn = rt.defaultHypotheses
	rt.branchFn = rt.defaultBranchReasoning
	rt.citationFn = rt.defaultCitationVerdict
	rt.draftFn = defaultQuestDraft
	rt.critiqueFn = defaultQuestCritique
	rt.dossierFn = rt.defaultEvidenceDossiers
	return rt
}

func (rt *ResearchQuestRuntime) ApplyHooks(hooks ResearchQuestHooks) *ResearchQuestRuntime {
	if rt == nil {
		return nil
	}
	if hooks.DecomposeFn != nil {
		rt.decomposeFn = hooks.DecomposeFn
	}
	if hooks.RetrieveFn != nil {
		rt.retrieveFn = hooks.RetrieveFn
	}
	if hooks.HypothesisFn != nil {
		rt.hypothesisFn = hooks.HypothesisFn
	}
	if hooks.BranchFn != nil {
		rt.branchFn = hooks.BranchFn
	}
	if hooks.CitationFn != nil {
		rt.citationFn = hooks.CitationFn
	}
	if hooks.DraftFn != nil {
		rt.draftFn = hooks.DraftFn
	}
	if hooks.CritiqueFn != nil {
		rt.critiqueFn = hooks.CritiqueFn
	}
	if hooks.DossierFn != nil {
		rt.dossierFn = hooks.DossierFn
	}
	return rt
}

func (rt *ResearchQuestRuntime) StartQuest(ctx context.Context, req ResearchQuestRequest) (*ResearchQuest, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	mode := NormalizeWisDevMode(req.Mode)
	if mode == "" {
		mode = WisDevModeGuided
	}
	qualityMode := NormalizeResearchQualityMode(req.QualityMode)
	if qualityMode == "" {
		qualityMode = "balanced"
	}

	questID := stableWisDevID("quest", req.UserID, query, fmt.Sprintf("%d", NowMillis()))
	profile := BuildResearchExecutionProfile(ctx, query, string(mode), qualityMode, false, req.MaxIterations)
	domain := strings.TrimSpace(req.Domain)
	quest := &ResearchQuest{
		SessionID:              questID,
		QuestID:                questID,
		UserID:                 strings.TrimSpace(req.UserID),
		Query:                  query,
		Domain:                 domain,
		DetectedDomain:         domain,
		Mode:                   mode,
		ServiceTier:            profile.ServiceTier,
		QualityMode:            qualityMode,
		Status:                 QuestStatusRunning,
		CurrentStage:           QuestStageInit,
		PersistUserPreferences: req.PersistUserPreferences,
		DecisionModelTier:      profile.PrimaryModelTier,
		ExecutionProfile:       profile,
		Artifacts:              map[string]any{},
		Events:                 []QuestEvent{},
		Memory: QuestMemoryState{
			PromotionRules: map[string]bool{
				"userPreferenceOptIn": req.PersistUserPreferences,
			},
		},
		CreatedAt: NowMillis(),
		UpdatedAt: NowMillis(),
	}

	if err := rt.seedReplayContext(ctx, quest); err != nil {
		rt.appendEvent(quest, "memory_warning", QuestStageInit, "Replay context unavailable", map[string]any{"error": err.Error()})
	}
	if req.PersistUserPreferences {
		quest.Memory.UserPersonalized = append(quest.Memory.UserPersonalized, MemoryEntry{
			ID:        stableWisDevID("pref", quest.UserID, quest.Query),
			Type:      "user_preference",
			Content:   fmt.Sprintf("quality:%s", qualityMode),
			CreatedAt: NowMillis(),
		})
	}
	rt.appendEvent(quest, "quest_started", QuestStageInit, "Research quest started", map[string]any{
		"mode":        quest.Mode,
		"qualityMode": quest.QualityMode,
	})
	if err := rt.persistQuest(ctx, quest); err != nil {
		return nil, err
	}

	return rt.continueQuest(ctx, quest, false)
}

func (rt *ResearchQuestRuntime) ResumeQuest(ctx context.Context, questID string, req ResearchQuestRequest) (*ResearchQuest, error) {
	quest, err := rt.LoadQuest(ctx, questID)
	if err != nil {
		return nil, err
	}
	if quest == nil {
		return nil, fmt.Errorf("quest not found")
	}
	if req.UserID != "" {
		quest.UserID = strings.TrimSpace(req.UserID)
	}
	return rt.continueQuest(ctx, quest, req.ForceResume)
}

func (rt *ResearchQuestRuntime) LoadQuest(ctx context.Context, questID string) (*ResearchQuest, error) {
	if rt == nil {
		return nil, fmt.Errorf("quest runtime unavailable")
	}
	if rt.stateStore != nil {
		payload, err := rt.stateStore.LoadQuestState(questID)
		if err == nil && payload != nil {
			return questFromMap(ctx, payload)
		}
	}
	return rt.loadQuestCheckpoint(ctx, questID)
}

func (rt *ResearchQuestRuntime) GetEvents(ctx context.Context, questID string) ([]QuestEvent, error) {
	quest, err := rt.LoadQuest(ctx, questID)
	if err != nil {
		return nil, err
	}
	if quest == nil {
		return nil, fmt.Errorf("quest not found")
	}
	return append([]QuestEvent(nil), quest.Events...), nil
}

func (rt *ResearchQuestRuntime) GetArtifacts(ctx context.Context, questID string) (map[string]any, error) {
	quest, err := rt.LoadQuest(ctx, questID)
	if err != nil {
		return nil, err
	}
	if quest == nil {
		return nil, fmt.Errorf("quest not found")
	}
	return cloneAnyMap(quest.Artifacts), nil
}

func (rt *ResearchQuestRuntime) loadQuestCheckpoint(ctx context.Context, questID string) (*ResearchQuest, error) {
	if rt == nil || rt.checkpoints == nil {
		return nil, fmt.Errorf("quest checkpoint unavailable")
	}
	payload, err := rt.checkpoints.Load(ctx, strings.TrimSpace(questID))
	if err != nil {
		return nil, err
	}
	var quest ResearchQuest
	if err := json.Unmarshal(payload, &quest); err != nil {
		return nil, err
	}
	ensureQuestDefaults(&quest)
	return &quest, nil
}

func (rt *ResearchQuestRuntime) continueQuest(ctx context.Context, quest *ResearchQuest, forceResume bool) (*ResearchQuest, error) {
	if quest == nil {
		return nil, fmt.Errorf("quest is required")
	}
	ensureQuestDefaultsWithContext(ctx, quest)

	if quest.Status == QuestStatusComplete && quest.CurrentStage == QuestStageComplete {
		return quest, nil
	}
	if quest.Status == QuestStatusBlocked && !forceResume {
		return quest, nil
	}
	if rt != nil && rt.gateway != nil && rt.gateway.ResourceGovernor != nil {
		release, err := rt.gateway.ResourceGovernor.TryAcquire(ctx, "wisdev_research_loop")
		if err != nil {
			rt.appendEvent(quest, "resource_rejected", quest.CurrentStage, "Research quest gracefully rejected by resource governor", map[string]any{"error": err.Error(), "kind": "system_overload"})
			quest.Status = QuestStatusBlocked
			quest.BlockingIssues = dedupeTrimmedStrings(append(quest.BlockingIssues, "system_overload"))
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		defer release()
	}
	if forceResume && quest.Status == QuestStatusBlocked {
		quest.Status = QuestStatusRunning
		quest.CurrentStage = QuestStageRetrieve
		quest.BlockingIssues = nil
		quest.FinalAnswer = ""
	}

	switch quest.CurrentStage {
	case "", QuestStageInit:
		quest.CurrentStage = QuestStageInit
	}

	if quest.CurrentStage == QuestStageInit {
		decomposition, err := rt.decomposeFn(ctx, quest)
		if err != nil {
			rt.appendEvent(quest, "decomposition_failed", QuestStageInit, "Research decomposition failed", map[string]any{"error": err.Error()})
			quest.Status = QuestStatusFailed
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.Artifacts["decomposition"] = cloneAnyMap(decomposition)
		if quest.ResearchScratchpad == nil {
			quest.ResearchScratchpad = map[string]string{}
		}
		quest.ResearchScratchpad["decomposition"] = summarizeQuestDecomposition(decomposition)
		quest.CurrentStage = QuestStageRetrieve
		rt.appendEvent(quest, "decomposition_complete", QuestStageInit, "Research decomposition captured", map[string]any{
			"taskCount": len(firstArtifactMaps(decomposition["tasks"])),
		})
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageRetrieve {
		papers, payload, err := rt.retrieve(ctx, quest)
		if err != nil {
			rt.appendEvent(quest, "retrieval_failed", QuestStageRetrieve, "Paper retrieval failed", map[string]any{"error": err.Error()})
			quest.Status = QuestStatusFailed
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.Papers = mergeQuestSources(quest.Papers, papers)
		quest.RetrievedCount = len(quest.Papers)
		retrievalPayload := cloneAnyMap(payload)
		if retrievalPayload == nil {
			retrievalPayload = map[string]any{}
		}
		retrievalPayload["retrievedCount"] = quest.RetrievedCount
		if followUps := decodeQuestFollowUpQueries(quest.ResearchScratchpad[questFollowUpQueriesScratchKey]); len(followUps) > 0 {
			retrievalPayload["followUpQueries"] = stringSliceToAny(followUps)
			delete(quest.ResearchScratchpad, questFollowUpQueriesScratchKey)
		}
		quest.Artifacts["retrieval"] = retrievalPayload
		if strings.EqualFold(AsOptionalString(retrievalPayload["engine"]), "unified_research_runtime") {
			quest.Artifacts[questCanonicalRuntimeArtifactKey] = cloneAnyMap(retrievalPayload)
			if quest.ResearchScratchpad == nil {
				quest.ResearchScratchpad = map[string]string{}
			}
			if finalAnswer := strings.TrimSpace(AsOptionalString(retrievalPayload["finalAnswer"])); finalAnswer != "" {
				quest.ResearchScratchpad["canonicalFinalAnswer"] = finalAnswer
			}
		}
		quest.CurrentStage = QuestStageHypotheses
		if quest.RetrievedCount >= 50 {
			escalateQuestModel(quest, "large retrieval breadth")
		}
		retrievalEvent := map[string]any{"count": len(quest.Papers)}
		if engine := strings.TrimSpace(AsOptionalString(retrievalPayload["engine"])); engine != "" {
			retrievalEvent["engine"] = engine
		}
		rt.appendEvent(quest, "retrieval_complete", QuestStageRetrieve, "Retrieved evidence papers", retrievalEvent)
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageHypotheses {
		hypotheses, err := rt.generateHypotheses(ctx, quest)
		if err != nil {
			rt.appendEvent(quest, "hypotheses_failed", QuestStageHypotheses, "Hypothesis generation failed", map[string]any{"error": err.Error()})
			quest.Status = QuestStatusFailed
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.Hypotheses = cloneQuestHypotheses(hypotheses)
		quest.Artifacts["hypotheses"] = map[string]any{
			"items":  valueToAny(hypotheses),
			"count":  len(hypotheses),
			"staged": true,
		}
		if quest.ResearchScratchpad == nil {
			quest.ResearchScratchpad = map[string]string{}
		}
		quest.ResearchScratchpad["hypotheses"] = summarizeQuestHypotheses(hypotheses)
		quest.CurrentStage = QuestStageReason
		rt.appendEvent(quest, "hypotheses_complete", QuestStageHypotheses, "Research hypotheses generated", map[string]any{"count": len(hypotheses)})
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageReason {
		hypotheses := cloneQuestHypothesisValues(quest.Hypotheses)
		outcome, err := rt.reasonAboutEvidence(ctx, quest, hypotheses)
		if err != nil {
			rt.appendEvent(quest, "reasoning_failed", QuestStageReason, "Evidence reasoning failed", map[string]any{"error": err.Error()})
			quest.Status = QuestStatusFailed
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.AcceptedClaims = append([]EvidenceFinding(nil), outcome.AcceptedClaims...)
		quest.RejectedBranches = append([]QuestBranchRecord(nil), outcome.RejectedBranches...)
		quest.Artifacts["routing"] = map[string]any{
			"methods":            stringSliceToAny(extractQuestMethods(quest.Papers)),
			"heavyModelRequired": quest.HeavyModelRequired,
			"agentRoles":         agentAssignmentsToRoleSlice(quest.AgentAssignments),
		}
		if len(outcome.Payload) > 0 {
			quest.Artifacts["reasoning"] = cloneAnyMap(outcome.Payload)
		}
		if rt != nil && rt.dossierFn != nil {
			if dossiers, err := rt.dossierFn(ctx, quest, quest.Papers); err != nil {
				rt.appendEvent(quest, "dossier_warning", QuestStageReason, "Evidence dossier generation degraded", map[string]any{"error": err.Error()})
			} else if len(dossiers) > 0 {
				quest.EvidenceDossiers = dossiers
				quest.Artifacts["evidenceDossiers"] = valueToAny(dossiers)
			}
		}
		if quest.ResearchScratchpad == nil {
			quest.ResearchScratchpad = map[string]string{}
		}
		quest.ResearchScratchpad["reasoning"] = summarizeQuestReasoning(quest.AcceptedClaims, quest.RejectedBranches)
		quest.ResearchScratchpad["coverage_gaps"] = strings.Join(collectQuestDossierGaps(quest.EvidenceDossiers), "; ")
		quest.CoverageLedger = buildQuestCoverageLedger(quest, quest.Papers, quest.CitationVerdict, "")
		quest.Artifacts["acceptedClaims"] = valueToAny(quest.AcceptedClaims)
		quest.Artifacts["coverageLedger"] = coverageLedgerEntriesToAny(quest.CoverageLedger)
		quest.Artifacts["rejectedBranches"] = valueToAny(quest.RejectedBranches)
		quest.CurrentStage = QuestStageVerify
		rt.appendEvent(quest, "reasoning_complete", QuestStageReason, "Reasoning branches evaluated", map[string]any{"acceptedClaims": len(quest.AcceptedClaims)})
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageVerify {
		authorities, verdict, payload, err := rt.verifyCitations(ctx, quest)
		if err != nil {
			quest.Status = QuestStatusRunning
			quest.CurrentStage = QuestStageVerify
			rt.appendEvent(quest, "citation_verification_failed", QuestStageVerify, "Citation verification failed", map[string]any{"error": err.Error()})
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.CitationAuthorities = append([]CitationAuthorityRecord(nil), authorities...)
		quest.CitationVerdict = verdict
		quest.BlockingIssues = dedupeTrimmedStrings(append([]string(nil), verdict.BlockingIssues...))
		quest.Artifacts["citationVerdict"] = cloneAnyMap(payload)
		if len(authorities) > 0 {
			quest.Artifacts["citationAuthorities"] = authorityRecordsToAny(authorities)
		}
		quest.CoverageLedger = buildQuestCoverageLedger(quest, quest.Papers, verdict, "")
		quest.Artifacts["coverageLedger"] = coverageLedgerEntriesToAny(quest.CoverageLedger)
		if !verdict.Promoted {
			escalateQuestModel(quest, "citation gate blocked publication")
			quest.Status = QuestStatusBlocked
			quest.FinalAnswer = buildCitationBlockedAnswer(verdict)
			rt.appendEvent(quest, "citation_blocked", QuestStageVerify, "Citation gate blocked publication", map[string]any{"blockingIssues": stringSliceToAny(quest.BlockingIssues)})
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.CurrentStage = QuestStageDraft
		rt.appendEvent(quest, "citation_verified", QuestStageVerify, "Citation verification promoted publication", map[string]any{"verifiedCount": verdict.VerifiedCount})
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageDraft {
		draft, err := rt.draftQuest(ctx, quest)
		if err != nil {
			quest.Status = QuestStatusRunning
			quest.CurrentStage = QuestStageDraft
			rt.appendEvent(quest, "draft_failed", QuestStageDraft, "Draft generation failed", map[string]any{"error": err.Error()})
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.FinalAnswer = strings.TrimSpace(draft)
		quest.Artifacts["draft"] = map[string]any{"text": draft}
		quest.CurrentStage = QuestStageCritique
		rt.appendEvent(quest, "draft_complete", QuestStageDraft, "Draft generated", nil)
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	if quest.CurrentStage == QuestStageCritique {
		critique, err := rt.critiqueQuest(ctx, quest)
		if err != nil {
			quest.Status = QuestStatusRunning
			rt.appendEvent(quest, "critique_failed", QuestStageCritique, "Critique generation failed", map[string]any{"error": err.Error()})
			if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
				return nil, persistErr
			}
			return quest, nil
		}
		quest.CoverageLedger = buildQuestCoverageLedger(quest, quest.Papers, quest.CitationVerdict, critique)
		quest.Artifacts["coverageLedger"] = coverageLedgerEntriesToAny(quest.CoverageLedger)
		if questCritiqueRequestedFollowUp(quest.CoverageLedger) && quest.CurrentIteration < maxQuestCritiqueRevisions {
			if followUps := buildFollowUpQueriesFromLedger(quest.Query, quest.CoverageLedger, 3); len(followUps) > 0 {
				quest.CurrentIteration++
				quest.Status = QuestStatusRunning
				quest.CurrentStage = QuestStageRetrieve
				quest.FinalAnswer = ""
				quest.ReviewerNotes = dedupeTrimmedStrings(append(append([]string(nil), quest.ReviewerNotes...), strings.TrimSpace(critique)))
				quest.ResearchScratchpad[questFollowUpQueriesScratchKey] = encodeQuestFollowUpQueries(followUps)
				quest.Artifacts["critique"] = map[string]any{
					"text":            critique,
					"reopened":        true,
					"followUpQueries": stringSliceToAny(followUps),
					"iteration":       quest.CurrentIteration,
				}
				rt.appendEvent(quest, "critique_reopened", QuestStageCritique, "Critique requested more evidence before completion", map[string]any{
					"followUpQueries": stringSliceToAny(followUps),
					"iteration":       quest.CurrentIteration,
				})
				if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
					return nil, persistErr
				}
				return rt.continueQuest(ctx, quest, false)
			}
		}
		quest.Artifacts["critique"] = map[string]any{"text": critique, "reopened": false}
		quest.Status = QuestStatusComplete
		quest.CurrentStage = QuestStageComplete
		if strings.TrimSpace(quest.FinalAnswer) == "" {
			quest.FinalAnswer = critique
		}
		if err := rt.persistQuestMemory(ctx, quest); err != nil {
			rt.appendEvent(quest, "memory_warning", QuestStageComplete, "Quest memory persistence degraded", map[string]any{"error": err.Error()})
		}
		rt.appendEvent(quest, "quest_complete", QuestStageComplete, "Research quest completed", nil)
		if persistErr := rt.persistQuest(ctx, quest); persistErr != nil {
			return nil, persistErr
		}
	}

	return quest, nil
}

func (rt *ResearchQuestRuntime) retrieve(ctx context.Context, quest *ResearchQuest) ([]Source, map[string]any, error) {
	if rt != nil && rt.retrieveFn != nil {
		return rt.retrieveFn(ctx, quest)
	}
	return rt.defaultRetrieve(ctx, quest)
}

func (rt *ResearchQuestRuntime) generateHypotheses(ctx context.Context, quest *ResearchQuest) ([]Hypothesis, error) {
	if rt != nil && rt.hypothesisFn != nil {
		return rt.hypothesisFn(ctx, quest, quest.Papers)
	}
	return defaultQuestHypotheses(ctx, quest, quest.Papers)
}

func (rt *ResearchQuestRuntime) reasonAboutEvidence(ctx context.Context, quest *ResearchQuest, hypotheses []Hypothesis) (branchReasoningOutcome, error) {
	if rt != nil && rt.branchFn != nil {
		return rt.branchFn(ctx, quest, quest.Papers, hypotheses)
	}
	return defaultQuestBranchReasoning(ctx, quest, quest.Papers, hypotheses)
}

func (rt *ResearchQuestRuntime) verifyCitations(ctx context.Context, quest *ResearchQuest) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error) {
	if rt != nil && rt.citationFn != nil {
		return rt.citationFn(ctx, quest, quest.Papers)
	}
	return rt.defaultCitationVerdict(ctx, quest, quest.Papers)
}

func (rt *ResearchQuestRuntime) draftQuest(ctx context.Context, quest *ResearchQuest) (string, error) {
	reasoning := asMap(quest.Artifacts["reasoning"])
	if rt != nil && rt.draftFn != nil {
		return rt.draftFn(ctx, quest, quest.Papers, reasoning)
	}
	return defaultQuestDraft(ctx, quest, quest.Papers, reasoning)
}

func (rt *ResearchQuestRuntime) critiqueQuest(ctx context.Context, quest *ResearchQuest) (string, error) {
	if rt != nil && rt.critiqueFn != nil {
		return rt.critiqueFn(ctx, quest, quest.Papers, quest.CitationVerdict)
	}
	return defaultQuestCritique(ctx, quest, quest.Papers, quest.CitationVerdict)
}

func (rt *ResearchQuestRuntime) defaultRetrieve(ctx context.Context, quest *ResearchQuest) ([]Source, map[string]any, error) {
	query := strings.TrimSpace(quest.Query)
	if query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	followUpQueries := decodeQuestFollowUpQueries(quest.ResearchScratchpad[questFollowUpQueriesScratchKey])
	if len(followUpQueries) > 0 {
		if len(followUpQueries) > 2 {
			followUpQueries = followUpQueries[:2]
		}
	}

	runtime := rt.canonicalResearchRuntime()
	if runtime == nil {
		return nil, nil, fmt.Errorf("UnifiedResearchRuntime not initialised for research quest retrieval")
	}
	runtimeResult, err := questRunUnifiedResearchLoop(ctx, runtime, LoopRequest{
		Query:           query,
		SeedQueries:     followUpQueries,
		Domain:          strings.TrimSpace(firstNonEmpty(quest.Domain, quest.DetectedDomain)),
		ProjectID:       firstNonEmpty(quest.SessionID, quest.QuestID, NewTraceID()),
		MaxIterations:   quest.ExecutionProfile.MaxIterations,
		MaxSearchTerms:  quest.ExecutionProfile.SearchBudget.MaxSearchTerms,
		HitsPerSearch:   quest.ExecutionProfile.SearchBudget.HitsPerSearch,
		MaxUniquePapers: quest.ExecutionProfile.SearchBudget.MaxUniquePapers,
		AllocatedTokens: quest.ExecutionProfile.AllocatedTokens,
		Mode:            string(quest.ExecutionProfile.Mode),
	})
	if err != nil {
		return nil, nil, err
	}
	if runtimeResult == nil || runtimeResult.LoopResult == nil {
		return nil, nil, fmt.Errorf("UnifiedResearchRuntime returned no quest retrieval result")
	}
	papers := searchPapersToQuestSources(runtimeResult.LoopResult.Papers)
	return papers, buildQuestCanonicalRuntimePayload(runtimeResult, query, followUpQueries), nil
}

func (rt *ResearchQuestRuntime) defaultCitationVerdict(_ context.Context, _ *ResearchQuest, papers []Source) ([]CitationAuthorityRecord, CitationVerdict, map[string]any, error) {
	result, err := VerifyCitationRecordsSecurely(papers)
	if err != nil {
		return nil, CitationVerdict{}, nil, err
	}

	canonical := citationMapsToTyped(firstArtifactMaps(result["verifiedRecords"]))
	authorities := authorityRecordsFromCanonical(canonical)
	verdict := CitationVerdict{
		Status:              map[bool]string{true: "promoted", false: "blocked"}[toBool(result["promotionEligible"])],
		Promoted:            toBool(result["promotionEligible"]),
		VerifiedCount:       toInt(result["verifiedCount"]),
		AmbiguousCount:      toInt(result["ambiguousCount"]),
		RejectedCount:       toInt(result["rejectedCount"]),
		BlockingIssues:      toStringSlice(result["blockingIssues"]),
		PromotionGate:       asMap(result["promotionGate"]),
		RequiresHumanReview: len(toStringSlice(result["blockingIssues"])) > 0,
	}
	if verdict.PromotionGate == nil {
		verdict.PromotionGate = map[string]any{
			"promoted":       verdict.Promoted,
			"blockingIssues": stringSliceToAny(verdict.BlockingIssues),
		}
	}

	payload := cloneAnyMap(result)
	payload["promotionGate"] = cloneAnyMap(verdict.PromotionGate)
	return authorities, verdict, payload, nil
}

func (rt *ResearchQuestRuntime) persistQuest(ctx context.Context, quest *ResearchQuest) error {
	if quest == nil {
		return nil
	}
	quest.UpdatedAt = NowMillis()
	payload, err := questToMap(quest)
	if err != nil {
		return err
	}
	if rt != nil && rt.stateStore != nil {
		if err := rt.stateStore.SaveQuestState(quest.QuestID, payload); err != nil {
			return err
		}
	}
	if rt != nil && rt.checkpoints != nil {
		body, err := json.Marshal(quest)
		if err != nil {
			return err
		}
		if err := rt.checkpoints.Save(ctx, quest.QuestID, body, rt.checkpointTTL); err != nil {
			return err
		}
	}
	return nil
}

func (rt *ResearchQuestRuntime) seedReplayContext(ctx context.Context, quest *ResearchQuest) error {
	if rt == nil || quest == nil {
		return nil
	}
	entries := []MemoryEntry{}
	if rt.gateway != nil && rt.gateway.Memory != nil {
		relevant, err := rt.gateway.Memory.GetRelevantFindingEntries(ctx, quest.UserID, quest.Query)
		if err == nil {
			entries = append(entries, relevant...)
		}
	}
	if len(entries) == 0 && rt.gateway != nil && rt.gateway.MemoryStore != nil {
		relevant, err := rt.gateway.MemoryStore.LoadLongTermVector(ctx, quest.UserID)
		if err == nil {
			entries = append(entries, relevant...)
		}
	}
	quest.Memory.LongTermVector = append([]MemoryEntry(nil), entries...)
	if len(entries) == 0 {
		return nil
	}
	findings := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		findings = append(findings, map[string]any{
			"id":        entry.ID,
			"type":      entry.Type,
			"content":   entry.Content,
			"createdAt": entry.CreatedAt,
		})
	}
	quest.Artifacts["replayContext"] = map[string]any{
		"findings": findings,
	}

	// ReasoningBank: inject past experiences
	if reasoningBankEnabled() && rt.gateway != nil && rt.gateway.ResearchMemory != nil {
		primitives, episodes, promptText := rt.gateway.ResearchMemory.QueryExperiencePrimitives(ctx, quest.UserID, quest.Query, quest.Domain)
		if len(primitives) > 0 || len(episodes) > 0 {
			quest.Artifacts["reasoningBankReplay"] = map[string]any{
				"primitiveCount": len(primitives),
				"episodeCount":   len(episodes),
			}
			if quest.ResearchScratchpad == nil {
				quest.ResearchScratchpad = map[string]string{}
			}
			quest.ResearchScratchpad["experienceReplay"] = promptText
		}
	}

	return nil
}

func (rt *ResearchQuestRuntime) persistQuestMemory(ctx context.Context, quest *ResearchQuest) error {
	if rt == nil || rt.memoryStore == nil || quest == nil {
		return nil
	}

	newEntries := make([]MemoryEntry, 0, len(quest.AcceptedClaims))
	for _, claim := range quest.AcceptedClaims {
		content := strings.TrimSpace(firstNonEmpty(claim.Claim, claim.Snippet))
		if content == "" {
			continue
		}
		newEntries = append(newEntries, MemoryEntry{
			ID:        stableWisDevID("quest-memory", quest.QuestID, claim.SourceID, content),
			Type:      "verified_claim",
			Content:   content,
			CreatedAt: NowMillis(),
		})
	}
	if len(newEntries) > 0 {
		quest.Memory.LongTermVector = mergeEntries(quest.Memory.LongTermVector, newEntries)
		if err := rt.memoryStore.AppendLongTermVector(ctx, quest.UserID, newEntries, rt.longTermTTL); err != nil {
			return err
		}
	}
	if quest.PersistUserPreferences && len(quest.Memory.UserPersonalized) > 0 {
		if err := rt.memoryStore.SaveUserPreferences(ctx, quest.UserID, quest.Memory.UserPersonalized); err != nil {
			return err
		}
	}
	if rt.gateway != nil && rt.gateway.ResearchMemory != nil {
		preferredSources := extractQuestMethods(quest.Papers)
		if _, err := rt.gateway.ResearchMemory.PromoteFindings(ctx, ResearchMemoryPromotionInput{
			UserID:             quest.UserID,
			Query:              quest.Query,
			Scope:              ResearchMemoryScopeUser,
			Findings:           append([]EvidenceFinding(nil), quest.AcceptedClaims...),
			RecommendedQueries: dedupeTrimmedStrings(append([]string{}, quest.BlockingIssues...)),
			PreferredSources:   preferredSources,
		}); err != nil {
			return err
		}
		contradictions := make([]string, 0, len(quest.RejectedBranches))
		for _, branch := range quest.RejectedBranches {
			if text := strings.TrimSpace(branch.Content); text != "" {
				contradictions = append(contradictions, text)
			}
		}

		episodeInput := ResearchMemoryEpisodeInput{
			UserID:             quest.UserID,
			Query:              quest.Query,
			Scope:              ResearchMemoryScopeUser,
			Summary:            firstNonEmpty(quest.FinalAnswer, fmt.Sprintf("Completed research quest for %s.", quest.Query)),
			AcceptedFindings:   findingClaims(quest.AcceptedClaims),
			Contradictions:     dedupeTrimmedStrings(contradictions),
			UnresolvedGaps:     dedupeTrimmedStrings(quest.BlockingIssues),
			RecommendedQueries: dedupeTrimmedStrings(append([]string{}, extractQuestMethods(quest.Papers)...)),
			ReusableStrategies: preferredSources,
		}

		// ReasoningBank: call judge and merge into the single consolidation path
		var judgeLessons []ExperienceLesson
		if reasoningBankEnabled() && rt.gateway != nil && rt.gateway.Brain != nil {
			var replayedHints []string
			if hint, ok := quest.ResearchScratchpad["experienceReplay"]; ok {
				replayedHints = append(replayedHints, hint)
			}
			judgeOutput, err := rt.gateway.Brain.JudgeQuestExperience(ctx, quest, replayedHints)
			if err != nil {
				slog.Warn("reasoning bank judge degraded", "quest_id", quest.QuestID, "error", err)
			} else if judgeOutput != nil {
				// Enrich episode summary with judge metadata
				episodeInput.Summary = fmt.Sprintf("[ReasoningBank %.2f/%s] %s",
					judgeOutput.Score, judgeOutput.Outcome, episodeInput.Summary)
				episodeInput.Metadata = map[string]any{
					"judgeScore":     judgeOutput.Score,
					"judgeOutcome":   string(judgeOutput.Outcome),
					"successFactors": judgeOutput.SuccessFactors,
					"failureFactors": judgeOutput.FailureFactors,
					"judgeReasoning": judgeOutput.Reasoning,
				}
				judgeLessons = judgeOutput.Lessons
				quest.Artifacts["reasoningBankTrajectory"] = map[string]any{
					"outcome":      string(judgeOutput.Outcome),
					"judgeScore":   judgeOutput.Score,
					"lessonsCount": len(judgeOutput.Lessons),
				}
				rt.appendEvent(quest, "reasoning_bank_judged", QuestStageComplete,
					fmt.Sprintf("Experience judge: %s (%.2f)", judgeOutput.Outcome, judgeOutput.Score), nil)
			}
		}

		if _, err := rt.gateway.ResearchMemory.ConsolidateEpisode(ctx, episodeInput); err != nil {
			return err
		}

		// Merge distilled lessons into existing procedures
		if len(judgeLessons) > 0 {
			if err := rt.gateway.ResearchMemory.MergeExperienceLessons(ctx, quest.UserID, quest.QuestID, judgeLessons); err != nil {
				slog.Warn("reasoning bank lesson merge degraded", "quest_id", quest.QuestID, "error", err)
			}
		}
	}
	// Persist high-confidence findings and dead ends to the knowledge graph so
	// future quests can replay past insights and skip exhausted branches.
	if rt.memory != nil {
		if err := rt.memory.ConsolidateResearchQuest(ctx, quest); err != nil {
			slog.Warn("knowledge graph consolidation degraded", "quest_id", quest.QuestID, "error", err)
		}
	}
	return nil
}

func (rt *ResearchQuestRuntime) appendEvent(quest *ResearchQuest, eventType string, stage QuestStage, summary string, metadata map[string]any) {
	if quest == nil {
		return
	}
	quest.Events = append(quest.Events, QuestEvent{
		EventID:   stableWisDevID("quest-event", quest.QuestID, eventType, fmt.Sprintf("%d", len(quest.Events)+1)),
		Type:      eventType,
		Stage:     stage,
		Summary:   strings.TrimSpace(summary),
		Payload:   cloneAnyMap(metadata),
		CreatedAt: NowMillis(),
	})
}

func (rt *ResearchQuestRuntime) redisClient() redis.UniversalClient {
	if rt == nil || rt.gateway == nil {
		return nil
	}
	return rt.gateway.Redis
}

func (rt *ResearchQuestRuntime) canonicalResearchRuntime() *UnifiedResearchRuntime {
	if rt == nil || rt.gateway == nil {
		return nil
	}
	if rt.gateway.Runtime == nil && rt.gateway.Loop == nil && rt.gateway.SearchRegistry != nil {
		rt.gateway.Loop = NewAutonomousLoop(rt.gateway.SearchRegistry, rt.gateway.LLMClient)
	}
	if rt.gateway.Runtime == nil && rt.gateway.Loop != nil {
		rt.gateway.Runtime = NewUnifiedResearchRuntime(
			rt.gateway.Loop,
			rt.gateway.SearchRegistry,
			rt.gateway.LLMClient,
			rt.gateway.ProgrammaticLoopExecutor(),
			rt.gateway.ADKRuntime,
		).WithDurableResearchState(rt.gateway.StateStore, rt.gateway.Journal)
	}
	return rt.gateway.Runtime
}

func searchPapersToQuestSources(papers []search.Paper) []Source {
	if len(papers) == 0 {
		return nil
	}
	out := make([]Source, 0, len(papers))
	for _, paper := range papers {
		out = append(out, Source{
			ID:            paper.ID,
			Title:         paper.Title,
			Summary:       paper.Abstract,
			Link:          paper.Link,
			DOI:           paper.DOI,
			Source:        paper.Source,
			SourceApis:    append([]string(nil), paper.SourceApis...),
			Authors:       append([]string(nil), paper.Authors...),
			Year:          paper.Year,
			Publication:   paper.Venue,
			Keywords:      append([]string(nil), paper.Keywords...),
			Score:         paper.Score,
			CitationCount: paper.CitationCount,
		})
	}
	return out
}

func questLoopResultFromPayload(payload map[string]any) *LoopResult {
	if len(payload) == 0 {
		return nil
	}
	loopPayload := asMap(payload["loopResult"])
	if len(loopPayload) == 0 {
		return nil
	}
	body, err := json.Marshal(loopPayload)
	if err != nil {
		return nil
	}
	var result LoopResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}
	return &result
}

func questCanonicalLoopResult(quest *ResearchQuest) *LoopResult {
	if quest == nil {
		return nil
	}
	return questLoopResultFromPayload(asMap(quest.Artifacts[questCanonicalRuntimeArtifactKey]))
}

func questAcceptedClaimsFromLoopResult(quest *ResearchQuest, papers []Source) []EvidenceFinding {
	if loopResult := questCanonicalLoopResult(quest); loopResult != nil && len(loopResult.Evidence) > 0 {
		return append([]EvidenceFinding(nil), loopResult.Evidence...)
	}
	return buildAcceptedClaims(papers)
}

// BuildUnifiedResearchRuntimePayload exposes the canonical runtime metadata
// envelope used by quest retrieval and gateway research.execute-loop adapters.
func BuildUnifiedResearchRuntimePayload(result *UnifiedResearchResult, querySeed string, followUpQueries []string) map[string]any {
	return buildQuestCanonicalRuntimePayload(result, querySeed, followUpQueries)
}

func buildQuestCanonicalRuntimePayload(result *UnifiedResearchResult, querySeed string, followUpQueries []string) map[string]any {
	payload := map[string]any{
		"engine":          "unified_research_runtime",
		"querySeed":       strings.TrimSpace(querySeed),
		"plannedQueries":  []any{},
		"executedQueries": []any{},
	}
	if len(followUpQueries) > 0 {
		payload["followUpQueries"] = stringSliceToAny(followUpQueries)
	}
	if result == nil {
		return payload
	}
	if result.State != nil {
		payload["sessionState"] = valueToAny(result.State)
		payload["plannedQueries"] = stringSliceToAny(result.State.PlannedQueries)
		payload["branchPlans"] = valueToAny(result.State.BranchPlans)
		payload["executedQueries"] = stringSliceToAny(result.State.ExecutedQueries)
		payload["branchEvaluations"] = valueToAny(result.State.BranchEvaluations)
		payload["claimVerification"] = valueToAny(result.State.ClaimVerification)
		payload["verifierDecision"] = valueToAny(result.State.VerifierDecision)
		payload["citationGraph"] = valueToAny(result.State.CitationGraph)
		payload["sourceAcquisition"] = valueToAny(result.State.SourceAcquisition)
		payload["workerReports"] = valueToAny(result.State.Workers)
		payload["blackboard"] = valueToAny(result.State.Blackboard)
		payload["stopReason"] = strings.TrimSpace(result.State.StopReason)
		if len(result.State.CoverageLedger) > 0 {
			payload["coverageLedger"] = coverageLedgerEntriesToAny(result.State.CoverageLedger)
		}
	}
	if result.LoopResult != nil {
		gate := result.LoopResult.FinalizationGate
		if gate == nil {
			gate = buildResearchFinalizationGate(result.State, result.LoopResult)
		}
		finalStopReason := strings.TrimSpace(firstNonEmpty(gate.StopReason, result.LoopResult.StopReason, AsOptionalString(payload["stopReason"])))
		answerVerified := gate != nil && gate.Ready && !gate.Provisional
		answerStatus := ResearchAnswerStatusFromState(result.State, gate, answerVerified, finalStopReason)
		papers := searchPapersToQuestSources(result.LoopResult.Papers)
		payload["loopResult"] = valueToAny(result.LoopResult)
		payload["papers"] = mapsToAny(sourcesToArtifactMaps(papers))
		payload["count"] = len(papers)
		payload["evidence"] = valueToAny(result.LoopResult.Evidence)
		payload["finalAnswer"] = gateProvisionalAnswer(strings.TrimSpace(result.LoopResult.FinalAnswer), gate)
		payload["finalizationGate"] = valueToAny(gate)
		payload["answerStatus"] = answerStatus
		payload["stopReason"] = finalStopReason
		payload["followUpQueries"] = stringSliceToAny(normalizeLoopQueries(querySeed, append(append([]string(nil), followUpQueries...), gate.FollowUpQueries...)))
		payload["openLedgerCount"] = gate.OpenLedgerCount
		if gate.Provisional || !strings.EqualFold(gate.Status, "promote") {
			payload["blockingFinalization"] = true
		}
		payload["reasoningGraph"] = valueToAny(result.LoopResult.ReasoningGraph)
		payload["memoryTiers"] = valueToAny(result.LoopResult.MemoryTiers)
		payload["draftCritique"] = valueToAny(result.LoopResult.DraftCritique)
		payload["serviceTier"] = string(result.LoopResult.ServiceTier)
		payload["mode"] = string(result.LoopResult.Mode)
		if len(result.LoopResult.WorkerReports) > 0 {
			payload["workerReports"] = valueToAny(result.LoopResult.WorkerReports)
		}
		if result.LoopResult.GapAnalysis != nil {
			payload["gapAnalysis"] = valueToAny(result.LoopResult.GapAnalysis)
			payload["observedSourceFamilies"] = stringSliceToAny(result.LoopResult.GapAnalysis.ObservedSourceFamilies)
			if _, ok := payload["coverageLedger"]; !ok && len(result.LoopResult.GapAnalysis.Ledger) > 0 {
				payload["coverageLedger"] = coverageLedgerEntriesToAny(result.LoopResult.GapAnalysis.Ledger)
			}
		}
	}
	return payload
}

func buildQuestHypothesesFromLoopResult(quest *ResearchQuest, loopResult *LoopResult) []Hypothesis {
	if quest == nil || loopResult == nil {
		return nil
	}
	hypotheses := make([]Hypothesis, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, finding := range loopResult.Evidence {
		claim := strings.TrimSpace(finding.Claim)
		if claim == "" {
			continue
		}
		key := strings.ToLower(claim)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		copyFinding := finding
		hypotheses = append(hypotheses, Hypothesis{
			ID:                  stableWisDevID("hypothesis", quest.QuestID, finding.SourceID, claim),
			Query:               quest.Query,
			Text:                claim,
			Claim:               claim,
			ConfidenceThreshold: ClampFloat(finding.Confidence, 0.45, 0.95),
			ConfidenceScore:     ClampFloat(finding.Confidence, 0.45, 0.95),
			Status:              "candidate",
			Evidence:            []*EvidenceFinding{&copyFinding},
			EvidenceCount:       1,
			UpdatedAt:           NowMillis(),
		})
		if len(hypotheses) >= 4 {
			return hypotheses
		}
	}
	for _, executedQuery := range loopResult.ExecutedQueries {
		claim := strings.TrimSpace(executedQuery)
		if claim == "" {
			continue
		}
		key := strings.ToLower(claim)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		hypotheses = append(hypotheses, Hypothesis{
			ID:                  stableWisDevID("hypothesis", quest.QuestID, claim),
			Query:               quest.Query,
			Text:                claim,
			Claim:               claim,
			ConfidenceThreshold: 0.55,
			ConfidenceScore:     0.55,
			Status:              "candidate",
			EvidenceCount:       0,
			UpdatedAt:           NowMillis(),
		})
		if len(hypotheses) >= 4 {
			break
		}
	}
	return hypotheses
}

func (rt *ResearchQuestRuntime) defaultDecompose(ctx context.Context, quest *ResearchQuest) (map[string]any, error) {
	if runtime := rt.canonicalResearchRuntime(); runtime != nil {
		session := &AgentSession{
			SessionID:      firstNonEmpty(quest.SessionID, quest.QuestID, NewTraceID()),
			Query:          strings.TrimSpace(quest.Query),
			CorrectedQuery: strings.TrimSpace(quest.Query),
			DetectedDomain: strings.TrimSpace(quest.Domain),
			Mode:           quest.Mode,
			ServiceTier:    quest.ExecutionProfile.ServiceTier,
			MemoryTiers:    &MemoryTierState{},
		}
		plannedQueries := normalizeLoopQueries(quest.Query, runtime.planProgrammaticQueries(ctx, session, quest.Query, quest.Domain, string(quest.Mode)))
		if len(plannedQueries) > 0 {
			tasks := make([]ResearchTask, 0, len(plannedQueries))
			for idx, plannedQuery := range plannedQueries {
				tasks = append(tasks, ResearchTask{
					ID:     stableWisDevID("quest-task", quest.QuestID, fmt.Sprintf("%d", idx), plannedQuery),
					Name:   plannedQuery,
					Action: ActionResearchRetrievePapers,
					Reason: "Canonical runtime planned retrieval branch.",
				})
			}
			return map[string]any{
				"query":          quest.Query,
				"mode":           quest.Mode,
				"domain":         quest.Domain,
				"tasks":          valueToAny(tasks),
				"taskCount":      len(tasks),
				"plannedQueries": stringSliceToAny(plannedQueries),
				"source":         "canonical_runtime",
			}, nil
		}
	}

	model := ResolveModelNameForTier(firstNonEmptyQuestModelTier(quest))
	if caps := rt.brainCapabilities(); caps != nil {
		tasks, err := caps.DecomposeTaskInteractive(ctx, quest.Query, quest.Domain, model)
		if err == nil && len(tasks) > 0 {
			return map[string]any{
				"query":     quest.Query,
				"mode":      quest.Mode,
				"domain":    quest.Domain,
				"tasks":     valueToAny(tasks),
				"taskCount": len(tasks),
				"model":     model,
			}, nil
		}
		if err != nil {
			slog.Warn("research quest decomposition degraded", "quest_id", quest.QuestID, "error", err)
		}
	}
	return map[string]any{
		"query":     quest.Query,
		"mode":      quest.Mode,
		"domain":    quest.Domain,
		"taskCount": 1,
		"tasks": valueToAny([]ResearchTask{{
			ID:     stableWisDevID("quest-task", quest.QuestID, "retrieve"),
			Name:   "retrieve_and_verify",
			Action: ActionResearchRetrievePapers,
			Reason: "Retrieve and ground evidence for the quest query.",
		}}),
	}, nil
}

func questToMap(quest *ResearchQuest) (map[string]any, error) {
	body, err := json.Marshal(quest)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func questFromMap(ctx context.Context, payload map[string]any) (*ResearchQuest, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var quest ResearchQuest
	if err := json.Unmarshal(body, &quest); err != nil {
		return nil, err
	}
	// Guard: reject quests with empty query before ensureQuestDefaults calls
	// BuildResearchExecutionProfile with an empty query, which propagates ""
	// into the execution profile and eventually into ParallelSearch.
	if strings.TrimSpace(quest.Query) == "" {
		return nil, fmt.Errorf("loaded quest has empty query field (questId=%s)", quest.QuestID)
	}
	ensureQuestDefaults(&quest)
	return &quest, nil
}

func ensureQuestDefaults(quest *ResearchQuest) {
	ensureQuestDefaultsWithContext(context.Background(), quest)
}

func ensureQuestDefaultsWithContext(ctx context.Context, quest *ResearchQuest) {
	if quest == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if quest.Artifacts == nil {
		quest.Artifacts = map[string]any{}
	}
	if quest.Events == nil {
		quest.Events = []QuestEvent{}
	}
	if quest.Memory.PromotionRules == nil {
		quest.Memory.PromotionRules = map[string]bool{
			"userPreferenceOptIn": quest.PersistUserPreferences,
		}
	}
	if quest.Mode == "" {
		quest.Mode = WisDevModeGuided
	}
	if quest.ExecutionProfile.Mode == "" {
		quest.ExecutionProfile = BuildResearchExecutionProfile(ctx, quest.Query, string(quest.Mode), quest.QualityMode, false, 0)
	}
	if quest.ServiceTier == "" {
		quest.ServiceTier = quest.ExecutionProfile.ServiceTier
	}
	if quest.DecisionModelTier == "" {
		quest.DecisionModelTier = quest.ExecutionProfile.PrimaryModelTier
	}
	if len(quest.AgentAssignments) == 0 {
		quest.AgentAssignments = buildQuestAgentAssignments(quest.HeavyModelRequired, quest.ExecutionProfile.PrimaryModelTier)
	}
	if quest.EvidenceDossiers == nil {
		quest.EvidenceDossiers = map[string]*EvidenceDossier{}
	}
	if quest.ResearchScratchpad == nil {
		quest.ResearchScratchpad = map[string]string{}
	}
}

func defaultQuestHypotheses(_ context.Context, quest *ResearchQuest, papers []Source) ([]Hypothesis, error) {
	findings := questAcceptedClaimsFromLoopResult(quest, papers)
	if len(findings) == 0 {
		findings = []EvidenceFinding{{
			ID:         stableWisDevID("finding", quest.QuestID, "fallback"),
			Claim:      strings.TrimSpace(quest.Query),
			SourceID:   quest.QuestID,
			Confidence: 0.45,
			Status:     "tentative",
		}}
	}

	hypotheses := make([]Hypothesis, 0, MinInt(4, len(findings)))
	for idx, finding := range findings {
		if idx >= 4 {
			break
		}
		hypotheses = append(hypotheses, Hypothesis{
			ID:                  stableWisDevID("hypothesis", quest.QuestID, finding.SourceID, finding.Claim),
			Query:               quest.Query,
			Text:                finding.Claim,
			Claim:               finding.Claim,
			ConfidenceThreshold: finding.Confidence,
			ConfidenceScore:     finding.Confidence,
			Status:              "candidate",
			EvidenceCount:       1,
			UpdatedAt:           NowMillis(),
		})
	}
	return hypotheses, nil
}

func defaultQuestBranchReasoning(_ context.Context, _ *ResearchQuest, papers []Source, hypotheses []Hypothesis) (branchReasoningOutcome, error) {
	acceptedClaims := buildAcceptedClaims(papers)
	rejectedBranches := fallbackQuestRejectedBranches(hypotheses, acceptedClaims)
	if len(hypotheses) > 0 {
		supported := make([]EvidenceFinding, 0, MaxInt(len(hypotheses), 1))
		seen := make(map[string]struct{}, len(acceptedClaims))
		for _, hypothesis := range hypotheses {
			for _, evidence := range selectQuestHypothesisEvidence(hypothesis, acceptedClaims, 2) {
				if evidence == nil {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(firstNonEmpty(evidence.ID, evidence.SourceID, evidence.Claim)))
				if key == "" {
					continue
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				supported = append(supported, *evidence)
			}
		}
		if len(supported) > 0 {
			acceptedClaims = supported
		}
	}
	return branchReasoningOutcome{
		AcceptedClaims:   acceptedClaims,
		RejectedBranches: rejectedBranches,
		Payload: map[string]any{
			"hypothesisCount":  len(hypotheses),
			"methods":          stringSliceToAny(extractQuestMethods(papers)),
			"acceptedClaims":   len(acceptedClaims),
			"rejectedBranches": len(rejectedBranches),
			"sourceFamilies":   stringSliceToAny(buildObservedSourceFamiliesFromSources(papers)),
			"coverageGaps":     stringSliceToAny(collectQuestCoverageGaps(papers, acceptedClaims, rejectedBranches)),
		},
	}, nil
}

func (rt *ResearchQuestRuntime) brainCapabilities() *BrainCapabilities {
	if rt == nil || rt.gateway == nil {
		return nil
	}
	if rt.gateway.Brain == nil && rt.gateway.LLMClient != nil {
		rt.gateway.Brain = NewBrainCapabilities(rt.gateway.LLMClient)
	}
	return rt.gateway.Brain
}

func (rt *ResearchQuestRuntime) defaultHypotheses(ctx context.Context, quest *ResearchQuest, papers []Source) ([]Hypothesis, error) {
	findings := questAcceptedClaimsFromLoopResult(quest, papers)
	if loopResult := questCanonicalLoopResult(quest); loopResult != nil {
		hypotheses := buildQuestHypothesesFromLoopResult(quest, loopResult)
		if len(hypotheses) > 0 {
			for idx := range hypotheses {
				hypotheses[idx] = normalizeQuestHypothesis(quest, hypotheses[idx], findings)
			}
			return hypotheses, nil
		}
	}
	if caps := rt.brainCapabilities(); caps != nil {
		model := ResolveModelNameForTier(firstNonEmptyQuestModelTier(quest))
		intent := summarizeQuestDecomposition(asMap(quest.Artifacts["decomposition"]))
		hypotheses, err := caps.ProposeHypothesesInteractive(ctx, quest.Query, firstNonEmpty(intent, "research_quest"), model)
		if err == nil && len(hypotheses) > 0 {
			for idx := range hypotheses {
				hypotheses[idx] = normalizeQuestHypothesis(quest, hypotheses[idx], findings)
			}
			return hypotheses, nil
		}
		if err != nil {
			slog.Warn("research quest hypothesis generation degraded", "quest_id", quest.QuestID, "error", err)
		}
	}
	hypotheses, err := defaultQuestHypotheses(ctx, quest, papers)
	if err != nil {
		return nil, err
	}
	for idx := range hypotheses {
		hypotheses[idx] = normalizeQuestHypothesis(quest, hypotheses[idx], findings)
	}
	return hypotheses, nil
}

func (rt *ResearchQuestRuntime) defaultBranchReasoning(ctx context.Context, quest *ResearchQuest, papers []Source, hypotheses []Hypothesis) (branchReasoningOutcome, error) {
	findings := questAcceptedClaimsFromLoopResult(quest, papers)
	if loopResult := questCanonicalLoopResult(quest); loopResult != nil {
		rejected := fallbackQuestRejectedBranches(hypotheses, findings)
		payload := map[string]any{
			"canonicalRuntime": true,
			"hypothesisCount":  len(hypotheses),
			"methods":          stringSliceToAny(extractQuestMethods(papers)),
			"acceptedClaims":   len(findings),
			"rejectedBranches": len(rejected),
			"executedQueries":  stringSliceToAny(loopResult.ExecutedQueries),
			"sourceFamilies":   stringSliceToAny(buildObservedSourceFamiliesFromSources(papers)),
		}
		if loopResult.ReasoningGraph != nil {
			payload["reasoningGraph"] = valueToAny(loopResult.ReasoningGraph)
		}
		if loopResult.GapAnalysis != nil {
			payload["gapAnalysis"] = valueToAny(loopResult.GapAnalysis)
			payload["coverageGaps"] = stringSliceToAny(dedupeTrimmedStrings(append(
				append([]string(nil), loopResult.GapAnalysis.MissingAspects...),
				loopResult.GapAnalysis.Contradictions...,
			)))
			if len(loopResult.GapAnalysis.Ledger) > 0 {
				payload["coverageLedger"] = coverageLedgerEntriesToAny(loopResult.GapAnalysis.Ledger)
			}
		}
		return branchReasoningOutcome{
			AcceptedClaims:   findings,
			RejectedBranches: rejected,
			Payload:          payload,
		}, nil
	}
	branches := hypothesesToBranches(valueToAny(hypotheses))
	payload := map[string]any{
		"hypothesisCount": len(hypotheses),
		"methods":         stringSliceToAny(extractQuestMethods(papers)),
		"branches":        mapsToAny(typedBranchesToMaps(branches)),
	}

	if caps := rt.brainCapabilities(); caps != nil {
		model := ResolveModelNameForTier(firstNonEmptyQuestModelTier(quest))
		contextData := map[string]any{
			"query":         quest.Query,
			"domain":        quest.Domain,
			"papers":        mapsToAny(sourcesToArtifactMaps(papers)),
			"hypotheses":    valueToAny(hypotheses),
			"decomposition": quest.Artifacts["decomposition"],
		}
		// Inject experience replay into LLM prompt path
		if replay, ok := quest.ResearchScratchpad["experienceReplay"]; ok && replay != "" {
			contextData["pastExperience"] = replay
		}

		if generated, err := caps.GenerateThoughtsInteractive(ctx, contextData, model); err == nil {
			payload["generatedThoughts"] = cloneAnyMap(generated)
			if generatedBranches := hypothesesToBranches(generated["branches"]); len(generatedBranches) > 0 {
				branches = generatedBranches
			}
		} else {
			slog.Warn("research quest branch expansion degraded", "quest_id", quest.QuestID, "error", err)
		}
		if contradictionSummary, err := caps.DetectContradictionsInteractive(ctx, papers, model); err == nil && len(contradictionSummary) > 0 {
			payload["contradictions"] = cloneAnyMap(contradictionSummary)
		}
		if len(branches) > 0 {
			verification, verifyErr := caps.VerifyReasoningPaths(ctx, typedBranchesToMaps(branches), model)
			if len(verification) > 0 {
				payload["reasoningVerification"] = cloneAnyMap(verification)
				if verifiedBranches := hypothesesToBranches(verification["branches"]); len(verifiedBranches) > 0 {
					branches = verifiedBranches
				}
			}
			payload["branches"] = mapsToAny(typedBranchesToMaps(branches))
			accepted, rejected := questOutcomeFromReasoningBranches(branches, findings)
			if len(accepted) > 0 || len(rejected) > 0 {
				return branchReasoningOutcome{
					AcceptedClaims:   accepted,
					RejectedBranches: rejected,
					Payload:          payload,
				}, nil
			}
			if verifyErr != nil {
				slog.Warn("research quest reasoning verification degraded", "quest_id", quest.QuestID, "error", verifyErr)
			}
		}
	}

	rejected := fallbackQuestRejectedBranches(hypotheses, findings)
	return branchReasoningOutcome{
		AcceptedClaims:   findings,
		RejectedBranches: rejected,
		Payload:          payload,
	}, nil
}

func (rt *ResearchQuestRuntime) defaultEvidenceDossiers(_ context.Context, quest *ResearchQuest, papers []Source) (map[string]*EvidenceDossier, error) {
	findings := append([]EvidenceFinding(nil), quest.AcceptedClaims...)
	if len(findings) == 0 {
		findings = questAcceptedClaimsFromLoopResult(quest, papers)
	}
	hypotheses := cloneQuestHypothesisValues(quest.Hypotheses)
	dossiers := make(map[string]*EvidenceDossier)
	if len(hypotheses) == 0 {
		dossiers["corpus"] = &EvidenceDossier{
			JobID:     stableWisDevID("dossier", quest.QuestID, "corpus"),
			Verified:  append([]EvidenceFinding(nil), findings...),
			Gaps:      collectQuestCoverageGaps(papers, nil, quest.RejectedBranches),
			Tentative: nil,
		}
		return dossiers, nil
	}
	for _, hypothesis := range hypotheses {
		verified := derefQuestEvidenceFindings(selectQuestHypothesisEvidence(hypothesis, findings, 4))
		tentative := selectQuestTentativeEvidence(hypothesis, findings, verified, 2)
		dossierID := strings.TrimSpace(hypothesis.ID)
		if dossierID == "" {
			dossierID = stableWisDevID("dossier", quest.QuestID, hypothesis.Claim)
		}
		dossiers[dossierID] = &EvidenceDossier{
			JobID:         dossierID,
			Verified:      verified,
			Tentative:     tentative,
			Contradictory: nil,
			Gaps:          collectQuestCoverageGaps(papers, verified, matchingRejectedQuestBranches(hypothesis, quest.RejectedBranches)),
		}
	}
	return dossiers, nil
}

func firstNonEmptyQuestModelTier(quest *ResearchQuest) ModelTier {
	if quest == nil {
		return ModelTierStandard
	}
	if quest.DecisionModelTier != "" {
		return quest.DecisionModelTier
	}
	if quest.ExecutionProfile.PrimaryModelTier != "" {
		return quest.ExecutionProfile.PrimaryModelTier
	}
	return ModelTierStandard
}

func valueToAny(value any) any {
	if value == nil {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}

func cloneQuestHypotheses(hypotheses []Hypothesis) []*Hypothesis {
	if len(hypotheses) == 0 {
		return nil
	}
	out := make([]*Hypothesis, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		copyHypothesis := hypothesis
		if len(hypothesis.Evidence) > 0 {
			copyHypothesis.Evidence = append([]*EvidenceFinding(nil), hypothesis.Evidence...)
		}
		if len(hypothesis.Contradictions) > 0 {
			copyHypothesis.Contradictions = append([]*EvidenceFinding(nil), hypothesis.Contradictions...)
		}
		out = append(out, &copyHypothesis)
	}
	return out
}

func cloneQuestHypothesisValues(hypotheses []*Hypothesis) []Hypothesis {
	if len(hypotheses) == 0 {
		return nil
	}
	out := make([]Hypothesis, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		if hypothesis == nil {
			continue
		}
		copyHypothesis := *hypothesis
		if len(hypothesis.Evidence) > 0 {
			copyHypothesis.Evidence = append([]*EvidenceFinding(nil), hypothesis.Evidence...)
		}
		if len(hypothesis.Contradictions) > 0 {
			copyHypothesis.Contradictions = append([]*EvidenceFinding(nil), hypothesis.Contradictions...)
		}
		out = append(out, copyHypothesis)
	}
	return out
}

func summarizeQuestDecomposition(decomposition map[string]any) string {
	if len(decomposition) == 0 {
		return ""
	}
	taskCount := len(firstArtifactMaps(decomposition["tasks"]))
	query := strings.TrimSpace(AsOptionalString(decomposition["query"]))
	switch {
	case taskCount > 0 && query != "":
		return fmt.Sprintf("%d planned research tasks for %s", taskCount, query)
	case taskCount > 0:
		return fmt.Sprintf("%d planned research tasks", taskCount)
	case query != "":
		return query
	default:
		return "research decomposition captured"
	}
}

func summarizeQuestHypotheses(hypotheses []Hypothesis) string {
	if len(hypotheses) == 0 {
		return ""
	}
	parts := make([]string, 0, MinInt(3, len(hypotheses)))
	for _, hypothesis := range hypotheses {
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		parts = append(parts, claim)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, "; ")
}

func summarizeQuestReasoning(accepted []EvidenceFinding, rejected []QuestBranchRecord) string {
	parts := []string{}
	if len(accepted) > 0 {
		parts = append(parts, fmt.Sprintf("%d accepted claims", len(accepted)))
	}
	if len(rejected) > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected branches", len(rejected)))
	}
	return strings.Join(parts, "; ")
}

func collectQuestDossierGaps(dossiers map[string]*EvidenceDossier) []string {
	if len(dossiers) == 0 {
		return nil
	}
	gaps := make([]string, 0)
	for _, dossier := range dossiers {
		if dossier == nil {
			continue
		}
		gaps = append(gaps, dossier.Gaps...)
	}
	return dedupeTrimmedStrings(gaps)
}

func normalizeQuestHypothesis(quest *ResearchQuest, hypothesis Hypothesis, findings []EvidenceFinding) Hypothesis {
	claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
	if claim == "" {
		claim = strings.TrimSpace(quest.Query)
	}
	hypothesis.Claim = claim
	if strings.TrimSpace(hypothesis.Text) == "" {
		hypothesis.Text = claim
	}
	if strings.TrimSpace(hypothesis.Query) == "" {
		hypothesis.Query = strings.TrimSpace(quest.Query)
	}
	if strings.TrimSpace(hypothesis.ID) == "" {
		hypothesis.ID = stableWisDevID("hypothesis", quest.QuestID, claim)
	}
	hypothesis.Evidence = selectQuestHypothesisEvidence(hypothesis, findings, 3)
	hypothesis.EvidenceCount = len(hypothesis.Evidence)
	if hypothesis.ConfidenceScore <= 0 {
		hypothesis.ConfidenceScore = averageQuestEvidenceConfidence(hypothesis.Evidence, 0.55)
	}
	if hypothesis.ConfidenceThreshold <= 0 {
		hypothesis.ConfidenceThreshold = ClampFloat(hypothesis.ConfidenceScore, 0.45, 0.9)
	}
	if strings.TrimSpace(hypothesis.Status) == "" {
		hypothesis.Status = map[bool]string{true: "supported", false: "candidate"}[hypothesis.EvidenceCount > 0]
	}
	if hypothesis.UpdatedAt == 0 {
		hypothesis.UpdatedAt = NowMillis()
	}
	if hypothesis.CreatedAt == 0 {
		hypothesis.CreatedAt = hypothesis.UpdatedAt
	}
	return hypothesis
}

func questOutcomeFromReasoningBranches(branches []ReasoningBranch, findings []EvidenceFinding) ([]EvidenceFinding, []QuestBranchRecord) {
	accepted := make([]EvidenceFinding, 0, len(branches))
	rejected := make([]QuestBranchRecord, 0, len(branches))
	seenAccepted := map[string]struct{}{}
	for _, branch := range branches {
		claim := strings.TrimSpace(firstNonEmpty(branch.Claim, branch.Thought))
		status := strings.ToLower(strings.TrimSpace(branch.Status))
		verified := status == "verified" || status == "supported" || status == "accepted"
		if verified {
			finding := questFindingFromBranch(branch, findings)
			key := strings.ToLower(strings.TrimSpace(finding.Claim))
			if key != "" {
				if _, exists := seenAccepted[key]; !exists {
					seenAccepted[key] = struct{}{}
					accepted = append(accepted, finding)
				}
			}
			continue
		}
		if claim == "" {
			continue
		}
		rejected = append(rejected, QuestBranchRecord{
			ID:      firstNonEmpty(branch.ID, stableWisDevID("rejected-branch", claim)),
			Content: claim,
		})
	}
	return accepted, dedupeQuestBranchRecords(rejected)
}

func questFindingFromBranch(branch ReasoningBranch, fallback []EvidenceFinding) EvidenceFinding {
	claim := strings.TrimSpace(firstNonEmpty(branch.Claim, branch.Thought))
	if len(branch.Findings) > 0 {
		finding := branch.Findings[0]
		if strings.TrimSpace(finding.Claim) == "" {
			finding.Claim = claim
		}
		if strings.TrimSpace(finding.Snippet) == "" {
			finding.Snippet = firstNonEmpty(finding.Claim, claim)
		}
		if finding.Confidence <= 0 {
			finding.Confidence = ClampFloat(branch.SupportScore, 0.45, 0.98)
		}
		if strings.TrimSpace(finding.Status) == "" {
			finding.Status = "accepted"
		}
		return finding
	}
	for _, candidate := range fallback {
		if questTextsOverlap(claim, candidate.Claim) {
			return candidate
		}
	}
	return EvidenceFinding{
		ID:         stableWisDevID("finding", branch.ID, claim),
		Claim:      claim,
		Snippet:    claim,
		SourceID:   branch.ID,
		Confidence: ClampFloat(branch.SupportScore, 0.45, 0.98),
		Status:     "accepted",
	}
}

func fallbackQuestRejectedBranches(hypotheses []Hypothesis, findings []EvidenceFinding) []QuestBranchRecord {
	out := make([]QuestBranchRecord, 0)
	for _, hypothesis := range hypotheses {
		if len(selectQuestHypothesisEvidence(hypothesis, findings, 1)) > 0 {
			continue
		}
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		out = append(out, QuestBranchRecord{
			ID:      firstNonEmpty(hypothesis.ID, stableWisDevID("rejected-branch", claim)),
			Content: claim,
		})
	}
	return dedupeQuestBranchRecords(out)
}

func dedupeQuestBranchRecords(records []QuestBranchRecord) []QuestBranchRecord {
	if len(records) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]QuestBranchRecord, 0, len(records))
	for _, record := range records {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(record.ID, record.Content)))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	return out
}

func questTextTokens(value string) []string {
	raw := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		if len(token) < 4 {
			continue
		}
		out = append(out, token)
	}
	return out
}

func questTextsOverlap(a string, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	seen := map[string]struct{}{}
	for _, token := range questTextTokens(a) {
		seen[token] = struct{}{}
	}
	for _, token := range questTextTokens(b) {
		if _, ok := seen[token]; ok {
			return true
		}
	}
	return false
}

func selectQuestHypothesisEvidence(hypothesis Hypothesis, findings []EvidenceFinding, limit int) []*EvidenceFinding {
	claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
	if claim == "" || len(findings) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 3
	}
	out := make([]*EvidenceFinding, 0, limit)
	for _, finding := range findings {
		if !questTextsOverlap(claim, finding.Claim) && !questTextsOverlap(claim, finding.Snippet) {
			continue
		}
		copyFinding := finding
		out = append(out, &copyFinding)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func averageQuestEvidenceConfidence(findings []*EvidenceFinding, fallback float64) float64 {
	if len(findings) == 0 {
		return fallback
	}
	total := 0.0
	for _, finding := range findings {
		if finding == nil {
			continue
		}
		total += finding.Confidence
	}
	return ClampFloat(total/float64(len(findings)), 0.45, 0.98)
}

func derefQuestEvidenceFindings(findings []*EvidenceFinding) []EvidenceFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]EvidenceFinding, 0, len(findings))
	for _, finding := range findings {
		if finding == nil {
			continue
		}
		out = append(out, *finding)
	}
	return out
}

func selectQuestTentativeEvidence(hypothesis Hypothesis, findings []EvidenceFinding, verified []EvidenceFinding, limit int) []EvidenceFinding {
	if len(findings) == 0 || limit <= 0 {
		return nil
	}
	verifiedIDs := map[string]struct{}{}
	for _, finding := range verified {
		verifiedIDs[strings.ToLower(strings.TrimSpace(firstNonEmpty(finding.ID, finding.Claim)))] = struct{}{}
	}
	claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
	out := make([]EvidenceFinding, 0, limit)
	for _, finding := range findings {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(finding.ID, finding.Claim)))
		if _, exists := verifiedIDs[key]; exists {
			continue
		}
		if claim != "" && !questTextsOverlap(claim, firstNonEmpty(finding.Claim, finding.Snippet)) {
			continue
		}
		out = append(out, finding)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func matchingRejectedQuestBranches(hypothesis Hypothesis, rejected []QuestBranchRecord) []QuestBranchRecord {
	if len(rejected) == 0 {
		return nil
	}
	claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
	out := make([]QuestBranchRecord, 0)
	for _, branch := range rejected {
		if questTextsOverlap(claim, branch.Content) {
			out = append(out, branch)
		}
	}
	return out
}

func collectQuestCoverageGaps(papers []Source, verified []EvidenceFinding, rejected []QuestBranchRecord) []string {
	gaps := make([]string, 0, 4)
	if len(verified) == 0 {
		gaps = append(gaps, "No directly grounded evidence mapped to this hypothesis yet.")
	}
	if len(rejected) > 0 {
		gaps = append(gaps, "Conflicting reasoning branch still requires adjudication.")
	}
	for _, paper := range papers {
		if strings.TrimSpace(paper.DOI) == "" {
			gaps = append(gaps, "Some supporting papers still need canonical citation resolution.")
			break
		}
	}
	return dedupeTrimmedStrings(gaps)
}

func defaultQuestDraft(_ context.Context, quest *ResearchQuest, papers []Source, reasoning map[string]any) (string, error) {
	safeQuest := ensureQuestSnapshot(quest)
	if loopResult := questCanonicalLoopResult(safeQuest); loopResult != nil {
		if finalAnswer := strings.TrimSpace(loopResult.FinalAnswer); finalAnswer != "" {
			return finalAnswer, nil
		}
	}
	findings := append([]EvidenceFinding(nil), safeQuest.AcceptedClaims...)
	if len(findings) == 0 {
		findings = buildAcceptedClaims(papers)
	}
	ledger := questLedgerOrBuild(safeQuest, papers, safeQuest.CitationVerdict, "")

	lines := make([]string, 0, 12)
	lines = append(lines, "Research quest: "+strings.TrimSpace(firstNonEmpty(safeQuest.Query, "research quest")))
	lines = append(lines, fmt.Sprintf("Evidence base: %d grounded claim(s) from %d source(s).", len(findings), len(papers)))
	if citationLine := questCitationStatusSummary(safeQuest.CitationVerdict); citationLine != "" {
		lines = append(lines, "Citation status: "+citationLine)
	}

	lines = append(lines, "Key findings:")
	for _, findingLine := range questFindingLines(findings, papers, 3) {
		lines = append(lines, "- "+findingLine)
	}

	if summary := strings.TrimSpace(firstNonEmpty(AsOptionalString(reasoning["finalSummary"]), AsOptionalString(reasoning["summary"]))); summary != "" {
		lines = append(lines, "Reasoning summary: "+trimEvidenceText(summary, 240))
	}

	if openTitles := questOpenLedgerTitles(ledger, 2); len(openTitles) > 0 {
		lines = append(lines, "Open gaps:")
		for _, title := range openTitles {
			lines = append(lines, "- "+title)
		}
	}

	return strings.Join(lines, "\n"), nil
}

func defaultQuestCritique(_ context.Context, quest *ResearchQuest, _ []Source, verdict CitationVerdict) (string, error) {
	safeQuest := ensureQuestSnapshot(quest)
	ledger := questLedgerOrBuild(safeQuest, safeQuest.Papers, verdict, "")
	strengths := make([]string, 0, 3)
	if len(safeQuest.AcceptedClaims) > 0 {
		strengths = append(strengths, fmt.Sprintf("%d grounded claim(s) survived reasoning.", len(safeQuest.AcceptedClaims)))
	}
	if verdict.VerifiedCount > 0 {
		strengths = append(strengths, fmt.Sprintf("%d citation(s) verified across canonical authorities.", verdict.VerifiedCount))
	}
	if len(strengths) == 0 {
		strengths = append(strengths, "No durable strengths were recorded beyond retrieval breadth.")
	}

	risks := dedupeTrimmedStrings(append(
		append([]string(nil), safeQuest.BlockingIssues...),
		questOpenLedgerTitles(ledger, 3)...,
	))
	for _, branch := range safeQuest.RejectedBranches {
		content := strings.TrimSpace(branch.Content)
		if content == "" {
			continue
		}
		risks = append(risks, "Unsupported branch: "+content)
		if len(risks) >= 4 {
			break
		}
	}
	risks = dedupeTrimmedStrings(risks)

	lines := []string{
		"Quest critique: " + strings.TrimSpace(firstNonEmpty(safeQuest.Query, "research quest")),
		"Citation status: " + firstNonEmpty(questCitationStatusSummary(verdict), "unverified"),
		"Strengths:",
	}
	for _, strength := range strengths {
		lines = append(lines, "- "+strength)
	}
	if len(risks) > 0 {
		lines = append(lines, "Risks:")
		for _, risk := range risks {
			lines = append(lines, "- "+risk)
		}
	}
	if followUps := buildFollowUpQueriesFromLedger(safeQuest.Query, ledger, 3); len(followUps) > 0 {
		lines = append(lines, "Next actions:")
		for _, followUp := range followUps {
			lines = append(lines, "- "+followUp)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func ensureQuestSnapshot(quest *ResearchQuest) *ResearchQuest {
	if quest == nil {
		return &ResearchQuest{}
	}
	copyQuest := *quest
	return &copyQuest
}

func questLedgerOrBuild(quest *ResearchQuest, papers []Source, verdict CitationVerdict, critique string) []CoverageLedgerEntry {
	if quest != nil && len(quest.CoverageLedger) > 0 {
		return append([]CoverageLedgerEntry(nil), quest.CoverageLedger...)
	}
	return buildQuestCoverageLedger(quest, papers, verdict, critique)
}

func questCitationStatusSummary(verdict CitationVerdict) string {
	status := strings.TrimSpace(verdict.Status)
	if status == "" {
		status = map[bool]string{true: "promoted", false: "blocked"}[verdict.Promoted]
	}
	parts := []string{status}
	if verdict.VerifiedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d verified", verdict.VerifiedCount))
	}
	if verdict.AmbiguousCount > 0 {
		parts = append(parts, fmt.Sprintf("%d ambiguous", verdict.AmbiguousCount))
	}
	if verdict.RejectedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected", verdict.RejectedCount))
	}
	return strings.Join(parts, ", ")
}

func questFindingLines(findings []EvidenceFinding, papers []Source, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	lines := make([]string, 0, limit)
	for _, finding := range findings {
		claim := trimEvidenceText(strings.TrimSpace(finding.Claim), 200)
		if claim == "" {
			continue
		}
		source := strings.TrimSpace(firstNonEmpty(finding.PaperTitle, finding.SourceID))
		if source != "" {
			lines = append(lines, fmt.Sprintf("%s (%s)", claim, source))
		} else {
			lines = append(lines, claim)
		}
		if len(lines) >= limit {
			return lines
		}
	}
	for _, paper := range papers {
		summary := trimEvidenceText(firstNonEmpty(paper.Summary, paper.Abstract, paper.Title), 200)
		if summary == "" {
			continue
		}
		lines = append(lines, summary)
		if len(lines) >= limit {
			return lines
		}
	}
	if len(lines) == 0 {
		return []string{"No grounded findings were assembled yet."}
	}
	return lines
}

func questOpenLedgerTitles(ledger []CoverageLedgerEntry, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	titles := make([]string, 0, limit)
	for _, entry := range ledger {
		if entry.Status != coverageLedgerStatusOpen {
			continue
		}
		title := trimEvidenceText(firstNonEmpty(entry.Title, entry.Description), 200)
		if title == "" {
			continue
		}
		titles = append(titles, title)
		if len(titles) >= limit {
			break
		}
	}
	return dedupeTrimmedStrings(titles)
}

func buildCitationBlockedAnswer(verdict CitationVerdict) string {
	issues := strings.Join(dedupeTrimmedStrings(verdict.BlockingIssues), "; ")
	if issues == "" {
		issues = "citation verification requires human review"
	}
	return "Citation gate blocked publication: " + issues
}

func buildQuestAgentAssignments(heavy bool, primaryTier ModelTier) []AgentAssignment {
	assignments := []AgentAssignment{
		{AgentID: "scout", Role: "scout", ModelTier: ModelTierLight, ModelName: ResolveModelNameForTier(ModelTierLight), Status: "ready", Required: true},
		{AgentID: "synthesizer", Role: "synthesizer", ModelTier: primaryTier, ModelName: ResolveModelNameForTier(primaryTier), Status: "ready", Required: true},
		{AgentID: "reviewer", Role: "reviewer", ModelTier: ModelTierStandard, ModelName: ResolveModelNameForTier(ModelTierStandard), Status: "ready", Required: true},
	}
	if heavy {
		assignments = append(assignments, AgentAssignment{
			AgentID:    "arbiter",
			Role:       "arbiter",
			ModelTier:  ModelTierHeavy,
			ModelName:  ResolveModelNameForTier(ModelTierHeavy),
			Status:     "ready",
			Required:   true,
			HeavyModel: true,
		})
	}
	return assignments
}

func agentAssignmentsToRoleSlice(assignments []AgentAssignment) []any {
	out := make([]any, 0, len(assignments))
	for _, assignment := range assignments {
		out = append(out, assignment.Role)
	}
	return out
}

func escalateQuestModel(quest *ResearchQuest, reason string) {
	if quest == nil {
		return
	}
	quest.HeavyModelRequired = true
	quest.DecisionModelTier = ModelTierHeavy
	quest.ExecutionProfile.PrimaryModelTier = ModelTierHeavy
	quest.ExecutionProfile.PrimaryModelName = ResolveModelNameForTier(ModelTierHeavy)
	quest.AgentAssignments = buildQuestAgentAssignments(true, ModelTierHeavy)
	if strings.TrimSpace(reason) != "" {
		quest.Artifacts["routing"] = map[string]any{
			"reason":             reason,
			"heavyModelRequired": true,
			"agentRoles":         agentAssignmentsToRoleSlice(quest.AgentAssignments),
		}
	}
}

func extractQuestMethods(papers []Source) []string {
	methods := []string{}
	seen := map[string]struct{}{}
	add := func(value string) {
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		methods = append(methods, value)
	}
	for _, paper := range papers {
		text := strings.ToLower(strings.Join([]string{paper.Title, paper.Summary, paper.Publication}, " "))
		for _, method := range []string{"randomized", "benchmark", "systematic review", "meta-analysis", "ablation"} {
			if strings.Contains(text, method) {
				add(method)
			}
		}
	}
	sort.Strings(methods)
	return methods
}

func buildCanonicalCitationsFromSources(papers []Source) []CitationAuthorityRecord {
	out := make([]CitationAuthorityRecord, 0, len(papers))
	for _, paper := range papers {
		verified := strings.TrimSpace(paper.DOI) != "" || strings.TrimSpace(paper.ArxivID) != ""
		out = append(out, CitationAuthorityRecord{
			Authority:        firstNonEmpty(strings.TrimSpace(paper.Source), "heuristic"),
			CanonicalID:      firstNonEmpty(strings.TrimSpace(paper.DOI), strings.TrimSpace(paper.ArxivID), strings.TrimSpace(paper.ID), strings.TrimSpace(paper.Title)),
			DOI:              strings.TrimSpace(paper.DOI),
			ArxivID:          strings.TrimSpace(paper.ArxivID),
			Title:            strings.TrimSpace(paper.Title),
			Resolved:         verified,
			Verified:         verified,
			AgreementCount:   map[bool]int{true: 1, false: 0}[verified],
			ResolutionEngine: "heuristic",
		})
	}
	return out
}

func buildAcceptedClaims(papers []Source) []EvidenceFinding {
	out := make([]EvidenceFinding, 0, len(papers)*2)
	for _, paper := range papers {
		out = append(out, buildEvidenceFindingsFromSource(paper, 2)...)
	}
	return dedupeAcceptedClaims(out)
}

func dedupeAcceptedClaims(findings []EvidenceFinding) []EvidenceFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]EvidenceFinding, 0, len(findings))
	positions := make(map[string]int, len(findings))
	for _, finding := range findings {
		key := acceptedClaimDedupeKey(finding)
		if key == "" {
			out = append(out, finding)
			continue
		}
		if idx, ok := positions[key]; ok {
			out[idx] = mergeAcceptedClaimFinding(out[idx], finding)
			continue
		}
		positions[key] = len(out)
		out = append(out, finding)
	}
	return out
}

func acceptedClaimDedupeKey(finding EvidenceFinding) string {
	claim := strings.ToLower(strings.TrimSpace(firstNonEmpty(finding.Claim, finding.Snippet)))
	source := strings.ToLower(strings.TrimSpace(firstNonEmpty(finding.SourceID, finding.PaperTitle)))
	if claim == "" && source == "" {
		return ""
	}
	return source + "::" + claim
}

func mergeAcceptedClaimFinding(current EvidenceFinding, candidate EvidenceFinding) EvidenceFinding {
	merged := current
	if candidate.Confidence > merged.Confidence {
		candidate.Keywords = dedupeTrimmedStrings(append(append([]string{}, candidate.Keywords...), current.Keywords...))
		return candidate
	}
	merged.Keywords = dedupeTrimmedStrings(append(append([]string{}, merged.Keywords...), candidate.Keywords...))
	if strings.TrimSpace(merged.Snippet) == "" {
		merged.Snippet = candidate.Snippet
	}
	if strings.TrimSpace(merged.PaperTitle) == "" {
		merged.PaperTitle = candidate.PaperTitle
	}
	if strings.TrimSpace(merged.SourceID) == "" {
		merged.SourceID = candidate.SourceID
	}
	if merged.Year == 0 {
		merged.Year = candidate.Year
	}
	return merged
}

type citationResolveEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  struct {
		Resolved          []map[string]any `json:"resolved"`
		ResolverTrace     []map[string]any `json:"resolverTrace"`
		PromotionEligible bool             `json:"promotionEligible"`
		BlockingIssues    []string         `json:"blockingIssues"`
		Engine            string           `json:"engine"`
	} `json:"data"`
}

func ResolveCitationBrokerGateConfig() CitationBrokerGateConfig {
	return CitationBrokerGateConfig{
		Mode:            "go_local",
		AllowGoFallback: true,
		Warnings:        []string{"Go-owned local citation verification enabled"},
	}
}

func ResolveCitationsLive(papers []Source) ([]map[string]any, error) {
	return localCitationFallbackRecords(papers), nil
}

func VerifyCitationRecordsSecurely(papers []Source) (map[string]any, error) {
	return buildCitationVerificationPayload(localCitationFallbackRecords(papers), nil, nil, "go-local", false), nil
}

func normalizeResolvedCitationRecords(records []map[string]any, engine string) []map[string]any {
	seenCanonical := make(map[string]struct{}, len(records))
	normalized := make([]map[string]any, 0, len(records))
	for _, record := range records {
		status := strings.TrimSpace(firstNonEmpty(
			AsOptionalString(record["verificationStatus"]),
			AsOptionalString(record["verification_status"]),
		))
		verified := toBool(record["verified"]) || toBool(record["api_confirmed"]) || status == string(CitationStatusVerified)
		resolved := toBool(record["resolved"]) || verified
		canonical := canonicalCitationWithTrustDefaults(CanonicalCitation{
			ID:                     firstNonEmpty(AsOptionalString(record["id"]), AsOptionalString(record["title"])),
			Title:                  AsOptionalString(record["title"]),
			DOI:                    AsOptionalString(record["doi"]),
			ArxivID:                firstNonEmpty(AsOptionalString(record["arxivId"]), AsOptionalString(record["arxiv_id"])),
			CanonicalID:            firstNonEmpty(AsOptionalString(record["canonicalId"]), AsOptionalString(record["canonical_id"]), AsOptionalString(record["doi"]), AsOptionalString(record["title"])),
			Authors:                toStringSlice(record["authors"]),
			Year:                   toInt(record["year"]),
			Resolved:               resolved,
			Verified:               verified,
			VerificationStatus:     CitationVerificationStatus(status),
			SourceAuthority:        firstNonEmpty(AsOptionalString(record["sourceAuthority"]), AsOptionalString(record["source_authority"])),
			ResolutionEngine:       firstNonEmpty(AsOptionalString(record["resolutionEngine"]), AsOptionalString(record["engine"]), strings.TrimSpace(engine)),
			ResolverAgreementCount: toInt(firstNonEmptyValue(record["resolverAgreementCount"], record["resolver_agreement_count"])),
			ResolverConflict:       toBool(firstNonEmptyValue(record["resolverConflict"], record["resolver_conflict"])),
			ConflictNote:           firstNonEmpty(AsOptionalString(record["conflictNote"]), AsOptionalString(record["conflict_note"])),
			LandingURL:             firstNonEmpty(AsOptionalString(record["landingUrl"]), AsOptionalString(record["landing_url"])),
			SemanticScholarID:      firstNonEmpty(AsOptionalString(record["semanticScholarId"]), AsOptionalString(record["semantic_scholar_id"])),
			OpenAlexID:             firstNonEmpty(AsOptionalString(record["openAlexId"]), AsOptionalString(record["open_alex_id"])),
			ProvenanceHash:         firstNonEmpty(AsOptionalString(record["provenanceHash"]), AsOptionalString(record["provenance_hash"])),
		})
		dedupeKey := stableCitationIdentityKey(canonical)
		if dedupeKey != "" {
			if _, exists := seenCanonical[dedupeKey]; exists {
				canonical.Verified = false
				canonical.Resolved = true
				canonical.VerificationStatus = CitationStatusAmbiguous
				canonical.ResolverConflict = true
				canonical.ConflictNote = firstNonEmpty(strings.TrimSpace(canonical.ConflictNote), "duplicate citation metadata")
			} else {
				seenCanonical[dedupeKey] = struct{}{}
			}
		}
		canonical = canonicalCitationWithTrustDefaults(canonical)
		normalized = append(normalized, typedCitationsToMaps([]CanonicalCitation{canonical})[0])
	}
	return normalized
}

func stableCitationIdentityKey(record CanonicalCitation) string {
	record = canonicalCitationWithTrustDefaults(record)
	switch {
	case strings.TrimSpace(record.DOI) != "":
		return "doi:" + strings.ToLower(strings.TrimSpace(record.DOI))
	case strings.TrimSpace(record.ArxivID) != "":
		return "arxiv:" + strings.ToLower(strings.TrimSpace(record.ArxivID))
	case strings.TrimSpace(record.CanonicalID) != "":
		return "canonical:" + strings.ToLower(strings.TrimSpace(record.CanonicalID))
	case strings.TrimSpace(record.Title) != "":
		return "title:" + normalizeSpaceLower(record.Title)
	default:
		return strings.ToLower(strings.TrimSpace(record.ID))
	}
}

func localCitationFallbackRecords(papers []Source) []map[string]any {
	seenCanonical := map[string]struct{}{}
	records := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		canonicalID := firstNonEmpty(strings.TrimSpace(paper.DOI), strings.TrimSpace(paper.ArxivID), strings.TrimSpace(paper.Title), strings.TrimSpace(paper.ID))
		status := CitationStatusRejected
		resolved := false
		verified := false
		conflict := false
		conflictNote := ""
		agreement := 0

		switch {
		case strings.TrimSpace(paper.DOI) != "" || strings.TrimSpace(paper.ArxivID) != "":
			if _, exists := seenCanonical[canonicalID]; exists {
				status = CitationStatusAmbiguous
				resolved = true
				conflict = true
				conflictNote = "duplicate citation metadata"
			} else {
				status = CitationStatusVerified
				resolved = true
				verified = true
				agreement = 2
				seenCanonical[canonicalID] = struct{}{}
			}
		default:
			conflictNote = "missing canonical identifier"
		}

		canonical := canonicalCitationWithTrustDefaults(CanonicalCitation{
			ID:                     firstNonEmpty(strings.TrimSpace(paper.ID), strings.TrimSpace(paper.Title)),
			Title:                  strings.TrimSpace(paper.Title),
			DOI:                    strings.TrimSpace(paper.DOI),
			ArxivID:                strings.TrimSpace(paper.ArxivID),
			CanonicalID:            canonicalID,
			Authors:                append([]string(nil), paper.Authors...),
			Year:                   paper.Year,
			Resolved:               resolved,
			Verified:               verified,
			VerificationStatus:     status,
			SourceAuthority:        firstNonEmpty(strings.TrimSpace(paper.Source), "heuristic"),
			ResolutionEngine:       "go-local",
			ResolverAgreementCount: agreement,
			ResolverConflict:       conflict,
			ConflictNote:           conflictNote,
		})
		records = append(records, typedCitationsToMaps([]CanonicalCitation{canonical})[0])
	}
	return records
}

func buildCitationVerificationPayload(records []map[string]any, resolverTrace []map[string]any, issues []string, engine string, promotionEligible bool) map[string]any {
	typed := normalizeDuplicateCitationAuthorities(citationMapsToTyped(records))
	if len(resolverTrace) == 0 {
		resolverTrace = resolverTraceFromCitations(typed)
	}
	verifiedCount := 0
	ambiguousCount := 0
	rejectedCount := 0
	duplicateCount := 0
	for _, record := range typed {
		switch record.VerificationStatus {
		case CitationStatusVerified:
			verifiedCount++
		case CitationStatusAmbiguous:
			ambiguousCount++
			if record.ResolverConflict || strings.Contains(strings.ToLower(record.ConflictNote), "duplicate") {
				duplicateCount++
			}
		default:
			rejectedCount++
		}
	}
	if len(issues) == 0 {
		issues = citationTrustBlockingIssues(typed, nil)
	}
	if !promotionEligible {
		promotionEligible = len(issues) == 0 && ambiguousCount == 0 && rejectedCount == 0 && len(typed) > 0
	}
	bundle := BuildCitationTrustBundle(typed, resolverTrace, issues)
	promotionGate := map[string]any{
		"promoted":       promotionEligible,
		"blockingIssues": stringSliceToAny(issues),
	}
	if bundle != nil && len(bundle.PromotionGate) > 0 {
		promotionGate = cloneAnyMap(bundle.PromotionGate)
		promotionEligible = toBool(promotionGate["promoted"])
	}
	return map[string]any{
		"engine":              firstNonEmpty(strings.TrimSpace(engine), "go-local"),
		"verifiedRecords":     typedCitationsToMaps(typed),
		"validCount":          verifiedCount,
		"invalidCount":        ambiguousCount + rejectedCount,
		"duplicateCount":      duplicateCount,
		"verifiedCount":       verifiedCount,
		"ambiguousCount":      ambiguousCount,
		"rejectedCount":       rejectedCount,
		"promotionEligible":   promotionEligible,
		"blockingIssues":      stringSliceToAny(issues),
		"resolverTrace":       mapsToAny(resolverTrace),
		"promotionGate":       promotionGate,
		"citationTrustBundle": citationTrustBundleToMap(bundle),
	}
}

func normalizeDuplicateCitationAuthorities(records []CanonicalCitation) []CanonicalCitation {
	if len(records) == 0 {
		return nil
	}

	normalized := append([]CanonicalCitation(nil), records...)
	seen := make(map[string]int, len(normalized))
	for idx, record := range normalized {
		key := strings.ToLower(strings.TrimSpace(firstNonEmpty(
			record.CanonicalID,
			record.DOI,
			record.ArxivID,
			record.Title,
		)))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			normalized[idx].Verified = false
			normalized[idx].Resolved = true
			normalized[idx].VerificationStatus = CitationStatusAmbiguous
			normalized[idx].ResolverConflict = true
			normalized[idx].ResolutionEngine = firstNonEmpty(normalized[idx].ResolutionEngine, "go-local")
			normalized[idx].ConflictNote = firstNonEmpty(normalized[idx].ConflictNote, "duplicate citation metadata")
			if normalized[idx].ResolverAgreementCount <= 0 {
				normalized[idx].ResolverAgreementCount = 1
			}
			continue
		}
		seen[key] = idx
	}
	return normalized
}

func authorityRecordsFromCanonical(records []CanonicalCitation) []CitationAuthorityRecord {
	out := make([]CitationAuthorityRecord, 0, len(records))
	for _, record := range records {
		record = canonicalCitationWithTrustDefaults(record)
		out = append(out, CitationAuthorityRecord{
			Authority:          record.SourceAuthority,
			CanonicalID:        record.CanonicalID,
			DOI:                record.DOI,
			ArxivID:            record.ArxivID,
			Title:              record.Title,
			Resolved:           record.Resolved,
			Verified:           record.Verified,
			AgreementCount:     record.ResolverAgreementCount,
			ResolutionEngine:   record.ResolutionEngine,
			VerificationStatus: string(record.VerificationStatus),
			SourceAuthority:    record.SourceAuthority,
			ResolverConflict:   record.ResolverConflict,
			ConflictNote:       record.ConflictNote,
			LandingURL:         record.LandingURL,
			SemanticScholarID:  record.SemanticScholarID,
			OpenAlexID:         record.OpenAlexID,
			ProvenanceHash:     record.ProvenanceHash,
		})
	}
	return out
}

func authorityRecordsToAny(records []CitationAuthorityRecord) []any {
	out := make([]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"authority":          record.Authority,
			"canonicalId":        record.CanonicalID,
			"doi":                record.DOI,
			"arxivId":            record.ArxivID,
			"title":              record.Title,
			"resolved":           record.Resolved,
			"verified":           record.Verified,
			"agreementCount":     record.AgreementCount,
			"resolutionEngine":   record.ResolutionEngine,
			"verificationStatus": record.VerificationStatus,
			"sourceAuthority":    record.SourceAuthority,
			"resolverConflict":   record.ResolverConflict,
			"conflictNote":       record.ConflictNote,
			"landingUrl":         record.LandingURL,
			"semanticScholarId":  record.SemanticScholarID,
			"openAlexId":         record.OpenAlexID,
			"provenanceHash":     record.ProvenanceHash,
		})
	}
	return out
}

func asMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func firstNonEmptyValue(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		if typed, ok := value.(string); ok && strings.TrimSpace(typed) == "" {
			continue
		}
		return value
	}
	return nil
}
