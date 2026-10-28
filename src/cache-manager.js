/**
 * @fileoverview Local JSON cache manager for sub-10ms queries on large transcript archives.
 * Tracks file modification times (mtime) and sizes for incremental zero-latency updates.
 */

const fs = require('fs');
const path = require('path');
const { CACHE_FILE, BRAIN_DIR, HISTORY_FILE, getActiveModelFromSettings } = require('./config');
const { discoverSessions, parseTranscriptFile, loadHistoryIndex } = require('./log-parser');

// v4 (Batch 1, REQ-304): turn-level model attribution stamps each turn with
// turn.modelName and records session.models; bumping invalidates schema-3
// caches so all sessions are re-parsed once with turn-level metadata.
const CACHE_SCHEMA_VERSION = 4;

let lastValidCache = null;

/**
 * Loads the current tracker cache from disk.
 * @param {string} [customCachePath] - Optional custom path for cache file.
 * @returns {object} Cache root object.
 */
function loadCache(customCachePath = CACHE_FILE) {
  if (!fs.existsSync(customCachePath)) {
    return lastValidCache || {
      version: CACHE_SCHEMA_VERSION,
      lastUpdated: null,
      sessions: {}
    };
  }

  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      const raw = fs.readFileSync(customCachePath, 'utf8');
      const data = JSON.parse(raw);
      if (data && data.version === CACHE_SCHEMA_VERSION && typeof data.sessions === 'object') {
        lastValidCache = JSON.parse(JSON.stringify(data));
        return data;
      }
    } catch (_err) {
      // Sleep for 20ms using Atomics or fallback to spinlock
      try {
        Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 20);
      } catch (e) {
        const start = Date.now();
        while (Date.now() - start < 20) {}
      }
    }
  }

  return lastValidCache || {
    version: CACHE_SCHEMA_VERSION,
    lastUpdated: null,
    sessions: {}
  };
}

/**
 * Atomically saves the cache object to disk using a temporary file.
 * @param {object} cacheData - Cache data to write.
 * @param {string} [customCachePath] - Optional custom path for cache file.
 */
function saveCache(cacheData, customCachePath = CACHE_FILE) {
  try {
    const dir = path.dirname(customCachePath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }

    cacheData.lastUpdated = new Date().toISOString();
    const content = JSON.stringify(cacheData, null, 2);
    const tempFile = `${customCachePath}.${Date.now()}.${process.pid}.tmp`;
    
    fs.writeFileSync(tempFile, content, 'utf8');
    
    let renamed = false;
    for (let attempt = 0; attempt < 10; attempt++) {
      try {
        fs.renameSync(tempFile, customCachePath);
        renamed = true;
        break;
      } catch (_renameErr) {
        try {
          Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 25);
        } catch (e) {
          const start = Date.now();
          while (Date.now() - start < 25) {}
        }
      }
    }

    if (!renamed) {
      try { fs.unlinkSync(tempFile); } catch (_e) {}
      fs.writeFileSync(customCachePath, content, 'utf8');
    }

    lastValidCache = JSON.parse(JSON.stringify(cacheData));
  } catch (_err) {
    // Graceful fallback if write fails
  }
}

/**
 * Clears the local token tracker cache file.
 * @param {string} [customCachePath] - Optional cache path.
 * @returns {boolean} True if removed, false otherwise.
 */
function clearCache(customCachePath = CACHE_FILE) {
  try {
    if (fs.existsSync(customCachePath)) {
      fs.unlinkSync(customCachePath);
      return true;
    }
  } catch (_err) {}
  return false;
}

/**
 * Synchronizes all sessions in the brain directory with the local cache.
 * Only parses new or modified transcript files for lightning-fast incremental performance.
 *
 * @param {object} [options] - Synchronization options.
 * @param {boolean} [options.forceFresh] - Whether to bypass cache and re-parse all files.
 * @param {string} [options.brainDir] - Custom brain directory path.
 * @param {string} [options.historyPath] - Custom history file path.
 * @param {string} [options.cachePath] - Custom cache file path.
 * @param {string} [options.modelName] - Active model override.
 * @returns {Promise<{ sessions: Array<object>, parsedCount: number, cachedCount: number, elapsedMs: number }>}
 */
async function syncSessions(options = {}) {
  const startTime = Date.now();
  const forceFresh = Boolean(options.forceFresh);
  const readOnly = Boolean(options.readOnly);
  const brainDir = options.brainDir || BRAIN_DIR;
  const historyPath = options.historyPath || HISTORY_FILE;
  const cachePath = options.cachePath || CACHE_FILE;
  const modelName = options.modelName || getActiveModelFromSettings();

  const cache = forceFresh ? { version: CACHE_SCHEMA_VERSION, sessions: {} } : loadCache(cachePath);
  const historyMap = loadHistoryIndex(historyPath);
  const discovered = discoverSessions(brainDir);

  let parsedCount = 0;
  let cachedCount = 0;
  const updatedSessionsMap = {};

  for (const item of discovered) {
    const cachedEntry = cache.sessions[item.sessionId];

    if (
      !forceFresh &&
      cachedEntry &&
      cachedEntry.mtimeMs === item.mtimeMs &&
      cachedEntry.size === item.size
    ) {
      // Re-use cached session data
      updatedSessionsMap[item.sessionId] = cachedEntry;
      cachedCount++;
    } else {
      // Parse or re-parse modified session
      try {
        const metadata = historyMap.get(item.sessionId) || {};
        const oldTurns = cachedEntry ? cachedEntry.turns : null;
        const parsed = await parseTranscriptFile(
          item.transcriptPath,
          item.sessionId,
          metadata,
          modelName,
          oldTurns
        );

        parsed.mtimeMs = item.mtimeMs;
        parsed.size = item.size;
        updatedSessionsMap[item.sessionId] = parsed;
        parsedCount++;
      } catch (_err) {
        // Skip unparseable session
      }
    }
  }

  cache.sessions = updatedSessionsMap;
  if (!readOnly) {
    saveCache(cache, cachePath);
  }

  const sessionList = Object.values(updatedSessionsMap).sort(
    (a, b) => new Date(b.startTime).getTime() - new Date(a.startTime).getTime()
  );

  const elapsedMs = Date.now() - startTime;

  return {
    sessions: sessionList,
    parsedCount,
    cachedCount,
    elapsedMs
  };
}

module.exports = {
  CACHE_SCHEMA_VERSION,
  loadCache,
  saveCache,
  clearCache,
  syncSessions
};
