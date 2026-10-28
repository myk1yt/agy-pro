---
name: design-office-deck
description: >
  오피스·프레젠테이션 조판 스킬. PptxGenJS 기반 슬라이드 마스터/도형 좌표/표 정렬 조판 가이드와 LibreOffice 헤드리스(soffice.exe) → 슬라이드 PNG 렌더링 → Vision QA 검수 → 좌표 보정 루프를 제공한다. 슬라이드별 줄바꿈·텍스트 넘침 검증 체크리스트 포함. soffice 미설치 시 수학적 좌표 검증으로 강등하고 "시각 미검증" 배지를 부착한다. PPTX뿐 아니라 PDF/DOCX 산출물에도 동일 루프를 적용한다.
---

# Design Office Deck (오피스·프레젠테이션 조판)

PPTX/DOCX/PDF를 "텍스트 더미"가 아니라 **정밀 디자인 레이아웃**으로
생성하는 조판 하네스. 핵심은 단순 파일 생성이 아니라,
**생성 → 렌더링 → 눈으로 검수 → 좌표 보정**의 폐루프다.

> 오피스 문서는 HTML처럼 DOM 검사가 쉽지 않다. 따라서 "실제 렌더링된
> 픽셀"을 봐야만 줄바꿈·텍스트 넘침·표 깨짐을 잡을 수 있다. 이 스킬의
> 완료 판정은 반드시 시각 QA 루프(§3) 또는 수학적 강등(§4)을 거친다.

---

## 1. 사전 준비 및 도구 체계

### 1.1 필수/선택 도구

| 도구 | 용도 | 설치 상태 |
|------|------|-----------|
| PptxGenJS (`pptxgenjs`) | PPTX 슬라이드 마스터/도형/표 생성 | Node 패키지 |
| LibreOffice `soffice.exe` | PPTX/PDF → 고해상도 PNG 렌더링 | 선택 (미설치 시 §4 강등) |
| Node.js | PptxGenJS 스크립트 실행 | 필수 |
| 에이전트 Vision / view_file | 렌더 PNG 시각 검수 | view_file로 PNG 직접 읽기 (네이티브) |

### 1.2 의존성 설치 (MCP 추가 불필요, 자체 구축)

```bash
npm init -y
npm install pptxgenjs
```

> LibreOffice는 시스템에 설치된 실행 파일을 그대로 활용한다. 별도 MCP
> 설치가 필요 없다. 설치 위치는 OS별로 다르므로 `soffice` 경로를 먼저
> 탐색한다 (Windows: `C:\Program Files\LibreOffice\program\soffice.exe`).

---

## 2. PptxGenJS 조판 가이드

### 2.1 슬라이드 기본 규격 (16:9)

```javascript
const pptxgen = require('pptxgenjs');
const pptx = new pptxgen();
pptx.defineLayout({ name: 'WIDE', width: 13.333, height: 7.5 }); // 16:9
pptx.layout = 'WIDE';
```

### 2.2 슬라이드 마스터 (공통 템플릿)

모든 슬라이드에 동일한 마스터 요소를 입히려면 루프로 공통 요소를
추가한다. **마스터 요소(배경·로고·페이지 번호)를 슬라이드마다 다르게
좌표를 흔들지 않는다.**

```javascript
const SLIDE_W = 13.333, SLIDE_H = 7.5;
const M = 0.6; // 마진(inch)

function addMaster(ppt) {
  const master = ppt.defineSlideMaster({
    title: 'DECK',
    background: { color: 'F9FAFB' }, // 오피스 톤 배경
    objects: [
      { rect: { x: 0, y: 0, w: SLIDE_W, h: 0.12, fill: { color: '1F2937' } } },
      { text: { text: '브랜드 / 데크 제목', options: { x: M, y: 7.05, w: 6, h: 0.3, fontSize: 9, color: '6B7280', align: 'left' } } },
      { text: { text: '', options: { x: SLIDE_W - M - 1, y: 7.05, w: 1, h: 0.3, fontSize: 9, color: '6B7280', align: 'right' } } },
    ],
  });
  return master;
}
```

### 2.3 도형 좌표 조판 규칙 (마진·정렬)

- **마진**: 좌우·상하 최소 `0.6in`. 내용은 13.333×7.5 기준
  `(0.6, 0.6) ~ (12.733, 6.9)` 영역 안에 배치한다.
- **그리드**: x 좌표를 `0.6`부터 시작, 기본 증분 `0.1in` 단위로 정렬해
  픽셀 어긋남을 막는다.
- **표준 간격**: 섹션 타이틀 아래 본문까지 `0.35in`, 단락 간 `0.15in`.
- **도형 종횡비**: 카드/박스는 넘침 방지를 위해 **고정 높이를 주지
  않고** 내용 크기에 맞는 높이를 §4.2 공식으로 먼저 계산한다.

### 2.4 표(Table) 정렬 규칙

```javascript
slide.addTable(
  [
    [
      { text: '항목', options: { bold: true, color: 'FFFFFF', fill: { color: '1F2937' } } },
      { text: '수치', options: { bold: true, color: 'FFFFFF', fill: { color: '1F2937' } } },
    ],
    [{ text: '매출' }, { text: '1,240' }],
  ],
  { x: 0.6, y: 1.8, w: 12.1, colW: [6.0, 6.1], border: { type: 'solid', pt: 0.5, color: 'E5E7EB' }, fontSize: 12 }
);
```

- 헤더 행은 어두운 채움 + 흰 텍스트, 본문 행은 흰 채움으로 **위계**를
  준다.
- `colW` 합이 테이블 `w`와 정확히 일치해야 넘침이 없다.
- 셀 안 긴 문자열은 §4.2 공식으로 폭을 검증하고, 초과 시
  `fontSize` 축소 또는 줄바꿈(`\n` + `align`)으로 처리한다.

### 2.5 폰트 규칙 (한글·라틴 혼용)

- 한글 폰트는 굴림/맑은 고딕 계열 명시(렌더링 환경에 따라 폴백).
- 헤딩 28~32pt, 본문 14~18pt, 캡션 10~12pt, 슬라이드당 최소 3단계
  타이포 위계를 유지한다.
- 라벨 텍스트 폭 추정은 라틴 `6.5px/자`, CJK `13px/자`(13px 기준)
  공식을 사용한다. (SVG 스킬과 동일 계열 수치)

---

## 3. 시각 QA 루프 (LibreOffice 헤드리스 렌더링)

도메인 7 아키텍처를 그대로 따른다:

```text
[PptxGenJS 마스터 조판] ──▶ [.pptx 저장]
        │
        ▼
[soffice.exe 헤드리스] ──▶ [슬라이드별 고해상도 PNG]
        │
        ▼
[Vision QA 검수] ──(결함 발견)──▶ [좌표·크기 보정] ──▶ 재렌더링
        │ (통과)
        ▼
[산출물 확정]
```

### 3.1 렌더링 명령 (soffice.exe)

```bash
# PPTX → PDF (먼저 PDF로 변환해 페이지 구조 확인)
soffice --headless --convert-to pdf --outdir ./out ./deck.pptx

# PDF → PNG (pdftoppm 또는 ImageMagick 사용)
pdftoppm -png -r 150 ./out/deck.pdf ./out/slide
```

- 렌더링 해상도는 **150dpi 이상**으로 뽑아 Vision 검수 시 줄바꿈
  경계가 선명히 보이게 한다.
- Windows PowerShell에서는 `soffice.exe` 전체 경로를 사용한다
  (예: `& 'C:\Program Files\LibreOffice\program\soffice.exe' ...`).

### 3.2 슬라이드별 줄바꿈·텍스트 넘침 검증 체크리스트

렌더링된 PNG마다 아래를 대조한다. **슬라이드 1장이라도 실패하면 해당
슬라이드 좌표를 보정하고 재렌더링한다.**

- [ ] 헤딩·본문 텍스트가 박스/슬라이드 경계 밖으로 넘치지 않음
- [ ] 예상치 못한 줄바꿈(의도와 다른 단어 분리) 없음
- [ ] 텍스트가 도형/이미지/다른 박스와 겹치지 않음
- [ ] 표 셀 내용이 셀 경계를 넘치거나 잘리지 않음
- [ ] 하단 마진 영역에 텍스트가 침범하지 않음 (페이지 번호와 충돌)
- [ ] 한글 텍스트의 어센더/디센더 잘림 없음 (§3.3)
- [ ] 폰트가 깨지지 않고(□/䛖) 지정 계열로 렌더링됨

### 3.3 텍스트 잘림 확인용 규칙

- 슬라이드 하단에 닿는 텍스트(`y + h > 7.5 - 0.1`)는 잘림 위험 →
  박스 위로 이동하거나 폰트를 축소한다.
- 표 셀 높이보다 긴 내용은 행 높이를 키우거나 셀 내 줄바꿈을 명시한다.

---

## 4. 폴백: 수학적 좌표 검증 (soffice 미설치 시)

soffice.exe를 찾을 수 없어 실제 렌더링이 불가능하면, **수학적 텍스트
폭 추정**으로 좌표·넘침을 사전 검증하고 산출물에 **"시각 미검증"**
배지를 부착한다. 렌더링 검증을 건너뛰고 "완료"라고 말하면 안 된다.

### 4.1 텍스트 폭 추정 공식

```text
텍스트 폭(px) = (라틴/숫자 글자 수 × 6.5) + (한글/CJK 글자 수 × 13)
  ※ 13px 본문 기준. 실제 pt로 환산: px × (pt / 13)
  ※ 인치 환산: px ÷ 96 (1인치 = 96px @96dpi 기준)
```

슬라이드 13.333in 폭 = 1280px @96dpi. 도형 폭(px) - 텍스트 폭(px)이
좌우 여백으로 남아야 하며, **여백 합 ≥ 40px(각 20px)** 을 요구한다.

### 4.2 높이/줄 수 추정 공식

```text
줄 수 = ceil(텍스트 폭(px) / 도형 내부 폭(px))
필요 높이(px) = 줄 수 × (fontSize(px) × 1.35)
```

- 도형 높이는 `필요 높이 + 상하 패딩(≥ 12px)`으로 잡는다.
- 추정값이 슬라이드 높이(7.5in=720px)를 넘으면 콘텐츠를 분할하거나
  폰트를 줄인다.

### 4.3 시각 미검증 배지 (필수 부착)

soffice 미설치로 렌더링 검증을 하지 못한 경우, 결과물(파일/응답)에
아래 배지를 반드시 포함한다:

```text
⚠️ 시각 미검증(UNVERIFIED), LibreOffice 헤드리스 렌더링 없이
좌표 수학적 검증만 수행됨. 실제 렌더링 검수(줄바꿈·넘침) 전에는
이 산출물을 완성으로 간주하지 말 것.
```

---

## 5. PDF/DOCX 동일 루프 적용

오피스 산출물이 PPTX가 아니라 PDF/DOCX여도 동일한 시각 QA 루프를
적용한다.

| 산출물 | 생성 | 렌더링 검수 수단 |
|--------|------|------------------|
| PPTX | PptxGenJS | soffice → PNG (슬라이드 단위) |
| DOCX | docx 라이브러리 등 | soffice → PDF → PNG (페이지 단위) |
| PDF | 위 변환 결과 | `pdftoppm -png -r 150` 직접 |

- DOCX는 페이지 단위로 넘침을 검사한다. 표가 페이지를 넘어갈 때는
  행 분할 속성(`cantSplit` 등)을 명시한다.
- PDF는 생성 후 텍스트 선택이 되는지(깨진 글꼴 아님), 페이지 여백이
  유지되는지를 PNG 렌더로 확인한다.

---

## 6. 완료 선언 최종 체크리스트

- [ ] 슬라이드/페이지 수, 마스터, 좌표, 표 정렬이 의도와 일치
- [ ] 시각 QA 루프(§3) 통과 OR 수학적 검증(§4) + "시각 미검증" 배지
- [ ] 슬라이드별 줄바꿈·넘침 0건 (렌더링 검증 시)
- [ ] 한글/라틴 폰트 깨짐 없음
- [ ] 사용된 검증 수단이 무엇인지 기록됨 (soffice 렌더링 / 수학 검증)

> 검증 수단을 기록하지 않은 완료 선언은 거짓 완료로 간주한다.

---

## 7. 관련 스킬 및 레퍼런스

- [`design-core-harness`](../design-core-harness/SKILL.md), 시각 검증
  루프·4대 결함 체크리스트 (본 스킬의 상위 하네스)
- [`design-vector-svg`](../design-vector-svg/SKILL.md), 도식이 필요하면
  SVG로 먼저 검증 후 임베드
- [`frontend-design`](../frontend-design/SKILL.md), 색상·타이포 방향 결정 참고
