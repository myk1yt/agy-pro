---
name: mode
description: >
  Profile switcher runbook between full-stack and clean profiles. Switches between full (VP of Engineering orchestration, 40+ skills, multi-model fanout swarm) and clean (minimal stock Antigravity CLI) modes.
---

# Mode Runbook — Full-Stack & Clean Profile Switcher

## 1. Purpose & Profile Architecture
The `/mode` skill manages switching between Antigravity CLI execution profiles:
- **`full` (Full-Stack Mode)**:
  - VP of Engineering orchestration charter
  - Complete 40+ domain skills portfolio (design, audit, FFI, testing, security, research)
  - Multi-model verification fanout (`agy pro fanout`)
  - Strict Definition of Done, double-blind verification, and intent conformance gates
- **`clean` (Clean Mode)**:
  - Clean slate baseline matching stock Antigravity CLI
  - Zero heavy orchestration rules or prompt injection overhead
  - Native toolset and conversational speed

## 2. Execution Commands
Invoke profile operations via `agy pro mode`:
- Switch to Full mode:
  ```bash
  agy pro mode full
  ```
- Switch to Clean mode:
  ```bash
  agy pro mode clean
  ```
- Query active profile status (mode, last backup, managed manifest):
  ```bash
  agy pro mode status
  ```
- Query combined status (profile mode + registered providers + real `agy` path):
  ```bash
  agy pro status
  ```

## 3. Switching Protocol & Governance
1. Validate requested target profile (`full` or `clean`).
2. Verify active profile state from the agy-pro state registry (`~/.agy-pro/state.json`); the managed
   configuration targets live in `~/.gemini/config`.
3. Atomic profile swap:
   - Synchronize rules (`rules/AGENTS.md`, `rules/GEMINI.md`)
   - Synchronize skill directory trees (`skills/**`)
   - Update active profile marker
4. Notify user of mode change and whether a session restart (`/reload` or new conversation) is recommended.
