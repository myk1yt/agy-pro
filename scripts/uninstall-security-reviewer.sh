#!/usr/bin/env bash
# Uninstall Security Reviewer Multi-Agent Suite from Antigravity CLI
set -e

echo "Uninstalling Security Reviewer Multi-Agent Suite from Antigravity CLI..."

# 1. Uninstall Plugin via Antigravity CLI
echo "Unregistering plugin via 'agy plugin uninstall security_reviewer'..."
agy plugin uninstall security_reviewer || true

# 2. Clean up any leftover agent registrations in ~/.gemini/config/agents, ~/.gemini/agents, and plugins
GEMINI_DIR="${HOME}/.gemini"
CONFIG_AGENTS_DIR="${GEMINI_DIR}/config/agents"
USER_AGENTS_DIR="${GEMINI_DIR}/agents"
CONFIG_PLUGINS_DIR="${GEMINI_DIR}/config/plugins"

SECURITY_AGENTS=(
    "security-reviewer"
    "sec-app-vuln"
    "sec-cloud-iam"
    "sec-credential-scanner"
    "sec-supply-mcp"
)

echo "Cleaning up security agents from global ~/.gemini registries..."
for agent in "${SECURITY_AGENTS[@]}"; do
    if [ -d "${CONFIG_AGENTS_DIR}/${agent}" ] || [ -L "${CONFIG_AGENTS_DIR}/${agent}" ]; then
        rm -rf "${CONFIG_AGENTS_DIR:?}/${agent}"
        echo "  - Removed agent from config: ${agent}"
    fi
    if [ -d "${USER_AGENTS_DIR}/${agent}" ] || [ -L "${USER_AGENTS_DIR}/${agent}" ]; then
        rm -rf "${USER_AGENTS_DIR:?}/${agent}"
        echo "  - Removed agent from user: ${agent}"
    fi
done

if [ -d "${CONFIG_PLUGINS_DIR}/security_reviewer" ] || [ -L "${CONFIG_PLUGINS_DIR}/security_reviewer" ]; then
    rm -rf "${CONFIG_PLUGINS_DIR:?}/security_reviewer"
fi

# 3. Verify Active Agents Registration
echo -e "\n[Success] Uninstallation Complete! Active agents:"
agy agents || true
