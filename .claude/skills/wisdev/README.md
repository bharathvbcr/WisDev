# WisDev usage skills

Installable agent skills for **operating** WisDev ARC (CLI, MCP, YOLO, DocGen, embedding API).
These are distinct from the GitNexus skills under `.claude/skills/gitnexus/` (those are for editing this codebase).

| Skill | Covers |
|-------|--------|
| `wisdev-cli` | Install, `check`/`doctor`, TUI, demo, serve, providers |
| `wisdev-mcp-research` | MCP search / evidence / author / DocGen / tuning tools |
| `wisdev-yolo` | Autonomous multi-iteration research loop |
| `wisdev-docgen` | Headless ScholarDoc generation |
| `wisdev-embed` | Go embedding API + HTTP/gRPC |

## Install (Claude Code + Cursor)

From this checkout:

```bash
./scripts/install-skills.sh
# preview:
./scripts/install-skills.sh --dry-run
```

This symlinks each skill into:

- `~/.claude/skills/wisdev-*` — Claude Code (user-global)
- `~/.cursor/skills/wisdev-*` — Cursor personal skills
- Parent checkout `.agents/skills/` and `.claude/skills/` when present (e.g. ScholarLM)

Also register the MCP server (skills document the tools; MCP exposes them):

```bash
PATH="$HOME/go/bin:$PATH"
wisdev setup --write ~/.cursor/mcp.json --binary
claude mcp add wisdev -- "$(command -v wisdev)" mcp
```

Canonical docs: `docs/MCP_CLIENTS.md`, `docs/CLI.md`, `docs/COMMANDS.md`.
