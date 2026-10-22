---
name: design-3d-web
description: >
  Web-native 3D graphics, procedural geometries, and custom GLSL vertex/fragment shaders using Three.js and GSAP without Blender or heavy external runtimes.
---

# Design 3D Web (Three.js 웹 3D 캔버스)

3D는 "코드만 보고 맞다"가 존재하지 않는다. 메시가 카메라 뒤로 삼켜졌는지,
조명이 한 면만 태우는지, 회전이 깨지는지, 전부 **돌려봐야** 안다. 정적
스크린샷 한 장으로는 3D의 본질(움직임·깊이)이 검증되지 않는다. 본 스킬은
사전 계약서 → 구현 → 인터랙션 시퀀스 검증 순서를 강제하는 3D 실행 프레임이다.

> 원전(MiMo 도메인 5): ① `specs/3d/design.md` 사전 계약 → ② Blender 절차적
> 모델링 → ③ Three.js+GSAP+GLSL 웹 캔버스. Antigravity CLI / agy-pro엔 Blender MCP가 없어
> (REQ-011) ②를 Three.js 절차적 지오메트리+GLSL로 대체한다(§1.1). 산출물은
> `design-core-harness` 루프 대상이며 §7이 3D 전용 검증 규칙이다.

---

## 1. 트리거와 Blender 대체 경계

| 사용자 발화 | 산출 |
|-------------|------|
| "3D 히어로", "제품 360° 회전" | Three.js 씬 + 턴테이블 (§5.2) |
| "스크롤 반응 3D", "패럴랙스 웹" | GSAP ScrollTrigger 씬 (§3.2) |
| "프로시저럴/셰이더 이펙트" | 커스텀 GLSL (§4) |
| "Blender로 만들어줘" | **Blender 미사용**, 아래 매핑으로 Three.js 대체 |

### 1.1 Blender 작업의 Three.js 매핑 (대체재 계약)

| Blender 능력 | Three.js 대체 | 한계 고지 |
|--------------|---------------|-----------|
| 매니폴드 모델링 | `Box`/`TorusKnot`/`Lathe` 조합, `ExtrudeGeometry`(2D 형상→3D) | 자유형 스컬프트 불가 → 형상 요약으로 설계 반영 |
| 절차적 셰이더 노드 | ShaderMaterial GLSL (§4), 노드 그래프를 수식으로 번역 | 노드 GUI 없음. 수식 서술 가능해야 구현 가능 |
| 턴테이블 카메라 애니메이션 | 궤도 회전 / `OrbitControls` (§5.2) | 렌더 베이크 없음, 실시간 웹이 목표 |
| 물리 시뮬레이션 | 범위 밖 → design-interactive-sandbox(2D 위젯)로 라우팅 | 2D 시뮬레이션 권장 |

> 경계: 단일 파일 렌더링 가능 산출물(HTML+CDN)까지 Designer 직접,
> 프레임워크 통합(React Three Fiber 등)은 code 모드 인계.

---

## 2. 사전 계약서 원칙 (design.md 3D 절)

**구현 전에** design-core-harness §6 계약서에 아래 3D 절을 추가한다, 조명·카메라·인터랙션 3종 없이는 구현을 시작하지 않는다(구조적 선택).

```markdown
## 7. 3D 씬 계약 (본 스킬 전용)
- 씬 조명: [키/필/림 3점 or 환경광 or 단광]
  * 예: "key warm(1,1,0.5) 강도1.2 / fill cool 0.35 / rim 0.8"
- 카메라 투영각: [Perspective fov=45 | Orthographic zoom=1.0]
  * 초기 위치·타깃·near/far(0.1/100), 씬 스케일과 정합
- 인터랙션 상태: [정지 / 자동회전 / 드래그 / 스크롤 연동] + 전이 표기
  * 예: idle →(hover)→ dragging →(release, 관성 600ms)→ idle
- 애니메이션: [턴테이블 주기 / ScrollTrigger 시퀀스 키프레임]
- 성능 예산: [트라이앵글 ≤ N, 머티리얼 ≤ M, 텍스처 ≤ 1024px] (§6에서 택1)
```

§7 검증은 이 계약과 인터랙션 시퀀스 캡처를 diff한다. "코드가 이렇게
됐으니 이랬겠지"로 판정하면 3D는 반드시 놓친다.

---

## 3. Three.js + GSAP ScrollTrigger 구성 (CDN 기반)

### 3.1 스캐폴드 (1회성 단일 파일 산출물)

```html
<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>[씬 제목]</title>
  <style>
    html, body { margin: 0; height: 100%; background: #0a0c10; }
    #scene { position: sticky; top: 0; width: 100vw; height: 100vh; }
    .spacer { height: 300vh; }                              /* 스크롤 트랙 */
  </style>
  <script type="importmap">
  { "imports": {
      "three": "https://cdn.jsdelivr.net/npm/three@0.160.0/build/three.module.js",
      "three/addons/": "https://cdn.jsdelivr.net/npm/three@0.160.0/examples/jsm/"
  } }
  </script>
</head>
<body>
  <div id="scene"></div>
  <div class="spacer"></div>
  <script src="https://cdn.jsdelivr.net/npm/gsap@3.12.5/dist/gsap.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/gsap@3.12.5/dist/ScrollTrigger.min.js"></script>
  <script type="module">
    // 동적 import: 정적 import의 CDN 차단은 try/catch로 못 잡는다(§8 강등 ②).
    let THREE, OrbitControls;
    try {
      THREE = await import('three');
      ({ OrbitControls } = await import('three/addons/controls/OrbitControls.js'));
    } catch { degradeToCss3d(); }            // §8
    // 이하 §3.2 부트스트랩 → §5 루프/턴테이블 → §6 가드레일 → §7 검증
  </script>
</body>
</html>
```

규칙:
1. **버전 고정** (`three@0.160.0`, `gsap@3.12.5`), 무고정은 API 붕괴 지름길(r150 전후 colorSpace·라이팅 기본값 변경 사례).
2. GSAP은 UMD 전역, Three는 ES module, 섞되 Three 코드는 module 안에서만 참조.
3. 오프라인·npm 요구 시 `npm install --no-save three gsap` peer 전환(bundling은 code 모드 인계). §8 강등과 혼동 금지.

### 3.2 씬 부트스트랩 + ScrollTrigger

```javascript
const wrap = document.getElementById('scene');
const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(Math.min(devicePixelRatio, 2));    // §6 상한
renderer.setSize(wrap.clientWidth, wrap.clientHeight);
renderer.toneMapping = THREE.ACESFilmicToneMapping;
wrap.appendChild(renderer.domElement);

const scene = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(45, wrap.clientWidth / wrap.clientHeight, 0.1, 100);
camera.position.set(0, 0.6, 4);   // design.md §7 정합(fov·near/far·초기 위치)

const key  = new THREE.DirectionalLight(0xffe9d2, 1.2); key.position.set(1, 1, 0.5);
const fill = new THREE.DirectionalLight(0x9db8d9, 0.35);
const rim  = new THREE.DirectionalLight(0xffffff, 0.8); rim.position.set(-1, 0.4, -1);
scene.add(key, fill, rim);                                  // 계약 값 상수화

gsap.registerPlugin(ScrollTrigger);
const M = { progress: 0 };                                  // §2 상태 머신
gsap.to(M, { progress: 1, ease: 'none',
  scrollTrigger: { trigger: '.spacer', start: 'top top',
                   end: 'bottom bottom', scrub: 0.6 } });
// GSAP만 progress를 "쓰고" 렌더 루프(§5.1)는 "읽기"만, 쓰기 소유권 분리.
// scrub 0.6: 600ms 관성(계약값). 0은 찰깃거림, ≥2는 따라오지 못해 늘어짐.
```

---

## 4. 커스텀 GLSL 기초 (절차적 노드 → GLSL 번역)

Blender 절차적 셰이더 노드는 결국 "좌표 → 수식 → 색" 그래프다.
ShaderMaterial의 GLSL로 번역한다.

| 노드 개념 | GLSL 번역 |
|-----------|-----------|
| Texture Coordinate | `uv` 속성 → vertex에서 varying으로 전달 |
| Noise (Procedural) | GLSL 내장 없음 → value noise 직접 (아래) |
| ColorRamp | `mix(cA, cB, smoothstep(a, b, t))` |
| Map Range | `lo + (hi-lo) * clamp((v-a)/(b-a), 0., 1.)` |
| Mix (factor) | `mix(colA, colB, factor)` |

```javascript
const mat = new THREE.ShaderMaterial({
  uniforms: {
    uTime:   { value: 0 },
    // hex는 design.md 계약과 diff 대상(규칙 1), 셰이더 내 하드코딩 금지
    uColorA: { value: new THREE.Color('#0e2a3f') },
    uColorB: { value: new THREE.Color('#ffb454') },
  },
  vertexShader: /* glsl */`
    varying vec2 vUv;
    void main() { vUv = uv;
      gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0); }`,
  fragmentShader: /* glsl */`
    uniform float uTime; uniform vec3 uColorA, uColorB; varying vec2 vUv;
    float n21(vec2 p) { return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
    float noise(vec2 p) {                        // 6줄 사인 합성으로 충분
      vec2 i = floor(p), f = fract(p); f = f * f * (3.0 - 2.0 * f);
      return mix(mix(n21(i), n21(i + vec2(1, 0)), f.x),
                 mix(n21(i + vec2(0, 1)), n21(i + vec2(1, 1)), f.x), f.y); }
    void main() {                                // ColorRamp 상당의 2구간
      float f = smoothstep(0.45, 0.75, noise(vUv * 6.0 + uTime * 0.3));
      gl_FragColor = vec4(mix(uColorA, uColorB, f), 1.0); }`,
});
// 루프에서 uTime을 += dt 누적(§5.1), 매 프레임 재설정 시 애니메이션이 멈춘다.
```

규칙:
1. 색·시간·밀도는 **uniform로**, 계약서와 diff 가능한 상태로 유지.
2. 셰이더는 씬당 ≤ 2종. 초과하면 위계가 아니라 소음이다.
3. 노이즈는 위 수준(≤6줄)에서 끝, 3D simplex 유입은 §6 예산 폭파.

---

## 5. 렌더 루프(60fps)·턴테이블·리사이즈

### 5.1 단일 루프 계약 (실코드)

```javascript
let last = performance.now(), fpsEMA = 60;
function tick(now) {
  const dt = Math.min((now - last) / 1000, 0.05); last = now;   // 탭 비활성 가드
  fpsEMA = fpsEMA * 0.92 + (1 / Math.max(dt, 1e-4)) * 0.08;     // 지수이동평균
  if (mat.uniforms) mat.uniforms.uTime.value += dt;              // 누적(점프 방지)

  if (S.mode === 'turntable') {                                   // §5.2
    group.rotation.y = now * 0.001 * (2 * Math.PI / TURNTABLE_PERIOD);
  } else if (S.mode === 'dragging') controls.update();           // damping 소산
  // scroll 상태: M.progress로 카메라/오브제 포즈(§3.2), 읽기만
  renderer.render(scene, camera);                                 // 렌더는 이 한 번
  requestAnimationFrame(tick);
}
requestAnimationFrame(tick);
// 이벤트 핸들러 안 산발 render() 금지, 이중 렌더로 FPS 반토막(3D 특유).
// fpsEMA는 §7 검증 오버레이로 노출하고 납품본에서는 제거한다.
```

### 5.2 턴테이블 카메라 애니메이션 패턴

```javascript
const TURNTABLE_PERIOD = 12;          // 12s: 늦지도 급하지도 않은 전시 주기
const S = { mode: 'turntable' };      // §2 계약 상태 머신
controls.enableDamping = true;        // 드래그 관성
controls.addEventListener('start', () => { S.mode = 'dragging'; });
controls.addEventListener('end', () => {
  clearTimeout(S.t);
  S.t = setTimeout(() => { S.mode = 'turntable'; }, 1500);  // 1.5s 무입력→자동회전 복귀
});
// 자동회전 없는 히어로는 "정지 이미지"로 착각된다. 단 prefers-reduced-motion 시 정지+버튼(§7).
```

| 회전 대상 | 방법 | 용도 |
|-----------|------|------|
| 씬 전체 | `group.rotation.y` (메시 원점 중심 필수) | 제품·오브제 단독 전시 |
| 카메라 | `camera.position` 원호 + `lookAt(0,0,0)` | 배경과 깊이차 있는 씬 |

- **회전 원점 실수는 3D 특유의 1호 결함**: 메시가 원점 밖이면 돌릴 때
  화면에서 나간다. 그룹화+`geometry.center()`(또는 `Box3` 중심 정렬)로
  계약서에 "원점 중심"을 명문화한다.
- `OrbitControls.autoRotate`도 유효하나 damping과 속도 곡선이 달라 재현성 필요 시 위 수동 패턴.

### 5.3 리사이즈 이벤트 쓰로틀링 (rAF 배칭, 실코드)

```javascript
let resizePending = false;
window.addEventListener('resize', () => {
  if (resizePending) return;                      // 같은 프레임 내 중복 소거
  resizePending = true;
  requestAnimationFrame(() => {
    resizePending = false;
    const w = wrap.clientWidth, h = wrap.clientHeight;
    renderer.setSize(w, h);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();   // 갱신 누락 → 찌그러짐이 '의도'로 보임
  });
});
// setTimeout 디바운스보다 rAF 배칭이 정확: 렌더 주기와 정렬돼 setSize가
// 프레임당 1회로 수렴한다. 모바일 주소창 show/hide resize도 흡수.
```

---

## 6. 성능 가드레일 (예산 표)

| 항목 | 예산 | 근거 |
|------|------|------|
| 트라이앵글 | 저사양 50K / 기본 150K / 상한 500K | 150K는 모바일 GPU 60fps 체감 경계 |
| 드로잉콜 | 정적 메시 `mergeGeometries` 합체, 머티리얼 ≤ 8 | 드로잉콜이 폴리곤보다 비싼 병목. 머티리얼 1종=콜 1종 |
| 셰이더 | ShaderMaterial ≤ 2종 (§4 규칙 2) | 컴파일·바인드 비용 |
| 텍스처 | 최대 변 **1024px**, `colorSpace=SRGBColorSpace`, 총 ≤ 8MB | 2048² RGBA는 디코드 후 16MB VRAM, 모바일 스로틀 |
| 그림자 | 캐스케이드 ≤ 1, 1024², 불필요 씬은 `castShadow=false` | 그림자 패스는 씬 렌더 2배 |
| 입자 | `Points` 단일 콜, 5K+는 셰이더 계산(CPU for-loop 금지) | 파티클 루프는 예산 폭파 |
| dpr | `Math.min(devicePixelRatio, 2)` | 4K+ 과부하 방지 |
| 루프 | 단일 rAF (§5.1), 이벤트 렌더 금지 | 프레임 경합 = FPS 반토막 |

- 저사양 감지: `fpsEMA < 45` 3초 지속 → 한 단계 강등(dpr 1/그림자 off/텍스처 절반) 후 **강등 사실 기록**.
- 정리: 씬 교체 시 `geometry/material/texture/renderer.dispose()`, 교체형 데모 필수(VRAM 누수).

---

## 7. 시각 검증 (인터랙션 시퀀스 캡처 → Vision 대조)

**3D는 정적 스크린샷 한 장으로 검증되지 않는다**(core-harness 키프레임 원칙의 3D 판). 절차:

```text
① 초기 프레임 → visual_qa.js(정적: 4대 결함·블랙리스트)
② 인터랙션 키프레임 → 포즈 강제 설정 → 포즈당 캡처(아래 주입법)
③ Vision 대조: design.md §7 계약(조명·fov·상태) diff → ④ 수정(1결함 단위) → ① 회귀 확인
```

캡처 도구로 누를 수 없는 조작은 **코드로 포즈를 찍어** 검증한다, `?pose=`
URL 파라미터로 초기 상태를 고정하게 §3.2를 설계하면 포즈마다 별도 URL이
되어 visual_qa.js로 개별 캡처된다. 예: `turntable-0s`(정면)/`turntable-3s`(90°)/`turntable-6s`(후면), `scroll-start/mid/end`.

### 7.1 3D 전용 체크리스트 (core-harness 4대 결함 위에)

- [ ] 오브제 화면 내·잘림 0, "빈 화면"(fov·near/far·원점·조명 0의 침묵 실패)은 별도 판정
- [ ] 조명 3점 계약 정합: 키 그림자 방향/필 소실/림 가장자리 식별
- [ ] 키프레임 3포즈 모두 오브제 화면 내, 회전 이탈(§5.2) 0건, 회전 중
      검은 면/플리커/찢힘 프레임 0건
- [ ] ScrollTrigger start/mid/end 3포즈가 실제로 상이, 스크롤 인과 확인
- [ ] 리사이즈 전후(1280×800 vs 390×844) 크롭·찌그러짐 없음
- [ ] reduced-motion 캡처(정지+버튼) 의미 전달 / `fpsEMA` ≥ 60 (dsf:1 캡처 저하 0건)

> 폰백: visual_qa 불가 시 수동 조작 화면을 스크린샷으로 저장해 view_file로 읽어 기록.
> 포즈 주입도 불가하면 정적 1캡처 + "모션 미검증" 명시(§8.3, core-harness §7).

---

## 8. 폴백 사다리 (WebGL 미지원 / CDN 차단)

### 8.1 판정과 수렴

```javascript
function webglOK() {
  try {
    const c = document.createElement('canvas');
    return !!(window.WebGLRenderingContext &&
              (c.getContext('webgl2') || c.getContext('webgl')));
  } catch { return false; }
}
// 트리거 2종: ① !webglOK()(컨텍스트 불가) ② §3.1 import catch(CDN 차단)
// → 모두 degradeToCss3d() 단일 경로 수렴.
if (!webglOK()) degradeToCss3d();
```

### 8.2 CSS 3D transform 강등 (preserve-3D 큐브)

`perspective` + `transform-style: preserve-3d`로 회전 전시를 흉내 낸다.
6면 카드는 "3D를 보여주려 했다"는 서사를 지키는 최소 형태:

```html
<style>
  .stage3d { width: 240px; height: 240px; perspective: 800px; }
  .cube { width: 100%; height: 100%; position: relative;
          transform-style: preserve-3d; animation: spin 12s linear infinite; }
  .cube > div { position: absolute; inset: 0; display: grid; place-items: center;
                background: rgba(14,42,63,.92); border: 1px solid #ffb454;
                color: #e8e6df; font-weight: 700; }
  .f1 { transform: rotateY(0deg) translateZ(120px); }   .f2 { transform: rotateY(90deg) translateZ(120px); }
  .f3 { transform: rotateY(180deg) translateZ(120px); }  .f4 { transform: rotateY(270deg) translateZ(120px); }
  .f5 { transform: rotateX(90deg) translateZ(120px); }   .f6 { transform: rotateX(-90deg) translateZ(120px); }
  @keyframes spin { to { transform: rotateX(-18deg) rotateY(360deg); } }
  @media (prefers-reduced-motion: reduce) {
    .cube { animation: none; transform: rotateX(-18deg) rotateY(35deg); }
  }
</style>
<div class="stage3d"><div class="cube">
  <div class="f1">FRONT</div><div class="f2">RIGHT</div><div class="f3">BACK</div>
  <div class="f4">LEFT</div><div class="f5">TOP</div><div class="f6">BOT</div>
</div></div>
```

### 8.3 강등 산출물은 "시각 미검증" 취급

CSS 강등은 실제 WebGL 씬(조명·재질·회전 물리)을 **대체하지 못한다**, 산출물 선두에 core-harness §7.3 배지 원문을 부착하고 미검증 범위를 고지한다:

```text
⚠️ 시각 미검증(UNVERIFIED), WebGL 강등(CSS 3D) 산출물. 실제 씬
(조명·재질·회전 물리)은 렌더링 검증 전이다. 완성으로 간주하지 말 것.
```

강등 사실은 결과에 기록하고, 렌더링 도구 기동까지 불가하면 §7.3 명세 강등으로 수렴한다.

---

## 9. 안티-패턴 (3D 특유의 실패)

| ❌ 안티패턴 | ✅ 대체 |
|------------|--------|
| 빈 화면을 "느리다"로 오진 | near/far·fov·원점·조명 순 사인, 3D 오류는 침묵 실패 |
| `scene.add`마다 즉각 `render()` / GSAP과 rAF의 같은 속성 동시 쓰기 | 단일 rAF 루프(§5.1) + 쓰기 소유권 분리(§3.2) |
| 4K 텍스처·최신버전 무비판 수용·라이브러리 남발 | §6 예산 + §3.1 규칙 1 (버전 고정) |
| 정적 스크린샷 1장으로 3D 완료 선언 | 인터랙션 키프레임 3포즈 (§7) |
| 회전 대상이 원점 밖 (제품 한쪽이 소실) | 중심 정렬 + 3포즈 이탈 검사 (§5.2) |

---

## 10. 완료 선언 최종 체크리스트

- [ ] design.md 3D 절 4종(조명/카메라/인터랙션/예산) 구현 전 작성 (§2)
- [ ] three/gsap 버전 고정 + 단일 렌더 루프·dt 가드·리사이즈 rAF 배칭 (§3~§5)
- [ ] 성능 예산 (§6) 초과 0건, 저사양 강등 시 그 사실 기록
- [ ] 초기+턴테이블 3포즈+스크롤 3포즈+리사이즈 2종 캡처 Vision 대조 (§7)
- [ ] reduced-motion 검증, 폴백 사용 시 CSS 강등 + UNVERIFIED 배지 (§8)

> 검증 수단을 기록하지 않은 완료 선언, 그리고 정적 캡처만으로 내린
> 3D 완료 선언은 거짓 완료로 간주한다.

---

## 11. 관련 스킬 및 레퍼런스

- [`design-core-harness`](../design-core-harness/SKILL.md), §2 계약서 원형, §7 검증 루프·4대 결함·폴백 사다리·UNVERIFIED 배지
- [`design-data-viz`](../design-data-viz/SKILL.md), 다크 씬 배경·대비 규칙(배경이 오브제를 삼키면 실패)
- [`design-interactive-sandbox`](../design-interactive-sandbox/SKILL.md), 조작형 2D 시뮬레이션 분담처 (§1.1 물리 라우팅)
- [`frontend-design`](../frontend-design/SKILL.md), MOTION 다이얼 근거(턴테이블 주기·스크롤 관성)
- Three.js docs(WebGL/BufferGeometry/ShaderMaterial), GSAP ScrollTrigger docs, 원전 아키텍처 도메인 1·5

## 6. WebGL Shader Templates
See `templates/shader-canvas-template.html` for ready-to-run WebGL shader setups.

