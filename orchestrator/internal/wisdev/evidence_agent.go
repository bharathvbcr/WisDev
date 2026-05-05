package wisdev

import (
	"context"
	"fmt"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
)

// EvidenceAgent handles the gathering of evidence for research hypotheses.
type EvidenceAgent struct {
	registry *search.ProviderRegistry
}

func NewEvidenceAgent(registry *search.ProviderRegistry) *EvidenceAgent {
	return &EvidenceAgent{registry: registry}
}

func (a *EvidenceAgent) Gather(ctx context.Context, query string, hypothesisText string, limit int) ([]*EvidenceFinding, error) {
	if limit <= 0 {
		limit = 20
	}

	if strings.TrimSpace(query) == "" && strings.TrimSpace(hypothesisText) == "" {
		return nil, fmt.Errorf("query or hypothesis text is required")
	}

	if a.registry == nil {
		return []*EvidenceFinding{}, nil
	}

	// Strategy: Search for both the hypothesis text and the original query to ensure coverage.
	combinedQuery := fmt.Sprintf("%s %s", query, hypothesisText)
	papers, _, err := retrieveCanonicalSearchPapers(ctx, a.registry, combinedQuery, search.SearchOpts{
		Limit:       limit,
		QualitySort: true,
	})
	if err != nil {
		return nil, err
	}

	if packetFindings := buildEvidenceFindingsFromRawMaterial(combinedQuery, papers, limit); len(packetFindings) > 0 {
		out := make([]*EvidenceFinding, 0, len(packetFindings))
		for idx := range packetFindings {
			finding := packetFindings[idx]
			if strings.TrimSpace(finding.Status) == "" {
				finding.Status = "complete"
			}
			out = append(out, &finding)
		}
		return out, nil
	}

	out := make([]*EvidenceFinding, 0, limit)
	for _, p := range papers {
		if len(out) >= limit {
			break
		}
		for _, finding := range buildEvidenceFindingsFromSource(mapPaperToSource(p), MinInt(2, limit-len(out))) {
			finding.Status = "complete"
			copyFinding := finding
			out = append(out, &copyFinding)
			if len(out) >= limit {
				break
			}
		}
	}

	return out, nil
}
