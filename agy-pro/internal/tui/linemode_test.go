package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"agypro/internal/settings"
)

// TestLineModeWordParity pins the mandated word vocabulary: every word the
// degrade matrix promises maps to the same Key the raw console produces. The
// `help` / `?` gestures are the one exception (they answer inside the reader
// and produce no key at all), so TestLineModeHelpBlock pins them.
func TestLineModeWordParity(t *testing.T) {
	cases := []struct {
		word string
		want Key
	}{
		{"up", KeyUp},
		{"down", KeyDown},
		{"left", KeyLeft},
		{"right", KeyRight},
		{"pgup", KeyPgUp},
		{"pgdn", KeyPgDn},
		{"home", KeyHome},
		{"end", KeyEnd},
		{"enter", KeyEnter},
		{"esc", KeyEsc},
		{"y", KeyYes},
		{"n", KeyNo},
		{"r", Key("r")},
		{"quit", KeyCtrlC},
		{"redraw", KeyCtrlL},
		{"del", KeyDelete},
		{"space", KeySpace},
	}
	for _, c := range cases {
		got, ok := mapLineWord(c.word)
		if !ok || got != c.want {
			t.Errorf("mapLineWord(%q) = %q,%v; want %q,true", c.word, got, ok, c.want)
		}
	}
	for n := 1; n <= 9; n++ {
		got, ok := mapLineWord(string(rune('0' + n)))
		if !ok || got != Digit(n) {
			t.Errorf("mapLineWord(%d) = %q,%v; want %q,true", n, got, ok, Digit(n))
		}
	}
}

// TestLineModeHelpBlock pins the P6-2 line-mode half: the `help` gesture prints
// one bounded block naming the vocabulary and produces NO key at all, so no
// screen has to consume it. It also pins the block against drift: every word it
// prints is a word mapLineWord accepts (or, for the two gestures, one it must
// keep refusing, because the reader answers those before the parser sees them),
// and the block stays inside three lines so a piped transcript is not flooded.
func TestLineModeHelpBlock(t *testing.T) {
	// Drift halves, cheapest first: the printed word lists resolve through the
	// parser, the gesture words do not.
	for _, w := range lineModeWords {
		if _, ok := mapLineWord(w); !ok {
			t.Errorf("printed word %q is not accepted by mapLineWord", w)
		}
	}
	for _, w := range lineModeAliasWords {
		if _, ok := mapLineWord(w); !ok {
			t.Errorf("printed alias %q is not accepted by mapLineWord", w)
		}
	}
	for _, w := range lineModeGestureWords {
		if _, ok := mapLineWord(w); ok {
			t.Errorf("gesture %q still maps to a key; the reader owns it", w)
		}
	}

	// The gesture prints and the read goes on: the next line is the next key.
	for _, gesture := range []string{"help", "?", "HELP"} {
		var out strings.Builder
		lk := NewLineKeys(strings.NewReader(gesture + "\ndown\n")).WithHelpOut(&out)
		k, ok := lk.ReadKey()
		if !ok || k != KeyDown {
			t.Fatalf("%q: next key = %q,%v; want down,true (the gesture produces no key)", gesture, k, ok)
		}
		block := out.String()
		if block == "" {
			t.Fatalf("%q printed nothing; the gesture must not be a silent no-op", gesture)
		}
		for _, w := range append(append(append([]string(nil),
			lineModeWords...), lineModeAliasWords...), lineModeGestureWords...) {
			if !strings.Contains(block, w) {
				t.Errorf("%q: the block omits the word %q:\n%s", gesture, w, block)
			}
		}
		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		if len(lines) > 4 {
			t.Errorf("%q: block is %d lines, want at most 4:\n%s", gesture, len(lines), block)
		}
		for _, ln := range lines {
			if len(ln) > 100 {
				t.Errorf("%q: block line is %d cells, want at most 100: %q", gesture, len(ln), ln)
			}
		}
	}

	// A bare caller (no writer wired) still consumes the word instead of
	// handing a dead key to a screen.
	var plain strings.Builder
	lk := NewLineKeys(strings.NewReader("?\nup\n")).WithHelpOut(&plain)
	if k, ok := lk.ReadKey(); !ok || k != KeyUp {
		t.Fatalf("bare reader: key = %q,%v; want up,true", k, ok)
	}
	// The gesture at EOF ends the stream cleanly instead of looping.
	lk = NewLineKeys(strings.NewReader("help"))
	if _, ok := lk.ReadKey(); ok {
		t.Fatal("a trailing help at EOF produced a key")
	}

	// runSetup wires the session's output stream, so the block is reachable in
	// production and not only in this test. The shell binds the stored UI
	// language through SetLanguage while it assembles, so this call needs an
	// isolated home: without one it reads the operator's live
	// ~/.agy-pro/state.json and leaves that language in the package-level
	// bundle, which made every later test that asserts English catalog text
	// (dashboard, doctor, help, manual) fail in a full run.
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	var framed strings.Builder
	if code := runSetup(strings.NewReader("help\nq\n"), &framed, &strings.Builder{}, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit code = %d; want 0", code)
	}
	if !strings.Contains(framed.String(), "line mode words:") {
		t.Fatalf("runSetup did not print the help block; output:\n%s", framed.String())
	}
}

func TestLineModeReader(t *testing.T) {
	lk := NewLineKeys(strings.NewReader("up\nbogus\ndown\n"))
	if k, ok := lk.ReadKey(); !ok || k != KeyUp {
		t.Fatalf("first key = %q,%v; want up,true", k, ok)
	}
	// Unknown words are skipped, not fatal.
	if k, ok := lk.ReadKey(); !ok || k != KeyDown {
		t.Fatalf("second key = %q,%v; want down,true", k, ok)
	}
	if _, ok := lk.ReadKey(); ok {
		t.Fatal("stream not exhausted after the script")
	}

	// Case-insensitive, empty line means Enter, no trailing newline works.
	lk = NewLineKeys(strings.NewReader("HOME\n\nY"))
	if k, _ := lk.ReadKey(); k != KeyHome {
		t.Fatalf("HOME = %q, want home", k)
	}
	if k, _ := lk.ReadKey(); k != KeyEnter {
		t.Fatalf("empty line = %q, want enter", k)
	}
	if k, ok := lk.ReadKey(); !ok || k != KeyYes {
		t.Fatalf("unterminated Y = %q,%v; want y,true", k, ok)
	}

	// The doctor refresh spelling survives the reader lower-casing: the
	// screen accepts the bare "r" key, so "R" must arrive as that key.
	lk = NewLineKeys(strings.NewReader("R\n"))
	if k, ok := lk.ReadKey(); !ok || k != Key("r") {
		t.Fatalf("R = %q,%v; want the bare r key,true", k, ok)
	}
}

func TestLineModeDeliversPresetDeleteWord(t *testing.T) {
	for _, line := range []string{"d\n", "D\n"} {
		lk := NewLineKeys(strings.NewReader(line))
		k, ok := lk.ReadKey()
		if !ok || k != Key("d") {
			t.Errorf("line %q delivered key %q, %v; want d, true", line, k, ok)
		}
	}
}

// TestLineModeDeliversDoctorRefresh pins the reachability half of the r
// mapping: on a host that reads keys as text lines (pipes, non-Windows TTYs,
// mintty) the doctor screen's refresh gesture is the only way to re-run the
// diagnose seam, and before the mapping existed a piped "r" was skipped as an
// unknown word.
func TestLineModeDeliversDoctorRefresh(t *testing.T) {
	doctorTestEnv(t)
	calls := 0
	core := settings.NewCore(settings.Deps{
		Doctor: func(out io.Writer, strict bool) error {
			calls++
			fmt.Fprintf(out, "DIAG-RUN-%d", calls)
			return nil
		},
	})
	app := NewApp(80, 24, DirLTR)
	app.Push(NewDoctorScreen(core))

	app.CaptureRun(NewLineKeys(strings.NewReader("r\n")))
	if calls != 2 {
		t.Fatalf("diagnose ran %d times, want 2 (construction snapshot + line-mode r)", calls)
	}
	app.Step(KeyTab)
	if frame := app.Frame().String(); !strings.Contains(frame, "DIAG-RUN-2") {
		t.Fatalf("Doctor pane did not show the refreshed run; frames:\n%s", frame)
	}
}

func TestLineReaderAndPromptLine(t *testing.T) {
	var out strings.Builder
	lr := NewLineReader(strings.NewReader("hello world\n"))
	got, ok := PromptLine(&out, lr, "Name")
	if !ok || got != "hello world" {
		t.Fatalf("PromptLine = %q,%v; want hello world,true", got, ok)
	}
	if out.String() != "Name: " {
		t.Fatalf("prompt = %q, want %q", out.String(), "Name: ")
	}

	// The scripted seam satisfies the same interface.
	sl := &ScriptedLines{Lines: []string{"typed"}}
	got, ok = PromptLine(&out, sl, "Again")
	if !ok || got != "typed" {
		t.Fatalf("scripted PromptLine = %q,%v; want typed,true", got, ok)
	}
	if _, ok := sl.ReadLine(); ok {
		t.Fatal("scripted lines not exhausted")
	}
}

func TestScriptedKeysExhaustion(t *testing.T) {
	sk := &ScriptedKeys{Keys: []Key{KeyUp, Digit(2)}}
	if k, ok := sk.ReadKey(); !ok || k != KeyUp {
		t.Fatalf("first = %q,%v", k, ok)
	}
	if k, ok := sk.ReadKey(); !ok || k != Digit(2) {
		t.Fatalf("second = %q,%v", k, ok)
	}
	if _, ok := sk.ReadKey(); ok {
		t.Fatal("scripted keys not exhausted")
	}
}
