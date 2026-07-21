package wisdev

import "time"

// ClaimProvenanceEntry traces a single claim back through the full derivation
// chain: which subqueries surfaced it, which evidence IDs support it, and which
// source papers back those evidence IDs.
type ClaimProvenanceEntry struct {
	// ClaimID is the EvidenceFinding.ID of the claim-bearing evidence item.
	ClaimID string `json:"claimId"`
	// ClaimText is the natural-language claim asserted by this entry.
	ClaimText string `json:"claimText"`
	// EvidenceIDs lists all EvidenceFinding.IDs that support or constitute this
	// claim (typically one, occasionally coalesced duplicates).
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
	// SubqueryOrigins are the executed subqueries whose retrieval surfaced this
	// claim, linked via EvidenceFinding.ProvenanceChain[].QueryID.
	SubqueryOrigins []string `json:"subqueryOrigins,omitempty"`
	// SourceIDs are the paper / source IDs behind the supporting evidence.
	SourceIDs []string `json:"sourceIds,omitempty"`
	// Confidence is the calibrated confidence inherited from the evidence finding.
	Confidence float64 `json:"confidence"`
	// WorkerRole identifies which specialist worker produced this claim.
	WorkerRole string `json:"workerRole,omitempty"`
}

// ResearchLineage is a first-class structured record of the full derivation
// chain from the user's original query through:
//
//	UserQuery → Decomposition → Subquery → Retrieval → Evidence → Claim
//
// It is attached to LoopResult.Lineage and populated during finalization so
// that every downstream consumer (API, frontend, audit) can traverse the exact
// path that produced any claim in the final answer.
type ResearchLineage struct {
	// UserQuery is the original, normalized query submitted by the user.
	UserQuery string `json:"userQuery"`
	// Decomposition lists the planned subqueries produced during pre-loop
	// specialist dispatch (ResearchSessionState.PlannedQueries).
	Decomposition []string `json:"decomposition,omitempty"`
	// ExecutedSubqueries are the subqueries that were actually executed by the
	// loop and specialists (LoopResult.ExecutedQueries).
	ExecutedSubqueries []string `json:"executedSubqueries,omitempty"`
	// SubqueryToEvidence maps executed subquery → []EvidenceFinding.ID.
	// Built from EvidenceFinding.ProvenanceChain[].QueryID links.
	SubqueryToEvidence map[string][]string `json:"subqueryToEvidence,omitempty"`
	// EvidenceToSource maps EvidenceFinding.ID → SourceID (paper).
	EvidenceToSource map[string]string `json:"evidenceToSource,omitempty"`
	// EvidenceToClaim maps EvidenceFinding.ID → []claim text.
	EvidenceToClaim map[string][]string `json:"evidenceToClaim,omitempty"`
	// ClaimProvenance is the per-claim full lineage, one entry per unique
	// EvidenceFinding that carries a non-empty claim.
	ClaimProvenance []ClaimProvenanceEntry `json:"claimProvenance,omitempty"`
	// WorkerContributions maps specialist role name → []EvidenceFinding.ID
	// contributed by that worker's evidence set.
	WorkerContributions map[string][]string `json:"workerContributions,omitempty"`

	// Q4.1: Sentence-level provenance
	ClaimToSentence    map[string][]string `json:"claimToSentence,omitempty"`    // ClaimID → sentence texts
	SentenceToEvidence map[int][]string    `json:"sentenceToEvidence,omitempty"` // Sentence index → evidence IDs

	// BuilderVersion is a monotone integer bumped when the lineage builder logic
	// changes in a backward-incompatible way, to allow clients to detect staleness.
	BuilderVersion string `json:"builderVersion"`
	// BuiltAtMs is the Unix millisecond timestamp when this lineage was built.
	BuiltAtMs int64 `json:"builtAtMs"`
}

// buildResearchLineage constructs the full ResearchLineage from the finalized
// ResearchSessionState and LoopResult.  It is called once per research session
// during finalization and its output is safe to serialize directly to JSON.
func buildResearchLineage(state *ResearchSessionState, loopResult *LoopResult) *ResearchLineage {
	if state == nil || loopResult == nil {
		return nil
	}

	allEvidence := loopResult.Evidence

	subqueryToEvidence := make(map[string][]string)
	evidenceToSource := make(map[string]string, len(allEvidence))
	evidenceToClaim := make(map[string][]string, len(allEvidence))
	workerContributions := make(map[string][]string)

	for _, ev := range allEvidence {
		if ev.ID == "" {
			continue
		}
		if ev.SourceID != "" {
			evidenceToSource[ev.ID] = ev.SourceID
		}
		if ev.Claim != "" {
			evidenceToClaim[ev.ID] = appendUniqueStr(evidenceToClaim[ev.ID], ev.Claim)
		}
		// Map to executing subqueries via the existing ProvenanceChain on each finding.
		for _, pe := range ev.ProvenanceChain {
			if pe.QueryID != "" {
				subqueryToEvidence[pe.QueryID] = appendUniqueStr(subqueryToEvidence[pe.QueryID], ev.ID)
			}
		}
	}

	// Worker contributions: each specialist's evidence slice.
	for _, w := range state.Workers {
		role := string(w.Role)
		for _, ev := range w.Evidence {
			if ev.ID != "" {
				workerContributions[role] = appendUniqueStr(workerContributions[role], ev.ID)
			}
		}
	}

	claimProvenance := buildClaimProvenanceEntries(allEvidence, state, subqueryToEvidence)

	lineage := &ResearchLineage{
		UserQuery:           state.Query,
		Decomposition:       append([]string(nil), state.PlannedQueries...),
		ExecutedSubqueries:  append([]string(nil), loopResult.ExecutedQueries...),
		SubqueryToEvidence:  subqueryToEvidence,
		EvidenceToSource:    evidenceToSource,
		EvidenceToClaim:     evidenceToClaim,
		ClaimProvenance:     claimProvenance,
		WorkerContributions: workerContributions,
		BuilderVersion:      "1",
		BuiltAtMs:           time.Now().UnixMilli(),
	}

	// Q4.1: Sentence-level provenance
	if loopResult.StructuredAnswer != nil {
		lineage.SentenceToEvidence = make(map[int][]string)
		sentenceIdx := 0
		for _, section := range loopResult.StructuredAnswer.Sections {
			for _, sent := range section.Sentences {
				if len(sent.EvidenceIDs) > 0 {
					lineage.SentenceToEvidence[sentenceIdx] = sent.EvidenceIDs
				}
				sentenceIdx++
			}
		}
	}

	return lineage
}

// buildClaimProvenanceEntries produces one ClaimProvenanceEntry per unique
// EvidenceFinding that carries a non-empty claim, enriched with the subqueries
// that originated the finding and the worker role that produced it.
func buildClaimProvenanceEntries(
	evidence []EvidenceFinding,
	state *ResearchSessionState,
	subqueryToEvidence map[string][]string,
) []ClaimProvenanceEntry {
	// Build reverse index: evidenceID → originating subqueries.
	evidenceToSubqueries := make(map[string][]string, len(evidence))
	for q, evIDs := range subqueryToEvidence {
		for _, id := range evIDs {
			evidenceToSubqueries[id] = appendUniqueStr(evidenceToSubqueries[id], q)
		}
	}

	// Build reverse index: evidenceID → worker role.
	evidenceToRole := make(map[string]string, len(evidence))
	for _, w := range state.Workers {
		role := string(w.Role)
		for _, ev := range w.Evidence {
			if ev.ID != "" {
				evidenceToRole[ev.ID] = role
			}
		}
	}

	seen := make(map[string]bool, len(evidence))
	entries := make([]ClaimProvenanceEntry, 0, len(evidence))
	for _, ev := range evidence {
		if ev.Claim == "" || ev.ID == "" || seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		sourceIDs := make([]string, 0, 1)
		if ev.SourceID != "" {
			sourceIDs = append(sourceIDs, ev.SourceID)
		}
		entries = append(entries, ClaimProvenanceEntry{
			ClaimID:         ev.ID,
			ClaimText:       ev.Claim,
			EvidenceIDs:     []string{ev.ID},
			SubqueryOrigins: evidenceToSubqueries[ev.ID],
			SourceIDs:       sourceIDs,
			Confidence:      ev.Confidence,
			WorkerRole:      evidenceToRole[ev.ID],
		})
	}
	return entries
}

// appendUniqueStr appends s to slice only if it is not already present.
func appendUniqueStr(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
