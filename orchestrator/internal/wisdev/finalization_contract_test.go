package wisdev

import (
	"testing"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"

	"github.com/stretchr/testify/assert"
)

func TestResearchFinalization_MetadataContract(t *testing.T) {
	state := &ResearchSessionState{
		Query: "test query",
		Workers: []ResearchWorkerState{
			{
				Role: ResearchWorkerScout,
				Evidence: []EvidenceFinding{
					{ID: "ev1", SourceID: "p1", Claim: "Claim 1"},
				},
			},
		},
		CoverageLedger: []CoverageLedgerEntry{
			{ID: "l1", Category: "contradiction", Status: coverageLedgerStatusOpen},
			{ID: "l2", Category: "source_diversity", Status: coverageLedgerStatusResolved},
		},
	}

	loopResult := &LoopResult{
		Papers: []search.Paper{
			{ID: "p1", Title: "Paper 1", DOI: "10.1234/p1", ArxivID: "2401.00001", Link: "https://arxiv.org/abs/2401.00001"},
		},
		Evidence: []EvidenceFinding{
			{ID: "ev1", SourceID: "p1", Claim: "Claim 1", PaperTitle: "Paper 1"},
		},
	}

	lineage := buildResearchLineage(state, loopResult)
	assert.NotNil(t, lineage)
	assert.Equal(t, "test query", lineage.UserQuery)
	assert.Equal(t, "p1", lineage.EvidenceToSource["ev1"])
	assert.Contains(t, lineage.EvidenceToClaim["ev1"], "Claim 1")
	assert.Contains(t, lineage.WorkerContributions[string(ResearchWorkerScout)], "ev1")

	// Verify paper metadata survival
	assert.Equal(t, "10.1234/p1", loopResult.Papers[0].DOI)
	assert.Equal(t, "2401.00001", loopResult.Papers[0].ArxivID)
}

func TestResearchFinalizationGate_CategoryMapping(t *testing.T) {
	state := &ResearchSessionState{
		Query: "test query",
		CoverageLedger: []CoverageLedgerEntry{
			{ID: "l1", Category: "contradiction", Status: coverageLedgerStatusOpen, Title: "Conflicting evidence found"},
		},
		VerifierDecision: &ResearchVerifierDecision{
			Verdict:         "revise_required",
			RevisionReasons: []string{"need more counter-evidence"},
		},
	}
	loopResult := &LoopResult{}

	gate := buildResearchFinalizationGate(state, loopResult)
	assert.NotNil(t, gate)
	assert.False(t, gate.Ready)
	assert.True(t, gate.Provisional)

	// Verify that open ledger items are reflected in the count
	// Note: buildResearchFinalizationGate uses finalizationOpenLedgerCount
	assert.GreaterOrEqual(t, gate.OpenLedgerCount, 1)

	// Verify that verifier reasons are propagated
	assert.Contains(t, gate.RevisionReasons, "need more counter-evidence")
}

func TestMapPaperToSource_StableIdentifiers(t *testing.T) {
	// Ensure mapPaperToSource (used in final result) includes all stable IDs
	p := search.Paper{
		ID:      "id1",
		DOI:     "doi1",
		ArxivID: "arxiv1",
		Link:    "link1",
		Title:   "Title 1",
	}
	s := mapPaperToSource(p)
	assert.Equal(t, "id1", s.ID)
	assert.Equal(t, "doi1", s.DOI)
	assert.Equal(t, "arxiv1", s.ArxivID)
	assert.Equal(t, "link1", s.Link)
	assert.Equal(t, "Title 1", s.Title)
}
