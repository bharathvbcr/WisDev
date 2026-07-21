# WisDev CLI installer for Windows.
#
# One-liner (downloads the latest release binary, no Go required):
#   irm https://raw.githubusercontent.com/bharathvbcr/WisDev/main/scripts/install.ps1 | iex
#
# From a repo checkout it builds from source when Go is available and no
# release asset matches, so the original `scripts\install.ps1` flow still works.
#
# Environment overrides:
#   WISDEV_REPO         GitHub repo to fetch releases from (default: bharathvbcr/WisDev)
#   WISDEV_VERSION      Release tag to install (default: latest)
#   WISDEV_INSTALL_DIR  Target directory (default: %USERPROFILE%\.wisdev\bin)
#   WISDEV_FROM_SOURCE  Set to 1 to force a source build (requires Go + checkout)

$ErrorActionPreference = "Stop"

$Repo = if ($env:WISDEV_REPO) { $env:WISDEV_REPO } else { "bharathvbcr/WisDev" }
$Version = if ($env:WISDEV_VERSION) { $env:WISDEV_VERSION } else { "latest" }
$InstallDir = if ($env:WISDEV_INSTALL_DIR) { $env:WISDEV_INSTALL_DIR } else { Join-Path $env:USERPROFILE ".wisdev\bin" }

function Add-UserPath {
    param([string]$Dir)
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $cleaned = if ($userPath) { $userPath.TrimEnd(';') } else { "" }
    if ($cleaned -notlike "*$Dir*") {
        Write-Host "Registering $Dir in User Environment PATH..." -ForegroundColor Yellow
        [Environment]::SetEnvironmentVariable("Path", ($cleaned + ";" + $Dir).TrimStart(';'), "User")
        Write-Host "PATH registered. Close and reopen your terminal to pick it up." -ForegroundColor Green
    } else {
        Write-Host "$Dir is already registered in PATH." -ForegroundColor Green
    }
}

function Install-FromRelease {
    $arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLower()) {
        "x64"   { "amd64" }
        "arm64" { "arm64" }
        default { return $false }
    }

    Write-Host "Step 1: Resolving release for windows_$arch..." -ForegroundColor Gray
    try {
        if ($Version -eq "latest") {
            $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -ErrorAction Stop
            $tag = $release.tag_name
        } else {
            $tag = $Version
        }
    } catch {
        return $false
    }
    if (-not $tag) { return $false }

    $asset = "wisdev_${tag}_windows_${arch}.zip"
    $url = "https://github.com/$Repo/releases/download/$tag/$asset"
    $tmp = Join-Path $env:TEMP "wisdev-install-$([System.Guid]::NewGuid().ToString('N').Substring(0,8))"
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null

    Write-Host "Step 2: Downloading $url..." -ForegroundColor Gray
    try {
        Invoke-WebRequest -Uri $url -OutFile (Join-Path $tmp $asset) -ErrorAction Stop
    } catch {
        Write-Host "No release asset $asset found for $tag." -ForegroundColor Yellow
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
        return $false
    }

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item (Join-Path $tmp "wisdev.exe") (Join-Path $InstallDir "wisdev.exe") -Force
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue

    Write-Host "Installed wisdev $tag to $InstallDir\wisdev.exe" -ForegroundColor Green
    Add-UserPath $InstallDir
    return $true
}

function Install-FromSource {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "No release binary available and Go is not installed. Install Go 1.25+ or download a release manually from https://github.com/$Repo/releases"
    }
    # Resolve the checkout when running as a file; `irm | iex` has no script path.
    $scriptDir = if ($PSScriptRoot) { $PSScriptRoot } else { $null }
    if (-not $scriptDir) {
        throw "No release binary available and no repo checkout found. Clone https://github.com/$Repo and rerun scripts\install.ps1"
    }
    $orchestrator = Join-Path (Resolve-Path (Join-Path $scriptDir "..")).Path "orchestrator"
    if (-not (Test-Path $orchestrator)) {
        throw "Could not find orchestrator directory at $orchestrator"
    }
    $goBin = Join-Path $env:USERPROFILE "go\bin"

    Write-Host "Building from source (go install ./cmd/wisdev)..." -ForegroundColor Gray
    Push-Location $orchestrator
    try {
        go install ./cmd/wisdev
        if ($LASTEXITCODE -ne 0) { throw "Failed to compile/install wisdev binary." }
    } finally {
        Pop-Location
    }
    Write-Host "Compiled and installed wisdev to $goBin." -ForegroundColor Green
    Add-UserPath $goBin
}

Write-Host "--- WisDev CLI Installer ---" -ForegroundColor Cyan
if ($env:WISDEV_FROM_SOURCE -eq "1") {
    Install-FromSource
} elseif (-not (Install-FromRelease)) {
    Write-Host "Falling back to source build..." -ForegroundColor Yellow
    Install-FromSource
}

Write-Host "`n[Success] WisDev CLI installed!" -ForegroundColor Green
Write-Host "Try running:" -ForegroundColor White
Write-Host "  wisdev check" -ForegroundColor Cyan
Write-Host "  wisdev `"What evidence supports RAG for scientific literature?`"" -ForegroundColor Cyan
