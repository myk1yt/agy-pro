package launcher

import (
	"fmt"
	"io"
	"os"

	"agypro/internal/hybrid"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// P2 (subagent profile) launcher front door (design §2 P2, unit U9b).
//
// Profile P2 runs agy in gemini mode against a per-session mesh behind a
// per-session --app_data_dir. hybrid.LaunchP2 owns everything mechanical: the
// wirecheck gate, the mesh lifetime, the settings write, the child env and the
// exit-code map. This file owns only what the launcher uniquely knows - whether
// the invocation is a bare interactive session, and therefore whether the
// combined native+external picker must run BEFORE the launch.
//
// WHY THE PICKER MATTERS HERE: in a P2 session agy's in-session /model lists
// only its static Gemini defaults, and customModels entries never appear in
// `agy models` (W12). The launcher picker is the ONLY place an external can be
// chosen, so for a bare interactive session it runs first and pins the choice.
// Every other invocation shape carries the user's intent in its args already.

// runHybridP2 is the RouteHybridP2 arm.
//
// A BARE interactive session (no args at all AND stdin a TTY) gets the combined
// picker first and the pin appended; an aborted pin runs agy's own default
// static id under the mesh's static-id policy. EVERY other shape (-p/--print,
// an explicit --model/-m, the models/agents subcommands, any argument at all, a
// pipe/CI run) passes its args straight through: hybrid.LaunchP2 rewrites an
// explicit --model into the blade: map key (p2ModelKey) and a missing pin means
// agy's default static id.
//
// This arm NEVER falls through to the bridge/passthrough path: a P2 session is
// a mesh session or a native fallback LaunchP2 itself decides, never a gateway
// bridge session.
func runHybridP2(realAgyPath string, args []string, childStdin io.Reader, rel *stdinRelay, reg *providers.Registry) (int, error) {
	stdin := childStdin
	if p2ShowsPicker(args) {
		printP2CostPreamble(os.Stdout)
		// Same relay/console handling as the menu [2] picker: the console path
		// leaves the relay untouched, so the child inherits the console
		// directly; the relay path hands the child the relay pipe.
		pick := showModelPicker(os.Stdout, os.Stdin, rel, p2PickerInventory(reg))
		if rel != nil && rel.Used() {
			stdin = rel.ChildStdin()
		}
		if pick.Picked {
			args = p2PinArgs(args, pick)
		} else {
			printP2AbortNotice(os.Stderr)
		}
	}
	return hybrid.LaunchP2(hybrid.LaunchOptions{
		RealAgyPath: realAgyPath,
		Args:        args,
		Stdin:       stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	})
}

// p2PickerInventory returns the combined inventory filtered to the rows a P2
// session can actually serve (R5B-FIX-3).
//
// The combined inventory is the same one menu [2] renders, and it lists rows a
// P2 session cannot use: native rows outside the static Gemini ids (agy's gemini
// mode never lists them, so the pin would resolve to a blade: key the settings
// document does not carry) and a second provider's row for a cross-provider
// duplicate id (p2Externals keys the first provider only, so the pick would bill
// the wrong vendor). hybrid.P2PickerAllows mirrors that policy, so the picker
// and the settings document agree by construction.
//
// The rows are renumbered 1..N after filtering: inventory.ParseSelection
// resolves a typed number against the slice position, so a gap would mis-seat
// every row after it.
func p2PickerInventory(reg *providers.Registry) *inventory.Inventory {
	inv := pickerInventory(reg)
	if inv == nil {
		return &inventory.Inventory{}
	}
	allows := hybrid.P2PickerAllows(reg)
	entries := make([]inventory.Entry, 0, len(inv.Entries))
	for _, e := range inv.Entries {
		if allows(e.Kind, e.Group, e.ModelID) {
			entries = append(entries, e)
		}
	}
	for i := range entries {
		entries[i].Number = i + 1
	}
	return &inventory.Inventory{
		Entries:     entries,
		NativeErr:   inv.NativeErr,
		GeneratedAt: inv.GeneratedAt,
	}
}

// p2ShowsPicker reports whether the P2 picker runs for this invocation: a BARE
// session (no args at all) on an interactive stdin. Anything else is a shaped
// invocation whose args already carry the user's intent, and interrupting it
// with a menu would break the one-shot.
func p2ShowsPicker(args []string) bool {
	return len(args) == 0 && stdinIsTTY()
}

// p2PinArgs appends the pinned --model to the caller's args. The pin is UNIFORM
// for native and external picks (Entry.ModelID, never the display name):
// hybrid.LaunchP2 rewrites an external id into the blade: map key
// (p2ModelKey) and passes a native/static id through verbatim, so agy resolves
// it from its own static catalog under the mesh's static-id policy. An aborted
// pick appends nothing.
func p2PinArgs(args []string, pick ModelPick) []string {
	if !pick.Picked {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, args...)
	return append(out, "--model", pick.Entry.ModelID)
}

// printP2CostPreamble states the P2 costs BEFORE the picker, in the same honest
// wording `agy pro hybrid on p2` prints (hybrid.ProfileP2.Description) plus the
// three P2-specific consequences the picker rows themselves cannot show.
func printP2CostPreamble(out io.Writer) {
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  P2 (subagent profile) - choose the session model")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintf(out, "  Cost : %s\n", hybrid.ProfileP2.Description())
	fmt.Fprintln(out, "  - externals are selected HERE, at launch: agy's in-session /model lists only its static Gemini defaults in a P2 session")
	fmt.Fprintln(out, "  - subscription Gemini is OFF in this session (modelProvider is switched to gemini; traffic is billed to the configured provider)")
	fmt.Fprintln(out, "  - only agy's static Gemini ids are offered as native choices (the other native catalog rows cannot be served in a P2 session)")
	fmt.Fprintln(out, "  - a native pick is answered by the mesh's static-id policy: served only when that exact id is in the registry, otherwise a clear error naming the model (never a silent substitution)")
}

// printP2AbortNotice states what an aborted pick means, so the session that
// follows is never a surprise: no pin is sent, so agy runs its own default
// static id, which the mesh answers under the static-id policy (served only if
// that exact id is in the registry, otherwise a clear error naming it).
//
// It corrects the shared picker's own cancel line, which names the legacy [2]
// bridge fallback ("continuing with External Models (bridge)") because that
// picker is shared with the non-hybrid menu; on a P2 session no bridge runs.
func printP2AbortNotice(errw io.Writer) {
	fmt.Fprintln(errw, "[agy-pro] P2: no model selected; the session runs on agy's default static id, answered by the mesh's static-id policy (served only when that exact id is in the registry, otherwise a clear error naming the model - never a substitution)")
}
