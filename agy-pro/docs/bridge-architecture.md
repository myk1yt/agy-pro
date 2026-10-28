# Bridge Architecture: Model Coexistence in `agy`

This note explains why `agy-pro` needs a launcher, a default-profile setting, and a Google AI Studio
preset, and what the local bridge does at runtime.

Primary evidence: [`reports/gateway-models-coexistence-experiment.md`](../../reports/gateway-models-coexistence-experiment.md)
and [`reports/user-model-coexistence-ux-report.md`](../../reports/user-model-coexistence-ux-report.md)
(both produced against `agy.exe` v1.3.3).

---

## 1. The constraint: gateway models are an all-or-nothing switch

`agy.exe` resolves its model catalog in `codeassistclient.fetchAvailableModels` (VA `0x141d1a900`).
When the `AGY_LLM_GATEWAY_MODELS` environment variable is non-empty, the binary calls
`buildGatewayAvailableModels` (VA `0x141d19940`) and **returns only that list**, discarding the built-in
catalog produced by `buildDefaultAvailableModels` (VA `0x141d190a0`).

Disassembly and live capture established three facts:

1. Every entry parsed from `AGY_LLM_GATEWAY_MODELS` is stamped with `provider = "Gateway"`, and the
   catalog group slice is overwritten with a single `"Enterprise Gateway Models"` group. There is no
   prefix, no `append:` syntax, and no merge branch that keeps default models as `personal_consumer`.
2. Because routing follows the provider tag, a model name that appears in the gateway list is sent to
   `AGY_LLM_GATEWAY_URL`, never to Google's internal endpoint (`aicode.googleapis.com`). A model name
   omitted from the list disappears from `/model` entirely.
3. `settings.json` `customModels` cannot be used as a workaround: `buildCustomAgentModel`
   (VA `0x142ab6c80`) enforces the regex
   `^(?:gemini-api:|blade:|evergreen:|beyond-api:)(?:.*?/models/(.+)|(?:/)?([^/]+))$`, which rejects
   third-party OpenAI-compatible ids, and the `gemini-api:` scheme speaks Google's RPC framing rather
   than OpenAI's.

Result: native Antigravity models and bridge (external) models **cannot coexist in one `agy /model`
catalog**. The user must pick one catalog per session.

| Session state | `AGY_LLM_GATEWAY_*` | Native Gemini in `/model` | External models in `/model` | Works cleanly |
|---|---|---|---|---|
| Native (plain `agy`) | off | yes | no | yes |
| Gateway with native names included | on | listed, but requests fail at the bridge | yes | no |
| Gateway with external models only | on | no | yes | yes |

---

## 2. The solution: pick the catalog per session, not per binary

`agy-pro` cannot patch `agy.exe`, so it moves the choice to three user-facing surfaces.

### 2.1 Interactive Launcher (default)

A **bare** `agy` (no arguments) in an interactive terminal, with at least one external model
registered, shows a 2-second picker:

```
===========================================================
  agy Fast Launcher — default in 2s: [1] Native Gemini
===========================================================
Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI
  [1] Native Gemini (default)
  [2] External Models (N models: <providers>)
  [3] Swarm (multi-model fanout)
Select [1/2/3] (Enter=default):
```

Pressing `Enter` or letting the timer expire starts native `agy` with zero gateway variables and no
delay. The community banner line is rendered here and only here: it cannot appear in print mode
(`-p` / `--print` / `--prompt`), in piped/non-TTY runs, in any `agy pro ...` subcommand, or in machine
output, because `ShowMenu` only runs for a bare `agy` on a TTY with at least one registered model.

`[2]` opens the **combined model picker** — one numbered list of native and external models (the
`agy pro models --all` order). A native number execs real `agy` natively with `--model <id>` and a
scrubbed environment (no bridge, no gateway variables); an external number starts the bridge for that
model. `Enter` cancels the picker and keeps the legacy `[2]` bridge behavior. See
[`swarm-hybrid.md`](swarm-hybrid.md) §3.1.

The picker belongs to the bare invocation, so any argument skips it and goes straight to the
real binary. The documented bypasses:

| Bypass | Example |
|---|---|
| any argument (the menu is bare-`agy` only) | `agy agents`, `agy --help` |
| explicit model or bridge flag | `-m` / `--model`, `-b` / `--bridge` |
| one-shot prompt flag | `-p` / `--print` (`--prompt` is the upstream alias) |
| bridge environment | `AGY_PRO_BRIDGE=1` (legacy `AGY_BRIDGE=1`) |
| launcher switched off | `AGY_LAUNCHER=0` |
| non-interactive stdin | pipes, CI, `</dev/null` |

### 2.2 Default Profile (`agy pro config default-profile`)

Users who do not want a prompt lock their startup behavior in `~/.agy-pro/state.json`
(`default_profile`):

- `native` — plain `agy` always boots 100% native (Antigravity subscription quota intact).
- `gateway` — plain `agy` always starts the bridge and exposes every registered external model in
  `/model` immediately, which is the closest available equivalent to a multi-model pool.
- `prompt` — the launcher (default).

### 2.3 Google AI Studio preset — how to get Gemini and external models in ONE `/model` list

Registering the `google` preset in `agy pro setup` adds public Gemini models
(`gemini-2.5-flash`, `gemini-2.5-pro`) served through Google AI Studio's OpenAI-compatible endpoint
(`https://generativelanguage.googleapis.com/v1beta/openai/`, key `GEMINI_API_KEY`). Because these
models are ordinary bridge models, they appear in the **same** gateway list as PurgedProvider, Charm,
DeepSeek, and OpenRouter models. With `agy pro config default-profile gateway`, the user sees Gemini
public models and external models side by side in one `/model` picker.

Steps, in order:

```bash
agy pro setup                            # register the `google` preset (AI Studio key)
agy pro config default-profile gateway   # plain `agy` now boots into the gateway
agy                                      # /model lists Gemini 2.5 + every external model together
```

This is the closest in-session equivalent to a multi-model pool. The complete native + external list
(including the Antigravity preview models) is available outside the session instead:
`agy pro models --all`, the launcher picker (`[2]`), and `agy pro swarm`
([`swarm-hybrid.md`](swarm-hybrid.md) §3).

Trade-off: this path bills Google AI Studio API tokens instead of using the Antigravity subscription
quota. The native Antigravity preview model `gemini-3.8-flash-high` is **not** published on Google AI
Studio, so it cannot be proxied. Preserving that model requires native mode (launcher option `[1]` or
`default_profile = native`).

---

## 3. Bridge runtime mechanics

- **Binding**: loopback only (`127.0.0.1`), OS-assigned ephemeral port, process-scoped lifetime. The
  listener is closed when the `agy` child exits.
- **Auth: two different keys, do not conflate them**:
  - **Session bearer key (local, bridge ↔ `agy`)**: every bridge session generates a fresh 32-byte
    `crypto/rand` hex key and injects it as `AGY_LLM_GATEWAY_API_KEY`. The bridge requires it in the
    `Authorization: Bearer` header on every `/models` and `/chat/completions` request, and fails
    closed on a missing or mismatched key. This key never leaves loopback and is never validated
    against a provider.
  - **Provider API keys (upstream, bridge → provider)**: resolved from the registry's environment
    variable names, exact name first with BOM-tolerant variants as fallback. `agy pro doctor`
    validates each configured provider key with a zero-token upstream probe
    (`GET <baseURL>/models`, Bearer, 5s timeout) and prints a masked verdict per provider: `valid`
    (2xx), `invalid` (401/403), `indeterminate` (any other HTTP status), or `SKIP` (unresolved key
    or blocked network; never an error). It also reports conflicting environment variable names, for
    example a BOM-prefixed `\uFEFFNW_API_KEY` shadowing `NW_API_KEY`.
    The doctor does not change its exit code on verdicts (an `invalid` key still exits 0); judge the
    printed verdict, not the exit status.
  - **Key display (masking rule)**: every place that shows key material - wizard echo, wizard
    confirmations, provider listings, settings panels, doctor / fix-keys / rollback output - renders
    the **first 5 characters followed by `****`** (5 characters or fewer render as `****`; a trailing
    suffix is never shown). While typing a key in the wizard, characters after the 5th echo as `*`.
  - **Repair**: `agy pro doctor --fix-keys` restores the clean name from a probe-valid BOM variant,
    writing a DPAPI-encrypted v2 backup (`registry-keys.v2.json`) first (legacy v1 backups stay
    readable for rollback); it mutates only when the clean name is absent or probe-invalid
    and a variant probes `valid`, and never mutates on a blocked or `indeterminate` probe.
    `agy pro doctor --rollback-keys <dir>` restores a previous repair backup.
- **Wire protocol**: `AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai`; `agy` posts to `/chat/completions` and the
  bridge rewrites the model back to the provider's raw id before forwarding.
- **Streaming**: the reverse proxy runs with `FlushInterval: -1` so SSE chunks reach `agy` as they
  arrive.
- **Catalog**: `AGY_LLM_GATEWAY_MODELS` carries both `model (provider)` and the bare `model` id, so the
  TUI picker and `--model` flags both resolve.

---

## 4. Summary of what is and is not possible

| Goal | Possible | How |
|---|---|---|
| Native Gemini 3.8 preview with full subscription quota | yes | native mode (launcher `[1]` / `default_profile = native`) |
| External models inside `agy /model` | yes | `-b` / `-m <external>` / `default_profile = gateway` |
| External + public Gemini in one `/model` list | yes | register the `google` preset, then `default_profile = gateway` |
| External + native Gemini 3.8 preview in one `/model` list | no | binary-level XOR; requires patching `agy.exe` |
| All native + external models in one agy-pro list (outside the session) | yes | `agy pro models --all`, launcher picker `[2]`, `agy pro swarm` ([`swarm-hybrid.md`](swarm-hybrid.md)) |

---

## 5. Bridge tool loop (R1 contract)

External models gain Gemini-parity workspace actions through a loop that lives entirely inside the
bridge. `agy` stays unaware: it sends a plain chat request and receives plain text.

### 5.1 Why the loop must be local

Live capture of the gateway client shows two hard facts: the request never carries `tools` /
`tool_choice`, and a response `tool_calls` array is discarded before the TUI sees it. Nothing in
`agy` will execute a tool call for a gateway model, so the bridge attaches tools, executes the calls,
re-feeds the results, and synthesizes a text-only answer.

### 5.2 Loop algorithm (aggregate then synthesize)

1. Clone the request messages per request (stateless; `agy` resends the full history each turn).
2. Append tool-protocol instructions to the system/developer message (insert one if absent).
3. Per iteration: call the provider with the loop's tool definitions, aggregate the stream internally,
   preserve every original request field for passthrough fidelity.
4. `tool_calls` in the response: execute each one through the local tool runner, append `role:"tool"`
   messages, iterate (max 8 turns, 60s per upstream turn, 240s wall clock).
5. Final text: synthesize the reply in the shape the request asked for - a `chat.completion.chunk`
   SSE stream for `stream:true` (reasoning content preserved, usage on the last chunk) ending with
   `finish_reason:"stop"` and `[DONE]`, or a single `chat.completion` JSON body for
   `stream:false` / an absent `stream` field (no SSE in that case).
6. Every exit path (panic, timeout, iteration exhaustion) ends with non-empty text or a fixed apology.
   A textless response triggers the `agy` retry storm (634 requests in 40s, observed), so it is
   structurally avoided.

### 5.3 Gating and precedence

| Layer | Rule |
|---|---|
| Loop trigger | config enabled AND request has no `tools` key AND the model supports tools AND the allow-list is non-empty |
| Allow-list source | `AGY_PRO_BRIDGE_TOOLS` env > `bridge_tools` in `~/.agy-pro/state.json` > default `read,search` |
| Loop off | request goes through the byte-identical reverse-proxy path (no `tools` attached, no synthesis) |
| Wire scope | OpenAI wire only; a gemini-wire request that reaches the loop path is answered with an explicit logged 404 |

The allow-list is bridge-tools config only. A model's per-model `AllowedTools` capability stays the
swarm's (`agy pro fanout`) concern; the loop gates on `SupportsTools` alone, which is why configuring
`write` / `exec` actually enables them.

### 5.4 Workspace root and safety

- **Workspace root**: the shim writes `AGY_PRO_WORKSPACE` into **its own** process environment
  immediately before starting the bridge (value = the directory `agy` was started in). An existing
  value is never overwritten (environment wins over cwd), and the variable is deliberately kept out of
  the child `agy` environment, which still carries exactly the four `AGY_LLM_GATEWAY_*` entries. Two
  sessions are two shim processes, so two workspaces stay isolated.
- **Path safety**: every path resolves through the same guard as the swarm's tool runner
  (symlink re-check included), command execution uses a sanitized environment allow-list, and every
  tool output is redacted before it is logged or sent upstream.
- **Budgets**: per-tool execution deadline 30s enforced by the loop for every tool; `run_command`'s
  `timeout_seconds` argument is clamped to 30 at the tool layer (120s hard cap stays there); at most
  16 tool calls execute per turn, further calls fail without running.

### 5.5 User-facing surfaces

| Surface | Command / variable |
|---|---|
| Persisted config | `agy pro config bridge-tools <read,search,write,exec\|none>` |
| Display | `agy pro config show` (prints `Bridge Tools`) |
| Per-session override | `AGY_PRO_BRIDGE_TOOLS` |
| Live one-shot runs | `agy --print-timeout 300s` (the loop ceiling is 240s) |

Tool work is invisible in `agy`'s own history across turns (the TUI only stores the final text). This
is an accepted v1 trade-off: the bridge keeps the audit log, `agy` keeps a clean conversation.

---

## 6. Related surfaces

- **Combined model list (outside the session)**: `agy pro models --all`, the launcher picker `[2]`, and
  the `agy pro swarm` builder — [`swarm-hybrid.md`](swarm-hybrid.md) §3.
- **Settings**: `agy pro settings` is the full-screen, keybinding-driven panel you launch in your own
  terminal; `/setting_agypro` is a **plugin slash command** that drives the non-interactive
  `agy pro settings ...` verbs through numbered menus and never contains or launches the panel —
  [`swarm-hybrid.md`](swarm-hybrid.md) §4.
- **Key masking**: first 5 characters followed by `****` wherever a key is displayed (§3 above).
