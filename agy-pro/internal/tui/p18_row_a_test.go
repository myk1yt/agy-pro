package tui

import (
	"encoding/json"
	"os"
	"testing"

	"agypro/internal/fanout"
)

func TestEditorSaveStampsOnlyDeliberatelySavedPreset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		marker     string
		editName   string
		editSeats  bool
		wantMarker string
	}{
		{
			name:       "no-edit seed save stamps its own marker",
			editName:   fanout.BuiltinPresetName,
			wantMarker: fanout.SeatsHash("native:seed"),
		},
		{
			name:       "unrelated save preserves a missing seed marker",
			editName:   "ordinary",
			wantMarker: "",
		},
		{
			name:       "edited seed save stamps the edited lineup",
			editName:   fanout.BuiltinPresetName,
			editSeats:  true,
			wantMarker: fanout.SeatsHash("native:edited"),
		},
		{
			name:       "unrelated save preserves a stale seed marker",
			marker:     "stale-marker",
			editName:   "ordinary",
			wantMarker: "stale-marker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := editorHome(t)
			pf := &fanout.PresetsFile{Presets: []fanout.Preset{
				{Name: fanout.BuiltinPresetName, Seats: "native:seed", SeatsSHA256: tc.marker},
				{Name: "ordinary", N: 2},
			}}
			raw, err := json.Marshal(pf)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			app, scr := newEditorApp(tc.editName)
			if tc.editSeats {
				scr.seatVals = []string{"native:edited"}
				scr.seatsDirty = true
				scr.syncSeatRows()
			}
			app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})

			got := reloadEditorPresets(t, path)
			seed, ok := got.FindPreset(fanout.BuiltinPresetName)
			if !ok {
				t.Fatalf("seed missing after save: %+v", got.Presets)
			}
			if seed.SeatsSHA256 != tc.wantMarker {
				t.Errorf("seed marker = %q, want %q", seed.SeatsSHA256, tc.wantMarker)
			}
		})
	}
}
