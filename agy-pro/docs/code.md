# `agy pro code`: the zero-MCP multi-model coding door

`agy pro code` is one command for a bounded coding change worked by more than one
model: native Antigravity seats plan and review (free, on the subscription quota),
an external provider seat writes the scoped files (metered API), and the engine
snapshots the tree before the first write, runs the verification itself, allows a
bounded repair, and can put the tree back byte-for-byte afterwards.

There is no MCP server in this path and no second write capability. The write
phase composes the shipped partition mode of [`agy pro fanout`](fanout.md)
(`--units`, per-unit `"write": true`), so the only child processes the door spawns
are that engine and the verification command the engine chose. The three packages
behind the verb:

| Layer | Package | What it owns |
|---|---|---|
| Primitives | `internal/codesnap` | copy-based snapshot, tree diff, archive-move rollback (never deletes, never runs git) |
| Orchestration | `internal/codework` | plan synthesis, engine-owned verification, bounded repair, phase ordering, the run report |
| Verb | `cmd/agy/pro_code.go` | flag parsing, governance defaults, stdout discipline, exit codes |

---

## 1. What the door is

- **Plan (free).** `agy pro code plan` runs the `plan-audit` fanout task over the
  request text with native seats and records the run. It spends nothing when the
  seat list is native-only, which the default line-up always is.
- **Write (metered).** `agy pro code write` executes a partition plan: snapshot ->
  one `agy pro fanout --units` run -> verification -> up to `--max-repairs` repair
  runs -> diff. A write unit is worked by exactly one **provider** seat: a native
  seat builds no scoped tool runner, so it cannot be bounded to a unit scope
  (`internal/fanout/engine.go:881-889`). The verb refuses such a `--writer` at
  usage time, before anything runs.
- **Review (free by default).** `agy pro code review` hands the change report of a
  completed write run to the `review` task seats. Native-only by default.
- **Rollback (never asks).** `agy pro code rollback` restores the tree from the
  run's snapshot manifest and archives every file the run created. It is manual,
  idempotent, and never deletes.
- **Run (the composed door).** `agy pro code run` chains plan -> write -> review in
  one run directory and prints one report. Without `--scope` it stops after the
  plan phase and tells the operator which scope the plan derived, exit 0.

The run directory (`reports/code/<ts>` by default, UTC `20060102-150405`, the same
layout the fanout engine uses one level over) holds `phases.log` (one line per
phase: `phase=<name> status=<status> artifacts=<a,b>`), `report.txt` (the change
report a review reads, written once the tree has been touched), a subdirectory per
phase (`plan/`, `write/`, `review/`), `verify/attempt-<n>-<cmd>.log` per
verification attempt, `repair-<n>/` per repair run, the snapshot (`manifest.json`
and `snapshot/`), and `rollback.json` after a rollback. Each phase's own fanout
run keeps its engine artifacts (`summary.json`, `reconciliation.md`,
`progress.log`, `seat-*.md`) inside that phase subdirectory.

---

## 2. Command surface

Everything below is the shipped usage text, printed by `agy pro code --help`,
`agy pro code help`, a bare `agy pro code`, an unknown subcommand and an unknown
flag (the last three write it to stderr and exit 2):

```text
Usage: agy pro code <subcommand> [options]

  plan      audit the request with the native seats (free) and record the run
  write     execute a partition plan: snapshot -> write run -> verify -> repairs -> diff
  review    review the change report of a completed write run
  rollback  restore the tree from the run's snapshot manifest (never asks, never deletes)
  run       plan -> write -> review in one run directory

Common options:
  --root DIR        workspace the seats and the verifier run inside (default ".")
  --out DIR         run directory (default reports/code/<ts>)
  --timeout SEC     fanout wall clock in seconds (default: the engine's own)
  --json            print the run report as JSON on stdout
  --dry-run         print the exact resolved commands and exit 0; nothing runs

plan:
  --ask TEXT | --ask-file PATH   the request text (- reads stdin)          (required)
  --seats A,B                    plan seats (default the native line-up below)
  --yes                          consent to the metered seats without asking

write:
  --plan PATH                    execute an existing partition plan file
  --scope GLOB                   workspace path a write unit may touch (repeatable)
  --writer SEAT                  the write seat: provider/model (native is refused)
  --verify CMD                   a verification command line (repeatable; all must pass;
                                 omit = compile-only go build+go vet detection)
  --max-repairs N                repair runs after a failed verification (default 1)
  --yes                          consent to the metered seats without asking
  Composing form: --ask/--ask-file + --scope + --writer (a one-unit plan is authored).

review:
  --run DIR                      the run directory holding report.txt       (required)
  --seats A,B                    review seats (default the native line-up below)
  --yes                          consent to the metered seats without asking

rollback:
  --run DIR                      the run directory holding manifest.json + snapshot/ (required)
  --yes                          accepted for uniformity; rollback never asks

review/rollback:
  --run DIR is the single source of truth: it is both the directory read (report.txt
  / manifest.json + snapshot/) and the directory written to. --out is accepted only
  when it names that same directory; a divergent --out is a usage error (exit 2).

run:
  plan -> write -> review. Without --scope it stops after the plan phase and prints
  the derived-scope instruction (exit 0). --plan implies --skip-plan.

Default native seats (plan and review): native:gemini-3.8-flash-high,native:claude-sonnet-5-5-high

Cost and consent: a provider seat in a seat list, or any write run, is a metered API
call with no token/cost cap. A terminal asks y/N; a reader that cannot answer is
refused unless --yes is given. Native-only phases never ask.
Exit codes: 0 ok (or a plan-only stop; a degraded phase still exits 0), 1 run failure
(a failed verification is one; the report names the rollback command), 2 usage error,
refused consent, or a refused plan/scope (the invocation has to change).
Artifacts: <out>/phases.log (codework trail), <out>/report.txt (the change report a
review reads), the engine's summary.json / seat-*.md / progress.log in each phase run
directory, and <out>/rollback.json after a rollback.
```

`agy pro code` has exactly five subcommands: `plan`, `write`, `review`,
`rollback`, and `run`.

Every flag is registered on the subcommand's own fail-closed `flag.FlagSet`, so
an unknown flag, an unparsable value or a stray positional operand is exit 2
before anything runs. The registrations, per subcommand:

| Flag | plan | write | review | rollback | run |
|---|---|---|---|---|---|
| `--ask TEXT` | yes | yes | - | - | yes |
| `--ask-file PATH` | yes | yes | - | - | yes |
| `--seats A,B` | yes | - | yes | - | yes |
| `--plan PATH` | - | yes | - | - | yes |
| `--scope GLOB` (repeatable) | - | yes | - | - | yes |
| `--writer SEAT` | - | yes | - | - | yes |
| `--verify CMD` (repeatable) | - | yes | - | - | yes |
| `--max-repairs N` | - | yes | - | - | yes |
| `--skip-plan` | - | - | - | - | yes |
| `--run DIR` | - | - | yes | yes | - |
| `--yes` | yes | yes | yes | accepted, never used | yes |
| `--root DIR` | yes | yes | yes | yes | yes |
| `--out DIR` | yes | yes | yes | yes | yes |
| `--timeout SEC` | yes | yes | yes | yes | yes |
| `--json` | yes | yes | yes | yes | yes |
| `--dry-run` | yes | yes | yes | yes | yes |

`--run` is the single source of truth for `review` and `rollback`: it is both the
input the subcommand reads (`report.txt`, or `manifest.json` + `snapshot/`) and the
directory its artifacts are written into. `--out` used to be an alias that silently
overrode it; now `--out` is accepted only when it names the same directory, and a
divergent pair is a usage error (exit 2) naming both paths. Restoring or reviewing
the wrong run is not a mistake an operator can see in the report afterwards.

Refusals that land before anything runs (every one is exit 2):

- `--ask` and `--ask-file` together, or neither where the request text is
  required.
- `--plan` together with `--ask`/`--ask-file` (the plan already carries the units).
- negative `--timeout` or negative `--max-repairs`.
- a `--verify` operand that is blank, or that does not split into an argv
  (unbalanced quote). Omitting the flag is how detection is requested.
- a `--writer` that is not exactly one provider seat (a native spec, or a seat
  list). A bare token is accepted as a provider spec, so a model id the registry
  does not resolve is caught by the engine at run time (exit 1), not here.
- `run --skip-plan` with no `--scope` (nothing left to derive a scope from).
- `review --run DIR` pointing at a directory without `report.txt`, and
  `--out DIR` on `review`/`rollback` that names a different directory than
  `--run`. (A `rollback --run DIR` with no usable manifest is a RUN failure, exit
  1: the invocation is right, the artifact is missing - see §3.)
- a refused plan or scope on the write path: `--plan` bytes (or a `--scope`
  entry) naming a governance or credential path, a root-wide scope, a scope that
  could claim the run directory's own subtree (including a case-variant spelling
  of it on a host whose filesystem folds case), a component carrying Win32 stream
  syntax (its own named class, §4.2), or a path component that is not in
  canonical Win32 form or has the 8.3 short-name shape is exit 2, because the
  invocation has to change and nothing started (§4.2, §4.4, §4.9). This refusal
  is raised when the plan is built, so on the composed `run` route it lands
  before the consent prompt.

`--dry-run` prints the exact composed vectors (the fanout argv, each verifier, and
the rollback command) to stderr and the run report to stdout, and exits 0 without
spawning anything: it runs **before** the consent gate, so a metered run can be
inspected without `--yes`. It does create the run directory with one file,
`phases.log`, carrying the `dry-run` status line.

---

## 3. Exit codes

| Code | Meaning | Example |
|---|---|---|
| 0 | ok, or a plan-only stop, or a `degraded` phase | a native-only plan phase; a `run` that stopped after the plan because no `--scope` was given; a write whose verification passed; a completed rollback (also a repeated one); a phase where some seats failed but the rest delivered and the reconciliation exists |
| 1 | run failure | a refused plan (`BuildPlan` rejected the request), a non-zero `agy pro fanout` exit, a write phase that failed (including a post-write diff or repair-plan failure - the report then says `failed` and still names the rollback command), an ultimately failed verification, a verification command the run-total budget never let start (`skipped=verify-skipped-budget`, which fails closed instead of passing on a partial verification), a phase where EVERY seat failed, a rollback with no usable manifest |
| 2 | usage error, refused consent, or a refused plan/scope | any flag/operand error above, an unknown subcommand, a consent refusal, and every pre-run refusal: a `--plan`/`--scope` naming a governance or credential path, a root-wide scope, a scope that could claim the run directory's own subtree (in any case spelling the host's filesystem folds - a `RePoRtS/**` scope meets a `reports/code/<ts>` run directory before the consent prompt, not after it) or any PREVIOUS run's ledger under the root, a scope component carrying Win32 stream syntax, or a path component that is not in canonical Win32 form or has the 8.3 short-name shape (§4.2, §4.4) |

Every refusal that says "the invocation has to change" is exit 2 no matter which
subcommand raised it, and the scope refusals fire BEFORE the metered run is
authorized. One function backs every route (`codework.RefuseScopeEntries`): the
verb calls it on `write` before the plan is built and before the consent prompt,
and on `run` before the plan phase starts (that phase may already snapshot the
scope), so a governance, credential, root-wide or run-directory-covering scope
never reaches an operator as a y/N question. It carries the same
`ErrRefusedPlanScope` the `--plan` route raises inside the write phase, so the
three routes cannot disagree and all of them return the usage code the usage text
above promises.

The run-directory arm is a behavioral copy of nothing: since round 5's FIX-F2 the
gate CALLS the snapshot's own rule. `codesnap` owns it and exports it
(`LedgerScopeClaims`, backed by the exported `FoldLiteralRuns` scanner), and
`codework` imports that package, so the pre-consent refusal and the snapshot
refusal are one implementation instead of two - a copy is what drifted twice in
round 5 (a shell-style `[!]` negation the matcher does not have, and a case probe of
the process temp directory instead of the operation's root). The rule itself is a
union - a lexical match on the scope entry as authored, plus a match with only the
entry's literal letters case-folded when the root's filesystem folds case, plus a
crude whole-pattern fold as a backstop, with character classes never folded
(§4.4). A case-variant scope
aimed at the run directory is therefore refused here, as exit 2 before the echo,
instead of surviving the consent prompt and failing as exit 1 afterwards. The
CROSS-RUN arm rides the same timing (FIX-F4, finding Q1): a scope that could claim
any PREVIOUS run's ledger under the root is refused here too, through
`codesnap.PriorLedgerClaim`, so the operator is never asked to consent to a run whose
snapshot must then fail. Both scope ROUTES carry it (FIX-F5, finding W5): the
operator's `--scope` entries and the units of a caller-supplied `--plan` (over their
union, one walk), so a plan whose unit scope names a prior run directory is refused
before the prompt with the same usage exit 2 instead of failing later as a snapshot
error. Two
pins hold the layers together: the snapshot side's
`TestLedgerScopeClaimsAgreeBetweenTheLayers` (which judges the exported predicate,
`RefuseScopeEntries` and `codesnap.Take` on every shared case) and the strict
`TestLedgerMirrorParityWithTheSnapshotAuthority`, which drives the same rows through
the write phase's gate and the snapshot, and fails on any disagreement in either
direction. `TestPriorLedgerScopeRefusedBeforeConsent` is the cross-run counterpart:
it asserts both doors on the same scope and proves the arm is inert on a tree with no
run ledger.

Two statuses are results, not failures: `unverified` (no verifier was given or
detected, so no verdict is claimed) exits 0, and a `consent-required` refusal
exits 2 with nothing run.

`degraded` is the third: the fanout call exited 0, the phase's artifacts exist, but
at least one seat never delivered (rate limit, timeout, an empty answer). The phase
row says `degraded`, the run-level status line carries the `(degraded)` token and
the seats line is printed above the status with the failed count, so `ok` never
stands in for a review nobody gave. The door keeps exit 0 because there is a
partial result to read; add `--json` and read `seat_status` / `seats_failed` when a
script has to gate on it.

**A failed verification is reported, not repaired automatically.** The run ends
with status `verify-failed` and exit 1, the tree keeps the writes it received, and
the report names the exact undo command:

```bash
agy pro code rollback --run reports/code/<ts>
```

The hint names the `--run` directory exactly as the operator typed it (never `.`),
and quotes it when the path contains whitespace, so the command it prints still
splits into one operand in the shell the run was started in.

`agy pro code run` stops before the review phase when the write or the
verification failed, so a change the verifier rejected is never handed to a
paid review. A manual `agy pro code review --run <dir>` over a failed run is
allowed: the diff is real, and an operator may want the seats' opinion on it.

The report block also answers the two questions a diff cannot: **how much of the
tree the baseline withheld** (`withheld: N secret-class file(s) not snapshotted`,
printed only when the snapshot withheld something - §4.4) and **what a native
seat changed** (`drift:` / `governance-drift:` rows, §4.7).

---

## 4. Safety model

### 4.1 Provider-only writers

A write unit is worked by exactly one provider seat. Native seats cannot be write
units: they run real `agy` at the workspace root, hold their own tool layer, and
build no scoped `ToolRunner`, so the engine refuses a native partition seat even
for a `"write": true` unit (`internal/fanout/engine.go:881-889`). The verb turns
that into a usage error naming the reason, before a snapshot is taken.

### 4.2 Per-unit dual-view scope

The write confinement is the partition engine's existing rule: a target is in
scope only when **either** its lexical root-relative path **or** its
symlink-resolved real path is inside the unit's scope globs. Both predicates are
the read filter's own exported authorities, so the write filter cannot drift from
the read filter: `agenttools.PathInScope` / `agenttools.ResolvedRelPath`
(`internal/agenttools/guard.go:231`, `:313`), used by the claim walk in
`internal/fanout/merge.go:336`. A refusal is error-shaped and model-visible: the
seat reads it as a tool result and continues, and the refusal text is redacted
through the same machinery as read refusals.

The same dual view decides what a snapshot claims, so the file a seat could write
and the file rollback can restore are the same set.

Every path a write tool or the scope matcher OPENS is judged on the name Windows
will open, not the bytes the seat typed. `internal/agenttools` applies a Win32
canonical-form layer before the denylist, the scope matcher and the 8.3 checks
see a path (`componentHygieneFailure` and `canonicalWriteRefusal` in `guard.go`),
while an authored scope PATTERN stays byte-identical (see the end of this
section):

- a component that ends in a dot or a space is refused (`src/notes.md.` would
  open `src/notes.md` under a name nothing compared), as is one that
  canonicalizes to nothing (`...`) and an exact `.` / `..` traversal segment;
- a component whose base name is a reserved DOS device (`nul`, `con.txt`,
  `aux.go`, `com1.log`) is refused: it addresses a device instead of a file;
- a component with the 8.3 short-name shape (`PROVID~1.JSON`) is refused on
  every component, so an intermediate directory created through an alias
  (`.AGY-PR~1/**`) is refused as well, and the write target is expanded through
  its long name (`GetLongPathNameW`) so an alias that resolves to a governance
  or credential name, to VCS metadata, or outside the root is refused even when
  the alias itself reads harmless;
- on Windows a component that contains `:` is refused (`HasWin32StreamSyntax`):
  `AGENTS.md:stream` and `AGENTS.md::$DATA` name a stream of a file (or of a
  device), never a file of that name, so no containment rule, denylist entry or
  scope pattern can bound them by comparing the written string; a colon is an
  ordinary filename character on POSIX, which is why the refusal is
  Windows-gated. The long-name expansion cannot help here either: it answers 0
  for a colon path, so a colon is refused explicitly rather than resolved;
- the governance, VCS, secret-name comparisons use one name fold,
  `win32NameFold`: an uppercase-then-lowercase approximation of the NTFS
  `$UpCase` table (the uppercase step first, because `strings.ToLower` leaves `ſ`
  alone while `$UpCase` is an uppercase table). The fold is exported
  (`Win32NameFold`) for guards outside this package, so the write guard, the
  snapshot ledger and the plan builder cannot disagree about which spellings are
  one NAME-CLASS name. What it decides is a name CLASS - governed, credential,
  VCS metadata, DOS device - and never whether two paths are the same FILE: file
  identity is the exact path (§4.4), because a name fold merges spellings the
  filesystem keeps apart.
  It folds MORE than the volumes this workspace runs on, deliberately. Measured
  on the two volumes this tree sits on, ASCII case collides (`A.txt` and `a.txt`
  are one file) while `ſ`/`s`, `ı`/`i` and U+212A/`k` are distinct files, so
  `ſtate.json` does NOT open `state.json` there. The fold refuses that spelling
  anyway, for two reasons: a spoof spelling of a governed name has no legitimate
  use, and `$UpCase` is a per-volume table (a volume formatted elsewhere can map
  a set this one does not). The cost is one line: a look-alike that is a distinct
  file is refused as if it were the governed name - the spoof-only over-refusal
  this class errs on. Its other limits are stated in the code: it folds FEWER
  than `$UpCase` in places (no expansion, so `ß` never becomes `SS`) and it does
  not fold superscript-digit look-alikes (`com¹`), which were measured not to
  collide with the ASCII forms.

The scope matcher canonicalizes its TARGET the same way
(`canonicalScopeTarget` in `guard.go`) while leaving the authored PATTERN
byte-identical, so a pattern keeps matching the names its author wrote; a target
spelled `src/.. /.. /outside/x.txt` is canonicalized the way Win32 canonicalizes
it (the decorated components vanish) and is then refused by the hygiene arm
rather than by a coincidence of `path.Match`.

The plan builder mirrors all of these rules when it validates `--scope` entries
(`internal/codework/plan.go`), so `rules./x`, `sub/nul.txt`, `aux.go` and
`src/..../x` are refused at plan time, before a snapshot exists. Two arms are
worth their own sentence, because an operator will meet both:

- **Win32 stream syntax is a named class at authoring time.** On Windows a
  `--scope` entry whose component contains `:` is refused with the class phrase
  `Windows stream syntax not allowed in scope entries` (exit 2, the refused entry
  named, §2/§4.9). A SCOPED write attempt at such a path is refused too, and two
  arms agree on that outcome: the engine's scope matcher puts a stream-carrying
  target out of scope on every arm (`PathInScope`), and the write guard's shape
  arm refuses the same component by name. Only the authoring refusal is worded by
  this door - the seat-side message belongs to the engine, so an operator reading
  a tool result should expect the refusal, not necessarily this class phrase.
- **The 8.3 shape arm refuses a narrow digit band, on purpose.** A component is
  refused when a `~` follows at least one character and is followed by ONE TO
  FIVE digits, either at the end of the component or immediately before a dot:
  `notes~2026.md`, `plan~12.txt` and `report~1` are refused, because that is the
  generated-alias shape and nothing in the name says whether the tail is a date
  or an alias. A tail of SIX OR MORE digits is not refused by this arm
  (`report~20261024.md` stays writable), and a component whose name STARTS with
  `~` is not refused either. The band is an owner decision on record (finding
  N9): accept the fail-closed side, because tightening the heuristic would
  weaken the 8.3 seal and the availability cost lands on a `~` filename this
  repository does not use. Nothing in this arm depends on the volume.

### 4.3 Governance and credential denylist

Independent of scope, two tool functions (`write_file`, `replace_in_file`) refuse
to touch the files that configure the agent system itself. The list lives in
`internal/agenttools/guard.go` and is component-exact and matched at any depth,
case-insensitively through the name fold described in §4.2 - an approximation of
the volume's `$UpCase` table, so a homoglyph spelling of a governed name is
refused like the plain one even where the two are distinct files:

- names: `agents.md`, `gemini.md`, `claude.md`, `providers.json`, `state.json`,
  `state-fanout.json`, `fanout-presets.json`, `integrations.json`, `settings.json`
  (`governanceWriteNames`, `guard.go:89`);
- directory components: `rules`, `.agy-pro`, `.gemini` (same map; this covers
  `profiles/tools/rules/` at any depth);
- sibling aliases of the provider registry: any `providers.json.<suffix>` except
  the documentation suffixes `md markdown txt rst adoc asciidoc html htm`
  (`governanceSiblingPrefix` `guard.go:109` + `governanceDocSuffixes`
  `guard.go:115`);
- the symlink-**resolved** directory chain is checked too, so a pre-existing link
  pointing into a governed directory cannot launder a clean-looking path.

The same predicate is exported and reused outside the write tools, so the write
guard and the snapshot ledger cannot disagree about what "governed" means:

- `agenttools.GovernanceWriteRefusal` is the governance and credential predicate
  over canonical components;
- `agenttools.VCSMetadataRefusal` is the sibling for VCS metadata (`.git/**` and
  the other protected names), which has no documentation carve-out;
- `agenttools.CanonicalPathComponentOK` is the canonical-form test, so a restore
  target that passes it is a name the host will open as written;
- `agenttools.ResolvedDirChainRefusal` is the **resolved directory chain** of a
  target that may not exist yet (root-relative, final component excluded), so a
  guard that resolves names itself can ask the same question the write tools ask:
  does the chain this path traverses end up inside a governed/credential
  directory or VCS metadata? It calls the write guard's own ancestor walk instead
  of copying it, and it answers "cannot resolve" with an error rather than a
  guess.

§4.4 is where that matters: a snapshot claim, a manifest row and a rollback or
archive target are each checked with these four predicates.

Reads and searches are unaffected: only the write tools consult this set. The
denylist is engine-wide, so it also hardens the legacy run-wide `--tools write`
path without changing that flag's behavior.

### 4.4 Snapshot manifest, secret withholding, no git

The snapshot is a byte copy: `manifest.json` (schema version, root, scopes, one
row per file with size, sha256, mode) plus `snapshot/<relpath>` under the run
directory. The manifest is the only source of truth a diff or a rollback may read:
no timestamp comparison, no git state, no "looks original" rescan.

Three comparisons over the names in that ledger are deliberately different forms,
and the difference is what round 4 changed:

- **FILE IDENTITY is the exact root-relative path.** Whether a walked path is a
  file the manifest recorded (or recorded as withheld) is decided by the path
  itself, never by a fold. The name fold merges spellings the filesystem keeps
  apart (`ſ`/`s`, `ı`/`i`), so an identity fold would hide a file the run created
  behind a recorded name: `src/prınce.txt` next to a recorded `src/prince.txt`
  would be absent from the diff AND skipped by the archive - a run-created file
  that survives its own rollback.
- **DIRECTORY PREFIXES fold ASCII case only,** and only when the root's own
  filesystem folds case. `--out <root>/REPORTS/run` against a scope `reports/**`
  is one directory on such a host (that tolerance is why the fold exists at all),
  while `reſults` never becomes `results` and so cannot smuggle a file past the
  run-directory exclusion.
- **The fold decision is a runtime probe of the root, not `GOOS`.** `policyFor`
  probes a directory INSIDE the operation's root - it makes a throw-away
  `.agy-casefold-probe-*` directory there, writes one empty `probe-Aa` file, and
  asks whether `probe-aA` in the same directory is the same file - so a
  case-insensitive APFS volume gets the tolerance NTFS gets and a case-sensitive
  volume never sees two directories merged. The probe answers about the directory
  whose entries the walk compares, and about no ancestor: flipping the whole root
  path instead would consult the mount's parent, which is how round 5's SEC R5-1
  switched the fold OFF on a case-insensitive directory below a case-sensitive one
  and let a case-variant scope reach the ledger. A probe that cannot answer (a root
  that does not exist or may not be written to, a filesystem that refuses the probe)
  folds, which is the refusing side for both uses, and the answer is cached per root
  so each root is probed at most once per process. **The ledger claim tests measure
  ONE MORE place**: the deepest existing ancestor of the run directory itself, and
  the claim folds when EITHER measurement folds (`ledgerPolicyFor`). That closes the
  sibling shape round 6 filed (SEC-review F-01): a byte-exact root whose ledger
  sits below an interior case-insensitive boundary - a bind-mounted casefold subtree,
  an ext4 casefold directory, an APFS volume mounted under the workspace - measured
  fold OFF from the root alone, so `reports/CODE/run` was accepted for a ledger at
  `reports/code/run`. Two probes cost nothing where the volume folds uniformly (the
  root's answer already folds, so the ancestor is never measured). The per-ledger
  measurement is GATED, not paid per directory: the walk asks the cheap claim test
  (under the fold-ON policy, a proven superset of every measured verdict) and the
  ledger fingerprint first, so a case-sensitive volume sees one probe per ledger
  CANDIDATE rather than one per walked directory. The walk
  EXCLUSION keeps the root-only policy on purpose: widening it could merge a
  run-created directory with the ledger's name and hide a created file from the
  archive, which is the round-4 escape the ASCII-only fold exists to prevent.
  One exception is deliberate and
  recorded: `sameRoot` - is this manifest's
  root the tree in front of me - keeps the `GOOS`-based fold, because it compares
  two arbitrary spellings of a root one of which may no longer exist, so a probe
  would answer exactly where the answer cannot be measured; a wrong ACCEPT there
  applies a manifest to the wrong tree, and the `GOOS` fold never errs that way
  on a Windows volume.

Six things bound what that ledger may hold, and each one refuses or withholds
instead of copying:

- **Secret-class files are withheld, never copied** (finding S5). A claim whose
  final component is in the class - prefixes `.env*`, `id_rsa*`; suffixes
  `*.env`, `*.pem`, `*.key`, `*.pfx`, `*.p12`, `*.kdbx`, `*.ppk`, matched on the
  canonical component through the same name fold §4.3 uses and covering a whole
  name that IS the suffix (`.pem`, `.key`) - is decided **inside the walk, before
  the claim is taken** (`walkScoped` in `internal/codesnap/snapshot.go`), so no
  copy loop ever reads its bytes. Both views are checked: a link `config.md` ->
  `.env` is withheld exactly like the dotenv. The row is recorded (`"skipped"`,
  reason `secret-class (<family>): withheld from the snapshot, no bytes
  copied`), the run report surfaces it as one line, `withheld: N secret-class
  file(s) not snapshotted`, plus the `withheld` rows in the JSON, and nothing
  downstream pretends coverage: `Diff` never reports a withheld path as
  `created`, and `Rollback` neither restores nor archives one (moving it would
  copy the same secret into the run directory by another route). A secret-class
  file the run CREATES after the snapshot is the one gap this leaves: it has no
  baseline row, so `Diff` stays silent and `rollback.json` names it in
  `skipped[]`.
  The class is a NAME filter, not a secret scanner: `.npmrc`, `*.crt`, a bare
  `secrets/` directory and a key pasted inside an ordinary source file are NOT
  withheld. Two over-refusals are deliberate and recorded: `.env.example` (a
  template is one edit away from a live dotenv) and `id_rsa.pub` (the family
  prefix wins).
- **A governance, credential or VCS claim fails the whole take.** A scope that
  reaches `AGENTS.md`, `rules/`, `providers.json`, `.agy-pro/**` or `.git/**`
  stops the snapshot with a named error (`ErrGovernanceRefused`) rather than
  holding a baseline the rollback would then be forbidden to restore. The
  refusal is a refusal, not a skip: a baseline with a hole in it is worse than no
  baseline.
- **An inode is not a name: a hardlink alias is withheld, never copied.** A file
  whose NAME is clean can still be the same bytes as a governed or secret file - a
  hardlink (`os.Link`, `mklink /H`, `ln`) has no alias view, so the symlink-resolving
  view answers with the benign name and every name-class arm passes while the walk
  reads `.env` or `AGENTS.md` into `snapshot/notes.txt`. The walk therefore collects
  the identity of every file it meets whose name IS denylisted (governance,
  credential, VCS metadata, secret class), and any claim whose inode matches one of
  them is withheld exactly like a secret: `skipped` row
  `hardlink alias of withheld path (<the governed path>): withheld from the
  snapshot, no bytes copied`, no byte read, and `Rollback` neither restores nor
  archives it (moving the entry would put the same inode inside the run directory by
  another route). The identity test is `os.SameFile`; the link count is only a fast
  gate, and Windows does not report one, so this host compares identities directly.
  The symlink spellings were always caught; this is the inode half. An alias whose
  target lives OUTSIDE `--root` has no denylisted name in the walk at all and is not
  withheld: that residue, its tier and the future Windows link-count arm are stated
  in §5.12.
- **The run directory is not claimable.** `<out>` normally lives inside
  `--root` (`reports/code/<ts>`), so a scope covering it could let a seat rewrite
  `manifest.json` and the snapshot copies in place, and the next rollback would
  restore forged bytes. A scope entry that could claim a path at or below the run
  directory refuses the whole take (`ErrLedgerRefused`), and `Rollback` checks the
  manifest's own scopes against the run directory before it touches the tree. The
  claim test is a UNION and both arms refuse: a lexical match on the entry as
  authored (so `reports/code/[A-z]ts-1` still covers `reports/code/_ts-1`, which
  is what Go's character-class syntax promises), plus a folded-LITERAL match when
  the root's volume folds case (so `RePoRtS/**` still meets a run directory
  spelled `reports/code/<ts>`). A character CLASS is never rewritten: folding
  `[A-z]` to `[a-z]` would drop `_` and every other byte between `Z` and `a`, and
  the refusal would become laxer than the runtime scope matcher instead of
  stricter. The folded arm is escape-aware and follows Go's own class grammar,
  byte for byte: `\[` and `\]` are the literals they are inside `path.Match` (an
  escaped `]` does not close a class), `^` is the only negation form (`!` is an
  ordinary member), and a `]` immediately after `[` or `[^` makes the pattern
  malformed rather than opening a class. The third arm is the pre-round-5
  whole-pattern fold itself, kept as a backstop so the escape-aware parser cannot
  LOSE a refusal that cruder rule made; it can only add refusals, never remove one.
  The union is strictly more refusing than either rule this used to
  have (byte-exact, and whole-pattern fold), and the pin
  `TestLedgerClaimIsMoreRefusingThanBothPriorVariants` walks a 168-pair pattern
  matrix against both, while `TestFoldScannerDifferentialAgainstPathMatch` walks
  898,040 pairs in three phases (exhaustive over
  `[`/`]`/`!`/`^`/`\`/`-`/`~`/letters/digits up to three bytes, an exhaustive
  non-ASCII phase with a two-byte letter, plus a seeded random phase) against
  `path.Match` itself on both fold policies. The consequence is unchanged: bare
  `reports` refuses a
  ledger at `reports/code/<ts>`, and a workspace whose sources live under the same
  top-level directory as the run directory must put `--out` outside `--root` (or
  narrow the scope). The pre-consent gate calls this same predicate; it is not a
  copy of it (§3).
- **A scope may not claim ANY run ledger, not just this run's.** The rule above
  compares a scope against the directory this take is about to write, which left the
  cross-run shape open: run 1 writes `reports/code/t1`, run 2 is granted
  `--scope reports/code/t1/**` with an output of its own, both pairwise comparisons
  say "t1 is not t2", and run 2's seat may rewrite run 1's `manifest.json` and
  `snapshot/` with rows that pass every path policy - after which the operator's
  documented `rollback --run t1` installs them and reports them as the recorded
  baseline. The walk therefore recognizes a ledger FINGERPRINT anywhere under the
  root - a directory holding `manifest.json` and a `snapshot/` subdirectory, where the
  manifest's own bytes declare this schema version and a `snapshot_dir` - and a
  scope that could claim one refuses the whole take (`ErrLedgerRefused`, naming the
  prior run directory), while `Rollback` applies the same test to the scopes its
  manifest records. The fingerprint is SHAPE-aware but content-blind about trust: it
  does not consult the signature, so a ledger whose key is gone still refuses, and it
  reads at most 1 MiB of the candidate. A candidate whose `manifest.json` is LARGER
  than that bound is not classified as a ledger at all, so a genuine ledger over a
  large enough workspace (roughly six to eight thousand claimed files) escapes this
  door: what is lost is the early, loud cross-run refusal, never the integrity -
  rewriting that ledger still fails `manifest.sig`, and rewriting only its snapshot
  copies still fails the rollback digest preflight, so nothing is installed
  (§5.14). Two consequences are stated rather than
  hidden. First, an ordinary tree that merely shares the two NAMES no longer refuses:
  an npm-style `myapp/dist/manifest.json` beside `dist/snapshot/` is an ordinary
  directory and is claimed like any other file (round 7 measured the old coarse rule
  over-refusing it). Second, the shape itself is plantable - a seat can write a
  `manifest.json` carrying `version` and `snapshot_dir` and re-create the refusal on
  purpose. The same walk runs on the `Rollback` door, so planting a pair inside a
  run's scope refuses that run's OWN undo too (self-denial, and every run whose
  scopes reach that directory), until the operator deletes the pair - so this arm is
  an availability guard, never an integrity one. The refusal
  names the directory and says what to do: delete the planted pair and re-run. A
  directory LINK at a ledger (symlink or junction) is resolved and judged by the same
  test, because the walk does not follow it and the link would otherwise hide the
  ledger behind a benign name; each DISCOVERED ledger's claim is also measured for
  case folding at the ledger's own subtree, not only at the root and this run's
  output directory, so a folding boundary that encloses a prior run cannot switch
  the refusal off.
- **The manifest is bound to its run (`manifest.sig`).** Every manifest `Take`
  writes is signed: 32 random bytes per take from `crypto/rand`, stored as
  `~/.agy-pro/ledger-keys/<key id>` (key store 0700, key file 0600; on this Windows
  deployment the mode bits are advisory and the scope containment is what keeps the
  key away from a seat), and `manifest.sig` beside `manifest.json` holds
  `HMAC-SHA256(key, manifest bytes)` in lowercase hex. `LoadManifest` - and so every
  `Diff` and `Rollback` - verifies that pair BEFORE it believes a row: a forged or
  hand-edited ledger refuses whole (`ErrLedgerProvenance`) even when every row would
  pass the path policy, and the snapshot copies are transitively bound because their
  digests live inside the signed manifest. The key id is derived from the ledger's
  absolute path, not random, so the manifest bytes stay byte-deterministic for a
  given (root, scopes, destDir), and a re-take into the same directory SPELLED THE
  SAME WAY re-keys it - an older genuine manifest kept aside no longer verifies. A
  re-take through a different spelling of that directory (another ASCII case on a
  folding volume, a junction path, a relative form) derives a second id and writes a
  second key, so the older pair KEEPS verifying: that is the disclosed stale-baseline
  replay bound, not a revocation. The id is also RECORDED in
  the manifest (`key_id`, inside the signed bytes) and read back from there when a
  ledger is verified, so the operator's `--run` spelling - another ASCII case, a
  relative form, a path through a junction - cannot decide which key is consulted;
  rewriting the field only makes the MAC fail, because the attacker would have to
  sign under the key it names. One consequence is disclosed rather than hidden: a run
  directory written before this binding has no sidecar at all - it refuses as
  `unsigned ledger (pre-provenance format)` and stays on disk as evidence, never as
  a restore target. A ledger COPIED elsewhere still verifies (the key id travels
  inside the signed bytes) and is still restorable, but only INTO its recorded root:
  `Rollback` refuses a manifest whose `root` is not the tree in front of it
  (`ErrRootMismatch`), so copying a ledger preserves what it proves and moves nothing.
  What that binding DOES cover is stated so it is not over-read: it binds code at
  seat tier and below, because the key store lives outside every operation root and no
  seat scope may claim a governance/credential path. A process running at the
  OPERATOR's own privilege - the verify child executes operator-authored argv, and any
  same-user process can do the same - can read `~/.agy-pro/ledger-keys` and sign
  anything; at that privilege it can also write any file in the tree directly, with no
  key at all. That boundary is accepted, and it is the same boundary the explicit
  `--verify` consent already assumes. `Rollback` also refuses to run when the key
  store resolves ONTO or INSIDE `--root`, the same containment `Take` enforces before
  it writes a key.
  The store is bounded: `ledger-keys/<id>` files are pruned by a count-plus-existence
  rule (keep the newest 50 records, and never remove one whose recorded run directory
  still exists - a dead directory's key is the only kind that can go), recorded in a
  small `ledger-keys.index.json` beside them. A key with no record is never deleted,
  and the prune is best effort: a bookkeeping failure never fails a take.
- **A manifest row is re-checked at load.** Every `files` and `skipped` row must
  be in canonical Win32 form, must not name a governance, credential or VCS path,
  and a `files` row must not be a secret-class name at all (a row the snapshot
  could never have produced is a forged row). A hand-forged manifest is therefore
  refused whole, at load, before the first write: never a selective restore of
  the "safe" rows and never an archive move of the governed one. The same policy
  runs in `Diff`, so an in-memory manifest is judged by the rule a read-from-disk
  one is.
  The row policy is a NAME test, so a row whose written form is clean while its
  chain transits a link is caught by one more arm: `refuseResolvedChainRows` runs
  `agenttools.ResolvedDirChainRefusal` over every `files` row before the walk and
  before the first write, a row whose chain resolves into a governance/credential
  directory or VCS metadata refuses the whole manifest (`ErrGovernanceRefused`),
  and a chain that cannot be resolved at all refuses it too
  (`ErrRollbackRefused`) - a restore target this package cannot describe is not
  restored. The final component is never resolved, because the restore is an
  atomic temp+rename that replaces a file symlink instead of writing through it.

A file the run created is moved into `<out>/rollback-archive/`, and neither a diff
nor a rollback deletes anything in the workspace: the package has exactly two
removal call sites, and neither can reach a file the run owns - the key-store
retention prune, which works inside `~/.agy-pro/ledger-keys` and never touches a
root, and the case-fold probe's cleanup of the throw-away `.agy-casefold-probe-*`
directory it created seconds earlier (and of nothing else), so rollback never
deletes.

Two things stop that move, and both leave the file exactly where it is:

- **A file the manifest already describes is not archived.** The identity keys
  above are exact, so a manifest row spelled `SRC/ALPHA.GO` no longer "covers"
  the file the walk reports as `src/alpha.go`; before moving a claim, rollback
  asks the FILESYSTEM whether that path IS one of the manifest rows' files
  (`os.SameFile`). A hit leaves the file in place and `rollback.json` names it
  with the reason "the manifest records this file under another spelling". The
  lookup is an index built once per rollback and keyed by the row's ASCII-folded
  path, so only a case-alias spelling is ever a candidate - which is what keeps the
  restraint from sparing anything else: a run-created HARDLINK of a recorded file
  under another name is a created directory entry (moving it leaves the recorded
  entry and its bytes alone), so it IS archived, where round 5 left it in place and
  called it a spelling variant. A
  Unicode look-alike is a DIFFERENT file, so it does not match and IS archived.
  `Diff` keeps the plain answer for the same case - the claim is reported
  `created`, because the manifest genuinely does not record that spelling.
- **A claim whose source is inside the run directory, or whose directory chain
  resolves into a governed directory, is not archived either**, and says so: the
  archive arm runs the same resolved-chain predicate on its source, so a claim
  that would drag a governed file into the run directory by another route is left
  where it is.

If the move fails (a locked file, a cross-volume destination, an archive target
that already exists), the file stays exactly where it is and `rollback.json` names
it in `skipped[]` with the reason. The archive destination is gated on its own
side: it must be in canonical form, clear of governance/VCS names and inside the
run directory. The archive claims are enumerated BEFORE the first write, so a
governed claim refuses while the tree is still untouched. A missing, corrupt or
foreign-root manifest refuses before a single byte is written.

`git` is never invoked, imported or parsed anywhere in this path, and `.git` is
not a "scope that claims it" exception: VCS metadata is refused at take like any
other protected name, because a snapshot the new load policy must refuse is a
take that cannot be undone.

### 4.5 Consent gate

A provider seat in a seat list, or any write run, is metered and asks for consent:
a terminal asks y/N, a reader that cannot answer is refused unless `--yes` is
given, and a native-only phase never asks.

A refusal at a `write` invocation runs nothing: no snapshot, no plan file, no
fanout call. The echo names each metered seat (masked) and states that no cap
exists. Rollback is exempt on purpose: requiring a flag to undo a bad run would be
the wrong incentive.

In the composed `run`, the write phase's consent gate sits AFTER the plan phase
(and after the plan's snapshot when `--scope` was given), so a refusal there stops
the metered write, not the whole run: the plan artifacts the operator already paid
nothing for stay on disk, and the run exits 2 with nothing written by a seat.

The consent echo of a write run also carries the verification disclosure and the
run-total verify budget (§4.8), because the verifier the operator is consenting to
runs over the tree the seat is about to write.

### 4.6 Cost truth

No token or dollar cap is enforced anywhere in this door: no flag, no default, and
no code path compares a spend against a ceiling, so the report prints no dollar
figure.

What actually bounds spend today is the engine's per-seat iteration limit
(`defaults.maxTurns`, 10 by default, in the registry defaults block) and the wall
clock (`--timeout`, or the engine's own partition scaling). There is no price
table in this path: `internal/codework` never converts tokens to money, and the
repository's dashboard pricing data (`data/pricing.json`) has no entry for any
registry model this door can seat (`<provider>/<model>` resolved from the user's own
registry), and `agy-pro` never reads it.
The report prints token counts when the engine's `summary.json` carries them, and
says so plainly when it does not. It also names its own counts unambiguously:
`native_seat_occurrences` and `provider_seat_occurrences` count seat ROWS (one
row per seat per phase run, so the default native line-up seated in both the plan
and the review phase is four occurrences of two models), `unique_seat_models`
answers the other question - how many different models this run seated - and the
human line says `seat occurrence(s)` for the same reason. Neither number has to
be guessed from the other. A real budget meter is a future decision, not a
shipped feature.

Every operand the door echoes is masked (`providers.MaskOperand` for one operand,
`providers.MaskSentence` for a sentence that embeds one), and the request text is
never echoed at all: it lands verbatim in the run directory as the phase's
`ask.txt`.

### 4.7 Native phases: detected, not contained

A native seat is a real `agy` subprocess with its own tool surface, and the engine
builds no scoped runner for it (`internal/fanout/engine.go:911-918`), so the
read-only grant this door passes is a grant, not a sandbox: a plan or review seat
can still write. The door does the two things it can, and says what it did. When
a write phase follows the plan, the snapshot is taken BEFORE the plan seats run
and the write phase reuses that baseline instead of re-taking it, so a
native-phase edit can never become the rollback baseline and rollback restores the
bytes the operator actually had. After every native phase the door re-walks the
snapshot scope, diffs it against the manifest, and reports the changed, created
and deleted files as that phase's `drift` rows, in `<out>/drift.json`, and as the
appended status token `native-drift`; the run continues, because the provider
writer is still bound by scope and the denylist. Governance and credential names
under the workspace root are re-hashed around the same phases (the U2 denylist
names that exist there), and a change is reported as `governance-drift`. The plan
and review asks also carry a fixed read-only instruction; that is prompt-level
mitigation - the belt - while the snapshot and the drift rows are the braces.

The same honesty applies to the seats themselves: a native phase whose seats only
partly delivered is `degraded`, never `ok` (§4.10). The review prompt carries the
read-only instruction only: the write contract ("produce a minimal diff", "only
edit files inside your scope", "`## Changed Files`") is not in it, because a
reviewer that holds real tools should not be handed two contradictory orders.

### 4.8 Verification: compile-only detection, explicit exec, one budget

Detection is compile-only: with `go.mod` at the root, a scope entry naming a
`.go` file yields `go build` and `go vet` for that package directory, and the
engine schedules no `go test` and no `npm test`.

Any other verifier, including every test runner, runs only when the operator
names it with `--verify`.

`--verify` is the operator's own argv, and the door says what that means before it
runs anything: verify executes commands over the working tree, which may include
code created by the writer seat in this run.

The environment is decided by WHAT runs, not by who named it. One predicate
(`verifyChildEnv`, `internal/codework/verify.go`) answers it for both seams:

- **A go-toolchain verifier runs hermetic: `GOPROXY=off`, `GOTOOLCHAIN=local`,
  `CGO_ENABLED=0` (`detectedVerifyEnv`, `internal/codework/verify.go`)** -
  whether the engine detected it or the operator named it. The reason is the same
  in both cases: the command runs over a tree a metered seat just wrote, so a
  seat-written `go.mod` / `go.sum` must not fetch a module, download a toolchain
  or build cgo, and that trust decision cannot depend on who typed the argv. An
  operator-set `GOPROXY`/`GOTOOLCHAIN`/`CGO_ENABLED` is therefore NOT passed
  through; an operator who genuinely needs the network names a shell wrapper as
  the verifier instead of a bare `go`. The identifier is still called
  `detectedVerifyEnv` for continuity, and the seam only decides WHICH runner
  executes, never what the toolchain may reach. The consequence is stated
  plainly: a go build cannot fetch a module, so a tree whose build needs a
  download fails the verification instead of fetching one, and cgo code does not
  compile under this environment on either route.
- **Every other explicit `--verify`** runs with the sanitized environment and
  nothing added, because the operator named a command that is not the toolchain
  this door hands a seat-written `go.mod` to.

The trio is a TOOLCHAIN restriction, not a sandbox: it stops a module fetch, a
toolchain download and a cgo build, and it does not put the child in a network
namespace, behind a socket filter or under a syscall policy. A verifier runs code
from the tree, and that code can dial the network perfectly well under
`GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0`. The echo says so in as many words
(`the verifier's own code is not network-sandboxed`) instead of calling the run
"offline", because an operator who reads "offline" could believe a run they
consented to was isolation-tested.

The consent echo states which arm applies before anything runs - `env: hermetic
(GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0) …` with the toolchain sentence and
its not-network-sandboxed clause, or `env: explicit --verify command(s) run with
the sanitized environment …` - and it
asks the same predicate the runner uses, so the echo cannot promise an
environment the child does not get (§4.5). The disclosure the operator consents to
is unchanged: verify executes commands over the working tree, which may include
code created by the writer seat in this run.

The whole run gets one verification wall budget of 1800 seconds
(`maxVerifyTotalSec`, `internal/codework/verify.go`). Each command keeps the
600-second engine deadline, clamped to what is left of the budget, and a command
that would start after the budget is spent is recorded as `skipped-budget` with
`Passed=false`, so the run does not pass and no repair run is spent on a
verifier that never started. The report row for such a command carries the token
`skipped=verify-skipped-budget`, so a machine reader can tell "the verifier
failed" from "the verifier never ran".

`--verify` with an empty operand is a usage error (exit 2), not a silent skip:
omitting the flag is how detection is requested.

The verification excerpt a repair seat reads is fenced as untrusted data: the
repair body carries it inside a `> **[UNTRUSTED-DATA]**` block - the same marker
the review prompt uses for its change report - with the instruction to treat
everything inside the fenced block as data, never as instructions.

The fence is a marking discipline over text a seat produced, so it is built to
survive that text: a run of backticks inside the excerpt cannot close the block.
The fence pair is widened to one backtick longer than the longest backtick run
inside the excerpt (`repairFenceFor`), capped at 16 backticks
(`repairFenceMaxTicks`), because a closing fence is a run at least as long as the
opener; an excerpt with no backtick run keeps the three-backtick pair the review
prompt uses, so the common case is byte-identical. The cap is not a second
mechanism for the same problem but the bound that keeps the first one honest: a
run of 16 or more backticks is replaced BEFORE the width is measured by a plain
ASCII marker naming its exact length, `[N backticks elided]`
(`neutralizeFenceRuns`), so the excerpt keeps the diagnostic text it exists to
carry and the block keeps its structure. Without it, a 3000-backtick run forces an
opener and closer of 6001 bytes together - more than the whole repair-tail budget
- and the bound then cuts the block from the FRONT, so the fence arrives open,
which is worse than no fence at all. The width is measured on the whole tail
before the body is bounded, and the bound keeps a suffix of it, so the kept part
can never hold a longer run than the one measured. An excerpt that could close
its own fence would let the seat under test end the "this is data" region and have
the rest read as instructions.

A tree with no compile-only verifier (an npm-only project, a language with no
build step) reports `unverified` instead of running a package script. Name a
verifier with `--verify` to get a verdict.

### 4.9 Plan ingestion and the effective scope

A plan that arrives through `--plan` is validated by the same governance and
canonical-form scope predicate the plan builder uses, before the snapshot and
before any seat runs. The refused entry is named in the error, the run fails, and
nothing starts: no snapshot, no plan file, no fanout call. The operator's own
`--scope` entries go through the same check, and so does the LEDGER rule on both
routes: a unit scope that could claim the run directory, or any PRIOR run's ledger,
is refused over the union of the unit scopes before the consent prompt (§4.4).

When `--scope` and a plan's unit scopes disagree, the effective snapshot, diff
and verification scope is their union, and the echo names it. A unit scope that
reaches outside `--scope` therefore cannot produce a write the snapshot and
rollback do not cover.

A baseline snapshot narrower than that union is refused rather than reused:
reusing it would leave the plan's own writes un-restorable.

### 4.10 Seat honesty: `degraded`, not `ok`

A fanout run exits 0 when reconciliation produced its artifacts, even if a seat
was rate-limited, timed out or answered nothing. Reporting that phase as `ok`
would claim a review nobody gave, so the door reads the engine's own per-seat
rows back out of `<phase>/summary.json` and decides:

- every seat reported success: `ok`;
- some seats failed, the rest delivered: `degraded` - the artifacts exist, the
  run keeps exit 0, and the report says so three ways: the phase row carries
  `"status": "degraded"` with `seats_total` / `seats_failed`, the run status line
  appends the `(degraded)` token (`seat_status` in the JSON, the drift token's
  sibling), and the seats line moves ABOVE the status line with the failed count
  so `ok` never opens the block;
- every seat failed: `failed` with an error naming the count, and exit 1 - there
  is no partial result to qualify;
- no `summary.json` at all: `ok`, because no seat claim is made (the cost note
  already says the telemetry is missing).

`phases.log` carries the same verdict (`status=degraded`), so the trail and the
report cannot disagree.


---

## 5. Known limitations

1. **Seat grammar is permissive.** The fanout seat parser accepts a bare
   credential-shaped string as a provider spec (no `provider/model` separator), so
   a pasted key in the `--writer` slot parses as a seat. The consent gate still
   blocks the spend and every echo masks the value, but the shape is accepted
   instead of refused. A model id the registry does not resolve therefore reaches
   the engine and fails the run there (exit 1) rather than failing as a usage
   error. Fixing it means tightening the shared seat parser, which would also
   affect `agy pro fanout --seats`.
2. **Windows `< /dev/null` looks like a terminal.** `stdinIsTTY` tests
   `ModeCharDevice`, and the NUL device satisfies it, so a redirected stdin on
   Windows prints `Proceed? [y/N]` and then takes the decline branch. Safety is
   intact (exit 2, nothing ran, guidance names `--yes`); the prompt line is
   cosmetic noise on this platform.
3. **A repair attempt re-runs the whole verification list.** Each attempt starts at
   the first `--verify` command; the failing command is not skipped. A repair run
   also re-executes every unit in the plan, including the ones that passed, so one
   repair costs one full plan run (cost, not safety).
4. **Symlinked directories are not descended.** The snapshot walk uses
   `filepath.WalkDir`, which does not follow directory links, so files reachable
   only through a directory symlink are outside every snapshot scope. Name real
   directories in `--scope`; a bounded plan scope always does. This is the same
   limitation the claim walk has. One exception, added for the ledger scan: a
   directory LINK that resolves to a run ledger is resolved and tested against the
   fingerprint, so a planted junction cannot hide a prior ledger from the cross-run
   refusal (§4.4). The mirror shape stays open and is recorded here: a planted
   FILE link whose RECORDED path lands inside a prior ledger is claimed under the
   dual view, and neither door refuses it - the writing capability is an
   operator-privilege one (the seats have no link primitive), and the provenance
   layer (§4.4) still refuses to restore a forged ledger, so the impact is a
   corrupted baseline, never installed attacker bytes.
5. **`providers.json.*` documentation suffixes.** A component named
   `providers.json.<ext>` is refused as a registry alias unless `<ext>` is one of
   `md markdown txt rst adoc asciidoc html htm`, so `docs/providers.json.md` stays
   writable while `providers.json.bak` stays refused. The carve-out is narrow and
   deliberate; any other suffix is treated as a credential-file alias.
6. **Native seats can write outside this door's containment model.** On a machine
   whose Antigravity configuration always proceeds, a native seat writing headless
   is possible, but native seats never build this door's scoped runner and are not
   part of its containment: that capability belongs to agy's own tool layer. Plan
   and review seats are read-only by grant (`--tools read,search`), not by this
   door's scope machinery. The door therefore measures instead of containing
   (§4.7): drift inside the snapshot scope is named, and a governance/credential
   change under the root is named, but a native seat's write outside both the
   declared scope and the watchlist is still invisible, and a native seat reports
   no tool calls or tokens (`usage: null`), so its work is known only by what the
   tree ended up holding.
7. **The run directory does not record the workspace root.** `code review --run`
   and `code rollback --run` must be given the same `--root` the write used when it
   was not `.`; otherwise the manifest's root check refuses the rollback.
8. **Verification and attribution are whole-tree.** The verifier runs over the
   plan's scope, not per unit, so a failing unit is not attributed to a seat; the
   seat rows in the report carry the engine's own per-seat status, and the file
   verdict comes from the verifier.
9. **A Windows verification timeout kills only the direct child.** Grandchildren
   (compiler children) can outlive the deadline. The write phase never assumes
   quiescence: it diffs the tree after the call returns and reports what it
   finds.
10. **Compile-only detection still compiles, and the toolchain still runs.**
    `go build` and `go vet` do not run the package's own programs, and the
    environment this door gives them is hermetic (§4.8: no module fetch, no
    toolchain download, `CGO_ENABLED=0`, and no operator-set toolchain names), so
    a build directive cannot reach the C compiler through cgo from a verifier this
    door runs. What remains is the toolchain compiling the tree's own code against
    the local module cache: reading and compiling, not executing what the tree
    holds. The guarantee is narrower than "no execution happened" and is stated as
    such: the engine schedules nothing that imports or runs the tree's own code.
11. **The verify budget is per run, not per command.** `maxVerifyTotalSec` (1800s)
    is spent by the sum of every command of every attempt, so a plan with many
    units plus several repairs can reach `skipped-budget` before the work is
    verified. The run then fails closed (status `verify-failed`) instead of
    passing on a partial verification.
12. **Out-of-root hardlink aliases are not withheld.** The inode arm (§4.4) withholds
    a claim whose inode is shared with a governance/credential, VCS or secret-class
    path of the SAME WALK. An alias whose target lives OUTSIDE `--root` (a same-volume
    `os.Link` of a homedir secret into the tree, made by the verify child or any
    same-user process before the take) has no denylisted name anywhere in the walk,
    so nothing marks it: the ordinary-looking name is claimed, and its bytes are
    copied into `snapshot/`. The tier is the same one §4.4's binding-scope paragraph
    names: creating a link is an operator-privilege capability (the seats have no link
    primitive), and at that tier the process can read the target file directly, so the
    disclosure is about what the LEDGER may end up holding, not about a seat gaining
    bytes it could not reach. Recorded, not fixed. Future hardening: ask the platform
    for the link count of every claim and withhold a multi-link file whose other
    entries cannot be resolved inside the walk - `GetFileInformationByHandle` returns
    `NumberOfLinks` on Windows, so the honest arm is one open handle per claim (the
    Unix ports already have that gate in `linkcount_unix.go`; Windows currently
    reports "unknown" and compares identities only). Until then, `--root` boundaries
    and the denylist name classes are the containment.
13. **Provenance binds what a scope can reach, not what an operator can.** `manifest.sig`
    proves a ledger was written by a run holding the per-run key, and the key store
    sits outside every root, so no seat scope reaches it. A process at the operator's
    own privilege (the verify child runs operator-authored argv; any same-user process
    is equivalent) can read the key store and sign a manifest, exactly as it could
    write any file in the tree without one. Reading that as a defect would mean never
    running a test suite at all; the accepted stance is that the binding covers
    seat-tier code and the explicit-verify consent is the operator's own act (§4.8).
14. **A ledger bigger than the fingerprint bound escapes the cross-run refusal.** The
    shape detector reads at most 1 MiB of a candidate `manifest.json` (one row per
    claimed file, so a workspace of roughly six to eight thousand files crosses it),
    and a candidate past that bound is not classified as a ledger. Run 2's scope over
    such a prior ledger is therefore not refused by the fingerprint door - the early,
    loud refusal is what is lost (§4.4). Integrity is unaffected: a rewritten
    `manifest.json` fails `manifest.sig` at rollback, and rewritten snapshot copies
    fail the digest preflight, so nothing is installed. The bound is a resource guard
    (never read whatever file a seat points us at), and the trade is stated rather
    than papered over: raising it moves the escape, not the guarantee.

---

## 6. Quickstart (agy TUI master, non-paid path)

`q.txt` holds the request text; the scope names one bounded path; the wrapper
seats stay native and free.

```bash
# 1. plan only, native seats, free. Writes the run directory and stops.
agy pro code plan --ask-file q.txt

# 2. read the plan, then run the composed door with one bounded scope.
#    --writer is a provider seat, so this step is metered and asks y/N
#    (or takes --yes). The verify command is the engine's to run, not a seat's.
agy pro code run --ask-file q.txt --scope "internal/x" \
  --writer <provider/model> --verify "go build ./..." --yes

# 3. if the verification failed (exit 1), the report names the undo command:
agy pro code rollback --run reports/code/<ts>
```

The paid steps in that sequence are step 2's write phase and any provider seat in
a `--seats` list; everything else is native.

Progress while a phase runs:

- `reports/code/<ts>/phases.log`: the codework trail, one line per phase;
- `reports/code/<ts>/<phase>/progress.log`: the engine's per-seat progress for
  that phase, next to `summary.json` and `seat-*.md`;
- `reports/code/<ts>/report.txt`: the change report once the tree has been
  touched (this is what `review --run` reads).

Inspect a run without spending: add `--dry-run` to any subcommand to see the exact
vectors; add `--json` to read the machine block (one `CodeRunReport` document on
stdout, human block suppressed).

---

## 7. Related

- [`fanout.md`](fanout.md): the engine this door composes, seat grammar, partition
  mode (`--units`, §8), the exclusion list (§4.1), and the tool sandbox (§5).
- [`swarm-hybrid.md`](swarm-hybrid.md): hybrid native + provider seat specs.
