# kimi-settings Inventory Report

Source: `D:\OneDrive\Projects\kimi-settings` (git repo, branch `main`, remote `github.com/myk1yt/kimi-settings`). Read-only exploration.

> [!WARNING]
> `config.toml` at the repo root holds live provider API keys in plaintext (Kimi, NeuralWatt, Charm, MiMo, Google). It is gitignored (decisions.md 2026-10-10) but exists on disk. Keys are NOT reproduced here. Never copy that file into Antigravity-cli. Treat the keys as exposed to anything that has read the file and consider rotating them.

> [!NOTE]
> Coverage gap: `PORTING_PLAN.md` (24 KB, likely the earlier Zoo/agy to Kimi porting plan) could not be read because the read permission prompt was denied. Everything else in the requested list was read. `docs/` (1816 files) was only sampled.

## 1. Directory structure and component roles

| Path | Role |
|---|---|
| `AGENTS.md` (78 lines) | Global rules, English. Korean user output, delegation policy (orchestrator-first, MICRO exception, PASSTHROUGH, intent-injection schema), judgment protocol (small/medium/large + preemptive-fix, no backlog), Definition of Done, data safety (recycle bin only, protected paths), records (decisions.md, atomic commits, no autonomous push), secrets tiers, crow-memory MCP recall/ingest, two-way sync rule, headless-verification rule (user screen off-limits). |
| `SYSTEM.md` (85 lines) | Main-agent system prompt override: "VP of Engineering Mode" charter. Ends with `${base_prompt}` so Kimi's default prompt is appended. Holds the swarm toolbox, dispatch protocol, reviewer cross-check swarm (3a), deep-research swarm variant, intent conformance (3b), decomposed dispatch (3c), multi-tier DoD (4), records (5). This is the heart of the "full-stack mode". |
| `config.toml` | Live copy of Kimi config (gitignored, contains secrets). Defines `[providers.*]`, `[models.*]`, `[services.*]`, `[secondary_model]` pool. |
| `config.fragment.toml` | Only comments. Records the swarm convention and says the live pool exists only in `~/.kimi-code/config.toml`. The installer merges it non-destructively. |
| `mcp.json` | MCP servers: `github` (url `https://api.githubcopilot.com/mcp`, `bearerTokenEnvVar=GITHUB_TOKEN`), `hf-mcp-server` (`HUGGINGFACE_TOKEN`), `crow-memory` (`http://127.0.0.1:9021/mcp`). |
| `setup-kimi.ps1` | Idempotent installer with `-DryRun`. |
| `agents/` (9 md) | Custom subagent definitions (see section 3). |
| `skills/` (11 flat `.md` + 29 `<name>/SKILL.md` folders) | Flat files are slash-style playbooks (`type: flow` manual, `type: prompt` auto-triggered). Folders are knowledge skills. |
| `references/visual-qa-rubric.md` | Reference doc for visual QA. |
| `docs/` (1816 files, 9 session folders) | Per-task working artifacts in `docs/YYMMDD_NNNN_session_<slug>/<agent>-report[-<alias>].md`. Never committed by policy (but present in repo). Includes `_repo/` clones of third-party material under autoprompt-skill-review. |
| `decisions.md` (127 lines) | Append-only executive decision log: the best single source for why each rule exists. |
| `PORTING_PLAN.md` | Not read (denied). |
| `github-account.cmd/.ps1`, `kimi-env.cmd` | Switch `GITHUB_TOKEN` between two GitHub accounts then relaunch Kimi. `kimi-env.cmd` is gitignored and machine-specific. |
| `horse.html`, `low-poly-horse.html`, `reports/designer-report.md` | Demo output from the designer agent. Not config. |

Skills (flat): `audit`, `backup`, `brainstorm`, `cleartemp`, `deepcheck`, `feature` (prompt type, 6-phase lifecycle), `goal`, `heal`, `init`, `testdrive`, `zerobug`. Descriptions are Korean.
Skills (folders): `adr-authoring-workflow`, `architecture-constraint-setup`, `causal-chain-analysis`, `ci-triage`, `code-review-taxonomy`, `codebase-onboarding`, `commit-standard`, `context-handoff`, `dependency-audit`, `dependency-hallucination-check`, `design-3d-web`, `design-academic-typeset`, `design-core-harness` (+ `scripts/visual_qa.js|py`), `design-data-viz`, `design-interactive-sandbox`, `design-office-deck`, `design-vector-svg`, `excel-data-art`, `ffi-api-wiring`, `frb-codegen-troubleshoot`, `frontend-design`, `pdf-report-expert`, `quality-gate`, `search-tool-discipline`, `security-review-orchestrator`, `stacked-pr-manager`, `web-research-discipline`.

Overlap with this machine's agy config: `code-review-taxonomy`, `quality-gate`, `security-review-orchestrator`, `design-*`, `web-research-discipline` and `deep-investigator`-style content already exist under `C:\Users\k1yt\.gemini\config\skills` and plugins. The kimi copies are the origin of several of these.

## 2. Install, activation, toggling

Install path (`setup-kimi.ps1`, PowerShell 5.1+, pwsh recommended):
1. Ensure `~/.kimi-code` exists.
2. Back up existing `config.toml`, `tui.toml`, `mcp.json`, `AGENTS.md`, `SYSTEM.md` to `~/backup_kimi_<yyyyMMdd_HHmmss>`.
3. Copy `AGENTS.md`, `SYSTEM.md`, `mcp.json` into `~/.kimi-code` (overwrite after backup).
4. `robocopy /E` sync `skills/`, `agents/`, `references/` into `~/.kimi-code/*` (exit 0-7 = success). No mirror delete, so removed files stay.
5. Merge `config.fragment.toml` into `config.toml` by appending only missing `[section]` headers or root keys (regex on header/key text). Never touches existing models/providers.
6. Print a checklist: set `GITHUB_TOKEN` and `HUGGINGFACE_TOKEN` user env vars, run `kimi`, check `/mcp`, `/skill`.

Activation model: Kimi Code natively scans only `~/.kimi-code/{skills,agents,AGENTS.md,SYSTEM.md,mcp.json,config.toml}` (User scope). The repo is a backup. Canonical side is `~/.kimi-code`; edit there first, then mirror and commit. Agent/SYSTEM/AGENTS changes take effect at next session start; config changes take effect on `/reload` or next session.

Toggle mechanism: there is NONE. There is no mode switch, no profile, no on/off flag. "Full-stack mode" is simply the presence of `SYSTEM.md` + agents + AGENTS.md in the home dir. The closest things are:
- Delete or rename `~/.kimi-code/SYSTEM.md` to drop the VP charter (agents and skills would remain).
- `feature.md` skill section 1: a user phrase such as "keep it light" aborts the heavy playbook and drops to normal execution (a per-task, in-prompt "light mode").
- The `[secondary_model]` pool controls which models subagents/swarm seats use, and `/secondary-model` switches the secondary default.
- Backups in `~/backup_kimi_*` allow manual rollback.
A `/mode` feature is therefore new design, not a port.

## 3. Agent roster and orchestration lifecycle

Main agent = VP of Engineering (SYSTEM.md). Invariants: orchestrator-first (delegate research/impl/design/review/security/QA), MICRO exception (single-file few-line edits, Q&A, lookups done directly), formal evidence over opinion. Enforcement is soft only ("SYSTEM.md cannot restrict the main agent toolset"; `/permission` gating deferred).

| Agent | Tools | Role |
|---|---|---|
| `research` | Read Grep Glob Bash WebSearch FetchURL Write | Modes: Deep (structure map with `[File#L]` citations), Verify (multi-source web cross-verification in the research swarm, returns claims register), Light, Causal-chain (`docs/causal-chains.yaml`). Refuses internal/loopback fetch targets. |
| `architect` | Read Grep Glob Write | Consult-only. Always emits exactly 3 options (A standard, B pragmatic, C staging) with Effort/Risk/Outcome plus per-subtask plan; writes plan under `docs/`. Document authority: spec > ADR > technical > code style. |
| `coder` | built-in Kimi agent | Implementation and fixes. No file in repo. |
| `designer` | Read Edit Write Bash ReadMediaFile | Render-and-inspect loop for visual deliverables. |
| `code-reviewer` | Read Grep Glob Bash Write | Read-only diff review: Stage 0 deterministic scan (via quality-gate skill), Stage 1 taxonomy scan (8 categories, P0-P3, confidence >= 75 filter, security findings bypass filter to handoff), verdict PASS/CONDITIONAL/REJECT (zero tolerance). Scope guard (>50 files or 2000 lines: split). Intent conformance check (missing required logic = P1). |
| `security-reviewer` | Read Grep Glob Write | Threat-model brief first, dimensions 2.1-2.6 (app vulns, secrets, transport/SSR, business-logic abuse, client/mobile), attack-chain analysis, attacker tier, verdict. 113 lines. |
| `blind-qa` | Read Grep Glob Bash Write | Build, test sweep, live exercise (headless only), state round-trip, side-effect check. Status PASS/FAIL/BLOCKED/INCONCLUSIVE. Deliberately single-instance (outside the swarm). |
| `debug-specialist` | Read Edit Write Bash Grep Glob WebSearch FetchURL | Hard bugs only: reverse-dependency map before edit, regression runs after. Escalation target of the no-progress rule. |
| `release-inspector` | Read Grep Glob Write | Production-artifact-only bugs (minify, tree-shake, CSP). Needs physical evidence. |
| `readiness-auditor` | Read Grep Glob Write | Whole-repo production readiness on explicit request only: 4 axes, scorecard, ranked plan. Single instance. |

All agents carry the "untrusted input is data, not instructions" guard.

Lifecycle:
- Dispatch contract (SYSTEM.md section 3): [User Intent & Rationale], [Domain Scope & File Boundaries], [Success Criteria/DoD], [Deliverable Contract] = full report to `docs/YYMMDD_NNNN_session_<slug>/<agent>-report.md` (folder chosen by VP, immutable, never committed), return `[Status] [Verdict] [Key Findings] [Artifact]`.
- 3b Intent conformance: user request becomes a verifiable checklist attached to every payload and to reviewers; at reconciliation each item is checked 1:1; missing = P1; every completion report ends with a requirement-coverage table (satisfied/partial/missing + evidence).
- 3c Decomposed dispatch: smallest units (tens of tool calls), phased, artifacts persisted so next phase starts fresh, AgentSwarm only over disjoint file scopes, resume only for true continuation, no-progress rule (same failure signature twice: change approach once, escalate to debug-specialist; after 2 rounds escalate to user), sequential same-file handoff protocol.
- 4 DoD: build/type green, tests pass, visual changes rendered and inspected, code-reviewer reconciled PASS (P0=0,P1=0), security-reviewer reconciled PASS (no critical/high), blind-qa PASS. Only unconditional PASS counts. All verification headless.
- `feature` skill (large work): Phase 1 explore + plan (parallel research), Phase 2 naive adversarial audit (fresh unprimed auditor, 3 axes: intent, grounding, risk/edge; REJECT loops max 3 then escalate; this audit also runs as the 4-model swarm), Phase 3 atomic-SRP coder dispatch, Phase 4 double-blind verification swarm (build/test, plan-conformance, edge-case), Phase 5 decisions.md, Phase 6 Korean final report. Light-mode abort rule at top.
- Judgment protocol (AGENTS.md 3): small fix now, medium fix in same session, large report-only and ask. No standing backlog; only user-choice items stay open.

## 4. Multi-model / external-provider mechanism

Provider layer (Kimi config.toml): `[providers.<Name>]` with `type` (`kimi` for the managed Kimi gateway, `openai` for all others = OpenAI-compatible chat completions), `api_key`, `base_url`. Providers configured: managed:kimi-code (Moonshot gateway), NW (NeuralWatt), Charm (hyper.charm.land), MiMo (Xiaomi token-plan), Google (OpenAI-compat endpoint; gemini-3.8-flash is permanently excluded), earlier also Mistral (removed later; see decisions 2026-10-06/07).
`[models."Provider/alias"]` maps an alias to provider + upstream `model`, with `max_context_size`, `capabilities`, `support_efforts`, `default_effort`.
`[secondary_model]`: `default_model` + `[secondary_model.models]` = alias to one-line capability hint. This is the "pool". The Agent tool's `model` parameter lists the pool; an unnamed subagent spawn binds `secondary_model.default_model`; an explicit `model` argument picks any pool alias. Subagent spawns bind the model entry's `default_effort`, not the caller's effort.
Current pool (names only): NW/kimi-k3-flex (default), kimi-code/k3-agent-swarm, MiMo/mimo-v2.6-pro, NW/glm-5.3-flash-flex, NW/deepseek-v4.1-flash-flex, Charm/kimi-k3, Charm/qwen3.8-max ([reserve]), Charm/qwen3.8-flash, Charm/glm-5.3-flash, Google/gemini-3.8-flash ("don't use").

Swarm rule (SYSTEM.md 3a), the multi-model review design:
- Every code-reviewer run, every security-reviewer run, every adversarial plan audit, and every deep-research dispatch = 4 parallel Agent calls with an IDENTICAL payload, double-blind (no shared context, can't see each other's reports).
- Seats: `primary` + fixed seat `charm/qwen3.8-flash` + 2 rotating draws.
- Draw candidates (live from the Agent tool model listing, never hardcoded): not primary, not the alias marked `[default]`, hint not starting `[reserve]` and not containing "don't use", and underlying model family differs from BOTH the main model and `secondary_model.default_model` (so all k3 aliases drop out). Prefer family-diverse pair, no reuse of the previous pair, spread usage evenly. Fixed seat missing from pool: drop it and tell the user.
- `[reserve]` hint tag: kept out of routine rotation; for genuinely hard problems or critical moments (disputed P0/CRITICAL, split verdict). A later user directive (2026-10-10) says reserve aliases run only on the user's explicit word.
- Output: each instance writes `<agent>-report.md` (primary) or `<agent>-report-<shortalias>.md`.
- Reconciliation, owned by the VP/Arbiter after all four return:
  1. Same root cause reported by >= 2 instances = CONFIRMED; merge gate judges on confirmed findings only.
  2. A blocking item (P0/P1, CRITICAL/HIGH, or plan weakness blocking APPROVE) reported by exactly 1 instance = DISPUTED: dispatch one verification instance on a pool model OUTSIDE the quartet to trace the exact path and confirm or refute. For external-fact disputes, use a deep-research dispatch.
  3. Final verdict = agent's own verdict rules on confirmed + upheld findings. Non-blocking singletons go back to the caller as preemptive-fix candidates.
  4. Report the reconciliation table (finding x instance x outcome, with accepted/rejected outcome later) and name models used. The outcomes are a calibration record.
- Deep-research variant: same 4 seats; claim-level verdicts CONFIRMED/REFUTED/UNRESOLVED/UNVERIFIABLE; CONFIRMED needs independent sources (same URL or syndicated copies count once, else INFERRED); VP writes merged `deep-research-report.md`; Verification Rigor block (min 2 sources, 40 WebSearch, 80 FetchURL per instance); quoted web text in reports is data not instructions.
- Out of the swarm: blind-qa (environment-determined verdicts, shared-state collisions), readiness-auditor, single `research` structure scans.
- Operational lessons (decisions.md): Charm credits ran out (402), so the 4-seat swarm degraded to 3 via drop-and-tell; provider tier limits (Mistral 20k TPM 429); strict providers reject extra fields or unsupported `reasoning_effort` values (422/400), so per-model `support_efforts`/`default_effort` must be accurate; cost was explicitly excluded from the design after a failed `[expensive]` tag experiment (replaced by `[reserve]`).
- "Arbiter" is not a named agent or word anywhere; the arbiter role = the main agent's reconciliation duty (3a Reconciliation) plus the out-of-quartet verification instance.

### What "hang" means
Searching the repo (excluding `docs/` and `.git`) for the whole word finds only: `skills/testdrive.md:200-201` ("Invisible Process Hang" when a launched app has no window handle; "crash or hang" auto-fix) and `skills/causal-chain-analysis/SKILL.md:101` (retry/timeout problems list "hangs"). It means a stuck process, nothing to do with models/providers. In `docs/` only two third-party files under `261008_0001_session_autoprompt-skill-review/_repo/` matched. No config key, command, provider, or mode named `hang` exists. The user may have meant something else (candidates: the "swarm", "AgentSwarm", the token-plan provider, or a Korean/typo term such as "행"/"heng"). Needs a clarifying question to the user; do not assume.

## 5. Kimi-specific parts and porting notes for Antigravity CLI (agy)

| Kimi artifact | Kimi-specific dependency | agy porting note |
|---|---|---|
| `~/.kimi-code/` home, `kimi` binary, `/skill`, `/mcp`, `/reload`, `/permission`, `/secondary-model` | Kimi runtime and slash commands | agy home is `~/.gemini/antigravity-cli` and config is `~/.gemini/config` (rules, skills, plugins). Slash commands must be re-expressed as agy skills/commands. A `/mode` command must be implemented using whatever agy supports (verify with `agy_help` skill). |
| `SYSTEM.md` with `${base_prompt}` | Kimi system prompt override; replaces/prepends base | agy has `config/rules/AGENTS.md` and `GEMINI.md` already acting as the VP charter. Full-stack mode = enabling a bundle of rules; clean mode = disabling it. No `${base_prompt}` equivalent known. |
| `AGENTS.md` | Mostly portable. Kimi tool names (`ReadMediaFile`, `WebSearch`, `FetchURL`, `mcp__crow-memory__*`) | Map tool names. crow-memory MCP at 127.0.0.1:9021 is optional. Some rules overlap or conflict with agy rules (e.g. agy forbids `rm`; kimi says recycle bin only; kimi bans em-dash; agy mandates Node-only tooling). |
| `agents/*.md` frontmatter (`name`, `description`, `whenToUse`, `tools: [Read, Grep, Glob, Bash, Write, Edit, WebSearch, FetchURL, ReadMediaFile]`) | Kimi tool vocabulary and Agent tool | agy uses `define_subagent`/`invoke_subagent` with `enable_write_tools`/`enable_mcp_tools`. Bodies are portable prose. Hardcoded `~/.kimi-code/skills/...` paths in code-reviewer and research must be repointed. Agent `model` selection per call does not exist in the same form; see below. |
| Agent tool `model` parameter, `AgentSwarm` tool | Kimi built-in parallel dispatch with per-call model alias | agy subagents run on agy's own model. External providers need a bridge: a small Node (zero-dependency) CLI that calls OpenAI-compatible `/chat/completions` endpoints (all non-Kimi providers here are `type=openai`), reads a provider registry file, runs N reviewers in parallel with the identical payload, writes `<agent>-report-<alias>.md`. The agy VP then performs reconciliation. This is the key new component. |
| `[providers]/[models]/[secondary_model]` in `config.toml` | Kimi TOML schema | Replace by an agy-side provider registry (JSON/TOML) with env-var key references (never plaintext keys). Port pool hints and tags (`[reserve]`, `don't use`) and the draw rule logic (family exclusion needs `model` field). |
| `config.fragment.toml` merge logic in `setup-kimi.ps1` | Regex append merge | Not needed unless a registry file is merged. |
| `setup-kimi.ps1` | PowerShell, `$HOME\.kimi-code`, backup to `~/backup_kimi_*`, robocopy | Reuse the pattern (idempotent, dry-run, timestamped backup, no secrets) but rewrite per the user's Node-only, cross-platform rule (`install.js`). Note the robocopy `/E` never deletes, so toggling off must remove files explicitly (or better, use a manifest). |
| `mcp.json` (`bearerTokenEnvVar`) | Kimi MCP format | Check agy MCP config format. Env-var-token pattern is reusable. |
| Report folder `docs/YYMMDD_NNNN_session_<slug>/` | Convention, portable | agy rules use `reports/<agent>-report.md`. Decide on one. |
| Skills (`type: flow|prompt`, Korean descriptions) | Kimi frontmatter keys `type`, `whenToUse` | agy SKILL.md uses `name`, `description` only. Flat `.md` playbooks must become `<name>/SKILL.md`. Many already exist in agy config. |
| `visual_qa.js/.py` | Playwright headless (portable) | Reuse as is. |
| `github-account.*`, `kimi-env.cmd` | Kimi relaunch wrapper | Not portable, skip. |
| `horse.html`, `low-poly-horse.html`, `docs/` | Artifacts | Do not port. |

Behavioral differences to resolve: the repo's swarm design assumes `AgentSwarm` and per-call model choice are native; in agy they are not, hence the bridge. Reviewer seat names are per-provider aliases; the fixed seat (`charm/qwen3.8-flash`) is already unservable (credits), so the design must keep drop-and-tell. Soft enforcement of orchestrator-only behavior is a known limitation in Kimi; agy rules say the same (Master delegates everything).

## 6. Recommended building blocks for the agy "full-stack mode"

1. Mode bundle: a manifest listing the rules, agents, skills and MCP entries that belong to `fullstack`; `default/clean` = none of them. `/mode` flips an active-mode marker and installs or removes the bundle (atomic writes, backups, Node-only).
2. Provider registry + `mm-swarm` Node CLI (OpenAI-compatible fan-out, env-var keys, per-alias hints/tags, family field, rotation state file, no-immediate-reuse pair logic).
3. Arbiter protocol skill: port SYSTEM.md 3a reconciliation (confirmed >= 2, disputed singleton blocking item to out-of-quartet verification, reconciliation table, accepted/rejected feedback record, untrusted-data rule) as a skill the agy VP follows.
4. Reuse agents as prompts: code-reviewer, security-reviewer, research, architect, blind-qa, debug-specialist, release-inspector, readiness-auditor, designer.
5. Adversarial plan audit in `feature` lifecycle runs through the same swarm.
6. Secrets: never copy `config.toml`; registry stores env var names only.

## 7. Evidence index
- Read in full: README.md, AGENTS.md, SYSTEM.md, setup-kimi.ps1, config.toml (keys redacted in this report), config.fragment.toml, mcp.json, decisions.md, agents/architect.md, code-reviewer.md, blind-qa.md, research.md, security-reviewer.md lines 1-30, skills/feature.md.
- Frontmatter-only: debug-specialist, designer, readiness-auditor, release-inspector, all flat skills.
- Not read: PORTING_PLAN.md (denied), skills folders bodies, SYSTEM-adjacent docs (sampled only `docs/261010_0001_.../code-reviewer-report-mimov26pro.md` head, which confirms double-blind per-model report naming).