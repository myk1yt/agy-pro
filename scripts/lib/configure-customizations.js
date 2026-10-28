/**
 * @fileoverview Full Antigravity CLI Customization Suite Configurator & Deployer.
 * Deploys rules/, plugins/, skills/, and hooks/ from repository to ~/.gemini/config/
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
 * Resolves repository root directory.
 * @returns {string} Absolute path to repo root directory.
 */
function getRepoRoot() {
  return path.resolve(__dirname, '..', '..');
}

/**
 * Resolves target ~/.gemini/config directory.
 * Supports AGY_CONFIG_DIR environment variable override for testing.
 * @returns {string} Absolute path to config directory.
 */
function getConfigDir() {
  if (process.env.AGY_CONFIG_DIR) {
    return path.resolve(process.env.AGY_CONFIG_DIR);
  }
  return path.join(os.homedir(), '.gemini', 'config');
}

/**
 * Atomically writes buffer/content to a target file.
 * @param {string} filePath - Target file path.
 * @param {string|Buffer} content - Content to write.
 */
function writeAtomic(filePath, content) {
  const dir = path.dirname(filePath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }
  const tmpPath = `${filePath}.tmp.${Date.now()}.${Math.random().toString(36).slice(2, 8)}`;
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
 * Synchronizes a source directory to target directory recursively.
 * @param {string} srcDir 
 * @param {string} tgtDir 
 * @param {Object} [options]
 * @returns {Array<Object>}
 */
function syncDirectory(srcDir, tgtDir, options = {}) {
  const ts = options.timestamp || getTimestamp();
  const results = [];
  if (!fs.existsSync(srcDir)) return results;

  function traverse(currentSrc, currentTgt) {
    if (!fs.existsSync(currentTgt)) {
      fs.mkdirSync(currentTgt, { recursive: true });
    }
    const entries = fs.readdirSync(currentSrc, { withFileTypes: true });
    for (const entry of entries) {
      if (entry.name === '.git' || entry.name.endsWith('.bak') || entry.name.includes('.bak.')) continue;
      const sPath = path.join(currentSrc, entry.name);
      const tPath = path.join(currentTgt, entry.name);

      if (entry.isDirectory()) {
        traverse(sPath, tPath);
      } else if (entry.isFile()) {
        const sContent = fs.readFileSync(sPath);
        if (fs.existsSync(tPath)) {
          const tContent = fs.readFileSync(tPath);
          if (sContent.equals(tContent) && !options.force) {
            results.push({ path: tPath, action: 'identical' });
            continue;
          }
          const backupPath = `${tPath}.bak.${ts}`;
          fs.copyFileSync(tPath, backupPath);
          writeAtomic(tPath, sContent);
          results.push({ path: tPath, action: 'updated', backupPath });
        } else {
          writeAtomic(tPath, sContent);
          results.push({ path: tPath, action: 'created' });
        }
      }
    }
  }

  traverse(srcDir, tgtDir);
  return results;
}

/**
 * Deploys all customizations (rules, plugins, skills, hooks).
 * @param {Object} [options]
 * @returns {Object} Deployment summary
 */
function deployAllCustomizations(options = {}) {
  const repoRoot = options.repoRoot || getRepoRoot();
  const configDir = options.configDir || getConfigDir();
  const ts = getTimestamp();

  const categories = ['rules', 'plugins', 'skills', 'hooks'];
  const summary = {
    created: 0,
    updated: 0,
    identical: 0,
    details: {}
  };

  for (const cat of categories) {
    const src = path.join(repoRoot, cat);
    const tgt = path.join(configDir, cat);
    const results = syncDirectory(src, tgt, { force: options.force, timestamp: ts });
    summary.details[cat] = results;
    for (const r of results) {
      summary[r.action] = (summary[r.action] || 0) + 1;
    }
  }

  return summary;
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
      options.configDir = path.resolve(args[++i]);
    } else if (arg === '--source' && args[i + 1]) {
      options.repoRoot = path.resolve(args[++i]);
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
    const summary = deployAllCustomizations(options);
    console.log(`[SUCCESS] Antigravity customizations deployed: ${summary.created} created, ${summary.updated} updated, ${summary.identical} identical.`);
    process.exit(0);
  } catch (err) {
    console.error(`[ERROR] Failed to deploy customizations: ${err.message}`);
    process.exit(1);
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  deployAllCustomizations,
  syncDirectory,
  getRepoRoot,
  getConfigDir,
  getTimestamp,
  parseArgs,
  writeAtomic
};
