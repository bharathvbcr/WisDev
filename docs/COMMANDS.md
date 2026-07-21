# WisDev — Full Command & Option Reference

Complete reference for every `wisdev` CLI command, its flags, and the
environment variables that configure a run. For a short cheat-sheet see
[`CLI.md`](./CLI.md).

## How to invoke

| Context | Command |
|---------|---------|
| Repo root (Windows) | `.\wisdev.cmd <command>` |
| Repo root (Mac/Linux) | `./wisdev <command>` |
| ScholarLM root | `npm run wisdev -- <command>` |
| Make | `make <command>` |
| Installed | `go install ./orchestrator/cmd/wisdev` then `wisdev <command>` |
| Hacking the CLI | `go run ./cmd/wisdev <command>` (from `orchestrator/`) |

A bare question is treated as `search`: `wisdev "your question"` ≡ `wisdev search "your question"`.

## Commands at a glance

| Command | Aliases | What it does |
|---------|---------|--------------|
| `search` / `run` | `ask` | Local research over built-in providers, then synthesize a cited answer |
| `max` | — | **Maximum-depth research** — forces unleashed budgets, long-form, 12 iterations, wide search |
| `docgen` | `docugen` | **Search + DocuGen** — retrieve grounded papers, then generate a ScholarDoc document (`report`, `litreview`, or `fullpaper`) |
| `yolo` | — | Same research engine with full flag control (`--local` default, or `--remote`) |
| `tui` | `ui` | Interactive terminal UI for local research |
| `serve` | — | Start the HTTP orchestrator (port from `$PORT`, default 8081) |
| `mcp` | — | MCP stdio server (academic search tools for IDE agents) |
| `mcp-config` | `setup` | Generate an MCP client config (e.g. `.cursor/mcp.json`) |
| `doctor` | `check` | Environment / orchestrator health check |
| `providers` | `sources` | List the built-in search providers |
| `update` | `upgrade` | Self-update to the latest GitHub release |
| `guide` | `commands` | Print all commands grouped by task (in-CLI version of this doc) |
| `help [cmd]` | `-h`, `--help` | Usage; `help <cmd>` for command detail |
| `version` | `-v`, `--version` | Print the version |

---

## `max` — maximum-depth research

```
wisdev max "your research question"
```

A one-word preset for the most elaborate run. It **forces `WISDEV_UNLEASHED=1`
for the process** (lifts budget/iteration/token/timeout caps and raises the
autonomous-loop minimum-iteration floor) and applies these presets:

| Preset | Value |
|--------|-------|
| `--long-form` | on (extended Introduction + Background) |
| `--stages` | on (streams loop progress to stderr) |
| `--max-iterations` | 12 |
| `--max-search-terms` | 20 |
| `--hits-per-search` | 12 |
| `--max-unique-papers` | 80 |
| `--timeout` | 30m |
| providers | all built-in |
| enhancements | query rewrite, hypotheses, planning all on (defaults) |

Any flag you add overrides the preset, e.g. `wisdev max --provider pubmed "…"`.

---

## `docgen` / `docugen` — search + DocuGen

```
wisdev docgen "your topic"
wisdev docgen --offline "your topic"
wisdev docgen -o paper.md --provider pubmed,arxiv "your topic"
wisdev docgen --intent report --citation-style ieee --offline "clinical RAG"
wisdev docgen --intent litreview --format html "graph neural networks"
wisdev docgen --intent fullpaper --words 4000 --min-citations 20 --flow introduction,methods,results,discussion "your topic"
wisdev docgen --format docx -o paper.docx "your topic"
wisdev docgen --corpus-file papers.json --intent fullpaper "your topic"
```

A one-shot **search + document generation** command. It runs the local research
loop to gather grounded papers, then dispatches through `internal/docgen` by
**intent**:

| Intent | `--intent` | Engine | Output |
|--------|------------|--------|--------|
| `fullpaper` (default) | `fullpaper` | `ManuscriptPipeline` — plan, draft, review, fact-check, peer review, references | Full grounded manuscript with sections, visuals, critique block |
| `report` | `report` | Quick Report synthesis (ported from ScholarDoc) | Thematic overview: Executive Summary, Thematic Analysis, Conclusion |
| `litreview` | `litreview` | Literature-review synthesis with grounded citations | Thematic review: Introduction, Analysis, Gaps, Conclusion |

All intents share citation-style-aware rendering via `internal/citations` (seven
styles). Section prose for `fullpaper` is enriched by the Python sidecar when
reachable (`PYTHON_SIDECAR_HTTP_URL` / `--python-url`) and falls back to grounded
scaffolds otherwise, so `--offline` produces a structured draft with no network access.
Report and litreview also emit offline scaffolds when no LLM backend is available.

| Flag | Default | Description |
|------|---------|-------------|
| `-o`, `--output` | stdout | Write the manuscript markdown to this file |
| `-j`, `--json` | false | Emit the raw manuscript pipeline result as JSON |
| `-q`, `--quiet` | false | Print only the manuscript markdown |
| `-v`, `--verbose` | false | Show search queries + pipeline stages on stderr |
| `--stages` | false | Stream search-loop stage events to stderr |
| `--offline` | false | Disable network providers (grounded scaffold only) |
| `--provider` | all built-in | Comma-separated provider names for retrieval |
| `--domain` | auto | Research domain hint (e.g. `medicine`, `cs`) |
| `--python-url` | resolved | Python sidecar base URL for section enrichment |
| `--timeout` | 10m | Overall timeout |
| `--max-iterations` | 3 (**12 when unleashed**) | Max search-loop iterations |
| `--max-search-terms` | 6 | Max distinct search terms |
| `--hits-per-search` | 5 | Results requested per provider query |
| `--max-unique-papers` | 24 | Cap on papers fed into the manuscript |
| `--no-enhance` | false | Disable AI query grammar/typo/acronym preparation |
| `-f`, `--format` | from `-o`, else markdown | Output format: `markdown` \| `latex` \| `html` \| `docx` \| `json` |
| `--intent` | fullpaper | Document type: `report` \| `litreview` \| `fullpaper` |
| `--citation-style` | apa | Bibliography citation style: `apa` \| `mla` \| `chicago` \| `vancouver` \| `ieee` \| `harvard` \| `nature` |
| `--words` | 0 (model default) | Target total word count, split across sections |
| `--min-citations` | auto 10 (0 for `--corpus-file` replays) | Minimum distinct sources to cite (also raises the retrieval floor) |
| `--flow` | default plan | Comma-separated section flow, e.g. `introduction,methods,results,discussion`. Known ids reuse tuned briefs; unknown ids become generic synthesis sections |
| `--review-rounds` | 2 | Max rounds of the agentic generate→review→revise loop (early-exits on convergence; max 5) |
| `--genre` | narrative literature review | Manuscript genre for `fullpaper`, e.g. `research paper` (controls voice + how the reviewer grades it) |
| `--corpus-dump` | — | After retrieval, write papers as JSON for reproducible re-runs |
| `--corpus-file` | — | Replay papers from a corpus dump instead of live retrieval |
| `--all-references` | off (on for `--corpus-file`) | List every retrieved source in the bibliography, not only in-text-cited ones |

> **JSON output:** `fullpaper` + `--json` emits the raw `ManuscriptPipelineResult`;
> `report` / `litreview` + `--json` emit the canonical `Document` envelope.
>
> **DOCX:** requires `pandoc` on PATH. MCP `wisdevGenerateManuscript` does not support
> `docx` (text-based tool); use the CLI for DOCX export.

> Section drafting runs an **agentic generate → review → revise loop**: each round
> re-reviews the draft and rewrites the sections the review flagged (re-grounding and
> re-verifying), stopping as soon as a round makes no changes.
>
> Manuscript prose minimizes em-dashes (`—`) — enforced by both the writer prompt and a deterministic post-process.
>
> The progress spinner streams live pipeline stages (planning, drafting, review
> round *n*, fact-check, rendering) so long runs never look stalled; `-o` prints
> the saved manuscript's absolute path when done.

---

## `search` / `run` / `ask` / `yolo` — research

`search`/`run`/`ask` are shortcuts that run `yolo --local`. Use `yolo` directly
for the full flag set below. The query is the positional argument.

```
wisdev "question"
wisdev yolo --local --long-form --max-iterations 8 "question"
wisdev yolo --remote --url http://localhost:8081 "question"
```

| Flag | Default | Description |
|------|---------|-------------|
| `-q`, `--quiet` | false | Print only the final answer |
| `-v`, `--verbose` | false | Print queries + top papers and the stage log on stderr |
| `--stages` | false | Stream research-loop stage events to stderr (local) |
| `-j`, `--json` | false | Emit the raw JSON response |
| `--local` | true | Run the loop in-process (default) |
| `--remote` | false | Call a running orchestrator over HTTP instead |
| `--url` | `$WISDEV_ORCHESTRATOR_URL` or `http://127.0.0.1:8081` | Orchestrator base URL (remote) |
| `--timeout` | 5m | Overall request timeout |
| `--offline` | false | Disable network providers (smoke test) |
| `--provider` | all built-in | Comma-separated provider names (e.g. `pubmed,arxiv`) |
| `--domain` | auto | Research domain hint (e.g. `medicine`, `cs`) |
| `--project-id` | — | Project / session id for local mode |
| `--max-iterations` | 3 (**12 when unleashed**) | Max autonomous-loop iterations |
| `--max-search-terms` | 6 | Max distinct search terms |
| `--hits-per-search` | 5 | Results requested per provider query |
| `--max-unique-papers` | 20 | Cap on unique papers retained |
| `--disable-planning` | false | Skip programmatic query planning |
| `--disable-hypotheses` | false | Skip hypothesis generation |
| `--no-enhance` | false | Disable AI query grammar/typo/acronym preparation |
| `--long-form` | false | Add extended Introduction + Background sections |
| `--docgen` | false | After research, also generate a grounded manuscript from the retrieved papers (same engine as `wisdev docgen`; local mode only) |
| `--doc-format` | markdown | Manuscript format when `--docgen` is set: `markdown` \| `latex` \| `html` \| `docx` \| `json` |
| `--doc-intent` | fullpaper | Document type when `--docgen` is set: `report` \| `litreview` \| `fullpaper` |
| `--doc-citation-style` | apa | Bibliography citation style when `--docgen` is set |
| `--doc-output` | auto `manuscript-<topic>-<timestamp>.<ext>` (stdout for `--json`) | Write the generated manuscript to this file (implies `--docgen`); the saved absolute path is printed |
| `--doc-words` | 0 | Target total word count for the `--docgen` manuscript |
| `--doc-min-citations` | auto 10 | Minimum distinct sources the `--docgen` manuscript should cite (also raises the research paper floor) |
| `--doc-flow` | default plan | Comma-separated section flow for `--docgen`, e.g. `introduction,methods,results,discussion` |
| `--doc-review-rounds` | 2 | Max rounds of the agentic generate→review→revise loop for `--docgen` (max 5) |
| `--doc-genre` | narrative literature review | Manuscript genre for `--docgen`, e.g. `research paper` |

> Under `WISDEV_UNLEASHED=1`, the loop also enforces a **minimum of 5 iterations**
> (capped by `--max-iterations`) so it does not converge on the first pass.

---

## `tui` / `ui` — interactive terminal UI

```
wisdev tui
wisdev tui --query "question" --autostart
```

| Flag | Default | Description |
|------|---------|-------------|
| `--offline` | false | Run without network providers |
| `--autostart` | false | Start research immediately when a query is set |
| `--query` | — | Pre-fill the research question |
| `--output` | — | Save results to this markdown file (also `s` in results) |
| `--iterations` | 0 | Pre-set max loop iterations (1–12) |
| `--exhaustive` | false | Run all max iterations before any early stop |
| `--biomedical` | false | Start with the biomedical provider preset |
| `--cs` | false | Start with the computer-science provider preset |
| `--no-enhance` | false | Disable query grammar/typo enhancement |
| `--fresh` | false | Bypass the search cache for a fresh retrieval pass |
| `--batch` | — | File of newline-delimited queries for batch mode |
| `--no-bell` | false | Disable the terminal bell on completion |

**Key bindings (input screen):** `Tab`/`Shift+Tab` move focus · `Enter` start ·
`Space` toggle setting/provider · `1`–`9` / `+` / `-` set max iterations ·
`b`/`c`/`p`/`g`/`x` provider presets · `a` toggle all · `/` filter providers ·
`Ctrl+P`/`Ctrl+N` query history · `Ctrl+Z`/`Ctrl+Y` undo/redo ·
`Ctrl+W` delete word · `Ctrl+V` paste · `Ctrl+O` recent saved runs ·
`Esc Esc` exit.

**DocGen settings (when setting #7 is focused and enabled):** `i` cycle intent
(`fullpaper` / `report` / `litreview`) · `f` cycle format (`markdown` / `latex` /
`html` / `json`) · `c` cycle citation style (`apa` / `mla` / `chicago` / `vancouver` /
`ieee` / `harvard` / `nature`). The manuscript is saved as `{export-stem}-manuscript.{ext}`.

---

## `serve`

```
wisdev serve
```

Starts the HTTP orchestrator. Port comes from `$PORT` (default `8081`); the
internal metrics listener uses the internal port. No command flags — configure
via environment variables (below).

## `mcp` — MCP stdio server

```
wisdev mcp [--provider openalex,arxiv] [--offline]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--provider` | — | Comma-separated built-in provider names |
| `--offline` | false | Disable network-backed providers |

## `mcp-config` / `setup`

```
wisdev setup --write .cursor/mcp.json
```

| Flag | Default | Description |
|------|---------|-------------|
| `--provider` | `openalex,arxiv` | Providers for the generated `wisdev mcp` invocation |
| `--write` | stdout | Write the config to this path |
| `--binary` | false | Reference `wisdev` on PATH instead of `go run` |

## `doctor` / `check`

```
wisdev check [--json] [--timeout 10s]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | false | Emit a JSON report |
| `--timeout` | 10s | Orchestrator probe timeout |

## `providers` / `sources`

```
wisdev sources [--json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | false | Emit a JSON array |

## `update` / `upgrade`

```
wisdev update            # install the latest release
wisdev update --check    # only report whether one is available
```

| Flag | Default | Description |
|------|---------|-------------|
| `--check` | false | Only check whether an update is available |
| `--version` | latest | Install a specific release tag |
| `--force` | false | Replace the binary even from a dev build / for a downgrade |
| `--timeout` | 2m | Download timeout |

---

## Environment variables

### Run behavior
| Variable | Purpose |
|----------|---------|
| `WISDEV_UNLEASHED` | `1`/`true` lifts budget/iteration/token/policy caps, scales timeouts, and enforces a ≥5 iteration floor. `wisdev max` sets this automatically. |
| `WISDEV_MODE` | Research mode (e.g. `yolo`). |
| `WISDEV_CONFIG` | Path to the agent YAML (default `config/wisdev.example.yaml`). |
| `WISDEV_DOTENV` | Explicit `.env` path to load first. |
| `WISDEV_STATE_DIR` / `WISDEV_JOURNAL_PATH` | Local state and journal locations. |
| `WISDEV_ORCHESTRATOR_URL` | Default base URL for `--remote`. |
| `WISDEV_PLAIN` / `NO_COLOR` | Disable ANSI color. |

### LLM provider
| Variable | Purpose |
|----------|---------|
| `WISDEV_LLM_PROVIDER` | `vertex` (cloud-only Gemini), `ollama`/`openai-compatible` (local), `hybrid`, or unset (auto). |
| `WISDEV_LLM_BASE_URL` | OpenAI-compatible endpoint (e.g. Ollama `http://127.0.0.1:11434/v1`). |
| `WISDEV_LLM_MODEL` | Local model name (e.g. `gemma2:9b`). |
| `WISDEV_LLM_API_KEY` | API key for the OpenAI-compatible endpoint. |
| `SCHOLAR_MODELS_CONFIG` | Path to `scholar_models.json` (tier → Gemini model map). |

### Google Cloud / Vertex (cloud mode)
| Variable | Purpose |
|----------|---------|
| `GOOGLE_CLOUD_PROJECT` | Project that owns Vertex calls and is billed. |
| `GOOGLE_CLOUD_LOCATION` | Vertex region; use `global` for Gemini 3.x models. |
| `GOOGLE_CLOUD_QUOTA_PROJECT` | Quota/billing attribution project (overrides the ADC default). |
| `GOOGLE_API_KEY` / `GEMINI_API_KEY` | Gemini Developer API key (also resolvable from Secret Manager under these names). |

### Sidecar / infra
| Variable | Purpose |
|----------|---------|
| `PYTHON_SIDECAR_HTTP_URL` / `PYTHON_SIDECAR_GRPC_ADDR` | Python capability sidecar endpoints. |
| `PORT` | `wisdev serve` public port (default 8081). |
| `TEMPORAL_ENABLED` / `TEMPORAL_ADDRESS` / … | Optional Temporal durable execution. |

---

## Examples

```bash
# Quickest: a bare question (local, all providers)
wisdev "CRISPR off-target detection methods"

# Maximum depth, billed to the configured cloud project
wisdev max "therapeutic strategies targeting tau aggregation in Alzheimer's"

# Targeted providers, long-form, watch the stages
wisdev yolo --local --provider pubmed,clinicaltrials --long-form --stages \
  --max-iterations 8 "GLP-1 agonists and cardiovascular outcomes"

# Research + document generation in one run
wisdev yolo --docgen --doc-intent report --doc-citation-style ieee "clinical RAG"
wisdev docgen --intent litreview --format html --offline "graph neural networks"

# Interactive
wisdev tui --biomedical

# Health check / providers
wisdev check
wisdev sources
```
