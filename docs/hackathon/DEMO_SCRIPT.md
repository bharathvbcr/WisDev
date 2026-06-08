# WisDev Hackathon Demo Script (~3 minutes)

Record at **1080p**, show your face OR terminal + browser (either works). Upload to **YouTube (Unlisted)** or **Vimeo**.

---

## Pre-flight (before recording)

```powershell
cd C:\Users\bhara\Downloads\Code\WisDev\orchestrator

# Terminal 1 — local YOLO smoke (no server needed)
go run ./cmd/wisdev yolo --local --provider openalex,arxiv "What evidence supports mixture-of-experts for long-context retrieval?"

# Optional Terminal 2 — MCP status (if server running)
go run ./cmd/server
# curl http://127.0.0.1:8081/wisdev/mcp/status
```

Have ready:
- Browser tab: https://scholarlm-vbcr.web.app (logged in as judge demo user)
- Terminal with WisDev CLI
- Architecture diagram slide (from `ARCHITECTURE.md`)

---

## Scene 1 — Hook (0:00–0:25)

**[Screen: ScholarLM homepage or architecture diagram]**

> "Researchers waste ten hours a week finding papers. WisDev is an autonomous research agent — built on Google's Agent Development Kit — that doesn't just chat. It plans, searches, retrieves evidence, and synthesizes traceable answers."

---

## Scene 2 — The problem (0:25–0:45)

**[Screen: typical search UI vs agent trace]**

> "Keyword search gives you a list. WisDev gives you an executed research workflow: hypotheses, multi-source retrieval, gap analysis, and a cited synthesis."

---

## Scene 3 — CLI YOLO demo (0:45–1:45) ⭐ CORE

**[Screen: terminal — full screen, large font]**

Run:

```powershell
go run ./cmd/wisdev yolo --local --provider openalex,arxiv "What evidence supports retrieval-augmented generation for scientific literature?"
```

**Narrate while it runs:**

> "I'm giving WisDev a research question. Watch the autonomous loop: it plans search branches, calls academic providers in parallel, evaluates evidence gaps, and converges on a synthesis — no manual question flow."

**Point out in output (if visible):**
- Iterations / papers found
- Executed queries
- Final answer with evidence framing

---

## Scene 4 — MCP + ADK (1:45–2:20)

**[Screen: GitHub WisDev repo → `orchestrator/internal/wisdev/mcp_server.go` OR live `/wisdev/mcp/status`]**

> "WisDev exposes academic tools through the Model Context Protocol — search papers, lookup metadata, retrieve evidence for claims. These are wired into Google ADK as native function tools, so any ADK agent or MCP client like Cursor can call them securely."

Show tool names:
- `wisdevSearchPapers`
- `wisdevEvidenceSearch`
- `wisdevPaperLookup`
- `wisdevAuthorSearch`

---

## Scene 5 — GCP production (2:20–2:45)

**[Screen: architecture diagram OR Cloud Run console]**

> "The full stack runs on Google Cloud — Cloud Run for the Go orchestrator and Python Gemini sidecar, Vertex AI for reasoning, Secret Manager for credentials, and OpenTelemetry for production tracing."

---

## Scene 6 — ScholarLM product + close (2:45–3:00)

**[Screen: scholarlm-vbcr.web.app — start a WisDev/deep research flow]**

> "ScholarLM is our startup product built on WisDev — the open-source runtime is on GitHub. WisDev turns research questions into evidence-grounded answers using Gemini, ADK, and MCP on GCP. Thank you."

---

## Recording tips

- Use **OBS** or **Windows Game Bar** (Win+G)
- Terminal font size **18+**
- Hide notifications
- If CLI is slow, pre-run once and narrate over a trimmed recording
- Keep video **under 3 minutes** — judges watch dozens of entries

---

## YouTube upload settings

- Visibility: **Unlisted**
- Title: `WisDev — Autonomous Research Agent | Google ADK + MCP | AI Agents Challenge 2026`
- Description: link to https://github.com/bharathvbcr/WisDev and https://scholarlm-vbcr.web.app
