#!/usr/bin/env bash
# Install Designer Agent and Skills Suite into Antigravity CLI
set -e

echo "Installing Zero-MCP Designer Suite into Antigravity CLI..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_PATH="$SCRIPT_DIR/../plugins/designer"

if [ ! -d "$PLUGIN_PATH" ]; then
    if [ -d "plugins/designer" ]; then
        PLUGIN_PATH="$(pwd)/plugins/designer"
    fi
fi

# 1. Install Plugin via Antigravity CLI
echo "Registering plugin via 'agy plugin install'..."
agy plugin install "$PLUGIN_PATH"

# 2. Deploy Skills Globally into ~/.gemini/config/skills
GEMINI_DIR="${HOME}/.gemini"
CONFIG_SKILLS_DIR="${GEMINI_DIR}/config/skills"

mkdir -p "$CONFIG_SKILLS_DIR"

SOURCE_SKILLS_DIR="${PLUGIN_PATH}/skills"
if [ -d "$SOURCE_SKILLS_DIR" ]; then
    echo "Deploying skills to global ~/.gemini registries..."
    for skill_dir in "$SOURCE_SKILLS_DIR"/*; do
        if [ -d "$skill_dir" ] || [ -L "$skill_dir" ]; then
            skill_name="$(basename "$skill_dir")"
            rm -rf "${CONFIG_SKILLS_DIR:?}/${skill_name}"
            cp -r "$skill_dir" "$CONFIG_SKILLS_DIR/"
            echo "  + Deployed skill: $skill_name"
        fi
    done
fi

# 3. Verify Active Agents Registration
echo -e "\n[Success] Installation Complete! Active agents:"
agy agents
