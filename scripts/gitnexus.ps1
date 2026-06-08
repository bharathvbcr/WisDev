<#
DESCRIPTION:
    Wrapper for running GitNexus inside the wisdev-agent-os workspace.

USAGE:
    .\scripts\gitnexus.ps1 index   # refresh index
    .\scripts\gitnexus.ps1 status  # print index status

    If you pass no arguments, defaults to `index`.
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('index', 'status', 'help')]
  [string]$Command = 'index'
)

$ErrorActionPreference = 'Stop'

Set-Location -Path $PSScriptRoot\..

function Invoke-GitNexus {
  param([string[]]$CommandArgs)

  npx --yes -p node@22 -p gitnexus@1.6.3 gitnexus @CommandArgs
}

if ($Command -eq 'help') {
  Write-Host "Usage: .\scripts\gitnexus.ps1 <index|status|help>`n"
  Write-Host "  index   - analyze this repo and refresh .gitnexus (default)"
  Write-Host "  status  - show gitnexus freshness status"
  Write-Host "  help    - show this help"
  Write-Host ""
  Write-Host "Examples:"
  Write-Host "  .\scripts\gitnexus.ps1 index"
  Write-Host "  .\scripts\gitnexus.ps1 status"
  return
}

if ($Command -eq 'index') {
  Invoke-GitNexus -CommandArgs @('analyze', '.', '--skip-git', '--skip-agents-md')
  return
}

Invoke-GitNexus -CommandArgs @($Command)
