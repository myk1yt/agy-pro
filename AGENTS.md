# Antigravity-cli workspace rules (agy loads this file automatically)

This file applies when the session runs inside the agy TUI (Antigravity CLI). Other agents
(e.g. Kimi Code) have their own swarm mechanics and ignore the agy-pro section below.

## Hybrid swarm auto-route (zero-friction, no slash command)

When the user's prompt asks for multi-perspective verification — code review, security audit,
deep research, comparative analysis, or a plan audit — route it through the agy-pro hybrid
swarm AUTOMATICALLY. Do not wait for a slash command and do not ask whether to swarm: ordinary
prompts trigger the swarm by standing instruction.

Standing workflow (SIMPLE MODE - the default; the full runbook is the `swarm` skill shipped by
the agypro_swarm plugin, used as the escalation path only):

1. **Trigger check**: swarm only when the request is multi-file/whole-project verification or
   explicitly asks for multi-perspective review. Single-file questions and quick lookups are
   answered directly (no swarm).
2. Map the request to a fanout task: security audit -> `security`, code review -> `review`,
   deep research -> `research`, plan audit -> `plan-audit`.
3. **Pick the seats YOURSELF** from `agy pro models --all`: 2-4 seats, native-first (Antigravity
   subscription), prefer DIFFERENT model families for cross-verification. Read the user's exclusion
   list instead of assuming a fixed family ban: a row marked `excluded (exclusion list)` is out of
   the auto-seat pool, every other row is fair game. The list is user-owned
   (`providers.json defaults.excludeModels`, edited in the setup hub at `agy pro setup`); when the
   key is absent a built-in k3 default entry applies, so read the marker, not the model name. State
   the choice in ONE line. When the request is ambiguous, fall back to this documented native
   line-up (a rule-level fallback, not an app default preset):
   `native:gemini-3.8-flash-high` + `native:claude-opus-5-5-high` + `native:claude-sonnet-5-5-high`.
4. Run it (background mode when your shell tool supports it; see the swarm skill for the progress
   protocol):
   `agy pro fanout --task <t> --seats "<your pick>" --root <target> --prompt-file <file> --tools read,search --timeout 900 --max-native <seat count>`
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

Opt-outs (answer directly, no swarm): the user says "just answer", "no swarm", or the task is a
trivial single-file question. Never run external-seat paid runs without the seat/cost echo + go.

The session's `/model` picker stays XOR (native OR bridge-external, never both — binary
constraint). The swarm is the mechanism that runs both kinds in one operation; this rule does
not change the picker.

## Governance

The full VP-of-Engineering charter lives in `rules/AGENTS.md` (the installer deploys it to
`~/.gemini/config/rules/` via `scripts/lib/configure-rules.js`) and the global orchestration
charter in `~/.gemini/AGENTS.md`. Direct ad-hoc edits under `~/.gemini/**` are prohibited;
governance changes go through the repo sources plus the sanctioned deploy scripts.
