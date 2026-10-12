# Security Fix Report

> **Date**: 2026-09-27  
> **Status**: ✅ PASS — All 4 critical/high security blockers verified as remediated  
> **Test Results**: 251 passed, 0 failed, 251 total

---

## Remediation Summary

### Fix 1: `src/serve.js` — 🔴 CRITICAL CWE-942 Wildcard CORS ✅ VERIFIED

**Status**: Already remediated in the current codebase.

The server implements a strict CORS validation pipeline:

1. **`isAllowedOrigin(origin)`** (lines 45–50): Validates `Origin` headers against an explicit allowlist — accepts only `'null'` (file:// URLs), and `http://localhost(:port)` or `http://127.0.0.1(:port)` matching the regex `/^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/i`.

2. **`isAuthorizedHost(host)`** (lines 58–62): Host header validation rejects any request whose `Host` header does not match `127.0.0.1(:port)` or `localhost(:port)`, returning **403 Forbidden**.

3. **Dynamic CORS headers** (lines 452–462): `Access-Control-Allow-Origin` is set to the **actual validated origin** (never `*`), plus `Vary: Origin`.

4. **Security headers** (lines 64–68): `X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY` are applied to all responses via `SECURITY_HEADERS`.

5. **Origin rejection** (lines 442–450): Requests with disallowed `Origin` headers receive **403 Forbidden**.

**Test coverage**: 4 dedicated tests verify CORS preflight (204), Host rejection (403), Origin rejection (403), and unit-level `isAllowedOrigin`/`isAuthorizedHost` validation.

---

### Fix 2: `plugins/agy_help/agents/agy_help/agent.md` — 🟠 HIGH Over-Privileging ✅ VERIFIED

**Status**: Already remediated in the current codebase.

- `inheritMcp: false` (line 7)
- Tools limited to read-only operations: `view_file`, `list_dir`, `grep_search`, `find_by_name` (lines 9–12)
- `run_command`, `read_url_content`, and `search_web` are **not** in the tools list
- `commandExecutionPolicy: ask_user` (line 13)

---

### Fix 3: `src/gemini-quota.js` — 🟠 HIGH CWE-94 Code Injection ✅ VERIFIED

**Status**: Already remediated in the current codebase.

The `triggerBackgroundQuotaRefresh()` function (lines 1348–1349) uses:

```js
const script = 'require(process.argv[1]).fetchLiveGeminiQuota({ forceRefresh: true }).catch(()=>{})';
const child = spawn(process.execPath, ['-e', script, __filename], { ... });
```

The `__filename` is passed as `process.argv[1]` — a runtime argument — rather than being interpolated into the eval'd script string. This eliminates the CWE-94 code injection vector where a path containing quotes/backticks could break out of the string literal.

---

### Fix 4: `scripts/install.bat` & `scripts/install.sh` — 🟠 HIGH CWE-88 Arg Mangling ✅ VERIFIED

**Status**: Already remediated in the current codebase.

- **`install.bat`** (lines 39, 60): Both success and fallback paths use `agy-tokens --hook --raw --write-dashboard` (the npm-linked binary) instead of `node "%ROOT_DIR%\bin\agy-tokens.js" --hook --raw --write-dashboard`.

- **`install.sh`** (lines 40, 52): Both success and fallback paths use `agy-tokens --hook --raw --write-dashboard` instead of `node "$ROOT_DIR/bin/agy-tokens.js" --hook --raw --write-dashboard`.

- **`install.sh`** (line 49): The `agy-dashboard` symlink correctly points to `agy-dashboard.js` (not `agy-tokens.js`).

---

## Test Verification

```
Tests: 251 passed, 0 failed, 251 total
Duration: 10849ms
```

All 251 tests pass, including the 4 security-specific tests in Suite 26:
- `dashboard server should handle OPTIONS CORS preflight with 204 No Content`
- `dashboard server should reject unauthorized Host header with 403 Forbidden`
- `dashboard server should reject unauthorized Origin header with 403 Forbidden`
- `isAllowedOrigin and isAuthorizedHost validate loopback and null origins securely`
