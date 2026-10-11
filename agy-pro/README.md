# agy-pro: High-Performance Go Shim & Swarm Orchestrator

`agy-pro` is a transparent, ultra-low-latency Go shim and orchestrator for Google's Antigravity CLI (`agy`).

It provides:
1. **Transparent Passthrough**: Intercepts `agy` calls without overhead, dynamically locating the authentic `agy` binary while preserving standard I/O handles, TUI compatibility, and exact process exit codes.
2. **Profile / Mode Switcher (`agy pro mode`)**: Effortlessly switch between `full-stack` (VP of Engineering orchestration, specialized agent skills, and plugins) and `clean` (minimal baseline environment) profiles with atomic swaps and non-destructive backups.
3. **Conversational Setup Wizard (`agy pro setup`)**: Step-by-step interactive CLI to configure external OpenAI-compatible LLM providers (OpenRouter, DeepSeek, Groq, etc.), capabilities (Vision, Video, Thinking, Function Calling), and live endpoint testing.
4. **Multi-Model Swarm (`agy pro fanout`)**: Concurrent multi-model orchestration across diverse LLM families for double-blind security audits, code reviews, and architecture analysis.
5. **One-Click Installation**: Zero-prerequisite setup on Windows (`install.cmd`), with automated Go toolchain provisioning and user PATH registration.

---

## Quick Start (Installation)

### Windows (One-Click)
Run `install.cmd` from the root repository or terminal:
```cmd
.\install.cmd
```
Or directly run via PowerShell:
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File agy-pro\scripts\install.ps1
```

The installer will:
1. Automatically detect or install the Go toolchain via `winget` (or download official portable Go if winget is busy).
2. Build `agy.exe` and deploy it to `~/.agy-pro/bin/agy.exe`.
3. Prepend `~/.agy-pro/bin` to the top of your Windows User `PATH`.
4. Run diagnostics (`agy pro doctor`) to verify system health.

---

## Directory Structure

```
~/.agy-pro/
├── bin/
│   └── agy.exe             # High-performance Go shim binary (takes PATH precedence)
├── backups/
│   └── <timestamp>/        # Non-destructive backups of replaced configurations
├── state.json              # Current profile state, last backup, and managed manifest
├── providers.json          # Provider & model registry (env variable names only)
└── state-fanout.json       # Swarm execution history and family rotation state
```

---

## CLI Reference

### 1. Transparent Passthrough
Any command not prefixed with `agy pro` is executed by the authentic `agy` binary:
```bash
agy --version
agy agents
agy -m gemini-2.5-flash
```

### 2. Conversational Setup Wizard
```bash
agy pro setup
```
- Configures provider name, Base URL, API key environment variable, and model IDs.
- Configures token limits: Context Length (e.g., 128k) and Max Output Tokens (e.g., 4096).
- Configures model capabilities: Vision, Video, Thinking mode, and allowed Agent Tools (`read_file`, `list_dir`, `grep`, `glob`).
- Performs an immediate live connectivity test and offers interactive retry loops.
- API keys are persisted securely to Windows User Environment variables; **secrets are NEVER stored in config files**.

### 3. Profile Mode Switcher
```bash
# Interactive selection menu
agy pro mode

# Direct mode switch
agy pro mode full      # Switch to full-stack orchestrator profile
agy pro mode clean     # Switch to minimal vanilla profile

# Restore previous backup
agy pro mode restore

# List available backups
agy pro mode list
```

### 4. Status & Diagnostics
```bash
# Display active mode, providers, and real agy location
agy pro status

# Run comprehensive system diagnostics
agy pro doctor

# Display shim and upstream versions
agy pro version
```

### 5. Multi-Model Fanout Swarm
```bash
agy pro fanout -task=security -n=3
agy pro fanout -task=review -models="openai/gpt-4o,deepseek/deepseek-chat"
```

---

## Development & Testing

Built exclusively with the Go standard library (Go 1.22+).

```bash
cd agy-pro
go test ./internal/paths/... ./internal/launcher/... ./internal/mode/...
```
