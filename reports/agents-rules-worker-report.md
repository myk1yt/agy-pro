# Full-Stack Rules, Kimi Agents Porting, and Clean Profile Report

- **Worker**: Domain Worker (Rules, Agents, and Profiles)
- **Scope**: `agy-pro/profiles/full/**`, `agy-pro/profiles/clean/**`, `agy-pro/profiles/profile.json`, `agy-pro/docs/rules-conflicts.md`
- **Date**: 2026-10-11
- **Status**: **COMPLETE / READY**

---

## 1. Executive Summary

This initiative successfully establishes the foundational rules, agent profiles, conflict documentation, and mode switching mechanisms for the **Full-Stack Mode** and **Clean Baseline Mode** of `agy-pro`:

1. **VP of Engineering Orchestrator Governance (`profiles/full/rules/AGENTS.md` & `GEMINI.md`)**:
   - Synthesized Antigravity CLI's native governance with Kimi's VP Charter and Arbiter protocols.
   - Enshrined mandatory 4-model double-blind consensus via `agy pro fanout` for Code Review, Security Review, Architect Evaluation, and Deep Research.
   - Formalized the VP Arbiter reconciliation protocol with up to 2 verification rounds and a 3-tier fault recovery / failover policy.
   - Integrated user light-mode opt-out ("가볍게" / "keep it light") and intent-conformance tracking.
2. **Rules Conflict Analysis (`agy-pro/docs/rules-conflicts.md`)**:
   - Authored an exhaustive 12-dimension comparison matrix contrasting native `agy` rules and `kimi-settings`.
   - Resolved all conflicts under a strict "Safety-First" default posture while outlining user configuration choices.
3. **Porting of 9 Kimi Agents (`profiles/full/agents/<name>/AGENT.md`)**:
   - Ported all 9 specialized agents from `kimi-settings/agents/` into the `agy` tool ecosystem (`view_file`, `write_to_file`, `replace_file_content`, `run_command`, `search_web`, `read_url_content`, etc.).
   - Authored `profiles/full/rules/agents-index.md` providing complete dynamic registration JSON templates (`define_subagent`) and VP delegation prompts.
4. **Clean Baseline Mode (`profiles/clean/**`)**:
   - Preserves pure vanilla Antigravity CLI behavior with zero subagent overhead.
   - Provides the interactive `/mode` skill (`profiles/clean/skills/mode/SKILL.md`) enabling seamless toggling between Full-Stack and Clean profiles.
   - Authored `profiles/clean/profile.json` and `profiles/full/profile.json` manifests.

---

## 2. Key Accomplishments & Deliverables

### 2.1 Task 1: Merged Rules (`AGENTS.md` and `GEMINI.md`)
- [AGENTS.md](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/rules/AGENTS.md):
  - **The 100% Delegation Invariant**: Master never performs direct implementation, ad-hoc inspection scripts, or testing suites; delegates 100% to specialized subagents.
  - **Pragmatic MICRO Exception**: Single-file edits under 5 lines (typo fixes, version bumps) and factual Q&A may be handled directly to eliminate conversational thrashing.
  - **4-Model Double-Blind Swarm**: Code review, security review, architect plan review, and deep research automatically execute across 4 models (Primary + 3 external models) via `agy pro fanout`.
  - **VP as Arbiter**:
    - Confirmed ($\ge 2$ models report same root cause): gates merge.
    - Disputed (singleton blocking item): triggers out-of-quartet re-verification (up to 2 rounds).
    - Preemptive-fix routing for non-blocking singleton items.
    - Reconciliation Matrix rendered in executive briefings.
  - **User Light-Mode Bypass**: Prompts mentioning "가볍게" or "keep it light" bypass the 4-model fanout swarm and execute single-instance reviews.
  - **3-Tier Failover Policy**: 1. Same model on alternate provider; 2. Fallback model from rotation pool; 3. Notify VP and exclude seed from current run while proceeding with remaining quorum ($\ge 3$).
  - **Intent Conformance (3b)**: Numbered checklist attached to delegation payload; omission = P1 defect; requirement-coverage table mandatory in completion briefings.
  - **Multi-Tier Definition of Done (DoD)**: Build is floor; visual changes rendered and headlessly inspected; reconciled code review PASS (P0=0, P1=0); reconciled security review PASS (Critical=0, High=0); blind-qa PASS.
  - **Safety & Privacy**: Permanent deletion strictly forbidden; headless verification invariant (user screen off-limits); zero plaintext keys in disk registries.
  - **Language & Style**: Executive briefings in 100% Korean; internal operations in Precision English; em-dash (`--`) ban enforced.
- [GEMINI.md](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/rules/GEMINI.md):
  - Zero external dependency standard (Go standard library for `agy-pro`, Node.js built-ins for scripts).
  - Cross-platform portability across Windows, macOS, and Linux without hardcoded separators or usernames.
  - Atomic file write pattern (`WriteFileAtomic`).
  - Security invariant: Registries store environment variable names only; runtime masking via redactor.
  - 100% pass rate requirement for `go test ./...` and `go vet`.

### 2.2 Task 2: Rules Conflicts Documentation (`rules-conflicts.md`)
- [rules-conflicts.md](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/docs/rules-conflicts.md):
  - Comprehensive comparison matrix across 12 dimensions:
    1. File Deletion Policy (Recycle Bin / Backup folder vs `rm -rf`)
    2. Typographic Style (em-dash prohibition)
    3. Language Policy (Korean CEO briefings vs English internals)
    4. Implementation Stack (Go standard library + Node built-ins)
    5. Orchestrator Delegation (100% delegation vs MICRO exception)
    6. Subagent Dispatch Architecture (`invoke_subagent` / `define_subagent` vs `Agent` tool)
    7. Multi-Model Consensus (`agy pro fanout` bridge vs native TOML pool)
    8. Arbiter Protocol & Escalation Rounds
    9. Fault Recovery & Failover Policy
    10. Report Artifact Paths (`reports/<agent>-report.md` vs Zoo session directories)
    11. Secret Storage (Zero-secret registry vs plaintext config)
    12. Screen & UI Verification (Headless-only invariant)
  - Clear user configuration choices and CLI commands (`agy pro mode full`, `agy pro mode clean`, `agy pro doctor`).

### 2.3 Task 3: Porting of 9 Kimi Agents & Catalog Index
Ported all 9 agents into `profiles/full/agents/<name>/AGENT.md` using native `agy` tool terminology (`view_file`, `write_to_file`, `replace_file_content`, `run_command`, `search_web`, `read_url_content`, `generate_image`):

1. [architect](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/architect/AGENT.md): Consult-only planner; cross-domain FE/BE contracts; emits exactly 3 options (A Standard, B Pragmatic, C Staging) with Effort/Risk/Outcome and subtask plan.
2. [blind-qa](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/blind-qa/AGENT.md): Independent runtime verifier; builds, test sweeps, live CLI/curl invocations, save/load state round-trips; strictly headless; single-instance execution outside swarm.
3. [code-reviewer](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/code-reviewer/AGENT.md): Read-only pre-merge diff reviewer; 4-model double-blind swarm participant; stage 0 deterministic scan, stage 1 8-taxonomy scan; zero-tolerance P0/P1 gate.
4. [debug-specialist](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/debug-specialist/AGENT.md): Escalation specialist for hard bugs; "Impact first, proof last" directive; reverse-dependency map before edit, regression run after; 5 diagnostic lenses.
5. [designer](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/designer/AGENT.md): Multimodal visual/UI engineer; "Not done until rendered" directive; headless screenshot inspection via `view_file`; WCAG AA contrast; aesthetic dials (Variance, Motion, Density).
6. [readiness-auditor](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/readiness-auditor/AGENT.md): Whole-repo deployability auditor; 4 axes (Operations, Database, Scalability 100 to 1M users, Missing Systems); 10-row scorecard; high-ROI fix plan.
7. [release-inspector](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/release-inspector/AGENT.md): Production artifact diagnostician; physical evidence only; tree-shaking, minification, CORS/CSP; Path A (build config) vs Path B (core logic flaw).
8. [research](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/research/AGENT.md): Codebase structure investigator and fact oracle; 4 modes (Deep, Verify, Light, Causal-chain); `[FilePath#L#]` citations; 4-model web cross-verification claims register.
9. [security-reviewer](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/agents/security-reviewer/AGENT.md): Read-only security auditor; 4-model double-blind swarm participant; Threat Model Brief; 6 audit dimensions; test mock token exemption; multi-step attack-chain modeling.
- [agents-index.md](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/rules/agents-index.md): Comprehensive catalog table, `define_subagent` dynamic registration JSON templates, and high-leverage VP dispatch guidelines.

### 2.4 Task 4: Clean Profile & Mode Switcher Skill
- [SKILL.md (clean)](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/clean/skills/mode/SKILL.md): Interactive skill prompting the user to choose between Full-Stack Mode and Clean Baseline Mode, with automated execution instructions for `agy pro mode`.
- [SKILL.md (full)](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/skills/mode/SKILL.md): Mirror skill in full-stack mode ensuring bidirectional switching.
- [profile.json (clean)](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/clean/profile.json): Manifest for the clean profile declaring managed files.

### 2.5 Task 5: Full Profile Manifest & Root Catalog
- [profile.json (full)](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/full/profile.json): Manifest for full profile with complete list of managed rules, agents, skills, and references.
- [profile.json (root)](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/profiles/profile.json): Root catalog registering `full` and `clean` profiles with `full` as default.

---

## 3. Verification & Validation Summary

| Check Item | Command / Procedure | Result | Details |
|---|---|---|---|
| **JSON Syntax Validation** | `ConvertFrom-Json` in PowerShell | **PASS** | Validated `clean/profile.json`, `full/profile.json`, and `profile.json`. |
| **Typographic em-dash Audit** | Regex search `\u2014` across all `.md` files | **PASS** | 100% clean; zero em-dashes across all files in `agy-pro`. |
| **Tool Mapping Integrity** | Verification of agy tool names in all agents | **PASS** | Verified tools: `view_file`, `write_to_file`, `replace_file_content`, `run_command`, `search_web`, `read_url_content`, `generate_image`. |
| **Headless Verification Rule** | Inspection of QA, Designer, and Rule files | **PASS** | Enforced across all agents: user desktop is strictly off-limits. |
| **Secret Protection** | Inspection of registry models & configs | **PASS** | Only environment variable names stored; zero raw credentials present. |

---

## 4. Artifact Directory Map

```text
agy-pro/
├── docs/
│   └── rules-conflicts.md
├── profiles/
│   ├── profile.json (Root profile catalog)
│   ├── clean/
│   │   ├── profile.json
│   │   └── skills/
│   │       └── mode/
│   │           └── SKILL.md
│   └── full/
│       ├── profile.json
│       ├── rules/
│       │   ├── AGENTS.md
│       │   ├── GEMINI.md
│       │   └── agents-index.md
│       ├── agents/
│       │   ├── architect/AGENT.md
│       │   ├── blind-qa/AGENT.md
│       │   ├── code-reviewer/AGENT.md
│       │   ├── debug-specialist/AGENT.md
│       │   ├── designer/AGENT.md
│       │   ├── readiness-auditor/AGENT.md
│       │   ├── release-inspector/AGENT.md
│       │   ├── research/AGENT.md
│       │   └── security-reviewer/AGENT.md
│       └── skills/
│           └── mode/
│               └── SKILL.md
```
