# Adaptive Responsive Statusline Terminal Layout Implementation Report

**Document ID:** AGY-RESPONSIVE-STATUSLINE-001  
**Target Environment:** Antigravity CLI Statusline & Dashboard Telemetry  
**Author:** Senior Full-Stack & CLI Systems Engineer  
**Date:** 2026-10-07  
**Branch:** `dashboard`  

---

## 1. Executive Summary & Problem Diagnosis

### 1.1 Context & Problem Statement
In Antigravity and modern CLI workflows, the real-time statusline badge renders turn cost, daily consumption, cache hit rate, and live 5h/7d Gemini API quotas:
```text
⚡ Turn: 564 ($0.0003) | Today: 123.1k ($0.047) | Cache: 89% | 5h: ▰▰▰▰▰ 97%! (4h 44m) | 7d: ▰▰▰▱▱ 61%! (9h 25m) | 📊 Dashboard
```

When rendered in its full visual format, the statusline consumes approximately **135–138 visible columns**. In split terminal panes (e.g., 80–100 columns), narrow editor viewports, or constrained tmux panes, the full layout causes awkward terminal line wrapping or horizontal overflow.

### 1.2 User Specification & Constraints
The user explicitly defined the desired UX transformation:
> *"터미널 창 폭에 따라, 자동으로 레이아웃이 반응형으로 변형되는게 좋아. 그렇게 해줘. 다만 초소형 말고, 압축까지만!"*  
> *(Automatically transform the layout responsively according to terminal window width. However, not ultra-minimal, only up to compact!)*

Key architectural boundaries:
1. **Strict 2-Tier Hierarchy**: Wide (Full) Mode vs. Compact Mode.
2. **Zero Cryptic Abbreviations**: Do **not** degrade to unreadable ultra-minimal shorthand such as `T:`, `Tod:`, `C:`. Maintain full recognizable metric labels (`Turn:`, `Today:`, `Cache:`, `5h:`, `7d:`), localized labels, token counts, costs, and staleness markers (`!`, `*`).
3. **Multi-Source Terminal Width Detection**: Reliable width resolution across interactive TTYs, child processes with piped stdio (hook execution), and Windows/POSIX consoles.
4. **Zero External Dependencies**: Built-in Node.js modules (`node:fs`, `node:tty`) only.
5. **High-Performance Budget**: <10ms execution budget (<0.1ms measured).
6. **Zero Regressions**: 100% backward compatibility with all prior test contracts (REQ-1a through REQ-1h, REQ-2, REQ-3, REQ-4).

---

## 2. Technical Architecture & Design Decisions

### 2.1 2-Tier Responsive Layout Engine

```mermaid
flowchart TD
    A["resolveBadgeWidth()"] --> B{"width >= 115 cols<br/>or unconstrained?"}
    B -- Yes --> C["Wide (Full) Mode<br/>• Mini progress bars (▰▰▰▰▰)<br/>• Reset countdown timers (4h 44m, 9h 25m)<br/>• Visible width: ~135 cols"]
    B -- No --> D["Compact Mode (width < 115 cols)<br/>• Omits mini-bars & reset countdowns<br/>• Preserves Turn:, Today:, Cache:, 5h:, 7d:<br/>• Preserves ⚡ emoji, costs & staleness markers<br/>• Visible width: ~95-104 cols"]
    C --> E{"Exceeds width?"}
    D --> E
    E -- No --> F["Single-Line Badge Output"]
    E -- Yes --> G["wrapBadgeSegments()<br/>Clean 2-line split at ' | ' boundaries<br/>(Line 1 <= width, Line 2 <= width)"]
```

#### Comparison of the Two Tiers

| Tier | Trigger Condition | 5h/7d Quota Format | Visible Width | Typical Display |
| :--- | :--- | :--- | :--- | :--- |
| **Wide (Full)** | `width >= 115` or unconstrained | `5h: ▰▰▰▰▰ 97%! (4h 44m)`<br/>`7d: ▰▰▰▱▱ 61%! (9h 25m)` | ~135–138 cols | Single line on wide terminals (>=135) or clean 2-line wrap |
| **Compact** | `width < 115` | `5h: 97%!`<br/>`7d: 61%!` | ~95–104 cols | Single line on medium terminals (100–114) or clean 2-line wrap on narrow (75–80) |

By omitting the progress bar (`▰▰▰▰▰ ` = 6 cols) and reset countdown (` (4h 44m)` = 10 cols) per quota metric, Compact mode eliminates **~32 columns** of auxiliary characters while retaining 100% of the operational signal.

### 2.2 Multi-Channel Terminal Width Resolution

When Antigravity invokes `agy-tokens --hook --raw` as a post-invocation hook, `process.stdout` and `process.stdin` are standard OS pipes (`process.stdout.isTTY` is undefined or false). Standard `stdout.columns` queries would return `undefined` and fall back blindly.

To solve this, `resolveBadgeWidth()` implements a multi-channel priority hierarchy:

1. **Explicit Environment (`process.env.COLUMNS`)**: High-priority override set by terminal emulators, testing fixtures, or manual overrides.
2. **Standard Output TTY (`process.stdout.columns`)**: Primary channel when invoked in an interactive terminal.
3. **Explicit Non-TTY Guard (`process.stdout.isTTY === false`)**: Contractual guard preserving unit test contract `REQ-1f` / `REQ-1g` when output redirection is intentional.
4. **Standard Error TTY (`process.stderr.columns`)**: Inherited TTY handle frequently available when stdout is piped.
5. **Standard Input TTY (`process.stdin.columns`)**: Inherited TTY handle for input.
6. **Direct Console Device Probe (`probeConsoleDeviceColumns()`)**:
   - Windows: `\\\\.\\CONOUT$` via `fs.openSync` and `tty.WriteStream(fd)`.
   - POSIX: `/dev/tty` via `fs.openSync` and `tty.WriteStream(fd)`.
   - Queries the active console screen buffer without modifying stdout/stderr streams.
7. **Safe Fallback**: `Infinity` (defaults to unconstrained single-line layout).

---

## 3. Implementation Details

### 3.1 Changes in `src/formatter.js`

1. **Imports**: Added native `fs` and `tty` modules.
2. **`probeConsoleDeviceColumns()`**:
   - Opens the system console device (`\\\\.\\CONOUT$` on Windows, `/dev/tty` on POSIX).
   - Extracts `.columns` via a lightweight `tty.WriteStream`.
   - Guaranteed atomic resource cleanup via `try / finally { fs.closeSync(fd); }`.
3. **`resolveBadgeWidth()`**:
   - Implemented the 7-stage resolution hierarchy.
4. **`renderRealTimeBadge(badgeData, currencyCode, isFree, link, options)`**:
   - Added `options` parameter supporting `{ width, mode }` or numeric shorthand `options: number`.
   - Computes `isCompact`:
     ```javascript
     const opt = typeof options === 'number' ? { width: options } : (options || {});
     const resolvedWidth = Number.isFinite(opt.width) ? opt.width : resolveBadgeWidth();
     const mode = opt.mode || 'auto';
     const isCompact = mode === 'compact' || (mode !== 'wide' && Number.isFinite(resolvedWidth) && resolvedWidth < 115);
     ```
   - In Wide mode: formats mini progress bars and reset countdown strings.
   - In Compact mode: formats clean percentages and staleness markers without progress bars or countdown strings.
   - Preserves all core segments (`⚡ Turn: ...`, `Today: ...`, `Cache: ...`, `📊 Dashboard`).
   - Routes final segments to `wrapBadgeSegments(segments, resolvedWidth)`.
5. **Exports**: Exported `probeConsoleDeviceColumns` in `module.exports` for diagnostic testability.

### 3.2 Test Suite Expansion in `test/run-tests.js`

Added **Suite 31: Adaptive Responsive Statusline Terminal Layout** (6 tests):
- `Wide (Full) Mode is rendered when width >= 115 columns (has mini-bar ▰ and countdown timer)`
- `Compact Mode is rendered when width < 115 columns (omits mini-bar and countdown, preserves full labels)`
- `Compact Mode preserves ⚡ emoji, cost metrics, and staleness markers`
- `Statusline wraps into at most 2 physical lines at width 75–80 without clipping any segment`
- `resolveBadgeWidth multi-fallback hierarchy and probeConsoleDeviceColumns`
- `renderRealTimeBadge supports explicit options overrides (mode and width)`

---

## 4. Verification & Benchmarks

### 4.1 CLI Output Verification

Executed `node bin/agy-tokens.js --hook --raw` across three terminal widths:

#### 1. Wide Mode (`COLUMNS=200`)
```text
⚡ Turn: 361 ($0.0002) | Today: 5.56M ($0.621) | Cache: 99% | 5h: ▰▰▰▰▰ 92% (4h 38m) | 7d: ▰▰▰▰▰ 99% (6d 23h) | 📊 Dashboard
```
*Result:* Single line, full progress bars and countdowns visible.

#### 2. Compact Mode (`COLUMNS=100`)
```text
⚡ Turn: 361 ($0.0002) | Today: 5.56M ($0.621) | Cache: 99% | 5h: 92% | 7d: 99% | 📊 Dashboard
```
*Result:* Single line, 97 visible columns, fits cleanly within 100 columns without wrapping.

#### 3. Compact 2-Line Wrapped Mode (`COLUMNS=75`)
```text
⚡ Turn: 361 ($0.0002) | Today: 5.56M ($0.621) | Cache: 99% | 5h: 92%
7d: 99% | 📊 Dashboard
```
*Result:* Exactly 2 physical lines. Line 1: 72 columns (<=75). Line 2: 24 columns (<=75). Zero truncation, zero clipped characters.

### 4.2 Test Suite Execution

```text
=======================================================
  Tests: 271 passed, 0 failed, 271 total
  Duration: 8883ms
=======================================================
```
- **Total Test Suites:** 31
- **Passed:** 271 / 271 (100%)
- **Regressions:** 0

### 4.3 Performance Benchmark
- Console device probe average execution time: **0.0905 ms** (<0.1ms).
- Budget: <10ms.
- Efficiency margin: >99% under budget.

---

## 5. Summary of Files Changed

| File Path | Description of Changes |
| :--- | :--- |
| `src/formatter.js` | Added `fs`/`tty` imports, implemented `probeConsoleDeviceColumns()`, enhanced `resolveBadgeWidth()` multi-fallback, and added 2-tier responsive rendering in `renderRealTimeBadge()`. |
| `test/run-tests.js` | Added guarded test environment for live quota countdown test, added Suite 31 with 6 unit tests covering responsive tiers, boundaries, and multi-channel detection. |
| `reports/responsive-statusline-report.md` | Comprehensive architectural diagnosis, design specifications, benchmark results, and verification records. |
