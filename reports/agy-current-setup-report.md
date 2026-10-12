# agy Current Setup Report (read-only reconnaissance)

Date: 2026-10-11 | Scope: read-only | Evidence: live commands + string extraction from agy.exe (no execution of gateway paths, no modification).

## 0. TL;DR
- `agy` = **Go binary** (Google-internal `jetski/cli` code, 193 MB), v **1.3.3**, at `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe` (on PATH). Self-updates in place (`agy.exe.<ts>.old` kept next to it). Stale older copy also at `C:\Users\k1yt\.gemini\bin\agy.exe` (186 MB, Aug 30; not on PATH).
- **External providers ARE supported natively** via three mechanisms found in the binary (see 3): `AGY_LLM_GATEWAY_*` env vars (new, most relevant), `modelProvider:"gemini"` + `GEMINI_API_KEY` + `GOOGLE_GEMINI_BASE_URL`, and a `customModels` array in settings.json.
- **No Go toolchain installed** (`go` not found; no `C:\Program Files\Go`). Available: Node v24.19.0, Rust cargo 1.98.0 / rustc, winget (can `winget install GoLang.Go`).
- **No built-in `/mode` conflict risk found**: agy has `--mode` flag (accept-edits|plan) and an `executionMode` setting; a shim-owned `/mode` for full/clean would be a separate concept. Slash commands are discovered from `~/.gemini/config/workflows*` / skills; no `/mode` file exists.

## 1. How agy is installed / launched; shim interception points
### 1.1 Layout
| Item | Path |
|---|---|
| Launcher on PATH | `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe` (User PATH entry; `agy install` configures PATH/shell) |
| Prior binary | `...\agy\bin\agy.exe.1791645022539060900.old` (190 MB) |
| Stale copy | `C:\Users\k1yt\.gemini\bin\agy.exe` (not on PATH) |
| App data dir | `C:\Users\k1yt\.gemini\antigravity-cli\` (settings.json, history.jsonl, cli.log, conversations, brain, builtin, updater, plugin_data, hooks…) |
| Global customization dir | `C:\Users\k1yt\.gemini\config\` (config.json, mcp_config.json, rules/, skills/, plugins/, hooks/, projects/, import_manifest.json) |
| Legacy/shared | `C:\Users\k1yt\.gemini\{AGENTS.md,GEMINI.md}`, `~/.gemini/antigravity/` (Desktop app data), `~/.gemini/antigravity-dashboard` |
| Helper | `antigravity-cli\bin\agentapi.bat` -> `agy.exe agentapi %*`; `webm_encoder.exe` |
| Node tools | `%APPDATA%\npm\{agy-tools,agy-tokens,agy-dashboard}.{cmd,ps1}` (from workspace package.json, v0.10.3) |
| Version | `agy --version` -> `1.3.3` (exe has no PE VersionInfo: 0.0.0.0) |
| Updater | `agy update`; `antigravity-cli\updater\{update.lock,update_status.json}`; env `AGY_CLI_DISABLE_AUTO_UPDATE` exists. Replaces `agy.exe` in place -> **a shim must NOT overwrite agy.exe or sit at the same path**; put shim earlier on PATH (e.g. `%USERPROFILE%\.agy-shim\bin\agy.exe`) and resolve the real binary dynamically. |

### 1.2 CLI flags (agy --help, 1.3.3)
`--add-dir, --agent, -c/--continue, --conversation, --dangerously-skip-permissions, --disable-slash-commands, --effort low|medium|high|xhigh|max, --input-format, --json-schema, --log-file, --mode (accept-edits|plan), --model, --new-project, --output-format, -p/--print, --print-timeout, --project, --prompt, -i/--prompt-interactive, --remote-control, --sandbox`.
Subcommands: `agent(s), changelog, help, install, mcp, mic-serve, models, plugin(s), remote-control, update`.
(No `--config-dir`, `--settings`, or `--home` flag.)

### 1.3 Environment variables present in agy.exe (string extraction)
- Gateway / provider: `AGY_LLM_GATEWAY_URL, AGY_LLM_GATEWAY_API_KEY, AGY_LLM_GATEWAY_HEADERS, AGY_LLM_GATEWAY_CA_CERT, AGY_LLM_GATEWAY_MODELS, AGY_LLM_GATEWAY_PROXY_URL, AGY_LLM_GATEWAY_WIRE_PROTOCOL`; `GEMINI_API_KEY, GOOGLE_GEMINI_BASE_URL`.
- Network: `HTTP_PROXY, HTTPS_PROXY, SSL_CERT_FILE, NODE_EXTRA_CA_CERTS`.
- Behaviour: `AGY_CLI_DISABLE_AUTO_UPDATE, AGY_CLI_MODEL_API_MAX_RETRIES, AGY_CLI_MODEL_EXPERIMENT_PROFILES, AGY_CLI_HIDE_LOGO, AGY_CLI_LOGO_STYLE, AGY_CLI_INTERACTIVE_HEADLESS, AGY_CLI_NONINTERACTIVE_HEADLESS, AGY_CLI_FAST_PROMPT_EDITOR, AGY_CLI_DISABLE_SAFETY_FILTERING, AGY_ADC_AUTH, AGY_ACCOUNT, AGY_ENABLE_HUB, AGY_LANG (set in this session)`.
- Dirs/IPC (set by parent agy for child sessions): `ANTIGRAVITY_APP_DATA_DIR` (currently `C:/Users/k1yt/.gemini/antigravity-cli`), `ANTIGRAVITY_AGENTAPI_EXE`, `ANTIGRAVITY_EXECUTABLE_DATA_DIR`, `ANTIGRAVITY_LS_ADDRESS/CSRF_TOKEN/LS_VERSION`, `ANTIGRAVITY_CONVERSATION_ID/TRAJECTORY_ID/PROJECT_ID`, `ANTIGRAVITY_NODE_PATH`, `ANTIGRAVITY_MULTICALL_DESTINATION`.
- `USERPROFILE` and `XDG_CONFIG_HOME` strings present (Windows home resolution via USERPROFILE).
- **NOT found in binary**: `GEMINI_CLI_HOME`, `AGY_HOME`, `AGY_CONFIG_DIR` (the latter is only used by this repo's configure-customizations.js, not agy itself).

### 1.4 What a shim can intercept
1. **PATH precedence** (safest, update-proof): shim `agy.exe` ahead of `...\agy\bin`, locate real agy via `Get-Command -All`/registry of last-known path/`%LOCALAPPDATA%\agy\bin\agy.exe`, exec with inherited stdio (need ConPTY-transparent passthrough: agy is a full-screen TUI, use real console handle inheritance, no pipe wrapping in interactive mode).
2. **Env injection before exec**: `AGY_LLM_GATEWAY_*`, `GEMINI_API_KEY`, `GOOGLE_GEMINI_BASE_URL`, `HTTP(S)_PROXY`, `USERPROFILE` (see 2.3 caveat), `AGY_CLI_DISABLE_AUTO_UPDATE`.
3. **Flag injection**: `--model`, `--agent`, `--effort`, `--mode`, `--project`.
4. **settings.json rewrite pre-launch** (`model`, `modelProvider`, `customModels`, `statusLine`, `hooks`) - atomic write + backup; file is shared with running sessions.
5. **Customization dir switch** by file ops (see 2).
6. **Local reverse proxy**: shim can spawn an in-process Go HTTP gateway and point `AGY_LLM_GATEWAY_URL`/`GOOGLE_GEMINI_BASE_URL` at it (translation Gemini<->OpenAI etc.).
7. agy `statusLine.command` + `hooks.json` PostInvocation already call `agy-tokens` (node); shim can also host these.

## 2. Customization layout and full/clean switching
### 2.1 Current "full" set (deployed to `~/.gemini/config`; source in workspace, deployed by `scripts/lib/configure-{rules,customizations}.js`, which back up to `*.bak.<ts>` and support env overrides `AGY_CONFIG_DIR`, `AGY_RULES_DIR`)
- `rules/AGENTS.md` (10 KB, VP-of-Engineering orchestration) and `rules/GEMINI.md` (4 KB) (+ many `.bak*` files in same dir - these may be globbed as rules; worth cleaning/moving in shim).
- `skills/`: autonomous-orchestrator, code-review-taxonomy, design-3d-canvas, design-core-harness, design-cyberpunk-brainmap, design-interactive-sandbox, design-vector-svg, quality-gate, security-review-orchestrator, usage.
- `plugins/`: agy_help, code_reviewer, deep_investigator, designer, security_reviewer. Each has `plugin.json`, `README.md`, `agents/<name>/agent.md`, `skills/*/SKILL.md`. (`agy agents` lists only code-reviewer, designer, security-reviewer; deep_investigator exposes a skill.)
- `hooks/hooks.json` (PostInvocation -> `agy-tokens --hook`), `mcp_config.json` (empty/minimal), `config.json` ({"plugins":{}, userSettings.remoteControlHostname}), `projects/default-cli-project.json`, `import_manifest.json`.
- User-level duplicates also exist at `~/.gemini/{AGENTS.md,GEMINI.md}` and `~/.gemini/antigravity-cli/hooks.json` is a valid location too (strings in binary: `~/.gemini/GEMINI.md`, `~/.gemini/AGENTS.md`, `~/.gemini/antigravity-cli/hooks.json`, `~/.gemini/config/{workflows,global_workflows,sidecars}`).
- Subagent model selection: `agent.md` frontmatter (name, description, mainAgent, subagent, hidden, inheritMcp, tools[]) has **no `model` field in code_reviewer sample**; subagents appear to inherit session model. Not verified whether a `model:` frontmatter key is honored (binary has `buildCustomAgentModel`/`resolveCustomAgentModels` -> suggests custom agent model resolution exists; needs a probe).
- Builtin docs: `~/.gemini/antigravity-cli/builtin/skills/{agy-customizations/docs/{skills,rules,plugins,hooks,mcp_servers,json_configs}.md, antigravity_guide/references/*, automation, plugin, ...}` (re-extracted by agy on update; `.checksum`).

### 2.2 Discovery rules (from builtin json_configs.md)
- `~/.gemini/config/` global; `.agents/` in project dirs (cwd up to repo root) are honored; `skills.json` / `plugins.json` manifests support `entries[]`, `inherits[]`, `include_only`, `exclude` regexes.
- => **Clean mode can be done without moving files**: write `~/.gemini/config/skills.json`/`plugins.json` with `exclude` patterns, OR the shim swaps directories. (Whether manifests also filter default-discovered dirs vs. only extra paths: docs say `exclude` "never filter customizations inherited from the user's environment" for explicitly configured paths - so exclusion of default-dir items likely does NOT work; need probe.)
- Plugins can be toggled: `agy plugin list|enable|disable|install|uninstall`.
- `--disable-slash-commands` only for print mode.

### 2.3 Switching strategies (ranked)
1. **Plugin enable/disable + file-set swap by shim (recommended)**: before exec, shim atomically renames/junctions `rules/`, `skills/`, `plugins/`, `hooks/` between `~/.gemini/config` and per-profile stores (`%USERPROFILE%\.agy-shim\profiles\{full,clean}\`), using NTFS directory junctions (`mklink /J`, no admin). Caveat: global state shared by concurrent agy sessions - switch is global, not per-process.
2. **Per-process isolation via `USERPROFILE` override**: agy resolves `~` (USERPROFILE string found). Setting `USERPROFILE=<profile-root>` would redirect `~/.gemini/**` entirely (settings, auth, history) - gives true per-process modes but also moves login credentials/keyring (auth) and conversation DBs; Windows keyring auth (`keyringAuth`) may break; **must be probed on a scratch profile**. Also `ANTIGRAVITY_APP_DATA_DIR` is exported for child processes (appears to be an output rather than an input; unverified as input override).
3. **Project-level `.agents/` only**: put full set in a project `.agents/` and keep global clean; "full" = launch in a dir with `.agents/`. Per-cwd, not a toggle.
4. In-session `/mode` impossible via shim alone (TUI owns slash commands) unless implemented as a workflow/skill file (`~/.gemini/config/workflows/mode.md`) that instructs agent to call `agy-shim mode <x>` and requires restart/new conversation.

## 3. Models / external providers
### 3.1 Current
- settings.json `"model": "Claude Sonnet 5.5 (High)"` (label string), `useG1Credits: true`, `gcp.project=gen-lang-client-0936248191`, `verbosity: high`, `toolPermission/artifactReviewPolicy: always-proceed`, `statusLine.command: agy-tokens --hook --raw --write-dashboard`, `trustedWorkspaces[]`.
- `agy models` (live): Gemini 3.8/3.7/3.6 Flash (High/Medium/Low), Gemini 3.1 Pro (High/Low), Claude Opus 5.5 (L/M/H), Claude Sonnet 5.5 (L/M/H), GPT-OSS 120B (Medium). IDs like `claude-sonnet-5-5-high`. `--model <id|label>` overrides per session. cli.log shows `model_config_manager.go: Propagating selected model override to backend: label="..."`.
- Provider enum in binary: `API_PROVIDER_{GOOGLE_GEMINI, GOOGLE_VERTEX, ANTHROPIC_VERTEX, OPENAI_VERTEX, GOOGLE_EVERGREEN, INTERNAL, UNSPECIFIED}`. `OPENAI_COMPATIBLE`, `ANTHROPIC_COMPATIBLE` string constants present.

### 3.2 Native external-provider hooks (binary evidence; behaviour NOT yet live-tested)
A. **LLM Gateway env vars (new)**: `AGY_LLM_GATEWAY_URL` (validated: must be http/https with hostname; error text example `http://localhost:4000` or `https://gateway.corp` -> LiteLLM-style gateway), `_API_KEY`, `_HEADERS`, `_CA_CERT`, `_MODELS` (model list to expose), `_PROXY_URL`, `_WIRE_PROTOCOL` (accepted values unknown; probably gemini/openai/anthropic - probe needed). This is the intended enterprise path to route to external-provider models and is the best target for the shim.
B. **Direct Gemini API** (changelog 1.1.13): `settings.json: "modelProvider": "gemini"` + `GEMINI_API_KEY` + optional `GOOGLE_GEMINI_BASE_URL`. Removing `modelProvider` restores sign-in. Error text confirms semantics. Endpoint must speak Gemini /v1beta (not OpenAI) -> a shim-hosted Go translator (OpenAI/Anthropic <-> Gemini) could back this.
C. **`customModels`** in settings.json: array (`[]types.CustomModelConfig`; error `customModels[%s]: modelName is required`; fields per prior report: modelName, context_window, modelFeatures; also resolvers `adHocCustomModelResolver`, `customModelsResolver`, `ResolveCustomModelLabel`, `constructCustomModelInfo`). Schema details beyond modelName not verified.
- Prior workspace reports (reports/antigravity-proxy-research.md, external-proxy-deep-dive.md, model-expansion-strategy.md) discuss `sortedcord/antigravity-proxy` (Go, Gemini /v1beta -> Cloud Code), explicitly NOT OpenAI-compatible; they do not mention `AGY_LLM_GATEWAY_*` (new finding).
- Subagent model: no per-agent model key confirmed; subagents likely inherit main session model/`--model`. `resolveCustomAgentModels`/`buildCustomAgentModel` symbols hint at agent-level custom model support; probe by adding `model:` to a scratch agent.md.

## 4. Existing workspace files relevant (D:\OneDrive\Projects\Antigravity-cli)
- `package.json` (agy-tools 0.10.3, Node, zero-dep), `bin/agy-{tools,tokens,dashboard}.js`, `src/*` (config.js 36K, gemini-quota.js 84K, hook-handler.js, log-parser.js, price-syncer.js, serve.js, i18n.js 184K…), `scripts/{install.bat,install.sh,lib/configure-{rules,customizations,statusline}.js}`, `rules/`, `skills/`, `plugins/`, `hooks/hooks.json`, `data/`, `test/` (`node test/run-tests.js`), `docs/`, `.vscode/`, nested `agy-tools/` git repo copy.
- Deployment model already exists (repo -> `~/.gemini/config`, backup + atomic write, `AGY_CONFIG_DIR` override): the full/clean profile deployer can reuse this.
- Relevant reports: `reports/antigravity-proxy-research.md`, `external-proxy-deep-dive.md`, `model-expansion-strategy.md`, `agent-infrastructure-topology.md`, `agy-cli-audit.md`. No existing Go code or shim project. 
- Git: workspace is a repo (`.git`).

## 5. Toolchains
- `go`: **NOT installed** (no PATH entry, no `C:\Program Files\Go`, no `%USERPROFILE%\go`).
- Node v24.19.0 (`C:\Program Files\nodejs`), Rust cargo 1.98.0 (`~/.cargo/bin`), winget available (`winget install GoLang.Go` or `--id GoLang.Go`). Shim in Go requires installing Go; alternative: Rust/Node shim.

## 6. Open questions / probes recommended (sandboxed)
1. Accepted values of `AGY_LLM_GATEWAY_WIRE_PROTOCOL` and `_MODELS` format (run agy against a local echo HTTP server and inspect requests; use `--log-file`).
2. Does `USERPROFILE` override redirect `~/.gemini` fully, and how is auth preserved (keyring/ADC)?
3. Does `exclude` in skills.json/plugins.json filter default-discovered global items?
4. Is `model:` honored in agent.md frontmatter?
5. customModels full schema (`apiProvider`, base URL per model?) - test by adding an entry and `agy models`.
6. Windows TUI passthrough: Go shim should spawn child with inherited console handles (`os/exec` with Stdin/Stdout/Stderr = os.Std*; forward Ctrl+C; propagate exit code) - no ConPTY needed.

Confidence: High for layout/PATH/flags/version (direct observation); Medium-High that env vars/settings keys exist (binary strings + official changelog 1.1.13 text); Low-Medium on exact semantics of AGY_LLM_GATEWAY_* and customModels (not tested).