#!/usr/bin/env node

/**
 * @fileoverview Executable CLI binary for Antigravity Web Dashboard.
 * Launches the interactive web dashboard server and browser by default.
 */

process.env.AGY_CLI_COMMAND = 'agy-dashboard';
require('./agy-tokens.js');
