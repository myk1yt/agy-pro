/**
 * @fileoverview Dashboard link target resolution for the statusline badge.
 *
 * VS Code's integrated terminal routes `file://` OSC 8 hyperlinks to the
 * EDITOR by design (vscode#39278, vscode#176812 — only http/https schemes
 * open in the default browser; no setting changes this). Therefore, when
 * the hook runs inside a VS Code terminal (TERM_PROGRAM=vscode), the
 * 📊 Dashboard badge links to `http://127.0.0.1:<port>/` served by the
 * local SSE dashboard server (src/serve.js — binds 127.0.0.1 ONLY and
 * re-aggregates every 5s, so data is always fresh). Outside VS Code the
 * badge keeps the plain `file://` dashboard.html link.
 *
 * Server discovery flow (the "simple correct" variant of the VP fix
 * design — the hook never waits for the server to boot):
 *   1. Read the port file <dashboard dir>/dashboard-server.json. It holds
 *      either a server record { port, pid, startedAt } (written by the
 *      `--serve` process once bound) or a spawn intent
 *      { intent: 'spawn', requestedPort, at } (written by a previous hook
 *      render right before spawning — the cross-process stampede guard,
 *      because every statusline render is its own node process).
 *   2. Probe the recorded port with a 300ms-capped net.connect (~1ms on
 *      loopback). Up → link immediately.
 *   3. Fresh spawn intent (< 15s old) → another render already started the
 *      server → link to the expected URL without spawning again.
 *   4. Probe the default port (8787) too — covers a running server whose
 *      port file was deleted.
 *   5. Still down → write a fresh spawn intent, spawn a detached
 *      background server (`node <bin> --serve --port 8787`, fire-and-
 *      forget ~1ms) and link to the EXPECTED URL right away; the spawned
 *      server overwrites the port file with its authoritative bound port
 *      for subsequent renders. If the default port was taken, the server
 *      auto-increments and the NEXT render links to the corrected port.
 *   6. On any failure (entry missing, spawn throw) → return null; the
 *      caller falls back to the file:// link.
 *
 * Statusline budget: the steady-state path (--hook --raw) is one small
 * file read + one loopback TCP probe (~1-2ms total), well under the 20ms
 * requirement. The spawn path happens at most once per 15s grace window.
 *
 * Env override: AGY_TOKENS_LINK_MODE=file|http forces the link mode
 * regardless of terminal detection (http also works outside VS Code).
 *
 * All file writes are atomic (tmp + rename). Failures are silent by
 * design — link building must never break the statusline.
 *
 * Zero dependencies (Node core: net, child_process, fs, path).
 */

const net = require('net');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');
const {
  DASHBOARD_SERVER_PORT_FILE,
  DASHBOARD_DEFAULT_PORT
} = require('./config');
const { DASHBOARD_PAYLOAD_VERSION } = require('./html-report');
const { pathToFileURL } = require('url');
const { DASHBOARD_HTML_FILE } = require('./config');

/** net.connect probe cap (loopback connects resolve in ~1ms). */

/**
 * Detects whether the current environment can render OSC 8 hyperlinks.
 * Disabled when colors are disabled (NO_COLOR, TERM=dumb) or when stdout
 * is not a TTY (statusline pipes output through the renderer).
 * @returns {boolean}
 */
function isOsc8Supported() {
  if (process.env.NO_COLOR) return false;
  if (process.env.TERM === 'dumb') return false;
  return true;
}

/**
 * Formats an OSC 8 terminal hyperlink escape sequence pair around a label.
 * Format: ESC ] 8 ; ; <uri> ESC \ <label> ESC ] 8 ; ; ESC \
 * Terminals without OSC 8 support ignore the escapes and render the label
 * as plain text (graceful degradation, E6).
 * @param {string} uri - Absolute URI to open (e.g. file:///C:/...).
 * @param {string} label - Visible link text.
 * @returns {string} OSC 8 wrapped label, or plain label when unsupported.
 */
function formatOsc8Link(uri, label) {
  if (!uri || !label) return label || '';
  if (!isOsc8Supported()) return label;
  const ESC = '\x1b';
  const BEL = '\x07';
  return `${ESC}]8;;${uri}${BEL}${label}${ESC}]8;;${BEL}`;
}

/**
 * Resolves the file:// URL of the generated dashboard.html.
 * Uses url.pathToFileURL for correct percent-encoding of spaces/unicode (E7).
 * @returns {string} file:// URI string for dashboard.html.
 */
function dashboardFileUrl() {
  return pathToFileURL(DASHBOARD_HTML_FILE).href;
}

const PROBE_TIMEOUT_MS = 300;
/**
 * Cross-process spawn stampede guard: a spawn intent younger than this
 * suppresses further spawns (the statusline renders on every state change,
 * each in its own process — module state cannot guard across renders).
 */
const SPAWN_GRACE_MS = 15000;

/**
 * Detects a VS Code integrated terminal (the only environment where
 * file:// OSC 8 links open in the editor instead of the browser).
 * @returns {boolean}
 */
function isVsCodeTerminal() {
  return process.env.TERM_PROGRAM === 'vscode';
}

/**
 * Resolves the AGY_TOKENS_LINK_MODE env override, if valid.
 * @returns {'file'|'http'|null} Override mode, or null when unset/invalid.
 */
function getLinkModeOverride() {
  const value = (process.env.AGY_TOKENS_LINK_MODE || '').trim().toLowerCase();
  return value === 'file' || value === 'http' ? value : null;
}

/**
 * Resolves the dashboard link target for the statusline badge (sync).
 * mode 'http' → local dashboard server URL (expected default port; the
 * authoritative port comes from ensureServerRunning). mode 'file' → the
 * dashboard.html file:// URL (pre-VS-Code behavior).
 * @returns {{ mode: 'http'|'file', url: string }}
 */
function resolveLinkTarget() {
  const override = getLinkModeOverride();
  const mode = override || (isVsCodeTerminal() ? 'http' : 'file');
  if (mode === 'http') {
    return { mode, url: `http://127.0.0.1:${DASHBOARD_DEFAULT_PORT}/` };
  }
  return { mode, url: dashboardFileUrl() };
}

/**
 * Builds the http://127.0.0.1:<port>/ dashboard URL.
 * @param {number} port - Bound port.
 * @returns {string} Absolute local dashboard URL.
 */
function httpUrl(port) {
  return `http://127.0.0.1:${port}/`;
}

/**
 * Reads and minimally validates the dashboard server port file.
 * Tolerates stale pid/startedAt values — liveness is decided by probing,
 * not by the record contents.
 * @param {string} [portFile] - Port file path (default: config constant).
 * @returns {object|null} Parsed record, or null when absent/corrupt/invalid.
 */
function readPortFile(portFile = DASHBOARD_SERVER_PORT_FILE) {
  try {
    const raw = fs.readFileSync(portFile, 'utf8');
    const data = JSON.parse(raw);
    if (!data || typeof data !== 'object') return null;
    const validPort = Number.isInteger(data.port) && data.port > 0 && data.port <= 65535;
    const validIntent = data.intent === 'spawn';
    if (!validPort && !validIntent) return null;
    return data;
  } catch (_err) {
    return null; // missing / corrupt / unreadable → treat as absent
  }
}

/**
 * Atomic JSON file write: tmp file + rename (crash-safe on same volume).
 * @param {string} filePath - Destination path.
 * @param {object} data - Serializable payload.
 */
function atomicWriteJson(filePath, data) {
  const tmp = `${filePath}.${Date.now()}.${process.pid}.tmp`;
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(tmp, JSON.stringify(data, null, 2), 'utf8');
  fs.renameSync(tmp, filePath);
}

/**
 * Atomically writes the server port record { port, pid, startedAt }.
 * Called by the `--serve` process once the port is bound.
 * @param {number} port - Bound port.
 * @param {number} [pid] - Server process id (default: current process).
 * @param {string} [portFile] - Port file path (default: config constant).
 * @returns {object} The written record.
 */
function writePortFile(port, pid = process.pid, portFile = DASHBOARD_SERVER_PORT_FILE) {
  const record = {
    port,
    pid,
    startedAt: new Date().toISOString(),
    payloadVersion: DASHBOARD_PAYLOAD_VERSION
  };
  atomicWriteJson(portFile, record);
  return record;
}

/**
 * Atomically writes a spawn intent record (cross-process stampede guard).
 * @param {number} requestedPort - Port the spawned server will try first.
 * @param {string} [portFile] - Port file path (default: config constant).
 * @returns {object} The written record.
 */
function writeSpawnIntent(requestedPort, portFile = DASHBOARD_SERVER_PORT_FILE) {
  const record = { intent: 'spawn', requestedPort, at: Date.now() };
  atomicWriteJson(portFile, record);
  return record;
}

/**
 * Removes the port file (best effort — an absent file is not an error).
 * @param {string} [portFile] - Port file path (default: config constant).
 */
function removePortFile(portFile = DASHBOARD_SERVER_PORT_FILE) {
  try {
    fs.unlinkSync(portFile);
  } catch (_err) {
    // already gone / never created
  }
}

/**
 * Removes the port file ONLY when it currently points at the given port.
 * Used on graceful server shutdown so the hook never links to a dead port
 * while a different (still valid) server record must be preserved.
 * @param {number} port - Port of the server that stopped.
 * @param {string} [portFile] - Port file path (default: config constant).
 */
function removePortFileIfPort(port, portFile = DASHBOARD_SERVER_PORT_FILE) {
  const record = readPortFile(portFile);
  if (record && record.port === port) {
    removePortFile(portFile);
  }
}

/**
 * Probes 127.0.0.1:<port> with a TCP connect. Never throws — resolves
 * false on error/timeout (DASHBOARD-LINK/probePort/001 failure path).
 * @param {number} port - Port to probe.
 * @param {number} [timeoutMs=300] - Connect timeout cap.
 * @returns {Promise<boolean>} true when something accepts the connection.
 */
function probePort(port, timeoutMs = PROBE_TIMEOUT_MS) {
  return new Promise((resolve) => {
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      resolve(false);
      return;
    }
    const socket = new net.Socket();
    let settled = false;
    const finish = (ok) => {
      if (settled) return;
      settled = true;
      socket.destroy();
      resolve(ok);
    };
    socket.setTimeout(timeoutMs);
    socket.once('connect', () => finish(true));
    socket.once('timeout', () => finish(false));
    socket.once('error', () => finish(false));
    socket.connect(port, '127.0.0.1');
  });
}

/**
 * Resolves the CLI entry used to spawn the background server. The package
 * layout (bin/ + src/, both shipped via package.json "files") makes
 * ../bin/agy-tokens.js valid both in this repo and in the npm-global
 * install (src/ and bin/ are siblings in both).
 * @returns {string} Absolute path to the CLI entry.
 */
function getOwnEntryJs() {
  return path.join(__dirname, '..', 'bin', 'agy-tokens.js');
}

/**
 * Ensures the local dashboard server is reachable, starting it in the
 * background when needed (see module header for the full flow).
 * Never throws; returns null when the http link cannot be guaranteed and
 * the caller should fall back to the file:// link.
 * @param {object} [opts]
 * @param {number} [opts.port] - Preferred port (default: DASHBOARD_DEFAULT_PORT).
 * @param {string} [opts.portFile] - Port file path (default: config constant).
 * @param {string} [opts.entryJs] - CLI entry to spawn (default: ../bin/agy-tokens.js).
 * @returns {Promise<{ url: string, started: boolean }|null>} Link target
 *   info, or null when the http link cannot be ensured.
 */
async function ensureServerRunning(opts = {}) {
  const portFile = opts.portFile || DASHBOARD_SERVER_PORT_FILE;
  const preferredPort = Number.isInteger(opts.port) ? opts.port : DASHBOARD_DEFAULT_PORT;
  const entryJs = opts.entryJs || getOwnEntryJs();

  const record = readPortFile(portFile);
  const recordedPort = record && Number.isInteger(record.port) ? record.port : null;

  // REQ-240: a recorded server whose payloadVersion is older than the current
  // DASHBOARD_PAYLOAD_VERSION is stale (running pre-upgrade code that pushes
  // old-schema SSE payloads). Skip the probe-hit fast path so a fresh server
  // is spawned; it fails to bind the same port and auto-increments (serve.js
  // EADDRINUSE retry), then rewrites the port file with the new version.
  const recordedVersion = record && Number.isInteger(record.payloadVersion) ? record.payloadVersion : null;
  const serverStale = recordedPort !== null &&
    (recordedVersion === null || recordedVersion < DASHBOARD_PAYLOAD_VERSION);

  // 1. Previously started server: probe its recorded port (fresh only).
  if (recordedPort !== null && !serverStale && (await probePort(recordedPort))) {
    return { url: httpUrl(recordedPort), started: false };
  }

  // 2. Fresh spawn intent: another hook render already started the server.
  if (
    record &&
    record.intent === 'spawn' &&
    Number.isInteger(record.at) &&
    Date.now() - record.at < SPAWN_GRACE_MS
  ) {
    const intentPort = Number.isInteger(record.requestedPort) ? record.requestedPort : preferredPort;
    return { url: httpUrl(intentPort), started: false };
  }

  // 3. Running server without a port file (deleted/cleared): probe default.
  //    A stale recorded server also falls through here; skip the default-port
  //    fast path so the stale server is NOT linked and a fresh one spawns.
  if (recordedPort !== preferredPort && !serverStale && (await probePort(preferredPort))) {
    return { url: httpUrl(preferredPort), started: false };
  }

  // 4. Server down → spawn detached background server (fire-and-forget).
  if (!fs.existsSync(entryJs)) {
    return null; // DASHBOARD-LINK/ensureServerRunning/001 — no entry, fall back to file://
  }
  try {
    writeSpawnIntent(preferredPort, portFile);
  } catch (_err) {
    // Intent is best-effort; spawning still proceeds
  }
  try {
    const child = spawn(
      process.execPath,
      [entryJs, '--serve', '--port', String(preferredPort)],
      { detached: true, stdio: 'ignore', windowsHide: true }
    );
    child.unref();
  } catch (_err) {
    removePortFile(portFile); // allow immediate retry on the next render
    return null; // DASHBOARD-LINK/ensureServerRunning/002 — spawn failed
  }
  return { url: httpUrl(preferredPort), started: true };
}

module.exports = {
  isOsc8Supported,
  formatOsc8Link,
  dashboardFileUrl,
  PROBE_TIMEOUT_MS,
  SPAWN_GRACE_MS,
  isVsCodeTerminal,
  getLinkModeOverride,
  resolveLinkTarget,
  readPortFile,
  writePortFile,
  writeSpawnIntent,
  removePortFile,
  removePortFileIfPort,
  probePort,
  ensureServerRunning
};