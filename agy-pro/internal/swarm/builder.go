package swarm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// RunFanout executes one fanout CLI invocation (contract U3: delegate via
// fanout.Run, the same pattern as launcher's runSwarmFromMenu). Tests replace
// it to stub the swarm run.
var RunFanout = fanout.Run

// RunSpec is a fully-resolved swarm ready to execute (contract U3).
type RunSpec struct {
	Template   Template
	Seats      []inventory.Entry
	Root       string
	Prompt     string
	PromptFile string // when set, the fanout args reference this file instead of inlining Prompt
	TimeoutSec int
	// PresetLineup is the seat lineup the fanout preset ladder WOULD resolve for a
	// run that names no seats: the store-backed pin the door read ONCE, at spec
	// construction (PresetSeatLineup).
	//
	// FL-1: it is applied only when this spec carries NO seats of its own
	// (len(Seats) == 0). A seat the operator named, on the command line or in the
	// interactive picker, outranks the preset everywhere, so a spec that has
	// seats always emits them as --seats and the pin is merely recorded (for the
	// door's echo). The zero value keeps the historical vector byte for byte.
	PresetLineup fanout.SeatLineup
}

// Args returns the exact fanout argument vector for this spec. When the prompt
// arrived through --prompt-file the vector references the file: inlining the
// prompt text into the echoed command line would put untrusted text into a
// copy-pasteable shell line (the dry-run echo is quoted Go-style, not
// shell-safe) and leak prompt content into console scrollback.
//
// Seat source, strongest first (FL-1): the seats this spec carries > the preset
// pin. The --seats pair is OMITTED only when the spec has NO seats and the pin
// has some (see BuildFanoutArgsPresetLineup), because emitting --seats would
// outrank the ladder the pin lives on, while emitting an empty --seats would
// still claim the flag rung and suppress the pin.
func (s RunSpec) Args() []string {
	root := s.Root
	if root == "" {
		root = "."
	}
	if len(s.Seats) == 0 && s.PresetLineup.Pinned() {
		if s.PromptFile != "" {
			return BuildFanoutArgsFilePresetLineup(s.Template, root, s.PromptFile, s.TimeoutSec)
		}
		return BuildFanoutArgsPresetLineup(s.Template, root, s.Prompt, s.TimeoutSec)
	}
	if s.PromptFile != "" {
		return BuildFanoutArgsFile(s.Template, s.Seats, root, s.PromptFile, s.TimeoutSec)
	}
	return BuildFanoutArgs(s.Template, s.Seats, root, s.Prompt, s.TimeoutSec)
}

// AppliesPresetLineup reports whether Args() will take the seat lineup from the
// preset instead of from this spec (no seats here + a pinned lineup). Doors use
// it to print the notice exactly when the substitution happens and to skip the
// misleading "passing no --seats" claim when the operator's seats win.
func (s RunSpec) AppliesPresetLineup() bool {
	return len(s.Seats) == 0 && s.PresetLineup.Pinned()
}

// PresetSeatLineup is the store-backed lookup the doors call to fill
// RunSpec.PresetLineup. It is a var, like RunFanout, so tests can pin the pin
// without a presets file on disk.
var PresetSeatLineup = fanout.ResolvePresetSeatLineup

// ResolveRunSpecLineup reads the fanout preset seat pin for this run and returns
// it with the warning line to print ("" when the read was clean). A lookup error
// (the env var names a preset that does not exist, an unparsable seats value)
// yields no lineup plus its one-line warning: the door then keeps the seats it
// was given, which is the pre-U2a behavior, and the delegated fanout run reports
// the same misconfiguration itself when it resolves the ladder.
//
// The pin is only a CANDIDATE here: whether it replaces the door's seats is
// RunSpec.AppliesPresetLineup()'s answer (no seats passed + a pinned lineup), and
// that is also when the notice belongs.
func ResolveRunSpecLineup() (fanout.SeatLineup, string) {
	lineup, err := PresetSeatLineup()
	if err != nil {
		return fanout.SeatLineup{}, fmt.Sprintf("swarm: warning: %v; using the requested seats", err)
	}
	return lineup, ""
}

// Execute runs the resolved swarm via RunFanout and renders the console
// summary table `seat | kind | model | verdict` from SummaryReport.Seats
// (contract U3/C3). The fanout run is delegated with `-json` so the summary is
// machine-readable; human artifacts (reconciliation.md/summary.json) still
// come from the delegated run. Returns the process exit code.
func Execute(spec RunSpec, in io.Reader, out, errw io.Writer, useANSI bool) int {
	args := spec.Args()
	var buf bytes.Buffer
	code := RunFanout(args, in, &buf, errw)
	if code != 0 {
		return code
	}
	var summary fanout.SummaryReport
	if err := json.Unmarshal(buf.Bytes(), &summary); err != nil {
		fmt.Fprintln(errw, "warning: fanout summary could not be parsed; raw output follows")
		_, _ = out.Write(buf.Bytes())
		return 0
	}
	RenderSummary(out, summary, useANSI)
	return 0
}

// RenderSummary writes the console summary table `seat | kind | model |
// verdict` from SummaryReport.Seats, plus a run-level footer whenever the run is
// NOT a clean full-quorum pass (FL-5): the requested-versus-seated counts, the
// skipped preset seats by name, and the degraded flag. Without that footer the
// table alone reads as "every seat I see succeeded", which is exactly the
// degraded-hiding the reviewers flagged. A clean run prints no footer, so its
// output stays byte-identical to the pre-FL-5 table.
//
// ANSI decoration is applied only when useANSI is true (stdout is a TTY) so
// piped/CI output stays clean (C3).
func RenderSummary(out io.Writer, summary fanout.SummaryReport, useANSI bool) {
	RenderSeatTable(out, summary.Seats, useANSI)
	renderRunFooter(out, summary)
}

// RenderSeatTable writes only the `seat | kind | model | verdict` table.
func RenderSeatTable(out io.Writer, seats []fanout.SeatResult, useANSI bool) {
	header := "seat | kind | model | verdict"
	if useANSI {
		fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\n", header)
	} else {
		fmt.Fprintln(out, header)
	}
	for _, s := range seats {
		// E8: registry-derived model IDs are echoed here too; mask key-shaped
		// ones for display consistency with `status`/`models` (benign IDs
		// readable).
		// FIX-8: this site was the last exempt operand-masker user on a registry
		// slot (recorded as a residual since the phase-2 masking mission). The
		// model id is a registry slot, so it renders through the registry-slot
		// authority (MaskSlot): every benign id keeps its exact bytes, and the
		// underscore-head credential class that MaskOperand renders raw masks
		// here like it already does in the seat list.
		fmt.Fprintf(out, "%d | %s | %s | %s\n",
			s.SeatID, kindOf(s), providers.MaskSlot(s.FinalTarget.Model.ID), verdictOf(s))
	}
}

// renderRunFooter states the run-level facts the seat table cannot: whether the
// run is degraded, how many seats were requested versus actually seated, and
// which preset seats were skipped. Nothing prints for a clean run
// (degenerate: NRequested is 0 for a caller that never set it, so the count
// comparison only fires when the caller supplied the number).
func renderRunFooter(out io.Writer, s fanout.SummaryReport) {
	if !s.Degraded && s.SkippedSeats == 0 && s.NRequested <= len(s.Seats) {
		return
	}
	seated := len(s.Seats)
	requested := s.NRequested
	if requested < seated {
		requested = seated
	}
	fmt.Fprintf(out, "run status: %s | seats %d / %d requested", degradedWord(s.Degraded), seated, requested)
	if s.SkippedSeats > 0 {
		specs := ""
		if len(s.SkippedSeatSpecs) > 0 {
			// R2-1 defense in depth: the engine already masks these tokens, but a
			// library caller can hand this struct anything, and the footer is a
			// shareable line. Each token takes the registry-slot authority (also
			// control-safe), so a doubly-masked value stays stable; the ", " join
			// keeps the existing rendering.
			masked := make([]string, 0, len(s.SkippedSeatSpecs))
			for _, spec := range s.SkippedSeatSpecs {
				masked = append(masked, fanout.MaskSeatToken(spec))
			}
			specs = " (" + strings.Join(masked, ", ") + ")"
		}
		fmt.Fprintf(out, " | skipped %d%s", s.SkippedSeats, specs)
	}
	if s.SeatLineup != nil {
		// Which preset supplied the lineup and whether that rung is standing
		// approval or ad-hoc - the same distinction the fanout echo prints.
		approval := "ad-hoc"
		if s.SeatLineup.Standing {
			approval = "standing approval"
		}
		fmt.Fprintf(out, " | lineup from preset %q (%s, %s)",
			providers.MaskSlot(s.SeatLineup.Preset), providers.StripSlotControls(s.SeatLineup.Rung), approval)
	}
	fmt.Fprintln(out)
}

// degradedWord renders the run-level status word for the footer.
func degradedWord(degraded bool) string {
	if degraded {
		return "DEGRADED"
	}
	return "ok"
}

func kindOf(s fanout.SeatResult) string {
	if s.Kind == "" {
		return "provider"
	}
	return string(s.Kind)
}

func verdictOf(s fanout.SeatResult) string {
	v := s.Status
	if v == "" {
		v = "unknown"
	}
	if s.Degraded {
		v += " (degraded)"
	}
	if s.Thin {
		v += " (thin)"
	}
	return v
}

// Builder is the interactive swarm builder (contract U3 flow):
// [1] template numbered pick -> [2] seat multi-select from the combined
// inventory (numbered grammar, presets as defaults) -> [3] tools/budget
// summary (template defaults, editable by number) -> [4] preview the exact
// `agy pro fanout ...` command: Enter runs it, "c" copies/prints only.
type Builder struct {
	In      io.Reader
	Out     io.Writer
	ErrW    io.Writer
	Inv     *inventory.Inventory
	Store   Store // optional; saved presets shown in the seat step
	UseANSI bool
	Root    string // workspace root; "" -> "."
}

// Run drives the builder to a run/copy/cancel outcome and returns the process
// exit code (0 run ok / copy / cancel, 1 run failure, 2 input failure).
func (b *Builder) Run() int {
	r := bufio.NewReader(b.In)
	out := b.Out

	tmpl, ok := b.pickTemplate(r, out)
	if !ok {
		return 2
	}
	seats, ok := b.pickSeats(r, out)
	if !ok {
		return 2
	}

	spec := RunSpec{
		Template:   tmpl,
		Seats:      seats,
		Root:       b.Root,
		Prompt:     tmpl.PromptScaffold,
		TimeoutSec: fanout.DefaultTimeoutSec,
	}
	// FL-1: the fanout preset pin is only a CANDIDATE. The builder always holds
	// seats after step [2] (the operator picked them), so Args() keeps emitting
	// them and the pin never substitutes: chat/operator-named seats outrank the
	// preset everywhere. The pin is still read so a run that somehow reached this
	// point with no seats can fall back to it, and so a lookup failure surfaces
	// once instead of silently changing nothing.
	var lookupWarning string
	spec.PresetLineup, lookupWarning = ResolveRunSpecLineup()
	if lookupWarning != "" {
		fmt.Fprintln(b.ErrW, lookupWarning)
	}
	for {
		b.printBudgetAndPreview(out, spec)
		line, ok := readBuilderLine(r)
		if !ok {
			fmt.Fprintln(b.ErrW, "Error: input closed before the swarm run")
			return 2
		}
		choice := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(choice, "c"):
			fmt.Fprintln(out, FormatCommand(spec.Args()))
			return 0
		case strings.EqualFold(choice, "q") || strings.EqualFold(choice, "n"):
			fmt.Fprintln(out, "Swarm build cancelled.")
			return 0
		case isFieldNumber(choice):
			if !b.editField(r, out, &spec, choice) {
				return 2
			}
			continue
		default:
			// Enter / "y" / anything else runs the previewed command.
			return Execute(spec, b.In, out, b.ErrW, b.UseANSI)
		}
	}
}

// pickTemplate is step [1]: the numbered template pick. Empty input accepts
// the default (first) template. Returns false on EOF.
func (b *Builder) pickTemplate(r *bufio.Reader, out io.Writer) (Template, bool) {
	templates := Templates()
	fmt.Fprintln(out, "=== agy pro swarm: template ===")
	for i, t := range templates {
		fmt.Fprintf(out, "[%d] %-16s  task=%s  tools=%s\n", i+1, t.Name, t.Task, t.Tools)
	}
	for {
		fmt.Fprint(out, "Select template [1]: ")
		line, ok := readBuilderLine(r)
		if !ok {
			fmt.Fprintln(b.ErrW, "Error: input closed during template selection")
			return Template{}, false
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			return templates[0], true
		}
		if n, err := strconv.Atoi(choice); err == nil {
			if n >= 1 && n <= len(templates) {
				return templates[n-1], true
			}
		} else if t, found := FindTemplate(choice); found {
			return t, true
		}
		// E7/B2: the raw TTY line may be a misdirected key paste. The composite
		// error routes through providers.MaskSentence seeded with the raw line
		// (providers.SeedEligible), so a decorated plant ("native:<aws>",
		// "1-<aws>", a non-eyJ JWT) is masked wherever the sentence echoes it
		// while benign tokens stay readable.
		fmt.Fprintln(b.ErrW, providers.MaskSentence(fmt.Sprintf("invalid template %q: enter 1-%d or a template id", choice, len(templates)), choice))
	}
}

// pickSeats is step [2]: presets as numbered defaults plus the full numbered
// inventory (excluded rows marked `excluded (exclusion list)`, the one marker
// providers.ExcludedMarker owns), then the §2.7 selection line. Selected seats
// print with provenance + quota note.
//
// The saved presets are loaded ONCE and used twice: for the listing below and
// for the name resolution (F04), so the name the user reads is exactly the name
// that resolves. A store read error degrades to "no saved presets" (the listing
// and the grammar simply do not offer names) rather than failing the step.
func (b *Builder) pickSeats(r *bufio.Reader, out io.Writer) ([]inventory.Entry, bool) {
	inv := b.Inv
	fmt.Fprintln(out, "=== agy pro swarm: seats ===")
	fmt.Fprintln(out, "Presets (numbered defaults):")
	fmt.Fprintln(out, "  p1  2 native + 2 external")
	fmt.Fprintln(out, "  p2  3 external (family-rotated)")
	fmt.Fprintln(out, "  p3  4 mixed (flash + pro native + 2 external)")
	var saved []SavedPreset
	if b.Store != nil {
		if loaded, err := b.Store.LoadPresets(); err == nil {
			saved = loaded
		}
	}
	if len(saved) > 0 {
		fmt.Fprintln(out, "Saved presets:")
		for _, p := range saved {
			// The saved preset is the same free-text value kind the three
			// preset surfaces render through providers.MaskOperand; this
			// picker was the swarm twin that still composed both operands
			// raw (internal/tui/screen_settings.go presetLines writes the
			// identical "%s = %s" line, masked). A credential planted in a
			// name or numbers field renders first-5+**** here too, and a
			// benign pair keeps its exact bytes.
			// R3-item-4: the name is a store-sourced slot like every preset
			// name in fanout, so it takes the registry-slot authority (MaskSlot,
			// which also strips the Cc/Cf class). r7 F-01: the NUMBERS are
			// seat-selection text and take the seat-token authority per token - a
			// legacy record can still carry a credential, and MaskOperand is the
			// identity on that class. Benign pairs keep their exact bytes.
			fmt.Fprintf(out, "  %s = %s\n", providers.MaskSlot(providers.StripSlotControls(p.Name)), fanout.MaskSeatTokens(p.Numbers))
		}
	}
	fmt.Fprintln(out, "Numbered inventory:")
	RenderSeatList(out, inv)
	for {
		fmt.Fprint(out, "Select seats (e.g. 1,3,5 or 2-4 or p1/p2/p3 or a saved preset name) [p1]: ")
		line, ok := readBuilderLine(r)
		if !ok {
			fmt.Fprintln(b.ErrW, "Error: input closed during seat selection")
			return nil, false
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			choice = "p1" // accept the presented default preset (§2.7)
		}
		seats, err := ResolveSeatsWith(choice, inv, saved, b.ErrW)
		if err != nil {
			// F5: inventory selection errors quote the offending token; seed
			// the masker with the raw line AND each §2.7 token so the quoted
			// token is masked even when the line carries several tokens
			// (providers.SeedEligible decides, benign tokens stay readable).
			// F04: a saved name expands to its stored Numbers, and the
			// inventory error then quotes a NUMBER token instead of what the
			// user typed, so SelectionSeeds adds each resolved Numbers value.
			seeds := SelectionSeeds(choice, saved)
			// r4 class closure: the quoted-token pass covers the selection grammar's
			// own MaskOperand echo (inventory package, outside this lane), so the
			// interactive seat step cannot print a raw underscore-head token either.
			fmt.Fprintln(b.ErrW, fanout.MaskQuotedSeatTokens(providers.MaskSentence(err.Error(), seeds...)))
			continue
		}
		if len(seats) == 0 {
			fmt.Fprintln(b.ErrW, "selection is empty: pick at least one seat")
			continue
		}
		fmt.Fprintln(out, "Selected seats:")
		for i, e := range seats {
			fmt.Fprintln(out, SeatLine(i+1, e))
		}
		return seats, true
	}
}

// printBudgetAndPreview renders step [3] tools/budget summary (template
// defaults, editable by number) and step [4] the exact fanout command.
func (b *Builder) printBudgetAndPreview(out io.Writer, spec RunSpec) {
	fmt.Fprintln(out, "=== tools & budget ===")
	fmt.Fprintf(out, "  1. task     %s\n", spec.Template.Task)
	fmt.Fprintf(out, "  2. tools    %s\n", spec.Template.Tools)
	fmt.Fprintf(out, "  3. timeout  %ds\n", spec.TimeoutSec)
	prompt := spec.Prompt
	if prompt == "" {
		prompt = "(template default)"
	}
	// I5/C1/S3: mask before any truncation so a key-shaped prompt cannot ride
	// into the preview frame raw at ANY length (the old >60-byte gate left a
	// <=60-byte key verbatim at this slot). providers.FlagSeedOperands already
	// returns the whole prompt first and then each
	// comma/space/semicolon-separated token, so the seed list is exactly that:
	// a multi-word prompt with an embedded bare key masks per token while
	// benign wording stays readable (providers.SeedEligible decides).
	prompt = providers.MaskSentence(prompt, providers.FlagSeedOperands(prompt)...)
	if utf8.RuneCountInString(prompt) > 60 {
		// Truncate on the RUNE boundary: the old prompt[:57] byte-slice could
		// split a multi-byte rune and render U+FFFD.
		prompt = string([]rune(prompt)[:57]) + "..."
	}
	fmt.Fprintf(out, "  4. prompt   %s\n", prompt)
	fmt.Fprintln(out, "=== preview ===")
	fmt.Fprintf(out, "  %s\n", FormatCommand(spec.Args()))
	if spec.AppliesPresetLineup() {
		// The command line carries no --seats (the preset supplies the lineup),
		// so the seat source is stated right under it. This branch only fires for
		// a spec with NO seats: when the operator picked seats, the vector shows
		// them and no notice claims otherwise (FL-1).
		fmt.Fprintf(out, "  seats: %s\n", SeatLineupNotice(spec.PresetLineup))
	}
	fmt.Fprintln(out, "Enter runs it.  c = copy/print only.  1-4 = edit that field.  q = quit.")
	fmt.Fprint(out, "> ")
}

// editField rewrites one budget field after a numbered edit pick. Returns
// false on EOF.
func (b *Builder) editField(r *bufio.Reader, out io.Writer, spec *RunSpec, choice string) bool {
	labels := map[string]string{"1": "task", "2": "tools", "3": "timeout (seconds)", "4": "prompt"}
	label := labels[choice]
	fmt.Fprintf(out, "Enter new %s: ", label)
	line, ok := readBuilderLine(r)
	if !ok {
		fmt.Fprintln(b.ErrW, "Error: input closed during edit")
		return false
	}
	value := strings.TrimRight(line, "\r\n")
	switch choice {
	case "1":
		if t, found := FindTemplate(value); found {
			// Keep the user's prompt if it diverged from the old scaffold.
			if spec.Prompt == spec.Template.PromptScaffold {
				spec.Prompt = t.PromptScaffold
			}
			spec.Template.Task = t.Task
			spec.Template.ID = t.ID
			spec.Template.Name = t.Name
			if spec.Template.Tools == "" {
				spec.Template.Tools = t.Tools
			}
		} else {
			spec.Template.Task = strings.TrimSpace(value)
		}
	case "2":
		spec.Template.Tools = strings.TrimSpace(value)
	case "3":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n <= 0 {
			fmt.Fprintln(b.ErrW, "timeout must be a positive integer of seconds; keeping previous value")
		} else {
			spec.TimeoutSec = n
		}
	case "4":
		spec.Prompt = value
	}
	return true
}

// isFieldNumber reports whether the confirm-line choice is an edit pick (1-4).
func isFieldNumber(choice string) bool {
	n, err := strconv.Atoi(choice)
	return err == nil && n >= 1 && n <= 4
}

// readBuilderLine reads one input line. The trailing newline is kept out.
func readBuilderLine(r *bufio.Reader) (string, bool) {
	line, err := r.ReadString('\n')
	if err != nil {
		if line == "" {
			return "", false
		}
		// Final line without a trailing newline still counts.
		return strings.TrimRight(line, "\r\n"), true
	}
	return strings.TrimRight(line, "\r\n"), true
}

// RenderSeatList writes the numbered inventory rows for the seat step
// (contract §2.7 layout). Excluded rows carry the `excluded (exclusion list)`
// suffix (providers.ExcludedMarker, the one user-visible wording).
func RenderSeatList(out io.Writer, inv *inventory.Inventory) {
	if inv == nil {
		return
	}
	idW, midW := 0, 0
	for _, e := range inv.Entries {
		// The widths come from the MASKED cells (the same strings the row
		// renders), so a long-but-masked id cannot pad every other row past its
		// rendered text. Mirrors inventory.RenderNumbered's width rule.
		if n := len(providers.MaskSlot(e.ModelID)); n > idW {
			idW = n
		}
		if n := len(providers.MaskSlot(seatMidColumn(e))); n > midW {
			midW = n
		}
	}
	for _, e := range inv.Entries {
		num := fmt.Sprintf("[%d]", e.Number)
		if e.Number < 10 {
			num = fmt.Sprintf("[ %d]", e.Number)
		}
		suffix := ""
		if e.KeyMissing {
			suffix = "      (key missing)"
		}
		if e.Excluded {
			suffix += "      " + providers.ExcludedMarker()
		}
		// E8/S5: the model ID, the mid column (native display name or the
		// provider capability text), and the group tag are registry-derived
		// display slots, so all three route through providers.MaskSlot, the one
		// registry-slot display authority, exactly like inventory.RenderNumbered
		// over the same entries. MaskOperand's location exemption and its
		// pre-policy classifiers left a "./<key>"-shaped or look-alike-decorated
		// id readable in the seat list while the models --json twin masked it.
		fmt.Fprintf(out, "%s %-*s  %-*s  [%s]%s\n", num, idW, providers.MaskSlot(e.ModelID), midW, providers.MaskSlot(seatMidColumn(e)), providers.MaskSlot(e.Group), suffix)
	}
}

func seatMidColumn(e inventory.Entry) string {
	if e.Kind == "native" {
		return e.DisplayName
	}
	return e.Capabilities
}
