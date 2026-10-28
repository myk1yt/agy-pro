---
name: testdrive
description: >
  Automated dependency discovery, release build, codegen, and smoke verification runbook. Prioritizes CI recipes as canonical release builds, executes self-terminating tasks, performs headless GUI/process smoke checks, and reports absolute execution commands.
---

# Testdrive Runbook — Automated Build & Smoke Verification

## 1. Execution Principles & Termination Rules
- **Self-Terminating Commands Only**: Execute only commands that run and exit with a status code (builds, codegen, linters, tests). Never execute persistent servers or watchers (`npm start`, `cargo watch`, `dev`, `serve`).
- **CI Recipe Canonical Authority**: CI/CD workflows (`.github/workflows/*.yml`) define the true production release build.
- **Zero Monolithic Execution**: For multi-project monorepos, resolve each workspace member's build dependencies methodically.

## 2. Phase 0: Multi-Tier Information Discovery
Scan the workspace using the strict priority hierarchy:
- **P0 Task Runners**: `Makefile`, `justfile`, `Taskfile.yml`, `package.json` scripts, `scripts/*.ps1|sh|bat`.
- **P1 CI/CD Workflows**: `.github/workflows/`, `.gitlab-ci.yml`, `azure-pipelines.yml`. Extract release build jobs.
- **P2 Containers**: `Dockerfile`, `docker-compose.yml`. Inspect multi-stage build recipes.
- **P3 Language Manifests**: `Cargo.toml`, `go.mod`, `pubspec.yaml`, `CMakeLists.txt`, `pyproject.toml`, `build.gradle`.
- **P4 Codegen Tools**: `build_runner`, `buf generate`, `prisma generate`, `graphql-codegen`, `frb.yaml`.
- **P5 Lockfiles & Package Managers**: `package-lock.json` (npm), `pnpm-lock.yaml` (pnpm), `yarn.lock` (yarn), `bun.lockb` (bun), `Cargo.lock` (cargo), `go.sum` (go).

## 3. Phase 1: Build Pipeline Construction & Ordering
Synthesize the ordered execution pipeline:
1. Dependency installation (`npm ci`, `pnpm install --frozen-lockfile`, `cargo fetch`, `go mod download`).
2. Code generation (`buf generate`, `prisma generate`, `flutter pub run build_runner build`).
3. Compilation & release build with `--release` flags where appropriate.
Present pipeline to user before execution:
```text
[빌드 파이프라인]
1. <명령어> - <설명>
2. <명령어> - <설명>
```

## 4. Phase 2: Sequential Execution & Error Handling
1. Execute pipeline steps sequentially.
2. **Auto-fix Rule**: Fix ordinary compilation/syntax errors automatically. For cross-layer boundary errors (FFI, binding desync), re-run codegen. If failure repeats 3 times, halt and escalate.
3. Never start long-running servers in this phase.

## 5. Phase 2.5: Headless GUI & Process Background Smoke Test
For compiled binary executables (e.g., Windows `.exe`), verify release integrity via temporary headless smoke test:
1. Launch process in background: `$p = Start-Process -FilePath "<path>" -WindowStyle Minimized -PassThru`
2. Wait 3 to 5 seconds (`Start-Sleep -Seconds 3`) for library linkage and initialization.
3. Check status:
   - Exited immediately -> **Silent Crash** (inspect exit code and dependencies).
   - Running with non-zero `MainWindowHandle` -> **Healthy UI Initialization**.
   - Running with zero window handle -> Flag potential **Process Hang**.
4. **Mandatory Termination**: Immediately kill the test process (`Stop-Process -Id $p.Id -Force`). Leave zero background artifacts.

## 6. Phase 3: Absolute Path Execution Reporting
Upon success, report ready-to-run commands using absolute paths for immediate user invocation:
```text
[testdrive 완료] 빌드/검증 성공
워크스페이스: C:\...\<프로젝트>

[실행 가능한 결과물]
- 실행 파일: C:\...\<산출물.exe>
- 실행 명령: cd C:\...\<디렉토리> && <명령어>
- 검증 명령: cd C:\...\<디렉토리> && <테스트명령어>
```
