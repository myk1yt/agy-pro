package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"agypro/internal/fanout"
)

func TestP22EditorDropCountVisibleInRenderedEightyColumnFrame(t *testing.T) {
	path := editorHome(t)
	longName := strings.Repeat("attacker-length-name-", 12)
	raw, err := json.Marshal(&fanout.PresetsFile{Presets: []fanout.Preset{
		{Name: longName, Tools: "read\u2028search"},
		{Name: "bad-two", Task: "review\u2029forged"},
		{Name: "healthy", Task: "review", N: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	app, scr := newEditorApp("healthy")
	app.Resize(80, 24)
	app.CaptureRun(&ScriptedKeys{})
	if len(scr.dropNotices) != 2 {
		t.Fatalf("construction drop notices = %d, want 2", len(scr.dropNotices))
	}
	frame := app.Frame().String()
	if !strings.Contains(frame, "(+1 more)") {
		t.Fatalf("80x24 rendered frame loses the additional-drop count:\n%s", frame)
	}
	if got := len(app.Frame().Lines); got != 24 {
		t.Fatalf("rendered frame height = %d, want 24", got)
	}
	t.Logf("80x24 frame:\n%s", frame)
}

func TestP22EditorDuplicateNameSaveIsFirstWins(t *testing.T) {
	path := editorHome(t)
	raw := []byte(`{"default_preset":"x","presets":[{"name":"x","seats":"native:first"},{"name":"x","seats":"native:second"},{"name":"other","n":2}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	app, _ := newEditorApp("x")
	app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyCtrlS}})
	if !strings.Contains(app.Message(), "Preset x saved.") {
		t.Fatalf("save message = %q", app.Message())
	}
	pf, err := editorStore(t, path).LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Presets) != 3 {
		t.Fatalf("record count = %d, want ordinary duplicates preserved", len(pf.Presets))
	}
	if pf.Presets[0].Seats != "native:first" || pf.Presets[0].SeatsSHA256 == "" {
		t.Fatalf("resolved first record was not saved: %+v", pf.Presets[0])
	}
	if pf.Presets[1].Seats != "native:second" {
		t.Fatalf("shadow duplicate was rewritten: %+v", pf.Presets[1])
	}
	resolved, ok := pf.FindPreset("x")
	if !ok || resolved.SeatsSHA256 != pf.Presets[0].SeatsSHA256 {
		t.Fatalf("run resolution and save target disagree: resolved=%+v first=%+v", resolved, pf.Presets[0])
	}
	t.Logf("first=%+v second=%+v", pf.Presets[0], pf.Presets[1])
}
