package search

import (
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	modernArxivID = regexp.MustCompile(`(?i)^\d{4}\.\d{4,5}(?:v\d+)?$`)
	legacyArxivID = regexp.MustCompile(`(?i)^[a-z.-]+/\d{7}(?:v\d+)?$`)
	arxivFromDOI  = regexp.MustCompile(`(?i)^10\.48550/arxiv\.(.+)$`)
)

// NormalizeDOI strips doi.org / doi: prefixes and query fragments.
func NormalizeDOI(value string) string {
	doi := strings.TrimSpace(value)
	if doi == "" {
		return ""
	}
	for i := 0; i < 2; i++ {
		if decoded, err := url.QueryUnescape(doi); err == nil && decoded != doi {
			doi = decoded
			continue
		}
		break
	}
	doi = strings.TrimSpace(doi)
	doi = regexp.MustCompile(`(?i)^https?://(dx\.)?doi\.org/`).ReplaceAllString(doi, "")
	doi = regexp.MustCompile(`(?i)^doi:\s*`).ReplaceAllString(doi, "")
	if idx := strings.IndexAny(doi, "?#"); idx >= 0 {
		doi = doi[:idx]
	}
	return strings.TrimSpace(doi)
}

// ExtractArxivIDFromDOI returns an arXiv id from a 10.48550/arXiv.* DOI.
func ExtractArxivIDFromDOI(doi string) string {
	normalized := NormalizeDOI(doi)
	m := arxivFromDOI.FindStringSubmatch(normalized)
	if len(m) < 2 {
		return ""
	}
	id := strings.TrimSpace(m[1])
	if modernArxivID.MatchString(id) || legacyArxivID.MatchString(id) {
		return id
	}
	return id
}

// IsLikelyRetractionProviderMissDOI skips OpenRetractions/Crossref for DOI
// families that routinely 404 (arXiv DOIs, LIPIcs/OASIcs, etc.).
func IsLikelyRetractionProviderMissDOI(doi string) bool {
	normalized := strings.ToLower(NormalizeDOI(doi))
	if normalized == "" {
		return true
	}
	if strings.HasPrefix(normalized, "10.48550/") {
		return true
	}
	if strings.HasPrefix(normalized, "10.4230/") {
		return true
	}
	return false
}

func academicPoliteEmail(envKeys ...string) string {
	for _, key := range envKeys {
		if email := strings.TrimSpace(os.Getenv(key)); email != "" {
			return email
		}
	}
	if email := strings.TrimSpace(os.Getenv("OPENALEX_EMAIL")); email != "" {
		return email
	}
	return "scholar.focus.app@gmail.com"
}
