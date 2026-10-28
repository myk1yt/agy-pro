package agysettings

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalSettingsPathResolvesUnderSandboxHome(t *testing.T) {
	home := sandboxHome(t)

	got, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	want := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if got != want {
		t.Errorf("GlobalSettingsPath = %q; want %q", got, want)
	}
	if !isUnder(got, home) {
		t.Errorf("GlobalSettingsPath %q is not under the sandbox home %q", got, home)
	}
}

// GEMINI_CONFIG_DIR was claimed to relocate the config dir. The claim is
// deleted from the design (agy ignores it on v1.3.5); this package must not
// resurrect it.
func TestGlobalSettingsPathIgnoresGeminiConfigDir(t *testing.T) {
	home := sandboxHome(t)
	elsewhere := t.TempDir()
	t.Setenv("GEMINI_CONFIG_DIR", elsewhere)

	got, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if strings.Contains(strings.ToLower(got), "gemini_config_dir") {
		t.Errorf("path mentions GEMINI_CONFIG_DIR: %q", got)
	}
	if !isUnder(got, home) {
		t.Errorf("GEMINI_CONFIG_DIR leaked into resolution: %q is not under %q", got, home)
	}
	if isUnder(got, elsewhere) {
		t.Errorf("GEMINI_CONFIG_DIR redirected the settings path to %q", elsewhere)
	}
}

func TestGlobalSettingsPathIgnoresAgyProHome(t *testing.T) {
	home := sandboxHome(t)
	t.Setenv("AGY_PRO_HOME", filepath.Join(t.TempDir(), "agy-pro-home"))

	got, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	if !isUnder(got, home) {
		t.Errorf("AGY_PRO_HOME leaked into resolution: %q is not under %q", got, home)
	}
}

func TestAppDataDirSettingsPath(t *testing.T) {
	home := sandboxHome(t)

	cases := []struct {
		name       string
		appDataDir string
		want       string
	}{
		{"plain", "agy-hybrid-abc123", filepath.Join(home, ".gemini", "agy-hybrid-abc123", "settings.json")},
		{"nested", "nest/deep", filepath.Join(home, ".gemini", "nest", "deep", "settings.json")},
		{"trailing slash cleaned", "./agy-hybrid-x/", filepath.Join(home, ".gemini", "agy-hybrid-x", "settings.json")},
		{"surrounding space trimmed", "  agy-hybrid-y  ", filepath.Join(home, ".gemini", "agy-hybrid-y", "settings.json")},
		{"redundant separators cleaned", "agy-hybrid//z", filepath.Join(home, ".gemini", "agy-hybrid", "z", "settings.json")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := AppDataDirSettingsPath(c.appDataDir)
			if err != nil {
				t.Fatalf("AppDataDirSettingsPath(%q): %v", c.appDataDir, err)
			}
			if got != c.want {
				t.Errorf("AppDataDirSettingsPath(%q) = %q; want %q", c.appDataDir, got, c.want)
			}
			if !isUnder(got, filepath.Join(home, ".gemini")) {
				t.Errorf("path %q escaped <home>/.gemini", got)
			}
		})
	}
}

func TestAppDataDirSettingsPathRejectsAbsolute(t *testing.T) {
	sandboxHome(t)

	absolute := []string{
		`C:\Users\someone\appdata`,
		`C:/Users/someone/appdata`,
		`C:relative`,
		`/etc/agy`,
		`\\fileserver\share\appdata`,
	}
	for _, v := range absolute {
		t.Run(v, func(t *testing.T) {
			got, err := AppDataDirSettingsPath(v)
			if err == nil {
				t.Fatalf("AppDataDirSettingsPath(%q) = %q; want an error (agy rejects absolute values)", v, got)
			}
			if got != "" {
				t.Errorf("path %q returned alongside the error", got)
			}
			if !strings.Contains(err.Error(), "relative") {
				t.Errorf("error does not say the value must be relative: %v", err)
			}
		})
	}
}

func TestAppDataDirSettingsPathRejectsTraversal(t *testing.T) {
	sandboxHome(t)

	traversing := []string{
		"..",
		"../evil",
		"../../evil",
		"agy-hybrid/../../evil",
		"./../evil",
	}
	for _, v := range traversing {
		t.Run(v, func(t *testing.T) {
			got, err := AppDataDirSettingsPath(v)
			if err == nil {
				t.Fatalf("AppDataDirSettingsPath(%q) = %q; want an error (traversal)", v, got)
			}
			if got != "" {
				t.Errorf("path %q returned alongside the error", got)
			}
			if !strings.Contains(err.Error(), "escape") {
				t.Errorf("error does not name the escape: %v", err)
			}
		})
	}
}

func TestAppDataDirSettingsPathRejectsEmptyAndDegenerate(t *testing.T) {
	sandboxHome(t)

	for _, v := range []string{"", "   ", "./"} {
		t.Run(v, func(t *testing.T) {
			if _, err := AppDataDirSettingsPath(v); err == nil {
				t.Errorf("AppDataDirSettingsPath(%q) succeeded; want an error", v)
			}
		})
	}

	// A value that cleans back to the app-data root would address the global
	// app-data dir, i.e. the global settings file.
	for _, v := range []string{".", "./", "agy-hybrid/.."} {
		t.Run(v, func(t *testing.T) {
			_, err := AppDataDirSettingsPath(v)
			if err == nil {
				t.Fatalf("AppDataDirSettingsPath(%q) succeeded; want an error", v)
			}
			if !strings.Contains(err.Error(), "global") {
				t.Errorf("error does not explain the global-path hazard: %v", err)
			}
		})
	}
}

// The app-data-dir regime and the global regime are different files, so a P2
// session can never touch the global settings file by accident.
func TestAppDataDirSettingsPathDiffersFromGlobal(t *testing.T) {
	sandboxHome(t)

	global, err := GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	session, err := AppDataDirSettingsPath("agy-hybrid-abc123")
	if err != nil {
		t.Fatalf("AppDataDirSettingsPath: %v", err)
	}
	if global == session {
		t.Fatalf("app-data-dir path collides with the global path %q", global)
	}
	if isUnder(session, filepath.Dir(global)) {
		t.Errorf("session path %q lives inside the global app-data dir %q", session, filepath.Dir(global))
	}
}
