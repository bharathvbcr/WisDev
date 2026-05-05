package wisdev

import (
	"fmt"
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestArchitecture_NormalizeModeAndTier(t *testing.T) {
	assert.Equal(t, WisDevModeGuided, NormalizeWisDevMode(""))
	assert.Equal(t, WisDevModeYOLO, NormalizeWisDevMode("autonomous"))
	assert.Equal(t, ServiceTierPriority, ResolveServiceTier(WisDevModeGuided, true))
	assert.Equal(t, ServiceTierFlex, ResolveServiceTier(WisDevModeYOLO, false))
}

func TestArchitecture_UpdateSessionReasoningGraph(t *testing.T) {
	session := &AgentSession{
		SessionID:      "sess-1",
		OriginalQuery:  "graph reasoning for academic synthesis",
		CorrectedQuery: "graph reasoning for academic synthesis",
		UserID:         "user-1",
	}

	hypotheses := []Hypothesis{
		{Claim: "Path A", FalsifiabilityCondition: "Counter evidence", ConfidenceThreshold: 0.7},
		{Claim: "Path B", FalsifiabilityCondition: "Missing citations", ConfidenceThreshold: 0.6},
	}
	evidence := []EvidenceFinding{
		{Claim: "Evidence 1", SourceID: "p1", PaperTitle: "Paper 1", Confidence: 0.91},
	}

	UpdateSessionReasoningGraph(session, hypotheses, evidence)

	if assert.NotNil(t, session.ReasoningGraph) {
		assert.Equal(t, "graph reasoning for academic synthesis", session.ReasoningGraph.Query)
		assert.GreaterOrEqual(t, len(session.ReasoningGraph.Nodes), 3)
		assert.GreaterOrEqual(t, len(session.ReasoningGraph.Edges), 2)
	}
	if assert.NotNil(t, session.MemoryTiers) {
		assert.Len(t, session.MemoryTiers.ShortTermWorking, 2)
		assert.Len(t, session.MemoryTiers.ArtifactMemory, 1)
		assert.Len(t, session.MemoryTiers.LongTermVector, 1)
		assert.Len(t, session.MemoryTiers.UserPersonalized, 1)
	}
}

func TestArchitecture_BuildDefaultPlanUsesMode(t *testing.T) {
	session := &AgentSession{
		SessionID:      "sess-2",
		OriginalQuery:  "citation verification in scholarly systems",
		CorrectedQuery: "citation verification in scholarly systems",
		Mode:           WisDevModeYOLO,
		ServiceTier:    ServiceTierFlex,
	}

	plan := BuildDefaultPlan(session)

	if assert.NotNil(t, plan) {
		assert.Len(t, plan.Steps, 10)
		assert.Equal(t, "research.resolveCanonicalCitations", plan.Steps[3].Action)
		assert.Equal(t, "research.verifyReasoningPaths", plan.Steps[7].Action)
		assert.True(t, plan.Steps[2].Parallelizable)
		assert.True(t, plan.Steps[7].RequiresHumanCheckpoint)
		assert.Contains(t, plan.Reasoning, "yolo")
	}
}

func TestArchitecture_BuildReasoningGraphWithPaperSeeds(t *testing.T) {
	papers := []search.Paper{{ID: "p-seed-1", Title: "Seed Paper", Abstract: "Evidence seed abstract"}}

	graph := BuildReasoningGraphWithPaperSeeds("", nil, nil, papers)

	if assert.NotNil(t, graph) {
		assert.NotEmpty(t, graph.Nodes)
		assert.Equal(t, ReasoningNodeEvidence, graph.Nodes[0].Type)
		assert.Equal(t, "Seed Paper", graph.Nodes[0].Label)
	}
}

func TestArchitecture_BuildReasoningGraph_SpecialistStatus(t *testing.T) {
	evidence := []EvidenceFinding{{
		Claim:      "Evidence with specialist review",
		SourceID:   "p-specialist",
		PaperTitle: "Paper Specialist",
		Confidence: 0.88,
		SpecialistNotes: []SpecialistNote{
			{Type: SpecialistTypeMethodologist, DeepAnalysis: "Strong RCT design", Verification: 1},
			{Type: SpecialistTypeSkeptic, DeepAnalysis: "Minor external validity caveat", Verification: 0},
		},
	}}

	graph := BuildReasoningGraph("specialist verification", nil, evidence)
	if assert.NotNil(t, graph) {
		if assert.NotEmpty(t, graph.Nodes) {
			var metadata map[string]any
			for _, node := range graph.Nodes {
				if node.Type == ReasoningNodeEvidence {
					metadata = node.Metadata
					break
				}
			}
			if assert.NotNil(t, metadata) {
				assert.Equal(t, "specialist-verified", metadata["specialistStatus"])
				assert.NotNil(t, metadata["specialistNotes"])
			}
		}
	}
}

func TestArchitecture_BuildReasoningGraphLinksEvidenceByExplicitSupport(t *testing.T) {
	hypotheses := []Hypothesis{
		{
			Claim: "Path A",
			Evidence: []*EvidenceFinding{
				{ID: "ev-a", SourceID: "paper-a"},
			},
		},
		{
			Claim: "Path B",
			Evidence: []*EvidenceFinding{
				{ID: "ev-b", SourceID: "paper-b"},
			},
		},
	}
	evidence := []EvidenceFinding{
		{ID: "ev-a", Claim: "Evidence A", SourceID: "paper-a", PaperTitle: "Paper A", Confidence: 0.9},
		{ID: "ev-b", Claim: "Evidence B", SourceID: "paper-b", PaperTitle: "Paper B", Confidence: 0.8},
		{ID: "ev-c", Claim: "Evidence C", SourceID: "paper-c", PaperTitle: "Paper C", Confidence: 0.7},
	}

	graph := BuildReasoningGraph("support mapping", hypotheses, evidence)
	if assert.NotNil(t, graph) {
		hypothesisA := hypothesisNodeID(hypotheses[0])
		hypothesisB := hypothesisNodeID(hypotheses[1])
		evidenceA := evidenceNodeID(evidence[0])
		evidenceB := evidenceNodeID(evidence[1])
		evidenceC := evidenceNodeID(evidence[2])

		edgeSet := make(map[string]struct{}, len(graph.Edges))
		for _, edge := range graph.Edges {
			edgeSet[edge.From+"|"+edge.To+"|"+edge.Label] = struct{}{}
		}

		_, hasA := edgeSet[hypothesisA+"|"+evidenceA+"|supported_by"]
		_, hasB := edgeSet[hypothesisB+"|"+evidenceB+"|supported_by"]
		_, crossAB := edgeSet[hypothesisA+"|"+evidenceB+"|supported_by"]
		_, crossBA := edgeSet[hypothesisB+"|"+evidenceA+"|supported_by"]
		_, rootC := edgeSet["question_root|"+evidenceC+"|supported_by"]

		assert.True(t, hasA)
		assert.True(t, hasB)
		assert.False(t, crossAB)
		assert.False(t, crossBA)
		assert.True(t, rootC)
	}
}

func TestArchitecture_UpdateSessionReasoningGraph_RebuildsWorkingMemoryDeterministically(t *testing.T) {
	session := &AgentSession{
		SessionID:      "sess-stable",
		OriginalQuery:  "evidence ordering stability",
		CorrectedQuery: "evidence ordering stability",
		UserID:         "user-stable",
	}

	hypotheses := []Hypothesis{
		{Claim: "Primary path", FalsifiabilityCondition: "Counter evidence", ConfidenceThreshold: 0.75},
	}
	firstEvidence := []EvidenceFinding{
		{ID: "ev-a", Claim: "Evidence A", SourceID: "paper-a", PaperTitle: "Paper A", Confidence: 0.9},
		{ID: "ev-b", Claim: "Evidence B", SourceID: "paper-b", PaperTitle: "Paper B", Confidence: 0.82},
	}
	secondEvidence := []EvidenceFinding{
		{ID: "ev-b", Claim: "Evidence B", SourceID: "paper-b", PaperTitle: "Paper B", Confidence: 0.82},
		{ID: "ev-a", Claim: "Evidence A", SourceID: "paper-a", PaperTitle: "Paper A", Confidence: 0.9},
	}

	UpdateSessionReasoningGraph(session, hypotheses, firstEvidence)
	firstWorking := append([]MemoryEntry(nil), session.MemoryTiers.ShortTermWorking...)
	firstArtifact := append([]MemoryEntry(nil), session.MemoryTiers.ArtifactMemory...)
	firstLongTerm := append([]MemoryEntry(nil), session.MemoryTiers.LongTermVector...)

	UpdateSessionReasoningGraph(session, hypotheses, secondEvidence)

	if assert.NotNil(t, session.MemoryTiers) {
		assert.Equal(t, firstWorking, session.MemoryTiers.ShortTermWorking)
		assert.ElementsMatch(t, firstArtifact, session.MemoryTiers.ArtifactMemory)
		assert.ElementsMatch(t, firstLongTerm, session.MemoryTiers.LongTermVector)
		assert.Len(t, session.MemoryTiers.ShortTermWorking, 1)
		assert.Len(t, session.MemoryTiers.ArtifactMemory, 2)
		assert.Len(t, session.MemoryTiers.LongTermVector, 2)
	}
}

func TestArchitecture_UpdateSessionMemoryTiers_PrunesByEvaluationScore(t *testing.T) {
	session := &AgentSession{
		SessionID:      "sess-memory",
		OriginalQuery:  "value-based pruning",
		CorrectedQuery: "value-based pruning",
		UserID:         "user-memory",
	}

	hypotheses := make([]Hypothesis, 0, 16)
	for i := 0; i < 16; i++ {
		hypotheses = append(hypotheses, Hypothesis{
			Claim:           fmt.Sprintf("Hypothesis %02d", i),
			ConfidenceScore: 0.1 + (float64(i) / 20.0),
		})
	}

	UpdateSessionMemoryTiers(session, hypotheses, nil)

	if assert.NotNil(t, session.MemoryTiers) && assert.Len(t, session.MemoryTiers.ShortTermWorking, 15) {
		assert.Equal(t, "Hypothesis 15", session.MemoryTiers.ShortTermWorking[0].Content)
		assert.Equal(t, "Hypothesis 01", session.MemoryTiers.ShortTermWorking[14].Content)
	}
}

func TestArchitecture_MergeBeliefStateIntoReasoningGraph(t *testing.T) {
	graph := BuildReasoningGraph("belief graph", nil, nil)
	manager := NewBeliefStateManager()
	belief := manager.CreateBeliefFromHypothesis(
		&Hypothesis{Claim: "Claim under review", ConfidenceScore: 0.9},
		[]EvidenceFinding{{ID: "ev-1", Claim: "Supporting evidence", SourceID: "paper-1", Confidence: 0.9}},
		"gap-1",
		"query-1",
	)
	if assert.NotNil(t, belief) {
		manager.GetState().RefuteBelief(belief.ID)
	}

	merged := MergeBeliefStateIntoReasoningGraph(graph, manager.GetState())
	if assert.NotNil(t, merged) {
		nodeFound := false
		edgeFound := false
		for _, node := range merged.Nodes {
			if node.Type == ReasoningNodeClaim && node.Label == "Claim under review" {
				nodeFound = true
				break
			}
		}
		for _, edge := range merged.Edges {
			if edge.Label == "refuted_by" && edge.To != "" {
				edgeFound = true
				break
			}
		}
		assert.True(t, nodeFound)
		assert.True(t, edgeFound)
	}
}

func TestArchitecture_UpdateReasoningGraphIncrementally_ColdStart(t *testing.T) {
	hypotheses := []Hypothesis{
		{Claim: "Hyp A", FalsifiabilityCondition: "Counter A", ConfidenceThreshold: 0.7},
	}
	evidence := []EvidenceFinding{
		{ID: "ev1", Claim: "Evidence 1", SourceID: "s1", PaperTitle: "Paper 1", Confidence: 0.8},
	}

	graph := UpdateReasoningGraphIncrementally(nil, "test query", hypotheses, evidence, nil)
	assert.NotNil(t, graph)
	assert.GreaterOrEqual(t, len(graph.Nodes), 3) // root + hypothesis + evidence
	assert.GreaterOrEqual(t, len(graph.Edges), 1)
}

func TestArchitecture_UpdateReasoningGraphIncrementally_MergesNew(t *testing.T) {
	hyp1 := []Hypothesis{{Claim: "Hyp A", FalsifiabilityCondition: "Counter A", ConfidenceThreshold: 0.7}}
	ev1 := []EvidenceFinding{{ID: "ev1", Claim: "Evidence 1", SourceID: "s1", PaperTitle: "P1", Confidence: 0.8}}

	graph := UpdateReasoningGraphIncrementally(nil, "test query", hyp1, ev1, nil)
	initialNodeCount := len(graph.Nodes)

	hyp2 := []Hypothesis{{Claim: "Hyp B", FalsifiabilityCondition: "Counter B", ConfidenceThreshold: 0.6}}
	ev2 := []EvidenceFinding{{ID: "ev2", Claim: "Evidence 2", SourceID: "s2", PaperTitle: "P2", Confidence: 0.7}}

	updated := UpdateReasoningGraphIncrementally(graph, "test query", hyp2, ev2, nil)
	assert.Greater(t, len(updated.Nodes), initialNodeCount)
}

func TestArchitecture_UpdateReasoningGraphIncrementally_UpdatesExisting(t *testing.T) {
	hyp := []Hypothesis{{Claim: "Hyp A", FalsifiabilityCondition: "Counter A", ConfidenceThreshold: 0.5}}
	graph := UpdateReasoningGraphIncrementally(nil, "query", hyp, nil, nil)

	hypUpdated := []Hypothesis{{Claim: "Hyp A", FalsifiabilityCondition: "Counter A", ConfidenceThreshold: 0.9, IsTerminated: true}}
	updated := UpdateReasoningGraphIncrementally(graph, "query", hypUpdated, nil, nil)

	nodeID := hypothesisNodeID(hyp[0])
	if node, ok := updated.NodesMap[nodeID]; ok {
		assert.Equal(t, 0.9, node.Confidence)
		if terminated, ok := node.Metadata["isTerminated"].(bool); ok {
			assert.True(t, terminated)
		}
	}
}

func TestArchitecture_HypothesisEvolutionEdges(t *testing.T) {
	parent := Hypothesis{ID: "parent1", Claim: "Parent claim", FalsifiabilityCondition: "FC"}
	child := Hypothesis{ID: "child1", ParentID: "parent1", Claim: "Child claim", FalsifiabilityCondition: "FC2"}
	hypotheses := []Hypothesis{parent, child}

	graph := BuildReasoningGraph("evolution test", hypotheses, nil)
	addHypothesisEvolutionEdges(graph, hypotheses)

	parentNodeID := hypothesisNodeID(parent)
	childNodeID := hypothesisNodeID(child)
	found := false
	for _, edge := range graph.Edges {
		if edge.From == parentNodeID && edge.To == childNodeID && edge.Label == "branched_into" {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected branched_into edge from parent to child")
}

func TestArchitecture_IdentifyUnderSupportedHypotheses(t *testing.T) {
	hypotheses := []Hypothesis{
		{Claim: "Well supported", FalsifiabilityCondition: "FC1"},
		{Claim: "Poorly supported", FalsifiabilityCondition: "FC2"},
	}
	ev1 := EvidenceFinding{ID: "ev1", Claim: "Ev 1", SourceID: "s1", PaperTitle: "P1", Confidence: 0.8}
	ev2 := EvidenceFinding{ID: "ev2", Claim: "Ev 2", SourceID: "s2", PaperTitle: "P2", Confidence: 0.7}
	ev3 := EvidenceFinding{ID: "ev3", Claim: "Ev 3", SourceID: "s3", PaperTitle: "P3", Confidence: 0.75}

	hypotheses[0].Evidence = []*EvidenceFinding{&ev1, &ev2, &ev3}

	graph := BuildReasoningGraph("test", hypotheses, []EvidenceFinding{ev1, ev2, ev3})
	underSupported := IdentifyUnderSupportedHypotheses(graph, 2)

	poorlyID := hypothesisNodeID(hypotheses[1])
	assert.Contains(t, underSupported, poorlyID)
}

func TestArchitecture_SuggestExplorationTargets(t *testing.T) {
	hypotheses := []Hypothesis{
		{Claim: "Confident", FalsifiabilityCondition: "FC1", ConfidenceScore: 0.9},
		{Claim: "Uncertain", FalsifiabilityCondition: "FC2", ConfidenceScore: 0.3},
		{Claim: "Terminated", FalsifiabilityCondition: "FC3", ConfidenceScore: 0.1, IsTerminated: true},
	}

	graph := BuildReasoningGraph("test", hypotheses, nil)
	targets := SuggestExplorationTargets(graph, hypotheses, 5)

	for _, tgt := range targets {
		assert.False(t, tgt.IsTerminated, "Terminated hypotheses should not be targets")
	}
	if len(targets) > 0 {
		assert.LessOrEqual(t, targets[0].ConfidenceScore, targets[len(targets)-1].ConfidenceScore+0.01,
			"Targets should be sorted by uncertainty descending")
	}
}
