package fanout

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// writeRawPresetsFile hand-writes fanout-presets.json, bypassing SavePresets
// so the test can plant shapes the write door (correctly) refuses to persist.
func writeRawPresetsFile(t *testing.T, home, raw string) string {
	t.Helper()
	path := filepath.Join(home, "fanout-presets.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// F-12: a hand-authored store with two reserved-name records must not split
// authority. The load door keeps the FIRST record (FindPreset's natural
// resolution), drops the duplicate with ONE masked warning, and a preset-less
// run consumes exactly that survivor - the warning plus the lineup echo are
// both on the run's stderr, so the collapse is disclosed, never silent.
func TestLoadDropsDuplicateBuiltinSeedRecords(t *testing.T) {
	home, stderr := ladderEnv(t)
	raw := `{"presets":[
		{"name":"(built-in defaults)","seats":"native:first","seats_sha256":"` + SeatsHash("native:first") + `"},
		{"name":"(built-in defaults)","seats":"native:second"},
		{"name":"ordinary","n":2}
	]}`
	path := writeRawPresetsFile(t, home, raw)

	var warn bytes.Buffer
	st := &PresetFileStore{Path: path, WarnOut: &warn}
	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Presets) != 2 || loaded.Presets[0].Name != BuiltinPresetName ||
		loaded.Presets[0].Seats != "native:first" || loaded.Presets[1].Name != "ordinary" {
		t.Fatalf("loaded presets = %+v; want the FIRST seed record plus the ordinary record", loaded.Presets)
	}
	wantWarn := "warning: fanout-presets.json holds more than one record named \"(built-in defaults)\"; keeping the first and ignoring 1 later duplicate record(s)\n"
	if warn.String() != wantWarn {
		t.Errorf("dedupe warning drifted\n got: %q\nwant: %q", warn.String(), wantWarn)
	}

	seed, ok := loaded.FindPreset(BuiltinPresetName)
	if !ok || seed.Seats != "native:first" {
		t.Fatalf("FindPreset(%q) = %+v, %v; want the first (surviving) record", BuiltinPresetName, seed, ok)
	}

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := seatsOf(cfg.Seats); got != "native:first" {
		t.Errorf("a preset-less run consumed seats %q; want the surviving first record's lineup", got)
	}
	if cfg.SeatLineup.Preset != BuiltinPresetName || !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing built-in seed origin", cfg.SeatLineup)
	}
	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("run stderr lines = %d; want the disclosure plus the lineup echo: %q", len(lines), stderr.String())
	}
	if lines[0] != strings.TrimSuffix(wantWarn, "\n") {
		t.Errorf("run disclosure drifted\n got: %q\nwant: %q", lines[0], wantWarn)
	}
	wantEcho := "fanout: seat lineup from preset \"(built-in defaults)\" (default_preset rung, no key set): 1 seat(s), 0 metered provider seats (native:first)"
	if lines[1] != wantEcho {
		t.Errorf("lineup echo drifted\n got: %q\nwant: %q", lines[1], wantEcho)
	}
}

// F-12: the resolver must agree with the ladder on the same survivor record;
// both read through the store door, so they cannot drift.
func TestResolvePresetSeatLineupAgreesWithLadderOnDuplicateSeed(t *testing.T) {
	home, _ := ladderEnv(t)
	raw := `{"presets":[
		{"name":"(built-in defaults)","seats":"native:first","seats_sha256":"` + SeatsHash("native:first") + `"},
		{"name":"(built-in defaults)","seats":"native:second"}
	]}`
	writeRawPresetsFile(t, home, raw)

	lineup, err := ResolvePresetSeatLineup()
	if err != nil {
		t.Fatal(err)
	}
	if lineup.PresetName != BuiltinPresetName || !lineup.Standing() || lineup.Raw != "native:first" {
		t.Errorf("ResolvePresetSeatLineup = %+v; want the same surviving first record the ladder runs", lineup)
	}
	if len(lineup.Specs) != 1 {
		t.Errorf("specs = %+v; want one parsed seat", lineup.Specs)
	}
}

// F-12 write door: SavePresets refuses to persist a file with two
// reserved-name records, before any byte is written and before any marker is
// stamped.
func TestSavePresetsRefusesDuplicateBuiltinSeedRecords(t *testing.T) {
	home, _ := ladderEnv(t)
	path := filepath.Join(home, "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	dup := PresetsFile{Presets: []Preset{
		{Name: BuiltinPresetName, Seats: "native:first"},
		{Name: "ordinary", N: 2},
		{Name: BuiltinPresetName, Seats: "native:second"},
	}}
	err := st.SavePresets(&dup)
	if !errors.Is(err, ErrPresetDuplicateBuiltin) {
		t.Fatalf("SavePresets = %v; want ErrPresetDuplicateBuiltin", err)
	}
	if strings.Contains(err.Error(), BuiltinPresetName) {
		t.Errorf("refusal echoes the record name operand: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("a refused save must not write the file (stat err = %v)", statErr)
	}
	if dup.Presets[0].SeatsSHA256 != "" || dup.Presets[2].SeatsSHA256 != "" {
		t.Errorf("a refused save must not stamp markers: %+v", dup.Presets)
	}
}

// The existing single-seed shapes still round-trip: one reserved-name record
// saves and loads unchanged, its marker is stamped, and a clean load stays
// warning-silent.
func TestSingleSeedRoundTripUnchangedByFDupGate(t *testing.T) {
	home, _ := ladderEnv(t)
	path := filepath.Join(home, "fanout-presets.json")
	st := &PresetFileStore{Path: path}
	in := &PresetsFile{Presets: []Preset{
		{Name: BuiltinPresetName, Seats: "native:solo"},
		{Name: "ordinary", N: 2},
	}}
	if err := st.SavePresets(in); err != nil {
		t.Fatalf("a single seed record must keep saving: %v", err)
	}

	var warn bytes.Buffer
	st.WarnOut = &warn
	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Presets) != 2 || loaded.Presets[0].Name != BuiltinPresetName ||
		loaded.Presets[0].Seats != "native:solo" {
		t.Fatalf("round-trip drifted: %+v", loaded.Presets)
	}
	if got := loaded.Presets[0].SeatsSHA256; got != SeatsHash("native:solo") {
		t.Errorf("seed marker = %q; want the stamped advisory hash", got)
	}
	if warn.Len() != 0 {
		t.Errorf("a clean single-seed load must stay silent: %q", warn.String())
	}

	// And the run the survivor steers keeps exactly the pre-existing echo shape.
	stderr := &bytes.Buffer{}
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := seatsOf(cfg.Seats); got != "native:solo" {
		t.Errorf("seats = %q; want the solo seed lineup", got)
	}
	wantEcho := "fanout: seat lineup from preset \"(built-in defaults)\" (default_preset rung, no key set): 1 seat(s), 0 metered provider seats (native:solo)"
	if stderr.String() != wantEcho+"\n" {
		t.Errorf("single-seed echo drifted\n got: %q\nwant: %q", stderr.String(), wantEcho)
	}
}

// A7: the F-12 disclosure names the record through providers.MaskSlot. The
// benign built-in label renders byte-identical, and a planted 16-rune
// credential in the same slot masks - the evidence is taken at the warning
// renderer itself, not copied from a sibling sink.
func TestDuplicateSeedWarningMasksPlantedCredential(t *testing.T) {
	benign := duplicateBuiltinPresetWarning(BuiltinPresetName, 1)
	if !strings.Contains(benign, `"`+BuiltinPresetName+`"`) {
		t.Errorf("benign warning lost the byte-identical label: %q", benign)
	}
	if !strings.Contains(benign, "ignoring 1 later duplicate record(s)") {
		t.Errorf("warning lost the duplicate count: %q", benign)
	}

	if got := providers.MaskSlot(BuiltinPresetName); got != BuiltinPresetName {
		t.Errorf("MaskSlot(%q) = %q; want byte-identical", BuiltinPresetName, got)
	}
	planted := duplicateBuiltinPresetWarning(testfixtures.SKLive, 1)
	if strings.Contains(planted, testfixtures.SKLive) {
		t.Errorf("warning leaks the planted credential: %q", planted)
	}
	if !strings.Contains(planted, `"sk_li****"`) {
		t.Errorf("warning does not mask the planted credential: %q", planted)
	}
}
