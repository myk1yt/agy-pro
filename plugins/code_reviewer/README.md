# Code Reviewer - Pre-Merge Diff Reviewer & Quality Gate

[![Antigravity Plugin](https://img.shields.io/badge/Antigravity-Plugin-blue.svg)](https://github.com/google/antigravity)
[![Zero-Tolerance Verdict](https://img.shields.io/badge/Verdict-Zero--Tolerance-red.svg)](#severity-classification-matrix-p0--p3)
[![Read-Only Gate](https://img.shields.io/badge/Authority-Read--Only-green.svg)](#hardware-read-only-guarantee)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#quick-start-one-click-installation)

An autonomous pre-merge diff reviewer and deterministic quality gate plugin for Google Antigravity. Built with a strict **read-only hardware constraint** (excluding all file write/edit tools), Code Reviewer audits pull requests and uncommitted working diffs through a robust 4-stage pipeline, catching bugs, security risks, stability flaws, and formatting issues before code merges into production.

---

## 🚀 Quick Start (One-Click Installation)

Open your terminal in the repository root directory and run the command for your operating system:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-code-reviewer.ps1
```

> 💡 **Beginner Tip (초보자 / 컴맹을 위한 팁)**:
> In Windows File Explorer, press **Shift + Right-Click** in an empty area inside the repository folder and select **"Open PowerShell window here"** or **"Open in Terminal"**, then copy and paste the command above and press Enter.

### macOS / Linux (Terminal)
```bash
bash scripts/install-code-reviewer.sh
```

> **What the installer does automatically**:
> 1. Registers the `code_reviewer` plugin with Antigravity CLI via `import_manifest.json`.
> 2. Deploys both modular review skills (`code-review-taxonomy` and `quality-gate`) to `~/.gemini/config/skills`.
> 3. Verifies agent registration via `agy agents` and validates plugin health via `agy plugin validate`.

---

## 💡 How to Use in Antigravity

Once installed, invoke Code Reviewer inside Antigravity CLI in two easy ways:

### Method 1: Using the `/agent` Menu

Type `/agent` in the Antigravity prompt and select `code-reviewer` using arrow keys:

```text
┌────────────────────────────────────────────────────────┐
│ Select an Agent                                        │
├────────────────────────────────────────────────────────┤
│ > code-reviewer     (Pre-Merge Diff Reviewer & Gate)   │
│   security-reviewer (Enterprise Multi-Agent Audit)     │
│   designer          (Zero-MCP Design Specialist)       │
│   agy_help          (Antigravity Ecosystem Guide)      │
└────────────────────────────────────────────────────────┘
  ▲/▼: Navigate   Enter: Select   Esc: Cancel
```

### Method 2: Direct Mention (`@code-reviewer`)

Mention `@code-reviewer` anywhere in your prompt:

#### 1. Review Working Tree Changes
```text
@code-reviewer Review all uncommitted changes in the current branch against main before I create a PR.
```

#### 2. Review a Specific Commit Range
```text
@code-reviewer Perform a pre-merge audit on HEAD~3..HEAD with focus on data-integrity and security.
```

#### 3. Targeted Pull Request Audit
```text
@code-reviewer Run Stage 0 quality-gate and Stage 1 taxonomy audit on the authentication refactor diff.
```

---

## 🔬 4-Stage Review Pipeline

Code Reviewer enforces a sequential 4-stage pipeline that cleanly separates deterministic command execution from LLM semantic judgment:

```text
[Diff / Changes]
       │
       ▼
┌────────────────────────────────────────┐
│ Stage 0: Deterministic Quality Gate    │ ➔ Runs QG-01/02/03/04/10/12/13/14
│ (quality-gate skill)                   │   Lint, format, types, invisible unicode
└──────────────────┬─────────────────────┘
                   │
                   ▼
┌────────────────────────────────────────┐
│ Stage 1: 8-Category Taxonomy Scan      │ ➔ LLM semantic reasoning across 8 orthogonal
│ (code-review-taxonomy skill)           │   categories, assigns P0-P3 & confidence (0-100)
└──────────────────┬─────────────────────┘
                   │
                   ▼
┌────────────────────────────────────────┐
│ Stage 2: Confidence Filter & Bypass    │ ➔ Drops noise (<75% confidence)
│ (security handoff to security-reviewer)│   Security bypasses filter ➔ @security-reviewer
└──────────────────┬─────────────────────┘
                   │
                   ▼
┌────────────────────────────────────────┐
│ Stage 3: Learning Handoff & Verdict    │ ➔ Synthesizes reports, captures lessons,
│ (Zero-Tolerance: PASS/CONDITIONAL/REJECT) emits blocking or passing verdict
└────────────────────────────────────────┘
```

1. **Stage 0 (Deterministic Scan)**: Executes non-destructive, read-only tool checks (`npx prettier --check`, `npx eslint`, `tsc --noEmit`, `cargo check`, invisible unicode regex scans).
2. **Stage 1 (8-Category Taxonomy Scan)**: Audits diff hunks with surrounding context (±20 lines) across 8 orthogonal defect dimensions.
3. **Stage 2 (Confidence Filter & Security Bypass)**: Drops speculative findings below 75% confidence. Security findings bypass the filter and trigger `@security-reviewer` for validation.
4. **Stage 3 (Learning Handoff & Verdict)**: Produces the final structured review deliverable and computes the zero-tolerance verdict.

---

## 📊 The 8 Taxonomy Categories

| # | Category | Focus Areas |
|---|:---|:---|
| 1 | **correctness** | Logic errors, off-by-one, inverted conditions, dead branches, API misuse, broken invariants. |
| 2 | **security** | Injection, auth/authz bypass, secret exposure, deserialization, path traversal, SSRF, crypto flaws. |
| 3 | **stability** | Crash paths, unhandled errors/panics, race conditions, resource leaks, missing awaits, lifecycle issues. |
| 4 | **data-integrity** | Validation gaps, transaction boundaries, migration safety, idempotency, data-loss edge cases. |
| 5 | **performance** | Superlinear complexity, N+1 queries, needless allocations, blocking I/O on hot paths, unbounded growth. |
| 6 | **maintainability** | Dead code, duplicated logic, oversized functions, leaky abstractions, misleading naming. |
| 7 | **test-coverage** | Changed logic without tests, missing edge cases (null/empty/max/concurrency), tautological assertions. |
| 8 | **style-docs** | Formatting drift, naming convention drift, stale documentation, comments out of sync with code. |

---

## ⚖️ Severity Classification Matrix (P0 – P3)

| Severity | Label | Impact & Criteria | Merge Gate Impact |
|:---|:---|:---|:---|
| 🔴 **P0** | **Blocker** | Data loss/corruption, auth/crypto vulnerability, fatal crash/hang, core logic breakdown. | **REJECT** (Zero tolerance, blocks merge immediately). |
| 🟠 **P1** | **Critical** | Real functional defect, unhandled error in secondary flow, major performance flaw, high-risk gap with no safe fallback. | **CONDITIONAL** (Requires fix before merge). |
| 🟡 **P2** | **Major** | Missing edge-case test coverage, maintainability debt, architectural degradation. | **CONDITIONAL** (Allowed to proceed only with explicit author waiver). |
| 🟢 **P3** | **Minor** | Polish, documentation improvements, formatting consistency, minor nits. | **PASS** (Non-blocking recommendations). |

---

## 🤝 Security Reviewer Pairing Architecture

Code Reviewer pairs seamlessly with `@security-reviewer`:
- When Stage 1 flags any `security` finding, it **bypasses** the Stage 2 confidence filter regardless of score.
- The finding is marked `handoff: security-reviewer` and routed via `invoke_subagent` to `@security-reviewer`.
- `@security-reviewer` performs a targeted confirmation pass (confirming, upgrading, or downgrading severity) and reports back.

---

## 📦 Bundled Modular Skills

Code Reviewer comes bundled with 2 specialized skills deployed globally:

| Skill | Description |
|---|---|
| **`code-review-taxonomy`** | 8-category semantic review taxonomy, normalized finding schema, confidence scoring, and security bypass rules. |
| **`quality-gate`** | Universal 20-category verification framework, 6-tier command resolution, and non-destructive audit engine. |

---

## 🔒 Hardware Read-Only Guarantee

To guarantee that the reviewer never accidentally mutates source code, configurations, or git history while auditing:
- Tools `write_to_file` and `replace_file_content` are **strictly omitted** from agent capabilities.
- The reviewer operates exclusively through read tools (`view_file`, `grep_search`, `find_by_name`, `list_dir`) and read-only test runners.

---

## 🗑️ One-Click Uninstallation

To cleanly remove the Code Reviewer plugin and its mirrored skills:

### Windows (PowerShell)
```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall-code-reviewer.ps1
```

### macOS / Linux (Terminal)
```bash
bash scripts/uninstall-code-reviewer.sh
```

---

## 🛡️ License

MIT © myk1yt
