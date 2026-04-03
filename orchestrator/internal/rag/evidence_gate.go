package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

const (
	minOverlapRatio = 0.12
	maxClaims       = 15
	// AIExtractionThreshold is the minimum synthesis text length (bytes) at which
	// the gate switches from heuristic claim extraction to AI-assisted extraction.
	// Exported so callers can report aiClaimExtractionUsed accurately.
	AIExtractionThreshold = 500
)

var (
	claimPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)[^.!?]*\b(?:found|show(?:ed|s)|demonstrat(?:ed|es)|suggest(?:ed|s)|indicat(?:ed|es)|reveal(?:ed|s)|confirm(?:ed|s)|establish(?:ed|es)|provid(?:ed|es)|report(?:ed|s)|observ(?:ed|es))\b[^.!?]*[.!?]`),
		regexp.MustCompile(`(?i)[^.!?]*\b(?:significantly|substantially|notably|importantly|critically|primarily|predominantly)\b[^.!?]*[.!?]`),
		regexp.MustCompile(`(?i)[^.!?]*\b(?:\d+(?:\.\d+)?%|\d+ (?:percent|times|fold))\b[^.!?]*[.!?]`),
		regexp.MustCompile(`(?i)[^.!?]*\b(?:p\s*[<=>]\s*0\.\d+|p-value|confidence interval|CI|OR|RR|HR)\b[^.!?]*[.!?]`),
	}
	contradictionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:however|but|although|despite|contrary|conversely|in contrast|on the other hand|nevertheless|yet|while)\b`),
	}
	stopWords = map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "of": {}, "in": {}, "on": {}, "to": {}, "for": {}, "with": {}, "and": {}, "or": {}, "but": {}, "not": {}, "this": {}, "that": {}, "these": {}, "those": {}, "it": {}, "its": {}, "they": {}, "them": {}, "their": {}, "we": {}, "us": {}, "our": {}, "you": {}, "your": {},
	}
)

type EvidenceGateResult struct {
	Claims             []string             `json:"claims"`
	LinkedClaims       []LinkedClaim        `json:"linked_claims"`
	UnlinkedClaims     []string             `json:"unlinked_claims"`
	Contradictions     []ClaimContradiction `json:"contradictions"`
	Verdict            string               `json:"verdict"` // "passed" | "provisional" | "failed"
	WarningPrefix      string               `json:"warning_prefix"`
	Message            string               `json:"message"`
	Checked            int                  `json:"checked"`
	PassedCount        int                  `json:"passed_count"`
	UnlinkedCount      int                  `json:"unlinked_count"`
	ContradictionCount int                  `json:"contradiction_count"`
}

type LinkedClaim struct {
	Claim        string  `json:"claim"`
	SourceID     string  `json:"source_id"`
	SourceTitle  string  `json:"source_title"`
	OverlapRatio float64 `json:"overlap_ratio"`
}

type ClaimContradiction struct {
	Claim    string `json:"claim"`
	SourceID string `json:"source_id"`
}

type EvidenceGate struct {
	llmClient *llm.Client
}

func NewEvidenceGate(client *llm.Client) *EvidenceGate {
	return &EvidenceGate{llmClient: client}
}

func (g *EvidenceGate) Run(ctx context.Context, synthesisText string, papers []search.Paper) (*EvidenceGateResult, error) {
	var claims []string
	var err error

	if len(synthesisText) > AIExtractionThreshold {
		claims, err = g.extractClaimsWithAI(ctx, synthesisText)
		if err != nil {
			slog.Warn("AI claim extraction failed, falling back to heuristic", "error", err)
			claims = g.extractHeuristicClaims(synthesisText)
		}
	} else {
		claims = g.extractHeuristicClaims(synthesisText)
	}

	linkedClaims := make([]LinkedClaim, 0)
	unlinkedClaims := make([]string, 0)
	contradictions := make([]ClaimContradiction, 0)

	for _, claim := range claims {
		bestPaper, ratio := g.findBestSourceForClaim(claim, papers)
		if bestPaper == nil {
			unlinkedClaims = append(unlinkedClaims, claim)
		} else {
			linkedClaims = append(linkedClaims, LinkedClaim{
				Claim:        claim,
				SourceID:     bestPaper.ID,
				SourceTitle:  bestPaper.Title,
				OverlapRatio: ratio,
			})
			if g.detectContradiction(claim, *bestPaper) {
				contradictions = append(contradictions, ClaimContradiction{
					Claim:    claim,
					SourceID: bestPaper.ID,
				})
			}
		}
	}

	total := len(claims)
	linkedCount := len(linkedClaims)
	unlinkedCount := len(unlinkedClaims)
	contradictionCount := len(contradictions)

	result := &EvidenceGateResult{
		Claims:             claims,
		LinkedClaims:       linkedClaims,
		UnlinkedClaims:     unlinkedClaims,
		Contradictions:     contradictions,
		Checked:            total,
		PassedCount:        linkedCount,
		UnlinkedCount:      unlinkedCount,
		ContradictionCount: contradictionCount,
	}

	if total == 0 {
		result.Verdict = "provisional"
		result.WarningPrefix = "⚠️ "
		result.Message = "No verifiable claims extracted from synthesis."
	} else if float64(linkedCount)/float64(total) >= 0.8 && contradictionCount == 0 {
		result.Verdict = "passed"
		result.Message = fmt.Sprintf("Evidence gate passed: %d/%d claims grounded in sources.", linkedCount, total)
	} else if float64(linkedCount)/float64(total) >= 0.5 {
		result.Verdict = "provisional"
		result.WarningPrefix = "⚠️ "
		result.Message = fmt.Sprintf("Partial grounding: %d/%d claims supported, %d unlinked, %d contradictions.", linkedCount, total, unlinkedCount, contradictionCount)
	} else {
		result.Verdict = "failed"
		result.WarningPrefix = "❌ "
		result.Message = fmt.Sprintf("Evidence gate failed: only %d/%d claims are grounded. %d unlinked claims detected.", linkedCount, total, unlinkedCount)
	}

	return result, nil
}

func (g *EvidenceGate) extractHeuristicClaims(text string) []string {
	sentences := strings.Split(text, ". ") // Simple split, could be improved with regex
	claims := make([]string, 0)
	seen := make(map[string]bool)

	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if len(s) < 20 || len(s) > 600 {
			continue
		}
		key := strings.ToLower(s)
		if len(key) > 80 {
			key = key[:80]
		}
		if seen[key] {
			continue
		}

		for _, p := range claimPatterns {
			if p.MatchString(s) {
				seen[key] = true
				claims = append(claims, s)
				break
			}
		}
		if len(claims) >= maxClaims {
			break
		}
	}
	return claims
}

func (g *EvidenceGate) extractClaimsWithAI(ctx context.Context, synthesisText string) ([]string, error) {
	prompt := fmt.Sprintf(`Extract the key factual claims from this academic synthesis text.
A factual claim is a specific assertion that can be verified against source papers.
Include claims about statistics, findings, effects, comparisons, and conclusions.

Text:
"""
%s
"""

Return a JSON object with a "claims" array containing up to 12 claim strings.
Each claim should be a complete sentence.`, synthesisText)

	resp, err := g.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveLightModel(),
		JsonSchema: `{"type": "object", "properties": {"claims": {"type": "array", "items": {"type": "string"}}}, "required": ["claims"]}`,
	})
	if err != nil {
		return nil, err
	}

	var result struct {
		Claims []string `json:"claims"`
	}
	if err := json.Unmarshal([]byte(resp.JsonResult), &result); err != nil {
		return nil, err
	}

	return result.Claims, nil
}

func (g *EvidenceGate) findBestSourceForClaim(claim string, papers []search.Paper) (*search.Paper, float64) {
	claimTokens := g.tokenize(claim)
	if len(claimTokens) == 0 {
		return nil, 0
	}

	var bestPaper *search.Paper
	bestRatio := 0.0

	for i := range papers {
		paper := &papers[i]
		content := paper.Title + " " + paper.Abstract
		paperTokens := g.tokenize(content)
		if len(paperTokens) == 0 {
			continue
		}

		overlap := 0
		paperTokenMap := make(map[string]bool)
		for _, t := range paperTokens {
			paperTokenMap[t] = true
		}

		for _, t := range claimTokens {
			if paperTokenMap[t] {
				overlap++
			}
		}

		ratio := float64(overlap) / float64(len(claimTokens))
		if ratio > bestRatio {
			bestRatio = ratio
			bestPaper = paper
		}
	}

	if bestRatio >= minOverlapRatio {
		return bestPaper, bestRatio
	}
	return nil, 0
}

func (g *EvidenceGate) tokenize(text string) []string {
	re := regexp.MustCompile(`[a-z0-9]+`)
	matches := re.FindAllString(strings.ToLower(text), -1)
	tokens := make([]string, 0)
	for _, m := range matches {
		if len(m) > 2 {
			if _, isStop := stopWords[m]; !isStop {
				tokens = append(tokens, m)
			}
		}
	}
	return tokens
}

func (g *EvidenceGate) detectContradiction(claim string, paper search.Paper) bool {
	negationRe := regexp.MustCompile(`(?i)\b(?:no|not|none|failed|lack|absent|without|ineffective)\b`)
	positiveRe := regexp.MustCompile(`(?i)\b(?:significant|positive|effective|beneficial|improved)\b`)

	claimNegated := negationRe.MatchString(claim)
	paperPositive := positiveRe.MatchString(paper.Abstract)

	return claimNegated && paperPositive
}
