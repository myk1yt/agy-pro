---
name: search-tool-discipline
description: >
  Prevents tool abuse and hallucination when using search tools (Grep, Glob). Enforces literal-first, narrow-first queries, and prevents blind retries.
---

# Search Tool Discipline

## The Three Rules

1. **Literal-First**, When the exact string, symbol, filename, error message,
   or config key is known, search with a fixed literal string. Regex is only
   for spelling variants in one pass, structural patterns, or unknown exact
   symbols, and only after a literal attempt was insufficient, the scope is
   limited, and the dialect/escaping is verified. First search = literal.
2. **Narrow-First**, Start with the narrowest reasonable scope (current
   file → package → feature → workspace → repository). Widen only with a
   stated justification: 0 results, insufficient results, or the question
   inherently spans wider. Never guess paths (`src/components` may not exist), pick paths from repo structure, import paths, or manifests; if the layout is
   unknown, do a quick structure discovery first.
3. **Zero Results ≠ Absence**, `NO_MATCH_IN_SCOPE` means only "no match for
   THAT query/path/include/exclude/tool". Before concluding anything about
   existence, check in order: correct repo and revision → path actually exists
   → spelling → casing → hyphen/underscore/alias variants → filename vs import
   name → generated source → ignored/excluded areas → other packages in the
   workspace → dynamic construction (reflection, string registration) →
   history. Conclude "no match found in the inspected scope", never "doesn't
   exist" for uninspected areas.

## Supporting Discipline

- **Search With a Question**: every search answers a specific question ("find
  the definition and direct callers of `refreshSession`"). State the intent
  before searching.
- **Search Results Require Reading**: read the enclosing function/file and
  verify the match is the definition (not a reference, re-export, overload,
  mock, or generated declaration) before concluding. Distinguish same-name
  symbols by package, signature, and call context.
- **Volume Control**: estimate result count first; narrow by path, extension,
  exact casing, or excludes when it could flood. If results are truncated or
  capped, narrow and re-run, never skim a flood and guess, and never treat a
  truncated list as exhaustive.
- **Classify Every Outcome**: `MATCHES_FOUND`, `NO_MATCH_IN_SCOPE`,
  `TOO_MANY_RESULTS`, `TRUNCATED`, `PATH_NOT_FOUND`, `INVALID_QUERY`,
  `TIMEOUT`, `TOOL_ERROR`, `INDEX_STALE`, `PERMISSION_DENIED`. A tool error,
  wrong path, or invalid query is NOT a genuine no-match.
- **Retry Only With a Changed Condition**: each retry must state what changed
  (path, include/exclude, case sensitivity, literal↔regex mode, revision,
  tool). Re-running identical conditions expecting a different outcome is
  banned.
- **Include Excluded Areas When They're the Target**: generated code, vendor,
  submodules, build output, and history are normally excluded, but they are
  exactly where codegen errors, FFI bindings, and packaged-artifact issues
  live. Decide exclusions per question, not unconditionally.
- **Stop When the Question Is Answered**: don't stop at the first match when
  the question needs all callers/consumers; don't keep searching the whole
  repo once a sufficient exact match is verified. When the next step is file
  reading, testing, or runtime observation, switch to that.
- **Read-Only**: searching never modifies source, installs dependencies,
  rebuilds indexes, changes checkouts, or runs builds. Any such action is a
  separate, justified mutation phase.
- **Sensitive Data**: if results contain secrets or PII, reference by location
  only; never echo values into reports or queries.

## When to Use

Locating or verifying code: symbol definition/references, import/export
relations, callers/callees, error-string sources, config keys, similar
implementations, change-impact scope, test locations, generated-code ↔ source
connections. Skip when an exact file + line was given (read it directly), the
answer needs external docs (use web/registry search), or the task is
search-unrelated.

## Report Format

For substantial searches: status (`COMPLETE` / `COMPLETE_WITH_LIMITATIONS` /
`PARTIAL` / `BLOCKED` / `INCONCLUSIVE`), question, scope (revision, paths,
includes/excludes), search strategy (step / intent / query / path / result),
confirmed results with qualified identity and evidence status, no-match areas
(stated as scoped, not total), limitations (excluded areas, dynamic
possibilities, truncation, stale index), and conclusion separating verified
facts from inference. Simple lookups need only path + symbol + verification
status.

## Prohibited

- Complex regex on the first search when a literal would do.
- Whole-repo search when a targeted path would do; guessing narrow paths and
  concluding absence from the resulting 0 hits.
- Repeating the same query unchanged; confusing tool errors with no-match;
  reporting "no usages anywhere" when only one directory was inspected.
- Dumping unbounded results or treating capped output as complete.
- Auto-excluding generated code when the answer lives there (or the reverse:
  flooding on vendor/build output when it's irrelevant).
- Concluding from the matched line alone; confusing definition/reference/import
  with runtime call; proving a call graph from text search alone (label edges
  `STATICALLY_VERIFIED` vs `POSSIBLE_DYNAMIC`).
- Mutating source, installing deps, or rebuilding indexes as a search side
  effect; echoing secrets found in results.

## Compact Protocol

```
DEFINE THE SEARCH QUESTION → CLASSIFY INTENT → CONFIRM REPO/PACKAGE/SOURCE ROOT
→ START WITH A LITERAL IN THE NARROWEST JUSTIFIED SCOPE → READ AND VALIDATE THE
CONTEXT OF FIRST MATCHES → ON EXCESS, NARROW; ON ZERO, CLASSIFY THE OUTCOME AND
WIDEN DELIBERATELY (VARIANTS → ALIASES → GENERATED → OTHER WORKSPACES →
HISTORY) → VERIFY DEFINITION/REFERENCE IDENTITY → SEPARATE VERIFIED FACTS FROM
INFERENCE → STOP WHEN THE QUESTION HAS SUFFICIENT EVIDENCE → REPORT COVERAGE
AND LIMITATIONS
```
