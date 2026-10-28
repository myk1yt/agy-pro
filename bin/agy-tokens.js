#!/usr/bin/env node

/**
 * @fileoverview Executable CLI binary for Antigravity Token & Cost Tracker.
 */

const { runCli } = require('../src/index');

process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE' || err.code === 'EOF' || err.syscall === 'write') {
    process.exit(0);
  }
});

const isHookMode = process.argv.includes('--hook') || process.argv.includes('--badge');

runCli().catch(err => {
  if (isHookMode) {
    try {
      console.log(JSON.stringify({ injectSteps: [{ ephemeralMessage: '' }] }));
    } catch (_e) { /* silent */ }
    process.exit(0);
  }
  console.error('\x1b[31m[Antigravity Token Tracker Error]\x1b[0m', err.message || err);
  process.exit(1);
});
