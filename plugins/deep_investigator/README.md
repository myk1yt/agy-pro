# Deep Investigator (🔎 Deep Research & Systems Verification Specialist)

[![Antigravity Plugin](https://img.shields.io/badge/Antigravity-Plugin-blue.svg)](https://github.com/google/antigravity)
[![Hardware Read-Only](https://img.shields.io/badge/Hardware-Read--Only-brightgreen.svg)](#1-identity--charter)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-lightgrey.svg)](#installation)

An empirical research and systems-level verification plugin for Google Antigravity CLI. **Deep Investigator** seamlessly unifies **Kimi-Grade Web Research Discipline** (Track A: Publisher Independence Gate, Claims Register, "What NOT to Claim" negative guardrails, untrusted XML sandboxing) with **Antigravity Low-Level Systems Engineering** (Track B: 4-Tier verification hierarchy for C++, Rust, TypeScript, and C#, Undefined Behavior prevention, Native AOT trimming, and cancellation safety).

---

## 🏛️ Architecture Overview

```text
                               ┌────────────────────────────────────────────────┐
                               │            Deep Investigator Agent             │
                               │          (Strictly Hardware Read-Only)         │
                               └──────────────────────┬─────────────────────────┘
                                                      │
                         ┌────────────────────────────┴───────────────────────────┐
                         ▼                                                        ▼
         [Track A: Web Research Discipline]                    [Track B: Systems Engineering]
         ├── Untrusted Web XML Sandboxing                      ├── Tier 1: Local Codebase / Native Inspect
         ├── Publisher-Level Independence Gate                 ├── Tier 2: Live CLI & Compiler Introspection
         ├── 4-Model / Multi-Source Quorum                     ├── Tier 3: Official Canonical Docs
         ├── Claims Register ([C-01], [C-02])                  └── Tier 4: Formal Specifications & RFCs
         └── "What NOT to Claim" Guardrails                          (C++, Rust, TypeScript, C# Recipes)
                         │                                                        │
                         └────────────────────────────┬───────────────────────────┘
                                                      ▼
                                       [4-Level Uncertainty Matrix]
                                 (Certain, Probable, Ambiguous, Unverifiable)
                                                      │
                                                      ▼
                                           [Context Shield Protocol]
                                      reports/<topic>-research.md
                                      Master Payload (<1k tokens)
```

---

## 🛠️ Included Components

- **Agent**: `deep-investigator` (`plugins/deep_investigator/agents/deep-investigator/agent.md`)
  - Standalone and subagent executable with `commandExecutionPolicy: ask_user`.
  - Excludes all write/edit tools (`write_to_file`, `replace_file_content`), ensuring strictly non-destructive inspection.
  - `run_command` is bounded to non-destructive CLI introspection; arbitrary binary execution and mutating redirections are forbidden.
- **Skill**: `deep-investigator` (`plugins/deep_investigator/skills/deep-investigator/SKILL.md`)
  - Complete verification playbooks, hardened XML sandboxing with nonces/CDATA sanitization, cross-platform introspection commands, and report schemas.
- **Configuration**: `plugin.json` (`plugins/deep_investigator/plugin.json`)

---

## 🔒 Security Model & Execution Boundaries

Deep Investigator enforces a strict non-destructive security model designed for zero-trust environments:

1. **Hardware Read-Only & Policy Enforcement**:
   - `commandExecutionPolicy: ask_user`: CLI command invocations require user approval.
   - Exclusion of write/edit tools (`write_to_file`, `replace_file_content`).
2. **Command Execution Boundaries (`run_command`)**:
   - **Permitted**: Non-destructive CLI introspection (`--version`, `cl.exe /Bv`, `cargo check`, `npx tsc --noEmit`, syntax-only diagnostics `clang++ -fsyntax-only`).
   - **Strictly Prohibited**: Shell output redirections (`>`, `>>`, `Out-File`, `Set-Content`), mutating commands, arbitrary binary compilation/executions (`./test`, `.exe`), and script execution.
3. **Hardened Untrusted Web XML Sandboxing**:
   - External content from `read_url_content` and `search_web` is wrapped in `<untrusted_web_source nonce="..." url="..." domain="...">` with dynamic nonces.
   - **CDATA Delimiter Sanitization**: Escapes `]]>` delimiters (`]]]]><![CDATA[>` or `]] >`) to block prompt injection breakout attacks.
   - **Argument Poisoning Prevention**: External web data is strictly passive text and must never be passed as CLI arguments or inputs to execution tools.
4. **Cross-Platform Portability**:
   - Introspection recipes supply platform-aware equivalents (Windows PowerShell `NUL | Select-String` alongside POSIX `/dev/null | grep`).
   - Report templates use portable generic paths (`file:///path/to/reports/...`) with zero hardcoded developer paths.

---

## 📋 Dual-Track Feature Breakdown

### Track A: Web Research Discipline (Kimi Grade)
1. **Untrusted Web Data XML Sandboxing**: Protects context by wrapping external content in `<untrusted_web_source url="...">` tags, neutralizing prompt injection vectors.
2. **Publisher-Level Independence Gate**: Rejects syndicated cross-posts and SEO echo chambers; traces assertions to primary maintainers.
3. **Claims Register**: Every factual assertion is tagged with `[C-xx]`, source attribution, confidence score, and verification status (`VERIFIED`, `CONTRADICTED`, `UNCONFIRMED`).
4. **"What NOT to Claim" Guardrails**: Explicitly lists unverified boundaries to eliminate hallucination.

### Track B: Systems Engineering Verification
1. **4-Tier Hierarchy of Truth**: Local Code $\to$ CLI Introspection $\to$ Official Docs $\to$ Standards/RFCs.
2. **C++ Systems**: Feature test macros (`__cpp_*`), Undefined Behavior audit, memory ordering (`acquire`/`release`), ASan/TSan sanitizer checks.
3. **Rust Systems**: Edition semantics (`2021`/`2024`), `unsafe` invariant audit (`// SAFETY:`), Stacked Borrows verification via Miri, Clippy lints.
4. **TypeScript Systems**: Compiler strictness flags (`strict`, `noUncheckedIndexedAccess`), type narrowing vs unsafe casting, runtime schema validation (Zod/TypeBox) at boundaries.
5. **C# / .NET Systems**: Native AOT trimming warnings (`[RequiresUnreferencedCode]`), memory optimization (`Span<T>`, `ReadOnlySpan<T>`), cancellation propagation (`CancellationToken`), async state machine overhead.

---

## 📄 License

MIT
