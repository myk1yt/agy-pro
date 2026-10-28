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
const { URL } = require('url');

const config = require('./config');
const i18n = require('./i18n');
const formatter = require('./formatter');

/**
 * Default remote pricing repository endpoint and system cache paths.
 */
const DEFAULT_REMOTE_URL = 'https://raw.githubusercontent.com/myk1yt/agy-tools/main/data/pricing.json';
const SYNC_CACHE_FILE = path.join(os.homedir(), '.gemini', 'antigravity_pricing.json');
const BUNDLED_PRICING_FILE = path.join(__dirname, '..', 'data', 'pricing.json');
const MAX_CACHE_AGE_MS = 24 * 60 * 60 * 1000; // 24 hours

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
 * Supports redirects and configurable timeout.
 * @param {string} [targetUrl=DEFAULT_REMOTE_URL] - URL to fetch from.
 * @param {number} [timeoutMs=8000] - Request timeout in milliseconds.
 * @param {number} [maxRedirects=3] - Maximum HTTP redirect hops allowed.
 * @returns {Promise<{ success: boolean, data?: object, error?: string, statusCode?: number }>}
 */
function fetchRemotePricing(targetUrl = DEFAULT_REMOTE_URL, timeoutMs = 8000, maxRedirects = 3) {
  return new Promise(resolve => {
    try {
      const parsed = new URL(targetUrl);
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
        // Handle HTTP Redirects
        if (
          [301, 302, 307, 308].includes(res.statusCode) &&
          res.headers.location &&
          maxRedirects > 0
        ) {
          const redirectUrl = new URL(res.headers.location, targetUrl).href;
          res.resume(); // Discard redirect body
          return resolve(fetchRemotePricing(redirectUrl, timeoutMs, maxRedirects - 1));
        }

        if (res.statusCode < 200 || res.statusCode >= 300) {
          res.resume();
          return resolve({
            success: false,
            statusCode: res.statusCode,
            error: `HTTP status ${res.statusCode} ${res.statusMessage || ''}`.trim()
          });
        }

        let rawData = '';
        res.setEncoding('utf8');
        res.on('data', chunk => {
          rawData += chunk;
        });

        res.on('end', () => {
          try {
            const parsedJson = JSON.parse(rawData);
            return resolve({
              success: true,
              statusCode: res.statusCode,
              data: parsedJson
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
 * Saves a pricing catalog object to the local sync cache file atomically.
 * @param {object} pricingData - Pricing catalog data.
 * @param {string} [destPath=SYNC_CACHE_FILE] - Destination cache file path.
 * @returns {boolean} True if saved successfully.
 */
function saveSyncedPricing(pricingData, destPath = SYNC_CACHE_FILE) {
  try {
    const dir = path.dirname(destPath);
    if (!fs.existsSync(dir)) {
      fs.mkdirSync(dir, { recursive: true });
    }
    const payload = {
      ...pricingData,
      _meta: {
        syncedAt: new Date().toISOString(),
        nodeVersion: process.version,
        platform: process.platform
      }
    };
    fs.writeFileSync(destPath, JSON.stringify(payload, null, 2), 'utf8');
    return true;
  } catch (_err) {
    return false;
  }
}

/**
 * Synchronizes model pricing catalog from remote repository.
 * If download fails or validation fails, gracefully falls back to local cache or bundled pricing.
 * @param {object} [options={}] - Options object.
 * @param {string} [options.url] - Custom remote URL.
 * @param {string} [options.destPath] - Custom destination path.
 * @param {number} [options.timeoutMs] - Custom timeout.
 * @returns {Promise<{ success: boolean, source: string, modelCount: number, version: string, lastUpdated: string, error?: string, path: string, timestamp: string }>}
 */
async function syncPricing(options = {}) {
  const url = options.url || DEFAULT_REMOTE_URL;
  const destPath = options.destPath || SYNC_CACHE_FILE;
  const timeoutMs = options.timeoutMs || 8000;

  const fetchResult = await fetchRemotePricing(url, timeoutMs);

  if (fetchResult.success && fetchResult.data) {
    const validation = validatePricingData(fetchResult.data);
    if (validation.valid) {
      saveSyncedPricing(fetchResult.data, destPath);

      // Refresh in-memory config catalog
      config.loadUserConfig();

      const models = fetchResult.data.models || fetchResult.data.pricing || {};
      return {
        success: true,
        source: 'remote',
        modelCount: Object.keys(models).length,
        version: fetchResult.data.version || '1.0.0',
        lastUpdated: fetchResult.data.lastUpdated || new Date().toISOString().split('T')[0],
        timestamp: new Date().toISOString(),
        path: destPath
      };
    }

    // Validation failed on remote data, fallback
    const fallback = loadLocalSyncedPricing(destPath) || loadBundledPricing() || {};
    const fallbackModels = fallback.models || fallback.pricing || {};
    return {
      success: false,
      source: loadLocalSyncedPricing(destPath) ? 'cached' : 'bundled',
      modelCount: Object.keys(fallbackModels).length,
      version: fallback.version || '1.0.0',
      lastUpdated: fallback.lastUpdated || '',
      error: `Validation error: ${validation.error}`,
      timestamp: new Date().toISOString(),
      path: destPath
    };
  }

  // Fetch failed, fallback to local cache or bundled
  const cachedData = loadLocalSyncedPricing(destPath);
  const bundledData = loadBundledPricing();
  const activeFallback = cachedData || bundledData || {};
  const activeModels = activeFallback.models || activeFallback.pricing || {};

  return {
    success: false,
    source: cachedData ? 'cached' : 'bundled',
    modelCount: Object.keys(activeModels).length,
    version: activeFallback.version || '1.0.0',
    lastUpdated: activeFallback.lastUpdated || '',
    error: fetchResult.error || 'Failed to download pricing',
    timestamp: new Date().toISOString(),
    path: destPath
  };
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
  SYNC_CACHE_FILE,
  BUNDLED_PRICING_FILE,
  MAX_CACHE_AGE_MS,
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
