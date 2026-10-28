# Antigravity-cli workspace rules (agy loads this file automatically)

This file applies when the session runs inside the agy TUI (Antigravity CLI). Other agents
(e.g. Kimi Code) have their own swarm mechanics and ignore the agy-pro section below.

## Hybrid swarm auto-route (zero-friction, no slash command)

When the user's prompt asks for multi-perspective verification — code review, security audit,
deep research, comparative analysis, or a plan audit — route it through the agy-pro hybrid
swarm AUTOMATICALLY. Do not wait for a slash command and do not ask whether to swarm: ordinary
prompts trigger the swarm by standing instruction.

Two disjoint routes hang off that one instruction, and the prompt's own words pick between them: a
prompt that only asks for findings never reaches the write door, and a prompt that asks for code
never burns seats on a read-only audit.

- **Verification route (read-only fanout)** - review, security audit, deep research, comparative
  analysis, plan audit. The standing workflow below, read-only (`--tools read,search`) by pin.
- **Implementation route (the `agy pro code` door)** - the prompt asks for code to be written or
  changed: 구현해줘 / 만들어줘 / 고쳐줘 / 버그 고쳐줘 / 리팩터 / 설계해줘 / 기획해줘, plus the English
  forms ("implement X", "add Y", "fix Z", "refactor W", "design V"). The "Implementation route"
  section below.

### Verification route: read-only fanout

Standing workflow (SIMPLE MODE - the default; the full runbook is the `swarm` skill shipped by
the agypro_swarm plugin, used as the escalation path only):

1. **Trigger check**: swarm only when the request is multi-file/whole-project verification or
   explicitly asks for multi-perspective review. Single-file questions and quick lookups are
   answered directly (no swarm).
2. Map the request to a fanout task: security audit -> `security`, code review -> `review`,
   deep research -> `research`, plan audit -> `plan-audit`.
3. **A pinned lineup outranks auto-seating.** Before picking anything, check the default fanout
   preset (`default_preset` in `~/.agy-pro/fanout-presets.json`, edited in the setup hub under
   `[2] Fanout Presets`). When no default key is set, the saved row [1] `(built-in defaults)`
   record is the same standing fallback; press Enter on that row to edit it directly. When either
   pins a seat lineup, that lineup IS the run's lineup: step 4 passes
   NO `--seats` at all, and the fanout run itself echoes one line naming the preset, the selection
   rung and the metered-seat count. Standing cost approval belongs to that STORE DEFAULT rung only:
   a provider seat inside the saved default lineup needs no go/no-go question. Seats the user named
   in chat (a `--seats` pick, a seat picked in the swarm builder) outrank the preset everywhere, and
   a preset named for one run only (`--preset`, `AGY_PRO_FANOUT_PRESET`) is ad-hoc: it resolves its
   lineup but keeps the full seat/cost echo plus the GO gate, exactly like an ad-hoc external pick.
   A hand edit of `fanout-presets.json` is not standing approval either (the run prints one advisory
   notice when that pin carries no `seats_sha256` marker, i.e. it was hand-authored or predates the
   marker, or when the marker no longer matches the stored seats - advisory only, never blocking, and
   printed only when the preset's lineup is the lineup the run actually adopts: an explicit `--seats`
   or a `--units` run is silent).
   With no pinned lineup, pick
   the seats YOURSELF from `agy pro models --all`: 2-4 seats, native-first
   (Antigravity subscription), prefer DIFFERENT model families for cross-verification. Read the
   user's exclusion list instead of assuming a fixed family ban: a row marked
   `excluded (exclusion list)` is out of the auto-seat pool, every other row is fair game. The list
   is user-owned (`providers.json defaults.excludeModels`, edited in the setup hub at
   `agy pro setup`); when the key is absent a built-in k3 default entry applies, so read the marker,
   not the model name. State the choice in ONE line. When the request is ambiguous, fall back to
   this documented native line-up (a rule-level fallback, not an app default preset):
   `native:gemini-3.8-flash-high` + `native:claude-opus-5-5-high` + `native:claude-sonnet-5-5-high`.
4. Run it (background mode when your shell tool supports it; see the swarm skill for the progress
   protocol):
   `agy pro fanout --task <t> --seats "<your pick>" --root <target> --prompt-file <file> --tools read,search --timeout 900 --max-native <seat count>`
   When the default preset pins the lineup, drop the `--seats` pair entirely and let the preset
   supply it:
   `agy pro fanout --task <t> --root <target> --prompt-file <file> --tools read,search --timeout 900 --max-native <seat count>`
   Write the user's prompt to a temp file and pass `--prompt-file` (shell-safety); `--root` is the
   current workspace or the path the user named.
5. **While it runs, relay progress**: the engine appends seat start/finish lines to
   `reports/fanout/<ts>/progress.log` in the run's artifact dir. Surface each line to the user as
   it appears so the run is never silent.
6. **Present the findings VERBATIM** from the reconciliation matrix (ID, severity, title, reporting
   seats) plus each seat's key locations and recommendations - do not paraphrase or compress the
   technical content - and give the artifact paths (`reports/fanout/<ts>/`: summary.json,
   reconciliation.md, seat-*.md).

Escalate to the full menu protocol (inventory, seat numbers, dry-run echo, per-seat cost gate)
ONLY when the user explicitly asks for external/provider models or names specific seats.
Auto-seating and failover skip every row the exclusion list covers; an explicit pick (a number, a
range, or a seat spec) is honored with a warning. Nothing is rejected because of that list.
Drift differs by source: a seat missing from the DEFAULT preset's lineup is skipped with one warning
and the run continues marked degraded (`skipped_seats` in `summary.json`), while the same model in an
explicit `--seats` list is a hard error, and so is a missing seat in a preset named for one run
(`--preset` / `AGY_PRO_FANOUT_PRESET`): only the store default rung tolerates the skip. Native seats
are never registry-checked at all: an unknown `native:<id>` is not skipped, it starts and fails inside
its own subprocess and the run ends marked degraded. Relay a skipped seat to the user, and never
report a degraded run as a full quorum.

Opt-outs (answer directly, no swarm): the user says "just answer", "no swarm", or the task is a
trivial single-file question. Never run external-seat paid runs without the seat/cost echo + go.

### Implementation route: the `agy pro code` door

Same standing instruction, second route: when the prompt asks for code to be written or changed,
the master does not write it and does not hand back a plan it authored alone. It runs the agy-pro
code door. The opt-outs above still win ("just answer", "no swarm", a trivial single-file edit is
done directly), and the `/model` picker constraint below is unchanged.

1. **Split the request.** 설계/기획/design questions ("설계해줘", "어떻게 구조화할까", "write the
   design for V") stop at the plan phase: `agy pro code plan --ask-file <tmp>`. The default plan
   seats are the two native ids `native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high`, so the
   phase is unmetered and runs without asking. Relay the reconciliation VERBATIM, exactly as step 6
   of the verification route does. Implementation and bugfix requests continue to step 2.
   A plan-only invocation has NO snapshot baseline: neither `code plan` nor `code run` without
   `--scope` leaves a manifest, and native plan seats are not sandboxed (they are real agy processes
   with their own tool surface), so a seat edit during such a phase is neither measured nor
   restorable. A standalone plan is therefore for text-only design answers. When the request could
   tempt a seat to implement in place, capture `git status` before and after the run and report any
   change yourself. Where a write follows in the same run directory (`--scope` given), the plan phase
   snapshots before the seats, so the same edit surfaces as `native-drift` and is restorable.
   Detection, never containment.
2. **Derive the scope from the plan result, never from the prompt alone.** The plan's units name the
   paths; `--scope` is what bounds the writer (repeatable, one bounded path per entry). A scope the
   master invented is a governance failure: keep each glob as narrow as the plan's file list, and
   never pass `--scope .` (the snapshot layer rejects a root-wide scope).
3. **Pick the writer seat from the live registry.** Write seats are provider seats only
   (`--writer` refuses a native seat: a native seat builds no scoped tool runner). Read the registry
   with `agy pro models --all` and prefer a DIFFERENT provider/model family per task, rotating across
   whatever provider rows that command resolves on this machine; this file names no provider. When
   zero provider seats are configured, fall back to the all-native plan/review flow: run
   `agy pro code plan` (and `agy pro code review --run <dir>` after a write run) and hand the change
   back to the CEO, because there is no writer to bind. Do not describe that flow as write-proof:
   native seats are not scope-bound (they run real agy with their own tool surface, and they can
   write on this machine), the engine detects and reports such an edit instead of preventing it, and
   a plan-only run carries no baseline at all (step 1). Say that plainly when the change goes back.
4. **Echo the cost, then wait for the go.** State the writer seat, the scope globs, the verify
   command, and that the write phase is a metered API call run with no token/cost cap. The verify
   step executes commands from the working tree: `--verify` is the operator's own argv, and without
   it the engine picks one, so the pre-run truth is the door's `--dry-run` echo, which prints each
   `verify:` line before anything executes (the final block carries the same argv with each
   attempt's verdict). Relay those lines instead of describing the step. Only after the user's go
   does the command carry `--yes`, and
   "Never run external-seat paid runs without the seat/cost echo + go" (the Opt-outs line above) is
   the standing rule for this door too. The plan and review phases are native-only and unmetered
   (the Antigravity quota covers them and rate-limits them), so they auto-run; the write phase is
   the metered one.
5. **Run it.**
   `agy pro code run --ask-file <tmp> --scope "<glob>" [--scope "<glob>"...] --writer <provider/model> --verify "<repo test command>" [--verify "<second>"...] --out reports/code/<ts>/`
   Write the user's request to a temp file and pass `--ask-file` for shell safety, exactly as the
   verification route passes `--prompt-file`. Omitting `--scope` stops the run after the plan phase
   (exit 0) and prints the derived-scope instruction: that is the documented way to read the plan
   first and then author the scopes. That stop takes no snapshot either, so never present it as a
   measured phase (step 1).
6. **Relay progress live and relay the seat rows**: the door echoes the run directory before the
   metered phase starts. Tail `reports/code/<ts>/phases.log` (the codework trail) and each phase's
   own `reports/code/<ts>/<phase>/progress.log` (seat start/finish lines), surfacing each line as it
   appears so the run is never silent. A phase can report ok while individual seats failed (rate
   limit, timeout, an empty answer): the `seats:` line in the block and each phase's summary.json
   carry the per-seat outcome, so relay that line and call such a phase degraded to the CEO. A
   fanout that reconciled on partial seats is never a clean pass.
7. **Present the result verbatim**: the compact code block the door prints (status, files, cost,
   artifacts) plus the rollback command line, uncompressed. On a `verify-failed` status (exit 1)
   offer `agy pro code rollback --run <dir>` BEFORE anything else: the snapshot is the undo button,
   it restores in place and archives (never deletes) the files the run created into
   `<dir>/rollback-archive/`. Pass that exact run directory, never a shortened or relative stand-in:
   rollback reads `manifest.json` from the directory it is given, so a path without one restores
   nothing. A failed verification also gets `--max-repairs` left at its default of one repair run.
8. **Verification still follows.** Once the write run reports ok, the verification route above applies
   to the resulting diff: the writer does not grade its own work, so a `review` fanout (read-only
   seats, reconciliation verbatim) closes the loop.

The session's `/model` picker stays XOR (native OR bridge-external, never both — binary
constraint). The swarm is the mechanism that runs both kinds in one operation; this rule does
not change the picker.

## Governance

The full VP-of-Engineering charter lives in `rules/AGENTS.md` (the installer deploys it to
`~/.gemini/config/rules/` via `scripts/lib/configure-rules.js`) and the global orchestration
charter in `~/.gemini/AGENTS.md`. Direct ad-hoc edits under `~/.gemini/**` are prohibited;
governance changes go through the repo sources plus the sanctioned deploy scripts.
