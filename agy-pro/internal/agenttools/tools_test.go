package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSafePath_Guard(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Valid path inside root
	safe, err := ResolveSafePath(tmpDir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error for valid path: %v", err)
	}
	if !strings.HasPrefix(safe, tmpDir) {
		t.Fatalf("safe path %q outside root %q", safe, tmpDir)
	}

	// Traversal with ..
	_, err = ResolveSafePath(tmpDir, "../escape.txt")
	if err == nil {
		t.Fatal("expected error for ../ traversal")
	}

	// Deep traversal
	_, err = ResolveSafePath(subDir, "../../escape.txt")
	if err == nil {
		t.Fatal("expected error for ../../ traversal")
	}

	// Absolute path outside root
	outside := filepath.Dir(tmpDir)
	_, err = ResolveSafePath(subDir, filepath.Join(outside, "other.txt"))
	if err == nil {
		t.Fatal("expected error for absolute path outside root")
	}
}

func TestReadFileAndWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := "test.txt"
	content := "line 1\nline 2\nline 3\nline 4\nline 5\n"

	// WriteFile
	msg, err := WriteFile(tmpDir, testFile, content, true)
	if err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if !strings.Contains(msg, "Successfully wrote") {
		t.Fatalf("unexpected message: %s", msg)
	}

	// Read full file
	readAll, err := ReadFile(tmpDir, testFile, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !strings.Contains(readAll, "line 1") || !strings.Contains(readAll, "line 5") {
		t.Fatalf("unexpected read output: %s", readAll)
	}

	// Read lines 2 to 4
	slice, err := ReadFile(tmpDir, testFile, 2, 4)
	if err != nil {
		t.Fatalf("ReadFile slice failed: %v", err)
	}
	if strings.Contains(slice, "line 1") || strings.Contains(slice, "line 5") {
		t.Fatalf("slice contains out of range lines: %s", slice)
	}
	if !strings.Contains(slice, "line 2") || !strings.Contains(slice, "line 4") {
		t.Fatalf("slice missing target lines: %s", slice)
	}

	// Overwrite protection
	_, err = WriteFile(tmpDir, testFile, "new content", false)
	if err == nil {
		t.Fatal("expected error when overwrite=false on existing file")
	}
}

func TestListDirAndGrepAndGlob(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "file1.go"), []byte("package main\nfunc Hello() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("hello world\nHELLO UPPER\n"), 0o644)
	subDir := filepath.Join(tmpDir, "nested")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "file3.go"), []byte("package nested\n"), 0o644)

	// ListDir
	list, err := ListDir(tmpDir, ".", true, 3)
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	if !strings.Contains(list, "file1.go") || !strings.Contains(list, "nested") {
		t.Fatalf("unexpected list output: %s", list)
	}

	// Grep case-insensitive
	matches, err := Grep(tmpDir, "hello", ".", true, 10)
	if err != nil {
		t.Fatalf("Grep failed: %v", err)
	}
	if !strings.Contains(matches, "file1.go") || !strings.Contains(matches, "file2.txt") {
		t.Fatalf("grep missing matches: %s", matches)
	}

	// Glob
	globFiles, err := Glob(tmpDir, "*.go", ".")
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if !strings.Contains(globFiles, "file1.go") {
		t.Fatalf("glob missing file1.go: %s", globFiles)
	}
}

func TestToolRunnerPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "fanout.jsonl")

	redact := func(s string) string {
		return strings.ReplaceAll(s, "SECRET_TOKEN", "[REDACTED]")
	}
	logger, err := NewLogger(logPath, redact)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	// Runner with read-only
	runner := NewRunner(tmpDir, false, false, logger)

	// Attempt write_file (should be rejected)
	res, _ := runner.Execute(1, "m1", "p1", "write_file", `{"path":"bad.txt","content":"data"}`)
	if !strings.Contains(res, "prohibited") {
		t.Fatalf("expected write_file to be prohibited, got: %s", res)
	}

	// Attempt run_command (should be rejected)
	res, _ = runner.Execute(1, "m1", "p1", "run_command", `{"command":"whoami"}`)
	if !strings.Contains(res, "prohibited") {
		t.Fatalf("expected run_command to be prohibited, got: %s", res)
	}

	// Model allowed tools restriction
	runner.AllowedModelTools = []string{"read_file"}
	res, _ = runner.Execute(1, "m1", "p1", "grep", `{"pattern":"foo"}`)
	if !strings.Contains(res, "not in the allowed tools list") {
		t.Fatalf("expected grep rejected by model tools filter, got: %s", res)
	}

	// Check logger recorded events and redacted secrets
	_ = logger.Log(LogEntry{
		SeatID:    1,
		Tool:      "test",
		Arguments: "key=SECRET_TOKEN",
	})

	logData, _ := os.ReadFile(logPath)
	if strings.Contains(string(logData), "SECRET_TOKEN") {
		t.Fatal("logger failed to redact SECRET_TOKEN")
	}
	if !strings.Contains(string(logData), "[REDACTED]") {
		t.Fatal("logger missing [REDACTED]")
	}
}
