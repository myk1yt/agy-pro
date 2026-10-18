# agy-pro: High-Performance Go Shim & Swarm Orchestrator

`agy-pro` is a transparent, ultra-low-latency Go shim and orchestrator for Google's Antigravity CLI (`agy`).

It provides:

1. **Transparent Passthrough**: Intercepts `agy` calls without overhead, dynamically locating the authentic `agy` binary while preserving standard I/O handles, TUI compatibility, and exact process exit codes.
2. **Interactive Launcher (opt-in)**: A bare `agy` (no arguments) goes straight to the native session, zero menu and zero delay. Set `AGY_LAUNCHER=1` to opt in: when external models are registered and you run a bare `agy` in a terminal, a 2-second picker then offers Native Gemini, External Models (a numbered native + external model list), or the Swarm. Pressing `Enter` (or waiting) starts native Gemini with no delay. The launcher also prints the community banner line once at startup.
3. **Native Coexistence (`agy pro hybrid`)**: Run registered external providers inside agy's OWN native session, with no agent mediation. Profile P1 ("picker coexistence") lists externals in agy's own `/model` catalog on agy's own 11-tool engine with zero settings writes; profile P2 ("subagent profile") attaches the full 13-tool set including the subagent family and runs a subagent `inherit` on the external session model. You pick one profile per session (one session is one client path). See the Native Coexistence section below for the honest limits.
4. **Native Bridge**: A local OpenAI-compatible reverse proxy on `127.0.0.1` that exposes every registered external model inside the authentic `agy` `/model` picker, with a per-session random bearer key. This stays the XOR gateway escape hatch; the M4 hardening work below makes its local tool loop more robust.
5. **Profile / Mode Switcher (`agy pro mode`)**: Effortlessly switch between `full-stack` (VP of Engineering orchestration, specialized agent skills, and plugins) and `clean` (minimal baseline environment) profiles with atomic swaps and non-destructive backups.
6. **Default Profile Config (`agy pro config`)**: Lock `agy` into `native` or `gateway` startup behavior; `prompt` is the neutral profile that leaves a bare `agy` native and allows the opt-in launcher.
7. **Conversational Setup Wizard (`agy pro setup`)**: Step-by-step interactive CLI to configure external OpenAI-compatible LLM providers (OpenRouter, DeepSeek, Groq, Google AI Studio, etc.), model capabilities (Vision, Thinking, Function Calling), and live endpoint testing.
8. **Multi-Model Swarm (`agy pro fanout`)**: Concurrent multi-model orchestration across diverse LLM families for double-blind security audits, code reviews, and architecture analysis. `--seats` mixes real `agy` (native Gemini) subprocess seats with external provider seats in ONE swarm.
9. **Numbered Swarm Builder (`agy pro swarm`)**: Presents the combined native + external inventory as a numbered list (presets `p1`..`p3`, `1,3,5`, ranges `2-4`), previews the exact `agy pro fanout` command, and runs it. Flags stay as the power-user/CI fallback.
10. **One Combined Model List (`agy pro models --all`)**: The native `agy models` catalog plus every registered provider model, numbered and group-tagged, as the single source of truth for the launcher picker, the swarm builder, and the settings panel.
11. **One Settings Surface (`agy pro settings` + `/setting_agypro`)**: A full-screen, keybinding-driven settings panel covering every agy-pro setting, plus the `agypro_settings` plugin whose `/setting_agypro` slash command drives the same settings through numbered menus inside an `agy` session.
12. **One-Click Install & Uninstall**: Zero-prerequisite setup on Windows (`install.cmd`) with automated Go toolchain provisioning and user PATH registration, plus one-command removal (`uninstall.cmd`) that restores the User `PATH` and moves `~/.agy-pro` to the Recycle Bin.

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

**C. Prefer to choose at launch time (opt-in)**
```bash
agy pro setup                            # register a provider
AGY_LAUNCHER=1 agy                       # opt in: 2-second picker [1] Native [2] External [3] Swarm
agy pro config default-profile prompt    # neutral profile: bare `agy` stays native, the picker itself is opt-in
```

> **Model visibility, in two layers (do not conflate them):**
>
> - **Inside an `agy` session, `/model` is an all-or-nothing switch.** Native Antigravity models and bridge
>   (external) models cannot share one catalog: when the gateway is on, the binary replaces the whole list.
>   The combined native + external view lives at the agy-pro layer instead: `agy pro models --all`, the
>   launcher's model picker, and `agy pro swarm`.
> - **Gateway mode CAN show Gemini and external models together.** Register the `google` preset (public
>   `gemini-2.5-flash` / `gemini-2.5-pro` via `GEMINI_API_KEY`,
>   `https://generativelanguage.googleapis.com/v1beta/openai/`), then run
>   `agy pro config default-profile gateway`: `/model` lists those Gemini models next to your NeuralWatt,
>   Charm, DeepSeek, and OpenRouter models. The native Antigravity preview model
>   (`gemini-3.8-flash-high`) is **not** available on AI Studio and therefore cannot be proxied through
>   the bridge; preserving it requires native mode.
>
> - **Native coexistence (profile P1) is a third option.** `agy pro hybrid on p1` runs externals inside
>   agy's own `/model` catalog on agy's own tool engine (an external-only catalog, no subagents on that
>   path). Profile P2 trades the in-session `/model` listing for the subagent family. See the Native
>   Coexistence section below.
>
> See [`docs/bridge-architecture.md`](docs/bridge-architecture.md) and
> [`docs/swarm-hybrid.md`](docs/swarm-hybrid.md) §8.

---

## Native Coexistence: External Providers Inside agy's Own Session

Native coexistence runs the external providers you registered **inside agy's own native session**, on agy's own request path, with agy's own tool engine. There is **no agent mediation**: you pick the model, agy drives, and a local mesh translates the wire. No MCP server or side channel exists in this design (a user decision, 2026-10-14).

The agy binary splits its client paths, and that split forces an honest either/or **per session**. agy-pro ships two profiles and lets you choose one per session.

### Pick one profile per session

```bash
agy pro hybrid on p1     # persist profile P1 (picker coexistence)
agy pro hybrid on p2     # persist profile P2 (subagent profile)
agy pro hybrid off       # clear the persisted profile
agy pro hybrid status    # effective profile (env override vs persisted) + wirecheck verdict
agy pro hybrid check     # render the wirecheck report; exit 1 when the effective profile is NOGO
```

- The `AGY_PRO_HYBRID` environment override (`p1` / `p2` / `off`) beats the persisted value at launch time, so a one-off `AGY_PRO_HYBRID=p2 agy` runs P2 without touching the saved profile. A malformed override is ignored and the persisted value decides.
- **One session is one client path.** This is a binary fact proven by probe against agy v1.3.5, not a design choice: the subagent tool family exists only on the genai client path (P2), and catalog composition lives only on the CodeAssist path (P1). Neither profile ships on paper alone; both are gated by `wirecheck` (a NOGO verdict falls back to a true native session with a clear notice, never a half-configured one).

### Profile P1 — picker coexistence

P1 execs the real `agy` behind a per-session local mesh with `CLOUD_CODE_URL` pointed at that mesh. `agy` composes its `/model` catalog from the mesh's `fetchAvailableModels`, so the external models you registered appear in **agy's own picker** and run on **agy's own native tool engine** (11 tools), with agy executing the tool calls locally. **No settings file is written**; the only injected state is the `CLOUD_CODE_URL` variable. Proven end-to-end against real `agy` v1.3.5 (gate G4): externals listed by `agy models`, each selectable and runnable with per-model wire metadata, and a real tool round-trip executed locally.

- **Cost:** the CodeAssist client path attaches **no subagent tool family**, so a P1 session cannot spawn subagents at all (a mesh-emitted `invoke_subagent` is rejected locally by agy as an unknown tool). Subagents are what P2 is for.
- The mesh serves an **external-only** catalog (the "P1-lite" shape): the session's `/model` is the externals you registered. The interactive TUI experience — picker contents, mid-session switching between externals, tool cards, approvals — is verified only by the G8 manual test script and carries the **시각 미검증 (UNVERIFIED)** badge until that script is run.

### Profile P2 — subagent profile

P2 execs `agy` in gemini mode behind the mesh. The shim creates a per-session `--app_data_dir`, writes `modelProvider:"gemini"` plus the managed `customModels` map into **that** per-session settings file (the global settings file is untouched), and execs `agy` with `GOOGLE_GEMINI_BASE_URL` pointed at the mesh and a per-spawn install nonce as `GEMINI_API_KEY`. Externals are **selected at launch** — the launcher picker for a bare TTY session, or `--model <key/id>` — and pinned so the session never lands on a static default by accident. The genai client path attaches the **full 13-tool set including the subagent family** (`invoke_subagent` / `define_subagent` / `manage_subagents`), and a subagent invoked with the default `inherit` **runs on the external session model** (the R6 proof). Proven end-to-end against real `agy` v1.3.5 (gate G5).

- **Cost 1:** externals are **not** listed in agy's in-session `/model`. A P2 session lists the 11 static Gemini defaults; to change the external you relaunch with another `--model`.
- **Cost 2:** subscription Gemini is **off** in the session (contained by the per-session app-data dir). This is the gemini-mode trade.
- **Cost 3 — the static-id policy:** a native/static id is served only if that exact id is in your registry (billed to the configured provider); otherwise the mesh answers a clear Gemini-shaped **404 naming the model**. Never a silent substitution to an external.

### Credential safety

Provider keys are resolved **in-process by NAME** and never placed in the child environment, logs, or settings files. Before exec, the child environment is scrubbed of every `*_API_KEY`-shaped name plus the registry's configured key names. On P2 the mesh authenticates the child with a per-spawn 64-hex install nonce (never a real key). agy's own keyring bearer, which reaches the P1 `/v1internal` mesh passively, is received and discarded and **never forwarded** upstream. The bridge's session key is server-side-only and never validated against a provider. Verified live in gates G4/G5/G6: the provider key VALUE appears in zero local sinks (child env, mesh audit, agy log, settings file); only header names are ever logged.

### Honest limits and accepted residuals

Stated plainly, not hidden:

1. **One session, one client path.** Picker coexistence (P1) and subagent spawning (P2) cannot coexist in one process. You choose per session.
2. **Per-subagent external model binding is impossible** on agy 1.3.5 (closed tier enum; a tier-only `model` frontmatter field). `inherit` is the mechanism: a subagent runs on the session model. Named-role ↔ model parallelism lives in the swarm (separate processes).
3. **Subscription Gemini plus externals in one picker needs the M1 relay**, which needs explicit user approval and is **not** in this release. On P1 today the picker is the externals; keeping subscription Gemini means running without the hybrid profile.
4. **P1's `/v1internal` posture is passive**: agy's keyring bearer is received and discarded, never forwarded. It is bounded by loopback-only bind, an ephemeral port, a 60 req/min limiter, session lifetime, and a names-only audit log.
5. **`go test -race` is not runnable on the dev machine** (no C compiler). That is an environmental degradation, not a PASS, and this project does not claim `-race` coverage.
6. **The conversation-title (housekeeping) turn is answered locally** by the mesh and never spends provider quota (a G10 fix; before it the title turn was 38% of provider traffic).
7. **The agy auto-updater makes a non-generation network contact** to a Google Cloud endpoint even with `AGY_CLI_DISABLE_AUTO_UPDATE=1`. All generation traffic stays on loopback; the "zero Google traffic" claim is scoped to **generation** only.
8. **The G8 TUI gate is 시각 미검증 (UNVERIFIED)**: the interactive picker / switching / tool-card experience awaits the manual test script. The automated gates G4/G5/G6/G10 carry live print-mode evidence.
9. **Migration is additive and reversible**: `agy pro hybrid on p1|p2` persists the profile and `agy pro hybrid off` clears it, returning a plain `agy` to its default native path. P1 writes no settings file; P2 writes only the per-session app-data-dir settings file (the global settings file stays byte-identical, proven in gates G5/G6). Existing gateway/bridge users are unaffected. The design's `sync-models --remove` rollback verb is not part of this release.
10. **A P2 subagent tier pick runs on the external session model.** In a P2 external session, an explicit subagent tier pick (e.g. `model="flash"`) runs on the external session model (agy's own resolution; the mesh never substitutes; a native static id is served only if that exact id is registered, else a clear 404 naming the model). Picking a tier is therefore not a way to land on a cheaper native model in that session: the tier is resolved by agy against the session's own model, and the mesh answers exactly what agy asks for.
11. **The pure-native path intentionally keeps ambient keys.** A plain `agy` with no hybrid profile and no bridge execs the child with the ambient environment (an ambient AI-Studio key included), which is what unshimmed native agy has always done. Every coexistence path (P1, P2, both native fallbacks, the picker, `agy models`, the fanout native seats) scrubs the child instead; the pure-native passthrough deliberately does not, so a native session's own credentials keep working.
12. **P2's `context_window` is cosmetic for routing.** The mesh routes by the bare slug derived from the customModels key, and the external provider governs the real context window. The P1 catalog's `max_tokens` floor is a run-hard-fail guard for P1's listed models; a P2 run whose registry `contextLength` is zero or small is unprobed (G5 did not exercise it), so a near-zero value is best avoided.

### Verification status (honest)

- **G4** (P1 sandbox e2e), **G5** (P2 sandbox e2e), **G6** (one real-provider print-mode run per profile — both live providers, NW and Charm, HTTP 200 on all four profile×provider cells), and **G10** (error classes + retry-storm detector): **PASS**, with live evidence against real `agy` v1.3.5.
- **G11** (auto-update restart): **NOT-TESTED with reason** — no update was available to trigger. The mechanism (env + settings only, zero binary patching) was re-proven with the updater enabled, and the drift branch was proven via a clearly-labeled version stand-in.
- **G8** (live TUI): **시각 미검증 (UNVERIFIED)** pending the manual test script.
- Full suite: `go build ./...`, `go vet ./...`, `go test ./... -count=1 -p 1` → 19 test packages ok, 0 FAIL (plus `profiles`, which has no test files). `go test -race` is not runnable here (no C compiler).

---

## Directory Structure

```
~/.agy-pro/
├── bin/
│   └── agy.exe             # High-performance Go shim binary (takes PATH precedence)
├── backups/
│   └── <timestamp>/        # Non-destructive backups of replaced configurations
├── state.json              # Profile mode, last backup, managed manifest, `default_profile`, `bridge_tools`, `native_seat_concurrency`
├── providers.json          # Provider & model registry (env variable names only)
├── state-fanout.json       # Swarm execution history and family rotation state
└── swarm-state.json        # Saved swarm seat presets (numbered selection text)
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

### 2. Interactive Launcher (opt-in)

A **bare** `agy` (no arguments) goes straight to the native session with zero menu and zero delay. With **`AGY_LAUNCHER=1`** set, at least one external model registered, and stdin an interactive terminal, it shows a 2-second picker instead:
```
===========================================================
  agy Fast Launcher - default in 2s: [1] Native Gemini
===========================================================
Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI
  [1] Native Gemini (default)
  [2] External Models (N models: <providers>)
  [3] Swarm (multi-model fanout)
Select [1/2/3] (Enter=default):
```
- `[2]` opens the **combined model picker**: one numbered list of native and external models (the `agy pro models --all` order). A native number execs real `agy` natively with `--model <id>` and no bridge; an external number starts the bridge for that model. `Enter` cancels and keeps the legacy `[2]` bridge behavior.
- The community banner line is printed here and only here. It never appears in print mode (`-p` / `--print` / `--prompt`), in piped/non-TTY runs, in any `agy pro ...` subcommand, or in machine output (`summary.json`, `models --json`), because the launcher only runs for a bare `agy` with `AGY_LAUNCHER=1` on a TTY with at least one registered model.

The picker belongs to the bare invocation, so any argument skips it (straight to the real binary, zero delay). The menu itself is opt-in via `AGY_LAUNCHER=1`; the overrides below still apply even when the opt-in is set:
- any argument, e.g. `agy agents`, `agy --help`
- `-m` / `--model` or `-b` / `--bridge`
- `-p` / `--print` (upstream one-shot prompt; `--prompt` is its alias)
- `AGY_PRO_BRIDGE=1` (legacy `AGY_BRIDGE=1`)
- `AGY_LAUNCHER=0` (legacy opt-out, now the default)
- stdin is not a TTY (pipes, CI, `</dev/null`)

External models and the swarm stay reachable without the launcher: `AGY_PRO_BRIDGE=1 agy` (or `agy -b`) for the gateway, `agy pro swarm` or the `agypro_swarm` plugin (`/agypro_swarm:swarm`) for the swarm.

### 3. Native Bridge (external models inside `agy /model`)
The bridge is started only when needed and bound to loopback on an OS-assigned port. Each bridge session generates a fresh random 32-byte `crypto/rand` bearer key, injects it as `AGY_LLM_GATEWAY_API_KEY`, and requires it on every loopback request. That session key is local only and is never validated against a provider.
Provider keys are a separate concern: `agy pro doctor` probes each configured provider key upstream with zero tokens (`GET <baseURL>/models`, 5s timeout) and prints a masked verdict per provider (`valid` / `invalid` / `indeterminate` / `SKIP`), plus any conflicting BOM-prefixed environment variable name. Key material is displayed as the first 5 characters followed by `****` (5 characters or fewer render as `****`; no trailing suffix is ever shown). `agy pro doctor --fix-keys` restores a clean name from a probe-valid BOM variant (backup written first) and `agy pro doctor --rollback-keys <dir>` undoes a repair.
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
agy pro config default-profile prompt    # neutral: plain `agy` stays native; the 2-second launcher needs AGY_LAUNCHER=1
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
- Key display is always masked: first 5 characters then `****` (5 characters or fewer render as `****`; a trailing suffix is never shown). While you type a key, characters after the 5th echo as `*`.

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

### 7. Status, Diagnostics & Combined Model List
```bash
# Display active mode, providers, and real agy location
agy pro status

# Display configured providers and detailed model capabilities (context, output, tools, thinking)
agy pro models

# Combined native + external model list, numbered and group-tagged
# ([native] catalog first, then your providers in registry order)
agy pro models --all
agy pro models --json

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
Key material is always displayed masked: first 5 characters followed by `****` (5 characters or fewer render as `****`; no trailing suffix is ever shown).

### 8. Hybrid Multi-Model Swarm (`agy pro fanout`)
```bash
agy pro fanout -task=security -n=3
agy pro fanout -task=review -models="openai/gpt-4o,deepseek/deepseek-chat"

# hybrid: real agy (native Gemini) seats + external provider seats in ONE swarm
agy pro fanout --task review --seats "native:gemini-3.8-flash-high,nw/glm-5.3-flash,bigmodel/glm-4.6"
```
- `native:<model-id>` runs the real `agy` binary as a subprocess seat (its own tool surface, Antigravity subscription quota, rate limits apply mid-swarm). Bare and `provider/model` tokens behave exactly like `--models`.
- At most **2 native seats run concurrently** by default; `--max-native N` or `native_seat_concurrency` (1..8) in `~/.agy-pro/state.json` changes that. Extra native seats queue; provider seats stay parallel.
- A failed native seat does **not** fail over: it is reported as a failed seat (`reason=quota`, `reason=timeout`, or the exit code plus a redacted stderr tail) and the swarm continues.
- Reports record provenance per seat: `kind: native|provider`, a `Quota/Cost` row, and a `Native Seats: k / m` header in `reconciliation.md`.
- `--seats` and `--models` cannot be combined (exit 2). Full reference: [`docs/swarm-hybrid.md`](docs/swarm-hybrid.md).

### 9. Numbered Swarm Builder (`agy pro swarm`)
```bash
agy pro swarm            # interactive: numbered template + numbered seat selection, then run
```
Numbers are the happy path: pick a template (`[1]` Code Review, `[2]` Security Audit, `[3]` Custom), then seats from the numbered combined list — `1,3,5`, a range `2-4`, or a preset (`p1` = 2 native + 2 external, `p2` = 3 external family-rotated, `p3` = 4 mixed). The builder prints the exact `agy pro fanout ...` command before running it; `c` prints it only, `--dry-run` prints it and exits 0.

CI and power users use flags instead (any flag means zero prompts; missing required input exits 2):
```bash
agy pro swarm --template code-review --seats "1,3,5" --root .
agy pro swarm --template security-audit --seats "native:gemini-3.8-flash-high,nw/glm-5.3-flash" --dry-run
```
k3-family models are marked `excluded (pool policy)` and are never auto-seated.

### 10. Settings (`agy pro settings` + `/setting_agypro`)
```bash
agy pro settings             # full-screen, keybinding-driven panel (TUI) - run it in your terminal
agy pro settings --panel p4  # deep-link one panel
agy pro settings show [--json]        # snapshot
agy pro settings set <key> <value>    # default-profile | bridge-tools | native-seat-concurrency
agy pro settings providers list | add-json <file|-> | add-interactive | test <name> | rename <old> <new> | delete <name>
agy pro settings mode switch <full|clean> | restore | list | status
agy pro settings doctor diagnose [--strict] | fix-keys [targets] | rollback-keys <dir>
```
Panels: Launch & Profile, Models & Providers, Bridge Tools & Budgets, Swarm & Fanout, Diagnostics & Keys, Install & Update, Environment Variables. Choices are numbered lists, destructive operations need a `y`/`n` confirmation, and the bridge-tool budgets are compile-time constants shown read-only.

Inside an `agy` session, `/setting_agypro` is a **plugin slash command** (install with `agy plugin install plugins/agypro_settings`; the slash name comes from the file name, same class as `/agy_help`). It is an agent-mediated workflow that runs the non-interactive `agy pro settings ...` verbs behind numbered menus. It does **not** contain, embed, or launch the TUI: for the full-screen panel, run `agy pro settings` in your own terminal. Restart `agy` after installing the plugin so the slash command is discovered.

### 11. Native Coexistence Profiles (`agy pro hybrid`)

```bash
agy pro hybrid on p1     # persist P1 (picker coexistence): externals in agy's own /model
agy pro hybrid on p2     # persist P2 (subagent profile): the 13-tool set incl. subagents
agy pro hybrid off       # clear the persisted profile
agy pro hybrid status    # effective profile (env override vs persisted) + wirecheck verdict
agy pro hybrid check     # render the wirecheck report; exit 1 when the effective profile is NOGO
```

- `on` persists the choice, prints the profile's honest cost line and the launch instruction; it does not start a mesh by itself. `AGY_PRO_HYBRID=p1|p2|off` overrides the persisted value for a single launch. A malformed override is ignored and the persisted value decides.
- `status` and `check` run the `wirecheck` drift gate against the resolved real-agy binary. A NOGO profile fails closed: `check` exits 1, and a launch falls back to a true native session with a clear notice (never a half-configured session). A GO with version drift proceeds and surfaces the drift notice.
- The two profiles are mutually exclusive per session (one session is one client path). Full mechanism, per-profile costs, credential safety, and the honest limits: the Native Coexistence section above and [`docs/bridge-architecture.md`](docs/bridge-architecture.md).

---

## Development & Testing

Built exclusively with the Go standard library (Go 1.22+).

```bash
cd agy-pro
go build ./...
go vet ./...
go test ./... -count=1 -p 1
```

Test packages: `cmd/agy`, `internal/agenttools`, `internal/agysettings`, `internal/bridge`, `internal/fanout`, `internal/geminiwire`, `internal/hybrid`, `internal/inventory`, `internal/launcher`, `internal/mesh`, `internal/mode`, `internal/openaiup`, `internal/paths`, `internal/providers`, `internal/realagy`, `internal/settings`, `internal/swarm`, `internal/wireadp`, `internal/wirecheck`; `profiles` has no test files.

`go test -race` is **not runnable on the development machine** (no C compiler). That is an environmental degradation, not a PASS, and this project does not claim `-race` coverage.

---

## Further Reading

- [`docs/bridge-architecture.md`](docs/bridge-architecture.md) — the gateway XOR constraint, the native-coexistence profiles (P1 picker coexistence, P2 subagent profile) and their local-mesh mechanism, the M4 hardened bridge, and the bridge tool loop that gives external models workspace actions (§5).
- [`docs/swarm-hybrid.md`](docs/swarm-hybrid.md) — hybrid native + external swarms (`--seats`), the numbered swarm builder, the combined model inventory, the settings panel + `/setting_agypro` plugin, key masking and the community banner.
- [`docs/fanout.md`](docs/fanout.md) — multi-model swarm specification.
- [`docs/rules-conflicts.md`](docs/rules-conflicts.md) — Kimi vs Antigravity rule resolution matrix.
- [`docs/skills-port-matrix.md`](docs/skills-port-matrix.md) — 41-skill 1:1 porting matrix.
- Native-coexistence design record and gate evidence: `docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md` and `docs/261014_0007_session_hybrid-impl/` (phase records + blind-qa gate reports G4/G5/G6/G10/G11 + the G8 manual test script).
