package agysettings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// leakedHome reports whether an error string carries any part of the sandbox
// home that would identify the machine or the user: the full resolved path, or
// the unique per-test temp directory that contains it.
func leakedHome(t *testing.T, err error, home string) (string, bool) {
	t.Helper()
	if err == nil {
		return "", false
	}
	msg := err.Error()
	probes := []string{home, filepath.Dir(home)}
	for _, probe := range probes {
		if probe == "" || probe == string(filepath.Separator) {
			continue
		}
		if strings.Contains(strings.ToLower(msg), strings.ToLower(probe)) {
			return probe, true
		}
	}
	return "", false
}

// assertNoLeakedPath fails when err carries any part of the sandbox home.
func assertNoLeakedPath(t *testing.T, err error, home string) {
	t.Helper()
	if probe, leaked := leakedHome(t, err, home); leaked {
		t.Errorf("the error leaked the local path %q:\n  %v", probe, err)
	}
}

func TestMaskedPathDropsTheDirectoryLayout(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	lock := lockPath(settings)
	backup := settings + ".bak.20261015-035441"

	cases := []struct {
		name  string
		path  string
		label string
		want  string
	}{
		{"settings file keeps its name", settings, "the settings file", settingsFileName},
		{"lock file keeps its name", lock, "the lock file", settingsFileName + ".lock"},
		{"backup keeps its name", backup, "the backup file", settingsFileName + ".bak.20261015-035441"},
		{"an arbitrary target collapses", filepath.Join(home, "alice-secrets.json"), "the settings file", "the settings file"},
		{"a nested target collapses", filepath.Join(home, "a", "b", "c.json"), "the settings file", "the settings file"},
		{"the directory label survives", filepath.Dir(settings), "the settings directory", "the settings directory"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := maskedPath(c.path, c.label)
			if got != c.want {
				t.Errorf("maskedPath(%q, %q) = %q; want %q", c.path, c.label, got, c.want)
			}
			if strings.Contains(got, home) {
				t.Errorf("maskedPath kept the directory layout: %q", got)
			}
			if strings.Contains(got, filepath.Base(filepath.Dir(home))) {
				t.Errorf("maskedPath kept the temp directory name: %q", got)
			}
		})
	}
}

func TestMaskOperandKeepsRelativeValuesAndMasksAbsolute(t *testing.T) {
	// Relative, caller-supplied values stay verbatim: the operator needs them to
	// fix the call.
	for _, v := range []string{"../evil", "agy-hybrid-abc", "nest/deep", "./x/", "gemini-api://127.0.0.1:8791/models/probe", "blade:127.0.0.1:8812/models/beta"} {
		if got := maskOperand(v); got != v {
			t.Errorf("maskOperand(%q) = %q; want the value verbatim", v, got)
		}
	}
	// Absolute values collapse, on every spelling.
	for _, v := range []string{`C:\Users\k1yt\.gemini`, `C:/Users/k1yt`, `C:x`, `/etc/agy`, `\\srv\share`} {
		got := maskOperand(v)
		if got == v {
			t.Errorf("maskOperand(%q) kept the absolute path raw", v)
		}
		if !strings.Contains(got, "absolute") {
			t.Errorf("maskOperand(%q) = %q; want an absolute-path label", v, got)
		}
	}
	if got := maskOperand("   "); got == "   " {
		t.Error("maskOperand echoed whitespace verbatim")
	}
}

// ---------------------------------------------------------------------------
// FIX-56 pin 1: the lock-contention error
// ---------------------------------------------------------------------------

// The lock-contention error must name the file class and carry no part of the
// sandbox's absolute path. Both lockedError branches are covered: with a parsed
// acquired timestamp and without one.
func TestLockContentionErrorHidesTheSandboxPath(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), settingsDirPerm); err != nil {
		t.Fatal(err)
	}

	// Branch A: a live, parseable lock.
	live := lockContent{pid: "4242", acquired: time.Now(), owner: "live-writer-token"}
	if err := writeFileForTest(lockPath(path), live.encode()); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Open error = %v; want ErrLocked", err)
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), settingsFileName+".lock") {
		t.Errorf("the error no longer names the file class: %v", err)
	}
	if !strings.Contains(err.Error(), "pid 4242") {
		t.Errorf("the error lost the holder identity: %v", err)
	}
	if !strings.Contains(err.Error(), "older than") {
		t.Errorf("the error lost the stale-bound explanation: %v", err)
	}

	// Branch B: a young but unparseable lock, which falls back to the file age.
	if err := writeFileForTest(lockPath(path), []byte("garbage-from-a-crash")); err != nil {
		t.Fatal(err)
	}
	_, err = Open(path)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Open error = %v; want ErrLocked for the unparseable branch", err)
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), settingsFileName+".lock") {
		t.Errorf("the unparseable branch no longer names the file class: %v", err)
	}
}

// A lock held across the stale bound that cannot be broken reports the same
// masked form.
func TestStaleBreakFailureHidesTheSandboxPath(t *testing.T) {
	narrowLocks(t)
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	lock := lockPath(path)
	if err := os.MkdirAll(filepath.Dir(path), settingsDirPerm); err != nil {
		t.Fatal(err)
	}
	plantStaleLock(t, lock)
	// A read-only stale lock cannot be truncated, so the takeover fails.
	if err := os.Chmod(lock, 0o400); err != nil {
		t.Skipf("cannot make the lock read-only here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })

	_, err := Open(path)
	if err == nil {
		t.Skip("the platform allowed truncating a read-only lock; the failure path is unreachable here")
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), settingsFileName+".lock") {
		t.Errorf("the error no longer names the file class: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FIX-56 pin 2: the traversal / absolute rejection errors
// ---------------------------------------------------------------------------

func TestTraversalAndAbsoluteRejectionHideTheSandboxPath(t *testing.T) {
	home := sandboxHome(t)

	t.Run("traversal", func(t *testing.T) {
		for _, v := range []string{"..", "../evil", "../../evil", "agy-hybrid/../../evil"} {
			_, err := AppDataDirSettingsPath(v)
			if err == nil {
				t.Fatalf("AppDataDirSettingsPath(%q) succeeded; want an error", v)
			}
			assertNoLeakedPath(t, err, home)
			if !strings.Contains(err.Error(), "escape") {
				t.Errorf("the error lost its diagnostic: %v", err)
			}
			// The relative operand the caller passed stays readable.
			if !strings.Contains(err.Error(), v) {
				t.Errorf("the error dropped the offending value %q: %v", v, err)
			}
		}
	})

	t.Run("absolute", func(t *testing.T) {
		// An absolute operand collapses: echoing it would put a raw local path
		// into the error string.
		for _, v := range []string{`C:\Users\k1yt\.gemini\appdata`, `C:/Users/k1yt`, `/etc/agy`} {
			_, err := AppDataDirSettingsPath(v)
			if err == nil {
				t.Fatalf("AppDataDirSettingsPath(%q) succeeded; want an error", v)
			}
			assertNoLeakedPath(t, err, home)
			if strings.Contains(err.Error(), v) {
				t.Errorf("the error echoed the raw absolute path %q: %v", v, err)
			}
			if !strings.Contains(err.Error(), "relative") {
				t.Errorf("the error lost its diagnostic: %v", err)
			}
		}
	})

	t.Run("segment", func(t *testing.T) {
		_, err := AppDataDirSettingsPath("agy/C:/x")
		if err == nil {
			t.Fatal("AppDataDirSettingsPath with a volume segment succeeded; want an error")
		}
		assertNoLeakedPath(t, err, home)
	})
}

// ---------------------------------------------------------------------------
// FIX-56 pin 3: no error path in the package leaks the sandbox path
// ---------------------------------------------------------------------------

// errorScenario drives one reachable error path of the package.
type errorScenario struct {
	name string
	run  func(t *testing.T, home string) error
}

func errorScenarios() []errorScenario {
	return []errorScenario{
		{"open on a directory target", func(t *testing.T, home string) error {
			dir := filepath.Join(home, "dir-target")
			if err := os.MkdirAll(dir, settingsDirPerm); err != nil {
				t.Fatal(err)
			}
			f, err := Open(dir)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"malformed settings", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(`{"modelProvider": `)); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"modelProvider with the wrong shape", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(`{"modelProvider": 42}`)); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"customModels with the wrong shape", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(`{"customModels": "nope"}`)); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"write on a closed file", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			f := openEmpty(t, path)
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			return f.Write()
		}},
		{"rollback on a closed file", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			f := openEmpty(t, path)
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			return f.Rollback()
		}},
		{"rollback with no backup", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			f := openEmpty(t, path)
			defer f.Close()
			if err := f.Write(); err != nil {
				t.Fatalf("Write: %v", err)
			}
			return f.Rollback()
		}},
		{"rollback with a missing backup", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := writeFileForTest(path, []byte(`{"toolPermission":"always-proceed"}`)); err != nil {
				t.Fatal(err)
			}
			f := openEmpty(t, path)
			defer f.Close()
			if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
				t.Fatal(err)
			}
			if err := f.Write(); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := os.Remove(f.LastBackup()); err != nil {
				t.Fatalf("remove the backup to force the error: %v", err)
			}
			return f.Rollback()
		}},
		{"a directory at the lock path", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := os.MkdirAll(lockPath(path), settingsDirPerm); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"a live lock blocks the open", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			if err := os.MkdirAll(filepath.Dir(path), settingsDirPerm); err != nil {
				t.Fatal(err)
			}
			live := lockContent{pid: "1", acquired: time.Now(), owner: "someone-else"}
			if err := writeFileForTest(lockPath(path), live.encode()); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"a reserved customModels entry on disk", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			planted := `{"customModels":{"gemini-api://h/models/gemini-3.8-flash-high":{"modelName":"gemini-api://h/models/gemini-3.8-flash-high"}}}`
			if err := writeFileForTest(path, []byte(planted)); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if f != nil {
				_ = f.Close()
			}
			return err
		}},
		{"a duplicate customModels key", func(_ *testing.T, _ string) error {
			_, err := BuildP2Settings([]ExternalEntry{
				probeEntry("gemini-api://h/models/probe", "gemini-api://h/models/probe"),
				probeEntry("gemini-api://h/models/probe", "gemini-api://h/models/probe"),
			})
			return err
		}},
		{"a bare customModels key", func(_ *testing.T, _ string) error {
			_, err := BuildP2Settings([]ExternalEntry{probeEntry("probe-gemini", "probe-gemini")})
			return err
		}},
		{"a static-id collision", func(_ *testing.T, _ string) error {
			_, err := BuildP2Settings([]ExternalEntry{probeEntry("gemini-api://h/models/gemini-3.1-pro-low", "gemini-api://h/models/gemini-3.1-pro-low")})
			return err
		}},
		{"a missing modelName", func(_ *testing.T, _ string) error {
			_, err := BuildP2Settings([]ExternalEntry{{URL: "gemini-api://h/models/probe"}})
			return err
		}},
		{"an unmanaged key", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			f := openEmpty(t, path)
			defer f.Close()
			return f.Set("toolPermission", "x")
		}},
		{"a wrong value shape", func(t *testing.T, home string) error {
			path := filepath.Join(home, "settings.json")
			f := openEmpty(t, path)
			defer f.Close()
			return f.Set(KeyModelProvider, 42)
		}},
	}
}

// Every reachable error path must report a class, never a local path.
func TestNoErrorPathLeaksTheSandboxPath(t *testing.T) {
	for _, scenario := range errorScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			home := sandboxHome(t)
			err := scenario.run(t, home)
			if err == nil {
				t.Fatalf("the scenario did not produce an error; the pin would be vacuous")
			}
			assertNoLeakedPath(t, err, home)
			if strings.TrimSpace(err.Error()) == "" {
				t.Error("the error carries no diagnostic at all")
			}
		})
	}
}

// The two named pins plus the sweep must keep naming what failed, so an operator
// reading only the error still knows which file class to look at.
func TestMaskedErrorsStayActionable(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	if err := writeFileForTest(path, []byte(`{"modelProvider": `)); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("expected a malformed-settings error")
	}
	if !strings.Contains(err.Error(), settingsFileName) && !strings.Contains(err.Error(), "the settings file") {
		t.Errorf("the malformed-settings error no longer names the file class: %v", err)
	}
	for _, want := range []string{"malformed", "refusing to overwrite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the malformed-settings error lost %q: %v", want, err)
		}
	}

	_, err = AppDataDirSettingsPath("../evil")
	if err == nil {
		t.Fatal("expected a traversal error")
	}
	for _, want := range []string{"app_data_dir", "escapes", "../evil"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the traversal error lost %q: %v", want, err)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-56 pin 4: masking must not destroy the diagnostic
// ---------------------------------------------------------------------------

// The scrub is a safety net, not a blunt instrument: a legitimately readable
// value (a model URL, a relative operand the caller passed) must survive it.
func TestScrubDoesNotMangleReadableMessages(t *testing.T) {
	_, err := BuildP2Settings([]ExternalEntry{probeEntry("probe-gemini", "probe-gemini")})
	if err == nil {
		t.Fatal("expected a bare-key error")
	}
	for _, want := range []string{"probe-gemini", "gemini-api://<base_url>/models/<model_name>", "beyond-api://<base_url>/<model_name>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the scrub mangled the model-URL diagnostic, lost %q: %v", want, err)
		}
	}

	_, err = AppDataDirSettingsPath("../evil")
	if err == nil {
		t.Fatal("expected a traversal error")
	}
	if !strings.Contains(err.Error(), "../evil") {
		t.Errorf("the scrub mangled the relative operand: %v", err)
	}
}

// scrubErrorPaths rewrites only when there is something to rewrite, and keeps
// the chain unwrappable when it does.
func TestScrubErrorPathsRewritesOnlyWhenNeeded(t *testing.T) {
	clean := errors.New("nothing to hide here")
	if got := scrubErrorPaths(clean); got != clean {
		t.Errorf("scrubErrorPaths rewrote a clean error: %v", got)
	}
	if scrubErrorPaths(nil) != nil {
		t.Error("scrubErrorPaths(nil) is not nil")
	}

	leaking := errors.New("open " + `C:\Users\k1yt\.gemini\settings.json.lock` + ": Access is denied")
	got := scrubErrorPaths(leaking)
	if got == leaking {
		t.Fatal("scrubErrorPaths left a leaking error untouched")
	}
	if strings.Contains(got.Error(), "k1yt") {
		t.Errorf("the rewrite still carries the path: %v", got)
	}
	if !strings.Contains(got.Error(), "Access is denied") {
		t.Errorf("the rewrite lost the OS reason: %v", got)
	}
	if !errors.Is(got, leaking) {
		t.Error("the rewrite broke the error chain")
	}
}

// Masking the OS error must not cost the caller the ability to test for a
// syscall errno.
func TestWrapOSErrorKeepsTheChainUnwrappable(t *testing.T) {
	wrapped := wrapOSError(&os.PathError{Op: "open", Path: `C:\Users\k1yt\.gemini\settings.json.lock`, Err: os.ErrNotExist}, "the lock file")
	if !errors.Is(wrapped, os.ErrNotExist) {
		t.Error("wrapOSError broke errors.Is on the syscall errno")
	}
	if strings.Contains(wrapped.Error(), "k1yt") {
		t.Errorf("wrapOSError kept the path: %v", wrapped)
	}
	if wrapped.Error() != "open the lock file: file does not exist" {
		t.Errorf("wrapOSError message = %q; want the op, the class and the reason", wrapped.Error())
	}
	if wrapOSError(nil, "the lock file") != nil {
		t.Error("wrapOSError(nil) is not nil")
	}

	link := wrapOSError(&os.LinkError{Op: "rename", Old: `C:\a\.tmp-1`, New: `C:\a\settings.json`, Err: os.ErrPermission}, "the settings file")
	if strings.Contains(link.Error(), `C:\a`) {
		t.Errorf("wrapOSError kept a LinkError path: %v", link)
	}
	if !strings.Contains(link.Error(), "rename the settings file") {
		t.Errorf("wrapOSError lost the LinkError shape: %v", link)
	}

	sys := wrapOSError(&os.SyscallError{Syscall: "flock", Err: os.ErrInvalid}, "the lock file")
	if strings.Contains(sys.Error(), "flock") == false {
		t.Errorf("wrapOSError lost the SyscallError shape: %v", sys)
	}
}

func TestIsAbsolutePathCoversEverySpelling(t *testing.T) {
	for _, v := range []string{`C:\x`, `C:/x`, `C:x`, `/etc`, `\\srv\share`, `\x`} {
		if !isAbsolutePath(v) {
			t.Errorf("isAbsolutePath(%q) = false; want true", v)
		}
	}
	for _, v := range []string{"", "  ", "../evil", "agy-hybrid-abc", "gemini-api://127.0.0.1:8791/models/probe", "blade:h/models/b"} {
		if isAbsolutePath(v) {
			t.Errorf("isAbsolutePath(%q) = true; want false", v)
		}
	}
}
