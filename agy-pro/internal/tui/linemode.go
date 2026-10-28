package tui

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// LineKeys is the non-console KeySource (pipes, non-Windows TTYs, mintty):
// one text line per key, mapped to the same Key set as the raw console
// reader, so the screen tree and key semantics are identical on every host
// (degrade matrix). The mandated word vocabulary is lineModeWords:
//
//	up down left right pgup pgdn home end enter esc y n r d quit redraw
//	del space
//
// (`r` is the doctor screen's refresh gesture and `d` is the presets screen's
// delete/reset gesture; without those spellings the gestures are unreachable
// on every host that reads keys as text lines.)
//
// plus the legacy settings-panel aliases in lineModeAliasWords (k/j, back,
// yes/no, q/exit, delete, ctrl-c, ctrl-l, save) and an empty line for Enter.
// `ctrl-a` rides the alias list with the other ctrl- spellings (U8: the
// built-in preset view's materialize gesture stays reachable on a pipe).
// `help` and `?` are reader-level gestures, not keys: they print
// lineModeHelpText and the read goes on. The word used to map to a KeyHelp
// that no screen consumed, so typing it in line mode was a silent no-op
// (SESSION6 P6-2). Unknown words are skipped, so one typo in a piped script
// cannot kill the session; EOF or a closed line ends the key stream.
type LineKeys struct {
	r       *bufio.Reader
	helpOut io.Writer
}

// lineModeWords is the mandated vocabulary in print order, one gesture key per
// word. mapLineWord owns the mapping and the `help` block prints this exact
// slice, so the printed list and the parser cannot drift (the drift pin is
// TestLineModeHelpBlock).
var lineModeWords = []string{
	"up", "down", "left", "right", "pgup", "pgdn", "home", "end",
	"enter", "esc", "y", "n", "r", "d", "quit", "redraw", "del", "space",
}

// lineModeAliasWords is the legacy spelling each key also accepts, listed for
// the `help` block: k/j are the settings-panel up/down aliases, back is Esc,
// q and exit are Ctrl+C spellings, delete is the long form of del, save is
// Ctrl+S, and ctrl-a is the built-in preset view's copy-to-new-preset gesture
// (U8) spelled for pipes.
var lineModeAliasWords = []string{
	"k", "j", "back", "yes", "no", "q", "exit", "delete", "ctrl-c", "ctrl-l", "save", "ctrl-a",
}

// lineModeGestureWords answer inside the reader instead of producing a key.
var lineModeGestureWords = []string{"help", "?"}

// lineModeHelpText is the one bounded block the `help` gesture prints: four
// short lines naming every word the reader accepts, plus the line-level rules a
// script author cannot read off the key vocabulary alone (digits select a
// numbered row, an empty line is Enter, and text fields take a whole line
// instead of a key word).
func lineModeHelpText() string {
	return "line mode words: " + strings.Join(lineModeWords, " ") +
		"\nline mode aliases: " + strings.Join(lineModeAliasWords, " ") +
		"\nline mode gestures: " + strings.Join(lineModeGestureWords, " ") + " (prints this block)" +
		"\ndigits 1-9 pick a numbered row; an empty line is Enter; text fields read a whole line"
}

// WithHelpOut returns l with the writer the `help` gesture prints to. The
// session owns the output stream, so runSetup wires it at construction; a bare
// caller leaves it nil and the gesture stays silent rather than writing
// somewhere the operator cannot see.
func (l *LineKeys) WithHelpOut(w io.Writer) *LineKeys {
	l.helpOut = w
	return l
}

// isLineHelpWord reports whether tok is a reader-level gesture rather than a
// key word.
func isLineHelpWord(tok string) bool {
	for _, w := range lineModeGestureWords {
		if tok == w {
			return true
		}
	}
	return false
}

// NewLineKeys builds a line-mode key source over r.
func NewLineKeys(r io.Reader) *LineKeys {
	return &LineKeys{r: bufio.NewReader(r)}
}

// ReadKey returns the next mapped key, skipping unmapped lines. A final
// empty read at EOF ends the stream (an empty LINE, with its newline, is
// Enter). The `help` gesture is answered here and produces no key at all: the
// block goes to the help writer and the read goes on, so the word does
// something on every screen instead of being a silent no-op. Line-mode keys
// are deliberately NOT marked as console-paced: the gate arm delay exists for
// paste remainders in the raw console stream, and a typed text line is a
// deliberate answer, so y / yes / n / no answer a gate immediately, exactly as
// before the delay existed.
func (l *LineKeys) ReadKey() (Key, bool) {
	for {
		line, err := l.r.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", false
		}
		tok := strings.ToLower(strings.TrimSpace(line))
		if isLineHelpWord(tok) {
			if l.helpOut != nil {
				fmt.Fprintln(l.helpOut, lineModeHelpText())
			}
			if err != nil {
				return "", false
			}
			continue
		}
		if k, ok := mapLineWord(tok); ok {
			return k, true
		}
		if err != nil {
			return "", false
		}
	}
}

// mapLineWord maps one trimmed lower-case line to a key.
func mapLineWord(tok string) (Key, bool) {
	switch tok {
	case "":
		return KeyEnter, true
	case "up", "k":
		return KeyUp, true
	case "down", "j":
		return KeyDown, true
	case "left":
		return KeyLeft, true
	case "right":
		return KeyRight, true
	case "pgup":
		return KeyPgUp, true
	case "pgdn":
		return KeyPgDn, true
	case "home":
		return KeyHome, true
	case "end":
		return KeyEnd, true
	case "tab":
		return KeyTab, true
	case "esc", "back":
		return KeyEsc, true
	case "enter":
		return KeyEnter, true
	case "del", "delete":
		return KeyDelete, true
	case "space":
		return KeySpace, true
	case "y", "yes":
		return KeyYes, true
	case "n", "no":
		return KeyNo, true
	case "r":
		return Key("r"), true
	case "d":
		return Key("d"), true
	case "ctrl-c", "quit", "exit", "q":
		return KeyCtrlC, true
	case "ctrl-l", "redraw":
		return KeyCtrlL, true
	case "ctrl-s", "save":
		return KeyCtrlS, true
	case "ctrl-a":
		return KeyCtrlA, true
	}
	// `help` and `?` are deliberately absent: they are reader-level gestures
	// (lineModeGestureWords), answered by LineKeys.ReadKey with a printed word
	// list, so no screen is handed a help key it would have to ignore.
	if len(tok) == 1 && tok[0] >= '0' && tok[0] <= '9' {
		return Key(tok[0]), true
	}
	return "", false
}

// LineReader delivers one whole text line. It is the EDIT-mode degrade
// seam off-console: where the raw console delivers rune keys, a pipe gets
// whole-line prompts through PromptLine.
type LineReader interface {
	ReadLine() (string, bool)
}

type bufioLines struct {
	r *bufio.Reader
}

func (b *bufioLines) ReadLine() (string, bool) {
	line, err := b.r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")), true
}

// NewLineReader builds a whole-line reader over r (the scripted
// ScriptedLines satisfies the same interface in tests).
func NewLineReader(r io.Reader) LineReader {
	return &bufioLines{r: bufio.NewReader(r)}
}

// PromptLine prints one "prompt: " line and reads the answer (the EDIT
// off-console degrade for one-shot text input).
func PromptLine(out io.Writer, lr LineReader, prompt string) (string, bool) {
	fmt.Fprintf(out, "%s: ", prompt)
	return lr.ReadLine()
}
