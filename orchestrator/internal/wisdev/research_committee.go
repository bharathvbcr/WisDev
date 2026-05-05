package wisdev

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"log/slog"
	"sort"
	"strings"

	llmv1 "github.com/wisdev/wisdev-agent-os/orchestrator/proto/llm"
)

type CommitteeMember struct {
	Role        string
	Instruction string
}

type CommitteeVerdict struct {
	Role    string
	Verdict string // "approve", "reject", "revise"
	Reason  string
}

type ResearchCommittee struct {
	Members    []CommitteeMember
	Supervisor *CommitteeSupervisor
	LLMClient  *llm.Client
}

type CommitteeSupervisor struct {
	Instruction string
}

func NewResearchCommittee(client *llm.Client) *ResearchCommittee {
	return &ResearchCommittee{
		Members: []CommitteeMember{
			{Role: "FactChecker", Instruction: "Verify all claims against provided evidence. Reject if unsupported."},
			{Role: "Synthesizer", Instruction: "Evaluate coherence and completeness. Reject if fragmented or missing key aspects."},
			{Role: "ContradictionAnalyst", Instruction: "Find unaddressed contradictions in the evidence. Reject if ignored."},
		},
		Supervisor: &CommitteeSupervisor{
			Instruction: "You are the supervisor. Review the members' verdicts. If split, resolve the tie. Return a final 'approve', 'reject', or 'revise' verdict.",
		},
		LLMClient: client,
	}
}

func (rc *ResearchCommittee) Deliberate(ctx context.Context, hypothesis *Hypothesis, evidence []EvidenceFinding) (*CommitteeVerdict, error) {
	if rc.LLMClient == nil || hypothesis == nil {
		return &CommitteeVerdict{Verdict: "approve", Reason: "No LLM client"}, nil
	}
	if remaining := rc.LLMClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("ResearchCommittee skipped during provider cooldown",
			"component", "wisdev.autonomous",
			"operation", "committee_deliberate",
			"retry_after_ms", remaining.Milliseconds(),
		)
		return &CommitteeVerdict{Verdict: "approve", Reason: "Provider cooldown active; deterministic fallback used."}, nil
	}

	var verdicts []CommitteeVerdict
	providerLimited := false
	parseFailures := 0

	evidenceText := formatEvidenceForDebate(evidence)

	for _, member := range rc.Members {
		if remaining := rc.LLMClient.ProviderCooldownRemaining(); remaining > 0 {
			providerLimited = true
			slog.Warn("ResearchCommittee stopped member deliberation during provider cooldown",
				"component", "wisdev.autonomous",
				"operation", "committee_deliberate",
				"stage", "member_deliberation",
				"retry_after_ms", remaining.Milliseconds(),
				"verdictCount", len(verdicts),
			)
			break
		}
		prompt := fmt.Sprintf(`Role: %s
Instruction: %s

Hypothesis: %s
Evidence:
%s

Provide a verdict of approve, reject, or revise, plus a concise reason.
Use the supplied structured output schema exactly.`, member.Role, member.Instruction, hypothesis.Claim, evidenceText)

		memberCtx, cancel := wisdevRecoverableStructuredContext(ctx)
		resp, err := rc.LLMClient.StructuredOutput(memberCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
			Prompt:     prompt,
			Model:      llm.ResolveStandardModel(),
			JsonSchema: `{"type":"object","properties":{"verdict":{"type":"string"},"reason":{"type":"string"}},"required":["verdict","reason"]}`,
		}))
		cancel()

		if err != nil {
			if llm.IsProviderRateLimitError(err) {
				providerLimited = true
				slog.Warn("ResearchCommittee member rate limited; skipping remaining optional deliberation",
					"component", "wisdev.autonomous",
					"operation", "committee_deliberate",
					"stage", "member_deliberation",
					"role", member.Role,
					"error", err.Error(),
					"verdictCount", len(verdicts),
				)
				break
			}
			continue
		}

		var v CommitteeVerdict
		if err := json.Unmarshal([]byte(resp.JsonResult), &v); err == nil {
			v.Role = member.Role
			verdicts = append(verdicts, v)
		} else {
			parseFailures++
			slog.Warn("ResearchCommittee rejected malformed structured member output",
				"component", "wisdev.autonomous",
				"operation", "committee_deliberate",
				"stage", "member_deliberation",
				"role", member.Role,
				"error", err.Error(),
			)
		}
	}

	if len(verdicts) == 0 {
		if providerLimited {
			return &CommitteeVerdict{Verdict: "approve", Reason: "Provider cooldown active; deterministic fallback used."}, nil
		}
		if parseFailures > 0 {
			return &CommitteeVerdict{Verdict: "revise", Reason: "Committee structured output was invalid; revision required."}, nil
		}
		return &CommitteeVerdict{Verdict: "approve", Reason: "Committee failed to deliberate"}, nil
	}
	majority := majorityCommitteeVerdict(verdicts)
	if providerLimited {
		return majority, nil
	}
	if remaining := rc.LLMClient.ProviderCooldownRemaining(); remaining > 0 {
		slog.Warn("ResearchCommittee skipped supervisor during provider cooldown",
			"component", "wisdev.autonomous",
			"operation", "committee_deliberate",
			"stage", "supervisor",
			"retry_after_ms", remaining.Milliseconds(),
			"verdictCount", len(verdicts),
		)
		return majority, nil
	}

	// Supervisor resolution
	verdictsText := ""
	for _, v := range verdicts {
		verdictsText += fmt.Sprintf("- %s: %s (%s)\n", v.Role, v.Verdict, v.Reason)
	}

	prompt := fmt.Sprintf(`Role: Supervisor
Instruction: %s

Hypothesis: %s
Member Verdicts:
%s

Provide the final verdict as approve, reject, or revise, plus final resolution reasoning.
Use the supplied structured output schema exactly.`, rc.Supervisor.Instruction, hypothesis.Claim, verdictsText)

	supervisorCtx, cancel := wisdevRecoverableStructuredContext(ctx)
	defer cancel()
	resp, err := rc.LLMClient.StructuredOutput(supervisorCtx, applyWisdevRecoverableStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveStandardModel(),
		JsonSchema: `{"type":"object","properties":{"verdict":{"type":"string"},"reason":{"type":"string"}},"required":["verdict","reason"]}`,
	}))

	if err != nil {
		if llm.IsProviderRateLimitError(err) {
			slog.Warn("ResearchCommittee supervisor rate limited; using majority verdict",
				"component", "wisdev.autonomous",
				"operation", "committee_deliberate",
				"stage", "supervisor",
				"error", err.Error(),
			)
			return majority, nil
		}
		slog.Warn("Supervisor failed", "err", err)
		return majority, nil
	}

	var finalVerdict CommitteeVerdict
	if err := json.Unmarshal([]byte(resp.JsonResult), &finalVerdict); err != nil {
		return majority, nil
	}
	finalVerdict.Role = "Supervisor"
	finalVerdict.Verdict = normalizeCommitteeVerdict(finalVerdict.Verdict)
	if finalVerdict.Verdict == "" {
		return majority, nil
	}

	return &finalVerdict, nil
}

func majorityCommitteeVerdict(verdicts []CommitteeVerdict) *CommitteeVerdict {
	if len(verdicts) == 0 {
		return &CommitteeVerdict{Verdict: "approve", Reason: "No member verdicts"}
	}
	counts := map[string]int{}
	reasons := map[string][]string{}
	for _, verdict := range verdicts {
		normalized := normalizeCommitteeVerdict(verdict.Verdict)
		if normalized == "" {
			normalized = "revise"
		}
		counts[normalized]++
		reasons[normalized] = append(reasons[normalized], strings.TrimSpace(verdict.Role+": "+verdict.Reason))
	}
	order := []string{"reject", "revise", "approve"}
	sort.SliceStable(order, func(i, j int) bool {
		if counts[order[i]] == counts[order[j]] {
			return i < j
		}
		return counts[order[i]] > counts[order[j]]
	})
	winner := order[0]
	return &CommitteeVerdict{
		Role:    "Majority",
		Verdict: winner,
		Reason:  strings.Join(reasons[winner], "; "),
	}
}

func normalizeCommitteeVerdict(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approve", "approved":
		return "approve"
	case "reject", "rejected":
		return "reject"
	case "revise", "revision", "needs_revision":
		return "revise"
	default:
		return ""
	}
}
