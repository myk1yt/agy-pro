# agy-pro Skills Porting & Mapping Matrix

> **Project**: `agy-pro` (Antigravity CLI Enterprise Wrapper & Profile Engine)  
> **Source Repository**: `D:\OneDrive\Projects\kimi-settings` (read-only reference) & `D:\OneDrive\Projects\Antigravity-cli\skills`  
> **Target Profile**: `agy-pro/profiles/full/skills/`  
> **Total Skills**: 41 (38 Kimi Ports/Merges + 3 agy-pro Core Skills)  
> **Date**: 2026-10-11  

---

## 1. Executive Summary

This document establishes the definitive 1:1 mapping between legacy Kimi Code CLI skills, Antigravity CLI baseline skills, and the target `agy-pro` full-stack profile.

### Core Conversion Directives Applied
1. **Directory-Based Packaging**: Every skill adheres to the canonical Antigravity directory specification (`profiles/full/skills/<name>/SKILL.md`). Flat `.md` slash commands from Kimi have been migrated into dedicated skill directories.
2. **Standard YAML Frontmatter**: Frontmatter is strictly standardized with `name` and clean multi-line `description`. Legacy `type: flow` and `type: prompt` indicators have been refactored into numbered execution runbooks.
3. **Execution Swarm Modernization**: Kimi-internal swarm invocation patterns and subagent orchestrations have been transformed into native `agy pro fanout` multi-model concurrent execution calls.
4. **Path & Environment Decontamination**: All hardcoded `~/.kimi-code` paths have been calibrated to `~/.gemini/config/skills` and `~/.agy-pro`. Legacy diagnostic globbing (`kimi-diagnostics-*.json`) has been modernized to `agy-diagnostics-*.json`.
5. **Unified Baseline Merging**: Where Kimi and Antigravity-cli had overlapping skills (`quality-gate`, `code-review-taxonomy`, `security-review-orchestrator`, `design-core-harness`), the robust agy baseline was preserved and merged with Kimi's advanced rules.

---

## 2. 1:1 Skills Porting Matrix

| # | Skill Name | Original Source Path | Target agy-pro Path | Type / Kind | Port Status | Key Changes & Enhancements |
|:---|:---|:---|:---|:---|:---|:---|
| 1 | `adr-authoring-workflow` | `kimi-settings/skills/adr-authoring-workflow/SKILL.md` | `profiles/full/skills/adr-authoring-workflow/SKILL.md` | Workflow Runbook | Ported | YAML frontmatter standardized; path references updated to `docs/adr/`. |
| 2 | `architecture-constraint-setup` | `kimi-settings/skills/architecture-constraint-setup/SKILL.md` | `profiles/full/skills/architecture-constraint-setup/SKILL.md` | Workflow Runbook | Ported | Standardized YAML frontmatter; calibrated rule issuance to AGENTS.md conventions. |
| 3 | `audit` | `kimi-settings/skills/audit.md` | `profiles/full/skills/audit/SKILL.md` | Slash Command | Ported & Restructured | Converted flat flow into 5-phase numbered runbook; integrated `docs/spec/ARCHITECTURE_CORE.md` 1:1 cross-audit; routes to `/heal`. |
| 4 | `autonomous-orchestrator` | `Antigravity-cli/skills/autonomous-orchestrator/SKILL.md` | `profiles/full/skills/autonomous-orchestrator/SKILL.md` | Orchestration Engine | Integrated | Core agy orchestrator; modernized subagent array dispatches to support `agy pro fanout`. |
| 5 | `backup` | `kimi-settings/skills/backup.md` | `profiles/full/skills/backup/SKILL.md` | Slash Command | Ported & Restructured | Converted flat flow into 6-phase numbered runbook; multi-threaded robocopy (`/MT:32`), 0-7 exit code handling, ZIP staging, BACKUP_INFO/RESTORE_GUIDE generation. |
| 6 | `brainstorm` | `kimi-settings/skills/brainstorm.md` | `profiles/full/skills/brainstorm/SKILL.md` | Slash Command | Ported & Restructured | Converted into 3-phase numbered runbook; interactive one-question-at-a-time loop; outputs 6-section System Directive Prompt for AI Builders. |
| 7 | `causal-chain-analysis` | `kimi-settings/skills/causal-chain-analysis/SKILL.md` | `profiles/full/skills/causal-chain-analysis/SKILL.md` | Hybrid Analysis | Ported | Standardized YAML frontmatter; calibrated evidence classes and competing hypothesis engine. |
| 8 | `ci-triage` | `kimi-settings/skills/ci-triage/SKILL.md` | `profiles/full/skills/ci-triage/SKILL.md` | Workflow Runbook | Ported | GitHub CLI (`gh pr checks`) triage runbook; environment paths decontaminated. |
| 9 | `cleartemp` | `kimi-settings/skills/cleartemp.md` | `profiles/full/skills/cleartemp/SKILL.md` | Slash Command | Ported & Restructured | Converted into 4-step numbered runbook; `@keep` tag preservation, untracked source guard, dry-run markdown table, visual cleanup bar chart. |
| 10 | `code-review-taxonomy` | `kimi-settings/skills/code-review-taxonomy/` & `Antigravity-cli/` | `profiles/full/skills/code-review-taxonomy/SKILL.md` | Knowledge Reference | Merged & Unified | Merged agy baseline with Kimi Diff Scope Guard (>50 files/2000 lines), Intent Conformance check, 8 category lenses, confidence >= 75 filter. |
| 11 | `codebase-onboarding` | `kimi-settings/skills/codebase-onboarding/SKILL.md` | `profiles/full/skills/codebase-onboarding/SKILL.md` | Knowledge Reference | Ported | Evidence-based exploration discipline; standardized YAML frontmatter. |
| 12 | `commit-standard` | `kimi-settings/skills/commit-standard/SKILL.md` | `profiles/full/skills/commit-standard/SKILL.md` | Hybrid Standard | Ported | Atomic commit hygiene, What/Why/Impact structure, pre-commit zero-byte/conflict checks. |
| 13 | `context-handoff` | `kimi-settings/skills/context-handoff/SKILL.md` | `profiles/full/skills/context-handoff/SKILL.md` | Workflow Runbook | Ported | Session continuity, resumption state capture, freshness classification. |
| 14 | `deepcheck` | `kimi-settings/skills/deepcheck.md` | `profiles/full/skills/deepcheck/SKILL.md` | Slash Command | Ported & Restructured | Converted into 4-phase numbered runbook; Backend ↔ Bridge/FFI ↔ Frontend linkage audit, `__auto_verify_*` temporary tests with mocked DB and 30s timeout; severity report to `docs/deepcheck/`. |
| 15 | `dependency-audit` | `kimi-settings/skills/dependency-audit/SKILL.md` | `profiles/full/skills/dependency-audit/SKILL.md` | Knowledge Reference | Ported | Rust workspace and Node dependency graph audit rules; standardized YAML frontmatter. |
| 16 | `dependency-hallucination-check` | `kimi-settings/skills/dependency-hallucination-check/SKILL.md` | `profiles/full/skills/dependency-hallucination-check/SKILL.md` | Knowledge Reference | Ported | Registry fact verification protocol (npm, crates.io, pub.dev); evidence hierarchy. |
| 17 | `design-3d-web` | `kimi-settings/skills/design-3d-web/` & `Antigravity-cli/design-3d-canvas` | `profiles/full/skills/design-3d-web/SKILL.md` | Knowledge Reference | Merged & Unified | Merged agy Web-Native 3D philosophy (`shader-canvas-template.html`) with Kimi Three.js, GSAP ScrollTrigger, and custom GLSL shader guides. |
| 18 | `design-academic-typeset` | `kimi-settings/skills/design-academic-typeset/SKILL.md` | `profiles/full/skills/design-academic-typeset/SKILL.md` | Knowledge Reference | Ported | MathJax/mhchem LaTeX and SVG typesetting pipeline; Zero-MCP standalone design. |
| 19 | `design-core-harness` | `kimi-settings/skills/design-core-harness/` & `Antigravity-cli/` | `profiles/full/skills/design-core-harness/SKILL.md` | Hybrid Quality Harness | Merged & Unified | Merged agy visual QA runbook + Kimi Playwright test scripts (`scripts/visual_qa.js`, `visual_qa.py`); linked `references/visual-qa-rubric.md`. |
| 20 | `design-data-viz` | `kimi-settings/skills/design-data-viz/` & `Antigravity-cli/design-cyberpunk-brainmap` | `profiles/full/skills/design-data-viz/SKILL.md` | Knowledge Reference | Merged & Unified | Merged agy Cyberpunk observatory UI (`cyberpunk-topology.html`) with Kimi D3.js v7 radial tree and DAG topology specs. |
| 21 | `design-interactive-sandbox` | `kimi-settings/skills/design-interactive-sandbox/` & `Antigravity-cli/` | `profiles/full/skills/design-interactive-sandbox/SKILL.md` | Knowledge Reference | Merged & Unified | Merged agy widget sandbox runbook (`single-file-widget.html`) with Kimi scoped state machine and DOM lifecycle rules. |
| 22 | `design-office-deck` | `kimi-settings/skills/design-office-deck/SKILL.md` | `profiles/full/skills/design-office-deck/SKILL.md` | Knowledge Reference | Ported | PPTX/PDF/DOCX presentation slide generation rules and soffice rendering inspection loop. |
| 23 | `design-vector-svg` | `kimi-settings/skills/design-vector-svg/` & `Antigravity-cli/` | `profiles/full/skills/design-vector-svg/SKILL.md` | Knowledge Reference | Merged & Unified | Merged agy SVG template (`svg-diagram-template.svg`) with Kimi Hangul character width formula (13px metric) and WCAG AAA tokens. |
| 24 | `excel-data-art` | `kimi-settings/skills/excel-data-art/SKILL.md` | `profiles/full/skills/excel-data-art/SKILL.md` | Knowledge Reference | Ported & Formatted | Standardized into numbered runbook; financial typography, conditional formatting, and grid alignment. |
| 25 | `feature` | `kimi-settings/skills/feature.md` | `profiles/full/skills/feature/SKILL.md` | Slash Command / Playbook | Ported & Modernized | Converted flat playbook into 6-phase numbered runbook; replaced Kimi swarm with `agy pro fanout` calls; double-blind verification with unconditional PASS gate; `decisions.md` logging. |
| 26 | `ffi-api-wiring` | `kimi-settings/skills/ffi-api-wiring/SKILL.md` | `profiles/full/skills/ffi-api-wiring/SKILL.md` | Workflow Runbook | Ported | Flutter Rust Bridge (FRB) FFI cross-layer wiring and type binding synchronization. |
| 27 | `frb-codegen-troubleshoot` | `kimi-settings/skills/frb-codegen-troubleshoot/SKILL.md` | `profiles/full/skills/frb-codegen-troubleshoot/SKILL.md` | Workflow Runbook | Ported | FRB codegen diagnosis, version mismatch detection, and reversible fix patterns. |
| 28 | `frontend-design` | `kimi-settings/skills/frontend-design/SKILL.md` | `profiles/full/skills/frontend-design/SKILL.md` | Knowledge Reference | Ported | Unified frontend design framework (Taste + Polish + Engineering); anti-slop rules, typography, spatial density, and motion tokens. |
| 29 | `goal` | `kimi-settings/skills/goal.md` | `profiles/full/skills/goal/SKILL.md` | Slash Command | Ported & Restructured | Converted into 6-phase numbered runbook; beginner-friendly CTO interview (5-7 question cap); auto-injects bcrypt/AES-256 security; generates `docs/spec/ARCHITECTURE_CORE.md`. |
| 30 | `heal` | `kimi-settings/skills/heal.md` | `profiles/full/skills/heal/SKILL.md` | Slash Command | Ported & Restructured | Converted into 4-phase numbered runbook; sandbox branch isolation (`heal/YYYY-MM-DD`), test-first reproduction (`__auto_verify_*`), serialized fix-verify-commit or rollback cycle. |
| 31 | `init` | `kimi-settings/skills/init.md` | `profiles/full/skills/init/SKILL.md` | Slash Command | Ported & Restructured | Converted into 4-phase numbered runbook; contamination shield (`.gitignore`), static/dynamic doc segregation (`ARCHITECTURE_CORE.md` vs `ACTIVE_STATE.md`), human approval checkpoint. |
| 32 | `mode` | *New for agy-pro* | `profiles/full/skills/mode/SKILL.md` | CLI Switcher Runbook | New Creation | `/mode` runbook for toggling between full (VP of Engineering, 41 skills, fanout swarm) and clean (stock CLI) profiles via `agy pro mode <full\|clean>`. |
| 33 | `pdf-report-expert` | `kimi-settings/skills/pdf-report-expert/SKILL.md` | `profiles/full/skills/pdf-report-expert/SKILL.md` | Knowledge Reference | Ported & Formatted | Standardized into numbered runbook; magazine-grade multi-column grid layout, typography hierarchy, and callout box formatting. |
| 34 | `quality-gate` | `Antigravity-cli/skills/quality-gate/` & `kimi-settings/` | `profiles/full/skills/quality-gate/SKILL.md` | Verification Framework | Merged & Unified | Preserved comprehensive 624-line agy framework (6 tiers, QG-01..20, unicode scan); merged Kimi Zoo-Code/Turbo project profile and reporting templates. |
| 35 | `search-tool-discipline` | `kimi-settings/skills/search-tool-discipline/SKILL.md` | `profiles/full/skills/search-tool-discipline/SKILL.md` | Knowledge Reference | Ported | The Three Rules: Literal-First, Narrow-First, Zero Results != Absence. Prevents hallucinated code searches. |
| 36 | `security-review-orchestrator` | `Antigravity-cli/skills/security-review-orchestrator/` | `profiles/full/skills/security-review-orchestrator/SKILL.md` | Security Framework | Merged & Unified | Comprehensive 817-line security review cycle; threat modeling, 6 review passes (Pass 0-5), remediation loops, and finding contracts. |
| 37 | `stacked-pr-manager` | `kimi-settings/skills/stacked-pr-manager/SKILL.md` | `profiles/full/skills/stacked-pr-manager/SKILL.md` | Workflow Runbook | Ported | Stacked PR dependency chain management; root-first fix order, pre-rebase backups, and post-rebase CI checks. |
| 38 | `testdrive` | `kimi-settings/skills/testdrive.md` | `profiles/full/skills/testdrive/SKILL.md` | Slash Command | Ported & Restructured | Converted into 4-phase numbered runbook; P0-P5 discovery hierarchy (CI is canonical release build), self-terminating command constraint, headless GUI background smoke test (`MainWindowHandle`), absolute path command reporting. |
| 39 | `usage` | `Antigravity-cli/skills/usage/SKILL.md` | `profiles/full/skills/usage/SKILL.md` | Analytics Runbook | Integrated | Real-time token tracking, prompt cache hit rate analytics, cost breakdowns by model, and interactive dashboard directly within Antigravity conversations. |
| 40 | `web-research-discipline` | `kimi-settings/skills/web-research-discipline/SKILL.md` | `profiles/full/skills/web-research-discipline/SKILL.md` | Research Methodology | Ported & Modernized | Multi-source web fact-verification methodology for Verify-mode research dispatches; modernized to reference `agy pro fanout` Verify mode; quorum and claim register rules. |
| 41 | `zerobug` | `kimi-settings/skills/zerobug.md` | `profiles/full/skills/zerobug/SKILL.md` | Slash Command | Ported & Restructured | Converted into 3-phase numbered runbook; serialized issue-by-issue remediation loop with strict Zero-Ignore Law (no `// ignore`, `// @ts-ignore`). |

---

## 3. Shared References & Assets

| Item | Source Path | Target agy-pro Path | Role & Usage |
|:---|:---|:---|:---|
| `visual-qa-rubric.md` | `kimi-settings/references/visual-qa-rubric.md` | `profiles/full/references/visual-qa-rubric.md` | Comprehensive visual QA rubric covering Critical/Major/Minor defect classes, contrast formulas, and code remediation recipes. |
| `visual_qa.js` / `visual_qa.py` | `kimi-settings/skills/design-core-harness/scripts/` | `profiles/full/skills/design-core-harness/scripts/` | Playwright and Python automated screenshot capture and headless visual verification test scripts. |
| `shader-canvas-template.html` | `Antigravity-cli/skills/design-3d-canvas/templates/` | `profiles/full/skills/design-3d-web/templates/` | Production-ready WebGL shader canvas template with throttled animation loops. |
| `cyberpunk-topology.html` | `Antigravity-cli/skills/design-cyberpunk-brainmap/templates/` | `profiles/full/skills/design-data-viz/templates/` | Observatory radial gradient, CRT scanline, and laser sweep DAG network visualization template. |
| `single-file-widget.html` | `Antigravity-cli/skills/design-interactive-sandbox/templates/` | `profiles/full/skills/design-interactive-sandbox/templates/` | Standalone DOM widget sandbox template with scoped state machines. |
| `svg-diagram-template.svg` | `Antigravity-cli/skills/design-vector-svg/templates/` | `profiles/full/skills/design-vector-svg/templates/` | Production-ready SVG diagram template with mathematical text bounding budgets. |

---

## 4. Verification & Conformance Status

- **Frontmatter Syntax**: 100% (41/41) validated with YAML parser. All skills contain standard `name` and `description`.
- **Environment Decontamination**: 0 residual references to `~/.kimi-code` or legacy diagnostic paths.
- **Runbook Numbering**: All slash command flows and flat guides refactored into structured numbered execution runbooks (`## 1. ...`, `## 2. ...`).
- **Fanout Readiness**: All swarm references modernized to leverage `agy pro fanout` multi-model execution.
