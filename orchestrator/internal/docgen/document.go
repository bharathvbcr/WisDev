package docgen

import (
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/citations"
	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
	internalwisdev "github.com/bharathvbcr/wisdev-arc/orchestrator/internal/wisdev"
)

// Section is one titled block of prose in a generated document.
type Section struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	SourceIDs   []string `json:"sourceIds,omitempty"`
	CitationIDs []string `json:"citationIds,omitempty"`
}

// Reference is a bibliography entry attached to the document envelope.
type Reference struct {
	ID        string   `json:"id,omitempty"`
	Authors   []string `json:"authors,omitempty"`
	Year      int      `json:"year,omitempty"`
	Title     string   `json:"title"`
	Venue     string   `json:"venue,omitempty"`
	Link      string   `json:"link,omitempty"`
	DOI       string   `json:"doi,omitempty"`
	Citations int      `json:"citations,omitempty"`
	Preprint  bool     `json:"preprint,omitempty"`
	Number    int      `json:"number,omitempty"`
}

// Document is the canonical ScholarDoc envelope shared by CLI, TUI, and MCP.
type Document struct {
	Title         string                        `json:"title"`
	Intent        Intent                        `json:"intent"`
	Sections      []Section                     `json:"sections"`
	References    []Reference                   `json:"references,omitempty"`
	Sources       []search.Paper                `json:"sources,omitempty"`
	CitationStyle citations.Style               `json:"citationStyle"`
	Critique      map[string]any                `json:"critique,omitempty"`
	Visuals       []internalwisdev.VisualArtifact `json:"visuals,omitempty"`
	Pipeline      *internalwisdev.ManuscriptPipelineResult `json:"pipeline,omitempty"`
}

// ToCitationEntry converts a Reference to a citations.Entry for formatting.
func (r Reference) ToCitationEntry() citations.Entry {
	return citations.EntryFromReference(r.ID, r.Authors, r.Year, r.Title, r.Venue, r.Link, r.DOI)
}
