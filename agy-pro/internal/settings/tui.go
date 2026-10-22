package settings

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"agypro/internal/i18n"
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

	view    SettingsView
	section int
	row     int
	message string
	result  []string
	pending *pendingConfirm
	choices *choiceState
	running bool
}

type pendingConfirm struct {
	item Item
	// operands are the prompted values the run's forwarded error may echo
	// (the rollback backup dir, a provider name); they seed the frame mask.
	operands []string
	// notice is an optional extra line under the "Confirm ...? [y/n]" prompt:
	// the reason the gate opened when the bare item label cannot say it (the
	// corrupt swarm-state.json acknowledgment, which must name the file the
	// save would overwrite).
	notice string
	// body is the exact diff the gate presents before it asks (P5-1 U5a, user
	// decision F3(a)): the pre-write lines composed by paneldiff.go, rendered
	// ABOVE the prompt so the approval describes the write it authorizes. The
	// panel renders them into the right pane, the same path notice takes, so a
	// line-mode frame and an interactive frame show the same bytes.
	body []string
	run  func() error
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
	Banner string
	// Notice is the deprecation banner of the legacy panel (P1-7=(a)); it rides
	// above the section list so the first frame of every entry path carries it.
	Notice  string
	Left    string
	Right   string
	Footer  string
	Message string
}

// LegacyPanelNotice is the one notice the legacy `agy pro settings` panel
// renders (P1-7=(a), user decision "deprecate the legacy panel and route to the
// hub"). Three claims, all true today: the panel still works exactly as before,
// it is deprecated, and new setting work lands in the setup hub. The second line
// carries the EXACT command that opens the hub, so the notice is actionable
// where it appears instead of pointing at a surface the reader has to find.
//
// The wording is a package const rather than an i18n catalog entry on purpose:
// the twelve catalogs describe the hub's own surfaces, and a key added here
// would need every catalog to follow in lockstep. Nothing in this text is
// registry- or remote-derived, so no masker applies (nothing to mask).
//
// Catalog boundary (S7-C7 convention, decisions.md 2026-10-22 S7-C7 entry):
// the notice stays a package const, deliberately outside the twelve catalogs.
// Revisit when the P5 unified settings surface exists.
const LegacyPanelNotice = "  DEPRECATED: this legacy panel still works exactly as today, but it is deprecated; new setting work lands in the setup hub.\n" +
	"  Open the hub: agy pro setup   (full-screen setup TUI; `agy pro setup --wizard` runs the line wizard)"

// Frame renders the current state as one full-screen frame.
func (t *TUI) Frame() Snapshot {
	s := Snapshot{}
	s.Banner = strings.Join([]string{
		"===========================================================",
		"  agy-pro Settings - full-screen panel (M2, native-style)",
		"===========================================================",
	}, "\n")
	s.Notice = LegacyPanelNotice

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
		right = append(right, "")
		// P5-1 U5a: the exact diff the answer authorizes, above the prompt. The
		// body lines are already control-stripped and masked at composition
		// (paneldiff.go), so the frame renders them verbatim: the diff the
		// operator approves is the diff that was composed from disk.
		for _, ln := range t.pending.body {
			right = append(right, "    "+ln)
		}
		if len(t.pending.body) > 0 {
			right = append(right, "")
		}
		right = append(right, fmt.Sprintf("  Confirm %s? [y/n]", t.pending.item.Label))
		if t.pending.notice != "" {
			right = append(right, "  "+t.pending.notice)
		}
	}
	right = append(right, t.result...)
	s.Right = strings.Join(right, "\n")

	s.Message = t.message
	// P1-4: the read-only legend rides above the keybinding line, so the
	// "● ro" dot every read-only row carries is explained on the first
	// frame, before any press.
	s.Footer = "  " + ReadOnlyLegend + "\n  ↑↓ move · PgUp/PgDn page · ←→ section · Tab switch · Enter/digits select · y/n confirm · Esc back · Ctrl+L redraw · Ctrl+C exit"
	return s
}

func statusDot(it Item) string {
	// HubOnly rows have no activation arm on this panel (their write arm lives
	// in the setup hub), so the panel marks them with the same read-only dot it
	// prints for a hard ReadOnly row: what the marker says is true here.
	if it.ReadOnly || it.HubOnly {
		return "● " + ReadOnlyMarker
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
	fmt.Fprintln(t.out, s.Notice)
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

// applyChoice runs the choice row's write arm. P5-1 U5b (F3(a)) moved the gate
// INSIDE every choice-bearing write arm (W20 scalars, W13 mode switch), so this
// function no longer opens a second, body-less confirmation of its own: an arm
// that arms pendingConfirm has already presented its exact diff, and answering
// that gate is what runs the Core verb. The row's Danger flag still drives the
// panel markers and the detail-pane notice; it is no longer the gate switch, so
// a future choice-bearing write row MUST compose its own gate in the arm.
func (t *TUI) applyChoice(value string) {
	cs := t.choices
	t.choices = nil
	run := cs.after
	if run == nil {
		return
	}
	if err := run(value); err != nil {
		t.setMaskedError("error: "+err.Error(), value)
	} else if t.pending != nil {
		// The arm only presented the diff; [SUCCESS] is stamped by the confirm
		// runner once the gate resolves (B8, the same rule runPrompt follows).
		t.message = "confirm to proceed - " + cs.item.Label
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
			return t.setScalar(it, key, v)
		}}
	case it.ID == "current_mode" || it.ID == "mode.switch":
		t.choices = &choiceState{item: it, choices: []string{"full", "clean"}, after: func(v string) error {
			return t.switchMode(it, v)
		}}
	case it.ID == "mode.restore":
		// P5-1 U5b (W15): the restore plan is composed BEFORE consent (the source
		// directory, its file groups and the pre-restore destination), so the
		// operator approves a deployment instead of a row label. A source that
		// cannot be resolved refuses before the gate: there is no plan to present
		// and no deployment to consent to.
		if err := t.restoreMode(it); err != nil {
			t.setMaskedError("error: " + err.Error())
		}
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
		t.message = "interactive wizard: run `agy pro setup` (or `agy pro settings providers add-interactive`) - raw key echo is masked first-5 + ****"
	case it.ID == "providers.add_json":
		t.runPrompt("JSON file path or - for stdin", func(s string) error {
			return t.addJSON(it, s)
		})
	case it.ID == "providers.test":
		t.runPrompt("provider name", func(s string) error { return t.core.ProviderTest(s) })
	case it.ID == "providers.rename":
		t.runPrompt("current provider name", func(old string) error {
			_, err := t.promptLine("new provider name", func(neu string) error {
				return t.renameProvider(it, old, neu)
			})
			return err
		})
	case it.ID == "providers.delete":
		t.runPrompt("provider name to delete (registry entry only)", func(s string) error {
			return t.deleteProvider(it, s)
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
			// P5-1 U5a (W32/W33): the plan is composed BEFORE consent and its
			// entries are rendered in the gate, so the operator approves the
			// NAMES this op may write, never the values.
			// H4: seed per-token forms like the CLI twin (a multi-token target
			// quotes individual tokens in the aggregate error).
			plan := t.core.panelFixPlan(s)
			t.pending = &pendingConfirm{
				item:     it,
				operands: providers.FlagSeedOperands(s),
				body:     plan.diff(),
				run: func() error {
					if err := t.core.panelRepairReverify(plan, "agy pro settings doctor fix-keys"); err != nil {
						return err
					}
					return t.core.DoctorFixKeys(s)
				},
			}
			return nil
		})
	case it.ID == "doctor.rollback_keys":
		t.runPrompt("backup dir printed by fix-keys", func(s string) error {
			// F6: the rollback error (the raw os.ReadFile PathError from the
			// repair stack) re-embeds the resolved backup file; seed the frame
			// mask with the prompted dir and the resolved file, matching the
			// CLI rollback tails (cmd/agy E5).
			// Batch N (M6 preemptive): the seed list also takes the per-token
			// forms (providers.FlagSeedOperands), the batch-M composition the
			// promptLine twin and the CLI rollback tails use: a location-shaped
			// whole operand ("./backups,<secret>") never seeds, so the
			// composite PathError re-echoed the bare secret token raw.
			// P5-1 U5a (W34): the backed-up entry set is read here and rendered
			// in the gate (names and roots only), then re-verified at write time.
			plan := panelRollbackPlan(s)
			t.pending = &pendingConfirm{
				item:     it,
				operands: providers.FlagSeedOperands(s, providers.ResolveBackupFile(s)),
				body:     plan.diff(),
				run: func() error {
					if err := t.core.panelRepairReverify(plan, "agy pro settings doctor rollback-keys"); err != nil {
						return err
					}
					return t.core.DoctorRollbackKeys(s)
				},
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
			// The preset name and its numbers are user-set operands; the legacy
			// settings screen renders them through providers.MaskOperand, the
			// same authority the preset screens use (screen_presets.go
			// buildRows). Benign values stay byte-identical.
			t.result = append(t.result, fmt.Sprintf("     ⎿ %s = %s",
				providers.MaskOperand(p.Name), providers.MaskOperand(p.Numbers)))
		}
		if len(presets) == 0 {
			t.result = []string{"     ⎿ (no saved presets; p1/p2/p3 defaults always available)"}
		}
	case it.ID == "swarm.save_preset":
		t.runPrompt("preset name", func(name string) error {
			_, err := t.promptLine("numbers (e.g. 1,3,19,23)", func(nums string) error {
				// P5-1/M-2 parity with the full-screen hub (tui.gateCorruptSwarmSave):
				// a corrupt swarm-state.json is silently replaced here otherwise, and
				// P5-1 U5b (W24) added the missing append consent: the healthy-path save
				// used to append with no presentation at all. One gate now covers both
				// halves, with two independent reasons on it.
				presets, corrupt, err := t.swarmPresetsProbe()
				if err != nil {
					return err
				}
				return t.savePreset(it, presets, corrupt, name, nums)
			})
			return err
		})
	case it.ReadOnly, it.HubOnly:
		// HubOnly rows land here for the same reason the dot says "ro": this
		// panel has no write arm for them (S7-C6). The note carries the hub
		// route, so the answer is a direction, not a dead end.
		t.message = "read-only: " + it.Note
	default:
		t.message = it.Label + ": " + it.Note
	}
}

// runPrompt reads one text line, runs fn, and records the outcome message.
// When fn only arms the y/n gate (every persisted-state write: add-json, rename,
// delete, fix-keys, rollback), the
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
		t.message = "confirm to proceed - " + prompt
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
		// Batch N (M6 preemptive): the seed list also takes the per-token
		// forms (providers.FlagSeedOperands), the batch-M composition the
		// doctor.fix_keys gate and the CLI twins already use. The whole
		// prompted value alone is not enough: a location-shaped whole value
		// ("./backups,<secret>") never seeds (SeedEligible's location
		// exemption), so the composite sentence fell back to MaskOperand,
		// whose generic patterns do not cover a bare secret token, and the
		// tail re-echoed it raw. FlagSeedOperands keeps the whole value AND
		// adds each comma/space/semicolon-separated token, so the token seed
		// closes the sentence; separator-free values seed exactly what they
		// seeded before (byte-identical).
		return s, errors.New(providers.MaskSentence(err.Error(), providers.FlagSeedOperands(s)...))
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

// swarmCorruptGateBody is the corrupt swarm-state.json acknowledgment
// sentence, reused VERBATIM from the full-screen surfaces' gate
// (presets.corrupt.body in all twelve catalogs, resolved by
// tui.gateCorruptSwarmSave and the fanout preset screens). Package tui imports
// this package, so the panel cannot call that helper and the sentence is copied
// rather than invented: TestSwarmCorruptGateWordingMatchesCatalog pins it
// byte-equal to the en catalog entry, so a wording change on one surface cannot
// leave the other behind. The {{path}} slot names the file a save would
// overwrite.
const swarmCorruptGateBody = "{{path}} is corrupt and was ignored. Saving now overwrites it. Continue?"

// swarmCorruptCounter counts the swarm store's corrupt-file fallback warnings
// while mirroring each one to the sink the store carried before (os.Stderr by
// default). The legacy panel runs in line mode, where that stderr line IS
// readable, so the redirect adds the count the write gate needs instead of
// hiding the line: this is the counting half of the fanout presets screen's
// corruptPresetSink/corruptLatch wiring, re-wired here because those types live
// in package tui.
type swarmCorruptCounter struct {
	n      int
	mirror io.Writer
}

func (c *swarmCorruptCounter) Write(p []byte) (int, error) {
	c.n++
	if c.mirror != nil {
		if _, err := c.mirror.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// swarmPresetsProbe reads the saved presets through the counting sink and
// reports whether the file was corrupt on THIS read (the fanout screen's
// count-delta rule). The read happens immediately before the write, so the
// answer describes the exact file the save replaces, and a save that has just
// written a healthy file needs no second acknowledgment. A store that is not
// the file-backed one (embeddeds, test doubles) has no warning sink and reports
// no corruption: there is no file to overwrite.
func (t *TUI) swarmPresetsProbe() ([]swarm.SavedPreset, bool, error) {
	fs := t.core.SwarmStateStore()
	if fs == nil {
		presets, err := t.core.SwarmPresets()
		return presets, false, err
	}
	c, ok := fs.WarnOut.(*swarmCorruptCounter)
	if !ok {
		c = &swarmCorruptCounter{mirror: fs.WarnOut}
		fs.WarnOut = c
	}
	before := c.n
	presets, err := t.core.SwarmPresets()
	return presets, c.n > before, err
}

// swarmStoreDisplayPath resolves the path of swarm-state.json the store reads
// and writes (AGY_PRO_HOME aware), so the gate names the file a save would
// overwrite. It mirrors the full-screen surface's swarmStatePath.
func (t *TUI) swarmStoreDisplayPath() string {
	if fs := t.core.SwarmStateStore(); fs != nil && fs.Path != "" {
		return fs.Path
	}
	if p, err := swarm.DefaultStatePath(); err == nil && p != "" {
		return p
	}
	return "swarm-state.json"
}

func (t *TUI) addJSON(it Item, path string) error {
	data, err := readSpecSource(path, t.lines)
	if err != nil {
		return err
	}
	spec, err := ParseProviderSpecJSON(data)
	if err != nil {
		return err
	}
	base, err := t.core.panelProviderBaseFor(spec.Name)
	if err != nil {
		return err
	}
	t.pending = &pendingConfirm{
		item:     it,
		operands: append(providers.FlagSeedOperands(spec.Name, spec.BaseURL), providers.FlagSeedOperands(spec.APIKeyEnv)...),
		body:     panelProviderAddDiff(base, spec),
		run: func() error {
			if err := t.core.panelProviderReverify(base, "agy pro settings providers add-json"); err != nil {
				return err
			}
			return t.core.ProviderAdd(spec)
		},
	}
	return nil
}

// renameProvider is the panel's W2 arm (P5-1 U5a): it composes the exact rename
// diff from disk and arms the y/n gate instead of writing. A name that is not on
// disk refuses BEFORE the gate, the same ordering the hub's rename approval uses
// (screen_settings.go:751-755): there is no record to move, so there is no diff
// to approve and no write to consent to.
func (t *TUI) renameProvider(it Item, old, neu string) error {
	base, err := t.core.panelProviderBaseForRename(old, neu)
	if err != nil {
		return err
	}
	if !base.present {
		return panelRefusal(panelMissingRenameFmt+"; nothing renamed", providers.FlagSeedOperands(old, neu), panelSlot(old))
	}
	t.pending = &pendingConfirm{
		item:     it,
		operands: append(providers.FlagSeedOperands(old), providers.FlagSeedOperands(neu)...),
		body:     panelProviderRenameDiff(base, neu),
		run: func() error {
			if err := t.core.panelProviderReverify(base, "agy pro settings providers rename"); err != nil {
				return err
			}
			// The base record's own name is the exact spelling the presented diff
			// committed to; the store matches case-insensitively, so handing it the
			// on-disk spelling addresses the record that was displayed.
			return t.core.ProviderRename(base.record.Name, neu)
		},
	}
	return nil
}

// deleteProvider is the panel's W3 arm (P5-1 U5a): it composes the exact delete
// diff from disk and arms the y/n gate. The pre-change arm gated the write with
// the bare item label and no body, so the operator approved a word instead of a
// record; a name that is not on disk now refuses before the gate like the
// providers screen's confirmDelete (screen_providers.go:667-673).
func (t *TUI) deleteProvider(it Item, name string) error {
	base, err := t.core.panelProviderBaseFor(name)
	if err != nil {
		return err
	}
	if !base.present {
		return panelRefusal(panelMissingDeleteFmt+"; nothing deleted", providers.FlagSeedOperands(name), panelSlot(name))
	}
	t.pending = &pendingConfirm{
		item:     it,
		operands: providers.FlagSeedOperands(name),
		body:     panelProviderDeleteDiff(base),
		run: func() error {
			if err := t.core.panelProviderReverify(base, "agy pro settings providers delete"); err != nil {
				return err
			}
			return t.core.ProviderDelete(base.record.Name)
		},
	}
	return nil
}

// --- P5-1 U5b: the remaining panel write doors (F3(a)) ----------------------
//
// W20 (state.json scalars), W24 (the swarm preset append) and W13/W15 (the mode
// switch and restore) were the panel's last ungated write paths. Each arm below
// composes its exact diff through paneldiff.go, arms ONE y/N gate, and re-verifies
// the signed state immediately before the Core verb runs, so the diff the
// operator approved is the diff that was composed from disk.

// panelRowVerb names the door in a stale-base refusal sentence: the panel route
// plus the row label the operator pressed.
func panelRowVerb(it Item) string {
	return "the " + it.Label + " row of the `agy pro settings` panel"
}

// setScalar is the panel's W20 arm: a state.json scalar choice row (default
// profile, bridge-tools allow-list, native seat concurrency). The pre-change arm
// called Core.Set the instant a number was pressed, so state.json changed with no
// presentation; the diff is composed here and the write waits for y.
func (t *TUI) setScalar(it Item, key, value string) error {
	plan, err := t.core.panelStatePlanFor(key, panelAfterStateValue(key, value))
	if err != nil {
		return err
	}
	t.pending = &pendingConfirm{
		item: it,
		// The prompted value may be a misdirected key paste, so it seeds the mask
		// for any error the store forwards (B2/U8, the same seeding applyChoice
		// already applies to a choice value).
		operands: providers.FlagSeedOperands(value),
		body:     plan.diff(),
		run: func() error {
			if err := t.core.panelStateReverify(plan, panelRowVerb(it)); err != nil {
				return err
			}
			return t.core.Set(key, value)
		},
	}
	return nil
}

// switchMode is the panel's W13 arm (the `current_mode` and `mode.switch` rows).
// It used to open a body-less confirmation; it now presents U2's file plan (mode
// transition, config dir, managed set, deployment groups, current_mode row) and
// re-verifies the mode and the managed set before the deployment starts.
func (t *TUI) switchMode(it Item, target string) error {
	plan, err := t.core.panelModeSwitchPlan(target)
	if err != nil {
		return err
	}
	t.pending = &pendingConfirm{
		item: it,
		body: plan.diff(),
		run: func() error {
			if err := t.core.panelModeReverify(plan, panelRowVerb(it)); err != nil {
				return err
			}
			return t.core.ModeSwitch(target)
		},
	}
	return nil
}

// restoreMode is the panel's W15 arm (the `mode.restore` row). The plan names the
// resolved source directory, its file groups and the pre-restore destination; a
// source that cannot be resolved refuses BEFORE the gate (there is no plan to
// present), matching U2's CLI ordering. mode.Restore itself still re-resolves the
// source, so a backup that disappears between consent and the write is named by
// mode's own sentence.
func (t *TUI) restoreMode(it Item) error {
	plan, err := t.core.panelModeRestorePlan()
	if err != nil {
		return err
	}
	t.pending = &pendingConfirm{
		item: it,
		body: plan.diff(),
		run: func() error {
			if err := t.core.panelModeReverify(plan, panelRowVerb(it)); err != nil {
				return err
			}
			return t.core.ModeRestore()
		},
	}
	return nil
}

// savePreset is the panel's W24 arm. Two independent reasons can ride the ONE
// gate: the append consent (the exact "+ preset <name> = <numbers>" line, the
// bounded list of presets it keeps, the atomic-0600 sentence for the file it
// rewrites) and, when the store was corrupt on this read, the overwrite
// acknowledgment naming the file (notice). The append itself re-reads the preset
// list and appends to what it verified, so a preset saved under the prompt
// refuses instead of being duplicated or dropped.
func (t *TUI) savePreset(it Item, presets []swarm.SavedPreset, corrupt bool, name, nums string) error {
	plan := panelPresetPlanFor(presets, name, nums)
	notice := ""
	if corrupt {
		notice = i18n.Fill(swarmCorruptGateBody, map[string]string{
			"path": providers.MaskOperand(t.swarmStoreDisplayPath()),
		})
	}
	t.pending = &pendingConfirm{
		item:     it,
		operands: providers.FlagSeedOperands(name, nums),
		notice:   notice,
		body:     plan.diff(),
		run: func() error {
			fresh, err := t.core.panelPresetReverify(plan, panelRowVerb(it))
			if err != nil {
				return err
			}
			return t.core.SwarmSavePresets(append(fresh, swarm.SavedPreset{Name: name, Numbers: nums}))
		},
	}
	return nil
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
