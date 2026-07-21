package wisdev

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
)

func TestAttachUncitedSpecificCitations(t *testing.T) {
	ordered := []evidence.EvidencePacket{
		{PacketID: "p1", ClaimText: "The LiVersa platform integrates a liver-disease chat interface.",
			EvidenceSpans: []evidence.EvidenceSpan{{Snippet: "LiVersa is a closed-loop liver chat tool."}}},
		{PacketID: "p2", ClaimText: "A MIMIC-based benchmark used 2,400 patient cases to stress-test models.",
			EvidenceSpans: []evidence.EvidenceSpan{{Snippet: "We evaluated across 2,400 patient cases."}}},
		{PacketID: "p3", ClaimText: "Grounding reduced unsupported generation.", EvidenceSpans: nil},
	}

	// Uncited sentences with a distinctive specific that uniquely matches one packet.
	in := "The LiVersa platform offers a closed-loop interface. A benchmark used 2,400 patient cases to evaluate models. Grounding improves reliability [3]."
	out := attachUncitedSpecificCitations(in, ordered)
	if !strings.Contains(out, "LiVersa platform offers a closed-loop interface [1].") {
		t.Errorf("LiVersa sentence not cited to p1:\n%s", out)
	}
	if !strings.Contains(out, "2,400 patient cases to evaluate models [2].") {
		t.Errorf("2,400 sentence not cited to p2:\n%s", out)
	}
	// Already-cited sentence is untouched (no double citation).
	if strings.Count(out, "[3]") != 1 {
		t.Errorf("already-cited sentence altered:\n%s", out)
	}

	// No distinctive specific -> unchanged.
	plain := "Retrieval-augmented generation grounds outputs in retrieved evidence."
	if got := attachUncitedSpecificCitations(plain, ordered); got != plain {
		t.Errorf("plain sentence altered: %q", got)
	}

	// Ambiguous specific (in two packets) -> not cited.
	amb := []evidence.EvidencePacket{
		{PacketID: "a", ClaimText: "Model A reached 314 points."},
		{PacketID: "b", ClaimText: "Model B also reached 314 points."},
	}
	ambIn := "The systems converged at 314 points."
	if got := attachUncitedSpecificCitations(ambIn, amb); got != ambIn {
		t.Errorf("ambiguous specific should not be cited: %q", got)
	}

	// Number + study-unit ("30 studies") -> cite the survey packet that has both.
	survey := []evidence.EvidencePacket{
		{PacketID: "s1", ClaimText: "A survey reviewed 30 studies and categorized RAG approaches into tiers.",
			EvidenceSpans: []evidence.EvidenceSpan{{Snippet: "We synthesized 30 studies on retrieval augmentation."}}},
		{PacketID: "s2", ClaimText: "Grounding lowers hallucination rates.", EvidenceSpans: nil},
	}
	nuIn := "A systematic literature review of 30 studies characterizes RAG into naive and modular tiers."
	if got := attachUncitedSpecificCitations(nuIn, survey); !strings.Contains(got, "tiers [1].") {
		t.Errorf("'30 studies' not cited to the survey packet:\n%s", got)
	}
	// Percentages / durations are NOT number+unit citations (no study-unit noun).
	for _, fp := range []string{
		"The system achieved a 30% improvement over the baseline.",
		"Inference completed in 250 milliseconds on average.",
	} {
		if got := attachUncitedSpecificCitations(fp, survey); got != fp {
			t.Errorf("percentage/duration wrongly cited: %q -> %q", fp, got)
		}
	}
}
