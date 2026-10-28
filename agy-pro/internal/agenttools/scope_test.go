package agenttools

// B3 pins: the per-unit working boundary.
//
// A scoped ToolRunner (Scope non-nil) refuses the path-reading tools when the
// resolved path matches none of the unit's scope patterns, and returns the
// refusal as an error-shaped tool result with a nil error, exactly like the
// model-tools refusal. A nil Scope must leave the dispatch byte-identical to
// the pre-change runner, so non-partition runs are unaffected.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeRoot builds a workspace with one in-scope file, one out-of-scope file
// and one loose root file.
func scopeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		filepath.Join("src", "alpha", "ok.go"),
		filepath.Join("src", "beta", "no.go"),
		"loose.go",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		body := "package " + strings.ReplaceAll(rel, string(filepath.Separator), "_") + "\n"
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// A scoped runner refuses every path-reading tool that addresses a path outside
// the unit scope, and lets the in-scope call proceed.
func TestToolRunnerScopeRefusesOutOfScopeReads(t *testing.T) {
	root := scopeRoot(t)
	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	cases := []struct {
		name    string
		tool    string
		args    string
		wantOut string // substring the result must carry
		wantNot string // substring the result must NOT carry ("" = unchecked)
	}{
		{
			name: "read_file out of scope", tool: "read_file", args: `{"path":"src/beta/no.go"}`,
			wantOut: "outside unit scope", wantNot: "package src_beta_no.go",
		},
		{
			name: "read_file in scope", tool: "read_file", args: `{"path":"src/alpha/ok.go"}`,
			wantOut: "package src_alpha_ok.go", wantNot: "outside unit scope",
		},
		{
			name: "read_file loose root file", tool: "read_file", args: `{"path":"loose.go"}`,
			wantOut: "outside unit scope", wantNot: "package loose.go",
		},
		{
			name: "read_file the scope dir itself", tool: "read_file", args: `{"path":"src/alpha"}`,
			wantNot: "outside unit scope",
		},
		{
			name: "list_dir out of scope", tool: "list_dir", args: `{"path":"src/beta"}`,
			wantOut: "outside unit scope", wantNot: "no.go",
		},
		{
			name: "list_dir in scope", tool: "list_dir", args: `{"path":"src/alpha"}`,
			wantOut: "ok.go", wantNot: "outside unit scope",
		},
		{
			name: "grep out of scope", tool: "grep", args: `{"pattern":"package","path":"src/beta"}`,
			wantOut: "outside unit scope", wantNot: "no.go",
		},
		{
			name: "grep in scope", tool: "grep", args: `{"pattern":"package","path":"src/alpha"}`,
			wantOut: "ok.go", wantNot: "outside unit scope",
		},
		{
			name: "glob out of scope", tool: "glob", args: `{"pattern":"*.go","path":"src/beta"}`,
			wantOut: "outside unit scope", wantNot: "no.go",
		},
		{
			name: "glob in scope", tool: "glob", args: `{"pattern":"*.go","path":"src/alpha"}`,
			wantOut: "ok.go", wantNot: "outside unit scope",
		},
		{
			name: "sibling directory is out of scope", tool: "list_dir", args: `{"path":"src"}`,
			wantOut: "outside unit scope",
		},
		{
			// Traversal is already refused by ResolveSafePath inside the tool;
			// the scope layer must not swallow or double-report it.
			name: "traversal stays the tool's own refusal", tool: "read_file", args: `{"path":"../escape.go"}`,
			wantNot: "outside unit scope",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Execute(1, "m1", "p1", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute returned error %v; the refusal must be error-shaped with a nil error", err)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("result = %q; want it to contain %q", out, tc.wantOut)
			}
			if tc.wantNot != "" && strings.Contains(out, tc.wantNot) {
				t.Errorf("result = %q; must not contain %q", out, tc.wantNot)
			}
		})
	}
}

// The refusal is error-shaped AND logged, so the seat's audit trail records why
// a read was denied.
func TestToolRunnerScopeRefusalIsLogged(t *testing.T) {
	root := scopeRoot(t)
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.Scope = []string{"src/alpha"}

	out, err := runner.Execute(7, "model-x", "prov-x", "read_file", `{"path":"src/beta/no.go"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Fatalf("result = %q; want the scope refusal", out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "outside unit scope") {
		t.Errorf("the scope refusal was not logged:\n%s", data)
	}
}

// A nil Scope leaves the dispatch byte-identical to the pre-change runner: the
// same out-of-scope path partition mode refuses returns the file content, and
// NewRunner does not invent a scope.
func TestToolRunnerNilScopeBehavesAsBefore(t *testing.T) {
	root := scopeRoot(t)
	runner := NewRunner(root, false, false, nil)
	if runner.Scope != nil {
		t.Fatalf("NewRunner set Scope = %v; want nil (non-partition runs must be unaffected)", runner.Scope)
	}

	out, err := runner.Execute(1, "m1", "p1", "read_file", `{"path":"src/beta/no.go"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil", err)
	}
	if strings.Contains(out, "outside unit scope") {
		t.Errorf("an unscoped runner refused a read: %q", out)
	}
	if !strings.Contains(out, "package src_beta_no.go") {
		t.Errorf("unscoped read = %q; want the file content", out)
	}

	// The other read tools keep their pre-change results too.
	for _, tc := range []struct{ tool, args, want string }{
		{"list_dir", `{"path":"src/beta"}`, "no.go"},
		{"grep", `{"pattern":"package","path":"src/beta"}`, "no.go"},
		{"glob", `{"pattern":"*.go","path":"src/beta"}`, "no.go"},
	} {
		out, err := runner.Execute(1, "m1", "p1", tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s error = %v; want nil", tc.tool, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s = %q; want it to contain %q", tc.tool, out, tc.want)
		}
	}

	// An empty (non-nil, zero-length) scope is the same as nil: no filter.
	empty := NewRunner(root, false, false, nil)
	empty.Scope = []string{}
	out, err = empty.Execute(1, "m1", "p1", "read_file", `{"path":"loose.go"}`)
	if err != nil || !strings.Contains(out, "package loose.go") {
		t.Errorf("empty scope changed the read result: out=%q err=%v", out, err)
	}
}

// B4-1: the scope boundary now covers the write tools too. A scoped runner
// refuses write_file / replace_in_file addressed at a path outside the unit
// scope (the same dual-view rule as the reads, seeded through the same
// refuseOutOfScope machinery), while exec tools stay outside the scope filter
// (partition mode never enables them; the exec gate itself is pinned in
// tools_test.go). This test used to pin the opposite held state ("the scope
// filter must not reach write tools"); the write-confinement decision (B4-1)
// flipped that held state, so the pin now asserts the refusal.
func TestToolRunnerScopeRefusesOutOfScopeWritesLeavesExecAlone(t *testing.T) {
	root := scopeRoot(t)
	runner := NewRunner(root, true, false, nil) // write enabled
	runner.Scope = []string{"src/alpha"}

	// write_file outside the unit scope is refused by the scope filter, and
	// the file must not be created.
	out, err := runner.Execute(1, "m1", "p1", "write_file", `{"path":"src/beta/new.go","content":"x"}`)
	if err != nil {
		t.Fatalf("write_file error = %v; want nil (the refusal is error-shaped)", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("the scope filter did not reach the out-of-scope write: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(root, "src", "beta", "new.go")); !os.IsNotExist(statErr) {
		t.Errorf("the refused write created the file anyway: %v", statErr)
	}

	// replace_in_file outside the unit scope is refused too, and the target
	// stays untouched.
	out, err = runner.Execute(1, "m1", "p1", "replace_in_file",
		`{"path":"src/beta/no.go","old_string":"package","new_string":"hacked","replace_all":true}`)
	if err != nil {
		t.Fatalf("replace_in_file error = %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("the scope filter did not reach the out-of-scope replace: %q", out)
	}
	data, rerr := os.ReadFile(filepath.Join(root, "src", "beta", "no.go"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(data), "package") {
		t.Errorf("the refused replace modified the file:\n%s", data)
	}

	// An in-scope write still proceeds (the guard does not over-refuse): the
	// file lands inside the unit scope.
	out, err = runner.Execute(1, "m1", "p1", "write_file", `{"path":"src/alpha/new.go","content":"package new_in_scope\n","overwrite":true}`)
	if err != nil {
		t.Fatalf("in-scope write_file error = %v; want nil", err)
	}
	if strings.Contains(out, "outside unit scope") || strings.Contains(out, "Error") {
		t.Errorf("the in-scope write was refused: %q", out)
	}

	// Exec tools stay outside the scope filter: a scoped runner WITHOUT exec
	// must get the exec gate's own "prohibited" refusal, never a scope
	// refusal (the scope layer must not start policing run_command).
	noexec := NewRunner(root, false, false, nil)
	noexec.Scope = []string{"src/alpha"}
	out, err = noexec.Execute(1, "m1", "p1", "run_command", `{"command":"echo","args":["hi"]}`)
	if err != nil {
		t.Fatalf("run_command error = %v; want nil", err)
	}
	if strings.Contains(out, "outside unit scope") {
		t.Errorf("the scope filter reached an exec tool: %q", out)
	}
	if !strings.Contains(out, "prohibited") {
		t.Errorf("the exec gate refusal changed: %q", out)
	}
}

// PathInScope is the documented matching rule: component-wise path.Match with
// no "**", a shorter pattern matching as a directory prefix, and a root-wide
// pattern matching everything.
func TestPathInScope(t *testing.T) {
	cases := []struct {
		rel   string
		scope []string
		want  bool
	}{
		// exact and directory-prefix matches
		{"src/alpha", []string{"src/alpha"}, true},
		{"src/alpha/ok.go", []string{"src/alpha"}, true},
		{"src/alpha/deep/er/ok.go", []string{"src/alpha"}, true},
		{"src/alpha/ok.go", []string{"src/alpha/ok.go"}, true},
		{"src/alphax/ok.go", []string{"src/alpha"}, false},
		{"src/alpha", []string{"src/alpha/ok.go"}, false}, // pattern deeper than the path
		{"src/beta/no.go", []string{"src/alpha"}, false},

		// glob components (path.Match semantics, never crossing a separator)
		{"src/alpha/ok.go", []string{"src/*"}, true},
		{"src/alpha/deep/ok.go", []string{"src/*"}, true}, // "src/*" matches the ancestor "src/alpha"
		{"src/alpha/ok.go", []string{"src/alpha/*.go"}, true},
		{"src/alpha/ok.go", []string{"src/*/ok.go"}, true},
		{"src/alpha/ok.go", []string{"src/[a-z]*"}, true},
		{"src/alpha/ok.go", []string{"src/al*"}, true},
		{"src/alpha/ok.go", []string{"src/be*"}, false},
		{"loose.go", []string{"*.go"}, true},
		{"src/alpha/ok.go", []string{"*.go"}, false}, // "*" never crosses a separator
		{"src/alpha/ok.go", []string{"**/ok.go"}, false},

		// root-wide and normalization
		{"src/alpha/ok.go", []string{"."}, true},
		{"src/alpha/ok.go", []string{""}, true},
		{"src/alpha/ok.go", []string{"/"}, true},
		{"src/alpha/ok.go", []string{"src/alpha/"}, true},  // trailing slash cleaned
		{"src/alpha/ok.go", []string{"src\\alpha"}, true},  // Windows-authored entry
		{"src/alpha/ok.go", []string{" src/alpha "}, true}, // surrounding space trimmed
		{"src/alpha/ok.go", []string{"/src/alpha"}, true},  // leading slash stripped
		{"src/alpha/ok.go", []string{"src/./alpha"}, true}, // cleaned
		{"src/alpha/ok.go", []string{"src//alpha"}, true},  // doubled separator cleaned

		// fail closed on a malformed pattern
		{"src/alpha/ok.go", []string{"src/["}, false},

		// any pattern in the list may match
		{"src/alpha/ok.go", []string{"src/beta", "other", "src/alpha"}, true},
		{"src/gamma/ok.go", []string{"src/beta", "other", "src/alpha"}, false},
	}
	for _, tc := range cases {
		got := PathInScope(tc.rel, tc.scope)
		if got != tc.want {
			t.Errorf("PathInScope(%q, %q) = %v; want %v", tc.rel, tc.scope, got, tc.want)
		}
	}
}

// The matcher is deterministic: the same inputs always answer the same way, and
// scope order never changes the answer.
func TestPathInScopeDeterministic(t *testing.T) {
	scope := []string{"src/beta", "src/alpha"}
	first := PathInScope("src/alpha/ok.go", scope)
	for i := 0; i < 50; i++ {
		if PathInScope("src/alpha/ok.go", scope) != first {
			t.Fatal("PathInScope is not deterministic")
		}
	}
	reversed := []string{"src/alpha", "src/beta"}
	if PathInScope("src/alpha/ok.go", reversed) != first {
		t.Error("scope order changed the answer")
	}
	if PathInScope("src/beta/no.go", reversed) != true {
		t.Error("the second entry of a reversed scope stopped matching")
	}
}

// FIX A1 regression pin: Grep/Glob/ListDir default a blank/omitted path to the
// workspace root "." (their schemas mark path optional), so a unit scoped to a
// subdirectory used to read the whole workspace by simply omitting "path". The
// scope layer must resolve that omission to "." and refuse it like an explicit
// ".", returning the error-shaped refusal with a nil error and never surfacing
// the out-of-scope unit content.
func TestToolRunnerScopeRefusesOmittedPath(t *testing.T) {
	root := scopeRoot(t)
	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	for _, tc := range []struct {
		tool string
		args string
	}{
		{"grep", `{"pattern":"package"}`},
		{"glob", `{"pattern":"*.go"}`},
		{"list_dir", `{}`},
		{"read_file", `{}`},
	} {
		t.Run(tc.tool+" omitted path", func(t *testing.T) {
			out, err := runner.Execute(1, "m1", "p1", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("%s with no path returned error %v; the refusal must be error-shaped with a nil error", tc.tool, err)
			}
			if !strings.Contains(out, "outside unit scope") {
				t.Errorf("%s with no path = %q; want the scope refusal", tc.tool, out)
			}
			// The whole-workspace read must not happen: the out-of-scope unit
			// content (src/beta/no.go and loose.go) must never surface.
			for _, leak := range []string{"no.go", "loose.go", "src_beta", "src_loose"} {
				if strings.Contains(out, leak) {
					t.Errorf("%s with no path leaked out-of-scope content %q: %q", tc.tool, leak, out)
				}
			}
		})
	}
}

// FIX A2 regression pin: the path tools FOLLOW symlinks, but ResolveSafePath
// returns the lexical path. A symlink planted inside a unit's scope that points
// at an out-of-scope in-root target used to read cross-unit content with no
// refusal. The scope layer must now also match the symlink-resolved real path
// and refuse, while an in-scope real file still reads normally.
func TestToolRunnerScopeRefusesInRootSymlinkAlias(t *testing.T) {
	root := scopeRoot(t)
	// Plant an in-scope symlink whose lexical path (src/alpha/notes) is inside
	// the scope but whose real target (src/beta) is out of scope and in-root.
	link := filepath.Join(root, "src", "alpha", "notes")
	if err := os.Symlink(filepath.Join(root, "src", "beta"), link); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}

	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	// Reading THROUGH the in-scope alias must be refused, not followed into the
	// out-of-scope unit.
	out, err := runner.Execute(1, "m1", "p1", "read_file", `{"path":"src/alpha/notes/no.go"}`)
	if err != nil {
		t.Fatalf("read through symlink returned error %v; the refusal must be error-shaped with a nil error", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("read through in-scope symlink = %q; want the scope refusal", out)
	}
	if strings.Contains(out, "src_beta_no.go") {
		t.Errorf("read through in-scope symlink leaked cross-unit content: %q", out)
	}

	// list_dir through the alias is refused too (its WalkDir would follow it).
	out, err = runner.Execute(1, "m1", "p1", "list_dir", `{"path":"src/alpha/notes"}`)
	if err != nil {
		t.Fatalf("list_dir through symlink returned error %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("list_dir through in-scope symlink = %q; want the scope refusal", out)
	}

	// An in-scope REAL file still reads normally (the fix does not over-refuse).
	out, err = runner.Execute(1, "m1", "p1", "read_file", `{"path":"src/alpha/ok.go"}`)
	if err != nil {
		t.Fatalf("in-scope read returned error %v; want nil", err)
	}
	if strings.Contains(out, "outside unit scope") || !strings.Contains(out, "package src_alpha_ok.go") {
		t.Errorf("in-scope real file read = %q; want the file content with no refusal", out)
	}
}

// FIX C1 regression pin: grep/glob/list_dir WALK the addressed directory and
// open (grep) or report (glob/list_dir) every discovered entry. The walk-entry
// guard (guardWalkFile) validated each entry only against the workspace ROOT,
// never the unit scope, so an in-scope file symlink whose lexical name is
// inside the scope but whose real target belongs to another unit
// (src/alpha/leak.go -> ../beta/no.go) was auto-followed by os.Open and the
// other unit's content leaked into the walk output. The walk now applies the
// SAME two tests the addressed path gets (lexical PathInScope AND
// resolvedPathInScope) to EVERY discovered entry BEFORE it is opened; refused
// entries are skipped and counted under "outside unit scope", while in-scope
// real files still return.
func TestToolRunnerScopeSkipsOutOfScopeWalkEntries(t *testing.T) {
	root := scopeRoot(t)
	// Plant an in-scope symlink whose lexical path (src/alpha/leak.go) is
	// inside the scope but whose real target (src/beta/no.go) is another
	// unit's file. Lexically it passes; only the resolved check refuses it.
	target := filepath.Join(root, "src", "beta", "no.go")
	link := filepath.Join(root, "src", "alpha", "leak.go")
	makeSymlink(t, target, link)

	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	// grep must not read through the link, must read the real in-scope file,
	// and must report the skip instead of leaving a silent hole.
	out, err := runner.Execute(1, "m1", "p1", "grep", `{"pattern":"package","path":"src/alpha"}`)
	if err != nil {
		t.Fatalf("grep error = %v; want nil", err)
	}
	if strings.Contains(out, "src_beta_no.go") {
		t.Errorf("grep leaked cross-unit content through the walk: %q", out)
	}
	if !strings.Contains(out, "src_alpha_ok.go") {
		t.Errorf("grep must still return the in-scope real file: %q", out)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("grep must report the skipped out-of-scope entry: %q", out)
	}

	// glob must not NAME the out-of-scope target (its lexical name is in
	// scope; only the resolved real path is out), and must name the real file.
	out, err = runner.Execute(1, "m1", "p1", "glob", `{"pattern":"*.go","path":"src/alpha"}`)
	if err != nil {
		t.Fatalf("glob error = %v; want nil", err)
	}
	if strings.Contains(out, "leak.go") {
		t.Errorf("glob named the out-of-scope symlink target: %q", out)
	}
	if !strings.Contains(out, "src/alpha/ok.go") {
		t.Errorf("glob must still return the in-scope real file: %q", out)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("glob must report the skipped out-of-scope entry: %q", out)
	}

	// list_dir must not enumerate the out-of-scope entry.
	out, err = runner.Execute(1, "m1", "p1", "list_dir", `{"path":"src/alpha"}`)
	if err != nil {
		t.Fatalf("list_dir error = %v; want nil", err)
	}
	if strings.Contains(out, "leak.go") {
		t.Errorf("list_dir enumerated the out-of-scope symlink: %q", out)
	}
	if !strings.Contains(out, "ok.go") {
		t.Errorf("list_dir must still enumerate the in-scope real file: %q", out)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("list_dir must report the skipped out-of-scope entry: %q", out)
	}
}

// FIX C1 companion pin: the walk-level check must NOT over-refuse. An in-scope
// symlink whose resolved target is ALSO in scope is followed and read, exactly
// like the legacy nil-scope behavior for in-root links.
func TestToolRunnerScopeKeepsInScopeWalkEntries(t *testing.T) {
	root := scopeRoot(t)
	real := filepath.Join(root, "src", "alpha", "deep.go")
	if err := os.WriteFile(real, []byte("package deep_in_scope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "src", "alpha", "alias.go")
	makeSymlink(t, real, link)

	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	out, err := runner.Execute(1, "m1", "p1", "grep", `{"pattern":"deep_in_scope","path":"src/alpha"}`)
	if err != nil {
		t.Fatalf("grep error = %v; want nil", err)
	}
	if !strings.Contains(out, "deep_in_scope") {
		t.Errorf("grep through an in-scope symlink must still work: %q", out)
	}
	if strings.Contains(out, "outside unit scope") {
		t.Errorf("grep skipped an in-scope entry: %q", out)
	}
}
