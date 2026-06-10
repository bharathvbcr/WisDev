param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$WisdevArgs
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Orchestrator = Join-Path $RepoRoot "orchestrator"
$DistExe = Join-Path $RepoRoot "dist\wisdev.exe"

function Invoke-WisdevBinary {
    param([string]$BinaryPath)
    if ($env:WISDEV_VERBOSE -eq "1") {
        Write-Host "wisdev: using $BinaryPath" -ForegroundColor DarkGray
    }
    & $BinaryPath @WisdevArgs
    exit $LASTEXITCODE
}

# Force dist\wisdev.exe (e.g. after scripts\build-wisdev-cli.ps1): $env:WISDEV_USE_DIST=1
if ($env:WISDEV_USE_DIST -eq "1") {
    if (Test-Path -LiteralPath $DistExe) {
        Invoke-WisdevBinary $DistExe
    }
    throw "WISDEV_USE_DIST=1 but dist\wisdev.exe not found. Run scripts\build-wisdev-cli.ps1 first."
}

# Prefer this repo's source by default so .\wisdev.cmd never runs a stale PATH binary.
# Use WISDEV_USE_PATH=1 to force the global `wisdev` on PATH instead.
if ($env:WISDEV_USE_PATH -eq "1") {
    $onPath = Get-Command wisdev -ErrorAction SilentlyContinue
    if ($onPath -and $onPath.CommandType -eq "Application") {
        Invoke-WisdevBinary $onPath.Source
    }
    throw "WISDEV_USE_PATH=1 but `wisdev` was not found on PATH."
}

Push-Location $Orchestrator
try {
    if ($env:WISDEV_VERBOSE -eq "1") {
        Write-Host "wisdev: using local source (go run ./cmd/wisdev)" -ForegroundColor DarkGray
    }
    go run ./cmd/wisdev @WisdevArgs
    exit $LASTEXITCODE
} finally {
    Pop-Location
}

if (Test-Path -LiteralPath $DistExe) {
    Invoke-WisdevBinary $DistExe
}

$onPath = Get-Command wisdev -ErrorAction SilentlyContinue
if ($onPath -and $onPath.CommandType -eq "Application") {
    Invoke-WisdevBinary $onPath.Source
}

throw "wisdev launcher could not find a runnable binary. Install Go or run scripts\build-wisdev-cli.ps1."
