---
name: wisdev-mcp-research
description: "Use when ScholarLM/WisDev MCP is available and you need academic search, paper lookup, claim evidence, author lookup, or async ScholarDoc manuscript jobs from Claude Code, Cursor, or another MCP client. Prefer SaaS remote MCP over wisdev-arc stdio."
---

# Researching with ScholarLM Native MCP

## When to Use

- ScholarLM SaaS MCP is registered (HTTP/SSE via rust_gateway, or `scripts/agent/mcp-server.mjs` stdio→HTTP bridge) and you need literature answers or a Full Paper / ScholarDoc job.
- A user asks for papers/evidence/citations, a DOI/arXiv lookup, an author list, or a grounded manuscript draft.
- You need claim-grounded snippets to cite — use returned IDs/DOIs only.

> MCP is **not** the multi-iteration YOLO research loop. For that, use the CLI (`wisdev "question"` / ScholarLM YOLO routes) — see [[wisdev-yolo]].

## Setup (SaaS-first)

```bash
# Skills (Claude Code + Cursor)
./scripts/install-skills.sh
```

Cursor / Codex `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "scholarlm": {
      "command": "node",
      "args": ["scripts/agent/mcp-server.mjs"],
      "env": {
        "SCHOLARLM_AGENT_BASE_URL": "http://127.0.0.1:8080",
        "SCHOLARLM_AGENT_TOKEN": "<firebase-id-token-for-non-local>"
      }
    }
  }
}
```

Claude Code remote SSE:

```bash
claude mcp add scholarlm http://127.0.0.1:8080/wisdev/mcp/sse
```

Optional local Go stdio (search/config; no FullPaper persist unless wired):  
`SCHOLARLM_MCP_MODE=go` → `go run ./backend/go_orchestrator/cmd/mcp`.

Authenticate with **Authorization: Bearer** (or local gateway user headers). Do not pass secrets in tool arguments (`scholarlmSignIn` is instructional only).

Full ops guide: `docs/ops/MCP_SERVER_GUIDE.md`.

## Tools

| Tool | Purpose | Notes |
|---|---|---|
| `wisdevSearchPapers` | Multi-provider paper search | `query`, `sources`, `minCitations` |
| `wisdevPaperLookup` | Metadata by ID/DOI/arXiv | `paperId` |
| `wisdevEvidenceSearch` | Claim-oriented snippets | Search + abstracts — not a separate RAG index |
| `wisdevAuthorSearch` | Papers by author ID | `authorId` |
| `wisdevGenerateManuscript` | ScholarDoc async start | Returns `jobId` + `pollHint`; same store as `/full-paper/*` |
| `scholarlmExportPaper` | Gate-aware export | Requires `jobId` |
| `scholarlmListScripts` / `RunScript` | Capability router | Remote: `search`, `paper.*`. Local-only: `stack.*`. Never shell on SaaS |
| `wisdevGetConfig` / `Tune` / `Reset` / `ListProviders` / `Capabilities` | Runtime knobs | Session defaults |

Legacy `scholarlm*` aliases remain accepted on `tools/call`.

## Workflow

1. `wisdevCapabilities` or `wisdevListProviders` once if unsure of valid `sources`.
2. Narrowest tool: topic → `wisdevSearchPapers`; known ID → `PaperLookup`; claim → `EvidenceSearch`; author → `AuthorSearch`.
3. Manuscript: `wisdevGenerateManuscript` → poll `scholarlmRunScript` with `paper.status` / `paper.manuscript` / `paper.export` (or HTTP `/full-paper/*`). Do not expect a multi-minute sync hold.
4. Cite returned IDs/DOIs only.

## Tuning

`wisdevGetConfig` → `wisdevTuneConfig({"settings": {...}})` → `wisdevResetConfig`. Groups: `search.*`, `evidence.*`, `author.*`, `manuscript.*`, `server.*`. Resources: `wisdev://config`, `wisdev://providers`, `wisdev://capabilities`.

## Remote endpoints

- Local gateway: `http://127.0.0.1:8080/wisdev/mcp`
- Production: rust_gateway `/wisdev/mcp` (auth required)
- Flag: `WISDEV_MCP_ENABLED` (default on)

ADK in-process bridge is **search/config only**; ScholarDoc stays on MCP HTTP + script router.
