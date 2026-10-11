#!/usr/bin/env node
/**
 * visual_qa.js ,  Design Core Harness 스크린샷 캡처 실행기 (단일 파일)
 * ------------------------------------------------------------------
 * 헤드리스 Chromium(Playwright)으로 로컬 HTML을 전체 페이지 스크린샷으로
 * 캡처하고, 콘솔/페이지 에러를 수집해 시각 검증 루프의 ① 렌더링 단계를
 * 수행한다. 클릭·입력·스크롤 흐름은 --flow로 위임한다.
 *
 * 사용법:
 *   node visual_qa.js <html_path> [out_dir] [--flow flow.json]
 *
 *   html_path : 렌더링할 로컬 HTML 파일 경로 (필수)
 *   out_dir   : 스크린샷 저장 디렉토리 (선택, 기본 ./visual_qa_out)
 *   --flow    : 클릭 흐름 정의 JSON (선택). 지정 시 같은 폴드의
 *               visual_qa.py(Python Playwright)로 자동 위임한다
 *
 * 종료 코드:
 *   0  = 스크린샷 캡처 성공 (console error 여부와 무관, 결과에 보고됨)
 *   1  = 브라우저 실행/캡처 실패 (치명적)
 *   2  = 인자 오류 또는 Playwright(Node·Python) 미설치
 *
 * Playwright 미설치 시:
 *   Node Playwright 로드 실패면 같은 폴드의 visual_qa.py로 자동 폰백을
 *   시도하고, 둘 다 없을 때 아래 안내를 출력한 뒤 exit 2로 종료한다.
 *   → 폰백: 헤드리스 스크린샷 PNG를 ReadMediaFile로 읽거나,
 *     렌더링 도구가 전무하면 "시각 미검증(UNVERIFIED)" 배지로 강등한다.
 *     사용자 화면에 창을 띄우는 캡처는 금지(AGENTS.md §10).
 *
 * 설계 원칙:
 *   - Playwright는 peer 의존성. hard dependency를 만들지 않기 위해
 *     require가 아닌 dynamic import()로 로드한다. 미설치여도 스크립트가
 *     require 단계에서 죽지 않고 명확한 안내 후 종료한다.
 *   - 로컬 파일만 다룬다 (file:// URL). 외부 네트워크 리소스는
 *     웹 폰트 등 렌더링에 필요한 경우에만 허용된다.
 */

'use strict';

const path = require('path');
const fs = require('fs');
const { spawnSync } = require('child_process');
const { pathToFileURL } = require('url');

const EXIT_OK = 0;
const EXIT_CAPTURE_FAIL = 1;
const EXIT_USAGE_OR_NO_PLAYWRIGHT = 2;

const FALLBACK_GUIDE = [
  '',
  '  [Playwright 미설치 ,  폰백 안내]',
  '  visual_qa.js는 Playwright를 peer 의존성으로 사용합니다.',
  '  Node Playwright 로드 실패 시 같은 폴드의 visual_qa.py',
  '  (Python Playwright)로 자동 폰백을 먼저 시도합니다.',
  '',
  '  둘 다 없을 때 설치 방법:',
  '    Node:   npm install --no-save playwright',
  '            (또는: npx playwright install chromium)',
  '    Python: pip install playwright',
  '            (그 뒤: python -m playwright install chromium)',
  '',
  '  설치 없이 진행해야 한다면 아래 순서로 폰백하십시오:',
  '    1순위: 헤드리스로 만든 스크린샷 PNG를 ReadMediaFile로 읽기',
  '           (사용자 화면에 창을 띄워 캡처하는 방식은 금지, AGENTS.md §10)',
  '    2순위: 도구 전무 시 순수 명세 대조로 강등하고 산출물에',
  '           "시각 미검증(UNVERIFIED)" 배지를 반드시 부착',
  '',
].join('\n');

function printUsage() {
  console.error(
    [
      '사용법: node visual_qa.js <html_path> [out_dir] [--flow flow.json]',
      '',
      '  html_path : 렌더링할 로컬 HTML 파일 경로 (필수)',
      '  out_dir   : 스크린샷 저장 디렉토리 (선택, 기본 ./visual_qa_out)',
      '  --flow    : 클릭 흐름 정의 JSON (선택, visual_qa.py로 위임)',
      '',
      '예:',
      '  node visual_qa.js ./preview.html',
      '  node visual_qa.js ./preview.html ./shots',
      '  node visual_qa.js ./preview.html ./shots --flow flow.json',
    ].join('\n')
  );
}

function sanitizeFileName(name) {
  // 경로 분리자/위험 문자를 제거해 파일명으로 안전하게 만든다.
  return name.replace(/[\\/:*?"<>|]/g, '_').replace(/\s+/g, '_');
}

// Node Playwright이 없거나 --flow가 지정되면 같은 폴드의 visual_qa.py로 위임한다.
function runViaPython(htmlPath, outDir, flowPath) {
  const pyScript = path.join(__dirname, 'visual_qa.py');
  if (!fs.existsSync(pyScript)) {
    console.error('[visual_qa] visual_qa.py 실행기를 찾을 수 없음');
    console.error(FALLBACK_GUIDE);
    return EXIT_USAGE_OR_NO_PLAYWRIGHT;
  }
  const pyArgs = [pyScript, htmlPath, outDir];
  if (flowPath) {
    pyArgs.push('--flow', path.resolve(flowPath));
  }
  const candidates = process.platform === 'win32'
    ? [['python', pyArgs], ['py', ['-3', ...pyArgs]]]
    : [['python3', pyArgs], ['python', pyArgs]];
  for (const [cmd, cargs] of candidates) {
    const result = spawnSync(cmd, cargs, { stdio: 'inherit' });
    if (result.error) {
      if (result.error.code === 'ENOENT') continue; // 다음 후보 시도
      console.error(`[visual_qa] Python 실행기 오류: ${result.error.message}`);
      return EXIT_CAPTURE_FAIL;
    }
    return result.status === null ? EXIT_CAPTURE_FAIL : result.status;
  }
  console.error('[visual_qa] Node·Python Playwright 모두 없음');
  console.error(FALLBACK_GUIDE);
  return EXIT_USAGE_OR_NO_PLAYWRIGHT;
}

async function main() {
  const args = process.argv.slice(2);

  if (args.length < 1 || args[0].startsWith('--')) {
    printUsage();
    console.error(FALLBACK_GUIDE);
    return EXIT_USAGE_OR_NO_PLAYWRIGHT;
  }

  const htmlPath = path.resolve(args[0]);
  let outDir = path.resolve('./visual_qa_out');
  let flowPath = null;
  for (let i = 1; i < args.length; i++) {
    if (args[i] === '--flow') {
      flowPath = args[i + 1] || null;
      i++;
    } else if (!args[i].startsWith('--')) {
      outDir = path.resolve(args[i]);
    }
  }

  if (!fs.existsSync(htmlPath)) {
    console.error(`[visual_qa] HTML 파일을 찾을 수 없음: ${htmlPath}`);
    printUsage();
    return EXIT_USAGE_OR_NO_PLAYWRIGHT;
  }

  if (!/\.html?$/i.test(htmlPath)) {
    console.error(`[visual_qa] 대상이 .html 파일이 아님: ${htmlPath}`);
    printUsage();
    return EXIT_USAGE_OR_NO_PLAYWRIGHT;
  }

  // --flow는 Python 실행기가 담당 (단계별 클릭/스크린샷)
  if (flowPath) {
    return runViaPython(htmlPath, outDir, flowPath);
  }

  // ── 1) Playwright 동적 로드 (peer 의존성) ────────────────────────────
  let playwright;
  try {
    playwright = await import('playwright');
  } catch (err) {
    console.error('[visual_qa] Node Playwright 로드 실패, Python 실행기로 폰백:');
    console.error(`  ${err.message}`);
    return runViaPython(htmlPath, outDir, null);
  }

  fs.mkdirSync(outDir, { recursive: true });

  const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  const baseName = sanitizeFileName(path.basename(htmlPath, path.extname(htmlPath)));
  const shotPath = path.join(outDir, `${baseName}_${stamp}.png`);
  const reportPath = path.join(outDir, `${baseName}_${stamp}.json`);

  const consoleIssues = []; // console.error / warning
  const pageErrors = []; // uncaught exceptions

  let browser;
  try {
    // ── 2) 브라우저 기동 ───────────────────────────────────────────────
    browser = await playwright.chromium.launch({ headless: true });
    const page = await browser.newPage({
      viewport: { width: 1280, height: 800 },
      deviceScaleFactor: 2, // 2x 캡처 → Vision 대조 해상도 확보
    });

    // ── 3) 콘솔/페이지 에러 수집 ───────────────────────────────────────
    //    "No console errors is the floor, not the finish line" , 
    //    에러는 보고하되, 캡처 성공 여부와 분리해 판정한다.
    page.on('console', (msg) => {
      if (msg.type() === 'error' || msg.type() === 'warning') {
        consoleIssues.push(`[${msg.type()}] ${msg.text()}`);
      }
    });
    page.on('pageerror', (err) => {
      pageErrors.push(err.message);
    });

    // ── 4) 렌더링 (① 단계) ─────────────────────────────────────────────
    const fileUrl = pathToFileURL(htmlPath).href;
    await page.goto(fileUrl, { waitUntil: 'load', timeout: 30000 });

    // 폰트/이미지 로딩이 늦게 끝나는 경우를 위해 추가 대기
    await page.evaluate(() => {
      return document.fonts ? document.fonts.ready : Promise.resolve();
    });
    await page.waitForTimeout(500);

    // ── 5) 전체 페이지 스크린샷 캡처 ───────────────────────────────────
    const dims = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      scrollHeight: document.documentElement.scrollHeight,
      clientWidth: document.documentElement.clientWidth,
      clientHeight: document.documentElement.clientHeight,
      title: document.title || '(no title)',
    }));

    await page.screenshot({ path: shotPath, fullPage: true });

    // ── 6) 결과 리포트 (JSON) ──────────────────────────────────────────
    const report = {
      tool: 'visual_qa.js',
      capturedAt: new Date().toISOString(),
      htmlPath,
      screenshotPath: shotPath,
      viewport: { width: 1280, height: 800, deviceScaleFactor: 2 },
      document: dims,
      consoleIssues,
      pageErrors,
      verdict: {
        floor: consoleIssues.length === 0 && pageErrors.length === 0,
        note:
          'console error 부재는 "floor"일 뿐. 4대 시각 결함 및 슬롭 ' +
          '블랙리스트 대조는 Vision 검사로 별도 수행할 것.',
      },
    };
    fs.writeFileSync(reportPath, JSON.stringify(report, null, 2), 'utf8');

    // ── 7) 콘솔 요약 ───────────────────────────────────────────────────
    console.log('[visual_qa] 캡처 완료');
    console.log(`  대상      : ${htmlPath}`);
    console.log(`  문서 제목 : ${dims.title}`);
    console.log(`  문서 크기 : ${dims.scrollWidth} x ${dims.scrollHeight}px`);
    console.log(`  스크린샷  : ${shotPath}`);
    console.log(`  리포트   : ${reportPath}`);
    if (consoleIssues.length > 0 || pageErrors.length > 0) {
      console.log(`  ⚠ console/page 에러 ${consoleIssues.length + pageErrors.length}건 (floor 미달)`);
      consoleIssues.forEach((m) => console.log(`    - ${m}`));
      pageErrors.forEach((m) => console.log(`    - [pageerror] ${m}`));
    } else {
      console.log('  console/page 에러 0건 (floor 충족 ,  finish line 아님)');
    }
    return EXIT_OK;
  } catch (err) {
    console.error('[visual_qa] 캡처 실패:');
    console.error(`  ${err.message}`);
    if (err.message && /Executable doesn't exist|browserType\.launch/.test(err.message)) {
      console.error(
        '  Chromium 실행 파일이 없습니다. 실행: npx playwright install chromium'
      );
    }
    console.error(FALLBACK_GUIDE);
    return EXIT_CAPTURE_FAIL;
  } finally {
    if (browser) {
      await browser.close().catch(() => {});
    }
  }
}

main().then((code) => {
  process.exitCode = code;
});
