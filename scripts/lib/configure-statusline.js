/**
 * @fileoverview Safe and idempotent statusline configurator for Antigravity CLI.
 * Merges statusLine hook configuration into ~/.gemini/antigravity-cli/settings.json
 * with zero external dependencies, atomic writes, automatic backups, and corruption recovery.
 */

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
  const pad = n => String(n).padStart(2, '0');
  const year = date.getFullYear();
  const month = pad(date.getMonth() + 1);
  const day = pad(date.getDate());
  const hours = pad(date.getHours());
  const minutes = pad(date.getMinutes());
  const seconds = pad(date.getSeconds());
  return `${year}${month}${day}-${hours}${minutes}${seconds}`;
}

/**
 * Resolves default settings.json path.
 * Supports AGY_SETTINGS_PATH environment variable override for testing.
 * @returns {string} Absolute path to settings.json.
 */
function getSettingsPath() {
  if (process.env.AGY_SETTINGS_PATH) {
    return path.resolve(process.env.AGY_SETTINGS_PATH);
  }
  return path.join(os.homedir(), '.gemini', 'antigravity-cli', 'settings.json');
}

/**
 * Atomically writes content to a target file.
 * @param {string} filePath - Target file path.
 * @param {string} content - Content to write.
 */
function writeAtomic(filePath, content) {
  const dir = path.dirname(filePath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }
  const tmpPath = `${filePath}.tmp.${Date.now()}.${Math.random().toString(36).slice(2, 8)}`;
  fs.writeFileSync(tmpPath, content, 'utf8');
  try {
    fs.renameSync(tmpPath, filePath);
  } catch (_err) {
    // Windows fallback for file locking / existing target overwrite
    try {
      if (fs.existsSync(filePath)) {
        fs.unlinkSync(filePath);
      }
      fs.renameSync(tmpPath, filePath);
    } catch (_fallbackErr) {
      fs.copyFileSync(tmpPath, filePath);
      try {
        fs.unlinkSync(tmpPath);
      } catch (_ignore) {}
    }
  }
}

/**
 * Parses CLI arguments into options object.
 * @param {string[]} args - Process arguments.
 * @returns {object} Parsed options.
 */
function parseArgs(args = process.argv.slice(2)) {
  const options = {
    command: DEFAULT_COMMAND,
    settingsPath: null,
    force: false
  };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--command' && i + 1 < args.length) {
      options.command = args[++i];
    } else if (arg.startsWith('--command=')) {
      options.command = arg.slice('--command='.length);
    } else if (arg === '--settings' && i + 1 < args.length) {
      options.settingsPath = path.resolve(args[++i]);
    } else if (arg.startsWith('--settings=')) {
      options.settingsPath = path.resolve(arg.slice('--settings='.length));
    } else if (arg === '--force') {
      options.force = true;
    } else if (!arg.startsWith('-') && options.command === DEFAULT_COMMAND) {
      options.command = arg;
    }
  }

  return options;
}

/**
 * Configures statusLine hook in settings.json.
 * @param {object} [opts] - Configuration options.
 * @param {string} [opts.command] - Statusline command.
 * @param {string} [opts.settingsPath] - Target settings.json path.
 * @param {boolean} [opts.force] - Whether to overwrite existing statusLine.
 * @returns {object} Result summary { action, settingsPath, backupPath, message }.
 */
function configureStatusline(opts = {}) {
  const command = opts.command || DEFAULT_COMMAND;
  const targetSettingsPath = opts.settingsPath || getSettingsPath();
  const dir = path.dirname(targetSettingsPath);
  const ts = getTimestamp();

  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  const statusLineConfig = {
    type: 'command',
    command: command,
    enabled: true,
    stack_with_default: true
  };

  // Case 1: settings.json does not exist -> Create new with template
  if (!fs.existsSync(targetSettingsPath)) {
    const newSettings = {
      statusLine: statusLineConfig,
      trustedWorkspaces: []
    };
    writeAtomic(targetSettingsPath, JSON.stringify(newSettings, null, 2) + '\n');
    return {
      action: 'created',
      settingsPath: targetSettingsPath,
      backupPath: null,
      message: `[SUCCESS] Created new settings.json with statusLine at ${targetSettingsPath}`
    };
  }

  // Case 2: settings.json exists -> Try reading and parsing
  let rawContent = '';
  try {
    rawContent = fs.readFileSync(targetSettingsPath, 'utf8');
  } catch (readErr) {
    throw new Error(`Failed to read settings.json at ${targetSettingsPath}: ${readErr.message}`);
  }

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

  // Case 2a: Corrupt JSON -> Backup to .corrupt.YYYYMMDD-HHMMSS.json and create fresh template
  if (isCorrupt) {
    const corruptBackupPath = path.join(dir, `settings.corrupt.${ts}.json`);
    try {
      fs.copyFileSync(targetSettingsPath, corruptBackupPath);
    } catch (_backupErr) {
      fs.writeFileSync(corruptBackupPath, rawContent, 'utf8');
    }

    const freshSettings = {
      statusLine: statusLineConfig,
      trustedWorkspaces: []
    };
    writeAtomic(targetSettingsPath, JSON.stringify(freshSettings, null, 2) + '\n');
    return {
      action: 'recovered_corrupt',
      settingsPath: targetSettingsPath,
      backupPath: corruptBackupPath,
      message: `[WARN] Corrupt settings.json backed up to ${path.basename(corruptBackupPath)}. Created new settings.json with statusLine at ${targetSettingsPath}`
    };
  }

  // Case 2b: Valid JSON, statusLine already configured -> Idempotent skip unless force
  if (settings.statusLine !== undefined && !opts.force) {
    return {
      action: 'already_configured',
      settingsPath: targetSettingsPath,
      backupPath: null,
      message: `[INFO] statusLine is already configured in ${targetSettingsPath}. Keeping existing configuration.`
    };
  }

  // Case 2c: Valid JSON, statusLine missing (or forced) -> Backup to .bak.YYYYMMDD-HHMMSS.json and merge
  const backupPath = path.join(dir, `settings.bak.${ts}.json`);
  fs.copyFileSync(targetSettingsPath, backupPath);

  settings.statusLine = statusLineConfig;
  writeAtomic(targetSettingsPath, JSON.stringify(settings, null, 2) + '\n');

  return {
    action: 'updated',
    settingsPath: targetSettingsPath,
    backupPath: backupPath,
    message: `[SUCCESS] statusLine automatically configured in ${targetSettingsPath} (backup: ${path.basename(backupPath)})`
  };
}

/**
 * CLI execution entrypoint.
 */
function main() {
  const options = parseArgs(process.argv.slice(2));
  try {
    const result = configureStatusline(options);
    console.log(result.message);
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
  getSettingsPath,
  getTimestamp,
  parseArgs,
  writeAtomic,
  DEFAULT_COMMAND
};
