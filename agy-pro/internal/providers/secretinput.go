package providers

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Masked secret entry (U8, contract §2.11): the wizard's raw-key input echo
// shows the first 5 typed runes and "*" for every later rune, and every
// confirmation prints only MaskSecret output. The raw value is written to
// nothing — not the transcript, not logs, not error text.

// secretEcho renders the U8 mask while the user types. The first 5 runes echo
// verbatim; every rune after that echoes as a single '*'. backspace erases one
// echoed cell. The full raw value is kept only for MaskSecret() at confirm
// time and is never written through the writer.
type secretEcho struct {
	w     io.Writer
	typed []rune
}

func newSecretEcho(w io.Writer) *secretEcho { return &secretEcho{w: w} }

func (e *secretEcho) add(r rune) {
	e.typed = append(e.typed, r)
	if len(e.typed) <= 5 {
		fmt.Fprint(e.w, string(r))
		return
	}
	fmt.Fprint(e.w, "*")
}

func (e *secretEcho) backspace() {
	if n := len(e.typed); n > 0 {
		e.typed = e.typed[:n-1]
		fmt.Fprint(e.w, "\b \b")
	}
}

func (e *secretEcho) newline() { fmt.Fprintln(e.w) }

// masked is the canonical first-5 + "****" rendering of what was typed.
func (e *secretEcho) masked() string { return MaskSecret(string(e.typed)) }

// promptSecret reads an API-key line with masked echo. When console is a real
// Windows console, echo is taken over by secretEcho (first 5 runes verbatim,
// "*" afterwards). Otherwise the line is read plainly (pipes and test scripts
// have no terminal echo) and the raw value is never written to w.
func promptSecret(r *bufio.Reader, w io.Writer, console *os.File, prompt, defaultValue string) (string, bool) {
	if defaultValue != "" && !strings.Contains(prompt, "[") {
		fmt.Fprintf(w, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(w, "%s: ", prompt)
	}

	if r.Buffered() == 0 {
		echo := newSecretEcho(w)
		if val, ok := readConsoleSecret(console, echo); ok {
			val = strings.TrimSpace(val)
			if val == "" {
				return defaultValue, false
			}
			return strings.TrimPrefix(val, "\ufeff"), false
		}
	}

	line, err := r.ReadString('\n')
	isEOF := err == io.EOF || (err != nil && len(line) == 0)
	line = strings.TrimPrefix(line, "\ufeff")
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, isEOF
	}
	return line, isEOF
}
