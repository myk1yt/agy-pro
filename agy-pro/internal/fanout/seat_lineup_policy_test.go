package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// Final-fix round (fix-list-1) behavior pins:
//
//	FL-2  a self-conflicting default preset must not dead-end the door path
//	FL-3  standing approval scoped to the default_preset rung; the lineup echo,
//	      the summary block and the advisory seats-hash marker
//	FL-4  control-rune gate at parse, at load, and on every spec echo
//	FL-5  a degraded run never wears the success checkmark

// Retirement of the preset `models` field, at the DOOR level: the delegated
// vector (no --seats, because the door applies the pin) must keep running on the
// seats lineup of a record that STILL carries the retired "models" key. The load
// door discloses the ignore with one advisory and never applies the value, so the
// run's lineup and its standing approval are exactly the seats row.
func TestRunRetiredModelsFieldDoesNotDisturbTheSeatsLineup(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv("AGY_PRO_PROVIDERS", filepath.Join(home, "no-such-providers.json"))
	t.Setenv(EnvFanoutPreset, "")
	// Hand-authored raw JSON: a Preset record cannot carry the key any more, so the
	// on-disk shape is the only way to reproduce a pre-removal file.
	body := `{"presets":[{"name":"nightly","task":"review","seats":"native:gemini-a,prov/m1","models":"prov/legacy"}],"default_preset":"nightly"}`
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	old := executeEngine
	var got []RunConfig
	executeEngine = func(_ context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw bytes.Buffer
	// The door vector: exactly what internal/swarm emits when it applies the pin.
	if code := Run([]string{"-task", "review", "-prompt", "p", "-json"}, nil, &out, &errw); code != 0 {
		t.Fatalf("exit = %d; a legacy models record must not dead-end the run; stderr = %s", code, errw.String())
	}
	if len(got) != 1 {
		t.Fatalf("engine calls = %d; want 1", len(got))
	}
	if s := seatsOf(got[0].Seats); s != "native:gemini-a,prov/m1" {
		t.Errorf("seats = %q; want the preset lineup", s)
	}
	if len(got[0].Models) != 0 {
		t.Errorf("models = %v; want the retired key ignored", got[0].Models)
	}
	if !strings.Contains(errw.String(), "the legacy models field is removed; use seats") {
		t.Errorf("stderr must carry the retired-field advisory: %q", errw.String())
	}
	if got[0].SeatLineup.Preset != "nightly" || !got[0].SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing nightly origin", got[0].SeatLineup)
	}
}

// FL-3(a): standing approval is scoped to the default_preset rung. A named preset
// resolves the same lineup but must read as ad-hoc, and its echo says so.
func TestLineupRungScopingAndEcho(t *testing.T) {
	for _, tc := range []struct {
		name     string
		preset   string
		env      string
		wantRung string
		standing bool
		adHoc    bool
	}{
		{name: "default key", wantRung: RungDefaultPreset, standing: true},
		{name: "--preset", preset: "nightly", wantRung: RungPresetFlag, adHoc: true},
		{name: "env", env: "nightly", wantRung: RungPresetEnv, adHoc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			savePresetsFile(t, home, &PresetsFile{
				Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a,nw/glm"}},
				DefaultPreset: "nightly",
			})
			if tc.env != "" {
				t.Setenv(EnvFanoutPreset, tc.env)
			}
			f := ladderFlags(stderr)
			f.preset = tc.preset
			cfg, err := buildRunConfig(f, "p")
			if err != nil {
				t.Fatalf("buildRunConfig: %v", err)
			}
			if cfg.SeatLineup.Rung != tc.wantRung {
				t.Errorf("Rung = %q; want %q", cfg.SeatLineup.Rung, tc.wantRung)
			}
			if cfg.SeatLineup.Standing() != tc.standing {
				t.Errorf("Standing() = %v; want %v", cfg.SeatLineup.Standing(), tc.standing)
			}
			echo := stderr.String()
			if !strings.Contains(echo, "seat lineup from preset \"nightly\"") {
				t.Fatalf("the lineup echo must always be printed: %q", echo)
			}
			if !strings.Contains(echo, "("+tc.wantRung+" rung)") {
				t.Errorf("echo must name the rung %q: %q", tc.wantRung, echo)
			}
			if !strings.Contains(echo, "2 seat(s), 1 metered provider seat") {
				t.Errorf("echo must state the seat and metered counts: %q", echo)
			}
			if got := strings.Contains(echo, "ad-hoc"); got != tc.adHoc {
				t.Errorf("ad-hoc clause present = %v; want %v (%q)", got, tc.adHoc, echo)
			}
		})
	}
}

// FL-3(c): the summary records the preset AND the rung, and says whether the
// lineup was standing approval.
func TestSummaryRecordsSeatLineupOrigin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		origin   SeatLineupOrigin
		standing bool
	}{
		{name: "standing", origin: SeatLineupOrigin{Preset: "nightly", Rung: RungDefaultPreset}, standing: true},
		{name: "adhoc", origin: SeatLineupOrigin{Preset: "named", Rung: RungPresetFlag}, standing: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			regPath, outDir := driftFixture(t)
			specs, err := ParseSeatSpecs("prov-a/model-a")
			if err != nil {
				t.Fatal(err)
			}
			var warn bytes.Buffer
			summary, err := Execute(context.Background(), driftConfig(regPath, outDir, specs, tc.origin, &warn))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if summary.SeatLineup == nil {
				t.Fatalf("summary.SeatLineup is nil; want the origin block")
			}
			if summary.SeatLineup.Preset != tc.origin.Preset || summary.SeatLineup.Rung != tc.origin.Rung {
				t.Errorf("SeatLineup = %+v; want preset %q rung %q", summary.SeatLineup, tc.origin.Preset, tc.origin.Rung)
			}
			if summary.SeatLineup.Standing != tc.standing {
				t.Errorf("Standing = %v; want %v", summary.SeatLineup.Standing, tc.standing)
			}
			if summary.SeatLineup.Seats != 1 || summary.SeatLineup.MeteredSeat != 1 {
				t.Errorf("counts = %d seats / %d metered; want 1/1", summary.SeatLineup.Seats, summary.SeatLineup.MeteredSeat)
			}
			raw, rerr := os.ReadFile(filepath.Join(outDir, "summary.json"))
			if rerr != nil {
				t.Fatalf("read summary.json: %v", rerr)
			}
			var decoded struct {
				SeatLineup *SeatLineupReport `json:"seat_lineup"`
			}
			if uerr := json.Unmarshal(raw, &decoded); uerr != nil || decoded.SeatLineup == nil {
				t.Fatalf("summary.json seat_lineup = %v (%v); want the block", decoded.SeatLineup, uerr)
			}
			if decoded.SeatLineup.Preset != tc.origin.Preset || decoded.SeatLineup.Rung != tc.origin.Rung {
				t.Errorf("summary.json seat_lineup = %+v; want preset+rung", decoded.SeatLineup)
			}
		})
	}
}

// FL-3(e): the advisory provenance marker. SavePresets stamps it, so a run on a
// store-written record is silent; a hand edit that changes the seats value
// without the marker prints ONE advisory notice, and a record with no marker at
// all stays silent (it may predate the marker or be hand-authored: advisory, not
// a seal).
func TestSeatsHashMarkerIsAdvisory(t *testing.T) {
	home, stderr := ladderEnv(t)
	path := filepath.Join(home, "fanout-presets.json")

	// A store-mediated save stamps the marker.
	if err := (&PresetFileStore{Path: path}).SavePresets(&PresetsFile{
		Presets:       []Preset{{Name: "nightly", Seats: "native:gemini-a"}},
		DefaultPreset: "nightly",
	}); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	var saved PresetsFile
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Presets[0].SeatsSHA256 != SeatsHash("native:gemini-a") {
		t.Fatalf("SeatsSHA256 = %q; want the stamped hash", saved.Presets[0].SeatsSHA256)
	}
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if strings.Contains(stderr.String(), "edited outside the preset editor") {
		t.Errorf("a store-written record must not warn: %q", stderr.String())
	}

	// A hand edit of the seats value, marker left stale: ONE advisory notice, and
	// the run still uses the on-disk lineup.
	handEdited := strings.Replace(string(raw), "native:gemini-a", "native:gemini-b", 1)
	if err := os.WriteFile(path, []byte(handEdited), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("a hash mismatch must never block the run: %v", err)
	}
	if s := seatsOf(cfg.Seats); s != "native:gemini-b" {
		t.Errorf("seats = %q; want the on-disk value used anyway", s)
	}
	if !strings.Contains(stderr.String(), "edited outside the preset editor") || !strings.Contains(stderr.String(), "advisory") {
		t.Errorf("stderr must carry the advisory provenance notice: %q", stderr.String())
	}

	// No marker at all (hand-authored or pre-marker record): R2-3 says this arm
	// WARNS too, with its own wording - silence here would arm standing cost
	// approval on a lineup no editor ever wrote.
	if err := os.WriteFile(path, []byte(`{"presets":[{"name":"nightly","seats":"native:gemini-c"}],"default_preset":"nightly"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	cfg, err = buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("a marker-less pin must never fail the run: %v", err)
	}
	if s := seatsOf(cfg.Seats); s != "native:gemini-c" {
		t.Errorf("seats = %q; want the on-disk lineup applied", s)
	}
	if strings.Contains(stderr.String(), "edited outside the preset editor") {
		t.Errorf("a marker-less record is not an edit-after-save: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "NO provenance marker") || !strings.Contains(stderr.String(), "advisory") {
		t.Errorf("a marker-less standing pin must print the hand-authored advisory: %q", stderr.String())
	}
}

// FL-4: a control or invisible-format rune in a seat token is REJECTED, never
// stripped, and the error never echoes the rune.
func TestParseSeatSpecsRejectsControlRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
	}{
		{name: "ESC in provider token", spec: "nw/\x1b[2Jmodel"},
		{name: "BEL in provider token", spec: "nw/mod\x07el"},
		{name: "NUL in provider token", spec: "nw/mod\x00el"},
		{name: "right-to-left override", spec: "nw/mod\u202eel"},
		{name: "zero width space", spec: "nw/mod\u200bel"},
		{name: "control in a later seat", spec: "native:gemini-a,nw/mod\x1bel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSeatSpecs(tc.spec)
			if err == nil {
				t.Fatalf("ParseSeatSpecs(%q) accepted a control rune", tc.spec)
			}
			if !strings.Contains(err.Error(), "control or invisible format") {
				t.Errorf("error = %q; want the control-class message", err)
			}
			for _, r := range err.Error() {
				if r == 0x1b || r == 0x07 || r == 0x00 || r == 0x202e || r == 0x200b {
					t.Fatalf("error echoes the planted rune: %q", err.Error())
				}
			}
		})
	}

	// Benign tokens are untouched, including the native grammar.
	specs, err := ParseSeatSpecs("native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex")
	if err != nil || len(specs) != 2 {
		t.Fatalf("benign specs = %v, %v; want 2 seats and no error", specs, err)
	}
	// TrimSpace still owns edge whitespace: a trailing newline is not a control
	// the parser should reject (it is trimmed before the check).
	if _, err := ParseSeatSpecs("native:a\n"); err != nil {
		t.Errorf("a trailing newline must remain trimmed, not rejected: %v", err)
	}
}

// FL-4: the preset loader gates control-carrying records (the providers.json
// precedent) by dropping that record with one warning, keeping every other row.
func TestLoadPresetsDropsControlCarryingRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	path := filepath.Join(home, "fanout-presets.json")
	body := `{"presets":[
		{"name":"good","seats":"native:gemini-a"},
		{"name":"planted","seats":"native:a\u001b[2J"},
		{"name":"planted-tools","tools":"read,\u202eexec"},
		{"name":"alsogood","n":2}
	],"default_preset":"good"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	st := &PresetFileStore{Path: path, WarnOut: &warn}
	pf, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	var names []string
	for _, p := range pf.Presets {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "good,alsogood" {
		t.Errorf("loaded presets = %q; want the two clean rows", got)
	}
	if n := strings.Count(warn.String(), "has control/format runes"); n != 2 {
		t.Errorf("warnings = %d; want one per dropped record: %q", n, warn.String())
	}
	for _, r := range warn.String() {
		if r == 0x1b || r == 0x202e {
			t.Fatalf("the warning echoes a control rune: %q", warn.String())
		}
	}
}

// FL-4: the lineup echo is control-safe even when the value reaches it anyway
// (defense in depth: the parse and load gates are upstream, not the only guard).
func TestSeatLineupEchoStripsControlRunes(t *testing.T) {
	line := seatLineupEcho(SeatLineupOrigin{Preset: "night\x1bly", Rung: RungDefaultPreset},
		[]SeatSpec{{Kind: SeatKindProvider, Raw: "nw/m\x07od", ModelID: "nw/m\x07od"}})
	for _, r := range line {
		if r == 0x1b || r == 0x07 {
			t.Fatalf("lineup echo carries a control rune: %q", line)
		}
	}
	if !strings.Contains(line, "1 metered provider seat") {
		t.Errorf("echo = %q; want the metered count", line)
	}
}

// FL-5: the success checkmark is suppressed on a degraded run and replaced by the
// degraded notice; a clean run keeps it.
func TestRunBannerSuppressesSuccessCheckmarkWhenDegraded(t *testing.T) {
	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	t.Setenv("AGY_PRO_PROVIDERS", filepath.Join(home, "no-such-providers.json"))
	t.Setenv(EnvFanoutPreset, "")

	old := executeEngine
	t.Cleanup(func() { executeEngine = old })

	for _, tc := range []struct {
		name     string
		summary  *SummaryReport
		wantMark bool
	}{
		{
			name:     "clean run keeps the checkmark",
			summary:  &SummaryReport{Task: "review", NRequested: 1, NCompleted: 1},
			wantMark: true,
		},
		{
			name: "degraded run drops it",
			summary: &SummaryReport{Task: "review", NRequested: 2, NCompleted: 1, Degraded: true,
				SkippedSeats: 1, SkippedSeatSpecs: []string{"prov/gone"}},
			wantMark: false,
		},
		{
			name: "degraded with zero disputes still drops it",
			summary: &SummaryReport{Task: "review", NRequested: 1, NCompleted: 1, Degraded: true,
				Seats: []SeatResult{{SeatID: 1, Status: "failed", Degraded: true}}},
			wantMark: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sum := tc.summary
			executeEngine = func(_ context.Context, _ RunConfig) (*SummaryReport, error) {
				return sum, nil
			}
			var out, errw bytes.Buffer
			// No -json: the human banner is the surface under test. OutDir "" keeps
			// the artifact paths relative and writes nothing.
			if code := Run([]string{"-prompt", "p", "-out", t.TempDir()}, nil, &out, &errw); code != 0 {
				t.Fatalf("exit = %d; stderr = %s", code, errw.String())
			}
			banner := out.String()
			hasMark := strings.Contains(banner, "✅")
			if hasMark != tc.wantMark {
				t.Errorf("success checkmark present = %v; want %v\nbanner:\n%s", hasMark, tc.wantMark, banner)
			}
			if !tc.wantMark && !strings.Contains(banner, "degraded run") {
				t.Errorf("a degraded run must print the degraded notice:\n%s", banner)
			}
		})
	}
}
