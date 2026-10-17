# Golden fixture manifest — `internal/geminiwire` (U4, session 261014_0007)

Byte-pin of the Gemini-wire contract per `docs/261014_0006_session_native-coexistence-design/wire-translation-spec.md` §11.1 (gate G1). The golden table test is `golden_test.go` (`TestGoldenFixtures`); the property tests are `property_test.go` (PT-1..PT-8).

## File conventions

- One pair per spec §11.1 row: `gf-NN-request.*` (inbound) + `gf-NN-expected.*` (pinned codec output). The numbered rows are GF-01..GF-32; GF-13b is the extra row the spec inserts after GF-13. Round-2b added GF-33 (refusal rows), GF-34 (exact-id pairing discriminator) and GF-35 (name-FIFO discriminator), so 36 pairs are pinned.
- Extensions: `.json` for JSON payloads, `.sse` for SSE byte streams (GF-22/23/27/28/29), `.json` for the error rows (provider status+body in, Google error envelope out).
- Every pinned expected file ends with exactly one `\n`; the comparison is byte-for-byte (`checkGolden`).
- Regenerate with `go test ./internal/geminiwire/ -run TestGoldenFixtures -update` (or `GOLDEN_UPDATE=1`). The committed bytes are the contract; `-update` exists only for a deliberate wire change.
- `gf-NN-request.sse` files carry the provider's own `data: [DONE]` sentinel: that is the OpenAI wire the adapter consumes. The ban on `[DONE]` applies to the pinned expected files only (§8 rule 3, toward agy).

## Entry point per fixture kind

| Kind | Rows | Codec entry point |
|---|---|---|
| `kindRequest` | GF-01..GF-21, GF-34, GF-35 | `DecodeGenerateRequest` → `ToOpenAIMessages` / `ToOpenAITools` / `ToOpenAIParams`; the expected file pins the assembled OpenAI body fragment (`messages`, `tools`, `tool_choice`, `params`, `surface_thoughts`, `notes`) |
| `kindStream` | GF-22, GF-23 | `openaiup.Turn.MergeSSE` → `TurnToGenerateResponse` → `StreamFrames` |
| `kindStreamApology` | GF-27 | same; the §9.2 apology is synthesized by the codec itself (`TurnToGenerateResponseWithCause`), with the row's `cause` string standing in for the adapter's "provider <name>: <redacted cause>" |
| `kindStreamError` | GF-28 | same, then the synthesized final chunk is dropped and the §9.3 error frame appended |
| `kindTitle` | GF-29 | §5.8 local title extraction → `TurnToGenerateResponse` → `StreamFrames` |
| `kindResponse` | GF-24, GF-25, GF-33 | provider non-stream body → `Turn.MergeChunk` → `TurnToGenerateResponseWithCause` |
| `kindEnvelope` | GF-31 | `ParseInternalRoute` + `UnwrapV1Internal` (schema rename) + `WrapV1InternalResponse` |
| `kindModels` | GF-30 | `ListModelsResponse` / `ModelInfo` shape (§5.7) |
| `kindError` / `kindRouteError` | GF-26, GF-32 | the §9.1 envelope bytes, validated against the §9.1 table encoded in `golden_test.go` |

Rows whose inbound carries several cases (GF-19, GF-20, GF-24, GF-25) store `{"cases":[...]}` and pin an array of expected fragments.

## Provenance

`CAPTURED` = shape taken from a design-session probe log (read-only; the real prompt text and the masked key value are never used). `SPEC` = constructed from the spec's documented shapes. `MIXED` = captured shape, stand-in content.

| ID | Inbound | Expected | Mapping row | Provenance |
|---|---|---|---|---|
| GF-01 | systemInstruction, 3 text parts | system message, `\n`-merged text | §7.1 | MIXED — the `systemInstruction.parts[]` shape is CAPTURED from `%TEMP%\agy-auditgap\logs\mock-q1c-gemini-baseline.log` (BODY #2); the `role` field (the capture carries none), the 3-part split and the stand-in text are SPEC (§5.5 item 5: truncated, shape-faithful stand-in, never the real prompt) |
| GF-02 | user turn, single text part | user message, string content | §7.1 | SPEC |
| GF-03 | user turn, 3 text parts (one empty) | merged content, empty part skipped | §7.1 | SPEC |
| GF-04 | model text turn in history | assistant message | §7.1 | SPEC |
| GF-05 | two consecutive user contents | one merged user message | §7.1 | SPEC |
| GF-06 | empty-text parts + all-empty content + call/response round | parts skipped, call still ordinal 0 and paired | §7.1 | SPEC |
| GF-07 | inlineData image/png, vision-capable model | `image_url` data-URI part | §7.2 | SPEC |
| GF-08 | the same image bytes, non-vision model | dropped + one audit note | §7.2 | SPEC — identical inbound to GF-07; only the row's vision flag differs; the note rides the returned `notes` channel, never the system message (post-A3) |
| GF-09 | inlineData audio/mp3, vision model | dropped + audit note (no OpenAI slot) | §7.2 | SPEC |
| GF-10 | single functionCall part | assistant + one tool_call, args object → compact string | §7.3 | SPEC |
| GF-11 | two functionCall parts (parallel) | two tool_calls, part order preserved | §7.3 | SPEC |
| GF-12 | functionResponse with id echoing the call id (W8 shape) | tool msg `tool_call_id` = that id | §7.3 | CAPTURED — `%TEMP%\agy-auditgap\logs\mock-q1-gem-gsub-inherit.log`: `functionCall.id` = `functionResponse.id` = `gsub-1`, `response.output` a string; args/output text are stand-ins |
| GF-13 | functionResponse with id, call side without ids | `tool_call_id` = synthesized call id (response.id unused) | §7.3 | SPEC |
| GF-14 | two same-name calls + two same-name responses, no ids | FIFO name pairing | §7.3 | SPEC |
| GF-15 | functionResponse with no open call (orphan), with and without an id | tool msg + audit orphan note | §7.3 | SPEC |
| GF-13b | functionResponse payload 1 MiB + 2 bytes of `한` | truncated at the rune boundary + `…[truncated 3 bytes]` | §7.3 | SPEC — the payload is sized so the 1 MiB cut lands mid-rune (1048576 is the 2nd byte of a `한`), pinning the back-off to 1048575 |
| GF-16 | tools[] with `parametersJsonSchema` | byte-identical passthrough | §7.4 | CAPTURED — declaration shape from `%TEMP%\agy-auditgap\payloads\.._tools_002.json` (3 of the 13 native declarations; `invoke_subagent`'s nested `Subagents` items schema and `Model` enum); descriptions are stand-ins |
| GF-17 | tools[] legacy `parameters` + toolConfig ANY + one allowed name | tools (Gemini-only keys stripped) + `tool_choice` object | §7.4 | SPEC |
| GF-18 | temperature/topP/maxOutputTokens/6 stop sequences | forwarded; stop truncated to 4 | §7.5 | SPEC |
| GF-19 | maxOutputTokens 65536 vs registry cap 8192 (and 4096 below the cap) | clamped 8192 / unchanged 4096 | §7.5 | SPEC — 65536 is the captured agy value [P#L147]; 8192 is the spec's registry example |
| GF-20 | thinkingBudget ladder −1 / 0 / 512 / 8192 / 65536 | omit / `none` / `low` / `medium` / `high` | §7.6 | SPEC — `{includeThoughts:true, thinkingBudget:-1}` is the captured generationConfig [P#L147] |
| GF-21 | non-thinking model + thinkingConfig present | no `reasoning_effort`, `surface_thoughts:false` | §7.6 | SPEC |
| GF-22 | full provider stream: role chunk, thought deltas, Korean+emoji text deltas, tool-call fragments, finish, usage | exact Gemini SSE, no `[DONE]` | §8 | SPEC — composed to §8.4-§8.6; usage carries `completion_tokens_details`/`prompt_tokens_details` |
| GF-23 | tool args in 5 fragments around multi-byte runes | one complete functionCall part, valid args object | §8.7 | SPEC — see "GF-23 mid-rune note" below |
| GF-24 | usage with reasoning/cached tokens, and a no-usage body | thoughtsTokenCount/cachedContentTokenCount; usage omitted when absent | §7.8 | SPEC |
| GF-25 | finish_reason length / content_filter / absent | MAX_TOKENS / SAFETY / STOP | §7.9 | SPEC |
| GF-26 | error map rows: 401, 403, 404, 429, 500, timeout, transport, malformed, 400+response_format | 9 Google error envelopes | §9.1 | SPEC — the §9.1 table transcribed; provider name is the `mock-provider` stand-in; upstream bodies are `<...>` stand-ins |
| GF-27 | provider 200 with zero content/tool_calls | apology text part + STOP | §9.2 | SPEC |
| GF-28 | mid-stream provider disconnect after 2 deltas | mapped chunk + error event, no fake success | §9.3 | SPEC |
| GF-29 | title-generator request (no tools) | local title stream, zero provider calls | §5.8 | MIXED — the route/model (`gemini-3.1-flash-lite-preview`), the absence of `tools` and the presence of a `systemInstruction` and a `generationConfig.thinkingConfig` are CAPTURED from `%TEMP%\agy-auditgap\logs\mock-q1c-gemini-baseline.log` #1; the capture carries no role on `systemInstruction` and `{"includeThoughts":true,"thinkingBudget":-1}`, so the fixture's `includeThoughts:false, thinkingBudget:0` and the transcript text are SPEC stand-ins |
| GF-30 | registry table (2 models) | `ListModelsResponse` with both generation methods, empty `nextPageToken` | §5.7 | SPEC |
| GF-31 | `/v1internal:streamGenerateContent` envelope | verb + unwrapped inner (responseSchema → responseJsonSchema) + re-wrapped response | §A | CAPTURED — envelope shape from `%TEMP%\agy-gate\logs\mock-r8.log` #17 (`project`, `model`, `userAgent`, `requestType`, `requestId` + `request{contents, systemInstruction, generationConfig, sessionId}`); payloads are stand-ins |
| GF-32 | unknown model on a generate route | 404 NOT_FOUND envelope naming the model | §3.3, §9.1 | SPEC — route shape per §5.5 items 1-2 (the colon-safe model segment) |
| GF-33 | provider refusal-only turn, and refusal + content | refusal text part (then the content part); no apology on top of a refusal | §7.1 response row, §9.2 | SPEC — added round-2b (FIX-21) |
| GF-34 | two same-name id-carrying calls answered out of order | exact-id rung wins: each response lands on the call whose id it echoes | §7.3 rung 1 | SPEC — added round-2b (FIX-22 discriminator; kills an exact-id rung deletion) |
| GF-35 | different-name calls answered in reverse order, no ids | per-name FIFO rung wins over the positional fallback | §7.3 rung 2 | SPEC — added round-2b (FIX-22 discriminator; kills a name-rung deletion and a positional hoist) |

## Property tests

| ID | Property | Where |
|---|---|---|
| PT-1 | Round-trip idempotence: `gemini→openai→echo→gemini→openai` fixpoint on messages/roles/merged text and tool ids/names/args; `provider→gemini→echo→provider→gemini` fixpoint on the aggregate (thought text, plain text, ordered calls, finish reason, usage) | `TestProperty_PT1_RoundTripIdempotence` |
| PT-2 | SSE chunk reassembly: the real `StreamFrames` output split at every offset (short streams, plus byte-at-a-time) and at 1000 seeded-random offsets (a >4 KiB synthesized stream) reassembles to the identical aggregate | `TestProperty_PT2_SSEChunkReassembly` |
| PT-3 | Rune safety: `openaiup.SplitPieces` over a CJK/emoji corpus at every piece size and every rune-boundary offset yields valid UTF-8 and rejoins to the original; the same holds on the emit path (a 5500-byte apology splits into ≤2048-byte valid parts) | `TestProperty_PT3_RuneSafety` |
| PT-4 | Resend stability: the id-pairing scan on 4 history shapes × 3 appended turns yields identical ids for the history's calls | `TestProperty_PT4_ResendStability` |
| PT-5 | Error-map completeness: every GF-26 row is a valid Google error envelope (code, RPC status, ≤240 one-line redacted message) matching the §9.1 table, and the 9 spec classes are covered | `TestProperty_PT5_ErrorMapCompleteness` |
| PT-6 | No-textless-200: 11 degenerate bodies (incl. refusal-only and null-content refusal) + 5 hand-built responses + 300 seeded-fuzz answers (content, refusal and finish randomized) always yield ≥1 part on the codec path and a finishReason on the final chunk | `TestProperty_PT6_NoTextless200` |
| PT-7 | Thought-leak: over 5 interleaved delta matrices (both reasoning keys) no `thought:true` text ever appears in a plain text part, and the request direction never leaks thought text into any message | `TestProperty_PT7_ThoughtLeak` |
| PT-8 | Envelope symmetry: `wrap(unwrap(x)) == x` byte-semantically for the captured envelope shape and 3 response shapes; the stream-chunk wrap equals the non-stream wrap; both stream wrap modes nest exactly the `StreamChunks` output | `TestProperty_PT8_EnvelopeSymmetry` |

## Decisions and notes

1. **GF-23 mid-rune note.** The provider SSE wire is JSON, and Go's JSON decoder coerces an invalid UTF-8 byte inside a string literal to U+FFFD, so a fragment boundary that truly splits a rune would corrupt the reassembled args. The fixture therefore places the 5 fragment boundaries at rune boundaries of the fragment strings while the reassembled args object mixes 3-byte Korean and a 4-byte emoji; byte-level rune safety of the adapter's own splitter is pinned separately by PT-3 (every piece size and every rune-boundary offset) and by GF-13b (the 1 MiB cut backing off mid-rune).
2. **The §9.2 apology is the codec's (round-2b).** `TurnToGenerateResponse` synthesizes the apology itself when a turn produced zero text, zero thought text, zero tool calls and zero refusal, before the finishReason mapping, so the canonical path can no longer emit a textless response. `TurnToGenerateResponseWithCause` takes the adapter's "provider <name>: <redacted one-line cause>" suffix; an empty cause falls back to `DefaultApologyCause`. GF-27 drives that path directly (no harness pre-injection) and its expected bytes are unchanged from the pre-fix pin, which is the proof the codec reproduces the adapter's string. The raw `StreamChunks` / `EnsureTerminal` branch remains as defense in depth for hand-built responses and is pinned by PT-6's raw cases.
3. **GF-28 close.** §9.3 forbids a fake success terminator after a mid-stream failure, so the runner emits the mapped part chunks, drops the synthesized final chunk and appends the `data: {"error":{...}}` frame. The error-frame emitter itself belongs to `wireadp` (U5); the fixture pins the exact bytes.
4. **Error map ownership.** The codec has no error mapper (it is I/O-free by contract; §9.1 belongs to `wireadp`, U5). GF-26/GF-28/GF-32 pin the exact envelope bytes and `golden_test.go` + PT-5 validate them against the §9.1 table encoded as data, so U5 has a byte contract to hit.
5. **GF-13b truncation is intentionally not idempotent.** The marker records the original length, so a second pass re-truncates with a different count. PT-1 allows exactly this one difference (identical pre-marker payload, marker on both sides).
6. **No secrets, no real prompt text.** Every fixture uses `<stand-in ...>` text. The captures' `X-Goog-Api-Key` value is masked in the logs and is never used. `TestGoldenFixtures_NoSentinelAndNoSecrets` sweeps every file under `golden/` — inbound `request.*` files included — for key-shaped literals (`sk-`, `AIza`, `Bearer `, `-----BEGIN`, `x-goog-api-key`, `x-api-key`, `Authorization:`, `AKIA`, `ASIA`, `ghp_`), a 32-64 character lowercase hex run, invalid UTF-8, and the `[DONE]` sentinel in pinned files only (the inbound provider streams legitimately carry it). `TestGoldenFixtures_SweepCatchesPlantedSecrets` plants each shape in a temp dir and asserts the sweep reports it, so the guard cannot go vacuous.
7. **Provider-500 row.** The §9.1 table's primary rule (5xx → 502/UNAVAILABLE) is pinned; the "500 may pass through as 500/INTERNAL" alternative is not pinned by a fixture.
8. **Pairing scan is strictly part-order (round-2b).** A `functionResponse` pairs against the calls seen so far in scan order, including earlier parts of the same content, and cannot see a call that appears later; the tool message is flushed after the assistant message that carries the content's text and calls. GF-34/GF-35 plus `TestCallPairing_SameContentPartOrder` pin both directions.
9. **The multimodal drop note is sanitized and stays off the system message (round-2b, post-A3).** The mime type is request-controlled text that reaches the provider in the §7.2 data URI and in the drop note. `sanitizeMime` keeps only `[a-zA-Z0-9/+._-]`, caps the value at 64 characters and collapses anything else to `unknown`, so a crafted mime cannot forge extra lines in the note. The drop notes themselves are kept OFF the system message: the system message is the head of the cacheable prefix, so folding a request-dependent dropped-part list into it would rewrite messages[0] bytes as the request grows and break prefix caching (A3). The notes surface only through the returned audit channel (`ToOpenAIMessages`'s second return value, the `notes` field of the assembled body), and the system message stays byte-identical whatever the request dropped. `TestToOpenAIMessages_DropNoteSanitizesMime` pins the newline, colon and length cases; `TestToOpenAIMessages_DroppedPartsStayOffTheSystemPrefix` pins the byte-stable prefix and the notes surfacing on the audit channel.
10. **The pairing scan is hash-indexed (round-3b).** `callScan` indexes the open calls instead of rescanning them: a per-id FIFO for the exact-id rung, a per-name FIFO for the name rung, and a global FIFO with a forward-only cursor for the positional fallback. The indexes are lazy (an entry popped by one rung is discarded from the other queues when they reach it), so each rung is O(1) amortized and the ladder still resolves in scan order — the round-2b golden bytes (GF-34/GF-35) and PT-4 are unchanged, which is the proof the semantics did not move. A linear rescan made translation O(calls x responses): ~10¹¹ comparisons for a 32 MB body of minimal parts, minutes of CPU inside the synchronous decode/translate where no upstream watchdog fires. `TestToOpenAIMessages_PairingScanStaysLinear` pins a 100k-pair agy-shaped history (W8: id and name on both sides) under a 5 s bound — the indexed scan finishes in ~0.26 s, and reverting the read path to the rescan fails the pin at ~6.6 s.
11. **Emitted text is capped at 64 KB per chunk (round-3b).** §8.8 requires text the adapter must split above 64 KB to go through `openaiup.SplitPieces` at rune boundaries. `partChunks` therefore cuts any text part longer than `maxPartTextBytes` into ≤64 KB pieces, one chunk each, before framing, so a merged turn never becomes one unbounded data line. `TestStreamChunks_SplitsGiantTextAtRuneBoundaries` pins a 200 KB mixed-width turn (multiple frames, each reassemblable to the identical text, no U+FFFD, thought markers preserved) and PT-2 carries the same stream through its random-offset branch.
