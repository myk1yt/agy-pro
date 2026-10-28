package agenttools

// FIX-D1a (R3-2) pins: the Win32 alternate-data-stream separator.
//
// Finding R3-2 as filed: "AGENTS.md:stream" and "::$DATA" pass every predicate,
// GetLongPathNameW returns 0 on a colon path so the 8.3 expansion arm early
// returns nil (=allowed), and every write funnels through os.Rename with zero
// colon rules.
//
// Measured on this host (probe harness scratch/fix-d1a, table in
// coder-report-fix-d1a.md): the predicates really did say "clear", and the
// platform really does express the shape - a DIRECT os.WriteFile to
// "x.md::$DATA" replaces x.md's content - while the shipped temp+rename path
// fails LATE with a raw Win32 error ("The filename, directory name, or volume
// label syntax is incorrect", ERROR_INVALID_NAME from MoveFileExW). So the
// bypass was real at the predicate layer and unreachable through the rename
// layer; the fix makes the layer that decides answer by name, instead of relying
// on a rename failure that leaks an opaque error to the model.
//
// The refusal is host-gated (win32StreamSyntaxRule), so these pins skip on a
// host where a colon is an ordinary filename character.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHasWin32StreamSyntax pins the platform-neutral predicate D1b consumes.
func TestHasWin32StreamSyntax(t *testing.T) {
	yes := []string{"AGENTS.md:evil", "name::$DATA", ":", "a:b:c"}
	for _, comp := range yes {
		if !HasWin32StreamSyntax(comp) {
			t.Errorf("HasWin32StreamSyntax(%q) = false; want true", comp)
		}
	}
	no := []string{"", "AGENTS.md", "notes-2026.md", "C", "a$DATA"}
	for _, comp := range no {
		if HasWin32StreamSyntax(comp) {
			t.Errorf("HasWin32StreamSyntax(%q) = true; want false", comp)
		}
	}
	// streamBaseOf is the name the OS actually opens.
	for in, want := range map[string]string{
		"AGENTS.md:evil":   "AGENTS.md",
		"AGENTS.md::$DATA": "AGENTS.md",
		"AGENTS.md":        "AGENTS.md",
	} {
		if got := streamBaseOf(in); got != want {
			t.Errorf("streamBaseOf(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestPredicatesRefuseStreamSyntax pins the three exported predicates D1b
// consumes: a stream-suffixed component is governed (the base is what the OS
// opens), VCS-protected, and not a canonical path component.
func TestPredicatesRefuseStreamSyntax(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host: ':' is an ordinary filename character here")
	}

	governed := []string{"AGENTS.md:evil", "AGENTS.md::$DATA", "state.json:1", "sub/ſtate.json:evil"}
	for _, rel := range governed {
		if comp, refuse := GovernanceWriteRefusal(rel); !refuse {
			t.Errorf("GovernanceWriteRefusal(%q) = (%q, false); want a refusal", rel, comp)
		}
	}

	vcs := []string{".git:evil", ".git/config::$DATA", "sub/.github:1"}
	for _, rel := range vcs {
		if comp, refuse := VCSMetadataRefusal(rel); !refuse {
			t.Errorf("VCSMetadataRefusal(%q) = (%q, false); want a refusal", rel, comp)
		}
	}

	bad := []string{"AGENTS.md:evil", "src/notes.md:stream", "sub/x::$DATA"}
	for _, rel := range bad {
		if comp, ok := CanonicalPathComponentOK(rel); ok {
			t.Errorf("CanonicalPathComponentOK(%q) = (%q, true); want false", rel, comp)
		}
	}

	// Non-vacuity: names without the separator stay clear.
	for _, rel := range []string{"src/notes.md", "docs/agents-notes.md", "notes-2026.md"} {
		if _, refuse := GovernanceWriteRefusal(rel); refuse {
			t.Errorf("GovernanceWriteRefusal(%q) refused a name with no stream separator", rel)
		}
		if comp, ok := CanonicalPathComponentOK(rel); !ok {
			t.Errorf("CanonicalPathComponentOK(%q) = (%q, false); want true", rel, comp)
		}
	}
}

// TestWriteGuardRefusesStreamSyntax pins both write tools: the refusal is
// name-shaped and nothing lands, under either the written name or the file the
// stream would have addressed.
func TestWriteGuardRefusesStreamSyntax(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host")
	}
	root := t.TempDir()

	// The governance/VCS arms run BEFORE the shape scan (refuseProtectedWrite
	// order), so a stream-suffixed governed name is refused by whichever arm owns
	// the base name - and a stream-suffixed ordinary name by the stream arm. Both
	// are name-shaped refusals; what matters is that neither is allowed and
	// neither leaves an entry behind.
	cases := map[string]string{
		"sub/AGENTS.md:evil":   "alternate-data-stream",
		"sub/AGENTS.md::$DATA": "alternate-data-stream",
		"AGENTS.md:evil":       "alternate-data-stream", // the shape scan runs before the governance arm
		"notes:2026.md":        "alternate-data-stream",
		"sub/.git:evil":        "protected VCS metadata", // the VCS arm runs before the shape scan
	}
	for rel, want := range cases {
		_, err := WriteFile(root, rel, "EVIL", true)
		if err == nil {
			t.Fatalf("WriteFile(%q): expected a named refusal, got nil", rel)
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("WriteFile(%q): refusal %q does not contain %q", rel, err, want)
		}
		// No file may appear under the base name either: the stream suffix must
		// never be silently dropped.
		base := streamBaseOf(filepath.ToSlash(rel))
		if _, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(base))); statErr == nil {
			t.Fatalf("WriteFile(%q) created %q despite the refusal", rel, base)
		}
		fi, readErr := os.ReadDir(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))))
		if readErr == nil {
			for _, e := range fi {
				if strings.Contains(e.Name(), ":") {
					t.Fatalf("WriteFile(%q) created the stream-named entry %q", rel, e.Name())
				}
			}
		}
	}

	// replace_in_file: the second write tool must refuse before it reads or
	// rewrites, leaving an existing governed file untouched.
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	governed := filepath.Join(root, "sub", "AGENTS.md")
	const original = "only the operator edits this\n"
	if err := os.WriteFile(governed, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"sub/AGENTS.md:evil", "sub/AGENTS.md::$DATA"} {
		if _, err := ReplaceInFile(root, rel, "operator", "seat", false); err == nil {
			t.Fatalf("ReplaceInFile(%q): expected the stream-syntax refusal, got nil", rel)
		} else if !strings.Contains(err.Error(), "alternate-data-stream") {
			t.Fatalf("ReplaceInFile(%q): unexpected error: %v", rel, err)
		}
	}
	body, err := os.ReadFile(governed)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Fatalf("a refused stream write modified the governed file: %q", string(body))
	}
}

// TestPathInScopeRefusesStreamSyntax pins the scope half of R3-2: no pattern can
// bound a name the OS resolves as a stream, so the target is out of scope and the
// scoped runner refuses the call as a tool result (not a transport error).
func TestPathInScopeRefusesStreamSyntax(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host")
	}

	if PathInScope("src/AGENTS.md:evil", []string{"src/*"}) {
		t.Error("PathInScope matched a stream-syntax target against src/*")
	}
	if PathInScope("src/x.md:stream/y.go", []string{"src/*"}) {
		t.Error("PathInScope matched a path with a stream-bearing intermediate component")
	}
	// Control: the same target without the separator is in scope, so the arm is
	// not a blanket refusal.
	if !PathInScope("src/AGENTS.md", []string{"src/*"}) {
		t.Error("PathInScope refused the stream-free control target")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(root, true, false, nil)
	runner.Scope = []string{"src/*"}
	out, err := runner.Execute(1, "pin-model", "pin-provider", "write_file",
		`{"path":"src/AGENTS.md:evil","content":"EVIL","overwrite":true}`)
	if err != nil {
		t.Fatalf("Execute returned a transport error: %v", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Fatalf("scoped Execute = %q; want the scope refusal", out)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "src"))
	if len(entries) != 0 {
		t.Fatalf("scoped Execute created entries despite the refusal: %v", entries)
	}
}
