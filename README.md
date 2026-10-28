# Antigravity & Gemini Configuration, Rules & Multi-Agent Skills Suite

A production-ready, shareable configuration bundle for **Antigravity CLI** and **Google Gemini** agents.

This suite provisions global governance rules, multi-agent lifecycle orchestration protocols, real-time token tracking analytics, and turnkey lifecycle hooks.

---

## 📦 What's Inside

```text
.
├── rules/
│   ├── AGENTS.md                   # Global Autonomous Multi-Agent Orchestrator Protocol
│   └── GEMINI.md                   # General Gemini zero-dep, cross-platform engineering rules
├── skills/
│   ├── usage/
│   │   └── SKILL.md                # /usage token, cache hit rate & API cost analytics skill
│   └── autonomous-orchestrator/
│       └── SKILL.md                # 7-Stage Multi-Agent Orchestrator & Double-Blind QA runbook
├── hooks/
│   └── hooks.json                  # Antigravity PostInvocation turn badge lifecycle hook
├── scripts/
│   ├── install.bat                 # 1-click Windows installer (installs into %USERPROFILE%\.gemini)
│   ├── install.sh                  # 1-click macOS/Linux installer (installs into ~/.gemini)
│   ├── uninstall.bat               # 1-click Windows uninstaller (removes ONLY this bundle's files)
│   └── uninstall.sh                # 1-click macOS/Linux uninstaller (removes ONLY this bundle's files)
├── LICENSE                         # MIT License (Copyright (c) 2026 kim,yong-tai)
└── README.md                       # Documentation and usage guide
```

---

## 🚀 1-Click Installation

### Windows (Command Prompt / PowerShell)
```cmd
scripts\install.bat
```

### macOS / Linux
```bash
chmod +x scripts/install.sh
./scripts/install.sh
```

The installer automatically copies all rules, skills, and hooks into your global Antigravity/Gemini configuration home:
- **Windows**: `%USERPROFILE%\.gemini\`
- **macOS / Linux**: `~/.gemini/`

### Uninstallation

To remove this configuration bundle from your machine, run the matching uninstaller. It deletes **only** the files this bundle installed (rules, the two global skills, and `hooks.json`) — nothing else in your `~/.gemini` directory is touched.

**Windows (Command Prompt / PowerShell):**
```cmd
scripts\uninstall.bat
```

**macOS / Linux:**
```bash
chmod +x scripts/uninstall.sh
./scripts/uninstall.sh
```

After running it, these paths will no longer exist: `~/.gemini/rules/AGENTS.md`, `~/.gemini/rules/GEMINI.md`, `~/.gemini/AGENTS.md`, `~/.gemini/GEMINI.md`, `~/.gemini/skills/usage/`, `~/.gemini/skills/autonomous-orchestrator/`, `~/.gemini/hooks/hooks.json`.

---

## 🛠️ Included Skills & Tools

### 1. Autonomous Orchestrator (`skills/autonomous-orchestrator`)
- **Charter**: Governs complex multi-domain feature implementation via a 7-Stage Multi-Agent Lifecycle.
- **Invariants**: Strict Zero-Source-Edit and Zero-Monolithic-Execution rules for Master Agent; enforces delegation to domain workers and blind QA verifiers.
- **Workflow**:
  ```text
  [Intent] ➔ [Decompose] ➔ [Strategy] ➔ [Adversarial Audit] ➔ [SRP Plan] ➔ [Worker Exec] ➔ [Blind QA] ➔ [Korean Delivery]
  ```

### 2. Token & Cost Tracker (`skills/usage`)
- **Commands**: `/usage`, `/tokens`, `/cost`
- **CLI Commands**:
  - `agy-tokens` — Quick daily token consumption & estimated cost check
  - `agy-tokens --7d` / `agy-tokens --30d` — Multi-day trend breakdowns
  - `agy-tokens --session` — Turn-by-turn conversation drilldown
  - `agy-tools dashboard` — Full-screen interactive terminal dashboard
  - `agy-tokens --lang ko|en|ja|zh` — Multi-language output

### 3. Lifecycle Hooks (`hooks/hooks.json`)
- **PostInvocation Hook**: Automatically runs `agy-tokens --hook` after every turn to render a lightweight 1-line real-time status badge:
  ```text
  ⚡ Turn 14 | In: 1.2k | Cached: 24.5k (95.3%) | Out: 480 | Cost: $0.0031
  ```

---

## 🌐 Multi-Language Policy
- **Internal System Engine**: English ONLY for code comments, type signatures, docstrings, subagent prompts, and audit records.
- **User Communication**: 100% fluent, professional **Korean (한국어)** for user-facing responses, explanations, and walkthroughs.

---

## 📄 License
MIT License - Copyright (c) 2026 kim,yong-tai.
