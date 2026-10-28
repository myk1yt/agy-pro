/**
 * @fileoverview Pure, injectable staleness detection and version header helpers
 * for the local dashboard server (R1).
 *
 * Provides:
 *   - getProcessStartTimeMs(): process start time captured at module load
 *   - sourceCodeChangedSinceStart(srcDir, startTimeMs): checks if any src/*.js mtime
 *     is newer than start time (fail-open on fs errors)
 *   - readCacheVersionHeader(cacheFilePath): reads the first 64 bytes of cache JSON
 *     to extract "version": N without parsing the entire multi-MB file
 *
 * Zero dependencies (Node core: fs, path only).
 */

const fs = require('fs');
const path = require('path');

/**
 * Safety margin in milliseconds for mtime comparisons.
 * Covers coarse/FAT32 timestamps or OneDrive sync timing skews.
 */
const MTIME_SAFETY_MARGIN_MS = 2000;

/**
 * Process start timestamp (ms epoch) captured once when this module is evaluated.
 */
const MODULE_LOAD_TIME_MS = Date.now();

/**
 * Returns the effective comparison start time (ms epoch), which is the module
 * load time minus the safety margin. The margin accounts for coarse/FAT32
 * timestamps and OneDrive sync timing skews (Windows/OneDrive): a .js file
 * written 0–2s before server spawn could have a coarse/future mtime and would
 * otherwise trigger a spurious self-termination.
 * Computed once at module load so it predates all subsequent module activity.
 * @returns {number}
 */
function getProcessStartTimeMs() {
  return MODULE_LOAD_TIME_MS - MTIME_SAFETY_MARGIN_MS;
}

/**
 * Scans srcDir for any .js file whose mtimeMs > startTimeMs.
 * Returns { stale: boolean, file: string|null, mtimeMs: number|null }.
 * On any fs error (EACCES, ENOENT, etc.), fails open and returns { stale: false, file: null, mtimeMs: null }.
 * Non-recursive by design (src/ has no subdirectories).
 *
 * @param {string} srcDir - Directory to inspect (e.g., __dirname or test temp dir).
 * @param {number} startTimeMs - Baseline start timestamp in ms.
 * @returns {{ stale: boolean, file: string|null, mtimeMs: number|null }}
 */
function sourceCodeChangedSinceStart(srcDir, startTimeMs) {
  try {
    if (!srcDir || typeof startTimeMs !== 'number') {
      return { stale: false, file: null, mtimeMs: null };
    }
    const entries = fs.readdirSync(srcDir);
    let maxMtime = -1;
    let maxFile = null;

    for (let i = 0; i < entries.length; i++) {
      const name = entries[i];
      if (!name.endsWith('.js')) continue;

      const fullPath = path.join(srcDir, name);
      const stat = fs.statSync(fullPath);
      if (stat.mtimeMs > startTimeMs) {
        if (stat.mtimeMs > maxMtime) {
          maxMtime = stat.mtimeMs;
          maxFile = name;
        }
      }
    }

    if (maxFile !== null) {
      return { stale: true, file: maxFile, mtimeMs: maxMtime };
    }
    return { stale: false, file: null, mtimeMs: null };
  } catch (_err) {
    // Fail-open: never crash on fs errors, transient scan errors self-heal next tick
    return { stale: false, file: null, mtimeMs: null };
  }
}

/**
 * Reads only the first 64 bytes of the cache file and extracts the top-level
 * "version": N without parsing the full (multi-MB) JSON document.
 * Null-tolerant on missing file (ENOENT), unreadable file, or corrupt header (REQ-107).
 *
 * @param {string} cacheFilePath - Path to cache JSON file.
 * @returns {number|null} Schema version, or null when unknown/unreadable.
 */
function readCacheVersionHeader(cacheFilePath) {
  if (!cacheFilePath || typeof cacheFilePath !== 'string') return null;
  let fd = null;
  try {
    fd = fs.openSync(cacheFilePath, 'r');
    const buf = Buffer.alloc(64);
    const bytesRead = fs.readSync(fd, buf, 0, 64, 0);
    fs.closeSync(fd);
    fd = null;

    if (bytesRead <= 0) return null;
    const head = buf.toString('utf8', 0, bytesRead);
    const match = head.match(/"version"\s*:\s*(\d+)/);
    if (match && match[1]) {
      return parseInt(match[1], 10);
    }
    return null;
  } catch (_err) {
    if (fd !== null) {
      try { fs.closeSync(fd); } catch (_e) {}
    }
    return null;
  }
}

module.exports = {
  MTIME_SAFETY_MARGIN_MS,
  getProcessStartTimeMs,
  sourceCodeChangedSinceStart,
  readCacheVersionHeader
};
