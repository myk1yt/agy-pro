/**
 * @fileoverview High-performance log parser for Antigravity transcript files.
 * Streams JSON lines from transcript.jsonl archives, extracting turn interactions,
 * tool calls, prompt sizes, cached contexts, and output generations.
 */

const fs = require('fs');
const path = require('path');
const readline = require('readline');
const { BRAIN_DIR, HISTORY_FILE, getActiveModelFromSettings, calculateCostUsd, calculateCacheSavingsUsd } = require('./config');


/**
 * Marker that opens a settings-change block inside a USER_INPUT turn's content.
 * Used as a cheap substring pre-filter before running the costlier regex (AD-2).
 * @type {string}
 */

// BPE Subword and Token Segmentation Regex
const RE_BPE_PATTERN =
  /[\uAC00-\uD7AF]|[\u1100-\u11FF\u3130-\u318F]|[\u4E00-\u9FFF\u3400-\u4DBF\uF900-\uFAFF]|[\u3040-\u309F]|[\u30A0-\u30FF]|[\u{1F300}-\u{1FAFF}\u{1F600}-\u{1F64F}\u{1F680}-\u{1F6FF}\u{2600}-\u{27BF}]|'s|'t|'re|'ve|'m|'ll|'d|[A-Z]?[a-z0-9]+|[A-Z]+(?![a-z0-9])|[0-9]{1,3}| {4}|\t|\r?\n|[^\s\w]/gu;

/**
 * Estimates token count for a raw string.
 * Uses calibrated subword weightings matching Byte-Pair Encoding and SentencePiece.
 *
 * @param {string} text - Input text content to estimate.
 * @returns {number} Estimated token count (integer >= 0).
 */
function estimateTokens(text) {
  if (!text || typeof text !== 'string') {
    return 0;
  }

  const len = text.length;
  if (len === 0) return 0;
  if (len <= 4 && /^[a-zA-Z0-9]+$/.test(text)) return 1;

  const matches = text.match(RE_BPE_PATTERN);
  if (!matches) {
    return Math.max(1, Math.ceil(len / 4));
  }

  let count = 0;
  for (let i = 0; i < matches.length; i++) {
    const chunk = matches[i];
    const cLen = chunk.length;

    if (cLen > 8 && /^[a-zA-Z0-9]+$/.test(chunk)) {
      // Long identifier or compound word (e.g. StatelessWidget, calculateCostUsd)
      count += Math.ceil(cLen / 4.5);
    } else {
      count += 1;
    }
  }

  return Math.max(1, count);
}

/**
 * Estimates token count for a structured message turn, including framing overhead.
 * @param {object} message - Message or turn object containing role, content, tool_calls, etc.
 * @returns {number}
 */
function estimateMessageTokens(message) {
  if (!message) return 0;

  // Base message framing overhead: <|im_start|>role\n ... <|im_end|>\n (~4 tokens)
  let tokens = 4;

  if (typeof message === 'string') {
    return tokens + estimateTokens(message);
  }

  if (message.content && typeof message.content === 'string') {
    tokens += estimateTokens(message.content);
  }

  if (message.display && typeof message.display === 'string') {
    tokens += estimateTokens(message.display);
  }

  if (message.tool_calls && Array.isArray(message.tool_calls)) {
    // Tool call wrapper overhead (~4 tokens)
    tokens += 4;
    for (const tc of message.tool_calls) {
      if (tc.name) tokens += estimateTokens(tc.name) + 2;
      if (tc.args) {
        const argsStr = typeof tc.args === 'string' ? tc.args : JSON.stringify(tc.args);
        tokens += estimateTokens(argsStr);
      }
    }
  }

  if (message.toolCall) {
    tokens += 4;
    if (message.toolCall.name) tokens += estimateTokens(message.toolCall.name) + 2;
    if (message.toolCall.argumentsJson) tokens += estimateTokens(message.toolCall.argumentsJson);
  }

  return tokens;
}

/**
 * Estimates cumulative token count for an array of conversation turns.
 * @param {Array<object>} turns - Array of message/turn objects.
 * @returns {number}
 */
function estimateConversationTokens(turns) {
  if (!Array.isArray(turns)) return 0;
  let total = 3; // Conversation start/end priming tokens
  for (const turn of turns) {
    total += estimateMessageTokens(turn);
  }
  return total;
}

const SETTINGS_CHANGE_MARKER = '<USER_SETTINGS_CHANGE>';

/**
 * Matches the model-selection line inside a <USER_SETTINGS_CHANGE> block.
 * Verified live-log format:
 *   "changed setting `Model Selection` from None to Gemini 3.7 Flash (High)"
 * Live transcripts may append prompt boilerplate after the model name on the
 * same line (e.g. "Claude Opus 4.6 (Thinking). No need to comment on this
 * change."), so capture group 1 stops at the first sentence boundary: any
 * sentence punctuation (. ! ? ; : , and CJK/fullwidth equivalents) followed
 * by whitespace or end-of-string, a newline, a backtick, "<", or an em/en
 * dash. Model display names never contain those sequences inside (dots in
 * names are dot-without-space like "4.6"), so the capture stays a clean
 * display string including any effort suffix (e.g. "(High)") — pricing
 * strips the effort later via getBaseModelName() in config.js (Batch 1, AD-3).
 * Deliberately NOT global: a module-level regex must stay stateless so
 * repeated .exec() calls never resume from a stale lastIndex.
 * @type {RegExp}
 */
const SETTINGS_CHANGE_RE = /changed setting `Model Selection` from (.+?) to ([^\n]+?)(?:[.!?;:,。！？；：](?:\s|$)|\n|[`—–<]|$)/;

/**
 * Loads history index from history.jsonl if available.
 * Returns a map of conversationId -> { title, workspace, timestamp }.
 * @param {string} [customHistoryPath] - Optional custom path for history file.
 * @returns {Map<string, object>}
 */
function loadHistoryIndex(customHistoryPath = HISTORY_FILE) {
  const historyMap = new Map();
  if (!fs.existsSync(customHistoryPath)) {
    return historyMap;
  }

  try {
    const lines = fs.readFileSync(customHistoryPath, 'utf8').split('\n');
    for (const line of lines) {
      if (!line || !line.trim()) continue;
      try {
        const item = JSON.parse(line);
        if (item && item.conversationId) {
          if (!historyMap.has(item.conversationId)) {
            historyMap.set(item.conversationId, {
              title: item.display ? item.display.substring(0, 80).replace(/\r?\n/g, ' ') : 'Conversation',
              workspace: item.workspace || '',
              timestamp: item.timestamp || Date.now()
            });
          }
        }
      } catch (_err) {
        // Skip malformed line
      }
    }
  } catch (_err) {
    // Ignore read errors
  }

  return historyMap;
}

/**
 * Finds the transcript file for a given session directory.
 * Checks .system_generated/logs/transcript.jsonl and root transcript.jsonl.
 * @param {string} sessionDirPath - Full path to session directory inside brain.
 * @returns {string|null} Full path to transcript.jsonl or null if not found.
 */
function findTranscriptPath(sessionDirPath) {
  const possiblePaths = [
    path.join(sessionDirPath, '.system_generated', 'logs', 'transcript.jsonl'),
    path.join(sessionDirPath, 'logs', 'transcript.jsonl'),
    path.join(sessionDirPath, 'transcript.jsonl'),
    path.join(sessionDirPath, '.system_generated', 'logs', 'transcript_full.jsonl')
  ];

  for (const p of possiblePaths) {
    if (fs.existsSync(p)) {
      try {
        const stat = fs.statSync(p);
        if (stat.isFile() && stat.size > 0) {
          return p;
        }
      } catch (_e) {}
    }
  }

  return null;
}

/**
 * Parses a single transcript file and returns structured session metrics.
 * @param {string} transcriptPath - Path to transcript.jsonl.
 * @param {string} sessionId - Conversation UUID.
 * @param {object} [metadata] - Optional history metadata (title, workspace).
 * @param {string} [modelName] - Active model for pricing calculations.
 * @returns {Promise<object>} Parsed session summary with turns.
 */
async function parseTranscriptFile(transcriptPath, sessionId, metadata = {}, modelName = null, oldTurns = null) {
  const model = modelName || getActiveModelFromSettings();
  const fileStream = fs.createReadStream(transcriptPath, { encoding: 'utf8' });
  const rl = readline.createInterface({
    input: fileStream,
    crlfDelay: Infinity
  });

  const turns = [];
  let sessionTitle = metadata.title || '';
  let workspace = metadata.workspace || '';
  let firstTimestamp = null;
  let lastTimestamp = null;

  let cumulativePromptTokens = 0;
  let sessionInputTokens = 0;
  let sessionCachedTokens = 0;
  let sessionOutputTokens = 0;
  let sessionCostUsd = 0;
  let sessionCacheSavingsUsd = 0;

  // Map old turns for model preservation
  const oldTurnsMap = new Map();
  if (oldTurns && Array.isArray(oldTurns)) {
    for (let i = 0; i < oldTurns.length; i++) {
      const t = oldTurns[i];
      const sIdx = t.stepIndex !== undefined ? t.stepIndex : i;
      if (t.modelName) {
        oldTurnsMap.set(sIdx, t.modelName);
      }
    }
  }

  // Active model tracked at turn-level across <USER_SETTINGS_CHANGE> blocks.
  // Initialized from modelName param or getActiveModelFromSettings() (AD-2).
  let currentActiveModel = model;
  let explicitModelChangeOccurred = false;

  const settingsChanges = []; // { lineIndex, fromModel, toModel }
  const turnLineIndices = []; // parallel to turns[]
  let lineIndex = 0;

  for await (const line of rl) {
    lineIndex++;
    if (!line || !line.trim()) continue;

    let record = null;
    try {
      record = JSON.parse(line);
    } catch (_err) {
      continue; // Skip malformed lines
    }

    if (!record || typeof record !== 'object') continue;

    const createdAt = record.created_at || (record.timestamp ? new Date(record.timestamp).toISOString() : new Date().toISOString());
    const stepIndex = typeof record.step_index === 'number' ? record.step_index : turns.length;
    const source = record.source || 'UNKNOWN';
    const type = record.type || 'MESSAGE';

    if (!firstTimestamp) firstTimestamp = createdAt;
    lastTimestamp = createdAt;

    let toolName = null;
    let turnInputTokens = 0;
    let turnCachedTokens = 0;
    let turnOutputTokens = 0;
    let preview = '';

    if (source === 'USER_INPUT' || source === 'USER_EXPLICIT' || type === 'USER_INPUT') {
      // User request
      const content = record.content || record.display || '';
      if (!sessionTitle && content) {
        sessionTitle = content.substring(0, 80).replace(/\r?\n/g, ' ');
      }
      preview = content.substring(0, 60).replace(/\r?\n/g, ' ');

      // AD-2 / REQ-301: scan for model+effort changes in this turn.
      // Cheap substring pre-filter first, then the anchored regex.
      // Settings change applies immediately to this turn and subsequent turns.
      if (content.includes(SETTINGS_CHANGE_MARKER)) {
        const settingsMatch = SETTINGS_CHANGE_RE.exec(content);
        if (settingsMatch && settingsMatch[2]) {
          // Defense-in-depth: strip any trailing sentence punctuation that
          // survived the regex boundary (e.g. a bare "." at line end) so the
          // stored identity is always a clean display string (REQ-255).
          const fromCandidate = settingsMatch[1].replace(/[.!?;:,。！？；：]+$/, '').trim();
          const toCandidate = settingsMatch[2].replace(/[.!?;:,。！？；：]+$/, '').trim();
          settingsChanges.push({ lineIndex, fromModel: fromCandidate, toModel: toCandidate });
          if (toCandidate) {
            currentActiveModel = toCandidate;
            explicitModelChangeOccurred = true;
          }
        }
      }

      const contentTokens = estimateTokens(content);
      // New user message adds to context
      if (cumulativePromptTokens === 0) {
        // First turn: standard system instruction baseline ~1,200 tokens + prompt
        turnInputTokens = contentTokens + 1200;
        turnCachedTokens = 0;
        cumulativePromptTokens = turnInputTokens;
      } else {
        // Subsequent turn: prior context is cached, new user input is fresh
        turnCachedTokens = cumulativePromptTokens;
        turnInputTokens = contentTokens;
        cumulativePromptTokens += turnInputTokens;
      }
    } else if (source === 'MODEL' || type === 'PLANNER_RESPONSE') {
      // Model generation / tool planning
      let genTokens = 0;
      if (record.tool_calls && Array.isArray(record.tool_calls) && record.tool_calls.length > 0) {
        toolName = record.tool_calls[0].name || 'tool_call';
        preview = `${toolName}(...)`;
        for (const tc of record.tool_calls) {
          genTokens += estimateTokens(tc.name) + 4;
          if (tc.args) {
            genTokens += estimateTokens(typeof tc.args === 'string' ? tc.args : JSON.stringify(tc.args));
          }
        }
      } else if (record.content) {
        preview = record.content.substring(0, 60).replace(/\r?\n/g, ' ');
        genTokens = estimateTokens(record.content);
      } else {
        genTokens = 20; // Minimal response tokens
      }

      turnOutputTokens = Math.max(1, genTokens);
      cumulativePromptTokens += turnOutputTokens;
    } else {
      // Tool output (VIEW_FILE, RUN_COMMAND, GENERIC, etc.)
      toolName = type;
      const content = record.content || '';
      preview = content.substring(0, 60).replace(/\r?\n/g, ' ');
      const outputTokens = estimateTokens(content);

      // Tool outputs become prompt context for next model turn
      turnCachedTokens = cumulativePromptTokens;
      turnInputTokens = outputTokens;
      cumulativePromptTokens += turnInputTokens;
    }

    let turnModelName = currentActiveModel;
    if (!explicitModelChangeOccurred && oldTurnsMap.has(stepIndex)) {
      turnModelName = oldTurnsMap.get(stepIndex);
    }

    const turnTotalTokens = turnInputTokens + turnCachedTokens + turnOutputTokens;
    const turnCostUsd = calculateCostUsd(turnInputTokens, turnCachedTokens, turnOutputTokens, turnModelName);
    const turnCacheSavingsUsd = calculateCacheSavingsUsd(turnCachedTokens, turnModelName);

    sessionInputTokens += turnInputTokens;
    sessionCachedTokens += turnCachedTokens;
    sessionOutputTokens += turnOutputTokens;
    sessionCostUsd += turnCostUsd;
    sessionCacheSavingsUsd += turnCacheSavingsUsd;

    turns.push({
      stepIndex,
      source,
      type,
      toolName: toolName || type,
      inputTokens: turnInputTokens,
      cachedTokens: turnCachedTokens,
      outputTokens: turnOutputTokens,
      totalTokens: turnTotalTokens,
      costUsd: turnCostUsd,
      cacheSavingsUsd: turnCacheSavingsUsd,
      createdAt,
      preview,
      modelName: turnModelName
    });
    turnLineIndices.push(lineIndex);
  }

  // Backtrack: if the first settings change has a meaningful `from` model,
  // all turns before that change should be attributed to the `from` model.
  if (settingsChanges.length > 0) {
    const first = settingsChanges[0];
    if (first.fromModel && first.fromModel.toLowerCase() !== 'none') {
      for (let i = 0; i < turns.length; i++) {
        if (turnLineIndices[i] < first.lineIndex) {
          if (!oldTurnsMap.has(turns[i].stepIndex)) {
            turns[i].modelName = first.fromModel;
            turns[i].costUsd = calculateCostUsd(
              turns[i].inputTokens, turns[i].cachedTokens, turns[i].outputTokens, first.fromModel
            );
            turns[i].cacheSavingsUsd = calculateCacheSavingsUsd(turns[i].cachedTokens || 0, first.fromModel);
          }
        } else {
          break;
        }
      }
      // Recalculate session totals
      sessionCostUsd = turns.reduce((sum, t) => sum + (t.costUsd || 0), 0);
      sessionCacheSavingsUsd = turns.reduce((sum, t) => sum + (t.cacheSavingsUsd || 0), 0);
    }
  }

  const totalTokens = sessionInputTokens + sessionCachedTokens + sessionOutputTokens;
  const costUsd = sessionCostUsd;
  const cacheSavingsUsd = sessionCacheSavingsUsd;
  const cacheHitRate = (sessionInputTokens + sessionCachedTokens) > 0
    ? (sessionCachedTokens / (sessionInputTokens + sessionCachedTokens)) * 100
    : 0;
  const models = [...new Set(turns.map(t => t.modelName).filter(Boolean))];

  return {
    sessionId,
    title: sessionTitle || `Session ${sessionId.substring(0, 8)}`,
    workspace: workspace || 'default',
    startTime: firstTimestamp || new Date().toISOString(),
    endTime: lastTimestamp || new Date().toISOString(),
    turnCount: turns.length,
    inputTokens: sessionInputTokens,
    cachedTokens: sessionCachedTokens,
    outputTokens: sessionOutputTokens,
    totalTokens,
    costUsd,
    cacheSavingsUsd,
    cacheHitRate,
    modelName: currentActiveModel,
    models: models.length > 0 ? models : (currentActiveModel ? [currentActiveModel] : []),
    turns
  };
}

/**
 * Discovers all session directories in the brain folder.
 * @param {string} [customBrainDir] - Optional path to brain directory.
 * @returns {Array<{ sessionId: string, dirPath: string, transcriptPath: string, mtimeMs: number, size: number }>}
 */
function discoverSessions(customBrainDir = BRAIN_DIR) {
  const sessions = [];
  if (!fs.existsSync(customBrainDir)) {
    return sessions;
  }

  let entries = [];
  try {
    entries = fs.readdirSync(customBrainDir, { withFileTypes: true });
  } catch (_err) {
    return sessions;
  }

  for (const entry of entries) {
    if (entry.isDirectory()) {
      const sessionId = entry.name;
      const sessionDir = path.join(customBrainDir, sessionId);
      const transcriptPath = findTranscriptPath(sessionDir);

      if (transcriptPath) {
        try {
          const stat = fs.statSync(transcriptPath);
          sessions.push({
            sessionId,
            dirPath: sessionDir,
            transcriptPath,
            mtimeMs: stat.mtimeMs,
            size: stat.size
          });
        } catch (_e) {}
      }
    }
  }

  return sessions;
}

module.exports = {
  estimateTokens,
  estimateMessageTokens,
  estimateConversationTokens,
  loadHistoryIndex,
  findTranscriptPath,
  parseTranscriptFile,
  discoverSessions
};
