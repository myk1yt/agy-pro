# AGY Native Model Selection & Gateway Bridge Guide

> **Target Audience**: Antigravity CLI (`agy`) & `agy-pro` Users, Autonomous Orchestrator Engineers  
> **Status**: Verified & Reverse-Engineered  
> **Applicable Version**: `agy` v1.3.3 / `agy-pro` v0.1.0  
> **Security Clearance**: Non-confidential (All paths sanitized to virtual paths `~/.gemini`, `~/.agy-pro`; zero plaintext secrets)

---

## 1. Executive Summary & Answer to the User Question

### ❓ 사용자의 질문
> *"그리고 agy pro mode로 1번(full-stack) 선택 후에 agy 들어갔는데, provider의 모델선택은 어디서 하는거야? 모델을 선택해야하잖아"*

### 💡 핵심 답변 (요약)
`agy-pro` 환경에서 모델은 **두 가지 계층(2-Tier Layer)**으로 구분되어 동작합니다:

1. **메인 대화 세션 모델 (Native Session Model)**:
   - `agy` 실행 시 진입하는 TUI 대화창에서 답변하는 **VP(Vice President) 오케스트레이터 모델**입니다.
   - **선택 방법**: 
     - **대화창 내부**: `/model` 입력 (실시간 퍼지 검색 팝업창 오픈) 또는 `/model <모델ID>` 입력
     - **실행 시점**: `agy --model <모델ID>`
     - **목록 확인**: `agy models`
     - 기본 제공: Gemini 3.8/3.7/3.1, Claude 3.5 Sonnet / Opus, GPT-OSS 120B 등
2. **외부 프로바이더 스웜 모델 (Multi-Model Swarm / Fanout Models)**:
   - `agy pro setup`을 통해 등록한 외부 모델(OpenAI GPT-4o, DeepSeek-V3/R1, Ollama, OpenRouter 등)입니다.
   - `full-stack` 모드에서는 메인 세션의 VP 오케스트레이터가 코드 리뷰, 보안 검증, 아키텍처 비교를 수행할 때 **`agy pro fanout`을 통해 백그라운드에서 자동으로 다중 모델을 선출·로테이션하여 병렬 호출**합니다.
   - 사용자가 직접 특정 모델을 지정해 스웜을 실행하려면:
     ```bash
     agy pro fanout --models deepseek-chat,gpt-4o "리팩토링 분석 요청"
     ```
3. **메인 대화 세션 자체를 외부 모델로 변경하고 싶은 경우**:
   - `agy` CLI 바이너리가 네이티브로 지원하는 **`AGY_LLM_GATEWAY_*` 환경 변수**를 통해 외부 OpenAI 호환 엔드포인트를 메인 세션 모델로 직접 라우팅할 수 있습니다 (상세 내용은 4장 참조).

---

## 2. 계층별 모델 아키텍처 다이어그램

```mermaid
flowchart TB
    subgraph UserInteraction [사용자 터미널 상호작용]
        CLI_START["agy 실행 (메인 TUI 대화 세션)"]
        FANOUT_CMD["agy pro fanout 실행 (수동 스웜 실행)"]
    end

    subgraph Tier1 [Tier 1: 메인 대화 세션 모델 (Antigravity Native)]
        direction TB
        SLASH_MODEL["/model (인터랙티브 퍼지 선택창)"]
        FLAG_MODEL["agy --model claude-sonnet-5-5-high"]
        SETTINGS["~/.gemini/antigravity-cli/settings.json"]
        VP_ENGINE["VP of Engineering (Master Orchestrator)"]
        
        SLASH_MODEL --> VP_ENGINE
        FLAG_MODEL --> VP_ENGINE
        SETTINGS --> VP_ENGINE
    end

    subgraph Tier2 [Tier 2: 외부 프로바이더 스웜 (agy-pro Swarm)]
        direction TB
        REGISTRY["~/.agy-pro/providers.json (API 키 환경변수 참조)"]
        SELECTOR["자동 패밀리 다양성 로테이션 (selector.go)"]
        SWARM_WORKERS["Double-Blind 병렬 실행 (N Seats)"]
        ARBITER["VP Arbiter 합의 & 대조 행렬 (reconciliation.md)"]

        REGISTRY --> SELECTOR --> SWARM_WORKERS --> ARBITER
    end

    subgraph Tier3 [Tier 3: 네이티브 게이트웨이 브릿지 (Advanced)]
        GATEWAY_ENV["AGY_LLM_GATEWAY_URL / WIRE_PROTOCOL"]
        LOCAL_PROXY["LiteLLM / agy-pro Local Bridge"]
        GATEWAY_ENV --> LOCAL_PROXY
    end

    CLI_START --> VP_ENGINE
    FANOUT_CMD --> Tier2
    VP_ENGINE -.->|"코드리뷰/보안리뷰 시 자동 호출"| Tier2
    LOCAL_PROXY -.->|"메인 세션 외부 모델 브릿지"| VP_ENGINE
```

---

## 3. Tier 1: Antigravity CLI 메인 대화 세션 모델 선택 가이드

사용자가 `agy`를 실행했을 때 터미널에서 작동하는 기본 모델 제어 방식입니다.

### 3.1 세션 내부 인터랙티브 모델 선택 (`/model`)
`agy` 대화창이 켜진 상태에서 프롬프트에 슬래시 명령어를 입력합니다.

| 명령어 | 동작 설명 |
|---|---|
| `/model` | **실시간 퍼지 검색 모델 선택창**이 팝업됩니다. 화살표 키와 검색어로 모델을 탐색한 뒤 `Enter`로 선택합니다. |
| `/model <모델ID>` | 해당 모델로 즉시 전환되며, 세션의 기본 모델로 저장됩니다. (예: `/model claude-sonnet-5-5-high`) |
| `/model <모델ID> <질문>` | **단발성 모델 호출**: 현재 세션은 유지한 채, 해당 질문 1회만 지정한 모델로 처리하고 원래 모델로 복귀합니다. |
| `Tab` 자동완성 | `/model` 입력 중 모델명 힌트(Ghost text)가 표시될 때 `Tab` 키로 완성할 수 있습니다. |

### 3.2 CLI 실행 플래그를 통한 모델 선택
세션을 시작할 때 특정 모델을 지정하여 실행할 수 있습니다:
```bash
# Claude 3.5 Sonnet 고성능 모드로 agy 시작
agy --model claude-sonnet-5-5-high

# Gemini 3.8 Flash 기본 모드로 agy 시작
agy --model gemini-3.8-flash-high

# 추론 강도(effort) 지정
agy --model claude-sonnet-5-5-high --effort high
```

### 3.3 사용 가능한 네이티브 모델 목록 확인
```bash
agy models
```
**출력 결과 예시:**
```text
gemini-3.8-flash-high    Gemini 3.8 Flash (High)
gemini-3.8-flash-medium  Gemini 3.8 Flash (Medium)
gemini-3.8-flash-low     Gemini 3.8 Flash (Low)
gemini-3.7-flash-high    Gemini 3.7 Flash (High)
gemini-3.1-pro-high      Gemini 3.1 Pro (High)
claude-opus-5-5-high     Claude Opus 5.5 (High)
claude-sonnet-5-5-high   Claude Sonnet 5.5 (High)
gpt-oss-120b-medium      GPT-OSS 120B (Medium)
```

### 3.4 영구 기본 모델 설정 (`settings.json`)
기본 모델은 `~/.gemini/antigravity-cli/settings.json` 파일에 저장됩니다:
```json
{
  "model": "Claude Sonnet 5.5 (High)",
  "useG1Credits": true,
  "verbosity": "high"
}
```

---

## 4. Tier 2: agy-pro 외부 프로바이더 스웜 모델 동작 원리

`agy pro setup`을 통해 외부 프로바이더(DeepSeek, OpenAI, Groq, Ollama 등)를 등록했을 때, 이 모델들이 어떻게 사용되는지에 대한 메커니즘입니다.

### 4.1 프로바이더 등록 및 보안 저장소 (`~/.agy-pro/providers.json`)
`agy pro setup` 대화형 위저드를 완료하면 설정은 `~/.agy-pro/providers.json`에 저장됩니다.
- **보안 불변성**: 실제 API 키 평문은 절대 저장되지 않으며, 환경 변수 이름(`apiKeyEnv`)만 기록됩니다.
- 키 값 자체는 사용자의 시스템 환경 변수(Windows User Environment 등)에 안전하게 격리됩니다.

```json
{
  "version": 1,
  "providers": [
    {
      "name": "deepseek",
      "baseUrl": "https://api.deepseek.com/v1",
      "apiKeyEnv": "DEEPSEEK_API_KEY",
      "models": [
        {
          "id": "deepseek-chat",
          "family": "deepseek",
          "contextLength": 64000,
          "capabilities": {
            "supportsTools": true,
            "supportsVision": false,
            "supportsThinking": true
          }
        }
      ]
    },
    {
      "name": "openai",
      "baseUrl": "https://api.openai.com/v1",
      "apiKeyEnv": "OPENAI_API_KEY",
      "models": [
        {
          "id": "gpt-4o",
          "family": "openai",
          "contextLength": 128000,
          "capabilities": {
            "supportsTools": true,
            "supportsVision": true
          }
        }
      ]
    }
  ]
}
```

### 4.2 full-stack 모드에서의 자동 선출 (Autonomous Swarm Rotation)
1. **VP Orchestrator의 역할**:
   - `agy pro mode`에서 `1. full-stack`을 선택하면 활성화되는 엔지니어링 룰(`rules/AGENTS.md`)에 따라 메인 세션의 에이전트는 **VP of Engineering**으로 동작합니다.
2. **배경 스웜 호출**:
   - VP 에이전트는 복잡한 코드 작성, 보안 감사, 아키텍처 결정 시 단일 모델의 편향이나 환각을 방지하기 위해 `agy pro fanout` 명령을 백그라운드 도구로 호출합니다.
3. **다양성 보장 로테이션 알고리즘 (`internal/fanout/selector.go`)**:
   - 등록된 프로바이더 중 서로 다른 모델 패밀리(`deepseek`, `openai`, `anthropic`, `llama` 등)를 골고루 배정합니다.
   - 최근 실행 이력(`~/.agy-pro/state-fanout.json`)을 조회하여 가장 오랫동안 선택되지 않은 패밀리를 우선 선출합니다.

### 4.3 수동 스웜 실행 및 모델 지정 방법
사용자가 CLI 또는 터미널에서 직접 외부 모델 스웜을 실행할 수 있습니다:

```bash
# 1. 자동 선출 스웜 (기본 3개 서로 다른 패밀리 모델 병렬 질의)
agy pro fanout "대규모 웹소켓 연결 처리 아키텍처 비교 분석"

# 2. 특정 외부 모델 명시적 지정 (--models)
agy pro fanout --models deepseek-chat,gpt-4o "인증 미들웨어 보안 취약점 검토"

# 3. 사전 정의 태스크 프리셋 활용 (--task)
agy pro fanout --task security "src/auth/token.go 검토"
agy pro fanout --task review "PR #42 변경사항 리뷰"
agy pro fanout --task plan-audit "마이그레이션 계획 검증"

# 4. 블라인드 채점 및 VP Arbiter 합의 매트릭스 출력 (--arbiter)
agy pro fanout --arbiter "PostgreSQL vs DynamoDB 동시성 제어 비교"
```

실행 후 생성되는 합의 리포트 위치:
- 개별 모델 리포트: `reports/fanout/<timestamp>/seat-<n>-<model>.md`
- 종합 의사결정 매트릭스: `reports/fanout/<timestamp>/reconciliation.md`

---

## 5. Tier 3: 메인 대화 세션에 외부 모델 연동 (LLM Gateway Bridge 심층 분석)

`agy` CLI 바이너리 내부(`jetski/gateway/gateway`)를 리버스 엔지니어링하여 밝혀낸 **메인 세션 외부 모델 라우팅 기술**입니다.

### 5.1 네이티브 LLM 게이트웨이 환경 변수 (`AGY_LLM_GATEWAY_*`)
`agy.exe`는 엔터프라이즈 사내 게이트웨이(LiteLLM 등)를 네이티브로 수용할 수 있는 전용 인터페이스를 내장하고 있습니다.

| 환경 변수 | 설명 | 예시 값 |
|---|---|---|
| `AGY_LLM_GATEWAY_URL` | 게이트웨이 엔드포인트 기본 주소 | `http://localhost:4000` 또는 `https://llm-gateway.internal` |
| `AGY_LLM_GATEWAY_WIRE_PROTOCOL` | 프로토콜 포맷 (`jetski/gateway` 바이너리 검증) | `openai` 또는 `vertex` |
| `AGY_LLM_GATEWAY_API_KEY` | 게이트웨이 인증 토큰 | `sk-gateway-secret` |
| `AGY_LLM_GATEWAY_MODELS` | CLI의 `agy models` 및 `/model`에 노출할 모델 목록 (콤마 구분) | `deepseek-chat,gpt-4o,claude-3-5-sonnet` |
| `AGY_LLM_GATEWAY_HEADERS` | 커스텀 HTTP 헤더 | `X-Custom-Auth: value,X-Tenant: team1` |
| `AGY_LLM_GATEWAY_PROXY_URL` | 아웃바운드 프록시 주소 | `http://proxy.corp:8080` |

> [!TIP]
> **LiteLLM 또는 로컬 프록시 브릿지 활용**:  
> 로컬 머신에서 LiteLLM 프록시(`http://localhost:4000`)를 실행하고 `AGY_LLM_GATEWAY_URL=http://localhost:4000`, `AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai`로 설정하면, `agy`의 메인 대화 세션 자체가 DeepSeek이나 로컬 Ollama 모델을 메인 LLM으로 삼아 동작할 수 있습니다.

### 5.2 Direct Gemini 엔드포인트 연동 (`settings.json`)
`~/.gemini/antigravity-cli/settings.json`에서 공식 로그인 세션 대신 독자적인 엔드포인트를 지정하는 방법입니다:
```json
{
  "modelProvider": "gemini"
}
```
환경 변수:
- `GEMINI_API_KEY`: 사용자 개인 Gemini API 키
- `GOOGLE_GEMINI_BASE_URL`: 커스텀 프록시 주소 (예: `https://my-gemini-proxy.com/v1beta`)

### 5.3 시스템 관리자 설정 (`admin_settings.json`)
바이너리에서 확인된 플랫폼별 관리자 설정 경로:
- **Windows**: `%PROGRAMDATA%\Antigravity\admin_settings.json`
- **Linux**: `/etc/antigravity/admin_settings.json`
- **macOS**: `/Library/Application Support/Antigravity/admin_settings.json`

형식:
```json
{
  "gateway": {
    "url": "http://localhost:4000",
    "wireProtocol": "openai",
    "apiKey": "dummy-token"
  },
  "models": [
    {
      "modelId": "deepseek-chat",
      "displayName": "DeepSeek V3 (External Gateway)",
      "supportsThinking": true
    }
  ]
}
```

---

## 6. 사용자 조치 가이드 (Step-by-Step)

사용자의 현재 상황에 맞춘 명확한 단계별 행동 요령입니다:

### 상황 A: "나는 agy 세션 안에서 메인으로 대화할 모델(Claude, Gemini 등)을 고르고 싶다"
1. 터미널에서 `agy`를 실행합니다.
2. 프롬프트 창에 `/model`을 입력하고 `Enter`를 누릅니다.
3. 목록에서 원하는 모델(예: `claude-sonnet-5-5-high`)을 선택합니다.
4. 다음 실행 때도 기본으로 쓰고 싶다면:
   - `agy --model claude-sonnet-5-5-high`로 실행하거나,
   - `~/.gemini/antigravity-cli/settings.json`의 `"model"` 값을 수정합니다.

### 상황 B: "나는 `agy pro setup`으로 등록한 DeepSeek/OpenAI 모델을 쓰고 싶다"
1. **스웜(다중 모델 교차 검증)으로 실행하기**:
   - 터미널에서 바로 실행:
     ```bash
     agy pro fanout "이 코드의 성능 병목을 찾아줘"
     ```
   - 특정 모델만 콕 집어서 실행:
     ```bash
     agy pro fanout --models deepseek-chat,gpt-4o "이 코드의 성능 병목을 찾아줘"
     ```
2. **`agy` 메인 세션 안에서 VP에게 요청하기**:
   - `full-stack` 모드 상태라면 메인 대화창에서 아래와 같이 자연어로 지시하면 됩니다:
     > *"이 설계안에 대해 등록된 외부 모델 스웜(fanout)을 돌려서 교차 검증해줘."*
   - VP 오케스트레이터가 백그라운드에서 `agy pro fanout` 도구를 호출하여 DeepSeek, GPT-4o 등의 결과를 취합한 뒤 합의 매트릭스(`reports/fanout/.../reconciliation.md`)를 보고합니다.

---

## 7. 향후 agy-pro 로드맵 제안 (Gateway Bridge 통합)

사용자가 `agy` 메인 대화창에서도 아무런 추가 설정 없이 `agy pro setup`에 등록한 외부 모델을 직접 고를 수 있도록 하기 위한 권장 아키텍처입니다:

1. **`agy pro bridge` (내장 Go 로컬 게이트웨이)**:
   - `agy-pro` shim이 백그라운드에서 가벼운 경량 Go HTTP 프록시(`127.0.0.1:4848`)를 호스팅.
   - `agy.exe`를 구동할 때 자동으로 `AGY_LLM_GATEWAY_URL=http://127.0.0.1:4848`, `AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai`, `AGY_LLM_GATEWAY_MODELS=...`를 주입.
2. **원클릭 단일 인터페이스 완성**:
   - 사용자가 `agy` 대화창에서 `/model`을 누르면 구글 공식 모델(Gemini, Claude)과 `agy pro setup`에 등록된 모델(DeepSeek, GPT-4o)이 동일한 선택창에 함께 노출되도록 통합 가능.
