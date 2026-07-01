package wisdev

// manuscript_logging.go centralizes the structured logging for the DocGen
// manuscript pipeline. The pipeline is a long multi-stage LLM workflow with many
// silent-degradation paths (sidecar unreachable -> scaffold, empty section, low
// grounding ratio, unresolved review findings, non-converging revise loop). These
// helpers make EVERY stage and decision point observable so a run can be traced
// end to end and any issue surfaced immediately.

import (
	"log/slog"
	"strings"
	"time"
)

const (
	manuscriptLogComponent = "wisdev.manuscript"
	manuscriptLogOp        = "pipeline.run"
)

// stageLogger is a per-run closure that timestamps and labels each pipeline stage,
// reporting both the time spent in that stage (stage_ms) and the cumulative run
// time (elapsed_ms) so it is obvious where a run spends — or loses — time.
type stageLogger struct {
	jobID   string
	started time.Time
	prev    time.Time
}

func newStageLogger(jobID string) *stageLogger {
	now := time.Now()
	return &stageLogger{jobID: jobID, started: now, prev: now}
}

// stage logs the completion of a named pipeline stage with the supplied metrics.
func (s *stageLogger) stage(name string, attrs ...any) {
	now := time.Now()
	base := []any{
		"component", manuscriptLogComponent,
		"operation", manuscriptLogOp,
		"job_id", s.jobID,
		"stage", name,
		"stage_ms", now.Sub(s.prev).Milliseconds(),
		"elapsed_ms", now.Sub(s.started).Milliseconds(),
	}
	slog.Info("manuscript stage", append(base, attrs...)...)
	s.prev = now
}

// warn logs a degradation/anomaly at the current stage (fallbacks, empties, low
// grounding) so issues are visible without failing the run.
func (s *stageLogger) warn(name, reason string, attrs ...any) {
	base := []any{
		"component", manuscriptLogComponent,
		"operation", manuscriptLogOp,
		"job_id", s.jobID,
		"stage", name,
		"reason", reason,
		"elapsed_ms", time.Since(s.started).Milliseconds(),
	}
	slog.Warn("manuscript stage anomaly", append(base, attrs...)...)
}

// sectionsMetrics summarizes a section set for a stage log: count, total prose
// characters, and the degradation signals (empty sections, sections flagged for
// revision, sections with unresolved issues).
func sectionsMetrics(sections []SectionDraftArtifact) []any {
	var chars, empty, needsRevision, withIssues int
	for i := range sections {
		c := strings.TrimSpace(sections[i].Content)
		chars += len(c)
		if c == "" {
			empty++
		}
		if sections[i].ReviewStatus == "needs_revision" {
			needsRevision++
		}
		if len(sections[i].UnresolvedIssues) > 0 {
			withIssues++
		}
	}
	return []any{
		"sections", len(sections),
		"content_chars", chars,
		"empty_sections", empty,
		"needs_revision", needsRevision,
		"sections_with_issues", withIssues,
	}
}

// groundingMetrics aggregates the blind-verifier outcome across all sections so a
// verify stage reports the manuscript-wide grounding ratio — the headline quality
// signal (verified vs flagged vs rejected paragraphs).
func groundingMetrics(sections []SectionDraftArtifact) []any {
	var verified, flagged, rejected, blocking int
	for i := range sections {
		v := sections[i].BlindVerifier
		verified += v.VerifiedParagraphs
		flagged += v.FlaggedParagraphs
		rejected += v.RejectedParagraphs
		blocking += len(v.BlockingIssues)
	}
	total := verified + flagged + rejected
	ratio := 0.0
	if total > 0 {
		ratio = float64(verified) / float64(total)
	}
	return []any{
		"verified_paras", verified,
		"flagged_paras", flagged,
		"rejected_paras", rejected,
		"blocking_issues", blocking,
		"grounding_ratio", ratio,
	}
}

// sectionIDs returns the ordered section ids for a plan/stage log.
func sectionIDs(sections []SectionDraftArtifact) []string {
	ids := make([]string, 0, len(sections))
	for i := range sections {
		ids = append(ids, sections[i].SectionID)
	}
	return ids
}

// truncForLog clamps free text (a query, a finding, a prose excerpt) to a bounded
// length so log lines stay readable and bounded regardless of input size.
func truncForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// pipelineMode reports whether the run will use the sidecar (real LLM prose) or
// fall back to grounded scaffolds (no sidecar configured) — the single biggest
// determinant of output quality, logged up front.
func (p *ManuscriptPipeline) pipelineMode() string {
	if strings.TrimSpace(p.pythonBaseURL) == "" {
		return "scaffold-only"
	}
	return "sidecar"
}
