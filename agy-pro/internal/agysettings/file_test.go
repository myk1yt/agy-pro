package agysettings

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A missing settings file is an empty document, not an error.
func TestOpenMissingFileIsAnEmptyDocument(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "missing", "settings.json")

	f := openEmpty(t, path)
	defer f.Close()

	if keys := f.Keys(); len(keys) != 0 {
		t.Errorf("Keys() = %v; want none for a missing file", keys)
	}
	v, err := f.Get(KeyModelProvider)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != nil {
		t.Errorf("Get(modelProvider) = %v; want nil for a missing file", v)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open created the settings file (stat err = %v)", err)
	}
}

// A malformed settings file is a clear error and is never overwritten: the
// caller decides what to do with it.
func TestOpenMalformedFileFailsWithoutOverwriting(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	broken := []byte(`{"modelProvider": "gemini", "customModels": `)
	if err := writeFileForTest(path, broken); err != nil {
		t.Fatal(err)
	}
	before := md5.Sum(broken)

	f, err := Open(path)
	if err == nil {
		f.Close()
		t.Fatal("Open accepted malformed JSON")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error does not say the file is malformed: %v", err)
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error does not state the no-overwrite rule: %v", err)
	}
	after := md5.Sum(readFileForTest(t, path))
	if before != after {
		t.Error("the malformed file was modified by the failed Open")
	}
}

// A managed key with the wrong shape is also a clear error, never a guess.
func TestOpenRejectsAManagedKeyWithTheWrongShape(t *testing.T) {
	cases := map[string]string{
		"modelProvider as a number":  `{"modelProvider": 42}`,
		"modelProvider as an object": `{"modelProvider": {}}`,
		"customModels as a string":   `{"customModels": "nope"}`,
		"customModels as an array":   `{"customModels": []}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home := sandboxHome(t)
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Errorf("Open accepted %s", body)
			}
		})
	}
}

// An empty file and a "null" document are treated as an empty document, the
// same way agy falls back to defaults.
func TestOpenEmptyAndNullDocuments(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "whitespace": "  \n\t", "null": "null"} {
		t.Run(name, func(t *testing.T) {
			home := sandboxHome(t)
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(body)); err != nil {
				t.Fatal(err)
			}
			f := openEmpty(t, path)
			defer f.Close()
			if keys := f.Keys(); len(keys) != 0 {
				t.Errorf("Keys() = %v; want none", keys)
			}
		})
	}
}

func TestWriteCreatesTheFileWithManagedKeys(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := readFileForTest(t, path)
	if string(raw) != `{"modelProvider":"gemini"}` {
		t.Errorf("written bytes = %s; want the compact managed document", raw)
	}
	if mode := fileModeForTest(t, path); mode != 0o600 {
		t.Errorf("settings file mode = %#o; want 0600", mode)
	}
}

func TestWriteBacksUpPreviousBytesWithTimestampedName(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	original := []byte(`{"toolPermission":"always-proceed"}`)
	if err := writeFileForTest(path, original); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	backup := f.LastBackup()
	if backup == "" {
		t.Fatal("Write recorded no backup for an existing file")
	}
	want := regexp.MustCompile(`^settings\.json\.bak\.\d{8}-\d{6}(-\d+)?$`)
	if !want.MatchString(filepath.Base(backup)) {
		t.Errorf("backup name %q does not match <path>.bak.YYYYMMDD-HHMMSS", filepath.Base(backup))
	}
	if got := readFileForTest(t, backup); string(got) != string(original) {
		t.Errorf("backup bytes = %s; want the previous bytes %s", got, original)
	}
	if mode := fileModeForTest(t, backup); mode != 0o600 {
		t.Errorf("backup mode = %#o; want 0600", mode)
	}
	if len(f.Backups()) != 1 || f.Backups()[0] != backup {
		t.Errorf("Backups() = %v; want [%s]", f.Backups(), backup)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestWriteLeavesNoTemporaryFilesBehind(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, name := range treeFiles(t, home) {
		if strings.HasPrefix(name, ".tmp-") || strings.Contains(name, "/.tmp-") {
			t.Errorf("atomic write left a temporary file behind: %s", name)
		}
	}
	assertOnlySiblings(t, home, path)
}

// Rollback restores the last backup byte-for-byte.
func TestRollbackRestoresBytesIdentically(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	original := []byte(`{"toolPermission":"always-proceed","theme":"dark","customModels":{"gemini-api://127.0.0.1:8791/models/probe":{"modelName":"gemini-api://127.0.0.1:8791/models/probe","displayName":"Probe","context_window":65536}}}`)
	if err := writeFileForTest(path, original); err != nil {
		t.Fatal(err)
	}
	want := md5.Sum(original)

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if string(readFileForTest(t, path)) == string(original) {
		t.Fatal("the managed write did not change the file; the rollback assertion would be vacuous")
	}

	if err := f.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	got := md5.Sum(readFileForTest(t, path))
	if got != want {
		t.Errorf("md5 after rollback = %x; want %x (byte-identical restore)", got, want)
	}

	// Rollback is idempotent and never deletes anything.
	if err := f.Rollback(); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if again := md5.Sum(readFileForTest(t, path)); again != want {
		t.Errorf("md5 after the second rollback = %x; want %x", again, want)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(f.LastBackup()); err != nil {
		t.Errorf("the backup disappeared after rollback: %v", err)
	}
}

// Rollback without a backup is a clear error, not a silent no-op.
func TestRollbackWithoutBackupFails(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// The file did not exist, so this Write creates no backup.
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	err := f.Rollback()
	if err == nil {
		t.Fatal("Rollback succeeded with no backup")
	}
	if !errors.Is(err, ErrNoBackup) {
		t.Errorf("Rollback error = %v; want ErrNoBackup", err)
	}
}

// Two writes in a row: Rollback restores the LAST backup, and backups
// accumulate rather than overwrite each other.
func TestRollbackRestoresTheLastBackupAndBackupsAccumulate(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	first := []byte(`{"toolPermission":"always-proceed"}`)
	if err := writeFileForTest(path, first); err != nil {
		t.Fatal(err)
	}
	firstHash := md5.Sum(first)

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, "one"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	second := readFileForTest(t, path)
	secondHash := md5.Sum(second)

	if err := f.Set(KeyModelProvider, "two"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if len(f.Backups()) != 2 {
		t.Fatalf("Backups() = %v; want 2 accumulated backups", f.Backups())
	}

	if err := f.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := md5.Sum(readFileForTest(t, path)); got != secondHash {
		t.Errorf("md5 after rollback = %x; want the last backup %x", got, secondHash)
	}
	if f.Backups()[0] == f.Backups()[1] {
		t.Error("the second write overwrote the first backup")
	}
	if got := md5.Sum(readFileForTest(t, f.Backups()[0])); got != firstHash {
		t.Errorf("the first backup no longer holds the original bytes: %x; want %x", got, firstHash)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Unmanaged keys survive a managed write with their bytes intact.
func TestWritePreservesUnmanagedKeysVerbatim(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"toolPermission":"always-proceed","mcpServers":{"probe":{"command":"node","args":["a","b"]}},"experimental":{"deep":{"nested":[1,2,3]}}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	if _, err := f.Get("toolPermission"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Get on an unmanaged key must fail with ErrNotManaged, got %v", err)
	}
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var beforeDoc, afterDoc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(planted), &beforeDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readFileForTest(t, path), &afterDoc); err != nil {
		t.Fatal(err)
	}
	for key, want := range beforeDoc {
		got, ok := afterDoc[key]
		if !ok {
			t.Errorf("unmanaged key %q was dropped", key)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("unmanaged key %q changed: %s -> %s", key, want, got)
		}
	}
}

// Delete drops a managed key from the document without touching anything else.
func TestDeleteDropsTheManagedKey(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(`{"modelProvider":"gemini","toolPermission":"always-proceed"}`)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	if err := f.Delete(KeyModelProvider); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := string(readFileForTest(t, path))
	if strings.Contains(raw, "modelProvider") {
		t.Errorf("modelProvider survived Delete: %s", raw)
	}
	if !strings.Contains(raw, "toolPermission") {
		t.Errorf("Delete damaged an unmanaged key: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// Lockfile
// ---------------------------------------------------------------------------

// A second Open while the lock is live fails cleanly instead of interleaving
// two read-modify-write cycles.
func TestSecondOpenFailsCleanlyWhileLocked(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	first := openEmpty(t, path)
	defer first.Close()

	second, err := Open(path)
	if err == nil {
		second.Close()
		t.Fatal("a second Open succeeded while the first handle holds the lock")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("second Open error = %v; want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error does not explain the lock: %v", err)
	}
}

// Once the first handle closes, the lock is free again.
func TestOpenSucceedsAfterClose(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	first := openEmpty(t, path)
	if err := first.Set(KeyModelProvider, "one"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := first.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := openEmpty(t, path)
	defer second.Close()
	v, err := second.Get(KeyModelProvider)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "one" {
		t.Errorf("Get(modelProvider) = %v; want the value the first handle wrote", v)
	}
}

// Close releases by truncation, never by unlink, so the lock file survives as a
// released marker.
func TestCloseReleasesTheLockWithoutDeletingIt(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lock := lockPath(path)
	info, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("the lock file was deleted by Close: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("released lock still carries content: %d bytes", info.Size())
	}
}

// A released (empty) lock is taken over immediately, with no stale wait.
func TestOpenTakesOverAReleasedLock(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	lock := lockPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileForTest(lock, nil); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	owned, err := lockOwnedBy(lock, f.owner)
	if err != nil {
		t.Fatalf("lockOwnedBy: %v", err)
	}
	if !owned {
		t.Error("Open did not take over the released lock")
	}
}

// A lock left behind by a crashed writer is broken once it is older than the
// bounded stale age.
func TestOpenBreaksAStaleLock(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	lock := lockPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	stale := lockContent{pid: "999999", acquired: old, owner: "crashed-writer-token"}
	if err := writeFileForTest(lock, stale.encode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	owned, err := lockOwnedBy(lock, f.owner)
	if err != nil {
		t.Fatalf("lockOwnedBy: %v", err)
	}
	if !owned {
		t.Error("the stale lock was not broken")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("breaking the stale lock deleted the lock file: %v", err)
	}
}

// A live lock is never broken, however long the caller waits within the bound.
func TestLiveLockIsNotBroken(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	lock := lockPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	live := lockContent{pid: fmt.Sprint(os.Getpid()), acquired: time.Now(), owner: "live-writer-token"}
	if err := writeFileForTest(lock, live.encode()); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open error = %v; want ErrLocked for a live lock", err)
	}
	data := readFileForTest(t, lock)
	if string(data) != string(live.encode()) {
		t.Errorf("the live lock was modified:\n got %s\nwant %s", data, live.encode())
	}
}

// An unparseable lock body is judged by the file's age: young means wait, old
// means break.
func TestUnparseableLockUsesFileAge(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	lock := lockPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileForTest(lock, []byte("garbage-from-a-crash")); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); !errors.Is(err, ErrLocked) {
		t.Errorf("Open error = %v; want ErrLocked for a young unparseable lock", err)
	}

	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	f := openEmpty(t, path)
	defer f.Close()
}

// Write re-verifies the lock, so a lock broken out from under a handle is
// caught instead of written through.
func TestWriteRefusesWhenTheLockWasTakenOver(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, "one"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Simulate another writer breaking the (supposedly stale) lock.
	takeover := lockContent{pid: "4242", acquired: time.Now(), owner: "other-writer-token"}
	if err := writeFileForTest(lockPath(path), takeover.encode()); err != nil {
		t.Fatal(err)
	}

	if err := f.Write(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Write error = %v; want ErrLocked after the lock was taken over", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused Write still touched the settings file (stat err = %v)", err)
	}
}

// ---------------------------------------------------------------------------
// Confinement
// ---------------------------------------------------------------------------

// A full write cycle touches only the target and its .lock / .bak siblings.
func TestWriteCycleStaysInsideTheTargetSiblings(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(`{"toolPermission":"always-proceed"}`)); err != nil {
		t.Fatal(err)
	}
	before := treeFiles(t, home)

	f := openEmpty(t, path)
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Set(KeyCustomModels, map[string]CustomModelConfig{
		"gemini-api://127.0.0.1:8791/models/probe": {
			ModelName:     "gemini-api://127.0.0.1:8791/models/probe",
			DisplayName:   "Probe",
			ContextWindow: 65536,
		},
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	seen := map[string]bool{}
	for _, name := range treeFiles(t, home) {
		seen[name] = true
	}
	assertOnlySiblings(t, home, path)
	for _, name := range before {
		if !seen[name] {
			t.Errorf("write cycle removed the pre-existing file %s", name)
		}
	}
}

func TestWriteOnAClosedFileFails(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.Set(KeyModelProvider, "gemini"); err != nil {
		t.Fatalf("Set on a closed file: %v", err)
	}
	if err := f.Write(); err == nil {
		t.Error("Write on a closed file succeeded")
	}
	if err := f.Rollback(); err == nil {
		t.Error("Rollback on a closed file succeeded")
	}
	if err := f.Close(); err != nil {
		t.Errorf("a second Close must be a no-op: %v", err)
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	sandboxHome(t)
	if _, err := Open(""); err == nil {
		t.Error("Open(\"\") succeeded")
	}
	if _, err := Open("   "); err == nil {
		t.Error("Open(\"   \") succeeded")
	}
}

func TestLockContentRoundTrip(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Nanosecond)
	c := lockContent{pid: "1234", acquired: at, owner: "1234-abcdef"}
	parsed, ok := parseLockContent(c.encode())
	if !ok {
		t.Fatalf("parseLockContent rejected its own encoding: %q", c.encode())
	}
	if parsed.pid != c.pid || parsed.owner != c.owner || !parsed.acquired.Equal(c.acquired) {
		t.Errorf("round trip mismatch: %+v -> %+v", c, parsed)
	}
	if _, ok := parseLockContent([]byte("owner=only\n")); ok {
		t.Error("parseLockContent accepted a body without an acquired timestamp")
	}
	if _, ok := parseLockContent(nil); ok {
		t.Error("parseLockContent accepted an empty body")
	}
}
