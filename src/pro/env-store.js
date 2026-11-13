/**
 * @fileoverview Multi-profile gateway state (`~/.agy-pro/gateway.json` v2) and
 * secret-safe OS environment persistence (Windows stdin-PowerShell HKCU\Environment
 * with zero argv leaks + BOM cleanup; POSIX `gateway.env` 0o600 + shell rc block).
 */

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const { spawnSync } = require('child_process');
const {
  isLoopbackURL,
  validateGatewayURL,
  validateWireProtocol,
  validateEnvName,
  validateProfileName,
  parseGatewayModels,
  formatGatewayModels,
  maskSecret,
  stripControlAndFormat
} = require('./validator');
const { defaultEnvForProvider } = require('./presets');

/**
 * Credential shapes that must never be echoed, even when they land in a profile/provider/model
 * field of gateway.json. Narrower than `looksLikeAPIKey`, which flags any hyphenated or dotted
 * string and would mask legitimate names such as `free-gw` or `my.profile`.
 * @type {RegExp[]}
 */
const CREDENTIAL_MATERIAL_PATTERNS = [
  /^(?:AKIA|ASIA)[0-9A-Z]{16}$/,
  /^gh[pousr]_[A-Za-z0-9]{16,}$/,
  /^github_pat_[A-Za-z0-9_]{20,}$/,
  /^xox[baprs]-[A-Za-z0-9-]{10,}$/,
  /^AIza[0-9A-Za-z_-]{35}$/,
  /^ya29\.[0-9A-Za-z_-]{20,}$/,
  /^sk-ant-[A-Za-z0-9_-]{10,}$/,
  /^sk-[A-Za-z0-9_-]{12,}$/,
  /^gsk_[A-Za-z0-9]{12,}$/,
  /^nvapi-[A-Za-z0-9_-]{12,}$/,
  /^glpat-[A-Za-z0-9_-]{16,}$/,
  /^npm_[A-Za-z0-9]{30,}$/,
  /^dckr_pat-[A-Za-z0-9_-]{20,}$/,
  /^eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\./
];

/**
 * Reports whether a config field holds credential material rather than a name.
 * @param {string} value
 * @returns {boolean}
 */
function looksLikeCredentialMaterial(value) {
  const s = String(value || '').trim();
  if (!s) return false;
  if (CREDENTIAL_MATERIAL_PATTERNS.some((re) => re.test(s))) return true;
  const lower = s.toLowerCase();
  if (['bearer ', 'secret_', 'live_', 'key-'].some((p) => lower.startsWith(p))) return true;
  return s.length >= 32 && !/[-.\s]/.test(s) && /[A-Z]/.test(s) && /[a-z]/.test(s) && /[0-9]/.test(s);
}

/**
 * Renders a config-derived label (profile name, provider name, model id) for terminal output.
 * gateway.json and providers.json are untrusted input: an ANSI escape or control rune in a name
 * would otherwise repaint the hub, forge a prompt, or hide a line. Credential-shaped values are
 * masked with the same idiom as `maskSecret`. This is the canonical label sanitizer: `wizard.js`
 * reuses it so an error hint and a hub line cannot drift apart.
 * @param {*} value
 * @returns {string}
 */
function displayConfigName(value) {
  const clean = stripControlAndFormat(String(value === null || value === undefined ? '' : value).trim());
  if (!clean) return '';
  return looksLikeCredentialMaterial(clean) ? maskSecret(clean) : clean;
}

/**
 * Strips control/format characters from a config-derived name before it is embedded in an
 * error message, so a tampered gateway.json cannot inject terminal escapes through errors.
 * A pasted secret stored as a profile name is additionally masked: error text ends up in
 * terminals and CI logs, where the raw value must never appear.
 * @param {*} value
 * @returns {string}
 */
function safeErrorName(value) {
  return displayConfigName(value).slice(0, 128);
}

const GATEWAY_ENV_KEYS = Object.freeze([
  'AGY_LLM_GATEWAY_URL',
  'AGY_LLM_GATEWAY_API_KEY',
  'AGY_LLM_GATEWAY_WIRE_PROTOCOL',
  'AGY_LLM_GATEWAY_MODELS',
  'AGY_LLM_GATEWAY_CA_CERT',
  'AGY_LLM_GATEWAY_HEADERS'
]);

const RC_BLOCK_START = '# >>> agy-pro gateway >>>';
const RC_BLOCK_END = '# <<< agy-pro gateway <<<';

const ENV_KEY_SAFE_RE = /^[A-Za-z_][A-Za-z0-9_]*$/;

// Namespace of the per-profile API key variables `agy-pro` creates itself
// (`AGY_PRO_GATEWAY_<PROFILE>_API_KEY`). Only these are purgeable on profile deletion.
const OWNED_ENV_PREFIX = 'AGY_PRO_GATEWAY_';

/**
 * Resolves the absolute Windows PowerShell executable path (CWE-426 protection).
 * @returns {string}
 */
function resolvePowershellExe() {
  const sysRoot = process.env.SystemRoot || process.env.WINDIR || 'C:\\Windows';
  return path.join(sysRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
}

/**
 * Static PowerShell script for updating HKCU\Environment via stdin (`-Command -`).
 * Contains zero user values; key-value pairs arrive solely in child process env
 * (`__AGY_ENV_MAP_JSON`) to prevent secrets from appearing in process argv.
 */
const WINDOWS_PERSIST_SCRIPT = [
  "$ErrorActionPreference = 'Stop'",
  "$json = [System.Environment]::GetEnvironmentVariable('__AGY_ENV_MAP_JSON', 'Process')",
  'if (-not $json) { exit 0 }',
  '$map = $json | ConvertFrom-Json',
  '$bom = [string][char]0xFEFF',
  "$userVars = [System.Environment]::GetEnvironmentVariables('User')",
  'foreach ($prop in $map.PSObject.Properties) {',
  '  $k = [string]$prop.Name',
  '  $v = $prop.Value',
  '  foreach ($existingKey in @($userVars.Keys)) {',
  '    if ($existingKey -ne $k -and $existingKey.Replace($bom, "").Trim() -eq $k) {',
  "      [System.Environment]::SetEnvironmentVariable($existingKey, $null, 'User')",
  '    }',
  '  }',
  '  if ($null -eq $v -or [string]$v -eq "") {',
  "    [System.Environment]::SetEnvironmentVariable($k, $null, 'User')",
  '  } else {',
  "    [System.Environment]::SetEnvironmentVariable($k, [string]$v, 'User')",
  '  }',
  '}'
].join('\n');

/**
 * Static PowerShell script for reading HKCU\Environment keys without exposing names in argv.
 * Matches both clean and BOM-prefixed variants of requested variable names.
 */
const WINDOWS_READ_SCRIPT = [
  "$ErrorActionPreference = 'Stop'",
  "$json = [System.Environment]::GetEnvironmentVariable('__AGY_ENV_QUERY_JSON', 'Process')",
  "$userVars = [System.Environment]::GetEnvironmentVariables('User')",
  '$bom = [char]0xFEFF',
  '$out = @{}',
  '$bomVariants = @()',
  '$requested = @()',
  'if ($json) { $requested = @($json | ConvertFrom-Json) }',
  'foreach ($k in $userVars.Keys) {',
  '  $rawKey = [string]$k',
  '  $clean = $rawKey.TrimStart($bom).Replace([string]$bom, "").Trim()',
  '  if ($requested.Count -eq 0 -or ($requested -contains $clean)) {',
  '    if ($rawKey -ne $clean) {',
  '      $bomVariants += $rawKey',
  '    }',
  '    $out[$rawKey] = [string]$userVars[$k]',
  '  }',
  '}',
  '@{ vars = $out; bomVariants = $bomVariants } | ConvertTo-Json -Compress'
].join('\n');

/**
 * Resolves the agy-pro state directory (`AGY_PRO_HOME` or `~/.agy-pro`).
 * Evaluated dynamically so tests can isolate `AGY_PRO_HOME` per case.
 * @returns {string}
 */
function getAgyProHome() {
  if (process.env.AGY_PRO_HOME) {
    return path.resolve(process.env.AGY_PRO_HOME);
  }
  return path.join(os.homedir(), '.agy-pro');
}

/**
 * Resolves the `gateway.json` path.
 * @param {{ configPath?: string }} [options]
 * @returns {string}
 */
function getGatewayConfigPath(options = {}) {
  if (options.configPath) return path.resolve(options.configPath);
  return path.join(getAgyProHome(), 'gateway.json');
}

/**
 * Resolves the POSIX `gateway.env` path.
 * @param {{ envPath?: string }} [options]
 * @returns {string}
 */
function getGatewayEnvPath(options = {}) {
  if (options.envPath) return path.resolve(options.envPath);
  return path.join(getAgyProHome(), 'gateway.env');
}

/**
 * Resolves the sandboxed mock Windows env file path if running under sandbox/test isolation.
 * Returns null when real HKCU\Environment should be used.
 * @param {{ envFile?: string, win32Runner?: Function }} [options]
 * @returns {string|null}
 */
function getMockEnvFilePath(options = {}) {
  if (options.envFile) return path.resolve(options.envFile);
  if (process.env.AGY_PRO_ENV_FILE) return path.resolve(process.env.AGY_PRO_ENV_FILE);
  if (process.env.AGY_PRO_HOME && process.env.AGY_PRO_WIN32_REGISTRY !== '1') {
    return path.join(getAgyProHome(), 'win32-env.json');
  }
  return null;
}

/**
 * Atomically writes text to `filePath` via a sibling temp file + rename with `mode` (default 0o600).
 * @param {string} filePath
 * @param {string} content
 * @param {number} [mode=0o600]
 */
function writeAtomic(filePath, content, mode = 0o600) {
  const dir = path.dirname(filePath);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const tmpName = `.${path.basename(filePath)}.tmp.${process.pid}.${Date.now()}.${Math.random()
    .toString(16)
    .slice(2)}`;
  const tmpPath = path.join(dir, tmpName);
  try {
    fs.writeFileSync(tmpPath, content, { encoding: 'utf8', mode, flag: 'wx' });
    try {
      fs.chmodSync(tmpPath, mode);
    } catch (_) {
      // chmod may be a partial no-op on Windows
    }
    fs.renameSync(tmpPath, filePath);
  } catch (err) {
    try {
      if (fs.existsSync(tmpPath)) fs.unlinkSync(tmpPath);
    } catch (_) {
      // Ignore cleanup failure
    }
    throw err;
  }
}

/**
 * Normalizes a single profile object into the canonical v2 profile shape.
 * Strips any accidental plaintext secret fields.
 * @param {object} raw
 * @returns {object}
 */
function normalizeProfile(raw = {}) {
  let models = [];
  if (raw.models !== undefined) {
    models = parseGatewayModels(raw.models);
  }
  const wireRes = validateWireProtocol(raw.wireProtocol || 'openai');
  const wireProtocol = wireRes.valid ? wireRes.value : 'openai';
  return {
    provider: String(raw.provider || 'custom').trim() || 'custom',
    url: String(raw.url || '').trim(),
    wireProtocol,
    apiKeyEnv: String(raw.apiKeyEnv || '').replace(/\ufeff/g, '').trim(),
    models,
    caCert: String(raw.caCert || '').trim(),
    headers: String(raw.headers || '').trim(),
    updatedAt: String(raw.updatedAt || new Date().toISOString())
  };
}

/**
 * Normalizes raw parsed `gateway.json` (v1 flat object or v2 multi-profile) into v2 shape.
 * @param {object|null|undefined} raw
 * @returns {{ version: 2, activeProfile: string, active: boolean, profiles: Record<string, object> }}
 */
function normalizeGatewayConfig(raw) {
  if (!raw || typeof raw !== 'object') {
    return { version: 2, activeProfile: '', active: false, profiles: {} };
  }

  // Auto-migrate v1 flat object `{ version: 1, provider, url, wireProtocol, apiKeyEnv, models, ... }`
  if (raw.version === 1 || (!raw.profiles && (raw.url || raw.provider))) {
    const profileName = String(raw.profile || raw.provider || 'default').trim() || 'default';
    const profile = normalizeProfile(raw);
    return {
      version: 2,
      activeProfile: profileName,
      active: Boolean(raw.active),
      profiles: profile.url ? { [profileName]: profile } : {}
    };
  }

  const profiles = {};
  if (raw.profiles && typeof raw.profiles === 'object' && !Array.isArray(raw.profiles)) {
    for (const [name, p] of Object.entries(raw.profiles)) {
      if (!p || typeof p !== 'object') continue;
      profiles[name] = normalizeProfile(p);
    }
  }

  let activeProfile = String(raw.activeProfile || '').trim();
  if (activeProfile && !Object.prototype.hasOwnProperty.call(profiles, activeProfile)) {
    const keys = Object.keys(profiles);
    activeProfile = keys.length > 0 ? keys[0] : '';
  }

  return {
    version: 2,
    activeProfile,
    active: Boolean(
      raw.active && activeProfile && Object.prototype.hasOwnProperty.call(profiles, activeProfile)
    ),
    profiles
  };
}

/**
 * Validates a gateway v2 config object before writing to disk.
 * Ensures plaintext API keys are never persisted inside `gateway.json`.
 * `options.allowMissingApiKeyEnv` names the profiles that `sanitizeInvalidApiKeyEnv` reported as
 * repaired during a repair write; only those may skip the "remote URLs need a key name" rule, so a
 * previously saved poisoned profile (invalid name, or an empty name on a remote URL) cannot block
 * `gateway off`/delete/switch.
 * @param {object} config
 * @param {{ allowMissingApiKeyEnv?: string[] }} [options]
 * @returns {object} Validated normalized v2 config
 */
function validateGatewayConfig(config, options = {}) {
  const normalized = normalizeGatewayConfig(config);
  const allowMissingApiKeyEnv = new Set(
    (Array.isArray(options.allowMissingApiKeyEnv) ? options.allowMissingApiKeyEnv : []).map((n) => String(n))
  );
  for (const [name, profile] of Object.entries(normalized.profiles)) {
    const nameCheck = validateProfileName(name);
    if (!nameCheck.valid) {
      throw new Error(`Invalid profile name "${safeErrorName(name)}": ${nameCheck.error}`);
    }
    const urlCheck = validateGatewayURL(profile.url);
    if (!urlCheck.valid) {
      throw new Error(`Profile "${name}": ${urlCheck.error}`);
    }
    const wireCheck = validateWireProtocol(profile.wireProtocol);
    if (!wireCheck.valid) {
      throw new Error(`Profile "${name}": ${wireCheck.error}`);
    }
    if (profile.apiKeyEnv) {
      // validateEnvName already rejects secret-shaped values while accepting long
      // UPPER_SNAKE names such as AGY_PRO_GATEWAY_<PROFILE>_API_KEY.
      const envCheck = validateEnvName(profile.apiKeyEnv);
      if (!envCheck.valid) {
        throw new Error(`Profile "${name}": ${envCheck.error}`);
      }
      profile.apiKeyEnv = envCheck.value;
    } else if (!urlCheck.isLoopback && !allowMissingApiKeyEnv.has(name)) {
      throw new Error(
        `Profile "${name}": apiKeyEnv is required for remote endpoints (only loopback URLs may omit an API key)`
      );
    }
    profile.models = parseGatewayModels(profile.models);
  }
  return normalized;
}

/**
 * Loads `gateway.json` (auto-migrating v1 flat objects in memory).
 * Returns an empty v2 config if the file does not exist.
 * @param {{ configPath?: string }} [options]
 * @returns {{ version: 2, activeProfile: string, active: boolean, profiles: Record<string, object> }}
 */
function loadGatewayConfig(options = {}) {
  const filePath = getGatewayConfigPath(options);
  if (!fs.existsSync(filePath)) {
    return { version: 2, activeProfile: '', active: false, profiles: {} };
  }
  const rawText = fs.readFileSync(filePath, 'utf8').replace(/^\ufeff/, '');
  if (!rawText.trim()) {
    return { version: 2, activeProfile: '', active: false, profiles: {} };
  }
  let parsed;
  try {
    parsed = JSON.parse(rawText);
  } catch (_) {
    // Never echo the parser message: V8 appends a raw file-content snippet, which can contain a
    // pasted API key.
    throw new Error(
      `Failed to parse ${filePath}: gateway.json is corrupted (invalid JSON); move or delete it to start fresh`
    );
  }
  return normalizeGatewayConfig(parsed);
}

/**
 * Validates and atomically writes `gateway.json` (mode `0o600`).
 * Never stores plaintext API keys inside `gateway.json`.
 * Repair writes (`options.allowMissingApiKeyEnv`) are reserved for `activateGateway`,
 * `deactivateGateway` and `deleteProfile`; user-facing save paths stay fully strict.
 * @param {object} config
 * @param {{ configPath?: string, allowMissingApiKeyEnv?: string[] }} [options]
 * @returns {object} Saved v2 config
 */
function saveGatewayConfig(config, options = {}) {
  const validated = validateGatewayConfig(config, {
    allowMissingApiKeyEnv: options.allowMissingApiKeyEnv
  });
  const filePath = getGatewayConfigPath(options);
  writeAtomic(filePath, JSON.stringify(validated, null, 2) + '\n', 0o600);
  return validated;
}

/**
 * Clears `apiKeyEnv` values that no longer pass `validateEnvName` (for example a profile written
 * before the reserved-name hardening, such as `apiKeyEnv: 'AGY_LLM_GATEWAY_URL'`). Mutates `config`
 * in place and reports every repaired entry so the caller can surface it; nothing is rewritten
 * silently.
 * A remote profile whose `apiKeyEnv` is already empty is reported as repaired as well: the strict
 * validator rejects that state, so without the exemption the repair write itself would throw and
 * `gateway off`/delete/switch would stay blocked until the file was edited by hand. Loopback
 * profiles legitimately omit the key name and are left alone.
 * Entries never carry the original value: it may be a pasted secret, and `validateEnvName` embeds
 * reserved names in its error text. Only `maskedApiKeyEnv` (or an empty string) plus a fixed
 * message are returned, so callers cannot leak the value even by serializing the whole record.
 * @param {object} config
 * @returns {Array<{ profile: string, maskedApiKeyEnv: string, error: string }>}
 */
function sanitizeInvalidApiKeyEnv(config) {
  const cleared = [];
  const profiles = config && config.profiles;
  if (!profiles || typeof profiles !== 'object') return cleared;

  for (const [name, profile] of Object.entries(profiles)) {
    if (!profile || typeof profile !== 'object') continue;
    const original = String(profile.apiKeyEnv || '')
      .replace(/\ufeff/g, '')
      .trim();
    if (!original) {
      const urlCheck = validateGatewayURL(profile.url);
      if (urlCheck.valid && !urlCheck.isLoopback) {
        cleared.push({
          profile: name,
          maskedApiKeyEnv: '',
          error:
            'apiKeyEnv is required for remote endpoints (left empty; repair saved without a key name)'
        });
      }
      continue;
    }
    const check = validateEnvName(original);
    if (check.valid) {
      profile.apiKeyEnv = check.value;
      continue;
    }
    profile.apiKeyEnv = '';
    cleared.push({
      profile: name,
      maskedApiKeyEnv: maskSecret(original),
      error: 'invalid or reserved env name (value hidden)'
    });
  }
  return cleared;
}

/**
 * Builds the `spawnSync` spec for Windows HKCU\Environment persistence with zero argv secrets.
 * @param {Record<string, string|null>} kvMap
 * @returns {{ command: string, args: string[], input: string, env: Record<string, string> }}
 */
function buildWindowsSpawnSpec(kvMap) {
  return {
    command: resolvePowershellExe(),
    args: ['-NoProfile', '-NonInteractive', '-Command', '-'],
    input: WINDOWS_PERSIST_SCRIPT,
    env: {
      ...process.env,
      __AGY_ENV_MAP_JSON: JSON.stringify(kvMap)
    }
  };
}

/**
 * Reads the simulated/mock persistent env JSON file (used in sandboxed/test runs).
 * @param {string} mockFile
 * @returns {Record<string, string>}
 */
function readMockEnvFile(mockFile) {
  if (!fs.existsSync(mockFile)) return {};
  try {
    const raw = fs.readFileSync(mockFile, 'utf8').replace(/^\ufeff/, '');
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch (_) {
    return {};
  }
}

/**
 * Applies `kvMap` (`null` deletes) to the mock persistent env JSON file atomically (`0o600`).
 * @param {string} mockFile
 * @param {Record<string, string|null>} kvMap
 */
function writeMockEnvFile(mockFile, kvMap) {
  const current = readMockEnvFile(mockFile);
  for (const [k, v] of Object.entries(kvMap)) {
    const cleanKey = String(k).replace(/\ufeff/g, '').trim();
    // Remove BOM-polluted variants
    for (const existingKey of Object.keys(current)) {
      if (existingKey !== cleanKey && existingKey.replace(/\ufeff/g, '').trim() === cleanKey) {
        delete current[existingKey];
      }
    }
    if (v === null || v === undefined || v === '') {
      delete current[cleanKey];
    } else {
      current[cleanKey] = String(v);
    }
  }
  writeAtomic(mockFile, JSON.stringify(current, null, 2) + '\n', 0o600);
}

/**
 * Applies environment updates on Windows (`HKCU\Environment` via stdin PowerShell,
 * or mock env store when `AGY_PRO_HOME` / `AGY_PRO_ENV_FILE` / `win32Runner` is active).
 * @param {Record<string, string|null>} kvMap
 * @param {object} [options]
 */
function persistWindowsEnvMap(kvMap, options = {}) {
  const spec = buildWindowsSpawnSpec(kvMap);
  if (typeof options.win32Runner === 'function') {
    options.win32Runner(kvMap, spec);
    return;
  }

  const mockFile = getMockEnvFilePath(options);
  if (mockFile) {
    writeMockEnvFile(mockFile, kvMap);
    return;
  }

  const res = spawnSync(spec.command, spec.args, {
    input: spec.input,
    env: spec.env,
    encoding: 'utf8',
    windowsHide: true
  });
  if (res.error) {
    throw new Error(`Failed to persist Windows user environment: ${res.error.message}`);
  }
  if (res.status !== 0) {
    const stderr = String(res.stderr || '').trim();
    throw new Error(
      `PowerShell HKCU\\Environment update exited with code ${res.status}${stderr ? `: ${stderr}` : ''}`
    );
  }
}

/**
 * Reads persistent user environment variables on Windows (or mock file in sandbox).
 * @param {string[]} keys
 * @param {object} [options]
 * @returns {{ vars: Record<string, string>, bomVariants: string[] }}
 */
function readWindowsEnvMap(keys = GATEWAY_ENV_KEYS, options = {}) {
  if (typeof options.win32Reader === 'function') {
    return options.win32Reader(keys);
  }

  const mockFile = getMockEnvFilePath(options);
  if (mockFile) {
    const all = readMockEnvFile(mockFile);
    const vars = {};
    const bomVariants = [];
    for (const [k, v] of Object.entries(all)) {
      const clean = k.replace(/\ufeff/g, '').trim();
      const matchesTarget = !keys.length || keys.includes(clean);
      if (!matchesTarget) continue;
      if (k !== clean) {
        bomVariants.push(k);
      }
      vars[k] = String(v);
    }
    return { vars, bomVariants };
  }

  const res = spawnSync(resolvePowershellExe(), ['-NoProfile', '-NonInteractive', '-Command', '-'], {
    input: WINDOWS_READ_SCRIPT,
    env: {
      ...process.env,
      __AGY_ENV_QUERY_JSON: JSON.stringify(keys)
    },
    encoding: 'utf8',
    windowsHide: true
  });
  if (res.error || res.status !== 0) {
    return { vars: {}, bomVariants: [] };
  }
  try {
    const parsed = JSON.parse(String(res.stdout || '{}').trim() || '{}');
    return {
      vars: parsed.vars && typeof parsed.vars === 'object' ? parsed.vars : {},
      bomVariants: Array.isArray(parsed.bomVariants) ? parsed.bomVariants : []
    };
  } catch (_) {
    return { vars: {}, bomVariants: [] };
  }
}

/**
 * Escapes a value safely for POSIX single-quoted shell assignments.
 * @param {string} val
 * @returns {string}
 */
function posixSingleQuote(val) {
  return `'${String(val).replace(/'/g, "'\\''")}'`;
}

/**
 * Renders the POSIX `gateway.env` file contents.
 * Enforces strict environment variable key name validation before emitting `export`.
 * @param {Record<string, string|null>} kvMap
 * @returns {string}
 */
function renderPosixGatewayEnv(kvMap) {
  const entries = Object.entries(kvMap).filter(([, v]) => v !== null && v !== undefined && v !== '');
  if (entries.length === 0) {
    return '# agy-pro gateway is OFF (native models active)\n';
  }
  const lines = ['# Managed by agy-pro (mode 0600). Do not commit this file.'];
  for (const [k, v] of entries) {
    if (!ENV_KEY_SAFE_RE.test(k)) {
      throw new Error(`Refusing to write unsafe environment variable name "${k}" to gateway.env`);
    }
    lines.push(`export ${k}=${posixSingleQuote(v)}`);
  }
  return lines.join('\n') + '\n';
}

/**
 * Resolves the POSIX shell rc files that should receive the `# >>> agy-pro gateway >>>` source block.
 * In sandboxed `AGY_PRO_HOME` runs, avoids modifying the real user's `~/.bashrc`/`~/.zshrc`
 * unless `options.rcFiles` or `AGY_PRO_RC_FILE` is explicitly provided.
 * @param {{ rcFiles?: string[] }} [options]
 * @returns {string[]}
 */
function resolvePosixRcFiles(options = {}) {
  if (Array.isArray(options.rcFiles)) {
    return options.rcFiles.map((f) => path.resolve(f));
  }
  if (process.env.AGY_PRO_RC_FILE) {
    return [path.resolve(process.env.AGY_PRO_RC_FILE)];
  }
  if (process.env.AGY_PRO_HOME) {
    return [];
  }
  const home = os.homedir();
  const candidates = [
    path.join(home, '.zshenv'),
    path.join(home, '.zshrc'),
    path.join(home, '.bashrc'),
    path.join(home, '.profile')
  ];
  const existing = candidates.filter((p) => fs.existsSync(p));
  if (existing.length > 0) return existing;
  return [path.join(home, process.platform === 'darwin' ? '.zshrc' : '.bashrc')];
}

/**
 * Idempotently inserts or removes the `# >>> agy-pro gateway >>>` source block in a shell rc file.
 * @param {string} rcPath
 * @param {string} envPath
 * @param {boolean} enabled
 */
function updateShellRcBlock(rcPath, envPath, enabled) {
  const exists = fs.existsSync(rcPath);
  if (!exists && !enabled) return;

  const existingMode = exists ? fs.statSync(rcPath).mode & 0o777 : 0o644;
  const targetMode = existingMode || 0o644;
  const content = exists ? fs.readFileSync(rcPath, 'utf8') : '';
  const blockRegex = new RegExp(
    `\\r?\\n?${RC_BLOCK_START.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}[\\s\\S]*?${RC_BLOCK_END.replace(
      /[.*+?^${}()|[\]\\]/g,
      '\\$&'
    )}\\r?\\n?`,
    'g'
  );
  const stripped = content.replace(blockRegex, '\n').replace(/\n+$/, '');

  if (!enabled) {
    if (exists && stripped !== content.replace(/\n+$/, '')) {
      writeAtomic(rcPath, stripped ? stripped + '\n' : '', targetMode);
    }
    return;
  }

  const posixEnvPath = envPath.replace(/\\/g, '/');
  const block = [
    RC_BLOCK_START,
    `if [ -r ${posixSingleQuote(posixEnvPath)} ]; then`,
    `  . ${posixSingleQuote(posixEnvPath)}`,
    'fi',
    RC_BLOCK_END
  ].join('\n');

  const next = stripped ? `${stripped}\n\n${block}\n` : `${block}\n`;
  if (next !== content) {
    writeAtomic(rcPath, next, targetMode);
  }
}

/**
 * Parses `export KEY='val'` statements (including multi-line and `'\''`-escaped values)
 * from `gateway.env`.
 * @param {string} envPath
 * @returns {Record<string, string>}
 */
function readPosixGatewayEnv(envPath) {
  if (!fs.existsSync(envPath)) return {};
  const text = fs.readFileSync(envPath, 'utf8');
  const out = {};
  let i = 0;

  while (i < text.length) {
    // Skip whitespace/comments at line start
    if (text[i] === '\r' || text[i] === '\n' || text[i] === ' ' || text[i] === '\t') {
      i++;
      continue;
    }
    if (text[i] === '#') {
      const nl = text.indexOf('\n', i);
      if (nl < 0) break;
      i = nl + 1;
      continue;
    }

    const slice = text.slice(i);
    const match = slice.match(/^export\s+([A-Za-z_][A-Za-z0-9_]*)=/);
    if (!match) {
      const nl = text.indexOf('\n', i);
      if (nl < 0) break;
      i = nl + 1;
      continue;
    }

    const key = match[1];
    i += match[0].length;

    if (text[i] === "'") {
      i++; // skip opening '
      let val = '';
      while (i < text.length) {
        if (text[i] === "'") {
          // Handle POSIX '\'' escape sequence
          if (text.slice(i, i + 4) === "'\\''") {
            val += "'";
            i += 4;
            continue;
          }
          i++; // skip closing '
          break;
        }
        val += text[i];
        i++;
      }
      out[key] = val;
      const nl = text.indexOf('\n', i);
      i = nl < 0 ? text.length : nl + 1;
    } else {
      const nl = text.indexOf('\n', i);
      const end = nl < 0 ? text.length : nl;
      out[key] = text.slice(i, end).trim();
      i = nl < 0 ? text.length : nl + 1;
    }
  }
  return out;
}

/**
 * Persists a map of environment variables for the current platform and updates `process.env`.
 * @param {Record<string, string|null>} kvMap
 * @param {object} [options]
 */
function persistEnvMap(kvMap, options = {}) {
  const platform = options.platform || process.platform;

  for (const [k, v] of Object.entries(kvMap)) {
    if (v === null || v === undefined || v === '') {
      delete process.env[k];
    } else {
      process.env[k] = String(v);
    }
  }

  if (platform === 'win32') {
    persistWindowsEnvMap(kvMap, options);
    return;
  }

  const envPath = getGatewayEnvPath(options);
  // Preserve existing non-gateway keys (e.g., DEEPSEEK_API_KEY) already stored in gateway.env
  const current = readPosixGatewayEnv(envPath);
  for (const [k, v] of Object.entries(kvMap)) {
    if (v === null || v === undefined || v === '') {
      delete current[k];
    } else {
      current[k] = String(v);
    }
  }
  writeAtomic(envPath, renderPosixGatewayEnv(current), 0o600);
  const rcFiles = resolvePosixRcFiles(options);
  const hasAny = Object.keys(current).length > 0;
  for (const rc of rcFiles) {
    updateShellRcBlock(rc, envPath, hasAny);
  }
}

/**
 * Reads persisted environment variables for the current platform (plus `process.env` fallback).
 * @param {string[]} [keys]
 * @param {object} [options]
 * @returns {{ vars: Record<string, string>, bomVariants: string[] }}
 */
function readPersistedEnvMap(keys = GATEWAY_ENV_KEYS, options = {}) {
  const platform = options.platform || process.platform;
  if (platform === 'win32') {
    return readWindowsEnvMap(keys, options);
  }
  const envPath = getGatewayEnvPath(options);
  const stored = readPosixGatewayEnv(envPath);
  const vars = {};
  for (const k of Object.keys(stored)) {
    if (!keys.length || keys.includes(k)) {
      vars[k] = stored[k];
    }
  }
  return { vars, bomVariants: [] };
}

/**
 * Resolves an API key value for a profile from `options.apiKey`, `process.env`, or persisted store.
 * @param {object} profile
 * @param {object} [options]
 * @returns {string}
 */
function resolveProfileAPIKey(profile, options = {}) {
  if (options.apiKey !== undefined && options.apiKey !== null && String(options.apiKey).trim()) {
    return String(options.apiKey).trim();
  }
  const envName = String((profile && profile.apiKeyEnv) || '').trim();
  if (!envName) return '';
  if (process.env[envName] && String(process.env[envName]).trim()) {
    return String(process.env[envName]).trim();
  }
  const persisted = readPersistedEnvMap([envName], options);
  if (persisted.vars[envName]) {
    return String(persisted.vars[envName]).trim();
  }
  for (const bomKey of persisted.bomVariants || []) {
    if (persisted.vars[bomKey]) {
      return String(persisted.vars[bomKey]).trim();
    }
  }
  return '';
}

/**
 * Builds the `AGY_LLM_GATEWAY_*` environment map for a profile.
 * @param {object} profile
 * @param {string} [apiKeyValue='']
 * @returns {Record<string, string|null>}
 */
function buildGatewayEnvMap(profile, apiKeyValue = '') {
  const modelsStr = formatGatewayModels(profile.models || []);
  const cleanHeaders = profile.headers
    ? String(profile.headers)
        .split(/[\r\n]+/)
        .map((s) => s.trim())
        .filter(Boolean)
        .join(', ')
    : '';
  return {
    AGY_LLM_GATEWAY_URL: profile.url,
    AGY_LLM_GATEWAY_API_KEY: apiKeyValue ? String(apiKeyValue) : null,
    AGY_LLM_GATEWAY_WIRE_PROTOCOL: profile.wireProtocol || 'openai',
    AGY_LLM_GATEWAY_MODELS: modelsStr || null,
    AGY_LLM_GATEWAY_CA_CERT: profile.caCert ? String(profile.caCert) : null,
    AGY_LLM_GATEWAY_HEADERS: cleanHeaders || null
  };
}

/**
 * Post-write read-back check: confirms the persisted store holds the URL, wire protocol and
 * models that were just written. Skipped when a custom `win32Runner` intercepts writes
 * without a matching `win32Reader` (nothing real was persisted to read back).
 * An empty read-back is reported separately from a genuine value mismatch: the write may have
 * succeeded while the read failed (Windows PowerShell read errors are swallowed into `{}`).
 * @param {Record<string, string|null>} expected
 * @param {object} [options]
 */
function verifyPersistedGateway(expected, options = {}) {
  const platform = options.platform || process.platform;
  if (platform === 'win32' && typeof options.win32Runner === 'function' && typeof options.win32Reader !== 'function') {
    return;
  }
  const persisted = readPersistedEnvMap(GATEWAY_ENV_KEYS, options);
  const persistedKeys = Object.keys(persisted.vars || {});
  const clean = {};
  for (const [k, v] of Object.entries(persisted.vars || {})) {
    clean[k.replace(/\ufeff/g, '').trim()] = String(v);
  }
  const checked = ['AGY_LLM_GATEWAY_URL', 'AGY_LLM_GATEWAY_WIRE_PROTOCOL', 'AGY_LLM_GATEWAY_MODELS'];
  const mismatched = checked.filter((k) => expected[k] && clean[k] !== expected[k]);
  if (mismatched.length === 0) return;
  if (persistedKeys.length === 0) {
    throw new Error(
      'Post-write verification failed: read-back could not be confirmed (the persisted environment read ' +
        'returned no values), so the write may have succeeded while the read failed. ' +
        'Run `agy-pro doctor` to inspect the persisted store.'
    );
  }
  throw new Error(
    `Post-write verification failed: persisted environment does not match for ${mismatched.join(', ')}. ` +
      'Check write permissions or run `agy-pro doctor`.'
  );
}

/**
 * Returns true when `envName` is the auto-generated key name `agy-pro` itself created for
 * `profileName` (`AGY_PRO_GATEWAY_<PROFILE>_API_KEY`). Provider-hint names such as
 * `DEEPSEEK_API_KEY` are user-owned and deliberately excluded.
 * @param {string} envName
 * @param {string} profileName
 * @returns {boolean}
 */
function isOwnedAutoEnvName(envName, profileName) {
  const clean = String(envName || '')
    .replace(/\ufeff/g, '')
    .trim();
  if (!clean || !clean.startsWith(OWNED_ENV_PREFIX)) return false;
  return clean === defaultEnvForProvider(profileName);
}

/**
 * Returns true when any profile in `profiles` still references `envName`.
 * Windows environment variable names are case-insensitive, so `my_key` and `MY_KEY` are the same
 * variable; a case-sensitive check could purge a key a surviving profile still depends on.
 * @param {Record<string, object>} profiles
 * @param {string} envName
 * @returns {boolean}
 */
function isEnvReferencedByProfiles(profiles, envName) {
  const target = String(envName || '')
    .replace(/\ufeff/g, '')
    .trim()
    .toUpperCase();
  if (!target) return false;
  return Object.keys(profiles || {}).some((name) => {
    const ref = String((profiles[name] || {}).apiKeyEnv || '')
      .replace(/\ufeff/g, '')
      .trim()
      .toUpperCase();
    return ref === target;
  });
}

/**
 * Removes a saved profile from `gateway.json`. If it was the active profile, the gateway is
 * turned OFF (env cleared) and the active profile falls back to the first remaining one.
 * The profile's API key env var is purged from persistent storage only when `agy-pro` generated
 * the name itself and no remaining profile references it; shared or custom names are preserved.
 * @param {string} profileName
 * @param {object} [options]
 * @returns {{ deleted: string, wasActive: boolean, wasOn: boolean, activeProfile: string, purgedEnv: string[], clearedApiKeyEnv: Array<object> }}
 */
function deleteProfile(profileName, options = {}) {
  const config = loadGatewayConfig(options);
  const name = String(profileName || '').trim();
  if (!Object.prototype.hasOwnProperty.call(config.profiles, name)) {
    throw new Error(`Gateway profile "${safeErrorName(name)}" not found.`);
  }
  const removed = config.profiles[name];
  const removedEnvName = String((removed && removed.apiKeyEnv) || '').trim();
  const wasActive = config.activeProfile === name;
  const wasOn = wasActive && config.active;
  // Deactivate first so a later save failure cannot leave the gateway env pointing at a deleted profile.
  if (wasOn) {
    deactivateGateway({ ...options, keepState: true });
  }
  delete config.profiles[name];
  if (wasActive) {
    const keys = Object.keys(config.profiles);
    config.activeProfile = keys.length > 0 ? keys[0] : '';
    config.active = false;
  }
  // Repair surviving profiles whose apiKeyEnv no longer validates before the save rejects the file.
  const clearedApiKeyEnv = sanitizeInvalidApiKeyEnv(config);
  saveGatewayConfig(config, {
    ...options,
    allowMissingApiKeyEnv: clearedApiKeyEnv.map((entry) => entry.profile)
  });

  // Purge the orphan credential only after the config is durably saved.
  const purgedEnv = [];
  if (
    removedEnvName &&
    isOwnedAutoEnvName(removedEnvName, name) &&
    !isEnvReferencedByProfiles(config.profiles, removedEnvName)
  ) {
    persistEnvMap({ [removedEnvName]: null }, options);
    purgedEnv.push(removedEnvName);
  }

  return {
    deleted: name,
    wasActive,
    wasOn,
    activeProfile: config.activeProfile,
    purgedEnv,
    clearedApiKeyEnv
  };
}

/**
 * Activates a saved profile (or `config.activeProfile`) by writing `AGY_LLM_GATEWAY_*`
 * to persistent OS storage and `process.env`, and marking `active: true` in `gateway.json`.
 * Validates profile and `gateway.json` schema BEFORE writing any OS env state.
 * A poisoned, non-target profile no longer blocks activation: its invalid `apiKeyEnv` is cleared
 * (and reported back in `clearedApiKeyEnv`) instead of failing the whole config save.
 * @param {string} [profileName]
 * @param {{ apiKey?: string, configPath?: string, envPath?: string, envFile?: string, rcFiles?: string[], platform?: string, win32Runner?: Function }} [options]
 * @returns {{ profileName: string, profile: object, envMap: Record<string, string|null>, maskedKey: string, clearedApiKeyEnv: Array<object> }}
 */
function activateGateway(profileName, options = {}) {
  const config = loadGatewayConfig(options);
  const targetName = String(profileName || config.activeProfile || '').trim();
  if (!targetName) {
    throw new Error('No gateway profile specified and no activeProfile configured. Run `agy-pro setup` first.');
  }
  if (!Object.prototype.hasOwnProperty.call(config.profiles, targetName)) {
    const available = Object.keys(config.profiles);
    // Both the queried name and the hint list come from gateway.json, so both go through
    // `safeErrorName` (control/format strip + credential mask): the CLI writes `err.message`
    // verbatim, and a tampered key could otherwise paint the terminal or leak a pasted secret.
    const hint =
      available.length > 0 ? ` Available: ${available.map((n) => safeErrorName(n)).join(', ')}` : '';
    throw new Error(`Gateway profile "${safeErrorName(targetName)}" not found.${hint}`);
  }
  const profile = config.profiles[targetName];
  const safeTarget = safeErrorName(targetName);

  const urlCheck = validateGatewayURL(profile.url);
  if (!urlCheck.valid) {
    throw new Error(`Profile "${safeTarget}": ${urlCheck.error}`);
  }
  const wireCheck = validateWireProtocol(profile.wireProtocol);
  if (!wireCheck.valid) {
    throw new Error(`Profile "${safeTarget}": ${wireCheck.error}`);
  }
  if (profile.apiKeyEnv) {
    const envCheck = validateEnvName(profile.apiKeyEnv);
    if (!envCheck.valid) {
      throw new Error(`Profile "${safeTarget}": ${envCheck.error}`);
    }
    profile.apiKeyEnv = envCheck.value;
  } else if (!urlCheck.isLoopback) {
    // The repair exemption below is for stale profiles that are not being used; the profile being
    // activated must still name a key variable, or the saved config could not be turned on again.
    throw new Error(
      `Profile "${safeTarget}": apiKeyEnv is required for remote endpoints (only loopback URLs may omit an API key)`
    );
  }

  const loopback = urlCheck.isLoopback;
  const apiKeyValue = resolveProfileAPIKey(profile, options);
  if (!loopback && !apiKeyValue) {
    throw new Error(
      `API key is not set for profile "${safeTarget}" (expected env var ${maskSecret(profile.apiKeyEnv) || 'AGY_LLM_GATEWAY_API_KEY'}). Set it or re-run \`agy-pro setup\`.`
    );
  }

  const updates = buildGatewayEnvMap(profile, apiKeyValue);
  if (apiKeyValue && profile.apiKeyEnv) {
    if (!ENV_KEY_SAFE_RE.test(profile.apiKeyEnv)) {
      throw new Error(`Profile "${safeTarget}": unsafe apiKeyEnv name`);
    }
    updates[profile.apiKeyEnv] = apiKeyValue;
  }

  config.activeProfile = targetName;
  config.active = true;
  const clearedApiKeyEnv = sanitizeInvalidApiKeyEnv(config);
  saveGatewayConfig(config, {
    ...options,
    allowMissingApiKeyEnv: clearedApiKeyEnv.map((entry) => entry.profile)
  });
  persistEnvMap(updates, options);
  verifyPersistedGateway(updates, options);

  return {
    profileName: targetName,
    profile,
    envMap: updates,
    maskedKey: apiKeyValue ? maskSecret(apiKeyValue) : '(none - loopback)',
    clearedApiKeyEnv
  };
}

/**
 * Deactivates gateway mode by clearing all `AGY_LLM_GATEWAY_*` variables from
 * persistent OS storage and `process.env` so native `agy` models are restored.
 * When `keepState` is false (uninstall), removes `gateway.json`, `gateway.env`,
 * mock win32 env store, and shell rc source blocks.
 * Turning the gateway OFF never fails on a poisoned profile: invalid `apiKeyEnv` values are cleared
 * (and remote profiles missing one are exempted) and reported in `clearedApiKeyEnv` before
 * `gateway.json` is saved.
 * @param {{ keepState?: boolean, configPath?: string, envPath?: string, envFile?: string, rcFiles?: string[], platform?: string, win32Runner?: Function }} [options]
 * @returns {{ deactivated: boolean, keepState: boolean, clearedKeys: string[], clearedApiKeyEnv: Array<object> }}
 */
function deactivateGateway(options = {}) {
  const keepState = options.keepState !== false;
  const clearMap = {};
  for (const k of GATEWAY_ENV_KEYS) {
    clearMap[k] = null;
  }

  persistEnvMap(clearMap, options);

  const configPath = getGatewayConfigPath(options);
  let clearedApiKeyEnv = [];
  if (keepState) {
    if (fs.existsSync(configPath)) {
      const config = loadGatewayConfig(options);
      config.active = false;
      clearedApiKeyEnv = sanitizeInvalidApiKeyEnv(config);
      saveGatewayConfig(config, {
        ...options,
        allowMissingApiKeyEnv: clearedApiKeyEnv.map((entry) => entry.profile)
      });
    }
  } else {
    const envPath = getGatewayEnvPath(options);
    for (const file of [configPath, envPath, getMockEnvFilePath(options)]) {
      if (file && fs.existsSync(file)) {
        fs.unlinkSync(file);
      }
    }
    const platform = options.platform || process.platform;
    if (platform !== 'win32') {
      for (const rc of resolvePosixRcFiles(options)) {
        updateShellRcBlock(rc, envPath, false);
      }
    }
  }

  return {
    deactivated: true,
    keepState,
    clearedKeys: [...GATEWAY_ENV_KEYS],
    clearedApiKeyEnv
  };
}

/**
 * Resolves the platform MDM `admin_settings.json` path.
 * @param {{ adminSettingsPath?: string, platform?: string }} [options]
 * @returns {string}
 */
function getAdminSettingsPath(options = {}) {
  if (options.adminSettingsPath) return path.resolve(options.adminSettingsPath);
  if (process.env.AGY_ADMIN_SETTINGS_PATH) {
    return path.resolve(process.env.AGY_ADMIN_SETTINGS_PATH);
  }
  const platform = options.platform || process.platform;
  if (platform === 'win32') {
    const programData = process.env.ProgramData || 'C:\\ProgramData';
    return path.join(programData, 'Antigravity', 'admin_settings.json');
  }
  if (platform === 'darwin') {
    return '/Library/Application Support/Antigravity/admin_settings.json';
  }
  return '/etc/antigravity/admin_settings.json';
}

/**
 * Read-only check for enterprise MDM `admin_settings.json` overrides.
 * The MDM file is read only to decide `configured`; `settings` stays on the record for future
 * callers, so any surface that serializes the return value must project its own whitelist.
 * @param {{ adminSettingsPath?: string, platform?: string }} [options]
 * @returns {{ exists: boolean, path: string, configured: boolean, settings: object|null, error: string|null }}
 */
function detectAdminGatewaySettings(options = {}) {
  const targetPath = getAdminSettingsPath(options);
  if (!fs.existsSync(targetPath)) {
    return { exists: false, path: targetPath, configured: false, settings: null, error: null };
  }
  try {
    const raw = fs.readFileSync(targetPath, 'utf8').replace(/^\ufeff/, '');
    const parsed = JSON.parse(raw);
    const configured = Boolean(
      parsed &&
        typeof parsed === 'object' &&
        ((parsed.gateway && typeof parsed.gateway === 'object' && parsed.gateway.url) ||
          (parsed.models && Array.isArray(parsed.models.list) && parsed.models.list.length > 0))
    );
    return { exists: true, path: targetPath, configured, settings: parsed, error: null };
  } catch (_) {
    // Never echo the parser message: V8 appends a raw file-content snippet, and the MDM file can
    // hold enterprise credentials.
    return {
      exists: true,
      path: targetPath,
      configured: false,
      settings: null,
      error: 'admin_settings.json is not valid JSON (contents withheld)'
    };
  }
}

/**
 * Computes comprehensive gateway status (`NATIVE` vs `GATEWAY_XOR` vs `ADMIN_OVERRIDE`).
 * @param {object} [options]
 * @returns {object}
 */
function getGatewayStatus(options = {}) {
  const config = loadGatewayConfig(options);
  const persisted = readPersistedEnvMap(GATEWAY_ENV_KEYS, options);
  const adminSettings = detectAdminGatewaySettings(options);

  const effectiveEnv = {};
  for (const k of GATEWAY_ENV_KEYS) {
    effectiveEnv[k] = process.env[k] || persisted.vars[k] || '';
  }

  const activeUrl = effectiveEnv.AGY_LLM_GATEWAY_URL;
  const activeKey = effectiveEnv.AGY_LLM_GATEWAY_API_KEY;
  const activeWire = effectiveEnv.AGY_LLM_GATEWAY_WIRE_PROTOCOL || (activeUrl ? 'genai' : '');
  const rawModels = effectiveEnv.AGY_LLM_GATEWAY_MODELS;

  let models = [];
  let modelsError = null;
  if (rawModels) {
    try {
      models = parseGatewayModels(rawModels);
    } catch (err) {
      modelsError = err.message;
    }
  }

  let mode = 'NATIVE';
  if (adminSettings.configured) {
    mode = 'ADMIN_OVERRIDE';
  } else if (activeUrl) {
    mode = 'GATEWAY_XOR';
  }

  let maskedKey = '(unset)';
  if (activeKey) {
    maskedKey = maskSecret(activeKey);
  } else if (activeUrl && isLoopbackURL(activeUrl)) {
    maskedKey = '(none - loopback)';
  }

  return {
    mode,
    active: mode === 'GATEWAY_XOR',
    activeProfile: config.activeProfile,
    profiles: Object.keys(config.profiles),
    url: activeUrl,
    wireProtocol: activeWire,
    models,
    modelsError,
    maskedKey,
    caCert: effectiveEnv.AGY_LLM_GATEWAY_CA_CERT,
    headers: effectiveEnv.AGY_LLM_GATEWAY_HEADERS,
    adminSettings,
    configPath: getGatewayConfigPath(options),
    bomVariants: persisted.bomVariants || []
  };
}

module.exports = {
  GATEWAY_ENV_KEYS,
  RC_BLOCK_START,
  RC_BLOCK_END,
  WINDOWS_PERSIST_SCRIPT,
  WINDOWS_READ_SCRIPT,
  resolvePowershellExe,
  getAgyProHome,
  getGatewayConfigPath,
  getGatewayEnvPath,
  writeAtomic,
  normalizeGatewayConfig,
  loadGatewayConfig,
  saveGatewayConfig,
  buildWindowsSpawnSpec,
  renderPosixGatewayEnv,
  readPosixGatewayEnv,
  persistEnvMap,
  readPersistedEnvMap,
  resolveProfileAPIKey,
  buildGatewayEnvMap,
  activateGateway,
  deactivateGateway,
  deleteProfile,
  verifyPersistedGateway,
  sanitizeInvalidApiKeyEnv,
  isEnvReferencedByProfiles,
  getAdminSettingsPath,
  detectAdminGatewaySettings,
  getGatewayStatus,
  displayConfigName
};
