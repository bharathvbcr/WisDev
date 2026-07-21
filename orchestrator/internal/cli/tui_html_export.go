package cli

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

// htmlCitationMarkerRe matches inline [n] citation markers in escaped text.
var htmlCitationMarkerRe = regexp.MustCompile(`\[(\d+)\]`)

// htmlBoldRe / htmlItalicRe match markdown emphasis in escaped text.
var (
	htmlBoldRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	htmlItalicRe = regexp.MustCompile(`\*([^*]+)\*`)
)

// htmlReportCSS is the inline dark-theme stylesheet (ScholarLM brand red
// #ea2a33). No external assets, fonts, or scripts.
const htmlReportCSS = `
:root { --accent: #ea2a33; --bg: #16090a; --panel: #241114; --text: #f3e7e8; --dim: #a89598; --warn: #f59e0b; --ok: #5fbf77; }
* { box-sizing: border-box; }
body { background: var(--bg); color: var(--text); font-family: Georgia, 'Times New Roman', serif; margin: 0; padding: 2rem 1rem; line-height: 1.6; }
main { max-width: 56rem; margin: 0 auto; }
h1, h2, h3 { font-family: Verdana, Helvetica, Arial, sans-serif; line-height: 1.25; }
h1 { color: var(--accent); border-bottom: 2px solid var(--accent); padding-bottom: .4rem; }
h2 { color: var(--accent); margin-top: 2rem; }
h3 { color: var(--text); }
a { color: var(--accent); }
sup a { text-decoration: none; }
.meta, .dim { color: var(--dim); font-size: .9rem; }
.card { background: var(--panel); border-left: 4px solid var(--accent); border-radius: 4px; padding: .8rem 1rem; margin: 1rem 0; }
.warn { border-left-color: var(--warn); }
.bar-track { display: inline-block; width: 10rem; height: .7rem; background: #3a2326; border-radius: 4px; overflow: hidden; vertical-align: middle; }
.bar-fill { display: block; height: 100%; background: var(--accent); }
.bar-fill.ok { background: var(--ok); }
.bar-fill.low { background: var(--warn); }
.status-tag { font-family: Verdana, sans-serif; font-size: .75rem; border: 1px solid var(--dim); border-radius: 3px; padding: 0 .35rem; margin-left: .4rem; color: var(--dim); }
ul.branches { list-style: none; padding-left: .5rem; }
ol.bibliography li { margin-bottom: .6rem; }
details { background: var(--panel); border-radius: 4px; padding: .6rem 1rem; margin: 1rem 0; }
summary { cursor: pointer; color: var(--accent); font-family: Verdana, sans-serif; }
.trace-step { margin: .5rem 0; }
.trace-phase { color: var(--warn); font-family: Verdana, sans-serif; font-size: .8rem; text-transform: uppercase; }
footer { margin-top: 3rem; color: var(--dim); font-size: .85rem; border-top: 1px solid #3a2326; padding-top: 1rem; }
`

// htmlEscape escapes interpolated content for safe HTML embedding.
func htmlEscape(text string) string {
	return html.EscapeString(text)
}

// htmlInlineMarkdown applies inline markdown transforms (bold, italic, [n]
// citation anchors) to already-escaped text. Citation markers only become
// anchors when they resolve to a bibliography entry (1..refCount).
func htmlInlineMarkdown(escaped string, refCount int) string {
	escaped = htmlBoldRe.ReplaceAllString(escaped, "<strong>$1</strong>")
	escaped = htmlItalicRe.ReplaceAllString(escaped, "<em>$1</em>")
	return htmlCitationMarkerRe.ReplaceAllStringFunc(escaped, func(marker string) string {
		num, err := strconv.Atoi(strings.Trim(marker, "[]"))
		if err != nil || num < 1 || num > refCount {
			return marker
		}
		return fmt.Sprintf(`<sup><a href="#ref-%d">[%d]</a></sup>`, num, num)
	})
}

// answerMarkdownToHTML converts the answer's markdown subset (headings,
// bold/italic, bullets, numbered lists, [n] markers) to HTML. All content is
// escaped before any tags are introduced. Code fences and tables are
// intentionally unsupported; unknown lines render as paragraphs.
func answerMarkdownToHTML(answer string, refCount int) string {
	var b strings.Builder
	inUL := false
	inOL := false
	closeLists := func() {
		if inUL {
			b.WriteString("</ul>\n")
			inUL = false
		}
		if inOL {
			b.WriteString("</ol>\n")
			inOL = false
		}
	}
	for _, raw := range strings.Split(strings.TrimSpace(answer), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			closeLists()
			continue
		}
		switch {
		case strings.HasPrefix(line, "### "):
			closeLists()
			b.WriteString("<h3>" + htmlInlineMarkdown(htmlEscape(strings.TrimPrefix(line, "### ")), refCount) + "</h3>\n")
		case strings.HasPrefix(line, "## "):
			closeLists()
			b.WriteString("<h2>" + htmlInlineMarkdown(htmlEscape(strings.TrimPrefix(line, "## ")), refCount) + "</h2>\n")
		case strings.HasPrefix(line, "# "):
			closeLists()
			b.WriteString("<h2>" + htmlInlineMarkdown(htmlEscape(strings.TrimPrefix(line, "# ")), refCount) + "</h2>\n")
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			if inOL {
				b.WriteString("</ol>\n")
				inOL = false
			}
			if !inUL {
				b.WriteString("<ul>\n")
				inUL = true
			}
			b.WriteString("<li>" + htmlInlineMarkdown(htmlEscape(line[2:]), refCount) + "</li>\n")
		case isNumberedAnswerListItem(line):
			if inUL {
				b.WriteString("</ul>\n")
				inUL = false
			}
			if !inOL {
				b.WriteString("<ol>\n")
				inOL = true
			}
			item := line[strings.Index(line, ". ")+2:]
			b.WriteString("<li>" + htmlInlineMarkdown(htmlEscape(item), refCount) + "</li>\n")
		case strings.HasPrefix(line, "> "):
			closeLists()
			b.WriteString(`<p class="dim">` + htmlInlineMarkdown(htmlEscape(strings.TrimPrefix(line, "> ")), refCount) + "</p>\n")
		default:
			closeLists()
			b.WriteString("<p>" + htmlInlineMarkdown(htmlEscape(line), refCount) + "</p>\n")
		}
	}
	closeLists()
	return b.String()
}

// htmlConfidenceBar renders a CSS-width confidence bar for a 0..1 score.
func htmlConfidenceBar(score float64) string {
	clamped := score
	if clamped < 0 {
		clamped = 0
	}
	if clamped > 1 {
		clamped = 1
	}
	band := ""
	switch {
	case score >= 0.7:
		band = " ok"
	case score < 0.4:
		band = " low"
	}
	return fmt.Sprintf(`<span class="bar-track"><span class="bar-fill%s" style="width:%d%%"></span></span> %.2f`,
		band, int(clamped*100+0.5), score)
}

// formatYOLOResultHTML renders the full self-contained HTML report.
func formatYOLOResultHTML(query string, result *agent.YOLOResult, elapsed time.Duration) string {
	refCount := 0
	if result != nil {
		refCount = len(result.Papers)
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>WisDev Research Result</title>\n")
	b.WriteString("<style>" + htmlReportCSS + "</style>\n</head>\n<body>\n<main>\n")
	b.WriteString("<h1>WisDev Research Result</h1>\n")
	if q := strings.TrimSpace(query); q != "" {
		b.WriteString("<p><strong>Question:</strong> " + htmlEscape(q) + "</p>\n")
	}

	if result == nil {
		b.WriteString("<p class=\"meta\">No results returned.</p>\n")
		b.WriteString("</main>\n</body>\n</html>\n")
		return b.String()
	}

	// Run summary + grounding scorecard.
	var metaParts []string
	if result.RequestedIterations > 0 && result.RequestedIterations != result.Iterations {
		metaParts = append(metaParts, fmt.Sprintf("Iterations: %d/%d", result.Iterations, result.RequestedIterations))
	} else {
		metaParts = append(metaParts, fmt.Sprintf("Iterations: %d", result.Iterations))
	}
	metaParts = append(metaParts, fmt.Sprintf("Papers: %d", result.PapersFound))
	if stop := strings.TrimSpace(result.StopReason); stop != "" {
		metaParts = append(metaParts, "Stop: "+stop)
	}
	if domain := strings.TrimSpace(result.DetectedDomain); domain != "" {
		metaParts = append(metaParts, "Domain: "+domain)
	}
	if mode := strings.TrimSpace(result.SynthesisMode); mode != "" {
		metaParts = append(metaParts, "Synthesis: "+mode)
	}
	if elapsed > 0 {
		metaParts = append(metaParts, fmt.Sprintf("Elapsed: %.1fs", elapsed.Seconds()))
	}
	b.WriteString("<p class=\"meta\">" + htmlEscape(strings.Join(metaParts, " · ")) + "</p>\n")
	if card := formatGroundingScorecard(result.Grounding); card != "" {
		class := "card"
		if groundingNeedsAttention(result.Grounding) {
			class += " warn"
		}
		b.WriteString(`<div class="` + class + `">` + htmlEscape(card) + "</div>\n")
	}

	if answer := strings.TrimSpace(result.FinalAnswer); answer != "" {
		b.WriteString("<h2>Final answer</h2>\n")
		b.WriteString(answerMarkdownToHTML(answer, refCount))
	}

	// Hypotheses & beliefs with CSS confidence bars.
	if len(result.Hypotheses) > 0 || len(result.Beliefs) > 0 {
		b.WriteString("<h2>Hypotheses &amp; beliefs</h2>\n")
		for _, hyp := range result.Hypotheses {
			b.WriteString(`<div class="card">` + htmlConfidenceBar(hyp.ConfidenceScore))
			if status := strings.TrimSpace(hyp.Status); status != "" {
				b.WriteString(`<span class="status-tag">` + htmlEscape(status) + "</span>")
			}
			b.WriteString("<br>" + htmlEscape(hyp.Claim))
			if fals := strings.TrimSpace(hyp.FalsifiabilityCondition); fals != "" {
				b.WriteString(`<br><span class="dim">falsifiable if: ` + htmlEscape(fals) + "</span>")
			}
			b.WriteString("</div>\n")
		}
		for _, belief := range result.Beliefs {
			b.WriteString(`<div class="card">` + htmlConfidenceBar(belief.Confidence))
			if status := strings.TrimSpace(belief.Status); status != "" {
				b.WriteString(`<span class="status-tag">` + htmlEscape(status) + "</span>")
			}
			b.WriteString("<br>" + htmlEscape(belief.Claim))
			if belief.SupportCount > 0 || belief.ContradictionCount > 0 {
				b.WriteString(fmt.Sprintf(`<br><span class="dim">%d supporting / %d contradicting</span>`, belief.SupportCount, belief.ContradictionCount))
			}
			b.WriteString("</div>\n")
		}
	}

	// Coverage and gaps.
	if gaps := resolveCoverageGaps(result); gaps != nil {
		b.WriteString("<h2>Coverage</h2>\n")
		b.WriteString(fmt.Sprintf("<p>%d/%d planned queries executed</p>\n", gaps.ExecutedQueryCount, gaps.PlannedQueryCount))
		if len(gaps.UnexecutedPlannedQueries) > 0 {
			b.WriteString("<ul>\n")
			for _, q := range gaps.UnexecutedPlannedQueries {
				b.WriteString(`<li class="dim">○ ` + htmlEscape(q) + "</li>\n")
			}
			b.WriteString("</ul>\n")
		}
		if len(gaps.MissingAspects) > 0 {
			b.WriteString("<h3>Open gaps</h3>\n<ul>\n")
			for _, aspect := range gaps.MissingAspects {
				b.WriteString("<li>" + htmlEscape(aspect) + "</li>\n")
			}
			b.WriteString("</ul>\n")
		}
	}

	// Branch plans grouped by reasoning strategy.
	if len(result.BranchPlans) > 0 {
		b.WriteString("<h2>Research branches</h2>\n")
		order, groups := groupBranchPlansByStrategy(result.BranchPlans)
		for _, strategy := range order {
			b.WriteString("<h3>" + htmlEscape(branchStrategyLabel(strategy)) + "</h3>\n<ul class=\"branches\">\n")
			for _, branch := range groups[strategy] {
				entry := branchStatusGlyph(branch.Status) + " " + branchPlanLabel(branch)
				if status := strings.TrimSpace(branch.Status); status != "" {
					entry += " [" + status + "]"
				}
				b.WriteString("<li>" + htmlEscape(entry))
				if hyp := strings.TrimSpace(branch.Hypothesis); hyp != "" && branchPlanIsHypothesis(branch) && hyp != branchPlanLabel(branch) {
					b.WriteString(`<br><span class="dim">hypothesis: ` + htmlEscape(hyp) + "</span>")
				}
				b.WriteString("</li>\n")
			}
			b.WriteString("</ul>\n")
		}
	}

	// Reasoning trace as a collapsible section.
	if len(result.ReasoningTrace) > 0 {
		b.WriteString("<details>\n<summary>Reasoning trace (" + strconv.Itoa(len(result.ReasoningTrace)) + " steps)</summary>\n")
		for _, step := range result.ReasoningTrace {
			b.WriteString(`<div class="trace-step"><span class="trace-phase">` + htmlEscape(reasoningPhaseLabel(step.Phase)) + "</span> ")
			b.WriteString("<strong>" + htmlEscape(reasoningDecisionLabel(step.Decision)) + "</strong>")
			if reasoning := collapseToSingleLine(step.Reasoning); reasoning != "" {
				b.WriteString("<br>" + htmlEscape(reasoning))
			}
			b.WriteString("</div>\n")
		}
		b.WriteString("</details>\n")
	}

	// Bibliography with anchor targets for [n] citation links.
	if len(result.Papers) > 0 {
		b.WriteString("<h2 id=\"bibliography\">Bibliography</h2>\n<ol class=\"bibliography\">\n")
		for idx, paper := range result.Papers {
			b.WriteString(fmt.Sprintf("<li id=\"ref-%d\">", idx+1))
			title := strings.TrimSpace(paper.Title)
			if title == "" {
				title = "(untitled)"
			}
			if link := paperSourceURL(paper); link != "" && isSafeHTTPURL(link) {
				b.WriteString(`<a href="` + htmlEscape(link) + `">` + htmlEscape(title) + "</a>")
			} else {
				b.WriteString(htmlEscape(title))
			}
			var detail []string
			if len(paper.Authors) > 0 {
				detail = append(detail, strings.Join(paper.Authors, ", "))
			}
			if paper.Year > 0 {
				detail = append(detail, strconv.Itoa(paper.Year))
			}
			if venue := strings.TrimSpace(paper.Venue); venue != "" {
				detail = append(detail, venue)
			}
			if paper.CitationCount > 0 {
				detail = append(detail, fmt.Sprintf("%d citations", paper.CitationCount))
			}
			if len(detail) > 0 {
				b.WriteString(`<br><span class="dim">` + htmlEscape(strings.Join(detail, " · ")) + "</span>")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}

	b.WriteString("<footer>Generated by WisDev · " + htmlEscape(time.Now().Format("2006-01-02 15:04")) + " · ScholarLM</footer>\n")
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

// isSafeHTTPURL only admits http/https links into href attributes.
func isSafeHTTPURL(link string) bool {
	lower := strings.ToLower(strings.TrimSpace(link))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

// saveTUIResultHTML writes the self-contained HTML report next to the
// markdown/JSON/CSV/BibTeX exports using the shared naming convention.
func saveTUIResultHTML(path, query string, result *agent.YOLOResult, elapsed time.Duration) (string, error) {
	if result == nil {
		return "", fmt.Errorf("no result available for HTML export")
	}
	target := strings.TrimSpace(path)
	if target == "" {
		target = defaultTUIResultFile(query, "html")
	} else {
		ext := strings.ToLower(filepath.Ext(target))
		switch ext {
		case ".md", ".json", ".bib", ".csv":
			target = strings.TrimSuffix(target, ext) + ".html"
		case "":
			target += ".html"
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil && filepath.Dir(target) != "." {
		return "", err
	}
	content := formatYOLOResultHTML(query, result, elapsed)
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}
