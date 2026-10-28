#!/usr/bin/env node

/**
 * @fileoverview Executable CLI binary for Antigravity Token & Cost Tracker.
 */

const path = require('path');
const { runCli } = require('../src/index');

process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE' || err.code === 'EOF' || err.syscall === 'write') {
    process.exit(0);
  }
});

const isHookMode = process.argv.includes('--hook') || process.argv.includes('--badge');

// Check if invoked specifically as agy-dashboard
const invokedBase = path.basename(process.argv[1] || '').replace(/\.(js|cmd|ps1|exe|bat)$/i, '');
const envUnderscoreBase = path.basename(process.env._ || '').replace(/\.(js|cmd|ps1|exe|bat)$/i, '');
const isAgyDashboard = invokedBase === 'agy-dashboard' ||
  envUnderscoreBase === 'agy-dashboard' ||
  process.env.AGY_CLI_COMMAND === 'agy-dashboard' ||
  process.env.npm_lifecycle_event === 'dashboard';

let cliArgv = process.argv;
if (isAgyDashboard) {
  const args = process.argv.slice(2);
  const reportFlags = new Set([
    '-t', '--today',
    '-y', '--yesterday',
    '--7d', '--week',
    '--30d', '--month',
    '--range',
    '-s', '--session',
    '-a', '--all',
    '--prices', '--models',
    '--sync', 'sync', 'sync-prices', '--sync-prices',
    '--sync-quota', 'quota', 'sync-quota', '--quota',
    '--hook', '--badge',
    '-h', '--help',
    '-v', '--version'
  ]);
  const hasReportFlag = args.some(arg => reportFlags.has(arg.split('=')[0]));
  if (!hasReportFlag) {
    const hasServe = args.some(a => a === '--serve' || a.startsWith('--serve=') || a === '--html' || a === '--dashboard');
    const hasOpen = args.includes('--open');
    const hasNoOpen = args.includes('--no-open');
    const extra = [];
    if (!hasServe) extra.push('--serve');
    if (!hasOpen && !hasNoOpen) extra.push('--open');
    cliArgv = [process.argv[0], process.argv[1], ...extra, ...args];
  }
}

runCli(cliArgv).catch(err => {
  if (isHookMode) {
    try {
      console.log(JSON.stringify({ injectSteps: [{ ephemeralMessage: '' }] }));
    } catch (_e) { /* silent */ }
    process.exit(0);
  }
  console.error('\x1b[31m[Antigravity Token Tracker Error]\x1b[0m', err.message || err);
  process.exit(1);
});
