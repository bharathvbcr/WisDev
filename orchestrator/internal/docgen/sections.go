package docgen

import (
	"regexp"
	"strings"
)

var markdownHeadingPattern = regexp.MustCompile(`(?m)^(#{1,3})\s+(.+)$`)

// splitMarkdownSections parses markdown headings into document sections.
func splitMarkdownSections(text string) []Section {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	matches := markdownHeadingPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []Section{{ID: "body", Title: "Body", Content: text}}
	}

	sections := make([]Section, 0, len(matches))
	for i, loc := range matches {
		level := text[loc[2]:loc[3]]
		title := strings.TrimSpace(text[loc[4]:loc[5]])
		contentStart := loc[1]
		contentEnd := len(text)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}
		content := strings.TrimSpace(text[contentStart:contentEnd])
		if strings.HasPrefix(content, level+" ") {
			content = strings.TrimSpace(content[len(level)+1+len(title):])
		}
		id := slugify(title)
		if id == "" {
			id = "section"
		}
		sections = append(sections, Section{ID: id, Title: title, Content: content})
	}
	return sections
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func truncateWords(text string, max int) string {
	words := strings.Fields(text)
	if len(words) <= max {
		return text
	}
	return strings.Join(words[:max], " ") + "…"
}
