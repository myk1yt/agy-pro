#!/usr/bin/env node

/**
 * @fileoverview Unified Option 1 uninstaller for agy-tools and agy-pro.
 *
 * Cleanly reverses scripts/install.bat and scripts/install.sh without external
 * dependencies:
 *   1. Best-effort `npm unlink -g agy-tools`
 *   2. Removes Option 1 launchers, root resolver, and root marker
 *   3. Removes the `statusLine` slot from settings.json (with .bak.<ts> backup)
 *   4. Clears active AGY_LLM_GATEWAY_* persistent env vars and removes
 *      ~/.agy-pro/gateway.* files (unless --keep-state is specified)
 *   5. On Windows, delegates to agy-pro/scripts/uninstall.ps1 ONLY when the
 *      legacy Go shim (~/.agy-pro/bin/agy.exe) exists.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const readline = require('readline');
const { spawnSync } = require('child_process');
const { writeAtomic, getTimestamp } = require('./configure-statusline');

const GATEWAY_ENV_VARS = [
  'AGY_LLM_GATEWAY_URL',
  'AGY_LLM_GATEWAY_API_KEY',
  'AGY_LLM_GATEWAY_WIRE_PROTOCOL',
  'AGY_LLM_GATEWAY_MODELS',
  'AGY_LLM_GATEWAY_CA_CERT',
  'AGY_LLM_GATEWAY_HEADERS'
];

const RC_FILES = ['.zshenv', '.zshrc', '.bashrc', '.profile'];
const GATEWAY_BLOCK_START = '# >>> agy-pro gateway >>>';
const GATEWAY_BLOCK_END = '# <<< agy-pro gateway <<<';

const WINDOWS_CLEAR_ENV_PS = `
$ErrorActionPreference = 'Stop'
$raw = [Environment]::GetEnvironmentVariable('__AGY_CLEAR_ENV_VARS', 'Process')
if (-not $raw) { exit 0 }
$vars = $raw | ConvertFrom-Json
foreach ($name in $vars) {
  [Environment]::SetEnvironmentVariable([string]$name, $null, 'User')
  [Environment]::SetEnvironmentVariable(([char]0xFEFF + [string]$name), $null, 'User')
}
`.trim();

/**
 * Checks whether a candidate path is an existing executable file.
 * @param {string} candidate - Path to inspect.
 * @param {string} platform - Node platform string.
 * @returns {boolean} True when usable.
 */
function isExecutableFile(candidate, platform) {
  try {
    if (!fs.statSync(candidate).isFile()) return false;
    if (platform !== 'win32') {
      fs.accessSync(candidate, fs.constants.X_OK);
    }
    return true;
  } catch (_err) {
    return false;
  }
}

/**
 * Resolves the trusted Windows PowerShell executable path under SystemRoot.
 * @param {Object} [env] - Environment object.
 * @returns {string} Absolute path to powershell.exe.
 */
function resolvePowerShellPath(env = process.env) {
  const systemRoot = env.SystemRoot || env.WINDIR || 'C:\\Windows';
  return path.join(systemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
}

/**
 * Resolves the trusted Windows cmd.exe executable path under SystemRoot.
 * @param {Object} [env] - Environment object.
 * @returns {string} Absolute path to cmd.exe.
 */
function resolveCmdExePath(env = process.env) {
  const systemRoot = env.SystemRoot || env.WINDIR || 'C:\\Windows';
  return path.join(systemRoot, 'System32', 'cmd.exe');
}

/**
 * Resolves npm executable safely: Node sibling directory first, then only
 * absolute PATH directories (skipping `.` and relative entries, CWE-426).
 * @param {Object} [opts] - Resolution options.
 * @returns {string|null} Absolute path to npm/npm.cmd or null if not found.
 */
function resolveNpmPath(opts = {}) {
  const platform = opts.platform || process.platform;
  const npmName = platform === 'win32' ? 'npm.cmd' : 'npm';
  const execPath = opts.execPath || process.execPath;
  const sibling = path.join(path.dirname(execPath), npmName);
  if (isExecutableFile(sibling, platform)) {
    return path.resolve(sibling);
  }
  const pathEnv = opts.pathEnv !== undefined ? opts.pathEnv : (process.env.PATH || '');
  const sep = platform === 'win32' ? ';' : ':';
  const isAbs = (dir) => (platform === 'win32' ? path.win32.isAbsolute(dir) : path.posix.isAbsolute(dir));
  const entries = String(pathEnv)
    .split(sep)
    .map((entry) => (platform === 'win32' ? entry.trim().replace(/^"(.*)"$/, '$1') : entry.trim()))
    .filter((entry) => entry && isAbs(entry));
  for (const dir of entries) {
    const candidate = path.join(dir, npmName);
    if (isExecutableFile(candidate, platform)) {
      return path.resolve(candidate);
    }
  }
  return null;
}

/**
 * Parses CLI flags supporting both Node (--flag) and PowerShell (-Flag) styles.
 * @param {string[]} [argv] - Raw CLI arguments.
 * @returns {Object} Parsed option flags.
 */
function parseArgs(argv = process.argv.slice(2)) {
  const options = {
    dryRun: false,
    yes: false,
    keepState: false,
    help: false
  };

  for (const arg of argv) {
    const normalized = String(arg || '').toLowerCase();
    if (normalized === '--dry-run' || normalized === '-dryrun') {
      options.dryRun = true;
    } else if (normalized === '--yes' || normalized === '-y' || normalized === '--force' || normalized === '-force') {
      options.yes = true;
    } else if (normalized === '--keep-state' || normalized === '-keepstate') {
      options.keepState = true;
    } else if (normalized === '--help' || normalized === '-h') {
      options.help = true;
    }
  }

  return options;
}

/**
 * Resolves target paths from explicit options and sandbox environment overrides.
 * @param {Object} [opts] - Path override options.
 * @returns {Object} Resolved paths and sandbox metadata.
 */
function resolvePaths(opts = {}) {
  const platform = opts.platform || process.platform;
  const envHome = process.env.AGY_UNINSTALL_HOME && process.env.AGY_UNINSTALL_HOME.trim()
    ? path.resolve(process.env.AGY_UNINSTALL_HOME.trim())
    : null;
  const homeDir = opts.homeDir
    ? path.resolve(opts.homeDir)
    : (envHome || (platform === 'win32'
      ? (process.env.USERPROFILE || process.env.HOME || os.homedir())
      : (process.env.HOME || os.homedir())));

  const settingsPath = opts.settingsPath
    ? path.resolve(opts.settingsPath)
    : (process.env.AGY_SETTINGS_PATH && process.env.AGY_SETTINGS_PATH.trim()
      ? path.resolve(process.env.AGY_SETTINGS_PATH.trim())
      : path.join(homeDir, '.gemini', 'antigravity-cli', 'settings.json'));

  const agyProHome = opts.agyProHome
    ? path.resolve(opts.agyProHome)
    : (process.env.AGY_PRO_HOME && process.env.AGY_PRO_HOME.trim()
      ? path.resolve(process.env.AGY_PRO_HOME.trim())
      : path.join(homeDir, '.agy-pro'));

  const configDir = opts.configDir
    ? path.resolve(opts.configDir)
    : (process.env.AGY_CONFIG_DIR && process.env.AGY_CONFIG_DIR.trim()
      ? path.resolve(process.env.AGY_CONFIG_DIR.trim())
      : path.join(homeDir, '.gemini', 'config'));

  const isSandboxed = Boolean(
    opts.sandbox ||
    envHome ||
    process.env.AGY_PRO_HOME ||
    process.env.AGY_SETTINGS_PATH ||
    process.env.AGY_CONFIG_DIR
  );

  const launcherTargets = platform === 'win32'
    ? {
      binDir: path.join(homeDir, '.gemini', 'antigravity-cli', 'bin'),
      files: [
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-tools.bat'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'antigravity-tools.bat'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-tokens.bat'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-dashboard.bat'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-pro.bat'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-root-resolve.js'),
        path.join(homeDir, '.gemini', 'antigravity-cli', 'agy-root.txt')
      ]
    }
    : {
      binDir: path.join(homeDir, '.local', 'bin'),
      files: [
        path.join(homeDir, '.local', 'bin', 'agy-tools'),
        path.join(homeDir, '.local', 'bin', 'antigravity-tools'),
        path.join(homeDir, '.local', 'bin', 'agy-tokens'),
        path.join(homeDir, '.local', 'bin', 'agy-dashboard'),
        path.join(homeDir, '.local', 'bin', 'agy-pro'),
        path.join(homeDir, '.local', 'share', 'agy-tools-root')
      ]
    };

  return {
    platform,
    homeDir,
    settingsPath,
    agyProHome,
    configDir,
    isSandboxed,
    launcherTargets
  };
}

/**
 * Removes the `# >>> agy-pro gateway >>>` ... `# <<< agy-pro gateway <<<` block
 * from a shell rc string if present.
 * @param {string} content - Original rc file text.
 * @returns {{ changed: boolean, content: string }} Updated result.
 */
function stripGatewayRcBlock(content) {
  const escapedStart = GATEWAY_BLOCK_START.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const escapedEnd = GATEWAY_BLOCK_END.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const pattern = new RegExp(`\\r?\\n?${escapedStart}[\\s\\S]*?${escapedEnd}\\r?\\n?`, 'g');
  const updated = content.replace(pattern, '\n').replace(/^\n+/, '');
  return {
    changed: updated !== content,
    content: updated
  };
}

/**
 * Prompts the user for y/N confirmation in an interactive TTY.
 * @param {string} question - Prompt message.
 * @returns {Promise<boolean>} True if user confirmed yes.
 */
function askConfirmation(question) {
  return new Promise((resolve) => {
    const rl = readline.createInterface({
      input: process.stdin,
      output: process.stdout
    });
    rl.question(question, (answer) => {
      rl.close();
      resolve(/^(y|yes)$/i.test(String(answer || '').trim()));
    });
  });
}

/**
 * Executes or previews the Option 1 uninstallation plan.
 * @param {Object} [opts] - Uninstall options.
 * @returns {Promise<Object>} Structured execution report and exitCode.
 */
async function runUninstall(opts = {}) {
  const dryRun = Boolean(opts.dryRun);
  const yes = Boolean(opts.yes);
  const keepState = Boolean(opts.keepState);
  const log = typeof opts.log === 'function' ? opts.log : (line) => console.log(line);
  const errLog = typeof opts.errLog === 'function' ? opts.errLog : (line) => console.error(line);
  const paths = resolvePaths(opts);
  const ts = opts.timestamp || getTimestamp();

  const report = {
    dryRun,
    yes,
    keepState,
    paths,
    steps: [],
    cancelled: false,
    exitCode: 0
  };

  const prefix = dryRun ? '[DRY-RUN]' : '[INFO]';

  if (!dryRun && !yes) {
    const isInteractive = opts.isInteractive !== undefined
      ? opts.isInteractive
      : Boolean(process.stdin.isTTY && process.stdout.isTTY);

    if (!isInteractive) {
      errLog('[ERROR] Non-interactive input detected; refusing to modify disk without --yes.');
      errLog('[INFO] Re-run with --yes to confirm, or --dry-run to preview planned actions.');
      report.cancelled = true;
      report.exitCode = 2;
      return report;
    }

    const confirmed = await askConfirmation('Proceed with uninstalling agy-tools / agy-pro? [y/N]: ');
    if (!confirmed) {
      log('[SKIP] Cancelled by user. Nothing was changed.');
      report.cancelled = true;
      report.exitCode = 0;
      return report;
    }
  }

  // 1. Best-effort global npm unlink
  if ((paths.isSandboxed && typeof opts.npmRunner !== 'function') || process.env.AGY_UNINSTALL_SKIP_NPM === '1') {
    log(`${prefix} Skipping global npm unlink in sandboxed mode.`);
    report.steps.push({ step: 'npm-unlink', action: 'skipped', reason: 'sandboxed' });
  } else if (dryRun) {
    log('[DRY-RUN] Would run: npm unlink -g agy-tools');
    report.steps.push({ step: 'npm-unlink', action: 'would-run' });
  } else {
    const npmPath = resolveNpmPath({
      platform: paths.platform,
      execPath: opts.execPath,
      pathEnv: opts.pathEnv
    });
    if (!npmPath && typeof opts.npmRunner !== 'function') {
      log('[INFO] npm executable not found in Node directory or absolute PATH; skipping npm unlink.');
      report.steps.push({ step: 'npm-unlink', action: 'skipped', reason: 'npm-not-found' });
    } else {
      const runNpm = typeof opts.npmRunner === 'function'
        ? opts.npmRunner
        : (resolvedNpm, args) => {
          if (paths.platform === 'win32') {
            return spawnSync(resolveCmdExePath(), ['/d', '/c', resolvedNpm, ...args], {
              encoding: 'utf8'
            });
          }
          return spawnSync(resolvedNpm, args, { encoding: 'utf8' });
        };
      try {
        const res = runNpm(npmPath, ['unlink', '-g', 'agy-tools']);
        const ok = Boolean(res && res.status === 0);
        log(ok ? '[OK] Unlinked global npm package agy-tools.' : '[INFO] npm unlink -g agy-tools skipped or not linked.');
        report.steps.push({
          step: 'npm-unlink',
          action: ok ? 'unlinked' : 'not-linked',
          status: res ? res.status : null,
          executable: npmPath
        });
      } catch (err) {
        log(`[INFO] npm unlink -g agy-tools skipped: ${err.message}`);
        report.steps.push({ step: 'npm-unlink', action: 'skipped', reason: err.message });
      }
    }
  }

  // 2. Remove Option 1 launchers, resolver, and root marker
  const removedLaunchers = [];
  for (const filePath of paths.launcherTargets.files) {
    if (!fs.existsSync(filePath)) continue;
    if (dryRun) {
      log(`[DRY-RUN] Would remove: ${filePath}`);
      removedLaunchers.push({ path: filePath, action: 'would-remove' });
    } else {
      fs.unlinkSync(filePath);
      log(`[OK] Removed: ${filePath}`);
      removedLaunchers.push({ path: filePath, action: 'removed' });
    }
  }
  if (!dryRun && paths.platform === 'win32' && fs.existsSync(paths.launcherTargets.binDir)) {
    try {
      const remaining = fs.readdirSync(paths.launcherTargets.binDir);
      if (remaining.length === 0) {
        fs.rmdirSync(paths.launcherTargets.binDir);
        log(`[OK] Removed empty launcher directory: ${paths.launcherTargets.binDir}`);
      }
    } catch (_err) {
      // Ignore non-empty or locked directory
    }
  }
  report.steps.push({ step: 'launchers', items: removedLaunchers });

  // 3. Remove statusLine from settings.json
  if (!fs.existsSync(paths.settingsPath)) {
    log(`${prefix} settings.json not found (${paths.settingsPath}); nothing to clean.`);
    report.steps.push({ step: 'statusline', action: 'absent', settingsPath: paths.settingsPath });
  } else {
    let parsed = null;
    let valid = true;
    try {
      parsed = JSON.parse(fs.readFileSync(paths.settingsPath, 'utf8'));
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        valid = false;
      }
    } catch (_err) {
      valid = false;
    }

    if (!valid) {
      log(`[WARN] settings.json is not valid JSON (${paths.settingsPath}); leaving untouched.`);
      report.steps.push({ step: 'statusline', action: 'corrupt-skipped', settingsPath: paths.settingsPath });
    } else if (!Object.prototype.hasOwnProperty.call(parsed, 'statusLine')) {
      log(`${prefix} No statusLine slot present in ${paths.settingsPath}.`);
      report.steps.push({ step: 'statusline', action: 'missing', settingsPath: paths.settingsPath });
    } else {
      const backupPath = `${paths.settingsPath}.bak.${ts}`;
      if (dryRun) {
        log(`[DRY-RUN] Would remove statusLine from ${paths.settingsPath} (backup: ${backupPath})`);
        report.steps.push({ step: 'statusline', action: 'would-remove', settingsPath: paths.settingsPath, backupPath });
      } else {
        fs.copyFileSync(paths.settingsPath, backupPath);
        delete parsed.statusLine;
        writeAtomic(paths.settingsPath, `${JSON.stringify(parsed, null, 2)}\n`);
        log(`[OK] Removed statusLine from ${paths.settingsPath} (backup: ${backupPath})`);
        report.steps.push({ step: 'statusline', action: 'removed', settingsPath: paths.settingsPath, backupPath });
      }
    }
  }

  const psExe = resolvePowerShellPath();
  const runPs = typeof opts.psRunner === 'function'
    ? opts.psRunner
    : (exePath, args, spawnOpts) => spawnSync(exePath, args, spawnOpts);

  // 4. Clear AGY_LLM_GATEWAY_* env vars and ~/.agy-pro/gateway.* (unless --keep-state)
  if (paths.platform === 'win32') {
    if (paths.isSandboxed && typeof opts.psRunner !== 'function') {
      log(`${prefix} Skipping Windows HKCU\\Environment clearing in sandboxed mode.`);
      report.steps.push({ step: 'gateway-env', action: 'skipped', reason: 'sandboxed' });
    } else if (dryRun) {
      log(`[DRY-RUN] Would clear Windows HKCU\\Environment variables: ${GATEWAY_ENV_VARS.join(', ')}`);
      report.steps.push({ step: 'gateway-env', action: 'would-clear', vars: GATEWAY_ENV_VARS });
    } else if (!fs.existsSync(psExe) && typeof opts.psRunner !== 'function') {
      log(`[WARN] PowerShell not found at ${psExe}; skipping Windows User environment clearing.`);
      report.steps.push({ step: 'gateway-env', action: 'warn', reason: 'powershell-not-found' });
    } else {
      const psRes = runPs(psExe, ['-NoProfile', '-NonInteractive', '-Command', '-'], {
        input: WINDOWS_CLEAR_ENV_PS,
        env: Object.assign({}, process.env, { __AGY_CLEAR_ENV_VARS: JSON.stringify(GATEWAY_ENV_VARS) }),
        encoding: 'utf8'
      });
      if (psRes && psRes.status === 0) {
        log('[OK] Cleared AGY_LLM_GATEWAY_* variables from Windows User environment.');
        report.steps.push({ step: 'gateway-env', action: 'cleared', vars: GATEWAY_ENV_VARS, executable: psExe });
      } else {
        log(`[WARN] Could not clear Windows User environment variables: ${(psRes && psRes.stderr) || (psRes && psRes.status)}`);
        report.steps.push({ step: 'gateway-env', action: 'warn', status: psRes ? psRes.status : null });
      }
    }
  } else {
    const rcCleaned = [];
    for (const rcName of RC_FILES) {
      const rcPath = path.join(paths.homeDir, rcName);
      if (!fs.existsSync(rcPath)) continue;
      const original = fs.readFileSync(rcPath, 'utf8');
      const stripped = stripGatewayRcBlock(original);
      if (!stripped.changed) continue;
      if (dryRun) {
        log(`[DRY-RUN] Would remove agy-pro gateway source block from ${rcPath}`);
        rcCleaned.push({ path: rcPath, action: 'would-clean' });
      } else {
        writeAtomic(rcPath, stripped.content);
        log(`[OK] Removed agy-pro gateway source block from ${rcPath}`);
        rcCleaned.push({ path: rcPath, action: 'cleaned' });
      }
    }
    report.steps.push({ step: 'gateway-env', items: rcCleaned });
  }

  const gatewayFiles = [];
  if (fs.existsSync(paths.agyProHome)) {
    const entries = fs.readdirSync(paths.agyProHome, { withFileTypes: true });
    for (const entry of entries) {
      if (!entry.isFile() || !entry.name.startsWith('gateway.')) continue;
      const target = path.join(paths.agyProHome, entry.name);
      if (keepState) {
        log(`[KEEP] Preserving gateway state file (--keep-state): ${target}`);
        gatewayFiles.push({ path: target, action: 'kept' });
      } else if (dryRun) {
        log(`[DRY-RUN] Would remove gateway file: ${target}`);
        gatewayFiles.push({ path: target, action: 'would-remove' });
      } else {
        fs.unlinkSync(target);
        log(`[OK] Removed gateway file: ${target}`);
        gatewayFiles.push({ path: target, action: 'removed' });
      }
    }
  }
  report.steps.push({ step: 'gateway-files', items: gatewayFiles });

  // 5. Conditional legacy Go agy.exe delegation on Windows
  const legacyExe = path.join(paths.agyProHome, 'bin', 'agy.exe');
  if (paths.platform === 'win32' && fs.existsSync(legacyExe)) {
    const legacyScript = path.resolve(__dirname, '..', '..', 'agy-pro', 'scripts', 'uninstall.ps1');
    if (fs.existsSync(legacyScript)) {
      const psArgs = ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', legacyScript, '-NoPause'];
      if (dryRun) psArgs.push('-DryRun');
      if (yes) psArgs.push('-Force');
      if (keepState) psArgs.push('-KeepState');
      if (paths.isSandboxed) {
        psArgs.push('-TargetHome', paths.agyProHome);
      }
      if (!fs.existsSync(psExe) && typeof opts.psRunner !== 'function') {
        log(`[WARN] PowerShell not found at ${psExe}; skipping legacy Go uninstaller delegation.`);
        report.steps.push({ step: 'legacy-go-uninstall', action: 'powershell-missing', legacyExe });
      } else {
        log(`${prefix} Legacy Go agy.exe found at ${legacyExe}; delegating to agy-pro/scripts/uninstall.ps1...`);
        const legacyRes = runPs(psExe, psArgs, {
          encoding: 'utf8',
          stdio: opts.silent ? 'pipe' : 'inherit'
        });
        report.steps.push({
          step: 'legacy-go-uninstall',
          action: dryRun ? 'previewed' : 'delegated',
          script: legacyScript,
          executable: psExe,
          status: legacyRes ? legacyRes.status : null
        });
        if (!legacyRes || legacyRes.status !== 0) {
          report.exitCode = (legacyRes && legacyRes.status) || 1;
        }
      }
    } else {
      log(`[WARN] Legacy agy.exe found at ${legacyExe}, but ${legacyScript} is missing.`);
      report.steps.push({ step: 'legacy-go-uninstall', action: 'script-missing', legacyExe });
    }
  } else {
    report.steps.push({ step: 'legacy-go-uninstall', action: 'not-needed' });
  }

  return report;
}

/**
 * Prints CLI usage help.
 */
function printHelp() {
  console.log([
    'Usage: node scripts/lib/uninstall.js [options]',
    '',
    'Options:',
    '  --dry-run      Preview planned actions without modifying disk',
    '  --yes, -Force  Skip confirmation prompt (required in non-interactive shells)',
    '  --keep-state   Keep ~/.agy-pro/gateway.* state files while clearing active env vars',
    '  --help, -h     Show this help message'
  ].join('\n'));
}

/**
 * CLI execution entrypoint.
 */
async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    printHelp();
    process.exit(0);
  }
  try {
    const report = await runUninstall(options);
    process.exit(report.exitCode);
  } catch (err) {
    console.error(`[ERROR] Uninstall failed: ${err.message}`);
    process.exit(1);
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  GATEWAY_ENV_VARS,
  parseArgs,
  resolveCmdExePath,
  resolveNpmPath,
  resolvePaths,
  resolvePowerShellPath,
  stripGatewayRcBlock,
  runUninstall
};
