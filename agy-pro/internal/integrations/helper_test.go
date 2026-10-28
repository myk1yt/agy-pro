package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agypro/internal/paths"
)

// realHome is the user's real home directory, resolved once before any test
// overrides USERPROFILE/HOME. It exists only for a leak assertion: no test in
// this package may write inside it. Nothing here reads or stats the real
// ~/.agy-pro or ~/.gemini.
var realHome string

func TestMain(m *testing.M) {
	if home, err := os.UserHomeDir(); err == nil {
		realHome = filepath.Clean(home)
	}
	os.Exit(m.Run())
}

// sandboxHome points USERPROFILE and HOME at ONE fresh temp directory and
// returns it. os.UserHomeDir honors USERPROFILE on Windows and HOME elsewhere,
// so setting both makes every home-derived path in this package resolve inside
// the sandbox on any platform (the discipline agy-pro/internal/dashboard's
// one-shot smoke and agy-pro/internal/agysettings both use).
func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

// sandboxSplitHome points USERPROFILE and HOME at TWO DIFFERENT temp
// directories and returns both. It is the F-W1-17 pin: a resolver that reads
// the wrong variable resolves inside the wrong tree, and this makes that
// observable.
func sandboxSplitHome(t *testing.T) (profile, home string) {
	t.Helper()
	profile = t.TempDir()
	home = t.TempDir()
	if filepath.Clean(profile) == filepath.Clean(home) {
		t.Fatalf("the profile and home sandboxes resolve to the same directory: %q", home)
	}
	t.Setenv("USERPROFILE", profile)
	t.Setenv("HOME", home)
	return profile, home
}

// sandboxStore points the agy-pro home at a fresh temp directory and returns
// the manifest path the production resolver produces inside it. Every store
// test goes through this helper, so no test ever writes the real ~/.agy-pro.
func sandboxStore(t *testing.T) string {
	t.Helper()
	storeHome := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, storeHome)
	manifestPath, err := ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	wantPath := filepath.Join(storeHome, ManifestFileName)
	if manifestPath != wantPath {
		t.Fatalf("ManifestPath = %q, want %q", manifestPath, wantPath)
	}
	return manifestPath
}

// clearToolkitEnv removes the toolkit-root override so the marker rung is the
// one under test.
func clearToolkitEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvToolkitRoot, "")
}

// sampleSHA is a valid lowercase sha256 digest for entries that only need to
// pass validation.
const sampleSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// sampleTime is a fixed timestamp, so a stamped manifest is byte-predictable.
var sampleTime = time.Date(2026, 10, 22, 3, 4, 5, 0, time.UTC)

// newEntry builds a valid entry for a destination.
func newEntry(destination string, provenance Provenance) ManifestEntry {
	return ManifestEntry{
		Path:       destination,
		SHA256:     sampleSHA,
		Provenance: provenance,
		BackupRef:  "backups/20261022_030405",
	}
}

// mustSet records an entry and fails the test if the store refuses it.
func mustSet(t *testing.T, manifest *Manifest, entry ManifestEntry) {
	t.Helper()
	if err := manifest.Set(entry); err != nil {
		t.Fatalf("Set(%q): %v", entry.Path, err)
	}
}

// writeFileRaw writes bytes verbatim, creating parent directories.
func writeFileRaw(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

// readFileRaw reads a file and fails the test when it is missing.
func readFileRaw(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return data
}

// decodeRawJSON decodes a JSON document into raw values, so a test can assert
// on keys the typed store does not model.
func decodeRawJSON(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode raw json: %v\n%s", err, string(data))
	}
	return doc
}

// decodeAny decodes one raw JSON value for semantic comparison.
func decodeAny(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode raw value: %v\n%s", err, string(raw))
	}
	return value
}

// fakeToolkitRoot creates a directory that passes validateToolkitRoot: it
// carries the bin/agy-tokens.js entry the launchers run.
func fakeToolkitRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeToolkitEntry(t, root)
	return root
}

// writeToolkitEntry makes root look like a toolkit clone root.
func writeToolkitEntry(t *testing.T, root string) {
	t.Helper()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", binDir, err)
	}
	entry := filepath.Join(binDir, "agy-tokens.js")
	if err := os.WriteFile(entry, []byte("#!/usr/bin/env node\n"), 0o644); err != nil {
		t.Fatalf("write %q: %v", entry, err)
	}
}

// hostMarkerPath returns the root-marker path the running OS uses below home.
func hostMarkerPath(home string) string {
	return markerPathFor(runtime.GOOS, home)
}

// writeMarker writes a marker file (creating parents) with the given content.
func writeMarker(t *testing.T, markerPath, content string) {
	t.Helper()
	writeFileRaw(t, markerPath, []byte(content))
}

// assertContains fails unless haystack contains every needle.
func assertContains(t *testing.T, label, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Fatalf("%s = %q, want it to contain %q", label, haystack, needle)
		}
	}
}
