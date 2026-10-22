---
name: causal-chain-analysis
description: >
  Analyze and document causal chains for bug fixing. Maps the full execution path from user trigger through UI, state management, services, FFI/API bridges, to backend and back. Graph-based, evidence-driven root-cause methodology.
---

# Causal Chain Analysis

A graph-based, evidence-driven framework for analyzing how a symptom is caused.
Models typed nodes (components, functions, state, boundaries) and typed edges
(calls, events, data flow, state transitions, errors) with explicit evidence and
confidence, instead of forcing execution into a fixed linear layer sequence.

## 1. Purpose

Answer these questions before, during, and after analysis. The analysis is
complete only when every question that applies to the target has an answer or an
explicitly recorded unknown.

1. **Trigger identification**: What event starts the execution path (user action,
   timer, message, callback, lifecycle event, external request)?
2. **Control-flow, data-flow, and state-transition**: What is the control flow
   (which code runs next), what data flows along each edge, and what state
   transitions occur at each node?
3. **Process/thread/service/storage boundaries**: Which boundaries does the path
   cross (process, thread, service, FFI, IPC, queue, database, cache, external
   API), and what happens at each crossing?
4. **Normal vs failure path divergence**: Where does the observed behavior first
   diverge from expected behavior, and what invariant was violated there?
5. **First invalid state creation**: Where is the first invalid state created
   (the earliest point where a value, state, or condition becomes wrong)?
6. **Propagation to symptom**: How does the invalid state travel from its creation
   point to the observable symptom (which edges, transformations, and boundaries)?
7. **Trigger/root-cause/contributing-factor/symptom identification**: Which
   elements are the trigger, the root cause, contributing factors, and the
   symptom? Do not conflate them.
8. **Evidence for each node/edge**: What evidence supports each node and edge,
   and what is its class (observed, tested, inferred, hypothesized) and
   confidence?
9. **Observed vs inferred vs hypothesized relationships**: For every relationship
   in the map, state whether it was observed at runtime, verified statically,
   inferred from code reading, or merely hypothesized.
10. **Affected contracts/consumers**: Which public contracts, APIs, schemas, and
    consumers are affected by the path and by any planned change?
11. **Verification tests/diagnostics**: Which tests, logs, traces, or diagnostics
    can verify each hypothesis and confirm the fix?

## 2. Core Principles

1. **Call Chain Is Not Causality**: A call relationship only says "A invoked B".
   Causality requires that B's behavior depends on A's action. Distinguish:
   - **Control flow**: which code runs next (call, return, branch).
   - **Call relationship**: A invokes B (may be unrelated to the symptom).
   - **Data flow**: a value produced by A is consumed by B.
   - **State transition**: a state change caused by A is observed by B.
   - **Event propagation**: an event emitted by A is handled by B (possibly async).
   - **Dependency**: B requires A to exist/compile/run (not causal at runtime).
   - **Error propagation**: an error raised by A is caught/handled by B.
   - **Causal relationship**: removing A's action removes B's behavior.
   Only the last one is causality. Record the type of each edge explicitly.
2. **Evidence Before Certainty**: Do not record unverified edges as fact. Every
   node and edge carries an evidence class and confidence. An edge found only by
   static search is `STATICALLY_VERIFIED` at best, never `CONFIRMED` at runtime.
3. **Graph, Not Forced Linear Sequence**: Execution paths branch, merge, loop,
   and run in parallel. Model them as a directed graph with typed nodes and
   edges. Do not force a fixed UI→State→Service→Bridge→Backend→Return sequence;
   real paths may skip layers, loop, fan out, or run callbacks asynchronously.
4. **Revision-Bound Analysis**: Every claim is bound to a specific repo revision,
   artifact, and environment. A chain verified on one revision is not valid on
   another without re-verification.
5. **Read-Only by Default**: Do not modify source, config, or docs unless the
   user explicitly requests artifact generation or diagnostics. Analysis itself
   is read-only.
6. **Scope Before Exhaustiveness**: Trace only the paths needed for the target
   symptom, feature, transaction, or high-risk journey. Do not map the whole
   system to answer one question.

## 3. When to Use

Use this skill when the task involves any of:

- **Reproducible bug root-cause analysis**: a bug reproduces and the cause is
  not obvious from a single location.
- **Multi-module/service problems**: the symptom spans modules, packages, or
  services.
- **UI-backend mismatches**: UI behavior disagrees with backend behavior or
  contract.
- **FFI/IPC/API/queue boundary issues**: problems at language, process, or
  message boundaries (e.g., Flutter↔Rust, HTTP, gRPC, message queues).
- **State management problems**: wrong, missing, or stale state; rebuilds that
  do not reflect state; state updated in the wrong place.
- **Callback/stream/event problems**: callbacks not firing, streams not
  emitting, events lost or duplicated.
- **Race conditions/stale state**: ordering-dependent behavior, reads of stale
  data, concurrent writers.
- **Cache inconsistency**: cache and source of truth disagree.
- **Serialization/encoding problems**: garbled text, wrong types, encoding
  mismatches across boundaries.
- **Retry/timeout/cancellation problems**: duplicate operations, hangs, partial
  work after cancellation, retry storms.
- **Database transaction problems**: partial commits, lost updates, deadlocks,
  isolation violations.
- **Refactor impact analysis**: a planned change may break callers or contracts.
- **High-risk feature execution map**: mapping a critical journey before
  changes or before writing tests.
- **Security-sensitive data flow**: tracing where sensitive data moves, is
  stored, and leaves the system.
- **Intermittent failure analysis**: failures that do not reproduce reliably.
- **Pre-review blast-radius analysis**: estimating the impact of a change
  before review or merge.

## 4. When Not to Use

Do not use this skill for:

- **Simple syntax errors**: a compile error with an obvious location and cause.
- **Isolated typos with obvious location/cause**: a misspelled identifier or
  constant where the fix is unambiguous.
- **Documentation changes** unrelated to execution paths.
- **General dependency audits**: dependency version/conflict analysis without a
  runtime symptom (use a dependency audit skill instead).
- **Architecture overview only**: describing the system without a target
  symptom, feature, or change.
- **Pure hypothesis discussion** without source or evidence: brainstorming
  causes without reading code or runtime data.

**Exception**: apply this skill even to seemingly simple problems when the issue
crosses a boundary (UI↔state, process, service, FFI, queue) or the cause is
unclear after reading the obvious location.

## 5. Analysis Modes

Choose exactly one mode per analysis and record it in the intake.

| Mode | Purpose | Typical output |
|---|---|---|
| `INCIDENT` | Trace the failure path of a specific symptom to its root cause. | Expected path, observed failure path, divergence, causal conclusion. |
| `FEATURE_MAP` | Map normal execution of a feature plus its failure branches. | Full graph with success and error branches, invariants, test coverage. |
| `IMPACT` | Map downstream/upstream effects of a planned change. | Impact map, affected contracts, risk-ranked consumers. |
| `REFACTOR_VALIDATION` | Compare chains before and after a refactor to prove behavior preserved. | Pre/post chain comparison, changed edges, regression evidence. |
| `INTERMITTENT` | Analyze race/timing/ordering/cache/retry-dependent failures. | Ordering-sensitive graph, candidate interleavings, discriminating tests. |
| `SECURITY_DATA_FLOW` | Trace sensitive data across trust boundaries, auth, and external sinks. | Data flow graph with trust boundaries, sinks, and risk marks. |

## 6. Analysis Intake

Record the intake before tracing. Fill every field that applies; leave unknown
fields explicitly marked `UNKNOWN`, never blank.

```yaml
analysis_id: "CCA-2026-08-06-001"
mode: INCIDENT            # INCIDENT | FEATURE_MAP | IMPACT | REFACTOR_VALIDATION | INTERMITTENT | SECURITY_DATA_FLOW
repo: "myownsettings"
branch: "main"
base_revision: "abc1234"          # revision the analysis starts from
current_revision: "def5678"       # revision actually analyzed
artifact: "debug build 1.2.3"      # binary/artifact under analysis, if any
environment: "Windows 11, dev VM, sqlite local"   # OS, runtime, infra
target_feature: "TTS playback"
symptom: "TTS button click produces no sound"
expected_behavior: "Clicking the button plays audio within 500 ms"
actual_behavior: "No audio; no error shown; button stays in idle state"
reproduction: REPRODUCED          # see states below
frequency: "every time"           # every time | intermittent (x%) | once | unknown
affected_users_or_components: "all users; reader page, tts service"
first_known_good: "v1.2.0 (commit 9a8b7c6)"
first_known_bad: "v1.3.0 (commit def5678)"
relevant_logs_or_traces: "app.log lines 120-145; tts trace id t-42"
constraints: "no production instrumentation allowed; no secret logging"
out_of_scope: "audio device driver issues; other features"
```

**Reproduction states** (exactly one):

- `REPRODUCED`: symptom observed with a recorded reproduction procedure.
- `INTERMITTENT`: symptom observed but not reliably; conditions partially known.
- `NOT_REPRODUCED`: symptom reported but not observed in this environment.
- `ENVIRONMENT_BLOCKED`: cannot reproduce in the available environment
  (missing hardware, license, data, or service).
- `NOT_APPLICABLE`: no reproduction needed (e.g., `IMPACT` or `FEATURE_MAP` mode).

## 7. Scope Definition

Define the scope before tracing. Scope is expressed in **scope units** and
**included elements**, with explicit **stop conditions**.

**Scope units** (choose the unit that matches the target):

- user journey (e.g., "sign up", "checkout")
- command (e.g., "save settings")
- request (e.g., "GET /api/books/42")
- event (e.g., "bookmark added")
- job (e.g., "nightly sync")
- transaction (e.g., "transfer funds")
- state transition (e.g., "player: idle → playing")
- background operation (e.g., "auto-save timer")
- data object (e.g., "Book model lifecycle")
- failure instance (e.g., "the crash on 2026-08-05 14:02")

**Included elements** (record which of these are in scope):

- source code, configuration, environment, feature flags, dependencies,
  schemas, storage, cache, queue, external services, generated code,
  deployment topology.

**Stop conditions** (the trace ends when any of these is reached):

- final state displayed to the user (UI settled)
- persistent state confirmed (write committed and acknowledged)
- event acknowledged (ACK sent, callback completed)
- error returned to the caller (exception/rejection propagated to a handler)
- retry budget exhausted (no more retries scheduled)
- trust boundary crossed (data left the system to an external sink)

## 8. Causal Concepts

Use these definitions consistently. Do not confuse trigger or symptom with root
cause.

- **Trigger**: the event that starts execution (a click, a message, a timer).
  Removing the trigger stops the run but does not fix the underlying defect.
- **Precondition**: pre-existing state required for the path to execute (a
  logged-in user, an initialized service, a non-empty list).
- **Necessary Cause**: a condition without which the symptom will not occur.
- **Sufficient Cause**: a condition that, together with other conditions, is
  enough to produce the symptom.
- **Root Cause**: the first invalid state or invariant violation in the chain,
  which is correctable. It is the earliest point where a value, state, or
  condition becomes wrong and from which the symptom follows.
- **Contributing Factor**: increases likelihood or impact but is not itself a
  cause (slow network, low memory, default config).
- **Propagation Mechanism**: how the bad state crossed layers or boundaries
  (returned value, shared state, event, message, exception, cache).
- **Amplifier**: expands the impact or scope of the failure (retry loop,
  fan-out to many consumers, cache poisoning affecting other users).
- **Detection Gap**: why validation, tests, or monitoring missed the defect
  (missing assertion, untested branch, no alert on the failure mode).
- **Symptom**: the observed final result (what the user or system sees).

**Rule**: The root cause is the first invalid state, not the trigger and not the
symptom. If the "root cause" you found is the same as the symptom, keep tracing
backward.

## 9. Evidence Model

Every node, edge, branch, and conclusion carries evidence and confidence.

**Evidence classes** (exactly one per evidence item):

| Class | Meaning |
|---|---|
| `RUNTIME_OBSERVED` | Directly observed during a live run (debugger breakpoint, live inspection). |
| `TEST_OBSERVED` | Observed by executing a test that asserts the behavior. |
| `DEBUGGER_OBSERVED` | Observed via debugger (breakpoints, watch, step-through). |
| `TRACE_OBSERVED` | Observed in a distributed/structured trace. |
| `LOG_OBSERVED` | Observed in application logs. |
| `STATICALLY_VERIFIED` | Verified by reading code/static analysis; not observed at runtime. |
| `CONFIG_VERIFIED` | Verified from configuration, feature flags, or environment. |
| `DOCUMENTED` | Taken from documentation, ADRs, or comments; not verified in code. |
| `INFERRED` | Reasoned from other evidence; not directly observed. |
| `HYPOTHESIZED` | Proposed explanation; not yet tested. |
| `UNKNOWN` | No evidence available. |

**Confidence levels** (exactly one per claim):

`CONFIRMED` (proven by direct observation or test), `HIGH` (strong consistent
evidence), `MEDIUM` (partial or indirect evidence), `LOW` (weak or single
source), `UNKNOWN`.

**Evidence YAML**:

```yaml
evidence:
  - id: "ev-01"
    type: LOG_OBSERVED
    source: "app.log:120-145"
    revision: "def5678"
    environment: "dev VM, Windows 11"
    timestamp: "2026-08-06T14:02:11Z"
    excerpt_or_reference: "TTS/play/001: engine returned error code 3"
    limitations: "log level INFO; may omit earlier failures"
```

**Rule**: Do not assume logs are complete or accurate just because they exist.
Logs can be truncated, buffered, reordered, or written by a different code path
than the one you are analyzing. Corroborate log claims with at least one other
evidence class when the claim is load-bearing.

## 10. Node Model

A node is a typed element in the graph. Nodes are identified by stable IDs, not
line numbers.

**Node YAML**:

```yaml
nodes:
  - id: "n-01"
    kind: ui_handler
    component: "ReaderPage"
    package_or_service: "reader"
    process: "app"
    thread_or_task: "main"
    file: "lib/features/reader/reader_page.dart"
    symbol: "ReaderPage._onTtsPressed"
    line_range: [210, 225]        # supplementary only; never the identity
    runtime: "Flutter 3.24"
    responsibility: "Handle TTS button press and dispatch to TtsService"
    input: "ButtonPressEvent"
    output: "TtsService.play(request)"
    state_before: "idle"
    state_after: "idle (unchanged until callback)"
    side_effects: "none"
    error_behavior: "shows SnackBar on service error"
    security_boundary: false
    evidence: ["ev-01"]
    confidence: HIGH
```

**Node kinds** (choose the closest; do not invent new kinds without recording
them in the project's schema notes):

`user_trigger`, `ui_handler`, `command`, `state_transition`, `domain_logic`,
`validation`, `adapter`, `API`, `FFI`, `IPC`, `queue`, `worker`, `cache`,
`database`, `external_service`, `callback`, `observer`, `serializer`,
`error_handler`, `recovery`, `final_effect`.

**Identity rule**: A node's stable ID is derived from its path, qualified
symbol, and node ID (e.g., `reader/ReaderPage._onTtsPressed`). Line numbers are
supplementary and change on every edit; never use a line number as the identity
of a node.

## 11. Edge Model

An edge is a typed relationship between two nodes. The edge type states what
kind of relationship it is, which is essential for separating call chains from
causality.

**Edge YAML**:

```yaml
edges:
  - id: "e-01"
    from: "n-01"
    to: "n-03"
    type: call
    direction: forward
    sync: true
    condition: "button enabled and service initialized"
    payload: "TtsPlayRequest(text, voiceId)"
    transformation: "none"
    serialization: "none (in-process)"
    ordering: "after n-01 state check"
    timeout: "none"
    retry: "none"
    cancellation: "none"
    idempotency: "n/a"
    failure_behavior: "exception propagates to n-01 error handler"
    evidence: ["ev-02"]
    confidence: HIGH
```

**Edge types**:

`call`, `return`, `event`, `message`, `stream`, `callback`, `data`, `state`,
`read`, `write`, `dependency`, `error`, `retry`, `fallback`, `cancellation`,
`notification`, `acknowledgement`.

**Direction**: `forward` (from cause to effect along execution), `reverse`
(return/acknowledgement path), `bidirectional` (shared state or stream).

**Rule**: Record the edge type honestly. A `call` edge is not evidence of
causality; a `data` or `state` edge with a demonstrated dependency is. When in
doubt, mark the edge `INFERRED` and lower the confidence.

## 12. Branches and Loops

Graphs contain branches and loops. Model each one explicitly; do not collapse
them into a linear chain.

**Express these structures**:

- conditional branches (if/else, switch, pattern match)
- success/error branches (result vs exception)
- retry loops (bounded and unbounded)
- fallbacks (primary → secondary path)
- fan-out (one node dispatches to many)
- fan-in (many nodes converge on one)
- multiple subscribers (event/stream with several handlers)
- async callbacks (deferred execution)
- parallel tasks (concurrent execution)
- timeout branches (slow path vs timeout path)
- cancellation branches (cancelled vs completed)
- compensation/rollback (undo paths after partial failure)

**Per branch, record**:

```yaml
branches:
  - id: "b-01"
    from_edge: "e-03"
    condition: "engine returns Ok(result)"
    expected_path: "n-05 → n-06"
    observed_path: "n-05 → n-07 (error handler)"
    branch_evidence: ["ev-04"]
    branch_coverage: PARTIALLY_COVERED   # see section 22
    failure_consequence: "user sees error snackbar; state stays idle"
```

**Rule**: For every branch, state the condition, the expected path, the
observed path (for incidents), the evidence, the coverage state, and what
happens to the user or system if that branch is taken.

## 13. Expected and Failure Paths

For incident analysis, build at minimum two paths:

- **Expected Path**: the nodes and edges that normal behavior follows.
- **Observed Failure Path**: the nodes and edges the failure actually followed.

**Divergence structure**:

```yaml
divergence:
  last_matching_node: "n-04"
  first_diverging_edge: "e-05"
  expected_state: "engine initialized, request accepted"
  observed_state: "engine not initialized, request rejected"
  violated_invariant: "engine must be initialized before play() is called"
  invalid_state_created_at: "n-02 (init skipped when voice list empty)"
  propagated_through: ["e-05", "n-05", "e-07"]
  final_symptom: "no audio; idle state; no error shown"
  evidence: ["ev-01", "ev-03"]
```

**Rule**: The first diverging edge is the earliest point where observed
behavior differs from expected. The invalid state may be created even earlier
than the divergence; trace backward from the divergence to find where the state
first became wrong.

## 14. Invariants and Divergence

Record invariants at key boundaries. An invariant is a condition that must hold
at a specific point in the path. The root cause should explain which invariant
was broken and where.

**Common invariants to check**:

- request ID is maintained through the entire return path
- state updates are not applied to disposed/unmounted components
- tenant ID is present in all storage queries
- payload encoding is identical on both sides of a boundary
- retry does not duplicate the same operation (idempotency)
- post-cancellation callbacks do not modify user-visible state
- cache entry version is not older than the persistent state it mirrors
- a resource is released exactly once (no leak, no double-free)
- a transaction commits or rolls back atomically

**Divergence analysis procedure**:

1. Identify the invariant that should hold at the divergence point.
2. Determine whether the invariant was violated, or never established.
3. Find where the invariant was first broken (the invalid state creation).
4. Verify the violation is necessary for the symptom (see section 16).

## 15. Hypothesis Analysis

Maintain competing hypotheses explicitly. Do not confirm the first plausible
explanation as the root cause.

**Hypothesis YAML**:

```yaml
hypotheses:
  - id: "h-01"
    statement: "TtsService.play() is called before the engine is initialized"
    predicted_observation: "log shows play() before init() completes; engine error code 3"
    supporting_evidence: ["ev-01"]
    contradicting_evidence: []
    discriminating_test: "add init-completion barrier; assert play() waits; run tts_test.dart"
    status: SUPPORTED
    confidence: MEDIUM
```

**Hypothesis states**:

- `OPEN`: proposed, not yet tested.
- `SUPPORTED`: evidence favors it, no contradiction.
- `WEAKENED`: contradicting evidence exists but is not conclusive.
- `REJECTED`: contradicted by evidence or a discriminating test.
- `CONFIRMED`: proven by a discriminating test or direct observation.

**Rule**: Keep at least two competing hypotheses alive until a discriminating
test or observation rejects all but one. A hypothesis is `CONFIRMED` only when
its predicted observation is actually observed and its competitors are rejected.

## 16. Causal Validation

Validate causal claims with these methods, in increasing strength:

- **Regression test**: a test that fails before the fix and passes after it.
- **Input variation**: change inputs while keeping the path fixed; symptom
  should track the causal input.
- **Feature flag variation**: toggle the suspected feature; symptom should
  appear/disappear accordingly.
- **Dependency substitution**: replace a suspected dependency with a stub or
  alternative; observe whether the symptom changes.
- **Controlled state injection**: inject the suspected invalid state directly;
  the symptom should appear without the original trigger.
- **Mock vs real integration**: compare behavior with a mock and with the real
  integration to isolate boundary effects.
- **Event ordering changes**: reorder events; if the symptom follows the order,
  ordering is causal.
- **Retry disable**: disable retries; if duplicates disappear, retry is causal.
- **Cache bypass**: bypass the cache; if the symptom disappears, the cache is
  causal.
- **Isolated component execution**: run the component alone; if the symptom
  disappears, the interaction is causal.
- **Trace correlation**: correlate symptom occurrence with trace spans.
- **Differential logging**: log the same event on both sides of a boundary and
  compare.
- **Known-good revision comparison**: compare behavior against a known-good
  revision to isolate the introducing change.
- **Counterfactual tests**: test what happens when a suspected cause is removed.

**Counterfactual questions** (ask for the leading hypothesis):

1. Does removing the cause eliminate the symptom?
2. Does the invalid state remain reachable through other paths?
3. Can the cause be isolated while keeping all other conditions constant?
4. Does the same cause occur at other entry points (and does it produce the
   same symptom there)?

**Rule**: A root cause is `CONFIRMED` only when a counterfactual test or
equivalent evidence demonstrates that removing it eliminates the symptom.

## 17. Async and Distributed Systems

When the path crosses async or distributed boundaries, explicitly check:

- process boundaries (separate runtimes, separate memory)
- task/thread boundaries (concurrency, shared state)
- queues/topics (message brokers, job queues)
- correlation/request/event IDs (are they propagated?)
- delivery semantics (at-most-once, at-least-once, exactly-once)
- ordering (is order preserved? per key? per partition?)
- duplicate delivery (can the same message arrive twice?)
- lost messages (no ACK, TTL expiry, dead-letter)
- retry (who retries, with what backoff, how many times)
- timeout (who times out, what happens on timeout)
- cancellation (who cancels, what happens to in-flight work)
- idempotency (can operations be safely repeated?)
- transactions (are they atomic across the boundary?)
- eventual consistency (how long until convergence?)
- stale reads (can a reader see old data?)
- cache invalidation (when and how is cache invalidated?)
- clock/timezone (timestamp comparisons across machines)
- partial failure (some nodes succeed, others fail)
- circuit breaker (does the system stop calling a failing dependency?)
- fallback (what happens when the primary path fails?)

**Rule**: Do not determine distributed causal order from function call sequence
alone. In distributed systems, call order in one process does not imply order
in another. Use correlation IDs, timestamps, and delivery semantics to
establish order.

## 18. Data Boundaries

At every boundary (FFI, API, IPC, queue, serialization, storage), check:

- source/target type (are the types compatible?)
- validation (is input validated on both sides?)
- normalization (are values normalized identically?)
- encoding (UTF-8 vs UTF-16, base64, percent-encoding)
- serialization (JSON, protobuf, msgpack; field names and versions)
- default values (do both sides agree on defaults?)
- nullable fields (can null cross the boundary?)
- enum mapping (do enum values map 1:1?)
- unit conversion (meters vs feet, bytes vs KB)
- timezone (UTC vs local; DST handling)
- precision (float vs double vs decimal; rounding)
- truncation (string length, integer overflow)
- escaping (HTML, SQL, shell, regex)
- auth context (identity carried across the boundary?)
- authorization context (permissions carried across the boundary?)

**Rule**: Boundary mismatches are separate causal candidates. If a symptom
appears only when data crosses a boundary, treat the boundary itself as a
suspect node and check every item above.

## 19. State Analysis

For state-related nodes, record:

- **owner**: which component owns the state
- **lifecycle**: when the state is created and destroyed
- **initialization**: what the initial value is and when it is set
- **mutation**: which nodes write the state, and under what conditions
- **observer**: which nodes read the state, and how they are notified
- **persistence**: whether and how the state is persisted
- **cache**: whether a cache mirrors the state
- **invalidation**: when and how cached/derived state is invalidated
- **disposal**: what happens to the state when the owner is disposed
- **synchronization**: how concurrent access is serialized
- **stale-state possibility**: can a reader observe an outdated value?
- **concurrent writers**: can two writers race?
- **rollback**: what happens if a mutation fails partway?

**Rule**: Do not only check UI rebuilds. Check the entire state lifecycle:
creation, change, consumption, and disposal. A symptom that appears "in the UI"
is often caused by a state that was never initialized, mutated in the wrong
order, or read after disposal.

## 20. Error and Recovery Paths

Do not trace only normal returns. For every node, check the error and recovery
paths:

- validation errors (rejected input)
- exceptions (thrown and caught)
- rejected promises/futures (async failures)
- panics (unrecoverable failures)
- timeouts (operation exceeded its budget)
- cancellations (operation aborted)
- retries (automatic re-execution)
- fallbacks (alternative path after failure)
- partial successes (some work done, some not)
- compensations (undo of partial work)
- rollbacks (transaction undo)
- user-visible errors (what the user sees)
- logging (what is recorded)
- metrics (what is measured)
- dead-letter queues (where failed messages go)

**Record where errors are transformed or swallowed**:

```yaml
error_handling:
  - node: "n-05"
    error_source: "engine error code 3"
    transformed_to: "null (swallowed)"
    swallowed_at: "n-05 catch block, line 88"
    user_visible: false
    logged: false
    consequence: "UI never learns the failure; state stays idle"
```

**Rule**: A swallowed error is a causal candidate. If the symptom is "nothing
happens", check whether an error was raised and then swallowed before it could
reach a handler.

## 21. Dependency and Impact Map

Center the impact map on symbols, modules, packages, and contracts, not files.

**Relationship types**:

- runtime caller/callee
- import (compile-time dependency)
- type contract (a type used across a boundary)
- data schema (a schema shared by producer and consumer)
- config (a config key consumed by a node)
- build (a build-time dependency)
- generated code (code produced from a schema or template)
- test (a test that exercises a node)
- external consumer (an outside caller of a public API)
- public API (an exported contract)
- shared state (a state object read/written by multiple nodes)

**Per impact, record**:

```yaml
impact_map:
  - target: "TtsService.play"
    relationship: "runtime caller/callee"
    direct_or_indirect: direct
    affected_contract: "TtsService.play(TtsPlayRequest) → Future<TtsResult>"
    change_risk: HIGH
    tests: ["test/features/tts/tts_test.dart"]
    confidence: HIGH
```

**Rule**: For a planned change, list every consumer of the changed contract,
mark direct vs indirect, and rank by change risk. Verify the most critical
consumers with tests (see section 22), not all files unconditionally.

## 22. Test Coverage Mapping

For each important node, edge, and branch, record what covers it:

- unit (single component in isolation)
- integration (multiple components together)
- contract (boundary contract verification)
- E2E (full user journey)
- regression (test added for a past bug)
- property-based (generated inputs)
- fuzz (malformed/random inputs)
- concurrency (race/parallel tests)
- manual evidence (observed by hand, not automated)

**Coverage states** (exactly one per item):

- `COVERED_SUCCESS_PATH`: a test exercises the success path of this item.
- `COVERED_FAILURE_PATH`: a test exercises the failure path of this item.
- `PARTIALLY_COVERED`: a test touches the item but not the specific branch.
- `INDIRECTLY_COVERED`: covered only through another component's test.
- `NOT_COVERED`: no test exercises this item.
- `UNKNOWN`: coverage not established.

```yaml
test_coverage:
  - item: "e-05 (play before init)"
    unit: NOT_COVERED
    integration: PARTIALLY_COVERED
    contract: NOT_COVERED
    e2e: NOT_COVERED
    regression: NOT_COVERED
    property_based: NOT_COVERED
    fuzz: NOT_COVERED
    concurrency: NOT_COVERED
    manual: "observed once in dev run"
    coverage_state: PARTIALLY_COVERED
```

**Rule**: Do not assume a test file's existence means the branch is actually
exercised. Verify by reading the test or running it with coverage. A test that
never executes the failing branch provides no evidence.

## 23. Diagnostics

Default analysis does not modify code. When evidence is insufficient, plan
diagnostics in this priority order:

1. **Existing tests**: run or extend existing tests that exercise the path.
2. **Structured logs**: read existing logs for the relevant correlation ID.
3. **Existing traces**: use existing tracing (spans, distributed traces).
4. **Debugger**: attach a debugger and step through the suspected path.
5. **Reproducible harness**: build a minimal reproduction outside the app.
6. **Temporary isolated instrumentation**: add temporary logging in a
   controlled, isolated way, with a cleanup plan.
7. **Production instrumentation**: only with explicit user approval and
   authorization.

**Diagnostic plan YAML**:

```yaml
diagnostic_plan:
  - hypothesis: "h-01"
    observation_needed: "order of init() vs play() calls"
    location: "TtsService.play entry, line 88"
    method: "temporary log at play() entry and init() completion"
    data_collected: "timestamps and call order"
    sensitive_data_risk: none
    performance_risk: "negligible (2 log lines per call)"
    cleanup_plan: "remove both log lines after confirmation"
    authorization: "user approved temporary instrumentation"
```

**Banned diagnostics**:

- secret logging (passwords, tokens, keys)
- PII logging (personal data in logs)
- permanent debug logs in production
- race-condition-altering excessive logging (logging that changes timing enough
  to hide the race)
- arbitrary log insertion based on line numbers (insert logs at semantic
  points, not at random lines)
- unauthorized source mutation (never modify source without approval)

## 24. Security and Privacy

When the path touches sensitive data, mark in the graph:

- trust boundaries (where trust level changes)
- auth context (identity at each node)
- authz decisions (permission checks and their results)
- credentials (where they are stored, transmitted, used)
- tokens (session, API, refresh tokens)
- PII (personal data locations and flows)
- encrypted/plaintext transitions (where data is encrypted or decrypted)
- external sinks (where data leaves the system)
- logging sinks (what gets logged, where)
- storage boundaries (where data is persisted)

```yaml
security_marks:
  - node: "n-09"
    trust_boundary: true
    auth_context: "user token present"
    authz_decision: "role check at n-09 entry"
    credentials: "none"
    tokens: "session token in header"
    pii: "none"
    encryption: "TLS in transit"
    external_sink: false
    logging_sink: "request log (no body)"
    storage_boundary: "sqlite, encrypted at rest"
```

**Rule**: Never record secret values or actual PII in the causal map.
Record references ("token in header", "user id hashed") instead of values.

## 25. Failure Patterns

Record failure patterns with structure, not just a `likely_cause` field:

```yaml
failure_patterns:
  - id: "fp-01"
    symptom: "TTS button click produces no sound"
    candidate_causes:
      - "engine not initialized before play()"
      - "play() error swallowed by catch block"
      - "audio session not configured on this platform"
    discriminating_evidence:
      - "log line TTS/play/001 with error code 3"
      - "init() log absent before play() log"
    layers_or_nodes: ["n-02", "n-05", "n-07"]
    safe_diagnostics: ["read logs for init/play order", "run tts_test.dart"]
    common_false_leads:
      - "button not wired (disproven: handler fires, log present)"
      - "volume muted (disproven: other audio plays)"
    confidence: MEDIUM
```

**Rule**: Distinguish actual incident evidence from general heuristics. A
pattern's `candidate_causes` are hypotheses, not facts; only `discriminating_evidence`
observed in this incident raises confidence.

## 26. Causal Conclusions

Each analysis ends with a causal conclusion:

```yaml
causal_conclusion:
  status: PROBABLE
  symptom: "no audio; idle state; no error shown"
  trigger: "user clicks TTS button (n-01)"
  preconditions: "voice list non-empty; service constructed"
  root_cause: "engine init skipped when voice list empty (n-02); play() called on uninitialized engine"
  contributing_factors: ["error swallowed at n-05", "no init-state assertion"]
  propagation: "uninitialized engine → play() error code 3 → swallowed → idle state"
  amplifiers: ["none"]
  detection_gaps: ["no test for empty voice list", "no log on swallowed error"]
  affected_paths: ["chain-tts-playback", "chain-tts-voice-list"]
  evidence: ["ev-01", "ev-03", "ev-05"]
  confidence: MEDIUM
  unresolved_questions: ["whether init() failure is also possible with non-empty list"]
```

**Conclusion states** (exactly one):

- `CONFIRMED`: root cause proven by discriminating test or counterfactual.
- `PROBABLE`: strong evidence, no contradiction, not fully proven.
- `POSSIBLE`: plausible but insufficient evidence.
- `NOT_ESTABLISHED`: no causal link established.
- `MULTIPLE_CAUSES`: more than one independent cause produces the symptom.

**Rule**: Do not use `CONFIRMED` if the root cause is not proven. When in
doubt, use `PROBABLE` or `POSSIBLE` and list the unresolved questions.

## 27. Revision Freshness

Every analysis is bound to the revision it was performed on. Re-verify claims
when any of the following changed before relying on a prior analysis:

- a node's symbol changed (renamed, moved, deleted)
- an edge's contract changed (signature, payload, return type)
- a schema changed (data model, serialization)
- a queue/topic changed (name, partition, delivery semantics)
- a state owner changed (state moved to another component)
- an API/FFI changed (endpoint, bridge function, types)
- a dependency changed (library version, service)
- a test was deleted (coverage evidence removed)
- deployment topology changed (new service, new replica, new region)
- environment/config changed (feature flag, config key)

**Rule**: Do not assume a date alone means current. An analysis is fresh only
if its analyzed revision matches the current revision and no stale condition
applies. Re-verify before relying on it.

## 28. Scalability

Prioritize analysis by value. Do not force complete analysis of all features at
kickoff.

**Priority order**:

1. user core journeys (login, checkout, main workflow)
2. security-sensitive paths (auth, payments, PII)
3. payment/data-loss paths (anything that can lose money or data)
4. frequent incidents (features that break often)
5. multi-subsystem boundaries (paths crossing services/processes)
6. frequently-changing paths (code churn correlates with bugs)
7. low-coverage paths (little or no test coverage)
8. high fan-in/fan-out paths (many callers or many consumers)

**Rule**: When scope exceeds available effort, analyze the highest-priority
paths first and record the rest as planned-but-not-analyzed. A partial map of
the top priorities beats an empty map of everything.

## 29. Bug-Fix Workflow Integration

When this skill is used to guide a bug fix, follow this sequence:

1. **DEFINE SYMPTOM AND REVISION**: record the symptom, expected/actual
   behavior, and the revision under analysis (section 6).
2. **REPRODUCE**: reproduce the symptom or mark the evidence limit
   (`REPRODUCED` / `INTERMITTENT` / `NOT_REPRODUCED`).
3. **BUILD THE RELEVANT SUBGRAPH**: build the typed node/edge graph for the
   affected path (sections 10-12).
4. **COMPARE EXPECTED AND OBSERVED PATHS**: overlay the failure path on the
   expected path (section 13).
5. **FIND FIRST DIVERGENCE**: locate the first diverging edge.
6. **IDENTIFY VIOLATED INVARIANT**: state which invariant broke and where
   (section 14).
7. **TEST CAUSAL HYPOTHESES**: run discriminating tests for competing
   hypotheses (sections 15-16).
8. **CONFIRM OR QUALIFY ROOT CAUSE**: use the conclusion states; do not claim
   `CONFIRMED` without proof (section 26).
9. **MAP IMPACT AND CONTRACTS**: list affected consumers and contracts
   (section 21).
10. **IMPLEMENT MINIMAL SUFFICIENT FIX**: fix the root cause, not the symptom.
11. **RUN REGRESSION AND AFFECTED-PATH TESTS**: run the regression test (fail
    before/pass after) plus tests for affected paths and consumers.
12. **RETRACE MODIFIED EDGES**: re-verify every edge the fix touched.
13. **RE-BIND TO REVISION**: record the revision the verified analysis applies
    to; re-verify after future changes (section 27).

**Post-fix verification checklist**:

- [ ] modified nodes re-verified
- [ ] changed edges re-traced
- [ ] violated invariant now holds
- [ ] original failure path no longer reachable
- [ ] success path still works
- [ ] error/retry branches still behave correctly
- [ ] direct consumers of changed contracts tested
- [ ] affected public contracts unchanged or intentionally changed

## 30. Status Model

**Analysis states** (exactly one per analysis):

- `COMPLETE`: scope and revision clear, trigger identified, key nodes/edges
  evidenced, expected path defined, failure path and divergence defined for
  incidents, confidence explicit, uncertainties disclosed, impact and tests
  connected, freshness confirmed.
- `COMPLETE_WITH_UNCERTAINTY`: complete except some claims are unproven;
  uncertainties are disclosed.
- `PARTIAL`: some required elements missing (e.g., no failure path, no
  evidence).
- `BLOCKED`: cannot proceed (missing environment, data, or access).
- `STALE`: was complete but no longer matches the current revision.
- `NOT_ESTABLISHED`: no causal link established.

**Node/edge states** (exactly one per node/edge):

- `VERIFIED`: confirmed by runtime observation or test.
- `INFERRED`: reasoned from other evidence.
- `HYPOTHESIZED`: proposed, not tested.
- `UNRESOLVED`: cannot be determined with available evidence.
- `STALE`: no longer matches the current revision.

**`COMPLETE` requires all of**:

1. clear scope and revision
2. identified trigger
3. key nodes/edges with evidence
4. defined expected path
5. failure path + divergence for incidents
6. explicit confidence
7. disclosed uncertainties
8. connected impact and tests
9. confirmed freshness

**Rule**: If any requirement is missing, the status is not `COMPLETE`. Report
the missing element explicitly.

## 31. Report Format

Produce a structured markdown report with these sections:

```markdown
# Causal Chain Analysis Report

## Status
One sentence: analysis state, conclusion state, and confidence.

## Scope
- analysis_id, mode, repo, branch, revision, artifact, environment
- feature or incident
- reproduction state and frequency
- scope confidence (how sure the scope covers the real path)

## Symptom and Expected Behavior (incidents only)
- symptom, expected behavior, actual behavior

## Causal Summary
- trigger, preconditions
- first divergence, violated invariant
- root cause, contributing factors, propagation, amplifiers, detection gaps
- final symptom, conclusion state, confidence

## Graph Summary
- node count, edge count, branch count
- processes/services involved, external boundaries crossed

## Evidence Matrix
| Claim | Evidence type | Source | Confidence | Limitations |
|---|---|---|---|---|

## Hypotheses
| Hypothesis | Predicted observation | Evidence | Status | Confidence |
|---|---|---|---|---|

## Impact Map
| Target | Relationship | Risk | Tests | Confidence |
|---|---|---|---|---|

## Coverage Gaps
- nodes/edges/branches with NOT_COVERED or PARTIALLY_COVERED

## Safe Diagnostic Plan (only if needed)
- planned diagnostics with cleanup and authorization

## Limitations and Unknowns
- unresolved questions, evidence limits, environment limits

## Freshness
- analyzed revision, stale conditions

## Final Statement
One of:
1. Root cause confirmed and fix verified.
2. Root cause probable; fix applied; confirmation pending.
3. Root cause possible; more evidence needed.
4. No causal link established.
5. Analysis blocked; state the blocker.
```

## 32. Prohibited Behaviors

Never do any of the following:

- equating a call chain with causality
- forcing a fixed UI→State→Service→Bridge→Backend→Return layer sequence
- creating placeholder nodes/edges for missing layers
- ignoring branches, error paths, or async edges in a "linear chain"
- confirming runtime paths from static search alone
- treating the trigger as the root cause
- repeating the symptom as the root cause
- confirming the first plausible hypothesis
- using a line number as stable identity
- verifying all files unconditionally (verify affected paths and contracts)
- adding diagnostic logs without approval
- recording secrets or PII in the causal map
- marking unverified edges as `VERIFIED`
- claiming an unverified incident as `REPRODUCED`
- declaring a root cause `CONFIRMED` when the conclusion is uncertain

## 33. Compact Protocol

When time or context is limited, run this compact sequence. It preserves the
evidence discipline while minimizing steps:

1. **DEFINE SCOPE, REVISION, ARTIFACT, AND ENVIRONMENT** (section 6).
2. **RECORD EXPECTED AND ACTUAL BEHAVIOR** (section 13).
3. **REPRODUCE OR MARK EVIDENCE LIMIT** (reproduction states, section 6).
4. **IDENTIFY TRIGGERS AND PRECONDITIONS** (section 8).
5. **BUILD TYPED NODES AND EDGES** (sections 10-11).
6. **MAP BRANCHES, ERRORS, RETRIES, AND ASYNC BOUNDARIES** (sections 12, 17,
   20).
7. **SEPARATE OBSERVED, VERIFIED, INFERRED, AND HYPOTHESIZED LINKS**
   (section 9).
8. **COMPARE EXPECTED AND FAILURE PATHS** (section 13).
9. **FIND FIRST DIVERGENCE AND VIOLATED INVARIANT** (section 14).
10. **TEST COMPETING CAUSAL HYPOTHESES** (sections 15-16).
11. **CLASSIFY ROOT CAUSE, CONTRIBUTORS, PROPAGATION, AND DETECTION GAPS**
    (sections 8, 26).
12. **MAP IMPACT, CONTRACTS, AND TEST COVERAGE** (sections 21-22).
13. **REPORT CONFIDENCE AND UNKNOWNS** (section 31).
14. **BIND RESULTS TO THE CURRENT REVISION AND STALE CONDITIONS**
    (section 27).

## 34. Project Profile (Example)

Project-specific commands and conventions for a Flutter + Rust project. This
subsection is project-specific and does not override the generic logic above.

- **Bug-fix delegation**: when a bug fix is delegated, build the relevant
  subgraph for the affected path before coding.
- **Verification commands** (run from the repo root):
  - Flutter: `flutter test test/features/<feature>/`
  - Rust: `cargo test -p <crate>`
  - Full: `flutter analyze && flutter test`
- **Diagnostics**: temporary instrumentation must be removed before reporting
  completion; report the cleanup in the report file.
- **Security**: never log tokens, credentials, or PII; mark trust boundaries in
  the graph (section 24).
- **Freshness**: after any fix, re-bind the affected analyses to the new
  revision (section 27) and report the change in the report file.
