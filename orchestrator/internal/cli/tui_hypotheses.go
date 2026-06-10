package cli

import (
	"fmt"
	"strings"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

// confidenceBarWidth is the glyph width of hypothesis/belief confidence bars.
const confidenceBarWidth = 10

// maxHypothesesInAllPane caps hypotheses shown in the combined "All" pane.
const maxHypothesesInAllPane = 6

// confidenceBandStyle maps a confidence score to a theme style:
// >=0.7 healthy, 0.4-0.7 neutral (no extra color), <0.4 warn.
func confidenceBandStyle(score float64, theme tuiTheme) string {
	switch {
	case score >= 0.7:
		return theme.HealthOK
	case score < 0.4:
		return theme.StatusWarn
	default:
		return ""
	}
}

// renderConfidenceBar renders `▓▓▓▓▓░░░░░ 0.52` colored by confidence band.
// Plain mode (NO_COLOR/WISDEV_PLAIN) emits the bar without escape sequences.
func renderConfidenceBar(score float64, theme tuiTheme) string {
	clamped := score
	if clamped < 0 {
		clamped = 0
	}
	if clamped > 1 {
		clamped = 1
	}
	filled := int(clamped*float64(confidenceBarWidth) + 0.5)
	if filled > confidenceBarWidth {
		filled = confidenceBarWidth
	}
	bar := strings.Repeat("▓", filled) + strings.Repeat("░", confidenceBarWidth-filled)
	label := fmt.Sprintf("%s %.2f", bar, score)
	if plainUI() {
		return label
	}
	if style := confidenceBandStyle(score, theme); style != "" {
		return style + label + ansiReset
	}
	return label
}

// hypothesisStatusStyle styles a hypothesis/belief status tag.
func hypothesisStatusStyle(status string, theme tuiTheme) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "supported", "confirmed", "verified", "active":
		return theme.HealthOK
	case "refuted", "rejected", "terminated":
		return theme.StatusError
	case "revised":
		return theme.StatusWarn
	default:
		return theme.DimText
	}
}

func hypothesisStatusTag(status string, theme tuiTheme) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return ""
	}
	tag := "[" + status + "]"
	if plainUI() {
		return tag
	}
	return hypothesisStatusStyle(status, theme) + tag + ansiReset
}

// buildHypothesesPaneLines renders the Hypotheses pane: per-hypothesis
// confidence bars with status tags and falsifiability notes, followed by the
// belief ledger and a Tensions subsection for contradicted beliefs.
func buildHypothesesPaneLines(result *agent.YOLOResult, width int, theme tuiTheme, showAll bool) []string {
	var lines []string
	if result == nil || (len(result.Hypotheses) == 0 && len(result.Beliefs) == 0) {
		if !showAll {
			lines = append(lines, "")
			lines = append(lines, "  "+themeHeading(theme, "Hypotheses & beliefs:"))
			lines = append(lines, "  No hypotheses or beliefs recorded for this run.")
			lines = append(lines, "  "+theme.DimText+"Enable the Hypotheses setting on the input screen to generate them."+ansiReset)
		}
		return lines
	}
	contentWidth := resultPaneContentWidth(width)

	if len(result.Hypotheses) > 0 {
		lines = append(lines, "")
		lines = append(lines, "  "+themeHeading(theme, fmt.Sprintf("Hypotheses (%d):", len(result.Hypotheses))))
		limit := len(result.Hypotheses)
		if showAll && limit > maxHypothesesInAllPane {
			limit = maxHypothesesInAllPane
		}
		for idx := 0; idx < limit; idx++ {
			hyp := result.Hypotheses[idx]
			header := "  " + renderConfidenceBar(hyp.ConfidenceScore, theme)
			if tag := hypothesisStatusTag(hyp.Status, theme); tag != "" {
				header += " " + tag
			}
			lines = append(lines, header)
			wrapAt := contentWidth - 4
			if wrapAt < 16 {
				wrapAt = 16
			}
			for _, wl := range wrapText(strings.TrimSpace(hyp.Claim), wrapAt) {
				lines = append(lines, "    "+wl)
			}
			if fals := strings.TrimSpace(hyp.FalsifiabilityCondition); fals != "" {
				for _, wl := range wrapText("falsifiable if: "+fals, wrapAt-2) {
					lines = append(lines, "      "+theme.DimText+wl+ansiReset)
				}
			}
		}
		if rest := len(result.Hypotheses) - limit; rest > 0 {
			lines = append(lines, fmt.Sprintf("  … +%d more in Hypotheses pane (y)", rest))
		}
	}

	if len(result.Beliefs) > 0 {
		lines = append(lines, "")
		lines = append(lines, "  "+themeHeading(theme, fmt.Sprintf("Beliefs (%d):", len(result.Beliefs))))
		wrapAt := contentWidth - 4
		if wrapAt < 16 {
			wrapAt = 16
		}
		for _, belief := range result.Beliefs {
			header := "  " + renderConfidenceBar(belief.Confidence, theme)
			if tag := hypothesisStatusTag(belief.Status, theme); tag != "" {
				header += " " + tag
			}
			if belief.SupportCount > 0 || belief.ContradictionCount > 0 {
				header += " " + theme.DimText + fmt.Sprintf("(%d for / %d against)", belief.SupportCount, belief.ContradictionCount) + ansiReset
			}
			lines = append(lines, header)
			for _, wl := range wrapText(belief.Claim, wrapAt) {
				lines = append(lines, "    "+wl)
			}
		}

		if tensions := contradictedBeliefs(result.Beliefs); len(tensions) > 0 {
			lines = append(lines, "")
			lines = append(lines, "  "+theme.StatusWarn+"Tensions (beliefs with contradicting evidence):"+ansiReset)
			for _, belief := range tensions {
				note := fmt.Sprintf("%s (%d supporting vs %d contradicting)", belief.Claim, belief.SupportCount, belief.ContradictionCount)
				for i, wl := range wrapText(note, wrapAt-2) {
					prefix := "    ⚡ "
					if i > 0 {
						prefix = "      "
					}
					lines = append(lines, prefix+wl)
				}
			}
		}
	}
	return lines
}

// contradictedBeliefs filters beliefs that carry contradicting evidence.
func contradictedBeliefs(beliefs []agent.Belief) []agent.Belief {
	var out []agent.Belief
	for _, belief := range beliefs {
		if belief.ContradictionCount > 0 {
			out = append(out, belief)
		}
	}
	return out
}

// resolveCoverageGaps returns the public gap summary, deriving a minimal one
// from planned/executed queries when the run (or an older saved run) carries
// no explicit gap analysis. Returns nil when nothing useful can be shown.
func resolveCoverageGaps(result *agent.YOLOResult) *agent.CoverageGaps {
	if result == nil {
		return nil
	}
	if result.Gaps != nil {
		return result.Gaps
	}
	if len(result.PlannedQueries) == 0 {
		return nil
	}
	executed := make(map[string]struct{}, len(result.ExecutedQueries))
	for _, q := range result.ExecutedQueries {
		executed[strings.ToLower(strings.TrimSpace(q))] = struct{}{}
	}
	derived := &agent.CoverageGaps{
		Sufficient:        true,
		PlannedQueryCount: len(result.PlannedQueries),
	}
	for _, q := range result.PlannedQueries {
		if _, ok := executed[strings.ToLower(strings.TrimSpace(q))]; ok {
			derived.ExecutedQueryCount++
		} else {
			derived.UnexecutedPlannedQueries = append(derived.UnexecutedPlannedQueries, q)
		}
	}
	return derived
}

// buildCoverageGapLines renders the consolidated coverage/gap block at the
// top of the Queries pane: executed-vs-planned counts, unexecuted planned
// queries dimmed with ○, and open-gap bullets from the critique step.
func buildCoverageGapLines(result *agent.YOLOResult, width int, theme tuiTheme) []string {
	gaps := resolveCoverageGaps(result)
	if gaps == nil {
		return nil
	}
	contentWidth := resultPaneContentWidth(width)
	var lines []string
	lines = append(lines, "")

	planned := gaps.PlannedQueryCount
	executed := gaps.ExecutedQueryCount
	if planned <= 0 {
		planned = len(result.PlannedQueries)
	}
	if executed <= 0 && planned > 0 {
		executed = planned - len(gaps.UnexecutedPlannedQueries)
		if executed < 0 {
			executed = 0
		}
	}
	header := fmt.Sprintf("Coverage: %d/%d planned queries executed", executed, planned)
	style := theme.HealthOK
	if len(gaps.UnexecutedPlannedQueries) > 0 || !gaps.Sufficient {
		style = theme.StatusWarn
	}
	if plainUI() {
		lines = append(lines, "  "+header)
	} else {
		lines = append(lines, "  "+style+header+ansiReset)
	}

	const maxUnexecuted = 8
	for idx, q := range gaps.UnexecutedPlannedQueries {
		if idx >= maxUnexecuted {
			lines = append(lines, "    "+theme.DimText+fmt.Sprintf("○ … +%d more unexecuted", len(gaps.UnexecutedPlannedQueries)-maxUnexecuted)+ansiReset)
			break
		}
		lines = append(lines, "    "+theme.DimText+"○ "+truncateVisible(q, contentWidth-6)+ansiReset)
	}

	openGaps := append([]string(nil), gaps.MissingAspects...)
	for _, q := range gaps.QueriesWithoutCoverage {
		openGaps = append(openGaps, "no sources found for: "+q)
	}
	if len(openGaps) > 0 {
		lines = append(lines, "  "+themeHeading(theme, "Open gaps:"))
		const maxGaps = 8
		for idx, gap := range openGaps {
			if idx >= maxGaps {
				lines = append(lines, fmt.Sprintf("    … +%d more gaps", len(openGaps)-maxGaps))
				break
			}
			wrapAt := contentWidth - 6
			if wrapAt < 16 {
				wrapAt = 16
			}
			for i, wl := range wrapText(gap, wrapAt) {
				prefix := "    • "
				if i > 0 {
					prefix = "      "
				}
				lines = append(lines, prefix+wl)
			}
		}
	} else if gaps.Sufficient && len(gaps.UnexecutedPlannedQueries) == 0 {
		lines = append(lines, "  "+theme.DimText+"No open coverage gaps."+ansiReset)
	}
	if reasoning := strings.TrimSpace(gaps.Reasoning); reasoning != "" && !gaps.Sufficient {
		wrapAt := contentWidth - 4
		if wrapAt < 16 {
			wrapAt = 16
		}
		for _, wl := range wrapText(reasoning, wrapAt) {
			lines = append(lines, "    "+theme.DimText+wl+ansiReset)
		}
	}
	return lines
}

// formatHypothesesAndBeliefsMarkdown emits the "## Hypotheses & beliefs"
// markdown export section mirroring the Hypotheses pane.
func formatHypothesesAndBeliefsMarkdown(result *agent.YOLOResult) string {
	if result == nil || (len(result.Hypotheses) == 0 && len(result.Beliefs) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Hypotheses & beliefs\n\n")
	for _, hyp := range result.Hypotheses {
		status := ""
		if hyp.Status != "" {
			status = fmt.Sprintf(" (%s)", hyp.Status)
		}
		line := fmt.Sprintf("- [%.2f]%s %s", hyp.ConfidenceScore, status, hyp.Claim)
		if q := strings.TrimSpace(hyp.Query); q != "" {
			line += fmt.Sprintf(" (query: %s)", truncateVisible(q, 80))
		}
		b.WriteString(line + "\n")
		if fals := strings.TrimSpace(hyp.FalsifiabilityCondition); fals != "" {
			b.WriteString(fmt.Sprintf("  - Falsifiable if: %s\n", fals))
		}
	}
	if len(result.Beliefs) > 0 {
		b.WriteString("\n### Beliefs\n\n")
		for _, belief := range result.Beliefs {
			status := ""
			if belief.Status != "" {
				status = fmt.Sprintf(" (%s)", belief.Status)
			}
			line := fmt.Sprintf("- [%.2f]%s %s", belief.Confidence, status, belief.Claim)
			if belief.SupportCount > 0 || belief.ContradictionCount > 0 {
				line += fmt.Sprintf(" — %d supporting / %d contradicting", belief.SupportCount, belief.ContradictionCount)
			}
			b.WriteString(line + "\n")
		}
		if tensions := contradictedBeliefs(result.Beliefs); len(tensions) > 0 {
			b.WriteString("\n### Tensions\n\n")
			for _, belief := range tensions {
				b.WriteString(fmt.Sprintf("- %s (%d supporting vs %d contradicting)\n", belief.Claim, belief.SupportCount, belief.ContradictionCount))
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}
