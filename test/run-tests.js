/**
 * @fileoverview Zero-dependency test runner and comprehensive test suite
 * for Antigravity Token & Cost Tracker, Remote Pricing Synchronization Engine,
 * and agy-tools developer suite.
 */

const fs = require('fs');
const path = require('path');
const os = require('os');
const http = require('http');
const https = require('https');
const crypto = require('crypto');
const assert = require('assert');

const config = require('../src/config');
const i18n = require('../src/i18n');
const tokenizer = require('../src/tokenizer');
const logParser = require('../src/log-parser');
const cacheManager = require('../src/cache-manager');
const aggregator = require('../src/aggregator');
const formatter = require('../src/formatter');
const hookHandler = require('../src/hook-handler');
const priceSyncer = require('../src/price-syncer');
const geminiQuota = require('../src/gemini-quota');
const { parseArgs } = require('../src/index');

/**
 * Test Runner Harness
 */
let totalTests = 0;
let passedTests = 0;
let failedTests = 0;
const failures = [];

async function test(name, fn) {
  totalTests++;
  try {
    await fn();
    passedTests++;
    console.log(`  \x1b[32m✓\x1b[0m ${name}`);
  } catch (err) {
    failedTests++;
    failures.push({ name, error: err });
    console.log(`  \x1b[31m✗\x1b[0m ${name}`);
    console.log(`    \x1b[31m${err.message}\x1b[0m`);
    if (err.stack) {
      console.log(`    \x1b[90m${err.stack.split('\n').slice(1, 4).join('\n    ')}\x1b[0m`);
    }
  }
}

function describe(suiteName, fn) {
  console.log(`\n\x1b[1m\x1b[36m▶ ${suiteName}\x1b[0m`);
  return fn();
}

/**
 * Main Test Execution Runner
 */
async function runAllTests() {
  const startTime = Date.now();
  console.log('\x1b[1m\x1b[35m=======================================================');
  console.log('   Antigravity CLI Developer Toolkit (agy-tools) Test Suite');
  console.log('=======================================================\x1b[0m');

  // --- Suite 1: Tokenizer Unit Tests ---
  await describe('1. Tokenizer Unit Tests (Subword & Multilingual)', async () => {
    await test('Should return 0 for empty or non-string inputs', () => {
      assert.strictEqual(tokenizer.estimateTokens(''), 0);
      assert.strictEqual(tokenizer.estimateTokens(null), 0);
      assert.strictEqual(tokenizer.estimateTokens(undefined), 0);
    });

    await test('Should estimate English words accurately', () => {
      const tokens = tokenizer.estimateTokens('Hello world, this is a test.');
      assert(tokens >= 7 && tokens <= 10, `Expected 7-10 tokens, got ${tokens}`);
    });

    await test('Should tokenize Korean (Hangul) text with subword calibration', () => {
      const koText = '안녕하세요 Antigravity 토큰 계산기 테스트입니다.';
      const tokens = tokenizer.estimateTokens(koText);
      assert(tokens >= 15 && tokens <= 25, `Expected 15-25 tokens, got ${tokens}`);
    });

    await test('Should tokenize Japanese (Hiragana, Katakana, Kanji)', () => {
      const jaText = 'こんにちは世界、トークン計算のテストです。';
      const tokens = tokenizer.estimateTokens(jaText);
      assert(tokens >= 15 && tokens <= 25, `Expected 15-25 tokens, got ${tokens}`);
    });

    await test('Should tokenize Chinese (CJK Ideographs)', () => {
      const zhText = '你好世界，这是一个词元计算测试。';
      const tokens = tokenizer.estimateTokens(zhText);
      assert(tokens >= 12 && tokens <= 22, `Expected 12-22 tokens, got ${tokens}`);
    });

    await test('Should estimate code tokens across Dart, Python, JS, and Rust', () => {
      const dartCode = `
        import 'package:flutter/material.dart';
        class UserProfileWidget extends StatelessWidget {
          final String userId;
          const UserProfileWidget({super.key, required this.userId});
          @override
          Widget build(BuildContext context) => Container(child: Text(userId));
        }
      `;
      const dartTokens = tokenizer.estimateTokens(dartCode);
      assert(dartTokens >= 50 && dartTokens <= 110, `Expected 50-110 tokens for Dart snippet, got ${dartTokens}`);

      const pyCode = `
        def calculate_tokens(text: str) -> int:
            """Estimate tokens for a given string."""
            return len(text.split()) * 4
      `;
      const pyTokens = tokenizer.estimateTokens(pyCode);
      assert(pyTokens >= 15 && pyTokens <= 60, `Expected 15-60 tokens for Python snippet, got ${pyTokens}`);
    });

    await test('Should estimate message framing and tool call overhead', () => {
      const message = {
        role: 'model',
        tool_calls: [
          {
            name: 'view_file',
            args: { AbsolutePath: '/workspace/main.js', StartLine: 1, EndLine: 50 }
          }
        ]
      };
      const msgTokens = tokenizer.estimateMessageTokens(message);
      assert(msgTokens >= 15 && msgTokens <= 55, `Expected 15-55 tokens, got ${msgTokens}`);
    });
  });

  // --- Suite 2: Configuration & Dynamic Pricing Unit Tests ---
  await describe('2. Configuration & Dynamic Pricing Unit Tests', async () => {
    await test('Should resolve default pricing for Gemini 3.7 Flash', () => {
      const pricing = config.getModelPricing('Gemini 3.7 Flash (High)');
      assert.strictEqual(pricing.id, 'gemini-3.7-flash');
      assert.strictEqual(pricing.inputPerMillion, 0.15);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.0375);
      assert.strictEqual(pricing.outputPerMillion, 0.60);
    });

    await test('Should resolve pricing for Claude 3.7 Sonnet', () => {
      const pricing = config.getModelPricing('claude-3.7-sonnet');
      assert.strictEqual(pricing.id, 'claude-3.7-sonnet');
      assert.strictEqual(pricing.inputPerMillion, 3.00);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.30);
      assert.strictEqual(pricing.outputPerMillion, 15.00);
    });

    await test('Should resolve pricing for Claude 3.5 Haiku', () => {
      const pricing = config.getModelPricing('claude-3.5-haiku');
      assert.strictEqual(pricing.id, 'claude-3.5-haiku');
      assert.strictEqual(pricing.inputPerMillion, 0.80);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.08);
      assert.strictEqual(pricing.outputPerMillion, 4.00);
    });

    await test('Should resolve pricing for Gemini 2.0 Flash Lite', () => {
      const pricing = config.getModelPricing('gemini-2.0-flash-lite');
      assert.strictEqual(pricing.id, 'gemini-2.0-flash-lite');
      assert.strictEqual(pricing.inputPerMillion, 0.075);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.01875);
      assert.strictEqual(pricing.outputPerMillion, 0.30);
    });

    await test('Should resolve pricing for GPT-4o mini', () => {
      const pricing = config.getModelPricing('gpt-4o-mini');
      assert.strictEqual(pricing.id, 'gpt-4o-mini');
      assert.strictEqual(pricing.inputPerMillion, 0.15);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.075);
      assert.strictEqual(pricing.outputPerMillion, 0.60);
    });

    await test('Should resolve pricing for o3-mini', () => {
      const pricing = config.getModelPricing('o3-mini');
      assert.strictEqual(pricing.id, 'o3-mini');
      assert.strictEqual(pricing.inputPerMillion, 1.10);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.55);
      assert.strictEqual(pricing.outputPerMillion, 4.40);
    });

    await test('Should resolve pricing for o1', () => {
      const pricing = config.getModelPricing('o1');
      assert.strictEqual(pricing.id, 'o1');
      assert.strictEqual(pricing.inputPerMillion, 15.00);
      assert.strictEqual(pricing.cachedInputPerMillion, 7.50);
      assert.strictEqual(pricing.outputPerMillion, 60.00);
    });

    await test('Should dynamically resolve Flash Tier via smart fuzzy heuristic for unlisted models', () => {
      const modelsToTest = [
        'gemini-4.0-flash-next',
        'custom-turbo-model',
        'mistral-small',
        'unlisted-lite-model',
        'unknown-fast-preview'
      ];

      for (const m of modelsToTest) {
        const pricing = config.getModelPricing(m);
        assert.strictEqual(pricing.inputPerMillion, 0.15, `Expected input 0.15 for ${m}`);
        assert.strictEqual(pricing.cachedInputPerMillion, 0.0375, `Expected cached input 0.0375 for ${m}`);
        assert.strictEqual(pricing.outputPerMillion, 0.60, `Expected output 0.60 for ${m}`);
        assert.strictEqual(pricing.tier, 'flash', `Expected flash tier for ${m}`);
      }
    });

    await test('Should dynamically resolve Pro Tier via smart fuzzy heuristic for unlisted models', () => {
      const modelsToTest = [
        'gemini-3.5-pro-preview',
        'gemini-ultra-preview',
        'llama-3-70b-large',
        'qwen-max-latest',
        'deepseek-high'
      ];

      for (const m of modelsToTest) {
        const pricing = config.getModelPricing(m);
        assert.strictEqual(pricing.inputPerMillion, 1.25, `Expected input 1.25 for ${m}`);
        assert.strictEqual(pricing.cachedInputPerMillion, 0.3125, `Expected cached input 0.3125 for ${m}`);
        assert.strictEqual(pricing.outputPerMillion, 5.00, `Expected output 5.00 for ${m}`);
        assert.strictEqual(pricing.tier, 'pro', `Expected pro tier for ${m}`);
      }
    });

    await test('Should dynamically resolve Free Tier for unlisted free/flat/local/ollama models (e.g., custom-free-model)', () => {
      const modelsToTest = [
        'custom-free-model',
        'ollama-llama3',
        'my-local-model',
        'subscription-flat-tier',
        'zero-cost-model'
      ];

      for (const m of modelsToTest) {
        const pricing = config.getModelPricing(m);
        assert.strictEqual(pricing.inputPerMillion, 0.0, `Expected input 0.0 for ${m}`);
        assert.strictEqual(pricing.cachedInputPerMillion, 0.0, `Expected cached input 0.0 for ${m}`);
        assert.strictEqual(pricing.outputPerMillion, 0.0, `Expected output 0.0 for ${m}`);
        assert.strictEqual(pricing.tier, 'free', `Expected free tier for ${m}`);

        const cost = config.calculateCostUsd(500000, 500000, 500000, m);
        assert.strictEqual(cost, 0, `Expected cost 0 for free model ${m}`);
      }
    });

    await test('Should direct-invoke smartHeuristicPricing for heuristic resolution and display names', () => {
      const flashRes = config.smartHeuristicPricing('gemini-4.0-flash-next');
      assert.strictEqual(flashRes.tier, 'flash');
      assert.strictEqual(flashRes.inputPerMillion, 0.15);
      assert.strictEqual(flashRes.displayName, 'Gemini 4.0 Flash Next');

      const proRes = config.smartHeuristicPricing('gemini-3.5-pro-preview');
      assert.strictEqual(proRes.tier, 'pro');
      assert.strictEqual(proRes.inputPerMillion, 1.25);
      assert.strictEqual(proRes.displayName, 'Gemini 3.5 Pro Preview');

      const freeRes = config.smartHeuristicPricing('custom-free-model');
      assert.strictEqual(freeRes.tier, 'free');
      assert.strictEqual(freeRes.inputPerMillion, 0.0);
      assert.strictEqual(freeRes.displayName, 'Custom Free Model');
    });

    await test('Should fallback to Default Flash Tier with auto-generated displayName for unknown models', () => {
      const pricing = config.getModelPricing('deepseek-chat-general');
      assert.strictEqual(pricing.inputPerMillion, 0.15);
      assert.strictEqual(pricing.cachedInputPerMillion, 0.0375);
      assert.strictEqual(pricing.outputPerMillion, 0.60);
      assert(pricing.displayName.includes('Deepseek Chat General'));
    });

    await test('Should resolve suffixed model name via settings-fallback path (REQ-256)', () => {
      // getBaseModelName must strip effort suffix before pricing lookup,
      // even when modelName is empty and settings provides the fallback.
      const baseFromSettings = config.getBaseModelName(config.getActiveModelFromSettings());
      assert(!baseFromSettings.includes('('), `Settings fallback must be suffix-stripped, got "${baseFromSettings}"`);

      const pricing = config.getModelPricing('Claude Opus 4.6 (Thinking)');
      assert.strictEqual(pricing.id, 'claude-3-opus', `Suffixed "Claude Opus 4.6 (Thinking)" must resolve to claude-opus tier, got "${pricing.id}"`);
    });

    await test('Should merge user configuration custom pricing models directly into MODEL_PRICING', () => {
      config.MODEL_PRICING['custom-enterprise-test'] = {
        id: 'custom-enterprise-test',
        displayName: 'Custom Enterprise Test',
        inputPerMillion: 2.50,
        cachedInputPerMillion: 0.50,
        outputPerMillion: 10.00,
        aliases: ['custom-enterprise-test', 'custom-enterprise']
      };

      const resolved = config.getModelPricing('custom-enterprise');
      assert.strictEqual(resolved.id, 'custom-enterprise-test');
      assert.strictEqual(resolved.inputPerMillion, 2.50);
      assert.strictEqual(resolved.cachedInputPerMillion, 0.50);
      assert.strictEqual(resolved.outputPerMillion, 10.00);
    });

    await test('Should calculate token cost accurately in USD', () => {
      // 1,000,000 fresh input ($0.15) + 1,000,000 cached input ($0.0375) + 1,000,000 output ($0.60) = $0.7875
      const cost = config.calculateCostUsd(1000000, 1000000, 1000000, 'gemini-3.7-flash');
      assert.strictEqual(Math.round(cost * 10000) / 10000, 0.7875);
    });

    await test('Should calculate cache savings accurately', () => {
      // 1,000,000 cached tokens regular cost ($0.15) - cached cost ($0.0375) = $0.1125 saved
      const savings = config.calculateCacheSavingsUsd(1000000, 'gemini-3.7-flash');
      assert.strictEqual(Math.round(savings * 10000) / 10000, 0.1125);
    });

    await test('Should convert currencies correctly', () => {
      const usdAmount = 10.0;
      assert.strictEqual(config.convertCurrency(usdAmount, 'usd'), 10.0);
      assert.strictEqual(config.convertCurrency(usdAmount, 'krw'), 14500.0);
      assert.strictEqual(config.convertCurrency(usdAmount, 'jpy'), 1550.0);
      assert.strictEqual(config.convertCurrency(usdAmount, 'eur'), 9.5);
    });
  });

  // --- Suite 3: i18n & Localization Unit Tests ---
  await describe('3. i18n & Localization Unit Tests', async () => {
    await test('Should have all required keys across all supported locale dictionaries', () => {
      const enKeys = Object.keys(i18n.TRANSLATIONS.en);
      assert(enKeys.length >= 123, `English dictionary must have at least 123 keys, got ${enKeys.length}`);

      for (const lang of i18n.SUPPORTED_LOCALES) {
        assert(i18n.TRANSLATIONS[lang], `Dictionary for "${lang}" must exist`);
        const langKeys = Object.keys(i18n.TRANSLATIONS[lang]);
        for (const key of enKeys) {
          assert(
            key in i18n.TRANSLATIONS[lang],
            `Missing key "${key}" in language "${lang}" dictionary`
          );
          assert(
            typeof i18n.TRANSLATIONS[lang][key] === 'string' && i18n.TRANSLATIONS[lang][key].length > 0,
            `Key "${key}" in language "${lang}" must not be empty`
          );
        }
      }
    });

    await test('Should verify filter-related keys across all 21 supported locales', () => {
      const filterKeys = [
        'filterDate', 'filterModel', 'filterAll', 'filterCustom', 'filterApply',
        'filterToday', 'filterYesterday', 'filter7d', 'filter30d',
        'filterFromDate', 'filterToDate'
      ];

      for (const lang of i18n.SUPPORTED_LOCALES) {
        const dict = i18n.TRANSLATIONS[lang];
        for (const fk of filterKeys) {
          assert(dict[fk], `Filter key "${fk}" missing or empty in "${lang}"`);
        }
      }
    });

    await test('Should verify estimate-panel keys across all 21 supported locales (REQ-250..253, 259)', () => {
      const estimateKeys = [
        'estimateDisclaimer', 'estimatePanelTitle', 'estimateMonthToDate',
        'estimateDailyAverage', 'estimateMonthEnd', 'estimateLast30d'
      ];

      assert(i18n.SUPPORTED_LOCALES.length >= 21, `Expected at least 21 supported locales, got ${i18n.SUPPORTED_LOCALES.length}`);
      for (const lang of i18n.SUPPORTED_LOCALES) {
        const dict = i18n.TRANSLATIONS[lang];
        for (const ek of estimateKeys) {
          assert(typeof dict[ek] === 'string' && dict[ek].length > 0, `Estimate key "${ek}" missing or empty in "${lang}"`);
        }
      }
      // Canonical spot-checks (REQ-250 Korean disclaimer text + en reference)
      assert.strictEqual(i18n.TRANSLATIONS.ko.estimateDisclaimer, '이 수치들은 장기 사용 관리를 위한 추정치입니다');
      assert.strictEqual(i18n.TRANSLATIONS.en.estimatePanelTitle, 'Long-Term Usage Estimate');
      // RTL locales keep natural translations (REQ-259)
      assert(i18n.TRANSLATIONS.ar.estimateDisclaimer.length > 0);
      assert(i18n.TRANSLATIONS.he.estimateDisclaimer.length > 0);
    });

    await test('Should correctly detect and handle RTL locales', () => {
      assert(Array.isArray(i18n.RTL_LOCALES), 'RTL_LOCALES must be an array');
      assert(i18n.RTL_LOCALES.includes('ar'), 'ar must be in RTL_LOCALES');
      assert(i18n.RTL_LOCALES.includes('he'), 'he must be in RTL_LOCALES');

      assert.strictEqual(i18n.isRtl('ar'), true);
      assert.strictEqual(i18n.isRtl('he'), true);
      assert.strictEqual(i18n.isRtl('ar-EG'), true);
      assert.strictEqual(i18n.isRtl('he-IL'), true);
      assert.strictEqual(i18n.isRtl('en'), false);
      assert.strictEqual(i18n.isRtl('ko'), false);
      assert.strictEqual(i18n.isRtl('zh-TW'), false);
    });

    await test('Should handle hyphenated and regional locales including zh-TW without truncation', () => {
      assert(i18n.SUPPORTED_LOCALES.includes('zh-TW'));

      // normalizeLocale tests
      assert.strictEqual(i18n.normalizeLocale('zh-TW'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('zh_TW'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('zh_TW.UTF-8'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('zh-tw'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('zh-Hant-TW'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('zh-HK'), 'zh-TW');
      assert.strictEqual(i18n.normalizeLocale('de-DE'), 'de');
      assert.strictEqual(i18n.normalizeLocale('ko-KR'), 'ko');

      // setLocale and getAllTranslations
      i18n.setLocale('zh-TW');
      assert.strictEqual(i18n.getLocale(), 'zh-TW');
      assert.strictEqual(i18n.t('appName'), 'Antigravity 詞元與成本追蹤器');
      const allZhTw = i18n.getAllTranslations('zh-TW');
      assert.strictEqual(allZhTw.appName, 'Antigravity 詞元與成本追蹤器');
    });

    await test('Should translate with parameter substitution', () => {
      const translated = i18n.t('sessionNotFound', { id: 'test-123' }, 'en');
      assert.strictEqual(translated, 'Session "test-123" not found in brain directory.');

      const translatedKo = i18n.t('sessionNotFound', { id: 'test-123' }, 'ko');
      assert.strictEqual(translatedKo, '세션 "test-123"을(를) 찾을 수 없습니다.');
    });

    await test('Should switch and retain active locale', () => {
      i18n.setLocale('ko');
      assert.strictEqual(i18n.getLocale(), 'ko');
      assert.strictEqual(i18n.t('periodToday'), '오늘');

      i18n.setLocale('ja');
      assert.strictEqual(i18n.getLocale(), 'ja');
      assert.strictEqual(i18n.t('periodToday'), '今日');

      i18n.setLocale('zh');
      assert.strictEqual(i18n.getLocale(), 'zh');
      assert.strictEqual(i18n.t('periodToday'), '今天');

      i18n.setLocale('de');
      assert.strictEqual(i18n.getLocale(), 'de');
      assert.strictEqual(i18n.t('periodToday'), 'Heute');

      i18n.setLocale('fr');
      assert.strictEqual(i18n.getLocale(), 'fr');
      assert.strictEqual(i18n.t('periodToday'), "Aujourd'hui");

      i18n.setLocale('ar');
      assert.strictEqual(i18n.getLocale(), 'ar');
      assert.strictEqual(i18n.t('periodToday'), 'اليوم');

      i18n.setLocale('en');
      assert.strictEqual(i18n.getLocale(), 'en');
      assert.strictEqual(i18n.t('periodToday'), 'Today');
    });
  });

  // --- Suite 4: Log Parser & Transcript Processing Unit Tests ---
  
  await describe('5. Quota & MiniBar Unit Tests', async () => {

    await test('Should format mini bar correctly', () => {
      assert.strictEqual(formatter.formatMiniBar(100), '▰▰▰▰▰');
      assert.strictEqual(formatter.formatMiniBar(50), '▰▰▰▱▱');
      assert.strictEqual(formatter.formatMiniBar(0), '▱▱▱▱▱');
      assert.strictEqual(formatter.formatMiniBar(80), '▰▰▰▰▱');
    });

    await test('Should parse quota config correctly', () => {
      const parsedConfig = config.loadUserConfig();
      // Even if not mocked, default quota should exist
      assert.strictEqual(parsedConfig.quota.limit5h, 20000000);
      assert.strictEqual(parsedConfig.quota.limit7d, 150000000);
    });

    await test('Should calculate rolling usage correctly', () => {
      const now = new Date('2026-08-27T12:00:00Z');
      const sessions = [{
        sessionId: 'test',
        turns: [
          { createdAt: '2026-08-27T10:00:00Z', inputTokens: 50, cachedTokens: 0, outputTokens: 50 }, // within 5h
          { createdAt: '2026-08-25T10:00:00Z', inputTokens: 100, cachedTokens: 0, outputTokens: 100 }, // within 7d
          { createdAt: '2026-08-10T10:00:00Z', inputTokens: 1000, cachedTokens: 0, outputTokens: 1000 } // older
        ]
      }];
      const result = aggregator.getRollingUsage(sessions, now, { limit5h: 1000, limit7d: 1000 });
      assert.strictEqual(result.tokens5h, 100);
      assert.strictEqual(result.tokens7d, 300);
      assert.strictEqual(result.used5hPercent, 10);
      assert.strictEqual(result.remain5hPercent, 90);
      assert.strictEqual(result.used7dPercent, 30);
      assert.strictEqual(result.remain7dPercent, 70);
    });

  });

  await describe('4. Log Parser Unit Tests', async () => {
    const tempDir = path.join(os.tmpdir(), `agy_test_parser_${Date.now()}`);
    fs.mkdirSync(tempDir, { recursive: true });

    const mockTranscriptFile = path.join(tempDir, 'transcript.jsonl');
    const sampleLines = [
      JSON.stringify({
        step_index: 0,
        source: 'USER_EXPLICIT',
        type: 'USER_INPUT',
        created_at: '2026-08-27T08:00:00Z',
        content: 'Please refactor the database layer.'
      }),
      JSON.stringify({
        step_index: 1,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        created_at: '2026-08-27T08:00:05Z',
        tool_calls: [{ name: 'view_file', args: { AbsolutePath: '/src/db.js' } }]
      }),
      JSON.stringify({
        step_index: 2,
        source: 'SYSTEM',
        type: 'VIEW_FILE',
        created_at: '2026-08-27T08:00:06Z',
        content: 'const db = require("sqlite3");\nfunction connect() { return new db.Database(); }'
      }),
      JSON.stringify({
        step_index: 3,
        source: 'MODEL',
        type: 'PLANNER_RESPONSE',
        created_at: '2026-08-27T08:00:10Z',
        content: 'I have reviewed the database file and planned the refactor.'
      })
    ];
    fs.writeFileSync(mockTranscriptFile, sampleLines.join('\n'), 'utf8');

    await test('Should parse transcript.jsonl stream into structured session', async () => {
      const parsed = await logParser.parseTranscriptFile(
        mockTranscriptFile,
        'test-session-001',
        { title: 'Refactor DB', workspace: '/test/workspace' },
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.sessionId, 'test-session-001');
      assert.strictEqual(parsed.turnCount, 4);
      assert(parsed.totalTokens > 1000, `Expected > 1000 total tokens with baseline, got ${parsed.totalTokens}`);
      assert(parsed.costUsd > 0, 'Cost must be greater than 0');
      assert(parsed.turns.length === 4, 'Should contain 4 turns');
    });

    await test('Should handle malformed or empty lines gracefully', async () => {
      const corruptFile = path.join(tempDir, 'corrupt.jsonl');
      fs.writeFileSync(corruptFile, 'INVALID JSON LINE\n\n{"incomplete":\n', 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        corruptFile,
        'corrupt-session',
        {},
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.turnCount, 0);
      assert.strictEqual(parsed.totalTokens, 0);
    });

    await test('Should extract effort-suffixed model from USER_SETTINGS_CHANGE blocks (REQ-254)', async () => {
      const settingsFile = path.join(tempDir, 'settings-change.jsonl');
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from None to Gemini 3.7 Flash (High)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Working on the task.'
        })
      ];
      fs.writeFileSync(settingsFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        settingsFile,
        'settings-session-001',
        { title: 'Settings Change' },
        'gemini-3.7-flash'
      );

      // Session model must be the effort-suffixed display string from the
      // LAST settings-change block, not the param fallback.
      assert.strictEqual(parsed.modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turnCount, 2);
      assert(parsed.costUsd > 0, 'Session cost must still be computed');
    });

    await test('Should fall back to param model when no USER_SETTINGS_CHANGE present (REQ-254)', async () => {
      const noSettingsFile = path.join(tempDir, 'no-settings-change.jsonl');
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: 'Plain prompt with no settings block.'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Response without settings changes.'
        })
      ];
      fs.writeFileSync(noSettingsFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        noSettingsFile,
        'no-settings-session',
        { title: 'No Settings' },
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.modelName, 'gemini-3.7-flash');
    });

    await test('Should use the LAST settings-change when multiple blocks exist (REQ-254)', async () => {
      const multiFile = path.join(tempDir, 'multi-settings-change.jsonl');
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from None to Gemini 3.7 Flash (High)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'First phase done.'
        }),
        JSON.stringify({
          step_index: 2,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:01:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from Gemini 3.7 Flash (High) to Claude Opus 4.6 (Thinking)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 3,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:01:05Z',
          content: 'Second phase done.'
        })
      ];
      fs.writeFileSync(multiFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        multiFile,
        'multi-settings-session',
        { title: 'Multi Settings' },
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.modelName, 'Claude Opus 4.6 (Thinking)');
    });

    await test('Should sanitize trailing boilerplate from settings-change model name (REQ-255)', async () => {
      const pollutedFile = path.join(tempDir, 'polluted-settings-change.jsonl');
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from None to Claude Opus 4.6 (Thinking). No need to comment on this change.\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Working on the task.'
        })
      ];
      fs.writeFileSync(pollutedFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        pollutedFile,
        'polluted-settings-session',
        { title: 'Polluted Settings' },
        'gemini-3.7-flash'
      );

      // Live transcripts append prompt boilerplate after the model name on
      // the same line; the captured identity must be the clean display
      // string with no prose suffix (REQ-255).
      assert.strictEqual(parsed.modelName, 'Claude Opus 4.6 (Thinking)');
      assert(!parsed.modelName.includes('. No need'), 'Model name must not contain boilerplate');
      assert(!parsed.modelName.includes('No need to comment'), 'Model name must not contain prose suffix');
    });

    await test('Should stamp each turn with active model across 2 settings changes and compute accurate session totals (REQ-307)', async () => {
      const switchFile = path.join(tempDir, 'two-settings-change.jsonl');
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from None to Gemini 3.7 Flash (Low)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Working on phase 1.'
        }),
        JSON.stringify({
          step_index: 2,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:01:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from Gemini 3.7 Flash (Low) to Gemini 3.7 Flash (High)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 3,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:01:05Z',
          content: 'Working on phase 2.'
        })
      ];
      fs.writeFileSync(switchFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        switchFile,
        'two-settings-session',
        { title: 'Two Settings Changes' },
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.turnCount, 4);
      assert.strictEqual(parsed.turns[0].modelName, 'Gemini 3.7 Flash (Low)');
      assert.strictEqual(parsed.turns[1].modelName, 'Gemini 3.7 Flash (Low)');
      assert.strictEqual(parsed.turns[2].modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turns[3].modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turns[parsed.turns.length - 1].modelName, 'Gemini 3.7 Flash (High)');

      assert.strictEqual(parsed.modelName, 'Gemini 3.7 Flash (High)');
      assert.deepStrictEqual(parsed.models, ['Gemini 3.7 Flash (Low)', 'Gemini 3.7 Flash (High)']);

      // Per-turn cost must be > 0 and session cost must match sum of turn costs
      assert(parsed.turns.every(t => typeof t.costUsd === 'number' && t.costUsd > 0), 'All turns should have costUsd > 0');
      const sumTurnCosts = parsed.turns.reduce((acc, t) => acc + t.costUsd, 0);
      assert(Math.abs(parsed.costUsd - sumTurnCosts) < 1e-9, `Session cost (${parsed.costUsd}) should equal sum of turn costs (${sumTurnCosts})`);
    });

    await test('Should track model transitions across 3 settings-changes preserving turn-level model boundaries (REQ-307)', async () => {
      const threeSwitchFile = path.join(tempDir, 'three-settings-change.jsonl');
      const lines = [
        // Turn 0: Switch to Model 1
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from None to Gemini 3.7 Flash (Low)\n</USER_SETTINGS_CHANGE>'
        }),
        // Turn 1: Model response under Model 1
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Phase 1 response.'
        }),
        // Turn 2: Switch to Model 2
        JSON.stringify({
          step_index: 2,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:01:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from Gemini 3.7 Flash (Low) to Gemini 3.7 Flash (High)\n</USER_SETTINGS_CHANGE>'
        }),
        // Turn 3: Model response under Model 2
        JSON.stringify({
          step_index: 3,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:01:05Z',
          content: 'Phase 2 response.'
        }),
        // Turn 4: Plain user input without settings change (remains Model 2)
        JSON.stringify({
          step_index: 4,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:02:00Z',
          content: 'Continue with phase 2 task.'
        }),
        // Turn 5: Model response under Model 2
        JSON.stringify({
          step_index: 5,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:02:05Z',
          content: 'Phase 2 continuation response.'
        }),
        // Turn 6: Switch to Model 3
        JSON.stringify({
          step_index: 6,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:03:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from Gemini 3.7 Flash (High) to Claude Opus 4.6 (Thinking)\n</USER_SETTINGS_CHANGE>'
        }),
        // Turn 7: Model response under Model 3
        JSON.stringify({
          step_index: 7,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:03:05Z',
          content: 'Phase 3 response with Claude.'
        })
      ];
      fs.writeFileSync(threeSwitchFile, lines.join('\n'), 'utf8');

      const parsed = await logParser.parseTranscriptFile(
        threeSwitchFile,
        'three-settings-session',
        { title: 'Three Settings Changes' },
        'gemini-3.7-flash'
      );

      assert.strictEqual(parsed.turnCount, 8);
      // Turn 0-1: Gemini 3.7 Flash (Low)
      assert.strictEqual(parsed.turns[0].modelName, 'Gemini 3.7 Flash (Low)');
      assert.strictEqual(parsed.turns[1].modelName, 'Gemini 3.7 Flash (Low)');
      // Turn 2-5: Gemini 3.7 Flash (High)
      assert.strictEqual(parsed.turns[2].modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turns[3].modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turns[4].modelName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(parsed.turns[5].modelName, 'Gemini 3.7 Flash (High)');
      // Turn 6-7: Claude Opus 4.6 (Thinking)
      assert.strictEqual(parsed.turns[6].modelName, 'Claude Opus 4.6 (Thinking)');
      assert.strictEqual(parsed.turns[7].modelName, 'Claude Opus 4.6 (Thinking)');

      // Session modelName is last active model
      assert.strictEqual(parsed.modelName, 'Claude Opus 4.6 (Thinking)');
      // Session models list in first-appearance order
      assert.deepStrictEqual(parsed.models, [
        'Gemini 3.7 Flash (Low)',
        'Gemini 3.7 Flash (High)',
        'Claude Opus 4.6 (Thinking)'
      ]);

      const sumTurnCosts = parsed.turns.reduce((acc, t) => acc + t.costUsd, 0);
      assert(Math.abs(parsed.costUsd - sumTurnCosts) < 1e-9);
    });

    // Cleanup temp directory
    try {
      fs.rmSync(tempDir, { recursive: true, force: true });
    } catch (_e) {}
  });

  // --- Suite 5: Cache Manager Unit Tests ---
  await describe('5. Cache Manager Unit Tests', async () => {
    const tempDir = path.join(os.tmpdir(), `agy_test_cache_${Date.now()}`);
    fs.mkdirSync(tempDir, { recursive: true });
    const cacheFile = path.join(tempDir, 'test_cache.json');

    await test('Should return clean initial cache structure if file does not exist', () => {
      const c = cacheManager.loadCache(cacheFile);
      assert.strictEqual(c.version, cacheManager.CACHE_SCHEMA_VERSION);
      assert.deepStrictEqual(c.sessions, {});
    });

    await test('Should save and reload cache atomically', () => {
      const sampleCache = {
        version: cacheManager.CACHE_SCHEMA_VERSION,
        lastUpdated: new Date().toISOString(),
        sessions: {
          'sess-1': {
            sessionId: 'sess-1',
            totalTokens: 5000,
            costUsd: 0.005,
            mtimeMs: 12345678,
            size: 1024
          }
        }
      };

      cacheManager.saveCache(sampleCache, cacheFile);
      assert(fs.existsSync(cacheFile), 'Cache file must exist');

      const loaded = cacheManager.loadCache(cacheFile);
      assert.strictEqual(loaded.sessions['sess-1'].totalTokens, 5000);
      assert.strictEqual(loaded.sessions['sess-1'].costUsd, 0.005);
    });

    await test('Should clear cache file successfully', () => {
      const removed = cacheManager.clearCache(cacheFile);
      assert.strictEqual(removed, true);
      assert.strictEqual(fs.existsSync(cacheFile), false);
    });

    // Cleanup temp
    try {
      fs.rmSync(tempDir, { recursive: true, force: true });
    } catch (_e) {}
  });

  // --- Suite 6: Aggregator Unit Tests ---
  await describe('6. Aggregator Unit Tests', async () => {
    const mockDate = new Date('2026-08-27T12:00:00Z');
    const mockSessions = [
      {
        sessionId: 'sess-today-1',
        title: 'Today Task 1',
        workspace: '/ws1',
        startTime: '2026-08-27T08:00:00Z',
        turns: [
          {
            stepIndex: 0,
            inputTokens: 1000,
            cachedTokens: 0,
            outputTokens: 200,
            costUsd: 0.00027,
            createdAt: '2026-08-27T08:00:00Z'
          },
          {
            stepIndex: 1,
            inputTokens: 500,
            cachedTokens: 1200,
            outputTokens: 300,
            costUsd: 0.0003,
            createdAt: '2026-08-27T08:05:00Z'
          }
        ]
      },
      {
        sessionId: 'sess-yesterday-1',
        title: 'Yesterday Task 1',
        workspace: '/ws2',
        startTime: '2026-08-26T10:00:00Z',
        turns: [
          {
            stepIndex: 0,
            inputTokens: 2000,
            cachedTokens: 1000,
            outputTokens: 500,
            costUsd: 0.00063,
            createdAt: '2026-08-26T10:00:00Z'
          }
        ]
      }
    ];

    await test('Should aggregate today usage correctly', () => {
      const todaySummary = aggregator.getToday(mockSessions, mockDate, 'gemini-3.7-flash');
      assert.strictEqual(todaySummary.totalSessions, 1);
      assert.strictEqual(todaySummary.totalTurns, 2);
      assert.strictEqual(todaySummary.inputTokens, 1500);
      assert.strictEqual(todaySummary.cachedTokens, 1200);
      assert.strictEqual(todaySummary.outputTokens, 500);
      assert.strictEqual(todaySummary.totalTokens, 3200);
      assert(todaySummary.cacheHitRate > 40 && todaySummary.cacheHitRate < 50);
    });

    await test('Should aggregate yesterday usage correctly', () => {
      const yestSummary = aggregator.getYesterday(mockSessions, mockDate, 'gemini-3.7-flash');
      assert.strictEqual(yestSummary.totalSessions, 1);
      assert.strictEqual(yestSummary.totalTurns, 1);
      assert.strictEqual(yestSummary.inputTokens, 2000);
      assert.strictEqual(yestSummary.cachedTokens, 1000);
      assert.strictEqual(yestSummary.outputTokens, 500);
      assert.strictEqual(yestSummary.totalTokens, 3500);
    });

    await test('Should aggregate 7-day breakdown and grand total', () => {
      const summary7d = aggregator.getLastNDays(mockSessions, 7, mockDate, 'gemini-3.7-flash');
      assert.strictEqual(summary7d.daily.length, 7);
      assert.strictEqual(summary7d.totalSessions, 2);
      assert.strictEqual(summary7d.totalTurns, 3);
      assert.strictEqual(summary7d.totalTokens, 6700);
    });

    await test('Should aggregate custom date range', () => {
      const rangeSummary = aggregator.getDateRange(
        mockSessions,
        '2026-08-26..2026-08-27',
        'gemini-3.7-flash'
      );
      assert.strictEqual(rangeSummary.totalTurns, 3);
      assert.strictEqual(rangeSummary.totalTokens, 6700);
    });

    await test('Should drilldown into a specific session', () => {
      const detail = aggregator.getSessionDrilldown(mockSessions, 'sess-today-1');
      assert(detail !== null);
      assert.strictEqual(detail.sessionId, 'sess-today-1');
      assert.strictEqual(detail.title, 'Today Task 1');
    });
  });

  // --- Suite 7: Formatter Unit Tests ---
  await describe('7. Formatter Unit Tests', async () => {
    await test('Should format numbers with commas and compact suffixes', () => {
      assert.strictEqual(formatter.formatNumber(1234567), '1,234,567');
      assert.strictEqual(formatter.formatCompact(450), '450');
      assert.strictEqual(formatter.formatCompact(1500), '1.5k');
      assert.strictEqual(formatter.formatCompact(2500000), '2.50M');
    });

    await test('Should format currency strings for USD, KRW, JPY, EUR', () => {
      const costUsd = 0.05;
      assert.strictEqual(formatter.formatCurrency(costUsd, 'usd'), '$0.050');
      assert(formatter.formatCurrency(costUsd, 'krw').startsWith('₩'));
      assert(formatter.formatCurrency(costUsd, 'jpy').startsWith('¥'));
      assert(formatter.formatCurrency(costUsd, 'eur').endsWith('€'));
    });

    await test('Should accurately strip ANSI and calculate CJK display width', () => {
      const styled = '\x1b[31mHello\x1b[0m';
      assert.strictEqual(formatter.stripAnsi(styled), 'Hello');
      assert.strictEqual(formatter.getDisplayWidth('Hello'), 5);
      assert.strictEqual(formatter.getDisplayWidth('안녕'), 4);
    });

    await test('Should render progress bar', () => {
      const bar = formatter.renderProgressBar(50, 10);
      assert(bar.includes('50.0%'));
    });
  });

  // --- Suite 8: Hook Handler Unit Tests ---
  await describe('8. Hook Handler Unit Tests', async () => {
    await test('Should generate a clean single-line real-time status badge', () => {
      const badgeStr = formatter.renderRealTimeBadge(
        {
          turnTokens: 2100,
          turnCostUsd: 0.0003,
          todayTokens: 85000,
          todayCostUsd: 0.0125,
          cacheHitRate: 75.4
        },
        'usd'
      );

      assert(badgeStr.includes('⚡ [Antigravity]'));
      assert(badgeStr.includes('2.1k'));
      assert(badgeStr.includes('85.0k'));
      assert(badgeStr.includes('75%'));
    });

    await test('Should generate badge in free quota mode', () => {
      const badgeStr = formatter.renderRealTimeBadge(
        {
          turnTokens: 2100,
          turnCostUsd: 0,
          todayTokens: 85000,
          todayCostUsd: 0,
          cacheHitRate: 75.4
        },
        'usd',
        true
      );

      assert(badgeStr.includes('⚡ [Antigravity]'));
      assert(badgeStr.includes('Free'));
    });

    await test('Should format hook response matching Antigravity PostInvocation schema', () => {
      const resp = hookHandler.formatHookResponse('⚡ [Antigravity] Turn: 1.2k | Today: 45k');
      assert(resp && Array.isArray(resp.injectSteps), 'Must have injectSteps array');
      assert.strictEqual(resp.injectSteps.length, 1);
      assert.strictEqual(resp.injectSteps[0].ephemeralMessage, '⚡ [Antigravity] Turn: 1.2k | Today: 45k');
    });

    await test('handlePostInvocation should return structured payload including injectSteps', async () => {
      const result = await hookHandler.handlePostInvocation({ currency: 'usd' });
      assert(typeof result.badge === 'string', 'result.badge must be a string');
      assert(Array.isArray(result.injectSteps), 'result.injectSteps must be an array');
      assert.strictEqual(result.injectSteps[0].ephemeralMessage, result.badge);
      assert(typeof result.turnTokens === 'number');
      assert(typeof result.todayTokens === 'number');
    });

    await test('readStdinJson should safely resolve null on TTY or empty input without blocking', async () => {
      const input = await hookHandler.readStdinJson(10);
      assert(input === null || typeof input === 'object');
    });

  });

  // --- Suite 9: CLI Argument Parsing Unit Tests ---
  await describe('9. CLI Argument Parser Unit Tests', async () => {
    await test('Should default to --today if no arguments provided', () => {
      const opts = parseArgs(['node', 'bin/agy-tokens.js']);
      assert.strictEqual(opts.today, true);
      assert.strictEqual(opts.yesterday, false);
      assert.strictEqual(opts.sevenDays, false);
    });

    await test('Should parse flags and option values correctly', () => {
      const opts = parseArgs([
        'node',
        'bin/agy-tokens.js',
        '--7d',
        '--currency',
        'krw',
        '--lang',
        'ko',
        '--model',
        'claude-3.7-sonnet',
        '--json'
      ]);

      assert.strictEqual(opts.sevenDays, true);
      assert.strictEqual(opts.currency, 'krw');
      assert.strictEqual(opts.lang, 'ko');
      assert.strictEqual(opts.model, 'claude-3.7-sonnet');
      assert.strictEqual(opts.json, true);
    });

    await test('Should parse --free and --no-cost flags', () => {
      const optsFree = parseArgs(['node', 'bin/agy-tokens.js', '--free']);
      assert.strictEqual(optsFree.free, true);

      const optsNoCost = parseArgs(['node', 'bin/agy-tokens.js', '--no-cost']);
      assert.strictEqual(optsNoCost.free, true);
    });

    await test('Should parse range and session options', () => {
      const opts = parseArgs([
        'node',
        'bin/agy-tokens.js',
        '--range',
        '2026-08-01..2026-08-27',
        '--session',
        'abc-123'
      ]);

      assert.strictEqual(opts.range, '2026-08-01..2026-08-27');
      assert.strictEqual(opts.session, true);
      assert.strictEqual(opts.sessionId, 'abc-123');
    });

    await test('Should parse --raw and --hook flags', () => {
      const optsHook = parseArgs(['node', 'bin/agy-tokens.js', '--hook']);
      assert.strictEqual(optsHook.hook, true);
      assert.strictEqual(optsHook.raw, false);

      const optsRaw = parseArgs(['node', 'bin/agy-tokens.js', '--hook', '--raw']);
      assert.strictEqual(optsRaw.hook, true);
      assert.strictEqual(optsRaw.raw, true);
    });

    await test('Should parse pricing catalog subcommands and flags', () => {
      const opts1 = parseArgs(['node', 'bin/agy-tokens.js', 'sync-prices']);
      assert.strictEqual(opts1.sync, true);

      const opts2 = parseArgs(['node', 'bin/agy-tokens.js', '--prices']);
      assert.strictEqual(opts2.prices, true);

      const opts3 = parseArgs(['node', 'bin/agy-tokens.js', 'models', '--auto-sync']);
      assert.strictEqual(opts3.prices, true);
      assert.strictEqual(opts3.autoSync, true);
    });
  });

  // --- Suite 10: Toolkit Subcommand & Statusline-Only Concept Integrity ---
  await describe('10. Toolkit Subcommand & Statusline-Only Concept Integrity', async () => {
    await test('Should correctly verify package.json bin registrations', () => {
      const pkgJson = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));
      assert.strictEqual(pkgJson.name, 'agy-tools');
      assert.strictEqual(pkgJson.bin['agy-tools'], './bin/agy-tools.js');
      assert.strictEqual(pkgJson.bin['agy-dashboard'], './bin/agy-tokens.js');
      assert.strictEqual(pkgJson.bin['agy-tokens'], './bin/agy-tokens.js');
    });

    await test('Should have valid executable entry files', () => {
      assert(fs.existsSync(path.join(__dirname, '..', 'bin', 'agy-tokens.js')));
      assert(fs.existsSync(path.join(__dirname, '..', 'bin', 'agy-tools.js')));
    });

    await test('Statusline-only concept: integrations/skills/ must NOT exist (no skills regression guard)', () => {
      const skillsDir = path.join(__dirname, '..', 'integrations', 'skills');
      assert(!fs.existsSync(skillsDir), 'integrations/skills/ must not exist — statusline-only concept forbids skills');
    });

    await test('Statusline-only concept: integrations/hooks.json must NOT exist (no hooks regression guard)', () => {
      const hooksPath = path.join(__dirname, '..', 'integrations', 'hooks.json');
      assert(!fs.existsSync(hooksPath), 'integrations/hooks.json must not exist — statusline --write-dashboard supersedes the PostInvocation hook');
    });

    await test('Statusline-only concept: README documents the statusLine settings.json snippet with --write-dashboard', () => {
      const readme = fs.readFileSync(path.join(__dirname, '..', 'README.md'), 'utf8');
      assert(readme.includes('statusLine'), 'README must document the statusLine settings.json entry');
      assert(readme.includes('--write-dashboard'), 'README statusLine snippet must include --write-dashboard');
      assert(readme.includes('PROGRA~1'), 'README statusLine snippet must use 8.3 short paths');
      assert(!/\/usage|\/tokens|skill|hooks\.json/i.test(readme), 'README must not reference /usage, /tokens, skills, or hooks.json');
    });
  });

  // --- Suite 11: Pricing Catalog Data Integrity & Model Coverage ---
  await describe('11. Pricing Catalog Data Integrity & Model Coverage', async () => {
    const pricingFilePath = path.join(__dirname, '..', 'data', 'pricing.json');

    await test('data/pricing.json file must exist and be valid JSON', () => {
      assert(fs.existsSync(pricingFilePath), 'data/pricing.json must exist');
      const raw = fs.readFileSync(pricingFilePath, 'utf8');
      const parsed = JSON.parse(raw);
      assert(parsed && typeof parsed === 'object');
      const validation = priceSyncer.validatePricingData(parsed);
      assert.strictEqual(validation.valid, true, `Validation failed: ${validation.error}`);
    });

    await test('Should have complete official Google Gemini models and exact rates', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      const models = catalog.models;

      // Gemini 3.7 Flash
      assert(models['gemini-3.7-flash']);
      assert.strictEqual(models['gemini-3.7-flash'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-3.7-flash'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-3.7-flash'].outputPerMillion, 0.60);

      // Gemini 3.7 Flash Thinking
      assert(models['gemini-3.7-flash-thinking']);
      assert.strictEqual(models['gemini-3.7-flash-thinking'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-3.7-flash-thinking'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-3.7-flash-thinking'].outputPerMillion, 0.60);

      // Gemini 2.5 Flash
      assert(models['gemini-2.5-flash']);
      assert.strictEqual(models['gemini-2.5-flash'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-2.5-flash'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-2.5-flash'].outputPerMillion, 0.60);

      // Gemini 2.5 Pro
      assert(models['gemini-2.5-pro']);
      assert.strictEqual(models['gemini-2.5-pro'].inputPerMillion, 1.25);
      assert.strictEqual(models['gemini-2.5-pro'].cachedInputPerMillion, 0.3125);
      assert.strictEqual(models['gemini-2.5-pro'].outputPerMillion, 5.00);

      // Gemini 2.0 Flash
      assert(models['gemini-2.0-flash']);
      assert.strictEqual(models['gemini-2.0-flash'].inputPerMillion, 0.10);
      assert.strictEqual(models['gemini-2.0-flash'].cachedInputPerMillion, 0.025);
      assert.strictEqual(models['gemini-2.0-flash'].outputPerMillion, 0.40);

      // Gemini 2.0 Flash Lite
      assert(models['gemini-2.0-flash-lite']);
      assert.strictEqual(models['gemini-2.0-flash-lite'].inputPerMillion, 0.075);
      assert.strictEqual(models['gemini-2.0-flash-lite'].cachedInputPerMillion, 0.01875);
      assert.strictEqual(models['gemini-2.0-flash-lite'].outputPerMillion, 0.30);
    });

    await test('Should have complete official Anthropic Claude models and exact rates', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      const models = catalog.models;

      // Claude 3.7 Sonnet
      assert(models['claude-3.7-sonnet']);
      assert.strictEqual(models['claude-3.7-sonnet'].inputPerMillion, 3.00);
      assert.strictEqual(models['claude-3.7-sonnet'].cachedInputPerMillion, 0.30);
      assert.strictEqual(models['claude-3.7-sonnet'].outputPerMillion, 15.00);

      // Claude 3.5 Sonnet
      assert(models['claude-3.5-sonnet']);
      assert.strictEqual(models['claude-3.5-sonnet'].inputPerMillion, 3.00);
      assert.strictEqual(models['claude-3.5-sonnet'].cachedInputPerMillion, 0.30);
      assert.strictEqual(models['claude-3.5-sonnet'].outputPerMillion, 15.00);

      // Claude 3.5 Haiku
      assert(models['claude-3.5-haiku']);
      assert.strictEqual(models['claude-3.5-haiku'].inputPerMillion, 0.80);
      assert.strictEqual(models['claude-3.5-haiku'].cachedInputPerMillion, 0.08);
      assert.strictEqual(models['claude-3.5-haiku'].outputPerMillion, 4.00);

      // Claude 3 Opus
      assert(models['claude-3-opus']);
      assert.strictEqual(models['claude-3-opus'].inputPerMillion, 15.00);
      assert.strictEqual(models['claude-3-opus'].cachedInputPerMillion, 1.50);
      assert.strictEqual(models['claude-3-opus'].outputPerMillion, 75.00);
    });

    await test('Should have complete official OpenAI models and exact rates', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      const models = catalog.models;

      // GPT-4o
      assert(models['gpt-4o']);
      assert.strictEqual(models['gpt-4o'].inputPerMillion, 2.50);
      assert.strictEqual(models['gpt-4o'].cachedInputPerMillion, 1.25);
      assert.strictEqual(models['gpt-4o'].outputPerMillion, 10.00);

      // GPT-4o mini
      assert(models['gpt-4o-mini']);
      assert.strictEqual(models['gpt-4o-mini'].inputPerMillion, 0.15);
      assert.strictEqual(models['gpt-4o-mini'].cachedInputPerMillion, 0.075);
      assert.strictEqual(models['gpt-4o-mini'].outputPerMillion, 0.60);

      // o3-mini
      assert(models['o3-mini']);
      assert.strictEqual(models['o3-mini'].inputPerMillion, 1.10);
      assert.strictEqual(models['o3-mini'].cachedInputPerMillion, 0.55);
      assert.strictEqual(models['o3-mini'].outputPerMillion, 4.40);

      // o1
      assert(models['o1']);
      assert.strictEqual(models['o1'].inputPerMillion, 15.00);
      assert.strictEqual(models['o1'].cachedInputPerMillion, 7.50);
      assert.strictEqual(models['o1'].outputPerMillion, 60.00);
    });

    await test('Should contain proper catalog metadata and source URLs', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      assert.strictEqual(typeof catalog.version, 'string');
      assert.strictEqual(typeof catalog.lastUpdated, 'string');
      assert(catalog.sources.google.includes('ai.google.dev'));
      assert(catalog.sources.anthropic.includes('anthropic.com'));
      assert(catalog.sources.openai.includes('openai.com'));
    });
  });

  // --- Suite 12: Price Syncer Unit Tests (Download, Validation & Fallback) ---
  await describe('12. Price Syncer Unit Tests (Download, Validation & Fallback)', async () => {
    await test('validatePricingData should validate correct schema and reject corrupt payloads', () => {
      const validPayload = {
        version: '1.0.0',
        models: {
          'test-model': {
            id: 'test-model',
            inputPerMillion: 1.0,
            cachedInputPerMillion: 0.25,
            outputPerMillion: 3.0
          }
        }
      };
      assert.strictEqual(priceSyncer.validatePricingData(validPayload).valid, true);

      assert.strictEqual(priceSyncer.validatePricingData(null).valid, false);
      assert.strictEqual(priceSyncer.validatePricingData({}).valid, false);
      assert.strictEqual(priceSyncer.validatePricingData({ models: {} }).valid, false);
      assert.strictEqual(priceSyncer.validatePricingData({ models: { 'bad': { inputPerMillion: -1 } } }).valid, false);
      assert.strictEqual(priceSyncer.validatePricingData({ models: { 'bad': { inputPerMillion: 'invalid' } } }).valid, false);
    });

    await test('loadBundledPricing should load and validate bundled data/pricing.json', () => {
      const bundled = priceSyncer.loadBundledPricing();
      assert(bundled !== null);
      assert(bundled.models['gemini-3.7-flash']);
      assert(bundled.models['claude-3.7-sonnet']);
      assert(bundled.models['gpt-4o']);
    });

    await test('saveSyncedPricing and loadLocalSyncedPricing should persist and read cached data', () => {
      const tmpCachePath = path.join(os.tmpdir(), `test_pricing_cache_${Date.now()}.json`);
      const mockData = {
        version: '9.9.9',
        lastUpdated: '2026-08-27',
        models: {
          'custom-synced-test': {
            id: 'custom-synced-test',
            displayName: 'Custom Synced Test',
            inputPerMillion: 0.99,
            cachedInputPerMillion: 0.25,
            outputPerMillion: 2.99
          }
        }
      };

      const saved = priceSyncer.saveSyncedPricing(mockData, tmpCachePath);
      assert.strictEqual(saved, true);

      const loaded = priceSyncer.loadLocalSyncedPricing(tmpCachePath);
      assert(loaded !== null);
      assert.strictEqual(loaded.version, '9.9.9');
      assert.strictEqual(loaded.models['custom-synced-test'].inputPerMillion, 0.99);
      assert(loaded._meta && loaded._meta.syncedAt);

      fs.unlinkSync(tmpCachePath);
    });

    await test('getSyncedPricing should fallback to bundled pricing when cache is absent', () => {
      const nonExistentPath = path.join(os.tmpdir(), `non_existent_${Date.now()}.json`);
      const pricing = priceSyncer.getSyncedPricing({ cachePath: nonExistentPath });
      assert(pricing !== null);
      assert.strictEqual(pricing._source, 'bundled');
      assert(pricing.models['gemini-3.7-flash']);
    });

    await test('syncPricing should gracefully handle unreachable URLs and fallback to bundled data', async () => {
      const tmpCache = path.join(os.tmpdir(), `unreachable_cache_${Date.now()}.json`);
      const result = await priceSyncer.syncPricing({
        url: 'http://127.0.0.1:54321/unreachable-endpoint.json',
        destPath: tmpCache,
        timeoutMs: 500
      });

      assert.strictEqual(result.success, false);
      assert.strictEqual(result.source, 'bundled');
      assert(result.modelCount > 0);
      assert(result.error);
    });

    await test('syncPricing should download, validate, and update cache over HTTP mock server', async () => {
      const mockServerPayload = {
        schemaVersion: 1,
        version: '2.0.0-mock',
        lastUpdated: '2026-08-27',
        sources: { test: 'https://test.local/pricing' },
        models: {
          'mock-gemini-next': {
            id: 'mock-gemini-next',
            displayName: 'Mock Gemini Next',
            provider: 'google',
            contextWindow: '2M',
            inputPerMillion: 0.12,
            cachedInputPerMillion: 0.03,
            outputPerMillion: 0.48
          }
        }
      };

      // Create ephemeral local HTTP test server
      const server = http.createServer((req, res) => {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(mockServerPayload));
      });

      await new Promise(res => server.listen(0, '127.0.0.1', res));
      const port = server.address().port;
      const testUrl = `http://127.0.0.1:${port}/pricing.json`;
      const testDestPath = path.join(os.tmpdir(), `mock_synced_pricing_${Date.now()}.json`);

      try {
        const syncResult = await priceSyncer.syncPricing({
          url: testUrl,
          destPath: testDestPath,
          timeoutMs: 2000
        });

        assert.strictEqual(syncResult.success, true);
        assert.strictEqual(syncResult.source, 'remote');
        assert.strictEqual(syncResult.version, '2.0.0-mock');
        assert.strictEqual(syncResult.modelCount, 1);
        assert(fs.existsSync(testDestPath));

        const cachedContent = JSON.parse(fs.readFileSync(testDestPath, 'utf8'));
        assert.strictEqual(cachedContent.models['mock-gemini-next'].inputPerMillion, 0.12);
      } finally {
        await new Promise(res => server.close(res));
        if (fs.existsSync(testDestPath)) {
          try { fs.unlinkSync(testDestPath); } catch (_e) {}
        }
      }
    });
  });

  // --- Suite 13: Pricing Table Formatter Tests ---
  await describe('13. Pricing Table Formatter Tests', async () => {
    await test('formatProviderBadge should format badges with color tags', () => {
      const googleBadge = priceSyncer.formatProviderBadge('google');
      const anthropicBadge = priceSyncer.formatProviderBadge('anthropic');
      const openaiBadge = priceSyncer.formatProviderBadge('openai');

      assert(googleBadge.includes('Google'));
      assert(anthropicBadge.includes('Anthropic'));
      assert(openaiBadge.includes('OpenAI'));
    });

    await test('formatUnitRate should format currency amounts accurately', () => {
      const usdRate = priceSyncer.formatUnitRate(0.15, 'usd');
      assert.strictEqual(usdRate, '$0.1500');

      const krwRate = priceSyncer.formatUnitRate(0.15, 'krw');
      assert(krwRate.includes('217.5'));

      const jpyRate = priceSyncer.formatUnitRate(0.15, 'jpy');
      assert(jpyRate.includes('23.25'));

      const eurRate = priceSyncer.formatUnitRate(1.0, 'eur');
      assert(eurRate.includes('0.9500€'));
    });

    await test('formatPricingTable should render a full ASCII/ANSI table without error', () => {
      const tableEn = priceSyncer.formatPricingTable('usd', 'en');
      assert(tableEn.includes('Official API Pricing Catalog'));
      assert(tableEn.includes('Gemini 3.7 Flash'));
      assert(tableEn.includes('Claude 3.7 Sonnet'));
      assert(tableEn.includes('GPT-4o'));
      assert(tableEn.includes('o3-mini'));
      assert(tableEn.includes('o1'));
      assert(tableEn.includes('Official Sources:'));
    });

    await test('formatPricingTable should render properly in Korean, Japanese, and Chinese', () => {
      const tableKo = priceSyncer.formatPricingTable('krw', 'ko');
      assert(tableKo.includes('공식 API 가격 카탈로그'));
      assert(tableKo.includes('제공사'));
      assert(tableKo.includes('모델명'));

      const tableJa = priceSyncer.formatPricingTable('jpy', 'ja');
      assert(tableJa.includes('公式API価格カタログ'));
      assert(tableJa.includes('プロバイダー'));

      const tableZh = priceSyncer.formatPricingTable('usd', 'zh');
      assert(tableZh.includes('官方 API 定价目录'));
      assert(tableZh.includes('供应商'));
    });
  });

  // --- Suite 14: Subcommand & Live Dispatcher Unit Tests ---
  await describe('14. Subcommand & Live Dispatcher Unit Tests', async () => {
    await test('parseArgs should handle prices, sync-prices, and options', () => {
      const optsPrices = parseArgs(['node', 'bin/agy-tokens.js', '--prices', '--currency', 'krw']);
      assert.strictEqual(optsPrices.prices, true);
      assert.strictEqual(optsPrices.currency, 'krw');

      const optsSync = parseArgs(['node', 'bin/agy-tokens.js', 'sync-prices']);
      assert.strictEqual(optsSync.sync, true);

      const optsAuto = parseArgs(['node', 'bin/agy-tokens.js', '--auto-sync']);
      assert.strictEqual(optsAuto.autoSync, true);
    });

    await test('renderHelp should include pricing and sync commands', () => {
      const help = formatter.renderHelp();
      assert(help.includes('--prices'));
      assert(help.includes('--sync'));
      assert(help.includes('--auto-sync'));
    });
  });

  // --- Suite 15: HTML Dashboard Report Unit Tests ---
  await describe('15. HTML Dashboard Report Unit Tests', async () => {
    const htmlReport = require('../src/html-report');

    // Sandbox (root cause of the v3.3 blank dashboard): artifact-writing tests
    // previously wrote test payloads into the REAL ~/.gemini/antigravity-dashboard/
    // directory, clobbering the user's live dashboard with an empty embedded
    // payload. Re-point the module's artifact paths at a temp dir for the whole
    // suite; the production dir is restored at the end of this suite.
    const testDashDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agy-dash-test-'));
    const prodDashDir = htmlReport.DASHBOARD_DIR;
    htmlReport._setDashboardDirForTests(testDashDir);

    await test('buildDashboardPayload should produce the DashboardPayload schema', () => {
      const sessions = [
        {
          sessionId: 's1',
          startTime: new Date().toISOString(),
          turns: [
            { createdAt: new Date().toISOString(), inputTokens: 100, cachedTokens: 50, outputTokens: 20, costUsd: 0.01 }
          ]
        }
      ];
      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en' });

      assert.strictEqual(payload.version, 3);
      assert(typeof payload.generatedAt === 'string');
      assert.strictEqual(payload.currency, 'usd');
      assert.strictEqual(payload.lang, 'en');
      assert.strictEqual(payload.isRtl, false);
      assert(typeof payload.isFree === 'boolean');
      assert(payload.summaries && payload.summaries.today && payload.summaries.yesterday);
      assert(payload.summaries.last7d && payload.summaries.last30d);
      assert.strictEqual(payload.summaries.today.totalTokens, 170);
      assert(Array.isArray(payload.daily) && payload.daily.length === 30);
      const row = payload.daily[payload.daily.length - 1];
      assert.strictEqual(row.date, aggregator.formatLocalDate(new Date()));
      assert.strictEqual(row.totalTokens, 170);
      assert(payload.dailyModels && typeof payload.dailyModels === 'object');
      assert(payload.cacheStats && payload.cacheStats.totalSessions === 1);
    });

    await test('renderDashboardHtml should embed payload, polling script, and SSE upgrade', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      assert(html.includes('<!DOCTYPE html>'));
      assert(html.includes('window.__AGY_DASH__'));
      assert(html.includes('dashboard-data.js?v='));
      assert(html.includes('EventSource'));
      assert(html.includes('http://127.0.0.1:8787/events'));
      assert(html.includes('<svg'));
      assert(html.includes('setInterval'));
      // C3: no fetch() usage in the client script
      assert(!html.includes('fetch('));
    });

    await test('writeDashboardFiles should atomically write all 3 artifacts (force mode)', () => {
      htmlReport.resetDashboardWriteState();
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const res = htmlReport.writeDashboardFiles(payload, { force: true });

      assert.strictEqual(res.html, true);
      assert.strictEqual(res.dataJs, true);
      assert.strictEqual(res.dataJson, true);
      assert(fs.existsSync(htmlReport.DASHBOARD_HTML_FILE));
      assert(fs.existsSync(htmlReport.DASHBOARD_DATA_JS));
      assert(fs.existsSync(htmlReport.DASHBOARD_DATA_JSON));

      const dataJs = fs.readFileSync(htmlReport.DASHBOARD_DATA_JS, 'utf8');
      assert(dataJs.startsWith('window.__AGY_DASH__'));
      const dataJson = JSON.parse(fs.readFileSync(htmlReport.DASHBOARD_DATA_JSON, 'utf8'));
      assert.strictEqual(dataJson.version, 3);
    });

    await test('writeDashboardFiles should throttle unchanged payloads (skip)', () => {
      htmlReport.resetDashboardWriteState();
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      htmlReport.writeDashboardFiles(payload, { force: true });
      const res = htmlReport.writeDashboardFiles(payload, {});
      assert.strictEqual(res.skipped, true);
    });

    await test('ensureDashboardHtml should self-heal missing HTML only', () => {
      htmlReport.resetDashboardWriteState();
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      // Ensure present, then delete to simulate E13
      htmlReport.writeDashboardFiles(payload, { force: true });
      fs.unlinkSync(htmlReport.DASHBOARD_HTML_FILE);
      const healed = htmlReport.ensureDashboardHtml(payload, {});
      assert.strictEqual(healed, true);
      assert(fs.existsSync(htmlReport.DASHBOARD_HTML_FILE));
      const again = htmlReport.ensureDashboardHtml(payload, {});
      assert.strictEqual(again, false);
    });

    await test('buildDashboardPayload should emit per-model rows costed with each session model (W4)', () => {
      const now = new Date().toISOString();
      const sessions = [
        {
          sessionId: 'm1',
          modelName: 'gemini-3-pro',
          startTime: now,
          inputTokens: 1000,
          cachedTokens: 500,
          outputTokens: 200,
          turns: [
            { createdAt: now, inputTokens: 1000, cachedTokens: 500, outputTokens: 200, costUsd: 0.01 }
          ]
        },
        {
          sessionId: 'm2',
          modelName: 'gemini-3-flash',
          startTime: now,
          inputTokens: 300,
          cachedTokens: 100,
          outputTokens: 50,
          turns: [
            { createdAt: now, inputTokens: 300, cachedTokens: 100, outputTokens: 50, costUsd: 0.001 }
          ]
        }
      ];
      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en' });

      assert(Array.isArray(payload.models), 'payload.models must be an array');
      assert.strictEqual(payload.models.length, 2);
      const byModel = {};
      for (const m of payload.models) byModel[m.model] = m;

      const pro = byModel['gemini-3-pro'];
      assert(pro, 'gemini-3-pro row missing');
      assert.strictEqual(pro.totalTokens, 1700);
      assert.strictEqual(pro.inputTokens, 1000);
      assert.strictEqual(pro.cachedTokens, 500);
      assert.strictEqual(pro.outputTokens, 200);
      assert.strictEqual(pro.sessions, 1);
      assert.strictEqual(pro.turns, 1);
      assert(typeof pro.costUsd === 'number' && pro.costUsd > 0, 'per-model costUsd must be > 0');
      assert(typeof pro.cacheSavingsUsd === 'number' && pro.cacheSavingsUsd >= 0);
      assert(pro.cacheHitRate > 0 && pro.cacheHitRate <= 100);
      assert(typeof pro.displayName === 'string' && pro.displayName.length > 0);

      const flash = byModel['gemini-3-flash'];
      assert(flash, 'gemini-3-flash row missing');
      assert.strictEqual(flash.totalTokens, 450);
      assert(typeof flash.costUsd === 'number' && flash.costUsd > 0);
      // Per-session model costing: flash pricing differs from pro pricing
      assert(flash.costUsd !== pro.costUsd, 'models must be costed with their own pricing');

      // Sorted by cost desc
      assert(payload.models[0].costUsd >= payload.models[1].costUsd);
    });

    await test('buildDashboardPayload should generate dailyModels map and isRtl flag', () => {
      const today = new Date();
      const yesterday = new Date(today);
      yesterday.setDate(yesterday.getDate() - 1);

      const todayStr = aggregator.formatLocalDate(today);
      const yesterdayStr = aggregator.formatLocalDate(yesterday);

      const sessions = [
        {
          sessionId: 's1',
          modelName: 'gemini-3-pro',
          startTime: today.toISOString(),
          turns: [
            { createdAt: today.toISOString(), inputTokens: 500, cachedTokens: 200, outputTokens: 100 }
          ]
        },
        {
          sessionId: 's2',
          modelName: 'gemini-3-flash',
          startTime: yesterday.toISOString(),
          turns: [
            { createdAt: yesterday.toISOString(), inputTokens: 300, cachedTokens: 100, outputTokens: 50 }
          ]
        }
      ];

      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'ar' });
      assert.strictEqual(payload.isRtl, true);
      assert.strictEqual(payload.version, 3);
      assert(payload.dailyModels && typeof payload.dailyModels === 'object');
      assert(payload.dailyModels[todayStr]);
      assert(payload.dailyModels[yesterdayStr]);

      const todayPro = payload.dailyModels[todayStr]['gemini-3-pro'];
      assert(todayPro, 'today gemini-3-pro missing');
      assert.strictEqual(todayPro.model, 'gemini-3-pro');
      assert.strictEqual(todayPro.inputTokens, 500);
      assert.strictEqual(todayPro.cachedTokens, 200);
      assert.strictEqual(todayPro.outputTokens, 100);
      assert.strictEqual(todayPro.totalTokens, 800);
      assert.strictEqual(todayPro.sessions, 1);
      assert.strictEqual(todayPro.turns, 1);
      assert(typeof todayPro.costUsd === 'number' && todayPro.costUsd > 0);
      assert(typeof todayPro.cacheSavingsUsd === 'number' && todayPro.cacheSavingsUsd > 0);

      const yestFlash = payload.dailyModels[yesterdayStr]['gemini-3-flash'];
      assert(yestFlash, 'yesterday gemini-3-flash missing');
      assert.strictEqual(yestFlash.model, 'gemini-3-flash');
      assert.strictEqual(yestFlash.totalTokens, 450);
      assert.strictEqual(yestFlash.sessions, 1);
      assert.strictEqual(yestFlash.turns, 1);

      const payloadEn = htmlReport.buildDashboardPayload([], { lang: 'en' });
      assert.strictEqual(payloadEn.isRtl, false);
    });

    await test('renderDashboardHtml should include the Models section with share bars', () => {
      const now = new Date().toISOString();
      const sessions = [
        {
          sessionId: 'm1',
          modelName: 'gemini-3-pro',
          startTime: now,
          turns: [{ createdAt: now, inputTokens: 100, cachedTokens: 50, outputTokens: 20 }]
        }
      ];
      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      assert(html.includes('modelsWrap'), 'Models section container missing');
      assert(html.includes('renderModels'), 'renderModels function missing');
      assert(html.includes('share-bar'), 'share bar CSS missing');
      assert(html.includes('modelsTitle'), 'modelsTitle i18n missing');
    });

    await test('buildDashboardPayload should include full i18n object for each supported locale', () => {
      const keys = [
        'dashboardTitle', 'summaryToday', 'summaryYesterday', 'summary7d', 'summary30d',
        'chartTitle', 'tableTitle', 'modelsTitle', 'modelColumn', 'lastUpdated',
        'noDataFound', 'colDate', 'colTokens', 'colCost', 'colSavings',
        'colCache', 'colTotal', 'colSessions', 'colTurns', 'colModel'
      ];

      const payloadEn = htmlReport.buildDashboardPayload([], { lang: 'en' });
      assert.strictEqual(payloadEn.lang, 'en');
      assert(payloadEn.i18n && typeof payloadEn.i18n === 'object');
      for (const k of keys) {
        assert(payloadEn.i18n[k], `payloadEn.i18n.${k} should exist`);
      }
      assert.strictEqual(payloadEn.i18n.modelsTitle, 'Model Usage & Cost');

      const payloadKo = htmlReport.buildDashboardPayload([], { lang: 'ko' });
      assert.strictEqual(payloadKo.lang, 'ko');
      for (const k of keys) {
        assert(payloadKo.i18n[k], `payloadKo.i18n.${k} should exist`);
      }
      assert.strictEqual(payloadKo.i18n.modelsTitle, '모델별 사용량 & 비용');
      assert.strictEqual(payloadKo.i18n.dashboardTitle, 'Antigravity 토큰 대시보드');

      const payloadJa = htmlReport.buildDashboardPayload([], { lang: 'ja' });
      assert.strictEqual(payloadJa.lang, 'ja');
      for (const k of keys) {
        assert(payloadJa.i18n[k], `payloadJa.i18n.${k} should exist`);
      }
      assert.strictEqual(payloadJa.i18n.modelsTitle, 'モデル別使用量 & コスト');

      const payloadZh = htmlReport.buildDashboardPayload([], { lang: 'zh' });
      assert.strictEqual(payloadZh.lang, 'zh');
      for (const k of keys) {
        assert(payloadZh.i18n[k], `payloadZh.i18n.${k} should exist`);
      }
      assert.strictEqual(payloadZh.i18n.modelsTitle, '模型使用量 & 成本');
    });

    await test('writeDashboardFiles should force regeneration on locale change', () => {
      htmlReport.resetDashboardWriteState();
      const payloadEn = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      htmlReport.writeDashboardFiles(payloadEn, { force: true });

      // Check initial write is English
      const initialHtml = fs.readFileSync(htmlReport.DASHBOARD_HTML_FILE, 'utf8');
      assert(initialHtml.includes('lang="en"'));
      assert(initialHtml.includes('Antigravity Token Dashboard'));

      // Second write with different locale (Korean) without force: should trigger write due to locale mismatch
      const payloadKo = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'ko' });
      const res = htmlReport.writeDashboardFiles(payloadKo, {});

      assert.strictEqual(res.html, true, 'HTML should be regenerated on locale change');
      assert.strictEqual(res.dataJs, true, 'dataJs should be written on locale change');
      assert.strictEqual(res.dataJson, true, 'dataJson should be written on locale change');
      assert.strictEqual(res.skipped, false, 'Should not be skipped when locale changes');

      const koHtml = fs.readFileSync(htmlReport.DASHBOARD_HTML_FILE, 'utf8');
      assert(koHtml.includes('lang="ko"'));
      assert(koHtml.includes('Antigravity 토큰 대시보드'));

      const koJson = JSON.parse(fs.readFileSync(htmlReport.DASHBOARD_DATA_JSON, 'utf8'));
      assert.strictEqual(koJson.lang, 'ko');
      assert.strictEqual(koJson.i18n.modelsTitle, '모델별 사용량 & 비용');

      // Third write with same locale should be skipped
      const res2 = htmlReport.writeDashboardFiles(payloadKo, {});
      assert.strictEqual(res2.skipped, true, 'Subsequent write with same locale should be skipped');
    });

    await test('writeDashboardFiles should rewrite HTML when embedded payload is stale (E13b)', () => {
      htmlReport.resetDashboardWriteState();
      // 1. Write a FRESH payload with data (force) so HTML + data files exist.
      const freshSessions = [
        {
          sessionId: 'e13b',
          modelName: 'gemini-3-pro',
          startTime: new Date().toISOString(),
          turns: [
            { createdAt: new Date().toISOString(), inputTokens: 100, cachedTokens: 50, outputTokens: 20 }
          ]
        }
      ];
      const freshPayload = htmlReport.buildDashboardPayload(freshSessions, { currency: 'usd', lang: 'en' });
      const write1 = htmlReport.writeDashboardFiles(freshPayload, { force: true });
      assert.strictEqual(write1.html, true);
      assert.strictEqual(write1.dataJs, true);

      // 2. Corrupt the embedded payload in the HTML to simulate the stale-empty
      //    artifact left by an older writer (models: 0, all-zero data).
      const htmlPath = htmlReport.DASHBOARD_HTML_FILE;
      const goodHtml = fs.readFileSync(htmlPath, 'utf8');
      const stalePayload = { ...freshPayload, models: [], generatedAt: '2000-01-01T00:00:00.000Z' };
      const staleHtml = goodHtml.replace(
        /<script>window\.__AGY_DASH__ = [\s\S]*?<\/script>/,
        `<script>window.__AGY_DASH__ = ${JSON.stringify(stalePayload)};</script>`
      );
      assert.notStrictEqual(staleHtml, goodHtml, 'embedded payload must have been replaced');
      fs.writeFileSync(htmlPath, staleHtml, 'utf8');

      // 3. Re-write the SAME fresh payload without force: data files are
      //    unchanged (hash matches), but the embedded payload is stale-empty ->
      //    the HTML must be rewritten (self-heal), data files untouched.
      const res = htmlReport.writeDashboardFiles(freshPayload, {});
      assert.strictEqual(res.dataJs, false, 'unchanged data files must not be rewritten');
      assert.strictEqual(res.dataJson, false, 'unchanged data files must not be rewritten');
      assert.strictEqual(res.html, true, 'stale embedded payload must trigger HTML rewrite');

      // 4. The rewritten HTML embeds the fresh payload again (models > 0).
      const healedHtml = fs.readFileSync(htmlPath, 'utf8');
      const marker = 'window.__AGY_DASH__ = ';
      const start = healedHtml.indexOf(marker);
      const end = healedHtml.indexOf(';</script>', start + marker.length);
      const embedded = JSON.parse(healedHtml.slice(start + marker.length, end));
      assert.strictEqual(embedded.models.length, 1, 'healed HTML must embed the fresh payload');

      // 5. Identical embedded payload -> skip behavior preserved (no rewrite).
      const res2 = htmlReport.writeDashboardFiles(freshPayload, {});
      assert.strictEqual(res2.skipped, true, 'identical embedded payload must keep skip semantics');
    });

    await test('renderDashboardHtml should include updateI18N function and lang attribute', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'ko' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      assert(html.includes('<html lang="ko">'));
      assert(html.includes('updateI18N('));
      assert(html.includes('currentLang'));
      assert(html.includes('id="dashTitle"'));
      assert(html.includes('id="chartTitle"'));
      assert(html.includes('id="modelsTitle"'));
      assert(html.includes('id="tableTitle"'));
    });

    await test('renderDashboardHtml should include Filter UI (CSS, HTML, and client-side JS)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // CSS classes
      assert(html.includes('.filters{'), 'Filter container CSS missing');
      assert(html.includes('.filter-group{'), 'Filter group CSS missing');
      assert(html.includes('.filter-btn{'), 'Filter button CSS missing');
      assert(html.includes('.filter-check{'), 'Filter checkbox CSS missing');
      assert(html.includes('[dir=rtl] .filter-group{flex-direction:row-reverse}'), 'RTL filter CSS missing');

      // HTML elements
      assert(html.includes('id="filters"'), 'Filter section container missing');
      assert(html.includes('id="filterDateLabel"'), 'filterDateLabel missing');
      assert(html.includes('data-range="30d"'), '30d range button missing');
      assert(html.includes('data-range="7d"'), '7d range button missing');
      assert(html.includes('data-range="today"'), 'today range button missing');
      assert(html.includes('data-range="yesterday"'), 'yesterday range button missing');
      assert(html.includes('data-range="custom"'), 'custom range button missing');
      assert(html.includes('id="customDateRange"'), 'customDateRange container missing');
      assert(html.includes('id="filterFrom"'), 'filterFrom input missing');
      assert(html.includes('id="filterTo"'), 'filterTo input missing');
      assert(html.includes('id="modelFilters"'), 'modelFilters container missing');
      assert(html.includes('id="filterModelLabel"'), 'filterModelLabel missing');

      // JavaScript filter logic
      assert(html.includes('filterState'), 'filterState missing in client JS');
      assert(html.includes('initFilters('), 'initFilters missing in client JS');
      assert(html.includes('getFilteredData('), 'getFilteredData missing in client JS');
      assert(html.includes('applyFilters('), 'applyFilters missing in client JS');
      assert(html.includes('bindDateFilterEvents('), 'bindDateFilterEvents missing in client JS');
      assert(html.includes('bindModelCheckboxEvents('), 'bindModelCheckboxEvents missing in client JS');

      // RTL rendered attribute
      const payloadAr = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'ar' });
      const htmlAr = htmlReport.renderDashboardHtml(payloadAr, { refreshSec: 5, servePort: 8787 });
      assert(htmlAr.includes('dir="rtl"'), 'RTL dir attribute missing on ar locale HTML tag');
    });

    await test('renderDashboardHtml should use v3.1 layout order: chart panel before cards before filters', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      const chartPos = html.indexOf('id="chartTitle"');
      const cardsPos = html.indexOf('id="cards"');
      const filtersPos = html.indexOf('id="filters"');
      const modelsPos = html.indexOf('id="modelsTitle"');
      const tablePos = html.indexOf('id="tableTitle"');
      const emptyPos = html.indexOf('id="empty"');

      assert(chartPos >= 0, 'chartTitle section missing');
      assert(cardsPos >= 0, 'cards section missing');
      assert(filtersPos >= 0, 'filters section missing');
      assert(modelsPos >= 0, 'modelsTitle missing');
      assert(tablePos >= 0, 'tableTitle missing');
      assert(emptyPos >= 0, 'empty div missing');

      assert(chartPos < cardsPos, 'chart section must come before cards section');
      assert(cardsPos < filtersPos, 'cards section must come before filters section');
      assert(filtersPos < modelsPos, 'filters section must come before models panel');
      assert(modelsPos < tablePos, 'models section must come before daily table section');
      assert(tablePos < emptyPos, 'daily table section must come before empty div');

      // Chart legend container present under the chart
      assert(html.includes('id="chartLegend"'), 'chartLegend container missing');
      assert(html.includes('chart-legend'), 'chart legend CSS missing');

      // Date filter group must appear before model filter group
      const dateGroupPos = html.indexOf('id="filterDateLabel"');
      const modelGroupPos = html.indexOf('id="modelFilters"');
      assert(dateGroupPos >= 0 && modelGroupPos > dateGroupPos, 'date filter group must precede model filter group');
    });

    await test('renderDashboardHtml should order date filter buttons today/yesterday/7d/30d/custom with today default active (REQ-241)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      const order = ['today', 'yesterday', '7d', '30d', 'custom'];
      let prevPos = -1;
      for (const r of order) {
        const pos = html.indexOf(`data-range="${r}"`);
        assert(pos >= 0, `data-range="${r}" button missing`);
        assert(pos > prevPos, `data-range="${r}" must appear after previous button (REQ-234 order)`);
        prevPos = pos;
      }

      // Default active is today (REQ-241); 30d must NOT be active
      assert(/data-range="today"[^>]*class="filter-btn active"/.test(html) ||
             /class="filter-btn active"[^>]*data-range="today"/.test(html),
        'today button must be the default active button');
      assert(!(/data-range="30d"[^>]*class="filter-btn active"/.test(html) ||
               /class="filter-btn active"[^>]*data-range="30d"/.test(html)),
        '30d button must not be active by default');

      // filterState default range is today
      assert(html.includes("range: 'today'"), "filterState default range must be 'today'");
    });

    await test('renderDashboardHtml should guard against stale (pre-v3) SSE/poll payloads (REQ-244)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // Version guard helper exists and checks version + dailyModels
      assert(html.includes('function isFreshPayload('), 'isFreshPayload guard missing');
      assert(/isFreshPayload\(p\)\s*\{\s*return/.test(html), 'isFreshPayload must be a predicate');
      assert(html.includes('p.version >= 3'), 'version check missing in guard');
      assert(html.includes('p.dailyModels'), 'dailyModels check missing in guard');

      // SSE handler ignores stale payloads before render/overwrite
      const sseFn = html.match(/es\.onmessage = function \(ev\) \{[\s\S]*?\n      \};/);
      assert(sseFn, 'SSE onmessage handler not found');
      assert(sseFn[0].includes('isFreshPayload(p)'), 'SSE handler must check isFreshPayload');
      assert(sseFn[0].indexOf('isFreshPayload(p)') < sseFn[0].indexOf('render(p)'),
        'stale check must run before render(p)');

      // Polling onload ignores stale payloads before render
      const pollFn = html.match(/sc\.onload = function \(\) \{[\s\S]*?\n    \};/);
      assert(pollFn, 'pollOnce onload handler not found');
      assert(pollFn[0].includes('isFreshPayload(window.__AGY_DASH__)'),
        'polling onload must check isFreshPayload before render');
    });

    await test('renderSvg should fall back to single-series bars when dailyModels is missing (REQ-244)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // Fallback path: single-series bar from d.totalTokens with default accent
      assert(html.includes('st.fallback'), 'renderSvg fallback flag missing');
      assert(html.includes('daily[i].totalTokens'), 'renderSvg must fall back to d.totalTokens');
      assert(html.includes("class=\"bar\""), 'fallback bar must use default accent .bar class');
      // Baseline-only path still exists for genuinely empty days
      assert(html.includes('height="1" class="bar"'), 'baseline rect for empty days missing');
    });

    await test('getFilteredData should degrade gracefully when dailyModels is missing (REQ-244)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // hasDailyModels detection + fallback to p.models and p.daily rows as-is
      assert(html.includes('hasDailyModels'), 'dailyModels presence detection missing');
      assert(html.includes('filteredDaily.push(dd)'), 'daily fallback to p.daily rows missing');
      assert(html.includes('srcModels[fmi].model'), 'models fallback to p.models missing');
    });

    await test('renderTable should render per-model sub-rows from dailyModels (REQ-243)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // renderTable consumes dailyModels and renders sub-rows
      assert(/function renderTable\(daily, dailyModels\)/.test(html),
        'renderTable must accept (daily, dailyModels)');
      assert(html.includes('class="subrow"'), 'subrow row class missing');
      assert(html.includes('\\u21b3'), 'subrow model-name arrow prefix missing');
      assert(html.includes('subList.sort'), 'sub-rows must be sorted by cost desc');
      assert(html.includes('filterState.models.has(mn)'), 'sub-rows must respect the model filter');

      // Sub-row CSS with RTL support
      assert(html.includes('.subrow td{color:var(--dim);font-size:11px}'), 'subrow CSS missing');
      assert(html.includes('[dir=rtl] .subrow td:first-child{padding-left:0;padding-right:20px}'),
        'RTL subrow padding missing');
    });

    await test('renderDashboardHtml should include stacked per-model chart JS with legend and custom 5th card logic', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // Stacked chart: renderSvg consumes dailyModels + per-model color palette
      assert(/function renderSvg\(daily, dailyModels\)/.test(html), 'renderSvg must accept (daily, dailyModels)');
      assert(html.includes('dailyModels['), 'renderSvg must index dailyModels per date');
      assert(html.includes('MODEL_COLORS'), 'per-model color palette missing');
      assert(html.includes('modelColor('), 'modelColor helper missing');
      assert(html.includes("function renderChart("), 'renderChart wrapper missing');
      assert(html.includes("getElementById('chartLegend')"), 'legend rendering missing in client JS');
      assert(html.includes('legend-swatch'), 'legend swatch CSS/JS missing');

      // Chart re-renders on model filter change (renderChart called in model checkbox handler)
      assert(/renderChart\(lastPayload\);\s*\n\s*applyFilters\(\);/.test(html),
        'model filter change must re-render chart via renderChart');

      // Summary cards: always 4 full-data cards from p.summaries
      assert(html.includes("cardHtml(I18N.summaryToday, s.today)"), 'today card missing in render()');
      assert(html.includes("cardHtml(I18N.summaryYesterday, s.yesterday)"), 'yesterday card missing in render()');
      assert(html.includes("cardHtml(I18N.summary7d, s.last7d)"), '7d card missing in render()');
      assert(html.includes("cardHtml(I18N.summary30d, s.last30d)"), '30d card missing in render()');

      // Custom 5th card: appended only when range is custom with from/to set
      assert(html.includes("filterState.range === 'custom' && filterState.from && filterState.to"),
        'custom 5th card condition missing');
      assert(html.includes("cardHtml(I18N.filterCustom || 'Custom', filtered.summary)"),
        'custom 5th card must use filtered.summary');

      // applyFilters must NOT touch the chart (chart is date-filter independent)
      const applyFn = html.match(/function applyFilters\(\) \{[\s\S]*?\n  \}/);
      assert(applyFn, 'applyFilters function not found');
      assert(!applyFn[0].includes("getElementById('chart')"),
        'applyFilters must not re-render the chart (chart ignores date filter)');

      // applyFilters must always render the 4 fixed cards from p.summaries
      assert(applyFn[0].includes('lastPayload.summaries'), 'applyFilters must render cards from full-data summaries');
    });

    await test('renderDashboardHtml should include estimate panel markup, CSS, and client JS (REQ-250..253)', () => {
      const payload = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'en' });
      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });

      // Panel + disclaimer markup (header note + panel footer)
      assert(html.includes('id="estimateNote"'), 'header estimateNote span missing');
      assert(html.includes('id="estimatePanel"'), 'estimatePanel section missing');
      assert(html.includes('id="estimateTitle"'), 'estimateTitle h2 missing');
      assert(html.includes('id="estimatePanelNote"'), 'estimatePanelNote footer missing');
      assert(html.includes('class="estimate-note"'), 'estimate-note class missing on disclaimer nodes');

      // 2 metric items: label/value/cost node ids
      const estIds = [
        'estAvgLabel', 'estAvgValue', 'estAvgCost',
        'estMonthEndLabel', 'estMonthEndValue', 'estMonthEndCost'
      ];
      for (const id of estIds) {
        assert(html.includes(`id="${id}"`), `estimate node id="${id}" missing`);
      }

      // CSS: note, panel, grid, layout wrapper + 2-col media query
      assert(html.includes('.estimate-note{'), 'estimate-note CSS missing');
      assert(html.includes('.estimate-panel{'), 'estimate-panel CSS missing');
      assert(html.includes('.est-grid{'), 'est-grid CSS missing');
      assert(html.includes('.est-item{'), 'est-item CSS missing');
      assert(html.includes('.est-layout{'), 'est-layout CSS missing');
      assert(html.includes('@media(min-width:1200px){.est-layout{grid-template-columns:1.6fr 1fr}}'),
        '2-col est-layout media query missing');
      // RTL-safe: grid layout uses direction-agnostic columns (no [dir=rtl] mirror needed)
      assert(html.includes('est-layout'), 'est-layout wrapper must exist for RTL-safe grid');

      // Client JS: computation + render functions wired into updateI18N and render()
      assert(html.includes('function computeEstimates('), 'computeEstimates missing in client JS');
      assert(html.includes('function renderEstimates('), 'renderEstimates missing in client JS');
      assert(html.includes('renderEstimates(lastPayload)'), 'updateI18N must re-render estimates on locale change');
      assert(/render\(p\) \{[\s\S]*?renderEstimates\(p\);/.test(html), 'render() must call renderEstimates(p)');

      // Computation guards: month-prefix match, 7d slice, divide-by-zero guard
      assert(html.includes("indexOf(monthPrefix) === 0"), 'MTD month-prefix match missing');
      assert(html.includes('rows.slice(-7)'), '7d window slice missing');
      assert(html.includes('rows.length > 0 ?'), 'divide-by-zero guard missing');
      assert(html.includes('new Date(y, m + 1, 0).getDate()'), 'days-in-month computation missing');

      // est-layout wrapper wraps cards + estimate panel (panel after cards)
      const cardsPos = html.indexOf('id="cards"');
      const panelPos = html.indexOf('id="estimatePanel"');
      const layoutPos = html.indexOf('class="est-layout"');
      assert(layoutPos >= 0 && layoutPos < cardsPos && cardsPos < panelPos,
        'est-layout must wrap #cards then #estimatePanel');
    });

    await test('renderDashboardHtml should render disclaimer text and activeModel label logic (REQ-250, 258)', () => {
      const payloadKo = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'ko', model: 'Gemini 3.7 Flash (High)' });
      const htmlKo = htmlReport.renderDashboardHtml(payloadKo, { refreshSec: 5, servePort: 8787 });

      // Korean disclaimer text present in both header note and panel footer
      assert(htmlKo.includes('이 수치들은 장기 사용 관리를 위한 추정치입니다'),
        'Korean estimateDisclaimer text missing in rendered HTML');
      assert(htmlKo.includes('장기 사용량 추정'), 'Korean estimatePanelTitle missing');

      // activeModel label logic in updateI18N + render()
      assert(htmlKo.includes("I18N.activeModel + ': ' + (lastPayload.model || '')"),
        'updateI18N must render #model with activeModel prefix');
      assert(htmlKo.includes("(I18N.activeModel ? I18N.activeModel + ': ' : '') + (p.model || '')"),
        'render() must render #model with activeModel prefix');
      assert(htmlKo.includes("document.getElementById('model')"), '#model span render missing');

      // updateI18N covers all new node ids (REQ-259)
      for (const nid of ['estimateNote', 'estimateTitle', 'estimatePanelNote', 'estAvgLabel', 'estMonthEndLabel']) {
        assert(htmlKo.includes(`getElementById('${nid}')`), `updateI18N must cover #${nid}`);
      }

      // RTL variant keeps dir attribute + Arabic disclaimer
      const payloadAr = htmlReport.buildDashboardPayload([], { currency: 'usd', lang: 'ar' });
      const htmlAr = htmlReport.renderDashboardHtml(payloadAr, { refreshSec: 5, servePort: 8787 });
      assert(htmlAr.includes('dir="rtl"'), 'RTL dir attribute missing for ar');
      assert(htmlAr.includes('هذه الأرقام تقديرية لإدارة الاستخدام على المدى الطويل'),
        'Arabic estimateDisclaimer text missing');
    });

    await test('buildDashboardPayload should keep effort variants distinct but costed at base-model rates (REQ-255, 256)', () => {
      const now = new Date().toISOString();
      const sessions = [
        {
          sessionId: 'eff-high',
          modelName: 'Gemini 3.7 Flash (High)',
          startTime: now,
          inputTokens: 1000,
          cachedTokens: 500,
          outputTokens: 200,
          turns: [
            { createdAt: now, inputTokens: 1000, cachedTokens: 500, outputTokens: 200 }
          ]
        },
        {
          sessionId: 'eff-low',
          modelName: 'Gemini 3.7 Flash (Low)',
          startTime: now,
          inputTokens: 300,
          cachedTokens: 100,
          outputTokens: 50,
          turns: [
            { createdAt: now, inputTokens: 300, cachedTokens: 100, outputTokens: 50 }
          ]
        }
      ];
      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en' });

      // models[]: 2 distinct effort-variant rows with full display names
      assert(Array.isArray(payload.models) && payload.models.length === 2,
        `expected 2 distinct effort-variant model rows, got ${payload.models.length}`);
      const byModel = {};
      for (const m of payload.models) byModel[m.model] = m;
      const high = byModel['Gemini 3.7 Flash (High)'];
      const low = byModel['Gemini 3.7 Flash (Low)'];
      assert(high, 'effort-variant (High) row missing');
      assert(low, 'effort-variant (Low) row missing');
      assert.strictEqual(high.displayName, 'Gemini 3.7 Flash (High)');
      assert.strictEqual(low.displayName, 'Gemini 3.7 Flash (Low)');

      // dailyModels keys distinct per variant
      const todayStr = aggregator.formatLocalDate(new Date());
      assert(payload.dailyModels[todayStr], 'dailyModels today missing');
      assert(payload.dailyModels[todayStr]['Gemini 3.7 Flash (High)'], 'dailyModels (High) key missing');
      assert(payload.dailyModels[todayStr]['Gemini 3.7 Flash (Low)'], 'dailyModels (Low) key missing');

      // Both variants costed at BASE-model rates (REQ-256): the suffixed name
      // must resolve to the same pricing as the bare base model.
      assert(high.costUsd > 0 && low.costUsd > 0, 'both variants must be costed');
      const baseHigh = config.calculateCostUsd(1000, 500, 200, 'Gemini 3.7 Flash');
      const baseLow = config.calculateCostUsd(300, 100, 50, 'Gemini 3.7 Flash');
      // Payload costs are round6()'d, so compare within half-ULP of 1e-6.
      assert(Math.abs(high.costUsd - baseHigh) < 5e-7,
        `(High) variant must be costed at base-model rates: ${high.costUsd} vs ${baseHigh}`);
      assert(Math.abs(low.costUsd - baseLow) < 5e-7,
        `(Low) variant must be costed at base-model rates: ${low.costUsd} vs ${baseLow}`);
      // dailyModels rows are costed with the suffixed key too — same base rates
      const dmHigh = payload.dailyModels[todayStr]['Gemini 3.7 Flash (High)'];
      const dmLow = payload.dailyModels[todayStr]['Gemini 3.7 Flash (Low)'];
      assert(Math.abs(dmHigh.costUsd - baseHigh) < 5e-7, 'dailyModels (High) must use base-model rates');
      assert(Math.abs(dmLow.costUsd - baseLow) < 5e-7, 'dailyModels (Low) must use base-model rates');
    });

    await test('buildDashboardPayload should aggregate multi-model session at turn granularity (REQ-308)', () => {
      const d1 = new Date('2026-08-27T10:00:00Z');
      const d2 = new Date('2026-08-28T10:00:00Z');
      const date1Str = aggregator.formatLocalDate(d1);
      const date2Str = aggregator.formatLocalDate(d2);

      const sessions = [
        {
          sessionId: 'multi-model-sess-1',
          modelName: 'Claude Opus 4.6 (Thinking)',
          startTime: d1.toISOString(),
          endTime: d2.toISOString(),
          inputTokens: 3000,
          cachedTokens: 1500,
          outputTokens: 600,
          costUsd: 0.02035,
          turns: [
            // Turn 1 (Day 1): Gemini 3.7 Flash (High)
            {
              createdAt: d1.toISOString(),
              modelName: 'Gemini 3.7 Flash (High)',
              inputTokens: 1000,
              cachedTokens: 500,
              outputTokens: 200,
              costUsd: config.calculateCostUsd(1000, 500, 200, 'Gemini 3.7 Flash (High)')
            },
            // Turn 2 (Day 2): Gemini 3.7 Flash (High) - second turn with same model
            {
              createdAt: d2.toISOString(),
              modelName: 'Gemini 3.7 Flash (High)',
              inputTokens: 500,
              cachedTokens: 250,
              outputTokens: 100,
              costUsd: config.calculateCostUsd(500, 250, 100, 'Gemini 3.7 Flash (High)')
            },
            // Turn 3 (Day 2): Claude Opus 4.6 (Thinking)
            {
              createdAt: d2.toISOString(),
              modelName: 'Claude Opus 4.6 (Thinking)',
              inputTokens: 1500,
              cachedTokens: 750,
              outputTokens: 300,
              costUsd: config.calculateCostUsd(1500, 750, 300, 'Claude Opus 4.6 (Thinking)')
            }
          ]
        }
      ];

      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en' });

      // 1. payload.models has 2 distinct entries keyed by model name
      assert(Array.isArray(payload.models) && payload.models.length === 2,
        `expected 2 distinct model entries in models[], got ${payload.models.length}`);
      const byModel = {};
      for (const m of payload.models) byModel[m.model] = m;
      const gemini = byModel['Gemini 3.7 Flash (High)'];
      const claude = byModel['Claude Opus 4.6 (Thinking)'];
      assert(gemini, 'Gemini 3.7 Flash (High) entry missing in models');
      assert(claude, 'Claude Opus 4.6 (Thinking) entry missing in models');

      // 2. Token / turn / session totals for each model
      // Gemini: 2 turns (1000+500=1500 in, 500+250=750 cached, 200+100=300 out, total=2550)
      assert.strictEqual(gemini.turns, 2);
      assert.strictEqual(gemini.inputTokens, 1500);
      assert.strictEqual(gemini.cachedTokens, 750);
      assert.strictEqual(gemini.outputTokens, 300);
      assert.strictEqual(gemini.totalTokens, 2550);
      assert.strictEqual(gemini.sessions, 1, 'Gemini should have sessions === 1 (REQ-306)');

      // Claude: 1 turn (1500 in, 750 cached, 300 out, total=2550)
      assert.strictEqual(claude.turns, 1);
      assert.strictEqual(claude.inputTokens, 1500);
      assert.strictEqual(claude.cachedTokens, 750);
      assert.strictEqual(claude.outputTokens, 300);
      assert.strictEqual(claude.totalTokens, 2550);
      assert.strictEqual(claude.sessions, 1, 'Claude should have sessions === 1 (REQ-306)');

      // 3. Costs
      const expectedGeminiCost = config.calculateCostUsd(1500, 750, 300, 'Gemini 3.7 Flash (High)');
      const expectedClaudeCost = config.calculateCostUsd(1500, 750, 300, 'Claude Opus 4.6 (Thinking)');
      assert(Math.abs(gemini.costUsd - expectedGeminiCost) < 5e-7,
        `Gemini costUsd (${gemini.costUsd}) should match expected (${expectedGeminiCost})`);
      assert(Math.abs(claude.costUsd - expectedClaudeCost) < 5e-7,
        `Claude costUsd (${claude.costUsd}) should match expected (${expectedClaudeCost})`);

      // 4. dailyModels mapping
      assert(payload.dailyModels[date1Str], `dailyModels[${date1Str}] missing`);
      assert(payload.dailyModels[date2Str], `dailyModels[${date2Str}] missing`);

      // Date 1: only Gemini
      const d1Gemini = payload.dailyModels[date1Str]['Gemini 3.7 Flash (High)'];
      assert(d1Gemini, `dailyModels[${date1Str}]['Gemini 3.7 Flash (High)'] missing`);
      assert.strictEqual(d1Gemini.turns, 1);
      assert.strictEqual(d1Gemini.inputTokens, 1000);
      assert.strictEqual(d1Gemini.cachedTokens, 500);
      assert.strictEqual(d1Gemini.outputTokens, 200);
      assert.strictEqual(d1Gemini.sessions, 1);

      // Date 2: both Gemini and Claude
      const d2Gemini = payload.dailyModels[date2Str]['Gemini 3.7 Flash (High)'];
      const d2Claude = payload.dailyModels[date2Str]['Claude Opus 4.6 (Thinking)'];
      assert(d2Gemini, `dailyModels[${date2Str}]['Gemini 3.7 Flash (High)'] missing`);
      assert(d2Claude, `dailyModels[${date2Str}]['Claude Opus 4.6 (Thinking)'] missing`);
      assert.strictEqual(d2Gemini.turns, 1);
      assert.strictEqual(d2Gemini.inputTokens, 500);
      assert.strictEqual(d2Gemini.cachedTokens, 250);
      assert.strictEqual(d2Gemini.outputTokens, 100);
      assert.strictEqual(d2Gemini.sessions, 1);

      assert.strictEqual(d2Claude.turns, 1);
      assert.strictEqual(d2Claude.inputTokens, 1500);
      assert.strictEqual(d2Claude.cachedTokens, 750);
      assert.strictEqual(d2Claude.outputTokens, 300);
      assert.strictEqual(d2Claude.sessions, 1);

      // 5. Total sessions in cacheStats remains 1
      assert.strictEqual(payload.cacheStats.totalSessions, 1, 'Total cacheStats sessions must remain 1');
    });

    // Restore the production dashboard dir and clean up the temp sandbox so
    // other suites (and the user's real dashboard) are unaffected.
    htmlReport._setDashboardDirForTests(prodDashDir);
    htmlReport.resetDashboardWriteState();
    fs.rmSync(testDashDir, { recursive: true, force: true });
  });

  // --- Suite 16: OSC 8 & New CLI Flags Unit Tests ---
  await describe('16. OSC 8 & New CLI Flags Unit Tests', async () => {
    const osc8 = require('../src/osc8');

    await test('formatOsc8Link should wrap label with OSC 8 escape pairs', () => {
      const linked = osc8.formatOsc8Link('file:///C:/test/dashboard.html', 'Dashboard');
      assert(linked.includes('\x1b]8;;file:///C:/test/dashboard.html\x07'));
      assert(linked.includes('Dashboard'));
      assert(linked.endsWith('\x1b]8;;\x07'));
    });

    await test('formatOsc8Link should degrade to plain label when unsupported', () => {
      const prevNoColor = process.env.NO_COLOR;
      process.env.NO_COLOR = '1';
      const plain = osc8.formatOsc8Link('file:///C:/test/dashboard.html', 'Dashboard');
      assert.strictEqual(plain, 'Dashboard');
      if (prevNoColor === undefined) delete process.env.NO_COLOR; else process.env.NO_COLOR = prevNoColor;
    });

    await test('dashboardFileUrl should return a file:// URL of dashboard.html', () => {
      const url = osc8.dashboardFileUrl();
      assert(url.startsWith('file://'));
      assert(url.includes('dashboard.html'));
    });

    await test('parseArgs should parse --html/--dashboard flags', () => {
      const optsHtml = parseArgs(['node', 'bin/agy-tokens.js', '--html']);
      assert.strictEqual(optsHtml.html, true);
      assert.strictEqual(optsHtml.today, false);

      const optsDash = parseArgs(['node', 'bin/agy-tokens.js', '--dashboard']);
      assert.strictEqual(optsDash.html, true);
    });

    await test('parseArgs should parse --serve, --port, --open, --write-dashboard, --no-link, --refresh', () => {
      const optsServe = parseArgs(['node', 'bin/agy-tokens.js', '--serve', '8787']);
      assert.strictEqual(optsServe.serve, true);
      assert.strictEqual(optsServe.servePort, 8787);

      const optsServeEq = parseArgs(['node', 'bin/agy-tokens.js', '--serve=9000']);
      assert.strictEqual(optsServeEq.serve, true);
      assert.strictEqual(optsServeEq.servePort, 9000);

      const optsPort = parseArgs(['node', 'bin/agy-tokens.js', '--serve', '--port', '0']);
      assert.strictEqual(optsPort.serve, true);
      assert.strictEqual(optsPort.servePort, 0);

      const optsOpen = parseArgs(['node', 'bin/agy-tokens.js', '--html', '--open']);
      assert.strictEqual(optsOpen.open, true);

      const optsWrite = parseArgs(['node', 'bin/agy-tokens.js', '--hook', '--raw', '--write-dashboard']);
      assert.strictEqual(optsWrite.hook, true);
      assert.strictEqual(optsWrite.raw, true);
      assert.strictEqual(optsWrite.writeDashboard, true);

      const optsNoLink = parseArgs(['node', 'bin/agy-tokens.js', '--hook', '--raw', '--no-link']);
      assert.strictEqual(optsNoLink.noLink, true);

      const optsRefresh = parseArgs(['node', 'bin/agy-tokens.js', '--html', '--refresh', '10']);
      assert.strictEqual(optsRefresh.refreshSec, 10);
    });

    await test('renderRealTimeBadge should append link segment and stay single-line', () => {
      const badgeData = { turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01, cacheHitRate: 99 };
      const base = formatter.renderRealTimeBadge(badgeData, 'usd', false);
      assert(!base.includes('Dashboard'));

      const linked = formatter.renderRealTimeBadge(badgeData, 'usd', false, '📊 Dashboard');
      assert(linked.includes('📊 Dashboard'));
      assert(!linked.includes('\n'));
    });

    await test('renderHelp should include dashboard flags', () => {
      const help = formatter.renderHelp();
      assert(help.includes('--html'));
      assert(help.includes('--serve'));
      assert(help.includes('--write-dashboard'));
      assert(help.includes('--no-link'));
    });
  });

  // --- Suite 17: Dashboard SSE Server Unit Tests (ephemeral) ---
  await describe('17. Dashboard SSE Server Unit Tests (ephemeral)', async () => {
    const serve = require('../src/serve');

    await test('startDashboardServer should bind 127.0.0.1 and stream SSE events', async () => {
      const info = await serve.startDashboardServer({ port: 0, intervalMs: 200 });

      assert(info.url.startsWith('http://127.0.0.1:'));
      assert(info.port > 0);

      const eventsBody = await new Promise((resolve) => {
        let settled = false;
        const finish = (val) => {
          if (settled) return;
          settled = true;
          resolve(val);
        };
        const req = http.get(`${info.url.replace(/\/$/, '')}/events`, (res) => {
          let body = '';
          res.on('data', (chunk) => {
            body += chunk.toString();
            if (body.includes('data:')) {
              finish(body);
              req.destroy();
            }
          });
          res.on('error', () => finish(body));
        });
        req.on('error', () => finish(''));
        setTimeout(() => finish(''), 35000);
      });

      assert(eventsBody.includes('data:'), 'SSE stream should push data events');

      await serve.stopDashboardServer(info.server);
    });

    await test('GET / should serve dashboard.html with no-store', async () => {
      const info = await serve.startDashboardServer({ port: 0, intervalMs: 60000 });

      const { statusCode, headers, body } = await new Promise((resolve, reject) => {
        http.get(info.url, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve({ statusCode: res.statusCode, headers: res.headers, body }));
        }).on('error', reject);
      });

      assert.strictEqual(statusCode, 200);
      assert.strictEqual(headers['cache-control'], 'no-store');
      assert(body.includes('<!DOCTYPE html>'));

      await serve.stopDashboardServer(info.server);
    });

    await test('GET /data.json should serve valid payload JSON', async () => {
      const info = await serve.startDashboardServer({ port: 0, intervalMs: 60000 });

      const { statusCode, body } = await new Promise((resolve, reject) => {
        http.get(`${info.url.replace(/\/$/, '')}/data.json`, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve({ statusCode: res.statusCode, body }));
        }).on('error', reject);
      });

      assert.strictEqual(statusCode, 200);
      const parsed = JSON.parse(body);
      assert.strictEqual(parsed.version, 3);

      await serve.stopDashboardServer(info.server);
    });
  });

  // --- Suite 18: Dashboard Link Target & Local Server Discovery (VS Code fix) ---
  await describe('18. Dashboard Link Target & Local Server Discovery', async () => {
    const dashboardLink = require('../src/dashboard-link');
    const net = require('net');

    /** Saves and restores TERM_PROGRAM / AGY_TOKENS_LINK_MODE around a test. */
    async function withEnv(env, fn) {
      const savedTermProgram = process.env.TERM_PROGRAM;
      const savedLinkMode = process.env.AGY_TOKENS_LINK_MODE;
      if ('TERM_PROGRAM' in env) process.env.TERM_PROGRAM = env.TERM_PROGRAM;
      else delete process.env.TERM_PROGRAM;
      if ('AGY_TOKENS_LINK_MODE' in env) process.env.AGY_TOKENS_LINK_MODE = env.AGY_TOKENS_LINK_MODE;
      else delete process.env.AGY_TOKENS_LINK_MODE;
      try {
        await fn();
      } finally {
        if (savedTermProgram === undefined) delete process.env.TERM_PROGRAM;
        else process.env.TERM_PROGRAM = savedTermProgram;
        if (savedLinkMode === undefined) delete process.env.AGY_TOKENS_LINK_MODE;
        else process.env.AGY_TOKENS_LINK_MODE = savedLinkMode;
      }
    }

    await test('isVsCodeTerminal should detect TERM_PROGRAM=vscode', async () => {
      await withEnv({ TERM_PROGRAM: 'vscode' }, () => {
        assert.strictEqual(dashboardLink.isVsCodeTerminal(), true);
      });
      await withEnv({ TERM_PROGRAM: undefined }, () => {
        assert.strictEqual(dashboardLink.isVsCodeTerminal(), false);
      });
      await withEnv({ TERM_PROGRAM: 'apple_Terminal' }, () => {
        assert.strictEqual(dashboardLink.isVsCodeTerminal(), false);
      });
    });

    await test('resolveLinkTarget should return http mode under TERM_PROGRAM=vscode', async () => {
      await withEnv({ TERM_PROGRAM: 'vscode' }, () => {
        const target = dashboardLink.resolveLinkTarget();
        assert.strictEqual(target.mode, 'http');
        assert(target.url.startsWith('http://127.0.0.1:'));
        assert(target.url.endsWith('/'));
      });
    });

    await test('resolveLinkTarget should return file mode outside VS Code', async () => {
      await withEnv({ TERM_PROGRAM: undefined }, () => {
        const target = dashboardLink.resolveLinkTarget();
        assert.strictEqual(target.mode, 'file');
        assert(target.url.startsWith('file://'));
        assert(target.url.includes('dashboard.html'));
      });
    });

    await test('AGY_TOKENS_LINK_MODE should force file/http regardless of terminal', async () => {
      await withEnv({ TERM_PROGRAM: 'vscode', AGY_TOKENS_LINK_MODE: 'file' }, () => {
        assert.strictEqual(dashboardLink.resolveLinkTarget().mode, 'file');
      });
      await withEnv({ TERM_PROGRAM: undefined, AGY_TOKENS_LINK_MODE: 'http' }, () => {
        const target = dashboardLink.resolveLinkTarget();
        assert.strictEqual(target.mode, 'http');
        assert(target.url.startsWith('http://127.0.0.1:'));
      });
      await withEnv({ TERM_PROGRAM: undefined, AGY_TOKENS_LINK_MODE: 'bogus' }, () => {
        assert.strictEqual(dashboardLink.resolveLinkTarget().mode, 'file');
      });
    });

    await test('probePort should succeed against a live ephemeral server and fail fast on a closed port', async () => {
      const server = net.createServer(() => {});
      await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
      const livePort = server.address().port;
      assert.strictEqual(await dashboardLink.probePort(livePort), true);
      await new Promise((resolve) => server.close(resolve));

      // Closed port: expect a fast false (well under the 300ms cap on loopback)
      const t0 = Date.now();
      assert.strictEqual(await dashboardLink.probePort(livePort), false);
      assert(Date.now() - t0 < 3000, 'closed-port probe should fail fast');
    });

    await test('port file should be written atomically, read back, and tolerate stale pid', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-portfile-${process.pid}.json`);
      try {
        const written = dashboardLink.writePortFile(8971, 999999, portFile);
        assert.strictEqual(written.port, 8971);
        assert.strictEqual(written.pid, 999999);
        assert(typeof written.startedAt === 'string');

        const readBack = dashboardLink.readPortFile(portFile);
        assert.strictEqual(readBack.port, 8971);
        assert.strictEqual(readBack.pid, 999999); // stale pid tolerated (probe decides liveness)
        assert(!fs.existsSync(`${portFile}.tmp`), 'no tmp file left after atomic rename');

        dashboardLink.removePortFile(portFile);
        assert.strictEqual(dashboardLink.readPortFile(portFile), null);
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });

    await test('readPortFile should reject corrupt JSON and invalid records', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-portfile-corrupt-${process.pid}.json`);
      try {
        fs.writeFileSync(portFile, '{ not json', 'utf8');
        assert.strictEqual(dashboardLink.readPortFile(portFile), null);

        fs.writeFileSync(portFile, JSON.stringify({ hello: 'world' }), 'utf8');
        assert.strictEqual(dashboardLink.readPortFile(portFile), null);

        fs.writeFileSync(portFile, JSON.stringify({ intent: 'spawn', requestedPort: 8787, at: Date.now() }), 'utf8');
        const intent = dashboardLink.readPortFile(portFile);
        assert.strictEqual(intent.intent, 'spawn');
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });

    await test('ensureServerRunning should link to a running server via port file probe', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-ensure-${process.pid}.json`);
      const httpServer = net.createServer(() => {});
      await new Promise((resolve) => httpServer.listen(0, '127.0.0.1', resolve));
      const livePort = httpServer.address().port;
      dashboardLink.writePortFile(livePort, process.pid, portFile);
      try {
        const result = await dashboardLink.ensureServerRunning({ portFile, entryJs: path.join(os.tmpdir(), 'definitely-missing-entry.js') });
        assert(result, 'should return a result when the recorded port is live');
        assert.strictEqual(result.url, `http://127.0.0.1:${livePort}/`);
        assert.strictEqual(result.started, false);
      } finally {
        dashboardLink.removePortFile(portFile);
        await new Promise((resolve) => httpServer.close(resolve));
      }
    });

    await test('ensureServerRunning should honor a fresh spawn intent without spawning', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-intent-${process.pid}.json`);
      try {
        dashboardLink.writeSpawnIntent(8793, portFile);
        const result = await dashboardLink.ensureServerRunning({
          portFile,
          entryJs: path.join(os.tmpdir(), 'definitely-missing-entry.js'),
          port: 8793
        });
        assert(result, 'fresh intent should yield a link target');
        assert.strictEqual(result.url, 'http://127.0.0.1:8793/');
        assert.strictEqual(result.started, false);
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });

    await test('ensureServerRunning should return null when entry is missing and ports are dead', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-null-${process.pid}.json`);
      try {
        // No port file, dead default port, missing entry → fallback signal
        const result = await dashboardLink.ensureServerRunning({
          portFile,
          entryJs: path.join(os.tmpdir(), 'definitely-missing-entry.js'),
          port: 1 // port 1 on loopback is closed in test environments
        });
        assert.strictEqual(result, null);
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });

    await test('removePortFileIfPort should only remove the file when the port matches', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-rmif-${process.pid}.json`);
      try {
        dashboardLink.writePortFile(8975, process.pid, portFile);
        dashboardLink.removePortFileIfPort(9999, portFile); // different port → keep
        assert(dashboardLink.readPortFile(portFile) !== null);
        dashboardLink.removePortFileIfPort(8975, portFile); // matching port → remove
        assert.strictEqual(dashboardLink.readPortFile(portFile), null);
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });
  });

  // --- Suite 19: Model Alias Priority (Part 3) ---
  await describe('Model Alias Priority (Part 3)', async () => {
    await test('getModelPricing("gpt-4o-mini") returns gpt-4o-mini, not gpt-4o', () => {
      const pricing = config.getModelPricing('gpt-4o-mini');
      assert.strictEqual(pricing.id, 'gpt-4o-mini');
    });

    await test('getModelPricing("gpt-4o") returns gpt-4o', () => {
      const pricing = config.getModelPricing('gpt-4o');
      assert.strictEqual(pricing.id, 'gpt-4o');
    });

    await test('getModelPricing("gemini-2.0-flash-lite") returns gemini-2.0-flash-lite', () => {
      const pricing = config.getModelPricing('gemini-2.0-flash-lite');
      assert.strictEqual(pricing.id, 'gemini-2.0-flash-lite');
    });

    await test('getModelPricing("gemini-2.0-flash") returns gemini-2.0-flash', () => {
      const pricing = config.getModelPricing('gemini-2.0-flash');
      assert.strictEqual(pricing.id, 'gemini-2.0-flash');
    });

    await test('getModelPricing("sonnet") still returns claude-3.5-sonnet via exact alias', () => {
      const pricing = config.getModelPricing('sonnet');
      assert.strictEqual(pricing.id, 'claude-3.5-sonnet');
    });
  });

  // --- Suite 20: Statusline Fail-Safe (Part 1) ---
  await describe('Statusline Fail-Safe (Part 1)', async () => {
    await test('readStdinJson resolves without unhandled error after timeout', async () => {
      // readStdinJson with short timeout on TTY should return null
      const result = await hookHandler.readStdinJson(10);
      assert.strictEqual(result, null);
    });

    await test('formatHookResponse returns valid injectSteps structure', () => {
      const resp = hookHandler.formatHookResponse('test badge');
      assert.ok(resp.injectSteps);
      assert.strictEqual(resp.injectSteps.length, 1);
      assert.strictEqual(resp.injectSteps[0].ephemeralMessage, 'test badge');
    });
  });

  // --- Suite 21: Turn-Level Model Attribution (Part 2) ---
  await describe('Turn-Level Model Attribution (Part 2)', async () => {
    await test('summarizeTurns sums per-turn costUsd when available', () => {
      const turns = [
        { inputTokens: 100, cachedTokens: 0, outputTokens: 50, costUsd: 0.001 },
        { inputTokens: 200, cachedTokens: 100, outputTokens: 80, costUsd: 0.003 }
      ];
      const summary = aggregator.summarizeTurns(turns, 'gpt-4o');
      assert.strictEqual(summary.totalTurns, 2);
      assert.ok(Math.abs(summary.costUsd - 0.004) < 0.0001);
    });

    await test('summarizeTurns falls back to single-model calc when costUsd missing', () => {
      const turns = [
        { inputTokens: 1000000, cachedTokens: 0, outputTokens: 0 },
        { inputTokens: 1000000, cachedTokens: 0, outputTokens: 0 }
      ];
      const summary = aggregator.summarizeTurns(turns, 'gpt-4o');
      // gpt-4o input: $2.50/M, 2M tokens = $5.00
      assert.ok(summary.costUsd > 0);
      assert.strictEqual(summary.totalTurns, 2);
    });

    await test('summarizeTurns handles empty turns array', () => {
      const summary = aggregator.summarizeTurns([], 'gpt-4o');
      assert.strictEqual(summary.totalTurns, 0);
      assert.strictEqual(summary.costUsd, 0);
    });

    await test('summarizeTurns handles mixed-model turns correctly', () => {
      const turns = [
        { inputTokens: 100, cachedTokens: 0, outputTokens: 50, costUsd: 0.001, modelName: 'gpt-4o' },
        { inputTokens: 200, cachedTokens: 0, outputTokens: 80, costUsd: 0.0005, modelName: 'gpt-4o-mini' }
      ];
      const summary = aggregator.summarizeTurns(turns);
      assert.ok(Math.abs(summary.costUsd - 0.0015) < 0.0001);
    });

    await test('parseTranscriptFile backtracks initial turns to first fromModel', async () => {
      const backtrackFile = path.join(os.tmpdir(), `backtrack-${Date.now()}.jsonl`);
      const lines = [
        JSON.stringify({
          step_index: 0,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:00:00Z',
          content: 'Initial query before settings change'
        }),
        JSON.stringify({
          step_index: 1,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:00:05Z',
          content: 'Initial model response'
        }),
        JSON.stringify({
          step_index: 2,
          source: 'USER_EXPLICIT',
          type: 'USER_INPUT',
          created_at: '2026-08-27T08:01:00Z',
          content: '<USER_SETTINGS_CHANGE>\nchanged setting `Model Selection` from Claude Opus 4.6 (Thinking) to Gemini 3.7 Flash (High)\n</USER_SETTINGS_CHANGE>'
        }),
        JSON.stringify({
          step_index: 3,
          source: 'MODEL',
          type: 'PLANNER_RESPONSE',
          created_at: '2026-08-27T08:01:05Z',
          content: 'Response with Gemini'
        })
      ];
      fs.writeFileSync(backtrackFile, lines.join('\n'), 'utf8');
      try {
        const parsed = await logParser.parseTranscriptFile(
          backtrackFile,
          'backtrack-session',
          { title: 'Backtrack Test' },
          'gpt-4o'
        );
        assert.strictEqual(parsed.turns.length, 4);
        assert.strictEqual(parsed.turns[0].modelName, 'Claude Opus 4.6 (Thinking)');
        assert.strictEqual(parsed.turns[1].modelName, 'Claude Opus 4.6 (Thinking)');
        assert.strictEqual(parsed.turns[2].modelName, 'Gemini 3.7 Flash (High)');
        assert.strictEqual(parsed.turns[3].modelName, 'Gemini 3.7 Flash (High)');
        assert.deepStrictEqual(parsed.models, ['Claude Opus 4.6 (Thinking)', 'Gemini 3.7 Flash (High)']);
      } finally {
        if (fs.existsSync(backtrackFile)) fs.unlinkSync(backtrackFile);
      }
    });
  });

  // --- Suite 22: Dynamic Y-Axis Chart (Part 4) ---
  await describe('Dynamic Y-Axis Chart (Part 4)', async () => {
    // Test the niceMax algorithm (replicated from client-side code)
    function niceMax(rawMax) {
      if (rawMax <= 0) return 10000;
      var headroom = rawMax * 1.15;
      var mag = Math.pow(10, Math.floor(Math.log10(headroom)));
      var norm = headroom / mag;
      var nice;
      if (norm <= 1) nice = 1;
      else if (norm <= 2) nice = 2;
      else if (norm <= 5) nice = 5;
      else nice = 10;
      return nice * mag;
    }

    function fmtAxis(v) {
      if (v >= 1000000) return (v / 1000000).toFixed(v % 1000000 === 0 ? 0 : 1) + 'M';
      if (v >= 1000) return (v / 1000).toFixed(v % 1000 === 0 ? 0 : 1) + 'K';
      return String(Math.round(v));
    }

    await test('niceMax(0) returns 10000 (default 10K)', () => {
      assert.strictEqual(niceMax(0), 10000);
    });

    await test('niceMax(3200) returns 5000', () => {
      assert.strictEqual(niceMax(3200), 5000);
    });

    await test('niceMax(850000) returns 1000000', () => {
      assert.strictEqual(niceMax(850000), 1000000);
    });

    await test('niceMax(42) returns 50', () => {
      assert.strictEqual(niceMax(42), 50);
    });

    await test('niceMax(100) returns 200', () => {
      assert.strictEqual(niceMax(100), 200);
    });

    await test('fmtAxis(0) returns "0"', () => {
      assert.strictEqual(fmtAxis(0), '0');
    });

    await test('fmtAxis(500) returns "500"', () => {
      assert.strictEqual(fmtAxis(500), '500');
    });

    await test('fmtAxis(5000) returns "5K"', () => {
      assert.strictEqual(fmtAxis(5000), '5K');
    });

    await test('fmtAxis(1500000) returns "1.5M"', () => {
      assert.strictEqual(fmtAxis(1500000), '1.5M');
    });

    await test('fmtAxis(2000000) returns "2M"', () => {
      assert.strictEqual(fmtAxis(2000000), '2M');
    });

    await test('fmtAxis(85000) returns "85K"', () => {
      assert.strictEqual(fmtAxis(85000), '85K');
    });
  });

  // --- Suite 19: Additional Bug Fix Validations ---
  await describe('19. Additional Bug Fix Validations', async () => {
    await test('buildDashboardPayload aggregates turn costs and cache savings accurately without recalculating', () => {
      const { buildDashboardPayload } = require('../src/html-report');
      const sessions = [{
        sessionId: 's1',
        modelName: 'gemini-3.7-flash',
        startTime: new Date().toISOString(),
        turns: [{
          createdAt: new Date().toISOString(),
          inputTokens: 1000,
          cachedTokens: 1000,
          outputTokens: 500,
          costUsd: 0.15,
          cacheSavingsUsd: 0.05
        }]
      }];
      const payload = buildDashboardPayload(sessions);
      const dateKeys = Object.keys(payload.dailyModels);
      const dm = payload.dailyModels[dateKeys[dateKeys.length - 1]]['gemini-3.7-flash'];
      assert.strictEqual(dm.costUsd, 0.15);
      assert.strictEqual(dm.cacheSavingsUsd, 0.05);
    });

    await test('parseTranscriptFile preserves old turn model assignments without settings marker', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'parse-test-'));
      const p = path.join(tempDir, 'transcript.jsonl');
      fs.writeFileSync(p, JSON.stringify({ type: 'USER_INPUT', content: 'test', step_index: 0 }) + '\n');
      
      const oldTurns = [{ stepIndex: 0, modelName: 'claude-3.5-sonnet' }];
      const parsed = await logParser.parseTranscriptFile(p, 's1', {}, 'gemini-3.7-flash', oldTurns);
      assert.strictEqual(parsed.turns[0].modelName, 'claude-3.5-sonnet');
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('EPIPE on process.stdout is handled and will exit cleanly with code 0', () => {
      assert(process.stdout.listeners('error').length > 0, 'EPIPE handler should be registered on stdout');
    });

    await test('config.loadSettings is exported as alias to loadUserConfig', () => {
      assert.strictEqual(typeof config.loadSettings, 'function');
      assert.strictEqual(config.loadSettings, config.loadUserConfig);
    });

    await test('getRollingUsage computes 5h and 7d rolling windows accurately', () => {
      const now = new Date('2026-08-29T12:00:00Z');
      const sessions = [
        {
          sessionId: 's1',
          startTime: '2026-08-29T10:00:00Z',
          turns: [
            { createdAt: '2026-08-29T10:00:00Z', totalTokens: 1000 },
            { createdAt: '2026-08-29T08:00:00Z', totalTokens: 2000 }
          ]
        },
        {
          sessionId: 's2',
          startTime: '2026-08-27T10:00:00Z',
          turns: [
            { createdAt: '2026-08-27T10:00:00Z', totalTokens: 5000 }
          ]
        },
        {
          sessionId: 's3',
          startTime: '2026-08-20T10:00:00Z',
          turns: [
            { createdAt: '2026-08-20T10:00:00Z', totalTokens: 10000 }
          ]
        }
      ];
      const ru = aggregator.getRollingUsage(sessions, now, { limit5h: 10000, limit7d: 100000 });
      // 5h window: >= 07:00:00Z (s1 turns: 1000 + 2000 = 3000)
      assert.strictEqual(ru.tokens5h, 3000);
      assert.strictEqual(ru.limit5h, 10000);
      assert.strictEqual(ru.used5hPercent, 30);
      assert.strictEqual(ru.remain5hPercent, 70);

      // 7d window: >= 2026-08-22 (s1 + s2: 3000 + 5000 = 8000; s3 is 9 days ago)
      assert.strictEqual(ru.tokens7d, 8000);
      assert.strictEqual(ru.limit7d, 100000);
      assert.strictEqual(ru.used7dPercent, 8);
      assert.strictEqual(ru.remain7dPercent, 92);
    });

    await test('renderRealTimeBadge renders 5h and 7d progress bars and percent', () => {
      const badgeData = {
        turnTokens: 500,
        turnCostUsd: 0.0005,
        todayTokens: 5000,
        todayCostUsd: 0.005,
        cacheHitRate: 80,
        rollingUsage: {
          remain5hPercent: 80,
          remain7dPercent: 50
        }
      };
      const badge = formatter.renderRealTimeBadge(badgeData, 'usd', false, 'DASH_LINK');
      const q5hLabel = i18n.t('quota5h') || '5h';
      const q7dLabel = i18n.t('quota7d') || '7d';
      assert(badge.includes(q5hLabel), `Badge should contain ${q5hLabel}`);
      assert(badge.includes(q7dLabel), `Badge should contain ${q7dLabel}`);
      assert(badge.includes('80%'), 'Badge should contain 80%');
      assert(badge.includes('50%'), 'Badge should contain 50%');
      assert(badge.includes('DASH_LINK'), 'Badge should contain link');
    });

    await test('renderDashboardHtml includes quotaWrap DOM element and rollingUsage logic', () => {
      const { buildDashboardPayload, renderDashboardHtml } = require('../src/html-report');
      const payload = buildDashboardPayload([], {
        quota: { limit5h: 50000, limit7d: 500000 }
      });
      assert(payload.rollingUsage, 'Payload must include rollingUsage');
      const html = renderDashboardHtml(payload);
      assert(html.includes('id="quotaWrap"'), 'HTML must include quotaWrap element');
      assert(html.includes('renderQuota'), 'HTML must include renderQuota client script');
    });
  });

  // --- Suite 23: Serve Staleness Self-Termination (R1, REQ-101..103, 107) ---
  await describe('23. Serve Staleness Self-Termination (R1)', async () => {
    const serveStaleness = require('../src/serve-staleness');
    const serve = require('../src/serve');
    const dashboardLink = require('../src/dashboard-link');

    await test('sourceCodeChangedSinceStart returns stale:false when all js files are older than startTimeMs', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'staleness-test-'));
      const f1 = path.join(tempDir, 'a.js');
      const f2 = path.join(tempDir, 'b.js');
      fs.writeFileSync(f1, '// a');
      fs.writeFileSync(f2, '// b');
      const baseTime = Date.now();
      const pastTime = new Date(baseTime - 10000);
      fs.utimesSync(f1, pastTime, pastTime);
      fs.utimesSync(f2, pastTime, pastTime);

      const res = serveStaleness.sourceCodeChangedSinceStart(tempDir, baseTime);
      assert.strictEqual(res.stale, false);
      assert.strictEqual(res.file, null);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('sourceCodeChangedSinceStart returns stale:true and filename when a js file is newer than startTimeMs', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'staleness-test-'));
      const f1 = path.join(tempDir, 'alpha.js');
      const f2 = path.join(tempDir, 'beta.js');
      fs.writeFileSync(f1, '// alpha');
      fs.writeFileSync(f2, '// beta');
      const baseTime = Date.now();
      const pastTime = new Date(baseTime - 10000);
      const futureTime = new Date(baseTime + 5000);
      fs.utimesSync(f1, pastTime, pastTime);
      fs.utimesSync(f2, futureTime, futureTime);

      const res = serveStaleness.sourceCodeChangedSinceStart(tempDir, baseTime);
      assert.strictEqual(res.stale, true);
      assert.strictEqual(res.file, 'beta.js');
      assert(res.mtimeMs >= baseTime + 5000);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('sourceCodeChangedSinceStart returns stale:false for mtime within safety margin before start', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'staleness-test-'));
      const f1 = path.join(tempDir, 'recent.js');
      fs.writeFileSync(f1, '// recent');
      const baseTime = Date.now();
      const slightlyEarlier = new Date(baseTime - 1000); // 1s before start (within 2s margin)
      fs.utimesSync(f1, slightlyEarlier, slightlyEarlier);

      const res = serveStaleness.sourceCodeChangedSinceStart(tempDir, baseTime);
      assert.strictEqual(res.stale, false);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('sourceCodeChangedSinceStart returns stale:false on non-existent dir (fail-open)', () => {
      const ghostDir = path.join(os.tmpdir(), 'non-existent-dir-' + Date.now());
      const res = serveStaleness.sourceCodeChangedSinceStart(ghostDir, Date.now());
      assert.strictEqual(res.stale, false);
      assert.strictEqual(res.file, null);
    });

    await test('sourceCodeChangedSinceStart ignores non-.js files even when newer than start', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'staleness-test-'));
      const f1 = path.join(tempDir, 'data.json');
      const f2 = path.join(tempDir, 'notes.tmp');
      fs.writeFileSync(f1, '{"test": 1}');
      fs.writeFileSync(f2, 'temporary notes');
      const baseTime = Date.now();
      const futureTime = new Date(baseTime + 5000);
      fs.utimesSync(f1, futureTime, futureTime);
      fs.utimesSync(f2, futureTime, futureTime);

      const res = serveStaleness.sourceCodeChangedSinceStart(tempDir, baseTime);
      assert.strictEqual(res.stale, false);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('readCacheVersionHeader extracts version from first 64 bytes of large fixture', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'cache-hdr-test-'));
      const p = path.join(tempDir, 'large-cache.json');
      const largeTail = 'x'.repeat(2048);
      const content = `{\n  "version": 4,\n  "lastUpdated": "${new Date().toISOString()}",\n  "tail": "${largeTail}"\n}`;
      fs.writeFileSync(p, content, 'utf8');

      const v = serveStaleness.readCacheVersionHeader(p);
      assert.strictEqual(v, 4);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('readCacheVersionHeader returns null on missing file, corrupt header, or no version (REQ-107)', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'cache-hdr-test-'));
      const ghostPath = path.join(tempDir, 'missing.json');
      assert.strictEqual(serveStaleness.readCacheVersionHeader(ghostPath), null);

      const corruptPath = path.join(tempDir, 'corrupt.json');
      fs.writeFileSync(corruptPath, '{ not valid json header at all ...', 'utf8');
      assert.strictEqual(serveStaleness.readCacheVersionHeader(corruptPath), null);

      const noVersionPath = path.join(tempDir, 'no-version.json');
      fs.writeFileSync(noVersionPath, '{\n  "sessions": []\n}', 'utf8');
      assert.strictEqual(serveStaleness.readCacheVersionHeader(noVersionPath), null);

      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('startup guard refuses to start and calls onSelfTerminate when disk cache version is newer (REQ-103)', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'startup-guard-test-'));
      const fakeCache = path.join(tempDir, 'future-cache.json');
      fs.writeFileSync(fakeCache, JSON.stringify({ version: 99, sessions: [] }, null, 2), 'utf8');

      let spyReason = null;
      const result = await serve.startDashboardServer({
        port: 0,
        cacheFile: fakeCache,
        onSelfTerminate: (reason) => {
          spyReason = reason;
        }
      });

      assert.strictEqual(result, null, 'startDashboardServer should return null when startup guard triggers in test mode');
      assert(spyReason !== null, 'onSelfTerminate spy should have been called');
      assert(spyReason.includes('newer than this build'), `Expected reason to mention newer version, got: ${spyReason}`);

      // When version <= current, it starts normally
      const validCache = path.join(tempDir, 'valid-cache.json');
      fs.writeFileSync(validCache, JSON.stringify({ version: cacheManager.CACHE_SCHEMA_VERSION, sessions: [] }, null, 2), 'utf8');
      let validSpy = null;
      const validServer = await serve.startDashboardServer({
        port: 0,
        cacheFile: validCache,
        onSelfTerminate: (r) => { validSpy = r; }
      });
      assert(validServer !== null && validServer.port > 0);
      assert.strictEqual(validSpy, null);
      await serve.stopDashboardServer(validServer.server);

      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('self-termination triggers on code update during SSE push, clears port, and closes server (REQ-101..102)', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'self-term-test-'));
      const testJs = path.join(tempDir, 'test-module.js');
      const startTime = Date.now();
      const pastTime = new Date(startTime - 10000);
      fs.writeFileSync(testJs, '// initial module');
      fs.utimesSync(testJs, pastTime, pastTime);

      let terminationReason = null;
      const info = await serve.startDashboardServer({
        port: 0,
        intervalMs: 50,
        srcDir: tempDir,
        onSelfTerminate: (reason) => {
          terminationReason = reason;
        }
      });

      assert(info && info.port > 0);
      assert.strictEqual(await dashboardLink.probePort(info.port), true);

      // Connect to SSE events to start push cycle
      const req = http.get(`${info.url.replace(/\/$/, '')}/events`, () => {});
      req.on('error', () => {});

      // Touch the test js file in injected srcDir to future mtime
      const futureTime = new Date(Date.now() + 5000);
      fs.utimesSync(testJs, futureTime, futureTime);

      // Wait for SSE push tick to detect staleness and terminate
      await new Promise((resolve) => {
        const check = setInterval(() => {
          if (terminationReason !== null) {
            clearInterval(check);
            resolve();
          }
        }, 30);
        setTimeout(() => {
          clearInterval(check);
          resolve();
        }, 5000);
      });

      req.destroy();

      assert(terminationReason !== null, 'Server should have self-terminated on modified source file');
      assert(terminationReason.includes('test-module.js'));

      // Verify port probe fails after self-termination
      const portClosed = !(await dashboardLink.probePort(info.port));
      assert(portClosed, 'Server port should be closed after self-termination');

      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('watchdog constant satisfies REQ-101 bound (STALENESS_WATCHDOG_MS <= 60000 and > 0)', () => {
      assert(serve.STALENESS_WATCHDOG_MS > 0, 'watchdog interval must be positive');
      assert(serve.STALENESS_WATCHDOG_MS <= 60000, 'watchdog interval must be <= 60s per REQ-101/105');
      assert(serveStaleness.MTIME_SAFETY_MARGIN_MS >= 1000, 'mtime safety margin must be >= 1s');
    });
  });

  // --- Suite 24: Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration ---
  // --- Suite 24: Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration ---
  await describe('24. Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration', async () => {
    const htmlReport = require('../src/html-report');

    await test('parseCommandLine should extract csrfToken, port, and protocol from arguments (hyphen and underscore variants)', () => {
      const cmd1 = '"C:\\path\\language_server_windows_x64.exe" --api_url http://127.0.0.1:54321 --csrf_token my-secret-csrf-token --other_flag';
      const res1 = geminiQuota.parseCommandLine(cmd1);
      assert.strictEqual(res1.csrfToken, 'my-secret-csrf-token');
      assert.strictEqual(res1.port, 54321);
      assert.strictEqual(res1.protocol, 'http');

      const cmd2 = '/usr/bin/language_server --port=8089 --csrf-token=tok_998877';
      const res2 = geminiQuota.parseCommandLine(cmd2);
      assert.strictEqual(res2.csrfToken, 'tok_998877');
      assert.strictEqual(res2.port, 8089);
      assert.strictEqual(res2.protocol, null);

      const cmd3 = 'language_server --manager-port 7777 --csrf_token auth-abc-123';
      const res3 = geminiQuota.parseCommandLine(cmd3);
      assert.strictEqual(res3.csrfToken, 'auth-abc-123');
      assert.strictEqual(res3.port, 7777);
      assert.strictEqual(res3.protocol, null);

      const cmd4 = 'language_server --api-url 127.0.0.1:8888 --csrf-token hyphen-tok';
      const res4 = geminiQuota.parseCommandLine(cmd4);
      assert.strictEqual(res4.csrfToken, 'hyphen-tok');
      assert.strictEqual(res4.port, 8888);
      assert.strictEqual(res4.protocol, null);

      const cmd5 = 'language_server --api_url https://127.0.0.1:54631 --csrf_token sec-token';
      const res5 = geminiQuota.parseCommandLine(cmd5);
      assert.strictEqual(res5.csrfToken, 'sec-token');
      assert.strictEqual(res5.port, 54631);
      assert.strictEqual(res5.protocol, 'https');

      const cmd6 = 'language_server --api-url=https://localhost:4433 --csrf-token=hyphen-sec';
      const res6 = geminiQuota.parseCommandLine(cmd6);
      assert.strictEqual(res6.csrfToken, 'hyphen-sec');
      assert.strictEqual(res6.port, 4433);
      assert.strictEqual(res6.protocol, 'https');

      const resEmpty = geminiQuota.parseCommandLine('');
      assert.strictEqual(resEmpty.csrfToken, null);
      assert.strictEqual(resEmpty.port, null);
      assert.strictEqual(resEmpty.protocol, null);
    });

    await test('extractPortFromNetstat should parse listening port for specific PID', () => {
      const netstatOutput = [
        'Active Connections',
        '  Proto  Local Address          Foreign Address        State           PID',
        '  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       112',
        '  TCP    127.0.0.1:54321        0.0.0.0:0              LISTENING       9876',
        '  TCP    127.0.0.1:4444         127.0.0.1:54321        ESTABLISHED     9876',
        '  TCP    [::]:8080              [::]:0                 LISTENING       5544'
      ].join('\n');

      const port = geminiQuota.extractPortFromNetstat(netstatOutput, 9876);
      assert.strictEqual(port, 54321);

      const portMissing = geminiQuota.extractPortFromNetstat(netstatOutput, 9999);
      assert.strictEqual(portMissing, null);
    });

    await test('extractPortFromLsof should parse listening port from lsof output', () => {
      const lsofOutput = 'language_ 9876 user 5u IPv4 0x123456 0t0 TCP 127.0.0.1:54321 (LISTEN)';
      const port = geminiQuota.extractPortFromLsof(lsofOutput);
      assert.strictEqual(port, 54321);

      const lsofEmpty = geminiQuota.extractPortFromLsof('');
      assert.strictEqual(lsofEmpty, null);
    });

    await test('formatCountdownDuration and formatResetTime should calculate countdowns accurately', () => {
      assert.strictEqual(geminiQuota.formatCountdownDuration(45), '45s');
      assert.strictEqual(geminiQuota.formatCountdownDuration(90), '1m');
      assert.strictEqual(geminiQuota.formatCountdownDuration(3600), '1h');
      assert.strictEqual(geminiQuota.formatCountdownDuration(8100), '2h 15m');
      assert.strictEqual(geminiQuota.formatCountdownDuration(90000), '1d 1h');

      const now = new Date('2026-08-29T06:00:00.000Z');
      const resetTime = '2026-08-29T08:15:00.000Z'; // 2h 15m later
      const res = geminiQuota.formatResetTime(resetTime, now);
      assert.strictEqual(res.resetInSeconds, 8100);
      assert.strictEqual(res.resetFormatted, '2h 15m');

      const pastReset = geminiQuota.formatResetTime('2026-08-29T05:00:00.000Z', now);
      assert.strictEqual(pastReset.resetInSeconds, 0);
      assert.strictEqual(pastReset.resetFormatted, '0s');

      const nullReset = geminiQuota.formatResetTime(null, now);
      assert.strictEqual(nullReset.resetInSeconds, null);
      assert.strictEqual(nullReset.resetFormatted, null);
    });

    await test('extractQuotaFromSummary should parse RetrieveUserQuotaSummary response with 5h and 7d buckets', () => {
      const refDate = new Date('2026-08-29T00:00:00.000Z');
      const summaryPayload = {
        response: {
          groups: [
            {
              displayName: 'Gemini Models',
              description: 'Models within this group: Gemini Flash, Gemini Pro',
              buckets: [
                {
                  bucketId: 'gemini-weekly',
                  displayName: 'Weekly Limit Remaining',
                  window: 'weekly',
                  remainingFraction: 0.21101993,
                  resetTime: '2026-09-01T17:57:54Z'
                },
                {
                  bucketId: 'gemini-5h',
                  displayName: 'Five Hour Limit Remaining',
                  window: '5h',
                  remainingFraction: 0.7923148,
                  resetTime: '2026-08-29T04:10:00Z'
                }
              ]
            },
            {
              displayName: 'Claude and GPT models',
              buckets: [
                { bucketId: '3p-weekly', window: 'weekly', remainingFraction: 0.26745465, resetTime: '2026-09-01T00:00:00Z' },
                { bucketId: '3p-5h', window: '5h', remainingFraction: 1.0, resetTime: '2026-08-29T05:00:00Z' }
              ]
            }
          ]
        }
      };

      const extracted = geminiQuota.extractQuotaFromSummary(summaryPayload, refDate);
      assert(extracted !== null, 'Extracted quota should not be null');
      assert.strictEqual(extracted.isLive, true);
      assert.strictEqual(extracted.modelLabel, 'Gemini Models');

      // 5h bucket validation
      assert(extracted.quota5h !== null);
      assert.strictEqual(extracted.quota5h.remainPercent, 79);
      assert.strictEqual(extracted.quota5h.remainingFraction, 0.7923148);
      assert.strictEqual(extracted.quota5h.resetFormatted, '4h 10m');
      assert.strictEqual(extracted.quota5h.window, '5h');

      // 7d bucket validation
      assert(extracted.quota7d !== null);
      assert.strictEqual(extracted.quota7d.remainPercent, 21);
      assert.strictEqual(extracted.quota7d.remainingFraction, 0.21101993);
      assert(extracted.quota7d.resetFormatted.includes('d') || extracted.quota7d.resetFormatted.includes('h'));
      assert.strictEqual(extracted.quota7d.window, 'weekly');

      // Top-level compatibility fields
      assert.strictEqual(extracted.remainPercent, 79);
      assert.strictEqual(extracted.remainingFraction, 0.7923148);
      assert.strictEqual(extracted.resetFormatted, '4h 10m');
    });

    await test('extractGeminiQuotaFromStatus should extract Gemini pool and discriminate Claude pool (legacy GetUserStatus)', () => {
      const refDate = new Date('2026-08-29T06:00:00.000Z');
      const payload = {
        clientModelConfigs: [
          {
            label: 'Claude 3.7 Sonnet',
            quotaInfo: {
              remainingFraction: 0.40,
              resetTime: '2026-08-29T15:00:00.000Z'
            }
          },
          {
            label: 'Gemini 3.7 Flash Thinking',
            quotaInfo: {
              remainingFraction: 0.85,
              resetTime: '2026-08-29T08:15:00.000Z'
            }
          }
        ]
      };

      const extracted = geminiQuota.extractGeminiQuotaFromStatus(payload, refDate);
      assert(extracted !== null, 'Gemini quota should be extracted');
      assert.strictEqual(extracted.remainPercent, 85);
      assert.strictEqual(extracted.remainingFraction, 0.85);
      assert.strictEqual(extracted.resetFormatted, '2h 15m');
      assert.strictEqual(extracted.resetInSeconds, 8100);
      assert.strictEqual(extracted.modelLabel, 'Gemini 3.7 Flash Thinking');
      assert.strictEqual(extracted.isLive, true);
      assert.strictEqual(extracted.source, 'language_server');
      assert(extracted.quota5h !== null);
      assert.strictEqual(extracted.quota5h.remainPercent, 85);

      // Test snake_case fallback and nested userStatus
      const payloadSnake = {
        userStatus: {
          clientModelConfigs: [
            {
              model: 'gemini-2.5-pro',
              quota_info: {
                remaining_fraction: 0.62,
                reset_time: '2026-08-29T07:00:00.000Z'
              }
            }
          ]
        }
      };
      const extractedSnake = geminiQuota.extractGeminiQuotaFromStatus(payloadSnake, refDate);
      assert(extractedSnake !== null);
      assert.strictEqual(extractedSnake.remainPercent, 62);
      assert.strictEqual(extractedSnake.resetFormatted, '1h');

      // Empty or invalid payload
      assert.strictEqual(geminiQuota.extractGeminiQuotaFromStatus({}), null);
      assert.strictEqual(geminiQuota.extractGeminiQuotaFromStatus(null), null);
      assert.strictEqual(geminiQuota.extractGeminiQuotaFromStatus({ clientModelConfigs: [] }), null);
    });

    await test('saveCachedGeminiQuota and getCachedGeminiQuota should persist and honor 30s TTL with 5h/7d countdowns', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-test-'));
      const testCacheFile = path.join(tempDir, 'gemini_quota_cache.json');

      const quotaData = {
        remainPercent: 79,
        remainingFraction: 0.79,
        resetTime: new Date(Date.now() + 7200000).toISOString(),
        resetFormatted: '2h',
        resetInSeconds: 7200,
        quota5h: {
          remainPercent: 79,
          remainingFraction: 0.79,
          resetTime: new Date(Date.now() + 7200000).toISOString(),
          resetFormatted: '2h',
          resetInSeconds: 7200,
          window: '5h'
        },
        quota7d: {
          remainPercent: 21,
          remainingFraction: 0.21,
          resetTime: new Date(Date.now() + 86400000 * 3).toISOString(),
          resetFormatted: '3d',
          resetInSeconds: 86400 * 3,
          window: 'weekly'
        },
        modelLabel: 'Gemini Models',
        isLive: true,
        source: 'language_server'
      };

      const saved = geminiQuota.saveCachedGeminiQuota(quotaData, testCacheFile);
      assert(saved !== null);
      assert.strictEqual(saved.remainPercent, 79);
      assert.strictEqual(saved.quota5h.remainPercent, 79);
      assert.strictEqual(saved.quota7d.remainPercent, 21);
      assert(fs.existsSync(testCacheFile));

      // Immediate read (Fresh)
      const readFresh = geminiQuota.getCachedGeminiQuota(testCacheFile, 30000);
      assert(readFresh !== null);
      assert.strictEqual(readFresh.remainPercent, 79);
      assert.strictEqual(readFresh.quota5h.remainPercent, 79);
      assert.strictEqual(readFresh.quota7d.remainPercent, 21);
      assert.strictEqual(readFresh.isFresh, true);
      assert(readFresh.quota5h.resetFormatted.includes('h') || readFresh.quota5h.resetFormatted.includes('m'));
      assert(readFresh.quota7d.resetFormatted.includes('d') || readFresh.quota7d.resetFormatted.includes('h'));

      // Stale read (Simulate expired timestamp)
      const staleContent = JSON.parse(fs.readFileSync(testCacheFile, 'utf8'));
      staleContent.timestampMs = Date.now() - 35000; // 35s ago (> 30s TTL)
      fs.writeFileSync(testCacheFile, JSON.stringify(staleContent, null, 2), 'utf8');

      const readStale = geminiQuota.getCachedGeminiQuota(testCacheFile, 30000);
      assert(readStale !== null);
      assert.strictEqual(readStale.isFresh, false);

      // Non-existent cache
      assert.strictEqual(geminiQuota.getCachedGeminiQuota(path.join(tempDir, 'missing.json')), null);

      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    // Self-signed certificate helper for ephemeral HTTPS mock servers
    function createTestCert() {
      const { publicKey, privateKey } = crypto.generateKeyPairSync('rsa', {
        modulusLength: 2048,
        publicKeyEncoding: { type: 'pkcs1', format: 'der' },
        privateKeyEncoding: { type: 'pkcs8', format: 'pem' }
      });
      const encodeLen = len => len < 128 ? Buffer.from([len]) : Buffer.concat([Buffer.from([0x80 | (len > 255 ? 2 : 1)]), len > 255 ? Buffer.from([len >> 8, len & 0xff]) : Buffer.from([len])]);
      const encodeTag = (tag, data) => Buffer.concat([Buffer.from([tag]), encodeLen(data.length), data]);
      const encodeSeq = items => encodeTag(0x30, Buffer.concat(items));
      const encodeOid = oidStr => {
        const p = oidStr.split('.').map(Number);
        const b = [p[0] * 40 + p[1]];
        for (let i = 2; i < p.length; i++) {
          let v = p[i], o = [];
          o.push(v & 0x7f);
          while ((v >>= 7) > 0) o.unshift(0x80 | (v & 0x7f));
          b.push(...o);
        }
        return encodeTag(0x06, Buffer.from(b));
      };
      const sigAlg = encodeSeq([encodeOid('1.2.840.113549.1.1.11'), Buffer.from([0x05, 0x00])]);
      const pubInfo = encodeSeq([encodeSeq([encodeOid('1.2.840.113549.1.1.1'), Buffer.from([0x05, 0x00])]), encodeTag(0x03, Buffer.concat([Buffer.from([0x00]), publicKey]))]);
      const name = encodeSeq([encodeTag(0x31, encodeSeq([encodeOid('2.5.4.3'), encodeTag(0x0c, Buffer.from('localhost', 'utf8'))]))]);
      const validity = encodeSeq([encodeTag(0x17, Buffer.from('250101000000Z', 'ascii')), encodeTag(0x17, Buffer.from('350101000000Z', 'ascii'))]);
      const tbs = encodeSeq([encodeTag(0x02, Buffer.from([0x01])), sigAlg, name, validity, name, pubInfo]);
      const signer = crypto.createSign('sha256');
      signer.update(tbs);
      const sig = signer.sign(privateKey);
      const certDer = encodeSeq([tbs, sigAlg, encodeTag(0x03, Buffer.concat([Buffer.from([0x00]), sig]))]);
      const certPem = `-----BEGIN CERTIFICATE-----\n${certDer.toString('base64').match(/.{1,64}/g).join('\n')}\n-----END CERTIFICATE-----\n`;
      return { certPem, privateKey };
    }

    await test('makeRpcRequest and RPC helpers should support HTTPS with self-signed certificate tolerance (rejectUnauthorized: false)', async () => {
      const { certPem, privateKey } = createTestCert();
      let lastHeaders = null;
      let lastPath = null;

      const httpsServer = https.createServer({ key: privateKey, cert: certPem }, (req, res) => {
        lastHeaders = req.headers;
        lastPath = req.url;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          response: {
            groups: [
              {
                displayName: 'Gemini Models',
                buckets: [
                  {
                    bucketId: 'gemini-5h',
                    window: '5h',
                    remainingFraction: 0.92,
                    resetTime: new Date(Date.now() + 18000000).toISOString()
                  }
                ]
              }
            ]
          }
        }));
      });

      await new Promise(r => httpsServer.listen(0, '127.0.0.1', r));
      const port = httpsServer.address().port;

      // 1. Test makeRpcRequest directly over HTTPS
      const rpcRes = await geminiQuota.makeRpcRequest({
        path: '/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary',
        port,
        csrfToken: 'test-https-csrf',
        protocol: 'https',
        rejectUnauthorized: false
      });
      assert(rpcRes.response !== undefined);
      assert.strictEqual(lastHeaders['x-codeium-csrf-token'], 'test-https-csrf');
      assert.strictEqual(lastHeaders['connect-protocol-version'], '1');
      assert.strictEqual(lastPath, '/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary');

      // 2. Test callRetrieveUserQuotaSummary with protocol 'https'
      const summaryRes = await geminiQuota.callRetrieveUserQuotaSummary({
        port,
        csrfToken: 'summary-https-csrf',
        protocol: 'https',
        rejectUnauthorized: false
      });
      assert(summaryRes.response !== undefined);
      assert.strictEqual(lastHeaders['x-codeium-csrf-token'], 'summary-https-csrf');

      // 3. Test callGetUserStatus with protocol 'https'
      const statusRes = await geminiQuota.callGetUserStatus({
        port,
        csrfToken: 'status-https-csrf',
        protocol: 'https',
        rejectUnauthorized: false
      });
      assert(statusRes.response !== undefined);
      assert.strictEqual(lastHeaders['x-codeium-csrf-token'], 'status-https-csrf');

      await new Promise(r => httpsServer.close(r));
    });

    await test('fetchLiveGeminiQuota should support HTTPS Language Server endpoints without TLS handshake errors and adaptively fallback to HTTP', async () => {
      const { certPem, privateKey } = createTestCert();
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-https-'));
      const testCacheFile = path.join(tempDir, 'quota_cache_https.json');

      let receivedHttpsCsrf = null;
      const httpsServer = https.createServer({ key: privateKey, cert: certPem }, (req, res) => {
        receivedHttpsCsrf = req.headers['x-codeium-csrf-token'];
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          response: {
            groups: [
              {
                displayName: 'Gemini Models',
                buckets: [
                  {
                    bucketId: 'gemini-5h',
                    window: '5h',
                    remainingFraction: 0.85,
                    resetTime: new Date(Date.now() + 10000000).toISOString()
                  },
                  {
                    bucketId: 'gemini-weekly',
                    window: 'weekly',
                    remainingFraction: 0.45,
                    resetTime: new Date(Date.now() + 86400000 * 2).toISOString()
                  }
                ]
              }
            ]
          }
        }));
      });

      await new Promise(r => httpsServer.listen(0, '127.0.0.1', r));
      const httpsPort = httpsServer.address().port;

      // Query HTTPS server (default probes HTTPS first)
      const liveHttpsRes = await geminiQuota.fetchLiveGeminiQuota({
        port: httpsPort,
        csrfToken: 'live-https-token',
        cachePath: testCacheFile
      });

      assert.strictEqual(receivedHttpsCsrf, 'live-https-token');
      assert.strictEqual(liveHttpsRes.isLive, true);
      assert.strictEqual(liveHttpsRes.remainPercent, 85);
      assert.strictEqual(liveHttpsRes.quota5h.remainPercent, 85);
      assert.strictEqual(liveHttpsRes.quota7d.remainPercent, 45);

      await new Promise(r => httpsServer.close(r));
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('fetchLiveGeminiQuota should query RetrieveUserQuotaSummary and fallback gracefully when offline', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-rpc-'));
      const testCacheFile = path.join(tempDir, 'quota_cache.json');

      // 1. Live RPC test with ephemeral mock server returning RetrieveUserQuotaSummary schema
      let receivedCsrf = null;
      let receivedPath = null;

      const mockServer = http.createServer((req, res) => {
        receivedCsrf = req.headers['x-codeium-csrf-token'];
        receivedPath = req.url;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          response: {
            groups: [
              {
                displayName: 'Gemini Models',
                buckets: [
                  {
                    bucketId: 'gemini-weekly',
                    window: 'weekly',
                    remainingFraction: 0.211,
                    resetTime: new Date(Date.now() + 86400000 * 3).toISOString()
                  },
                  {
                    bucketId: 'gemini-5h',
                    window: '5h',
                    remainingFraction: 0.792,
                    resetTime: new Date(Date.now() + 15000000).toISOString()
                  }
                ]
              }
            ]
          }
        }));
      });

      await new Promise((resolve) => mockServer.listen(0, '127.0.0.1', resolve));
      const boundPort = mockServer.address().port;

      const liveRes = await geminiQuota.fetchLiveGeminiQuota({
        port: boundPort,
        csrfToken: 'test-csrf-token-123',
        cachePath: testCacheFile
      });

      assert.strictEqual(receivedCsrf, 'test-csrf-token-123');
      assert.strictEqual(receivedPath, '/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary');
      assert.strictEqual(liveRes.isLive, true);
      assert.strictEqual(liveRes.remainPercent, 79);
      assert.strictEqual(liveRes.quota5h.remainPercent, 79);
      assert.strictEqual(liveRes.quota7d.remainPercent, 21);

      await new Promise((resolve) => mockServer.close(resolve));

      // 2. Offline fallback test
      const offlineRes = await geminiQuota.fetchLiveGeminiQuota({
        port: 59999, // Unused closed port
        csrfToken: 'any-token',
        cachePath: testCacheFile
      });

      assert.strictEqual(offlineRes.isLive, false);
      assert.strictEqual(offlineRes.remainPercent, null);
      assert.strictEqual(offlineRes.source, 'fallback');

      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('hookHandler and formatter should render both Gemini 5h and 7d live quotas and fallback to rolling usage', async () => {
      const i18n = require('../src/i18n');
      i18n.setLocale('ko');

      const liveQuota = {
        remainPercent: 79,
        remainingFraction: 0.792,
        resetFormatted: '4h 10m',
        resetInSeconds: 15000,
        quota5h: {
          remainPercent: 79,
          remainingFraction: 0.792,
          resetFormatted: '4h 10m',
          resetInSeconds: 15000,
          window: '5h'
        },
        quota7d: {
          remainPercent: 21,
          remainingFraction: 0.211,
          resetFormatted: '3d 20h',
          resetInSeconds: 331200,
          window: 'weekly'
        },
        isLive: true,
        source: 'language_server'
      };

      const badgeStr = formatter.renderRealTimeBadge({
        turnTokens: 1500,
        turnCostUsd: 0.001,
        todayTokens: 25000,
        todayCostUsd: 0.02,
        cacheHitRate: 80,
        isFree: false,
        geminiQuota: liveQuota
      }, 'usd', false);

      assert(badgeStr.includes('5h:'), `Badge must include '5h:', got: ${badgeStr}`);
      assert(!badgeStr.includes('Gemini 5h:'), `Badge must not include 'Gemini 5h:', got: ${badgeStr}`);
      assert(badgeStr.includes('79%'), 'Badge must include 79% for 5h quota');
      assert(badgeStr.includes('4h 10m'), 'Badge must include 4h 10m countdown');
      assert(badgeStr.includes('7d:'), `Badge must include '7d:', got: ${badgeStr}`);
      assert(badgeStr.includes('21%'), 'Badge must include 21% for 7d quota');
      assert(badgeStr.includes('3d 20h'), 'Badge must include 3d 20h countdown');

      // Fallback rendering when geminiQuota is null
      const fallbackBadge = formatter.renderRealTimeBadge({
        turnTokens: 1500,
        turnCostUsd: 0.001,
        todayTokens: 25000,
        todayCostUsd: 0.02,
        cacheHitRate: 80,
        isFree: false,
        geminiQuota: null,
        rollingUsage: {
          remain5hPercent: 95,
          remain7dPercent: 90,
          tokens5h: 1000,
          limit5h: 20000000,
          tokens7d: 5000,
          limit7d: 150000000
        }
      }, 'usd', false);

      assert(fallbackBadge.includes('5시간') || fallbackBadge.includes('5h'), 'Fallback badge must include rolling usage');
      assert(fallbackBadge.includes('95%'), 'Fallback badge must include 5h percent');
    });

    await test('htmlReport payload and client script should include Gemini 5h and 7d quota live cards and indicator', () => {
      const payload = htmlReport.buildDashboardPayload([], {
        currency: 'usd',
        lang: 'ko',
        geminiQuota: {
          remainPercent: 79,
          resetFormatted: '4h 10m',
          quota5h: {
            remainPercent: 79,
            resetFormatted: '4h 10m',
            window: '5h'
          },
          quota7d: {
            remainPercent: 21,
            resetFormatted: '3d 20h',
            window: 'weekly'
          },
          isLive: true,
          source: 'language_server'
        }
      });

      assert(payload.geminiQuota !== undefined);
      assert.strictEqual(payload.geminiQuota.remainPercent, 79);
      assert.strictEqual(payload.geminiQuota.isLive, true);
      assert.strictEqual(payload.geminiQuota.quota5h.remainPercent, 79);
      assert.strictEqual(payload.geminiQuota.quota7d.remainPercent, 21);

      const html = htmlReport.renderDashboardHtml(payload, { refreshSec: 5, servePort: 8787 });
      assert(html.includes('renderQuota(r, gq)'), 'renderQuota must accept (r, gq)');
      assert(html.includes('quota5h'), 'HTML must reference quota5h');
      assert(html.includes('quota7d'), 'HTML must reference quota7d');
      assert(html.includes('quotaLive'), 'HTML must include quotaLive indicator');
      assert(html.includes('quotaFallback'), 'HTML must include quotaFallback indicator');
    });

    await test('parseArgs and renderHelp should support --sync-quota flag', () => {
      const opts1 = parseArgs(['node', 'bin/agy-tokens.js', '--sync-quota']);
      assert.strictEqual(opts1.syncQuota, true);

      const opts2 = parseArgs(['node', 'bin/agy-tokens.js', 'sync-quota']);
      assert.strictEqual(opts2.syncQuota, true);

      const opts3 = parseArgs(['node', 'bin/agy-tokens.js', '--quota']);
      assert.strictEqual(opts3.syncQuota, true);

      const helpText = formatter.renderHelp();
      assert(helpText.includes('--sync-quota'), 'Help screen must document --sync-quota');
    });
  });

  // --- Suite 25: Cross-Platform Installer Scripts Unit Tests ---
  await describe('25. Cross-Platform Installer Scripts Unit Tests (install.bat & install.sh & configure-statusline.js)', async () => {
    const rootDir = path.resolve(__dirname, '..');
    const batPath = path.join(rootDir, 'scripts', 'install.bat');
    const shPath = path.join(rootDir, 'scripts', 'install.sh');
    const configScriptPath = path.join(rootDir, 'scripts', 'lib', 'configure-statusline.js');
    const {
      configureStatusline,
      getSettingsPath,
      getTimestamp,
      parseArgs,
      writeAtomic,
      DEFAULT_COMMAND
    } = require(configScriptPath);

    await test('scripts/install.bat should exist with CRLF line endings, shims, and configure-statusline invocation', () => {
      assert(fs.existsSync(batPath), 'scripts/install.bat must exist');
      const rawBat = fs.readFileSync(batPath, 'utf8');

      // Verify line endings
      assert(rawBat.includes('\r\n'), 'install.bat must contain CRLF line endings for Windows cmd.exe');

      // Node.js >= 16 check and npm check
      assert(rawBat.includes('where node'), 'install.bat must check where node');
      assert(rawBat.includes('process.versions.node'), 'install.bat must check Node.js version >= 16');
      assert(rawBat.includes('where npm'), 'install.bat must check where npm');

      // Canonical ROOT_DIR
      assert(rawBat.includes('for %%I in ("%SCRIPT_DIR%..") do set "ROOT_DIR=%%~fI"'), 'install.bat must canonicalize ROOT_DIR');

      // Fallback launchers for all 4 binaries
      assert(rawBat.includes('agy-tools.bat'), 'install.bat must generate agy-tools.bat shim');
      assert(rawBat.includes('antigravity-tools.bat'), 'install.bat must generate antigravity-tools.bat shim');
      assert(rawBat.includes('agy-tokens.bat'), 'install.bat must generate agy-tokens.bat shim');
      assert(rawBat.includes('agy-dashboard.bat'), 'install.bat must generate agy-dashboard.bat shim');
      assert(rawBat.includes('mkdir'), 'install.bat must create fallback bin directory if missing');

      // Automated configure-statusline invocation
      assert(rawBat.includes('configure-statusline.js'), 'install.bat must invoke configure-statusline.js');
      assert(rawBat.includes('"type": "command"'), 'install.bat statusLine snippet must include "type": "command"');
      assert(rawBat.includes('!STATUSLINE_CMD!'), 'install.bat must reference statusline command variable');
    });

    await test('scripts/install.sh should exist with LF line endings, POSIX redirects, symlinks, and configure-statusline invocation', () => {
      assert(fs.existsSync(shPath), 'scripts/install.sh must exist');
      const rawSh = fs.readFileSync(shPath, 'utf8');

      // Verify line endings (LF, no CRLF)
      assert(!rawSh.includes('\r\n'), 'install.sh must not contain CRLF line endings');

      // Node.js >= 16 check and npm check
      assert(rawSh.includes('command -v node'), 'install.sh must check node binary');
      assert(rawSh.includes('process.versions.node'), 'install.sh must check Node.js version >= 16');
      assert(rawSh.includes('command -v npm'), 'install.sh must check npm binary');

      // Non-failing chmod
      assert(rawSh.includes('chmod +x') && rawSh.includes('|| true'), 'install.sh must use non-failing chmod');

      // POSIX redirects
      assert(rawSh.includes('>/dev/null 2>&1'), 'install.sh must use POSIX redirects');

      // Fallback symlinks for all 4 binaries matching package.json bin
      assert(rawSh.includes('ln -sf "$ROOT_DIR/bin/agy-tools.js" "$USER_BIN/agy-tools"'), 'install.sh must link agy-tools');
      assert(rawSh.includes('ln -sf "$ROOT_DIR/bin/agy-tools.js" "$USER_BIN/antigravity-tools"'), 'install.sh must link antigravity-tools');
      assert(rawSh.includes('ln -sf "$ROOT_DIR/bin/agy-tokens.js" "$USER_BIN/agy-tokens"'), 'install.sh must link agy-tokens');
      assert(rawSh.includes('ln -sf "$ROOT_DIR/bin/agy-tokens.js" "$USER_BIN/agy-dashboard"'), 'install.sh must link agy-dashboard');

      // Automated configure-statusline invocation
      assert(rawSh.includes('configure-statusline.js'), 'install.sh must invoke configure-statusline.js');
      assert(rawSh.includes('"type": "command"'), 'install.sh statusLine snippet must include "type": "command"');
      assert(rawSh.includes('$STATUSLINE_CMD'), 'install.sh must reference statusline command variable');
    });

    await test('scripts/lib/configure-statusline.js should create new settings.json when absent (REQ-403)', () => {
      const testDir = path.join(os.tmpdir(), `agy-test-statusline-new-${Date.now()}`);
      fs.mkdirSync(testDir, { recursive: true });
      const testSettingsPath = path.join(testDir, 'settings.json');

      try {
        const result = configureStatusline({ settingsPath: testSettingsPath });
        assert.strictEqual(result.action, 'created');
        assert(fs.existsSync(testSettingsPath), 'settings.json should be created');

        const parsed = JSON.parse(fs.readFileSync(testSettingsPath, 'utf8'));
        assert.strictEqual(parsed.statusLine.type, 'command');
        assert.strictEqual(parsed.statusLine.command, DEFAULT_COMMAND);
        assert.strictEqual(parsed.statusLine.enabled, true);
        assert.strictEqual(parsed.statusLine.stack_with_default, true);
        assert(Array.isArray(parsed.trustedWorkspaces));
      } finally {
        fs.rmSync(testDir, { recursive: true, force: true });
      }
    });

    await test('scripts/lib/configure-statusline.js should merge statusLine into existing settings and create backup (REQ-401)', () => {
      const testDir = path.join(os.tmpdir(), `agy-test-statusline-merge-${Date.now()}`);
      fs.mkdirSync(testDir, { recursive: true });
      const testSettingsPath = path.join(testDir, 'settings.json');

      const initialSettings = {
        colorScheme: 'dark',
        telemetry: false,
        trustedWorkspaces: ['/test/project']
      };
      fs.writeFileSync(testSettingsPath, JSON.stringify(initialSettings, null, 2), 'utf8');

      try {
        const result = configureStatusline({ settingsPath: testSettingsPath });
        assert.strictEqual(result.action, 'updated');
        assert(result.backupPath && fs.existsSync(result.backupPath), 'Backup file must exist');

        // Verify backup preserves original content
        const backupContent = JSON.parse(fs.readFileSync(result.backupPath, 'utf8'));
        assert.strictEqual(backupContent.colorScheme, 'dark');
        assert.strictEqual(backupContent.statusLine, undefined);

        // Verify updated settings contains existing properties + statusLine
        const updated = JSON.parse(fs.readFileSync(testSettingsPath, 'utf8'));
        assert.strictEqual(updated.colorScheme, 'dark');
        assert.strictEqual(updated.telemetry, false);
        assert.deepStrictEqual(updated.trustedWorkspaces, ['/test/project']);
        assert.strictEqual(updated.statusLine.type, 'command');
        assert.strictEqual(updated.statusLine.command, DEFAULT_COMMAND);
        assert.strictEqual(updated.statusLine.enabled, true);
        assert.strictEqual(updated.statusLine.stack_with_default, true);
      } finally {
        fs.rmSync(testDir, { recursive: true, force: true });
      }
    });

    await test('scripts/lib/configure-statusline.js should be idempotent when statusLine already exists (REQ-404)', () => {
      const testDir = path.join(os.tmpdir(), `agy-test-statusline-idempotent-${Date.now()}`);
      fs.mkdirSync(testDir, { recursive: true });
      const testSettingsPath = path.join(testDir, 'settings.json');

      const initialSettings = {
        colorScheme: 'light',
        statusLine: {
          type: 'command',
          command: 'my-custom-statusline-cmd',
          enabled: false
        }
      };
      fs.writeFileSync(testSettingsPath, JSON.stringify(initialSettings, null, 2), 'utf8');

      try {
        const result = configureStatusline({ settingsPath: testSettingsPath });
        assert.strictEqual(result.action, 'already_configured');
        assert.strictEqual(result.backupPath, null);

        const current = JSON.parse(fs.readFileSync(testSettingsPath, 'utf8'));
        assert.strictEqual(current.statusLine.command, 'my-custom-statusline-cmd', 'Must not overwrite existing command');
        assert.strictEqual(current.statusLine.enabled, false, 'Must not overwrite existing properties');
      } finally {
        fs.rmSync(testDir, { recursive: true, force: true });
      }
    });

    await test('scripts/lib/configure-statusline.js should recover from corrupt JSON with backup (REQ-403)', () => {
      const testDir = path.join(os.tmpdir(), `agy-test-statusline-corrupt-${Date.now()}`);
      fs.mkdirSync(testDir, { recursive: true });
      const testSettingsPath = path.join(testDir, 'settings.json');

      const corruptContent = '{ "colorScheme": "dark", INVALID_JSON... ';
      fs.writeFileSync(testSettingsPath, corruptContent, 'utf8');

      try {
        const result = configureStatusline({ settingsPath: testSettingsPath });
        assert.strictEqual(result.action, 'recovered_corrupt');
        assert(result.backupPath && fs.existsSync(result.backupPath), 'Corrupt backup must be created');
        assert.strictEqual(fs.readFileSync(result.backupPath, 'utf8'), corruptContent);

        const fresh = JSON.parse(fs.readFileSync(testSettingsPath, 'utf8'));
        assert.strictEqual(fresh.statusLine.command, DEFAULT_COMMAND);
        assert.strictEqual(fresh.statusLine.enabled, true);
      } finally {
        fs.rmSync(testDir, { recursive: true, force: true });
      }
    });

    await test('scripts/lib/configure-statusline.js should support custom fallback commands and CLI arg parsing (REQ-405)', () => {
      const parsed1 = parseArgs(['--command', 'node "/custom/path/agy-tokens.js" --hook']);
      assert.strictEqual(parsed1.command, 'node "/custom/path/agy-tokens.js" --hook');

      const parsed2 = parseArgs(['--command=custom-cmd', '--settings=/tmp/test.json', '--force']);
      assert.strictEqual(parsed2.command, 'custom-cmd');
      assert.strictEqual(parsed2.settingsPath, path.resolve('/tmp/test.json'));
      assert.strictEqual(parsed2.force, true);

      const parsed3 = parseArgs(['custom-positional-cmd']);
      assert.strictEqual(parsed3.command, 'custom-positional-cmd');
    });
  });

  // --- Summary & Exit Code ---
  const duration = Date.now() - startTime;
  console.log('\n\x1b[1m=======================================================');
  console.log(`  Tests: \x1b[32m${passedTests} passed\x1b[0m, \x1b[${failedTests > 0 ? '31' : '32'}m${failedTests} failed\x1b[0m, ${totalTests} total`);
  console.log(`  Duration: ${duration}ms`);
  console.log('=======================================================\x1b[0m\n');

  if (failedTests > 0) {
    process.exit(1);
  }
}

runAllTests().catch(err => {
  console.error('Fatal Test Runner Error:', err);
  process.exit(1);
});
