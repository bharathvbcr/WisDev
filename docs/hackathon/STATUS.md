# Hackathon Submission Status

**Challenge:** Google for Startups AI Agents Challenge  
**Deadline:** June 11, 2026, 7:00 PM CDT  
**Track:** Build (Net-New Agents)  
**Last preflight:** PASS (11/11) — production + MCP stdio + CLI launcher

---

## Code & OSS (done)

| Item | Status |
|------|--------|
| WisDev OSS repo published | ✅ https://github.com/bharathvbcr/WisDev |
| MCP tools rebranded (`wisdev*`) | ✅ |
| MCP stdio for Cursor / Claude / Codex | ✅ `.\wisdev.cmd mcp` |
| Simplified CLI (`wisdev "question"`, `check`, `setup`) | ✅ |
| CLI launcher (`wisdev.cmd`, `npm run wisdev`) | ✅ |
| CLI UX (spinner, `-q`/`-v`/`-j`, grouped help) | ✅ |
| Hackathon demo + record prep | ✅ `ops:hackathon:demo-cli`, `ops:hackathon:record-prep` |
| OSS sync script | ✅ `wisdev-agent-os/scripts/sync-wisdev-oss.ps1` |
| ADK + MCP + YOLO runtime | ✅ |
| Submission doc package | ✅ `docs/hackathon/*` |
| Live MCP status in ScholarLM UI | ✅ |
| Hackathon landing banner | ✅ Off (`hackathonSubmissionBanner: false`) |
| Preflight script | ✅ `npm run ops:hackathon:preflight` |
| Submit gate script | ✅ `npm run ops:hackathon:submit-gate` |
| MCP stdio smoke | ✅ `npm run ops:hackathon:mcp-stdio` |
| Judge user creation script | ✅ `npm run ops:hackathon:judge-user` |
| Diagram export script | ✅ `npm run ops:hackathon:diagram` |
| Production deploy hardening | ✅ CORS + `ops:verify:cloudrun` guards |

---

## Production (verified)

| Check | Status |
|-------|--------|
| Live demo | ✅ https://scholarlm-vbcr.web.app |
| Rust gateway health/readiness | ✅ HTTP 200 |
| Go orchestrator readiness | ✅ HTTP 200 |
| Judge Firebase sign-in | ✅ `judge.wisdev2026@scholarlm.dev` |
| Authenticated MCP status | ✅ 4 tools, ADK bridge enabled |
| MCP stdio local smoke | ✅ initialize + tools/list |
| Architecture diagram asset | ✅ `assets/architecture-overview.png` |

---

## Manual (your action)

| Item | Status | Command / doc |
|------|--------|----------------|
| Record demo video | ⬜ | `DEMO_SCRIPT.md` |
| Upload video (YouTube/Vimeo) | ⬜ | Paste URL in `SUBMISSION.md` |
| Run submit gate before Devpost | ⬜ | `npm run ops:hackathon:submit-gate` |
| Submit Devpost | ⬜ | Copy from `SUBMISSION.md` + judge password in private field |

---

## Quick commands

```powershell
cd C:\Users\bhara\Downloads\Code\scholarlm

# Full production + judge + MCP verification
npm run ops:hackathon:preflight -- --require-judge-auth

# MCP stdio smoke only (no network)
npm run ops:hackathon:mcp-stdio

# Rehearse hackathon video CLI sequence
npm run ops:hackathon:demo-cli
npm run ops:hackathon:demo-cli -- --offline

# One-shot prep before recording (offline demo + checklist)
npm run ops:hackathon:record-prep

# Cursor MCP config example
# docs/examples/cursor-mcp.json  (see docs/MCP_CLIENTS.md)

# Pre-Devpost checklist (preflight + video URL + deadline)
npm run ops:hackathon:submit-gate

# After pasting video URL, require it before submit
npm run ops:hackathon:submit-gate -- --require-video-url
```
