package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocs_NoNonexistentProvidersCommands is the C6 doc-claim lint. The
// security re-review's blind-QA caveat named `agy pro providers list`; this
// build has no top-level `providers` command (`Unknown subcommand:
// "providers"`), the real surface being `agy pro settings providers <verb>`
// (cmd/agy/pro_settings.go settingsProvidersCmd) and `agy pro models`. The
// same lint pass caught a doc claim of a `agy pro picker` command (also
// nonexistent). Every documented `agy pro settings providers <verb>` must be a
// verb the CLI implements, and the verb list is pinned against the source.
func TestDocs_NoNonexistentProvidersCommands(t *testing.T) {
	// Doc scope, relative to this package: agy-pro/README.md, agy-pro/docs/**,
	// and the repo-root manual.txt / README.ko.md.
	roots := []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs"),
		filepath.Join("..", "..", "..", "manual.txt"),
		filepath.Join("..", "..", "..", "README.ko.md"),
	}
	// The real providers verbs under `agy pro settings` (pro_settings.go).
	realVerbs := map[string]bool{
		"list": true, "add-json": true, "add-interactive": true,
		"test": true, "rename": true, "delete": true,
	}
	// Pin the verb list against the implementation so a CLI verb change fails
	// here until the docs and this map follow.
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agy", "pro_settings.go"))
	if err != nil {
		t.Fatalf("read cmd/agy/pro_settings.go: %v", err)
	}
	for verb := range realVerbs {
		if !strings.Contains(string(src), `case "`+verb+`"`) {
			t.Errorf("providers verb %q is not implemented in cmd/agy/pro_settings.go", verb)
		}
	}

	topLevelProviders := regexp.MustCompile(`(?i)agy\s+pro\s+providers\b`)
	pickerCmd := regexp.MustCompile(`(?i)agy\s+pro\s+picker\b`)
	settingsVerb := regexp.MustCompile(`(?i)agy\s+pro\s+settings\s+providers\s+([a-z-]+)`)

	scanned := 0
	lint := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		scanned++
		for i, line := range strings.Split(string(data), "\n") {
			if topLevelProviders.MatchString(line) {
				t.Errorf("%s:%d: claims the nonexistent top-level `agy pro providers` command (real surface: `agy pro settings providers ...` / `agy pro models`):\n  %s", path, i+1, strings.TrimSpace(line))
			}
			if pickerCmd.MatchString(line) {
				t.Errorf("%s:%d: claims the nonexistent `agy pro picker` command (real surface: `agy pro models --all` / launcher [2]):\n  %s", path, i+1, strings.TrimSpace(line))
			}
			for _, m := range settingsVerb.FindAllStringSubmatch(line, -1) {
				if !realVerbs[strings.ToLower(m[1])] {
					t.Errorf("%s:%d: documents providers verb %q the CLI does not implement (real verbs: list, add-json, add-interactive, test, rename, delete)", path, i+1, m[1])
				}
			}
		}
	}
	for _, root := range roots {
		fi, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", root, err)
		}
		if !fi.IsDir() {
			lint(root)
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			lint(filepath.Join(root, e.Name()))
		}
	}
	if scanned == 0 {
		t.Fatal("doc-claim lint scanned no files")
	}
}
