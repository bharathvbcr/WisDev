package wisdev

import (
	"context"
	"encoding/json"
	"errors"
)

// DocumentOptions configures public document generation.
type DocumentOptions struct {
	Query             string
	Intent            string // "report" | "litreview" | "fullpaper" (default: fullpaper)
	CitationStyle     string // default: apa
	Format            string // markdown | latex | html | json | docx (default: markdown)
	Papers            []Paper
	Research          *YOLOResult
	PythonURL         string
	Offline           bool
	IncludeUncited    bool
	VoiceInstructions string
	ReviewStyle       string
	TargetWords       int
	MinCitations      int
	SectionFlow       []string
	ReviewRounds      int
	Genre             string
}

// DocumentResult bundles the rendered output with the canonical document envelope.
type DocumentResult struct {
	Rendered string          `json:"rendered"`
	Document json.RawMessage `json:"document,omitempty"`
	Pipeline json.RawMessage `json:"pipeline,omitempty"` // fullpaper intent only
}

// DocumentGenerator is the function signature for the injected DocGen backend.
// Wired once at process startup from a layer that can import internal/docgen
// (the CLI wires it in wirePublicDocumentGenerator).
type DocumentGenerator func(ctx context.Context, opts DocumentOptions) (DocumentResult, error)

var documentGenerator DocumentGenerator

// SetDocumentGenerator injects the DocGen-backed generator. Called once from
// the CLI at startup. When unset, GenerateDocument returns an error.
func SetDocumentGenerator(fn DocumentGenerator) { documentGenerator = fn }

// GenerateDocument runs the ScholarDoc generation pipeline and renders the
// result in the requested format. This is an additive public API — existing
// Agent/YOLO surfaces are unchanged. Requires the CLI (or another host) to have
// called SetDocumentGenerator at startup.
func GenerateDocument(ctx context.Context, opts DocumentOptions) (DocumentResult, error) {
	if documentGenerator == nil {
		return DocumentResult{}, errors.New("document generator not wired: run via wisdev CLI or call SetDocumentGenerator")
	}
	return documentGenerator(ctx, opts)
}
