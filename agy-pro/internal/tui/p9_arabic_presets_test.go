package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
)

func TestPresetsArabicRowOneVisibleAcrossStoreStates(t *testing.T) {
	SetLanguage("ar")
	t.Cleanup(func() { SetLanguage("en") })

	seed := fanout.Preset{Name: fanout.BuiltinPresetName, Task: "security", Seats: "native:a,nw/m1"}
	ordinary := fanout.Preset{Name: strings.Repeat("ع", 45), Task: "review", Seats: "native:b"}
	states := []struct {
		name string
		pf   fanout.PresetsFile
	}{
		{name: "empty", pf: fanout.PresetsFile{}},
		{name: "seed-only", pf: fanout.PresetsFile{Presets: []fanout.Preset{seed}}},
		{name: "seed+ordinary", pf: fanout.PresetsFile{Presets: []fanout.Preset{seed, ordinary}}},
		{name: "seed-as-default", pf: fanout.PresetsFile{Presets: []fanout.Preset{seed, ordinary}, DefaultPreset: fanout.BuiltinPresetName}},
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			appTestHome(t, "ar")
			st := &fanout.PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
			pf := tc.pf
			if err := st.SavePresets(&pf); err != nil {
				t.Fatalf("save fixture: %v", err)
			}
			app := NewApp(80, 24, DirRTL)
			app.Push(&stubScreen{title: "root", mode: ModeNav})
			scr := NewPresetsScreen(st, nil).(*presetsScreen)
			app.Push(scr)
			app.CaptureRun(&ScriptedKeys{})

			frame := app.Frame()
			if frame.Width != 80 || frame.Height != 24 || len(frame.Lines) > 24 {
				t.Fatalf("frame geometry = %dx%d with %d lines:\n%s", frame.Width, frame.Height, len(frame.Lines), frame.String())
			}
			if got := len(scr.explainLines(80)); got != 4 {
				t.Fatalf("explain lines = %d, want 4", got)
			}
			noticeRows := 0
			if tc.name == "empty" {
				noticeRows = 2
			}
			if got, want := scr.list.PageSize(), 24-presetsListChrome-noticeRows; got != want {
				t.Fatalf("page size = %d, want %d", got, want)
			}
			row := ""
			for _, line := range frame.Lines {
				if strings.Contains(line, "[ 1]") {
					row = line
					break
				}
			}
			if strings.TrimSpace(row) == "" {
				t.Fatalf("Arabic row [1] is blank:\n%s", frame.String())
			}
			if !strings.Contains(row, "▶ [ 1]") || !strings.Contains(row, fanout.BuiltinPresetName) || cellwidth.String(row) > 80 {
				t.Fatalf("row [1] lost its gutter/name or exceeds 80 cells: %q", row)
			}
			wantNote := "task=review"
			if tc.name != "empty" {
				wantNote = "task=security"
			}
			if !strings.Contains(row, wantNote) {
				t.Fatalf("row [1] lost its note %q: %q", wantNote, row)
			}
			if tc.name != "empty" && !strings.Contains(row, "المقاعد") {
				t.Fatalf("row [1] lost the Arabic seats note: %q", row)
			}
			rendered := mirrorLines(frame.Lines, 80)
			renderedRow := ""
			for _, line := range rendered {
				if strings.Contains(line, "[ 1]") {
					renderedRow = line
					break
				}
			}
			if strings.TrimSpace(renderedRow) == "" || !strings.Contains(renderedRow, fanout.BuiltinPresetName) {
				t.Fatalf("rendered Arabic row [1] is blank or lost its name:\n%s", strings.Join(rendered, "\n"))
			}
			t.Logf("logical row [1]: %q", row)
			t.Logf("rendered row [1]: %q", renderedRow)

			if tc.name == "seed+ordinary" || tc.name == "seed-as-default" {
				ordinaryRow := ""
				for _, line := range frame.Lines {
					if strings.Contains(line, "[ 2]") {
						ordinaryRow = line
						break
					}
				}
				if strings.TrimSpace(ordinaryRow) == "" || !strings.Contains(ordinaryRow, "ع") {
					t.Fatalf("ordinary Arabic row is blank or lost its label: %q", ordinaryRow)
				}
				t.Logf("ordinary Arabic row [2]: %q", ordinaryRow)
			}
		})
	}
}
