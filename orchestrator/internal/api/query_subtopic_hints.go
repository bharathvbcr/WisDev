package api

import "strings"

func queryAwareSubtopicHints(query string, domain string) []string {
	text := strings.ToLower(strings.TrimSpace(query + " " + domain))
	if text == "" {
		return nil
	}

	hints := []string{}
	add := func(values ...string) {
		hints = append(hints, values...)
	}

	if containsAnyTerm(text,
		[]string{"reinforcement learning from human feedback", "preference learning", "human feedback", "reward model", "reward modeling", "alignment"},
		[]string{"rlhf"}) {
		add(
			"Reward Modeling",
			"Policy Optimization",
			"Preference Data Quality",
			"Evaluation Benchmarks",
			"Human Feedback Protocols",
			"Safety and Alignment Failures",
		)
	}
	if containsAny(text, []string{"benchmark", "evaluation", "leaderboard", "baseline", "model comparison", "generalization"}) {
		add(
			"Benchmark Datasets",
			"Evaluation Protocols",
			"Baseline Comparisons",
			"Generalization Tests",
			"Metric Validity",
		)
	}
	if containsAny(text, []string{"clinical", "medicine", "health", "patient", "treatment", "therapy"}) {
		add(
			"Patient Selection",
			"Clinical Outcomes",
			"Safety Signals",
			"Treatment Protocols",
			"Bias and Confounding",
		)
	}
	if containsAny(text, []string{"systematic review", "meta-analysis", "meta analysis", "prisma", "evidence synthesis"}) {
		add(
			"Inclusion Criteria",
			"Evidence Synthesis",
			"Study Quality Assessment",
			"Publication Bias",
		)
	}
	if containsAny(text, []string{"causal", "longitudinal", "confounding", "counterfactual"}) {
		add(
			"Causal Identification",
			"Longitudinal Outcomes",
			"Confounding Control",
		)
	}

	return uniqueStrings(hints)
}
