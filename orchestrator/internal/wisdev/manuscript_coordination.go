package wisdev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/evidence"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/resilience"
)

// entailmentBlockThreshold is the EntailmentScore at or below which a fact-checked
// paragraph is treated as not entailed by its cited sources (a BLOCKING downgrade).
// The score-bearing fact-check sets 0.0 for a sentence cited to nothing and 0.5 for
// a partially-entailed one, so 0.4 blocks the former and merely flags the latter.
const entailmentBlockThreshold = 0.4

// entailmentIssuePrefix marks an UnresolvedIssue produced by the entailment
// fact-check so the aggressive revise can surface exactly those flags.
const entailmentIssuePrefix = "Sentence not entailed by cited sources"

// OwnershipConcept is a salient named statistic / study / taxonomy / worked example
// that the cross-section coordinator assigns to exactly ONE owning section, so it is
// developed in full in one place rather than repeated across sections.
type OwnershipConcept struct {
	ConceptLabel    string   `json:"conceptLabel"`
	OwningSectionID string   `json:"owningSectionId"`
	PacketIDs       []string `json:"packetIds,omitempty"`
	Rationale       string   `json:"rationale,omitempty"`
}

type conceptAssignment struct {
	ConceptLabel    string   `json:"concept_label"`
	OwningSectionID string   `json:"owning_section_id"`
	PacketIDs       []string `json:"packet_ids"`
	Rationale       string   `json:"rationale"`
}

type coordinationResult struct {
	Assignments []conceptAssignment `json:"assignments"`
}

type flaggedSentence struct {
	SentenceText      string   `json:"sentence_text"`
	Issue             string   `json:"issue"`
	EntailedPacketIDs []string `json:"entailed_packet_ids"`
	UnentailedReason  string   `json:"unentailed_reason"`
}

type factCheckResult struct {
	FlaggedSentences []flaggedSentence `json:"flagged_sentences"`
}

var entailmentCitationToken = regexp.MustCompile(`\[[^\]]*\]`)
var nonMatchChars = regexp.MustCompile(`[^a-z0-9]+`)

// manuscriptSidecarRetryPolicy bounds EVERY manuscript sidecar POST: at most 3
// attempts with exponential backoff + jitter between them. A var (not a const)
// so tests can shrink the delays; production code must not mutate it.
var manuscriptSidecarRetryPolicy = resilience.RetryPolicy{
	MaxAttempts: 3,
	BaseDelay:   250 * time.Millisecond,
	MaxDelay:    2 * time.Second,
}

// SetManuscriptSidecarRetryPolicyForTest overrides manuscriptSidecarRetryPolicy for
// the duration of a test and returns a restore func to hand to t.Cleanup. Test-only:
// it lets cross-package integration tests (e.g. the api docgen job, which runs the
// real pipeline against an offline sidecar) shrink the backoff so their scaffold
// fallback completes inside the test deadline instead of waiting on production
// retries. Production code must not call it.
func SetManuscriptSidecarRetryPolicyForTest(p resilience.RetryPolicy) func() {
	prev := manuscriptSidecarRetryPolicy
	manuscriptSidecarRetryPolicy = p
	return func() { manuscriptSidecarRetryPolicy = prev }
}

// postManuscriptJSON POSTs payload to a sidecar manuscript endpoint and decodes the
// JSON body into out. Offline-guarded like the other post* helpers. Transient
// failures (network errors, timeouts, 5xx responses) are retried under
// manuscriptSidecarRetryPolicy; 4xx responses and malformed bodies are terminal
// and never retried. Exhausted retries surface the last error unchanged, so every
// caller keeps its existing grounded-scaffold fallback.
func (p *ManuscriptPipeline) postManuscriptJSON(ctx context.Context, path string, payload map[string]any, out any) error {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return fmt.Errorf("python sidecar base URL is not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return resilience.Retry(ctx, "manuscript.sidecar_post:"+path, manuscriptSidecarRetryPolicy, func(ctx context.Context) (string, bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.pythonBaseURL+path, bytes.NewReader(body))
		if err != nil {
			return "request_error", false, err
		}
		p.setSidecarHeaders(req)
		resp, err := p.sidecarHTTPClient().Do(req)
		if err != nil {
			return "transport_error", true, err // network error / timeout: retryable
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			statusErr := fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
			if resp.StatusCode >= http.StatusInternalServerError {
				return "http_5xx", true, statusErr // transient upstream failure: retryable
			}
			return "http_4xx", false, statusErr // caller/payload problem: never retried
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return "decode_error", false, err
		}
		return "", false, nil
	})
}

// ---- Feature A: concept-level cross-section coordination ----

// fetchCoordinationPlan asks the sidecar editor to assign each salient named
// stat/study/taxonomy/example to one owning section. Returns nil offline / on error,
// so writers fall back to today's prompt-only behavior.
func (p *ManuscriptPipeline) fetchCoordinationPlan(ctx context.Context, blueprint ManuscriptBlueprint, raw evidence.ManuscriptRawMaterialSet) []OwnershipConcept {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return nil
	}
	roster := make([]map[string]any, 0, len(blueprint.Sections))
	for _, brief := range blueprint.Sections {
		roster = append(roster, map[string]any{
			"section_id":       brief.SectionID,
			"title":            brief.Title,
			"claim_packet_ids": brief.RequiredClaimPacketIDs,
		})
	}
	var result coordinationResult
	if err := p.postManuscriptJSON(ctx, "/wisdev/manuscript/coordinate", map[string]any{
		"query":         blueprint.Query,
		"thesis":        blueprint.Thesis,
		"sections":      roster,
		"claim_packets": raw.ClaimPackets,
	}, &result); err != nil {
		slog.Warn("manuscript coordination plan unavailable — writers use default behavior",
			"component", manuscriptLogComponent, "operation", "coordination_plan",
			"sections", len(roster), "error", err.Error())
		return nil
	}
	concepts := make([]OwnershipConcept, 0, len(result.Assignments))
	for _, a := range result.Assignments {
		concepts = append(concepts, OwnershipConcept{
			ConceptLabel:    a.ConceptLabel,
			OwningSectionID: a.OwningSectionID,
			PacketIDs:       a.PacketIDs,
			Rationale:       a.Rationale,
		})
	}
	return dedupeOwnershipConcepts(concepts, blueprint.SectionOrder)
}

// dedupeOwnershipConcepts keeps the first owner of each concept label and drops any
// assignment whose owning section is not in the blueprint's section order.
func dedupeOwnershipConcepts(concepts []OwnershipConcept, sectionOrder []string) []OwnershipConcept {
	known := make(map[string]bool, len(sectionOrder))
	for _, id := range sectionOrder {
		known[id] = true
	}
	seen := make(map[string]bool, len(concepts))
	out := make([]OwnershipConcept, 0, len(concepts))
	for _, c := range concepts {
		label := strings.TrimSpace(c.ConceptLabel)
		owner := strings.TrimSpace(c.OwningSectionID)
		if label == "" || owner == "" {
			continue
		}
		if len(known) > 0 && !known[owner] {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// ownershipForSection splits concepts into those owned by sectionID and those owned
// by other sections.
func ownershipForSection(concepts []OwnershipConcept, sectionID string) (owned, foreign []OwnershipConcept) {
	for _, c := range concepts {
		if c.OwningSectionID == sectionID {
			owned = append(owned, c)
		} else {
			foreign = append(foreign, c)
		}
	}
	return owned, foreign
}

func joinConceptLabels(concepts []OwnershipConcept, limit int) string {
	labels := make([]string, 0, len(concepts))
	for _, c := range concepts {
		if label := strings.TrimSpace(c.ConceptLabel); label != "" {
			labels = append(labels, label)
		}
		if len(labels) >= limit {
			break
		}
	}
	return strings.Join(labels, "; ")
}

// renderOwnershipForSection builds the writer-prompt directive for a section: what it
// owns (develop fully) and what belongs to other sections (do not re-spell; a brief
// back-reference is fine — deliberately NOT "omit", so synthesis sections are not
// starved and the directive never conflicts with a hardcoded section goal).
func renderOwnershipForSection(sectionID string, concepts []OwnershipConcept) string {
	if len(concepts) == 0 {
		return ""
	}
	owned, foreign := ownershipForSection(concepts, sectionID)
	var b strings.Builder
	if len(owned) > 0 {
		b.WriteString("This section OWNS (spell out in full, exactly once): " + joinConceptLabels(owned, 8) + ".")
	}
	if len(foreign) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("Owned by OTHER sections — do NOT re-spell the full figure/example here; at most a one-clause back-reference that adds a new angle: " + joinConceptLabels(foreign, 8) + ".")
	}
	return b.String()
}

// mergeReviseFindings folds the canonical ownership plan and the section's entailment
// flags into the review-findings channel the aggressive revise already consumes, so
// the rewrite enforces them even when the adversarial review returned no findings.
func mergeReviseFindings(findings []string, section SectionDraftArtifact, concepts []OwnershipConcept) []string {
	out := append([]string{}, findings...)
	if directive := renderOwnershipForSection(section.SectionID, concepts); directive != "" {
		out = append(out, "EXCLUSIVE OWNERSHIP: "+directive)
	}
	for _, issue := range section.UnresolvedIssues {
		if strings.HasPrefix(issue, entailmentIssuePrefix) {
			out = append(out, issue)
		}
	}
	return uniqueStrings(out)
}

// ---- Feature B: prose-vs-source entailment fact-check ----

// factCheckSections runs the entailment fact-check over every section concurrently
// (bounded), then applies the flags. scoreBearing=false only records UnresolvedIssues
// (which survive the next paragraph rebuild and drive revise); scoreBearing=true also
// sets EntailmentChecked/Score so the next blind verify can honestly downgrade. No-op
// offline.
func (p *ManuscriptPipeline) factCheckSections(ctx context.Context, sections []SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet, scoreBearing bool) []SectionDraftArtifact {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return sections
	}
	results := make([]*factCheckResult, len(sections))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range sections {
		// The abstract is a deliberate summary of the body — fact-checking it against
		// its own packets would double-penalize legitimate restatement.
		if sections[i].SectionID == "abstract" {
			continue
		}
		if strings.TrimSpace(sections[i].Content) == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = p.factCheckSection(ctx, sections[i], raw)
		}(i)
	}
	wg.Wait()
	for i := range sections {
		if results[i] == nil {
			continue
		}
		sections[i] = applyEntailmentFlags(sections[i], results[i], scoreBearing)
	}
	return sections
}

func (p *ManuscriptPipeline) factCheckSection(ctx context.Context, section SectionDraftArtifact, raw evidence.ManuscriptRawMaterialSet) *factCheckResult {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return nil
	}
	content := strings.TrimSpace(section.Content)
	if content == "" {
		return nil
	}
	packets := claimPacketsByIDs(raw.ClaimPackets, section.ClaimPacketIDs)
	if len(packets) == 0 {
		return nil
	}
	var result factCheckResult
	if err := p.postManuscriptJSON(ctx, "/wisdev/manuscript/fact-check", map[string]any{
		"section_id":    section.SectionID,
		"content":       content,
		"claim_packets": packets,
	}, &result); err != nil {
		return nil
	}
	return &result
}

// applyEntailmentFlags turns each flagged sentence into an UnresolvedIssue and, when
// scoreBearing, lowers EntailmentScore on the matching paragraph (only a sentence
// cited to NOTHING — empty entailed_packet_ids — scores 0.0 and blocks; a partially
// entailed sentence scores 0.5 and merely flags). Matching strips [n] citation tokens
// from both sides so a model-echoed sentence still attaches to its paragraph.
func applyEntailmentFlags(section SectionDraftArtifact, result *factCheckResult, scoreBearing bool) SectionDraftArtifact {
	if result == nil || len(result.FlaggedSentences) == 0 {
		return section
	}
	issues := append([]string{}, section.UnresolvedIssues...)
	paraScore := make(map[int]float64)
	for _, flag := range result.FlaggedSentences {
		sentence := strings.TrimSpace(flag.SentenceText)
		if sentence == "" {
			continue
		}
		reason := strings.TrimSpace(flag.UnentailedReason)
		if reason == "" {
			reason = strings.TrimSpace(flag.Issue)
		}
		issues = append(issues, fmt.Sprintf("%s — fix or cut: %q (%s)", entailmentIssuePrefix, truncateForIssue(sentence), reason))
		if !scoreBearing {
			continue
		}
		score := 0.5
		if len(flag.EntailedPacketIDs) == 0 {
			score = 0.0
		}
		norm := normalizeForMatch(sentence)
		// Require enough specificity that the normalized sentence attaches to ONE
		// paragraph; a short/generic string could substring-match (and wrongly block)
		// several unrelated paragraphs.
		if len(strings.Fields(norm)) < 6 {
			continue
		}
		for pi := range section.Paragraphs {
			if strings.Contains(normalizeForMatch(section.Paragraphs[pi].Text), norm) {
				if cur, ok := paraScore[pi]; !ok || score < cur {
					paraScore[pi] = score
				}
				// A sentence belongs to a single paragraph — stop so one flag never
				// fans a blocking score out across multiple paragraphs.
				break
			}
		}
	}
	if scoreBearing {
		for pi, score := range paraScore {
			section.Paragraphs[pi].EntailmentChecked = true
			section.Paragraphs[pi].EntailmentScore = score
		}
	}
	section.UnresolvedIssues = uniqueStrings(issues)
	section.ReviewStatus = "needs_revision"
	return section
}

// anySectionHasEntailmentFlags reports whether any section carries an entailment flag,
// so the aggressive revise runs to address them even with zero adversarial-review
// findings.
func anySectionHasEntailmentFlags(sections []SectionDraftArtifact) bool {
	for _, section := range sections {
		for _, issue := range section.UnresolvedIssues {
			if strings.HasPrefix(issue, entailmentIssuePrefix) {
				return true
			}
		}
	}
	return false
}

// normalizeForMatch lowercases, removes whole [n] citation tokens (so the bracketed
// number does not survive as a digit), then drops all remaining non-alphanumerics and
// collapses whitespace — so a model-echoed sentence that dropped a citation marker or
// trailing punctuation still substring-matches its paragraph.
func normalizeForMatch(s string) string {
	s = strings.ToLower(s)
	s = entailmentCitationToken.ReplaceAllString(s, " ")
	s = nonMatchChars.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func truncateForIssue(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}
