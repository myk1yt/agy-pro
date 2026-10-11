# Go Shim & Router Worker Report: agy-pro Core Implementation

**Date**: 2026-10-11  
**Worker**: Domain Worker (Go Shim & Launcher)  
**Branch**: `feature/agy-pro`  
**Status**: COMPLETED  

---

## 1. Executive Summary

This domain worker successfully delivered the complete Go shim core, router, profile mode switcher, dynamic launcher, and zero-friction one-click installer for `agy-pro` (Antigravity CLI wrapper). All implementations strictly adhere to Go standard library constraints (Go 1.22+), multi-platform file/path conventions, and non-destructive atomic operations.

---

## 2. Key Accomplishments & Deliverables

### 2.1 Git Branch & Initial Workspace Commit
- Switched to new branch: `feature/agy-pro`.
- Staged and committed core modules under author `kim,yong-tai <k1yt@users.noreply.github.com>` targeting repository `myk1yt/agy-tools.git`.
- Commit SHA: `37a5520f15e1dcfdb36ab902c0f5e4740766083b`.

### 2.2 Go Toolchain Detection & Robust Multi-Tier Provisioning
- Automated detection in `install.ps1`:
  1. Active `PATH` checking for `go.exe`.
  2. Default path checking: `C:\Program Files\Go\bin\go.exe`.
  3. System package manager: silent `winget install --id GoLang.Go -e --silent --accept-source-agreements --accept-package-agreements`.
  4. Portable zip fallback: automated download and extraction of official Go binary (`dl.google.com/go/go1.23.6.windows-amd64.zip`) into `~/.agy-pro/go` if package installer locks or lacks administrative rights.
- Resolved Windows installer service conflicts and verified both Go 1.27.0 and Go 1.23.6 runtime capabilities.

### 2.3 Transparent Passthrough & Launcher (`internal/launcher`)
- **Dynamic Binary Discovery**: Scans `PATH` while strictly excluding the shim's own directory and comparing target files via `os.SameFile`.
- **Precedence Hierarchy**:
  1. `AGY_PRO_REAL_AGY` explicit environment variable.
  2. Dynamic PATH search (skipping current binary).
  3. Default fallback: `%LOCALAPPDATA%\agy\bin\agy.exe`.
- **Recursion Guard**: Propagates `AGY_PRO_ACTIVE=1` to detect and break infinite loops immediately (`ErrRecursionDetected`).
- **Standard I/O Preservation**: Full ConPTY / TUI passthrough with `cmd.Stdin`, `cmd.Stdout`, and `cmd.Stderr` connected directly, preserving exact process exit codes.

### 2.4 Profile Mode Management (`internal/mode`, `profiles/embed.go`)
- **Non-Destructive Atomic Swaps**:
  - `agy pro mode`: Interactive selection menu (`1. full-stack`, `2. default (clean)`) or direct flag arguments (`full`, `clean`).
  - Prior configurations (`rules`, `skills`, `plugins`, `config.json`) in `~/.gemini/config` are moved (never deleted) into `~/.agy-pro/backups/<timestamp>`.
  - Atomically deploys selected profile from embedded Go templates (`profiles.EmbeddedProfiles`).
- **State & Rollback**:
  - Tracks state in `~/.agy-pro/state.json` (`current_mode`, `updated_at`, `last_backup`, `managed_items`).
  - `agy pro mode restore`: One-command restoration of the most recent backup into active configuration.

### 2.5 Router & Command Surface (`cmd/agy/main.go`)
- `agy pro setup`: Invokes the conversational CLI wizard (`providers.InteractiveSetupWizard`) to configure OpenAI-compatible providers, model IDs, token limits, and capabilities with live connection testing.
- `agy pro mode`: Subcommand for profile switching and backup rollback.
- `agy pro status`: Overview of active mode, last backup, real agy binary path, and configured providers.
- `agy pro doctor`: Complete diagnostic health check for toolchains, directories, binary paths, and environment variables.
- `agy pro fanout`: Direct invocation entry point for the multi-model swarm engine.
- `agy pro version`: Displays shim version alongside upstream `agy` version.
- `agy <any>`: Transparently passed to real `agy` (`C:\Users\k1yt\AppData\Local\agy\bin\agy.exe`).

### 2.6 One-Click One-Liner Windows Installation
- `install.cmd`: Root batch launcher enabling double-click execution or `.\install.cmd` invocation via `powershell -ExecutionPolicy Bypass`.
- `agy-pro/scripts/install.ps1`:
  - Creates directories: `~/.agy-pro`, `~/.agy-pro/bin`, `~/.agy-pro/backups`.
  - Probes or installs Go toolchain.
  - Compiles `agy.exe` with strip flags (`-ldflags="-s -w"`).
  - Prepends `~/.agy-pro/bin` to the top of Windows User `PATH`.
  - Runs `agy pro doctor` self-check and prints friendly Korean completion guide.

---

## 3. Test & Verification Matrix

| Test Suite / Step | Target Package / Command | Result | Notes |
|---|---|---|---|
| Paths unit tests | `agypro/internal/paths` | **PASS** | Home, overrides, atomic writes, backup moves |
| Launcher unit tests | `agypro/internal/launcher` | **PASS** | Env override, self-dir exclusion, recursion guard |
| Mode unit tests | `agypro/internal/mode` | **PASS** | State persistence, backup moves, restore |
| Go vet | `internal/paths`, `internal/launcher`, `internal/mode`, `cmd/agy`, `profiles` | **PASS** | 0 linter errors |
| End-to-End Build | `go build -o ~/.agy-pro/bin/agy.exe ./cmd/agy` | **PASS** | Binary size optimized |
| Installer Execution | `install.cmd` -> `install.ps1` | **PASS** | Verified end-to-end installation flow |
| Passthrough Test | `agy.exe --version` | **PASS** | Correctly invoked upstream agy (v1.3.3) |
| Doctor Diagnostics | `agy.exe pro doctor` | **PASS** | All checks green |

---

## 4. Key Files Created

- `install.cmd`
- `agy-pro/go.mod`
- `agy-pro/README.md`
- `agy-pro/cmd/agy/main.go`
- `agy-pro/internal/paths/paths.go`
- `agy-pro/internal/paths/paths_test.go`
- `agy-pro/internal/launcher/launcher.go`
- `agy-pro/internal/launcher/launcher_test.go`
- `agy-pro/internal/mode/mode.go`
- `agy-pro/internal/mode/mode_test.go`
- `agy-pro/profiles/embed.go`
- `agy-pro/profiles/clean/**`
- `agy-pro/profiles/full/**`
- `agy-pro/scripts/install.ps1`
