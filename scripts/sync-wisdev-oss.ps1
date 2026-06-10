param(
    [string]$Destination = "",
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"
$Source = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not $Destination) {
    $CodeRoot = (Resolve-Path (Join-Path $Source "..\..")).Path
    $Destination = Join-Path (Split-Path $CodeRoot -Parent) "WisDev"
}
$Destination = [System.IO.Path]::GetFullPath($Destination)

Write-Host "Sync WisDev OSS"
Write-Host "  from: $Source"
Write-Host "  to:   $Destination"

$excludeDirs = @(
    ".git", ".gopath", ".gocache", "node_modules", "dist", "WisDev", "__pycache__", ".pytest_cache", ".venv"
)

function Should-SkipPath {
    param([string]$RelativePath)
    foreach ($part in $excludeDirs) {
        if ($RelativePath -match "(^|[\\/])$([regex]::Escape($part))([\\/]|$)") {
            return $true
        }
    }
    return $false
}

if (-not (Test-Path -LiteralPath $Destination)) {
    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
}

$files = Get-ChildItem -LiteralPath $Source -Recurse -File -Force
$copied = 0
foreach ($file in $files) {
    $relative = $file.FullName.Substring($Source.Length).TrimStart('\', '/')
    if (Should-SkipPath $relative) {
        continue
    }
    $target = Join-Path $Destination $relative
    $targetDir = Split-Path -Parent $target
    if (-not (Test-Path -LiteralPath $targetDir)) {
        if ($WhatIf) {
            Write-Host "mkdir $targetDir"
        } else {
            New-Item -ItemType Directory -Force -Path $targetDir | Out-Null
        }
    }
    if ($WhatIf) {
        Write-Host "copy $relative"
    } else {
        Copy-Item -LiteralPath $file.FullName -Destination $target -Force
    }
    $copied += 1
}

Write-Host "Synced $copied files."

if (Test-Path -LiteralPath (Join-Path $Destination "WisDev")) {
    Write-Host "Removing stale nested WisDev/ folder..."
    if (-not $WhatIf) {
        Remove-Item -LiteralPath (Join-Path $Destination "WisDev") -Recurse -Force
    }
}

Write-Host "Done. Next: cd `"$Destination`" && git status"
