package api

import (
	"fmt"
	"sort"
	"strings"
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
	title := strings.TrimSpace(query)
	if title == "" {
		title = "Research topic"
	}

	if len(papers) == 0 {
		return fmt.Sprintf(
			"## %s\n\nThis search reviews the current evidence landscape for %s. No paper metadata was available to ground a richer local introduction, so treat this as a placeholder and verify claims directly against the retrieved studies.",
			title,
			title,
		)
	}

	topTitles := make([]string, 0, 3)
	methodSignals := make([]string, 0, 4)
	yearMin := 0
	yearMax := 0
	providerSet := map[string]struct{}{}

	for _, provider := range providersUsed {
		provider = strings.TrimSpace(provider)
		if provider != "" {
			providerSet[provider] = struct{}{}
		}
	}

	for _, paper := range papers {
		paperTitle := strings.TrimSpace(paper.Title)
		if paperTitle != "" && len(topTitles) < 3 {
			topTitles = append(topTitles, paperTitle)
		}
		if paper.Year > 0 {
			if yearMin == 0 || paper.Year < yearMin {
				yearMin = paper.Year
			}
			if yearMax == 0 || paper.Year > yearMax {
				yearMax = paper.Year
			}
		}
		for _, provider := range paper.SourceApis {
			provider = strings.TrimSpace(provider)
			if provider != "" {
				providerSet[provider] = struct{}{}
			}
		}
		abstractLower := strings.ToLower(strings.TrimSpace(paper.Abstract + " " + paper.Summary))
		switch {
		case strings.Contains(abstractLower, "survey"), strings.Contains(abstractLower, "review"):
			methodSignals = appendIfMissing(methodSignals, "survey and review papers")
		case strings.Contains(abstractLower, "benchmark"), strings.Contains(abstractLower, "evaluation"):
			methodSignals = appendIfMissing(methodSignals, "benchmark-style evaluations")
		case strings.Contains(abstractLower, "alignment"), strings.Contains(abstractLower, "preference"):
			methodSignals = appendIfMissing(methodSignals, "alignment and preference-learning analyses")
		case strings.Contains(abstractLower, "safety"), strings.Contains(abstractLower, "risk"):
			methodSignals = appendIfMissing(methodSignals, "safety and risk framing")
		}
	}

	if len(methodSignals) == 0 {
		methodSignals = append(methodSignals, "a mix of empirical and conceptual papers")
	}

	providers := make([]string, 0, len(providerSet))
	for provider := range providerSet {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	if len(providers) == 0 {
		providers = append(providers, "local search providers")
	}

	evidenceWindow := "recent and foundational literature"
	if yearMin > 0 && yearMax > 0 {
		if yearMin == yearMax {
			evidenceWindow = fmt.Sprintf("%d literature", yearMin)
		} else {
			evidenceWindow = fmt.Sprintf("%d-%d literature", yearMin, yearMax)
		}
	}

	leadSentence := fmt.Sprintf(
		"%s is reviewed here through %s drawn from %d retrieved papers.",
		title,
		evidenceWindow,
		len(papers),
	)
	methodSentence := fmt.Sprintf(
		"The current set emphasizes %s, giving a practical snapshot of how the topic is being framed in the retrieved evidence.",
		strings.Join(methodSignals, ", "),
	)
	exampleSentence := ""
	if len(topTitles) > 0 {
		exampleSentence = fmt.Sprintf(
			"Representative papers in this batch include %s.",
			strings.Join(topTitles, "; "),
		)
	}
	providerSentence := fmt.Sprintf(
		"Results were assembled from %s, so you should expect some overlap in canonical links even when multiple providers contributed evidence.",
		strings.Join(providers, ", "),
	)

	paragraphs := []string{leadSentence, methodSentence}
	if exampleSentence != "" {
		paragraphs = append(paragraphs, exampleSentence)
	}
	paragraphs = append(paragraphs, providerSentence)

	return fmt.Sprintf("## %s\n\n%s", title, strings.Join(paragraphs, "\n\n"))
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
