---
name: usage
description: Antigravity Token & Cost Analytics. Provides real-time token tracking, prompt cache hit rate analytics, cost breakdowns by model, and interactive dashboard directly within Antigravity conversations.
---

# `/usage` Skill - Antigravity Token & Cost Analytics

Provides real-time token tracking, cache hit rate analytics, and API cost breakdown directly within Antigravity conversations.

## Description
When the user asks about token consumption, costs, API usage, or executes the `/usage` command, run `agy-tokens` or `agy-tools` to generate structured analytics.

## Triggers & Keywords
- `/usage`
- `/tokens`
- `/cost`
- "How many tokens did we use today?"
- "What is our estimated API cost this week?"
- "Show conversation token breakdown"
- "Open token dashboard"

## Execution Syntax

### 1. Quick Daily Check
```bash
agy-tokens
```
Outputs today's total tokens, cached tokens, generation tokens, cache hit rate %, and estimated cost.

### 2. 7-Day & 30-Day Trend Breakdown
```bash
agy-tokens --7d
agy-tokens --30d --currency krw
```

### 3. Custom Date Range
```bash
agy-tokens --range 2026-08-01..2026-08-27 --currency eur
```

### 4. Turn-by-Turn Conversation Drilldown
```bash
# Latest session
agy-tokens --session

# Specific session ID
agy-tokens --session <session-id>
```

### 5. Multi-Language Output
```bash
agy-tokens --7d --lang ko
agy-tokens --today --lang ja
agy-tokens --30d --lang zh
```

### 6. Interactive Terminal Dashboard
```bash
agy-tools dashboard
```
Launches the full-screen terminal dashboard with real-time turn monitoring, daily charts, and cache efficiency metrics.

### 7. Lifecycle Hook Status Line
```bash
agy-tokens --hook
```
Produces a compact single-line badge summarizing the latest turn's tokens, cache rate, and cost.

### 8. Machine-Readable JSON
```bash
agy-tokens --today --json
```

---

## Metrics & Definitions
- **Input Tokens**: Fresh prompt tokens sent to the model (user prompt + tool outputs + un-cached context).
- **Cached Tokens**: Prompt tokens served from Antigravity / Gemini / Claude prompt cache (billed at 75%-90% discount).
- **Output Tokens**: Generated model tokens (thoughts + tool calls + text).
- **Cache Hit %**: Percentage of total prompt context fulfilled by prompt caching: `Cached / (Input + Cached) * 100`.
- **Estimated Cost**: Total calculated API cost based on the active model's pricing tier.
- **Cache Savings**: Theoretical dollar amount saved thanks to prompt caching.

---

## Model Pricing Tiers Reference (USD per 1M Tokens)

| Model Name | Input ($/M) | Cached Input ($/M) | Output ($/M) |
|---|---|---|---|
| `gemini-2.5-flash` | $0.15 | $0.0375 | $0.60 |
| `gemini-2.5-pro` | $1.25 | $0.3125 | $5.00 |
| `gemini-2.0-flash` | $0.10 | $0.025 | $0.40 |
| `claude-3-5-sonnet` | $3.00 | $0.30 | $15.00 |
| `claude-3-7-sonnet` | $3.00 | $0.30 | $15.00 |
