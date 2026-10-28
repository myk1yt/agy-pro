---
name: swarm
description: Run the agy-pro multi-model swarm (native + external provider seats) from inside the agy chat. Numbered seat/template selection, a cost-confirmed dry-run echo of the exact command, then the real run whose artifacts (summary.json, reconciliation.md, seat-*.md) are presented in chat. Use when the user wants a multi-model review/security/research swarm without leaving the session. ALSO auto-load this skill (no slash command needed) whenever the user asks for code review, a security audit, deep research, or comparative analysis of code - the standing hybrid-swarm rule in the workspace AGENTS.md routes those prompts here. The same rule sends implementation prompts (구현해줘 / 고쳐줘 / 버그 고쳐줘 / 설계해줘) to the `agy pro code` door; this skill carries that escalation too (see "Escalation: the `agy pro code` door").
---

# swarm (skill mirror of the plugin command)

This skill exists so the swarm workflow is discoverable from the model side and from `/skills`
listing. The authoritative workflow body is `commands/swarm.md` in this plugin (registered as the
plugin slash command, invoked as `/agypro_swarm:swarm`).

## What to do when invoked

Follow `commands/swarm.md` exactly. In short: collect template/seats/tools/timeout/task through
numbered menus (or from the user's message; a seat choice may be numbers, a range, a preset id
p1/p2/p3, or the name of a saved seat preset), run `agy pro models --all` for the seat catalog,
echo the resolved `agy pro swarm --dry-run` command with a cost note and an explicit go/no-go,
and only then run the real swarm and present its artifacts.

SIMPLE MODE (the default for auto-routed runs): when the standing AGENTS.md hybrid-swarm rule
fired on an ordinary prompt (no slash command, no named seats): (1) trigger check - single-file
questions and quick lookups are answered directly, no swarm; (2) map the request to the task
(security audit -> security, code review -> review, deep research -> research, plan audit ->
plan-audit); (3) pinned-lineup check FIRST: if the default fanout preset (`default_preset` in
`~/.agy-pro/fanout-presets.json`, edited in the setup hub under `[2] Fanout Presets`) pins a seat
lineup, that lineup IS the lineup of every run that names no seat of its own. When no default key
is set, the saved row [1] `(built-in defaults)` record is the same standing fallback; press Enter
on that row to edit it directly. With either pin, pass NO `--seats` in
(4). Standing cost approval belongs to that STORE DEFAULT rung alone, so a provider seat inside the
saved default lineup asks no go/no-go; the fanout run echoes one line naming the preset, the rung,
the seat count and the metered count, and that is what you relay. Seats named in the chat (a
`--seats` pick, a builder pick) outrank the preset everywhere; a preset named for one run only
(`--preset`, `AGY_PRO_FANOUT_PRESET`) resolves its lineup but is ad-hoc, so it keeps the full
seat/cost echo + go gate, as does any ad-hoc external pick; a hand edit of `fanout-presets.json` is
not standing approval (the run prints one advisory notice when that pin carries no `seats_sha256`
marker, i.e. it was hand-authored or predates the marker, or when the marker no longer matches the
stored seats - advisory only, never blocking, and printed only when the preset's lineup is the lineup
the run actually adopts: an explicit `--seats` or a `--units` run is silent). With no
pin, pick 2-4 seats YOURSELF from `agy pro models --all` - native-first, different
families preferred, and read the user's exclusion list rather than assuming a fixed family ban: a
row marked `excluded (exclusion list)` is out of the auto-seat pool and every other row is fair
game (the list is user-owned, `providers.json defaults.excludeModels`, edited in the setup hub at
`agy pro setup`; an absent key means the built-in k3 default entry applies) - stating the choice
in one line, falling back to this documented native line-up when unsure (a rule-level fallback,
not an app default preset, and unchanged):
(native:gemini-3.8-flash-high,native:claude-opus-5-5-high,native:claude-sonnet-5-5-high);
(4) run `agy pro fanout --task <t> --seats "<pick>" --root <target> --prompt-file
<file> --tools read,search --timeout 900 --max-native <n>` in background mode when available (under
a default-preset pin and with no seats of your own to pass, drop the `--seats` pair entirely so the
preset supplies it); a STANDING (default preset) seat that no longer resolves is skipped with one
warning and the run is reported degraded (`skipped_seats` in summary.json), while the same model in
an explicit `--seats` list or in a preset named for this run only is a hard error; an unknown
`native:<id>` is never registry-checked, so it is not skipped - it starts and fails and the run is
reported degraded;
(5) relay progress live from `reports/fanout/<ts>/progress.log` (seat start/finish lines) so the
run is never silent; (6) present the reconciliation matrix and seat findings VERBATIM with
artifact paths. The menu protocol applies only when the user explicitly asks for
external/provider seats or names specific seats.

PARTITION MODE: when the request decomposes into disjoint scopes, `agy pro fanout --units
<planfile>` runs one PROVIDER seat per plan unit and merges the work products into a single
`merged.md` (see the "Partition mode" section of `commands/swarm.md` and agy-pro/docs/fanout.md
section 8). The plan file is written with your file-write tool like the prompt file; partition
runs refuse native seats and fail the run on any scope conflict. They are read-only by default:
a unit opts into write with `"write": true` (no CLI-wide write switch; `run_command` stays OUT),
and a run may pass `--allow-host` / `--search-endpoint` / `--search-key-env` to give every
provider partition seat `read_url_content` / `search_web` (both default OFF).

## Escalation: the `agy pro code` door (implementation requests)

The standing rule in the workspace AGENTS.md has two disjoint routes. Verification prompts come to
the fanout workflow above; implementation prompts go to `agy pro code`
(`plan | write | review | rollback | run`, exit codes 0 ok / 1 run failure / 2 usage or refused
consent). Escalate here when the prompt asks for code to be written or changed: 구현해줘 / 만들어줘 /
고쳐줘 / 버그 고쳐줘 / 리팩터 / 설계해줘 / 기획해줘, or "implement X" / "add Y" / "fix Z" / "refactor W".
The trigger check still owns the boundary: a trivial single-file edit, or an explicit opt-out ("just
answer", "no swarm"), is answered directly and never reaches the door.

Full flow (run from the agy-pro workspace root; the ask text always travels through `--ask-file`):

1. **Design/plan request** -> `agy pro code plan --ask-file <tmp>` and stop there. The default plan
   seats are the native line-up `native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high`, so the
   phase is unmetered and asks nothing; relay the reconciliation VERBATIM exactly as SIMPLE MODE
   step 6 says. A design request never needs a writer seat.
   A plan-only invocation has NO snapshot baseline: neither `code plan` nor `code run` without
   `--scope` leaves a manifest, and a native seat is not sandboxed (it is a real agy process with its
   own tool surface), so an edit a seat makes during such a phase is neither measured nor
   restorable. That makes the standalone plan a text-only design answer. When the request could
   tempt a seat to implement in place, capture `git status` before and after the phase and report
   any change yourself; inside a `run` that names `--scope`, the plan phase snapshots before the
   seats, so the same edit surfaces as `native-drift` and is restorable. Detection, not containment.
2. **Derive `--scope` from the plan's units**, never from the prompt alone: repeatable, one bounded
   workspace path per entry, never `.` (the snapshot layer rejects a root-wide scope). A scope the
   master invented is a governance failure. Running `agy pro code run` without `--scope` stops after
   the plan phase at exit 0 and prints the derived-scope instruction, which is the documented way to
   read the plan first and then author the scopes.
3. **Choose the writer from `agy pro models --all`**, preferring a DIFFERENT provider/model family
   per task and rotating across tasks. `--writer` accepts exactly one provider seat; a native seat is
   refused at usage time because it builds no scoped tool runner (`engine.go:883-889`). The external
   rows are whatever the registry resolves on this machine: `agy pro models --all` names them, this
   file names none. With zero provider rows configured there is
   no writer to bind: degrade to the all-native plan/review path, hand the change and the plan back
   to the user, and never call that path write-proof. Native seats are not scope-bound and hold
   their own tool surface, and a plan-only run has no baseline, so the engine detects and reports a
   seat edit instead of preventing it (step 1).
4. **Cost echo gate (mandatory, the same discipline as the menu protocol's per-seat gate).** Print the
   writer seat, the scope globs, the `--verify` command(s), the run directory, and the honest cost
   line: a write run is a metered API call with no token/cost cap. The verify step executes commands
   from the working tree (`--verify` is the operator's own argv, otherwise the engine picks one), so
   the pre-run truth is the `--dry-run` echo, which precedes the gate, prints each `verify:` line
   before anything executes plus the three headless fanout commands (plan / units write / review) and
   the rollback line, and touches nothing but `phases.log`. The final block repeats the same argv
   with each attempt's verdict. Relay those lines instead of describing the step. Wait for an
   explicit go. Then, and only then, add `--yes`: without it a non-TTY invocation is refused at exit
   2 (`ErrConsentRequired` plus a hint naming `--yes`) before any write seat starts (in a composed
   `run` the plan phase, and its snapshot when `--scope` was named, have already executed, so the
   refusal stops the metered write and not the whole run). Plan and review phases are native-only
   and unmetered (Antigravity quota, rate-limited), so they auto-run without asking; the write phase
   is the metered one.
5. **Run it**: `agy pro code run --ask-file <tmp> --scope "<glob>" [--scope "<glob>"...]
   --writer <provider/model> --verify "<repo test cmd>" [--verify "<second>"...] --out reports/code/<ts>/`
   in background mode when available. Relay progress live: `reports/code/<ts>/phases.log` (the
   codework trail) and each phase's `reports/code/<ts>/<phase>/progress.log` (seat start/finish lines),
   one line per event, the same way the fanout progress protocol above is relayed. Relay the block's
   `seats:` line and each phase's summary.json seat rows with it: a phase can report ok while
   individual seats failed (rate limit, timeout, an empty answer), so a phase that reconciled on
   partial seats goes to the user as degraded, never as a clean pass.
6. **Present the compact code block verbatim** (status, files, cost, artifacts) plus the rollback
   command line, uncompressed.

Repair and rollback semantics (not interchangeable):

- A failed verification triggers ONE repair run by default (`--max-repairs 1`) before the run reports
  `verify-failed` at exit 1; `--max-repairs 0` reports and stops without repairing. Never raise the
  count silently: every repair is another metered write turn and needs its own go.
- A `verify-failed` run is not self-healing and must not be presented as done. Offer
  `agy pro code rollback --run <dir>` FIRST: it restores the snapshot in place, archives the files the
  run created into `<dir>/rollback-archive/` (it never deletes), needs no consent, and is idempotent
  (a second call reports them `unchanged`, so repeating the offer is harmless). Pass the exact run
  directory the door echoed, never a shortened or relative stand-in: rollback reads `manifest.json`
  from the directory it is given, and a path without one restores nothing. A new attempt only makes
  sense on a clean tree, with the first failure's reason carried into the new ask.
- `agy pro code review --run <dir>` is the post-write verification of the change report. It refuses a
  run directory with no `report.txt` (exit 2), so it never reviews an empty diff, and the review seats
  are native by default (unmetered, rate-limited).

## Honesty rules (binding)

- This is a **plugin skill/slash command**, the same class as `/agy_help`. It is NOT a core `agy`
  binary command, and it is NOT a mixed `/model` list: the agy TUI's `/model` popover stays XOR
  (native OR bridge-external, never both). The swarm runs as separate subprocesses chosen here in
  chat.
- Every seat is a full agent turn. Native seats spend the Antigravity subscription; provider seats
  spend that provider's API credits. No real run without an explicit go after the dry-run echo.
- Native seats are NOT sandboxed and no doc may claim otherwise: a dispatched native seat is a real
  agy process with its own tool surface, and it can create or edit files (live-proved on this
  machine, verification-report Q1). The engine's guarantee is measurement, not containment: a plan
  phase that precedes a write snapshots first, so a seat edit surfaces as `native-drift` and rollback
  restores it, while a plan-only run has no baseline and a seat edit during it is neither measured
  nor restorable.
- API key values are never requested or echoed in chat. Entry happens only in the interactive
  wizard (`agy pro setup`), masked as first-5 characters + `****`. The model exclusion list is
  edited in that same setup hub (Swarm & Fanout); the legacy `agy pro settings` line panel is
  deprecated in favor of the hub and renders the list read-only.
- The task prompt goes through `--prompt-file` (a temp file you write), never spliced raw into a
  shell line.
