package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	internal "github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
	agent "github.com/wisdev/wisdev-agent-os/orchestrator/pkg/wisdev"
)

func (s *tuiState) handleProgressEvent(event agent.ProgressEvent) {
	msg, tag := formatProgressEvent(event)
	if msg == "" {
		return
	}
	if progressEventDegraded(event) {
		s.logMutex.Lock()
		s.degradedSteps++
		s.logMutex.Unlock()
	}
	s.updateRunCountersFromPayload(event.Payload)
	s.addLog(msg, tag)
}

// updateRunCountersFromPayload keeps the running-pane counters fresh from
// stage payloads (several loop stages report the cumulative paper count).
func (s *tuiState) updateRunCountersFromPayload(payload map[string]any) {
	if len(payload) == 0 {
		return
	}
	s.logMutex.Lock()
	defer s.logMutex.Unlock()
	if n, ok := payloadInt(payload["paperCount"]); ok && n > s.papersFound {
		s.papersFound = n
	}
	if n, ok := payloadInt(payload["iteration"]); ok && n > s.iterations {
		s.iterations = n
	}
	// Only "provider_result_counts" (emitted once per completed query) is
	// aggregated; "providers" on admission events repeats the same map and
	// would double-count.
	if counts := payloadProviderCounts(payload["provider_result_counts"]); len(counts) > 0 {
		if s.providerCounts == nil {
			s.providerCounts = make(map[string]int, len(counts))
		}
		for name, n := range counts {
			s.providerCounts[name] += n
		}
	}
}

// payloadProviderCounts extracts a provider→count map from a progress payload
// value. Events delivered in-process carry map[string]int; JSON round-trips
// produce map[string]any with float64 values.
func payloadProviderCounts(value any) map[string]int {
	switch typed := value.(type) {
	case map[string]int:
		return typed
	case map[string]any:
		counts := make(map[string]int, len(typed))
		for name, raw := range typed {
			if n, ok := payloadInt(raw); ok {
				counts[name] = n
			}
		}
		return counts
	}
	return nil
}

// formatProviderCounts renders cumulative per-provider hit counts as a compact
// line, e.g. "openalex 12 · arxiv 7 · pubmed 3" — sorted descending by count
// (ties alphabetical), truncated to maxWidth.
func formatProviderCounts(counts map[string]int, maxWidth int) string {
	if len(counts) == 0 {
		return ""
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	var b strings.Builder
	shown := 0
	for _, name := range names {
		entry := fmt.Sprintf("%s %d", name, counts[name])
		candidate := entry
		if shown > 0 {
			candidate = b.String() + " · " + entry
		}
		if maxWidth > 0 && shown > 0 && visibleWidth(candidate) > maxWidth {
			b.WriteString(fmt.Sprintf(" +%d", len(names)-shown))
			break
		}
		if shown > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(entry)
		shown++
	}
	return b.String()
}

func payloadInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func printCLIProgressEvent(w io.Writer, event agent.ProgressEvent) {
	msg, tag := formatProgressEvent(event)
	if msg == "" {
		return
	}
	glyph := statusGlyph("ok")
	switch tag {
	case "W":
		glyph = statusGlyph("warn")
	case "E":
		glyph = statusGlyph("fail")
	}
	fmt.Fprintf(w, "  %s %s\n", glyph, msg)
}

func formatProgressEvent(event agent.ProgressEvent) (string, string) {
	message := strings.TrimSpace(event.Message)
	stage := strings.TrimSpace(event.Stage)
	if stage == "" && event.Payload != nil {
		stage = strings.TrimSpace(internal.AsOptionalString(event.Payload["stage"]))
	}
	if message == "" && stage != "" {
		message = stage
	}
	if message == "" {
		return "", "I"
	}

	degraded := progressEventDegraded(event)
	tag := "I"
	if degraded {
		tag = "W"
	} else if event.Payload != nil {
		if internal.AsOptionalString(event.Payload["failed"]) == "true" || event.Payload["failed"] == true {
			tag = "E"
		}
	}
	if strings.EqualFold(strings.TrimSpace(event.Type), "completed") {
		tag = "I"
	}

	var b strings.Builder
	if degraded {
		b.WriteString("⚠ degraded: ")
	}
	if stage != "" {
		b.WriteString("[")
		b.WriteString(progressStageLabel(stage))
		b.WriteString("] ")
	}
	b.WriteString(message)
	if detail := formatProgressPayload(event.Payload, degraded); detail != "" {
		b.WriteString(" — ")
		b.WriteString(detail)
	}
	return b.String(), tag
}

// progressStageLabel maps newer machine stage IDs to human-readable labels.
// Established stage IDs are passed through unchanged because downstream
// consumers (phase inference, log filters) match on the raw names.
func progressStageLabel(stage string) string {
	switch stage {
	case "pre_retrieval_hypotheses":
		return "hypothesis planning"
	case "hypothesis_probe_budget_capped":
		return "hypothesis probe budget capped"
	case "critique_retrieval_started":
		return "critique retrieval"
	}
	return stage
}

func progressEventDegraded(event agent.ProgressEvent) bool {
	if event.Degraded {
		return true
	}
	if event.Payload == nil {
		return false
	}
	if v, ok := event.Payload["degraded"].(bool); ok && v {
		return true
	}
	return strings.TrimSpace(internal.AsOptionalString(event.Payload["fallback"])) != ""
}

func formatProgressPayload(payload map[string]any, degraded bool) string {
	if len(payload) == 0 {
		return ""
	}
	skip := map[string]struct{}{
		"component":         {},
		"operation":         {},
		"stage":             {},
		"researchPlane":     {},
		"degraded":          {},
		"failed":            {},
		"fallback":          {},
		"trace_id":          {},
		"session_id":        {},
		"executionMode":     {},
		"mode":              {},
		"dynamicProviders":  {},
		"bypassSearchCache": {},
	}
	if degraded {
		if fb := strings.TrimSpace(internal.AsOptionalString(payload["fallback"])); fb != "" {
			return "fallback=" + fb
		}
		return "fallback=heuristic"
	}

	keys := make([]string, 0, len(payload))
	for key, value := range payload {
		if _, omit := skip[key]; omit {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(value)) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	if len(keys) > 4 {
		keys = keys[:4]
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, payload[key]))
	}
	return strings.Join(parts, " ")
}

func formatStageLogsMarkdown(logs []tuiLogEntry) string {
	if len(logs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Stage log\n\n")
	for _, entry := range logs {
		tag := strings.TrimSpace(entry.tag)
		if tag == "" {
			tag = "I"
		}
		b.WriteString(fmt.Sprintf("- `[%s]` %s\n", tag, entry.msg))
	}
	b.WriteString("\n")
	return b.String()
}

func formatAutonomousSlogRecord(level string, stage, operation, message string, attrs []string, reason string, degraded bool) (string, string) {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = strings.TrimSpace(operation)
	}
	msg := strings.TrimSpace(message)
	if stage != "" {
		msg = fmt.Sprintf("[%s] %s", progressStageLabel(stage), message)
	}
	if degraded {
		msg = "⚠ degraded: " + msg
	}
	if reason != "" {
		msg += " — " + reason
	} else if len(attrs) > 0 {
		msg += " — " + strings.Join(attrs, " ")
	}
	tag := "I"
	switch level {
	case "ERROR":
		tag = "E"
	case "WARN":
		tag = "W"
	}
	if degraded {
		tag = "W"
	}
	return msg, tag
}

func slogRecordDegraded(attrs map[string]any) bool {
	if attrs == nil {
		return false
	}
	if v, ok := attrs["degraded"].(bool); ok && v {
		return true
	}
	stage := strings.ToLower(strings.TrimSpace(fmt.Sprint(attrs["stage"])))
	if strings.Contains(stage, "fallback") || strings.Contains(stage, "heuristic") {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(fmt.Sprint(attrs["message"])))
	return strings.Contains(msg, "heuristic fallback") || strings.Contains(msg, "using heuristic")
}

func shouldIncludeAutonomousLogAttr(key string) bool {
	switch key {
	case "component", "operation", "msg", "time", "level":
		return false
	default:
		return true
	}
}

func extractLogStage(msg string) string {
	msg = strings.TrimSpace(msg)
	msg = strings.TrimPrefix(msg, "⚠ degraded: ")
	if !strings.HasPrefix(msg, "[") {
		return ""
	}
	end := strings.Index(msg, "]")
	if end <= 1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(msg[1:end]))
}

func inferRunPhaseFromLogs(logs []tuiLogEntry, iterations int) string {
	if len(logs) == 0 {
		return "Initialising"
	}
	for i := len(logs) - 1; i >= 0 && i >= len(logs)-6; i-- {
		stage := extractLogStage(logs[i].msg)
		lower := strings.ToLower(logs[i].msg)
		switch {
		case stage == "loop_completed", strings.Contains(lower, "complete"):
			return "Complete"
		case stage == "synthesis_started", strings.Contains(stage, "synth"), strings.Contains(stage, "refine_draft"):
			return "Synthesizing"
		case stage == "evaluate_sufficiency", stage == "post_critique_sufficiency", strings.Contains(stage, "sufficiency"), strings.Contains(stage, "critique"):
			return "Verifying"
		case stage == "propose_hypotheses", strings.Contains(stage, "hypothes"), strings.Contains(stage, "tree_search"):
			return "Planning"
		case stage == "search_batch_started", stage == "search_result_admitted", stage == "query_completed", strings.Contains(stage, "search"):
			return "Searching"
		case stage == "query_prepared", stage == "loop_started":
			return "Preparing"
		case stage == "loop_iteration":
			if iterations > 0 {
				return fmt.Sprintf("Iteration %d", iterations)
			}
			return "Running"
		}
	}
	return inferRunPhase(logs, iterations)
}
