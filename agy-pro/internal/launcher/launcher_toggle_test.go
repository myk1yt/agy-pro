package launcher

import (
	"os"
	"path/filepath"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
)

// P10b: the persisted launcher-menu toggle (state.json launcher_menu) merged
// into Decide's env lookup at the launcher.go call site. Precedence is
// env-wins: AGY_LAUNCHER non-empty passes through unchanged; only when it is
// unset or empty does the stored value apply. Asserted through Decide, the
// same observable behavior the decide tests pin, not by re-implementing the
// precedence.

// sandboxLauncherToggleState points AGY_PRO_HOME at a fresh temp dir so
// loadLauncherMenu reads (and tests write) only the sandboxed state file; the
// real ~/.agy-pro is never touched.
func sandboxLauncherToggleState(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	return home
}

func writeLauncherToggleState(t *testing.T, launcherMenu string) {
	t.Helper()
	if err := mode.SaveState(&mode.State{LauncherMenu: launcherMenu}); err != nil {
		t.Fatalf("SaveState(launcher_menu=%q): %v", launcherMenu, err)
	}
}

func corruptLauncherToggleState(t *testing.T, home string) {
	t.Helper()
	p := filepath.Join(home, "state.json")
	if err := os.WriteFile(p, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt state file: %v", err)
	}
}

// unsetLauncherEnv removes AGY_LAUNCHER (a truly unset variable, not an empty
// one) and restores it on cleanup. t.Setenv cannot express unset.
func unsetLauncherEnv(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv("AGY_LAUNCHER")
	if err := os.Unsetenv("AGY_LAUNCHER"); err != nil {
		t.Fatalf("Unsetenv(AGY_LAUNCHER): %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("AGY_LAUNCHER", old)
		}
	})
}

func TestLoadLauncherMenu(t *testing.T) {
	cases := []struct {
		name    string
		stored  string // "" = no stored key
		corrupt bool
		want    string
	}{
		{"stored on", "1", false, "1"},
		{"stored off", "0", false, "0"},
		{"no stored key", "", false, ""},
		{"corrupt state reads as no decision", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := sandboxLauncherToggleState(t)
			switch {
			case tc.corrupt:
				corruptLauncherToggleState(t, home)
			case tc.stored != "":
				writeLauncherToggleState(t, tc.stored)
			}
			if got := loadLauncherMenu(); got != tc.want {
				t.Errorf("loadLauncherMenu() = %q; want %q", got, tc.want)
			}
		})
	}
}

// TestDecideLauncherTogglePrecedence pins the merged precedence through
// Decide itself: bare `agy` on a TTY with the prompt profile and 5 registered
// models, exactly the "menu opt in" shape the decide tests use. The lookup is
// built exactly as the ExecutePassthrough call site builds it.
func TestDecideLauncherTogglePrecedence(t *testing.T) {
	cases := []struct {
		name     string
		envValue string // "" = env unset
		stored   string // "1"/"0"; "" = no stored key
		corrupt  bool
		want     Route
	}{
		{"env 1 beats stored 0", "1", "0", false, RouteMenu},
		{"env 0 beats stored 1", "0", "1", false, RouteNative},
		{"unset env shows menu with stored 1", "", "1", false, RouteMenu},
		{"unset env hides menu with stored 0", "", "0", false, RouteNative},
		{"unset env hides menu with no stored key", "", "", false, RouteNative},
		{"unreadable state hides menu", "", "1", true, RouteNative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := sandboxLauncherToggleState(t)
			switch {
			case tc.corrupt:
				corruptLauncherToggleState(t, home)
			case tc.stored != "":
				writeLauncherToggleState(t, tc.stored)
			}
			if tc.envValue == "" {
				unsetLauncherEnv(t)
			} else {
				t.Setenv("AGY_LAUNCHER", tc.envValue)
			}

			lookup := launcherEnvLookup(loadLauncherMenu())
			if got := Decide(nil, lookup, true, 5, mode.ProfilePrompt, ""); got != tc.want {
				t.Errorf("Decide(bare tty, models=5, prompt profile) = %s; want %s", got, tc.want)
			}
		})
	}
}

// TestLauncherEnvLookupPassesOtherKeysThrough: the wrapper must not divert any
// key other than AGY_LAUNCHER, so every other Decide input (bridge flags,
// hybrid overrides) keeps reading the live process env.
func TestLauncherEnvLookupPassesOtherKeysThrough(t *testing.T) {
	unsetLauncherEnv(t)
	lookup := launcherEnvLookup("1")
	for _, key := range []string{"AGY_PRO_BRIDGE", "AGY_BRIDGE", "AGY_PRO_HYBRID", "PATH"} {
		if got := lookup(key); got != os.Getenv(key) {
			t.Errorf("lookup(%q) = %q; want the live env value %q", key, got, os.Getenv(key))
		}
	}
}

// TestLauncherEnvLookupStoredOnKeepsEarlierRules: the stored "1" flows through
// Decide's existing gates, not around them. A one-shot prompt flag and a
// non-TTY stdin still bypass the menu even with the toggle on.
func TestLauncherEnvLookupStoredOnKeepsEarlierRules(t *testing.T) {
	sandboxLauncherToggleState(t)
	writeLauncherToggleState(t, "1")
	unsetLauncherEnv(t)
	lookup := launcherEnvLookup(loadLauncherMenu())
	if got := Decide([]string{"-p", "hi"}, lookup, true, 5, mode.ProfilePrompt, ""); got != RouteNative {
		t.Errorf("stored 1 + prompt flag = %s; want native (one-shots never block)", got)
	}
	if got := Decide(nil, lookup, false, 5, mode.ProfilePrompt, ""); got != RouteNative {
		t.Errorf("stored 1 + non-TTY = %s; want native (pipes stay zero-delay)", got)
	}
}
