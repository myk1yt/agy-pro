# agy-pro 사용 설명서

이 설명서는 `agy pro` Go 셔틀과 `agy-tokens` Node 도구를 처음 설치하는 사람부터, 여러 모델로 검토하고 실제 코드 변경까지 맡기는 사용자까지를 대상으로 합니다. 영어 기준 문서는 `manual.txt`입니다.

| 구성 요소 | 하는 일 | 필요한 것 |
|---|---|---|
| `agy pro` Go 셔틀 | 네이티브 명령 전달, 외부 프로바이더, fanout·swarm, 범위 제한 코드 변경, 통합, 하이브리드 프로필 | 정품 Antigravity `agy`. 직접 빌드할 때만 Go 1.22 이상 |
| `agy-tokens` Node 도구 | 턴·세션 토큰과 비용, 실시간 Gemini 쿼터, 상태줄, HTML/SSE 대시보드 | Node.js 16 이상 |

`agy-tools`, `agy-dashboard`, `agy-tokens`, `antigravity-tools`는 같은 Node 프로그램을 여는 명령입니다. 토큰 수와 비용은 로컬에서 계산한 추정치이지 프로바이더 청구서가 아닙니다.

## 목차

1. 설치와 빠른 시작
2. 명령과 라우팅 구조
3. 설정 허브, 프로바이더, 모델, 프롬프트
4. 설정, 런처, 브리지 도구
5. fanout과 교차 검증
6. fanout 프리셋과 값 결정 사다리
7. 번호로 고르는 swarm 빌더
8. `agy pro code`: 범위를 제한한 코드 변경
9. 네이티브 공존 하이브리드
10. 통합, 프로필 모드, 복구
11. 상태줄, 사용량, 토큰, 대시보드
12. 플러그인과 수명 주기 훅
13. 저장 위치, 보안, 문제 해결
14. 제거와 명령 요약

명령 예시의 `acme/fast-reasoner`는 자리 표시자이므로 실제 등록한 프로바이더와 모델로 바꾸세요. "실행 캡처"라고 적은 출력은 읽기 전용 또는 dry-run을 실제로 실행한 결과입니다. "출력 모양"이라고 적은 블록은 실행 기록이 아니라 필드 설명입니다.

---

## 1. 설치와 빠른 시작

### 1.1 준비물

- 정품 Antigravity CLI. 셔틀은 `AGY_PRO_REAL_AGY`, PATH, 기본 설치 위치 순으로 찾습니다.
- `agy-tokens`를 쓰려면 Node.js 16 이상.
- Go 셔틀을 직접 빌드할 때만 Go 1.22 이상. 설치된 바이너리 실행에는 Go가 필요 없습니다.

### 1.2 Windows에서 두 구성 요소 설치

명령 프롬프트:

```cmd
git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat && install.cmd
```

PowerShell 5.1은 `&&`를 바로 받지 못하므로 `cmd /c`로 감쌉니다.

```powershell
cmd /c "git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat && install.cmd"
```

`install.cmd`는 `agy-pro\scripts\install.ps1`을 부르는 짧은 래퍼입니다. PowerShell 스크립트를 직접 실행할 수도 있습니다.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\agy-pro\scripts\install.ps1
```

Go 설치 프로그램은 다섯 단계로 진행합니다.

1. `~/.agy-pro`, `~/.agy-pro/bin`, `~/.agy-pro/backups`를 만듭니다.
2. PATH, `C:\Program Files\Go`, `~/.agy-pro/go`에서 Go를 찾습니다. 없거나 1.22보다 오래됐으면 winget을 시도하고, 그래도 없으면 Go 1.23.6 포터블 압축 파일을 받습니다. 압축을 풀기 전에 설치 프로그램에 고정된 SHA256과 비교합니다. 해시가 다르면 파일을 `.BADHASH`로 옮기고 설치를 멈춥니다.
3. `./cmd/agy`를 임시 실행 파일로 빌드합니다. 빌드가 실패하고 저장소의 `agy-pro/bin/agy.exe`가 있으면, "새로 컴파일하지 않은 사전 빌드"라는 안내와 함께 그 파일을 사용합니다. 그다음 `~/.agy-pro/bin/agy.exe`로 교체합니다.
4. Windows 사용자 PATH 맨 앞에 `~/.agy-pro/bin`을 넣습니다. 기존 PATH 원문과 레지스트리 종류는 `~/.agy-pro/backups/path-<타임스탬프>.json`에 저장하고 복원 명령을 출력합니다. 다른 PATH 항목과 레지스트리 종류는 유지합니다.
5. `agy pro doctor --strict`를 실행합니다. 구조 문제가 있으면 설치를 실패로 끝냅니다. 성공하면 `agy pro setup`을 제안합니다. `-NoWizard`는 그 질문을, `-NoPause`는 마지막 키 대기를 생략합니다.

Node 설치 프로그램은 전역 명령을 등록합니다. `agy`가 잡히면 `agy pro integrations apply tools --yes`로 규칙, 스킬, 플러그인, 훅, 상태줄을 배포합니다. `agy-tokens` 런처가 없으면 상태줄 단계만 사유와 함께 걸러뛰고, `agy pro integrations status`에서 그 상태를 확인할 수 있습니다. 런처를 설치한 뒤 `agy pro integrations repair`로 마저 배포합니다.

설치 후에는 새 터미널을 열어 바뀐 PATH를 읽게 하세요.

![main 브랜치 설치 흐름](docs/manual/images/install-flow.svg)

### 1.3 macOS와 Linux

Node 도구 설치:

```bash
git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && bash scripts/install.sh
```

Go 셔틀 자동 설치 프로그램은 Windows 전용입니다. 직접 빌드하세요.

```bash
cd agy-pro/agy-pro
go build -o ~/.agy-pro/bin/agy ./cmd/agy
```

`~/.agy-pro/bin`을 PATH 앞에 추가합니다. 전체 화면 설정 허브는 Windows 콘솔용이고, 다른 환경에서는 줄 마법사와 전체 CLI를 씁니다.

### 1.4 첫 확인

```bash
agy pro setup
agy pro status
agy pro doctor
agy pro models --all
agy pro integrations status
agy-tokens --version
```

대화형 빌더 없이 첫 검토를 실행하는 예:

```bash
agy pro fanout --task review --n 3 --prompt-file request.txt \
  --root . --tools read,search --timeout 900 --max-native 3
```

### 1.5 업데이트

Node 런타임은 보통 클론이 아니라 전역 설치본을 씁니다. `git pull` 뒤에는 루트에서 다시 설치합니다.

```cmd
npm install -g .
```

`agy-tokens --version`과 Windows의 `where agy-tokens`로 실제 실행 파일을 확인합니다. Go 코드가 바뀌었으면 셔틀도 다시 빌드하고 설치하세요.

---

## 2. 명령과 라우팅 구조

### 2.1 정품 바이너리를 고치지 않는 셔틀

```text
agy                         -> 정품 agy, 기본 네이티브 경로
agy agents / -m / -p        -> 필요한 인자 정규화 후 정품 agy
agy pro <동사>              -> agy-pro 엔진
agy pro setup               -> 설정 허브 또는 줄 마법사
```

네이티브 표준 입력·출력과 종료 코드를 유지합니다. 현재 `agy pro` 동사는 14개입니다.

| 동사 | 용도 |
|---|---|
| `setup` | 프로바이더, 모델, fanout 프리셋, 프롬프트, 설정, 진단, 도움말, 언어, 대시보드, 통합 |
| `mode` | full/clean 프로필, 복원, 백업 목록, 상태 |
| `status` | 현재 모드, 시작 프로필, 정품 agy 경로, 프로바이더 레지스트리 |
| `models` | 등록 모델, 통합 목록, JSON 목록 |
| `swarm` | 대화형 또는 플래그 기반 스웜 빌더 |
| `settings` | 기존 패널과 비대화형 설정·프로바이더·모드·진단 명령 |
| `integrations` | 배포된 내용과 statusLine 관리 |
| `hybrid` | P1/P2 네이티브 공존 프로필 |
| `doctor` | 구조·키 진단, Windows 키 이름 복구 |
| `fanout` | 교차 검증과 파티션 엔진 |
| `code` | 계획, 쓰기, 리뷰, 복원, 전체 실행 |
| `config` | 시작 프로필과 브리지 도구 기본값 |
| `version` | 셔틀과 업스트림 버전 |
| `help` | 최상위 도움말 |

모르는 `agy pro` 동사는 종료 코드 1입니다. 플래그 기반 하위 명령은 보통 성공 0, 실행 실패 1, 사용법 오류 2를 씁니다. 명시적 `--help`/`-h`는 사용법을 출력하고 모든 `agy pro` 하위 명령에서 성공(0)입니다(R3-F-06: 예전에는 플래그를 파싱하는 동사만 해당). 어떤 하위 명령이든 모르는 플래그나 피연산자는 사용법 오류(2)입니다. 대화형 쓰기를 취소하면 `[CANCELLED] nothing written`과 함께 0이고, 비대화형에서 `--yes` 없이 동의할 수 없으면 1입니다.

### 2.2 "native"가 뜻하는 네 가지 다른 설정

| 설정 | 실제로 결정하는 것 |
|---|---|
| `default_profile = native` | 인자 없는 실행이 정품 agy로 바로 감 |
| `hybrid_profile = p1/p2/none` | 세션별 네이티브 공존 mesh를 띄울지 여부 |
| `current_mode = full/clean` | 어떤 프로필 내용이 배포되는지 |
| `integration_preset = native/tools/custom` | 어떤 통합을 배포하는지. `native`는 아무것도 배포하지 않음 |

통합 프리셋이 다른 실행 바이너리를 고르는 일은 없습니다.

![agy-pro 아키텍처](docs/manual/images/agy-pro-architecture.svg)

---

## 3. 설정 허브, 프로바이더, 모델, 프롬프트

### 3.1 환경에 맞는 설정 화면

```bash
agy pro setup                 # stdin과 stdout이 모두 콘솔일 때만 전체 화면
agy pro setup --wizard        # 어디서든 영어 줄 마법사
agy pro setup --wizard --yes  # diff 이후 비대화형 쓰기 동의
```

파이프, CI, non-Windows TTY, mintty는 줄 마법사로 갑니다. `--yes`는 `--wizard` 앞뒤 어디에도 둘 수 있습니다. 모르는 인자는 값을 마스킹한 오류와 함께 종료 코드 2입니다. `agy pro settings providers add-interactive`과 숨은 별칭 `wizard`도 같은 마법사를 엽니다.

### 3.2 허브 이동 지도

```text
                         agy pro setup
                              │
       ┌──────────────────────┼──────────────────────┐
       │                      │                      │
 [1] 프로바이더          [2] 팬아웃 프리셋         [3] 프롬프트
       │                      │                      │
 추가/편집/테스트,       [1] built-in defaults   작업 문장과
 모델과 기능              별도 프리셋              네이티브 좌석 역할
       │                      │
       │                 Enter: 고른 행 편집
       │                 Ctrl+S: 저장
       │                 Ctrl+A: 행 [1]을 새 프리셋으로 복사
       │                 D: 확인 후 행 [1] 초기화
       │
 [4] 설정               [5] 진단                [6] 도움말 / 설명서
 [7] 언어               [8] 대시보드            [9] 통합
 [0] 종료
```

숫자 1-9로 행을 실행하고, 화살표+Enter는 10 이후에도 쓸 수 있습니다. 목록에서 Space는 동작 대상을 고정합니다. 루트 Esc는 종료 확인, Ctrl+L은 다시 그리기, Ctrl+C는 종료입니다. 텍스트는 Left/Right/Home/End/Delete/Backspace로 고칩니다. 저장하지 않은 편집기에서 Esc나 오른쪽 화살표를 누르면 버릴지 묻습니다.

도움말은 Up/Down/PgUp/PgDn/Home/End로 읽습니다. Enter는 화멘별 설명서를 열고, Esc나 q는 닫습니다.

### 3.3 프로바이더와 키 보관

줄 마법사는 프로바이더 이름, 기본 URL, 키 환경 변수 이름, 키 값, 모델 ID·패밀리, 기능을 묻고 실제 연결을 시험합니다. 키 값은 Windows 사용자 환경 변수에 저장할 수 있습니다. `providers.json`에는 변수 **이름**만 들어갑니다. 화면의 키 값은 앞 5글자 + `****`로 표시하고, 5글자 이하면 `****`만 보입니다.

레지스트리 예:

```json
{
  "providers": [
    {
      "name": "acme",
      "baseURL": "https://api.example.com/v1",
      "apiKeyEnv": "ACME_API_KEY",
      "models": [
        {
          "id": "fast-reasoner",
          "family": "reasoner",
          "contextLength": 200000,
          "maxOutputTokens": 32768,
          "capabilities": {
            "supportsTools": true,
            "supportsThinking": true,
            "reasoningEffort": "high",
            "supportsVision": true,
            "allowedTools": ["read_file", "list_dir", "grep", "glob"]
          }
        }
      ]
    }
  ]
}
```

현재 PowerShell 창에만 키 설정:

```powershell
$env:ACME_API_KEY = "여기에 실제 키"
```

Windows 사용자 환경 변수로 영구 저장:

```powershell
[Environment]::SetEnvironmentVariable("ACME_API_KEY", "여기에 실제 키", "User")
```

키 값을 `providers.json`, 프리셋, 채팅, 리포트, 좌석 토큰에 넣지 마세요.

### 3.4 모델 편집과 검증

허브 > 프로바이더에서 프로바이더를 고르면 모델 목록이 나옵니다. 추가, 편집, 삭제, 사용/사용 안 함, 도구 편집은 `●`가 붙은 모델에 적용됩니다. 먼저 모델 행을 고르세요. 모델 행에서 Enter를 누르면 편집 폼이 열립니다.

칸은 ID, 패밀리, 태그, 사용 여부, 컨텍스트 길이, 최대 출력 토큰, 사고, 추론 강도, 비전, 비디오, 도구 허용 목록입니다. 추론 강도는 다음 중 하나입니다.

```text
default | none | low | medium | high | xhigh | max
```

대소문자와 앞뒤 공백은 정규화하고, 비우면 런타임 기본값입니다. 도구 허용 목록을 비우면 모든 도구 허용으로 읽히므로 저장하지 않습니다. 선택 창에서는 Space로 켜고 끄고, Enter로 반영하고, Esc로 취소합니다.

키처럼 생긴 값은 승인 창 전에 값을 출력하지 않고 거부합니다. 경로·URL 모양 ID와 보이지 않는 제어 문자도 거부합니다. 모든 레지스트리 변경은 정확한 마스킹 diff를 보여 주고, 쓰기 직전에 제시한 원본을 다시 읽습니다. 그 사이 다른 변경이 있으면 오래된 승인으로 덮어쓰지 않고 거부합니다.

CLI로 같은 작업을 할 수 있습니다.

```bash
agy pro settings providers list
agy pro settings providers add-json provider.json --yes
agy pro settings providers test acme
agy pro settings providers rename acme acme-eu --yes
agy pro settings providers delete acme-eu --yes
agy pro settings providers model-add-json acme model.json --yes
agy pro settings providers model-update acme fast-reasoner model.json --yes
agy pro settings providers model-delete acme fast-reasoner --yes
```

기존 이름으로 `add-json`을 실행하면 제시된 승인 뒤 그 프로바이더와 전체 모델 목록을 교체합니다. 프로바이더 삭제는 환경 변수의 키 값을 건드리지 않습니다.

### 3.5 네이티브와 외부 모델 통합 목록

```bash
agy pro models          # 등록 프로바이더와 모델 기능
agy pro models --all    # 네이티브 + 외부 그룹, 번호 표시
agy pro models --json   # 같은 항목을 JSON으로
```

`--all`과 `--json`은 함께 더하는 옵션이 아니라 둘 중 하나를 고르는 형태입니다. 목록 조회 실패는 종료 코드 1, 사용법 오류는 2입니다. 키 변수가 없는 행과 `excluded (exclusion list)` 행이 표시됩니다. 네이티브 카탈로그는 정리된 자식 환경으로 가져와 캐시합니다.

### 3.6 모델 제외 목록

설정 허브 > 설정 > 모델 제외 목록에서 고칩니다.

| 항목 | 제외 대상 |
|---|---|
| `acme` | 그 프로바이더의 모든 모델 |
| `fast-reasoner` | 어느 프로바이더든 그 모델 ID |
| `acme/fast-reasoner` | 첫 `/` 기준 정확한 프로바이더/모델 쌍 |
| `family:k3` | 패밀리 k3 또는 ID에 경계가 맞는 k3 토큰이 있는 모델 |

읽을 때 앞뒤 공백을 지웁니다. 문자 사이의 공백, 제어 문자(Cc), 보이지 않는 서식 문자(Cf)는 거부합니다. 빈 항목 하나는 버리고, 전부 빈 목록은 거부합니다. `defaults.excludeModels`가 없거나 null이면 내장 `family:k3`을 씁니다. 명시한 `[]`는 아무것도 제외하지 않습니다.

자동 배정과 페일오버는 제외 모델을 걸러뜁니다. `--seats`, 계획 유닛 좌석, 빌더 번호, 저장한 프리셋 이름으로 직접 고른 모델은 `explicit pick honored` 경고와 함께 실행합니다. 제외 목록이 직접 선택을 거부하지는 않습니다. 기존 설정 패널에서는 읽기 전용으로 보입니다.

### 3.7 프롬프트 작성

허브 > 프롬프트에서 `review`, `security`, `plan-audit`, `research`, `ideas`, `custom`과 네이티브 좌석 서문을 고칩니다. `~/.agy-pro/prompts/`에 슬롯별 파일로 저장하고 좌석 서문은 `seat.txt`입니다. 파일이 없으면 내장 문장을 바이트 단위로 그대로 씁니다. 빈 재정의 파일은 오류입니다.

우선순위는 환경 변수 > 슬롯 파일 > 내장 문장입니다.

```text
AGY_PRO_PROMPTS_DIR      -> 모든 슬롯 파일의 디렉터리 변경
AGY_PRO_SEAT_PREAMBLE    -> 네이티브 좌석 서문만 직접 지정
```

좌석 서문은 행동 지침이지 권한 설정이 아닙니다. 쓰기, 명령, URL 읽기, 웹 검색 금지는 도구 허용 목록으로 설정하세요.

### 3.8 언어와 화면

첫 setup 실행에서 English, 简体中文, 繁體中文, 한국어, 日本語, Русский, हिन्दी, Deutsch, Français, Italiano, Tiếng Việt, العربية 12개 언어를 고릅니다. 이후에는 설정 > 모양 > Language 또는 `agy pro settings set language <코드> --yes`에서 바꿉니다.

아랍어는 논리 순서 문자열과 레이아웃 미러링을 지원하지만, 시각적 bidi 재배열, 문자 모양 연결, 문자열 중간 자르기, 인라인 RTL 편집은 하지 않습니다. 힌디어는 데바나가리 고정 폭 정렬을 약속하지 않습니다. `AGY_PRO_COLOR=truecolor|256|16|off`로 색 등급을 강제하고, `NO_COLOR`는 무색으로 만듭니다.

---

## 4. 설정, 런처, 브리지 도구

### 4.1 기존 설정 패널과 현재 CLI

`agy pro settings`는 기존 전체 화면 패널을 열고 사용 중단 예고를 보여줍니다. 유지 관리되는 설정 표면은 `agy pro setup`입니다. 비대화형 명령은 계속 지원합니다.

```bash
agy pro settings show
agy pro settings show --json
agy pro settings --panel p4
agy pro settings set native-seat-concurrency 4 --yes
```

패널 여덟 구역은 Launch & Profile, Models & Providers, Bridge Tools & Budgets, Swarm & Fanout, Diagnostics & Keys, Install & Update, Environment Variables, Appearance입니다.

`set`이 받는 키:

| 키 | 값 |
|---|---|
| `default-profile` | `prompt`, `native`, `gateway` |
| `bridge-tools` | `read,search,write,exec`의 부분 집합 또는 `none` |
| `native-seat-concurrency` | 정수 1..8 |
| `language` | 12개 UI 코드 중 하나 |
| `launcher_menu` | `on/off`, `1/0`, `true/false` |
| `integration-preset` | `native`, `tools` |
| `integration-statusline` | `on`, `off` |
| `integration-hook` | `on`, `off` |
| `integration-rules` | `on`, `off` |
| `integration-plugins` | `on`, `off` |
| `integration-skills` | `on`, `off` |

하이픈과 밑줄 별칭도 state 계층에서 받습니다. 모든 쓰기는 동의 전에 실제 저장될 diff를 보여줍니다.

### 4.2 런처 라우팅

인자 없는 대화형 `agy`는 `AGY_LAUNCHER=1`이거나 설정 허브 > 설정 > Launcher menu가 켜져 있을 때만 2초 런처를 엽니다. 환경 변수가 비어 있지 않으면 그 값이 우선합니다. `1`은 표시, 다른 값은 숨김입니다. 변수가 없거나 비어 있으면 저장값을 쓰고, 저장값도 없으면 꺼짐입니다.

선택지는 네이티브, 외부 모델 통합 목록, 스웜입니다. Enter나 시간 초과는 네이티브입니다. 인자가 있거나, `-p`/`--print` 일회성 실행이거나, stdin이 파이프이거나, `AGY_LAUNCHER=0`이면 메뉴를 걸러뜁니다.

```powershell
$env:AGY_LAUNCHER=1; agy
$env:AGY_LAUNCHER=0; agy
```

`default-profile`로 기본 경로를 정할 수도 있습니다.

```bash
agy pro config default-profile prompt --yes
agy pro config default-profile native --yes
agy pro config default-profile gateway --yes
```

### 4.3 게이트웨이 브리지 도구

```bash
agy pro config show
agy pro config bridge-tools read,search --yes
agy pro config bridge-tools read,search,write --yes
agy pro config bridge-tools read,search,write,exec --yes
agy pro config bridge-tools none --yes
```

| 토큰 | 제공 도구 | 기본 상태 |
|---|---|---|
| `read` | `read_file`, `list_dir` | 켜짐 |
| `search` | `grep`, `glob` | 켜짐 |
| `write` | `write_file`, `replace_in_file` | 꺼짐 |
| `exec` | `run_command` | 꺼짐 |
| `none` | 도구 루프 끄기 | 선택 가능 |

`AGY_PRO_BRIDGE_TOOLS`가 한 번의 실행에서 state.json보다 우선합니다. 잘못되거나 빈 재정의 값은 루프를 fail-closed로 끕니다. agy가 아니라 브리지가 도구 호출을 실행하고 최종 텍스트만 agy에 돌려줍니다.

고정 예산은 반복 8회, 루프 240초, 업스트림 턴 60초, 도구 호출 30초, 턴당 호출 16회입니다. 설정으로 바꾸는 값이 아니라 컴파일 상수입니다. 도구를 많이 쓰는 일회성 실행에는 `agy --print-timeout 300s`을 지정할 수 있습니다.

---

## 5. fanout과 교차 검증

### 5.1 언제 쓰는가

코드 리뷰, 보안 감사, 조사, 계획 감사, 아이디어, 사용자 지정 작업을 여러 좌석에 독립적으로 맡길 때 씁니다. 각 좌석은 서로의 답을 보지 않고 같은 입력을 받습니다. 프로바이더 좌석은 과금 API를 쓰고, 네이티브 좌석은 실제 서브프로세스에서 Antigravity 쿼터를 씁니다.

```bash
agy pro fanout --task review --n 3 --prompt-file review.txt \
  --root . --tools read,search --timeout 900 --max-native 3

agy pro fanout --task security \
  --seats "native:gemini-3.8-flash-high,acme/fast-reasoner" \
  --prompt-file security.txt --tools read,search
```

좌석 문법은 쉼표로 구분한 `native:<모델ID>`, `provider/model`, 또는 `model`입니다. `--seats`가 유일한 명시 fanout 좌석 목록 플래그입니다. 명시한 좌석 수가 실제 좌석 수입니다. 네이티브 동시 실행은 state.json 값을 쓰고 없으면 1로 폴백하며, 8보다 크면 8로 제한합니다. 실패한 네이티브 좌석은 다른 네이티브 모델로 대체하지 않습니다. 프로바이더 페일오버는 설정된 풀과 제외 정책을 따릅니다.

### 5.2 실행 수명 주기

```text
플래그 + 환경 변수 + 프리셋 + providers defaults
                    │
                    ▼
             해석된 RunConfig
                    │
          좌석 / 파티션 계획 해석
                    │
       ┌────────────┴─────────────┐
       │                          │
 프로바이더 좌석             네이티브 agy 서브프로세스
 (페일오버 가능)             (실패를 그대로 기록)
       └────────────┬─────────────┘
                    ▼
       좌석 markdown + progress.log
                    ▼
       findings 파서 + 발견 통합
                    ▼
       reconciliation.md + summary.json
```

`--json`은 stdout에 요약 문서 하나만 두고 진행 줄을 stderr로 옮깁니다. 사람용 모드에서는 진행 줄과 최종 배너가 stdout입니다. 프리셋과 제외 안내는 stderr입니다.

### 5.3 전체 플래그

| 플래그 | 기본값과 동작 |
|---|---|
| `--task` | `review`. `review`, `security`, `plan-audit`, `research`, `ideas`, `custom` 지원 |
| `--prompt` | 직접 입력한 프롬프트 |
| `--prompt-file` | 프롬프트 파일, `-`는 stdin |
| `--units` | 파티션 JSON 계획. 프롬프트와 좌석 출처 플래그와 공존 불가 |
| `--n` | 자동 좌석 3. 라인업이나 유닛이 있으면 그 수 사용 |
| `--seats` | 네이티브/프로바이더 혼합 좌석. 유일한 명시 좌석 목록 플래그 |
| `--max-native` | 0이면 state.json 다음 기본 1. 양수는 1..8로 제한 |
| `--root` | `.`. 프로바이더 도구 작업 공간 |
| `--tools` | `read,search`. `read,search,write,exec` 닫힌 집합 |
| `--max-turns` | 프로바이더 도구 루프 기본 500턴 |
| `--timeout` | 300초. 설정한 값은 그대로 적용 |
| `--out` | `./reports/fanout/<ts>` |
| `--json` | 사람용 배너 대신 stdout에 요약 JSON |
| `--preset` | 이름 있는 프리셋. 없는 이름은 사용법 오류 2 |
| `--allow-host` | 반복 가능한 호스트/IP. 포트 무시, 소문자 비교 |
| `--search-endpoint` | `{query}` 하나가 있는 HTTPS URL. 루프백 HTTP 허용. 비우면 `--search-native=off`가 아닌 한 네이티브 폴백 사용 |
| `--search-key-env` | 환경 변수 이름. 키가 필요 없는 엔드포인트는 생략 가능. 키 이름만 있어도 네이티브 폴백 사용 |
| `--search-native` | 기본 `auto`는 엔드포인트가 빈 검색마다 네이티브 agy 1턴을 실행. `off`는 기존의 검색 없음 동작 유지 |
| `--setup` | 프로바이더 마법사를 열고 종료 |
| `--yes` | `--setup` 전용 동의. 단독 사용은 종료 코드 2 |

### 5.4 발견과 상태

파서는 `CRITICAL`을 `BLOCKER`로 바꾸고, 정규화한 발견 제목끼리 묶습니다.

| 상태 | 판정 | 다음 행동 |
|---|---|---|
| `CONFIRMED` | 두 좌석 이상이 같은 발견 보고 | 높은 합의로 검토하고 조치 |
| `DISPUTED` | 한 좌석만 BLOCKER 또는 HIGH 보고 | file:line이나 재현 근거로 master 판정 |
| `OBSERVATION` | 한 좌석만 MEDIUM, LOW, INFO 보고 | 관찰 항목으로 추적 |
| `thin` | 성공했지만 발견 신호가 없음 | 합의 분모에서 제외 |
| `degraded` | 실패, 걸러뜀, thin 등 부분 상태 | 깨끗한 통과가 아니라 부분 결과로 판독 |

출력 모양:

```text
Task:                  <작업>
Seats Evaluated:       <완료> / <요청>
Degraded Mode:         <true|false>
Confirmed Issues:      <개수>
Disputed Issues:       <개수>
Artifacts Generated:
  Reconciliation:      <출력>/reconciliation.md
  Machine Summary:     <출력>/summary.json
```

`reconciliation.md`에는 matrix, 상세 발견, 판정 체크리스트가 있습니다. `summary.json`에는 좌석, 발견, 개수, 선택적 사용량, `seat_lineup`, `partition`, `sources`, `untrusted_data` 안내가 있습니다. `progress.log`에는 좌석 시작·종료 줄이 기록됩니다. 좌석 리포트는 `seat-<번호>-<프로바이더>-<모델>.md`입니다. 모두 소유자만 읽는 파일로 저장합니다.

### 5.5 프로바이더 도구와 외부 통신

`read,search`는 기본으로 켜져 있습니다. URL 읽기는 호스트를 허용하기 전까지 꺼져 있습니다. 프로바이더 좌석의 웹 검색은 엔드포인트를 설정하면 그 경로를 쓰고, 비우면 기본적으로 네이티브 폴백을 씁니다.

| 기능 | 켜는 방법 | 제한 |
|---|---|---|
| 작업 공간 읽기·검색 | `--tools read,search` | root와 파티션 유닛 scope |
| 작업 공간 쓰기 | 파티션 유닛에 `"write": true` | 프로바이더만, scope 안쪽. 전역 파티션 쓰기 스위치 없음 |
| `read_url_content` | `--allow-host example.com` 반복 | GET, 루프백 외 HTTPS, 인증 없음, 리다이렉트 3회 재검사, 512KiB, 텍스트만 |
| `search_web` | `--search-endpoint`를 설정하면 명시 HTTP. 비우면 `--search-native=off`가 아닌 한 네이티브 폴백 | 명시: `{query}` 하나, 루프백 외 HTTPS, 키는 호출 시에만 읽음. 네이티브: 검색당 헤드리스 실제 agy 1턴, 구독 쿼터 사용 |

예:

```bash
agy pro fanout --task research --prompt-file research.txt \
  --allow-host example.com --allow-host docs.example.com

agy pro fanout --task research --prompt-file research.txt \
  --search-endpoint "https://search.example.com/q={query}" \
  --search-key-env SEARCH_API_KEY
```

검색 키는 환경 변수에 두고, 엔드포인트나 프리셋에 넣지 마세요. 네이티브 좌석은 이 프로바이더 도구 러너를 받지 않고 실제 agy의 도구를 씁니다.

프로바이더 좌석에서 `search_endpoint`를 설정하면 기존의 명시 HTTP 동작을 유지합니다. 이 값을 비우는 것은 더 이상 "웹 검색 없음"을 뜻하지 않습니다. 기본값 `--search-native=auto`이고 실제 agy를 찾을 수 있으면, 엔진이 `search_web`을 제공하고 검색할 때마다 헤드리스 실제 agy 1턴을 실행해 Antigravity 구독 쿼터를 씁니다. `search_key_env`만 있어도 명시 모드로 바뀌지 않습니다. `--search-native=off`이거나 agy 폴백을 쓸 수 없으면 기존의 검색 없음 동작을 유지합니다. 네이티브 폴백 체인은 실제 검색이 아니라 스텁 agy 실행 파일로 검증했습니다.

네이티브 검색 자식은 기본적으로 제한된 agy 에이전트로 실행됩니다. 배포된 `agypro-search` 에셋(`~/.gemini/config/agents/agypro-search/AGENT.md`, 리포 사본에서 배포)은 웹 도구 두 개(`search_web`, `read_url_content`)만 선언하므로, 자식에게 파일·셸·서브에이전트 도구가 없습니다. 설치 스크립트(`install.ps1` 4/6단계)가 이 에셋을 배포하고 두 내장 프로필이 모두 이 에셋을 담고 있어서, `agy pro mode` 프로필 전환으로 이 경로가 사라지지 않습니다. 에셋이 없거나 agy가 그 이름을 목록에 올리지 않으면, 자식은 자체 도구 표면을 가진 완전한 agy 에이전트로 폴백하고 제한은 검색 프롬프트뿐이며, 실행 줄이 그 사실을 알립니다. 어느 경로든 자식은 실행의 `--allow-host` 목록이나 좌석의 읽기 전용 도구 집합에 묶이지 않고, 검색 프롬프트가 웹 검색과 URL 읽기만 쓰도록 요청합니다.

실제 fanout CLI 실행은 좌석을 시작하기 전에 결정된 검색 경로를 항상 공개합니다. 단계 라벨은 `flag`, `engine`, `preset`이고, `restricted child` 조항이 기본 배포 경로이며 `full agy agent child`는 폴백에서만 나옵니다.

```text
fanout[flag] search=endpoint(api.example.com)
fanout[engine] search=agy-native (restricted child; subscription per search)
fanout[flag] search=agy-native (full agy agent child; subscription per search)
fanout[flag] search=off
```

대신 검색 API를 연결할 수도 있습니다. Tavily(월 1,000건 무료)와 Serper(가입 시 2,500건 1회성)가 진입 장벽이 낮은 선택지이며, Brave는 더 이상 카드 없이 쓸 수 없습니다. Google 공식 Gemini API grounding 경로도 있지만, API 키로 과금되고, 월 5,000건 무료 검색 쿼리는 유료 Gemini API 등급에서만 적용되며 그 이후에는 실행된 쿼리마다 과금되고, 무료 API 등급에서는 검색 grounding을 제공하지 않습니다. 이 경로는 `GEMINI_API_KEY`를 쓰며 `search_endpoint` 템플릿에 바로 넣을 수 있는 값이 아닙니다.

### 5.6 파티션 모드

`--units`는 겹치지 않는 유닛마다 프로바이더 좌석 하나를 배정하고, 계획 순서대로 합칩니다.

```json
[
  {
    "id": "auth",
    "title": "인증 검토",
    "scope": ["internal/auth/*.go"],
    "deliverable": "file:line 근거가 있는 발견",
    "seat": "acme/fast-reasoner"
  },
  {
    "id": "store",
    "title": "저장소 검토",
    "scope": ["internal/store/*.go"],
    "deliverable": "file:line 근거가 있는 발견",
    "write": false
  }
]
```

```bash
agy pro fanout --task review --units plan.json --root .
```

필수 필드는 `id`, `title`, `scope`, `deliverable`, 선택 필드는 `seat`, `body`, `write`입니다.

- 최대 64유닛, 앞뒤 공백을 정규화한 ID는 유일해야 합니다.
- scope는 비어 있거나, 루트 전체이거나, `**` 같은 범 와일드카드이면 안 됩니다.
- 감지 가능한 scope 겹침은 파싱에서 거부하고, 병합 때 다시 확인합니다.
- 네이티브 좌석은 거부합니다.
- `--prompt`, `--prompt-file`, `--seats`, 전역 `--tools write/exec`와 함께 쓸 수 없습니다.
- 쓰기는 프로바이더 유닛별로만 허용하고 그 유닛 scope로 제한합니다.
- 기본 제한 시간은 300 + 유닛당 120초, 최대 3600초입니다. 명시하거나 프리셋/defaults로 설정한 시간은 그대로 적용합니다.

산출물은 `merged.md`, `reconciliation.md`, `partition` 매니페스트가 있는 `summary.json`입니다. 충돌, deliverable 누락, 병합 검증 실패는 실행을 실패로 끝내지만 증거 파일은 남습니다.

![파티션 모드 개념](docs/manual/images/partition-mode.svg)

---

## 6. fanout 프리셋과 값 결정 사다리

### 6.1 저장되는 것

`~/.agy-pro/fanout-presets.json`은 이름 있는 실행 설정과 `default_preset`을 저장하고 프롬프트는 저장하지 않습니다. 13개 선택 항목은 다음과 같습니다.

| 필드 | 관련 플래그 / 뜻 |
|---|---|
| `task` | `--task` |
| `n` | `--n` |
| `timeout_seconds` | `--timeout` |
| `tools` | `--tools` |
| `max_turns` | `--max-turns` |
| `max_native` | `--max-native` |
| `allow_hosts` | 반복 `--allow-host` |
| `search_endpoint` | `--search-endpoint`. 비우면 기본적으로 네이티브 폴백 선택 |
| `search_key_env` | 환경 변수 이름만. 키 이름만 있어도 네이티브 폴백 사용 |
| `out_dir` | `--out` |
| `root` | `--root` |
| `seats` | 구조화된 고정 좌석. 유일한 프리셋 좌석 출처 |
| `json` | JSON 출력. true일 때만 고정 |

빈 칸은 설정하지 않은 것입니다. 0이나 false 또는 "화면의 엔진 기본값을 저장"이라는 뜻이 아닙니다. `seats`가 비어 있으면 엔진이 자동 선택합니다.

### 6.2 행 [1]과 일반 프리셋 편집

설정 허브 > [2] 팬아웃 프리셋으로 갑니다.

- 행 [1] `(built-in defaults)`에서 Enter를 누르면 그 시드 레코드를 직접 편집합니다.
- `Ctrl+S`는 실제 설정한 칸만 저장합니다.
- `Ctrl+A`는 보이는 기본값을 채운 새 프리셋 편집기를 엽니다. 행 [1]을 덮어쓰는 동작이 아닙니다.
- 행 [1]에서 `삭제` 동작 행을 실행하면 확인 후 엔진 기본값으로 초기화하고 시드를 가리키는 default 키도 지웁니다. `D` 키는 고정된 현재 선택에 대해 똑같이 작동합니다. 행 [1]이면 똑같은 초기화 확인 창을 열고("Reset (built-in defaults) to engine defaults? All saved settings are removed."), 일반 프리셋 행이면 똑같은 삭제 확인 창을 엽니다. 선택이 없으면 이 화면의 기존 안내 "먼저 프리셋 행을 선택하세요."가 나옵니다. 콘솔 직접 입력과 라인 모드 모두에서 `d`, `D`를 받습니다.
- 행 [1]의 기본값으로 설정은 시드 레코드를 저장한 뒤에는 가능하지만 중복 동작입니다. 아직 시드 레코드가 없으면 이 화면의 기존 안내 "먼저 프리셋 행을 선택하세요."와 함께 거부됩니다.
- 새로 만들기, 편집, 복제, 삭제, 기본값으로 설정은 일반 프리셋에 씁니다. 동작 행 전에 Space로 대상을 고정하세요.

예약 이름은 대소문자까지 정확한 `(built-in defaults)`입니다. 일반 프리셋 생성·이름 변경에 쓸 수 없습니다. 비슷한 다른 이름은 일반 프리셋입니다. 손으로 중복 입력한 예약 레코드는 읽을 때 첫 번째만 남기고 한 줄 경고를 냅니다.

편집 구역은 스칼라 A, allow_hosts, 스칼라 B, 좌석입니다. B구역은 `search_endpoint`, `search_key_env`, `out_dir`, `root`, `json`의 5행입니다. Tab은 A > hosts > B > seats > A 순으로 돕니다. Ctrl+S 저장, 오른쪽 화살표 닫기, `json` 칸 Enter도 저장입니다. task 칸 Enter는 알려진 작업 선택 창을 엽니다. 모든 좌석 행은 선택되면 `[x] `, 선택되지 않으면 `[ ] `로 시작합니다. Enter는 좌석 행을 선택하고, Space는 선택하거나 선택을 해제합니다. 좌석 동작은 `+ 좌석 추가`, `▲ 좌석 위로`, `▼ 좌석 아래로`, `✕ 좌석 삭제`이고, 좌석 행 Delete는 한 번에 지웁니다.

저장 검증은 모르는 작업, 잘못된 좌석, 잘못된 호스트, 잘못된 검색 키 변수 이름, 키 모양 값, 상위 경로 이동을 거부합니다. 중복 좌석과 같은 패밀리 좌석은 거부가 아니라 경고입니다.

### 6.3 값 결정 사다리

```text
가장 강함
   │
   ├─ 명시한 CLI 값, 채팅 선택, 빌더 선택
   ├─ --preset 이름
   ├─ AGY_PRO_FANOUT_PRESET=이름
   ├─ fanout-presets.json의 default_preset 키
   ├─ 행 [1] (built-in defaults) 저장 시드 레코드
   ├─ providers.json의 defaults 블록
   └─ 엔진 기본값 / 네이티브 우선 자동 좌석
가장 약함
```

사다리는 필드마다 적용됩니다. 명시 플래그가 모든 프리셋보다 강합니다. `default_preset`이 설정됐지만 대상이 없으면 경고하고 행 [1]을 쓰지 않습니다. 플래그나 환경 변수로 지정한 프리셋이 없으면 종료 코드 2입니다.

`providers.json defaults`는 `n`, `timeoutSeconds`, `maxTurns`, `tools`, 제외 정책을 제공합니다. `tokenBudget`과 `excludeFamilies`는 아직 예약 상태입니다. 엔진 기본값과 다른 저장값은 조용히 정규화하지 않고 출력합니다. `max_native`는 플래그 > 프리셋 > state.json > 1입니다.

기본 프리셋이 명시 좌석 출처나 파티션 읽기 전용 규칙과 충돌하면, 충돌한 프리셋 항목만 경고와 함께 버립니다. 직접 이름을 댄 프리셋이 충돌하면 사용법 오류입니다. `seats`가 유일한 프리셋 좌석 출처이며, 빈 값이면 엔진이 자동 선택합니다. 예전 fanout `models` 필드, 플래그, 사다리 단계는 제거됐습니다. 저장된 프리셋 레코드에 예전 `models` 키가 있으면 레코드마다 advisory 한 줄을 출력하고 값은 무시하며, 그 값을 절대 출력하지 않습니다.

```text
warning: preset "legacy": the legacy models field is removed; use seats (ignoring the stored value)
```

### 6.4 좌석 경로, 비용, 모델 누락

| 출처 | 프로바이더 모델이 사라짐 | 비용·동의 동작 |
|---|---|---|
| `--seats`, 채팅, 빌더 직접 선택 | 오류로 중단 | 호출 워크플로가 지출 전 승인을 받아야 함 |
| `--preset`, 환경 변수 | 오류로 중단 | 일회성 echo. 호출 워크플로가 승인을 받아야 함 |
| 저장소 `default_preset` | 경고 후 걸러뛰고 `degraded: true` | 저장된 좌석에 대한 상시 승인 |
| default 키가 없는 행 [1] 시드 | 같은 상시 경로와 동작 | 같은 상시 승인 |
| 프리셋 좌석 없음 | 엔진이 네이티브 우선 자동 선택 | 선택된 좌석과 호출 워크플로에 따름 |

fanout 바이너리에는 일반 프로바이더 좌석용 `y/N` 관문이 없습니다. `--yes`는 `--setup` 전용입니다. 스웜 플러그인, 에이전트 라우팅 규칙, `agy pro code`가 각자 승인 관문을 담당합니다. 일회성 echo가 실제 사용자 동의라는 뜻은 아닙니다.

상시 좌석의 프로바이더 모델이 전부 사라지면 실행을 중단합니다. 모르는 `native:<id>`는 레지스트리에서 검사하지 않고 실행하다가 실패하며 degraded 결과를 남깁니다.

### 6.5 실제 실행 안내 문장

좌석 echo:

```text
fanout: seat lineup from preset "<이름>" (<경로> rung): N seat(s), M metered provider seat(s) (<좌석>)
```

저장소에 `default_preset` 키가 없어 행 [1]의 시드가 적용되면 경로는 `(default_preset rung, no key set)`입니다. 실제 출력은 다음과 같습니다.

```text
fanout: seat lineup from preset "(built-in defaults)" (default_preset rung, no key set): 1 seat(s), 0 metered provider seats (native:no-key)
```

저장소 키가 있는 기본 프리셋은 접미사 없이 `(default_preset rung)`를 유지합니다.

일회성 프리셋 경로에는 다음이 붙습니다.

```text
; a named preset is ad-hoc - confirm the metered seats above before the run spends
```

권한·외부 통신 공개, 실제 프리셋 경로 테스트 예:

```text
fanout: tools="read,search,write", search_endpoint="https://search.example/{query}", search_key_env="P6_SEARCH_KEY", root="/workspace/review", out_dir="reports/review" (privilege/egress knobs: preset "(built-in defaults)")
fanout: tools="read,search,write", search_endpoint="https://search.example/{query}", search_key_env="P6_SEARCH_KEY", root="/workspace/review", out_dir="reports/review" (privilege/egress knobs: standing preset "(built-in defaults)")
```

실제 적용된 프리셋 값 `tools`, `search_endpoint`, `search_key_env`, `root`, `out_dir`만 나옵니다. 플래그가 덮은 항목은 빠집니다. 실제 동작에 필요한 항목을 줄 앞에 배치하고 프리셋 이름은 뒤에 붙입니다. 기본 제공 프리셋 이름을 써도 첫 번째 핵심 절은 첫 80열 안에 들어갑니다. 여러 항목이 있는 줄은 뒤따르는 절이 80열을 넘어 이어질 수 있으므로 전체 줄을 읽어야 합니다.

출처 advisory:

```text
fanout: notice: preset "(built-in defaults)" pins a seat lineup with NO provenance marker (hand-authored or pre-marker record); using it as the store default lineup anyway - advisory only, and a hand-authored pin is not standing approval
```

```text
fanout: notice: preset "(built-in defaults)" seats were edited outside the preset editor (stored hash mismatch); using the on-disk lineup anyway - this is advisory, not a seal, and a hand-edited pin is not standing approval
```

편집기는 저장하는 레코드마다 `seats_sha256`을 찍으므로, 행 [1]을 편집기로 저장하면 값을 고쳤는지와 상관없이 표식이 갱신되고 안내가 사라집니다. 다른 프리셋을 저장하는 경우에는 행 [1]의 표식이 바뀌지 않으므로 손으로 심어 둔 좌석 구성은 계속 드러난 상태로 남습니다. 표식이 없거나 다르면 알림일 뿐이며 실행도 저장도 편집도 막지 않고, 값과 맞게 직접 붙여 넣은 표식은 표식 자체가 밝혀진 증거라 실행이 조용한 것이 원래 설계입니다. 그 좌석 구성이 이번 실행에 실제로 쓰일 때만 출력하고, `--seats` 또는 `--units` 실행에서는 출력하지 않습니다.

중복 시드 경고:

```text
warning: fanout-presets.json holds more than one record named "(built-in defaults)"; keeping the first and ignoring 1 later duplicate record(s)
```

`summary.json.seat_lineup`에는 `preset`, `rung`, `seats`, `metered_seats`, `standing`이 기록됩니다. 걸러뛴 좌석이 있으면 `skipped_seats`, `skipped_seat_specs`가 추가되고 `degraded`가 설정됩니다.

### 6.6 경로와 레코드 안전

프리셋 `root`와 `out_dir`은 절대 경로를 허용합니다. `/`나 `\`로 나뉜 완전한 `..` 구성 요소는 저장할 때와 실제 적용할 때 거부합니다. `..cache`, `...`, 절대 경로는 허용합니다. 명시 플래그 경로는 기존 규칙을 유지합니다.

손상된 프리셋 파일은 경고와 함께 빈 저장소로 읽습니다. 그 후 첫 저장은 경로를 보여 주는 덮어쓰기 확인이 필요합니다. 이름, task, root, out_dir, seats, tools, search_endpoint, search_key_env, allow_hosts 항목에 제어·서식 문자가 있는 레코드는 다른 뜻으로 정리하지 않고 경고와 함께 버립니다.

---

## 7. 번호로 고르는 swarm 빌더

### 7.1 대화형과 비대화형

TTY에서 플래그 없이 실행:

```bash
agy pro swarm
```

템플릿, 좌석, 도구, 예산, 미리보기를 묻습니다. 템플릿은 code review, security audit, custom입니다. 좌석은 번호, 범위, p1..p3, 저장한 swarm 프리셋 이름, 명시 스펙으로 고릅니다. Enter/y는 실행, c는 출력만, 1-4는 수정, q는 취소입니다.

비대화형 플래그를 하나라도 주면 TTY에서도 질문이 없어집니다.

```bash
agy pro swarm --template code-review --seats "1,3,5" --root . \
  --prompt-file review.txt --tools read,search --timeout 900 --dry-run
```

| 플래그 | 뜻 |
|---|---|
| `--template` | 필수: `code-review`, `security-audit`, `custom` |
| `--seats` | 상시 fanout 프리셋이 좌석을 주지 않으면 필수 |
| `--root` | 작업 공간, 기본 `.` |
| `--prompt` | 직접 문장. 기본은 템플릿 scaffold |
| `--prompt-file` | 파일 또는 stdin `-` |
| `--tools` | 템플릿 도구 덮어쓰기 |
| `--task` | 템플릿 작업 덮어쓰기 |
| `--timeout` | 양의 초, 기본 300 |
| `--dry-run` | fanout 명령만 출력하고 좌석 없이 종료 0 |

`p1`은 네이티브 2 + 외부 2, `p2`는 패밀리를 바꾼 외부 3, `p3`은 혼합 4를 목표로 합니다. 제외 행은 자동 프리셋에 들어가지 않습니다. `~/.agy-pro/swarm-state.json`의 저장 swarm 프리셋은 번호 선택식을 저장하고 이름으로 고를 수 있습니다. fanout 프리셋과 다른 기능입니다.

### 7.2 실제 dry-run 캡처

```console
$ agy pro swarm --template code-review --seats "native:gemini-3.8-flash-high" --root . --prompt-file README.md --tools read,search --timeout 300 --dry-run
agy pro fanout --task review --seats native:gemini-3.8-flash-high --root . --timeout 300 --tools read,search --prompt-file README.md --json
```

모델 호출은 없었습니다. 이 벡터는 정보이며 셸에 그대로 붙여넣을 명령으로 보장하지 않습니다. 과금 좌석을 확인한 뒤 `agy pro swarm`에서 `--dry-run`만 빼고 실행하세요. 호출자가 좌석을 주지 않고 상시 프리셋이 좌석을 주면, 벡터에서 `--seats`를 빼서 프리셋 경로가 그대로 적용되게 합니다.

---

## 8. `agy pro code`: 범위를 제한한 코드 변경

### 8.1 언제 쓰는가

실제 쓰기 범위를 제한하고, 엔진이 검증하고, 독립 좌석이 리뷰하고, 되돌리기까지 필요한 변경에 씁니다. 계획과 리뷰의 기본 네이티브 좌석은 다음과 같습니다.

```text
native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high
```

writer는 정확히 하나의 provider/model이어야 합니다. 네이티브는 scope가 적용된 ToolRunner가 없으므로 writer가 될 수 없습니다.

### 8.2 실행 순서와 사용 예

```text
요청 -> 네이티브 계획 좌석 -> 계획/reconciliation 읽기
                          │
                          ▼
             계획에서 좁은 scope 도출
                          │
                          ▼
스냅샷 -> 프로바이더 쓰기 -> 엔진 검증 -> repair(기본 최대 1)
                          │                     │
                          │                     └─ 실패: 종료 1 + rollback 안내
                          ▼
                  네이티브 리뷰 좌석 -> 최종 리포트
```

계획만:

```bash
agy pro code plan --ask-file request.txt
```

계획에서 scope를 정한 뒤 전체 실행:

```bash
agy pro code run --ask-file request.txt \
  --scope "internal/parser" --writer acme/fast-reasoner \
  --verify "go build ./..." --verify "go test ./internal/parser" --yes
```

기존 계획으로 쓰기만:

```bash
agy pro code write --plan plan.json --verify "go test ./internal/parser" --yes
```

리뷰와 복원:

```bash
agy pro code review --run reports/code/<ts>
agy pro code rollback --run reports/code/<ts>
```

`--scope .`을 임의로 넓히지 마세요. scope가 없는 `code run`은 계획 후 종료 코드 0으로 멈추고 다음 scope 명령을 안내합니다. `--skip-plan`인데 scope가 없으면 종료 코드 2입니다.

### 8.3 전체 옵션

| 옵션 | 적용 대상 | 동작 |
|---|---|---|
| `--ask`, `--ask-file` | plan, write 조합, run | 조합에 필요한 요청 출처. 둘은 공존 불가. 파일 `-`는 stdin |
| `--plan` | write, run | 기존 파티션 계획. run에서는 `--skip-plan`과 같고 ask와 공존 불가 |
| `--scope` | write, run | 반복 가능한 제한 경로/glob. 조합 write는 하나 이상 필요 |
| `--writer` | write, run | 정확히 프로바이더 좌석 하나, 기본값 없음. 네이티브는 종료 2 |
| `--seats` | plan, review, run | 읽기 전용 좌석. run은 계획과 리뷰에 공유 |
| `--verify` | write, run | 반복 가능한 명령행. 모두 통과해야 함. 빈 값이나 짝이 안 맞는 따옴표는 종료 2 |
| `--max-repairs` | write, run | 기본 1, 0은 repair 끔, 음수는 종료 2 |
| `--run` | review, rollback | 필수 실행 폴터이자 유일한 산출물 출처 |
| `--skip-plan` | run | 계획 phase 생략 |
| `--root` | 전체 | 작업 공간, 기본 `.` |
| `--out` | 전체 | 실행 폴터, 기본 `reports/code/<ts>`. review/rollback에서 지정하면 `--run`과 같아야 함 |
| `--timeout` | 전체 | 0 이상 fanout 초, 0은 엔진 기본 |
| `--json` | 전체 | stdout에 리포트 JSON 하나. phase 출력은 stderr |
| `--dry-run` | 전체 | 실제 명령과 공개 사항만 출력. 스냅샷·좌석 없이 종료 0 |
| `--yes` | write/review/run, 과금 plan | 비대화형 과금 동의. rollback은 받지만 무시 |

review는 `report.txt`, rollback은 `manifest.json`과 `snapshot/`이 필요합니다. 리포트가 없거나, `--out`과 `--run`이 다르거나, 위치 인자나 모르는 플래그가 있으면 종료 코드 2입니다.

### 8.4 실제 dry-run 발췌

자리 표시 프로바이더로 실행한 실제 dry-run의 일부입니다. 모델 호출은 없었습니다.

```console
$ agy pro code run --ask "Add input validation to internal/example" --scope "internal/example" --writer acme/model --verify "go test ./internal/example" --dry-run
agy pro code plan: dry-run (nothing executed)
agy pro code write: 1 metered seat(s) - this run spends money
  seat 1  [provider]  acme/model  (metered API)
  note: no token/cost cap is enforced on this run
agy pro code write: dry-run (nothing executed)
  scope: inter****
  disclosure: verify executes commands over the working tree, which may include code created by the writer seat in this run
  budget: 1 verification command(s), 10m0s per command, 1800s for the whole run
  verify: go test ./internal/example
  rollback: agy pro code rollback --run reports\code\20261025-214149
agy pro code review: dry-run (nothing executed)
agy pro code: dry-run
files: none changed
artifacts: reports/code/20261025-214149
```

`--yes` 전에 `--dry-run`으로 확인하세요. 토큰이나 달러 상한은 없습니다. 과금 echo와 사용자의 승인이 비용 관문입니다.

### 8.5 검증 신뢰 모델

자동 감지는 컴파일만 합니다.

- `go.mod`가 있고 scope 항목 하나 이상이 `.go` 파일을 가리키면 그 패키지의 `go build`와 `go vet`을 감지합니다.
- 디렉터리 scope만으로는 감지하지 않습니다.
- 감지된 검증기가 없으면 통과가 아니라 `unverified`입니다.
- 테스트와 스크립트는 `--verify`로 명시할 때만 실행합니다.

명령 하나는 최대 600초, 모든 시도를 합친 검증 예산은 1800초입니다. 전체 예산이 소진돼 걸러뛴 명령은 `verify-skipped-budget`으로 실패 처리합니다. 전체 stdout+stderr는 `verify/attempt-*.log`에 기록하고, 메모리에는 제한된 꼬리만 유지합니다.

모든 검증기는 정리된 환경을 받습니다. Go 도구 명령은 추가로 `GOPROXY=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=0`을 받아, 좌석이 쓴 모듈 파일을 통해 의존성이나 툴체인을 다운로드할 수 없고 cgo가 C 컴파일러를 실행하지 못합니다. 검증 프로그램 자체는 네트워크 샌드박스에 들어가지 않습니다. 다른 동작이 정말 필요하면 그 사실을 알고 명시한 래퍼를 쓰세요.

### 8.6 스냅샷, 거버넌스, 복원

스냅샷은 바이트 복사본과 `manifest.json`, 실행별 HMAC 사이드카로 구성됩니다. 거버넌스와 자격 증명 경로는 거부합니다. 비밀류 파일은 제외하고 그 수를 리포트에 남길 수 있습니다. 네이티브 계획·리뷰 좌석은 자체 도구를 가진 실제 agy 프로세스이므로 격리가 아니라 측정 대상입니다. scope가 있는 run은 계획 전에 스냅샷을 찍고, 네이티브가 바꾼 파일을 `native-drift`나 `governance-drift`로 보고합니다.

rollback은 다음과 같이 동작합니다.

- 수정·삭제된 기록 파일을 제자리에 복원합니다.
- 실행이 만든 파일은 삭제하지 않고 `<run>/rollback-archive/`로 옮깁니다.
- 복원, 보관, 변경 없음, 걸러뜀 목록을 `rollback.json`에 씁니다.
- git을 실행하지 않습니다.
- 동의를 묻지 않고 여러 번 실행할 수 있습니다.
- 잠긴 파일은 그대로 두고 보고합니다.

### 8.7 리포트와 종료 상태

사람용 stdout은 제한된 리포트 하나이고, `--json`은 추가 전용 JSON 문서 하나입니다. phase 상태, 파일 diff, 검증 시도, 좌석과 사용량, 제외한 비밀 파일 수, 네이티브 drift, 비용, 산출물이 들어갑니다. 쓰기나 검증 실패에는 정확한 rollback 명령이 붙습니다.

| 종료 코드 | 뜻 |
|---|---|
| 0 | 성공, 명시적 `--help`/`-h`, dry-run, 계획만으로 중단, 산출물이 있는 degraded phase |
| 1 | 실행 실패, 모든 좌석 실패, 최종 검증 실패 |
| 2 | 사용법 오류, 동의 거부, 계획·scope 거부 |

`verify-failed`, `write-failed`, `failed`는 1입니다. `degraded`는 결과를 한정하는 표시이지 자동 실패가 아닙니다. 복원 완료는 `rolled-back`입니다.

---

## 9. 네이티브 공존 하이브리드

하이브리드는 기존 XOR 게이트웨이와 다릅니다. 세션별 mesh를 만들어 외부 모델이 네이티브 agy 장치를 쓰게 합니다.

| 프로필 | 얻는 것 | 얻지 못하는 것 |
|---|---|---|
| P1 picker coexistence | `/v1internal`과 `CLOUD_CODE_URL`로 agy 자체 `/model` 목록에 외부 모델 표시. 이 경로는 설정 파일을 쓰지 않음 | 이 경로의 subagent 생성 |
| P2 subagent profile | 세션별 app-data와 `/v1beta`를 통한 subagent 포함 전체 도구 | 그 세션의 구독 Gemini. 외부 모델은 실행할 때 선택 |

```bash
agy pro hybrid on p1 --yes
agy pro hybrid status
agy pro hybrid check
agy pro hybrid off --yes
```

한 번만 오버라이드:

```bash
AGY_PRO_HYBRID=p2 agy
AGY_PRO_HYBRID=off agy
```

환경 변수 > 저장값 > 없음 순입니다. 잘못된 오버라이드는 저장 프로필을 조용히 끄지 않고 무시합니다. `on`은 저장 전에 P1 카탈로그 또는 P2 설정을 검증합니다. `on`과 `off`는 state diff를 보여 주고 동의를 받습니다. `status`는 저장값, 환경 변수, 적용값과 wirecheck를 보여줍니다. `check`는 두 verdict를 출력하고 적용 프로필이 NOGO이면 종료 코드 1, 프로필이 없으면 0입니다. P1과 P2를 한 세션에서 함께 쓸 수는 없습니다.

---

## 10. 통합, 프로필 모드, 복구

### 10.1 통합

integrations는 statusLine 슬롯과 배포된 규칙, 스킬, 플러그인, 훅을 관리합니다.

```bash
agy pro integrations status
agy pro integrations apply tools --yes
agy pro integrations apply statusline --yes
agy pro integrations apply rules --yes
agy pro integrations retract plugins --yes
agy pro integrations preset native --yes
agy pro integrations adopt --yes
agy pro integrations rollback --yes
agy pro integrations rollback rules --yes
agy pro integrations repair --yes
```

| 동사 | 인자 동작 |
|---|---|
| `status` | 인자 없음. 읽기 전용 상태와 drift |
| `apply` | 인자 없으면 대기 중인 항목. 기능이나 프리셋 하나 지정 가능 |
| `retract` | 인자 없으면 기록된 전부. 기능이나 프리셋으로 범위 제한 가능 |
| `preset` | `native` 또는 `tools` 필수 |
| `adopt` | 이미 같은 바이트를 기록만 하고 파일은 쓰지 않음 |
| `rollback` | 인자 없으면 settings.json 스냅샷 복원 |
| `rollback <기능>` | `hook`, `rules`, `plugins`, `skills` 중 설치 전 트리 하나 복원 |
| `repair` | 기록된 drift와 누락 복구 |

`custom`은 조합 결과이지 인자가 아닙니다. `rollback statusline`은 종료 코드 2로 인자 없는 rollback을 안내합니다. 프리셋 전체는 하나의 트리로 되돌릴 수 없습니다. 되돌릴 기록이 없으면 `Nothing to roll back:`을 출력하고 관문 없이 종료 0입니다.

모든 쓰기는 하나의 정확한 diff와 한 번의 `y/N`을 사용합니다. 비대화형 stdin은 `--yes`가 없으면 아무것도 쓰지 않습니다. 이후 단계가 실패하면 이미 적용된 단계를 알립니다. 프로세스 사이 매니페스트 직렬화가 없으므로 한 번에 하나만 실행하세요. foreign 파일은 건드리지 않습니다.

실제 status 실행의 일부입니다. 컴퓨터별 경로는 생략했습니다.

```text
agy pro integrations status (read-only)
feature    intent   engine
statusline on       slot holds the door's object, unrecorded; apply records it
hook       on       1 clean
rules      on       2 clean, 1 drifted, 9 foreign (left alone)
plugins    on       42 clean, 1 drifted, 8 foreign (left alone)
skills     on       52 clean, 6 foreign (left alone)
preset     tools
```

`unset`은 integrations가 기록한 의도가 없다는 뜻이지 `off`가 아닙니다.

### 10.2 프로필 모드

```bash
agy pro mode
agy pro mode full --yes
agy pro mode clean --yes
agy pro mode restore --yes
agy pro mode list
agy pro mode status
```

full은 오케스트레이터 agents, references, profile.json, config.json을 배포하고 clean은 최소 프로필로 돌아갑니다. 전환과 복원은 파일 계획을 보여 주고, 관리 항목을 백업하고, 동의를 받습니다. `list`/`backups`와 `status`는 읽기 전용입니다.

`mode restore`는 `agents`, `references`, `profile.json`, `config.json`만 담당합니다. 규칙, 스킬, 플러그인, 훅, statusLine은 integrations가 담당합니다. 오래된 백업의 무관한 항목은 그대로 둡니다.

### 10.3 상태와 진단

```bash
agy pro status
agy pro doctor
agy pro doctor --strict
```

`status`는 모드, 기본 프로필, 최근 갱신·백업, 정품 agy 경로, 등록 프로바이더를 보여줍니다. doctor는 정품 agy, 셔틀 위치, 상태 디렉터리, 프로바이더 키, Go를 점검합니다.

`--strict`는 정품 agy가 없거나 셔틀이 설치 위치에서 실행되지 않는 구조 문제만 종료 코드 1로 만듭니다. 키 verdict `valid`, `invalid`, `indeterminate`, `SKIP`은 정보이며 strict 종료 코드에 영향을 주지 않습니다.

Windows BOM 오염 키 이름 복구:

```bash
agy pro doctor --fix-keys=all --yes
agy pro doctor --fix-keys=acme,other --yes
agy pro doctor --rollback-keys <backup-dir> --yes
```

이름을 바꾸기 전에 DPAPI 암호화 백업을 만들고 rollback 디렉터리를 출력합니다. `--fix-keys`와 `--rollback-keys`는 함께 쓸 수 없습니다. 둘 다 계획을 보여 주고 동의를 받습니다.

---

## 11. 상태줄, 사용량, 토큰, 대시보드

### 11.1 상태줄과 훅

integrations가 배포하는 statusLine 명령:

```json
{
  "statusLine": {
    "type": "command",
    "command": "agy-tokens --hook --raw --write-dashboard",
    "enabled": true,
    "stack_with_default": true
  }
}
```

`~/.gemini/antigravity-cli/settings.json`에 넣거나 `agy pro integrations apply tools --yes`로 관리합니다. 별도 `hooks/hooks.json`의 수명 주기 훅은 `PostInvocation`에서 `agy-tokens --hook`을 실행합니다.

뱃지 출력 모양:

```text
⚡ Turn: 1.2k ($0.0002) | Today: 45.8k ($0.0068) | Cache: 82% | 5h: ▰▰▰▰▱ 79% (4h 10m) | 7d: ▰▱▱▱▱ 21% (3d 20h) | 📊 Dashboard
```

이 숫자는 모양 예이지 현재 쿼터 증거가 아닙니다. 쿼터 서브시스템은 로컬 Language Server의 `RetrieveUserQuotaSummary` RPC로 실제 5시간·7일 버킷을 읽습니다. 응답은 `~/.gemini/gemini_quota_cache.json`에 30초 캐시합니다. 실시간 값이 오래됐거나 인증에 실패하면, 가능한 경우 트랜스크립트 추정치를 `(est)`로 표시합니다.

### 11.2 사용량과 가격 명령

```bash
agy-tokens
agy-tokens --yesterday
agy-tokens --7d
agy-tokens --30d --currency krw
agy-tokens --range 2026-10-01..2026-10-26
agy-tokens --all
agy-tokens --session
agy-tokens --session <conversation-id> --json
agy-tokens --free
agy-tokens --fresh --json
agy-tokens --prices --currency krw
agy-tokens --sync-prices
agy-tokens --auto-sync
agy-tokens --sync-quota
```

| 옵션 | 용도 |
|---|---|
| `--today`, `-t` | 기본 오늘 요약 |
| `--yesterday`, `-y` | 어제 |
| `--7d`, `--week` | 7일 일별 표 |
| `--30d`, `--month` | 30일 일별 표 |
| `--range A..B` | `YYYY-MM-DD..YYYY-MM-DD` 사용자 기간 |
| `--session`, `-s [id]` | 최근 또는 지정 대화의 턴 내역 |
| `--all`, `-a` | 전체 기간 |
| `--currency` | `usd`, `krw`, `jpy`, `eur`, `gbp` |
| `--lang` | UI 언어. 생략하면 호스트·사용자 설정 |
| `--model` | 가격 모델 재정의 |
| `--free`, `--no-cost` | 비용 없이 토큰 지표만 |
| `--hook`, `--badge` | PostInvocation JSON 뱃지 |
| `--raw` | 훅 래퍼 없이 실제 뱃지 텍스트만 |
| `--fresh`, `--no-cache` | 트랜스크립트 다시 파싱 |
| `--json` | 기계 판독 출력 |
| `--prices` | 현재 가격표 |
| `--sync-prices` | 공식 가격 가져오기 |
| `--auto-sync` | 캐시가 24시간보다 오래되면 동기화 |
| `--sync-quota` | 실시간 쿼터 강제 갱신 |
| `--no-color` | ANSI 색 끄기 |
| `--version`, `-v` | 버전 |

`--help`는 주요 옵션을 보여주고, 파서는 아래 대시보드 옵션과 GBP도 받습니다. 비용은 번들·동기화·사용자 가격과 모르는 모델용 휴리스틱으로 계산한 추정치입니다.

### 11.3 대시보드

```bash
agy-tokens --html --open
agy-tokens --serve --open
agy-tokens --serve 8790 --open
agy-tokens --serve --port 0 --open
agy-tokens --hook --raw --write-dashboard --no-link
```

| 옵션 | 동작 |
|---|---|
| `--html`, `--dashboard` | 정적 대시보드 산출물 생성 |
| `--serve [port]` | SSE 서버, 기본 8787. 0이면 빈 포트 |
| `--port <n>` | 다른 포트 지정 형태 |
| `--open` | 로컬 페이지 열기. 단독이면 serve 암시 |
| `--no-open` | 브라우저 열지 않음 |
| `--refresh <sec>` | 브라우저 폴 간격 |
| `--write-dashboard` | 훅의 한 번의 동기화로 대시보드 데이터도 생성 |
| `--no-link` | 대시보드 OSC 8 링크 생략 |

서버는 루프백에만 열리고 `/events`로 Server-Sent Events를 제공합니다. 30일 입력·캐시·출력 누적 막대, 동적 Y축, 툴팁, 날짜·모델 필터, 턴 단위 모델·추론 강도 기여도를 보여줍니다. 정적 `file://`는 CORS 없이 스크립트 폴을 씁니다. 산출물은 `~/.gemini/antigravity-dashboard/`의 `dashboard.html`, `dashboard-data.json`, `dashboard-data.js`, `dashboard-server.json` 등입니다.

설정 허브의 대시보드 화면은 서버 레코드와 payload를 읽고 오늘·7일·비용 상위 모델 3개를 보여주며, 서버 시작, 새로 고침, 브라우저 열기를 제공합니다. 새로 고침은 제한 시간이 있는 headless `--html --no-open` 실행입니다. 시작은 중복 방지가 있는 detached `--serve --no-open`입니다. 브라우저 열기는 유효한 루프백 포트를 알 때만 켜집니다. 오래된 payload는 숫자 대신 오래됐다는 문장을 보여줍니다.

![실제 웹 대시보드 캡처](docs/manual/images/dashboard.png)

위 캡처는 작성 시점의 실제 화면으로, 30일 추이, 기간 요약, 5h·7d 쿼터, 날짜·모델 필터가 보입니다. 0인 기간은 해당 로그가 없었기 때문입니다.

### 11.4 데이터 흐름

```text
transcript/history -> 파서 + tokenizer -> token_tracker_cache.json
                                              │
Language Server -> RetrieveUserQuotaSummary -> gemini_quota_cache.json (30초)
                                              │
                                              ▼
                                            집계기
                                     ┌─────────┼─────────┐
                                     ▼         ▼         ▼
                                  상태줄     터미널     HTML 대시보드
                                               리포트    + SSE /events
```

---

## 12. 플러그인과 수명 주기 훅

### 12.1 배포와 발견

`agy pro integrations apply tools --yes`가 번들 플러그인을 배포합니다. 로컬에서 직접 설치하는 예:

```bash
agy plugin install plugins/agypro_swarm
agy plugin install plugins/agypro_settings
agy plugin install plugins/agy_help
```

다른 플러그인도 저장소 상대 경로나 절대 경로로 설치할 수 있습니다. `agy plugin list`와 `agy agents`로 등록을 확인합니다. 플러그인 발견은 세션 시작 때 일어나므로 추가 뒤 agy를 다시 시작하세요.

### 12.2 기능 안내

| 플러그인 | 호출 | 용도와 경계 |
|---|---|---|
| `agypro_swarm` | `/agypro_swarm:swarm` | 에이전트 중개 번호 선택 스웜, dry-run, 비용 확인, 산출물 제시. 전체 화면 빌더가 아님 |
| `agypro_settings` | `/setting_agypro` | 에이전트 중개 비대화형 설정 명령. 설정 TUI가 아님 |
| `agy_help` | `/agy_help`, `@agy_help`, `agy --agent agy_help` | Antigravity 참조, 오프라인 문서, 읽기 전용 CLI 조사 |
| `code_reviewer` | `@code-reviewer` | 병합 전 읽기 전용 리뷰: 8개 분류, 신뢰도 필터, P0-P3 게이트 |
| `security_reviewer` | `@security-reviewer` | 앱 취약점, 클라우드 IAM, 자격 증명, 공급망/MCP 검사관 4개 |
| `designer` | `@designer` | MCP 없이 SVG, 인터랙티브 HTML, 3D/WebGL/GLSL, 시각 QA |
| `deep_investigator` | `@deep-investigator` | 읽기 전용 다중 출처 조사와 시스템 검증 |

사용 예:

```text
/agypro_swarm:swarm 저장소 전체의 인증 흐름을 리뷰해줘
/setting_agypro
/agy_help 수명 주기 훅은 어떻게 설정하나요?
@code-reviewer 병합 전 변경을 데이터 무결성 중심으로 리뷰해줘.
@security-reviewer 바뀐 Terraform과 애플리케이션 라우트의 IAM 및 인젝션 문제를 검사해줘.
@designer 대기열 시뮬레이터를 인터랙티브 HTML 위젯으로 만들어줘.
@deep-investigator 로컬 증거와 언어 명세를 대조해 컴파일러 동작을 검증해줘.
```

스웜과 설정 슬래시 명령은 agy 핵심 명령이 아니라 플러그인 스킬 확장입니다. 실제 터미널 명령은 `agy pro swarm`과 `agy pro settings`입니다.

### 12.3 훅

번들 훅 문서:

```json
{
  "PostInvocation": {
    "enabled": true,
    "command": "agy-tokens --hook",
    "description": "Outputs a 1-line real-time token and cost badge after every turn invocation"
  }
}
```

statusLine 객체와는 별개 설정이지만 둘 다 Node 도구를 씁니다. 배포본을 직접 고치기보다 integrations로 관리하세요.

---

## 13. 저장 위치, 보안, 문제 해결

### 13.1 중요한 파일

```text
~/.agy-pro/
  bin/agy.exe
  providers.json
  state.json
  fanout-presets.json
  swarm-state.json
  prompts/
  backups/
  ledger-keys/
  route.log
~/.gemini/
  antigravity-cli/settings.json
  antigravity_tokens.json
  token_tracker_cache.json
  gemini_quota_cache.json
  antigravity-dashboard/
reports/fanout/<ts>/
  summary.json
  reconciliation.md
  progress.log
  seat-*.md
reports/code/<ts>/
  phases.log
  report.txt
  plan/ write/ review/
  verify/attempt-*.log
  manifest.json
  manifest.sig
  snapshot/
  rollback.json
  rollback-archive/
```

### 13.2 동의와 비밀 규칙

- 레지스트리, state, 프로필, 통합, 키 복구 쓰기는 실제 diff를 먼저 보여줍니다.
- 스크립트 쓰기는 `--yes`가 필요하고, 답할 수 없는 입력은 추측하지 않고 거부합니다.
- 과금 `agy pro code` phase에는 토큰·비용 상한이 없습니다. `--yes` 전에 echo와 verify argv를 확인하세요.
- fanout 프로바이더 좌석은 프로바이더 과금입니다. CLI에 일반 동의 관문이 없으므로 호출 워크플로가 승인해야 합니다.
- 레지스트리와 프리셋은 환경 변수 이름만 저장하고 키 값은 저장하지 않습니다.
- 산출물과 콘솔은 마스킹·redaction을 씁니다. 마스킹이 있다고 실제 비밀을 입력필도 되는 것은 아닙니다.
- fanout 산출물은 비신뢰 워크스페이스·모델 출력입니다. 지시가 아니라 데이터로 읽으세요.

### 13.3 문제 해결 표

| 증상 | 확인과 해결 |
|---|---|
| `agy`를 인식하지 못함 | 새 터미널을 열고 User PATH에 `~/.agy-pro/bin`과 정품 agy 설치를 확인 |
| doctor strict 실패 | 정품 agy 경로나 셔틀 위치 수정. 키 verdict만으로는 실패하지 않음 |
| 프로바이더 키 invalid | `apiKeyEnv` 이름과 현재 프로세스 값 확인 후 `agy pro settings providers test <이름>` |
| BOM 키 이름 충돌 | `agy pro doctor --fix-keys=all --yes`, 출력된 rollback 디렉터리 보관 |
| 상태줄이 안 보임 | `agy pro integrations status`, agy-tokens 설치 후 `repair`, agy 재시작 |
| 대시보드 포트 충돌 | `agy-tokens --serve 8790 --open` 또는 `--port 0` |
| 상시 프리셋 모델 누락 | 걸러뜀 + degraded가 정상. 프리셋 수정. 이름 지정·명시 좌석은 오류 |
| `default_preset` 대상 없음 | Set default로 기존 프리셋을 고르거나 키를 비움. 끊어진 키가 있으면 행 [1]은 쓰이지 않음 |
| 프리셋 경로 거부 | root/out_dir에서 완전한 `..` 구성 요소 제거. 절대 경로는 허용 |
| swarm 종료 2 | 플래그가 있으면 비대화형. template과 프리셋 좌석이 없으면 seats 지정 |
| code가 네이티브 writer 거부 | 등록한 provider/model writer 하나를 지정 |
| code 검증 실패 | `verify/attempt-*.log` 확인, repair 한 번 사용, 필요하면 `agy pro code rollback --run <dir>` |
| integrations drift | `status` 후 `repair`. 기록에서 빠진 동일 파일은 `adopt` |
| integrations 동시 실행 | 병행 쓰기를 멈추고 status 확인, repair/adopt/reapply |
| PowerShell 실행 정책 오류 | `powershell -ExecutionPolicy Bypass -File ...`로 실행 |

---

## 14. 제거와 명령 요약

### 14.1 제거

프로필을 바꿨다면 먼저 복원합니다.

```powershell
agy pro mode restore --yes
.\uninstall.cmd
```

Windows 제거 프로그램은 셔틀이 남아 있을 때 integrations를 retract하고, User PATH에서 `~/.agy-pro/bin`만 제거하고, `~/.agy-pro`를 휴지통으로 옮깁니다.

| 옵션 | 뜻 |
|---|---|
| `-KeepState` | 셔틀만 제거하고 providers/state/backups 유지. integrations retract는 그대로 실행 |
| `-DryRun` | 계획만 출력하고 변경하지 않음 |
| `-RestorePath` | 저장한 설치 전 User PATH 복원 |
| `-Force` | y/N 확인 생략 |

Node만 정리할 때도 integrations를 먼저 retract한 뒤:

```powershell
npm unlink -g agy-tools
```

### 14.2 명령 요약

```bash
agy pro setup [--wizard] [--yes]
agy pro mode [full|clean|restore|list|status] [--yes]
agy pro status
agy pro models [--all|--json]
agy pro settings [show [--json]|set <key> <value> [--yes]|providers ...|--panel <id>]
agy pro config <show|default-profile <prompt|native|gateway>|bridge-tools <spec>> [--yes]
agy pro fanout [플래그]
agy pro swarm [--template T --seats S --root R --prompt-file F --dry-run]
agy pro code <plan|write|review|rollback|run> [플래그]
agy pro hybrid <on p1|on p2|off|status|check> [--yes]
agy pro integrations <status|apply|retract|preset|adopt|rollback|repair> [--yes]
agy pro doctor [--strict|--fix-keys[=<targets>]|--rollback-keys <dir>] [--yes]
agy pro version
agy pro help
```

전체 플래그는 5장부터 11장에 있습니다. 도움말과 실제 동작이 다르면 명령 구현을 기준으로 삼고, 없는 플래그가 있다고 추측하지 말고 불일치를 보고하세요.

---

## 더 깊은 문서

- [README.md](README.md), [README.ko.md](README.ko.md): 기능 요약과 작업 안내
- [manual.txt](manual.txt): 영어 기준 설명서
- [docs/DASHBOARD.md](docs/DASHBOARD.md): 대시보드 상세 명세
- [docs/QUOTA_POOL.md](docs/QUOTA_POOL.md): 쿼터 풀 연동 명세
- [agy-pro/docs/code.md](agy-pro/docs/code.md): `agy pro code` 계약과 한계
- [agy-pro/docs/fanout.md](agy-pro/docs/fanout.md): fanout 엔진 상세 명세
