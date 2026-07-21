package cli

import (
	"fmt"
	"regexp"
	"strings"

	agent "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"
)

var bibTeXKeySanitizer = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func formatPaperAuthors(authors []string, maxAuthors int) string {
	if maxAuthors <= 0 {
		maxAuthors = 3
	}
	clean := make([]string, 0, len(authors))
	for _, author := range authors {
		if trimmed := strings.TrimSpace(author); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	switch len(clean) {
	case 0:
		return "Unknown authors"
	case 1:
		return clean[0]
	case 2:
		return clean[0] + " & " + clean[1]
	default:
		if len(clean) > maxAuthors {
			return strings.Join(clean[:maxAuthors], ", ") + ", et al."
		}
		return strings.Join(clean, ", ")
	}
}

func formatPaperDOI(doi string) string {
	doi = strings.TrimSpace(doi)
	if doi == "" {
		return ""
	}
	if strings.HasPrefix(doi, "http://") || strings.HasPrefix(doi, "https://") {
		return doi
	}
	return "https://doi.org/" + strings.TrimPrefix(strings.TrimPrefix(doi, "doi:"), "DOI:")
}

func formatPaperBibliography(paper agent.Paper) string {
	segments := make([]string, 0, 5)
	authors := formatPaperAuthors(paper.Authors, 3)
	if paper.Year > 0 {
		segments = append(segments, fmt.Sprintf("%s (%d)", authors, paper.Year))
	} else {
		segments = append(segments, authors)
	}
	if title := strings.TrimSpace(paper.Title); title != "" {
		segments = append(segments, title)
	}
	if venue := strings.TrimSpace(paper.Venue); venue != "" {
		segments = append(segments, venue)
	}
	if paper.CitationCount > 0 {
		segments = append(segments, fmt.Sprintf("Citations: %d", paper.CitationCount))
	}
	if doi := formatPaperDOI(paper.DOI); doi != "" {
		segments = append(segments, doi)
	} else if link := paperSourceURL(paper); link != "" {
		segments = append(segments, link)
	}
	return strings.Join(segments, ". ")
}

// formatPaperBibliographyTerminal renders the bibliography line for terminal
// output with the title wrapped in an OSC-8 hyperlink when a source URL is
// available. Plain mode keeps the unstyled bibliography (which already carries
// the raw DOI/URL as its own segment).
func formatPaperBibliographyTerminal(paper agent.Paper) string {
	bib := formatPaperBibliography(paper)
	title := strings.TrimSpace(paper.Title)
	link := paperSourceURL(paper)
	if title == "" || link == "" || plainUI() {
		return bib
	}
	return strings.Replace(bib, title, terminalHyperlink(link, title), 1)
}

func bibTeXKey(paper agent.Paper, index int) string {
	author := "unknown"
	if len(paper.Authors) > 0 {
		author = paper.Authors[0]
		if idx := strings.IndexByte(author, ' '); idx > 0 {
			author = author[idx+1:]
		}
	}
	author = strings.ToLower(bibTeXKeySanitizer.ReplaceAllString(author, ""))
	titleWord := ""
	for _, word := range strings.Fields(strings.TrimSpace(paper.Title)) {
		clean := strings.ToLower(bibTeXKeySanitizer.ReplaceAllString(word, ""))
		if len(clean) > 2 {
			titleWord = clean
			break
		}
	}
	year := paper.Year
	if year <= 0 {
		year = 0
	}
	if author == "" {
		author = "source"
	}
	if titleWord == "" {
		titleWord = "paper"
	}
	return fmt.Sprintf("%s%d%s%d", author, year, titleWord, index+1)
}

func formatPaperBibTeX(paper agent.Paper, index int) string {
	key := bibTeXKey(paper, index)
	entryType := "article"
	if strings.Contains(strings.ToLower(paper.Venue), "conference") || strings.Contains(strings.ToLower(paper.Source), "arxiv") {
		entryType = "inproceedings"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "@%s{%s,\n", entryType, key)
	if title := strings.TrimSpace(paper.Title); title != "" {
		fmt.Fprintf(&b, "  title = {%s},\n", bibTeXProtectTitle(title))
	}
	if len(paper.Authors) > 0 {
		fmt.Fprintf(&b, "  author = {%s},\n", bibTeXEscape(strings.Join(paper.Authors, " and ")))
	}
	if paper.Year > 0 {
		fmt.Fprintf(&b, "  year = {%d},\n", paper.Year)
	}
	if venue := strings.TrimSpace(paper.Venue); venue != "" {
		field := "journal"
		if entryType == "inproceedings" {
			field = "booktitle"
		}
		fmt.Fprintf(&b, "  %s = {%s},\n", field, bibTeXEscape(venue))
	}
	if doi := strings.TrimSpace(paper.DOI); doi != "" {
		doi = strings.TrimPrefix(strings.TrimPrefix(doi, "https://doi.org/"), "doi:")
		fmt.Fprintf(&b, "  doi = {%s},\n", bibTeXEscape(doi))
	}
	noteParts := make([]string, 0, 2)
	if source := strings.TrimSpace(paper.Source); source != "" {
		noteParts = append(noteParts, "Source: "+source)
	}
	if paper.CitationCount > 0 {
		noteParts = append(noteParts, fmt.Sprintf("Citations: %d", paper.CitationCount))
	}
	if len(noteParts) > 0 {
		fmt.Fprintf(&b, "  note = {%s},\n", bibTeXEscape(strings.Join(noteParts, " · ")))
	}
	if link := paperSourceURL(paper); link != "" {
		fmt.Fprintf(&b, "  url = {%s},\n", bibTeXEscape(link))
	}
	b.WriteString("}\n")
	return b.String()
}

func bibTeXEscape(value string) string {
	value = strings.ReplaceAll(value, "{", "\\{")
	value = strings.ReplaceAll(value, "}", "\\}")
	return value
}

// bibTeXProtectTitle escapes a title and wraps words containing capital
// letters in braces so BibTeX styles cannot lowercase acronyms or proper
// nouns (e.g. "DNA repair in Mice" -> "{DNA} repair in {Mice}").
func bibTeXProtectTitle(title string) string {
	words := strings.Fields(bibTeXEscape(strings.TrimSpace(title)))
	for idx, word := range words {
		if strings.ContainsAny(word, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && !strings.HasPrefix(word, "{") {
			words[idx] = "{" + word + "}"
		}
	}
	return strings.Join(words, " ")
}

func formatPaperCitationLine(paper agent.Paper) string {
	parts := []string{formatPaperAuthors(paper.Authors, 2)}
	if paper.Year > 0 {
		parts = append(parts, fmt.Sprintf("(%d)", paper.Year))
	}
	if paper.CitationCount > 0 {
		parts = append(parts, fmt.Sprintf("Citations: %d", paper.CitationCount))
	} else {
		parts = append(parts, "Citations: n/a")
	}
	if venue := strings.TrimSpace(paper.Venue); venue != "" {
		parts = append(parts, venue)
	}
	return strings.Join(parts, " · ")
}
