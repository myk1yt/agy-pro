---
name: swarm
description: Run the agy-pro multi-model swarm (native + external provider seats) from inside the agy chat. Numbered seat/template selection, a cost-confirmed dry-run echo of the exact command, then the real run whose artifacts (summary.json, reconciliation.md, seat-*.md) are presented in chat. Use when the user wants a multi-model review/security/research swarm without leaving the session. ALSO auto-load this skill (no slash command needed) whenever the user asks for code review, a security audit, deep research, or comparative analysis of code - the standing hybrid-swarm rule in the workspace AGENTS.md routes those prompts here.
---

# swarm (skill mirror of the plugin command)

This skill exists so the swarm workflow is discoverable from the model side and from `/skills`
listing. The authoritative workflow body is `commands/swarm.md` in this plugin (registered as the
plugin slash command, invoked as `/agypro_swarm:swarm`).

## What to do when invoked

Follow `commands/swarm.md` exactly. In short: collect template/seats/tools/timeout/task through
numbered menus (or from the user's message), run `agy pro models --all` for the seat catalog,
echo the resolved `agy pro swarm --dry-run` command with a cost note and an explicit go/no-go,
and only then run the real swarm and present its artifacts.

SIMPLE MODE (the default for auto-routed runs): when the standing AGENTS.md hybrid-swarm rule
fired on an ordinary prompt (no slash command, no named seats): (1) trigger check - single-file
questions and quick lookups are answered directly, no swarm; (2) map the request to the task
(security audit -> security, code review -> review, deep research -> research, plan audit ->
plan-audit); (3) pick 2-4 seats YOURSELF from `agy pro models --all` - native-first, different
families preferred, never k3 - stating the choice in one line, falling back to the default
preset (native:gemini-3.8-flash-high,native:claude-opus-5-5-high,native:claude-sonnet-5-5-high)
when unsure; (4) run `agy pro fanout --task <t> --seats "<pick>" --root <target> --prompt-file
<file> --tools read,search --timeout 900 --max-native <n>` in background mode when available;
(5) relay progress live from `reports/fanout/<ts>/progress.log` (seat start/finish lines) so the
run is never silent; (6) present the reconciliation matrix and seat findings VERBATIM with
artifact paths. The menu protocol applies only when the user explicitly asks for
external/provider seats or names specific seats.

## Honesty rules (binding)

- This is a **plugin skill/slash command**, the same class as `/agy_help`. It is NOT a core `agy`
  binary command, and it is NOT a mixed `/model` list: the agy TUI's `/model` popover stays XOR
  (native OR bridge-external, never both). The swarm runs as separate subprocesses chosen here in
  chat.
- Every seat is a full agent turn. Native seats spend the Antigravity subscription; provider seats
  spend that provider's API credits. No real run without an explicit go after the dry-run echo.
- API key values are never requested or echoed in chat. Entry happens only in the interactive
  wizard (`agy pro setup`), masked as first-5 characters + `****`.
- The task prompt goes through `--prompt-file` (a temp file you write), never spliced raw into a
  shell line.
