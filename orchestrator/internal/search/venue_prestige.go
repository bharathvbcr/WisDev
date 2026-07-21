package search

import "strings"

// Venue prestige scores (0–100) folded from frontend/config/scoringConfig.ts.
// Canonical owner: Go. Used by ScoreQuality and PaperPreferenceScoreWithQuery.
var venuePrestigeTable = []struct {
	needle string
	score  float64
}{
	{"nature communications", 80},
	{"new england journal of medicine", 90},
	{"proceedings of the national academy", 85},
	{"science advances", 78},
	{"ieee transactions", 75},
	{"nature", 100},
	{"science", 98},
	{"cell", 95},
	{"lancet", 92},
	{"nejm", 90},
	{"jama", 88},
	{"pnas", 85},
	{"acm", 75},
	{"plos one", 70},
	{"frontiers", 65},
	{"mdpi", 60},
	{"arxiv", 60},
	{"biorxiv", 58},
	{"medrxiv", 55},
}

const venuePrestigeDefault = 50.0

// VenuePrestigeNorm maps a venue/publication name to a 0–1 prestige score.
// Longer needle matches are preferred (table is ordered longest-first for
// compound names like "nature communications" before "nature").
func VenuePrestigeNorm(venue string) float64 {
	lower := strings.ToLower(strings.TrimSpace(venue))
	if lower == "" {
		return venuePrestigeDefault / 100.0
	}
	for _, entry := range venuePrestigeTable {
		if strings.Contains(lower, entry.needle) {
			return entry.score / 100.0
		}
	}
	return venuePrestigeDefault / 100.0
}

// AccessNorm maps open-access availability to a 0–1 score
// (FE: openAccessUrl/pdfUrl → 100 else 30).
func AccessNorm(openAccessURL, pdfURL string) float64 {
	if strings.TrimSpace(openAccessURL) != "" || strings.TrimSpace(pdfURL) != "" {
		return 1.0
	}
	return 0.30
}
