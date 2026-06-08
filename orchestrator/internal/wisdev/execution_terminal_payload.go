package wisdev

import (
	"fmt"
	"sort"
	"strings"
)

type terminalPaperCategory struct {
	Category string
	Sources  []Source
}

func buildExecutionTerminalPayload(session *AgentSession, status string, synthetic bool) map[string]any {
	payload := map[string]any{
		"status":          strings.TrimSpace(status),
		"sessionTerminal": true,
	}
	if payload["status"] == "" {
		payload["status"] = "completed"
	}
	if synthetic {
		payload["synthetic"] = true
	}
	if session == nil || session.Plan == nil {
		return payload
	}
	if session.Mode != "" {
		payload["mode"] = string(session.Mode)
	}
	if session.ServiceTier != "" {
		payload["serviceTier"] = string(session.ServiceTier)
	}

	plan := session.Plan
	payload["planId"] = plan.PlanID
	payload["totalSteps"] = len(plan.Steps)
	completedStepIDs := sortedTrueKeys(plan.CompletedStepIDs)
	failedStepIDs := sortedStringMapKeys(plan.FailedStepIDs)
	payload["completedSteps"] = len(completedStepIDs)
	payload["failedSteps"] = len(failedStepIDs)
	payload["completedStepIds"] = stringSliceToAny(completedStepIDs)
	payload["failedStepIds"] = stringSliceToAny(failedStepIDs)

	categories := collectTerminalPaperCategories(plan)
	papers := flattenTerminalPaperCategories(categories)
	payload["resultCount"] = len(papers)
	if len(papers) == 0 {
		return payload
	}
	paperItems := terminalPaperCategorySourcesToAny(categories)
	payload["papers"] = paperItems
	payload["sources"] = paperItems
	payload["categorizedSources"] = terminalPaperCategoriesToAny(categories)
	return payload
}

func applyExecutionTerminalPayloadProgress(session *AgentSession, payload map[string]any) {
	if session == nil || session.Plan == nil || len(payload) == 0 {
		return
	}
	if session.Plan.CompletedStepIDs == nil {
		session.Plan.CompletedStepIDs = map[string]bool{}
	}
	if session.Plan.FailedStepIDs == nil {
		session.Plan.FailedStepIDs = map[string]string{}
	}
	for _, stepID := range toStringSlice(payload["completedStepIds"]) {
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			continue
		}
		session.Plan.CompletedStepIDs[stepID] = true
		delete(session.Plan.FailedStepIDs, stepID)
	}
	for _, stepID := range toStringSlice(payload["failedStepIds"]) {
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			continue
		}
		if strings.TrimSpace(session.Plan.FailedStepIDs[stepID]) == "" {
			session.Plan.FailedStepIDs[stepID] = "step failed before terminal event"
		}
		delete(session.Plan.CompletedStepIDs, stepID)
	}
}

func collectTerminalPaperSources(plan *PlanState) []Source {
	return flattenTerminalPaperCategories(collectTerminalPaperCategories(plan))
}

func collectTerminalPaperCategories(plan *PlanState) []terminalPaperCategory {
	if plan == nil || len(plan.StepArtifacts) == 0 {
		return nil
	}
	orderedStepIDs := make([]string, 0, len(plan.StepArtifacts))
	for _, step := range plan.Steps {
		if _, ok := plan.StepArtifacts[step.ID]; ok {
			orderedStepIDs = append(orderedStepIDs, step.ID)
		}
	}
	if len(orderedStepIDs) < len(plan.StepArtifacts) {
		known := make(map[string]struct{}, len(orderedStepIDs))
		for _, stepID := range orderedStepIDs {
			known[stepID] = struct{}{}
		}
		extra := make([]string, 0, len(plan.StepArtifacts)-len(orderedStepIDs))
		for stepID := range plan.StepArtifacts {
			if _, ok := known[stepID]; !ok {
				extra = append(extra, stepID)
			}
		}
		sort.Strings(extra)
		orderedStepIDs = append(orderedStepIDs, extra...)
	}

	seen := map[string]struct{}{}
	categories := make([]terminalPaperCategory, 0)
	categoryIndexes := map[string]int{}
	for index, stepID := range orderedStepIDs {
		artifactSet := plan.StepArtifacts[stepID]
		sources := terminalSourcesFromArtifactSet(artifactSet)
		if len(sources) == 0 {
			continue
		}
		category := terminalCategoryForStep(plan, stepID, artifactSet, sources, index)
		for _, sourceGroup := range terminalDisplayCategoriesForSources(category, sources) {
			key := strings.ToLower(sourceGroup.Category)
			categoryIndex, ok := categoryIndexes[key]
			if !ok {
				categoryIndex = len(categories)
				categoryIndexes[key] = categoryIndex
				categories = append(categories, terminalPaperCategory{Category: sourceGroup.Category})
			}
			for _, source := range sourceGroup.Sources {
				key := terminalSourceIdentity(source)
				if key == "" {
					continue
				}
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				categories[categoryIndex].Sources = append(categories[categoryIndex].Sources, source)
			}
		}
	}
	out := categories[:0]
	for _, category := range categories {
		if len(category.Sources) > 0 {
			out = append(out, category)
		}
	}
	return out
}

func flattenTerminalPaperCategories(categories []terminalPaperCategory) []Source {
	if len(categories) == 0 {
		return nil
	}
	out := make([]Source, 0)
	for _, category := range categories {
		out = append(out, category.Sources...)
	}
	return out
}

func terminalPaperCategoriesToAny(categories []terminalPaperCategory) []any {
	out := make([]any, 0, len(categories))
	for _, category := range categories {
		if len(category.Sources) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"category": category.Category,
			"sources":  mapsToAny(terminalSourceArtifactMaps(category.Category, category.Sources)),
		})
	}
	return out
}

func terminalPaperCategorySourcesToAny(categories []terminalPaperCategory) []any {
	out := make([]any, 0)
	for _, category := range categories {
		for _, item := range terminalSourceArtifactMaps(category.Category, category.Sources) {
			out = append(out, item)
		}
	}
	return out
}

func terminalSourceArtifactMaps(category string, sources []Source) []map[string]any {
	items := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		item := sourceToArtifactMap(source)
		if strings.TrimSpace(category) != "" {
			item["category"] = category
			relevanceReason := terminalSourceRelevanceReason(source, category)
			item["relevanceReason"] = relevanceReason
			item["whyItMatters"] = relevanceReason
			item["relevanceChecked"] = true
			if relevanceScore := terminalSourceRelevanceScore(source); relevanceScore > 0 {
				item["relevanceScore"] = relevanceScore
			}
		}
		items = append(items, item)
	}
	return items
}

func terminalCategoryForStep(plan *PlanState, stepID string, artifactSet StepArtifactSet, sources []Source, index int) string {
	if artifactSet.Artifacts != nil {
		for _, key := range []string{"category", "categoryName", "category_name", "label", "topic", "focus", "branch", "hypothesis", "claim", "query"} {
			if category := strings.TrimSpace(AsOptionalString(artifactSet.Artifacts[key])); category != "" {
				return semanticTerminalCategory(category, sources, index)
			}
		}
	}

	if step := terminalPlanStepForID(plan, stepID); step != nil {
		if step.Params != nil {
			for _, key := range []string{"category", "categoryName", "category_name", "label", "topic", "focus", "branch", "hypothesis", "claim", "query"} {
				if category := strings.TrimSpace(AsOptionalString(step.Params[key])); category != "" {
					return semanticTerminalCategory(category, sources, index)
				}
			}
		}
		if reason := strings.TrimSpace(step.Reason); reason != "" {
			return semanticTerminalCategory(reason, sources, index)
		}
		if action := strings.TrimSpace(step.Action); action != "" {
			return terminalCategoryFromAction(action, sources, index)
		}
	}

	if action := strings.TrimSpace(artifactSet.Action); action != "" {
		return terminalCategoryFromAction(action, sources, index)
	}
	return deriveTerminalCategoryFromSources(sources, index)
}

func terminalPlanStepForID(plan *PlanState, stepID string) *PlanStep {
	if plan == nil || strings.TrimSpace(stepID) == "" {
		return nil
	}
	for i := range plan.Steps {
		if plan.Steps[i].ID == stepID {
			return &plan.Steps[i]
		}
	}
	return nil
}

func terminalCategoryFromAction(action string, sources []Source, index int) string {
	return deriveTerminalCategoryFromSources(sources, index)
}

func semanticTerminalCategory(raw string, sources []Source, index int) string {
	normalized := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(raw))
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized != "" && !isTechnicalTerminalCategory(normalized) && !isBroadTerminalCategoryLabel(normalized) {
		return trimTerminalCategoryLabel(normalized)
	}
	if normalized != "" && isBroadTerminalCategoryLabel(normalized) && len(sources) > 0 {
		return terminalSourceEvidenceCategory(sources[0], index)
	}
	return deriveTerminalCategoryFromSources(sources, index)
}

func terminalDisplayCategoriesForSources(category string, sources []Source) []terminalPaperCategory {
	if len(sources) == 0 {
		return nil
	}
	category = strings.TrimSpace(category)
	if !shouldSplitTerminalPaperCategory(category, sources) {
		return []terminalPaperCategory{{Category: firstNonEmpty(category, fallbackTerminalSemanticCategory(0)), Sources: sources}}
	}

	groups := make([]terminalPaperCategory, 0)
	indexes := map[string]int{}
	for index, source := range sources {
		sourceCategory := terminalSourceEvidenceCategory(source, index)
		key := strings.ToLower(sourceCategory)
		groupIndex, ok := indexes[key]
		if !ok {
			groupIndex = len(groups)
			indexes[key] = groupIndex
			groups = append(groups, terminalPaperCategory{Category: sourceCategory})
		}
		groups[groupIndex].Sources = append(groups[groupIndex].Sources, source)
	}
	return groups
}

func shouldSplitTerminalPaperCategory(category string, sources []Source) bool {
	if len(sources) <= 1 {
		return false
	}
	return len(sources) >= 20 || isBroadTerminalCategoryLabel(category)
}

func terminalSourceEvidenceCategory(source Source, index int) string {
	text := strings.ToLower(strings.Join([]string{
		source.Title,
		source.Summary,
		source.Abstract,
		strings.Join(source.Keywords, " "),
	}, " "))

	switch {
	case containsAnyTerminalTerm(text, "survey", "review", "overview", "introduction", "introductory", "tutorial", "taxonomy", "primer", "roadmap", "foundation", "foundations"):
		return "Introduction and surveys"
	case containsAnyTerminalTerm(text, "continual", "continuous", "lifelong", "incremental", "retention", "catastrophic forgetting", "forgetting", "knowledge editing", "knowledge retention", "model editing", "unlearning"):
		return "Knowledge retention and continual learning"
	case containsAnyTerminalTerm(text, "inference", "serving", "latency", "decoding", "speculative", "quantization", "compression", "throughput", "accelerator", "acceleration", "deployment"):
		return "Inference advances"
	case containsAnyTerminalTerm(text, "training", "pretrain", "pre-training", "finetune", "fine tune", "fine-tune", "alignment", "rlhf", "rlaif", "distillation", "optimizer", "optimization", "gradient", "curriculum", "reinforcement learning", "self-supervised", "supervised fine"):
		return "Training advances"
	case containsAnyTerminalTerm(text, "architecture", "architectures", "transformer", "attention", "state space", "mamba", "mixture of experts", "moe", "diffusion", "retrieval augmented", "rag", "neural architecture", "agent architecture"):
		return "Architectures"
	case containsAnyTerminalTerm(text, "method", "methods", "algorithm", "approach", "framework", "benchmark", "evaluation", "metric", "dataset", "experimental", "protocol"):
		return "Methods and evaluation"
	case containsAnyTerminalTerm(text, "application", "applications", "clinical", "education", "robot", "robotics", "code generation", "software engineering", "vision", "speech", "biology", "medicine"):
		return "Applications"
	default:
		return deriveTerminalCategoryFromSources([]Source{source}, index)
	}
}

func containsAnyTerminalTerm(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func isTechnicalTerminalCategory(label string) bool {
	lower := strings.ToLower(strings.TrimSpace(label))
	return strings.HasPrefix(lower, "wisdev step") ||
		strings.HasPrefix(lower, "wisdev evidence") ||
		strings.HasPrefix(lower, "step ") ||
		strings.HasPrefix(lower, "evidence search ")
}

func isBroadTerminalCategoryLabel(label string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(label)), " "))
	if normalized == "" {
		return true
	}
	if strings.Contains(normalized, "?") || len(strings.Fields(normalized)) > 12 {
		return true
	}
	for _, prefix := range []string{
		"what are ",
		"what is ",
		"how do ",
		"how does ",
		"why ",
		"are there ",
		"parallel evidence gathering for ",
		"retrieve candidate papers",
		"retrieve initial seed corpus",
		"retrieve high confidence papers",
		"parallel retrieval",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func fallbackTerminalSemanticCategory(index int) string {
	if index < 0 {
		index = 0
	}
	return fmt.Sprintf("Research branch %d", index+1)
}

func deriveTerminalCategoryFromSources(sources []Source, index int) string {
	for _, source := range sources {
		for _, candidate := range []string{source.Title, source.Summary, source.Abstract} {
			if phrase := terminalCategoryPhrase(candidate); phrase != "" {
				return phrase
			}
		}
		for _, candidate := range append([]string{}, source.Keywords...) {
			if phrase := terminalCategoryPhrase(candidate); phrase != "" {
				return phrase
			}
		}
	}
	return fallbackTerminalSemanticCategory(index)
}

func terminalCategoryPhrase(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return !(r == '+' || r == '-' || r == '/' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	})
	words := make([]string, 0, 4)
	for _, part := range parts {
		part = strings.Trim(part, "._-")
		if len(part) <= 1 || terminalCategoryStopwords[strings.ToLower(part)] {
			continue
		}
		words = append(words, part)
		if len(words) == 4 {
			break
		}
	}
	if len(words) == 0 {
		return ""
	}
	for idx, word := range words {
		if word == strings.ToUpper(word) {
			continue
		}
		words[idx] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

func trimTerminalCategoryLabel(label string) string {
	label = strings.Join(strings.Fields(strings.TrimSpace(label)), " ")
	if len(label) <= 72 {
		return label
	}
	return strings.TrimSpace(label[:69]) + "..."
}

var terminalCategoryStopwords = map[string]bool{
	"about": true, "abstract": true, "across": true, "advance": true, "advances": true, "analysis": true,
	"and": true, "are": true, "based": true, "because": true, "branch": true, "branches": true, "case": true,
	"category": true, "compares": true, "evidence": true, "for": true, "from": true, "grouped": true, "into": true,
	"latest": true, "method": true, "methods": true, "new": true, "paper": true, "papers": true, "recent": true,
	"research": true, "result": true, "results": true, "search": true, "source": true, "sources": true, "study": true,
	"supports": true, "system": true, "systems": true, "that": true, "the": true, "this": true, "through": true,
	"as": true, "at": true, "by": true, "in": true, "is": true, "of": true, "on": true, "or": true,
	"to": true, "under": true, "using": true, "via": true, "with": true,
}

func terminalSourceRelevanceReason(source Source, category string) string {
	category = strings.TrimSpace(firstNonEmpty(category, "this WisDev research branch"))
	if summary := strings.TrimSpace(firstNonEmpty(source.Summary, source.Abstract)); summary != "" {
		return fmt.Sprintf("WisDev grouped this source under %q because it provides branch-specific evidence: %s", category, trimTerminalCategoryLabel(summary))
	}
	if title := strings.TrimSpace(source.Title); title != "" {
		return fmt.Sprintf("WisDev grouped %q under %q as evidence for that branch of the autonomous research plan.", title, category)
	}
	return fmt.Sprintf("WisDev grouped this source under %q as evidence for that branch of the autonomous research plan.", category)
}

func terminalSourceRelevanceScore(source Source) int {
	score := source.Score
	if score <= 0 {
		return 0
	}
	if score <= 1 {
		score *= 100
	}
	if score > 100 {
		score = 100
	}
	return int(score)
}

func terminalSourcesFromArtifactSet(artifactSet StepArtifactSet) []Source {
	if artifactSet.PaperBundle != nil && len(artifactSet.PaperBundle.Papers) > 0 {
		return append([]Source(nil), artifactSet.PaperBundle.Papers...)
	}
	for _, key := range []string{"papers", "sources", "canonicalSources", "canonical_sources", "citations", "verifiedRecords"} {
		if sources := ArtifactMapsToSources(firstArtifactMaps(artifactSet.Artifacts[key])); len(sources) > 0 {
			return sources
		}
	}
	return nil
}

func terminalSourceIdentity(source Source) string {
	for _, candidate := range []string{source.DOI, source.ID, source.ArxivID, source.Link, source.Title} {
		if value := strings.ToLower(strings.TrimSpace(candidate)); value != "" {
			return value
		}
	}
	return ""
}

func sortedTrueKeys(values map[string]bool) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key, ok := range values {
		key = strings.TrimSpace(key)
		if ok && key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func sortedStringMapKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}
