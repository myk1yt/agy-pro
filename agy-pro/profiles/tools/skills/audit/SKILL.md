---
name: audit
description: >
  Spec-conformance verification runbook. Cross-verifies implemented code 1:1 against architecture specifications (docs/spec/ARCHITECTURE_CORE.md) from an adversarial verifier perspective to catch missing features, half-done implementations, and patch-jobs.
---

# Audit Runbook — Spec Conformance Interrogation

## 1. Operational Invariants & Adversarial Stance
- **Adversarial Verifier Mindset**: Evaluate the codebase strictly from the perspective of an adversarial auditor. The question is never whether the code compiles or "works", but whether it strictly and completely satisfies the original design specifications.
- **Reference Document Precedence**: Target specification resides at `docs/spec/ARCHITECTURE_CORE.md` (fallback to `docs/ARCHITECTURE_CORE.md`). If neither exists, halt and prompt the user to establish a specification baseline via `/goal` or `/init`.
- **Zero Monolithic Execution**: For large changes or complex multi-module systems, leverage `agy pro fanout` or parallel subagents to interrogate orthogonal modules independently.
- **Read-Only Invariant**: Audit is strictly read-only. Never modify application code directly. All defects are recorded for triage and routed to `/heal`.

## 2. Phase 1: Specification Ingestion & Checklist Extraction
1. Read the architecture specification (`docs/spec/ARCHITECTURE_CORE.md`).
2. Extract an itemized requirements checklist covering:
   - Core functional requirements and APIs
   - User experience and workflow contracts
   - Security policies, authentication, and data encryption
   - Edge cases, error boundaries, and rate limits
   - Definition of Done (DoD) criteria
3. This extracted checklist forms the immutable evaluation baseline for Phase 2.

## 3. Phase 2: 1:1 Adversarial Conformance Cross-Verification
For every checklist item, examine the codebase to locate the exact implementation evidence (`[filepath:line]`).
Actively hunt for four specific defect patterns:
1. **Omission (전무 - Missing)**: Specified in the plan, but completely absent in implementation.
2. **Partial Implementation (반쪽짜리 구현 - Incomplete)**: Functionality partially coded, but error branches, fallback conditions, or sub-features omitted.
3. **Patch-Job (땜질식 코드 - Temporary Workaround)**: Mechanically working code that circumvents architectural boundaries, security policies, or error handling standards.
4. **Specification Distortion (명세 왜곡 - Drift)**: Implementation deviates from the specified intent or architectural paradigm.

## 4. Phase 3: Defect Classification & Severity Triage
Assign severity to every discovered discrepancy:
- 🔴 **CRITICAL**: Core specification omitted or patched around architectural integrity/security invariants.
- 🟠 **HIGH**: Secondary feature missing, error boundary absent, or incomplete workflow.
- 🟡 **MEDIUM**: Minor behavioral deviation, suboptimal data flow, or missing non-critical validation.
- 🟢 **LOW**: Aesthetic discrepancy, comment/doc mismatch, or non-blocking polish.

## 5. Phase 4: Report Generation & Remediation Handoff
1. Write the formal audit report to `docs/audit/YYYY-MM-DD_audit-report.md` (create folder if missing).
2. Report structure:
   - Executive Summary (Total requirements checked, pass rate)
   - Conformance Matrix (Requirement vs Status [PASS / PARTIAL / MISSING] vs Evidence)
   - Severity-Ranked Finding Register with concrete `[filepath:line]` citations and root cause analysis
3. Present a concise summary to the user.
4. If blocking defects (🔴/🟠) exist, guide the user to invoke `/heal` for autonomous test-driven remediation.
