# Independent Stand-Alone QA Verification Report

**Project**: `Antigravity-cli` / `agy-pro`  
**Branch**: `feature/agy-pro`  
**Verifier**: Independent Stand-Alone Blind QA Verifier  
**Date**: 2026-10-11  
**Overall Status**: **[Status]: PASS**

---

## 1. Executive Summary

This independent blind quality assurance audit was performed on the `agy-pro` workspace modifications, focusing on the latest updates:
1. **Modern Token Defaults & Server Maximum Support**:
   - Default Context Length raised to **1,048,576 tokens (1M tokens)**, supporting `-1` for server maximum.
   - Default Max Output Tokens raised to **131,072 tokens (128k tokens)**, supporting `-1` for server maximum.
   - Client payload generator (`internal/fanout/client.go`) omits `max_tokens` when `MaxOutputTokens <= 0` (e.g., `-1`), allowing the remote server to use its native maximum output capacity without client-side capping or HTTP 400 parameter errors.
   - Prompts and message histories are forwarded completely without artificial client-side truncation when `ContextLength <= 0` or `-1`.
2. **Removed Tags Friction**:
   - The interactive prompt `Tags (comma-separated)` was completely removed from the wizard.
   - Tags are now automatically derived by `DeriveTags()` based on model capabilities (`general`, `tools`, `reasoning`, `vision`).
3. **Curated Presets Updated**:
   - Presets in `internal/providers/presets.go` updated to unified Context Lengths (1M tokens or official spec) and `MaxOutputTokens: -1` (server maximum).
4. **Installer Verification**:
   - Both `install.cmd` and `agy-pro/scripts/install.ps1` executed cleanly end-to-end, compiled `agy.exe`, set up PATH, and passed post-installation diagnostics (`agy pro doctor`).
5. **Security Scanning**:
   - Zero plaintext real API keys and zero personal username paths are hardcoded in the codebase.

All Go compilation targets (`go build ./...`), static analysis (`go vet ./...`), and automated unit/integration tests across all packages executed with a 100% pass rate (**39/39 tests passed**, 0 failures, exceeding the 21+ requirement).

---

## 2. Key Metrics

| Metric | Target | Actual | Evaluation |
| :--- | :--- | :--- | :--- |
| **Go Compilation (`go build ./...`)** | 0 errors | 0 errors | **PASS** |
| **Go Static Analysis (`go vet ./...`)** | 0 warnings | 0 warnings | **PASS** |
| **Go Test Suite (`go test -v -count=1 ./...`)** | $\ge 21$ tests passing | 100% pass (**39/39 tests passing**) | **PASS** |
| **Security Scan: Plaintext API Keys** | 0 detected | 0 detected (Mock tokens only in unit tests) | **PASS** |
| **Security Scan: Hardcoded User Paths** | 0 detected in code | 0 detected in `agy-pro/**` | **PASS** |
| **Interactive Wizard UX Verification** | 1M Context, 128k Output, -1 Server Max, No Tags Prompt | Verified via headless unit tests | **PASS** |
| **Installer Integrity (`install.cmd` / `install.ps1`)** | Zero errors, Exit Code 0 | Tested clean, Doctor checks passed | **PASS** |

---

## 3. Security Scan Results

A rigorous automated security scan was performed across all modified and untracked files:
1. **API Key Token Scan**:
   - Verified that no real API keys (such as `sk-...`, `gsk_...`, `nvapi-...`) are hardcoded or committed.
   - All tests in `providers_test.go` and `fanout_test.go` utilize synthetic dummy tokens (e.g., `sk-dummy-mock-token-1234567890abcdef`, `mock-test-key-123`).
   - `providers.json` persistence invariant: Even if a user enters/pastes a raw API key during `agy pro setup`, the wizard automatically intercepts it, sets the corresponding environment variable (`NW_API_KEY`, `OPENAI_API_KEY`, etc.), and persists only the environment variable name in `providers.json`.
2. **Personal Path Scan**:
   - Zero hardcoded occurrences of `C:\Users\k1yt\...` or personal paths exist in any Go source code under `agy-pro/`.
   - All file and directory resolutions rely strictly on runtime evaluation of `os.UserHomeDir()` or environment overrides via `internal/paths`.

---

## 4. Verification Checklist & Test Breakdown

- [x] **`agypro/internal/agenttools`** (PASS - 4/4 tests)
  - `TestResolveSafePath_Guard`: Directory traversal containment
  - `TestReadFileAndWriteFile`: Safe read/write roundtrip
  - `TestListDirAndGrepAndGlob`: Agent filesystem inspection tools
  - `TestToolRunnerPermissions`: Sandboxed tool execution permissions
- [x] **`agypro/internal/fanout`** (PASS - 8/8 tests)
  - `TestFanout_ParallelDoubleBlind`: Multi-seat concurrency
  - `TestFanout_Failover_SameModelDifferentProvider`: Same-model failover
  - `TestFanout_Failover_DifferentFamily`: Diverse family failover
  - `TestFanout_Failover_Degraded`: Graceful degradation
  - `TestFanout_AgenticToolLoop`: Autonomous tool execution loop
  - `TestFanout_ReconciliationLogic`: Multi-model synthesis
  - `TestFanout_CLI_FlagParsing`: Flag evaluation
  - `TestClient_TokenHandling`: Client omitting `max_tokens` on `-1`/`0`, explicit token forwarding, no prompt truncation
- [x] **`agypro/internal/launcher`** (PASS - 4/4 tests)
  - `TestResolveRealAgyWithEnvOverride`: Passthrough override resolution
  - `TestResolveRealAgyExcludesSelfDirAndSameFile`: Self-referential loop protection
  - `TestResolveRealAgyFallback`: Graceful executable locator fallback
  - `TestExecutePassthroughRecursionGuard`: Infinite loop prevention
- [x] **`agypro/internal/mode`** (PASS - 3/3 tests)
  - `TestStatePersistence`: Mode state saving and loading
  - `TestSwitchModesAndBackup`: Backup creation during profile switch
  - `TestSwitchInteractive`: Interactive mode selection
- [x] **`agypro/internal/paths`** (PASS - 5/5 tests)
  - `TestUserHome`: User home resolution
  - `TestAgyProHomeOverride`: Custom directory overrides
  - `TestAtomicWriteFile`: Atomic file writes with permissions
  - `TestMoveToBackup`: Safe timestamped backup rotation
  - `TestDefaultRealAgyPath`: Real binary discovery
- [x] **`agypro/internal/providers`** (PASS - 15/15 tests)
  - `TestValidate`: Schema and integrity checking
  - `TestLoadFrom_NotExist`: Handling of missing configuration
  - `TestUnsetEnvVars`: Environment isolation
  - `TestInferFamily`: Model family inference
  - `TestRedactor`: Sensitive key redacting
  - `TestSelectionAndAlternates`: Model alternate failover mapping
  - `TestInteractiveSetupWizard`: Custom model flow without tags prompt, default limits
  - `TestGetModelPresets`: Presets integrity for all supported providers
  - `TestIsLikelyAPIKey`: Heuristic key detection
  - `TestFetchRemoteModels`: Discovery over mock HTTP endpoint
  - `TestInteractiveSetupWizard_DeepSeekPreset`: Preset flow verification
  - `TestInteractiveSetupWizard_NWPreset`: NeuralWatt preset verification (1M context, -1 max output)
  - `TestInteractiveSetupWizard_PastedRawKeyAbsorption`: Key absorption into environment
  - `TestDeriveTags`: Capability-to-tags synthesis logic
  - `TestInteractiveSetupWizard_ServerMaximumTokens`: Handling of `-1` for server maximum tokens
- [x] **Installers** (PASS)
  - `agy-pro/scripts/install.ps1`: Executed with `-NoPause`, exit code 0, doctor diagnostics pass
  - `install.cmd`: Executed with `-NoPause`, exit code 0

---

## 5. Final Recommendation & Sign-Off

The modifications meet all quality, operational, and security invariants. All 39 test suites pass cleanly. The repository is ready for git commit and push to remote origin on branch `feature/agy-pro`.

**Verdict: READY FOR MERGE / PRODUCTION**
