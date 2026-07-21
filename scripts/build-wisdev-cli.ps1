param(
    [string]$Version = "dev",
    [string]$OutputDir = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Orchestrator = Join-Path $RepoRoot "orchestrator"
if (-not $OutputDir) {
    $OutputDir = Join-Path $RepoRoot "dist"
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$out = Join-Path $OutputDir "wisdev.exe"

Push-Location $Orchestrator
try {
    go build -ldflags "-X main.version=$Version" -o $out ./cmd/wisdev
} finally {
    Pop-Location
}

& $out check --json | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Built binary failed wisdev check smoke"
}

Write-Host "Built $out (version $Version)"
Write-Host "Install: copy to a directory on PATH, or run directly."
Write-Host "Demo:     $out demo --offline"
Write-Host "MCP config: $out mcp-config --write `$HOME\.cursor\mcp.json --binary"
