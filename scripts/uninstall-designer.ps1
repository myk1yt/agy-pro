# Uninstall Designer Agent and Skills Suite from Antigravity CLI
$ErrorActionPreference = "Continue"
Write-Host "Uninstalling Zero-MCP Designer Suite from Antigravity CLI..." -ForegroundColor Cyan

# 1. Uninstall Plugin via Antigravity CLI
Write-Host "Unregistering plugin via 'agy plugin uninstall designer'..." -ForegroundColor Gray
try {
    agy plugin uninstall designer
} catch {
    Write-Warning "Failed or plugin already uninstalled: $_"
}

# 2. Clean up deployed skills from ~/.gemini/config/skills and ~/.gemini/skills
$homeDir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $HOME }
$geminiDir = Join-Path $homeDir ".gemini"
$configSkillsDir = Join-Path (Join-Path $geminiDir "config") "skills"
$userSkillsDir = Join-Path $geminiDir "skills"
$configPluginsDir = Join-Path (Join-Path $geminiDir "config") "plugins"

$designerSkills = @(
    "design-core-harness",
    "design-vector-svg",
    "design-interactive-sandbox",
    "design-3d-canvas",
    "design-cyberpunk-brainmap"
)

Write-Host "Cleaning up deployed skills from global ~/.gemini registries..." -ForegroundColor Gray
foreach ($skill in $designerSkills) {
    $targetConfig = Join-Path $configSkillsDir $skill
    $targetUser = Join-Path $userSkillsDir $skill

    if (Test-Path $targetConfig) {
        Remove-Item -Path $targetConfig -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "  - Removed skill: $targetConfig" -ForegroundColor Yellow
    }
    if (Test-Path $targetUser) {
        Remove-Item -Path $targetUser -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "  - Removed skill: $targetUser" -ForegroundColor Yellow
    }
}

# Ensure plugin directory in config/plugins/designer is also cleaned up if left behind
$targetPlugin = Join-Path $configPluginsDir "designer"
if (Test-Path $targetPlugin) {
    Remove-Item -Path $targetPlugin -Recurse -Force -ErrorAction SilentlyContinue
}

# 3. Verify Active Agents Registration
Write-Host "`n[Success] Uninstallation Complete! Active agents:" -ForegroundColor Green
agy agents
