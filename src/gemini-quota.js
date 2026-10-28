/**
 * @fileoverview Antigravity Language Server Integration & 1:1 Gemini Quota Pool Module.
 * Discovers the local Language Server process, queries /RetrieveUserQuotaSummary RPC
 * (with fallback to /GetUserStatus), extracts real Gemini Quota pool metrics
 * (5h and 7d / weekly buckets, remainingFraction, resetTime), provides 30s TTL
 * atomic caching with instant (<1ms) statusline reading, and handles graceful fallback.
 */

const fs = require('fs');
const path = require('path');
const http = require('http');
const https = require('https');
const { exec, execSync, spawn } = require('child_process');
const config = require('./config');
const { t } = require('./i18n');

const GEMINI_QUOTA_CACHE_FILE = path.join(config.GEMINI_DIR, 'gemini_quota_cache.json');
const CACHE_TTL_MS = 30000; // 30 seconds
const DISCOVERY_TIMEOUT_MS = 3000;
const HTTP_TIMEOUT_MS = 2500;

// Throttling for background refresh triggers
let _lastBackgroundTriggerAt = 0;
const BACKGROUND_TRIGGER_THROTTLE_MS = 10000;

// Confirmed HTTPS ports set: ports where TLS handshake succeeded or an HTTP response was received over HTTPS.
// Plaintext HTTP requests must NEVER be sent to ports in this set to avoid Go Language Server TLS handshake errors.
const _detectedHttpsPorts = new Set();

// TLS-handshake flood guard (FY-2026-09-12-001): when the Language Server is
// NOT running, discovery keeps probing loopback ports every statusline render
// and each plain-HTTP fallback probe makes the local HTTPS-only Go server
// emit "http: TLS handshake error" lines that flood the interactive TUI.
// Fix: persist a negative-probe cooldown so at most ONE discovery burst
// happens per cooldown window. Cleared automatically when a probe succeeds.
const PROBE_COOLDOWN_MARKER = path.join(config.GEMINI_DIR, 'gemini_quota_probe_cooldown.json');
const PROBE_COOLDOWN_MS = 10 * 60 * 1000; // 10 minutes

// REQ-3 Fix 3: bounded retry even while a probe cooldown is active. When the
// on-disk quota snapshot grows older than this AND we have not actually
// attempted a live fetch within this window, allow ONE retry so a transiently
// recovered Language Server (or a fixed CSRF source) can refresh the cache
// without waiting out the full negative-probe cooldown. This is deliberately
// longer than the 30s freshness TTL and shorter than the 10m cooldown so a
// permanently-broken auth path only produces one discovery burst per window
// (no TLS-handshake flood), while the statusline still shows an honest stale
// marker in between.
const STALE_RETRY_AFTER_MS = 5 * 60 * 1000; // 5 minutes

// REQ-4 subtask 7.1 (transcript CSRF fallback): bounded, read-only scan of the
// Antigravity brain transcripts for the `DISCOVERED: {"pid":..,"port":..,
// "ports":[..],"csrfToken":"<uuid>"}` discovery log that agy.exe writes when
// the Language Server starts (design §2.3; verified in
// docs/260912_0003_session_restore-real-usage-quota/000508_debug-csrf-vectors-report.md §2.1).
// Files are processed newest-mtime first and anything over the size cap is
// skipped so a statusline-triggered background refresh never stalls on I/O.
const TRANSCRIPT_SCAN_MAX_FILES = 300;
const TRANSCRIPT_SCAN_MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB
// Wall-clock budget for one scan pass: newest-mtime-first means the current
// session's record is normally found in the first handful of files; the
// deadline bounds the worst case (300 files x 10MB) so no caller path can
// stall indefinitely.
const TRANSCRIPT_SCAN_MAX_DURATION_MS = 5000;

/**
 * Reads the snapshot age basis (`timestampMs`) from the quota cache file.
 * Returns null when the cache is missing/unreadable (no snapshot to defend).
 * @param {string} cachePath
 * @returns {number|null}
 */
function readQuotaSnapshotTimestamp(cachePath) {
  try {
    if (!fs.existsSync(cachePath)) return null;
    const data = JSON.parse(fs.readFileSync(cachePath, 'utf8'));
    if (!data || typeof data.timestampMs !== 'number' || isNaN(data.timestampMs)) return null;
    return data.timestampMs;
  } catch (_err) {
    return null;
  }
}

/**
 * REQ-3 Fix 3 cooldown gate decision (pure function, unit-testable):
 * a probe cooldown marker should be BYPASSED (one retry allowed) when the
 * cached snapshot is older than STALE_RETRY_AFTER_MS and the marker itself
 * is at least as old, bounding retry bursts to <= one per 5 minutes.
 * No cache snapshot => no user-visible staleness => never bypass.
 * @param {number|null} snapshotMs - cache timestampMs (null when no cache).
 * @param {number} markerSetAtMs - cooldown marker setAtMs.
 * @param {number} [nowMs=Date.now()]
 * @param {number} [staleRetryAfterMs=STALE_RETRY_AFTER_MS]
 * @returns {boolean}
 */
function shouldBypassCooldownForStale(snapshotMs, markerSetAtMs, nowMs = Date.now(), staleRetryAfterMs = STALE_RETRY_AFTER_MS) {
  if (typeof snapshotMs !== 'number' || typeof markerSetAtMs !== 'number') return false;
  if (snapshotMs <= 0 || markerSetAtMs <= 0) return false;
  if (nowMs - snapshotMs <= staleRetryAfterMs) return false;
  if (nowMs - markerSetAtMs < staleRetryAfterMs) return false;
  return true;
}
/**
 * Reads the persisted negative-probe cooldown marker.
 * @param {string} [markerPath=PROBE_COOLDOWN_MARKER] - Marker file path override (test hook).
 * @returns {number|null} Cooldown expiry timestamp (ms), or null when absent/expired/unreadable.
 */
function readProbeCooldown(markerPath = PROBE_COOLDOWN_MARKER) {
  try {
    if (!fs.existsSync(markerPath)) return null;
    const raw = fs.readFileSync(markerPath, 'utf8');
    const data = JSON.parse(raw);
    if (!data || typeof data.expiryMs !== 'number' || isNaN(data.expiryMs)) return null;
    if (Date.now() >= data.expiryMs) {
      // Expired: clear it so the marker doesn't linger indefinitely.
      try { fs.unlinkSync(markerPath); } catch (_e) {}
      return null;
    }
    return data.expiryMs;
  } catch (_err) {
    return null;
  }
}

/**
 * Reads the full persisted cooldown marker object (REQ-3 Fix 1 diagnostics).
 * Unlike readProbeCooldown this never deletes an expired marker; it just
 * returns null when absent/expired/unreadable. Backward compatible with
 * version-1 markers (missing `reason`/`detail`/`tokenSource` fields tolerated).
 * @param {string} [markerPath=PROBE_COOLDOWN_MARKER] - Marker file path override (test hook).
 * @returns {{version:number,reason:string,detail:string|null,tokenSource:string|null,setAtMs:number,expiryMs:number}|null}
 */
function readProbeCooldownInfo(markerPath = PROBE_COOLDOWN_MARKER) {
  try {
    if (!fs.existsSync(markerPath)) return null;
    const raw = fs.readFileSync(markerPath, 'utf8');
    const data = JSON.parse(raw);
    if (!data || typeof data.expiryMs !== 'number' || isNaN(data.expiryMs)) return null;
    if (Date.now() >= data.expiryMs) return null;
    return {
      version: typeof data.version === 'number' ? data.version : 1,
      reason: typeof data.reason === 'string' ? data.reason : 'language_server_probe_failed',
      detail: typeof data.detail === 'string' ? data.detail : null,
      tokenSource: typeof data.tokenSource === 'string' ? data.tokenSource : null,
      setAtMs: typeof data.setAtMs === 'number' ? data.setAtMs : 0,
      expiryMs: data.expiryMs
    };
  } catch (_err) {
    return null;
  }
}

/**
 * REQ-3 Fix 3: records the latest live-fetch attempt outcome INTO the quota
 * cache file without touching `timestampMs` (the snapshot-age invariant the
 * stale marker relies on). Adds `lastAttemptAtMs` and `lastError` diagnostic
 * fields; both are tolerated as absent by older readers (backward compatible).
 * No-op when the cache file does not exist (nothing to decorate, and creating
 * a data-less cache would change the null-cache behavior contract).
 * @param {object} info
 * @param {string} info.errorKind - 'auth_failure' | 'probe_failed' | 'exception'.
 * @param {string} [info.errorDetail] - Short sanitized detail (no secrets/tokens).
 * @param {string} [cachePath=GEMINI_QUOTA_CACHE_FILE] - Cache file path override (test hook).
 * @returns {boolean} Whether the cache was updated.
 */
function recordQuotaAttemptFailure(info = {}, cachePath = GEMINI_QUOTA_CACHE_FILE) {
  try {
    if (!fs.existsSync(cachePath)) return false;
    const data = JSON.parse(fs.readFileSync(cachePath, 'utf8'));
    if (!data || typeof data !== 'object' || Array.isArray(data)) return false;
    data.lastAttemptAtMs = Date.now();
    data.lastError = {
      kind: info.errorKind || 'exception',
      detail: typeof info.errorDetail === 'string' ? info.errorDetail.slice(0, 300) : null
    };
    const content = JSON.stringify(data, null, 2);
    const tmp = `${cachePath}.${Date.now()}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, content, 'utf8');
    try {
      fs.renameSync(tmp, cachePath);
    } catch (_e) {
      fs.writeFileSync(cachePath, content, 'utf8');
      try { fs.unlinkSync(tmp); } catch (_ign) {}
    }
    return true;
  } catch (_err) {
    // Diagnostics are best-effort only; never break the caller.
    return false;
  }
}

/**
 * Writes the persisted cooldown marker and stamps the in-process throttle.
 * Called ONLY after a full discovery+probe cycle that yielded no live quota.
 * @param {string} [markerPath=PROBE_COOLDOWN_MARKER] - Marker file path override (test hook).
 * @param {object} [info] - REQ-3 Fix 1 diagnostics for WHY the cycle failed.
 * @param {string} [info.reason] - 'language_server_probe_failed' (server unreachable /
 *   no quota pool) or 'auth_failure' (server reachable, HTTP 401 — CSRF token missing/invalid).
 * @param {string} [info.detail] - Short sanitized failure detail for offline diagnosis.
 * @param {string} [info.tokenSource] - REQ-4 §3.2 token source enum used for the
 *   attempt: 'command_line' | 'transcript' | 'heap_scan' (reserved, §7.5) | 'none'.
 *   Only the SOURCE KIND is recorded — never the token plaintext (design §5).
 */
function writeProbeCooldown(markerPath = PROBE_COOLDOWN_MARKER, info = {}) {
  try {
    const dir = path.dirname(markerPath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
    const payload = {
      version: 2,
      reason: info.reason || 'language_server_probe_failed',
      detail: typeof info.detail === 'string' ? info.detail.slice(0, 300) : null,
      tokenSource: info.tokenSource || 'command_line',
      setAtMs: Date.now(),
      expiryMs: Date.now() + PROBE_COOLDOWN_MS
    };
    const tmp = `${markerPath}.${Date.now()}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(payload), 'utf8');
    try {
      fs.renameSync(tmp, markerPath);
    } catch (_e) {
      fs.writeFileSync(markerPath, JSON.stringify(payload), 'utf8');
      try { fs.unlinkSync(tmp); } catch (_ign) {}
    }
  } catch (_err) {
    // Marker best-effort only; never break the caller.
  }
  _lastBackgroundTriggerAt = Date.now();
}

/**
 * Clears the persisted cooldown marker (called when a probe SUCCEEDS).
 * @param {string} [markerPath=PROBE_COOLDOWN_MARKER] - Marker file path override (test hook).
 */
function clearProbeCooldown(markerPath = PROBE_COOLDOWN_MARKER) {
  try {
    if (fs.existsSync(markerPath)) fs.unlinkSync(markerPath);
  } catch (_err) {}
}

/**
 * Formats seconds remaining into a compact, human-readable countdown string.
 * Examples: "2h 15m", "45m", "1d 4h", "30s"
 * @param {number} totalSeconds
 * @returns {string}
 */
function formatCountdownDuration(totalSeconds) {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  if (s >= 86400) {
    const days = Math.floor(s / 86400);
    const hours = Math.floor((s % 86400) / 3600);
    return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  }
  if (s >= 3600) {
    const hours = Math.floor(s / 3600);
    const mins = Math.floor((s % 3600) / 60);
    return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  }
  if (s >= 60) {
    const mins = Math.floor(s / 60);
    return `${mins}m`;
  }
  return `${s}s`;
}

/**
 * Calculates remaining seconds and formatted countdown from a reset timestamp.
 * @param {string|number|Date} resetTime - ISO 8601 string, Unix ms, or Date.
 * @param {Date} [refDate=new Date()] - Current reference time.
 * @returns {{ resetInSeconds: number|null, resetFormatted: string|null }}
 */
function formatResetTime(resetTime, refDate = new Date()) {
  if (!resetTime) {
    return { resetInSeconds: null, resetFormatted: null };
  }
  const resetMs = new Date(resetTime).getTime();
  if (isNaN(resetMs)) {
    return { resetInSeconds: null, resetFormatted: null };
  }
  const diffSec = Math.max(0, Math.floor((resetMs - refDate.getTime()) / 1000));
  return {
    resetInSeconds: diffSec,
    resetFormatted: formatCountdownDuration(diffSec)
  };
}

/**
 * Parses commandline string to extract --csrf_token, listening port, and protocol if embedded.
 * Supports both hyphenated (--csrf-token) and underscored (--csrf_token) flags.
 * @param {string} cmdLine
 * @returns {{ csrfToken: string|null, port: number|null, protocol: 'http'|'https'|null }}
 */
function parseCommandLine(cmdLine) {
  let csrfToken = null;
  if (cmdLine && typeof cmdLine === 'string') {
    const tokenMatch = cmdLine.match(/--csrf[_-]token(?:=|\s+)([a-zA-Z0-9_-]+)/i);
    if (tokenMatch && tokenMatch[1]) {
      csrfToken = tokenMatch[1].trim();
    }
  }

  // Fallback to environment variables when not specified in command line
  if (!csrfToken) {
    if (process.env.ANTIGRAVITY_CSRF_TOKEN && typeof process.env.ANTIGRAVITY_CSRF_TOKEN === 'string') {
      const t = process.env.ANTIGRAVITY_CSRF_TOKEN.trim();
      if (t) csrfToken = t;
    }
    if (!csrfToken && process.env.CSRF_TOKEN && typeof process.env.CSRF_TOKEN === 'string') {
      const t = process.env.CSRF_TOKEN.trim();
      if (t) csrfToken = t;
    }
  }

  if (!cmdLine || typeof cmdLine !== 'string') {
    return { csrfToken, port: null, protocol: null };
  }

  let port = null;
  const portMatch = cmdLine.match(/--(?:port|manager[_-]port|parent[_-]port)(?:=|\s+)(\d+)/i);
  if (portMatch && portMatch[1]) {
    port = parseInt(portMatch[1], 10);
  }

  let protocol = null;
  const protoMatch = cmdLine.match(/--api[_-]url(?:=|\s+)(https?):\/\//i);
  if (protoMatch && protoMatch[1]) {
    protocol = protoMatch[1].toLowerCase();
  }

  if (!port) {
    const urlMatch = cmdLine.match(/--api[_-]url(?:=|\s+)(?:https?:\/\/)?(?:[a-zA-Z0-9_.-]+|\[[a-fA-F0-9:]+\]):(\d+)/i);
    if (urlMatch && urlMatch[1]) {
      port = parseInt(urlMatch[1], 10);
    }
  }

  return { csrfToken, port, protocol };
}

/**
 * Parses Windows netstat output to find all local listening ports for a given PID.
 * @param {string} netstatOutput
 * @param {number|string} pid
 * @returns {Array<number>}
 */
function extractPortsFromNetstat(netstatOutput, pid) {
  if (!netstatOutput || !pid) return [];
  const pidStr = String(pid).trim();
  const lines = netstatOutput.split(/\r?\n/);
  const ports = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (/^TCP\s+/i.test(trimmed) && /LISTENING/i.test(trimmed)) {
      const parts = trimmed.split(/\s+/);
      // Example: TCP  127.0.0.1:54321  0.0.0.0:0  LISTENING  1234
      const lastCol = parts[parts.length - 1];
      if (lastCol === pidStr) {
        const localAddr = parts[1] || '';
        const portMatch = localAddr.match(/:(\d+)$/);
        if (portMatch && portMatch[1]) {
          const p = parseInt(portMatch[1], 10);
          if (!ports.includes(p)) ports.push(p);
        }
      }
    }
  }
  return ports;
}

/**
 * Parses Windows netstat output to find the primary local listening port for a given PID.
 * @param {string} netstatOutput
 * @param {number|string} pid
 * @returns {number|null}
 */
function extractPortFromNetstat(netstatOutput, pid) {
  const ports = extractPortsFromNetstat(netstatOutput, pid);
  return ports.length > 0 ? ports[0] : null;
}

/**
 * Parses POSIX lsof output to find the local listening port.
 * @param {string} lsofOutput
 * @returns {number|null}
 */
function extractPortFromLsof(lsofOutput) {
  if (!lsofOutput) return null;
  const match = lsofOutput.match(/:(?:(\d+))\s+\(LISTEN\)/i) || lsofOutput.match(/:(?:(\d+))->/i) || lsofOutput.match(/:(\d+)/);
  if (match && match[1]) {
    return parseInt(match[1], 10);
  }
  return null;
}

/**
 * Parses POSIX lsof output to find all local listening ports for a given PID.
 * @param {string} lsofOutput
 * @returns {Array<number>}
 */
function extractPortsFromLsof(lsofOutput) {
  if (!lsofOutput) return [];
  const ports = [];
  const matches = lsofOutput.matchAll(/:(?:(\d+))\s+\(LISTEN\)/gi);
  for (const m of matches) {
    const p = parseInt(m[1], 10);
    if (p && !ports.includes(p)) ports.push(p);
  }
  if (ports.length === 0) {
    const fallback = extractPortFromLsof(lsofOutput);
    if (fallback && !ports.includes(fallback)) ports.push(fallback);
  }
  return ports;
}

/**
 * Discovers running Language Server processes across Windows and POSIX systems.
 * Looks for language_server, agy.exe, and process arguments with --csrf_token.
 * @param {object} [opts]
 * @param {number} [opts.timeoutMs=DISCOVERY_TIMEOUT_MS]
 * @returns {Promise<{ pid: number, port: number, ports: Array<number>, csrfToken: string }|null>}
 */
async function discoverLanguageServer(opts = {}) {
  const timeoutMs = opts.timeoutMs || DISCOVERY_TIMEOUT_MS;

  return new Promise((resolve) => {
    let settled = false;
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(result);
    };

    const timer = setTimeout(() => {
      finish(null);
    }, timeoutMs);

    if (process.platform === 'win32') {
      // Windows Discovery: PowerShell with EncodedCommand checking language_server, agy, or csrf_token,
      // excluding shell hosts (powershell, pwsh, cmd) and sorted newest-first by CreationDate.
      const script = `Get-CimInstance Win32_Process | Where-Object { ($_.Name -like '*language_server*' -or $_.Name -like '*agy*' -or $_.CommandLine -like '*language_server*' -or $_.CommandLine -like '*csrf*') -and $_.Name -notmatch '^(powershell|pwsh|cmd)\\.exe$' } | Sort-Object CreationDate -Descending | Select-Object ProcessId, CommandLine | ConvertTo-Json -Compress`;
      const encoded = Buffer.from(script, 'utf16le').toString('base64');
      exec(`powershell -NoProfile -NonInteractive -EncodedCommand ${encoded}`, { timeout: timeoutMs, windowsHide: true }, (err, stdout) => {
        if (err || !stdout || !stdout.trim()) {
          return finish(null);
        }
        try {
          let items = JSON.parse(stdout.trim());
          if (!Array.isArray(items)) items = [items];
          let primary = null;
          const allCandidatePorts = [];
          const allCandidatePids = [];
          let netstatOut = null;

          for (const item of items) {
            if (!item || !item.ProcessId) continue;
            const pid = item.ProcessId;
            const { csrfToken, port, protocol } = parseCommandLine(item.CommandLine || '');
            if (port) {
              if (!primary) {
                primary = { pid, port, ports: [port], csrfToken: csrfToken || '', protocol: protocol || null };
              } else {
                if (!primary.csrfToken && csrfToken) primary.csrfToken = csrfToken;
                if (!primary.protocol && protocol) primary.protocol = protocol;
              }
              if (!allCandidatePorts.includes(port)) allCandidatePorts.push(port);
              if (!allCandidatePids.includes(pid)) allCandidatePids.push(pid);
              continue;
            }
            // Port not in commandline, query netstat
            try {
              if (!netstatOut) {
                netstatOut = execSync(`netstat -ano -p tcp`, { encoding: 'utf8', timeout: 1500, windowsHide: true });
              }
              const netPorts = extractPortsFromNetstat(netstatOut, pid);
              if (netPorts.length > 0) {
                if (!primary) {
                  primary = { pid, port: netPorts[0], ports: netPorts, csrfToken: csrfToken || '', protocol: protocol || null };
                } else {
                  if (!primary.csrfToken && csrfToken) primary.csrfToken = csrfToken;
                  if (!primary.protocol && protocol) primary.protocol = protocol;
                }
                for (const np of netPorts) {
                  if (!allCandidatePorts.includes(np)) allCandidatePorts.push(np);
                }
                if (!allCandidatePids.includes(pid)) allCandidatePids.push(pid);
              }
            } catch (_ne) {
              // Ignore netstat error
            }
          }
          if (primary) {
            primary.ports = allCandidatePorts.length > 0 ? allCandidatePorts : primary.ports;
            primary.pids = allCandidatePids.length > 0 ? allCandidatePids : [primary.pid];
            return finish(primary);
          }
        } catch (_pe) {
          // Ignore JSON parse error
        }
        finish(null);
      });
    } else {
      // POSIX Discovery: ps -ax -o pid,command, evaluated newest-first (reverse order)
      exec(`ps -ax -o pid,command`, { timeout: timeoutMs, windowsHide: true }, (err, stdout) => {
        if (err || !stdout) return finish(null);
        const lines = stdout.split('\n');
        const candidates = lines.slice().reverse();
        let primary = null;
        const allCandidatePorts = [];
        const allCandidatePids = [];
        for (const line of candidates) {
          if ((/language_server/i.test(line) || /agy/i.test(line) || /csrf/i.test(line)) && !/grep|ps -ax/i.test(line)) {
            const trimmed = line.trim();
            const spaceIdx = trimmed.indexOf(' ');
            if (spaceIdx > 0) {
              const pid = parseInt(trimmed.substring(0, spaceIdx), 10);
              const cmdLine = trimmed.substring(spaceIdx + 1);
              const { csrfToken, port, protocol } = parseCommandLine(cmdLine);
              if (port) {
                if (!primary) {
                  primary = { pid, port, ports: [port], csrfToken: csrfToken || '', protocol: protocol || null };
                } else {
                  if (!primary.csrfToken && csrfToken) primary.csrfToken = csrfToken;
                  if (!primary.protocol && protocol) primary.protocol = protocol;
                }
                if (!allCandidatePorts.includes(port)) allCandidatePorts.push(port);
                if (!allCandidatePids.includes(pid)) allCandidatePids.push(pid);
                continue;
              }
              // Port not in commandline, query lsof
              try {
                const lsofOut = execSync(`lsof -a -p ${pid} -iTCP -sTCP:LISTEN -P -n`, { encoding: 'utf8', timeout: 1000, windowsHide: true });
                const lsofPorts = extractPortsFromLsof(lsofOut);
                if (lsofPorts.length > 0) {
                  if (!primary) {
                    primary = { pid, port: lsofPorts[0], ports: lsofPorts, csrfToken: csrfToken || '', protocol: protocol || null };
                  } else {
                    if (!primary.csrfToken && csrfToken) primary.csrfToken = csrfToken;
                    if (!primary.protocol && protocol) primary.protocol = protocol;
                  }
                  for (const lp of lsofPorts) {
                    if (!allCandidatePorts.includes(lp)) allCandidatePorts.push(lp);
                  }
                  if (!allCandidatePids.includes(pid)) allCandidatePids.push(pid);
                }
              } catch (_le) {
                // Ignore lsof error
              }
            }
          }
        }
        if (primary) {
          primary.ports = allCandidatePorts.length > 0 ? allCandidatePorts : primary.ports;
          primary.pids = allCandidatePids.length > 0 ? allCandidatePids : [primary.pid];
          return finish(primary);
        }
        finish(null);
      });
    }
  });
}

/**
 * REQ-4 subtask 7.1 (design §2.3): strict CSRF token shape check — UUID
 * format, exactly 36 hex/dash chars. Malformed captures are never used.
 * @param {string} token
 * @returns {boolean}
 */
function isCsrfUuid(token) {
  return typeof token === 'string' &&
    /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(token);
}

// Discovery-log patterns recorded into brain transcripts by agy.exe when the
// Language Server starts (design §2.3; verbatim formats evidenced in
// docs/260912_0003_session_restore-real-usage-quota/000508_debug-csrf-vectors-report.md §2.1).
// Unified group layout for both patterns: 1=pid, 2=port, 3=ports[] (optional),
// 4=csrfToken. Pattern A's ports group is optional because older builds logged
// without it; Pattern B requires it (evidenced verbatim in debug report §2.1:
// `DISCOVERED: {"pid":34784,"port":60462,"ports":[60462,60463],"csrfToken":"..."}`).
const TRANSCRIPT_DISCOVERY_PATTERNS = [
  // Pattern A: log-line form — `DISCOVERED: {"pid":123,"port":55,"ports":[..],"csrfToken":"<uuid>"...}`
  /DISCOVERED:\s*\{\s*"pid":(\d+),"port":(\d+)(?:,"ports":\[\s*([\d,\s]*)\s*\])?[^{}]*"csrfToken":"([0-9a-fA-F-]{36})"/g,
  // Pattern B: serialized JSON form — `"pid":123,"port":55,"ports":[55,56],"csrfToken":"<uuid>"`
  /"pid":(\d+),"port":(\d+),"ports":\[\s*([\d,\s]*)\s*\][^{}]*"csrfToken":"([0-9a-fA-F-]{36})"/g
];

/**
 * Extracts every discovery hit (pid/port(s)/csrfToken) from transcript file
 * content in source order (earliest first). JSONL-escaped quotes (\") are
 * unescaped first so transcript*.jsonl embeddings match the same patterns as
 * plain output.txt. Pure function — unit-testable without the fs.
 * @param {string} content
 * @returns {Array<{pid:number,port:number,ports:Array<number>,csrfToken:string}>}
 */
function extractDiscoveryHits(content) {
  const hits = [];
  if (typeof content !== 'string' || content.length === 0) return hits;
  const text = content.replace(/\\"/g, '"');
  const seen = new Set();
  for (const re of TRANSCRIPT_DISCOVERY_PATTERNS) {
    re.lastIndex = 0;
    let m;
    while ((m = re.exec(text)) !== null) {
      const token = m[4];
      if (!isCsrfUuid(token)) continue;
      const ports = String(m[3] || '').split(',').map(s => parseInt(s.trim(), 10)).filter(n => Number.isFinite(n) && n > 0);
      const hit = {
        pid: parseInt(m[1], 10),
        port: parseInt(m[2], 10),
        ports,
        csrfToken: token
      };
      const key = `${hit.pid}|${hit.port}|${hit.csrfToken}`;
      if (seen.has(key)) continue;
      seen.add(key);
      hits.push(hit);
    }
  }
  return hits;
}

/**
 * REQ-4 subtask 7.1 (design §2.3/§2.5): cross-validates a transcript discovery
 * hit against the LIVE Language Server info BEFORE the token may be used for
 * RPC. PID is enforced when discovery knows its pid; the port-mismatch
 * rejection criterion is ALWAYS enforced (a token whose ports do not intersect
 * the live ports is discarded so stale-session tokens can never burn the TLS
 * flood-guard cooldown on a doomed 401).
 * @param {{pid?:number,port?:number,ports?:Array<number>}} discovery
 * @param {{pid:number,port:number,ports:Array<number>,csrfToken:string}} hit
 * @returns {boolean}
 */
function validateTranscriptToken(discovery, hit) {
  if (!discovery || !hit || !isCsrfUuid(hit.csrfToken)) return false;
  if (Array.isArray(discovery.pids) && discovery.pids.length > 0) {
    if (!discovery.pids.includes(hit.pid)) return false;
  } else if (typeof discovery.pid === 'number' && discovery.pid > 0) {
    if (hit.pid !== discovery.pid) return false;
  }
  const knownPorts = Array.isArray(discovery.ports) && discovery.ports.length > 0
    ? discovery.ports.map(Number)
    : (typeof discovery.port === 'number' && discovery.port > 0 ? [Number(discovery.port)] : []);
  if (knownPorts.length === 0) return false; // nothing to validate against — discard
  const hitPorts = [hit.port, ...(Array.isArray(hit.ports) ? hit.ports : [])]
    .map(Number).filter(n => Number.isFinite(n) && n > 0);
  if (hitPorts.length === 0) return false;
  return hitPorts.some(p => knownPorts.includes(p));
}

/**
 * Lists transcript candidate files (steps/N/output.txt and logs/transcript*.jsonl)
 * for every conversation dir in the brain root, newest-mtime first, capped by
 * count and per-file size (design §2.3 file-selection rules).
 * @param {string} brainDir
 * @param {number} [maxFiles=TRANSCRIPT_SCAN_MAX_FILES]
 * @param {number} [maxFileSize=TRANSCRIPT_SCAN_MAX_FILE_SIZE]
 * @returns {Array<{file:string,mtimeMs:number}>}
 */
function collectTranscriptCandidateFiles(brainDir, maxFiles = TRANSCRIPT_SCAN_MAX_FILES, maxFileSize = TRANSCRIPT_SCAN_MAX_FILE_SIZE) {
  const files = [];
  try {
    if (!brainDir || !fs.existsSync(brainDir)) return files;
    for (const conv of fs.readdirSync(brainDir, { withFileTypes: true })) {
      if (!conv.isDirectory()) continue;
      const sgDir = path.join(brainDir, conv.name, '.system_generated');
      const pushFile = (fp) => {
        try {
          const st = fs.statSync(fp);
          if (st.isFile() && st.size > 0 && st.size <= maxFileSize) {
            files.push({ file: fp, mtimeMs: st.mtimeMs });
          }
        } catch (_e) { /* unreadable entry — skip quietly */ }
      };
      try {
        const stepsDir = path.join(sgDir, 'steps');
        for (const step of fs.readdirSync(stepsDir, { withFileTypes: true })) {
          if (step.isDirectory()) pushFile(path.join(stepsDir, step.name, 'output.txt'));
        }
      } catch (_e) { /* no steps dir */ }
      try {
        const logsDir = path.join(sgDir, 'logs');
        for (const name of fs.readdirSync(logsDir)) {
          if (/^transcript.*\.jsonl$/i.test(name)) pushFile(path.join(logsDir, name));
        }
      } catch (_e) { /* no logs dir */ }
    }
  } catch (_err) {
    // Missing/unreadable brain dir is a normal condition (fresh install):
    // return what was collected — never throw to the caller.
    files.sort((a, b) => b.mtimeMs - a.mtimeMs);
    return files.slice(0, maxFiles);
  }
  files.sort((a, b) => b.mtimeMs - a.mtimeMs);
  return files.slice(0, maxFiles);
}

/**
 * REQ-4 subtask 7.1: transcript CSRF fallback (design §2.3). Scans Antigravity
 * brain transcripts for a DISCOVERED record whose pid/port cross-validate
 * against the live discovery result, preferring the newest file and, within a
 * file, the LAST match (most recent discovery). Discarded tokens (pid/port
 * mismatch, malformed UUID) are never returned. Every failure mode yields null
 * quietly — this path must never crash a statusline-triggered refresh.
 * @param {{pid?:number,port?:number,ports?:Array<number>}} discovery - Live LS info from discoverLanguageServer().
 * @param {object} [opts]
 * @param {string} [opts.brainDir] - Brain root override (test hook).
 * @param {number} [opts.maxFiles] - Candidate-file cap (test hook).
 * @param {number} [opts.maxFileSize] - Per-file size cap in bytes (test hook).
 * @returns {{pid:number,port:number,ports:Array<number>,csrfToken:string,sourceFile:string,mtimeMs:number}|null}
 */
function scanTranscriptForCsrf(discovery, opts = {}) {
  try {
    if (!discovery) return null;
    const brainDir = opts.brainDir || path.join(config.ANTIGRAVITY_DIR, 'brain');
    const candidates = collectTranscriptCandidateFiles(
      brainDir,
      opts.maxFiles || TRANSCRIPT_SCAN_MAX_FILES,
      opts.maxFileSize || TRANSCRIPT_SCAN_MAX_FILE_SIZE
    );
    const deadline = Date.now() + (opts.maxDurationMs || TRANSCRIPT_SCAN_MAX_DURATION_MS);
    for (const cand of candidates) {
      if (Date.now() > deadline) break; // bounded I/O budget exhausted
      let content;
      try {
        content = fs.readFileSync(cand.file, 'utf8');
      } catch (_e) {
        continue; // file vanished/locked between stat and read — skip it
      }
      const hits = extractDiscoveryHits(content);
      // Last match in the newest matching file wins (most recent discovery).
      for (let i = hits.length - 1; i >= 0; i--) {
        const hit = hits[i];
        if (validateTranscriptToken(discovery, hit)) {
          return { ...hit, sourceFile: cand.file, mtimeMs: cand.mtimeMs };
        }
        // pid/port mismatch → discard this token, keep scanning (design §2.5).
      }
    }
    return null;
  } catch (_err) {
    return null;
  }
}

/**
 * REQ-4 subtasks 7.1/7.2: CSRF token downgrade chain (design §2.1/§2.5):
 * command_line (priority 1, existing) → transcript (priority 2, new).
 * `heap_scan` is the reserved priority-3 slot (separate delegation §7.5).
 * Only the token SOURCE KIND is exposed for diagnostics; the token plaintext
 * returned here is for immediate in-memory RPC use and must never be
 * persisted to cache/marker/log files (design §5).
 * @param {{pid?:number,port?:number,ports?:Array<number>,csrfToken?:string,protocol?:string|null}} discovery
 * @param {object} [opts] - Passed through to scanTranscriptForCsrf.
 * @returns {{csrfToken:string,tokenSource:'command_line'|'transcript'|'none',pinnedPorts?:Array<number>,pinnedProtocol?:'https'}}
 */
function resolveCsrfTokenFallback(discovery, opts = {}) {
  try {
    if (!discovery) return { csrfToken: '', tokenSource: 'none' };
    if (discovery.csrfToken) {
      return { csrfToken: discovery.csrfToken, tokenSource: 'command_line' };
    }
    const hit = scanTranscriptForCsrf(discovery, opts);
    if (hit && hit.csrfToken) {
      const pinnedPorts = [hit.port, ...(hit.ports || [])].filter(
        (p, i, a) => typeof p === 'number' && p > 0 && a.indexOf(p) === i
      );
      return {
        csrfToken: hit.csrfToken,
        tokenSource: 'transcript',
        pinnedPorts,
        // The DISCOVERED ports are the LS HTTPS API ports (TLS RPC 200
        // evidence, debug report §2.2) — pin HTTPS and stop the blind
        // plain-HTTP knock that produces "TLS handshake error" console floods.
        pinnedProtocol: 'https'
      };
    }
    return { csrfToken: '', tokenSource: 'none' };
  } catch (_err) {
    return { csrfToken: '', tokenSource: 'none' };
  }
}

/**
 * Executes an RPC POST request against the Language Server over HTTP or HTTPS.
 * @param {object} params
 * @param {string} params.path - RPC endpoint path.
 * @param {number} params.port - Target port.
 * @param {string} [params.csrfToken=''] - CSRF token.
 * @param {string} [params.host='127.0.0.1'] - Target host.
 * @param {string} [params.protocol='https'] - 'https' or 'http'.
 * @param {number} [params.timeoutMs=HTTP_TIMEOUT_MS] - Timeout in ms.
 * @param {boolean} [params.rejectUnauthorized=false] - Whether to reject self-signed certs.
 * @returns {Promise<object>} Parsed JSON response.
 */
function makeRpcRequest({
  path: rpcPath,
  port,
  csrfToken = '',
  host = '127.0.0.1',
  protocol = 'https',
  timeoutMs = HTTP_TIMEOUT_MS,
  rejectUnauthorized = false
}) {
  return new Promise((resolve, reject) => {
    const isHttps = String(protocol).toLowerCase() === 'https';
    if (!isHttps && _detectedHttpsPorts.has(port)) {
      const blockedErr = new Error(`Plaintext HTTP request blocked: port ${port} is confirmed HTTPS endpoint`);
      blockedErr.code = 'ERR_HTTP_BLOCKED_ON_HTTPS';
      return reject(blockedErr);
    }

    const client = isHttps ? https : http;
    const postData = JSON.stringify({});

    const reqOptions = {
      hostname: host,
      port: port,
      path: rpcPath,
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Codeium-Csrf-Token': csrfToken || '',
        'Connect-Protocol-Version': '1',
        'Content-Length': Buffer.byteLength(postData)
      },
      timeout: timeoutMs
    };

    if (isHttps) {
      reqOptions.rejectUnauthorized = rejectUnauthorized;
    }

    let tlsHandshakeSucceeded = false;

    const req = client.request(reqOptions, (res) => {
      if (isHttps) {
        tlsHandshakeSucceeded = true;
        _detectedHttpsPorts.add(port);
      }
      let data = '';
      res.setEncoding('utf8');
      res.on('data', chunk => { data += chunk; });
      res.on('end', () => {
        if (res.statusCode >= 200 && res.statusCode < 300) {
          try {
            const parsed = JSON.parse(data);
            resolve(parsed);
          } catch (e) {
            const parseErr = new Error(`Failed to parse ${rpcPath} response JSON: ${e.message}`);
            parseErr.httpStatus = res.statusCode;
            if (isHttps) parseErr.tlsHandshakeSucceeded = true;
            reject(parseErr);
          }
        } else {
          const httpErr = new Error(`${rpcPath} returned HTTP ${res.statusCode}: ${data}`);
          // REQ-3 Fix 1: tag the HTTP status so callers can distinguish an
          // authentication rejection (401 — server alive, CSRF token missing/
          // invalid) from a genuine "no server" negative probe.
          httpErr.httpStatus = res.statusCode;
          if (isHttps) httpErr.tlsHandshakeSucceeded = true;
          reject(httpErr);
        }
      });
    });

    if (isHttps) {
      req.on('socket', (socket) => {
        socket.once('secureConnect', () => {
          tlsHandshakeSucceeded = true;
          _detectedHttpsPorts.add(port);
        });
      });
    }

    req.on('error', (err) => {
      if (tlsHandshakeSucceeded) {
        err.tlsHandshakeSucceeded = true;
        _detectedHttpsPorts.add(port);
      } else if (isHttps) {
        _detectedHttpsPorts.delete(port);
      }
      reject(err);
    });

    req.on('timeout', () => {
      req.destroy(new Error(`${rpcPath} request timed out after ${timeoutMs}ms`));
    });

    req.write(postData);
    req.end();
  });
}

/**
 * Sends POST HTTP/HTTPS request to /exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary.
 * @param {object} params
 * @param {number} params.port - Language server port.
 * @param {string} [params.csrfToken] - CSRF token.
 * @param {string} [params.host='127.0.0.1'] - Host.
 * @param {string} [params.protocol='https'] - Protocol ('https' or 'http').
 * @param {number} [params.timeoutMs=HTTP_TIMEOUT_MS] - Timeout.
 * @param {boolean} [params.rejectUnauthorized=false] - Whether to reject self-signed certs.
 * @returns {Promise<object>} Parsed JSON response.
 */
function callRetrieveUserQuotaSummary(params = {}) {
  const {
    port,
    csrfToken = '',
    host = '127.0.0.1',
    protocol = 'https',
    timeoutMs = HTTP_TIMEOUT_MS,
    rejectUnauthorized = false
  } = params;

  return makeRpcRequest({
    path: '/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary',
    port,
    csrfToken,
    host,
    protocol,
    timeoutMs,
    rejectUnauthorized
  });
}

/**
 * Sends POST HTTP/HTTPS request to /exa.language_server_pb.LanguageServerService/GetUserStatus.
 * @param {object} params
 * @param {number} params.port - Language server port.
 * @param {string} [params.csrfToken] - CSRF token.
 * @param {string} [params.host='127.0.0.1'] - Host.
 * @param {string} [params.protocol='https'] - Protocol ('https' or 'http').
 * @param {number} [params.timeoutMs=HTTP_TIMEOUT_MS] - Timeout.
 * @param {boolean} [params.rejectUnauthorized=false] - Whether to reject self-signed certs.
 * @returns {Promise<object>} Parsed JSON response.
 */
function callGetUserStatus(params = {}) {
  const {
    port,
    csrfToken = '',
    host = '127.0.0.1',
    protocol = 'https',
    timeoutMs = HTTP_TIMEOUT_MS,
    rejectUnauthorized = false
  } = params;

  return makeRpcRequest({
    path: '/exa.language_server_pb.LanguageServerService/GetUserStatus',
    port,
    csrfToken,
    host,
    protocol,
    timeoutMs,
    rejectUnauthorized
  });
}

/**
 * Extracts 5h and 7d/Weekly Gemini Quota buckets from RetrieveUserQuotaSummary response.
 * @param {object} summaryPayload - JSON response from RetrieveUserQuotaSummary.
 * @param {Date} [refDate=new Date()] - Reference date.
 * @returns {object|null}
 */
function extractQuotaFromSummary(summaryPayload, refDate = new Date()) {
  if (!summaryPayload || typeof summaryPayload !== 'object') {
    return null;
  }

  const groups =
    (summaryPayload.response && summaryPayload.response.groups) ||
    summaryPayload.groups ||
    (summaryPayload.userStatus && summaryPayload.userStatus.groups) ||
    (summaryPayload.user && summaryPayload.user.groups) ||
    [];

  if (!Array.isArray(groups) || groups.length === 0) {
    return null;
  }

  // Find Gemini group
  let geminiGroup = null;
  for (const g of groups) {
    if (!g || typeof g !== 'object') continue;
    const name = (g.displayName || g.name || g.id || '').toLowerCase();
    if (/gemini|google/i.test(name)) {
      geminiGroup = g;
      break;
    }
  }

  // Fallback check if any group has gemini bucket
  if (!geminiGroup) {
    for (const g of groups) {
      if (!g || !Array.isArray(g.buckets)) continue;
      const hasGeminiBucket = g.buckets.some(b => /gemini/i.test(b.bucketId || '') || /gemini/i.test(b.displayName || ''));
      if (hasGeminiBucket) {
        geminiGroup = g;
        break;
      }
    }
  }

  // Fallback to first non-3p group
  if (!geminiGroup && groups.length > 0) {
    const firstName = (groups[0].displayName || groups[0].name || '').toLowerCase();
    if (!/claude|gpt|openai|3p/i.test(firstName)) {
      geminiGroup = groups[0];
    }
  }

  if (!geminiGroup || !Array.isArray(geminiGroup.buckets) || geminiGroup.buckets.length === 0) {
    return null;
  }

  let bucket5h = null;
  let bucket7d = null;

  for (const b of geminiGroup.buckets) {
    if (!b || typeof b !== 'object') continue;
    const win = (b.window || '').toLowerCase();
    const id = (b.bucketId || b.id || '').toLowerCase();
    const disp = (b.displayName || b.name || '').toLowerCase();

    if (win === '5h' || /5h|five/i.test(id) || /five\s*hour|5\s*h/i.test(disp)) {
      bucket5h = b;
    } else if (win === 'weekly' || win === '7d' || /weekly|7d/i.test(id) || /week|7\s*day/i.test(disp)) {
      bucket7d = b;
    }
  }

  // If buckets didn't match window name, map by order if 2 buckets exist
  if (!bucket5h && !bucket7d && geminiGroup.buckets.length >= 2) {
    bucket7d = geminiGroup.buckets[0];
    bucket5h = geminiGroup.buckets[1];
  } else if (!bucket5h && geminiGroup.buckets.length === 1) {
    bucket5h = geminiGroup.buckets[0];
  }

  const parseBucket = (bucket, fallbackWindow) => {
    if (!bucket) return null;
    const rawFraction = bucket.remainingFraction !== undefined ? bucket.remainingFraction : bucket.remaining_fraction;
    if (rawFraction === undefined || rawFraction === null || isNaN(rawFraction)) {
      return null;
    }
    const remainingFraction = Math.max(0, Math.min(1.0, Number(rawFraction)));
    const remainPercent = Math.round(remainingFraction * 100);
    const resetTime = bucket.resetTime || bucket.reset_time || null;
    const { resetInSeconds, resetFormatted } = formatResetTime(resetTime, refDate);
    return {
      remainPercent,
      remainingFraction,
      resetTime,
      resetInSeconds,
      resetFormatted,
      displayName: bucket.displayName || bucket.name || (fallbackWindow === '5h' ? '5-Hour Limit' : 'Weekly Limit'),
      window: bucket.window || fallbackWindow,
      bucketId: bucket.bucketId || bucket.id || null
    };
  };

  const parsed5h = parseBucket(bucket5h, '5h');
  const parsed7d = parseBucket(bucket7d, 'weekly');

  if (!parsed5h && !parsed7d) {
    return null;
  }

  const primary = parsed5h || parsed7d;

  return {
    quota5h: parsed5h,
    quota7d: parsed7d,
    remainPercent: primary.remainPercent,
    remainingFraction: primary.remainingFraction,
    resetTime: primary.resetTime,
    resetInSeconds: primary.resetInSeconds,
    resetFormatted: primary.resetFormatted,
    modelLabel: geminiGroup.displayName || 'Gemini Models',
    isLive: true,
    source: 'language_server'
  };
}

/**
 * Extracts Gemini Quota pool metrics from Language Server GetUserStatus response (legacy fallback).
 * @param {object} userStatusPayload - GetUserStatus JSON response.
 * @param {Date} [refDate=new Date()] - Reference date for countdown calculation.
 * @returns {object|null} Extracted Gemini quota object or null.
 */
function extractGeminiQuotaFromStatus(userStatusPayload, refDate = new Date()) {
  if (!userStatusPayload || typeof userStatusPayload !== 'object') {
    return null;
  }

  // Model configs can appear in userStatus, user, or top-level
  const configs =
    userStatusPayload.clientModelConfigs ||
    (userStatusPayload.userStatus && userStatusPayload.userStatus.clientModelConfigs) ||
    (userStatusPayload.user && userStatusPayload.user.clientModelConfigs) ||
    userStatusPayload.modelConfigs ||
    [];

  if (!Array.isArray(configs) || configs.length === 0) {
    return null;
  }

  // Find Gemini quota pool entries
  let targetGeminiConfig = null;
  let targetClaudeConfig = null;

  for (const cfg of configs) {
    if (!cfg || typeof cfg !== 'object') continue;
    const name = (cfg.label || cfg.model || cfg.modelOrTier || cfg.name || cfg.id || '').toLowerCase();
    const quotaInfo = cfg.quotaInfo || cfg.quota_info || cfg.quota || (cfg.modelConfig && cfg.modelConfig.quotaInfo);

    if (!quotaInfo) continue;

    if (/gemini|google/i.test(name) || !/claude|anthropic|openai|gpt|o3|o1/i.test(name)) {
      if (!targetGeminiConfig || (quotaInfo.remainingFraction !== undefined && targetGeminiConfig.quotaInfo && quotaInfo.remainingFraction < targetGeminiConfig.quotaInfo.remainingFraction)) {
        targetGeminiConfig = { ...cfg, quotaInfo };
      }
    } else if (/claude|anthropic/i.test(name)) {
      targetClaudeConfig = { ...cfg, quotaInfo };
    }
  }

  if (!targetGeminiConfig || !targetGeminiConfig.quotaInfo) {
    return null;
  }

  const qInfo = targetGeminiConfig.quotaInfo;
  const rawFraction = qInfo.remainingFraction !== undefined ? qInfo.remainingFraction : qInfo.remaining_fraction;
  if (rawFraction === undefined || rawFraction === null || isNaN(rawFraction)) {
    return null;
  }

  const remainingFraction = Math.max(0, Math.min(1.0, Number(rawFraction)));
  const remainPercent = Math.round(remainingFraction * 100);
  const resetTime = qInfo.resetTime || qInfo.reset_time || qInfo.resetTimestamp || null;
  const { resetInSeconds, resetFormatted } = formatResetTime(resetTime, refDate);

  const quota5h = {
    remainPercent,
    remainingFraction,
    resetTime,
    resetInSeconds,
    resetFormatted,
    displayName: targetGeminiConfig.label || targetGeminiConfig.model || 'Gemini Quota',
    window: '5h'
  };

  return {
    remainPercent,
    remainingFraction,
    resetTime,
    resetInSeconds,
    resetFormatted,
    modelLabel: targetGeminiConfig.label || targetGeminiConfig.model || 'Gemini Quota Pool',
    quota5h,
    quota7d: null,
    isLive: true,
    source: 'language_server'
  };
}

/**
 * Unified extractor for Language Server responses (supports both RetrieveUserQuotaSummary and GetUserStatus).
 * @param {object} payload - RPC response JSON.
 * @param {Date} [refDate=new Date()] - Reference date.
 * @returns {object|null}
 */
function extractGeminiQuotaFromPayload(payload, refDate = new Date()) {
  if (!payload || typeof payload !== 'object') return null;
  // 1. Try RetrieveUserQuotaSummary schema
  const summaryQuota = extractQuotaFromSummary(payload, refDate);
  if (summaryQuota) return summaryQuota;
  // 2. Fall back to GetUserStatus schema
  return extractGeminiQuotaFromStatus(payload, refDate);
}

/**
 * Atomically saves quota data to ~/.gemini/gemini_quota_cache.json.
 * @param {object} quotaData
 * @param {string} [cachePath=GEMINI_QUOTA_CACHE_FILE]
 * @returns {object|null}
 */
function saveCachedGeminiQuota(quotaData, cachePath = GEMINI_QUOTA_CACHE_FILE) {
  try {
    const dir = path.dirname(cachePath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
    const payload = {
      version: 2,
      timestamp: new Date().toISOString(),
      timestampMs: Date.now(),
      ...quotaData
    };
    const content = JSON.stringify(payload, null, 2);
    const tmp = `${cachePath}.${Date.now()}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, content, 'utf8');
    try {
      fs.renameSync(tmp, cachePath);
    } catch (_e) {
      fs.writeFileSync(cachePath, content, 'utf8');
      try { fs.unlinkSync(tmp); } catch (_ign) {}
    }
    return payload;
  } catch (_err) {
    return null;
  }
}

/**
 * Synchronously reads cached Gemini quota from ~/.gemini/gemini_quota_cache.json.
 * Recalculates countdown durations in real-time.
 * Designed for ultra-fast (<1ms) statusline and hook reading.
 * @param {string} [cachePath=GEMINI_QUOTA_CACHE_FILE]
 * @param {number} [ttlMs=CACHE_TTL_MS]
 * @returns {object|null}
 */
function getCachedGeminiQuota(cachePath = GEMINI_QUOTA_CACHE_FILE, ttlMs = CACHE_TTL_MS) {
  try {
    if (!fs.existsSync(cachePath)) {
      return null;
    }
    const raw = fs.readFileSync(cachePath, 'utf8');
    const data = JSON.parse(raw);
    if (!data || typeof data !== 'object') return null;

    const age = Date.now() - (data.timestampMs || 0);
    const isAuthFailure = Boolean(data.lastError && typeof data.lastError === 'object' && data.lastError.kind === 'auth_failure');
    const isStale = age >= STALE_RETRY_AFTER_MS || age < 0 || isAuthFailure;
    const isFresh = age < ttlMs && age >= 0 && !isAuthFailure;

    let resetFormatted = data.resetFormatted;
    let resetInSeconds = data.resetInSeconds;
    if (data.resetTime) {
      const remainingSec = Math.max(0, Math.floor((new Date(data.resetTime).getTime() - Date.now()) / 1000));
      resetInSeconds = remainingSec;
      resetFormatted = formatCountdownDuration(remainingSec);
    }

    if (data.quota5h && data.quota5h.resetTime) {
      const remainingSec = Math.max(0, Math.floor((new Date(data.quota5h.resetTime).getTime() - Date.now()) / 1000));
      data.quota5h.resetInSeconds = remainingSec;
      data.quota5h.resetFormatted = formatCountdownDuration(remainingSec);
    }

    if (data.quota7d && data.quota7d.resetTime) {
      const remainingSec = Math.max(0, Math.floor((new Date(data.quota7d.resetTime).getTime() - Date.now()) / 1000));
      data.quota7d.resetInSeconds = remainingSec;
      data.quota7d.resetFormatted = formatCountdownDuration(remainingSec);
    }

    return {
      ...data,
      resetFormatted,
      resetInSeconds,
      isFresh,
      isStale,
      ageMs: age
    };
  } catch (_err) {
    return null;
  }
}

/**
 * Triggers non-blocking background quota refresh to keep the cache warm.
 * Throttled to prevent spawning multiple background child processes.
 * @param {object} [opts]
 * @returns {boolean} Whether a refresh process was spawned.
 */
function triggerBackgroundQuotaRefresh(opts = {}) {
  const now = Date.now();
  if (now - _lastBackgroundTriggerAt < BACKGROUND_TRIGGER_THROTTLE_MS) {
    return false;
  }
  _lastBackgroundTriggerAt = now;

  try {
    const script = `require('${__filename.replace(/\\/g, '/')}').fetchLiveGeminiQuota().catch(()=>{})`;
    const child = spawn(process.execPath, ['-e', script], {
      detached: true,
      stdio: 'ignore',
      windowsHide: true
    });
    child.unref();
    return true;
  } catch (_e) {
    return false;
  }
}

/**
 * Fetches live Gemini quota pool from local Language Server, caches to disk, and returns result.
 * Tries /RetrieveUserQuotaSummary first, then falls back to /GetUserStatus.
 * @param {object} [opts]
 * @param {number} [opts.port] - Explicit port override.
 * @param {Array<number>} [opts.ports] - Candidate ports override.
 * @param {string} [opts.csrfToken] - Explicit CSRF token override.
 * @param {string} [opts.host='127.0.0.1'] - Host override.
 * @param {number} [opts.timeoutMs] - Request timeout.
 * @param {string} [opts.cachePath] - Custom cache file path.
 * @param {function} [opts.fetcher] - Custom fetcher function for unit testing.
 * @param {function} [opts.discover] - Custom discovery function for unit testing (same contract as discoverLanguageServer).
 * @param {object} [opts.transcriptScan] - Override forwarded to scanTranscriptForCsrf (e.g. {brainDir} test hook).
 * @param {Date} [opts.refDate] - Reference date.
 * @returns {Promise<object>} Quota result object.
 */
async function fetchLiveGeminiQuota(opts = {}) {
  const cachePath = opts.cachePath || GEMINI_QUOTA_CACHE_FILE;
  const refDate = opts.refDate || new Date();

  try {
    let port = opts.port;
    let csrfToken = opts.csrfToken;
    let discoveredProtocol = null;
    let candidatePorts = opts.ports || (port ? [port] : []);
    // REQ-4 subtask 7.2 (design §3.2): which token source serves this attempt.
    // Enum: 'command_line' | 'transcript' | 'heap_scan' (reserved, §7.5) | 'none'.
    let tokenSource = csrfToken ? 'command_line' : 'none';
    // REQ-4 item C: transcript DISCOVERED ports are the LS's own HTTPS API
    // ports — when known, the probe set is PINNED to them over HTTPS only,
    // removing the blind plain-HTTP knock that makes agy.exe log
    // "http: TLS handshake error ... client sent an HTTP request to an HTTPS server".
    let pinnedHttpsOnly = false;
    let discoveredPid;
    let discoveredPids = [];

    // FY-2026-09-12-001: honor the negative-probe cooldown BEFORE spawning any
    // discovery work — this is what stops the TLS-handshake-error flood while
    // Antigravity's language server is not running. Explicit callers (unit
    // tests passing port/ports/fetcher, or forceRefresh) bypass the cooldown.
    const hasExplicitTarget = Boolean(opts.port || (Array.isArray(opts.ports) && opts.ports.length > 0) || typeof opts.fetcher === 'function');
    const cooldownInfo = (!opts.forceRefresh && !hasExplicitTarget)
      ? readProbeCooldownInfo(opts.cooldownMarker)
      : null;
    if (cooldownInfo && !shouldBypassCooldownForStale(readQuotaSnapshotTimestamp(cachePath), cooldownInfo.setAtMs)) {
      return {
        isLive: false,
        remainPercent: null,
        remainingFraction: null,
        resetTime: null,
        resetFormatted: null,
        resetInSeconds: null,
        quota5h: null,
        quota7d: null,
        error: `Language server probe cooldown active (retries suppressed for ${Math.round(PROBE_COOLDOWN_MS / 60000)} min after a failed scan)`,
        source: 'cooldown',
        cooldownReason: cooldownInfo.reason,
        cacheFile: cachePath
      };
    }
    // REQ-3 Fix 3: reached here either with no cooldown or via the stale-escape
    // bypass (cache older than STALE_RETRY_AFTER_MS and marker at least as old),
    // so at most ONE discovery burst per STALE_RETRY_AFTER_MS window can leak
    // past an active cooldown — bounded, no TLS flood regression.

    if (!port && candidatePorts.length === 0) {
      const discoverFn = typeof opts.discover === 'function' ? opts.discover : discoverLanguageServer;
      const discovered = await discoverFn();
      if (discovered) {
        candidatePorts = discovered.ports && discovered.ports.length > 0 ? discovered.ports : [discovered.port];
        port = port || discovered.port;
        csrfToken = csrfToken !== undefined ? csrfToken : discovered.csrfToken;
        discoveredProtocol = discovered.protocol || null;
        discoveredPid = discovered.pid;
        discoveredPids = Array.isArray(discovered.pids) && discovered.pids.length > 0
          ? discovered.pids
          : (discovered.pid ? [discovered.pid] : []);
        if (csrfToken) tokenSource = 'command_line';
      }
    }

    // REQ-4 subtasks 7.1/7.2 + C: CSRF downgrade chain (design §2.5):
    // command_line (priority 1, existing) → transcript (priority 2, new).
    // heap_scan stays a reserved slot (separate delegation §7.5). Invoked ONLY
    // when the command line yielded no token AND this is a discovery-driven
    // attempt — explicit-target probes (unit tests, forced port) keep the
    // hermetic behaviour they had before this feature. A transcript hit
    // replaces the candidate ports with its validated port list and pins
    // HTTPS, so a plain HTTP request is never sent against an HTTPS port.
    if (!csrfToken && candidatePorts.length > 0 && !hasExplicitTarget) {
      const fb = resolveCsrfTokenFallback(
        { pid: discoveredPid, pids: discoveredPids, ports: candidatePorts, port: candidatePorts[0], csrfToken: '' },
        opts.transcriptScan || {}
      );
      tokenSource = fb.tokenSource;
      if (fb.csrfToken) {
        csrfToken = fb.csrfToken;
        if (Array.isArray(fb.pinnedPorts) && fb.pinnedPorts.length > 0) {
          candidatePorts = fb.pinnedPorts.slice();
          port = fb.pinnedPorts[0];
        }
        pinnedHttpsOnly = fb.pinnedProtocol === 'https';
      }
    }

    if (candidatePorts.length === 0) {
      // No server found: persist the cooldown so the next statusline renders
      // stop hammering loopback with HTTPS-first probes (TLS flood root cause).
      // Explicit-target probes (tests/forced port) never write the marker.
      // REQ-4 §3.2: record the REAL token-source verdict (no discovery token
      // and no candidate ports means the fallback chain could not run) instead
      // of the legacy default, which mislabelled this case 'command_line'.
      if (!hasExplicitTarget) {
        writeProbeCooldown(opts.cooldownMarker, { tokenSource });
      }
      return {
        isLive: false,
        remainPercent: null,
        remainingFraction: null,
        resetTime: null,
        resetFormatted: null,
        resetInSeconds: null,
        quota5h: null,
        quota7d: null,
        error: 'Language Server not running or listening port not found',
        source: 'fallback',
        cacheFile: cachePath
      };
    }

    let protocols;
    if (opts.protocols && Array.isArray(opts.protocols) && opts.protocols.length > 0) {
      protocols = opts.protocols;
    } else if (pinnedHttpsOnly) {
      // REQ-4 item C: token recovered from a transcript DISCOVERED record —
      // its ports are known HTTPS-only (TLS RPC 200 evidence, debug report
      // §2.2). Probing them over plain HTTP is exactly what produced the
      // "TLS handshake error" console flood. Pin HTTPS; no HTTP fallback.
      protocols = ['https'];
    } else if (opts.protocol) {
      protocols = [opts.protocol];
    } else if (discoveredProtocol) {
      // REQ-4 item C (command_line side): the command line told us the LS API
      // URL protocol — knock ONLY that protocol. Probing the opposite protocol
      // on a known port is the blind port×protocol loop that produced the
      // `TLS handshake error ... HTTP request to an HTTPS server` console
      // flood when the HTTPS probe failed with 401.
      protocols = [discoveredProtocol];
    } else {
      // No token and no port intelligence: total probe loop retained (the
      // legacy 'none' path, flood-guarded by the 10m cooldown marker).
      protocols = ['https', 'http'];
    }

    // Fallback to environment variables when not specified in command line or opts
    if (!csrfToken) {
      if (process.env.ANTIGRAVITY_CSRF_TOKEN && typeof process.env.ANTIGRAVITY_CSRF_TOKEN === 'string') {
        const t = process.env.ANTIGRAVITY_CSRF_TOKEN.trim();
        if (t) csrfToken = t;
      }
      if (!csrfToken && process.env.CSRF_TOKEN && typeof process.env.CSRF_TOKEN === 'string') {
        const t = process.env.CSRF_TOKEN.trim();
        if (t) csrfToken = t;
      }
    }

    const rejectUnauthorized = opts.rejectUnauthorized !== undefined ? opts.rejectUnauthorized : false;
    let quota = null;
    // REQ-3 Fix 1: classify 401 (auth) failures separately from transport
    // errors so the cooldown marker records WHY live fetch keeps failing.
    let sawAuthFailure = false;
    let authFailureDetail = null;
    const hasValidCsrf = Boolean(csrfToken && typeof csrfToken === 'string' && csrfToken.trim());

    if (typeof opts.fetcher === 'function') {
      const data = await opts.fetcher({
        port: candidatePorts[0],
        csrfToken,
        protocol: protocols[0]
      });
      quota = extractGeminiQuotaFromPayload(data, refDate);
    } else if (!hasValidCsrf) {
      // Unauthenticated probe skip: Go Language Server strictly rejects requests
      // without CSRF token ('missing CSRF token' / HTTP 401). Skip unauthenticated
      // RPC calls to eliminate pointless network noise and prevent handshake issues.
      sawAuthFailure = true;
      authFailureDetail = 'HTTP 401 (missing CSRF token)';
    } else {
      outerLoop:
      for (const p of candidatePorts) {
        let portIsHttps = false;
        for (const proto of protocols) {
          if (proto === 'http' && (portIsHttps || _detectedHttpsPorts.has(p))) {
            // NEVER attempt plaintext HTTP on a port that already responded over HTTPS
            break;
          }

          // 1. Try RetrieveUserQuotaSummary
          try {
            const summaryData = await callRetrieveUserQuotaSummary({
              port: p,
              csrfToken,
              host: opts.host,
              protocol: proto,
              timeoutMs: opts.timeoutMs,
              rejectUnauthorized
            });
            if (proto === 'https') {
              portIsHttps = true;
              _detectedHttpsPorts.add(p);
            }
            quota = extractGeminiQuotaFromPayload(summaryData, refDate);
            if (quota) break outerLoop;
          } catch (summaryErr) {
            if (proto === 'https') {
              if (summaryErr?.httpStatus !== undefined || summaryErr?.tlsHandshakeSucceeded) {
                portIsHttps = true;
                _detectedHttpsPorts.add(p);
              } else {
                portIsHttps = false;
                _detectedHttpsPorts.delete(p);
              }
            }
            if (summaryErr && summaryErr.httpStatus === 401) {
              sawAuthFailure = true;
              authFailureDetail = authFailureDetail || String(summaryErr.message || '').slice(0, 300);
            }
            // Try GetUserStatus or next protocol
          }

          // 2. Try GetUserStatus
          if (!quota) {
            try {
              const statusData = await callGetUserStatus({
                port: p,
                csrfToken,
                host: opts.host,
                protocol: proto,
                timeoutMs: opts.timeoutMs,
                rejectUnauthorized
              });
              if (proto === 'https') {
                portIsHttps = true;
                _detectedHttpsPorts.add(p);
              }
              quota = extractGeminiQuotaFromPayload(statusData, refDate);
              if (quota) break outerLoop;
            } catch (statusErr) {
              if (proto === 'https') {
                if (statusErr?.httpStatus !== undefined || statusErr?.tlsHandshakeSucceeded) {
                  portIsHttps = true;
                  _detectedHttpsPorts.add(p);
                } else if (!portIsHttps) {
                  _detectedHttpsPorts.delete(p);
                }
              }
              if (statusErr && statusErr.httpStatus === 401) {
                sawAuthFailure = true;
                authFailureDetail = authFailureDetail || String(statusErr.message || '').slice(0, 300);
              }
              // Try next protocol / port
            }
          }

          if (proto === 'https' && (portIsHttps || _detectedHttpsPorts.has(p))) {
            // Port responded via HTTPS (whether 200, 401, 403, or any HTTP status code),
            // or TLS handshake succeeded. DO NOT attempt plaintext HTTP on that port!
            break;
          }
        }
      }
    }

    if (!quota) {
      // Probed every candidate port/protocol but no live quota came back:
      // persist cooldown so subsequent statusline renders stay silent.
      // Explicit-target probes (tests/forced port) never write the marker.
      // REQ-3 Fix 1: an HTTP 401 means the server IS alive and only auth
      // failed — record that distinctly from a genuine probe failure.
      const authFailure = typeof sawAuthFailure !== 'undefined' ? sawAuthFailure : false;
      const cooldownReason = authFailure ? 'auth_failure' : 'language_server_probe_failed';
      const errorKind = authFailure ? 'auth_failure' : 'probe_failed';
      const errorDetail = authFailure
        ? (authFailureDetail || 'HTTP 401 (missing CSRF token)')
        : 'No Gemini quota pool found in Language Server response';
      if (!hasExplicitTarget) {
        writeProbeCooldown(opts.cooldownMarker, {
          reason: cooldownReason,
          detail: errorDetail,
          tokenSource
        });
      }
      // REQ-3 Fix 3: stamp attempt diagnostics into the existing cache
      // (no-op when there is no cache file yet).
      recordQuotaAttemptFailure({ errorKind, errorDetail }, cachePath);
      return {
        isLive: false,
        remainPercent: null,
        remainingFraction: null,
        resetTime: null,
        resetFormatted: null,
        resetInSeconds: null,
        quota5h: null,
        quota7d: null,
        error: authFailure
          ? `Language Server rejected the CSRF token (HTTP 401): ${errorDetail}`
          : 'No Gemini quota pool found in Language Server response',
        errorKind,
        tokenSource,
        source: 'fallback',
        cacheFile: cachePath
      };
    }

    // Success: clear any stale cooldown marker so future failures re-probe fast.
    clearProbeCooldown(opts.cooldownMarker);

    // REQ-4 §3.3/§5: the cache records the token SOURCE KIND only — agy-tools
    // never persists the token plaintext.
    const saved = saveCachedGeminiQuota({ ...quota, tokenSource }, cachePath);
    return {
      ...saved,
      isLive: true,
      cacheFile: cachePath
    };
  } catch (err) {
    // REQ-3 Fix 3: even unexpected exceptions get stamped as attempt
    // diagnostics so `agy-tokens --hook --json` can surface them.
    recordQuotaAttemptFailure({ errorKind: 'exception', errorDetail: String(err && err.message || err) }, cachePath);
    return {
      isLive: false,
      remainPercent: null,
      remainingFraction: null,
      resetTime: null,
      resetFormatted: null,
      resetInSeconds: null,
      quota5h: null,
      quota7d: null,
      error: err.message,
      errorKind: 'exception',
      source: 'fallback',
      cacheFile: cachePath
    };
  }
}

/**
 * Unified resolver for Gemini quota: tries fresh cache, or fetches live if required.
 * @param {object} [opts]
 * @returns {Promise<object|null>}
 */
async function getGeminiQuota(opts = {}) {
  const cached = getCachedGeminiQuota(opts.cachePath, opts.ttlMs);
  if (cached && cached.isFresh) {
    return cached;
  }
  // If cache is stale or missing, trigger non-blocking refresh and return existing cache if available
  if (cached) {
    triggerBackgroundQuotaRefresh();
    return cached;
  }
  // If no cache at all and sync requested
  if (opts.sync) {
    return await fetchLiveGeminiQuota(opts);
  }
  // Trigger background refresh and return fallback
  triggerBackgroundQuotaRefresh();
  return null;
}

/**
 * Clears the set of detected HTTPS ports (useful for test isolation).
 */
function clearDetectedHttpsPorts() {
  _detectedHttpsPorts.clear();
}

/**
 * Returns array of detected HTTPS ports.
 * @returns {Array<number>}
 */
function getDetectedHttpsPorts() {
  return Array.from(_detectedHttpsPorts);
}

module.exports = {
  GEMINI_QUOTA_CACHE_FILE,
  CACHE_TTL_MS,
  PROBE_COOLDOWN_MARKER,
  PROBE_COOLDOWN_MS,
  STALE_RETRY_AFTER_MS,
  readProbeCooldown,
  readProbeCooldownInfo,
  readQuotaSnapshotTimestamp,
  shouldBypassCooldownForStale,
  recordQuotaAttemptFailure,
  writeProbeCooldown,
  clearProbeCooldown,
  clearDetectedHttpsPorts,
  getDetectedHttpsPorts,
  formatCountdownDuration,
  formatResetTime,
  parseCommandLine,
  extractPortsFromNetstat,
  extractPortFromNetstat,
  extractPortFromLsof,
  extractPortsFromLsof,
  discoverLanguageServer,
  isCsrfUuid,
  extractDiscoveryHits,
  validateTranscriptToken,
  collectTranscriptCandidateFiles,
  scanTranscriptForCsrf,
  resolveCsrfTokenFallback,
  makeRpcRequest,
  callRetrieveUserQuotaSummary,
  callGetUserStatus,
  extractQuotaFromSummary,
  extractGeminiQuotaFromStatus,
  extractGeminiQuotaFromPayload,
  saveCachedGeminiQuota,
  getCachedGeminiQuota,
  triggerBackgroundQuotaRefresh,
  fetchLiveGeminiQuota,
  getGeminiQuota
};
