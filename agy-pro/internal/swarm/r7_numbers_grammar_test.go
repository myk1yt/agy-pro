package swarm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/inventory"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// r7 F-01, swarm package: the saved-preset numbers field is §2.7 selection text.
// The grammar is enforced at the store load (records dropped with one warning) and
// at the settings save prompt (internal/tui); the display surfaces take the
// seat-token authority as defense in depth, because a legacy record can still reach
// them.

// The validator IS the selection expander's parser, so the shapes it accepts are
// exactly the shapes a selection line accepts.
func TestValidateSavedPresetNumbersTable(t *testing.T) {
	for _, ok := range []string{"1", "1,3", "1,3,19,23", "2-4", "p1", "1, 3 ,5", "10,2-3"} {
		if err := ValidateSavedPresetNumbers(ok); err != nil {
			t.Errorf("ValidateSavedPresetNumbers(%q) = %v; want the grammar to accept it", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"   ",
		testfixtures.SKLive,
		"1," + testfixtures.SKLive,
		"native:gemini-a",
		"nw/glm-5.3-flash",
		"1-",
		"4-2",         // inverted range
		"0",           // seat numbers are 1-based
		"1-999999999", // over-wide range
		"1,3,x",       // junk token
		"1,3\nnw/abc", // newline-split token
	} {
		if err := ValidateSavedPresetNumbers(bad); err == nil {
			t.Errorf("ValidateSavedPresetNumbers(%q) accepted a value the grammar refuses", bad)
		}
	}
	// The sentinel is distinguishable, because the load door keeps empty records
	// while dropping invalid ones.
	if !errorsIs(ValidateSavedPresetNumbers(""), ErrSavedPresetNumbersEmpty) {
		t.Error("an empty value must return ErrSavedPresetNumbersEmpty")
	}
}

// errorsIs avoids importing errors only for one call.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// The load door drops an invalid record with one warning and keeps the others.
func TestLoadPresetsDropsInvalidNumbersRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "swarm-state.json")
	body := `{"presets":[
		{"name":"good","numbers":"1,3"},
		{"name":"planted","numbers":"` + testfixtures.SKLive + `"},
		{"name":"legacy-empty","numbers":""},
		{"name":"alsoGood","numbers":"2-4"}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	st := &FileStore{Path: path, WarnOut: &warn}
	presets, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	var names []string
	for _, p := range presets {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "good,legacy-empty,alsoGood" {
		t.Errorf("loaded presets = %q; want the planted record dropped and the empty (legacy) one kept", got)
	}
	if n := strings.Count(warn.String(), "has numbers that are not seat numbers"); n != 1 {
		t.Errorf("warnings = %d; want exactly one for the dropped record: %q", n, warn.String())
	}
	if strings.Contains(warn.String(), testfixtures.SKLive) {
		t.Errorf("the warning echoes the stored value: %q", warn.String())
	}
	if !strings.Contains(warn.String(), `"planted"`) {
		t.Errorf("the warning must name the dropped preset: %q", warn.String())
	}
}

// Display arm: a LEGACY record forced into the in-memory list (the struct literal
// is that path - the store is what enforces the grammar) renders masked in the
// builder's saved-preset listing, and a benign pair is byte-identical.
func TestBuilderSavedPresetListingMasksUnderscoreHeadNumbers(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})
	const plant = testfixtures.SKLive
	saved := []SavedPreset{
		{Name: "legacy", Numbers: plant},
		{Name: "split", Numbers: "1," + plant},
		{Name: "benign", Numbers: "1,3"},
	}
	var out, errOut bytes.Buffer
	b := &Builder{In: strings.NewReader("1\nq\n"), Out: &out, ErrW: &errOut, Inv: inv, Store: fakeStore(saved)}
	_ = b.Run()
	got := out.String()
	if strings.Contains(got, plant) {
		t.Errorf("saved-preset listing echoes the numbers plant raw:\n%s", got)
	}
	if !strings.Contains(got, "legacy = sk_li****") {
		t.Errorf("numbers must render through the seat-token authority:\n%s", got)
	}
	if !strings.Contains(got, "split = 1,sk_li****") {
		t.Errorf("per-token masking drifted:\n%s", got)
	}
	if !strings.Contains(got, "benign = 1,3") {
		t.Errorf("benign numbers drifted:\n%s", got)
	}
}
