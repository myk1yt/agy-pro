package providers

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Masked secret entry (U8, contract §2.11): the wizard's raw-key input echo
// shows the first 5 typed runes and "*" for every later rune, and every
// confirmation prints only MaskSecret output. The raw value is written to
// nothing — not the transcript, not logs, not error text.

// errSecretInterrupted is returned when the user aborts masked key entry with
// Ctrl+C (B6). The caller must abort: it must NOT fall through to the
// empty-value default path and silently keep an existing key.
var errSecretInterrupted = errors.New("secret entry aborted (Ctrl+C)")

// errNotConsole is the internal fallback signal from the platform console
// reader: no raw console is available, use the plain line read instead.
var errNotConsole = errors.New("secret input: not a console")

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

// readConsoleSecretFn is the platform console reader seam; B6 tests stub it to
// drive the interrupt path without a live console.
var readConsoleSecretFn = readConsoleSecret

// promptSecret reads an API-key line with masked echo. When console is a real
// Windows console, echo is taken over by secretEcho (first 5 runes verbatim,
// "*" afterwards). Otherwise the line is read plainly (pipes and test scripts
// have no terminal echo) and the raw value is never written to w.
//
// Returns (defaultValue, nil) on empty input — the historical empty-value
// default path (Enter keeps the existing key). Returns errSecretInterrupted
// when the user presses Ctrl+C during masked entry (B6): the caller must abort
// and must NOT treat the empty result as "keep the existing key".
func promptSecret(r *bufio.Reader, w io.Writer, console *os.File, prompt, defaultValue string) (string, error) {
	if defaultValue != "" && !strings.Contains(prompt, "[") {
		fmt.Fprintf(w, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(w, "%s: ", prompt)
	}

	if r.Buffered() == 0 {
		echo := newSecretEcho(w)
		val, err := readConsoleSecretFn(console, echo)
		switch {
		case err == nil:
			val = strings.TrimSpace(val)
			if val == "" {
				return defaultValue, nil
			}
			return strings.TrimPrefix(val, "\ufeff"), nil
		case errors.Is(err, errSecretInterrupted):
			return "", errSecretInterrupted
		}
		// errNotConsole: fall through to the plain line read below.
	}

	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return defaultValue, nil
	}
	line = strings.TrimPrefix(line, "\ufeff")
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}
