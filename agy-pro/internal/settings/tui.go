package settings

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"agypro/internal/providers"
	"agypro/internal/swarm"
)

// Full-screen, keybinding-driven settings TUI (contract §2.9 F-TUI). Layout
// (research §4.1-B): header banner (launcher `===` style) · left numbered
// section list · right detail pane (value + default + source + precedence
// notes) · footer keybinding legend. Numbered selection is the single language
// for CHOICES; navigation is keybindings. NOT a shell menu, NOT stdin line
// menus. Glyph parity with the native Settings Editor overlay stays UNVERIFIED
// (research §3.3 / D3) — evidence-backed glyphs only ([n], ⎿, ●).

// KeySource delivers one key at a time (console raw poll or scripted).
type KeySource interface {
	ReadKey() (Key, bool)
}

// LineReader delivers one text line (for ops that need a name/path).
type LineReader interface {
	ReadLine() (string, bool)
}

type consoleKeys struct{ f *os.File }

func (c consoleKeys) ReadKey() (Key, bool) { return readConsoleKey(c.f) }

type bufioLines struct{ r *bufio.Reader }

func (b bufioLines) ReadLine() (string, bool) {
	line, err := b.r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")), true
}

// TUI is the M2 settings panel state.
type TUI struct {
	core  *Core
	out   io.Writer
	keys  KeySource
	lines LineReader
	// clearScreen requests ANSI home/clear between frames (console only; C3).
	clearScreen bool

	view     SettingsView
	section  int
	row      int
	message  string
	result   []string
	pending  *pendingConfirm
	choices  *choiceState
	running  bool
}

type pendingConfirm struct {
	item Item
	// operands are the prompted values the run's forwarded error may echo
	// (the rollback backup dir, a provider name); they seed the frame mask.
	operands []string
	run      func() error
}

type choiceState struct {
	item    Item
	choices []string
	cursor  int
	after   func(string) error
}

// NewTUI builds a TUI over an already-loaded view.
func NewTUI(core *Core, view SettingsView, keys KeySource, lines LineReader, out io.Writer) *TUI {
	return &TUI{core: core, view: view, keys: keys, lines: lines, out: out, running: true}
}

// SelectSection jumps to a 1-based section number (the --panel deep-link).
func (t *TUI) SelectSection(n int) {
	if n >= 1 && n <= len(t.view.Sections) {
		t.section = n - 1
		t.row = 0
	}
}

// Snapshot is one captured frame (headless capture test source).
type Snapshot struct {
	Banner  string
	Left    string
	Right   string
	Footer  string
	Message string
}

// Frame renders the current state as one full-screen frame.
func (t *TUI) Frame() Snapshot {
	s := Snapshot{}
	s.Banner = strings.Join([]string{
		"===========================================================",
		"  agy-pro Settings — full-screen panel (M2, native-style)",
		"===========================================================",
	}, "\n")

	var left []string
	for i, sec := range t.view.Sections {
		marker := "  "
		if i == t.section {
			marker = "● "
		}
		left = append(left, fmt.Sprintf("%s%s", marker, sec.Title))
	}
	s.Left = strings.Join(left, "\n")

	var right []string
	sec := t.view.Sections[t.section]
	right = append(right, sec.Title)
	for i, it := range sec.Items {
		marker := "  "
		if i == t.row && t.choices == nil {
			marker = "▶ "
		}
		num := fmt.Sprintf("%d", i+1)
		if i < 9 {
			num = " " + num
		}
		dot := " "
		if it.Danger {
			dot = "!"
		}
		right = append(right, fmt.Sprintf(" %s [%s] %s %s %s", marker, num, dot, it.Label, statusDot(it)))
	}
	if t.choices != nil {
		right = append(right, "", "  Choices (numbered selection):")
		for i, ch := range t.choices.choices {
			marker := "  "
			if i == t.choices.cursor {
				marker = "▶ "
			}
			right = append(right, fmt.Sprintf("   %s [%d] %s", marker, i+1, ch))
		}
	} else if t.row < len(sec.Items) {
		it := sec.Items[t.row]
		right = append(right, "")
		right = append(right, fmt.Sprintf("  ⎿ %s", it.Label))
		right = append(right, fmt.Sprintf("     value   : %s", it.Value))
		right = append(right, fmt.Sprintf("     default : %s", emptyDash(it.Default)))
		right = append(right, fmt.Sprintf("     source  : %s", it.Source))
		if it.Note != "" {
			right = append(right, fmt.Sprintf("     note    : %s", it.Note))
		}
		if len(it.Choices) > 0 {
			right = append(right, fmt.Sprintf("     choices : %s (numbered)", strings.Join(it.Choices, " | ")))
		}
		if it.Danger {
			right = append(right, "     danger  : y/n confirmation required")
		}
	}
	if t.pending != nil {
		right = append(right, "", fmt.Sprintf("  Confirm %s? [y/n]", t.pending.item.Label))
	}
	right = append(right, t.result...)
	s.Right = strings.Join(right, "\n")

	s.Message = t.message
	s.Footer = "  ↑↓ move · PgUp/PgDn page · ←→ section · Tab switch · Enter/digits select · y/n confirm · Esc back · Ctrl+L redraw · Ctrl+C exit"
	return s
}

func statusDot(it Item) string {
	if it.ReadOnly {
		return "● ro"
	}
	return "●"
}

// Render writes one frame to the output. ANSI clear only for the interactive
// console path (C3: no ANSI in pipes).
func (t *TUI) Render(clear bool) {
	if clear && t.clearScreen {
		fmt.Fprint(t.out, "\x1b[2J\x1b[H")
	}
	s := t.Frame()
	fmt.Fprintln(t.out, s.Banner)
	fmt.Fprintln(t.out, s.Left)
	fmt.Fprintln(t.out, "  ----")
	fmt.Fprintln(t.out, s.Right)
	if s.Message != "" {
		fmt.Fprintf(t.out, "\n  ⎿ %s\n", s.Message)
	}
	fmt.Fprintln(t.out, s.Footer)
}

// Step processes one key. Returns false when the TUI should exit.
func (t *TUI) Step(k Key) bool {
	switch k {
	case KeyCtrlC:
		return false
	case KeyCtrlL:
		t.message = ""
		t.result = nil
		return true
	}

	if t.pending != nil {
		switch k {
		case KeyYes:
			p := t.pending
			t.pending = nil
			if err := p.run(); err != nil {
				// F6: the forwarded op error is a composite sentence a library
				// call built from raw prompt input (the rollback backup-dir
				// PathError); mask it seeded with the prompted values.
				t.setMaskedError("error: "+err.Error(), p.operands...)
			} else {
				t.message = "[SUCCESS] " + p.item.Label
			}
			t.reload()
		case KeyNo, KeyEsc:
			t.pending = nil
			t.message = "cancelled"
		}
		return true
	}

	if t.choices != nil {
		t.stepChoices(k)
		return true
	}

	sec := t.view.Sections[t.section]
	switch k {
	case KeyUp:
		if t.row > 0 {
			t.row--
		}
	case KeyDown:
		if t.row < len(sec.Items)-1 {
			t.row++
		}
	case KeyPgUp:
		t.row -= 10
		if t.row < 0 {
			t.row = 0
		}
	case KeyPgDn:
		t.row += 10
		if t.row >= len(sec.Items) {
			t.row = len(sec.Items) - 1
		}
	case KeyLeft:
		if t.section > 0 {
			t.section--
			t.row = 0
		}
	case KeyRight, KeyTab:
		if t.section < len(t.view.Sections)-1 {
			t.section++
			t.row = 0
		}
	case KeyEsc:
		if t.message != "" || len(t.result) > 0 {
			t.message = ""
			t.result = nil
		} else {
			return false
		}
	case KeyEnter:
		if t.row < len(sec.Items) {
			t.activate(sec.Items[t.row])
		}
	default:
		// Digit jump: numbered selection for rows inside the panel.
		if idx, ok := digitIndex(k); ok && idx < len(sec.Items) {
			t.row = idx
			t.activate(sec.Items[idx])
		}
	}
	return true
}

func (t *TUI) stepChoices(k Key) {
	cs := t.choices
	switch k {
	case KeyUp:
		if cs.cursor > 0 {
			cs.cursor--
		}
	case KeyDown:
		if cs.cursor < len(cs.choices)-1 {
			cs.cursor++
		}
	case KeyEsc, KeyNo:
		t.choices = nil
		t.message = "cancelled"
	case KeyEnter, KeyTab:
		t.applyChoice(cs.choices[cs.cursor])
	default:
		if idx, ok := digitIndex(k); ok && idx < len(cs.choices) {
			t.applyChoice(cs.choices[idx])
		}
	}
}

func (t *TUI) applyChoice(value string) {
	cs := t.choices
	t.choices = nil
	run := cs.after
	if run == nil {
		return
	}
	if cs.item.Danger {
		t.pending = &pendingConfirm{item: cs.item, operands: []string{value}, run: func() error { return run(value) }}
		return
	}
	if err := run(value); err != nil {
		t.setMaskedError("error: "+err.Error(), value)
	} else {
		t.message = "[SUCCESS] " + cs.item.Label + " = " + value
	}
	t.reload()
}

// activate opens the op behind one item (contract panel map P1-P6).
func (t *TUI) activate(it Item) {
	switch {
	case it.ID == "default_profile" || it.ID == "bridge_tools" || it.ID == "native_seat_concurrency":
		key := it.ID
		t.choices = &choiceState{item: it, choices: it.Choices, after: func(v string) error {
			return t.core.Set(key, v)
		}}
	case it.ID == "current_mode" || it.ID == "mode.switch":
		t.choices = &choiceState{item: it, choices: []string{"full", "clean"}, after: func(v string) error {
			return t.core.ModeSwitch(v)
		}}
	case it.ID == "mode.restore":
		t.pending = &pendingConfirm{item: it, run: t.core.ModeRestore}
	case it.ID == "mode.backups":
		paths, err := t.core.ModeBackups()
		if err != nil {
			t.setMaskedError("error: " + err.Error())
			return
		}
		t.result = nil
		for i, p := range paths {
			t.result = append(t.result, fmt.Sprintf("     ⎿ backup %d: %s", i+1, p))
		}
		if len(paths) == 0 {
			t.result = []string{"     ⎿ (no backups)"}
		}
	case it.ID == "mode.status":
		var b strings.Builder
		if err := t.core.ModeStatus(&b); err != nil {
			t.setMaskedError("error: " + err.Error())
			return
		}
		t.result = splitLines("     ", b.String())
	case it.ID == "providers.add_interactive":
		t.message = "interactive wizard: run `agy pro setup` (or `agy pro settings providers add-interactive`) — raw key echo is masked first-5 + ****"
	case it.ID == "providers.add_json":
		t.runPrompt("JSON file path or - for stdin", func(s string) error {
			return t.addJSON(s)
		})
	case it.ID == "providers.test":
		t.runPrompt("provider name", func(s string) error { return t.core.ProviderTest(s) })
	case it.ID == "providers.rename":
		t.runPrompt("current provider name", func(old string) error {
			_, err := t.promptLine("new provider name", func(neu string) error {
				return t.core.ProviderRename(old, neu)
			})
			return err
		})
	case it.ID == "providers.delete":
		t.runPrompt("provider name to delete (registry entry only)", func(s string) error {
			t.pending = &pendingConfirm{item: it, operands: []string{s}, run: func() error { return t.core.ProviderDelete(s) }}
			return nil
		})
	case it.ID == "doctor.diagnose":
		var b strings.Builder
		err := t.core.DoctorDiagnose(&b, false)
		t.result = splitLines("     ", b.String())
		if err != nil {
			t.setMaskedError("doctor: " + err.Error())
		} else {
			t.message = "[SUCCESS] doctor finished"
		}
	case it.ID == "doctor.fix_keys":
		t.runPrompt("fix-keys target (all | provider list)", func(s string) error {
			// H4: seed per-token forms like the CLI twin (a multi-token target
			// quotes individual tokens in the aggregate error).
			t.pending = &pendingConfirm{item: it, operands: providers.FlagSeedOperands(s), run: func() error { return t.core.DoctorFixKeys(s) }}
			return nil
		})
	case it.ID == "doctor.rollback_keys":
		t.runPrompt("backup dir printed by fix-keys", func(s string) error {
			// F6: the rollback error (the raw os.ReadFile PathError from the
			// repair stack) re-embeds the resolved backup file; seed the frame
			// mask with the prompted dir and the resolved file, matching the
			// CLI rollback tails (cmd/agy E5).
			t.pending = &pendingConfirm{
				item:     it,
				operands: []string{s, providers.ResolveBackupFile(s)},
				run:      func() error { return t.core.DoctorRollbackKeys(s) },
			}
			return nil
		})
	case it.ID == "swarm.presets":
		presets, err := t.core.SwarmPresets()
		if err != nil {
			t.setMaskedError("error: " + err.Error())
			return
		}
		t.result = nil
		for _, p := range presets {
			t.result = append(t.result, fmt.Sprintf("     ⎿ %s = %s", p.Name, p.Numbers))
		}
		if len(presets) == 0 {
			t.result = []string{"     ⎿ (no saved presets; p1/p2/p3 defaults always available)"}
		}
	case it.ID == "swarm.save_preset":
		t.runPrompt("preset name", func(name string) error {
			_, err := t.promptLine("numbers (e.g. 1,3,19,23)", func(nums string) error {
				presets, err := t.core.SwarmPresets()
				if err != nil {
					return err
				}
				presets = append(presets, swarm.SavedPreset{Name: name, Numbers: nums})
				return t.core.SwarmSavePresets(presets)
			})
			return err
		})
	case it.ReadOnly:
		t.message = "read-only: " + it.Note
	default:
		t.message = it.Label + ": " + it.Note
	}
}

// runPrompt reads one text line, runs fn, and records the outcome message.
// When fn only arms the danger y/n gate (delete / fix-keys / rollback), the
// message is "confirm to proceed" — [SUCCESS] is stamped only by the confirm
// runner once the gate resolves (B8). A failing fn's error is masked seeded
// with the prompted value (F6): the error may be a composite the library built
// from that value.
func (t *TUI) runPrompt(prompt string, fn func(string) error) {
	input, err := t.promptLine(prompt, fn)
	switch {
	case err != nil:
		t.setMaskedError("error: "+err.Error(), input)
	case t.pending != nil:
		t.message = "confirm to proceed — " + prompt
	default:
		t.message = "[SUCCESS] " + prompt
	}
	t.reload()
}

// promptLine reads one text line and runs fn with it (rename/delete/etc).
// Returns the prompted value together with fn's outcome so callers can seed
// the frame mask with it; a failing fn's error is already masked seeded with
// the value it was handed.
func (t *TUI) promptLine(prompt string, fn func(string) error) (string, error) {
	if t.lines == nil {
		t.message = "text input unavailable (non-interactive); use the matching `agy pro settings` verb"
		return "", nil
	}
	fmt.Fprintf(t.out, "  %s: ", prompt)
	s, ok := t.lines.ReadLine()
	if !ok {
		return s, fmt.Errorf("input closed")
	}
	if err := fn(s); err != nil {
		// F6: a composite error (a PathError embedding the JSON path, a
		// rename conflict echoing the new name) must not carry a pasted key
		// into the frame; benign values stay readable.
		return s, errors.New(providers.MaskSentence(err.Error(), s))
	}
	return s, nil
}

// setMaskedError records an error message for the next frame. Forwarded
// core/doctor errors are composite sentences a library call built from raw
// prompt input (the rollback backup-dir PathError, a rename conflict), so
// every prompted operand in scope seeds providers.MaskSentence; with no
// eligible seed the sentence still passes the generic credential patterns and
// benign values stay readable (F6 / U8).
func (t *TUI) setMaskedError(msg string, operands ...string) {
	t.message = providers.MaskSentence(msg, operands...)
}

func (t *TUI) addJSON(path string) error {
	data, err := readSpecSource(path, t.lines)
	if err != nil {
		return err
	}
	spec, err := ParseProviderSpecJSON(data)
	if err != nil {
		return err
	}
	return t.core.ProviderAdd(spec)
}

// reload refreshes the view snapshot after a write verb.
func (t *TUI) reload() {
	view, err := t.core.View()
	if err == nil {
		t.view = view
		if t.section >= len(t.view.Sections) {
			t.section = 0
		}
		if sec := t.view.Sections[t.section]; t.row >= len(sec.Items) {
			t.row = 0
		}
	}
}

// Run drives the loop until exit. Returns the process exit code.
func (t *TUI) Run() int {
	t.Render(true)
	for t.running {
		k, ok := t.keys.ReadKey()
		if !ok {
			break
		}
		if !t.Step(k) {
			break
		}
		t.Render(true)
	}
	return 0
}

func splitLines(indent, s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, indent+ln)
	}
	return out
}
