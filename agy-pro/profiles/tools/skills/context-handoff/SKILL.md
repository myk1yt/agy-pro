---
name: context-handoff
description: >
  Saves a work-session handoff with an exact resume point and resumes from a prior handoff with staleness classification. Ensures continuity across sessions, context limits, and agent switches.
---

# Context Handoff

Two phases in one skill: **HANDOFF** (save a resumable snapshot of the current
work) and **RESUME** (restore from a prior snapshot, classifying what is still
trustworthy). No external memory tools, the handoff file plus the live
repository are the only state.

## Core Principles

1. **Resume-Oriented**: record what the NEXT worker should do, not what was done.
2. **Current State Is Authoritative**: a handoff is a past snapshot. Current
   source, Git state, and repo instructions take precedence over the document.
3. **Exact Resume Point**: always name the first concrete file/symbol/test/command
   to open, never "continue implementation".
4. **Selective Trust**: every saved fact is classified on resume as `VALID`,
   `STALE`, `CONFLICTING`, `UNVERIFIED`, or `NOT_APPLICABLE`.
5. **Read-Only**: handoff ≠ commit/stash/checkout/reset; resume ≠ checkout/reset/
   stash/merge/rebase. Neither changes Git state unless the user separately requests it.
6. **Summary, Not Dump**: summarize paths, symbols, commands, results. Never copy
   full diffs, logs, or file contents. No hidden state: surface incomplete work,
   failures, blockers, guesses, and unverified items.

## When to Use

- **HANDOFF**: before a long pause, task/agent switch, context limit, major
  milestone, blocked state, or risky change; on user request. Skip for trivial
  queries or completed simple tasks.
- **RESUME**: new session continuing previous work, long interruption return,
  worker handoff, blocker resolved, or a user-requested restore of a specific
  handoff. Skip when current conversation + code state already suffice.

---

## Phase 1: HANDOFF

### 1. Pre-Save Snapshot
Verify: repository root, current branch, HEAD SHA, upstream/divergence,
staged/unstaged/untracked changes, merge/rebase/cherry-pick/bisect state,
submodule/worktree state when relevant. Non-Git repos: mark the Git section
`NOT_APPLICABLE` and record actual workspace state. Never save per-file diffs, summarize change purpose only.

### 2. Required Handoff Record (YAML)

```yaml
context:
  objective:              # final result the work should achieve
  acceptance_criteria:    # conditions for judging completion
  current_phase:          # which phase/step we are in
  completed:              # what is done
  in_progress:            # what is actively being worked on
  exact_resume_point:     # first file/symbol/test/command to open (NOT "continue implementation")
  remaining:              # what is left to do
  decisions:              # CONFIRMED/PROVISIONAL/REJECTED with brief rationale
  failed_attempts:        # approaches NOT to repeat, with failure evidence
  blockers:               # what prevents progress
  open_questions:         # unresolved items needing answer
  changed_areas:          # paths/components modified and why
  validation:             # only actually executed checks (command, result, exit code)
  next_command_or_action: # concrete next step
  risks:                  # known risks or constraints
```

### 3. Sensitive Data Rules
Never save: API keys, passwords, tokens, private keys, credentials, session
cookies, PII, production secrets, full sensitive logs, exploit payload
originals. Record env var names and necessity only (e.g., "DATABASE_URL
required; value not recorded").

### 4. Save Location
Default: `docs/context/context-handoff-YYYYMMDD-HHmm.md`. Rules: prefer repo
policy or user-specified path; don't overwrite existing files; use
conflict-free timestamps; don't auto-create dirs without checking permissions;
if sensitive/temp environment, don't save to repo; don't commit without a
separate request.

### 5. Handoff Output Format

```markdown
# Context Handoff
## Snapshot
- Saved at / Repository / Branch / HEAD / Upstream / Working tree / Operation in progress / Context status

## Objective
- Goal / Acceptance criteria

## Progress
### Completed
### In Progress
### Exact Resume Point (file/symbol/state/next action/next command)
### Remaining

## Decisions (Status | Decision | Rationale)
## Changed Areas (Path/Component | State | Purpose)
## Validation (Command/Method | Result | Freshness)
## Failed Attempts and Blockers
## Open Questions
## Risks and Constraints

## Resume Checklist
1. Confirm branch and HEAD.
2. Confirm working tree still matches snapshot.
3. Read exact resume target.
4. Re-run stale or missing validation.
5. Continue with listed next action.
```

Empty sections: mark `None` or omit. Never omit blockers, validation limits, or incomplete work.

### 6. Handoff Status

- **RESUMABLE**: next action + code state clear
- **RESUMABLE_WITH_WARNINGS**: resumable but stale validation or risks present
- **BLOCKED**: needs external decision/environment/access
- **INCOMPLETE**: failed to collect key state

RESUMABLE requires: clear objective/criteria, HEAD + working tree recorded,
exact resume point, completed/remaining separated, decisions/blockers
recorded, validation/unrun checks separated, no sensitive data.

---

## Phase 2: RESUME

### 1. Locate Candidate Handoffs
If the user specifies a file, use it. Otherwise search: `docs/context/`,
`docs/`, project-defined handoff directory. **Selection criteria**:
repository identity, task/feature, branch, HEAD/ancestor relationship,
timestamp, context status. Don't auto-select by newest timestamp alone, pick
the best match and record the rationale. Don't merge handoffs from different tasks.

### 2. Validate Handoff Integrity
Verify: repository, branch, HEAD, saved timestamp, objective, acceptance
criteria, exact resume point, remaining work, validation, blockers, context
status. Key items missing → `INCOMPLETE_SAVE`. Extract semantically even if
markdown structure differs; don't invent missing info. If secrets/credentials
are included, warn without re-displaying values.

### 3. Capture Current State and Compare
Verify current repository root, branch, HEAD SHA, upstream/divergence,
staged/unstaged/untracked changes, merge/rebase/cherry-pick/bisect state,
worktree/submodule when relevant, read-only. Then compare:

- Repository: same repo?
- Branch: same or intended switch?
- Revision: `EXACT` / `CURRENT_DESCENDS_FROM_SAVE` / `SAVE_DESCENDS_FROM_CURRENT` / `DIVERGED` / `UNRELATED` / `UNKNOWN`
- Working tree: do saved changes match current?
- Task: does the objective match the current request?
- Resume target: does the file/symbol still exist?
- Validation: still valid?

Don't treat a revision change alone as restore failure, check related-area
changes and resume-point validity.

### 4. Staleness Classification

Judge staleness by **state change, not time**. Mark a saved fact `STALE` when:
resume target file/symbol changed, objective/acceptance criteria changed,
dependency/lockfile changed, schema/migration changed, config changed, tests
changed, branch history changed, external blocker changed, generated code
changed, or a validation target changed. Time is a supplementary signal only:
old handoff + same code state = usable; recent handoff + different HEAD/working
tree = potentially stale.

Classify every saved fact: `VALID` (matches current code/commit/test
evidence), `STALE` (state changed), `CONFLICTING` (contradicts current
code/docs), `UNVERIFIED` (cannot be confirmed, including any completion claim
without current evidence), `NOT_APPLICABLE`. Keep completed items only when
they match current evidence; move unverifiable completion claims to
`UNVERIFIED`. Also restore failed approaches and prohibited retry attempts, and
invalidate stale test results (never carry a stale PASS forward).

### 5. Resume Point Verification and First Action

Re-verify the saved exact resume point: path exists, symbol exists, surrounding
code, current behavior, related test, next command exists, command is not
destructive, validation freshness. If the resume point disappeared, search for
the corresponding location in current code but don't assume it's the same.

**First action: one specific non-destructive task**, e.g., re-read target file
+ related test, re-run a stale regression test, or re-search changed API
consumers. Resume, do not replay: verify current state, then perform only the
needed next step.

### 6. Resume State Model

- **READY**: matches current, immediately resumable
- **READY_WITH_WARNINGS**: some stale/unverified but safe next step clear
- **NEEDS_RECONCILIATION**: branch/revision/working-tree conflict needs investigation
- **BLOCKED**: needs environment/access/decision
- **INCOMPLETE_SAVE**: handoff lacks key info
- **WRONG_CONTEXT**: handoff doesn't belong to current repo/task
- **NO_SAVE_FOUND**: no suitable handoff

READY requires: repo+task match, revision relationship confirmed, working-tree
differences understood, resume point valid, stale validation distinguished,
blockers + remaining clear, first safe action determined.

### 7. Confirmation Policy

Don't always require confirmation. Non-destructive resume can proceed after
reporting: file reading, search, existing test execution, current state
analysis, doc review. Require a decision for: branch switch,
reset/stash/merge/rebase, conflict resolution, a different handoff selection
that significantly changes outcome, acceptance-criteria conflict, destructive/
production action, or risk of overwriting another worker's changes.

### 8. Resume Output Format

```markdown
# Context Resume
## Status
**READY / READY_WITH_WARNINGS / NEEDS_RECONCILIATION / BLOCKED / INCOMPLETE_SAVE / WRONG_CONTEXT / NO_SAVE_FOUND**
## Selected Handoff
- Path / Saved at / Selection reason / Original status
## State Comparison
- Repository / Branch / Saved HEAD / Current HEAD / Revision relationship / Working-tree differences / Git operation
## Restored Objective
- Goal / Acceptance criteria
## Progress
- Valid completed / Stale or unverified / Remaining
## Resume Point
- File/symbol / Current validity / First safe action / Next command
## Decisions and Blockers
- Valid decisions / Conflicts / Open questions / Blockers
## Validation
- Still valid / Stale / Not run
## Warnings
## Final Statement (one of the 7 state options)
```

---

## Prohibited

- Selecting a handoff by newest timestamp alone; judging stale by a fixed
  age rule alone
- Vague "continue work" as the only resume point; recording a branch without HEAD
- Prioritizing the handoff document over current code; trusting completion
  claims without verification; keeping stale test results as PASS
- Auto-commit/stash on save; overwriting existing files; saving full
  diffs/conversations; saving secrets; marking incomplete items complete;
  marking unrun tests PASS; hiding failures
- Performing checkout/reset/stash/merge/rebase/conflict resolution during resume
- Merging handoffs from different tasks; guessing a disappeared symbol's current
  location; re-displaying secret values
- Always requiring user confirmation (blocks non-destructive investigation);
  auto-resuming implementation when state conflicts
- Confusing restore success with task completion

## Compact Protocol

```
HANDOFF: FIX OBJECTIVE AND ACCEPTANCE CRITERIA → CAPTURE REVISION AND WORKING
STATE → SEPARATE COMPLETED, IN-PROGRESS, AND REMAINING → RECORD DECISIONS,
FAILURES, BLOCKERS, AND UNKNOWNS → IDENTIFY THE EXACT RESUME POINT → RECORD
ONLY REAL VALIDATION AND ITS FRESHNESS → REDACT SENSITIVE DATA → WRITE A
NON-DESTRUCTIVE CHECKPOINT → VERIFY THE SAVED FILE

RESUME: LOCATE MATCHING HANDOFF → VALIDATE HANDOFF INTEGRITY → CAPTURE CURRENT
REPOSITORY STATE → COMPARE REPOSITORY, REVISION, WORKTREE, AND TASK → CLASSIFY
EACH SAVED FACT AS VALID, STALE, CONFLICTING, OR UNVERIFIED → VERIFY THE EXACT
RESUME POINT → INVALIDATE STALE TEST AND DECISION EVIDENCE → RECONSTRUCT ONLY
CURRENTLY SUPPORTED CONTEXT → SELECT ONE SAFE NEXT ACTION → REQUEST APPROVAL
ONLY FOR MUTATING OR AMBIGUOUS RECOVERY
```
