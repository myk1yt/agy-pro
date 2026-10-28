#!/usr/bin/env bash
set -e

echo "==================================================================="
echo "  Antigravity & Gemini Configuration, Rules & Skills Installer     "
echo "==================================================================="
echo ""

TARGET_GLOBAL="${HOME}/.gemini"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "[INFO] Target Global Directory: ${TARGET_GLOBAL}"

# Create directories
mkdir -p "${TARGET_GLOBAL}/rules"
mkdir -p "${TARGET_GLOBAL}/skills/usage"
mkdir -p "${TARGET_GLOBAL}/skills/autonomous-orchestrator"
mkdir -p "${TARGET_GLOBAL}/hooks"

# Install rules
echo "[INFO] Installing Rules..."
cp "${ROOT_DIR}/rules/AGENTS.md" "${TARGET_GLOBAL}/rules/AGENTS.md"
cp "${ROOT_DIR}/rules/GEMINI.md" "${TARGET_GLOBAL}/rules/GEMINI.md"
cp "${ROOT_DIR}/rules/AGENTS.md" "${TARGET_GLOBAL}/AGENTS.md"
cp "${ROOT_DIR}/rules/GEMINI.md" "${TARGET_GLOBAL}/GEMINI.md"

# Install skills
echo "[INFO] Installing Skills..."
cp "${ROOT_DIR}/skills/usage/SKILL.md" "${TARGET_GLOBAL}/skills/usage/SKILL.md"
cp "${ROOT_DIR}/skills/autonomous-orchestrator/SKILL.md" "${TARGET_GLOBAL}/skills/autonomous-orchestrator/SKILL.md"

# Install hooks
echo "[INFO] Installing Hooks..."
cp "${ROOT_DIR}/hooks/hooks.json" "${TARGET_GLOBAL}/hooks/hooks.json"

echo ""
echo "==================================================================="
echo "[SUCCESS] Antigravity/Gemini configuration installed successfully!"
echo ""
echo "Installed Locations:"
echo "  - Rules:  ${TARGET_GLOBAL}/rules/"
echo "  - Skills: ${TARGET_GLOBAL}/skills/"
echo "  - Hooks:  ${TARGET_GLOBAL}/hooks/hooks.json"
echo "==================================================================="
