# Independent Stand-Alone QA Verification Report

**Project**: `Antigravity-cli` / `agy-pro`  
**Branch**: `feature/agy-pro`  
**Verifier**: Independent Stand-Alone Blind QA Verifier  
**Date**: 2026-10-11  
**Overall Status**: **[Status]: PASS**

---

## 1. Executive Summary

This independent blind quality assurance audit was performed on the `agy-pro` workspace modifications, focusing on the latest connection probe and setup UX enhancements:

1. **Extended Live Connection Probe Timeout (35s)**:
   - Live endpoint test timeout increased from 10s to **35 seconds** (`http.Client{Timeout: 35 * time.Second}` and `context.WithTimeout(..., 35*time.Second)`) in `internal/providers/wizard.go`.
   - Accommodates cold-starting model instances, heavy gateway queuing, and initial reasoning model handshakes (e.g., DeepSeek Reasoner, NeuralWatt, OpenRouter) without false-positive failures.
2. **Instant Live Retry & Recovery Loop**:
   - In case of probe failures or network hiccups, users are presented with a non-destructive interactive loop:
     1. `1) Save model configuration anyway [Recommended]`
     2. `2) Retry test immediately`
     3. `3) Edit Base URL or API Key`
     4. `4) Discard model`
   - Added `isTimeoutErr()` heuristic detection that automatically alerts the user if the error was a gateway or context timeout (`[Notice] Gateway response timed out. The model may be cold-starting or queueing. You can retry immediately or save anyway.`).
3. **Preserved Base URL & API Key Credentials Across Multi-Model Configuration**:
   - When users configure additional models for the same provider (`Add another model for <provider>? (y/N)`), the established Base URL and authenticated API Key are seamlessly retained without re-prompting or credential loss.
4. **Automated Test Suite Expansion**:
   - Added 7 new dedicated test cases in `internal/providers/providers_test.go` covering timeout detection, probe pass/fail paths, instant retry loops, URL/key re-entry, timeout notice messages, and multiple model credential preservation.
   - All **46/46 tests pass** cleanly without caching across the entire workspace.
5. **Installer & Shell Integration Verification**:
   - `install.cmd` and `agy-pro/scripts/install.ps1` execute cleanly end-to-end, compile `agy.exe`, register highest-priority PATH, and pass post-installation diagnostics (`agy pro doctor`).
6. **Security & Cleanliness**:
   - Rigorous automated scan confirms **0 real API keys** and **0 hardcoded personal user paths** in the entire codebase.

---

## 2. Key Metrics

| Metric | Target | Actual | Evaluation |
| :--- | :--- | :--- | :--- |
| **Go Compilation (`go build ./...`)** | 0 errors | 0 errors | **PASS** |
| **Go Static Analysis (`go vet ./...`)** | 0 warnings | 0 warnings | **PASS** |
| **Go Test Suite (`go test -v -count=1 ./...`)** | $\ge 21$ tests passing | 100% pass (**46/46 tests passing**) | **PASS** |
| **Security Scan: Plaintext API Keys** | 0 detected | 0 detected (Synthetic dummy tokens only in unit tests) | **PASS** |
| **Security Scan: Hardcoded User Paths** | 0 detected in code | 0 detected in `agy-pro/**` | **PASS** |
| **Probe Timeout Verification** | 35 seconds | Configured and verified in `wizard.go` | **PASS** |
| **Instant Retry & Recovery Menu** | Choice 1-4 loop | Tested clean via mock servers in unit tests | **PASS** |
| **Multi-Model Credential Preservation** | 0 re-prompting | Reuses provider credentials across model loop | **PASS** |
| **Installer Integrity (`install.cmd` / `install.ps1`)** | Zero errors, Exit Code 0 | Tested clean, Doctor checks passed | **PASS** |

---

## 3. Security Scan Results

A rigorous automated security scan was performed across all modified and untracked files:
1. **API Key Token Scan**:
   - Verified that no real API keys (such as `sk-...`, `gsk_...`, `nvapi-...`) are hardcoded or committed.
   - All tests in `providers_test.go` and `fanout_test.go` utilize synthetic dummy tokens (e.g., `mock-nw-key-999`, `mock-test-key-123`).
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
- [x] **`agypro/internal/providers`** (PASS - 22/22 tests)
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
  - `TestIsTimeoutErr`: Recognition of network, context, and error message timeouts
  - `TestProbeEndpoint`: Endpoint verification and HTTP 401 handling
  - `TestInteractiveSetupWizard_ProbeFailure_SaveAnyway`: Recommended fallback saving
  - `TestInteractiveSetupWizard_ProbeFailure_RetryImmediately`: Seamless retry upon transient gateway failures
  - `TestInteractiveSetupWizard_ProbeFailure_EditAndRetry`: In-place credential and URL editing
  - `TestInteractiveSetupWizard_ProbeFailure_TimeoutNotice`: Informative timeout diagnostics
  - `TestInteractiveSetupWizard_MultipleModels_PreservesCredentials`: Preserving credentials across model loops
- [x] **Installers** (PASS)
  - `agy-pro/scripts/install.ps1`: Executed with `-NoPause`, exit code 0, doctor diagnostics pass
  - `install.cmd`: Executed with `-NoPause`, exit code 0

---

## 5. Final Recommendation & Sign-Off

The modifications meet all quality, operational, and security invariants. All 46 test suites pass cleanly. The repository is ready for git commit and push to remote origin on branch `feature/agy-pro`.

**Verdict: READY FOR MERGE / PRODUCTION**
