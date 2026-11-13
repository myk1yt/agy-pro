# Antigravity-cli workspace rules (agy loads this file automatically)

This file applies when the session runs inside the `agy` TUI (Antigravity CLI).

## Native Swarm & Subagent Auto-Route (zero-friction, no slash command)

When the user's prompt asks for multi-perspective verification — code review, security audit,
deep research, comparative analysis, or a plan audit — or asks for multi-domain code changes,
route it through native `antigravity-cli` `/swarm` and `invoke_subagent` orchestration
AUTOMATICALLY. Do not wait for a slash command and do not ask whether to swarm: ordinary prompts
trigger multi-perspective orchestration by standing instruction.

> **Deprecation Notice**: Legacy Go `agy pro` commands (`agy pro fanout`, `agy pro code`,
> `agy pro models`, `agy pro mode`, `agy pro setup`) are **deprecated** and superseded by native
> `antigravity-cli` (`/swarm`, subagents, Auto mode, hooks, plugins) and the simplified Node.js
> `agy-pro` CLI (`agy-pro setup`, `agy-pro gateway on [<profile>]|off|status`). Never invoke
> legacy `agy pro ...` commands from workspace workflows.

Two disjoint routes hang off this standing instruction, and the prompt's own words pick between
them:

- **Verification route (read-only `/swarm` or concurrent `invoke_subagent`)** — whole-project or
  multi-file code review, security audit, deep research, comparative analysis, or plan audit.
- **Implementation route (VP 7-Stage Subagent Lifecycle)** — the prompt asks for code to be
  written or changed: 구현해줘 / 만들어줘 / 고쳐줘 / 버그 고쳐줘 / 리팩터 / 설계해줘 / 기획해줘,
  plus English forms ("implement X", "add Y", "fix Z", "refactor W", "design V").

### Verification route: read-only `/swarm` & parallel `invoke_subagent`

1. **Trigger check**: Swarm only when the request is multi-file/whole-project verification or
   explicitly asks for multi-perspective review. Single-file questions and quick lookups are
   answered directly (no swarm).
2. **Dispatch**: Use native `/swarm` or a concurrent `invoke_subagent` batch of read-only
   specialists (`architect`, `code-reviewer`, `security-reviewer`, `research` from
   `agents/<name>/AGENT.md`) with isolated report targets (`reports/<agent>-report*.md`).
   For broad, non-analytic codebase symbol scanning or online fact reconnaissance, batch-dispatch
   3–8 orthogonal boundaries with `Model: "flash_lite"`.
3. **Present findings VERBATIM**: Surface the reconciled matrix (severity, file/line citations,
   confirming seats) plus key recommendations and artifact paths without paraphrasing away
   technical evidence.

Opt-outs (answer directly, no swarm): the user says "just answer", "no swarm", or the task is a
trivial single-file question.

### Implementation route: VP 7-Stage Orchestration

1. **Design / Plan requests** (`설계해줘`, `기획해줘`, `write the design for V`): Run read-only
   research + `architect` (`agents/architect/AGENT.md`) and unprimed plan review, then present the
   3 options and recommendation verbatim (`reports/architect-plan.md`).
2. **Implementation / Bugfix requests**: Follow `rules/AGENTS.md` — decompose into orthogonal
   domains (Stage 1), dispatch parallel research/audit (Stages 3–4), define disjoint SRP file
   boundaries (Stage 5), delegate edits strictly to isolated Domain Workers (Stage 6), and
   verify via single-instance `blind-qa` + read-only `code-reviewer` / `security-reviewer`
   (Stage 7).

### `/model` Picker & Simplified `agy-pro` Gateway CLI

The session's `/model` picker stays XOR (native Antigravity catalog OR `AGY_LLM_GATEWAY_MODELS`
"Enterprise Gateway Models" — never both). Use `agy-pro setup`, `agy-pro gateway on [<profile>]`,
`agy-pro gateway off`, `agy-pro gateway status`, and `agy-pro doctor` solely for managing
`AGY_LLM_GATEWAY_*` environment configuration.

## Governance

The full VP-of-Engineering charter lives in `rules/AGENTS.md` (the installer deploys it to
`~/.gemini/config/rules/` via `scripts/lib/configure-customizations.js`) and the global
orchestration charter in `~/.gemini/AGENTS.md`. Direct ad-hoc edits under `~/.gemini/**` are
prohibited; governance changes go through the repo sources plus the sanctioned deploy scripts.
