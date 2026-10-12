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
