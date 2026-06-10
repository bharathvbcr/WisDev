# Google for Startups AI Agents Challenge — Submission Package

**Deadline:** June 11, 2026, 5:00 PM PT / 7:00 PM CDT  
**Project:** WisDev — Autonomous Research Agent  
**Track:** Build (Net-New Agents)  
**Region:** AMERS  

---

## Before you submit (2 min)

```powershell
cd scholarlm
npm run ops:hackathon:record-prep
npm run ops:hackathon:submit-gate -- --require-video-url
```

Judge quick test (no login):

```powershell
git clone https://github.com/bharathvbcr/WisDev.git
cd WisDev
.\wisdev.cmd check
.\wisdev.cmd "What evidence supports RAG for scientific literature?"
```

---

## Devpost fields (copy/paste)

### Project title

**WisDev — Autonomous Academic Research Agent (ADK + MCP on GCP)**

### Tagline (one line)

An ADK-powered agent that doesn't just answer research questions — it plans, searches 200M+ papers via MCP, verifies evidence, and synthesizes traceable answers.

### Project URL (live demo)

https://scholarlm-vbcr.web.app

### Code repository

https://github.com/bharathvbcr/WisDev

### Video URL

`[UPLOAD TO YOUTUBE/VIMEO — see DEMO_SCRIPT.md — paste link here]`

> Before Devpost: `npm run ops:hackathon:submit-gate -- --require-video-url`

### Theme

**Build (Net-New Agents)**

### Region

**AMERS**

---

## Description (Devpost project story)

### The problem

Researchers and R&D teams lose **10+ hours per week** on literature discovery: manually searching Google Scholar, PubMed, arXiv, and dozens of databases, then reading hundreds of abstracts to find what's relevant. Keyword search misses semantic connections; synthesis is manual and error-prone.

### Our solution — WisDev

**WisDev** is an autonomous research agent that turns a natural-language question into an evidence-grounded synthesis. It is the open-source agent runtime behind **ScholarLM**, our startup research platform.

Given a research question, WisDev:

1. **Plans** a multi-step research agenda (hypotheses, retrieval branches, gap checks)
2. **Acts** via MCP-connected tools — searching papers, looking up metadata, retrieving evidence snippets
3. **Reasons** with **Gemini** on **Google Cloud Vertex AI**
4. **Synthesizes** a traceable answer with citations and explicit evidence gaps

This is not a chatbot. WisDev runs a bounded **YOLO research loop**:

```text
Query → Plan → Search → Analyze → Synthesize → Report
```

### Why Google stack

| Technology | How we use it |
|---|---|
| **Gemini** | Structured reasoning, gap analysis, hypothesis evaluation, synthesis |
| **Google ADK (Go)** | Canonical agent runtime — `agent.New`, tool orchestration, HITL gates, A2A agent card |
| **MCP** | Open tool protocol exposing `wisdevSearchPapers`, `wisdevEvidenceSearch`, `wisdevPaperLookup`, `wisdevAuthorSearch` to Cursor, Claude Desktop, and external ADK agents |
| **Google Cloud** | Cloud Run deployment (Go orchestrator + Python Gemini sidecar), Secret Manager, OpenTelemetry, Firebase Hosting for the product UI |

### Open source + product

- **Open source:** https://github.com/bharathvbcr/WisDev — terminal-first YOLO agent, CLI, MCP server, ADK bridge
- **Product:** https://scholarlm-vbcr.web.app — ScholarLM wraps WisDev for researchers (search, WisDev sessions, systematic review, drafting)

### Business case

- **Market:** 10M+ researchers globally; literature discovery is the #1 academic productivity bottleneck
- **Customer:** PhD students, university labs, biotech R&D, policy research teams
- **Model:** Freemium search + Pro/Enterprise for AI re-ranking, deep WisDev runs, team workspaces
- **Moat:** Multi-provider academic search orchestration + evidence-grounded agent loop + traceable synthesis (not generic RAG chat)

### What we built for this challenge

- Net-new autonomous agent on **Google ADK for Go**
- **MCP tool server** with ADK function-tool bridge plus **stdio transport** (`wisdev mcp`) for Cursor, Claude Code, and Codex
- Production deployment on **GCP Cloud Run** with observability and reliability baselines
- Open-sourced the runtime so the agent is inspectable and extensible

---

## Judging criteria mapping

| Criterion (weight) | Evidence |
|---|---|
| **Technical implementation (30%)** | ADK runtime, MCP JSON-RPC server, Gemini sidecar, multi-provider search, autonomous loop with replanning |
| **Business case (30%)** | ScholarLM startup, researcher pain point, live product, monetization path |
| **Innovation & creativity (20%)** | Evidence-grounded YOLO loop, belief-state feedback, MCP+ADK bridge for academic tools |
| **Demo & presentation (20%)** | Video + architecture diagram + live app + OSS repo |

---

## Submission checklist

See live tracker: [`STATUS.md`](./STATUS.md)

- [ ] Record demo video (see `DEMO_SCRIPT.md`)
- [ ] Upload video to YouTube (unlisted) or Vimeo
- [ ] Export architecture diagram: `npm run ops:hackathon:diagram` (from ScholarLM repo root)
- [ ] Create judge demo account: `npm run ops:hackathon:judge-user` (see `JUDGE_ACCESS.md`)
- [ ] Verify stack: `npm run ops:hackathon:preflight -- --require-judge-auth`
- [ ] MCP stdio smoke: `npm run ops:hackathon:mcp-stdio`
- [ ] Paste video URL into Devpost
- [ ] Submit before deadline (7:00 PM CDT June 11)
