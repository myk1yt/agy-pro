#!/usr/bin/env node

/**
 * @fileoverview Entry point for the `agy-pro` CLI binary.
 * Manages enterprise/provider gateway setup (`AGY_LLM_GATEWAY_*`), multi-profile
 * switching (`gateway on/off/status`), model discovery, and diagnostics without
 * shadowing the native `agy` binary.
 */

'use strict';

const { runCli } = require('../src/pro/cli');

process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE' || err.code === 'EOF' || err.syscall === 'write') {
    process.exit(0);
  }
});

runCli(process.argv.slice(2), {
  input: process.stdin,
  output: process.stdout,
  errOutput: process.stderr
})
  .then((code) => {
    process.exitCode = code;
  })
  .catch((err) => {
    process.stderr.write(`Fatal error: ${err && err.message ? err.message : String(err)}\n`);
    process.exitCode = 1;
  });
