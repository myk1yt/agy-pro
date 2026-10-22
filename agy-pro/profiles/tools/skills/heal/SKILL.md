---
name: heal
description: >
  Autonomous test-driven defect remediation runbook. Ingests deepcheck or audit reports, creates isolated sandbox branches (heal/YYYY-MM-DD), enforces test-first reproduction (__auto_verify_* failure proof before edits), and executes serialized fix-verify-commit or rollback cycles.
---

# Heal Runbook — Autonomous Test-Driven Defect Remediation

## 1. Operational Invariants & Branch Isolation
- **Sandbox Branch Isolation**: Before modifying code, checkout a dedicated sandbox branch: `git checkout -b heal/YYYY-MM-DD` (where YYYY-MM-DD is today's date). All remediation must occur strictly within this isolated branch.
- **Serialized Remediation (One-by-One)**: Fix defects strictly one at a time. Never attempt parallel fixes across multiple issues or batch modifications.
- **Autonomous Execution**: Execute full diagnosis and repair autonomously without stalling on interactive approvals for routine fixes.

## 2. Phase 1: Diagnostic Report Ingestion & Priority Triage
1. Locate and read the latest diagnostic report under `docs/deepcheck/` or `docs/audit/`.
2. Extract all findings and sort by severity: 🔴 CRITICAL -> 🟠 HIGH -> 🟡 MEDIUM -> 🟢 LOW.
3. Select the highest priority defect for the first healing cycle.

## 3. Phase 2: Severity-Based Remediation Strategy
- 🔴 **CRITICAL (Architectural Discrepancy / Security Flaw)**:
  - Re-examine interface specifications, update design documents, and align implementation to architectural invariants.
- 🟠 **HIGH (Logic Bug / Unhandled Exception / Build Failure)**:
  - Perform Root Cause Analysis (RCA), trace causality, and apply surgical bug fix.
- 🟡 **MEDIUM / 🟢 LOW (Type Warning / Lint / Polish)**:
  - Apply idiomatic standard code correction.

## 4. Phase 3: Test-Driven Verification Loop
For each selected defect:
1. **Reproduction Test**: Before touching production code, write a temporary reproduction test (`__auto_verify_<issue>.test.*`) using mocked databases. Run it and confirm failure.
2. **Execute Code Fix**: Apply minimal targeted code changes.
3. **Verify Pass**: Execute the reproduction test with a strict 30-second timeout. Confirm 100% pass.
4. **Commit or Rollback**:
   - **PASS**: Promote valuable regression tests to the permanent `test/` suite or clean up temporary files. Commit with Conventional Commits (`fix: ...`).
   - **FAIL / TIMEOUT**: Instantly rollback all changes (`git checkout -- .` or `git reset --hard`). Record failure reason and proceed to the next issue.

## 5. Phase 4: Final Healing Report Generation
1. Write healing summary report to `docs/heal/YYYY-MM-DD_heal-report.md`.
2. Include: Branch name, processed defect outcomes (remediated, failed, rolled back), promoted regression tests, and final build status.
3. Present summary to user and request branch merge confirmation.
