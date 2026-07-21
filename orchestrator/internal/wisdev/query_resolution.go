package wisdev

const sessionQueryPreviewLimit = 96

func ResolveSessionSearchQuery(query string, correctedQuery string, originalQuery string) string {
	if resolved := normalizeSearchQuery(query); resolved != "" {
		return resolved
	}
	return ResolveSessionQueryText(correctedQuery, originalQuery)
}

func ResolveSessionQueryText(correctedQuery string, originalQuery string) string {
	if query := normalizeSearchQuery(correctedQuery); query != "" {
		return query
	}
	return normalizeSearchQuery(originalQuery)
}

func QueryPreview(query string) string {
	normalized := normalizeSearchQuery(query)
	if len(normalized) <= sessionQueryPreviewLimit {
		return normalized
	}
	return normalized[:sessionQueryPreviewLimit-3] + "..."
}
