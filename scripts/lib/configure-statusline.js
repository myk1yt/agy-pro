#!/usr/bin/env node

/**
 * @fileoverview Atomic configurator for the Antigravity CLI statusLine slot in
 * ~/.gemini/antigravity-cli/settings.json.
 *
 * Supports creating, merging (preserving unmanaged top-level keys), corrupt JSON
 * recovery (.corrupt.<ts>), timestamped backups (.bak.<ts>), read-only checks
 * (--check / --dry-read), and consistent cross-platform agy-tokens launcher
 * discovery.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');

const DEFAULT_COMMAND = 'agy-tokens --hook --raw --write-dashboard';

/**
 * Generates a timestamp string formatted as YYYYMMDD-HHMMSS using local time.
 * @param {Date} [date] - Optional Date object.
 * @returns {string} Timestamp string.
 */
function getTimestamp(date = new Date()) {
  const pad = (n) => String(n).padStart(2, '0');
  const year = date.getFullYear();
  const month = pad(date.getMonth() + 1);
  const day = pad(date.getDate());
  const hours = pad(date.getHours());
  const minutes = pad(date.getMinutes());
  const seconds = pad(date.getSeconds());
  return `${year}${month}${day}-${hours}${minutes}${seconds}`;
}

/**
 * Resolves the target settings.json path.
 * Supports AGY_SETTINGS_PATH environment variable override for testing.
 * @returns {string} Absolute path to settings.json.
 */
function getSettingsPath() {
  if (process.env.AGY_SETTINGS_PATH && process.env.AGY_SETTINGS_PATH.trim()) {
    return path.resolve(process.env.AGY_SETTINGS_PATH.trim());
  }
  return path.join(os.homedir(), '.gemini', 'antigravity-cli', 'settings.json');
}

/**
 * Atomically writes content to a target file via temp file + rename.
 * @param {string} filePath - Target file path.
 * @param {string|Buffer} content - Content to write.
 */
function writeAtomic(filePath, content) {
  const dir = path.dirname(filePath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }
  const tmpPath = `${filePath}.tmp.${process.pid}.${Date.now()}.${Math.random().toString(36).slice(2, 8)}`;
  fs.writeFileSync(tmpPath, content);
  try {
    fs.renameSync(tmpPath, filePath);
  } catch (_err) {
    try {
      if (fs.existsSync(filePath)) {
        fs.unlinkSync(filePath);
      }
      fs.renameSync(tmpPath, filePath);
    } catch (finalErr) {
      if (fs.existsSync(tmpPath)) {
        try { fs.unlinkSync(tmpPath); } catch (_) {}
      }
      throw finalErr;
    }
  }
}

/**
 * Checks whether a candidate path is an existing executable file.
 * @param {string} candidate - Path to inspect.
 * @param {string} platform - Node platform string.
 * @returns {boolean} True when usable as a launcher.
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
 * Resolves the agy-tokens launcher path using the shared discovery ladder:
 * - Windows: ~/.gemini/antigravity-cli/bin/agy-tokens.{bat,cmd,exe} -> PATH -> fallback
 * - POSIX:   ~/.local/bin/agy-tokens -> ~/.gemini/antigravity-cli/bin/agy-tokens -> PATH -> fallback
 *
 * @param {Object} [opts] - Discovery options.
 * @param {string} [opts.platform] - Platform override (default: process.platform).
 * @param {string} [opts.homeDir] - Home directory override.
 * @param {string} [opts.pathEnv] - PATH string override.
 * @param {string|null} [opts.fallback] - Fallback value when no absolute launcher is found (default: 'agy-tokens').
 * @returns {string|null} Resolved launcher path or fallback.
 */
function resolveTokensLauncher(opts = {}) {
  const platform = opts.platform || process.platform;
  const homeDir = opts.homeDir || (
    platform === 'win32'
      ? (process.env.USERPROFILE || process.env.HOME || os.homedir())
      : (process.env.HOME || os.homedir())
  );
  const pathEnv = opts.pathEnv !== undefined ? opts.pathEnv : (process.env.PATH || '');
  const fallback = opts.fallback !== undefined ? opts.fallback : 'agy-tokens';

  const isAbsForPlatform = (dir) => (
    platform === 'win32' ? path.win32.isAbsolute(dir) : path.posix.isAbsolute(dir)
  );

  if (platform === 'win32') {
    const fixedDir = path.join(homeDir, '.gemini', 'antigravity-cli', 'bin');
    const winNames = ['agy-tokens.bat', 'agy-tokens.cmd', 'agy-tokens.exe'];
    for (const name of winNames) {
      const candidate = path.join(fixedDir, name);
      if (isExecutableFile(candidate, platform)) {
        return path.resolve(candidate);
      }
    }
    const pathEntries = String(pathEnv)
      .split(';')
      .map((entry) => entry.trim().replace(/^"(.*)"$/, '$1'))
      .filter((entry) => entry && isAbsForPlatform(entry));
    for (const dir of pathEntries) {
      for (const name of winNames) {
        const candidate = path.join(dir, name);
        if (isExecutableFile(candidate, platform)) {
          return path.resolve(candidate);
        }
      }
    }
    return fallback;
  }

  const fixedCandidates = [
    path.join(homeDir, '.local', 'bin', 'agy-tokens'),
    path.join(homeDir, '.gemini', 'antigravity-cli', 'bin', 'agy-tokens')
  ];
  for (const candidate of fixedCandidates) {
    if (isExecutableFile(candidate, platform)) {
      return path.resolve(candidate);
    }
  }
  const pathEntries = String(pathEnv)
    .split(':')
    .map((entry) => entry.trim())
    .filter((entry) => entry && isAbsForPlatform(entry));
  for (const dir of pathEntries) {
    const candidate = path.join(dir, 'agy-tokens');
    if (isExecutableFile(candidate, platform)) {
      return path.resolve(candidate);
    }
  }
  return fallback;
}

/**
 * Validates and quotes a command path when it contains whitespace or benign
 * shell characters (such as spaces or parentheses). Rejects dangerous shell
 * injection characters (`\r`, `\n`, `"`, `` ` ``, `$(`).
 *
 * @param {string} launcherPath - Launcher binary or path.
 * @returns {string} Safely quoted command token.
 */
function quoteCommandPath(launcherPath) {
  if (!launcherPath) return '';
  if (/[\r\n"`]|\$\(/.test(launcherPath)) {
    throw new Error(`Unsafe launcher path rejected (contains disallowed shell characters): ${JSON.stringify(launcherPath)}`);
  }
  if (/[\s&()^%;,=!'$]/.test(launcherPath)) {
    return `"${launcherPath}"`;
  }
  return launcherPath;
}

/**
 * Resolves the statusLine command string.
 * Explicit custom commands override launcher discovery; default falls back to
 * the discovered agy-tokens launcher + ` --hook --raw --write-dashboard`.
 * @param {Object} [opts] - Resolution options.
 * @returns {string} Full command string for statusLine.command.
 */
function resolveStatuslineCommand(opts = {}) {
  if (opts.command && (opts.commandExplicit || opts.command !== DEFAULT_COMMAND)) {
    return opts.command;
  }
  const launcher = resolveTokensLauncher(opts);
  return `${quoteCommandPath(launcher)} --hook --raw --write-dashboard`;
}

/**
 * Parses CLI arguments into an options object.
 * @param {string[]} [args] - Process arguments.
 * @returns {Object} Parsed options.
 */
function parseArgs(args = process.argv.slice(2)) {
  const options = {
    command: DEFAULT_COMMAND,
    commandExplicit: false,
    settingsPath: null,
    force: false,
    check: false
  };

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === '--command' && i + 1 < args.length) {
      options.command = args[++i];
      options.commandExplicit = true;
    } else if (arg.startsWith('--command=')) {
      options.command = arg.slice('--command='.length);
      options.commandExplicit = true;
    } else if (arg === '--settings' && i + 1 < args.length) {
      options.settingsPath = path.resolve(args[++i]);
    } else if (arg.startsWith('--settings=')) {
      options.settingsPath = path.resolve(arg.slice('--settings='.length));
    } else if (arg === '--check' || arg === '--dry-read') {
      options.check = true;
    } else if (arg === '--force' || arg === '-f') {
      options.force = true;
    } else if (!arg.startsWith('-') && !options.commandExplicit) {
      options.command = arg;
      options.commandExplicit = true;
    }
  }

  return options;
}

/**
 * Reads the current statusLine state without creating directories, backups, or files.
 * @param {Object} [opts] - Inspection options.
 * @param {string} [opts.settingsPath] - Target settings.json path.
 * @returns {Object} Inspection report.
 */
function inspectStatusline(opts = {}) {
  const targetSettingsPath = opts.settingsPath ? path.resolve(opts.settingsPath) : getSettingsPath();
  const resolvedDefault = resolveStatuslineCommand(opts);
  const report = (status, command, message) => ({
    status,
    settingsPath: targetSettingsPath,
    command,
    matchesDefault: command === DEFAULT_COMMAND || command === resolvedDefault,
    message
  });

  if (!fs.existsSync(targetSettingsPath)) {
    return report('absent', null, `[INFO] ${targetSettingsPath} does not exist; no statusLine is configured.`);
  }

  let rawContent = '';
  try {
    rawContent = fs.readFileSync(targetSettingsPath, 'utf8');
  } catch (readErr) {
    return report('unreadable', null, `[WARN] ${targetSettingsPath} cannot be read: ${readErr.message}`);
  }

  let settings = null;
  try {
    settings = JSON.parse(rawContent);
  } catch (_parseErr) {
    return report('corrupt', null, `[WARN] ${targetSettingsPath} is not valid JSON; no statusLine can be reported.`);
  }
  if (!settings || typeof settings !== 'object' || Array.isArray(settings)) {
    return report('corrupt', null, `[WARN] ${targetSettingsPath} is not a JSON object; no statusLine can be reported.`);
  }

  if (settings.statusLine === undefined) {
    return report('missing', null, `[INFO] ${targetSettingsPath} has no statusLine slot.`);
  }
  const command = settings.statusLine && typeof settings.statusLine === 'object'
    ? settings.statusLine.command
    : null;
  return report(
    'configured',
    typeof command === 'string' ? command : null,
    `[INFO] statusLine is configured in ${targetSettingsPath}: ${command === undefined ? '(no command slot)' : command}`
  );
}

/**
 * Compares existing statusLine slot against the desired configuration.
 * @param {*} current - Existing statusLine value.
 * @param {Object} desired - Desired statusLine object.
 * @returns {boolean} True when equivalent.
 */
function statusLineMatches(current, desired) {
  return Boolean(
    current &&
    typeof current === 'object' &&
    !Array.isArray(current) &&
    current.type === desired.type &&
    current.command === desired.command &&
    current.enabled === desired.enabled &&
    current.stack_with_default === desired.stack_with_default
  );
}

/**
 * Atomically creates or merges the statusLine slot into settings.json.
 * Preserves unmanaged top-level keys, creates .bak.<ts> before modifying valid
 * files, and moves/backs up corrupt JSON to .corrupt.<ts> before recovering.
 *
 * @param {Object} [opts] - Configuration options.
 * @returns {Object} Result summary { action, settingsPath, statusLine, command, backupPath }.
 */
function configureStatusline(opts = {}) {
  const targetSettingsPath = opts.settingsPath ? path.resolve(opts.settingsPath) : getSettingsPath();
  const ts = opts.timestamp || getTimestamp();
  const desiredCommand = resolveStatuslineCommand(opts);
  const desiredStatusLine = {
    type: 'command',
    command: desiredCommand,
    enabled: true,
    stack_with_default: true
  };

  if (!fs.existsSync(targetSettingsPath)) {
    const payload = `${JSON.stringify({ statusLine: desiredStatusLine }, null, 2)}\n`;
    writeAtomic(targetSettingsPath, payload);
    return {
      action: 'created',
      settingsPath: targetSettingsPath,
      statusLine: desiredStatusLine,
      command: desiredCommand,
      backupPath: null
    };
  }

  const rawContent = fs.readFileSync(targetSettingsPath, 'utf8');
  let settings = null;
  let isCorrupt = false;
  try {
    settings = JSON.parse(rawContent);
    if (!settings || typeof settings !== 'object' || Array.isArray(settings)) {
      isCorrupt = true;
    }
  } catch (_parseErr) {
    isCorrupt = true;
  }

  if (isCorrupt) {
    const corruptBackupPath = `${targetSettingsPath}.corrupt.${ts}`;
    fs.copyFileSync(targetSettingsPath, corruptBackupPath);
    const payload = `${JSON.stringify({ statusLine: desiredStatusLine }, null, 2)}\n`;
    writeAtomic(targetSettingsPath, payload);
    return {
      action: 'recovered',
      settingsPath: targetSettingsPath,
      statusLine: desiredStatusLine,
      command: desiredCommand,
      backupPath: corruptBackupPath,
      corruptBackupPath
    };
  }

  if (statusLineMatches(settings.statusLine, desiredStatusLine) && !opts.force) {
    return {
      action: 'identical',
      settingsPath: targetSettingsPath,
      statusLine: settings.statusLine,
      command: settings.statusLine.command,
      backupPath: null
    };
  }

  const backupPath = `${targetSettingsPath}.bak.${ts}`;
  fs.copyFileSync(targetSettingsPath, backupPath);
  const updated = Object.assign({}, settings, { statusLine: desiredStatusLine });
  writeAtomic(targetSettingsPath, `${JSON.stringify(updated, null, 2)}\n`);
  return {
    action: 'updated',
    settingsPath: targetSettingsPath,
    statusLine: desiredStatusLine,
    command: desiredCommand,
    backupPath
  };
}

/**
 * CLI execution entrypoint.
 */
function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.check) {
    try {
      const result = inspectStatusline(options);
      console.log(result.message);
      process.exit(0);
    } catch (err) {
      console.error(`[ERROR] Failed to inspect settings.json: ${err.message}`);
      process.exit(1);
    }
  }

  try {
    const result = configureStatusline(options);
    if (result.action === 'identical') {
      console.log(`[INFO] statusLine already up to date in ${result.settingsPath}: ${result.command}`);
    } else if (result.action === 'recovered') {
      console.log(`[WARN] Corrupt settings.json backed up to ${result.backupPath}`);
      console.log(`[SUCCESS] Recovered settings.json with statusLine: ${result.command}`);
    } else if (result.action === 'created') {
      console.log(`[SUCCESS] Created ${result.settingsPath} with statusLine: ${result.command}`);
    } else {
      console.log(`[SUCCESS] Updated statusLine in ${result.settingsPath} (backup: ${result.backupPath}): ${result.command}`);
    }
    process.exit(0);
  } catch (err) {
    console.error(`[ERROR] Failed to configure statusLine: ${err.message}`);
    process.exit(1);
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  configureStatusline,
  inspectStatusline,
  resolveStatuslineCommand,
  resolveTokensLauncher,
  quoteCommandPath,
  getSettingsPath,
  parseArgs,
  DEFAULT_COMMAND,
  writeAtomic,
  getTimestamp
};
