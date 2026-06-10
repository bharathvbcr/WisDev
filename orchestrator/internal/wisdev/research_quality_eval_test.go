package wisdev

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Research-quality eval harness: golden fixtures scoring coverage, grounding,
// and contradiction detection so quality regressions fail CI like code
// regressions do. Deterministic — no network, no live LLM.

const evalQuery = "sleep deprivation memory consolidation"

func evalOnTopicPapers() []search.Paper {
	return []search.Paper{
		{ID: "on1", Title: "Sleep deprivation impairs memory consolidation in adults", Abstract: "Randomized trial: sleep deprivation reduced memory consolidation and recall performance."},
		{ID: "on2", Title: "Sleep deprivation and memory consolidation: hippocampal replay", Abstract: "Sleep deprivation disrupts memory consolidation through impaired hippocampal replay mechanisms."},
		{ID: "on3", Title: "Effects of sleep deprivation on declarative memory consolidation", Abstract: "Sleep deprivation degraded declarative memory consolidation in a cohort of students."},
	}
}

func evalOffTopicPapers() []search.Paper {
	return []search.Paper{
		{ID: "off1", Title: "Soil microbiome diversity in tropical forests", Abstract: "We catalog bacterial taxa across forest plots."},
		{ID: "off2", Title: "Quarterly earnings prediction with transformers", Abstract: "A finance model for earnings forecasts."},
	}
}

func TestResearchQualityEval_Coverage(t *testing.T) {
	// On-topic fixtures must be admitted; off-topic fixtures must be filtered.
	admitted, accepted := admitSearchPapersForQuery(nil, evalQuery, append(evalOnTopicPapers(), evalOffTopicPapers()...), 10)
	require.NotEmpty(t, accepted)

	admittedIDs := make(map[string]bool, len(admitted))
	for _, p := range admitted {
		admittedIDs[p.ID] = true
	}
	onTopicAdmitted := 0
	for _, p := range evalOnTopicPapers() {
		if admittedIDs[p.ID] {
			onTopicAdmitted++
		}
	}
	offTopicAdmitted := 0
	for _, p := range evalOffTopicPapers() {
		if admittedIDs[p.ID] {
			offTopicAdmitted++
		}
	}

	assert.GreaterOrEqual(t, onTopicAdmitted, 2, "coverage: at least 2 of 3 on-topic papers must be admitted")
	assert.Zero(t, offTopicAdmitted, "coverage: off-topic papers must be filtered out")
}

func TestResearchQualityEval_Grounding(t *testing.T) {
	// Every on-topic finding must yield at least one sentence-level span
	// anchoring the research claim.
	grounded := 0
	for _, paper := range evalOnTopicPapers() {
		finding := &EvidenceFinding{
			SourceID:   paper.ID,
			PaperTitle: paper.Title,
			Snippet:    paper.Abstract,
		}
		if spans := extractEvidenceSpans(evalQuery, finding); len(spans) > 0 {
			grounded++
			assert.NotEmpty(t, spans[0].Quote)
			assert.Greater(t, spans[0].MatchScore, 0.0)
		}
	}
	assert.GreaterOrEqual(t, grounded, 2, "grounding: at least 2 of 3 on-topic findings must produce span anchors")
}

func TestResearchQualityEval_ContradictionDetection(t *testing.T) {
	hypothesis := &Hypothesis{
		ID:    "h-eval",
		Claim: "Sleep deprivation impairs memory consolidation",
		Text:  "Sleep deprivation impairs memory consolidation",
		Evidence: []*EvidenceFinding{
			{ID: "evA", Claim: "Study shows sleep deprivation significantly improves nothing; deprivation impairs memory consolidation", Snippet: "The trial confirms sleep deprivation impairs memory consolidation.", Confidence: 0.8},
			{ID: "evB", Claim: "Replication found no significant effect of sleep deprivation on memory consolidation", Snippet: "Sleep deprivation did not impair memory consolidation in this replication.", Confidence: 0.8},
		},
	}

	t.Run("heuristic prescreen detects the planted contradiction", func(t *testing.T) {
		pairs := contradictionPairsFor(hypothesis)
		require.NotEmpty(t, pairs, "planted contradictory pair must be detected")
	})

	t.Run("judge confirms real contradictions and drops false positives", func(t *testing.T) {
		prev := GlobalLLMClient
		defer func() { GlobalLLMClient = prev }()

		msc := &mockLLMServiceClient{}
		lc := llm.NewClient()
		lc.SetClient(msc)
		GlobalLLMClient = lc

		msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
			return req != nil && strings.Contains(req.Prompt, "genuinely CONTRADICT")
		})).Return(&llmv1.StructuredResponse{
			JsonResult: `{"verdicts":[{"pairIndex":0,"contradicts":true,"severity":"high","explanation":"opposite effect direction"}]}`,
		}, nil).Once()

		candidates := contradictionPairsFor(hypothesis)
		confirmed := judgeContradictionPairsWithLLM(context.Background(), hypothesis, candidates)
		require.Len(t, confirmed, 1)
		assert.Equal(t, ContradictionHigh, confirmed[0].Severity)
		assert.Equal(t, "opposite effect direction", confirmed[0].Explanation)
	})

	t.Run("judge failure falls back to heuristic candidates", func(t *testing.T) {
		prev := GlobalLLMClient
		defer func() { GlobalLLMClient = prev }()
		GlobalLLMClient = nil

		candidates := contradictionPairsFor(hypothesis)
		confirmed := judgeContradictionPairsWithLLM(context.Background(), hypothesis, candidates)
		assert.Equal(t, candidates, confirmed)
	})
}

func TestBeliefHypothesisSync(t *testing.T) {
	bsm := NewBeliefStateManager()
	state := bsm.GetState()
	state.AddBelief(&Belief{ID: "b1", Claim: "Claim Alpha", Status: BeliefStatusActive})
	state.AddBelief(&Belief{ID: "b2", Claim: "Claim Beta", Status: BeliefStatusActive})
	state.AddBelief(&Belief{ID: "b3", Claim: "Claim Gamma", Status: BeliefStatusActive})

	retired := bsm.RetireBeliefsForInactiveHypotheses([]Hypothesis{
		{Claim: "Claim Alpha", IsTerminated: true, Status: "refuted"},
		{Claim: "claim beta", Status: "merged", IsTerminated: true},
		{Claim: "Claim Gamma", Status: "active"},
	})

	assert.Equal(t, 2, retired)
	assert.Equal(t, BeliefStatusRefuted, state.Beliefs["b1"].Status)
	assert.Equal(t, BeliefStatusRevised, state.Beliefs["b2"].Status)
	assert.Equal(t, BeliefStatusActive, state.Beliefs["b3"].Status)
}

func TestDomainOutcomeStore(t *testing.T) {
	store := NewDomainOutcomeStore()
	assert.Equal(t, 0.5, store.HistoricalReward("medicine"), "no history yields neutral reward")

	store.Record(context.Background(), "Medicine", 0.2, 4)
	assert.InDelta(t, 0.2, store.HistoricalReward("medicine"), 1e-9)

	store.Record(context.Background(), "medicine", 0.9, 30)
	reward := store.HistoricalReward("MEDICINE")
	assert.Greater(t, reward, 0.2)
	assert.Less(t, reward, 0.9)

	assert.Equal(t, 0.5, store.HistoricalReward(""), "blank domain maps to general with no history... unless recorded")
}

func TestPRMRewardLogging(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "prm_rewards.jsonl")
	t.Setenv("WISDEV_PRM_LOG_PATH", logPath)

	reward, err := callPRM(context.Background(), "sess-log", map[string]any{
		"paperCount":            3,
		"searchSuccess":         0.5,
		"citationVerifiedRatio": 0.5,
		"coverageScore":         0.5,
	})
	require.NoError(t, err)
	assert.Greater(t, reward, 0.0)

	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var record PRMRewardRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &record))
	assert.Equal(t, "step", record.Kind)
	assert.Equal(t, "heuristic", record.Source)
	assert.Equal(t, "sess-log", record.SessionID)
	assert.Equal(t, reward, record.Reward)

	t.Setenv("WISDEV_PRM_LOG_PATH", "off")
	_, err = callPRM(context.Background(), "sess-log", map[string]any{"paperCount": 1})
	require.NoError(t, err)
}
