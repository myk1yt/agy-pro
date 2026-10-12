# Antigravity Statusline 5h & 7d Real-Time Metric Stagnation Fix Report

- **Date**: 2026-10-07
- **Target Component**: `Antigravity-cli` statusline badge & quota telemetry (`src/gemini-quota.js`, `src/hook-handler.js`, `src/index.js`, `test/run-tests.js`)
- **Status**: Verified & Fixed (Zero Regressions, 256/256 Tests Passing)

---

## 1. Problem Statement

In the Antigravity-cli statusline badge:
```text
Turn: 564 ($0.0003) | Today: 123.1k ($0.047) | Cache: 89% | 5h: ▰▰▰▰▰ 97%! (4h 44m) | 7d: ▰▰▰▱▱ 61%! (9h 25m) | 📊 Dashboard
```

While `Turn`, `Today`, and `Cache` metrics updated normally on each turn, `5h` and `7d` quota metrics remained frozen at stale snapshot values with an exclamation mark (`97%!`, `61%!`).

---

## 2. Root Cause Analysis

Investigation traced the entire statusline and Language Server quota refresh pipeline, identifying three intertwined root causes:

### Root Cause 1: CSRF Token Disconnect in Background Refresh Subprocesses
- The Go Language Server (`agy.exe`) listens on HTTPS (e.g. port 57994) and requires the `X-Codeium-Csrf-Token` request header for `/RetrieveUserQuotaSummary` and `/GetUserStatus`.
- When Antigravity CLI runs `statusLine.command` (`agy-tokens --hook --raw --write-dashboard`), it executes in a subprocess environment where `process.env.ANTIGRAVITY_CSRF_TOKEN` is not exported by the host shell.
- When `triggerBackgroundQuotaRefresh()` spawned the detached background Node process to query the Language Server, it did not forward any CSRF token.
- As a result, the background probe sent an unauthenticated request, received HTTP 401 ("missing CSRF token"), recorded `lastError: { kind: 'auth_failure', detail: 'HTTP 401 (missing CSRF token)' }` into `~/.gemini/gemini_quota_cache.json`, and wrote `~/.gemini/gemini_quota_probe_cooldown.json` (locking retries out for 10 minutes).
- This permanently trapped the cache in an `auth_failure` state, appending the `!` marker to statusline renders.

### Root Cause 2: Missing Real-Time Turn-Level Deductions on Cached Snapshots
- Even when a valid quota snapshot existed in `gemini_quota_cache.json` (`quota5h.remainPercent: 97`, `quota7d.remainPercent: 61`), `getCachedGeminiQuota()` only updated countdown strings (`resetFormatted`) from `resetTime`.
- It never deducted tokens consumed in subsequent turns since `timestampMs`. If a user executed turns consuming hundreds of thousands of tokens between Language Server polls (or while offline), the displayed percentages remained completely frozen.
- Furthermore, if `resetTime` elapsed (`Date.now() >= resetTime`), the bucket percentage was never replenished to 100%.

### Root Cause 3: Stale Cache Formatter Precedence Lockout
- In `src/formatter.js`, `renderRealTimeBadge()` prioritizes an existing real quota snapshot over rolling window estimates (Priority 4 over Priority 5, per Design 000815 §4.3/§4.4) and appends the `!` marker when `lastError.kind === 'auth_failure'`.
- Because `gemini_quota_cache.json` persisted indefinitely on disk with `lastError: { kind: 'auth_failure' }`, the statusline was permanently stuck displaying the stale snapshot with `!`.

---

## 3. Implemented Solutions

All changes strictly adhere to **Node.js built-in modules only (Zero External Dependencies)**, execute in **<10ms**, and employ **robust atomic file I/O**.

### A. PID-Bound CSRF Token Persistence & Multi-Channel Recovery (`src/gemini-quota.js`)
1. **Persistent Token Store (`.gemini_quota_ls_token.json`)**:
   - Implemented `savePersistedLsToken({ pid, port, ports, csrfToken })` and `readPersistedLsToken()`.
   - Whenever a valid UUID token is encountered (from `process.env.ANTIGRAVITY_CSRF_TOKEN`, command-line discovery, or transcript hits), it is atomically saved to `~/.gemini/.gemini_quota_ls_token.json`.
2. **Discovery & Fallback Chain Integration**:
   - In `getEnvLanguageServerTarget()` and `discoverLanguageServer()`, if `csrfToken` is absent from process environment/flags, `readPersistedLsToken()` recovers the saved token if its port or PID matches the active Language Server.
   - In `resolveCsrfTokenFallback()`, the persisted cache is checked as a primary fast path before transcript I/O scanning.
3. **Environment Forwarding in Background Workers**:
   - `triggerBackgroundQuotaRefresh()` now inspects known tokens (via options, environment, or persisted cache) and forwards `ANTIGRAVITY_CSRF_TOKEN` and `ANTIGRAVITY_LS_PORT` into the spawned child process's `env`.
   - The child process now successfully authenticates over HTTPS, receives HTTP 200, updates `gemini_quota_cache.json` with fresh live data, and clears the negative probe cooldown marker.

### B. Turn-Based Dynamic Quota Adjustment & Bucket Replenishment (`src/gemini-quota.js`)
1. **`applyTurnUsageToQuota(quotaData, sessions, options)`**:
   - Dynamically calculates tokens consumed in turns created after `quotaData.timestampMs`.
   - Subtracts the consumed token ratio (`tokens / limit5h * 100` and `tokens / limit7d * 100`) from `remainPercent` in real time, clamping results safely between 0% and 100%.
   - Detects when `resetTime` has elapsed (`nowMs >= resetTime`) and replenishes the bucket to 100% (minus any turns executed after `resetTime`).
2. **Hook Handler & Statusline Integration (`src/hook-handler.js` & `src/index.js`)**:
   - `handlePostInvocation()` passes synced `sessions` and `quota` configuration to `applyTurnUsageToQuota()`.
   - The statusline badge reflects real-time token drops on every single turn, eliminating frozen values.

---

## 4. Verification & Testing

### 1. Full Automated Regression Test Suite
Executed:
```bash
node test/run-tests.js
```
**Results**:
- Added `Suite 29: Statusline 5h/7d Real-Time Update & Token Recovery Validations` (5 new tests).
- Total: **256 passed, 0 failed** across all 29 test suites.

### 2. Live System Verification
Executed `node bin/agy-tokens.js --hook --raw` on the active live workspace:
```text
⚡ Turn: 116 ($0.0001) | Today: 2.61M ($0.323) | Cache: 99% | 5h: ▰▰▰▱▱ 65% (3h 56m) | 7d: ▰▰▰▱▱ 56% (8h 37m) | 📊 Dashboard
```
- The `!` marker was cleanly removed.
- `5h: 65%` and `7d: 56%` dynamically refreshed to reflect actual turn consumption.
- Countdown timers (`3h 56m`, `8h 37m`) advanced accurately.
- `gemini_quota_cache.json` contains `isLive: true` with zero errors, and `gemini_quota_probe_cooldown.json` was unlinked.

---

## 5. Modified Files

| File | Changes |
|---|---|
| [`src/gemini-quota.js`](file:///D:/OneDrive/Projects/Antigravity-cli/src/gemini-quota.js) | Added `LS_TOKEN_FILE`, `savePersistedLsToken`, `readPersistedLsToken`, `applyTurnUsageToQuota`; updated `getEnvLanguageServerTarget`, `discoverLanguageServer`, `resolveCsrfTokenFallback`, `getCachedGeminiQuota`, and `triggerBackgroundQuotaRefresh` |
| [`src/hook-handler.js`](file:///D:/OneDrive/Projects/Antigravity-cli/src/hook-handler.js) | Integrated token persistence and `applyTurnUsageToQuota` on `handlePostInvocation` badge data |
| [`src/index.js`](file:///D:/OneDrive/Projects/Antigravity-cli/src/index.js) | Persists environment CSRF token on startup; passes `userConfig.quota` to hook handler |
| [`test/run-tests.js`](file:///D:/OneDrive/Projects/Antigravity-cli/test/run-tests.js) | Added Suite 29 unit tests covering token persistence, turn deduction, replenishment, and fallback |
| [`reports/statusline-5h-7d-fix-report.md`](file:///D:/OneDrive/Projects/Antigravity-cli/reports/statusline-5h-7d-fix-report.md) | Full diagnosis and architectural resolution report |
