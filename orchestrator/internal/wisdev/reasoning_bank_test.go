package wisdev

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
)

func TestReasoningBankFeatureFlagOff(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "false")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	prims, eps, prompt := compiler.QueryExperiencePrimitives(context.Background(), "u1", "test query", "cs")
	assert.Nil(t, prims)
	assert.Nil(t, eps)
	assert.Empty(t, prompt)

	err := compiler.MergeExperienceLessons(context.Background(), "u1", "traj-1", []ExperienceLesson{
		{Title: "Lesson A", Description: "desc", Content: "body", ApplicableWhen: "always"},
	})
	assert.NoError(t, err)

	state, loadErr := compiler.loadState("u1", "")
	require.NoError(t, loadErr)
	if state != nil {
		for _, p := range state.Procedures {
			assert.Empty(t, p.Content, "no primitives should be persisted when flag is off")
		}
	}
}

func TestQueryExperiencePrimitives_Empty(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	prims, eps, prompt := compiler.QueryExperiencePrimitives(context.Background(), "u1", "graph retrieval", "cs")
	assert.Empty(t, prims)
	assert.Empty(t, eps)
	assert.Equal(t, "No relevant past experiences found.", prompt)
}

func TestQueryExperiencePrimitives_Ranked(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	state := &ResearchMemoryState{
		Procedures: []ResearchProcedureMemory{
			{
				ID: "p1", Label: "Use citation chains", Content: "Follow citation chains for deep coverage.",
				ApplicableWhen: "multi-hop research", QueryPatterns: []string{"citation", "graph"},
				DomainHints: []string{"cs"}, Confidence: 0.8, Uses: 3, SuccessRate: 0.9, Scope: ResearchMemoryScopeUser, UserID: "u1",
			},
			{
				ID: "p2", Label: "Seed with reviews", Content: "Start with survey/review papers.",
				ApplicableWhen: "broad topics", QueryPatterns: []string{"survey", "review"},
				DomainHints: []string{"biology"}, Confidence: 0.6, Uses: 1, SuccessRate: 0.5, Scope: ResearchMemoryScopeUser, UserID: "u1",
			},
			{
				ID: "p3", Label: "Plain procedure", Confidence: 0.5, Uses: 1,
				Scope: ResearchMemoryScopeUser, UserID: "u1",
			},
		},
		Episodes: []ResearchMemoryEpisode{
			{ID: "e1", Query: "citation graph methods", Summary: "Explored citation graph approaches.", UserID: "u1", Scope: ResearchMemoryScopeUser},
		},
	}
	require.NoError(t, compiler.saveState("u1", "", state))

	prims, eps, prompt := compiler.QueryExperiencePrimitives(context.Background(), "u1", "citation graph retrieval", "cs")

	assert.Len(t, prims, 2, "only procedures with Content should be returned")
	assert.Equal(t, "Use citation chains", prims[0].Label, "query pattern + domain match should rank first")
	assert.Equal(t, "Seed with reviews", prims[1].Label)

	assert.NotEmpty(t, eps)
	assert.Contains(t, prompt, "Use citation chains")
	assert.Contains(t, prompt, "Follow citation chains")
	assert.Contains(t, prompt, "Strategic Primitives")
}

func TestMergeExperienceLessons(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	lessons := []ExperienceLesson{
		{Title: "Broad before deep", Description: "d1", Content: "Start broad then narrow.", ApplicableWhen: "new domains", QueryPatterns: []string{"survey"}, DomainHints: []string{"cs"}},
		{Title: "Check retractions", Description: "d2", Content: "Verify retraction status.", ApplicableWhen: "always"},
	}
	err := compiler.MergeExperienceLessons(context.Background(), "u1", "traj-1", lessons)
	require.NoError(t, err)

	state, err := compiler.loadState("u1", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Len(t, state.Procedures, 2)

	broad := findProcedure(state.Procedures, "Broad before deep")
	require.NotNil(t, broad)
	assert.Equal(t, "Start broad then narrow.", broad.Content)
	assert.Equal(t, "new domains", broad.ApplicableWhen)
	assert.Equal(t, []string{"survey"}, broad.QueryPatterns)
	assert.Equal(t, 1, broad.Uses)
	assert.Equal(t, 1.0, broad.SuccessRate)
	assert.Equal(t, 0.5, broad.Confidence)
	assert.Equal(t, []string{"traj-1"}, broad.SourceTrajectoryIDs)
}

func TestMergeExperienceLessons_Dedup(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	lesson := ExperienceLesson{Title: "Verify sources", Description: "d", Content: "Check citations.", ApplicableWhen: "always"}
	require.NoError(t, compiler.MergeExperienceLessons(context.Background(), "u1", "traj-1", []ExperienceLesson{lesson}))
	require.NoError(t, compiler.MergeExperienceLessons(context.Background(), "u1", "traj-2", []ExperienceLesson{lesson}))
	require.NoError(t, compiler.MergeExperienceLessons(context.Background(), "u1", "traj-3", []ExperienceLesson{lesson}))

	state, err := compiler.loadState("u1", "")
	require.NoError(t, err)
	require.Len(t, state.Procedures, 1)

	proc := state.Procedures[0]
	assert.Equal(t, 3, proc.Uses)
	assert.LessOrEqual(t, proc.Confidence, 0.95)
	assert.Greater(t, proc.Confidence, 0.5)
	assert.Len(t, proc.SourceTrajectoryIDs, 3)
}

func TestMergeExperienceLessons_CapAt80(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	lessons := make([]ExperienceLesson, 85)
	for i := range lessons {
		lessons[i] = ExperienceLesson{
			Title:       strings.Repeat("L", 3) + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Description: "d", Content: "c", ApplicableWhen: "always",
		}
	}
	require.NoError(t, compiler.MergeExperienceLessons(context.Background(), "u1", "traj-1", lessons))

	state, err := compiler.loadState("u1", "")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(state.Procedures), 80)
}

func TestConsolidateEpisodeWithJudgeMetadata(t *testing.T) {
	t.Setenv("WISDEV_STATE_DIR", t.TempDir())

	store := NewRuntimeStateStore(nil, nil)
	compiler := NewResearchMemoryCompiler(store, nil)

	meta := map[string]any{
		"judgeScore":     0.82,
		"judgeOutcome":   "success",
		"successFactors": []string{"thorough citation verification"},
		"failureFactors": []string{},
	}
	_, err := compiler.ConsolidateEpisode(context.Background(), ResearchMemoryEpisodeInput{
		UserID:           "u1",
		Query:            "graph retrieval methods",
		Scope:            ResearchMemoryScopeUser,
		Summary:          "[ReasoningBank 0.82/success] Explored graph retrieval.",
		AcceptedFindings: []string{"finding1"},
		Metadata:         meta,
	})
	require.NoError(t, err)

	state, err := compiler.loadState("u1", "")
	require.NoError(t, err)
	require.NotNil(t, state)
	require.NotEmpty(t, state.Episodes)

	ep := state.Episodes[0]
	assert.Contains(t, ep.Summary, "[ReasoningBank 0.82/success]")
	require.NotNil(t, ep.Metadata)
	assert.Equal(t, 0.82, ep.Metadata["judgeScore"])
	assert.Equal(t, "success", ep.Metadata["judgeOutcome"])
}

func TestProcedureRankingWithPatterns(t *testing.T) {
	procs := []ResearchProcedureMemory{
		{Label: "Generic hint", Confidence: 0.6, Content: "generic", ApplicableWhen: "always"},
		{Label: "Pattern match", Confidence: 0.6, Content: "specific", ApplicableWhen: "always", QueryPatterns: []string{"neural"}, DomainHints: []string{"cs"}, SuccessRate: 0.8},
	}

	ranked := rankExperienceProcedures(procs, "neural network architecture", "cs", 5)
	require.Len(t, ranked, 2)
	assert.Equal(t, "Pattern match", ranked[0].Label, "pattern+domain+successRate match should outrank equal base confidence")
}

func TestBuildExperiencePromptBlock(t *testing.T) {
	prims := []ResearchProcedureMemory{
		{Label: "Follow citations", Content: "Walk citation graph.", ApplicableWhen: "multi-hop"},
	}
	eps := []ResearchMemoryEpisode{
		{Query: "citation graph retrieval", Summary: "Explored citation approaches with good results."},
	}

	text := buildExperiencePromptBlock(prims, eps)
	assert.Contains(t, text, "Follow citations")
	assert.Contains(t, text, "Walk citation graph.")
	assert.Contains(t, text, "Use when: multi-hop")
	assert.Contains(t, text, "citation graph retrieval")
	assert.Contains(t, text, "Strategic Primitives")
	assert.Contains(t, text, "Relevant Past Trajectories")
}

func TestBuildExperiencePromptBlock_Empty(t *testing.T) {
	text := buildExperiencePromptBlock(nil, nil)
	assert.Equal(t, "No relevant past experiences found.", text)
}

func TestJudgeQuestExperience_NilBrain(t *testing.T) {
	var brain *BrainCapabilities
	result, err := brain.JudgeQuestExperience(context.Background(), &ResearchQuest{}, nil)
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestJudgeQuestExperience_MockLLM(t *testing.T) {
	msc := &mockLLMServiceClient{}
	lc := llm.NewClient()
	lc.SetClient(msc)
	brain := NewBrainCapabilities(lc)

	judgeJSON := `{
		"score": 0.78,
		"outcome": "success",
		"reasoning": "Good coverage and verified citations.",
		"successFactors": ["thorough search", "citation verification"],
		"failureFactors": ["limited scope"],
		"lessons": [{
			"title": "Use parallel queries",
			"description": "Running parallel queries improves coverage.",
			"content": "Issue multiple queries simultaneously for breadth.",
			"applicableWhen": "broad research topics",
			"queryPatterns": ["survey", "overview"],
			"domainHints": ["cs"]
		}]
	}`

	msc.On("StructuredOutput", mock.Anything, mock.MatchedBy(func(req *llmv1.StructuredRequest) bool {
		return assertBrainRecoverableStructuredPolicy(t, req) &&
			req.Model == llm.ResolveLightModel() &&
			strings.Contains(req.Prompt, "research quality judge")
	})).Return(&llmv1.StructuredResponse{JsonResult: judgeJSON}, nil).Once()

	quest := &ResearchQuest{
		QuestID:          "q1",
		Query:            "transformer architectures",
		Domain:           "cs",
		FinalAnswer:      "Transformers use self-attention mechanisms...",
		CurrentIteration: 3,
		Papers:           []Source{{ID: "p1"}, {ID: "p2"}},
		AcceptedClaims:   []EvidenceFinding{{ID: "c1"}},
	}

	result, err := brain.JudgeQuestExperience(context.Background(), quest, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0.78, result.Score)
	assert.Equal(t, TrajectoryOutcomeSuccess, result.Outcome)
	assert.Contains(t, result.Reasoning, "Good coverage")
	assert.Len(t, result.SuccessFactors, 2)
	assert.Len(t, result.Lessons, 1)
	assert.Equal(t, "Use parallel queries", result.Lessons[0].Title)
	assert.Equal(t, []string{"survey", "overview"}, result.Lessons[0].QueryPatterns)

	msc.AssertExpectations(t)
}

func TestReasoningBankEnabled_EnvValues(t *testing.T) {
	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "true")
	assert.True(t, ReasoningBankEnabled())

	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "TRUE")
	assert.True(t, ReasoningBankEnabled())

	t.Setenv("WISDEV_ENABLE_REASONING_BANK", " True ")
	assert.True(t, ReasoningBankEnabled())

	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "false")
	assert.False(t, ReasoningBankEnabled())

	t.Setenv("WISDEV_ENABLE_REASONING_BANK", "")
	assert.False(t, ReasoningBankEnabled())
}

func findProcedure(procs []ResearchProcedureMemory, label string) *ResearchProcedureMemory {
	for i := range procs {
		if procs[i].Label == label {
			return &procs[i]
		}
	}
	return nil
}
