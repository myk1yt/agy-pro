# Install only the Security Reviewer Multi-Agent Suite into Antigravity CLI
$ErrorActionPreference = "Stop"
Write-Host "Installing Security Reviewer Multi-Agent Suite into Antigravity CLI..." -ForegroundColor Cyan

$scriptPath = if ($PSScriptRoot) {
    $PSScriptRoot
} elseif ($MyInvocation.MyCommand.Path) {
    Split-Path -Parent $MyInvocation.MyCommand.Path
} else {
    $current = (Get-Location).Path
    if (Test-Path (Join-Path $current "scripts")) {
        Join-Path $current "scripts"
    } else {
        $current
    }
}

$candidate1 = Join-Path $scriptPath "..\plugins\security-reviewer"
$candidate2 = Join-Path (Get-Location).Path "plugins\security-reviewer"
$candidate3 = (Get-Location).Path

$pluginPath = if (Test-Path (Join-Path $candidate1 "plugin.json")) {
    (Resolve-Path $candidate1).Path
} elseif (Test-Path (Join-Path $candidate2 "plugin.json")) {
    (Resolve-Path $candidate2).Path
} elseif (Test-Path (Join-Path $candidate3 "plugin.json")) {
    (Resolve-Path $candidate3).Path
} else {
    (Resolve-Path (Join-Path $scriptPath "..\plugins\security-reviewer")).Path
}

agy plugin install $pluginPath

Write-Host "`n[Success] Installation Complete! Active agents:" -ForegroundColor Green
agy agents
