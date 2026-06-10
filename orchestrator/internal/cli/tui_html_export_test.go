package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func htmlTestResult() *agent.YOLOResult {
	return &agent.YOLOResult{
		FinalAnswer: "## Findings\n" +
			"Scaffold repair shows **promise** [1]. Unknown marker [99].\n\n" +
			"- bullet one\n- bullet two\n\n" +
			"1. first step\n2. second step\n",
		Iterations:  2,
		PapersFound: 2,
		Papers: []agent.Paper{
			{Title: "Meniscus Scaffolds", Authors: []string{"Lee J"}, Year: 2024, Link: "https://example.org/p1", CitationCount: 12},
			{Title: "Allograft Outcomes", Authors: []string{"Chen R"}, Year: 2023, DOI: "10.1000/xyz"},
		},
		Hypotheses: []agent.Hypothesis{
			{Claim: "Scaffolds restore load distribution", ConfidenceScore: 0.72, Status: "supported"},
		},
		Beliefs: []agent.Belief{
			{Claim: "Repair beats resection", Confidence: 0.35, Status: "active", SupportCount: 1, ContradictionCount: 1},
		},
		BranchPlans: []agent.BranchPlan{
			{ID: "b1", Query: "scaffold biomechanics", ReasoningStrategy: "evidence_grounded_retrieval", Status: "retrieved"},
		},
		ReasoningTrace: []agent.ReasoningStep{
			{Phase: "planning", Decision: "cot_plan_summary", Reasoning: "Plan retrieval branches."},
		},
		Grounding: &agent.GroundingStats{GroundedClaims: 3, TotalClaims: 4, CitedSources: 2},
	}
}

func TestFormatYOLOResultHTMLStructure(t *testing.T) {
	html := formatYOLOResultHTML("meniscus repair", htmlTestResult(), 3*time.Second)

	for _, want := range []string{
		"<!DOCTYPE html>",
		"<style>",
		"</html>",
		"<h1>WisDev Research Result</h1>",
		"Question:</strong> meniscus repair",
		"Grounding: 3/4 claims cited",
		"Hypotheses &amp; beliefs",
		`id="ref-1"`,
		`id="ref-2"`,
		`<a href="https://example.org/p1">Meniscus Scaffolds</a>`,
		"<details>",
		"Reasoning trace (1 steps)",
		"evidence grounded retrieval",
		"bar-fill",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HTML report missing %q", want)
		}
	}

	// Self-contained: nothing in <head> may reference an external URL.
	headEnd := strings.Index(html, "</head>")
	if headEnd < 0 {
		t.Fatal("missing </head>")
	}
	head := html[:headEnd]
	if strings.Contains(head, "http://") || strings.Contains(head, "https://") {
		t.Fatalf("head references external assets:\n%s", head)
	}
	if strings.Contains(html, "<script") {
		t.Fatal("HTML report must not contain scripts")
	}
}

func TestFormatYOLOResultHTMLCitationAnchors(t *testing.T) {
	html := formatYOLOResultHTML("q", htmlTestResult(), 0)
	if !strings.Contains(html, `<sup><a href="#ref-1">[1]</a></sup>`) {
		t.Fatal("expected [1] to render as a superscript anchor")
	}
	if strings.Contains(html, `href="#ref-99"`) {
		t.Fatal("[99] has no bibliography entry and must not become an anchor")
	}
	if !strings.Contains(html, "[99]") {
		t.Fatal("unresolved [99] marker should stay literal")
	}
}

func TestAnswerMarkdownToHTMLBlocks(t *testing.T) {
	out := answerMarkdownToHTML("# Top\n## Section\n### Sub\n\n- a\n- b\n\n1. one\n2. two\n\nplain **bold** *ital*", 0)
	for _, want := range []string{
		"<h2>Top</h2>", "<h2>Section</h2>", "<h3>Sub</h3>",
		"<ul>\n<li>a</li>\n<li>b</li>\n</ul>",
		"<ol>\n<li>one</li>\n<li>two</li>\n</ol>",
		"<strong>bold</strong>", "<em>ital</em>",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("converted answer missing %q in:\n%s", want, out)
		}
	}
}

func TestHTMLExportEscapesScriptInAnswer(t *testing.T) {
	result := htmlTestResult()
	result.FinalAnswer = `Attack <script>alert("x")</script> & <img src=x onerror=alert(1)>`
	result.Papers[0].Title = `<script>steal()</script> Title`
	result.Hypotheses[0].Claim = `claim with <b>markup</b>`
	html := formatYOLOResultHTML(`query "with" <quotes>`, result, 0)

	if strings.Contains(html, "<script>") || strings.Contains(html, "<img") {
		t.Fatal("interpolated content must be HTML-escaped")
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img src=x", "&lt;b&gt;markup&lt;/b&gt;", "&lt;quotes&gt;"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected escaped form %q in report", want)
		}
	}
}

func TestHTMLConfidenceBarWidth(t *testing.T) {
	if bar := htmlConfidenceBar(0.72); !strings.Contains(bar, "width:72%") || !strings.Contains(bar, "0.72") || !strings.Contains(bar, `class="bar-fill ok"`) {
		t.Fatalf("unexpected bar for 0.72: %s", bar)
	}
	if bar := htmlConfidenceBar(0.2); !strings.Contains(bar, `class="bar-fill low"`) {
		t.Fatalf("expected low band for 0.2: %s", bar)
	}
	if bar := htmlConfidenceBar(1.5); !strings.Contains(bar, "width:100%") {
		t.Fatalf("expected clamped width for 1.5: %s", bar)
	}
}

func TestIsSafeHTTPURL(t *testing.T) {
	if !isSafeHTTPURL("https://example.org") || !isSafeHTTPURL("http://example.org") {
		t.Fatal("expected http(s) links to be safe")
	}
	if isSafeHTTPURL("javascript:alert(1)") || isSafeHTTPURL("data:text/html,x") {
		t.Fatal("non-http schemes must be rejected")
	}
}

func TestSaveTUIResultHTML(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "report.md")
	saved, err := saveTUIResultHTML(target, "q", htmlTestResult(), time.Second)
	if err != nil {
		t.Fatalf("saveTUIResultHTML error: %v", err)
	}
	if !strings.HasSuffix(saved, ".html") {
		t.Fatalf("expected .html target, got %s", saved)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("read saved report: %v", err)
	}
	if !strings.Contains(string(raw), "<!DOCTYPE html>") {
		t.Fatal("saved report is not an HTML document")
	}

	if _, err := saveTUIResultHTML(filepath.Join(dir, "x.html"), "q", nil, 0); err == nil {
		t.Fatal("expected error for nil result")
	}
}
