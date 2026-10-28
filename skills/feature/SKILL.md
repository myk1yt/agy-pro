---
name: feature
description: >
  Full-lifecycle enterprise feature orchestration runbook for multi-hour to multi-day builds. Decomposes tasks across domain boundaries, dispatches parallel research, executes unprimed adversarial audits, coordinates isolated coder workers, runs double-blind verification swarms with unconditional PASS gates, and records decisions in decisions.md.
---

# Feature Runbook — Full-Lifecycle Enterprise Orchestration

## 1. Scope Evaluation & Light-Mode Abort Rule
- **Scope Threshold**: Invoked strictly for substantial initiatives (multi-hour to multi-day features, deep refactors). For small bug fixes or localized edits, proceed directly without heavy ceremonial overhead.
- **Light-Mode Abort**: If the user requests lightweight execution ("가볍게 해", "간단히 해", "대충 해줘", "빠르게만"), immediately abort this playbook, transition to conversational agile mode, and inform the user in one sentence.

## 2. Standardized Intent Injection Schema
Every subagent dispatch (Auditor, Coder, Verifier) must include this four-part payload:
- **[User Intent & Objective]**: Verbatim user goal and the underlying motivation (*why*).
- **[Domain Scope & File Boundaries]**: Explicit allowed files and bounded module responsibilities (*what*).
- **[Intent-Anchored Success Criteria]**: Testable, concrete conditions satisfying requirements without tunnel-vision.
- **[Execution / Deliverable Contract]**: Output format, diff requirements, test assertions, and completion signals.

## 3. Phase 1: Domain Decomposition & Parallel Research
1. Deconstruct the feature across architectural boundaries (Core/Architecture, UI/Design, Data/API, Security, QA).
2. Dispatch parallel research subagents via `agy pro fanout` or multi-agent invocation to investigate existing patterns and dependencies.
3. Consolidate findings into a comprehensive design document on disk: Intent, Domain Analysis, Interface Contracts, and Edge Cases.

## 4. Phase 2: Unprimed Adversarial Plan Audit
1. Spawn a fresh-context adversarial auditor without conversation history. Provide only the plan and original user intent.
2. The auditor evaluates the plan across 3 critical axes:
   - **Intent Conformance**: 100% user objective alignment, zero scope creep or distortion.
   - **Grounding & Factuality**: Verified against actual codebase files, symbols, and APIs (zero hallucination).
   - **Risk & Edge Cases**: Concurrency hazards, migration risks, error handling, backwards compatibility.
3. Decision: `APPROVE` or `REJECT` (with actionable feedback).
4. On `REJECT`: Route critiques back to Phase 1 for plan refinement (max 3 loops; escalate to user if exceeded).

## 5. Phase 3: Atomic SRP Implementation via Isolated Domain Workers
1. Decompose the approved plan into atomic Single Responsibility Principle (SRP) tasks.
2. Dispatch coder workers with strictly disjoint file boundaries.
3. Worker failures must be contained and resolved within the worker's domain scope.

## 6. Phase 4: Double-Blind Verification Swarm
1. Verification agents receive only the specification and success criteria—never the implementer's rationales or transcripts.
2. Dispatch parallel verification swarm via `agy pro fanout`:
   - **Build & Test Verifier**: Executes full test suite, confirms 100% pass rate and zero regressions.
   - **Plan Conformance Verifier**: Audits 1:1 itemized alignment with user requirements.
   - **Edge Case & Security Verifier**: Stresses boundary conditions, error paths, and security attack surfaces.
3. **Unconditional PASS Invariant**: Only an absolute, unconditional PASS is accepted. Any conditional or qualified approval ("Pass but...", "Pass with minor issue...") is strictly treated as **REJECT**.
4. On `REJECT`: Extract required conditions, route back to Phase 3 workers, and re-verify with fresh verifiers until unconditional PASS is achieved.

## 7. Phase 5: Append-Only decisions.md Record Keeping
1. Maintain `decisions.md` in the project root as an append-only executive log.
2. Record every user decision, architecture choice, or scope adjustment: Date, Decision, Rationale, Rejected Alternatives, Scope Impact. Never mutate past entries.

## 8. Phase 6: Evidence-Anchored Final Reporting
Deliver a concise Korean report containing only verified empirical evidence (test logs, diff summaries, audit records). Omit unverified claims.
