# WisDev MCP — Claude Code, Cursor, Codex

WisDev exposes academic search **and grounded manuscript generation (DocGen)** over
**MCP stdio** for local agent clients.

## Tools

| Tool | Purpose |
|------|---------|
| `wisdevSearchPapers` | Multi-provider paper search |
| `wisdevPaperLookup` | Single-paper metadata by ID/DOI/arXiv |
| `wisdevEvidenceSearch` | Claim-grounded evidence snippets |
| `wisdevAuthorSearch` | Papers by author ID |
| `wisdevGenerateManuscript` | **DocGen** — retrieve papers, then draft a grounded, citation-backed manuscript (Markdown or JSON) |
| `wisdevGetConfig` | **Tuning** — discover + read every runtime knob (type, range/enum, default, current value) |
| `wisdevTuneConfig` | **Tuning** — change runtime defaults that subsequent calls inherit (validated against each knob) |
| `wisdevResetConfig` | **Tuning** — restore knob defaults (all or a subset) |
| `wisdevListProviders` | List registered search providers with domains + health (valid `sources` values) |
| `wisdevCapabilities` | Full control-surface overview: tools, tunable groups, resources |

Legacy `scholarlm*` aliases are accepted on `tools/call` (incl. `scholarlmGenerateManuscript`; `wisdevDocGen` is also accepted).

The tuning tools let an external LLM tune **anything** without restarting the server.
Read the same state as MCP resources: `wisdev://config`, `wisdev://providers`, `wisdev://capabilities`.

### Tuning knobs

Discover every knob with `wisdevGetConfig`, then change them with `wisdevTuneConfig`
(`{"settings": {"<key>": <value>}}`). Tuned values become the per-call defaults for
the action tools above. Groups: `search.*`, `evidence.*`, `author.*`, `manuscript.*`,
`server.*` — e.g. `search.limit`, `search.defaultSources`, `search.minCitations`,
`manuscript.maxPapers`, `manuscript.genre`, `manuscript.reviewRounds`,
`server.timeoutSeconds`. Out-of-range or unknown keys are rejected (valid updates in
the same call still apply).

## Granular controls

`wisdevGenerateManuscript` parameters:

| Param | Type | Description |
|-------|------|-------------|
| `query` | string (required) | Research question / manuscript topic |
| `words` | integer | Target total word count, split across sections (0 = model default) |
| `minCitations` | integer | Minimum distinct sources the manuscript must cite — raises the retrieval floor and instructs the writers to cite broadly |
| `flow` | string[] | Ordered section plan, e.g. `["abstract","introduction","methods","results","discussion","conclusion"]`. Known ids reuse the tuned section briefs; unknown ids become generic synthesis sections in the given order |
| `reviewRounds` | integer | Max rounds of the agentic generate→review→revise loop (each round re-reviews and rewrites flagged sections, stopping on convergence). 0 = default (2), max 5 |
| `genre` | string | Manuscript genre, e.g. `narrative literature review` (default) or `research paper` — controls voice and how the reviewer grades it |
| `maxPapers` | integer | Max papers to ground on (1–80, default 30) |
| `sources` | string[] | Provider hints (openalex, arxiv, semantic_scholar, pubmed, …) |
| `domain` | string | Research domain hint for provider routing |
| `format` | string | `markdown` (default) or `json` |

`wisdevSearchPapers` also accepts `minCitations` (return only papers with at least that many citations).

> Manuscript prose minimizes em-dashes (`—`) by default — both via the writer prompt and a deterministic post-process.

## Run the stdio server

```powershell
cd wisdev-arc
.\wisdev.cmd mcp
```

```bash
cd wisdev-arc
./wisdev mcp
```

Optional flags: `.\wisdev.cmd mcp --provider openalex,arxiv` or `--offline`

The process reads JSON-RPC from **stdin** and writes responses to **stdout**. Do not print logs to stdout.

## Cursor

```powershell
cd wisdev-arc
.\wisdev.cmd setup --write .cursor\mcp.json
```

Or copy [`docs/examples/cursor-mcp.json`](examples/cursor-mcp.json). Minimal hand-written config:

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "powershell",
      "args": ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "C:\\path\\to\\wisdev-arc\\scripts\\wisdev.ps1", "mcp"],
      "cwd": "C:\\path\\to\\wisdev-arc"
    }
  }
}
```

After `make build-cli`:

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "C:\\path\\to\\wisdev-arc\\dist\\wisdev.exe",
      "args": ["mcp"]
    }
  }
}
```

## Claude Desktop / Claude Code

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "/absolute/path/to/wisdev-arc/wisdev",
      "args": ["mcp"]
    }
  }
}
```

## Full research loop vs MCP tools

MCP provides **search tools plus DocGen** (`wisdevGenerateManuscript`), not the
streamed autonomous research loop. For the autonomous loop:

```powershell
.\wisdev.cmd "your research question"
```

Or use ScholarLM: https://scholarlm-vbcr.web.app

## HTTP MCP (remote)

- Local server: `http://127.0.0.1:8081/wisdev/mcp`
- Prod gateway: `https://rust-gateway-cyucrnqqnq-uc.a.run.app/wisdev/mcp` (auth required)
