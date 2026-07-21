package paper

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
)

// ExportOptions defines document formatting preferences.
type ExportOptions struct {
	CitationStyle string `json:"citation_style"`
	PaperSize     string `json:"paper_size"`
}

// ExportRequest matches the frontend export request structure.
type ExportRequest struct {
	DraftID string          `json:"draft_id"`
	Content DocumentContent `json:"content"`
	Options ExportOptions   `json:"options"`
}

type DocumentContent struct {
	Title      string            `json:"title"`
	Sections   []Section         `json:"sections"`
	References []ExportReference `json:"references,omitempty"`
}

type Section struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ExportReference is a bibliography entry for export rendering.
type ExportReference struct {
	Authors []string `json:"authors,omitempty"`
	Year    int      `json:"year,omitempty"`
	Title   string   `json:"title"`
	Journal string   `json:"journal,omitempty"`
	DOI     string   `json:"doi,omitempty"`
	Link    string   `json:"link,omitempty"`
}

func (r ExportReference) toCitationEntry(id string) citations.Entry {
	return citations.EntryFromReference(id, r.Authors, r.Year, r.Title, r.Journal, r.Link, r.DOI)
}

func resolveExportStyle(opts ExportOptions) citations.Style {
	style, err := citations.ParseStyle(opts.CitationStyle)
	if err != nil {
		return citations.StyleAPA
	}
	return style
}

func bibliographyLines(refs []ExportReference, style citations.Style, format citations.OutputFormat) []string {
	if len(refs) == 0 {
		return nil
	}
	entries := make([]citations.Entry, 0, len(refs))
	for i, ref := range refs {
		id := fmt.Sprintf("ref%d", i+1)
		entries = append(entries, ref.toCitationEntry(id))
	}
	return citations.FormatBibliography(entries, style, format)
}

// GenerateMarkdown creates a markdown string from document content.
func GenerateMarkdown(req ExportRequest) string {
	style := resolveExportStyle(req.Options)
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ntitle: %q\ncitation_style: %q\ndate: %q\n---\n\n", req.Content.Title, style, "2026-03-31")
	fmt.Fprintf(&sb, "# %s\n\n", req.Content.Title)

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "## %s\n\n%s\n\n", sec.Name, sec.Content)
	}

	if lines := bibliographyLines(req.Content.References, style, citations.FormatMarkdown); len(lines) > 0 {
		sb.WriteString("## References\n\n")
		for i, line := range lines {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, line)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// GenerateHTML creates a basic HTML document from document content.
func GenerateHTML(req ExportRequest) string {
	style := resolveExportStyle(req.Options)
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html>\n<head>\n")
	fmt.Fprintf(&sb, "    <title>%s</title>\n", citations.EscapeForHTML(req.Content.Title))
	sb.WriteString("    <style>\n        body { font-family: serif; max-width: 800px; margin: 0 auto; padding: 2rem; }\n        h1 { color: #333; }\n    </style>\n")
	sb.WriteString("</head>\n<body>\n")
	fmt.Fprintf(&sb, "    <h1>%s</h1>\n", citations.EscapeForHTML(req.Content.Title))

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "    <h2>%s</h2>\n    <p>%s</p>\n", citations.EscapeForHTML(sec.Name), citations.EscapeForHTMLParagraphs(sec.Content))
	}

	if lines := bibliographyLines(req.Content.References, style, citations.FormatHTML); len(lines) > 0 {
		sb.WriteString("    <h2>References</h2>\n    <ol>\n")
		for _, line := range lines {
			fmt.Fprintf(&sb, "        <li>%s</li>\n", line)
		}
		sb.WriteString("    </ol>\n")
	}

	sb.WriteString("</body>\n</html>")
	return sb.String()
}

// GenerateLaTeX creates a basic LaTeX document from document content.
func GenerateLaTeX(req ExportRequest) string {
	style := resolveExportStyle(req.Options)
	var sb strings.Builder
	sb.WriteString("\\documentclass{article}\n\\usepackage[utf8]{inputenc}\n\\begin{document}\n")
	fmt.Fprintf(&sb, "\\title{%s}\n\\maketitle\n\n", latexEscapeExport(req.Content.Title))

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "\\section{%s}\n%s\n\n", latexEscapeExport(sec.Name), latexEscapeExport(sec.Content))
	}

	if lines := bibliographyLines(req.Content.References, style, citations.FormatLaTeX); len(lines) > 0 {
		sb.WriteString("\\begin{thebibliography}{99}\n")
		for i, line := range lines {
			fmt.Fprintf(&sb, "\\bibitem{ref%d} %s\n", i+1, line)
		}
		sb.WriteString("\\end{thebibliography}\n\n")
	}

	sb.WriteString("\\end{document}")
	return sb.String()
}

// GenerateDOCX converts an HTML document to DOCX bytes via pandoc.
func GenerateDOCX(html string) ([]byte, error) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		return nil, fmt.Errorf("docx export requires pandoc on PATH: %w", err)
	}
	tmp, err := os.CreateTemp("", "wisdev-export-*.html")
	if err != nil {
		return nil, err
	}
	htmlPath := tmp.Name()
	defer os.Remove(htmlPath)
	if _, err := tmp.WriteString(html); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	outPath := strings.TrimSuffix(htmlPath, ".html") + ".docx"
	defer os.Remove(outPath)
	cmd := exec.Command("pandoc", htmlPath, "-f", "html", "-t", "docx", "-o", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pandoc failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return os.ReadFile(outPath)
}

func latexEscapeExport(s string) string {
	var b strings.Builder
	for _, ch := range s {
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
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}
