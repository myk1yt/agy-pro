---
name: design-academic-typeset
description: >
  학술·수학·화학 출판 조판 스킬(도메인 9). mathjax-full v3을 Node에서 직접 로드해 LaTeX 수식을 출판급 벡터 SVG로 변환하는 tex2svg 실코드 파이프라인을 제공한다. mhchem 확장(\ce{})으로 화학 반응식·분자식을 정밀 조판하고, MathJax SVG의 currentColor 상속으로 다크/라이트 테마별 명도 대비를 자동 조정하며, speech-rule-engine(SRE)과 assistive MathML로 스크린리더 접근성(aria-label)을 부여한다. 텐서·행렬 조판 예산 규칙과, mathjax-full 미설치 시 KaTeX → 유니코드 텍스트 강등 + "시각 미검증" 배지 폴백 사다리를 따른다. 산출물은 design-core-harness 시각 검증 루프(잘림/겹침 검사)를 통과해야 완성이다.
---

# Design Academic Typeset (학술·수식·화학 조판)

수식을 "텍스트처럼" 다루는 순간 출판물은 죽는다. `x^2`를 문자 그대로
띄우는 것과 `\frac{x^2}{2}`를 분수선까지 계산해 박는 것의 차이는 독자가
문서의 **권위**를 느끼는가의 차이다. 본 스킬은 LaTeX 원문을 **출판급
벡터 SVG**로 조판하는 파이프라인과 그 품질 규칙을 소유한다.

> 원전(MiMo 도메인 9) 실체: `mathjax-full` (MathJax v3.2), `mhchemparser`
> (화학 파서), `speech-rule-engine` (수식 음성 엔진). Antigravity CLI / agy-pro에서는 MCP 없이
> **npm peer 설치 + Node 스크립트**로 자체 구축한다(REQ-011). 전제: 산출물은
> `design-core-harness` 시각 검증 루프 대상, 옳아도 잘리면 미완성이다.

---

## 1. 트리거 조건 (언제 본 스킬인가)

| 사용자 발화 패턴 | 산출 형태 |
|------------------|-----------|
| "LaTeX 수식", "분수/적분/행렬을 예쁘게" | 벡터 SVG 조판 (§3) |
| "화학 반응식", "분자식 보여줘" | mhchem 조판 (§4) |
| "논문/리포트용 수식", "출판 품질" | SVG + 테마 대비 + 접근성 (§5~§6) |
| "스크린리더로 수식이 읽혀야 해" | SRE aria-label (§6) |

> 라우팅 분담: 수식이 아닌 **정적 다이어그램**은
> [`design-vector-svg`](../design-vector-svg/SKILL.md), 데이터 차트는
> [`design-data-viz`](../design-data-viz/SKILL.md), **조작 가능한** 수학
> 데모는 [`design-interactive-sandbox`](../design-interactive-sandbox/SKILL.md).
> 본 스킬의 대상은 "읽히는 수식과 화학식"이다.

---

## 2. 도구 선택 사다리 (mathjax-full 우선)

```text
① mathjax-full v3 (npm, Node 직접 로드)          ← 주 경로. 오프라인·출판급 SVG
     npm install --no-save mathjax-full
② MathJax v3 CDN (브라우저, tex-svg 컴포넌트)    ← 인터넷 있는 1회성 프리뷰
③ KaTeX (npm/CDN)                                ← MathJax 불가 시 강등 (§8.1)
④ 유니코드 수식 텍스트                            ← 라이브러리 전무 최후 강등 (§8.2)
     + "시각 미검증(UNVERIFIED)" 배지 의무 부착
```

- ①이 주 경로다. 브라우저에 의존하지 않고 **Node에서 SVG 문자열을
  생산**하므로 visual_qa 루프·파일 산출 모두와 궁합이 좋다.
- peer 원칙: `--no-save` 설치, require 실패 시 설치 안내 후 exit 2 (visual_qa.js와
  동일 철학). ②는 오프라인에서 깨진다, 이식성 요구 시 ①/③ 산출물을 인라인으로.

---

## 3. mathjax-full Node 조판 파이프라인 (주 경로 실코드)

### 3.1 tex2svg 스크립트

공식 MathJax-demos-node `direct/tex2svg` 패턴(CommonJS 직접 require)을
따르는, 본 스킬의 엔진 스크립트 한 개:

```javascript
// tex2svg.js, LaTeX 문자열 → 독립 벡터 SVG (peer: mathjax-full)
'use strict';
const fs = require('fs');

function requirePeer(name) {
  try { return require(name); } catch (e) {
    console.error(`[tex2svg] peer 의존성 로드 실패: ${name}\n` +
      '  설치: npm install --no-save mathjax-full, 불가 시 §8 사다리로 강등.');
    process.exit(2);
  }
}

const { mathjax }             = requirePeer('mathjax-full/js/mathjax.js');
const { TeX }                 = requirePeer('mathjax-full/js/input/tex.js');
const { SVG }                 = requirePeer('mathjax-full/js/output/svg.js');
const { liteAdaptor }         = requirePeer('mathjax-full/js/adaptors/liteAdaptor.js');
const { RegisterHTMLHandler } = requirePeer('mathjax-full/js/handlers/html.js');
// 직접 로드 모드는 \require{mhchem} 불가 → 설정 모듈을 정적 등록(server/direct 규칙).
requirePeer('mathjax-full/js/input/tex/mhchem/MhchemConfiguration.js');

const adaptor = liteAdaptor();
RegisterHTMLHandler(adaptor);
const tex = new TeX({ packages: ['base', 'ams', 'newcommand', 'noundefined', 'mhchem'] });
// fontCache:'local' → 글리프 defs를 SVG 내부에 봉합(단일 파일 이식성의 핵심).
const svg = new SVG({ fontCache: 'local' });

function tex2svg(input, { display = true, em = 16, containerWidth = 680 } = {}) {
  const doc = mathjax.document('', { InputJax: tex, OutputJax: svg });
  const node = doc.convert(input, {
    display, em,
    ex: em / 2,          // 노드에는 실측 불가 → ex≈em/2 고정
    containerWidth,      // 줄바꿈·확장 판정 기준 폭(§7 예산과 연동)
  });
  return adaptor.innerHTML(node); // <svg>…</svg> (컨테이너 포함은 outerHTML)
}

if (require.main === module) {
  const [texStr, outPath] = process.argv.slice(2);
  if (!texStr) { console.error('사용법: node tex2svg.js "LaTeX" [out.svg]'); process.exit(2); }
  const result = tex2svg(texStr);
  if (outPath) fs.writeFileSync(outPath, result, 'utf8'); else console.log(result);
}
```

```bash
node tex2svg.js "\int_{-\infty}^{\infty} e^{-x^2}\,dx = \sqrt{\pi}" integral.svg
node tex2svg.js "\ce{2H2 + O2 -> 2H2O}" water.svg   # §4 화학식
```

### 3.2 옵션 결정 규칙

| 옵션 | 값 | 근거 |
|------|-----|------|
| `fontCache` | `'local'` | SVG 단독 개방 시에도 글리프 온전해야 함 |
| `em` | 16 | 수식 크기는 본문 em에 비례. 크게 보이려면 em을 내리고 CSS로 조절 |
| `containerWidth` | 680 | design-vector-svg 뷰포트 규격과 동일 기준, 병합 시 폭 충돌 원천 차단 |
| `packages` | base+ams+newcommand+noundefined+mhchem | 직접 로드는 Ahead-of-Time 등록이 원칙(§3.1). 행렬은 ams 포함 |

### 3.3 HTML 프리뷰 래퍼 (검증·납품 공용)

SVG 단품이 아니라 **문서에 심어진 상태**가 산출물이다. `<head>` 표준 3종
(`lang="ko"`/charset/viewport)은 design-interactive-sandbox §2.2 규격 공유.
아래 골격에 tex2svg 출력(outerHTML)을 인라인 삽입한 `mathjax-preview.html`을
§9 검증 대상으로 삼는다.

```html
<style>
  :root { --fg: #18181b; --bg: #fafafa; }                  /* §5 테마 토큰 */
  @media (prefers-color-scheme: dark) { :root { --fg: #e8e6df; --bg: #101418; } }
  body { margin: 0; padding: 48px 24px; background: var(--bg); color: var(--fg); }
  .prose { max-width: 680px; margin: 0 auto; font: 400 17px/1.7 Georgia, serif; }
  mjx-container { color: var(--fg); }        /* currentColor 상속, §5 계약 */
  mjx-container[display="true"] { display: block; margin: 1em 0; }
</style>
<article class="prose">
  <h1>[제목]</h1><p>본문…</p>
  <!-- tex2svg.js 출력(outerHTML)을 인라인 삽입 -->
</article>
```

> 브라우저 CDN 프리뷰(②)가 필요할 때만: config가 먼저, 로더가 나중
> (순서 뒤집으면 mhchem 미적용).
> `<script>window.MathJax = { loader: { load: ['[tex]/mhchem'] }, tex: { packages: { '[+]': ['mhchem'] } } };</script>`
> `<script id="MathJax-script" async src="https://cdn.jsdelivr.net/npm/mathjax@3/es5/tex-svg.js"></script>`

---

## 4. mhchem 화학 조판 (\ce{} / \pu{})

mhchem은 LaTeX mhchem 패키지의 `\ce`(화학식)·`\pu`(물리 단위) 매크로를
구현한다. 화학식을 **텍스트가 아닌 결합 구조로** 조판한다.

### 4.1 구문 규칙 (실전 패턴)

| 목적 | TeX 원문 | 조판 결과 의도 |
|------|----------|----------------|
| 반응식 | `\ce{2H2 + O2 -> 2H2O}` | 계수·화살표·분자하수 자동 배치 |
| 조건부 화살표 | `\ce{CH4 + 2O2 ->[\Delta] CO2 + 2H2O}` | 화살표 위 가열(Δ) 조건 라벨 |
| 평형 | `\ce{N2 + 3H2 <=> 2NH3}` | 양방향 평형 화살표(`<=>`) |
| 상태 기호 | `\ce{H2O(l)}`, `\ce{Na+(aq)}` | (s)·(l)·(g)·(aq) 표기 |
| 전하·이온 | `\ce{SO4^{2-}}`, `\ce{Fe^{3+}}` | 전하를 위첨자로 통합 정렬 |
| 수화물 | `\ce{CuSO4.5H2O}` | `.` 중점으로 결정수 결합 |
| 단위 | `\pu{25 mol/L}`, `\pu{101.325 kPa}` | 수치+단위 고정 스페이스 |

### 4.2 mhchemparser 연계 (사전 검증, 선택)

`mathjax-full`은 `mhchemparser`를 자동 설치한다(조판 내장). 추가로 **원문
검증기**로 쓰면 렌더링 전 문법 오류를 잡아 §9 반복 비용이 줄어든다.
사전검증은 선택이지 관문이 아니다(관문은 §9 시각 검증).

```javascript
// peer: npm install --no-save mhchemparser (버전별 진입점은 API 문서 확인)
function checkCe(texSrc) {
  const errors = [], re = /\\ce\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(texSrc)) !== null) {
    try { require('mhchemparser').parse(m[1]); }
    catch (e) { errors.push(`${m[1]} :: ${e.message}`); }
  }
  return errors; // [] 아니면 렌더 중단, 원문 교체 요구
}
```

### 4.3 화학 조판 규칙

1. 계수는 항상 `\ce` 안에서: 텍스트 `2H2O`와 조판 `\ce{2H2O}`의 2는
   자리가 다르다. 원고에 섞여 있으면 정규화한다.
2. 반응 화살표 3종 고정: `->`(비가역), `<=>`(평형), `<->`(공명). 임의
   문자 `→`를 LaTeX에 섞지 않는다(폰트 의존).
3. 복잡한 유기 구조식(고리·치환)은 `\ce`를 넘어선다 → SVG 수작업으로
   전환하고(design-vector-svg) 본 스킬의 벡터 규격으로 봉합한다.
   억지 LaTeX는 금지.

---

## 5. 다크/라이트 테마 명도 대비 (currentColor 상속)

MathJax v3 SVG 출력 path의 기본은 **`fill="currentColor"`**, 수식 색은 CSS
`color`를 상속한다. SVG 내부 색을 하드코딩하지 않고 **컨테이너 color만**
바꿔 테마를 전환하는 것이 본 스킬의 테마 계약이다(실코드 = §3.3 래퍼
토큰, JS 없이 작동).

### 5.1 테마 토큰 표

| 테마 | 배경(--bg) | 수식 전경(--fg) | 대비 | 판정 |
|------|-----------|----------------|------|------|
| light | `#fafafa` | `#18181b` | ≈15.9:1 | AAA(≥7:1) ✅ 학술 기본 |
| dark | `#101418` | `#e8e6df` | ≈13.2:1 | AAA ✅ 야간 원고 |
| light 강조 | `#fafafa` | `#7c2d12` | ≈8.9:1 | AAA ✅ 기호 주석용 |

규칙:
1. 수식 본문 색은 문서 본문과 **동일 토큰**만, 밝은 회색(`#9ca3af`류)은 탁해져 AAA를 깬다.
2. 대비 계산은 배경의 **가장 밝은 지점** 기준(design-data-viz §5.1 동일).
3. 강조만 별도 색 허용(AAA 유지) + 굵기·상자 중 하나로 색맹 대응 보강.
4. SVG 단품 납품 시 하드코딩된 fill은 `fill="currentColor"`로 되돌린다.

---

## 6. 접근성, speech-rule-engine + assistive MathML

SVG 수식은 스크린리더에 빈 이미지로 읽힌다. 벡터 조판과 동시에 음성
문자열을 산출물에 심는 것이 출판 규정 수준이다.

### 6.1 노드 파이프라인 확장 (aria-label 주입)

```javascript
// §3 상단에 추가: 어시스티브 MathML 캐리지, mjx-container 안 <math> 출력이
// 아래 SRE 입력(mathmlStr)이 돼 스크린리더가 수식을 직접 읽는다.
const { AssistiveMmlHandler } = require('mathjax-full/js/a11y/assistive-mml.js');
AssistiveMmlHandler(RegisterHTMLHandler(adaptor)); // 등록 순서: handler 다음

// SRE로 음성 문자열 → svg에 role="math"+aria-label 부여
// peer: npm install --no-save speech-rule-engine
async function labelSvgWithSpeech(svgStr, mathmlStr) {
  const sre = require('speech-rule-engine');
  await sre.setupEngine({ locale: 'en', style: 'clearspeak', mod: 'default' });
  await sre.engineReady();                  // 엔진 비동기, 대기는 필수
  const speech = sre.toSpeech(mathmlStr);   // MathML 문자열 → 평문 음성
  return svgStr.replace(/<svg\b/,
    `<svg role="math" aria-label="${speech.replace(/"/g, '"')}"`);
}
```

> SRE는 Clearspeak(명료한 자연어)/Mathspeak, Nemeth 점자까지 지원, > 규칙 스타일은 대상 독자가 정하지 기본값으로 넘기지 않는다.

### 6.2 브라우저 경로(②): `loader.load`에 `a11y/speech`·`a11y/braille`를,
config에 `a11y: { speech: 'sre', subtitles: true }`를 추가하면 Speech Explorer+자막 활성.

### 6.3 접근성 규칙

1. display 수식 **전부** `role="math"` + `aria-label`(음성 문자열), 하나라도 없으면 §10에서 반려.
2. 수식번호(`\tag{3}`)는 음성 문자열 끝에 붙여 위치 참조를 살린다.
3. 캡처가 불가한 보조 도구 영역이므로 `aria-label` 원문을 결과 리포트에
   **기록**해 화면 검증을 대체한다(명세 기록 = 부분 검증).

---

## 7. 복잡 수식 조판 규칙 (텐서·행렬)

### 7.1 행렬 (ams 패키지)

| 환경 | 용도 | 구분자 |
|------|------|--------|
| `pmatrix` | 수학적 행렬 | ( ) |
| `bmatrix` | 선형대수 행렬 | [ ] |
| `Bmatrix` | 조합 | { } |
| `vmatrix` | 행렬식 | \| \| |
| `smallmatrix` | 인라인 행렬 | 본문 높이 유지 |

- 인라인 행렬은 반드시 `\bigl(\begin{smallmatrix}…\end{smallmatrix}\bigr)`.
  display용 큰 행렬을 본문 줄에 넣으면 줄간격이 터진다(§9 겹침 결함의
  단골 원인).
- 행렬 열 ≤ **6열** 예산. 초과 시 분할 행렬
  (`\left(\begin{array}{cc}A&B\\0&C\end{array}\right)`) 또는 식 분리.

### 7.2 텐서·인덱스

- 상·하 인덱스 병존: `T^{\mu\nu}_{\ \alpha\beta}`, `\ `(스페이스)로
  오프셋을 맞춰 첨자 간 겹침을 막는다.
- 고계 도함수는 본문 인라인 `\partial_x^n u`를 선호, display에서만
  `\frac{\partial^2 u}{\partial x^2}` 분수 2층으로 전개.
- 지수 중첩(`e^{e^{x}}`)은 2층까지만. 3층 이상은
  `\exp\left(\dots\right)`로 풀어 가독성 회복.

### 7.3 조판 예산 (containerWidth=680 기준)

1. display 식 폭이 680px 초과 → `aligned`/`split`로 **2행 분할**.
   잘림은 수정이 아니라 분할로 해결한다.
2. 분수 중첩 ≤ 3층, `aligned` 정렬점은 1개만. 식 번호는 `\tag{n}` 연속.
3. 한 화면 display 식 ≤ 6개. 초과 시 섹션 분리.

---

## 8. 폴백 사다리 실행

### 8.1 KaTeX 강등 (MathJax 불가 시)

```javascript
// peer: npm install --no-save katex
require('katex/contrib/mhchem');           // 확장 require 후 \ce 활성
const html = require('katex').renderToString('\\ce{2H2 + O2 -> 2H2O}', {
  displayMode: true,
  throwOnError: false,      // 오류 식을 빨간 글자로 노출, 조용히 죽이지 않음
  output: 'htmlAndMathml',  // MathML 사본 → 스크린리더 환류 (§6 대체)
});
```

차이점 계약: KaTeX는 SRE 내장이 없으므로 `htmlAndMathml`의 MathML에
직접 `aria-label`을 조립하거나 §6.1 SRE를 별도 호출해 부착한다. 폰트
CSS가 동반되어야 하므로 **HTML 번들로만** 산출한다(SVG 단품 불가).

### 8.2 유니코드 강등 + 배지 (라이브러리 전무 시)

| LaTeX | 유니코드 강등 |
|-------|---------------|
| `\frac{\partial^2 u}{\partial x^2}` | ∂²u/∂x² |
| `e^{i\pi}+1=0` | e^(iπ) + 1 = 0 |
| `\sqrt{1+x^2}` | √(1+x²) |
| `\ce{2H2 + O2 -> 2H2O}` | 2H₂ + O₂ → 2H₂O |
| `\int_a^b f(x)\,dx` | ∫ₐᵇ f(x) dx |

분수·행렬·다층 지수가 있는 순간 **강등의 한계를 고지**한다(억지
유니코드는 오독을 낳는다). 산출물 선두에 core-harness §7.3 배지 원문을
**그대로** 부착:

```text
⚠️ 시각 미검증(UNVERIFIED), 렌더링 도구 없이 명세 대조만 수행됨.
실제 화면 검증(Visual QA Loop) 전에는 이 산출물을 완성으로 간주하지
말 것.
```

---

## 9. visual_qa.js 루프 연동

```text
[tex2svg.js → preview.html] ──▶ node ../design-core-harness/scripts/visual_qa.js
        ▲                                preview.html ./shots
        │      Vision 대조: §9.1 체크리스트 (+ light/dark 2회 캡처)
        └── [수정(1결함 단위: 1개 식만 재조판)] ◀┘
```

- 대상은 항상 **HTML 래퍼에 심은 상태**(§3.3). SVG 단품 캡처는 여백·
  baseline이 문서 문맥과 달라 검증 가치가 낮다.
- 테마 2회 캡처: light/dark를 강제해 각각 스크린샷, §5 대비가 **두 테마
  모두**에서 실측되는지 확인. 도구 폴백은 core-harness §7 사다리.

### 9.1 수식 전용 추가 체크리스트 (core-harness 4대 결함 위에)

- [ ] display 식이 컨테이너 폭(680px)에서 우측 잘림 0건, 마지막 기호를
      경계 확대해 완성형인지 확인
- [ ] 분수선·근호 상단이 위 문장과 겹치지 않음 (행간 침범)
- [ ] 큰 구분자 `\left(…\right)`가 인접 baseline을 밀지 않음
- [ ] 상·하 인덱스 병존 위치에서 첨자 간 충돌 0건 (§7.2)
- [ ] `\tag{n}` 번호가 화면 밖으로 밀려나지 않음
- [ ] light/dark 캡처 각각에서 전경-배경 대비 AAA(§5.1) 실측
- [ ] 화학식 화살표 위 조건 라벨(`->[\Delta]`류)과 위 분자 겹침 0건

---

## 10. 완료 선언 최종 체크리스트

- [ ] 사다리 단계(①~④)와 산출물이 일치 (①인데 CDN `<script>` 없음 확인)
- [ ] `fontCache:'local'`, SVG 단품으로 열어 글리프가 온전함
- [ ] display 식 전부 `role="math"` + `aria-label` (§6.3)
- [ ] 테마 2종 캡처 대비 AAA 통과 (§5, §9)
- [ ] 화학식 원문 계수·화살표·상태 표기 정규화 (§4.3)
- [ ] 조판 예산(열 ≤6, 분수 ≤3층, 식 폭 ≤680) 위반 0건 (§7)
- [ ] Visual QA Loop 통과 + 검증 수단 기록 (visual_qa.js / view_file
      / 강등+배지) (§9)

> 검증 수단을 기록하지 않은 완료 선언은 거짓 완료로 간주한다
> (core-harness §9 동일).

---

## 11. 관련 스킬 및 레퍼런스

- [`design-core-harness`](../design-core-harness/SKILL.md), 시각 검증
  루프·4대 결함·폴백 사다리·UNVERIFIED 배지 원문 (§9의 상위 하네스)
- [`design-vector-svg`](../design-vector-svg/SKILL.md), 680px 뷰포트
  규격 공유, 구조식·다이어그램 이관 시 사용
- [`design-data-viz`](../design-data-viz/SKILL.md), 다크 테마 대비
  규칙(가장 밝은 배경 지점 기준) 공유
- [`design-interactive-sandbox`](../design-interactive-sandbox/SKILL.md), 조작형 수학 요구 전환 시
- 레퍼런스: MathJax 공식 문서(server/direct), MathJax-demos-node `direct/tex2svg`,
  mhchem 확장 문서, speech-rule-engine npm README
