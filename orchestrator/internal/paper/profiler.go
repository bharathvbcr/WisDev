package paper

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev-agent/wisdev-agent-os/orchestrator/internal/search"
	llmv1 "github.com/wisdev-agent/wisdev-agent-os/orchestrator/proto/llm/v1"
)

// Profile represents a deep analysis of an academic paper.
type Profile struct {
	PaperID             string   `json:"paperId"`
	DOI                 string   `json:"doi,omitempty"`
	Summary             string   `json:"summary"`
	KeyFindings         []string `json:"keyFindings"`
	Methodology         string   `json:"methodology"`
	MethodologicalRigor string   `json:"methodologicalRigor"` // e.g. "High", "Medium", "Low"
	SampleSize          string   `json:"sampleSize,omitempty"`
	Limitations         []string `json:"limitations"`
	ImpactScore         float64  `json:"impactScore"`  // 0-1
	NoveltyScore        float64  `json:"noveltyScore"` // 0-1
}

// Profiler handles paper extraction and analysis.
type Profiler struct {
	llmClient *llm.Client
}

func NewProfiler(llm *llm.Client) *Profiler {
	return &Profiler{llmClient: llm}
}

// ExtractProfile generates a deep profile for a given paper.
func (p *Profiler) ExtractProfile(ctx context.Context, paper search.Paper) (*Profile, error) {
	prompt := fmt.Sprintf(`Extract a deep research profile for the following paper:
Title: %s
Abstract: %s

Return a JSON object with:
- summary (detailed 2-3 sentence summary)
- keyFindings (list of 3-5 main results)
- methodology (description of techniques used)
- methodologicalRigor (one of: "high", "medium", "low")
- sampleSize (if mentioned, otherwise "not specified")
- limitations (potential biases or weaknesses)
- impactScore (float 0-1 based on citation potential and novelty)
- noveltyScore (float 0-1 based on how new the approach is)
`, paper.Title, paper.Abstract)

	schema := `{
		"type": "object",
		"properties": {
			"summary": {"type": "string"},
			"keyFindings": {"type": "array", "items": {"type": "string"}},
			"methodology": {"type": "string"},
			"methodologicalRigor": {"type": "string", "enum": ["high", "medium", "low"]},
			"sampleSize": {"type": "string"},
			"limitations": {"type": "array", "items": {"type": "string"}},
			"impactScore": {"type": "number"},
			"noveltyScore": {"type": "number"}
		},
		"required": ["summary", "keyFindings", "methodology", "methodologicalRigor", "sampleSize", "limitations", "impactScore", "noveltyScore"]
	}`

	resp, err := p.llmClient.StructuredOutput(ctx, &llmv1.StructuredRequest{
		Prompt:     prompt,
		JsonSchema: schema,
		Model:      llm.ResolveStandardModel(),
	})
	if err != nil {
		return nil, fmt.Errorf("llm structured output failed: %w", err)
	}

	var profile Profile
	if err := json.Unmarshal([]byte(resp.JsonResult), &profile); err != nil {
		return nil, fmt.Errorf("failed to decode profile json: %w", err)
	}

	profile.PaperID = paper.ID
	profile.DOI = paper.DOI

	return &profile, nil
}
