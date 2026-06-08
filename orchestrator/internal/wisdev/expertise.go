package wisdev

import (
	"regexp"
	"strings"
)

type ExpertiseLevel string

const (
	ExpertiseBeginner     ExpertiseLevel = "beginner"
	ExpertiseIntermediate ExpertiseLevel = "intermediate"
	ExpertiseExpert       ExpertiseLevel = "expert"
)

var (
	expertSignals = []string{
		"rlhf", "pico", "dft", "mcmc", "bayesian", "lasso", "xgboost", "vae", "gan",
		"transformer", "attention mechanism", "backpropagation", "gradient descent",
		"imagenet", "cifar", "mnist", "glue", "squad", "coco", "wikitext",
		"bleu", "rouge", "f1 score", "auc", "perplexity", "accuracy",
		"pytorch", "tensorflow", "huggingface", "scikit-learn",
		"et al", "llm", "nlp", "cv", "rl", "rnn", "cnn", "lstm", "gpt", "bert", "clip",
		"fmri", "eeg", "rct", "snp", "gwas", "pcr", "elisa",
		"fem", "cfd", "pid", "mimo",
	}

	beginnerSignals = []string{
		"what is", "how does", "explain", "introduction to", "basics of",
		"overview of", "beginner", "simple", "easy", "fundamentals",
		"define", "meaning of", "difference between",
	}

	comparativeRegex = regexp.MustCompile(`(?i)\b(versus|vs\.?|compared to|in contrast|specifically|notably)\b`)
)

// DetectExpertiseLevel uses heuristics to estimate the researcher's expertise.
func DetectExpertiseLevel(query string) ExpertiseLevel {
	lower := strings.ToLower(strings.TrimSpace(query))
	words := strings.Fields(lower)
	if len(words) == 0 {
		return ExpertiseBeginner
	}

	score := 5.0

	for _, s := range beginnerSignals {
		if strings.Contains(lower, s) {
			score -= 2.0
		}
	}

	for _, s := range expertSignals {
		if strings.Contains(lower, s) {
			score += 1.5
		}
	}

	if len(words) <= 3 {
		score -= 2.0
	} else if len(words) > 12 {
		score += 1.0
	}

	if strings.HasPrefix(lower, "what") || strings.HasPrefix(lower, "how") || strings.HasSuffix(lower, "?") {
		score -= 1.0
	}

	if comparativeRegex.MatchString(lower) {
		score += 1.0
	}

	if score <= 3.5 {
		return ExpertiseBeginner
	}
	if score >= 7.5 {
		return ExpertiseExpert
	}
	return ExpertiseIntermediate
}
