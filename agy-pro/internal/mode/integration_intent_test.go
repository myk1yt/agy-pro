package mode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
)

// P5-3 T6: the integrations INTENT half (five per-feature toggles plus the
// composed preset) is persisted in state.json as six additive keys. These tests
// pin the additive-key contract (old files keep loading, absent keys are never
// injected on re-save), the encoding ("on"/"off" and native/tools/custom), and
// the read-side tolerance: an absent key is the legacy posture and stays
// distinct from an explicit off, while an unrecognized value is inert instead
// of being reinterpreted.

// TestIntegrationIntentAdditiveKeyRoundTrip covers the six integration keys: a
// fresh state carries none of them, every stored value round-trips through
// state.json, and an explicit "off" is not the same posture as an absent key.
func TestIntegrationIntentAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no integration key: no integrations-owned intent.
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	for name, got := range integrationRows(state) {
		if got != "" {
			t.Fatalf("fresh state should not carry %s, got %q", name, got)
		}
	}
	if got := NormalizeIntegrationPreset(state.IntegrationPreset); got != "" {
		t.Fatalf("NormalizeIntegrationPreset(absent) = %q; want empty", got)
	}

	// Every stored value round-trips alongside the other additive keys.
	state.IntegrationPreset = IntegrationPresetTools
	state.IntegrationStatusline = IntegrationToggleOn
	state.IntegrationHook = IntegrationToggleOff
	state.IntegrationRules = IntegrationToggleOn
	state.IntegrationPlugins = IntegrationToggleOff
	state.IntegrationSkills = IntegrationToggleOn
	state.Language = "ko"
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	for name, got := range integrationRows(reloaded) {
		if want := integrationRows(state)[name]; got != want {
			t.Fatalf("%s did not round-trip: got %q; want %q", name, got, want)
		}
	}
	if reloaded.Language != "ko" {
		t.Fatalf("the neighbouring language key did not survive, got %q", reloaded.Language)
	}
	if got := NormalizeIntegrationPreset(reloaded.IntegrationPreset); got != IntegrationPresetTools {
		t.Fatalf("NormalizeIntegrationPreset(tools) = %q; want tools", got)
	}
	if got := NormalizeIntegrationToggle(reloaded.IntegrationHook); got != IntegrationToggleOff {
		t.Fatalf("NormalizeIntegrationToggle(off) = %q; want off (an explicit off is not legacy)", got)
	}

	// The persisted bytes carry the documented spellings, not the identifiers.
	saved, err := os.ReadFile(mustStateFile(t))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{
		`"integration_preset": "tools"`,
		`"integration_statusline": "on"`,
		`"integration_hook": "off"`,
		`"integration_rules": "on"`,
		`"integration_plugins": "off"`,
		`"integration_skills": "on"`,
	} {
		if !strings.Contains(string(saved), want) {
			t.Fatalf("state.json is missing %s:\n%s", want, saved)
		}
	}
}

// TestLegacyStateKeepsIntegrationIntentOmitted verifies the additive-key
// contract: a state file written before the six keys existed loads with the
// legacy posture and re-saves without any of them being injected.
func TestLegacyStateKeepsIntegrationIntentOmitted(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	legacy := `{"current_mode":"full","updated_at":"2026-10-11T07:19:00Z","last_backup":"C:\\agy\\backups\\20261011_071900","managed_items":["rules","skills","plugins","agents","references","profile.json","config.json"]}`
	stateFile := mustStateFile(t)
	if err := os.WriteFile(stateFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState of legacy file failed: %v", err)
	}
	for name, got := range integrationRows(state) {
		if got != "" {
			t.Fatalf("legacy state must not gain %s on load, got %q", name, got)
		}
	}

	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"integration_preset"`, `"integration_statusline"`, `"integration_hook"`,
		`"integration_rules"`, `"integration_plugins"`, `"integration_skills"`,
	} {
		if strings.Contains(string(saved), key) {
			t.Fatalf("re-saved legacy state must not inject %s:\n%s", key, saved)
		}
	}
	for _, key := range []string{`"current_mode"`, `"updated_at"`, `"last_backup"`, `"managed_items"`} {
		if !strings.Contains(string(saved), key) {
			t.Fatalf("re-saved legacy state lost %s:\n%s", key, saved)
		}
	}
}

// TestNormalizeIntegrationToggleAndPreset pins the read side: the canonical
// words normalize to themselves, the launcher-menu alias set reads on a
// hand-edited file, and anything else is inert ("") rather than reinterpreted.
// The preset and the toggle domains stay disjoint, so a preset word typed into a
// feature slot can never read as a toggle (the SET door refuses it too).
func TestNormalizeIntegrationToggleAndPreset(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{IntegrationToggleOn, IntegrationToggleOn},
		{IntegrationToggleOff, IntegrationToggleOff},
		{"ON", IntegrationToggleOn},
		{"Off", IntegrationToggleOff},
		{" 1 ", IntegrationToggleOn},
		{"0", IntegrationToggleOff},
		{"true", IntegrationToggleOn},
		{"false", IntegrationToggleOff},
		{"", ""},
		{"  ", ""},
		{"yes", ""},
		{"2", ""},
		{"enabled", ""},
		{"native", ""},
		{"tools", ""},
		{"custom", ""},
	} {
		if got := NormalizeIntegrationToggle(tc.stored); got != tc.want {
			t.Errorf("NormalizeIntegrationToggle(%q) = %q; want %q", tc.stored, got, tc.want)
		}
	}

	for _, tc := range []struct{ stored, want string }{
		{IntegrationPresetNative, IntegrationPresetNative},
		{IntegrationPresetTools, IntegrationPresetTools},
		{IntegrationPresetCustom, IntegrationPresetCustom},
		{"TOOLS", IntegrationPresetTools},
		{" Custom ", IntegrationPresetCustom},
		{"", ""},
		{"bogus", ""},
		{"on", ""},
		{"off", ""},
		{"all", ""},
	} {
		if got := NormalizeIntegrationPreset(tc.stored); got != tc.want {
			t.Errorf("NormalizeIntegrationPreset(%q) = %q; want %q", tc.stored, got, tc.want)
		}
	}
}

// integrationRows returns the six integration rows under their state.json key
// names, so a test can assert over the whole family without repeating itself.
func integrationRows(st *State) map[string]string {
	return map[string]string{
		"integration_preset":     st.IntegrationPreset,
		"integration_statusline": st.IntegrationStatusline,
		"integration_hook":       st.IntegrationHook,
		"integration_rules":      st.IntegrationRules,
		"integration_plugins":    st.IntegrationPlugins,
		"integration_skills":     st.IntegrationSkills,
	}
}

// mustStateFile returns the state.json path under the current sandbox.
func mustStateFile(t *testing.T) string {
	t.Helper()
	file, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatalf("AgyProStateFile: %v", err)
	}
	return file
}
