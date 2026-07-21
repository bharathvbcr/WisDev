# wisdev-docgen

Generate ScholarDoc-equivalent documents headless via the WisDev ARC CLI, TUI, or MCP.

## When to use

- User wants a grounded manuscript, quick report, or literature review from the terminal
- User asks for `wisdev docgen`, document generation, or `wisdevGenerateManuscript`
- User needs citation-style-aware export (APA, MLA, Chicago, Vancouver, IEEE, Harvard, Nature)

## Document intents

| Intent | CLI flag | Description |
|--------|----------|-------------|
| `fullpaper` | `--intent fullpaper` (default) | Full grounded manuscript: plan → draft → review → fact-check |
| `report` | `--intent report` | Fast thematic synthesis (Quick Report) |
| `litreview` | `--intent litreview` | Thematic literature review with grounded citations |

## CLI examples

```bash
# Full manuscript (default)
wisdev docgen --offline "transformers in drug discovery"

# Quick report with IEEE bibliography
wisdev docgen --intent report --citation-style ieee --offline "clinical RAG"

# Literature review as HTML
wisdev docgen --intent litreview --format html --offline "graph neural networks"

# Full paper as LaTeX with custom section flow
wisdev docgen --intent fullpaper --format latex --flow introduction,methods,results,discussion \
  --min-citations 10 --words 5000 "battery anodes"

# DOCX export (requires pandoc on PATH)
wisdev docgen --format docx -o paper.docx --offline "topic"

# Replay a fixed corpus (skip retrieval)
wisdev docgen --corpus-file papers.json --intent fullpaper "topic"
```

## YOLO + DocGen

```bash
wisdev yolo --docgen --doc-intent fullpaper --doc-citation-style apa "topic"
```

## TUI

Enable **DocGen** in settings (row 7). When on, cycle with:

- `i` — intent (`fullpaper` / `report` / `litreview`)
- `f` — format (`markdown` / `latex` / `html` / `json`)
- `c` — citation style (`apa` / `mla` / `chicago` / `vancouver` / `ieee` / `harvard` / `nature`)

Manuscript is written beside the research export as `{stem}-manuscript.{ext}`.

## MCP tool: `wisdevGenerateManuscript`

Key parameters:

| Param | Values | Notes |
|-------|--------|-------|
| `query` | string | Required |
| `intent` | `fullpaper` \| `report` \| `litreview` | Default `fullpaper` |
| `citationStyle` | `apa` \| `mla` \| `chicago` \| `vancouver` \| `ieee` \| `harvard` \| `nature` | Default `apa` |
| `format` | `markdown` \| `json` \| `latex` \| `html` | `docx` is CLI-only |
| `words`, `minCitations`, `flow`, `genre`, `reviewRounds` | | Full-paper knobs |

`fullpaper` + `json` returns the raw pipeline result; other intents return canonical Document JSON.

## Go embedding API

```go
import "github.com/bharathvbcr/wisdev-arc/orchestrator/pkg/wisdev"

// Requires SetDocumentGenerator wired at startup (CLI does this automatically).
result, err := wisdev.GenerateDocument(ctx, wisdev.DocumentOptions{
    Query:         "topic",
    Intent:        "report",
    CitationStyle: "apa",
    Format:        "markdown",
    Offline:       true,
})
```

## Offline / no sidecar

`--offline` disables network search and uses grounded scaffolds when the Python sidecar is unreachable. Full-paper sections fall back to claim-packet scaffolds; report/litreview emit structured scaffolds from retrieved (or empty) papers.

## Canonical docs

Track `wisdev-arc/docs/CLI.md`, `docs/COMMANDS.md`, and `docs/MCP_CLIENTS.md` for flag/param details.
