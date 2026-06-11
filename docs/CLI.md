# WisDev CLI Reference

## Run (pick one)

| From | Command |
|------|---------|
| **Repo root (Windows)** | `.\wisdev.cmd check` |
| **Repo root (Mac/Linux)** | `./wisdev check` |
| **ScholarLM root** | `npm run wisdev -- check` |
| **Make** | `make check` |
| **Installed** | `go install ./orchestrator/cmd/wisdev` then `wisdev check` |

No more `go run ./cmd/wisdev` unless you are hacking the CLI itself.

## Essentials

| Command | What it does |
|---------|----------------|
| `wisdev "question"` | Local research (openalex + arxiv) |
| `wisdev max "question"` | Maximum-depth research: unleashed budgets, 12 iterations, all providers, long-form report |
| `wisdev check` | Health check |
| `wisdev tui` | Interactive terminal UI for local research |
| `wisdev demo` | Hackathon demo sequence |
| `wisdev mcp` | MCP stdio server |
| `wisdev setup --write .cursor/mcp.json` | MCP client config |
| `wisdev serve` | Start HTTP orchestrator |
| `wisdev sources` | List search providers |
| `wisdev update` | Self-update to the latest GitHub release (`--check` to only report; alias: `upgrade`) |
| `wisdev guide` | One-screen guide to every command, grouped by task (alias: `commands`) |

## Examples

```powershell
cd wisdev-arc
.\wisdev.cmd check
.\wisdev.cmd tui
.\wisdev.cmd "RAG for scientific literature"
.\wisdev.cmd demo --offline
.\wisdev.cmd setup --write .cursor\mcp.json
```

```bash
cd wisdev-arc
./wisdev check
./wisdev tui
./wisdev "RAG for scientific literature"
make demo
```

```powershell
cd scholarlm
npm run wisdev -- check
npm run wisdev -- tui
npm run wisdev -- "your research question"
```

## Interactive TUI

`wisdev tui` opens a raw-terminal interface with a query field, provider checklist, run settings, live iteration progress, and scrollable results panes (Answer, Hypotheses, Queries, Sources with citations). Use `wisdev tui --offline` to start with network-backed providers disabled.

Run settings:

| Setting | Default | Effect |
|---------|---------|--------|
| Max iter | 6 | Maximum loop iterations and search budget |
| Planning | on | Programmatic query decomposition |
| Offline | off | Disable network providers |
| Enhance | on | Grammar/typo cleanup + domain detection |
| Hypotheses | on | Swarm hypothesis generation |
| Exhaustive | off | Run all max iterations before early stop |
| Long-form | off | Synthesize extended Introduction and Background sections in the report |

With **Enhance** on, the input screen previews the corrected query and detected domain (e.g. `medicine` for meniscus/ACL questions). Status shows `iterations=3/9` when the loop stops early unless **Exhaustive** is on. Setting max iterations to **8+** auto-enables Exhaustive so the loop runs the full budget. When many providers are enabled, detected domains auto-apply provider presets (`medicine`/`biology` → biomedical, `cs`/`ai` → computer science, `physics`/`math` → physics).

TUI flags:

| Flag | Effect |
|------|--------|
| `--iterations N` | Pre-set max iterations (1–12) |
| `--exhaustive` | Start with Exhaustive mode on |
| `--biomedical` | Start with biomedical provider preset |
| `--cs` | Start with computer science provider preset |
| `--no-enhance` | Disable query enhancement (on by default for `yolo`, `search`, and TUI) |
| `--fresh` | Bypass search cache for a new retrieval pass |
| `--batch FILE` | Run newline-delimited queries sequentially |
| `--no-bell` | Disable the completion chime (double bell on success, single on failure) |
| `--autostart` | Begin research immediately when `--query` is set |

While the TUI is open it renames the terminal tab/window to `WisDev` (restored on exit), animates a spinner in the title while research runs (`⠹ WisDev — researching`), and shows `✓ WisDev — done` / `✗ WisDev — failed` when it finishes. On Windows Terminal and ConEmu the taskbar icon also shows live research progress via OSC 9;4. On Windows the title is set both via escape sequence and Win32 so it works across terminal hosts.

> VS Code's integrated terminal shows the process name as the tab title by default. To see the WisDev title there, set `"terminal.integrated.tabs.title": "${sequence}"` in VS Code settings.

```powershell
.\wisdev.cmd tui --demo
.\wisdev.cmd tui --demo --autostart
.\wisdev.cmd tui --query "RAG for scientific literature"
.\wisdev.cmd tui --query "meniscus scaffolds and ACL reconstruction" --biomedical --exhaustive --iterations 9 --autostart
.\wisdev.cmd tui --offline --output demo-result.md
```

`--demo` pre-fills the hackathon question and offline mode; add `--autostart` (on by default with `--demo`) to launch research immediately for screen recording.

Keyboard controls:

| Key | Effect |
|-----|--------|
| `Tab` / `Shift+Tab` | Move focus |
| Arrow keys | Navigate providers, settings, buttons, and results |
| `Space` | Toggle selected provider or planning |
| `a` | Toggle all providers in the provider list |
| `b` | Biomedical provider preset (when provider list is focused) |
| `Enter` | Start research or return from results |
| `Esc` / `q` | Cancel, exit, or return from results |
| `j` / `k` or arrows | Scroll results |
| `PgUp` / `PgDn` | Page scroll in results |
| `g` / `G` | Top / bottom of results |
| `s` | Save results markdown (`--output` sets default path) |
| `t` | Save CSV export (results view) |
| `h` | Results home: All pane, scroll to top (results view) |
| `r` | Re-run the same query |
| `Tab` / `[` / `]` | Cycle results panes (All, Answer, Hypotheses, Queries, Sources) |
| `e` | Save JSON export |
| `E` | Re-run with Exhaustive mode (results view) |
| `f` | Follow-up chat: ask about the results, answered only from the retrieved sources with `[n]` citations (results view). In chat, `Enter` asks, `Ctrl+R` turns the typed question into a full new research run, `Esc` returns |
| `?` | Keyboard help overlay |
| Mouse wheel | Scroll results (Windows Terminal, iTerm, etc.) |

## LLM providers (Ollama, cloud, hybrid)

WisDev TUI and `wisdev serve` support three routing modes via `WISDEV_LLM_PROVIDER`:

| Mode | `WISDEV_LLM_PROVIDER` | Behavior |
|------|----------------------|----------|
| **Ollama / local** | `ollama` or `openai-compatible` | Local OpenAI-compatible endpoint only (defaults to `http://127.0.0.1:11434/v1`) |
| **Cloud** | `cloud`, `vertex`, or `gemini` | Vertex/Gemini only (requires GCP credentials) |
| **Hybrid** | `hybrid` or unset with `WISDEV_LLM_BASE_URL` | Tier-split: `light`/`standard` → Ollama first; `heavy`/`structured_high_value` → Vertex/Gemini first; cross-fallback then sidecar |

**Ollama example:**

```powershell
ollama serve
ollama pull llama3.1

$env:WISDEV_LLM_PROVIDER = "ollama"
$env:WISDEV_LLM_BASE_URL = "http://127.0.0.1:11434/v1"
$env:WISDEV_LLM_MODEL = "llama3.1"

.\wisdev.cmd doctor
.\wisdev.cmd tui
```

**Cloud example:**

```powershell
$env:WISDEV_LLM_PROVIDER = "cloud"
$env:GOOGLE_CLOUD_PROJECT = "your-gcp-project"
gcloud auth application-default login

.\wisdev.cmd doctor
.\wisdev.cmd tui
```

**Hybrid example (tier-split local + cloud):**

```powershell
$env:WISDEV_LLM_PROVIDER = "hybrid"
$env:WISDEV_LLM_BASE_URL = "http://127.0.0.1:11434/v1"
$env:WISDEV_LLM_MODEL = "llama3.1"
$env:GOOGLE_CLOUD_PROJECT = "your-gcp-project"

.\wisdev.cmd doctor
.\wisdev.cmd tui
```

| Variable | Purpose |
|----------|---------|
| `WISDEV_LLM_PROVIDER` | `ollama`, `openai-compatible`, `cloud`, `vertex`, `gemini`, or `hybrid` |
| `WISDEV_LLM_BASE_URL` | OpenAI-compatible base URL (Ollama default: `http://127.0.0.1:11434/v1`) |
| `WISDEV_LLM_MODEL` | Default local model name (must be pulled in Ollama) |
| `WISDEV_LLM_MODEL_LIGHT` | Optional local model for light-tier structured calls |
| `WISDEV_LLM_MODEL_STANDARD` | Optional local model for standard-tier structured calls |
| `WISDEV_LLM_MODEL_HEAVY` | Optional local model for heavy-tier structured calls |
| `WISDEV_LLM_API_KEY` | Optional bearer token for remote compatible servers |
| `GOOGLE_CLOUD_PROJECT` | GCP project for Vertex/Gemini in cloud or hybrid mode |

The CLI auto-loads `.env` from the current directory, parent directory, or `WISDEV_DOTENV` without overriding shell env vars. `wisdev doctor` reports the active mode, fallback chain, and backend health. TUI status shows `llm=hybrid:light→ollama/llama3.1|heavy→vertex_ai` when both backends are wired. ADK agent sessions use cloud in hybrid mode (heavy agent loop); structured light/standard calls still route to Ollama when configured.

Offline Ollama adapter smoke:

```powershell
node scripts/ops/hackathon-ollama-smoke.mjs
```

## Flags

| Flag | Effect |
|------|--------|
| `-q` / `--quiet` | Answer only |
| `-v` / `--verbose` | Queries on stderr |
| `-j` / `--json` | JSON output |
| `--offline` | Smoke test without network |
| `--remote` | HTTP orchestrator (`yolo` only) |
| `--long-form` | Extended Introduction and Background sections (`yolo` local mode; same as the TUI Long-form setting) |

## Build binary

```powershell
.\scripts\build-wisdev-cli.ps1 -Version 0.1.0
.\dist\wisdev.exe check
```

`.\wisdev.cmd` and `./wisdev` prefer the same binary as plain `wisdev` on your PATH (for example `go install` → `%USERPROFILE%\go\bin\wisdev.exe`). That keeps `wisdev tui` and `.\wisdev.cmd tui` in sync.

If no `wisdev` is on PATH, the launcher falls back to `dist\wisdev.exe`, then `go run`.

To test a freshly built dist binary explicitly:

```powershell
$env:WISDEV_USE_DIST = "1"
.\wisdev.cmd tui
```

Show which binary runs: `$env:WISDEV_VERBOSE = "1"; .\wisdev.cmd version`
