package search

import "strings"

func appendUniqueAuthor(authors []string, seen map[string]struct{}, raw string) []string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return authors
	}

	key := strings.ToLower(name)
	if _, ok := seen[key]; ok {
		return authors
	}

	seen[key] = struct{}{}
	return append(authors, name)
}

func parseDelimitedAuthors(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}

	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ';' || r == ',' || r == '|' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return nil
	}

	authors := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		authors = appendUniqueAuthor(authors, seen, field)
	}
	if len(authors) == 0 {
		return nil
	}
	return authors
}

func parseDBLPAuthors(value any) []string {
	if value == nil {
		return nil
	}

	authors := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)

	var collect func(any)
	collect = func(raw any) {
		switch typed := raw.(type) {
		case string:
			authors = appendUniqueAuthor(authors, seen, typed)
		case []string:
			for _, item := range typed {
				authors = appendUniqueAuthor(authors, seen, item)
			}
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			if text, ok := typed["text"].(string); ok {
				authors = appendUniqueAuthor(authors, seen, text)
				return
			}
			if text, ok := typed["#text"].(string); ok {
				authors = appendUniqueAuthor(authors, seen, text)
				return
			}
			if name, ok := typed["name"].(string); ok {
				authors = appendUniqueAuthor(authors, seen, name)
			}
		}
	}

	collect(value)
	if len(authors) == 0 {
		return nil
	}
	return authors
}

func countNonEmptyStrings(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}
