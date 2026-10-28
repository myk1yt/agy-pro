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
  \x1b[33mdashboard\x1b[0m                 Reports below, or the web dashboard when no report flag is given
  \x1b[33mtokens, usage\x1b[0m             Token & Cost Dashboard reports (default command)
  \x1b[33mprices, models\x1b[0m        Display live official API pricing catalog for all /model choices
  \x1b[33msync-prices, sync\x1b[0m         Synchronize latest official API pricing catalog from remote repo
  \x1b[33msync-quota, quota\x1b[0m         Fetch live Gemini quota from the Language Server
  \x1b[33mhelp\x1b[0m                      Show this help screen
  \x1b[33mversion\x1b[0m                   Display toolkit version

\x1b[1mReport Options:\x1b[0m
  --today, -t                 Today's usage & cost summary (default)
  --yesterday, -y             Yesterday's usage & cost summary
  --7d, --week                7-day daily breakdown table and grand total
  --30d, --month              30-day daily breakdown table and grand total
  --range <start..end>        Custom date range aggregation (YYYY-MM-DD..YYYY-MM-DD)
  --session, -s [id]          Turn-by-turn breakdown for latest or specified conversation ID
  --all, -a                   All-time historical summary

\x1b[1mFormat Options:\x1b[0m
  --currency <usd|krw|jpy|eur|gbp> Display currency
  --lang <en|ko|ja|zh|zh-TW|hi|vi|id|th|de|fr|es|pt|it|nl|pl|sv|ru|ar|he|tr> Interface language
  --model <name>              Override model pricing lookup
  --free, --no-cost           Display pure token metrics without dollar cost (free subscription)
  --json                      Raw JSON output
  --no-color                  Disable ANSI colors

\x1b[1mData Options:\x1b[0m
  --fresh, --no-cache         Bypass the session cache and re-parse logs
  --auto-sync                 Auto-sync pricing if the local catalog is older than 24 hours

\x1b[1mCatalog & Quota Commands:\x1b[0m (also accepted as flags: --prices, --sync, --sync-quota)
  prices, models              Print the pricing catalog table (--currency and --json apply)
  sync-prices, sync           Re-download the official pricing catalog from the remote repo
  sync-quota, quota           Refresh live Gemini quota (5h / 7d windows, --json supported)

\x1b[1mHook / Badge Options:\x1b[0m (used with --hook)
  --hook, --badge             1-line real-time badge for lifecycle hooks (PostInvocation JSON output)
  --raw                       Print the raw badge string without the PostInvocation JSON wrapper
  --write-dashboard           Also write dashboard files during the hook render
  --no-link                   Omit the OSC 8 dashboard link segment from the badge

\x1b[1mDashboard Options:\x1b[0m
  --html, --dashboard         Generate dashboard HTML files and print the file link
  --serve [port]              Start the live SSE dashboard server (default port 8787)
  --serve=<port>, --port <n>  Bind the dashboard server to a specific port
  --open                      Open the dashboard in the browser (agy-dashboard default)
  --no-open                   Do not open the browser
  --refresh <sec>             Dashboard auto-refresh interval in seconds

\x1b[1mGeneral:\x1b[0m
  --help, -h                  Show this help screen
  --version, -v               Display toolkit version

\x1b[1mExamples:\x1b[0m
  $ agy-tools                                  # today's summary (default report)
  $ agy-tools --7d --currency krw              # 7-day report in KRW
  $ agy-tools --range 2026-08-01..2026-08-27 --currency gbp
  $ agy-tools --session abc123 --json          # turn-by-turn drilldown
  $ agy-tools --all --free --no-color          # all-time tokens only, plain text
  $ agy-tools prices --currency eur            # pricing catalog table
  $ agy-tools sync-prices                      # refresh the pricing catalog
  $ agy-tools sync-quota                       # refresh live Gemini quota
  $ agy-tokens --hook --raw                    # statusline badge (raw string)
  $ agy-dashboard                              # serve + open (port 8787)
  $ agy-dashboard --serve 9000 --no-open       # headless server on port 9000
  $ agy-dashboard --html --open --refresh 5    # static HTML, 5s auto-refresh
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

  if (firstArg === 'dashboard') {
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
    const subArgs = args.slice(1);
    const hasReportFlag = subArgs.some(arg => reportFlags.has(arg.split('=')[0]));

    if (hasReportFlag) {
      const forwardedArgv = [process.argv[0], process.argv[1], ...subArgs];
      await runCli(forwardedArgv);
      return;
    }

    // Default for `dashboard`: launch the web dashboard
    const hasServe = subArgs.some(a => a === '--serve' || a.startsWith('--serve=') || a === '--html' || a === '--dashboard');
    const hasOpen = subArgs.includes('--open');
    const hasNoOpen = subArgs.includes('--no-open');
    const extra = [];
    if (!hasServe) extra.push('--serve');
    if (!hasOpen && !hasNoOpen) extra.push('--open');

    const forwardedArgv = [process.argv[0], process.argv[1], ...extra, ...subArgs];
    await runCli(forwardedArgv);
    return;
  }

  if (firstArg === 'tokens' || firstArg === 'usage') {
    // Strip the subcommand and route remaining arguments to the token tracker CLI
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
