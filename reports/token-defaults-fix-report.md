# agy-pro Token Defaults & Wizard Tags Friction Removal Report

**Date:** 2026-10-11  
**Scope:** `agy-pro/internal/providers`, `agy-pro/internal/fanout`  
**Binary Deployed:** `~/.agy-pro/bin/agy.exe`

---

## 1. Executive Summary

This update streamlines provider configuration and eliminates token truncation friction across `agy-pro`:
1. **Modern Token Defaults & Server Maximum Support**:
   - Default Context Length raised to **1,048,576 (1M tokens)**, supporting `-1` for server maximum.
   - Default Max Output Tokens raised to **131,072 (128k tokens)**, supporting `-1` for server maximum.
2. **Removed Tags Friction**:
   - The confusing interactive prompt `Tags (comma-separated)` was completely removed from the wizard.
   - Tags are now automatically derived by the system from model capabilities (`general`, `tools`, `reasoning`, `vision`).
3. **Preset Unification**:
   - Model presets in `presets.go` updated to unified Context Lengths (1M tokens or official spec) and `MaxOutputTokens: -1` (server maximum).
4. **Client-Side Omitting & Zero Truncation**:
   - OpenAI request generator in `fanout/client.go` omits `max_tokens` when `MaxOutputTokens <= 0` (e.g., `-1`), allowing the remote server to use its native maximum output capacity without client-side capping or HTTP 400 parameter errors.
   - Prompts and message histories are forwarded completely without artificial client-side truncation when `ContextLength <= 0` or `-1`.

---

## 2. Key Changes by Component

### 2.1 `internal/providers/wizard.go`
- **Context Length Prompt**:
  - Prompt text: `Context Length in tokens [1048576, or -1 for server maximum]`
  - Default value: `1048576`. If `-1` is entered, it is preserved as `-1` (server maximum).
- **Max Output Tokens Prompt**:
  - Prompt text: `Max Output Tokens [131072, or -1 for server maximum]`
  - Default value: `131072`. If `-1` is entered, it is preserved as `-1` (server maximum).
- **Tags Prompt Removal & Automated Tagging**:
  - Removed `promptLine(r, w, "Tags (comma-separated)", ...)` from custom model flow.
  - Implemented `DeriveTags(cap Capabilities) []string`:
    - Base tag: `general`
    - If `SupportsTools`: adds `tools`
    - If `SupportsThinking`: adds `reasoning`
    - If `SupportsVision`: adds `vision`
- **Auto-discovered Preset Defaults**:
  - Discovered models default to `ContextLength: 1048576`, `MaxOutputTokens: -1`, `Tags: ["general", "tools"]`.
- **Wizard Summary Display**:
  - Context length display supports `server-max` for `<= 0` and `1M` format for `>= 1048576`.

### 2.2 `internal/providers/presets.go`
- Unified presets across all providers (`deepseek`, `openrouter`, `groq`, `openai`, `nw`, `together`, `mistral`):
  - `MaxOutputTokens`: Unified to `-1` (server maximum), preventing artificial capping and avoiding HTTP 400 errors on models with varying max completion thresholds.
  - `ContextLength`: Unified to 1M tokens (`1048576`) for high-context models (e.g., NeuralWatt flash models) and official specifications (e.g., Claude 200k, o3-mini 200k, DeepSeek/Llama 128k, Codestral 256k).

### 2.3 `internal/fanout/client.go`
- **Omitting `max_tokens` when `<= 0`**:
  - `reqBody.MaxTokens` is set only when `target.Model.MaxOutputTokens > 0`.
  - When `MaxOutputTokens <= 0` (e.g. `-1` or `0`), `reqBody.MaxTokens` remains `0` and is completely omitted from the JSON payload via `json:"max_tokens,omitempty"`.
- **Prompt Forwarding**:
  - Ensured messages and prompts are transmitted fully without client-side truncation.

---

## 3. Verification & Testing

### 3.1 Unit Test Suite
- `providers_test.go`:
  - `TestDeriveTags`: Verifies tag synthesis for empty, tools-only, reasoning-only, vision-only, and all-capability configurations.
  - `TestInteractiveSetupWizard`: Updated to verify new custom model flow without tags prompt, confirming default token limits (`1048576`, `131072`) and derived tags (`general`, `tools`).
  - `TestInteractiveSetupWizard_ServerMaximumTokens`: Verifies entering `-1` for both context length and max output tokens preserves `-1` in `providers.json`.
  - `TestInteractiveSetupWizard_NWPreset`: Updated to assert `ContextLength == 1048576` and `MaxOutputTokens == -1`.
- `fanout_test.go`:
  - `TestClient_TokenHandling`: Verifies with an `httptest.Server` that:
    1. `MaxOutputTokens == -1` omits `max_tokens` from JSON.
    2. `MaxOutputTokens == 0` omits `max_tokens` from JSON.
    3. `MaxOutputTokens == 131072` produces `"max_tokens": 131072` in JSON.
    4. Large prompt content (50,000 chars) with `ContextLength == -1` is forwarded completely without truncation.

### 3.2 Test Results
- `go vet ./...`: PASS (0 warnings)
- `go test -v ./...`:
  - `agypro/internal/agenttools`: PASS
  - `agypro/internal/fanout`: PASS (0.804s)
  - `agypro/internal/launcher`: PASS
  - `agypro/internal/mode`: PASS
  - `agypro/internal/paths`: PASS
  - `agypro/internal/providers`: PASS (3.865s)

### 3.3 Binary Rebuild & Diagnostic
- Rebuilt binary to `C:\Users\k1yt\.agy-pro\bin\agy.exe`.
- Tested with `agy pro doctor`:
  - Successfully verified real `agy` locator, running shim, state directory, providers config, and Go toolchain.
