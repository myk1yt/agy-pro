# Uninstall Security Reviewer Multi-Agent Suite from Antigravity CLI
$ErrorActionPreference = "Continue"
Write-Host "Uninstalling Security Reviewer Multi-Agent Suite from Antigravity CLI..." -ForegroundColor Cyan

# 1. Uninstall Plugin via Antigravity CLI
Write-Host "Unregistering plugin via 'agy plugin uninstall security_reviewer'..." -ForegroundColor Gray
try {
    agy plugin uninstall security_reviewer
} catch {
    Write-Warning "Failed or plugin already uninstalled: $_"
}

# 2. Clean up any leftover agent registrations in ~/.gemini/config/agents, ~/.gemini/agents, and plugins
$homeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$geminiDir = Join-Path $homeDir ".gemini"
$configAgentsDir = Join-Path (Join-Path $geminiDir "config") "agents"
$userAgentsDir = Join-Path $geminiDir "agents"
$configPluginsDir = Join-Path (Join-Path $geminiDir "config") "plugins"

$securityAgents = @(
    "security-reviewer",
    "sec-app-vuln",
    "sec-cloud-iam",
    "sec-credential-scanner",
    "sec-supply-mcp"
)

Write-Host "Cleaning up security agents from global ~/.gemini registries..." -ForegroundColor Gray
foreach ($agent in $securityAgents) {
    $targetConfig = Join-Path $configAgentsDir $agent
    $targetUser = Join-Path $userAgentsDir $agent

    if (Test-Path $targetConfig) {
        Remove-Item -Path $targetConfig -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "  - Removed agent: $targetConfig" -ForegroundColor Yellow
    }
    if (Test-Path $targetUser) {
        Remove-Item -Path $targetUser -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "  - Removed agent: $targetUser" -ForegroundColor Yellow
    }
}

$targetPlugin = Join-Path $configPluginsDir "security_reviewer"
if (Test-Path $targetPlugin) {
    Remove-Item -Path $targetPlugin -Recurse -Force -ErrorAction SilentlyContinue
}

# 3. Verify Active Agents Registration
Write-Host "`n[Success] Uninstallation Complete! Active agents:" -ForegroundColor Green
agy agents
