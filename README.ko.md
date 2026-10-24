# ⚡ agy-pro

<div align="center">

**[English](README.md)** | **[한국어](README.ko.md)**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Zero Dependencies](https://img.shields.io/badge/Dependencies-0%20(stdlib%20only)-brightgreen.svg)](#under-the-hood)
[![Node.js Version](https://img.shields.io/badge/Node.js-%3E%3D16.0.0-green.svg)](https://nodejs.org)
[![i18n Supported](https://img.shields.io/badge/i18n-21%20Languages%20(RTL)-orange.svg)](#analytics)
[![MCP Servers](https://img.shields.io/badge/MCP%20servers-0-blueviolet.svg)](#under-the-hood)
[![Daemons](https://img.shields.io/badge/daemons-0%20at%20rest-blueviolet.svg)](#under-the-hood)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#install)

**엔진 둘, 저장소 하나. `agy-tokens`는 매 턴의 비용을 알려주고, `agy pro`는 두 개 이상의 모델을 같은 `agy` 터미널 뒤에 세웁니다.**

</div>

---

<a id="toc"></a>

`agy-tokens`는 Antigravity CLI 로그와 로컬 Language Server를 읽어 토큰, 달러, 실제 5h/7d Gemini quota를 statusline과 브라우저 dashboard에 보여줍니다. `agy pro`는 실제 `agy` 바이너리를 감싼 Go shim입니다. 네이티브 Gemini와 Claude 좌석이 무료로 계획과 리뷰를 맡고, 외부 모델이 scope 안에서 실제 코드를 쓰며, `y/N` 없이는 어떤 것도 과금되지 않습니다.

**목차:** [직접 느끼는 변화](#what-you-feel) · [`agy pro`에 들어 있는 것](#what-agy-pro-contains) · [코딩 도어](#coding-door) · [agy vs agy-pro](#difference) · [분석](#analytics) · [설치](#install) · [브랜치](#branches) · [내부 구조](#under-the-hood) · [안전](#safety) · [문서](#docs)

> [!NOTE]
> 토큰과 비용 숫자는 이 프로젝트 자체 tokenizer와 휴리스틱 추정 엔진이 로컬에서 계산합니다. 프로바이더 청구서가 아니라 추정치입니다.

---

<a id="what-you-feel"></a>

## 🎭 직접 느끼는 변화

![전후 비교: 순수 agy와 agy-pro가 붙은 agy](docs/assets/agy-vs-agy-pro.svg)

터미널에서 바뀌는 것은 없습니다. `agy`는 여전히 `agy`입니다. 같은 TUI, 같은 키, 같은 종료 코드입니다. 요청이 커졌을 때 방 안에 누가 더 있는지가 달라집니다.

위쪽 레인에서는 모델 하나가 모든 것을 답합니다. `/model`은 바이너리 수준에서 XOR이므로 한 세션은 네이티브 모델 **또는** 브리지 모델만 담고 둘 다 담지는 못합니다. 두뇌가 하나뿐이라 자기 답을 자기가 채점합니다. 아래쪽 레인에서는 같은 프롬프트가 Gemini master에 도착하고 master가 분배합니다. 구독에 포함된 무료 네이티브 좌석, 다른 모델 패밀리의 외부 프로바이더 좌석, 그리고 엔진이 먼저 snapshot을 뜬 scope 안에서만 과금 좌석이 쓸 수 있는 방어 경로입니다.

마찰이 없다고 느껴지는 이유는 이 경로들이 **슬래시 명령이 아니라 상시 지시**이기 때문입니다. `agy` 안의 master 에이전트가 워크스페이스 `AGENTS.md`에 라우팅 규칙을 들고 있으므로, 평범한 문장 하나로 발동합니다.

| 입력 (일반 채팅, 명령 아님) | 실행되는 것 |
|---|---|
| `review the auth flow across the repo` | 읽기 전용 `review` fanout. 서로 다른 모델 패밀리 좌석, severity가 붙은 reconciliation matrix 하나 |
| `audit this repo for leaked keys and OWASP issues` | 읽기 전용 `security` fanout, 같은 좌석 규칙 |
| `compare these two designs, which one breaks first?` | `research` fanout. 좌석들이 논쟁하고 master가 증거로 중재 |
| `audit my plan before I build it` | `plan-audit` fanout, 네이티브 좌석, 추가 비용 `$0` |
| `fix the auth bug` | `agy pro code`: 무료 네이티브 plan, 그다음 scope 하나 안에서 과금 프로바이더 write, 엔진 검증, 트리를 되돌려야 하면 snapshot rollback |
| `just answer` / `no swarm` | 아무것도 fanout하지 않음. 단일 파일 수준의 질문은 바로 답변 |

한국어는 별도 모드가 아니라 같은 문장입니다. `"인증 버그 고쳐줘"`와 `"리뷰해줘"`가 동일한 경로를 발동합니다.

리뷰 좌석들은 서로를 보지 못합니다. 어떤 좌석도 다른 좌석의 프롬프트, 도구 출력, 가설을 받지 않습니다. 그래서 두 모델의 일치는 한 모델이 다른 모델을 따라 말한 것이 아니라, 정말로 둘이 같은 결론에 도달했다는 뜻입니다.

---

<a id="what-agy-pro-contains"></a>

## 🧩 `agy pro`에 들어 있는 것

![agy pro 구성 요소](docs/assets/agy-pro-feature-map.svg)

동사 열네 개, 바이너리 하나. 나머지(`agy agents`, `agy -m ...`, `agy -p ...`)는 실제 바이너리로 바이트 단위 그대로 전달됩니다.

| 동사 | 하는 일 | 언제 쓰는가 |
|---|---|---|
| `setup` | 전체 화면 설정 TUI: 프로바이더, 모델, fanout 프리셋, 프롬프트, UI 언어. 고전 줄 단위 마법사는 `--wizard` | 첫 실행, 그리고 registry가 바뀔 때마다 |
| `mode` | 프로필을 `full`(full-stack 오케스트레이터)과 `clean` 사이에서 전환하거나 `restore` | 같은 바이너리, 다른 배포 내용 |
| `status` | 활성 모드, 등록된 프로바이더, 실제 `agy` 경로 | 뭔가 이상할 때 치는 명령 하나 |
| `models` | 설정된 프로바이더와 모델. `--all`은 네이티브와 외부를 합친 번호 목록을 더 보여줌 | 이름이 아니라 번호로 좌석을 고를 때 |
| `swarm` | 번호 기반 빌더: 템플릿과 좌석을 번호로 고르고, 좌석별 비용을 붙인 dry-run echo 뒤 실행 | 좌석 문법을 외우지 않고 하이브리드 스웜을 돌릴 때 |
| `settings` | 전체 화면 설정 패널, 그리고 `show` / `set` | JSON을 열지 않고 설정 하나를 바꿀 때 |
| `integrations` | 배포된 규칙, 플러그인, 기능 폴더, `statusLine` 슬롯을 위한 `status`, `apply`, `retract`, `preset`, `adopt`, `rollback`, `repair` | agy-pro가 디스크에 놓는 모든 파일의 단일 소유자 |
| `hybrid` | 네이티브 공존 프로필 (`on p1`, `on p2`, `off`, `status`, `check`) | 네이티브 설정과 agy-pro 설정을 나란히 둘 때 |
| `doctor` | 툴체인, 경로, 키. `--strict`, `--fix-keys`, `--rollback-keys` | 토큰을 쓰지 않는 업스트림 키 probe와 마스킹된 판정 |
| `fanout` | 멀티모델 스웜 엔진: `review`, `security`, `research`, `plan-audit`, 그리고 파티션 모드(`--units`) | 다른 모든 멀티모델 표면 아래에 있는 엔진 |
| `code` | `plan`, `write`, `review`, `rollback`, `run` | 코딩 도어([다음 절](#coding-door)) |
| `config` | 영구 기본값: `default-profile`, `bridge-tools` | 시작 동작을 한 번만 정할 때 |
| `version` | agy-pro 버전과 그 아래 `agy` 버전 | 버그 리포트 |
| `help` | 사용법 텍스트, TUI 먼저 | 기능 탐색 |

쓰기를 하는 모든 동사(`config`, `mode`, `doctor --fix-keys`, 설정 허브의 프로바이더·모델 편집 각각, integration apply 각각)는 정확한 diff를 출력하고 `y/N`을 기다립니다. 스크립트 호출자는 `--yes`를 붙입니다. 답할 수 없는 입력은 추측하지 않고 거부합니다.

동사를 직접 치고 싶지 않다면, 나머지 런처 표면:

```bash
agy pro setup                            # 전체 화면 허브, 첫 실행에 권장
AGY_LAUNCHER=1 agy                       # 옵트인 2초 픽커: [1] 네이티브 Gemini [2] 외부 [3] 스웜
agy pro models --all                     # 번호 목록, 네이티브 행과 프로바이더 행
agy pro swarm                            # TTY에서 도는 번호 기반 빌더
agy pro code --help                      # 코딩 도어의 전체 표면
```

인자 없이 `AGY_LAUNCHER=1` 없이 실행한 `agy`는 추가 지연 0으로 네이티브 Gemini로 바로 갑니다. 픽커는 인자 없는 `agy`에만 붙습니다. 인자가 하나라도 있으면 건너뜁니다.

---

<a id="coding-door"></a>

## 🛠 코딩 도어: `agy pro code`

![agy pro code 파이프라인](docs/assets/code-door-lifecycle.svg)

여러 모델이 나눠 처리하는, 범위가 정해진 변경 하나를 위한 명령 하나입니다. 네이티브 좌석이 구독 안에서 plan과 review를 맡고(추가 비용 `$0`), 외부 프로바이더 좌석 하나가 scope 안의 파일을 씁니다(과금 API). 엔진이 첫 write 전에 트리 snapshot을 뜨고, 검증을 직접 실행하고, 범위가 정해진 repair를 한 번 허용하고, 트리를 바이트 단위로 되돌릴 수 있습니다.

```bash
# 1. plan만: 네이티브 좌석, 무료. 실행 디렉터리를 쓰고 멈춥니다.
agy pro code plan --ask-file q.txt

# 2. plan을 읽고, 범위가 정해진 scope 하나로 실행합니다.
#    --writer는 프로바이더 좌석이므로 이 단계는 과금이며 y/N을 묻습니다.
agy pro code run --ask-file q.txt --scope "internal/x" \
  --writer <provider/model> --verify "go build ./..." --yes

# 3. 검증이 실패했다면(exit 1), 리포트가 정확한 되돌리기 명령을 알려줍니다:
agy pro code rollback --run reports/code/<ts>
```

각 phase가 실제로 하는 일:

- **plan과 review는 네이티브 좌석에서 돌고 기본이 무료입니다.** 기본 라인업은 `native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high`입니다. 네이티브만 쓰는 phase는 구독 외에 드는 비용이 없으므로 동의를 묻지 않습니다.
- **write phase는 과금이고 동의를 묻습니다.** writer는 프로바이더 좌석만 될 수 있습니다. 네이티브 좌석은 scope가 걸린 도구 러너를 만들지 않으므로, 이 동사는 실행 전 사용법 단계에서 네이티브 `--writer`를 거부합니다.
- **검증은 좌석이 아니라 엔진의 것입니다.** 컴파일 검사는 자동입니다(scope가 `go.mod` 아래 `.go` 파일을 가리키면 `go build` + `go vet`). 테스트 러너를 포함한 다른 검증기는 `--verify`로 직접 지정할 때만 실행됩니다.
- **실행 디렉터리가 감사 기록입니다.** `reports/code/<ts>/phases.log`(phase마다 한 줄), `report.txt`(리뷰가 읽는 변경 리포트), phase별 `summary.json` / `reconciliation.md` / `seat-*.md`, 시도별 `verify/attempt-<n>-<cmd>.log`, 그리고 snapshot 자체입니다.

`--dry-run`은 조합된 정확한 argv(fanout 호출, 모든 검증기, rollback 줄)를 출력하고 아무것도 띄우지 않은 채 exit 0으로 끝납니다. 토큰을 쓰기 전에 과금 실행을 미리 볼 수 있습니다.

### 안전 모델, 그대로 적으면

- **scope가 걸린 프로바이더 write.** write 대상은 어휘적 루트 상대 경로나 해석된 실제 경로 중 하나라도 unit의 scope glob 안에 있을 때만 scope 안으로 봅니다. snapshot이 주장하는 범위도 같은 이중 시점으로 정해지므로, 좌석이 쓸 수 있는 파일과 rollback이 복원할 수 있는 파일은 같은 집합입니다.
- **거버넌스 파일은 모든 좌석 write를 거부합니다.** `AGENTS.md`, `gemini.md`, `claude.md`, `providers.json`과 그 형제 별칭들, `state.json`, `state-fanout.json`, `fanout-presets.json`, `integrations.json`, `settings.json`, 그리고 깊이와 무관하게 `rules/`, `.agy-pro/`, `.gemini/` 디렉터리 구성 요소입니다. `.git/**`는 snapshot 시점에 거부됩니다. 검사는 해석된 디렉터리 체인을 따라가므로 symlink로 거버넌스 경로를 세탁할 수 없고, Windows에서는 Win32 표준 이름으로 비교하므로 `AGENTS.md.`와 `AGENTS.md:stream`도 거부됩니다.
- **rollback은 삭제하지도, git을 실행하지도 않습니다.** snapshot은 바이트 복사본입니다(`manifest.json` 더하기 `snapshot/<relpath>`). rollback은 제자리에서 복원하고, 실행이 만들어낸 파일은 `<out>/rollback-archive/`로 옮깁니다. 잠긴 파일은 있던 자리에 그대로 두고 `rollback.json`에 이름을 남깁니다. 이 경로 어디에서도 `git`을 호출하거나 import하거나 파싱하지 않습니다.
- **원장은 자기 실행에 묶여 있습니다.** 모든 `manifest.json`은 `manifest.sig`를 함께 가지며, 이는 실행별 키로 manifest 바이트에 계산한 HMAC-SHA256입니다. 그 키는 모든 작업 루트 밖에 보관합니다. 손으로 고치거나 위조한 원장은 바이트 하나가 쓰이기 전에 통째로 거부됩니다.
- **비용 규칙은 하나입니다. 먼저 echo, 그다음 승인.** 과금 좌석은 모두 마스킹된 echo에 이름이 나오고, 그 echo는 상한이 없다고 분명히 적고, `y/N`이나 `--yes` 없이는 아무것도 실행되지 않습니다.
- **일부만 실패한 좌석은 `degraded`로 적고 `ok`로 적지 않습니다.** 이 도어는 엔진 자신의 좌석별 행을 다시 읽습니다. 일부 좌석이 실패하고 나머지가 결과를 냈다면 그 phase는 `degraded`이고, 리포트는 그 사실을 세 가지 방식으로 적습니다. 모든 좌석이 실패하면 exit 1입니다.
- **솔직한 단서: 네이티브 좌석은 측정 대상이지 격리 대상이 아닙니다.** 네이티브 좌석은 자체 도구 표면을 가진 진짜 `agy` 서브프로세스이므로 plan이나 review 좌석도 쓸 수 있습니다. 그래서 이 도어는 plan phase 전에 snapshot을 뜨고, 네이티브 phase가 끝날 때마다 scope를 다시 훑고, 그 차이를 `native-drift`로 보고합니다(거버넌스 파일이면 `governance-drift`도). 소유하지 않은 sandbox를 주장하는 대신 실제로 일어난 일을 적습니다.

형용사 없이 적는 비용 사실: 이 도어 어디에도 토큰이나 달러 상한은 없습니다. 실행을 묶는 것은 엔진의 좌석별 반복 한도와 실제 시간(wall clock)이고, 리포트는 엔진의 `summary.json`이 토큰 수를 담고 있을 때만 그 수치를 출력합니다. 예산 미터는 앞으로의 결정 사항이지, 이미 출시된 기능이 아닙니다.

---

<a id="difference"></a>

## ⚖️ `agy` 단독 vs `agy` + agy-pro

| | `agy` 단독 | `agy` + agy-pro |
|---|---|---|
| **모델 선택** | `/model` 픽커는 XOR: 한 번에 한 패밀리, 네이티브 또는 브리지 | 모든 패밀리가 한 스웜에서 함께 동작. 네이티브 좌석과 프로바이더 좌석이 같은 실행에 |
| **코딩 손** | 네이티브 에이전트만 | 외부 프로바이더 모델이 범위가 정해진 scope 하나 안에서 실제 코드를 씀 |
| **아이디어 다양성** | 두뇌 하나, 계획 하나 | plan과 review 좌석이 병렬로 돌고 하나의 matrix로 조정됨 |
| **리뷰** | 자기 리뷰 | severity와 합의/불일치 구분이 있는 독립 멀티모델 fanout |
| **비용 가시성** | 없음 | 5h/7d quota 버킷 실시간, 매 턴 statusline, 실행별 좌석 사용량, 지출 전 마스킹 echo |
| **설정** | 파일 직접 편집 | 설정 허브 TUI, integration apply마다 동의 한 번 |
| **폴백** | 없음 | `mode clean` / `mode full`, `restore`, `doctor`, snapshot rollback, 좌석이 빠지면 `degraded` 상태 |

agy-pro가 바꾸지 않는 단 하나: 세션 안 `/model` 팝오버는 그대로 XOR입니다. 그 제약이 바이너리 안에 있기 때문입니다. 합쳐진 목록은 한 계층 위에 있습니다(`agy pro models --all`, 런처 `[2]`, `agy pro swarm`).

---

<a id="analytics"></a>

## 📈 분석: statusline, quota, dashboard

`agy-tokens`는 `~/.gemini/antigravity-cli/settings.json`의 `statusLine` 항목 하나만 필요로 하고, Antigravity 자체는 바꾸지 않습니다. `agy pro integrations apply tools`가 그 항목을 쓰고(쓰기 전에 파일을 먼저 snapshot), `agy pro integrations status`가 아직 일치하는지 보여주며, `agy pro integrations rollback`이 snapshot을 복원합니다.

```text
⚡ Turn: 1.2k ($0.0002) | Today: 45.8k ($0.0068) | Cache: 82% | 5h: ▰▰▰▰▱ 79% (4h 10m) | 7d: ▰▱▱▱▱ 21% (3d 20h) | 📊 Dashboard
```

```text
⚡ 이번 턴: 1.2k (₩0.3) | 오늘 누적: 45.8k (₩9.9) | 캐시: 82% | 5h: ▰▰▰▰▱ 79% (4h 10m) | 7d: ▰▱▱▱▱ 21% (3d 20h) | 📊 대시보드
```

**1:1 Gemini quota.** quota 서브시스템은 로컬 Antigravity Language Server에 HTTPS/HTTP RPC(`RetrieveUserQuotaSummary`)로 질의해 실제 롤링 버킷 두 개를 보여줍니다. **5시간(`5h`)** 버스트 윈도우와 **7일(`7d`)** 주간 허용량이고, 각각 실시간 카운트다운이 붙습니다. 응답은 `~/.gemini/gemini_quota_cache.json`에 30초 캐시되므로 statusline 조회는 1ms 미만으로 유지되고, 갱신이 턴을 막지 않습니다.

**`statusLine` 항목 자체.** 직접 연결하는 편이 나은 경우(컨테이너, 잠긴 `PATH`, 또는 엔진이 호출할 `agy`가 `PATH`에 없는 환경)에는 `~/.gemini/antigravity-cli/settings.json`에 이 블록을 넣으세요:

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

직접 Node 경로 지정: `"command": "node /path/to/agy-pro/bin/agy-tokens.js --hook --raw --write-dashboard"`. Windows에서 서브프로세스가 `PATH`를 물려받지 못하면 8.3 축약 경로를 쓰고 모든 역슬래시를 이스케이프하세요:

```text
"command": "C:\\PROGRA~1\\nodejs\\node.exe %APPDATA%\\npm\\NODE_M~1\\AGY-TO~1\\bin\\AGY-TO~1.JS --hook --raw --write-dashboard"
```

**8787 포트의 실시간 dashboard.** 루프백 서버 하나, `/events`의 Server-Sent Events, 반응형 순수 SVG 차트입니다. 30일 토큰 볼륨은 입력·캐시·출력을 쌓은 스택 막대이고, Y축은 동적으로 스케일되며, 인터랙티브 툴팁, 날짜 범위(오늘, 7d, 30d, 사용자 지정)와 모델 기준 필터, reasoning effort까지 포함한 턴별 모델 기여도를 제공합니다. 같은 페이지가 스크립트 태그 폴링으로 `file://`에서도 CORS 오류 없이 동작합니다.

```bash
agy-tokens --serve --open     # 127.0.0.1:8787에 SSE 서버를 열고 브라우저 실행
agy-tokens --html --open      # 스스로 갱신하는 정적 HTML 산출물
agy-tokens --sync-quota       # 실시간 quota 풀을 지금 갱신
```

**데이터 경로, 처음부터 끝까지.**

```mermaid
flowchart LR
    A["agy 세션<br/>transcript.jsonl + history.jsonl"] --> B["로그 파서<br/>+ BPE tokenizer"]
    B --> C[("token_tracker_cache.json")]
    LS["Language Server<br/>language_server / agy.exe"] -->|"HTTPS RPC<br/>RetrieveUserQuotaSummary"| Q["quota 서브시스템"]
    Q --> Qc[("gemini_quota_cache.json<br/>30s TTL")]
    C --> D["집계기"]
    Qc --> D
    D --> E["statusline 뱃지<br/>--hook --raw"]
    D --> F["HTML 리포트"] --> G["SSE 서버<br/>127.0.0.1:8787"] --> H["브라우저 UI"]
```

**모델과 가격 matrix.** 서브워드 토큰 단가, 프롬프트 캐시 할인, 공식 가격 변경 동기화 엔진:

| 모델 ID | 표시 이름 | 제공사 | 컨텍스트 | 입력 / 1M | 캐시 / 1M | 출력 / 1M |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `gemini-3.7-flash` | **Gemini 3.7 Flash** | Google | 1M | $0.15 | $0.0375 | $0.60 |
| `gemini-3.7-flash-thinking` | **Gemini 3.7 Flash (Thinking)** | Google | 1M | $0.15 | $0.0375 | $0.60 |
| `gemini-3.6-flash` | **Gemini 3.6 Flash** | Google | 1M | $0.15 | $0.0375 | $0.60 |
| `gemini-3.5-flash` | **Gemini 3.5 Flash** | Google | 1M | $0.15 | $0.0375 | $0.60 |
| `gemini-2.5-pro` | **Gemini 2.5 Pro** | Google | 2M | $1.25 | $0.3125 | $5.00 |
| `gemini-2.0-flash` | **Gemini 2.0 Flash** | Google | 1M | $0.10 | $0.0250 | $0.40 |
| `claude-opus-4.6` | **Claude Opus 4.6** | Anthropic | 200k | $15.00 | $1.50 | $75.00 |
| `claude-sonnet-4.6` | **Claude Sonnet 4.6** | Anthropic | 200k | $3.00 | $0.30 | $15.00 |
| `claude-3.5-haiku` | **Claude 3.5 Haiku** | Anthropic | 200k | $0.80 | $0.08 | $4.00 |
| `gpt-4o` | **GPT-4o** | OpenAI | 128k | $2.50 | $1.25 | $10.00 |
| `o3-mini` | **o3-mini** | OpenAI | 200k | $1.10 | $0.55 | $4.40 |
| `o1` | **o1** | OpenAI | 200k | $15.00 | $7.50 | $60.00 |

```bash
agy-tokens --prices --currency krw    # 공식 카탈로그를 표로
agy-tokens --sync-prices              # 최신 공식 가격을 가져옴
agy-tokens --auto-sync                # 로컬 카탈로그가 24시간보다 오래되면 동기화
```

표에 없는 모델(신규 릴리스, 로컬 모델)은 `flash`, `pro`, `mini`, `free`, `local`에 대한 정규식 휴리스틱으로 요금 티어를 고릅니다.

**21개 언어, RTL 포함.** 로케일은 호스트(`LANG`, `LC_ALL`)에서 감지하고, 출력 레이아웃은 아랍어와 히브리어를 위한 양방향 처리를 따로 가집니다.

| 지역 | 언어 |
| :--- | :--- |
| 동아시아 | English (`en`), 한국어 (`ko`), 日本語 (`ja`), 简体中文 (`zh`), 繁體中文 (`zh-TW`) |
| 남아시아·동남아시아 | हिन्दी (`hi`), Tiếng Việt (`vi`), Bahasa Indonesia (`id`), ภาษาไทย (`th`) |
| 유럽 | Deutsch (`de`), Français (`fr`), Español (`es`), Português (`pt`), Italiano (`it`), Nederlands (`nl`), Polski (`pl`), Svenska (`sv`), Русский (`ru`), Türkçe (`tr`) |
| 중동 (RTL) | العربية (`ar`), עברית (`he`) |

`agy pro setup` 허브는 자체 12개 언어 UI를 제공하며, 아랍어는 터미널에 bidi 셰이핑이 없으므로 논리 순서까지만 정직하게 지원합니다.

### `agy-tokens` CLI, 짧은 목록

`agy-tokens`, `agy-tools`, `agy-dashboard`는 같은 명령입니다.

| 옵션 | 제공하는 것 |
| :--- | :--- |
| *(없음)*, `--today`, `-t` | 오늘 사용량 요약 |
| `--yesterday`, `-y` / `--7d` / `--30d` / `--all` / `--range YYYY-MM-DD..YYYY-MM-DD` | 그 밖의 다른 기간 |
| `--session [id]`, `-s` | 최근 또는 이름을 지정한 대화의 턴별 내역 |
| `--currency <usd\|krw\|jpy\|eur\|gbp>` / `--lang <code>` | 표시 통화와 UI 언어 |
| `--free`, `--no-cost` | 금액을 숨기고 토큰만 표시(정액제 모드) |
| `--json` | 스크립트용 원본 JSON |
| `--hook`, `--badge` / `--raw` | statusline 페이로드 / 원본 뱃지 문자열 |
| `--fresh`, `--no-cache` | 캐시를 건너뛰고 트랜스크립트를 다시 파싱 |
| `--prices`, `--sync-prices`, `--auto-sync` | 가격 카탈로그, 동기화, 자동 동기화 |
| `--sync-quota` | 실시간 Gemini quota 풀 갱신 |
| `--html`, `--dashboard` / `--serve [port]` / `--open` / `--refresh <sec>` / `--port <n>` | 정적 리포트 / SSE 서버(기본 `8787`, `0`이면 임의 포트) / 브라우저 열기 / 폴링 간격 |
| `--write-dashboard` / `--no-link` / `--no-color` | statusline 쓰기 쪽, OSC 8 링크 끄기, 색 끄기 |
| `--model <name>` / `--help`, `-h` / `--version`, `-v` | 단가 재정의, 도움말, 버전 |

---

<a id="install"></a>

## 🚀 설치와 빠른 시작

사전 요구사항: 분석 쪽을 위한 **Node.js 16+**([nodejs.org](https://nodejs.org)), **Antigravity CLI**(`agy`), shim을 위한 **Go 1.22+**(없으면 설치 프로그램이 준비합니다).

**Windows, 명령 프롬프트:**

```cmd
git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat
```

**Windows, PowerShell (5.1은 맨 `&&`을 받지 않으므로 `cmd /c`로 감쌉니다):**

```powershell
cmd /c "git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat"
```

**Linux / macOS:**

```bash
git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && bash scripts/install.sh
```

Node 설치 프로그램은 `npm link`로 전역 명령을 등록하고, `agy`가 `PATH`에서 잡히면 Antigravity 배포를 Go 엔진에 한 번에 넘깁니다(`agy pro integrations apply tools --yes`). `agy-tokens` 런처가 없으면 statusline 단계를 사유 한 줄과 함께 건너뛰고 그래도 exit 0으로 끝납니다. 나중에 `agy pro integrations repair`가 그 단계를 완료합니다.

**이제 shim을 추가합니다(git이 있든 없든 동작하는 한 줄):**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "if (Get-Command git -ErrorAction SilentlyContinue) { cmd /c 'git clone -b feature/agy-pro https://github.com/myk1yt/agy-pro.git && cd agy-pro && install.cmd' } else { iwr 'https://codeload.github.com/myk1yt/agy-pro/zip/refs/heads/feature/agy-pro' -OutFile '$env:TEMP\agy-pro-src.zip' -UseBasicParsing; Expand-Archive -Path '$env:TEMP\agy-pro-src.zip' -DestinationPath '$env:TEMP\agy-pro-src' -Force; & (Resolve-Path '$env:TEMP\agy-pro-src\*\install.ps1').Path }"
```

설치 프로그램은 `agy.exe`를 `~/.agy-pro/bin/`에 빌드하고, 그 디렉터리를 사용자 `PATH` 앞에 붙이고, `agy pro doctor`를 실행합니다. 설치 후 새 터미널을 열어야 `PATH` 변경이 반영됩니다.

**첫 실행:**

```bash
agy pro setup            # 전체 화면 허브: 프로바이더, 모델, fanout 프리셋, 프롬프트, 언어
agy                      # 네이티브 Gemini, 추가 지연 0
AGY_LAUNCHER=1 agy       # 옵트인: 2초 픽커 [1] 네이티브 [2] 외부 [3] 스웜
agy pro code --help      # 코딩 도어 전체를 한 화면에
agy pro status           # 활성 모드, 프로바이더, 실제 agy 경로
```

**npm 전역 사본 갱신(런타임은 클론이 아니라 전역 사본을 씁니다):**

```cmd
cd agy-pro
npm install -g .
```

`git pull`을 할 때마다 다시 실행하세요. `agy-tokens --version`과 `where agy-tokens`가 어느 사본이 살아 있는지 알려줍니다. `where`가 `AppData\Roaming\npm`을 가리키지 않으면 터미널을 다시 열고 재링크하세요.

**설치 제거, 한 줄:**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "iwr 'https://raw.githubusercontent.com/myk1yt/agy-pro/feature/agy-pro/agy-pro/scripts/uninstall.ps1' -OutFile '$env:TEMP\agy-pro-uninstall.ps1' -UseBasicParsing; & '$env:TEMP\agy-pro-uninstall.ps1'"
```

배포된 integration을 먼저 retract하고(`<AGY_PRO_HOME>/backups/<ts>_retract/`로 옮김), 사용자 `PATH`에서 `~/.agy-pro/bin` 항목만 지우고, `~/.agy-pro`를 **휴지통**으로 옮깁니다. `-KeepState`는 `providers.json`과 백업을 남기고, `-DryRun`은 계획만 출력하며, `-RestorePath`는 설치 전에 저장한 `PATH`를 되돌립니다.

statusline 슬롯만 되돌리려면 `agy pro integrations retract --yes`가 `statusLine` 항목을 제거하고 덮어쓴 바이트를 보관합니다.

### 뭔가 이상해 보이면

| 증상 | 해결 |
|---|---|
| `agy`가 인식되지 않음 | Antigravity CLI를 설치한 뒤 **새** 터미널 창을 엽니다(새 창이 `PATH`를 다시 읽습니다) |
| 설치 중 `plugin.json not found` | 클론이 다른 브랜치에 있습니다. 위 원큐 한 줄을 다시 실행하세요. 클론 안에서 브랜치를 체크아웃합니다 |
| PowerShell이 스크립트 실행이 비활성이라고 함 | 여기 적힌 그대로 `powershell -ExecutionPolicy Bypass -File scripts\...ps1`로 실행하세요 |
| 설치 직후 끊이지 않는 `TLS handshake` 출력 | 최신 `main`으로 갱신하세요(probe 쿨다운 가드가 있습니다). 또는 `agy pro integrations retract --yes`를 실행하세요 |
| 설정이 꼬임 | `agy pro doctor`, 그다음 `agy pro status`, 그다음 `agy pro integrations repair` |

---

<a id="branches"></a>

## 🧵 나머지 브랜치

추가 기능은 각자 자기 브랜치에 있으므로, `git clone -b <branch>`가 클론하는 동안 그 브랜치로 체크아웃합니다. 빠뜨릴 수 있는 수동 `git checkout` 단계가 남지 않습니다.

| 브랜치 | 추가하는 것 | 원큐 설치 (Windows PowerShell) |
|---|---|---|
| [`main`](https://github.com/myk1yt/agy-pro/tree/main) | 전부: 분석, dashboard, shim, 스웜, 코딩 도어 | `cmd /c "git clone https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat && install.cmd"` |
| [`Agent/designer`](https://github.com/myk1yt/agy-pro/tree/Agent/designer) | `@designer`: MCP 없는 디자인 전문가(SVG, 3D 캔버스, 사이버펑크, 샌드박스, QA 하네스) | `cmd /c "git clone -b Agent/designer https://github.com/myk1yt/agy-pro.git && cd agy-pro && powershell -ExecutionPolicy Bypass -File scripts\install-designer.ps1"` |
| [`Agent/security-reviewer`](https://github.com/myk1yt/agy-pro/tree/Agent/security-reviewer) | `@security-reviewer`: 오케스트레이터 + 도메인 인스펙터 4개(OWASP, IAM, 자격 증명, 공급망) | `cmd /c "git clone -b Agent/security-reviewer https://github.com/myk1yt/agy-pro.git && cd agy-pro && powershell -ExecutionPolicy Bypass -File scripts\install-security-reviewer.ps1"` |
| [`Agent/code-reviewer`](https://github.com/myk1yt/agy-pro/tree/Agent/code-reviewer) | `@code-reviewer`: 병합 전 diff 리뷰, 8개 카테고리 택사노미, P0-P3 게이트 | `cmd /c "git clone -b Agent/code-reviewer https://github.com/myk1yt/agy-pro.git && cd agy-pro && powershell -ExecutionPolicy Bypass -File scripts\install-code-reviewer.ps1"` |
| [`gemini-config`](https://github.com/myk1yt/agy-pro/tree/gemini-config) | 거버넌스 규칙과 7단계 멀티 에이전트 라이프사이클, `~/.gemini`에 배포 | `cmd /c "git clone -b gemini-config https://github.com/myk1yt/agy-pro.git && cd agy-pro && scripts\install.bat"` |
| [`feature/agy-pro`](https://github.com/myk1yt/agy-pro/tree/feature/agy-pro) | 원라이너가 설치해 오는 Go shim 브랜치 | `cmd /c "git clone -b feature/agy-pro https://github.com/myk1yt/agy-pro.git && cd agy-pro && install.cmd"` |

macOS와 Linux에서는 `bash scripts/install.sh`가 Node 쪽을 담당합니다. Go shim은 `go build`로 직접 빌드합니다(자세한 내용은 [한국어 설명서](MANUAL.ko.md)).

---

<a id="under-the-hood"></a>

## 🔧 내부 구조

**shim 하나, 추가 프로세스 하나, 패치된 바이너리 없음.**

```text
agy                  ->  실제 agy 바이너리, 이전과 완전히 동일
agy agents           ->  그대로 전달
agy -m "..."         ->  그대로 전달
agy pro <verb>       ->  agy-pro 자체 엔진
```

`agy pro`는 자기 동사 공간만 가로챕니다. shim이 정품 바이너리를 다시 구현하지 않고 exec하기 때문에 네이티브 TUI, stdio 핸들, 종료 코드는 그대로입니다. `AGY_LAUNCHER=1`은 인자 없는 `agy`에 2초 모델 픽커를 더할 뿐, 다른 것은 바뀌지 않습니다.

**양쪽 모두 의존성 0.**

- Go: `module agypro`, `go 1.22`, 표준 라이브러리만. `agy.exe` 바이너리 하나.
- Node: 표준 라이브러리만(`http`, `fs`, `path`, `net`, `child_process`), Node 16+. statusline 조회는 캐시 적중이고 다시 파싱하지 않습니다.

**MCP 없음, 상주 daemon 없음.** 오케스트레이션 경로 전체가 자식 프로세스를 띄우고 파일을 읽고 씁니다. dashboard를 열기 전까지 어떤 포트도 열리지 않고, 터미널을 닫으면 도는 것이 없습니다.

**테스트 기준선.** `go test ./...`가 Go 패키지 28개를 덮고, `npm test`가 JavaScript 테스트 296개를 실행합니다(실패 0). 이 두 명령이 모든 변경이 통과해야 하는 게이트입니다.

**산출물은 그냥 읽을 수 있는 파일입니다.**

```text
reports/fanout/<ts>/   summary.json, reconciliation.md, progress.log, seat-*.md
reports/code/<ts>/     phases.log, report.txt, plan/, write/, review/,
                       verify/attempt-*.log, manifest.json, snapshot/, rollback-archive/
~/.agy-pro/            providers.json, state.json, backups/, ledger-keys/
```

리포트나 콘솔로 흘러든 키 모양 값은 앞 5글자에 `****`를 붙여 표시하고, 뒤쪽 일부를 노출하는 일은 없습니다.

---

<a id="safety"></a>

## 🔒 안전과 동의 모델

![코딩 도어 write 실행을 둘러싼 다섯 안전 계층](docs/assets/security-layers.svg)

규칙 세 개로 거의 전부를 덮습니다:

1. **동의는 계획 하나당 한 번, 파일당이 아닙니다.** `agy pro integrations apply tools`가 계획 하나(`statusLine` 슬롯, 내용 트리, 의도 행)를 만들어 정확한 diff를 출력하고 한 번만 묻습니다. `status`는 읽기 전용이고 절대 묻지 않습니다. 비대화형 stdin은 `--yes`를 붙여야 하며, 없으면 그 동사는 거부하고 아무것도 쓰지 않습니다.
2. **유료라는 말은 echo와 승인을 뜻합니다.** 좌석 목록의 프로바이더 좌석, 그리고 모든 write 실행은 토큰·비용 상한이 없는 과금 API 호출입니다. echo는 좌석마다 마스킹된 이름을 적고 상한이 없다고 밝히며, `y/N`이나 `--yes` 없이는 아무것도 실행되지 않습니다. 네이티브만 쓰는 phase는 추가 비용이 없으므로 묻지 않습니다.
3. **비밀은 산출물에 들어가지 않습니다.** registry는 환경 변수 이름만 선언하고 값은 절대 담지 않습니다. echo는 모든 인자를 마스킹하고, 요청 텍스트는 화면에 찍지 않고 `ask.txt`로 저장하며, 로그는 redactor를 통과합니다.

`agy pro mode full|clean`과 `agy pro integrations preset`은 **무엇이 배포되는지**만 바꾸고, 어떤 바이너리가 도는지는 바꾸지 않습니다. 이 분리 덕분에 배포가 깨져도 `retract`나 `rollback` 한 번이면 깨끗해지고, `agy pro doctor`가 툴체인과 경로와 키를 한 화면에 보고할 수 있습니다.

---

<a id="docs"></a>

## 📚 심화 문서

- 📖 **[Gemini quota 풀](docs/QUOTA_POOL.md)**: RPC 탐색, 5h/7d 슬라이딩 윈도우, 원자적 캐싱, 1ms 미만 statusline 조회.
- 📊 **[SSE dashboard](docs/DASHBOARD.md)**: 이중 전송(SSE 푸시 + 스크립트 폴링), SVG 차트 엔진, VS Code 터미널 연동, 루프백 보안.
- 🛠 **[`agy pro code` 계약](agy-pro/docs/code.md)**: 명령 표면, 종료 코드, 이름 붙은 거부 사유가 있는 안전 모델, 그리고 알려진 한계(이 도어가 풀지 않은 것 포함).
- 🌊 **[fanout 엔진](agy-pro/docs/fanout.md)**: 좌석 문법, reconciliation, 파티션 모드(`--units`), 도구 sandbox.
- 🔀 **[하이브리드 좌석](agy-pro/docs/swarm-hybrid.md)**: 한 실행에 네이티브 좌석과 프로바이더 좌석, failover 동작.
- 🌉 **[브리지 아키텍처](agy-pro/docs/bridge-architecture.md)**: 외부 모델이 네이티브 `agy` 세션에 닿는 방법.
- 📘 **[한국어 설명서](MANUAL.ko.md)**: 두 설치 프로그램, 검증, 설정, 문제 해결, 설치 제거까지 그림으로 설명한 안내서.
- 🧰 **[agy-pro README](agy-pro/README.md)**: shim 자체의 레퍼런스.

---

<a id="license"></a>

## 📄 라이선스

MIT입니다. [`LICENSE`](LICENSE)를 참고하세요.

개발: **kim,yong-tai**.
