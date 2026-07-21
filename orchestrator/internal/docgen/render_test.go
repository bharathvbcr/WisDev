package docgen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

func sampleDocument() Document {
	return Document{
		Title:  "Transformer Attention",
		Intent: IntentFullPaper,
		Sections: []Section{
			{ID: "introduction", Title: "Introduction", Content: "Attention mechanisms improve sequence modeling [1]."},
			{ID: "conclusion", Title: "Conclusion", Content: "Further work remains."},
		},
		References: []Reference{
			{Authors: []string{"Vaswani, A."}, Year: 2017, Title: "Attention Is All You Need", Venue: "NeurIPS"},
		},
		CitationStyle: citations.StyleAPA,
	}
}

func TestParseRenderFormat(t *testing.T) {
	cases := map[string]RenderFormat{
		"":         FormatMarkdown,
		"markdown": FormatMarkdown,
		"md":       FormatMarkdown,
		"latex":    FormatLaTeX,
		"tex":      FormatLaTeX,
		"html":     FormatHTML,
		"json":     FormatJSON,
		"docx":     FormatDOCX,
	}
	for raw, want := range cases {
		got, err := ParseRenderFormat(raw)
		if err != nil {
			t.Fatalf("ParseRenderFormat(%q) error: %v", raw, err)
		}
		if got != want {
			t.Errorf("ParseRenderFormat(%q)=%q want %q", raw, got, want)
		}
	}
	if _, err := ParseRenderFormat("pdf"); err == nil {
		t.Fatal("expected error for unknown format")
	}
}

func TestRenderMarkdown(t *testing.T) {
	out, err := Render(sampleDocument(), RenderOptions{Format: FormatMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# Transformer Attention") {
		t.Error("missing title heading")
	}
	if !strings.Contains(out, "## Introduction") {
		t.Error("missing section heading")
	}
	if !strings.Contains(out, "## References") {
		t.Error("missing references section")
	}
}

func TestRenderHTML(t *testing.T) {
	out, err := Render(sampleDocument(), RenderOptions{Format: FormatHTML})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<!DOCTYPE html>") {
		t.Error("missing doctype")
	}
	if !strings.Contains(out, "<h1>Transformer Attention</h1>") {
		t.Error("missing title")
	}
	if !strings.Contains(out, "<h2>References</h2>") {
		t.Error("missing references")
	}
}

func TestRenderLaTeX(t *testing.T) {
	out, err := Render(sampleDocument(), RenderOptions{Format: FormatLaTeX})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\\documentclass") {
		t.Error("missing documentclass")
	}
	if !strings.Contains(out, "\\begin{thebibliography}") {
		t.Error("missing bibliography")
	}
}

func TestRenderJSON(t *testing.T) {
	out, err := Render(sampleDocument(), RenderOptions{Format: FormatJSON})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"title": "Transformer Attention"`) {
		t.Error("missing title in JSON")
	}
	if !strings.Contains(out, `"intent": "fullpaper"`) {
		t.Error("missing intent in JSON")
	}
}

func TestRenderCitationStyleOverride(t *testing.T) {
	doc := sampleDocument()
	out, err := Render(doc, RenderOptions{Format: FormatMarkdown, CitationStyle: citations.StyleIEEE})
	if err != nil {
		t.Fatal(err)
	}
	// IEEE bibliography lines are numbered with [n] prefix inside the entry.
	if !strings.Contains(out, "## References") {
		t.Error("missing references")
	}
}

func sampleVisuals() []internalwisdev.VisualArtifact {
	return []internalwisdev.VisualArtifact{
		{
			Title:    "Evidence summary",
			Kind:     "table_summary",
			SpecType: "table",
			Spec: internalwisdev.ManuscriptTableSpec{
				Headers: []string{"Theme", "Finding"},
				Rows:    [][]string{{"Attention", "Improves modeling"}},
			},
			Caption: "Key themes from the literature.",
		},
		{
			Title:    "Concept map",
			Kind:     "concept_diagram",
			SpecType: "mermaid",
			Spec:     "flowchart TD\n    A --> B",
			Caption:  "Source packet linkage.",
		},
		{
			Title:    "Metric chart",
			Kind:     "chart",
			SpecType: "vega_lite",
			Spec: map[string]any{
				"mark": "bar",
				"encoding": map[string]any{
					"x": map[string]any{"field": "label", "type": "nominal"},
					"y": map[string]any{"field": "value", "type": "quantitative"},
				},
			},
		},
	}
}

func TestTableSpecFromAny(t *testing.T) {
	spec, ok := tableSpecFromAny(internalwisdev.ManuscriptTableSpec{
		Headers: []string{"A", "B"},
		Rows:    [][]string{{"1", "2"}},
	})
	if !ok || len(spec.Headers) != 2 || len(spec.Rows) != 1 {
		t.Fatalf("typed table spec not parsed: ok=%v spec=%+v", ok, spec)
	}

	raw := map[string]any{
		"headers": []any{"Col1", "Col2"},
		"rows":    []any{[]any{"r1c1", "r1c2"}},
	}
	spec, ok = tableSpecFromAny(raw)
	if !ok || spec.Headers[0] != "Col1" || spec.Rows[0][1] != "r1c2" {
		t.Fatalf("map table spec not parsed: ok=%v spec=%+v", ok, spec)
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var unmarshaled map[string]interface{}
	if err := json.Unmarshal(encoded, &unmarshaled); err != nil {
		t.Fatal(err)
	}
	spec, ok = tableSpecFromAny(unmarshaled)
	if !ok || spec.Headers[1] != "Col2" {
		t.Fatalf("json-unmarshaled map table spec not parsed: ok=%v spec=%+v", ok, spec)
	}

	if _, ok := tableSpecFromAny(map[string]any{"mark": "bar"}); ok {
		t.Fatal("expected non-table map to fail")
	}
}

func TestRenderVisualsMarkdown(t *testing.T) {
	out := renderVisualsMarkdown(sampleVisuals())
	if !strings.Contains(out, "| Theme | Finding |") {
		t.Error("missing markdown table header")
	}
	if !strings.Contains(out, "| Attention | Improves modeling |") {
		t.Error("missing markdown table row")
	}
	if !strings.Contains(out, "```mermaid\nflowchart TD") {
		t.Error("missing mermaid fenced block")
	}
	if !strings.Contains(out, "```json\n") || !strings.Contains(out, `"mark": "bar"`) {
		t.Error("missing vega-lite json fenced block")
	}
}

func TestRenderVisualsHTML(t *testing.T) {
	out := renderVisualsHTML(sampleVisuals())
	if !strings.Contains(out, "<table>") || !strings.Contains(out, "<th>Theme</th>") {
		t.Error("missing html table")
	}
	if !strings.Contains(out, "<td>Attention</td>") {
		t.Error("missing html table cell")
	}
	if !strings.Contains(out, `<code class="language-mermaid">`) || !strings.Contains(out, "flowchart TD") {
		t.Error("missing mermaid pre/code block")
	}
	if !strings.Contains(out, `<code class="language-json">`) || !strings.Contains(out, "mark") {
		t.Error("missing vega-lite json pre/code block")
	}
}

func TestRenderVisualsLaTeX(t *testing.T) {
	out := renderVisualsLaTeX(sampleVisuals())
	if !strings.Contains(out, `\begin{tabular}`) || !strings.Contains(out, `\textbf{Theme}`) {
		t.Error("missing latex table")
	}
	if !strings.Contains(out, "Diagram source (Mermaid)") || !strings.Contains(out, "flowchart TD") {
		t.Error("missing mermaid verbatim block")
	}
	if !strings.Contains(out, "Vega-Lite specification") || !strings.Contains(out, `"mark"`) {
		t.Error("missing vega-lite verbatim block")
	}
}

func TestRenderDocumentWithVisuals(t *testing.T) {
	doc := sampleDocument()
	doc.Visuals = sampleVisuals()

	md, err := Render(doc, RenderOptions{Format: FormatMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "## Figures & Visuals") || !strings.Contains(md, "| Theme | Finding |") {
		t.Error("markdown export missing rendered visuals")
	}

	html, err := Render(doc, RenderOptions{Format: FormatHTML})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "<table>") || !strings.Contains(html, "language-mermaid") {
		t.Error("html export missing rendered visuals")
	}

	tex, err := Render(doc, RenderOptions{Format: FormatLaTeX})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tex, `\begin{tabular}`) || !strings.Contains(tex, "Vega-Lite specification") {
		t.Error("latex export missing rendered visuals")
	}
}
