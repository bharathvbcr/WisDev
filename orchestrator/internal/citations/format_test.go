package citations

import (
	"strings"
	"testing"
)

func sampleEntry() Entry {
	return Entry{
		Authors: []string{"Smith, John", "Jones, Mary"},
		Year:    2024,
		Title:   "Deep Learning for Science",
		Journal: "Nature",
		DOI:     "10.1038/s41586-024-00001",
	}
}

func TestParseStyle(t *testing.T) {
	styles := []Style{StyleAPA, StyleMLA, StyleChicago, StyleVancouver, StyleIEEE, StyleHarvard, StyleNature}
	for _, want := range styles {
		got, err := ParseStyle(string(want))
		if err != nil {
			t.Fatalf("ParseStyle(%q) error: %v", want, err)
		}
		if got != want {
			t.Errorf("ParseStyle(%q)=%q", want, got)
		}
	}
	if _, err := ParseStyle("bibtex"); err == nil {
		t.Fatal("expected error for unknown style")
	}
}

func TestFormatEntryAllStyles(t *testing.T) {
	entry := sampleEntry()
	styles := []Style{StyleAPA, StyleMLA, StyleChicago, StyleVancouver, StyleIEEE, StyleHarvard, StyleNature}
	for _, style := range styles {
		t.Run(string(style), func(t *testing.T) {
			line := FormatEntry(entry, style, FormatPlain, 1)
			if line == "" {
				t.Fatal("empty bibliography line")
			}
			if !strings.Contains(line, "Deep Learning for Science") {
				t.Errorf("missing title in %q line: %s", style, line)
			}
		})
	}
}

func TestFormatBibliographyMarkdown(t *testing.T) {
	lines := FormatBibliography([]Entry{sampleEntry()}, StyleAPA, FormatMarkdown)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "*Nature*") {
		t.Errorf("expected italic journal in markdown: %s", lines[0])
	}
}

func TestFormatBibliographyHTML(t *testing.T) {
	lines := FormatBibliography([]Entry{sampleEntry()}, StyleAPA, FormatHTML)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "<em>Nature</em>") {
		t.Errorf("expected em journal in HTML: %s", lines[0])
	}
}

func TestFormatBibliographyLaTeX(t *testing.T) {
	lines := FormatBibliography([]Entry{sampleEntry()}, StyleAPA, FormatLaTeX)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "\\emph{") {
		t.Errorf("expected latex emph: %s", lines[0])
	}
}

func TestFormatInlineVancouver(t *testing.T) {
	marker := FormatInline(sampleEntry(), StyleVancouver, 3)
	if marker != "[3]" {
		t.Errorf("vancouver inline=%q want [3]", marker)
	}
}

func TestFormatInlineAPA(t *testing.T) {
	marker := FormatInline(sampleEntry(), StyleAPA, 0)
	if !strings.Contains(marker, "Smith") || !strings.Contains(marker, "2024") {
		t.Errorf("apa inline=%q", marker)
	}
}

func TestFormatAuthorsEtAl(t *testing.T) {
	authors := []string{"A One", "B Two", "C Three", "D Four", "E Five"}
	line := FormatAuthors(authors, StyleMLA, true)
	if !strings.Contains(line, "et al.") {
		t.Errorf("expected et al. in MLA with 5 authors: %s", line)
	}
}

func TestEntryFromReference(t *testing.T) {
	e := EntryFromReference("id1", []string{"Lee"}, 2023, "Title", "Journal", "https://doi.org/10.1234/x", "")
	if e.DOI != "10.1234/x" {
		t.Errorf("doi=%q", e.DOI)
	}
	if e.Journal != "Journal" {
		t.Errorf("journal=%q", e.Journal)
	}
}
