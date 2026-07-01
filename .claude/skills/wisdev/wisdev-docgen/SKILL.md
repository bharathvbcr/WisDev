---
name: wisdev-docgen
description: "Use when the user wants a citation-backed manuscript or literature-review draft generated from research — not just a search result. Covers both the wisdev docgen CLI command and the wisdevGenerateManuscript MCP tool. Examples: \"draft a literature review on X\", \"generate a manuscript with citations\", \"write a research paper draft grounded in real papers\""
---

# Generating Manuscripts with WisDev DocGen

## When to Use

- The user wants a structured, multi-section document (Abstract through Conclusion) grounded in retrieved papers, not just a synthesized answer.
- "Write a literature review", "draft a paper on X with citations", "generate a manuscript".
- Choose CLI `docgen` for a one-shot terminal run; choose the MCP tool `wisdevGenerateManuscript` when already driving WisDev as an MCP server (see [[wisdev-mcp-research]]); use `yolo --docgen` when you want the full research loop *and* a manuscript from the same run (see [[wisdev-yolo]]).

## What it does

Runs the local research loop to gather grounded papers, then drives the manuscript pipeline (same engine as the `/full-paper` HTTP route): ordered sections, grounded visuals, a peer-review critique pass, and a reference list. Drafting is an **agentic generate → review → revise loop** — each round re-reviews and rewrites flagged sections, stopping on convergence (max rounds configurable). Prose minimizes em-dashes by both prompt instruction and a deterministic post-process.

Section prose is enriched by the Python sidecar when reachable; falls back to grounded scaffolds otherwise — `--offline`/no sidecar still produces a structured draft, just less polished prose.

## CLI

```bash
wisdev docgen "your topic"
wisdev docgen -o paper.md --provider pubmed,arxiv "your topic"
wisdev docgen --words 4000 --min-citations 20 --flow introduction,methods,results,discussion "your topic"
wisdev yolo --docgen --doc-words 3000 "your question"   # research loop + manuscript in one run
```

| Flag | Default | Effect |
|---|---|---|
| `-o`/`--output` | stdout | Write manuscript markdown to a file |
| `-f`/`--format` | markdown | `markdown` \| `latex` \| `json` |
| `--words` | model default | Target total word count across sections |
| `--min-citations` | 0 | Minimum distinct sources cited (raises retrieval floor too) |
| `--flow` | default plan | Comma-separated section order; unknown ids become generic sections |
| `--review-rounds` | 2 (max 5) | Agentic review/revise rounds |
| `--genre` | narrative literature review | e.g. `research paper` — controls voice + reviewer grading |
| `--provider` / `--domain` / `--offline` | — | Same retrieval controls as `yolo`/`search` |

Inside a `yolo` run, the same controls are the `--doc-*` aliases (`--doc-words`, `--doc-min-citations`, `--doc-flow`, `--doc-review-rounds`, `--doc-genre`, `--doc-format`, `--doc-output`).

## MCP (`wisdevGenerateManuscript`)

| Param | Type | Notes |
|---|---|---|
| `query` | string, required | Topic / research question |
| `words` | integer | 0 = model default |
| `minCitations` | integer | Raises retrieval floor |
| `flow` | string[] | e.g. `["abstract","introduction","methods","results","discussion","conclusion"]` |
| `reviewRounds` | integer | 0 = default 2, max 5 |
| `genre` | string | default `narrative literature review` |
| `maxPapers` | integer | 1–80, default 30 |
| `sources` | string[] | Provider hints |
| `format` | string | `markdown` (default) or `json` |

Legacy aliases accepted: `scholarlmGenerateManuscript`, `wisdevDocGen`. Tune persistent defaults (e.g. `manuscript.maxPapers`, `manuscript.genre`, `manuscript.reviewRounds`) with `wisdevTuneConfig` — see [[wisdev-mcp-research]].

## Local-model drafting

DocGen's writer/reviewer/coordinator calls go through the sidecar's `manuscript_llm()` selector, independent of the research loop's `WISDEV_LLM_*` vars. Set `MANUSCRIPT_LLM_PROVIDER=local` (or `ollama`) plus `LOCAL_LLM_BASE_URL`/`LOCAL_LLM_MODEL` to draft with no cloud credentials; unset defaults to Gemini/Vertex if no local server is configured.

Full reference: `docs/CLI.md` (DocGen controls), `docs/COMMANDS.md` (`docgen` section), `docs/MCP_CLIENTS.md` (granular controls).
