package search

import "strings"

// InferDomainFromQuery returns a coarse routing domain inferred from query text.
func InferDomainFromQuery(query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return "general"
	}

	if containsAny(query,
		"meniscus", "acl", "orthopedic", "orthopaedic", "knee", "arthroplasty",
		"clinical trial", "randomized trial", "patient", "hospital", "surgery",
		"diagnosis", "therapy", "treatment", "disease", "drug", "pharma",
		"medicine", "medical", "healthcare", "cardiology", "oncology",
	) {
		return "medicine"
	}
	if containsAny(query,
		"cancer", "tumor", "virus", "vaccine", "genome", "genomics", "protein",
		"cell signaling", "biomedical", "biorxiv", "medrxiv",
	) {
		return "biomedical"
	}
	if containsAny(query,
		"neuroscience", "neuro", "brain", "cortex", "synapse", "fmri",
	) {
		return "neuro"
	}
	if containsAny(query,
		"transformer", "large language model", " llm ", "gpt", "diffusion model",
		"neural network", "deep learning", "machine learning", "algorithm",
		"software", "database", "cybersecurity", "compiler", "programming",
		"retrieval-augmented", "rag ", "computer science", "distributed system",
		"reinforcement learning", "computer vision", "nlp", "benchmark dataset",
	) {
		return "cs"
	}
	if containsAny(query,
		"galaxy", "quantum", "gravity", "particle", "black hole", "star",
		"telescope", "cosmology", "astrophysics", "nasa", "superconductor",
	) {
		return "physics"
	}
	if containsAny(query,
		"market", "economy", "social", "policy", "political", "finance",
		"inequality", "regression", "econometrics",
	) {
		return "social"
	}
	if containsAny(query,
		"theorem", "topology", "algebra", "calculus", "mathematics", "proof",
	) {
		return "math"
	}

	return "general"
}

// NormalizeRoutingDomain maps planner/TUI domain labels onto registry route keys.
func NormalizeRoutingDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	switch domain {
	case "", "general", "unknown":
		return "general"
	case "medicine", "medical", "clinical", "healthcare", "health":
		return "medicine"
	case "biomedical", "biology", "life sciences", "life_sciences":
		return "biomedical"
	case "computer science", "computer_science", "artificial intelligence",
		"artificial_intelligence", "machine learning", "machine_learning", "ai", "ml":
		return "cs"
	case "astronomy", "astrophysics":
		return "physics"
	case "mathematics", "maths":
		return "math"
	case "economics", "social science", "social_science", "humanities":
		return domain
	default:
		return domain
	}
}

// ProviderPresetForDomain returns a TUI preset id for auto provider narrowing.
// DomainsMatchForRouting reports whether two domain labels should share provider intelligence.
func DomainsMatchForRouting(left, right string) bool {
	l := NormalizeRoutingDomain(left)
	r := NormalizeRoutingDomain(right)
	if l == r {
		return true
	}
	if isClinicalDomain(l) && isClinicalDomain(r) {
		return true
	}
	return false
}

func isClinicalDomain(domain string) bool {
	switch domain {
	case "medicine", "biomedical", "biology", "neuro":
		return true
	default:
		return false
	}
}

func ProviderPresetForDomain(domain string) (preset string, ok bool) {
	switch NormalizeRoutingDomain(domain) {
	case "medicine", "biomedical", "biology", "neuro":
		return "biomedical", true
	case "cs", "ai", "ml", "machine_learning", "computer_science":
		return "cs", true
	case "physics", "astronomy", "math", "mathematics":
		return "physics", true
	default:
		return "", false
	}
}
