package cli

import (
	"fmt"
	"strings"
	"time"

	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

// maxReasoningAlternatives caps the dim sub-bullets shown per trace entry.
const maxReasoningAlternatives = 4

// reasoningPhaseLabel humanizes a reasoning-trace phase ID for the chip header.
func reasoningPhaseLabel(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "planning":
		return "Planning"
	case "retrieval":
		return "Retrieval"
	case "evaluation":
		return "Evaluation"
	case "replan":
		return "Replan"
	case "synthesis":
		return "Synthesis"
	case "branching":
		return "Branching"
	case "debate":
		return "Debate"
	case "":
		return "Other"
	}
	phase = strings.ReplaceAll(strings.TrimSpace(phase), "_", " ")
	return strings.ToUpper(phase[:1]) + phase[1:]
}

// reasoningPhaseStyle picks a theme color for a phase chip.
func reasoningPhaseStyle(phase string, theme tuiTheme) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "planning":
		return theme.Accent
	case "retrieval":
		return theme.StatusInfo
	case "evaluation":
		return theme.StatusWarn
	case "replan":
		return theme.StatusError
	case "synthesis":
		return theme.HealthOK
	default:
		return theme.DimText
	}
}

// reasoningDecisionLabel renders a machine decision ID as a human label.
func reasoningDecisionLabel(decision string) string {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "cot_plan_summary":
		return "plan: chain-of-thought summary"
	case "pre_retrieval_hypotheses":
		return "plan: pre-retrieval hypotheses"
	case "react_action_retrieve":
		return "act: retrieve"
	case "react_observation_evidence":
		return "observe: evidence"
	case "react_reflect_sufficiency":
		return "reflect: sufficiency"
	case "critique_replan":
		return "replan: critique"
	case "draft":
		return "synthesize: draft"
	case "post_critique_review":
		return "review: post-critique"
	case "memory_context_refresh":
		return "memory: context refresh"
	case "":
		return "step"
	}
	return strings.ReplaceAll(strings.TrimSpace(decision), "_", " ")
}

func reasoningStepTimestamp(millis int64) string {
	if millis <= 0 {
		return "--:--:--"
	}
	return time.UnixMilli(millis).Format("15:04:05")
}

// buildReasoningTraceLines renders the chronological ReAct reasoning timeline,
// grouped by phase with colored phase chips.
func buildReasoningTraceLines(result *agent.YOLOResult, width int, theme tuiTheme) []string {
	var lines []string
	if result == nil || len(result.ReasoningTrace) == 0 {
		lines = append(lines, "")
		lines = append(lines, "  "+themeHeading(theme, "Reasoning trace:"))
		lines = append(lines, "  No reasoning trace recorded for this run.")
		return lines
	}
	contentWidth := resultPaneContentWidth(width)
	lines = append(lines, "")
	lines = append(lines, "  "+themeHeading(theme, fmt.Sprintf("Reasoning trace (%d steps, chronological):", len(result.ReasoningTrace))))

	prevPhase := "\x00sentinel"
	for _, step := range result.ReasoningTrace {
		phase := strings.ToLower(strings.TrimSpace(step.Phase))
		if phase != prevPhase {
			prevPhase = phase
			label := reasoningPhaseLabel(step.Phase)
			ruleWidth := contentWidth - len(label) - 8
			if ruleWidth < 4 {
				ruleWidth = 4
			}
			lines = append(lines, "")
			lines = append(lines,
				"  "+reasoningPhaseStyle(step.Phase, theme)+"◆ "+label+ansiReset+
					" "+theme.DimText+strings.Repeat("─", ruleWidth)+ansiReset)
		}
		lines = append(lines, fmt.Sprintf("   %s%s%s  %s",
			theme.DimText, reasoningStepTimestamp(step.Timestamp), ansiReset,
			theme.BorderLabel+reasoningDecisionLabel(step.Decision)+ansiReset))
		if reasoning := strings.TrimSpace(step.Reasoning); reasoning != "" {
			wrapAt := contentWidth - 13
			if wrapAt < 16 {
				wrapAt = 16
			}
			for _, wl := range wrapText(reasoning, wrapAt) {
				lines = append(lines, "             "+wl)
			}
		}
		alts := step.Alternatives
		shown := len(alts)
		if shown > maxReasoningAlternatives {
			shown = maxReasoningAlternatives
		}
		for idx := 0; idx < shown; idx++ {
			alt := strings.TrimSpace(alts[idx])
			if alt == "" {
				continue
			}
			lines = append(lines, "             "+theme.DimText+"· alt: "+truncateVisible(alt, contentWidth-22)+ansiReset)
		}
		if rest := len(alts) - shown; rest > 0 {
			lines = append(lines, "             "+theme.DimText+fmt.Sprintf("· +%d more alternatives", rest)+ansiReset)
		}
	}
	return lines
}

// collapseToSingleLine flattens reasoning text for one-line markdown export.
func collapseToSingleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// formatReasoningTraceMarkdown emits the "## Reasoning trace" export section,
// collapsed to one line per entry.
func formatReasoningTraceMarkdown(trace []agent.ReasoningStep) string {
	if len(trace) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Reasoning trace\n\n")
	for _, step := range trace {
		line := fmt.Sprintf("- `[%s]` %s", strings.ToLower(reasoningPhaseLabel(step.Phase)), reasoningDecisionLabel(step.Decision))
		if reasoning := collapseToSingleLine(step.Reasoning); reasoning != "" {
			line += " — " + reasoning
		}
		if len(step.Alternatives) > 0 {
			line += fmt.Sprintf(" (%d alternative(s) considered)", len(step.Alternatives))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	return b.String()
}
