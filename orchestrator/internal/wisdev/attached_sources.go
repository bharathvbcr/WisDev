package wisdev

import (
	"strconv"
	"strings"

	"github.com/bharathvbcr/wisdev-arc/orchestrator/internal/search"
)

// Attached sources are papers and resources a user uploads (extracted PDFs) or
// connects (existing library / search results) and hands to an agent run as
// pre-seeded context. They flow into both agents:
//
//   - WisDev: persisted on the session at initialize time and seeded into
//     LoopRequest.InitialPapers so the autonomous research loop admits them
//     before any live retrieval.
//   - DocGen (wisdev-arc full-paper): passed through as metadata.papers, which
//     full_paper_routes.extractFullPaperStartPapers already consumes.
//
// The canonical wire shape is search.Paper JSON (title, abstract, doi, link,
// authors []string, year, fullText, sourceApis). To stay resilient to the
// frontend Source shape the converter below also tolerates author objects
// ({"name": ...}) and a nested publishDate.year.

// attachedSourceMaxPapers bounds how many attached sources a single run admits
// so a malformed or oversized client payload cannot blow up downstream memory
// or token budgets.
const attachedSourceMaxPapers = 200

// attachedProvenanceMarkers are the sourceApis / source values that mark a paper
// as user-provided: uploaded PDFs, connected library/search papers, and the
// generic default applied by the attached-sources converter. Used to compute
// provenance ("which of the user's papers reached the output") on results.
var attachedProvenanceMarkers = map[string]struct{}{
	"pdf_upload": {},
	"connected":  {},
	"attached":   {},
}

// IsAttachedSourceProvenance reports whether a paper originated from a user
// attachment, based on its source/sourceApis provenance markers.
func IsAttachedSourceProvenance(source string, sourceApis []string) bool {
	if _, ok := attachedProvenanceMarkers[strings.ToLower(strings.TrimSpace(source))]; ok {
		return true
	}
	for _, api := range sourceApis {
		if _, ok := attachedProvenanceMarkers[strings.ToLower(strings.TrimSpace(api))]; ok {
			return true
		}
	}
	return false
}

// CountAttachedSourcesUsed returns how many distinct papers in the result set
// carry user-attachment provenance — i.e. how many of the user's uploaded or
// connected papers reached the research output.
func CountAttachedSourcesUsed(papers []search.Paper) int {
	seen := make(map[string]struct{})
	count := 0
	for _, paper := range papers {
		if !IsAttachedSourceProvenance(paper.Source, paper.SourceApis) {
			continue
		}
		key := strings.TrimSpace(paper.ID)
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(paper.Title))
		}
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		count++
	}
	return count
}

// AttachedSourcePapers converts a raw attached-sources payload (an array of
// source maps as decoded from JSON) into search.Paper values. Entries without a
// usable title are dropped. It tolerates both the canonical search.Paper shape
// and the richer frontend Source shape.
func AttachedSourcePapers(raw any) []search.Paper {
	items := toAnySlice(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]search.Paper, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		paper, ok := attachedSourceToPaper(m)
		if !ok {
			continue
		}
		out = append(out, paper)
		if len(out) >= attachedSourceMaxPapers {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeAttachedSources returns a cleaned, bounded []map[string]any suitable
// for persisting on a session document. It mirrors AttachedSourcePapers so what
// is stored is exactly what would later be admitted into the loop.
func NormalizeAttachedSources(raw any) []map[string]any {
	papers := AttachedSourcePapers(raw)
	if len(papers) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(papers))
	for _, paper := range papers {
		entry := map[string]any{
			"id":    paper.ID,
			"title": paper.Title,
		}
		if paper.Abstract != "" {
			entry["abstract"] = paper.Abstract
		}
		if paper.Link != "" {
			entry["link"] = paper.Link
		}
		if paper.DOI != "" {
			entry["doi"] = paper.DOI
		}
		if len(paper.Authors) > 0 {
			entry["authors"] = paper.Authors
		}
		if paper.Year > 0 {
			entry["year"] = paper.Year
		}
		if len(paper.SourceApis) > 0 {
			entry["sourceApis"] = paper.SourceApis
		}
		if paper.FullText != "" {
			entry["hasFullText"] = true
		}
		out = append(out, entry)
	}
	return out
}

// AttachedSourceList converts a raw attached-sources payload into wisdev.Source
// values (the type the plan executor and research loop reason over). It reuses
// AttachedSourcePapers so the search.Paper and Source views stay in lockstep.
func AttachedSourceList(raw any) []Source {
	return searchPapersToLoopSources(AttachedSourcePapers(raw))
}

func attachedSourceToPaper(m map[string]any) (search.Paper, bool) {
	title := strings.TrimSpace(AsOptionalString(m["title"]))
	if title == "" {
		return search.Paper{}, false
	}
	id := strings.TrimSpace(AsOptionalString(m["id"]))
	if id == "" {
		id = strings.TrimSpace(AsOptionalString(m["paperId"]))
	}
	sourceApis := attachedSourceStringSlice(m["sourceApis"])
	if len(sourceApis) == 0 {
		// Default provenance so downstream evidence accounting can tell these
		// apart from live-retrieved papers.
		sourceApis = []string{"attached"}
	}
	source := strings.TrimSpace(AsOptionalString(m["source"]))
	if source == "" {
		source = sourceApis[0]
	}
	paper := search.Paper{
		ID:            id,
		Title:         title,
		Abstract:      strings.TrimSpace(firstNonEmpty(AsOptionalString(m["abstract"]), AsOptionalString(m["summary"]))),
		Link:          strings.TrimSpace(firstNonEmpty(AsOptionalString(m["link"]), AsOptionalString(m["url"]))),
		DOI:           strings.TrimSpace(AsOptionalString(m["doi"])),
		ArxivID:       strings.TrimSpace(AsOptionalString(m["arxivId"])),
		Source:        source,
		SourceApis:    sourceApis,
		Authors:       attachedSourceAuthors(m["authors"]),
		Keywords:      attachedSourceStringSlice(m["keywords"]),
		Year:          attachedSourceYear(m),
		OpenAccessUrl: strings.TrimSpace(AsOptionalString(m["openAccessUrl"])),
		PdfUrl:        strings.TrimSpace(firstNonEmpty(AsOptionalString(m["pdfUrl"]), AsOptionalString(m["pdf_url"]))),
		FullText:      strings.TrimSpace(firstNonEmpty(AsOptionalString(m["fullText"]), AsOptionalString(m["full_text"]))),
	}
	paper.CitationCount = toInt(m["citationCount"])
	paper.ReferenceCount = toInt(m["referenceCount"])
	return paper, true
}

// attachedSourceAuthors accepts either []string, []any of strings, or the
// frontend Source author-object shape ([]{"name": ...}).
func attachedSourceAuthors(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return trimmedNonEmpty(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			switch a := item.(type) {
			case string:
				if s := strings.TrimSpace(a); s != "" {
					out = append(out, s)
				}
			case map[string]any:
				if s := strings.TrimSpace(AsOptionalString(a["name"])); s != "" {
					out = append(out, s)
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// attachedSourceYear reads a flat year field, falling back to publishDate.year.
func attachedSourceYear(m map[string]any) int {
	if y := toInt(m["year"]); y > 0 {
		return y
	}
	if s := strings.TrimSpace(AsOptionalString(m["year"])); s != "" {
		if y, err := strconv.Atoi(s); err == nil {
			return y
		}
	}
	if pd, ok := m["publishDate"].(map[string]any); ok {
		if y := toInt(pd["year"]); y > 0 {
			return y
		}
	}
	return 0
}

func attachedSourceStringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return trimmedNonEmpty(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(AsOptionalString(item)); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func toAnySlice(raw any) []any {
	switch v := raw.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func trimmedNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// firstNonNil returns the first non-nil value, used to accept an attached-source
// payload under either of several request keys.
func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}
