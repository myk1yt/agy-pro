/**
 * @fileoverview Input validation, masking, and gateway variable formatters for agy-pro.
 * Enforces URL transport rules (HTTPS or loopback HTTP), wire protocol constraints,
 * comma-separated non-JSON AGY_LLM_GATEWAY_MODELS formatting, and secret masking.
 */

'use strict';

const net = require('net');
const { KNOWN_PROVIDERS } = require('./presets');

const WIRE_PROTOCOLS = Object.freeze(['genai', 'openai']);

const OPENAI_TOOL_WARNING =
  'Note: native `agy` strips tool declarations when AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai is used directly without a Gemini-wire bridge; use `genai` for Gemini/Vertex gateways with native tools.';

const XOR_MODELS_WARNING =
  'Note: Setting AGY_LLM_GATEWAY_MODELS replaces the entire built-in /model catalog with "Enterprise Gateway Models" (XOR). Run `agy-pro gateway off` (and open a new terminal) to restore native Antigravity models.';

const CREDENTIAL_SHAPE_PATTERNS = [
  /^(?:AKIA|ASIA)[0-9A-Z]{16}$/,
  /^gh[pousr]_[A-Za-z0-9]{16,}$/,
  /^github_pat_[A-Za-z0-9_]{20,}$/,
  /^xox[baprs]-[A-Za-z0-9-]{10,}$/,
  /^AIza[0-9A-Za-z_-]{35}$/,
  /^ya29\.[0-9A-Za-z_-]{20,}$/,
  /^sk-ant-[A-Za-z0-9_-]{10,}$/,
  /^glpat-[A-Za-z0-9_-]{16,}$/,
  /^npm_[A-Za-z0-9]{30,}$/,
  /^dckr_pat-[A-Za-z0-9_-]{20,}$/,
  /^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\./
];

const SECRET_PREFIXES = ['sk-', 'key-', 'gsk_', 'nvapi-', 'live_', 'secret_', 'bearer '];
const ENV_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;
const CLEAN_UPPER_ENV_RE = /^[A-Z][A-Z0-9_]*_[A-Z0-9_]+$/;
const PROFILE_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const CONTROL_CHAR_RE = /[\x00-\x1f\x7f-\x9f\u200b-\u200f\u202a-\u202e\u2066-\u2069\ufeff]/;

/**
 * Strips leading BOM and control/format runes from an untrusted string.
 * @param {string} val
 * @returns {string}
 */
function stripControlAndFormat(val) {
  return String(val || '').replace(
    /[\x00-\x1f\x7f-\x9f\u200b-\u200f\u202a-\u202e\u2066-\u2069\ufeff]/g,
    ''
  );
}

/**
 * Returns true if the URL or URL instance targets a loopback host
 * (`localhost`, `*.localhost`, `127.0.0.0/8`, or `::1`).
 * @param {string|URL} rawOrUrl
 * @returns {boolean}
 */
function isLoopbackURL(rawOrUrl) {
  let host = '';
  if (rawOrUrl instanceof URL) {
    host = rawOrUrl.hostname;
  } else {
    const raw = String(rawOrUrl || '').trim();
    if (!raw) return false;
    try {
      host = new URL(raw).hostname;
    } catch (_) {
      host = raw;
    }
  }

  const h = host.trim().toLowerCase().replace(/^\[|\]$/g, '');
  if (!h) return false;
  if (h === 'localhost' || h.endsWith('.localhost')) {
    return true;
  }
  if (h === '::1' || h === '0:0:0:0:0:0:0:1') {
    return true;
  }
  if (net.isIPv4(h)) {
    return h.startsWith('127.');
  }
  if (net.isIPv6(h)) {
    // Handle IPv4-mapped IPv6 loopback (both dotted ::ffff:127.0.0.1 and WHATWG hex ::ffff:7f00:1)
    if (h.startsWith('::ffff:')) {
      const mapped = h.slice('::ffff:'.length);
      if (net.isIPv4(mapped) && mapped.startsWith('127.')) {
        return true;
      }
    }
    if (/^(?:0:0:0:0:0:ffff:|::ffff:)7f[0-9a-f]{2}:[0-9a-f]{1,4}$/.test(h)) {
      return true;
    }
  }
  return false;
}

/**
 * Validates an enterprise/provider gateway Base URL.
 * Requires `https://`, or `http://` strictly for loopback hosts, and rejects userinfo.
 * Never echoes raw invalid URLs in error messages in case a secret was pasted.
 * @param {string} rawUrl
 * @returns {{ valid: boolean, url?: string, isLoopback?: boolean, parsed?: URL, error?: string }}
 */
function validateGatewayURL(rawUrl) {
  const trimmed = String(rawUrl || '').replace(/^\ufeff/, '').trim();
  if (!trimmed) {
    return { valid: false, error: 'Base URL is required' };
  }
  if (CONTROL_CHAR_RE.test(trimmed)) {
    return { valid: false, error: 'Base URL contains invalid control characters' };
  }

  let parsed;
  try {
    parsed = new URL(trimmed);
  } catch (_) {
    return { valid: false, error: 'Base URL is not a valid URL (value hidden)' };
  }

  if (parsed.username || parsed.password) {
    return {
      valid: false,
      error: 'Base URL must not contain embedded credentials (user:pass@host)'
    };
  }

  if (!parsed.hostname) {
    return { valid: false, error: 'Base URL must include a hostname' };
  }

  const proto = parsed.protocol.toLowerCase();
  const loopback = isLoopbackURL(parsed);

  if (proto === 'https:') {
    return { valid: true, url: trimmed, isLoopback: loopback, parsed };
  }
  if (proto === 'http:') {
    if (loopback) {
      return { valid: true, url: trimmed, isLoopback: true, parsed };
    }
    return {
      valid: false,
      error: 'Remote Base URL must use https:// (http:// is only allowed for loopback hosts: localhost, 127.0.0.0/8, ::1)'
    };
  }
  return {
    valid: false,
    error: 'Base URL must start with https:// or http:// (loopback only)'
  };
}

/**
 * Validates AGY_LLM_GATEWAY_WIRE_PROTOCOL (`genai` or `openai`).
 * @param {string} wire
 * @returns {{ valid: boolean, value?: 'genai'|'openai', error?: string }}
 */
function validateWireProtocol(wire) {
  const normalized = String(wire || '').trim().toLowerCase();
  if (WIRE_PROTOCOLS.includes(normalized)) {
    return { valid: true, value: normalized };
  }
  return {
    valid: false,
    error: 'Wire protocol must be "genai" or "openai"'
  };
}

/**
 * Returns true if the value resembles an API key / secret token rather than an env variable name.
 * @param {string} val
 * @returns {boolean}
 */
function looksLikeAPIKey(val) {
  const s = String(val || '').trim();
  if (!s) return false;

  for (const re of CREDENTIAL_SHAPE_PATTERNS) {
    if (re.test(s)) return true;
  }

  const lower = s.toLowerCase();
  for (const prefix of SECRET_PREFIXES) {
    if (lower.startsWith(prefix)) return true;
  }

  if (s.includes('-') || s.includes('.')) {
    return true;
  }

  if (s.length >= 24) {
    return true;
  }

  return false;
}

/**
 * Checks whether an env name is in the known built-in provider catalog.
 * @param {string} envName
 * @returns {boolean}
 */
function isKnownKeyEnvName(envName) {
  return KNOWN_PROVIDERS.some((p) => p.apiKeyEnv && p.apiKeyEnv === envName);
}

/**
 * Validates that a value is a safe environment variable NAME (never a secret value).
 * @param {string} envName
 * @returns {{ valid: boolean, value?: string, error?: string }}
 */
function validateEnvName(envName) {
  const trimmed = String(envName || '').replace(/\ufeff/g, '').trim();
  if (!trimmed) {
    return { valid: false, error: 'apiKeyEnv is empty' };
  }
  if (CONTROL_CHAR_RE.test(trimmed)) {
    return { valid: false, error: 'apiKeyEnv contains invalid control characters' };
  }
  if (!ENV_NAME_RE.test(trimmed)) {
    return {
      valid: false,
      error: 'apiKeyEnv must be an environment variable NAME, not a key (value hidden)'
    };
  }
  for (const re of CREDENTIAL_SHAPE_PATTERNS) {
    if (re.test(trimmed)) {
      return {
        valid: false,
        error: 'apiKeyEnv must be an environment variable NAME, not a key value (value hidden)'
      };
    }
  }
  const lower = trimmed.toLowerCase();
  if (
    SECRET_PREFIXES.some((p) => lower.startsWith(p.trim())) ||
    lower.startsWith('sk_live_') ||
    lower.startsWith('pk_live_') ||
    lower.startsWith('tok_')
  ) {
    return {
      valid: false,
      error: 'apiKeyEnv must be an environment variable NAME, not a key value (value hidden)'
    };
  }

  if (Object.prototype.hasOwnProperty.call(process.env, trimmed)) {
    return { valid: true, value: trimmed };
  }
  if (isKnownKeyEnvName(trimmed)) {
    return { valid: true, value: trimmed };
  }
  if (
    /^AGY_PRO_GATEWAY_[A-Z0-9_]+$/.test(trimmed) ||
    /^[A-Z][A-Z0-9_]*_(?:API_)?KEY$/.test(trimmed)
  ) {
    return { valid: true, value: trimmed };
  }
  if (trimmed.length < 24 && CLEAN_UPPER_ENV_RE.test(trimmed)) {
    return { valid: true, value: trimmed };
  }
  if (looksLikeAPIKey(trimmed)) {
    return {
      valid: false,
      error: 'apiKeyEnv must be an environment variable NAME, not a key value (value hidden)'
    };
  }
  return { valid: true, value: trimmed };
}

/**
 * Validates a profile name for shell/file safety.
 * Allows valid hyphenated/dotted names (e.g., `local-ollama`, `my.profile`, `work_gateway-1`)
 * while rejecting actual secret/credential material.
 * @param {string} name
 * @returns {{ valid: boolean, value?: string, error?: string }}
 */
function validateProfileName(name) {
  const trimmed = String(name || '').replace(/^\ufeff/, '').trim();
  if (!trimmed) {
    return { valid: false, error: 'Profile name is required' };
  }
  if (!PROFILE_NAME_RE.test(trimmed)) {
    return {
      valid: false,
      error: 'Profile name must be 1-64 chars, start with alphanumeric, and contain only letters, digits, ".", "_", "-"'
    };
  }
  const lower = trimmed.toLowerCase();
  const matchesCredentialShape = CREDENTIAL_SHAPE_PATTERNS.some((re) => re.test(trimmed));
  const matchesSecretPrefix =
    SECRET_PREFIXES.some((p) => lower.startsWith(p.trim())) ||
    lower.startsWith('sk_live_') ||
    lower.startsWith('pk_live_') ||
    lower.startsWith('tok_');
  const looksOpaqueSecret =
    trimmed.length >= 32 &&
    !/[-.\s]/.test(trimmed) &&
    /[A-Z]/.test(trimmed) &&
    /[a-z]/.test(trimmed) &&
    /[0-9]/.test(trimmed);

  if (matchesCredentialShape || matchesSecretPrefix || looksOpaqueSecret) {
    return {
      valid: false,
      error: 'Profile name looks like secret material and cannot be saved (value hidden)'
    };
  }
  return { valid: true, value: trimmed };
}

/**
 * Parses AGY_LLM_GATEWAY_MODELS input (comma-separated IDs or string array).
 * Rejects JSON syntax (`[` or `{`).
 * @param {string|Array<string>} input
 * @returns {Array<string>}
 */
function parseGatewayModels(input) {
  if (input === null || input === undefined) {
    return [];
  }

  let rawTokens;
  if (Array.isArray(input)) {
    rawTokens = input.map((item) => String(item === null || item === undefined ? '' : item));
  } else if (typeof input === 'string') {
    const trimmed = input.replace(/^\ufeff/, '').trim();
    if (!trimmed) return [];
    if (trimmed.startsWith('[') || trimmed.startsWith('{')) {
      throw new Error(
        'AGY_LLM_GATEWAY_MODELS must be comma-separated model IDs, not JSON (do not start with "[" or "{")'
      );
    }
    rawTokens = trimmed.split(',');
  } else {
    throw new Error('AGY_LLM_GATEWAY_MODELS must be a comma-separated string or array of model IDs');
  }

  const seen = new Set();
  const out = [];
  for (const rawTok of rawTokens) {
    const tok = stripControlAndFormat(rawTok).trim();
    if (!tok) continue;
    if (tok.startsWith('[') || tok.startsWith('{')) {
      throw new Error(
        'AGY_LLM_GATEWAY_MODELS must be comma-separated model IDs, not JSON (do not use "[" or "{")'
      );
    }
    if (!seen.has(tok)) {
      seen.add(tok);
      out.push(tok);
    }
  }
  return out;
}

/**
 * Formats model IDs into the exact comma-separated `AGY_LLM_GATEWAY_MODELS` string.
 * Rejects JSON (`[`/`{`).
 * @param {string|Array<string>} models
 * @param {{ allowEmpty?: boolean }} [options]
 * @returns {string}
 */
function formatGatewayModels(models, options = {}) {
  const parsed = parseGatewayModels(models);
  if (parsed.length === 0 && options.allowEmpty === false) {
    throw new Error('At least one model ID is required');
  }
  return parsed.join(',');
}

/**
 * Masks a secret for terminal/log display: first 5 Unicode code points + `"****"`,
 * or `"****"` for values of 5 characters or fewer. Never exposes suffix characters.
 * @param {string} secret
 * @returns {string}
 */
function maskSecret(secret) {
  const chars = [...String(secret === null || secret === undefined ? '' : secret).trim()];
  if (chars.length <= 5) {
    return '****';
  }
  return `${chars.slice(0, 5).join('')}****`;
}

/**
 * Masks embedded userinfo, credential-like query parameters, or fragments in a Base URL
 * while keeping benign `https://host/v1` URLs readable.
 * @param {string} rawUrl
 * @returns {string}
 */
function maskBaseURL(rawUrl) {
  const trimmed = String(rawUrl || '').trim();
  if (!trimmed) return '';

  try {
    const u = new URL(trimmed);
    if (!/^https?:$/i.test(u.protocol)) {
      return looksLikeAPIKey(trimmed) ? maskSecret(trimmed) : trimmed;
    }
    let changed = false;
    if (u.username || u.password) {
      u.username = '';
      u.password = '';
      changed = true;
    }
    for (const [k, v] of [...u.searchParams.entries()]) {
      if (looksLikeAPIKey(v) || /^(api[_-]?key|token|secret|key)$/i.test(k)) {
        u.searchParams.set(k, maskSecret(v));
        changed = true;
      }
    }
    if (u.hash && looksLikeAPIKey(u.hash.slice(1))) {
      u.hash = `#${maskSecret(u.hash.slice(1))}`;
      changed = true;
    }
    return changed ? u.toString() : trimmed;
  } catch (_) {
    if (trimmed.includes('@') || looksLikeAPIKey(trimmed)) {
      return maskSecret(trimmed);
    }
    return trimmed;
  }
}

/**
 * Masks header values in `AGY_LLM_GATEWAY_HEADERS` (`KEY: VALUE` comma/newline pairs)
 * while keeping header key names readable.
 * @param {string|Record<string, string>} rawHeaders
 * @returns {string}
 */
function maskHeaders(rawHeaders) {
  if (!rawHeaders) return '';
  if (typeof rawHeaders === 'object' && !Array.isArray(rawHeaders)) {
    const parts = [];
    for (const [k, v] of Object.entries(rawHeaders)) {
      const key = String(k || '').trim();
      if (!key) continue;
      parts.push(`${key}: ${maskSecret(String(v || '').trim())}`);
    }
    return parts.join(', ');
  }

  const trimmed = String(rawHeaders).trim();
  if (!trimmed) return '';
  const pairs = trimmed.split(/[\r\n,]+/).map((p) => p.trim()).filter(Boolean);
  const masked = [];
  for (const pair of pairs) {
    const idx = pair.indexOf(':');
    if (idx <= 0) {
      masked.push(maskSecret(pair));
      continue;
    }
    const key = pair.slice(0, idx).trim();
    const val = pair.slice(idx + 1).trim();
    masked.push(`${key}: ${maskSecret(val)}`);
  }
  return masked.join(', ');
}

/**
 * Redacts known secret values and common credential token patterns from diagnostic text.
 * @param {string} text
 * @param {string} [knownSecret]
 * @returns {string}
 */
function redactSecrets(text, knownSecret = '') {
  let out = String(text || '');
  if (knownSecret && knownSecret.length >= 4) {
    out = out.split(knownSecret).join('[REDACTED]');
  }
  out = out.replace(/(bearer\s+)[A-Za-z0-9._~+/=-]{8,}/gi, '$1[REDACTED]');
  out = out.replace(/\b(?:sk|pk|key|tok)-[A-Za-z0-9_-]{12,}/g, '[REDACTED]');
  out = out.replace(/AIza[0-9A-Za-z_-]{35}/g, '[REDACTED]');
  out = out.replace(/\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/g, '[REDACTED]');
  return out;
}

module.exports = {
  WIRE_PROTOCOLS,
  OPENAI_TOOL_WARNING,
  XOR_MODELS_WARNING,
  stripControlAndFormat,
  isLoopbackURL,
  validateGatewayURL,
  validateWireProtocol,
  looksLikeAPIKey,
  validateEnvName,
  validateProfileName,
  parseGatewayModels,
  formatGatewayModels,
  maskSecret,
  maskBaseURL,
  maskHeaders,
  redactSecrets
};
