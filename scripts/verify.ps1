param(
    [switch]$Go,
    [switch]$PythonContract,
    [switch]$SmokeLocal,
    [switch]$StaticRelease,
    [switch]$All
)

$ErrorActionPreference = "Stop"
$RepoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")

if (-not ($Go -or $PythonContract -or $SmokeLocal -or $StaticRelease -or $All)) {
    $All = $true
}

function Invoke-GoTests {
    Push-Location (Join-Path $RepoRoot "orchestrator")
    try {
        New-Item -ItemType Directory -Force -Path ".gopath\pkg\mod", ".gocache" | Out-Null
        $env:GOPATH = (Resolve-Path ".gopath").Path
        $env:GOMODCACHE = (Resolve-Path ".gopath\pkg\mod").Path
        $env:GOCACHE = (Resolve-Path ".gocache").Path
        go test ./internal/api ./internal/search ./internal/wisdev ./internal/rag ./internal/evidence ./internal/evidence/citations ./internal/telemetry ./internal/stackconfig ./cmd/server ./cmd/wisdev ./pkg/wisdev -count=1 -parallel=1
    } finally {
        Pop-Location
    }
}

function Invoke-PythonContractTests {
    Push-Location (Join-Path $RepoRoot "sidecar")
    try {
        python -m pytest -q tests/unit/test_stack_contract.py
    } finally {
        Pop-Location
    }
}

function Invoke-LocalSmoke {
    Push-Location (Join-Path $RepoRoot "orchestrator")
    try {
        go run ./cmd/wisdev yolo --local --offline --max-iterations 1 "map open source research agent evidence"
    } finally {
        Pop-Location
    }
}

function Invoke-StaticReleaseChecks {
    $blockedPatterns = @(
        "backend/python_sidecar",
        "backend/go_orchestrator",
        "compute_go",
        "compute_rust",
        "search-gateway",
        "/app/backend/python_sidecar"
    )
    $allowedPathFragments = @(
        "docs\MIGRATION_STATUS.md",
        "docs\RELEASE_CHECKLIST.md",
        "README.md",
        "scripts\verify.ps1"
    )

    $files = Get-ChildItem -LiteralPath $RepoRoot -Recurse -File -Force |
        Where-Object {
            $_.FullName -notmatch "\\.gopath|\\.gocache|__pycache__|\\.pytest_cache|\\.venv|\\.git"
        }

    foreach ($pattern in $blockedPatterns) {
        $hits = Select-String -Path ($files.FullName) -Pattern $pattern -SimpleMatch -ErrorAction SilentlyContinue |
            Where-Object {
                $relative = [System.IO.Path]::GetRelativePath($RepoRoot, $_.Path)
                -not ($allowedPathFragments | Where-Object { $relative -eq $_ })
            }
        if ($hits) {
            $formatted = ($hits | ForEach-Object {
                "$([System.IO.Path]::GetRelativePath($RepoRoot, $_.Path)):$($_.LineNumber): $($_.Line.Trim())"
            }) -join [Environment]::NewLine
            throw "Stale standalone path references found:$([Environment]::NewLine)$formatted"
        }
    }

    $dockerfiles = @(
        (Join-Path $RepoRoot "orchestrator\Dockerfile"),
        (Join-Path $RepoRoot "sidecar\Dockerfile"),
        (Join-Path $RepoRoot "sidecar\Dockerfile.sidecar")
    )
    foreach ($dockerfile in $dockerfiles) {
        if (-not (Test-Path -LiteralPath $dockerfile)) {
            throw "Missing Dockerfile: $dockerfile"
        }
        $contextDir = Split-Path -Parent $dockerfile
        $dockerignore = Join-Path $contextDir ".dockerignore"
        if (-not (Test-Path -LiteralPath $dockerignore)) {
            $relativeContext = [System.IO.Path]::GetRelativePath($RepoRoot, $contextDir)
            throw "Missing .dockerignore for Docker context: $relativeContext"
        }
        foreach ($line in Get-Content -LiteralPath $dockerfile) {
            $trimmed = $line.Trim()
            if ($trimmed -notmatch '^COPY\s+') {
                continue
            }
            if ($trimmed -match '^COPY\s+--from=') {
                continue
            }
            $parts = $trimmed -split '\s+'
            if ($parts.Count -lt 3) {
                continue
            }
            $sources = $parts[1..($parts.Count - 2)]
            foreach ($source in $sources) {
                if ($source -eq "." -or $source.StartsWith("--") -or $source -match "^[a-z]+://") {
                    continue
                }
                $sourcePath = Join-Path $contextDir $source
                if (-not (Test-Path -LiteralPath $sourcePath)) {
                    $relativeDockerfile = [System.IO.Path]::GetRelativePath($RepoRoot, $dockerfile)
                    throw "Dockerfile COPY source not found in ${relativeDockerfile}: $source"
                }
            }
        }
    }

    $requirements = Join-Path $RepoRoot "sidecar\requirements.txt"
    if (Select-String -Path $requirements -Pattern "git+" -SimpleMatch -Quiet) {
        throw "sidecar/requirements.txt contains an unpinned VCS dependency"
    }
}

if ($All -or $StaticRelease) {
    Invoke-StaticReleaseChecks
}
if ($All -or $Go) {
    Invoke-GoTests
}
if ($All -or $PythonContract) {
    Invoke-PythonContractTests
}
if ($All -or $SmokeLocal) {
    Invoke-LocalSmoke
}
