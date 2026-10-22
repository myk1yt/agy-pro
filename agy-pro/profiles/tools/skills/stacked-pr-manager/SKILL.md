---
name: stacked-pr-manager
description: >
  Manages stacked PR dependency chains. Maps parent-child relationships, enforces root-first fix order, creates backups before rebases, and verifies CI after each rebase in the chain.
---

# Stacked PR Manager Skill

## When to Use
- User mentions "stacked PR", "PR 체인", "rebase children", "dependency chain"
- Multiple PRs have parent-child dependency relationships
- Need to update a chain of PRs after modifying a root PR

## Prerequisites
- GitHub CLI (`gh`) must be authenticated
- Git repository with stacked branch structure

## Workflow

### Step 1: Map PR Dependencies
Identify the dependency chain:
- Use `gh pr list` to find all open PRs
- Determine parent-child relationships by branch base:
  - PR-A: base=main, head=feature/a (ROOT)
  - PR-B: base=feature/a, head=feature/b (child of A)
  - PR-C: base=feature/b, head=feature/c (child of B)

### Step 2: Determine Fix Order
Always fix from ROOT to LEAF:
1. Fix PR-A first (root)
2. Then rebase PR-B onto updated feature/a
3. Then rebase PR-C onto updated feature/b

### Step 3: Fix Root PR
Apply fixes to the root PR branch.

### Step 4: Rebase Children
For each child PR (in order):
```bash
git checkout <child-branch>
git branch backup/<child-branch>-<YYYYMMDD>
git rebase <parent-branch>
git push --force-with-lease origin <child-branch>
```

### Step 5: Verify CI
For each PR in the chain, verify CI passes:
```bash
gh pr checks {PR번호}
```

## Core Rules
- **ROOT FIRST**: Always fix root PR before children
- **BACKUP BEFORE REBASE**: Create `backup/<branch>-<date>` before any rebase
- **FORCE-WITH-LEASE**: Use `--force-with-lease` NEVER `--force`
- **INDIVIDUAL CI CHECK**: Verify each PR's CI independently after rebase
- **SEQUENTIAL**: Process one PR at a time, verify before moving to next

## Output Format
| Order | PR | Branch | Parent Branch | CI Status | Rebase Status |
|-------|-----|--------|---------------|-----------|---------------|
