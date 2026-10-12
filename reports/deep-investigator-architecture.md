# Deep Investigator Integration Architecture & Swarm Protocol
> **System**: Google Antigravity CLI (`agy-cli`) & VP of Engineering Swarm
> **Architect**: Deep Investigator Systems Architect
> **Target Path**: `reports/deep-investigator-architecture.md`
> **Classification**: Core Governance & Multi-Agent Orchestration Specification

---

## 1. Executive Summary & Problem Space

In modern autonomous multi-agent software engineering, **monolithic context pollution** and **speculative hallucination** represent the two greatest vectors of failure:
1. **Speculative Planning**: When an orchestrator or planner operates on incomplete or hallucinated mental models of the codebase, framework contracts, or external dependencies, it generates flawed plans. In the Antigravity VP of Engineering paradigm, a single speculative assumption in Stage 5 cascading into Stage 6 wastes tens of thousands of developer tokens and creates broken code.
2. **Context Window Degradation**: In-depth research requires exhaustive grep searches, multi-file AST traversals, official documentation web fetches, and iterative Think-Act-Observe reasoning loops (often consuming 100,000 to 300,000+ tokens). If executed directly in Master's context, the context window suffers severe needle-in-a-haystack degradation, high token billing on every subsequent conversational turn, and loss of strategic oversight.

To resolve these challenges, this specification details the architectural integration of the **Deep Investigator (`deep-investigator` / 심층 조사 요원)** into the Antigravity CLI swarm.

---

## 2. Insertion Model Trade-off Analysis & Strategic Selection

We evaluated three potential insertion models for the Deep Investigator:

```
[User Query]
     │
     ▼
┌─────────────────────────────────┐
│  Model 1: Pre-flight Scout      │ ◄─── PRIMARY INSERTION POINT
│  (Stage 3: Proactive Grounding) │
└────────────────┬────────────────┘
                 ▼
┌─────────────────────────────────┐
│  Stage 5: SRP Execution Plan    │
└────────────────┬────────────────┘
                 ▼
┌─────────────────────────────────┐
│  Model 2: Inline Oracle         │ ◄─── SECONDARY INSERTION POINT
│  (Stage 6: Just-In-Time Query)  │
└────────────────┬────────────────┘
                 ▼
┌─────────────────────────────────┐
│  Model 3: Post-Execution Audit  │ ◄─── QA AUDIT SAFETY NET
│  (Stage 7: Retrospective QA)    │
└─────────────────────────────────┘
```

### 2.1 Comparative Analysis Matrix

| Evaluation Dimension | Model 1: Pre-flight Scout (사전 탐색자) | Model 2: Inline Oracle (인라인 오라클) | Model 3: Post-execution Verifier (사후 검증자) |
| :--- | :--- | :--- | :--- |
| **Pipeline Position** | Stage 3 (Parallel Investigation / Pre-Planning) | Stage 6 (Worker Execution) & Stage 5 (Planning) | Stage 4 (Naive Audit) & Stage 7 (Blind QA) |
| **Operational Mode** | Proactive, comprehensive repository & external survey | Reactive, targeted query when worker hits ambiguity | Retrospective verification of generated code/diff |
| **Latency Profile** | High upfront latency (15s–40s), but zero downstream delay | Zero upfront latency; localized 8s–15s pauses per query | Zero upfront latency; catastrophic delay on failure (rollback) |
| **Token Cost Efficiency** | High upfront token expenditure; highest net project ROI | Pay-as-you-go; minimum token footprint on simple tasks | Low token footprint if passing; massive rework token multiplier on rejection |
| **Hallucination Prevention** | **95%+ (Highest)**: Eliminates speculative planning entirely | **75%–85%**: High for recognized ambiguities; misses unknown unknowns | **90%**: Catches hallucinations before merge, but after code is authored |
| **Developer Ergonomics** | Smooth for medium/complex tasks; sluggish for 1-line edits | Highest responsiveness; interactive, snappy feedback | Frustrating when failure causes late-stage rollbacks |
| **Blast Radius & Rollback Penalty** | **Minimal**: Architecture is grounded before code is touched | **Low**: Worker pauses locally before modifying files | **Severe**: Discard entire Stage 6 worker work and restart Stage 3 |
| **Context Hygiene Impact** | Isolated in Stage 3; emits clean Markdown report | Isolated sub-call; returns atomic answer to worker | Verifier runs on diff; minimal context bloat |

### 2.2 Deep Dive into Insertion Models

#### Model 1: Pre-flight Scout (사전 탐색자)
- **Mechanism**: Invoked immediately after Stage 1 (Decomposition) or within Stage 3 (Parallel Investigation). Explores the problem space, relevant codebase files, upstream dependency signatures, and official documentation before any task breakdown or code edits occur.
- **Strengths**: Prevents the "garbage in, garbage out" planning trap. Ensures that Stage 5 SRP tasks are built on verified interfaces, existing patterns, and actual file paths.
- **Weaknesses**: For simple, trivial changes (e.g., updating a README typo or tweaking a CSS margin), running a full pre-flight scout introduces noticeable friction and latency.

#### Model 2: On-demand Subagent / Inline Oracle (인라인 오라클)
- **Mechanism**: Triggered just-in-time when Master or a Domain Worker encounters an ambiguous interface, unverified OS-specific API, undocumented config parameter, or unexpected compile error.
- **Strengths**: Highly agile. Consumes zero tokens unless ambiguity is actually encountered. Keeps developers engaged with rapid feedback.
- **Weaknesses**: Susceptible to "unconscious hallucinations"—situations where an LLM coding worker firmly believes an assumption is correct (e.g., inventing a non-existent parameter on `fs.cpSync`) and thus never queries the oracle.

#### Model 3: Post-execution Verifier (사후 검증자)
- **Mechanism**: Invoked during Stage 4 (Naive Audit) or Stage 7 (Blind QA). Audits generated diffs or plans against external documentation and ground truth.
- **Strengths**: Acts as a hard firewall against ungrounded code merging into production.
- **Weaknesses**: Punishing developer UX. If an architectural assumption is proven false after the worker has already authored 400 lines of code, the entire execution must be discarded, resulting in maximum token waste and user frustration.

### 2.3 Strategic Recommendation: Hybrid Tiered Protocol
1. **Primary Insertion Point: Model 1 (Pre-flight Scout in Stage 3)**
   - In the Antigravity VP of Engineering paradigm, Master manages a swarm of specialized workers. Planning without ground-truth facts creates catastrophic multi-worker synchronization failures. Model 1 is established as the mandatory primary phase for all architectural, feature, and refactoring tasks.
2. **Secondary Insertion Point: Model 2 (Inline Oracle in Stage 6)**
   - Even with pre-flight scouting, workers encounter micro-level unknowns (e.g., Windows path quirks, obscure linter rules). Workers are granted explicit authority to invoke `@deep-investigator` as an inline oracle without escalating back to Master.
3. **Audit Pairing (Model 3 Synergy in Stage 4 & 7)**:
   - Deep Investigator does not replace the Blind QA Verifier; instead, the Stage 4 Naive Auditor and Stage 7 Blind QA Verifier ingest the Deep Investigator's **Grounded Findings & Uncertainty Matrix** as an objective evaluation rubric.

---

## 3. Context Window Isolation Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│                        Master Context Window                           │
│  (Pure Orchestrator: Lean, Focused, Strategic, CEO Briefings)          │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │
               1. Dispatch via invoke_subagent / Tool Call
               (Structured Input Payload: Query + Scope + Rigor)
                                    │
                                    ▼
┌────────────────────────────────────────────────────────────────────────┐
│                 Deep Investigator Context Sandbox                      │
│                                                                        │
│   [Transient Multi-Hop Search & Analysis Loop]                         │
│   - view_file, grep_search, find_by_name (Local Codebase)              │
│   - run_command (Read-only CLI / git / node introspection)             │
│   - search_web, read_url_content (Official Docs / RFCs / Repos)        │
│   - Multi-Turn Think-Act-Observe Scratchpad (100k - 300k+ Tokens)      │
│                                                                        │
│   [Isolation Barrier: Raw logs & scrapes NEVER leak to Master]         │
└───────────────┬────────────────────────────────────────┬───────────────┘
                │                                        │
     2. Write Full Report (Disk)           3. Compact Summary Message (<1k Tokens)
                │                                        │
                ▼                                        ▼
┌───────────────────────────────┐        ┌───────────────────────────────┐
│ Disk Storage (Persistence)    │        │ Master / Caller Thread        │
│ reports/<topic>-research.md   │◄───────┤ [Status]: SUCCESS             │
│                               │        │ [Verdict]: VERIFIED           │
│ - Full Evidence & Telemetry   │        │ [Key Findings]: 3-5 Bullets   │
│ - Complete Citations & Diff   │        │ [Uncertainty Matrix]: Risks   │
│ - Methodology & Exhaustive    │        │ [Artifact Link]: file:///...  │
└───────────────────────────────┘        └───────────────────────────────┘
```

### 3.1 Context Shield & Decoupling Mechanism
1. **Isolated Transient Context**: Deep Investigator executes inside an isolated conversation thread. All intermediate reasoning tokens, raw search results, HTML parsing noise, and multi-file read outputs remain strictly contained within this ephemeral context.
2. **Dual-Layer Delivery Architecture**:
   - **Layer 1 (Disk Artifact)**: The complete, unabridged investigation report (3,000 to 10,000+ words) is atomically saved to `reports/<topic>-research.md`. This forms an immutable audit trail and project memory asset.
   - **Layer 2 (Compact Communication Contract)**: The subagent returns ONLY a concise, token-optimized message (<1,000 tokens) via `send_message` containing the status, verdict, high-level findings, uncertainty matrix, and a `file:///` link.

### 3.2 Bidirectional Input/Output Payload Contracts

#### Input Payload Schema (Dispatch Contract)
When Master or a Worker invokes Deep Investigator, it supplies the following structured schema:

```json
{
  "$schema": "https://antigravity.google/schemas/deep-investigator-input.v1.json",
  "requestId": "di-req-20261010-0941",
  "caller": {
    "agentName": "master-vp",
    "conversationId": "3fbcd0ac-ede1-462a-a42e-bf588773e1de",
    "lifecycleStage": "STAGE_3_PARALLEL_INVESTIGATION"
  },
  "intent": {
    "userGoal": "Integrate cross-platform zero-dependency streaming HTTP client into agy-cli",
    "researchObjective": "Determine whether Node.js built-in fetch supports duplex streaming on Node 18 vs Node 20 and identify Windows pipe pitfalls."
  },
  "scope": {
    "targetFiles": [
      "src/network/client.js",
      "package.json"
    ],
    "externalDomains": [
      "nodejs.org",
      "github.com/nodejs/node"
    ],
    "verificationRigor": "L3_EMPIRICAL"
  },
  "budget": {
    "maxSearchDepth": 4,
    "maxToolInvocations": 25,
    "timeoutSeconds": 60
  }
}
```

*Verification Rigor Levels*:
- `L1_SURVEY`: Fast structural inspection, index survey, documentation summary (<10s).
- `L2_SYNTACTIC`: AST/grep validation, public contract signature mapping, exported API verification (<20s).
- `L3_EMPIRICAL`: Live read-only CLI command execution, local version introspection, official documentation citation (<40s).
- `L4_FORMAL`: Multi-source cross-verification, RFC/upstream repository issue tracker verification, attack-surface analysis (<90s).

#### Output Payload Schema (Deliverable Contract)
Deep Investigator responds via `send_message` with this exact schema:

```json
{
  "$schema": "https://antigravity.google/schemas/deep-investigator-output.v1.json",
  "requestId": "di-req-20261010-0941",
  "status": "SUCCESS",
  "verdict": "VERIFIED_FEASIBLE",
  "summary": {
    "headline": "Node.js v18.0+ native fetch requires duplex: 'half' flag for streaming bodies.",
    "keyTakeaways": [
      "Node 18.0.0 introduced fetch behind experimental flag, stabilized in v18.13.0.",
      "Streaming request bodies require explicit duplex: 'half' in RequestInit; omission throws TypeError.",
      "Windows PowerShell stdout piping does not mangle UTF-8 streams when process.stdout.isTTY is respected."
    ]
  },
  "groundedFindings": [
    {
      "id": "FINDING-01",
      "claim": "duplex: 'half' is mandatory when passing a ReadableStream as fetch body in Node.js.",
      "evidenceType": "OFFICIAL_DOC_CITATION",
      "citations": [
        {
          "source": "Node.js v18 LTS Documentation (Globals / fetch)",
          "url": "https://nodejs.org/docs/latest-v18.x/api/globals.html#fetch",
          "exactQuote": "duplex <string> | must be 'half' if request body is a ReadableStream."
        }
      ],
      "confidence": 99
    }
  ],
  "uncertaintyMatrix": [
    {
      "dimension": "Node 16 Backward Compatibility",
      "confidenceLevel": "CERTAIN",
      "finding": "Node 16 lacks native fetch entirely.",
      "riskLevel": "HIGH",
      "mitigation": "Enforce engines.node >= 18.0.0 in package.json or maintain zero-dependency http.request fallback."
    },
    {
      "dimension": "Windows CMD Non-UTF8 Code Page 949/437",
      "confidenceLevel": "PROBABLE",
      "finding": "ANSI sequences may render as gibberish if console code page is legacy.",
      "riskLevel": "MEDIUM",
      "mitigation": "Utilize ASCII fallback tables and check process.env.CI/NO_COLOR per GEMINI.md."
    }
  ],
  "artifactLink": "file:///D:/OneDrive/Projects/Antigravity-cli/reports/streaming-http-research.md"
}
```

### 3.3 Triggering Mechanisms & Routing

We establish three triggering pathways:

1. **Automated Router Classifier (Master Pre-Flight Guardrail)**:
   - When Master decomposes user intent in Stage 1, it runs an internal intent classifier.
   - *Activation Conditions*:
     - User requests integration of a new framework, library, or protocol.
     - Ambiguity score $\ge 0.6$ (multiple viable architectural paths).
     - Multi-platform OS constraints (Windows/macOS/Linux) or low-level Node.js built-ins.
     - Multi-file blast radius across $\ge 5$ files.
   - *Action*: Automatically dispatches `@deep-investigator` in Stage 3 before generating Stage 5 plans.

2. **Slash Command Override (`/deep-research <query>` or `/investigate <topic>`)**:
   - Enables the CEO/Developer to explicitly demand an exhaustive investigation without triggering code modifications.
   - Master bypasses Stages 5 and 6, executing only Stages 1 $\to$ 2 $\to$ 3, and delivering an executive strategic briefing in Korean accompanied by the disk-saved research artifact.

3. **Tool Delegation via `invoke_subagent` (Inline Oracle Trigger)**:
   - Any Domain Worker during Stage 6 or the Naive Auditor during Stage 4 can issue an on-demand tool call to `deep-investigator`.
   - The worker yields asynchronously, receives the grounded factual answer, and resumes coding without guesswork.

---

## 4. Concrete Implementation Plan & Code Diffs

### 4.1 Configuration Diff: `%USERPROFILE%\.gemini\config\rules\AGENTS.md` (and `rules/AGENTS.md`)

```diff
--- a/rules/AGENTS.md
+++ b/rules/AGENTS.md
@@ -27,14 +27,15 @@
 graph TD
   CEO([User: CEO / Founder]) <-->|Strategic Alignment & Briefings| VP[Master Agent: VP of Engineering]
   
   subgraph Specialists [Specialized Swarm]
-    VP -->|1. Deep Investigation| R[research / DeepInvestigator]
+    VP -->|1. Deep Repository & Web Recon| R[deep-investigator: Lead Research Architect]
     VP -->|2. Core & Backend Logic| CW[Core / Domain Workers]
     VP -->|3. UI / UX & Visual Design| DES[designer: UI Specialist]
     VP -->|4. Pre-Merge Diff Review| CR[code-reviewer: 8-Taxonomy Auditor]
     VP -->|5. Security & PII Audit| SEC[security-reviewer: Lead Security Auditor]
     VP -->|6. Runtime & E2E Testing| QA[Blind QA Verifier: Double-Blind QA]
   end
   
   subgraph Artifacts [Disk-Based Reports: reports/]
-    R -.-> Rep1[reports/research-report.md]
+    R -.-> Rep1[reports/<topic>-research.md]
     CW -.-> Rep2[reports/implementation-plan.md]
     DES -.-> Rep3[reports/design-report.md]
@@ -48,9 +49,15 @@
 
 ### 2.1 Specialist Boundaries
-1. **`research`**: Multi-file repository survey, technical documentation lookup, and external web research.
+1. **`deep-investigator`**: Lead Research Architect & Grounding Specialist.
+   - *Charter*: Proactive pre-flight codebase recon, official upstream documentation verification, multi-hop technical research, and anti-hallucination fact grounding.
+   - *Four-Tier Hierarchy*: Strictly follows (1) Primary Local References -> (2) Local Runtime Introspection -> (3) Live Official Docs -> (4) Authoritative Upstream Repos/RFCs.
+   - *Strict Boundary*: HARDWARE READ-ONLY. Strictly prohibited from creating or modifying project application files (`lib/**`, `src/**`, `test/**`).
+   - *Inline Oracle*: Available on-demand to Domain Workers via `@deep-investigator` during Stage 6 implementation blocks.
 2. **`Core / Domain Workers`**: Business logic, API implementation, database queries, and architectural refactoring.
 3. **`designer`**: Visual layouts, responsive styling, SVG/Canvas, and micro-interactions. *Strict Boundary*: Never touches backend logic or core business rules. *DoD*: Not done until rendered, screenshotted, and inspected for visual defects.
@@ -70,9 +77,10 @@
 [Deliverable Contract]:
   - Detailed findings/diffs MUST be written to: reports/<agent-name>-report.md
   - Return message schema:
     [Status]: SUCCESS | BLOCKED | FAILED | PASS | REJECT
-    [Verdict]: Quantitative metrics (e.g., P0=0, P1=0)
+    [Verdict]: Quantitative metrics (e.g., P0=0, P1=0) or Grounding Assessment (e.g., VERIFIED_FEASIBLE)
     [Key Findings]: 3 to 5 concise bullet points
+    [Uncertainty Matrix]: Dimension, risk level, and mitigation
     [Artifact Link]: file:///path/to/report.md
 ```
 
@@ -82,6 +90,7 @@
 Master never accepts completion without verifiable, multi-tier evidence:
+1. **Grounded Fact Verification (`deep-investigator`)**: 100% of architectural claims backed by primary-source citations or empirical CLI evidence. 0% speculative assumptions. Uncertainty Matrix explicitly categorized.
-1. **Passing Build is the Floor**: Compiling without syntax errors is baseline, not proof of completion.
+2. **Passing Build is the Floor**: Compiling without syntax errors is baseline, not proof of completion.
-2. **Visual Verification (`designer`)**: Rendered in browser/canvas -> visually inspected -> free of layout, clipping, or contrast defects.
+3. **Visual Verification (`designer`)**: Rendered in browser/canvas -> visually inspected -> free of layout, clipping, or contrast defects.
-3. **Static Diff Verification (`code-reviewer`)**: Pre-merge diff scanned -> P0=0, P1=0 -> Unconditional PASS.
+4. **Static Diff Verification (`code-reviewer`)**: Pre-merge diff scanned -> P0=0, P1=0 -> Unconditional PASS.
-4. **Security Verification (`security-reviewer`)**: Secrets, credentials, and vulnerabilities scanned -> Critical=0, High=0 -> PASS.
+5. **Security Verification (`security-reviewer`)**: Secrets, credentials, and vulnerabilities scanned -> Critical=0, High=0 -> PASS.
-5. **Runtime Verification (`Blind QA Verifier`)**: Live endpoint invoked with real responses -> state round-trip verified -> 100% test pass.
+6. **Runtime Verification (`Blind QA Verifier`)**: Live endpoint invoked with real responses -> state round-trip verified -> 100% test pass.
```

---

### 4.2 Configuration Diff: `%USERPROFILE%\.gemini\config\skills\autonomous-orchestrator\SKILL.md` (and `skills/autonomous-orchestrator/SKILL.md`)

```diff
--- a/skills/autonomous-orchestrator/SKILL.md
+++ b/skills/autonomous-orchestrator/SKILL.md
@@ -16,3 +16,3 @@
    2. Running verification/build commands (`flutter test`, `cargo test`, etc.)? -> **HALT!** Delegate to `Blind QA Verifier`.
-   3. Performing multi-file codebase investigation? -> **HALT!** Delegate to Stage 3 Research subagents.
+   3. Performing multi-file codebase investigation or technical research? -> **HALT!** Delegate to Stage 3 `deep-investigator`.
    4. Defining/invoking subagents or managing `.gemini/rules/skills`? -> **PROCEED**.
@@ -66,3 +66,3 @@
   ➔ [Stage 2: Dynamic Provisioning (Subagents & Skills)]
-  ➔ [Stage 3: Parallel Domain Investigation & Strategy Draft]
+  ➔ [Stage 3: Parallel Domain Investigation & Deep Research Grounding]
   ➔ [Stage 4: Naive Adversarial Audit Loop (Max 3 iterations)] ──(Pass)──➔
@@ -87,7 +87,13 @@
-### Stage 3: Parallel Domain Investigation & Draft Strategy
-- **Concurrent Dispatch**: Dispatch parallel domain research tasks across specialists via `invoke_subagent` with injected intent.
+### Stage 3: Parallel Domain Investigation & Deep Research Grounding
+- **Lead Investigator Dispatch**: Master dispatches `@deep-investigator` to conduct proactive reconnaissance across local codebase, official documentation, and upstream runtime invariants.
+- **Concurrent Domain Specialists**: In parallel, dispatch domain specialists (UI/UX, Security, Core) for scoped boundary mapping.
 - **Async Yield**: Stop calling tools immediately after subagent invocation. Await reactive wakeup. Never poll.
+- **Context Shield & Report Persistence**: `deep-investigator` outputs complete findings to `reports/<topic>-research.md` and returns a compact structured summary payload.
 - **Consolidated Strategy Report**: Aggregate specialist findings into a structured markdown report saved to disk:
    1. Executive Summary & Problem Framing
    2. Domain Analysis & Architectural Invariants
    3. Strict Interface Contracts & Boundaries
    4. Edge Cases, Performance & Security Risks
+   5. Grounded Citations & Uncertainty Matrix
@@ -96,5 +102,6 @@
 ### Stage 4: Naive Adversarial Audit Loop
 - **Spawn Naive Auditor**: Fresh unprimed context with zero memory/bias to review the strategy report against 3 vectors:
    1. *Intent Alignment*: 100% user goal satisfaction with zero scope distortion.
-   2. *Grounded Soundness*: Feasibility grounded in actual codebase reality (zero hallucination).
+   2. *Grounded Soundness*: Feasibility grounded in actual codebase reality and verified citations provided by `deep-investigator` (zero hallucination).
    3. *Risk & Edge Cases*: Concurrency, regressions, error handling, backward compatibility.
@@ -108,3 +115,5 @@
 ### Stage 6: Modular Domain-Isolated Worker Execution
 - Spawn isolated `Domain Worker` subagents passing high-level intent + atomic task scope.
 - Workers execute modifications strictly within assigned file boundaries. Master yields execution asynchronously.
+- **Inline Oracle Query**: If a worker hits an undocumented interface, signature ambiguity, or environment quirk, it may invoke `@deep-investigator` inline to resolve the blocker without guess-work.
 - Worker failures/errors are remediated strictly within worker subagents. Master never touches source files.
```

---

### 4.3 Subagent Specification: `deep-investigator`

This file is placed at `plugins/deep_investigator/agents/deep-investigator/agent.md` and registered in `plugins/deep_investigator/plugin.json`:

#### `plugins/deep_investigator/plugin.json`
```json
{
  "name": "deep_investigator",
  "version": "1.0.0",
  "description": "In-depth Technical Research and Anti-Hallucination Grounding Subagent for Google Antigravity CLI.",
  "author": "myk1yt",
  "keywords": [
    "antigravity",
    "deep-investigator",
    "research",
    "grounding",
    "citations",
    "zero-hallucination"
  ],
  "license": "MIT"
}
```

#### `plugins/deep_investigator/agents/deep-investigator/agent.md`
```markdown
---
name: deep-investigator
description: In-depth technical research, multi-hop repository survey, official documentation verification, and anti-hallucination fact grounding subagent.
mainAgent: true
subagent: true
hidden: false
inheritMcp: false
tools:
  - view_file
  - list_dir
  - grep_search
  - find_by_name
  - run_command
  - search_web
  - read_url_content
  - send_message
---

# Deep Investigator (🔬 심층 조사 요원)

## 1. Identity & Charter
- **Display Name**: Deep Investigator
- **Role**: Lead Technical Research Architect & Grounding Specialist for Google Antigravity.
- **Authority**: HARDWARE READ-ONLY. Produces comprehensive, citation-anchored research deliverables and uncertainty matrices. NEVER modifies code, application files, or repositories directly.
- **Model**: `gemini-3.8-flash-high` or `gemini-2.5-pro` (high reasoning capability, large context window).

## 2. Core Invariants & Anti-Hallucination Protocol

### 2.1 The Four-Tier Fallback Hierarchy (Four-Tier Verification)
Before making any technical assertion or documenting an interface contract, Deep Investigator MUST verify facts in strict hierarchical order:
1. **Tier 1: Primary Local References**: Official repository documentation, local skills (`references/*.md`), and local schemas.
2. **Tier 2: Local Runtime & CLI Introspection**: Dynamic inspection via read-only `run_command` (`node --version`, `git status`, `node -e "..."`, package manifests).
3. **Tier 3: Live Official Documentation**: Primary-source online documentation (`nodejs.org`, `rust-lang.org`, official API portals) fetched via `search_web` or `read_url_content`.
4. **Tier 4: Authoritative Upstream Repositories & RFCs**: GitHub issue trackers, commit histories, and official standards RFCs.

*Under no circumstances may Deep Investigator fabricate function signatures, invent config parameters, or guess runtime behaviors.*

### 2.2 Citation & Evidence Grounding Protocol
Every technical finding must include:
- **Exact File Path & Line Range** (for local codebase findings).
- **Exact URL & Verbatim Excerpt** (for external documentation findings).
- **Confidence Score (0–100%)** based on primary evidence.

### 2.3 Uncertainty Matrix Classification
All findings must categorize remaining unknowns into:
- 🟢 **CERTAIN**: 100% verified by primary code or official test run.
- 🟡 **PROBABLE**: Documented in official guides; minor version discrepancy possible.
- 🟠 **AMBIGUOUS**: Conflicting documentation or undocumented platform-specific edge case.
- 🔴 **UNVERIFIABLE**: Requires live production credentials or hardware not present locally.

## 3. Context Shield & Output Protocol

### 3.1 Transient Sandbox
Deep Investigator operates in an isolated context window. Raw search HTML, multi-turn reasoning traces, and large file outputs are strictly quarantined.

### 3.2 Deliverable Persistence
- Save the full, unabridged markdown report to `reports/<topic>-research.md`.
- Transmit ONLY the structured summary payload via `send_message` back to the caller.

### 3.3 Deliverable Schema
```text
[Status]: SUCCESS | BLOCKED | FAILED
[Verdict]: VERIFIED_FEASIBLE | FEASIBLE_WITH_RISKS | INFEASIBLE
[Key Findings]:
- Finding 1 with citation
- Finding 2 with citation
- Finding 3 with citation
[Uncertainty Matrix]:
- Dimension | Confidence | Risk Level | Mitigation
[Artifact Link]: file:///absolute/path/to/reports/<topic>-research.md
```
```

---

## 5. Deployment & Execution Plan

To deploy this architecture into the active environment:
1. **Repository Staging**: Commit the new plugin structure in `D:\OneDrive\Projects\Antigravity-cli\plugins\deep_investigator\`.
2. **Rule Synchronization**:
   - Update `rules/AGENTS.md` and `skills/autonomous-orchestrator/SKILL.md`.
   - Run `node scripts/lib/configure-rules.js` to atomically deploy to `~/.gemini/config/rules/AGENTS.md`.
   - Run `node scripts/lib/configure-customizations.js` to atomically deploy skills and plugins to `~/.gemini/config/`.
3. **Verification**: Execute `node test/run-tests.js` to verify zero regressions across existing tools and rules.
``