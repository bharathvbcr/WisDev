package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/llm"
	internalsearch "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
	llmv1 "github.com/bharathvbcr/wisdev-arc/orchestrator/proto/llm"
)

var wisdevAnalyzeQueryHandlerTimeout = 20 * time.Second
var wisdevQuestionRecommendationTimeout = 25 * time.Second

// maxAgentAnswerValues bounds how many values (and display values) a single
// multi-select answer may carry. Kept in sync with the frontend selection cap
// (frontend/services/wisdevAgent/answerNormalization.ts) so the UI blocks the
// user before this validation would reject the submission with a 400.
const maxAgentAnswerValues = 24

type dynamicQuestionOptionsResult struct {
	options     []any
	source      string
	explanation string
}

type dynamicQuestionOptionsInflightCall struct {
	done   chan struct{}
	result dynamicQuestionOptionsResult
}

func dynamicQuestionOptionsSingleflightKey(sessionID string, questionID string, session map[string]any) string {
	normalizedSessionID := strings.TrimSpace(sessionID)
	normalizedQuestionID := strings.TrimSpace(questionID)
	return normalizedSessionID + ":" + normalizedQuestionID + ":" + dynamicQuestionOptionContextSignature(normalizedQuestionID, session)
}

func dynamicQuestionOptionContextSignature(questionID string, session map[string]any) string {
	normalizedQuestionID := strings.TrimSpace(questionID)
	optionContext := "default"
	switch normalizedQuestionID {
	case "q5_study_types":
		subtopics := uniqueStrings(answeredAgentQuestionValues(session, "q4_subtopics"))
		if len(subtopics) > 0 {
			sort.Strings(subtopics)
			optionContext = "q4:" + strings.Join(subtopics, "|")
		} else {
			optionContext = "speculative"
		}
	case "q6_exclusions", "q7_evidence_quality", "q8_output_focus":
		contextParts := []string{}
		subtopics := uniqueStrings(answeredAgentQuestionValues(session, "q4_subtopics"))
		if len(subtopics) > 0 {
			sort.Strings(subtopics)
			contextParts = append(contextParts, "q4:"+strings.Join(subtopics, "|"))
		}
		studyTypes := uniqueStrings(answeredAgentQuestionValues(session, "q5_study_types"))
		if len(studyTypes) > 0 {
			sort.Strings(studyTypes)
			contextParts = append(contextParts, "q5:"+strings.Join(studyTypes, "|"))
		}
		if len(contextParts) > 0 {
			optionContext = strings.Join(contextParts, ";")
		} else {
			optionContext = "speculative"
		}
	}
	return optionContext
}

func dynamicQuestionOptionsNeedContextMatch(questionID string, contextSignature string) bool {
	switch strings.TrimSpace(questionID) {
	case "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus":
		return strings.TrimSpace(contextSignature) != "" && strings.TrimSpace(contextSignature) != "speculative"
	default:
		return false
	}
}

func dynamicQuestionOptionsNeedRefresh(session map[string]any, question map[string]any, questionID string) bool {
	if len(questionOptionValues(question["options"])) == 0 {
		return false
	}
	expectedContext := dynamicQuestionOptionContextSignature(questionID, session)
	if !dynamicQuestionOptionsNeedContextMatch(questionID, expectedContext) {
		return false
	}
	storedContext := strings.TrimSpace(wisdev.AsOptionalString(question["optionsContextKey"]))
	return storedContext == "" || storedContext != expectedContext
}

func isDynamicQuestionOptionID(questionID string) bool {
	switch strings.TrimSpace(questionID) {
	case "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus":
		return true
	default:
		return false
	}
}

func isAIQuestionOptionSource(source string) bool {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "ai", "llm_structured":
		return true
	default:
		return false
	}
}

func dynamicQuestionOptionsWithDescriptions(questionID string, query string, domain string, options []map[string]any) ([]map[string]any, bool) {
	switch strings.TrimSpace(questionID) {
	case "q4_subtopics", "q5_study_types", "q6_exclusions", "q7_evidence_quality", "q8_output_focus":
	default:
		return options, false
	}
	enriched := make([]map[string]any, 0, len(options))
	changed := false
	for _, option := range options {
		next := cloneAnyMap(option)
		label := strings.TrimSpace(wisdev.AsOptionalString(next["label"]))
		if label == "" {
			label = strings.TrimSpace(wisdev.AsOptionalString(next["value"]))
		}
		if strings.TrimSpace(wisdev.AsOptionalString(next["description"])) == "" {
			if description := dynamicQuestionOptionDescription(questionID, label, query, domain); description != "" {
				next["description"] = description
				changed = true
			}
		}
		enriched = append(enriched, next)
	}
	return enriched, changed
}

func dynamicQuestionOptionMapsAsAny(options []map[string]any) []any {
	out := make([]any, 0, len(options))
	for _, option := range options {
		out = append(out, option)
	}
	return out
}

func agentSessionIncludesQuestion(session map[string]any, questionID string) bool {
	questionID = strings.TrimSpace(questionID)
	if len(session) == 0 || questionID == "" {
		return false
	}
	for _, question := range sliceAnyMap(session["questions"]) {
		if wisdev.AsOptionalString(question["id"]) == questionID {
			return true
		}
	}
	for _, id := range sliceStrings(session["questionSequence"]) {
		if strings.TrimSpace(id) == questionID {
			return true
		}
	}
	return false
}

// wisdevAnalyzeQueryBudget returns an adaptive handler timeout that accounts
// for cold-start conditions. During the cold-start window, sidecar
// initialization and ADC token acquisition can consume 5-15s, so the budget
// is extended to prevent premature heuristic fallbacks.
func wisdevAnalyzeQueryBudget() time.Duration {
	if llm.IsColdStartWindow() {
		return wisdevAnalyzeQueryHandlerTimeout + 20*time.Second
	}
	return wisdevAnalyzeQueryHandlerTimeout
}

func dynamicQuestionOptionContextAnchors(query string, domain string, contextGroups ...[]string) []string {
	candidates := []string{}
	for _, group := range contextGroups {
		candidates = append(candidates, group...)
	}
	candidates = append(candidates, queryAwareSubtopicHints(query, domain)...)
	candidates = append(candidates, deriveQuerySubtopics(query, 6)...)
	if len(normalizeDynamicOptionValues(candidates)) == 0 {
		candidates = append(candidates, defaultDomainSubtopics(domain)...)
	}

	anchors := []string{}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		anchor := normalizeDynamicQuestionOptionAnchor(candidate)
		if anchor == "" {
			continue
		}
		key := strings.ToLower(anchor)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		anchors = append(anchors, anchor)
		if len(anchors) >= 4 {
			break
		}
	}
	return anchors
}

func normalizeDynamicQuestionOptionAnchor(value string) string {
	normalized := strings.NewReplacer("_", " ", "/", " ", "-", " ").Replace(strings.TrimSpace(value))
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 5 {
		fields = fields[:5]
	}

	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		cleaned := strings.Trim(field, ".,;:!?")
		if cleaned == "" {
			continue
		}
		parts = append(parts, normalizeResearchDisplayToken(cleaned))
	}
	anchor := strings.TrimSpace(strings.Join(parts, " "))
	if anchor == "" {
		return ""
	}
	switch strings.ToLower(anchor) {
	case "background", "methods", "method", "limitations", "applications", "theoretical background",
		"empirical evidence", "comparative analysis", "future directions", "systematic review",
		"meta analysis", "review", "survey", "best papers first", "broad coverage map":
		return ""
	default:
		return anchor
	}
}

func defaultEvidenceQualityOptions(query string, domain string, contextGroups ...[]string) []string {
	text := strings.ToLower(strings.TrimSpace(query + " " + domain))
	anchors := dynamicQuestionOptionContextAnchors(query, domain, contextGroups...)
	options := make([]string, 0, 12)
	if containsAnyTerm(text,
		[]string{"reinforcement learning from human feedback", "preference learning", "human feedback", "reward model", "reward modeling", "alignment"},
		[]string{"rlhf"}) {
		options = append(options,
			"Human preference label reliability",
			"Reward model validation benchmarks",
			"Policy-optimization ablation evidence",
			"Reward hacking and safety failure evidence",
		)
	}
	if containsAny(text, []string{"benchmark", "evaluation", "leaderboard", "baseline", "model comparison", "generalization"}) {
		options = append(options,
			"Benchmark protocol reproducibility",
			"Baseline and dataset comparability",
			"Metric validity and error analysis",
		)
	}
	if containsAny(text, []string{"clinical", "medicine", "health", "patient", "treatment", "therapy"}) {
		options = append(options,
			"Randomized or controlled clinical evidence",
			"Safety and adverse-event evidence",
			"Population and inclusion fit",
		)
	}
	if containsAny(text, []string{"systematic review", "meta-analysis", "meta analysis", "prisma", "evidence synthesis"}) {
		options = append(options,
			"Systematic review inclusion quality",
			"Publication-bias assessment",
		)
	}
	if len(anchors) > 0 {
		primary := anchors[0]
		options = append(options,
			primary+" validation evidence",
			primary+" reproducibility evidence",
		)
		if len(anchors) > 1 {
			options = append(options, primary+" vs "+anchors[1]+" evidence quality")
		}
		if containsAnyTerm(text, []string{"benchmark", "model", "computer"}, []string{"ai", "rlhf"}) {
			options = append(options, primary+" benchmark protocol quality")
		}
		if strings.Contains(text, "human") || strings.Contains(text, "feedback") || strings.Contains(text, "label") || strings.Contains(text, "rlhf") {
			options = append(options, "Human feedback label reliability")
		}
	}
	options = append(options, queryGroundedEvidenceQualityFallbacks(anchors)...)
	if strings.Contains(text, "clinical") || strings.Contains(text, "medicine") || strings.Contains(text, "health") {
		options = append(options, "Randomized or controlled evidence", "Systematic review evidence")
	}
	if containsAnyTerm(text, []string{"benchmark", "model", "computer"}, []string{"ai"}) {
		options = append(options, "Reproducible benchmarks", "Ablation-backed evidence")
	}
	return uniqueStrings(options)
}

func defaultOutputFocusOptions(query string, domain string, contextGroups ...[]string) []string {
	text := strings.ToLower(strings.TrimSpace(query + " " + domain))
	anchors := dynamicQuestionOptionContextAnchors(query, domain, contextGroups...)
	options := make([]string, 0, 12)
	if containsAnyTerm(text,
		[]string{"reinforcement learning from human feedback", "preference learning", "human feedback", "reward model", "reward modeling", "alignment"},
		[]string{"rlhf"}) {
		options = append(options,
			"Reward-model comparison takeaways",
			"Preference-data failure modes",
			"Alignment safety tradeoffs",
		)
	}
	if containsAny(text, []string{"benchmark", "evaluation", "leaderboard", "baseline", "model comparison", "generalization"}) {
		options = append(options,
			"Benchmark leaderboard caveats",
			"Method and dataset tradeoffs",
			"Reproducibility checklist",
		)
	}
	if containsAny(text, []string{"clinical", "medicine", "health", "patient", "treatment", "therapy"}) {
		options = append(options,
			"Clinical relevance and safety",
			"Patient-selection implications",
		)
	}
	if len(anchors) > 0 {
		primary := anchors[0]
		options = append(options,
			primary+" evidence map",
			primary+" gaps and limitations",
			primary+" method tradeoffs",
		)
		if len(anchors) > 1 {
			options = append(options, primary+" vs "+anchors[1]+" comparison")
		}
		if containsAnyTerm(text, []string{"benchmark", "model", "computer"}, []string{"ai", "rlhf"}) {
			options = append(options, primary+" benchmark takeaways")
		}
	}
	options = append(options, queryGroundedOutputFocusFallbacks(anchors)...)
	if strings.Contains(text, "benchmark") || strings.Contains(text, "compare") || strings.Contains(text, "model") {
		options = append(options, "Benchmark comparison", "Method tradeoffs")
	}
	if strings.Contains(text, "clinical") || strings.Contains(text, "medicine") || strings.Contains(text, "health") {
		options = append(options, "Clinical relevance", "Safety and adverse effects")
	}
	return uniqueStrings(options)
}

func queryGroundedEvidenceQualityFallbacks(anchors []string) []string {
	if len(anchors) == 0 {
		return []string{
			"Query-specific validation evidence",
			"Query-specific replication signals",
			"Query-specific data fit",
		}
	}
	primary := anchors[0]
	options := []string{
		primary + " validation evidence",
		primary + " replication signals",
		primary + " data fit",
	}
	if len(anchors) > 1 {
		options = append(options, primary+" vs "+anchors[1]+" evidence contrast")
	}
	return options
}

func queryGroundedOutputFocusFallbacks(anchors []string) []string {
	if len(anchors) == 0 {
		return []string{
			"Query-specific evidence map",
			"Query-specific unresolved gaps",
			"Query-specific comparison angles",
		}
	}
	primary := anchors[0]
	options := []string{
		primary + " evidence map",
		primary + " unresolved gaps",
		primary + " comparison angles",
	}
	if len(anchors) > 1 {
		options = append(options, primary+" vs "+anchors[1]+" synthesis contrast")
	}
	return options
}

func defaultExclusionOptions(query string, domain string, contextGroups ...[]string) []string {
	contextTerms := flattenStringGroups(contextGroups...)
	text := strings.ToLower(strings.TrimSpace(query + " " + domain + " " + strings.Join(contextTerms, " ")))
	options := []string{"No exclusions"}
	if containsAny(text, []string{"biomedical", "clinical", "medicine", "patient", "therapy", "disease", "biomarker", "single-cell", "crispr", "assay"}) {
		options = append(options,
			"Exclude non-human evidence unless mechanistic",
			"Exclude underpowered cohorts",
			"Exclude unvalidated assay-only findings",
			"Exclude non-primary research",
		)
	}
	if containsAnyTerm(text, []string{"benchmark", "model", "evaluation", "leaderboard"}, []string{"ai"}) {
		options = append(options,
			"Exclude non-reproducible benchmarks",
			"Exclude papers without baseline comparisons",
			"Exclude result-only reports without methods",
		)
	}
	options = append(options,
		"Exclude preprints",
		"Exclude non-English studies",
		"Exclude non-peer-reviewed sources",
	)
	return uniqueStrings(options)
}

func flattenStringGroups(groups ...[]string) []string {
	out := []string{}
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func normalizeQuestionOptionValue(label string) string {
	value := strings.ToLower(strings.TrimSpace(label))
	replacer := strings.NewReplacer("&", " and ", "/", " ", "-", " ", "(", " ", ")", " ", ",", " ")
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), "_")
}

func dynamicQuestionOptionDescription(questionID string, label string, query string, domain string) string {
	trimmedLabel := strings.TrimSpace(label)
	if trimmedLabel == "" {
		return ""
	}
	lowerLabel := strings.ToLower(trimmedLabel)
	switch strings.TrimSpace(questionID) {
	case "q6_exclusions":
		switch {
		case strings.Contains(lowerLabel, "no exclusion"):
			return "Keep the search broad and let later evidence scoring reject weak papers."
		case strings.Contains(lowerLabel, "non-human") || strings.Contains(lowerLabel, "animal"):
			return "Filter animal or model-organism evidence unless it directly explains mechanism or translation."
		case strings.Contains(lowerLabel, "underpowered") || strings.Contains(lowerLabel, "small"):
			return "Down-rank studies whose sample size or statistical power is too weak for the claim."
		case strings.Contains(lowerLabel, "assay") || strings.Contains(lowerLabel, "validation"):
			return "Filter findings that only report exploratory assay signals without independent validation."
		case strings.Contains(lowerLabel, "non-primary") || strings.Contains(lowerLabel, "review"):
			return "Prefer original evidence over reviews, commentary, or secondary summaries for this pass."
		case strings.Contains(lowerLabel, "preprint"):
			return "Filter preprints when formally reviewed evidence is needed for the current question."
		case strings.Contains(lowerLabel, "non-english"):
			return "Limit retrieval to English-language records for this pass."
		case strings.Contains(lowerLabel, "non-peer-reviewed"):
			return "Prioritize peer-reviewed venues and indexed literature."
		default:
			return fmt.Sprintf("Apply %s as an exclusion or down-ranking rule for this research pass.", trimmedLabel)
		}
	case "q4_subtopics":
		switch {
		case strings.Contains(lowerLabel, "reward model"):
			return "Focus retrieval on how reward models are trained, validated, and compared."
		case strings.Contains(lowerLabel, "preference data") || strings.Contains(lowerLabel, "human feedback"):
			return "Look for evidence about feedback collection, label quality, annotator agreement, and preference data bias."
		case strings.Contains(lowerLabel, "policy optimization"):
			return "Prioritize papers on PPO-style optimization, reward-policy coupling, and RLHF training stability."
		case strings.Contains(lowerLabel, "safety") || strings.Contains(lowerLabel, "alignment failure") || strings.Contains(lowerLabel, "reward hacking"):
			return "Surface papers on failure modes, misuse risks, reward hacking, and alignment tradeoffs."
		case strings.Contains(lowerLabel, "benchmark") || strings.Contains(lowerLabel, "evaluation protocol"):
			return "Target benchmark datasets, evaluation protocols, baselines, and reproducibility constraints."
		case strings.Contains(lowerLabel, "patient") || strings.Contains(lowerLabel, "clinical"):
			return "Focus retrieval on patient fit, clinical outcomes, safety signals, and care-context constraints."
		case strings.Contains(lowerLabel, "publication bias") || strings.Contains(lowerLabel, "study quality"):
			return "Track how inclusion criteria, bias assessment, and study quality affect the evidence base."
		}
		return fmt.Sprintf("Focus retrieval on %s within the current research question.", trimmedLabel)
	case "q5_study_types":
		switch {
		case strings.Contains(lowerLabel, "benchmark"):
			return "Include benchmark studies with explicit tasks, baselines, datasets, and comparable metrics."
		case strings.Contains(lowerLabel, "ablation"):
			return "Include ablation studies that isolate which components drive the reported results."
		case strings.Contains(lowerLabel, "human evaluation") || strings.Contains(lowerLabel, "user study"):
			return "Include papers with human judgments, annotation protocols, rater agreement, or user-centered evaluation."
		case strings.Contains(lowerLabel, "safety evaluation"):
			return "Include studies that test harmful behavior, failure cases, robustness, or alignment risks."
		case strings.Contains(lowerLabel, "cohort") || strings.Contains(lowerLabel, "observational"):
			return "Include observational evidence that can expose population fit, confounding, and real-world outcomes."
		case strings.Contains(lowerLabel, "randomized") || strings.Contains(lowerLabel, "controlled trial"):
			return "Include controlled comparisons that can support stronger causal or intervention claims."
		case strings.Contains(lowerLabel, "systematic review") || strings.Contains(lowerLabel, "meta-analysis"):
			return "Include evidence syntheses with explicit search criteria, inclusion rules, and bias assessment."
		case strings.Contains(lowerLabel, "comparative"):
			return "Include side-by-side comparisons that explain where each method or evidence type is stronger."
		}
		return fmt.Sprintf("Include papers that use %s as a method or evidence design.", trimmedLabel)
	case "q7_evidence_quality":
		switch {
		case strings.Contains(lowerLabel, "preference label") || strings.Contains(lowerLabel, "label reliability") || strings.Contains(lowerLabel, "human feedback"):
			return "Check whether preference labels, annotator agreement, and feedback protocols are reliable enough to trust."
		case strings.Contains(lowerLabel, "reward model validation"):
			return "Prefer studies that validate reward models against held-out preferences, adversarial cases, or downstream policy behavior."
		case strings.Contains(lowerLabel, "policy") && strings.Contains(lowerLabel, "ablation"):
			return "Look for ablations that separate policy optimization effects from reward-model and data-quality effects."
		case strings.Contains(lowerLabel, "reward hacking") || strings.Contains(lowerLabel, "safety failure"):
			return "Surface evidence that probes reward hacking, specification gaming, robustness failures, or unsafe behaviors."
		case strings.Contains(lowerLabel, "metric validity"):
			return "Check whether reported metrics match the actual research claim and are robust to dataset or protocol changes."
		case strings.Contains(lowerLabel, "dataset comparability"):
			return "Prefer evidence where datasets, baselines, and evaluation splits are comparable across methods."
		case strings.Contains(lowerLabel, "adverse-event") || strings.Contains(lowerLabel, "population"):
			return "Prioritize evidence with clear safety reporting, population fit, and inclusion or exclusion criteria."
		case strings.Contains(lowerLabel, "label reliability") || strings.Contains(lowerLabel, "human feedback"):
			return "Check whether human-preference or labeling evidence is consistent enough to trust."
		case strings.Contains(lowerLabel, "benchmark protocol"):
			return "Prefer benchmark papers with clear tasks, baselines, metrics, and reproducible protocols."
		case strings.Contains(lowerLabel, "validation evidence"):
			return fmt.Sprintf("Prioritize papers that validate %s with external, held-out, or comparative evidence.", trimmedLabel)
		case strings.Contains(lowerLabel, "reproducibility evidence"):
			return fmt.Sprintf("Favor studies that make %s reproducible through data, code, protocols, or repeated trials.", trimmedLabel)
		case strings.Contains(lowerLabel, "evidence quality"):
			return fmt.Sprintf("Compare how strong the evidence is across %s.", trimmedLabel)
		case strings.Contains(lowerLabel, "randomized") || strings.Contains(lowerLabel, "controlled"):
			return "Prioritize controlled designs that can support stronger causal or comparative claims."
		case strings.Contains(lowerLabel, "systematic review"):
			return "Favor papers that synthesize evidence across multiple studies with explicit inclusion criteria."
		case strings.Contains(lowerLabel, "peer-reviewed"):
			return "Prefer vetted publications from journals, conferences, or comparable peer-review venues."
		case strings.Contains(lowerLabel, "transparent"):
			return "Look for papers that clearly expose methods, datasets, measurements, and analysis choices."
		case strings.Contains(lowerLabel, "replication") || strings.Contains(lowerLabel, "validation"):
			return "Emphasize findings that were replicated, externally validated, or tested on held-out settings."
		case strings.Contains(lowerLabel, "citation"):
			return "Surface influential papers while still checking recency and methodological fit."
		case strings.Contains(lowerLabel, "recent"):
			return "Bias toward newer evidence so the review reflects the current state of the field."
		case strings.Contains(lowerLabel, "open data") || strings.Contains(lowerLabel, "code"):
			return "Favor studies with public artifacts that make methods easier to inspect or reproduce."
		case strings.Contains(lowerLabel, "benchmark"):
			return "Prefer papers with clear benchmark suites, baselines, protocols, and reproducible comparisons."
		case strings.Contains(lowerLabel, "ablation"):
			return "Prioritize studies that isolate components and measure how each design choice changes results."
		default:
			return fmt.Sprintf("Use %s as the quality bar for ranking candidate papers.", trimmedLabel)
		}
	case "q8_output_focus":
		switch {
		case strings.Contains(lowerLabel, "reward-model comparison"):
			return "Compare reward-model objectives, validation setup, and downstream policy effects in the final synthesis."
		case strings.Contains(lowerLabel, "preference-data failure"):
			return "Center the output on feedback-source bias, label noise, coverage gaps, and their effect on conclusions."
		case strings.Contains(lowerLabel, "alignment safety"):
			return "Explain the main safety tradeoffs, unresolved failure modes, and where evidence is still weak."
		case strings.Contains(lowerLabel, "leaderboard caveat"):
			return "Separate headline benchmark scores from protocol limits, dataset leakage risks, and baseline fairness."
		case strings.Contains(lowerLabel, "dataset tradeoff"):
			return "Compare methods through the datasets, assumptions, and evaluation constraints behind each result."
		case strings.Contains(lowerLabel, "reproducibility checklist"):
			return "End with the data, code, protocol, and replication details needed to trust or rerun the evidence."
		case strings.Contains(lowerLabel, "patient-selection"):
			return "Frame clinical conclusions around who the evidence applies to and where population fit is uncertain."
		case strings.Contains(lowerLabel, "evidence map"):
			return fmt.Sprintf("Organize the final answer around where %s has strong, weak, or missing evidence.", trimmedLabel)
		case strings.Contains(lowerLabel, "benchmark takeaway"):
			return "Summarize benchmark setup, baselines, and the practical meaning of the reported scores."
		case strings.Contains(lowerLabel, "method tradeoffs"):
			return fmt.Sprintf("Explain the practical tradeoffs behind %s instead of only ranking papers.", trimmedLabel)
		case strings.Contains(lowerLabel, "best papers"):
			return "Rank the strongest papers first and keep weaker supporting evidence secondary."
		case strings.Contains(lowerLabel, "coverage"):
			return "Build a broad map of themes, methods, and evidence clusters before narrowing."
		case strings.Contains(lowerLabel, "gap") || strings.Contains(lowerLabel, "limitation"):
			return "Highlight unresolved questions, weak evidence, and places where follow-up work is needed."
		case strings.Contains(lowerLabel, "method comparison"):
			return "Compare approaches side by side, including strengths, weaknesses, and where each fits."
		case strings.Contains(lowerLabel, "contradiction") || strings.Contains(lowerLabel, "disagreement"):
			return "Call out conflicting findings and explain which assumptions or methods may drive them."
		case strings.Contains(lowerLabel, "practical"):
			return "Emphasize implications for implementation, decision-making, or real-world use."
		case strings.Contains(lowerLabel, "benchmark"):
			return "Center the output on benchmark setup, baselines, metrics, and comparative performance."
		case strings.Contains(lowerLabel, "tradeoff"):
			return "Summarize the main tradeoffs between methods instead of only ranking winners."
		case strings.Contains(lowerLabel, "clinical"):
			return "Prioritize clinical usefulness, patient relevance, and translational implications."
		case strings.Contains(lowerLabel, "safety") || strings.Contains(lowerLabel, "adverse"):
			return "Surface safety signals, risks, adverse effects, and uncertainty around harms."
		default:
			context := strings.TrimSpace(query)
			if context == "" {
				context = strings.TrimSpace(domain)
			}
			if context != "" {
				return fmt.Sprintf("Shape the final synthesis around %s for %s.", trimmedLabel, context)
			}
			return fmt.Sprintf("Shape the final synthesis around %s.", trimmedLabel)
		}
	default:
		return ""
	}
}

func dynamicQuestionOptionPayload(questionID string, value string, label string, query string, domain string) map[string]any {
	return normalizeQuestionOptionPayload(
		value,
		label,
		dynamicQuestionOptionDescription(questionID, label, query, domain),
		"",
	)
}

func dynamicQuestionOptionExcluded(label string, excluded []string) bool {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return true
	}
	normalizedValue := normalizeQuestionOptionValue(trimmed)
	for _, value := range normalizeDynamicOptionValues(excluded) {
		if strings.EqualFold(trimmed, value) || strings.EqualFold(normalizedValue, normalizeQuestionOptionValue(value)) {
			return true
		}
	}
	return false
}

func dynamicQuestionOptionKind(questionID string) (string, string, string, bool) {
	switch strings.TrimSpace(questionID) {
	case "q6_exclusions":
		return "exclusions_generate",
			"exclusion filters for retrieval and evidence ranking",
			"Generate exclusion or down-ranking options that are specific to the query, detected domain, selected subtopics, and selected study types. Infer which constraints could materially improve evidence quality; for biomedical research, adapt to the actual population, model organism, assay, intervention, endpoint, validation stage, or study design instead of using a fixed checklist. Include a broad no-exclusion option only when keeping the search broad is a reasonable user choice.",
			true
	case "q7_evidence_quality":
		return "evidence_quality_generate",
			"evidence-quality filters used to rank candidate papers",
			"Generate evidence-quality options that are specific to the query, detected domain, selected subtopics, and selected study types. Infer the relevant scientific standards from context; for biomedical research, adapt to the actual modality, population, assay, mechanism, endpoint, or translational question instead of using a fixed checklist. Avoid generic labels like Peer-reviewed evidence, Recent evidence, and Transparent methods unless the query makes them unusually important.",
			true
	case "q8_output_focus":
		return "output_focus_generate",
			"final synthesis focus options",
			"Generate final-output focus options that shape how the answer should be organized for this query and domain. Infer the useful synthesis angles from context; for biomedical research, adapt to the actual disease area, intervention, biomarker, cohort, mechanism, or translational workflow instead of using a fixed checklist. Avoid generic labels like Best papers first, Broad coverage map, and Practical implications unless the query makes them unusually important.",
			true
	default:
		return "", "", "", false
	}
}

func buildStructuredDynamicQuestionOptions(ctx context.Context, agentGateway *wisdev.AgentGateway, questionID string, query string, domain string, selectedSubtopics []string, selectedStudyTypes []string, excluded []string, limit int) ([]map[string]any, string, string, bool) {
	if limit <= 0 {
		limit = 4
	}
	operation, optionKind, guidance, ok := dynamicQuestionOptionKind(questionID)
	if !ok {
		return nil, "", "", false
	}
	if agentGateway == nil || agentGateway.LLMClient == nil {
		logInteractiveStructuredFallback(operation, "llm_unavailable", query, nil)
		return nil, "", "", false
	}

	avoidLine := ""
	if filteredExcluded := normalizeDynamicOptionValues(excluded); len(filteredExcluded) > 0 {
		avoidLine = fmt.Sprintf("Avoid repeating any of these already shown options: %s.", strings.Join(filteredExcluded, ", "))
	}

	requestCtx, structuredClient, cancel := interactiveStructuredRequest(ctx, agentGateway.LLMClient)
	defer cancel()
	prompt := fmt.Sprintf(`You are WisDev, ScholarLM's adaptive research-planning assistant.
Question slot: %s
Query: %q
Detected domain: %q
Selected subtopics: %s
Selected study types: %s
Generate exactly %d %s.
%s
Each option must include a concise label and a one-sentence description explaining why that option is useful for this exact research task. Do not hard-code domain playbooks; infer the best labels from the query and selected context.
%s
%s`, questionID, query, domain, strings.Join(selectedSubtopics, ", "), strings.Join(selectedStudyTypes, ", "), limit, optionKind, guidance, avoidLine, structuredOutputSchemaInstruction)
	schema := fmt.Sprintf(`{"type":"object","properties":{"options":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"}},"required":["label","description"]},"maxItems":%d},"explanation":{"type":"string"}},"required":["options","explanation"]}`, limit)
	resp, err := structuredClient.StructuredOutput(requestCtx, llm.ApplyStructuredPolicy(&llmv1.StructuredRequest{
		Prompt:     prompt,
		Model:      llm.ResolveLightModel(),
		JsonSchema: schema,
	}, llm.ResolveRequestPolicy(llm.RequestPolicyInput{
		RequestedTier: "light",
		Structured:    true,
		HighValue:     false,
	})))
	if err != nil {
		logInteractiveStructuredFallback(operation, "llm_request_failed", query, err)
		return nil, "", "", false
	}

	var parsed struct {
		Options []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		Explanation string `json:"explanation"`
	}
	if json.Unmarshal([]byte(resp.JsonResult), &parsed) != nil {
		logInteractiveStructuredFallback(operation, "llm_invalid_response", query, fmt.Errorf("structured output JSON decode failed"))
		return nil, "structured_invalid_fallback", "Regenerated heuristic options because model structured output was invalid.", false
	}

	options := make([]map[string]any, 0, limit)
	seen := map[string]struct{}{}
	for _, item := range parsed.Options {
		label := strings.TrimSpace(item.Label)
		if dynamicQuestionOptionExcluded(label, excluded) {
			continue
		}
		key := normalizeQuestionOptionValue(label)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		description := strings.TrimSpace(item.Description)
		if description == "" {
			description = dynamicQuestionOptionDescription(questionID, label, query, domain)
		}
		options = append(options, normalizeQuestionOptionPayload(key, label, description, ""))
		if len(options) >= limit {
			break
		}
	}
	if len(options) == 0 {
		logInteractiveStructuredFallback(operation, "llm_empty_response", query, nil)
		return nil, "structured_invalid_fallback", "Regenerated heuristic options because model structured output had no usable options.", false
	}

	explanation := strings.TrimSpace(parsed.Explanation)
	if len(normalizeDynamicOptionValues(excluded)) > 0 && explanation != "" {
		explanation += " Regenerated to avoid repeating prior options."
	}
	return options, "llm_structured", explanation, true
}

func (s *wisdevServer) registerQuestioningRoutes(mux *http.ServeMux, agentGateway *wisdev.AgentGateway) {
	var questionRecommendationBrainMu sync.Mutex
	var dynamicOptionInflightMu sync.Mutex
	dynamicOptionInflight := map[string]*dynamicQuestionOptionsInflightCall{}

	buildDynamicQuestionOptions := func(ctx context.Context, session map[string]any, questionID string, previousOptions []string) ([]any, string, string) {
		if len(session) == 0 {
			return nil, "", ""
		}
		query := wisdev.ResolveSessionQueryText(
			wisdev.AsOptionalString(session["correctedQuery"]),
			wisdev.AsOptionalString(session["originalQuery"]),
		)
		domain := wisdev.AsOptionalString(session["detectedDomain"])
		selectedSubtopics := answeredAgentQuestionValues(session, "q4_subtopics")
		selectedStudyTypes := answeredAgentQuestionValues(session, "q5_study_types")
		switch strings.TrimSpace(questionID) {
		case "q4_subtopics":
			subtopics, _, _, source, explanation := buildSubtopicsResponseWithExclusions(ctx, agentGateway, query, domain, 8, previousOptions)
			options := make([]any, 0, len(subtopics))
			for _, subtopic := range subtopics {
				options = append(options, dynamicQuestionOptionPayload("q4_subtopics", subtopic, subtopic, query, domain))
			}
			return options, source, explanation
		case "q5_study_types":
			existingSubtopics := answeredAgentQuestionValues(session, "q4_subtopics")
			studyTypes, _, source, explanation := buildStudyTypesResponseWithExclusions(ctx, agentGateway, query, domain, existingSubtopics, 6, previousOptions)
			options := make([]any, 0, len(studyTypes))
			for _, studyType := range studyTypes {
				options = append(options, dynamicQuestionOptionPayload("q5_study_types", studyType, studyType, query, domain))
			}
			return options, source, explanation
		case "q6_exclusions":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q6_exclusions", query, domain, selectedSubtopics, selectedStudyTypes, previousOptions, 5); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			exclusions := avoidRepeatedDynamicOptions(defaultExclusionOptions(query, domain, selectedSubtopics, selectedStudyTypes), previousOptions, 5, func(selected []string) []string {
				return defaultExclusionOptions(query+" "+strings.Join(selected, " "), domain, selectedSubtopics, selectedStudyTypes)
			})
			options := make([]any, 0, len(exclusions))
			for _, exclusion := range exclusions {
				options = append(options, dynamicQuestionOptionPayload("q6_exclusions", normalizeQuestionOptionValue(exclusion), exclusion, query, domain))
			}
			return options, "heuristic", "WisDev refreshed exclusion options from the current query and domain."
		case "q7_evidence_quality":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q7_evidence_quality", query, domain, selectedSubtopics, selectedStudyTypes, previousOptions, 4); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			qualityBars := avoidRepeatedDynamicOptions(defaultEvidenceQualityOptions(query, domain, selectedSubtopics, selectedStudyTypes), previousOptions, 4, func(selected []string) []string {
				return defaultEvidenceQualityOptions(query+" "+strings.Join(selected, " "), domain, selectedSubtopics, selectedStudyTypes)
			})
			options := make([]any, 0, len(qualityBars))
			for _, qualityBar := range qualityBars {
				options = append(options, dynamicQuestionOptionPayload("q7_evidence_quality", normalizeQuestionOptionValue(qualityBar), qualityBar, query, domain))
			}
			return options, "heuristic", "WisDev refreshed evidence-quality options from the current query and domain."
		case "q8_output_focus":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q8_output_focus", query, domain, selectedSubtopics, selectedStudyTypes, previousOptions, 4); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			outputFocus := avoidRepeatedDynamicOptions(defaultOutputFocusOptions(query, domain, selectedSubtopics, selectedStudyTypes), previousOptions, 4, func(selected []string) []string {
				return defaultOutputFocusOptions(query+" "+strings.Join(selected, " "), domain, selectedSubtopics, selectedStudyTypes)
			})
			options := make([]any, 0, len(outputFocus))
			for _, focus := range outputFocus {
				options = append(options, dynamicQuestionOptionPayload("q8_output_focus", normalizeQuestionOptionValue(focus), focus, query, domain))
			}
			return options, "heuristic", "WisDev refreshed output-focus options from the current query and domain."
		default:
			return nil, "", ""
		}
	}

	buildFastQuestionOptions := func(ctx context.Context, session map[string]any, questionID string) ([]any, string, string) {
		if len(session) == 0 {
			return nil, "", ""
		}
		query := wisdev.ResolveSessionQueryText(
			wisdev.AsOptionalString(session["correctedQuery"]),
			wisdev.AsOptionalString(session["originalQuery"]),
		)
		domain := wisdev.AsOptionalString(session["detectedDomain"])
		selectedSubtopics := answeredAgentQuestionValues(session, "q4_subtopics")
		selectedStudyTypes := answeredAgentQuestionValues(session, "q5_study_types")
		switch strings.TrimSpace(questionID) {
		case "q4_subtopics":
			subtopics, _, _, source, explanation := buildSubtopicsResponseWithExclusions(ctx, agentGateway, query, domain, 8, nil)
			options := make([]any, 0, len(subtopics))
			for _, subtopic := range subtopics {
				options = append(options, dynamicQuestionOptionPayload("q4_subtopics", subtopic, subtopic, query, domain))
			}
			return options, source, explanation
		case "q5_study_types":
			existingSubtopics := answeredAgentQuestionValues(session, "q4_subtopics")
			studyTypes, _, source, explanation := buildStudyTypesResponseWithExclusions(ctx, agentGateway, query, domain, existingSubtopics, 6, nil)
			options := make([]any, 0, len(studyTypes))
			for _, studyType := range studyTypes {
				options = append(options, dynamicQuestionOptionPayload("q5_study_types", studyType, studyType, query, domain))
			}
			return options, source, explanation
		case "q6_exclusions":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q6_exclusions", query, domain, selectedSubtopics, selectedStudyTypes, nil, 5); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			exclusions := defaultExclusionOptions(query, domain, selectedSubtopics, selectedStudyTypes)
			options := make([]any, 0, len(exclusions))
			for _, exclusion := range trimStrings(exclusions, 5) {
				options = append(options, dynamicQuestionOptionPayload("q6_exclusions", normalizeQuestionOptionValue(exclusion), exclusion, query, domain))
			}
			return options, "heuristic", "WisDev served exclusion options from the current query and domain."
		case "q7_evidence_quality":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q7_evidence_quality", query, domain, selectedSubtopics, selectedStudyTypes, nil, 4); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			qualityBars := defaultEvidenceQualityOptions(query, domain, selectedSubtopics, selectedStudyTypes)
			options := make([]any, 0, len(qualityBars))
			for _, qualityBar := range trimStrings(qualityBars, 4) {
				options = append(options, dynamicQuestionOptionPayload("q7_evidence_quality", normalizeQuestionOptionValue(qualityBar), qualityBar, query, domain))
			}
			return options, "heuristic", "WisDev served evidence-quality options from the current query and domain."
		case "q8_output_focus":
			if llmOptions, source, explanation, ok := buildStructuredDynamicQuestionOptions(ctx, agentGateway, "q8_output_focus", query, domain, selectedSubtopics, selectedStudyTypes, nil, 4); ok {
				return dynamicQuestionOptionMapsAsAny(llmOptions), source, explanation
			}
			outputFocus := defaultOutputFocusOptions(query, domain, selectedSubtopics, selectedStudyTypes)
			options := make([]any, 0, len(outputFocus))
			for _, focus := range trimStrings(outputFocus, 4) {
				options = append(options, dynamicQuestionOptionPayload("q8_output_focus", normalizeQuestionOptionValue(focus), focus, query, domain))
			}
			return options, "heuristic", "WisDev served output-focus options from the current query and domain."
		default:
			return nil, "", ""
		}
	}

	buildDynamicQuestionOptionsOnce := func(ctx context.Context, sessionID string, session map[string]any, questionID string, previousOptions []string) ([]any, string, string) {
		if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(questionID) == "" || len(previousOptions) > 0 {
			return buildDynamicQuestionOptions(ctx, session, questionID, previousOptions)
		}
		key := dynamicQuestionOptionsSingleflightKey(sessionID, questionID, session)

		dynamicOptionInflightMu.Lock()
		if call, ok := dynamicOptionInflight[key]; ok {
			dynamicOptionInflightMu.Unlock()
			select {
			case <-call.done:
				return call.result.options, call.result.source, call.result.explanation
			case <-ctx.Done():
				return nil, "options_unavailable", "Timed out while waiting for dynamic option generation already in progress."
			}
		}
		call := &dynamicQuestionOptionsInflightCall{done: make(chan struct{})}
		dynamicOptionInflight[key] = call
		dynamicOptionInflightMu.Unlock()

		call.result.options, call.result.source, call.result.explanation = buildDynamicQuestionOptions(ctx, session, questionID, nil)

		dynamicOptionInflightMu.Lock()
		delete(dynamicOptionInflight, key)
		dynamicOptionInflightMu.Unlock()
		close(call.done)
		return call.result.options, call.result.source, call.result.explanation
	}

	patchDynamicQuestionOptions := func(session map[string]any, questionID string, options []any, source string, explanation string, contextSignature string, overwrite bool) bool {
		if len(session) == 0 || len(options) == 0 {
			return false
		}
		contextSignature = strings.TrimSpace(contextSignature)
		if contextSignature == "" {
			contextSignature = dynamicQuestionOptionContextSignature(questionID, session)
		}
		questions := sliceAnyMap(session["questions"])
		if len(questions) == 0 {
			return false
		}
		patched := false
		for i, question := range questions {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			if len(questionOptionValues(question["options"])) > 0 && !overwrite {
				break
			}
			questions[i]["options"] = options
			questions[i]["optionsSource"] = source
			questions[i]["optionsExplanation"] = explanation
			questions[i]["optionsContextKey"] = contextSignature
			patched = true
			break
		}
		if patched {
			session["questions"] = questions
			previousUpdatedAt := wisdev.IntValue64(session["updatedAt"])
			updatedAt := time.Now().UnixMilli()
			if updatedAt <= previousUpdatedAt {
				updatedAt = previousUpdatedAt + 1
			}
			session["updatedAt"] = updatedAt
		}
		return patched
	}

	persistDynamicQuestionOptions := func(sessionID, userID, questionID string, options []any, source string, explanation string, contextSignature string, overwrite bool) bool {
		if agentGateway == nil || agentGateway.StateStore == nil {
			return false
		}
		latest, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			return false
		}
		if !patchDynamicQuestionOptions(latest, questionID, options, source, explanation, contextSignature, overwrite) {
			return false
		}
		err = agentGateway.StateStore.PersistAgentSessionMutation(sessionID, userID, latest, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			SessionID: sessionID,
			UserID:    userID,
			StepID:    questionID,
			EventType: "agent_session_options_patch",
			Status:    "completed",
			Summary:   "Question options patched via dynamic generation.",
			Payload: map[string]any{
				"questionId":  questionID,
				"optionCount": len(options),
				"overwrite":   overwrite,
				"source":      source,
				"context":     contextSignature,
			},
		})
		if err != nil {
			slog.Warn("wisdev dynamic question options persist failed",
				"component", "api.wisdev",
				"operation", "persist_dynamic_options",
				"stage", "failed",
				"session_id", sessionID,
				"question_id", questionID,
				"error", err,
			)
			return false
		}
		return true
	}

	appendQuestionRouteJournalEntry := func(entry wisdev.RuntimeJournalEntry) {
		if agentGateway == nil || agentGateway.Journal == nil {
			return
		}
		if strings.TrimSpace(entry.EventID) == "" {
			entry.EventID = wisdev.NewTraceID()
		}
		if strings.TrimSpace(entry.TraceID) == "" {
			entry.TraceID = wisdev.NewTraceID()
		}
		if entry.CreatedAt == 0 {
			entry.CreatedAt = time.Now().UnixMilli()
		}
		if strings.TrimSpace(entry.Status) == "" {
			entry.Status = "completed"
		}
		agentGateway.Journal.Append(entry)
	}

	ensureQuestionRecommendationBrain := func() *wisdev.BrainCapabilities {
		if agentGateway == nil {
			return nil
		}
		if agentGateway.Brain != nil {
			return agentGateway.Brain
		}
		if agentGateway.LLMClient == nil {
			return nil
		}

		questionRecommendationBrainMu.Lock()
		defer questionRecommendationBrainMu.Unlock()

		if agentGateway.Brain == nil && agentGateway.LLMClient != nil {
			agentGateway.Brain = wisdev.NewBrainCapabilities(agentGateway.LLMClient)
			slog.Info("wisdev question recommendations brain initialised",
				"component", "api.wisdev",
				"operation", "question_recommendations",
				"stage", "brain_initialised",
			)
		}
		return agentGateway.Brain
	}

	logQuestionRecommendationFallback := func(r *http.Request, sessionID string, questionID string, stage string, attrs ...any) {
		base := []any{
			"component", "api.wisdev",
			"operation", "question_recommendations",
			"stage", stage,
			"path", r.URL.Path,
			"session_id", sessionID,
			"question_id", questionID,
			"fallback_source", "heuristic",
		}
		base = append(base, attrs...)
		slog.Warn("wisdev question recommendations fallback", base...)
	}

	describeQuestionRecommendationFallback := func(stage string) string {
		switch strings.TrimSpace(stage) {
		case "ai_request_failed":
			return "AI option ranking was unavailable, so WisDev used fallback recommendations from the current option set."
		case "ai_unavailable":
			return "AI option ranking is not configured for this request, so WisDev used fallback recommendations from the current option set."
		case "ai_empty_response":
			return "AI option ranking returned no usable matches, so WisDev used fallback recommendations from the current option set."
		case "options_unavailable":
			return "No dynamic options were available to rank for this question."
		default:
			return ""
		}
	}

	deriveQuestionOptionFallback := func(source string) (bool, string) {
		switch strings.TrimSpace(strings.ToLower(source)) {
		case "heuristic", "heuristic_fallback", "fallback":
			return true, "heuristic_fallback"
		case "ai_request_failed", "ai_unavailable", "ai_empty_response", "options_unavailable":
			normalized := strings.TrimSpace(strings.ToLower(source))
			return true, normalized
		default:
			return false, ""
		}
	}

	writeQuestionOptionResponse := func(w http.ResponseWriter, questionID string, options any, source string, explanation string) {
		fallbackTriggered, fallbackReason := deriveQuestionOptionFallback(source)
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"questionId":        questionID,
			"options":           options,
			"source":            source,
			"explanation":       explanation,
			"fallbackTriggered": fallbackTriggered,
			"fallbackReason":    fallbackReason,
		})
	}

	requireQuestioningStateStore := func(w http.ResponseWriter, r *http.Request, operation string) bool {
		reason := ""
		message := ""
		switch {
		case agentGateway == nil:
			reason = "agent_gateway_unavailable"
			message = "agent gateway is not initialized"
		case agentGateway.StateStore == nil:
			reason = "state_store_unavailable"
			message = "runtime state store unavailable"
		default:
			return true
		}

		logWisdevRouteError(r, "wisdev "+operation+" unavailable",
			"operation", operation,
			"reason", reason,
		)
		WriteError(w, http.StatusServiceUnavailable, ErrServiceUnavailable, message, map[string]any{
			"operation": operation,
			"reason":    reason,
		})
		return false
	}

	handleProcessAnswer := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID         string   `json:"sessionId"`
			QuestionID        string   `json:"questionId"`
			Values            []string `json:"values"`
			DisplayValues     []string `json:"displayValues"`
			Proceed           bool     `json:"proceed"`
			ExpectedUpdatedAt int64    `json:"expectedUpdatedAt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev process answer decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := validateRequiredString(req.QuestionID, "questionId", 80); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "questionId",
			})
			return
		}
		if err := validateStringSlice(req.Values, "values", maxAgentAnswerValues, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "values",
			})
			return
		}
		if err := validateStringSlice(req.DisplayValues, "displayValues", maxAgentAnswerValues, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "displayValues",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "process_answer") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev process answer load failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		questionID := strings.TrimSpace(req.QuestionID)
		normalizedValues, normalizedDisplayValues := normalizeAgentQuestionAnswerValues(session, questionID, req.Values, req.DisplayValues)
		idempotencyKey := makeAgentAnswerIdempotencyKey(
			req.SessionID,
			questionID,
			normalizedValues,
			normalizedDisplayValues,
			req.Proceed,
			req.ExpectedUpdatedAt,
		)
		if status, cached, ok := enforceIdempotency(r, agentGateway, idempotencyKey); ok {
			writeCachedWisdevEnvelopeResponse(w, status, cached)
			return
		}
		if req.ExpectedUpdatedAt > 0 &&
			wisdev.IntValue64(session["updatedAt"]) != req.ExpectedUpdatedAt &&
			(agentAnswerAlreadyApplied(session, questionID, normalizedValues, normalizedDisplayValues) ||
				agentAnswerValuesAlreadyApplied(session, questionID, normalizedValues)) {
			traceID := wisdev.NewTraceID()
			responseBody := buildAgentQuestioningEnvelopeBody(traceID, session, false)
			if body, err := json.Marshal(responseBody); err == nil {
				storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Trace-Id", traceID)
			_ = json.NewEncoder(w).Encode(responseBody)
			return
		}
		if !assertExpectedUpdatedAt(w, req.ExpectedUpdatedAt, session) {
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			logWisdevRouteError(r, "wisdev process answer rejected immutable session",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		pendingFollowUp := getPendingAgentFollowUpQuestion(session)
		isPendingFollowUpAnswer := len(pendingFollowUp) > 0 &&
			questionID == strings.TrimSpace(wisdev.AsOptionalString(pendingFollowUp["id"]))
		if isAgentQuestionRequired(session, questionID) && !hasNonEmptyAnswerValues(normalizedValues) {
			logWisdevRouteError(r, "wisdev process answer rejected empty required answer",
				"session_id", req.SessionID,
				"question_id", questionID,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "required question answer must include at least one value", map[string]any{
				"field":      "values",
				"sessionId":  req.SessionID,
				"questionId": questionID,
			})
			return
		}
		answers := mapAny(session["answers"])
		answers[questionID] = map[string]any{
			"questionId":    questionID,
			"values":        normalizedValues,
			"displayValues": normalizedDisplayValues,
			"answeredAt":    time.Now().UTC().Format(time.RFC3339),
		}
		session["answers"] = answers
		if questionID == "q1_domain" {
			replanAgentSessionForDomainAnswer(session)
		}
		if isPendingFollowUpAnswer {
			mirrorPendingAgentFollowUpAnswer(session, pendingFollowUp, normalizedValues, normalizedDisplayValues)
		}
		nextIndex := wisdev.IntValue(session["currentQuestionIndex"])
		if !isPendingFollowUpAnswer {
			nextIndex += 1
		}
		stopReason := wisdev.QuestionStopReasonNone
		status := string(wisdev.SessionQuestioning)
		if isPendingFollowUpAnswer {
			clearPendingAgentFollowUpQuestion(session)
		}
		if req.Proceed {
			stopReason = wisdev.QuestionStopReasonUserProceed
			status = "ready"
		} else if canonical := buildCanonicalAgentSession(session); canonical != nil {
			shouldStop, reason := wisdev.ShouldStopQuestioning(canonical)
			if shouldStop {
				stopReason = reason
				status = "ready"
			}
			nextQuestionID := strings.TrimSpace(wisdev.FindNextQuestionID(canonical))
			if nextQuestionID == "" {
				nextIndex = len(sliceStrings(session["questionSequence"]))
			} else {
				for index, questionID := range sliceStrings(session["questionSequence"]) {
					if questionID == nextQuestionID {
						nextIndex = index
						break
					}
				}
			}
		}
		if len(getPendingAgentFollowUpQuestion(session)) > 0 {
			session["questionStopReason"] = ""
			session["status"] = string(wisdev.SessionQuestioning)
		} else if status == "ready" {
			session["questionStopReason"] = string(stopReason)
			session["status"] = "ready"
		} else {
			session["questionStopReason"] = ""
			session["status"] = status
		}
		session["currentQuestionIndex"] = nextIndex
		bumpUpdatedAt(session)
		traceID := wisdev.NewTraceID()
		if err := agentGateway.StateStore.PersistAgentSessionMutation(req.SessionID, wisdev.AsOptionalString(session["userId"]), session, wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    req.QuestionID,
			EventType: "agent_session_answer",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Your answer was saved for the next search pass.",
			Payload:   cloneAnyMap(session),
			Metadata:  map[string]any{"proceed": req.Proceed},
		}); err != nil {
			logWisdevRouteError(r, "wisdev process answer persist failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to persist processed answer", map[string]any{
				"error":      err.Error(),
				"sessionId":  req.SessionID,
				"questionId": req.QuestionID,
			})
			return
		}
		if err := syncCanonicalSessionStore(agentGateway, session); err != nil {
			logWisdevRouteError(r, "wisdev process answer canonical sync failed",
				"session_id", req.SessionID,
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusInternalServerError, ErrWisdevFailed, "failed to sync canonical session", map[string]any{
				"error":     err.Error(),
				"sessionId": req.SessionID,
			})
			return
		}
		responseBody := buildAgentQuestioningEnvelopeBody(traceID, session, false)
		// Only cache the idempotent response when Marshal succeeds; a nil body
		// would cause subsequent replays to return an empty HTTP response.
		if body, err := json.Marshal(responseBody); err == nil {
			storeIdempotentResponse(agentGateway, r, idempotencyKey, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		_ = json.NewEncoder(w).Encode(responseBody)

		// Background pre-warm: generate dynamic options for the next dynamic
		// question(s) so that when the user arrives at q4/q5 the options are
		// already stored and the endpoint responds immediately without blocking
		// on an LLM call. Uses a detached context so it outlives the request.
		answeredID := strings.TrimSpace(req.QuestionID)
		if agentGateway != nil {
			sessionSnap := cloneAnyMap(session)
			sessionID := req.SessionID
			userID := wisdev.AsOptionalString(session["userId"])
			if answeredID == "q2_scope" || answeredID == "q3_timeframe" {
				// Pre-warm q4_subtopics then immediately chain into q5_study_types
				// in the same goroutine, so both questions are fully ready before
				// the user reaches them. q5 uses the q4 LLM output as subtopic
				// context so it gets contextually accurate study type options even
				// though the user hasn't answered q4 yet.
				q4HasOptions := func() bool {
					for _, q := range sliceAnyMap(sessionSnap["questions"]) {
						if wisdev.AsOptionalString(q["id"]) == "q4_subtopics" {
							return len(questionOptionValues(q["options"])) > 0
						}
					}
					return false
				}()
				if !q4HasOptions {
					go func() {
						// Budget covers two sequential LLM calls: q4 (~40s) + q5 (~40s)
						budget := 100 * time.Second
						if llm.IsColdStartWindow() {
							budget = 140 * time.Second
						}
						pwCtx, pwCancel := context.WithTimeout(context.Background(), budget)
						defer pwCancel()

						// Step 1: Generate q4 subtopic options via LLM.
						q4Opts, q4Src, q4Expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, sessionSnap, "q4_subtopics", nil)
						if len(q4Opts) > 0 {
							q4Context := dynamicQuestionOptionContextSignature("q4_subtopics", sessionSnap)
							if persistDynamicQuestionOptions(sessionID, userID, "q4_subtopics", q4Opts, q4Src, q4Expl, q4Context, false) {
								slog.Info("wisdev q4_subtopics options pre-warmed",
									"component", "api.wisdev",
									"operation", "prewarm_options",
									"stage", "completed",
									"session_id", sessionID,
									"triggered_by", answeredID,
									"option_count", len(q4Opts),
									"source", q4Src,
								)
							}

							// Step 2: Chain q5 study types immediately using q4 generated
							// options as subtopic context (better than waiting for user's q4 answer).
							q5AlreadyStored := func() bool {
								latest, err := agentGateway.StateStore.LoadAgentSession(sessionID)
								if err != nil {
									return false
								}
								for _, q := range sliceAnyMap(latest["questions"]) {
									if wisdev.AsOptionalString(q["id"]) == "q5_study_types" {
										return len(questionOptionValues(q["options"])) > 0
									}
								}
								return false
							}()
							if !q5AlreadyStored {
								q4SubtopicValues := make([]string, 0, len(q4Opts))
								for _, opt := range q4Opts {
									if om, ok := opt.(map[string]any); ok {
										if v := wisdev.AsOptionalString(om["value"]); v != "" {
											q4SubtopicValues = append(q4SubtopicValues, v)
										}
									}
								}
								q5SessionSnap := cloneAnyMap(sessionSnap)
								answers := mapAny(q5SessionSnap["answers"])
								if answers == nil {
									answers = map[string]any{}
								}
								answers["q4_subtopics"] = map[string]any{
									"questionId": "q4_subtopics",
									"values":     q4SubtopicValues,
								}
								q5SessionSnap["answers"] = answers
								q5Opts, q5Src, q5Expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, q5SessionSnap, "q5_study_types", nil)
								if len(q5Opts) > 0 {
									if q5Src == "" {
										q5Src = "heuristic"
									}
									q5Context := dynamicQuestionOptionContextSignature("q5_study_types", q5SessionSnap)
									if persistDynamicQuestionOptions(sessionID, userID, "q5_study_types", q5Opts, q5Src, q5Expl, q5Context, false) {
										slog.Info("wisdev q5_study_types options pre-warmed (chained from q4)",
											"component", "api.wisdev",
											"operation", "prewarm_options",
											"stage", "completed",
											"session_id", sessionID,
											"triggered_by", answeredID,
											"option_count", len(q5Opts),
											"source", q5Src,
										)
									}
								}
							}
						}
					}()
				}
			} else if answeredID == "q4_subtopics" {
				// Re-warm q5_study_types using the user's actual q4 answer selections.
				// This overrides any previously pre-warmed q5 options so they reflect
				// the user's real subtopic choices rather than the LLM pre-generated ones.
				go func() {
					budget := 50 * time.Second
					if llm.IsColdStartWindow() {
						budget = 70 * time.Second
					}
					pwCtx, pwCancel := context.WithTimeout(context.Background(), budget)
					defer pwCancel()
					opts, src, expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, sessionSnap, "q5_study_types", nil)
					if len(opts) > 0 {
						q5Context := dynamicQuestionOptionContextSignature("q5_study_types", sessionSnap)
						if persistDynamicQuestionOptions(sessionID, userID, "q5_study_types", opts, src, expl, q5Context, true) {
							slog.Info("wisdev q5_study_types options re-warmed from q4 selection",
								"component", "api.wisdev",
								"operation", "prewarm_options",
								"stage", "completed",
								"session_id", sessionID,
								"triggered_by", answeredID,
								"option_count", len(opts),
								"source", src,
							)
						}
					}
				}()
			} else if answeredID == "q5_study_types" {
				// Q7/Q8 depend on the selected subtopics and study types. Re-warm
				// both after q5 so the next cards do not show generic or stale
				// fallback options while the user is progressing through the flow.
				go func() {
					pwCtx, pwCancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer pwCancel()
					for _, nextQuestionID := range []string{"q7_evidence_quality", "q8_output_focus"} {
						if !agentSessionIncludesQuestion(sessionSnap, nextQuestionID) {
							slog.Info("wisdev context-sensitive options prewarm skipped",
								"component", "api.wisdev",
								"operation", "prewarm_options",
								"stage", "skipped",
								"session_id", sessionID,
								"triggered_by", answeredID,
								"question_id", nextQuestionID,
								"reason", "question_not_planned",
							)
							continue
						}
						opts, src, expl := buildDynamicQuestionOptionsOnce(pwCtx, sessionID, sessionSnap, nextQuestionID, nil)
						if len(opts) == 0 {
							continue
						}
						contextKey := dynamicQuestionOptionContextSignature(nextQuestionID, sessionSnap)
						if persistDynamicQuestionOptions(sessionID, userID, nextQuestionID, opts, src, expl, contextKey, true) {
							slog.Info("wisdev context-sensitive options re-warmed from q5 selection",
								"component", "api.wisdev",
								"operation", "prewarm_options",
								"stage", "completed",
								"session_id", sessionID,
								"triggered_by", answeredID,
								"question_id", nextQuestionID,
								"option_count", len(opts),
								"source", src,
							)
						}
					}
				}()
			}
		}
	}
	for _, path := range wisdevAnswerPaths {
		mux.HandleFunc(path, handleProcessAnswer)
	}

	handleNextQuestion := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethods": []string{http.MethodGet, http.MethodPost},
			})
			return
		}
		var req struct {
			SessionID           string `json:"sessionId"`
			UseAdaptiveOrdering bool   `json:"useAdaptiveOrdering"`
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				logWisdevRouteError(r, "wisdev next question decode failed", "error", err)
				WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
		if strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = strings.TrimSpace(r.URL.Query().Get("sessionId"))
		}
		if !requireQuestioningStateStore(w, r, "next_question") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev next question load failed",
				"session_id", req.SessionID,
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if err := ensureAgentSessionMutable(session); err != nil {
			logWisdevRouteError(r, "wisdev next question rejected immutable session",
				"session_id", req.SessionID,
				"error", err,
			)
			WriteError(w, http.StatusConflict, ErrInvalidParameters, err.Error(), map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		payload := buildAgentQuestionPayload(session, req.UseAdaptiveOrdering)
		traceID := wisdev.NewTraceID()
		appendQuestionRouteJournalEntry(wisdev.RuntimeJournalEntry{
			EventID:   wisdev.NewTraceID(),
			TraceID:   traceID,
			SessionID: req.SessionID,
			UserID:    wisdev.AsOptionalString(session["userId"]),
			StepID:    wisdev.AsOptionalString(payload["id"]),
			EventType: "agent_next_question",
			Path:      r.URL.Path,
			Status:    "completed",
			CreatedAt: time.Now().UnixMilli(),
			Summary:   "Next agent question requested.",
			Payload:   cloneAnyMap(payload),
			Metadata:  map[string]any{"adaptive": req.UseAdaptiveOrdering},
		})
		writeEnvelopeWithTraceID(w, traceID, "question", payload)
	}
	for _, path := range wisdevQuestionNextPaths {
		mux.HandleFunc(path, handleNextQuestion)
	}

	handleSessionPreliminarySearch := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
			UserID    string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev preliminary search decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if err := validateRequiredString(req.SessionID, "sessionId", 120); err != nil {
			logWisdevRouteError(r, "wisdev preliminary search validation failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"error", err,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "sessionId",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "preliminary_search") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(req.SessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev preliminary search load failed", "session_id", strings.TrimSpace(req.SessionID), "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		requestUserID, authErr := resolveAuthorizedUserID(r, strings.TrimSpace(req.UserID))
		if authErr != nil {
			logWisdevRouteError(r, "wisdev preliminary search authorization failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"request_user_id", strings.TrimSpace(req.UserID),
				"error", authErr,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, authErr.Error(), nil)
			return
		}
		ownerID := wisdev.AsOptionalString(session["userId"])
		if requestUserID != ownerID && requestUserID != "admin" && requestUserID != "internal-service" {
			logWisdevRouteError(r, "wisdev preliminary search owner mismatch",
				"session_id", strings.TrimSpace(req.SessionID),
				"request_user_id", requestUserID,
				"owner_id", ownerID,
			)
			WriteError(w, http.StatusForbidden, ErrUnauthorized, "access denied to resource", nil)
			return
		}

		payload := buildAgentSessionPreliminarySearchPayload(r.Context(), agentGateway.SearchRegistry, session)
		writeEnvelope(w, "preliminarySearch", payload)
	}
	for _, path := range wisdevSessionPreliminarySearchPaths {
		mux.HandleFunc(path, handleSessionPreliminarySearch)
	}

	handleQuestionOptions := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
		if sessionID == "" {
			logWisdevRouteError(r, "wisdev question options validation failed", "reason", "missing_session_id")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId is required", map[string]any{
				"field": "sessionId",
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_options") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev question options load failed", "session_id", sessionID, "question_id", questionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		if questionID == "" {
			writeQuestionOptionResponse(w, "", []any{}, "", "")
			return
		}
		for _, question := range sliceAnyMap(session["questions"]) {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			query := wisdev.ResolveSessionQueryText(
				wisdev.AsOptionalString(session["correctedQuery"]),
				wisdev.AsOptionalString(session["originalQuery"]),
			)
			domain := wisdev.AsOptionalString(session["detectedDomain"])
			options := questionOptionPayloads(question["options"])
			optionsStale := dynamicQuestionOptionsNeedRefresh(session, question, questionID)
			if !optionsStale && len(options) > 0 {
				var descriptionsAdded bool
				options, descriptionsAdded = dynamicQuestionOptionsWithDescriptions(questionID, query, domain, options)
				if descriptionsAdded {
					contextKey := strings.TrimSpace(wisdev.AsOptionalString(question["optionsContextKey"]))
					if contextKey == "" {
						contextKey = dynamicQuestionOptionContextSignature(questionID, session)
					}
					enrichedOptions := dynamicQuestionOptionMapsAsAny(options)
					if patchDynamicQuestionOptions(session, questionID, enrichedOptions, wisdev.AsOptionalString(question["optionsSource"]), wisdev.AsOptionalString(question["optionsExplanation"]), contextKey, true) {
						persistDynamicQuestionOptions(sessionID, wisdev.AsOptionalString(session["userId"]), questionID, enrichedOptions, wisdev.AsOptionalString(question["optionsSource"]), wisdev.AsOptionalString(question["optionsExplanation"]), contextKey, true)
					}
				}
			}
			// Read stored provenance so pre-seeded LLM options are correctly
			// identified as AI-generated on subsequent fetches (BUG-1 fix).
			source := wisdev.AsOptionalString(question["optionsSource"])
			if source == "" {
				source = "stored"
			}
			explanation := wisdev.AsOptionalString(question["optionsExplanation"])

			// GET must stay inside the Rust gateway's interactive proxy budget:
			// first reads attempt structured AI generation on that short budget,
			// then persist deterministic Go fallback options when the model is unavailable.
			if (len(options) == 0 || optionsStale) && agentGateway != nil {
				genCtx, genCancel := context.WithTimeout(r.Context(), 2*time.Second)
				generatedOptions, generatedSource, generatedExplanation := buildFastQuestionOptions(genCtx, session, questionID)
				genCancel()
				if generatedSource != "" {
					source = generatedSource
				}
				if generatedExplanation != "" {
					explanation = generatedExplanation
				}
				contextKey := dynamicQuestionOptionContextSignature(questionID, session)
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, source, explanation, contextKey, optionsStale) {
					persistDynamicQuestionOptions(sessionID, wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, source, explanation, contextKey, optionsStale)
				}
				options = questionOptionPayloads(generatedOptions)
			}

			// The fast path above runs the LLM on a ~2s budget, so it usually
			// persists static heuristic options — and once stored with a fresh
			// context key they are never considered stale, locking the question
			// to non-dynamic suggestions. Upgrade heuristic options to real
			// LLM-generated ones in the background (detached context, same
			// pattern as the answer-triggered prewarm); the frontend re-polls
			// fallback-sourced options and swaps the AI set in when it lands.
			if fallbackTriggered, _ := deriveQuestionOptionFallback(source); fallbackTriggered &&
				len(options) > 0 && isDynamicQuestionOptionID(questionID) &&
				agentGateway != nil && agentGateway.LLMClient != nil {
				upgradeSession := cloneAnyMap(session)
				upgradeUserID := wisdev.AsOptionalString(session["userId"])
				go func() {
					budget := 50 * time.Second
					if llm.IsColdStartWindow() {
						budget = 70 * time.Second
					}
					upgradeCtx, upgradeCancel := context.WithTimeout(context.Background(), budget)
					defer upgradeCancel()
					upgradedOptions, upgradedSource, upgradedExplanation := buildDynamicQuestionOptionsOnce(upgradeCtx, sessionID, upgradeSession, questionID, nil)
					if len(upgradedOptions) == 0 || !isAIQuestionOptionSource(upgradedSource) {
						return
					}
					contextKey := dynamicQuestionOptionContextSignature(questionID, upgradeSession)
					if persistDynamicQuestionOptions(sessionID, upgradeUserID, questionID, upgradedOptions, upgradedSource, upgradedExplanation, contextKey, true) {
						slog.Info("wisdev dynamic question options upgraded to AI",
							"component", "api.wisdev",
							"operation", "upgrade_options",
							"stage", "completed",
							"session_id", sessionID,
							"question_id", questionID,
							"option_count", len(upgradedOptions),
							"source", upgradedSource,
						)
					}
				}()
			}

			writeQuestionOptionResponse(w, questionID, options, source, explanation)
			return
		}
		writeQuestionOptionResponse(w, questionID, []any{}, "", "")
	}
	for _, path := range wisdevQuestionOptionsPaths {
		mux.HandleFunc(path, handleQuestionOptions)
	}

	handleQuestionRecommendations := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodGet,
			})
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
		questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
		if sessionID == "" || questionID == "" {
			logWisdevRouteError(r, "wisdev question recommendations validation failed",
				"session_id", sessionID,
				"question_id", questionID,
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
				"fields": []string{"sessionId", "questionId"},
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_recommendations") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(sessionID)
		if err != nil {
			logWisdevRouteError(r, "wisdev question recommendations load failed", "session_id", sessionID, "question_id", questionID, "error", err)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": sessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		answers := mapAny(session["answers"])
		if answer, ok := answers[questionID].(map[string]any); ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"questionId":        questionID,
				"values":            sliceStrings(answer["values"]),
				"explanation":       "",
				"source":            "session",
				"fallbackTriggered": false,
				"fallbackReason":    "",
			})
			return
		}
		recommended := []string{}
		explanation := ""
		source := "heuristic"
		fallbackTriggered := false
		fallbackReason := ""
		questions := sliceAnyMap(session["questions"])
		findRecommendationQuestion := func() (map[string]any, int) {
			if pending := getPendingAgentFollowUpQuestion(session); len(pending) > 0 &&
				wisdev.AsOptionalString(pending["id"]) == questionID {
				return pending, -1
			}
			for i, question := range questions {
				if wisdev.AsOptionalString(question["id"]) == questionID {
					return question, i
				}
			}
			return nil, -1
		}

		question, questionIndex := findRecommendationQuestion()
		if len(question) > 0 {
			optionsStale := dynamicQuestionOptionsNeedRefresh(session, question, questionID)
			if questionIndex >= 0 && (len(questionOptionValues(question["options"])) == 0 || optionsStale) && agentGateway != nil {
				genCtx, genCancel := context.WithTimeout(r.Context(), 12*time.Second)
				generatedOptions, generatedSource, generatedExplanation := buildDynamicQuestionOptionsOnce(genCtx, sessionID, session, questionID, nil)
				genCancel()
				contextKey := dynamicQuestionOptionContextSignature(questionID, session)
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, generatedSource, generatedExplanation, contextKey, optionsStale) {
					persistDynamicQuestionOptions(sessionID, wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, generatedSource, generatedExplanation, contextKey, optionsStale)
					questions = sliceAnyMap(session["questions"])
					question = questions[questionIndex]
				}
			}
			allOptionValues := questionOptionValues(question["options"])
			isMultiSelect, _ := question["isMultiSelect"].(bool)
			limit := 1
			if isMultiSelect {
				limit = 3
			}
			if len(allOptionValues) == 0 {
				fallbackTriggered = true
				fallbackReason = "options_unavailable"
				explanation = describeQuestionRecommendationFallback("options_unavailable")
				logQuestionRecommendationFallback(r, sessionID, questionID, "options_unavailable")
			}

			// AI-first path: use Brain.SuggestQuestionValues when available.
			// The LLM picks the most relevant options for the query; we fall
			// back to the heuristic slice on any error.
			// Keep this bounded, but give the sidecar enough room for healthy
			// Gemini structured responses during local warm-up and contention.
			brain := ensureQuestionRecommendationBrain()
			if brain != nil && len(allOptionValues) > 0 {
				query := wisdev.ResolveSessionQueryText(
					wisdev.AsOptionalString(session["correctedQuery"]),
					wisdev.AsOptionalString(session["originalQuery"]),
				)
				questionLabel := wisdev.AsOptionalString(question["text"])
				if questionLabel == "" {
					questionLabel = wisdev.AsOptionalString(question["question"])
				}
				if questionLabel == "" {
					questionLabel = questionID
				}
				recCtx, recCancel := context.WithTimeout(r.Context(), wisdevQuestionRecommendationTimeout)
				aiValues, aiExplanation, aiErr := brain.SuggestQuestionValues(
					recCtx, query, questionID, questionLabel, allOptionValues, limit, "",
				)
				recCancel()
				if aiErr == nil && len(aiValues) > 0 {
					recommended = aiValues
					explanation = aiExplanation
					source = "ai"
				} else {
					fallbackStage := "ai_empty_response"
					if aiErr != nil {
						fallbackStage = "ai_request_failed"
						logQuestionRecommendationFallback(r, sessionID, questionID, fallbackStage,
							"error", aiErr.Error(),
							"option_count", len(allOptionValues),
						)
					} else {
						logQuestionRecommendationFallback(r, sessionID, questionID, fallbackStage,
							"option_count", len(allOptionValues),
						)
					}
					fallbackTriggered = true
					fallbackReason = fallbackStage
					// Heuristic fallback: return the first N options.
					recommended = allOptionValues
					if len(recommended) > limit {
						recommended = recommended[:limit]
					}
					if explanation == "" {
						explanation = describeQuestionRecommendationFallback(fallbackStage)
					}
				}
			} else if len(allOptionValues) > 0 {
				fallbackTriggered = true
				fallbackReason = "ai_unavailable"
				logQuestionRecommendationFallback(r, sessionID, questionID, "ai_unavailable",
					"brain_available", agentGateway != nil && agentGateway.Brain != nil,
					"llm_client_available", agentGateway != nil && agentGateway.LLMClient != nil,
					"option_count", len(allOptionValues),
				)
				// Heuristic fallback: return the first N options.
				recommended = allOptionValues
				if len(recommended) > limit {
					recommended = recommended[:limit]
				}
				if explanation == "" {
					explanation = describeQuestionRecommendationFallback("ai_unavailable")
				}
			}
		}
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"questionId":        questionID,
			"values":            recommended,
			"explanation":       explanation,
			"source":            source,
			"fallbackTriggered": fallbackTriggered,
			"fallbackReason":    fallbackReason,
		})
	}
	for _, path := range wisdevQuestionRecommendationsPaths {
		mux.HandleFunc(path, handleQuestionRecommendations)
	}

	handleQuestionRegenerate := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			SessionID       string   `json:"sessionId"`
			QuestionID      string   `json:"questionId"`
			PreviousOptions []string `json:"previousOptions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev question regenerate decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "failed to parse request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.QuestionID) == "" {
			logWisdevRouteError(r, "wisdev question regenerate validation failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"question_id", strings.TrimSpace(req.QuestionID),
			)
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "sessionId and questionId are required", map[string]any{
				"fields": []string{"sessionId", "questionId"},
			})
			return
		}
		if !requireQuestioningStateStore(w, r, "question_regenerate") {
			return
		}
		session, err := agentGateway.StateStore.LoadAgentSession(strings.TrimSpace(req.SessionID))
		if err != nil {
			logWisdevRouteError(r, "wisdev question regenerate load failed",
				"session_id", strings.TrimSpace(req.SessionID),
				"question_id", strings.TrimSpace(req.QuestionID),
				"error", err,
			)
			WriteError(w, http.StatusNotFound, ErrNotFound, "agent session not found", map[string]any{
				"sessionId": req.SessionID,
			})
			return
		}
		if !requireOwnerAccess(w, r, wisdev.AsOptionalString(session["userId"])) {
			return
		}
		questionID := strings.TrimSpace(req.QuestionID)
		if err := validateStringSlice(req.PreviousOptions, "previousOptions", 12, 160); err != nil {
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, err.Error(), map[string]any{
				"field": "previousOptions",
			})
			return
		}

		// Generate fresh dynamic options with a per-call timeout so the user
		// actually gets different options when they click "Regenerate".
		var options []map[string]any
		source := "stored"
		explanation := ""
		existingOptionValues := []string{}
		for _, question := range sliceAnyMap(session["questions"]) {
			if wisdev.AsOptionalString(question["id"]) != questionID {
				continue
			}
			existingOptionValues = questionOptionValues(question["options"])
			break
		}
		previousOptions := uniqueStrings(append(existingOptionValues, req.PreviousOptions...))
		if agentGateway != nil {
			// Explicit cancel (not defer) so the context is released immediately
			// after the LLM call returns, not when the entire handler returns.
			genCtx, genCancel := context.WithTimeout(r.Context(), 12*time.Second)
			generatedOptions, generatedSource, generatedExplanation := buildDynamicQuestionOptions(genCtx, session, questionID, previousOptions)
			genCancel()
			if generatedSource != "" {
				source = generatedSource
			}
			if generatedExplanation != "" {
				explanation = generatedExplanation
			}
			if len(generatedOptions) > 0 {
				options = questionOptionPayloads(generatedOptions)
				contextKey := dynamicQuestionOptionContextSignature(questionID, session)
				if patchDynamicQuestionOptions(session, questionID, generatedOptions, source, explanation, contextKey, true) {
					persistDynamicQuestionOptions(strings.TrimSpace(req.SessionID), wisdev.AsOptionalString(session["userId"]), questionID, generatedOptions, source, explanation, contextKey, true)
				}
			}
		}

		// Fall back to stored options if LLM was unavailable or returned nothing.
		if len(options) == 0 {
			for _, question := range sliceAnyMap(session["questions"]) {
				if wisdev.AsOptionalString(question["id"]) == questionID {
					storedOptions := questionOptionPayloads(question["options"])
					query := wisdev.ResolveSessionQueryText(
						wisdev.AsOptionalString(session["correctedQuery"]),
						wisdev.AsOptionalString(session["originalQuery"]),
					)
					domain := wisdev.AsOptionalString(session["detectedDomain"])
					var descriptionsAdded bool
					storedOptions, descriptionsAdded = dynamicQuestionOptionsWithDescriptions(questionID, query, domain, storedOptions)
					options = storedOptions
					source = wisdev.AsOptionalString(question["optionsSource"])
					if source == "" {
						source = "stored"
					}
					explanation = wisdev.AsOptionalString(question["optionsExplanation"])
					if descriptionsAdded {
						contextKey := strings.TrimSpace(wisdev.AsOptionalString(question["optionsContextKey"]))
						if contextKey == "" {
							contextKey = dynamicQuestionOptionContextSignature(questionID, session)
						}
						enrichedOptions := dynamicQuestionOptionMapsAsAny(storedOptions)
						if patchDynamicQuestionOptions(session, questionID, enrichedOptions, source, explanation, contextKey, true) {
							persistDynamicQuestionOptions(strings.TrimSpace(req.SessionID), wisdev.AsOptionalString(session["userId"]), questionID, enrichedOptions, source, explanation, contextKey, true)
						}
					}
					break
				}
			}
		}

		writeQuestionOptionResponse(w, req.QuestionID, options, source, explanation)
	}
	for _, path := range wisdevQuestionRegeneratePaths {
		mux.HandleFunc(path, handleQuestionRegenerate)
	}

	mux.HandleFunc("/wisdev/analyze-query", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, ErrBadRequest, "method not allowed", map[string]any{
				"allowedMethod": http.MethodPost,
			})
			return
		}
		var req struct {
			Query   string `json:"query"`
			TraceID string `json:"traceId"`
			// UserID is accepted for forward-compatibility with the frontend transport
			// but is not used here — the canonical user identity is resolved from the
			// authenticated JWT context via GetUserID(r), not from the request body.
			UserID string `json:"userId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logWisdevRouteError(r, "wisdev analyze query decode failed", "error", err)
			WriteError(w, http.StatusBadRequest, ErrBadRequest, "invalid request body", map[string]any{
				"error": err.Error(),
			})
			return
		}
		traceID := resolveWisdevRouteTraceID(r, req.TraceID)
		query := wisdev.ResolveSessionQueryText(req.Query, "")
		if query == "" {
			logWisdevRouteError(r, "wisdev analyze query validation failed", "trace_id", traceID, "reason", "missing_query")
			WriteError(w, http.StatusBadRequest, ErrInvalidParameters, "query is required", map[string]any{
				"field": "query",
			})
			return
		}

		logWisdevRouteLifecycle(r, "wisdev_analyze_query", "request_received", query,
			"trace_id", traceID,
			"result", "accepted",
		)
		// Wrap the request context with a longer deadline so Gemini 2.5 thinking
		// has room to complete without tripping the heuristic fallback on healthy
		// requests. buildAnalyzeQueryPayloadWithAI
		// runs the actual LLM call in a goroutine and selects on ctx.Done(), so
		// this deadline is respected even when the Go oauth2 library's Token()
		// refresh is blocking (Token() has no context parameter). The frontend
		// keeps a slightly larger fast-path budget so network and middleware
		// overhead do not force unnecessary fallbacks. The background goroutine
		// continues on its own bounded timeout, so no goroutine leak occurs if the
		// handler fires first.
		analyzeCtx, analyzeCancel := context.WithTimeout(r.Context(), wisdevAnalyzeQueryBudget())
		defer analyzeCancel()
		payload := buildAnalyzeQueryPayloadWithAI(analyzeCtx, agentGateway, query, traceID)
		entities, _ := payload["entities"].([]string)
		researchQuestions, _ := payload["research_questions"].([]string)
		entityCount := len(entities)
		researchQuestionCount := len(researchQuestions)

		// Expose whether the response is AI-derived or a heuristic so the
		// frontend can log the true analysis source without parsing the body.
		analysisSource := "ai"
		fallbackReason, _ := payload["fallbackReason"].(string)
		if ft, _ := payload["fallbackTriggered"].(bool); ft {
			analysisSource = "heuristic"
		}

		logWisdevRouteLifecycle(r, "wisdev_analyze_query", "response_ready", query,
			"trace_id", traceID,
			"entity_count", entityCount,
			"research_question_count", researchQuestionCount,
			"analysis_source", analysisSource,
			"fallback_reason", fallbackReason,
			"fallback_detail", payload["fallbackDetail"],
			"result", "success",
		)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Trace-Id", traceID)
		w.Header().Set("X-Analysis-Source", analysisSource)
		if fallbackReason != "" {
			w.Header().Set("X-Fallback-Reason", fallbackReason)
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func buildAgentSessionPreliminarySearchPayload(ctx context.Context, registry *internalsearch.ProviderRegistry, session map[string]any) map[string]any {
	payload := map[string]any{
		"totalCount":  0,
		"perSubtopic": map[string]int{},
	}
	if registry == nil || session == nil {
		return payload
	}

	query := wisdev.ResolveSessionQueryText(
		wisdev.AsOptionalString(session["correctedQuery"]),
		wisdev.AsOptionalString(session["originalQuery"]),
	)
	if query == "" {
		return payload
	}

	subtopicsQuestion := map[string]any{}
	for _, question := range sliceAnyMap(session["questions"]) {
		if strings.EqualFold(strings.TrimSpace(wisdev.AsOptionalString(question["type"])), "subtopics") {
			subtopicsQuestion = question
			break
		}
	}

	subtopicOptions := sliceAnyMap(subtopicsQuestion["options"])
	if len(subtopicOptions) > 5 {
		subtopicOptions = subtopicOptions[:5]
	}

	perSubtopic := make(map[string]int, len(subtopicOptions))
	for _, option := range subtopicOptions {
		key := strings.TrimSpace(wisdev.AsOptionalString(option["value"]))
		if key != "" {
			perSubtopic[key] = 0
		}
	}

	type outcome struct {
		key    string
		count  int
		isMain bool
	}

	results := make(chan outcome, len(subtopicOptions)+1)
	var wg sync.WaitGroup
	runSearch := func(key string, searchQuery string, limit int, isMain bool) {
		defer wg.Done()
		if strings.TrimSpace(searchQuery) == "" {
			results <- outcome{key: key, count: 0, isMain: isMain}
			return
		}
		result := internalsearch.ParallelSearch(ctx, registry, searchQuery, internalsearch.SearchOpts{
			Limit:       limit,
			QualitySort: true,
		})
		results <- outcome{key: key, count: len(result.Papers), isMain: isMain}
	}

	wg.Add(1)
	go runSearch("", query, 30, true)

	for _, option := range subtopicOptions {
		key := strings.TrimSpace(wisdev.AsOptionalString(option["value"]))
		label := strings.TrimSpace(wisdev.AsOptionalString(option["label"]))
		if key == "" {
			key = strings.TrimSpace(wisdev.AsOptionalString(option["id"]))
		}
		if label == "" {
			label = key
		}
		if key == "" || label == "" {
			continue
		}

		// Build the search query for this subtopic, but strip structural section
		// header labels ("Background", "Methods", "Overview", etc.) that produce
		// near-zero results when appended to the base query — the same guard
		// applied in composeTopicTreePathQuery.
		searchQuery := query
		if !isStructuralTopicLabel(label) {
			searchQuery = strings.TrimSpace(query + " " + label)
		}

		wg.Add(1)
		go runSearch(key, searchQuery, 10, false)
	}

	wg.Wait()
	close(results)

	totalCount := 0
	for item := range results {
		if item.isMain {
			totalCount = item.count
			continue
		}
		if item.key != "" {
			perSubtopic[item.key] = item.count
		}
	}

	payload["totalCount"] = totalCount
	payload["perSubtopic"] = perSubtopic
	return payload
}
