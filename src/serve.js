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
const {
  DASHBOARD_HTML_FILE,
  DASHBOARD_DATA_JSON,
  DASHBOARD_DATA_JS,
  DASHBOARD_DEFAULT_PORT,
  CACHE_FILE
} = require('./config');
const { syncSessions, CACHE_SCHEMA_VERSION } = require('./cache-manager');
const { buildDashboardPayload } = require('./html-report');
// Acyclic dependency: dashboard-link requires html-report and config only (never serve.js)
const { removePortFileIfPort } = require('./dashboard-link');
const staleness = require('./serve-staleness');

const SSE_INTERVAL_MS = 5000;
const STALENESS_WATCHDOG_MS = 30000; // REQ-101/105: <=60s; 30s chosen for responsive self-termination
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
 * @param {string} [opts.cacheFile] - Cache file path override (test hook).
 * @param {string} [opts.srcDir] - Source directory path override (test hook).
 * @param {Function} [opts.onSelfTerminate] - Callback on self-termination (test hook).
 * @returns {Promise<{ server: http.Server, port: number, url: string }|null>}
 */
function startDashboardServer(opts = {}) {
  const preferredPort = Number.isInteger(opts.port) ? opts.port : DASHBOARD_DEFAULT_PORT;
  const intervalMs = Number(opts.intervalMs) > 0 ? Number(opts.intervalMs) : SSE_INTERVAL_MS;
  const targetCacheFile = typeof opts.cacheFile === 'string' ? opts.cacheFile : CACHE_FILE;
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

  /**
   * Re-aggregates the payload from a fresh incremental sync pass.
   * @returns {Promise<object>} DashboardPayload.
   */
  async function aggregate() {
    const syncResult = await syncSessions({ modelName: payloadOpts.modelName, readOnly: true });
    return buildDashboardPayload(syncResult.sessions, {
      ...payloadOpts,
      parsedCount: syncResult.parsedCount,
      cachedCount: syncResult.cachedCount,
      elapsedMs: syncResult.elapsedMs
    });
  }

  /**
   * Attempts to listen on the given port, auto-incrementing on EADDRINUSE.
   * @param {number} port - Port to try.
   * @param {number} attempt - Current attempt index.
   * @returns {Promise<{ server: http.Server, port: number, url: string }>}
   */
  function tryListen(port, attempt) {
    return new Promise((resolve, reject) => {
      let boundPortRef = null;

      const server = http.createServer((req, res) => {
        // CORS for file:// pages (origin null) — E10; localhost-only server (C6)
        res.setHeader('Access-Control-Allow-Origin', '*');

        const urlPath = (req.url || '/').split('?')[0];

        if (urlPath === '/' || urlPath === '/index.html') {
          try {
            const html = fs.readFileSync(DASHBOARD_HTML_FILE, 'utf8');
            res.writeHead(200, {
              'Content-Type': 'text/html; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(html);
          } catch (_err) {
            res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
            res.end('dashboard.html not found. Run: agy-tokens --html');
          }
          return;
        }

        if (urlPath === '/events') {
          res.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-store',
            'Connection': 'keep-alive'
          });
          res.write(': connected\n\n');

          let closed = false;
          let inFlight = false;
          const push = async () => {
            if (closed || inFlight || terminated) return;
            // REQ-101: staleness check per push — a stale server must die before it
            // can push one more merged/old-schema payload to the dashboard.
            const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
            if (hit.stale) {
              selfTerminate(server, boundPortRef,
                `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
              return;
            }
            inFlight = true;
            try {
              const payload = await aggregate();
              if (!closed && !terminated) {
                res.write(`data: ${JSON.stringify(payload)}\n\n`);
              }
            } catch (_err) {
              // Keep the stream alive on aggregation errors
            } finally {
              inFlight = false;
            }
          };

          push();
          const timer = setInterval(push, intervalMs);
          req.on('close', () => {
            closed = true;
            clearInterval(timer);
          });
          return;
        }

        if (urlPath === '/data.json') {
          try {
            const json = fs.readFileSync(DASHBOARD_DATA_JSON, 'utf8');
            res.writeHead(200, {
              'Content-Type': 'application/json; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(json);
          } catch (_err) {
            res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
            res.end('{}');
          }
          return;
        }

        if (urlPath === '/dashboard-data.js') {
          try {
            const dataJs = fs.readFileSync(DASHBOARD_DATA_JS, 'utf8');
            res.writeHead(200, {
              'Content-Type': 'text/javascript; charset=utf-8',
              'Cache-Control': 'no-store'
            });
            res.end(dataJs);
          } catch (_err) {
            res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
            res.end('{}');
          }
          return;
        }

        res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
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

        // REQ-101/105: independent watchdog — catches clientless stale servers.
        const watchdog = setInterval(() => {
          if (terminated) {
            clearInterval(watchdog);
            return;
          }
          const hit = staleness.sourceCodeChangedSinceStart(targetSrcDir, staleness.getProcessStartTimeMs());
          if (hit.stale) {
            clearInterval(watchdog);
            selfTerminate(server, boundPort,
              `self-terminating: source file changed on disk (${hit.file}) — restart for updated code`);
          }
        }, STALENESS_WATCHDOG_MS);
        if (typeof watchdog.unref === 'function') watchdog.unref(); // never keep process alive
        server.once('close', () => clearInterval(watchdog));

        resolve({
          server,
          port: boundPort,
          url: `http://127.0.0.1:${boundPort}/`
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
    if (typeof server.closeAllConnections === 'function') {
      try { server.closeAllConnections(); } catch (_e) {}
    }
    server.close(() => resolve());
  });
}

module.exports = {
  SSE_INTERVAL_MS,
  STALENESS_WATCHDOG_MS,
  PORT_RETRY_MAX,
  startDashboardServer,
  stopDashboardServer
};