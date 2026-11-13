/**
 * @fileoverview Curated provider and model presets for the simplified agy-pro gateway CLI.
 * Ported from agy-pro/internal/providers/presets.go with native wireProtocol defaults
 * (google -> genai, all others -> openai) and loopback local-gateway / custom presets.
 */

'use strict';

/**
 * Default Google AI Studio base URL for native GenAI wire protocol.
 * Native `agy` (>= v1.5.3) ignores trailing `/v1beta` and appends `:generateContent` / `:streamGenerateContent`.
 * @type {string}
 */
const GOOGLE_AI_STUDIO_BASE_URL = 'https://generativelanguage.googleapis.com/v1beta';

/**
 * Default Google AI Studio OpenAI-compatibility endpoint.
 * @type {string}
 */
const GOOGLE_AI_STUDIO_OPENAI_BASE_URL = 'https://generativelanguage.googleapis.com/v1beta/openai/';

/**
 * Default environment variable for Google AI Studio API keys.
 * @type {string}
 */
const GOOGLE_AI_STUDIO_KEY_ENV = 'GEMINI_API_KEY';

/**
 * Well-known cloud provider catalog in menu order.
 * @type {ReadonlyArray<object>}
 */
const KNOWN_PROVIDERS = Object.freeze([
  {
    name: 'deepseek',
    label: 'deepseek',
    baseURL: 'https://api.deepseek.com/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'DEEPSEEK_API_KEY',
    aliases: [],
    keyURL: 'https://platform.deepseek.com/api_keys'
  },
  {
    name: 'openrouter',
    label: 'openrouter',
    baseURL: 'https://openrouter.ai/api/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'OPENROUTER_API_KEY',
    aliases: [],
    keyURL: 'https://openrouter.ai/keys'
  },
  {
    name: 'groq',
    label: 'groq',
    baseURL: 'https://api.groq.com/openai/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'GROQ_API_KEY',
    aliases: [],
    keyURL: 'https://console.groq.com/keys'
  },
  {
    name: 'openai',
    label: 'openai',
    baseURL: 'https://api.openai.com/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'OPENAI_API_KEY',
    aliases: [],
    keyURL: 'https://platform.openai.com/api-keys'
  },
  {
    name: 'bigmodel',
    label: 'bigmodel (Zhipu GLM)',
    baseURL: 'https://open.bigmodel.cn/api/paas/v4',
    wireProtocol: 'openai',
    apiKeyEnv: 'BIGMODEL_API_KEY',
    aliases: ['glm', 'zhipu'],
    keyURL: ''
  },
  {
    name: 'google',
    label: 'google (Google AI Studio)',
    baseURL: GOOGLE_AI_STUDIO_BASE_URL,
    wireProtocol: 'genai',
    apiKeyEnv: GOOGLE_AI_STUDIO_KEY_ENV,
    aliases: ['gemini', 'google ai studio', 'aistudio'],
    keyURL: 'https://aistudio.google.com/apikey'
  },
  {
    name: 'together',
    label: 'together',
    baseURL: 'https://api.together.xyz/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'TOGETHER_API_KEY',
    aliases: [],
    keyURL: ''
  },
  {
    name: 'mistral',
    label: 'mistral',
    baseURL: 'https://api.mistral.ai/v1',
    wireProtocol: 'openai',
    apiKeyEnv: 'MISTRAL_API_KEY',
    aliases: ['mistralai'],
    keyURL: 'https://console.mistral.ai/api-keys'
  }
]);

/**
 * Built-in local loopback and custom gateway presets.
 * @type {ReadonlyArray<object>}
 */
const EXTRA_PROVIDERS = Object.freeze([
  {
    name: 'local-gateway',
    label: 'local-gateway (Ollama / LiteLLM / vLLM)',
    baseURL: 'http://127.0.0.1:11434/v1',
    wireProtocol: 'openai',
    apiKeyEnv: '',
    aliases: ['local', 'ollama', 'litellm', 'vllm', 'lmstudio'],
    keyURL: ''
  },
  {
    name: 'custom',
    label: 'custom (Other OpenAI / GenAI endpoint)',
    baseURL: '',
    wireProtocol: 'openai',
    apiKeyEnv: '',
    aliases: ['other'],
    keyURL: ''
  }
]);

/**
 * Curated model presets keyed by canonical provider name.
 * @type {Record<string, ReadonlyArray<object>>}
 */
const MODEL_PRESETS = Object.freeze({
  deepseek: [
    {
      id: 'deepseek-chat',
      family: 'deepseek',
      description: 'DeepSeek-V3 (128k context, tools enabled)',
      recommended: true,
      contextLength: 128000,
      tags: ['flagship', 'tools', 'coding']
    },
    {
      id: 'deepseek-reasoner',
      family: 'deepseek',
      description: 'DeepSeek-R1 (128k context, reasoning/thinking)',
      recommended: false,
      contextLength: 128000,
      tags: ['reasoning', 'coding', 'math']
    }
  ],
  openrouter: [
    {
      id: 'anthropic/claude-3.7-sonnet',
      family: 'claude',
      description: 'Claude 3.7 Sonnet (200k context, hybrid thinking & tools)',
      recommended: true,
      contextLength: 200000,
      tags: ['flagship', 'tools', 'coding', 'reasoning']
    },
    {
      id: 'deepseek/deepseek-r1',
      family: 'deepseek',
      description: 'DeepSeek R1 (128k context, reasoning/thinking)',
      recommended: false,
      contextLength: 128000,
      tags: ['reasoning', 'coding']
    },
    {
      id: 'meta-llama/llama-3.3-70b-instruct',
      family: 'llama',
      description: 'Llama 3.3 70B Instruct (128k context, tools enabled)',
      recommended: false,
      contextLength: 128000,
      tags: ['tools', 'open-weights']
    },
    {
      id: 'openai/gpt-4o',
      family: 'gpt',
      description: 'GPT-4o (128k context, multimodal & tools)',
      recommended: false,
      contextLength: 128000,
      tags: ['flagship', 'vision', 'tools']
    }
  ],
  groq: [
    {
      id: 'llama-3.3-70b-versatile',
      family: 'llama',
      description: 'Llama 3.3 70B Versatile (Ultra-fast, 128k context, tools)',
      recommended: true,
      contextLength: 128000,
      tags: ['fast', 'tools']
    },
    {
      id: 'deepseek-r1-distill-llama-70b',
      family: 'deepseek',
      description: 'DeepSeek R1 Distill Llama 70B (Fast reasoning, 128k context)',
      recommended: false,
      contextLength: 128000,
      tags: ['fast', 'reasoning']
    }
  ],
  openai: [
    {
      id: 'gpt-4o',
      family: 'gpt',
      description: 'GPT-4o (128k context, flagship multimodal & tools)',
      recommended: true,
      contextLength: 128000,
      tags: ['flagship', 'vision', 'tools']
    },
    {
      id: 'gpt-4o-mini',
      family: 'gpt',
      description: 'GPT-4o Mini (128k context, lightweight, tools enabled)',
      recommended: false,
      contextLength: 128000,
      tags: ['fast', 'tools']
    },
    {
      id: 'o3-mini',
      family: 'o-series',
      description: 'o3-mini (200k context, reasoning/STEM)',
      recommended: false,
      contextLength: 200000,
      tags: ['reasoning', 'coding']
    }
  ],
  bigmodel: [
    {
      id: 'glm-4.6',
      family: 'glm',
      description: 'GLM-4.6 (200k context, advanced coding, reasoning & tools)',
      recommended: true,
      contextLength: 200000,
      tags: ['flagship', 'tools', 'coding', 'reasoning']
    },
    {
      id: 'glm-4.5-flash',
      family: 'glm',
      description: 'GLM-4.5-Flash (free tier, 128k context, thinking & tools)',
      recommended: false,
      contextLength: 128000,
      tags: ['free', 'fast', 'tools', 'reasoning']
    }
  ],
  google: [
    {
      id: 'gemini-2.5-flash',
      family: 'gemini',
      description: 'Gemini 2.5 Flash (1M context, Google AI Studio, vision & thinking)',
      recommended: true,
      contextLength: 1048576,
      tags: ['fast', 'tools', 'vision', 'reasoning']
    },
    {
      id: 'gemini-2.5-pro',
      family: 'gemini',
      description: 'Gemini 2.5 Pro (1M context, Google AI Studio, vision & thinking)',
      recommended: false,
      contextLength: 1048576,
      tags: ['flagship', 'tools', 'vision', 'reasoning']
    }
  ],
  together: [
    {
      id: 'meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo',
      family: 'llama',
      description: 'Llama 3.1 70B Turbo (128k context, tools enabled)',
      recommended: true,
      contextLength: 128000,
      tags: ['fast', 'tools']
    },
    {
      id: 'deepseek-ai/DeepSeek-R1',
      family: 'deepseek',
      description: 'DeepSeek R1 (128k context, reasoning enabled)',
      recommended: false,
      contextLength: 128000,
      tags: ['reasoning']
    }
  ],
  mistral: [
    {
      id: 'mistral-large-latest',
      family: 'mistral',
      description: 'Mistral Large Latest (128k context, tools enabled)',
      recommended: true,
      contextLength: 128000,
      tags: ['flagship', 'tools']
    },
    {
      id: 'codestral-latest',
      family: 'mistral',
      description: 'Codestral Latest (256k context, specialized coding)',
      recommended: false,
      contextLength: 256000,
      tags: ['coding', 'tools']
    }
  ]
});

/**
 * Clones a provider preset entry so callers cannot mutate catalog constants.
 * @param {object} p
 * @returns {object}
 */
function cloneProvider(p) {
  return {
    name: p.name,
    label: p.label,
    baseURL: p.baseURL,
    wireProtocol: p.wireProtocol,
    apiKeyEnv: p.apiKeyEnv,
    aliases: [...(p.aliases || [])],
    keyURL: p.keyURL || ''
  };
}

/**
 * Returns the provider catalog in menu order.
 * @param {{ includeCustom?: boolean }} [options]
 * @returns {Array<object>}
 */
function knownProviders(options = {}) {
  const list = KNOWN_PROVIDERS.map(cloneProvider);
  if (options && options.includeCustom) {
    for (const extra of EXTRA_PROVIDERS) {
      list.push(cloneProvider(extra));
    }
  }
  return list;
}

/**
 * Folds a provider name for alias comparison: lowercase, removing spaces, `_`, `-`, `.`.
 * @param {string} name
 * @returns {string}
 */
function providerAliasKey(name) {
  return String(name || '')
    .replace(/^\ufeff/, '')
    .trim()
    .toLowerCase()
    .replace(/[\s_.-]+/g, '');
}

/**
 * Resolves a provider name or alias to its canonical catalog name.
 * Unknown names are returned trimmed and lowercased.
 * @param {string} name
 * @returns {string}
 */
function canonicalProviderName(name) {
  const cleaned = String(name || '').replace(/^\ufeff/, '').trim().toLowerCase();
  const key = providerAliasKey(cleaned);
  if (!key) return '';

  const all = [...KNOWN_PROVIDERS, ...EXTRA_PROVIDERS];
  for (const h of all) {
    if (key === providerAliasKey(h.name)) {
      return h.name;
    }
    for (const alias of h.aliases || []) {
      if (key === providerAliasKey(alias)) {
        return h.name;
      }
    }
  }
  return cleaned;
}

/**
 * Returns the provider preset entry for a canonical name or alias, or null.
 * @param {string} name
 * @returns {object|null}
 */
function lookupProviderHint(name) {
  const canonical = canonicalProviderName(name);
  if (!canonical) return null;
  const all = [...KNOWN_PROVIDERS, ...EXTRA_PROVIDERS];
  const hit = all.find((h) => h.name === canonical);
  return hit ? cloneProvider(hit) : null;
}

/**
 * Returns curated popular models for a known provider (by name or alias).
 * @param {string} providerName
 * @returns {Array<object>}
 */
function getModelPresets(providerName) {
  const canonical = canonicalProviderName(providerName);
  const presets = MODEL_PRESETS[canonical];
  if (!presets) return [];
  return presets.map((m) => ({
    ...m,
    tags: [...(m.tags || [])]
  }));
}

/**
 * Derives a default API key environment variable name for a provider or profile name.
 * @param {string} name
 * @returns {string}
 */
function defaultEnvForProvider(name) {
  const hint = lookupProviderHint(name);
  if (hint && hint.name !== 'custom' && hint.name !== 'local-gateway') {
    return hint.apiKeyEnv;
  }
  if (hint && hint.name === 'local-gateway') {
    return '';
  }
  const normalized = String(name || '')
    .replace(/^\ufeff/, '')
    .trim()
    .toUpperCase()
    .replace(/[^A-Z0-9_]+/g, '_')
    .replace(/^_+|_+$/g, '');
  if (!normalized) return 'AGY_PRO_GATEWAY_API_KEY';
  return `AGY_PRO_GATEWAY_${normalized}_API_KEY`;
}

module.exports = {
  GOOGLE_AI_STUDIO_BASE_URL,
  GOOGLE_AI_STUDIO_OPENAI_BASE_URL,
  GOOGLE_AI_STUDIO_KEY_ENV,
  KNOWN_PROVIDERS,
  EXTRA_PROVIDERS,
  MODEL_PRESETS,
  knownProviders,
  canonicalProviderName,
  lookupProviderHint,
  getModelPresets,
  defaultEnvForProvider
};
