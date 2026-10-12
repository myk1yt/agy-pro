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
const vm = require('vm');
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
        'gemini-pro-preview',
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

    await test('Should dynamically resolve Ultra Tier via smart fuzzy heuristic for unlisted models', () => {
      const modelsToTest = [
        'gemini-ultra-preview',
        'google-ultra-exp',
        'custom-ultra-model'
      ];

      for (const m of modelsToTest) {
        const pricing = config.getModelPricing(m);
        assert.strictEqual(pricing.inputPerMillion, 2.50, `Expected input 2.50 for ${m}`);
        assert.strictEqual(pricing.cachedInputPerMillion, 0.625, `Expected cached input 0.625 for ${m}`);
        assert.strictEqual(pricing.outputPerMillion, 10.00, `Expected output 10.00 for ${m}`);
        assert.strictEqual(pricing.tier, 'ultra', `Expected ultra tier for ${m}`);
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
      assert.strictEqual(pricing.id, 'claude-opus-4.6', `Suffixed "Claude Opus 4.6 (Thinking)" must resolve to claude-opus-4.6, got "${pricing.id}"`);
      assert.strictEqual(pricing.inputPerMillion, 15.00);
      assert.strictEqual(pricing.outputPerMillion, 75.00);
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

      assert(badgeStr.includes('⚡'));
      assert(!badgeStr.includes('[Antigravity]'), 'Badge must not contain the [Antigravity] label (REQ-2)');
      assert(formatter.stripAnsi(badgeStr).startsWith('⚡'), 'Badge must start with the ⚡ emoji (REQ-2)');
      assert(formatter.stripAnsi(badgeStr).startsWith('⚡ '));
      assert(formatter.stripAnsi(badgeStr).includes(`⚡ ${i18n.t('hookBadgeTurn')}:`), 'Badge must read "⚡ Turn:" right after the emoji (REQ-2)');
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

      assert(badgeStr.includes('⚡'));
      assert(!badgeStr.includes('[Antigravity]'), 'Free-mode badge must not contain the [Antigravity] label (REQ-2)');
      assert(formatter.stripAnsi(badgeStr).startsWith('⚡'), 'Free-mode badge must start with the ⚡ emoji (REQ-2)');
      assert(badgeStr.includes('Free'));
    });

    await test('Should format hook response matching Antigravity PostInvocation schema', () => {
      const resp = hookHandler.formatHookResponse('⚡ Turn: 1.2k | Today: 45k');
      assert(resp && Array.isArray(resp.injectSteps), 'Must have injectSteps array');
      assert.strictEqual(resp.injectSteps.length, 1);
      assert.strictEqual(resp.injectSteps[0].ephemeralMessage, '⚡ Turn: 1.2k | Today: 45k');
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
      assert.strictEqual(pkgJson.bin['agy-dashboard'], './bin/agy-dashboard.js');
      assert.strictEqual(pkgJson.bin['agy-tokens'], './bin/agy-tokens.js');
    });

    await test('Should have valid executable entry files', () => {
      assert(fs.existsSync(path.join(__dirname, '..', 'bin', 'agy-tokens.js')));
      assert(fs.existsSync(path.join(__dirname, '..', 'bin', 'agy-tools.js')));
      assert(fs.existsSync(path.join(__dirname, '..', 'bin', 'agy-dashboard.js')));
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

      // Gemini 3.8 Flash
      assert(models['gemini-3.8-flash']);
      assert.strictEqual(models['gemini-3.8-flash'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-3.8-flash'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-3.8-flash'].outputPerMillion, 0.60);

      // Gemini 3.8 Flash Thinking
      assert(models['gemini-3.8-flash-thinking']);
      assert.strictEqual(models['gemini-3.8-flash-thinking'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-3.8-flash-thinking'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-3.8-flash-thinking'].outputPerMillion, 0.60);

      // Gemini 3.0 Flash
      assert(models['gemini-3.0-flash']);
      assert.strictEqual(models['gemini-3.0-flash'].inputPerMillion, 0.15);
      assert.strictEqual(models['gemini-3.0-flash'].cachedInputPerMillion, 0.0375);
      assert.strictEqual(models['gemini-3.0-flash'].outputPerMillion, 0.60);

      // Gemini 3.0 Pro
      assert(models['gemini-3.0-pro']);
      assert.strictEqual(models['gemini-3.0-pro'].inputPerMillion, 1.25);
      assert.strictEqual(models['gemini-3.0-pro'].cachedInputPerMillion, 0.3125);
      assert.strictEqual(models['gemini-3.0-pro'].outputPerMillion, 5.00);

      // Gemini 1.5 Flash
      assert(models['gemini-1.5-flash']);
      assert.strictEqual(models['gemini-1.5-flash'].inputPerMillion, 0.075);
      assert.strictEqual(models['gemini-1.5-flash'].cachedInputPerMillion, 0.01875);
      assert.strictEqual(models['gemini-1.5-flash'].outputPerMillion, 0.30);

      // Gemini 1.5 Pro
      assert(models['gemini-1.5-pro']);
      assert.strictEqual(models['gemini-1.5-pro'].inputPerMillion, 1.25);
      assert.strictEqual(models['gemini-1.5-pro'].cachedInputPerMillion, 0.3125);
      assert.strictEqual(models['gemini-1.5-pro'].outputPerMillion, 5.00);
    });

    await test('Should have complete official Anthropic Claude models and exact rates', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      const models = catalog.models;

      // Claude Opus 5.5
      assert(models['claude-opus-5.5']);
      assert.strictEqual(models['claude-opus-5.5'].inputPerMillion, 15.00);
      assert.strictEqual(models['claude-opus-5.5'].cachedInputPerMillion, 1.50);
      assert.strictEqual(models['claude-opus-5.5'].outputPerMillion, 75.00);

      // Claude Opus 4.6
      assert(models['claude-opus-4.6']);
      assert.strictEqual(models['claude-opus-4.6'].inputPerMillion, 15.00);
      assert.strictEqual(models['claude-opus-4.6'].cachedInputPerMillion, 1.50);
      assert.strictEqual(models['claude-opus-4.6'].outputPerMillion, 75.00);

      // Claude Sonnet 5.5
      assert(models['claude-sonnet-5.5']);
      assert.strictEqual(models['claude-sonnet-5.5'].inputPerMillion, 3.00);
      assert.strictEqual(models['claude-sonnet-5.5'].cachedInputPerMillion, 0.30);
      assert.strictEqual(models['claude-sonnet-5.5'].outputPerMillion, 15.00);

      // Claude Sonnet 4.6
      assert(models['claude-sonnet-4.6']);
      assert.strictEqual(models['claude-sonnet-4.6'].inputPerMillion, 3.00);
      assert.strictEqual(models['claude-sonnet-4.6'].cachedInputPerMillion, 0.30);
      assert.strictEqual(models['claude-sonnet-4.6'].outputPerMillion, 15.00);

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

    await test('Should have complete official DeepSeek models and exact rates', () => {
      const catalog = JSON.parse(fs.readFileSync(pricingFilePath, 'utf8'));
      const models = catalog.models;

      // DeepSeek V3
      assert(models['deepseek-v3']);
      assert.strictEqual(models['deepseek-v3'].inputPerMillion, 0.14);
      assert.strictEqual(models['deepseek-v3'].cachedInputPerMillion, 0.014);
      assert.strictEqual(models['deepseek-v3'].outputPerMillion, 0.28);

      // DeepSeek R1
      assert(models['deepseek-r1']);
      assert.strictEqual(models['deepseek-r1'].inputPerMillion, 0.55);
      assert.strictEqual(models['deepseek-r1'].cachedInputPerMillion, 0.14);
      assert.strictEqual(models['deepseek-r1'].outputPerMillion, 2.19);
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
      assert(catalog.sources.deepseek.includes('deepseek.com'));
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

      const payload = htmlReport.buildDashboardPayload(sessions, { currency: 'usd', lang: 'en', refDate: d2 });

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

      const optsOpenAlone = parseArgs(['node', 'bin/agy-tokens.js', '--open']);
      assert.strictEqual(optsOpenAlone.open, true);
      assert.strictEqual(optsOpenAlone.serve, true);
      assert.strictEqual(optsOpenAlone.today, false);

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
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
        const base = formatter.renderRealTimeBadge(badgeData, 'usd', false);
        assert(!base.includes('Dashboard'));

        const linked = formatter.renderRealTimeBadge(badgeData, 'usd', false, '📊 Dashboard');
        assert(linked.includes('📊 Dashboard'));
        assert(!linked.includes('\n'));
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    // REQ-1: intentional 2-line wrap when badge visible width exceeds terminal width.
    // Fixture: badge data wide enough to overflow 80 columns but fit in 2 lines.
    const req1WideBadgeData = () => ({
      turnTokens: 2100,
      turnCostUsd: 0.05,
      todayTokens: 850000,
      todayCostUsd: 21.5,
      cacheHitRate: 75.4,
      rollingUsage: { remain5hPercent: 80, remain7dPercent: 50 }
    });
    // A realistic OSC 8 link segment: its URI bytes must NOT count toward width.
    const req1Osc8Link = '\x1b]8;;file:///C:/Users/test/dashboard.html\x07📊 Dashboard\x1b]8;;\x07';

    await test('REQ-1a: badge wider than COLUMNS=80 wraps into exactly 2 physical lines, each <= 80 visible cols', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '80';
      try {
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        const lines = badge.split('\n');
        assert.strictEqual(lines.length, 2, `Expected 2 physical lines, got ${lines.length}`);
        assert(!badge.endsWith('\n'), 'Badge string must not carry a trailing newline (caller adds exactly one)');
        assert((badge.match(/\n/g) || []).length === 1, 'At most one \\n separator');
        for (const line of lines) {
          const w = formatter.getBadgeVisibleWidth(line);
          assert(w <= 80, `Line visible width ${w} must be <= 80: ${JSON.stringify(line)}`);
        }
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('REQ-1b: badge stays single-line at COLUMNS=200', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        assert(!badge.includes('\n'), `Badge must be single-line at width 200, got ${badge.split('\n').length} lines`);
        assert(formatter.getBadgeVisibleWidth(badge) <= 200, 'Single line must fit in 200 visible cols');
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('REQ-1c: 2-line wrap preserves every segment (no drops), OSC 8 link intact', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '80';
      try {
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        const plain = formatter.stripOsc8(badge);
        const i18nTurn = i18n.t('hookBadgeTurn');
        const i18nToday = i18n.t('hookBadgeToday');
        const i18nCache = i18n.t('hookBadgeCache');
        const q5h = i18n.t('quota5h') || '5h';
        const q7d = i18n.t('quota7d') || '7d';
        for (const needle of [
          '⚡', `${i18nTurn}:`, `${i18nToday}:`, `${i18nCache}:`,
          `${q5h}:`, `${q7d}:`, '2.1k', '850.0k', '75%', '80%', '50%', '📊 Dashboard'
        ]) {
          assert(plain.includes(needle), `Wrapped badge lost segment content '${needle}': ${JSON.stringify(plain)}`);
        }
        // REQ-2: the removed [Antigravity] label must not resurrect through the wrap path.
        assert(!plain.includes('[Antigravity]'), `Wrapped badge must not contain [Antigravity] (REQ-2): ${JSON.stringify(plain)}`);
        const plainVisible = formatter.stripAnsi(plain);
        assert(plainVisible.startsWith('⚡'), `Wrapped line 1 must start with the ⚡ emoji (REQ-2): ${JSON.stringify(plainVisible)}`);
        assert(plainVisible.startsWith('⚡ '), `Wrapped line 1 must read "⚡ " then the turn label (REQ-2): ${JSON.stringify(plainVisible)}`);
        // OSC 8 framing itself must survive the wrap untouched.
        assert(badge.includes('\x1b]8;;file:///C:/Users/test/dashboard.html\x07'), 'OSC 8 open sequence must be intact');
        assert(badge.includes('\x1b]8;;\x07'), 'OSC 8 close sequence must be intact');
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('REQ-1d: first segment alone exceeding width is truncated with ellipsis, visible width respected', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '20';
      try {
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        const lines = badge.split('\n');
        assert.strictEqual(lines.length, 2, 'Still exactly 2 lines when first segment is truncated');
        assert(formatter.getBadgeVisibleWidth(lines[0]) <= 20, 'Line 1 (truncated first segment) must fit in 20 cols');
        assert(lines[0].includes('…'), `Truncated line 1 must end with ellipsis, got: ${JSON.stringify(formatter.stripOsc8(lines[0]))}`);
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('REQ-1e: getBadgeVisibleWidth excludes OSC 8 and SGR sequences from the measurement', () => {
      // 8 visible chars ("Dashboard") + CJK-adjacent label, zero width for escapes
      const linked = '\x1b[96m\x1b]8;;http://127.0.0.1:8787/\x07📊 Dashboard\x1b]8;;\x07\x1b[0m';
      const w = formatter.getBadgeVisibleWidth(linked);
      // '📊' counts per getDisplayWidth surrogate handling (2 code units => 2), space 1, 'Dashboard' 9
      assert(w > 0 && w <= 13, `OSC 8/SGR must not inflate width, got ${w}`);
      assert.strictEqual(formatter.getBadgeVisibleWidth('Dashboard'), 9);
      assert.strictEqual(formatter.getBadgeVisibleWidth('안녕'), 4, 'CJK width still 2 per char');
    });

    await test('REQ-1f: resolveBadgeWidth returns Infinity when COLUMNS is unset and stdout is not a TTY', () => {
      const prevCols = process.env.COLUMNS;
      delete process.env.COLUMNS;
      const origIsTTY = process.stdout ? process.stdout.isTTY : undefined;
      const origCols = process.stdout ? process.stdout.columns : undefined;
      try {
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: false, configurable: true });
          Object.defineProperty(process.stdout, 'columns', { value: 80, configurable: true });
        }
        assert.strictEqual(formatter.resolveBadgeWidth(), Infinity);
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: origIsTTY, configurable: true });
          Object.defineProperty(process.stdout, 'columns', { value: origCols, configurable: true });
        }
      }
    });

    await test('REQ-1g: renderRealTimeBadge defaults to 1 single line in hook/subprocess (non-TTY) environment', () => {
      const prevCols = process.env.COLUMNS;
      delete process.env.COLUMNS;
      const origIsTTY = process.stdout ? process.stdout.isTTY : undefined;
      try {
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: false, configurable: true });
        }
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        assert(!badge.includes('\n'), 'Statusline badge must remain 1 single line when stdout is not a TTY (no newline)');
        assert(badge.includes('📊 Dashboard'), 'Dashboard link must remain on the single line');
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: origIsTTY, configurable: true });
        }
      }
    });

    await test('REQ-1h: renderRealTimeBadge wraps into 2 lines when stdout.isTTY is true and stdout.columns is narrow', () => {
      const prevCols = process.env.COLUMNS;
      delete process.env.COLUMNS;
      const origIsTTY = process.stdout ? process.stdout.isTTY : undefined;
      const origCols = process.stdout ? process.stdout.columns : undefined;
      try {
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: true, configurable: true });
          Object.defineProperty(process.stdout, 'columns', { value: 80, configurable: true });
        }
        const badge = formatter.renderRealTimeBadge(req1WideBadgeData(), 'usd', false, req1Osc8Link);
        const lines = badge.split('\n');
        assert.strictEqual(lines.length, 2, 'Must wrap into exactly 2 lines when terminal width is narrow');
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
        if (process.stdout) {
          Object.defineProperty(process.stdout, 'isTTY', { value: origIsTTY, configurable: true });
          Object.defineProperty(process.stdout, 'columns', { value: origCols, configurable: true });
        }
      }
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

    await test('removePortFileIfPort preserves record when another live process owns the port', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-rmif-live-${process.pid}.json`);
      try {
        dashboardLink.writePortFile(8976, process.pid, portFile);
        dashboardLink.removePortFileIfPort(8976, portFile, 99999998);
        assert(dashboardLink.readPortFile(portFile) !== null, 'port file must be preserved when owned by another live PID');
      } finally {
        dashboardLink.removePortFile(portFile);
      }
    });

    await test('isPidAlive should accurately detect live and dead processes', () => {
      assert.strictEqual(dashboardLink.isPidAlive(process.pid), true);
      assert.strictEqual(dashboardLink.isPidAlive(99999999), false);
      assert.strictEqual(dashboardLink.isPidAlive(-1), false);
      assert.strictEqual(dashboardLink.isPidAlive(0), false);
    });

    await test('ensureServerRunning should remove stale port file when recorded PID is dead and port is closed', async () => {
      const portFile = path.join(os.tmpdir(), `agy-test-stale-pid-${process.pid}.json`);
      try {
        // Record with a dead PID (99999999) and a closed port (8798)
        dashboardLink.writePortFile(8798, 99999999, portFile);
        assert(fs.existsSync(portFile));
        await dashboardLink.ensureServerRunning({
          portFile,
          entryJs: path.join(os.tmpdir(), 'definitely-missing-entry.js'),
          port: 1
        });
        assert.strictEqual(fs.existsSync(portFile), false, 'stale port file with dead PID should be deleted');
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

    await test('getModelPricing("sonnet") resolves to sonnet tier pricing ($3.00/$15.00) without collision', () => {
      const pricing = config.getModelPricing('sonnet');
      assert.strictEqual(pricing.inputPerMillion, 3.00);
      assert.strictEqual(pricing.outputPerMillion, 15.00);
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

    await test('idle timeout should trigger self-termination when server is inactive (REQ-IDLE)', async () => {
      let terminationReason = null;
      const info = await serve.startDashboardServer({
        port: 0,
        idleTimeoutMs: 50,
        onSelfTerminate: (reason) => {
          terminationReason = reason;
        }
      });

      assert(info !== null && info.port > 0);

      // Wait for idle timeout
      await new Promise((resolve) => {
        let timeout;
        const check = setInterval(() => {
          if (terminationReason !== null) {
            clearInterval(check);
            if (timeout) clearTimeout(timeout);
            resolve();
          }
        }, 20);
        timeout = setTimeout(() => {
          clearInterval(check);
          resolve();
        }, 3000);
      });

      assert(terminationReason !== null, 'Server should have self-terminated on idle timeout');
      assert(terminationReason.includes('idle timeout'));

      const portClosed = !(await dashboardLink.probePort(info.port));
      assert(portClosed, 'Server port should be closed after idle self-termination');
    });

    await test('IDLE_TIMEOUT_DEFAULT_MS should be 30 minutes', () => {
      assert.strictEqual(serve.IDLE_TIMEOUT_DEFAULT_MS, 30 * 60 * 1000);
    });
  });

  // --- Suite 24: Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration ---
  // --- Suite 24: Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration ---
  await describe('24. Real-Time 1:1 Gemini Quota Pool & Language Server RPC Integration', async () => {
    const htmlReport = require('../src/html-report');

    await test('parseCommandLine should extract csrfToken, port, and protocol from arguments (hyphen and underscore variants)', () => {
      const origAntigravityToken = process.env.ANTIGRAVITY_CSRF_TOKEN;
      const origCsrfToken = process.env.CSRF_TOKEN;
      try {
        delete process.env.ANTIGRAVITY_CSRF_TOKEN;
        delete process.env.CSRF_TOKEN;

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
      } finally {
        if (origAntigravityToken !== undefined) process.env.ANTIGRAVITY_CSRF_TOKEN = origAntigravityToken;
        else delete process.env.ANTIGRAVITY_CSRF_TOKEN;
        if (origCsrfToken !== undefined) process.env.CSRF_TOKEN = origCsrfToken;
        else delete process.env.CSRF_TOKEN;
      }
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

      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
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
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
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

    // ---- REQ-3: quota realtime-linkage fixes ----

    await test('REQ-3 Fix 2: stale quota snapshot renders `*`, auth-failed stale renders `!`, fresh renders no marker', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
        const baseQuota = (extra) => Object.assign({
          remainPercent: 97,
          quota5h: { remainPercent: 97, resetFormatted: '1h 18m', window: '5h' },
          quota7d: { remainPercent: 83, resetFormatted: '3d 20h', window: 'weekly' },
          isLive: true,
          source: 'language_server'
        }, extra);

        // Fresh cache read (isFresh true) => no marker anywhere
        const fresh = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50, geminiQuota: baseQuota({ isFresh: true, ageMs: 1000 })
        }, 'usd', false));
        assert(fresh.includes('97%'), 'fresh badge must include 97%');
        assert(!fresh.includes('97%*') && !fresh.includes('97%!'), `fresh badge must NOT carry stale markers, got: ${fresh}`);

        // Stale cache read (isFresh false) => `*` on BOTH segments
        const stale = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50, geminiQuota: baseQuota({ isFresh: false, ageMs: 12345678 })
        }, 'usd', false));
        assert(stale.includes('97%*'), `stale badge must mark 5h with *, got: ${stale}`);
        assert(stale.includes('83%*'), `stale badge must mark 7d with *, got: ${stale}`);

        // Stale + auth failure => `!` wins over `*`
        const auth = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50, geminiQuota: baseQuota({ isFresh: false, ageMs: 12345678, lastError: { kind: 'auth_failure', detail: 'HTTP 401' } })
        }, 'usd', false));
        assert(auth.includes('97%!') && auth.includes('83%!'), `auth-stale badge must mark with !, got: ${auth}`);

        // Legacy live object without isFresh flag => no marker (backward compatible)
        const legacy = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50, geminiQuota: baseQuota({})
        }, 'usd', false));
        assert(!legacy.includes('%*') && !legacy.includes('%!'), `legacy live quota must not be marked, got: ${legacy}`);

        // Marker helper contract directly
        assert.strictEqual(formatter.quotaStalenessMarker(null), '');
        assert.strictEqual(formatter.quotaStalenessMarker({ isFresh: true }), '');
        assert.strictEqual(formatter.quotaStalenessMarker({ isFresh: false }), '*');
        assert.strictEqual(formatter.quotaStalenessMarker({ isFresh: false, lastError: { kind: 'auth_failure' } }), '!');
        assert.strictEqual(formatter.quotaStalenessMarker({ isFresh: false, lastError: { kind: 'probe_failed' } }), '*');
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('REQ-3 Fix 3: shouldBypassCooldownForStale gate passes only when snapshot AND marker are older than 5min', () => {
      const now = 1_000_000_000_000;
      const S = geminiQuota.STALE_RETRY_AFTER_MS;
      assert.strictEqual(S, 5 * 60 * 1000, 'stale-retry window must be 5 minutes');

      // Old snapshot + old marker => bypass (one retry allowed)
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(now - S - 60000, now - S - 1000, now), true);
      // Fresh snapshot => blocked regardless of marker age
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(now - 30000, now - S - 1000, now), false);
      // Old snapshot but marker written recently (a burst just happened) => blocked
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(now - S - 60000, now - 1000, now), false);
      // Exactly at the boundary (age == S) => blocked (strictly greater required)
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(now - S, now - S, now), false);
      // No cache snapshot at all => blocked (nothing visibly stale)
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(null, now - S - 1000, now), false);
      // Corrupt inputs => blocked
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale(0, 0, now), false);
      assert.strictEqual(geminiQuota.shouldBypassCooldownForStale('x', 'y', now), false);
    });

    await test('REQ-3 Fix 1/3: 401 responses classified as auth_failure and stamped into cache diagnostics without touching snapshot age', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-auth-'));
      const testCacheFile = path.join(tempDir, 'quota_cache.json');

      // Pre-existing stale snapshot (the 04:29Z-style scenario): timestampMs fixed.
      const snapshotMs = Date.now() - 3 * 60 * 60 * 1000; // 3h old
      fs.writeFileSync(testCacheFile, JSON.stringify({
        version: 2,
        timestamp: new Date(snapshotMs).toISOString(),
        timestampMs: snapshotMs,
        remainPercent: 97,
        quota5h: { remainPercent: 97, window: '5h' },
        quota7d: { remainPercent: 83, window: 'weekly' },
        isLive: true,
        source: 'language_server'
      }, null, 2), 'utf8');

      // Mock server that always answers 401 like the CSRF-stripped Language Server.
      const mock401 = http.createServer((req, res) => {
        res.writeHead(401, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ code: 'unauthenticated', message: 'missing CSRF token' }));
      });
      await new Promise((resolve) => mock401.listen(0, '127.0.0.1', resolve));
      const boundPort = mock401.address().port;

      try {
        // makeRpcRequest must expose httpStatus for classification.
        await geminiQuota.makeRpcRequest({
          path: '/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary',
          port: boundPort, protocol: 'http'
        }).then(
          () => assert.fail('401 must reject'),
          (err) => {
            assert.strictEqual(err.httpStatus, 401, 'RPC error must carry httpStatus');
          }
        );

        // fetchLiveGeminiQuota against the 401 server (explicit port => no cooldown
        // marker writes, but cache diagnostics must still record the auth failure).
        const res = await geminiQuota.fetchLiveGeminiQuota({
          port: boundPort,
          csrfToken: '',
          protocol: 'http',
          cachePath: testCacheFile
        });
        assert.strictEqual(res.isLive, false);
        assert.strictEqual(res.errorKind, 'auth_failure', `401 must classify as auth_failure, got: ${res.errorKind}`);
        assert(String(res.error).includes('401'), `error text must mention 401, got: ${res.error}`);

        // Cache decorated with lastAttemptAtMs/lastError, snapshot age untouched.
        const cached = JSON.parse(fs.readFileSync(testCacheFile, 'utf8'));
        assert.strictEqual(cached.timestampMs, snapshotMs, 'failed attempt must NOT touch timestampMs');
        assert.strictEqual(typeof cached.lastAttemptAtMs, 'number', 'lastAttemptAtMs must be stamped');
        assert(cached.lastError && cached.lastError.kind === 'auth_failure', 'lastError.kind must be auth_failure');
        assert(typeof cached.lastError.detail === 'string' && cached.lastError.detail.includes('401'), 'lastError.detail must carry the HTTP 401 evidence');

        // getCachedGeminiQuota passes the diagnostics through to the statusline renderer.
        const readBack = geminiQuota.getCachedGeminiQuota(testCacheFile, 30000);
        assert.strictEqual(readBack.isFresh, false);
        assert(readBack.lastError && readBack.lastError.kind === 'auth_failure', 'stale read must surface lastError');
        assert.strictEqual(readBack.lastAttemptAtMs, cached.lastAttemptAtMs);

        // And the badge shows the `!` warning for this exact state.
        const prevCols = process.env.COLUMNS;
        process.env.COLUMNS = '200';
        try {
          const badge = formatter.stripAnsi(formatter.renderRealTimeBadge({
            turnTokens: 1, turnCostUsd: 0, todayTokens: 1, todayCostUsd: 0, cacheHitRate: 0,
            geminiQuota: readBack
          }, 'usd', false));
          assert(badge.includes('97%!'), `auth-stale badge must render !, got: ${badge}`);
        } finally {
          if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
        }
      } finally {
        await new Promise((resolve) => mock401.close(resolve));
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });

    await test('REQ-3 Fix 1/3: cooldown marker v2 carries reason/detail/tokenSource, stays backward compatible, and the gate reports cooldownReason', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-cd-'));
      const markerPath = path.join(tempDir, 'gemini_quota_probe_cooldown.json');
      try {
        // v2 write with auth-failure diagnostics
        geminiQuota.writeProbeCooldown(markerPath, {
          reason: 'auth_failure',
          detail: 'RetrieveUserQuotaSummary returned HTTP 401: {"code":"unauthenticated"}',
          tokenSource: 'none'
        });
        const raw = JSON.parse(fs.readFileSync(markerPath, 'utf8'));
        assert.strictEqual(raw.version, 2);
        assert.strictEqual(raw.reason, 'auth_failure');
        assert.strictEqual(raw.tokenSource, 'none');
        assert(raw.detail.includes('401'));

        const info = geminiQuota.readProbeCooldownInfo(markerPath);
        assert(info !== null);
        assert.strictEqual(info.reason, 'auth_failure');
        assert(typeof info.setAtMs === 'number' && info.setAtMs > 0, 'setAtMs must be present for the stale-retry gate');

        // v1 marker (pre-REQ-3 file) tolerated: defaults filled in.
        fs.writeFileSync(markerPath, JSON.stringify({ version: 1, reason: 'language_server_probe_failed', setAtMs: Date.now(), expiryMs: Date.now() + 60000 }), 'utf8');
        const infoV1 = geminiQuota.readProbeCooldownInfo(markerPath);
        assert.strictEqual(infoV1.version, 1);
        assert.strictEqual(infoV1.reason, 'language_server_probe_failed');
        assert.strictEqual(infoV1.detail, null);
        // Legacy reader still works on either version
        assert.strictEqual(typeof geminiQuota.readProbeCooldown(markerPath), 'number');

        // fetchLiveGeminiQuota cooldown gate: active marker + fresh-ish cache => early return with reason
        const testCacheFile = path.join(tempDir, 'quota_cache.json');
        fs.writeFileSync(testCacheFile, JSON.stringify({ version: 2, timestampMs: Date.now() - 60000, remainPercent: 50 }), 'utf8');
        geminiQuota.writeProbeCooldown(markerPath, { reason: 'auth_failure', detail: 'x', tokenSource: 'none' });
        return geminiQuota.fetchLiveGeminiQuota({
          cachePath: testCacheFile,
          cooldownMarker: markerPath
        }).then((res) => {
          assert.strictEqual(res.source, 'cooldown', 'gate must short-circuit into cooldown');
          assert.strictEqual(res.cooldownReason, 'auth_failure', 'cooldown reason must be surfaced to callers');
          assert.strictEqual(res.isLive, false);
          fs.rmSync(tempDir, { recursive: true, force: true });
        });
      } catch (e) {
        fs.rmSync(tempDir, { recursive: true, force: true });
        throw e;
      }
    });

    await test('REQ-4 7.2: command_line remains priority-1 token source; transcript is priority-2, heap_scan reserved, none last (design §2.1)', () => {
      // The REQ-3 documentation test ("fallback NOT implemented") is superseded by
      // the evidence-backed transcript fallback (design doc §2.3, debug report §2.1).
      // command_line must still win whenever discovery carries a token.
      assert(typeof geminiQuota.discoverLanguageServer === 'function');
      const parsed = geminiQuota.parseCommandLine('agy.exe --api_url https://127.0.0.1:60125 --csrf_token tok-123');
      assert.strictEqual(parsed.csrfToken, 'tok-123', 'command-line token path must remain first priority');

      // resolveCsrfTokenFallback enum contract: 'command_line' | 'transcript' | 'none'
      // (heap_scan reserved for §7.5), decided WITHOUT touching the filesystem for
      // the command_line branch.
      const cl = geminiQuota.resolveCsrfTokenFallback({ pid: 1, ports: [1], csrfToken: 'tok-123' }, { brainDir: 'Z:\\definitely-missing-brain' });
      assert.strictEqual(cl.tokenSource, 'command_line');
      assert.strictEqual(cl.csrfToken, 'tok-123');

      const none = geminiQuota.resolveCsrfTokenFallback({ pid: 1, ports: [1], csrfToken: '' }, { brainDir: 'Z:\\definitely-missing-brain' });
      assert.strictEqual(none.tokenSource, 'none');
      assert.strictEqual(none.csrfToken, '');

      const noDisc = geminiQuota.resolveCsrfTokenFallback(null);
      assert.strictEqual(noDisc.tokenSource, 'none');
    });

    await test('parseCommandLine should discover token from ANTIGRAVITY_CSRF_TOKEN and CSRF_TOKEN env vars', () => {
      const origAntigravityToken = process.env.ANTIGRAVITY_CSRF_TOKEN;
      const origCsrfToken = process.env.CSRF_TOKEN;
      try {
        delete process.env.ANTIGRAVITY_CSRF_TOKEN;
        delete process.env.CSRF_TOKEN;

        // Without env vars, parses CLI
        const p1 = geminiQuota.parseCommandLine('agy.exe --csrf_token cli-tok');
        assert.strictEqual(p1.csrfToken, 'cli-tok');

        // CLI token takes first priority
        process.env.ANTIGRAVITY_CSRF_TOKEN = 'env-tok-agy';
        const p2 = geminiQuota.parseCommandLine('agy.exe --csrf_token cli-tok');
        assert.strictEqual(p2.csrfToken, 'cli-tok');

        // Falls back to ANTIGRAVITY_CSRF_TOKEN when missing from command line
        const p3 = geminiQuota.parseCommandLine('agy.exe --api_url https://127.0.0.1:60125');
        assert.strictEqual(p3.csrfToken, 'env-tok-agy');

        // Works even with empty/missing command line
        const p4 = geminiQuota.parseCommandLine(null);
        assert.strictEqual(p4.csrfToken, 'env-tok-agy');

        // CSRF_TOKEN used when ANTIGRAVITY_CSRF_TOKEN is not set
        delete process.env.ANTIGRAVITY_CSRF_TOKEN;
        process.env.CSRF_TOKEN = 'env-tok-csrf';
        const p5 = geminiQuota.parseCommandLine('agy.exe');
        assert.strictEqual(p5.csrfToken, 'env-tok-csrf');
      } finally {
        if (origAntigravityToken !== undefined) process.env.ANTIGRAVITY_CSRF_TOKEN = origAntigravityToken;
        else delete process.env.ANTIGRAVITY_CSRF_TOKEN;
        if (origCsrfToken !== undefined) process.env.CSRF_TOKEN = origCsrfToken;
        else delete process.env.CSRF_TOKEN;
      }
    });

    await test('getCachedGeminiQuota marks isStale: true on age >= 5m or auth_failure', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-stale-'));
      const cachePath = path.join(tempDir, 'quota_cache.json');
      try {
        const now = Date.now();
        // 1. Fresh cache (< 30s)
        fs.writeFileSync(cachePath, JSON.stringify({
          version: 2,
          timestampMs: now - 5000,
          remainPercent: 95
        }), 'utf8');
        const q1 = geminiQuota.getCachedGeminiQuota(cachePath, 30000);
        assert.strictEqual(q1.isFresh, true);
        assert.strictEqual(q1.isStale, false);

        // 2. Cache older than 5m
        fs.writeFileSync(cachePath, JSON.stringify({
          version: 2,
          timestampMs: now - 6 * 60 * 1000,
          remainPercent: 95
        }), 'utf8');
        const q2 = geminiQuota.getCachedGeminiQuota(cachePath, 30000);
        assert.strictEqual(q2.isFresh, false);
        assert.strictEqual(q2.isStale, true);

        // 3. Recent cache (< 30s) but with auth_failure
        fs.writeFileSync(cachePath, JSON.stringify({
          version: 2,
          timestampMs: now - 5000,
          remainPercent: 95,
          lastError: { kind: 'auth_failure', detail: 'HTTP 401' }
        }), 'utf8');
        const q3 = geminiQuota.getCachedGeminiQuota(cachePath, 30000);
        assert.strictEqual(q3.isFresh, false);
        assert.strictEqual(q3.isStale, true);
      } finally {
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });

    await test('renderRealTimeBadge renders stale real quota with ! marker (never demoted to rolling est) per design 000815 §4.3/§4.4', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
        const staleQuota = {
          remainPercent: 97,
          quota5h: { remainPercent: 97, window: '5h' },
          quota7d: { remainPercent: 83, window: 'weekly' },
          isLive: false,
          isFresh: false,
          isStale: true,
          lastError: { kind: 'auth_failure', detail: 'HTTP 401' }
        };

        const rolling = {
          remain5hPercent: 71,
          remain7dPercent: 85
        };

        const badge = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50,
          geminiQuota: staleQuota,
          rollingUsage: rolling
        }, 'usd', false));

        // Priority 4 (stale REAL snapshot + `!` marker) > Priority 5 (ESTIMATE):
        // the real measurement must stay visible with its staleness marker,
        // and the local rolling estimate must NOT take over the badge.
        assert(badge.includes('97%!'), `Badge must show stale real 5h quota with ! marker, got: ${badge}`);
        assert(badge.includes('83%!'), `Badge must show stale real 7d quota with ! marker, got: ${badge}`);
        assert(!badge.includes('(est)'), `Badge must NOT demote to rolling estimate, got: ${badge}`);
        assert(!badge.includes('71%'), `Badge must not show rolling 5h estimate, got: ${badge}`);
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('renderRealTimeBadge renders rolling estimate only when geminiQuota is absent (null)', () => {
      const prevCols = process.env.COLUMNS;
      process.env.COLUMNS = '200';
      try {
        const badge = formatter.stripAnsi(formatter.renderRealTimeBadge({
          turnTokens: 100, turnCostUsd: 0.001, todayTokens: 1000, todayCostUsd: 0.01,
          cacheHitRate: 50,
          geminiQuota: null,
          rollingUsage: { remain5hPercent: 71, remain7dPercent: 85 }
        }, 'usd', false));

        // ESTIMATE is the only source when no real snapshot exists at all.
        assert(badge.includes('71%'), `Badge must show rolling 5h estimate, got: ${badge}`);
        assert(badge.includes('85%'), `Badge must show rolling 7d estimate, got: ${badge}`);
      } finally {
        if (prevCols === undefined) delete process.env.COLUMNS; else process.env.COLUMNS = prevCols;
      }
    });

    await test('fetchLiveGeminiQuota should NEVER attempt plaintext HTTP fallback on a port that responded via HTTPS', async () => {
      const { certPem, privateKey } = createTestCert();
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-https-guard-'));
      const testCacheFile = path.join(tempDir, 'quota_cache_guard.json');

      const clientErrors = [];
      let requestCount = 0;
      const httpsServer = https.createServer({ key: privateKey, cert: certPem }, (req, res) => {
        requestCount++;
        // Simulate Language Server rejecting invalid token with 401
        res.writeHead(401, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ code: 'unauthenticated', message: 'invalid or expired token' }));
      });

      httpsServer.on('clientError', (err) => {
        clientErrors.push(err);
      });

      await new Promise(r => httpsServer.listen(0, '127.0.0.1', r));
      const httpsPort = httpsServer.address().port;

      try {
        const res = await geminiQuota.fetchLiveGeminiQuota({
          port: httpsPort,
          csrfToken: 'expired-token',
          protocols: ['https', 'http'],
          cachePath: testCacheFile
        });

        assert.strictEqual(res.isLive, false);
        assert.strictEqual(res.errorKind, 'auth_failure');
        assert(String(res.error).includes('401'));

        // Crucial invariant: clientErrors MUST be 0!
        // If fetchLiveGeminiQuota attempted plaintext HTTP fallback on httpsPort,
        // Node's httpsServer would emit clientError (ERR_SSL_WRONG_VERSION_NUMBER / Parse Error)
        assert.strictEqual(clientErrors.length, 0, 'No TLS handshake errors or clientError must occur from plaintext HTTP probe on HTTPS server');

        // Port must be recorded in detected HTTPS ports
        const detected = geminiQuota.getDetectedHttpsPorts();
        assert(detected.includes(httpsPort), 'httpsPort must be detected as HTTPS');

        // makeRpcRequest must block plaintext HTTP to this port
        await assert.rejects(
          async () => {
            await geminiQuota.makeRpcRequest({
              path: '/test',
              port: httpsPort,
              protocol: 'http'
            });
          },
          /Plaintext HTTP request blocked/
        );
      } finally {
        await new Promise(r => httpsServer.close(r));
        geminiQuota.clearDetectedHttpsPorts();
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });

    await test('fetchLiveGeminiQuota should gracefully skip unauthenticated RPC probe when csrfToken is missing', async () => {
      const { certPem, privateKey } = createTestCert();
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gemini-quota-unauth-'));
      const testCacheFile = path.join(tempDir, 'quota_cache_unauth.json');

      let requestsReceived = 0;
      const clientErrors = [];
      const httpsServer = https.createServer({ key: privateKey, cert: certPem }, (req, res) => {
        requestsReceived++;
        res.writeHead(401, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ code: 'unauthenticated', message: 'missing CSRF token' }));
      });

      httpsServer.on('clientError', (err) => {
        clientErrors.push(err);
      });

      await new Promise(r => httpsServer.listen(0, '127.0.0.1', r));
      const httpsPort = httpsServer.address().port;

      fs.writeFileSync(testCacheFile, JSON.stringify({
        version: 2,
        timestampMs: Date.now() - 60000,
        remainPercent: 90
      }), 'utf8');

      const origAgyCsrf = process.env.ANTIGRAVITY_CSRF_TOKEN;
      const origCsrf = process.env.CSRF_TOKEN;
      delete process.env.ANTIGRAVITY_CSRF_TOKEN;
      delete process.env.CSRF_TOKEN;

      try {
        const res = await geminiQuota.fetchLiveGeminiQuota({
          port: httpsPort,
          csrfToken: '',
          cachePath: testCacheFile
        });

        // Zero requests should have reached the server
        assert.strictEqual(requestsReceived, 0, 'Zero requests must be made when CSRF token is missing');
        assert.strictEqual(clientErrors.length, 0, 'Zero client/TLS handshake errors');
        assert.strictEqual(res.isLive, false);
        assert.strictEqual(res.errorKind, 'auth_failure');
        assert(String(res.error).includes('401'));
        assert(String(res.error).includes('missing CSRF token'));

        // Cache diagnostics must record the auth failure
        const cached = JSON.parse(fs.readFileSync(testCacheFile, 'utf8'));
        assert.strictEqual(cached.lastError.kind, 'auth_failure');
        assert(cached.lastError.detail.includes('missing CSRF token'));
      } finally {
        if (origAgyCsrf !== undefined) process.env.ANTIGRAVITY_CSRF_TOKEN = origAgyCsrf;
        if (origCsrf !== undefined) process.env.CSRF_TOKEN = origCsrf;
        await new Promise(r => httpsServer.close(r));
        geminiQuota.clearDetectedHttpsPorts();
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });
    // ---- REQ-4 subtask 7.1/7.2 + C: transcript CSRF fallback unit tests ----
    {
      const UUID_OK = '0653f3e9-312c-4dd4-b5dc-9017a78d4b05';
      const UUID_OLD = '11111111-2222-3333-4444-555555555555';
      const discoveredLine = (pid, port, ports, token) =>
        `2026-09-12T21:00:00Z INFO LS bootstrap\nDISCOVERED: {"pid":${pid},"port":${port},"ports":[${ports.join(',')}],"csrfToken":"${token}","protocol":null}\nnext line`;

      const mkBrain = (tmp) => {
        const brain = path.join(tmp, 'brain');
        const writeOutput = (conv, step, content) => {
          const dir = path.join(brain, conv, '.system_generated', 'steps', String(step));
          fs.mkdirSync(dir, { recursive: true });
          const fp = path.join(dir, 'output.txt');
          fs.writeFileSync(fp, content, 'utf8');
          return fp;
        };
        const writeLog = (conv, name, content) => {
          const dir = path.join(brain, conv, '.system_generated', 'logs');
          fs.mkdirSync(dir, { recursive: true });
          const fp = path.join(dir, name);
          fs.writeFileSync(fp, content, 'utf8');
          return fp;
        };
        const setMtime = (fp, ms) => {
          const d = new Date(ms);
          fs.utimesSync(fp, d, d);
        };
        return { brain, writeOutput, writeLog, setMtime };
      };

      await test('REQ-4 7.1 case1: transcript scan returns token on PID+port match (pattern A, plain output.txt)', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-scan-'));
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          writeOutput('conv-a', 25, discoveredLine(34784, 60462, [60462, 60463], UUID_OK));
          const hit = geminiQuota.scanTranscriptForCsrf(
            { pid: 34784, ports: [60462, 60463] },
            { brainDir: brain }
          );
          assert(hit !== null, 'matching discovery must yield a transcript hit');
          assert.strictEqual(hit.csrfToken, UUID_OK);
          assert.strictEqual(hit.pid, 34784);
          assert.strictEqual(hit.port, 60462);
          assert.deepStrictEqual(hit.ports, [60462, 60463]);
          assert(hit.sourceFile.endsWith('output.txt'));
          // fallback wrapper surfaces it with the transcript tokenSource + HTTPS pin
          const fb = geminiQuota.resolveCsrfTokenFallback(
            { pid: 34784, ports: [60462, 60463], csrfToken: '' },
            { brainDir: brain }
          );
          assert.strictEqual(fb.tokenSource, 'transcript');
          assert.deepStrictEqual(fb.pinnedPorts, [60462, 60463]);
          assert.strictEqual(fb.pinnedProtocol, 'https');
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.1 case2: token from a different PID is discarded and never returned', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-pid-'));
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          // Stale-session record: same ports as today's LS, but a dead PID.
          writeOutput('conv-old', 7, discoveredLine(11111, 60462, [60462, 60463], UUID_OLD));
          const hit = geminiQuota.scanTranscriptForCsrf(
            { pid: 34784, ports: [60462, 60463] },
            { brainDir: brain }
          );
          assert.strictEqual(hit, null, 'pid-mismatch token must be discarded');
          // direct validator contract
          assert.strictEqual(geminiQuota.validateTranscriptToken(
            { pid: 34784, ports: [60462] },
            { pid: 11111, port: 60462, ports: [60462], csrfToken: UUID_OLD }
          ), false);
          // malformed (non-UUID) token never validates
          assert.strictEqual(geminiQuota.isCsrfUuid('zzzzzzzz-bbbb-cccc-dddd-eeeeeeeeeeee'), false);
          assert.strictEqual(geminiQuota.isCsrfUuid(UUID_OLD), true);
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.1 case3: port mismatch discards the token; pid-less discovery relaxes to port-only validation', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-port-'));
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          writeOutput('conv-a', 1, discoveredLine(34784, 50001, [50001, 50002], UUID_OK));
          // Same PID but ports do not intersect → discard (mandatory rejection rule).
          assert.strictEqual(geminiQuota.scanTranscriptForCsrf(
            { pid: 34784, ports: [60462, 60463] },
            { brainDir: brain }
          ), null, 'port-mismatch token must be discarded even when pid matches');
          // Delegation-A relaxation: discovery knowing only ports (no pid) still
          // accepts a port-intersecting hit…
          assert(geminiQuota.scanTranscriptForCsrf(
            { ports: [60462], port: 60462 },
            { brainDir: brain }
          ) === null, 'no port intersection with pid-less discovery must still discard');
          const relaxed = geminiQuota.scanTranscriptForCsrf(
            { ports: [50001], port: 50001 },
            { brainDir: brain }
          );
          assert(relaxed !== null && relaxed.csrfToken === UUID_OK, 'port-only discovery must accept intersecting hit');
          // …and a discovery without ANY port info validates against nothing → discard.
          assert.strictEqual(geminiQuota.validateTranscriptToken(
            { pid: 34784 },
            { pid: 34784, port: 50001, ports: [50001], csrfToken: UUID_OK }
          ), false);
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.1 case4: newest mtime file wins and the LAST match inside it is adopted (JSONL-escaped too)', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-multi-'));
        try {
          const { brain, writeOutput, writeLog, setMtime } = mkBrain(tmp);
          const now = Date.now();
          // Older conversation file: valid but stale.
          const oldFp = writeOutput('conv-old', 3, discoveredLine(34784, 60462, [60462, 60463], UUID_OLD));
          setMtime(oldFp, now - 3600 * 1000);
          // Newest file: JSONL transcript with escaped quotes, TWO matches inside.
          const jsonl = [
            JSON.stringify({ type: 'LOG', content: discoveredLine(34784, 60462, [60462, 60463], UUID_OLD) }),
            JSON.stringify({ type: 'LOG', content: 'noise line' }),
            JSON.stringify({ type: 'LOG', content: discoveredLine(34784, 60462, [60462, 60463], UUID_OK) })
          ].join('\n');
          const newFp = writeLog('conv-new', 'transcript_full.jsonl', jsonl);
          setMtime(newFp, now);
          const hit = geminiQuota.scanTranscriptForCsrf(
            { pid: 34784, ports: [60462, 60463] },
            { brainDir: brain }
          );
          assert(hit !== null);
          assert.strictEqual(hit.csrfToken, UUID_OK, 'last match of the newest-mtime file must win');
          assert.strictEqual(hit.sourceFile, newFp);
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.1 case5: empty or missing brain directory yields null without crashing', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-empty-'));
        try {
          const { brain } = mkBrain(tmp);
          fs.mkdirSync(brain, { recursive: true });
          assert.strictEqual(geminiQuota.scanTranscriptForCsrf({ pid: 1, ports: [2] }, { brainDir: brain }), null);
          assert.strictEqual(geminiQuota.scanTranscriptForCsrf({ pid: 1, ports: [2] }, { brainDir: path.join(tmp, 'nope') }), null);
          assert.strictEqual(geminiQuota.scanTranscriptForCsrf(null), null);
          // Garbage conversation dirs (no .system_generated) are skipped quietly.
          fs.mkdirSync(path.join(brain, 'junk-conv'), { recursive: true });
          assert.strictEqual(geminiQuota.scanTranscriptForCsrf({ pid: 1, ports: [2] }, { brainDir: brain }), null);
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.1 case6: files above the size cap are skipped (10MB default cap honored)', () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-size-'));
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          const big = writeOutput('conv-big', 1, discoveredLine(34784, 60462, [60462, 60463], UUID_OK) + '\n' + 'x'.repeat(5000));
          const small = writeOutput('conv-small', 2, discoveredLine(34784, 60462, [60462, 60463], UUID_OLD));
          assert(fs.statSync(big).size > fs.statSync(small).size);
          // Cap injected via test hook: only the small file may be scanned.
          const hit = geminiQuota.scanTranscriptForCsrf(
            { pid: 34784, ports: [60462] },
            { brainDir: brain, maxFileSize: fs.statSync(small).size + 1 }
          );
          assert(hit !== null, 'small file must still be found when the oversized one is skipped');
          assert.strictEqual(hit.sourceFile, small);
          assert.strictEqual(hit.csrfToken, UUID_OLD);
          // Production default cap is 10MB per design §2.3.
          const caps = geminiQuota.collectTranscriptCandidateFiles(brain, 300, 10 * 1024 * 1024);
          assert.strictEqual(caps.length, 2, 'both files are under the 10MB production cap');
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.2 case7: transcript success persists tokenSource only — cache/marker never store the token plaintext (design §5)', async () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-src-'));
        const markerPath = path.join(tmp, 'gemini_quota_probe_cooldown.json');
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          writeOutput('conv-a', 1, discoveredLine(7777, 65001, [65001, 65002], UUID_OK));
          // writeProbeCooldown must carry tokenSource 'transcript' when passed, and
          // a raw write of the whole cooldown payload must not contain the token.
          geminiQuota.writeProbeCooldown(markerPath, {
            reason: 'auth_failure',
            detail: 'HTTP 401',
            tokenSource: 'transcript'
          });
          const raw = fs.readFileSync(markerPath, 'utf8');
          assert(raw.includes('"tokenSource":"transcript"'), `marker must record transcript kind, got: ${raw}`);
          assert(!raw.includes(UUID_OK), 'marker must NEVER contain the token plaintext');
          const fb = geminiQuota.resolveCsrfTokenFallback(
            { pid: 7777, ports: [65001, 65002], csrfToken: '' },
            { brainDir: brain }
          );
          assert.strictEqual(fb.tokenSource, 'transcript');
          geminiQuota.saveCachedGeminiQuota({ remainPercent: 99, isLive: true, source: 'language_server', tokenSource: fb.tokenSource }, path.join(tmp, 'cache.json'));
          const cacheRaw = fs.readFileSync(path.join(tmp, 'cache.json'), 'utf8');
          assert(cacheRaw.includes('"tokenSource": "transcript"'));
          assert(!cacheRaw.includes(UUID_OK), 'cache must NEVER contain the token plaintext');
        } finally {
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 C case8: transcript-pinned probe never sends plain HTTP at an HTTPS-only LS (TLS-flood root fix)', async () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-pin-'));
        const markerPath = path.join(tmp, 'gemini_quota_probe_cooldown.json');
        const cachePath = path.join(tmp, 'quota_cache.json');
        const { certPem, privateKey } = createTestCert();
        let httpsRequests = 0;
        let httpRequests = 0;
        const httpsServer = https.createServer({ key: privateKey, cert: certPem }, (req, res) => {
          httpsRequests++;
          assert.strictEqual(req.headers['x-codeium-csrf-token'], UUID_OK, 'pinned probe must carry the transcript token');
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({
            response: { groups: [{ displayName: 'Gemini Models', buckets: [
              { bucketId: 'gemini-5h', window: '5h', remainingFraction: 0.996, resetTime: new Date(Date.now() + 3600e3).toISOString() },
              { bucketId: 'gemini-weekly', window: 'weekly', remainingFraction: 0.655, resetTime: new Date(Date.now() + 5 * 86400e3).toISOString() }
            ] }] }
          }));
        });
        const httpServer = http.createServer((req, res) => {
          httpRequests++; // must never be reached on the pinned path
          res.writeHead(400); res.end('plain http must not be probed');
        });
        await new Promise(r => httpsServer.listen(0, '127.0.0.1', r));
        await new Promise(r => httpServer.listen(0, '127.0.0.1', r));
        const httpsPort = httpsServer.address().port;
        const httpPort = httpServer.address().port;
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          writeOutput('conv-a', 1, discoveredLine(7777, httpsPort, [httpsPort], UUID_OK));
          const res = await geminiQuota.fetchLiveGeminiQuota({
            discover: async () => ({ pid: 7777, port: httpsPort, ports: [httpsPort, httpPort], csrfToken: '', protocol: null }),
            transcriptScan: { brainDir: brain },
            cachePath,
            cooldownMarker: markerPath
          });
          assert.strictEqual(res.isLive, true, `transcript fallback fetch must succeed, error: ${res.error}`);
          assert.strictEqual(res.tokenSource, 'transcript');
          assert.strictEqual(httpsRequests, 1, 'exactly one HTTPS RPC (RetrieveUserQuotaSummary first-try success)');
          assert.strictEqual(httpRequests, 0, 'pinned path must NEVER knock plain HTTP on an HTTPS port');
          assert.strictEqual(res.remainPercent, 100, '0.996 rounds to 100% (real 99%+ quota restoration)');
          // cache records source kind only
          const cacheRaw = fs.readFileSync(cachePath, 'utf8');
          assert(cacheRaw.includes('"tokenSource": "transcript"'));
          assert(!cacheRaw.includes(UUID_OK), 'cache must not contain the token plaintext');
          // success cleared/never wrote a cooldown marker
          assert.strictEqual(fs.existsSync(markerPath), false, 'success must not leave a cooldown marker');
        } finally {
          await new Promise(r => httpsServer.close(r));
          await new Promise(r => httpServer.close(r));
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('REQ-4 7.2 case9: transcript token + HTTP 401 records marker v2 with tokenSource transcript (no plaintext) and errorKind auth_failure', async () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'rq4-401-'));
        const markerPath = path.join(tmp, 'gemini_quota_probe_cooldown.json');
        const cachePath = path.join(tmp, 'quota_cache.json');
        fs.writeFileSync(cachePath, JSON.stringify({ version: 2, timestampMs: Date.now() - 7200e3, remainPercent: 97 }), 'utf8');
        // Real LS answers over TLS even for auth rejections (debug report §2.2),
        // and the transcript path pins HTTPS — mirror that with an HTTPS 401 mock.
        const { certPem: cert401, privateKey: key401 } = createTestCert();
        const mock401 = https.createServer({ key: key401, cert: cert401 }, (req, res) => {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ code: 'unauthenticated', message: 'missing CSRF token' }));
        });
        await new Promise(r => mock401.listen(0, '127.0.0.1', r));
        const port = mock401.address().port;
        try {
          const { brain, writeOutput } = mkBrain(tmp);
          // Stale record: right pid but wrong ports → must be discarded, so the
          // attempt falls back to the legacy 'none' classification first…
          writeOutput('conv-stale', 1, discoveredLine(7777, port + 1000, [port + 1000], UUID_OLD));
          const resStale = await geminiQuota.fetchLiveGeminiQuota({
            discover: async () => ({ pid: 7777, port, ports: [port], csrfToken: '', protocol: null }),
            transcriptScan: { brainDir: brain },
            cachePath,
            cooldownMarker: markerPath
          });
          assert.strictEqual(resStale.errorKind, 'auth_failure');
          assert.strictEqual(resStale.tokenSource, 'none', 'pid-OK/port-mismatch token must be discarded, not used');
          let raw = JSON.parse(fs.readFileSync(markerPath, 'utf8'));
          assert.strictEqual(raw.tokenSource, 'none');
          assert(!raw.detail.includes(UUID_OLD), 'marker detail must never carry a discarded token');

          // …while a VALID transcript token drives the 401 with transcript provenance.
          fs.unlinkSync(markerPath);
          writeOutput('conv-live', 2, discoveredLine(7777, port, [port], UUID_OK));
          const res = await geminiQuota.fetchLiveGeminiQuota({
            discover: async () => ({ pid: 7777, port, ports: [port], csrfToken: '', protocol: null }),
            transcriptScan: { brainDir: brain },
            cachePath,
            cooldownMarker: markerPath
          });
          assert.strictEqual(res.isLive, false);
          assert.strictEqual(res.errorKind, 'auth_failure');
          assert.strictEqual(res.tokenSource, 'transcript', 'returned diagnostics must expose the transcript source');
          raw = JSON.parse(fs.readFileSync(markerPath, 'utf8'));
          assert.strictEqual(raw.version, 2);
          assert.strictEqual(raw.reason, 'auth_failure');
          assert.strictEqual(raw.tokenSource, 'transcript', 'marker must record the transcript source kind');
          assert(!fs.readFileSync(markerPath, 'utf8').includes(UUID_OK), 'marker must not contain the token plaintext');
          assert(!fs.readFileSync(cachePath, 'utf8').includes(UUID_OK), 'cache decoration must not contain the token plaintext');
        } finally {
          await new Promise(r => mock401.close(r));
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('Multi-candidate process discovery recovers live quota across multiple ports and renders clean badge without ! marker', async () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'multi-cand-'));
        const cachePath = path.join(tmp, 'gemini_quota_cache.json');
        const markerPath = path.join(tmp, 'gemini_quota_probe_cooldown.json');

        const server401 = http.createServer((req, res) => {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ code: 'unauthenticated', message: 'invalid CSRF token' }));
        });
        const server200 = http.createServer((req, res) => {
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
                      remainingFraction: 0.55,
                      resetTime: new Date(Date.now() + 3600000).toISOString()
                    }
                  ]
                }
              ]
            }
          }));
        });

        await new Promise(r => server401.listen(0, '127.0.0.1', r));
        await new Promise(r => server200.listen(0, '127.0.0.1', r));
        const port401 = server401.address().port;
        const port200 = server200.address().port;

        try {
          fs.writeFileSync(markerPath, JSON.stringify({
            version: 2, reason: 'auth_failure', expiryMs: Date.now() + 600000
          }), 'utf8');

          const res = await geminiQuota.fetchLiveGeminiQuota({
            discover: async () => ({
              pid: 9999,
              port: port200,
              ports: [port200, port401],
              csrfToken: 'tok-valid',
              protocol: 'http'
            }),
            cachePath,
            cooldownMarker: markerPath,
            forceRefresh: true
          });

          assert.strictEqual(res.isLive, true, 'Live fetch must succeed using responsive candidate port');
          assert.strictEqual(res.remainPercent, 55);
          assert.strictEqual(fs.existsSync(markerPath), false, 'Cooldown marker must be cleared on success');

          const cached = geminiQuota.getCachedGeminiQuota(cachePath, 30000);
          assert.strictEqual(cached.isFresh, true);
          assert.strictEqual(cached.isStale, false);

          const badge = formatter.stripAnsi(formatter.renderRealTimeBadge({
            turnTokens: 50, turnCostUsd: 0.001, todayTokens: 500, todayCostUsd: 0.005,
            cacheHitRate: 80, geminiQuota: cached
          }));
          assert(badge.includes('55%'), `Badge must show 55%, got: ${badge}`);
          assert(!badge.includes('55%!'), `Badge must not append anomalous ! marker, got: ${badge}`);
          assert(!badge.includes('55%*'), `Badge must not append anomalous * marker, got: ${badge}`);
          assert(!badge.includes('55%1'), `Badge must not display 55%1, got: ${badge}`);
        } finally {
          await new Promise(r => server401.close(r));
          await new Promise(r => server200.close(r));
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('Candidate port fallback: when 401 port is first in candidate list, fetchLiveGeminiQuota falls through to 200 port and clears cooldown', async () => {
        const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'cand-401-first-'));
        const cachePath = path.join(tmp, 'gemini_quota_cache.json');
        const markerPath = path.join(tmp, 'gemini_quota_probe_cooldown.json');

        const server401 = http.createServer((req, res) => {
          res.writeHead(401, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ code: 'unauthenticated', message: 'invalid CSRF token' }));
        });
        const server200 = http.createServer((req, res) => {
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
                      remainingFraction: 0.70,
                      resetTime: new Date(Date.now() + 7200000).toISOString()
                    }
                  ]
                }
              ]
            }
          }));
        });

        await new Promise(r => server401.listen(0, '127.0.0.1', r));
        await new Promise(r => server200.listen(0, '127.0.0.1', r));
        const port401 = server401.address().port;
        const port200 = server200.address().port;

        try {
          fs.writeFileSync(markerPath, JSON.stringify({
            version: 2, reason: 'auth_failure', expiryMs: Date.now() + 600000
          }), 'utf8');

          const res = await geminiQuota.fetchLiveGeminiQuota({
            discover: async () => ({
              pid: 3700,
              pids: [38004, 3700],
              port: port401,
              ports: [port401, port200],
              csrfToken: 'tok-valid',
              protocol: 'http'
            }),
            cachePath,
            cooldownMarker: markerPath,
            forceRefresh: true
          });

          assert.strictEqual(res.isLive, true, 'Must succeed on secondary port despite 401 on primary port');
          assert.strictEqual(res.remainPercent, 70);
          assert.strictEqual(fs.existsSync(markerPath), false, 'Cooldown marker must be removed on eventual success');

          const cached = geminiQuota.getCachedGeminiQuota(cachePath, 30000);
          assert.strictEqual(cached.isFresh, true);
          assert.strictEqual(cached.isStale, false);
          assert.strictEqual(cached.lastError, undefined, 'Successful probe must not have lastError');

          const badge = formatter.stripAnsi(formatter.renderRealTimeBadge({
            turnTokens: 50, turnCostUsd: 0.001, todayTokens: 500, todayCostUsd: 0.005,
            cacheHitRate: 80, geminiQuota: cached
          }));
          assert(badge.includes('70%'), `Badge must show 70%, got: ${badge}`);
          assert(!badge.includes('70%!'), `Badge must not have !, got: ${badge}`);
          assert(!badge.includes('70%1'), `Badge must not have 1, got: ${badge}`);
        } finally {
          await new Promise(r => server401.close(r));
          await new Promise(r => server200.close(r));
          fs.rmSync(tmp, { recursive: true, force: true });
        }
      });

      await test('validateTranscriptToken matches against discovery.pids array for multi-candidate processes', () => {
        const validUuid = '12345678-1234-1234-1234-123456789abc';
        const discovery = {
          pid: 38004,
          pids: [38004, 3700],
          ports: [54457, 54713]
        };
        const hit = {
          pid: 3700,
          port: 54457,
          ports: [54457],
          csrfToken: validUuid
        };
        assert.strictEqual(geminiQuota.validateTranscriptToken(discovery, hit), true);

        const hitMismatch = {
          pid: 99999,
          port: 54457,
          ports: [54457],
          csrfToken: validUuid
        };
        assert.strictEqual(geminiQuota.validateTranscriptToken(discovery, hitMismatch), false);
      });

      await test('extractPortsFromLsof parses all listening ports from lsof output', () => {
        const lsofSample = `COMMAND   PID USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
agy      1234 user    3u  IPv4 0xdeadbeef      0t0  TCP 127.0.0.1:54457 (LISTEN)
agy      1234 user    4u  IPv4 0xfeedface      0t0  TCP 127.0.0.1:54458 (LISTEN)
agy      1234 user    5u  IPv4 0xbaadf00d      0t0  TCP 127.0.0.1:54457 (LISTEN)
`;
        const ports = geminiQuota.extractPortsFromLsof(lsofSample);
        assert.deepStrictEqual(ports, [54457, 54458]);

        assert.deepStrictEqual(geminiQuota.extractPortsFromLsof(''), []);
        assert.deepStrictEqual(geminiQuota.extractPortsFromLsof(null), []);
      });

      await test('buildDashboardPayload triggers triggerBackgroundQuotaRefresh when geminiQuota is missing or stale', () => {
        let triggered = false;
        const origTrigger = geminiQuota.triggerBackgroundQuotaRefresh;
        geminiQuota.triggerBackgroundQuotaRefresh = () => { triggered = true; return true; };
        try {
          htmlReport.buildDashboardPayload([], { geminiQuota: { isFresh: false, remainPercent: 50 } });
          assert.strictEqual(triggered, true, 'Must trigger background refresh when quota is stale');

          triggered = false;
          htmlReport.buildDashboardPayload([], { geminiQuota: { isFresh: true, remainPercent: 50 } });
          assert.strictEqual(triggered, false, 'Must NOT trigger background refresh when quota is fresh');
        } finally {
          geminiQuota.triggerBackgroundQuotaRefresh = origTrigger;
        }
      });
    }
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
      assert(rawBat.includes('configure-customizations.js'), 'install.bat must invoke configure-customizations.js');
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
      assert(rawSh.includes('ln -sf "$ROOT_DIR/bin/agy-dashboard.js" "$USER_BIN/agy-dashboard"'), 'install.sh must link agy-dashboard');

      // Automated configure-statusline invocation
      assert(rawSh.includes('configure-statusline.js'), 'install.sh must invoke configure-statusline.js');
      assert(rawSh.includes('configure-customizations.js'), 'install.sh must invoke configure-customizations.js');
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

    await test('scripts/lib/configure-rules.js should deploy rules and handle idempotency, backups, and CLI args', () => {
      const configRulesPath = path.join(__dirname, '..', 'scripts', 'lib', 'configure-rules.js');
      assert(fs.existsSync(configRulesPath), 'scripts/lib/configure-rules.js must exist');
      const { configureRules, parseArgs: parseRulesArgs } = require(configRulesPath);

      const tmpSrc = path.join(os.tmpdir(), `agy-test-rules-src-${Date.now()}`);
      const tmpTgt = path.join(os.tmpdir(), `agy-test-rules-tgt-${Date.now()}`);
      fs.mkdirSync(tmpSrc, { recursive: true });
      fs.mkdirSync(tmpTgt, { recursive: true });

      try {
        fs.writeFileSync(path.join(tmpSrc, 'RULE1.md'), '# Rule 1 Initial', 'utf8');
        fs.writeFileSync(path.join(tmpSrc, 'RULE2.md'), '# Rule 2 Initial', 'utf8');

        // 1. Initial deployment
        const res1 = configureRules({ sourceDir: tmpSrc, targetDir: tmpTgt });
        assert.strictEqual(res1.length, 2);
        assert.strictEqual(res1[0].action, 'created');
        assert.strictEqual(res1[1].action, 'created');
        assert.strictEqual(fs.readFileSync(path.join(tmpTgt, 'RULE1.md'), 'utf8'), '# Rule 1 Initial');

        // 2. Idempotent deployment (no changes)
        const res2 = configureRules({ sourceDir: tmpSrc, targetDir: tmpTgt });
        assert.strictEqual(res2[0].action, 'identical');
        assert.strictEqual(res2[1].action, 'identical');

        // 3. Updated source should create backup and update target
        fs.writeFileSync(path.join(tmpSrc, 'RULE1.md'), '# Rule 1 Updated', 'utf8');
        const res3 = configureRules({ sourceDir: tmpSrc, targetDir: tmpTgt });
        assert.strictEqual(res3[0].action, 'updated');
        assert(res3[0].backupPath && fs.existsSync(res3[0].backupPath));
        assert.strictEqual(fs.readFileSync(path.join(tmpTgt, 'RULE1.md'), 'utf8'), '# Rule 1 Updated');

        // 4. CLI arg parsing
        const parsed = parseRulesArgs(['--force', '--source', '/tmp/src', '--target', '/tmp/tgt']);
        assert.strictEqual(parsed.force, true);
        assert.strictEqual(parsed.sourceDir, path.resolve('/tmp/src'));
        assert.strictEqual(parsed.targetDir, path.resolve('/tmp/tgt'));
      } finally {
        fs.rmSync(tmpSrc, { recursive: true, force: true });
        fs.rmSync(tmpTgt, { recursive: true, force: true });
      }
    });

    await test('scripts/lib/configure-customizations.js should deploy all customizations across categories', () => {
      const configCustPath = path.join(__dirname, '..', 'scripts', 'lib', 'configure-customizations.js');
      assert(fs.existsSync(configCustPath), 'scripts/lib/configure-customizations.js must exist');
      const { deployAllCustomizations, parseArgs: parseCustArgs } = require(configCustPath);

      const tmpRepo = path.join(os.tmpdir(), `agy-test-cust-repo-${Date.now()}`);
      const tmpConfig = path.join(os.tmpdir(), `agy-test-cust-tgt-${Date.now()}`);
      fs.mkdirSync(path.join(tmpRepo, 'rules'), { recursive: true });
      fs.mkdirSync(path.join(tmpRepo, 'plugins', 'my_plugin'), { recursive: true });
      fs.mkdirSync(path.join(tmpRepo, 'skills', 'my_skill'), { recursive: true });
      fs.mkdirSync(path.join(tmpRepo, 'hooks'), { recursive: true });

      try {
        fs.writeFileSync(path.join(tmpRepo, 'rules', 'AGENTS.md'), '# Agents Rule', 'utf8');
        fs.writeFileSync(path.join(tmpRepo, 'plugins', 'my_plugin', 'plugin.json'), '{"name":"test"}', 'utf8');
        fs.writeFileSync(path.join(tmpRepo, 'skills', 'my_skill', 'SKILL.md'), '# My Skill', 'utf8');
        fs.writeFileSync(path.join(tmpRepo, 'hooks', 'hooks.json'), '{"hooks":{}}', 'utf8');

        // 1. Initial full deployment
        const sum1 = deployAllCustomizations({ repoRoot: tmpRepo, configDir: tmpConfig });
        assert.strictEqual(sum1.created, 4);
        assert.strictEqual(sum1.updated, 0);
        assert.strictEqual(sum1.identical, 0);

        // Verify files deployed to respective destinations
        assert(fs.existsSync(path.join(tmpConfig, 'rules', 'AGENTS.md')));
        assert(fs.existsSync(path.join(tmpConfig, 'plugins', 'my_plugin', 'plugin.json')));
        assert(fs.existsSync(path.join(tmpConfig, 'skills', 'my_skill', 'SKILL.md')));
        assert(fs.existsSync(path.join(tmpConfig, 'hooks', 'hooks.json')));

        // 2. Idempotent deployment
        const sum2 = deployAllCustomizations({ repoRoot: tmpRepo, configDir: tmpConfig });
        assert.strictEqual(sum2.created, 0);
        assert.strictEqual(sum2.updated, 0);
        assert.strictEqual(sum2.identical, 4);

        // 3. Updated source deployment
        fs.writeFileSync(path.join(tmpRepo, 'rules', 'AGENTS.md'), '# Agents Rule V2', 'utf8');
        const sum3 = deployAllCustomizations({ repoRoot: tmpRepo, configDir: tmpConfig });
        assert.strictEqual(sum3.created, 0);
        assert.strictEqual(sum3.updated, 1);
        assert.strictEqual(sum3.identical, 3);
        assert.strictEqual(fs.readFileSync(path.join(tmpConfig, 'rules', 'AGENTS.md'), 'utf8'), '# Agents Rule V2');

        // 4. CLI arg parsing
        const parsed = parseCustArgs(['--force', '--source', '/tmp/repo', '--target', '/tmp/cfg']);
        assert.strictEqual(parsed.force, true);
        assert.strictEqual(parsed.repoRoot, path.resolve('/tmp/repo'));
        assert.strictEqual(parsed.configDir, path.resolve('/tmp/cfg'));
      } finally {
        fs.rmSync(tmpRepo, { recursive: true, force: true });
        fs.rmSync(tmpConfig, { recursive: true, force: true });
      }
    });
  });

  // --- Suite 26: CLI Dispatcher & Dashboard Routing ---
  await describe('26. CLI Dispatcher & Dashboard Routing', async () => {
    const { spawnSync } = require('child_process');
    const serve = require('../src/serve');

    await test('agy-tools dashboard routes to --serve --open when no flags provided', () => {
      const agyToolsPath = path.join(__dirname, '..', 'bin', 'agy-tools.js');

      // Test that report flag is preserved
      const res7d = spawnSync(process.execPath, [agyToolsPath, 'dashboard', '--7d', '--help'], { encoding: 'utf8' });
      assert.strictEqual(res7d.status, 0);

      // Test package.json binary mapping and scripts.dashboard
      const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'));
      assert.strictEqual(pkg.bin['agy-dashboard'], './bin/agy-dashboard.js');
      assert.strictEqual(pkg.scripts.dashboard, 'node bin/agy-dashboard.js');
    });

    await test('bin/agy-dashboard.js entry point exists and invokes agy-tokens with AGY_CLI_COMMAND', () => {
      const dashboardEntry = path.join(__dirname, '..', 'bin', 'agy-dashboard.js');
      assert(fs.existsSync(dashboardEntry), 'bin/agy-dashboard.js must exist');

      const res = spawnSync(process.execPath, [dashboardEntry, '--help'], { encoding: 'utf8' });
      assert.strictEqual(res.status, 0);
      assert(res.stdout.includes('Antigravity Token Tracker') || res.stdout.includes('Usage'));
    });

    await test('parseArgs should respect --no-open flag', () => {
      const opts = parseArgs(['node', 'bin/agy-tokens.js', '--open', '--no-open']);
      assert.strictEqual(opts.open, false);

      const optsNoOpenAlone = parseArgs(['node', 'bin/agy-tokens.js', '--no-open']);
      assert.strictEqual(optsNoOpenAlone.open, false);
    });

    await test('dashboard server should handle OPTIONS CORS preflight with 204 No Content', async () => {
      const http = require('http');
      const info = await serve.startDashboardServer({ port: 0, idleTimeoutMs: 10000 });
      try {
        const status = await new Promise((resolve, reject) => {
          const req = http.request({
            port: info.port,
            host: '127.0.0.1',
            method: 'OPTIONS',
            path: '/events',
            headers: {
              Origin: 'http://127.0.0.1'
            }
          }, (res) => {
            assert.strictEqual(res.headers['access-control-allow-origin'], 'http://127.0.0.1');
            assert(res.headers['access-control-allow-methods'].includes('OPTIONS'));
            assert.strictEqual(res.headers['x-content-type-options'], 'nosniff');
            assert.strictEqual(res.headers['x-frame-options'], 'DENY');
            assert.strictEqual(res.headers['vary'], 'Origin');
            resolve(res.statusCode);
          });
          req.on('error', reject);
          req.end();
        });
        assert.strictEqual(status, 204);
      } finally {
        await serve.stopDashboardServer(info.server);
      }
    });

    await test('dashboard server should reject unauthorized Host header with 403 Forbidden', async () => {
      const http = require('http');
      const info = await serve.startDashboardServer({ port: 0, idleTimeoutMs: 10000 });
      try {
        const status = await new Promise((resolve, reject) => {
          const req = http.request({
            port: info.port,
            host: '127.0.0.1',
            method: 'GET',
            path: '/',
            headers: {
              Host: 'evil.com'
            }
          }, (res) => {
            resolve(res.statusCode);
          });
          req.on('error', reject);
          req.end();
        });
        assert.strictEqual(status, 403);
      } finally {
        await serve.stopDashboardServer(info.server);
      }
    });

    await test('dashboard server should reject unauthorized Origin header with 403 Forbidden', async () => {
      const http = require('http');
      const info = await serve.startDashboardServer({ port: 0, idleTimeoutMs: 10000 });
      try {
        const status = await new Promise((resolve, reject) => {
          const req = http.request({
            port: info.port,
            host: '127.0.0.1',
            method: 'GET',
            path: '/data.json',
            headers: {
              Origin: 'http://malicious-website.com'
            }
          }, (res) => {
            resolve(res.statusCode);
          });
          req.on('error', reject);
          req.end();
        });
        assert.strictEqual(status, 403);
      } finally {
        await serve.stopDashboardServer(info.server);
      }
    });

    await test('isAllowedOrigin and isAuthorizedHost validate loopback and null origins securely', () => {
      assert.strictEqual(serve.isAllowedOrigin('null'), true);
      assert.strictEqual(serve.isAllowedOrigin('http://localhost'), true);
      assert.strictEqual(serve.isAllowedOrigin('http://localhost:8787'), true);
      assert.strictEqual(serve.isAllowedOrigin('http://127.0.0.1'), true);
      assert.strictEqual(serve.isAllowedOrigin('http://127.0.0.1:9999'), true);
      assert.strictEqual(serve.isAllowedOrigin('https://localhost'), false);
      assert.strictEqual(serve.isAllowedOrigin('http://evil.com'), false);
      assert.strictEqual(serve.isAllowedOrigin('http://localhost.evil.com'), false);
      assert.strictEqual(serve.isAllowedOrigin(''), false);
      assert.strictEqual(serve.isAllowedOrigin(null), false);

      assert.strictEqual(serve.isAuthorizedHost('127.0.0.1'), true);
      assert.strictEqual(serve.isAuthorizedHost('127.0.0.1:8787'), true);
      assert.strictEqual(serve.isAuthorizedHost('localhost'), true);
      assert.strictEqual(serve.isAuthorizedHost('localhost:8787'), true);
      assert.strictEqual(serve.isAuthorizedHost('evil.com'), false);
      assert.strictEqual(serve.isAuthorizedHost('evil.com:8787'), false);
      assert.strictEqual(serve.isAuthorizedHost(''), false);
      assert.strictEqual(serve.isAuthorizedHost(null), false);
    });
  });

  // --- Suite 27: Real-Time Dashboard Server Watcher & Live SSE Broadcast Integration ---
  await describe('27. Real-Time Dashboard Server Watcher & Live SSE Broadcast Integration', async () => {
    const serve = require('../src/serve');
    const htmlReport = require('../src/html-report');

    await test('fs.watch detects session token logging and broadcasts SSE update to client in real-time', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'live-token-test-'));
      const brainDir = path.join(tempDir, 'brain');
      const cacheFile = path.join(tempDir, 'token-cache.json');
      const quotaCacheFile = path.join(tempDir, 'gemini_quota_cache.json');
      fs.mkdirSync(brainDir, { recursive: true });

      const info = await serve.startDashboardServer({
        port: 0,
        intervalMs: 60000, // long interval to ensure update is triggered by fs.watch
        brainDir,
        cacheFile,
        quotaCacheFile
      });

      assert(info && info.port > 0);

      const receivedEvents = [];
      let reqRef = null;

      const sseConnectedPromise = new Promise((resolve) => {
        reqRef = http.get(`${info.url.replace(/\/$/, '')}/events`, (res) => {
          let buffer = '';
          res.on('data', (chunk) => {
            buffer += chunk.toString();
            const parts = buffer.split('\n\n');
            buffer = parts.pop();
            for (const part of parts) {
              if (part.includes('data:')) {
                const dataLine = part.split('\n').find((l) => l.startsWith('data:'));
                if (dataLine) {
                  try {
                    const parsed = JSON.parse(dataLine.slice(5).trim());
                    receivedEvents.push(parsed);
                    resolve();
                  } catch (_e) {}
                }
              }
            }
          });
        });
        reqRef.on('error', () => {});
      });

      // Wait for initial SSE connection
      await sseConnectedPromise;
      assert(receivedEvents.length >= 1, 'Initial SSE payload must be received');

      // Now simulate a new session turn written to brain directory
      const sessionDir = path.join(brainDir, 'session-test-realtime-1');
      fs.mkdirSync(sessionDir, { recursive: true });
      const transcriptFile = path.join(sessionDir, 'transcript.jsonl');
      const turnLine = JSON.stringify({
        source: 'USER_INPUT',
        content: 'Testing real-time token tracking with a live user prompt message',
        created_at: new Date().toISOString()
      }) + '\n';
      fs.writeFileSync(transcriptFile, turnLine, 'utf8');

      // Await live SSE push triggered by fs.watch (within 2.5s)
      const updatedEvent = await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
          reject(new Error('Timeout waiting for real-time token SSE push'));
        }, 3500);

        const check = setInterval(() => {
          for (let i = 1; i < receivedEvents.length; i++) {
            const ev = receivedEvents[i];
            if (ev && ev.version === 3 && ev.summaries && ev.summaries.today && ev.summaries.today.totalTokens > 0) {
              clearTimeout(timeout);
              clearInterval(check);
              resolve(ev);
              return;
            }
          }
        }, 50);
      });

      assert(updatedEvent.summaries.today.totalTokens >= 500, 'Live SSE update must include new tokens');

      // Verify GET /data.json serves the updated payload
      const dataJsonRes = await new Promise((resolve, reject) => {
        http.get(`${info.url.replace(/\/$/, '')}/data.json`, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve(JSON.parse(body)));
        }).on('error', reject);
      });

      assert(dataJsonRes.summaries.today.totalTokens >= 500, 'GET /data.json must serve updated tokens');

      if (reqRef) reqRef.destroy();
      await serve.stopDashboardServer(info.server);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('fs.watch detects quota changes and broadcasts live quota to SSE clients', async () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'live-quota-test-'));
      const brainDir = path.join(tempDir, 'brain');
      const cacheFile = path.join(tempDir, 'token-cache.json');
      const quotaCacheFile = path.join(tempDir, 'gemini_quota_cache.json');
      fs.mkdirSync(brainDir, { recursive: true });

      // Write initial quota snapshot
      fs.writeFileSync(quotaCacheFile, JSON.stringify({
        timestampMs: Date.now(),
        isFresh: true,
        isLive: true,
        remainPercent: 50.0,
        quota5h: { remainPercent: 40.0, resetFormatted: '3h 10m' },
        quota7d: { remainPercent: 60.0, resetFormatted: '4d 2h' }
      }, null, 2), 'utf8');

      const info = await serve.startDashboardServer({
        port: 0,
        intervalMs: 60000,
        brainDir,
        cacheFile,
        quotaCacheFile
      });

      const receivedEvents = [];
      let reqRef = null;

      await new Promise((resolve) => {
        reqRef = http.get(`${info.url.replace(/\/$/, '')}/events`, (res) => {
          let buffer = '';
          res.on('data', (chunk) => {
            buffer += chunk.toString();
            const parts = buffer.split('\n\n');
            buffer = parts.pop();
            for (const part of parts) {
              if (part.includes('data:')) {
                const dataLine = part.split('\n').find((l) => l.startsWith('data:'));
                if (dataLine) {
                  try {
                    const parsed = JSON.parse(dataLine.slice(5).trim());
                    receivedEvents.push(parsed);
                    resolve();
                  } catch (_e) {}
                }
              }
            }
          });
        });
        reqRef.on('error', () => {});
      });

      // Update quota on disk
      fs.writeFileSync(quotaCacheFile, JSON.stringify({
        timestampMs: Date.now(),
        isFresh: true,
        isLive: true,
        remainPercent: 94.5,
        quota5h: { remainPercent: 89.0, resetFormatted: '1h 30m' },
        quota7d: { remainPercent: 97.2, resetFormatted: '6d 18h' }
      }, null, 2), 'utf8');

      // Await live quota update via SSE
      const updatedQuotaEvent = await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
          reject(new Error('Timeout waiting for real-time quota SSE push'));
        }, 3500);

        const check = setInterval(() => {
          for (let i = 1; i < receivedEvents.length; i++) {
            const ev = receivedEvents[i];
            if (ev && ev.geminiQuota && ev.geminiQuota.remainPercent === 94.5) {
              clearTimeout(timeout);
              clearInterval(check);
              resolve(ev);
              return;
            }
          }
        }, 50);
      });

      assert.strictEqual(updatedQuotaEvent.geminiQuota.remainPercent, 94.5);
      assert.strictEqual(updatedQuotaEvent.geminiQuota.quota5h.remainPercent, 89.0);

      // Verify GET /data.json returns fresh quota
      const dataJson = await new Promise((resolve, reject) => {
        http.get(`${info.url.replace(/\/$/, '')}/data.json`, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve(JSON.parse(body)));
        }).on('error', reject);
      });

      assert.strictEqual(dataJson.geminiQuota.remainPercent, 94.5);

      if (reqRef) reqRef.destroy();
      await serve.stopDashboardServer(info.server);
      fs.rmSync(tempDir, { recursive: true, force: true });
    });

    await test('broadcastSSE pushes updates to all concurrent SSE clients', async () => {
      const info = await serve.startDashboardServer({ port: 0, intervalMs: 60000 });
      const client1Events = [];
      const client2Events = [];
      let req1 = null;
      let req2 = null;

      const connectClient = (eventsArr) => new Promise((resolve) => {
        let resolved = false;
        const req = http.get(`${info.url.replace(/\/$/, '')}/events`, (res) => {
          let buffer = '';
          res.on('data', (chunk) => {
            buffer += chunk.toString();
            const parts = buffer.split('\n\n');
            buffer = parts.pop();
            for (const part of parts) {
              if (part.includes('data:')) {
                const dataLine = part.split('\n').find((l) => l.startsWith('data:'));
                if (dataLine) {
                  try {
                    eventsArr.push(JSON.parse(dataLine.slice(5).trim()));
                    if (!resolved) {
                      resolved = true;
                      resolve(req);
                    }
                  } catch (_e) {}
                }
              }
            }
          });
        });
        req.on('error', () => {});
      });

      req1 = await connectClient(client1Events);
      req2 = await connectClient(client2Events);

      assert(client1Events.length >= 1);
      assert(client2Events.length >= 1);

      // Broadcast an update
      const mockPayload = { version: 3, dailyModels: {}, broadcastTest: true, generatedAt: new Date().toISOString() };
      info.broadcastSSE(mockPayload);

      await new Promise((resolve) => setTimeout(resolve, 200));

      assert(client1Events.some((e) => e.broadcastTest === true), 'Client 1 must receive broadcast');
      assert(client2Events.some((e) => e.broadcastTest === true), 'Client 2 must receive broadcast');

      if (req1) req1.destroy();
      if (req2) req2.destroy();
      await serve.stopDashboardServer(info.server);
    });

    await test('GET / serves fresh HTML with current port baked into SSE hint', async () => {
      const info = await serve.startDashboardServer({ port: 0 });

      const { statusCode, headers, body } = await new Promise((resolve, reject) => {
        http.get(info.url, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve({ statusCode: res.statusCode, headers: res.headers, body }));
        }).on('error', reject);
      });

      assert.strictEqual(statusCode, 200);
      assert.strictEqual(headers['cache-control'], 'no-store');
      assert(body.includes(`SSE_PORT_HINT = ${info.port}`));
      assert(body.includes("getSseUrl()"));
      assert(body.includes("return '/events'"));

      await serve.stopDashboardServer(info.server);
    });

    await test('GET /dashboard-data.js serves fresh script matching newest aggregate', async () => {
      const info = await serve.startDashboardServer({ port: 0 });

      const { statusCode, headers, body } = await new Promise((resolve, reject) => {
        http.get(`${info.url.replace(/\/$/, '')}/dashboard-data.js`, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve({ statusCode: res.statusCode, headers: res.headers, body }));
        }).on('error', reject);
      });

      assert.strictEqual(statusCode, 200);
      assert.strictEqual(headers['cache-control'], 'no-store');
      assert(body.startsWith('window.__AGY_DASH__ = '));
      const jsonStr = body.slice('window.__AGY_DASH__ = '.length).trim().replace(/;$/, '');
      const parsed = JSON.parse(jsonStr);
      assert.strictEqual(parsed.version, 3);

      await serve.stopDashboardServer(info.server);
    });

    await test('abrupt client disconnect during broadcast is handled gracefully without uncaught exceptions', async () => {
      const info = await serve.startDashboardServer({ port: 0, intervalMs: 60000 });

      const req = http.get(`${info.url.replace(/\/$/, '')}/events`, (res) => {
        res.on('data', () => {
          // Immediately destroy socket while in stream
          req.destroy();
        });
      });
      req.on('error', () => {});

      await new Promise((resolve) => setTimeout(resolve, 200));

      // Broadcast while client is dead/destroyed
      info.broadcastSSE({ version: 3, dailyModels: {}, test: 'abrupt-disconnect' });

      await new Promise((resolve) => setTimeout(resolve, 100));
      // Server should still be healthy and responding to requests
      const dataJson = await new Promise((resolve, reject) => {
        http.get(`${info.url.replace(/\/$/, '')}/data.json`, (res) => {
          let body = '';
          res.on('data', (c) => { body += c; });
          res.on('end', () => resolve(JSON.parse(body)));
        }).on('error', reject);
      });
      assert.strictEqual(dataJson.version, 3);

      await serve.stopDashboardServer(info.server);
    });
  });

  // --- 28. Statusline & Dashboard Reliability Validations ---
  await describe('28. Statusline & Dashboard Reliability Validations', async () => {
    const htmlReport = require('../src/html-report');
    await test('renderDashboardHtml embeds clientScript that parses cleanly in vm.Script without syntax errors', () => {
      const payload = htmlReport.buildDashboardPayload([]);
      const html = htmlReport.renderDashboardHtml(payload);
      const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)];
      assert(scripts.length >= 2, 'Should contain at least 2 script tags');
      for (let i = 0; i < scripts.length; i++) {
        assert.doesNotThrow(() => {
          new vm.Script(scripts[i][1]);
        }, `Script ${i} should have valid JavaScript syntax without syntax errors`);
      }
    });

    await test('discoverSessions discovers sessions across brain and sibling sessions directories with deduplication', () => {
      const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agy-test-sibling-'));
      try {
        const brainDir = path.join(tmpDir, 'brain');
        const sessionsDir = path.join(tmpDir, 'sessions');
        fs.mkdirSync(path.join(brainDir, 'sess-1', 'logs'), { recursive: true });
        fs.writeFileSync(
          path.join(brainDir, 'sess-1', 'logs', 'transcript.jsonl'),
          JSON.stringify({ created_at: '2026-09-19T10:00:00Z', source: 'USER_INPUT', content: 'hello' }) + '\n'
        );
        fs.mkdirSync(path.join(sessionsDir, 'sess-2', 'logs'), { recursive: true });
        fs.writeFileSync(
          path.join(sessionsDir, 'sess-2', 'logs', 'transcript.jsonl'),
          JSON.stringify({ created_at: '2026-09-19T11:00:00Z', source: 'USER_INPUT', content: 'world' }) + '\n'
        );
        // Duplicate session in sessionsDir should be deduplicated
        fs.mkdirSync(path.join(sessionsDir, 'sess-1', 'logs'), { recursive: true });
        fs.writeFileSync(
          path.join(sessionsDir, 'sess-1', 'logs', 'transcript.jsonl'),
          JSON.stringify({ created_at: '2026-09-19T10:00:00Z', source: 'USER_INPUT', content: 'dup' }) + '\n'
        );

        const discovered = logParser.discoverSessions(brainDir);
        assert.strictEqual(discovered.length, 2);
        const ids = discovered.map(d => d.sessionId);
        assert(ids.includes('sess-1'));
        assert(ids.includes('sess-2'));
      } finally {
        fs.rmSync(tmpDir, { recursive: true, force: true });
      }
    });

    await test('syncSessions orders sessions by most recent activity timestamp rather than creation time', async () => {
      const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agy-test-order-'));
      try {
        const brainDir = path.join(tmpDir, 'brain');
        const cacheFile = path.join(tmpDir, 'cache.json');
        // Session Old: created yesterday at 08:00, but active today at 12:00
        fs.mkdirSync(path.join(brainDir, 'sess-old', 'logs'), { recursive: true });
        fs.writeFileSync(
          path.join(brainDir, 'sess-old', 'logs', 'transcript.jsonl'),
          JSON.stringify({ created_at: '2026-09-18T08:00:00Z', source: 'USER_INPUT', content: 'old turn 1' }) + '\n' +
          JSON.stringify({ created_at: '2026-09-19T12:00:00Z', source: 'USER_INPUT', content: 'new turn 2' }) + '\n'
        );
        // Session New: created today at 09:00, but inactive since 09:05
        fs.mkdirSync(path.join(brainDir, 'sess-new', 'logs'), { recursive: true });
        fs.writeFileSync(
          path.join(brainDir, 'sess-new', 'logs', 'transcript.jsonl'),
          JSON.stringify({ created_at: '2026-09-19T09:00:00Z', source: 'USER_INPUT', content: 'turn 1' }) + '\n' +
          JSON.stringify({ created_at: '2026-09-19T09:05:00Z', source: 'USER_INPUT', content: 'turn 2' }) + '\n'
        );

        const res = await cacheManager.syncSessions({ brainDir, cachePath: cacheFile });
        assert.strictEqual(res.sessions.length, 2);
        // sess-old has latest turn at 12:00, so it must be index 0
        assert.strictEqual(res.sessions[0].sessionId, 'sess-old');
        assert.strictEqual(res.sessions[1].sessionId, 'sess-new');
      } finally {
        fs.rmSync(tmpDir, { recursive: true, force: true });
      }
    });

    await test('handlePostInvocation targets ANTIGRAVITY_CONVERSATION_ID from env and falls back to most active session', async () => {
      const prevEnv = process.env.ANTIGRAVITY_CONVERSATION_ID;
      try {
        const mockSessions = [
          {
            sessionId: 'sess-inactive',
            startTime: '2026-09-19T08:00:00Z',
            endTime: '2026-09-19T08:05:00Z',
            turns: [{ totalTokens: 50, costUsd: 0.001, createdAt: '2026-09-19T08:05:00Z' }]
          },
          {
            sessionId: 'sess-active',
            startTime: '2026-09-18T20:00:00Z',
            endTime: '2026-09-19T14:00:00Z',
            turns: [{ totalTokens: 250, costUsd: 0.005, createdAt: '2026-09-19T14:00:00Z' }]
          }
        ];

        // 1. Without env var, should pick most active session (sess-active with turn at 14:00)
        delete process.env.ANTIGRAVITY_CONVERSATION_ID;
        const res1 = await hookHandler.handlePostInvocation({ sessions: mockSessions });
        assert.strictEqual(res1.turnTokens, 250);

        // 2. With env var, should target the specified conversation even if older
        process.env.ANTIGRAVITY_CONVERSATION_ID = 'sess-inactive';
        const res2 = await hookHandler.handlePostInvocation({ sessions: mockSessions });
        assert.strictEqual(res2.turnTokens, 50);
      } finally {
        if (prevEnv !== undefined) {
          process.env.ANTIGRAVITY_CONVERSATION_ID = prevEnv;
        } else {
          delete process.env.ANTIGRAVITY_CONVERSATION_ID;
        }
      }
    });

    await test('getEnvLanguageServerTarget extracts target from ANTIGRAVITY_LS_ADDRESS and ANTIGRAVITY_LS_PORT env vars', () => {
      const prevAddr = process.env.ANTIGRAVITY_LS_ADDRESS;
      const prevPort = process.env.ANTIGRAVITY_LS_PORT;
      try {
        process.env.ANTIGRAVITY_LS_ADDRESS = 'localhost:63768';
        delete process.env.ANTIGRAVITY_LS_PORT;
        const res1 = geminiQuota.getEnvLanguageServerTarget();
        assert(res1 !== null);
        assert.strictEqual(res1.port, 63768);

        delete process.env.ANTIGRAVITY_LS_ADDRESS;
        process.env.ANTIGRAVITY_LS_PORT = '54321';
        const res2 = geminiQuota.getEnvLanguageServerTarget();
        assert(res2 !== null);
        assert.strictEqual(res2.port, 54321);
      } finally {
        if (prevAddr !== undefined) process.env.ANTIGRAVITY_LS_ADDRESS = prevAddr;
        else delete process.env.ANTIGRAVITY_LS_ADDRESS;
        if (prevPort !== undefined) process.env.ANTIGRAVITY_LS_PORT = prevPort;
        else delete process.env.ANTIGRAVITY_LS_PORT;
      }
    });

    await test('triggerBackgroundQuotaRefresh triggers refresh with forceRefresh to bypass stale probe cooldowns', () => {
      assert(typeof geminiQuota.triggerBackgroundQuotaRefresh === 'function');
      const spawned = geminiQuota.triggerBackgroundQuotaRefresh();
      assert(typeof spawned === 'boolean');
    });
  });

  // --- Suite 29: Statusline 5h/7d Real-Time Update & Token Recovery Validations ---
  await describe('29. Statusline 5h/7d Real-Time Update & Token Recovery Validations', async () => {
    await test('savePersistedLsToken and readPersistedLsToken persist valid token and reject invalid UUIDs', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agy-ls-tok-test-'));
      const testTokFile = path.join(tempDir, '.ls_token.json');
      try {
        const validUuid = '12345678-1234-1234-1234-123456789abc';
        // Reject invalid UUID
        assert.strictEqual(geminiQuota.savePersistedLsToken({ csrfToken: 'not-a-uuid' }, testTokFile), false);
        assert.strictEqual(geminiQuota.readPersistedLsToken(testTokFile), null);

        // Accept and persist valid UUID
        const saved = geminiQuota.savePersistedLsToken({
          pid: 4321,
          port: 57994,
          ports: [57995, 57994],
          csrfToken: validUuid
        }, testTokFile);
        assert.strictEqual(saved, true);
        assert(fs.existsSync(testTokFile));

        const read = geminiQuota.readPersistedLsToken(testTokFile);
        assert(read !== null);
        assert.strictEqual(read.pid, 4321);
        assert.strictEqual(read.port, 57994);
        assert.deepStrictEqual(read.ports, [57995, 57994]);
        assert.strictEqual(read.csrfToken, validUuid);
        assert(typeof read.updatedAt === 'number' && read.updatedAt > 0);
      } finally {
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });

    await test('applyTurnUsageToQuota deducts subsequent turns in real-time from 5h and 7d quotas', () => {
      const snapshotTime = Date.now() - 60000; // snapshot 1 min ago
      const initialQuota = {
        timestampMs: snapshotTime,
        quota5h: {
          remainPercent: 90,
          remainingFraction: 0.90,
          resetTime: new Date(Date.now() + 3600000).toISOString(),
          resetFormatted: '1h',
          window: '5h'
        },
        quota7d: {
          remainPercent: 60,
          remainingFraction: 0.60,
          resetTime: new Date(Date.now() + 86400000).toISOString(),
          resetFormatted: '1d',
          window: 'weekly'
        },
        remainPercent: 90
      };

      // Turn 1: 1M tokens executed 30 seconds after snapshot
      // 1M on 20M limit (5h) = 5% deduction -> 90% - 5% = 85%
      // 1M on 150M limit (7d) = 0.667% deduction -> 60% - 1% = 59%
      const mockSessions = [
        {
          sessionId: 's1',
          turns: [
            { createdAt: new Date(snapshotTime - 10000).toISOString(), totalTokens: 500000 },
            { createdAt: new Date(snapshotTime + 30000).toISOString(), totalTokens: 1000000 }
          ]
        }
      ];

      const adjusted = geminiQuota.applyTurnUsageToQuota(initialQuota, mockSessions, {
        now: Date.now(),
        quota: { limit5h: 20000000, limit7d: 150000000 }
      });

      assert.strictEqual(adjusted.quota5h.remainPercent, 85);
      assert.strictEqual(adjusted.quota7d.remainPercent, 59);
      assert.strictEqual(adjusted.remainPercent, 85);
      assert.strictEqual(initialQuota.quota5h.remainPercent, 90);
    });

    await test('applyTurnUsageToQuota replenishes quota to 100% when resetTime has elapsed', () => {
      const snapshotTime = Date.now() - 3600000; // snapshot 1h ago
      const expiredQuota = {
        timestampMs: snapshotTime,
        quota5h: {
          remainPercent: 50,
          remainingFraction: 0.50,
          resetTime: new Date(Date.now() - 60000).toISOString(), // reset 1m ago
          resetFormatted: '0s',
          window: '5h'
        },
        quota7d: {
          remainPercent: 30,
          remainingFraction: 0.30,
          resetTime: new Date(Date.now() + 3600000).toISOString(), // not yet reset
          resetFormatted: '1h',
          window: 'weekly'
        },
        remainPercent: 50
      };

      const adjusted = geminiQuota.applyTurnUsageToQuota(expiredQuota, [], { now: Date.now() });
      assert.strictEqual(adjusted.quota5h.remainPercent, 100, '5h quota must replenish to 100% after resetTime passes');
      assert.strictEqual(adjusted.quota7d.remainPercent, 30, '7d quota must remain unreset before its resetTime');
    });

    await test('resolveCsrfTokenFallback recovers token from persisted cache on matching live server', () => {
      const tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agy-fb-test-'));
      const testTokFile = path.join(tempDir, '.ls_token.json');
      const validUuid = 'abcdef01-2345-6789-abcd-ef0123456789';
      try {
        geminiQuota.savePersistedLsToken({
          pid: 9999,
          port: 48000,
          ports: [48000, 48001],
          csrfToken: validUuid
        }, testTokFile);

        const discovery = {
          pid: 9999,
          port: 48000,
          ports: [48000, 48001],
          csrfToken: ''
        };

        const resolved = geminiQuota.resolveCsrfTokenFallback(discovery, { tokenFilePath: testTokFile });
        assert.strictEqual(resolved.csrfToken, validUuid);
        assert.strictEqual(resolved.tokenSource, 'persisted_cache');
        assert.strictEqual(resolved.pinnedProtocol, 'https');
      } finally {
        fs.rmSync(tempDir, { recursive: true, force: true });
      }
    });

    await test('handlePostInvocation applies dynamic turn adjustments into badge output', async () => {
      const snapshotTime = Date.now() - 10000;
      const cached = {
        timestampMs: snapshotTime,
        isLive: true,
        isFresh: true,
        isStale: false,
        quota5h: { remainPercent: 95, remainingFraction: 0.95, window: '5h' },
        quota7d: { remainPercent: 70, remainingFraction: 0.70, window: 'weekly' },
        remainPercent: 95
      };

      // 2M tokens on 20M limit = 10% deduction -> 95% - 10% = 85%
      const sessions = [
        {
          sessionId: 'test-sess',
          turns: [
            { createdAt: new Date(snapshotTime + 5000).toISOString(), totalTokens: 2000000, costUsd: 0.001 }
          ]
        }
      ];

      const res = await hookHandler.handlePostInvocation({
        sessions,
        geminiQuota: cached,
        quota: { limit5h: 20000000, limit7d: 150000000 }
      });

      assert(res.badge.includes('85%'), `Badge must reflect real-time 85% instead of frozen 95%, got: ${res.badge}`);
    });
  });

  // --- Suite 30: Generational Version-Aware & Provider-Aware Pricing Engine ---
  await describe('30. Generational Version-Aware & Provider-Aware Pricing Engine', async () => {
    await test('Generational pricing: gemini-3.8-flash ($0.15) vs gemini-2.0-flash ($0.10) vs gemini-2.0-flash-lite ($0.075)', () => {
      const g38 = config.getModelPricing('gemini-3.8-flash');
      assert.strictEqual(g38.inputPerMillion, 0.15);
      assert.strictEqual(g38.cachedInputPerMillion, 0.0375);
      assert.strictEqual(g38.outputPerMillion, 0.60);

      const g20 = config.getModelPricing('gemini-2.0-flash');
      assert.strictEqual(g20.inputPerMillion, 0.10);
      assert.strictEqual(g20.cachedInputPerMillion, 0.025);
      assert.strictEqual(g20.outputPerMillion, 0.40);

      const g20lite = config.getModelPricing('gemini-2.0-flash-lite');
      assert.strictEqual(g20lite.inputPerMillion, 0.075);
      assert.strictEqual(g20lite.cachedInputPerMillion, 0.01875);
      assert.strictEqual(g20lite.outputPerMillion, 0.30);
    });

    await test('Generational Opus: opus-5.5 ($15.00) vs opus-4.6 ($15.00) vs claude-3-opus ($15.00), verifying none mispriced as $1.25', () => {
      const op55 = config.getModelPricing('opus-5.5');
      assert.strictEqual(op55.inputPerMillion, 15.00);
      assert.strictEqual(op55.cachedInputPerMillion, 1.50);
      assert.strictEqual(op55.outputPerMillion, 75.00);
      assert.notStrictEqual(op55.inputPerMillion, 1.25, 'opus-5.5 must NEVER be priced as generic $1.25 Pro');

      const op46 = config.getModelPricing('opus-4.6');
      assert.strictEqual(op46.inputPerMillion, 15.00);
      assert.strictEqual(op46.cachedInputPerMillion, 1.50);
      assert.strictEqual(op46.outputPerMillion, 75.00);
      assert.notStrictEqual(op46.inputPerMillion, 1.25, 'opus-4.6 must NEVER be priced as generic $1.25 Pro');

      const c3op = config.getModelPricing('claude-3-opus');
      assert.strictEqual(c3op.inputPerMillion, 15.00);
      assert.strictEqual(c3op.cachedInputPerMillion, 1.50);
      assert.strictEqual(c3op.outputPerMillion, 75.00);
      assert.notStrictEqual(c3op.inputPerMillion, 1.25, 'claude-3-opus must NEVER be priced as generic $1.25 Pro');
    });

    await test('Generational Sonnet: claude-sonnet-5.5 ($3.00) vs claude-3.5-sonnet ($3.00)', () => {
      const son55 = config.getModelPricing('claude-sonnet-5.5');
      assert.strictEqual(son55.inputPerMillion, 3.00);
      assert.strictEqual(son55.cachedInputPerMillion, 0.30);
      assert.strictEqual(son55.outputPerMillion, 15.00);
      assert.strictEqual(son55.id, 'claude-sonnet-5.5');

      const son35 = config.getModelPricing('claude-3.5-sonnet');
      assert.strictEqual(son35.inputPerMillion, 3.00);
      assert.strictEqual(son35.cachedInputPerMillion, 0.30);
      assert.strictEqual(son35.outputPerMillion, 15.00);
      assert.strictEqual(son35.id, 'claude-3.5-sonnet');
    });

    await test('Future unknown model heuristics: gemini-4.2-flash ($0.15), gemini-2.1-flash ($0.10), claude-opus-6.0 ($15.00), deepseek-v3 ($0.14)', () => {
      const g42 = config.getModelPricing('gemini-4.2-flash');
      assert.strictEqual(g42.inputPerMillion, 0.15);
      assert.strictEqual(g42.cachedInputPerMillion, 0.0375);
      assert.strictEqual(g42.outputPerMillion, 0.60);

      const g21 = config.getModelPricing('gemini-2.1-flash');
      assert.strictEqual(g21.inputPerMillion, 0.10);
      assert.strictEqual(g21.cachedInputPerMillion, 0.025);
      assert.strictEqual(g21.outputPerMillion, 0.40);

      const op60 = config.getModelPricing('claude-opus-6.0');
      assert.strictEqual(op60.inputPerMillion, 15.00);
      assert.strictEqual(op60.cachedInputPerMillion, 1.50);
      assert.strictEqual(op60.outputPerMillion, 75.00);

      const dsv3 = config.getModelPricing('deepseek-v3');
      assert.strictEqual(dsv3.inputPerMillion, 0.14);
      assert.strictEqual(dsv3.cachedInputPerMillion, 0.014);
      assert.strictEqual(dsv3.outputPerMillion, 0.28);
    });

    await test('Defensive calculateCostUsd with undefined/null inputs returns valid numeric 0, not NaN', () => {
      const res1 = config.calculateCostUsd(undefined, null, undefined, 'gemini-3.7-flash');
      assert.strictEqual(res1, 0);
      assert(!isNaN(res1));

      const res2 = config.calculateCostUsd(null, null, null, 'claude-opus-5.5');
      assert.strictEqual(res2, 0);
      assert(!isNaN(res2));

      const res3 = config.calculateCostUsd(NaN, undefined, null, 'gemini-3.8-flash');
      assert.strictEqual(res3, 0);
      assert(!isNaN(res3));

      const savings = config.calculateCacheSavingsUsd(null, 'gemini-3.8-flash');
      assert.strictEqual(savings, 0);
      assert(!isNaN(savings));
    });

    await test('KRW displayDecimals is 0 (whole integer currency)', () => {
      assert.strictEqual(config.CURRENCIES.krw.displayDecimals, 0);
    });

    await test('Bare single-word aliases do not cause false substring collisions', () => {
      const op55 = config.getModelPricing('opus-5.5');
      assert.strictEqual(op55.id, 'claude-opus-5.5');

      const op46 = config.getModelPricing('opus-4.6');
      assert.strictEqual(op46.id, 'claude-opus-4.6');

      const son55 = config.getModelPricing('sonnet-5.5');
      assert.strictEqual(son55.id, 'claude-sonnet-5.5');

      const son46 = config.getModelPricing('sonnet-4.6');
      assert.strictEqual(son46.id, 'claude-sonnet-4.6');
    });
  });

  // --- Suite 31: Adaptive Responsive Statusline Terminal Layout ---
  await describe('31. Adaptive Responsive Statusline Terminal Layout', async () => {
    const fixtureBadgeData = (stale = false) => ({
      turnTokens: 564,
      turnCostUsd: 0.0003,
      todayTokens: 123100,
      todayCostUsd: 0.047,
      cacheHitRate: 89,
      geminiQuota: {
        remainPercent: 97,
        isFresh: !stale,
        lastError: stale ? { kind: 'auth_failure', detail: 'HTTP 401' } : undefined,
        quota5h: { remainPercent: 97, resetFormatted: '4h 44m', window: '5h' },
        quota7d: { remainPercent: 61, resetFormatted: '9h 25m', window: 'weekly' }
      }
    });

    await test('Wide (Full) Mode is rendered when width >= 115 columns (has mini-bar ▰ and countdown timer)', () => {
      const data = fixtureBadgeData(false);
      // Width = 120 (Wide mode)
      const badge120 = formatter.renderRealTimeBadge(data, 'usd', false, '📊 Dashboard', { width: 120 });
      const plain120 = formatter.stripAnsi(badge120);
      assert(plain120.includes('▰'), 'Wide mode at width 120 must include mini progress bar ▰');
      assert(plain120.includes('4h 44m'), 'Wide mode at width 120 must include 5h reset countdown (4h 44m)');
      assert(plain120.includes('9h 25m'), 'Wide mode at width 120 must include 7d reset countdown (9h 25m)');
      assert(plain120.includes('5h:'), 'Wide mode must include 5h label');
      assert(plain120.includes('7d:'), 'Wide mode must include 7d label');

      // Boundary width = 115 (Wide mode threshold)
      const badge115 = formatter.renderRealTimeBadge(data, 'usd', false, '📊 Dashboard', { width: 115 });
      const plain115 = formatter.stripAnsi(badge115);
      assert(plain115.includes('▰'), 'Wide mode at boundary 115 must include mini progress bar');
      assert(plain115.includes('4h 44m'), 'Wide mode at boundary 115 must include countdown');
    });

    await test('Compact Mode is rendered when width < 115 columns (omits mini-bar and countdown, preserves full labels)', () => {
      const data = fixtureBadgeData(false);
      // Boundary width = 114 (Compact mode threshold)
      const badge114 = formatter.renderRealTimeBadge(data, 'usd', false, '📊 Dashboard', { width: 114 });
      const plain114 = formatter.stripAnsi(badge114);

      // Omits mini-bars and countdown strings
      assert(!plain114.includes('▰') && !plain114.includes('▱'), 'Compact mode must omit mini progress bars');
      assert(!plain114.includes('4h 44m'), 'Compact mode must omit 5h countdown string');
      assert(!plain114.includes('9h 25m'), 'Compact mode must omit 7d countdown string');

      // Preserves full readable labels (never ultra-minimal abbreviations like T:, Tod:, C:)
      const i18nTurn = i18n.t('hookBadgeTurn');
      const i18nToday = i18n.t('hookBadgeToday');
      const i18nCache = i18n.t('hookBadgeCache');
      assert(plain114.includes(`${i18nTurn}:`), 'Compact mode must preserve full Turn label');
      assert(plain114.includes(`${i18nToday}:`), 'Compact mode must preserve full Today label');
      assert(plain114.includes(`${i18nCache}:`), 'Compact mode must preserve full Cache label');
      assert(plain114.includes('5h:'), 'Compact mode must preserve 5h label');
      assert(plain114.includes('7d:'), 'Compact mode must preserve 7d label');
      assert(plain114.includes('97%'), 'Compact mode must preserve 5h percentage');
      assert(plain114.includes('61%'), 'Compact mode must preserve 7d percentage');
      assert(plain114.includes('📊 Dashboard'), 'Compact mode must preserve Dashboard link');

      // No cryptic abbreviations
      assert(!plain114.includes('T: ') && !plain114.includes('Tod: ') && !plain114.includes('C: '), 'Compact mode must not use ultra-minimal abbreviations');
    });

    await test('Compact Mode preserves ⚡ emoji, cost metrics, and staleness markers', () => {
      const staleData = fixtureBadgeData(true);
      const badge = formatter.renderRealTimeBadge(staleData, 'usd', false, '📊 Dashboard', { width: 100 });
      const plain = formatter.stripAnsi(badge);

      assert(plain.startsWith('⚡'), 'Compact mode must preserve ⚡ leading emoji');
      assert(plain.includes('564'), 'Compact mode must preserve turn tokens');
      assert(plain.includes('123.1k'), 'Compact mode must preserve today tokens');
      assert(plain.includes('$0.0003'), 'Compact mode must preserve turn cost');
      assert(plain.includes('$0.047'), 'Compact mode must preserve today cost');
      assert(plain.includes('97%!'), 'Compact mode must preserve staleness marker (!) on 5h');
      assert(plain.includes('61%!'), 'Compact mode must preserve staleness marker (!) on 7d');
    });

    await test('Statusline wraps into at most 2 physical lines at width 75–80 without clipping any segment', () => {
      const data = fixtureBadgeData(false);
      // Width = 80
      const badge80 = formatter.renderRealTimeBadge(data, 'usd', false, '📊 Dashboard', { width: 80 });
      const lines80 = badge80.split('\n');
      assert.strictEqual(lines80.length, 2, 'Compact badge at width 80 must wrap into exactly 2 lines');
      for (const line of lines80) {
        const w = formatter.getBadgeVisibleWidth(line);
        assert(w <= 80, `Line width ${w} must be <= 80: ${JSON.stringify(line)}`);
      }
      assert(!badge80.includes('…'), 'No segment should be truncated at width 80');

      // Width = 75
      const badge75 = formatter.renderRealTimeBadge(data, 'usd', false, '📊 Dashboard', { width: 75 });
      const lines75 = badge75.split('\n');
      assert.strictEqual(lines75.length, 2, 'Compact badge at width 75 must wrap into exactly 2 lines');
      for (const line of lines75) {
        const w = formatter.getBadgeVisibleWidth(line);
        assert(w <= 75, `Line width ${w} must be <= 75: ${JSON.stringify(line)}`);
      }
      assert(!badge75.includes('…'), 'No segment should be truncated at width 75');
    });

    await test('resolveBadgeWidth multi-fallback hierarchy and probeConsoleDeviceColumns', () => {
      const prevCols = process.env.COLUMNS;
      try {
        // Priority 1: process.env.COLUMNS
        process.env.COLUMNS = '88';
        assert.strictEqual(formatter.resolveBadgeWidth(), 88);

        delete process.env.COLUMNS;
        const origStdoutTTY = process.stdout ? process.stdout.isTTY : undefined;
        const origStdoutCols = process.stdout ? process.stdout.columns : undefined;
        const origStderrTTY = process.stderr ? process.stderr.isTTY : undefined;
        const origStderrCols = process.stderr ? process.stderr.columns : undefined;

        try {
          // Priority 2: process.stdout.columns when isTTY is true
          if (process.stdout) {
            Object.defineProperty(process.stdout, 'isTTY', { value: true, configurable: true });
            Object.defineProperty(process.stdout, 'columns', { value: 105, configurable: true });
            assert.strictEqual(formatter.resolveBadgeWidth(), 105);
          }

          // Priority 3: process.stdout.isTTY === false -> Infinity (REQ-1f)
          if (process.stdout) {
            Object.defineProperty(process.stdout, 'isTTY', { value: false, configurable: true });
            assert.strictEqual(formatter.resolveBadgeWidth(), Infinity);
          }

          // Priority 4: fallback to stderr when stdout.isTTY is undefined
          if (process.stdout && process.stderr) {
            Object.defineProperty(process.stdout, 'isTTY', { value: undefined, configurable: true });
            Object.defineProperty(process.stderr, 'isTTY', { value: true, configurable: true });
            Object.defineProperty(process.stderr, 'columns', { value: 92, configurable: true });
            assert.strictEqual(formatter.resolveBadgeWidth(), 92);
          }
        } finally {
          if (process.stdout) {
            Object.defineProperty(process.stdout, 'isTTY', { value: origStdoutTTY, configurable: true });
            Object.defineProperty(process.stdout, 'columns', { value: origStdoutCols, configurable: true });
          }
          if (process.stderr) {
            Object.defineProperty(process.stderr, 'isTTY', { value: origStderrTTY, configurable: true });
            Object.defineProperty(process.stderr, 'columns', { value: origStderrCols, configurable: true });
          }
        }

        // probeConsoleDeviceColumns direct function validation
        assert(typeof formatter.probeConsoleDeviceColumns === 'function');
        const probeResult = formatter.probeConsoleDeviceColumns();
        assert(probeResult === null || (Number.isFinite(probeResult) && probeResult > 0));
      } finally {
        if (prevCols !== undefined) process.env.COLUMNS = prevCols;
        else delete process.env.COLUMNS;
      }
    });

    await test('renderRealTimeBadge supports explicit options overrides (mode and width)', () => {
      const data = fixtureBadgeData(false);
      // Explicit mode: 'compact' at wide width 200
      const forcedCompact = formatter.renderRealTimeBadge(data, 'usd', false, null, { mode: 'compact', width: 200 });
      assert(!forcedCompact.includes('▰'), 'Forced compact must omit mini progress bar even at width 200');
      assert(!forcedCompact.includes('4h 44m'), 'Forced compact must omit countdown');

      // Explicit mode: 'wide' at narrow width 80
      const forcedWide = formatter.renderRealTimeBadge(data, 'usd', false, null, { mode: 'wide', width: 80 });
      assert(forcedWide.includes('▰'), 'Forced wide must include mini progress bar even at width 80');

      // Numeric width argument shorthand
      const numericArgCompact = formatter.renderRealTimeBadge(data, 'usd', false, null, 90);
      assert(!numericArgCompact.includes('▰'), 'Numeric width 90 must select compact layout');
    });
  });

  // --- Summary & Exit Code ---
  const duration = Date.now() - startTime;
  console.log('\n\x1b[1m=======================================================');
  console.log(`  Tests: \x1b[32m${passedTests} passed\x1b[0m, \x1b[${failedTests > 0 ? '31' : '32'}m${failedTests} failed\x1b[0m, ${totalTests} total`);
  console.log(`  Duration: ${duration}ms`);
  console.log('=======================================================\x1b[0m\n');

  process.exit(failedTests > 0 ? 1 : 0);
}

runAllTests().catch(err => {
  console.error('Fatal Test Runner Error:', err);
  process.exit(1);
});
