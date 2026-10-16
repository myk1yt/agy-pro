package agysettings

import (
	"crypto/md5"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// realHomeForIsolationCheck is the home directory resolved once, before any
// test overrides USERPROFILE/HOME. It is used only for a string-prefix
// assertion that a sandboxed write can never land in the real profile. The real
// ~/.gemini is never read, stat'ed, or written by this suite.
var realHomeForIsolationCheck string

func TestMain(m *testing.M) {
	if home, err := os.UserHomeDir(); err == nil {
		realHomeForIsolationCheck = filepath.Clean(home)
	}
	os.Exit(m.Run())
}

// sandboxHome points USERPROFILE and HOME at a fresh temp directory and returns
// it. os.UserHomeDir honors USERPROFILE on Windows and HOME elsewhere, so
// setting both makes every path in this package resolve inside the sandbox on
// any platform.
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

// narrowLocks shrinks the lock waits so the lock tests run in milliseconds and
// restores the production values afterwards.
func narrowLocks(t *testing.T) {
	t.Helper()
	prevWait, prevPoll := lockAcquireWait, lockPollInterval
	lockAcquireWait = 40 * time.Millisecond
	lockPollInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		lockAcquireWait = prevWait
		lockPollInterval = prevPoll
	})
}

// isUnder reports whether path is dir itself or lives inside it.
func isUnder(path, dir string) bool {
	if dir == "" {
		return false
	}
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || len(path) > len(dir)+1 && path[:len(dir)+1] == dir+string(filepath.Separator)
}

// treeFiles returns every regular file under root, relative to root, sorted.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// allowedSibling reports whether name is one of the paths a write cycle may
// create next to target (the target itself, its lockfile, its backups).
func allowedSibling(name, target string) bool {
	switch {
	case name == target:
		return true
	case name == lockPath(target):
		return true
	case len(name) > len(target)+len(".bak.") && name[:len(target)+len(".bak.")] == target+".bak.":
		return true
	}
	return false
}

// assertOnlySiblings walks root and fails when any file is not one of the
// paths a write cycle may create next to target.
func assertOnlySiblings(t *testing.T, root, target string) {
	t.Helper()
	for _, name := range treeFiles(t, root) {
		full := filepath.Join(root, filepath.FromSlash(name))
		if !allowedSibling(full, target) {
			t.Errorf("a write created %s outside the target's siblings (%s)", full, strings.Join(siblingPaths(target), ", "))
		}
		if !isUnder(full, root) {
			t.Errorf("a write escaped %s: %s", root, full)
		}
	}
}

// openEmpty opens a settings file that does not exist yet and fails the test on
// error. The caller must Close the handle.
func openEmpty(t *testing.T, path string) *File {
	t.Helper()
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return f
}

// writeFileForTest plants bytes with os.WriteFile (test setup only; the package
// itself never writes this way).
func writeFileForTest(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// readFileForTest reads a file and fails the test on error.
func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// md5Sum is the md5 pin used by the byte-identity assertions.
func md5Sum(data []byte) [16]byte { return md5.Sum(data) }

// fileModeForTest returns the permission bits of path, skipping the assertion
// on Windows where the OS reports only the read-only attribute (0666 for a
// writable file, 0444 for a read-only one) and real modes live in the ACL.
func fileModeForTest(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return 0o600 // not observable on Windows; see the caller's note
	}
	return info.Mode().Perm()
}
