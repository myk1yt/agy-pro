# agy-pro Feature Enhancement Report: Model List Display, Reasoning Effort & Gateway Bridge

**Date:** 2026-10-11  
**Module:** `agy-pro` (Go shim, multi-model orchestrator)  
**Status:** COMPLETED & VERIFIED  

---

## 1. Executive Summary

This report documents the design, implementation, and verification of high-priority feature enhancements for `agy-pro`:
1. **Interactive Model List & Spec Viewer (`agy pro models` & `agy pro status`)**: Upgraded provider listings to render human-friendly, columnar tables showing provider endpoint, credential status, model ID, family, context capacity, max output, tool calling support, and reasoning/thinking capabilities.
2. **Reasoning / Thinking Configuration & Effort Expansion**:
   - `Supports Reasoning / Thinking? (Y/n) [Y]` now defaults to `[Y]`.
   - Expanded Reasoning Effort prompt with options: `default`, `none`, `low`, `medium`, `high`, `xhigh`, `max` (default `default`).
   - Added `ReasoningEffort` field to `Capabilities` struct in `internal/providers`.
3. **OpenAI Fanout Client Reasoning Effort & Graceful Fallback**:
   - Injects `reasoning_effort` based on configuration (`default` omits the parameter for server default behavior; `high|medium|low` forwards; `xhigh|max` maps to `high`).
   - Integrated automatic fallback: if an endpoint rejects `reasoning_effort` with HTTP 400 (or 422), the client automatically strips `reasoning_effort` and retries immediately, ensuring 100% safety even if users enable thinking on unsupported models.
4. **Native agy `/model` Gateway Bridge**:
   - `ExecutePassthrough` in `internal/launcher` extracts registered providers and injects `AGY_LLM_GATEWAY_MODELS`, `AGY_LLM_GATEWAY_URL`, `AGY_LLM_GATEWAY_WIRE_PROTOCOL`, and `AGY_LLM_GATEWAY_API_KEY` into the child process environment.
   - When the native `agy` CLI runs, external models configured in `providers.json` appear directly in the interactive `/model` selector.
5. **Rebuild & Deployment**:
   - Built and deployed fresh binary to `~/.agy-pro/bin/agy.exe`.
   - Full test suite passed across all packages (`cmd/agy`, `internal/agenttools`, `internal/fanout`, `internal/launcher`, `internal/mode`, `internal/paths`, `internal/providers`, `internal/bridge`).

---

## 2. Key Architecture & Code Changes

### 2.1 Model List Display (`cmd/agy/main.go`, `cmd/agy/main_test.go`)
- Added `models` subcommand to `handleProCommand` and documented it in `printProHelp`.
- Implemented `printConfiguredProvidersAndModels` and `formatTokenCount` to align columns cleanly across providers:
```
Configured Providers & Models:
[1] NW (https://api.neuralwatt.com/v1) [API Key: Configured]
    - deepseek-v4.1-flash-flex  [Family: deepseek | Context: 1M | MaxOutput: max | Tools: YES | Thinking: YES (high)]
    - glm-5.3-flash-flex        [Family: glm      | Context: 1M | MaxOutput: max | Tools: YES | Thinking: YES (high)]
```
- Context and MaxOutput token formatting handles special cases: `<= 0` -> `max`, `>= 1048576` -> `XM`, `>= 1000` / multiples of 1024 -> `Xk`.

### 2.2 Reasoning Effort in Registry & Wizard (`internal/providers`)
- **`providers.go`**: Added `ReasoningEffort string` with `omitempty` JSON tag to `Capabilities`, and helper `EffectiveReasoningEffort()`.
- **`wizard.go`**:
  - `promptCustomModel`: Default thinking prompt changed to `Supports Reasoning / Thinking? (Y/n) [Y]`.
  - Added prompt: `Reasoning Effort [default, none, low, medium, high, xhigh, max] [default]: `.
  - Presets selection automatically normalizes empty thinking effort to `default`.
  - Summary display includes `thinking: yes (<effort>)`.
- **`presets.go`**: Updated reasoning-specialized models (`deepseek-reasoner`, `claude-3.7-sonnet`, `deepseek/deepseek-r1`, `o3-mini`, `deepseek-ai/DeepSeek-R1`, `deepseek-r1-distill-llama-70b`) with `ReasoningEffort: "high"`.

### 2.3 Fanout Client Reasoning Effort & Graceful Fallback (`internal/fanout`)
- **`client.go`**:
  - Added `mapReasoningEffort(effort string) string`:
    - `"default"`, `""` -> `""` (parameter omitted, triggers server default reasoning).
    - `"none"` -> `"none"`.
    - `"low"`, `"medium"`, `"high"` -> forwarded as-is.
    - `"xhigh"`, `"max"` -> mapped to `"high"`.
  - Splitted network call into `send()`.
  - In `Complete()`: If `reqBody.ReasoningEffort != ""` and server returns HTTP 400/422 or error mentions `reasoning_effort`, clears `ReasoningEffort` and retries immediately.

### 2.4 Native agy Gateway Bridge (`internal/launcher`)
- Added `BuildGatewayEnv(reg *providers.Registry) []string` and `setEnvKV(env []string, entry string) []string`.
- `ExecutePassthrough` loads providers and injects:
  - `AGY_LLM_GATEWAY_MODELS`: comma-separated enabled model IDs (e.g. `deepseek-v4.1-flash-flex,glm-5.3-flash-flex`).
  - `AGY_LLM_GATEWAY_URL`: active provider `BaseURL`.
  - `AGY_LLM_GATEWAY_WIRE_PROTOCOL`: `openai`.
  - `AGY_LLM_GATEWAY_API_KEY`: active provider `KeyValue()`.
- When child `agy` starts, it registers these gateway models into native conversation `/model` prompt.

---

## 3. Verification & Test Results

### 3.1 Unit Test Execution (`go test -v ./...`)
```
ok      agypro/cmd/agy              0.460s  (TestFormatTokenCount, TestPrintConfiguredProvidersAndModels)
ok      agypro/internal/agenttools  (cached)
ok      agypro/internal/bridge      0.976s
ok      agypro/internal/fanout      1.023s  (TestMapReasoningEffort, TestClient_ReasoningEffort_MappingAndFallback, ...)
ok      agypro/internal/launcher    0.561s  (TestBuildGatewayEnv, TestSetEnvKV, ...)
ok      agypro/internal/mode        (cached)
ok      agypro/internal/paths       (cached)
ok      agypro/internal/providers   10.942s (TestInteractiveSetupWizard_ReasoningEffortDefault, ...)
```

### 3.2 Linter & Clean Code Check
- `go vet ./...`: Passed with 0 errors.

### 3.3 Live CLI Command Output Verification
- `agy pro version`:
  ```
  agy-pro version 0.1.0 (Go shim & swarm orchestrator, windows/amd64)
  upstream: 1.3.3
  ```
- `agy pro status`:
  ```
  Profile Mode : FULL
  Real agy.exe : %LOCALAPPDATA%\agy\bin\agy.exe
  Providers    : 2 configured

  Configured Providers & Models:
  [1] NW (https://api.neuralwatt.com/v1) [API Key: Configured]
      - glm-5.3-flash-flex        [Family: glm      | Context: max | MaxOutput: max | Tools: YES | Thinking: YES (default)]
      - glm-5.3-flex              [Family: glm      | Context: max | MaxOutput: max | Tools: YES | Thinking: YES (default)]
  [2] Charm (https://hyper.charm.land/v1) [API Key: Configured]
      - glm-5.3-flash             [Family: glm      | Context: max | MaxOutput: max | Tools: YES | Thinking: YES (default)]
      - qwen3.8-flash             [Family: qwen     | Context: max | MaxOutput: max | Tools: YES | Thinking: YES (default)]
  ```
- `agy pro models`: Successfully lists all configured models with full capability specifications.
- `agy pro doctor`: System operational check PASSED.

---

## 4. File Modification Summary

| File | Status | Description |
|---|---|---|
| `cmd/agy/main.go` | Modified | Added `models` subcommand, updated `status` output with model list table |
| `cmd/agy/main_test.go` | Added | Unit tests for `formatTokenCount` and `printConfiguredProvidersAndModels` |
| `internal/providers/providers.go` | Modified | Added `ReasoningEffort` field and `EffectiveReasoningEffort()` helper |
| `internal/providers/wizard.go` | Modified | Default thinking to `Y`, added effort prompt `[default, none, ...]` |
| `internal/providers/presets.go` | Modified | Added `ReasoningEffort: "high"` to thinking models |
| `internal/providers/providers_test.go` | Modified | Added tests for default and explicit reasoning effort prompts |
| `internal/fanout/client.go` | Modified | Added effort mapping, request injection, and HTTP 400 graceful fallback |
| `internal/fanout/fanout_test.go` | Modified | Added tests for `mapReasoningEffort` and 400 error fallback retry |
| `internal/launcher/launcher.go` | Modified | Injected `AGY_LLM_GATEWAY_*` env vars for `/model` CLI gateway |
| `internal/launcher/launcher_test.go` | Modified | Added tests for `BuildGatewayEnv` and `setEnvKV` |
| `~/.agy-pro/bin/agy.exe` | Rebuilt & Deployed | Active production binary updated |
