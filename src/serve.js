/**
 * @fileoverview Local SSE dashboard server for the Antigravity Token & Cost
 * Tracker. Core http server bound to 127.0.0.1 ONLY (C6 — token usage data is
 * personal; never bind 0.0.0.0). Routes:
 *   GET /          -> dashboard.html (Cache-Control: no-store)
 *   GET /events    -> SSE stream; re-aggregates every 5s, pushes payload
 *   GET /data.json -> dashboard-data.json
 * Port auto-increments on EADDRINUSE (up to 10 tries); --port 0 = random (E5).
 * Zero dependencies (Node core: http, fs only).
 */

const http = require('http');
const fs = require('fs');
const path = require('path');
const config = require('./config');
const {
  DASHBOARD_HTML_FILE,
  DASHBOARD_DATA_JSON,
  DASHBOARD_DATA_JS,
  DASHBOARD_DEFAULT_PORT,
  CACHE_FILE,
  BRAIN_DIR,
  GEMINI_QUOTA_CACHE_FILE
} = config;
const { syncSessions, CACHE_SCHEMA_VERSION } = require('./cache-manager');
const { buildDashboardPayload, renderDashboardHtml, writeDashboardFiles } = require('./html-report');
// Acyclic dependency: dashboard-link requires html-report and config only (never serve.js)
const { removePortFileIfPort } = require('./dashboard-link');
const staleness = require('./serve-staleness');
const geminiQuotaModule = require('./gemini-quota');

const SSE_INTERVAL_MS = 5000;
const STALENESS_WATCHDOG_MS = 30000; // REQ-101/105: <=60s; 30s chosen for responsive self-termination
const IDLE_TIMEOUT_DEFAULT_MS = 30 * 60 * 1000; // 30 minutes default idle auto-shutdown
const PORT_RETRY_MAX = 10;

/**
 * Starts the local dashboard server.
 * @param {object} [opts]
 * @param {number} [opts.port=8787] - Preferred port (0 = random).
 * @param {string} [opts.currency='usd'] - Currency code.
 * @param {string} [opts.lang] - UI language code.
 * @param {boolean} [opts.isFree=false] - Free quota mode.
 * @param {string} [opts.model] - Active model display id.
 * @param {string} [opts.modelName] - Model name used for pricing lookups.
 * @param {number} [opts.refreshSec] - HTML polling interval (embedded template).
 * @param {number} [opts.intervalMs=5000] - SSE push interval (test hook).
 * @param {number} [opts.idleTimeoutMs=1800000] - Inactivity self-termination timeout in ms (test hook).
 * @param {string} [opts.cacheFile] - Cache file path override (test hook).
 * @param {string} [opts.brainDir] - Brain session directory override (test hook).
 * @param {string} [opts.quotaCacheFile] - Quota cache file override (test hook).
 * @param {object} [opts.geminiQuota] - Explicit Gemini quota object override (test hook).
 * @param {string} [opts.srcDir] - Source directory path override (test hook).
 * @param {Function} [opts.onSelfTerminate] - Callback on self-termination (test hook).
 * @returns {Promise<{ server: http.Server, port: number, url: string, broadcastSSE: Function, aggregate: Function, notifyChange: Function }|null>}
 */
function startDashboardServer(opts = {}) {
  const preferredPort = Number.isInteger(opts.port) ? opts.port : DASHBOARD_DEFAULT_PORT;
  const intervalMs = Number(opts.intervalMs) > 0 ? Number(opts.intervalMs) : SSE_INTERVAL_MS;
  const idleTimeoutMs = Number.isInteger(opts.idleTimeoutMs) && opts.idleTimeoutMs >= 0
    ? opts.idleTimeoutMs
    : IDLE_TIMEOUT_DEFAULT_MS;
  const targetCacheFile = typeof opts.cacheFile === 'string' ? opts.cacheFile : CACHE_FILE;
  const targetBrainDir = typeof opts.brainDir === 'string' ? opts.brainDir : BRAIN_DIR;
  const targetQuotaCacheFile = typeof opts.quotaCacheFile === 'string' ? opts.quotaCacheFile : GEMINI_QUOTA_CACHE_FILE;
  const targetSrcDir = typeof opts.srcDir === 'string' ? opts.srcDir : __dirname;
  const onSelfTerminate = typeof opts.onSelfTerminate === 'function' ? opts.onSelfTerminate : null;

  // REQ-103: refuse to start when the on-disk cache was written by NEWER code.
  const diskCacheVersion = staleness.readCacheVersionHeader(targetCacheFile);
  if (diskCacheVersion !== null && diskCacheVersion > CACHE_SCHEMA_VERSION) {
    const reason = `refusing to start: on-disk cache schema v${diskCacheVersion} is newer than this build's v${CACHE_SCHEMA_VERSION}. Update agy-tools.`;
    console.log(`[agy-dashboard] ${reason}`);
    if (onSelfTerminate) {
      onSelfTerminate(reason);
      return Promise.resolve(null);
    }
    // Exit 0: this is a deliberate guard, not a crash; detached spawns must not
    // surface a failure to the hook (which treats non-zero as spawn failure).
    process.exit(0);
  }

  let terminated = false;

  /**
   * Gracefully terminates the dashboard server on detected staleness (REQ-102).
   * Idempotent: concurrent triggers (SSE push + watchdog + signal) run once.
   * @param {http.Server} server
   * @param {number|null} boundPort
   * @param {string} reason - One-line human reason for the console.
   */
  function selfTerminate(server, boundPort, reason) {
    if (terminated) return;
    terminated = true;
    try { console.log(`[agy-dashboard] ${reason}`); } catch (_e) {}
    if (boundPort) {
      try { removePortFileIfPort(boundPort); } catch (_e) {}
    }
    if (onSelfTerminate) {
      stopDashboardServer(server)
        .then(() => { onSelfTerminate(reason); })
        .catch(() => { onSelfTerminate(reason); });
      return;
    }
    // Hard-exit fallback: if graceful close stalls >1s (lingering SSE socket on
    // Node 16 without closeAllConnections), force exit (REQ-102).
    const hardExit = setTimeout(() => process.exit(0), 1000);
    if (typeof hardExit.unref === 'function') hardExit.unref();
    stopDashboardServer(server)
      .then(() => { clearTimeout(hardExit); process.exit(0); })
      .catch(() => process.exit(0));
  }

  const payloadOpts = {
    currency: opts.currency || 'usd',
    lang: opts.lang || 'en',
    isFree: Boolean(opts.isFree),
    model: opts.model || '',
    modelName: opts.modelName || null
  };

  let latestPayload = null;

  /**
   * Re-aggregates the payload from a fresh incremental sync pass.
   * @returns {Promise<object>} DashboardPayload.
   */
  async function aggregate() {
    const syncResult = await syncSessions({
      modelName: payloadOpts.modelName,
      readOnly: true,
      brainDir: targetBrainDir,
      cachePath: targetCacheFile
    });
    const geminiQuota = opts.geminiQuota !== undefined
      ? opts.geminiQuota
      : geminiQuotaModule.getCachedGeminiQuota(targetQuotaCacheFile);
    return buildDashboardPayload(syncResult.sessions, {
      ...payloadOpts,
      parsedCount: syncResult.parsedCount,
      cachedCount: syncResult.cachedCount,
      elapsedMs: syncResult.elapsedMs,
      geminiQuota
    });
  }

  /**
   * Attempts to listen on the given port, auto-incrementing on EADDRINUSE.
   * @param {number} port - Port to try.
   * @param {number} attempt - Current attempt index.
   * @returns {Promise<{ server: http.Server, port: number, url: string, broadcastSSE: Function, aggregate: Function, notifyChange: Function }>}
   */
  function tryListen(port, attempt) {
    return new Promise((resolve, reject) => {
      let boundPortRef = null;
      let lastActivityAt = Date.now();
      const sseClients = new Set();
      const watchers = [];
      const pendingReattachTimers = new Map();
      let debounceTimer = null;
      let periodicTimer = null;
      let watchdog = null;
      let isWritingDashboard = false;

      const quotaDir = path.dirname(targetQuotaCacheFile);
      const tokenCacheDir = path.dirname(targetCacheFile);
      const brainParent = path.dirname(targetBrainDir);
      const historyFile = path.join(brainParent, 'history.jsonl');

      let lastQuotaMtime = fs.existsSync(targetQuotaCacheFile) ? fs.statSync(targetQuotaCacheFile).mtimeMs : 0;
      let lastCacheMtime = fs.existsSync(targetCacheFile) ? fs.statSync(targetCacheFile).mtimeMs : 0;
      let lastHistoryMtime = fs.existsSync(historyFile) ? fs.statSync(historyFile).mtimeMs : 0;

      const CORS_HEADERS = {
        'Access-Control-Allow-Origin': '*',
        'Access-Control-Allow-Methods': 'GET, OPTIONS',
        'Access-Control-Allow-Headers': '*',
        'Access-Control-Allow-Private-Network': 'true'
      };

      /**
       * Broadcasts payload to all connected SSE clients.
       * Sends both standard data event (for onmessage) and named 'update' event.
       * @param {object} payload
       */
      function broadcastSSE(payload) {
        if (!payload || typeof payload !== 'object' || terminated) return;
        const isFull = typeof payload.version === 'number';
        if (isFull) latestPayload = payload;
        const dataStr = JSON.stringify(payload);
        const messageChunk = `data: ${dataStr}\n\n`;
        const updatePayload = isFull
          ? { type: 'update', timestamp: Date.now(), ...payload }
          : { type: 'update', timestamp: Date.now(), ...payload };
        const updateChunk = `event: update\ndata: ${JSON.stringify(updatePayload)}\n\n`;

        for (const client of sseClients) {
          try {
            client.write(messageChunk);
            client.write(updateChunk);
          } catch (_err) {
            try { sseClients.delete(client); } catch (_e) {}
          }
        }
      }

      /**
       * Debounced change notifier triggered by file watchers.
       * @param {string} source
       */
      function notifyChange(source) {
        if (terminated) return;
        lastActivityAt = Date.now();
        if (debounceTimer) clearTimeout(debounceTimer);
        debounceTimer = setTimeout(async () => {
          debounceTimer = null;
          if (terminated) return;
          const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
          if (hit.stale) {
            selfTerminate(server, boundPortRef,
              `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
            return;
          }
          try {
            // Check for late-created brain or quota directories
            if (fs.existsSync(targetBrainDir) && !watchers.some(w => w._agyTarget === targetBrainDir)) {
              attachWatcher(targetBrainDir, true, 'brain');
            }
            if (quotaDir && fs.existsSync(quotaDir) && !watchers.some(w => w._agyTarget === quotaDir)) {
              attachWatcher(quotaDir, false, 'gemini-dir');
            }

            const payload = await aggregate();
            latestPayload = payload;
            if (!isWritingDashboard) {
              try {
                isWritingDashboard = true;
                writeDashboardFiles(payload, {
                  force: false,
                  refreshSec: opts.refreshSec,
                  servePort: boundPortRef || port
                });
              } catch (_wErr) {} finally {
                isWritingDashboard = false;
              }
            }
            broadcastSSE(payload);
          } catch (_err) {}
        }, 150);
      }

      /**
       * Safely attaches a filesystem watcher, tolerating non-existent paths and OS quirks.
       * @param {string} targetPath
       * @param {boolean} recursive
       * @param {string} label
       * @returns {fs.FSWatcher|null}
       */
      function attachWatcher(targetPath, recursive, label) {
        if (!targetPath || !fs.existsSync(targetPath)) return null;
        // Never attach duplicate watchers for the same targetPath
        const existing = watchers.find(w => w._agyTarget === targetPath);
        if (existing) return existing;

        let isDir = false;
        try {
          isDir = fs.statSync(targetPath).isDirectory();
        } catch (_e) {
          return null;
        }

        const quotaBase = path.basename(targetQuotaCacheFile);
        const cacheBase = path.basename(targetCacheFile);

        const handleEvent = (eventType, filename) => {
          const fname = filename ? String(filename) : '';
          if (fname) {
            const baseName = path.basename(fname);
            if (baseName.startsWith('.') || baseName.endsWith('.tmp') || baseName.includes('dashboard')) {
              return;
            }
            if (label === 'gemini-dir' &&
                !baseName.includes('gemini_quota_cache') &&
                !baseName.includes('token_tracker_cache') &&
                baseName !== quotaBase &&
                baseName !== cacheBase) {
              return;
            }
            if (label === 'antigravity-dir' && !baseName.includes('history.jsonl')) {
              return;
            }
          } else if (isDir) {
            // filename is null on Windows or Linux directory watches: check mtime to avoid spurious triggers
            let changed = false;
            if (label === 'gemini-dir') {
              try {
                const qM = fs.existsSync(targetQuotaCacheFile) ? fs.statSync(targetQuotaCacheFile).mtimeMs : 0;
                const cM = fs.existsSync(targetCacheFile) ? fs.statSync(targetCacheFile).mtimeMs : 0;
                if (qM !== lastQuotaMtime || cM !== lastCacheMtime) {
                  lastQuotaMtime = qM;
                  lastCacheMtime = cM;
                  changed = true;
                }
              } catch (_e) {}
            } else if (label === 'antigravity-dir') {
              try {
                const hM = fs.existsSync(historyFile) ? fs.statSync(historyFile).mtimeMs : 0;
                if (hM !== lastHistoryMtime) {
                  lastHistoryMtime = hM;
                  changed = true;
                }
              } catch (_e) {}
            } else if (label === 'brain') {
              changed = true;
            }
            if (!changed) return;
          }

          notifyChange(`${label}:${filename || eventType}`);

          // Re-attach ONLY for single files (never directories!) undergoing atomic replacement (rename)
          if (!isDir && eventType === 'rename' && fs.existsSync(targetPath)) {
            if (pendingReattachTimers.has(targetPath)) {
              clearTimeout(pendingReattachTimers.get(targetPath));
            }
            const timer = setTimeout(() => {
              pendingReattachTimers.delete(targetPath);
              if (terminated) return;
              try {
                const idx = watchers.findIndex(w => w._agyTarget === targetPath);
                if (idx !== -1) {
                  const oldW = watchers.splice(idx, 1)[0];
                  try { oldW.close(); } catch (_e) {}
                }
                attachWatcher(targetPath, false, label);
              } catch (_reErr) {}
            }, 100);
            pendingReattachTimers.set(targetPath, timer);
          }
        };

        try {
          const w = fs.watch(targetPath, { recursive }, handleEvent);
          w._agyTarget = targetPath;
          w.on('error', () => {
            const idx = watchers.indexOf(w);
            if (idx !== -1) watchers.splice(idx, 1);
          });
          w.on('close', () => {
            const idx = watchers.indexOf(w);
            if (idx !== -1) watchers.splice(idx, 1);
          });
          watchers.push(w);
          return w;
        } catch (_err) {
          if (recursive) {
            try {
              const w2 = fs.watch(targetPath, { recursive: false }, handleEvent);
              w2._agyTarget = targetPath;
              w2.on('error', () => {
                const idx = watchers.indexOf(w2);
                if (idx !== -1) watchers.splice(idx, 1);
              });
              w2.on('close', () => {
                const idx = watchers.indexOf(w2);
                if (idx !== -1) watchers.splice(idx, 1);
              });
              watchers.push(w2);
              return w2;
            } catch (_e2) {}
          }
        }
        return null;
      }

      // 1. Watch session transcripts directory (recursive where supported)
      attachWatcher(targetBrainDir, true, 'brain');

      // 2. Watch brain parent directory (ANTIGRAVITY_DIR for history.jsonl updates)
      if (brainParent && brainParent !== targetBrainDir && brainParent !== quotaDir) {
        attachWatcher(brainParent, false, 'antigravity-dir');
      }

      // 3. Watch quota directory (GEMINI_DIR for gemini_quota_cache.json & token_tracker_cache.json)
      if (quotaDir) {
        attachWatcher(quotaDir, false, 'gemini-dir');
      }

      // 4. Watch token cache directory if distinct from quotaDir and brainParent
      if (tokenCacheDir && tokenCacheDir !== quotaDir && tokenCacheDir !== brainParent) {
        attachWatcher(tokenCacheDir, false, 'gemini-dir');
      }

      // 5. Watch individual cache files directly if present (and not in watched quotaDir)
      if (fs.existsSync(targetQuotaCacheFile) && path.dirname(targetQuotaCacheFile) !== quotaDir) {
        attachWatcher(targetQuotaCacheFile, false, 'quota-cache');
      }
      if (fs.existsSync(targetCacheFile) && path.dirname(targetCacheFile) !== quotaDir && path.dirname(targetCacheFile) !== tokenCacheDir) {
        attachWatcher(targetCacheFile, false, 'token-cache');
      }

      const server = http.createServer(async (req, res) => {
        lastActivityAt = Date.now();
        // CORS for file:// pages (origin null) — E10; localhost-only server (C6)
        for (const [k, v] of Object.entries(CORS_HEADERS)) {
          res.setHeader(k, v);
        }

        if (req.method === 'OPTIONS') {
          res.writeHead(204, {
            ...CORS_HEADERS,
            'Cache-Control': 'no-store'
          });
          res.end();
          return;
        }

        const urlPath = (req.url || '/').split('?')[0];

        if (urlPath === '/' || urlPath === '/index.html') {
          try {
            const payload = await aggregate();
            latestPayload = payload;
            const html = renderDashboardHtml(payload, {
              refreshSec: opts.refreshSec,
              servePort: boundPortRef || port
            });
            try {
              writeDashboardFiles(payload, {
                force: true,
                refreshSec: opts.refreshSec,
                servePort: boundPortRef || port
              });
            } catch (_wErr) {}
            res.writeHead(200, {
              ...CORS_HEADERS,
              'Content-Type': 'text/html; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(html);
          } catch (_err) {
            try {
              if (fs.existsSync(DASHBOARD_HTML_FILE)) {
                const fallbackHtml = fs.readFileSync(DASHBOARD_HTML_FILE, 'utf8');
                res.writeHead(200, {
                  ...CORS_HEADERS,
                  'Content-Type': 'text/html; charset=utf-8',
                  'Cache-Control': 'no-store'
                });
                res.end(fallbackHtml);
                return;
              }
            } catch (_fErr) {}
            res.writeHead(404, { ...CORS_HEADERS, 'Content-Type': 'text/plain; charset=utf-8' });
            res.end('dashboard.html not found. Run: agy-tokens --html');
          }
          return;
        }

        if (urlPath === '/events') {
          sseClients.add(res);
          lastActivityAt = Date.now();
          res.writeHead(200, {
            ...CORS_HEADERS,
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-store',
            'Connection': 'keep-alive'
          });
          res.write(': connected\n\n');

          // Staleness check on connect
          const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
          if (hit.stale) {
            selfTerminate(server, boundPortRef,
              `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
            return;
          }

          // Push immediate initial payload to newly connected client
          if (latestPayload) {
            res.write(`data: ${JSON.stringify(latestPayload)}\n\n`);
            res.write(`event: update\ndata: ${JSON.stringify({ type: 'update', timestamp: Date.now(), ...latestPayload })}\n\n`);
          } else {
            aggregate().then((p) => {
              latestPayload = p;
              if (sseClients.has(res) && !terminated) {
                res.write(`data: ${JSON.stringify(p)}\n\n`);
                res.write(`event: update\ndata: ${JSON.stringify({ type: 'update', timestamp: Date.now(), ...p })}\n\n`);
              }
            }).catch(() => {});
          }

          req.on('close', () => {
            sseClients.delete(res);
            lastActivityAt = Date.now();
          });
          return;
        }

        if (urlPath === '/data.json') {
          try {
            const payload = await aggregate();
            latestPayload = payload;
            res.writeHead(200, {
              ...CORS_HEADERS,
              'Content-Type': 'application/json; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(JSON.stringify(payload));
          } catch (_err) {
            try {
              if (fs.existsSync(DASHBOARD_DATA_JSON)) {
                const json = fs.readFileSync(DASHBOARD_DATA_JSON, 'utf8');
                res.writeHead(200, {
                  ...CORS_HEADERS,
                  'Content-Type': 'application/json; charset=utf-8',
                  'Cache-Control': 'no-store'
                });
                res.end(json);
                return;
              }
            } catch (_fErr) {}
            res.writeHead(500, { ...CORS_HEADERS, 'Content-Type': 'application/json; charset=utf-8' });
            res.end('{}');
          }
          return;
        }

        if (urlPath === '/dashboard-data.js') {
          try {
            const payload = await aggregate();
            latestPayload = payload;
            res.writeHead(200, {
              ...CORS_HEADERS,
              'Content-Type': 'text/javascript; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(`window.__AGY_DASH__ = ${JSON.stringify(payload)};\n`);
          } catch (_err) {
            try {
              if (fs.existsSync(DASHBOARD_DATA_JS)) {
                const dataJs = fs.readFileSync(DASHBOARD_DATA_JS, 'utf8');
                res.writeHead(200, {
                  ...CORS_HEADERS,
                  'Content-Type': 'text/javascript; charset=utf-8',
                  'Cache-Control': 'no-store'
                });
                res.end(dataJs);
                return;
              }
            } catch (_fErr) {}
            res.writeHead(500, { ...CORS_HEADERS, 'Content-Type': 'text/javascript; charset=utf-8' });
            res.end('window.__AGY_DASH__ = {};\n');
          }
          return;
        }

        res.writeHead(404, { ...CORS_HEADERS, 'Content-Type': 'text/plain; charset=utf-8' });
        res.end('Not Found');
      });

      server.once('error', (err) => {
        if (err && err.code === 'EADDRINUSE' && attempt < PORT_RETRY_MAX && port !== 0) {
          server.close();
          resolve(tryListen(port + 1, attempt + 1));
        } else {
          reject(err);
        }
      });

      server.listen(port, '127.0.0.1', () => {
        const boundPort = server.address().port;
        boundPortRef = boundPort;

        // Periodic broadcast / refresh timer
        periodicTimer = setInterval(async () => {
          if (terminated) {
            clearInterval(periodicTimer);
            return;
          }
          if (sseClients.size > 0) {
            lastActivityAt = Date.now();
            const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
            if (hit.stale) {
              clearInterval(periodicTimer);
              selfTerminate(server, boundPortRef,
                `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
              return;
            }
            try {
              const cachedQ = geminiQuotaModule.getCachedGeminiQuota(targetQuotaCacheFile);
              if (!cachedQ || !cachedQ.isFresh) {
                geminiQuotaModule.triggerBackgroundQuotaRefresh();
              }
              const payload = await aggregate();
              latestPayload = payload;
              broadcastSSE(payload);
            } catch (_err) {
              for (const client of sseClients) {
                try { client.write(': keepalive\n\n'); } catch (_e) {}
              }
            }
          }
        }, intervalMs);
        if (typeof periodicTimer.unref === 'function') periodicTimer.unref();

        const watchdogInterval = (idleTimeoutMs > 0 && idleTimeoutMs < STALENESS_WATCHDOG_MS)
          ? idleTimeoutMs
          : STALENESS_WATCHDOG_MS;

        // REQ-101/105: independent watchdog — catches clientless stale servers and idle timeouts.
        watchdog = setInterval(() => {
          if (terminated) {
            clearInterval(watchdog);
            return;
          }
          const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
          if (hit.stale) {
            clearInterval(watchdog);
            selfTerminate(server, boundPort,
              `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
            return;
          }
          if (idleTimeoutMs > 0 && sseClients.size === 0 && (Date.now() - lastActivityAt) >= idleTimeoutMs) {
            clearInterval(watchdog);
            selfTerminate(server, boundPort,
              `self-terminating: idle timeout (${Math.round(idleTimeoutMs / 60000)}m with no active connections) — auto-shutdown`);
          }
        }, watchdogInterval);
        if (typeof watchdog.unref === 'function') watchdog.unref(); // never keep process alive

        server._agyDashboardState = {
          sseClients,
          watchers,
          broadcastSSE,
          aggregate,
          notifyChange,
          cleanup: () => {
            terminated = true;
            if (periodicTimer) clearInterval(periodicTimer);
            if (debounceTimer) clearTimeout(debounceTimer);
            if (watchdog) clearInterval(watchdog);
            for (const t of pendingReattachTimers.values()) clearTimeout(t);
            pendingReattachTimers.clear();
            for (const w of watchers) {
              try { w.close(); } catch (_e) {}
            }
            watchers.length = 0;
            for (const client of sseClients) {
              try {
                client.end();
                if (typeof client.destroy === 'function') client.destroy();
                if (client.socket && typeof client.socket.destroy === 'function') client.socket.destroy();
              } catch (_e) {}
            }
            sseClients.clear();
          }
        };

        server.once('close', () => {
          if (server._agyDashboardState) {
            server._agyDashboardState.cleanup();
          }
        });

        resolve({
          server,
          port: boundPort,
          url: `http://127.0.0.1:${boundPort}/`,
          broadcastSSE,
          aggregate,
          notifyChange
        });
      });
    });
  }

  return tryListen(preferredPort, 0);
}

/**
 * Gracefully closes the dashboard server (test hook / Ctrl+C).
 * @param {http.Server} server
 * @returns {Promise<void>}
 */
function stopDashboardServer(server) {
  return new Promise((resolve) => {
    if (!server) {
      resolve();
      return;
    }
    let resolved = false;
    const finish = () => {
      if (!resolved) {
        resolved = true;
        resolve();
      }
    };
    const timer = setTimeout(() => {
      if (typeof server.closeAllConnections === 'function') {
        try { server.closeAllConnections(); } catch (_e) {}
      }
      finish();
    }, 1000);
    if (typeof timer.unref === 'function') timer.unref();

    if (server._agyDashboardState && typeof server._agyDashboardState.cleanup === 'function') {
      try { server._agyDashboardState.cleanup(); } catch (_e) {}
    }
    if (typeof server.closeAllConnections === 'function') {
      try { server.closeAllConnections(); } catch (_e) {}
    }
    try {
      server.close(() => {
        clearTimeout(timer);
        finish();
      });
    } catch (_e) {
      clearTimeout(timer);
      finish();
    }
  });
}

module.exports = {
  SSE_INTERVAL_MS,
  STALENESS_WATCHDOG_MS,
  IDLE_TIMEOUT_DEFAULT_MS,
  PORT_RETRY_MAX,
  startDashboardServer,
  stopDashboardServer,
  broadcastSSE: (server, payload) => {
    if (server && server._agyDashboardState && typeof server._agyDashboardState.broadcastSSE === 'function') {
      server._agyDashboardState.broadcastSSE(payload);
    }
  }
};