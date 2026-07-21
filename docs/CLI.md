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
| `wisdev docgen "topic"` | Search + DocGen: gather grounded papers, then draft a citation-backed document (`report`, `litreview`, or `fullpaper`) |
| `wisdev check` | Health check |
| `wisdev tui` | Interactive terminal UI for local research |
| `wisdev mcp` | MCP stdio server |
| `wisdev setup --write ~/.cursor/mcp.json --binary` | MCP client config (absolute binary; merges) |
| `./scripts/install-skills.sh` | Symlink WisDev skills into Claude + Cursor skill dirs |
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
| DocGen | off | After research, generate a grounded document (space toggles on/off; when focused and on: **i** intent, **f** format, **c** citation style) |

When DocGen is enabled, the settings sub-line shows the current intent, format, and
citation style. The manuscript is written beside the research export as
`{stem}-manuscript.{ext}` (extension follows the selected format).

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
.\wisdev.cmd tui
.\wisdev.cmd tui --query "RAG for scientific literature"
.\wisdev.cmd tui --query "meniscus scaffolds and ACL reconstruction" --biomedical --exhaustive --iterations 9 --autostart
.\wisdev.cmd tui --offline --output result.md
```

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
| `i` / `f` / `c` | When DocGen setting is focused: cycle intent / format / citation style |
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

## Flags

| Flag | Effect |
|------|--------|
| `-q` / `--quiet` | Answer only |
| `-v` / `--verbose` | Queries on stderr |
| `-j` / `--json` | JSON output |
| `--offline` | Smoke test without network |
| `--remote` | HTTP orchestrator (`yolo` only) |
| `--long-form` | Extended Introduction and Background sections (`yolo` local mode; same as the TUI Long-form setting) |
| `--docgen` | After a `yolo` run, also generate a grounded manuscript. Zero-config: auto-saves to `manuscript-<topic>-<timestamp>.<ext>` with an auto citation floor of 10 distinct sources; override with `--doc-output`, `--doc-min-citations`, `--doc-words`, `--doc-flow`, `--doc-format` |

### DocGen controls (`docgen`, and `yolo --docgen` via the `--doc-*` aliases)

DocGen is the headless ScholarDoc document generator. All surfaces (CLI, TUI, MCP,
`pkg/wisdev.GenerateDocument`) route through `internal/docgen`, which dispatches by
**intent** and renders with **citation-style-aware** bibliographies.

#### Document intents

| Intent | Flag | Description |
|--------|------|-------------|
| `fullpaper` | `--intent fullpaper` (default) | Full grounded manuscript pipeline: plan → draft → review → fact-check → references |
| `report` | `--intent report` | Quick Report — fast thematic synthesis from retrieved papers |
| `litreview` | `--intent litreview` | Thematic literature review with grounded in-text citations |

#### Examples

```powershell
# Default: full grounded manuscript
wisdev docgen --offline "transformers in drug discovery"

# Quick report with IEEE bibliography
wisdev docgen --intent report --citation-style ieee --offline "clinical RAG"

# Literature review as HTML
wisdev docgen --intent litreview --format html --offline "graph neural networks"

# Full paper as LaTeX with custom section flow
wisdev docgen --intent fullpaper --format latex --flow introduction,methods,results,discussion `
  --min-citations 10 --words 5000 "battery anodes"

# DOCX export (requires pandoc on PATH)
wisdev docgen --format docx -o paper.docx --offline "topic"

# Replay a fixed corpus (skip retrieval; good for A/B testing pipeline changes)
wisdev docgen --corpus-file papers.json --intent fullpaper "topic"
```

#### Flags

| Flag | Effect |
|------|--------|
| `--words` / `--doc-words` | Target total word count, split across sections (0 = model default) |
| `--min-citations` / `--doc-min-citations` | Minimum distinct sources to cite; also raises the retrieval floor |
| `--flow` / `--doc-flow` | Comma-separated section flow, e.g. `introduction,methods,results,discussion` |
| `--review-rounds` / `--doc-review-rounds` | Max rounds of the agentic generate→review→revise loop (0 = default 2, max 5) |
| `--genre` / `--doc-genre` | Manuscript genre, e.g. `research paper` (default: narrative literature review) |
| `--format` / `--doc-format` | `markdown` (default) \| `latex` \| `html` \| `docx` \| `json` |
| `--intent` / `--doc-intent` | Document type: `fullpaper` (default) \| `report` \| `litreview` |
| `--citation-style` / `--doc-citation-style` | Bibliography style: `apa` (default) \| `mla` \| `chicago` \| `vancouver` \| `ieee` \| `harvard` \| `nature` |
| `--corpus-dump` | After retrieval, save papers as JSON for reproducible re-runs |
| `--corpus-file` | Replay papers from a `--corpus-dump` file instead of live retrieval |
| `--all-references` | List every retrieved source in the bibliography, not only in-text-cited ones (default on for `--corpus-file`) |

Both paths are zero-config: without `--min-citations`/`--doc-min-citations` an
auto floor of 10 distinct sources applies (and raises the retrieval floor), and
`yolo --docgen` without `--doc-output` auto-saves the manuscript and prints the
absolute path. The progress spinner streams live pipeline stages (drafting,
review round *n*, fact-check) so long runs never look stalled.

Section drafting runs an **agentic generate → review → revise loop** (re-review and
rewrite flagged sections each round, stopping on convergence). Manuscript prose
minimizes em-dashes (`—`). The same controls are exposed over MCP on
`wisdevGenerateManuscript` (`words`, `minCitations`, `flow`, `reviewRounds`, `intent`,
`citationStyle`, `format`) — see [MCP_CLIENTS.md](MCP_CLIENTS.md). MCP supports
`markdown`, `json`, `latex`, and `html`; `docx` is CLI-only.

The public Go embedding API `pkg/wisdev.GenerateDocument` exposes the same pipeline
(additive; requires `SetDocumentGenerator` wired at startup — the CLI does this
automatically).

DocGen drafting/review/coordination/fact-check calls go through the sidecar's
`manuscript_llm()` provider selector, independent of the `WISDEV_LLM_*` vars used
by the research loop. Set `MANUSCRIPT_LLM_PROVIDER=local` (or `ollama`) plus
`LOCAL_LLM_BASE_URL`/`LOCAL_LLM_MODEL` (`OLLAMA_BASE_URL` is accepted as an alias)
to draft manuscripts with a local model and no cloud credentials at all; unset
defaults to Gemini/Vertex unless a local server is already configured. See
`.env.example`.

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
