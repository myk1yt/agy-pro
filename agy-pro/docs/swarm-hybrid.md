# Hybrid Swarms, Numbered Seat Selection & Settings

This note covers the surfaces added on top of the classic `agy pro fanout --models` swarm:

- **hybrid swarms** — real `agy` (native Gemini) seats and external provider seats in ONE run (`--seats`),
- **partition mode**: a plan file of disjoint work units, one PROVIDER seat per unit, merged into one
  deliverable (`agy pro fanout --units <planfile>`; [`fanout.md`](fanout.md) §8),
- **numbered seat selection** — `agy pro swarm`, the interactive builder (numbers are the happy path; flags
  are the power-user/CI fallback),
- **one combined model list** — `agy pro models --all` and the launcher's model picker,
- **one settings surface** — `agy pro settings` (full-screen panel) and `/setting_agypro` (plugin command),
- **API-key masking** and the **community banner**.

Everything below was verified against the shipped code and, where noted, against real `agy` v1.3.3.

---

## 1. Hybrid swarms: native + external seats in one run

```bash
agy pro fanout --task review --seats "native:gemini-3.8-flash-high,nw/glm-5.3-flash,bigmodel/glm-4.6"
```

### 1.1 Seat spec grammar (`--seats`)

| Token form | Seat kind | What runs |
|---|---|---|
| `native:<model-id>` | native | the real `agy` binary as a subprocess, print mode, pinned to that model |
| `provider/model`, `model (provider)`, `model` | provider | the existing OpenAI-compatible client (identical to `--models`) |

- Comma-separated; whitespace around tokens is trimmed. Seat order = seat number order (seat 1..N).
- `native:` is case-insensitive and REQUIRED for a native seat; the native model id must not contain `/`.
- Bare tokens are always provider specs, so `--seats` keeps `--models` semantics for external seats.
- The seat count comes from the token count (`--n` is not consulted when `--seats` is used).
- `--seats` and `--models` cannot be combined: `Error: --seats and --models cannot be combined`, exit 2.
  Malformed seat specs also exit 2 (`seat N: ...`), before any seat runs.
- Duplicate tokens are accepted (the swarm builder dedupes; the raw CLI stays permissive).

### 1.2 How a native seat runs

`<real agy> --model <model-id> --disable-slash-commands -p "<seat prompt>" --print-timeout <budget>s`

- `<real agy>` uses the same resolution order as the launcher: `AGY_PRO_REAL_AGY` > `PATH` scan (the shim
  itself is excluded) > `%LOCALAPPDATA%\agy\bin\agy.exe`.
- The prompt is the seat-role preamble (anti-nesting control, `buildNativeSeatPrompt`) first, then the task
  preset system prompt, then your text; multi-line prompts travel over `-p` argv (verified
  byte-faithful on `agy` 1.3.3). If the resolved binary is a `.cmd`/`.bat` wrapper, a multi-line prompt
  automatically switches to stdin transport.
- The seat's working directory is the swarm root (`--root`), so a native seat analyzes the same workspace as
  the provider seats.
- `--print-timeout` is the remaining swarm budget minus 5 seconds (floor 10s), so `agy`'s own timeout fires
  first with clean diagnostics; the process context is the hard backstop.
- Child environment is scrubbed: no `AGY_LLM_GATEWAY_*`, no provider API-key variables (including
  BOM-prefixed variants), no `AGY_PRO_BRIDGE`/`AGY_BRIDGE`. Native seats authenticate through your
  Antigravity login and never see provider keys. `AGY_PRO_ACTIVE=1` is set so a resolved shim cannot recurse.
- Native seats use `agy`'s own tool surface (its own sandbox and approval model). They are NOT wrapped in
  `agy-pro`'s tool runner; that runner belongs to provider seats.

### 1.3 Concurrency cap

Real `agy` is a ~193 MB binary, so native seats are throttled:

- Default **2 concurrent native seats**; extra native seats queue and start as slots free.
- Precedence: `--max-native <n>` flag > `native_seat_concurrency` in `~/.agy-pro/state.json` > default 2.
  The stored value is clamped to 1..8 (`agy pro settings set native-seat-concurrency <n>` writes it).
- Provider seats keep running in parallel with no cap; the whole swarm stays bounded by `--timeout`.

### 1.4 Failure semantics (no failover for native seats)

A native seat has no family to fail over to. A failed native seat stays a failed seat, exactly like a
provider seat whose 3-step failover is exhausted:

| Outcome | Seat result |
|---|---|
| exit 0, non-empty stdout | `success`; stdout is the seat answer |
| exit 0, empty/whitespace stdout | `failed` — `native seat produced empty output` (never a silent hang) |
| non-zero exit | `failed`, degraded; error carries the exit code plus a redacted stderr tail. `429` / `quota` / `resource_exhausted` in the stderr tail prefixes the reason with `reason=quota` (an `AGY_ERROR` line on its own does NOT trigger it) |
| swarm deadline reached (process killed) | `failed` — `reason=timeout` |
| binary missing / spawn failure | `failed` — `native seat spawn failed: <err>`; sibling seats are unaffected |

The swarm always continues; reconciliation lists the failed seat with its reason. Quota: native seats draw on
the Antigravity subscription (rate limits can hit mid-swarm), provider seats are metered API.

### 1.5 Provenance in the artifacts

- `summary.json` — every seat entry carries `kind: "native" | "provider"`.
- Seat report `seat-N-native-<model>.md` — rows `| Kind | native |` and
  `| Quota/Cost | antigravity subscription quota (rate limits apply) |` (provider seats say `metered API`).
- `reconciliation.md` — the header adds `Native Seats: k / m`, and the matrix renders
  `Seat 1 (native/gemini-3.8-flash-high)`.
- `state-fanout.json` — a native seat records provider `native` and its inferred family, so family rotation
  treats it like any other family.
- Native stdout/stderr pass through the same redactor as provider seats; provider key values are absent from
  the child environment to begin with.

### 1.6 Registry-less all-native runs

An all-native `--seats` run works on a machine with no `~/.agy-pro/providers.json`: the missing registry is
tolerated when every seat is native. Any provider seat, or any other registry error, keeps the hard failure.

### 1.7 Partition mode: one provider seat per work unit

`agy pro fanout --units <planfile>` runs the OTHER swarm shape (full reference: [`fanout.md`](fanout.md) §8):
instead of one prompt across N cross-verifying seats, the plan file decomposes the work into disjoint units
and the engine runs **one PROVIDER seat per unit**, concurrently, then merges the per-unit work products
into a single `merged.md` deliverable.

- **Provider seats only.** A native partition seat is refused with a clear error: the read-only slice below
  cannot confine a subprocess seat. (`--max-native` and the native-seat semaphore do not apply.)
- **Read-only by default, per-unit write opt-in, scope-bounded.** Partition
  runs refuse `--tools write/exec` (no CLI-wide write switch); a plan unit
  opts into write with `"write": true`, and that unit's PROVIDER seat gains
  `write_file`/`replace_in_file` confined by the same lexical AND
  symlink-resolved scope rule as reads (native seats stay refused, also for
  write units; `run_command` stays OUT). Each seat's path tools are bounded
  to its unit's scope (fail closed). Root-wide unit scopes and provable
  scope overlaps are rejected at plan parse; the merge step re-checks
  disjointness at runtime, and written paths ride the same claim walk, so
  two write units claiming one path fail the run with a named conflict.
- **Optional network tools.** A partition run may also enable
  `read_url_content` (repeatable `--allow-host`, empty list refuses) and
  `search_web` (`--search-endpoint` plus `--search-key-env`, key by
  env-var NAME only); both default OFF and reach every provider partition
  seat under the rules of [`fanout.md`](fanout.md) §5.3.
- **Uncapped concurrency, scaled deadline.** All unit seats run concurrently with no provider cap (a cap
  would serialize independent units; the swarm deadline is the bound). With no explicit `--timeout` the
  default budget is 300s + 120s per unit (cap 3600s); an explicit `--timeout` wins exactly.
- **Fail-on-conflict.** Any file claimed by two units, any missing unit deliverable, or a merge-verification
  failure fails the run; `merged.md`, the `partition` manifest block in `summary.json`, and the conflict
  report are still written for inspection.
- A plan unit MAY name its own seat (`provider/model` or `model` in the unit's `seat` field); the numbered
  `--seats` grammar is unchanged. Partition mode is mutually exclusive with `--prompt`/`--prompt-file`/
  `--seats`/`--models`.

---

## 2. Numbered seat selection: `agy pro swarm`

```bash
agy pro swarm
```

Seat assignment is **numbered selection** in the happy path — you never write a `--seats` spec unless you want
to. The builder walks four steps:

```
=== agy pro swarm: template ===
[1] Code Review       task=review    tools=read,search
[2] Security Audit    task=security  tools=read,search
[3] Custom            task=custom    tools=read,search

=== agy pro swarm: seats ===
Presets (numbered defaults):
  p1  2 native + 2 external
  p2  3 external (family-rotated)
  p3  4 mixed (flash + pro native + 2 external)
Numbered inventory:
[ 1] gemini-3.8-flash-high  Gemini 3.8 Flash (High)   [native]
[19] gemini-2.5-flash       tools, vision             [google]
[23] glm-5.3-flash-flex     tools, thinking(default)  [nw]

=== tools & budget ===   (1. task / 2. tools / 3. timeout / 4. prompt)

=== preview ===
  agy pro fanout --task review --seats native:gemini-3.8-flash-high,... --root . --timeout 300 ...
Enter runs it.  c = copy/print only.  1-4 = edit that field.  q = quit.
```

- **Selection grammar**: `1,3,5`, ranges `2-4`, delimiters `,` / space / `;`, presets `p1`..`p3`
  (case-insensitive), and the NAME of a saved seat preset (§2.2). Empty input accepts the presented
  default preset (`p1`). The list is capped at 40 seats.
- **Presets resolve against the live combined inventory** (native catalog + your registry): `p1` = first two
  available native entries + two family-rotated external entries; `p2` = three external entries with distinct
  families; `p3` = one flash-tier native + one `-pro` native + two family-rotated externals. If the inventory
  is too small, a preset degrades to "all available" with a warning instead of failing.
- **Model exclusion list**: an entry on the list (`providers.json` `defaults.excludeModels`, §2.3) is marked
  `excluded (exclusion list)` in the numbered list. Presets never auto-pick one, and naming one by number,
  range, saved preset name or seat spec is HONORED with the shared warning line. Nothing is rejected because
  of the list.
- **Preview** prints the exact `agy pro fanout ...` command that will run. `--dry-run` prints it and exits 0.
- **Console summary** after the run: `seat | kind | model | verdict` (verdict gets ` (degraded)` when a seat
  degraded). ANSI styling only on a terminal; piped output is plain text.

### 2.1 Non-interactive (power-user / CI) fallback

The interactive builder runs ONLY when stdin is a TTY **and none of the non-interactive flags are present**.
Any one of `--template`, `--seats`, `--root`, `--prompt`, `--prompt-file`, `--timeout`, `--dry-run` switches
to the zero-prompt path even on a TTY; missing required input exits 2 with usage and never prompts.

```bash
agy pro swarm --template code-review --seats "1,3,5" --root .            # numbered, CI-safe
agy pro swarm --template security-audit --seats "native:gemini-3.8-flash-high,nw/glm-5.3-flash" --dry-run
agy pro swarm --template code-review --seats "p3" --prompt-file - --timeout 300
```

`--seats` accepts the numbered grammar (`1,3,5`, `2-4`, `p1`..`p3`), the name of a saved seat preset, or
explicit seat specs (`native:id,provider/model`). A saved name expands to the numbered selection it stores. A
token that names no saved preset keeps the pre-F04 seat-spec reading (a bare model id is a legal spec), so a
lone unknown name is still a spec. In a mixed line (a number, a range, a preset id, or a known saved name
alongside it), an unknown name-shaped token is refused with `unknown saved preset`. Typed at the interactive
seat step, an unknown name-shaped token is refused
with `unknown saved preset`: the builder prints that refusal and prompts for seats again. Only a valid selection
ends the seat step; exit 2 happens only when stdin closes at the prompt.
`--prompt-file -` reads the prompt from stdin (the only non-interactive read of stdin); an empty prompt falls
back to the template scaffold.

Templates: `code-review` (`--task review`, `read,search`), `security-audit` (`--task security`, `read,search`
plus adversarial framing), `custom`. Default timeout 300s (the fanout CLI default).

### 2.2 Presets: builder presets, saved seat selections, and the fanout default

Three different "preset" ideas meet in this command; they are not the same thing:

- **The builder's `p1`..`p3` are seat-selection shortcuts**, resolved against the
  live combined inventory at build time. They are stored nowhere and they are not
  fanout presets.
- **Saved swarm presets are user-saved numbered seat selections**, managed in
  Hub > Settings. They are selectable BY NAME in the selection grammar: `--seats
  deep` (or the name typed at the seat step) resolves the numbers that preset
  stores. At the seat step an unknown name-shaped token is refused with `unknown
  saved preset` and the builder re-prompts; only a valid selection ends the step,
  and exit 2 happens only if stdin closes at the prompt. The numbers a saved
  preset expands to count as EXPLICIT picks, so an excluded entry it names is
  honored with the warning (§2.3).
- **Fanout presets** (the run configurations of [`fanout.md`](fanout.md) §2.4)
  are named budget sets in `~/.agy-pro/fanout-presets.json`: seat source, tools,
  timeout, output directory. Not a prompt, not a seat selection of its own. The
  builder never names one: it hands fanout exactly its seven explicit flags
  (`--task`, `--seats`, `--root`, `--timeout`, `--tools`, `--prompt`, `--json`;
  `--prompt-file` replaces `--prompt` on the file path), so under a swarm run
  the fanout ladder supplies only the knobs no flag covers. A store DEFAULT
  preset whose `models` / `seats` / write-granting `tools` pins conflict with
  the builder's explicit `--seats` (or the partition read-only gate) is dropped
  with one stderr warning per knob and the run proceeds; an explicitly named
  `--preset` keeps the hard error ([`fanout.md`](fanout.md) §2.4.2, finding F1).

### 2.3 The model exclusion list

The old k3 rule is now the BUILT-IN DEFAULT ENTRY of one user-owned list. The list
is `defaults.excludeModels` in `providers.json` (the sibling of the still-reserved
`excludeFamilies`), and an entry names one of four things: a provider (every model
of it), a model id (that model under any provider), a `provider/model` pair (both
sides must match; the entry splits at the FIRST `/`), or `family:<name>` (a family
match, case-insensitive, plus the boundary-safe id-token match). Provider names and
model ids match exactly and case-sensitively. Entries are trimmed at load, an entry
carrying interior whitespace, a control rune (Cc) or an invisible format rune (Cf)
is refused, an individual entry that is empty after the trim is dropped, and a list
whose every entry is blank is refused as a whole. An ABSENT key, or an explicit
null (`"excludeModels": null`), leaves the built-in default entry in force
(`family:k3`: family `k3`, or an id carrying the exact token `k3` such as
`kimi-k3` / `k3-agent-swarm`; `k3000` / `glm-k3x` stay eligible). An explicit EMPTY
list excludes nothing.

The list governs AUTO-seating and failover only:

- presets `p1`..`p3` and the fanout family rotation never pick an excluded entry,
  and a preset that loses rows to the list degrades to "all available" with a
  warning instead of failing;
- an explicit number, range, saved preset name or seat spec that names an excluded
  entry is HONORED and prints the shared warning in its swarm seat form
  `warning: seat <n> (<spec>) is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)`;
  fanout's own routes print the same tail with a bare target, `warning: <provider/model> is excluded (exclusion list); ...`;
- nothing an explicit pick names is rejected because of the list; a run whose every auto-seat candidate is excluded still fails with the all-excluded error.

Excluded rows render the marker `excluded (exclusion list)` in the numbered builder
list and in `agy pro models --all`, and carry `excluded` on the `models --json`
entry rows. Edit the list in the setup hub (`agy pro setup` > Settings > "Model
Exclusion List", row `swarm.exclude_models`): the write goes through the
before/after approval modal like every other `providers.json` write. The legacy
`agy pro settings` panel and `agy pro settings show` render the row read-only and
name the hub route in its note.

---

## 3. One combined model list

```bash
agy pro models --all     # numbered combined inventory: native catalog + registered providers
agy pro models --json    # the same entries as JSON
agy pro models           # unchanged legacy output (providers, models, capabilities)
```

```
===========================================================
  agy-pro Combined Model Inventory
===========================================================
[ 1] gemini-3.8-flash-high        Gemini 3.8 Flash (High)          [native]
...
[19] gemini-2.5-flash             tools, vision                    [google]
[23] glm-5.3-flash-flex           tools, thinking(default)         [nw]      (key missing)
```

- Group tags: `[native]` first (the `agy models` catalog, 18 models on `agy` 1.3.3), then your providers in
  `providers.json` order. Provider rows show their capability summary; native rows show the display name.
- `(key missing)` marks a provider whose API-key environment variable is not set.
- The native catalog is fetched by running real `agy models` with a scrubbed environment (a nested gateway
  shell would otherwise return the gateway list), cached in memory for 5s, with a 15s per-fetch deadline.
  If the fetch fails, the native group renders as `[native] unavailable: <err>` and the provider rows survive.
- Numbers are assigned once per load and are THE selection language across `models --all`, the launcher
  picker, and the swarm builder.
- A provider model whose id collides with a native id appears in BOTH groups on purpose: they are different
  seats (subscription quota vs metered API).
- `models --json` dumps the entry rows only and does NOT carry the native-fetch error. When `agy models`
  fails, the JSON output simply omits the native group with no marker; run `agy pro models --all` (which
  prints `[native] unavailable: <err>`) to see the degrade honestly.

### 3.1 Launcher picker

Bare `agy` on a terminal with at least one registered model shows the 2-second launcher when
`AGY_LAUNCHER=1` is set (the default bare launch goes straight to native). Choosing `[2]`
("External Models") now opens the combined picker — the same numbered list:

- a **native** number execs real `agy` natively with `--model <id>`, no bridge, gateway variables scrubbed;
- an **external** number starts the bridge and runs `agy` in gateway mode for that model;
- Enter/timeout/EOF cancels the picker and keeps the legacy `[2]` bridge behavior.

Routing follows the entry's provenance (native vs provider), never id-string matching, so a registered
external model that shares a native model id cannot hijack a native pick.

---

## 4. Settings: `agy pro settings` and `/setting_agypro`

### 4.1 `agy pro settings` — the full-screen panel (run it in your terminal)

```bash
agy pro settings                 # full-screen, keybinding-driven TUI
agy pro settings --panel p4      # deep-link one panel
```

Panels: `Launch & Profile`, `Models & Providers`, `Bridge Tools & Budgets`, `Swarm & Fanout`,
`Diagnostics & Keys`, `Install & Update`, `Environment Variables`.

Keys: `↑/↓` move, `PgUp`/`PgDn` (or `Shift+↑/↓`) page, `←/→` switch section, `Tab` confirm/switch,
`Enter`/digits select, `y`/`n` confirm destructive operations, `Esc` back, `Ctrl+L` redraw, `Ctrl+C` exit.
Choices (profile values, bridge tools, seat presets, backups) are numbered lists — the same selection
language as everywhere else. Bridge-tool budgets are compile-time constants and are displayed read-only.

Outside a Windows console (`show`, pipes, CI) the panel degrades to a plain snapshot plus a line-mode
fallback over the same settings core; ANSI is emitted only on a console.

**The panel is deprecated.** It keeps working exactly as today (same panels, same keys, same write
behaviour), but new setting work goes to the setup hub (`agy pro setup`, or `agy pro setup --wizard` for the
classic line wizard), and the panel prints the same notice plus the hub command under its banner on the first
frame. One row makes the split concrete: the model exclusion list (§2.3, row `swarm.exclude_models`) is
editable ONLY in the hub, where its write goes through the before/after approval modal; the panel and
`agy pro settings show` render that row read-only and put the hub route in its note.

### 4.2 Non-interactive verbs (what the plugin and CI call)

```bash
agy pro settings show [--json]
agy pro settings set <key> <value>            # default-profile | bridge-tools | native-seat-concurrency
agy pro settings providers list | add-json <file|-> | add-interactive | test <name> |
                             rename <old> <new> | delete <name>
agy pro settings mode switch <full|clean> | restore | list | status
agy pro settings doctor diagnose [--strict] | fix-keys [targets] | rollback-keys <dir>
agy pro settings update-info | uninstall-info | version-info
```

Every verb is idempotent and non-interactive: exit code plus a one-line result. `providers add-json` takes an
environment-variable NAME (`apiKeyEnv`) only — a key VALUE is rejected. `providers delete` removes the
registry entry only; key values in the environment are untouched. Key material is always displayed masked
(§5).

### 4.3 `/setting_agypro` — a PLUGIN slash command (not a core command)

`/setting_agypro` ships as the `agypro_settings` plugin (the same class as `/agy_help`):

```bash
agy plugin install plugins/agypro_settings
```

- The slash name comes from the FILE name: `commands/setting_agypro.md` → `/setting_agypro`.
- It is an **agent-mediated workflow**: it drives the non-interactive `agy pro settings ...` verbs above
  through `run_command`, reports each result, and re-shows a numbered menu until you exit.
- It **does not contain, embed, or launch the `agy pro settings` TUI** — custom slash commands expand to
  agent turns and cannot render a full-screen panel. For the panel, run `agy pro settings` in your own
  terminal. The command body says exactly this in its loop footer.
- A newly added plugin directory is discovered on the next session start, so restart `agy` after installing.
  The underlying `agy pro settings ...` verbs work regardless.

---

## 5. API-key masking

Every place that displays a key — wizard echo, wizard confirmations, provider listings, settings panels and
`show --json`, doctor / `--fix-keys` / `--rollback-keys` output — renders the **first 5 characters followed by
`****`** (`sk-pr****`). A value of 5 characters or fewer renders as `****`; no trailing suffix is ever shown.

While typing a key in the setup wizard, the first 5 characters echo normally and every character after that
echoes as `*` on a Windows console, where the wizard takes echo over. Other platforms have no raw-console
implementation: the tty line discipline owns echo there, so the terminal shows what it shows and the wizard
falls back to an ordinary line read (it cannot mask the echo, and this document does not claim otherwise).
Every other surface stays value-free: the wizard transcript never writes the raw value, confirmations use
`MaskSecret`, error messages and logs never embed it, and free-text leakage is handled by
the redactor (`[REDACTED]`).

---

## 6. Community banner

The interactive launcher prints one community line at startup:

```
Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI
```

It is rendered exactly once per interactive launcher invocation, right after the `===` header and before the
`[1]/[2]/[3]` options. It cannot appear in print mode (`-p`/`--print`/`--prompt`), in piped/non-TTY runs, in
any `agy pro ...` subcommand, or in machine output (`summary.json`, `models --json`), because the launcher
only runs for a bare `agy` with `AGY_LAUNCHER=1` on a TTY with at least one registered model.

---

## 7. Reconciliation and arbitration

Reconciliation is unchanged (consensus = 2 or more seats; disputed = a single Blocker/High seat). What
changed is who rules on disputes and how the ruling is recorded:

- The **session main agent** arbitrates: it reads the two seat reports, rules on quoted evidence
  (`file:line` / reproduction), and records the ruling on the item as
  `:: arbitration: <ruling> - <one-line rationale> (ruled-by: main-agent|user)`.
- Items the main agent cannot settle (missing evidence, product judgment) are recorded as
  `arbitration: escalated to user - <reason>`. Nothing is dropped silently.
- Seat identity stays visible in the record, but it is not a tiebreaker: rhetoric is not evidence.

---

## 8. What is NOT available

- **One `/model` popover that MERGES native + external models: still not shipped.** The gateway is an
  all-or-nothing switch — the binary replaces the whole catalog when `AGY_LLM_GATEWAY_MODELS` is set
  (`agy-pro/docs/bridge-architecture.md` §1). Native coexistence profile P1 (`agy pro hybrid on p1`) does
  put externals into agy's own `/model` catalog on agy's own tool engine, but the P1-lite mesh serves an
  **external-only** catalog and P1 cannot spawn subagents (profile P2 trades the in-session `/model`
  listing for the subagent family). The merged native+external picker is the fuller vision, verified only
  at the G8 TUI gate (시각 미검증). The combined native + external list still lives at the `agy-pro`
  layer: `agy pro models --all`, the launcher picker, and `agy pro swarm`
  (`agy-pro/docs/bridge-architecture.md` §6).
- **Gateway mode with the `google` (AI Studio) preset is the closest in-session equivalent**: `/model` then
  lists public Gemini 2.5 models next to your external models. The native Antigravity preview model
  (`gemini-3.8-flash-high`) is not published on AI Studio and cannot be proxied. See
  `agy-pro/docs/bridge-architecture.md` §2.3.
- **A P2 subagent tier pick runs on the external session model.** In a P2 external session, an explicit
  subagent tier pick (e.g. `model="flash"`) runs on the external session model (agy's own resolution; the
  mesh never substitutes; a native static id is served only if that exact id is registered, else a clear
  404 naming the model). Picking a tier is not a way to land on a cheaper native model in that session.
- **The pure-native path intentionally keeps ambient keys.** A plain `agy` with no hybrid profile and no
  bridge execs the child with the ambient environment (an ambient AI-Studio key included), which is what
  unshimmed native agy has always done. Every coexistence path (P1, P2, both native fallbacks, the
  picker, `agy models`, the fanout native seats) scrubs the child instead; the pure-native passthrough
  deliberately does not.
- **P2's `context_window` is cosmetic for routing.** The mesh routes by the bare slug derived from the
  customModels key, and the external provider governs the real context window. The P1 catalog's
  `max_tokens` floor is a run-hard-fail guard for P1's listed models; a P2 run with a zero/small registry `contextLength` is unprobed (G5 did not exercise it), so a near-zero value is best avoided.
- **Disputes are ruled by the session main agent, not by an extra model call.** There is no automatic
  arbitration pass in `agy pro fanout`; the workflow in §7 is the shipped mechanism.
