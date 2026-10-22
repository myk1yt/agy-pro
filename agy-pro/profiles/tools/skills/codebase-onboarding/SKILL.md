---
name: codebase-onboarding
description: >
  Rapidly understand an unfamiliar project. Systematically map entry points, dependency graph, data flow, and key abstractions. Language and framework agnostic, works for Rust, Python, JavaScript, Flutter, and any other project.
---

## Core Principles

1. **Evidence Before Labels**: Don't infer architecture from folder names alone. Distinguish: VERIFIED (code/config/command), INFERRED (multiple evidence), UNKNOWN (insufficient).
2. **Repository Before Features**: Understand repo/workspace structure first, then trace execution paths.
3. **Graph, Not Forced Linear**: Don't force User→UI→State→API→Storage. Represent as discovered components and edges.
4. **Read-Only Default**: Don't modify source, dependencies, lockfiles, generated files, or config during onboarding.
5. **Breadth First, Then One Slice**: Shallow understanding of entire repo, then trace one representative flow end-to-end.

## Execution Flow

```
SCOPE → DISCOVER → VERIFY COMMANDS → MAP COMPONENTS → TRACE ONE VERTICAL SLICE
→ IDENTIFY CONTRACTS AND RISKS → MAP TESTS → REPORT EVIDENCE AND UNKNOWNs
```

## Compact Protocol

FIX SCOPE AND REVISION → DISCOVER WORKSPACES, MANIFESTS, AND INSTRUCTIONS → FIND REAL COMMANDS AND ENTRY POINTS → MAP RESPONSIBILITIES AND CONTRACTS → TRACE ONE REPRESENTATIVE FLOW → CONNECT STATE, STORAGE, EXTERNAL BOUNDARIES, AND TESTS → IDENTIFY HIGH-RISK CHANGE AREAS → SEPARATE VERIFIED FACTS FROM INFERENCE → STOP AT SUFFICIENT WORKING UNDERSTANDING → REPORT HOW TO START, CHANGE, AND VERIFY

## Sections

### 1. SCOPE
Record: repository, branch, revision, working_tree, target_area, depth (quick/standard/deep), time_budget. If user specified a package/feature, don't analyze entire repo in detail.

### 2. DISCOVER
Priority: repo instructions/README → workspace/package manifest → lockfile/toolchain/runtime → source/test/generated/infra dirs → build/run/test/lint scripts → CI/container/deployment → config/env/secret refs → schema/migration/API contract → ownership/public boundary.

Classify project as: application, library, CLI, service, worker, plugin, monorepo, mixed-language, generated/vendored.

### 3. VERIFY COMMANDS
Don't trust README commands blindly. Verify: setup/install, build, run, test, lint/type-check, code generation, package-specific commands. Don't run destructive commands/migrations/deployments in default mode.

States: DISCOVERED, VERIFIED, FAILED, BLOCKED.

### 4. MAP COMPONENTS
Center on responsibilities and boundaries. Per component record: name, path, responsibility, entry_points, public_contract, depends_on, consumed_by, state_or_storage, tests, evidence.

Relationship types: runtime call, import/build dependency, API/IPC/FFI, event/queue, data/schema, config, generated-code. Don't confuse package dependency with runtime execution.

### 5. ENTRY POINTS
Don't stop at one main file. Investigate: process/app start, CLI commands, routes/endpoints, background jobs, event consumers, plugin registration, public library exports, test harness, migrations, build/code-gen entries. Record environment and artifact per entry.

### 6. VERTICAL SLICE
Select one representative flow and trace end-to-end. Priority: user-specified feature > core user journey > most important public API > representative example/test > main background workflow.

Trace: trigger → validation → state transition → business logic → boundary crossing → persistence/external effect → success result → error path → related tests. Don't hide branches/callbacks/retry/async in linear arrows.

### 7. PATTERNS AND CONTRACTS
Record only what matters: module boundaries, dependency injection, state ownership, error model, async/concurrency, config loading, serialization/schema, logging/observability, feature flags, code-gen ownership, test organization, naming/formatting, public API compatibility. Don't generalize from one or two cases.

### 8. RISK AND CHANGE GUIDE
Identify caution areas: auth/authz, tenant/data isolation, persistence/migrations, public API, shared core, concurrency, FFI/IPC, generated code, dependency/lockfile, CI/release, platform-specific code, low-coverage critical paths. Connect each risk to "files to read before changing" and "checks to run after changing".

### 9. TEST MAP
Distinguish: unit, integration, contract, E2E, snapshot/golden, benchmark, manual. Verify if the vertical slice's success/failure paths are actually tested. Don't claim unrun tests as passed.

### 10. OUTPUT FORMAT

```markdown
# Codebase Onboarding

## Snapshot
- Repository / revision:
- Project shape:
- Languages and runtimes:
- Primary artifacts:
- Working-tree state:
- Confidence:

## How to Work Here
| Task | Command | Status |

## Component Map
| Component | Responsibility | Entry | Contracts | Tests |

## Entry Points
- [entry]: [purpose and environment]

## Representative Flow
[trigger] → [key components/boundaries] → [effect]

- Success path:
- Error path:
- State/storage:
- Evidence:

## Key Conventions
- Architecture:
- Error handling:
- Async/state:
- Configuration:
- Testing:

## Change Guide
| Area | Start Here | Validate With | Risk |

## Unknowns and Conflicts
## First Safe Task
```

## Exploration Discipline

Don't dump full repo tree. Start with exact filename/symbol/script, widen to workspace when needed. Distinguish generated/vendor/build output but include when investigating. Don't conclude absence from 0 results. Read context of matches. When README/source/CI conflict, prefer executable source and machine-enforced config, but report the conflict.

## Stop Conditions

Stop when you have: repo/workspace type, major components/responsibilities, key entry points, one vertical slice, build/run/test command status, contracts/state/storage, test structure, risk areas, where to start work, important unknowns. Don't try to read every file.

## Completion Criteria

COMPLETE when: revision+scope clear, structure confirmed, commands verified, components+entries have evidence, vertical slice traced in actual code, contracts/tests/risks connected, inference+unknowns stated, uninspected areas not claimed.

States: COMPLETE, COMPLETE_WITH_LIMITATIONS, PARTIAL, BLOCKED.

## Prohibited

- Confirming project structure from a single manifest
- Forcing fixed UI→State→API→Storage flow
- Claiming command success from README alone
- Confusing package dependency with runtime call
- Assuming single entry point
- Inventing architecture patterns
- Treating test file existence as coverage
- Running setup/install/migration/code-gen without user request
- Reading all source files
- Inventing unverified env vars/services/databases
- Hiding unknowns in complete-looking summaries
