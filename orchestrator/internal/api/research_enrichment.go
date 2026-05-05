package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/search"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/wisdev"
)

var proposeAutonomousHypotheses = func(ctx context.Context, agentGateway *wisdev.AgentGateway, query string) ([]wisdev.Hypothesis, error) {
	if agentGateway == nil || agentGateway.Brain == nil {
		return nil, nil
	}
	return agentGateway.Brain.ProposeHypothesesInteractive(ctx, strings.TrimSpace(query), "autonomous_research", "")
}

func buildAutonomousHypothesisPayloadsFromLoop(query string, result *wisdev.LoopResult) []map[string]any {
	if result == nil {
		return nil
	}

	hypotheses := make([]wisdev.Hypothesis, 0)
	if result.ReasoningGraph != nil {
		for _, node := range result.ReasoningGraph.Nodes {
			if node.Type != wisdev.ReasoningNodeHypothesis {
				continue
			}
			claim := strings.TrimSpace(firstNonEmpty(node.Label, node.Text, node.RefinedQuery))
			if claim == "" {
				continue
			}
			evidence := buildAutonomousHypothesisEvidence(node, result.Evidence)
			hypotheses = append(hypotheses, wisdev.Hypothesis{
				ID:              strings.TrimSpace(firstNonEmpty(node.ID, wisdev.NewTraceID())),
				Query:           strings.TrimSpace(query),
				Text:            claim,
				Claim:           claim,
				Category:        "autonomous",
				Status:          "validated",
				ConfidenceScore: wisdev.ClampFloat(node.Confidence, 0, 1),
				Evidence:        evidence,
				EvidenceCount:   len(evidence),
			})
		}
	}

	return serializeAutonomousHypothesisPayloads(hypotheses)
}

func buildAutonomousHypothesisPayloads(
	ctx context.Context,
	agentGateway *wisdev.AgentGateway,
	query string,
	plannedQueries []string,
	result *wisdev.LoopResult,
	policy wisdev.DeepAgentsExecutionPolicy,
) []map[string]any {
	proposeAllowed, proposeReason := autonomousActionAllowed(
		agentGateway,
		policy,
		wisdev.ActionResearchProposeHypotheses,
	)
	generateAllowed, generateReason := autonomousActionAllowed(
		agentGateway,
		policy,
		wisdev.ActionResearchGenerateHypotheses,
	)
	if !proposeAllowed && !generateAllowed {
		slog.Info("skipping autonomous hypothesis enrichment due to deep-agents policy",
			"proposeReason", proposeReason,
			"generateReason", generateReason,
			"mode", policy.Mode,
			"queryCount", len(plannedQueries),
		)
		return nil
	}

	if proposeAllowed {
		merged := mergeAutonomousHypothesisPayloads(
			buildAutonomousHypothesisPayloadsFromLoop(query, result),
			maybeBuildAutonomousHypothesisPayloads(ctx, agentGateway, query),
		)
		if len(merged) > 0 {
			return merged
		}
	}

	if generateAllowed {
		return serializeAutonomousHypothesisPayloads(buildAutonomousSeedHypotheses(plannedQueries))
	}

	return nil
}

func maybeBuildAutonomousHypothesisPayloads(ctx context.Context, agentGateway *wisdev.AgentGateway, query string) []map[string]any {
	hypotheses, err := proposeAutonomousHypotheses(ctx, agentGateway, query)
	if err != nil {
		slog.Warn("failed to enrich autonomous research with proposed hypotheses", "query", query, "error", err)
		return nil
	}
	if len(hypotheses) == 0 {
		return nil
	}

	return serializeAutonomousHypothesisPayloads(hypotheses)
}

func maybeBuildAutonomousHypothesisPayloadsForQueries(ctx context.Context, agentGateway *wisdev.AgentGateway, queries []string) []map[string]any {
	if len(queries) == 0 {
		return nil
	}
	payloadGroups := make([][]map[string]any, 0, len(queries))
	for _, query := range normalizeResearchPlanQueries(queries) {
		payloadGroups = append(payloadGroups, maybeBuildAutonomousHypothesisPayloads(ctx, agentGateway, query))
	}
	return mergeAutonomousHypothesisPayloads(payloadGroups...)
}

func serializeAutonomousHypothesisPayloads(hypotheses []wisdev.Hypothesis) []map[string]any {
	if len(hypotheses) == 0 {
		return nil
	}

	payloads := make([]map[string]any, 0, len(hypotheses))
	for idx, hypothesis := range hypotheses {
		claim := strings.TrimSpace(firstNonEmpty(hypothesis.Claim, hypothesis.Text, hypothesis.Query))
		if claim == "" {
			continue
		}
		id := strings.TrimSpace(hypothesis.ID)
		if id == "" {
			id = wisdev.NewTraceID()
		}
		payload := map[string]any{
			"id":          id,
			"text":        claim,
			"claim":       claim,
			"confidence":  wisdev.ClampFloat(hypothesis.ConfidenceScore, 0, 1),
			"status":      strings.TrimSpace(firstNonEmpty(hypothesis.Status, "validated")),
			"evidence":    serializeAutonomousHypothesisEvidence(hypothesis.Evidence),
			"category":    strings.TrimSpace(hypothesis.Category),
			"sourceIndex": idx,
		}
		if trimmed := strings.TrimSpace(hypothesis.FalsifiabilityCondition); trimmed != "" {
			payload["falsifiabilityCondition"] = trimmed
		}
		payloads = append(payloads, payload)
	}

	return payloads
}

func buildAutonomousSeedHypotheses(queries []string) []wisdev.Hypothesis {
	normalizedQueries := normalizeResearchPlanQueries(queries)
	if len(normalizedQueries) == 0 {
		return nil
	}

	hypotheses := make([]wisdev.Hypothesis, 0, len(normalizedQueries))
	for idx, query := range normalizedQueries {
		hypotheses = append(hypotheses, wisdev.Hypothesis{
			ID:              fmt.Sprintf("planned_hypothesis_%d", idx+1),
			Query:           query,
			Text:            query,
			Claim:           query,
			Category:        "planned_query",
			Status:          "candidate",
			ConfidenceScore: 0.55,
		})
	}
	return hypotheses
}

func mergeAutonomousHypothesisPayloads(payloadGroups ...[]map[string]any) []map[string]any {
	merged := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	for _, payloads := range payloadGroups {
		for _, payload := range payloads {
			if len(payload) == 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(firstNonEmpty(
				wisdev.AsOptionalString(payload["claim"]),
				wisdev.AsOptionalString(payload["text"]),
				wisdev.AsOptionalString(payload["id"]),
			)))
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, payload)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func buildAutonomousHypothesisEvidence(node wisdev.ReasoningNode, findings []wisdev.EvidenceFinding) []*wisdev.EvidenceFinding {
	if len(findings) == 0 {
		return nil
	}

	allowedEvidenceIDs := make(map[string]struct{})
	for _, evidenceID := range autonomousNodeEvidenceIDs(node) {
		if trimmed := strings.TrimSpace(evidenceID); trimmed != "" {
			allowedEvidenceIDs[trimmed] = struct{}{}
		}
	}
	if len(allowedEvidenceIDs) > 0 {
		evidence := make([]*wisdev.EvidenceFinding, 0, len(allowedEvidenceIDs))
		for idx := range findings {
			finding := findings[idx]
			if _, ok := allowedEvidenceIDs[strings.TrimSpace(finding.ID)]; !ok {
				continue
			}
			copyFinding := finding
			evidence = append(evidence, &copyFinding)
			if len(evidence) >= 3 {
				break
			}
		}
		if len(evidence) > 0 {
			return evidence
		}
	}

	allowed := make(map[string]struct{}, len(node.SourceIDs))
	for _, sourceID := range node.SourceIDs {
		if trimmed := strings.TrimSpace(sourceID); trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	evidence := make([]*wisdev.EvidenceFinding, 0, len(findings))
	for idx := range findings {
		finding := findings[idx]
		if len(allowed) > 0 {
			if _, ok := allowed[strings.TrimSpace(finding.SourceID)]; !ok {
				continue
			}
		}
		copyFinding := finding
		evidence = append(evidence, &copyFinding)
		if len(evidence) >= 3 {
			break
		}
	}

	if len(evidence) > 0 || len(allowed) > 0 {
		return evidence
	}

	for idx := range findings {
		copyFinding := findings[idx]
		evidence = append(evidence, &copyFinding)
		if len(evidence) >= 3 {
			break
		}
	}
	return evidence
}

func autonomousNodeEvidenceIDs(node wisdev.ReasoningNode) []string {
	if len(node.Metadata) == 0 {
		return nil
	}
	raw, ok := node.Metadata["evidenceIds"]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if trimmed := strings.TrimSpace(fmt.Sprintf("%v", item)); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}

func serializeAutonomousHypothesisEvidence(findings []*wisdev.EvidenceFinding) []map[string]any {
	if len(findings) == 0 {
		return nil
	}

	payloads := make([]map[string]any, 0, len(findings))
	for _, finding := range findings {
		if finding == nil {
			continue
		}
		payloads = append(payloads, map[string]any{
			"id":         finding.ID,
			"claim":      finding.Claim,
			"paperTitle": finding.PaperTitle,
			"snippet":    finding.Snippet,
			"source_id":  finding.SourceID,
			"confidence": finding.Confidence,
		})
	}
	return payloads
}

func buildAutonomousCoveragePayload(planCoverage map[string][]string, queries []string, queryCoverage map[string][]map[string]any) map[string]any {
	coverage := make(map[string]any)
	normalizedQueries := normalizeResearchPlanQueries(queries)
	normalizedCoverage := normalizeAutonomousCoveragePayloadByQuery(queryCoverage)
	if len(planCoverage) > 0 {
		for key, mappedQueries := range planCoverage {
			normalizedKey := strings.TrimSpace(key)
			normalizedMappedQueries := normalizeResearchPlanQueries(mappedQueries)
			if normalizedKey == "" || len(normalizedMappedQueries) == 0 {
				continue
			}
			mergedCoverage := mergeAutonomousCoverageQueries(normalizedCoverage, normalizedMappedQueries)
			if len(mergedCoverage) == 0 {
				if directCoverage, ok := normalizedCoverage[normalizedKey]; ok {
					mergedCoverage = cloneAutonomousPaperPayloads(directCoverage)
				}
			}
			coverage[normalizedKey] = mergedCoverage
		}
		return coverage
	}
	if len(normalizedQueries) <= 1 {
		return coverage
	}
	for _, query := range normalizedQueries[1:] {
		coverage[query] = cloneAutonomousPaperPayloads(normalizedCoverage[query])
	}
	return coverage
}

func normalizeAutonomousCoveragePayloadByQuery(queryCoverage map[string][]map[string]any) map[string][]map[string]any {
	if len(queryCoverage) == 0 {
		return map[string][]map[string]any{}
	}
	normalized := make(map[string][]map[string]any, len(queryCoverage))
	for query, papers := range queryCoverage {
		trimmedQuery := strings.TrimSpace(query)
		if trimmedQuery == "" {
			continue
		}
		normalized[trimmedQuery] = cloneAutonomousPaperPayloads(papers)
	}
	return normalized
}

func mergeAutonomousCoverageQueries(queryCoverage map[string][]map[string]any, queries []string) []map[string]any {
	if len(queries) == 0 {
		return []map[string]any{}
	}
	merged := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	for _, query := range queries {
		for _, paper := range queryCoverage[strings.TrimSpace(query)] {
			key := autonomousCoveragePaperKey(paper)
			if key != "" {
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
			copyPaper := make(map[string]any, len(paper))
			for field, value := range paper {
				copyPaper[field] = value
			}
			merged = append(merged, copyPaper)
		}
	}
	if len(merged) == 0 {
		return []map[string]any{}
	}
	return merged
}

func autonomousCoveragePaperKey(paper map[string]any) string {
	for _, candidate := range []any{paper["id"], paper["paperId"], paper["doi"], paper["link"], paper["title"]} {
		if trimmed := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", candidate))); trimmed != "" && trimmed != "<nil>" {
			return trimmed
		}
	}
	return ""
}

func serializeAutonomousCoverageSourcesByQuery(queryCoverage map[string][]wisdev.Source) map[string][]map[string]any {
	if len(queryCoverage) == 0 {
		return map[string][]map[string]any{}
	}
	payloads := make(map[string][]map[string]any, len(queryCoverage))
	for query, papers := range queryCoverage {
		payloads[strings.TrimSpace(query)] = buildCommitteePapers(papers)
	}
	return payloads
}

func serializeAutonomousCoverageSearchPapersByQuery(queryCoverage map[string][]search.Paper) map[string][]map[string]any {
	if len(queryCoverage) == 0 {
		return map[string][]map[string]any{}
	}
	payloads := make(map[string][]map[string]any, len(queryCoverage))
	for query, papers := range queryCoverage {
		payloads[strings.TrimSpace(query)] = buildCommitteePapers(searchPapersToWisdevSources(papers))
	}
	return payloads
}

func cloneAutonomousPaperPayloads(papers []map[string]any) []map[string]any {
	if len(papers) == 0 {
		return []map[string]any{}
	}
	cloned := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		if paper == nil {
			continue
		}
		copyPaper := make(map[string]any, len(paper))
		for key, value := range paper {
			copyPaper[key] = value
		}
		cloned = append(cloned, copyPaper)
	}
	return cloned
}

func buildAutonomousGapPayloads(dossier map[string]any) []map[string]any {
	if len(dossier) == 0 {
		return nil
	}

	rawGaps, ok := dossier["gaps"].([]string)
	if !ok || len(rawGaps) == 0 {
		rawGapItems, ok := dossier["gaps"].([]any)
		if !ok || len(rawGapItems) == 0 {
			return nil
		}
		rawGaps = make([]string, 0, len(rawGapItems))
		for _, item := range rawGapItems {
			if trimmed := strings.TrimSpace(fmt.Sprintf("%v", item)); trimmed != "" {
				rawGaps = append(rawGaps, trimmed)
			}
		}
	}

	payloads := make([]map[string]any, 0, len(rawGaps))
	for index, gap := range rawGaps {
		trimmed := strings.TrimSpace(gap)
		if trimmed == "" {
			continue
		}
		title := trimmed
		if len(title) > 72 {
			title = strings.TrimSpace(title[:72]) + "..."
		}
		payloads = append(payloads, map[string]any{
			"id":                  fmt.Sprintf("gap_%d", index+1),
			"type":                "theoretical",
			"title":               title,
			"description":         trimmed,
			"supportingPapers":    []string{},
			"confidence":          0.55,
			"suggestedApproaches": []string{},
			"keywords":            []string{},
			"potentialImpact":     "medium",
		})
	}

	return payloads
}

func buildAutonomousGapPayloadsFromLoopAnalysis(gapState *wisdev.LoopGapState) []map[string]any {
	if gapState == nil {
		return nil
	}
	if len(gapState.Ledger) > 0 {
		payloads := make([]map[string]any, 0, len(gapState.Ledger))
		for _, entry := range gapState.Ledger {
			title := strings.TrimSpace(firstNonEmpty(entry.Title, entry.Description))
			if title == "" {
				continue
			}
			if len(title) > 72 {
				title = strings.TrimSpace(title[:72]) + "..."
			}
			payloads = append(payloads, map[string]any{
				"id":                     strings.TrimSpace(firstNonEmpty(entry.ID, fmt.Sprintf("loop_gap_%d", len(payloads)+1))),
				"type":                   strings.TrimSpace(firstNonEmpty(entry.Category, "theoretical")),
				"status":                 strings.TrimSpace(firstNonEmpty(entry.Status, "open")),
				"title":                  title,
				"description":            strings.TrimSpace(firstNonEmpty(entry.Description, entry.Title)),
				"supportingPapers":       []string{},
				"confidence":             wisdev.ClampFloat(entry.Confidence, 0.25, 0.95),
				"suggestedApproaches":    uniqueStrings(append([]string(nil), entry.SupportingQueries...)),
				"keywords":               []string{},
				"potentialImpact":        "high",
				"sourceFamilies":         uniqueStrings(append([]string(nil), entry.SourceFamilies...)),
				"observedSourceFamilies": uniqueStrings(append([]string(nil), gapState.ObservedSourceFamilies...)),
				"observedEvidenceCount":  gapState.ObservedEvidenceCount,
			})
		}
		if len(payloads) > 0 {
			return payloads
		}
	}

	payloads := make([]map[string]any, 0, 8)
	appendPayload := func(gapType string, title string, description string, confidence float64, supportingQueries []string) {
		trimmedTitle := strings.TrimSpace(title)
		trimmedDescription := strings.TrimSpace(description)
		if trimmedTitle == "" && trimmedDescription == "" {
			return
		}
		if trimmedTitle == "" {
			trimmedTitle = trimmedDescription
		}
		if len(trimmedTitle) > 72 {
			trimmedTitle = strings.TrimSpace(trimmedTitle[:72]) + "..."
		}
		payloads = append(payloads, map[string]any{
			"id":                    fmt.Sprintf("loop_gap_%d", len(payloads)+1),
			"type":                  strings.TrimSpace(firstNonEmpty(gapType, "theoretical")),
			"status":                "open",
			"title":                 trimmedTitle,
			"description":           trimmedDescription,
			"supportingPapers":      []string{},
			"confidence":            wisdev.ClampFloat(confidence, 0.25, 0.95),
			"suggestedApproaches":   supportingQueries,
			"keywords":              []string{},
			"potentialImpact":       "high",
			"sourceFamilies":        uniqueStrings(append([]string(nil), gapState.ObservedSourceFamilies...)),
			"observedEvidenceCount": gapState.ObservedEvidenceCount,
		})
	}

	for _, aspect := range gapState.MissingAspects {
		appendPayload("coverage", aspect, aspect, gapState.Confidence, append([]string(nil), gapState.NextQueries...))
	}
	for _, sourceType := range gapState.MissingSourceTypes {
		appendPayload(
			"source_diversity",
			"Missing source coverage: "+sourceType,
			fmt.Sprintf("The loop did not gather enough %s evidence to close the research question.", strings.TrimSpace(sourceType)),
			gapState.Confidence,
			append([]string(nil), gapState.NextQueries...),
		)
	}
	for _, contradiction := range gapState.Contradictions {
		appendPayload(
			"contradiction",
			"Resolve contradiction",
			contradiction,
			gapState.Confidence,
			append([]string(nil), gapState.NextQueries...),
		)
	}
	for _, query := range gapState.Coverage.QueriesWithoutCoverage {
		appendPayload(
			"query_coverage",
			"No grounded evidence for query: "+query,
			fmt.Sprintf("The autonomous loop executed %q without adding grounded evidence.", strings.TrimSpace(query)),
			gapState.Confidence,
			[]string{strings.TrimSpace(query)},
		)
	}
	for _, query := range gapState.Coverage.UnexecutedPlannedQueries {
		appendPayload(
			"planned_query",
			"Unexecuted planned branch: "+query,
			fmt.Sprintf("The loop stopped before executing planned query %q.", strings.TrimSpace(query)),
			gapState.Confidence,
			[]string{strings.TrimSpace(query)},
		)
	}

	if len(payloads) == 0 && !gapState.Sufficient {
		appendPayload(
			"coverage",
			"Further evidence gathering recommended",
			firstNonEmpty(strings.TrimSpace(gapState.Reasoning), "The autonomous loop did not reach a fully grounded stopping point."),
			gapState.Confidence,
			append([]string(nil), gapState.NextQueries...),
		)
	}
	return payloads
}

func buildAutonomousGapPayloadsFromCoverageLedger(ledger []wisdev.CoverageLedgerEntry) []map[string]any {
	if len(ledger) == 0 {
		return nil
	}

	payloads := make([]map[string]any, 0, len(ledger))
	for _, entry := range ledger {
		if !strings.EqualFold(strings.TrimSpace(entry.Status), "open") {
			continue
		}
		title := strings.TrimSpace(firstNonEmpty(entry.Title, entry.Description))
		if title == "" {
			continue
		}
		if len(title) > 72 {
			title = strings.TrimSpace(title[:72]) + "..."
		}
		payloads = append(payloads, map[string]any{
			"id":                    strings.TrimSpace(firstNonEmpty(entry.ID, fmt.Sprintf("quest_gap_%d", len(payloads)+1))),
			"type":                  strings.TrimSpace(firstNonEmpty(entry.Category, "theoretical")),
			"status":                "open",
			"title":                 title,
			"description":           strings.TrimSpace(firstNonEmpty(entry.Description, entry.Title)),
			"supportingPapers":      []string{},
			"confidence":            wisdev.ClampFloat(entry.Confidence, 0.25, 0.95),
			"suggestedApproaches":   uniqueStrings(append([]string(nil), entry.SupportingQueries...)),
			"keywords":              []string{},
			"potentialImpact":       "high",
			"sourceFamilies":        uniqueStrings(append([]string(nil), entry.SourceFamilies...)),
			"observedEvidenceCount": 0,
		})
	}
	return payloads
}
