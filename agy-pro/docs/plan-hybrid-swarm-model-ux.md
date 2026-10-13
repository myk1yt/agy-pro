# Plan: Hybrid Native/External Swarm + Combined Model Picker + Swarm UX

Status: PLANNED (not started) - written for a FRESH session to pick up.
Date: 2026-10-12 | Project: agy-pro (D:/OneDrive/Projects/Antigravity-cli, branch feature/agy-pro merged to main locally)
Author context: previous session delivered the bridge-side tool loop; this plan continues from there.

## 0. Executive summary (KR)
사용자 목표 3건: (G1) 실 agy 네이티브 Gemini 좌석과 외부 provider 좌석을 섞는 하이브리드 스웜(경로 2), (G2) `/model`에서 네이티브+외부 모델을 한 번에 보이게, (G3) 스웜 좌석 2~4 선택 UX 개선.
핵심 사실: 단일 `/model` 팝업의 네이티브+외부 혼합은 agy.exe 하드코딩 XOR로 불가(바이너리 패치 없이는 영구). 따라서 G2는 (B1) agy-pro 자체 통합 픽커로 "전부 목록"을 구현하고, (B2) 게이트웨이 목록 내 Gemini는 AI Studio 프리셋으로 편입, (B3) 바이너리 패치는 기각.
G1은 fanout에 `native` 시트 타입(실 agy 서브프로세스)을 추가하는 중규모 작업. G3는 `agy pro swarm` 인터랙티브 빌더.

## 1. User goals (verbatim)
1. "경로2로 하고 싶어" = hybrid native seat in fanout (real agy subprocess seats + external provider seats in ONE swarm).
2. "/model 한번에 네이티브모델과 외부모델 전부가 나오도록" = combined native+external model visibility.
3. "seat 2~4를 보다 선택하기 쉽도록 UX, UI를 개선" = ergonomic seat selection for 3-4 seat code-review / cyber-security swarms ("kimi code style").
4. (2026-10-12 추가) "seat배정같은것을 코드로 하게 하지 말고, 번호로 선택하도록" + "agy-cli 터미널 상에서 /setting_agypro 같은것을 만들어서 그 안에서 agy pro와 관련된 모든 설정을 할 수 있도록... 최대한 antigravity native와 비슷하게" = numbered seat selection and an in-TUI native-styled settings command (see §4.5).
Deliverable requested: this .md so a NEW session can start immediately.

## 2. What ALREADY EXISTS (DO NOT REBUILD)
Merged to local main (24 commits ahead of origin/main, NO push per user policy). Previous session's reports live in docs/261011_0001_session_agy-pro-research/, docs/261011_0002_session_agy-pro-completion/, docs/261012_0001_session_bridge-tool-loop/ (gitignored working artifacts - read them from disk).
- Go shim (agy-pro/): passthrough with recursion latch, interactive launcher + `agy pro config default-profile`, per-session crypto/rand bridge bearer, probe doctor + `--fix-keys`/`--rollback-keys` (DPAPI v2 backups, advapi32 registry, BOM-tolerant key resolution).
- Bridge-side tool execution loop (internal/bridge/bridge_tools.go): external providers perform workspace actions inside gateway sessions. Default read,search; opt-in write,exec via `AGY_PRO_BRIDGE_TOOLS` env > `agy pro config bridge-tools`. Budgets 8 iter / 240s wall / 60s turn / 16 calls per turn / 30s per tool. Rune-safe SSE, stream:false branch, redacted audit log, VCS-meta write refusal.
- agenttools: read_file/list_dir/grep/glob/write_file/replace_in_file/run_command, fail-closed ParseToolTokens, sanitized env, symlink guard, protected-write guard.
- fanout swarm (internal/fanout/): double-blind seats, family-rotation selector, 3-step failover, consensus reconciliation (>=2 = CONFIRMED), seat reports + summary.json + reconciliation.md. Seat types TODAY = provider/OpenAI-compatible only (no native seat - verified absent: no exec of real agy in internal/fanout).
- providers presets: google (Google AI Studio, GEMINI_API_KEY, https://generativelanguage.googleapis.com/v1beta/openai/), bigmodel (GLM), nw, charm + wizard. `agy pro fanout --models "google/gemini-2.5-flash,nw/glm-5.3-flash,..."` ALREADY runs mixed Gemini+external swarms via AI Studio seats.
- Installer: one-line install/uninstall (README), PATH backup, strict doctor gate, wizard handoff, recycle-bin uninstall.
- Installed shim at ~/.agy-pro/bin/agy.exe is current (sha256-verified); real agy at %LOCALAPPDATA%\agy\bin\agy.exe v1.3.3.

## 3. Hard constraints (evidence-based, do not relitigate)
1. Binary XOR: `buildGatewayAvailableModels` (VA 0x141d19940) replaces ALL native models with gateway models when AGY_LLM_GATEWAY_MODELS is set; requests route to the bridge, never to aicode.googleapis.com. Evidence: reports/gateway-models-coexistence-experiment.md, reports/user-model-coexistence-ux-report.md, docs/261011_0002_session_agy-pro-completion/qa-tool-parity-report.md. Single-popover native(3.8 preview)+external = IMPOSSIBLE without binary patching.
2. agy.exe gateway client never sends `tools`/`tool_choice` and discards `tool_calls` (qa-tool-parity-report.md §4). External tool parity = bridge loop (DONE). Native seats get agy's own 54 tools for free (research-native-tool-inventory.md).
3. Security gates from previous session bind: no secrets in repo/logs; recycle-bin-only deletion; headless-only verification (no visible windows, never kill user's live agy sessions); run_command env scrub; .git write refusal; redaction. Preserve them.
4. User policy: NO GitHub push without explicit approval. Provider keys stay local-only. k3-family and Charm-family models were excluded from the SUBAGENT pool due to credits - re-check credit status at session start (Charm reported billing_error on the agy-pro provider too).
5. Go stdlib-only in agy-pro (no new deps).

## 4. Design

### 4.1 G1 - Hybrid native seats (core deliverable)
Concept: fanout gains a second seat kind. A `native` seat runs the REAL agy binary as a subprocess in print mode; a `provider` seat uses the existing OpenAI-compatible client. Both feed the same reconciliation pipeline.

Seat spec syntax (proposal): `--seats "native:gemini-3.8-flash-high,native:gemini-3.8-pro,nw/glm-5.3-flash,bigmodel/glm-4.6"` or via templates (4.3). Selector keeps family-rotation for provider seats; native seats are pinned by model id.

Native seat execution:
- Spawn: real agy (reuse launcher's real-agy resolution: AGY_PRO_REAL_AGY > PATH scan > %LOCALAPPDATA%\agy\bin\agy.exe) with `--model <id> -p "<seat prompt>" --print-timeout <swarm timeout>`. Set seat working dir = swarm root. IMPORTANT: verify the exact print-mode flags against real agy v1.3.3 (`agy --help`) at implementation time; QA evidence shows `-p`/`--print` work and `--print-timeout` exists.
- Output capture: stdout as the seat answer; stderr captured for diagnostics. Exit code mapping: 0 = completed, else seat failed with reason (failover chain applies: native seat failure falls back to... nothing within native family; document: failed native seat becomes a failed seat in reconciliation, same as provider failover exhaustion).
- Recursion: the shim spawns real agy via the existing passthrough mechanism (AGY_PRO_ACTIVE latch logic must be honored - the latch currently prevents shim recursion, which is what we want; the spawned agy must NOT re-enter the shim's bridge activation unless intended).
- Tools: native seats use agy's own tool surface (54 tools, its own sandbox/approval model). Do NOT wrap them in agenttools. Provider seats use the bridge-loop or fanout client tool loop as today.
- Concurrency: real agy is a 193MB binary; cap native seats at 2 concurrent (configurable), rest queued; provider seats stay parallel. Document quota impact: native = free Antigravity quota (rate limits apply mid-swarm).
- Provenance: every seat report records `kind: native|provider`, model id, quota/cost note, so reconciliation tables show which side said what.

Acceptance (G1):
- A1 `agy pro fanout --seats "native:gemini-3.8-flash-high,nw/<model>,charm-or-bigmodel/<model>" --prompt <task>` completes with 3 seat reports + reconciliation.md where at least one seat is kind:native with verbatim agy output.
- A2 Native seat failure (bad model id / quota exhausted) degrades to a failed seat entry; swarm continues; no panic; no textless hang.
- A3 Concurrency cap enforced (3 native seats -> max 2 concurrent, test with a stub binary).
- A4 No secrets in any seat report/log; recycle-bin/headless rules intact.

### 4.2 G2 - Combined model visibility ("한번에 전부")
Reality table:
| Where | Native 3.8 preview | Gemini public 2.5 | External (GLM/DeepSeek...) |
|---|---|---|---|
| agy's own /model, native mode | YES | YES | NO (XOR) |
| agy's own /model, gateway mode | NO | YES (via google AI Studio preset) | YES |
| agy-pro unified picker (B1) | YES | YES | YES |

Implementation (B1, recommended): agy-pro's own combined inventory + picker.
- `agy pro models --all`: one list = native catalog (shim invokes real `agy models` once, caches ~5s) + providers registry (existing DisplayName/capability API), grouped `[native] Gemini 3.8 Flash (High) ...` / `[google] gemini-2.5-flash ...` / `[nw] glm-5.3-flash ...` . There is no separate picker subcommand; the interactive picker for the same list is the launcher's `[2]` sub-menu on a bare `agy` (next bullet).
- Launcher upgrade: the 2s picker's option [2] becomes "choose a model" sub-menu listing the combined inventory; selecting a native model execs real agy native with `-m <id>`; selecting an external model activates the bridge and runs agy gateway (existing path). Also usable non-interactively: `agy -m "<model> (<provider>)"` already routes external; extend `-m` to accept bare native ids (already works as passthrough - verify).
- `agy pro swarm` seat builder (4.3) consumes the SAME combined inventory - one source of truth.
B2 (already works): document prominently that gateway mode's /model shows Gemini 2.5 + external together when the google preset is registered.
B3 binary patch: REJECT (update fragility, integrity, ToS). Record as rejected option; do not pursue unless user re-asks with full awareness.

Acceptance (G2):
- B1-1 `agy pro models --all` shows >=18 native + all registered external models with group tags and capabilities, no duplicates.
- B1-2 Picker: selecting a native entry launches real agy native session; selecting external launches gateway session with bridge tool loop active. Both verified live.
- B1-3 Plain `agy` UX unchanged unless user opts into the new picker (default profile semantics preserved).

### 4.3 G3 - Swarm UX (seat 2~4 selection)
New command `agy pro swarm` (interactive builder + non-interactive flags):
1. Task template: [1] code-review [2] security-audit [3] custom prompt. Templates seed prompt scaffolding + tool defaults (code-review: read,search; security: read,search + adversarial framing).
2. Seat multi-select from the combined inventory: numbered list with checkboxes-style input ("1,3,4" or "n,n,e,e" shorthands), default presets like "2 native + 2 external" / "3 external (family-rotated)" / "4 mixed". Show per-seat provenance + estimated cost/quota per seat.
3. Tools + budget summary (reuse ParseToolTokens surface), then preview the full fanout invocation and run (or print the equivalent `agy pro fanout --seats ...` command for copy-paste).
4. Output: existing reconciliation.md/summary.json + a compact console summary table (seat | kind | model | verdict).
Non-interactive: `agy pro swarm --template code-review --seats "native:gemini-3.8-flash-high,nw/glm-5.3-flash,bigmodel/glm-4.6" --root <dir>`.

Acceptance (G3):
- C1 A user builds a 4-seat mixed swarm in <=6 keystrokes-equivalent (numbered defaults + Enter) and it runs.
- C2 Non-interactive flags cover the same surface; CI-safe (no TTY prompts when flags present).
- C3 Console summary renders seat provenance; no ANSI/OSC escapes in non-TTY.

### 4.4 Reconciliation policy on disagreement (user decision 2026-10-12)
When seats disagree (2:2 split, 2-seat tie, or a singleton Blocker/High), the MAIN AGENT (the orchestrating session agent that launched the swarm) rules on it - not the user by default.
- Flow: reconciliation.md surfaces each disputed item with both positions -> main agent reads the two seat reports (evidence, not rhetoric) -> issues a ruling + one-line rationale recorded as `arbitration:` on the item -> only items the main agent cannot settle (missing evidence, product judgment) escalate to the user.
- Bias mitigation: the arbiter must rule on quoted evidence (file:line / repro) only; seat identity/provenance stays visible in the record but rhetoric is not a tiebreaker. Record dissent: "seat X maintained Y" stays in the report.
- Implementation options (pick at U1 kickoff): (O1, default) no fanout code change - the session's main agent performs arbitration as part of its synthesis workflow (this is already the charter pattern); (O2) optional `--arbiter` pass in fanout that calls a designated model to auto-rule disputed items and stamp `arbitration:` lines in reconciliation.md. If O2 lands, the name must not collide with the old fake `--arbiter` docs (manual.txt history) - keep docs truthful.
- Acceptance addition: A5 disputed items in a mixed swarm end with a recorded main-agent ruling (or an explicit "escalated to user" note), never a silent drop.

### 4.5 G4 - Numbered seat assignment + in-TUI settings (user decisions 2026-10-12)
G4-a Numbered seats, NOT code/flags: the happy path never requires writing `--seats "native:gemini-3.8-flash-high,nw/..."`. The swarm builder (4.3) and the picker (4.2 B1) present the combined inventory as a NUMBERED list (`[1] Gemini 3.8 Flash (High) [native] ... [5] glm-5.3-flash [nw] ...`) and the user types `1,3,5` or accepts a numbered default preset. Flags stay as a power-user/CI fallback only, and docs lead with numbers.
G4-b `/setting_agypro` inside the agy terminal (user verbatim: "agy-cli 터미널 상에서 /setting_agypro 같은것을 만들어서 그 안에서 agy pro와 관련된 모든 설정을 할 수 있도록", "최대한 antigravity native와 비슷하게"): ONE in-TUI place for ALL agy-pro settings - default profile, bridge-tools, providers (add/test/rename), swarm templates + default seat presets (numbered), budgets, doctor status, uninstall/update info. UX must follow Antigravity native TUI conventions (panel/keybinding/visual language), NOT a shell menu and NOT a web page.
- Mechanism (verify first, then implement): the repo ships plugins/ (agy_help, code_reviewer, deep_investigator, designer, security_reviewer) - inspect their manifest format and how they register slash commands in agy. If agy supports plugin slash commands, implement `/setting_agypro` as a real plugin that drives the existing `agy pro config`/providers/mode operations. If slash-command plugins are NOT supported, degrade honestly: (a) `agy pro settings` as a full-screen TUI styled to match Antigravity native, plus (b) a skill/hook so `/setting_agypro` in the agy prompt surfaces the right entry. NEVER fake a native slash command that does not exist.
- Numbered selection is the single selection language across launcher picker, swarm builder, and settings seat presets.

### 4.6 Model pool update (2026-10-12)
Charm credits RESTORED (user). Subagent pool: k3 family still EXCLUDED; everything else usable (MiMo, NW/*, Charm/*; Google/gemini-3.8-flash remains excluded by standing system rule). Re-verify credit status at session start.

### 4.7 Acceptance additions (G4)
- D1 Happy-path seat assignment requires zero flag-writing: numbered input only (verified by a scripted TTY transcript).
- D2 `/setting_agypro` (or its documented degrade path) covers every `agy pro config` surface + providers + swarm presets; no setting is reachable only via flags.
- D3 Look-and-feel matches Antigravity native conventions (captured terminal buffer compared against a native agy panel screenshot-style capture; headless rules apply - no visible window on the user's screen).

### 4.8 Kickoff prompt for the new session (copy-paste)
```
agy-pro/docs/plan-hybrid-swarm-model-ux.md 를 읽고 새 이니셔티브를 착수해줘.
요구: (G1) fanout에 네이티브 agy 시트를 추가해 Gemini+외부 혼합 스웜(경로 2), (G2) 통합 모델 픽커로 네이티브+외부 전부 노출,
(G3) 좌석 배정은 번호 선택(플래그 작성 금지, 플래그는 파워유저 폴백만), (G4) agy 터미널 안 /setting_agypro 에서
agy-pro 전체 설정을 네이티브 Antigravity 스타일로 관리.
갈림 시 메인 에이전트가 증거 기반 판정(§4.4). 로컬 커밋만, GitHub push 금지. k3 패밀리는 서브에이전트 풀 제외, Charm 사용 가능.
절차: 계획서 §7 재개 지침 준수(기존 산출물 재구축 금지, report 폴더 미커밋, decisions.md append-only).
```

## 5. Work breakdown (fresh-session dispatch units, SRP)
- U1 (fanout core): native seat worker + seat spec parser + concurrency cap + provenance metadata + tests (internal/fanout/**, maybe cmd/agy wiring). Stub-binary tests for subprocess seat; live test gated on user quota.
- U2 (inventory/picker): combined model inventory (`agy pro models --all`), launcher picker sub-menu, caching of `agy models` output (cmd/agy/**, internal/launcher/**).
- U3 (swarm UX): `agy pro swarm` builder + templates (new internal/swarm/ or cmd layer; reuse fanout RunConfig).
- U4 (docs + polish): README/manual/bridge-architecture + decisions.md + B2 documentation; installer unchanged unless wizard handoff needs a swarm teaser.
- U5 (verification, per charter): code-reviewer + security-reviewer + blind-qa gates with the session's then-available model pool; live matrix incl. one real mixed swarm (user quota permitting).
Suggested order: U1 -> U2 -> U3 in parallel where files disjoint (U1 fanout/, U2 launcher+cmd, U3 new package + cmd) after a shared contract review; U4 after.

## 6. Risks & open questions (decide at kickoff)
R1 real agy subprocess startup cost per seat (193MB binary) - mitigate with concurrency cap + early spawn.
R2 Native quota exhaustion mid-swarm - seat fails gracefully (A2); surface a "quota" reason code.
R3 Print-mode output fidelity (does -p include tool traces? plan for output trimming markers) - verify with one live call at U1 kickoff.
R4 Shim-recursion latch vs subprocess spawn - explicit test that the spawned agy does not re-enter launcher menu/bridge unexpectedly.
R5 /model UX expectation gap: even after B1, INSIDE an interactive agy session the /model popup remains XOR-limited. Set user expectation in docs; the combined picker is at agy-pro layer.
Open Q1: should native seats default to read-only-ish tasks (code review) and leave exec-heavy work to provider seats with bridge tool loop? (recommend: template-driven).
Open Q2: cost display - show "free quota" vs "$ estimate" per seat? (nice-to-have).
Open Q3: model pool for the new session's subagents - re-check Charm/k3 credit status first (user excluded both in the previous session for credits).

## 7. Resume instructions for the new session
1. Read this file first. Then skim docs/261012_0001_session_bridge-tool-loop/plan.md (R1 contract) and qa-live-report.md (behavioral ground truth). Do NOT redo section 2 items.
2. Check model credit status with the user (k3/Charm were excluded). Confirm no-GitHub-push policy still stands.
3. Kick off per section 5. Charter: orchestrator-first delegation, report folders docs/YYMMDD_NNNN_session_<slug>/ (never commit them), decisions.md append-only at repo root.
4. Live tests pin `agy --print-timeout 300s`; never kill user's running agy sessions; all verification headless.
