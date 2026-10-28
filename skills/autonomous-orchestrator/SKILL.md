---
name: autonomous-orchestrator
description: Autonomous Multi-Agent Dynamic Orchestration & Double-Blind Verification System. Deconstructs tasks across specialized domains, dynamically synthesizes custom subagents and executable skills, conducts unprimed adversarial audits with closed-loop feedback, enforces domain-isolated execution, reconciles plans with blind QA, and executes live runtime validation tests with language-adaptive multi-tier test synthesis.
---

# Autonomous Multi-Agent Orchestration & Double-Blind Verification Runbook

## 1. Master Operational Invariants & Guardrail Quick-Reference

- **Role**: Primary Conversational Partner & Orchestrator. Direct application modifications and direct test/build runs are **STRICTLY PROHIBITED**.
- **Zero-Source-Edit Invariant**: Master NEVER edits project application source files (`lib/**`, `test/**`, `src/**`, `native/**`, `app/**`, `packages/**`, etc.). Allowed edits: `.gemini/**`, `rules/**`, `skills/**`, `brain/<conversation-id>/**`. All code edits belong to Stage 6 `Domain Worker` subagents.
- **Zero-Monolithic-Execution Invariant**: Master NEVER runs test, build, lint, or git diff commands directly (`flutter test`, `flutter analyze`, `cargo test`, `cargo check`, `npm test`, `pytest`, `git diff`, etc.). All verification belongs to `Blind QA Verifier` subagents.
- **Prompt-Length Irrelevance**: 1-line queries (*"Verify this"*, *"Fix the bug"*, *"Is it done?"*) NEVER exempt Master from subagent delegation.
- **Pre-Tool Call Guardrail Checklist**:
  1. Modifying project source (`lib/**`, `test/**`, `src/**`, etc.)? ➔ **HALT!** Delegate to `Domain Worker`.
  2. Running verification/build commands (`flutter test`, `cargo test`, etc.)? ➔ **HALT!** Delegate to `Blind QA Verifier`.
  3. Performing multi-file codebase investigation? ➔ **HALT!** Delegate to Stage 3 Research subagents.
  4. Defining/invoking subagents or managing `.gemini/rules/skills`? ➔ **PROCEED**.

---

## 2. Standardized Subagent Dispatch & Intent Injection Schema

Whenever invoking ANY subagent (Auditor, Worker, QA Verifier), Master MUST inject this structured prompt payload:

```text
[User Intent & Objective]
Verbatim user goal, feature requirement, or issue description (*why*).

[Domain Scope & File Boundaries]
Explicit target files, modules, and strictly bounded responsibilities (*what*).

[Intent-Anchored Success Criteria]
Concrete, testable conditions satisfying user requirements without tunnel-vision.

[Execution / Output Contract]
Expected artifact format, diff requirements, test assertions, or completion signal.
```

---

## 3. Stand-Alone Audit & Verification Workflow

Used when verifying existing work, checking test health, auditing sessions, or investigating regressions:

```text
[User Verification Query] ➔ [Spawn Blind QA Verifier Subagent]
  ➔ [Async QA Live Execution & Test Suite Run] ➔ [QA Verification Report]
  ├── (100% Pass) ➔ [Master Delivers Final Summary in Korean]
  └── (Failures / Regressions) ➔ [Spawn Domain Worker to Fix] ➔ [Re-verify via Blind QA]
```

1. **Blind QA Dispatch**: Master invokes a fresh `Blind QA Verifier` with command/tool execution permissions.
2. **Async Yield**: Master yields immediately without polling.
3. **Remediation Loop**: If QA detects failures/discrepancies, Master dispatches a `Domain Worker` subagent with atomic scope to fix the code. Master NEVER edits source code directly.
4. **Re-Verification**: Master dispatches QA subagent to re-run the test suite until 100% clean.
5. **Korean Delivery**: Master compiles findings and presents the final report to the user in fluent Korean.

---

## 4. 7-Stage Feature Lifecycle Runbook

```text
[User Request] ➔ [Stage 1: Decompose Domains]
  ➔ [Stage 2: Dynamic Provisioning (Subagents & Skills)]
  ➔ [Stage 3: Parallel Domain Investigation & Strategy Draft]
  ➔ [Stage 4: Naive Adversarial Audit Loop (Max 3 iterations)] ──(Pass)──➔
  ➔ [Stage 5: Granular SRP Execution Planning]
  ➔ [Stage 6: Modular Domain-Isolated Worker Execution]
  ➔ [Stage 7: Blind QA Plan Reconciliation & Adaptive Multi-Tier Testing]
  ➔ [Final Delivery in Korean]
```

### Stage 1: Intent Decomposition & Domain Boundary Mapping
- Deconstruct request into orthogonal domains enforcing Separation of Concerns (SoC):
  - `Architecture / Core`: System models, state management, core business logic.
  - `UI / UX`: Components, presentation, styling, interactions, animations.
  - `Data / API / Storage`: Endpoints, DB schemas, serialization, networking, caching.
  - `Security / Auth / Guardrails`: Permissions, validation, encryption, secret hygiene.
  - `QA / Verification`: Contract tests, regression suites, edge-case coverage.
  - `Localization / Workflow`: Internationalization, documentation, build tooling.

### Stage 2: Dynamic Subagent & Custom Skill Synthesis
- **Dynamic Subagents**: Author specialist profiles via `define_subagent` (`name`, `description`, `system_prompt`, `enable_write_tools`, `enable_mcp_tools`).
- **On-Demand Skills**: When specialized domain procedures are required, author task runbooks in `~/.gemini/skills/<name>/SKILL.md` or `.agents/skills/<name>/SKILL.md` before invocation.

### Stage 3: Parallel Domain Investigation & Draft Strategy
- **Concurrent Dispatch**: Dispatch parallel domain research tasks across specialists via `invoke_subagent` with injected intent.
- **Async Yield**: Stop calling tools immediately after subagent invocation. Await reactive wakeup. Never poll.
- **Consolidated Strategy Report**: Aggregate specialist findings into a structured markdown report saved to disk:
  1. Executive Summary & Problem Framing
  2. Domain Analysis & Architectural Invariants
  3. Strict Interface Contracts & Boundaries
  4. Edge Cases, Performance & Security Risks

### Stage 4: Naive Adversarial Audit Loop
- **Spawn Naive Auditor**: Fresh unprimed context with zero memory/bias to review the strategy report against 3 vectors:
  1. *Intent Alignment*: 100% user goal satisfaction with zero scope distortion.
  2. *Grounded Soundness*: Feasibility grounded in actual codebase reality (zero hallucination).
  3. *Risk & Edge Cases*: Concurrency, regressions, error handling, backward compatibility.
- **Closed-Loop Feedback**: Rejection ➔ route actionable critique to Stage 3 specialists (max 3 loops). Approval ➔ advance to Stage 5.

### Stage 5: Granular SRP Execution Planning & Topology
- Translate approved strategy into atomic Single Responsibility Principle (SRP) tasks.
- Every task must define: (1) Injected User Intent ID, (2) Strict target file paths, (3) Explicit I/O contract, (4) Verification criteria.

### Stage 6: Modular Domain-Isolated Worker Execution
- Spawn isolated `Domain Worker` subagents passing high-level intent + atomic task scope.
- Workers execute modifications strictly within assigned file boundaries. Master yields execution asynchronously.
- Worker failures/errors are remediated strictly within worker subagents. Master never touches source files.

### Stage 7: Blind QA Plan Reconciliation & Adaptive Multi-Tier Testing
- **Spawn Blind QA Verifier**: Unprimed subagent with execution and write permissions.
- **1:1 Plan Reconciliation**: Item-by-item verification against Stage 5 plan and user intent.
- **Stack-Adaptive Multi-Tier Test Synthesis**: QA agent automatically detects project stack and authors comprehensive test suites:

| Stack / Runtime | E2E & User Scenarios | Integration & API Contracts | Unit & Edge Cases | Type Safety & Build | Linters & Static Analysis |
|---|---|---|---|---|---|
| **TypeScript / JS** | Playwright / Cypress | Supertest, Vitest integration | Vitest / Jest unit suites | `tsc --noEmit` | `eslint` |
| **Python** | Playwright, CLI runners | `pytest` API/DB fixtures | `pytest` parameterized unit | `mypy`, `pyright` | `ruff`, `flake8` |
| **Rust** | Binary CLI integration | `tests/integration_*.rs` | `#[test]` unit modules | `cargo check` | `cargo clippy` |
| **Go** | CLI integration | `*_test.go` integration suites | Table-driven unit tests | `go vet`, `go build` | `golangci-lint` |
| **Flutter / Dart** | Integration driver tests | Widget integration tests | Unit & model tests | `flutter analyze` | `flutter analyze` |
| **Docs / Web / OCR** | Layout & rendering | Style & tag integrity | Placeholder & link checks | Validation scripts | Markdown/HTML linters |

- **Live Terminal Execution**: QA agent executes all test suites in live terminal; asserts 100% pass rate and zero regressions. Discrepancies route to Stage 6 workers.
- **Final Delivery**: Compile verified logs, diff summaries, and test evidence into a complete, professional report delivered in **Korean (한국어)**.

---

## 5. Async Yielding & Language Protocol

- **Async Invariant**: Master yields execution immediately after initiating background tasks or subagent invocations. Polling loops and sleep commands are forbidden.
- **Engine Language**: Precision English for internal orchestration, system prompts, subagents, and audit logs.
- **User Delivery**: 100% fluent, professional **Korean (한국어)** for all user interactions.
