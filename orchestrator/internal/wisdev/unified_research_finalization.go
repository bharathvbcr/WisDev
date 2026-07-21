package wisdev

import (
	"fmt"
	"strings"
)

type ResearchFinalizationGate struct {
	Status           string   `json:"status"`
	Ready            bool     `json:"ready"`
	Provisional      bool     `json:"provisional"`
	StopReason       string   `json:"stopReason,omitempty"`
	VerifierVerdict  string   `json:"verifierVerdict,omitempty"`
	SynthesisGate    string   `json:"synthesisGate,omitempty"`
	OpenLedgerCount  int      `json:"openLedgerCount,omitempty"`
	FollowUpQueries  []string `json:"followUpQueries,omitempty"`
	RevisionReasons  []string `json:"revisionReasons,omitempty"`
	PromotedClaimIDs []string `json:"promotedClaimIds,omitempty"`
	RejectedClaimIDs []string `json:"rejectedClaimIds,omitempty"`
}

func verifierVerdict(decision *ResearchVerifierDecision) string {
	if decision == nil || strings.TrimSpace(decision.Verdict) == "" {
		return "unknown"
	}
	return strings.TrimSpace(decision.Verdict)
}

func researchAnswerStatus(state *ResearchSessionState, gate *ResearchFinalizationGate, ready bool, stopReason string) string {
	return ResearchAnswerStatusFromState(state, gate, ready, stopReason)
}

func ResearchAnswerStatusFromState(state *ResearchSessionState, gate *ResearchFinalizationGate, ready bool, stopReason string) string {
	if state != nil && state.DurableJob != nil && state.DurableJob.BudgetUsed.Exhausted {
		return "budget_exhausted"
	}
	if gate != nil {
		switch {
		case gate.Ready || ready:
			return "verified"
		case strings.EqualFold(gate.Status, "blocked"):
			return "blocked"
		case gate.Provisional:
			return "provisional"
		case strings.TrimSpace(gate.Status) != "":
			return strings.TrimSpace(gate.Status)
		}
	}
	switch strings.TrimSpace(stopReason) {
	case "verified_final":
		return "verified"
	case "budget_exhausted_with_open_gaps":
		return "budget_exhausted"
	case "":
		return "provisional"
	default:
		return "provisional"
	}
}

func mergeVerifierDecisionIntoGapState(gap *LoopGapState, state *ResearchSessionState) *LoopGapState {
	if gap == nil {
		gap = &LoopGapState{}
	}
	if state == nil {
		return gap
	}
	entries := make([]CoverageLedgerEntry, 0)
	addEntry := func(category string, title string, description string, queries []string, priority int) {
		description = strings.TrimSpace(description)
		queries = normalizeLoopQueries("", queries)
		if description == "" && len(queries) == 0 {
			return
		}
		entry := CoverageLedgerEntry{
			ID:                stableWisDevID("post-verifier-gap", state.Query, category, title, description, strings.Join(queries, "|")),
			Category:          category,
			Status:            coverageLedgerStatusOpen,
			Title:             strings.TrimSpace(firstNonEmpty(title, "Post-verifier follow-up required")),
			Description:       description,
			SupportingQueries: queries,
			Required:          true,
			Priority:          priority,
			Confidence:        0.42,
		}
		switch category {
		case "claim_evidence":
			entry.ObligationType = "unverified_claim"
			entry.OwnerWorker = string(ResearchWorkerIndependentVerifier)
		case "citation_integrity":
			entry.ObligationType = "missing_citation_identity"
			entry.OwnerWorker = string(ResearchWorkerCitationGraph)
		case "contradiction":
			entry.ObligationType = "missing_counter_evidence"
			entry.OwnerWorker = string(ResearchWorkerContradictionCritic)
		case "hypothesis_branch":
			entry.ObligationType = "missing_replication"
			entry.OwnerWorker = string(ResearchWorkerContradictionCritic)
		case "independent_verifier":
			entry.ObligationType = "unverified_claim"
			entry.OwnerWorker = string(ResearchWorkerIndependentVerifier)
		default:
			entry.ObligationType = inferCoverageObligationType(entry)
			entry.OwnerWorker = inferCoverageObligationOwner(entry)
		}
		entry.Severity = inferCoverageObligationSeverity(entry)
		entry.ClosureEvidence = dedupeTrimmedStrings([]string{
			"verifier_followup_required",
			"obligation:" + entry.ObligationType,
			"owner:" + entry.OwnerWorker,
		})
		entries = append(entries, entry)
	}
	if state.ClaimVerification != nil {
		for _, query := range state.ClaimVerification.RequiredFollowUpQueries {
			addEntry("claim_evidence", "Verifier required claim follow-up", query, []string{query}, 97)
		}
		for _, record := range state.ClaimVerification.Records {
			if record.Status == "supported" && record.CitationHealth == "healthy" && record.ContradictionStatus != "open" {
				continue
			}
			category := "claim_evidence"
			if record.CitationHealth != "healthy" {
				category = "citation_integrity"
			}
			if record.ContradictionStatus == "open" || record.Status == "contradicted" {
				category = "contradiction"
			}
			addEntry(category, "Verifier rejected claim", verifierRevisionReason(record), record.FollowUpQueries, 96)
		}
	}
	for _, branch := range state.BranchEvaluations {
		if len(branch.OpenGaps) == 0 && branch.StopReason != "branch_unexecuted" && branch.StopReason != "branch_needs_evidence" {
			continue
		}
		addEntry("hypothesis_branch", "Verifier blocked branch", strings.TrimSpace(firstNonEmpty(branch.StopReason, branch.Query)), []string{branch.Query}, 94)
	}
	if state.VerifierDecision != nil {
		for _, reason := range state.VerifierDecision.RevisionReasons {
			addEntry("independent_verifier", "Independent verifier requested revision", reason, []string{strings.TrimSpace(state.Query + " " + summarizeLoopGapTerms(reason))}, 98)
		}
	}
	if len(entries) == 0 {
		return gap
	}
	gap.Sufficient = false
	gap.Ledger = mergeCoverageLedgerEntries(gap.Ledger, entries)
	gap.NextQueries = dedupeTrimmedStrings(append(buildFollowUpQueriesFromLedger(state.Query, entries, 8), gap.NextQueries...))
	for _, entry := range entries {
		gap.MissingAspects = appendUniqueString(gap.MissingAspects, firstNonEmpty(entry.Description, entry.Title))
		if entry.Category == "contradiction" {
			gap.Contradictions = appendUniqueString(gap.Contradictions, firstNonEmpty(entry.Description, entry.Title))
		}
	}
	return gap
}

func buildPostVerifierFollowUpQueries(query string, gap *LoopGapState, state *ResearchSessionState, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, limit)
	add := func(value string) {
		for _, candidate := range normalizeLoopQueries("", []string{value}) {
			if semanticallyRedundantLoopQuery(candidate, out, semanticGapDuplicateThreshold) {
				continue
			}
			out = appendUniqueLoopQuery(out, candidate)
		}
	}
	for _, candidate := range buildFollowUpQueriesFromLedger(query, gapLedger(gap), limit) {
		add(candidate)
		if len(out) >= limit {
			return out[:limit]
		}
	}
	if state != nil && state.ClaimVerification != nil {
		for _, candidate := range state.ClaimVerification.RequiredFollowUpQueries {
			add(candidate)
			if len(out) >= limit {
				return out[:limit]
			}
		}
		for _, record := range state.ClaimVerification.Records {
			for _, candidate := range record.FollowUpQueries {
				add(candidate)
				if len(out) >= limit {
					return out[:limit]
				}
			}
		}
	}
	if state != nil {
		for _, branch := range state.BranchEvaluations {
			if len(branch.OpenGaps) > 0 || branch.StopReason == "branch_unexecuted" || branch.StopReason == "branch_needs_evidence" {
				add(branch.Query)
				if len(out) >= limit {
					return out[:limit]
				}
			}
		}
	}
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func applyFinalizationGateToLoopResult(loopResult *LoopResult, state *ResearchSessionState) {
	if loopResult == nil {
		return
	}
	gate := buildResearchFinalizationGate(state, loopResult)
	loopResult.FinalizationGate = gate
	if gate != nil {
		loopResult.StopReason = gate.StopReason
		if gate.Provisional {
			loopResult.FinalAnswer = gateProvisionalAnswer(loopResult.FinalAnswer, gate)
		}
	}
}

func buildResearchFinalizationGate(state *ResearchSessionState, loopResult *LoopResult) *ResearchFinalizationGate {
	gate := &ResearchFinalizationGate{
		Status:      "revise_required",
		Provisional: true,
		StopReason:  "runtime_not_verified",
	}
	if state == nil {
		return gate
	}
	decision := state.VerifierDecision
	finalizationGap := buildFinalizationGapState(state, loopResult)
	openLedgerCount := finalizationOpenLedgerCount(state, finalizationGap)
	ready := state.Blackboard != nil && state.Blackboard.ReadyForSynthesis && openLedgerCount == 0 && decision != nil && decision.Verdict == "promote"
	gate.Ready = ready
	gate.OpenLedgerCount = openLedgerCount
	gate.SynthesisGate = finalizationSynthesisGate(state.Blackboard, openLedgerCount, ready)
	gate.StopReason = strings.TrimSpace(firstNonEmpty(state.StopReason, determineResearchStopReason(loopResult, state.ClaimVerification, decision)))
	if openLedgerCount > 0 && finalizationStopReasonAllowsOpenLedgerOverride(gate.StopReason) {
		gate.StopReason = "coverage_open"
	}
	if decision != nil {
		gate.VerifierVerdict = strings.TrimSpace(decision.Verdict)
		gate.RevisionReasons = append([]string(nil), decision.RevisionReasons...)
		gate.PromotedClaimIDs = append([]string(nil), decision.PromotedClaimIDs...)
		gate.RejectedClaimIDs = append([]string(nil), decision.RejectedClaimIDs...)
	}
	gate.FollowUpQueries = buildPostVerifierFollowUpQueries(state.Query, finalizationGap, state, 6)
	if ready {
		gate.Status = "promote"
		gate.Provisional = false
		if gate.StopReason == "" || gate.StopReason == "verifier_promoted" {
			gate.StopReason = "verified_final"
		}
		return gate
	}
	if decision != nil && decision.Verdict == "abstain" {
		gate.Status = "abstain"
	}
	if gate.Status == "revise_required" {
		gate.Status = classifyBlockedFinalizationStatus(gate.StopReason, finalizationGap)
	}
	return gate
}

func classifyBlockedFinalizationStatus(stopReason string, gap *LoopGapState) string {
	reason := strings.ToLower(strings.TrimSpace(stopReason))
	switch {
	case strings.Contains(reason, "budget") || strings.Contains(reason, "iteration"):
		return "budget_exhausted_with_gaps"
	case strings.Contains(reason, "no_grounded_sources"):
		return "blocked_unavailable_evidence"
	case strings.Contains(reason, "missing_full_text") || strings.Contains(reason, "missing_citation_identity"):
		return "blocked_unavailable_evidence"
	}
	if gap != nil {
		for _, entry := range gap.Ledger {
			if !strings.EqualFold(strings.TrimSpace(entry.Status), coverageLedgerStatusOpen) {
				continue
			}
			normalized := normalizeCoverageLedgerObligation(entry)
			switch strings.TrimSpace(normalized.ObligationType) {
			case "missing_full_text", "missing_citation_identity":
				return "blocked_unavailable_evidence"
			case "missing_counter_evidence", "missing_replication", "unverified_claim":
				return "revise_required"
			}
		}
	}
	return "revise_required"
}

func buildFinalizationGapState(state *ResearchSessionState, loopResult *LoopResult) *LoopGapState {
	var gap *LoopGapState
	if loopResult != nil && loopResult.GapAnalysis != nil {
		clone := *loopResult.GapAnalysis
		clone.Ledger = append([]CoverageLedgerEntry(nil), loopResult.GapAnalysis.Ledger...)
		clone.NextQueries = append([]string(nil), loopResult.GapAnalysis.NextQueries...)
		clone.MissingAspects = append([]string(nil), loopResult.GapAnalysis.MissingAspects...)
		clone.MissingSourceTypes = append([]string(nil), loopResult.GapAnalysis.MissingSourceTypes...)
		clone.Contradictions = append([]string(nil), loopResult.GapAnalysis.Contradictions...)
		gap = &clone
	} else {
		gap = &LoopGapState{}
	}
	gap.Ledger = aggregateFinalizationCoverageLedger(state, loopResult)
	return gap
}

func aggregateFinalizationCoverageLedger(state *ResearchSessionState, loopResult *LoopResult) []CoverageLedgerEntry {
	var ledger []CoverageLedgerEntry
	if state != nil {
		if state.Blackboard != nil {
			ledger = mergeCoverageLedgerEntries(ledger, state.Blackboard.CoverageLedger)
		}
		ledger = mergeCoverageLedgerEntries(ledger, state.CoverageLedger)
		if state.SourceAcquisition != nil {
			ledger = mergeCoverageLedgerEntries(ledger, state.SourceAcquisition.CoverageLedger)
		}
	}
	if loopResult != nil && loopResult.GapAnalysis != nil {
		ledger = mergeCoverageLedgerEntries(ledger, loopResult.GapAnalysis.Ledger)
	}
	return ledger
}

func finalizationOpenLedgerCount(state *ResearchSessionState, gap *LoopGapState) int {
	count := 0
	if gap != nil {
		count = countOpenCoverageLedgerEntries(gap.Ledger)
	}
	if state != nil {
		count = maxInt(count, researchBlackboardOpenLedgerCount(state.Blackboard))
	}
	return count
}

func finalizationSynthesisGate(board *ResearchBlackboard, openLedgerCount int, ready bool) string {
	if ready {
		return "ready: independent verifier promoted grounded claims and runtime ledger is closed"
	}
	base := researchBlackboardSynthesisGate(board)
	blackboardOpen := researchBlackboardOpenLedgerCount(board)
	runtimeOpen := maxInt(openLedgerCount-blackboardOpen, 0)
	if openLedgerCount <= 0 {
		return base
	}
	if runtimeOpen > 0 {
		runtimeGate := fmt.Sprintf("%d runtime coverage ledger item(s) remain open", runtimeOpen)
		if strings.TrimSpace(base) == "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(base)), "ready:") {
			return "blocked: " + runtimeGate
		}
		return strings.TrimSpace(base + "; " + runtimeGate)
	}
	if strings.TrimSpace(base) == "" {
		return fmt.Sprintf("blocked: %d coverage ledger item(s) remain open", openLedgerCount)
	}
	return base
}

func finalizationStopReasonAllowsOpenLedgerOverride(reason string) bool {
	return !isBlockingFinalizationStopReason(strings.ToLower(strings.TrimSpace(reason)))
}

func isBlockingFinalizationStopReason(reason string) bool {
	switch {
	case reason == "":
		return false
	case reason == "verifier_promoted":
		return false
	case strings.Contains(reason, "verifier_") && reason != "verifier_promoted":
		return true
	case strings.Contains(reason, "claim_verification_") && reason != "claim_verification_satisfied":
		return true
	case strings.HasPrefix(reason, "missing_"):
		return true
	case strings.HasPrefix(reason, "unverified_"):
		return true
	case strings.Contains(reason, "unverified"):
		return true
	case strings.Contains(reason, "claim_coverage_open"):
		return true
	case strings.Contains(reason, "budget_exhaust"):
		return true
	case strings.Contains(reason, "no_grounded_sources"):
		return true
	default:
		return false
	}
}

func gateProvisionalAnswer(answer string, gate *ResearchFinalizationGate) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" || gate == nil || !gate.Provisional {
		return trimmed
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "provisional answer:") || strings.HasPrefix(strings.ToLower(trimmed), "provisional research synthesis:") {
		return trimmed
	}
	reasons := gate.RevisionReasons
	if len(reasons) > 2 {
		reasons = reasons[:2]
	}
	reasonText := strings.TrimSpace(gate.StopReason)
	if len(reasons) > 0 {
		reasonText = strings.TrimSpace(reasonText + ": " + strings.Join(reasons, "; "))
	}
	if reasonText == "" {
		reasonText = "independent verifier did not clear final synthesis"
	}
	return "Provisional answer: " + reasonText + "\n\n" + trimmed
}

func gapFromLoopResult(loopResult *LoopResult) *LoopGapState {
	if loopResult == nil {
		return nil
	}
	return loopResult.GapAnalysis
}
