# agy-pro Bridge Auth & Model Display Fix Report

**Date**: 2026-10-11  
**Status**: Completed  
**Domain Scope**: `agy-pro/internal/bridge`, `agy-pro/internal/providers`, `agy-pro/internal/launcher`, `agy-pro/cmd/agy`

---

## 1. Executive Summary

This report addresses two critical issues reported after applying the one-touch local bridge in `agy-pro`:
1. **HTTP 401 Invalid API Key Failure**: When launching `agy`, upstream requests failed with `401 Unauthorized` (`invalid_api_key`) due to environment variable latency/uninherited values across terminal sessions, unvalidated bridge proxying forwarding missing keys, and test suite registry pollution.
2. **Missing Provider Identification in `/model` Picker**: When typing `/model`, the model picker displayed ambiguous identifiers without provider attribution (e.g. `glm-5.3-flash-flex`), making it impossible for users to know which provider (NW, Charm, etc.) handled the request.

Both issues have been resolved, thoroughly tested, and verified with 100% test pass rates across the entire `agy-pro` codebase.

---

## 2. Root Cause Analysis

### 2.1 401 Invalid API Key
- **Process Environment Staleness**: When `agy pro setup` saves an API key (`NW_API_KEY`) to Windows User Environment via PowerShell `[Environment]::SetEnvironmentVariable($k, $v, 'User')`, running shell processes do not automatically reload `os.Environ()`. When `agy` started, `os.Getenv(p.APIKeyEnv)` was empty.
- **Unvalidated Forwarding in Bridge**: Previously, `bridge.go`'s `handleChatCompletions` did not validate whether `apiKey := target.Provider.KeyValue()` was empty prior to dispatching upstream requests via `ReverseProxy`. If the key was unset, the bridge deleted the `Authorization` header and forwarded the request unauthenticated, resulting in cryptic upstream 401 errors.
- **Test Suite Registry Overwrite**: Unit tests in `providers_test.go` were directly calling `InteractiveSetupWizard` with mock credentials (`mock-nw-key-999`), which unintentionally overwrote the real user registry in Windows User Environment (`HKCU\Environment`).

### 2.2 Lack of Provider Context in Model Picker
- **Ambiguous Model IDs**: `bridge.go` `/models` and `launcher.go` `AGY_LLM_GATEWAY_MODELS` registered bare model IDs (`m.ID`). When multiple providers offered identical or similar models, they either collided or left the user uncertain which provider backed each model.
- **Upstream ID Incompatibility**: If models are displayed as `model (provider)` to the client, the upstream OpenAI/NeuralWatt API does not recognize `glm-5.3-flash-flex (NW)` and rejects it with 404 or 400. The bridge must strip this suffix before proxying to upstream.

---

## 3. Technical Implementation

### 3.1 Robust Windows Registry Real-Time Key Lookup (`internal/providers`)
- **`GetWindowsUserEnv(key string) string`** in [`presets.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/presets.go):
  - Queries `HKCU\Environment` and falls back to `HKLM\SYSTEM\CurrentControlSet\Control\Session Manager\Environment`.
  - Implements exact whitespace-aware token matching (`strings.Fields`) with case-insensitivity (`strings.EqualFold`) to prevent prefix collisions (e.g., `NW_API` vs `NW_API_KEY`).
  - Preserves internal spaces within secret tokens while trimming outer whitespace.
- **`Provider.KeyValue()`** in [`providers.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/providers.go):
  - Checks `os.Getenv(p.APIKeyEnv)` first; if empty on Windows, falls back in real time to `GetWindowsUserEnv(p.APIKeyEnv)` and caches into `os.Setenv`.
- **Test Isolation Guard** in [`wizard.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/wizard.go):
  - Added `isRunningTests()` guard to bypass `persistWindowsUserEnv` when running under `go test` (`.test.exe` / `.test`).

### 3.2 Display Format: `model (provider)` (`internal/launcher` & `internal/bridge`)
- **Model Spec Parsing** in [`selection.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/selection.go):
  - Exported `ParseModelSpec(spec string) (baseModel, providerName string)` supporting:
    - `model (provider)` -> `("model", "provider")`
    - `provider/model`   -> `("model", "provider")`
    - `model`            -> `("model", "")`
- **Gateway Model Registration** in [`launcher.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/launcher/launcher.go):
  - `CollectModelIDs` formats all enabled models as `fmt.Sprintf("%s (%s)", m.ID, p.Name)`.
  - Injected into child process environment: `AGY_LLM_GATEWAY_MODELS=glm-5.3-flash-flex (NW),glm-5.3-flex (NW),glm-5.3-flash (Charm),...`.
  - Child environment synchronization: All resolved provider keys are automatically synced into `cmd.Env` child environment.
- **OpenAI `/models` Endpoint** in [`bridge.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/bridge/bridge.go):
  - Returns `ID: fmt.Sprintf("%s (%s)", m.ID, p.Name)` and `OwnedBy: p.Name`.
  - Resolves duplicate model IDs across distinct providers without collision.

### 3.3 Target Resolution, Upstream Rewriting, and 401 Interception (`internal/bridge`)
- **Target Resolution** (`findBestTarget`):
  - Uses `providers.ParseModelSpec` to identify provider name and base model ID.
  - Matches the exact requested provider first, with fallback to active-key providers and legacy IDs.
- **Strict Pre-Proxy Authentication Validation**:
  - If `target.Provider.APIKeyEnv != ""` and `apiKey == ""`:
    - Immediately terminates with HTTP `401 Unauthorized`.
    - Returns standardized JSON error payload:
      ```json
      {
        "error": {
          "message": "API key for provider NW (NW_API_KEY) is not configured or empty in environment",
          "type": "authentication_error",
          "code": 401
        }
      }
      ```
    - Upstream server is never contacted with invalid or empty authentication headers.
- **Upstream Model ID Rewriting**:
  - Rewrites `rawReq["model"] = target.Model.ID` prior to serializing `forwardBody`.
  - The client sees `glm-5.3-flash-flex (NW)`, while the upstream server receives the raw ID `glm-5.3-flash-flex`.
  - Correct `Authorization: Bearer <apiKey>` is set on `outReq`.

---

## 4. Verification & Test Suite

1. **`internal/bridge`**:
   - `TestStartBridge_Lifecycle`: PASS
   - `TestModelsEndpoint`: PASS (validates `deepseek-chat (deepseek)`, `deepseek-chat (openrouter)`, `glm-5.3-flash-flex (openrouter)`)
   - `TestChatCompletions_StandardJSON`: PASS
   - `TestChatCompletions_SSEStreaming`: PASS
   - `TestChatCompletions_ReasoningEffortHandling`: PASS
   - `TestChatCompletions_ErrorHandling`: PASS
   - `TestChatCompletions_ModelWithProviderSuffix`: PASS (verifies client `model (NW)` -> upstream receives stripped `model` with `Bearer <key>`)
   - `TestChatCompletions_MissingAPIKeyError`: PASS (verifies missing key returns 401 without contacting upstream)
   - `TestParseModelSpec`: PASS (all syntax formats verified)
2. **`internal/launcher`**:
   - `TestBuildGatewayEnv`: PASS
   - `TestCollectModelIDs`: PASS
   - `TestExecutePassthrough_StartsBridgeAndInjectsEnv`: PASS
3. **`internal/providers`**:
   - All 24 unit tests: PASS
4. **Toolchain Quality**:
   - `go vet ./...`: 0 warnings, 0 errors
   - `go test ./...`: 100% PASS across all 9 packages
   - `go build ./cmd/agy`: Clean binary build generated at `bin/agy.exe`

---

## 5. Artifact Links
- Bridge implementation: [`internal/bridge/bridge.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/bridge/bridge.go)
- Bridge tests: [`internal/bridge/bridge_test.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/bridge/bridge_test.go)
- Launcher implementation: [`internal/launcher/launcher.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/launcher/launcher.go)
- Launcher tests: [`internal/launcher/launcher_test.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/launcher/launcher_test.go)
- Provider selection & spec parser: [`internal/providers/selection.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/selection.go)
- Windows registry fallback: [`internal/providers/presets.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/presets.go)
- KeyValue resolution: [`internal/providers/providers.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/providers.go)
- Test isolation in wizard: [`internal/providers/wizard.go`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/internal/providers/wizard.go)
