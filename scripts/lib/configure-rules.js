/**
 * @fileoverview Safe and idempotent agent rules and protocol configurator for Antigravity CLI.
 * Deploys AGENTS.md and GEMINI.md from repository rules/ to ~/.gemini/config/rules/
 * with zero external dependencies, atomic writes, automatic backups, and idempotency.
 */

const fs = require('fs');
const path = require('path');
const os = require('os');

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
 * Resolves source rules directory.
 * @returns {string} Absolute path to repo rules directory.
 */
function getSourceRulesDir() {
  return path.resolve(__dirname, '..', '..', 'rules');
}

/**
 * Resolves target rules directory.
 * Supports AGY_RULES_DIR environment variable override for testing.
 * @returns {string} Absolute path to target rules directory.
 */
function getTargetRulesDir() {
  if (process.env.AGY_RULES_DIR) {
    return path.resolve(process.env.AGY_RULES_DIR);
  }
  return path.join(os.homedir(), '.gemini', 'config', 'rules');
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
 * Deploys agent rules from repo to target directory.
 * @param {Object} [options]
 * @param {boolean} [options.force=false]
 * @param {string} [options.sourceDir]
 * @param {string} [options.targetDir]
 * @returns {Array<Object>} Deployment results per file.
 */
function configureRules(options = {}) {
  const srcDir = options.sourceDir || getSourceRulesDir();
  const tgtDir = options.targetDir || getTargetRulesDir();
  const ts = getTimestamp();

  if (!fs.existsSync(srcDir)) {
    throw new Error(`Source rules directory not found: ${srcDir}`);
  }

  if (!fs.existsSync(tgtDir)) {
    fs.mkdirSync(tgtDir, { recursive: true });
  }

  const ruleFiles = fs.readdirSync(srcDir).filter(f => f.endsWith('.md'));
  const results = [];

  for (const file of ruleFiles) {
    const srcFile = path.join(srcDir, file);
    const tgtFile = path.join(tgtDir, file);
    const srcContent = fs.readFileSync(srcFile, 'utf8');

    if (fs.existsSync(tgtFile)) {
      const tgtContent = fs.readFileSync(tgtFile, 'utf8');
      if (srcContent === tgtContent && !options.force) {
        results.push({
          file,
          action: 'identical',
          targetPath: tgtFile,
          backupPath: null,
          message: `[INFO] Rule ${file} is identical in ${tgtDir}. Skipped.`
        });
        continue;
      }

      // Backup existing file
      const backupPath = path.join(tgtDir, `${file}.bak.${ts}`);
      fs.copyFileSync(tgtFile, backupPath);

      writeAtomic(tgtFile, srcContent);
      results.push({
        file,
        action: 'updated',
        targetPath: tgtFile,
        backupPath,
        message: `[SUCCESS] Updated ${file} in ${tgtDir} (backup: ${path.basename(backupPath)})`
      });
    } else {
      writeAtomic(tgtFile, srcContent);
      results.push({
        file,
        action: 'created',
        targetPath: tgtFile,
        backupPath: null,
        message: `[SUCCESS] Installed new rule ${file} in ${tgtDir}`
      });
    }
  }

  return results;
}

/**
 * Parses CLI arguments.
 */
function parseArgs(args = []) {
  const options = { force: false };
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--force' || arg === '-f') {
      options.force = true;
    } else if (arg === '--target' && args[i + 1]) {
      options.targetDir = path.resolve(args[++i]);
    } else if (arg === '--source' && args[i + 1]) {
      options.sourceDir = path.resolve(args[++i]);
    }
  }
  return options;
}

/**
 * CLI execution entrypoint.
 */
function main() {
  const options = parseArgs(process.argv.slice(2));
  try {
    const results = configureRules(options);
    for (const r of results) {
      console.log(r.message);
    }
    process.exit(0);
  } catch (err) {
    console.error(`[ERROR] Failed to configure agent rules: ${err.message}`);
    process.exit(1);
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  configureRules,
  getSourceRulesDir,
  getTargetRulesDir,
  getTimestamp,
  parseArgs,
  writeAtomic
};
