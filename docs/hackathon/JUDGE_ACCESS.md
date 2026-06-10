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

---

## Option B — CLI (no login, runs locally)

```bash
git clone https://github.com/bharathvbcr/WisDev.git
cd WisDev
./wisdev check
./wisdev "What evidence supports RAG for scientific literature?"
```

Windows:

```powershell
.\wisdev.cmd check
.\wisdev.cmd "What evidence supports RAG for scientific literature?"
```

Offline smoke:

```powershell
.\wisdev.cmd demo --offline
```

Windows binary (no Go):

```powershell
.\scripts\build-wisdev-cli.ps1
.\dist\wisdev.exe check
```

---

## Option C — MCP (for technical judges)

```powershell
cd WisDev
.\wisdev.cmd setup --write .cursor\mcp.json
.\wisdev.cmd mcp
```

See [`docs/MCP_CLIENTS.md`](../MCP_CLIENTS.md).

### HTTP (orchestrator server running)

```powershell
.\wisdev.cmd serve
```

| Endpoint | Method | Purpose |
|---|---|---|
| `http://127.0.0.1:8081/wisdev/mcp` | POST | MCP JSON-RPC |
| `http://127.0.0.1:8081/wisdev/mcp/status` | GET | ADK + MCP status |

---

## What to look for

- Agent **acts** (multi-step search, not single-shot chat)
- **MCP tools** invoked for retrieval
- **ADK** runtime orchestrates the loop
- **Gemini** powers reasoning and synthesis
- Evidence-grounded output with traceable paper references
