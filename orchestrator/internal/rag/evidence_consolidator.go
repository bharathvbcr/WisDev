package rag

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ConsolidatedDossier is the cross-iteration output of EvidenceConsolidator.
// It deduplicates claims semantically, scores contradictions by severity, and
// surfaces gaps that the planner should treat as hard blockers.
type ConsolidatedDossier struct {
	Unique            []UniqueClaim         `json:"unique"`
	Contradictions    []ScoredContradiction `json:"contradictions"`
	Gaps              []string              `json:"gaps"`
	DuplicatesDropped int                   `json:"duplicates_dropped"`
	// HardBlockers lists contradiction IDs with severity == "high".
	// The executor must not proceed to synthesis until these are resolved.
	HardBlockers []string `json:"hard_blockers"`
}

// UniqueClaim is a deduplicated claim with provenance from one or more sources.
type UniqueClaim struct {
	Canonical  string   `json:"canonical"`  // representative claim text
	SourceIDs  []string `json:"source_ids"` // all papers that support this claim
	Confidence float64  `json:"confidence"` // max confidence among supporting sources
}

// ScoredContradiction pairs two claims that conflict, with a severity rating
// and an escalation signal for the planner.
type ScoredContradiction struct {
	ID          string `json:"id"`
	ClaimA      string `json:"claim_a"`
	ClaimB      string `json:"claim_b"`
	SourceA     string `json:"source_a"`
	SourceB     string `json:"source_b"`
	// Severity is "low" | "medium" | "high".
	Severity string `json:"severity"`
	Explanation string `json:"explanation"`
	// NeedsHumanArbitration is true when severity is high and claims come from
	// independent sources, meaning automated resolution is unreliable.
	NeedsHumanArbitration bool `json:"needs_human_arbitration"`
}

// RawEvidenceItem is the input type for Consolidate. Mirrors the fields used
// by AutonomousLoop.assembleDossier without coupling the consolidator to the
// wisdev package (which would create a circular import).
type RawEvidenceItem struct {
	Claim      string
	SourceID   string
	Confidence float64
}

// EvidenceConsolidator merges evidence items gathered across multiple search
// iterations, deduplicates semantically similar claims, and escalates
// contradictions with structured severity scores.
type EvidenceConsolidator struct{}

// NewEvidenceConsolidator constructs a consolidator. Stateless; safe to reuse.
func NewEvidenceConsolidator() *EvidenceConsolidator {
	return &EvidenceConsolidator{}
}

// dedupeThreshold is the minimum Jaccard similarity at which two claims are
// considered semantic duplicates. Tuned for academic claim texts.
const dedupeThreshold = 0.55

// consolidatorNegTokens and consolidatorPosTokens are used for semantic
// contradiction detection without importing regexp at this level.
var consolidatorNegTokens = []string{
	"no ", "not ", "none", "failed", "lack", "absent", "without",
	"ineffective", "does not", "did not", "no evidence",
}
var consolidatorPosTokens = []string{
	"significant", "positive", "effective", "beneficial", "improved",
	"superior", "demonstrates", "confirms", "supports",
}

// consolidatorTokenize extracts lowercased content tokens from s, filtering
// stop words. Mirrors EvidenceGate.tokenize without repeating the regex var.
func consolidatorTokenize(s string) map[string]bool {
	out := make(map[string]bool)
	lower := strings.ToLower(s)
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tok := cur.String()
			cur.Reset()
			if len(tok) > 2 {
				if _, isStop := stopWords[tok]; !isStop {
					out[tok] = true
				}
			}
		}
	}
	for _, ch := range lower {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			cur.WriteRune(ch)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// tokenJaccard computes the Jaccard similarity between the token sets of two
// strings. Returns a value in [0, 1].
func tokenJaccard(a, b string) float64 {
	ta := consolidatorTokenize(a)
	tb := consolidatorTokenize(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 1.0
	}
	intersection := 0
	for k := range ta {
		if tb[k] {
			intersection++
		}
	}
	union := len(ta) + len(tb) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func containsAny(s string, tokens []string) bool {
	lower := strings.ToLower(s)
	for _, t := range tokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// contradictionSeverity scores a contradiction pair based on:
//  1. Direct semantic opposition (negation vs. positive affirmation)
//  2. Source independence (same paper cannot contradict itself)
//  3. Confidence delta (large spread weakens the weaker claim further)
func contradictionSeverity(a, b UniqueClaim) string {
	aHasNeg := containsAny(a.Canonical, consolidatorNegTokens)
	bHasNeg := containsAny(b.Canonical, consolidatorNegTokens)
	aHasPos := containsAny(a.Canonical, consolidatorPosTokens)
	bHasPos := containsAny(b.Canonical, consolidatorPosTokens)

	directConflict := (aHasNeg && bHasPos) || (bHasNeg && aHasPos)
	delta := math.Abs(a.Confidence - b.Confidence)

	sourcesOverlap := false
	for _, sa := range a.SourceIDs {
		for _, sb := range b.SourceIDs {
			if sa == sb {
				sourcesOverlap = true
				break
			}
		}
		if sourcesOverlap {
			break
		}
	}

	switch {
	case directConflict && !sourcesOverlap:
		return "high"
	case directConflict && sourcesOverlap:
		return "medium"
	case !directConflict && delta > 0.4:
		return "medium"
	default:
		return "low"
	}
}

// Consolidate merges RawEvidenceItems from multiple gate runs into a single
// deduplicated dossier with scored, escalated contradictions.
//
// Call this after assembling evidence across all loop iterations, passing the
// union of all EvidenceItem slices.
func (c *EvidenceConsolidator) Consolidate(items []RawEvidenceItem) ConsolidatedDossier {
	if len(items) == 0 {
		return ConsolidatedDossier{}
	}

	// ── Step 1: greedy Jaccard clustering → unique claim groups ──────────────
	type member struct {
		text       string
		sourceID   string
		confidence float64
	}
	groups := make([][]member, 0, len(items))

	for _, item := range items {
		merged := false
		for gi := range groups {
			if tokenJaccard(item.Claim, groups[gi][0].text) >= dedupeThreshold {
				groups[gi] = append(groups[gi], member{item.Claim, item.SourceID, item.Confidence})
				merged = true
				break
			}
		}
		if !merged {
			groups = append(groups, []member{{item.Claim, item.SourceID, item.Confidence}})
		}
	}

	duplicatesDropped := len(items) - len(groups)

	// ── Step 2: build UniqueClaim per group ───────────────────────────────────
	unique := make([]UniqueClaim, 0, len(groups))
	for _, g := range groups {
		var bestText string
		var maxConf float64
		sourceSeen := make(map[string]bool)
		var sourceIDs []string

		for _, m := range g {
			if m.confidence > maxConf {
				maxConf = m.confidence
				bestText = m.text
			}
			if !sourceSeen[m.sourceID] {
				sourceIDs = append(sourceIDs, m.sourceID)
				sourceSeen[m.sourceID] = true
			}
		}
		unique = append(unique, UniqueClaim{
			Canonical:  bestText,
			SourceIDs:  sourceIDs,
			Confidence: maxConf,
		})
	}

	// Sort by confidence descending for deterministic output.
	sort.Slice(unique, func(i, j int) bool {
		return unique[i].Confidence > unique[j].Confidence
	})

	// ── Step 3: detect and score contradictions ────────────────────────────────
	contradictions := make([]ScoredContradiction, 0)
	hardBlockers := make([]string, 0)

	for i := 0; i < len(unique); i++ {
		for j := i + 1; j < len(unique); j++ {
			a, b := unique[i], unique[j]

			// Require enough topic overlap to be a meaningful contradiction.
			if tokenJaccard(a.Canonical, b.Canonical) < 0.15 {
				continue
			}

			// Require actual semantic opposition (negation vs. affirmation).
			aHasNeg := containsAny(a.Canonical, consolidatorNegTokens)
			bHasNeg := containsAny(b.Canonical, consolidatorNegTokens)
			aHasPos := containsAny(a.Canonical, consolidatorPosTokens)
			bHasPos := containsAny(b.Canonical, consolidatorPosTokens)
			if !((aHasNeg && bHasPos) || (bHasNeg && aHasPos)) {
				continue
			}

			sev := contradictionSeverity(a, b)
			id := fmt.Sprintf("contra_%d_%d", i, j)
			sourceA := firstSourceOrEmpty(a.SourceIDs)
			sourceB := firstSourceOrEmpty(b.SourceIDs)

			pair := ScoredContradiction{
				ID:                    id,
				ClaimA:                a.Canonical,
				ClaimB:                b.Canonical,
				SourceA:               sourceA,
				SourceB:               sourceB,
				Severity:              sev,
				Explanation:           buildContradictionExplanation(a, b, sev),
				NeedsHumanArbitration: sev == "high",
			}
			contradictions = append(contradictions, pair)
			if sev == "high" {
				hardBlockers = append(hardBlockers, id)
			}
		}
	}

	// ── Step 4: identify knowledge gaps ──────────────────────────────────────
	gaps := make([]string, 0)
	for _, u := range unique {
		if u.Confidence < 0.4 && len(u.SourceIDs) < 2 {
			gaps = append(gaps, fmt.Sprintf(
				"Low-confidence claim needs corroboration: %s",
				truncateRune(u.Canonical, 120),
			))
		}
	}

	return ConsolidatedDossier{
		Unique:            unique,
		Contradictions:    contradictions,
		Gaps:              gaps,
		DuplicatesDropped: duplicatesDropped,
		HardBlockers:      hardBlockers,
	}
}

func buildContradictionExplanation(a, b UniqueClaim, severity string) string {
	switch severity {
	case "high":
		return fmt.Sprintf(
			"Direct conflict from independent sources (conf %.2f vs %.2f). "+
				"Human arbitration required before synthesis.",
			a.Confidence, b.Confidence,
		)
	case "medium":
		return fmt.Sprintf(
			"Probable conflict (conf %.2f vs %.2f). "+
				"Verify whether claims address the same population/intervention.",
			a.Confidence, b.Confidence,
		)
	default:
		return fmt.Sprintf(
			"Weak tension (conf %.2f vs %.2f). Monitor but may proceed.",
			a.Confidence, b.Confidence,
		)
	}
}

func firstSourceOrEmpty(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func truncateRune(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
