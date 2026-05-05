package api

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ClassifyQueryField(query string) (string, float64) {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return "", 0
	}

	fieldSignals := map[string][]string{
		"medicine": {
			"clinical", "therapy", "therapeutic", "disease", "patient", "hospital",
			"health", "medical", "medicine", "oncology", "cancer", "diagnosis",
			"drug", "treatment", "epidemiology", "surgery", "biomarker",
		},
		"biology": {
			"biology", "biological", "genome", "genomics", "protein", "cell",
			"molecular", "microbiome", "species", "evolution", "ecology",
			"metabolism", "enzyme", "neuron", "neural tissue",
		},
		"psychology": {
			"psychology", "psychological", "cognition", "cognitive", "behavior",
			"behaviour", "emotion", "mental", "depression", "anxiety", "learning",
			"attention", "memory", "therapy outcome", "wellbeing",
		},
		"computerscience": {
			"algorithm", "algorithms", "reinforcement learning", "rlhf", "rlaif",
			"machine learning", "deep learning", "large language model", "llm",
			"neural network", "artificial intelligence", "mixture of experts",
			"transformer", "model", "benchmark", "optimization", "retrieval",
			"distributed systems", "compiler", "database", "software",
		},
		"physics": {
			"physics", "quantum", "particle", "relativity", "cosmology", "astrophysics",
			"gravitational", "thermodynamics", "optics", "photon", "superconduct",
			"plasma", "mechanics",
		},
	}

	bestField := "computerscience"
	bestScore := 0
	secondScore := 0

	for fieldID, signals := range fieldSignals {
		score := 0
		for _, signal := range signals {
			if strings.Contains(normalized, signal) {
				score++
			}
		}
		if score > bestScore {
			secondScore = bestScore
			bestScore = score
			bestField = fieldID
		} else if score > secondScore {
			secondScore = score
		}
	}

	if bestScore == 0 {
		return bestField, 0.55
	}

	confidence := 0.6 + (0.08 * float64(bestScore))
	if bestScore-secondScore >= 2 {
		confidence += 0.08
	}
	if confidence > 0.95 {
		confidence = 0.95
	}

	return bestField, confidence
}

func BuildQueryIntroductionMarkdown(query string, papers []queryIntroductionPaper, providersUsed []string) string {
	fieldLabel := strings.TrimSpace(query)
	if fieldLabel == "" {
		fieldLabel = "Research topic"
	}

	meta := BuildQueryIntroductionMeta(fieldLabel, papers, providersUsed)

	themeBullets := buildMarkdownBullets(meta.CoreThemes)
	if len(themeBullets) == 0 {
		themeBullets = []string{"- The evidence is still too sparse to separate stable sub-areas from one-off studies."}
	}

	questionBullets := buildMarkdownBullets(meta.OpenQuestions)
	if len(questionBullets) == 0 {
		questionBullets = []string{"- Which assumptions drive the strongest disagreement across current evidence?"}
	}

	directionLines := make([]string, 0, len(meta.ResearchDirections))
	for _, direction := range meta.ResearchDirections {
		directionLines = append(
			directionLines,
			fmt.Sprintf(
				"- **%s:** %s Search query: `%s`",
				direction.Title,
				direction.Rationale,
				sanitizeMarkdownInlineCode(direction.Query),
			),
		)
	}
	if len(directionLines) == 0 {
		directionLines = append(directionLines, "- **Broaden evidence framing:** Compare methods, populations, and outcomes with a targeted follow-up. Search query: `field-level replication and robustness`")
	}

	providerSentence := "This brief is grounded in indexed literature metadata rather than full-text synthesis."
	if len(providersUsed) > 0 {
		providerSentence = fmt.Sprintf(
			"This brief draws on %s; high-stakes claims should still be checked against the primary papers.",
			providersSentence(buildProviderSet(papers, providersUsed)),
		)
	}

	return fmt.Sprintf(
		"## What this field studies\n\n%s\n\n## Why it matters\n\n%s\n\n## Major themes in the evidence base\n\n%s\n\n## Open gaps and contested claims\n\n%s\n\n## Useful next research directions\n\n%s",
		meta.Overview,
		deriveWhyItMatters(meta),
		strings.Join(themeBullets, "\n"),
		strings.Join(questionBullets, "\n"),
		strings.Join(directionLines, "\n"),
	) + "\n\n" + providerSentence
}

func BuildQueryIntroductionMeta(
	query string,
	papers []queryIntroductionPaper,
	providersUsed []string,
) queryIntroductionMeta {
	fieldLabel := strings.TrimSpace(query)
	if fieldLabel == "" {
		fieldLabel = "Research Topic"
	}
	fieldLabel = titleCasePreservingAcronyms(fieldLabel)
	fieldFamily, fieldConfidence := ClassifyQueryField(fieldLabel)

	providerSet := buildProviderSet(papers, providersUsed)
	yearMin := -1
	yearMax := -1

	coreThemes := collectCoreThemes(papers)
	researchDirections := make([]queryIntroductionResearchDirection, 0, 4)

	for _, paper := range papers {
		for _, provider := range paper.SourceApis {
			provider = strings.TrimSpace(provider)
			if provider != "" {
				providerSet[provider] = struct{}{}
			}
		}
		if paper.Year > 0 {
			if yearMin < 0 || paper.Year < yearMin {
				yearMin = paper.Year
			}
			if paper.Year > yearMax {
				yearMax = paper.Year
			}
		}
	}

	if len(coreThemes) == 0 {
		coreThemes = []string{
			"Method diversity across studies",
			"Evidence quality and reporting patterns",
			"Comparative claims across providers",
		}
	}

	themeSummary := joinHumanList(coreThemes[:minInt(len(coreThemes), 2)])
	if themeSummary == "" {
		themeSummary = "method diversity across studies, evidence quality, and comparative claims"
	}
	overview := joinOverviewParts(
		buildTopicPrimer(fieldLabel),
		buildOverviewSentence(fieldLabel, fieldFamily, fieldConfidence, len(papers), providersSentence(providerSet), themeSummary),
	)
	if len(coreThemes) > 2 {
		overview = overview + fmt.Sprintf(" Together, these papers point most strongly to %s.", joinHumanList(coreThemes[:minInt(len(coreThemes), 3)]))
	}

	openQuestions := deriveOpenQuestions(fieldLabel, fieldFamily, yearMin, yearMax, coreThemes)

	researchDirections = append(researchDirections,
		buildMethodDirection(fieldLabel, fieldFamily, coreThemes),
		buildRobustnessDirection(fieldLabel, fieldFamily, coreThemes),
		buildComparisonDirection(fieldLabel, fieldFamily, coreThemes),
		buildApplicationDirection(fieldLabel, fieldFamily, coreThemes),
		buildGapDirection(fieldLabel, fieldFamily, coreThemes),
	)

	researchDirections = dedupeDirectionStructs(researchDirections)

	if len(openQuestions) == 0 {
		openQuestions = append(openQuestions, fmt.Sprintf("Which open questions in %s are still under-tested across settings?", fieldLabel))
	}

	return queryIntroductionMeta{
		FieldLabel:         fieldLabel,
		Overview:           overview,
		CoreThemes:         coreThemes[:minInt(len(coreThemes), 5)],
		OpenQuestions:      openQuestions[:minInt(len(openQuestions), 4)],
		ResearchDirections: researchDirections[:minInt(len(researchDirections), 4)],
		Limitations: []string{
			"Introductory summaries are derived from query abstracts and metadata, not full-text systematic extraction.",
		},
	}
}

func buildOverviewSentence(fieldLabel, fieldFamily string, fieldConfidence float64, paperCount int, providerText, themeSummary string) string {
	subject := fieldLabel
	if subject == "" {
		subject = "this topic"
	}

	if paperCount <= 0 {
		return fmt.Sprintf(
			"This field brief uses %s as its organizing frame and draws on %s. The strongest recurring signals are %s.",
			subject,
			providerText,
			themeSummary,
		)
	}

	if paperCount == 1 {
		return fmt.Sprintf(
			"This field brief organizes the single retrieved paper around %s and draws on %s. The strongest recurring signals are %s.",
			subject,
			providerText,
			themeSummary,
		)
	}

	return fmt.Sprintf(
		"This field brief organizes %d papers around %s and draws on %s. The strongest recurring signals are %s.",
		paperCount,
		subject,
		providerText,
		themeSummary,
	)
}

func buildTopicPrimer(fieldLabel string) string {
	normalized := strings.ToLower(strings.TrimSpace(fieldLabel))
	switch {
	case containsAny(normalized, []string{"rlhf", "reinforcement learning from human feedback"}):
		return "RLHF (reinforcement learning from human feedback) fine-tunes a model against a reward signal learned from human preference data, usually after supervised instruction tuning. In large language model work, it is a core alignment step for steering outputs toward more helpful, safe, and instruction-following behavior."
	case containsAny(normalized, []string{"rlaif", "reinforcement learning from ai feedback"}):
		return "RLAIF (reinforcement learning from AI feedback) replaces some human preference judgments with model-generated critiques or rankings so alignment data can scale more cheaply."
	case containsAny(normalized, []string{"reinforcement learning"}):
		return "Reinforcement learning studies how an agent improves a policy through reward-driven feedback over sequential decisions."
	default:
		return ""
	}
}

func isRLHFTopic(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return containsAny(normalized, []string{
		"rlhf",
		"reinforcement learning from human feedback",
		"reward model",
		"reward-model",
		"preference optimization",
		"direct preference optimization",
		"human preference",
		"human feedback",
	})
}

func joinOverviewParts(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, " ")
}

func providersSentence(providerSet map[string]struct{}) string {
	providers := make([]string, 0, len(providerSet))
	for provider := range providerSet {
		provider = strings.TrimSpace(provider)
		if provider != "" {
			providers = append(providers, provider)
		}
	}
	if len(providers) == 0 {
		return "the local search providers"
	}
	sort.Strings(providers)
	if len(providers) == 1 {
		return providers[0]
	}
	if len(providers) == 2 {
		return fmt.Sprintf("%s and %s", providers[0], providers[1])
	}
	return strings.Join(providers[:minInt(len(providers), 3)], ", ") + " and others"
}

func deriveWhyItMatters(meta queryIntroductionMeta) string {
	themeBlob := strings.ToLower(strings.Join(meta.CoreThemes, " "))
	field := strings.ToLower(meta.FieldLabel)
	switch {
	case isRLHFTopic(meta.FieldLabel):
		return "Changes in preference data, reward-model calibration, and evaluation protocol can make RLHF look aligned on paper while masking reward hacking or weak transfer to real user interactions."
	case meta.FieldLabel != "" && strings.Contains(strings.ToLower(meta.FieldLabel), "medicine"):
		return "Small changes in study design, comparators, or endpoints can change what counts as a meaningful clinical result."
	case meta.FieldLabel != "" && strings.Contains(strings.ToLower(meta.FieldLabel), "biology"):
		return "Assay choice, model systems, and biological context can shift the meaning of the same reported effect."
	case meta.FieldLabel != "" && strings.Contains(strings.ToLower(meta.FieldLabel), "psychology"):
		return "Sample composition, task design, and measurement choices can change both effect size and reproducibility."
	case meta.FieldLabel != "" && strings.Contains(strings.ToLower(meta.FieldLabel), "physics"):
		return "Parameter regime and experimental setup often determine whether a result is broadly valid or narrowly conditional."
	case containsAny(themeBlob, []string{"benchmark", "evaluation"}):
		return fmt.Sprintf("Benchmark choices and evaluation rules can make %s look more or less mature than it really is.", field)
	case containsAny(themeBlob, []string{"method", "protocol"}):
		return fmt.Sprintf("Small protocol differences can drive large swings in how %s is interpreted.", field)
	case containsAny(themeBlob, []string{"application", "real-world", "clinical", "deployment"}):
		return "The field only becomes useful once claims survive transfer to real settings, users, or workflows."
	case containsAny(themeBlob, []string{"replication", "robust", "transfer", "generaliz"}):
		return fmt.Sprintf("Reproducibility and transfer are what separate durable findings from noisy claims in %s.", field)
	case containsAny(themeBlob, []string{"survey", "review", "synthesis"}):
		return fmt.Sprintf("%s still needs synthesis that separates broad coverage from actual consensus.", field)
	default:
		return fmt.Sprintf("The literature is still filtering durable findings from incomplete evidence around %s.", field)
	}
}

func deriveOpenQuestions(fieldLabel, fieldFamily string, yearMin, yearMax int, coreThemes []string) []string {
	if fieldLabel == "" {
		fieldLabel = "this field"
	}
	normalizedLabel := strings.ToLower(fieldLabel)
	themeBlob := strings.ToLower(strings.Join(coreThemes, " "))
	questions := fieldSpecificOpenQuestions(fieldLabel, fieldFamily)
	questions = append(questions,
		fmt.Sprintf("Which methodological choices most change the reported outcome in %s?", normalizedLabel),
	)
	if len(coreThemes) > 0 {
		questions = append(questions, fmt.Sprintf("Where do the main themes in %s still disagree across studies?", normalizedLabel))
	}
	if containsAny(themeBlob, []string{"application", "real-world", "clinical", "deployment"}) {
		questions = append(questions, fmt.Sprintf("Which applied settings still lack strong evidence for %s?", normalizedLabel))
	}
	if yearMin > 0 && yearMax > 0 && yearMin != yearMax {
		questions = append(questions, fmt.Sprintf("How have the main findings in %s shifted from %d to %d?", normalizedLabel, yearMin, yearMax))
	}
	if yearMin > 0 && yearMax > 0 && yearMin == yearMax {
		questions = append(questions, fmt.Sprintf("How representative is the evidence concentrated around %d?", yearMin))
	}
	return dedupeStrings(questions, 4)
}

func buildMethodDirection(fieldLabel, fieldFamily string, coreThemes []string) queryIntroductionResearchDirection {
	field := strings.ToLower(fieldLabel)
	rationale, query, title := fieldSpecificMethodDirection(field, fieldFamily)
	if containsAny(strings.ToLower(strings.Join(coreThemes, " ")), []string{"benchmark", "evaluation"}) && fieldFamily == "computerscience" {
		rationale = "A methods-focused follow-up can separate benchmark design from true progress."
		query = fmt.Sprintf("%s benchmark methodology comparison", field)
	}
	return queryIntroductionResearchDirection{
		Title:     title,
		Rationale: rationale,
		Query:     query,
		Kind:      "method",
	}
}

func buildRobustnessDirection(fieldLabel, fieldFamily string, coreThemes []string) queryIntroductionResearchDirection {
	field := strings.ToLower(fieldLabel)
	rationale, query, title := fieldSpecificRobustnessDirection(field, fieldFamily)
	if containsAny(strings.ToLower(strings.Join(coreThemes, " ")), []string{"replication", "robust", "transfer", "generaliz"}) && fieldFamily == "computerscience" {
		rationale = "A robustness-focused search helps verify whether the same finding holds across seeds, cohorts, or reporting conventions."
	}
	return queryIntroductionResearchDirection{
		Title:     title,
		Rationale: rationale,
		Query:     query,
		Kind:      "method",
	}
}

func buildGapDirection(fieldLabel, fieldFamily string, coreThemes []string) queryIntroductionResearchDirection {
	field := strings.ToLower(fieldLabel)
	rationale, query, title := fieldSpecificGapDirection(field, fieldFamily)
	if len(coreThemes) > 0 && fieldFamily == "computerscience" {
		rationale = fmt.Sprintf("A gap-focused follow-up can surface where %s still lacks consensus or complete coverage.", field)
	}
	return queryIntroductionResearchDirection{
		Title:     title,
		Rationale: rationale,
		Query:     query,
		Kind:      "gap",
	}
}

func buildComparisonDirection(fieldLabel, fieldFamily string, coreThemes []string) queryIntroductionResearchDirection {
	field := strings.ToLower(fieldLabel)
	rationale, query, title := fieldSpecificComparisonDirection(field, fieldFamily)
	if containsAny(strings.ToLower(strings.Join(coreThemes, " ")), []string{"application", "clinical", "real-world", "deployment"}) && fieldFamily == "computerscience" {
		rationale = "A comparison search helps show which claims survive different settings, populations, or deployment constraints."
	}
	return queryIntroductionResearchDirection{
		Title:     title,
		Rationale: rationale,
		Query:     query,
		Kind:      "comparison",
	}
}

func buildApplicationDirection(fieldLabel, fieldFamily string, coreThemes []string) queryIntroductionResearchDirection {
	field := strings.ToLower(fieldLabel)
	rationale, query, title := fieldSpecificApplicationDirection(field, fieldFamily)
	if containsAny(strings.ToLower(strings.Join(coreThemes, " ")), []string{"clinical", "deployment", "practice"}) && fieldFamily == "computerscience" {
		rationale = "An application-focused follow-up clarifies whether the literature is ready for practice or still mostly proof-of-concept."
	}
	return queryIntroductionResearchDirection{
		Title:     title,
		Rationale: rationale,
		Query:     query,
		Kind:      "application",
	}
}

func fieldSpecificOpenQuestions(fieldLabel, fieldFamily string) []string {
	field := strings.ToLower(fieldLabel)
	if isRLHFTopic(fieldLabel) {
		return []string{
			fmt.Sprintf("How much of the reported gain in %s comes from reward-model quality versus policy optimization versus preference-data curation?", field),
			fmt.Sprintf("Do improvements in %s hold under adversarial, out-of-distribution, or multi-turn evaluation rather than only single-turn benchmarks?", field),
		}
	}
	switch fieldFamily {
	case "medicine":
		return []string{
			fmt.Sprintf("Which patient cohorts, endpoints, or comparators most change the findings in %s?", field),
			fmt.Sprintf("Which safety, outcome, or follow-up questions are still unresolved in %s?", field),
		}
	case "biology":
		return []string{
			fmt.Sprintf("Which organisms, cell types, or assays most change the findings in %s?", field),
			fmt.Sprintf("Which mechanism or pathway claims in %s still need stronger experimental support?", field),
		}
	case "psychology":
		return []string{
			fmt.Sprintf("Which samples, tasks, or measures most change the findings in %s?", field),
			fmt.Sprintf("Which interventions in %s still need better replication across populations?", field),
		}
	case "physics":
		return []string{
			fmt.Sprintf("Which regimes, observables, or boundary conditions most change the findings in %s?", field),
			fmt.Sprintf("Which experimental or simulation settings in %s remain least settled?", field),
		}
	default:
		return []string{
			fmt.Sprintf("Which benchmarks, datasets, or settings best test whether claims in %s generalize?", field),
		}
	}
}

func fieldSpecificMethodDirection(field, fieldFamily string) (string, string, string) {
	if isRLHFTopic(field) {
		return "A methods-focused follow-up isolates whether reported gains come from preference-data curation, reward-model design, or policy-optimization choices.",
			fmt.Sprintf("%s reward model preference dataset optimization comparison", field),
			"Compare reward models and preference data"
	}
	switch fieldFamily {
	case "medicine":
		return "A clinical-method follow-up isolates whether outcome differences come from study design, comparator choice, or endpoint selection.",
			fmt.Sprintf("%s clinical trial design comparison", field),
			"Compare clinical methods and trial design"
	case "biology":
		return "A biology-focused follow-up separates assay choice, model system, and pathway interpretation.",
			fmt.Sprintf("%s assay model system comparison", field),
			"Compare assays and model systems"
	case "psychology":
		return "A psychology-focused follow-up separates intervention design, sample composition, and measurement choice.",
			fmt.Sprintf("%s intervention measurement comparison", field),
			"Compare interventions and measures"
	case "physics":
		return "A physics-focused follow-up separates model assumptions, experimental setup, and parameter regime.",
			fmt.Sprintf("%s model experiment comparison", field),
			"Compare models and experiments"
	default:
		return "A methods-focused follow-up isolates whether performance changes come from protocol choices, evaluation settings, or benchmarks.",
			fmt.Sprintf("%s methods and benchmarks", field),
			"Compare methods and benchmarks"
	}
}

func fieldSpecificRobustnessDirection(field, fieldFamily string) (string, string, string) {
	if isRLHFTopic(field) {
		return "A robustness search helps check whether gains persist under adversarial prompts, multi-turn interaction, and reward-hacking stress tests.",
			fmt.Sprintf("%s reward hacking adversarial multi-turn evaluation", field),
			"Stress-test reward hacking and multi-turn robustness"
	}
	switch fieldFamily {
	case "medicine":
		return "A robustness search checks whether the same clinical signal holds across cohorts, sites, and care settings.",
			fmt.Sprintf("%s cohort site replication", field),
			"Test replication across cohorts and sites"
	case "biology":
		return "A robustness search checks whether the same biological signal holds across species, cell types, and assay conditions.",
			fmt.Sprintf("%s species assay reproducibility", field),
			"Test reproducibility across species and assays"
	case "psychology":
		return "A robustness search checks whether the same psychological signal holds across samples, tasks, and measurement scales.",
			fmt.Sprintf("%s sample measure replication", field),
			"Test replication across samples and measures"
	case "physics":
		return "A robustness search checks whether the same physical signal holds across parameter regimes, calibration choices, and experimental conditions.",
			fmt.Sprintf("%s parameter sensitivity replication", field),
			"Stress-test parameter sensitivity and replication"
	default:
		return "A replication-oriented search helps separate stable claims from results that only hold in a narrow setup.",
			fmt.Sprintf("%s robustness replication", field),
			"Stress-test robustness and replication"
	}
}

func fieldSpecificGapDirection(field, fieldFamily string) (string, string, string) {
	if isRLHFTopic(field) {
		return "A gap-focused follow-up can surface where alignment claims outpace evidence on transfer, safety, or evaluator agreement.",
			fmt.Sprintf("%s alignment evaluation gaps", field),
			"Map unresolved alignment and evaluation gaps"
	}
	switch fieldFamily {
	case "medicine":
		return "A gap-focused follow-up can surface where the clinical evidence still lacks consensus or complete coverage.",
			fmt.Sprintf("%s clinical evidence gaps", field),
			"Map unresolved clinical questions"
	case "biology":
		return "A gap-focused follow-up can surface where the biological evidence still lacks consensus or complete coverage.",
			fmt.Sprintf("%s biological evidence gaps", field),
			"Map unresolved mechanism gaps"
	case "psychology":
		return "A gap-focused follow-up can surface where the psychological evidence still lacks consensus or complete coverage.",
			fmt.Sprintf("%s psychology evidence gaps", field),
			"Map unresolved measurement gaps"
	case "physics":
		return "A gap-focused follow-up can surface where the physical evidence still lacks consensus or complete coverage.",
			fmt.Sprintf("%s physics regime gaps", field),
			"Map unresolved regime gaps"
	default:
		return "A gap-focused follow-up can surface where the field still lacks consensus or complete coverage.",
			fmt.Sprintf("%s unresolved questions", field),
			"Map unresolved questions"
	}
}

func fieldSpecificComparisonDirection(field, fieldFamily string) (string, string, string) {
	if isRLHFTopic(field) {
		return "A comparison search helps separate classic RLHF from DPO, RLAIF, constitutional methods, and other preference-optimization variants.",
			fmt.Sprintf("%s RLHF DPO RLAIF comparison", field),
			"Compare RLHF with adjacent alignment methods"
	}
	switch fieldFamily {
	case "medicine":
		return "A comparison search helps show which claims survive different patient cohorts, care settings, and follow-up windows.",
			fmt.Sprintf("%s patient cohort care setting comparison", field),
			"Compare cohorts and care settings"
	case "biology":
		return "A comparison search helps show which claims survive different species, tissues, and assay systems.",
			fmt.Sprintf("%s species tissue assay comparison", field),
			"Compare species, tissues, and assays"
	case "psychology":
		return "A comparison search helps show which claims survive different samples, tasks, and outcome measures.",
			fmt.Sprintf("%s sample task measure comparison", field),
			"Compare samples, tasks, and measures"
	case "physics":
		return "A comparison search helps show which claims survive different observables, apparatus, and parameter regimes.",
			fmt.Sprintf("%s observable regime comparison", field),
			"Compare observables and regimes"
	default:
		return "A comparison search helps identify where effects generalize across cohorts, datasets, and contexts.",
			fmt.Sprintf("%s comparison across settings", field),
			"Compare settings and populations"
	}
}

func fieldSpecificApplicationDirection(field, fieldFamily string) (string, string, string) {
	if isRLHFTopic(field) {
		return "An application-focused follow-up clarifies whether offline alignment gains survive real user interaction, agent workflows, or deployment monitoring.",
			fmt.Sprintf("%s deployed assistant user study outcomes", field),
			"Check deployed assistant outcomes"
	}
	switch fieldFamily {
	case "medicine":
		return "An application-focused follow-up clarifies whether the literature is ready for clinical use or still mostly proof-of-concept.",
			fmt.Sprintf("%s clinical outcomes real-world", field),
			"Check translational outcomes"
	case "biology":
		return "An application-focused follow-up clarifies whether the biology has moved from mechanism claims to functional validation.",
			fmt.Sprintf("%s functional validation application", field),
			"Check translational or experimental outcomes"
	case "psychology":
		return "An application-focused follow-up clarifies whether the findings hold in intervention, therapy, or educational settings.",
			fmt.Sprintf("%s intervention outcome application", field),
			"Check intervention outcomes"
	case "physics":
		return "An application-focused follow-up clarifies whether the findings translate into experimental design, engineering, or instrument use.",
			fmt.Sprintf("%s experimental engineering application", field),
			"Check experimental applications"
	default:
		return "An application-focused follow-up clarifies where strong evidence exists for real-world use.",
			fmt.Sprintf("%s real-world applications", field),
			"Check applied outcomes"
	}
}

func collectCoreThemes(papers []queryIntroductionPaper) []string {
	coreThemes := make([]string, 0, 5)
	for _, paper := range papers {
		text := strings.ToLower(strings.TrimSpace(paper.Abstract + " " + paper.Summary + " " + paper.Title))
		switch {
		case containsAny(text, []string{"rlhf", "rlaif", "reward model", "reward-model", "preference optimization", "direct preference optimization", "dpo", "ppo", "preference dataset", "human preference", "human feedback"}):
			coreThemes = appendIfMissing(coreThemes, "Preference data, reward modeling, and policy optimization")
		}
		switch {
		case containsAny(text, []string{"alignment", "instruction following", "helpful", "harmless", "assistant behavior", "chatbot", "safety", "toxicity"}):
			coreThemes = appendIfMissing(coreThemes, "Alignment behavior, safety, and instruction following")
		}
		switch {
		case containsAny(text, []string{"reward hacking", "specification gaming", "adversarial", "jailbreak", "multi-turn", "out-of-distribution", "ood"}):
			coreThemes = appendIfMissing(coreThemes, "Reward hacking, robustness, and multi-turn evaluation")
		}
		switch {
		case containsAny(text, []string{"benchmark", "evaluation", "metric", "accuracy", "precision", "recall"}):
			coreThemes = appendIfMissing(coreThemes, "Benchmark design and evaluation quality")
		}
		switch {
		case containsAny(text, []string{"method", "framework", "protocol", "algorithm", "architecture", "pipeline"}):
			coreThemes = appendIfMissing(coreThemes, "Method variation and protocol design")
		}
		switch {
		case containsAny(text, []string{"robust", "replication", "reproducibility", "generaliz", "transfer", "ablation"}):
			coreThemes = appendIfMissing(coreThemes, "Robustness, replication, and transfer")
		}
		switch {
		case containsAny(text, []string{"dataset", "cohort", "population", "setting", "domain"}):
			coreThemes = appendIfMissing(coreThemes, "Dataset and setting effects")
		}
		switch {
		case containsAny(text, []string{"application", "deployment", "clinical", "practice", "intervention", "policy", "translation"}):
			coreThemes = appendIfMissing(coreThemes, "Applied and real-world evidence")
		}
		switch {
		case containsAny(text, []string{"survey", "review", "meta-analysis", "systematic"}):
			coreThemes = appendIfMissing(coreThemes, "Synthesis and review literature")
		}
	}
	return coreThemes
}

func joinHumanList(values []string) string {
	cleaned := dedupeStrings(values, len(values))
	switch len(cleaned) {
	case 0:
		return ""
	case 1:
		return cleaned[0]
	case 2:
		return fmt.Sprintf("%s and %s", cleaned[0], cleaned[1])
	default:
		return fmt.Sprintf("%s, %s, and %s", cleaned[0], cleaned[1], cleaned[2])
	}
}

func buildProviderSet(
	papers []queryIntroductionPaper,
	providersUsed []string,
) map[string]struct{} {
	providerSet := map[string]struct{}{}
	for _, provider := range providersUsed {
		provider = strings.TrimSpace(provider)
		if provider != "" {
			providerSet[provider] = struct{}{}
		}
	}
	for _, paper := range papers {
		for _, provider := range paper.SourceApis {
			provider = strings.TrimSpace(provider)
			if provider != "" {
				providerSet[provider] = struct{}{}
			}
		}
	}
	return providerSet
}

func buildMarkdownBullets(values []string) []string {
	bullets := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		bullets = append(bullets, "- "+value)
	}
	return bullets
}

func dedupeDirectionStructs(directions []queryIntroductionResearchDirection) []queryIntroductionResearchDirection {
	seen := map[string]struct{}{}
	normalized := make([]queryIntroductionResearchDirection, 0, len(directions))
	for _, direction := range directions {
		query := strings.TrimSpace(direction.Query)
		kind := strings.TrimSpace(direction.Kind)
		title := strings.TrimSpace(direction.Title)
		rationale := strings.TrimSpace(direction.Rationale)
		if query == "" || kind == "" || title == "" || rationale == "" {
			continue
		}
		key := strings.ToLower(fmt.Sprintf("%s:%s:%s", kind, query, title))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, queryIntroductionResearchDirection{
			Title:     title,
			Rationale: rationale,
			Query:     sanitizeMarkdownInlineCode(query),
			Kind:      kind,
		})
	}
	return normalized
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func titleCasePreservingAcronyms(value string) string {
	parts := strings.Fields(strings.TrimSpace(value))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = titleCaseToken(part)
	}
	return strings.Join(parts, " ")
}

func titleCaseToken(value string) string {
	start := 0
	end := len(value)
	for start < end {
		r, size := utf8.DecodeRuneInString(value[start:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(value[:end])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		end -= size
	}
	if start >= end {
		return value
	}

	prefix := value[:start]
	core := value[start:end]
	suffix := value[end:]
	if isAcronymToken(core) {
		return prefix + strings.ToUpper(core) + suffix
	}

	lower := strings.ToLower(core)
	r, size := utf8.DecodeRuneInString(lower)
	return prefix + strings.ToUpper(string(r)) + lower[size:] + suffix
}

func isAcronymToken(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if _, ok := commonResearchAcronyms[normalized]; ok {
		return true
	}

	letters := 0
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			letters++
			if !unicode.IsUpper(r) {
				return false
			}
		case unicode.IsDigit(r):
			continue
		default:
			return false
		}
	}
	return letters > 0 && letters <= 6
}

var commonResearchAcronyms = map[string]struct{}{
	"ai":     {},
	"cnn":    {},
	"crispr": {},
	"dna":    {},
	"gnn":    {},
	"gpt":    {},
	"llm":    {},
	"ml":     {},
	"nlp":    {},
	"rag":    {},
	"rna":    {},
	"rl":     {},
	"rlhf":   {},
	"rnn":    {},
}

func sanitizeMarkdownInlineCode(value string) string {
	s := strings.ReplaceAll(value, "`", "")
	return strings.ReplaceAll(s, "\n", " ")
}

func appendIfMissing(values []string, candidate string) []string {
	normalizedCandidate := strings.ToLower(strings.TrimSpace(candidate))
	if normalizedCandidate == "" {
		return values
	}
	for _, existing := range values {
		if strings.ToLower(strings.TrimSpace(existing)) == normalizedCandidate {
			return values
		}
	}
	return append(values, candidate)
}

func dedupeStrings(values []string, maxItems int) []string {
	seen := map[string]struct{}{}
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		key := strings.ToLower(normalized)
		if normalized == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, normalized)
		if len(deduped) >= maxItems {
			break
		}
	}
	return deduped
}

func BuildLocalPaperSummary(title string, abstract string, maxFindings int) string {
	cleanTitle := strings.TrimSpace(title)
	cleanAbstract := strings.TrimSpace(abstract)
	if cleanAbstract == "" {
		return fmt.Sprintf("%s appears relevant to the current research question. Local development mode generated a lightweight metadata summary because richer summarization workers are optional.", cleanTitle)
	}
	sentences := strings.FieldsFunc(cleanAbstract, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	parts := make([]string, 0, 2)
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		parts = append(parts, sentence)
		if len(parts) >= 2 {
			break
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s is relevant to the current query. Available metadata suggests it should be reviewed for evidence, methods, and conclusions.", cleanTitle)
	}
	summary := strings.Join(parts, ". ")
	if !strings.HasSuffix(summary, ".") {
		summary += "."
	}
	if maxFindings > 0 {
		return summary
	}
	return summary
}

func BuildKeyFindings(abstract string, maxFindings int) []string {
	if maxFindings <= 0 {
		maxFindings = 3
	}
	sentences := strings.FieldsFunc(strings.TrimSpace(abstract), func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	findings := make([]string, 0, maxFindings)
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		findings = append(findings, sentence)
		if len(findings) >= maxFindings {
			break
		}
	}
	if len(findings) == 0 {
		findings = append(findings, "Review this source directly for methods, evidence, and limitations.")
	}
	return findings
}
