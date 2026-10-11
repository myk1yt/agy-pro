---
name: deepcheck
description: >
  Autonomous full-stack project health verification runbook. Cross-audits static architecture documents, backend-bridge-frontend linkages, and executes autonomous proof via short-lived temporary __auto_verify_* tests with mocked databases and strict 30s timeouts.
---

# Deepcheck Runbook — Autonomous Full-Stack Health Audit

## 1. Operational Invariants
- **Sequential Scanning**: Traverse directories sequentially (FFI/Bridge -> Backend -> Frontend -> Tests). Avoid uncoordinated parallel scans.
- **Single-Target Test Loop**: In Phase 3, test exactly ONE module at a time: Write test -> Execute test -> Delete test. Never leave dangling test files.
- **Zero Production Mutation**: Audit is non-destructive. Never modify application source code during verification.

## 2. Phase 1: Sync & Documentation Drift Verification
1. Scan workspace structure and compare against claims in `docs/spec/ARCHITECTURE_CORE.md` (or `docs/ARCHITECTURE_CORE.md`).
2. If drift or undocumented additions exist, run `/init` synchronization flow.

## 3. Phase 2: Full-Stack Linkage Auditing
1. Audit cross-layer boundaries: Backend ↔ Bridge/FFI ↔ Frontend.
2. Identify FFI serialization mismatches, divergent type definitions, unhandled error boundaries, and API contract drifts.
3. Select the top 3 highest-risk linkage points for empirical verification.

## 4. Phase 3: Autonomous Test-Driven Proof
For each of the top 3 high-risk modules:
1. Synthesize a temporary verification test prefixed with `__auto_verify_`.
2. Ensure mocked database usage. Never write to live production databases.
3. Execute the test command with a **strict 30-second timeout**. Kill hanging processes immediately and mark as failed.
4. **Mandatory Cleanup**: Delete the `__auto_verify_` test immediately after execution before proceeding to the next target.

## 5. Phase 4: Severity-Ranked Health Audit Report
1. Compile all test results, execution logs, and current Git commit hash.
2. Write report to `docs/deepcheck/YYYY-MM-DD_deepcheck-report.md`.
3. Categorize findings into 🔴 CRITICAL, 🟠 HIGH, 🟡 MEDIUM, 🟢 LOW.
4. Present summary to the user and offer automated remediation via `/heal`.
