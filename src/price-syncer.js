/**
 * @fileoverview Remote API Pricing Synchronization Engine for Antigravity CLI (/model).
 * Downloads and validates official model pricing catalog over HTTPS with zero dependencies,
 * manages local caching in ~/.gemini/antigravity_pricing.json, and renders multi-language
 * ANSI pricing tables.
 */

const https = require('https');
const http = require('http');
const fs = require('fs');
const path = require('path');
const os = require('os');
const crypto = require('crypto');
const { URL } = require('url');

const config = require('./config');
const i18n = require('./i18n');
const formatter = require('./formatter');

/**
 * Default remote pricing repository endpoint and system cache paths.
 */
const DEFAULT_REMOTE_URL = 'https://raw.githubusercontent.com/myk1yt/agy-pro/main/data/pricing.json';
/**
 * Host every hop of a catalog fetch is allowed to end at (see redirectRefusal):
 * the catalog's own publisher. A redirect that leaves it is not the catalog.
 */
const DEFAULT_REMOTE_HOST = 'raw.githubusercontent.com';
const SYNC_CACHE_FILE = path.join(os.homedir(), '.gemini', 'antigravity_pricing.json');
const BUNDLED_PRICING_FILE = path.join(__dirname, '..', 'data', 'pricing.json');
const MAX_CACHE_AGE_MS = 24 * 60 * 60 * 1000; // 24 hours
/**
 * Cap on one downloaded catalog. The real catalog is ~10 KiB; the cap only
 * bounds how much a hostile or broken upstream can make this process buffer.
 */
const MAX_REMOTE_BYTES = 1024 * 1024;
const RENAME_ATTEMPTS = 10;
const RENAME_RETRY_MS = 25;

/**
 * Reports whether a hostname can only be reached from this machine. Loopback
 * HTTP is not a network hop, so a plain-http catalog served there cannot be
 * downgraded by anyone in between; that is the one transport exception below
 * (it is what the local mock-server tests use).
 * @param {string} hostname - URL hostname (IPv6 brackets optional).
 * @returns {boolean} True for localhost, 127.0.0.0/8 and ::1.
 */
function isLoopbackHost(hostname) {
  const host = String(hostname || '').toLowerCase().replace(/^\[|\]$/g, '');
  return host === 'localhost' || host === '::1' || /^127\./.test(host);
}

/**
 * Refuses a transport this module will not accept a catalog over: HTTPS
 * everywhere, plain HTTP only on loopback (see isLoopbackHost). Applied to the
 * first hop and to every redirect hop, so a redirect can never downgrade the
 * fetch.
 * @param {URL} parsed - Parsed target URL.
 * @returns {string|null} Refusal sentence, or null when the transport is allowed.
 */
function transportRefusal(parsed) {
  if (parsed.protocol === 'https:') {
    return null;
  }
  if (parsed.protocol === 'http:' && isLoopbackHost(parsed.hostname)) {
    return null;
  }
  return `refusing ${parsed.protocol} transport for the pricing catalog; https is required (plain http only on loopback)`;
}

/**
 * Decides whether one redirect hop may be followed: the redirect must keep an
 * allowed transport AND stay on the current host or on the pinned catalog host.
 * Both facts are checked before a request is built, so a refused hop touches no
 * network.
 * @param {URL} redirect - Parsed Location of the redirect.
 * @param {URL} current - URL that produced the redirect.
 * @returns {string|null} Refusal sentence, or null when the hop may be followed.
 */
function redirectRefusal(redirect, current) {
  const transport = transportRefusal(redirect);
  if (transport) {
    return `refusing a redirect that downgrades the fetch: ${transport}`;
  }
  const nextHost = redirect.hostname.toLowerCase();
  const sameHost = nextHost === current.hostname.toLowerCase();
  if (!sameHost && nextHost !== DEFAULT_REMOTE_HOST) {
    return `refusing a redirect to a different host (${nextHost}); the pricing catalog is fetched from ${DEFAULT_REMOTE_HOST} only`;
  }
  return null;
}

/**
 * Normalizes an expected catalog digest to bare lowercase hex.
 * @param {string} [value] - Caller-supplied digest, with or without a sha256: prefix.
 * @returns {string|null} 64 hex characters, or null when no pin was supplied.
 */
function normalizeSha256(value) {
  if (typeof value !== 'string') {
    return null;
  }
  const hex = value.trim().toLowerCase().replace(/^sha256:/, '');
  return /^[0-9a-f]{64}$/.test(hex) ? hex : null;
}

/**
 * Failure to persist the pricing cache. Raised so a caller cannot mistake a failed
 * write for a successful one; the previous cache stays intact in this case.
 */
class PricingSaveError extends Error {
  /**
   * @param {string} message - Human-readable failure reason.
   * @param {object} [info={}] - Failure context.
   * @param {string} [info.path] - Cache path that could not be replaced.
   * @param {string} [info.tmpPath] - Temp file used for the write, when one was created.
   * @param {string} [info.code] - Stable error code for callers that branch on the kind.
   * @param {Error} [info.cause] - Underlying fs error.
   */
  constructor(message, info = {}) {
    super(message);
    this.name = 'PricingSaveError';
    this.code = info.code || 'PRICING_SAVE_FAILED';
    this.path = info.path || null;
    this.tmpPath = info.tmpPath || null;
    if (info.cause) {
      this.cause = info.cause;
    }
  }
}

/**
 * Validates the schema and completeness of a pricing catalog object.
 * @param {object} data - Pricing JSON data to validate.
 * @returns {{ valid: boolean, error?: string, modelCount?: number }}
 */
function validatePricingData(data) {
  if (!data || typeof data !== 'object') {
    return { valid: false, error: 'Data is not a valid object' };
  }

  const models = data.models || data.pricing;
  if (!models || typeof models !== 'object' || Array.isArray(models)) {
    return { valid: false, error: 'Missing or invalid "models" dictionary' };
  }

  const modelKeys = Object.keys(models);
  if (modelKeys.length === 0) {
    return { valid: false, error: 'Models dictionary is empty' };
  }

  for (const key of modelKeys) {
    const m = models[key];
    if (!m || typeof m !== 'object') {
      return { valid: false, error: `Invalid entry for model key "${key}"` };
    }

    const input = Number(m.inputPerMillion ?? m.input ?? m.prompt);
    const output = Number(m.outputPerMillion ?? m.output ?? m.completion);

    if (isNaN(input) || input < 0) {
      return { valid: false, error: `Model "${key}" has invalid inputPerMillion rate` };
    }
    if (isNaN(output) || output < 0) {
      return { valid: false, error: `Model "${key}" has invalid outputPerMillion rate` };
    }
  }

  return { valid: true, modelCount: modelKeys.length };
}

/**
 * Fetches JSON content from a remote URL using Node.js built-in https/http modules.
 * Follows redirects (same-or-pinned host, https only) with a configurable timeout,
 * caps the body size and reports the sha256 of the bytes it accepted.
 * @param {string} [targetUrl=DEFAULT_REMOTE_URL] - URL to fetch from.
 * @param {number} [timeoutMs=8000] - Request timeout in milliseconds.
 * @param {number} [maxRedirects=3] - Maximum HTTP redirect hops allowed.
 * @returns {Promise<{ success: boolean, data?: object, digest?: string, error?: string, statusCode?: number }>}
 */
function fetchRemotePricing(targetUrl = DEFAULT_REMOTE_URL, timeoutMs = 8000, maxRedirects = 3) {
  return new Promise(resolve => {
    try {
      const parsed = new URL(targetUrl);
      const refused = transportRefusal(parsed);
      if (refused) {
        return resolve({ success: false, error: refused });
      }
      const client = parsed.protocol === 'http:' ? http : https;

      const reqOptions = {
        protocol: parsed.protocol,
        hostname: parsed.hostname,
        port: parsed.port || (parsed.protocol === 'http:' ? 80 : 443),
        path: parsed.pathname + parsed.search,
        method: 'GET',
        headers: {
          'User-Agent': 'Antigravity-Tools-PriceSyncer/1.0',
          'Accept': 'application/json, text/plain, */*'
        },
        timeout: timeoutMs
      };

      const req = client.request(reqOptions, res => {
        // Handle HTTP Redirects. A hop is checked before it is requested, so a
        // downgrade or a foreign host is refused without touching the network.
        if (
          [301, 302, 307, 308].includes(res.statusCode) &&
          res.headers.location &&
          maxRedirects > 0
        ) {
          let redirect = null;
          try {
            redirect = new URL(res.headers.location, targetUrl);
          } catch (_err) {
            res.resume(); // Discard redirect body
            return resolve({ success: false, error: 'refusing a redirect with an unparseable Location header' });
          }
          const refusedHop = redirectRefusal(redirect, parsed);
          if (refusedHop) {
            res.resume(); // Discard redirect body
            return resolve({ success: false, statusCode: res.statusCode, error: refusedHop });
          }
          res.resume(); // Discard redirect body
          return resolve(fetchRemotePricing(redirect.href, timeoutMs, maxRedirects - 1));
        }

        if (res.statusCode < 200 || res.statusCode >= 300) {
          res.resume();
          return resolve({
            success: false,
            statusCode: res.statusCode,
            error: `HTTP status ${res.statusCode} ${res.statusMessage || ''}`.trim()
          });
        }

        const declared = Number(res.headers['content-length']);
        if (Number.isFinite(declared) && declared > MAX_REMOTE_BYTES) {
          res.resume();
          return resolve({
            success: false,
            statusCode: res.statusCode,
            error: `catalog exceeds the ${MAX_REMOTE_BYTES}-byte cap (declared ${declared} bytes)`
          });
        }

        let rawData = '';
        let received = 0;
        let tooLarge = false;
        res.setEncoding('utf8');
        res.on('data', chunk => {
          if (tooLarge) {
            return;
          }
          received += Buffer.byteLength(chunk, 'utf8');
          if (received > MAX_REMOTE_BYTES) {
            tooLarge = true;
            return;
          }
          rawData += chunk;
        });

        res.on('end', () => {
          if (tooLarge) {
            return resolve({
              success: false,
              statusCode: res.statusCode,
              error: `catalog exceeds the ${MAX_REMOTE_BYTES}-byte cap`
            });
          }
          try {
            const parsedJson = JSON.parse(rawData);
            return resolve({
              success: true,
              statusCode: res.statusCode,
              data: parsedJson,
              digest: crypto.createHash('sha256').update(rawData, 'utf8').digest('hex')
            });
          } catch (jsonErr) {
            return resolve({
              success: false,
              statusCode: res.statusCode,
              error: `JSON parse error: ${jsonErr.message}`
            });
          }
        });
      });

      req.on('timeout', () => {
        req.destroy(new Error(`Request timed out after ${timeoutMs}ms`));
      });

      req.on('error', err => {
        return resolve({
          success: false,
          error: err.message || 'Network connection failed'
        });
      });

      req.end();
    } catch (err) {
      return resolve({
        success: false,
        error: err.message || 'Invalid target URL'
      });
    }
  });
}

/**
 * Reads bundled pricing catalog packaged with the application.
 * @param {string} [filePath=BUNDLED_PRICING_FILE] - Path to bundled data/pricing.json.
 * @returns {object|null} Parsed pricing catalog or null if unavailable.
 */
function loadBundledPricing(filePath = BUNDLED_PRICING_FILE) {
  try {
    if (fs.existsSync(filePath)) {
      const raw = fs.readFileSync(filePath, 'utf8');
      const data = JSON.parse(raw);
      if (validatePricingData(data).valid) {
        return data;
      }
    }
  } catch (_err) {
    // Ignore read/parse error
  }
  return null;
}

/**
 * Reads local cached synced pricing file from ~/.gemini/antigravity_pricing.json.
 * @param {string} [cachePath=SYNC_CACHE_FILE] - Path to local cache.
 * @returns {object|null} Parsed pricing catalog or null if invalid or missing.
 */
function loadLocalSyncedPricing(cachePath = SYNC_CACHE_FILE) {
  try {
    if (fs.existsSync(cachePath)) {
      const raw = fs.readFileSync(cachePath, 'utf8');
      const data = JSON.parse(raw);
      if (validatePricingData(data).valid) {
        return data;
      }
    }
  } catch (_err) {
    // Ignore read/parse error
  }
  return null;
}

/**
 * Blocks the thread briefly without a busy spin where Atomics is available.
 * @param {number} ms - Milliseconds to wait.
 */
function sleepBlocking(ms) {
  try {
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
  } catch (_err) {
    const until = Date.now() + ms;
    while (Date.now() < until) {
      // Last-resort spin when shared memory is unavailable
    }
  }
}

/**
 * Replaces `destPath` with `content` atomically: the payload goes to a sibling temp
 * file first and is renamed into place, so a reader sees either the whole previous
 * catalog or the whole new one, never a truncated file. The temp file is this
 * function's own artifact and never outlives a failure.
 * @param {string} destPath - Destination file path.
 * @param {string} content - Complete file content.
 * @throws {PricingSaveError} When the content cannot be put in place.
 */
function writeFileAtomic(destPath, content) {
  const tmpPath = `${destPath}.${Date.now()}.${process.pid}.${Math.random().toString(36).slice(2, 8)}.tmp`;
  let failure = null;

  try {
    fs.mkdirSync(path.dirname(destPath), { recursive: true });
    fs.writeFileSync(tmpPath, content, 'utf8');

    // Windows can hold a transient lock or AV scan on the destination; retry briefly.
    for (let attempt = 0; attempt < RENAME_ATTEMPTS; attempt++) {
      try {
        fs.renameSync(tmpPath, destPath);
        return;
      } catch (renameErr) {
        failure = renameErr;
        if (attempt < RENAME_ATTEMPTS - 1) {
          sleepBlocking(RENAME_RETRY_MS);
        }
      }
    }
  } catch (writeErr) {
    failure = failure || writeErr;
  }

  try {
    fs.unlinkSync(tmpPath);
  } catch (_unlinkErr) {
    // Nothing to clean up: the temp file was never created or is already gone
  }

  throw new PricingSaveError(`${destPath}: ${failure.message}`, {
    path: destPath,
    tmpPath,
    cause: failure
  });
}

/**
 * Saves a pricing catalog object to the local sync cache file atomically.
 * A failed save leaves the previous cache untouched and is reported, never swallowed.
 * @param {object} pricingData - Pricing catalog data.
 * @param {string} [destPath=SYNC_CACHE_FILE] - Destination cache file path.
 * @param {object} [extraMeta] - Extra `_meta` fields, e.g. the digest of the bytes fetched.
 * @returns {boolean} True once the new catalog is in place.
 * @throws {PricingSaveError} When the cache cannot be written (permissions, locked file, unusable path).
 */
function saveSyncedPricing(pricingData, destPath = SYNC_CACHE_FILE, extraMeta = null) {
  const payload = {
    ...pricingData,
    _meta: {
      ...(extraMeta || {}),
      syncedAt: new Date().toISOString(),
      nodeVersion: process.version,
      platform: process.platform
    }
  };

  let content;
  try {
    content = JSON.stringify(payload, null, 2);
  } catch (serializeErr) {
    throw new PricingSaveError(`${destPath}: catalog is not serializable (${serializeErr.message})`, {
      path: destPath,
      code: 'PRICING_SERIALIZE_FAILED',
      cause: serializeErr
    });
  }

  writeFileAtomic(destPath, content);
  return true;
}

/**
 * Surfaces a degraded sync on the user's output instead of falling back quietly.
 * `syncPricing` keeps returning the fallback catalog (the caller's behavior is
 * unchanged); this only makes a failed refresh visible. Auto-refresh callers pass
 * `silent: true` and stay quiet.
 * @param {{ source: string, path: string, modelCount: number }} result - Fallback result.
 */
function warnDegradedSync(result) {
  try {
    process.stderr.write(
      `  ⚠ pricing sync did not refresh the cache: ${result.path} still holds the ` +
      `${result.source} catalog (${result.modelCount} models), so rates may be stale.\n`
    );
  } catch (_err) {
    // A failed warning must never break the sync path
  }
}

/**
 * Surfaces a sync whose fresh catalog could not be persisted. The cache still serves
 * the previous catalog, so the caller's numbers stay stale until a save succeeds.
 * @param {{ path: string, source: string, modelCount: number, error: string, saveError?: object }} result - Unsaved result.
 */
function warnSaveFailure(result) {
  try {
    const detail = result.saveError && result.saveError.message ? result.saveError.message : result.error;
    process.stderr.write(
      `  ⚠ pricing sync downloaded fresh rates but could not save them (${detail}). ` +
      `The ${result.source} catalog (${result.modelCount} models) stays active, so rates may be stale.\n`
    );
  } catch (_err) {
    // A failed warning must never break the sync path
  }
}

/**
 * Synchronizes model pricing catalog from remote repository.
 * If download fails or validation fails, gracefully falls back to local cache or bundled pricing.
 * @param {object} [options={}] - Options object.
 * @param {string} [options.url] - Custom remote URL.
 * @param {string} [options.destPath] - Custom destination path.
 * @param {number} [options.timeoutMs] - Custom timeout.
 * @param {boolean} [options.silent] - Suppress the degraded-sync warning (auto-refresh path).
 * @param {string} [options.expectedSha256] - Pin for the catalog bytes; a mismatch degrades the sync.
 * @returns {Promise<{ success: boolean, source: string, modelCount: number, version: string, lastUpdated: string, error?: string, digest?: string, saveError?: { name: string, code: string|null, path: string, tmpPath: string|null }, fetchedModelCount?: number, fetchedVersion?: string, path: string, timestamp: string }>}
 */
async function syncPricing(options = {}) {
  const url = options.url || DEFAULT_REMOTE_URL;
  const destPath = options.destPath || SYNC_CACHE_FILE;
  const timeoutMs = options.timeoutMs || 8000;
  const silent = Boolean(options.silent);

  const fetchResult = await fetchRemotePricing(url, timeoutMs);

  // A caller that pins the catalog digest gets exactly those bytes or a
  // reported degradation; the pin is never silently ignored.
  const pinnedDigest = normalizeSha256(options.expectedSha256);
  if (fetchResult.success && pinnedDigest && fetchResult.digest !== pinnedDigest) {
    delete fetchResult.data;
    fetchResult.success = false;
    fetchResult.error = `catalog digest mismatch: downloaded ${fetchResult.digest}, expected ${pinnedDigest}`;
  }

  if (fetchResult.success && fetchResult.data) {
    const validation = validatePricingData(fetchResult.data);
    if (validation.valid) {
      const models = fetchResult.data.models || fetchResult.data.pricing || {};
      let saveError = null;
      try {
        saveSyncedPricing(fetchResult.data, destPath, fetchResult.digest ? { remoteSha256: fetchResult.digest } : null);
      } catch (err) {
        saveError = err;
      }

      // Refresh in-memory config catalog
      config.loadUserConfig();

      if (!saveError) {
        return {
          success: true,
          source: 'remote',
          modelCount: Object.keys(models).length,
          version: fetchResult.data.version || '1.0.0',
          lastUpdated: fetchResult.data.lastUpdated || new Date().toISOString().split('T')[0],
          digest: fetchResult.digest || null,
          timestamp: new Date().toISOString(),
          path: destPath
        };
      }

      // Fresh rates arrived but could not be persisted: the cache still serves the old
      // catalog, so report the degradation instead of claiming a successful save.
      const cachedAfterFailure = loadLocalSyncedPricing(destPath);
      const activeFallback = cachedAfterFailure || loadBundledPricing() || {};
      const activeModels = activeFallback.models || activeFallback.pricing || {};
      const unsaved = {
        success: false,
        source: cachedAfterFailure ? 'cached' : 'bundled',
        modelCount: Object.keys(activeModels).length,
        version: activeFallback.version || '1.0.0',
        lastUpdated: activeFallback.lastUpdated || '',
        error: `Cache write failed: ${saveError.message}`,
        saveError: {
          name: saveError.name || 'Error',
          code: saveError.code || null,
          message: saveError.message,
          path: destPath,
          tmpPath: saveError.tmpPath || null
        },
        fetchedModelCount: Object.keys(models).length,
        fetchedVersion: fetchResult.data.version || '1.0.0',
        timestamp: new Date().toISOString(),
        path: destPath
      };
      if (!silent) {
        warnSaveFailure(unsaved);
      }
      return unsaved;
    }

    // Validation failed on remote data, fallback
    const fallback = loadLocalSyncedPricing(destPath) || loadBundledPricing() || {};
    const fallbackModels = fallback.models || fallback.pricing || {};
    const degraded = {
      success: false,
      source: loadLocalSyncedPricing(destPath) ? 'cached' : 'bundled',
      modelCount: Object.keys(fallbackModels).length,
      version: fallback.version || '1.0.0',
      lastUpdated: fallback.lastUpdated || '',
      error: `Validation error: ${validation.error}`,
      timestamp: new Date().toISOString(),
      path: destPath
    };
    if (!silent) {
      warnDegradedSync(degraded);
    }
    return degraded;
  }

  // Fetch failed, fallback to local cache or bundled
  const cachedData = loadLocalSyncedPricing(destPath);
  const bundledData = loadBundledPricing();
  const activeFallback = cachedData || bundledData || {};
  const activeModels = activeFallback.models || activeFallback.pricing || {};

  const degraded = {
    success: false,
    source: cachedData ? 'cached' : 'bundled',
    modelCount: Object.keys(activeModels).length,
    version: activeFallback.version || '1.0.0',
    lastUpdated: activeFallback.lastUpdated || '',
    error: fetchResult.error || 'Failed to download pricing',
    timestamp: new Date().toISOString(),
    path: destPath
  };
  if (!silent) {
    warnDegradedSync(degraded);
  }
  return degraded;
}

/**
 * Resolves the active pricing catalog from local cache (< 24h old or force) or bundled data.
 * @param {object} [options={}] - Retrieval options.
 * @param {boolean} [options.force=false] - Ignore cache age.
 * @param {number} [options.maxAgeMs=MAX_CACHE_AGE_MS] - Maximum cache freshness age.
 * @param {string} [options.cachePath=SYNC_CACHE_FILE] - Override cache path.
 * @param {string} [options.bundledPath=BUNDLED_PRICING_FILE] - Override bundled path.
 * @returns {object} Pricing catalog with metadata.
 */
function getSyncedPricing(options = {}) {
  const cachePath = options.cachePath || SYNC_CACHE_FILE;
  const bundledPath = options.bundledPath || BUNDLED_PRICING_FILE;
  const maxAgeMs = options.maxAgeMs ?? MAX_CACHE_AGE_MS;
  const force = Boolean(options.force);

  // 1. Try reading local cache
  const cached = loadLocalSyncedPricing(cachePath);
  if (cached) {
    let isFresh = true;
    if (!force && maxAgeMs > 0) {
      const syncedAtStr = cached._meta?.syncedAt;
      const syncedTime = syncedAtStr ? new Date(syncedAtStr).getTime() : 0;
      if (syncedTime > 0 && Date.now() - syncedTime > maxAgeMs) {
        isFresh = false;
      }
    }

    if (force || isFresh) {
      return {
        ...cached,
        _source: 'cache',
        _cachePath: cachePath
      };
    }
  }

  // 2. Try bundled pricing
  const bundled = loadBundledPricing(bundledPath);
  if (bundled) {
    return {
      ...bundled,
      _source: 'bundled',
      _cachePath: bundledPath
    };
  }

  // 3. Fallback to in-memory MODEL_PRICING
  return {
    schemaVersion: 1,
    version: '1.0.0',
    lastUpdated: '2026-08-27',
    sources: {
      google: 'https://ai.google.dev/pricing',
      anthropic: 'https://www.anthropic.com/pricing',
      openai: 'https://openai.com/pricing'
    },
    models: config.MODEL_PRICING,
    _source: 'memory',
    _cachePath: null
  };
}

/**
 * Returns a styled provider badge for terminal display.
 * @param {string} provider - Provider key ('google', 'anthropic', 'openai').
 * @returns {string} Styled provider badge string.
 */
function formatProviderBadge(provider) {
  const p = (provider || '').toLowerCase().trim();
  if (p === 'google' || p.includes('gemini')) {
    return formatter.styleText('[Google]', 'brightCyan');
  }
  if (p === 'anthropic' || p.includes('claude')) {
    return formatter.styleText('[Anthropic]', 'brightMagenta');
  }
  if (p === 'openai' || p.includes('gpt') || p.includes('o1') || p.includes('o3')) {
    return formatter.styleText('[OpenAI]', 'brightGreen');
  }
  return formatter.styleText(`[${p.charAt(0).toUpperCase() + p.slice(1)}]`, 'brightYellow');
}

/**
 * Formats a unit rate into a formatted currency string aligned for table columns.
 * @param {number} rateUsdPerMillion - Rate in USD per 1M tokens.
 * @param {string} currencyCode - Target currency.
 * @returns {string} Formatted currency rate string.
 */
function formatUnitRate(rateUsdPerMillion, currencyCode = 'usd') {
  const cKey = (currencyCode || 'usd').toLowerCase();
  const currInfo = config.CURRENCIES[cKey] || config.CURRENCIES.usd;
  const converted = config.convertCurrency(rateUsdPerMillion, cKey);

  let formatted;
  if (cKey === 'krw') {
    formatted = converted >= 100 ? converted.toFixed(1) : converted.toFixed(2);
  } else if (cKey === 'jpy') {
    formatted = converted.toFixed(2);
  } else {
    formatted = converted.toFixed(4);
  }

  if (currInfo.position === 'after') {
    return `${formatted}${currInfo.symbol}`;
  }
  return `${currInfo.symbol}${formatted}`;
}

/**
 * Formats all selectable Antigravity CLI (/model) choices into a rich ANSI terminal table.
 * @param {string} [currency='usd'] - Target display currency.
 * @param {string} [lang='en'] - Interface language ('en', 'ko', 'ja', 'zh').
 * @param {object} [options={}] - Additional rendering options.
 * @returns {string} Rendered ANSI table string.
 */
function formatPricingTable(currency = 'usd', lang = 'en', options = {}) {
  const targetLang = lang || i18n.getLocale() || 'en';
  i18n.setLocale(targetLang);

  const pricingCatalog = options.catalog || getSyncedPricing(options);
  const modelsDict = pricingCatalog.models || pricingCatalog.pricing || config.MODEL_PRICING;
  const targetCurrency = (currency || 'usd').toLowerCase();

  // Categorize models by provider order
  const modelEntries = Object.entries(modelsDict)
    .filter(([key]) => key !== 'default')
    .map(([key, def]) => {
      const id = def.id || key;
      const displayName = def.displayName || config.formatModelDisplayName(id);
      let provider = def.provider;
      if (!provider) {
        if (id.startsWith('gemini')) provider = 'google';
        else if (id.startsWith('claude')) provider = 'anthropic';
        else if (id.startsWith('gpt') || id.startsWith('o1') || id.startsWith('o3')) provider = 'openai';
        else provider = 'other';
      }

      return {
        id,
        displayName,
        provider,
        contextWindow: def.contextWindow || '1M',
        inputPerMillion: Number(def.inputPerMillion ?? 0),
        cachedInputPerMillion: Number(def.cachedInputPerMillion ?? (def.inputPerMillion * 0.25) ?? 0),
        outputPerMillion: Number(def.outputPerMillion ?? 0),
        notes: def.notes || ''
      };
    });

  // Sort by provider (Google, Anthropic, OpenAI, Others) then by rate descending/model
  const providerPriority = { google: 1, anthropic: 2, openai: 3, other: 4 };
  modelEntries.sort((a, b) => {
    const pDiff = (providerPriority[a.provider] || 99) - (providerPriority[b.provider] || 99);
    if (pDiff !== 0) return pDiff;
    return a.displayName.localeCompare(b.displayName);
  });

  // Column definitions and widths
  const colProviderWidth = 13;
  const colModelWidth = 27;
  const colContextWidth = 9;
  const colInputWidth = 14;
  const colCachedWidth = 14;
  const colOutputWidth = 14;

  const totalInnerWidth =
    colProviderWidth +
    colModelWidth +
    colContextWidth +
    colInputWidth +
    colCachedWidth +
    colOutputWidth +
    5 * 3; // 5 internal ' │ ' separators (3 chars each)

  // Header Titles
  const titleText = i18n.t('pricingCatalogTitle', {}, targetLang);
  const subtitleText = i18n.t('pricingCatalogSubtitle', {}, targetLang);

  // Border characters
  const topBorder = `┌${'─'.repeat(totalInnerWidth + 2)}┐`;
  const midBorder = `├${'─'.repeat(colProviderWidth + 2)}┬${'─'.repeat(colModelWidth + 2)}┬${'─'.repeat(colContextWidth + 2)}┬${'─'.repeat(colInputWidth + 2)}┬${'─'.repeat(colCachedWidth + 2)}┬${'─'.repeat(colOutputWidth + 2)}┤`;
  const rowDivider = `├${'─'.repeat(colProviderWidth + 2)}┼${'─'.repeat(colModelWidth + 2)}┼${'─'.repeat(colContextWidth + 2)}┼${'─'.repeat(colInputWidth + 2)}┼${'─'.repeat(colCachedWidth + 2)}┼${'─'.repeat(colOutputWidth + 2)}┤`;
  const bottomBorder = `└${'─'.repeat(colProviderWidth + 2)}┴${'─'.repeat(colModelWidth + 2)}┴${'─'.repeat(colContextWidth + 2)}┴${'─'.repeat(colInputWidth + 2)}┴${'─'.repeat(colCachedWidth + 2)}┴${'─'.repeat(colOutputWidth + 2)}┘`;

  // Header line
  const headerCols = [
    formatter.padDisplay(formatter.styleText(i18n.t('colProvider', {}, targetLang), 'bold'), colProviderWidth, 'left'),
    formatter.padDisplay(formatter.styleText(i18n.t('colModel', {}, targetLang), 'bold'), colModelWidth, 'left'),
    formatter.padDisplay(formatter.styleText(i18n.t('colContext', {}, targetLang), 'bold'), colContextWidth, 'center'),
    formatter.padDisplay(formatter.styleText(i18n.t('colInputRate', {}, targetLang), 'bold'), colInputWidth, 'right'),
    formatter.padDisplay(formatter.styleText(i18n.t('colCachedRate', {}, targetLang), 'bold'), colCachedWidth, 'right'),
    formatter.padDisplay(formatter.styleText(i18n.t('colOutputRate', {}, targetLang), 'bold'), colOutputWidth, 'right')
  ];
  const headerRow = `│ ${headerCols.join(' │ ')} │`;

  const lines = [];
  lines.push(topBorder);
  lines.push(`│ ${formatter.padDisplay(formatter.styleText(titleText, 'bold'), totalInnerWidth, 'center')} │`);
  lines.push(`│ ${formatter.padDisplay(formatter.styleText(subtitleText, 'dim'), totalInnerWidth, 'center')} │`);
  lines.push(midBorder);
  lines.push(headerRow);
  lines.push(rowDivider);

  let currentProvider = null;
  for (let idx = 0; idx < modelEntries.length; idx++) {
    const m = modelEntries[idx];

    // Add separator between provider groups
    if (currentProvider !== null && currentProvider !== m.provider) {
      lines.push(rowDivider);
    }
    currentProvider = m.provider;

    const badge = formatProviderBadge(m.provider);
    const modelName = formatter.styleText(m.displayName, 'brightWhite');
    const ctx = formatter.styleText(m.contextWindow, 'cyan');
    const inputStr = formatter.styleText(formatUnitRate(m.inputPerMillion, targetCurrency), 'green');
    const cachedStr = formatter.styleText(formatUnitRate(m.cachedInputPerMillion, targetCurrency), 'yellow');
    const outputStr = formatter.styleText(formatUnitRate(m.outputPerMillion, targetCurrency), 'magenta');

    const rowCells = [
      formatter.padDisplay(badge, colProviderWidth, 'left'),
      formatter.padDisplay(modelName, colModelWidth, 'left'),
      formatter.padDisplay(ctx, colContextWidth, 'center'),
      formatter.padDisplay(inputStr, colInputWidth, 'right'),
      formatter.padDisplay(cachedStr, colCachedWidth, 'right'),
      formatter.padDisplay(outputStr, colOutputWidth, 'right')
    ];

    lines.push(`│ ${rowCells.join(' │ ')} │`);
  }

  lines.push(bottomBorder);

  // Footer Metadata
  const versionStr = pricingCatalog.version || '1.0.0';
  const lastUpdatedStr = pricingCatalog.lastUpdated || '2026-08-27';
  const sourceLabel = pricingCatalog._source === 'remote' ? 'Live Remote' : (pricingCatalog._source === 'cache' ? 'Local Cache' : 'Bundled');

  const footerVersion = i18n.t('pricingFooterVersion', {
    version: versionStr,
    date: lastUpdatedStr,
    source: sourceLabel
  }, targetLang);

  const sourcesList = Object.values(pricingCatalog.sources || {
    google: 'ai.google.dev/pricing',
    anthropic: 'anthropic.com/pricing',
    openai: 'openai.com/pricing'
  }).map(s => s.replace(/^https?:\/\//, '')).join(' • ');

  const footerSources = i18n.t('pricingFooterSources', {
    sources: sourcesList
  }, targetLang);

  const footerTip = i18n.t('pricingFooterSyncTip', {}, targetLang);

  lines.push('');
  lines.push(`  ${formatter.styleText('ℹ', 'cyan')} ${footerVersion}`);
  lines.push(`  ${formatter.styleText('🌐', 'blue')} ${footerSources}`);
  lines.push(`  ${formatter.styleText('💡', 'yellow')} ${footerTip}`);

  return lines.join('\n');
}

module.exports = {
  DEFAULT_REMOTE_URL,
  DEFAULT_REMOTE_HOST,
  SYNC_CACHE_FILE,
  BUNDLED_PRICING_FILE,
  MAX_CACHE_AGE_MS,
  MAX_REMOTE_BYTES,
  PricingSaveError,
  validatePricingData,
  fetchRemotePricing,
  loadBundledPricing,
  loadLocalSyncedPricing,
  saveSyncedPricing,
  syncPricing,
  getSyncedPricing,
  formatProviderBadge,
  formatUnitRate,
  formatPricingTable
};
