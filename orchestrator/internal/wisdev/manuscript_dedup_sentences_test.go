package wisdev

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

func TestDedupeCrossSectionSentences(t *testing.T) {
	p := &ManuscriptPipeline{}
	dup := "Clinical documentation is a primary driver of physician burnout and compromised patient safety [1]."
	sections := []SectionDraftArtifact{
		{SectionID: "abstract", Content: dup}, // abstract restates -> must be untouched
		{SectionID: "introduction", Content: dup + " Retrieval-augmented generation grounds outputs in evidence [2]."},
		// Literature Review near-verbatim restates the burnout sentence + has a distinct claim.
		{SectionID: "literature_review", Content: "Clinical documentation remains a primary driver of physician burnout and compromised patient safety [1]. Grounding reduces hallucination across clinical benchmarks [3]."},
	}
	out := p.dedupeCrossSectionSentences(sections, evidence.ManuscriptRawMaterialSet{})

	// Abstract untouched.
	if out[0].Content != dup {
		t.Errorf("abstract must not be deduped: %q", out[0].Content)
	}
	// Introduction keeps the first occurrence.
	if !strings.Contains(out[1].Content, "primary driver of physician burnout") {
		t.Errorf("introduction lost the first occurrence: %q", out[1].Content)
	}
	// Literature Review drops the near-verbatim restatement but keeps its distinct claim.
	if strings.Contains(out[2].Content, "primary driver of physician burnout") {
		t.Errorf("literature_review kept a cross-section restatement: %q", out[2].Content)
	}
	if !strings.Contains(out[2].Content, "Grounding reduces hallucination") {
		t.Errorf("literature_review dropped its distinct claim: %q", out[2].Content)
	}
}

func TestDedupeCrossSectionSentencesKeepsDistinctClaims(t *testing.T) {
	p := &ManuscriptPipeline{}
	sections := []SectionDraftArtifact{
		{SectionID: "introduction", Content: "Retrieval-augmented generation reduces hallucination by grounding outputs in retrieved clinical evidence [1]."},
		// Same topic, DIFFERENT claim — must NOT be dropped (low keyword Jaccard).
		{SectionID: "discussion", Content: "Clinician trust depends on transparent, citation-backed responses from the system [2]."},
	}
	out := p.dedupeCrossSectionSentences(sections, evidence.ManuscriptRawMaterialSet{})
	if !strings.Contains(out[1].Content, "Clinician trust depends on transparent") {
		t.Errorf("distinct on-topic claim was wrongly deduped: %q", out[1].Content)
	}
}
