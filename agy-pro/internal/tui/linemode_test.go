package tui

import (
	"strings"
	"testing"
)

// TestLineModeWordParity pins the mandated word vocabulary: every word the
// degrade matrix promises maps to the same Key the raw console produces.
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
		{"quit", KeyCtrlC},
		{"redraw", KeyCtrlL},
		{"del", KeyDelete},
		{"space", KeySpace},
		{"help", KeyHelp},
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
