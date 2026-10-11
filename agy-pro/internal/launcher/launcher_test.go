package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agypro/internal/paths"
)

func TestResolveRealAgyWithEnvOverride(t *testing.T) {
	tempDir := t.TempDir()
	fakeRealAgy := filepath.Join(tempDir, "real-agy")
	if runtime.GOOS == "windows" {
		fakeRealAgy += ".exe"
	}
	if err := os.WriteFile(fakeRealAgy, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake real agy: %v", err)
	}

	// 1. Env override succeeds
	resolved, err := resolveRealAgy("", fakeRealAgy, "", "")
	if err != nil {
		t.Fatalf("expected resolution via env, got error: %v", err)
	}
	if resolved != fakeRealAgy {
		t.Fatalf("expected %q, got %q", fakeRealAgy, resolved)
	}

	// 2. Env override non-existent fails
	_, err = resolveRealAgy("", filepath.Join(tempDir, "non-existent"), "", "")
	if !errors.Is(err, ErrRealAgyNotFound) {
		t.Fatalf("expected ErrRealAgyNotFound, got: %v", err)
	}
}

func TestResolveRealAgyExcludesSelfDirAndSameFile(t *testing.T) {
	tempDir := t.TempDir()
	selfDir := filepath.Join(tempDir, "shim-bin")
	otherDir := filepath.Join(tempDir, "real-bin")

	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}

	binName := "agy"
	if runtime.GOOS == "windows" {
		binName = "agy.exe"
	}

	selfBin := filepath.Join(selfDir, binName)
	realBin := filepath.Join(otherDir, binName)

	if err := os.WriteFile(selfBin, []byte("shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realBin, []byte("real"), 0o755); err != nil {
		t.Fatal(err)
	}

	// PATH has selfDir first, then otherDir
	pathEnv := stringsJoinPaths(selfDir, otherDir)

	resolved, err := resolveRealAgy(selfBin, "", pathEnv, "")
	if err != nil {
		t.Fatalf("resolveRealAgy failed: %v", err)
	}
	if resolved != realBin {
		t.Fatalf("expected to resolve %q (excluding selfDir), but got %q", realBin, resolved)
	}
}

func TestResolveRealAgyFallback(t *testing.T) {
	tempDir := t.TempDir()
	fallbackBin := filepath.Join(tempDir, "fallback-agy")
	if runtime.GOOS == "windows" {
		fallbackBin += ".exe"
	}
	if err := os.WriteFile(fallbackBin, []byte("fallback"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveRealAgy("", "", "", fallbackBin)
	if err != nil {
		t.Fatalf("resolveRealAgy fallback failed: %v", err)
	}
	if resolved != fallbackBin {
		t.Fatalf("expected fallback %q, got %q", fallbackBin, resolved)
	}
}

func TestExecutePassthroughRecursionGuard(t *testing.T) {
	t.Setenv(paths.EnvAgyProActive, "1")

	_, err := ExecutePassthrough("dummy", []string{})
	if !errors.Is(err, ErrRecursionDetected) {
		t.Fatalf("expected ErrRecursionDetected, got: %v", err)
	}
}

func stringsJoinPaths(paths ...string) string {
	return filepath.Join(paths[0]) + string(filepath.ListSeparator) + filepath.Join(paths[1])
}
