# Judge Demo Access Instructions

Paste this into the Devpost **Testing access** field (fill in credentials before submitting).

---

## Live demo URLs

| Surface | URL |
|---|---|
| **Product UI** | https://scholarlm-vbcr.web.app |
| **Open-source repo** | https://github.com/bharathvbcr/WisDev |
| **API gateway** | `https://rust-gateway-cyucrnqqnq-uc.a.run.app` (auth required) |

---

## Option A — Web UI (recommended for judges)

1. Open https://scholarlm-vbcr.web.app
2. Sign in with the judge account below
3. Enter a research question (e.g. *"mixture of experts for long-context retrieval"*)
4. Start **WisDev / Deep Research** or **Smart Search** mode
5. Observe the agent planning, searching, and synthesizing with evidence

### Judge credentials

```
Email:    judge.wisdev2026@scholarlm.dev
Password: [Devpost private testing field only — do not commit]
```

> **Created** in Firebase Auth (`scholarlm-vbcr`). Store the password in Devpost **Testing access** / private notes only.

From the ScholarLM repo root (requires Application Default Credentials):

```powershell
$env:HACKATHON_JUDGE_EMAIL = "judge.wisdev2026@yourdomain.com"
$env:HACKATHON_JUDGE_PASSWORD = "your-secure-password"
npm run ops:hackathon:judge-user
npm run ops:hackathon:preflight -- --require-judge-auth
```

---

## Option B — CLI (no login, runs locally)

Judges with Go 1.25+ installed can run the open-source agent directly:

```bash
git clone https://github.com/bharathvbcr/WisDev.git
cd WisDev
cp .env.example .env
cd orchestrator
go run ./cmd/wisdev yolo --local --provider openalex,arxiv "What evidence supports RAG for scientific literature?"
```

Offline smoke (no network):

```bash
go run ./cmd/wisdev yolo --local --offline "smoke test query"
```

---

## Option C — MCP endpoint (for technical judges)

With the orchestrator server running:

```bash
cd WisDev/orchestrator
go run ./cmd/server
```

| Endpoint | Method | Purpose |
|---|---|---|
| `http://127.0.0.1:8081/wisdev/mcp` | POST | MCP JSON-RPC (tools/list, tools/call) |
| `http://127.0.0.1:8081/wisdev/mcp/status` | GET | ADK + MCP live status |
| `http://127.0.0.1:8081/.well-known/agent-card.json` | GET | A2A agent discovery |

Example MCP tool call:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "wisdevSearchPapers",
    "arguments": { "query": "transformer retrieval augmented generation", "limit": 5 }
  }
}
```

Legacy alias `scholarlmSearchPapers` is also accepted.

---

## What to look for

- Agent **acts** (multi-step search, not single-shot chat)
- **MCP tools** invoked for retrieval
- **ADK** runtime orchestrates the loop (`config/wisdev-adk.yaml`)
- **Gemini** powers reasoning and synthesis
- Evidence-grounded output with traceable paper references
