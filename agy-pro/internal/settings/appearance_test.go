package settings

import (
	"os"
	"testing"

	"agypro/internal/mode"
)

// TestCoreSetLanguage covers the Core.Set "language" case (plan §4): the
// closed 12-code set validates, accepted values normalize to their canonical
// code, and invalid values are rejected with the operand masked.
func TestCoreSetLanguage(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"en", "en"},
		{"ko", "ko"},
		{"KO", "ko"},
		{" zh-hans ", "zh-Hans"},
		{"ZH-HANT", "zh-Hant"},
		{"vi", "vi"},
		{"AR", "ar"},
	}
	for _, tc := range cases {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := c.Set("language", tc.in); err != nil {
			t.Errorf("Set(language, %q): %v", tc.in, err)
			continue
		}
		if f.state.Language != tc.want {
			t.Errorf("Set(language, %q) stored %q; want %q", tc.in, f.state.Language, tc.want)
		}
	}

	// Invalid codes reject and must not touch the stored value.
	for _, bad := range []string{"klingon", "zh", "pt", "", "english"} {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := c.Set("language", bad); err == nil {
			t.Errorf("Set(language, %q) accepted; want rejection", bad)
			continue
		}
		if f.state.Language != "" {
			t.Errorf("rejected language %q still mutated state to %q", bad, f.state.Language)
		}
		if f.saved != 0 {
			t.Errorf("rejected language %q still triggered a save", bad)
		}
	}
}

// TestCoreSetLauncherMenu covers the Core.Set "launcher_menu" case (plan §5):
// every accepted spelling stores the canonical "1"/"0", and anything else is
// rejected with the operand masked.
func TestCoreSetLauncherMenu(t *testing.T) {
	on := []string{"on", "1", "true", "ON", "True", " 1 "}
	for _, v := range on {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := c.Set("launcher_menu", v); err != nil {
			t.Errorf("Set(launcher_menu, %q): %v", v, err)
			continue
		}
		if f.state.LauncherMenu != mode.LauncherMenuOn {
			t.Errorf("Set(launcher_menu, %q) stored %q; want %q", v, f.state.LauncherMenu, mode.LauncherMenuOn)
		}
	}
	off := []string{"off", "0", "false", "OFF", "False", " 0 "}
	for _, v := range off {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := c.Set("launcher_menu", v); err != nil {
			t.Errorf("Set(launcher_menu, %q): %v", v, err)
			continue
		}
		if f.state.LauncherMenu != mode.LauncherMenuOff {
			t.Errorf("Set(launcher_menu, %q) stored %q; want %q", v, f.state.LauncherMenu, mode.LauncherMenuOff)
		}
	}

	// The dashed spelling is accepted too (the key itself is dashed in JSON).
	f := newFakeDeps()
	c := NewCore(f.deps())
	if err := c.Set("launcher-menu", "off"); err != nil {
		t.Errorf("Set(launcher-menu, off): %v", err)
	} else if f.state.LauncherMenu != mode.LauncherMenuOff {
		t.Errorf("Set(launcher-menu, off) stored %q; want %q", f.state.LauncherMenu, mode.LauncherMenuOff)
	}

	// Invalid spellings reject without mutating or saving.
	for _, bad := range []string{"maybe", "2", "enabled", ""} {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := c.Set("launcher_menu", bad); err == nil {
			t.Errorf("Set(launcher_menu, %q) accepted; want rejection", bad)
			continue
		}
		if f.state.LauncherMenu != "" {
			t.Errorf("rejected launcher_menu %q still mutated state to %q", bad, f.state.LauncherMenu)
		}
		if f.saved != 0 {
			t.Errorf("rejected launcher_menu %q still triggered a save", bad)
		}
	}
}

// TestViewAppearanceRows verifies the new Appearance section (plan §4/§5):
// both item IDs are present, the language row resolves its value and source,
// and the pre-existing read-only env.AGY_LAUNCHER row is untouched.
func TestViewAppearanceRows(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	sec, ok := view.SectionByID("p8")
	if !ok {
		t.Fatal("appearance section (p8) missing from view")
	}
	if sec.Title == "" {
		t.Fatal("appearance section has no title")
	}
	lang, ok := view.FindItem("appearance.language")
	if !ok {
		t.Fatal("appearance.language item missing")
	}
	if lang.Label == "" || lang.Value == "" {
		t.Errorf("language row must carry label and value: %+v", lang)
	}
	if got := len(lang.Choices); got != 12 {
		t.Errorf("language row choices = %d; want the 12 native names", got)
	}
	menu, ok := view.FindItem("appearance.launcher_menu")
	if !ok {
		t.Fatal("appearance.launcher_menu item missing")
	}
	if menu.Label != "Launcher menu" {
		t.Errorf("launcher row label = %q; want %q", menu.Label, "Launcher menu")
	}
	if got := len(menu.Choices); got != 2 || menu.Choices[0] != "show" || menu.Choices[1] != "hide" {
		t.Errorf("launcher row choices = %v; want [show hide]", menu.Choices)
	}

	// The pre-existing read-only env docs row is untouched.
	envRow, ok := view.FindItem("env.AGY_LAUNCHER")
	if !ok {
		t.Fatal("pre-existing env.AGY_LAUNCHER row missing")
	}
	if !envRow.ReadOnly || envRow.Source != "env" {
		t.Errorf("env.AGY_LAUNCHER row changed: %+v", envRow)
	}
}

// TestViewAppearanceLanguageSource covers the language row source column for
// the stored and default states.
func TestViewAppearanceLanguageSource(t *testing.T) {
	// No stored key: source shows the default, value is English.
	f := newFakeDeps()
	view, err := NewCore(f.deps()).View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	lang, _ := view.FindItem("appearance.language")
	if lang.Source != "default: English" {
		t.Errorf("no stored language: source = %q; want %q", lang.Source, "default: English")
	}
	if lang.Value != "English" {
		t.Errorf("no stored language: value = %q; want English", lang.Value)
	}

	// Stored key: source shows state.json, value is the native name.
	f = newFakeDeps()
	f.state.Language = "ko"
	view, err = NewCore(f.deps()).View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	lang, _ = view.FindItem("appearance.language")
	if lang.Source != "state.json" {
		t.Errorf("stored language: source = %q; want state.json", lang.Source)
	}
	if lang.Value != "한국어" {
		t.Errorf("stored language: value = %q; want 한국어", lang.Value)
	}
}

// TestViewAppearanceLauncherSource covers the launcher-menu row source column
// for the three launcher states (plan §5): env override set, stored toggle
// deciding, and the no-stored-value default.
func TestViewAppearanceLauncherSource(t *testing.T) {
	cases := []struct {
		name   string
		env    string // "" means unset; t.Setenv cannot unset, so use a sentinel
		setEnv bool
		stored string
		source string
		value  string
	}{
		{name: "env override on", setEnv: true, env: "1", source: "env AGY_LAUNCHER (override)", value: "show"},
		{name: "env override off", setEnv: true, env: "0", source: "env AGY_LAUNCHER (override)", value: "hide"},
		{name: "env override non-1 hides", setEnv: true, env: "banana", source: "env AGY_LAUNCHER (override)", value: "hide"},
		{name: "stored on", setEnv: false, stored: "1", source: "state.json", value: "show"},
		{name: "stored off", setEnv: false, stored: "0", source: "state.json", value: "hide"},
		{name: "neither", setEnv: false, source: "default (off)", value: "hide"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// AGY_LAUNCHER must start unset; t.Setenv restores the original
			// value after the subtest, so empty the parent env first is not
			// possible here - instead skip when it is already set.
			if tc.setEnv {
				t.Setenv("AGY_LAUNCHER", tc.env)
			} else {
				// Ensure the override path is not taken: a blank value still
				// counts as set for LookupEnv, so these subtests require the
				// var to be absent. Guard explicitly.
				if v, ok := os.LookupEnv("AGY_LAUNCHER"); ok && v != "" {
					t.Skipf("AGY_LAUNCHER=%q is set in the environment; cannot test the unset path", v)
				}
			}
			f := newFakeDeps()
			f.state.LauncherMenu = tc.stored
			view, err := NewCore(f.deps()).View()
			if err != nil {
				t.Fatalf("View: %v", err)
			}
			menu, _ := view.FindItem("appearance.launcher_menu")
			if menu.Source != tc.source {
				t.Errorf("source = %q; want %q", menu.Source, tc.source)
			}
			if menu.Value != tc.value {
				t.Errorf("value = %q; want %q", menu.Value, tc.value)
			}
		})
	}
}

// TestViewAppearanceLauncherEnvNote verifies the honest override note: while
// AGY_LAUNCHER is set, the row tells the reader the stored toggle has no
// effect until the env is cleared.
func TestViewAppearanceLauncherEnvNote(t *testing.T) {
	t.Setenv("AGY_LAUNCHER", "1")
	f := newFakeDeps()
	f.state.LauncherMenu = "0" // stored off: env must still win
	view, err := NewCore(f.deps()).View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	menu, _ := view.FindItem("appearance.launcher_menu")
	if menu.Value != "show" {
		t.Errorf("env=1 must win over stored off: value = %q; want show", menu.Value)
	}
	if menu.Source != "env AGY_LAUNCHER (override)" {
		t.Errorf("source = %q; want env override", menu.Source)
	}
	if menu.Note == "" {
		t.Error("env override row must carry the honest no-effect-until-cleared note")
	}
}
