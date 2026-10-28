package agysettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The USERPROFILE/HOME override redirects every path this package resolves.
func TestSandboxOverrideRedirectsEveryPath(t *testing.T) {
	home := sandboxHome(t)

	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	session, err := AppDataDirSettingsPath("agy-hybrid-sandbox")
	if err != nil {
		t.Fatalf("AppDataDirSettingsPath: %v", err)
	}
	for _, p := range []string{global, session, lockPath(global), global + ".bak.20261015-030000"} {
		if !isUnder(p, home) {
			t.Errorf("path %q is not inside the sandbox home %q", p, home)
		}
	}
}

// A canary planted in the sandbox home proves the whole regime runs there: the
// managed write lands on the canary, the canary's previous bytes are backed up,
// and nothing is created outside the sandbox.
func TestSandboxCanaryWriteNeverLeavesTheSandbox(t *testing.T) {
	home := sandboxHome(t)

	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := []byte(`{"toolPermission":"always-proceed","canary":"do-not-touch"}`)
	if err := writeFileForTest(global, canary); err != nil {
		t.Fatal(err)
	}
	canaryHash := md5Sum(canary)
	before := treeFiles(t, home)

	// The canary lives at the sandbox's global path, so this test opts in
	// explicitly: it is the one place the package is allowed to touch that path
	// class, and it does so only to prove the regime is confined to the sandbox.
	f, err := OpenWithOptions(global, OpenOptions{AllowGlobal: true})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
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

	if got := string(readFileForTest(t, global)); !strings.Contains(got, `"modelProvider":"gemini"`) {
		t.Errorf("the sandbox settings file was not written: %s", got)
	}
	if !strings.Contains(string(readFileForTest(t, global)), "canary") {
		t.Error("the managed write dropped the canary's unmanaged keys")
	}
	backup := f.LastBackup()
	if backup == "" {
		t.Fatal("no backup of the canary was recorded")
	}
	if got := md5Sum(readFileForTest(t, backup)); got != canaryHash {
		t.Errorf("the canary backup was not byte-identical: %x; want %x", got, canaryHash)
	}

	seen := map[string]bool{}
	for _, name := range treeFiles(t, home) {
		seen[name] = true
	}
	assertOnlySiblings(t, home, global)
	for _, name := range before {
		if !seen[name] {
			t.Errorf("the write cycle deleted the pre-existing sandbox file %s", name)
		}
	}
}

// The P2 app-data-dir regime runs in its own directory and leaves the global
// settings file alone.
func TestSandboxAppDataDirRegimeLeavesGlobalSettingsAlone(t *testing.T) {
	home := sandboxHome(t)

	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(global), 0o700); err != nil {
		t.Fatal(err)
	}
	globalBytes := []byte(`{"toolPermission":"always-proceed"}`)
	if err := writeFileForTest(global, globalBytes); err != nil {
		t.Fatal(err)
	}
	globalHash := md5Sum(globalBytes)

	session, err := AppDataDirSettingsPath("agy-hybrid-regime")
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
	for key, value := range doc {
		if err := f.Set(key, value); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := md5Sum(readFileForTest(t, global)); got != globalHash {
		t.Errorf("the global settings file changed during a P2 session: %x; want %x", got, globalHash)
	}
	if _, err := os.Stat(lockPath(global)); !os.IsNotExist(err) {
		t.Errorf("the global settings file gained a lockfile sibling (stat err = %v)", err)
	}
	if got := string(readFileForTest(t, session)); !strings.Contains(got, `"modelProvider":"gemini"`) {
		t.Errorf("the session settings file was not written: %s", got)
	}
	if !isUnder(session, filepath.Join(home, ".gemini", "agy-hybrid-regime")) {
		t.Errorf("session path %q is not under its own app-data dir", session)
	}
}

// Nothing this package resolves can address the real profile while the sandbox
// override is in place. The proof is anchoring, not inspection: every resolved
// path must be rooted at the sandbox home, and the sandbox home must differ
// from the real home. The real ~/.gemini is never read or stat'ed.
func TestSandboxNeverResolvesIntoTheRealProfile(t *testing.T) {
	home := sandboxHome(t)

	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	session, err := AppDataDirSettingsPath("agy-hybrid-real-check")
	if err != nil {
		t.Fatalf("AppDataDirSettingsPath: %v", err)
	}

	// Anchoring: the global path is exactly <sandbox home>/.gemini/antigravity-cli/settings.json.
	wantGlobal := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if global != wantGlobal {
		t.Fatalf("GlobalSettingsPath = %q; want %q (the resolution root must be the sandbox home)", global, wantGlobal)
	}
	wantSession := filepath.Join(home, ".gemini", "agy-hybrid-real-check", "settings.json")
	if session != wantSession {
		t.Fatalf("AppDataDirSettingsPath = %q; want %q", session, wantSession)
	}
	if realHomeForIsolationCheck == "" {
		t.Skip("the real home directory could not be resolved at startup")
	}
	if filepath.Clean(home) == realHomeForIsolationCheck {
		t.Fatal("the sandbox home is the real home; the override did not take effect")
	}
	// The real profile's own settings file is a different path from anything we
	// resolved, so no write can land on it.
	realGlobal := filepath.Join(realHomeForIsolationCheck, ".gemini", "antigravity-cli", "settings.json")
	for _, p := range []string{global, session, lockPath(global), global + ".bak.20261015-030000"} {
		if p == realGlobal {
			t.Errorf("path %q is the real global settings file", p)
		}
		if isUnder(p, realHomeForIsolationCheck) && !isUnder(home, realHomeForIsolationCheck) {
			t.Errorf("path %q resolves into the real profile %q while the sandbox home %q is outside it", p, realHomeForIsolationCheck, home)
		}
	}
}

// The sandbox is the only sanctioned mechanism, and it must work for a path the
// caller hands in directly as well.
func TestSandboxAppliesToCallerSuppliedPaths(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "caller-supplied", "settings.json")

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

	if !isUnder(path, home) {
		t.Errorf("path %q is not inside the sandbox home %q", path, home)
	}
	if got := string(readFileForTest(t, path)); got != `{"modelProvider":"gemini"}` {
		t.Errorf("written bytes = %s", got)
	}
}
