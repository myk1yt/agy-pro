package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
)

// legacyStateJSON is the four-key state file written by builds before
// default_profile existed. It must keep loading and re-saving intact.
const legacyStateJSON = `{
  "current_mode": "full",
  "updated_at": "2026-10-01T12:00:00Z",
  "last_backup": "/tmp/backup-1",
  "managed_items": ["rules", "skills", "plugins", "config.json"]
}`

func TestDefaultProfileRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)

	stateFile := filepath.Join(home, "state.json")
	if err := os.WriteFile(stateFile, []byte(legacyStateJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Legacy file: effective profile falls back to prompt.
	if got := loadDefaultProfile(); got != mode.ProfilePrompt {
		t.Fatalf("loadDefaultProfile() on legacy state = %q; want %q", got, mode.ProfilePrompt)
	}

	// 2. Persist a new default profile through the same path runConfig uses.
	st, err := mode.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	st.DefaultProfile = mode.NormalizeProfile("gateway")
	if err := mode.SaveState(st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// 3. The four legacy keys survive; default_profile is additive.
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("state file is not valid JSON: %v\n%s", err, data)
	}
	if raw["default_profile"] != "gateway" {
		t.Errorf("default_profile = %v; want gateway", raw["default_profile"])
	}
	if raw["current_mode"] != "full" {
		t.Errorf("current_mode = %v; want full (legacy key must survive)", raw["current_mode"])
	}
	if raw["last_backup"] != "/tmp/backup-1" {
		t.Errorf("last_backup = %v; want /tmp/backup-1 (legacy key must survive)", raw["last_backup"])
	}
	if _, ok := raw["managed_items"]; !ok {
		t.Error("managed_items key lost after save")
	}
	if _, ok := raw["updated_at"]; !ok {
		t.Error("updated_at key lost after save")
	}

	// 4. Reading back yields the stored profile.
	if got := loadDefaultProfile(); got != mode.ProfileGateway {
		t.Errorf("loadDefaultProfile() after save = %q; want %q", got, mode.ProfileGateway)
	}
}

func TestDefaultProfileAllValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)

	for _, profile := range []string{mode.ProfilePrompt, mode.ProfileNative, mode.ProfileGateway} {
		st, err := mode.LoadState()
		if err != nil {
			t.Fatalf("LoadState: %v", err)
		}
		st.DefaultProfile = mode.NormalizeProfile(profile)
		if err := mode.SaveState(st); err != nil {
			t.Fatalf("SaveState(%s): %v", profile, err)
		}
		if got := loadDefaultProfile(); got != profile {
			t.Errorf("round trip %q -> %q", profile, got)
		}
	}
}

func TestLoadDefaultProfileMissingState(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, filepath.Join(t.TempDir(), "fresh-home"))
	if got := loadDefaultProfile(); got != mode.ProfilePrompt {
		t.Errorf("loadDefaultProfile() with no state file = %q; want %q", got, mode.ProfilePrompt)
	}
}

func TestLoadDefaultProfileUnknownValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	stateFile := filepath.Join(home, "state.json")
	if err := os.WriteFile(stateFile, []byte(`{"current_mode":"clean","default_profile":"wat"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadDefaultProfile(); got != mode.ProfilePrompt {
		t.Errorf("loadDefaultProfile() with unknown value = %q; want %q", got, mode.ProfilePrompt)
	}
}

func TestModeProfileHelpers(t *testing.T) {
	if !mode.ValidProfile("prompt") || !mode.ValidProfile("native") || !mode.ValidProfile("gateway") {
		t.Error("ValidProfile rejected a supported profile")
	}
	if mode.ValidProfile("bogus") || mode.ValidProfile("") {
		t.Error("ValidProfile accepted an unsupported profile")
	}
	if mode.NormalizeProfile("  GATEWAY ") != mode.ProfileGateway {
		t.Error("NormalizeProfile should canonicalize case and spaces")
	}
	if mode.NormalizeProfile("bogus") != mode.ProfilePrompt {
		t.Error("NormalizeProfile should fall back to prompt")
	}
}
