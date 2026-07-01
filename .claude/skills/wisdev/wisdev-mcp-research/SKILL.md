---
name: wisdev-mcp-research
description: "Use when WisDev is wired in as an MCP server and you need to search academic literature, look up a paper, find claim-grounded evidence, or look up an author from inside Claude Code, Cursor, or another MCP client. Examples: \"find papers on X\", \"what's the evidence for Y\", \"look up this DOI\", \"what has this author published\""
---

# Researching with the WisDev MCP Tools

## When to Use

- WisDev is registered as an MCP server (`claude mcp add wisdev -- wisdev mcp`) and you need literature answers, not codebase answers.
- A user asks for papers/evidence/citations on a topic, a specific paper by ID/DOI/arXiv, or an author's publication list.
- You need claim-grounded evidence snippets to cite in an answer, not just a freeform literature search.

> MCP gives you search + DocGen tools, not the full autonomous loop. For the multi-iteration research loop with hypothesis generation and gap analysis, use the CLI (`wisdev "question"`) — see [[wisdev-yolo]].

## Setup (once)

```bash
claude mcp add wisdev -- wisdev mcp
```

Or `wisdev setup --write .cursor/mcp.json` for Cursor; see `docs/examples/cursor-mcp.json`. Claude Desktop/Code config:

```json
{"mcpServers": {"wisdev": {"command": "/absolute/path/to/wisdev-arc/wisdev", "args": ["mcp"]}}}
```

The stdio server keeps stdout protocol-clean; logs go to stderr. Optional flags: `--provider openalex,arxiv`, `--offline`.

## Tools

| Tool | Purpose | Key params |
|---|---|---|
| `wisdevSearchPapers` | Multi-provider paper search | `query`, `sources`, `minCitations` |
| `wisdevPaperLookup` | Single-paper metadata by ID/DOI/arXiv | `paperId` |
| `wisdevEvidenceSearch` | Claim-grounded evidence snippets | `claim` |
| `wisdevAuthorSearch` | Papers by author ID | `authorId` |
| `wisdevGenerateManuscript` | DocGen — see [[wisdev-docgen]] | `query`, `words`, `flow`, ... |
| `wisdevListProviders` | Registered providers + health (valid `sources` values) | — |
| `wisdevCapabilities` | Full control-surface overview: tools, tunable groups, resources | — |
| `wisdevGetConfig` / `wisdevTuneConfig` / `wisdevResetConfig` | Read / change / reset runtime knobs | `settings` |

Legacy `scholarlm*` aliases (e.g. `scholarlmSearchPapers`, `scholarlmGenerateManuscript`) are also accepted on `tools/call`.

## Workflow

1. `wisdevListProviders` (or `wisdevCapabilities`) once per session if you're unsure which `sources` values are valid for this deployment.
2. Pick the narrowest tool for the question:
   - General topic search → `wisdevSearchPapers`
   - Known paper, ID/DOI in hand → `wisdevPaperLookup`
   - "What's the evidence that X" / need quotable snippets → `wisdevEvidenceSearch`
   - "What has author Y published" → `wisdevAuthorSearch`
3. If results are too sparse or too broad, retune before re-querying rather than guessing flags per call: `wisdevTuneConfig({"settings": {"search.limit": 20, "search.minCitations": 5}})`. Tuned values persist as defaults for subsequent calls in the session.
4. Cite returned papers by their returned IDs/DOIs; don't invent citation text not present in the tool result.

## Tuning knobs

Discover with `wisdevGetConfig`, change with `wisdevTuneConfig({"settings": {...}})`, restore with `wisdevResetConfig`. Groups: `search.*`, `evidence.*`, `author.*`, `manuscript.*`, `server.*` (e.g. `search.limit`, `search.defaultSources`, `search.minCitations`, `manuscript.maxPapers`, `manuscript.genre`, `manuscript.reviewRounds`, `server.timeoutSeconds`). Out-of-range/unknown keys are rejected; valid keys in the same call still apply. Same state is also readable as resources: `wisdev://config`, `wisdev://providers`, `wisdev://capabilities`.

## Remote / HTTP MCP

- Local: `http://127.0.0.1:8081/wisdev/mcp` (needs `wisdev serve` running — see [[wisdev-embed]])
- Prod gateway: `https://rust-gateway-cyucrnqqnq-uc.a.run.app/wisdev/mcp` (auth required)

Full reference: `docs/MCP_CLIENTS.md`.
