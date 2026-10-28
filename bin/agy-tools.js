#!/usr/bin/env node

/**
 * @fileoverview Unified CLI dispatcher and developer toolkit gateway for Antigravity.
 * Routes subcommands (e.g., `dashboard`, `tokens`, `prices`, `sync-prices`, `usage`)
 * to their respective modules, defaulting to the Token & Cost Dashboard.
 */

const { runCli } = require('../src/index');
const pkg = require('../package.json');

/**
 * Prints the main toolkit overview help message.
 */
function printToolkitHelp() {
  console.log(`
\x1b[1m\x1b[36mAntigravity CLI Developer Toolkit (agy-tools)\x1b[0m - v${pkg.version}
\x1b[90mModular, zero-dependency developer tools for Antigravity CLI & LLM workflows\x1b[0m

\x1b[1mUsage:\x1b[0m
  agy-tools [command] [options]
  agy-dashboard [options]
  agy-tokens [options]

\x1b[1mAvailable Commands:\x1b[0m
  \x1b[33mdashboard, tokens, usage\x1b[0m   Token & Cost Dashboard (usage breakdown, cache %, costs) [Default]
  \x1b[33mprices, models\x1b[0m             Display live official API pricing catalog for all /model choices
  \x1b[33msync-prices, sync\x1b[0m          Synchronize latest official API pricing catalog from remote repo
  \x1b[33mhelp\x1b[0m                      Show this help screen
  \x1b[33mversion\x1b[0m                   Display toolkit version

\x1b[1mDashboard Options:\x1b[0m
  --today, -t                 Today's usage & cost summary (default)
  --yesterday, -y             Yesterday's usage & cost summary
  --7d, --week                7-day daily breakdown table and grand total
  --30d, --month              30-day daily breakdown table and grand total
  --range <start..end>        Custom date range aggregation (YYYY-MM-DD..YYYY-MM-DD)
  --session, -s [id]          Turn-by-turn breakdown for latest or specified conversation ID
  --all, -a                   All-time historical summary
  --currency <usd|krw|jpy|eur> Display currency
  --lang <en|ko|ja|zh>        Interface language
  --model <name>              Override model pricing
  --free, --no-cost           Display pure token metrics without dollar cost (free subscription)
  --hook, --badge             1-line real-time badge for lifecycle hooks (JSON output)
  --raw                       Print raw badge string without PostInvocation JSON wrapper
  --auto-sync                 Auto-sync pricing if older than 24 hours
  --json                      Raw JSON output

\x1b[1mExamples:\x1b[0m
  $ agy-tools prices --currency krw
  $ agy-tools prices --lang ko
  $ agy-tools sync-prices
  $ agy-tools dashboard --7d --currency krw
  $ agy-tools dashboard --range 2026-08-01..2026-08-27
  $ agy-tools dashboard --free
  $ agy-dashboard --today --lang ko
  $ agy-tokens --hook
`);
}

async function main() {
  const args = process.argv.slice(2);
  const firstArg = (args[0] || '').toLowerCase();

  if ((firstArg === 'help' || firstArg === '--help' || firstArg === '-h') && args.length === 1) {
    printToolkitHelp();
    return;
  }

  if (firstArg === 'version' || firstArg === '--version' || firstArg === '-v') {
    console.log(`agy-tools v${pkg.version}`);
    return;
  }

  if (firstArg === 'sync-prices' || firstArg === 'sync') {
    const forwardedArgv = [process.argv[0], process.argv[1], '--sync', ...args.slice(1)];
    await runCli(forwardedArgv);
    return;
  }

  if (firstArg === 'prices' || firstArg === 'models') {
    const forwardedArgv = [process.argv[0], process.argv[1], '--prices', ...args.slice(1)];
    await runCli(forwardedArgv);
    return;
  }

  if (firstArg === 'dashboard' || firstArg === 'tokens' || firstArg === 'usage') {
    // Strip the subcommand and route remaining arguments to the dashboard runner
    const forwardedArgv = [process.argv[0], process.argv[1], ...args.slice(1)];
    await runCli(forwardedArgv);
    return;
  }

  // Default: route to dashboard
  await runCli(process.argv);
}

main().catch(err => {
  console.error('\x1b[31m[Antigravity Tools Error]\x1b[0m', err.message || err);
  process.exit(1);
});
