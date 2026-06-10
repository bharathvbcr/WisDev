# WisDev MCP — Claude Code, Cursor, Codex

WisDev exposes academic search tools over **MCP stdio** for local agent clients.

## Tools

| Tool | Purpose |
|------|---------|
| `wisdevSearchPapers` | Multi-provider paper search |
| `wisdevPaperLookup` | Single-paper metadata by ID/DOI/arXiv |
| `wisdevEvidenceSearch` | Claim-grounded evidence snippets |
| `wisdevAuthorSearch` | Papers by author ID |

Legacy `scholarlm*` aliases are accepted on `tools/call`.

## Run the stdio server

```powershell
cd wisdev-arc
.\wisdev.cmd mcp
```

```bash
cd wisdev-arc
./wisdev mcp
```

Optional flags: `.\wisdev.cmd mcp --provider openalex,arxiv` or `--offline`

The process reads JSON-RPC from **stdin** and writes responses to **stdout**. Do not print logs to stdout.

## Cursor

```powershell
cd wisdev-arc
.\wisdev.cmd setup --write .cursor\mcp.json
```

Or copy [`docs/examples/cursor-mcp.json`](examples/cursor-mcp.json). Minimal hand-written config:

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "powershell",
      "args": ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "C:\\path\\to\\wisdev-arc\\scripts\\wisdev.ps1", "mcp"],
      "cwd": "C:\\path\\to\\wisdev-arc"
    }
  }
}
```

After `make build-cli`:

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "C:\\path\\to\\wisdev-arc\\dist\\wisdev.exe",
      "args": ["mcp"]
    }
  }
}
```

## Claude Desktop / Claude Code

```json
{
  "mcpServers": {
    "wisdev": {
      "command": "/absolute/path/to/wisdev-arc/wisdev",
      "args": ["mcp"]
    }
  }
}
```

## Full research loop vs MCP tools

MCP provides **search tools only**. For the autonomous loop:

```powershell
.\wisdev.cmd "your research question"
```

Or use ScholarLM: https://scholarlm-vbcr.web.app

## HTTP MCP (remote)

- Local server: `http://127.0.0.1:8081/wisdev/mcp`
- Prod gateway: `https://rust-gateway-cyucrnqqnq-uc.a.run.app/wisdev/mcp` (auth required)
