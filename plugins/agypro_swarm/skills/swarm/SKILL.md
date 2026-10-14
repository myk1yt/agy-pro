---
name: swarm
description: Run the agy-pro multi-model swarm (native + external provider seats) from inside the agy chat. Numbered seat/template selection, a cost-confirmed dry-run echo of the exact command, then the real run whose artifacts (summary.json, reconciliation.md, seat-*.md) are presented in chat. Use when the user wants a multi-model review/security/research swarm without leaving the session.
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
