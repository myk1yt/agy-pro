package swarm

import (
	"fmt"
	"strconv"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
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
//
// This is the EXPLICIT-seat form and stays byte-identical to the pre-U2a
// seven-flag vector: a run whose lineup comes from the fanout preset ladder
// instead goes through BuildFanoutArgsPresetLineup, which omits the --seats
// pair (RunSpec.Args decides).
func BuildFanoutArgs(t Template, seats []inventory.Entry, root, prompt string, timeoutSec int) []string {
	return buildFanoutArgs(t, specsOf(seats), root, prompt, "", timeoutSec, true)
}

// BuildFanoutArgsFile is BuildFanoutArgs with the prompt passed by file
// reference: the dry-run echo then never inlines prompt text (untrusted,
// potentially shell-metacharacter-bearing) into a copy-pasteable line.
func BuildFanoutArgsFile(t Template, seats []inventory.Entry, root, promptFile string, timeoutSec int) []string {
	return buildFanoutArgs(t, specsOf(seats), root, "", promptFile, timeoutSec, true)
}

// BuildFanoutArgsPresetLineup builds the vector for a run whose seat lineup
// comes from the fanout PRESET ladder: the --seats pair is omitted, so the
// ladder resolves the active preset's pinned lineup (the store's default_preset
// key, or the EnvFanoutPreset name) exactly as a bare `agy pro fanout` run
// does. Everything else is identical to BuildFanoutArgs.
//
// Why omit rather than repeat the lineup: passing --seats back as a flag would
// outrank the preset rung (the flag wins the ladder outright), and it would
// also re-classify the seats as an explicit operator pick, which takes the
// hard-error drift policy instead of the preset lineup's skip-with-warning one
// (fanout's resolveSeatTargets).
func BuildFanoutArgsPresetLineup(t Template, root, prompt string, timeoutSec int) []string {
	return buildFanoutArgs(t, nil, root, prompt, "", timeoutSec, false)
}

// BuildFanoutArgsFilePresetLineup is BuildFanoutArgsPresetLineup with the
// prompt passed by file reference (same rationale as BuildFanoutArgsFile).
func BuildFanoutArgsFilePresetLineup(t Template, root, promptFile string, timeoutSec int) []string {
	return buildFanoutArgs(t, nil, root, "", promptFile, timeoutSec, false)
}

// buildFanoutArgs is the ONE vector builder behind all four exported forms:
// withSeats controls whether the --seats pair is present, and promptFile != ""
// selects the --prompt-file form over the inlined --prompt one. Keeping one body
// means the four forms cannot drift in flag order, defaults or quoting.
func buildFanoutArgs(t Template, specs []string, root, prompt, promptFile string, timeoutSec int, withSeats bool) []string {
	if timeoutSec <= 0 {
		timeoutSec = fanout.DefaultTimeoutSec
	}
	if root == "" {
		root = "."
	}
	effective := prompt
	if strings.TrimSpace(effective) == "" {
		effective = t.PromptScaffold
	}
	args := make([]string, 0, 13)
	args = append(args, "--task", t.Task)
	if withSeats {
		args = append(args, "--seats", strings.Join(specs, ","))
	}
	args = append(args, "--root", root, "--timeout", strconv.Itoa(timeoutSec), "--tools", t.Tools)
	if promptFile != "" {
		args = append(args, "--prompt-file", promptFile)
	} else {
		args = append(args, "--prompt", effective)
	}
	return append(args, "--json")
}

// specsOf joins seat specs; kept local so the builders cannot drift.
func specsOf(seats []inventory.Entry) []string {
	out := make([]string, 0, len(seats))
	for _, e := range seats {
		out = append(out, e.Spec)
	}
	return out
}

// SeatLineupNotice renders the ONE line that tells the operator the fanout
// preset ladder, not this command line, supplies the seat lineup. It must only be
// printed when RunSpec.AppliesPresetLineup() is true (no seats passed + a pin):
// claiming the preset supplies the lineup while also passing --seats was the
// false notice the reviewers flagged (FL-1).
//
// The preset name is a file-sourced registry slot (MaskSlot) and the lineup is a
// comma-separated seat/models TOKEN list rendered per token through the same
// registry-slot authority (fanout.MaskSeatTokens, which also strips the Cc/Cf
// class - FL-4/R3-item-2; MaskOperand is the identity on the underscore-head
// credential class), matching fanout's own preset notices. Benign names and specs
// keep their exact bytes.
func SeatLineupNotice(l fanout.SeatLineup) string {
	return fmt.Sprintf("seat lineup from fanout preset %q (%s); no seats were passed, so the preset's lineup applies",
		providers.MaskSlot(providers.StripSlotControls(l.PresetName)),
		fanout.MaskSeatTokens(l.Raw))
}

// FormatCommand renders a fanout argument vector as the equivalent
// `agy pro fanout ...` command line (preview, copy, --dry-run). Arguments that
// contain whitespace or quotes are double-quoted with inner quotes escaped.
//
// H5/R3-item-2: every rendered argument passes through a masker, so a key-shaped
// spec or prompt pasted into a flag slot cannot be echoed verbatim on the
// preview, copy and dry-run paths. The `--seats` VALUE is a
// comma-separated seat/model TOKEN list, so it takes the registry-slot authority
// (fanout.MaskSeatTokens): MaskOperand is provably the identity on the
// underscore-head credential class, and this line lands in stdout, the copy
// buffer and every dry-run transcript. Every other argument keeps the operand
// masker, and benign arguments (including benign lineups) render unchanged.
func FormatCommand(args []string) string {
	var b strings.Builder
	b.WriteString("agy pro fanout")
	for i, a := range args {
		b.WriteByte(' ')
		masked := maskArgvValue(args, i, a)
		if strings.ContainsAny(masked, " \t\"") {
			b.WriteString(strconv.Quote(masked))
		} else {
			b.WriteString(masked)
		}
	}
	return b.String()
}

// maskSeatValueFlags are the argv flags whose VALUE is a seat token list.
// The list is the single home of that knowledge, so FormatCommand and any sibling
// renderer cannot drift on which slot needs the stronger authority. `--seats` is
// the only seat source: the retired `--models` flag is rejected by the fanout
// parser, so no vector this builder produces can carry it.
var maskSeatValueFlags = map[string]bool{"--seats": true}

// maskArgvValue masks one argv element: the value of a seat-source flag takes the
// registry-slot authority per token, every other element the operand masker.
func maskArgvValue(args []string, i int, a string) string {
	if i > 0 && maskSeatValueFlags[args[i-1]] {
		return fanout.MaskSeatTokens(a)
	}
	return providers.MaskOperand(a)
}

// QuotaNote renders the per-seat provenance quota note (contract U3 flow):
// native seats draw on the free Antigravity quota, provider seats are metered.
func QuotaNote(kind string) string {
	if kind == "native" {
		return "free Antigravity quota (rate limits)"
	}
	return "metered API"
}

// SeatLine renders one selected seat row (provenance + quota note). E8/F-beta:
// Spec and ModelID come from the user-editable registry, so key-shaped ones
// render masked inside the row (display consistency with the summary table and
// the seat list), while benign specs stay readable.
//
// R3 (surfaced while pinning the preview path): the SPEC slot is a seat TOKEN
// from the registry, so it takes the registry-slot authority — the previous
// seeded MaskSentence pass was identity on the underscore-head credential class
// and rendered `provider/sk_live_...` raw right above the (already masked) preview
// line. The display name keeps the seeded sentence masker.
func SeatLine(num int, e inventory.Entry) string {
	return fmt.Sprintf("seat %d  [%s]  %s  %s  (%s)",
		num, e.Kind, fanout.MaskSeatToken(e.Spec), providers.MaskSlot(displayName(e)), QuotaNote(e.Kind))
}

func displayName(e inventory.Entry) string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.Capabilities
}
