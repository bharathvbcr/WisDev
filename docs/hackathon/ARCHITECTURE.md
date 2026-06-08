# WisDev — Submission Architecture Diagram

Export this diagram for Devpost (screenshot or [mermaid.live](https://mermaid.live) → PNG/SVG).

---

## System overview

```mermaid
flowchart TB
    subgraph Clients["Clients"]
        CLI["WisDev CLI<br/>wisdev yolo"]
        UI["ScholarLM Web UI<br/>scholarlm-vbcr.web.app"]
        MCPClients["MCP Clients<br/>Cursor · Claude Desktop · ADK agents"]
    end

    subgraph GCP["Google Cloud Platform"]
        subgraph CloudRun["Cloud Run"]
            GW["Rust Gateway<br/>auth · rate limits"]
            GO["Go Orchestrator<br/>WisDev YOLO · ADK Runtime"]
            PY["Python Sidecar<br/>Gemini · embeddings · PDF"]
        end
        SM["Secret Manager"]
        OTEL["Cloud Logging / OpenTelemetry"]
        VTX["Vertex AI · Gemini"]
    end

    subgraph AgentCore["WisDev Agent Core (ADK)"]
        ADK["Google ADK for Go<br/>agent · runner · HITL"]
        LOOP["AutonomousLoop<br/>plan · search · synthesize"]
        MCP["MCP Server<br/>/wisdev/mcp"]
        TOOLS["MCP Tools<br/>wisdevSearchPapers · wisdevEvidenceSearch<br/>wisdevPaperLookup · wisdevAuthorSearch"]
    end

    subgraph External["External Data"]
        APIs["Academic APIs<br/>OpenAlex · arXiv · PubMed · Semantic Scholar · Crossref"]
    end

    CLI --> GO
    UI --> GW --> GO
    MCPClients --> MCP
    GO --> ADK --> LOOP
    ADK --> MCP --> TOOLS
    LOOP --> TOOLS
    GO --> PY --> VTX
    TOOLS --> APIs
    GO --> SM
    GO --> OTEL
    PY --> SM
```

---

## Autonomous YOLO execution flow

```mermaid
sequenceDiagram
    participant User
    participant WisDev as WisDev YOLO (ADK)
    participant MCP as MCP Tools
    participant Search as Academic APIs
    participant Gemini as Gemini (Vertex)

    User->>WisDev: Research question
    WisDev->>Gemini: Plan branches & hypotheses
    loop Until budget exhausted or converged
        WisDev->>MCP: wisdevSearchPapers(query)
        MCP->>Search: Parallel provider fan-out
        Search-->>MCP: Ranked papers
        MCP-->>WisDev: Evidence candidates
        WisDev->>Gemini: Gap analysis & critique
        WisDev->>MCP: wisdevEvidenceSearch(claim)
        MCP-->>WisDev: Grounded snippets
    end
    WisDev->>Gemini: Final synthesis
    WisDev-->>User: Traceable answer + citations
```

---

## Technology map (challenge requirements)

| Requirement | Implementation |
|---|---|
| Gemini | Vertex AI via Python sidecar + ADK Gemini model |
| ADK | `google.golang.org/adk` — agent, runner, function tools, HITL |
| MCP | JSON-RPC 2.0 server at `/wisdev/mcp` + ADK bridge |
| GCP hosted | Cloud Run stack + Firebase Hosting |
| Acts autonomously | WisDev YOLO mode — no intake Q&A required |

---

## Repo layout (open source)

```text
WisDev/
├── orchestrator/          Go agent runtime (ADK, MCP, YOLO loop)
│   ├── cmd/wisdev/        CLI
│   ├── cmd/server/        HTTP API
│   ├── internal/wisdev/   AutonomousLoop, ADKRuntime, MCPServer
│   └── pkg/wisdev/        Public RunYOLO() embedding API
├── sidecar/               Python Gemini / ML primitives
├── config/                wisdev-adk.yaml, wisdev.example.yaml
└── docs/hackathon/        This submission package
```
