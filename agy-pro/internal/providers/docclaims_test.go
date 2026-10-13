package providers

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// realProvidersVerbs are the verbs `agy pro settings providers` implements
// (cmd/agy/pro_settings.go). `wizard` is an alias of add-interactive sharing
// its case clause (pro_settings.go:146 `case "add-interactive", "wizard":`),
// so a doc may legitimately claim `agy pro settings providers wizard` (D5).
var realProvidersVerbs = map[string]bool{
	"list": true, "add-json": true, "add-interactive": true,
	"test": true, "rename": true, "delete": true, "wizard": true,
}

// settingsProvidersVerbRe captures the verb of a documented
// `agy pro settings providers <verb>` claim.
var settingsProvidersVerbRe = regexp.MustCompile(`(?i)agy\s+pro\s+settings\s+providers\s+([a-z-]+)`)

// providersVerbImplemented reports whether verb is implemented in
// cmd/agy/pro_settings.go, either directly (`case "list":`) or as an alias
// sharing a case clause (`case "add-interactive", "wizard":`).
func providersVerbImplemented(src, verb string) bool {
	return strings.Contains(src, `case "`+verb+`"`) || strings.Contains(src, `, "`+verb+`"`)
}

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
	// Pin the verb list against the implementation so a CLI verb change fails
	// here until the docs and this map follow.
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agy", "pro_settings.go"))
	if err != nil {
		t.Fatalf("read cmd/agy/pro_settings.go: %v", err)
	}
	for verb := range realProvidersVerbs {
		if !providersVerbImplemented(string(src), verb) {
			t.Errorf("providers verb %q is not implemented in cmd/agy/pro_settings.go", verb)
		}
	}

	topLevelProviders := regexp.MustCompile(`(?i)agy\s+pro\s+providers\b`)
	pickerCmd := regexp.MustCompile(`(?i)agy\s+pro\s+picker\b`)

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
			for _, m := range settingsProvidersVerbRe.FindAllStringSubmatch(line, -1) {
				if !realProvidersVerbs[strings.ToLower(m[1])] {
					t.Errorf("%s:%d: documents providers verb %q the CLI does not implement (real verbs: list, add-json, add-interactive, wizard, test, rename, delete)", path, i+1, m[1])
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

// TestProvidersVerbs_WizardAliasAllowed pins the D5 ruling: `wizard` is an
// implemented alias of add-interactive (pro_settings.go:146
// `case "add-interactive", "wizard":`), so the doc lint must accept a
// `agy pro settings providers wizard` claim while a verb the CLI does not
// implement stays rejected.
func TestProvidersVerbs_WizardAliasAllowed(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "agy", "pro_settings.go"))
	if err != nil {
		t.Fatalf("read cmd/agy/pro_settings.go: %v", err)
	}
	if !realProvidersVerbs["wizard"] {
		t.Error("wizard alias must be an allowed providers verb (D5)")
	}
	if !providersVerbImplemented(string(src), "wizard") {
		t.Error("wizard alias is not implemented in cmd/agy/pro_settings.go; drop it from realProvidersVerbs")
	}
	for _, line := range []string{
		"agy pro settings providers wizard",
		"agy pro settings providers add-interactive",
		"agy pro settings providers list",
	} {
		for _, m := range settingsProvidersVerbRe.FindAllStringSubmatch(line, -1) {
			if !realProvidersVerbs[strings.ToLower(m[1])] {
				t.Errorf("doc line %q: implemented verb %q rejected by the lint", line, m[1])
			}
		}
	}
	for _, m := range settingsProvidersVerbRe.FindAllStringSubmatch("agy pro settings providers frobnicate", -1) {
		if realProvidersVerbs[strings.ToLower(m[1])] {
			t.Error("nonexistent providers verb must stay rejected")
		}
	}
}
