package tui

import (
	"strings"
	"testing"

	"agypro/internal/cellwidth"
)

// TestFrameGolden pins the frame snapshot layout with a mixed ASCII+CJK
// fixture: title, blank, body, blank, message, blank, footer. CJK text is
// kept verbatim (the painter, not the snapshot, pads and mirrors).
func TestFrameGolden(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "Hub",
		mode:  ModeNav,
		body:  []string{"설정 Providers", "언어 Language"},
	})
	app.SetMessage("한 line saved")

	f := app.Frame()
	if f.Width != 80 || f.Height != 20 || f.Dir != DirLTR {
		t.Fatalf("frame geometry = %dx%d dir %v, want 80x20 LTR", f.Width, f.Height, f.Dir)
	}

	want := strings.Join([]string{
		"Hub",
		"",
		"설정 Providers",
		"언어 Language",
		"",
		"한 line saved",
		"",
		baseFooter,
	}, "\n")
	if got := f.String(); got != want {
		t.Fatalf("frame mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestFrameDirRidesAlong pins that DirRTL reaches the frame while the
// lines stay in logical order (mirroring is the P4 painter's job).
func TestFrameDirRidesAlong(t *testing.T) {
	app := NewApp(80, 20, DirRTL)
	app.Push(&stubScreen{title: "اللغة", mode: ModeNav, body: []string{"العربية"}})
	f := app.Frame()
	if f.Dir != DirRTL {
		t.Fatalf("frame dir = %v, want DirRTL", f.Dir)
	}
	if f.Lines[0] != "اللغة" {
		t.Fatalf("RTL title mutated: %q", f.Lines[0])
	}
}

func TestFrameModalBlock(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{title: "Providers", mode: ModeNav})
	app.ShowModal(NewConfirm("Delete provider?", []string{"provider: acme"}, nil))
	f := app.Frame()
	want := strings.Join([]string{
		"Providers",
		"",
		"Delete provider?",
		"provider: acme",
		"y confirm · n/Esc cancel",
		"",
		baseFooter,
	}, "\n")
	if got := f.String(); got != want {
		t.Fatalf("modal frame mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFrameHeightClamp(t *testing.T) {
	app := NewApp(80, 4, DirLTR)
	app.Push(&stubScreen{title: "Tall", mode: ModeNav, body: []string{"1", "2", "3", "4", "5"}})
	f := app.Frame()
	if len(f.Lines) != 4 {
		t.Fatalf("frame lines = %d, want 4 (clamped to height)", len(f.Lines))
	}
}

// TestCaptureRun drives a full headless run through the scripted seam:
// one frame per step, clean exit on Ctrl+C.
func TestCaptureRun(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	var got []Key
	app.Push(&stubScreen{title: "Hub", mode: ModeNav, record: &got})

	keys := &ScriptedKeys{Keys: []Key{KeyUp, KeyCtrlC}}
	out := app.CaptureRun(keys)
	if strings.Count(out, "Hub") != 2 {
		t.Fatalf("captured frames = %d, want 2 (initial + one step)", strings.Count(out, "Hub"))
	}
	if len(got) != 1 || got[0] != KeyUp {
		t.Fatalf("screen keys = %v, want [up]", got)
	}
	if !strings.HasSuffix(out, baseFooter+"\n") {
		t.Fatalf("capture does not end with the footer frame")
	}
}

func TestAppStackBasics(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	if app.Top() != nil || app.Depth() != 0 {
		t.Fatal("empty stack must have no top and depth 0")
	}
	app.Push(&stubScreen{title: "A", mode: ModeNav})
	app.Push(&stubScreen{title: "B", mode: ModeNav})
	if app.Top().Title() != "B" || app.Depth() != 2 {
		t.Fatalf("top/depth = %s/%d, want B/2", app.Top().Title(), app.Depth())
	}
	if !app.Pop() || app.Top().Title() != "A" {
		t.Fatalf("pop did not return to A")
	}
	if app.Pop() {
		t.Fatal("pop at the root must fail (Esc at the root exits instead)")
	}
	if app.Top() == nil || app.Depth() != 1 {
		t.Fatal("the root must survive a failed pop")
	}
}

func TestAppExitByScreen(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	quitter := &stubScreen{title: "Q", mode: ModeNav, quitAfter: 1}
	app.Push(quitter)
	if app.Step(KeyUp) {
		t.Fatal("screen Exit() did not stop the loop")
	}
}

// TestFrameStripsControlCharacters pins the F3 fix at the composition choke
// point: an escape-bearing model id in the title, body, modal, and message
// never reaches the frame lines, while tab and multi-byte UTF-8 survive.
func TestFrameStripsControlCharacters(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "Models\x1b]0;pwned\x07",
		mode:  ModeNav,
		body:  []string{"ok\x1b[2J\x00model", "col1\tcol2", "日本語 العربية"},
	})
	app.SetMessage("saved\x1b[31m!")
	app.ShowModal(NewConfirm("Delete?", []string{"m\x1b[2K1"}, nil))

	f := app.Frame()
	joined := strings.Join(f.Lines, "\n")
	if strings.ContainsRune(joined, 0x1b) || strings.ContainsRune(joined, 0x07) || strings.ContainsRune(joined, 0x00) {
		t.Fatalf("frame contains control characters:\n%q", joined)
	}
	for _, want := range []string{"Models]0;pwned", "ok[2Jmodel", "col1\tcol2", "日本語 العربية", "saved[31m!", "m[2K1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frame missing stripped content %q:\n%q", want, joined)
		}
	}
}

// TestFrameStripKeepsCellwidthAlignment pins that stripping is pure
// deletion: the surviving runes keep exactly the cell width they would have
// if the escape bytes had never been present, so pad/truncate layout math
// on the stripped line stays consistent.
func TestFrameStripKeepsCellwidthAlignment(t *testing.T) {
	app := NewApp(80, 20, DirLTR)
	app.Push(&stubScreen{
		title: "T",
		mode:  ModeNav,
		body:  []string{"Width: \x1b[31m日本語\x1b[0m end"},
	})
	f := app.Frame()
	got := f.Lines[2] // title, blank, first body line
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("body line still carries ESC: %q", got)
	}
	want := "Width: [31m日本語[0m end"
	if got != want {
		t.Errorf("stripped line = %q; want %q", got, want)
	}
	if w := cellwidth.String(got); w != cellwidth.String(want) {
		t.Errorf("stripped line cellwidth = %d, want %d", w, cellwidth.String(want))
	}
}
