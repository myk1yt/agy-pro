# Antigravity CLI vs Kimi Rules Conflict Analysis and Resolution

## 1. Executive Summary

This document analyzes structural, procedural, and stylistic conflicts between the native Antigravity CLI (`agy`) rules and the Kimi Code ecosystem (an external, private configuration repo), establishing an authoritative resolution matrix for `agy-pro` in **Full-Stack Mode**.

The governing principle of `agy-pro` is **Safety First without Compromise**:
- Whenever safety, data integrity, or credential privacy is at stake, the most protective standard governs.
- Whenever operational leverage, double-blind verification, or orchestrator discipline is concerned, Kimi's VP of Engineering paradigm and multi-model consensus are integrated.

---

## 2. Comprehensive Conflict Comparison Matrix

| # | Dimension | Antigravity CLI (`agy`) Rule | Kimi Code (external config) Rule | Core Conflict | `agy-pro` Full-Stack Default Resolution | User Choice / Customization Option |
|---|---|---|---|---|---|---|
| **1** | **File Deletion Policy** | Permanent deletion (`rm -rf`, `del /s /q`, `Remove-Item -Recurse -Force`) is strictly forbidden. Move files to Recycle Bin. | Deletion goes to Recycle Bin only. `rm -rf`, `del`, `Remove-Item` forbidden. | None in philosophy; slight difference in fallback behavior (Recycle Bin vs atomic backup folder). | **Harmonized (Strict Safety)**: Permanent deletion commands are banned across all shells. Deleted or replaced files are moved to the OS Recycle Bin, or to an isolated project backup folder (`.agy-backup/<timestamp>/`) when headless. | Set `AGY_DELETE_POLICY=recycle` (default) or `AGY_DELETE_POLICY=backup_dir`. |
| **2** | **Typographic Style (em-dash)** | Standard technical Markdown allows em-dash (`--`) for parenthetical clauses. | **Strictly Forbidden**: em-dash (`--`) is banned; use commas, periods, or `...`. | Kimi explicitly bans em-dash to prevent token encoding bugs and Windows CP949 display glitches. | **Adopt Kimi Ban**: Prohibit em-dash (`--`) in all generated agent documentation, reports, and code comments. Use commas, hyphens, periods, or ellipses. | Users may override in custom rules, but default profile enforces ASCII/Unicode safe punctuation. |
| **3** | **Language Policy** | CEO briefings in 100% Korean; internal operations, code, rules, and reports in Precision English. | User-facing output 100% Korean; internal agent files, `decisions.md`, skills in English. | Identical core philosophy. | **Harmonized**: 100% Korean for all user-facing interactions and executive briefings. Precision English for all internal code, identifiers, commit messages, and persisted reports. | `--lang en` or `AGY_LANG=en` flag can switch executive briefings to English. |
| **4** | **Implementation Stack & Dependencies** | Pure Node.js standard library (`fs`, `path`, `child_process`). Zero external npm dependencies. | Mixed environment: Python (`visual_qa.py`), PowerShell (`setup-kimi.ps1`), external MCP servers. | Language and toolchain divergence between Node.js and Python/PS. | **Go Standard Library for `agy-pro` Core**: `agy-pro` is written in Go 1.22+ standard library (zero external modules, single binary). CLI scripts and UI harness use Node.js built-ins. | Pure Go binary operates standalone across Windows, macOS, and Linux. |
| **5** | **Orchestrator Delegation Invariant** | **100% Delegation Invariant**: Master NEVER modifies project source code, authors scripts, or runs tests directly. | **Orchestrator-First with MICRO Exception**: Single-file edits of a few lines, factual Q&A, and quick lookups may be handled directly. | Strict zero-labor invariant vs pragmatic micro-edit bypass. | **Synthesized Pragmatism**: The Master Agent is pure orchestrator for all architectural, multi-file, or behavioral changes. The **MICRO exception** is permitted exclusively for trivial fixes (≤5 lines) and factual Q&A to eliminate conversational thrashing. | Users can enforce strict 100% delegation by specifying `--strict-delegation` or prompting "모든 수정 위임". |
| **6** | **Subagent Dispatch Architecture** | Native Antigravity tools: `invoke_subagent`, `define_subagent`, `send_message`. Single-engine execution. | Native `Agent` and `AgentSwarm` tools with explicit `model` argument bound to TOML aliases. | Tool API names and execution engines differ completely. | **Mapped to `agy` Tools + `agy pro fanout`**: Agents are registered via `define_subagent` and spawned via `invoke_subagent`. Multi-model consensus is executed via `agy pro fanout` external bridge. | Fallback to single-model native dispatch when fanout providers are unconfigured. |
| **7** | **Multi-Model Consensus & Swarm** | Not native in standard `agy` (subagents share primary model). | 4-model parallel swarm (Primary + Fixed `<provider>/<model>` + 2 rotating secondary pool models) on identical payload. | Standard `agy` lacks native multi-provider model routing. | **`agy pro fanout` Double-Blind Swarm**: Trigger 4-model double-blind consensus for Code Review, Security Review, Architect Evaluation, and Deep Research. | Light Mode bypass: prompt "가볍게" or "keep it light" to execute single-instance without swarm. |
| **8** | **Arbiter Protocol & Escalation** | Master reviews single subagent reports and performs qualitative synthesis. | Main agent reconciles 4 reports (Confirmed $\ge 2$, Disputed singleton blocking item $\to$ 1 out-of-quartet verification). | Unilateral master review vs structured quantitative quorum reconciliation. | **VP as Arbiter**: VP reconciles 4 reports. Confirmed ($\ge 2$) gates merge. Disputed blocking items trigger targeted re-verification (up to 2 rounds). Reconciled table rendered in briefing. | Final discretion remains with VP Arbiter; CEO can override via chat. |
| **9** | **Fault Recovery & Failover** | Subagent failure reports BLOCKED / FAILED to master. | Fixed model drops if unservable (drop-and-tell). | Ad-hoc error handling vs structured failover tiers. | **3-Tier Failover Policy**: 1. Same model on alternate provider; 2. Fallback model from rotation pool; 3. Notify VP and exclude seed from current run while proceeding with remaining quorum ($\ge 3$). | Configurable provider timeout and retry limits in `providers.json`. |
| **10** | **Report Artifact Locations** | Persisted to `reports/<domain>-report.md` in workspace root. | Persisted to Zoo session folders: `docs/YYMMDD_NNNN_session_<task-slug>/<agent>-report[-<alias>].md`. | Monolithic flat reports directory vs nested task-session folders. | **Standardized Workspace Reports**: Write to `reports/<agent>-report.md` (and `reports/<agent>-report-<alias>.md` for fanout models). Clean, predictable, and directly navigable with `file:///` URIs. | Session subdirectories can be enabled via `AGY_SESSION_REPORTS=true`. |
| **11** | **Secret Management & Storage** | Zero hardcoded secrets. Environment variable references only. | `config.toml` held plaintext API keys in disk configuration (gitignored, but exposed locally). | High risk of credential leakage in local config files. | **Strict Zero-Secret Registry**: Registries (`providers.json`) store **ONLY environment variable names** (e.g., `GEMINI_API_KEY`, `OPENAI_API_KEY`). Real values are resolved in-memory at call time and dynamically redacted. | Never persist raw keys. Automated doctor check warns if plaintext key is mistakenly detected. |
| **12** | **Screen & UI Verification** | Visual inspection via browser/canvas rendering and screenshot review. | **Headless Verification Invariant**: User screen is strictly off-limits. Never open a visible window, never move user mouse/keyboard. | Complete alignment, reinforced to zero-tolerance. | **Headless-Only Verification**: All UI/E2E testing runs via headless automation (Playwright `headless: true`, minimized background process). Any visible window interaction is classified as BLOCKED. | When headless execution is technically impossible, mark as `시각 미검증 (UNVERIFIED)`. |

---

## 3. Deep Dive into Key Divergences

### 3.1 File Deletion and Safety Protocol
- **Conflict**: In standard Unix workflows, developers often run `rm -rf` or `git clean -fd`. However, both Antigravity and Kimi rules classify permanent deletion as high risk.
- **Resolution in Full-Stack**:
  1. Permanent deletion via CLI commands is strictly prohibited.
  2. Temporary files must use `os.Remove()` only within designated `.tmp-*` scopes created by atomic writes.
  3. When an agent needs to remove or replace user files, it moves them to a timestamped backup directory (e.g., `reports/backups/` or `.agy-backup/`) or the OS Recycle Bin.
  4. Protected paths (`.git`, `.github`, user AppData, dependency directories) are completely untouchable without explicit CEO permission.

### 3.2 Stylistic and Typographic Standard: The em-dash Ban
- **Conflict**: Technical documentation in English frequently uses em-dashes (`--`). However, Kimi explicitly bans them because LLMs frequently misuse them as filler connectors, and they cause character encoding anomalies across mixed UTF-8 / Windows Korean CP949 environments.
- **Resolution in Full-Stack**:
  1. The ban on em-dashes (`--`) is adopted across all agent system prompts and output rules.
  2. Agents must use commas, semicolons, parentheses, or periods instead.
  3. This also eliminates empty AI rhetorical transitions and forces crisp, punchy sentences.

### 3.3 Orchestrator Role: Strict 100% vs Pragmatic MICRO Exception
- **Conflict**: Antigravity's original rule states "Master NEVER modifies project source code directly." Kimi introduces the MICRO exception for 1-to-5 line typo fixes and quick fact queries to prevent context thrashing.
- **Resolution in Full-Stack**:
  - The default remains **Orchestrator-First**. The VP must delegate any task that alters logic, spans multiple files, touches tests, or requires validation.
  - The **MICRO Exception** is strictly bounded:
    * Condition: Single-file edit under 5 lines (e.g., correcting an import typo, bumping a version string) or read-only factual lookup.
    * Constraint: If the edit triggers regression risks or modifies business behavior, it immediately reverts to mandatory delegation.

### 3.4 Multi-Model Consensus vs Native agy Subagents
- **Conflict**: Standard Antigravity CLI does not provide built-in multi-provider model routing for subagents. Kimi relied on native `secondary_model` configuration and `AgentSwarm`.
- **Resolution in Full-Stack**:
  - `agy-pro` introduces `agy pro fanout`, a Go-powered CLI shim that queries an OpenAI-compatible provider registry using only environment-variable credentials.
  - Reviewers (`code-reviewer`, `security-reviewer`, `architect`, `research`) execute double-blind across 4 models (Primary + 3 external).
  - The VP of Engineering acts as the **Arbiter**, synthesizing consensus and resolving singleton disputes.

---

## 4. User Configuration and Profile Selection

Users can switch between profiles or fine-tune specific rules using the `agy pro` toolchain:

```bash
# Switch to Full-Stack Profile (VP Orchestrator + 9 Agents + 4-Model Swarm)
agy pro mode full

# Switch to Clean Profile (Pure baseline agy state + /mode switch skill)
agy pro mode clean

# Check current configuration and provider environment variables
agy pro doctor
```

In any prompt, users can also declare ad-hoc behavioral overrides:
- `"가볍게 진행해줘"` or `"keep it light"`: Skips the 4-model fanout swarm and runs a single-instance review.
- `"직접 수정해줘"`: Bypasses subagent spawning for small tasks within the active conversation.
- `"엄격 위임 모드"`: Disables the MICRO exception and enforces 100% subagent delegation.
