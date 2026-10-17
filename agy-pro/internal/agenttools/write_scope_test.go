package agenttools

// B4-1 pins: the per-unit write boundary.
//
// A scoped ToolRunner whose AllowWrite flag is set (the per-unit write
// opt-in) accepts write_file / replace_in_file addressed INSIDE the unit
// scope, including brand-new files whose closest existing ancestor must be
// resolved to build the symlink-resolved view. The same runner refuses an
// out-of-scope write with the error-shaped refusal (nil Go error, the seat
// continues), under the SAME dual-view rule as the reads: BOTH the lexical
// path AND the resolved real path must stay inside the scope, so a symlink
// alias in either direction cannot smuggle a write across units. A scoped
// runner WITHOUT the write flag keeps refusing writes through the existing
// "write flag not enabled" prohibition, and a nil-Scope runner is unchanged.
//
// Every pin here is mutation-verified: disabling the write guard in
// scopeRefusal (dropping the scopedWriteTools check) fails the out-of-scope
// refusal pins, and building the resolved view with a plain EvalSymlinks
// (no closest-existing-ancestor walk) fails the new-file pin on hosts where
// the temp root itself is symlinked or the file does not exist yet.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopedWriter builds the B4-1 unit seat: write enabled, scope bounded.
func scopedWriter(t *testing.T, root string, scope ...string) *ToolRunner {
	t.Helper()
	r := NewRunner(root, true, false, nil)
	r.Scope = scope
	return r
}

// An in-scope write succeeds through a scoped writer runner: write_file
// creates a brand-new file inside the scope (the resolved view is built from
// the closest existing ancestor because the target does not exist yet), and
// replace_in_file edits an existing in-scope file.
func TestToolRunnerScopedWriterAcceptsInScopeWrite(t *testing.T) {
	root := scopeRoot(t)
	runner := scopedWriter(t, root, "src/alpha")

	out, err := runner.Execute(1, "m1", "p1", "write_file",
		`{"path":"src/alpha/new.go","content":"package newfile\n","overwrite":true}`)
	if err != nil {
		t.Fatalf("in-scope write_file error = %v; want nil", err)
	}
	if strings.Contains(out, "outside unit scope") || strings.Contains(out, "Error") {
		t.Fatalf("in-scope write_file refused: %q", out)
	}
	data, rerr := os.ReadFile(filepath.Join(root, "src", "alpha", "new.go"))
	if rerr != nil {
		t.Fatalf("the in-scope write did not create the file: %v", rerr)
	}
	if string(data) != "package newfile\n" {
		t.Fatalf("file content = %q; want the written bytes", data)
	}

	// A nested new file (deeper nonexistent tail) writes too.
	out, err = runner.Execute(1, "m1", "p1", "write_file",
		`{"path":"src/alpha/deep/er/new.go","content":"package deep\n","overwrite":true}`)
	if err != nil || strings.Contains(out, "Error") {
		t.Fatalf("nested in-scope write_file = %q, %v; want success", out, err)
	}

	// replace_in_file on an existing in-scope file works end to end.
	out, err = runner.Execute(1, "m1", "p1", "replace_in_file",
		`{"path":"src/alpha/ok.go","old_string":"src_alpha_ok.go","new_string":"edited","replace_all":true}`)
	if err != nil || strings.Contains(out, "Error") {
		t.Fatalf("in-scope replace_in_file = %q, %v; want success", out, err)
	}
	data, rerr = os.ReadFile(filepath.Join(root, "src", "alpha", "ok.go"))
	if rerr != nil || !strings.Contains(string(data), "edited") {
		t.Fatalf("the in-scope replace did not apply: content=%q err=%v", data, rerr)
	}
}

// An out-of-scope write is refused error-shaped: the model sees the refusal
// as the tool result, the Go error is nil, and nothing is written.
func TestToolRunnerScopedWriterRefusesOutOfScopeWrite(t *testing.T) {
	root := scopeRoot(t)
	runner := scopedWriter(t, root, "src/alpha")

	for _, tc := range []struct {
		name string
		tool string
		args string
	}{
		{
			name: "write_file sibling unit",
			tool: "write_file",
			args: `{"path":"src/beta/new.go","content":"package x\n","overwrite":true}`,
		},
		{
			name: "write_file loose root file",
			tool: "write_file",
			args: `{"path":"loose.go","content":"package x\n","overwrite":true}`,
		},
		{
			name: "replace_in_file sibling unit",
			tool: "replace_in_file",
			args: `{"path":"src/beta/no.go","old_string":"package","new_string":"hacked","replace_all":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Execute(1, "m1", "p1", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute error = %v; the refusal must be error-shaped with a nil error", err)
			}
			if !strings.Contains(out, "outside unit scope") {
				t.Errorf("result = %q; want the scope refusal", out)
			}
		})
	}

	// Nothing may have been created or modified outside the scope.
	if _, err := os.Stat(filepath.Join(root, "src", "beta", "new.go")); !os.IsNotExist(err) {
		t.Errorf("the refused write created src/beta/new.go anyway: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "src", "beta", "no.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "package src_beta_no.go") {
		t.Errorf("the refused replace modified src/beta/no.go:\n%s", data)
	}
	data, lerr := os.ReadFile(filepath.Join(root, "loose.go"))
	if lerr != nil || string(data) != "package loose.go\n" {
		t.Errorf("the refused write touched loose.go: content=%q err=%v", data, lerr)
	}
}

// Dual-view symlink pins, both directions. Writes (unlike reads) create and
// modify content, so an in-scope-looking alias that resolves into another
// unit would let a seat overwrite the other unit's files, and an
// out-of-scope-looking alias that resolves INTO this unit would let a seat
// launder a write through a foreign path name. Both must be refused.
func TestToolRunnerScopedWriterRefusesSymlinkAlias(t *testing.T) {
	root := scopeRoot(t)
	runner := scopedWriter(t, root, "src/alpha")

	// Direction 1: lexical IN, resolved OUT. The lexical name lives inside
	// the scope but the real target belongs to the other unit.
	inOut := filepath.Join(root, "src", "alpha", "alias.go")
	makeSymlink(t, filepath.Join(root, "src", "beta", "no.go"), inOut)

	out, err := runner.Execute(1, "m1", "p1", "replace_in_file",
		`{"path":"src/alpha/alias.go","old_string":"package","new_string":"hacked","replace_all":true}`)
	if err != nil {
		t.Fatalf("replace through in-scope alias returned error %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("write through lexical-in/resolved-out alias was not refused: %q", out)
	}
	data, rerr := os.ReadFile(filepath.Join(root, "src", "beta", "no.go"))
	if rerr != nil {
		t.Fatalf("fixture unreadable: %v", rerr)
	}
	if !strings.Contains(string(data), "package") || strings.Contains(string(data), "hacked") {
		t.Errorf("the refused alias write modified the real target:\n%s", data)
	}

	// Direction 1b: a NEW file written through a symlinked directory whose
	// lexical path is in scope but which resolves into the other unit.
	inOutDir := filepath.Join(root, "src", "alpha", "linkdir")
	makeSymlink(t, filepath.Join(root, "src", "beta"), inOutDir)
	out, err = runner.Execute(1, "m1", "p1", "write_file",
		`{"path":"src/alpha/linkdir/via_link.go","content":"package via\n","overwrite":true}`)
	if err != nil {
		t.Fatalf("write through symlinked dir returned error %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("new file through lexical-in/resolved-out dir was not refused: %q", out)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "beta", "via_link.go")); !os.IsNotExist(serr) {
		t.Errorf("the refused dir-alias write landed in the other unit: %v", serr)
	}

	// Direction 2: lexical OUT, resolved IN. The name lives outside the
	// scope but the real target is this unit's file; the AND rule refuses
	// because the lexical view is out of scope.
	outIn := filepath.Join(root, "src", "beta", "backdoor.go")
	makeSymlink(t, filepath.Join(root, "src", "alpha", "ok.go"), outIn)

	out, err = runner.Execute(1, "m1", "p1", "replace_in_file",
		`{"path":"src/beta/backdoor.go","old_string":"src_alpha_ok.go","new_string":"hacked","replace_all":true}`)
	if err != nil {
		t.Fatalf("replace through out-scope alias returned error %v; want nil", err)
	}
	if !strings.Contains(out, "outside unit scope") {
		t.Errorf("write through lexical-out/resolved-in alias was not refused: %q", out)
	}
	data, rerr = os.ReadFile(filepath.Join(root, "src", "alpha", "ok.go"))
	if rerr != nil || strings.Contains(string(data), "hacked") {
		t.Errorf("the refused alias write modified the in-scope real target: %q\n%s", rerr, data)
	}
}

// Default off: a scoped runner WITHOUT the write flag still refuses
// write_file / replace_in_file through the existing prohibition (the write
// opt-in is per unit; a scope alone never enables writes).
func TestToolRunnerScopedReadOnlyRefusesWrite(t *testing.T) {
	root := scopeRoot(t)
	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	for _, tc := range []struct {
		tool string
		args string
	}{
		{"write_file", `{"path":"src/alpha/new.go","content":"package x\n","overwrite":true}`},
		{"replace_in_file", `{"path":"src/alpha/ok.go","old_string":"package","new_string":"hacked","replace_all":true}`},
	} {
		out, err := runner.Execute(1, "m1", "p1", tc.tool, tc.args)
		if err != nil {
			t.Fatalf("%s error = %v; want nil (the prohibition is error-shaped)", tc.tool, err)
		}
		if !strings.Contains(out, "prohibited") || !strings.Contains(out, "write flag not enabled") {
			t.Errorf("%s = %q; want the write-flag prohibition", tc.tool, out)
		}
	}

	// Neither call may have written anything, in scope or out.
	if _, err := os.Stat(filepath.Join(root, "src", "alpha", "new.go")); !os.IsNotExist(err) {
		t.Errorf("the prohibited write created a file anyway: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "src", "alpha", "ok.go"))
	if err != nil || strings.Contains(string(data), "hacked") {
		t.Errorf("the prohibited replace modified the file: %q\n%s", err, data)
	}
}

// Nil-Scope runners are unchanged: a writer runner without a scope writes
// anywhere in the workspace (the guard only exists for partition units), and
// a read-only runner without a scope keeps the legacy prohibition.
func TestToolRunnerNilScopeWriteUnchanged(t *testing.T) {
	root := scopeRoot(t)

	writer := NewRunner(root, true, false, nil)
	out, err := writer.Execute(1, "m1", "p1", "write_file",
		`{"path":"src/beta/anywhere.go","content":"package anywhere\n","overwrite":true}`)
	if err != nil || strings.Contains(out, "Error") {
		t.Fatalf("nil-scope writer must keep the legacy behavior, got %q, %v", out, err)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "beta", "anywhere.go")); serr != nil {
		t.Errorf("nil-scope writer did not write: %v", serr)
	}

	readonly := NewRunner(root, false, false, nil)
	out, err = readonly.Execute(1, "m1", "p1", "write_file",
		`{"path":"src/beta/readonly.go","content":"package x\n","overwrite":true}`)
	if err != nil || !strings.Contains(out, "prohibited") {
		t.Fatalf("nil-scope read-only runner must keep the prohibition, got %q, %v", out, err)
	}
	if _, serr := os.Stat(filepath.Join(root, "src", "beta", "readonly.go")); !os.IsNotExist(serr) {
		t.Errorf("the prohibited write created a file anyway: %v", serr)
	}
}
