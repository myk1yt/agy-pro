---
name: cleartemp
description: >
  Autonomous disk optimization and workspace hygiene runbook. Selectively purges build artifacts, __auto_verify_* temporary tests, diagnostics JSONs, and cache junk while strictly preserving tracked files, docs, and @keep annotated assets.
---

# Cleartemp Runbook — Autonomous Workspace Hygiene & Cache Purge

## 1. Operational Invariants & Strict Retention Laws
- **Strict Keep (보존 대상)**:
  - Git Tracked Files (`git ls-files`): All source code and tracked assets.
  - Documentation: All files in `docs/**` and `.md` files.
  - Permanent Tests: Test suites in `test/**`, `tests/**`.
  - System Configurations: `.git/`, `.vscode/`, `.gemini/`, `.agy-pro/`, `configs/`.
  - Manual Retention Annotation (`@keep`): Any file containing `// @keep` or `# @keep`.
- **Strict Delete (정리 대상)**:
  - Temporary verification tests (`__auto_verify_*`).
  - Diagnostic junk (`zoo-diagnostics-*.json`, `agy-diagnostics-*.json`).
  - Untracked logs (`*.log`) matching `.gitignore`.
  - Framework build caches: Python (`__pycache__/`, `.pytest_cache/`), Node.js (`.next/`, `.turbo/`), Flutter/Dart (`.dart_tool/`, untracked `build/`), Rust (`target/` untracked compile artifacts).

## 2. Phase 1: Target Scan & Safety Verification
1. Scan the workspace for candidate files matching Strict Delete patterns.
2. Cross-verify with `git status --ignored --others` to confirm untracked status.
3. **Untracked Source Guard**: If any untracked genuine source code file (`.py`, `.ts`, `.js`, `.go`, `.dart`, `.rs`, `.json`, `.yaml`) is detected, exclude it from deletion and issue an immediate Git registration notice.

## 3. Phase 2: Dry-Run Briefing
Present a formatted markdown table of candidates prior to deletion:
| Path | File Type | Size | Reason |
|---|---|---|---|
Calculate total reclaimable disk space.

## 4. Phase 3: Sequential Safe Deletion
Delete candidate files one by one sequentially. Never execute bulk destructive commands (`rm -rf *`).

## 5. Phase 4: Disk Optimization Summary
Report reclaimed disk space with a visual bar representation (`[Before] ■■■■■■■■□□ 80MB -> [After] □□□□□□□□□□ 0MB`) and confirm the integrity of protected assets.
