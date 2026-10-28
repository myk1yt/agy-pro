package agenttools

// FIX-A1 (S2) pins: the Win32 canonical-form arm of the write guard.
//
// Finding S2: the governance denylist and the scope gate compared the path
// STRING the seat wrote, but Win32 resolves a different string. On a real
// Windows host `write_file("sub/AGENTS.md.")` CREATES `sub/AGENTS.md` (the
// trailing dot is stripped), so the denylist, which compared "AGENTS.md.", never
// fired; a DOS device name writes to a device instead of a file; an 8.3 short
// name (`PROVID~1.JSON`) can name a governance file the lexical match cannot
// see. These pins hold the write tools to the canonical form, and hold the
// exported predicates A2/B consume.
//
// Every case is a WRITE case: reads and searches stay byte-identical (the
// guard's read paths are unchanged, which TestCanonicalGuardLeavesReadsAlone
// pins by reading through the same names).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nonCanonicalWriteNames are refused by the component-hygiene arm ALONE: each
// one is a governance-denylisted or device target whose WRITTEN form differs
// from the form Win32 resolves, which is the whole S2 shape.
var nonCanonicalWriteNames = []string{
	// governance names with a Win32-stripped suffix
	"AGENTS.md.",
	"AGENTS.md ",
	"sub/AGENTS.md.",
	"sub/AGENTS.MD ",
	"AGENTS.md...",
	"docs/AGENTS.md. ",
	"providers.json.",
	"providers.json..",
	".agy-pro./providers.json",
	// a denylisted DIRECTORY component with a stripped suffix
	"rules./team-rules.md",
	"sub/.gemini./settings.json",
	// device names, with and without an extension, at the root and nested
	"NUL",
	"nul",
	"sub/nul.txt",
	"CON.log",
	"aux.go",
	"lpt9.json",
	"deep/nested/PRN",
	// components that Win32 strips to nothing
	"sub/..../x.txt",
	"sub/ /x.txt",
}

// TestWriteGuardRefusesNonCanonicalComponents is the S2 pin for both write
// tools: the write is refused by name (error-shaped, model-correctable) AND no
// file appears under either the written name or the canonical one.
func TestWriteGuardRefusesNonCanonicalComponents(t *testing.T) {
	root := t.TempDir()

	for _, rel := range nonCanonicalWriteNames {
		_, err := WriteFile(root, rel, "EVIL", true)
		if err == nil {
			t.Fatalf("WriteFile(%q): expected a canonical-form refusal, got nil (the file now exists as %q)",
				rel, strings.TrimRight(rel, ". "))
		}
		if !strings.Contains(err.Error(), "access denied") {
			t.Fatalf("WriteFile(%q): refusal is not error-shaped: %v", rel, err)
		}
		// The refusal must name the component, so the model can correct itself
		// instead of guessing which part of the path was wrong.
		if !strings.Contains(err.Error(), "path component") {
			t.Fatalf("WriteFile(%q): refusal does not name the offending component: %v", rel, err)
		}
		// Nothing landed, under the written name or under the canonical one.
		// ReadDir is the check, not Stat: a DOS device name ("NUL") always
		// stats successfully (it is a device, not a file), so only a real
		// directory entry proves something was created.
		assertNoWriteLanded(t, root, rel)
	}

	// replace_in_file takes the same guard, on an EXISTING governance file, and
	// must refuse before it reads or rewrites anything. The file is planted with
	// a non-canonical NAME so the case is the real bypass: the seat names the
	// file "team-rules.md.", which on Windows opens "team-rules.md".
	if err := os.MkdirAll(filepath.Join(root, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "rules", "team-rules.md")
	const original = "only the operator edits this\n"
	if err := os.WriteFile(existing, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"rules/team-rules.md.", "rules/team-rules.md ", "rules./team-rules.md"} {
		if _, err := ReplaceInFile(root, rel, "operator", "agent", false); err == nil {
			t.Fatalf("ReplaceInFile(%q): expected a canonical-form refusal, got nil", rel)
		} else if !strings.Contains(err.Error(), "access denied") {
			t.Fatalf("ReplaceInFile(%q): unexpected error: %v", rel, err)
		}
	}
	body, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("a refused replace_in_file modified the file: %q", string(body))
	}
}

// assertNoWriteLanded fails when a directory entry exists for rel or for its
// canonically trimmed form, under the given root. It reads the DIRECTORY (not
// Stat) because a DOS device name stats successfully on every host with or
// without a file behind it.
func assertNoWriteLanded(t *testing.T, root, rel string) {
	t.Helper()
	for _, cand := range []string{rel, strings.TrimRight(rel, ". "), strings.TrimRight(rel, ". ") + "."} {
		cand = filepath.ToSlash(cand)
		dir := filepath.Join(root, filepath.FromSlash(pathOrDot(cand)))
		base := cand
		if i := strings.LastIndexByte(cand, '/'); i >= 0 {
			base = cand[i+1:]
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // the directory does not exist: nothing landed there
		}
		for _, e := range entries {
			if strings.EqualFold(e.Name(), base) {
				t.Fatalf("WriteFile(%q) created %q despite the refusal", rel, filepath.ToSlash(filepath.Join(pathOrDot(cand), e.Name())))
			}
		}
	}
}

// pathOrDot is the directory part of a slash path, "." for a bare filename.
func pathOrDot(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return filepath.FromSlash(rel[:i])
	}
	return "."
}

// TestCanonicalGuardLeavesBenignWritesAlone is the non-vacuity arm: the arm above
// must not be a blanket refusal. Ordinary names, look-alike names the denylist
// deliberately keeps writable, and dotted extensions all still write.
func TestCanonicalGuardLeavesBenignWritesAlone(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"sub/notes.md",
		"docs/agents-notes.md",
		"docs/providers.json.md",
		"config/app.yaml.bak",
		"a.b.c/d.e.txt",
		"internal/auxiliary/helpers.go",
		"console.go",
	} {
		if _, err := WriteFile(root, rel, "ok", true); err != nil {
			t.Fatalf("WriteFile(%q): benign write refused: %v", rel, err)
		}
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr != nil {
			t.Fatalf("WriteFile(%q): reported success but the file is absent: %v", rel, statErr)
		}
	}
}

// TestCanonicalGuardLeavesReadsAlone pins the "reads and searches unaffected"
// half of the fix: the read tools keep working on governance and look-alike
// names, because the canonical-form arm lives in the write guard only.
func TestCanonicalGuardLeavesReadsAlone(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rules", "team-rules.md"), []byte("rule body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFile(root, "rules/team-rules.md", 0, 0)
	if err != nil {
		t.Fatalf("ReadFile on a governance file must stay unaffected: %v", err)
	}
	if !strings.Contains(out, "rule body") {
		t.Fatalf("ReadFile returned %q", out)
	}
	if _, err := Grep(root, "rule body", "rules", false, 10); err != nil {
		t.Fatalf("Grep on a governance directory must stay unaffected: %v", err)
	}
}

// TestGovernanceRefusalMatchesCanonicalComponents pins sub-rule 2: the denylist
// comparison runs over canonical components, so a trailing dot or space and any
// case mix are the same target.
func TestGovernanceRefusalMatchesCanonicalComponents(t *testing.T) {
	governed := []string{
		"AGENTS.md", "agents.md", "AGENTS.MD", "Agents.Md",
		"AGENTS.md.", "AGENTS.md ", "AGENTS.MD..",
		"sub/AGENTS.md.", "deep/nested/AGENTS.md ",
		"providers.json.", "providers.json.tmp ", "rules.", ".gemini ", ".agy-pro.",
		"state.json ", "settings.json.  ",
	}
	for _, rel := range governed {
		if comp, refuse := GovernanceWriteRefusal(rel); !refuse {
			t.Errorf("GovernanceWriteRefusal(%q) = (%q, false); want a refusal", rel, comp)
		} else if comp == "" {
			t.Errorf("GovernanceWriteRefusal(%q) named no component", rel)
		}
	}

	clear := []string{
		"docs/agents-notes.md",
		"docs/providers.json.md",
		"internal/provider/registry.go",
		"rulesnotes.md",
		"agents.md.go",
		"",
	}
	for _, rel := range clear {
		if comp, refuse := GovernanceWriteRefusal(rel); refuse {
			t.Errorf("GovernanceWriteRefusal(%q) refused via %q; want clear", rel, comp)
		}
	}
}

// TestCanonicalPathComponentOK pins the exported predicate A2/B consume: it
// reports the bad component and false, and ("", true) for a canonical path.
func TestCanonicalPathComponentOK(t *testing.T) {
	bad := map[string]string{
		"AGENTS.md.":            "AGENTS.md.",
		"sub/AGENTS.md ":        "AGENTS.md ",
		"rules./team-rules.md":  "rules.",
		"sub/nul.txt":           "nul.txt",
		"NUL":                   "NUL",
		"..":                    "..",
		"sub/../x.txt":          "..",
		"sub/..../x.txt":        "....",
		"deep/nested/lpt1.json": "lpt1.json",
	}
	for rel, want := range bad {
		comp, ok := CanonicalPathComponentOK(rel)
		if ok {
			t.Errorf("CanonicalPathComponentOK(%q) = (%q, true); want false", rel, comp)
			continue
		}
		if comp != want {
			t.Errorf("CanonicalPathComponentOK(%q) named %q; want %q", rel, comp, want)
		}
	}

	for _, rel := range []string{
		"", "notes.md", "src/alpha/ok.go", "docs/agents-notes.md",
		"config/app.yaml.bak", "a.b.c/d.e.txt", "internal/auxiliary/helpers.go",
		"reports/code/261024_0001/summary.json",
	} {
		if comp, ok := CanonicalPathComponentOK(rel); !ok {
			t.Errorf("CanonicalPathComponentOK(%q) = (%q, false); want true", rel, comp)
		}
	}
}

// TestPathInScopeCanonicalizesTarget pins the scope-gate half: the matcher
// compares the components the OS resolves, so a decorated component cannot be
// read as an in-scope one by a wildcard, and the control cases stay unchanged.
func TestPathInScopeCanonicalizesTarget(t *testing.T) {
	cases := []struct {
		rel   string
		scope []string
		want  bool
		why   string
	}{
		// Windows strips ".. " to nothing, so this target is "src/outside/x.txt"
		// (measured, see scratch/fix-a1). It must be matched as that path, not as
		// an arbitrary component that "*" happens to accept.
		{"src/.. /.. /outside/x.txt", []string{"src/*"}, true, "decorated components vanish; the landing is in scope"},
		{"src/.. /.. /outside/x.txt", []string{"other/*"}, false, "the landing is not in the other unit's scope"},
		// A trailing slash-space inside a component the wildcard would have taken.
		{"src/notes.md.", []string{"src/*.md"}, true, "canonical form is src/notes.md"},
		{"src/notes.md.", []string{"src/*.txt"}, false, "canonical form is still a .md"},
		// Exact ".." still traverses and still reads as out of scope.
		{"../etc/passwd", []string{"src/*"}, false, "a real traversal is never in scope"},
		// Controls: ordinary paths are untouched (the pre-canonical answers).
		{"src/alpha/ok.go", []string{"src/*"}, true, "control"},
		{"src/alpha/ok.go", []string{"*.go"}, false, "control: * never crosses a separator"},
	}
	for _, tc := range cases {
		if got := PathInScope(tc.rel, tc.scope); got != tc.want {
			t.Errorf("PathInScope(%q, %q) = %v; want %v (%s)", tc.rel, tc.scope, got, tc.want, tc.why)
		}
	}
}

// TestScopedRunnerRefusesNonCanonicalWrite pins the end-to-end partition path:
// a scoped write unit cannot reach a governance file through a canonical-form
// alias, and the refusal is model-visible (a tool result, not a crash).
func TestScopedRunnerRefusesNonCanonicalWrite(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"src", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := NewRunner(root, true, false, nil)
	runner.Scope = []string{"src/*"}

	for _, rel := range []string{
		"src/AGENTS.md.",
		"src/AGENTS.md ",
		"src/nul.txt",
	} {
		args := `{"path":"` + rel + `","content":"EVIL","overwrite":true}`
		out, err := runner.Execute(1, "pin-model", "pin-provider", "write_file", args)
		if err != nil {
			t.Fatalf("Execute(%q) returned a transport error: %v", rel, err)
		}
		if !strings.Contains(out, "access denied") {
			t.Fatalf("Execute(%q) = %q; want an error-shaped refusal", rel, out)
		}
		if _, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(strings.TrimRight(rel, ". ")))); statErr == nil {
			t.Fatalf("Execute(%q) created the canonical target", rel)
		}
	}
}
