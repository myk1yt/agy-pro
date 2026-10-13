package swarm

import (
	"fmt"
	"strconv"
	"strings"

	"agypro/internal/inventory"
)

// BuildFanoutArgs builds the exact `agy pro fanout` argument vector for a
// resolved swarm (contract U3). Seat order is preserved: the `--seats` value
// is the exact "," join of the entries' canonical Spec tokens
// ("native:<id>" / "<provider>/<id>").
//
// Flags use the documented double-dash CLI spelling of §2.6 (`agy pro fanout
// --seats ...`); Go's flag parser accepts it identically to the single-dash
// sketch in the contract signature comment.
//
// A2: `--task t.Task` and `--tools t.Tools` are emitted from the template, so
// the non-interactive --task / --tools overrides (SwarmFlags.ApplyToTemplate)
// and the interactive builder's edits reach the fanout run through the same
// two flags.
//
// The vector ends in "--json": the builder consumes fanout's machine summary
// (SummaryReport) to render the console `seat | kind | model | verdict` table,
// and the preview/dry-run command shown to the user is byte-identical to the
// executed one.
func BuildFanoutArgs(t Template, seats []inventory.Entry, root, prompt string, timeoutSec int) []string {
	specs := make([]string, 0, len(seats))
	for _, e := range seats {
		specs = append(specs, e.Spec)
	}
	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeoutSec
	}
	if root == "" {
		root = "."
	}
	effective := prompt
	if strings.TrimSpace(effective) == "" {
		effective = t.PromptScaffold
	}
	return []string{
		"--task", t.Task,
		"--seats", strings.Join(specs, ","),
		"--root", root,
		"--timeout", strconv.Itoa(timeoutSec),
		"--tools", t.Tools,
		"--prompt", effective,
		"--json",
	}
}

// FormatCommand renders a fanout argument vector as the equivalent
// `agy pro fanout ...` command line (preview, copy, --dry-run). Arguments that
// contain whitespace or quotes are double-quoted with inner quotes escaped.
func FormatCommand(args []string) string {
	var b strings.Builder
	b.WriteString("agy pro fanout")
	for _, a := range args {
		b.WriteByte(' ')
		if strings.ContainsAny(a, " \t\"") {
			b.WriteString(strconv.Quote(a))
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

// QuotaNote renders the per-seat provenance quota note (contract U3 flow):
// native seats draw on the free Antigravity quota, provider seats are metered.
func QuotaNote(kind string) string {
	if kind == "native" {
		return "free Antigravity quota (rate limits)"
	}
	return "metered API"
}

// k3Marker is the exact pool-policy suffix rendered for k3 entries.
const k3Marker = "excluded (pool policy)"

// SeatLine renders one selected seat row (provenance + quota note).
func SeatLine(num int, e inventory.Entry) string {
	return fmt.Sprintf("seat %d  [%s]  %s  %s  (%s)",
		num, e.Kind, e.Spec, displayName(e), QuotaNote(e.Kind))
}

func displayName(e inventory.Entry) string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Capabilities
}
