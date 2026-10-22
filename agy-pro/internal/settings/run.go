package settings

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// RunTUI is the M2 entry for `agy pro settings` (contract §2.6/§2.9):
// no args → full-screen keybinding-driven TUI; non-TTY / non-console degrades
// to the same panel driven by line-mode keys (reusing the same Core verbs) with
// the `--show` snapshot printed first; ANSI stripped when not a console (C3).
// startPanel deep-links a panel by 1-based number.
func RunTUI(core *Core, in io.Reader, out io.Writer, startPanel string) int {
	view, err := core.View()
	if err != nil {
		fmt.Fprintf(out, "[agy-pro] settings view failed: %v\n", err)
		return 1
	}

	file, isFile := in.(*os.File)
	isTTY := isFile && isTerminalFile(file)
	// Raw console keys exist on Windows only (console_other.go:9-11 returns no
	// keys). On a non-Windows interactive TTY the old check still chose the
	// raw-console path, which rendered one frame and exited instead of the
	// documented line-mode degrade (B7). Degrade whenever the build cannot
	// serve raw keys — line-mode works over a TTY line discipline too.
	interactive := useRawConsoleKeys(isTTY)

	var keys KeySource
	var lines LineReader
	if interactive {
		keys = consoleKeys{f: file}
		lines = bufioLines{r: bufio.NewReader(file)}
	} else {
		// Honest degrade (contract §2.9): line-mode keys over the same panel
		// and Core verbs. Print the snapshot first so a non-interactive pipe
		// still gets the full surface without driving a loop.
		if !isFile {
			_ = WriteSnapshot(out, core, false)
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "Line-mode fallback: type navigation words (up, down, left, right, enter,")
			fmt.Fprintln(out, "esc, y, n, 1-9) or `quit`. `agy pro settings show` prints this snapshot alone.")
		}
		r := bufio.NewReader(in)
		keys = &lineModeKeys{r: r}
		lines = bufioLines{r: r}
	}

	tui := NewTUI(core, view, keys, lines, out)
	tui.clearScreen = interactive
	if startPanel != "" {
		var n int
		if _, err := fmt.Sscanf(startPanel, "%d", &n); err == nil {
			tui.SelectSection(n)
		} else if sec, ok := view.SectionByID(startPanel); ok {
			for i, s := range view.Sections {
				if s.ID == sec.ID {
					tui.SelectSection(i + 1)
				}
			}
		}
	}
	return tui.Run()
}

// lineModeKeys is the non-console KeySource: one text line per key, mapped to
// the same Key set (contract §2.9 line-mode degrade).
type lineModeKeys struct{ r *bufio.Reader }

func (l *lineModeKeys) ReadKey() (Key, bool) {
	line, err := l.r.ReadString('\n')
	if err != nil && len(strings.TrimSpace(line)) == 0 {
		return "", false
	}
	tok := strings.ToLower(strings.TrimSpace(line))
	switch tok {
	case "":
		return KeyEnter, true
	case "up", "k":
		return KeyUp, true
	case "down", "j":
		return KeyDown, true
	case "pgup":
		return KeyPgUp, true
	case "pgdn":
		return KeyPgDn, true
	case "left":
		return KeyLeft, true
	case "right":
		return KeyRight, true
	case "tab":
		return KeyTab, true
	case "esc", "back":
		return KeyEsc, true
	case "enter":
		return KeyEnter, true
	case "y", "yes":
		return KeyYes, true
	case "n", "no":
		return KeyNo, true
	case "ctrl-c", "quit", "exit", "q":
		return KeyCtrlC, true
	case "ctrl-l", "redraw":
		return KeyCtrlL, true
	}
	if len(tok) == 1 && tok[0] >= '0' && tok[0] <= '9' {
		return Key(tok[0]), true
	}
	return "", false
}

// WriteSnapshot renders the settings view as the non-interactive snapshot
// (`agy pro settings show`). ANSI-free for pipes (C3).
func WriteSnapshot(out io.Writer, core *Core, asJSON bool) error {
	view, err := core.View()
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(view)
	}
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Settings Snapshot (show)")
	fmt.Fprintln(out, "===========================================================")
	// P1-4: the row flags are explained before the first section, so "[ro]"
	// is a documented marker on the printed surface, not an undocumented
	// suffix.
	fmt.Fprintln(out, "  Flags: ["+ReadOnlyMarker+"] = read-only (display only; pressing the row changes nothing) · [danger] = writes ask y/n first")
	for _, sec := range view.Sections {
		fmt.Fprintf(out, "%s\n", sec.Title)
		for i, it := range sec.Items {
			num := fmt.Sprintf("%d", i+1)
			flags := ""
			if it.ReadOnly || it.HubOnly {
				flags += " [" + ReadOnlyMarker + "]"
			}
			if it.Danger {
				flags += " [danger]"
			}
			fmt.Fprintf(out, "  [%s] %s = %s%s\n", num, it.Label, it.Value, flags)
			if it.Note != "" {
				fmt.Fprintf(out, "        note: %s\n", it.Note)
			}
		}
		fmt.Fprintln(out, "")
	}
	fmt.Fprintln(out, "All secret values render as first-5 + **** (no suffix).")
	return nil
}

// isTerminalFile reports whether f is attached to a character device.
func isTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// wantRawConsole is the pure degrade rule (B7): raw console keys are used only
// when the input is a TTY AND the build implements a raw console key reader.
// A non-Windows TTY degrades to the documented line-mode UI instead of the
// one-frame-and-exit stub that readConsoleKey's empty answer produced.
func wantRawConsole(isTTY, rawConsoleOK bool) bool {
	return isTTY && rawConsoleOK
}

// useRawConsoleKeys applies wantRawConsole to a RunTUI input file, with the
// build's raw-console capability (rawConsoleKeysSupported: true on Windows,
// false elsewhere — console_other.go returns no keys).
func useRawConsoleKeys(isTTY bool) bool {
	return wantRawConsole(isTTY, rawConsoleKeysSupported)
}

// ScriptedKeys feeds a fixed key sequence (headless capture tests / D3).
type ScriptedKeys struct {
	Keys []Key
	pos  int
}

// ReadKey returns the next scripted key.
func (s *ScriptedKeys) ReadKey() (Key, bool) {
	if s.pos >= len(s.Keys) {
		return "", false
	}
	k := s.Keys[s.pos]
	s.pos++
	return k, true
}

// ScriptedLines feeds fixed text lines to TUI prompts.
type ScriptedLines struct {
	Lines []string
	pos   int
}

// ReadLine returns the next scripted line.
func (s *ScriptedLines) ReadLine() (string, bool) {
	if s.pos >= len(s.Lines) {
		return "", false
	}
	l := s.Lines[s.pos]
	s.pos++
	return l, true
}

// Renderable is a tiny helper for tests: capture a full run's frames.
func captureRun(tui *TUI) string {
	var b strings.Builder
	tui.out = &b
	tui.Render(false)
	for {
		k, ok := tui.keys.ReadKey()
		if !ok {
			break
		}
		if !tui.Step(k) {
			break
		}
		tui.Render(false)
	}
	return b.String()
}
