/**
 * @fileoverview Date-based and session-based aggregation engine.
 * Computes token sums, cache hit rates, cost estimates, and daily trends.
 */

const { calculateCostUsd, calculateCacheSavingsUsd, convertCurrency, DEFAULT_QUOTA_5H, DEFAULT_QUOTA_7D } = require('./config');

/**
 * Formats a Date object to YYYY-MM-DD string in local time.
 * @param {Date} date - Date object.
 * @returns {string} Formatted date string (e.g. '2026-08-27').
 */
function formatLocalDate(date) {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, '0');
  const d = String(date.getDate()).padStart(2, '0');
  return `${y}-${m}-${d}`;
}

/**
 * Parses a YYYY-MM-DD string into a local Date object (at 00:00:00).
 * @param {string} str - Date string.
 * @returns {Date|null}
 */
function parseLocalDate(str) {
  if (!str || typeof str !== 'string') return null;
  const match = str.trim().match(/^(\d{4})-(\d{1,2})-(\d{1,2})$/);
  if (!match) return null;
  const year = parseInt(match[1], 10);
  const month = parseInt(match[2], 10) - 1;
  const day = parseInt(match[3], 10);
  return new Date(year, month, day, 0, 0, 0, 0);
}

/**
 * Creates an empty summary accumulator.
 * @returns {object}
 */
function createEmptySummary() {
  return {
    totalSessions: 0,
    totalTurns: 0,
    inputTokens: 0,
    cachedTokens: 0,
    outputTokens: 0,
    totalTokens: 0,
    cacheHitRate: 0,
    costUsd: 0,
    cacheSavingsUsd: 0
  };
}

/**
 * Summarizes an array of turn objects.
 * @param {Array<object>} turns - Array of turn objects.
 * @param {string} [modelName] - Active model for pricing.
 * @returns {object} Aggregated metrics.
 */
function summarizeTurns(turns, modelName = null) {
  const summary = createEmptySummary();
  summary.totalTurns = turns.length;

  let hasPerTurnCosts = turns.length > 0;
  let hasPerTurnSavings = turns.length > 0;
  for (const turn of turns) {
    summary.inputTokens += turn.inputTokens || 0;
    summary.cachedTokens += turn.cachedTokens || 0;
    summary.outputTokens += turn.outputTokens || 0;
    if (typeof turn.costUsd === 'number') {
      summary.costUsd += turn.costUsd;
    } else {
      hasPerTurnCosts = false;
    }
    if (typeof turn.cacheSavingsUsd === 'number') {
      summary.cacheSavingsUsd += turn.cacheSavingsUsd;
    } else {
      hasPerTurnSavings = false;
    }
  }

  summary.totalTokens = summary.inputTokens + summary.cachedTokens + summary.outputTokens;
  summary.cacheHitRate =
    summary.inputTokens + summary.cachedTokens > 0
      ? (summary.cachedTokens / (summary.inputTokens + summary.cachedTokens)) * 100
      : 0;

  if (!hasPerTurnCosts) {
    // Legacy fallback: recalculate from aggregate tokens with single model
    summary.costUsd = calculateCostUsd(
      summary.inputTokens, summary.cachedTokens, summary.outputTokens, modelName
    );
  }

  if (!hasPerTurnSavings) {
    summary.cacheSavingsUsd = calculateCacheSavingsUsd(summary.cachedTokens, modelName);
  }

  return summary;
}


/**
 * Unifies date-bucketing logic into a single pass engine.
 * @param {Array<object>} sessions
 * @param {string} [modelName]
 * @returns {object} Bucket maps
 */
function bucketSessionsByDate(sessions, modelName = null) {
  const turnsByDate = new Map();
  const sessionIdsByDate = new Map();
  const dailyModelsMap = new Map();
  const dailyModelSessions = new Map();
  const modelsMap = new Map();

  for (const session of (Array.isArray(sessions) ? sessions : [])) {
    const sId = session.sessionId !== undefined ? session.sessionId : session;

    if (!session || !Array.isArray(session.turns) || session.turns.length === 0) {
      if (session && session.startTime) {
        const key = formatLocalDate(new Date(session.startTime));
        if (!turnsByDate.has(key)) {
          turnsByDate.set(key, []);
          sessionIdsByDate.set(key, new Set());
          dailyModelsMap.set(key, {});
          dailyModelSessions.set(key, {});
        }
        sessionIdsByDate.get(key).add(sId);
      }
      continue;
    }

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
      modelRow.costUsd += turnCost;
      modelRow.cacheSavingsUsd += calculateCacheSavingsUsd(turn.cachedTokens || 0, turnModel);

      const key = formatLocalDate(new Date(turn.createdAt));
      
      if (!turnsByDate.has(key)) {
        turnsByDate.set(key, []);
        sessionIdsByDate.set(key, new Set());
        dailyModelsMap.set(key, {});
        dailyModelSessions.set(key, {});
      }
      
      turnsByDate.get(key).push(turn);
      sessionIdsByDate.get(key).add(sId);

      const dateModelMap = dailyModelsMap.get(key);
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
      dm.turns += 1;
      const turnSavingsUsd = (typeof turn.cacheSavingsUsd === 'number') ? turn.cacheSavingsUsd : calculateCacheSavingsUsd(turn.cachedTokens || 0, turnModel);

      dm.costUsd += turnCost;
      dm.cacheSavingsUsd += turnSavingsUsd;

      const dateSessionMap = dailyModelSessions.get(key);
      if (!dateSessionMap[turnModel]) {
        dateSessionMap[turnModel] = new Set();
      }
      dateSessionMap[turnModel].add(sId);
    }
  }

  return { turnsByDate, sessionIdsByDate, dailyModelsMap, dailyModelSessions, modelsMap };
}

/**
 * Aggregates usage for a specific calendar date (default: today).
 * @param {Array<object>} sessions - List of parsed session objects.
 * @param {Date} [refDate] - Reference date (defaults to new Date()).
 * @param {string} [modelName] - Active model name.
 * @returns {object} Aggregation result.
 */
function getToday(sessions, refDate = new Date(), modelName = null) {
  const targetDateStr = formatLocalDate(refDate);
  const { turnsByDate, sessionIdsByDate } = bucketSessionsByDate(sessions, modelName);

  const matchingTurns = turnsByDate.get(targetDateStr) || [];
  const matchingSessionIds = sessionIdsByDate.get(targetDateStr) || new Set();

  const summary = summarizeTurns(matchingTurns, modelName);
  summary.totalSessions = matchingSessionIds.size;
  summary.period = 'today';
  summary.dateStr = targetDateStr;

  return summary;
}

/**
 * Aggregates usage for yesterday.
 * @param {Array<object>} sessions - List of parsed session objects.
 * @param {Date} [refDate] - Reference date (defaults to new Date()).
 * @param {string} [modelName] - Active model name.
 * @returns {object} Aggregation result.
 */
function getYesterday(sessions, refDate = new Date(), modelName = null) {
  const yesterday = new Date(refDate);
  yesterday.setDate(yesterday.getDate() - 1);
  const targetDateStr = formatLocalDate(yesterday);

  const { turnsByDate, sessionIdsByDate } = bucketSessionsByDate(sessions, modelName);

  const matchingTurns = turnsByDate.get(targetDateStr) || [];
  const matchingSessionIds = sessionIdsByDate.get(targetDateStr) || new Set();

  const summary = summarizeTurns(matchingTurns, modelName);
  summary.totalSessions = matchingSessionIds.size;
  summary.period = 'yesterday';
  summary.dateStr = targetDateStr;

  return summary;
}

/**
 * Aggregates usage across the last N days with daily breakdown.
 * @param {Array<object>} sessions - List of parsed session objects.
 * @param {number} nDays - Number of days (e.g. 7 or 30).
 * @param {Date} [refDate] - Reference date.
 * @param {string} [modelName] - Active model name.
 * @returns {object} Daily breakdown and grand total.
 */
function getLastNDays(sessions, nDays = 7, refDate = new Date(), modelName = null) {
  const dateList = [];
  for (let i = nDays - 1; i >= 0; i--) {
    const d = new Date(refDate);
    d.setDate(d.getDate() - i);
    dateList.push(formatLocalDate(d));
  }

  const { turnsByDate, sessionIdsByDate } = bucketSessionsByDate(sessions, modelName);

  const dailyBreakdown = [];
  const allTurns = [];
  const allSessionIds = new Set();

  for (const dateStr of dateList) {
    const turns = turnsByDate.get(dateStr) || [];
    const sessionsSet = sessionIdsByDate.get(dateStr) || new Set();

    const daySummary = summarizeTurns(turns, modelName);
    daySummary.date = dateStr;
    daySummary.sessions = sessionsSet.size;
    dailyBreakdown.push(daySummary);

    for (const t of turns) allTurns.push(t);
    for (const s of sessionsSet) allSessionIds.add(s);
  }

  const grandTotal = summarizeTurns(allTurns, modelName);
  grandTotal.totalSessions = allSessionIds.size;
  grandTotal.period = `${nDays}d`;
  grandTotal.dateRange = `${dateList[0]}..${dateList[dateList.length - 1]}`;
  grandTotal.daily = dailyBreakdown;

  return grandTotal;
}

/**
 * Aggregates usage for a custom date range (YYYY-MM-DD..YYYY-MM-DD).
 * @param {Array<object>} sessions - List of parsed session objects.
 * @param {string} rangeStr - Date range string (e.g. '2026-08-01..2026-08-27').
 * @param {string} [modelName] - Active model name.
 * @returns {object} Aggregated range summary with daily breakdown.
 */
function getDateRange(sessions, rangeStr, modelName = null) {
  const parts = rangeStr.split(/\.\.|:/);
  if (parts.length !== 2) throw new Error('Invalid date range format. Expected YYYY-MM-DD..YYYY-MM-DD');

  const start = parseLocalDate(parts[0]);
  const end = parseLocalDate(parts[1]);
  if (!start || !end || isNaN(start.getTime()) || isNaN(end.getTime())) throw new Error('Invalid date range format. Expected YYYY-MM-DD..YYYY-MM-DD');

  const startTime = start.getTime();
  const endTime = new Date(end.getFullYear(), end.getMonth(), end.getDate(), 23, 59, 59, 999).getTime();
  if (startTime > endTime) throw new Error('Start date must be before or equal to end date.');

  const dateList = [];
  const cur = new Date(start);
  while (cur <= end) {
    dateList.push(formatLocalDate(cur));
    cur.setDate(cur.getDate() + 1);
  }

  const { turnsByDate, sessionIdsByDate } = bucketSessionsByDate(sessions, modelName);

  const dailyBreakdown = [];
  const allTurns = [];
  const allSessionIds = new Set();

  for (const dateStr of dateList) {
    const turns = turnsByDate.get(dateStr) || [];
    const sessionsSet = sessionIdsByDate.get(dateStr) || new Set();

    const daySummary = summarizeTurns(turns, modelName);
    daySummary.date = dateStr;
    daySummary.sessions = sessionsSet.size;
    dailyBreakdown.push(daySummary);

    for (const t of turns) allTurns.push(t);
    for (const s of sessionsSet) allSessionIds.add(s);
  }

  const grandTotal = summarizeTurns(allTurns, modelName);
  grandTotal.totalSessions = allSessionIds.size;
  grandTotal.period = 'range';
  grandTotal.dateRange = rangeStr;
  grandTotal.daily = dailyBreakdown;

  return grandTotal;
}

/**
 * Retrieves drilldown details for a specific session ID or the latest session.
 * @param {Array<object>} sessions - List of parsed sessions.
 * @param {string} [sessionId] - Optional session ID or prefix.
 * @returns {object|null} Detailed session object.
 */
function getSessionDrilldown(sessions, sessionId = null) {
  if (!sessions || sessions.length === 0) return null;

  if (!sessionId) {
    // Return latest session
    return sessions[0];
  }

  const query = sessionId.toLowerCase().trim();
  const match = sessions.find(s => s.sessionId.toLowerCase().startsWith(query));
  return match || null;
}

/**
 * Aggregates all-time history across all sessions.
 * @param {Array<object>} sessions - List of parsed sessions.
 * @param {string} [modelName] - Active model name.
 * @returns {object} All-time summary.
 */
function getAllTime(sessions, modelName = null) {
  const { turnsByDate, sessionIdsByDate } = bucketSessionsByDate(sessions, modelName);

  const allTurns = [];
  const allSessionIds = new Set();
  const sortedDates = Array.from(turnsByDate.keys()).sort();

  const dailyBreakdown = sortedDates.map(dateStr => {
    const turns = turnsByDate.get(dateStr) || [];
    const sessionsSet = sessionIdsByDate.get(dateStr) || new Set();

    const daySummary = summarizeTurns(turns, modelName);
    daySummary.date = dateStr;
    daySummary.sessions = sessionsSet.size;

    for (const t of turns) allTurns.push(t);
    for (const s of sessionsSet) allSessionIds.add(s);

    return daySummary;
  });

  const grandTotal = summarizeTurns(allTurns, modelName);
  grandTotal.totalSessions = allSessionIds.size;
  grandTotal.period = 'all';
  grandTotal.dateRange = sortedDates.length > 0 ? `${sortedDates[0]}..${sortedDates[sortedDates.length - 1]}` : 'all-time';
  grandTotal.daily = dailyBreakdown;

  return grandTotal;
}

/**
 * Computes 5-hour and 7-day rolling usage quota based on absolute time.
 * @param {Array<object>} sessions - List of parsed session objects.
 * @param {Date} [refDate] - Reference date (defaults to new Date()).
 * @param {object} [quota] - Quota configuration.
 * @returns {object} Rolling usage statistics.
 */
function getRollingUsage(sessions, refDate = new Date(), quota = null) {
  const now = (refDate instanceof Date ? refDate : new Date(refDate)).getTime();
  const limit5h = (quota && Number(quota.limit5h) > 0) ? Number(quota.limit5h) : (DEFAULT_QUOTA_5H || 20000000);
  const limit7d = (quota && Number(quota.limit7d) > 0) ? Number(quota.limit7d) : (DEFAULT_QUOTA_7D || 150000000);

  const threshold5h = now - 5 * 3600 * 1000;
  const threshold7d = now - 7 * 86400 * 1000;

  let tokens5h = 0;
  let tokens7d = 0;

  for (const session of (Array.isArray(sessions) ? sessions : [])) {
    if (!session || (!session.turns && !session.startTime)) continue;

    if (session.turns && session.turns.length > 0) {
      for (const turn of session.turns) {
        if (!turn) continue;
        const turnTime = turn.createdAt ? new Date(turn.createdAt).getTime() : 0;
        if (Number.isNaN(turnTime) || turnTime <= 0) continue;
        const tokens = typeof turn.totalTokens === 'number'
          ? turn.totalTokens
          : ((turn.inputTokens || 0) + (turn.cachedTokens || 0) + (turn.outputTokens || 0));
        if (turnTime >= threshold5h) {
          tokens5h += tokens;
        }
        if (turnTime >= threshold7d) {
          tokens7d += tokens;
        }
      }
    } else if (session.startTime) {
      const turnTime = new Date(session.startTime).getTime();
      if (!Number.isNaN(turnTime) && turnTime > 0) {
        const tokens = session.totalTokens || 0;
        if (turnTime >= threshold5h) {
          tokens5h += tokens;
        }
        if (turnTime >= threshold7d) {
          tokens7d += tokens;
        }
      }
    }
  }

  const used5hPercent = Math.min(100, Math.max(0, (tokens5h / limit5h) * 100));
  const remain5hPercent = Math.max(0, 100 - used5hPercent);
  
  const used7dPercent = Math.min(100, Math.max(0, (tokens7d / limit7d) * 100));
  const remain7dPercent = Math.max(0, 100 - used7dPercent);

  return {
    tokens5h,
    limit5h,
    used5hPercent,
    remain5hPercent,
    tokens7d,
    limit7d,
    used7dPercent,
    remain7dPercent
  };
}

module.exports = {
  bucketSessionsByDate,
  formatLocalDate,
  parseLocalDate,
  createEmptySummary,
  summarizeTurns,
  getToday,
  getYesterday,
  getLastNDays,
  getDateRange,
  getSessionDrilldown,
  getAllTime,
  getRollingUsage
};
