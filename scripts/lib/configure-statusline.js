/**
 * @fileoverview RETIRED WRITER - agy-pro owns settings.json now.
 *
 * This script used to merge the statusLine hook into
 * ~/.gemini/antigravity-cli/settings.json (create, merge, corrupt recovery,
 * timestamped backups). agy-pro's Go settings door
 * (agy-pro/internal/integrations/settings.go, P5-3 D5) owns that slot through the
 * agysettings ledger, so every write path here is gone: calling one raises a
 * typed refusal instead of writing, and the CLI exits with a dedicated code.
 *
 * What is left is read-only inspection for a person asking "what is configured
 * right now": neither installer calls this script any more (the Go integrations
 * engine deploys the slot):
 *
 *   node scripts/lib/configure-statusline.js --check [--settings <path>]
 *
 * The verb that replaces the write is
 * `agy pro integrations apply statusline --yes`.
 */

const fs = require('fs');
const path = require('path');
const os = require('os');

const DEFAULT_COMMAND = 'agy-tokens --hook --raw --write-dashboard';

/**
 * Exit code of the retired write path. It is deliberately not 1: a caller can
 * tell "the tool refused because another owner took the file" (3) from "the tool
 * broke" (1) without parsing prose.
 * @type {number}
 */
const DEPRECATED_EXIT_CODE = 3;

/**
 * The refusal sentence, printed verbatim and carried by the error. The command it
 * names is the verb that replaces this script: the explicit `statusline` operand
 * (the retired `--feature` flag form is not a door spelling; the installers run
 * the fuller `agy pro integrations apply tools --yes`).
 * @type {string}
 */
const DEPRECATION_MESSAGE =
  '[deprecated] agy-pro now owns settings.json; run: agy pro integrations apply statusline --yes';

/**
 * Raised by every retired write path. `exitCode` lets a caller that cannot print
 * map the refusal onto the documented status.
 */
class DeprecatedWriterError extends Error {
  /** @param {string} [detail] - Optional extra sentence for the caller. */
  constructor(detail = '') {
    super(detail ? `${DEPRECATION_MESSAGE} (${detail})` : DEPRECATION_MESSAGE);
    this.name = 'DeprecatedWriterError';
    this.deprecated = true;
    this.exitCode = DEPRECATED_EXIT_CODE;
  }
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
 * The one way a write path can end now: it refuses. Every retired branch below
 * (create, merge, corrupt recovery) is a single line, so no branch can quietly
 * regrow a writer.
 * @param {string} [detail] - Optional extra sentence for the caller.
 * @returns {never} Always throws.
 */
function refuseWrite(detail = '') {
  throw new DeprecatedWriterError(detail);
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
    force: false,
    check: false
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
    } else if (arg === '--check' || arg === '--dry-read') {
      // The read-only mode: it prints the current state and writes nothing.
      options.check = true;
    } else if (arg === '--force') {
      options.force = true;
    } else if (!arg.startsWith('-') && options.command === DEFAULT_COMMAND) {
      options.command = arg;
    }
  }

  return options;
}

/**
 * Reads the current statusLine state without writing anything.
 *
 * This is the whole surviving read surface: a person asking "what is configured?"
 * uses it, and it never creates a directory, a backup, or a file.
 * @param {object} [opts] - Inspection options.
 * @param {string} [opts.settingsPath] - Target settings.json path.
 * @returns {object} { status, settingsPath, command, matchesDefault, message }.
 *   status is one of: absent (no file), missing (no statusLine slot),
 *   configured, corrupt (not JSON / not an object), unreadable.
 */
function inspectStatusline(opts = {}) {
  const targetSettingsPath = opts.settingsPath || getSettingsPath();
  const report = (status, command, message) => ({
    status,
    settingsPath: targetSettingsPath,
    command,
    matchesDefault: command === DEFAULT_COMMAND,
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
  const suffix = command === DEFAULT_COMMAND
    ? ' (the Node-era bare command: agy-pro replaces it with the absolute wrapper)'
    : '';
  return report(
    'configured',
    typeof command === 'string' ? command : null,
    `[INFO] statusLine is configured in ${targetSettingsPath}: ${command === undefined ? '(no command slot)' : command}${suffix}`
  );
}

/**
 * The retired writer. It used to create, merge and repair settings.json; every
 * one of those branches now ends in the same typed refusal.
 *
 * It is kept exported on purpose: a caller that still invokes it gets an error it
 * can branch on (err.deprecated, err.exitCode) instead of a silent no-op that
 * reads as success.
 * @param {object} [opts] - Retired options (command, settingsPath, force).
 * @returns {never} Always throws a DeprecatedWriterError.
 */
function configureStatusline(opts = {}) {
  const detail = opts.settingsPath
    ? `nothing was written to ${opts.settingsPath}`
    : 'nothing was written to ~/.gemini/antigravity-cli/settings.json';
  return refuseWrite(detail);
}

/**
 * CLI execution entrypoint.
 *
 * `--check` / `--dry-read` prints the current state and exits 0. Any other
 * invocation is a retired write: the refusal goes to stderr and the exit code is
 * DEPRECATED_EXIT_CODE, so an old script that still calls this step fails loudly
 * instead of pretending the statusLine was configured.
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
  console.error(DEPRECATION_MESSAGE);
  console.error('[deprecated] nothing was written. Use --check (or --dry-read) to print the current state.');
  process.exit(DEPRECATED_EXIT_CODE);
}

if (require.main === module) {
  main();
}

module.exports = {
  configureStatusline,
  inspectStatusline,
  getSettingsPath,
  parseArgs,
  DEFAULT_COMMAND,
  DEPRECATED_EXIT_CODE,
  DEPRECATION_MESSAGE,
  DeprecatedWriterError
};
