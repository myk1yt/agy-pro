package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserHome(t *testing.T) {
	home, err := UserHome()
	if err != nil {
		t.Fatalf("UserHome failed: %v", err)
	}
	if home == "" {
		t.Fatal("UserHome returned empty string")
	}
}

func TestAgyProHomeOverride(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(EnvAgyProHome, tempDir)

	resolved, err := AgyProHome()
	if err != nil {
		t.Fatalf("AgyProHome failed: %v", err)
	}
	if resolved != filepath.Clean(tempDir) {
		t.Fatalf("expected %q, got %q", tempDir, resolved)
	}

	binDir, err := AgyProBin()
	if err != nil {
		t.Fatalf("AgyProBin failed: %v", err)
	}
	expectedBin := filepath.Join(tempDir, "bin")
	if binDir != expectedBin {
		t.Fatalf("expected bin %q, got %q", expectedBin, binDir)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	tempDir := t.TempDir()
	target := filepath.Join(tempDir, "subdir", "test.txt")
	content := []byte("hello atomic world")

	if err := AtomicWriteFile(target, content, 0o644); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	readBack, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(readBack) != string(content) {
		t.Fatalf("content mismatch: got %q, want %q", string(readBack), string(content))
	}

	// Overwrite test
	newContent := []byte("updated content")
	if err := AtomicWriteFile(target, newContent, 0o644); err != nil {
		t.Fatalf("AtomicWriteFile overwrite failed: %v", err)
	}
	readBack, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after overwrite failed: %v", err)
	}
	if string(readBack) != string(newContent) {
		t.Fatalf("overwrite content mismatch: got %q, want %q", string(readBack), string(newContent))
	}
}

func TestMoveToBackup(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(EnvAgyProHome, tempDir)

	srcFile := filepath.Join(tempDir, "orig.txt")
	if err := os.WriteFile(srcFile, []byte("data"), 0o644); err != nil {
		t.Fatalf("write srcFile failed: %v", err)
	}

	backupDir := filepath.Join(tempDir, "backups", "20261011_120000")
	dest, err := MoveToBackup(srcFile, backupDir)
	if err != nil {
		t.Fatalf("MoveToBackup failed: %v", err)
	}

	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("srcFile still exists after move: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("dest does not exist: %v", err)
	}

	read, err := os.ReadFile(dest)
	if err != nil || string(read) != "data" {
		t.Fatalf("unexpected content at dest: %v", err)
	}
}

func TestDefaultRealAgyPath(t *testing.T) {
	path, err := DefaultRealAgyPath()
	if err != nil {
		t.Fatalf("DefaultRealAgyPath failed: %v", err)
	}
	if !strings.Contains(path, "agy") {
		t.Fatalf("expected agy in path, got %q", path)
	}
}
