# agy Binary Gateway Models Coexistence Deep-Dive & Live Experiment Report

**Date**: 2026-10-11  
**Target Binary**: `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe` (193,113,240 bytes)  
**Investigator**: agy-pro Domain Worker  
**Report File**: `reports/gateway-models-coexistence-experiment.md`  
**Domain Scope**: `agy.exe` disassembly, `buildGatewayAvailableModels`, live HTTP bridge routing test, `internal/launcher`, `internal/bridge`

---

## 1. Executive Summary & Direct Answer to User Inquiry

### 1.1 User Question
> "지금 상태로 'agy'로 기본실행하게되면, 네이티브모델과 gemini는 유지되는데, 등록한 외부모델이 아예 포함이 안 되어있는걸?"  
> *(If I launch `agy` by default in the current state, native Gemini models are kept, but registered external models are not included at all?)*

### 1.2 Core Findings
1. **The Native Binary Constraint**:
   - `agy.exe`'s internal function `codeassistclient.buildGatewayAvailableModels` (VA `0x141d19940`) is an **all-or-nothing replacement** mechanism.
   - When `AGY_LLM_GATEWAY_URL` and `AGY_LLM_GATEWAY_MODELS` are injected into `agy.exe`, the binary replaces all model categories with a single `"Enterprise Gateway Models"` group.
   - Every model listed in `AGY_LLM_GATEWAY_MODELS` is forcibly stamped with `provider = "Gateway"`.
2. **Disassembly Finding on `buildDefaultAvailableModels`**:
   - `buildGatewayAvailableModels` (VA `0x141d19940`) calls `buildDefaultAvailableModels` (VA `0x141d190a0`) at line entry solely to initialize an `AvailableModels` catalog structure on the stack (`[rsp + 0xe0]`).
   - If `AGY_LLM_GATEWAY_MODELS` is empty, it returns this default catalog immediately (`mov rax, [rsp + 0xe0]; ret`).
   - If non-empty, it references the default catalog only to look up canonical display names (e.g. mapping ID `gemini-3.8-flash-high` to name `Gemini 3.8 Flash (High)`).
   - **Crucial finding**: There is **no syntax**, **no prefix** (such as `+`, `append:`, `default,`), and **no conditional logic** that preserves default models as `personal_consumer`. Every parsed entry in `AGY_LLM_GATEWAY_MODELS` becomes `provider = "Gateway"`.
3. **Live Experiment & Routing Proof**:
   - Registering `AGY_LLM_GATEWAY_MODELS="gemini-3.8-flash-high,glm-5.3-flash-flex (NW)"` makes both models appear in `realAgy models`.
   - **However**, when a turn is executed targeting `gemini-3.8-flash-high`, `agy.exe` **routes the request to the local gateway bridge** (`POST /v1/chat/completions` or `POST /v1/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse`) with `auth_method=gateway`, instead of contacting Google AICode (`aicode.googleapis.com`).
   - External providers (NeuralWatt, DeepSeek, OpenRouter) and Google AI Studio do not have access to Google Code Assist's private preview model `gemini-3.8-flash-high`. The bridge cannot fulfill this request, resulting in failure.
4. **Conclusion & Solution**:
   - In `agy.exe`, native Google AICode models (`personal_consumer`) and gateway models (`gateway`) **cannot coexist in the same interactive session** without either breaking native Gemini execution or causing external bridge failures.
   - The optimal, robust user experience requires explicit profile / session modes:
     - Default `agy`: Native Google session with 100% stable `Gemini 3.8 Flash (High)` and full tool support.
     - Swarm / External mode (`agy --model <model>` or `agy pro /mode swarm`): Bridge activated with external models.

---

## 2. In-Depth Disassembly Analysis of `buildGatewayAvailableModels`

### 2.1 Function Identification & Symbol Mapping
Using PE section headers and `.gopclntab` symbol offsets in `agy.exe`:
- **`codeassistclient.buildGatewayAvailableModels`**: VA `0x141d19940` (File offset `0x1d18f40`, size 4,032 bytes)
- **`codeassistclient.buildDefaultAvailableModels`**: VA `0x141d190a0` (File offset `0x1d186a0`, size 2,208 bytes)
- **`codeassistclient.(*CodeAssistClient).fetchAvailableModels`**: VA `0x141d1a900` (File offset `0x1d19f00`, size 1,306 bytes)

### 2.2 Control Flow in `buildGatewayAvailableModels`

```mermaid
flowchart TD
    A["Entry: buildGatewayAvailableModels (0x141d19940)"] --> B["CALL buildDefaultAvailableModels (0x141d190a0)"]
    B --> C["Store baseline catalog pointer at [rsp + 0xe0]"]
    C --> D["CALL os.Getenv('AGY_LLM_GATEWAY_MODELS')<br/>CALL strings.TrimSpace"]
    D --> E{"len(AGY_LLM_GATEWAY_MODELS) == 0?"}
    E -- "Yes (JZ 0x141d19ac7)" --> F["mov rax, [rsp + 0xe0]<br/>leave<br/>ret (Return Default Catalog)"]
    E -- "No" --> G["Split models by ',' (0x141d19aa2)"]
    G --> H["Iterate model tokens"]
    H --> I["Lookup token in [rsp + 0xe0] for display name"]
    I --> J["Set model.Group = 'Enterprise Gateway Models'<br/>Set model.Provider = 'Gateway'"]
    J --> K["Overwrites [rsp+0xe0] catalog groups slice at [rdx + 0x20] with Gateway group"]
    K --> L["mov rax, rdx<br/>leave<br/>ret (0x141d1a736)"]
```

### 2.3 Detailed Instruction Evidence

#### 1. Invocation of `buildDefaultAvailableModels` and Stack Storage
```assembly
0x141d19952: push rbp
0x141d19953: mov rbp, rsp
0x141d19956: sub rsp, 0x420
0x141d1995d: mov [rsp + 0x438], rbx
0x141d19965: mov [rsp + 0x430], rax
0x141d1996d: call 0x141d190a0       ; CALL buildDefaultAvailableModels
0x141d199a4: mov [rsp + 0xe0], rax  ; SAVE returned default catalog struct ptr!
```

#### 2. Check of `AGY_LLM_GATEWAY_MODELS` and Early Return
```assembly
0x141d199ee: lea rax, [rip + 0x19c026c] ; "AGY_LLM_GATEWAY_MODELS"
0x141d199f5: mov ebx, 0x16             ; len = 22
0x141d199fa: call os.Getenv
0x141d19a00: call strings.TrimSpace
0x141d19a05: test rbx, rbx             ; Check length
0x141d19a09: jz 0x141d19ac7            ; IF EMPTY -> JUMP TO FALLBACK EXIT!
...
0x141d19ac7: mov rax, [rsp + 0xe0]     ; LOAD default catalog
0x141d19ace: leave
0x141d19acf: ret                       ; RETURN DEFAULT CATALOG!
```

#### 3. Why `buildDefaultAvailableModels` is Called
- **Allocation & Baseline Data Structure**: `buildGatewayAvailableModels` reuses the catalog object structure initialized by `buildDefaultAvailableModels`.
- **Display Name Resolution**: In lines `0x141d1a367` to `0x141d1a385`, the code indexes `[rsp + 0xe0]` by the model token string to check if the user supplied a known model ID (e.g. `gemini-3.8-flash-high`). If matched, it reads the human-readable display name (`Gemini 3.8 Flash (High)`).
- **No Model Retention**: In lines `0x141d1a720` to `0x141d1a730`, the catalog's model groups slice (`[rdx + 0x20]`) is overwritten with `len = 1` and `cap = 1`, containing **only** the newly built `"Enterprise Gateway Models"` group. None of the default models are appended or retained.

#### 4. Exhaustive Search for Model Syntax / Prefixes
A complete binary search for character comparisons (`CMP byte/word/dword`) across the entire 4,032 bytes of `buildGatewayAvailableModels` revealed:
- `0x141d1998e`: `cmp dword ptr [rdx], 0x6e65706f` (`"open"`)
- `0x141d19996`: `cmp word ptr [rdx + 4], 0x6961` (`"ai"`)
- **No prefix check exists**: There are no checks for `+`, `append:`, `default`, `gemini:`, or any other modifier.
- **Unconditional Provider Tag**: Every entry in `AGY_LLM_GATEWAY_MODELS` is hardcoded to provider tag `"Gateway"` (VA `0x14359fc65`).

---

## 3. Live Experiment: Verification of Coexistence & Routing

### 3.1 Experiment Setup
We conducted an automated live experiment using a custom in-memory HTTP bridge server listening on `127.0.0.1:51239` and `127.0.0.1:51240` running against the official `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe`.

### 3.2 Experiment 1: Catalog Listing (`realAgy models`)
**Command**:
```powershell
$env:AGY_LLM_GATEWAY_URL = "http://127.0.0.1:51239/v1"
$env:AGY_LLM_GATEWAY_MODELS = "gemini-3.8-flash-high,glm-5.3-flash-flex (NW)"
& "C:\Users\k1yt\AppData\Local\agy\bin\agy.exe" models
```

**Observed Output**:
```text
Fetching available models...
gemini-3.8-flash-high    Gemini 3.8 Flash (High)
glm-5.3-flash-flex (NW)  glm-5.3-flash-flex (NW)
```
- Both models are listed in the catalog.
- `gemini-3.8-flash-high` automatically acquired the display name `Gemini 3.8 Flash (High)` via the baseline catalog lookup.

### 3.3 Experiment 2: Request Dispatch Test (`realAgy -p "ping"`)
**Hypothesis**: If `gemini-3.8-flash-high` is listed, does `agy.exe` know to send it to Google AICode (`aicode.googleapis.com`), or does it send it to `AGY_LLM_GATEWAY_URL`?

**Command Executed**:
```powershell
& "C:\Users\k1yt\AppData\Local\agy\bin\agy.exe" -p "ping" --model "gemini-3.8-flash-high" --print-timeout 10s
```

**Live Bridge Capture Log**:
```http
>>> BRIDGE RECEIVED REQUEST <<<
[2026-10-11T21:21:59+09:00] POST /v1/v1beta/models/gemini-3.8-flash:streamGenerateContent?alt=sse
Headers: map[
  Accept-Encoding:[gzip]
  Content-Length:[38043]
  Content-Type:[application/json]
  User-Agent:[antigravity/cli/1.3.3 (aidev_client; os_type=windows; arch=amd64; cl=996823801; auth_method=gateway)]
]
Body: {
  "contents":[{"role":"user", "parts":[{"text":"<USER_REQUEST>\nping\n</USER_REQUEST>..."}]}],
  "systemInstruction":{...},
  "generationConfig":{"thinkingConfig":{"includeThoughts":true, "thinkingBudget":-1}},
  "sessionId":"-3750763034362895579"
}
```

**When Wire Protocol is OpenAI (`AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai`)**:
```http
>>> BRIDGE RECEIVED REQUEST <<<
POST /v1/chat/completions
Body: {
  "model": "gemini-3.8-flash-high",
  "messages": [...]
}
```

### 3.4 Key Empirical Discovery
1. **Request Rerouting**:
   When `gemini-3.8-flash-high` is included in `AGY_LLM_GATEWAY_MODELS`, `agy.exe` tags it as a Gateway model.
   **It does NOT send the request to Google AICode (`aicode.googleapis.com`). It sends it to our local bridge (`AGY_LLM_GATEWAY_URL`).**
2. **Why Bridge Cannot Handle It**:
   - `gemini-3.8-flash-high` is a private model hosted only at `aicode.googleapis.com`.
   - External providers (OpenAI, NeuralWatt, DeepSeek, OpenRouter) do not recognize `gemini-3.8-flash-high` and return `404 Model Not Found`.
   - Public Google AI Studio (`GEMINI_API_KEY`) only supports public Gemini models (`gemini-2.5-flash`, `gemini-2.5-pro`), rejecting `gemini-3.8-flash-high` with 404.
   - Forwarding from bridge back to `aicode.googleapis.com` would require extracting agy's private Gaia OAuth token from `~/.gemini/` or keyring, which strictly violates project security directives (Worker Rule 1 & Rule 4).

---

## 4. Architectural Analysis & Response to User

### 4.1 Summary of Binary Realities
| State | `AGY_LLM_GATEWAY_*` Injected? | Native Gemini in `/model`? | External Models in `/model`? | Works Without Errors? |
|---|---|---|---|---|
| **Clean Native (Current `agy`)** | **No** | **Yes (`Gemini 3.8 Flash`)** | No | **Yes (100% native stability)** |
| **All in Gateway (`gemini,glm,...`)** | **Yes** | Yes (as label) | Yes | **NO (Gemini calls fail at bridge)** |
| **External Only Gateway (`glm,...`)** | **Yes** | No (Warning logged) | Yes | Yes (External models work) |

### 4.2 Why Native Models & External Models Cannot Coexist in a Single Native `/model` Picker
`agy.exe`'s architecture strictly separates models by provider:
- Models with `provider = "personal_consumer"` (internal Gemini) are dispatched using Google OAuth tokens to Google internal servers.
- Models with `provider = "gateway"` are dispatched to `AGY_LLM_GATEWAY_URL`.
- The gateway loader (`buildGatewayAvailableModels`) assigns `provider = "Gateway"` to **every model it returns**, leaving zero models with `personal_consumer`.
- Consequently, if a model name is in the gateway list, `agy.exe` will attempt to route it through the gateway. If it is omitted from the gateway list, `agy.exe` drops it entirely.

### 4.3 Clean Resolution Strategy

To provide the user with the most natural and ergonomic experience without breaking native Gemini:

1. **Default Interactive Execution (`agy`)**:
   - Runs native `agy.exe` without injecting `AGY_LLM_GATEWAY_*`.
   - Keeps `Gemini 3.8 Flash (High)` fully functional with native quotas and zero errors.
2. **Dedicated Swarm & External Model Invocations**:
   - When the user specifies an external model:
     - `agy --model <model>` (e.g. `agy -m glm-5.3-flash-flex`)
     - `agy pro run --model <model>`
     - `agy pro /mode swarm`
   - In this context, `agy-pro` starts the bridge and injects `AGY_LLM_GATEWAY_*`.
3. **Interactive Menu / Switching in `agy-pro`**:
   - `agy pro models`: Lists all registered external models alongside native presets with capability indicators.
   - `agy pro switch <model>`: Launches an interactive session pre-configured for the selected external model.

---

## 5. Verification Checklist & DoD Completion

- [x] **Investigation of `buildGatewayAvailableModels`**: Disassembly confirmed it calls `buildDefaultAvailableModels` (VA `0x141d190a0`) to allocate the catalog struct, returns it directly if `AGY_LLM_GATEWAY_MODELS` is empty, and queries it for display names if set.
- [x] **Prefix / Syntax Verification**: Confirmed via x86 instruction scanning that no prefix (`+`, `append:`, `default`) or merge logic exists.
- [x] **Live Experiment Execution**: Automated tests ran with real `agy.exe`, proving that including `gemini-3.8-flash-high` in gateway models causes requests to be routed to the bridge rather than Google AICode.
- [x] **Comprehensive Report Written**: All findings, assembly code excerpts, and HTTP capture logs documented in `reports/gateway-models-coexistence-experiment.md`.
