# WisDev MCP — Claude Code, Cursor, Codex

WisDev exposes academic search **and headless ScholarDoc document generation (DocGen)**
over **MCP stdio** for local agent clients. DocGen supports three document intents
(`fullpaper`, `report`, `litreview`), seven citation styles, and multiple output
formats.

On `initialize`, the server returns agent **instructions** (tool routing) and advertises
**tools**, **resources**, and **prompts**. Prefer canonical `wisdev*` names; legacy
`scholarlm*` aliases remain accepted on `tools/call`.

## Tools

| Tool | Purpose |
|------|---------|
| `wisdevSearchPapers` | Multi-provider paper search |
| `wisdevPaperLookup` | Single-paper metadata by ID/DOI/arXiv |
| `wisdevEvidenceSearch` | Claim-grounded evidence snippets |
| `wisdevAuthorSearch` | Papers by author ID |
| `wisdevGenerateManuscript` | **DocGen** — retrieve papers, then generate a ScholarDoc document (`fullpaper`, `report`, or `litreview`) with citation-style-aware bibliography |
| `wisdevGetConfig` | **Tuning** — discover + read every runtime knob (type, range/enum, default, current value) |
| `wisdevTuneConfig` | **Tuning** — change runtime defaults that subsequent calls inherit (validated against each knob) |
| `wisdevResetConfig` | **Tuning** — restore knob defaults (all or a subset) |
| `wisdevListProviders` | List registered search providers with domains + health (valid `sources` values) |
| `wisdevCapabilities` | Full control-surface overview: tools, tunable groups, resources |

Legacy `scholarlm*` aliases are accepted on `tools/call` (incl. `scholarlmGenerateManuscript`; `wisdevDocGen` is also accepted).

### Prompts

| Prompt | Arguments |
|--------|-----------|
| `wisdev_literature_search` | `query` (required), `minCitations` (optional) |
| `wisdev_evidence_check` | `claim` (required) |
| `wisdev_docgen` | `query` (required), `intent`, `citationStyle` (optional) |

The tuning tools let an external LLM tune **anything** without restarting the server.
Read the same state as MCP resources: `wisdev://config`, `wisdev://providers`, `wisdev://capabilities`.

### Tuning knobs

Discover every knob with `wisdevGetConfig`, then change them with `wisdevTuneConfig`
(`{"settings": {"<key>": <value>}}`). Tuned values become the per-call defaults for
the action tools above. Groups: `search.*`, `evidence.*`, `author.*`, `manuscript.*`,
`server.*` — e.g. `search.limit`, `search.defaultSources`, `search.minCitations`,
`manuscript.maxPapers`, `manuscript.genre`, `manuscript.reviewRounds`,
`manuscript.intent`, `manuscript.citationStyle`, `manuscript.format`,
`server.timeoutSeconds`. Out-of-range or unknown keys are rejected (valid updates in
the same call still apply).

## Granular controls

`wisdevGenerateManuscript` parameters:

### Document intents

| `intent` | Default | Behavior |
|----------|---------|----------|
| `fullpaper` | yes | Full `ManuscriptPipeline`: plan → draft → review → fact-check → peer review. Honors `words`, `minCitations`, `flow`, `genre`, `reviewRounds`. |
| `report` | | Quick Report — fast thematic synthesis. Ignores manuscript-pipeline knobs (`flow`, `reviewRounds`, `genre`). |
| `litreview` | | Thematic literature review with grounded in-text citations. Ignores manuscript-pipeline knobs. |

### Parameters

| Param | Type | Description |
|-------|------|-------------|
| `query` | string (required) | Research question / manuscript topic |
| `words` | integer | Target total word count, split across sections (0 = model default) |
| `minCitations` | integer | Minimum distinct sources the manuscript must cite — raises the retrieval floor and instructs the writers to cite broadly. Omit for the default floor of 10 (tunable via `manuscript.minCitations`); pass an explicit 0 for no minimum |
| `flow` | string[] | Ordered section plan, e.g. `["abstract","introduction","methods","results","discussion","conclusion"]`. Known ids reuse the tuned section briefs; unknown ids become generic synthesis sections in the given order |
| `reviewRounds` | integer | Max rounds of the agentic generate→review→revise loop (each round re-reviews and rewrites flagged sections, stopping on convergence). 0 = default (2), max 5 |
| `genre` | string | Manuscript genre, e.g. `narrative literature review` (default) or `research paper` — controls voice and how the reviewer grades it |
| `maxPapers` | integer | Max papers to ground on (1–80, default 30) |
| `sources` | string[] | Provider hints (openalex, arxiv, semantic_scholar, pubmed, …) |
| `domain` | string | Research domain hint for provider routing |
| `format` | string | `markdown` (default) \| `json` \| `latex` \| `html` |
| `intent` | string | Document type: `fullpaper` (default) \| `report` \| `litreview` |
| `citationStyle` | string | Bibliography citation style: `apa` (default) \| `mla` \| `chicago` \| `vancouver` \| `ieee` \| `harvard` \| `nature` |

> **JSON output:** `fullpaper` + `format=json` returns the raw `ManuscriptPipelineResult`;
> `report` / `litreview` + `format=json` return the canonical `Document` envelope.
>
> **DOCX** is CLI-only (`wisdev docgen --format docx`); MCP is a text-based tool.

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
# or installed binary:
wisdev mcp
```

Optional flags: `.\wisdev.cmd mcp --provider openalex,arxiv` or `--offline`

The process reads JSON-RPC from **stdin** and writes responses to **stdout**. Do not print logs to stdout.

## Install skills (Claude + Cursor)

Usage skills ship under `.claude/skills/wisdev/`. Install them into user + project skill paths:

```bash
cd wisdev-arc
./scripts/install-skills.sh
```

This symlinks `wisdev-cli`, `wisdev-mcp-research`, `wisdev-yolo`, `wisdev-docgen`, and
`wisdev-embed` into `~/.claude/skills/`, `~/.cursor/skills/`, and (when present) the
parent checkout’s `.agents/skills/` and `.claude/skills/`. See
[`.claude/skills/wisdev/README.md`](../.claude/skills/wisdev/README.md).

## Cursor

Recommended (absolute binary; merges into an existing `mcpServers` map):

```bash
PATH="$HOME/go/bin:$PATH"
wisdev setup --write ~/.cursor/mcp.json --binary
# project-local:
wisdev setup --write .cursor/mcp.json --binary
```

Or copy [`docs/examples/cursor-mcp.json`](examples/cursor-mcp.json). Minimal hand-written config:

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "/Users/you/go/bin/wisdev",
      "args": ["mcp", "--provider", "openalex,arxiv"]
    }
  }
}
```

`setup --binary` resolves the path via the running binary (or `PATH`) and merges the
`wisdev` entry without wiping other servers (use `--replace` to overwrite the whole map).
It also writes a PATH env hint so Cursor’s minimal environment still finds dependent tools.

Windows / go-run template:

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

## Claude Desktop / Claude Code

```bash
# Claude Code (stdio)
claude mcp add wisdev -- /Users/you/go/bin/wisdev mcp
# or:
claude mcp add-json wisdev '{"command":"/Users/you/go/bin/wisdev","args":["mcp","--provider","openalex,arxiv"]}'
```

Config file shape (Claude Desktop / manual):

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "/Users/you/go/bin/wisdev",
      "args": ["mcp", "--provider", "openalex,arxiv"]
    }
  }
}
```

Verify: `claude mcp list` and `wisdev doctor` (reports MCP tool count).

## Full research loop vs MCP tools

MCP provides **search tools plus DocGen** (`wisdevGenerateManuscript`), not the
streamed autonomous research loop. DocGen supports three ScholarDoc intents
(`fullpaper`, `report`, `litreview`), seven citation styles, and formats
`markdown`, `json`, `latex`, and `html`. For the autonomous loop:

```powershell
.\wisdev.cmd "your research question"
```

Or use ScholarLM: https://scholarlm-vbcr.web.app

## HTTP MCP (remote)

- Local server: `http://127.0.0.1:8081/wisdev/mcp`
- Prod gateway: `https://rust-gateway-cyucrnqqnq-uc.a.run.app/wisdev/mcp` (auth required)

OSS note: this surface is academic search + DocGen + tuning only — no Firebase/Stripe
or Scholar product routes.
