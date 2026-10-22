---
name: dependency-hallucination-check
description: >
  Prevents hallucination of non-existent libraries, deprecated packages, or incorrect function names. Mandatory check before adding new external dependencies.
---

# Dependency Hallucination Check

## The One Rule

**Never write code against a dependency you have not verified in its exact
version, in this session, against external evidence.**

Model memory is only a source of *candidates*, never confirmation. Every claim
about a package name, version, API symbol, import path, feature, config key,
generated filename, or platform support must pass three checks before
dependency-related code is written:

1. **Identity**, the package exists in the configured registry, under the
   exact canonical name, published by the expected owner (not a typosquat or
   similar-name wrong package). A public-registry 404 is NOT proof of absence:
   check private registry/scope config, auth, mirrors, workspace packages,
   git/path deps, and aliases first.
2. **Exact version**, the version the project uses (or will use) is
   determined from manifest + lockfile + repo policy, NOT "latest". Docs for
   `latest` are not evidence for an older pinned version. If the version
   cannot be determined from evidence, mark `VERSION_UNRESOLVED`, never
   insert an arbitrary "latest".
3. **Exact-version API**, every symbol, signature, import path, subpath,
   feature/extra, plugin ID, config key, and generated filename is verified
   against the exact version's source, type stubs, or official versioned docs, BEFORE writing the code, not after import/build errors surface.

## Core Principles

- **Repository Before Registry**, Check the current repository (manifest,
  lockfile, workspace, existing code) first. Many "dependencies" are already
  present, local, workspace, or built-in (standard library, platform SDK,
  framework built-ins). Never run an external lookup for those.
- **Existence Is Not Suitability**, A package existing in a registry does not
  mean it is appropriate. Also check: no equivalent already in the repo,
  standard library/framework built-in insufficient, size/maintenance cost
  justified.
- **Distinguish Every Axis**, package existence ≠ project compatibility;
  latest version ≠ compatible version; install status ≠ registry existence;
  public registry ≠ private registry; distribution name ≠ import name.
- **Read-Only by Default**, Verification never modifies the manifest,
  lockfile, or source. `npm view`, `cargo info`, registry APIs, and PyPI/pub.dev
  JSON metadata are read-only; `npm install`/`cargo add`/`pip install`/code
  generators are mutations that require explicit user request.

## When to Use

Run this check when ANY of the following applies:

- Adding a new external dependency, import, plugin, or code generator.
- Using a new API symbol, feature, extra, or subpath of an existing dependency
  ("already in the repo" only covers what the repo already uses).
- Changing a dependency's version (upgrade, downgrade, or pin).
- Replacing one dependency with another.
- Learning an API from examples, tutorials, or documentation.
- Diagnosing import errors, missing-symbol errors, or codegen failures.

NOT required for: already-verified same-version usage, standard library APIs,
local modules, user-provided source, or pure design discussion.

## Evidence Hierarchy

When sources conflict, prefer higher priority:

1. Repo manifest/lockfile (what the project actually declares and resolves).
2. Installed exact-version source or type stubs (`.d.ts`, crate source).
3. Official versioned API docs for the exact version.
4. Official registry metadata (npm registry, crates.io API, PyPI JSON,
   pub.dev API, Maven metadata, Go proxy, NuGet API).
5. Official tagged source of the exact version / official release notes.
6. Reliable secondary sources. Search snippets, post titles, and AI summaries
   are NEVER sufficient confirmation on their own.

## Supply-Chain Risk Signals (report, block on serious ones)

- One-letter difference from a famous package, or different owner/repo.
- Recent ownership change; abnormal version history (jumps, many yanks).
- Install/build scripts (postinstall, setup.py network access, build.rs).
- Yanked exact version, deprecated/archived status, known advisories.
- Unofficial wrapper not referenced in official docs; name-squatted stub.

## Failure Diagnosis

On an import/API error, do NOT immediately guess alternative packages:

1. Record the exact error and stack trace.
2. Check manifest (declared?), lockfile (resolved?), installed location
   (which version is actually present?).
3. Verify package/import identity (distribution name ≠ import name).
4. Verify the symbol exists in the exact version; check features/extras,
   conditional exports, generated code, local shadowing.
5. Check official migration/release notes; fix with a targeted build/test.

Classify the cause (`PACKAGE_NOT_DECLARED`, `WRONG_VERSION`,
`FEATURE_NOT_ENABLED`, `LOCAL_NAME_COLLISION`, `NETWORK_BLOCKED`, ...), never express a network/auth failure as package absence.

## Verdicts

- `APPROVED`, identity, exact version, exact-version API, compatibility, and
  necessity all confirmed. `APPROVED_WITH_CONSTRAINTS`, with documented
  constraints (version, feature, platform).
- `REJECTED`, fails existence, identity, compatibility, necessity, or
  supply-chain checks.
- `BLOCKED`, verification could not complete (network, auth, ambiguity).
- `INCONCLUSIVE`, evidence insufficient.

Record with every verdict: confidence (`HIGH`/`MEDIUM`/`LOW`/`UNKNOWN`),
evidence source, exact version verified, registry used, tool + command, repo
HEAD, and verification timestamp. Re-verify when version, manifest, lockfile,
toolchain, or registry config changes.

## Prohibited

1. Adding a dependency from memory alone without external verification.
2. Choosing between similar names arbitrarily without identity verification.
3. Treating a public-registry 404 (or `pip show` absence, or `cargo search`
   fuzzy hits, or `flutter pub outdated` output) as existence/absence proof.
4. Always using "latest stable"; using latest-version docs as evidence for a
   pinned older version.
5. Guessing API names, signatures, feature names, config keys, generated
   filenames, plugin IDs, Maven coordinates, or Gradle plugin IDs.
6. Assuming a subpath exists because the package root exists; assuming a
   package supports the project's platform/peers/license without checking.
7. Assuming a package is declared because an import succeeded (transitive).
8. Writing generated files by hand; running install/add/codegen without
   explicit user request.
9. Recommending a deprecated package or confusing official packages with
   unofficial forks; citing non-existent repos, publishers, versions, or docs.
10. Declaring "official", "compatible", "safe", or any verdict without
    recorded confidence and evidence.

## Compact Protocol

```
CLASSIFY THE REFERENCE (stdlib / platform / framework / local / workspace /
generated / public / private / git / path / vendored / alias)
→ INSPECT REPO MANIFEST, LOCKFILE, AND REGISTRY CONFIG FIRST
→ CHECK BUILT-IN, LOCAL, WORKSPACE, AND EXISTING ALTERNATIVES
→ VERIFY CANONICAL PACKAGE IDENTITY (name, owner, repo, registry)
→ DETERMINE THE EXACT VERSION (policy > workspace > compat > constraints;
  never blind "latest")
→ VERIFY PACKAGE-TO-IMPORT MAPPING AND SUBPATHS
→ VERIFY EVERY API SYMBOL/SIGNATURE/FEATURE IN THE EXACT VERSION
→ CHECK DEPRECATION, YANK, AND SUPPLY-CHAIN SIGNALS
→ ASSESS NECESSITY (built-in? existing utility? redundant?)
→ APPROVE / REJECT / BLOCK / INCONCLUSIVE, with confidence + evidence
→ MUTATE (install/add/codegen) ONLY ON EXPLICIT REQUEST, THEN RE-VERIFY THE
  ACTUALLY RESOLVED VERSION AND BUILD/TYPE-CHECK/TEST
```
