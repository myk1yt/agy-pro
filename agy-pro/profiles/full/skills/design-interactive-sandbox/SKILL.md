---
name: design-interactive-sandbox
description: >
  Interactive single-file DOM widget sandbox protocol with isolated styling, scoped state machines, and headless visual QA verification.
---

# Design Interactive Sandbox (sci-widget 라이브 HTML 위젯)

"인터랙티브한 설명"은 산출물이 아니다. 사용자가 **시연·시뮬레이션·조작**을
요구했는데 정적 텍스트나 정적 스크린샷으로 답하는 것은 이 스킬의 철학상
**미완성 응답**이다. 답은 대화 안에서 움직이는 살아있는 UI 샌드박스다.

> 원전(MiMo `sci-widget` 공식 명세): *"An interactive EXPLANATION is not
> a deliverable: when the user asks you to demonstrate, simulate, or
> interactively visualize something ('interactive demo', 'let me drag a
> slider and watch it change'), answer with one self-contained HTML
> document rendered as a live, interactive widget."*
>
> Antigravity CLI / agy-pro에는 MiMo Desktop의 `sci-widget` fenced 블록 렌더러가 없다.
> 따라서 본 스킬은 **동일 규격의 단일 파일 HTML을 산출물로 생성**하고,
> 브라우저에 띄워 design-core-harness 시각 검증 루프를 통과시키는 방식으로
> 이식한다 (§5).

---

## 1. 트리거 조건 (언제 sci-widget으로 답하는가)

아래 요청 패턴이 감지되면 정적 설명 대신 위젯 생성을 기본 응답으로 택한다.

| 사용자 발화 패턴 | 위젯 응답 형태 |
|------------------|----------------|
| "인터랙티브 데모 보여줘" | 조작 가능한 데모 위젯 |
| "슬라이더 조작해서 변화 보고 싶어" | 컨트롤 패널 + 실시간 리렌더 |
| "시뮬레이션 보여줘" / "물리 엔진 동작 확인" | 캔버스 애니메이션 + 파라미터 |
| "이 알고리즘이 어떻게 동작하는지 직접 돌려보고 싶어요" | 단계 재생/일부 조작 위젯 |
| "A와 B를 즉각 비교할 수 있게" | 토글·병열 뷰 위젯 |

> 발화가 모호하면 "살아있게 조작 가능한 산출물"로 해석한다. 사용자가
> 슬라이더라는 단어를 썼다면 슬라이더가 실제로 움직여야 한다.
> 반대로 단순 수치 표·STATIC 다이어그램 요청에 위젯을 강제하지 않는다.
> (벡터 도식은 [`design-vector-svg`](../design-vector-svg/SKILL.md),
> 데이터 대시보드는 [`design-data-viz`](../design-data-viz/SKILL.md)로 보낸다)

---

## 2. 규격: 외부 의존성 0 단일 파일 HTML

산출물은 **파일 1개**여야 한다. 복사해서 어디에(오프라인, 다른 PC,
이메일 첨부) 던져도 동일하게 동작해야 한다.

### 2.1 금지 항목

- ❌ CDN 스크립트·폰트 로드 (`<script src="https://...">`, `@import url(...)`)
- ❌ npm/CDN 라이브러리 (D3, GSAP, Three.js 등), 필요하면 §5.3 순수 구현으로 대체
- ❌ 외부 이미지·폰트 파일 참조 (`<img src="a.png">`), 인라인 SVG/data URI만 허용
- ❌ 서버 요구 (fetch, WebSocket, API 호출), 완전 클라이언트 사이드
- ❌ 빌드 과정 (JSX/TS 직접 실행 불가)

### 2.2 필수 구성

```html
<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>[위젯 제목]</title>
  <style>/* 모든 CSS 인라인 */</style>
</head>
<body>
  <!-- §3의 3단 구조 -->
  <script>/* 모든 JS 인라인 */</script>
</body>
</html>
```

- `<script>`/`<style>`는 반드시 인라인. 파일 참조는 0건.
- 문서 최상단 주석으로 위젯 목적과 조작 요약을 3줄 내 명시한다
  (`<!-- sci-widget: [이름], [목적], [조작 방법] -->`).
- 인라인 SVG/data URI는 허용되나 용량상 남용 금지(§6.3).

---

## 3. 구조 템플릿: 컨트롤 패널 + 캔버스 + 상태 표시

모든 sci-widget은 아래 3단 구조를 따른다. 순서는 좌(컨트롤) → 우(캔버스)
또는 상(컨트롤) → 하(캔버스)이며 상태 표시가 항상 현재 값을 숫자로 환류한다.

```text
┌──────────────────────────────────────────────┐
│  ① 컨트롤 패널          │  ② 캔버스(시각화 영역)    │
│  - 슬라이더 (range)        │  - 실시간 리렌더링            │
│  - 토글 버튼               │  - 애니메이션 루프              │
│  - 리셋                    │                                 │
│  ③ 상태 표시: 파라미터 현재값 / 측정값 (숫자, aria-live)      │
└──────────────────────────────────────────────┘
```

### 3.1 최소 골격 코드

```html
<div class="widget">
  <section class="panel" aria-label="컨트롤">
    <label for="p-speed">속도</label>
    <input type="range" id="p-speed" min="0.1" max="3" step="0.1" value="1">
    <button id="t-pause" aria-pressed="false">일시정지</button>
    <button id="b-reset">리셋</button>
  </section>
  <section class="stage" aria-label="시뮬레이션">
    <canvas id="cv" width="640" height="400"></canvas>
  </section>
  <output class="status" id="stat" aria-live="polite">
    속도 1.0배 · FPS 60 · 누적시간 0.0s
  </output>
</div>
```

```javascript
// ① 상태 단일 소유: 모든 파라미터를 한 객체에서
const S = { speed: 1, paused: false, t: 0 };

// ② 컨트롤 바인딩: input → 상태 갱신 → 즉시 반영
const $ = (id) => document.getElementById(id);
$('p-speed').addEventListener('input', e => { S.speed = +e.target.value; });
$('t-pause').addEventListener('click', e => {
  S.paused = !S.paused;
  e.target.setAttribute('aria-pressed', String(S.paused));
});
$('b-reset').addEventListener('click', () => {
  S.t = 0; $('p-speed').value = 1; S.speed = 1;
});

// ③ 렌더 루프: requestAnimationFrame 고정 (setTimeout 금지)
const cv = $('cv'), ctx = cv.getContext('2d');
let last = performance.now(), fps = 60;
function loop(now) {
  const dt = Math.min((now - last) / 1000, 0.05); last = now;   // 탭 비활성 가드
  if (!S.paused) S.t += dt * S.speed;
  fps = fps * 0.9 + (1 / Math.max(dt, 1e-4)) * 0.1;             // 지수이동평균
  render(ctx, S);
  $('stat').textContent =
    `속도 ${S.speed.toFixed(1)}배 · FPS ${Math.round(fps)} · 누적시간 ${S.t.toFixed(1)}s`;
  requestAnimationFrame(loop);
}
requestAnimationFrame(loop);
```

`render()`는 순수 함수로 분리한다(상태 S만 입력). 디버깅과 검증이 쉬워진다.

### 3.2 컨트롤 규칙

1. **모든 입력은 즉시 반영**: `input` 이벤트 사용(`change` 아님). 슬라이더
   조작과 화면 변화 사이에 딜레이·확인 버튼이 있으면 안 된다.
2. **리셋 버튼 필수**: 조작으로 상태를 망가뜨려도 원점 복귀가 가능해야 한다.
3. **값의 단위를 라벨에 쓴다**: `"속도 (배)"`, `"각도 (°)"`, 순수 숫자만
   놓으면 어떤 단위인지 모르는 위젯이 된다.
4. ** 범위 상한은 물리적으로 유의미하게**: `min/max`가 없는 슬라이더,
   음수 질량 같은 비물리 조합을 허용하지 않는다.

---

## 4. 시뮬레이션 내용 설계 규약

위젯은 "예쁜 움직임"이 아니라 **조작 → 인과 → 학습**의 사이클을 제공해야 한다.

1. **주입 변수는 1~4개로 제한.** 5개 이상이면 조작자 인지 한계를 넘는다.
   복잡할 것 같으면 위젯을 두 개로 쪼갠다.
2. **관측 출력과 연결.** 슬라이더를 움직이면 상태 표시(숫자)와 캔버스
   (시각)가 **동시에** 반응해야 한다. 둘 중 하나만 바뀌면 고장이다.
3. **물리·수치 모델은 소스 표기.** 캔버스 하단이나 상태 라인에 모델 식을
   한 줄로 남긴다 (`v = v₀·e^(-kt)` 류). 사용자가 검증 가능한 시뮬레이션이
   목표다.
4. **정지 상태에서도 읽혀야 한다.** 일시정지 화면은 정적 다이어그램으로서
  의미가 있어야 한다(축·라벨·범례 유지).

---

## 5. Antigravity CLI / agy-pro 환경 적용: 생성 → 브라우저 → 시각 검증

MiMo는 위젯을 대화창에 직접 렌더링하지만, Antigravity CLI / agy-pro는 **파일 기반 루프**로
동일한 검증을 수행한다. `design-core-harness` §2의 Visual QA Loop를
그대로 사용한다.

```text
① 위젯 HTML 파일 생성 (./<slug>.html) ──▶ ② 브라우저 오픈
        │                                        │
        ▼                                        ▼
④ 수정(Fix) ◀── ③ Vision 대조 (visual_qa.js 캡처 / view_file 폴백)
```

### 5.1 단계별 절차

1. **파일 생성**: §2~§3 규격으로 단일 `.html` 작성. 산출 위치는 세션 작업
   폴더 또는 지정 출력 폴더 (지정 없으면 워커스페이스 최상단 `widgets/`).
2. **브라우저 오픈 + 캡처**: design-core-harness 1순위 도구 실행, `node scripts/visual_qa.js <html_path> [out_dir]`
   (Playwright 헤드리스 전체 페이지 스크린샷).
3. **시각 확인**: 스크린샷을 Vision으로 대조한다, 렌더링 초기 프레임
   기준으로 §3 구조(컨트롤/캔버스/상태)가 모두 보이는가, 4대 시각 결함
   (design-core-harness §3) 0건인가.
4. **인터랙션 검증**: 캡처 도구만으로는 조작을 못 본다. 최소 2가지 조작
   케이스를 **코드 경로 추적 또는 주입 테스트**로 확인한다:
   - 슬라이더 최댓값·최솟값에서 캔버스와 상태 라벨이 갱신되는가
   - 리셋 클릭 후 초기 상태로 돌아가는가
   Playwright 사용 가능 환경이면 스크립트에 조작 스니펫을 추가해 캡처 전
   값을 바꾸고 렌더를 재확인한다. 불가 환경이면 수동 조작 화면을
   스크린샷으로 저장해 view_file로 읽어 검증하고, 그 사실(조작 검증 방식)을 결과에 기록한다.
5. **수정 → 재렌더링**: 결함 발견 시 1결함 단위 수정 후 ③~④ 반복.

### 5.2 완료 판정 (core-harness §9 준용)

- [ ] 초기 렌더링 캡처에서 3단 구조 전부 확인됨
- [ ] 조작(슬라이더·토글·리셋) → 시각+상태 갱신 인과 확인됨
- [ ] 4대 시각 결함 0건, 슬롭 블랙리스트 0건
- [ ] 검증 수단 기록됨 (visual_qa.js / view_file / 명세 강등+배지)

### 5.3 라이브러리 유혹 차단

D3·GSAP를 쓰고 싶을 수 있으나 §2.1상 금지다. 대체:
- 애니메이션 → `requestAnimationFrame` + 수동 이징 (frontend-design
  §7 Q3 이징 표 참고: `cubic-bezier`는 CSS 전환에, JS에서는 easing 함수로 구현)
- 차트 → SVG/Canvas 직접 렌더. 위상 다이어그램이 목적이라면
  [`design-data-viz`](../design-data-viz/SKILL.md)의 Zero-Dependency 절 사용
- 물리 → dt 기반 명시적 적분 (§3.1 루프 구조). 엔진 아키텍처는 필요 없다.

---

## 6. 성능/접근성 가드레일

### 6.1 성능

1. `requestAnimationFrame` 고정. 60fps 목표가 불가능하면(수천 입자)
   입자 수 하한 옵션(밀도 슬라이더)을 제공해 사용자가 타협하게 한다.
2. 캔버스 리사이즈는 **쓰로틀링** (리사이즈 이벤트당 즉시 리렌더 금지).
3. 렌더 루프 안에서 DOM 조회(`getElementById`) 금지, §3.1처럼 변수에
   캐시한다.
4. `devicePixelRatio` 반영 시 상한 2로 클램프 (4K+ 과부하 방지):
   `const dpr = Math.min(window.devicePixelRatio || 1, 2);`
5. 탭 비활성 시 `dt`가 폭증하지 않도록 §3.1의 `Math.min(dt, 0.05)` 캡 유지.

### 6.2 접근성

1. 모든 입력에 `<label for>` 또는 `aria-label`. 토글은 `aria-pressed`.
2. 상태 표시는 `aria-live="polite"`로 스크린 리더 환류.
3. **`prefers-reduced-motion` 대응**: 감지 시 애니메이션 자동 재생을
   정지하고 초기 프레임 정적 뷰 + 재생 버튼으로 대체한다.

   ```css
   @media (prefers-reduced-motion: reduce) {
     .auto-motion { animation-play-state: paused; }
   }
   ```

4. 키보드 조작: 슬라이더는 `input[type=range]` 기본 지원, 버튼 조작은
   `tabindex` 순서 유지. 캔버스 클릭만 가능한 조작은 보조 컨트롤(버튼/슬라이더)을 병설한다.
5. 상태 라벨과 캔버스 색 대비는 design-core-harness §3.4 기준
   (일반 텍스트 4.5:1, 대형 3:1)을 충족한다.

### 6.3 용량/이식성

- 산출물 권장 상한 **80KB**. 초과 시 로직을 압축하거나 위젯을 분리한다.
- 파일 인코딩 UTF-8(BOM 금지), `lang="ko"` + `<meta charset="UTF-8">` 필수, 한글 라벨 깨짐은 §3.1 검증 단계에서 즉시 발견된다.

---

## 7. 안티-패턴 (반례)

| ❌ 안티패턴 | ✅ 대체 |
|------------|--------|
| GIF/정적 스크린샷으로 "시뮬레이션 보여줌" | 실제로 움직이는 위젯 파일 |
| 설명 + 코드 블록만 주고 "직접 실행해 보세요" | 위젯을 생성·검증해 즉시 조작 가능하게 전달 |
| 슬라이더는 있으나 화면이 안 바뀜 | input → 상태 → 렌더 파이프라인 (§3.1) |
| 애니메이션만 있고 수치 환류 없음 | 상태 표시에 조작 값+측정 값 병기 |
| `setInterval` 기반 루프 | `requestAnimationFrame` (§3.1) |

---

## 8. 완료 선언 최종 체크리스트

- [ ] 단일 파일, 외부 참조 0건 (grep으로 `src=`, `@import`, `fetch(` 확인)
- [ ] 3단 구조 + 리셋 + 즉시 반영 (§3)
- [ ] 조작 인과 검증 (최소 슬라이더 1종·토글 1종·리셋) (§5.1 ④)
- [ ] Visual QA Loop 통과 + 검증 수단 기록 (§5)
- [ ] 성능 가드레일 (§6.1) 및 reduced-motion 대응 (§6.2)
- [ ] 파일명/제목이 목적을 설명 (`damped-spring.html` O, `widget1.html` X)

> 검증 수단을 기록하지 않은 완료 선언, 그리고 조작 인과가 확인되지 않은
> 위젯은 완료가 아니다.

---

## 9. 관련 스킬 및 레퍼런스

- [`design-core-harness`](../design-core-harness/SKILL.md), 시각 검증
  루프·4대 결함·블랙리스트·폴백 사다리 (본 스킬 §5의 상위 하네스)
- [`design-vector-svg`](../design-vector-svg/SKILL.md), 조작이 필요 없는
  정적 다이어그램 요청일 때 사용
- [`design-data-viz`](../design-data-viz/SKILL.md), 라이브러리 기반
  대시보드/위상 시각화 요청일 때 사용
- [`frontend-design`](../frontend-design/SKILL.md), 위젯 내 모션
  이징·감속 곡선 근거
- [`frontend-design`](../frontend-design/SKILL.md), 위젯 스타일 방향 결정
  시 다이얼 선언

## 6. Widget Sandbox Template
See `templates/single-file-widget.html` for isolated DOM widget prototyping.

