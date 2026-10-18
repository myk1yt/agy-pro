package main

// Blind-qa gate D end-to-end pins through runSettingsCmd with the REAL Core
// seams (settings.NewCore defaults) pointed at a temp AGY_PRO_HOME: a registry
// that fails to load (ESC byte in a provider name, rejected by the round-6
// control-rune guard) makes `settings show` and `settings providers list`
// exit non-zero with the clean, masked loader error and no panic; a clean
// registry with a CJK provider name still renders normally. The live
// providers.json is never touched.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/settings"
)

// installRealSettingsCore swaps newSettingsCore for a production Core (default
// seams) whose AGY_PRO_HOME and providers registry both live in a temp dir.
// It returns the temp providers.json path for seeding.
func installRealSettingsCore(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("AGY_PRO_HOME", tmp)
	provPath := filepath.Join(tmp, "providers.json")
	prev := newSettingsCore
	newSettingsCore = func() *settings.Core {
		return settings.NewCore(settings.Deps{VersionFunc: func() string { return "0.0-test" }})
	}
	t.Cleanup(func() { newSettingsCore = prev })
	return provPath
}

func TestSettingsView_PoisonedRegistry_ExitsNonZeroNoPanic(t *testing.T) {
	provPath := installRealSettingsCore(t)
	// json.Marshal renders the ESC rune as the JSON unicode escape, which
	// decodes back to the ESC rune at load and trips the control-rune guard.
	name, err := json.Marshal("bad\x1bname")
	if err != nil {
		t.Fatalf("marshal poisoned name: %v", err)
	}
	poisoned := []byte(`{"providers":[{"name":` + string(name) + `,"baseURL":"https://poison.example/v1","apiKeyEnv":"POISON_KEY"}]}`)
	if err := os.WriteFile(provPath, poisoned, 0o600); err != nil {
		t.Fatalf("seed poisoned registry: %v", err)
	}
	for _, args := range [][]string{{"show"}, {"show", "--json"}, {"providers", "list"}} {
		code, out, errw := runSettings(t, args, "")
		if code == 0 {
			t.Errorf("%v: exit 0 on a registry that fails to load; want non-zero", args)
		}
		combined := out + errw
		if !strings.Contains(combined, "control characters") {
			t.Errorf("%v: output missing the clean loader error:\n%s", args, combined)
		}
		if strings.Contains(combined, "panic") || strings.Contains(combined, "goroutine") {
			t.Errorf("%v: panic leaked into output:\n%s", args, combined)
		}
		if strings.Contains(combined, "\x1b") {
			t.Errorf("%v: ESC byte echoed in output", args)
		}
	}
}

func TestSettingsView_CJKRegistry_RendersNormally(t *testing.T) {
	provPath := installRealSettingsCore(t)
	clean := `{"providers":[{"name":"모델공급자","baseURL":"https://cjk.example/v1","apiKeyEnv":"CJK_KEY"}]}`
	if err := os.WriteFile(provPath, []byte(clean), 0o600); err != nil {
		t.Fatalf("seed clean CJK registry: %v", err)
	}
	code, out, errw := runSettings(t, []string{"show"}, "")
	if code != 0 {
		t.Fatalf("show exit = %d, stderr=%s", code, errw)
	}
	if !strings.Contains(out, "모델공급자") {
		t.Errorf("show output missing the CJK provider row:\n%s", out)
	}
	code, out, errw = runSettings(t, []string{"providers", "list"}, "")
	if code != 0 {
		t.Fatalf("providers list exit = %d, stderr=%s", code, errw)
	}
	if !strings.Contains(out, "모델공급자") {
		t.Errorf("providers list output missing the CJK provider row:\n%s", out)
	}
}
