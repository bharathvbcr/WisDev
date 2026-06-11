# WisDev ARC — Architecture & Project Overview

**ScholarLM** is a closed prototype research platform. **WisDev ARC** is the
open-source autonomous research agent that powers it (Apache-2.0). The *same*
agent runs hosted on Google Cloud and locally as a single binary / TUI / MCP server.

- **Live product:** https://scholarlm-vbcr.web.app
- **Open-source agent:** https://github.com/bharathvbcr/WisDev
- **Install in one line:** `curl -fsSL https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.sh | bash`

---

## Architecture

```mermaid
flowchart TB
    subgraph CLIENTS["Entry points"]
        UI["ScholarLM Web App<br/><i>React 19 · closed product</i>"]
        TUI["WisDev CLI / TUI<br/><i>single Go binary · open source</i>"]
        MCPC["MCP clients<br/><i>Cursor · Claude</i>"]
    end

    subgraph AGENT["WisDev ARC agent — Google ADK for Go"]
        direction TB
        RUN["ADK Runner<br/><i>llmagent · toolconfirmation (HITL) · retryandreflect</i>"]
        subgraph LOOP["YOLO research loop"]
            direction LR
            P["Plan"] --> S["Search"] --> A["Analyze"] --> C["Critique"] --> Y["Synthesize"]
        end
        RUN --> LOOP
        A2A["A2A remoteagent<br/><i>remote delegation</i>"]
        RUN -.-> A2A
    end

    subgraph TOOLS["Tools & models"]
        FT["ADK function tools"]
        MCPS["MCP server (JSON-RPC / stdio)<br/>wisdevSearchPapers · wisdevEvidenceSearch<br/>wisdevPaperLookup · wisdevAuthorSearch"]
        GEM["Gemini via Vertex AI<br/><i>light · standard · heavy tiers</i>"]
        RAG["Hybrid RAG + CAG pipeline"]
    end

    subgraph DATA["Academic data sources — 200M+ papers"]
        OA["OpenAlex"]
        AX["arXiv"]
        PM["PubMed"]
        DB["DBLP"]
        SS["Semantic Scholar"]
    end

    subgraph CLOUD["Google Cloud deployment (hosted path)"]
        GO["Cloud Run — Go orchestrator"]
        PY["Cloud Run — Python sidecar (LLM/ML)"]
        FH["Firebase Hosting"]
        FS["Firestore"]
    end

    UI --> FH --> GO --> RUN
    TUI --> RUN
    MCPC --> MCPS
    LOOP --> FT
    LOOP --> RAG
    RUN --> GEM
    FT --> MCPS
    MCPS --> DATA
    RAG --> DATA
    GO <--> PY
    PY --> GEM
    GO --> FS

    classDef product fill:#1f2937,stroke:#ef4444,color:#fff;
    classDef agent fill:#7f1d1d,stroke:#ef4444,color:#fff;
    classDef tool fill:#111827,stroke:#9ca3af,color:#e5e7eb;
    classDef data fill:#0f172a,stroke:#38bdf8,color:#e5e7eb;
    classDef cloud fill:#052e16,stroke:#22c55e,color:#e5e7eb;
    class UI,TUI,MCPC product;
    class RUN,P,S,A,C,Y,A2A agent;
    class FT,MCPS,GEM,RAG tool;
    class OA,AX,PM,DB,SS data;
    class GO,PY,FH,FS cloud;
```

**Three ways in, one agent.** The hosted web app, the local CLI/TUI, and any MCP
client all drive the same WisDev ARC runtime. ADK orchestrates the autonomous
YOLO loop (Plan → Search → Analyze → Critique → Synthesize) with human-in-the-loop
tool confirmation, retry/reflect self-correction, and A2A remote delegation.
Gemini via Vertex AI handles planning, critique, and long-form synthesis.

---

## Problem to solve
Researchers waste hours on academic search: broad keyword queries return mostly
irrelevant results, evidence is scattered across siloed databases (PubMed, arXiv,
OpenAlex, DBLP), and synthesizing a defensible, citation-grounded answer is manual
and slow. Existing AI tools hallucinate citations and act as a black box — you
can't see *why* a claim is supported, or run the agent on your own terms.

## Our solution
Given a question, WisDev runs a fully autonomous loop: **plan → parallel
multi-source search → rank by evidence → critique (gap & contradiction analysis)
→ synthesize a traceable, cited answer.** It searches 200M+ papers across
domain-specific APIs, verifies evidence before asserting it, and produces
publication-ready output with real citations. ScholarLM (hosted) is the polished
front end; WisDev ARC (the agent) is open source so anyone can run it, inspect it,
wire it into their own stack via MCP, and improve it.

## Technologies used
- **Google ADK for Go** — `llmagent`, `runner`, `functiontool`, `toolconfirmation`
  (HITL), `retryandreflect`, `remoteagent` (A2A)
- **Gemini** (light/standard/heavy) via **Vertex AI**
- **Model Context Protocol (MCP)** — JSON-RPC server over stdio, 4 academic-search tools
- **Google Cloud Run** (Go orchestrator + Python sidecar), **Firebase Hosting**, **Firestore**
- Hybrid **RAG + CAG** retrieval; Go 1.25; React 19 + Vite 6 (thin display layer)

## Data sources
OpenAlex, arXiv, PubMed, DBLP, Semantic Scholar (200M+ works, domain-routed).
MCP tools: `wisdevSearchPapers`, `wisdevEvidenceSearch`, `wisdevPaperLookup`,
`wisdevAuthorSearch`. All public/openly-licensed academic APIs.

## Findings and learnings
- ADK for Go is young but production-capable — a multi-step autonomous loop with
  HITL confirmation and retry/reflect compiles to a fast, dependency-free single binary.
- MCP is the unlock for distribution: the same tools that power the product run in
  Cursor/Claude/CI, not just our UI.
- Bridging MCP tools into ADK function tools by hand was the biggest friction point.
- Critique-before-synthesis materially improves trust — an explicit
  gap/contradiction pass catches unsupported claims a single-shot RAG answer asserts.
- Provider-agnostic paid off: the same loop runs on Gemini/Vertex in the cloud and
  on a local model offline for CI smoke tests.

## Third-party integrations
OpenAlex, arXiv, PubMed, DBLP, Semantic Scholar — public, openly-licensed APIs used
within their terms. No proprietary SDKs, datasets, or restricted content. Agent
runtime is Apache-2.0.

## Testing access
- **Live demo:** https://scholarlm-vbcr.web.app — anonymous guest login works, no credentials needed.
- **Run the agent locally:** `curl -fsSL https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.sh | bash` (Windows: `irm https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.ps1 | iex`), then `wisdev "your research question"`.
