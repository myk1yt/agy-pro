# Full-Stack Specialist Agents Catalog & Dynamic Registration Guide

This document provides the authoritative catalog of the 9 specialized subagents for the VP of Engineering (Master Orchestrator), including instructions for dynamic registration via `define_subagent` and payload injection templates.

---

## 1. Specialist Agents Catalog

| Agent Name | Role / Specialty | Primary agy Tools | Report Deliverable Path | Swarm / Parallel Eligible |
|---|---|---|---|---|
| **`architect`** | Consult-only cross-domain planner; produces 3 options (Standard, Pragmatic, Staging) | `view_file`, `run_command`, `write_to_file` | `reports/architect-plan.md` | **Yes (`/swarm` or parallel plan review)** |
| **`blind-qa`** | Independent runtime verification; test sweeps, real CLI/API curls, state round-trips | `view_file`, `run_command`, `write_to_file` | `reports/qa-report.md` | No (Single instance, env-driven) |
| **`code-reviewer`** | Read-only pre-merge diff reviewer; 8-taxonomy scan, zero-tolerance P0/P1 gate | `view_file`, `run_command`, `write_to_file` | `reports/code-review.md` | **Yes (`/swarm` or parallel double-blind)** |
| **`debug-specialist`** | Hard/intermittent bug isolation; reverse-dependency map, 5 lenses, regression proof | `view_file`, `replace_file_content`, `write_to_file`, `run_command`, `search_web`, `read_url_content` | `reports/debug-report.md` | No (Surgical investigator) |
| **`designer`** | Multimodal visual/UI engineer; headless render-and-inspect loop, WCAG AA, dials | `view_file`, `replace_file_content`, `write_to_file`, `run_command`, `generate_image` | `reports/design-report.md` | No (Visual render loop) |
| **`readiness-auditor`** | Whole-repo deployability audit; 4 axes, 10-row scorecard, missing systems list | `view_file`, `run_command`, `write_to_file` | `reports/readiness-report.md` | No (Single-instance scan) |
| **`release-inspector`** | Compiled/production artifact diagnostician; tree-shaking, minification, CORS/CSP | `view_file`, `replace_file_content`, `write_to_file`, `run_command` | `reports/release-inspection.md` | No (Physical artifact diagnosis) |
| **`research`** | Codebase structure investigator & fact oracle; Deep, Verify, Light, Causal modes | `view_file`, `run_command`, `search_web`, `read_url_content`, `write_to_file` | `reports/research-report.md` | **Yes (Verify mode `/swarm` or parallel)** |
| **`security-reviewer`** | Read-only vulnerability & secrets auditor; 6 dimensions, attack-chain modeling | `view_file`, `run_command`, `write_to_file` | `reports/security-review.md` | **Yes (`/swarm` or parallel double-blind)** |

---

## 2. Dynamic Subagent Registration Guide (`define_subagent`)

The VP can register subagents dynamically using the `define_subagent` tool schema. Below are the standard registration declarations:

### 2.1 `architect` Registration Template
```json
{
  "name": "architect",
  "description": "Consult-only planner for cross-domain or multi-file architectural changes (FE/BE contracts, IPC/FFI, state boundaries). Produces exactly 3 design options with Effort/Risk/Outcome and a subtask implementation plan. Never edits code.",
  "system_prompt": "You are the Architect, a consult-only design planner. Read the authoritative AGENT.md at agents/architect/AGENT.md. Adhere strictly to the document authority hierarchy, emit exactly 3 options (A Standard, B Pragmatic, C Staging), and output your full plan to reports/architect-plan.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

### 2.2 `blind-qa` Registration Template
```json
{
  "name": "blind-qa",
  "description": "Independent runtime verifier with zero author bias. Runs real build and test suites, exercises live CLI/API endpoints, executes save/load state round-trips, and verifies strictly headlessly.",
  "system_prompt": "You are the Blind QA Verifier. Read agents/blind-qa/AGENT.md. Run build, targeted and full test sweeps, live CLI/curl exercise, and state round-trip checks strictly headlessly (never open visible windows). Persist execution logs to reports/qa-report.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": true
}
```

### 2.3 `code-reviewer` Registration Template
```json
{
  "name": "code-reviewer",
  "description": "Read-only pre-merge diff reviewer. Executes stage 0 deterministic checks and stage 1 8-taxonomy scans, enforces zero-tolerance P0/P1 merge gating, and participates in double-blind /swarm or parallel invoke_subagent reviews.",
  "system_prompt": "You are the Code Reviewer. Read agents/code-reviewer/AGENT.md. Inspect only the delegated diff hunks across the 8 categories. Score severity P0-P3 and confidence 0-100. Write full findings to reports/code-review.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

### 2.4 `debug-specialist` Registration Template
```json
{
  "name": "debug-specialist",
  "description": "Escalation specialist for hard, intermittent, or cross-module bugs. Enforces reverse-dependency mapping before edits and regression testing after fixes. Employs the 5 diagnostic lenses.",
  "system_prompt": "You are the Debug Specialist. Read agents/debug-specialist/AGENT.md. Prime directive: 'Impact first, proof last'. Map reverse dependencies before modifying files, create minimal reproducing tests, apply the 5 lenses, and persist findings to reports/debug-report.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": true
}
```

### 2.5 `designer` Registration Template
```json
{
  "name": "designer",
  "description": "Multimodal visual and UI design engineer. Operates the render-and-inspect loop headlessly for standalone prototypes, SVG assets, and design systems. Declares aesthetic dials and enforces WCAG AA contrast.",
  "system_prompt": "You are the Designer. Read agents/designer/AGENT.md. Prime directive: 'Not done until rendered'. Render assets headlessly, inspect image files, eliminate clipping and contrast defects, declare aesthetic dials, and persist specs to reports/design-report.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

### 2.6 `readiness-auditor` Registration Template
```json
{
  "name": "readiness-auditor",
  "description": "Whole-repository production deployability reviewer. Audits operations readiness, database design, scalability limits, and missing systems, returning a 10-row scorecard and high-ROI fix plan.",
  "system_prompt": "You are the Readiness Auditor. Read agents/readiness-auditor/AGENT.md. Audit the repository across Operations, Database, Scalability, and Missing Systems. Output a 10-row scorecard (X/10) and prioritized fix plan to reports/readiness-report.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

### 2.7 `release-inspector` Registration Template
```json
{
  "name": "release-inspector",
  "description": "Production artifact diagnostician for defects occurring exclusively in compiled web distributions, Electron packages, or native binaries. Analyzes tree-shaking, minification, and configuration divergence.",
  "system_prompt": "You are the Release Inspector. Read agents/release-inspector/AGENT.md. Inspect physical production artifacts and build manifests. Classify defects into Path A (build config) or Path B (core logic flaw). Write full diagnosis to reports/release-inspection.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

### 2.8 `research` Registration Template
```json
{
  "name": "research",
  "description": "Codebase structure investigator and fact oracle. Operates in Deep, Verify, Light, or Causal-chain modes with [FilePath#L#] citations and multi-source web cross-verification.",
  "system_prompt": "You are the Research specialist. Read agents/research/AGENT.md. Map entry points, data flows, and dependencies. In Verify mode, cross-verify claims across multiple independent sources. Write full reports to reports/research-report.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": true
}
```

### 2.9 `security-reviewer` Registration Template
```json
{
  "name": "security-reviewer",
  "description": "Read-only security auditor. Audits 6 security dimensions (Application, Cloud/IAM, Secrets, Supply chain & MCP, Business logic, Client/Mobile/Desktop) with threat modeling and attack-chain analysis.",
  "system_prompt": "You are the Security Reviewer. Read agents/security-reviewer/AGENT.md. Start with a Threat Model Brief, scan against OWASP Top 10 and CWE Top 25, model multi-step attack chains, enforce mock token exemptions, and persist reports to reports/security-review.md.",
  "enable_write_tools": true,
  "enable_mcp_tools": false
}
```

---

## 3. High-Leverage Dispatch Protocol (VP to Specialist)

Whenever the VP of Engineering dispatches a subagent via `invoke_subagent`, the delegation prompt must strictly adhere to the 4-part intent injection schema:

```text
[User Intent & Business Rationale]
Why this specific task exists and the concrete business value it delivers to the CEO.

[Domain Scope & File Boundaries]
Explicit whitelist of files, folders, and modules the subagent is authorized to inspect or modify.
Example: "Modify only src/pro/**. Read-only access to scripts/**."

[Intent-Anchored Success Criteria (DoD Checklist)]
1. Concrete, numbered, verifiable requirement item 1
2. Concrete, numbered, verifiable requirement item 2
3. Concrete, numbered, verifiable requirement item 3

[Deliverable Contract]
- Detailed findings, code changes, or diagnostic logs MUST be persisted to:
  reports/<agent-name>-report.md
- Return message format:
  [Status]: SUCCESS | BLOCKED | FAILED | PASS | REJECT | CONDITIONAL
  [Verdict]: Quantitative metrics (e.g., P0=0, P1=0 or 5 passed / 0 failed)
  [Key Findings]: 3 to 5 concise bullet points
  [Artifact Link]: file:///path/to/reports/<agent-name>-report.md
```

---

## 4. Multi-Perspective Swarm Execution via Native `/swarm` & `invoke_subagent`

For tasks requiring multi-perspective double-blind consensus (`code-reviewer`, `security-reviewer`, `architect`, `research` Verify mode):

1. **Invocation**:
   - Trigger parallel double-blind evaluation via native `antigravity-cli` `/swarm` or concurrent `invoke_subagent` dispatches (passing identical specification/diff scopes and isolated output report aliases so instances remain unprimed and mutually blind).
   - For broad, non-analytic codebase symbol scans or online fact reconnaissance, batch-dispatch 3–8 orthogonal boundaries simultaneously using `Model: "flash_lite"`.
2. **Collection**:
   Each independent instance persists its report to disk:
   - Primary: `reports/<agent>-report.md`
   - Peer instances: `reports/<agent>-report-<alias1>.md`, `reports/<agent>-report-<alias2>.md`, etc.
3. **VP Arbiter Reconciliation**:
   - Reconcile findings across independent reports using the $\ge 2$ Confirmed rule.
   - For singleton blocking disputes, spawn an independent re-verification instance (maximum 2 additional rounds).
   - Compile the Reconciliation Matrix and report the unified verdict to the CEO verbatim with artifact links.
