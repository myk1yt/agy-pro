// Exact-diff renderer for the settings door.
//
// The settings door changes exactly one slot of one file, so its diff is not a
// file tree: it is the statusLine object before and after, the top-level keys
// that are NOT touched, and the file it all happens in. T8's CLI and TUI doors
// hand these lines to the same consent gate the deployment engine's plan feeds,
// which is why the rendering lives here, next to the plan, and stays plain text:
// no prompting, no colour, no i18n (T8 composes the sentences).
//
// Two shapes are rendered from the same template:
//
//   - (*StatusLineDoor).Lines() is the plan's diff: what create, update, adopt or
//     identical would do.
//   - StatusLineRemovalLines is the retract's diff: what removing the slot takes
//     away.
//
// Every operand goes through providers.MaskOperand, the display authority the
// rest of agy-pro routes operands through (deploy.go:169 is the same call for
// the deployment plan; mode.go:401 for the mode switch). Paths are exempt from
// masking by that authority, so the user sees the file they are about to change,
// while a key-shaped string that reached a command slot cannot ride out raw.
//
// The lines are bounded by construction: the ledger declares four slots, and the
// unmanaged-key sentence names at most statusLineUnmanagedPreview keys before it
// counts the rest.
package integrations

import (
	"fmt"
	"strings"

	"agypro/internal/agysettings"
	"agypro/internal/providers"
)

// statusLineFieldOrder is the ledger's declaration order (agysettings'
// statusLineConfigLedger), so a rendered object reads like the JSON it becomes.
var statusLineFieldOrder = []string{"type", "command", "enabled", "stack_with_default"}

// statusLineUnmanagedPreview caps how many unmanaged top-level keys one diff line
// names. A settings.json is a shared file; the sentence must stay one line even
// when a user has a dozen keys of their own.
const statusLineUnmanagedPreview = 8

// diffRow renders one labelled diff row, aligned the way the deployment plan
// aligns its action column (deploy.go Lines: "create     <path>").
func diffRow(label, text string) string { return fmt.Sprintf("%-11s%s", label, text) }

// slotRow renders one nested slot row under an action row.
func slotRow(slot, text string) string { return fmt.Sprintf("             %s: %s", slot, text) }

// Lines renders the exact diff of this plan.
//
//	file        <settings path>
//	create      statusLine
//	              type: command
//	              command: <absolute launcher> --hook --raw --write-dashboard
//	              enabled: true
//	              stack_with_default: true
//	unchanged   2 unmanaged top-level key(s) left byte-for-byte alone: colorScheme, trustedWorkspaces
//
// An update names only the slots that change, old value first. An adopt or an
// identical plan renders the object it keeps, because the point of those two
// actions is that nothing is written.
func (d *StatusLineDoor) Lines() []string {
	if d == nil {
		return nil
	}
	lines := []string{diffRow("file", providers.MaskOperand(d.SettingsPath))}

	switch d.Action {
	case StatusLineActionCreate:
		lines = append(lines, diffRow(string(d.Action), "statusLine"))
		for _, field := range statusLineFieldOrder {
			lines = append(lines, slotRow(field, providers.MaskOperand(statusLineFieldValue(&d.Desired, field))))
		}
	case StatusLineActionUpdate:
		lines = append(lines, diffRow(string(d.Action), "statusLine"))
		changed := 0
		for _, field := range statusLineFieldOrder {
			before := statusLineFieldValue(d.Current, field)
			after := statusLineFieldValue(&d.Desired, field)
			if before == after {
				continue
			}
			lines = append(lines, slotRow(field, fmt.Sprintf("%s -> %s",
				providers.MaskOperand(before), providers.MaskOperand(after))))
			changed++
		}
		if changed == 0 {
			// The plan says update but no declared slot differs: a door built by
			// hand, rendered honestly rather than as an empty block.
			lines = append(lines, slotRow("(no declared slot differs)", "the plan would rewrite identical bytes"))
		}
	case StatusLineActionAdopt:
		lines = append(lines, diffRow("adopt", "statusLine  (already the door's object: bytes untouched, bookkeeping records it)"))
		for _, field := range statusLineFieldOrder {
			lines = append(lines, slotRow(field, providers.MaskOperand(statusLineFieldValue(d.Current, field))))
		}
	case StatusLineActionIdentical:
		lines = append(lines, diffRow("identical", "statusLine  (present and already recorded: nothing to write)"))
		for _, field := range statusLineFieldOrder {
			lines = append(lines, slotRow(field, providers.MaskOperand(statusLineFieldValue(d.Current, field))))
		}
	default:
		lines = append(lines, diffRow(string(d.Action), "statusLine"))
	}

	lines = append(lines, unmanagedKeysLine(d.UnmanagedKeys))
	return lines
}

// StatusLineRemovalLines renders the exact diff of removing the slot (the retract
// verb). It takes the object found on disk rather than a plan, because a retract
// has no desired state to describe.
//
//	file        <settings path>
//	retract     statusLine  (the ledger archives the previous bytes first)
//	              command: <the command being removed>
//	              enabled: true
//	unchanged   2 unmanaged top-level key(s) left byte-for-byte alone: colorScheme, trustedWorkspaces
func StatusLineRemovalLines(settingsPath string, current *agysettings.StatusLineConfig, unmanagedKeys []string) []string {
	lines := []string{diffRow("file", providers.MaskOperand(settingsPath))}
	if current == nil {
		lines = append(lines, diffRow(string(StatusLineActionRetract), "statusLine  (absent: nothing to remove)"))
		return append(lines, unmanagedKeysLine(unmanagedKeys))
	}
	lines = append(lines, diffRow(string(StatusLineActionRetract), "statusLine  (the ledger archives the previous bytes first)"))
	for _, field := range statusLineFieldOrder {
		lines = append(lines, slotRow(field, providers.MaskOperand(statusLineFieldValue(current, field))))
	}
	return append(lines, unmanagedKeysLine(unmanagedKeys))
}

// statusLineFieldValue renders one slot of an object; "-" means the slot is
// absent (only stack_with_default can be).
func statusLineFieldValue(object *agysettings.StatusLineConfig, field string) string {
	if object == nil {
		return "-"
	}
	switch field {
	case "type":
		return object.Type
	case "command":
		return object.Command
	case "enabled":
		return fmt.Sprintf("%t", object.Enabled)
	case "stack_with_default":
		if object.StackWithDefault == nil {
			return "-"
		}
		return fmt.Sprintf("%t", *object.StackWithDefault)
	}
	return "-"
}

// unmanagedKeysLine is the sentence that keeps the diff honest about what it does
// not touch: every top-level key outside the ledger survives byte for byte.
func unmanagedKeysLine(keys []string) string {
	if len(keys) == 0 {
		return diffRow("unchanged", "(no unmanaged top-level keys)")
	}
	shown := keys
	suffix := ""
	if len(shown) > statusLineUnmanagedPreview {
		suffix = fmt.Sprintf(", +%d more", len(shown)-statusLineUnmanagedPreview)
		shown = shown[:statusLineUnmanagedPreview]
	}
	return diffRow("unchanged", fmt.Sprintf("%d unmanaged top-level key(s) left byte-for-byte alone: %s%s",
		len(keys), strings.Join(shown, ", "), suffix))
}
