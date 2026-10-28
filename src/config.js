/**
 * @fileoverview Configuration, dynamic model pricing catalog, currency exchange rates,
 * and system path resolvers for Antigravity Token & Cost Tracker.
 * Supports official model catalogs, remote pricing synchronization cache,
 * smart fuzzy heuristic pricing resolution, and dynamic user config overrides.
 */

const fs = require('fs');
const path = require('path');
const os = require('os');

/**
 * Standard system paths for Antigravity and token tracking cache.
 */
const HOME_DIR = os.homedir();
const GEMINI_DIR = path.join(HOME_DIR, '.gemini');
const ANTIGRAVITY_DIR = path.join(GEMINI_DIR, 'antigravity-cli');
const BRAIN_DIR = path.join(ANTIGRAVITY_DIR, 'brain');
const HISTORY_FILE = path.join(ANTIGRAVITY_DIR, 'history.jsonl');
const SETTINGS_FILE = path.join(ANTIGRAVITY_DIR, 'settings.json');
const CACHE_FILE = path.join(GEMINI_DIR, 'token_tracker_cache.json');
const USER_CONFIG_FILE = path.join(GEMINI_DIR, 'antigravity_tokens.json');
const USER_PRICING_FILE = path.join(GEMINI_DIR, 'pricing.json');
const ALT_PRICING_FILE = path.join(ANTIGRAVITY_DIR, 'pricing.json');
const REMOTE_PRICING_CACHE_FILE = path.join(GEMINI_DIR, 'antigravity_pricing.json');
const GEMINI_QUOTA_CACHE_FILE = path.join(GEMINI_DIR, 'gemini_quota_cache.json');
const GEMINI_QUOTA_CACHE_TTL_MS = 30000;
const BUNDLED_PRICING_FILE = path.join(__dirname, '..', 'data', 'pricing.json');

/**
 * Dashboard (real-time HTML) artifact paths and server defaults.
 * All dashboard artifacts live under ~/.gemini/antigravity-dashboard/.
 */
const DASHBOARD_DIR = path.join(GEMINI_DIR, 'antigravity-dashboard');
const DASHBOARD_HTML_FILE = path.join(DASHBOARD_DIR, 'dashboard.html');
const DASHBOARD_DATA_JS = path.join(DASHBOARD_DIR, 'dashboard-data.js');
const DASHBOARD_DATA_JSON = path.join(DASHBOARD_DIR, 'dashboard-data.json');
const DASHBOARD_SERVER_PORT_FILE = path.join(DASHBOARD_DIR, 'dashboard-server.json');
const DASHBOARD_DEFAULT_PORT = 8787;
const DASHBOARD_WRITE_THROTTLE_MS = 2000;

/**
 * Regex patterns for smart fuzzy heuristic pricing tier detection.
 * Uses token-boundary matching to prevent false substring matches (e.g., 'mini' in 'gemini').
 */
const FLASH_PATTERN = /(?:^|[^a-z0-9])(flash|lite|mini|haiku|fast|small|turbo|low)(?:[^a-z0-9]|$)/i;
const PRO_PATTERN = /(?:^|[^a-z0-9])(pro|ultra|opus|sonnet|large|max|high)(?:[^a-z0-9]|$)/i;
const FREE_PATTERN = /(?:^|[^a-z0-9])(free|flat|zero|local|ollama)(?:[^a-z0-9]|$)/i;

const DEFAULT_QUOTA_5H = 20000000;
const DEFAULT_QUOTA_7D = 150000000;

/**
 * Baseline model pricing catalog (prices in USD per 1,000,000 tokens).
 * Covering all Google Gemini, Anthropic Claude, and OpenAI models in Antigravity CLI (/model).
 */
const MODEL_PRICING = {
  'gemini-3.7-flash': {
    id: 'gemini-3.7-flash',
    provider: 'google',
    displayName: 'Gemini 3.7 Flash',
    contextWindow: '1M',
    inputPerMillion: 0.05,
    cachedInputPerMillion: 0.0125,
    outputPerMillion: 0.20,
    aliases: ['gemini-3.7-flash', 'gemini 3.7 flash', 'gemini-3-7-flash']
  },
  'gemini-3.7-flash-thinking': {
    id: 'gemini-3.7-flash-thinking',
    provider: 'google',
    displayName: 'Gemini 3.7 Flash Thinking',
    contextWindow: '1M',
    inputPerMillion: 0.15,
    cachedInputPerMillion: 0.0375,
    outputPerMillion: 0.60,
    aliases: ['gemini-3.7-flash-thinking', 'gemini 3.7 flash thinking']
  },
  'gemini-2.5-pro': {
    id: 'gemini-2.5-pro',
    provider: 'google',
    displayName: 'Gemini 2.5 Pro',
    contextWindow: '2M',
    inputPerMillion: 1.25,
    cachedInputPerMillion: 0.3125,
    outputPerMillion: 5.00,
    aliases: ['gemini-2.5-pro', 'gemini 2.5 pro']
  },
  'gemini-2.5-flash': {
    id: 'gemini-2.5-flash',
    provider: 'google',
    displayName: 'Gemini 2.5 Flash',
    contextWindow: '1M',
    inputPerMillion: 0.075,
    cachedInputPerMillion: 0.01875,
    outputPerMillion: 0.30,
    aliases: ['gemini-2.5-flash', 'gemini 2.5 flash']
  },
  'gemini-2.0-flash': {
    id: 'gemini-2.0-flash',
    provider: 'google',
    displayName: 'Gemini 2.0 Flash',
    contextWindow: '1M',
    inputPerMillion: 0.10,
    cachedInputPerMillion: 0.025,
    outputPerMillion: 0.40,
    aliases: ['gemini-2.0-flash', 'gemini 2.0 flash']
  },
  'claude-3.7-sonnet': {
    id: 'claude-3.7-sonnet',
    provider: 'anthropic',
    displayName: 'Claude 3.7 Sonnet',
    contextWindow: '200k',
    inputPerMillion: 3.00,
    cachedInputPerMillion: 0.30,
    outputPerMillion: 15.00,
    aliases: ['claude-3.7-sonnet', 'claude 3.7 sonnet']
  },
  'claude-3.5-sonnet': {
    id: 'claude-3.5-sonnet',
    provider: 'anthropic',
    displayName: 'Claude 3.5 Sonnet',
    contextWindow: '200k',
    inputPerMillion: 3.00,
    cachedInputPerMillion: 0.30,
    outputPerMillion: 15.00,
    aliases: ['claude-3.5-sonnet', 'claude 3.5 sonnet', 'sonnet']
  },
  'claude-3.5-haiku': {
    id: 'claude-3.5-haiku',
    provider: 'anthropic',
    displayName: 'Claude 3.5 Haiku',
    contextWindow: '200k',
    inputPerMillion: 0.80,
    cachedInputPerMillion: 0.08,
    outputPerMillion: 4.00,
    aliases: ['claude-3.5-haiku', 'claude 3.5 haiku', 'haiku']
  },
  'gpt-4o': {
    id: 'gpt-4o',
    provider: 'openai',
    displayName: 'GPT-4o',
    contextWindow: '128k',
    inputPerMillion: 2.50,
    cachedInputPerMillion: 1.25,
    outputPerMillion: 10.00,
    aliases: ['gpt-4o', 'gpt 4o']
  },
  'o3-mini': {
    id: 'o3-mini',
    provider: 'openai',
    displayName: 'o3-mini',
    contextWindow: '200k',
    inputPerMillion: 1.10,
    cachedInputPerMillion: 0.55,
    outputPerMillion: 4.40,
    aliases: ['o3-mini', 'o3 mini']
  },
  'o1': {
    id: 'o1',
    provider: 'openai',
    displayName: 'o1',
    contextWindow: '200k',
    inputPerMillion: 15.00,
    cachedInputPerMillion: 7.50,
    outputPerMillion: 60.00,
    aliases: ['o1', 'o1-preview']
  },
  'default': {
    id: 'default',
    provider: 'google',
    displayName: 'Gemini 3.7 Flash (Default)',
    contextWindow: '1M',
    inputPerMillion: 0.05,
    cachedInputPerMillion: 0.0125,
    outputPerMillion: 0.20,
    aliases: ['default']
  }
};

/**
 * Pre-sorted alias list for longest-match-first model alias resolution.
 * Prevents shorter aliases (e.g., 'gpt-4o') from matching longer inputs
 * (e.g., 'gpt-4o-mini') when iterating Object.keys in insertion order.
 */
let _sortedAliases = null;

function _buildSortedAliases() {
  const entries = [];
  for (const key of Object.keys(MODEL_PRICING)) {
    if (key === 'default') continue;
    const info = MODEL_PRICING[key];
    if (!info.aliases) continue;
    for (const alias of info.aliases) {
      entries.push({ alias: alias.toLowerCase(), info });
    }
  }
  entries.sort((a, b) => b.alias.length - a.alias.length);
  _sortedAliases = entries;
}

/**
 * Currency conversion table relative to USD.
 */
const CURRENCIES = {
  usd: {
    code: 'USD',
    symbol: '$',
    rate: 1.0,
    precision: 4,
    displayDecimals: 4,
    position: 'before',
    name: 'US Dollar'
  },
  krw: {
    code: 'KRW',
    symbol: '₩',
    rate: 1450.0,
    precision: 0,
    displayDecimals: 1,
    position: 'before',
    name: 'South Korean Won'
  },
  jpy: {
    code: 'JPY',
    symbol: '¥',
    rate: 155.0,
    precision: 2,
    displayDecimals: 2,
    position: 'before',
    name: 'Japanese Yen'
  },
  eur: {
    code: 'EUR',
    symbol: '€',
    rate: 0.95,
    precision: 4,
    displayDecimals: 4,
    position: 'after',
    name: 'Euro'
  },
  gbp: {
    code: 'GBP',
    symbol: '£',
    rate: 0.80,
    precision: 4,
    displayDecimals: 4,
    position: 'before',
    name: 'British Pound'
  }
};

/**
 * Formats a model name into a human-readable title.
 * @param {string} name - Raw model string.
 * @returns {string} Formatted display name.
 */
function formatModelDisplayName(name) {
  if (!name || typeof name !== 'string') return 'Default Model';
  return name
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map(token => {
      if (/^\d+(\.\d+)*[a-z]?$/i.test(token)) {
        return token;
      }
      return token.charAt(0).toUpperCase() + token.slice(1);
    })
    .join(' ');
}

/**
 * Dynamically resolves pricing tier using regex/fuzzy heuristic pattern matching
 * for models not explicitly registered in MODEL_PRICING.
 * @param {string} modelName - Model identifier or display name.
 * @returns {object} Pricing configuration with tier information.
 */
function smartHeuristicPricing(modelName) {
  if (!modelName || typeof modelName !== 'string') {
    return { ...MODEL_PRICING['default'] };
  }

  const normalized = modelName.toLowerCase().trim();
  const displayName = formatModelDisplayName(modelName);

  // 1. Free Tier heuristic: contains free, flat, zero, local, or ollama
  if (FREE_PATTERN.test(normalized)) {
    return {
      id: normalized,
      displayName: displayName || 'Custom Free Model',
      inputPerMillion: 0.0,
      cachedInputPerMillion: 0.0,
      outputPerMillion: 0.0,
      aliases: [normalized],
      tier: 'free'
    };
  }

  // 2. Flash Tier heuristic: contains flash, lite, mini, haiku, fast, small, turbo, or low
  if (FLASH_PATTERN.test(normalized)) {
    return {
      id: normalized,
      displayName: displayName || 'Custom Flash Model',
      inputPerMillion: 0.15,
      cachedInputPerMillion: 0.0375,
      outputPerMillion: 0.60,
      aliases: [normalized],
      tier: 'flash'
    };
  }

  // 3. Pro Tier heuristic: contains pro, ultra, opus, sonnet, large, max, or high
  if (PRO_PATTERN.test(normalized)) {
    return {
      id: normalized,
      displayName: displayName || 'Custom Pro Model',
      inputPerMillion: 1.25,
      cachedInputPerMillion: 0.3125,
      outputPerMillion: 5.00,
      aliases: [normalized],
      tier: 'pro'
    };
  }

  // 4. Graceful fallback to Default Flash Tier
  return {
    id: normalized,
    displayName: `${displayName} (Default)`,
    inputPerMillion: 0.15,
    cachedInputPerMillion: 0.0375,
    outputPerMillion: 0.60,
    aliases: [normalized],
    tier: 'default'
  };
}

let _cachedActiveModel = null;
let _cachedActiveModelMtime = 0;

/**
 * Reads the active model name configured in Antigravity settings.json.
 * @returns {string} The active model name or a default fallback.
 */
function getActiveModelFromSettings() {
  try {
    if (fs.existsSync(SETTINGS_FILE)) {
      const stat = fs.statSync(SETTINGS_FILE);
      if (_cachedActiveModel && stat.mtimeMs === _cachedActiveModelMtime) {
        return _cachedActiveModel;
      }
      const content = fs.readFileSync(SETTINGS_FILE, 'utf8');
      const settings = JSON.parse(content);
      if (settings && typeof settings.model === 'string' && settings.model.trim()) {
        _cachedActiveModel = settings.model.trim();
        _cachedActiveModelMtime = stat.mtimeMs;
        return _cachedActiveModel;
      }
    }
  } catch (_err) {
    // Gracefully ignore file read or parse error
  }
  return _cachedActiveModel || 'gemini-3.7-flash';
}

/**
 * Strips ONE trailing parenthesized group (e.g. reasoning-effort suffix) from a
 * model display name so pricing resolves on the base model.
 *   "Gemini 3.7 Flash (Low)"     -> "Gemini 3.7 Flash"
 *   "Claude Opus 4.6 (Thinking)" -> "Claude Opus 4.6"
 *   "gemini-3.7-flash"           -> "gemini-3.7-flash" (no parens -> unchanged)
 * Null/non-string values pass through untouched; if stripping would yield an
 * empty string (e.g. "(High)"), the original input is returned.
 * @param {string} [modelName] - Model display name, possibly effort-suffixed.
 * @returns {string} Base model name without the trailing parenthesized group.
 */
function getBaseModelName(modelName) {
  if (!modelName || typeof modelName !== 'string') return modelName;
  return modelName.replace(/\s*\([^)]*\)\s*$/, '').trim() || modelName;
}

/**
 * Resolves pricing structure for a given model string or alias.
 * Checks registered MODEL_PRICING (including synced rates and user overrides) first,
 * then falls back to smart fuzzy heuristic resolution.
 * @param {string} [modelName] - Optional model identifier or display name.
 * @returns {object} Pricing configuration for the matched model.
 */
function getModelPricing(modelName) {
  const rawTarget = getBaseModelName(modelName || getActiveModelFromSettings());
  const target = (rawTarget || '').toLowerCase().trim();

  // 1. Exact key match in MODEL_PRICING
  if (MODEL_PRICING[target]) {
    return MODEL_PRICING[target];
  }

  // 1b. Direct alias match in MODEL_PRICING definitions
  for (const def of Object.values(MODEL_PRICING)) {
    if (def && Array.isArray(def.aliases) && def.aliases.some(a => (typeof a === 'string' && a.toLowerCase().trim() === target))) {
      return def;
    }
  }

  // 2. Exact or substring match against known model aliases (longest alias wins)
  if (_sortedAliases) {
    for (const { alias, info } of _sortedAliases) {
      if (alias === target || target.includes(alias)) {
        return info;
      }
    }
  }

  // 3. Fallback to smart fuzzy heuristic pattern matching
  return smartHeuristicPricing(rawTarget || target);
}

/**
 * Helper to normalize and merge a dictionary of custom model definitions into MODEL_PRICING.
 * @param {object} pricingDict - Map of model keys to pricing definitions.
 * @param {object} [destination] - Optional destination object to record custom pricing.
 */
function mergePricingDict(pricingDict, destination = null) {
  if (!pricingDict || typeof pricingDict !== 'object') return;
  for (const [key, def] of Object.entries(pricingDict)) {
    if (!def || typeof def !== 'object') continue;
    const normalizedKey = key.toLowerCase().trim();
    const id = def.id || normalizedKey;
    const displayName = def.displayName || def.name || formatModelDisplayName(id);
    const inputPerMillion = Number(def.inputPerMillion ?? def.input ?? def.prompt ?? 0);
    const cachedInputPerMillion = Number(
      def.cachedInputPerMillion ?? def.cachedInput ?? def.cached ?? (inputPerMillion * 0.25)
    );
    const outputPerMillion = Number(def.outputPerMillion ?? def.output ?? def.completion ?? 0);
    const provider = def.provider || '';
    const contextWindow = def.contextWindow || '1M';
    const notes = def.notes || '';
    const customAliases = Array.isArray(def.aliases)
      ? def.aliases.map(a => String(a).toLowerCase().trim())
      : [];
    const aliases = Array.from(
      new Set([normalizedKey, id.toLowerCase().trim(), ...customAliases])
    );

    const modelEntry = {
      id,
      displayName,
      provider,
      contextWindow,
      inputPerMillion,
      cachedInputPerMillion,
      outputPerMillion,
      notes,
      aliases
    };

    MODEL_PRICING[normalizedKey] = modelEntry;
    if (destination && typeof destination === 'object') {
      destination[normalizedKey] = modelEntry;
    }
  }
  _buildSortedAliases();
}

/**
 * Loads and merges pricing definitions according to priority:
 * 1. Bundled data/pricing.json (if present)
 * 2. Synced remote cache ~/.gemini/antigravity_pricing.json (if present)
 * 3. Local pricing files ~/.gemini/pricing.json and ~/.gemini/antigravity-cli/pricing.json
 * 4. User config overrides ~/.gemini/antigravity_tokens.json (Highest Priority)
 * @returns {object} Merged configuration object.
 */
function loadUserConfig() {
  const config = {
    currency: 'usd',
    lang: null,
    free: false,
    noCost: false,
    customRates: {},
    customPricing: {},
    quota: {
      limit5h: DEFAULT_QUOTA_5H,
      limit7d: DEFAULT_QUOTA_7D
    }
  };

  // 1. Try loading bundled data/pricing.json
  try {
    if (fs.existsSync(BUNDLED_PRICING_FILE)) {
      const raw = fs.readFileSync(BUNDLED_PRICING_FILE, 'utf8');
      const data = JSON.parse(raw);
      if (data && typeof data === 'object') {
        mergePricingDict(data.models || data.pricing || data);
      }
    }
  } catch (_err) {
    // Ignore bundled read error
  }

  // 2. Try loading synced remote cache ~/.gemini/antigravity_pricing.json
  try {
    if (fs.existsSync(REMOTE_PRICING_CACHE_FILE)) {
      const raw = fs.readFileSync(REMOTE_PRICING_CACHE_FILE, 'utf8');
      const data = JSON.parse(raw);
      if (data && typeof data === 'object') {
        mergePricingDict(data.models || data.pricing || data);
      }
    }
  } catch (_err) {
    // Ignore cache read error
  }

  // 3. Try reading ~/.gemini/pricing.json or ~/.gemini/antigravity-cli/pricing.json
  const pricingFiles = [USER_PRICING_FILE, ALT_PRICING_FILE];
  for (const pFile of pricingFiles) {
    try {
      if (fs.existsSync(pFile)) {
        const raw = fs.readFileSync(pFile, 'utf8');
        const data = JSON.parse(raw);
        if (data && typeof data === 'object') {
          mergePricingDict(data.pricing || data.models || data, config.customPricing);
        }
      }
    } catch (_err) {
      // Ignore invalid pricing file
    }
  }

  // 4. Try reading user custom overrides ~/.gemini/antigravity_tokens.json (Highest Priority)
  try {
    if (fs.existsSync(USER_CONFIG_FILE)) {
      const raw = fs.readFileSync(USER_CONFIG_FILE, 'utf8');
      const user = JSON.parse(raw);
      if (user.currency && CURRENCIES[user.currency.toLowerCase()]) {
        config.currency = user.currency.toLowerCase();
      }
      if (user.lang) {
        config.lang = user.lang.toLowerCase();
      }
      if (user.free || user.noCost || user.freeTier) {
        config.free = true;
        config.noCost = true;
      }
      if (user.rates && typeof user.rates === 'object') {
        config.customRates = user.rates;
        for (const [currKey, rate] of Object.entries(user.rates)) {
          const cKey = currKey.toLowerCase();
          if (CURRENCIES[cKey] && typeof rate === 'number') {
            CURRENCIES[cKey].rate = rate;
          }
        }
      }
      if (user.pricing && typeof user.pricing === 'object') {
        mergePricingDict(user.pricing, config.customPricing);
      }
      if (user.customPricing && typeof user.customPricing === 'object') {
        mergePricingDict(user.customPricing, config.customPricing);
      }
      if (user.models && typeof user.models === 'object') {
        mergePricingDict(user.models, config.customPricing);
      }
      if (user.quota && typeof user.quota === 'object') {
        config.quota.limit5h = user.quota.limit5h || DEFAULT_QUOTA_5H;
        config.quota.limit7d = user.quota.limit7d || DEFAULT_QUOTA_7D;
      }
    }
  } catch (_err) {
    // Ignore invalid user config file
  }

  return config;
}

// Perform initial user config and pricing merge on load
loadUserConfig();
_buildSortedAliases();

/**
 * Calculates token cost in USD based on input, cached input, and output tokens.
 * @param {number} inputTokens - Fresh (uncached) input tokens.
 * @param {number} cachedTokens - Cached input prompt tokens.
 * @param {number} outputTokens - Model generation output tokens.
 * @param {string} [modelName] - Model identifier.
 * @returns {number} Cost in USD.
 */
function calculateCostUsd(inputTokens, cachedTokens, outputTokens, modelName) {
  const pricing = getModelPricing(modelName);
  const inputCost = (Math.max(0, inputTokens) / 1000000) * pricing.inputPerMillion;
  const cachedCost = (Math.max(0, cachedTokens) / 1000000) * pricing.cachedInputPerMillion;
  const outputCost = (Math.max(0, outputTokens) / 1000000) * pricing.outputPerMillion;
  return inputCost + cachedCost + outputCost;
}

/**
 * Calculates theoretical savings achieved through prompt caching.
 * @param {number} cachedTokens - Cached prompt tokens.
 * @param {string} [modelName] - Model identifier.
 * @returns {number} Dollar amount saved in USD.
 */
function calculateCacheSavingsUsd(cachedTokens, modelName) {
  if (!cachedTokens || cachedTokens <= 0) return 0;
  const pricing = getModelPricing(modelName);
  const regularInputCost = (cachedTokens / 1000000) * pricing.inputPerMillion;
  const cachedInputCost = (cachedTokens / 1000000) * pricing.cachedInputPerMillion;
  return Math.max(0, regularInputCost - cachedInputCost);
}

/**
 * Converts USD amount to a target currency.
 * @param {number} usdAmount - Amount in USD.
 * @param {string} currencyCode - Target currency code ('usd', 'krw', 'jpy', 'eur', 'gbp').
 * @returns {number} Converted amount.
 */
function convertCurrency(usdAmount, currencyCode = 'usd') {
  const key = (currencyCode || 'usd').toLowerCase();
  const info = CURRENCIES[key] || CURRENCIES.usd;
  return usdAmount * info.rate;
}

module.exports = {
  HOME_DIR,
  GEMINI_DIR,
  ANTIGRAVITY_DIR,
  BRAIN_DIR,
  HISTORY_FILE,
  SETTINGS_FILE,
  CACHE_FILE,
  USER_CONFIG_FILE,
  USER_PRICING_FILE,
  ALT_PRICING_FILE,
  REMOTE_PRICING_CACHE_FILE,
  GEMINI_QUOTA_CACHE_FILE,
  GEMINI_QUOTA_CACHE_TTL_MS,
  BUNDLED_PRICING_FILE,
  DASHBOARD_DIR,
  DASHBOARD_HTML_FILE,
  DASHBOARD_DATA_JS,
  DASHBOARD_DATA_JSON,
  DASHBOARD_SERVER_PORT_FILE,
  DASHBOARD_DEFAULT_PORT,
  DASHBOARD_WRITE_THROTTLE_MS,
  DEFAULT_QUOTA_5H,
  DEFAULT_QUOTA_7D,
  MODEL_PRICING,
  CURRENCIES,
  FLASH_PATTERN,
  PRO_PATTERN,
  FREE_PATTERN,
  formatModelDisplayName,
  smartHeuristicPricing,
  getActiveModelFromSettings,
  getBaseModelName,
  getModelPricing,
  mergePricingDict,
  loadUserConfig,
  loadSettings: loadUserConfig,
  calculateCostUsd,
  calculateCacheSavingsUsd,
  convertCurrency
};
