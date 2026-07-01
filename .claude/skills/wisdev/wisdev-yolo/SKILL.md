---
name: wisdev-yolo
description: "Use when the user wants a multi-iteration, evidence-grounded research run — planning search branches, fanning out across academic providers, ranking evidence, and synthesizing a cited answer with gap/contradiction analysis. Examples: \"do a deep literature review on X\", \"research this question thoroughly\", \"run wisdev max on this\""
---

# Running the WisDev YOLO Research Loop

## When to Use

- The user wants more than a single search call: a planned, multi-iteration research pass that converges on a cited answer.
- They mention depth/thoroughness ("deep dive", "exhaustive", "maximum depth") or want hypothesis/contradiction analysis, not just a paper list.
- For single search/lookup/evidence calls from inside an IDE agent, prefer [[wisdev-mcp-research]] instead — MCP exposes search tools only, not this loop.

## The loop

```
Normalize task -> Plan search terms -> Retrieve (parallel providers) -> Analyze/rank evidence
  -> Synthesize provisional answer -> Evaluate gaps/contradictions -> iterate until budget exhausted
  -> Final report + trace events
```

## Entry points

| Command | Depth | Notes |
|---|---|---|
| `wisdev "question"` | Default (3 iter, 6 search terms) | `search`/`run`/`ask` are shortcuts for `yolo --local` |
| `wisdev max "question"` | Maximum | Forces `WISDEV_UNLEASHED=1`: 12 iterations, 20 search terms, 12 hits/search, 80 max papers, all providers, long-form, 30m timeout. Any flag you pass overrides the preset. |
| `wisdev yolo --local ...` | Full flag control | Use when you need a specific combination not covered by the presets |
| `wisdev yolo --remote --url <orchestrator>` | Calls a running `wisdev serve` over HTTP | For embedding behind a server instead of running in-process — see [[wisdev-embed]] |

## Key flags (`yolo`/`search`/`max`)

| Flag | Default | Effect |
|---|---|---|
| `--offline` | false | Disable network providers (smoke test) |
| `--provider` | all built-in | Comma-separated providers, e.g. `pubmed,arxiv` — see `wisdev sources` |
| `--domain` | auto | Domain hint (`medicine`, `cs`, ...) for provider routing |
| `--max-iterations` | 3 (12 unleashed) | Loop budget |
| `--max-search-terms` / `--hits-per-search` / `--max-unique-papers` | 6 / 5 / 20 | Retrieval breadth caps |
| `--disable-planning` / `--disable-hypotheses` | false | Skip programmatic query decomposition / swarm hypotheses |
| `--long-form` | false | Extended Introduction + Background sections |
| `--docgen` | false | Also draft a manuscript from retrieved papers — see [[wisdev-docgen]] |
| `--stages` | false | Stream stage events to stderr (use when debugging a run) |
| `-j`/`--json` | false | Raw JSON response, for programmatic consumption |

`WISDEV_UNLEASHED=1` lifts budget/iteration/token caps and enforces a minimum 5-iteration floor so the loop doesn't converge on the first pass.

## Interpreting a result

A YOLO result/report includes: a synthesized answer with `[n]` citations, the source list (provider + citation count), hypotheses explored (unless `--disable-hypotheses`), the search queries actually run, and (with `--stages`/TUI) per-iteration progress. Treat the answer's citations as the only grounding — don't add claims the report doesn't cite.

## Interactive alternative

`wisdev tui [--biomedical|--cs] [--iterations N] [--exhaustive]` opens a live dashboard (query field, provider checklist, scrollable Answer/Hypotheses/Queries/Sources panes) — useful when a human is steering the run rather than an agent consuming JSON. See `docs/CLI.md` for the full keybinding table.

Full reference: `docs/COMMANDS.md` (`max`, `search`/`yolo` sections), `docs/CLI.md`.
