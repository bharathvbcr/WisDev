package search

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/policy"
)

const (
	paperRankRecencyWeight   = 0.40
	paperRankCitationWeight  = 0.25
	paperRankRelevanceWeight = 0.20
	paperRankVenueWeight     = 0.10
	paperRankAccessWeight    = 0.05
	paperRecencyHorizonYears = 12
	paperUnknownYearRecency  = 0.35
)

// queryRankIntent captures FE SCORING_WEIGHTS_BY_INTENT + legacy Go keyword intents.
type queryRankIntent struct {
	name string // default|trends|review|methodology|cited|author_impact
}

func detectQueryRankIntent(query string) queryRankIntent {
	lowerQuery := strings.ToLower(query)

	// FE-aligned named intents (scoringConfig SCORING_WEIGHTS_BY_INTENT).
	if containsAny(lowerQuery, "trend", "trends", "trending", "latest", "recent advances", "recent progress", "newly", "state of the art", "sota", "current", "modern", "2024", "2025", "2026") ||
		containsAny(lowerQuery, "recent", "new") {
		return queryRankIntent{name: "trends"}
	}
	if containsAny(lowerQuery, "systematic review", "literature review", "meta-analysis", "meta analysis", "survey", "review of") ||
		(strings.Contains(lowerQuery, "review") && !strings.Contains(lowerQuery, "peer review")) {
		return queryRankIntent{name: "review"}
	}
	if containsAny(lowerQuery, "method", "methodology", "protocol", "procedure", "technique", "how to", "approach") {
		return queryRankIntent{name: "methodology"}
	}

	// Legacy Go keyword intents retained for ScoreQuality / preference parity.
	if containsAny(lowerQuery, "classic", "foundational", "seminal", "most cited", "highly cited", "citation", "popular", "famous", "landmark", "influential", "pioneer", "key papers", "seminal work") {
		return queryRankIntent{name: "cited"}
	}
	if containsAny(lowerQuery, "h-index", "hindex", "h-factor", "h factor", "author impact", "prestigious", "h-value") {
		return queryRankIntent{name: "author_impact"}
	}
	return queryRankIntent{name: "default"}
}

// RecencyNorm maps publication year to a 0–1 score favoring recent work.
func RecencyNorm(year int) float64 {
	if year <= 0 {
		return paperUnknownYearRecency
	}
	currentYear := time.Now().Year()
	age := currentYear - year
	if age < 0 {
		age = 0
	}
	if age >= paperRecencyHorizonYears {
		return 0.0
	}
	return 1.0 - float64(age)/float64(paperRecencyHorizonYears)
}

// CitationNorm maps citation count to a log-damped 0–1 score.
func CitationNorm(count int) float64 {
	const maxCitations = 10_000.0
	cit := math.Min(float64(count), maxCitations)
	return math.Log1p(cit) / math.Log1p(maxCitations)
}

// PaperPreferenceScore blends recency, citations, venue, access, and retrieval relevance.
func PaperPreferenceScore(p Paper) float64 {
	recency := RecencyNorm(p.Year)
	citations := CitationNorm(p.CitationCount)
	relevance := math.Max(0, p.Score)
	if relevance > 1 {
		relevance = 1
	}
	venue := VenuePrestigeNorm(p.Venue)
	access := AccessNorm(p.OpenAccessUrl, p.PdfUrl)
	return paperRankRecencyWeight*recency +
		paperRankCitationWeight*citations +
		paperRankRelevanceWeight*relevance +
		paperRankVenueWeight*venue +
		paperRankAccessWeight*access
}

// SortPapersByPreference returns papers ordered by PaperPreferenceScore (highest first).
func SortPapersByPreference(papers []Paper) []Paper {
	if len(papers) < 2 {
		return papers
	}
	sorted := append([]Paper(nil), papers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si := PaperPreferenceScore(sorted[i])
		sj := PaperPreferenceScore(sorted[j])
		if si == sj {
			if sorted[i].Year == sorted[j].Year {
				return sorted[i].CitationCount > sorted[j].CitationCount
			}
			return sorted[i].Year > sorted[j].Year
		}
		return si > sj
	})
	return sorted
}

// PaperPreferenceScoreWithQuery blends signals based on query intent
// (FE SCORING_WEIGHTS_BY_INTENT + legacy cited/author-impact modes).
func PaperPreferenceScoreWithQuery(p Paper, query string) float64 {
	intent := detectQueryRankIntent(query)

	recencyWeight := paperRankRecencyWeight
	citationWeight := paperRankCitationWeight
	relevanceWeight := paperRankRelevanceWeight
	venueWeight := paperRankVenueWeight
	accessWeight := paperRankAccessWeight
	authorImpactWeight := 0.0

	switch intent.name {
	case "trends":
		// FE trends: citation 0.15, venue 0.15, recency 0.40, access 0.10, relevance 0.20
		citationWeight = 0.15
		venueWeight = 0.15
		recencyWeight = 0.40
		accessWeight = 0.10
		relevanceWeight = 0.20
	case "review":
		// FE review: citation 0.35, venue 0.30, recency 0.10, access 0.05, relevance 0.20
		citationWeight = 0.35
		venueWeight = 0.30
		recencyWeight = 0.10
		accessWeight = 0.05
		relevanceWeight = 0.20
	case "methodology":
		// FE methodology: citation 0.20, venue 0.15, recency 0.15, access 0.20, relevance 0.30
		citationWeight = 0.20
		venueWeight = 0.15
		recencyWeight = 0.15
		accessWeight = 0.20
		relevanceWeight = 0.30
	case "cited":
		recencyWeight = 0.10
		citationWeight = 0.55
		relevanceWeight = 0.25
		venueWeight = 0.10
		accessWeight = 0.00
	case "author_impact":
		recencyWeight = 0.15
		citationWeight = 0.25
		relevanceWeight = 0.15
		venueWeight = 0.10
		accessWeight = 0.05
		authorImpactWeight = 0.30
	}

	recency := RecencyNorm(p.Year)
	citations := CitationNorm(p.CitationCount)
	relevance := math.Max(0, p.Score)
	if relevance > 1 {
		relevance = 1
	}
	venue := VenuePrestigeNorm(p.Venue)
	access := AccessNorm(p.OpenAccessUrl, p.PdfUrl)

	const maxInfluential = 500.0
	inf := math.Min(float64(p.InfluentialCitationCount), maxInfluential)
	authorImpact := math.Log1p(inf) / math.Log1p(maxInfluential)

	return recencyWeight*recency +
		citationWeight*citations +
		relevanceWeight*relevance +
		venueWeight*venue +
		accessWeight*access +
		authorImpactWeight*authorImpact
}

// SortPapersByPreferenceWithQuery returns papers ordered by PaperPreferenceScoreWithQuery (highest first).
func SortPapersByPreferenceWithQuery(papers []Paper, query string) []Paper {
	if len(papers) < 2 {
		return papers
	}
	sorted := append([]Paper(nil), papers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		si := PaperPreferenceScoreWithQuery(sorted[i], query)
		sj := PaperPreferenceScoreWithQuery(sorted[j], query)
		if si == sj {
			if sorted[i].Year == sorted[j].Year {
				return sorted[i].CitationCount > sorted[j].CitationCount
			}
			return sorted[i].Year > sorted[j].Year
		}
		return si > sj
	})
	return sorted
}

// PaperPreferenceScoreWithResearchMode applies Go-owned research-mode ranking weights
// from internal/policy (ported from frontend/config/researchModes.ts).
func PaperPreferenceScoreWithResearchMode(p Paper, mode string) float64 {
	normalized, _ := policy.NormalizeResearchMode(mode)
	return paperPreferenceScoreWithWeights(p, policy.ModeRankingWeights(normalized))
}

// SortPapersByPreferenceWithResearchMode orders by research-mode weights and
// writes the preference score onto Paper.Score for downstream consumers.
func SortPapersByPreferenceWithResearchMode(papers []Paper, mode string) []Paper {
	if len(papers) == 0 {
		return papers
	}
	sorted := append([]Paper(nil), papers...)
	for i := range sorted {
		sorted[i].Score = PaperPreferenceScoreWithResearchMode(sorted[i], mode)
	}
	if len(sorted) < 2 {
		return sorted
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score == sorted[j].Score {
			if sorted[i].Year == sorted[j].Year {
				return sorted[i].CitationCount > sorted[j].CitationCount
			}
			return sorted[i].Year > sorted[j].Year
		}
		return sorted[i].Score > sorted[j].Score
	})
	return sorted
}

func paperPreferenceScoreWithWeights(p Paper, w policy.RankingWeights) float64 {
	recency := RecencyNorm(p.Year)
	citations := CitationNorm(p.CitationCount)
	relevance := math.Max(0, p.Score)
	if relevance > 1 {
		relevance = 1
	}
	venue := VenuePrestigeNorm(p.Venue)
	hasPdf := 0.0
	if strings.TrimSpace(p.PdfUrl) != "" || strings.TrimSpace(p.OpenAccessUrl) != "" {
		hasPdf = 1.0
	}
	hasCode := codeAvailabilityNorm(p)
	velocity := citationVelocityNorm(p)

	return w.Citations*citations +
		w.Recency*recency +
		w.Relevance*relevance +
		w.VenuePrestige*venue +
		w.HasCode*hasCode +
		w.HasPdf*hasPdf +
		w.CitationVelocity*velocity
}

func codeAvailabilityNorm(p Paper) float64 {
	haystack := strings.ToLower(p.Source + " " + strings.Join(p.SourceApis, " "))
	if strings.Contains(haystack, "paperswithcode") || strings.Contains(haystack, "pwc") {
		return 1.0
	}
	return 0.0
}

func citationVelocityNorm(p Paper) float64 {
	age := time.Now().Year() - p.Year
	if p.Year <= 0 {
		age = paperRecencyHorizonYears / 2
	}
	if age < 1 {
		age = 1
	}
	perYear := float64(p.CitationCount) / float64(age)
	const maxVelocity = 500.0
	return math.Log1p(math.Min(perYear, maxVelocity)) / math.Log1p(maxVelocity)
}
