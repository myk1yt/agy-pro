---
name: design-data-viz
description: >
  D3.js v7 topology and data visualization skill combined with Cyberpunk observatory UI theme, laser sweep animations, and SVG neural pulse DAG networks.
---

# Design Data Viz (D3 위상 시각화 + 사이버펑크 관측소)

데이터를 "차트로 보이게" 만드는 것은 누구나 한다. 본 스킬의 목표는
**관측소(observation post)에 앉아 시스템을 감시하는 느낌**을 주는
위상 시각화다. 관계·계층·흐름이 구조로 읽히고, 테마가 장식으로 끝나는
것이 아니라 정보 위계(선택/비선택, 활성/대기)를 강화하도록 설계한다.

> 전제: 산출물(HTML/SVG)은 `design-core-harness` 시각 검증 루프 대상이다.
> D3 코드가 논리적으로 옳아도 실제 화면에서 라벨이 겹치면 미완성이다.

---

## 1. 도구 선택 사다리 (D3 vs 무의존)

REQ-011(추가 MCP 설치 불요)과 오프라인 이식성 사이에서 다음 순서로 택한다.

```text
① D3.js v7 (npm 설치 후 번들 or 로컬 복사)     ← 라이브러리 허용 환경
② D3.js v7 CDN (https://cdn.jsdelivr.net/npm/d3@7) ← 인터넷 있는 1회성 산출물
③ 순수 SVG + 네이티브 JS (d3 없이)             ← 단일 파일 무의존 규격
   (design-interactive-sandbox와 동일 제약: 외부 참조 0)
④ Zero-Dependency DOM/CSS 브레인맵 (§5)        ← 실시간 펄스까지 CSS만으로
```

- 위상/DAG·수백 노드·확축(zoom/pan)이 필요하면 ①/②.
- sci-widget 규격(단일 파일 무의존)을 요구받으면 ③: 레이아웃 수식은
  §3과 동일하고 렌더만 직접 SVG 문자열 생성.
- ②는 파일은 단일 HTML이지만 오프라인에서 깨진다. 이식성 요구가
  있으면 ②를 택하지 말고 ③으로 간다.

### 1.1 D3 v7 사용 규칙

- v7 고정 (`import * as d3 from 'd3'` 또는 `<script src="...d3@7...">`).
  v6와 v7의 `d3.event` 제거 차원에 주의, v7은 핸들러 첫 인자가 event다.
- 최소 모듈만 필요해도 전체 d3 번들로 충분(위젯 규모). tree, link,
  selection, transition 4개 API가 주력.

---

## 2. 위상/DAG 렌더링 규격 (방사형 계통수)

Git 브랜치 계통·조직도·의존 트리 같은 **계층+분기** 구조의 기본 레이아웃은
방사형 계통수(radial dendrogram)다. 시간축 또는 중심에서 바깥쪽 방향이
분기 서사를 가장 잘 보여준다.

### 2.1 레이아웃 수식

```javascript
// 계층 데이터 → 방사형 좌표
const root = d3.hierarchy(data);
const radius = Math.min(width, height) / 2 - 60; // 외곽 라벨 여백 60px
d3.tree().size([2 * Math.PI, radius])(root);

// (x=각도, y=반지름) → 직교 좌표
const pos = d => [d.y * Math.cos(d.x - Math.PI / 2),
                  d.y * Math.sin(d.x - Math.PI / 2)];
```

- 각도 시작을 `-π/2`(12시 방향)에 고정하면 루트가 위를 가리켜 읽기 순서가
  시계와 일치한다.
- 동급 노드 각도 균등 배분은 `d3.tree()`가 보장. 손으로 간격을 흔들지 않는다.
- 리프가 밀집한 방향에 라벨 충돌이 집중된다 → §2.3 라벨 규칙 필수.

### 2.2 링크(가지) 곡선

```javascript
// 방사형 3차 베지어: 부모→자식의 유기적 곡선
function radialLink(s, t) {
  const [x1, y1] = pos(s), [x2, y2] = pos(t);
  const mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
  const k = 0.62; // 중심 쪽 제어점 수축 계수, 곡선 "가지" 느낌
  return `M${x1},${y1} Q${mx * k},${my * k} ${x2},${y2}`;
}
```

- 직선 링크는 계통수에서 별 의미가 없다. 곡선 계수 `k`는 0.5~0.7 사이에서
  분기 밀도에 맞춰 한 번 정하고 **도식 전체에서 고정**한다.
- 링크 굵기는 깊이(depth)에 반비례(간선 2px → 말단 1px)해 위계를 만든다.

### 2.3 호버 인포카드와 딤 처리 (핵심 인터랙션 계약)

원전 규격 그대로: **180ms 관성 인포카드 + 비선택 82% 딤(opacity .18)**.

```javascript
// 호버 진입: 선택 경로만 남기고 나머지 전체 딤
node.on('pointerenter', (event, d) => {
  gRoot.classed('dimmed-all', true);
  d.ancestors().concat(d.leaves()).forEach(a => {
    a.descendants().forEach(n => selectPath(n).classed('lit', true));
  });
  card.style('opacity', 1).style('transform',
    `translate(${d.x}px,${d.y}px)`); // 관성 transition은 CSS에서
});
```

```css
/* 인포카드: 180ms ease-out, 즉각 표시도, 늘어지는 것도 아닌 '관성' */
.info-card {
  position: absolute;
  pointer-events: none;
  opacity: 0;
  transition: opacity 180ms cubic-bezier(0.22, 1, 0.36, 1),
              transform 180ms cubic-bezier(0.22, 1, 0.36, 1);
}
/* 비선택 브랜치 딤: 82% 감쇠 → opacity .18 */
.dimmed { opacity: 0.18; transition: opacity 240ms ease; }
```

규칙:
1. **180ms는 하한이 아니다 고정값이다.** 인포카드 표시/이동은 이 값.
   400ms 이상은 "느리다", 0ms는 "깜빡인다".
2. 딤 대상은 **호버한 노드의 조상 경로+자손** 외 전부. 인접한 형제
   브랜치까지 남겨두면 포커스가 흐려진다.
3. 딤은 opacity로만. 색을 바꾸면(예: 회색화) 복귀 시 색상 보정까지
   필요해져 상태가 복잡해진다.
4. `pointerenter`/`pointerleave` 대칭: leave 시 `.dimmed-all`, `.lit`을
   전부 클래스 제거로 원복. 트랜지션은 역방향도 240ms로 매끄럽게.

### 2.4 위계 가독성 예산

- 1 화면 리프 노드 ≤ **60개**. 초과 시 서브트리 접기(collapsible) 또는
  단계 분리. 60개 이상을 한 번에 보이는 것은 시각화가 아니라 벽이다.
- 리프 라벨: 각도에 따라 우측/좌측 정렬(`angle < π ? 'start' : 'end'`)
  + 회전(`rotate(±90)`)으로 거꾸로 읽히는 라벨 0건.
- 리프 간 호 각도 < 2°가 발생하면(밀집) 라벨 생략 + 호버로 보완.

---

## 3. 사이버펑크 관측소 테마 언어

테마는 "어두우면 사이버펑크"가 아니다. **관측 장비(CRT·레이저·스캔라인)의
물성**이 데이터 위계를 보조할 때 성립한다. 배경이 정보를 삼키면 실패다
(design-core-harness §3.4 대비 결함).

### 3.1 딥 블루-블랙 radial 배경

```css
:root {
  --bg-deep:   #0b2340;  /* 관측소 심해 블루 */
  --bg-black:  #01030a;  /* 하단 수직 심도 */
  --grid-line: rgba(120, 200, 255, 0.10);
  --text-hi:   #d8ecff;  /* 본문, 배경 대비 ≥ 10:1 */
  --text-lo:   #7da3c4;  /* 보조 라벨, 4.5:1 만족선 */
  --accent:    #ffb454;  /* amber, 선택/활성 강조 */
  --danger:    #ff5d5d;  /* 경보 브랜치 */
}
body {
  margin: 0;
  background: radial-gradient(1200px at 44% 96%,
              var(--bg-deep) 0%, var(--bg-black) 100%);
  color: var(--text-hi);
  min-height: 100vh;
}
```

- 중심 좌표 `44% 96%`는 하단-좌우 어긋남으로, 대칭 히어로보다
  오프그리드(VARIANCE 감각)다. 그대로 쓰되 화면 비례에 맞춰 미세 조정한다.
- 노드 채움은 반투명 글래스(rgba) + accent 보더 1px. 불투명한 단색 원판이
  배경 그라디언트를 죽인다.

### 3.2 CRT 스캔라인 (repeating-linear-gradient)

```css
/* 화면 전체에 걸치는 홀로그램 CRT 결. pointer-events 반드시 none */
.crt::after {
  content: '';
  position: fixed;
  inset: 0;
  z-index: 50;
  pointer-events: none;
  background: repeating-linear-gradient(
    to bottom,
    rgba(180, 220, 255, 0.045) 0px,
    rgba(180, 220, 255, 0.045) 1px,
    transparent 1px,
    transparent 3px
  );
  mix-blend-mode: overlay;
}
```

- 줄 간격은 3px 주기, 알파는 **0.05 미만**. 그 이상은 텍스트를 갉아먹어
  §6 대비 검사를 통과하지 못한다. 스캔라인은 "느껴져야지 읽혀선 안 된다".
- `inset:0` + `position:fixed` + `z-index:50`: 어떤 스크롤에서도 결이
  고정되어야 "화면 위의 유리" 물성이 산다. `pointer-events:none` 없으면
  모든 인터랙션이 죽는다, 4대 결함(겹침) 유발 패턴.

### 3.3 레이저 스윕 (7.5초 keyframes)

```css
/* 관측 레이저가 7.5초 주기로 화면을 훑는 단일 빛줄기 */
@keyframes sweep {
  from { left: -12%; }
  to   { left: 108%; }
}
.laser {
  position: fixed;
  top: 0;
  width: 10%;
  height: 100vh;
  z-index: 49;
  pointer-events: none;
  background: linear-gradient(to right,
    transparent, rgba(140, 210, 255, 0.10) 45%,
    rgba(180, 230, 255, 0.16) 50%, rgba(140, 210, 255, 0.10) 55%,
    transparent);
  filter: blur(6px);
  animation: sweep 7.5s cubic-bezier(0.45, 0.05, 0.55, 0.95) infinite;
}
```

- 주기 **7.5초**: 사용자의 시선이 한 번 화면에 머무는 시간과 비슷해,
  스윕이 "우연히" 겹친다. 2초 이하는 번쩍임(접근성 위반), 20초는 정지.
- 이징은 ease-in-out 계열로 끝에서 감속, 선형은 기계식 와이퍼처럼
  보인다. `prefers-reduced-motion: reduce` 시 `animation: none` 필수(§6).
- 빛줄기 투명도 최대 알파 **0.16**. 이 위로 배경 그라디언트를 하얗게
  씻어낸다.

### 3.4 테마 사용 원칙

1. accent amber(--accent)는 **선택 상태 전용**: 기본 대기 상태에 쓰면
   강조가 소진된다.
2. danger red는 경보 의미와 무관한 장식으로 쓰지 않는다.
3. 그라디언트 배경 위 모든 텍스트는 §6 대비를 통과해야 한다. 테마는
   대비를 깎는 방향으로 양보하지 않는다, 텍스트가 우선이다.

---

## 4. Zero-Dependency 브레인맵 (순수 DOM/CSS 펄스)

라이브러리 없이도 "신경망이 살아있다"는 감각을 준다. 노드는 DOM 요소,
활성화는 CSS 트랜지션, 파동은 `animation-delay`의 시간차다.

### 4.1 구조

```html
<div class="bm-stage" style="--n:1">
  <svg class="bm-links"><!-- 링크: 정적 SVG path + flowing dash --></svg>
  <span class="bm-node" style="left:44%;top:96%;--d:0ms"></span>
  <span class="bm-node" style="left:30%;top:60%;--d:140ms"></span>
  <span class="bm-node" style="left:62%;top:48%;--d:280ms"></span>
  <!-- ... 좌표는 계층 깊이에 따른 사전 배치 -->
</div>
```

### 4.2 펄스 기법 (delay 변수로 파동 전파)

```css
.bm-node {
  position: absolute;
  width: 10px; height: 10px;
  margin: -5px;
  border-radius: 50%;
  background: rgba(180, 220, 255, 0.85);
  box-shadow: 0 0 0 0 rgba(140, 200, 255, 0.5);
  animation: pulse 2.6s ease-out var(--d) infinite;
}
@keyframes pulse {
  0%   { box-shadow: 0 0 0 0 rgba(140,200,255,.5); transform: scale(1); }
  70%  { box-shadow: 0 0 0 18px rgba(140,200,255,0); transform: scale(1.25); }
  100% { box-shadow: 0 0 0 0 rgba(140,200,255,0); transform: scale(1); }
}
/* 링크 흐름: 점선 offset 애니메이션으로 신호 전달 방향 표시 */
.bm-links path {
  stroke: var(--grid-line); stroke-width: 1; fill: none;
  stroke-dasharray: 4 6;
  animation: flow 1.8s linear infinite;
}
@keyframes flow { to { stroke-dashoffset: -20; } }
```

규칙:
1. **`--d`(delay)가 파동이다.** 루트→말단 순으로 120~160ms씩 누적 지연을
   주면 활성화가 시간축으로 퍼져 "델타 펄스"가 된다. JS 타이머 불요.
2. 노드 좌표는 `left/top %`로 무대 대비 사전 고정. 리사이즈 대응은
   `aspect-ratio: 16/9` 컨테이너로 단순화한다.
3. 실시간 상태 갱신이 필요하면(이벤트 발생 시각화) `classList.add('fire')`
   후 `animationend`로 제거, 애니메이션 재생성 리셋 패턴.
4. 무대 전체 노드는 40개 이하. DOM 애니메이션 요소가 많아지면 합성
   레이어 폭주로 FPS가 죽는다 (design-interactive-sandbox §6.1 준용).

---

## 5. 접근성 대비 규칙 (어두운 테마 전용 절)

dark theme은 대비 실패의 온상이다. 관측소 테마에서 아래를 강제한다.

1. **본문 텍스트 ≥ 7:1**, `#d8ecff` on `#01030a`는 AAA급 출발점.
   배경 그라디언트의 **가장 밝은 지점(#0b2340) 기준**으로 비율을 계산한다.
   가장 어두운 곳이 아니라.
2. **보조 라벨 ≥ 4.5:1**, `#7da3c4` 기준 최소값. 채도 낮은 회색
   (`#6b7280`류)를 dark 배경에 올리면 탁해져서 실패한다. blue-tinted
   그레이를 쓴다.
3. **딤(opacity .18)은 비정보 상태 전용**, 딤된 텍스트를 읽혀야 하는
   상태(예: 범례)에 두지 않는다.
4. **색 차이만으로 위계 표현 금지**, 선택/비선택은 opacity+굵기 중 하나로
   더 구별 가능하게 (색맹 사용자 대응).
5. `prefers-reduced-motion: reduce` 시 스윕·펄스·플로우 전부 정지:

   ```css
   @media (prefers-reduced-motion: reduce) {
     .laser, .bm-node, .bm-links path { animation: none; }
     .info-card { transition-duration: 0.01ms; }
   }
   ```

6. 스크린 리더: SVG 도식에 `role="img"` + `aria-label`(요약), 인포카드
   영역은 `aria-live="polite"`로 선택 노드 정보를 텍스트 환류.

---

## 6. 시각 검증 루프 (design-core-harness 연동)

```text
[HTML 산출물 생성] ──▶ node scripts/visual_qa.js <html> [out]
        ▲                       │
        │               Vision 대조: §6.1 체크리스트
        │                       │(결함 기록)
        └── [수정(1결함 단위)] ◀┘
```

### 6.1 도식 전용 추가 체크리스트 (core-harness 4대 결함 위에)

- [ ] 리프 라벨끼리 겹침 0건 (밀집 방향 확대 확인)
- [ ] 링크가 노드 라벨 본문을 관통하지 않음
- [ ] 스캔라인/레이저 오버레이 하에서도 텍스트 대비 §5.1-2 만족
      (캡처 스크린샷에서 라벨 가독성 실측)
- [ ] 호버 인포카드가 화면 밖으로 벗어나지 않음 (우상단/좌상단 노드 검증)
- [ ] 딤 상태와 기본 상태의 스크린샷 각각 캡처, .18과 1.0의 위계가
      실제로 구분되는지 확인
- [ ] 7.5초 스윕 프레임 중 라벨을 가리는 순간 캡처가 없는지 확인
      (스윕 위치를 0%/50%/100% 3프레임으로 검증)

도구 폴백은 core-harness §7 사다리(visual_qa.js → view_file →
명세 강등+UNVERIFIED 배지)를 그대로 따른다.

---

## 7. 완료 선언 최종 체크리스트

- [ ] 도구 사다리 단계(①~④)가 산출물에 일치 (무의존 규격인데 CDN 스크립트 없음 확인)
- [ ] 방사형 수식 기준값 유지: -π/2 시작, k=0.5~0.7 고정, 리프 ≤ 60
- [ ] 인포카드 180ms / 딤 .18 고정값 준수, pointerenter/leave 대칭
- [ ] 테마 3종(배경 radial, CRT 알파<0.05, 스윕 7.5s·알파≤0.16) 실측값 일치
- [ ] reduced-motion 대응, 대비 7:1/4.5:1 충족 (§5)
- [ ] Visual QA Loop 통과 + 검증 수단 기록 (§6)

> 검증 수단을 기록하지 않은 완료 선언은 거짓 완료로 간주한다.

---

## 8. 관련 스킬 및 레퍼런스

- [`design-core-harness`](../design-core-harness/SKILL.md), 시각 검증
  루프·4대 결함·폴백 사다리 (본 스킬 §6의 상위 하네스)
- [`design-interactive-sandbox`](../design-interactive-sandbox/SKILL.md), 단일 파일 무의존 제약과 렌더 루프 규격(§4와 공유)
- [`design-vector-svg`](../design-vector-svg/SKILL.md), 정적 도식 요청일
  때의 대체 경로
- [`frontend-design`](../frontend-design/SKILL.md), 180ms/240ms
  트랜지션·이징 근거와 VARIANCE/MOTION/DENSITY 다이얼 근거
  (모션·방향 규칙은 frontend-design 하위)

## 6. Topology Templates
See `templates/cyberpunk-topology.html` for deep observatory radial gradients and laser sweep animation setups.

