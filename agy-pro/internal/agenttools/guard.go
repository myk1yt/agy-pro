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
