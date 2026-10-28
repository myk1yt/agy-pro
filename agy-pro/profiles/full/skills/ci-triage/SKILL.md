---
name: ci-triage
description: >
  Systematic CI failure triage and fix routing. Classifies failures by type (compile, lint, test, E2E), identifies root causes from logs, and routes fixes by complexity.
---

# CI Triage Skill

## When to Use
- User reports CI test failures ("CI가 안 돼요", "fix CI", "tests failing", "CI failure")
- Multiple PRs need CI verification
- Stacked PR dependency chain needs validation

## Prerequisites
- GitHub CLI (`gh`) must be authenticated
- Access to the repository's CI/CD pipeline

## Workflow

### Step 1: Scan
Run `gh pr checks {PR번호}` for all referenced PRs to get overall status.

### Step 2: Classify
Categorize each failure by type (fix priority order):
1. **Compile**, syntax errors, type mismatches, missing imports (HIGHEST priority)
2. **Lint**, formatting, style violations
3. **Test**, unit/integration test failures
4. **E2E**, end-to-end test failures, timeout issues (LOWEST priority)

### Step 3: Analyze
For each failure:
- Fetch failure logs: `gh run view {run-id} --log-failed`
- Identify root cause from log output
- Determine which file/function needs changes

### Step 4: Prioritize
Fix order: Compile → Lint → Test → E2E
Within each category, fix the root cause first (not symptoms).

### Step 5: Fix
Route the fix by complexity:
- **Lint fixes**, direct simple formatting/style changes
- **Compile fixes**, type errors, missing imports (implementation fix)
- **Test fixes**, logic changes, test updates (implementation fix)
- **E2E fixes**, complex timing/fixture issues (needs deeper debugging: timing, fixtures, shell integration)

### Step 6: Verify
After each fix, re-run CI checks to confirm resolution.

## Output Format
| PR | Branch | Failing Check | Root Cause | Fix Direction | Status |
|----|--------|---------------|------------|---------------|--------|

## E2E Failure Patterns (Quick Reference)
When analyzing E2E failures, check:
1. **Timeout**: Is it a shell integration timeout or API response timeout?
2. **Fixture**: Is it a "404 No fixture matched" issue?
3. **Compile**: Is it a pre-test compile/type error masquerading as E2E failure?
4. **Terminal Profile**: Do profile args block shell integration injection?
