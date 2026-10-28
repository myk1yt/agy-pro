---
name: commit-standard
description: >
  Enforces Conventional Commits standard. Prevents lazy commit messages like "fix bug" or "update file". Requires What and Why.
---

# Commit Standard

A commit lifecycle skill: staged-diff-based message writing, atomic commit judgment, breaking changes, footers, safe commit execution, hook handling, and post-commit verification.

## 1. Purpose

This skill prevents the most common commit failures:

- Writing a message without reading the diff, the staged diff is the source of truth
- Messages that don't match the staged changes, validate message-diff consistency
- Mixed unrelated changes in one commit, atomic commit judgment
- Vague subjects ("fix bug", "update file"), subject rules
- Missing root cause in bug-fix messages, root cause expression
- Missing breaking-change marker, breaking changes review
- Guessing issue numbers, authors, or reviewers, footer and trailer rules
- Unauthorized staging/committing/history rewrites, role boundaries and execution gates
- `git add .` sweeping unrelated files into a commit, staging safety
- Committing secrets or binary artifacts, sensitive data review
- Bypassing hooks (`--no-verify`), hook policy
- Committing content never reviewed; claiming tests pass without running them
- Confusing working tree with staged snapshot (`git diff` vs `git diff --cached`)
- Hiding remaining changes after commit, report exactly what remains

## 2. Role Boundaries and Modes

Three authority levels:

1. **Inspect**, investigate repository, branch, diff, and conventions. Always allowed.
2. **Propose / Validate**, suggest a commit message or validate one against the staged diff. Always allowed.
3. **Execute**, stage files or run `git commit` (or amend/revert). **Only on explicit user request.**

**Writing a message is NOT committing.** Each write action (stage, commit, amend, revert) requires its own explicit user request. When intent is ambiguous, default to the least destructive mode: `MESSAGE_ONLY` (write a proposed message) or `REVIEW_STAGED` (inspect staged snapshot, assess atomicity, report readiness), never infer `COMMIT` from a message-writing request.

## 3. When to Use / When Not

Use for: writing/validating commit messages, reviewing the staged snapshot, judging atomicity, fixing commitlint failures, analyzing hook failures.

Do **not** proceed (report the blocker instead) when: code is still unverified, the staged scope is unclear, a merge conflict is unresolved, staged content may contain secrets, a hook failure is unresolved, others' changes are mixed in, the branch state is unexpected (detached HEAD, wrong branch), the directory is not a Git repository, or a history rewrite lacks explicit approval.

## 4. Repository Convention Detection

Before writing any message, detect the repo's commit conventions in this order:

1. **User request**, explicit instructions take highest priority
2. **Machine-enforced rules**, commitlint config, CI checks, husky/lint-staged hooks
3. **Repo docs**, `CONTRIBUTING.md`, `AGENTS.md`, README commit sections
4. **Consistent recent history**, the dominant pattern in the last ~50 commits
5. **Skill defaults**, the rules in this document

Search for: `CONTRIBUTING.md`, `AGENTS.md`, `.commitlintrc*`, `commitlint.config.*`, `.czrc`, `semantic-release`/`release-please`/`changeset` configs, PR templates (may mandate `Closes #`), DCO/sign-off requirements.

**Conflict rule:** machine-enforced rules win over history; the user request wins over everything. Report the conflict if it changes the message.

## 5. Git State Inspection and the Staged Snapshot

Before any message or commit work, establish:

- Repo root (`git rev-parse --show-toplevel`), branch/upstream (`git status --short --branch`), HEAD SHA
- Detached HEAD (`git symbolic-ref -q HEAD`, empty = detached), merge/rebase in progress
- Staged files (`git diff --cached --name-status`), staged diff content (`git diff --cached`), unstaged changes (`git diff`), untracked files (`git ls-files --others --exclude-standard`)
- Deleted/renamed/submodule/binary detection via `--name-status` (D/R/S) and `--numstat` (binary shows `-`)

**Critical distinction:** `git diff --cached` shows the **staged snapshot** (what a commit would record). `git diff` shows the **working tree**. Never confuse them.

**Staged snapshot priority:** the commit message describes the staged snapshot only, exactly what `git commit` would record. It cannot be based on the implementation summary alone, unstaged edits, planned-but-unstaged changes, or unexecuted tests. If there are **no staged changes**, do not declare the repository commit-ready, report the state and request explicit staging instructions.

## 6. Staging Safety

**Never stage files without an explicit user request.** When staging is requested:

1. Identify the explicit files the user named; verify each file's relevance
2. Check for sensitive files (§7) before staging
3. Review deletes, renames, binary files, and generated artifacts in the list
4. Use explicit pathspecs, never broad patterns
5. Re-review `git diff --cached` after staging to confirm the snapshot

**Default bans:** `git add .` and `git add -A`; unreviewed wildcard staging; arbitrary unstaging; committing without reading the full staged patch; partial-hunk staging unless the exact hunks were requested (e.g., `git add -p` with clearly bounded selection).

## 7. Sensitive Data Review

Before any commit, scan the staged files and diff for: API keys, tokens, passwords, private keys, certificates, `.env` and credential files, cloud provider keys, registry tokens, database dumps, production config, user data/access logs/session data, embedded secrets, large binaries/build output/caches, editor/OS metadata (`.DS_Store`, `Thumbs.db`).

- Use an automated secret scanner if one is available in the repo or toolchain
- **Block the commit** on an actual secret risk, do not proceed
- Do **not** display secret values; report only the file and the type of risk

## 8. Atomic Commit Judgment

**One commit = one coherent purpose.**

A staged snapshot is atomic when ALL hold:

- Explainable in one sentence; all staged files serve the same purpose
- Independently revertible and independently reviewable
- The repository is in a reasonable state after the commit; verifiable without other changes
- One title honestly describes the entire staged diff

**Split when the snapshot contains:** different bug fixes; a feature plus an unrelated refactor; a behavior change plus massive formatting; a dependency upgrade plus an independent feature; production code plus unrelated doc cleanup; independent multi-subsystem changes; a generated artifact plus a separate manual change; a rename plus a behavior change; a test cleanup plus an independent production fix; a CI change plus an independent app change.

**Do NOT split:** an implementation and its tests; a schema change and its required serializer/type changes; a bug fix and its regression test; source and the required generated artifact; an API change and the docs it directly requires; a dependency upgrade and its required compatibility patch.

**File count alone is never a split reason.** One purpose across 20 files is one commit; three purposes in one file are three commits.

**Splitting procedure:** propose a split plan (do not arbitrarily restructure the staged state). Present per-commit purpose, type, scope, files, rationale, and suggested message as YAML. Preserve at all times: other users' changes, partially staged state, untracked files, the current index and working tree. If the snapshot cannot be split safely, report the blocker and do not force a split.

## 9. Conventional Commits Grammar

```
<type>[optional scope][optional !]: <description>

[optional body]

[optional footer(s)]
```

- **`!`** after the type/scope marks a breaking change; expressible via header `!`, a `BREAKING CHANGE:` footer, or both, follow repo policy; when in doubt, use both
- Blank line required between header, body, and footers

**Types:** `feat` (new observable behavior), `fix` (defect fix), `refactor` (structure change, behavior preserved), `perf`, `test` (only tests), `docs`, `build` (package/build config), `ci` (CI/workflow config), `style` (no behavior change), `chore` (maintenance, no user-visible effect), `revert`.

Rules: the repository may restrict the allowed set (commitlint), respect it; do **not** use `chore` as a catch-all; choose by the commit's **primary purpose**, not by file types (a docs file in a `feat` commit stays `feat`).

**Scope = a stable subsystem, package, module, or product area.** Good: `auth`, `cli`, `api`, `parser`. Bad: file extensions, temp branch names, issue numbers, `misc`, `all`, full filenames. Selection order: commitlint policy → actual package/workspace name → stable subsystem name → omit. For a cross-package atomic change, use a higher-level scope or omit.

## 10. Subject Rules

The subject MUST:

- Describe the **actual staged diff**, in imperative mood (or the repo's consistent tone), concise and specific
- Have **no trailing period**; avoid unnecessary capitalization
- Describe behavior/purpose, not a file list; prioritize the meaningful outcome over implementation detail
- Not claim unverified effects; respect the repo's length limit (default ≤ 72 characters)
- Not end with a bare `update` / `change` / `fix` / `misc` / `cleanup`

**Prohibited:** `fix bug`, `update code`, `change files`, `misc fixes`, `cleanup`, `work in progress`, `final changes`, `address feedback`, `improve stuff`, `test`, `refactor code`.

**Good:** `fix(auth): validate refresh token before clearing session`, `feat(cli): add --dry-run flag to migrate command`, `refactor(parser): extract tokenizer from lexer`.

## 11. Body Rules and What/Why/Impact

**Not every commit needs a body.** A body is required when the title alone cannot convey motivation or cause, the root cause matters, multiple layers changed, a contract changed, migration impact exists, a non-obvious tradeoff was made, or revert/rollback context is important. Omit for obvious typo fixes and small single-purpose changes fully described by the title.

A good body answers: Why was the change made? What was wrong before? What was the root cause? What invariant is restored? Why this approach over alternatives? What compatibility/tradeoff does it introduce? What tests confirmed it? What was intentionally NOT changed? Do **not** turn the body into a per-file changelog.

For important commits, include a balance of **What** (behavior/contract change), **Why** (problem, motivation, root cause), and **Impact** (user/API/data/compatibility/build/ops effects), as natural prose, not forced labels:

```
fix(auth): validate refresh token before clearing session

The refresh handler cleared the previous token before validating the
replacement, so a failed refresh left the user logged out with no
recovery path. Validate the incoming token first and only clear the
session when the replacement is confirmed valid.

Impact: users with expired refresh tokens keep their session until a
valid replacement arrives; no API contract change.
```

## 12. Root Cause Expression

A bug-fix body must explain **why the invalid state was created**, not repeat the symptom.

- **Bad:** "The app crashed because an error occurred."
- **Good:** "The refresh handler cleared the previous token before validating the replacement, so a failed refresh left the session in an invalid state."

If the root cause is **not confirmed**, do not write a hypothesis as certainty, describe the observed behavior and the safe fix only:

```
fix(parser): reject unterminated string literals

Observed: malformed input caused an unhandled panic. The parser now
returns a parse error for unterminated strings instead of panicking.
Root cause of the panic is still under investigation.
```

## 13. Test and Verification Claims

Claims like "tests pass" or "no regressions" require **actual evidence**, the checks must have been executed. Keep it short and specific (`Tests: pnpm --filter @example/core test`); never mention verifications that were not executed; if verification is pending, say so or omit the claim.

## 14. Breaking Changes and Footers

Review for breaking changes when the diff: removes/changes a public API, removes/renames a CLI option or changes its semantics, changes a config key or environment variable, breaks DB or persisted-state compatibility, adds a required schema field, changes a response shape or package export, changes default behavior incompatibly, changes a protocol, removes a supported runtime/platform, changes an auth/permission contract, or requires consumer migration.

Format:

```
feat(api)!: require explicit workspace identifiers

BREAKING CHANGE: workspace identifiers are now mandatory in all
create/update requests. Migrate by adding the `workspaceId` field;
the `workspace` shorthand is removed.
```

Include a migration path when one exists. Do **not** use `!` for emphasis without an actual breaking change.

**Footers:** `BREAKING CHANGE:`, `Closes: #123` (fully resolved), `Fixes: #123` (fully fixed), `Refs: #123` (related only), `Reviewed-by:`, `Co-authored-by:`, `Signed-off-by:` (only when DCO required).

Rules: only use **known** issue numbers, never guess; `Closes`/`Fixes` only when the commit fully resolves; never invent author/reviewer info or email addresses; no AI attribution trailers unless requested; maintain `Key: value` format with a blank line before the footer block.

## 15. Commit Readiness Check

Before committing, verify ALL:

- [ ] Staged changes exist and the staged diff has been reviewed
- [ ] Snapshot is atomic (§8)
- [ ] Message covers the entire staged diff; type/scope match the primary purpose
- [ ] Repo policy compliant; body root cause proven, not hypothesized
- [ ] No secrets or unintended files; breaking changes and footers correct
- [ ] No blockers outstanding; commit mode matches the user request; branch/repo state as expected

**Readiness states:** `READY` (commit may proceed with authorization) / `NEEDS_SPLIT` (propose split) / `NEEDS_MESSAGE_REVISION` / `BLOCKED` (secrets, conflicts, hooks, state) / `INCONCLUSIVE`. **Only `READY` allows an actual commit.**

## 16. Hook Policy

**Default: let hooks run.** Never bypass silently. Banned: `--no-verify` without explicit user approval; modifying hook scripts to bypass checks; reporting a failed hook as success; ignoring a hook timeout; re-committing hook-modified files without review; running a narrower check after a hook failure and declaring pass.

On hook failure, record: hook name, command, exit code, key error output, whether files were modified, staged/working-tree state after, and the fix + re-verification needed. **If a hook modifies files** (e.g., a formatter), re-review the staged snapshot and message fitness before any retry, the diff may have changed.

## 17. Commit Execution, Amend, and Revert

**Only with an explicit user request AND a `READY` judgment.** Use a temp message file or safe multiple `-m` args to preserve line breaks. Re-verify immediately before committing: branch, staged file list, staged diff fingerprint, final message, amend flag, sign-off flag, hook policy.

Without explicit approval, ban: `git commit --amend`, `git reset`, `git rebase`, `git squash`, force push, commit date manipulation, author change, GPG signing config change, hook bypass, empty commits (`--allow-empty`), merge commit creation. Do not claim success on commit failure, report the actual error and state.

**Amend is a history rewrite.** Verify before amending: explicit user request, target is current HEAD, whether the commit may already be pushed/shared, author/timestamp impact, staged changes match the existing commit's purpose, force-push risk. If the new changes have a different purpose, recommend a new commit instead. **Never amend an already-shared commit without explicit approval.**

**Revert format** (follow the repo's Git-default revert format when used):

```
revert: <subject of the reverted commit>

This reverts commit <sha>.

<reason and downstream impact>
```

Never guess the revert target SHA, use the actual SHA from history.

**Merge/fixup/squash:** respect the Git/platform default merge format (do not force Conventional Commits onto merge messages); `fixup!` only when autosquash explicitly requested; a squash final message must describe the **entire final diff**, not concatenated subjects; history rewrites require separate explicit approval.

## 18. Post-Commit Verification

After a commit, verify: commit success, new SHA, final subject/body, author, included files and stat, hook results, remaining staged/unstaged/untracked changes, and whether a hook modified the working tree after the commit.

```
git show --stat --summary --format=fuller HEAD
git status --short --branch
```

**Do not auto-fail because the working tree is not clean.** Report exactly what remains, remaining changes are normal and must be disclosed, not hidden.

## 19. Message-Diff Consistency Validation

Validate the message against the staged diff:

- [ ] Type matches the primary purpose; scope matches the actual subsystem
- [ ] Subject describes the entire staged diff
- [ ] Body root cause is proven, not hypothesized
- [ ] No claimed behavior that does not exist in the diff; all important staged changes explained
- [ ] No hidden unrelated changes; breaking change marked; issue footers accurate
- [ ] Verification claims match actual execution
- [ ] A future reviewer can understand the motivation; a revert would remove one coherent purpose

## 20. Output Format

### Template 1: Commit Review (message writing / validation / staged review)

```markdown
## Status
[READY | NEEDS_SPLIT | NEEDS_MESSAGE_REVISION | BLOCKED | INCONCLUSIVE]

## Repository State
- Branch: [branch] | HEAD: [sha] | Upstream: [present/missing]
- Staged: [N files] | Unstaged: [N files] | Untracked: [N files]

## Atomicity
[One sentence: single purpose, or split plan reference]

## Proposed Commit Message
```text
<type>(<scope>): <subject>

<body>
```

## Rationale
- Type: [why this type] | Scope: [why this scope]
- What: [behavior/contract change] | Why: [motivation/root cause]
- Breaking: [yes/no + marker] | Footers: [issue refs, trailers]

## Risks / Blockers
[Secrets, hook concerns, state issues, or none]
```

### Template 2: Commit Result (after an actual commit)

```markdown
## Status
[COMMITTED | FAILED]

## Commit
- SHA: [sha] | Branch: [branch]
- Subject: [subject] | Files: [N changed, +X/-Y] | Hooks: [passed / failed with details]

## Commit Message
```text
<full message>
```

## Remaining Changes
- Staged: [files or none] | Unstaged: [files or none] | Untracked: [files or none]

## Notes
[Hook-modified files, follow-up actions, or none]
```

**Completion criteria:** `READY` requires staged snapshot exists, diff reviewed, coherent purpose, message matches diff, repo policy compliant, no sensitive files, correct breaking/footers, no blockers, execution authority clear. `COMMITTED` requires the commit succeeded with SHA, actual message, files/stat, hook results, and post-commit state confirmed.

## 21. Compact Protocol

```
DETERMINE MODE
→ READ REPOSITORY POLICY
→ INSPECT BRANCH AND GIT STATE
→ READ STAGED SNAPSHOT
→ CHECK SECRETS AND RISK FILES
→ ASSESS ATOMICITY
→ SPLIT IF NECESSARY
→ SELECT TYPE AND SCOPE
→ WRITE WHAT / WHY / IMPACT
→ MARK BREAKING CHANGES AND FOOTERS
→ VALIDATE MESSAGE AGAINST DIFF
→ COMMIT ONLY WITH EXPLICIT AUTHORIZATION
→ ALLOW HOOKS TO RUN
→ VERIFY COMMIT SHA AND CONTENT
→ REPORT REMAINING CHANGES
```

Each arrow is a gate: do not pass to the next stage without completing the current one.
