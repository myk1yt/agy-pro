#!/usr/bin/env bash
# Uninstall Designer Agent and Skills Suite from Antigravity CLI
set -e

echo "Uninstalling Zero-MCP Designer Suite from Antigravity CLI..."

# 1. Uninstall Plugin via Antigravity CLI
echo "Unregistering plugin via 'agy plugin uninstall designer'..."
agy plugin uninstall designer || true

# 2. Clean up deployed skills from ~/.gemini/config/skills and ~/.gemini/skills
GEMINI_DIR="${HOME}/.gemini"
CONFIG_SKILLS_DIR="${GEMINI_DIR}/config/skills"
USER_SKILLS_DIR="${GEMINI_DIR}/skills"
CONFIG_PLUGINS_DIR="${GEMINI_DIR}/config/plugins"

DESIGNER_SKILLS=(
    "design-core-harness"
    "design-vector-svg"
    "design-interactive-sandbox"
    "design-3d-canvas"
    "design-cyberpunk-brainmap"
)

echo "Cleaning up deployed skills from global ~/.gemini registries..."
for skill in "${DESIGNER_SKILLS[@]}"; do
    if [ -d "${CONFIG_SKILLS_DIR}/${skill}" ] || [ -L "${CONFIG_SKILLS_DIR}/${skill}" ]; then
        rm -rf "${CONFIG_SKILLS_DIR:?}/${skill}"
        echo "  - Removed skill from config: ${skill}"
    fi
    if [ -d "${USER_SKILLS_DIR}/${skill}" ] || [ -L "${USER_SKILLS_DIR}/${skill}" ]; then
        rm -rf "${USER_SKILLS_DIR:?}/${skill}"
        echo "  - Removed skill from user: ${skill}"
    fi
done

if [ -d "${CONFIG_PLUGINS_DIR}/designer" ] || [ -L "${CONFIG_PLUGINS_DIR}/designer" ]; then
    rm -rf "${CONFIG_PLUGINS_DIR:?}/designer"
fi

# 3. Verify Active Agents Registration
echo -e "\n[Success] Uninstallation Complete! Active agents:"
agy agents || true
