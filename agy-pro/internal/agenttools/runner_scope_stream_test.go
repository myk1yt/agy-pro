package agenttools

// FIX-E2b pins: the WRITE branch names the stream class instead of reporting a
// bare scope miss.
//
// Blind QA r4 row 2 filed the gap: a provider seat wrote `marker5/AGENTS.md:evil`
// (inside its own scope `marker5/`) and the tool result said only
// `path "marker5/AGENTS.md:evil" is outside unit scope`. The refusal was safe,
// but the reason class was invisible, because PathInScope refuses every arm for a
// stream-syntax component (guard.go) and this scope gate answers before the write
// guard's own by-name stream refusal ever runs. The fix states the shape on the
// write branch only: a colon target is not a scope verdict at all, it addresses a
// stream of another file.
//
// The shape arm is host-gated (win32StreamSyntaxRule), so the colon pins skip on
// a host where ':' is an ordinary filename character. The OTHER direction is not
// host-gated on purpose: a benign out-of-scope write and every scoped read must
// keep the plain refusal, which is what keeps the new text from swallowing the
// scope message it exists to complement.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// streamRefusalMarker is the phrase the new arm must contribute. It is asserted
// as a phrase, not as a whole sentence: the sentence also carries the scope
// framing and the refusing tool names.
const streamRefusalMarker = "Windows stream syntax not allowed in path components"

// A colon target inside the unit's own scope is refused with the named stream
// reason, for both write tools and for an intermediate component.
func TestScopedWriterNamesStreamSyntaxInsideScope(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host: ':' is an ordinary filename character here")
	}

	root := scopeRoot(t)
	runner := scopedWriter(t, root, "src/alpha")

	for _, tc := range []struct {
		name string
		tool string
		args string
		comp string
	}{
		{
			name: "write_file leaf",
			tool: "write_file",
			args: `{"path":"src/alpha/AGENTS.md:evil","content":"EVIL","overwrite":true}`,
			comp: "AGENTS.md:evil",
		},
		{
			name: "write_file nested new file",
			tool: "write_file",
			args: `{"path":"src/alpha/deep/notes.md:stream","content":"EVIL","overwrite":true}`,
			comp: "notes.md:stream",
		},
		{
			name: "write_file data stream",
			tool: "write_file",
			args: `{"path":"src/alpha/ok.go::$DATA","content":"EVIL","overwrite":true}`,
			comp: "ok.go::$DATA",
		},
		{
			name: "write_file stream-bearing intermediate component",
			tool: "write_file",
			args: `{"path":"src/alpha/x.md:stream/y.go","content":"EVIL","overwrite":true}`,
			comp: "x.md:stream",
		},
		{
			name: "replace_in_file leaf",
			tool: "replace_in_file",
			args: `{"path":"src/alpha/ok.go:evil","old_string":"src_alpha_ok","new_string":"hacked","replace_all":true}`,
			comp: "ok.go:evil",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Execute(1, "pin-model", "pin-provider", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute error = %v; the refusal must be error-shaped with a nil error", err)
			}
			if !strings.Contains(out, streamRefusalMarker) {
				t.Errorf("result = %q; want the named stream reason %q", out, streamRefusalMarker)
			}
			if !strings.Contains(out, tc.comp) {
				t.Errorf("result = %q; want the offending path component %q named", out, tc.comp)
			}
			// The scope framing stays: the call really is outside the boundary,
			// and the message complements the scope refusal instead of replacing
			// it (existing pins assert this phrase for the same shape).
			if !strings.Contains(out, "outside unit scope") {
				t.Errorf("result = %q; want the scope framing kept", out)
			}
			if !strings.Contains(out, "write_file/replace_in_file refuse it") {
				t.Errorf("result = %q; want the refusing tools named", out)
			}
		})
	}

	// Nothing landed: no base file, no aliased name under src/alpha.
	entries, err := os.ReadDir(filepath.Join(root, "src", "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "ok.go" {
			t.Errorf("a refused stream write created %q in the unit scope", e.Name())
		}
	}
}

// The new text fires ONLY on stream-syntax shapes: a benign out-of-scope write
// keeps the plain refusal, byte for byte.
func TestScopedWriterKeepsGenericRefusalForBenignOutOfScopeWrite(t *testing.T) {
	root := scopeRoot(t)
	runner := scopedWriter(t, root, "src/alpha")

	for _, tc := range []struct {
		name string
		tool string
		args string
		want string
	}{
		{
			name: "sibling unit",
			tool: "write_file",
			args: `{"path":"src/beta/new.go","content":"package x\n","overwrite":true}`,
			want: `Error: path "src/beta/new.go" is outside unit scope`,
		},
		{
			name: "loose root file",
			tool: "write_file",
			args: `{"path":"loose.go","content":"package x\n","overwrite":true}`,
			want: `Error: path "loose.go" is outside unit scope`,
		},
		{
			name: "replace_in_file sibling unit",
			tool: "replace_in_file",
			args: `{"path":"src/beta/no.go","old_string":"package","new_string":"hacked","replace_all":true}`,
			want: `Error: path "src/beta/no.go" is outside unit scope`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Execute(1, "pin-model", "pin-provider", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute error = %v; want nil", err)
			}
			if out != tc.want {
				t.Errorf("result = %q; want the plain scope refusal %q", out, tc.want)
			}
			if strings.Contains(out, streamRefusalMarker) {
				t.Errorf("result = %q; the stream reason must not fire on a benign path", out)
			}
		})
	}
}

// Reads and searches are unchanged: a stream-syntax target addressed by a
// scoped read keeps the generic scope refusal (the write branch is the only
// place the named stream reason exists).
func TestScopedReadKeepsGenericRefusalForStreamSyntax(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host")
	}

	root := scopeRoot(t)
	runner := NewRunner(root, false, false, nil)
	runner.Scope = []string{"src/alpha"}

	for _, tc := range []struct {
		name string
		tool string
		args string
		want string
	}{
		{
			name: "read_file",
			tool: "read_file",
			args: `{"path":"src/alpha/ok.go:evil"}`,
			want: `Error: path "src/alpha/ok.go:evil" is outside unit scope`,
		},
		{
			name: "grep",
			tool: "grep",
			args: `{"pattern":"package","path":"src/alpha/ok.go:evil"}`,
			want: `Error: path "src/alpha/ok.go:evil" is outside unit scope`,
		},
		{
			name: "glob",
			tool: "glob",
			args: `{"pattern":"*.go","path":"src/alpha/ok.go:evil"}`,
			want: `Error: path "src/alpha/ok.go:evil" is outside unit scope`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Execute(1, "pin-model", "pin-provider", tc.tool, tc.args)
			if err != nil {
				t.Fatalf("Execute error = %v; want nil", err)
			}
			if out != tc.want {
				t.Errorf("result = %q; want the unchanged scope refusal %q", out, tc.want)
			}
			if strings.Contains(out, streamRefusalMarker) {
				t.Errorf("result = %q; the write-branch stream reason leaked into a read", out)
			}
		})
	}
}

// The named stream refusal goes through the SAME E2 seeding as the plain one, so
// a key-shaped scope entry is masked in the model-visible result and in the tool
// log while the stream reason and the benign scope entry stay readable.
func TestScopedWriterStreamRefusalMasksKeyShapedScopeEntry(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host")
	}

	root := scopeRoot(t)
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, true, false, logger)
	runner.Scope = []string{"src/alpha", testfixtures.AWSSecretExample}

	out, err := runner.Execute(7, "pin-model", "pin-provider", "write_file",
		`{"path":"src/alpha/AGENTS.md:evil","content":"EVIL","overwrite":true}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil (the refusal is error-shaped)", err)
	}
	if !strings.Contains(out, streamRefusalMarker) {
		t.Errorf("result = %q; want the named stream reason", out)
	}
	if strings.Contains(out, testfixtures.AWSSecretExample) {
		t.Errorf("result leaks the raw key-shaped scope entry: %q", out)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), streamRefusalMarker) {
		t.Errorf("tool log lost the named stream reason:\n%s", data)
	}
	if strings.Contains(string(data), testfixtures.AWSSecretExample) {
		t.Errorf("tool log leaks the raw key-shaped scope entry:\n%s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", data)
	}
}

// The arm is scope-only: a nil-scope writer never enters scopeRefusal, so a
// colon target falls through to the write guard's own by-name stream refusal.
func TestNilScopeWriterStreamRefusalComesFromTheGuard(t *testing.T) {
	if !win32StreamSyntaxRule {
		t.Skip("the stream-syntax rule is off on this host")
	}

	root := scopeRoot(t)
	runner := NewRunner(root, true, false, nil)
	out, err := runner.Execute(1, "pin-model", "pin-provider", "write_file",
		`{"path":"src/alpha/AGENTS.md:evil","content":"EVIL","overwrite":true}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil", err)
	}
	if !strings.Contains(out, "access denied") || !strings.Contains(out, "alternate-data-stream") {
		t.Errorf("result = %q; want the write guard's own stream refusal", out)
	}
	if strings.Contains(out, "outside unit scope") {
		t.Errorf("result = %q; an unscoped runner must not report a scope verdict", out)
	}
}
