package docgen

import (
	"context"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

// ManuscriptControls holds granular knobs for full-paper generation.
type ManuscriptControls struct {
	TargetWords        int
	MinCitations       int
	SectionFlow        []string
	ReviewRounds       int
	Genre              string
	CustomInstructions string
}

// Options configures document generation across intents.
type Options struct {
	Query            string
	Intent           Intent
	CitationStyle    citations.Style
	Papers           []search.Paper
	Research         *agent.YOLOResult
	PythonURL        string
	Offline          bool
	IncludeUncited   bool
	VoiceInstructions string
	ReviewStyle      string // "academic" | "accessible" for litreview
	Manuscript       ManuscriptControls
	LLMClient        *llm.Client
	OnStage          func(string)
	JobID            string
}

// GenerateResult bundles the canonical document with optional raw pipeline output.
type GenerateResult struct {
	Document Document
	Pipeline internalwisdev.ManuscriptPipelineResult
}

// Generate retrieves or reuses a corpus, then dispatches by intent.
func Generate(ctx context.Context, opts Options) (GenerateResult, error) {
	intent := opts.Intent
	if intent == "" {
		intent = IntentFullPaper
	}
	style := opts.CitationStyle
	if style == "" {
		style = citations.StyleAPA
	}

	switch intent {
	case IntentReport:
		doc, err := generateReport(ctx, opts)
		if err != nil {
			return GenerateResult{}, err
		}
		doc.CitationStyle = style
		return GenerateResult{Document: doc}, nil
	case IntentLitReview:
		doc, err := generateLitReview(ctx, opts)
		if err != nil {
			return GenerateResult{}, err
		}
		doc.CitationStyle = style
		return GenerateResult{Document: doc}, nil
	default:
		doc, pipeline, err := generateFullPaper(ctx, opts)
		if err != nil {
			return GenerateResult{}, err
		}
		doc.CitationStyle = style
		return GenerateResult{Document: doc, Pipeline: pipeline}, nil
	}
}

func defaultJobID(prefix string) string {
	if prefix == "" {
		prefix = "docgen"
	}
	return prefix + "_" + time.Now().Format("20060102150405")
}

func resolveLLMClient(client *llm.Client) *llm.Client {
	if client != nil {
		return client
	}
	return llm.NewClient()
}
