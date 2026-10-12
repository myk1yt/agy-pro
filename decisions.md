# decisions.md (append-only)

## 2026-10-12 — Bridge-side tool execution loop (agy-pro)
- Decision: implement the tool loop inside agy-pro's local bridge (aggregate-then-synthesize SSE; final text only to agy). Why: agy.exe gateway mode never sends `tools` and discards `tool_calls` (live-proven, qa-tool-parity-report.md §4); binary-level XOR means the only Gemini-parity path for external providers is bridge-side execution. Alternatives rejected: binary patching (unsupported), fanout-only tools (different UX, not in agy session), agy.exe upstream change (not ours).
- Decision: default tool surface `read,search`; `write`/`exec` opt-in via `AGY_PRO_BRIDGE_TOOLS` env > `agy pro config bridge-tools` > default; `none` disables the loop. Why: prompt-injection escalation risk vs Gemini parity; user asked for "same actions", so opt-in keeps parity reachable without shipping exec by default.
- Decision: budgets 8 iterations / 240s wall / 60s turn / 16 tool calls per turn / 30s per-tool deadline; every exit emits non-empty text + stop + [DONE]. Why: agy retries textless streams (634 req/40s observed).
- Impact scope: internal/bridge, internal/agenttools, internal/launcher, cmd/agy, mode state key `bridge_tools`, docs.

## 2026-10-12 — User decisions (CEO)
- P1 distribution: no signed/prebuilt exe release. One copy-paste install line (git or zip branch) documented in READMEs. P3 (winget/scoop manifest) therefore dropped — it requires prebuilt release artifacts, contradicting P1. P2 uninstaller implemented as a matching one-liner + uninstall.cmd (recycle-bin delete only).
- Secrets: provider keys stay on this machine only; never push key material to GitHub. Enforced: no secrets in commits (pre-commit scan), DPAPI-encrypted backups, redacted logs.
- No GitHub push this session: commits and the merge to main are local-only (user instruction).
- Model pool this session: k3 family excluded (no credits); Charm family excluded (no credits). Subagent pool used: MiMo, NW/glm-5.3-flash-flex, NW/deepseek-v4.1-flash-flex. The reviewer swarm's fixed Charm seat was dropped per the pool-changed rule.
- Session pause: work stops after this delivery; resume on user's word.

## 2026-10-12 — Known open items (held for user)
- NW_API_KEY real value is lost from HKCU (only a dead mock remains). User re-entry via `agy pro setup` required. Charm provider reports billing_error (out of credits) — live-provider acceptance of the tool loop is verified on mock upstream only; re-run one live turn when a working key exists (`agy --print-timeout 300s`).
- npm test has 1 pre-existing environment-dependent failure (formatOsc8Link, src/ untouched).
- User must manually confirm launcher raw-key input on a real console (headless rules blocked that check).

## 2026-10-12 — Next initiative decided (user)
- Choice: Path 2 (hybrid native seats in fanout) + combined model picker + swarm seat UX. Plan written: agy-pro/docs/plan-hybrid-swarm-model-ux.md (uncommitted at time of writing; new session commits it).
- Goal restated by user: native Antigravity Gemini + external providers in the SAME swarm (code-review / security 3-4 seats, kimi-code style), and /model-level combined visibility.
- Constraint restated: single-popover native+external is binary-XOR impossible; solution = agy-pro layer picker (B1) + AI Studio in-list Gemini (B2); binary patch (B3) rejected.
- Session now PAUSED; resume with a fresh session reading the plan file.
- Reconciliation policy (user, same day): when swarm seats disagree, the MAIN AGENT rules (evidence-based arbitration recorded in reconciliation.md); escalate to user only when evidence is missing or the call is a product judgment. Seat counts 2/3/4+ all supported; 2-seat ties fall under this arbitration. Plan updated (plan-hybrid-swarm-model-ux.md §4.4).
- G4 UX (user, same day): seat assignment by NUMBERS only in the happy path (flags = power-user fallback); add `/setting_agypro` inside the agy terminal for ALL agy-pro settings, styled like native Antigravity TUI (verify plugin/slash mechanism first; honest degrade if unsupported). Plan §4.5-§4.8 updated incl. kickoff prompt.
- Model pool (user, same day): Charm credits restored - usable. k3 family remains excluded. All others OK.

## 2026-10-12 — User additions mid-initiative (CEO)
- Startup banner: when `agy` runs in a terminal and the agy-pro launcher starts, show an English community banner: "Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI". Placement decided by VP: interactive launcher startup banner only (never print mode, never non-TTY/machine output). Scope: initiative U2 launcher unit.
- API key masking: all API-key input echo and display shows only the first 5 characters then `****` (e.g. `sk-ab****`). Applies to wizard key input, confirmations, providers list/display, settings display, logs. Security requirement; scope: initiative U6 settings/providers unit.
- Swarm seat family mix: glm-5.3-flash and glm-5.3-flash-flex are one family; one review-swarm seat must instead use deepseek v4.1 or mimo v2.6 flash. Orchestrator pool policy only (no code impact). Rotation going forward: one glm alias + one of {Charm/deepseek-v4.1-flash, MiMo/mimo-v2.6-flash} (alternating), plus primary and the fixed Charm/qwen3.8-flash seat.

## 2026-10-12 — Arbiter rulings on re-audit round 2 split (main agent, §4.4)
- Split verdict 2 REJECT / 2 APPROVE. Ruling 1 (agent-48 P1 + agent-49 P2 vs agent-50): UPHELD. reconcile.go:506 ("As Vice President of Engineering, review all disputed or blocker findings...") IS a real 5th VP-persona emission site (direct grep: :483 "VP Arbiter review recommended", :505 "## 3. VP Arbiter Decision Log", :506 the persona instruction, + cli.go:154). Revision 1 row 11(a) ":506 off-by-one" refutation is WRONG. Contract must enumerate ALL emission sites and prescribe main-agent wording (D-3). Dissent recorded: agent-50 maintained the off-by-one view; refuted by direct grep.
- Ruling 2 (agent-47 P1): UPHELD. architect-report.md:302 "interactive when TTY & no --seats" contradicts :535 "any of --template/--seats/--root/--prompt(-file)/--timeout/--dry-run present => zero prompts (C2)". Contract aligns to: interactive builder only when TTY AND no non-interactive flags present; any flag present => zero prompts (exit 2 when required input missing). Test added.
- Disposition: both rulings + the k3 failover/selector exclusion seam (round-2 P2, 4/4 instances) + the banner/masking user additions entered contract Revision 2 (v3, frozen). Phase 2 plan gate closed by arbitration; Phase 4 double-blind verification re-checks plan conformance on the real implementation.

## 2026-10-12 — Model budget ruling (user, mid-initiative)
- Charm/qwen3.8-max is BANNED from the subagent pool going forward ("qwen3.8-max는 지금부터 사용하지 말아줘") - extreme cost; the user manually stopped the U1 implementation agent running on it. This supersedes the same-day "planning only" guidance. No dispatch on that alias without explicit user opt-in.
- U1 (fanout native seats) is INCOMPLETE: its qwen3.8-max instance was cancelled mid-unit (agent-51) and must NOT be restarted. Partial work landed (internal/realagy package, internal/providers/k3.go) and U2's full `go build/vet/test` ran green on top of it. U1 completion goes to a fresh cheaper-model instance when the user resumes the initiative.
- Pool policy now: excluded = k3 family (user), Google/gemini-3.8-flash (standing directive), Charm/qwen3.8-max (user, this entry). Reviewer/audit swarms: primary + fixed Charm/qwen3.8-flash + one glm alias + one of {Charm/deepseek-v4.1-flash, MiMo/mimo-v2.6-flash} (alternating). U2 landed this session on `primary` (MiMo/mimo-v2.6-pro) - cost profile acceptable.

## 2026-10-12 — Initiative delivery record (hybrid swarm model UX, G1-G4)
- Implementation landed locally (no push): U1 native fanout seats (a9e7660), U2 inventory/picker/banner (e2ec72d), U3 numbered swarm builder (45550df), U6 settings TUI + /setting_agypro plugin + API-key masking, U-wiring main.go dispatch, U4 docs alignment (swarm-hybrid.md + 6 updated docs). Contract source of truth: docs/261012_0002_session_hybrid-swarm-model-ux/architect-report.md v3.
- Arbitration implementation: O1 only (session main agent rules, `arbitration:` lines in reconciliation.md). O2 (`--arbiter` auto-rule pass) DEFERRED - the name stays banned from docs because a fake `--arbiter` never shipped.
- U7 delivered: `launcher.CommunityBanner` ("Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI"), interactive launcher startup only; never in -p/piped/machine output. U8 delivered: `providers.MaskSecret` = first 5 runes + `****` (breaking rewrite of the old prefix6/suffix4 display), applied to wizard input, confirmations, provider lists, settings panels, errors, logs.
- Out of scope (noted, unchanged): `rules-conflicts.md` and `profiles/**` still describe the full-stack profile's governance with "VP Arbiter" wording; contract ruled these out of scope (they describe profile governance, not fanout output).
- Open item (user quota gated): one LIVE mixed swarm (A1) run is part of U5 verification; in-session `/model` XOR remains a binary constraint (combined visibility lives at the agy-pro layer).
