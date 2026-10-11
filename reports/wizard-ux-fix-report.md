# agy pro setup UX & Abrupt Exit Investigation & Fix Report

## 1. Executive Summary

This report documents the root-cause analysis, architecture redesign, implementation, and end-to-end verification of `agy pro setup`.

- **Issue**: When running `agy pro setup`, after providing the Provider Name, Base URL, and API Key, the wizard immediately aborted with an error (`[agy-pro] Setup failed: invalid environment variable name: ...`) without allowing the user to select or configure models.
- **Root Cause**:
  1. **UX Ambiguity & Regex Failure**: Step 3 previously prompted `API Key environment variable name [NW_API_KEY]:`. Natural users expected to paste their actual API Key (`sk-...`). When a raw API token was pasted, `envNameRe.MatchString(apiKeyEnv)` rejected the token because of lowercase letters and hyphens, instantly terminating the setup process with an error before reaching model configuration.
  2. **PowerShell Stdin UTF-8 BOM (`\ufeff`) Interference**: In Windows PowerShell pipelines and console redirects, a UTF-8 Byte Order Mark (`\ufeff`) prepended to Stdin caused string prefix mismatches in provider lookups and regex failures.
  3. **High UX Friction for Model Configuration**: Previously, adding a model required answering 10+ manual questions (Model ID, Model Family, Context Length, Max Output, Tags, 4 separate capability questions). There were no presets or numbered menus.
- **Resolution**:
  1. Reordered and simplified questions: The wizard now prompts directly for the **API Key**, while automatically generating the corresponding environment variable name (`<PROVIDER>_API_KEY`). If a raw key is pasted at any prompt, it is smartly detected via `isLikelyAPIKey()` and absorbed without crashing.
  2. Integrated curated **Model Presets** and dynamic `/models` endpoint discovery: When a provider is chosen (DeepSeek, OpenRouter, Groq, OpenAI, NW, Together, Mistral, etc.), top recommended models are presented in a numbered menu (1, 2, 3...). Pressing Enter or typing a number instantly populates the Model ID, Context Length, Max Tokens, and Capabilities.
  3. Immediate **Live Connection Probe** (`ProbeEndpoint`) with latency measurement and clear pass/fail status and interactive recovery options.
  4. Robust **Windows User Environment Persistence** using base64-encoded PowerShell invocation and registry (`reg.exe`) fallback in `KeyValue()`, allowing immediate use without shell restarts.
  5. Strictly enforced the security invariant: Only environment variable names (`apiKeyEnv: "NW_API_KEY"`) are stored in `~/.agy-pro/providers.json`. Real keys are never persisted to JSON or repository files.

---

## 2. Root Cause Analysis

### 2.1 The Abrupt Exit Mechanism

In `agy-pro/internal/providers/wizard.go`, the original interaction was structured as follows:

```go
// Step 3: API Key Environment Variable Name
defaultEnv := defaultEnvFor(provName)
apiKeyEnv := promptString(reader, out, "API Key environment variable name", defaultEnv)
if apiKeyEnv != "" && !envNameRe.MatchString(apiKeyEnv) {
    return fmt.Errorf("invalid environment variable name: %s", apiKeyEnv) // <-- ABORT HERE
}

// Step 4: API Key Value (optional entry)
keyValue := promptString(reader, out, fmt.Sprintf("Value for %s (leave blank to skip)", apiKeyEnv), "")
```

When a user ran `agy pro setup`:
```text
Provider name: NW
OpenAI-compatible Base URL: https://api.neuralwatt.com/v1
API Key environment variable name [NW_API_KEY]: sk-a83a...
[agy-pro] Setup failed: invalid environment variable name: sk-a83a...
```

The user pasted their API key (`sk-...`) at the prompt displaying `[NW_API_KEY]`. Because the token contains hyphens and lowercase hexadecimal characters, it failed `envNameRe` (`^[A-Za-z_][A-Za-z0-9_]*$`). `InteractiveSetupWizard` returned an error, which `runSetup()` caught and called `os.Exit(1)`.

### 2.2 PowerShell Stdin UTF-8 BOM (`\ufeff`)

When commands or strings are piped to `agy.exe` in Windows PowerShell (`$input | agy pro setup`), PowerShell prefixes standard input with a 3-byte UTF-8 Byte Order Mark (`0xEF 0xBB 0xBF`, `\ufeff`). This caused:
- Provider name `NW` to become `\ufeffNW`.
- `defaultEnvFor("\ufeffNW")` to generate `\ufeffNW_API_KEY`.
- `envNameRe.MatchString("\ufeffNW_API_KEY")` to fail due to the non-ASCII BOM character.

---

## 3. Architecture & UX Redesign

### 3.1 Seamless API Key & Environment Variable Intake

The interaction flow was overhauled:
1. **Provider Name**: e.g., `deepseek`, `openrouter`, `groq`, `openai`, `NW`. Stripped of BOM and whitespace.
2. **Base URL**: Pre-filled with provider-specific default URL (e.g. `https://api.neuralwatt.com/v1`).
3. **API Key Prompt**: Direct question for API key (`API Key for NW (will be stored in environment variable NW_API_KEY):`).
   - If an existing environment variable is found in the current process or Windows User registry: `Detected existing environment variable NW_API_KEY. Press Enter to reuse, or paste new key:`.
   - Heuristic Absorption: `isLikelyAPIKey(val)` detects keys by common prefixes (`sk-`, `gsk_`, `key-`, `nvapi-`) or format (contains `-` or `.`, or length $\ge 24$). Even if an API key is pasted at any variable name prompt, the wizard identifies it as a key value and automatically maintains the clean env variable name (`NW_API_KEY`).
4. **Persistent Storage**:
   - Process environment: `os.Setenv(apiKeyEnv, keyValue)`.
   - Windows User Environment: Persisted via Base64-encoded script avoiding quote escaping hazards.
   - Hot Reloading: `Provider.KeyValue()` checks process environment and falls back to `reg query HKCU\Environment` on Windows, enabling immediate availability in the existing terminal session.

### 3.2 Curated Model Presets & Dynamic `/models` Discovery

Added `internal/providers/presets.go`:
- Built-in curated presets for top providers:
  - **DeepSeek**: `deepseek-chat` (V3 - 128k context, tools enabled) [Recommended], `deepseek-reasoner` (R1 - 128k, reasoning)
  - **OpenRouter**: `anthropic/claude-3.7-sonnet` (200k, hybrid thinking/tools) [Recommended], `deepseek/deepseek-r1`, `meta-llama/llama-3.3-70b-instruct`, `openai/gpt-4o`
  - **Groq**: `llama-3.3-70b-versatile` (128k, tools) [Recommended], `deepseek-r1-distill-llama-70b`
  - **OpenAI**: `gpt-4o` [Recommended], `gpt-4o-mini`, `o3-mini`
  - **NeuralWatt (NW)**: `nw-flash` (128k, tools) [Recommended], `deepseek-v4-flash`, `glm-5.3-flash`, `qwen-3.8-27b`
  - **Together**: `meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo` [Recommended], `deepseek-ai/DeepSeek-R1`
  - **Mistral**: `mistral-large-latest` [Recommended], `codestral-latest`
- **Dynamic `/models` Discovery**: For custom or self-hosted endpoints (vLLM, Ollama, NeuralWatt), `FetchRemoteModels(baseURL, apiKey)` queries `<baseURL>/models` with a 3s timeout. If no presets exist, discovered models are automatically offered in the menu.
- **Single-Key Model Selection**: The user simply presses Enter to select the recommended model `[1]`, or types a number. All attributes (Family, Context Length, Max Output Tokens, Tags, Capabilities) are pre-configured automatically.

### 3.3 Live Endpoint Probing & Verification

As soon as a model is chosen, `ProbeEndpoint(baseURL, apiKey, modelID)` executes a minimal chat completion request:
- Success displays: `✅ Connection succeeded! Latency: 552ms`.
- Failure displays: Detailed HTTP status code, response snippet, and options to save anyway, retry with modified settings, or discard.

### 3.4 Summary Confirmation Banner

Upon saving to `~/.agy-pro/providers.json`, a clear overview is printed:
```text
===========================================================
  ✅ Provider Setup Complete!
===========================================================
Provider        : NW
Base URL        : https://api.neuralwatt.com/v1
API Key Env     : NW_API_KEY (saved in environment)
Config File     : C:\Users\<user>\.agy-pro\providers.json
Configured Models (1):
  1. nw-flash                 [family: nw, context: 128k, tools: yes]
===========================================================
Ready to use! Example:
  agy pro fanout -m NW/nw-flash "your prompt"
===========================================================
```

---

## 4. Verification & Testing

### 4.1 Unit Test Suite

Added unit tests in `internal/providers/providers_test.go`:
- `TestGetModelPresets`: Validates preset integrity across all recognized providers.
- `TestIsLikelyAPIKey`: Validates key heuristics for standard keys and variable names.
- `TestFetchRemoteModels`: Validates dynamic model querying against mock HTTP server.
- `TestInteractiveSetupWizard_DeepSeekPreset`: End-to-end simulation of preset selection.
- `TestInteractiveSetupWizard_NWPreset`: End-to-end simulation of NW preset selection.
- `TestInteractiveSetupWizard_PastedRawKeyAbsorption`: Verifies that pasting raw keys does not crash and preserves the environment variable name invariant.

Test Execution Results:
```text
go test -v ./internal/providers/...
=== RUN   TestValidate                                       --- PASS (0.00s)
=== RUN   TestLoadFrom_NotExist                              --- PASS (0.00s)
=== RUN   TestUnsetEnvVars                                   --- PASS (0.03s)
=== RUN   TestInferFamily                                    --- PASS (0.00s)
=== RUN   TestRedactor                                       --- PASS (0.00s)
=== RUN   TestSelectionAndAlternates                         --- PASS (0.00s)
=== RUN   TestInteractiveSetupWizard                         --- PASS (0.71s)
=== RUN   TestGetModelPresets                                --- PASS (0.00s)
=== RUN   TestIsLikelyAPIKey                                 --- PASS (0.00s)
=== RUN   TestFetchRemoteModels                              --- PASS (0.00s)
=== RUN   TestInteractiveSetupWizard_DeepSeekPreset          --- PASS (0.70s)
=== RUN   TestInteractiveSetupWizard_NWPreset                --- PASS (0.68s)
=== RUN   TestInteractiveSetupWizard_PastedRawKeyAbsorption  --- PASS (0.57s)
PASS
ok      agypro/internal/providers       3.361s
```

All package tests across `agy-pro` (`agenttools`, `fanout`, `launcher`, `mode`, `paths`, `providers`) pass with 0 failures.

### 4.2 Live Provider Test (NeuralWatt)

Live test against `https://api.neuralwatt.com/v1`:
- Probed `nw-flash`: Succeeded in 552ms.
- Saved to `~/.agy-pro/providers.json`.
- Verified `agy pro status`: `NW : 1 models [configured]`.
- Verified `agy pro doctor`: `All 1 configured providers have API keys set. [PASS]`.
- Verified `~/.agy-pro/providers.json`: Stores strictly `"apiKeyEnv": "NW_API_KEY"`, zero raw secrets stored.

---

## 5. Security Invariant Verification

1. **No API Keys in Repository**: No credentials or tokens exist in source code, test files, configs, reports, or git history.
2. **Registry Integrity**: `~/.agy-pro/providers.json` stores solely `apiKeyEnv: "NW_API_KEY"`.
3. **Cross-Platform Compatibility**: No hardcoded user directories; utilizes `os.UserHomeDir()`, `path/filepath`, and standard library utilities.
