/**
 * @fileoverview Rich ANSI terminal formatter and UI component renderer.
 * Produces clean box layouts, data grids, progress bars, and localized currency cards.
 */

const { CURRENCIES, convertCurrency } = require('./config');
const { t, getLocale } = require('./i18n');

/**
 * ANSI Escape Codes for styling terminal output.
 */
const STYLES = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  italic: '\x1b[3m',
  underline: '\x1b[4m',

  // Foreground colors
  black: '\x1b[30m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  white: '\x1b[37m',
  gray: '\x1b[90m',
  brightGreen: '\x1b[92m',
  brightYellow: '\x1b[93m',
  brightCyan: '\x1b[96m',
  brightWhite: '\x1b[97m',

  // Background colors
  bgBlack: '\x1b[40m',
  bgBlue: '\x1b[44m',
  bgCyan: '\x1b[46m',
  bgGray: '\x1b[100m'
};

let colorsEnabled = !process.env.NO_COLOR && process.env.TERM !== 'dumb';

/**
 * Enables or disables ANSI color output.
 * @param {boolean} enabled
 */
function setColorsEnabled(enabled) {
  colorsEnabled = Boolean(enabled);
}

/**
 * Wraps text with ANSI styling if colors are enabled.
 * @param {string} text - Input text.
 * @param {string} style - Key from STYLES.
 * @returns {string}
 */
function styleText(text, style) {
  if (!colorsEnabled || !STYLES[style]) return String(text);
  return `${STYLES[style]}${text}${STYLES.reset}`;
}

/**
 * Strips all ANSI escape sequences from a string.
 * @param {string} str
 * @returns {string}
 */
function stripAnsi(str) {
  if (typeof str !== 'string') return '';
  return str.replace(/\x1b\[[0-9;]*m/g, '');
}

/**
 * Calculates visual display width considering full-width CJK characters.
 * @param {string} str
 * @returns {number}
 */
function getDisplayWidth(str) {
  const clean = stripAnsi(str);
  let width = 0;
  for (let i = 0; i < clean.length; i++) {
    const code = clean.charCodeAt(i);
    // CJK Unified Ideographs, Hangul, Katakana, Hiragana, Fullwidth forms
    if (
      (code >= 0x1100 && code <= 0x115f) ||
      (code >= 0x2e80 && code <= 0xa4cf && code !== 0x303f) ||
      (code >= 0xac00 && code <= 0xd7a3) ||
      (code >= 0xf900 && code <= 0xfaff) ||
      (code >= 0xfe10 && code <= 0xfe19) ||
      (code >= 0xfe30 && code <= 0xfe6f) ||
      (code >= 0xff00 && code <= 0xff60) ||
      (code >= 0xffe0 && code <= 0xffe6)
    ) {
      width += 2;
    } else {
      width += 1;
    }
  }
  return width;
}

/**
 * Pads a string to a specific visual width with alignment.
 * @param {string} str - Content to pad.
 * @param {number} targetWidth - Desired column width.
 * @param {'left'|'right'|'center'} [align='left'] - Text alignment.
 * @returns {string}
 */
function padDisplay(str, targetWidth, align = 'left') {
  const visibleWidth = getDisplayWidth(str);
  const diff = targetWidth - visibleWidth;
  if (diff <= 0) return str;

  const padSpace = ' '.repeat(diff);
  if (align === 'right') {
    return padSpace + str;
  }
  if (align === 'center') {
    const leftPad = ' '.repeat(Math.floor(diff / 2));
    const rightPad = ' '.repeat(diff - Math.floor(diff / 2));
    return leftPad + str + rightPad;
  }
  return str + padSpace;
}

/**
 * Formats a number with comma separators.
 * @param {number} num
 * @returns {string}
 */
function formatNumber(num) {
  if (num === null || num === undefined || isNaN(num)) return '0';
  return Math.round(num).toLocaleString('en-US');
}

/**
 * Formats numbers into compact suffixes (e.g. 1.2k, 45.8k, 1.4M).
 * @param {number} num
 * @returns {string}
 */
function formatCompact(num) {
  if (num === null || num === undefined || isNaN(num)) return '0';
  if (num < 1000) return String(Math.round(num));
  if (num < 1000000) return `${(num / 1000).toFixed(1)}k`;
  return `${(num / 1000000).toFixed(2)}M`;
}

/**
 * Formats currency according to selected currency code and exchange rate.
 * @param {number} usdAmount - Amount in USD.
 * @param {string} [currencyCode='usd'] - Target currency.
 * @returns {string}
 */
function formatCurrency(usdAmount, currencyCode = 'usd') {
  if (usdAmount === null || usdAmount === undefined || isNaN(usdAmount)) {
    usdAmount = 0;
  }
  const currKey = (currencyCode || 'usd').toLowerCase();
  const info = CURRENCIES[currKey] || CURRENCIES.usd;
  const converted = convertCurrency(usdAmount, currKey);

  let numFormatted;
  if (currKey === 'krw') {
    numFormatted = Math.round(converted).toLocaleString('ko-KR');
  } else if (currKey === 'jpy') {
    numFormatted = converted.toFixed(2);
  } else {
    numFormatted = converted < 0.01 && converted > 0 ? converted.toFixed(4) : converted.toFixed(3);
  }

  if (info.position === 'after') {
    return `${numFormatted}${info.symbol}`;
  }
  return `${info.symbol}${numFormatted}`;
}

/**
 * Renders an ANSI ASCII progress bar.
 * @param {number} percentage - Value from 0 to 100.
 * @param {number} [barWidth=10] - Number of characters for the bar.
 * @returns {string}
 */
function renderProgressBar(percentage, barWidth = 10) {
  const clamped = Math.max(0, Math.min(100, percentage || 0));
  const filledCount = Math.round((clamped / 100) * barWidth);
  const emptyCount = Math.max(0, barWidth - filledCount);

  const filledStr = styleText('█'.repeat(filledCount), 'brightGreen');
  const emptyStr = styleText('░'.repeat(emptyCount), 'gray');
  return `${filledStr}${emptyStr} ${clamped.toFixed(1)}%`;
}

/**
 * Renders the application banner and metadata card.
 * @param {string} periodTitle - Formatted period title.
 * @param {string} modelName - Model name.
 * @param {string} currencyCode - Currency code.
 * @param {string} dateRange - Date or date range string.
 * @param {boolean} [isFree=false] - Free subscription quota mode flag.
 * @returns {string}
 */
function renderBanner(periodTitle, modelName, currencyCode, dateRange, isFree = false) {
  const line = '━'.repeat(68);
  const appTitle = styleText(t('appName'), 'brightCyan');
  const tag = styleText(t('tagline'), 'dim');
  const currInfo = CURRENCIES[currencyCode] || CURRENCIES.usd;
  const currencyDisplay = isFree
    ? `${currInfo.code} [${t('freeQuota')}]`
    : `${currInfo.code} (${currInfo.symbol})`;

  const header = [
    styleText(`┏${line}┓`, 'cyan'),
    styleText('┃', 'cyan') + padDisplay(`  ⚡ ${appTitle}  ${tag}`, 68) + styleText('┃', 'cyan'),
    styleText(`┣${line}┫`, 'cyan'),
    styleText('┃', 'cyan') +
      padDisplay(
        `  ${styleText(t('timeRange') + ':', 'bold')} ${styleText(periodTitle, 'brightYellow')} (${dateRange || 'N/A'})` +
          `  |  ${styleText(t('activeModel') + ':', 'bold')} ${styleText(modelName, 'green')}`,
        68
      ) +
      styleText('┃', 'cyan'),
    styleText('┃', 'cyan') +
      padDisplay(
        `  ${styleText(t('currency') + ':', 'bold')} ${styleText(currencyDisplay, 'brightWhite')}` +
          `  |  ${styleText('Locale:', 'bold')} ${styleText(getLocale().toUpperCase(), 'magenta')}`,
        68
      ) +
      styleText('┃', 'cyan'),
    styleText(`┗${line}┛`, 'cyan')
  ];

  return header.join('\n');
}

/**
 * Renders key summary KPI metric cards.
 * @param {object} summary - Aggregated summary object.
 * @param {string} currencyCode - Currency code.
 * @param {boolean} [isFree=false] - Free subscription quota mode flag.
 * @returns {string}
 */
function renderSummaryMetrics(summary, currencyCode, isFree = false) {
  const boxWidth = 33;
  const divider = '─'.repeat(boxWidth);

  const costStr = isFree ? t('freeCostLabel') : formatCurrency(summary.costUsd, currencyCode);
  const savingsStr = isFree ? t('freeCostLabel') : formatCurrency(summary.cacheSavingsUsd, currencyCode);
  const hitRateStr = `${(summary.cacheHitRate || 0).toFixed(1)}%`;

  const leftCard = [
    `┌${divider}┐`,
    `│ ${padDisplay(styleText(t('totalTokens'), 'bold'), boxWidth - 2)} │`,
    `│ ${padDisplay(styleText(formatNumber(summary.totalTokens), 'brightCyan') + styleText(` (${formatCompact(summary.totalTokens)})`, 'dim'), boxWidth - 2)} │`,
    `├${divider}┤`,
    `│ ${padDisplay(`${t('inputTokens')}: ${styleText(formatNumber(summary.inputTokens), 'white')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('cachedTokens')}: ${styleText(formatNumber(summary.cachedTokens), 'green')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('outputTokens')}: ${styleText(formatNumber(summary.outputTokens), 'yellow')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('cacheHitRate')}: ${styleText(hitRateStr, 'brightGreen')}`, boxWidth - 2)} │`,
    `└${divider}┘`
  ];

  const savingsDetail = isFree ? ` (${t('freeQuota')})` : ` (${t('cacheSavings')}: ${savingsStr})`;

  const rightCard = [
    `┌${divider}┐`,
    `│ ${padDisplay(styleText(t('totalCost'), 'bold'), boxWidth - 2)} │`,
    `│ ${padDisplay(styleText(costStr, 'brightGreen') + styleText(savingsDetail, 'dim'), boxWidth - 2)} │`,
    `├${divider}┤`,
    `│ ${padDisplay(`${t('totalSessions')}: ${styleText(formatNumber(summary.totalSessions), 'white')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('totalTurns')}: ${styleText(formatNumber(summary.totalTurns), 'white')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('avgTokensPerTurn')}: ${styleText(formatCompact(summary.totalTurns > 0 ? summary.totalTokens / summary.totalTurns : 0), 'cyan')}`, boxWidth - 2)} │`,
    `│ ${padDisplay(`${t('cacheSavings')}: ${styleText(isFree ? `${formatCompact(summary.cachedTokens)} tok` : savingsStr, 'brightYellow')}`, boxWidth - 2)} │`,
    `└${divider}┘`
  ];

  const combined = [];
  for (let i = 0; i < leftCard.length; i++) {
    combined.push(`${leftCard[i]}  ${rightCard[i]}`);
  }

  return combined.join('\n');
}

/**
 * Renders a table of daily breakdown usage.
 * @param {Array<object>} dailyList - List of daily aggregation items.
 * @param {string} currencyCode - Currency code.
 * @param {object} grandTotal - Summary grand total.
 * @param {boolean} [isFree=false] - Free subscription quota mode flag.
 * @returns {string}
 */
function renderDailyTable(dailyList, currencyCode, grandTotal, isFree = false) {
  if (!dailyList || dailyList.length === 0) {
    return styleText(`\n  ${t('noDataFound')}\n`, 'dim');
  }

  const colWidths = {
    date: 12,
    sessions: 6,
    turns: 6,
    input: 10,
    cached: 10,
    output: 9,
    total: 11,
    hit: 8,
    cost: 11,
    savings: 10
  };

  const topBorder =
    '┌' +
    '─'.repeat(colWidths.date) +
    '┬' +
    '─'.repeat(colWidths.sessions) +
    '┬' +
    '─'.repeat(colWidths.turns) +
    '┬' +
    '─'.repeat(colWidths.input) +
    '┬' +
    '─'.repeat(colWidths.cached) +
    '┬' +
    '─'.repeat(colWidths.output) +
    '┬' +
    '─'.repeat(colWidths.total) +
    '┬' +
    '─'.repeat(colWidths.hit) +
    '┬' +
    '─'.repeat(colWidths.cost) +
    '┬' +
    '─'.repeat(colWidths.savings) +
    '┐';

  const midBorder =
    '├' +
    '─'.repeat(colWidths.date) +
    '┼' +
    '─'.repeat(colWidths.sessions) +
    '┼' +
    '─'.repeat(colWidths.turns) +
    '┼' +
    '─'.repeat(colWidths.input) +
    '┼' +
    '─'.repeat(colWidths.cached) +
    '┼' +
    '─'.repeat(colWidths.output) +
    '┼' +
    '─'.repeat(colWidths.total) +
    '┼' +
    '─'.repeat(colWidths.hit) +
    '┼' +
    '─'.repeat(colWidths.cost) +
    '┼' +
    '─'.repeat(colWidths.savings) +
    '┤';

  const bottomBorder =
    '└' +
    '─'.repeat(colWidths.date) +
    '┴' +
    '─'.repeat(colWidths.sessions) +
    '┴' +
    '─'.repeat(colWidths.turns) +
    '┴' +
    '─'.repeat(colWidths.input) +
    '┴' +
    '─'.repeat(colWidths.cached) +
    '┴' +
    '─'.repeat(colWidths.output) +
    '┴' +
    '─'.repeat(colWidths.total) +
    '┴' +
    '─'.repeat(colWidths.hit) +
    '┴' +
    '─'.repeat(colWidths.cost) +
    '┴' +
    '─'.repeat(colWidths.savings) +
    '┘';

  const headerRow =
    '│' +
    padDisplay(styleText(t('colDate'), 'bold'), colWidths.date, 'center') +
    '│' +
    padDisplay(styleText(t('colSessions'), 'bold'), colWidths.sessions, 'center') +
    '│' +
    padDisplay(styleText(t('colTurns'), 'bold'), colWidths.turns, 'center') +
    '│' +
    padDisplay(styleText(t('colInput'), 'bold'), colWidths.input, 'center') +
    '│' +
    padDisplay(styleText(t('colCached'), 'bold'), colWidths.cached, 'center') +
    '│' +
    padDisplay(styleText(t('colOutput'), 'bold'), colWidths.output, 'center') +
    '│' +
    padDisplay(styleText(t('colTotal'), 'bold'), colWidths.total, 'center') +
    '│' +
    padDisplay(styleText(t('colCacheHit'), 'bold'), colWidths.hit, 'center') +
    '│' +
    padDisplay(styleText(t('colCost'), 'bold'), colWidths.cost, 'center') +
    '│' +
    padDisplay(styleText(t('colSavings'), 'bold'), colWidths.savings, 'center') +
    '│';

  const rows = [topBorder, headerRow, midBorder];

  for (const item of dailyList) {
    const isZero = item.totalTokens === 0;
    const dateFormatted = isZero ? styleText(item.date, 'dim') : styleText(item.date, 'white');
    const costDisplay = isFree
      ? (isZero ? '-' : styleText('$0.00', 'brightGreen'))
      : (isZero ? '-' : styleText(formatCurrency(item.costUsd, currencyCode), 'brightGreen'));
    const savingsDisplay = isFree
      ? (isZero ? '-' : styleText('$0.00', 'dim'))
      : (isZero ? '-' : styleText(formatCurrency(item.cacheSavingsUsd, currencyCode), 'dim'));

    const row =
      '│' +
      padDisplay(dateFormatted, colWidths.date, 'center') +
      '│' +
      padDisplay(isZero ? '-' : String(item.sessions), colWidths.sessions, 'right') +
      '│' +
      padDisplay(isZero ? '-' : String(item.totalTurns || item.turns), colWidths.turns, 'right') +
      '│' +
      padDisplay(isZero ? '-' : formatCompact(item.inputTokens), colWidths.input, 'right') +
      '│' +
      padDisplay(isZero ? '-' : styleText(formatCompact(item.cachedTokens), 'green'), colWidths.cached, 'right') +
      '│' +
      padDisplay(isZero ? '-' : styleText(formatCompact(item.outputTokens), 'yellow'), colWidths.output, 'right') +
      '│' +
      padDisplay(isZero ? '-' : styleText(formatCompact(item.totalTokens), 'brightCyan'), colWidths.total, 'right') +
      '│' +
      padDisplay(isZero ? '-' : `${(item.cacheHitRate || 0).toFixed(0)}%`, colWidths.hit, 'right') +
      '│' +
      padDisplay(costDisplay, colWidths.cost, 'right') +
      '│' +
      padDisplay(savingsDisplay, colWidths.savings, 'right') +
      '│';
    rows.push(row);
  }

  if (grandTotal) {
    rows.push(midBorder);
    const grandCost = isFree ? '$0.00' : formatCurrency(grandTotal.costUsd, currencyCode);
    const grandSavings = isFree ? '$0.00' : formatCurrency(grandTotal.cacheSavingsUsd, currencyCode);

    const totalRow =
      '│' +
      padDisplay(styleText(t('colGrandTotal'), 'bold'), colWidths.date, 'center') +
      '│' +
      padDisplay(styleText(String(grandTotal.totalSessions), 'bold'), colWidths.sessions, 'right') +
      '│' +
      padDisplay(styleText(String(grandTotal.totalTurns), 'bold'), colWidths.turns, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(grandTotal.inputTokens), 'bold'), colWidths.input, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(grandTotal.cachedTokens), 'green'), colWidths.cached, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(grandTotal.outputTokens), 'yellow'), colWidths.output, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(grandTotal.totalTokens), 'brightCyan'), colWidths.total, 'right') +
      '│' +
      padDisplay(styleText(`${(grandTotal.cacheHitRate || 0).toFixed(0)}%`, 'bold'), colWidths.hit, 'right') +
      '│' +
      padDisplay(styleText(grandCost, 'brightGreen'), colWidths.cost, 'right') +
      '│' +
      padDisplay(styleText(grandSavings, 'dim'), colWidths.savings, 'right') +
      '│';
    rows.push(totalRow);
  }

  rows.push(bottomBorder);
  return rows.join('\n');
}

/**
 * Renders turn-by-turn breakdown table for a single session.
 * @param {object} session - Parsed session object.
 * @param {string} currencyCode - Currency code.
 * @param {boolean} [isFree=false] - Free subscription quota mode flag.
 * @returns {string}
 */
function renderSessionTable(session, currencyCode, isFree = false) {
  if (!session || !session.turns || session.turns.length === 0) {
    return styleText(`\n  ${t('noDataFound')}\n`, 'dim');
  }

  const costSummaryStr = isFree ? t('freeCostLabel') : formatCurrency(session.costUsd, currencyCode);

  const infoBlock = [
    styleText(`\n📌 ${t('periodSession')}: ${session.title || session.sessionId}`, 'bold'),
    `   ${styleText('ID:', 'dim')} ${session.sessionId} | ${styleText(t('workspace') + ':', 'dim')} ${session.workspace}`,
    `   ${styleText(t('timeRange') + ':', 'dim')} ${session.startTime} ~ ${session.endTime}`,
    `   ${styleText(t('totalTokens') + ':', 'dim')} ${formatNumber(session.totalTokens)} | ${styleText(t('totalCost') + ':', 'dim')} ${costSummaryStr} | ${styleText(t('cacheHitRate') + ':', 'dim')} ${(session.cacheHitRate || 0).toFixed(1)}%\n`
  ].join('\n');

  const colWidths = {
    step: 6,
    type: 14,
    tool: 16,
    input: 9,
    cached: 9,
    output: 8,
    cost: 10,
    preview: 32
  };

  const topBorder =
    '┌' +
    '─'.repeat(colWidths.step) +
    '┬' +
    '─'.repeat(colWidths.type) +
    '┬' +
    '─'.repeat(colWidths.tool) +
    '┬' +
    '─'.repeat(colWidths.input) +
    '┬' +
    '─'.repeat(colWidths.cached) +
    '┬' +
    '─'.repeat(colWidths.output) +
    '┬' +
    '─'.repeat(colWidths.cost) +
    '┬' +
    '─'.repeat(colWidths.preview) +
    '┐';

  const midBorder =
    '├' +
    '─'.repeat(colWidths.step) +
    '┼' +
    '─'.repeat(colWidths.type) +
    '┼' +
    '─'.repeat(colWidths.tool) +
    '┼' +
    '─'.repeat(colWidths.input) +
    '┼' +
    '─'.repeat(colWidths.cached) +
    '┼' +
    '─'.repeat(colWidths.output) +
    '┼' +
    '─'.repeat(colWidths.cost) +
    '┼' +
    '─'.repeat(colWidths.preview) +
    '┤';

  const bottomBorder =
    '└' +
    '─'.repeat(colWidths.step) +
    '┴' +
    '─'.repeat(colWidths.type) +
    '┴' +
    '─'.repeat(colWidths.tool) +
    '┴' +
    '─'.repeat(colWidths.input) +
    '┴' +
    '─'.repeat(colWidths.cached) +
    '┴' +
    '─'.repeat(colWidths.output) +
    '┴' +
    '─'.repeat(colWidths.cost) +
    '┴' +
    '─'.repeat(colWidths.preview) +
    '┘';

  const headerRow =
    '│' +
    padDisplay(styleText(t('colStep'), 'bold'), colWidths.step, 'center') +
    '│' +
    padDisplay(styleText(t('colType'), 'bold'), colWidths.type, 'center') +
    '│' +
    padDisplay(styleText(t('colAction'), 'bold'), colWidths.tool, 'center') +
    '│' +
    padDisplay(styleText(t('colInput'), 'bold'), colWidths.input, 'center') +
    '│' +
    padDisplay(styleText(t('colCached'), 'bold'), colWidths.cached, 'center') +
    '│' +
    padDisplay(styleText(t('colOutput'), 'bold'), colWidths.output, 'center') +
    '│' +
    padDisplay(styleText(t('colCost'), 'bold'), colWidths.cost, 'center') +
    '│' +
    padDisplay(styleText(t('colSummary'), 'bold'), colWidths.preview, 'center') +
    '│';

  const rows = [infoBlock, topBorder, headerRow, midBorder];

  for (const turn of session.turns) {
    const previewTrunc = (turn.preview || '').substring(0, 30);
    const turnCostDisplay = isFree ? '$0.00' : formatCurrency(turn.costUsd, currencyCode);

    const row =
      '│' +
      padDisplay(String(turn.stepIndex), colWidths.step, 'center') +
      '│' +
      padDisplay(turn.type || turn.source, colWidths.type, 'left') +
      '│' +
      padDisplay(turn.toolName || '-', colWidths.tool, 'left') +
      '│' +
      padDisplay(formatCompact(turn.inputTokens), colWidths.input, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(turn.cachedTokens), 'green'), colWidths.cached, 'right') +
      '│' +
      padDisplay(styleText(formatCompact(turn.outputTokens), 'yellow'), colWidths.output, 'right') +
      '│' +
      padDisplay(styleText(turnCostDisplay, 'brightGreen'), colWidths.cost, 'right') +
      '│' +
      padDisplay(previewTrunc, colWidths.preview, 'left') +
      '│';
    rows.push(row);
  }

  rows.push(bottomBorder);
  return rows.join('\n');
}

/**
 * Formats a mini progress bar using block characters.
 * @param {number} percent 0-100
 * @param {number} [length=5] Length of the bar
 * @returns {string}
 */
function formatMiniBar(percent, length = 5) {
  const filled = Math.round((percent / 100) * length);
  const clamped = Math.max(0, Math.min(length, filled));
  return '▰'.repeat(clamped) + '▱'.repeat(length - clamped);
}

/**
 * Generates a 1-line real-time status badge string for PostInvocation hooks.
 * @param {object} badgeData - Turn & daily metrics.
 * @param {string} [currencyCode='usd'] - Currency code.
 * @param {boolean} [isFree=false] - Free subscription quota mode flag.
 * @param {string|null} [link=null] - Optional OSC 8 link segment appended as
 *   the last badge segment (e.g. clickable 📊 Dashboard). Null/empty omits it.
 * @returns {string}
 */
function renderRealTimeBadge(badgeData, currencyCode = 'usd', isFree = false, link = null) {
  const turnTok = formatCompact(badgeData.turnTokens || 0);
  const turnCost = isFree || badgeData.isFree
    ? t('freeCostLabel')
    : formatCurrency(badgeData.turnCostUsd || 0, currencyCode);
  const todayTok = formatCompact(badgeData.todayTokens || 0);
  const todayCost = isFree || badgeData.isFree
    ? t('freeCostLabel')
    : formatCurrency(badgeData.todayCostUsd || 0, currencyCode);
  const cacheHit = `${(badgeData.cacheHitRate || 0).toFixed(0)}%`;

  let badge =
    `${styleText('⚡ [Antigravity]', 'brightCyan')} ` +
    `${t('hookBadgeTurn')}: ${styleText(turnTok, 'white')} (${styleText(turnCost, 'green')}) | ` +
    `${t('hookBadgeToday')}: ${styleText(todayTok, 'brightYellow')} (${styleText(todayCost, 'brightGreen')}) | ` +
    `${t('hookBadgeCache')}: ${styleText(cacheHit, 'cyan')}`;

  if (badgeData.geminiQuota && badgeData.geminiQuota.remainPercent !== null && badgeData.geminiQuota.remainPercent !== undefined) {
    const gq = badgeData.geminiQuota;
    if (gq.quota5h || gq.quota7d) {
      const parts = [];
      if (gq.quota5h && gq.quota5h.remainPercent !== null && gq.quota5h.remainPercent !== undefined) {
        const pct5 = Math.max(0, Math.min(100, Math.round(Number(gq.quota5h.remainPercent) || 0)));
        const bar5 = formatMiniBar(pct5, 5);
        const resetPart5 = gq.quota5h.resetFormatted ? ` (${gq.quota5h.resetFormatted})` : '';
        parts.push(`5h: ${styleText(bar5, 'brightCyan')} ${pct5}%${resetPart5}`);
      }
      if (gq.quota7d && gq.quota7d.remainPercent !== null && gq.quota7d.remainPercent !== undefined) {
        const pct7 = Math.max(0, Math.min(100, Math.round(Number(gq.quota7d.remainPercent) || 0)));
        const bar7 = formatMiniBar(pct7, 5);
        const resetPart7 = gq.quota7d.resetFormatted ? ` (${gq.quota7d.resetFormatted})` : '';
        parts.push(`7d: ${styleText(bar7, 'brightCyan')} ${pct7}%${resetPart7}`);
      }
      if (parts.length > 0) {
        badge += ` | ${parts.join(' | ')}`;
      }
    } else {
      const gPct = Math.max(0, Math.min(100, Math.round(Number(gq.remainPercent) || 0)));
      const bar = formatMiniBar(gPct, 5);
      const resetWord = t('quotaReset') || '리셋';
      const resetPart = gq.resetFormatted ? ` (${resetWord} ${gq.resetFormatted})` : '';
      const gqSegment = `${t('quota5h') || '5h'}: ${styleText(bar, 'brightCyan')} ${gPct}%${resetPart}`;
      badge += ` | ${gqSegment}`;
    }
  } else if (badgeData.rollingUsage) {
    const ru = badgeData.rollingUsage;
    const r5h = Number.isFinite(ru.remain5hPercent) ? ru.remain5hPercent : 100;
    const r7d = Number.isFinite(ru.remain7dPercent) ? ru.remain7dPercent : 100;
    const bar5 = formatMiniBar(r5h, 5);
    const bar7 = formatMiniBar(r7d, 5);
    const q5h = `${t('quota5h') || '5h'}: ${styleText(bar5, 'cyan')} ${Math.round(r5h)}%`;
    const q7d = `${t('quota7d') || '7d'}: ${styleText(bar7, 'cyan')} ${Math.round(r7d)}%`;
    badge += ` | ${q5h} | ${q7d}`;
  }

  if (link) {
    badge += ` | ${link}`;
  }

  return badge;
}

/**
 * Renders formatted CLI help screen.
 * @returns {string}
 */
function renderHelp() {
  const title = styleText(t('appName'), 'brightCyan');
  const tagline = styleText(t('tagline'), 'dim');

  const options = [
    ['--today, -t', t('cliOptToday')],
    ['--yesterday, -y', t('cliOptYesterday')],
    ['--7d, --week', t('cliOpt7d')],
    ['--30d, --month', t('cliOpt30d')],
    ['--range <start..end>', t('cliOptRange')],
    ['--session, -s [id]', t('cliOptSession')],
    ['--all, -a', t('cliOptAll')],
    ['--currency <usd|krw|jpy|eur|gbp>', t('cliOptCurrency')],
    ['--lang <en|ko|ja|zh|zh-TW|hi|vi|id|th|de|fr|es|pt|it|nl|pl|sv|ru|ar|he|tr>', t('cliOptLang')],
    ['--model <name>', t('cliOptModel')],
    ['--free, --no-cost', t('cliOptFree')],
    ['--hook, --badge', t('cliOptHook')],
    ['--raw', t('cliOptRaw')],
    ['--json', t('cliOptJson')],
    ['--fresh, --no-cache', t('cliOptFresh')],
    ['--prices, --models', t('cliOptPrices')],
    ['--sync, --sync-prices', t('cliOptSync')],
    ['--sync-quota', t('cliOptSyncQuota')],
    ['--auto-sync', t('cliOptAutoSync')],
    ['--html, --dashboard', t('cliOptHtml')],
    ['--serve [port]', t('cliOptServe')],
    ['--port <n>', t('cliOptPort')],
    ['--open', t('cliOptOpen')],
    ['--write-dashboard', t('cliOptWriteDashboard')],
    ['--no-link', t('cliOptNoLink')],
    ['--refresh <sec>', t('cliOptRefresh')],
    ['--no-color', t('cliOptNoColor')],
    ['--help, -h', t('cliOptHelp')],
    ['--version, -v', t('cliOptVersion')]
  ];

  const optLines = options
    .map(([flag, desc]) => `  ${padDisplay(styleText(flag, 'brightYellow'), 32)} ${desc}`)
    .join('\n');

  return `
${title} - ${tagline}

${styleText(t('cliHelpTitle'), 'bold')}
  ${t('cliHelpUsage')}

${styleText('Options:', 'bold')}
${optLines}

${styleText('Examples:', 'bold')}
  $ agy-tokens
  $ agy-tokens --prices --currency krw
  $ agy-tokens --sync
  $ agy-tokens --7d --currency krw
  $ agy-tokens --range 2026-08-01..2026-08-27 --currency eur
  $ agy-tokens --session --lang ko
  $ agy-tokens --free
  $ agy-tokens --hook
`;
}

module.exports = {
  STYLES,
  setColorsEnabled,
  styleText,
  stripAnsi,
  getDisplayWidth,
  padDisplay,
  formatNumber,
  formatCompact,
  formatCurrency,
  renderProgressBar,
  formatMiniBar,
  renderBanner,
  renderSummaryMetrics,
  renderDailyTable,
  renderSessionTable,
  renderRealTimeBadge,
  renderHelp
};
