package cli

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	internalwisdev "github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

var (
	answerCitationMarkerRe   = regexp.MustCompile(`\[\d+\]`)
	answerBoldMarkdownRe     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	answerItalicMarkdownRe   = regexp.MustCompile(`\*([^*]+)\*`)
	answerEvidenceStrengthRe = regexp.MustCompile(`\[(strong|moderate|exploratory|grounded) evidence\]`)
	answerGroundingWarningRe = regexp.MustCompile(`\[requires verification against retrieved sources\]`)
)

type tuiResultExport struct {
	Query         string             `json:"query"`
	ElapsedSec    float64            `json:"elapsedSec,omitempty"`
	Error         string             `json:"error,omitempty"`
	Result        *agent.YOLOResult    `json:"result,omitempty"`
}

func formatYOLOResultMarkdown(query string, result *agent.YOLOResult, elapsed time.Duration, runErr error, stageLogs []tuiLogEntry) string {
	var b strings.Builder
	b.WriteString("# WisDev Research Result\n\n")
	if q := strings.TrimSpace(query); q != "" {
		b.WriteString("**Question:** ")
		b.WriteString(q)
		b.WriteString("\n\n")
	}
	if result != nil {
		if prepared := strings.TrimSpace(result.PreparedQuery); prepared != "" {
			original := strings.TrimSpace(result.OriginalQuery)
			if original != "" && prepared != original {
				b.WriteString("**Query enhanced:** ")
				b.WriteString(original)
				b.WriteString(" -> ")
				b.WriteString(prepared)
				b.WriteString("\n\n")
			}
		}
	}
	if elapsed > 0 {
		b.WriteString(fmt.Sprintf("**Elapsed:** %.1fs\n\n", elapsed.Seconds()))
	}
	if section := formatStageLogsMarkdown(stageLogs); section != "" {
		b.WriteString(section)
	}

	if runErr != nil {
		b.WriteString("## Error\n\n")
		b.WriteString(runErr.Error())
		b.WriteString("\n")
		return b.String()
	}
	if result == nil {
		b.WriteString("_No results returned._\n")
		return b.String()
	}

	stopReason := strings.TrimSpace(result.StopReason)
	if stopReason == "" && result.Converged {
		stopReason = "converged"
	}
	b.WriteString("## Run summary\n\n")
	if result.RequestedIterations > 0 && result.RequestedIterations != result.Iterations {
		b.WriteString(fmt.Sprintf("- Iterations: %d/%d\n", result.Iterations, result.RequestedIterations))
	} else {
		b.WriteString(fmt.Sprintf("- Iterations: %d\n", result.Iterations))
	}
	b.WriteString(fmt.Sprintf("- Papers found: %d\n", result.PapersFound))
	if stopReason != "" {
		b.WriteString(fmt.Sprintf("- Stop reason: %s\n", stopReason))
		if hint := explainStopReason(stopReason); hint != "" {
			b.WriteString(fmt.Sprintf("- Note: %s\n", hint))
		}
	}
	if domain := strings.TrimSpace(result.DetectedDomain); domain != "" {
		b.WriteString(fmt.Sprintf("- Detected domain: %s\n", domain))
	}
	if mode := strings.TrimSpace(result.SynthesisMode); mode != "" {
		b.WriteString(fmt.Sprintf("- Synthesis: %s\n", mode))
	}
	if card := formatGroundingScorecard(result.Grounding); card != "" {
		b.WriteString("- " + card + "\n")
	}
	if result.Converged {
		b.WriteString("- Converged: yes\n")
	}
	b.WriteString("\n")

	if answer := strings.TrimSpace(result.FinalAnswer); answer != "" {
		b.WriteString("## Final answer\n\n")
		b.WriteString(answer)
		b.WriteString("\n\n")
	}

	if section := formatHypothesesAndBeliefsMarkdown(result); section != "" {
		b.WriteString(section)
	}

	if len(result.PlannedQueries) > 0 {
		b.WriteString("## Planned research agenda\n\n")
		limit := len(result.PlannedQueries)
		if limit > 15 {
			limit = 15
		}
		for idx := 0; idx < limit; idx++ {
			b.WriteString(fmt.Sprintf("- %s\n", result.PlannedQueries[idx]))
		}
		if len(result.PlannedQueries) > limit {
			b.WriteString(fmt.Sprintf("- … +%d more\n", len(result.PlannedQueries)-limit))
		}
		b.WriteString("\n")
	}

	if len(result.BranchPlans) > 0 {
		b.WriteString("## Research branches\n\n")
		order, groups := groupBranchPlansByStrategy(result.BranchPlans)
		written := 0
		for _, strategy := range order {
			if written >= 10 {
				break
			}
			b.WriteString(fmt.Sprintf("### %s\n\n", branchStrategyLabel(strategy)))
			for _, branch := range groups[strategy] {
				if written >= 10 {
					break
				}
				written++
				label := branchPlanLabel(branch)
				line := fmt.Sprintf("- %s %s", branchStatusGlyph(branch.Status), label)
				if status := strings.TrimSpace(branch.Status); status != "" {
					line += " [" + status + "]"
				}
				if stop := strings.TrimSpace(branch.StopReason); stop != "" {
					line += " (stop: " + stop + ")"
				}
				b.WriteString(line + "\n")
				if hyp := strings.TrimSpace(branch.Hypothesis); hyp != "" && branchPlanIsHypothesis(branch) && hyp != label {
					b.WriteString(fmt.Sprintf("  - Hypothesis: %s\n", hyp))
				}
				if fals := strings.TrimSpace(branch.FalsifiabilityCondition); fals != "" {
					b.WriteString(fmt.Sprintf("  - Falsifiable if: %s\n", fals))
				}
			}
			b.WriteString("\n")
		}
		if remaining := len(result.BranchPlans) - written; remaining > 0 {
			b.WriteString(fmt.Sprintf("- … +%d more branches\n\n", remaining))
		}
	}

	if len(result.ExecutedQueries) > 0 {
		b.WriteString("## Executed queries\n\n")
		for _, q := range result.ExecutedQueries {
			b.WriteString(fmt.Sprintf("- %s\n", q))
		}
		b.WriteString("\n")
	}

	if section := formatReasoningTraceMarkdown(result.ReasoningTrace); section != "" {
		b.WriteString(section)
	}

	if len(result.Papers) > 0 {
		b.WriteString("## References\n\n")
		for idx, paper := range result.Papers {
			if idx >= 20 {
				b.WriteString(fmt.Sprintf("- … +%d more\n", len(result.Papers)-20))
				break
			}
			b.WriteString(formatPaperMarkdown(idx+1, paper))
		}
	}

	return b.String()
}

// branchStatusGlyph maps a branch execution status to a compact glyph:
// ✓ retrieved, ○ planned/pending, ✗ no sources / failed.
func branchStatusGlyph(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "retrieved", "completed", "satisfied", "done":
		return "✓"
	case "no_sources", "failed", "abandoned", "error":
		return "✗"
	default:
		return "○"
	}
}

func branchStrategyLabel(strategy string) string {
	s := strings.TrimSpace(strategy)
	if s == "" {
		s = "evidence_grounded_retrieval"
	}
	return strings.ReplaceAll(s, "_", " ")
}

// groupBranchPlansByStrategy groups branch plans by ReasoningStrategy while
// preserving first-seen order so hypothesis branches stay clustered.
func groupBranchPlansByStrategy(plans []agent.BranchPlan) ([]string, map[string][]agent.BranchPlan) {
	var order []string
	groups := make(map[string][]agent.BranchPlan, len(plans))
	for _, plan := range plans {
		key := strings.TrimSpace(plan.ReasoningStrategy)
		if key == "" {
			key = "evidence_grounded_retrieval"
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], plan)
	}
	return order, groups
}

func branchPlanLabel(branch agent.BranchPlan) string {
	label := strings.TrimSpace(branch.Query)
	if label == "" {
		label = strings.TrimSpace(branch.Hypothesis)
	}
	if label == "" {
		label = branch.ID
	}
	return label
}

func branchPlanIsHypothesis(branch agent.BranchPlan) bool {
	return strings.TrimSpace(branch.Hypothesis) != "" ||
		strings.TrimSpace(branch.ReasoningStrategy) == "pre_retrieval_hypothesis_test"
}

func formatYOLOResultBibTeX(result *agent.YOLOResult) string {
	if result == nil || len(result.Papers) == 0 {
		return ""
	}
	var b strings.Builder
	for idx, paper := range result.Papers {
		if idx >= 50 {
			break
		}
		b.WriteString(formatPaperBibTeX(paper, idx))
		b.WriteString("\n")
	}
	return b.String()
}

func saveTUIResultBibTeX(path, query string, result *agent.YOLOResult) (string, error) {
	content := formatYOLOResultBibTeX(result)
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("no references available for BibTeX export")
	}
	target := strings.TrimSpace(path)
	if target == "" {
		stamp := time.Now().Format("20060102-150405")
		target = filepath.Join(".", fmt.Sprintf("wisdev-result-%s.bib", stamp))
	} else {
		ext := strings.ToLower(filepath.Ext(target))
		switch ext {
		case ".md", ".json":
			target = strings.TrimSuffix(target, ext) + ".bib"
		case "":
			target += ".bib"
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil && filepath.Dir(target) != "." {
		return "", err
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func saveTUIResult(path, query string, result *agent.YOLOResult, elapsed time.Duration, runErr error, stageLogs []tuiLogEntry) (string, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		target = defaultTUIResultPath()
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil && filepath.Dir(target) != "." {
		return "", err
	}
	content := formatYOLOResultMarkdown(query, result, elapsed, runErr, stageLogs)
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func defaultTUIResultPath() string {
	stamp := time.Now().Format("20060102-150405")
	return filepath.Join(".", fmt.Sprintf("wisdev-result-%s.md", stamp))
}

func defaultTUIResultJSONPath() string {
	stamp := time.Now().Format("20060102-150405")
	return filepath.Join(".", fmt.Sprintf("wisdev-result-%s.json", stamp))
}

func saveTUIResultJSON(path, query string, result *agent.YOLOResult, elapsed time.Duration, runErr error) (string, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		target = defaultTUIResultJSONPath()
	} else if strings.HasSuffix(strings.ToLower(target), ".md") {
		target = strings.TrimSuffix(target, filepath.Ext(target)) + ".json"
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil && filepath.Dir(target) != "." {
		return "", err
	}
	payload := tuiResultExport{
		Query: query,
		Result: result,
	}
	if elapsed > 0 {
		payload.ElapsedSec = elapsed.Seconds()
	}
	if runErr != nil {
		payload.Error = runErr.Error()
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func parseMouseScrollDelta(b []byte) int {
	if len(b) < 6 || b[0] != 27 || b[1] != '[' || b[2] != '<' {
		return 0
	}
	s := string(b)
	if !strings.HasSuffix(s, "M") && !strings.HasSuffix(s, "m") {
		return 0
	}
	inner := s[3 : len(s)-1]
	parts := strings.Split(inner, ";")
	if len(parts) < 1 {
		return 0
	}
	btn, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	switch btn {
	case 64:
		return -3
	case 65:
		return 3
	default:
		return 0
	}
}

func highlightAnswerMarkers(line string, theme tuiTheme) string {
	if plainUI() || strings.TrimSpace(line) == "" {
		return line
	}
	line = answerBoldMarkdownRe.ReplaceAllString(line, theme.Accent+"$1"+ansiReset)
	line = answerEvidenceStrengthRe.ReplaceAllString(line, theme.StatusWarn+"[$1 evidence]"+ansiReset)
	line = answerGroundingWarningRe.ReplaceAllString(line, theme.StatusError+"[requires verification against retrieved sources]"+ansiReset)
	line = answerCitationMarkerRe.ReplaceAllStringFunc(line, func(marker string) string {
		return theme.Accent + marker + ansiReset
	})
	line = answerItalicMarkdownRe.ReplaceAllString(line, theme.DimText+"$1"+ansiReset)
	return line
}

func isNumberedAnswerListItem(line string) bool {
	dot := strings.Index(line, ". ")
	if dot <= 0 || dot > 3 {
		return false
	}
	for _, r := range line[:dot] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func resultPaneContentWidth(termWidth int) int {
	// │ + space + content + scrollbar gutter + │
	content := termWidth - 9
	if content < 24 {
		return 24
	}
	return content
}

func appendWrappedAnswerLines(lines []string, text string, contentWidth int, theme tuiTheme, indent int) []string {
	prefix := strings.Repeat(" ", indent)
	plain := removeEscapeSequences(text)
	wrapAt := contentWidth - indent
	if wrapAt < 16 {
		wrapAt = 16
	}
	for _, wl := range wrapText(plain, wrapAt) {
		lines = append(lines, prefix+highlightAnswerMarkers(wl, theme))
	}
	return lines
}

// appendDimAnswerLines renders meta-section body text (grounding audit, loop
// critique gaps) dimmed so it reads as run metadata, not answer content.
func appendDimAnswerLines(lines []string, text string, contentWidth int, theme tuiTheme, indent int) []string {
	prefix := strings.Repeat(" ", indent)
	plain := removeEscapeSequences(text)
	wrapAt := contentWidth - indent
	if wrapAt < 16 {
		wrapAt = 16
	}
	for _, wl := range wrapText(plain, wrapAt) {
		lines = append(lines, prefix+theme.DimText+wl+ansiReset)
	}
	return lines
}

// isMetaAnswerSection reports whether an answer "##" heading introduces a
// loop-meta section that should be styled as metadata rather than answer text.
func isMetaAnswerSection(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "grounding audit", "loop critique gaps":
		return true
	}
	return false
}

func formatAnswerLinesForTUI(answer string, width int, theme tuiTheme) []string {
	return formatAnswerLinesForTUIGrounded(answer, width, theme, nil)
}

// formatAnswerLinesForTUIGrounded renders the final answer and, when per-claim
// grounding stats are available, appends a dim "(grounded/total cited)" suffix
// to section headings that match structured-answer sections by title.
func formatAnswerLinesForTUIGrounded(answer string, width int, theme tuiTheme, grounding *agent.GroundingStats) []string {
	contentWidth := resultPaneContentWidth(width)
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return nil
	}
	var lines []string
	dimMeta := false
	for _, paragraph := range strings.Split(answer, "\n") {
		trimmed := strings.TrimSpace(paragraph)
		if trimmed == "" {
			lines = append(lines, "")
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "# "):
			dimMeta = false
			lines = append(lines, "")
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			if strings.HasPrefix(title, "Provisional Research Synthesis:") {
				title = strings.TrimSpace(strings.TrimPrefix(title, "Provisional Research Synthesis:"))
			}
			lines = append(lines, "  "+themeHeading(theme, title))
		case strings.HasPrefix(trimmed, "## "):
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "##"))
			dimMeta = isMetaAnswerSection(title)
			lines = append(lines, "")
			if dimMeta {
				lines = append(lines, "  "+theme.DimText+title+ansiReset)
			} else {
				heading := "  " + themeHeading(theme, title)
				if suffix := sectionGroundingSuffix(grounding, title); suffix != "" {
					heading += "  " + theme.DimText + suffix + ansiReset
				}
				lines = append(lines, heading)
			}
		case strings.HasPrefix(trimmed, "### "):
			if dimMeta {
				lines = append(lines, "  "+theme.DimText+strings.TrimSpace(strings.TrimPrefix(trimmed, "###"))+ansiReset)
			} else {
				lines = append(lines, "  "+theme.Accent+strings.TrimSpace(strings.TrimPrefix(trimmed, "###"))+ansiReset)
			}
		case dimMeta:
			lines = appendDimAnswerLines(lines, strings.TrimPrefix(trimmed, "> "), contentWidth, theme, 2)
		case strings.HasPrefix(trimmed, "> "):
			lines = appendWrappedAnswerLines(lines, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), contentWidth, theme, 2)
		case strings.HasPrefix(trimmed, "- "):
			lines = appendWrappedAnswerLines(lines, trimmed, contentWidth, theme, 2)
		case isNumberedAnswerListItem(trimmed):
			lines = appendWrappedAnswerLines(lines, trimmed, contentWidth, theme, 2)
		default:
			lines = appendWrappedAnswerLines(lines, trimmed, contentWidth, theme, 2)
		}
	}
	return lines
}

func buildTUIResultLines(s *tuiState, width int, pane tuiResultPane) []string {
	theme := activeTheme()
	var lines []string
	if s.runError != nil {
		lines = append(lines, "")
		lines = append(lines, "  "+themeHeading(theme, "Error during research run:"))
		lines = append(lines, "  "+s.runError.Error())
		return lines
	}
	if s.result == nil {
		lines = append(lines, "  No results returned.")
		return lines
	}

	showAll := pane == resultPaneAll
	if showAll || pane == resultPaneAnswer || pane == resultPaneHypotheses || pane == resultPaneQueries || pane == resultPaneSources {
		stopReason := strings.TrimSpace(s.result.StopReason)
		if stopReason == "" && s.result.Converged {
			stopReason = "converged"
		}
		iterLabel := fmt.Sprintf("%d", s.result.Iterations)
		if requested := s.result.RequestedIterations; requested > 0 {
			iterLabel = fmt.Sprintf("%d/%d", s.result.Iterations, requested)
		} else if s.requestedIterations > 0 {
			iterLabel = fmt.Sprintf("%d/%d", s.result.Iterations, s.requestedIterations)
		}
		status := fmt.Sprintf("  %s iterations=%s papers=%d", themeHeading(theme, "Status:"), iterLabel, s.result.PapersFound)
		if s.deepSearch {
			status += " exhaustive=yes"
		}
		if stopReason != "" {
			status += " stop=" + stopReason
		}
		if s.result.Converged {
			status += " converged=yes"
		}
		if showAll || pane == resultPaneAnswer {
			status += " synthesis=" + internalwisdev.SynthesisPipelineVersion
			if mode := strings.TrimSpace(s.result.SynthesisMode); mode != "" {
				status += " mode=" + mode
				if mode == "heuristic" && s.degradedSteps > 0 {
					status += fmt.Sprintf(" (%d degraded steps)", s.degradedSteps)
				}
			}
			if strings.TrimSpace(s.result.SynthesisMode) == "heuristic" {
				status += " llm=" + strings.TrimSpace(s.llmBackend)
			}
		}
		if domain := strings.TrimSpace(s.result.DetectedDomain); domain != "" && (showAll || pane == resultPaneQueries) {
			status += " domain=" + domain
		}
		if s.completedElapsed > 0 {
			status += fmt.Sprintf(" elapsed=%.1fs", s.completedElapsed.Seconds())
		}
		if pane != resultPaneAll {
			status += " view=" + pane.label()
		}
		if s.batchMode && len(s.batchQueries) > 0 {
			status += fmt.Sprintf(" batch=%d/%d", s.batchQueryIdx+1, len(s.batchQueries))
		}
		lines = append(lines, status)
		if card := formatGroundingScorecard(s.result.Grounding); card != "" && (showAll || pane == resultPaneAnswer) {
			style := theme.DimText
			if groundingNeedsAttention(s.result.Grounding) {
				style = theme.StatusWarn
			}
			lines = append(lines, "  "+style+card+ansiReset)
		}
		if hint := explainStopReasonForState(stopReason, s.deepSearch, s.result.Iterations, s.result.RequestedIterations); hint != "" && (showAll || pane == resultPaneAnswer) {
			lines = append(lines, "  "+theme.DimText+hint+ansiReset)
		}
		if synthHint := explainSynthesisModeHint(s.result.SynthesisMode, s.llmBackend); synthHint != "" && (showAll || pane == resultPaneAnswer) {
			lines = append(lines, "  "+theme.DimText+synthHint+ansiReset)
		}
		lines = append(lines, "")
	}

	if showAll || pane == resultPaneAnswer {
		if prepared := strings.TrimSpace(s.result.PreparedQuery); prepared != "" {
			original := strings.TrimSpace(s.result.OriginalQuery)
			if original == "" {
				original = strings.TrimSpace(s.query)
			}
			if original != "" && prepared != original {
				lines = append(lines, "  "+themeHeading(theme, "Query enhanced:"))
				for _, wl := range wrapText(original+" -> "+prepared, width-8) {
					lines = append(lines, "  "+wl)
				}
				lines = append(lines, "")
			}
		}
		lines = append(lines, "  "+themeHeading(theme, "Research Answer:"))
		for _, wl := range formatAnswerLinesForTUIGrounded(s.result.FinalAnswer, width, theme, s.result.Grounding) {
			lines = append(lines, wl)
		}
		if len(s.result.Papers) > 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "Top references (author · year · citations):"))
			maxCited := len(s.result.Papers)
			if maxCited > 5 {
				maxCited = 5
			}
			for idx := 0; idx < maxCited; idx++ {
				paper := s.result.Papers[idx]
				title := strings.TrimSpace(paper.Title)
				if title == "" {
					continue
				}
				lines = append(lines, fmt.Sprintf("  [%d] %s", idx+1, hyperlinkedPaperTitle(paper, truncateVisible(title, width-12))))
				if link := paperSourceURL(paper); link != "" && plainUI() {
					lines = append(lines, "      "+link)
				}
				lines = append(lines, "      "+formatPaperCitationLine(paper))
			}
			if len(s.result.Papers) > maxCited {
				lines = append(lines, fmt.Sprintf("  … +%d more in Sources pane (s)", len(s.result.Papers)-maxCited))
			}
		}
	}

	if showAll || pane == resultPaneHypotheses {
		lines = append(lines, buildHypothesesPaneLines(s.result, width, theme, showAll)...)
	}

	if showAll || pane == resultPaneQueries {
		coverageLines := buildCoverageGapLines(s.result, width, theme)
		lines = append(lines, coverageLines...)
		// The coverage block already accounts for every planned query
		// (executed ones appear under Executed Search Queries, unexecuted
		// ones as ○ entries), so the agenda list only renders without it.
		if len(s.result.PlannedQueries) > 0 && len(coverageLines) == 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "Planned Research Agenda:"))
			limit := len(s.result.PlannedQueries)
			if limit > 12 {
				limit = 12
			}
			for idx := 0; idx < limit; idx++ {
				lines = append(lines, fmt.Sprintf("  - %s", s.result.PlannedQueries[idx]))
			}
			if len(s.result.PlannedQueries) > limit {
				lines = append(lines, fmt.Sprintf("  … +%d more planned", len(s.result.PlannedQueries)-limit))
			}
		}
		if len(s.result.BranchPlans) > 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "Research Branches")+" "+theme.DimText+"(✓ retrieved · ○ planned · ✗ no sources)"+ansiReset)
			order, groups := groupBranchPlansByStrategy(s.result.BranchPlans)
			shown := 0
			const maxBranches = 8
			for _, strategy := range order {
				if shown >= maxBranches {
					break
				}
				lines = append(lines, "  "+theme.Accent+branchStrategyLabel(strategy)+ansiReset)
				for _, branch := range groups[strategy] {
					if shown >= maxBranches {
						break
					}
					shown++
					glyph := branchStatusGlyph(branch.Status)
					switch glyph {
					case "✓":
						glyph = theme.HealthOK + glyph + ansiReset
					case "✗":
						glyph = theme.HealthBad + glyph + ansiReset
					default:
						glyph = theme.DimText + glyph + ansiReset
					}
					label := branchPlanLabel(branch)
					entry := fmt.Sprintf("    %s %s", glyph, truncateVisible(label, width-16))
					if status := strings.TrimSpace(branch.Status); status != "" {
						entry += " [" + status + "]"
					}
					lines = append(lines, entry)
					if hyp := strings.TrimSpace(branch.Hypothesis); hyp != "" && branchPlanIsHypothesis(branch) && hyp != label {
						lines = append(lines, "       "+theme.DimText+"hypothesis: "+truncateVisible(hyp, width-26)+ansiReset)
					}
					if stop := strings.TrimSpace(branch.StopReason); stop != "" {
						lines = append(lines, "       "+theme.DimText+"stop: "+stop+ansiReset)
					}
				}
			}
			if remaining := len(s.result.BranchPlans) - shown; remaining > 0 {
				lines = append(lines, fmt.Sprintf("  … +%d more branches", remaining))
			}
		}
		if len(s.result.ExecutedQueries) > 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "Executed Search Queries:"))
			for _, q := range s.result.ExecutedQueries {
				lines = append(lines, fmt.Sprintf("  * %s", q))
			}
		}
	}

	if showAll || pane == resultPaneSources {
		if len(s.result.Papers) > 0 {
			maxShow := len(s.result.Papers)
			if showAll {
				if maxShow > 8 {
					maxShow = 8
				}
			} else if maxShow > 25 {
				maxShow = 25
			}
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "References (cited papers, highest first):"))
			for idx := 0; idx < maxShow; idx++ {
				paper := s.result.Papers[idx]
				lines = append(lines, fmt.Sprintf("  [%d] %s", idx+1, hyperlinkedPaperTitle(paper, strings.TrimSpace(paper.Title))))
				lines = append(lines, "      "+formatPaperCitationLine(paper))
				if abstract := strings.TrimSpace(paper.Abstract); abstract != "" && pane == resultPaneSources {
					for _, wl := range wrapText(abstract, width-10) {
						lines = append(lines, "      "+truncateVisible(wl, width-10))
					}
				}
				if link := paperSourceURL(paper); link != "" {
					lines = append(lines, "      "+link)
				}
			}
			if len(s.result.Papers) > maxShow {
				lines = append(lines, fmt.Sprintf("  … +%d more sources", len(s.result.Papers)-maxShow))
			}
		} else if pane == resultPaneSources {
			lines = append(lines, "")
			lines = append(lines, "  No source publications in this run.")
		}
	}

	if pane == resultPaneReasoning {
		lines = append(lines, buildReasoningTraceLines(s.result, width, theme)...)
	}

	if pane == resultPaneCompare && s.prevResult == nil {
		lines = append(lines, "  "+themeHeading(theme, "Results Comparison:"))
		lines = append(lines, "")
		lines = append(lines, "  Re-run with r or E to capture a previous result, then compare deltas here.")
	} else if pane == resultPaneCompare && s.prevResult != nil {
		lines = append(lines, "  "+themeHeading(theme, "Results Comparison (Previous vs. Current):"))
		lines = append(lines, "")
		
		// 1. Answer delta
		lines = append(lines, "  "+themeHeading(theme, "Answer Delta:"))
		if s.prevResult.FinalAnswer == s.result.FinalAnswer {
			lines = append(lines, "    No changes in the final answer.")
		} else {
			lines = append(lines, "    [Answer has changed!]")
			prevLinesCount := len(strings.Split(s.prevResult.FinalAnswer, "\n"))
			currLinesCount := len(strings.Split(s.result.FinalAnswer, "\n"))
			lines = append(lines, fmt.Sprintf("    Previous answer: %d lines | Current answer: %d lines", prevLinesCount, currLinesCount))
		}
		lines = append(lines, "")

		// 2. Hypotheses delta
		lines = append(lines, "  "+themeHeading(theme, "Hypotheses Delta:"))
		prevHypCount := len(s.prevResult.Hypotheses)
		currHypCount := len(s.result.Hypotheses)
		lines = append(lines, fmt.Sprintf("    Previous: %d hypotheses | Current: %d hypotheses", prevHypCount, currHypCount))
		
		prevClaims := make(map[string]float64)
		for _, h := range s.prevResult.Hypotheses {
			prevClaims[strings.ToLower(strings.TrimSpace(h.Claim))] = h.ConfidenceScore
		}
		
		newHypCount := 0
		for _, h := range s.result.Hypotheses {
			claim := strings.ToLower(strings.TrimSpace(h.Claim))
			if _, ok := prevClaims[claim]; !ok {
				newHypCount++
				lines = append(lines, fmt.Sprintf("    * %s[NEW]%s [Score: %.2f] %s", theme.Accent, ansiReset, h.ConfidenceScore, h.Claim))
			} else {
				prevScore := prevClaims[claim]
				if math.Abs(h.ConfidenceScore-prevScore) > 0.01 {
					diff := h.ConfidenceScore - prevScore
					sign := "+"
					if diff < 0 { sign = "" }
					lines = append(lines, fmt.Sprintf("    * [Score change: %.2f -> %.2f (%s%.2f)] %s", prevScore, h.ConfidenceScore, sign, diff, h.Claim))
				}
			}
		}
		if newHypCount == 0 && len(s.result.Hypotheses) == len(s.prevResult.Hypotheses) {
			lines = append(lines, "    No new hypotheses generated.")
		}
		lines = append(lines, "")

		// 3. Papers delta (new papers found)
		lines = append(lines, "  "+themeHeading(theme, "Papers Found Delta:"))
		prevPapers := make(map[string]bool)
		for _, p := range s.prevResult.Papers {
			prevPapers[strings.ToLower(strings.TrimSpace(p.Title))] = true
		}
		
		newPapersCount := 0
		for _, p := range s.result.Papers {
			title := strings.ToLower(strings.TrimSpace(p.Title))
			if !prevPapers[title] {
				newPapersCount++
				lines = append(lines, fmt.Sprintf("    + %s[NEW PAPER]%s %s", theme.Accent, ansiReset, p.Title))
			}
		}
		lines = append(lines, fmt.Sprintf("    Total previous: %d | Total current: %d | New papers: %d", len(s.prevResult.Papers), len(s.result.Papers), newPapersCount))
	}

	if len(lines) == 0 {
		lines = append(lines, "  No content in this pane.")
	}
	return lines
}

func paperSourceURL(paper agent.Paper) string {
	link := firstNonEmpty(paper.OpenAccessURL, paper.PDFURL, paper.Link)
	if link == "" && strings.TrimSpace(paper.DOI) != "" {
		link = formatPaperDOI(paper.DOI)
	}
	return link
}

// hyperlinkedPaperTitle wraps a rendered paper title in an OSC-8 terminal
// hyperlink when the paper has a source URL. Plain mode (NO_COLOR/WISDEV_PLAIN)
// returns the title unchanged; callers surface the raw URL on its own line.
func hyperlinkedPaperTitle(paper agent.Paper, title string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}
	link := paperSourceURL(paper)
	if link == "" || plainUI() {
		return title
	}
	return terminalHyperlink(link, title)
}

func formatPaperMarkdown(index int, paper agent.Paper) string {
	line := fmt.Sprintf("%d. %s\n", index, formatPaperBibliography(paper))
	if link := paperSourceURL(paper); link != "" {
		line += "   - Link: " + link + "\n"
	}
	if paper.ArxivID != "" {
		line += "   - arXiv: " + paper.ArxivID + "\n"
	}
	return line
}

func saveTUIResultCSV(path string, result *agent.YOLOResult) (string, error) {
	if result == nil || len(result.Papers) == 0 {
		return "", fmt.Errorf("no papers available for CSV export")
	}
	target := strings.TrimSpace(path)
	if target == "" {
		stamp := time.Now().Format("20060102-150405")
		target = filepath.Join(".", fmt.Sprintf("wisdev-result-%s.csv", stamp))
	} else {
		ext := strings.ToLower(filepath.Ext(target))
		switch ext {
		case ".md", ".json", ".bib":
			target = strings.TrimSuffix(target, ext) + ".csv"
		case "":
			target += ".csv"
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil && filepath.Dir(target) != "." {
		return "", err
	}
	
	file, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer file.Close()
	
	writer := csv.NewWriter(file)
	defer writer.Flush()
	
	headers := []string{"Title", "Authors", "Year", "DOI", "Citations", "Source"}
	if err := writer.Write(headers); err != nil {
		return "", err
	}
	
	for _, paper := range result.Papers {
		authors := strings.Join(paper.Authors, "; ")
		yearStr := ""
		if paper.Year > 0 {
			yearStr = fmt.Sprintf("%d", paper.Year)
		}
		citationsStr := fmt.Sprintf("%d", paper.CitationCount)
		link := paperSourceURL(paper)
		row := []string{
			paper.Title,
			authors,
			yearStr,
			paper.DOI,
			citationsStr,
			link,
		}
		if err := writer.Write(row); err != nil {
			return "", err
		}
	}
	
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

