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

# Capture source provenance and warn if the source tree has uncommitted changes,
# so a publish always reflects a known commit (or a deliberate dirty snapshot).
$SourceCommit = "unknown"
$SourceDirty = $false
try {
    $SourceCommit = (& git -C $ScholarLMRoot rev-parse HEAD 2>$null).Trim()
    if (& git -C $ScholarLMRoot status --porcelain -- $Source 2>$null) {
        $SourceDirty = $true
    }
} catch {
    Write-Host "  warning: could not read source git provenance ($_)"
}
if ($SourceCommit) {
    Write-Host "  source commit: $SourceCommit$(if ($SourceDirty) { ' (dirty: uncommitted changes in wisdev-arc/)' })"
}
if ($SourceDirty) {
    Write-Host "  WARNING: wisdev-arc/ has uncommitted changes; the mirror will include them."
}

$excludeDirs = @(
    ".git", ".gopath", ".gocache", "node_modules", "dist", "__pycache__", ".pytest_cache", ".venv"
)
$excludeRootEntries = @("WisDev", "ACL", "Meniscus_and_ACL", "Mixture_of_Experts")
# Files force-kept even when a broader exclude pattern below would otherwise match.
$keepFilePatterns = @(
    '(^|/)\.env\.example$'
)
# Secrets, local state, and run artifacts that must never reach the OSS mirror.
# Single-quoted so regex backslashes are literal (the old double-quoted
# "_prm_rewards\\.jsonl$" expanded to a backslash that never matched a real file).
$excludeFilePatterns = @(
    '^wisdev-result-',
    '_prm_rewards\.jsonl$',
    '(^|/)\.env$',
    '(^|/)\.env\..+',
    '(^|/)service-account[^/]*\.json$',
    '(^|/)[^/]*-credentials\.json$',
    '(^|/)wisdev_journal\.jsonl',
    '\.log$'
)

function Should-SkipPath {
    param([string]$RelativePath)
    # Strip only a leading "./" prefix. (TrimStart('./') would strip ALL leading
    # dots/slashes, turning ".env" into "env" so dotfile patterns never matched.)
    $normalized = ($RelativePath -replace '\\', '/') -replace '^(\./)+', ''
    foreach ($pattern in $keepFilePatterns) {
        if ($normalized -cmatch $pattern) {
            return $false
        }
    }
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

# Prune excluded entries that may linger from earlier syncs. The copy loop skips these,
# but it never deletes, so previously-leaked local research artifacts can remain in the
# OSS tree. Remove them so the destination never holds files we intend to exclude.
$pruned = 0
foreach ($entry in $excludeRootEntries) {
    if ($entry -ceq "WisDev") { continue }  # handled above
    $target = Join-Path $Destination $entry
    if (Test-Path -LiteralPath $target) {
        Write-Host "Pruning stale excluded entry: $entry"
        if (-not $WhatIf) {
            Remove-Item -LiteralPath $target -Recurse -Force
        }
        $pruned += 1
    }
}
$destFiles = Get-ChildItem -LiteralPath $Destination -Recurse -File -Force -ErrorAction SilentlyContinue
foreach ($file in $destFiles) {
    $relative = $file.FullName.Substring($Destination.Length).TrimStart('\', '/')
    $normalized = ($relative -replace '\\', '/') -replace '^(\./)+', ''
    # Never descend into excluded directories in the destination (.git, caches,
    # node_modules, .gopath, ...). They are git-ignored junk in the mirror; pruning
    # individual files inside them would corrupt git or delete vendored testdata.
    $underExcludedDir = $false
    foreach ($part in $excludeDirs) {
        if ($normalized -cmatch "(^|/)$([regex]::Escape($part))(/|$)") { $underExcludedDir = $true; break }
    }
    if ($underExcludedDir) { continue }
    $keep = $false
    foreach ($pattern in $keepFilePatterns) {
        if ($normalized -cmatch $pattern) { $keep = $true; break }
    }
    if ($keep) { continue }
    foreach ($pattern in $excludeFilePatterns) {
        if ($normalized -cmatch $pattern) {
            Write-Host "Pruning stale excluded file: $relative"
            if (-not $WhatIf) {
                Remove-Item -LiteralPath $file.FullName -Force
            }
            $pruned += 1
            break
        }
    }
}
if ($pruned -gt 0) {
    Write-Host "Pruned $pruned stale excluded entr$(if ($pruned -eq 1) { 'y' } else { 'ies' })."
}

# Stamp provenance so the mirror records which source commit it reflects. The stamp is
# git-ignored in the OSS tree (see .gitignore) — it is a local freshness marker, not a
# committed file, so it never adds churn to the public repo.
$stamp = [ordered]@{
    source_commit = $SourceCommit
    source_dirty  = $SourceDirty
    synced_utc    = (Get-Date).ToUniversalTime().ToString("o")
    files_copied  = $copied
    whatif        = [bool]$WhatIf
}
$stampPath = Join-Path $Destination ".oss-sync-stamp.json"
if ($WhatIf) {
    Write-Host "write .oss-sync-stamp.json (source $SourceCommit)"
} else {
    ($stamp | ConvertTo-Json) | Set-Content -LiteralPath $stampPath -Encoding utf8
    Write-Host "Wrote provenance stamp: .oss-sync-stamp.json"
}

Write-Host "Done. Next: cd `"$Destination`" && git status"
