package swarm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
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
	TimeoutSec int
}

// Args returns the exact fanout argument vector for this spec.
func (s RunSpec) Args() []string {
	root := s.Root
	if root == "" {
		root = "."
	}
	return BuildFanoutArgs(s.Template, s.Seats, root, s.Prompt, s.TimeoutSec)
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
	RenderSummary(out, summary.Seats, useANSI)
	return 0
}

// RenderSummary writes the console summary table `seat | kind | model |
// verdict` from SummaryReport.Seats. ANSI decoration is applied only when
// useANSI is true (stdout is a TTY) so piped/CI output stays clean (C3).
func RenderSummary(out io.Writer, seats []fanout.SeatResult, useANSI bool) {
	header := "seat | kind | model | verdict"
	if useANSI {
		fmt.Fprintf(out, "\x1b[1m%s\x1b[0m\n", header)
	} else {
		fmt.Fprintln(out, header)
	}
	for _, s := range seats {
		fmt.Fprintf(out, "%d | %s | %s | %s\n",
			s.SeatID, kindOf(s), s.FinalTarget.Model.ID, verdictOf(s))
	}
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
		TimeoutSec: DefaultTimeoutSec,
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
		fmt.Fprintf(b.ErrW, "invalid template %q: enter 1-%d or a template id\n", choice, len(templates))
	}
}

// pickSeats is step [2]: presets as numbered defaults plus the full numbered
// inventory (k3 rows marked `excluded (pool policy)`), then the §2.7
// selection line. Selected seats print with provenance + quota note.
func (b *Builder) pickSeats(r *bufio.Reader, out io.Writer) ([]inventory.Entry, bool) {
	inv := b.Inv
	fmt.Fprintln(out, "=== agy pro swarm: seats ===")
	fmt.Fprintln(out, "Presets (numbered defaults):")
	fmt.Fprintln(out, "  p1  2 native + 2 external")
	fmt.Fprintln(out, "  p2  3 external (family-rotated)")
	fmt.Fprintln(out, "  p3  4 mixed (flash + pro native + 2 external)")
	if b.Store != nil {
		if saved, err := b.Store.LoadPresets(); err == nil && len(saved) > 0 {
			fmt.Fprintln(out, "Saved presets:")
			for _, p := range saved {
				fmt.Fprintf(out, "  %s = %s\n", p.Name, p.Numbers)
			}
		}
	}
	fmt.Fprintln(out, "Numbered inventory:")
	RenderSeatList(out, inv)
	for {
		fmt.Fprint(out, "Select seats (e.g. 1,3,5 or 2-4 or p1/p2/p3) [p1]: ")
		line, ok := readBuilderLine(r)
		if !ok {
			fmt.Fprintln(b.ErrW, "Error: input closed during seat selection")
			return nil, false
		}
		choice := strings.TrimSpace(line)
		if choice == "" {
			choice = "p1" // accept the presented default preset (§2.7)
		}
		seats, err := ResolveSeats(choice, inv, b.ErrW)
		if err != nil {
			fmt.Fprintf(b.ErrW, "%v\n", err)
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
	if len(prompt) > 60 {
		prompt = prompt[:57] + "..."
	}
	fmt.Fprintf(out, "  4. prompt   %s\n", prompt)
	fmt.Fprintln(out, "=== preview ===")
	fmt.Fprintf(out, "  %s\n", FormatCommand(spec.Args()))
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
// (contract §2.7 layout). k3 rows carry the `excluded (pool policy)` suffix.
func RenderSeatList(out io.Writer, inv *inventory.Inventory) {
	if inv == nil {
		return
	}
	idW, midW := 0, 0
	for _, e := range inv.Entries {
		if n := len(e.ModelID); n > idW {
			idW = n
		}
		if n := len(seatMidColumn(e)); n > midW {
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
		if inventory.IsK3Excluded(e.ModelID) {
			suffix += "      " + k3Marker
		}
		fmt.Fprintf(out, "%s %-*s  %-*s  [%s]%s\n", num, idW, e.ModelID, midW, seatMidColumn(e), e.Group, suffix)
	}
}

func seatMidColumn(e inventory.Entry) string {
	if e.Kind == "native" {
		return e.DisplayName
	}
	return e.Capabilities
}
