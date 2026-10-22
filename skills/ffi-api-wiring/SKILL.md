---
name: ffi-api-wiring
description: >
  Wire a new Rust CRUD function through the flutter_rust_bridge (FRB) layer. Covers state.rs singleton, type conversion, api module, codegen, and Dart service. For Flutter+Rust projects using flutter_rust_bridge v2.
---

## Core Principles

1. **Contract First**: Define the Dart caller's contract before writing Rust function.
2. **Existing Pattern First**: Read FRB config, existing API modules, generated output,
   state init, error types, conversion patterns, Dart bridge wrapper, build/codegen
   scripts, related tests before creating anything new.
3. **Boundary Types Must Be Explicit**: Use stable owned types at FFI boundary. Verify
   support in actual FRB version. Check: integer width/signedness, String, bytes,
   list/map, enum, nullable/optional, timestamp/timezone, identifier, nested struct,
   large payload, opaque handle.
4. **Errors Are Part of the API**: Don't flatten all errors to single string. Provide
   stable error categories (validation/not_found/conflict/permission/unavailable/
   cancelled/internal) if Dart needs different recovery. Don't expose SQL, paths,
   credentials, stack traces in user errors.
5. **Generated Code Is Derived**: Never modify generated files directly. Modify source
   API + generator config, then run repo-defined codegen.

## Execution Flow

```
INSPECT → DEFINE CONTRACT → DESIGN BOUNDARY TYPES → IMPLEMENT RUST → GENERATE
→ VERIFY GENERATED API → WRAP IN DART → TEST BOTH SIDES → CHECK PLATFORM BUILD
```

## Sections

### 1. INSPECT
Record: repo/revision, FRB + toolchain version, Rust API source root, generated
Dart/Rust locations, generator command + cwd, state initialization pattern,
current error model, target platforms, working-tree state. Repo instructions
and existing scripts take priority over generic examples.

### 2. CONTRACT AND COMPATIBILITY
Determine: Dart-visible function name, parameter names + nullable, return type,
sync/async/stream, error behavior, cancellation/timeout, threading/blocking
characteristics, impact on existing callers. Don't rename or remove existing
public APIs, add new API and manage caller migration as a separate step.

### 3. STATE AND CONCURRENCY
Don't force global singleton as default. Follow repo's existing state ownership.
Review: initialization-before-call, lock poisoning, lock scope (minimum only),
blocking I/O, deadlock (nested locks, ordering), reentrant callback, Send/Sync,
task/thread affinity, disposal/shutdown, concurrent mutation. Don't await or
perform long I/O while holding a lock. Follow repo's worker/async strategy.

### 4. RUST IMPLEMENTATION
Boundary responsibilities only: boundary input validation, internal service call,
boundary output mapping, stable error mapping. Don't duplicate business logic
in FFI wrapper. Don't use panic as normal error handling. Review panic possibility
across FFI boundary. Keep conversions explicit and testable. Don't hide value loss,
truncation, timezone changes, or enum fallbacks.

### 5. TYPE STRATEGY
Value type: small immutable data where Dart needs field access/serialization/UI
rendering. Opaque type: internal state, large objects, native resources, Rust-managed
lifecycle. Don't unconditionally create opaque→value helpers, expose only needed
projection DTOs. Review copy cost and memory peak for large collections/binary.

### 6. CODEGEN
Before: verify command + cwd, generator/config version, record working-tree baseline.
After: verify exit code, generated signatures, nullability, async/stream form,
import paths, converter generation, unexpected mass diff, deletions/renames,
generated↔API source consistency. Don't guess generated names from docs. If codegen
fails, don't manually write generated files, fix source or config instead.

### 7. DART WIRING
Place thin service/repository wrapper over generated API if needed. Wrapper
responsibilities: domain-friendly naming, error translation, cancellation/lifecycle
connection, stable model for UI, logging/metrics boundary, test substitution.
Don't let UI/provider call generated API directly, follow existing architecture.
Don't replace errors with defaults. Distinguish empty data from failure.
Provider/state: represent loading/data/empty/error + refresh/cancel.

### 8. TESTING
Minimum layers: Rust domain/service (ops + errors), Conversion (all fields, enum,
optional, boundary values), Rust FFI API (validation + error mapping), Generated
contract (expected Dart signature), Dart wrapper (success/failure/cancellation),
Integration (Dart→Rust→effect→Dart), Platform (target native build/link/package).
For CRUD: verify create/read/update/delete × success/not-found/invalid-input/conflict.
Don't claim unrun tests or platform builds as passed.

### 9. COMPLETION CRITERIA
ALL required: FRB pattern+version confirmed, Dart/Rust contract specified,
boundary types+error semantics stable, state+concurrency risks reviewed,
codegen succeeded, generated API read and verified, generated files not modified,
Dart wrapper doesn't hide errors, Rust+Dart tests passed, integration/platform
build passed, API compatibility impact recorded, working tree has only expected
changes. States: COMPLETE, COMPLETE_WITH_LIMITATIONS, BLOCKED, INCOMPATIBLE,
CODEGEN_FAILED.

### 10. OUTPUT FORMAT (complex work)
```markdown
# FFI Wiring Result
- Contract / Rust API / Boundary types / State-concurrency / Generated API
- Dart wrapper / Compatibility / Validation / Platforms / Limitations
```

## Prohibited
Forcing global singleton pattern, flattening all errors to one string, awaiting/
callbacking/long-I/O while holding lock, exposing internal DB models without review,
guessing generated function names/filenames, modifying generated code directly,
running arbitrary codegen without checking repo command, swallowing exceptions with
defaults, confusing failure with empty state in UI, declaring Dart wiring complete
from Rust tests alone, declaring integration complete from codegen success alone,
upgrading dependencies without user request, claiming unsupported builds as supported.

## Compact Protocol
```
READ EXISTING FRB CONFIGURATION AND PATTERNS → DEFINE THE DART-RUST CONTRACT
→ CHOOSE VALUE OR OPAQUE TYPES → DESIGN STRUCTURED ERRORS → VERIFY STATE, LOCK,
THREAD, AND LIFECYCLE RULES → IMPLEMENT A THIN RUST BOUNDARY → RUN REPOSITORY-
DEFINED CODEGEN → INSPECT THE ACTUAL GENERATED API → ADD A THIN DART WRAPPER
WITHOUT HIDING FAILURES → TEST CONVERSION, ERRORS, INTEGRATION, AND TARGET BUILDS
→ REPORT ONLY VERIFIED COMPATIBILITY
```
