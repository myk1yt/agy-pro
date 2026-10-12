package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoTempResidue(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-write-") {
			t.Fatalf("temp residue left behind: %s", e.Name())
		}
	}
}

func TestReplaceInFileSingleOccurrence(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "src/main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")

	out, err := ReplaceInFile(root, "src/main.go", "println(\"hello\")", "fmt.Println(\"hello\")", false)
	if err != nil {
		t.Fatalf("ReplaceInFile: %v", err)
	}
	if !strings.Contains(out, "Replaced 1 occurrence") {
		t.Fatalf("unexpected result message: %s", out)
	}

	data, err := os.ReadFile(filepath.Join(root, "src/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fmt.Println(\"hello\")") || strings.Contains(string(data), "println(\"hello\")") {
		t.Fatalf("replacement not applied:\n%s", data)
	}
	assertNoTempResidue(t, filepath.Join(root, "src"))
}

func TestReplaceInFileMultilineAnchor(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "multi.txt", "alpha\nbeta\ngamma\n")

	if _, err := ReplaceInFile(root, "multi.txt", "beta\ngamma", "BETA\nGAMMA\ndelta", false); err != nil {
		t.Fatalf("ReplaceInFile: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "multi.txt"))
	if string(data) != "alpha\nBETA\nGAMMA\ndelta\n" {
		t.Fatalf("unexpected file content:\n%s", data)
	}
}

func TestReplaceInFileFailureSemantics(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "dup.txt", "x := 1\nx := 1\n")
	writeFixture(t, root, "gone.txt", "nothing here\n")

	// Zero matches: fail, file untouched.
	if _, err := ReplaceInFile(root, "dup.txt", "ABSENT", "y", false); err == nil {
		t.Fatal("zero matches: expected error, got nil")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("zero matches: unexpected error: %v", err)
	}

	// Multiple matches without replace_all: fail, file untouched.
	if _, err := ReplaceInFile(root, "dup.txt", "x := 1", "x := 2", false); err == nil {
		t.Fatal("multiple matches: expected error, got nil")
	} else if !strings.Contains(err.Error(), "matches 2 times") || !strings.Contains(err.Error(), "replace_all=true") {
		t.Fatalf("multiple matches: unexpected error: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "dup.txt"))
	if string(data) != "x := 1\nx := 1\n" {
		t.Fatalf("failed call must not modify the file, got:\n%s", data)
	}

	// Empty anchor is always invalid.
	if _, err := ReplaceInFile(root, "dup.txt", "", "y", true); err == nil {
		t.Fatal("empty old_string: expected error, got nil")
	}

	// Missing file: error, no file created.
	if _, err := ReplaceInFile(root, "missing.txt", "a", "b", false); err == nil {
		t.Fatal("missing file: expected error, got nil")
	}
	if _, err := os.Stat(filepath.Join(root, "missing.txt")); !os.IsNotExist(err) {
		t.Fatal("missing file: the failed call must not create the target")
	}

	// Directory target: error.
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceInFile(root, "adir", "a", "b", false); err == nil {
		t.Fatal("directory target: expected error, got nil")
	}

	assertNoTempResidue(t, root)
}

func TestReplaceInFileReplaceAll(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "loop.txt", "TODO\nkeep\nTODO\nkeep\nTODO\n")

	out, err := ReplaceInFile(root, "loop.txt", "TODO", "DONE", true)
	if err != nil {
		t.Fatalf("ReplaceInFile: %v", err)
	}
	if !strings.Contains(out, "Replaced 3 occurrence") {
		t.Fatalf("unexpected result message: %s", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, "loop.txt"))
	if string(data) != "DONE\nkeep\nDONE\nkeep\nDONE\n" {
		t.Fatalf("replace_all result wrong:\n%s", data)
	}

	// replace_all on a single occurrence still succeeds.
	writeFixture(t, root, "one.txt", "only one\n")
	if _, err := ReplaceInFile(root, "one.txt", "one", "1", true); err != nil {
		t.Fatalf("replace_all single occurrence: %v", err)
	}

	// Empty new_string deletes the matched text.
	if _, err := ReplaceInFile(root, "one.txt", "1", "", false); err != nil {
		t.Fatalf("delete via empty new_string: %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "one.txt"))
	if string(data) != "only \n" {
		t.Fatalf("deletion result wrong:\n%s", data)
	}
}

func TestReplaceInFileGuard(t *testing.T) {
	root := t.TempDir()

	// Traversal is refused like every other tool.
	if _, err := ReplaceInFile(root, "../escape.txt", "a", "b", false); err == nil {
		t.Fatal("traversal: expected guard denial, got nil")
	}

	// A symlink pointing outside the root must be refused (same S-4/F1 guard).
	symlinkRoot, secretPath := setupSymlinkSandbox(t)
	if _, err := ReplaceInFile(symlinkRoot, "leak.txt", "SUPERSECRETVALUE123", "OWNED", false); err == nil {
		t.Fatal("out-of-root symlink: expected guard denial, got nil")
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secret), "OWNED") {
		t.Fatal("VULN: outside-root secret file was modified through a symlink")
	}
	assertNoTempResidue(t, symlinkRoot)
}

func TestReplaceInFileSizeCap(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", maxReplaceFileBytes+1)
	writeFixture(t, root, "big.txt", big)

	if _, err := ReplaceInFile(root, "big.txt", "a", "b", false); err == nil {
		t.Fatal("oversized file: expected error, got nil")
	} else if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized file: unexpected error: %v", err)
	}
}

func TestBuildOpenAIToolsWriteGateIncludesReplaceInFile(t *testing.T) {
	withWrite := BuildOpenAITools(true, false)
	names := map[string]bool{}
	for _, td := range withWrite {
		names[td.Function.Name] = true
	}
	if !names["write_file"] || !names["replace_in_file"] {
		t.Fatalf("write token must expose write_file and replace_in_file, got: %v", names)
	}
	if names["run_command"] {
		t.Fatal("exec tool must not appear without the exec token")
	}

	withoutWrite := BuildOpenAITools(false, false)
	for _, td := range withoutWrite {
		if td.Function.Name == "replace_in_file" || td.Function.Name == "write_file" {
			t.Fatalf("%s must not be exposed without the write token", td.Function.Name)
		}
	}
}

func TestRunnerReplaceInFileGate(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "gate.txt", "value = 1\n")

	// Read-only runner: prohibited.
	readonly := NewRunner(root, false, false, nil)
	res, _ := readonly.Execute(1, "m", "p", "replace_in_file", `{"path":"gate.txt","old_string":"1","new_string":"2"}`)
	if !strings.Contains(res, "prohibited") {
		t.Fatalf("expected replace_in_file to be prohibited, got: %s", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "gate.txt"))
	if string(data) != "value = 1\n" {
		t.Fatalf("prohibited call must not modify the file, got:\n%s", data)
	}

	// Write-enabled runner: dispatch works end to end.
	writer := NewRunner(root, true, false, nil)
	res, err := writer.Execute(1, "m", "p", "replace_in_file", `{"path":"gate.txt","old_string":"value = 1","new_string":"value = 2"}`)
	if err != nil {
		t.Fatalf("replace_in_file dispatch: %v", err)
	}
	if strings.Contains(res, "Error") {
		t.Fatalf("dispatch failed: %s", res)
	}
	data, _ = os.ReadFile(filepath.Join(root, "gate.txt"))
	if string(data) != "value = 2\n" {
		t.Fatalf("dispatch did not apply the replacement, got:\n%s", data)
	}

	// Malformed JSON args: tool error, not a panic.
	res, _ = writer.Execute(1, "m", "p", "replace_in_file", `{not-json`)
	if !strings.Contains(res, "invalid arguments for replace_in_file") {
		t.Fatalf("expected invalid-arguments error, got: %s", res)
	}
}
