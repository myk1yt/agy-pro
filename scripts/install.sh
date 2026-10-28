#!/usr/bin/env bash
set -e

echo "======================================================="
echo "  Antigravity CLI Developer Toolkit (agy-tools) Installer"
echo "======================================================="

if ! command -v node >/dev/null 2>&1; then
    echo "[ERROR] Node.js is not installed or not in your PATH."
    echo "Please install Node.js (v16+) from https://nodejs.org/"
    exit 1
fi

if ! node -e "process.exit(parseInt(process.versions.node.split('.')[0], 10) >= 16 ? 0 : 1)" >/dev/null 2>&1; then
    echo "[ERROR] Node.js version 16 or higher is required."
    echo "Current version: $(node -v 2>/dev/null || echo 'unknown')"
    echo "Please upgrade Node.js from https://nodejs.org/"
    exit 1
fi

if ! command -v npm >/dev/null 2>&1; then
    echo "[ERROR] npm is not installed or not in your PATH."
    echo "Please ensure npm is installed and in your PATH."
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

chmod +x "$ROOT_DIR/bin/agy-tools.js" 2>/dev/null || true
chmod +x "$ROOT_DIR/bin/agy-tokens.js" 2>/dev/null || true

echo "[INFO] Linking agy-tools globally via npm link..."
cd "$ROOT_DIR"

if npm link >/dev/null 2>&1; then
    echo ""
    echo "[SUCCESS] agy-tools, agy-tokens, agy-dashboard, and antigravity-tools installed globally!"
    STATUSLINE_CMD="agy-tokens --hook --raw --write-dashboard"
else
    echo ""
    echo "[WARN] Global npm link required sudo or failed. Setting up user symlinks..."
    USER_BIN="$HOME/.local/bin"
    mkdir -p "$USER_BIN"
    ln -sf "$ROOT_DIR/bin/agy-tools.js" "$USER_BIN/agy-tools"
    ln -sf "$ROOT_DIR/bin/agy-tools.js" "$USER_BIN/antigravity-tools"
    ln -sf "$ROOT_DIR/bin/agy-tokens.js" "$USER_BIN/agy-tokens"
    ln -sf "$ROOT_DIR/bin/agy-tokens.js" "$USER_BIN/agy-dashboard"
    echo "[SUCCESS] Created symlinks in $USER_BIN"
    echo "Make sure $USER_BIN is in your PATH."
    STATUSLINE_CMD="node \"$ROOT_DIR/bin/agy-tokens.js\" --hook --raw --write-dashboard"
fi

echo ""
echo "[INFO] Configuring Antigravity statusLine integration..."
node "$ROOT_DIR/scripts/lib/configure-statusline.js" --command "$STATUSLINE_CMD"

echo ""
echo "[INFO] Statusline integration:"
echo "  Target settings: $HOME/.gemini/antigravity-cli/settings.json"
echo '  "statusLine": {'
echo '    "type": "command",'
echo "    \"command\": \"$STATUSLINE_CMD\","
echo '    "enabled": true,'
echo '    "stack_with_default": true'
echo '  }'
echo ""
echo "  - statusLine is now automatically configured in settings.json."
echo "  - Restart Antigravity CLI (agy) to start seeing real-time token tracking."
echo "  - For manual setup or troubleshooting, see README.md."
echo ""

echo "Try running:"
echo "  agy-tools --version"
echo "  agy-tools prices --currency krw"
echo "  agy-tokens --help"
echo ""
