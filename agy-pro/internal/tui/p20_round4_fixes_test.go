package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/providers"
)

func TestP20EditorTwoDropsShowCount(t *testing.T) {
	path := editorHome(t)
	raw := []byte(`{"presets":[{"name":"bad-one","tools":"read\u2028search"},{"name":"bad-two","task":"review\u2029forged"},{"name":"healthy","task":"review","n":2}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	app, scr := newEditorApp("healthy")
	if len(scr.dropNotices) != 2 {
		t.Fatalf("construction drop notices = %d, want 2", len(scr.dropNotices))
	}
	app.CaptureRun(&ScriptedKeys{})
	if msg := app.Message(); !strings.Contains(msg, "(+1 more)") {
		t.Fatalf("two-drop message lacks count: %q", msg)
	}
	t.Logf("two-drop editor message=%q", app.Message())
}

func TestP20EditorDropNoticesDoNotAccumulateAcrossSave(t *testing.T) {
	path := editorHome(t)
	raw := []byte(`{"presets":[{"name":"bad-one","tools":"read\u2028search"},{"name":"bad-two","task":"review\u2029forged"},{"name":"healthy","task":"review","n":2}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	app, scr := newEditorApp("healthy")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if len(scr.dropNotices) != 2 {
		t.Fatalf("drop notices accumulated across save: got %d, want 2", len(scr.dropNotices))
	}
}

func TestP20DuplicatePreservesAbsentOrStaleMarker(t *testing.T) {
	for _, marker := range []string{"", "stale-marker"} {
		t.Run(marker, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(providers.EnvHome, home)
			path := filepath.Join(home, "fanout-presets.json")
			raw, err := json.Marshal(&fanout.PresetsFile{Presets: []fanout.Preset{{Name: "hand-planted", Seats: "native:seed", SeatsSHA256: marker}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			st := &fanout.PresetFileStore{Path: path}
			app, scr := presetsAppAt(st, 100, 30)
			scr.app = app
			scr.duplicate(scr.pf.Presets[0])
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got fanout.PresetsFile
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Presets) != 2 {
				t.Fatalf("records after duplicate = %d, want 2", len(got.Presets))
			}
			if got.Presets[0].SeatsSHA256 != marker || got.Presets[1].SeatsSHA256 != marker {
				t.Fatalf("markers after duplicate = %q, %q; want preserved %q", got.Presets[0].SeatsSHA256, got.Presets[1].SeatsSHA256, marker)
			}
			t.Logf("duplicate source marker=%q; copy marker=%q", got.Presets[0].SeatsSHA256, got.Presets[1].SeatsSHA256)
		})
	}
}
