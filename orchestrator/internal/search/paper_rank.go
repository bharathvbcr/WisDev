package search

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	paperRankRecencyWeight   = 0.45
	paperRankCitationWeight  = 0.30
	paperRankRelevanceWeight = 0.25
	paperRecencyHorizonYears = 12
	paperUnknownYearRecency  = 0.35
)

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

// PaperPreferenceScore blends recency, citations, and retrieval relevance.
func PaperPreferenceScore(p Paper) float64 {
	recency := RecencyNorm(p.Year)
	citations := CitationNorm(p.CitationCount)
	relevance := math.Max(0, p.Score)
	if relevance > 1 {
		relevance = 1
	}
	return paperRankRecencyWeight*recency +
		paperRankCitationWeight*citations +
		paperRankRelevanceWeight*relevance
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

// PaperPreferenceScoreWithQuery blends recency, citations, and retrieval relevance based on query intent.
func PaperPreferenceScoreWithQuery(p Paper, query string) float64 {
	lowerQuery := strings.ToLower(query)

	// Detect query intent
	isRecent := containsAny(lowerQuery, "recent", "new", "latest", "state of the art", "sota", "current", "trending", "2024", "2025", "2026", "modern", "recent advances", "recent progress", "newly")
	isCited := containsAny(lowerQuery, "classic", "foundational", "seminal", "most cited", "highly cited", "citation", "popular", "famous", "landmark", "influential", "pioneer", "key papers", "seminal work")
	isAuthorImpact := containsAny(lowerQuery, "h-index", "hindex", "h-factor", "h factor", "author impact", "prestigious", "h-value")

	recencyWeight := paperRankRecencyWeight
	citationWeight := paperRankCitationWeight
	relevanceWeight := paperRankRelevanceWeight
	authorImpactWeight := 0.0

	if isRecent {
		recencyWeight = 0.50
		citationWeight = 0.20
		relevanceWeight = 0.30
	} else if isCited {
		recencyWeight = 0.10
		citationWeight = 0.60
		relevanceWeight = 0.30
	} else if isAuthorImpact {
		recencyWeight = 0.20
		citationWeight = 0.30
		relevanceWeight = 0.20
		authorImpactWeight = 0.30
	}

	recency := RecencyNorm(p.Year)
	citations := CitationNorm(p.CitationCount)
	relevance := math.Max(0, p.Score)
	if relevance > 1 {
		relevance = 1
	}

	const maxInfluential = 500.0
	inf := math.Min(float64(p.InfluentialCitationCount), maxInfluential)
	authorImpact := math.Log1p(inf) / math.Log1p(maxInfluential)

	return recencyWeight*recency +
		citationWeight*citations +
		relevanceWeight*relevance +
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

