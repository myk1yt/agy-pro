#!/usr/bin/env bash
# Install only the Security Reviewer Multi-Agent Suite into Antigravity CLI
set -e

echo "Installing Security Reviewer Multi-Agent Suite into Antigravity CLI..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_PATH="$SCRIPT_DIR/../plugins/security-reviewer"

if [ ! -d "$PLUGIN_PATH" ]; then
    if [ -d "plugins/security-reviewer" ]; then
        PLUGIN_PATH="$(pwd)/plugins/security-reviewer"
    fi
fi

agy plugin install "$PLUGIN_PATH"

echo -e "\n[Success] Installation Complete! Active agents:"
agy agents
