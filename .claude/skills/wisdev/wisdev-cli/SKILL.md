---
name: wisdev-cli
description: "Use when the user wants to install, run, or operate the wisdev CLI itself — health checks, local research runs, the TUI, the demo, or starting the HTTP server. Examples: \"how do I install wisdev\", \"run a quick research query from the terminal\", \"start the wisdev server\", \"check if wisdev is healthy\""
---

# Operating the WisDev CLI

## When to Use

- Installing or updating the `wisdev` binary
- Running a one-off research query from the terminal
- Health-checking the environment (LLM provider, sidecar, providers)
- Opening the interactive TUI for an exploratory research session
- Starting the HTTP orchestrator (`wisdev serve`) for remote/embedded use
- Listing available search providers

## Install

| Path | Command |
|---|---|
| One-line (release binary, source fallback) | `curl -fsSL https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.sh \| bash` |
| Windows | `irm https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.ps1 \| iex` |
| From checkout | `make install-cli` (or `scripts/install.sh` / `scripts\install.ps1`) |
| Installed via Go | `go install ./orchestrator/cmd/wisdev` |

Overrides: `WISDEV_VERSION`, `WISDEV_INSTALL_DIR`, `WISDEV_FROM_SOURCE=1`.

## Workflow

1. `wisdev check` (`doctor` is the canonical name; `check` is the alias) — confirm the active LLM provider, sidecar reachability, and provider health before anything else.
2. Pick an entrypoint:
   - `wisdev "question"` — quick local research (openalex + arxiv); see [[wisdev-yolo]] for full flag control.
   - `wisdev tui` — interactive session with live progress and a results browser.
   - `wisdev demo [--offline]` — scripted walkthrough query.
   - `wisdev serve` — start the HTTP orchestrator (`:8081`) for `--remote` calls or embedding; see [[wisdev-embed]].
3. `wisdev sources` (alias `providers`) — list registered search providers and their domain fit.
4. `wisdev guide` (alias `commands`) — full in-CLI command reference when unsure what's available.

## Commands at a glance

| Command | Aliases | Use |
|---|---|---|
| `search` / `run` | `ask` | Local research, synthesize cited answer — see [[wisdev-yolo]] |
| `max` | — | Maximum-depth research (unleashed budgets, 12 iterations) |
| `docgen` | `docugen` | Search + manuscript draft — see [[wisdev-docgen]] |
| `yolo` | — | Full-flag research engine (`--local` default, `--remote`) |
| `tui` | `ui` | Interactive terminal UI |
| `demo` | — | Scripted demo sequence |
| `serve` | — | Start HTTP orchestrator |
| `mcp` | — | MCP stdio server — see [[wisdev-mcp-research]] |
| `mcp-config` | `setup` | Generate an MCP client config |
| `doctor` | `check` | Environment / orchestrator health check |
| `providers` | `sources` | List search providers |
| `update` | `upgrade` | Self-update from GitHub releases |
| `guide` | `commands` | Print full command reference |

## Running from different contexts

```bash
cd wisdev-arc
./wisdev check            # Mac/Linux
.\wisdev.cmd check        # Windows, repo root
```

```bash
cd scholarlm               # from the parent ScholarLM checkout
npm run wisdev -- check
```

`make check` / `make demo` / `make cli-help` also work from `wisdev-arc/`.

## LLM provider modes

Set before `check`/`tui`/`serve` so `wisdev doctor` reports the right backend:

| Mode | `WISDEV_LLM_PROVIDER` | Needs |
|---|---|---|
| Local | `ollama` / `openai-compatible` | `WISDEV_LLM_BASE_URL`, `WISDEV_LLM_MODEL` (Ollama default `http://127.0.0.1:11434/v1`) |
| Cloud | `cloud` / `vertex` / `gemini` | `GOOGLE_CLOUD_PROJECT` + ADC (`gcloud auth application-default login`) |
| Hybrid | `hybrid` (or unset + `WISDEV_LLM_BASE_URL`) | Both — light/standard tiers try Ollama first, heavy tiers try Vertex first |

## Troubleshooting

- `wisdev check --json` for a machine-readable health report.
- Title/spinner not showing in VS Code's terminal → set `"terminal.integrated.tabs.title": "${sequence}"`.
- No `wisdev` on PATH → the launcher falls back to `dist/wisdev.exe`/`dist/wisdev`, then `go run`. Force the dist binary with `WISDEV_USE_DIST=1`; show which binary runs with `WISDEV_VERBOSE=1 wisdev version`.

Full reference: `docs/CLI.md`, `docs/COMMANDS.md`.
