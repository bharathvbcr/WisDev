# ReasoningBank Implementation Plan for WisDev

## Context

The user discovered Google Research's **ReasoningBank** paper (experience-based learning for agents) and wants to implement it in the WisDev research agent runtime. ReasoningBank enables agents to learn from past research sessions by: (1) retrieving relevant past experiences before starting a new quest, (2) judging the outcome after completion via LLM, (3) distilling success/failure trajectories into reusable strategic primitives.

This plan maps ReasoningBank onto WisDev's existing Go memory infrastructure — no new storage backends, no Python involvement. Architecture mandate: Go owns orchestration.

---

## Mapping: ReasoningBank → Existing WisDev Types

| ReasoningBank concept | Maps to | File |
|---|---|---|
| Individual experience (trajectory) | `ResearchMemoryEpisode` (extended with judge metadata) | `research_memory.go:77` |
| Strategic primitive (distilled lesson) | `ResearchProcedureMemory` (extend with content fields) | `research_memory.go:92` |
| Factual memory (verified claims) | `ResearchMemoryRecord` (already exists) | `research_memory.go:52` |
| Experience graph edges | `ResearchMemoryEdge` (already exists) | `research_memory.go:104` |
| Memory retrieval | `ResearchMemoryCompiler.Query()` (already exists) | `research_memory.go:273` |
| Episode consolidation | `ResearchMemoryCompiler.ConsolidateEpisode()` (already exists) | `research_memory.go:208` |

---

## Files to Create/Modify

| Action | File | What |
|---|---|---|
| **NEW** | `internal/wisdev/reasoning_bank.go` | Types, ReasoningBank service, LLM judge, retrieval, distillation, persistence |
| **NEW** | `internal/wisdev/reasoning_bank_test.go` | Unit tests |
| MODIFY | `internal/wisdev/gateway.go` | Add `ReasoningBank *ReasoningBank` to `AgentGateway` struct (line 62), initialize after line 594 |
| MODIFY | `internal/wisdev/research_quest_runtime.go` | Add `reasoningBankEnabled()`, modify `seedReplayContext` (line 697), modify `persistQuestMemory` (line 771) |

---

## Phase 1: New Types (`reasoning_bank.go`)

```go
// Feature flag — follows WISDEV_ALLOW_GO_CITATION_FALLBACK pattern
func reasoningBankEnabled() bool {
    return strings.EqualFold(strings.TrimSpace(os.Getenv("WISDEV_ENABLE_REASONING_BANK")), "true")
}

type TrajectoryOutcome string // "success" | "partial" | "failure"

// ExperienceTrajectory — a judged quest run record
type ExperienceTrajectory struct {
    ID                  string              `json:"id"`
    QuestID             string              `json:"questId"`
    UserID              string              `json:"userId"`
    Query               string              `json:"query"`
    Domain              string              `json:"domain,omitempty"`
    Outcome             TrajectoryOutcome   `json:"outcome"`
    JudgeScore          float64             `json:"judgeScore"`       // 0.0–1.0
    JudgeReasoning      string              `json:"judgeReasoning"`
    SuccessFactors      []string            `json:"successFactors,omitempty"`
    FailureFactors      []string            `json:"failureFactors,omitempty"`
    IterationCount      int                 `json:"iterationCount"`
    PapersRetrieved     int                 `json:"papersRetrieved"`
    AcceptedClaimCount  int                 `json:"acceptedClaimCount"`
    CoverageGaps        []string            `json:"coverageGaps,omitempty"`
    ExtractedLessons    []StrategicPrimitive `json:"extractedLessons,omitempty"`
    CreatedAt           int64               `json:"createdAt"`
}

// StrategicPrimitive — a distilled, reusable lesson
type StrategicPrimitive struct {
    ID                  string   `json:"id"`
    Title               string   `json:"title"`
    Description         string   `json:"description"`
    Content             string   `json:"content"`
    ApplicableWhen      string   `json:"applicableWhen"`
    QueryPatterns       []string `json:"queryPatterns"`
    DomainHints         []string `json:"domainHints"`
    Confidence          float64  `json:"confidence"`
    Uses                int      `json:"uses"`
    SuccessRate         float64  `json:"successRate"`
    SourceTrajectoryIDs []string `json:"sourceTrajectoryIds,omitempty"`
    CreatedAt           int64    `json:"createdAt"`
    UpdatedAt           int64    `json:"updatedAt"`
}

// ExperienceRetrievalResult — injected into quest at seed time
type ExperienceRetrievalResult struct {
    Primitives         []StrategicPrimitive        `json:"primitives"`
    RelatedEpisodes    []ResearchMemoryEpisode     `json:"relatedEpisodes"`
    ProceduralHints    []ResearchProcedureMemory   `json:"proceduralHints"`
    InjectedPromptText string                      `json:"injectedPromptText"`
}
```

---

## Phase 2: ReasoningBank Service

```go
type ReasoningBank struct {
    stateStore     *RuntimeStateStore
    journal        *RuntimeJournal
    memoryCompiler *ResearchMemoryCompiler
    brain          *BrainCapabilities
}

func NewReasoningBank(store *RuntimeStateStore, journal *RuntimeJournal,
    compiler *ResearchMemoryCompiler, brain *BrainCapabilities) *ReasoningBank
```

**Two public methods:**

### `RetrieveExperiences` — called BEFORE quest

1. Query `ResearchMemoryCompiler.Query()` for episodes + procedural hints
2. Load `StrategicPrimitive` list from state store file `reasoning_bank_primitives_{userID}.json`
3. Rank primitives by keyword overlap with query + domain match (same scoring logic as `researchMemoryScore` at `research_memory.go:1037`)
4. Build formatted prompt text block from top-5 primitives + top-3 episodes

### `JudgeAndDistill` — called AFTER quest

1. Build judge input from `ResearchQuest` fields (query, finalAnswer, acceptedClaims, rejectedBranches, coverageLedger, citationVerdict, iterationCount, blockingIssues)
2. Call LLM judge via `BrainCapabilities` pattern: `appendWisdevStructuredOutputInstruction` + `applyBrainStructuredPolicy` + `llmClient.StructuredOutput` → parse `ExperienceJudgeOutput`
3. Build `ExperienceTrajectory` from judge output
4. Distill `StrategicPrimitive` entries from `ExtractedLessons`
5. Persist trajectory via `ResearchMemoryCompiler.ConsolidateEpisode()` (reuse existing episode infra)
6. Persist primitives via state store file (merge with existing, cap at 80, prune low-confidence/low-use)

---

## Phase 3: LLM Judge Prompt

Uses `ResolveLightModel()` (cost-efficient, same as `CritiqueEvidenceSet`), 30s timeout.

Prompt structure:
```
You are a research quality judge. Evaluate this completed research quest and extract lessons.

## Quest Details
- Query: {query}
- Domain: {domain}
- Papers retrieved: {count}
- Accepted claims: {count}
- Rejected branches: {count}
- Iteration count: {count}
- Citation verdict: {status}
- Coverage gaps: {list}

## Final Answer (truncated to 500 chars):
{finalAnswer}

## Previously Injected Experience:
{summaries of replayed primitives, or "None"}

## Instructions
1. Score overall outcome (0.0-1.0): evidence quality, coverage, citation verification, efficiency
2. Classify: "success" (≥0.7), "partial" (0.4-0.7), "failure" (<0.4)
3. List 1-3 success factors, 1-3 failure factors
4. Extract 0-2 reusable lessons as {title, description, content, applicableWhen, queryPatterns}
```

JSON schema response → unmarshal into `ExperienceJudgeOutput`.

---

## Phase 4: Insertion Points

### A. Before quest — `seedReplayContext` (`research_quest_runtime.go:668`)

Insert after line 697 (after `quest.Artifacts["replayContext"]` is set, before `return nil`):

```go
// ReasoningBank: inject past experiences
if reasoningBankEnabled() && rt.gateway != nil && rt.gateway.ReasoningBank != nil {
    expResult, err := rt.gateway.ReasoningBank.RetrieveExperiences(ctx, quest.UserID, quest.Query, quest.Domain)
    if err != nil {
        rt.appendEvent(quest, "reasoning_bank_warning", QuestStageInit,
            "Experience retrieval degraded", map[string]any{"error": err.Error()})
    } else if expResult != nil && len(expResult.Primitives) > 0 {
        quest.Artifacts["reasoningBankReplay"] = map[string]any{
            "primitiveCount": len(expResult.Primitives),
            "episodeCount":   len(expResult.RelatedEpisodes),
            "injectedPrompt": expResult.InjectedPromptText,
        }
        if quest.ResearchScratchpad == nil {
            quest.ResearchScratchpad = map[string]string{}
        }
        quest.ResearchScratchpad["reasoningBankReplay"] = expResult.InjectedPromptText
    }
}
```

### B. After quest — `persistQuestMemory` (`research_quest_runtime.go:704`)

Insert after line 771 (after `memory.ConsolidateResearchQuest` call):

```go
// ReasoningBank: judge and distill experiences
if reasoningBankEnabled() && rt.gateway != nil && rt.gateway.ReasoningBank != nil {
    trajectory, err := rt.gateway.ReasoningBank.JudgeAndDistill(ctx, quest, nil)
    if err != nil {
        slog.Warn("reasoning bank judge degraded", "quest_id", quest.QuestID, "error", err)
    } else if trajectory != nil {
        quest.Artifacts["reasoningBankTrajectory"] = map[string]any{
            "outcome":      string(trajectory.Outcome),
            "judgeScore":   trajectory.JudgeScore,
            "lessonsCount": len(trajectory.ExtractedLessons),
        }
        rt.appendEvent(quest, "reasoning_bank_judged", QuestStageComplete,
            fmt.Sprintf("Experience judge: %s (%.2f)", trajectory.Outcome, trajectory.JudgeScore), nil)
    }
}
```

### C. Gateway wiring — `NewAgentGateway` (`gateway.go:493`)

Add field to struct (after line 62, after `Runtime`):
```go
ReasoningBank *ReasoningBank
```

Initialize after line 594 (after `ResearchMemory` is created):
```go
if reasoningBankEnabled() {
    gw.ReasoningBank = NewReasoningBank(gw.StateStore, journal, gw.ResearchMemory, gw.Brain)
}
```

---

## Phase 5: Persistence

**Strategic primitives** stored as `reasoning_bank_primitives_{userID}.json` via `RuntimeStateStore.writeJSONFile` / `readJSONFile` — same pattern as `research_memory_{scope}_{userID}_{projectID}.json`.

**Trajectory episodes** stored via existing `ResearchMemoryCompiler.ConsolidateEpisode()` — the episode `Summary` field is prefixed with `[ReasoningBank]` and includes judge score. Episode `ReusableStrategies` carries the extracted lesson titles+content.

**Merge logic** for primitives: dedup by ID, increment `Uses`, running-average `SuccessRate`, boost `Confidence` by +0.05 per reuse (capped at 0.95), cap total at 80 per user. Same approach as `mergeResearchProcedures` at `research_memory.go:806`.

---

## Phase 6: Test Strategy

**File**: `internal/wisdev/reasoning_bank_test.go`

| Test | What it verifies |
|---|---|
| `TestReasoningBankFeatureFlagOff` | Feature flag disabled → all methods return nil gracefully |
| `TestRetrieveExperiences_Empty` | No prior primitives → returns empty result (not error) |
| `TestRetrieveExperiences_WithPrimitives` | Seeded primitives → ranked by query relevance, prompt text built |
| `TestJudgeAndDistill_NoBrain` | nil BrainCapabilities → graceful nil return |
| `TestJudgeAndDistill_MockLLM` | Mock LLM returns judge JSON → trajectory built, primitives distilled, episode consolidated |
| `TestStrategicPrimitiveMerge` | Dedup, uses increment, success rate averaging, cap at 80 |
| `TestRankPrimitivesByRelevance` | Query pattern + domain matching produces correct ordering |
| `TestBuildExperiencePromptBlock` | Formatted text includes primitive titles, confidence, applicable-when |
| `TestPersistAndLoadPrimitives` | Round-trip via state store file |

Mock pattern: `mockLLMServiceClient.StructuredOutput` returning canned JSON — same as `contradiction_test.go:17` and `test_helpers_test.go:32`.

---

## Verification

1. `cd orchestrator && go build ./...` — compiles cleanly
2. `go test ./internal/wisdev/... -run TestReasoningBank -count=1 -v` — all new tests pass
3. `go test ./... -count=1` — full suite still passes (no regressions)
4. With `WISDEV_ENABLE_REASONING_BANK=false` (default): zero behavioral change, feature is invisible
5. With `WISDEV_ENABLE_REASONING_BANK=true`: quest artifacts include `reasoningBankReplay` (at start) and `reasoningBankTrajectory` (at end), journal has `reasoning_bank_judged` events

---

## Implementation Sequence

1. Create `reasoning_bank.go` — types + `ReasoningBank` struct + `RetrieveExperiences` (no LLM dependency)
2. Add primitive persistence helpers (`save/load/mergeStrategicPrimitives`)
3. Wire into `AgentGateway` struct + constructor
4. Wire retrieval into `seedReplayContext`
5. Implement `callExperienceJudge` + `JudgeAndDistill` with LLM structured output
6. Wire judge into `persistQuestMemory`
7. Write comprehensive tests
8. Compile + run full test suite
