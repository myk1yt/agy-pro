package agysettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// symlinkOrSkip creates a symlink, skipping the test when the OS refuses.
// Windows needs Developer Mode or an elevated token for symlink creation, so the
// directory-based pin below carries the coverage there.
func symlinkOrSkip(t *testing.T, target, linkpath string) {
	t.Helper()
	if err := os.Symlink(target, linkpath); err != nil {
		t.Skipf("symlink creation is not permitted here: %v", err)
	}
}

// plantStaleLock overwrites the lockfile with a lock that is already older than
// staleLockAge, simulating another writer that judged our lock stale and broke
// it.
func plantStaleLock(t *testing.T, lock string) {
	t.Helper()
	old := time.Now().Add(-2 * staleLockAge)
	stale := lockContent{pid: "999999", acquired: old, owner: "other-writer-token"}
	if err := writeFileForTest(lock, stale.encode()); err != nil {
		t.Fatalf("plant stale lock: %v", err)
	}
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatalf("backdate the stale lock: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FIX-37: the protected global settings path
// ---------------------------------------------------------------------------

// Open refuses the resolved global settings path by default, names the path
// class, and never leaks the resolved user path into the error.
func TestOpenRefusesTheGlobalSettingsPathByDefault(t *testing.T) {
	sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	// The resolved path is the mask target: no part of it may reach the error.
	resolvedHome := filepath.Dir(filepath.Dir(filepath.Dir(global)))

	f, err := Open(global)
	if err == nil {
		f.Close()
		t.Fatal("Open accepted the protected global settings path without an override")
	}
	if !errors.Is(err, ErrGlobalSettingsPath) {
		t.Errorf("Open error = %v; want ErrGlobalSettingsPath", err)
	}
	if !strings.Contains(err.Error(), "global settings path") {
		t.Errorf("the error does not name the path class: %v", err)
	}
	if strings.Contains(err.Error(), resolvedHome) {
		t.Errorf("the error leaked the resolved user path: %v", err)
	}
	if strings.Contains(err.Error(), filepath.Base(resolvedHome)) {
		t.Errorf("the error leaked part of the resolved user path: %v", err)
	}
	if _, serr := os.Stat(global); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the refused Open still created the settings file (stat err = %v)", serr)
	}
	if _, serr := os.Stat(lockPath(global)); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the refused Open still created a lockfile (stat err = %v)", serr)
	}
}

// The explicit override is the only way through, and it works.
func TestOpenWithAllowGlobalOverrideSucceeds(t *testing.T) {
	sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}

	f, err := OpenWithOptions(global, OpenOptions{AllowGlobal: true})
	if err != nil {
		t.Fatalf("OpenWithOptions with AllowGlobal: %v", err)
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
	if got := string(readFileForTest(t, global)); got != `{"modelProvider":"gemini"}` {
		t.Errorf("written bytes = %s", got)
	}
}

// OpenGlobal is the self-documenting override and it warns loudly.
func TestOpenGlobalWarnsLoudly(t *testing.T) {
	home := sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}

	var warnings []string
	prev := globalOpenWarning
	globalOpenWarning = func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { globalOpenWarning = prev })

	f, err := OpenGlobal(global)
	if err != nil {
		t.Fatalf("OpenGlobal: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("OpenGlobal emitted %d warnings; want exactly 1", len(warnings))
	}
	if !strings.Contains(warnings[0], "WARNING") {
		t.Errorf("the warning is not loud: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "app-data-dir") {
		t.Errorf("the warning does not point at the safe alternative: %q", warnings[0])
	}
	if strings.Contains(warnings[0], home) {
		t.Errorf("the warning leaked the resolved user path: %q", warnings[0])
	}
}

// A per-session app-data dir that happens to be named "antigravity-cli" would
// address the global file. It is refused like any other spelling.
func TestAppDataDirNamedAntigravityCliIsAlsoRefused(t *testing.T) {
	sandboxHome(t)
	session, err := AppDataDirSettingsPath("antigravity-cli")
	if err != nil {
		t.Fatalf("AppDataDirSettingsPath: %v", err)
	}
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if !samePath(session, global) {
		t.Fatalf("expected the app-data-dir form to resolve to the global path: %q vs %q", session, global)
	}

	f, err := Open(session)
	if err == nil {
		f.Close()
		t.Fatal("Open accepted the global path through the app-data-dir spelling")
	}
	if !errors.Is(err, ErrGlobalSettingsPath) {
		t.Errorf("Open error = %v; want ErrGlobalSettingsPath", err)
	}
}

// The P2 app-data-dir regime is unaffected: the ordinary per-session path opens
// without an override.
func TestP2AppDataDirPathOpensWithoutAnOverride(t *testing.T) {
	sandboxHome(t)
	session, err := AppDataDirSettingsPath("agy-hybrid-unaffected")
	if err != nil {
		t.Fatalf("AppDataDirSettingsPath: %v", err)
	}
	doc, err := BuildP2Settings([]ExternalEntry{
		probeEntry("gemini-api://127.0.0.1:8791/models/probe-gemini", "gemini-api://127.0.0.1:8791/models/probe-gemini"),
	})
	if err != nil {
		t.Fatalf("BuildP2Settings: %v", err)
	}

	f := openEmpty(t, session)
	defer f.Close()
	for key, value := range doc {
		if err := f.Set(key, value); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := string(readFileForTest(t, session)); !strings.Contains(got, `"modelProvider":"gemini"`) {
		t.Errorf("session settings were not written: %s", got)
	}
}

// The guard also lives on the write path, so a handle that was not built by the
// guarded Open cannot write the global file either.
func TestWriteRefusesTheGlobalPath(t *testing.T) {
	sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(global), settingsDirPerm); err != nil {
		t.Fatalf("create the settings directory: %v", err)
	}

	owner, _, err := acquireLock(global)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	f := &File{path: global, owner: owner, raw: map[string]json.RawMessage{}, doc: SettingsDoc{ModelProvider: ModelProviderGemini}}

	if err := f.Write(); !errors.Is(err, ErrGlobalSettingsPath) {
		t.Fatalf("Write error = %v; want ErrGlobalSettingsPath", err)
	}
	if _, serr := os.Stat(global); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the refused Write still created the settings file (stat err = %v)", serr)
	}

	// The override is exactly what unblocks the write path.
	f.allowGlobal = true
	if err := f.Write(); err != nil {
		t.Fatalf("Write with the override: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := string(readFileForTest(t, global)); got != `{"modelProvider":"gemini"}` {
		t.Errorf("written bytes = %s", got)
	}
}

func TestIsGlobalSettingsPathIgnoresSpelling(t *testing.T) {
	home := sandboxHome(t)
	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if !isGlobalSettingsPath(global) {
		t.Error("isGlobalSettingsPath rejected the resolved global path")
	}
	if !isGlobalSettingsPath(global + string(filepath.Separator)) {
		t.Error("isGlobalSettingsPath rejected a trailing-separator spelling")
	}
	if !isGlobalSettingsPath(filepath.Join(filepath.Dir(global), "..", filepath.Base(filepath.Dir(global)), settingsFileName)) {
		t.Error("isGlobalSettingsPath rejected a .. spelling")
	}
	other := filepath.Join(home, "settings.json")
	if isGlobalSettingsPath(other) {
		t.Errorf("isGlobalSettingsPath accepted an unrelated path %q", other)
	}
}

func TestSamePathIsCaseInsensitiveOnWindowsOnly(t *testing.T) {
	a, b := filepath.Join("C:", "Users", "x"), filepath.Join("c:", "users", "X")
	if samePath(a, b) != (os.PathSeparator == '\\') {
		t.Errorf("samePath(%q, %q) = %v; want it to follow the platform's case rules", a, b, samePath(a, b))
	}
	if !samePath(a, a) {
		t.Error("samePath rejected an identical path")
	}
}

// ---------------------------------------------------------------------------
// FIX-38a: a non-regular file at the lock path
// ---------------------------------------------------------------------------

// A directory planted at the lock path is refused. This variant needs no
// privileges, so it carries the coverage on every platform.
func TestAcquireLockRefusesADirectoryAtTheLockPath(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := os.MkdirAll(lockPath(path), settingsDirPerm); err != nil {
		t.Fatalf("plant a directory at the lock path: %v", err)
	}

	f, err := Open(path)
	if err == nil {
		f.Close()
		t.Fatal("Open accepted a directory at the lock path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("the error does not name the non-regular lock: %v", err)
	}
}

// A planted symlink at the lock path must not be followed by the O_TRUNC open
// used to take over or release a lock, so the file it points at survives.
func TestAcquireLockRefusesAPlantedSymlink(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	victim := filepath.Join(home, "victim.txt")
	victimBytes := []byte("do-not-corrupt-me")
	if err := writeFileForTest(victim, victimBytes); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, victim, lockPath(path))

	f, err := Open(path)
	if err == nil {
		f.Close()
		t.Fatal("Open accepted a symlinked lock path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("the error does not name the non-regular lock: %v", err)
	}
	if got := readFileForTest(t, victim); string(got) != string(victimBytes) {
		t.Errorf("the lock path was followed and the symlink target was corrupted: %s", got)
	}
}

// A symlink that appears after acquisition must not be followed by Close's
// O_TRUNC release either. The victim is given lock content this handle owns, so
// Close really does believe it owns the lock and would truncate it.
func TestCloseRefusesToFollowAPlantedSymlink(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lockBytes := readFileForTest(t, lockPath(path))

	victim := filepath.Join(home, "victim.txt")
	if err := writeFileForTest(victim, lockBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockPath(path)); err != nil {
		t.Fatalf("clear the lock for the symlink swap: %v", err)
	}
	symlinkOrSkip(t, victim, lockPath(path))

	if err := f.Close(); err == nil {
		t.Error("Close accepted a symlinked lock path")
	} else if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("Close error does not name the non-regular lock: %v", err)
	}
	if got := readFileForTest(t, victim); string(got) != string(lockBytes) {
		t.Errorf("Close followed the symlink and corrupted the target: %s", got)
	}
}

// ---------------------------------------------------------------------------
// FIX-38b: the stale-break lost update
// ---------------------------------------------------------------------------

// When the lock is taken over as stale, the document is re-read from disk and
// this handle's managed-key changes are re-applied on top, so a concurrent
// writer's content is never silently overwritten.
func TestWriteReloadsAfterAStaleTakeover(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(`{"toolPermission":"a"}`)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Another writer lands content while this handle waits, then our lock is
	// judged stale and broken.
	if err := writeFileForTest(path, []byte(`{"toolPermission":"b","theme":"dark"}`)); err != nil {
		t.Fatal(err)
	}
	plantStaleLock(t, lockPath(path))

	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw := string(readFileForTest(t, path))
	if !strings.Contains(raw, `"theme":"dark"`) {
		t.Errorf("the concurrent writer's new key was lost: %s", raw)
	}
	if !strings.Contains(raw, `"toolPermission":"b"`) {
		t.Errorf("the concurrent writer's value was reverted: %s", raw)
	}
	if !strings.Contains(raw, `"modelProvider":"gemini"`) {
		t.Errorf("this handle's managed change was lost: %s", raw)
	}
}

// Without a takeover, no reload happens: an ordinary second write keeps working
// exactly as before (no behavior change for the P2 regime).
func TestWriteWithoutATakeoverDoesNotReload(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(`{"toolPermission":"a"}`)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, "one"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	// Change the file behind the handle's back WITHOUT touching the lock: the
	// handle still owns it, so no reload is triggered and the write is based on
	// the document this handle read.
	if err := writeFileForTest(path, []byte(`{"toolPermission":"z"}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(KeyModelProvider, "two"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	raw := string(readFileForTest(t, path))
	if !strings.Contains(raw, `"toolPermission":"a"`) {
		t.Errorf("an unlocked-behind-our-back change should not be adopted: %s", raw)
	}
	if !strings.Contains(raw, `"modelProvider":"two"`) {
		t.Errorf("this handle's managed change was lost: %s", raw)
	}
}

func TestMergeManaged(t *testing.T) {
	got := mergeManaged(
		SettingsDoc{ModelProvider: "gemini", CustomModels: map[string]CustomModelConfig{"k": {ModelName: "k"}}},
		SettingsDoc{ModelProvider: "other", CustomModels: map[string]CustomModelConfig{"disk": {ModelName: "disk"}}},
	)
	if got.ModelProvider != "gemini" {
		t.Errorf("ModelProvider = %q; want the caller's intent to win", got.ModelProvider)
	}
	if _, ok := got.CustomModels["k"]; !ok {
		t.Error("the caller's customModels were dropped")
	}

	// A caller who never touched a managed key keeps whatever is on disk.
	onDisk := SettingsDoc{ModelProvider: "other"}
	got = mergeManaged(SettingsDoc{}, onDisk)
	if got.ModelProvider != "other" {
		t.Errorf("ModelProvider = %q; want the on-disk value to survive an empty intent", got.ModelProvider)
	}
}
