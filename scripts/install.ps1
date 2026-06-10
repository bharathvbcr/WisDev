# WisDev CLI Easy Installer for Windows

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $ScriptDir "..")).Path
$Orchestrator = Join-Path $RepoRoot "orchestrator"
$GoBin = Join-Path $env:USERPROFILE "go\bin"

Write-Host "--- WisDev CLI Easy Installer ---" -ForegroundColor Cyan
Write-Host "Step 1: Compiling and installing CLI binary..." -ForegroundColor Gray

# Compile and place in $GOPATH/bin
Push-Location $Orchestrator
try {
    go install ./cmd/wisdev
} finally {
    Pop-Location
}

Write-Host "Step 2: Checking environment PATH..." -ForegroundColor Gray

# Check and append Go bin directory to User PATH environment variable
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$cleanedUserPath = $userPath.TrimEnd(';')

if ($cleanedUserPath -notlike "*$GoBin*") {
    Write-Host "Registering $GoBin in User Environment PATH..." -ForegroundColor Yellow
    [Environment]::SetEnvironmentVariable("Path", $cleanedUserPath + ";" + $GoBin, "User")
    Write-Host "PATH registered successfully!" -ForegroundColor Green
} else {
    Write-Host "$GoBin is already registered in PATH." -ForegroundColor Green
}

Write-Host "`n[Success] WisDev CLI installed successfully!" -ForegroundColor Green
Write-Host "Please close and reopen your terminal windows, then type:" -ForegroundColor White
Write-Host "  wisdev tui" -ForegroundColor Cyan
