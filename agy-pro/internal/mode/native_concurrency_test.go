package mode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
)

func TestNativeSeatConcurrencyAdditiveKeyRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	// A fresh state has no key: the effective value is the default (2).
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.NativeSeatConcurrency != 0 {
		t.Fatalf("fresh state should not carry native_seat_concurrency, got %d", state.NativeSeatConcurrency)
	}
	if got := state.EffectiveNativeSeatConcurrency(); got != DefaultNativeSeatConcurrency {
		t.Fatalf("EffectiveNativeSeatConcurrency() = %d; want %d", got, DefaultNativeSeatConcurrency)
	}
	if DefaultNativeSeatConcurrency != 2 {
		t.Fatalf("contract default is 2, got %d", DefaultNativeSeatConcurrency)
	}

	// The key survives a save/load cycle alongside the other additive keys.
	state.NativeSeatConcurrency = 5
	state.BridgeTools = "read,search"
	state.DefaultProfile = ProfileNative
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("reload LoadState failed: %v", err)
	}
	if reloaded.NativeSeatConcurrency != 5 {
		t.Fatalf("native_seat_concurrency did not round-trip, got %d", reloaded.NativeSeatConcurrency)
	}
	if got := reloaded.EffectiveNativeSeatConcurrency(); got != 5 {
		t.Fatalf("EffectiveNativeSeatConcurrency() = %d; want stored 5", got)
	}
	if reloaded.EffectiveBridgeTools() != "read,search" || reloaded.EffectiveDefaultProfile() != ProfileNative {
		t.Fatal("sibling additive keys must survive alongside native_seat_concurrency")
	}
}

func TestEffectiveNativeSeatConcurrencyClamps(t *testing.T) {
	cases := []struct {
		stored int
		want   int
	}{
		{0, 2},   // missing -> default
		{-3, 2},  // invalid -> default
		{1, 1},   // lower clamp bound
		{2, 2},   // default
		{8, 8},   // upper clamp bound
		{9, 8},   // clamped to 8
		{100, 8}, // clamped to 8
	}
	for _, c := range cases {
		s := &State{NativeSeatConcurrency: c.stored}
		if got := s.EffectiveNativeSeatConcurrency(); got != c.want {
			t.Errorf("stored %d -> %d; want %d", c.stored, got, c.want)
		}
	}
	var nilState *State
	if got := nilState.EffectiveNativeSeatConcurrency(); got != 2 {
		t.Errorf("nil state -> %d; want 2", got)
	}
}

// TestLegacyStateKeepsNativeConcurrencyOmitted verifies the additive-key
// contract for native_seat_concurrency: a legacy state.json loads and
// re-saves without the new key being injected (omitempty), and a state
// saved with the zero value does not emit it either.
func TestLegacyStateKeepsNativeConcurrencyOmitted(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, tempDir)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(tempDir, "cfg"))

	legacy := `{"current_mode":"full","updated_at":"2026-10-11T07:19:00Z","managed_items":["rules","skills","plugins","agents","references","profile.json","config.json"]}`
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
	if state.NativeSeatConcurrency != 0 {
		t.Fatalf("legacy state must not gain native_seat_concurrency on load, got %d", state.NativeSeatConcurrency)
	}
	if got := state.EffectiveNativeSeatConcurrency(); got != DefaultNativeSeatConcurrency {
		t.Fatalf("legacy effective = %d; want default %d", got, DefaultNativeSeatConcurrency)
	}

	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), `"native_seat_concurrency"`) {
		t.Fatalf("re-saved legacy state must not inject native_seat_concurrency:\n%s", saved)
	}
	for _, key := range []string{`"current_mode"`, `"updated_at"`, `"managed_items"`} {
		if !strings.Contains(string(saved), key) {
			t.Fatalf("re-saved legacy state lost %s:\n%s", key, saved)
		}
	}
}
