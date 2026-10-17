package agenttools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ResolveSafePath validates that target resolves to a location within root.
// It guards against directory traversal attacks (e.g. '../', absolute paths outside root,
// or symlinks pointing outside root).
func ResolveSafePath(root, target string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("failed to resolve root directory: %w", err)
	}

	var targetAbs string
	if filepath.IsAbs(target) {
		targetAbs = filepath.Clean(target)
	} else {
		targetAbs = filepath.Clean(filepath.Join(rootAbs, target))
	}

	// Traversal check via filepath.Rel
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path %q escapes root directory %q", target, rootAbs)
	}

	// Symlink escape check: evaluate existing path or closest existing ancestor
	checkPath := targetAbs
	for {
		if _, err := os.Lstat(checkPath); err == nil {
			eval, err := filepath.EvalSymlinks(checkPath)
			if err == nil {
				evalRel, err := filepath.Rel(rootAbs, eval)
				if err != nil || evalRel == ".." || strings.HasPrefix(evalRel, ".."+string(filepath.Separator)) {
					return "", fmt.Errorf("access denied: symlink %q resolves outside root directory: %q", target, eval)
				}
			}
			break
		}
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			break
		}
		checkPath = parent
	}

	return targetAbs, nil
}

// protectedWriteNames are VCS metadata path components that model-driven
// write tools must never touch (M-2 / house rule §5): a planted
// .git/hooks/pre-commit or a rewritten .git/config turns one prompt-injected
// write into code execution on the user's next git invocation. Keys are
// lowercase; comparison is case-insensitive so Windows case-variant aliases
// (".GIT") are covered on every host.
var protectedWriteNames = map[string]bool{
	".git":           true,
	".github":        true,
	".gitattributes": true,
	".gitconfig":     true,
	".gitignore":     true,
	".gitmodules":    true,
	".svn":           true,
	".hg":            true,
	".bzr":           true,
}

// refuseProtectedWrite rejects writes into VCS metadata relative to the
// workspace root. It runs AFTER ResolveSafePath (which already refuses
// symlink escapes outside the root); this layer closes the in-root part of
// the persistence chain. The check is component-based on the root-relative
// path, so protected names are refused at any depth ("sub/.git/config" too),
// while names that merely contain a protected substring ("docs/.github-notes.md")
// stay writable.
func refuseProtectedWrite(root, safePath string) error {
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("failed to resolve root directory: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, safePath)
	if err != nil {
		// Unrelatable to the root: fail closed.
		return fmt.Errorf("access denied: cannot verify %q is outside protected VCS metadata", safePath)
	}
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if protectedWriteNames[strings.ToLower(comp)] {
			return fmt.Errorf("access denied: %q is protected VCS metadata (%s); write_file/replace_in_file refuse it", filepath.ToSlash(rel), comp)
		}
	}
	return nil
}

// PathInScope reports whether the root-relative slash path rel is inside the
// unit scope patterns. It is the B3 per-unit working boundary for the
// path-reading tools (read_file / list_dir / grep / glob).
//
// Rule (deterministic, no "**"): the path is split into "/"-separated
// components and compared component by component against each pattern with
// path.Match semantics, which never crosses a separator. A pattern may be
// SHORTER than the path, in which case it matches as a directory prefix
// ("src/foo" covers "src/foo/bar.go", "src/*" covers "src/a/b.go" because
// "src/*" matches the ancestor "src/a"). A pattern longer than the path never
// matches. A pattern that normalizes to the root itself ("", ".", "/")
// matches every path under the root. A pattern with a malformed glob never
// matches (fail closed), and the caller must have resolved rel through
// ResolveSafePath first, so containment in root is already guaranteed.
func PathInScope(rel string, scope []string) bool {
	target := normalizeScopePath(rel)
	tc := strings.Split(target, "/")
	for _, pattern := range scope {
		pc := splitScopePattern(pattern)
		if len(pc) == 0 {
			return true // root-wide pattern
		}
		if len(pc) > len(tc) {
			continue
		}
		matched := true
		for i, c := range pc {
			ok, err := path.Match(c, tc[i])
			if err != nil || !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// resolvedPathInScope reports whether the symlink-resolved REAL path of target
// stays inside the unit scope (B3, FIX A2). The path-reading tools (os.Open,
// the WalkDir guard) FOLLOW symlinks, but ResolveSafePath returns the LEXICAL
// path and the scope check matches that, so a symlink planted inside a unit's
// scope that points at an out-of-scope in-root target would otherwise leak
// cross-unit content. Matching the resolved path closes that aliasing hole.
//
// Both root and target are resolved through EvalSymlinks so the two sides are
// compared in the same real-path space: a symlinked root (a temp dir on some
// hosts) would otherwise make filepath.Rel produce a bogus "../.." prefix. It
// fails closed on purpose: a resolution error, a target that is not relative to
// the root, or a resolved path outside the scope all return false and the caller
// refuses. root and target must already be absolute and root-contained (the
// caller passes ResolveSafePath output), and scope must be non-empty.
func resolvedPathInScope(root, target string, scope []string) bool {
	rel, err := resolvedRelPath(root, target)
	if err != nil {
		return false
	}
	return PathInScope(rel, scope)
}

// resolvedRelPath renders the symlink-resolved REAL path of target as a path
// relative to the symlink-resolved root: both sides are EvalSymlinks'd so the
// two are compared in the same real-path space (a symlinked root would
// otherwise make filepath.Rel produce a bogus "../.." prefix). It fails closed:
// a resolution error or a resolved path that escapes the root yields an error.
// root and target must already be absolute and root-contained.
func resolvedRelPath(root, target string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootReal, resolvedReal)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved real path escapes root: %q", rel)
	}
	return rel, nil
}

// ResolvedRelPath is the exported form of resolvedRelPath (D5): the partition
// merge's disjointness audit needs the resolved root-relative path itself (as
// the file-ownership claim key), not just the boolean scope verdict. The
// resolved claim test composes it with PathInScope, which is exactly the
// resolvedPathInScope test the per-seat read filter runs. Semantics are
// identical to the unexported original.
func ResolvedRelPath(root, target string) (string, error) {
	return resolvedRelPath(root, target)
}

// walkEntryInScope is the C1 per-entry unit-scope check for the Grep/Glob/
// ListDir directory walks. WalkDir hands each discovered entry to the walk
// callback as named under the walk root, and the path tools os.Open entries
// (following symlinks), so guardWalkFile's root-only validation is not enough
// for a scoped seat: a file symlink planted inside the unit's scope with a
// lexical in-scope name but an out-of-scope in-root target would be opened and
// the other unit's content leaked into the walk output. This check applies the
// SAME two tests the addressed path gets in runner.scopeRefusal - lexical
// PathInScope on the root-relative path AND resolvedPathInScope on the
// EvalSymlinks real path - and fails closed on any resolution error. A nil or
// empty scope returns true, leaving the walk byte-identical to the legacy
// nil-scope path (sandbox_test.go pins that behavior). rootAbs must be the
// cleaned absolute workspace root and p an absolute root-contained entry.
func walkEntryInScope(rootAbs, p string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	rel, err := filepath.Rel(rootAbs, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	if !PathInScope(rel, scope) {
		return false
	}
	return resolvedPathInScope(rootAbs, p, scope)
}

// splitScopePattern normalizes one scope entry and splits it into components.
// Backslashes become "/" (Windows-authored plans), the entry is cleaned and
// stripped of a leading "/", and an entry that denotes the root itself yields
// zero components (match-everything).
func splitScopePattern(pattern string) []string {
	s := normalizeScopePath(pattern)
	if s == "" || s == "." {
		return nil
	}
	return strings.Split(s, "/")
}

// normalizeScopePath renders a path or pattern in the canonical form the
// matcher compares: forward slashes, path.Clean'd, no leading "/".
func normalizeScopePath(s string) string {
	s = filepath.ToSlash(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "/")
	if s == "." {
		return ""
	}
	return s
}
