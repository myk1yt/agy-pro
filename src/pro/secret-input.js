/**
 * @fileoverview Raw TTY masked secret input (`abcde****`) with Ctrl+C safety
 * and clean non-TTY / stdin fallback for scripted invocations.
 */

'use strict';

const { maskSecret } = require('./validator');

/**
 * Error thrown when secret entry is interrupted via Ctrl+C (SIGINT / \x03).
 */
class SecretInterruptedError extends Error {
  constructor(message = 'Secret entry aborted (Ctrl+C)') {
    super(message);
    this.name = 'SecretInterruptedError';
    this.code = 'EINTR';
    this.interrupted = true;
  }
}

/**
 * Reads a line from a non-TTY readable stream without echoing characters to output.
 * @param {NodeJS.ReadableStream} input
 * @returns {Promise<{ value: string, eof: boolean }>}
 */
function readLineSilent(input) {
  return new Promise((resolve, reject) => {
    let buffer = '';
    let settled = false;

    const cleanup = () => {
      input.removeListener('data', onData);
      input.removeListener('end', onEnd);
      input.removeListener('error', onError);
    };

    const finish = (value, eof) => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve({
        value: value.replace(/^\ufeff/, '').trim(),
        eof
      });
    };

    const onData = (chunk) => {
      const str = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk);
      for (const ch of str) {
        if (ch === '\x03') {
          if (!settled) {
            settled = true;
            cleanup();
            reject(new SecretInterruptedError());
          }
          return;
        }
        if (ch === '\n') {
          finish(buffer.replace(/\r$/, ''), false);
          return;
        }
        buffer += ch;
      }
    };

    const onEnd = () => {
      finish(buffer.replace(/\r$/, ''), buffer.length === 0);
    };

    const onError = (err) => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(err);
    };

    input.on('data', onData);
    input.on('end', onEnd);
    input.on('error', onError);
    if (typeof input.resume === 'function') {
      input.resume();
    }
  });
}

/**
 * Reads secret input using raw TTY masked echo (first 5 characters verbatim,
 * `*` for every subsequent character). Handles Backspace and Ctrl+C safely.
 * @param {NodeJS.ReadableStream} input
 * @param {NodeJS.WritableStream} output
 * @returns {Promise<string>}
 */
function readSecretTTY(input, output) {
  return new Promise((resolve, reject) => {
    const chars = [];
    const wasRaw = Boolean(input.isRaw);
    let settled = false;

    const restore = () => {
      input.removeListener('data', onData);
      input.removeListener('error', onError);
      if (typeof input.setRawMode === 'function') {
        try {
          input.setRawMode(wasRaw);
        } catch (_) {
          // Ignore restore error if stream closed
        }
      }
      if (typeof input.pause === 'function') {
        try {
          input.pause();
        } catch (_) {
          // Ignore
        }
      }
    };

    const onError = (err) => {
      if (settled) return;
      settled = true;
      restore();
      reject(err);
    };

    const onData = (chunk) => {
      const str = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk);
      for (const ch of str) {
        if (ch === '\x03') {
          output.write('\n');
          if (!settled) {
            settled = true;
            restore();
            reject(new SecretInterruptedError());
          }
          return;
        }
        if (ch === '\r' || ch === '\n') {
          output.write('\n');
          if (!settled) {
            settled = true;
            restore();
            resolve(chars.join('').replace(/^\ufeff/, '').trim());
          }
          return;
        }
        if (ch === '\x7f' || ch === '\b') {
          if (chars.length > 0) {
            chars.pop();
            output.write('\b \b');
          }
          continue;
        }
        // Ignore other ASCII control characters (e.g. escape sequences)
        if (ch.charCodeAt(0) < 0x20) {
          continue;
        }
        chars.push(ch);
        output.write(chars.length <= 5 ? ch : '*');
      }
    };

    if (typeof input.setRawMode === 'function') {
      input.setRawMode(true);
    }
    if (typeof input.resume === 'function') {
      input.resume();
    }
    input.on('data', onData);
    input.on('error', onError);
  });
}

/**
 * Prompts the user for an API key / secret with masked TTY echo (`abcde****`),
 * or silent line read in non-TTY environments.
 * @param {{ prompt?: string, defaultValue?: string, input?: NodeJS.ReadableStream, output?: NodeJS.WritableStream, isTTY?: boolean }} [options]
 * @returns {Promise<string>}
 */
async function promptSecret(options = {}) {
  const promptText = options.prompt || 'API Key';
  const defaultValue = options.defaultValue || '';
  const input = options.input || process.stdin;
  const output = options.output || process.stdout;
  const useTTY =
    options.isTTY !== undefined
      ? Boolean(options.isTTY)
      : Boolean(input && input.isTTY && typeof input.setRawMode === 'function');

  if (defaultValue && !promptText.includes('[')) {
    output.write(`${promptText} [${defaultValue}]: `);
  } else {
    output.write(`${promptText}: `);
  }

  let entered = '';
  if (useTTY) {
    entered = await readSecretTTY(input, output);
  } else {
    const res = await readLineSilent(input);
    entered = res.value;
    if (output && typeof output.write === 'function') {
      output.write('\n');
    }
  }

  if (!entered) {
    return defaultValue;
  }
  return entered;
}

/**
 * Reads a secret from stdin for `--api-key-stdin` non-interactive setup.
 * Caps read size at 64 KiB and trims whitespace/BOM.
 * @param {NodeJS.ReadableStream} [input]
 * @returns {Promise<string>}
 */
function readSecretFromStdin(input = process.stdin) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let totalBytes = 0;
    const MAX_BYTES = 64 * 1024;

    input.on('data', (chunk) => {
      const buf = Buffer.isBuffer(chunk) ? chunk : Buffer.from(String(chunk), 'utf8');
      totalBytes += buf.length;
      if (totalBytes <= MAX_BYTES) {
        chunks.push(buf);
      }
    });
    input.on('end', () => {
      const text = Buffer.concat(chunks)
        .toString('utf8')
        .replace(/^\ufeff/, '')
        .split(/\r?\n/)[0]
        .trim();
      resolve(text);
    });
    input.on('error', reject);
    if (typeof input.resume === 'function') {
      input.resume();
    }
  });
}

module.exports = {
  SecretInterruptedError,
  promptSecret,
  readSecretFromStdin,
  maskSecret
};
