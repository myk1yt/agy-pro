---
name: dependency-audit
description: >
  Analyze crate/module dependencies in a Rust workspace or package.json project. Detect circular dependencies, unused deps, version conflicts, and suggest refactoring. Works for Rust Cargo workspaces and Node.js projects.
---

# Dependency Audit

Universal, evidence-based dependency audit for Rust workspaces and Node.js
projects, with honest partial support for other ecosystems. The audit is
**read-only by default**: it inspects manifests, lockfiles, source usage, build
configuration, and tool output, then reports findings and recommendations. It
never mutates dependencies unless the user explicitly requests remediation.

---

## 1. Purpose

A dependency audit answers these questions:

1. **Dependency structure**, What does the project depend on, directly and
   transitively? Which packages/crates are internal vs external?
2. **Runtime/dev/build/test distinction**, Which dependencies are needed at
   runtime, which only for development, building, or testing?
3. **Circular dependencies**, Are there cycles in the workspace graph, and are
   they reachable at runtime or only in dev/build/test contexts?
4. **Unused dependencies**, Which declared dependencies are never used by
   source, build scripts, tests, examples, or tooling?
5. **Version conflicts**, Where do multiple versions of the same package
   coexist, and does that cause real problems (type identity, peer conflicts,
   duplicate native libraries, size/security exposure)?
6. **Vulnerable dependencies**, Which dependencies have known advisories, and
   are those vulnerabilities actually reachable from production code?
7. **License and supply-chain risk**, What licenses are in use, are they
   compatible with project policy, and is the lockfile/source chain trustworthy?
8. **Build/binary/bundle cost**, Which dependencies are heavy to compile,
   install, or ship, and are they feature-gated or tree-shakeable?
9. **Lockfile consistency**, Is the lockfile in sync with manifests? Are there
   unexpected churn, registry changes, or source changes?
10. **Dependency delta**, What changed between the base revision and the
    current revision (added/removed/upgraded dependencies)?
11. **Safe removal/upgrade candidates**, Which dependencies can be removed or
    upgraded with low risk, and what is the blast radius of each change?
12. **Confirmed vs needs-verification findings**, Which findings are backed by
    direct evidence, and which require further verification before acting?

---

## 2. Operating Principles

### Audit-First (Read-Only by Default)

The audit does **not** perform any of the following without an explicit user
request:

- Adding or removing a dependency
- Changing a dependency version
- Regenerating a lockfile
- Installing packages
- Changing workspace members
- Creating a new crate or package
- Refactoring source code
- Changing feature flags
- Changing the package manager
- Auto-cleanup of manifests
- Auto-fixing vulnerabilities
- Performing major upgrades
- Adding resolution overrides

If a command could mutate state (e.g., `cargo update`, `npm install`,
`pnpm install`), classify its mutation risk first (see Section 3 and Section 25)
and prefer dry-run or read-only variants.

### Evidence Before Recommendation

Every recommendation must be grounded in at least one of: manifest content,
lockfile content, source usage, build configuration, feature graph, or tool
output. Specifically:

- Do **not** create a crate/package just because documentation mentions it.
  Verify whether it exists, is planned, was renamed, or was deleted.
- Do **not** delete an empty directory immediately. Check workspace member
  registration, build inclusion, and documented plans first.
- Do **not** merge duplicate versions just because they differ. Analyze why the
  resolution produced multiple versions and what merging would break.
- Do **not** remove a dependency just because an import was not found. Check
  dynamic usage, feature-gated code, build scripts, and tooling conventions.

### No Silent Mutation

- Classify mutation risk **before** running any command that could change the
  manifest, lockfile, or source tree.
- Compare the working tree (e.g., `git status --porcelain` and `git diff
  --stat`) before and after the audit.
- If the working tree changed during the audit, report it as
  `MUTATION_DETECTED` (Section 26) and identify the command that caused it.

---

## 3. When to Use

Use this skill when the task involves any of:

- **Rust workspace audit**, crate graph, workspace inheritance, feature
  propagation, internal cycles.
- **Node.js monorepo audit**, package graph, workspace protocol, hoisting,
  peer dependency resolution.
- **Pre-upgrade review**, assess the impact of upgrading one or more
  dependencies before committing to it.
- **Dependency PR review**, evaluate a pull request that changes manifests or
  lockfiles.
- **Slow build analysis**, identify which dependencies dominate compile or
  install time.
- **Binary/bundle size analysis**, identify which dependencies dominate the
  shipped artifact.
- **Duplicate investigation**, why multiple versions of a package exist and
  whether they can be unified.
- **Circular dependency investigation**, where cycles exist and how to break
  them.
- **Unused dependency cleanup planning**, which declared dependencies are
  unused and safe to remove.
- **Security/license review**, advisory status, reachability, license
  compatibility, supply-chain integrity.
- **Workspace architecture review**, layering, fan-in/fan-out, dependency
  direction, shared package bloat.
- **Feature flag review**, whether heavy dependencies are properly gated and
  whether default features are minimal.
- **Large lockfile change review**, unexpected churn, registry changes, or
  source changes in a lockfile diff.
- **Package manager migration analysis**, what changes when switching between
  npm/pnpm/yarn/bun or Cargo versions.
- **Pre-release supply-chain review**, provenance, checksums, pinned
  revisions, and install scripts before shipping.

---

## 4. When Not to Use

Do **not** use this skill for:

- **Package usage questions**, "how do I use library X?" (use documentation
  instead).
- **API explanations**, "what does function Y do?" (use source/docs instead).
- **Source-only code review**, reviewing logic without any dependency
  dimension.
- **Dependency-unrelated bug fixes**, bugs with no manifest/lockfile/graph
  component.
- **Install command requests**, "install package Z for me" (that is
  remediation, not audit; only proceed if the user explicitly requests it).
- **Abstract comparisons**, comparing tools or libraries without an actual
  repository to inspect.

---

## 5. Supported Ecosystems

### Rust (detailed support)

**Files inspected:**
- `Cargo.toml` (package and workspace manifests)
- Workspace root `Cargo.toml` (`[workspace]`, `[workspace.dependencies]`,
  `[workspace.package]`, `[workspace.lints]`)
- `Cargo.lock`
- `.cargo/config.toml` (registries, source replacement, build config)
- `rust-toolchain` / `rust-toolchain.toml`
- `cargo-deny.toml` / `.cargo-deny.toml` (if present)
- `cargo-audit` config (if present)
- Workspace inheritance: `workspace = true` in member manifests
- `[patch]` and `[replace]` sections

**Dependency kinds:**
- `dependencies` (runtime)
- `dev-dependencies` (tests, examples, benches)
- `build-dependencies` (build scripts)
- Target-specific (`[target.'cfg(...)'.dependencies]`)
- Optional dependencies (`optional = true`)
- Workspace dependencies (`workspace = true`)
- Git dependencies (`git = ...`, `branch`/`tag`/`rev`)
- Path dependencies (`path = ...`)
- Renamed dependencies (`foo = { package = "bar", ... }`)
- Feature-induced dependencies (activated via `[features]`)
- Proc-macro dependencies (`proc-macro = true` crates)

### Node.js (detailed support)

**Files inspected:**
- `package.json` (root and workspace packages)
- `package-lock.json` (npm)
- `pnpm-lock.yaml` (pnpm)
- `yarn.lock` (yarn classic/berry)
- `bun.lock` / `bun.lockb` (bun)
- `pnpm-workspace.yaml` (pnpm workspaces)
- `turbo.json` (Turborepo)
- `nx.json` (Nx)
- Package manager config: `.npmrc`, `.yarnrc.yml`, `packageManager` field
- `overrides` (npm), `resolutions` (yarn), `pnpm.overrides` (pnpm)
- `patch-package` patches (`patches/` directory, `postinstall` hook)
- Workspace catalog (pnpm `catalog:` protocol)

**Dependency kinds:**
- `dependencies` (runtime)
- `devDependencies` (build/test/tooling)
- `peerDependencies` (host-provided)
- `peerDependenciesMeta` (optional peers)
- `optionalDependencies` (best-effort install)
- `bundledDependencies` (shipped inside the package)
- Workspace packages (`workspace:*`, `workspace:^`, `catalog:`)
- Aliases (`"foo": "npm:bar@1.0.0"`)
- Transitive dependencies (lockfile-only)
- Platform-specific packages (`@platform/...`, `os`/`cpu` fields)
- Native addons (node-gyp, N-API, prebuilds)
- CLI-only tools (binaries invoked from scripts)
- Build plugins (babel/webpack/vite/rollup plugins, loaders)

### Other Ecosystems (honest partial support)

Python, Go, Java, Kotlin, .NET, Dart, C/C++ are **not** claimed as fully
supported. Handle them with one of:

1. **Supported ecosystem adapter**, if a generic adapter exists for the
   ecosystem (e.g., `pip`/`uv` for Python, `go mod graph` for Go), use it and
   mark the ecosystem as `PARTIAL SUPPORT`.
2. **Generic manifest/lockfile review**, inspect the manifest and lockfile
   manually using the same evidence rules (direct/transitive, runtime/dev,
   duplicates, security, licenses).
3. **PARTIAL SUPPORT**, record in the report that the ecosystem is not fully
   covered and that some checks (e.g., feature-graph analysis) were skipped.
4. **MANUAL REVIEW REQUIRED**, flag findings in unsupported ecosystems as
   needing human verification before any action.

---

## 6. Project and Workspace Discovery

Do **not** assume specific paths (e.g., do not assume a `native/` subdirectory
or a particular manifest location). Search from the repository root.

Check, in order:

1. **Repo root**, where `.git/` lives; the base for all relative paths.
2. **Branch/HEAD**, current branch and commit; needed for baseline comparison.
3. **Workspace/monorepo root**, the directory containing the workspace
   manifest (`Cargo.toml` with `[workspace]`, `pnpm-workspace.yaml`, root
   `package.json` with `workspaces`, `turbo.json`, `nx.json`).
4. **Package manager and version**, `packageManager` field, `.npmrc`,
   `.yarnrc.yml`, `corepack` metadata; for Rust, the Cargo version.
5. **Runtime/toolchain version**, `rust-toolchain*`, `.nvmrc`, `.node-version`,
   `engines` field.
6. **Manifest list**, every `Cargo.toml` / `package.json` in scope.
7. **Lockfile list**, `Cargo.lock`, `package-lock.json`, `pnpm-lock.yaml`,
   `yarn.lock`, `bun.lock*`; note which exist and which are missing.
8. **Workspace members**, declared members, glob patterns, and the actual
   directories they resolve to.
9. **Excluded members**, `exclude` lists in workspace manifests.
10. **Nested workspaces**, workspaces inside workspaces; note them and decide
    whether they are in scope.
11. **Package/crate name**, the name field of each package.
12. **Private/public**, `private: true` (npm) or publish settings
    (`publish = false` in Cargo).
13. **Publish settings**, `publish` field, `files` field, `publishConfig`.
14. **Application/library/tool distinction**, is the package an app, a
    library, or a CLI tool? This changes which dependency kinds matter.

---

## 7. Audit Scope

Define the scope explicitly before running checks:

```yaml
repository: <repo name>
root: <repo root path>
ecosystems:
  - rust        # or node, python, go, ...
workspaces:
  - <workspace name or path>
manifests:
  - <path to each manifest in scope>
lockfiles:
  - <path to each lockfile in scope>
base_revision: <commit or tag, or null>
current_revision: <commit or tag, or null>
include:
  - <paths or packages explicitly in scope>
exclude:
  - <paths or packages explicitly out of scope>
network_access: <true | false | unknown>
mutation_allowed: <false | true-only-if-user-requested>
scope_confidence: <HIGH | MEDIUM | LOW>
```

**Audit modes:**

| Mode | Scope |
|---|---|
| `FULL` | Entire repository, all ecosystems, all workspaces |
| `CHANGED` | Only dependencies affected between base and current revision |
| `PACKAGE` | A single package/crate and its dependency closure |
| `SECURITY` | Advisories, reachability, sources, integrity, licenses |
| `ARCHITECTURE` | Cycles, layering, fan-in/out, dependency direction |
| `COST` | Build/install/binary/bundle cost of dependencies |

**Default:** use the narrowest scope sufficient to answer the user's question.
A `PACKAGE` audit does not need to scan the whole monorepo; a `SECURITY` audit
of a library may not need `COST` analysis.

---

## 8. Baseline and Dependency Delta

### Base Revision Selection (priority order)

1. User-specified revision
2. PR base branch (when auditing a pull request)
3. Upstream target branch (when auditing a fork)
4. `origin/main`
5. `origin/master`
6. Local `main` / `master`
7. Current state only (no baseline; delta checks are skipped)

### CHANGED Mode Delta Record

For each dependency, record:

- **Added direct deps**, new entries in manifests
- **Removed direct deps**, deleted entries in manifests
- **Version changes**, before/after versions for direct and transitive deps
- **Source changes**, registry → git, git → path, registry → path, and reverse
- **Feature changes**, added/removed features, default feature changes
- **Optionality changes**, `optional = true` added/removed, `optionalDependencies`
  moved to `dependencies`
- **New/removed transitive packages**, packages that appear/disappear in the
  lockfile
- **License changes**, license field or license file changes across the delta
- **Advisory delta**, advisories added/removed between revisions
- **Lockfile churn**, total number of lockfile lines changed, and whether the
  churn is proportional to the manifest change or unexpected

---

## 9. Dependency Graph Model

Build a graph with the following fields. Record them per node and per edge so
findings can cite exact evidence.

### Node fields

- `name`, package/crate name
- `version`, resolved version
- `source`, registry, git, path, workspace, alias
- `direct` / `transitive`, declared in a manifest vs pulled in transitively
- `internal` / `external`, inside the workspace vs outside
- `runtime` / `dev` / `build` / `test`, which contexts use it
- `optional`, optional dependency
- `activated_features`, features enabled in the current resolution
- `target`, target-specific condition, if any
- `license`, SPDX identifier or unknown
- `advisory_status`, known advisories, if any
- `publish_status`, published, unpublished, private
- `native_requirements`, native libraries, compilers, or system deps

### Edge fields

- `depender`, the package that declares the edge
- `dependency`, the package being depended on
- `kind`, runtime, dev, build, peer, optional, target-specific
- `version_requirement`, the declared range/requirement
- `activated_feature`, feature that activates this edge, if any
- `optionality`, optional or required
- `target_condition`, cfg/target condition, if any
- `source_location`, manifest file and line
- `evidence`, the exact manifest snippet or tool output that proves the edge

### Separate graph views

Build and analyze these views independently:

- **Runtime graph**, only runtime dependencies
- **Dev/test graph**, dev-dependencies and test-only edges
- **Build graph**, build-dependencies and build scripts
- **Feature-expanded graph**, edges activated by each feature combination
- **Target-specific graph**, edges for each target condition
- **Internal workspace graph**, edges between workspace members only
- **External transitive graph**, external packages and their transitive
  closure

---

## 10. Tool Resolution

Resolve available tools per ecosystem. Record each tool's presence, version, and
whether it was run. **Do not auto-install missing tools.** If a tool is missing,
record it as `NOT_AVAILABLE` and fall back to manifest/lockfile inspection.

### Rust

| Tool | Purpose |
|---|---|
| `cargo metadata` | Full manifest graph, features, targets, workspace layout |
| `cargo tree` | Dependency tree with sources and features |
| `cargo tree -d` | Duplicate versions |
| `cargo tree -e features` | Feature-expanded graph |
| `cargo tree --invert` | Reverse graph (who depends on X) |
| `cargo-deny` | Licenses, advisories, sources, bans |
| `cargo-audit` | Advisory database check |
| `cargo-machete` | Unused dependency detection (fast, heuristic) |
| `cargo-udeps` | Unused dependency detection (needs nightly, slow) |
| `cargo-bloat` | Binary size contribution per crate |

### Node.js

| Tool | Purpose |
|---|---|
| `npm ls` / `pnpm why` / `yarn why` / `bun pm ls` | Dependency tree and why a package is present |
| `npm ls <pkg>` / `pnpm why <pkg>` | Reverse lookup for a specific package |
| Knip | Unused files, exports, and dependencies |
| depcheck | Unused and missing dependencies |
| Madge | Circular dependency detection in source imports |
| `nx graph` / `nx affected:graph` | Monorepo graph (Nx) |
| Turbo metadata (`turbo.json` + task graph) | Monorepo graph (Turborepo) |
| Bundle analyzer (webpack-bundle-analyzer, vite-bundle-visualizer, source-map-explorer) | Bundle size per package |
| License checker (license-checker, `pnpm licenses list`) | License inventory |
| Package manager audit (`npm audit`, `pnpm audit`, `yarn audit`, `bun audit`) | Advisory check |

### Fallback rule

If a tool is unavailable, use the lockfile and manifests directly: parse the
lockfile to reconstruct the graph, and search source for usage evidence. Mark
the finding's evidence source accordingly.

---

## 11. Circular Dependency Analysis

### Distinguish cycle kinds

- **Internal workspace runtime cycle**, workspace member A depends on B and B
  depends on A in runtime dependencies.
- **Dev/test-only cycle**, the cycle exists only through dev-dependencies.
- **Build-time cycle**, the cycle exists only through build-dependencies.
- **Source import cycle**, modules import each other, but the package graph
  has no cycle (a code-level issue, not a manifest issue).
- **Package manifest cycle**, the manifest graph has a cycle even if source
  imports do not.
- **Feature-induced cycle**, the cycle appears only when certain features are
  activated.
- **Type-only cycle**, the cycle exists only for types, not runtime values.
- **Tool-reported false positive**, the tool reports a cycle that does not
  exist in the actual graph (verify with `cargo tree` or `pnpm why`).

### Per-cycle record (YAML)

```yaml
- path: [A, B, A]
  kind: internal_workspace_runtime
  runtime_reachable: true
  severity: HIGH
  architectural_impact: "A and B cannot be built or reasoned about independently"
  evidence: "cargo tree output, manifest lines"
  recommended_boundary: "extract shared types into a leaf crate"
```

### Solutions (do not always recommend common-crate extraction)

- **Dependency inversion**, make the higher-level package depend on an
  interface owned by the lower-level package.
- **Interface/trait extraction**, extract the shared trait/interface into a
  leaf package.
- **Event/callback boundary**, replace direct calls with events or callbacks.
- **Shared data type separation**, move only the shared data types to a leaf
  package, not the whole logic.
- **Package ownership restructure**, move the cyclic code into one package.
- **Runtime edge removal**, convert a runtime edge to a dev or build edge.
- **Dev dependency move**, if the cycle is dev-only, move the edge to
  dev-dependencies.
- **Type-only import**, use type-only imports to break a type-only cycle.
- **Lazy loading**, defer the import to runtime (Node.js dynamic import,
  Rust `once_cell` + late binding).
- **Public API redesign**, change the public API so the cycle disappears.

---

## 12. Unused Dependency Analysis

### Usage states

| State | Meaning |
|---|---|
| `CONFIRMED_UNUSED` | No usage found in any context after all checks |
| `LIKELY_UNUSED` | No usage found in common paths, but some checks not run |
| `POSSIBLY_UNUSED` | Usage not found, but a plausible usage mechanism exists |
| `USED_INDIRECTLY` | Not imported directly, but required by another package |
| `TOOLING_ONLY` | Used only by build tooling, scripts, or config |
| `FEATURE_ONLY` | Used only when a non-default feature is active |
| `TARGET_ONLY` | Used only on a specific target/platform |
| `DYNAMIC_USAGE` | Used via dynamic import, reflection, or runtime lookup |
| `UNKNOWN` | Cannot determine usage with available evidence |

### Rust checks (in order)

1. Source `use` / path references
2. Fully qualified paths (`crate::`, `::pkg::`)
3. Proc/derive macro usage (`#[derive(...)]`, attribute macros)
4. `build.rs` usage (build-dependencies)
5. Examples, benches, and tests (dev-dependencies)
6. Feature-only code (`#[cfg(feature = "...")]`)
7. Target-specific code (`#[cfg(target_os = "...")]`)
8. Doc tests (`rustdoc` code blocks)
9. Renamed dependencies (imported under a different name)
10. Re-exports (`pub use`)
11. `links` key (native library linking)
12. Native build requirements (build-dependencies that only link system libs)

### Node.js checks (in order)

1. Static `import` statements
2. `require()` calls
3. Dynamic `import()` calls
4. Subpath imports (`pkg/subpath`)
5. CLI binary usage (in `scripts`, `bin` fields)
6. Config file references (`.babelrc`, `webpack.config.js`, `vite.config.ts`,
   `eslint.config.js`, `jest.config.js`, `tsconfig.json` `types`/`paths`)
7. Plugin conventions (babel plugins, webpack loaders, eslint plugins,
   prettier plugins, tailwind plugins)
8. Framework auto-discovery (Next.js, Nuxt, Remix, Expo auto-registered
   packages)
9. Type-only imports (`import type`, `@types/*` packages)
10. Test setup files (`setupFiles`, `jest.setup.ts`, vitest setup)
11. Bundler config (entry points, externals, aliases)
12. Code generators (prisma, graphql-codegen, openapi-generator)
13. `postinstall` / `preinstall` / `prepare` scripts
14. Loaders (ts-node, tsx, esbuild-register)
15. Peer dependencies (declared for consumers, not used directly)
16. Optional platform packages (`@rollup/rollup-linux-x64-gnu` style)

### Rules

- **Do not auto-confirm removal from tool results alone.** A tool like
  `cargo-machete` or depcheck can produce false positives (dynamic usage,
  feature-gated code, config-driven usage).
- For each candidate, record: evidence, false-positive risk, affected
  scripts/config, verification commands, lockfile impact, and rollback method.

---

## 13. Duplicate Version and Version Conflict

### Distinguish situations

- **Harmless semver coexistence**, two versions of a package coexist because
  the semver ranges genuinely differ; no runtime interaction.
- **Intentional major split**, the project deliberately uses two major
  versions (e.g., a plugin ecosystem requiring both).
- **Workspace policy violation**, the workspace mandates a single version and
  the lockfile violates it.
- **Type identity conflict**, two copies of a package with different types
  cause `instanceof`/type mismatch errors.
- **Peer dependency conflict**, a package requires a peer version that
  conflicts with the resolved version.
- **Duplicate native library**, two versions of a native addon or C library
  are linked into the same binary.
- **Security exposure**, the older duplicate carries a known advisory.
- **Binary/bundle duplication**, both copies end up in the shipped artifact.
- **Duplicated singleton/global**, two copies of a stateful package (e.g.,
  React, a logger) break singleton behavior.
- **Feature incompatibility**, two copies with different feature sets behave
  differently.
- **Ecosystem limitation**, the package manager cannot deduplicate this
  package (e.g., platform-specific packages, git deps).

### Do not merge just because versions differ

Analyze before recommending deduplication:

- Dependency paths (who pulls each version)
- Direct vs transitive status
- Semver ranges declared
- Resolution reason (why the resolver kept both)
- Affected packages
- Deduplication possibility (can the ranges be unified?)
- Upgrade blast radius (what else changes if unified)
- Breaking risk (major version jump, API changes)
- Size/security benefit (what is gained)

### Ecosystem-specific considerations

- **Rust:** consider feature unification (`cargo tree -e features` shows
  feature sets per version; unifying features can merge versions).
- **Node.js:** consider hoisting behavior and peer dependency resolution
  (`pnpm why`, `npm ls`); overrides/resolutions can force unification but
  change the whole tree.

---

## 14. Outdated and Upgrade Analysis

### Distinguish `latest version` vs `recommended version`

- **Latest version**, the newest published version, regardless of
  compatibility.
- **Recommended version**, the newest version that is compatible with the
  project's constraints (semver range, MSRV/Node engine, peer deps, breaking
  changes).

### Per-candidate record

- Current version
- Allowed range (from manifest)
- Latest compatible version
- Latest version
- Major/minor/patch distance
- Release date
- Maintenance status (actively maintained, in maintenance, deprecated)
- Runtime/toolchain requirement (MSRV, Node engine)
- Peer dependencies (Node.js)
- Breaking changes (changelog, migration guide)
- Migration guide availability
- License change (if the upgrade changes the license)
- Security impact (advisories fixed or introduced)
- Ownership/provenance change (maintainer transfer, new registry)
- Deprecation status
- Downstream compatibility (what else depends on this package)
- Test coverage (are there tests that would catch regressions)
- Rollback possibility (is the old version still installable)

### Rules

- Record network access status and query time for every external check.
- **Do not guess outdated status without external data access.** If the
  registry cannot be queried (offline, rate-limited, auth required), mark the
  check as `REMOTE_DATA_REQUIRED` and do not claim a version is outdated.

---

## 15. Security Audit

### Analyze per dependency

- Known vulnerabilities (advisory database)
- Severity (CVSS, advisory severity)
- Affected range
- Reachability (is the vulnerable code path reachable from production code?)
- Direct vs transitive
- Exploit prerequisites (auth, local access, specific input)
- Patched version
- Workaround (if no patched version exists)
- Yanked/revoked versions
- Unmaintained status
- Suspicious package (recent creation, typosquatting candidate)
- Typosquatting (name similar to a popular package)
- Dependency confusion (private package name colliding with a public one)
- Install scripts (postinstall/preinstall executing arbitrary code)
- Git dependency (floating branch/tag, unpinned commit)
- Path dependency (local code, no integrity check)
- Checksum/integrity (lockfile integrity fields)
- Registry source (official vs mirror vs unknown)
- Maintainer/ownership change
- Native code (compiled at install or build time)
- Network/download at build time
- Provenance/attestation (SLSA, npm provenance)

### Rules

- **Do not declare a dependency safe just because no advisory was found.**
  Absence of an advisory is not proof of safety; record the check as
  `REMOTE_DATA_REQUIRED` if the advisory database was not queried.
- **Do not run `audit fix` or auto-upgrade in default audit mode.** Report the
  fix as a recommendation; apply it only in remediation mode (Section 27).

---

## 16. Reachability and Exploitability

### Distinguish reachability states

- **Present**, the package is in the lockfile.
- **Compiled/bundled**, the package is compiled into the binary or bundled
  into the artifact.
- **Runtime reachable**, a vulnerable code path can execute in production.
- **Feature disabled**, the package is only active under a feature that is
  off in the current build.
- **Target excluded**, the package is target-specific and the current target
  does not include it.
- **Dev-only**, the package is only in dev-dependencies.
- **Build-only**, the package is only in build-dependencies.
- **Unreachable**, the package is present but no code path reaches it.
- **Reachability unknown**, cannot determine reachability with available
  evidence.

### Per-advisory record

- `PRESENT_AND_REACHABLE`, vulnerable and reachable; highest priority.
- `PRESENT_REACHABILITY_UNKNOWN`, vulnerable, reachability not determined;
  treat as needing investigation, not as safe.
- `PRESENT_BUT_NOT_IN_PRODUCTION_TARGET`, vulnerable but excluded from the
  production target (dev/build-only, feature off, target excluded).
- `NOT_PRESENT`, not in the current lockfile.

### Rule

- **Do not ignore an advisory just because reachability is unclear.** Mark it
  `PRESENT_REACHABILITY_UNKNOWN` and recommend verification.

---

## 17. License Audit

### Per-dependency record

- SPDX identifier
- Multiple/dual license
- Unknown license
- Missing license metadata
- License file presence
- Repo policy (project's allowed/forbidden licenses)
- Copyleft strength (permissive, weak copyleft, strong copyleft)
- Linking/distribution implication (static vs dynamic, distribution model)
- Attribution/notice requirements
- Source disclosure requirements
- License change across upgrade (did the license change between versions?)
- Incompatible combination (two dependencies whose licenses conflict)

### Rules

- **Do not make a final legal judgment.** License analysis is not legal advice.
- Mark policy violations explicitly and flag them as `LEGAL_REVIEW` when the
  license is unusual, ambiguous, or conflicts with project policy.

---

## 18. Lockfile and Supply-Chain Integrity

### Check

- **Manifest-lockfile sync**, does the lockfile match the manifests? (e.g.,
  `cargo metadata --locked`, `npm ci --dry-run`, `pnpm install --frozen-lockfile
  --dry-run` in a temp environment)
- **Missing lockfile for application**, an application without a lockfile
  cannot reproduce builds; flag it.
- **Library lockfile policy**, libraries may intentionally omit lockfiles;
  check the repo policy before flagging.
- **Mixed lockfiles**, multiple lockfile types in one repo (e.g., both
  `package-lock.json` and `pnpm-lock.yaml`); flag inconsistency.
- **Large churn**, a lockfile diff far larger than the manifest diff; flag for
  review.
- **Unexpected registry change**, a package's resolved registry changed
  between revisions.
- **Checksum changes**, integrity hashes changed without a version change.
- **Source changes**, registry → git/path changes in the lockfile.
- **Overrides/resolutions**, forced resolutions and their scope.
- **Patch sections**, `[patch]` (Cargo) and patch-package entries.
- **Git revisions**, pinned vs floating (`branch`/`tag` vs `rev`).
- **Path dependencies**, local path deps and their reproducibility.
- **Workspace protocol**, `workspace:*` / `catalog:` usage consistency.
- **Aliases**, `npm:` aliases and their targets.
- **Local tarballs**, `file:` tarball dependencies.
- **Vendored deps**, vendored directories and their sync state.
- **Optional platform packages**, platform-specific packages and their
  presence in the lockfile.
- **Postinstall/build scripts**, which packages run scripts at install/build
  time.
- **Package manager version mismatch**, lockfile format vs the package
  manager version in use.

### Rules

- **Do not auto-regenerate the lockfile.** Use dry-run or a temporary
  environment to verify sync.
- Report sync verification results with the exact command used and its exit
  code.

---

## 19. Architecture Dependency Audit

### Analyze

- **Cycles**, internal cycles and their reachability (see Section 11).
- **Forbidden layer edges**, edges that violate the repo's declared layering
  (e.g., UI → storage).
- **Dependency inversion violations**, high-level packages depending on
  low-level implementation details instead of abstractions.
- **High fan-in/fan-out**, packages that many others depend on (fan-in) or
  that depend on many others (fan-out).
- **God/core packages**, packages that everything depends on; assess whether
  the coupling is justified.
- **Unstable dependency direction**, stable packages depending on volatile
  ones.
- **Domain → infrastructure coupling**, domain logic depending directly on
  infrastructure.
- **UI → storage/network direct deps**, UI layers bypassing service layers.
- **Test utility production deps**, test utilities leaking into production
  dependency sets.
- **Deep/internal path imports**, importing deep internal paths instead of the
  public API.
- **Re-export chains**, long chains of re-exports that obscure the real
  dependency.
- **Duplicated abstractions**, the same abstraction implemented in multiple
  packages.
- **Shared package bloat**, a shared package that accumulates unrelated
  responsibilities.
- **Public API surface**, packages exposing more than they need.
- **Dependency centrality**, which packages are central to the graph and
  would be costly to change.
- **Change amplification**, how many packages rebuild/retest when one package
  changes.

### Rules

- Follow the repo's architecture policy if one exists (e.g.,
  `docs/architecture-*.md`, ADRs, `architecture-constraints.md`).
- Otherwise, report observations and tradeoffs, not absolute rules. Frame
  findings as "this coupling exists; here is the cost and the options", not
  "this is wrong".

---

## 20. Cost and Heavy Dependencies

### Do not judge heavy by name alone

A package with a large name or reputation may be cheap in context (tree-shaken,
feature-gated, already present). Measure or estimate:

- Clean build time
- Incremental build time
- Install time
- Package/binary/bundle size
- Transitive package count
- Native compiler requirement
- System library requirement
- Platform support
- Startup cost
- Memory usage
- Feature activation (is the heavy part behind a feature?)
- Tree-shaking (is the heavy part tree-shakeable?)
- Duplicate copies (is the heavy part duplicated?)
- Download size

### Evidence states

| State | Meaning |
|---|---|
| `MEASURED` | Directly measured (build timing, bundle size output) |
| `TOOL_ESTIMATED` | Estimated by a tool (cargo-bloat, bundle analyzer) |
| `GRAPH_INFERRED` | Inferred from the dependency graph (transitive count, feature graph) |
| `UNKNOWN` | No evidence available |

### Feature gate recommendation requirements

Before recommending a feature gate, verify:

- Selective usage is possible (the heavy part is separable)
- Default features are needed (would `default = []` break the package?)
- Feature combinations are tested
- The optional dep is not exposed in the public API (or the API is gated too)
- Feature unification side effects (Rust: unifying features across the
  workspace)
- Documentation burden (users must know to enable the feature)
- Build matrix increase (more feature combinations to test)
- `no-default-features` behavior (what breaks when defaults are off)

---

## 21. Missing and Empty Package Handling

### Docs-only packages (mentioned in docs but no manifest)

Investigate before recommending creation:

- **Stale docs**, the package was removed and docs were not updated.
- **Planned component**, the package is planned but not yet created.
- **Renamed package**, the package exists under a different name.
- **Deleted package**, the package was intentionally deleted.
- **Moved path**, the package moved to a different directory.
- **Feature branch artifact**, the package exists only on another branch.
- **Generation output**, the package is generated at build time.
- **Submodule missing**, the package lives in a submodule that is not
  checked out.
- **Checkout issue**, the package directory is missing due to a partial
  checkout.
- **Actually missing**, the package genuinely does not exist.

**Default recommendation is not immediate creation.** Report the investigation
result and let the user decide.

### Empty directories

Check:

- Workspace member registration (is the directory listed as a member?)
- Build inclusion (does the build reference it?)
- Scaffold status (is it an empty scaffold awaiting implementation?)
- Documented plan (does any doc describe the intended content?)
- Other branch usage (is it used on another branch?)
- Deletion impact on tooling (would removing it break scripts, globs, or
  tooling configs?)

---

## 22. Recommendation Classification

### Types

| Type | Meaning |
|---|---|
| `REMOVE` | Remove the dependency |
| `ADD` | Add a dependency |
| `UPGRADE` | Upgrade to a newer version |
| `DOWNGRADE` | Downgrade to an older version |
| `ALIGN` | Align versions across the workspace |
| `DEDUPLICATE` | Unify duplicate versions |
| `FEATURE_GATE` | Gate a heavy dependency behind a feature |
| `MOVE_DEPENDENCY_KIND` | Move between dependencies/dev/build/peer |
| `REPLACE` | Replace with an alternative package |
| `PIN` | Pin to an exact version or commit |
| `UNPIN` | Unpin a version or commit |
| `ISOLATE` | Isolate a package from the shared graph |
| `INVERT_DEPENDENCY` | Invert a dependency direction |
| `DOCUMENT` | Document a decision or constraint |
| `INVESTIGATE` | Gather more evidence before deciding |
| `ACCEPT_AS_IS` | Accept the current state with rationale |

### Per-recommendation record (YAML)

```yaml
- action: UPGRADE
  target: <package name>
  finding: <finding ID this addresses>
  evidence: <manifest/lockfile/tool output reference>
  expected_benefit: <security fix, size reduction, feature, ...>
  risk: <breaking change, peer conflict, ...>
  breaking_change: <true | false | unknown>
  required_changes: <manifest edit, source changes, lockfile regen, ...>
  validation: <commands to run after the change>
  rollback: <how to revert>
  confidence: <CONFIRMED | HIGH | MEDIUM | LOW | UNKNOWN>
```

---

## 23. Severity and Confidence

### Severity

| Severity | Meaning |
|---|---|
| `CRITICAL` | Exploitable/malicious package, production advisory, dependency confusion, deployment-blocking license |
| `HIGH` | Runtime cycle, reachable advisory, peer conflict, untrusted source, security-impacting upgrade |
| `MEDIUM` | Unnecessary heavy dependency, actionable duplicate, likely unused, moderate advisory, workspace policy violation |
| `LOW` | Low-impact outdated version, minor lockfile hygiene, low-risk optional improvement |
| `INFO` | Normal multi-version coexistence, intended tradeoff, future cleanup |

### Confidence

| Confidence | Meaning |
|---|---|
| `CONFIRMED` | Direct evidence (manifest, lockfile, tool output, source) |
| `HIGH` | Strong evidence, minor uncertainty |
| `MEDIUM` | Reasonable evidence, some assumptions |
| `LOW` | Weak evidence, mostly inference |
| `UNKNOWN` | No evidence available |

---

## 24. False-Positive Controls

Before reporting a finding as unused/removable/conflicting, verify each of the
following that could apply:

- **Dynamic use**, dynamic import, reflection, runtime lookup
- **Feature/target use**, feature-gated or target-specific code
- **Generated code**, code generated at build time that references the package
- **Config/plugin use**, config files, plugin conventions, framework
  auto-discovery
- **Build script**, build.rs, postinstall, preinstall, prepare
- **Proc macro**, derive/attribute macros (Rust)
- **Peer dep**, declared for consumers, not used directly (Node.js)
- **CLI binary**, invoked from scripts or bin fields
- **Test/benchmark/example use**, dev-dependencies used only in tests,
  benches, or examples
- **Framework convention**, framework-registered packages (Next.js, Nuxt,
  Expo, etc.)
- **Platform-specific use**, usage on a platform not inspected
- **Re-export**, re-exported through a public API
- **Hidden workspace consumer**, another workspace member that uses the
  package

If false-positive risk remains after these checks, use `VERIFY_BEFORE_CHANGE`
instead of an auto-recommendation: report the finding with the remaining
uncertainty and the verification steps needed.

---

## 25. Evidence Collection

### Per-command record

| Field | Meaning |
|---|---|
| Command | exact command run |
| cwd | working directory |
| shell | shell used |
| environment | relevant env vars (e.g., `CARGO_NET_OFFLINE`) |
| tool version | version of the tool |
| start/end time | timestamps |
| duration | elapsed time |
| exit code | numeric exit code |
| output summary | key output lines |
| report path | where full output is saved |
| mutation risk | none / low / high (could this change files?) |
| working-tree change | did the working tree change after this command? |

### Network-dependent checks

| State | Meaning |
|---|---|
| `COMPLETED` | External data retrieved successfully |
| `OFFLINE` | No network access |
| `RATE_LIMITED` | Rate limited by the remote service |
| `AUTH_REQUIRED` | Authentication required and not available |
| `SERVICE_ERROR` | Remote service error |
| `STALE_DATA` | Data retrieved but older than a freshness threshold |

---

## 26. Status Model

### Per check/finding

| Status | Meaning |
|---|---|
| `PASS` | Check passed |
| `FINDING` | Check produced a finding |
| `WARN` | Non-blocking concern |
| `NOT_APPLICABLE` | Check does not apply to this project |
| `NOT_RUN` | Check not run (no tool, no time) |
| `BLOCKED` | Check could not run (missing tool, missing data) |
| `TOOL_ERROR` | Tool failed |
| `TIMEOUT` | Check timed out |
| `REMOTE_DATA_REQUIRED` | Needs external data that is unavailable |
| `PARTIAL` | Check ran but covered only part of the scope |
| `MUTATION_DETECTED` | Working tree changed during the audit |

### Overall verdict

| Verdict | Meaning |
|---|---|
| `CLEAN` | No findings in the inspected scope |
| `CLEAN_WITH_OBSERVATIONS` | No action required, but observations recorded |
| `ACTION_REQUIRED` | Findings that need action |
| `HIGH_RISK` | Critical/high findings present |
| `INCONCLUSIVE` | Not enough evidence for a verdict |

### Rules

- `CLEAN` applies **only within the inspected scope**, never claim the whole
  repository is clean if only a package was audited.
- **Do not declare the project fully safe without external security and
  latest-version data.** If advisories or registry data were not queried, the
  verdict must reflect that limitation.

---

## 27. Remediation Mode

Remediation runs **only when the user explicitly requests dependency fixes**.
It is a separate phase after the audit report.

### Pre-remediation

- Report the target recommendations and get user confirmation
- Check direct and transitive impact of each change
- Analyze breaking changes (changelog, migration guide, semver)
- Verify compatibility with the toolchain/runtime (MSRV, Node engine)
- Estimate lockfile impact (how many packages change)
- Plan rollback (how to revert if the change breaks something)
- Plan verification (what to run after the change)

### Post-remediation

- Verify the manifest diff (only intended changes)
- Verify the lockfile diff (proportional to the manifest change)
- Rebuild the dependency graph and confirm the expected resolution
- Run build, type-check, and lint
- Run targeted tests for the affected packages
- Run tests for packages that depend on the affected packages
- Re-run security and license checks
- Verify binary/bundle/build cost changes (if cost was a goal)
- Verify working tree integrity (no unintended mutations)

### Approval requirements

Explicit user approval is required for:

- Major upgrades (breaking semver)
- Package replacements
- Package manager changes
- License changes
- Public API changes

---

## 28. Output Format

Structured markdown report:

```markdown
# Dependency Audit Report: <project>

## Verdict
<one sentence: CLEAN / CLEAN_WITH_OBSERVATIONS / ACTION_REQUIRED / HIGH_RISK / INCONCLUSIVE>

## Scope
- Repository: <name>
- Root: <path>
- Ecosystems: <rust, node, ...>
- Workspaces: <list>
- Mode: <FULL | CHANGED | PACKAGE | SECURITY | ARCHITECTURE | COST>
- Base revision: <commit/tag or none>
- Current revision: <commit/tag>
- Network access: <true | false | unknown>
- Confidence: <HIGH | MEDIUM | LOW>

## Environment
| Tool | Version | Status |
|---|---|---|
| <tool> | <version> | <available | NOT_AVAILABLE | TOOL_ERROR> |

## Dependency Summary
- Internal packages: <count>
- Direct dependencies: <count>
- Transitive dependencies: <count>
- Runtime / dev / build / optional: <counts>
- Git/path dependencies: <counts>

## Internal Graph
| Package | Runtime deps | Dev deps | Fan-in | Fan-out |
|---|---|---|---|---|
| <name> | <count> | <count> | <count> | <count> |

## Dependency Delta (CHANGED mode)
| Change | Package | Before | After | Impact |
|---|---|---|---|---|
| <added/removed/upgraded> | <name> | <version> | <version> | <impact> |

## Findings
| ID | Severity | Confidence | Category | Package | Finding |
|---|---|---|---|---|---|
| F-001 | HIGH | CONFIRMED | cycle | <name> | <summary> |

## Finding Details
### F-001
- Category: <cycle | unused | duplicate | outdated | security | license | integrity | architecture | cost | missing>
- Severity: <...>
- Confidence: <...>
- Affected: <package(s)>
- Path: <dependency path>
- Direct/transitive: <...>
- Runtime/dev/build: <...>
- Evidence: <manifest/lockfile/tool output reference>
- Impact: <what breaks or what is at risk>
- Reachability: <PRESENT_AND_REACHABLE | ...>
- False-positive risk: <...>
- Recommendation: <action type + summary>
- Breaking risk: <...>
- Validation: <commands to verify>
- Rollback: <how to revert>

## Cycles
| Cycle | Kind | Runtime reachable | Severity | Evidence |
|---|---|---|---|---|

## Duplicate Versions
| Package | Versions | Paths | Reason | Action |
|---|---|---|---|---|

## Unused Candidates
| Package | State | Evidence | False-positive risk | Action |
|---|---|---|---|---|

## Security and Supply Chain
<advisories, reachability, sources, integrity, install scripts, provenance>

## Licenses
| Package | License | Policy | Note |
|---|---|---|---|

## Cost and Portability
<measured/estimated costs, heavy dependencies, feature gates>

## Recommendations (priority order)
1. <action>: <target>, <benefit> (risk: <...>, confidence: <...>)

## Limitations
- Tools not run: <...>
- Remote data not queried: <...>
- Unsupported ecosystems: <...>
- Targets not analyzed: <...>
- Possible false positives: <...>

## Working Tree Integrity
<git status before/after; MUTATION_DETECTED if changed>

## Final Statement
<one of: CLEAN / CLEAN_WITH_OBSERVATIONS / ACTION_REQUIRED / HIGH_RISK / INCONCLUSIVE, with the one-sentence justification>
```

---

## 29. Compact Protocol

Follow this sequence for every audit:

1. **DETERMINE AUDIT MODE**, FULL / CHANGED / PACKAGE / SECURITY /
   ARCHITECTURE / COST; narrowest sufficient scope.
2. **DISCOVER REPOSITORY AND ECOSYSTEMS**, repo root, branch, workspace
   root, package manager, toolchain.
3. **RECORD MANIFESTS, LOCKFILES, AND TOOLCHAINS**, every manifest, lockfile,
   and tool version in scope.
4. **DEFINE BASELINE AND SCOPE**, base revision, include/exclude, network
   access, mutation policy.
5. **BUILD SEPARATE DEPENDENCY GRAPHS**, runtime, dev/test, build,
   feature-expanded, target-specific, internal, external transitive.
6. **ANALYZE DELTA**, added/removed/upgraded deps, source changes, feature
   changes, lockfile churn (CHANGED mode).
7. **CHECK CYCLES AND LAYERING**, cycles by kind and reachability; layer
   violations; fan-in/out.
8. **CHECK UNUSED WITH FALSE-POSITIVE CONTROLS**, usage states, dynamic
   usage, feature/target/config/plugin checks.
9. **ANALYZE DUPLICATES AND VERSION CONFLICTS**, coexistence vs conflict,
   resolution reason, dedup possibility.
10. **CHECK SECURITY, SOURCES, AND INTEGRITY**, advisories, reachability,
    sources, lockfile sync, checksums, install scripts.
11. **CHECK LICENSES**, SPDX, policy, copyleft, changes across upgrades.
12. **MEASURE OR ESTIMATE COST**, build/install/bundle size, transitive
    count, feature gates.
13. **CLASSIFY FINDINGS BY SEVERITY AND CONFIDENCE**, CRITICAL/HIGH/MEDIUM/
    LOW/INFO × CONFIRMED/HIGH/MEDIUM/LOW/UNKNOWN.
14. **VERIFY WORKING TREE WAS NOT MUTATED**, compare git status before/after.
15. **REPORT EVIDENCE, LIMITATIONS, AND RECOMMENDATIONS**, full report with
    evidence, limitations, and prioritized recommendations.

---

## 30. Zoo-Code Profile

Project-specific commands for the Zoo Code project live here as a subsection.
They are **not** prioritized over the generic logic above: the generic
protocol, evidence rules, and false-positive controls always apply first.

- Zoo Code is a Flutter + Rust project. Rust crates live under the native
  workspace; Dart packages under the Flutter app. Audit both sides with the
  generic Rust and Node.js (Dart uses pub, see below) procedures.
- Dart/Flutter dependencies (`pubspec.yaml`, `pubspec.lock`) are handled as
  `PARTIAL SUPPORT`: use `dart pub deps` / `flutter pub deps` for the graph,
  `dart pub outdated` for version status, and manual lockfile review for
  integrity. Mark findings as `MANUAL REVIEW REQUIRED` where the generic
  checks do not apply.
- The local CI precheck skill (`local-ci-precheck`) runs before pushes; a
  dependency audit can feed its checks but does not replace them.
- When auditing the Rust side, prefer `cargo metadata --locked` and
  `cargo tree -e features`; when auditing the Dart side, prefer
  `flutter pub deps --style=compact` and `dart pub outdated`.
- Report folders follow the session protocol: `docs/YYMMDD_NNNN_session_<slug>/`.

---

## 31. (Reserved)

This section is reserved for future ecosystem profiles. Do not add content
here without updating the section numbering of the sections that follow.

---

## 32. Checklist

Before delivering an audit report, confirm:

- [ ] Audit mode and scope recorded
- [ ] Repository, ecosystems, manifests, lockfiles, toolchains discovered
- [ ] Baseline defined (or explicitly "current state only")
- [ ] Separate graphs built (runtime/dev/build/feature/target/internal)
- [ ] Delta analyzed (CHANGED mode)
- [ ] Cycles checked with kind and reachability
- [ ] Unused deps checked with false-positive controls
- [ ] Duplicates analyzed (coexistence vs conflict)
- [ ] Security checked (advisories, reachability, sources, integrity)
- [ ] Licenses checked (SPDX, policy, changes)
- [ ] Cost measured or estimated with evidence states
- [ ] Findings classified by severity and confidence
- [ ] Working tree verified unmutated
- [ ] Limitations recorded (unrun tools, unqueried remote data, unsupported
      ecosystems, possible false positives)
- [ ] Recommendations include evidence, risk, validation, and rollback
- [ ] External data access limitations marked clearly
- [ ] No dependency was added, removed, upgraded, or regenerated without
      explicit user request
