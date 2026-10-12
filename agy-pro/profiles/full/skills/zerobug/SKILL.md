---
name: zerobug
description: >
  Deterministic static analysis and diagnostic zeroing runbook. Systematically resolves 100% of errors, warnings, and linter findings via serialized issue-by-issue remediation loops with zero ignore/suppression workarounds.
---

# Zerobug Runbook — Deterministic Diagnostic Zeroing

## 1. Operational Invariants & Zero-Ignore Law
- **Zero Ignore Workarounds**: NEVER bypass diagnostics using suppression comments (`// @ts-ignore`, `// ignore`, `# noqa`, `eslint-disable`). Fix every issue at the root cause.
- **Serialized Single-Target Remediation**: Never batch-edit multiple files simultaneously. Select and fix exactly ONE issue in ONE file per cycle.
- **Continuous Local Verification**: Re-run the diagnostic command immediately after each edit to confirm resolution before proceeding.

## 2. Phase 1: Diagnostic Scan & Listing
1. Run project diagnostic commands (`npm run lint`, `tsc --noEmit`, `cargo check`, `flutter analyze`, `pytest`).
2. Collect and parse all error, warning, and lint entries.
3. Sort issues by complexity, prioritizing straightforward lint/type warnings first.

## 3. Phase 2: Serialized Single-Target Remediation Loop
For each diagnostic item:
1. **Target Selection**: Pick the single file and issue under remediation.
2. **Standard Code Correction**: Apply clean, idiomatic code correction without suppressing rules.
3. **Local Diagnostic Verification**: Execute the specific diagnostic check against the modified file.
4. **Incremental Commit or Rollback**:
   - Issue resolved cleanly: Commit with Conventional Commits (`fix: resolve lint/warning in <file>`).
   - Issue unresolved or cascades into new errors: Instantly rollback (`git checkout -- <file>`) and log the blocker.
5. **Loop**: Move to the next item in the register.

## 4. Phase 3: Final Clean-Slate Verification Pass
1. Execute the full project build and analysis suite across the entire workspace.
2. Confirm total diagnostic count is strictly 0.
3. Present completion summary with total resolved issues and commit history to the user.
