package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertSymlinkFixture is the mandatory static check on the fixture itself:
// the link must really be a symlink as reported by os.Lstat. The fixture is
// created at runtime (never checked out of git), so the assertion is
// independent of `git core.symlinks` and of how the workspace was cloned.
func assertSymlinkFixture(t *testing.T, link string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("Lstat(%s): %v", link, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("fixture %s is not a symlink (mode %v); the test would be a false green", link, info.Mode())
	}
}

// makeSymlink creates a symlink fixture and skips the test when the host does
// not permit symlink creation (Windows without Developer Mode / non-admin).
func makeSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation not permitted on this host: %v", err)
	}
	assertSymlinkFixture(t, link)
}

// setupSymlinkSandbox builds the S-4 PoC layout in an isolated temp tree:
// root/ (workspace) with leak.txt -> outside/credentials.txt, where the secret
// lives outside the workspace root.
func setupSymlinkSandbox(t *testing.T) (root, secretPath string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secretPath = filepath.Join(outside, "credentials.txt")
	if err := os.WriteFile(secretPath, []byte("aws_secret_access_key = SUPERSECRETVALUE123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeSymlink(t, secretPath, filepath.Join(root, "leak.txt"))
	return root, secretPath
}

// S-4/F1 regression (the verify sheet's ready-to-run PoC, ported): a file
// symlink inside the workspace must not let grep read outside-root content.
func TestGrepSkipsFileSymlinkOutsideRoot(t *testing.T) {
	root, _ := setupSymlinkSandbox(t)

	// read_file is guarded (symmetry check).
	if _, err := ReadFile(root, "leak.txt", 0, 0); err == nil {
		t.Fatal("read_file: expected guard denial for an out-of-root symlink, got nil")
	}

	out, err := Grep(root, "SUPERSECRET", ".", false, 10)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	t.Logf("grep output: %q", out)
	if strings.Contains(out, "SUPERSECRETVALUE123") {
		t.Fatalf("VULN: grep leaked outside-root content: %s", out)
	}
	if !strings.Contains(out, "skipped 1 entries that resolve outside the workspace root") {
		t.Errorf("grep output must report the skipped entry (one meta line), got: %s", out)
	}

	// The skip must be visible even when nothing matched.
	if strings.Contains(out, "no matches found") && !strings.Contains(out, "skipped") {
		t.Errorf("skip reason missing from output: %s", out)
	}
}

// S-4/F1: a symlink pointing outside is also refused when named directly, and
// an in-root target still greps normally (no over-blocking).
func TestGrepSymlinkInsideRootStillWorks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(sub, "real.txt")
	if err := os.WriteFile(real, []byte("needle inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeSymlink(t, real, filepath.Join(root, "alias.txt"))

	out, err := Grep(root, "needle", ".", false, 10)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "needle inside") {
		t.Errorf("in-root symlink target must still be greppable: %s", out)
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("in-root symlink must not be skipped: %s", out)
	}
}

// S-4/F1 (mandatory static case): the walk-entry guard rejects escaping
// entries without needing any symlink fixture, so the regression net holds on
// hosts where symlink creation is impossible.
func TestGuardWalkFileRejectsEscapes(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(inside, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "credentials.txt")
	if err := os.WriteFile(secret, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if safe, ok := guardWalkFile(root, inside); !ok || safe != inside {
		t.Errorf("guardWalkFile(%q) = (%q, %v); want the path itself", inside, safe, ok)
	}
	for _, escape := range []string{
		secret,
		filepath.Join(root, "..", "outside", "credentials.txt"),
		filepath.Join(outside, "credentials.txt"),
	} {
		if safe, ok := guardWalkFile(root, escape); ok {
			t.Errorf("guardWalkFile(%q) = (%q, true); want refusal (out of root)", escape, safe)
		}
	}
}

// S-4 optional hardening: ListDir marks symlink/reparse entries instead of
// presenting them as ordinary files.
func TestListDirMarksSymlinks(t *testing.T) {
	root, _ := setupSymlinkSandbox(t)
	out, err := ListDir(root, ".", false, 1)
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if !strings.Contains(out, "[LINK] leak.txt") {
		t.Errorf("list_dir must mark the symlink entry, got: %s", out)
	}
	if strings.Contains(out, "SUPERSECRETVALUE123") {
		t.Errorf("list_dir leaked target content: %s", out)
	}
}

// S-4/F1: glob never opens file content, so its behavior is unchanged - the
// in-root name still matches and no content is exposed.
func TestGlobUnchangedForSymlinks(t *testing.T) {
	root, _ := setupSymlinkSandbox(t)
	out, err := Glob(root, "*.txt", ".")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if strings.Contains(out, "SUPERSECRETVALUE123") {
		t.Fatalf("glob leaked content: %s", out)
	}
	if !strings.Contains(out, "leak.txt") {
		t.Errorf("glob should still list the in-root entry name: %s", out)
	}
}
