#!/usr/bin/env bash
# Uninstall Code Reviewer Agent and Skills Suite from Antigravity CLI
set -e

echo "Uninstalling Pre-Merge Code Reviewer Suite from Antigravity CLI..."

# 1. Uninstall Plugin via Antigravity CLI
echo "Unregistering plugin via 'agy plugin uninstall code_reviewer'..."
agy plugin uninstall code_reviewer || true

# 2. Clean up deployed skills from ~/.gemini/config/skills and ~/.gemini/skills
GEMINI_DIR="${HOME}/.gemini"
CONFIG_SKILLS_DIR="${GEMINI_DIR}/config/skills"
USER_SKILLS_DIR="${GEMINI_DIR}/skills"
CONFIG_PLUGINS_DIR="${GEMINI_DIR}/config/plugins"

REVIEWER_SKILLS=(
    "code-review-taxonomy"
    "quality-gate"
)

echo "Cleaning up deployed skills from global ~/.gemini registries..."
for skill in "${REVIEWER_SKILLS[@]}"; do
    if [ -d "${CONFIG_SKILLS_DIR}/${skill}" ] || [ -L "${CONFIG_SKILLS_DIR}/${skill}" ]; then
        rm -rf "${CONFIG_SKILLS_DIR:?}/${skill}"
        echo "  - Removed skill from config: ${skill}"
    fi
    if [ -d "${USER_SKILLS_DIR}/${skill}" ] || [ -L "${USER_SKILLS_DIR}/${skill}" ]; then
        rm -rf "${USER_SKILLS_DIR:?}/${skill}"
        echo "  - Removed skill from user: ${skill}"
    fi
done

if [ -d "${CONFIG_PLUGINS_DIR}/code_reviewer" ] || [ -L "${CONFIG_PLUGINS_DIR}/code_reviewer" ]; then
    rm -rf "${CONFIG_PLUGINS_DIR:?}/code_reviewer"
fi
if [ -d "${CONFIG_PLUGINS_DIR}/code-reviewer" ] || [ -L "${CONFIG_PLUGINS_DIR}/code-reviewer" ]; then
    rm -rf "${CONFIG_PLUGINS_DIR:?}/code-reviewer"
fi

# 3. Verify Active Agents Registration
echo -e "\n[Success] Uninstallation Complete! Active agents:"
agy agents || true
