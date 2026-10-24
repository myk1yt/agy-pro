package agenttools

// F11 pin: the governance/credential denylist. Seats that hold
// write_file / replace_in_file must never be able to edit the files that
// configure the agent system itself (agent rules, provider registry, per-run
// state, integration settings, and the directories agy-pro / agy read them
// from), in ANY mode: the legacy run-wide --tools write path and the
// partition-unit scoped-write path share the same tool functions, so both are
// refused by the guard these pins exercise. Reads and searches on the same
// paths stay untouched.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// governanceDeniedNames is the exact denylist this pin holds the guard to:
// basenames refused at any depth, at the root and nested, in any case.
var governanceDeniedNames = []string{
	"agents.md",
	"gemini.md",
	"claude.md",
	"providers.json",
	"providers.json.bak",
	"providers.json.tmp",
	"providers.json.2026-01-01",
	"state.json",
	"state-fanout.json",
	"fanout-presets.json",
	"integrations.json",
	"settings.json",
	"rules/team-rules.md",
	"profiles/tools/rules/registry.json",
	".agy-pro/providers.json",
	".gemini/config/settings.json",
}

// The denylist is refused at the root, nested at depth, and with mixed case,
// by BOTH write tools, and no file is created or modified by the attempt.
func TestWriteGuardRefusesGovernanceNames(t *testing.T) {
	root := t.TempDir()

	cases := []string{}
	for _, rel := range governanceDeniedNames {
		cases = append(cases, rel, "deep/nested/dir/"+rel, strings.ToUpper(rel))
	}
	// Mixed case rather than all-caps only: Windows aliasing is
	// case-insensitive, so the guard must lowercase the component it sees.
	cases = append(cases, "Sub/Rules/Team-Rules.md", "docs/AGENTS.Md", "Docs/Providers.JSON")

	for _, rel := range cases {
		if _, err := WriteFile(root, rel, "evil", true); err == nil {
			t.Fatalf("WriteFile(%q): expected governance refusal, got nil", rel)
		} else if !strings.Contains(err.Error(), "access denied") || !strings.Contains(err.Error(), "governance/credential file") {
			t.Fatalf("WriteFile(%q): unexpected error: %v", rel, err)
		}
		// The target is pre-created so a refusal cannot be mistaken for the
		// "file already exists" path, then re-read to prove it is untouched.
		target := filepath.Join(root, filepath.FromSlash(rel))
		if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
			t.Fatalf("denied write created %s", rel)
		}
	}

	// replace_in_file on an EXISTING governance file must refuse before it
	// reads or rewrites anything (no "old_string not found" style error).
	existing := filepath.Join(root, "rules", "team-rules.md")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	const original = "only the operator edits this\n"
	if err := os.WriteFile(existing, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceInFile(root, "rules/team-rules.md", "operator", "agent", false); err == nil {
		t.Fatal("ReplaceInFile: expected governance refusal, got nil")
	} else if !strings.Contains(err.Error(), "governance/credential file") {
		t.Fatalf("ReplaceInFile: unexpected error: %v", err)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != original {
		t.Fatalf("governance file content changed: %q (%v)", data, err)
	}
}

// Names that merely LOOK like the denylist stay writable: no substring and no
// prefix matching beyond the documented providers.json sibling arm.
func TestGovernanceGuardLeavesLookAlikesWritable(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"agents-notes.md",
		"docs/providers.json.md",
		"docs/providers.json.txt",
		"docs/agents.md.md",
		"my-agents.md",
		"rules-notes/plan.md",
		"src/rulesdoc/x.go",
		"settings.jsonc",
		"state.json5",
		"agentsmd/notes.md",
		".gemini-notes/x.md",
	} {
		if _, err := WriteFile(root, rel, "fine", true); err != nil {
			t.Fatalf("look-alike %q wrongly refused: %v", rel, err)
		}
	}
	// The sibling arm is limited to the provider registry, and the doc
	// extensions keep the prose copy writable.
	if _, err := ReplaceInFile(root, "docs/providers.json.md", "fine", "still fine", false); err != nil {
		t.Fatalf("prose copy of the registry wrongly refused: %v", err)
	}
}

// Reads, listings, greps and globs on the SAME paths still work: the denylist
// is a write-tool rule only.
func TestGovernanceGuardDoesNotAffectReadTools(t *testing.T) {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"agents.md":                  "operator rules\n",
		"rules/team-rules.md":        "rule body needle\n",
		"config/settings.json":       `{"tool":"x"}`,
		".agy-pro/state.json":        `{"run":"y"}`,
		"deep/dir/state-fanout.json": `{"fanout":"z"}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := ReadFile(root, "agents.md", 0, 0); err != nil || !strings.Contains(out, "operator rules") {
		t.Fatalf("read_file on agents.md = %q, %v; want the file body", out, err)
	}
	if out, err := ReadFile(root, "rules/team-rules.md", 0, 0); err != nil || !strings.Contains(out, "needle") {
		t.Fatalf("read_file on rules/ = %q, %v; want the file body", out, err)
	}
	if out, err := Grep(root, "needle", ".", false, 10); err != nil || !strings.Contains(out, "team-rules.md") {
		t.Fatalf("grep over the workspace = %q, %v; want the governance file match", out, err)
	}
	if out, err := Glob(root, "*.md", "."); err != nil || !strings.Contains(out, "agents.md") {
		t.Fatalf("glob = %q, %v; want agents.md listed", out, err)
	}
	if out, err := ListDir(root, "rules", false, 1); err != nil || !strings.Contains(out, "team-rules.md") {
		t.Fatalf("list_dir rules/ = %q, %v; want the file listed", out, err)
	}
}

// Both dispatch modes are covered by the same refusal: the legacy run-wide
// worker (AllowWrite, nil Scope) and the partition-unit scoped writer whose
// scope CONTAINS the governance path (so the scope filter lets it through and
// only the governance arm can refuse it). The refusal is error-shaped, returns
// a nil dispatch error, and the seat keeps running. A path outside the unit
// scope is refused by the scope filter first (an earlier, also-error-shaped
// refusal), which is why the scoped seat's table stays inside "rules".
func TestRunnerRefusesGovernanceWriteInEveryMode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rules", "team-rules.md"), []byte("operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	legacy := NewRunner(root, true, false, nil)
	scoped := NewRunner(root, true, false, nil)
	scoped.Scope = []string{"rules"}

	for name, runner := range map[string]*ToolRunner{"legacy run-wide": legacy, "scoped unit": scoped} {
		for _, call := range []struct {
			tool string
			args string
			// inScope marks the calls the scoped seat's scope filter lets
			// through, so only the governance arm can refuse them.
			inScope bool
		}{
			{"write_file", `{"path":"rules/team-rules.md","content":"agent rules","overwrite":true}`, true},
			{"write_file", `{"path":"AGENTS.MD","content":"agent rules","overwrite":true}`, false},
			{"replace_in_file", `{"path":"rules/team-rules.md","old_string":"operator","new_string":"agent"}`, true},
			{"replace_in_file", `{"path":"RULES/team-rules.md","old_string":"operator","new_string":"agent"}`, false},
		} {
			out, err := runner.Execute(1, "m1", "p1", call.tool, call.args)
			if err != nil {
				t.Fatalf("%s/%s %s: dispatch error = %v; want nil (error-shaped refusal)", name, call.tool, call.args, err)
			}
			if !strings.HasPrefix(out, "Error:") {
				t.Fatalf("%s/%s %s: got %q; want an error-shaped refusal", name, call.tool, call.args, out)
			}
			if (name == "legacy run-wide" || call.inScope) && !strings.Contains(out, "governance/credential file") {
				t.Fatalf("%s/%s %s: got %q; want a governance refusal", name, call.tool, call.args, out)
			}
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "rules", "team-rules.md"))
	if err != nil || string(data) != "operator\n" {
		t.Fatalf("governance file content = %q, %v; want it unchanged", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.MD")); !os.IsNotExist(err) {
		t.Fatalf("denied write created AGENTS.MD")
	}
}

// An in-root directory symlink into a governed directory is refused: the
// lexical path looks clear, but the write would land inside the protected
// directory. Only the DIRECTORY chain is resolved, so the file-symlink case
// keeps its existing semantics (the atomic rename replaces the link, never the
// link target's content - TestWriteThroughInRootSymlinkCannotTouchHook).
func TestGovernanceGuardResolvesDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	governed := filepath.Join(root, ".agy-pro")
	if err := os.MkdirAll(governed, 0o755); err != nil {
		t.Fatal(err)
	}
	makeSymlink(t, governed, filepath.Join(root, "link"))

	if _, err := WriteFile(root, "link/registry-keys.v2.json", "evil", true); err == nil {
		t.Fatal("write through a directory symlink into .agy-pro: expected refusal, got nil")
	} else if !strings.Contains(err.Error(), "governance/credential file") {
		t.Fatalf("directory symlink: unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(governed, "registry-keys.v2.json")); !os.IsNotExist(err) {
		t.Fatal("VULN: write landed inside the governed directory through a symlink")
	}
}
