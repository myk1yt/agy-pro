package integrations

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestMarkerPathForOS pins the per-OS marker spelling against the files wave 1
// landed: scripts/lib/make-launchers.js MARKER_TAIL (Windows) and
// scripts/install.sh ROOT_MARKER (POSIX). The function takes the OS as an
// argument, so both spellings are pinned on whatever host runs the suite.
func TestMarkerPathForOS(t *testing.T) {
	cases := []struct {
		name string
		goos string
		home string
		want string
	}{
		{
			name: "windows",
			goos: "windows",
			home: `C:\Users\tester`,
			want: `C:\Users\tester\.gemini\antigravity-cli\agy-root.txt`,
		},
		{
			name: "windows with trailing separator",
			goos: "windows",
			home: `C:\Users\tester\`,
			want: `C:\Users\tester\.gemini\antigravity-cli\agy-root.txt`,
		},
		{
			name: "GOOS spelling in any case",
			goos: "Windows",
			home: `C:\Users\tester`,
			want: `C:\Users\tester\.gemini\antigravity-cli\agy-root.txt`,
		},
		{
			name: "linux",
			goos: "linux",
			home: "/home/tester",
			want: "/home/tester/.local/share/agy-tools-root",
		},
		{
			name: "darwin",
			goos: "darwin",
			home: "/Users/tester",
			want: "/Users/tester/.local/share/agy-tools-root",
		},
		{
			name: "posix with trailing separator",
			goos: "linux",
			home: "/home/tester/",
			want: "/home/tester/.local/share/agy-tools-root",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if got := markerPathFor(testCase.goos, testCase.home); got != testCase.want {
				t.Fatalf("markerPathFor(%q, %q) = %q, want %q", testCase.goos, testCase.home, got, testCase.want)
			}
		})
	}
}

// TestToolkitRootMarkerPaths pins the host ladder: the first rung is the
// OS-correct primary marker, and a POSIX host also consults the Node-written
// marker as a fallback (decision a: install.sh marker first, then the file
// scripts/lib/make-launchers.js writes on every OS).
func TestToolkitRootMarkerPaths(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)

	markers, err := ToolkitRootMarkerPaths()
	if err != nil {
		t.Fatalf("ToolkitRootMarkerPaths: %v", err)
	}
	want := markerPathsFor(runtime.GOOS, home)
	if len(markers) != len(want) {
		t.Fatalf("ToolkitRootMarkerPaths() = %v, want %v", markers, want)
	}
	for index := range want {
		if markers[index] != want[index] {
			t.Fatalf("ToolkitRootMarkerPaths() = %v, want %v", markers, want)
		}
	}
	path, err := ToolkitRootMarkerPath()
	if err != nil {
		t.Fatalf("ToolkitRootMarkerPath: %v", err)
	}
	if path != want[0] {
		t.Fatalf("ToolkitRootMarkerPath() = %q, want the first rung %q", path, want[0])
	}
	if isWindowsGOOS(runtime.GOOS) && len(markers) != 1 {
		t.Fatalf("Windows consults %d marker(s): %v", len(markers), markers)
	}
	if !isWindowsGOOS(runtime.GOOS) && len(markers) != 2 {
		t.Fatalf("POSIX consults %d marker(s), want the install.sh marker and the Node fallback: %v", len(markers), markers)
	}
}

// TestMarkerPathsForOSLadder pins the ladder itself, on any host: Windows has
// one rung, POSIX has two, and the install.sh marker stays first.
func TestMarkerPathsForOSLadder(t *testing.T) {
	cases := []struct {
		name string
		goos string
		home string
		want []string
	}{
		{
			name: "windows has one rung",
			goos: "windows",
			home: `C:\Users\tester`,
			want: []string{`C:\Users\tester\.gemini\antigravity-cli\agy-root.txt`},
		},
		{
			name: "linux prefers install.sh then the node marker",
			goos: "linux",
			home: "/home/tester",
			want: []string{
				"/home/tester/.local/share/agy-tools-root",
				"/home/tester/.gemini/antigravity-cli/agy-root.txt",
			},
		},
		{
			name: "darwin prefers install.sh then the node marker",
			goos: "darwin",
			home: "/Users/tester/",
			want: []string{
				"/Users/tester/.local/share/agy-tools-root",
				"/Users/tester/.gemini/antigravity-cli/agy-root.txt",
			},
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got := markerPathsFor(testCase.goos, testCase.home)
			if len(got) != len(testCase.want) {
				t.Fatalf("markerPathsFor(%q, %q) = %v, want %v", testCase.goos, testCase.home, got, testCase.want)
			}
			for index := range testCase.want {
				if got[index] != testCase.want[index] {
					t.Fatalf("markerPathsFor(%q, %q) = %v, want %v", testCase.goos, testCase.home, got, testCase.want)
				}
			}
			if first := markerPathFor(testCase.goos, testCase.home); first != testCase.want[0] {
				t.Fatalf("markerPathFor(%q, %q) = %q, want the first rung %q", testCase.goos, testCase.home, first, testCase.want[0])
			}
		})
	}
}

// posixLadder swaps the resolver's marker ladder for the POSIX two-rung one,
// built from real host paths, so the fallback rung is exercised on a Windows
// host too.
func posixLadder(t *testing.T, home string) (installShMarker, nodeMarker string) {
	t.Helper()
	installShMarker = filepath.Join(home, ".local", "share", "agy-tools-root")
	nodeMarker = filepath.Join(home, ".gemini", "antigravity-cli", "agy-root.txt")
	ladder := []string{installShMarker, nodeMarker}
	previous := resolveMarkerPaths
	resolveMarkerPaths = func() ([]string, error) {
		return append([]string(nil), ladder...), nil
	}
	t.Cleanup(func() { resolveMarkerPaths = previous })
	return installShMarker, nodeMarker
}

// TestToolkitRootPosixFallbackRung pins decision (a): when install.sh's marker
// is absent, the marker make-launchers.js writes on every OS is adopted.
func TestToolkitRootPosixFallbackRung(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)
	installShMarker, nodeMarker := posixLadder(t, home)
	root := fakeToolkitRoot(t)

	writeMarker(t, nodeMarker, root+"\r\n")

	info, err := ResolveToolkitRoot()
	if err != nil {
		t.Fatalf("ResolveToolkitRoot with only the Node fallback marker: %v", err)
	}
	if info.Path != filepath.Clean(root) {
		t.Fatalf("resolved path = %q, want %q", info.Path, filepath.Clean(root))
	}
	if info.Marker != nodeMarker {
		t.Fatalf("resolved marker = %q, want the fallback %q", info.Marker, nodeMarker)
	}
	if info.Source != ToolkitRootSourceMarker {
		t.Fatalf("resolved source = %q, want %q", info.Source, ToolkitRootSourceMarker)
	}
	if _, statErr := os.Stat(installShMarker); statErr == nil {
		t.Fatalf("the resolution created %q", installShMarker)
	}
}

// TestToolkitRootPosixLadderPriority pins the order inside the ladder: the
// install.sh marker wins when both are present, and a stale install.sh marker
// falls through to the Node one instead of stopping the lookup.
func TestToolkitRootPosixLadderPriority(t *testing.T) {
	t.Run("install.sh marker wins", func(t *testing.T) {
		home := sandboxHome(t)
		clearToolkitEnv(t)
		installShMarker, nodeMarker := posixLadder(t, home)
		installRoot := fakeToolkitRoot(t)
		nodeRoot := fakeToolkitRoot(t)
		writeMarker(t, installShMarker, installRoot+"\n")
		writeMarker(t, nodeMarker, nodeRoot+"\n")

		info, err := ResolveToolkitRoot()
		if err != nil {
			t.Fatalf("ResolveToolkitRoot: %v", err)
		}
		if info.Path != filepath.Clean(installRoot) || info.Marker != installShMarker {
			t.Fatalf("resolved %#v, want the install.sh root %q via %q", info, installRoot, installShMarker)
		}
	})

	t.Run("stale install.sh marker falls through", func(t *testing.T) {
		home := sandboxHome(t)
		clearToolkitEnv(t)
		installShMarker, nodeMarker := posixLadder(t, home)
		nodeRoot := fakeToolkitRoot(t)
		stale := t.TempDir() // exists, but holds no bin/agy-tokens.js
		writeMarker(t, installShMarker, stale+"\n")
		writeMarker(t, nodeMarker, nodeRoot+"\n")

		info, err := ResolveToolkitRoot()
		if err != nil {
			t.Fatalf("ResolveToolkitRoot: %v", err)
		}
		if info.Path != filepath.Clean(nodeRoot) || info.Marker != nodeMarker {
			t.Fatalf("resolved %#v, want the Node root %q via %q", info, filepath.Clean(nodeRoot), nodeMarker)
		}
	})

	t.Run("both markers absent names both", func(t *testing.T) {
		home := sandboxHome(t)
		clearToolkitEnv(t)
		installShMarker, nodeMarker := posixLadder(t, home)

		info, err := ResolveToolkitRoot()
		if err == nil {
			t.Fatalf("ResolveToolkitRoot resolved %#v with no marker on disk", info)
		}
		if !errors.Is(err, ErrToolkitRootNotFound) {
			t.Fatalf("error = %v, want ErrToolkitRootNotFound", err)
		}
		assertContains(t, "not-found error", err.Error(), installShMarker, nodeMarker, ToolkitReinstallHint)

		var toolkitErr *ToolkitRootError
		if !errors.As(err, &toolkitErr) {
			t.Fatalf("error %v is not a *ToolkitRootError", err)
		}
		if len(toolkitErr.Markers) != 2 || toolkitErr.Markers[0] != installShMarker || toolkitErr.Markers[1] != nodeMarker {
			t.Fatalf("error markers = %v, want [%q %q]", toolkitErr.Markers, installShMarker, nodeMarker)
		}
	})
}

// TestToolkitRootEnvOverride pins the first rung: AGY_PRO_TOOLS_ROOT wins, is
// whitespace-trimmed, and is reported as the source.
func TestToolkitRootEnvOverride(t *testing.T) {
	sandboxHome(t)
	root := fakeToolkitRoot(t)
	t.Setenv(EnvToolkitRoot, "  "+root+"  ")

	info, err := ResolveToolkitRoot()
	if err != nil {
		t.Fatalf("ResolveToolkitRoot: %v", err)
	}
	if info.Path != filepath.Clean(root) {
		t.Fatalf("resolved path = %q, want %q", info.Path, filepath.Clean(root))
	}
	if info.Source != ToolkitRootSourceEnv {
		t.Fatalf("resolved source = %q, want %q", info.Source, ToolkitRootSourceEnv)
	}
	if info.Marker != "" {
		t.Fatalf("env resolution reported a marker: %q", info.Marker)
	}

	path, err := ToolkitRoot()
	if err != nil {
		t.Fatalf("ToolkitRoot: %v", err)
	}
	if path != filepath.Clean(root) {
		t.Fatalf("ToolkitRoot() = %q, want %q", path, filepath.Clean(root))
	}
}

// TestToolkitRootEnvInvalidIsLoudEvenWithMarker pins the fail-closed rule for
// the override: an unusable AGY_PRO_TOOLS_ROOT stops the lookup even when a
// valid marker would have resolved, because silently ignoring an explicit
// override sends the engine at a tree the operator did not choose.
func TestToolkitRootEnvInvalidIsLoudEvenWithMarker(t *testing.T) {
	home := sandboxHome(t)
	markerRoot := fakeToolkitRoot(t)
	writeMarker(t, hostMarkerPath(home), markerRoot+"\r\n")

	missing := filepath.Join(t.TempDir(), "not-a-toolkit")
	t.Setenv(EnvToolkitRoot, missing)

	info, err := ResolveToolkitRoot()
	if err == nil {
		t.Fatalf("ResolveToolkitRoot accepted an unusable override and returned %#v", info)
	}
	if !errors.Is(err, ErrToolkitRootInvalid) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootInvalid", err)
	}
	if errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("an unusable override reported as not-found: %v", err)
	}
	assertContains(t, "error message", err.Error(),
		EnvToolkitRoot,
		strconv.Quote(missing),
		"does not exist",
		ToolkitReinstallHint,
	)

	var toolkitErr *ToolkitRootError
	if !errors.As(err, &toolkitErr) {
		t.Fatalf("error %v is not a *ToolkitRootError", err)
	}
	if toolkitErr.Source != ToolkitRootSourceEnv {
		t.Fatalf("error source = %q, want %q", toolkitErr.Source, ToolkitRootSourceEnv)
	}
}

// TestToolkitRootMarkerValid pins the second rung: the marker is read, its
// recorded directory is validated, and the source names both.
func TestToolkitRootMarkerValid(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)
	root := fakeToolkitRoot(t)
	markerPath := hostMarkerPath(home)
	writeMarker(t, markerPath, root+"\r\n")

	info, err := ResolveToolkitRoot()
	if err != nil {
		t.Fatalf("ResolveToolkitRoot: %v", err)
	}
	if info.Path != filepath.Clean(root) {
		t.Fatalf("resolved path = %q, want %q", info.Path, filepath.Clean(root))
	}
	if info.Source != ToolkitRootSourceMarker {
		t.Fatalf("resolved source = %q, want %q", info.Source, ToolkitRootSourceMarker)
	}
	if info.Marker != markerPath {
		t.Fatalf("resolved marker = %q, want %q", info.Marker, markerPath)
	}
}

// TestToolkitRootMarkerStale pins the stale-marker rule: a recorded directory
// that is not a toolkit root is refused, and the message names the marker.
func TestToolkitRootMarkerStale(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)
	stale := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stale, "bin"), 0o755); err != nil {
		t.Fatalf("prepare stale root: %v", err)
	}
	markerPath := hostMarkerPath(home)
	writeMarker(t, markerPath, stale+"\r\n")

	info, err := ResolveToolkitRoot()
	if err == nil {
		t.Fatalf("ResolveToolkitRoot adopted a stale marker and returned %#v", info)
	}
	if !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", err)
	}
	assertContains(t, "error message", err.Error(),
		markerPath,
		"does not contain "+ToolkitEntryScript,
		ToolkitReinstallHint,
	)
}

// TestToolkitRootMarkerMalformed pins every way a marker can fail to name a
// usable root: absent, empty, relative, UNC, a file, and a directory without
// the entry script.
func TestToolkitRootMarkerMalformed(t *testing.T) {
	fileTarget := filepath.Join(t.TempDir(), "regular-file")
	writeFileRaw(t, fileTarget, []byte("not a directory\n"))

	cases := []struct {
		name     string
		content  string
		wantWord string
	}{
		{name: "empty marker", content: "", wantWord: "marker file is empty"},
		{name: "whitespace marker", content: " \r\n\t\r\n", wantWord: "marker file is empty"},
		{name: "relative path", content: "relative/toolkit\n", wantWord: "is not absolute"},
		{name: "unc path", content: `\\server\share\toolkit` + "\n", wantWord: "UNC"},
		{name: "path is a file", content: fileTarget + "\n", wantWord: "is not a directory"},
		{name: "path does not exist", content: filepath.Join(t.TempDir(), "gone") + "\n", wantWord: "does not exist"},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			home := sandboxHome(t)
			clearToolkitEnv(t)
			markerPath := hostMarkerPath(home)
			writeMarker(t, markerPath, testCase.content)

			info, err := ResolveToolkitRoot()
			if err == nil {
				t.Fatalf("ResolveToolkitRoot accepted a malformed marker and returned %#v", info)
			}
			if !errors.Is(err, ErrToolkitRootNotFound) {
				t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", err)
			}
			assertContains(t, "error message", err.Error(),
				markerPath,
				testCase.wantWord,
				ToolkitReinstallHint,
			)

			var toolkitErr *ToolkitRootError
			if !errors.As(err, &toolkitErr) {
				t.Fatalf("error %v is not a *ToolkitRootError", err)
			}
			wantMarkers, markerErr := ToolkitRootMarkerPaths()
			if markerErr != nil {
				t.Fatalf("ToolkitRootMarkerPaths: %v", markerErr)
			}
			if len(toolkitErr.Markers) != len(wantMarkers) || toolkitErr.Markers[0] != markerPath {
				t.Fatalf("error markers = %v, want %v (the primary marker %q)", toolkitErr.Markers, wantMarkers, markerPath)
			}
		})
	}
}

// TestToolkitRootNothingFound pins the typed not-found error: no env, no
// marker, marker path named, reinstall hint attached, nothing written.
func TestToolkitRootNothingFound(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)
	markerPath := hostMarkerPath(home)

	info, err := ResolveToolkitRoot()
	if err == nil {
		t.Fatalf("ResolveToolkitRoot resolved a root with nothing on disk: %#v", info)
	}
	if !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", err)
	}
	assertContains(t, "error message", err.Error(),
		markerPath,
		"marker file is absent",
		"fix: "+ToolkitReinstallHint,
	)
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatalf("the failed resolution created the marker %q", markerPath)
	}
}

// TestToolkitRootMarkerFirstLineOnly pins the marker parse: wave 1 writes the
// root followed by CRLF, so the first line is the path and a trailing newline,
// extra lines, or a BOM must never become part of it.
func TestToolkitRootMarkerFirstLineOnly(t *testing.T) {
	cases := []struct {
		name    string
		content func(root string) string
	}{
		{name: "crlf with a second line", content: func(root string) string { return root + "\r\nC:\\stale\\other\r\n" }},
		{name: "lf only", content: func(root string) string { return root + "\n" }},
		{name: "no trailing newline", content: func(root string) string { return root }},
		{name: "bom and crlf", content: func(root string) string { return "\ufeff" + root + "\r\n" }},
		{name: "leading and trailing spaces", content: func(root string) string { return "  " + root + "  \r\n" }},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			home := sandboxHome(t)
			clearToolkitEnv(t)
			root := fakeToolkitRoot(t)
			writeMarker(t, hostMarkerPath(home), testCase.content(root))

			info, err := ResolveToolkitRoot()
			if err != nil {
				t.Fatalf("ResolveToolkitRoot: %v", err)
			}
			if info.Path != filepath.Clean(root) {
				t.Fatalf("resolved path = %q, want %q", info.Path, filepath.Clean(root))
			}
		})
	}
}

// TestToolkitRootHomeResolutionExplicit is the F-W1-17 pin
// (agy-pro/internal/dashboard/oneshot_test.go:700-702 is the same discipline):
// USERPROFILE and HOME point at two DIFFERENT temp directories, and the resolver
// must read the marker under the platform-correct one. The other OS's marker
// carries a different, equally valid root, so a resolver that consults the
// wrong variable resolves to the wrong tree instead of failing.
func TestToolkitRootHomeResolutionExplicit(t *testing.T) {
	profile, home := sandboxSplitHome(t)
	clearToolkitEnv(t)
	wantRoot := fakeToolkitRoot(t)
	decoyRoot := fakeToolkitRoot(t)

	var wantMarker, decoyMarker string
	if isWindowsGOOS(runtime.GOOS) {
		wantMarker = markerPathFor("windows", profile)
		decoyMarker = markerPathFor("linux", home)
	} else {
		wantMarker = markerPathFor("linux", home)
		decoyMarker = markerPathFor("windows", profile)
	}
	writeMarker(t, wantMarker, wantRoot+"\r\n")
	writeMarker(t, decoyMarker, decoyRoot+"\r\n")

	resolvedMarker, err := ToolkitRootMarkerPath()
	if err != nil {
		t.Fatalf("ToolkitRootMarkerPath: %v", err)
	}
	if resolvedMarker != wantMarker {
		t.Fatalf("marker path = %q, want the %s path %q (USERPROFILE=%q HOME=%q)",
			resolvedMarker, runtime.GOOS, wantMarker, profile, home)
	}

	info, err := ResolveToolkitRoot()
	if err != nil {
		t.Fatalf("ResolveToolkitRoot: %v", err)
	}
	if info.Path != filepath.Clean(wantRoot) {
		t.Fatalf("resolved path = %q, want %q (the other-variable root %q was adopted: F-W1-17)",
			info.Path, filepath.Clean(wantRoot), decoyRoot)
	}
	if info.Marker != wantMarker {
		t.Fatalf("resolved marker = %q, want %q", info.Marker, wantMarker)
	}

	// Negative control: with the platform-correct marker gone, the decoy must
	// NOT be adopted. A fallback here would mean the resolver really consults
	// the other variable.
	if err := os.Remove(wantMarker); err != nil {
		t.Fatalf("remove marker %q: %v", wantMarker, err)
	}
	if doomed, err := ResolveToolkitRoot(); err == nil {
		t.Fatalf("resolution succeeded with only the other-variable marker present: %#v", doomed)
	} else if !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", err)
	}
}

// TestToolkitRootMarkerUnreadable pins the read-error branch: a marker path
// that exists but cannot be read as a file is a not-found failure carrying the
// reason, not a silent skip.
func TestToolkitRootMarkerUnreadable(t *testing.T) {
	home := sandboxHome(t)
	clearToolkitEnv(t)
	markerPath := hostMarkerPath(home)
	if err := os.MkdirAll(markerPath, 0o755); err != nil {
		t.Fatalf("create marker directory: %v", err)
	}

	info, err := ResolveToolkitRoot()
	if err == nil {
		t.Fatalf("ResolveToolkitRoot read a directory as a marker: %#v", info)
	}
	if !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", err)
	}
	assertContains(t, "error message", err.Error(), markerPath, "marker file is unreadable", ToolkitReinstallHint)
}

// TestToolkitRootEntryScriptIsADirectory pins the last validation branch: a
// bin/agy-tokens.js that is a directory is not the entry script.
func TestToolkitRootEntryScriptIsADirectory(t *testing.T) {
	sandboxHome(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin", "agy-tokens.js"), 0o755); err != nil {
		t.Fatalf("create fake entry directory: %v", err)
	}
	t.Setenv(EnvToolkitRoot, root)

	info, err := ResolveToolkitRoot()
	if err == nil {
		t.Fatalf("ResolveToolkitRoot adopted a root whose entry script is a directory: %#v", info)
	}
	if !errors.Is(err, ErrToolkitRootInvalid) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootInvalid", err)
	}
	assertContains(t, "error message", err.Error(), "is a directory, not the agy-tokens entry script")
}

// TestToolkitRootErrorFormatting pins the message chain a CLI verb prints and
// the nil-receiver helpers.
func TestToolkitRootErrorFormatting(t *testing.T) {
	var nilError *ToolkitRootError
	if got := nilError.Error(); got == "" {
		t.Fatal("nil *ToolkitRootError rendered an empty message")
	}
	if err := nilError.Unwrap(); err != nil {
		t.Fatalf("nil *ToolkitRootError Unwrap() = %v, want nil", err)
	}

	thin := &ToolkitRootError{Source: ToolkitRootSourceMarker, Detail: "detail text"}
	assertContains(t, "thin error message", thin.Error(),
		"agy-tools toolkit root unresolved", "detail text", "fix: "+ToolkitReinstallHint)

	envErr := &ToolkitRootError{
		Source:    ToolkitRootSourceEnv,
		Env:       EnvToolkitRoot,
		Candidate: `C:\wrong`,
		Detail:    "path does not exist",
		Err:       ErrToolkitRootInvalid,
	}
	assertContains(t, "env error message", envErr.Error(),
		ErrToolkitRootInvalid.Error(),
		EnvToolkitRoot+`="C:\\wrong"`,
		"path does not exist",
		"fix: "+ToolkitReinstallHint,
	)
}

// TestToolkitRootFailsClosedWithoutHome pins that an unresolvable home is a
// reported failure, never a working-directory fallback for the marker.
func TestToolkitRootFailsClosedWithoutHome(t *testing.T) {
	clearToolkitEnv(t)
	for _, name := range []string{"USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}

	markers, err := ToolkitRootMarkerPaths()
	if err == nil {
		t.Skipf("this host still resolves a home directory (%v) with every home variable cleared", markers)
	}
	assertContains(t, "marker path error", err.Error(), "home")

	info, resolveErr := ResolveToolkitRoot()
	if resolveErr == nil {
		t.Fatalf("ResolveToolkitRoot resolved %#v with no resolvable home", info)
	}
	if !errors.Is(resolveErr, ErrToolkitRootNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrToolkitRootNotFound", resolveErr)
	}
	assertContains(t, "resolution error", resolveErr.Error(), "home", ToolkitReinstallHint)
}

// TestToolkitRootWrappersReportFailures pins that the two convenience wrappers
// propagate failures instead of returning a half-value: ToolkitRoot() with
// nothing on disk, and ToolkitRootMarkerPath() with no resolvable home.
func TestToolkitRootWrappersReportFailures(t *testing.T) {
	sandboxHome(t)
	clearToolkitEnv(t)

	path, err := ToolkitRoot()
	if err == nil {
		t.Fatalf("ToolkitRoot() resolved %q with nothing on disk", path)
	}
	if path != "" {
		t.Fatalf("ToolkitRoot() returned %q alongside its error", path)
	}
	if !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("ToolkitRoot() error = %v, want it to wrap ErrToolkitRootNotFound", err)
	}

	for _, name := range []string{"USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}
	markerPath, markerErr := ToolkitRootMarkerPath()
	if markerErr == nil {
		t.Skipf("this host still resolves a home directory (%q) with every home variable cleared", markerPath)
	}
	if markerPath != "" {
		t.Fatalf("ToolkitRootMarkerPath() returned %q alongside its error", markerPath)
	}
	assertContains(t, "marker path error", markerErr.Error(), "home")
}

// TestToolkitRootReadsNodeWrittenMarker is the cross-language contract the
// design rests on: the REAL wave-1 helper (scripts/lib/make-launchers.js, the
// `marker` subcommand) writes the root marker and the Go resolver reads it
// back. The test skips with a named reason when Node or the helper is missing,
// and it writes only inside temp directories (both --home and --marker are
// passed explicitly), so no real home is touched.
func TestToolkitRootReadsNodeWrittenMarker(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the marker/Go contract check needs the runtime the installer uses")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	helper := filepath.Join(repoRoot, "scripts", "lib", "make-launchers.js")
	if _, statErr := os.Stat(helper); statErr != nil {
		t.Skipf("the wave-1 marker helper is not present: %v", statErr)
	}

	home := sandboxHome(t)
	clearToolkitEnv(t)
	markerPath := hostMarkerPath(home)

	output, runErr := exec.Command(node, helper, "marker", "--root", repoRoot, "--home", home, "--marker", markerPath).CombinedOutput()
	if runErr != nil {
		t.Fatalf("node marker helper failed: %v\n%s", runErr, string(output))
	}
	written := string(readFileRaw(t, markerPath))
	if got := strings.TrimRight(written, "\r\n"); got != repoRoot {
		t.Fatalf("the helper recorded %q, want %q", got, repoRoot)
	}
	if !strings.Contains(written, "\r\n") {
		t.Fatalf("the helper no longer terminates the marker with CRLF: %q", written)
	}

	info, err := ResolveToolkitRoot()
	if err != nil {
		t.Fatalf("ResolveToolkitRoot on a node-written marker: %v", err)
	}
	if info.Source != ToolkitRootSourceMarker {
		t.Fatalf("resolved source = %q, want %q", info.Source, ToolkitRootSourceMarker)
	}
	if info.Path != filepath.Clean(repoRoot) {
		t.Fatalf("resolved path = %q, want %q", info.Path, filepath.Clean(repoRoot))
	}
	if info.Marker != markerPath {
		t.Fatalf("resolved marker = %q, want %q", info.Marker, markerPath)
	}
}
