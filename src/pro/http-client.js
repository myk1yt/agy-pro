/**
 * @fileoverview Zero-dependency HTTP/HTTPS client for model discovery (`GET /models`)
 * and live endpoint probing (`POST /chat/completions` for openai, `:generateContent` for genai).
 * Supports optional authentication headers for loopback servers and custom transport injection for tests.
 */

'use strict';

const fs = require('fs');
const http = require('http');
const https = require('https');
const {
  stripControlAndFormat,
  validateGatewayURL,
  validateWireProtocol,
  maskBaseURL,
  redactSecrets
} = require('./validator');

const DEFAULT_MODELS_TIMEOUT_MS = 3000;
const DEFAULT_PROBE_TIMEOUT_MS = 20000;
const MAX_SANE_TOKENS = 10000000;
const MAX_ERROR_SNIPPET_BYTES = 512;

const CONTEXT_CEILING_ALIASES = [
  'context_window',
  'max_model_len',
  'context_length',
  'context_size',
  'max_context_tokens',
  'max_input_tokens',
  'inputTokenLimit'
];

const OUTPUT_CEILING_ALIASES = [
  'max_output_tokens',
  'max_tokens',
  'max_completion_tokens',
  'outputTokenLimit'
];

/**
 * Parses `KEY: VALUE` pairs separated by commas or newlines into a plain header object.
 * @param {string|Record<string, string>|undefined|null} rawHeaders
 * @returns {Record<string, string>}
 */
function parseCustomHeaders(rawHeaders) {
  if (!rawHeaders) return {};
  if (typeof rawHeaders === 'object' && !Array.isArray(rawHeaders)) {
    const out = {};
    for (const [k, v] of Object.entries(rawHeaders)) {
      const cleanK = stripControlAndFormat(k).trim();
      const cleanV = stripControlAndFormat(v).trim();
      if (cleanK && cleanV) out[cleanK] = cleanV;
    }
    return out;
  }

  const out = {};
  const parts = String(rawHeaders).split(/[\r\n,]+/);
  for (const part of parts) {
    const idx = part.indexOf(':');
    if (idx <= 0) continue;
    const key = stripControlAndFormat(part.slice(0, idx)).trim();
    const val = stripControlAndFormat(part.slice(idx + 1)).trim();
    if (key && val) {
      out[key] = val;
    }
  }
  return out;
}

/**
 * Extracts sanitized hostname from a URL for error messages.
 * @param {string} rawUrl
 * @returns {string}
 */
function extractHost(rawUrl) {
  try {
    const u = new URL(String(rawUrl || '').trim());
    return u.host || 'gateway';
  } catch (_) {
    return 'gateway';
  }
}

/**
 * Coerces a candidate token ceiling field to a positive integer <= MAX_SANE_TOKENS, or 0.
 * @param {*} value
 * @returns {number}
 */
function saneTokenCount(value) {
  let n;
  if (typeof value === 'number') {
    n = value;
  } else if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return 0;
    n = Number(trimmed);
  } else {
    return 0;
  }
  if (!Number.isInteger(n) || n <= 0 || n > MAX_SANE_TOKENS) {
    return 0;
  }
  return n;
}

/**
 * Returns the first sane ceiling value from a model metadata object.
 * @param {Record<string, *>} obj
 * @param {string[]} aliases
 * @returns {number}
 */
function firstCeiling(obj, aliases) {
  for (const key of aliases) {
    if (Object.prototype.hasOwnProperty.call(obj, key)) {
      const val = saneTokenCount(obj[key]);
      if (val > 0) return val;
    }
  }
  return 0;
}

/**
 * Builds authentication headers for a gateway request.
 * Omits auth headers completely when `apiKey` is empty (loopback servers).
 * @param {URL} parsedUrl
 * @param {string} apiKey
 * @param {string} [wireProtocol]
 * @returns {Record<string, string>}
 */
function buildAuthHeaders(parsedUrl, apiKey, wireProtocol) {
  const key = String(apiKey || '').trim();
  if (!key) return {};

  const isGoogleNative =
    parsedUrl.hostname === 'generativelanguage.googleapis.com' &&
    !parsedUrl.pathname.includes('/openai') &&
    (wireProtocol === 'genai' || !wireProtocol);

  if (isGoogleNative) {
    return { 'x-goog-api-key': key };
  }
  return { Authorization: `Bearer ${key}` };
}

/**
 * Performs a single HTTP/HTTPS request using Node built-in modules.
 * @param {{ url: string, method?: string, headers?: Record<string, string>, body?: string, timeoutMs?: number, caCert?: string }} params
 * @returns {Promise<{ statusCode: number, statusMessage: string, headers: object, body: string }>}
 */
function defaultTransport(params) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(params.url);
    const isHttps = parsed.protocol === 'https:';
    const mod = isHttps ? https : http;
    const timeoutMs = params.timeoutMs || DEFAULT_MODELS_TIMEOUT_MS;

    const reqOptions = {
      protocol: parsed.protocol,
      hostname: parsed.hostname,
      port: parsed.port || (isHttps ? 443 : 80),
      path: `${parsed.pathname}${parsed.search}`,
      method: params.method || 'GET',
      headers: { ...(params.headers || {}) },
      timeout: timeoutMs
    };

    if (params.body !== undefined && params.body !== null) {
      reqOptions.headers['Content-Length'] = Buffer.byteLength(params.body, 'utf8');
    }

    if (isHttps && params.caCert) {
      try {
        reqOptions.ca = fs.readFileSync(params.caCert);
      } catch (err) {
        reject(new Error(`Failed to read CA certificate (${params.caCert}): ${err.message}`));
        return;
      }
    }

    let settled = false;
    const finish = (err, result) => {
      if (settled) return;
      settled = true;
      if (err) reject(err);
      else resolve(result);
    };

    const req = mod.request(reqOptions, (res) => {
      const chunks = [];
      let totalBytes = 0;
      res.on('data', (chunk) => {
        totalBytes += chunk.length;
        if (totalBytes <= 2 * 1024 * 1024) {
          chunks.push(chunk);
        }
      });
      res.on('end', () => {
        finish(null, {
          statusCode: res.statusCode || 0,
          statusMessage: res.statusMessage || '',
          headers: res.headers || {},
          body: Buffer.concat(chunks).toString('utf8')
        });
      });
      res.on('error', (err) => finish(err));
    });

    req.on('timeout', () => {
      const timeoutErr = new Error(`Request timed out after ${timeoutMs}ms`);
      timeoutErr.code = 'ETIMEDOUT';
      timeoutErr.isTimeout = true;
      req.destroy(timeoutErr);
    });

    req.on('error', (err) => finish(err));

    if (params.body !== undefined && params.body !== null) {
      req.write(params.body, 'utf8');
    }
    req.end();
  });
}

/**
 * Fetches available model IDs from `<baseURL>/models`.
 * Supports OpenAI `{ data: [{ id }] }` and Gemini `{ models: [{ name }] }` payloads.
 * @param {string} baseURL
 * @param {string} [apiKey]
 * @param {{ timeoutMs?: number, wireProtocol?: string, caCert?: string, headers?: string|Record<string, string>, transport?: Function }} [options]
 * @returns {Promise<Array<{ id: string, contextLength: number, maxOutputTokens: number }>>}
 */
async function fetchRemoteModels(baseURL, apiKey = '', options = {}) {
  const urlCheck = validateGatewayURL(baseURL);
  if (!urlCheck.valid) {
    const err = new Error(urlCheck.error);
    err.code = 'ERR_MODELS_REQUEST';
    throw err;
  }

  const parsed = urlCheck.parsed;
  const cleanBase = urlCheck.url.replace(/\/+$/, '');
  const endpoint = cleanBase.endsWith('/models') ? cleanBase : `${cleanBase}/models`;
  const host = extractHost(urlCheck.url);
  const timeoutMs = options.timeoutMs || DEFAULT_MODELS_TIMEOUT_MS;
  const transport = options.transport || defaultTransport;

  const headers = {
    Accept: 'application/json',
    ...parseCustomHeaders(options.headers),
    ...buildAuthHeaders(parsed, apiKey, options.wireProtocol)
  };

  let res;
  try {
    res = await transport({
      url: endpoint,
      method: 'GET',
      headers,
      timeoutMs,
      caCert: options.caCert || ''
    });
  } catch (err) {
    const wrapped = new Error(
      `models fetch: endpoint unreachable (${host}): ${redactSecrets(err.message, apiKey)}`
    );
    wrapped.code = err.isTimeout || err.code === 'ETIMEDOUT' ? 'ERR_MODELS_TIMEOUT' : 'ERR_MODELS_CONNECT';
    wrapped.host = host;
    wrapped.isTimeout = Boolean(err.isTimeout || err.code === 'ETIMEDOUT');
    throw wrapped;
  }

  if (res.statusCode < 200 || res.statusCode >= 300) {
    const err = new Error(`models fetch: unexpected HTTP ${res.statusCode} from ${host}`);
    err.code = 'ERR_MODELS_STATUS';
    err.statusCode = res.statusCode;
    err.host = host;
    throw err;
  }

  let payload;
  try {
    payload = JSON.parse(res.body);
  } catch (_) {
    const err = new Error(`models fetch: response from ${host} is not valid JSON`);
    err.code = 'ERR_MODELS_DECODE';
    err.host = host;
    throw err;
  }

  const rawList = Array.isArray(payload && payload.data)
    ? payload.data
    : Array.isArray(payload && payload.models)
      ? payload.models
      : Array.isArray(payload)
        ? payload
        : null;

  if (!rawList) {
    const err = new Error(`models fetch: response from ${host} lacks a models array`);
    err.code = 'ERR_MODELS_DECODE';
    err.host = host;
    throw err;
  }

  const seen = new Set();
  const models = [];
  for (const item of rawList) {
    if (!item || typeof item !== 'object') continue;
    let rawId = typeof item.id === 'string' ? item.id : '';
    if (!rawId && typeof item.name === 'string') {
      rawId = item.name.replace(/^models\//, '');
    }
    const cleanId = stripControlAndFormat(rawId).trim();
    if (!cleanId || seen.has(cleanId)) continue;
    seen.add(cleanId);
    models.push({
      id: cleanId,
      contextLength: firstCeiling(item, CONTEXT_CEILING_ALIASES),
      maxOutputTokens: firstCeiling(item, OUTPUT_CEILING_ALIASES)
    });
  }

  return models;
}

/**
 * Resolves the probe target URL and JSON request payload for the requested wire protocol.
 * @param {string} baseURL
 * @param {'openai'|'genai'} wireProtocol
 * @param {string} model
 * @returns {{ endpoint: string, body: string }}
 */
function buildProbeRequest(baseURL, wireProtocol, model) {
  const cleanBase = baseURL.replace(/\/+$/, '');
  if (wireProtocol === 'genai') {
    const encodedModel = encodeURIComponent(model);
    const hasVersionSuffix = /\/v1(?:beta)?$/i.test(cleanBase);
    const endpoint = hasVersionSuffix
      ? `${cleanBase}/models/${encodedModel}:generateContent`
      : `${cleanBase}/v1beta/models/${encodedModel}:generateContent`;
    const body = JSON.stringify({
      contents: [{ role: 'user', parts: [{ text: 'ping' }] }],
      generationConfig: { maxOutputTokens: 5 }
    });
    return { endpoint, body };
  }

  const endpoint = cleanBase.endsWith('/chat/completions')
    ? cleanBase
    : `${cleanBase}/chat/completions`;
  const body = JSON.stringify({
    model,
    messages: [{ role: 'user', content: 'ping' }],
    max_tokens: 5
  });
  return { endpoint, body };
}

/**
 * Probes a gateway endpoint with a minimal completion/generateContent request.
 * Never returns raw API keys or credential-shaped substrings in error details.
 * @param {{ url: string, apiKey?: string, wireProtocol?: string, model: string, timeoutMs?: number, caCert?: string, headers?: string|Record<string, string>, transport?: Function }} options
 * @returns {Promise<{ ok: boolean, status: number, latencyMs: number, model: string, wireProtocol: string, isTimeout?: boolean, error?: string }>}
 */
async function probeEndpoint(options = {}) {
  const urlCheck = validateGatewayURL(options.url);
  if (!urlCheck.valid) {
    return {
      ok: false,
      status: 0,
      latencyMs: 0,
      model: String(options.model || ''),
      wireProtocol: String(options.wireProtocol || 'openai'),
      isTimeout: false,
      error: urlCheck.error
    };
  }

  const wireCheck = validateWireProtocol(options.wireProtocol || 'openai');
  if (!wireCheck.valid) {
    return {
      ok: false,
      status: 0,
      latencyMs: 0,
      model: String(options.model || ''),
      wireProtocol: String(options.wireProtocol || ''),
      isTimeout: false,
      error: wireCheck.error
    };
  }

  const model = stripControlAndFormat(options.model || '').trim();
  if (!model) {
    return {
      ok: false,
      status: 0,
      latencyMs: 0,
      model: '',
      wireProtocol: wireCheck.value,
      isTimeout: false,
      error: 'Model ID is required for endpoint probe'
    };
  }

  const apiKey = String(options.apiKey || '').trim();
  const timeoutMs = options.timeoutMs || DEFAULT_PROBE_TIMEOUT_MS;
  const transport = options.transport || defaultTransport;
  const host = extractHost(urlCheck.url);
  const { endpoint, body } = buildProbeRequest(urlCheck.url, wireCheck.value, model);

  const headers = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
    ...parseCustomHeaders(options.headers),
    ...buildAuthHeaders(urlCheck.parsed, apiKey, wireCheck.value)
  };

  const startedAt = Date.now();
  let res;
  try {
    res = await transport({
      url: endpoint,
      method: 'POST',
      headers,
      body,
      timeoutMs,
      caCert: options.caCert || ''
    });
  } catch (err) {
    const latencyMs = Date.now() - startedAt;
    const isTimeout = Boolean(
      err.isTimeout ||
        err.code === 'ETIMEDOUT' ||
        /timeout|timed out/i.test(String(err.message || ''))
    );
    const safeMsg = redactSecrets(err.message || 'Network error', apiKey);
    return {
      ok: false,
      status: 0,
      latencyMs,
      model,
      wireProtocol: wireCheck.value,
      isTimeout,
      error: `Network error contacting ${host} (${maskBaseURL(urlCheck.url)}): ${safeMsg}`
    };
  }

  const latencyMs = Date.now() - startedAt;
  if (res.statusCode < 200 || res.statusCode >= 300) {
    const rawSnippet = String(res.body || '').slice(0, MAX_ERROR_SNIPPET_BYTES).trim();
    const safeSnippet = redactSecrets(rawSnippet, apiKey);
    const detail = safeSnippet ? `: ${safeSnippet}` : '';
    return {
      ok: false,
      status: res.statusCode,
      latencyMs,
      model,
      wireProtocol: wireCheck.value,
      isTimeout: false,
      error: `HTTP ${res.statusCode} from ${host}${detail}`
    };
  }

  return {
    ok: true,
    status: res.statusCode,
    latencyMs,
    model,
    wireProtocol: wireCheck.value
  };
}

module.exports = {
  DEFAULT_MODELS_TIMEOUT_MS,
  DEFAULT_PROBE_TIMEOUT_MS,
  parseCustomHeaders,
  fetchRemoteModels,
  probeEndpoint,
  buildProbeRequest
};
