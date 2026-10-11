# One-Touch Bridge Implementation Report

## 1. Executive Summary

This report documents the design, implementation, and deployment of the **One-Touch Bridge** in `agy-pro`. The bridge acts as a zero-configuration, lightweight local OpenAI-compatible HTTP reverse proxy that exposes all external models configured via `agy pro setup` (e.g. DeepSeek, GLM, Qwen, OpenRouter) directly inside the native Antigravity CLI (`agy`) `/model` selector, allowing seamless conversation with any external model without requiring manual gateway configuration.

---

## 2. Architecture & Request Flow

```mermaid
flowchart TD
    User["User Terminal (`agy`)"] -->|Runs CLI| Shim["agy-pro Shim (`launcher.ExecutePassthrough`)"]
    Shim -->|1. providers.Load| Reg["providers.json Registry"]
    Shim -->|2. bridge.StartBridge| Bridge["Local HTTP Bridge (`127.0.0.1:XXXXX`)"]
    Shim -->|3. Inject AGY_LLM_GATEWAY_*| RealAgy["Official agy.exe"]
    RealAgy -->|4. GET /models| Bridge
    Bridge -->|Return model list| RealAgy
    User -->|Selects Model & Prompts| RealAgy
    RealAgy -->|5. POST /chat/completions (SSE / JSON)| Bridge
    Bridge -->|6. Resolve Provider & Inject Auth| Upstream["External Provider API (e.g. NeuralWatt / DeepSeek / OpenRouter)"]
    Upstream -->|7. SSE Stream Data| Bridge
    Bridge -->|8. Transparent Flush (-1)| RealAgy
    RealAgy -->|Render TUI Response| User
    RealAgy -->|Process Exits| Stop["defer stopFunc() cleans up Bridge"]
```

### Key Architectural Characteristics
1. **Zero External Dependencies**: Implemented strictly with the Go standard library (`net/http`, `net/http/httputil`, `net`, `encoding/json`).
2. **Ephemeral Dynamic Port**: Binds to `127.0.0.1:0` to guarantee zero port collisions across concurrent CLI sessions.
3. **Transparent Lifespan**: Started on-demand inside `ExecutePassthrough` when models are present, and guaranteed to stop via `defer stopFunc()` when `realAgy` exits.
4. **Real-time SSE Streaming**: Employs `httputil.ReverseProxy` with `FlushInterval: -1` so that tokens stream into the `agy` TUI without latency or buffering.
5. **Strict Secret Hygiene**: Uses dummy key `AGY_LLM_GATEWAY_API_KEY=agy-pro-local-key` on the bridge listener while injecting the real provider secret into the upstream request's `Authorization: Bearer <key>` header on the fly.

---

## 3. Package Implementation Details

### 3.1 `agypro/internal/bridge` (`bridge.go`)

- **Server Lifecycle**:
  - `StartBridge(reg *providers.Registry) (bridgeURL string, stopFunc func(), err error)`: Binds a local TCP listener on `127.0.0.1:0`, spawns a background `http.Server`, and returns the dynamic URL along with a thread-safe idempotent `stopFunc()`.
  - `NewHandler(reg *providers.Registry) http.Handler`: Mounts routing for both standard and versioned endpoints (`/models`, `/v1/models`, `/chat/completions`, `/v1/chat/completions`, `/health`).

- **`/models` Endpoint**:
  - Inspects `reg.Providers` and aggregates all enabled models (`m.IsEnabled()`).
  - Deduplicates identical model IDs across multiple providers.
  - Returns the OpenAI format:
    ```json
    {
      "object": "list",
      "data": [
        {
          "id": "glm-5.3-flash-flex",
          "object": "model",
          "created": 1773306000,
          "owned_by": "NW"
        }
      ]
    }
    ```

- **`/chat/completions` Endpoint**:
  - Extracts and parses `"model"` from incoming JSON request body.
  - Matches the model using `findBestTarget(reg, modelID)`, prioritizing providers that have active credentials (`p.KeyAvailable()`).
  - Rewrites target `model` to provider's canonical ID (`target.Model.ID`).
  - Dispatches via `httputil.ReverseProxy`:
    - Modifies outgoing headers (`Authorization: Bearer <key>`, `Content-Type: application/json`).
    - Flushes SSE chunks immediately (`FlushInterval: -1`).
    - Catches upstream connection errors and returns a 502 Bad Gateway JSON error payload.

### 3.2 Reasoning Effort Normalization & Compatibility

- **Allowed Options**: `default`, `none`, `low`, `medium`, `high`, `xhigh`, `max` (case-insensitive, normalized via `NormalizeReasoningEffort`). Default value is `default`.
- **Upstream Sanitization Rules**:
  1. **Non-thinking models** (`!SupportsThinking`): `reasoning_effort` is stripped completely. Strict upstream endpoints (such as OpenAI and DeepSeek) reject requests containing unknown fields with HTTP 400 Bad Request.
  2. **Thinking models with `default` or `none`**: `reasoning_effort` is deleted from the outbound JSON payload so upstream servers fall back to their native thinking defaults without rejecting the string `"default"`.
  3. **Thinking models with explicit effort** (`low`, `medium`, `high`, `xhigh`, `max`): Retained and forwarded.
  4. **Fallback to Model Capability**: If the client does not specify `reasoning_effort`, the model's configured preset (`target.Model.Capabilities.ReasoningEffort`) is used if thinking is enabled.

### 3.3 `agypro/internal/launcher` (`launcher.go`)

- **`ExecutePassthrough` Integration**:
  - Reads configured providers via `providers.Load()`.
  - If enabled models exist, calls `bridge.StartBridge(reg)` and defers `stopFunc()`.
  - Injects gateway environment variables:
    - `AGY_LLM_GATEWAY_URL`: Bridge address (e.g. `http://127.0.0.1:54321`)
    - `AGY_LLM_GATEWAY_WIRE_PROTOCOL`: `openai`
    - `AGY_LLM_GATEWAY_API_KEY`: `agy-pro-local-key`
    - `AGY_LLM_GATEWAY_MODELS`: Comma-separated list of enabled model IDs
  - Executes `realAgy` process with inherited stdio.
- **Helper Functions**:
  - `CollectModelIDs(reg *providers.Registry) []string`: Collects unique enabled model IDs across providers.
  - `BuildGatewayEnv(reg *providers.Registry, bridgeURL string) []string`: Builds the 4 environment variable key-value strings.

---

## 4. Test Matrix & Verification

### 4.1 Unit & Integration Test Suites
All test suites pass cleanly across all packages:

| Test Package | Test Function | Purpose / Verified Behavior | Result |
| :--- | :--- | :--- | :--- |
| `internal/bridge` | `TestStartBridge_Lifecycle` | Tests server bind, `/health`, `/models`, clean shutdown, and port release | **PASS** |
| `internal/bridge` | `TestModelsEndpoint` | Tests `/models` and `/v1/models`, deduplication, and disabled model exclusion | **PASS** |
| `internal/bridge` | `TestChatCompletions_StandardJSON` | Tests JSON routing, `Authorization` injection, and response forwarding | **PASS** |
| `internal/bridge` | `TestChatCompletions_SSEStreaming` | Tests Server-Sent Events chunk delivery with immediate flush | **PASS** |
| `internal/bridge` | `TestChatCompletions_ReasoningEffortHandling` | Tests stripping of `default`/`none` and non-thinking models; tests forwarding `low`/`medium`/`high`/`xhigh`/`max` | **PASS** |
| `internal/bridge` | `TestChatCompletions_ErrorHandling` | Tests 400 on missing model, 404 on unknown model, 502 on offline provider | **PASS** |
| `internal/launcher` | `TestBuildGatewayEnv` | Tests injection variable construction with local bridge URL | **PASS** |
| `internal/launcher` | `TestCollectModelIDs` | Tests deduplication and disabled model filtering | **PASS** |
| `internal/launcher` | `TestExecutePassthrough_StartsBridgeAndInjectsEnv` | E2E helper process test verifying live bridge startup, env injection, and clean teardown | **PASS** |

### 4.2 Overall Test Suite Status
```
ok  	agypro/cmd/agy          0.527s
ok  	agypro/internal/agenttools  (cached)
ok  	agypro/internal/bridge      (cached)
ok  	agypro/internal/fanout      (cached)
ok  	agypro/internal/launcher    1.566s
ok  	agypro/internal/mode        (cached)
ok  	agypro/internal/paths       (cached)
ok  	agypro/internal/providers   (cached)
```
Code analysis with `go vet ./...` completed with zero warnings.

---

## 5. Binary Deployment

- The binary was re-compiled and installed to `~/.agy-pro/bin/agy.exe` via `scripts/install.ps1 -NoPause`.
- Verified live command output:
  - `agy --version` -> `1.3.3` (transparent passthrough with bridge injection)
  - `agy pro status` -> Displays 2 configured providers (`NW`, `Charm`) and 4 models (`glm-5.3-flash-flex`, `glm-5.3-flex`, `glm-5.3-flash`, `qwen3.8-flash`)
  - `agy pro version` -> `agy-pro version 0.1.0 (upstream: 1.3.3)`
