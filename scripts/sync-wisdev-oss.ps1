param(
    [string]$Destination = "",
    [switch]$WhatIf
)

$ErrorActionPreference = "Stop"
$Source = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$ScholarLMRoot = (Resolve-Path (Join-Path $Source "..")).Path
if (-not $Destination) {
    $Destination = Join-Path (Split-Path $ScholarLMRoot -Parent) "WisDev"
}
if (-not (Test-Path -LiteralPath $Destination)) {
    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
}
$Destination = (Resolve-Path -LiteralPath $Destination).Path

Write-Host "Sync WisDev OSS"
Write-Host "  from: $Source"
Write-Host "  to:   $Destination"

$excludeDirs = @(
    ".git", ".gopath", ".gocache", "node_modules", "dist", "__pycache__", ".pytest_cache", ".venv"
)
$excludeRootEntries = @("WisDev", "ACL", "Meniscus_and_ACL", "Mixture_of_Experts")
$excludeFilePatterns = @("^wisdev-result-")

function Should-SkipPath {
    param([string]$RelativePath)
    $normalized = ($RelativePath -replace '\\', '/').TrimStart('./')
    foreach ($pattern in $excludeFilePatterns) {
        if ($normalized -cmatch $pattern) {
            return $true
        }
    }
    foreach ($entry in $excludeRootEntries) {
        if ($normalized -ceq $entry -or $normalized -cmatch "^$([regex]::Escape($entry))(/|$)") {
            return $true
        }
    }
    foreach ($part in $excludeDirs) {
        if ($normalized -cmatch "(^|/)$([regex]::Escape($part))(/|$)") {
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

$scholarModelsSource = Join-Path $ScholarLMRoot "scholar_models.json"
$scholarModelsTarget = Join-Path $Destination "scholar_models.json"
if (Test-Path -LiteralPath $scholarModelsSource) {
    if ($WhatIf) {
        Write-Host "copy scholar_models.json"
    } else {
        Copy-Item -LiteralPath $scholarModelsSource -Destination $scholarModelsTarget -Force
    }
    Write-Host "Synced scholar_models.json"
}

if (Test-Path -LiteralPath (Join-Path $Destination "WisDev")) {
    Write-Host "Removing stale nested WisDev/ folder..."
    if (-not $WhatIf) {
        Remove-Item -LiteralPath (Join-Path $Destination "WisDev") -Recurse -Force
    }
}

Write-Host "Done. Next: cd `"$Destination`" && git status"
