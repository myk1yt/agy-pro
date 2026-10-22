---
name: init
description: >
  Workspace onboarding and foundational documentation runbook. Deploys contamination shields (.gitignore), performs precision architecture scans, segments static and dynamic documentation (ARCHITECTURE_CORE.md vs ACTIVE_STATE.md), and validates with human approval.
---

# Init Runbook — Workspace Onboarding & Architecture Baseline

## 1. Operational Invariants & Contamination Shield
- **Contamination Shield**: Before scanning, inspect and patch `.gitignore`. Ensure build artifacts, lockfiles, minified bundles, and vendor folders (`dist/`, `build/`, `node_modules/`, `.next/`, `target/`) are excluded to avoid context contamination.
- **Context Hygiene**: Never dump raw directories into context. Read directory trees progressively.

## 2. Phase 1: Precision Codebase & Stack Scan
1. Scan directory structure, manifests (`package.json`, `Cargo.toml`, `go.mod`), entry points, and communications protocols.
2. Identify core technologies, framework versions, database integrations, and FFI/IPC bridges.

## 3. Phase 2: Segmented Architecture Documentation
Document findings into two strictly segregated files under `docs/`:
1. `docs/ARCHITECTURE_CORE.md` (Static):
   - Invariant architectural stack, system boundaries, and design conventions.
   - **Read-Only Invariant**: Read-only during routine tasks; updated only on fundamental architecture shifts.
   - **Append-Only / No Timestamps**: Do not add dynamic timestamps.
2. `docs/ACTIVE_STATE.md` (Dynamic):
   - Current task goals, volatile working state, and recent change history.

## 4. Phase 3: Human-in-the-Loop Validation Checkpoint
1. Pause execution before persisting context or committing documentation.
2. Present a concise architectural summary to the user:
   *"Architecture discovery complete. Summary: [Stack / Core Modules / Storage]. Does this accurately reflect your project architecture? Proceed to freeze and persist?"*
3. Await explicit user approval.

## 5. Phase 4: Structured Chunked Memory Ingestion
Following approval, ingest architectural facts in discrete logical chunks (`[Tech Stack]`, `[Folder Structure]`, `[State Management]`) into workspace documentation and persistence layers.
