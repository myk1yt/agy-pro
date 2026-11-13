/**
 * @fileoverview CLI dispatcher for the simplified Node.js `agy-pro` tool.
 * Implements `setup`, `gateway on|off|status`, `models`, `env`, `doctor`, `help`, and `version`.
 * Exit codes: 0 = success, 1 = runtime/probe/persistence error, 2 = usage/validation error.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const {
  XOR_MODELS_WARNING,
  OPENAI_TOOL_WARNING,
  isLoopbackURL,
  validateGatewayURL,
  validateWireProtocol,
  parseGatewayModels,
  formatGatewayModels,
  maskSecret,
  maskBaseURL,
  maskHeaders
} = require('./validator');
const { fetchRemoteModels } = require('./http-client');
const {
  GATEWAY_ENV_KEYS,
  loadGatewayConfig,
  activateGateway,
  deactivateGateway,
  getGatewayStatus,
  getGatewayConfigPath,
  resolveProfileAPIKey,
  buildGatewayEnvMap,
  readPersistedEnvMap,
  detectAdminGatewaySettings
} = require('./env-store');
const { runSetup } = require('./wizard');
const pkg = require('../../package.json');

/**
 * Formats the CLI help screen.
 * @returns {string}
 */
function formatHelp() {
  return [
    `agy-pro v${pkg.version} — Antigravity Enterprise Gateway & Profile Manager`,
    '',
    'Usage:',
    '  agy-pro <command> [options]',
    '',
    'Commands:',
    '  setup [options]                 Interactive 7-step setup wizard (or --yes scripted mode)',
    '  gateway on [<profile>] [--yes]  Activate gateway profile (sets AGY_LLM_GATEWAY_*)',
    '  gateway off [--yes]             Deactivate gateway (restores native Antigravity models)',
    '  gateway status [--json]         Show active mode (NATIVE vs GATEWAY_XOR), profile & MDM state',
    '  models [--remote] [--json]      List configured or remote (/models) gateway models',
    '  env [--shell sh|pwsh|cmd]       Print shell export commands (--reveal for plaintext key)',
    '  doctor [--json]                 Verify Node >=16, zero agy PATH shadowing, hooks, statusLine & gateway env',
    '  help                            Show this help message',
    '  version                         Print version',
    '',
    'Setup Non-Interactive Flags (--yes):',
    '  --yes, --profile <name>, --provider <name>, --url <url>, --wire <genai|openai>,',
    '  --models <m1,m2>, --api-key-env <ENV>, --api-key-stdin, --ca-cert <path>,',
    '  --headers "<K: V>", --activate | --no-activate, --probe',
    '',
    'Security Note:',
    '  Never pass secrets via --api-key on argv. Use interactive prompt, --api-key-stdin,',
    '  or AGY_PRO_API_KEY_INPUT.',
    ''
  ].join('\n');
}

/**
 * Checks whether any CLI argument attempts to pass a plaintext `--api-key` in argv.
 * @param {string[]} args
 * @returns {boolean}
 */
function hasForbiddenApiKeyFlag(args) {
  return args.some((a) => a === '--api-key' || a.startsWith('--api-key='));
}

/**
 * Parses common flags (`--json`, `--yes`, `--remote`, `--reveal`, `--shell`, `--profile`).
 * @param {string[]} args
 * @returns {{ flags: object, positionals: string[], error?: string }}
 */
function parseCommonArgs(args, allowedFlags = new Set()) {
  const flags = {
    json: false,
    yes: false,
    remote: false,
    reveal: false,
    shell: '',
    profile: ''
  };
  const positionals = [];

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--json' && allowedFlags.has('json')) {
      flags.json = true;
    } else if ((arg === '--yes' || arg === '-y') && allowedFlags.has('yes')) {
      flags.yes = true;
    } else if (arg === '--remote' && allowedFlags.has('remote')) {
      flags.remote = true;
    } else if (arg === '--reveal' && allowedFlags.has('reveal')) {
      flags.reveal = true;
    } else if (arg === '--shell' && allowedFlags.has('shell')) {
      if (i + 1 >= args.length) return { flags, positionals, error: '--shell requires a value (sh|pwsh|cmd)' };
      flags.shell = args[++i];
    } else if (arg.startsWith('--shell=') && allowedFlags.has('shell')) {
      flags.shell = arg.slice('--shell='.length);
    } else if (arg === '--profile' && allowedFlags.has('profile')) {
      if (i + 1 >= args.length) return { flags, positionals, error: '--profile requires a value' };
      flags.profile = args[++i];
    } else if (arg.startsWith('--profile=') && allowedFlags.has('profile')) {
      flags.profile = arg.slice('--profile='.length);
    } else if (arg.startsWith('-')) {
      return { flags, positionals, error: `Unknown option: ${arg}` };
    } else {
      positionals.push(arg);
    }
  }

  return { flags, positionals };
}

/**
 * Handles `agy-pro gateway <on|off|status>`.
 */
function handleGateway(args, deps) {
  const { output, envOptions } = deps;
  const errOut = deps.errOutput || output;
  const sub = (args[0] || '').toLowerCase();
  const rest = args.slice(1);

  if (!sub || sub === 'status') {
    const parsed = parseCommonArgs(sub === 'status' ? rest : args, new Set(['json']));
    if (parsed.error) {
      errOut.write(`Error: ${parsed.error}\n`);
      return 2;
    }
    let status;
    try {
      status = getGatewayStatus(envOptions);
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return 1;
    }

    const maskedHdrs = status.headers ? maskHeaders(status.headers) : '';
    const safeStatus = {
      ...status,
      url: maskBaseURL(status.url),
      headers: maskedHdrs,
      maskedHeaders: maskedHdrs
    };

    if (parsed.flags.json) {
      output.write(JSON.stringify(safeStatus, null, 2) + '\n');
      return 0;
    }

    output.write(`Gateway Mode   : ${safeStatus.mode}\n`);
    output.write(`Active Profile : ${safeStatus.activeProfile || '(none)'}\n`);
    output.write(
      `Saved Profiles : ${safeStatus.profiles.length > 0 ? safeStatus.profiles.join(', ') : '(none)'}\n`
    );
    output.write(`Gateway URL    : ${safeStatus.url || '(unset)'}\n`);
    output.write(`Wire Protocol  : ${safeStatus.wireProtocol || '(unset)'}\n`);
    output.write(
      `Gateway Models : ${safeStatus.models.length > 0 ? safeStatus.models.join(', ') : '(unset)'}\n`
    );
    if (safeStatus.modelsError) {
      output.write(`Models Error   : ${safeStatus.modelsError}\n`);
    }
    output.write(`API Key        : ${safeStatus.maskedKey}\n`);
    if (safeStatus.maskedHeaders) {
      output.write(`Headers        : ${safeStatus.maskedHeaders}\n`);
    }
    output.write(
      `MDM Admin File : ${safeStatus.adminSettings.configured ? `ACTIVE (${safeStatus.adminSettings.path})` : 'none'}\n`
    );
    output.write(`Config File    : ${safeStatus.configPath}\n`);
    return 0;
  }

  if (sub === 'on') {
    const parsed = parseCommonArgs(rest, new Set(['yes', 'json', 'profile']));
    if (parsed.error) {
      errOut.write(`Error: ${parsed.error}\n`);
      return 2;
    }
    const profileName = parsed.flags.profile || parsed.positionals[0] || '';
    try {
      const res = activateGateway(profileName, envOptions);
      if (parsed.flags.json) {
        output.write(
          JSON.stringify(
            {
              ok: true,
              mode: 'GATEWAY_XOR',
              profile: res.profileName,
              url: maskBaseURL(res.profile.url),
              wireProtocol: res.profile.wireProtocol,
              models: res.profile.models,
              maskedKey: res.maskedKey,
              warning: XOR_MODELS_WARNING
            },
            null,
            2
          ) + '\n'
        );
        return 0;
      }
      output.write(`Gateway activated: profile "${res.profileName}" (${maskBaseURL(res.profile.url)})\n`);
      output.write(`Wire Protocol: ${res.profile.wireProtocol} | Models: ${res.profile.models.join(',')}\n`);
      output.write(`API Key      : ${res.maskedKey}\n`);
      if (res.profile.wireProtocol === 'openai') {
        output.write(`${OPENAI_TOOL_WARNING}\n`);
      }
      output.write(`${XOR_MODELS_WARNING}\n`);
      return 0;
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return 1;
    }
  }

  if (sub === 'off') {
    const parsed = parseCommonArgs(rest, new Set(['yes', 'json']));
    if (parsed.error) {
      errOut.write(`Error: ${parsed.error}\n`);
      return 2;
    }
    try {
      const res = deactivateGateway({ ...envOptions, keepState: true });
      if (parsed.flags.json) {
        output.write(JSON.stringify({ ok: true, mode: 'NATIVE', ...res }, null, 2) + '\n');
        return 0;
      }
      output.write('Gateway turned OFF. Native Antigravity models restored (open a new terminal).\n');
      return 0;
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return 1;
    }
  }

  errOut.write(`Error: Unknown gateway subcommand "${sub}". Use: gateway on [<profile>] | off | status\n`);
  return 2;
}

/**
 * Handles `agy-pro models [--remote] [--profile <name>] [--json]`.
 */
async function handleModels(args, deps) {
  const { output, envOptions, transport } = deps;
  const errOut = deps.errOutput || output;
  const parsed = parseCommonArgs(args, new Set(['remote', 'json', 'profile']));
  if (parsed.error) {
    errOut.write(`Error: ${parsed.error}\n`);
    return 2;
  }

  let config;
  try {
    config = loadGatewayConfig(envOptions);
  } catch (err) {
    errOut.write(`Error: ${err.message}\n`);
    return 1;
  }

  const targetName = parsed.flags.profile || config.activeProfile || '';
  const profile =
    targetName && Object.prototype.hasOwnProperty.call(config.profiles, targetName)
      ? config.profiles[targetName]
      : null;
  if (parsed.flags.profile && !profile) {
    errOut.write(`Error: Profile "${parsed.flags.profile}" not found.\n`);
    return 1;
  }

  if (parsed.flags.remote) {
    const url = (profile && profile.url) || process.env.AGY_LLM_GATEWAY_URL || '';
    if (!url) {
      errOut.write('Error: No gateway URL configured. Run `agy-pro setup` or pass --profile <name>.\n');
      return 2;
    }
    const apiKey = profile
      ? resolveProfileAPIKey(profile, envOptions)
      : process.env.AGY_LLM_GATEWAY_API_KEY || '';
    const wireProtocol =
      (profile && profile.wireProtocol) || process.env.AGY_LLM_GATEWAY_WIRE_PROTOCOL || 'openai';
    try {
      const remote = await fetchRemoteModels(url, apiKey, {
        wireProtocol,
        caCert: (profile && profile.caCert) || process.env.AGY_LLM_GATEWAY_CA_CERT || '',
        headers: (profile && profile.headers) || process.env.AGY_LLM_GATEWAY_HEADERS || '',
        transport
      });
      if (parsed.flags.json) {
        output.write(JSON.stringify({ url: maskBaseURL(url), models: remote }, null, 2) + '\n');
        return 0;
      }
      if (remote.length === 0) {
        output.write(`No models returned by ${maskBaseURL(url)}/models.\n`);
        return 0;
      }
      output.write(`Remote models (${maskBaseURL(url)}):\n`);
      for (const m of remote) {
        const ctx = m.contextLength > 0 ? ` (context: ${m.contextLength})` : '';
        output.write(`  - ${m.id}${ctx}\n`);
      }
      return 0;
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return 1;
    }
  }

  const status = getGatewayStatus(envOptions);
  const models = profile ? profile.models : status.models;
  if (parsed.flags.json) {
    output.write(
      JSON.stringify(
        {
          mode: status.mode,
          profile: profile ? targetName : status.activeProfile || null,
          models
        },
        null,
        2
      ) + '\n'
    );
    return 0;
  }

  if (models.length === 0) {
    output.write('No gateway models configured. Run `agy-pro setup` to add a provider.\n');
    return 0;
  }
  const label = profile ? `Profile "${targetName}"` : `Active gateway (${status.mode})`;
  output.write(`${label} models:\n`);
  for (const id of models) {
    output.write(`  - ${id}\n`);
  }
  return 0;
}

/**
 * Formats a shell variable assignment for `sh`, `pwsh`, or `cmd`.
 * @param {'sh'|'pwsh'|'cmd'} shell
 * @param {string} key
 * @param {string} value
 * @returns {string}
 */
function formatShellExport(shell, key, value) {
  const raw = String(value);
  if (shell === 'pwsh') {
    const escaped = raw.replace(/['\u2018\u2019\u201A\u201B]/g, (q) => q + q);
    return `$env:${key} = '${escaped}'`;
  }
  if (shell === 'cmd') {
    const singleLine = raw.replace(/[\r\n]+/g, ' ').replace(/"/g, '').replace(/%/g, '%%');
    return `set "${key}=${singleLine}"`;
  }
  return `export ${key}='${raw.replace(/'/g, "'\\''")}'`;
}

/**
 * Formats a shell unset statement for `sh`, `pwsh`, or `cmd`.
 * @param {'sh'|'pwsh'|'cmd'} shell
 * @param {string} key
 * @returns {string}
 */
function formatShellUnset(shell, key) {
  if (shell === 'pwsh') return `Remove-Item Env:${key} -ErrorAction SilentlyContinue`;
  if (shell === 'cmd') return `set "${key}="`;
  return `unset ${key}`;
}

/**
 * Handles `agy-pro env [--shell sh|pwsh|cmd] [--profile <name>] [--reveal]`.
 */
function handleEnv(args, deps) {
  const { output, envOptions } = deps;
  const errOut = deps.errOutput || output;
  const parsed = parseCommonArgs(args, new Set(['shell', 'profile', 'reveal']));
  if (parsed.error) {
    errOut.write(`Error: ${parsed.error}\n`);
    return 2;
  }

  const rawShell = (
    parsed.flags.shell || (process.platform === 'win32' ? 'pwsh' : 'sh')
  ).toLowerCase();
  const shell =
    rawShell === 'bash' || rawShell === 'zsh' || rawShell === 'sh'
      ? 'sh'
      : rawShell === 'powershell' || rawShell === 'pwsh'
        ? 'pwsh'
        : rawShell === 'cmd'
          ? 'cmd'
          : '';
  if (!shell) {
    errOut.write(`Error: Unsupported --shell "${rawShell}". Choose sh, pwsh, or cmd.\n`);
    return 2;
  }

  let envMap = {};
  if (parsed.flags.profile) {
    let config;
    try {
      config = loadGatewayConfig(envOptions);
    } catch (err) {
      errOut.write(`Error: ${err.message}\n`);
      return 1;
    }
    const profile = Object.prototype.hasOwnProperty.call(config.profiles, parsed.flags.profile)
      ? config.profiles[parsed.flags.profile]
      : null;
    if (!profile) {
      errOut.write(`Error: Profile "${parsed.flags.profile}" not found.\n`);
      return 1;
    }
    const apiKey = resolveProfileAPIKey(profile, envOptions);
    envMap = buildGatewayEnvMap(profile, apiKey);
  } else {
    const persisted = readPersistedEnvMap(GATEWAY_ENV_KEYS, envOptions);
    for (const k of GATEWAY_ENV_KEYS) {
      envMap[k] = process.env[k] || persisted.vars[k] || null;
    }
  }

  const hasUrl = Boolean(envMap.AGY_LLM_GATEWAY_URL);
  if (!hasUrl) {
    for (const k of GATEWAY_ENV_KEYS) {
      output.write(formatShellUnset(shell, k) + '\n');
    }
    return 0;
  }

  for (const k of GATEWAY_ENV_KEYS) {
    const val = envMap[k];
    if (!val) {
      output.write(formatShellUnset(shell, k) + '\n');
      continue;
    }
    let displayVal = val;
    if (!parsed.flags.reveal) {
      if (k === 'AGY_LLM_GATEWAY_API_KEY') {
        displayVal = maskSecret(val);
      } else if (k === 'AGY_LLM_GATEWAY_HEADERS') {
        displayVal = maskHeaders(val);
      }
    }
    output.write(formatShellExport(shell, k, displayVal) + '\n');
  }
  return 0;
}

/**
 * Searches PATH for `agy` executables to verify zero shadowing by `~/.agy-pro/bin`.
 * @param {object} [deps]
 * @returns {string[]}
 */
function findAgyBinariesOnPath(deps = {}) {
  if (typeof deps.findAgyBinaries === 'function') {
    return deps.findAgyBinaries();
  }
  const pathEnv = process.env.PATH || '';
  const dirs = pathEnv.split(path.delimiter).filter(Boolean);
  const candidates = process.platform === 'win32' ? ['agy.exe', 'agy.cmd', 'agy.bat'] : ['agy'];
  const found = [];
  const seen = new Set();

  for (const dir of dirs) {
    for (const name of candidates) {
      const full = path.join(dir, name);
      try {
        if (fs.existsSync(full) && fs.statSync(full).isFile()) {
          const norm = path.resolve(full);
          if (!seen.has(norm)) {
            seen.add(norm);
            found.push(norm);
          }
        }
      } catch (_) {
        // Ignore unreadable dir
      }
    }
  }
  return found;
}

/**
 * Handles `agy-pro doctor [--json]`.
 */
function handleDoctor(args, deps) {
  const { output, envOptions } = deps;
  const errOut = deps.errOutput || output;
  const parsed = parseCommonArgs(args, new Set(['json']));
  if (parsed.error) {
    errOut.write(`Error: ${parsed.error}\n`);
    return 2;
  }

  const checks = [];

  // 1. Node.js >= 16
  const major = Number(String(process.versions.node || '0').split('.')[0]);
  checks.push({
    id: 'node_version',
    status: major >= 16 ? 'pass' : 'fail',
    message:
      major >= 16
        ? `Node.js v${process.versions.node} (>=16 supported)`
        : `Node.js v${process.versions.node} is below required >=16`
  });

  // 2. Native agy binary PATH check (must NOT be shadowed by ~/.agy-pro/bin/agy*)
  const agyBins = findAgyBinariesOnPath(deps);
  const legacyShadowDir = path.resolve(path.join(os.homedir(), '.agy-pro', 'bin')).toLowerCase();
  const shadowed = agyBins.filter((b) => path.dirname(b).toLowerCase() === legacyShadowDir);
  if (shadowed.length > 0) {
    checks.push({
      id: 'agy_path',
      status: 'fail',
      message: `Native agy is shadowed by legacy binary: ${shadowed[0]} (run uninstall or remove ~/.agy-pro/bin from PATH)`
    });
  } else if (agyBins.length > 0) {
    checks.push({
      id: 'agy_path',
      status: 'pass',
      message: `Native agy found without shadowing: ${agyBins[0]}`
    });
  } else {
    checks.push({
      id: 'agy_path',
      status: 'warn',
      message: 'agy binary not found on PATH (install Antigravity CLI if not already installed)'
    });
  }

  // 3. statusLine check in settings.json
  const settingsPath = process.env.AGY_SETTINGS_PATH
    ? path.resolve(process.env.AGY_SETTINGS_PATH)
    : path.join(os.homedir(), '.gemini', 'antigravity-cli', 'settings.json');
  if (fs.existsSync(settingsPath)) {
    try {
      const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
      const sl = settings && settings.statusLine;
      if (sl && sl.enabled && sl.command) {
        checks.push({
          id: 'status_line',
          status: 'pass',
          message: `statusLine enabled in ${settingsPath}`
        });
      } else {
        checks.push({
          id: 'status_line',
          status: 'warn',
          message: `statusLine not configured in ${settingsPath} (run configure-statusline.js)`
        });
      }
    } catch (err) {
      checks.push({
        id: 'status_line',
        status: 'fail',
        message: `Invalid JSON in ${settingsPath}: ${err.message}`
      });
    }
  } else {
    checks.push({
      id: 'status_line',
      status: 'warn',
      message: `settings.json not found at ${settingsPath}`
    });
  }

  // 4. hooks.json check
  const configDir = process.env.AGY_CONFIG_DIR
    ? path.resolve(process.env.AGY_CONFIG_DIR)
    : path.join(os.homedir(), '.gemini', 'config');
  const hooksPath = path.join(configDir, 'hooks', 'hooks.json');
  if (fs.existsSync(hooksPath)) {
    try {
      const hooks = JSON.parse(fs.readFileSync(hooksPath, 'utf8'));
      const hasPost = Boolean(hooks && hooks.hooks && hooks.hooks.PostInvocation);
      checks.push({
        id: 'hooks_json',
        status: hasPost ? 'pass' : 'warn',
        message: hasPost
          ? `hooks.json valid in ${hooksPath}`
          : `hooks.json present at ${hooksPath} without PostInvocation`
      });
    } catch (err) {
      checks.push({
        id: 'hooks_json',
        status: 'fail',
        message: `Invalid JSON in ${hooksPath}: ${err.message}`
      });
    }
  } else {
    checks.push({
      id: 'hooks_json',
      status: 'warn',
      message: `hooks.json not deployed at ${hooksPath}`
    });
  }

  // 5. AGY_LLM_GATEWAY_* consistency & gateway.json validity
  let status;
  try {
    status = getGatewayStatus(envOptions);
  } catch (err) {
    checks.push({
      id: 'gateway_config',
      status: 'fail',
      message: `Failed to load ${getGatewayConfigPath(envOptions)}: ${err.message}`
    });
    status = null;
  }

  if (status) {
    if (status.mode === 'NATIVE') {
      checks.push({
        id: 'gateway_env',
        status: 'pass',
        message: `Gateway is OFF (NATIVE mode, ${status.profiles.length} saved profile(s))`
      });
    } else {
      const urlCheck = status.url ? validateGatewayURL(status.url) : { valid: true, isLoopback: false };
      const wireCheck = validateWireProtocol(status.wireProtocol || 'genai');
      const modelsValid = !status.modelsError;

      if (status.url && !urlCheck.valid) {
        checks.push({ id: 'gateway_url', status: 'fail', message: urlCheck.error });
      } else if (!wireCheck.valid) {
        checks.push({ id: 'gateway_wire', status: 'fail', message: wireCheck.error });
      } else if (!modelsValid) {
        checks.push({ id: 'gateway_models', status: 'fail', message: status.modelsError });
      } else if (status.url && !urlCheck.isLoopback && status.maskedKey === '(unset)') {
        checks.push({
          id: 'gateway_key',
          status: 'fail',
          message: 'AGY_LLM_GATEWAY_URL is remote but AGY_LLM_GATEWAY_API_KEY is unset'
        });
      } else {
        checks.push({
          id: 'gateway_env',
          status: 'pass',
          message: `Gateway active (${status.mode}, ${maskBaseURL(status.url)}, wire=${status.wireProtocol}, models=${status.models.length})`
        });
      }
    }

    if (status.bomVariants.length > 0) {
      checks.push({
        id: 'bom_env',
        status: 'warn',
        message: `Found ${status.bomVariants.length} BOM-polluted env variable name(s) in user store`
      });
    }

    if (status.adminSettings.configured) {
      checks.push({
        id: 'admin_settings',
        status: 'warn',
        message: `Enterprise admin_settings.json override present at ${status.adminSettings.path}`
      });
    }
  }

  const hasFail = checks.some((c) => c.status === 'fail');

  if (parsed.flags.json) {
    output.write(JSON.stringify({ ok: !hasFail, checks }, null, 2) + '\n');
    return hasFail ? 1 : 0;
  }

  output.write(`agy-pro doctor (v${pkg.version}):\n`);
  for (const c of checks) {
    const tag = c.status === 'pass' ? '[PASS]' : c.status === 'warn' ? '[WARN]' : '[FAIL]';
    output.write(`  ${tag} ${c.id}: ${c.message}\n`);
  }
  return hasFail ? 1 : 0;
}

/**
 * Main CLI runner. Never calls `process.exit` directly so callers/tests can inspect exit codes.
 * @param {string[]} [argv]
 * @param {{ input?: NodeJS.ReadableStream, output?: NodeJS.WritableStream, isTTY?: boolean, envOptions?: object, transport?: Function, findAgyBinaries?: Function }} [deps]
 * @returns {Promise<number>} Exit code (`0`, `1`, or `2`)
 */
async function runCli(argv = process.argv.slice(2), deps = {}) {
  const output = deps.output || process.stdout;
  const errOut = deps.errOutput || process.stderr;
  const context = {
    input: deps.input || process.stdin,
    output,
    errOutput: errOut,
    isTTY: deps.isTTY,
    envOptions: deps.envOptions || {},
    transport: deps.transport,
    findAgyBinaries: deps.findAgyBinaries
  };

  if (hasForbiddenApiKeyFlag(argv)) {
    errOut.write(
      'Error: Refusing --api-key on command line (secrets in argv leak to process lists). Use interactive setup, --api-key-stdin, or AGY_PRO_API_KEY_INPUT.\n'
    );
    return 2;
  }

  const cmd = (argv[0] || '').toLowerCase();
  const rest = argv.slice(1);

  if (!cmd || cmd === 'help' || cmd === '--help' || cmd === '-h') {
    output.write(formatHelp());
    return 0;
  }

  if (cmd === 'version' || cmd === '--version' || cmd === '-v') {
    output.write(`agy-pro v${pkg.version}\n`);
    return 0;
  }

  if (cmd === 'setup') {
    const res = await runSetup(rest, context);
    return res.exitCode;
  }

  if (cmd === 'gateway') {
    return handleGateway(rest, context);
  }

  if (cmd === 'models') {
    return handleModels(rest, context);
  }

  if (cmd === 'env') {
    return handleEnv(rest, context);
  }

  if (cmd === 'doctor') {
    return handleDoctor(rest, context);
  }

  errOut.write(`Error: Unknown command "${argv[0]}". Run \`agy-pro --help\` for usage.\n`);
  return 2;
}

module.exports = {
  formatHelp,
  runCli
};
