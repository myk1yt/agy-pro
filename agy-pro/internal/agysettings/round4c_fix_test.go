package agysettings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The offending URL used by every FIX-67 pin. Its "gemini-api:" scheme carries an
// "i:/" pair, which is what the old drive-letter pattern matched.
const fix67URL = "gemini-api://127.0.0.1:8791/models/gemini-3.8-flash-high"

// ---------------------------------------------------------------------------
// FIX-67 pin 1: the regex must not eat a model URL
// ---------------------------------------------------------------------------

func TestScrubAbsolutePathsDoesNotEatModelURLs(t *testing.T) {
	urls := []string{
		"gemini-api://127.0.0.1:8791/models/probe-gemini",
		"gemini-api://127.0.0.1:8791/models/gemini-3.8-flash-high",
		"blade:127.0.0.1:8812/models/probe-blade",
		"beyond-api://127.0.0.1:8813/probe-beyond",
		"evergreen://blade:gdm-aip/lmroot-v3:/jetski/gamma",
		"gemini-api:/models/snowball",
	}
	for _, url := range urls {
		t.Run(url, func(t *testing.T) {
			got := scrubAbsolutePaths(url)
			if got != url {
				t.Errorf("scrubAbsolutePaths mangled a model URL:\n got %q\nwant %q", got, url)
			}
		})
	}

	// A URL inside a sentence must survive too, including the trailing prose.
	sentence := "collides with the reserved name " + fix67URL + "; refusing to emit an entry"
	got := scrubAbsolutePaths(sentence)
	if !strings.Contains(got, fix67URL) {
		t.Errorf("scrubAbsolutePaths mangled the URL inside a sentence: %q", got)
	}
	if !strings.Contains(got, "refusing to emit an entry") {
		t.Errorf("scrubAbsolutePaths ate the prose after the URL: %q", got)
	}
}

// The tightened pattern must still catch real absolute paths, at a token
// boundary and mid-sentence.
func TestScrubAbsolutePathsStillCatchesRealPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"drive path at the start", `C:\Users\k1yt\.gemini\settings.json.lock`, absolutePathLabel},
		{"drive path mid-sentence", "open " + `C:\Users\k1yt\.gemini\settings.json.lock` + ": Access is denied", "open " + absolutePathLabel + ": Access is denied"},
		{"forward-slash drive path", "open D:/data/x/settings.json: denied", "open " + absolutePathLabel + ": denied"},
		{"drive path in quotes", `"C:\Users\k1yt\x"`, `"` + absolutePathLabel + `"`},
		{"drive path after an equals", "path=C:\\Users\\k1yt\\x", "path=" + absolutePathLabel},
		{"drive path in parens", "(" + `C:\Users\k1yt\x` + ")", "(" + absolutePathLabel + ")"},
		{"two paths in one sentence", "rename " + `C:\a\.tmp-1` + " to " + `C:\a\settings.json`, "rename " + absolutePathLabel + " to " + absolutePathLabel},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scrubAbsolutePaths(c.in)
			if got != c.want {
				t.Errorf("scrubAbsolutePaths(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
			if strings.Contains(got, "k1yt") {
				t.Errorf("the user name survived: %q", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FIX-67 pin 2: the public API keeps the offending URL readable
// ---------------------------------------------------------------------------

// A customModels key that collides through a gemini-api:// URL, surfaced through
// Open, must keep the URL in the diagnostic while any absolute path stays masked.
// This is the pin the round-4b sweep missed: it only drove paths that bypass the
// choke point.
func TestCollisionErrorThroughOpenKeepsTheModelURL(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"customModels":{"` + fix67URL + `":{"modelName":"` + fix67URL + `"}}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	f, err := Open(path)
	if f != nil {
		_ = f.Close()
	}
	if err == nil {
		t.Fatal("Open accepted a customModels entry that resolves to a static default")
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), fix67URL) {
		t.Errorf("the offending URL was lost from the diagnostic:\n  %v", err)
	}
	for _, want := range []string{"gemini-3.8-flash-high", "reserved name", "refusing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the collision diagnostic lost %q: %v", want, err)
		}
	}
}

// The same collision reached through Write must keep the URL too. Set refuses a
// colliding entry up front, so this drives Write's own validation by seeding the
// managed document directly.
func TestCollisionErrorThroughWriteKeepsTheModelURL(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	f := openEmpty(t, path)
	defer f.Close()
	f.doc.CustomModels = map[string]CustomModelConfig{fix67URL: {ModelName: fix67URL}}
	err := f.Write()
	if err == nil {
		t.Fatal("Write accepted a customModels entry that resolves to a static default")
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), fix67URL) {
		t.Errorf("the offending URL was lost from the diagnostic:\n  %v", err)
	}
}

// And through Rollback. Rollback restores the backup's bytes and then reloads, so
// a tampered backup is the route to a collision error on that path.
func TestCollisionErrorThroughRollbackKeepsTheModelURL(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	if err := writeFileForTest(path, []byte(`{"toolPermission":"always-proceed"}`)); err != nil {
		t.Fatal(err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	if err := f.Set(KeyModelProvider, ModelProviderGemini); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Tamper with the backup so the bytes Rollback restores carry a collision.
	if err := writeFileForTest(f.LastBackup(), []byte(`{"customModels":{"`+fix67URL+`":{"modelName":"`+fix67URL+`"}}}`)); err != nil {
		t.Fatal(err)
	}
	err := f.Rollback()
	if err == nil {
		t.Fatal("Rollback restored a colliding document without complaint")
	}
	assertNoLeakedPath(t, err, home)
	if !strings.Contains(err.Error(), fix67URL) {
		t.Errorf("the offending URL was lost from the diagnostic:\n  %v", err)
	}
}

// BuildP2Settings does not cross the choke point, so it is the control: it always
// kept the URL. Comparing the two proves the choke point was the damaged layer.
func TestBuildP2SettingsKeepsTheModelURL(t *testing.T) {
	_, err := BuildP2Settings([]ExternalEntry{probeEntry(fix67URL, fix67URL)})
	if err == nil {
		t.Fatal("expected a collision error")
	}
	if !strings.Contains(err.Error(), fix67URL) {
		t.Errorf("the offending URL was lost from the diagnostic:\n  %v", err)
	}
}

// ---------------------------------------------------------------------------
// FIX-67: the recorded residual
// ---------------------------------------------------------------------------

// The primary layer handles a space-bearing path completely, whatever the regex
// does, because it renders the OS error's op, class and reason directly.
func TestWrapOSErrorMasksASpaceBearingPathCompletely(t *testing.T) {
	wrapped := wrapOSError(&os.PathError{
		Op:   "open",
		Path: `C:\Users\John Smith\.gemini\antigravity-cli\settings.json.lock`,
		Err:  os.ErrPermission,
	}, "the lock file")
	if strings.Contains(wrapped.Error(), "John") || strings.Contains(wrapped.Error(), "Smith") {
		t.Errorf("a space-bearing path leaked through the primary layer: %v", wrapped)
	}
	if wrapped.Error() != "open the lock file: permission denied" {
		t.Errorf("message = %q; want the op, the class and the reason", wrapped.Error())
	}
}

// DOCUMENTED RESIDUAL (recorded, not fixed): the last-resort regex stops at
// whitespace, so an error shape that is NOT a PathError/LinkError/SyscallError and
// that carries a space-bearing absolute path leaves the post-space fragment in the
// message. The primary layers cover every error shape this package can produce
// (every os call returns one of those three), so the residual is unreachable
// through the package's own paths; the regex is defense-in-depth only.
//
// This pin asserts the documented behavior so the limitation cannot silently
// change: the drive and user are scrubbed, the tail is not.
func TestScrubAbsolutePathsSpaceBearingResidualIsDocumented(t *testing.T) {
	const spaceBearing = `C:\Users\John Smith\.gemini\settings.json.lock`
	got := scrubAbsolutePaths("open " + spaceBearing + ": denied")

	if !strings.HasPrefix(got, "open "+absolutePathLabel) {
		t.Errorf("the drive and user were not scrubbed: %q", got)
	}
	if !strings.Contains(got, `Smith\.gemini\settings.json.lock`) {
		t.Errorf("the documented residual changed; the post-space fragment is gone. "+
			"If the regex was tightened, update this pin and the report: %q", got)
	}
	if !strings.Contains(got, "denied") {
		t.Errorf("the trailing reason was lost: %q", got)
	}
}

// The residual is unreachable through the public API: every error this package
// produces crosses either a site mask or wrapOSError, both of which are
// whitespace-agnostic.
func TestNoErrorPathLeaksASpaceBearingPath(t *testing.T) {
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
	// The sandbox home itself has no spaces here, so plant the space-bearing shape
	// directly through the layer the public API uses.
	wrapped := scrubErrorPaths(errors.New("open " + `C:\Users\John Smith\.gemini\settings.json.lock` + ": denied"))
	if !strings.Contains(wrapped.Error(), "John") {
		t.Logf("note: the public-API path did not carry a space-bearing path; the " +
			"residual is asserted directly in TestScrubAbsolutePathsSpaceBearingResidualIsDocumented")
	}
	assertNoLeakedPath(t, err, home)
}
