package researchquery

import "strings"

// researchTypoExact maps frequent research-query misspellings to canonical terms.
var researchTypoExact = map[string]string{
	"menicius":        "meniscus",
	"meniscis":        "meniscus",
	"menisci":         "meniscus",
	"meniscu":         "meniscus",
	"meniscal":        "meniscal",
	"stratiges":       "strategies",
	"stratigies":      "strategies",
	"startegies":      "strategies",
	"stratigy":        "strategy",
	"stratige":        "strategy",
	"reconstricution": "reconstruction",
	"recontruction":   "reconstruction",
	"recontructions":  "reconstructions",
	"recontrcution":   "reconstruction",
	"constricution":   "reconstruction",
	"scafold":         "scaffold",
	"scafolds":        "scaffolds",
	"hydogel":         "hydrogel",
	"retieval":        "retrieval",
	"retreival":       "retrieval",
	"systematc":       "systematic",
	"metanalysis":     "meta-analysis",
	"metananalysis":   "meta-analysis",
}

var researchVocabulary = []string{
	"meniscus", "meniscal", "reconstruction", "reconstructive",
	"strategies", "strategy", "scaffold", "scaffolds", "hydrogel",
	"ligament", "cruciate", "anterior", "posterior", "orthopedic",
	"orthopaedic", "cartilage", "regeneration", "transplantation",
	"systematic", "meta-analysis", "clinical", "trial", "cohort",
	"retrieval", "augmented", "generation", "transformer", "transformers",
	"benchmark", "dataset", "neural", "network", "networks",
}

// NormalizeText trims and collapses whitespace in a research query.
func NormalizeText(query string) string {
	query = strings.TrimSpace(query)
	query = strings.TrimRight(query, "_-")
	return strings.Join(strings.Fields(query), " ")
}

// CorrectTypos applies offline typo and fuzzy vocabulary correction.
func CorrectTypos(query string) string {
	query = NormalizeText(query)
	if query == "" {
		return ""
	}
	words := strings.Fields(query)
	changed := false
	for i, word := range words {
		cleaned := strings.ToLower(strings.Trim(word, ".,;:!?\"'()[]{}/-"))
		if cleaned == "" {
			continue
		}
		replacement := ""
		if exact, ok := researchTypoExact[cleaned]; ok {
			replacement = exact
		} else if len(cleaned) >= 5 {
			if fuzzy, ok := fuzzyResearchVocabMatch(cleaned, 2); ok {
				replacement = fuzzy
			}
		}
		if replacement != "" && replacement != cleaned {
			words[i] = replacement
			changed = true
		}
	}
	if !changed {
		return query
	}
	return strings.Join(words, " ")
}

// PrepareForProviderSearch normalizes and corrects a query before provider search.
func PrepareForProviderSearch(query string) string {
	return CorrectTypos(query)
}

func fuzzyResearchVocabMatch(word string, maxDistance int) (string, bool) {
	best := ""
	bestDistance := maxDistance + 1
	for _, candidate := range researchVocabulary {
		d := researchLevenshtein(word, candidate)
		if d < bestDistance {
			bestDistance = d
			best = candidate
		}
	}
	if best == "" || bestDistance > maxDistance {
		return "", false
	}
	return best, true
}

func researchLevenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(minInt(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
