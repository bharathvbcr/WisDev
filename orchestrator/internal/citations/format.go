package citations

import (
	"fmt"
	"strings"
)

type parsedAuthor struct {
	first string
	last  string
}

func parseAuthor(author string) parsedAuthor {
	trimmed := strings.TrimSpace(author)
	if trimmed == "" {
		return parsedAuthor{last: "Unknown"}
	}
	if strings.Contains(trimmed, ",") {
		parts := strings.SplitN(trimmed, ",", 2)
		return parsedAuthor{last: strings.TrimSpace(parts[0]), first: strings.TrimSpace(parts[1])}
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 1 {
		return parsedAuthor{last: parts[0]}
	}
	last := parts[len(parts)-1]
	return parsedAuthor{first: strings.Join(parts[:len(parts)-1], " "), last: last}
}

func initials(first string, dotted bool) string {
	parts := strings.Fields(first)
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteRune([]rune(p)[0])
		if dotted {
			b.WriteString(". ")
		}
	}
	return strings.TrimSpace(b.String())
}

// FormatAuthors renders author names for a bibliography entry.
func FormatAuthors(authors []string, style Style, useEtAl bool) string {
	if len(authors) == 0 {
		return ""
	}

	maxAuthors := map[Style]int{
		StyleAPA: 20, StyleMLA: 3, StyleChicago: 10, StyleVancouver: 6,
		StyleIEEE: 6, StyleHarvard: 3, StyleNature: 5,
	}
	etAlThreshold := map[Style]int{
		StyleAPA: 21, StyleMLA: 4, StyleChicago: 11, StyleVancouver: 7,
		StyleIEEE: 7, StyleHarvard: 4, StyleNature: 6,
	}

	parsed := make([]parsedAuthor, len(authors))
	for i, a := range authors {
		parsed[i] = parseAuthor(a)
	}

	formatSingle := func(author parsedAuthor, index int) string {
		switch style {
		case StyleAPA:
			return fmt.Sprintf("%s, %s", author.last, initials(author.first, true))
		case StyleMLA, StyleChicago:
			if index == 0 {
				return fmt.Sprintf("%s, %s", author.last, author.first)
			}
			return fmt.Sprintf("%s %s", author.first, author.last)
		case StyleVancouver:
			return fmt.Sprintf("%s %s", author.last, initials(author.first, false))
		case StyleIEEE:
			return fmt.Sprintf("%s %s", initials(author.first, true), author.last)
		case StyleHarvard:
			return fmt.Sprintf("%s, %s", author.last, initials(author.first, true))
		case StyleNature:
			return fmt.Sprintf("%s, %s", author.last, initials(author.first, true))
		default:
			return fmt.Sprintf("%s %s", author.first, author.last)
		}
	}

	if useEtAl && len(authors) > etAlThreshold[style] {
		shown := parsed[:maxAuthors[style]]
		formatted := make([]string, len(shown))
		for i, a := range shown {
			formatted[i] = formatSingle(a, i)
		}
		return strings.Join(formatted, ", ") + ", et al."
	}

	formatted := make([]string, len(parsed))
	for i, a := range parsed {
		formatted[i] = formatSingle(a, i)
	}

	if len(formatted) == 1 {
		return formatted[0]
	}
	if len(formatted) == 2 {
		conj := map[Style]string{
			StyleAPA: " & ", StyleMLA: " and ", StyleChicago: " and ",
			StyleVancouver: ", ", StyleIEEE: " and ", StyleHarvard: " & ", StyleNature: " & ",
		}
		return formatted[0] + conj[style] + formatted[1]
	}
	lastSep := map[Style]string{
		StyleAPA: ", & ", StyleMLA: ", and ", StyleChicago: ", and ",
		StyleVancouver: ", ", StyleIEEE: ", and ", StyleHarvard: ", & ", StyleNature: " & ",
	}
	return strings.Join(formatted[:len(formatted)-1], ", ") + lastSep[style] + formatted[len(formatted)-1]
}

// FormatInline renders an in-text citation marker.
func FormatInline(entry Entry, style Style, number int) string {
	firstAuthor := "Unknown"
	if len(entry.Authors) > 0 {
		firstAuthor = strings.FieldsFunc(entry.Authors[0], func(r rune) bool { return r == ',' || r == ' ' })[0]
	}
	etAl := ""
	if len(entry.Authors) > 2 {
		etAl = " et al."
	}
	and := ""
	if len(entry.Authors) == 2 {
		second := strings.FieldsFunc(entry.Authors[1], func(r rune) bool { return r == ',' || r == ' ' })[0]
		and = " & " + second
	}

	switch style {
	case StyleAPA:
		return fmt.Sprintf("(%s%s%s, %d)", firstAuthor, and, etAl, entry.Year)
	case StyleMLA:
		return fmt.Sprintf("(%s%s%s)", firstAuthor, and, etAl)
	case StyleChicago, StyleHarvard:
		return fmt.Sprintf("(%s%s%s %d)", firstAuthor, and, etAl, entry.Year)
	case StyleVancouver, StyleIEEE:
		if number > 0 {
			return fmt.Sprintf("[%d]", number)
		}
		return "[?]"
	case StyleNature:
		if number > 0 {
			return fmt.Sprintf("<sup>%d</sup>", number)
		}
		return "<sup>?</sup>"
	default:
		return fmt.Sprintf("(%s, %d)", firstAuthor, entry.Year)
	}
}

func escapeText(text string, format OutputFormat) string {
	switch format {
	case FormatHTML:
		return strings.NewReplacer(
			"&", "&amp;", "<", "&lt;", ">", "&gt;",
			"\"", "&quot;", "'", "&#39;",
		).Replace(text)
	case FormatLaTeX:
		var b strings.Builder
		for _, ch := range text {
			switch ch {
			case '\\':
				b.WriteString("\\textbackslash{}")
			case '&':
				b.WriteString("\\&")
			case '%':
				b.WriteString("\\%")
			case '$':
				b.WriteString("\\$")
			case '#':
				b.WriteString("\\#")
			case '_':
				b.WriteString("\\_")
			case '{':
				b.WriteString("\\{")
			case '}':
				b.WriteString("\\}")
			case '~':
				b.WriteString("\\textasciitilde{}")
			case '^':
				b.WriteString("\\textasciicircum{}")
			default:
				b.WriteRune(ch)
			}
		}
		return b.String()
	default:
		return text
	}
}

func italic(text string, format OutputFormat) string {
	switch format {
	case FormatHTML:
		return "<em>" + text + "</em>"
	case FormatLaTeX:
		return "\\emph{" + text + "}"
	case FormatMarkdown:
		return "*" + text + "*"
	default:
		return text
	}
}

func doiLink(doi string, format OutputFormat) string {
	doi = strings.TrimSpace(doi)
	if doi == "" {
		return ""
	}
	url := "https://doi.org/" + doi
	switch format {
	case FormatHTML:
		safe := escapeText(doi, FormatHTML)
		return fmt.Sprintf(`<a href="https://doi.org/%s">https://doi.org/%s</a>`, safe, safe)
	case FormatMarkdown:
		return fmt.Sprintf("[%s](%s)", url, url)
	default:
		return url
	}
}

// FormatEntry renders one bibliography line in the requested style and output format.
func FormatEntry(entry Entry, style Style, format OutputFormat, number int) string {
	authors := FormatAuthors(entry.Authors, style, true)
	authors = escapeText(authors, format)
	title := escapeText(strings.TrimSpace(entry.Title), format)
	journal := escapeText(strings.TrimSpace(entry.Journal), format)
	volume := strings.TrimSpace(entry.Volume)
	issue := strings.TrimSpace(entry.Issue)
	pages := strings.TrimSpace(entry.Pages)
	doi := strings.TrimSpace(entry.DOI)
	link := doiLink(doi, format)
	year := entry.Year

	var out string
	switch style {
	case StyleAPA:
		out = fmt.Sprintf("%s (%d). %s.", authors, year, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += ", " + italic(volume, format)
			}
			if issue != "" {
				out += "(" + issue + ")"
			}
			if pages != "" {
				out += ", " + pages
			}
			out += "."
		}
		if link != "" {
			out += " " + link
		}
	case StyleMLA:
		out = fmt.Sprintf("%s. \"%s.\"", authors, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += ", vol. " + volume
			}
			if issue != "" {
				out += ", no. " + issue
			}
			out += fmt.Sprintf(", %d", year)
			if pages != "" {
				out += ", pp. " + pages
			}
			out += "."
		} else {
			out += fmt.Sprintf(" %d.", year)
		}
		if link != "" {
			out += " " + link
		}
	case StyleChicago:
		out = fmt.Sprintf("%s. \"%s.\"", authors, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += " " + volume
			}
			if issue != "" {
				out += ", no. " + issue
			}
			out += fmt.Sprintf(" (%d)", year)
			if pages != "" {
				out += ": " + pages
			}
			out += "."
		} else {
			out += fmt.Sprintf(" %d.", year)
		}
		if link != "" {
			out += " " + link + "."
		}
	case StyleVancouver:
		prefix := ""
		if number > 0 {
			prefix = fmt.Sprintf("[%d] ", number)
		}
		out = fmt.Sprintf("%s%s. %s.", prefix, authors, title)
		if journal != "" {
			out += " " + journal + fmt.Sprintf(". %d", year)
			if volume != "" {
				out += ";" + volume
			}
			if issue != "" {
				out += "(" + issue + ")"
			}
			if pages != "" {
				out += ":" + pages
			}
			out += "."
		} else {
			out += fmt.Sprintf(" %d.", year)
		}
		if doi != "" {
			out += " doi:" + doi
		}
	case StyleIEEE:
		prefix := ""
		if number > 0 {
			prefix = fmt.Sprintf("[%d] ", number)
		}
		out = fmt.Sprintf("%s%s, \"%s,\"", prefix, authors, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += ", vol. " + volume
			}
			if issue != "" {
				out += ", no. " + issue
			}
			if pages != "" {
				out += ", pp. " + pages
			}
			out += fmt.Sprintf(", %d.", year)
		} else {
			out += fmt.Sprintf(" %d.", year)
		}
		if link != "" {
			out += " " + link
		}
	case StyleHarvard:
		out = fmt.Sprintf("%s (%d) %s.", authors, year, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += ", " + volume
			}
			if issue != "" {
				out += "(" + issue + ")"
			}
			if pages != "" {
				out += ", pp. " + pages
			}
			out += "."
		}
		if link != "" {
			out += " Available at: " + link
		}
	case StyleNature:
		prefix := ""
		if number > 0 {
			prefix = fmt.Sprintf("%d. ", number)
		}
		out = fmt.Sprintf("%s%s. %s.", prefix, authors, title)
		if journal != "" {
			out += " " + italic(journal, format)
			if volume != "" {
				out += " " + italic(volume, format)
			}
			if pages != "" {
				out += ", " + pages
			}
			out += fmt.Sprintf(" (%d).", year)
		} else {
			out += fmt.Sprintf(" (%d).", year)
		}
		if doi != "" {
			out += " https://doi.org/" + doi
		}
	default:
		out = fmt.Sprintf("%s (%d). %s.", authors, year, title)
		if journal != "" {
			out += " " + journal + "."
		}
	}
	return out
}

// EscapeForHTML escapes text for HTML body content.
func EscapeForHTML(text string) string {
	return escapeText(text, FormatHTML)
}

// EscapeForHTMLParagraphs converts plain text paragraphs to HTML <p> blocks.
func EscapeForHTMLParagraphs(text string) string {
	parts := strings.Split(strings.TrimSpace(text), "\n\n")
	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fmt.Fprintf(&b, "<p>%s</p>", EscapeForHTML(part))
	}
	return b.String()
}

func FormatBibliography(entries []Entry, style Style, format OutputFormat) []string {
	out := make([]string, 0, len(entries))
	for i, entry := range entries {
		out = append(out, FormatEntry(entry, style, format, i+1))
	}
	return out
}

// EntryFromReference converts a docgen-style reference into a citations.Entry.
func EntryFromReference(id string, authors []string, year int, title, journal, link, doi string) Entry {
	if doi == "" && strings.Contains(strings.ToLower(link), "doi.org") {
		doi = strings.TrimPrefix(strings.TrimPrefix(link, "https://doi.org/"), "http://doi.org/")
	}
	return Entry{
		ID:      id,
		Authors: authors,
		Year:    year,
		Title:   title,
		Journal: journal,
		DOI:     doi,
		URL:     link,
	}
}
