/**
 * @fileoverview Interactive 7-step setup wizard and non-interactive `--yes` setup runner
 * for `agy-pro setup`, modeled on `agy-pro/internal/providers/wizard.go` & `presets.go`.
 */

'use strict';

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
  maskHeaders
} = require('./validator');
const { fetchRemoteModels, probeEndpoint } = require('./http-client');
const { promptSecret, readSecretFromStdin, SecretInterruptedError } = require('./secret-input');
const {
  loadGatewayConfig,
  saveGatewayConfig,
  activateGateway,
  deactivateGateway,
  getGatewayConfigPath,
  getGatewayStatus,
  resolveProfileAPIKey,
  persistEnvMap
} = require('./env-store');

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
      `Error: API key is required for remote URL (${maskBaseURL(urlCheck.url)}). Pass --api-key-stdin, set AGY_PRO_API_KEY_INPUT, or export ${apiKeyEnv}.\n`
    );
    return { exitCode: 2 };
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

  if (flags.probe) {
    const probeRes = await probeEndpoint({
      url: urlCheck.url,
      apiKey: apiKeyValue,
      wireProtocol: wireCheck.value,
      model: models[0],
      caCert: flags.caCert,
      headers: flags.headers,
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
    headers: flags.headers || '',
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
    `Saved profile "${profileCheck.value}" (${maskBaseURL(urlCheck.url)}, wire=${wireCheck.value}, models=${formatGatewayModels(models)}, key=${apiKeyValue ? maskSecret(apiKeyValue) : '(none - loopback)'}).\n`
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
    output.write(`  Get a key at: ${selectedHint.keyURL}\n`);
  }
  let apiKeyEnv = selectedHint ? selectedHint.apiKeyEnv : defaultEnvForProvider(profileName);
  if (isLoopback && !apiKeyEnv && providerName !== 'local-gateway') {
    apiKeyEnv = '';
  }
  let existingKey = apiKeyEnv ? resolveProfileAPIKey({ apiKeyEnv }, envOptions) : '';
  let apiKeyValue = '';

  if (isLoopback) {
    output.write('  Loopback URL detected: API key is optional (press Enter to skip).\n');
  }
  if (apiKeyEnv && existingKey) {
    output.write(`  Detected existing ${apiKeyEnv}: ${maskSecret(existingKey)}\n`);
  }

  const keyPrompt = isLoopback
    ? 'API Key (optional on loopback, press Enter to skip)'
    : existingKey
      ? `API Key (Enter to keep ${apiKeyEnv}, or paste key/env name)`
      : `API Key (stored in ${apiKeyEnv || defaultEnvForProvider(profileName)})`;

  const enteredKey = await promptSecret({
    prompt: keyPrompt,
    defaultValue: '',
    input,
    output
  });

  if (enteredKey) {
    if (!looksLikeAPIKey(enteredKey) && validateEnvName(enteredKey).valid && process.env[enteredKey]) {
      apiKeyEnv = enteredKey;
      apiKeyValue = String(process.env[enteredKey]).trim();
      output.write(`  Loaded key from existing env var ${apiKeyEnv}: ${maskSecret(apiKeyValue)}\n`);
    } else {
      if (!apiKeyEnv) {
        apiKeyEnv = defaultEnvForProvider(profileName);
      }
      apiKeyValue = enteredKey;
      output.write(`  API Key recorded (${apiKeyEnv}): ${maskSecret(apiKeyValue)}\n`);
    }
  } else if (existingKey) {
    apiKeyValue = existingKey;
  } else if (!isLoopback) {
    output.write(`  Warning: No key entered yet; export ${apiKeyEnv} before activating.\n`);
  } else {
    apiKeyEnv = '';
  }

  const { value: caCert } = await prompter.ask('Custom CA cert path (optional, Enter to skip)', '');
  const { value: headers } = await prompter.ask('Extra HTTP headers "K: V" (optional, Enter to skip)', '');

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
        const desc = m.description ? ` (${m.description})` : '';
        output.write(`    ${idx + 1}) ${m.id}${desc}${rec}\n`);
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
      `Test connection to ${maskBaseURL(baseURL)} with "${selectedModels[0]}"? [Y/n]`,
      'Y'
    );
    if (runProbeAns.toLowerCase() === 'n' || runProbeAns.toLowerCase() === 'no') {
      break;
    }
    output.write(`  Probing ${selectedModels[0]} (${wireProtocol})...\n`);
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

    output.write(`  Connection test failed: ${probeRes.error}\n`);
    output.write('  Options: 1) Save anyway  2) Retry  3) Edit Base URL / API Key  4) Abort\n');
    const { value: failChoice } = await prompter.ask('Choice (1-4)', '1');
    if (failChoice === '1') {
      probeDone = true;
    } else if (failChoice === '2') {
      continue;
    } else if (failChoice === '3') {
      const { value: nextUrl } = await prompter.ask('Base URL', maskBaseURL(baseURL));
      if (nextUrl && nextUrl !== maskBaseURL(baseURL)) {
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
          ? `API Key (required for remote URL, stored in ${apiKeyEnv})`
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
          output.write(`  Warning: Remote endpoint requires an API key in ${apiKeyEnv}.\n`);
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
  output.write(`  Profile         : ${profileName} (provider: ${providerName})\n`);
  output.write(`  Config File     : ${getGatewayConfigPath(envOptions)} (0600, atomic)\n`);
  output.write(`  AGY_LLM_GATEWAY_URL           = ${maskBaseURL(baseURL)}\n`);
  output.write(`  AGY_LLM_GATEWAY_WIRE_PROTOCOL = ${wireProtocol}\n`);
  output.write(`  AGY_LLM_GATEWAY_MODELS        = ${formattedModels}\n`);
  output.write(
    `  AGY_LLM_GATEWAY_API_KEY       = ${apiKeyValue ? maskSecret(apiKeyValue) : '(none - loopback)'}\n`
  );
  if (caCert) output.write(`  AGY_LLM_GATEWAY_CA_CERT       = ${caCert}\n`);
  if (headers) output.write(`  AGY_LLM_GATEWAY_HEADERS       = ${maskHeaders(headers)}\n`);
  output.write('-----------------------------------------------------------\n');
  output.write(`${XOR_MODELS_WARNING}\n\n`);

  const { value: applyAns } = await prompter.ask('Save profile and activate gateway? [Y/n]', 'Y');
  if (applyAns.toLowerCase() === 'n' || applyAns.toLowerCase() === 'no') {
    output.write('[CANCELLED] nothing written\n');
    return { exitCode: 0 };
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
      `\nProfile "${profileName}" saved, but ${apiKeyEnv} is not set yet. Export it and run \`agy-pro gateway on ${profileName}\`.\n`
    );
    return { exitCode: 0 };
  }

  activateGateway(profileName, { ...envOptions, apiKey: apiKeyValue });
  output.write(`\nGateway profile "${profileName}" saved and activated.\n`);
  output.write('Open a new terminal (or run `eval "$(agy-pro env)"` on POSIX) before launching `agy`.\n');
  return { exitCode: 0 };
}

/**
 * Interactive menu when saved profiles already exist in `gateway.json`.
 * @param {object} deps
 * @returns {Promise<{ exitCode: number }>}
 */
async function runExistingProfilesMenu(deps) {
  const { input, output, envOptions } = deps;
  const prompter = createLinePrompter(input, output);
  const status = getGatewayStatus(envOptions);
  const config = loadGatewayConfig(envOptions);
  const names = Object.keys(config.profiles);

  output.write('\n===========================================================\n');
  output.write('  agy-pro Gateway Manager\n');
  output.write('===========================================================\n');
  output.write(`  Mode          : ${status.mode}\n`);
  output.write(`  Active Profile: ${config.activeProfile || '(none)'}\n`);
  output.write(`  Saved Profiles: ${names.join(', ')}\n\n`);
  output.write('  [1] Add or update a provider & models\n');
  output.write('  [2] Switch active gateway profile\n');
  output.write('  [3] Edit models in active profile\n');
  output.write('  [4] Turn gateway OFF (restore native models)\n');
  output.write('  [0] Exit\n\n');

  const { value, eof } = await prompter.ask('Select action (0-4)', '1');
  if (eof && !value) return { exitCode: 0 };

  if (value === '0') {
    return { exitCode: 0 };
  }
  if (value === '1') {
    return runSevenStepWizard(deps);
  }
  if (value === '2') {
    output.write('\nSaved profiles:\n');
    names.forEach((n, idx) => {
      const p = config.profiles[n];
      const mark = n === config.activeProfile ? ' [Active]' : '';
      output.write(`  ${idx + 1}) ${n} (${p.provider}, ${maskBaseURL(p.url)}, ${p.wireProtocol})${mark}\n`);
    });
    const { value: pick } = await prompter.ask(`Choose profile (1-${names.length} or name)`, '1');
    const idx = Number(pick) - 1;
    const target =
      Number.isInteger(idx) && idx >= 0 && idx < names.length ? names[idx] : pick.trim();
    if (!Object.prototype.hasOwnProperty.call(config.profiles, target)) {
      output.write(`Profile "${target}" not found.\n`);
      return { exitCode: 1 };
    }
    activateGateway(target, envOptions);
    output.write(`Switched active gateway profile to "${target}".\n`);
    output.write(`${XOR_MODELS_WARNING}\n`);
    return { exitCode: 0 };
  }
  if (value === '3') {
    const activeName = config.activeProfile || names[0];
    const profile = config.profiles[activeName];
    output.write(
      `\nEditing models for "${activeName}" (current: ${formatGatewayModels(profile.models) || '(none)'})\n`
    );
    const { value: nextModels } = await prompter.ask(
      'Comma-separated model IDs',
      formatGatewayModels(profile.models)
    );
    let parsed;
    try {
      parsed = parseGatewayModels(nextModels);
    } catch (err) {
      output.write(`Error: ${err.message}\n`);
      return { exitCode: 2 };
    }
    if (parsed.length === 0) {
      output.write('Error: At least one model ID is required.\n');
      return { exitCode: 2 };
    }
    profile.models = parsed;
    profile.updatedAt = new Date().toISOString();
    saveGatewayConfig(config, envOptions);
    if (config.active) {
      activateGateway(activeName, envOptions);
    }
    output.write(`Updated models for "${activeName}": ${formatGatewayModels(parsed)}\n`);
    return { exitCode: 0 };
  }
  if (value === '4') {
    deactivateGateway({ ...envOptions, keepState: true });
    output.write('Gateway turned OFF. Native Antigravity models restored (open a new terminal).\n');
    return { exitCode: 0 };
  }

  output.write(`Unknown selection "${value}".\n`);
  return { exitCode: 2 };
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
  const context = { input, output, errOutput: errOut, isTTY, envOptions, transport: deps.transport };

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

    const config = loadGatewayConfig(envOptions);
    if (Object.keys(config.profiles).length > 0 && !flags.provider && !flags.url) {
      return await runExistingProfilesMenu(context);
    }
    return await runSevenStepWizard(context);
  } catch (err) {
    if (err instanceof SecretInterruptedError || err.interrupted) {
      errOut.write(`\n${err.message}\n`);
      return { exitCode: 1 };
    }
    errOut.write(`Error: ${err.message}\n`);
    return { exitCode: 1 };
  }
}

module.exports = {
  parseSetupArgs,
  buildModelCandidates,
  resolveModelSelectionInput,
  runSetup
};
