/**
 * @fileoverview Antigravity PostInvocation hook handler.
 * Generates lightweight, real-time single-line terminal badges
 * displaying turn consumption, daily accumulated totals, and cache hit metrics,
 * conforming to the Antigravity PostInvocation hook I/O contract.
 */

const { syncSessions } = require('./cache-manager');
const { getToday } = require('./aggregator');
const { renderRealTimeBadge } = require('./formatter');
const geminiQuota = require('./gemini-quota');

/**
 * Asynchronously reads JSON payload from process.stdin if piped.
 * Returns null if process.stdin is a TTY or if no input is received within timeout.
 * @param {number} [timeoutMs=50] - Timeout in milliseconds to prevent hanging on interactive shells.
 * @returns {Promise<object|null>} Parsed JSON object from stdin or null.
 */
function readStdinJson(timeoutMs = 50) {
  if (process.stdin.isTTY) {
    return Promise.resolve(null);
  }

  return new Promise((resolve) => {
    let data = '';
    let settled = false;

    const finish = (raw) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      process.stdin.removeListener('data', onData);
      process.stdin.removeListener('end', onEnd);
      process.stdin.removeListener('error', onError);
      process.stdin.on('error', () => {});
      process.stdin.pause();
      if (typeof process.stdin.unref === 'function') {
        process.stdin.unref();
      }

      if (!raw || !raw.trim()) {
        resolve(null);
        return;
      }
      try {
        const parsed = JSON.parse(raw.trim());
        resolve(parsed && typeof parsed === 'object' ? parsed : null);
      } catch (_e) {
        resolve(null);
      }
    };

    const onData = (chunk) => {
      data += chunk;
    };

    const onEnd = () => {
      finish(data);
    };

    const onError = () => {
      finish(null);
    };

    const timer = setTimeout(() => {
      finish(data);
    }, timeoutMs);

    process.stdin.setEncoding('utf8');
    process.stdin.on('data', onData);
    process.stdin.once('end', onEnd);
    process.stdin.once('error', onError);
    process.stdin.resume();
  });
}

/**
 * Formats a badge string into the official Antigravity PostInvocation hook response schema.
 * @param {string} badge - Status badge string.
 * @returns {{ injectSteps: Array<{ ephemeralMessage: string }> }}
 */
function formatHookResponse(badge) {
  return {
    injectSteps: [
      {
        ephemeralMessage: badge
      }
    ]
  };
}

/**
 * Handles PostInvocation hook event and returns status badge data.
 * @param {object} [options] - Options.
 * @param {string} [options.currency='usd'] - Selected currency.
 * @param {string} [options.modelName] - Model override.
 * @param {boolean} [options.isFree=false] - Free quota flag.
 * @param {string} [options.conversationId] - Optional active conversation UUID.
 * @param {object} [options.stdinContext] - Optional parsed stdin payload.
 * @param {Array<object>} [options.sessions] - Optional pre-synced sessions array.
 *   When provided, the internal syncSessions() call is skipped (C4: the caller
 *   shares ONE sync pass between the badge and the dashboard writer).
 * @returns {Promise<{ badge: string, turnTokens: number, turnCostUsd: number, todayTokens: number, todayCostUsd: number, cacheHitRate: number, isFree: boolean, sessions: Array<object>|null, injectSteps: Array<{ ephemeralMessage: string }> }>}
 */
async function handlePostInvocation(options = {}) {
  const currency = options.currency || 'usd';
  const isFree = Boolean(options.isFree);

  // Model resolution priority: options.modelName -> stdinContext.modelName -> default settings
  let modelName = options.modelName || null;
  if (!modelName && options.stdinContext && options.stdinContext.modelName && options.stdinContext.modelName !== 'auto') {
    modelName = options.stdinContext.modelName;
  }

  const conversationId = options.conversationId ||
    (options.stdinContext ? options.stdinContext.conversationId : null) ||
    process.env.ANTIGRAVITY_CONVERSATION_ID ||
    process.env.CONVERSATION_ID ||
    null;

  let sessions = Array.isArray(options.sessions) ? options.sessions : null;
  if (!sessions) {
    sessions = (await syncSessions({ modelName })).sessions;
  }
  const todaySummary = getToday(sessions, new Date(), modelName);

  let latestTurn = null;
  let targetSession = null;

  if (conversationId && sessions.length > 0) {
    targetSession = sessions.find(s => s.sessionId === conversationId);
  }

  if (!targetSession && sessions.length > 0) {
    const getActiveTime = (s) => {
      if (!s) return 0;
      const lastTurnTime = s.turns && s.turns.length > 0
        ? new Date(s.turns[s.turns.length - 1].createdAt || 0).getTime()
        : 0;
      const endTime = s.endTime ? new Date(s.endTime).getTime() : 0;
      const startTime = s.startTime ? new Date(s.startTime).getTime() : 0;
      const logicalTime = Math.max(lastTurnTime, endTime, startTime);
      if (logicalTime > 0) return logicalTime;
      return s.mtimeMs || 0;
    };
    targetSession = sessions.reduce((latest, current) => {
      if (!latest) return current;
      return getActiveTime(current) > getActiveTime(latest) ? current : latest;
    }, sessions[0]);
  }

  if (targetSession && targetSession.turns && targetSession.turns.length > 0) {
    const turns = targetSession.turns;
    latestTurn = turns[turns.length - 1];
  }

  const turnTokens = latestTurn ? latestTurn.totalTokens : 0;
  const turnCostUsd = isFree ? 0 : (latestTurn ? latestTurn.costUsd : 0);
  const todayCostUsd = isFree ? 0 : todaySummary.costUsd;

  if (process.env.ANTIGRAVITY_CSRF_TOKEN && geminiQuota.isCsrfUuid(process.env.ANTIGRAVITY_CSRF_TOKEN)) {
    geminiQuota.savePersistedLsToken({ csrfToken: process.env.ANTIGRAVITY_CSRF_TOKEN });
  }

  const currentGeminiQuota = options.geminiQuota !== undefined
    ? options.geminiQuota
    : geminiQuota.getCachedGeminiQuota(undefined, undefined, { sessions, quota: options.quota, now: new Date() });

  if (!currentGeminiQuota || !currentGeminiQuota.isFresh) {
    geminiQuota.triggerBackgroundQuotaRefresh({
      csrfToken: (currentGeminiQuota && currentGeminiQuota.csrfToken) || undefined
    });
  }

  const adjustedGeminiQuota = geminiQuota.applyTurnUsageToQuota(
    currentGeminiQuota,
    sessions,
    { quota: options.quota, now: new Date() }
  );

  const badgeData = {
    turnTokens,
    turnCostUsd,
    todayTokens: todaySummary.totalTokens,
    todayCostUsd,
    cacheHitRate: todaySummary.cacheHitRate,
    isFree,
    rollingUsage: options.rollingUsage || null,
    geminiQuota: adjustedGeminiQuota
  };

  // W1: optional OSC 8 link segment (📊 Dashboard) built by the CLI hook
  // branch; null when --no-link or when the terminal cannot render OSC 8.
  const badgeStr = renderRealTimeBadge(badgeData, currency, isFree, options.link || null);
  const hookResponse = formatHookResponse(badgeStr);

  return {
    badge: badgeStr,
    ...badgeData,
    sessions,
    ...hookResponse
  };
}

module.exports = {
  readStdinJson,
  formatHookResponse,
  handlePostInvocation
};
