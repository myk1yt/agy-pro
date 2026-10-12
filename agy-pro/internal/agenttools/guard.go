package agenttools

import (
	"fmt"
	"os"
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
