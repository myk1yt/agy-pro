/**
 * @fileoverview Real-time HTML dashboard generator for the Antigravity Token &
 * Cost Tracker. Builds the DashboardPayload (single source of truth), renders a
 * single-file offline-capable dashboard.html (inline CSS/JS + SVG chart +
 * script-tag polling + SSE auto-upgrade), and writes all artifacts atomically.
 * Zero dependencies (Node core: fs, path only).
 *
 * Transport constraints honored here:
 * - C3: file:// pages cannot use fetch()/XHR (CORS) -> classic <script src>
 *   injection polling is the sanctioned refresh transport.
 * - C5: all writes are atomic (tmp + rename, mirroring cache-manager saveCache),
 *   with a 100ms retry and direct-write fallback for Windows EPERM/EBUSY (E2).
 */

const fs = require('fs');
const path = require('path');
const {
  DASHBOARD_DIR: CONFIG_DASHBOARD_DIR,
  DASHBOARD_HTML_FILE: CONFIG_DASHBOARD_HTML_FILE,
  DASHBOARD_DATA_JS: CONFIG_DASHBOARD_DATA_JS,
  DASHBOARD_DATA_JSON: CONFIG_DASHBOARD_DATA_JSON,
  DASHBOARD_DEFAULT_PORT,
  DASHBOARD_WRITE_THROTTLE_MS,
  CURRENCIES,
  calculateCostUsd,
  calculateCacheSavingsUsd
} = require('./config');

// Dashboard artifact paths: overridable module state (test hook, mirrors the
// resetDashboardWriteState() pattern). Defaults to the production config
// constants; _setDashboardDirForTests() re-points them at a temp directory so
// tests never write to the real ~/.gemini/antigravity-dashboard/ directory.
let DASHBOARD_DIR = CONFIG_DASHBOARD_DIR;
let DASHBOARD_HTML_FILE = CONFIG_DASHBOARD_HTML_FILE;
let DASHBOARD_DATA_JS = CONFIG_DASHBOARD_DATA_JS;
let DASHBOARD_DATA_JSON = CONFIG_DASHBOARD_DATA_JSON;
const { formatLocalDate, summarizeTurns, getRollingUsage } = require('./aggregator');
const geminiQuotaModule = require('./gemini-quota');
const { t, getLocale, getAllTranslations, isRtl } = require('./i18n');

const DASHBOARD_PAYLOAD_VERSION = 3;
const DASHBOARD_DEFAULT_REFRESH_SEC = 5;

/**
 * Module-level write throttle state (content-hash skip + minimum interval).
 * Reset via resetDashboardWriteState() (used by tests).
 */
let lastWriteAt = 0;
let lastDataHash = null;

/**
 * Rounds a number to 6 decimal places to keep the payload compact.
 * @param {number} n
 * @returns {number}
 */
function round6(n) {
  return Math.round(Number(n) * 1e6) / 1e6;
}

/**
 * Normalizes one aggregator daily row into the DailyRow schema (report §1.4).
 * @param {object} row - Aggregator day summary.
 * @returns {object} DailyRow.
 */
function toDailyRow(row) {
  return {
    date: row.date,
    sessions: row.sessions || 0,
    turns: row.totalTurns || 0,
    inputTokens: row.inputTokens || 0,
    cachedTokens: row.cachedTokens || 0,
    outputTokens: row.outputTokens || 0,
    totalTokens: row.totalTokens || 0,
    cacheHitRate: round6(row.cacheHitRate || 0),
    costUsd: round6(row.costUsd || 0),
    cacheSavingsUsd: round6(row.cacheSavingsUsd || 0)
  };
}

/**
 * Builds the DashboardPayload from a pre-synced sessions array.
 * Reuses the SAME syncSessions() result as the badge (C4 — no second sync).
 *
 * Performance: buckets every turn into per-date maps in ONE pass over the
 * sessions array (single Date parse per turn), then derives today/yesterday/
 * 7d/30d summaries from the buckets — equivalent to four aggregator passes
 * at a quarter of the Date-parsing cost (statusline <20ms script-work budget).
 * @param {Array<object>} sessions - Parsed session objects.
 * @param {object} [opts]
 * @param {string} [opts.currency='usd'] - Currency code.
 * @param {string} [opts.lang] - UI language code.
 * @param {boolean} [opts.isFree=false] - Free quota mode.
 * @param {string} [opts.model] - Active model display id.
 * @param {string} [opts.modelName] - Model name used for pricing lookups.
 * @param {Date} [opts.refDate] - Reference date (defaults to now).
 * @param {number} [opts.parsedCount=0] - Cache stats: freshly parsed sessions.
 * @param {number} [opts.cachedCount=0] - Cache stats: reused cached sessions.
 * @param {number} [opts.elapsedMs=0] - Cache stats: sync duration.
 * @returns {object} DashboardPayload per report §1.4.
 */
function buildDashboardPayload(sessions, opts = {}) {
  const refDate = opts.refDate || new Date();
  const modelName = opts.modelName || null;
  const list = Array.isArray(sessions) ? sessions : [];
  const lang = opts.lang || getLocale() || 'en';
  const i18n = getAllTranslations(lang);

  // Date keys for the 30-day window (oldest -> newest)
  const dateKeys = [];
  for (let i = 29; i >= 0; i--) {
    const d = new Date(refDate);
    d.setDate(d.getDate() - i);
    dateKeys.push(formatLocalDate(d));
  }
  const todayKey = dateKeys[29];
  const yesterdayKey = dateKeys[28];
  const last7Keys = new Set(dateKeys.slice(23));

  // One pass: bucket turns per date + track session membership
  const turnsByDate = new Map();
  const sessionIdsByDate = new Map();
  const dailyModelsMap = new Map();
  const dailyModelSessions = new Map();
  for (const key of dateKeys) {
    turnsByDate.set(key, []);
    sessionIdsByDate.set(key, new Set());
    dailyModelsMap.set(key, {});
    dailyModelSessions.set(key, {});
  }

  // Per-model accumulators (v3.4 / REQ-305, REQ-306): aggregated by turn.modelName
  // with fallback chain: turn.modelName -> session.modelName -> opts.modelName -> 'unknown'.
  // Each session contributes +1 to modelRow.sessions for every model active in >=1 of its turns.
  const modelsMap = new Map();

  for (const session of list) {
    if (!session || !Array.isArray(session.turns)) continue;
    const sessionFallbackModel = session.modelName || modelName || 'unknown';
    const modelsSeenInSession = new Set();

    for (const turn of session.turns) {
      if (!turn) continue;
      const turnModel = turn.modelName || sessionFallbackModel;
      let modelRow = modelsMap.get(turnModel);
      if (!modelRow) {
        modelRow = {
          model: turnModel,
          displayName: turnModel,
          totalTokens: 0,
          inputTokens: 0,
          cachedTokens: 0,
          outputTokens: 0,
          cacheHitRate: 0,
          costUsd: 0,
          cacheSavingsUsd: 0,
          sessions: 0,
          turns: 0
        };
        modelsMap.set(turnModel, modelRow);
      }
      if (!modelsSeenInSession.has(turnModel)) {
        modelRow.sessions += 1;
        modelsSeenInSession.add(turnModel);
      }
      modelRow.turns += 1;
      modelRow.inputTokens += turn.inputTokens || 0;
      modelRow.cachedTokens += turn.cachedTokens || 0;
      modelRow.outputTokens += turn.outputTokens || 0;

      const turnCost = (typeof turn.costUsd === 'number')
        ? turn.costUsd
        : calculateCostUsd(turn.inputTokens || 0, turn.cachedTokens || 0, turn.outputTokens || 0, turnModel);
      const turnSavingsUsd = (typeof turn.cacheSavingsUsd === 'number')
        ? turn.cacheSavingsUsd
        : calculateCacheSavingsUsd(turn.cachedTokens || 0, turnModel);
      modelRow.costUsd += turnCost;
      modelRow.cacheSavingsUsd += turnSavingsUsd;

      const key = formatLocalDate(new Date(turn.createdAt));
      if (turnsByDate.has(key)) {
        turnsByDate.get(key).push(turn);
        sessionIdsByDate.get(key).add(session.sessionId !== undefined ? session.sessionId : session);

        const dateModelMap = dailyModelsMap.get(key);
        if (dateModelMap) {
          if (!dateModelMap[turnModel]) {
            dateModelMap[turnModel] = {
              model: turnModel,
              displayName: turnModel,
              totalTokens: 0,
              inputTokens: 0,
              cachedTokens: 0,
              outputTokens: 0,
              cacheHitRate: 0,
              costUsd: 0,
              cacheSavingsUsd: 0,
              sessions: 0,
              turns: 0
            };
          }
          const dm = dateModelMap[turnModel];
          dm.inputTokens += turn.inputTokens || 0;
          dm.cachedTokens += turn.cachedTokens || 0;
          dm.outputTokens += turn.outputTokens || 0;
          dm.costUsd += turnCost;
          dm.cacheSavingsUsd += turnSavingsUsd;
          dm.turns += 1;

          const dateSessionMap = dailyModelSessions.get(key);
          if (dateSessionMap) {
            if (!dateSessionMap[turnModel]) {
              dateSessionMap[turnModel] = new Set();
            }
            dateSessionMap[turnModel].add(session.sessionId !== undefined ? session.sessionId : session);
          }
        }
      }
    }
  }

  // Finalize per-model rows: totals + cache hit rate, sorted by cost desc.
  const models = Array.from(modelsMap.values())
    .map(row => {
      row.totalTokens = row.inputTokens + row.cachedTokens + row.outputTokens;
      row.cacheHitRate = row.inputTokens + row.cachedTokens > 0
        ? (row.cachedTokens / (row.inputTokens + row.cachedTokens)) * 100
        : 0;
      row.costUsd = round6(row.costUsd);
      row.cacheSavingsUsd = round6(row.cacheSavingsUsd);
      return row;
    })
    .sort((a, b) => b.costUsd - a.costUsd || b.totalTokens - a.totalTokens);

  // Finalize dailyModels: per-date per-model map
  const dailyModels = {};
  for (const key of dateKeys) {
    const dateModelMap = dailyModelsMap.get(key) || {};
    const sessionMap = dailyModelSessions.get(key) || {};
    dailyModels[key] = {};
    for (const model of Object.keys(dateModelMap)) {
      const row = dateModelMap[model];
      row.totalTokens = row.inputTokens + row.cachedTokens + row.outputTokens;
      row.cacheHitRate = row.inputTokens + row.cachedTokens > 0
        ? (row.cachedTokens / (row.inputTokens + row.cachedTokens)) * 100
        : 0;
      row.sessions = (sessionMap[model] || new Set()).size;
      row.costUsd = round6(row.costUsd);
      row.cacheSavingsUsd = round6(row.cacheSavingsUsd);
      dailyModels[key][model] = row;
    }
  }

  // Per-day summaries (DailyRow schema)
  const daily = dateKeys.map(key => {
    const row = summarizeTurns(turnsByDate.get(key), modelName);
    row.date = key;
    row.sessions = sessionIdsByDate.get(key).size;
    return toDailyRow(row);
  });

  // Period summaries derived from the same buckets
  const todaySummary = summarizeTurns(turnsByDate.get(todayKey), modelName);
  todaySummary.totalSessions = sessionIdsByDate.get(todayKey).size;
  todaySummary.period = 'today';
  todaySummary.dateStr = todayKey;

  const yesterdaySummary = summarizeTurns(turnsByDate.get(yesterdayKey), modelName);
  yesterdaySummary.totalSessions = sessionIdsByDate.get(yesterdayKey).size;
  yesterdaySummary.period = 'yesterday';
  yesterdaySummary.dateStr = yesterdayKey;

  const summarizeRange = (keys, period) => {
    const allTurns = [];
    const allSessionIds = new Set();
    for (const key of keys) {
      for (const turn of turnsByDate.get(key)) allTurns.push(turn);
      for (const id of sessionIdsByDate.get(key)) allSessionIds.add(id);
    }
    const summary = summarizeTurns(allTurns, modelName);
    summary.totalSessions = allSessionIds.size;
    summary.period = period;
    summary.dateRange = `${keys[0]}..${keys[keys.length - 1]}`;
    return summary;
  };

  const last7dSummary = summarizeRange(dateKeys.slice(23), '7d');
  const last30dSummary = summarizeRange(dateKeys, '30d');
  const rollingUsage = getRollingUsage(list, refDate, opts.quota || null);
  const geminiQuota = opts.geminiQuota !== undefined ? opts.geminiQuota : geminiQuotaModule.getCachedGeminiQuota();

  return {
    version: DASHBOARD_PAYLOAD_VERSION,
    generatedAt: new Date().toISOString(),
    currency: opts.currency || 'usd',
    lang,
    isRtl: isRtl(lang),
    i18n,
    isFree: Boolean(opts.isFree),
    model: opts.model || '',
    models,
    dailyModels,
    summaries: {
      today: todaySummary,
      yesterday: yesterdaySummary,
      last7d: last7dSummary,
      last30d: last30dSummary
    },
    daily,
    cacheStats: {
      totalSessions: list.length,
      parsedCount: opts.parsedCount || 0,
      cachedCount: opts.cachedCount || 0,
      elapsedMs: opts.elapsedMs || 0
    },
    rollingUsage,
    geminiQuota
  };
}

/**
 * JSON.stringify with `</script>`-safe escaping for inline script embedding.
 * @param {*} value
 * @returns {string}
 */
function jsonForScript(value) {
  return JSON.stringify(value)
    .replace(/</g, '\\u003c')
    .replace(/\u2028/g, '\\u2028')
    .replace(/\u2029/g, '\\u2029');
}

/**
 * Builds the i18n dictionary embedded into the dashboard HTML.
 * @param {string} [lang] - Optional locale override.
 * @returns {object}
 */
function dashboardI18n(lang = null) {
  const dict = getAllTranslations(lang);
  return {
    ...dict,
    dashboardTitle: dict.dashboardTitle,
    summaryToday: dict.summaryToday,
    summaryYesterday: dict.summaryYesterday,
    summary7d: dict.summary7d,
    summary30d: dict.summary30d,
    chartTitle: dict.chartTitle,
    tableTitle: dict.tableTitle,
    modelsTitle: dict.modelsTitle,
    modelColumn: dict.modelColumn,
    lastUpdated: dict.lastUpdated,
    liveStatus: dict.liveStatus,
    noDataFound: dict.noDataFound,
    freeCostLabel: dict.freeCostLabel,
    colDate: dict.colDate,
    colSessions: dict.colSessions,
    colTurns: dict.colTurns,
    colInput: dict.colInput,
    colCached: dict.colCached,
    colOutput: dict.colOutput,
    colTotal: dict.colTotal,
    colCacheHit: dict.colCacheHit,
    colCost: dict.colCost,
    colSavings: dict.colSavings,
    quotaTitle: dict.quotaTitle,
    quota5h: dict.quota5h,
    quota7d: dict.quota7d,
    remaining: dict.remaining
  };
}

/**
 * Builds the currency formatting descriptor embedded into the dashboard HTML.
 * @param {string} currencyCode
 * @param {boolean} isFree
 * @returns {object}
 */
function dashboardCurrencyFmt(currencyCode, isFree) {
  const cur = CURRENCIES[currencyCode] || CURRENCIES.usd;
  return {
    symbol: cur.symbol,
    rate: cur.rate,
    decimals: cur.displayDecimals,
    position: cur.position,
    isFree: Boolean(isFree)
  };
}

/**
 * Renders the single-file dashboard HTML (inline CSS/JS, no CDN).
 * Refresh strategy (report §2.1 hybrid):
 * - Default: <script src="dashboard-data.js?v=ts"> injection polling (works on
 *   file:// where fetch/XHR are CORS-blocked — C3).
 * - Auto-upgrade: EventSource SSE push when the optional --serve server is up;
 *   falls back to polling automatically on error. SSE_PORT_HINT is baked at
 *   write time; the client retries hint+1..hint+3 at runtime (Fix 4) because
 *   the live server may have auto-incremented its port (EADDRINUSE).
 * @param {object} payload - DashboardPayload.
 * @param {object} [opts]
 * @param {number} [opts.refreshSec=5] - Polling interval in seconds.
 * @param {number} [opts.servePort=8787] - Hint port for the SSE upgrade URL;
 *   the client retries hint+1..hint+3 before falling back to polling.
 * @returns {string} Complete HTML document.
 */
function renderDashboardHtml(payload, opts = {}) {
  const refreshSec = Number(opts.refreshSec) > 0 ? Number(opts.refreshSec) : DASHBOARD_DEFAULT_REFRESH_SEC;
  const servePort = Number(opts.servePort) > 0 ? Number(opts.servePort) : DASHBOARD_DEFAULT_PORT;
  const sseUrl = `http://127.0.0.1:${servePort}/events`;
  const lang = String(payload.lang || getLocale() || 'en');
  const rtlAttr = payload.isRtl || isRtl(lang) ? ' dir="rtl"' : '';
  const initialI18n = payload.i18n || dashboardI18n(lang);
  const i18nJson = jsonForScript(initialI18n);
  const fmtJson = jsonForScript(dashboardCurrencyFmt(payload.currency, payload.isFree));
  const payloadJson = jsonForScript(payload);

  const clientScript = [
    "(function () {",
    "  'use strict';",
    `  var I18N = ${i18nJson};`,
    `  var FMT = ${fmtJson};`,
    `  var REFRESH_MS = ${Math.round(refreshSec * 1000)};`,
    `  var SSE_URL = '${sseUrl}';`,
    `  var SSE_PORT_HINT = ${servePort};`,
    `  var currentLang = '${lang}';`,
    "  var filterState = { range: 'today', from: null, to: null, models: new Set() };",
    "  var allModels = [];",
    "  var lastPayload = null;",
    "  function isFreshPayload(p) { return !!(p && p.version >= 3 && p.dailyModels); }",
    "",
    "  function updateI18N(p) {",
    "    if (!p || !p.i18n) return;",
    "    if (p.lang && p.lang !== currentLang) {",
    "      currentLang = p.lang;",
    "      document.documentElement.lang = p.lang;",
    "    }",
    "    if (p.isRtl !== undefined) {",
    "      document.documentElement.dir = p.isRtl ? 'rtl' : 'ltr';",
    "    }",
    "    for (var k in p.i18n) {",
    "      if (Object.prototype.hasOwnProperty.call(p.i18n, k)) {",
    "        I18N[k] = p.i18n[k];",
    "      }",
    "    }",
    "    var el;",
    "    if (I18N.dashboardTitle) {",
    "      document.title = I18N.dashboardTitle;",
    "      el = document.getElementById('dashTitle');",
    "      if (el) el.textContent = '\\u{1F4CA} ' + I18N.dashboardTitle;",
    "    }",
    "    el = document.getElementById('chartTitle');",
    "    if (el && I18N.chartTitle) el.textContent = I18N.chartTitle;",
    "    el = document.getElementById('modelsTitle');",
    "    if (el && I18N.modelsTitle) el.textContent = I18N.modelsTitle;",
    "    el = document.getElementById('tableTitle');",
    "    if (el && I18N.tableTitle) el.textContent = I18N.tableTitle;",
    "    el = document.getElementById('empty');",
    "    if (el && I18N.noDataFound) el.textContent = I18N.noDataFound;",
    "    el = document.getElementById('filterDateLabel');",
    "    if (el && I18N.filterDate) el.textContent = I18N.filterDate;",
    "    el = document.getElementById('filterModelLabel');",
    "    if (el && I18N.filterModel) el.textContent = I18N.filterModel;",
    "    el = document.getElementById('estimateNote');",
    "    if (el && I18N.estimateDisclaimer) el.textContent = I18N.estimateDisclaimer;",
    "    el = document.getElementById('estimateTitle');",
    "    if (el && I18N.estimatePanelTitle) el.textContent = I18N.estimatePanelTitle;",
    "    el = document.getElementById('estimatePanelNote');",
    "    if (el && I18N.estimateDisclaimer) el.textContent = I18N.estimateDisclaimer;",
    "    el = document.getElementById('estAvgLabel');",
    "    if (el && I18N.estimateDailyAverage) el.textContent = I18N.estimateDailyAverage;",
    "    el = document.getElementById('estMonthEndLabel');",
    "    if (el && I18N.estimateMonthEnd) el.textContent = I18N.estimateMonthEnd;",
    "    el = document.getElementById('model');",
    "    if (el && I18N.activeModel && lastPayload) el.textContent = I18N.activeModel + ': ' + (lastPayload.model || '');",
    "    renderEstimates(lastPayload);",
    "    if (lastPayload && (lastPayload.rollingUsage || lastPayload.geminiQuota)) renderQuota(lastPayload.rollingUsage, lastPayload.geminiQuota);",
    "",
    "    var filterBtns = document.querySelectorAll('.filter-btn[data-range]');",
    "    var rangeKeys = { '30d': 'filter30d', '7d': 'filter7d', 'today': 'filterToday', 'yesterday': 'filterYesterday', 'custom': 'filterCustom' };",
    "    for (var bi = 0; bi < filterBtns.length; bi++) {",
    "      var r = filterBtns[bi].getAttribute('data-range');",
    "      if (rangeKeys[r] && I18N[rangeKeys[r]]) filterBtns[bi].textContent = I18N[rangeKeys[r]];",
    "    }",
    "  }",
    "",
    "  function esc(s) {",
    "    return String(s == null ? '' : s).replace(/[&<>\"']/g, function (c) {",
    "      var AMP = '&' + 'amp;', LT = '&' + 'lt;', GT = '&' + 'gt;',",
    "        QUOT = '&' + 'quot;', APOS = '&' + '#39;';",
    "      return { '&': AMP, '<': LT, '>': GT, '\"': QUOT, \"'\": APOS }[c];",
    "    });",
    "  }",
    "  function fmtCompact(n) {",
    "    n = Number(n) || 0;",
    "    var neg = n < 0; n = Math.abs(n);",
    "    var s;",
    "    if (n >= 1e9) s = (n / 1e9).toFixed(2) + 'B';",
    "    else if (n >= 1e6) s = (n / 1e6).toFixed(2) + 'M';",
    "    else if (n >= 1e3) s = (n / 1e3).toFixed(1) + 'K';",
    "    else s = String(Math.round(n));",
    "    return neg ? '-' + s : s;",
    "  }",
    "  function fmtCost(usd) {",
    "    if (FMT.isFree) return I18N.freeCostLabel;",
    "    var v = (Number(usd) || 0) * FMT.rate;",
    "    var num = v.toFixed(FMT.decimals);",
    "    return FMT.position === 'after' ? num + FMT.symbol : FMT.symbol + num;",
    "  }",
    "  function fmtPct(n) { return (Number(n) || 0).toFixed(1) + '%'; }",
    "  window.__showLabel = function(txt) { var e = document.getElementById('chartHoverLabel'); if (e) { e.textContent = txt; e.style.display = ''; } };",
    "  window.__hideLabel = function() { var e = document.getElementById('chartHoverLabel'); if (e) e.style.display = 'none'; };",
    "",
    "  function computeEstimates(daily) {",
    "    var rows = Array.isArray(daily) ? daily : [];",
    "    var now = new Date();",
    "    var y = now.getFullYear();",
    "    var m = now.getMonth();",
    "    var monthPrefix = y + '-' + ('0' + (m + 1)).slice(-2);",
    "    var mtdTokens = 0, mtdCost = 0, totalTokens = 0, totalCost = 0, i;",
    "    for (i = 0; i < rows.length; i++) {",
    "      var row = rows[i] || {};",
    "      var tok = Number(row.totalTokens) || 0;",
    "      var cost = Number(row.costUsd) || 0;",
    "      totalTokens += tok;",
    "      totalCost += cost;",
    "      if (String(row.date || '').indexOf(monthPrefix) === 0) {",
    "        mtdTokens += tok;",
    "        mtdCost += cost;",
    "      }",
    "    }",
    "    var last7 = rows.slice(-7);",
    "    var sum7Tokens = 0, sum7Cost = 0;",
    "    for (i = 0; i < last7.length; i++) {",
    "      sum7Tokens += Number(last7[i].totalTokens) || 0;",
    "      sum7Cost += Number(last7[i].costUsd) || 0;",
    "    }",
    "    var avg7Tokens = sum7Tokens / 7;",
    "    var avg7Cost = sum7Cost / 7;",
    "    var avg30Tokens = rows.length > 0 ? totalTokens / rows.length : 0;",
    "    var avg30Cost = rows.length > 0 ? totalCost / rows.length : 0;",
    "    var daysInMonth = new Date(y, m + 1, 0).getDate();",
    "    var remaining = Math.max(0, daysInMonth - now.getDate());",
    "    var monthEndTokens = mtdTokens + avg7Tokens * remaining;",
    "    var monthEndCost = mtdCost + avg7Cost * remaining;",
    "    return {",
    "      avg7Tokens: avg7Tokens, avg7Cost: avg7Cost,",
    "      avg30Tokens: avg30Tokens, avg30Cost: avg30Cost,",
    "      monthEndTokens: monthEndTokens, monthEndCost: monthEndCost",
    "    };",
    "  }",
    "  function renderEstimates(p) {",
    "    var est = computeEstimates(p ? p.daily : null);",
    "    var pairs = [",
    "      ['estAvgValue', est.avg7Tokens, 'estAvgCost', est.avg7Cost],",
    "      ['estMonthEndValue', est.monthEndTokens, 'estMonthEndCost', est.monthEndCost]",
    "    ];",
    "    for (var i = 0; i < pairs.length; i++) {",
    "      var vEl = document.getElementById(pairs[i][0]);",
    "      if (vEl) vEl.textContent = fmtCompact(pairs[i][1]);",
    "      var cEl = document.getElementById(pairs[i][2]);",
    "      if (cEl) cEl.textContent = fmtCost(pairs[i][3]);",
    "    }",
    "    var avgEl = document.getElementById('estAvgLabel');",
    "    if (avgEl && I18N.estimateDailyAverage) {",
    "      var avgDetail = fmtCompact(est.avg7Tokens) + ' / ' + fmtCompact(est.avg30Tokens);",
    "      avgEl.textContent = I18N.estimateDailyAverage + ' (' + avgDetail + ')';",
    "    }",
    "  }",
    "",
    "  function cardHtml(label, s) {",
    "    s = s || {};",
    "    return '<div class=\"card\">' +",
    "      '<div class=\"card-label\">' + esc(label) + '</div>' +",
    "      '<div class=\"card-tokens\">' + esc(fmtCompact(s.totalTokens)) + '</div>' +",
    "      '<div class=\"card-cost\">' + esc(fmtCost(s.costUsd)) + '</div>' +",
    "      '<div class=\"card-sub\">' + esc(I18N.colSessions) + ' ' + (s.totalSessions || 0) + ' \\u00b7 ' +",
    "        esc(I18N.colTurns) + ' ' + (s.totalTurns || 0) + ' \\u00b7 ' +",
    "        esc(I18N.colCacheHit) + ' ' + fmtPct(s.cacheHitRate) + '</div>' +",
    "      '</div>';",
    "  }",
    "",
    "  var MODEL_COLORS = ['#58a6ff','#3fb950','#f778ba','#d29922','#a371f7','#ff7b72','#79c0ff','#56d364','#e3b341','#8b949e'];",
    "  function modelColor(name) {",
    "    var idx = allModels.indexOf(name);",
    "    if (idx < 0) idx = 0;",
    "    return MODEL_COLORS[idx % MODEL_COLORS.length];",
    "  }",
    "  function niceMax(rawMax) {",
    "    if (rawMax <= 0) return 10000;",
    "    var headroom = rawMax * 1.15;",
    "    var mag = Math.pow(10, Math.floor(Math.log10(headroom)));",
    "    var norm = headroom / mag;",
    "    var nice;",
    "    if (norm <= 1) nice = 1;",
    "    else if (norm <= 2) nice = 2;",
    "    else if (norm <= 5) nice = 5;",
    "    else nice = 10;",
    "    return nice * mag;",
    "  }",
    "  function fmtAxis(v) {",
    "    if (v >= 1000000) return (v / 1000000).toFixed(v % 1000000 === 0 ? 0 : 1) + 'M';",
    "    if (v >= 1000) return (v / 1000).toFixed(v % 1000 === 0 ? 0 : 1) + 'K';",
    "    return String(Math.round(v));",
    "  }",
    "  function renderSvg(daily, dailyModels) {",
    "    var W = 760, H = 200, PAD_L = 48, PAD_B = 22, PAD_T = 8;",
    "    var n = daily.length || 1;",
    "    var dm = dailyModels || {};",
    "    var ordered = [];",
    "    var mi;",
    "    for (mi = 0; mi < allModels.length; mi++) {",
    "      if (filterState.models.has(allModels[mi])) ordered.push(allModels[mi]);",
    "    }",
    "    var max = 0, i;",
    "    var stacks = [];",
    "    for (i = 0; i < daily.length; i++) {",
    "      var dateModels = dm[daily[i].date] || {};",
    "      var segs = [], dayTotal = 0;",
    "      for (mi = 0; mi < ordered.length; mi++) {",
    "        var mr = dateModels[ordered[mi]];",
    "        var tok = mr ? (mr.totalTokens || 0) : 0;",
    "        if (tok > 0) { segs.push({ name: ordered[mi], tokens: tok, cost: mr.costUsd || 0 }); dayTotal += tok; }",
    "      }",
    "      var fallback = false;",
    "      if (segs.length === 0 && (daily[i].totalTokens || 0) > 0) {",
    "        fallback = true;",
    "        dayTotal = daily[i].totalTokens;",
    "      }",
    "      if (dayTotal > max) max = dayTotal;",
    "      stacks.push({ date: daily[i].date, segs: segs, total: dayTotal, fallback: fallback });",
    "    }",
    "    max = niceMax(max);",
    "    var innerH = H - PAD_B - PAD_T;",
    "    var bw = (W - PAD_L - 8) / n;",
    "    var guides = '';",
    "    for (var gi = 0; gi <= 4; gi++) {",
    "      var gv = (gi / 4) * max;",
    "      var gy = H - PAD_B - Math.round((gi / 4) * innerH);",
    "      guides += '<line x1=\"' + PAD_L + '\" y1=\"' + gy + '\" x2=\"' + (W - 8) + '\" y2=\"' + gy + '\" class=\"guide\"/>';",
    "      guides += '<text x=\"' + (PAD_L - 4) + '\" y=\"' + (gy + 3) + '\" class=\"yaxis\" text-anchor=\"end\">' + fmtAxis(gv) + '</text>';",
    "    }",
    "    var bars = '';",
    "    for (i = 0; i < stacks.length; i++) {",
    "      var st = stacks[i];",
    "      var x = PAD_L + i * bw;",
    "      var hTotal = st.total > 0 ? Math.max(3, Math.round((st.total / max) * innerH)) : 1;",
    "      var _ht = st.date + ' \\u00b7 ' + fmtCompact(st.total) + ' tokens';",
    "      if (st.fallback) {",
    "        var hFb = Math.max(3, Math.round((st.total / max) * innerH));",
    "        bars += '<rect x=\"' + x.toFixed(1) + '\" y=\"' + (H - PAD_B - hFb) + '\" width=\"' + Math.max(1, bw - 2).toFixed(1) + '\" height=\"' + hFb + '\" rx=\"1\" class=\"bar\" onmouseover=\"__showLabel(\\'' + _ht + '\\')\" onmouseout=\"__hideLabel()\">' +",
    "          '<title>' + esc(st.date + ' \\u00b7 ' + fmtCompact(st.total)) + '</title></rect>';",
    "      } else if (st.segs.length === 0) {",
    "        bars += '<rect x=\"' + x.toFixed(1) + '\" y=\"' + (H - PAD_B - 1) + '\" width=\"' + Math.max(1, bw - 2).toFixed(1) + '\" height=\"1\" class=\"bar\"/>';",
    "      }",
    "      var yCursor = H - PAD_B;",
    "      for (var sj = 0; sj < st.segs.length; sj++) {",
    "        var seg = st.segs[sj];",
    "        var segH = Math.max(1, Math.round((seg.tokens / st.total) * hTotal));",
    "        yCursor -= segH;",
    "        bars += '<rect x=\"' + x.toFixed(1) + '\" y=\"' + yCursor + '\" width=\"' + Math.max(1, bw - 2).toFixed(1) +",
    "          '\" height=\"' + segH + '\" rx=\"1\" fill=\"' + modelColor(seg.name) + '\" onmouseover=\"__showLabel(\\'' + _ht + '\\')\" onmouseout=\"__hideLabel()\">' +",
    "          '<title>' + esc(st.date + ' \\u00b7 ' + seg.name + ' \\u00b7 ' + fmtCompact(seg.tokens) + ' \\u00b7 ' + fmtCost(seg.cost)) + '</title></rect>';",
    "      }",
    "      if (i % 5 === 0 || i === stacks.length - 1) {",
    "        bars += '<text x=\"' + (x + bw / 2).toFixed(1) + '\" y=\"' + (H - 6) + '\" class=\"axis\">' + esc(daily[i].date.slice(5)) + '</text>';",
    "      }",
    "    }",
    "    return '<svg viewBox=\"0 0 ' + W + ' ' + H + '\" preserveAspectRatio=\"none\" role=\"img\"><text id=\"chartHoverLabel\" x=\"' + (PAD_L + 4) + '\" y=\"' + (PAD_T + 12) + '\" style=\"display:none;pointer-events:none\" fill=\"var(--text)\" font-size=\"11\" font-weight=\"600\"></text>' + guides + bars + '</svg>';",
    "  }",
    "",
    "  function renderChart(p) {",
    "    var chartEl = document.getElementById('chart');",
    "    if (chartEl) chartEl.innerHTML = renderSvg((p && p.daily) || [], p ? p.dailyModels : null);",
    "    var legend = document.getElementById('chartLegend');",
    "    if (legend) {",
    "      var lhtml = '';",
    "      for (var li = 0; li < allModels.length; li++) {",
    "        if (!filterState.models.has(allModels[li])) continue;",
    "        lhtml += '<span class=\"legend-item\"><span class=\"legend-swatch\" style=\"background:' + modelColor(allModels[li]) + '\"></span>' + esc(allModels[li]) + '</span>';",
    "      }",
    "      legend.innerHTML = lhtml;",
    "    }",
    "  }",
    "",
    "  function renderTable(daily, dailyModels) {",
    "    var cols = [I18N.colDate, I18N.colSessions, I18N.colTurns, I18N.colInput, I18N.colCached,",
    "      I18N.colOutput, I18N.colTotal, I18N.colCacheHit, I18N.colCost, I18N.colSavings];",
    "    var head = '<tr><th>' + cols.map(esc).join('</th><th>') + '</th></tr>';",
    "    var rows = '';",
    "    var dm = dailyModels || {};",
    "    for (var i = daily.length - 1; i >= 0; i--) {",
    "      var d = daily[i];",
    "      rows += '<tr><td>' + esc(d.date) + '</td><td>' + d.sessions + '</td><td>' + d.turns + '</td><td>' +",
    "        fmtCompact(d.inputTokens) + '</td><td>' + fmtCompact(d.cachedTokens) + '</td><td>' +",
    "        fmtCompact(d.outputTokens) + '</td><td class=\"strong\">' + fmtCompact(d.totalTokens) + '</td><td>' +",
    "        fmtPct(d.cacheHitRate) + '</td><td>' + fmtCost(d.costUsd) + '</td><td>' + fmtCost(d.cacheSavingsUsd) + '</td></tr>';",
    "      var dateModels = dm[d.date];",
    "      if (dateModels) {",
    "        var subList = [];",
    "        for (var mn in dateModels) {",
    "          if (Object.prototype.hasOwnProperty.call(dateModels, mn) && filterState.models.has(mn)) subList.push(dateModels[mn]);",
    "        }",
    "        subList.sort(function(a, b) { return (b.costUsd || 0) - (a.costUsd || 0); });",
    "        for (var si = 0; si < subList.length; si++) {",
    "          var sm = subList[si];",
    "          rows += '<tr class=\"subrow\"><td>\\u21b3 ' + esc(sm.displayName || sm.model) + '</td><td>' + (sm.sessions || 0) + '</td><td>' + (sm.turns || 0) + '</td><td>' +",
    "            fmtCompact(sm.inputTokens) + '</td><td>' + fmtCompact(sm.cachedTokens) + '</td><td>' + fmtCompact(sm.outputTokens) + '</td><td class=\"strong\">' +",
    "            fmtCompact(sm.totalTokens) + '</td><td>' + fmtPct(sm.cacheHitRate) + '</td><td>' + fmtCost(sm.costUsd) + '</td><td>' + fmtCost(sm.cacheSavingsUsd) + '</td></tr>';",
    "        }",
    "      }",
    "    }",
    "    return '<table>' + head + rows + '</table>';",
    "  }",
    "",
    "  function renderModels(models) {",
    "    var wrap = document.getElementById('modelsWrap');",
    "    if (!wrap) return;",
    "    if (!models || models.length === 0) {",
    "      wrap.innerHTML = '<div class=\"models-empty\">' + esc(I18N.noDataFound) + '</div>';",
    "      return;",
    "    }",
    "    var maxTok = 0, i;",
    "    for (i = 0; i < models.length; i++) { if (models[i].totalTokens > maxTok) maxTok = models[i].totalTokens; }",
    "    if (maxTok <= 0) maxTok = 1;",
    "    var html = '<table><tr><th>' + esc(I18N.modelColumn) + '</th><th>' + esc(I18N.colSessions) + '</th><th>' +",
    "      esc(I18N.colTurns) + '</th><th>' + esc(I18N.colInput) + '</th><th>' + esc(I18N.colCached) + '</th><th>' +",
    "      esc(I18N.colOutput) + '</th><th>' + esc(I18N.colTotal) + '</th><th>' + esc(I18N.colCacheHit) + '</th><th>' +",
    "      esc(I18N.colCost) + '</th><th>' + esc(I18N.colSavings) + '</th><th>' + esc(I18N.colTotal) + '</th></tr>';",
    "    for (i = 0; i < models.length; i++) {",
    "      var m = models[i];",
    "      var pct = Math.max(m.totalTokens > 0 ? 2 : 0, Math.round((m.totalTokens / maxTok) * 100));",
    "      html += '<tr><td class=\"strong\">' + esc(m.displayName || m.model) + '</td><td>' + m.sessions + '</td><td>' + m.turns + '</td><td>' +",
    "        fmtCompact(m.inputTokens) + '</td><td>' + fmtCompact(m.cachedTokens) + '</td><td>' + fmtCompact(m.outputTokens) + '</td><td>' +",
    "        fmtCompact(m.totalTokens) + '</td><td>' + fmtPct(m.cacheHitRate) + '</td><td>' + fmtCost(m.costUsd) + '</td><td>' +",
    "        fmtCost(m.cacheSavingsUsd) + '</td><td><div class=\"share\"><div class=\"share-bar\" style=\"width:' + pct + '%\"></div></div></td></tr>';",
    "    }",
    "    wrap.innerHTML = html + '</table>';",
    "  }",
    "",
    "  function initFilters(p) {",
    "    if (!p) return;",
    "    lastPayload = p;",
    "    var models = p.models || [];",
    "    var modelNames = models.map(function(m) { return m.model; }).sort();",
    "    if (JSON.stringify(modelNames) !== JSON.stringify(allModels)) {",
    "      allModels = modelNames;",
    "      filterState.models = new Set(modelNames);",
    "      var wrap = document.getElementById('modelFilters');",
    "      if (wrap) {",
    "        var html = '<span class=\"filter-group-label\" id=\"filterModelLabel\">' + esc(I18N.filterModel || 'Model') + '</span>';",
    "        html += '<label class=\"filter-check\"><input type=\"checkbox\" checked data-model=\"*\">' + esc(I18N.filterAll || 'All') + '</label>';",
    "        for (var i = 0; i < modelNames.length; i++) {",
    "          html += '<label class=\"filter-check\"><input type=\"checkbox\" checked data-model=\"' + esc(modelNames[i]) + '\">' + esc(modelNames[i]) + '</label>';",
    "        }",
    "        wrap.innerHTML = html;",
    "        bindModelCheckboxEvents();",
    "      }",
    "    }",
    "    if (p.daily && p.daily.length > 0) {",
    "      var fromEl = document.getElementById('filterFrom');",
    "      var toEl = document.getElementById('filterTo');",
    "      if (fromEl) { fromEl.min = p.daily[0].date; fromEl.max = p.daily[p.daily.length - 1].date; }",
    "      if (toEl) { toEl.min = p.daily[0].date; toEl.max = p.daily[p.daily.length - 1].date; }",
    "    }",
    "  }",
    "",
    "  function getFilteredData(p) {",
    "    if (!p) return null;",
    "    var daily = p.daily || [];",
    "    var dailyModels = p.dailyModels || {};",
    "    var hasDailyModels = false;",
    "    for (var dk in dailyModels) {",
    "      if (!Object.prototype.hasOwnProperty.call(dailyModels, dk) || !dailyModels[dk]) continue;",
    "      for (var mk in dailyModels[dk]) {",
    "        if (Object.prototype.hasOwnProperty.call(dailyModels[dk], mk)) { hasDailyModels = true; break; }",
    "      }",
    "      if (hasDailyModels) break;",
    "    }",
    "    var range = filterState.range;",
    "    var startIdx = 0;",
    "    var endIdx = daily.length;",
    "    if (range === 'today') {",
    "      startIdx = Math.max(0, daily.length - 1);",
    "    } else if (range === 'yesterday') {",
    "      startIdx = Math.max(0, daily.length - 2);",
    "      endIdx = Math.max(0, daily.length - 1);",
    "    } else if (range === '7d') {",
    "      startIdx = Math.max(0, daily.length - 7);",
    "    } else if (range === '30d') {",
    "      startIdx = 0;",
    "    } else if (range === 'custom' && filterState.from && filterState.to) {",
    "      startIdx = daily.length;",
    "      endIdx = 0;",
    "      for (var i = 0; i < daily.length; i++) {",
    "        if (daily[i].date >= filterState.from) { startIdx = i; break; }",
    "      }",
    "      for (var j = daily.length - 1; j >= 0; j--) {",
    "        if (daily[j].date <= filterState.to) { endIdx = j + 1; break; }",
    "      }",
    "      if (startIdx >= endIdx) { startIdx = 0; endIdx = 0; }",
    "    }",
    "    var slicedDaily = daily.slice(startIdx, endIdx);",
    "    var slicedDates = slicedDaily.map(function(d) { return d.date; });",
    "    var selectedModels = filterState.models;",
    "    var modelAgg = {};",
    "    var filteredModels = [];",
    "    if (!hasDailyModels) {",
    "      var srcModels = p.models || [];",
    "      for (var fmi = 0; fmi < srcModels.length; fmi++) {",
    "        if (selectedModels.has(srcModels[fmi].model)) filteredModels.push(srcModels[fmi]);",
    "      }",
    "    }",
    "    for (var di = 0; hasDailyModels && di < slicedDates.length; di++) {",
    "      var dateKey = slicedDates[di];",
    "      var dateModels = dailyModels[dateKey];",
    "      if (!dateModels) continue;",
    "      for (var modelName in dateModels) {",
    "        if (!selectedModels.has(modelName)) continue;",
    "        if (!modelAgg[modelName]) {",
    "          modelAgg[modelName] = { model: modelName, displayName: dateModels[modelName].displayName || modelName, totalTokens: 0, inputTokens: 0, cachedTokens: 0, outputTokens: 0, cacheHitRate: 0, costUsd: 0, cacheSavingsUsd: 0, sessions: 0, turns: 0 };",
    "        }",
    "        var a = modelAgg[modelName];",
    "        var b = dateModels[modelName];",
    "        a.inputTokens += b.inputTokens || 0;",
    "        a.cachedTokens += b.cachedTokens || 0;",
    "        a.outputTokens += b.outputTokens || 0;",
    "        a.turns += b.turns || 0;",
    "        a.sessions += b.sessions || 0;",
    "        a.costUsd += b.costUsd || 0;",
    "        a.cacheSavingsUsd += b.cacheSavingsUsd || 0;",
    "      }",
    "    }",
    "    for (var mn in modelAgg) {",
    "      var r = modelAgg[mn];",
    "      r.totalTokens = r.inputTokens + r.cachedTokens + r.outputTokens;",
    "      r.cacheHitRate = (r.inputTokens + r.cachedTokens) > 0 ? (r.cachedTokens / (r.inputTokens + r.cachedTokens)) * 100 : 0;",
    "      r.costUsd = Math.round(r.costUsd * 1e6) / 1e6;",
    "      r.cacheSavingsUsd = Math.round(r.cacheSavingsUsd * 1e6) / 1e6;",
    "      filteredModels.push(r);",
    "    }",
    "    filteredModels.sort(function(a, b) { return b.costUsd - a.costUsd || b.totalTokens - a.totalTokens; });",
    "    var filteredDaily = [];",
    "    for (var di2 = 0; di2 < slicedDaily.length; di2++) {",
    "      var dd = slicedDaily[di2];",
    "      if (!hasDailyModels) { filteredDaily.push(dd); continue; }",
    "      var dateKey2 = dd.date;",
    "      var dm = dailyModels[dateKey2];",
    "      var totalIn = 0, totalCach = 0, totalOut = 0, totalTurns = 0, totalSess = 0, totalCost = 0, totalSav = 0;",
    "      if (dm) {",
    "        for (var mname in dm) {",
    "          if (!selectedModels.has(mname)) continue;",
    "          var mr = dm[mname];",
    "          totalIn += mr.inputTokens || 0;",
    "          totalCach += mr.cachedTokens || 0;",
    "          totalOut += mr.outputTokens || 0;",
    "          totalTurns += mr.turns || 0;",
    "          totalSess += mr.sessions || 0;",
    "          totalCost += mr.costUsd || 0;",
    "          totalSav += mr.cacheSavingsUsd || 0;",
    "        }",
    "      }",
    "      var tt = totalIn + totalCach + totalOut;",
    "      var chr = (totalIn + totalCach) > 0 ? (totalCach / (totalIn + totalCach)) * 100 : 0;",
    "      filteredDaily.push({",
    "        date: dd.date, sessions: totalSess, turns: totalTurns,",
    "        inputTokens: totalIn, cachedTokens: totalCach, outputTokens: totalOut,",
    "        totalTokens: tt, cacheHitRate: Math.round(chr * 1e6) / 1e6,",
    "        costUsd: Math.round(totalCost * 1e6) / 1e6, cacheSavingsUsd: Math.round(totalSav * 1e6) / 1e6",
    "      });",
    "    }",
    "    var sumTokens = 0, sumCost = 0, sumSess = 0, sumTurns = 0, sumIn = 0, sumCach = 0, sumOut = 0, sumSav = 0;",
    "    for (var fi = 0; fi < filteredDaily.length; fi++) {",
    "      var fd = filteredDaily[fi];",
    "      sumTokens += fd.totalTokens; sumCost += fd.costUsd; sumSess += fd.sessions;",
    "      sumTurns += fd.turns; sumIn += fd.inputTokens; sumCach += fd.cachedTokens;",
    "      sumOut += fd.outputTokens; sumSav += fd.cacheSavingsUsd;",
    "    }",
    "    var sumChr = (sumIn + sumCach) > 0 ? (sumCach / (sumIn + sumCach)) * 100 : 0;",
    "    var filteredSummary = {",
    "      totalTokens: sumTokens, costUsd: Math.round(sumCost * 1e6) / 1e6,",
    "      totalSessions: sumSess, totalTurns: sumTurns, cacheHitRate: Math.round(sumChr * 1e6) / 1e6,",
    "      cacheSavingsUsd: Math.round(sumSav * 1e6) / 1e6",
    "    };",
    "    return { daily: filteredDaily, models: filteredModels, summary: filteredSummary };",
    "  }",
    "",
    "  function renderQuota(r, gq) {",
    "    if (!r && !gq) return;",
    "    var w = document.getElementById('quotaWrap');",
    "    if (!w) return;",
    "    var html = '<div class=\"card\" style=\"width:100%;\"><div class=\"card-label\" style=\"display:flex;justify-content:space-between;align-items:center;\">' +",
    "      '<span>' + esc(I18N.quotaTitle || 'Rate Limits & Quota') + '</span>';",
    "    if (gq && gq.remainPercent !== null && gq.remainPercent !== undefined) {",
    "      var liveBadge = gq.isLive",
    "        ? '<span style=\"font-size:10px;padding:2px 6px;background:rgba(63,185,80,0.15);color:#3fb950;border:1px solid #3fb950;border-radius:10px;font-weight:600;\">\\u25cf ' + esc(I18N.quotaLive || 'LIVE') + '</span>'",
    "        : '<span style=\"font-size:10px;padding:2px 6px;background:rgba(139,148,158,0.15);color:#8b949e;border:1px solid var(--border);border-radius:10px;\">' + esc(I18N.quotaFallback || 'ESTIMATE') + '</span>';",
    "      html += liveBadge;",
    "    }",
    "    html += '</div>';",
    "    if (gq && (gq.quota5h || gq.quota7d)) {",
    "      if (gq.quota5h && gq.quota5h.remainPercent !== null && gq.quota5h.remainPercent !== undefined) {",
    "        var pct5 = Math.max(0, Math.min(100, Number(gq.quota5h.remainPercent) || 0));",
    "        var reset5 = gq.quota5h.resetFormatted ? ' (' + esc(I18N.quotaReset || 'Reset') + ' ' + esc(gq.quota5h.resetFormatted) + ')' : '';",
    "        html += '<div style=\"margin-top:14px;padding:12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;\">' +",
    "          '<div style=\"font-size:13px;color:var(--text);display:flex;justify-content:space-between;align-items:center;\">' +",
    "          '<span class=\"strong\" style=\"color:var(--accent);\">Gemini ' + esc(I18N.quota5h || '5h') + '</span>' +",
    "          '<span class=\"strong\" style=\"color:#3fb950;\">' + pct5 + '% ' + esc(I18N.remaining || 'rem.') + '<span style=\"color:var(--dim);font-weight:normal;font-size:11px;\">' + reset5 + '</span></span></div>' +",
    "          '<div style=\"margin-top:8px;height:10px;background:var(--border);border-radius:5px;overflow:hidden;\">' +",
    "          '<div style=\"height:100%;background:linear-gradient(90deg, #58a6ff, #3fb950);width:' + pct5 + '%;transition:width 0.5s ease;\"></div></div>' +",
    "          '</div>';",
    "      }",
    "      if (gq.quota7d && gq.quota7d.remainPercent !== null && gq.quota7d.remainPercent !== undefined) {",
    "        var pct7 = Math.max(0, Math.min(100, Number(gq.quota7d.remainPercent) || 0));",
    "        var reset7 = gq.quota7d.resetFormatted ? ' (' + esc(I18N.quotaReset || 'Reset') + ' ' + esc(gq.quota7d.resetFormatted) + ')' : '';",
    "        html += '<div style=\"margin-top:10px;padding:12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;\">' +",
    "          '<div style=\"font-size:13px;color:var(--text);display:flex;justify-content:space-between;align-items:center;\">' +",
    "          '<span class=\"strong\" style=\"color:var(--accent);\">Gemini ' + esc(I18N.quota7d || '7d') + '</span>' +",
    "          '<span class=\"strong\" style=\"color:#3fb950;\">' + pct7 + '% ' + esc(I18N.remaining || 'rem.') + '<span style=\"color:var(--dim);font-weight:normal;font-size:11px;\">' + reset7 + '</span></span></div>' +",
    "          '<div style=\"margin-top:8px;height:10px;background:var(--border);border-radius:5px;overflow:hidden;\">' +",
    "          '<div style=\"height:100%;background:linear-gradient(90deg, #d29922, #3fb950);width:' + pct7 + '%;transition:width 0.5s ease;\"></div></div>' +",
    "          '</div>';",
    "      }",
    "    } else if (gq && gq.remainPercent !== null && gq.remainPercent !== undefined) {",
    "      var gPct = Math.max(0, Math.min(100, Number(gq.remainPercent) || 0));",
    "      var resetInfo = gq.resetFormatted ? ' (' + esc(I18N.quotaReset || 'Reset') + ' ' + esc(gq.resetFormatted) + ')' : '';",
    "      html += '<div style=\"margin-top:14px;padding:12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;\">' +",
    "        '<div style=\"font-size:13px;color:var(--text);display:flex;justify-content:space-between;align-items:center;\">' +",
    "        '<span class=\"strong\" style=\"color:var(--accent);\">' + esc(I18N.quotaGemini || 'Gemini Quota Pool') + '</span>' +",
    "        '<span class=\"strong\" style=\"color:#3fb950;\">' + gPct + '% ' + esc(I18N.remaining || 'rem.') + '<span style=\"color:var(--dim);font-weight:normal;font-size:11px;\">' + resetInfo + '</span></span></div>' +",
    "        '<div style=\"margin-top:8px;height:10px;background:var(--border);border-radius:5px;overflow:hidden;\">' +",
    "        '<div style=\"height:100%;background:linear-gradient(90deg, #58a6ff, #3fb950);width:' + gPct + '%;transition:width 0.5s ease;\"></div></div>' +",
    "        '</div>';",
    "    }",
    "    if (r && (!gq || gq.remainPercent === null || gq.remainPercent === undefined)) {",
    "      var u5h = Math.min(100, Math.max(0, Number(r.used5hPercent) || 0));",
    "      var r5h = Math.max(0, Number(r.remain5hPercent) || 0);",
    "      var u7d = Math.min(100, Math.max(0, Number(r.used7dPercent) || 0));",
    "      var r7d = Math.max(0, Number(r.remain7dPercent) || 0);",
    "      html += '<div style=\"margin-top:12px;font-size:13px;color:var(--text);display:flex;justify-content:space-between;\">' +",
    "        '<span>' + esc(I18N.quota5h || '5h') + ': ' + fmtCompact(r.tokens5h) + ' / ' + fmtCompact(r.limit5h) + '</span>' +",
    "        '<span class=\"strong\">' + esc(r5h.toFixed(1)) + '% ' + esc(I18N.remaining || 'rem.') + '</span></div>';",
    "      html += '<div style=\"margin-top:4px;height:8px;background:var(--border);border-radius:4px;overflow:hidden;\">' +",
    "        '<div style=\"height:100%;background:linear-gradient(90deg, #3fb950, #58a6ff);width:' + u5h + '%;transition:width 0.5s ease;\"></div></div>';",
    "      html += '<div style=\"margin-top:16px;font-size:13px;color:var(--text);display:flex;justify-content:space-between;\">' +",
    "        '<span>' + esc(I18N.quota7d || '7d') + ': ' + fmtCompact(r.tokens7d) + ' / ' + fmtCompact(r.limit7d) + '</span>' +",
    "        '<span class=\"strong\">' + esc(r7d.toFixed(1)) + '% ' + esc(I18N.remaining || 'rem.') + '</span></div>';",
    "      html += '<div style=\"margin-top:4px;height:8px;background:var(--border);border-radius:4px;overflow:hidden;\">' +",
    "        '<div style=\"height:100%;background:linear-gradient(90deg, #d29922, #f778ba);width:' + u7d + '%;transition:width 0.5s ease;\"></div></div>';",
    "    }",
    "    html += '</div>';",
    "    w.innerHTML = html;",
    "  }",
    "",
    "  function applyFilters() {",
    "    if (!lastPayload) return;",
    "    var filtered = getFilteredData(lastPayload);",
    "    if (!filtered) return;",
    "    document.getElementById('tableWrap').innerHTML = renderTable(filtered.daily, lastPayload.dailyModels);",
    "    renderModels(filtered.models);",
    "    var s = lastPayload.summaries || {};",
    "    var cards = cardHtml(I18N.summaryToday, s.today) + cardHtml(I18N.summaryYesterday, s.yesterday) +",
    "      cardHtml(I18N.summary7d, s.last7d) + cardHtml(I18N.summary30d, s.last30d);",
    "    if (filterState.range === 'custom' && filterState.from && filterState.to) {",
    "      cards += cardHtml(I18N.filterCustom || 'Custom', filtered.summary);",
    "    }",
    "    document.getElementById('cards').innerHTML = cards;",
    "    if (lastPayload.rollingUsage || lastPayload.geminiQuota) renderQuota(lastPayload.rollingUsage, lastPayload.geminiQuota);",
    "  }",
    "",
    "  function bindDateFilterEvents() {",
    "    var btns = document.querySelectorAll('.filter-btn[data-range]');",
    "    for (var i = 0; i < btns.length; i++) {",
    "      btns[i].addEventListener('click', function() {",
    "        var range = this.getAttribute('data-range');",
    "        filterState.range = range;",
    "        var allBtns = document.querySelectorAll('.filter-btn[data-range]');",
    "        for (var j = 0; j < allBtns.length; j++) allBtns[j].classList.remove('active');",
    "        this.classList.add('active');",
    "        var customEl = document.getElementById('customDateRange');",
    "        if (customEl) customEl.style.display = range === 'custom' ? 'inline' : 'none';",
    "        applyFilters();",
    "      });",
    "    }",
    "    var fromEl = document.getElementById('filterFrom');",
    "    var toEl = document.getElementById('filterTo');",
    "    if (fromEl) fromEl.addEventListener('change', function() { filterState.from = this.value; if (filterState.range === 'custom') applyFilters(); });",
    "    if (toEl) toEl.addEventListener('change', function() { filterState.to = this.value; if (filterState.range === 'custom') applyFilters(); });",
    "  }",
    "",
    "  var modelEventsBound = false;",
    "  function bindModelCheckboxEvents() {",
    "    var wrap = document.getElementById('modelFilters');",
    "    if (!wrap || modelEventsBound) return;",
    "    modelEventsBound = true;",
    "    wrap.addEventListener('change', function(e) {",
    "      if (!e.target || e.target.type !== 'checkbox') return;",
    "      var model = e.target.getAttribute('data-model');",
    "      if (model === '*') {",
    "        var checked = e.target.checked;",
    "        var cbs = wrap.querySelectorAll('input[data-model]');",
    "        filterState.models = new Set();",
    "        for (var i = 0; i < cbs.length; i++) {",
    "          cbs[i].checked = checked;",
    "          if (checked && cbs[i].getAttribute('data-model') !== '*') {",
    "            filterState.models.add(cbs[i].getAttribute('data-model'));",
    "          }",
    "        }",
    "        if (!checked) filterState.models = new Set();",
    "      } else {",
    "        if (e.target.checked) {",
    "          filterState.models.add(model);",
    "        } else {",
    "          filterState.models.delete(model);",
    "        }",
    "        var allCb = wrap.querySelector('input[data-model=\"*\"]');",
    "        if (allCb) allCb.checked = filterState.models.size === allModels.length;",
    "      }",
    "      renderChart(lastPayload);",
    "      applyFilters();",
    "    });",
    "  }",
    "",
    "  function render(p) {",
    "    if (!p) return;",
    "    updateI18N(p);",
    "    initFilters(p);",
    "    var s = p.summaries || {};",
    "    var cards = cardHtml(I18N.summaryToday, s.today) + cardHtml(I18N.summaryYesterday, s.yesterday) +",
    "      cardHtml(I18N.summary7d, s.last7d) + cardHtml(I18N.summary30d, s.last30d);",
    "    document.getElementById('cards').innerHTML = cards;",
    "    renderChart(p);",
    "    var filtersActive = filterState.range !== '30d' || (allModels.length > 0 && filterState.models.size !== allModels.length);",
    "    if (filtersActive) {",
    "      applyFilters();",
    "    } else {",
    "      document.getElementById('tableWrap').innerHTML = renderTable(p.daily || [], p.dailyModels);",
    "      renderModels(p.models);",
    "    }",
    "    var empty = (!s.last30d || s.last30d.totalTokens === 0) && (!s.today || s.today.totalTokens === 0);",
    "    document.getElementById('empty').style.display = empty ? 'block' : 'none';",
    "    var lu = document.getElementById('lastUpdated');",
    "    if (lu) lu.textContent = I18N.lastUpdated.replace('{time}', new Date(p.generatedAt).toLocaleString());",
    "    var model = document.getElementById('model');",
    "    if (model) model.textContent = (I18N.activeModel ? I18N.activeModel + ': ' : '') + (p.model || '');",
    "    renderEstimates(p);",
    "    if (p.rollingUsage || p.geminiQuota) renderQuota(p.rollingUsage, p.geminiQuota);",
    "  }",
    "",
    "  function setLive(on) {",
    "    var el = document.getElementById('live');",
    "    if (el) { el.textContent = on ? '\\u25cf ' + I18N.liveStatus : '\\u25cb'; el.className = on ? 'live on' : 'live'; }",
    "  }",
    "",
    "  var pollTimer = null;",
    "  function pollOnce() {",
    "    var sc = document.createElement('script');",
    "    sc.src = 'dashboard-data.js?v=' + Date.now();",
    "    sc.onload = function () {",
    "      if (isFreshPayload(window.__AGY_DASH__)) {",
    "        render(window.__AGY_DASH__);",
    "      } else if (lastPayload) {",
    "        window.__AGY_DASH__ = lastPayload;",
    "      }",
    "      if (sc.parentNode) sc.parentNode.removeChild(sc);",
    "    };",
    "    sc.onerror = function () {",
    "      setLive(false);",
    "      console.warn('[agy-dashboard] data poll failed');",
    "      if (sc.parentNode) sc.parentNode.removeChild(sc);",
    "    };",
    "    document.head.appendChild(sc);",
    "  }",
    "  function startPolling() { if (!pollTimer) pollTimer = setInterval(pollOnce, REFRESH_MS); }",
    "  function stopPolling() { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } }",
    "",
    "  var sseActive = false;",
    "  var sseAttempt = 0;",
    "  var sseEverOpened = false;",
    "  // SSE port retry ladder (Fix 4): SSE_URL bakes the port known at HTML-write",
    "  // time, but the live server may have auto-incremented (EADDRINUSE -> 8788+).",
    "  // Ladder: try the hint port first; while no SSE connection has ever opened,",
    "  // retry ONCE each on hint+1, hint+2, hint+3 (1s apart) before giving up to",
    "  // polling. Once a port is found, EventSource auto-reconnects to it on",
    "  // transient drops (unchanged legacy behavior).",
    "  function sseUrlForPort(port) { return 'http://127.0.0.1:' + port + '/events'; }",
    "  function trySse() {",
    "    try {",
    "      var url = sseAttempt === 0 ? SSE_URL : sseUrlForPort(SSE_PORT_HINT + sseAttempt);",
    "      var es = new EventSource(url);",
    "      es.onopen = function () { sseEverOpened = true; sseActive = true; stopPolling(); setLive(true); };",
    "      es.onmessage = function (ev) {",
    "        try {",
    "          var p = JSON.parse(ev.data);",
    "          if (!isFreshPayload(p)) return;",
    "          window.__AGY_DASH__ = p;",
    "          render(p);",
    "          setLive(true);",
    "        } catch (e) {}",
    "      };",
    "      es.onerror = function () {",
    "        sseActive = false;",
    "        setLive(false);",
    "        startPolling();",
    "        if (sseEverOpened) return; // live stream dropped: EventSource auto-reconnects to the same port",
    "        try { es.close(); } catch (e2) {}",
    "        if (sseAttempt < 3) {",
    "          sseAttempt += 1;",
    "          setTimeout(trySse, 1000);",
    "        }",
    "      };",
    "    } catch (e) { startPolling(); }",
    "  }",
    "",
    "  render(window.__AGY_DASH__);",
    "  bindDateFilterEvents();",
    "  setLive(false)",
    "  startPolling();",
    "  trySse();",
    "})();"
  ].join('\n');

  return `<!DOCTYPE html>
<html lang="${lang}"${rtlAttr}>
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${t('dashboardTitle', {}, lang)}</title>
<style>
:root{--bg:#0d1117;--panel:#161b22;--border:#30363d;--text:#e6edf3;--dim:#8b949e;--accent:#58a6ff;--green:#3fb950}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--text);font-family:'Segoe UI',system-ui,-apple-system,sans-serif;padding:24px;max-width:1080px;margin:0 auto}
header{display:flex;justify-content:space-between;align-items:baseline;flex-wrap:wrap;gap:8px;margin-bottom:20px}
h1{font-size:20px}
.meta{color:var(--dim);font-size:12px;display:flex;gap:12px;align-items:center;flex-wrap:wrap}
.live{font-weight:600}
.live.on{color:var(--green)}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:12px;margin-bottom:20px}
.card{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:14px 16px}
.card-label{color:var(--dim);font-size:12px;text-transform:uppercase;letter-spacing:.05em}
.card-tokens{font-size:26px;font-weight:700;margin:4px 0 2px}
.card-cost{color:var(--green);font-size:14px}
.card-sub{color:var(--dim);font-size:11px;margin-top:6px}
.panel{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:16px;margin-bottom:20px}
.panel h2{font-size:14px;color:var(--dim);margin-bottom:12px;font-weight:600}
svg{width:100%;height:auto;display:block}
.bar{fill:var(--accent);opacity:.85}
.axis{fill:var(--dim);font-size:10px;text-anchor:middle}
.guide{stroke:var(--border,#30363d);stroke-width:0.5;stroke-dasharray:4,4}
.yaxis{fill:var(--text-secondary,#8b949e);font-size:10px;font-family:system-ui,-apple-system,sans-serif}
.chart-legend{display:flex;flex-wrap:wrap;gap:10px;margin-top:10px;font-size:11px;color:var(--dim)}
.legend-item{display:flex;align-items:center;gap:5px}
.legend-swatch{display:inline-block;width:10px;height:10px;border-radius:2px;flex-shrink:0}
table{width:100%;border-collapse:collapse;font-size:12px}
th,td{padding:6px 8px;text-align:right;border-bottom:1px solid var(--border);white-space:nowrap}
th:first-child,td:first-child{text-align:left}
th{color:var(--dim);font-weight:600}
td.strong{font-weight:600}
#empty{display:none;color:var(--dim);padding:24px;text-align:center}
.models-empty{color:var(--dim);padding:12px 0;text-align:center;font-size:12px}
.share{background:var(--panel);border:1px solid var(--border);border-radius:4px;height:12px;min-width:80px;overflow:hidden}
.share-bar{background:var(--accent);height:100%;border-radius:3px}
.share-bar:last-child{background:var(--green)}
.filters{background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:12px 16px;margin-bottom:20px;display:flex;flex-wrap:wrap;gap:12px;align-items:center}
.filter-group{display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.filter-group-label{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.05em;margin-right:4px}
.filter-btn{background:var(--bg);border:1px solid var(--border);border-radius:4px;color:var(--text);padding:4px 10px;font-size:11px;cursor:pointer;transition:border-color .15s}
.filter-btn:hover{border-color:var(--accent)}
.filter-btn.active{background:var(--accent);border-color:var(--accent);color:#fff}
.filter-check{display:flex;align-items:center;gap:4px;font-size:11px;color:var(--text);cursor:pointer}
.filter-check input{accent-color:var(--accent)}
.filter-date-input{background:var(--bg);border:1px solid var(--border);border-radius:4px;color:var(--text);padding:3px 6px;font-size:11px}
.filter-sep{color:var(--dim);font-size:11px}
[dir=rtl] .filter-group{flex-direction:row-reverse}
.subrow td{color:var(--dim);font-size:11px}
.subrow td:first-child{padding-left:20px}
[dir=rtl] .subrow td:first-child{padding-left:0;padding-right:20px}
.estimate-note{color:var(--dim);font-size:11px}
.est-layout{display:grid;grid-template-columns:1fr;gap:20px;margin-bottom:20px}
@media(min-width:1200px){.est-layout{grid-template-columns:1.6fr 1fr}}
.estimate-panel{margin-bottom:0}
.est-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.est-item{background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:10px 12px}
.est-label{color:var(--dim);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
.est-value{font-size:20px;font-weight:700;margin:4px 0 2px}
.est-cost{color:var(--green);font-size:12px}
.estimate-panel .estimate-note{margin-top:12px;padding-top:10px;border-top:1px solid var(--border)}
@media(max-width:560px){.est-grid{grid-template-columns:1fr}}
</style>
</head>
<body>
<header>
  <h1 id="dashTitle">\u{1F4CA} ${t('dashboardTitle', {}, lang)}</h1>
  <div class="meta"><span id="model"></span><span id="live" class="live"></span><span id="lastUpdated"></span><span id="estimateNote" class="estimate-note">${t('estimateDisclaimer', {}, lang)}</span></div>
</header>
<main>
  <section class="panel"><h2 id="chartTitle">${t('chartTitle', {}, lang)}</h2><div id="chart"></div><div id="chartLegend" class="chart-legend"></div></section>
  <div class="est-layout">
    <section id="cards" class="cards"></section>
    <section class="panel estimate-panel" id="estimatePanel">
      <h2 id="estimateTitle">${t('estimatePanelTitle', {}, lang)}</h2>
      <div class="est-grid">
        <div class="est-item"><div class="est-label" id="estAvgLabel"></div><div class="est-value" id="estAvgValue"></div><div class="est-cost" id="estAvgCost"></div></div>
        <div class="est-item"><div class="est-label" id="estMonthEndLabel"></div><div class="est-value" id="estMonthEndValue"></div><div class="est-cost" id="estMonthEndCost"></div></div>
      </div>
      <div class="estimate-note" id="estimatePanelNote">${t('estimateDisclaimer', {}, lang)}</div>
    </section>
  </div>
  <div id="quotaWrap" style="margin-bottom:20px;"></div>
  <section id="filters" class="filters">
    <div class="filter-group">
      <span class="filter-group-label" id="filterDateLabel">${t('filterDate', {}, lang)}</span>
      <button class="filter-btn active" data-range="today">${t('filterToday', {}, lang)}</button>
      <button class="filter-btn" data-range="yesterday">${t('filterYesterday', {}, lang)}</button>
      <button class="filter-btn" data-range="7d">${t('filter7d', {}, lang)}</button>
      <button class="filter-btn" data-range="30d">${t('filter30d', {}, lang)}</button>
      <button class="filter-btn" data-range="custom">${t('filterCustom', {}, lang)}</button>
      <span id="customDateRange" style="display:none">
        <input type="date" class="filter-date-input" id="filterFrom">
        <span class="filter-sep">~</span>
        <input type="date" class="filter-date-input" id="filterTo">
      </span>
    </div>
    <div class="filter-group" id="modelFilters">
      <span class="filter-group-label" id="filterModelLabel">${t('filterModel', {}, lang)}</span>
      <!-- model checkboxes populated by JS -->
    </div>
  </section>
  <section class="panel"><h2 id="modelsTitle">${t('modelsTitle', {}, lang)}</h2><div id="modelsWrap"></div></section>
  <section class="panel"><h2 id="tableTitle">${t('tableTitle', {}, lang)}</h2><div id="tableWrap"></div></section>
  <div id="empty">${t('noDataFound', {}, lang)}</div>
</main>
<script>window.__AGY_DASH__ = ${payloadJson};</script>
<script>
${clientScript}
</script>
</body>
</html>
`;
}

/**
 * Blocks synchronously for ~ms using Atomics.wait (zero-dependency sync sleep).
 * Falls back silently (immediate continue) on platforms without Atomics.wait.
 * @param {number} ms
 */
function sleepSync(ms) {
  try {
    const arr = new Int32Array(new SharedArrayBuffer(4));
    Atomics.wait(arr, 0, 0, ms);
  } catch (_err) {
    // Immediate continue: retry without delay
  }
}

/**
 * Atomically writes a file: tmp + rename (mirrors cache-manager saveCache).
 * On rename failure (Windows EPERM/EBUSY, E2): retry once after 100ms, then
 * fall back to a direct (non-atomic) write so data is never lost.
 * @param {string} filePath - Absolute target path.
 * @param {string} content - File content.
 */
function atomicWriteFile(filePath, content) {
  const dir = path.dirname(filePath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }

  const tmpFile = `${filePath}.${Date.now()}.${process.pid}.tmp`;
  fs.writeFileSync(tmpFile, content, 'utf8');
  try {
    fs.renameSync(tmpFile, filePath);
  } catch (_renameErr) {
    try { fs.unlinkSync(tmpFile); } catch (_e) { /* ignore */ }
    sleepSync(100);
    try {
      const tmpFile2 = `${filePath}.${Date.now()}.${process.pid}.tmp`;
      fs.writeFileSync(tmpFile2, content, 'utf8');
      fs.renameSync(tmpFile2, filePath);
    } catch (_retryErr) {
      fs.writeFileSync(filePath, content, 'utf8');
    }
  }
}

/**
 * Writes all dashboard artifacts (dashboard.html, dashboard-data.js,
 * dashboard-data.json) with throttling and content-hash skip.
 * - Data files are written only when the payload actually changed (hash over
 *   the payload excluding generatedAt) and the throttle interval has elapsed.
 * - dashboard.html is self-healed whenever missing (E13), regardless of throttle.
 * @param {object} payload - DashboardPayload.
 * @param {object} [opts]
 * @param {boolean} [opts.force=false] - Bypass throttle/hash and write everything.
 * @param {number} [opts.refreshSec] - Polling interval for the HTML template.
 * @param {number} [opts.servePort] - SSE port for the HTML template.
 * @returns {{ html: boolean, dataJs: boolean, dataJson: boolean, skipped: boolean }}
 */
function writeDashboardFiles(payload, opts = {}) {
  const force = Boolean(opts.force);
  const now = Date.now();

  const hashInput = { ...payload, generatedAt: null };
  const hash = JSON.stringify(hashInput);
  const dataChanged = hash !== lastDataHash;
  const throttleOk = now - lastWriteAt >= DASHBOARD_WRITE_THROTTLE_MS;
  const htmlMissing = !fs.existsSync(DASHBOARD_HTML_FILE);
  const dataFilesMissing =
    !fs.existsSync(DASHBOARD_DATA_JS) || !fs.existsSync(DASHBOARD_DATA_JSON);

  // Cross-process skip: each statusline invocation is a fresh node process, so
  // the in-memory hash is always cold. Compare against the on-disk payload
  // (generatedAt excluded) to avoid rewriting identical data every turn.
  let diskLangMismatch = false;
  let diskUnchanged = false;
  if (!force && !dataFilesMissing && dataChanged) {
    try {
      const diskJson = fs.readFileSync(DASHBOARD_DATA_JSON, 'utf8');
      const diskPayload = JSON.parse(diskJson);
      diskUnchanged = JSON.stringify({ ...diskPayload, generatedAt: null }) === hash;
      diskLangMismatch = Boolean(diskPayload && diskPayload.lang && diskPayload.lang !== payload.lang);
    } catch (_err) {
      diskUnchanged = false;
      diskLangMismatch = true;
    }
  }

  let htmlLangMismatch = false;
  if (!htmlMissing && payload.lang) {
    try {
      const htmlHead = fs.readFileSync(DASHBOARD_HTML_FILE, 'utf8').slice(0, 300);
      htmlLangMismatch = !htmlHead.includes(`lang="${payload.lang}"`);
    } catch (_err) {
      htmlLangMismatch = true;
    }
  }

  const localeMismatch = diskLangMismatch || htmlLangMismatch;
  const writeData = force || dataFilesMissing || localeMismatch || (dataChanged && throttleOk && !diskUnchanged);
  const writeHtmlBase = force || htmlMissing || localeMismatch;

  // Self-heal stale embedded payloads (E13b): when the existing dashboard.html
  // would NOT otherwise be rewritten, check whether its embedded
  // `window.__AGY_DASH__` payload is stale and rewrite the HTML so the first
  // paint is never a stale-empty page. Stale = marker/parse failure, embedded
  // content differing from the incoming payload, or an empty embedded payload
  // while the incoming one has data. generatedAt and cacheStats.elapsedMs are
  // excluded from the comparison: generatedAt is rebuilt per invocation and
  // elapsedMs jitters per sync run, so neither indicates real staleness (this
  // keeps the existing skip semantics intact in steady state). The check runs
  // whenever the HTML is not already being written — gating it on !writeData
  // would be dead code in the hook flow, where cacheStats.elapsedMs jitter
  // makes writeData true on essentially every render (verified live).
  let htmlStale = false;
  if (!writeHtmlBase) {
    try {
      const html = fs.readFileSync(DASHBOARD_HTML_FILE, 'utf8');
      const marker = 'window.__AGY_DASH__ = ';
      const start = html.indexOf(marker);
      const end = start === -1 ? -1 : html.indexOf(';</script>', start + marker.length);
      const embedded = end === -1 ? null : JSON.parse(html.slice(start + marker.length, end));
      const stableKey = (p) => {
        if (!p || typeof p !== 'object') return null;
        const { generatedAt: _g, ...rest } = p;
        const cs = rest.cacheStats && typeof rest.cacheStats === 'object'
          ? { ...rest.cacheStats, elapsedMs: 0 }
          : rest.cacheStats;
        return JSON.stringify({ ...rest, cacheStats: cs });
      };
      const embeddedEmpty =
        (!embedded || typeof embedded !== 'object' ||
          !Array.isArray(embedded.models) || embedded.models.length === 0) &&
        Array.isArray(payload.models) && payload.models.length > 0;
      const embeddedDiffers = stableKey(embedded) !== stableKey(payload);
      htmlStale = embeddedEmpty || embeddedDiffers;
    } catch (_err) {
      htmlStale = true;
    }
  }

  const writeHtml = writeHtmlBase || htmlStale;

  const results = { html: false, dataJs: false, dataJson: false, skipped: false };

  if (!writeData && !writeHtml) {
    results.skipped = true;
    return results;
  }

  if (writeData) {
    const json = JSON.stringify(payload, null, 2);
    atomicWriteFile(DASHBOARD_DATA_JS, `window.__AGY_DASH__ = ${jsonForScript(payload)};\n`);
    results.dataJs = true;
    atomicWriteFile(DASHBOARD_DATA_JSON, json);
    results.dataJson = true;
    lastDataHash = hash;
    lastWriteAt = now;
  }

  if (writeHtml) {
    atomicWriteFile(DASHBOARD_HTML_FILE, renderDashboardHtml(payload, opts));
    results.html = true;
  }

  return results;
}

/**
 * Self-heals dashboard.html when missing (E13): regenerates the static
 * template with the given payload embedded. Data files are not touched.
 * @param {object} payload - DashboardPayload.
 * @param {object} [opts] - renderDashboardHtml options.
 * @returns {boolean} True if the HTML was (re)written.
 */
function ensureDashboardHtml(payload, opts = {}) {
  if (fs.existsSync(DASHBOARD_HTML_FILE)) {
    return false;
  }
  atomicWriteFile(DASHBOARD_HTML_FILE, renderDashboardHtml(payload, opts));
  return true;
}

/**
 * Resets module-level throttle state (test hook).
 */
function resetDashboardWriteState() {
  lastWriteAt = 0;
  lastDataHash = null;
}

/**
 * Re-points the dashboard artifact paths at a test directory (test hook).
 * Mirrors resetDashboardWriteState(): artifact tests call this with a temp dir
 * before writing and restore the production dir afterwards, so tests never
 * touch the real ~/.gemini/antigravity-dashboard/ directory.
 * @param {string} dir - Absolute directory for dashboard artifacts.
 * @returns {{ dir: string, html: string, dataJs: string, dataJson: string }} Resolved paths.
 */
function _setDashboardDirForTests(dir) {
  DASHBOARD_DIR = dir;
  DASHBOARD_HTML_FILE = path.join(dir, 'dashboard.html');
  DASHBOARD_DATA_JS = path.join(dir, 'dashboard-data.js');
  DASHBOARD_DATA_JSON = path.join(dir, 'dashboard-data.json');
  return { dir: DASHBOARD_DIR, html: DASHBOARD_HTML_FILE, dataJs: DASHBOARD_DATA_JS, dataJson: DASHBOARD_DATA_JSON };
}

module.exports = {
  DASHBOARD_PAYLOAD_VERSION,
  DASHBOARD_DEFAULT_REFRESH_SEC,
  get DASHBOARD_DIR() { return DASHBOARD_DIR; },
  get DASHBOARD_HTML_FILE() { return DASHBOARD_HTML_FILE; },
  get DASHBOARD_DATA_JS() { return DASHBOARD_DATA_JS; },
  get DASHBOARD_DATA_JSON() { return DASHBOARD_DATA_JSON; },
  buildDashboardPayload,
  renderDashboardHtml,
  writeDashboardFiles,
  ensureDashboardHtml,
  resetDashboardWriteState,
  _setDashboardDirForTests
};