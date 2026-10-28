/**
 * @fileoverview Main entry point and CLI coordinator for Antigravity Token & Cost Tracker.
 * Integrates log parsing, local caching, date aggregation, remote price synchronization,
 * i18n, and ANSI terminal rendering.
 */

const fs = require('fs');
const path = require('path');
const config = require('./config');
const i18n = require('./i18n');
const tokenizer = require('./tokenizer');
const logParser = require('./log-parser');
const cacheManager = require('./cache-manager');
const aggregator = require('./aggregator');
const formatter = require('./formatter');
const hookHandler = require('./hook-handler');
const priceSyncer = require('./price-syncer');
const geminiQuota = require('./gemini-quota');
const htmlReport = require('./html-report');
const serve = require('./serve');
const osc8 = require('./osc8');
const dashboardLinkModule = require('./dashboard-link');

const pkg = require('../package.json');

process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE' || err.code === 'EOF' || err.syscall === 'write') {
    process.exit(0);
  }
});

/**
 * Parses raw process.argv arguments into a structured options object.
 * @param {Array<string>} argv - CLI argument array.
 * @returns {object} Parsed flags and arguments.
 */
function parseArgs(argv) {
  const args = argv.slice(2);
  const options = {
    today: false,
    yesterday: false,
    sevenDays: false,
    thirtyDays: false,
    range: null,
    session: false,
    sessionId: null,
    all: false,
    currency: null,
    lang: null,
    model: null,
    free: false,
    raw: false,
    json: false,
    hook: false,
    fresh: false,
    sync: false,
    syncQuota: false,
    prices: false,
    autoSync: false,
    noColor: false,
    help: false,
    version: false,
    html: false,
    serve: false,
    servePort: null,
    open: false,
    writeDashboard: false,
    noLink: false,
    refreshSec: null
  };

  for (let i = 0; i < args.length; i++) {
    const arg = args[i];

    if (arg === '-h' || arg === '--help') {
      options.help = true;
    } else if (arg === '-v' || arg === '--version') {
      options.version = true;
    } else if (arg === '-t' || arg === '--today') {
      options.today = true;
    } else if (arg === '-y' || arg === '--yesterday') {
      options.yesterday = true;
    } else if (arg === '--7d' || arg === '--week') {
      options.sevenDays = true;
    } else if (arg === '--30d' || arg === '--month') {
      options.thirtyDays = true;
    } else if (arg === '--range') {
      options.range = args[i + 1] || null;
      i++;
    } else if (arg.startsWith('--range=')) {
      options.range = arg.split('=')[1];
    } else if (arg === '-s' || arg === '--session') {
      options.session = true;
      if (args[i + 1] && !args[i + 1].startsWith('-')) {
        options.sessionId = args[i + 1];
        i++;
      }
    } else if (arg.startsWith('--session=')) {
      options.session = true;
      options.sessionId = arg.split('=')[1];
    } else if (arg === '-a' || arg === '--all') {
      options.all = true;
    } else if (arg === '--currency') {
      options.currency = args[i + 1] || null;
      i++;
    } else if (arg.startsWith('--currency=')) {
      options.currency = arg.split('=')[1];
    } else if (arg === '--lang') {
      options.lang = args[i + 1] || null;
      i++;
    } else if (arg.startsWith('--lang=')) {
      options.lang = arg.split('=')[1];
    } else if (arg === '--model') {
      options.model = args[i + 1] || null;
      i++;
    } else if (arg.startsWith('--model=')) {
      options.model = arg.split('=')[1];
    } else if (arg === '--free' || arg === '--no-cost') {
      options.free = true;
    } else if (arg === '--raw') {
      options.raw = true;
    } else if (arg === '--json') {
      options.json = true;
    } else if (arg === '--hook' || arg === '--badge') {
      options.hook = true;
    } else if (arg === '--fresh' || arg === '--no-cache') {
      options.fresh = true;
    } else if (arg === 'sync-prices' || arg === 'sync' || arg === '--sync' || arg === '--sync-prices') {
      options.sync = true;
    } else if (arg === 'sync-quota' || arg === '--sync-quota' || arg === 'quota' || arg === '--quota') {
      options.syncQuota = true;
    } else if (arg === 'prices' || arg === 'models' || arg === '--prices' || arg === '--models') {
      options.prices = true;
    } else if (arg === '--auto-sync') {
      options.autoSync = true;
    } else if (arg === '--html' || arg === '--dashboard') {
      options.html = true;
    } else if (arg === '--serve') {
      options.serve = true;
      // Optional inline port: --serve 8787
      if (args[i + 1] && /^\d+$/.test(args[i + 1])) {
        options.servePort = parseInt(args[i + 1], 10);
        i++;
      }
    } else if (arg.startsWith('--serve=')) {
      options.serve = true;
      options.servePort = parseInt(arg.split('=')[1], 10) || null;
    } else if (arg === '--port') {
      options.servePort = parseInt(args[i + 1], 10);
      i++;
    } else if (arg.startsWith('--port=')) {
      options.servePort = parseInt(arg.split('=')[1], 10);
    } else if (arg === '--open') {
      options.open = true;
    } else if (arg === '--write-dashboard') {
      options.writeDashboard = true;
    } else if (arg === '--no-link') {
      options.noLink = true;
    } else if (arg === '--refresh') {
      options.refreshSec = parseInt(args[i + 1], 10);
      i++;
    } else if (arg.startsWith('--refresh=')) {
      options.refreshSec = parseInt(arg.split('=')[1], 10);
    } else if (arg === '--no-color') {
      options.noColor = true;
    }
  }

  // Default to today if no specific report mode is specified
  if (
    !options.yesterday &&
    !options.sevenDays &&
    !options.thirtyDays &&
    !options.range &&
    !options.session &&
    !options.all &&
    !options.hook &&
    !options.sync &&
    !options.syncQuota &&
    !options.prices &&
    !options.html &&
    !options.serve &&
    !options.help &&
    !options.version
  ) {
    options.today = true;
  }

  return options;
}

/**
 * Opens a URL in the default browser (Windows-first, POSIX-safe).
 * @param {string} url - URL to open.
 */
function openInBrowser(url) {
  try {
    if (process.platform === 'win32') {
      const { spawn } = require('child_process');
      spawn('cmd', ['/c', 'start', '', url], { detached: true, stdio: 'ignore', windowsHide: true }).unref();
    } else if (process.platform === 'darwin') {
      const { exec } = require('child_process');
      exec(`open ${JSON.stringify(url)}`);
    } else {
      const { exec } = require('child_process');
      exec(`xdg-open ${JSON.stringify(url)}`);
    }
  } catch (_err) {
    // Non-fatal: user can open the printed URL manually
  }
}

/**
 * Main CLI runner function.
 * @param {Array<string>} [argv=process.argv] - Command line arguments.
 */
async function runCli(argv = process.argv) {
  const options = parseArgs(argv);

  if (options.noColor) {
    formatter.setColorsEnabled(false);
  }

  // Handle language configuration
  const userConfig = config.loadUserConfig();
  const targetLang = options.lang || userConfig.lang;
  if (targetLang) {
    i18n.setLocale(targetLang);
  }

  // Determine if free/no-cost quota mode is enabled
  const isFree = Boolean(options.free || userConfig.free || userConfig.noCost);

  // Version check
  if (options.version) {
    console.log(`agy-tokens v${pkg.version}`);
    return;
  }

  // Help check
  if (options.help) {
    console.log(formatter.renderHelp());
    return;
  }

  // Handle Auto-Sync if requested or configured
  if (options.autoSync) {
    try {
      const cached = priceSyncer.loadLocalSyncedPricing();
      const syncedAt = cached?._meta?.syncedAt ? new Date(cached._meta.syncedAt).getTime() : 0;
      if (!syncedAt || Date.now() - syncedAt > priceSyncer.MAX_CACHE_AGE_MS) {
        await priceSyncer.syncPricing({ silent: true });
      }
    } catch (_err) {
      // Auto-sync errors are non-blocking
    }
  }

  // 1. Sync Prices Subcommand
  if (options.sync) {
    const syncRes = await priceSyncer.syncPricing();
    if (options.json) {
      console.log(JSON.stringify(syncRes, null, 2));
      return;
    }

    if (syncRes.success) {
      console.log(`\n  ${formatter.styleText('✔', 'brightGreen')} ${formatter.styleText(i18n.t('syncSuccess', { count: syncRes.modelCount, version: syncRes.version }), 'bold')}`);
      console.log(`  ${formatter.styleText('↳', 'gray')} ${i18n.t('syncCacheSaved', { path: syncRes.path })}\n`);
    } else {
      console.log(`\n  ${formatter.styleText('⚠', 'brightYellow')} ${i18n.t('syncFailed', { error: syncRes.error, fallback: syncRes.source })}`);
      console.log(`  ${formatter.styleText('↳', 'gray')} ${i18n.t('syncCacheSaved', { path: syncRes.path })}\n`);
    }
    return;
  }

  // 1b. Sync Gemini Quota Subcommand
  if (options.syncQuota) {
    const quotaRes = await geminiQuota.fetchLiveGeminiQuota();
    if (options.json) {
      console.log(JSON.stringify(quotaRes, null, 2));
      return;
    }

    if (quotaRes.isLive) {
      console.log(`\n  ${formatter.styleText('✔', 'brightGreen')} ${formatter.styleText(i18n.t('syncQuotaSuccess', { percent: quotaRes.remainPercent, reset: quotaRes.resetFormatted || 'N/A' }), 'bold')}`);
      if (quotaRes.quota5h && quotaRes.quota7d) {
        console.log(`  ${formatter.styleText('↳', 'gray')} 5h: ${quotaRes.quota5h.remainPercent}% (${quotaRes.quota5h.resetFormatted || 'N/A'}) | 7d: ${quotaRes.quota7d.remainPercent}% (${quotaRes.quota7d.resetFormatted || 'N/A'})`);
      }
      console.log(`  ${formatter.styleText('↳', 'gray')} ${quotaRes.cacheFile}\n`);
    } else {
      console.log(`\n  ${formatter.styleText('⚠', 'brightYellow')} ${i18n.t('syncQuotaFailed', { error: quotaRes.error || 'Language server not running' })}`);
      if (quotaRes.cacheFile) {
        console.log(`  ${formatter.styleText('↳', 'gray')} ${quotaRes.cacheFile}\n`);
      }
    }
    return;
  }

  // 2. Prices / Models Catalog Table Subcommand
  if (options.prices) {
    const currency = (options.currency || userConfig.currency || 'usd').toLowerCase();
    if (options.json) {
      const catalog = priceSyncer.getSyncedPricing();
      console.log(JSON.stringify(catalog, null, 2));
      return;
    }

    const tableOutput = priceSyncer.formatPricingTable(currency, targetLang);
    console.log('\n' + tableOutput + '\n');
    return;
  }

  // 3a. Dashboard HTML generation mode (--html / --dashboard)
  if (options.html) {
    const activeModel = options.model || config.getActiveModelFromSettings();
    const currency = (options.currency || userConfig.currency || 'usd').toLowerCase();
    const syncResult = await cacheManager.syncSessions({
      forceFresh: options.fresh,
      modelName: activeModel
    });
    const payload = htmlReport.buildDashboardPayload(syncResult.sessions, {
      currency,
      lang: targetLang || i18n.getLocale(),
      isFree,
      model: activeModel,
      modelName: activeModel,
      parsedCount: syncResult.parsedCount,
      cachedCount: syncResult.cachedCount,
      elapsedMs: syncResult.elapsedMs,
      quota: userConfig.quota
    });
    htmlReport.writeDashboardFiles(payload, {
      force: true,
      refreshSec: options.refreshSec,
      servePort: options.servePort || config.DASHBOARD_DEFAULT_PORT
    });

    const htmlUrl = osc8.dashboardFileUrl();
    console.log(`\n  ${formatter.styleText('✔', 'brightGreen')} ${i18n.t('openDashboard', { url: htmlUrl })}`);
    console.log(`  ${formatter.styleText('↳', 'gray')} ${config.DASHBOARD_HTML_FILE}\n`);

    if (options.open) {
      openInBrowser(htmlUrl);
    }
    return;
  }

  // 3b. SSE dashboard server mode (--serve)
  if (options.serve) {
    const activeModel = options.model || config.getActiveModelFromSettings();
    const currency = (options.currency || userConfig.currency || 'usd').toLowerCase();
    const port = Number.isInteger(options.servePort) ? options.servePort : config.DASHBOARD_DEFAULT_PORT;
    const serverInfo = await serve.startDashboardServer({
      port,
      currency,
      lang: targetLang || i18n.getLocale(),
      isFree,
      model: activeModel,
      modelName: activeModel,
      refreshSec: options.refreshSec
    });

    // Publish the authoritative bound port so hook renders can discover the
    // running server and link to it (atomic tmp+rename; DASHBOARD-LINK/serve/001).
    // Note: The server monitors src/*.js for code updates and self-terminates (R1);
    // the statusline hook auto-respawns a fresh server on the next render.
    dashboardLinkModule.writePortFile(serverInfo.port);

    // Graceful shutdown: clear the port file ONLY while it still points at
    // this server's port, so the badge never links to a dead port and a
    // newer server's record is preserved.
    const shutdownAndExit = () => {
      dashboardLinkModule.removePortFileIfPort(serverInfo.port);
      serve.stopDashboardServer(serverInfo.server).then(() => process.exit(0));
    };
    process.once('SIGINT', shutdownAndExit);
    process.once('SIGTERM', shutdownAndExit);

    console.log(`\n  ${formatter.styleText('✔', 'brightGreen')} ${i18n.t('serveStarted', { url: serverInfo.url })}`);
    if (serverInfo.port !== port) {
      console.log(`  ${formatter.styleText('↳', 'gray')} ${i18n.t('servePortInUse', { port, nextPort: serverInfo.port })}`);
    }
    console.log('');

    if (options.open) {
      openInBrowser(serverInfo.url);
    }
    return;
  }

  // 3c. Hook / 1-line badge mode (single sync pass feeds badge + optional
  //     dashboard write — C4: no second syncSessions call)
  if (options.hook) {
    try {
      const stdinContext = await hookHandler.readStdinJson();
      const currency = (options.currency || userConfig.currency || 'usd').toLowerCase();
      const activeModel = options.model || config.getActiveModelFromSettings();

      // W1: OSC 8 hyperlink for the 📊 Dashboard badge segment. --no-link omits
      // the segment entirely; NO_COLOR/TERM=dumb degrade to plain text inside
      // formatOsc8Link (isOsc8Supported). Inside VS Code terminals file:// OSC 8
      // links open in the EDITOR by design (vscode#39278/176812), so the badge
      // links to the local http dashboard server instead (auto-started in the
      // background, 127.0.0.1 only); AGY_TOKENS_LINK_MODE=file|http forces a
      // mode. Any http failure falls back to the file:// link.
      let dashboardLink = null;
      if (!options.noLink) {
        const linkTarget = dashboardLinkModule.resolveLinkTarget();
        let linkUrl = linkTarget.url;
        if (linkTarget.mode === 'http') {
          const ensured = await dashboardLinkModule.ensureServerRunning();
          linkUrl = ensured ? ensured.url : osc8.dashboardFileUrl();
        }
        dashboardLink = osc8.formatOsc8Link(linkUrl, `📊 ${i18n.t('dashboardLink')}`);
      }

      // ONE syncSessions pass shared by badge and dashboard writer (C4)
      const syncResult = await cacheManager.syncSessions({
        forceFresh: options.fresh,
        modelName: activeModel
      });

      const rollingUsage = aggregator.getRollingUsage(syncResult.sessions, new Date(), userConfig.quota);

      const result = await hookHandler.handlePostInvocation({
        currency,
        modelName: options.model,
        isFree,
        stdinContext,
        sessions: syncResult.sessions,
        link: dashboardLink,
        rollingUsage
      });

      if (options.writeDashboard) {
        // Empty-sync guard (Fix 6): a transient empty aggregation (e.g. transcripts
        // dir briefly unreadable) must never clobber good on-disk artifacts. When
        // this sync produced zero sessions but the existing dashboard-data.json
        // still holds real data, skip the write entirely. Silent by design — the
        // statusline hook path must stay fast and quiet. (--html keeps force:true
        // semantics and is NOT guarded.)
        let skipEmptySyncWrite = false;
        if (syncResult.sessions.length === 0) {
          try {
            if (fs.existsSync(config.DASHBOARD_DATA_JSON)) {
              const prev = JSON.parse(fs.readFileSync(config.DASHBOARD_DATA_JSON, 'utf8'));
              const prevHasData = (Array.isArray(prev.models) && prev.models.length > 0) ||
                (Array.isArray(prev.daily) && prev.daily.some(d => d && d.totalTokens > 0));
              if (prevHasData) skipEmptySyncWrite = true;
            }
          } catch (_err) {
            // Unreadable/corrupt previous data: fall through and write normally.
          }
        }
        if (!skipEmptySyncWrite) {
          const payload = htmlReport.buildDashboardPayload(syncResult.sessions, {
            currency,
            lang: targetLang || i18n.getLocale(),
            isFree,
            model: activeModel,
            modelName: activeModel,
            parsedCount: syncResult.parsedCount,
            cachedCount: syncResult.cachedCount,
            elapsedMs: syncResult.elapsedMs,
            quota: userConfig.quota
          });
          htmlReport.writeDashboardFiles(payload, {
            refreshSec: options.refreshSec,
            servePort: options.servePort || config.DASHBOARD_DEFAULT_PORT
          });
        }
      }

      if (options.raw) {
        console.log(result.badge);
      } else if (options.json) {
        console.log(JSON.stringify(result, null, 2));
      } else {
        // Default PostInvocation contract for Antigravity hook runner
        console.log(JSON.stringify(hookHandler.formatHookResponse(result.badge)));
      }
    } catch (_hookErr) {
      try {
        console.log(JSON.stringify({ injectSteps: [{ ephemeralMessage: '' }] }));
      } catch (_e) { /* last resort: silent exit */ }
      process.exit(0);
    }
    return;
  }

  const activeModel = options.model || config.getActiveModelFromSettings();
  const currency = (options.currency || userConfig.currency || 'usd').toLowerCase();

  // Sync sessions with cache
  const syncResult = await cacheManager.syncSessions({
    forceFresh: options.fresh,
    modelName: activeModel
  });

  const sessions = syncResult.sessions;

  // 4. Session drilldown mode
  if (options.session) {
    const sessionDetail = aggregator.getSessionDrilldown(sessions, options.sessionId);
    if (!sessionDetail) {
      if (options.json) {
        console.log(JSON.stringify({ error: i18n.t('sessionNotFound', { id: options.sessionId || 'latest' }) }));
      } else {
        console.log(`\n  ${i18n.t('sessionNotFound', { id: options.sessionId || 'latest' })}\n`);
      }
      return;
    }

    if (options.json) {
      console.log(JSON.stringify(sessionDetail, null, 2));
    } else {
      const banner = formatter.renderBanner(
        i18n.t('periodSession'),
        activeModel,
        currency,
        sessionDetail.startTime.split('T')[0],
        isFree
      );
      const table = formatter.renderSessionTable(sessionDetail, currency, isFree);
      console.log(banner);
      console.log(table);
    }
    return;
  }

  // 5. Aggregations (Today, Yesterday, 7d, 30d, Range, All)
  let reportData = null;
  let periodTitle = '';
  let dateRange = '';

  if (options.yesterday) {
    reportData = aggregator.getYesterday(sessions, new Date(), activeModel);
    periodTitle = i18n.t('periodYesterday');
    dateRange = reportData.dateStr;
  } else if (options.sevenDays) {
    reportData = aggregator.getLastNDays(sessions, 7, new Date(), activeModel);
    periodTitle = i18n.t('period7Days');
    dateRange = reportData.dateRange;
  } else if (options.thirtyDays) {
    reportData = aggregator.getLastNDays(sessions, 30, new Date(), activeModel);
    periodTitle = i18n.t('period30Days');
    dateRange = reportData.dateRange;
  } else if (options.range) {
    try {
      reportData = aggregator.getDateRange(sessions, options.range, activeModel);
      periodTitle = i18n.t('periodRange');
      dateRange = options.range;
    } catch (err) {
      console.error(`Error: ${err.message}`);
      process.exit(1);
    }
  } else if (options.all) {
    reportData = aggregator.getAllTime(sessions, activeModel);
    periodTitle = i18n.t('periodAll');
    dateRange = reportData.dateRange;
  } else {
    // Default: Today
    reportData = aggregator.getToday(sessions, new Date(), activeModel);
    periodTitle = i18n.t('periodToday');
    dateRange = reportData.dateStr;
  }

  // Programmatic JSON Output
  if (options.json) {
    console.log(JSON.stringify({
      period: periodTitle,
      dateRange,
      model: activeModel,
      currency,
      freeQuota: isFree,
      metrics: isFree
        ? {
            ...reportData,
            costUsd: 0,
            cacheSavingsUsd: 0,
            daily: reportData.daily ? reportData.daily.map(d => ({ ...d, costUsd: 0, cacheSavingsUsd: 0 })) : undefined
          }
        : reportData,
      cacheStats: {
        totalSessions: syncResult.sessions.length,
        parsedCount: syncResult.parsedCount,
        cachedCount: syncResult.cachedCount,
        elapsedMs: syncResult.elapsedMs
      }
    }, null, 2));
    return;
  }

  // Visual Terminal Output
  const banner = formatter.renderBanner(periodTitle, activeModel, currency, dateRange, isFree);
  const summaryCard = formatter.renderSummaryMetrics(
    isFree ? { ...reportData, costUsd: 0, cacheSavingsUsd: 0 } : reportData,
    currency,
    isFree
  );

  console.log('\n' + banner + '\n');
  console.log(summaryCard + '\n');

  if (reportData.daily && reportData.daily.length > 0) {
    const dailyTable = formatter.renderDailyTable(reportData.daily, currency, reportData, isFree);
    console.log(dailyTable + '\n');
  }
}

module.exports = {
  config,
  i18n,
  tokenizer,
  logParser,
  cacheManager,
  aggregator,
  formatter,
  hookHandler,
  priceSyncer,
  geminiQuota,
  parseArgs,
  runCli
};
