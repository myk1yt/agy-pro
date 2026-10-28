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
  if (!cmdLine || typeof cmdLine !== 'string') {
    return { csrfToken: null, port: null, protocol: null };
  }

  let csrfToken = null;
  const tokenMatch = cmdLine.match(/--csrf[_-]token(?:=|\s+)([a-zA-Z0-9_-]+)/i);
  if (tokenMatch && tokenMatch[1]) {
    csrfToken = tokenMatch[1].trim();
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
      // Windows Discovery: PowerShell with EncodedCommand checking language_server, agy, or csrf_token
      const script = `Get-CimInstance Win32_Process | Where-Object { $_.Name -like '*language_server*' -or $_.Name -like '*agy*' -or $_.CommandLine -like '*language_server*' -or $_.CommandLine -like '*csrf*' } | Select-Object ProcessId, CommandLine | ConvertTo-Json -Compress`;
      const encoded = Buffer.from(script, 'utf16le').toString('base64');
      exec(`powershell -NoProfile -NonInteractive -EncodedCommand ${encoded}`, { timeout: timeoutMs, windowsHide: true }, (err, stdout) => {
        if (err || !stdout || !stdout.trim()) {
          return finish(null);
        }
        try {
          let items = JSON.parse(stdout.trim());
          if (!Array.isArray(items)) items = [items];
          for (const item of items) {
            if (!item || !item.CommandLine) continue;
            const pid = item.ProcessId;
            const { csrfToken, port, protocol } = parseCommandLine(item.CommandLine);
            if (port) {
              return finish({ pid, port, ports: [port], csrfToken: csrfToken || '', protocol: protocol || null });
            }
            // Port not in commandline, query netstat
            try {
              const netstatOut = execSync(`netstat -ano -p tcp`, { encoding: 'utf8', timeout: 1500, windowsHide: true });
              const netPorts = extractPortsFromNetstat(netstatOut, pid);
              if (netPorts.length > 0) {
                return finish({ pid, port: netPorts[0], ports: netPorts, csrfToken: csrfToken || '', protocol: protocol || null });
              }
            } catch (_ne) {
              // Ignore netstat error
            }
          }
        } catch (_pe) {
          // Ignore JSON parse error
        }
        finish(null);
      });
    } else {
      // POSIX Discovery: ps -ax -o pid,command
      exec(`ps -ax -o pid,command`, { timeout: timeoutMs, windowsHide: true }, (err, stdout) => {
        if (err || !stdout) return finish(null);
        const lines = stdout.split('\n');
        for (const line of lines) {
          if ((/language_server/i.test(line) || /agy/i.test(line) || /csrf/i.test(line)) && !/grep|ps -ax/i.test(line)) {
            const trimmed = line.trim();
            const spaceIdx = trimmed.indexOf(' ');
            if (spaceIdx > 0) {
              const pid = parseInt(trimmed.substring(0, spaceIdx), 10);
              const cmdLine = trimmed.substring(spaceIdx + 1);
              const { csrfToken, port, protocol } = parseCommandLine(cmdLine);
              if (port) {
                return finish({ pid, port, ports: [port], csrfToken: csrfToken || '', protocol: protocol || null });
              }
              // Port not in commandline, query lsof
              try {
                const lsofOut = execSync(`lsof -a -p ${pid} -iTCP -sTCP:LISTEN -P -n`, { encoding: 'utf8', timeout: 1000, windowsHide: true });
                const lsofPort = extractPortFromLsof(lsofOut);
                if (lsofPort) {
                  return finish({ pid, port: lsofPort, ports: [lsofPort], csrfToken: csrfToken || '', protocol: protocol || null });
                }
              } catch (_le) {
                // Ignore lsof error
              }
            }
          }
        }
        finish(null);
      });
    }
  });
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

    const req = client.request(reqOptions, (res) => {
      let data = '';
      res.setEncoding('utf8');
      res.on('data', chunk => { data += chunk; });
      res.on('end', () => {
        if (res.statusCode >= 200 && res.statusCode < 300) {
          try {
            const parsed = JSON.parse(data);
            resolve(parsed);
          } catch (e) {
            reject(new Error(`Failed to parse ${rpcPath} response JSON: ${e.message}`));
          }
        } else {
          reject(new Error(`${rpcPath} returned HTTP ${res.statusCode}: ${data}`));
        }
      });
    });

    req.on('error', (err) => {
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
    const isFresh = age < ttlMs && age >= 0;

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

    if (!port && candidatePorts.length === 0) {
      const discovered = await discoverLanguageServer();
      if (discovered) {
        candidatePorts = discovered.ports && discovered.ports.length > 0 ? discovered.ports : [discovered.port];
        port = port || discovered.port;
        csrfToken = csrfToken !== undefined ? csrfToken : discovered.csrfToken;
        discoveredProtocol = discovered.protocol || null;
      }
    }

    if (candidatePorts.length === 0) {
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
    } else if (opts.protocol) {
      protocols = [opts.protocol];
    } else if (discoveredProtocol) {
      protocols = [discoveredProtocol, discoveredProtocol === 'https' ? 'http' : 'https'];
    } else {
      protocols = ['https', 'http'];
    }

    const rejectUnauthorized = opts.rejectUnauthorized !== undefined ? opts.rejectUnauthorized : false;
    let quota = null;

    if (typeof opts.fetcher === 'function') {
      const data = await opts.fetcher({
        port: candidatePorts[0],
        csrfToken,
        protocol: protocols[0]
      });
      quota = extractGeminiQuotaFromPayload(data, refDate);
    } else {
      outerLoop:
      for (const p of candidatePorts) {
        for (const proto of protocols) {
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
            quota = extractGeminiQuotaFromPayload(summaryData, refDate);
            if (quota) break outerLoop;
          } catch (_summaryErr) {
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
              quota = extractGeminiQuotaFromPayload(statusData, refDate);
              if (quota) break outerLoop;
            } catch (_statusErr) {
              // Try next protocol / port
            }
          }
        }
      }
    }

    if (!quota) {
      return {
        isLive: false,
        remainPercent: null,
        remainingFraction: null,
        resetTime: null,
        resetFormatted: null,
        resetInSeconds: null,
        quota5h: null,
        quota7d: null,
        error: 'No Gemini quota pool found in Language Server response',
        source: 'fallback',
        cacheFile: cachePath
      };
    }

    const saved = saveCachedGeminiQuota(quota, cachePath);
    return {
      ...saved,
      isLive: true,
      cacheFile: cachePath
    };
  } catch (err) {
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

module.exports = {
  GEMINI_QUOTA_CACHE_FILE,
  CACHE_TTL_MS,
  formatCountdownDuration,
  formatResetTime,
  parseCommandLine,
  extractPortsFromNetstat,
  extractPortFromNetstat,
  extractPortFromLsof,
  discoverLanguageServer,
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
