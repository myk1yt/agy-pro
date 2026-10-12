#!/usr/bin/env python3
"""
visual_qa.py ,  Design Core Harness 헤드리스 캡처/클릭 흐름 실행기 (단일 파일)
----------------------------------------------------------------------
헤드리스 Chromium(Python Playwright)으로 로컬 HTML을 렌더링해
  1) 기본 모드: 전체 페이지 스크린샷 + 콘솔/페이지 에러 수집
  2) 흐름 모드(--flow): 클릭/입력/스크롤/대기 단계를 헤드리스로 수행하면서
     단계별 스크린샷을 남긴다

visual_qa.js의 Node Playwright가 없는 환경에서 js가 자동으로 호출하는
폰백 실행기이며, 단독으로도 사용할 수 있다.

사용법:
  python visual_qa.py <html_path> [out_dir] [--flow flow.json]

  html_path : 렌더링할 로컬 HTML 파일 경로 (필수)
  out_dir   : 스크린샷 저장 디렉토리 (선택, 기본 ./visual_qa_out)
  --flow    : 클릭 흐름 정의 JSON (선택)

종료 코드:
  0  = 캡처(흐름) 성공 (console error 여부와 무관, 결과에 보고됨)
  1  = 브라우저 실행/캡처/단계 실패 (치명적)
  2  = 인자 오류 또는 Python Playwright 미설치

flow.json 스키마:
  {
    "steps": [
      {"action": "click",  "selector": "#btn"},          # CSS 선택자 클릭
      {"action": "type",   "selector": "#in", "text": "hello"},
      {"action": "scroll", "y": 800},                   # px 단위 휠 스크롤
      {"action": "wait",   "ms": 500},
      {"action": "shot",   "name": "after-click"},      # 이름 붙은 단계 스크린샷
      {"action": "goto",   "url": "https://..."}        # 임의 URL (file:// 포함)
    ]
  }
  각 단계는 실행 직후 자동으로 단계 스크린샷을 저장한다(shot 생략 시에도).

설계 원칙:
  - headless=True 고정. 사용자 화면에 창을 띄우는 옵션은 의도적으로 없다
    (AGENTS.md §10 Headless verification).
  - 로컬 파일만 기본 다룬다 (file:// URL).
"""

import argparse
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

EXIT_OK = 0
EXIT_CAPTURE_FAIL = 1
EXIT_USAGE_OR_NO_PLAYWRIGHT = 2

FALLBACK_GUIDE = """
  [Python Playwright 미설치 ,  폰백 안내]
  설치:  pip install playwright
         (그 뒤: python -m playwright install chromium)

  설치 없이 진행해야 한다면 아래 순서로 폰백하십시오:
    1순위: 헤드리스로 만든 스크린샷 PNG를 ReadMediaFile로 읽기
           (사용자 화면에 창을 띄워 캡처하는 방식은 금지, AGENTS.md §10)
    2순위: 도구 전무 시 순수 명세 대조로 강등하고 산출물에
           "시각 미검증(UNVERIFIED)" 배지를 반드시 부착
"""


def sanitize_file_name(name: str) -> str:
    # 경로 분리자/위험 문자를 제거해 파일명으로 안전하게 만든다.
    return re.sub(r"\s+", "_", re.sub(r'[\\/:*?"<>|]', "_", name))


def print_usage() -> None:
    sys.stderr.write(
        "사용법: python visual_qa.py <html_path> [out_dir] [--flow flow.json]\n"
        "\n"
        "  html_path : 렌더링할 로컬 HTML 파일 경로 (필수)\n"
        "  out_dir   : 스크린샷 저장 디렉토리 (선택, 기본 ./visual_qa_out)\n"
        "  --flow    : 클릭 흐름 정의 JSON (선택)\n"
        "\n"
        "예:\n"
        "  python visual_qa.py ./preview.html\n"
        "  python visual_qa.py ./preview.html ./shots --flow flow.json\n"
    )


def parse_args(argv):
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("html_path", nargs="?")
    parser.add_argument("out_dir", nargs="?", default=None)
    parser.add_argument("--flow", default=None)
    parser.add_argument("-h", "--help", action="store_true")
    return parser.parse_args(argv)


def run_step(page, step: dict) -> None:
    action = step.get("action")
    if action == "click":
        page.click(step["selector"], timeout=10000)
    elif action == "type":
        page.fill(step["selector"], step.get("text", ""), timeout=10000)
    elif action == "scroll":
        page.mouse.wheel(step.get("x", 0), step.get("y", 0))
    elif action == "wait":
        page.wait_for_timeout(int(step.get("ms", 500)))
    elif action == "shot":
        pass  # 스크린샷은 공통 후처리로 수행
    elif action == "goto":
        page.goto(step["url"], wait_until="load", timeout=30000)
    else:
        raise ValueError(f"알 수 없는 action: {action}")


def main() -> int:
    args = parse_args(sys.argv[1:])
    if args.help or not args.html_path:
        print_usage()
        sys.stderr.write(FALLBACK_GUIDE)
        return EXIT_USAGE_OR_NO_PLAYWRIGHT

    html_path = Path(args.html_path).resolve()
    out_dir = (
        Path(args.out_dir).resolve()
        if args.out_dir
        else Path("./visual_qa_out").resolve()
    )

    if not html_path.exists():
        sys.stderr.write(f"[visual_qa] HTML 파일을 찾을 수 없음: {html_path}\n")
        print_usage()
        return EXIT_USAGE_OR_NO_PLAYWRIGHT

    if not re.search(r"\.html?$", html_path.name, re.IGNORECASE):
        sys.stderr.write(f"[visual_qa] 대상이 .html 파일이 아님: {html_path}\n")
        print_usage()
        return EXIT_USAGE_OR_NO_PLAYWRIGHT

    flow = None
    if args.flow:
        try:
            flow = json.loads(Path(args.flow).read_text(encoding="utf-8"))
            if not isinstance(flow, dict) or not isinstance(flow.get("steps"), list):
                raise ValueError('"steps" 배열이 필요합니다')
        except Exception as err:  # JSONDecodeError 포함
            sys.stderr.write(f"[visual_qa] flow JSON 로드 실패: {err}\n")
            return EXIT_USAGE_OR_NO_PLAYWRIGHT

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        sys.stderr.write("[visual_qa] Python Playwright 로드 실패:\n")
        sys.stderr.write(FALLBACK_GUIDE)
        return EXIT_USAGE_OR_NO_PLAYWRIGHT

    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")
    base = sanitize_file_name(html_path.stem)
    shot_path = out_dir / f"{base}_{stamp}.png"
    report_path = out_dir / f"{base}_{stamp}.json"

    console_issues = []  # console.error / warning
    page_errors = []  # uncaught exceptions
    steps_report = []
    failed_steps = 0

    def on_console(msg):
        if msg.type in ("error", "warning"):
            console_issues.append(f"[{msg.type}] {msg.text}")

    def on_page_error(err):
        page_errors.append(str(err))

    try:
        with sync_playwright() as p:
            # headless 고정: 사용자 화면에 창을 띄우는 옵션은 제공하지 않는다
            browser = p.chromium.launch(headless=True)
            page = browser.new_page(
                viewport={"width": 1280, "height": 800},
                device_scale_factor=2,  # 2x 캡처 → Vision 대조 해상도 확보
            )
            page.on("console", on_console)
            page.on("pageerror", on_page_error)

            page.goto(html_path.as_uri(), wait_until="load", timeout=30000)
            page.evaluate(
                "() => (document.fonts ? document.fonts.ready : Promise.resolve())"
            )
            page.wait_for_timeout(500)

            dims = page.evaluate(
                """() => ({
                    scrollWidth: document.documentElement.scrollWidth,
                    scrollHeight: document.documentElement.scrollHeight,
                    clientWidth: document.documentElement.clientWidth,
                    clientHeight: document.documentElement.clientHeight,
                    title: document.title || '(no title)'
                })"""
            )
            page.screenshot(path=str(shot_path), full_page=True)

            if flow:
                for idx, step in enumerate(flow["steps"], start=1):
                    name = sanitize_file_name(str(step.get("name") or step.get("action") or f"step{idx}"))
                    step_shot = out_dir / f"{base}_{idx:02d}_{name}.png"
                    rec = {"index": idx, "action": step.get("action"), "screenshot": str(step_shot)}
                    try:
                        run_step(page, step)
                        page.wait_for_timeout(150)
                        page.screenshot(path=str(step_shot), full_page=False)
                        rec["ok"] = True
                    except Exception as err:
                        rec["ok"] = False
                        rec["error"] = str(err)
                        failed_steps += 1
                    steps_report.append(rec)

            browser.close()
    except Exception as err:
        sys.stderr.write(f"[visual_qa] 캡처 실패:\n  {err}\n")
        sys.stderr.write(FALLBACK_GUIDE)
        return EXIT_CAPTURE_FAIL

    report = {
        "tool": "visual_qa.py",
        "capturedAt": datetime.now(timezone.utc).isoformat(),
        "htmlPath": str(html_path),
        "screenshotPath": str(shot_path),
        "viewport": {"width": 1280, "height": 800, "deviceScaleFactor": 2},
        "document": dims,
        "consoleIssues": console_issues,
        "pageErrors": page_errors,
        "steps": steps_report,
        "failedSteps": failed_steps,
        "verdict": {
            "floor": len(console_issues) == 0 and len(page_errors) == 0,
            "note": (
                'console error 부재는 "floor"일 뿐. 4대 시각 결함 및 슬롭 '
                "블랙리스트 대조는 Vision 검사로 별도 수행할 것."
            ),
        },
    }
    report_path.write_text(
        json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    print("[visual_qa] 캡처 완료")
    print(f"  대상      : {html_path}")
    print(f"  문서 제목 : {dims['title']}")
    print(f"  문서 크기 : {dims['scrollWidth']} x {dims['scrollHeight']}px")
    print(f"  스크린샷  : {shot_path}")
    print(f"  리포트   : {report_path}")
    if flow:
        print(f"  흐름 단계 : {len(steps_report)}개 (실패 {failed_steps}개)")
    if console_issues or page_errors:
        print(f"  ⚠ console/page 에러 {len(console_issues) + len(page_errors)}건 (floor 미달)")
        for m in console_issues:
            print(f"    - {m}")
        for m in page_errors:
            print(f"    - [pageerror] {m}")
    else:
        print("  console/page 에러 0건 (floor 충족 ,  finish line 아님)")

    return EXIT_CAPTURE_FAIL if failed_steps else EXIT_OK


if __name__ == "__main__":
    sys.exit(main())
