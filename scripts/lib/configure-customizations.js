#!/usr/bin/env node

/**
 * @fileoverview Full Antigravity CLI Customization Suite Configurator & Deployer.
 * Deploys rules/, plugins/, skills/, hooks/, agents/, references/, and optional
 * workflows/ from the repository to ~/.gemini/config/ with zero external
 * dependencies, atomic writes, automatic backups, exclusion filtering,
 * deploy-time hooks.json launcher pinning, and idempotency.
 */

'use strict';

const fs = require('fs');
const path = require('path');
const os = require('os');
const { resolveTokensLauncher, quoteCommandPath } = require('./configure-statusline');

/**
 * Supported customization categories deployed to ~/.gemini/config/.
 * Note: 'workflows' is an optional supported category — when repoRoot/workflows
 * does not exist, syncDirectory safely treats it as a no-op without requiring
 * an empty workflows/ directory in package.json "files".
 */
const CATEGORIES = ['rules', 'plugins', 'skills', 'hooks', 'agents', 'references', 'workflows'];

/**
 * Default top-level directory exclusions per category.
 * Legacy swarm/settings plugins, mode skill, and agypro-search agent stay in
 * the repository for reference/Go tests, but are excluded from user deployment.
 */
const DEFAULT_EXCLUDES = {
  plugins: new Set(['agypro_swarm', 'agypro_settings']),
  skills: new Set(['mode']),
  agents: new Set(['agypro-search'])
};

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
  if (process.env.AGY_CONFIG_DIR && process.env.AGY_CONFIG_DIR.trim()) {
    return path.resolve(process.env.AGY_CONFIG_DIR.trim());
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
 * Rewrites hooks.PostInvocation.command in hooks.json at deploy time to pin the
 * discovered absolute agy-tokens launcher path. Leaves the original buffer
 * untouched when no absolute launcher is found or when the expected slot is absent.
 *
 * @param {Buffer} rawBuffer - Raw hooks.json file bytes.
 * @param {string|null} absLauncher - Resolved absolute agy-tokens launcher path.
 * @returns {Buffer} Transformed or original buffer.
 */
function pinHooksJsonContent(rawBuffer, absLauncher) {
  if (!absLauncher || !path.isAbsolute(absLauncher)) {
    return rawBuffer;
  }
  let parsed;
  try {
    parsed = JSON.parse(rawBuffer.toString('utf8'));
  } catch (_err) {
    return rawBuffer;
  }
  if (
    !parsed ||
    typeof parsed !== 'object' ||
    !parsed.hooks ||
    typeof parsed.hooks !== 'object' ||
    !parsed.hooks.PostInvocation ||
    typeof parsed.hooks.PostInvocation !== 'object' ||
    typeof parsed.hooks.PostInvocation.command !== 'string'
  ) {
    return rawBuffer;
  }
  parsed.hooks.PostInvocation.command = `${quoteCommandPath(absLauncher)} --hook`;
  return Buffer.from(`${JSON.stringify(parsed, null, 2)}\n`, 'utf8');
}

/**
 * Synchronizes a source directory to a target directory recursively.
 * @param {string} srcDir - Source directory.
 * @param {string} tgtDir - Target directory.
 * @param {Object} [options] - Sync options.
 * @param {boolean} [options.force] - Force overwrite even when identical.
 * @param {string} [options.timestamp] - Timestamp suffix for backup files.
 * @param {Set<string>|string[]} [options.excludes] - Top-level subdirectory names to skip.
 * @param {Function} [options.transformFile] - Optional `(srcPath, relPath, buf) => Buffer` transform.
 * @returns {Array<Object>} List of file deployment records.
 */
function syncDirectory(srcDir, tgtDir, options = {}) {
  const ts = options.timestamp || getTimestamp();
  const results = [];
  if (!fs.existsSync(srcDir)) return results;

  const excludeSet = options.excludes instanceof Set
    ? options.excludes
    : new Set(options.excludes || []);

  function traverse(currentSrc, currentTgt, relPrefix) {
    if (!fs.existsSync(currentTgt)) {
      fs.mkdirSync(currentTgt, { recursive: true });
    }
    const entries = fs.readdirSync(currentSrc, { withFileTypes: true });
    for (const entry of entries) {
      if (entry.name === '.git' || entry.name.endsWith('.bak') || entry.name.includes('.bak.')) {
        continue;
      }
      if (currentSrc === srcDir && excludeSet.has(entry.name)) {
        continue;
      }
      const sPath = path.join(currentSrc, entry.name);
      const tPath = path.join(currentTgt, entry.name);
      const relPath = relPrefix ? `${relPrefix}/${entry.name}` : entry.name;

      if (entry.isDirectory()) {
        traverse(sPath, tPath, relPath);
      } else if (entry.isFile()) {
        let sContent = fs.readFileSync(sPath);
        if (typeof options.transformFile === 'function') {
          sContent = options.transformFile(sPath, relPath, sContent);
        }
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

  traverse(srcDir, tgtDir, '');
  return results;
}

/**
 * Deploys all customizations across supported categories.
 * @param {Object} [options] - Deployment options.
 * @returns {Object} Deployment summary.
 */
function deployAllCustomizations(options = {}) {
  const repoRoot = options.repoRoot || getRepoRoot();
  const configDir = options.configDir || getConfigDir();
  const ts = options.timestamp || getTimestamp();

  const absLauncher = options.tokensLauncher !== undefined
    ? options.tokensLauncher
    : resolveTokensLauncher({
      homeDir: options.homeDir,
      pathEnv: options.pathEnv,
      platform: options.platform,
      fallback: null
    });

  const summary = {
    created: 0,
    updated: 0,
    identical: 0,
    details: {}
  };

  for (const cat of CATEGORIES) {
    const src = path.join(repoRoot, cat);
    const tgt = path.join(configDir, cat);
    const excludes = options.excludes && options.excludes[cat]
      ? options.excludes[cat]
      : DEFAULT_EXCLUDES[cat];

    const transformFile = cat === 'hooks'
      ? (_sPath, relPath, buf) => (relPath === 'hooks.json' ? pinHooksJsonContent(buf, absLauncher) : buf)
      : undefined;

    const results = syncDirectory(src, tgt, {
      force: options.force,
      timestamp: ts,
      excludes,
      transformFile
    });
    summary.details[cat] = results;
    for (const r of results) {
      summary[r.action] = (summary[r.action] || 0) + 1;
    }
  }

  return summary;
}

/**
 * Parses CLI arguments.
 * @param {string[]} [args] - CLI arguments.
 * @returns {Object} Parsed options.
 */
function parseArgs(args = []) {
  const options = { force: false };
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === '--force' || arg === '-f') {
      options.force = true;
    } else if (arg === '--target' && args[i + 1]) {
      options.configDir = path.resolve(args[++i]);
    } else if (arg.startsWith('--target=')) {
      options.configDir = path.resolve(arg.slice('--target='.length));
    } else if (arg === '--source' && args[i + 1]) {
      options.repoRoot = path.resolve(args[++i]);
    } else if (arg.startsWith('--source=')) {
      options.repoRoot = path.resolve(arg.slice('--source='.length));
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
  CATEGORIES,
  DEFAULT_EXCLUDES,
  deployAllCustomizations,
  pinHooksJsonContent,
  syncDirectory,
  getRepoRoot,
  getConfigDir,
  getTimestamp,
  parseArgs,
  writeAtomic
};
