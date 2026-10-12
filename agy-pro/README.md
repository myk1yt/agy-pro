# agy-pro: High-Performance Go Shim & Swarm Orchestrator

`agy-pro` is a transparent, ultra-low-latency Go shim and orchestrator for Google's Antigravity CLI (`agy`).

It provides:

1. **Transparent Passthrough**: Intercepts `agy` calls without overhead, dynamically locating the authentic `agy` binary while preserving standard I/O handles, TUI compatibility, and exact process exit codes.
2. **Interactive Launcher**: When external models are registered and you run a bare `agy` (no arguments) in a terminal, a 2-second picker offers Native Gemini, External Models, or the Swarm. Pressing `Enter` (or waiting) starts native Gemini with no delay.
3. **Native Bridge**: A local OpenAI-compatible reverse proxy on `127.0.0.1` that exposes every registered external model inside the authentic `agy` `/model` picker, with a per-session random bearer key.
4. **Profile / Mode Switcher (`agy pro mode`)**: Effortlessly switch between `full-stack` (VP of Engineering orchestration, specialized agent skills, and plugins) and `clean` (minimal baseline environment) profiles with atomic swaps and non-destructive backups.
5. **Default Profile Config (`agy pro config`)**: Lock `agy` into `native`, `gateway`, or `prompt` startup behavior so power users never see the launcher.
6. **Conversational Setup Wizard (`agy pro setup`)**: Step-by-step interactive CLI to configure external OpenAI-compatible LLM providers (OpenRouter, DeepSeek, Groq, Google AI Studio, etc.), model capabilities (Vision, Thinking, Function Calling), and live endpoint testing.
7. **Multi-Model Swarm (`agy pro fanout`)**: Concurrent multi-model orchestration across diverse LLM families for double-blind security audits, code reviews, and architecture analysis.
8. **One-Click Install & Uninstall**: Zero-prerequisite setup on Windows (`install.cmd`) with automated Go toolchain provisioning and user PATH registration, plus one-command removal (`uninstall.cmd`) that restores the User `PATH` and moves `~/.agy-pro` to the Recycle Bin.

---

## Quick Start (Installation)

### 📋 Copy & Paste Install (Windows)

Paste ONE line into a PowerShell window (Windows PowerShell 5.1 or PowerShell 7). It picks the route on its own: with `git` it clones the `feature/agy-pro` branch and runs `install.cmd`; without `git` it downloads the branch ZIP into `%TEMP%` and runs `install.ps1`.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "if (Get-Command git -ErrorAction SilentlyContinue) { cmd /c 'git clone -b feature/agy-pro https://github.com/myk1yt/agy-tools.git && cd agy-tools && install.cmd' } else { iwr 'https://codeload.github.com/myk1yt/agy-tools/zip/refs/heads/feature/agy-pro' -OutFile '$env:TEMP\agy-tools-agy-pro.zip' -UseBasicParsing; Expand-Archive -Path '$env:TEMP\agy-tools-agy-pro.zip' -DestinationPath '$env:TEMP\agy-tools-agy-pro' -Force; & (Resolve-Path '$env:TEMP\agy-tools-agy-pro\*\install.ps1').Path }"
```

> Every one-liner in this README installs from the **current state of the `feature/agy-pro` branch on GitHub**, so local commits that have not been pushed yet are not part of what you download.

Command Prompt users can run the clone line directly: `git clone -b feature/agy-pro https://github.com/myk1yt/agy-tools.git && cd agy-tools && install.cmd`. Already cloned? Run `.\install.cmd` from the repository root, or call the installer directly with `powershell -NoProfile -ExecutionPolicy Bypass -File agy-pro\scripts\install.ps1`.

The installer will:
1. Automatically detect or install the Go toolchain via `winget` (or download official portable Go if winget is busy).
2. Build `agy.exe` and deploy it to `~/.agy-pro/bin/agy.exe`.
3. Prepend `~/.agy-pro/bin` to the top of your Windows User `PATH`.
4. Run diagnostics (`agy pro doctor`) to verify system health.

### 🧹 Uninstall (Windows, one line)

Paste ONE line into a PowerShell window. It downloads the uninstaller, shows what it is about to do, and waits for your `y/N` confirmation:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr 'https://raw.githubusercontent.com/myk1yt/agy-tools/feature/agy-pro/agy-pro/scripts/uninstall.ps1' -OutFile '$env:TEMP\agy-pro-uninstall.ps1' -UseBasicParsing; & '$env:TEMP\agy-pro-uninstall.ps1'"
```

It removes exactly what the installer added, and nothing else:

1. the `~/.agy-pro/bin` entry from your Windows User `PATH` (that entry only; every other entry and the registry value kind are preserved),
2. the whole `~/.agy-pro` folder, moved to the **Recycle Bin** (never deleted permanently).

Variants, appended to the same line or run from a clone: `-KeepState` removes the shim but keeps `providers.json`, `state.json` and the backups; `-DryRun` prints the plan and changes nothing; `-RestorePath` restores the User `PATH` saved before the install (the newest `~/.agy-pro/backups/path-*.json`); `-Force` skips the confirmation prompt for scripted use. Cloned the repository earlier? Double-click `uninstall.cmd` in the repository root instead, or run `.\uninstall.cmd`. If you switched profiles with `agy pro mode`, run `agy pro mode restore` **before** uninstalling (the uninstaller prints that reminder whenever profile backups exist). A run without an interactive console (piped input, scheduled task) answers `no` automatically, so it cannot wipe the installation by accident.

---

## Use External Models by Default (3-line recipes)

After installation, registering a provider is all it takes. Pick the recipe that matches how you work.

**A. Every `agy` run exposes all registered models in `/model` (multi-model pool, kimi-code style)**
```bash
agy pro setup                            # register a provider (e.g. `google`, `openrouter`, `nw`)
agy pro config default-profile gateway   # plain `agy` now boots straight into the gateway
agy                                      # /model lists every registered external model at once
```

**B. Keep native Gemini as the default, reach an external model on demand**
```bash
agy pro setup                            # register a provider
agy -m "gemini-2.5-flash (google)"       # target one external model for this run only
agy pro config default-profile native    # or lock plain `agy` to 100% native, no prompt
```

**C. Prefer to choose at launch time (the default)**
```bash
agy pro setup                            # register a provider
agy                                      # 2-second picker: [1] Native [2] External [3] Swarm
agy pro config default-profile prompt    # restore the picker if you had locked a profile
```

> Google AI Studio note: registering the `google` preset (public `gemini-2.5-flash` / `gemini-2.5-pro`
> via `GEMINI_API_KEY`) is the only way to see Gemini **and** external models in one gateway list.
> The native Antigravity preview model (`gemini-3.8-flash-high`) is **not** available on AI Studio and
> therefore cannot be proxied through the bridge. See [`docs/bridge-architecture.md`](docs/bridge-architecture.md).

---

## Directory Structure

```
~/.agy-pro/
├── bin/
│   └── agy.exe             # High-performance Go shim binary (takes PATH precedence)
├── backups/
│   └── <timestamp>/        # Non-destructive backups of replaced configurations
├── state.json              # Profile mode, last backup, managed manifest, and `default_profile`
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

### 2. Interactive Launcher
A **bare** `agy` (no arguments) shows a 2-second picker when **at least one external model is registered** and stdin is an interactive terminal:
```
[1] Native Gemini        (Enter or timeout)
[2] External Models      (local bridge + registered models in /model)
[3] Swarm                (agy pro fanout)
```
The picker belongs to the bare invocation, so any argument skips it (straight to the real binary, zero delay). The documented bypasses:
- any argument, e.g. `agy agents`, `agy --help`
- `-m` / `--model` or `-b` / `--bridge`
- `-p` / `--print` (upstream one-shot prompt; `--prompt` is its alias)
- `AGY_PRO_BRIDGE=1` (legacy `AGY_BRIDGE=1`)
- `AGY_LAUNCHER=0`
- stdin is not a TTY (pipes, CI, `</dev/null`)

### 3. Native Bridge (external models inside `agy /model`)
The bridge is started only when needed and bound to loopback on an OS-assigned port. Each bridge session generates a fresh random 32-byte `crypto/rand` bearer key, injects it as `AGY_LLM_GATEWAY_API_KEY`, and requires it on every loopback request. That session key is local only and is never validated against a provider.
Provider keys are a separate concern: `agy pro doctor` probes each configured provider key upstream with zero tokens (`GET <baseURL>/models`, 5s timeout) and prints a masked verdict per provider (`valid` / `invalid` / `indeterminate` / `SKIP`), plus any conflicting BOM-prefixed environment variable name. `agy pro doctor --fix-keys` restores a clean name from a probe-valid BOM variant (backup written first) and `agy pro doctor --rollback-keys <dir>` undoes a repair.
```bash
agy -m "glm-5.3-flash (charm)"      # targeted: bridge activates for this run only
agy -b                              # explicit bridge mode: every registered model in /model
agy -b models                       # `-b` is stripped; `models` runs with the gateway env (session ends with the command)
AGY_PRO_BRIDGE=1 agy                # same as -b, via environment
```
Model aliases: both `model (provider)` and the bare `model` id are registered, so either form works.

#### Bridge Tool Loop (Gemini-parity workspace actions)

`agy`'s gateway client never sends `tools` and discards `tool_calls`, so an external model cannot touch your workspace on its own. The bridge therefore runs the tool loop locally: it attaches tool definitions to the upstream request, executes the returned calls with the local tool runner, feeds the results back, and hands `agy` plain text only.

| Capability token | Tools the model gets |
|---|---|
| `read` | `read_file`, `list_dir` |
| `search` | `grep`, `glob` |
| `write` (opt-in) | `write_file`, `replace_in_file` |
| `exec` (opt-in) | `run_command` |

- Default is `read,search`: the model can inspect the workspace but cannot change it.
- `write` and `exec` change real files and run real commands. Enable them only for a session that needs them.
- `none` disables the loop entirely (plain passthrough, byte-identical proxy path).
- Workspace root is the directory you started `agy` in (one shim process per session; two sessions cannot see each other's workspace).
- Budgets: 8 upstream turns, 60s per turn, 240s wall clock. Every exit path ends with non-empty text, so `agy` never falls into its textless-response retry storm.
- A tool turn can outlive the default print timeout. For live one-shot runs use `agy --print-timeout 300s` (the loop's own ceiling is 240s).

### 4. Default Profile Configuration
```bash
agy pro config default-profile gateway   # plain `agy` boots with all external models exposed
agy pro config default-profile native    # plain `agy` boots 100% native, no prompt
agy pro config default-profile prompt    # plain `agy` shows the 2-second launcher
agy pro config show                      # print the active default profile and bridge tools
```
The value is stored under the `default_profile` key in `~/.agy-pro/state.json`.

#### Bridge Tools (`agy pro config bridge-tools`)

```bash
agy pro config bridge-tools read,search        # default: read-only tool loop
agy pro config bridge-tools read,search,write  # also allow workspace edits
agy pro config bridge-tools read,search,write,exec  # edits plus command execution
agy pro config bridge-tools none               # disable the loop (plain passthrough)
agy pro config show                            # prints Default Launch Profile, Profile Mode, Bridge Tools
```

The value is stored under the `bridge_tools` key in `~/.agy-pro/state.json` (additive: older state files keep loading unchanged). At run time the precedence is `AGY_PRO_BRIDGE_TOOLS` environment variable > stored value > default `read,search`; an unknown token is rejected at set time and disables the loop at run time rather than widening it.

### 5. Conversational Setup Wizard
```bash
agy pro setup
```
- Configures provider name, Base URL, API key environment variable, and model IDs.
- Offers curated presets for known providers, including **Google AI Studio** (`gemini-2.5-flash`, `gemini-2.5-pro`, `GEMINI_API_KEY`, `https://generativelanguage.googleapis.com/v1beta/openai/`).
- Configures token limits: Context Length (e.g., 128k) and Max Output Tokens (e.g., 4096).
- Configures model capabilities: Vision, Thinking mode, and allowed Agent Tools (`read_file`, `list_dir`, `grep`, `glob`).
- Performs an immediate live connectivity test and offers interactive retry loops.
- API keys are persisted securely to Windows User Environment variables; **secrets are NEVER stored in config files**.

### 6. Profile Mode Switcher
```bash
# Interactive selection menu
agy pro mode

# Direct mode switch
agy pro mode full      # Switch to full-stack orchestrator profile
agy pro mode clean     # Switch to minimal vanilla profile

# Read-only profile status (active mode, last backup, managed manifest)
agy pro mode status

# Restore previous backup
agy pro mode restore

# List available backups
agy pro mode list
```

### 7. Status & Diagnostics
```bash
# Display active mode, providers, and real agy location
agy pro status

# Display configured providers and detailed model capabilities (context, output, tools, thinking)
agy pro models

# Run system diagnostics (toolchain, real agy path, and a zero-token
# upstream probe of every configured provider key, masked; also reports conflicting
# BOM-prefixed environment variable names)
agy pro doctor

# Same diagnostics with a strict exit gate for automation: exits with code 1
# only when a structural check fails (real agy binary missing, or the shim is
# not running from ~/.agy-pro/bin - i.e. the User PATH registration is not in
# effect). Provider-key probe verdicts (valid/invalid/SKIP) are informational
# and never change the exit code.
agy pro doctor --strict

# Restore BOM-prefixed environment variable names from a probe-valid variant
# (writes a backup first; skips when no variant probes valid)
agy pro doctor --fix-keys

# Undo a repair from its backup directory
agy pro doctor --rollback-keys <backup-dir>

# Display shim and upstream versions
agy pro version
```

### 8. Multi-Model Fanout Swarm
```bash
agy pro fanout -task=security -n=3
agy pro fanout -task=review -models="openai/gpt-4o,deepseek/deepseek-chat"
```

---

## Development & Testing

Built exclusively with the Go standard library (Go 1.22+).

```bash
cd agy-pro
go test ./... -count=1
```

Test packages: `cmd/agy`, `internal/agenttools`, `internal/bridge`, `internal/fanout`, `internal/launcher`, `internal/mode`, `internal/paths`, `internal/providers`.

---

## Further Reading

- [`docs/bridge-architecture.md`](docs/bridge-architecture.md) — why native and external models cannot share one `agy /model` catalog, how the launcher / profile / AI Studio preset work around it, and the bridge tool loop that gives external models workspace actions (§5).
- [`docs/fanout.md`](docs/fanout.md) — multi-model swarm specification.
- [`docs/rules-conflicts.md`](docs/rules-conflicts.md) — Kimi vs Antigravity rule resolution matrix.
- [`docs/skills-port-matrix.md`](docs/skills-port-matrix.md) — 41-skill 1:1 porting matrix.
