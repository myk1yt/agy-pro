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
// (degrade matrix). The mandated word vocabulary is:
//
//	up down left right pgup pgdn home end enter esc y n 1-9 quit redraw
//	del space help
//
// plus the legacy settings-panel aliases (k/j, back, yes/no, q/exit,
// ctrl-c, ctrl-l, tab) and an empty line for Enter. Unknown words are
// skipped, so one typo in a piped script cannot kill the session; EOF or a
// closed line ends the key stream.
type LineKeys struct {
	r *bufio.Reader
}

// NewLineKeys builds a line-mode key source over r.
func NewLineKeys(r io.Reader) *LineKeys {
	return &LineKeys{r: bufio.NewReader(r)}
}

// ReadKey returns the next mapped key, skipping unmapped lines. A final
// empty read at EOF ends the stream (an empty LINE, with its newline, is
// Enter).
func (l *LineKeys) ReadKey() (Key, bool) {
	for {
		line, err := l.r.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return "", false
		}
		if k, ok := mapLineWord(strings.ToLower(strings.TrimSpace(line))); ok {
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
	case "ctrl-c", "quit", "exit", "q":
		return KeyCtrlC, true
	case "ctrl-l", "redraw":
		return KeyCtrlL, true
	case "ctrl-s", "save":
		return KeyCtrlS, true
	case "help", "?":
		return KeyHelp, true
	}
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
