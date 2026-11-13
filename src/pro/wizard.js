/**
 * @fileoverview Interactive 7-step setup wizard and non-interactive `--yes` setup runner
 * for `agy-pro setup`, modeled on `agy-pro/internal/providers/wizard.go` & `presets.go`.
 */

'use strict';

const fs = require('fs');
const path = require('path');

const {
  knownProviders,
  canonicalProviderName,
  lookupProviderHint,
  getModelPresets,
  defaultEnvForProvider
} = require('./presets');
const {
  OPENAI_TOOL_WARNING,
  XOR_MODELS_WARNING,
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
  stripControlAndFormat
} = require('./validator');
const { fetchRemoteModels, probeEndpoint } = require('./http-client');
const { promptSecret, readSecretFromStdin, SecretInterruptedError } = require('./secret-input');
const {
  loadGatewayConfig,
  saveGatewayConfig,
  activateGateway,
  deactivateGateway,
  deleteProfile,
  getAgyProHome,
  getGatewayConfigPath,
  getGatewayStatus,
  resolveProfileAPIKey,
  persistEnvMap,
  displayConfigName
} = require('./env-store');

/**
 * Env-name prefix for keys agy-pro generates itself (`AGY_PRO_GATEWAY_<PROFILE>_API_KEY`).
 * @type {string}
 */
const OWNED_ENV_PREFIX = 'AGY_PRO_GATEWAY_';

/**
 * Env var names owned by the provider catalog. A profile that reads any other (unmanaged)
 * name must get explicit consent before its value is sent to the profile URL.
 * @type {Set<string>}
 */
const KNOWN_PROVIDER_ENV_NAMES = new Set(
  knownProviders({ includeCustom: true })
    .map((p) => String(p.apiKeyEnv || '').trim())
    .filter(Boolean)
);

/**
 * Catalog provider entries keyed by their `apiKeyEnv` name. Used to bind a catalog env name to
 * the host it is meant for: a profile that reads `DEEPSEEK_API_KEY` but points at another host
 * is a confused deputy, not a trusted catalog match.
 * @type {Map<string, object>}
 */
const CATALOG_PROVIDERS_BY_ENV = new Map();
for (const provider of knownProviders({ includeCustom: true })) {
  const envName = String(provider.apiKeyEnv || '').trim();
  if (envName && !CATALOG_PROVIDERS_BY_ENV.has(envName)) {
    CATALOG_PROVIDERS_BY_ENV.set(envName, provider);
  }
}

/**
 * ANSI escape sequences (CSI, OSC, and every short escape including ECMA-48 intermediate-byte
 * forms). Removed as whole sequences before any control-character stripping so
 * `Author\x1b[31mization` collapses to `Authorization` instead of the `Author[31mization` residue
 * that a naive ESC strip would leave behind.
 *
 * The short-form family matters as much as CSI: `\x1b7` (DECSC), `\x1b8` (DECRC), `\x1bc` (RIS),
 * and `\x1b(B` (charset designation) used to survive as `7`, `8`, `c`, `(B` residue, which left
 * `Authoriz7ation` unmatched against `authorization` and let the credential through. The grammar
 * therefore consumes `ESC` + up to two intermediate bytes (`0x20`-`0x2F`, which covers `(`, `)`,
 * `#`, space) + one final byte, and falls back to `ESC` + any single character for the two-byte
 * short escapes. `ESC \` (ST) is the one sequence the OSC alternative already consumes.
 *
 * Documented residual compromises: an unterminated OSC (`\x1b]0;text` with no BEL/ST) swallows
 * the rest of the string, and U+034F (combining grapheme joiner) is not in the format-rune class,
 * so it survives at rest.
 * @type {RegExp}
 */
const ANSI_ESCAPE_RE =
  /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][\s\S]*?(?:\x07|\x1b\\)|\x1b[ -/]{1,2}[\s\S]|\x1b[\s\S]/g;

/**
 * Characters dropped from a header NAME before the credential comparison: every C0/C1 control
 * character, soft hyphen, NBSP, zero-width/format runes, and any whitespace. A header name cannot
 * contain whitespace, so `Authoriz ation` must fold onto `Authorization`.
 * @type {RegExp}
 */
const HEADER_NAME_JUNK_RE =
  /[\x00-\x1f\x7f-\x9f\u00ad\u00a0\u200b-\u200f\u202a-\u202e\u2060-\u206f\ufeff\ufff9-\ufffb\s]/g;

/**
 * Characters dropped from a header line that is KEPT. Deliberately narrower than
 * `HEADER_NAME_JUNK_RE`: whitespace, NBSP, and punctuation stay as the user wrote them, and no
 * NFKC pass runs on kept text (see `stripAuthorizationHeaders`).
 * @type {RegExp}
 */
const HEADER_OUTPUT_JUNK_RE =
  /[\x00-\x1f\x7f-\x9f\u00ad\u200b-\u200f\u202a-\u202e\u2060-\u206f\ufeff\ufff9-\ufffb]/g;

/**
 * Header names that carry credentials. They never belong in gateway.json (auth flows through
 * the profile env var). Names are compared after control-char stripping, case-insensitively.
 * @type {Set<string>}
 */
const CREDENTIAL_HEADER_NAMES = new Set([
  'authorization',
  'proxy-authorization',
  'x-api-key',
  'api-key',
  'x-auth-token',
  'x-auth',
  'auth-token'
]);

/**
 * Creates a line-oriented prompt helper over a readable/writable stream pair.
 * Reads incrementally without buffering ahead past newlines so raw TTY `promptSecret`
 * can safely share `input`.
 * @param {NodeJS.ReadableStream} input
 * @param {NodeJS.WritableStream} output
 */
function createLinePrompter(input, output) {
  function readLine() {
    return new Promise((resolve, reject) => {
      let buf = '';
      let settled = false;

      const cleanup = () => {
        input.removeListener('data', onData);
        input.removeListener('end', onEnd);
        input.removeListener('error', onError);
      };

      const finish = (line, eof) => {
        if (settled) return;
        settled = true;
        cleanup();
        if (typeof input.pause === 'function' && input.isTTY) {
          try {
            input.pause();
          } catch (_) {
            // Ignore
          }
        }
        resolve({ line: line.replace(/^\ufeff/, '').trim(), eof });
      };

      const onData = (chunk) => {
        const str = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk);
        for (const ch of str) {
          if (ch === '\x03') {
            if (!settled) {
              settled = true;
              cleanup();
              reject(new SecretInterruptedError('Setup aborted (Ctrl+C)'));
            }
            return;
          }
          if (ch === '\n') {
            finish(buf.replace(/\r$/, ''), false);
            return;
          }
          buf += ch;
        }
      };

      const onEnd = () => {
        finish(buf.replace(/\r$/, ''), buf.length === 0);
      };

      const onError = (err) => {
        if (settled) return;
        settled = true;
        cleanup();
        reject(err);
      };

      input.on('data', onData);
      input.on('end', onEnd);
      input.on('error', onError);
      if (typeof input.resume === 'function') {
        input.resume();
      }
    });
  }

  async function ask(label, defaultValue = '') {
    if (defaultValue && !label.includes('[')) {
      output.write(`${label} [${defaultValue}]: `);
    } else {
      output.write(`${label}: `);
    }
    const { line, eof } = await readLine();
    if (!line) {
      return { value: defaultValue, eof };
    }
    return { value: line, eof };
  }

  return { ask };
}

/**
 * Parses `agy-pro setup` flags and detects forbidden `--api-key <secret>` usage.
 * @param {string[]} args
 * @returns {{ flags: object, error?: string }}
 */
function parseSetupArgs(args = []) {
  const flags = {
    yes: false,
    profile: '',
    provider: '',
    url: '',
    wire: '',
    models: '',
    apiKeyEnv: '',
    apiKeyStdin: false,
    caCert: '',
    headers: '',
    activate: true,
    probe: false
  };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--api-key' || arg.startsWith('--api-key=')) {
      return {
        flags,
        error:
          'Refusing --api-key on command line (secrets in argv leak to process lists). Use --api-key-stdin or AGY_PRO_API_KEY_INPUT instead.'
      };
    }
    if (arg === '--yes' || arg === '-y') {
      flags.yes = true;
    } else if (arg === '--api-key-stdin') {
      flags.apiKeyStdin = true;
    } else if (arg === '--activate') {
      flags.activate = true;
    } else if (arg === '--no-activate') {
      flags.activate = false;
    } else if (arg === '--probe') {
      flags.probe = true;
    } else if (arg === '--profile' && i + 1 < args.length) {
      flags.profile = args[++i];
    } else if (arg.startsWith('--profile=')) {
      flags.profile = arg.slice('--profile='.length);
    } else if (arg === '--provider' && i + 1 < args.length) {
      flags.provider = args[++i];
    } else if (arg.startsWith('--provider=')) {
      flags.provider = arg.slice('--provider='.length);
    } else if (arg === '--url' && i + 1 < args.length) {
      flags.url = args[++i];
    } else if (arg.startsWith('--url=')) {
      flags.url = arg.slice('--url='.length);
    } else if (arg === '--wire' && i + 1 < args.length) {
      flags.wire = args[++i];
    } else if (arg.startsWith('--wire=')) {
      flags.wire = arg.slice('--wire='.length);
    } else if (arg === '--models' && i + 1 < args.length) {
      flags.models = args[++i];
    } else if (arg.startsWith('--models=')) {
      flags.models = arg.slice('--models='.length);
    } else if (arg === '--api-key-env' && i + 1 < args.length) {
      flags.apiKeyEnv = args[++i];
    } else if (arg.startsWith('--api-key-env=')) {
      flags.apiKeyEnv = arg.slice('--api-key-env='.length);
    } else if (arg === '--ca-cert' && i + 1 < args.length) {
      flags.caCert = args[++i];
    } else if (arg.startsWith('--ca-cert=')) {
      flags.caCert = arg.slice('--ca-cert='.length);
    } else if (arg === '--headers' && i + 1 < args.length) {
      flags.headers = args[++i];
    } else if (arg.startsWith('--headers=')) {
      flags.headers = arg.slice('--headers='.length);
    } else if (arg.startsWith('-')) {
      return { flags, error: `Unknown option for setup: ${arg}` };
    }
  }

  return { flags };
}

/**
 * Merges static provider presets with discovered `/models` entries without duplicates.
 * @param {string} providerName
 * @param {Array<{ id: string, contextLength?: number }>} remoteModels
 * @returns {Array<{ id: string, description: string, recommended: boolean }>}
 */
function buildModelCandidates(providerName, remoteModels) {
  const staticPresets = getModelPresets(providerName).map((m) => ({
    id: m.id,
    description: m.description || '',
    recommended: Boolean(m.recommended)
  }));
  const seen = new Set(staticPresets.map((m) => m.id));

  for (const rm of remoteModels || []) {
    if (!rm || !rm.id || seen.has(rm.id)) continue;
    if (staticPresets.length >= 12) break;
    seen.add(rm.id);
    staticPresets.push({
      id: rm.id,
      description: 'discovered via /models',
      recommended: staticPresets.length === 0
    });
  }
  return staticPresets;
}

/**
 * Resolves a numbered or comma-separated model selection string into model IDs.
 * @param {string} rawChoice
 * @param {Array<{ id: string }>} candidates
 * @returns {string[]}
 */
function resolveModelSelectionInput(rawChoice, candidates) {
  const trimmed = String(rawChoice || '').trim();
  if (!trimmed) return [];
  if (trimmed.toLowerCase() === 'all' && candidates.length > 0) {
    return candidates.map((c) => c.id);
  }
  if (trimmed.startsWith('[') || trimmed.startsWith('{')) {
    return parseGatewayModels(trimmed);
  }

  const tokens = trimmed.split(',').map((t) => t.trim()).filter(Boolean);
  const allNumeric = tokens.length > 0 && tokens.every((t) => /^\d+$/.test(t));
  if (allNumeric && candidates.length > 0) {
    const out = [];
    for (const tok of tokens) {
      const idx = Number(tok) - 1;
      if (idx >= 0 && idx < candidates.length && !out.includes(candidates[idx].id)) {
        out.push(candidates[idx].id);
      }
    }
    return out;
  }

  return parseGatewayModels(trimmed);
}

/**
 * Runs non-interactive `--yes` setup using CLI flags + optional stdin/env secret.
 * @param {object} flags
 * @param {object} deps
 * @returns {Promise<{ exitCode: number, result?: object }>}
 */
async function runNonInteractiveSetup(flags, deps) {
  const { input, output, envOptions, transport } = deps;
  const errOut = deps.errOutput || output;

  if (!flags.provider && !flags.url) {
    errOut.write(
      'Error: Non-interactive `agy-pro setup --yes` requires --provider <name> or --url <url>.\n'
    );
    return { exitCode: 2 };
  }

  const hint = flags.provider ? lookupProviderHint(flags.provider) : null;
  const provider = hint ? hint.name : canonicalProviderName(flags.provider || 'custom') || 'custom';
  const profileName = (flags.profile || provider || 'default').trim();
  const profileCheck = validateProfileName(profileName);
  if (!profileCheck.valid) {
    errOut.write(`Error: ${profileCheck.error}\n`);
    return { exitCode: 2 };
  }

  const rawUrl = flags.url || (hint ? hint.baseURL : '');
  const urlCheck = validateGatewayURL(rawUrl);
  if (!urlCheck.valid) {
    errOut.write(`Error: ${urlCheck.error}\n`);
    return { exitCode: 2 };
  }

  const rawWire = flags.wire || (hint ? hint.wireProtocol : 'openai');
  const wireCheck = validateWireProtocol(rawWire);
  if (!wireCheck.valid) {
    errOut.write(`Error: ${wireCheck.error}\n`);
    return { exitCode: 2 };
  }

  let apiKeyEnv = flags.apiKeyEnv
    ? flags.apiKeyEnv.trim()
    : hint
      ? hint.apiKeyEnv
      : defaultEnvForProvider(profileCheck.value);

  if (apiKeyEnv) {
    const envCheck = validateEnvName(apiKeyEnv);
    if (!envCheck.valid) {
      errOut.write(`Error: ${envCheck.error}\n`);
      return { exitCode: 2 };
    }
    apiKeyEnv = envCheck.value;
  } else if (!urlCheck.isLoopback) {
    apiKeyEnv = defaultEnvForProvider(profileCheck.value);
  }

  let apiKeyValue = '';
  if (flags.apiKeyStdin) {
    apiKeyValue = await readSecretFromStdin(input);
  } else if (process.env.AGY_PRO_API_KEY_INPUT) {
    apiKeyValue = String(process.env.AGY_PRO_API_KEY_INPUT).trim();
  } else if (apiKeyEnv) {
    apiKeyValue = resolveProfileAPIKey({ apiKeyEnv }, envOptions);
  }

  if (!urlCheck.isLoopback && !apiKeyValue) {
    errOut.write(
      `Error: API key is required for remote URL (${displayURL(urlCheck.url)}). Pass --api-key-stdin, set AGY_PRO_API_KEY_INPUT, or export ${displayEnvName(apiKeyEnv)}.\n`
    );
    return { exitCode: 2 };
  }

  if (apiKeyValue && !apiKeyEnv) {
    apiKeyEnv = defaultEnvForProvider(profileCheck.value);
  }

  if (urlCheck.isLoopback && !apiKeyValue && !flags.apiKeyEnv) {
    apiKeyEnv = '';
  }

  let models = [];
  if (flags.models) {
    try {
      models = parseGatewayModels(flags.models);
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return { exitCode: 2 };
    }
  } else if (hint) {
    models = getModelPresets(hint.name).map((m) => m.id);
  }

  if (models.length === 0) {
    errOut.write('Error: No models provided. Pass --models "<id1>,<id2>".\n');
    return { exitCode: 2 };
  }

  const headersStrip = stripAuthorizationHeaders(flags.headers || '');
  const headers = headersStrip.text;
  if (headersStrip.removedAuthorization) {
    output.write(
      'Removed credential header(s) from --headers; the API key already travels in the Authorization header.\n'
    );
  }

  if (flags.probe) {
    const probeRes = await probeEndpoint({
      url: urlCheck.url,
      apiKey: apiKeyValue,
      wireProtocol: wireCheck.value,
      model: models[0],
      caCert: flags.caCert,
      headers,
      transport
    });
    if (!probeRes.ok) {
      errOut.write(`Error: Endpoint probe failed: ${probeRes.error}\n`);
      return { exitCode: 1 };
    }
    output.write(`Probe succeeded (${probeRes.latencyMs}ms, HTTP ${probeRes.status}).\n`);
  }

  const config = loadGatewayConfig(envOptions);
  config.profiles[profileCheck.value] = {
    provider,
    url: urlCheck.url,
    wireProtocol: wireCheck.value,
    apiKeyEnv,
    models,
    caCert: flags.caCert || '',
    headers,
    updatedAt: new Date().toISOString()
  };
  if (!config.activeProfile || flags.activate) {
    config.activeProfile = profileCheck.value;
  }
  saveGatewayConfig(config, envOptions);

  if (apiKeyValue && apiKeyEnv) {
    persistEnvMap({ [apiKeyEnv]: apiKeyValue }, envOptions);
  }

  if (wireCheck.value === 'openai') {
    output.write(`${OPENAI_TOOL_WARNING}\n`);
  }

  if (flags.activate) {
    activateGateway(profileCheck.value, { ...envOptions, apiKey: apiKeyValue });
    output.write(`${XOR_MODELS_WARNING}\n`);
  }

  output.write(
    `Saved profile "${displayConfigName(profileCheck.value)}" (${displayURL(urlCheck.url)}, wire=${wireCheck.value}, models=${formatGatewayModels(models)}, key=${apiKeyValue ? maskSecret(apiKeyValue) : '(none - loopback)'}).\n`
  );
  return { exitCode: 0, result: { profile: profileCheck.value, active: Boolean(flags.activate) } };
}

/**
 * Interactive 7-step setup wizard flow.
 * @param {object} deps
 * @returns {Promise<{ exitCode: number }>}
 */
async function runSevenStepWizard(deps) {
  const { input, output, envOptions, transport } = deps;
  const prompter = createLinePrompter(input, output);
  const providers = knownProviders({ includeCustom: true });

  output.write('\n===========================================================\n');
  output.write('  agy-pro Enterprise Gateway Setup Wizard\n');
  output.write('===========================================================\n');
  output.write('Configures AGY_LLM_GATEWAY_* profiles in ~/.agy-pro/gateway.json.\n');
  output.write('API keys are never stored inside gateway.json.\n\n');

  // Step 1: Provider preset / custom
  output.write('Step 1/7 — Choose a provider:\n');
  providers.forEach((p, idx) => {
    const aliasText = p.aliases.length > 0 ? ` [aliases: ${p.aliases.join(', ')}]` : '';
    output.write(`  ${idx + 1}) ${p.label}${aliasText}\n`);
  });

  let selectedHint = null;
  let providerName = '';
  while (!providerName) {
    const { value, eof } = await prompter.ask(`Select provider (1-${providers.length} or name)`, '1');
    if (eof && !value) {
      output.write('Setup aborted (EOF).\n');
      return { exitCode: 1 };
    }
    const num = Number(value);
    if (Number.isInteger(num) && num >= 1 && num <= providers.length) {
      selectedHint = providers[num - 1];
      providerName = selectedHint.name;
    } else {
      const hint = lookupProviderHint(value);
      if (hint) {
        selectedHint = hint;
        providerName = hint.name;
      } else {
        const clean = canonicalProviderName(value);
        const check = validateProfileName(clean);
        if (!check.valid) {
          output.write(`  Invalid provider: ${check.error}\n`);
          continue;
        }
        providerName = clean;
      }
    }
  }

  let profileName = '';
  while (!profileName) {
    const defaultProfile = providerName === 'custom' ? 'my-gateway' : providerName;
    const { value } = await prompter.ask('Profile name', defaultProfile);
    const check = validateProfileName(value);
    if (!check.valid) {
      output.write(`  ${check.error}\n`);
      continue;
    }
    profileName = check.value;
  }

  // Step 2: Base URL
  output.write('\nStep 2/7 — Gateway Base URL:\n');
  const defaultURL = selectedHint ? selectedHint.baseURL : '';
  let baseURL = '';
  let isLoopback = false;
  while (!baseURL) {
    const { value, eof } = await prompter.ask('Base URL (https://... or loopback http://...)', defaultURL);
    if (eof && !value) {
      output.write('Setup aborted (EOF).\n');
      return { exitCode: 1 };
    }
    const urlCheck = validateGatewayURL(value);
    if (!urlCheck.valid) {
      output.write(`  ${urlCheck.error}\n`);
      continue;
    }
    baseURL = urlCheck.url;
    isLoopback = urlCheck.isLoopback;
  }

  // Step 3: Wire protocol
  output.write('\nStep 3/7 — Wire Protocol:\n');
  output.write('  1) openai  (OpenAI Chat Completions /v1/chat/completions)\n');
  output.write('  2) genai   (Google Gemini native API :generateContent)\n');
  const defaultWire = selectedHint ? selectedHint.wireProtocol : 'openai';
  const defaultWireChoice = defaultWire === 'genai' ? '2' : '1';
  let wireProtocol = '';
  while (!wireProtocol) {
    const { value } = await prompter.ask('Wire protocol (1=openai, 2=genai)', defaultWireChoice);
    if (value === '1' || value.toLowerCase() === 'openai') {
      wireProtocol = 'openai';
    } else if (value === '2' || value.toLowerCase() === 'genai') {
      wireProtocol = 'genai';
    } else {
      output.write('  Please enter 1 (openai) or 2 (genai).\n');
    }
  }
  if (wireProtocol === 'openai') {
    output.write(`  ${OPENAI_TOOL_WARNING}\n`);
  }

  // Step 4: API Key / env name
  output.write('\nStep 4/7 — API Key & Environment Variable:\n');
  if (selectedHint && selectedHint.keyURL) {
    output.write(`  Get a key at: ${stripControlAndFormat(selectedHint.keyURL)}\n`);
  }
  let apiKeyEnv = selectedHint && selectedHint.apiKeyEnv ? selectedHint.apiKeyEnv : '';
  if (!apiKeyEnv && !isLoopback) {
    apiKeyEnv = defaultEnvForProvider(profileName);
  }
  let existingKey = apiKeyEnv ? resolveProfileAPIKey({ apiKeyEnv }, envOptions) : '';
  let apiKeyValue = '';

  if (isLoopback) {
    output.write('  Loopback URL detected: API key is optional (press Enter to skip).\n');
  }
  if (apiKeyEnv && existingKey) {
    output.write(`  Detected existing ${displayEnvName(apiKeyEnv)}: ${maskSecret(existingKey)}\n`);
  }

  const keyPrompt = isLoopback
    ? 'API Key (optional on loopback, press Enter to skip)'
    : existingKey
      ? `API Key (Enter to keep ${displayEnvName(apiKeyEnv)}, or paste key/env name)`
      : `API Key (stored in ${displayEnvName(apiKeyEnv || defaultEnvForProvider(profileName))})`;

  const enteredKey = await promptSecret({
    prompt: keyPrompt,
    defaultValue: '',
    input,
    output
  });

  if (enteredKey) {
    const envShaped =
      validateEnvName(enteredKey).valid &&
      /^[A-Za-z_][A-Za-z0-9_]*$/.test(enteredKey) &&
      (!looksLikeAPIKey(enteredKey) || /^[A-Z][A-Z0-9_]*$/.test(enteredKey));
    const fromStore = envShaped ? resolveProfileAPIKey({ apiKeyEnv: enteredKey }, envOptions) : '';
    if (fromStore) {
      apiKeyEnv = enteredKey;
      apiKeyValue = fromStore;
      output.write(`  Loaded key from existing env var ${displayEnvName(apiKeyEnv)}: ${maskSecret(apiKeyValue)}\n`);
    } else {
      if (!apiKeyEnv) {
        apiKeyEnv = defaultEnvForProvider(profileName);
      }
      apiKeyValue = enteredKey;
      output.write(`  API Key recorded (${displayEnvName(apiKeyEnv)}): ${maskSecret(apiKeyValue)}\n`);
    }
  } else if (existingKey) {
    apiKeyValue = existingKey;
  } else if (!isLoopback) {
    output.write(`  Warning: No key entered yet; export ${displayEnvName(apiKeyEnv)} before activating.\n`);
  } else {
    apiKeyEnv = '';
  }

  const { value: caCert } = await prompter.ask('Custom CA cert path (optional, Enter to skip)', '');
  const { value: rawHeaders } = await prompter.ask('Extra HTTP headers "K: V" (optional, Enter to skip)', '');
  const headersStrip = stripAuthorizationHeaders(rawHeaders);
  const headers = headersStrip.text;
  if (headersStrip.removedAuthorization) {
    output.write(
      '  Removed credential header(s) from the extra headers; the API key already travels in the Authorization header.\n'
    );
  }

  // Step 5: Model selection
  output.write('\nStep 5/7 — Model Selection (AGY_LLM_GATEWAY_MODELS):\n');
  let remoteModels = [];
  try {
    remoteModels = await fetchRemoteModels(baseURL, apiKeyValue, {
      wireProtocol,
      caCert,
      headers,
      transport
    });
  } catch (_) {
    remoteModels = [];
  }

  const candidates = buildModelCandidates(providerName, remoteModels);
  let selectedModels = [];
  while (selectedModels.length === 0) {
    if (candidates.length > 0) {
      output.write('  Available models:\n');
      candidates.forEach((m, idx) => {
        const rec = m.recommended ? ' [Recommended]' : '';
        const desc = m.description ? ` (${stripControlAndFormat(m.description)})` : '';
        output.write(`    ${idx + 1}) ${displayConfigName(m.id)}${desc}${rec}\n`);
      });
      output.write('  Enter numbers (e.g. 1,2), "all", or comma-separated model IDs:\n');
      const { value, eof } = await prompter.ask('Models', '1');
      if (eof && !value) {
        output.write('Setup aborted (EOF).\n');
        return { exitCode: 1 };
      }
      try {
        selectedModels = resolveModelSelectionInput(value, candidates);
      } catch (err) {
        output.write(`  ${err.message}\n`);
      }
    } else {
      const { value, eof } = await prompter.ask('Comma-separated Model IDs (e.g. llama3.2,qwen2.5)', '');
      if (eof && !value) {
        output.write('Setup aborted (EOF).\n');
        return { exitCode: 1 };
      }
      try {
        selectedModels = parseGatewayModels(value);
      } catch (err) {
        output.write(`  ${err.message}\n`);
      }
    }
    if (selectedModels.length === 0) {
      output.write('  Please select at least one valid model ID.\n');
    }
  }

  // Step 6: Optional live endpoint probe
  output.write('\nStep 6/7 — Live Endpoint Probe:\n');
  let probeDone = false;
  while (!probeDone) {
    const { value: runProbeAns } = await prompter.ask(
      `Test connection to ${displayURL(baseURL)} with "${displayConfigName(selectedModels[0])}"? [Y/n]`,
      'Y'
    );
    if (runProbeAns.toLowerCase() === 'n' || runProbeAns.toLowerCase() === 'no') {
      break;
    }
    output.write(`  Probing ${displayConfigName(selectedModels[0])} (${wireProtocol})...\n`);
    const probeRes = await probeEndpoint({
      url: baseURL,
      apiKey: apiKeyValue,
      wireProtocol,
      model: selectedModels[0],
      caCert,
      headers,
      transport
    });
    if (probeRes.ok) {
      output.write(`  Connection succeeded (${probeRes.latencyMs}ms, HTTP ${probeRes.status}).\n`);
      probeDone = true;
      break;
    }

    output.write(`  Connection test failed: ${stripControlAndFormat(String(probeRes.error || ''))}\n`);
    output.write('  Options: 1) Save anyway  2) Retry  3) Edit Base URL / API Key  4) Abort\n');
    const { value: failChoice } = await prompter.ask('Choice (1-4)', '1');
    if (failChoice === '1') {
      probeDone = true;
    } else if (failChoice === '2') {
      continue;
    } else if (failChoice === '3') {
      const { value: nextUrl } = await prompter.ask('Base URL', displayURL(baseURL));
      if (nextUrl && nextUrl !== displayURL(baseURL)) {
        const uCheck = validateGatewayURL(nextUrl);
        if (uCheck.valid) {
          baseURL = uCheck.url;
          isLoopback = uCheck.isLoopback;
        } else {
          output.write(`  ${uCheck.error}\n`);
        }
      }
      if (!isLoopback && !apiKeyEnv) {
        const suggestedEnv = defaultEnvForProvider(profileName);
        while (!apiKeyEnv) {
          const { value: enteredEnv } = await prompter.ask(
            'API Key env variable name (required for remote URL)',
            suggestedEnv
          );
          const envCheck = validateEnvName(enteredEnv);
          if (!envCheck.valid) {
            output.write(`  ${envCheck.error}\n`);
            continue;
          }
          apiKeyEnv = envCheck.value;
        }
      }
      const keyEditPrompt =
        !isLoopback && !apiKeyValue
          ? `API Key (required for remote URL, stored in ${displayEnvName(apiKeyEnv)})`
          : 'API Key (Enter to keep current)';
      const nextKey = await promptSecret({
        prompt: keyEditPrompt,
        defaultValue: '',
        input,
        output
      });
      if (nextKey) {
        apiKeyValue = nextKey;
        if (!apiKeyEnv) apiKeyEnv = defaultEnvForProvider(profileName);
      } else if (!isLoopback && !apiKeyValue) {
        const existingFromEnv = resolveProfileAPIKey({ apiKeyEnv }, envOptions);
        if (existingFromEnv) {
          apiKeyValue = existingFromEnv;
        } else {
          output.write(`  Warning: Remote endpoint requires an API key in ${displayEnvName(apiKeyEnv)}.\n`);
        }
      }
    } else {
      output.write('[CANCELLED] nothing written\n');
      return { exitCode: 0 };
    }
  }

  // Step 7: Diff preview + XOR warning + save & activate
  const formattedModels = formatGatewayModels(selectedModels);
  output.write('\nStep 7/7 — Review & Apply:\n');
  output.write('-----------------------------------------------------------\n');
  output.write(
    `  Profile         : ${displayConfigName(profileName)} (provider: ${displayConfigName(providerName)})\n`
  );
  output.write(`  Config File     : ${getGatewayConfigPath(envOptions)} (0600, atomic)\n`);
  output.write(`  AGY_LLM_GATEWAY_URL           = ${displayURL(baseURL)}\n`);
  output.write(`  AGY_LLM_GATEWAY_WIRE_PROTOCOL = ${wireProtocol}\n`);
  output.write(`  AGY_LLM_GATEWAY_MODELS        = ${formattedModels}\n`);
  output.write(
    `  AGY_LLM_GATEWAY_API_KEY       = ${apiKeyValue ? maskSecret(apiKeyValue) : '(none - loopback)'}\n`
  );
  if (caCert) output.write(`  AGY_LLM_GATEWAY_CA_CERT       = ${stripControlAndFormat(caCert)}\n`);
  if (headers) output.write(`  AGY_LLM_GATEWAY_HEADERS       = ${stripControlAndFormat(maskHeaders(headers))}\n`);
  output.write('-----------------------------------------------------------\n');
  output.write(`${XOR_MODELS_WARNING}\n\n`);

  const { value: applyAns } = await prompter.ask('Save profile and activate gateway? [Y/n]', 'Y');
  if (applyAns.toLowerCase() === 'n' || applyAns.toLowerCase() === 'no') {
    output.write('[CANCELLED] nothing written\n');
    return { exitCode: 0 };
  }

  if ((!isLoopback || apiKeyValue) && !apiKeyEnv) {
    apiKeyEnv = defaultEnvForProvider(profileName);
  }

  const config = loadGatewayConfig(envOptions);
  config.profiles[profileName] = {
    provider: providerName,
    url: baseURL,
    wireProtocol,
    apiKeyEnv,
    models: selectedModels,
    caCert,
    headers,
    updatedAt: new Date().toISOString()
  };
  config.activeProfile = profileName;
  saveGatewayConfig(config, envOptions);

  if (apiKeyValue && apiKeyEnv) {
    persistEnvMap({ [apiKeyEnv]: apiKeyValue }, envOptions);
  }

  if (!isLoopback && !apiKeyValue) {
    output.write(
      `\nProfile "${displayConfigName(profileName)}" saved, but ${displayEnvName(apiKeyEnv)} is not set yet. ` +
        `Export it and run \`agy-pro gateway on ${displayConfigName(profileName)}\`.\n`
    );
    return { exitCode: 0 };
  }

  activateGateway(profileName, { ...envOptions, apiKey: apiKeyValue });
  output.write(`\nGateway profile "${displayConfigName(profileName)}" saved and activated.\n`);
  output.write(`${RELOAD_HINT}\n`);
  return { exitCode: 0 };
}

/**
 * Hint printed after the persistent environment changed. `--reveal` is required so the
 * emitted exports carry the real key instead of a masked placeholder.
 * @type {string}
 */
const RELOAD_HINT =
  'Open a new terminal before launching `agy`, or reload now: ' +
  'POSIX `eval "$(agy-pro env --reveal)"` | PowerShell `agy-pro env --reveal | Invoke-Expression`.';

/**
 * Resolves the legacy `providers.json` path (inside the agy-pro home directory).
 * @returns {string}
 */
function getLegacyProvidersPath() {
  return path.join(getAgyProHome(), 'providers.json');
}

/**
 * Extracts importable provider entries from a parsed legacy `providers.json`
 * (`{ providers: {...} }`, a bare map, or an array of named entries).
 * @param {*} parsed
 * @returns {Array<{ name: string, entry: object }>}
 */
function collectLegacyEntries(parsed) {
  if (!parsed || typeof parsed !== 'object') return [];
  const source = parsed.providers && typeof parsed.providers === 'object' ? parsed.providers : parsed;
  if (Array.isArray(source)) {
    return source
      .filter((e) => e && typeof e === 'object')
      .map((e) => ({ name: String(e.name || e.provider || '').trim(), entry: e }));
  }
  return Object.entries(source)
    .filter(([, e]) => e && typeof e === 'object' && !Array.isArray(e))
    .map(([name, e]) => ({ name: String(name).trim(), entry: e }));
}

/**
 * Normalizes legacy `providers.json` model entries into model ID strings.
 * Legacy entries store models as an array of `{ id: "..." }` objects (Go
 * `providers.Provider.Models []Model`), while hand-written files may use plain
 * strings; both forms collapse to IDs here. Go's `Enabled *bool` treats nil as
 * enabled and an explicit `false` as disabled, so disabled models are skipped
 * rather than imported as active. Empty or invalid entries drop out.
 * @param {*} models
 * @returns {string[]}
 */
function normalizeLegacyModelIds(models) {
  if (!Array.isArray(models)) return [];
  const out = [];
  for (const item of models) {
    if (item === null || item === undefined) continue;
    let id = '';
    if (typeof item === 'object') {
      if (item.enabled === false) continue;
      id = String(item.id || item.name || item.model || '');
    } else {
      id = String(item);
    }
    id = id.trim();
    if (id) out.push(id);
  }
  return out;
}

/**
 * Strips ANSI escape sequences and control characters from an untrusted legacy
 * field before it reaches gateway.json or the Hub picker output.
 * @param {*} value
 * @returns {string}
 */
function sanitizeImportedField(value) {
  return stripControlAndFormat(String(value || '')).trim();
}

/**
 * Normalizes an untrusted header name for comparison only. Whole ANSI escape sequences are removed
 * first, then every control/zero-width/format rune and all whitespace, then NFKC so fullwidth
 * homoglyphs (`Ａｕｔｈｏｒｉｚａｔｉｏｎ`, `Ａuthorization`) fold onto ASCII. The character class
 * matches `validator.stripControlAndFormat` plus whitespace and the remaining format runes, so
 * `Author\x1b[31mization`, `Authoriz\nation`, `auth\u200borization`, and `Authoriz ation` all
 * collapse onto `authorization` before the credential check.
 *
 * The NFKC pass lives here, on the comparison copy only. Kept output is never NFKC-normalized:
 * doing that turned an inert fullwidth-colon line (`Authorization： Bearer sk-…`) into a live
 * ASCII `Authorization` header that was then persisted and transmitted.
 * @param {string} rawName
 * @returns {string}
 */
function normalizeHeaderName(rawName) {
  return String(rawName || '')
    .replace(ANSI_ESCAPE_RE, '')
    .replace(HEADER_NAME_JUNK_RE, '')
    .normalize('NFKC')
    .toLowerCase();
}

/**
 * Reports whether a token opens a header (`name:`) and yields the normalized name.
 * The colon may be fullwidth (U+FF1A); NFKC folds it for the comparison copy only.
 * @param {string} token
 * @returns {{ opens: boolean, name: string, nameText: string }}
 */
function readHeaderName(token) {
  const text = String(token || '');
  const idx = text.indexOf(':');
  if (idx > 0) {
    return { opens: true, name: normalizeHeaderName(text.slice(0, idx)), nameText: text.slice(0, idx) };
  }
  const nfkc = text.normalize('NFKC');
  const nidx = nfkc.indexOf(':');
  if (nidx > 0) {
    return { opens: true, name: normalizeHeaderName(nfkc.slice(0, nidx)), nameText: nfkc.slice(0, nidx) };
  }
  return { opens: false, name: '', nameText: '' };
}

/**
 * Reports whether a normalized header name carries credentials.
 * @param {string} name
 * @returns {boolean}
 */
function isCredentialHeaderName(name) {
  return Boolean(name) && CREDENTIAL_HEADER_NAMES.has(name);
}

/**
 * Splits a header string into comma/newline separated segments that remember where they came from,
 * so a kept group can be re-emitted with its original separators.
 * @param {string} flat
 * @returns {Array<{ line: number, text: string }>}
 */
function tokenizeHeaderSegments(flat) {
  const segments = [];
  const lines = String(flat || '').split(/\r\n|\n|\r/);
  lines.forEach((line, lineIndex) => {
    for (const part of line.split(',')) {
      const text = part.trim();
      if (text) segments.push({ line: lineIndex, text });
    }
  });
  return segments;
}

/**
 * Joins a group's segments back into one header line. Same-line segments keep their commas
 * (benign values such as `X-List: a,b,c` survive verbatim); a group that spans lines is folded
 * with a space, which is what a continued value looks like.
 * @param {Array<{ line: number, text: string }>} segments
 * @returns {string}
 */
function joinHeaderGroup(segments) {
  const sameLine = segments.every((s) => s.line === segments[0].line);
  return segments.map((s) => s.text).join(sameLine ? ',' : ' ');
}

/**
 * Removes credential-bearing headers from a "K: V" header string so API keys never land in
 * gateway.json; auth must flow through the profile's env var.
 *
 * Whole ANSI escape sequences are removed first, then the string is tokenized on newlines and
 * commas. Fragments that do not open a header are folded back into the current pair, so a dropped
 * credential takes its entire value with it (`X-Api-Key: p1,p2` loses `p2` as well).
 *
 * Names are reassembled across an arbitrary number of fragments before the comparison, so
 * `Auth` + `oriz` + `ation: Bearer …`, `Authoriz ation: Bearer …`, and a colon-leading fragment
 * (`: Bearer …`) all resolve to a credential and are dropped. A group that never opens a header is
 * a value-only fragment and is dropped too, since it cannot be a valid header. Comparison always
 * uses a normalized copy: ANSI, control characters, zero-width/format runes, and whitespace are
 * removed from the name and NFKC is applied to that copy only. Kept lines keep their original
 * spelling and commas (`X-List: a,b,c` stays intact), and no NFKC pass runs on them.
 * @param {string} headers
 * @returns {{ text: string, removedAuthorization: boolean }}
 */
function stripAuthorizationHeaders(headers) {
  const raw = String(headers || '');
  if (!raw.trim()) return { text: '', removedAuthorization: false };

  const segments = tokenizeHeaderSegments(raw.replace(ANSI_ESCAPE_RE, ''));
  const groups = [];
  let removedAuthorization = false;

  let i = 0;
  while (i < segments.length) {
    const head = readHeaderName(segments[i].text);
    if (head.opens) {
      groups.push({ segments: [segments[i]], credential: isCredentialHeaderName(head.name) });
      i += 1;
      continue;
    }

    // Colon-less fragment(s): collect the run, then decide what it belongs to. A run whose joined
    // spelling (with the header that follows it, or up to its own colon) is a credential name is
    // the split name of a credential, so the whole group is dropped.
    const run = [segments[i]];
    let j = i + 1;
    while (j < segments.length && !readHeaderName(segments[j].text).opens) {
      run.push(segments[j]);
      j += 1;
    }
    const runText = run.map((s) => s.text).join('');
    const nextHead = j < segments.length ? readHeaderName(segments[j].text) : null;
    const candidate = nextHead ? runText + nextHead.nameText : runText.split(':')[0];
    if (isCredentialHeaderName(normalizeHeaderName(candidate))) {
      groups.push({ segments: nextHead ? [...run, segments[j]] : run, credential: true });
      removedAuthorization = true;
      i = nextHead ? j + 1 : j;
      continue;
    }

    if (runText.startsWith(':')) {
      // A fragment that opens with a colon cannot be a header (`parseCustomHeaders` skips index 0)
      // and is the tail of a split credential name (`: Bearer …`). Dropping it keeps the credential
      // value out of gateway.json; folding it into a preceding benign pair would persist it.
      removedAuthorization = true;
    } else if (groups.length > 0) {
      // Continuation of the previous pair's value; if that pair was a credential it is dropped
      // along with the value it carried.
      groups[groups.length - 1].segments.push(...run);
    } else {
      // No pair to attach to: keep the fragment as its own group so the caller still sees what it
      // typed. `parseCustomHeaders` ignores colon-less parts, so nothing is transmitted.
      groups.push({ segments: run, credential: false });
    }
    i = j;
  }

  const kept = [];
  for (const group of groups) {
    if (group.credential) {
      removedAuthorization = true;
      continue;
    }
    const clean = joinHeaderGroup(group.segments).replace(HEADER_OUTPUT_JUNK_RE, '').trim();
    if (!clean) continue;
    kept.push(clean);
  }
  return { text: kept.join('\n'), removedAuthorization };
}

/**
 * Reports whether an env name is managed by agy-pro for a given profile: the name agy-pro generates
 * for that profile (`defaultEnvForProvider`), or a name from the provider catalog (`presets.js`).
 *
 * An `AGY_PRO_GATEWAY_*` name that belongs to a *different* profile is not managed: a legacy import
 * or a hand-edited gateway.json could otherwise point a profile at a sibling profile's generated
 * name and have that sibling's LLM key forwarded without consent. This is the name-only test;
 * callers that also have the profile URL should use `isManagedProfileEnv` so a catalog name cannot
 * be pointed at a foreign host.
 * @param {string} envName
 * @param {string} [profileName] Name the name must have been generated for; omit to accept any
 *   `AGY_PRO_GATEWAY_*` name (used where no profile is in hand yet).
 * @returns {boolean}
 */
function isManagedEnvName(envName, profileName) {
  const name = String(envName || '').trim();
  if (!name) return true;
  if (KNOWN_PROVIDER_ENV_NAMES.has(name)) return true;
  if (!name.startsWith(OWNED_ENV_PREFIX)) return false;
  if (!profileName) return true;
  return name === defaultEnvForProvider(profileName);
}

/**
 * Extracts a lowercase hostname from a URL, or '' when it cannot be parsed.
 * @param {string} rawUrl
 * @returns {string}
 */
function urlHostname(rawUrl) {
  const raw = String(rawUrl || '').trim();
  if (!raw) return '';
  try {
    return new URL(raw).hostname.trim().toLowerCase().replace(/^\[|\]$/g, '');
  } catch (_) {
    return '';
  }
}

/**
 * Reports whether an `AGY_PRO_GATEWAY_*` name is contested inside the loaded profile set: some
 * other profile either references the name as its `apiKeyEnv` or derives the same name from its
 * own profile name. `defaultEnvForProvider` folds case and separators, so `work-gw`, `work.gw`
 * and `WORK-GW` all derive `AGY_PRO_GATEWAY_WORK_GW_API_KEY`; only one of them can own it.
 * Missing/unreadable config counts as contested: uniqueness cannot be proven there, which is the
 * fail-closed direction the consent gate needs.
 * @param {string} envName
 * @param {string} profileName Name of the profile being evaluated (excluded from the scan).
 * @param {{ profiles?: Record<string, object> } | null} config
 * @returns {boolean}
 */
function hasOwnedEnvRival(envName, profileName, config) {
  const target = String(envName || '')
    .replace(/\ufeff/g, '')
    .trim()
    .toUpperCase();
  if (!target) return true;
  if (!config || !config.profiles || typeof config.profiles !== 'object') return true;

  const self = String(profileName || '').trim();
  return Object.keys(config.profiles).some((other) => {
    if (other === self) return false;
    const candidate = config.profiles[other] || {};
    // Windows env var names are case-insensitive, so a differently-cased reference is the same
    // variable and must count as a rival.
    const referenced = String(candidate.apiKeyEnv || '')
      .replace(/\ufeff/g, '')
      .trim()
      .toUpperCase();
    if (referenced === target) return true;
    const derived = String(defaultEnvForProvider(other) || '')
      .trim()
      .toUpperCase();
    return derived === target;
  });
}

/**
 * Reports whether a saved profile's `apiKeyEnv` may be resolved without asking the user.
 *
 * A loopback URL passes for any name: the value never leaves the machine, so the spec's
 * "loopback needs no confirmation" rule applies regardless of which variable holds the key.
 * Otherwise the name must be agy-pro's generated name for *this* profile, or a provider-catalog
 * name AND the profile URL must target that provider's host. A catalog name pointed at another
 * host is a confused deputy, and a sibling profile's generated name is a cross-profile binding;
 * both need the same consent as an arbitrary ambient variable. When the catalog entry carries no
 * host, any non-loopback URL is treated as a mismatch.
 * @param {{ apiKeyEnv?: string, url?: string }} profile
 * @param {string} [profileName] Name the generated env name must match. When omitted or
 *   unresolvable (several profiles share the name), an `AGY_PRO_GATEWAY_*` name cannot prove
 *   ownership, so it is treated as unmanaged (fail-closed) and the caller asks for consent.
 * @param {{ profiles?: Record<string, object> } | null} [config] The profile set the name must be
 *   unique in. An exact name match is not enough: the derivation is many-to-one, so a colliding
 *   sibling (`work.gw` vs `work-gw`) could otherwise borrow the other profile's key silently.
 *   Without a readable config the owned name is treated as contested (fail-closed).
 * @returns {boolean}
 */
function isManagedProfileEnv(profile, profileName, config) {
  const name = String((profile && profile.apiKeyEnv) || '').trim();
  if (!name) return true;

  const url = String((profile && profile.url) || '').trim();
  if (isLoopbackURL(url)) return true;

  if (name.startsWith(OWNED_ENV_PREFIX)) {
    if (!profileName) return false;
    if (name !== defaultEnvForProvider(profileName)) return false;
    // Ownership also requires derivation uniqueness: a sibling whose name normalizes to the same
    // env var (or that references it) can make the same exact-match claim, so neither profile can
    // prove the key is its own. Both sides then go through the consent gate.
    return !hasOwnedEnvRival(name, profileName, config);
  }
  if (!KNOWN_PROVIDER_ENV_NAMES.has(name)) return false;

  const catalogHost = urlHostname((CATALOG_PROVIDERS_BY_ENV.get(name) || {}).baseURL);
  if (!catalogHost) return false;
  return urlHostname(url) === catalogHost;
}

/**
 * Renders an env name for a terminal message. A tampered gateway.json can hold a pasted secret in
 * `apiKeyEnv`; that must never be echoed verbatim, so secret-shaped values are masked.
 * @param {string} envName
 * @returns {string}
 */
function displayEnvName(envName) {
  const clean = stripControlAndFormat(String(envName || '').trim());
  if (!clean) return '(unset)';
  return looksLikeAPIKey(clean) ? maskSecret(clean) : clean;
}

/**
 * Builds the consent prompt for a profile that would forward an unmanaged env var to its URL.
 * @param {string} envName
 * @param {string} url
 * @returns {string}
 */
function formatUnmanagedEnvPrompt(envName, url) {
  return (
    `Profile references unmanaged env var ${displayEnvName(envName)}; ` +
    `its value will be sent to ${displayURL(url)}. Continue? [y/N]`
  );
}

/**
 * Renders a URL for a terminal message. `maskBaseURL` hides embedded credentials but passes ANSI
 * escapes and control characters through, so a tampered profile URL could restyle or rewrite the
 * consent prompt and spoof the decision. Stripping them first closes that surface.
 * @param {string} rawUrl
 * @returns {string}
 */
function displayURL(rawUrl) {
  return stripControlAndFormat(maskBaseURL(rawUrl));
}

/**
 * Resolves which saved profile owns an `apiKeyEnv` reference, so a caller that only holds the
 * profile object still gets the cross-profile binding check. Returns '' when the name is
 * referenced by zero or several profiles (ambiguous: the caller must pass the name explicitly).
 * @param {{ apiKeyEnv?: string }} profile
 * @param {object} [envOptions]
 * @returns {string}
 */
function findProfileNameForEnv(profile, envOptions) {
  const target = String((profile && profile.apiKeyEnv) || '').trim();
  if (!target) return '';
  let config;
  try {
    config = loadGatewayConfig(envOptions || {});
  } catch (_) {
    return '';
  }
  const owners = Object.keys(config.profiles).filter(
    (name) => String(config.profiles[name].apiKeyEnv || '').trim() === target
  );
  return owners.length === 1 ? owners[0] : '';
}

/**
 * Loads the profile set for the consent gate. A read/parse failure returns null, which the gate
 * treats as "ownership unprovable" (consent) rather than throwing out of an activation path.
 * @param {object} [envOptions]
 * @returns {{ profiles?: Record<string, object> } | null}
 */
function loadConsentConfig(envOptions) {
  try {
    return loadGatewayConfig(envOptions || {});
  } catch (_) {
    return null;
  }
}

/**
 * Consent gate for every path that resolves a saved profile's API key — activation, remote model
 * listing, and probing alike. One policy everywhere: a loopback URL passes for any name; otherwise
 * the name must be agy-pro's generated name for *this* profile, or a provider-catalog name whose
 * URL targets that provider's host. A catalog name pointed at a foreign host is a confused deputy,
 * and a sibling profile's generated name is a cross-profile binding; both get the same treatment as
 * an arbitrary ambient variable.
 *
 * When the caller does not pass `profileName`, the owning profile is looked up from the saved config
 * so the binding check still runs (the CLI paths hold only the profile object). An ambiguous name
 * referenced by several profiles stays unbound, which is the fail-closed direction: a shared
 * `AGY_PRO_GATEWAY_*` name cannot prove ownership, so it needs consent. An explicit `profileName`
 * does not skip the check either: the same config is loaded and must show the name uncontested
 * (see `hasOwnedEnvRival`), because the derivation folds `work-gw` and `work.gw` onto one variable.
 *
 * An unmanaged name needs an explicit `y` (default N) from an interactive prompter; a caller that
 * supplies `allowUnmanagedEnv` (the `--allow-unmanaged-env` flag) opts out explicitly.
 * Non-interactive callers without that flag get `false`, and the caller refuses.
 * @param {{ apiKeyEnv?: string, url?: string }} profile
 * @param {{ prompter?: { ask: Function }, input?: object, output?: object, isTTY?: boolean, allowUnmanagedEnv?: boolean, profileName?: string, envOptions?: object }} [options]
 * @returns {Promise<boolean>} true when the value may be resolved.
 */
async function requestUnmanagedEnvConsent(profile, options = {}) {
  if (options.allowUnmanagedEnv) return true;
  const profileName = options.profileName || findProfileNameForEnv(profile, options.envOptions);
  const config = loadConsentConfig(options.envOptions);
  if (isManagedProfileEnv(profile, profileName, config)) return true;

  let prompter = options.prompter;
  if (!prompter) {
    if (!options.isTTY || !options.input || !options.output) return false;
    prompter = createLinePrompter(options.input, options.output);
  }
  const envName = String(profile.apiKeyEnv || '').trim();
  const { value, eof } = await prompter.ask(formatUnmanagedEnvPrompt(envName, profile.url), 'N');
  if (eof) return false;
  return /^y/i.test(String(value || '').trim());
}

/**
 * Formats the refusal message for an unmanaged env var on a non-interactive path.
 * @param {string} envName
 * @param {string} url
 * @returns {string}
 */
function formatUnmanagedEnvRefusal(envName, url) {
  return (
    `Profile references unmanaged env var ${displayEnvName(envName)}; ` +
    `refusing to send its value to ${displayURL(url)}. ` +
    'Re-run setup, approve the prompt, or pass --allow-unmanaged-env.'
  );
}

/**
 * Lists legacy `providers.json` entry names that collide with existing saved profiles.
 * Returns an empty list when the legacy file cannot be read or parsed.
 * @param {object} envOptions
 * @returns {string[]}
 */
function listLegacyImportConflicts(envOptions) {
  let parsed;
  try {
    const raw = fs.readFileSync(getLegacyProvidersPath(), 'utf8').replace(/^\ufeff/, '');
    parsed = JSON.parse(raw);
  } catch (_) {
    return [];
  }
  const config = loadGatewayConfig(envOptions);
  const conflicts = [];
  for (const { name } of collectLegacyEntries(parsed)) {
    const check = validateProfileName(name);
    if (check.valid && Object.prototype.hasOwnProperty.call(config.profiles, check.value)) {
      conflicts.push(check.value);
    }
  }
  return conflicts;
}

/**
 * Imports legacy `~/.agy-pro/providers.json` entries as saved profiles (never activates).
 * Existing profiles with the same name are kept unless `options.overwrite` is set.
 * @param {object} envOptions
 * @param {{ overwrite?: boolean }} [options]
 * @returns {{ imported: string[], skipped: string[], notices: string[] }}
 */
function importLegacyProviders(envOptions, options = {}) {
  let parsed;
  try {
    const raw = fs.readFileSync(getLegacyProvidersPath(), 'utf8').replace(/^\ufeff/, '');
    parsed = JSON.parse(raw);
  } catch (err) {
    const code = err && err.code ? String(err.code) : '';
    if (code === 'ENOENT') {
      throw new Error('Failed to read legacy providers.json (file not found).');
    }
    if (err instanceof SyntaxError) {
      throw new Error('Failed to parse legacy providers.json: invalid JSON');
    }
    throw new Error(`Failed to read legacy providers.json (${code || 'read error'})`);
  }
  const entries = collectLegacyEntries(parsed);
  const config = loadGatewayConfig(envOptions);
  const allowOverwrite = Boolean(options && options.overwrite);
  const imported = [];
  const skipped = [];
  const secrets = {};
  const notices = [];

  for (const { name, entry } of entries) {
    const url = entry.url || entry.baseURL || entry.base_url || '';
    const nameCheck = validateProfileName(name);
    const urlCheck = validateGatewayURL(url);
    const wire = validateWireProtocol(entry.wireProtocol || entry.wire_protocol || 'openai');
    let models = [];
    try {
      const rawModels = entry.models || [];
      models = parseGatewayModels(
        Array.isArray(rawModels) ? normalizeLegacyModelIds(rawModels) : rawModels
      );
    } catch (_) {
      models = [];
    }
    if (!nameCheck.valid || !urlCheck.valid || !wire.valid || models.length === 0) {
      skipped.push(sanitizeImportedField(name) || '(unnamed)');
      continue;
    }
    if (!allowOverwrite && Object.prototype.hasOwnProperty.call(config.profiles, nameCheck.value)) {
      skipped.push(nameCheck.value);
      notices.push(`Profile "${nameCheck.value}" already exists; skipped (existing profile kept).`);
      continue;
    }
    const plainKey = String(entry.apiKey || entry.api_key || '').trim();
    const declaredEnv = String(entry.apiKeyEnv || entry.api_key_env || '').trim();
    const declaredCheck = validateEnvName(declaredEnv);
    let apiKeyEnv = declaredCheck.valid ? declaredCheck.value : '';
    // The validator's own error text can embed the raw name, which may be a pasted secret.
    const redactName = (text) =>
      declaredEnv ? String(text || '').split(declaredEnv).join(maskSecret(sanitizeImportedField(declaredEnv))) : String(text || '');
    if (declaredEnv && !declaredCheck.valid) {
      const fallbackEnv = defaultEnvForProvider(nameCheck.value);
      notices.push(
        `legacy apiKeyEnv "${maskSecret(sanitizeImportedField(declaredEnv))}" rejected (${redactName(declaredCheck.error)}); using generated ${fallbackEnv}.`
      );
    } else if (apiKeyEnv && !isManagedEnvName(apiKeyEnv, nameCheck.value)) {
      // Default-deny: a legacy file may name any variable, including execution hooks
      // (`GIT_CONFIG_*`, `SUDO_ASKPASS`, ...) or a sibling profile's generated name. Only
      // agy-pro-owned names generated for THIS profile and catalog names are accepted;
      // everything else falls back to a generated name instead of trusting a denylist to be
      // complete.
      const fallbackEnv = defaultEnvForProvider(nameCheck.value);
      notices.push(
        `legacy apiKeyEnv "${maskSecret(sanitizeImportedField(declaredEnv))}" is not managed by agy-pro; using generated ${fallbackEnv}.`
      );
      apiKeyEnv = fallbackEnv;
    }
    if (!apiKeyEnv && (!urlCheck.isLoopback || plainKey)) {
      apiKeyEnv = defaultEnvForProvider(nameCheck.value);
    }
    if (plainKey && apiKeyEnv) {
      const existing = resolveProfileAPIKey({ apiKeyEnv }, envOptions);
      const inFlight = Object.prototype.hasOwnProperty.call(secrets, apiKeyEnv)
        ? String(secrets[apiKeyEnv])
        : '';
      if (inFlight) {
        if (inFlight !== plainKey) {
          notices.push(
            `${apiKeyEnv} already has a value from an earlier legacy entry; kept the first key for profile "${nameCheck.value}" (not overwritten).`
          );
        }
      } else if (existing && existing !== plainKey) {
        notices.push(`${apiKeyEnv} already has a value; kept existing key for profile "${nameCheck.value}" (not overwritten).`);
      } else {
        secrets[apiKeyEnv] = plainKey;
      }
    }
    const headerStrip = stripAuthorizationHeaders(entry.headers || '');
    if (headerStrip.removedAuthorization) {
      notices.push(
        `Removed Authorization header from profile "${nameCheck.value}"; store the key in the ${apiKeyEnv || 'profile'} env variable instead.`
      );
    }
    config.profiles[nameCheck.value] = {
      provider: sanitizeImportedField(entry.provider || nameCheck.value) || nameCheck.value,
      url: urlCheck.url,
      wireProtocol: wire.value,
      apiKeyEnv,
      models,
      caCert: sanitizeImportedField(entry.caCert || ''),
      headers: headerStrip.text,
      updatedAt: new Date().toISOString()
    };
    imported.push(nameCheck.value);
  }

  if (imported.length > 0) {
    saveGatewayConfig(config, envOptions);
    if (Object.keys(secrets).length > 0) persistEnvMap(secrets, envOptions);
  }
  return { imported, skipped, notices };
}

/**
 * Formats human-readable warnings for profiles whose invalid `apiKeyEnv` was cleared by a repair
 * write (`activateGateway`/`deactivateGateway`/`deleteProfile`). Only `maskedApiKeyEnv` is used:
 * the raw name may be a pasted secret, so it must never reach a terminal.
 * @param {Array<{ profile: string, maskedApiKeyEnv: string }>} cleared
 * @returns {string[]} One line per cleared profile (each ends with `\n`).
 */
function formatClearedApiKeyEnvNotices(cleared) {
  if (!Array.isArray(cleared) || cleared.length === 0) return [];
  return cleared.map((entry) => {
    const profile = displayConfigName((entry && entry.profile) || '') || '(unknown profile)';
    const masked = stripControlAndFormat((entry && entry.maskedApiKeyEnv) || '') || '(hidden)';
    return (
      `Warning: cleared invalid apiKeyEnv for profile "${profile}" (masked: "${masked}"); ` +
      'run `agy-pro setup` to set a valid key name.\n'
    );
  });
}

/**
 * Prints the Hub status header (OS env mode, active profile, URL, wire, models, masked key, profiles).
 * @param {NodeJS.WritableStream} output
 * @param {object} envOptions
 */
function printHubHeader(output, envOptions) {
  const status = getGatewayStatus(envOptions);
  const activeProfile = displayConfigName(status.activeProfile) || '(none)';
  const profiles = status.profiles.map((p) => displayConfigName(p)).filter(Boolean);
  const models =
    status.models.length > 0
      ? formatGatewayModels(status.models.map((m) => displayConfigName(m)))
      : '(unset)';
  output.write('\n===========================================================\n');
  output.write('  agy-pro Hub\n');
  output.write('===========================================================\n');
  output.write(`  OS Env Mode     : ${status.mode}\n`);
  output.write(`  Active Profile  : ${activeProfile}\n`);
  output.write(`  Gateway URL     : ${status.url ? displayURL(status.url) : '(unset)'}\n`);
  output.write(`  Wire Protocol   : ${displayConfigName(status.wireProtocol) || '(unset)'}\n`);
  output.write(`  GATEWAY_MODELS  : ${models}\n`);
  output.write(`  API Key         : ${displayConfigName(status.maskedKey)}\n`);
  output.write(`  Saved Profiles  : ${profiles.length > 0 ? profiles.join(', ') : '(none)'}\n\n`);
}

/**
 * Prompts for a saved profile by number or name.
 * @param {object} ctx `{ prompter, output }`
 * @param {object} config
 * @param {string} label
 * @returns {Promise<string>} Profile name, or '' when invalid/none.
 */
async function pickProfile(ctx, config, label) {
  const { prompter, output } = ctx;
  const names = Object.keys(config.profiles);
  if (names.length === 0) {
    output.write('No saved profiles yet. Use [1] to add one.\n');
    return '';
  }
  names.forEach((n, idx) => {
    const p = config.profiles[n];
    const mark = n === config.activeProfile ? ' [Active]' : '';
    const safeName = displayConfigName(n);
    const safeProvider = displayConfigName(p.provider || '');
    output.write(`  ${idx + 1}) ${safeName} (${safeProvider}, ${displayURL(p.url)}, ${displayConfigName(p.wireProtocol)})${mark}\n`);
  });
  const def = config.activeProfile && names.includes(config.activeProfile) ? config.activeProfile : names[0];
  const { value } = await prompter.ask(`${label} (1-${names.length} or name)`, displayConfigName(def));
  const idx = Number(value) - 1;
  const target = Number.isInteger(idx) && idx >= 0 && idx < names.length ? names[idx] : value.trim();
  if (!Object.prototype.hasOwnProperty.call(config.profiles, target)) {
    output.write(`Profile "${displayConfigName(target)}" not found.\n`);
    return '';
  }
  return target;
}

/**
 * Hub action [2]: switch/activate a saved profile.
 *
 * Activation persists the profile's key into the OS env store, so an unmanaged `apiKeyEnv` needs
 * the same consent as a probe: otherwise a tampered gateway.json turns the next `agy` run into a
 * repeated exfiltration of an ambient credential.
 */
async function hubSwitchProfile(ctx) {
  const { output, envOptions } = ctx;
  const config = loadGatewayConfig(envOptions);
  const target = await pickProfile(ctx, config, 'Activate profile');
  if (!target) return;
  const profile = config.profiles[target];
  if (!(await requestUnmanagedEnvConsent(profile, { ...ctx, profileName: target }))) {
    output.write(
      `Activation refused: profile "${displayConfigName(target)}" reads unmanaged env var ` +
        `${displayEnvName(profile.apiKeyEnv)}; its value was not sent to ${displayURL(profile.url)}.\n`
    );
    return;
  }
  const res = activateGateway(target, envOptions);
  output.write(`Activated gateway profile "${displayConfigName(target)}".\n`);
  for (const notice of formatClearedApiKeyEnvNotices(res.clearedApiKeyEnv)) output.write(notice);
  output.write(`${XOR_MODELS_WARNING}\n${RELOAD_HINT}\n`);
}

/**
 * Hub action [3]: edit models of a profile (offers activation when the gateway is OFF).
 *
 * Both activation branches pass through the unmanaged-env consent gate before `activateGateway`
 * resolves the key, matching hub [2].
 */
async function hubEditModels(ctx) {
  const { prompter, output, envOptions } = ctx;
  const config = loadGatewayConfig(envOptions);
  const name = await pickProfile(ctx, config, 'Edit models of profile');
  if (!name) return;
  const profile = config.profiles[name];
  // Model ids are untrusted config text: control-strip AND mask credential shapes, matching the
  // cli `models` path (`displayConfigLabel`) and hub [5]. A benign id such as `qwen2.5-coder:32b`
  // passes through unchanged.
  const rawModels = Array.isArray(profile.models) ? profile.models.map((m) => String(m)) : [];
  const currentModels = formatGatewayModels(rawModels.map((m) => displayConfigName(m)));
  output.write(`Current models: ${currentModels || '(none)'}\n`);
  const { value } = await prompter.ask('Comma-separated model IDs', currentModels);
  // The prompt default is the MASKED list, so pressing Enter hands that masked text back. Keeping
  // the stored ids verbatim for an empty answer or an echo of the default is what stops a display
  // artifact (`sk-an****`) from being persisted into gateway.json.
  const answer = String(value === null || value === undefined ? '' : value).trim();
  const keepStored = answer === '' || answer === currentModels;
  const parsed = keepStored ? rawModels : parseGatewayModels(answer);
  if (parsed.length === 0) {
    output.write('Error: At least one model ID is required.\n');
    return;
  }
  profile.models = parsed;
  profile.updatedAt = new Date().toISOString();
  saveGatewayConfig(config, envOptions);
  output.write(`Updated models for "${displayConfigName(name)}": ${formatGatewayModels(parsed.map((m) => displayConfigName(m)))}\n`);

  const gatewayOn = config.active && config.activeProfile === name;
  if (gatewayOn) {
    if (!(await requestUnmanagedEnvConsent(profile, { ...ctx, profileName: name }))) {
      output.write(
        `Refresh refused: profile "${displayConfigName(name)}" reads unmanaged env var ` +
          `${displayEnvName(profile.apiKeyEnv)}; its value was not sent to ${displayURL(profile.url)}.\n`
      );
      return;
    }
    const refreshed = activateGateway(name, envOptions);
    output.write('Gateway refreshed.\n');
    for (const notice of formatClearedApiKeyEnvNotices(refreshed.clearedApiKeyEnv)) output.write(notice);
    output.write(`${RELOAD_HINT}\n`);
    return;
  }
  const { value: ans } = await prompter.ask(
    `Gateway is OFF for "${displayConfigName(name)}". Activate it now? [Y/n]`,
    'Y'
  );
  if (/^n/i.test(ans)) return;
  if (!(await requestUnmanagedEnvConsent(profile, { ...ctx, profileName: name }))) {
    output.write(
      `Activation refused: profile "${displayConfigName(name)}" reads unmanaged env var ` +
        `${displayEnvName(profile.apiKeyEnv)}; its value was not sent to ${displayURL(profile.url)}.\n`
    );
    return;
  }
  const activated = activateGateway(name, envOptions);
  output.write(`Activated gateway profile "${displayConfigName(name)}".\n`);
  for (const notice of formatClearedApiKeyEnvNotices(activated.clearedApiKeyEnv)) output.write(notice);
  output.write(`${XOR_MODELS_WARNING}\n${RELOAD_HINT}\n`);
}

/**
 * Hub action [4]: probe a saved profile's endpoint with its first model.
 *
 * A tampered or hand-edited gateway.json must not be able to send an arbitrary ambient
 * credential (`GITHUB_TOKEN`, `AWS_SECRET_ACCESS_KEY`, ...) to a profile URL. Before the key is
 * resolved, the profile must pass the same consent gate as activation: an agy-pro generated name
 * (`AGY_PRO_GATEWAY_*`), or a provider-catalog name whose URL targets that provider's host. A
 * catalog name pointed at a foreign host is a confused deputy and needs an explicit `y`.
 */
async function hubProbe(ctx) {
  const { prompter, output, envOptions, transport } = ctx;
  const config = loadGatewayConfig(envOptions);
  const name = await pickProfile(ctx, config, 'Probe profile');
  if (!name) return;
  const profile = config.profiles[name];
  if (profile.apiKeyEnv) {
    const envCheck = validateEnvName(profile.apiKeyEnv);
    if (!envCheck.valid) {
      // The validator's error text can embed the raw name, which may be a pasted secret.
      const rawName = String(profile.apiKeyEnv || '');
      const errorText = rawName
        ? String(envCheck.error || '').split(rawName).join(displayEnvName(rawName))
        : String(envCheck.error || '');
      output.write(
        `Probe refused: profile "${displayConfigName(name)}" stores an invalid apiKeyEnv (${stripControlAndFormat(errorText)}). Fix or re-create the profile before probing.\n`
      );
      return;
    }
  }
  const models = Array.isArray(profile.models) ? profile.models : [];
  if (models.length === 0) {
    output.write(
      `Probe refused: profile "${displayConfigName(name)}" has no models. Add one with [3] Edit models before probing.\n`
    );
    return;
  }
  if (profile.apiKeyEnv) {
    const consent = await requestUnmanagedEnvConsent(profile, { ...ctx, profileName: name });
    if (!consent) {
      output.write(
        `Probe refused: profile "${displayConfigName(name)}" reads unmanaged env var ` +
          `${displayEnvName(profile.apiKeyEnv)}; its value was not sent to ${displayURL(profile.url)}.\n`
      );
      return;
    }
  }
  const apiKey = resolveProfileAPIKey(profile, envOptions);
  output.write(
    `Probing ${displayURL(profile.url)} with "${displayConfigName(models[0])}" (${displayConfigName(profile.wireProtocol)})...\n`
  );
  const res = await probeEndpoint({
    url: profile.url,
    apiKey,
    wireProtocol: profile.wireProtocol,
    model: models[0],
    caCert: profile.caCert,
    headers: profile.headers,
    transport
  });
  output.write(
    res.ok
      ? `Connection succeeded (${res.latencyMs}ms, HTTP ${res.status}).\n`
      : `Connection test failed: ${stripControlAndFormat(String(res.error || ''))}\n`
  );
}

/**
 * Hub action [5]: full gateway status followed by the doctor report.
 */
async function hubStatusAndDoctor(ctx) {
  const { output, envOptions } = ctx;
  const status = getGatewayStatus(envOptions);
  output.write(`Gateway Mode   : ${status.mode}\n`);
  output.write(`Active Profile : ${displayConfigName(status.activeProfile) || '(none)'}\n`);
  output.write(`Gateway URL    : ${status.url ? displayURL(status.url) : '(unset)'}\n`);
  output.write(`Wire Protocol  : ${displayConfigName(status.wireProtocol) || '(unset)'}\n`);
  output.write(
    `Gateway Models : ${status.models.length > 0 ? status.models.map((m) => displayConfigName(m)).join(', ') : '(unset)'}\n`
  );
  output.write(`API Key        : ${displayConfigName(status.maskedKey)}\n`);
  if (status.headers) output.write(`Headers        : ${stripControlAndFormat(maskHeaders(status.headers))}\n`);
  output.write(`MDM Admin File : ${status.adminSettings.configured ? `ACTIVE (${status.adminSettings.path})` : 'none'}\n`);
  output.write(`Config File    : ${status.configPath}\n\n`);
  if (typeof ctx.runDoctor === 'function') await ctx.runDoctor();
}

/**
 * Hub action [6]: delete a saved profile after confirmation.
 */
async function hubDeleteProfile(ctx) {
  const { prompter, output, envOptions } = ctx;
  const config = loadGatewayConfig(envOptions);
  const name = await pickProfile(ctx, config, 'Delete profile');
  if (!name) return;
  const { value } = await prompter.ask(`Delete profile "${displayConfigName(name)}"? [y/N]`, 'N');
  if (!/^y/i.test(value)) {
    output.write('[CANCELLED] nothing deleted\n');
    return;
  }
  const res = deleteProfile(name, envOptions);
  const wasOn = typeof res.wasOn === 'boolean' ? res.wasOn : Boolean(res.wasActive);
  const suffix = wasOn
    ? ' Gateway turned OFF (it was active).'
    : res.wasActive
      ? ' Gateway was already OFF; active profile cleared.'
      : '';
  output.write(`Deleted profile "${displayConfigName(res.deleted)}".${suffix}\n`);
  for (const notice of formatClearedApiKeyEnvNotices(res.clearedApiKeyEnv)) output.write(notice);
}

/**
 * Hub action [8]: import legacy providers.json.
 */
async function hubImportLegacy(ctx) {
  const { prompter, output, envOptions } = ctx;
  if (!fs.existsSync(getLegacyProvidersPath())) {
    output.write(`No legacy file found at ${getLegacyProvidersPath()}.\n`);
    return;
  }
  const conflicts = listLegacyImportConflicts(envOptions).map((n) => displayConfigName(n));
  let overwrite = false;
  if (conflicts.length > 0) {
    output.write(`Legacy import conflicts with existing profile(s): ${conflicts.join(', ')}.\n`);
    const { value } = await prompter.ask(
      `Overwrite existing profile(s) ${conflicts.join(', ')}? [y/N]`,
      'N'
    );
    overwrite = /^y/i.test(value);
    if (!overwrite) {
      output.write('Keeping existing profiles; conflicting entries will be skipped.\n');
    }
  }
  const res = importLegacyProviders(envOptions, { overwrite });
  const imported = res.imported.map((n) => displayConfigName(n)).filter(Boolean);
  const skipped = res.skipped.map((n) => displayConfigName(n)).filter(Boolean);
  output.write(
    `Imported ${imported.length} profile(s)${imported.length ? `: ${imported.join(', ')}` : ''}` +
      `${skipped.length ? `; skipped ${skipped.length}: ${skipped.join(', ')}` : ''}.\n`
  );
  for (const note of res.notices) output.write(`Notice: ${stripControlAndFormat(note)}\n`);
}

/**
 * Interactive Hub: status header + action menu looping until `[0] Exit`.
 * Action errors are reported and the loop continues.
 * @param {object} deps
 * @returns {Promise<{ exitCode: number }>}
 */
async function runSetupHub(deps) {
  const { input, output, envOptions } = deps;
  const prompter = createLinePrompter(input, output);
  const ctx = { ...deps, prompter };

  for (;;) {
    printHubHeader(output, envOptions);
    const hasProfiles = Object.keys(loadGatewayConfig(envOptions).profiles).length > 0;
    output.write('  [1] Add or update a provider / custom endpoint\n');
    output.write('  [2] Switch / activate a profile\n');
    output.write('  [3] Edit models\n');
    output.write('  [4] Test / probe an endpoint\n');
    output.write('  [5] Full status & doctor\n');
    output.write('  [6] Delete a profile\n');
    output.write('  [7] Turn gateway OFF (restore native models)\n');
    if (fs.existsSync(getLegacyProvidersPath())) {
      output.write('  [8] Import legacy providers.json\n');
    }
    output.write('  [0] Exit\n\n');

    const { value, eof } = await prompter.ask('Select action (0-8)', hasProfiles ? '0' : '1');
    if (eof && !value) return { exitCode: 0 };
    if (value === '0') return { exitCode: 0 };

    try {
      if (value === '1') {
        const res = await runSevenStepWizard(deps);
        if (res.exitCode !== 0) return res;
      } else if (value === '2') {
        await hubSwitchProfile(ctx);
      } else if (value === '3') {
        await hubEditModels(ctx);
      } else if (value === '4') {
        await hubProbe(ctx);
      } else if (value === '5') {
        await hubStatusAndDoctor(ctx);
      } else if (value === '6') {
        await hubDeleteProfile(ctx);
      } else if (value === '7') {
        const offRes = deactivateGateway({ ...envOptions, keepState: true });
        output.write('Gateway turned OFF. Native Antigravity models restored.\n');
        for (const notice of formatClearedApiKeyEnvNotices(offRes.clearedApiKeyEnv)) output.write(notice);
        output.write(`${RELOAD_HINT}\n`);
      } else if (value === '8') {
        await hubImportLegacy(ctx);
      } else {
        output.write(`Unknown selection "${value}".\n`);
      }
    } catch (err) {
      if (err instanceof SecretInterruptedError || err.interrupted) throw err;
      // An error message can embed an untrusted profile name or env name (a tampered
      // gateway.json, or a pasted secret in apiKeyEnv). Strip control/format runes before it
      // reaches the terminal so the message cannot repaint the hub or forge a prompt.
      output.write(`Error: ${stripControlAndFormat(String((err && err.message) || ''))}\n`);
    }
  }
}

/**
 * Entry point for `agy-pro setup`.
 * @param {string[]} [args]
 * @param {{ input?: NodeJS.ReadableStream, output?: NodeJS.WritableStream, isTTY?: boolean, envOptions?: object, transport?: Function }} [deps]
 * @returns {Promise<{ exitCode: number }>}
 */
async function runSetup(args = [], deps = {}) {
  const input = deps.input || process.stdin;
  const output = deps.output || process.stdout;
  const errOut = deps.errOutput || output;
  const isTTY = deps.isTTY !== undefined ? Boolean(deps.isTTY) : Boolean(input && input.isTTY);
  const envOptions = deps.envOptions || {};

  const parsed = parseSetupArgs(args);
  if (parsed.error) {
    errOut.write(`Error: ${parsed.error}\n`);
    return { exitCode: 2 };
  }

  const { flags } = parsed;
  const context = {
    input,
    output,
    errOutput: errOut,
    isTTY,
    envOptions,
    transport: deps.transport,
    runDoctor: deps.runDoctor
  };

  try {
    if (flags.yes) {
      return await runNonInteractiveSetup(flags, context);
    }

    if (!isTTY) {
      errOut.write(
        'Error: Non-interactive stdin cannot run setup prompts; re-run with --yes and required flags (--provider/--url, --models).\n'
      );
      return { exitCode: 2 };
    }

    if (flags.provider || flags.url) {
      return await runSevenStepWizard(context);
    }
    return await runSetupHub(context);
  } catch (err) {
    if (err instanceof SecretInterruptedError || err.interrupted) {
      errOut.write(`\n${stripControlAndFormat(String((err && err.message) || ''))}\n`);
      return { exitCode: 1 };
    }
    errOut.write(`Error: ${stripControlAndFormat(String((err && err.message) || ''))}\n`);
    return { exitCode: 1 };
  }
}

module.exports = {
  parseSetupArgs,
  buildModelCandidates,
  resolveModelSelectionInput,
  runSetup,
  runSetupHub,
  importLegacyProviders,
  formatClearedApiKeyEnvNotices,
  isManagedEnvName,
  isManagedProfileEnv,
  requestUnmanagedEnvConsent,
  formatUnmanagedEnvRefusal,
  formatUnmanagedEnvPrompt,
  stripAuthorizationHeaders,
  RELOAD_HINT
};
