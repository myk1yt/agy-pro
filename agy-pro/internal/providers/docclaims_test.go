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
	"model-add-json": true, "model-update": true, "model-delete": true,
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
	// and the repo-root manual.txt / README.ko.md / MANUAL.ko.md / README.md.
	// The last two were added in the SESSION7 fix round: the round-2 finding named
	// MANUAL.ko.md as sitting outside every doc-claim read root, and both files
	// were proven clean against this lint before the roots were widened
	// (TestDocs_NoNonexistentProvidersCommands PASS with both roots in place).
	roots := []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "..", "docs"),
		filepath.Join("..", "..", "..", "manual.txt"),
		filepath.Join("..", "..", "..", "README.ko.md"),
		filepath.Join("..", "..", "..", "MANUAL.ko.md"),
		filepath.Join("..", "..", "..", "README.md"),
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
					t.Errorf("%s:%d: documents providers verb %q the CLI does not implement (real verbs: list, add-json, add-interactive, wizard, test, rename, delete, model-add-json, model-update, model-delete)", path, i+1, m[1])
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

// docsWS collapses whitespace runs so a pin survives a legitimate re-wrap of the
// sentence and still fails on a wording regression.
func docsWS(s string) string { return strings.Join(strings.Fields(s), " ") }

// docsHasSentence reports whether want is present in doc modulo whitespace.
func docsHasSentence(doc, want string) bool {
	return strings.Contains(docsWS(doc), docsWS(want))
}

// TestDocs_ExclusionLoaderRuleMatchesBehavior pins the exclusion-list loader rule
// of agy-pro/README.md §10 to the single entry authority the loader runs
// (NormalizeExcludeModels -> excludeEntries -> checkExcludeEntry,
// internal/providers/providers.go:566-586 and :635-657). README.md is already one
// of the doc-claim read roots the lint above scans, so this pin widens no root; it
// adds the sentence-level claim the file never had (SESSION7 residual, round-2
// code seat P3 test-coverage) and pairs it with the behavior in the same test, so
// a pin that stops describing the code fails here.
func TestDocs_ExclusionLoaderRuleMatchesBehavior(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read agy-pro/README.md: %v", err)
	}
	doc := string(data)

	const loaderRule = "Entries are trimmed at load, an entry carrying interior whitespace or a control rune (the Cc or the Cf class) is refused, an individual entry that is empty after the trim is dropped, and a list whose every entry is blank is refused as a whole."
	if !docsHasSentence(doc, loaderRule) {
		t.Errorf("README.md §10 must document the loader rule verbatim; missing sentence:\n  %s", loaderRule)
	}
	const nilVsEmpty = "The key absent or null applies the built-in default entry `family:k3`; an explicit empty list (`[]`) excludes nothing."
	if !docsHasSentence(doc, nilVsEmpty) {
		t.Errorf("README.md §10 must document the absent-or-null vs explicit-empty rule; missing sentence:\n  %s", nilVsEmpty)
	}

	// Clause 1: entries are trimmed at load.
	trimmed, err := NormalizeExcludeModels([]string{"  nw  "})
	if err != nil || len(trimmed) != 1 || trimmed[0] != "nw" {
		t.Errorf("pinned sentence drifted from the loader: trim of %q = %#v, %v; want [nw] and no error", "  nw  ", trimmed, err)
	}
	// Clause 2: an individual blank entry is DROPPED (the loader, not the write
	// door, which refuses it: ValidateExcludeModels).
	dropped, err := NormalizeExcludeModels([]string{"nw", "", "   "})
	if err != nil || len(dropped) != 1 || dropped[0] != "nw" {
		t.Errorf("pinned sentence drifted from the loader: [\"nw\" \"\" \"   \"] = %#v, %v; want [nw] and no error", dropped, err)
	}
	if _, err := ValidateExcludeModels([]string{"nw", ""}); err == nil {
		t.Error("premise broken: the WRITE door must refuse a blank entry instead of dropping it")
	}
	// Clause 3 + 4: interior whitespace, Cc and Cf refused; an all-blank list
	// refused as a whole.
	for _, bad := range [][]string{
		{"nw glm"},   // interior whitespace
		{"nw\x00"},   // Cc
		{"nw\u202e"}, // Cf (right-to-left override)
		{"", "   "},  // every entry blank
	} {
		if _, err := NormalizeExcludeModels(bad); err == nil {
			t.Errorf("pinned sentence drifted from the loader: NormalizeExcludeModels(%q) accepted an entry shape the doc says is refused", bad)
		}
	}
	// The absent/null vs explicit-empty half: nil stays nil (built-in default
	// governs), an explicit empty list stays non-nil and excludes nothing.
	if got, err := NormalizeExcludeModels(nil); err != nil || got != nil {
		t.Errorf("NormalizeExcludeModels(nil) = %#v, %v; want nil, nil (absent key = built-in default)", got, err)
	}
	if got, err := NormalizeExcludeModels([]string{}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("NormalizeExcludeModels([]) = %#v, %v; want a non-nil empty list and no error", got, err)
	}

	// Non-vacuity: the predicate rejects a regressed copy in memory, so the pin
	// fails when the loader sentence regresses instead of matching anything.
	regressed := strings.Replace(doc, "an entry carrying interior whitespace", "an entry carrying trailing whitespace", 1)
	if docsHasSentence(regressed, loaderRule) {
		t.Error("pin is vacuous: it still matches after the loader sentence regresses")
	}
}
