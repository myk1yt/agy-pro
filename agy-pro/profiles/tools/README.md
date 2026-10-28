# tools/ — the integrations content snapshot (COMMITTED COPY, DO NOT EDIT BY HAND)

This directory is not a source. It is a byte-for-byte copy of the repository's four
content trees, captured so the integrations engine can deploy them from the agy-pro
binary's own embed instead of reading a runtime tree:

| Snapshot path | Repo source (two directories up from agy-pro/) |
|---|---|
| `tools/rules/`   | `../../rules/`   |
| `tools/skills/`  | `../../skills/`  |
| `tools/plugins/` | `../../plugins/` |
| `tools/hooks/`   | `../../hooks/`   |

The repo trees are the semantic authority (P5-3 pick record, item 10). This copy is
the deploy source. `tools_sync_test.go` walks both sides by relative path and SHA-256
and fails, naming every drifted pair, so the copy cannot quietly become a second
truth. That gate is the only reason the duplication is allowed to exist.

## Why a copy at all

- Hermetic deploys: the engine needs no toolkit-root resolution to find rules, skills
  or plugins. A repo move, a cloned-elsewhere install or a standalone agy-pro checkout
  cannot change what gets deployed.
- The engine stays content-agnostic: it is handed an `fs.FS`
  (`internal/integrations/content.go: OpenFeatureSource`), and this snapshot is what
  the CLI passes in.
- `hooks/hooks.json` is deployed from here too. The runtime `hooks/hooks.json` under
  the toolkit root remains only as a fallback for a caller that passes no source.

## Regenerating (no script file involved)

Edit the repo trees, then re-derive this directory from them:

```bash
cd agy-pro
AGY_TOOLS_SYNC_REGEN=1 go test ./profiles/ -run TestToolsSnapshotMatchesRepoTrees -count=1
go test ./profiles/ -run TestToolsSnapshotMatchesRepoTrees -count=1   # must now pass
```

Regeneration mode copies every repo file over the snapshot and removes snapshot files
whose repo source is gone, so the two sides are equal by construction afterwards. It
is opt-in through the environment variable only: a plain test run never writes. The
snapshot is a derived copy, so removing a stale entry loses nothing that the repo tree
still holds; the removed paths are printed.

A standalone `agy-pro` checkout (no `../../rules` and friends) skips the gate with a
message instead of failing: there is no sibling tree to compare against, which is not
a product failure.
