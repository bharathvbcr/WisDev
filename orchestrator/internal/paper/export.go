package paper

import (
	"fmt"
	"strings"
)

// ExportOptions defines document formatting preferences.
type ExportOptions struct {
	CitationStyle string `json:"citation_style"`
	PaperSize     string `json:"paper_size"`
}

// ExportRequest matches the frontend export request structure.
type ExportRequest struct {
	DraftID string        `json:"draft_id"`
	Content DocumentContent `json:"content"`
	Options ExportOptions `json:"options"`
}

type DocumentContent struct {
	Title    string `json:"title"`
	Sections []Section `json:"sections"`
}

type Section struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// GenerateMarkdown creates a markdown string from document content.
func GenerateMarkdown(req ExportRequest) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ntitle: %q\ndate: %q\n---\n\n", req.Content.Title, "2026-03-31")
	fmt.Fprintf(&sb, "# %s\n\n", req.Content.Title)

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "## %s\n\n%s\n\n", sec.Name, sec.Content)
	}

	return sb.String()
}

// GenerateHTML creates a basic HTML document from document content.
func GenerateHTML(req ExportRequest) string {
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html>\n<head>\n")
	fmt.Fprintf(&sb, "    <title>%s</title>\n", req.Content.Title)
	sb.WriteString("    <style>\n        body { font-family: serif; max-width: 800px; margin: 0 auto; padding: 2rem; }\n        h1 { color: #333; }\n    </style>\n")
	sb.WriteString("</head>\n<body>\n")
	fmt.Fprintf(&sb, "    <h1>%s</h1>\n", req.Content.Title)

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "    <h2>%s</h2>\n    <p>%s</p>\n", sec.Name, sec.Content)
	}

	sb.WriteString("</body>\n</html>")
	return sb.String()
}

// GenerateLaTeX creates a basic LaTeX document from document content.
func GenerateLaTeX(req ExportRequest) string {
	var sb strings.Builder
	sb.WriteString("\\documentclass{article}\n\\begin{document}\n")
	fmt.Fprintf(&sb, "\\title{%s}\n\\maketitle\n\n", req.Content.Title)

	for _, sec := range req.Content.Sections {
		fmt.Fprintf(&sb, "\\section{%s}\n%s\n\n", sec.Name, sec.Content)
	}

	sb.WriteString("\\end{document}")
	return sb.String()
}
