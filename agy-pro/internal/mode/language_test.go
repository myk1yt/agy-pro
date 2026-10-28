package mode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
)

// TestLanguageAdditiveKeyRoundTrip covers the language key (plan §4): it
// round-trips through state.json, defaults to en when absent, normalizes
// case-folded and padded codes to the canonical 12-code set, and fails
// closed to en for unrecognized values.
func TestLanguageAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no key: the effective value is the default (en).
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.Language != "" {
		t.Fatalf("fresh state should not carry language, got %q", state.Language)
	}
	if got := state.EffectiveLanguage(); got != DefaultLanguage {
		t.Fatalf("EffectiveLanguage() = %q; want %q", got, DefaultLanguage)
	}

	// The key survives a save/load cycle alongside the other additive keys.
	state.Language = "ko"
	state.DefaultProfile = ProfileNative
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	if reloaded.Language != "ko" {
		t.Fatalf("language did not round-trip, got %q", reloaded.Language)
	}
	if got := reloaded.EffectiveLanguage(); got != "ko" {
		t.Fatalf("EffectiveLanguage() = %q; want ko", got)
	}
	if reloaded.EffectiveDefaultProfile() != ProfileNative {
		t.Fatalf("default_profile did not survive alongside language, got %q", reloaded.DefaultProfile)
	}

	// Case folding and whitespace: every canonical code normalizes to itself.
	for _, code := range []string{"en", "zh-Hans", "zh-Hant", "ko", "ja", "ru", "hi", "de", "fr", "it", "vi", "ar"} {
		if !ValidLanguage(code) {
			t.Errorf("ValidLanguage(%q) = false; want true", code)
		}
		upper := strings.ToUpper(code)
		if got := NormalizeLanguage(upper); got != code {
			t.Errorf("NormalizeLanguage(%q) = %q; want %q", upper, got, code)
		}
		if got := (&State{Language: "  " + code + " "}).EffectiveLanguage(); got != code {
			t.Errorf("EffectiveLanguage(padded %q) = %q; want %q", code, got, code)
		}
	}

	// Unknown or blank values fail closed to en.
	for _, bad := range []string{"", "  ", "klingon", "zh", "zh-CN", "pt", "ENGLISH"} {
		if ValidLanguage(bad) {
			t.Errorf("ValidLanguage(%q) = true; want false", bad)
		}
		if got := (&State{Language: bad}).EffectiveLanguage(); got != DefaultLanguage {
			t.Errorf("EffectiveLanguage(%q) = %q; want %q", bad, got, DefaultLanguage)
		}
	}

	// A nil state reads as the default (the pre-existing Effective* contract).
	var nilState *State
	if got := nilState.EffectiveLanguage(); got != DefaultLanguage {
		t.Fatalf("nil.EffectiveLanguage() = %q; want %q", got, DefaultLanguage)
	}
}

// TestLauncherMenuAdditiveKeyRoundTrip covers the launcher_menu key (plan §5):
// string "1"/"0" round-trips (an explicit off survives), an absent key reads
// as "" (no stored decision, distinct from an explicit off), and the launcher
// layer (not this normalizer) owns the AGY_LAUNCHER env precedence.
func TestLauncherMenuAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no key: no stored decision.
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.LauncherMenu != "" {
		t.Fatalf("fresh state should not carry launcher_menu, got %q", state.LauncherMenu)
	}
	if got := state.EffectiveLauncherMenu(); got != "" {
		t.Fatalf("EffectiveLauncherMenu() = %q; want empty (no stored decision)", got)
	}

	// Both stored values round-trip, including the explicit off.
	for _, stored := range []string{LauncherMenuOn, LauncherMenuOff} {
		state.LauncherMenu = stored
		if err := SaveState(state); err != nil {
			t.Fatalf("SaveState(%q) failed: %v", stored, err)
		}
		reloaded, err := LoadState()
		if err != nil {
			t.Fatalf("reload LoadState failed: %v", err)
		}
		if reloaded.LauncherMenu != stored {
			t.Fatalf("launcher_menu %q did not round-trip, got %q", stored, reloaded.LauncherMenu)
		}
		if got := reloaded.EffectiveLauncherMenu(); got != stored {
			t.Fatalf("EffectiveLauncherMenu() = %q; want stored %q", got, stored)
		}
	}

	// Stored aliases normalize; an invalid value reads as absent, not as off.
	for stored, want := range map[string]string{
		"on": "1", "true": "1", "ON": "1",
		"off": "0", "false": "0", "Off": "0",
		" 1 ": "1", " 0 ": "0",
		"garbage": "", "2": "", "enabled": "",
	} {
		if got := (&State{LauncherMenu: stored}).EffectiveLauncherMenu(); got != want {
			t.Errorf("EffectiveLauncherMenu(%q) = %q; want %q", stored, got, want)
		}
	}

	// A nil state reads as absent (the pre-existing Effective* contract).
	var nilState *State
	if got := nilState.EffectiveLauncherMenu(); got != "" {
		t.Fatalf("nil.EffectiveLauncherMenu() = %q; want empty", got)
	}
}

// TestLegacyStateKeepsLanguageAndLauncherMenuOmitted verifies the
// additive-key contract for language and launcher_menu: a legacy state.json
// written before either key existed loads and re-saves without them being
// injected (omitempty), while the original keys stay intact.
func TestLegacyStateKeepsLanguageAndLauncherMenuOmitted(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	legacy := `{"current_mode":"full","updated_at":"2026-10-11T07:19:00Z","last_backup":"C:\\agy\\backups\\20261011_071900","managed_items":["rules","skills","plugins","agents","references","profile.json","config.json"]}`
	stateFile, err := paths.AgyProStateFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState of legacy file failed: %v", err)
	}
	if state.Language != "" {
		t.Fatalf("legacy state must not gain language on load, got %q", state.Language)
	}
	if state.LauncherMenu != "" {
		t.Fatalf("legacy state must not gain launcher_menu on load, got %q", state.LauncherMenu)
	}
	if got := state.EffectiveLanguage(); got != DefaultLanguage {
		t.Fatalf("legacy effective language = %q; want default %q", got, DefaultLanguage)
	}

	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"language"`, `"launcher_menu"`} {
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
