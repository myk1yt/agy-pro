---
name: mode
description: Switch Antigravity CLI operational mode between Full-Stack (VP Orchestrator, 9-Agent Swarm, 4-Model Fanout Consensus) and Clean (Minimal Baseline Antigravity CLI state).
---

# Mode Switcher Skill (`/mode`)

This skill allows the CEO/User to toggle the operational environment of Antigravity CLI between **Full-Stack Mode** and **Clean Baseline Mode** at any time.

---

## 1. Operational Mode Comparison

| Feature / Dimension | Full-Stack Mode (`full`) | Clean Mode (`clean`) |
|---|---|---|
| **Primary Paradigm** | VP of Engineering Orchestrator (CEO/VP) | Single Developer Assistant |
| **Delegation Policy** | Mandatory batch subagent delegation (MICRO exception) | Direct action by primary agent |
| **Specialized Swarm** | 9 Domain Subagents (Architect, Blind QA, Reviewers, etc.) | Zero custom subagent overhead |
| **Multi-Model Consensus** | 4-Model double-blind fanout swarm (`agy pro fanout`) | Single model execution |
| **Review & Security Gate** | Reconciled Arbiter consensus (P0=0, P1=0, Critical=0) | Standard direct review |
| **Intent Conformance** | Checklist tracking with Requirement-Coverage table | Ad-hoc user verification |
| **Token Overhead** | Strategic governance depth | Minimal token footprint |

---

## 2. Interactive Mode Selection Workflow

When this skill is invoked via `/mode` or user prompt, execute the following interactive workflow:

### Step 1: Prompt User for Mode Selection
Use `ask_question` (or prompt response) to present the choice to the CEO:

> **Antigravity CLI 프로필 모드 전환**
> 
> 사용할 환경 모드를 선택해 주십시오:
> 
> 1. **Full-Stack Mode (`full`)**
>    - VP of Engineering 오케스트레이터 패러다임 활성화
>    - 9종 전문 에이전트 스웜 (architect, blind-qa, code-reviewer, debug-specialist, designer 등)
>    - `agy pro fanout` 기반 4개 모델 double-blind 합의 및 VP Arbiter 검증
>    - 엄격한 의도 일치(Intent Conformance) 및 다계층 DoD 적용
> 
> 2. **Clean Baseline Mode (`clean`)**
>    - 순수 본연의 초경량 Antigravity CLI 기본 환경
>    - 추가 서브에이전트 룰 및 프롬프트 오버헤드 완전 제거
>    - 빠른 응답 속도 및 최소 토큰 소비
>    - `/mode` 스킬만 유지하여 언제든 복귀 가능

### Step 2: Execute Mode Switch Command
Based on the user's selection:

- **If Full-Stack Mode (Option 1) is chosen**:
  Execute the profile switch via `run_command`:
  ```powershell
  agy pro mode full
  ```
- **If Clean Mode (Option 2) is chosen**:
  Execute the profile switch via `run_command`:
  ```powershell
  agy pro mode clean
  ```

### Step 3: Verify and Confirm
After command execution:
1. Confirm that the target profile rules and manifests are applied.
2. Inform the user in 100% Korean:
   - 현재 활성화된 모드 이름
   - 변경 사항 적용 시점 (신규 세션 또는 `/reload` 즉시 반영)
   - 언제든 `/mode`를 통해 다른 모드로 전환할 수 있음을 안내
