package integrations

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"agypro/internal/paths"
)

// sandboxConfigDir points GEMINI_CONFIG_DIR at a fresh temp directory and
// returns it. The engine reads its destination root through
// paths.GeminiConfigDir (the same override internal/mode honors), so every test
// in this file writes inside the sandbox and never touches a real ~/.gemini.
func sandboxConfigDir(t *testing.T) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv(paths.EnvGeminiConfigDir, configDir)
	return configDir
}

// fixtureContent is the content source the deploy tests read: one rules file,
// two skills files in a nested directory, one plugin file. It is a plain
// fstest.MapFS, which is the point of the content-source-agnostic engine: no
// test here needs a directory on disk to stand in for the real trees.
// Since P5-3 T7 the caller's source is canonical for every feature - the hook
// included - so the fixture carries hooks/hooks.json too: a hook plan against
// this source reads the fixture, never the machine's toolkit root.
func fixtureContent() fstest.MapFS {
	return fstest.MapFS{
		"rules/AGENTS.md":             {Data: []byte("# agents rule\n")},
		"skills/alpha/SKILL.md":       {Data: []byte("# alpha skill\n")},
		"skills/alpha/notes/more.md":  {Data: []byte("more notes\n")},
		"plugins/beta/plugin.json":    {Data: []byte("{\"name\":\"beta\"}\n")},
		"hooks/" + hookSourceFile:     {Data: []byte(hookFixtureJSON)},
		"unrelated/top-level-file.md": {Data: []byte("not content\n")},
	}
}

// fixtureFiles returns the fixture entries a feature deploys, as
// destination-relative slash paths in sorted order.
func fixtureFiles(feature Feature) []string {
	switch feature {
	case FeatureRules:
		return []string{"AGENTS.md"}
	case FeatureSkills:
		return []string{"alpha/SKILL.md", "alpha/notes/more.md"}
	case FeaturePlugins:
		return []string{"beta/plugin.json"}
	default:
		return nil
	}
}

// hookFixtureJSON is the hooks.json body the hook-feature tests deploy. It is
// deliberately a plain hook definition with no credential-shaped token: the
// provider-pattern gate scans these fixtures.
const hookFixtureJSON = "{\n  \"hooks\": {\n    \"PostInvocation\": [\n      {\"command\": \"agy-tokens --hook\"}\n    ]\n  }\n}\n"

func TestLayoutForFeature(t *testing.T) {
	configDir := sandboxConfigDir(t)

	cases := []struct {
		feature    Feature
		wantDir    string
		wantSingle bool
	}{
		{feature: FeatureRules, wantDir: "rules"},
		{feature: FeatureSkills, wantDir: "skills"},
		{feature: FeaturePlugins, wantDir: "plugins"},
		{feature: FeatureHook, wantDir: "hooks", wantSingle: true},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(string(testCase.feature), func(t *testing.T) {
			layout, err := LayoutFor(testCase.feature)
			if err != nil {
				t.Fatalf("LayoutFor(%q): %v", testCase.feature, err)
			}
			wantDestDir := filepath.Join(configDir, testCase.wantDir)
			if layout.DestDir != wantDestDir {
				t.Fatalf("DestDir = %q, want %q (GEMINI_CONFIG_DIR must be honored)", layout.DestDir, wantDestDir)
			}
			if layout.SourceDir != testCase.wantDir {
				t.Fatalf("SourceDir = %q, want %q", layout.SourceDir, testCase.wantDir)
			}
			if layout.SingleFile() != testCase.wantSingle {
				t.Fatalf("SingleFile() = %v, want %v", layout.SingleFile(), testCase.wantSingle)
			}
			if testCase.wantSingle && layout.SourceFile != hookSourceFile {
				t.Fatalf("SourceFile = %q, want %q", layout.SourceFile, hookSourceFile)
			}
			if !testCase.wantSingle && layout.SourceFile != "" {
				t.Fatalf("SourceFile = %q, want it empty for a tree feature", layout.SourceFile)
			}
			wantSourcePath := testCase.wantDir
			if testCase.wantSingle {
				wantSourcePath = testCase.wantDir + "/" + hookSourceFile
			}
			if got := layout.SourcePath(); got != wantSourcePath {
				t.Fatalf("SourcePath() = %q, want %q", got, wantSourcePath)
			}
		})
	}
}

// TestLayoutFailsClosedWithoutHome pins that an unresolvable home is a reported
// failure, never a working-directory fallback for the destination root.
func TestLayoutFailsClosedWithoutHome(t *testing.T) {
	t.Setenv(paths.EnvGeminiConfigDir, "")
	for _, name := range []string{"USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}
	layout, err := LayoutFor(FeatureRules)
	if err == nil {
		t.Skipf("this host still resolves a home directory (layout %+v) with every home variable cleared", layout)
	}
	assertContains(t, "layout error", err.Error(), "home")
}

// TestLayoutForRefusesFeaturesItDoesNotOwn pins the destination partition: the
// statusLine door and the mode-owned items are not this engine's layouts.
func TestLayoutForRefusesFeaturesItDoesNotOwn(t *testing.T) {
	sandboxConfigDir(t)

	if _, err := LayoutFor(FeatureStatusline); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("LayoutFor(statusline) error = %v, want ErrNoContentLayout", err)
	}
	if _, err := LayoutFor(Feature("agents")); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("LayoutFor(agents) error = %v, want ErrUnknownFeature", err)
	}
	if _, err := LayoutFor(Feature("")); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("LayoutFor(empty) error = %v, want ErrUnknownFeature", err)
	}
}

// TestLayoutDestinationStaysInsideTheTree pins the containment check: a source
// tree is data, so it must not be able to name a destination outside the
// directory this engine owns.
func TestLayoutDestinationStaysInsideTheTree(t *testing.T) {
	configDir := sandboxConfigDir(t)
	layout, err := LayoutFor(FeatureRules)
	if err != nil {
		t.Fatalf("LayoutFor(rules): %v", err)
	}

	destination, err := layout.Destination("AGENTS.md")
	if err != nil {
		t.Fatalf("Destination(AGENTS.md): %v", err)
	}
	if want := filepath.Join(configDir, "rules", "AGENTS.md"); destination != want {
		t.Fatalf("Destination(AGENTS.md) = %q, want %q", destination, want)
	}

	for _, rel := range []string{"", ".", "..", "../escape.md", "nested/../../escape.md", "/absolute.md"} {
		if _, err := layout.Destination(rel); err == nil {
			t.Fatalf("Destination(%q) was accepted; want a refusal", rel)
		}
	}
}

// TestLayoutHomeResolutionExplicit is the F-W1-17 pin for this engine's
// destination root: USERPROFILE and HOME point at two different temp
// directories, and ~/.gemini/config must resolve under the platform-correct
// one. GEMINI_CONFIG_DIR is cleared so the default is under test.
func TestLayoutHomeResolutionExplicit(t *testing.T) {
	profile, home := sandboxSplitHome(t)
	t.Setenv(paths.EnvGeminiConfigDir, "")

	layout, err := LayoutFor(FeatureRules)
	if err != nil {
		t.Fatalf("LayoutFor(rules): %v", err)
	}
	wantHome := home
	if isWindowsGOOS(runtime.GOOS) {
		wantHome = profile
	}
	want := filepath.Join(wantHome, ".gemini", "config", "rules")
	if layout.DestDir != want {
		t.Fatalf("DestDir = %q, want %q (USERPROFILE=%q HOME=%q)", layout.DestDir, want, profile, home)
	}
}

func TestEnumerateHashesAndOrders(t *testing.T) {
	sandboxConfigDir(t)
	source := fixtureContent()

	for _, feature := range []Feature{FeatureRules, FeatureSkills, FeaturePlugins} {
		feature := feature
		t.Run(string(feature), func(t *testing.T) {
			files, err := Enumerate(feature, source)
			if err != nil {
				t.Fatalf("Enumerate(%q): %v", feature, err)
			}
			want := fixtureFiles(feature)
			if len(files) != len(want) {
				t.Fatalf("enumerated %d file(s), want %d: %+v", len(files), len(want), files)
			}
			got := make([]string, 0, len(files))
			for _, file := range files {
				got = append(got, file.RelPath)
				if file.SHA256 != SHA256Hex(file.Data) {
					t.Fatalf("%q digest = %q, want the digest of its own bytes", file.RelPath, file.SHA256)
				}
				if len(file.SHA256) != 64 {
					t.Fatalf("%q digest = %q, want 64 hex characters", file.RelPath, file.SHA256)
				}
				if file.SourcePath != filepath.ToSlash(layoutSourcePath(feature, file.RelPath)) {
					t.Fatalf("%q source path = %q", file.RelPath, file.SourcePath)
				}
				if filepath.Base(file.DestPath) != filepath.Base(file.RelPath) {
					t.Fatalf("%q destination = %q", file.RelPath, file.DestPath)
				}
			}
			if !sort.StringsAreSorted(got) {
				t.Fatalf("enumeration order is not sorted: %v", got)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("enumeration = %v, want %v", got, want)
				}
			}
		})
	}
}

// layoutSourcePath renders the source path a feature's relative file has, in
// slash form, without asking the layout twice.
func layoutSourcePath(feature Feature, rel string) string {
	return sourceDirOf(feature) + "/" + rel
}

// sourceDirOf maps a feature onto its source directory name.
func sourceDirOf(feature Feature) string {
	switch feature {
	case FeatureRules:
		return sourceDirRules
	case FeatureSkills:
		return sourceDirSkills
	case FeaturePlugins:
		return sourceDirPlugins
	default:
		return sourceDirHooks
	}
}

// TestEnumerateHookIsOneFile pins the single-file feature: hooks/hooks.json in,
// <config>/hooks/hooks.json out, and nothing else from the source tree.
//
// The digest is the digest of the DEPLOYED bytes, not of the source bytes: the
// hook file's command value is rewritten at read time to this machine's absolute
// launcher (hookrewrite.go). The home is sandboxed, so this machine has no
// launcher and the deployed bytes are the portable source bytes; the rewrite's
// own tests cover the launcher-present half.
func TestEnumerateHookIsOneFile(t *testing.T) {
	sandboxHome(t)
	configDir := sandboxConfigDir(t)
	source := fstest.MapFS{
		"hooks/hooks.json":       {Data: []byte(hookFixtureJSON)},
		"hooks/other-file.json":  {Data: []byte("{}\n")},
		"hooks/nested/deep.json": {Data: []byte("{}\n")},
	}

	files, err := Enumerate(FeatureHook, source)
	if err != nil {
		t.Fatalf("Enumerate(hook): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("enumerated %d file(s), want exactly the hooks.json: %+v", len(files), files)
	}
	if files[0].RelPath != hookSourceFile {
		t.Fatalf("RelPath = %q, want %q", files[0].RelPath, hookSourceFile)
	}
	wantDest := filepath.Join(configDir, "hooks", hookSourceFile)
	if files[0].DestPath != wantDest {
		t.Fatalf("DestPath = %q, want %q", files[0].DestPath, wantDest)
	}
	if files[0].SHA256 != SHA256Hex(files[0].Data) {
		t.Fatalf("digest = %q, want the digest of the deployed bytes", files[0].SHA256)
	}
	if string(files[0].Data) != hookFixtureJSON {
		t.Fatalf("deployed bytes = %q, want the portable source bytes (no launcher exists in this sandbox)", string(files[0].Data))
	}
	if files[0].HookCommand == nil || files[0].HookCommand.Posture != HookCommandBare {
		t.Fatalf("hook posture = %+v, want the bare fallback", files[0].HookCommand)
	}
}

// TestEnumerateSkipsDeployerNoise pins the skip list the Node deployer used
// (scripts/lib/configure-customizations.js syncDirectory): .git, *.bak, and
// anything carrying a .bak. infix is not content.
func TestEnumerateSkipsDeployerNoise(t *testing.T) {
	sandboxConfigDir(t)
	source := fstest.MapFS{
		"rules/AGENTS.md":              {Data: []byte("# rule\n")},
		"rules/AGENTS.md.bak":          {Data: []byte("# old\n")},
		"rules/AGENTS.md.bak.20260101": {Data: []byte("# older\n")},
		"rules/.git/config":            {Data: []byte("[core]\n")},
		"rules/nested/kept.md":         {Data: []byte("kept\n")},
		"rules/nested/skipped.md.bak":  {Data: []byte("skipped\n")},
		"rules/nested/.git/HEAD":       {Data: []byte("ref\n")},
		"rules/a-symlink.md":           {Data: []byte("target.md"), Mode: fs.ModeSymlink},
	}

	files, err := Enumerate(FeatureRules, source)
	if err != nil {
		t.Fatalf("Enumerate(rules): %v", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.RelPath)
	}
	want := []string{"AGENTS.md", "nested/kept.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("enumerated %v, want %v", got, want)
	}
}

// TestEnumerateRefusesUnusableSources pins the fail-closed read: a missing tree
// is a broken installation, never "deploy nothing".
func TestEnumerateRefusesUnusableSources(t *testing.T) {
	sandboxConfigDir(t)

	if _, err := Enumerate(FeatureRules, nil); !errors.Is(err, ErrNoContentSource) {
		t.Fatalf("Enumerate(nil source) error = %v, want ErrNoContentSource", err)
	}

	empty := fstest.MapFS{"other/file.md": {Data: []byte("x\n")}}
	if _, err := Enumerate(FeatureSkills, empty); !errors.Is(err, ErrSourceUnusable) {
		t.Fatalf("Enumerate(missing skills dir) error = %v, want ErrSourceUnusable", err)
	}

	noHookFile := fstest.MapFS{"hooks/other.json": {Data: []byte("{}\n")}}
	if _, err := Enumerate(FeatureHook, noHookFile); !errors.Is(err, ErrSourceUnusable) {
		t.Fatalf("Enumerate(missing hooks.json) error = %v, want ErrSourceUnusable", err)
	}

	if _, err := Enumerate(FeatureStatusline, fixtureContent()); !errors.Is(err, ErrNoContentLayout) {
		t.Fatalf("Enumerate(statusline) error = %v, want ErrNoContentLayout", err)
	}
}

// TestOpenFeatureSource pins which source a feature reads (P5-3 T7): the
// caller's fs.FS - the embedded snapshot in production - for EVERY feature
// including the hook, with ToolkitRoot as the hook-only fallback for a caller
// that passes no source.
func TestOpenFeatureSource(t *testing.T) {
	// The home is sandboxed because the toolkit-root ladder's marker rung reads
	// it: on a machine with an installed toolkit the "no root at all" assertion
	// below would otherwise resolve the DEVELOPER's toolkit and fail for the
	// host's state rather than for this engine's behavior.
	sandboxHome(t)
	sandboxConfigDir(t)
	clearToolkitEnv(t)

	source := fixtureContent()
	resolved, err := OpenFeatureSource(FeatureRules, source)
	if err != nil {
		t.Fatalf("OpenFeatureSource(rules): %v", err)
	}
	if data, readErr := fs.ReadFile(resolved, "rules/AGENTS.md"); readErr != nil || string(data) != "# agents rule\n" {
		t.Fatalf("OpenFeatureSource(rules) did not return the caller's source: %q, %v", string(data), readErr)
	}
	if _, err := OpenFeatureSource(FeatureRules, nil); !errors.Is(err, ErrNoContentSource) {
		t.Fatalf("OpenFeatureSource(rules, nil) error = %v, want ErrNoContentSource", err)
	}

	// The caller's content source is canonical for EVERY feature, the hook
	// included: a toolkit root holding DIFFERENT bytes must not win while a source
	// is in hand - that is what makes the embedded snapshot the deploy source.
	const toolkitOnlyJSON = "{\"hooks\":{\"PostInvocation\":[{\"command\":\"toolkit-root-copy\"}]}}\n"
	root := fakeToolkitRoot(t)
	writeFileRaw(t, filepath.Join(root, "hooks", hookSourceFile), []byte(toolkitOnlyJSON))
	t.Setenv(EnvToolkitRoot, root)
	resolved, err = OpenFeatureSource(FeatureHook, source)
	if err != nil {
		t.Fatalf("OpenFeatureSource(hook) with a content source: %v", err)
	}
	hookBytes, readErr := fs.ReadFile(resolved, filepath.ToSlash(filepath.Join("hooks", hookSourceFile)))
	if readErr != nil {
		t.Fatalf("read hooks.json through the resolved hook source: %v", readErr)
	}
	if string(hookBytes) != hookFixtureJSON {
		t.Fatalf("the hook resolution returned %q; want the caller's source bytes (the toolkit root must not win while a source is in hand)", string(hookBytes))
	}

	// The toolkit root is the FALLBACK, and only for a caller with no source.
	resolved, err = OpenFeatureSource(FeatureHook, nil)
	if err != nil {
		t.Fatalf("OpenFeatureSource(hook, nil) with a toolkit root: %v", err)
	}
	data, err := fs.ReadFile(resolved, filepath.ToSlash(filepath.Join("hooks", hookSourceFile)))
	if err != nil {
		t.Fatalf("read hooks.json from the resolved fallback source: %v", err)
	}
	if string(data) != toolkitOnlyJSON {
		t.Fatalf("resolved hook fallback source returned %q; want the toolkit root's bytes", string(data))
	}

	// ... and with neither, the hook fails loudly instead of reading nothing.
	clearToolkitEnv(t)
	if _, err := OpenFeatureSource(FeatureHook, nil); !errors.Is(err, ErrToolkitRootNotFound) {
		t.Fatalf("OpenFeatureSource(hook, nil) error = %v, want ErrToolkitRootNotFound", err)
	}
}

func TestSHA256Hex(t *testing.T) {
	if got := SHA256Hex(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("SHA256Hex(nil) = %q", got)
	}
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256Hex(abc) = %q", got)
	}
	if a, b := SHA256Hex([]byte("x")), SHA256Hex([]byte("y")); a == b {
		t.Fatal("SHA256Hex returned the same digest for different bytes")
	}
}

// TestLiveDestinationStates pins the live-read classification the plan and the
// status both rest on.
func TestLiveDestinationStates(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.md")
	if _, present, err := liveDestination(missing); err != nil || present {
		t.Fatalf("liveDestination(missing) = present %v, err %v; want absent and no error", present, err)
	}

	file := filepath.Join(dir, "file.md")
	writeFileRaw(t, file, []byte("body\n"))
	sha, present, err := liveDestination(file)
	if err != nil || !present {
		t.Fatalf("liveDestination(file) = present %v, err %v", present, err)
	}
	if sha != SHA256Hex([]byte("body\n")) {
		t.Fatalf("liveDestination(file) digest = %q", sha)
	}

	asDir := filepath.Join(dir, "a-directory")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, _, err := liveDestination(asDir); err == nil {
		t.Fatal("liveDestination accepted a directory; want a refusal")
	} else if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory refusal = %q", err.Error())
	}
}
