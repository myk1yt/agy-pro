package fanout

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8SaveRefusesLoadDroppedRuneClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value string
	}{
		{name: "name line separator", field: "name", value: "line\u2028separator"},
		{name: "name emoji ZWJ", field: "name", value: "family\u200demoji"},
		{name: "root line separator", field: "root", value: "work\u2028escape"},
		{name: "root emoji ZWJ", field: "root", value: "work\u200descape"},
		{name: "out_dir line separator", field: "out_dir", value: "reports\u2028forge"},
		{name: "out_dir emoji ZWJ", field: "out_dir", value: "reports\u200dforge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Preset{Name: "ordinary"}
			switch tc.field {
			case "name":
				p.Name = tc.value
			case "root":
				p.Root = tc.value
			case "out_dir":
				p.OutDir = tc.value
			}
			st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
			err := st.SavePresets(&PresetsFile{Presets: []Preset{p}})
			if err == nil {
				t.Fatalf("SavePresets accepted a %s value the load gate drops", tc.field)
			}
			if tc.field == "name" && !errors.Is(err, ErrPresetNameControl) {
				t.Errorf("name refusal = %v; want ErrPresetNameControl", err)
			}
			if tc.field != "name" && !errors.Is(err, ErrPresetPathControl) {
				t.Errorf("path refusal = %v; want ErrPresetPathControl", err)
			}
			if strings.Contains(err.Error(), tc.value) || strings.ContainsRune(err.Error(), '\u2028') || strings.ContainsRune(err.Error(), '\u200d') {
				t.Errorf("refusal echoes the rejected operand: %q", err)
			}
			if _, statErr := os.Stat(st.Path); !os.IsNotExist(statErr) {
				t.Errorf("a refused save wrote the file: %v", statErr)
			}
		})
	}
}

func TestP8BenignUnicodeNamesAndPathsRoundTrip(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	in := &PresetsFile{Presets: []Preset{
		{Name: "spaces and-hyphens", Root: "작업 공간", OutDir: "レポート"},
		{Name: "日本語 プリセット"},
		{Name: "中文 预设"},
		{Name: "العربية-الافتراضي"},
		{Name: "한국어 기본값"},
	}}
	if err := st.SavePresets(in); err != nil {
		t.Fatalf("benign Unicode save: %v", err)
	}
	var warn bytes.Buffer
	st.WarnOut = &warn
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Presets) != len(in.Presets) {
		t.Fatalf("loaded %d presets; want %d", len(got.Presets), len(in.Presets))
	}
	if warn.Len() != 0 {
		t.Errorf("benign Unicode records produced a drop warning: %q", warn.String())
	}
}

func TestP8HandAuthoredDropWarningReachesFanoutWriters(t *testing.T) {
	t.Run("ladder stderr", func(t *testing.T) {
		home, stderr := ladderEnv(t)
		writeRawPresetsFile(t, home, `{"presets":[{"name":"hand\u2028authored","seats":"native:unused"}]}`)
		if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stderr.String(), `warning: ignoring preset; name has control/format runes; record name "hand\u2028authored"`) {
			t.Errorf("ladder discarded the hand-authored drop warning: %q", stderr.String())
		}
	})

	t.Run("seat lineup resolver process stderr", func(t *testing.T) {
		home, _ := ladderEnv(t)
		writeRawPresetsFile(t, home, `{"presets":[{"name":"hand\u2028authored","seats":"native:unused"}]}`)
		old := os.Stderr
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stderr = w
		defer func() { os.Stderr = old }()
		_, resolveErr := ResolvePresetSeatLineup()
		_ = w.Close()
		os.Stderr = old
		got, readErr := io.ReadAll(r)
		_ = r.Close()
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(got), "warning: ignoring preset; name has control/format runes") {
			t.Errorf("resolver discarded the hand-authored drop warning: %q", got)
		}
	})
}

func TestP8ProvenanceMarkerSurvivesUnrelatedSaves(t *testing.T) {
	home, stderr := ladderEnv(t)
	path := writeRawPresetsFile(t, home, `{"presets":[{"name":"(built-in defaults)","seats":"native:seed"},{"name":"ordinary","n":2}]}`)
	st := &PresetFileStore{Path: path}
	assertAdvisory := func(want bool) {
		t.Helper()
		stderr.Reset()
		if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(stderr.String(), "NO provenance marker"); got != want {
			t.Errorf("provenance advisory present = %v, want %v; stderr=%q", got, want, stderr.String())
		}
	}
	assertAdvisory(true)

	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	loaded.Presets[1].N = 3
	if err := st.SavePresets(loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Presets[0].SeatsSHA256; got != "" {
		t.Errorf("unrelated save stamped the hand-authored seed: %q", got)
	}
	assertAdvisory(true)

	loaded.Presets[0].Seats = "native:edited"
	if err := ValidateSeedPresetForSave(loaded.Presets[0]); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePresets(loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Presets[0].SeatsSHA256; got != SeatsHash("native:edited") {
		t.Errorf("edited seed marker = %q; want its own current seats hash", got)
	}
	assertAdvisory(false)
}

func TestP8StaleMarkerSurvivesUnrelatedSave(t *testing.T) {
	home, _ := ladderEnv(t)
	path := writeRawPresetsFile(t, home, `{"presets":[{"name":"(built-in defaults)","seats":"native:seed","seats_sha256":"stale-marker"},{"name":"ordinary","n":2}]}`)
	st := &PresetFileStore{Path: path}
	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	loaded.Presets[1].N = 4
	if err := st.SavePresets(loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Presets[0].SeatsSHA256; got != "stale-marker" {
		t.Errorf("unrelated save replaced stale provenance evidence: %q", got)
	}

	rebuilt := &PresetsFile{Presets: []Preset{
		{Name: BuiltinPresetName, Seats: "native:seed"},
		{Name: "ordinary", N: 5},
	}}
	if err := st.SavePresets(rebuilt); err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.Presets[0].SeatsSHA256; got != "stale-marker" {
		t.Errorf("rebuilt unrelated save did not preserve the stored marker: %q", got)
	}
}
