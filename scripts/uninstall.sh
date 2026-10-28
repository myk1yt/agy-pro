#!/usr/bin/env bash
# Uninstaller for the Antigravity & Gemini Configuration Bundle (gemini-config branch).
# Removes ONLY the files this bundle's installer wrote — nothing else. Idempotent.

set -u

TARGET_GLOBAL="${HOME:-$USERPROFILE}/.gemini"
if [ "$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')" != "linux" ] && [ -n "${USERPROFILE:-}" ]; then
    TARGET_GLOBAL="$USERPROFILE\\.gemini"
fi

echo "==================================================================="
echo "  Antigravity & Gemini Configuration, Rules & Skills Uninstaller"
echo "==================================================================="
echo ""
echo "[INFO] Target Global Directory: $TARGET_GLOBAL"
echo "[INFO] This removes ONLY the files installed by this bundle:"
echo "         rules/AGENTS.md, rules/GEMINI.md, AGENTS.md, GEMINI.md,"
echo "         skills/usage/SKILL.md, skills/autonomous-orchestrator/SKILL.md"
echo "         hooks/hooks.json"
echo ""

removed() {
    echo "[REMOVED] $1"
}

# Root-level rule copies
[ -f "$TARGET_GLOBAL/AGENTS.md" ] && rm -f "$TARGET_GLOBAL/AGENTS.md" && removed "$TARGET_GLOBAL/AGENTS.md"
[ -f "$TARGET_GLOBAL/GEMINI.md" ] && rm -f "$TARGET_GLOBAL/GEMINI.md" && removed "$TARGET_GLOBAL/GEMINI.md"

# Rules
[ -f "$TARGET_GLOBAL/rules/AGENTS.md" ] && rm -f "$TARGET_GLOBAL/rules/AGENTS.md" && removed "$TARGET_GLOBAL/rules/AGENTS.md"
[ -f "$TARGET_GLOBAL/rules/GEMINI.md" ] && rm -f "$TARGET_GLOBAL/rules/GEMINI.md" && removed "$TARGET_GLOBAL/rules/GEMINI.md"

# Skills
[ -f "$TARGET_GLOBAL/skills/usage/SKILL.md" ] && rm -f "$TARGET_GLOBAL/skills/usage/SKILL.md" && removed "$TARGET_GLOBAL/skills/usage/SKILL.md"
[ -d "$TARGET_GLOBAL/skills/usage" ] && rmdir "$TARGET_GLOBAL/skills/usage" 2>/dev/null && removed "$TARGET_GLOBAL/skills/usage/"
[ -f "$TARGET_GLOBAL/skills/autonomous-orchestrator/SKILL.md" ] && rm -f "$TARGET_GLOBAL/skills/autonomous-orchestrator/SKILL.md" && removed "$TARGET_GLOBAL/skills/autonomous-orchestrator/SKILL.md"
[ -d "$TARGET_GLOBAL/skills/autonomous-orchestrator" ] && rmdir "$TARGET_GLOBAL/skills/autonomous-orchestrator" 2>/dev/null && removed "$TARGET_GLOBAL/skills/autonomous-orchestrator/"

# Hooks file (directory left intact for other hooks)
[ -f "$TARGET_GLOBAL/hooks/hooks.json" ] && rm -f "$TARGET_GLOBAL/hooks/hooks.json" && removed "$TARGET_GLOBAL/hooks/hooks.json"

echo ""
echo "==================================================================="
echo "[SUCCESS] Antigravity/Gemini configuration bundle uninstalled!"
echo "  (Directories created by the installer are left in place;"
echo "   no user data outside this bundle's own files was touched.)"
echo "==================================================================="
