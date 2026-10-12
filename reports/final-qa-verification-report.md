# Independent Double-Blind QA Verification & Security Audit Report

**Project**: `Antigravity-cli` / `agy-pro`  
**Branch**: `feature/agy-pro`  
**Verifier**: Independent Stand-Alone Blind QA Verifier  
**Date**: 2026-10-11  
**Overall Status**: **[Status]: PASS**

---

## 1. Executive Summary

This independent blind quality assurance audit was performed on the `agy-pro` workspace, verifying:
1. **Preservation of Native Gemini 3.8 Flash**: Resolving the issue where `AGY_LLM_GATEWAY_*` unconditionally hijacked `agy.exe` and caused native Gemini models to disappear with `"Gemini 3.8 Flash (High) is no longer available"` warnings.
2. **Conditional Activation Architecture**: Ensuring default `agy` invocations run 100% native with zero proxy overhead and full Antigravity subscription quota, while targeted `--model <external-model>`, `-m <external-model>`, and `--bridge` flags dynamically activate the local bridge reverse proxy.
3. **HTTP 401 Pre-Proxy Guard & Real-Time Windows Registry Key Resolution**: Real-time querying of `HKCU\Environment` and `HKLM` for API keys, child process environment propagation, and pre-proxy validation to eliminate upstream 401 Unauthorized failures.
4. **Model (Provider) Aliasing**: Dual-alias registration of `model (provider)` and bare `model` in `AGY_LLM_GATEWAY_MODELS` and model spec parsing in `bridge` to seamlessly rewrite model IDs before contacting external providers.
5. **Security & Sanitization**: Comprehensive scan verifying zero exposed API keys and zero hardcoded personal user paths in the codebase.
6. **Full Test Suite & Compilation Integrity**: `go build ./...`, `go vet ./...`, and `go test -count=1 ./...` executed across all packages: **66/66 tests passing (100% pass rate)**.
7. **Installer Verification**: `install.cmd` and `install.ps1` verified end-to-end.

---

## 2. Key Metrics

| Metric | Target | Actual | Evaluation |
| :--- | :--- | :--- | :--- |
| **Go Compilation (`go build ./...`)** | 0 errors | 0 errors | **PASS** |
| **Go Static Analysis (`go vet ./...`)** | 0 warnings | 0 warnings | **PASS** |
| **Go Test Suite (`go test -count=1 ./...`)** | 100% pass | **66/66 tests passing (100%)** | **PASS** |
| **Native Model Catalog (`agy models`)** | 18 native models intact | All 18 native models listed (Gemini 3.8 Flash High/Med/Low intact) | **PASS** |
| **Native Execution Warning** | 0 warnings | 0 warnings, native prompt responds instantly | **PASS** |
| **Diagnostic Doctor (`agy pro doctor`)** | Healthy status | `[PASS] Doctor diagnostics completed successfully. System is operational.` | **PASS** |
| **External Bridge Catalog (`agy --bridge models`)** | Provider attribution | Shows `model (provider)` and bare `model` aliases | **PASS** |
| **External Execution (`agy --model ...`)** | Code 0, valid output | Verified with Charm & NW models | **PASS** |
| **Pre-Proxy Auth Guard (Missing Key)** | HTTP 401 JSON error | Returns 401 locally without hitting upstream | **PASS** |
| **Security Scan: Plaintext API Keys** | 0 detected | 0 detected (synthetic mocks only in unit tests) | **PASS** |
| **Security Scan: Hardcoded User Paths** | 0 in codebase | 0 detected in `agy-pro/**` | **PASS** |
| **Installer Integrity (`install.cmd -NoPause`)** | Code 0 | Clean execution, binary deployed, PATH updated | **PASS** |
| **Git Push Status** | Pushed to origin | Pushed to `origin/feature/agy-pro` | **PASS** |

---

## 3. Detailed Verification Results

### 3.1 Go Environment & Full Test Suite Execution

Executed inside `D:\OneDrive\Projects\Antigravity-cli\agy-pro`:

```text
=== Go Test Results (-count=1 ./...) ===
ok   agypro/cmd/agy            0.500s  (2/2 tests pass)
ok   agypro/internal/agenttools 0.496s  (4/4 tests pass)
ok   agypro/internal/bridge     1.056s  (9/9 tests pass)
ok   agypro/internal/fanout     1.012s  (10/10 tests pass)
ok   agypro/internal/launcher   1.407s  (9/9 tests pass)
ok   agypro/internal/mode       0.871s  (3/3 tests pass)
ok   agypro/internal/paths      0.374s  (5/5 tests pass)
ok   agypro/internal/providers  1.087s  (24/24 tests pass)
Total: 66/66 unit tests passing (100% pass rate)
```

- `go build ./...`: Exit Code 0 (clean compilation).
- `go vet ./...`: Exit Code 0 (zero lint warnings).

### 3.2 Security & Sanitization Audit

1. **API Key Token Scan**:
   - Scanned all modified and new files for high-entropy API key patterns (`sk-[a-zA-Z0-9]{8,}`).
   - Result: **0 plaintext API keys detected**.
   - All tests use dummy test tokens (`mock-nw-key-999`, `test-api-key`).
2. **Hardcoded User Paths Scan**:
   - Scanned `agy-pro/**` for `C:\Users\k1yt` and `C:\Users\`.
   - Result: **0 occurrences found in code**.
   - All paths are dynamically constructed using `os.UserHomeDir()`, `[Environment]::GetFolderPath("UserProfile")`, and `paths.AgyProHome()`.
3. **Environment Isolation**:
   - `InteractiveSetupWizard` includes `isRunningTests()` guard to prevent test runs from polluting user registry (`HKCU\Environment`).

### 3.3 Native Gemini Preservation & External Bridge Verification

1. **Native Model Catalog (`agy models`)**:
   - Verified via `~/.agy-pro/bin/agy.exe models`.
   - Output confirmed:
     ```text
     gemini-3.8-flash-high     Gemini 3.8 Flash (High)
     gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)
     gemini-3.8-flash-low      Gemini 3.8 Flash (Low)
     gemini-3.7-flash-high     Gemini 3.7 Flash (High)
     ...
     claude-opus-5-5-high      Claude Opus 5.5 (High)
     gpt-oss-120b-medium       GPT-OSS 120B (Medium)
     ```
2. **Native Execution Warning Verification**:
   - Command: `agy -p "ping"`
   - Output: `pong! 준비되었습니다. 어떤 작업을 진행할까요?`
   - Warnings: **Zero warnings** (no `"Gemini 3.8 Flash (High) is no longer available"` message).
3. **Doctor Verification (`agy pro doctor`)**:
   - Real `agy.exe` located at `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe` (v1.3.3).
   - Shim executable active at `C:\Users\k1yt\.agy-pro\bin\agy.exe`.
   - All configured providers operational.
   - Status: `[PASS] Doctor diagnostics completed successfully. System is operational.`
4. **External Bridge On-Demand Activation**:
   - `agy --bridge models` lists external models with dual alias formatting:
     - `glm-5.3-flash-flex (NW)` and `glm-5.3-flash-flex`
     - `glm-5.3-flash (Charm)` and `glm-5.3-flash`
     - `qwen3.8-flash (Charm)` and `qwen3.8-flash`
   - `agy --model "glm-5.3-flash (Charm)" -p "Reply with exactly 'PONG-CHARM'"` -> Returned `PONG-CHARM` (Exit code 0).
   - `agy --model qwen3.8-flash -p "Reply with exactly 'PONG-QWEN'"` -> Returned `PONG-QWEN` (Exit code 0).
5. **Installer Verification**:
   - Executed `install.cmd -NoPause`.
   - Successfully compiled `agy.exe`, safely swapped into `~/.agy-pro/bin/agy.exe`, validated Windows User PATH precedence, and ran doctor diagnostics with code 0.

---

## 4. Verification Checklist

- [x] `go build ./...` passes with 0 errors
- [x] `go vet ./...` passes with 0 warnings
- [x] `go test -v ./...` passes with 100% success (66/66 tests)
- [x] Plaintext API keys sanitized (0 keys detected)
- [x] Hardcoded personal user paths sanitized (0 paths in codebase)
- [x] Native Gemini models preserved under default `agy` execution
- [x] Zero `"Gemini 3.8 Flash (High) is no longer available"` warnings
- [x] External models accessible via `--model <model>`, `-m <model>`, and `--bridge`
- [x] Dual alias `model (provider)` and bare `model` recognized
- [x] Pre-proxy auth guard returns 401 locally if key is missing
- [x] Real-time registry key resolution handles uninherited environment variables
- [x] `agy pro doctor` reports operational health
- [x] `install.cmd` and `install.ps1` execute cleanly end-to-end
- [x] Staged and committed on branch `feature/agy-pro`
- [x] Pushed to remote origin GitHub (`git push origin feature/agy-pro`)

---

## 5. Artifact Links

- [Final QA Verification Report](file:///D:/OneDrive/Projects/Antigravity-cli/reports/final-qa-verification-report.md)
- [Architecture Completion Plan](file:///D:/OneDrive/Projects/Antigravity-cli/reports/agy-pro-architecture-completion-plan.md)
- [Binary Gateway Reverse-Engineering Analysis](file:///D:/OneDrive/Projects/Antigravity-cli/reports/agy-binary-gateway-analysis.md)
- [Bridge Auth & Display Fix Report](file:///D:/OneDrive/Projects/Antigravity-cli/reports/bridge-auth-display-fix-report.md)
- [Bridge Implementation](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/bridge/bridge.go)
- [Launcher Implementation](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/launcher/launcher.go)
- [Provider Selection Implementation](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/selection.go)
