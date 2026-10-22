---
name: frb-codegen-troubleshoot
description: >
  Fix flutter_rust_bridge codegen failures. Diagnose opaque type issues, missing imports, SseEncode trait errors, and type visibility problems. For Flutter+Rust projects using flutter_rust_bridge v2.
---

## 1. Core Principles and Diagnosis Flow

**Principles:**
1. **Identify the Failing Stage First**, Don't fix just because error contains a familiar type name.
2. **Exact Error Before Fix**, Preserve first failing command, exit code, first meaningful error. Prioritize first cause over dozens of subsequent errors.
3. **Existing Configuration Is Authoritative**, Prefer manifest, lockfile, FRB config, repo scripts over README examples.
4. **Generated Code Is Read-Only**, Never modify generated Rust/Dart files directly. Fix source API or config, then regenerate.
5. **Minimal Reversible Fix**, Don't arbitrarily simplify domain models or public APIs just to pass codegen.

**Flow:** CAPTURE → CLASSIFY STAGE → VERIFY VERSIONS AND CONFIG → ISOLATE FIRST ROOT ERROR → INSPECT TYPE/EXPORT/FEATURE GRAPH → APPLY MINIMAL FIX → REGENERATE → INSPECT DIFF → BUILD AND ANALYZE BOTH SIDES

## 2. CAPTURE

Record YAML: `repository`, `revision`, `command`, `cwd`, `exit_code`, `first_error`, `failing_stage`, `frb_versions`, `rust_toolchain`, `dart_flutter_versions`, `target_platform`, `working_tree`.
Try clean reproduction with repo-defined codegen command.
Don't run clean/reset to delete user changes.

## 3. CLASSIFY STAGE

Seven stages to distinguish:

- **CONFIG**, config not found, wrong paths, crate/module root error, duplicate config, wrong cwd.
- **PARSE**, unsupported/malformed syntax, macro limits, unresolved module, conditional compilation, parser vs compiler source mismatch.
- **TYPE MODEL**, unsupported boundary type, opaque/value selection error, generic/lifetime/trait object, private/unreachable type, missing codec, recursive type, unsupported map/set/key.
- **GENERATED RUST COMPILE**, missing import, unresolved type, duplicate symbol, missing trait impl, stale output, feature/crate dependency mismatch.
- **NATIVE BUILD/LINK**, dart-sys, compiler/linker, SDK discovery, target toolchain, env vars, native lib/architecture mismatch.
- **DART/FLUTTER**, generated import mismatch, stale generated file, nullability/signature drift, duplicate generated library, analyzer/platform build error.
- **RUNTIME**, bridge initialization, dynamic library loading, symbol lookup, ABI/artifact mismatch.

## 4. VERSION AND CONFIG CHECK

Verify from actual files and resolved metadata:
Rust FRB dependency, Dart FRB package, codegen CLI or repo-pinned executable,
related runtime/helper packages, config schema, input Rust root, generated Rust/Dart output,
crate features, build scripts.

Don't assume all components must have same numeric version, judge by compatibility matrix and repo lockfile.
Don't present fixed-version `cargo install` commands.
Don't upgrade/downgrade without user request.

## 5. TYPE AND VISIBILITY DIAGNOSIS

For `cannot find type`, codec trait, opaque errors, separate and check in order:
1. Type actually compiles in current feature/target.
2. Reachable in FRB's module graph.
3. Import/re-export path correct.
4. Type should be public at boundary.
5. Value vs opaque contract match.
6. Required derive/attribute/codec supported in current FRB version.
7. Internal field types also supported.
8. Generated output references stale API.

Public re-export is a possible visibility fix but doesn't auto-solve codec trait or unsupported field issues.
Don't expose internal domain types as public API just for codegen, use boundary DTO or opaque handle.

## 6. PARSER FAILURE

Don't arbitrarily simplify source on parser errors. First verify:
exact file and token, syntax valid for Rust compiler, macro or generated source,
varies by cfg/feature, unsupported type hidden behind type alias,
codegen input root correct, current FRB version support range.
`HashSet→Vec`, `&str→String`, generic removal only when contractually valid and actually the cause.

## 7. NATIVE BUILD FAILURE

For dart-sys or native build errors: verify Dart/Flutter SDK detection, PATH + required env,
host/target architecture, Rust target installation, compiler/linker, platform SDK,
package version compatibility, stale build cache possibility, CI vs local environment differences.

Don't conclude from single version mismatch.
Cache deletion, tool reinstall, global env changes, suggest as separate action with impact and rationale.

## 8. REGENERATION

Before: record working-tree baseline, actual generator command, config + cwd,
expected output paths, existing generated file locations.
After: verify exit code, created/modified/deleted files, unexpected mass diff,
duplicate output, stale symbols, generated signatures, import paths, nullability + async forms.
Don't declare problem solved from codegen success alone.

## 9. VERIFICATION AND STATUS

Minimum applicable checks:

| Stage | Check |
|-------|-------|
| Rust source | format/check or target crate build |
| Generated Rust | compile |
| Generated Dart | analyze |
| Contract | generated signature inspection |
| Integration | targeted Dart→Rust call |
| Platform | affected Flutter/native build |

After error fix, re-run the original failing command.
Mark previous success results as STALE when related source changed.

**Status states:** `RESOLVED`, `RESOLVED_WITH_LIMITATIONS`, `ROOT_CAUSE_IDENTIFIED`, `PARTIAL`, `BLOCKED`, `INCONCLUSIVE`.
RESOLVED requires: original failure reproduced, failing stage + root cause confirmed,
minimal source/config fix applied, generated files not directly modified, codegen re-run succeeded,
generated diff reviewed, Rust + Dart verification passed, original failing command passes.
Output format: Status / Failing stage / First root error / Root cause / Versions+config checked / Source fix / Generated diff / Verification / Platforms checked / Remaining limitations.

## 10. PROHIBITED AND COMPACT PROTOCOL

**Prohibited (12 items):**
1. Judging cause by subsequent error count alone.
2. Fixing SseEncode errors with unconditional re-export.
3. Converting opaque to DTO unconditionally.
4. Simplifying types unconditionally on parser errors.
5. Assuming dart-sys errors are always version mismatch.
6. Installing fixed FRB CLI version.
7. Modifying generated files directly.
8. Running clean/reset to delete user changes.
9. Unjustified full cache deletion.
10. Declaring integration complete from codegen success alone.
11. Declaring Dart API normal from Rust compile alone.
12. Claiming unverified platform builds as passed.

**Compact Protocol:**
REPRODUCE THE EXACT COMMAND → CLASSIFY CONFIG/PARSE/TYPE/COMPILE/LINK/DART/RUNTIME STAGE → PRESERVE THE FIRST ROOT ERROR → VERIFY ACTUAL VERSIONS/FEATURES/PATHS/OUTPUTS → DISTINGUISH VISIBILITY/CODEC/OPAQUE/UNSUPPORTED-TYPE FAILURES → APPLY THE SMALLEST SOURCE OR CONFIG FIX → REGENERATE WITHOUT EDITING GENERATED FILES → INSPECT GENERATED DIFF AND SIGNATURES → REBUILD RUST/ANALYZE DART/RETEST ORIGINAL FAILURE → REPORT VERIFIED RESULTS AND LIMITATIONS
