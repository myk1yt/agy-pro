package settings

// P5-1 U5b: write-gate tests for the legacy `agy pro settings` panel's last four
// ungated write paths (user decision F3(a)): the state.json scalar choice rows
// (W20 -> Core.Set), the swarm preset append (W24 -> Core.SwarmSavePresets), and
// the two mode doors (W13 switch -> Core.ModeSwitch, W15 restore ->
// Core.ModeRestore).
//
// Every test drives the panel's own surface: fakeDeps for the Core verbs, the
// PRODUCTION path resolvers pointed at a temp home (isolatePanelModeDirs, so no
// test reads the live ~/.agy-pro/backups or ~/.gemini/config), and real files
// only under t.TempDir(). Harnesses reused read-only from the U5a file:
// newGateTUI, armWith, frameBody, newSwarmFileTUI, findItem, selectItemRow,
// ScriptedKeys/ScriptedLines and keyName.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/swarm"
	"agypro/internal/testfixtures"
)

// --- isolation harness ------------------------------------------------------

// isolatePanelModeDirs points every directory the panel's file plan resolves at a
// temp home, so a panel test that reaches a mode door never touches the live
// machine:
//
//	AGY_PRO_HOME       -> <tmp>                 (mode.ListBackups, swarm state path)
//	GEMINI_CONFIG_DIR  -> <tmp>/gemini-config    (panelConfigDir, the managed set)
//
// Both resolutions go through the production code the operator runs
// (paths.GeminiConfigDir, mode.ListBackups read those two variables), so the
// isolation exercises the real path rules instead of a test-only branch. The
// config directory is created EMPTY: every managed entry therefore reads
// "absent" until a test makes one appear, which is what the mode-plan pins and
// the managed-set drift pin rely on. The backups root is created empty too, so a
// restore door refuses ("no previous backups found to restore") until
// seedPanelBackup lays one down.
func isolatePanelModeDirs(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, "gemini-config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatalf("create isolated config dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "backups"), 0o755); err != nil {
		t.Fatalf("create isolated backups root: %v", err)
	}
	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(paths.EnvGeminiConfigDir, cfg)
}

// seedPanelBackup lays down one timestamped, restorable backup under the isolated
// AGY_PRO_HOME backups root and returns its directory. files maps a
// slash-separated relative path to its body, so a test can state exactly what a
// restore would copy (and what a stale-source refusal notices changing).
func seedPanelBackup(t *testing.T, files map[string]string) string {
	t.Helper()
	if os.Getenv(paths.EnvAgyProHome) == "" {
		// Isolation must already be in place: without AGY_PRO_HOME the resolver
		// answers with the LIVE ~/.agy-pro and this helper would seed a fake
		// backup into the operator's own backups root.
		t.Fatal("seedPanelBackup requires isolatePanelModeDirs first (AGY_PRO_HOME is unset)")
	}
	base, err := paths.AgyProBackups()
	if err != nil {
		t.Fatalf("resolve isolated backups root: %v", err)
	}
	dir := filepath.Join(base, "20260101_000000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create backup subdir for %q: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write backup file %q: %v", rel, err)
		}
	}
	return dir
}

// panelConfigPath is the isolated live config directory the mode plan stats.
func panelConfigPath(t *testing.T) string {
	t.Helper()
	dir, err := panelConfigDir()
	if err != nil || dir == "" {
		t.Fatalf("panelConfigDir after isolation = (%q, %v)", dir, err)
	}
	return dir
}

// managedLine rebuilds one managed-set body line: the name column is 14 wide and
// is always followed by exactly one space before the state word
// (paneldiff.go panelModeEntryLine).
func managedLine(name, state string) string {
	pad := 14 - len(name)
	if pad < 0 {
		pad = 0
	}
	return "  " + name + strings.Repeat(" ", pad+1) + state
}

// assertBodyLines is the byte pin for a composed gate body: element-wise
// equality, so a dropped, added, reordered or reworded line fails here.
func assertBodyLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("gate body has %d lines; want %d\ngot:\n  %s\nwant:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gate body line %d drifted:\ngot:  %q\nwant: %q", i+1, got[i], want[i])
		}
	}
}

// itemAtCursor is the row the panel cursor currently points at.
func itemAtCursor(tui *TUI) Item { return tui.view.Sections[tui.section].Items[tui.row] }

// armChoice presses the row, then the numbered choice that starts its write.
func armChoice(t *testing.T, tui *TUI, id string, choice int) {
	t.Helper()
	findItem(t, tui, id)
	tui.Step(KeyEnter)
	tui.Step(Digit(choice))
}

// --- W20: state.json scalar choice rows -------------------------------------

// scalarRow is one W20 door under test: the row id, the state.json key the write
// touches, the numbered choice that triggers it, and the persisted field.
type scalarRow struct {
	id      string
	jsonKey string
	choice  int
	before  string
	after   string
	read    func(f *fakeDeps) string
}

func stateScalarRows() []scalarRow {
	return []scalarRow{
		{
			id: "default_profile", jsonKey: "default_profile", choice: 2,
			before: mode.ProfilePrompt, after: mode.ProfileNative,
			read: func(f *fakeDeps) string { return f.state.DefaultProfile },
		},
		{
			id: "bridge_tools", jsonKey: "bridge_tools", choice: 2,
			before: "read,search", after: "read,search,write",
			read: func(f *fakeDeps) string { return f.state.BridgeTools },
		},
		{
			id: "native_seat_concurrency", jsonKey: "native_seat_concurrency", choice: 5,
			before: "2", after: "5",
			read: func(f *fakeDeps) string { return strconv.Itoa(f.state.NativeSeatConcurrency) },
		},
	}
}

// TestU5bStateScalarGatesRequireConsent is the W20 consent matrix: pressing a
// choice arms the exact-diff gate and writes nothing, n and Esc cancel with
// nothing written, and y is the only path to Core.Set.
func TestU5bStateScalarGatesRequireConsent(t *testing.T) {
	for _, r := range stateScalarRows() {
		t.Run(r.id+"/arms without writing", func(t *testing.T) {
			tui, f := newGateTUI(t)
			armChoice(t, tui, r.id, r.choice)
			if tui.pending == nil {
				t.Fatalf("%s did not arm the consent gate: %q", r.id, tui.message)
			}
			if tui.pending.item.ID != r.id {
				t.Errorf("gate item = %q; want %q", tui.pending.item.ID, r.id)
			}
			if f.saved != 0 {
				t.Fatalf("%s wrote before consent: %d saves", r.id, f.saved)
			}
			if v := r.read(f); v != r.before {
				t.Errorf("%s = %q before consent; want %q", r.id, v, r.before)
			}
			if !strings.Contains(tui.message, "confirm to proceed") {
				t.Errorf("message = %q; want the consent prompt", tui.message)
			}
			wantLine := fmt.Sprintf("  %s: %s -> %s", r.jsonKey, r.before, r.after)
			if body := strings.Join(tui.pending.body, "\n"); !strings.Contains(body, wantLine) {
				t.Errorf("gate body missing the old -> new line %q:\n%s", wantLine, body)
			}
		})

		for _, answer := range []Key{KeyNo, KeyEsc} {
			t.Run(r.id+"/"+keyName(answer)+" cancels", func(t *testing.T) {
				tui, f := newGateTUI(t)
				armChoice(t, tui, r.id, r.choice)
				if tui.pending == nil {
					t.Fatalf("%s did not arm the gate", r.id)
				}
				tui.Step(answer)
				if tui.pending != nil {
					t.Fatal("the declined gate stayed armed")
				}
				if f.saved != 0 {
					t.Fatalf("%s wrote on %s: %d saves", r.id, keyName(answer), f.saved)
				}
				if v := r.read(f); v != r.before {
					t.Errorf("%s = %q after a cancel; want %q", r.id, v, r.before)
				}
				if tui.message != "cancelled" {
					t.Errorf("declined message = %q; want cancelled", tui.message)
				}
			})
		}

		t.Run(r.id+"/y applies exactly one write", func(t *testing.T) {
			tui, f := newGateTUI(t)
			armChoice(t, tui, r.id, r.choice)
			if tui.pending == nil {
				t.Fatalf("%s did not arm the gate", r.id)
			}
			tui.Step(KeyYes)
			if f.saved != 1 {
				t.Fatalf("%s saves after consent = %d; want exactly 1", r.id, f.saved)
			}
			if v := r.read(f); v != r.after {
				t.Errorf("%s = %q after consent; want %q", r.id, v, r.after)
			}
			if !strings.HasPrefix(tui.message, "[SUCCESS]") {
				t.Errorf("approved gate message = %q; want the success stamp", tui.message)
			}
		})
	}
}

// TestU5bStateScalarGateBodyBytePins pins the W20 presentation itself: the diff
// names the one state.json row the write touches, states that nothing else
// changes, carries the exact old -> new line, and ends with the atomic-0600
// sentence for the file it rewrites. The frame must render every composed line
// verbatim and ABOVE the y/n prompt, so the approval describes the write it
// authorizes.
func TestU5bStateScalarGateBodyBytePins(t *testing.T) {
	cases := []struct {
		row   scalarRow
		extra string
	}{}
	for _, r := range stateScalarRows() {
		cases = append(cases, struct {
			row   scalarRow
			extra string
		}{row: r})
	}
	for _, c := range cases {
		r := c.row
		t.Run(r.id, func(t *testing.T) {
			tui, _ := newGateTUI(t)
			armChoice(t, tui, r.id, r.choice)
			if tui.pending == nil {
				t.Fatalf("%s did not arm the gate", r.id)
			}
			assertBodyLines(t, tui.pending.body,
				fmt.Sprintf("Sets %s in state.json. No other key changes.", r.jsonKey),
				fmt.Sprintf("  %s: %s -> %s", r.jsonKey, r.before, r.after),
				"state.json will be written (0600, atomic) with this exact diff:",
			)
			right := frameBody(tui)
			for _, ln := range tui.pending.body {
				if !strings.Contains(right, "    "+ln) {
					t.Errorf("the frame does not render the composed line verbatim: %q\n%s", ln, right)
				}
			}
			diffAt := strings.Index(right, fmt.Sprintf("  %s: %s -> %s", r.jsonKey, r.before, r.after))
			promptAt := strings.Index(right, "Confirm ")
			if diffAt < 0 || promptAt < 0 || diffAt > promptAt {
				t.Errorf("the diff does not precede the prompt (diff=%d, prompt=%d):\n%s", diffAt, promptAt, right)
			}
		})
	}
}

// TestU5bStateScalarGateStaleBaseRefuses pins the write-time re-verify: the row
// is signed at presentation, so an external edit under the prompt refuses the
// write instead of overwriting what the operator never saw.
func TestU5bStateScalarGateStaleBaseRefuses(t *testing.T) {
	t.Run("default_profile changed under the prompt", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armChoice(t, tui, "default_profile", 2)
		if tui.pending == nil {
			t.Fatal("default_profile did not arm the gate")
		}
		f.state.DefaultProfile = mode.ProfileGateway
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a stale scalar write landed: %d saves", f.saved)
		}
		if f.state.DefaultProfile != mode.ProfileGateway {
			t.Errorf("the refusal reverted the external edit: %q", f.state.DefaultProfile)
		}
		for _, want := range []string{"default_profile", "changed on disk", "stale", "nothing written"} {
			if !strings.Contains(tui.message, want) {
				t.Errorf("refusal message missing %q: %q", want, tui.message)
			}
		}
	})

	t.Run("native_seat_concurrency changed under the prompt", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armChoice(t, tui, "native_seat_concurrency", 5)
		if tui.pending == nil {
			t.Fatal("native_seat_concurrency did not arm the gate")
		}
		f.state.NativeSeatConcurrency = 3
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a stale concurrency write landed: %d saves", f.saved)
		}
		if f.state.NativeSeatConcurrency != 3 {
			t.Errorf("the refusal reverted the external edit: %d", f.state.NativeSeatConcurrency)
		}
		if !strings.Contains(tui.message, "native_seat_concurrency") || !strings.Contains(tui.message, "stale") {
			t.Errorf("refusal message = %q; want the row named as stale", tui.message)
		}
	})
}

// TestU5bStateScalarGateMasksKeyShapedValues: the value slot is a user paste, so
// a credential-shaped value renders masked in the diff, and the store's refusal
// (the value is outside the closed set) reaches the frame masked too.
//
// The plant is AnthropicPlanted, NOT testfixtures.SKLive: SKLive is deliberately
// NOT providers.SeedEligible (probed: SeedEligible=false, MaskOperand=identity,
// MaskSlot="sk_li****"), so it only exercises the slot authority. The value also
// rides the forwarded store sentence through the seed list, and this test pins
// both halves, so it needs a plant the shared seed rule recognises.
func TestU5bStateScalarGateMasksKeyShapedValues(t *testing.T) {
	plant := testfixtures.AnthropicPlanted
	tui, f := newGateTUI(t)
	findItem(t, tui, "default_profile")
	if err := tui.setScalar(itemAtCursor(tui), "default-profile", plant); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if tui.pending == nil {
		t.Fatal("the direct scalar arm did not open the gate")
	}
	body := strings.Join(tui.pending.body, "\n")
	if strings.Contains(body, plant) {
		t.Errorf("the diff prints the key-shaped value raw:\n%s", body)
	}
	if !strings.Contains(body, panelSlot(plant)) {
		t.Errorf("the diff does not carry the slot-masked value:\n%s", body)
	}

	tui.Step(KeyYes)
	if f.saved != 0 {
		t.Fatalf("an out-of-set value wrote state: %d saves", f.saved)
	}
	if strings.Contains(tui.message, plant) {
		t.Errorf("the forwarded store refusal leaks the value raw: %q", tui.message)
	}
	if !strings.Contains(tui.message, "invalid default profile") {
		t.Errorf("the store's own refusal sentence is gone: %q", tui.message)
	}
}

// TestU5bStateScalarGateStatesTheUnknownRow: a key outside the panel's closed
// table has no old -> new line to print, so the body says exactly that (and
// renders the key masked) while the store owns the refusal after consent.
func TestU5bStateScalarGateStatesTheUnknownRow(t *testing.T) {
	plant := testfixtures.AnthropicPlanted
	tui, f := newGateTUI(t)
	findItem(t, tui, "default_profile")
	if err := tui.setScalar(itemAtCursor(tui), plant, "ko"); err != nil {
		t.Fatalf("setScalar: %v", err)
	}
	if tui.pending == nil {
		t.Fatal("an unknown row skipped the gate")
	}
	body := strings.Join(tui.pending.body, "\n")
	want := "  " + panelSlot(plant) + " is not a row this panel writes; the store refuses unknown keys."
	if !strings.Contains(body, want) {
		t.Errorf("the body does not state the unknown row:\ngot:\n%s\nwant line: %q", body, want)
	}
	if strings.Contains(body, plant) {
		t.Errorf("the body prints the key-shaped row id raw:\n%s", body)
	}

	tui.Step(KeyYes)
	if f.saved != 0 {
		t.Fatalf("an unknown key wrote state: %d saves", f.saved)
	}
	if !strings.Contains(tui.message, "unknown settings key") {
		t.Errorf("the store's unknown-key refusal is gone: %q", tui.message)
	}
	if strings.Contains(tui.message, plant) {
		t.Errorf("the forwarded refusal leaks the row id raw: %q", tui.message)
	}
}

// TestU5bStateScalarGateUnreadExitWritesNothing: an input that ends while the
// gate is open (the scripted-key loop and the real line-mode pipe) writes
// nothing, and the transcript still carries the diff and the open prompt.
func TestU5bStateScalarGateUnreadExitWritesNothing(t *testing.T) {
	t.Run("exhausted key source", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armChoice(t, tui, "default_profile", 2)
		if tui.pending == nil {
			t.Fatal("default_profile did not arm the gate")
		}
		tui.keys = &ScriptedKeys{} // no further key: the loop exits unread
		if code := tui.Run(); code != 0 {
			t.Fatalf("Run exit = %d; want 0", code)
		}
		if f.saved != 0 {
			t.Fatalf("an unread exit wrote state: %d saves", f.saved)
		}
		if f.state.DefaultProfile != mode.ProfilePrompt {
			t.Errorf("an unread exit changed the profile: %q", f.state.DefaultProfile)
		}
	})

	t.Run("line-mode pipe hits EOF at the gate", func(t *testing.T) {
		isolatePanelModeDirs(t)
		f := newFakeDeps()
		f.state.ManagedItems = mode.DefaultManagedItems
		c := NewCore(f.deps())
		out := &bytes.Buffer{}
		if code := RunTUI(c, strings.NewReader("1\n2\n"), out, ""); code != 0 {
			t.Fatalf("RunTUI exit = %d; want 0", code)
		}
		if f.saved != 0 {
			t.Fatalf("EOF at the scalar gate wrote state: %d saves", f.saved)
		}
		transcript := out.String()
		for _, want := range []string{
			"default_profile: prompt -> native",
			"Confirm Default Launch Profile? [y/n]",
		} {
			if !strings.Contains(transcript, want) {
				t.Errorf("line-mode transcript missing %q:\n%s", want, transcript)
			}
		}
	})
}

// --- W24: swarm preset append -----------------------------------------------

// TestU5bPresetAppendGateBodyBytePins pins the W24 presentation: the append names
// the preset it adds, counts before/after, lists the presets it keeps (names
// only, they are other people's numbers), shows the exact "+ preset name =
// numbers" line and the atomic-0600 sentence for the file it rewrites.
func TestU5bPresetAppendGateBodyBytePins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	fs := &swarm.FileStore{Path: path}
	if err := fs.SavePresets([]swarm.SavedPreset{{Name: "existing", Numbers: "1"}}); err != nil {
		t.Fatal(err)
	}
	tui, _, _ := newSwarmFileTUI(t, path)
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatalf("a healthy append did not arm the consent gate: %q", tui.message)
	}
	if tui.pending.notice != "" {
		t.Errorf("a healthy store carried the corrupt-overwrite notice: %q", tui.pending.notice)
	}
	assertBodyLines(t, tui.pending.body,
		"Appends preset deep-review to swarm-state.json. No existing preset changes.",
		"  presets before: 1, after: 2",
		"  existing presets (kept byte-identical):",
		"  preset existing",
		"+ preset deep-review = 2,5",
		"swarm-state.json will be written (0600, atomic) with this exact diff:",
	)
	right := frameBody(tui)
	for _, ln := range tui.pending.body {
		if !strings.Contains(right, "    "+ln) {
			t.Errorf("the frame does not render the composed line verbatim: %q\n%s", ln, right)
		}
	}
	if diffAt, promptAt := strings.Index(right, "+ preset deep-review = 2,5"), strings.Index(right, "Confirm "); diffAt < 0 || promptAt < 0 || diffAt > promptAt {
		t.Errorf("the append line does not precede the prompt (diff=%d, prompt=%d):\n%s", diffAt, promptAt, right)
	}
	if presets, err := fs.LoadPresets(); err != nil || len(presets) != 1 {
		t.Fatalf("the gate wrote before it was answered: %+v (%v)", presets, err)
	}
}

// TestU5bPresetAppendGateBoundedBody pins the bounded-body rule on the one
// list-shaped diff in this class: at most panelDiffEntryBudget-1 existing presets
// render and ONE tail line names exactly how many were withheld.
func TestU5bPresetAppendGateBoundedBody(t *testing.T) {
	tui, f := newGateTUI(t)
	const existing = panelDiffEntryBudget + 5
	for i := 1; i <= existing; i++ {
		f.store.presets = append(f.store.presets, swarm.SavedPreset{
			Name:    fmt.Sprintf("saved-%02d", i),
			Numbers: strconv.Itoa(i),
		})
	}
	armWith(t, tui, "swarm.save_preset", "late", "9")
	if tui.pending == nil {
		t.Fatal("the append did not arm the gate")
	}
	body := tui.pending.body
	visible := panelDiffEntryBudget - 1
	wantLines := 3 + visible + 1 + 2
	if len(body) != wantLines {
		t.Fatalf("bounded body = %d lines; want %d:\n  %s", len(body), wantLines, strings.Join(body, "\n  "))
	}
	if want := fmt.Sprintf("  presets before: %d, after: %d", existing, existing+1); body[1] != want {
		t.Errorf("count line = %q; want %q", body[1], want)
	}
	if want := "  preset saved-01"; body[3] != want {
		t.Errorf("first kept entry = %q; want %q", body[3], want)
	}
	if want := "  preset saved-" + fmt.Sprintf("%02d", visible); body[3+visible-1] != want {
		t.Errorf("last visible entry = %q; want %q", body[3+visible-1], want)
	}
	tail := fmt.Sprintf(panelDiffEntriesHidden, existing-visible)
	if got := body[3+visible]; got != tail {
		t.Errorf("bounded tail = %q; want %q", got, tail)
	}
	if joined := strings.Join(body, "\n"); strings.Contains(joined, "saved-20") {
		t.Errorf("an entry past the budget rendered anyway:\n%s", joined)
	}
	if !strings.Contains(strings.Join(body, "\n"), "+ preset late = 9") {
		t.Errorf("the added entry is missing from the bounded body:\n%s", strings.Join(body, "\n"))
	}
}

// TestU5bPresetAppendGateRequiresConsent is the W24 consent matrix: the append
// waits for y, n and Esc cancel with the file byte-identical, and an unread exit
// appends nothing.
func TestU5bPresetAppendGateRequiresConsent(t *testing.T) {
	seed := func(t *testing.T) (string, *swarm.FileStore) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "swarm-state.json")
		fs := &swarm.FileStore{Path: path}
		if err := fs.SavePresets([]swarm.SavedPreset{{Name: "existing", Numbers: "1"}}); err != nil {
			t.Fatal(err)
		}
		return path, fs
	}

	for _, answer := range []Key{KeyNo, KeyEsc} {
		t.Run(keyName(answer)+" cancels", func(t *testing.T) {
			path, fs := seed(t)
			tui, _, _ := newSwarmFileTUI(t, path)
			selectItemRow(t, tui, "swarm.save_preset")
			tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
			tui.Step(KeyEnter)
			if tui.pending == nil {
				t.Fatal("the append did not arm the gate")
			}
			tui.Step(answer)
			if tui.pending != nil {
				t.Fatal("the declined gate stayed armed")
			}
			presets, err := fs.LoadPresets()
			if err != nil || len(presets) != 1 || presets[0].Name != "existing" {
				t.Fatalf("a declined append changed the file: %+v (%v)", presets, err)
			}
			if tui.message != "cancelled" {
				t.Errorf("declined message = %q; want cancelled", tui.message)
			}
		})
	}

	t.Run("y appends the presented entry", func(t *testing.T) {
		path, fs := seed(t)
		tui, _, _ := newSwarmFileTUI(t, path)
		selectItemRow(t, tui, "swarm.save_preset")
		tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
		tui.Step(KeyEnter)
		if tui.pending == nil {
			t.Fatal("the append did not arm the gate")
		}
		tui.Step(KeyYes)
		presets, err := fs.LoadPresets()
		if err != nil {
			t.Fatalf("LoadPresets: %v", err)
		}
		if len(presets) != 2 || presets[1].Name != "deep-review" || presets[1].Numbers != "2,5" {
			t.Fatalf("on-disk presets = %+v; want existing + deep-review", presets)
		}
		if !strings.HasPrefix(tui.message, "[SUCCESS]") {
			t.Errorf("approved append message = %q; want the success stamp", tui.message)
		}
	})

	t.Run("unread exit appends nothing", func(t *testing.T) {
		path, fs := seed(t)
		tui, _, _ := newSwarmFileTUI(t, path)
		selectItemRow(t, tui, "swarm.save_preset")
		tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
		tui.Step(KeyEnter)
		if tui.pending == nil {
			t.Fatal("the append did not arm the gate")
		}
		tui.keys = &ScriptedKeys{}
		if code := tui.Run(); code != 0 {
			t.Fatalf("Run exit = %d; want 0", code)
		}
		presets, err := fs.LoadPresets()
		if err != nil || len(presets) != 1 || presets[0].Name != "existing" {
			t.Fatalf("an unread exit appended: %+v (%v)", presets, err)
		}
	})
}

// TestU5bPresetAppendGateStaleBaseRefuses: the preset list is the signed state,
// so a preset saved under the prompt refuses the append instead of being
// duplicated, dropped or overwritten.
func TestU5bPresetAppendGateStaleBaseRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	fs := &swarm.FileStore{Path: path}
	if err := fs.SavePresets([]swarm.SavedPreset{{Name: "existing", Numbers: "1"}}); err != nil {
		t.Fatal(err)
	}
	tui, _, _ := newSwarmFileTUI(t, path)
	selectItemRow(t, tui, "swarm.save_preset")
	tui.lines = &ScriptedLines{Lines: []string{"deep-review", "2,5"}}
	tui.Step(KeyEnter)
	if tui.pending == nil {
		t.Fatal("the append did not arm the gate")
	}
	if err := fs.SavePresets([]swarm.SavedPreset{
		{Name: "existing", Numbers: "1"},
		{Name: "late", Numbers: "9"},
	}); err != nil {
		t.Fatal(err)
	}
	tui.Step(KeyYes)
	presets, err := fs.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	if len(presets) != 2 || presets[1].Name != "late" {
		t.Fatalf("a stale append ran: %+v", presets)
	}
	for _, want := range []string{"stale", "the preset count moved from 1 to 2", "nothing written"} {
		if !strings.Contains(tui.message, want) {
			t.Errorf("refusal message missing %q: %q", want, tui.message)
		}
	}
}

// --- W13: the mode switch ---------------------------------------------------

// TestU5bModeSwitchGatePresentsFilePlan pins the W13 body: the panel presents
// U2's deployment plan (mode transition, config dir, managed set with its on-disk
// state, deployment groups, the current_mode row) instead of a bare label.
func TestU5bModeSwitchGatePresentsFilePlan(t *testing.T) {
	t.Run("current_mode row", func(t *testing.T) {
		tui, f := newGateTUI(t)
		cfg := panelConfigPath(t)
		armChoice(t, tui, "current_mode", 1) // full
		if tui.pending == nil {
			t.Fatalf("the mode switch did not arm the gate: %q", tui.message)
		}
		if tui.pending.item.ID != "current_mode" {
			t.Errorf("gate item = %q; want current_mode", tui.pending.item.ID)
		}
		if len(f.switched) != 0 {
			t.Fatalf("the switch ran before consent: %v", f.switched)
		}
		assertModeSwitchPlan(t, tui.pending.body, cfg)
		if diffAt, promptAt := strings.Index(frameBody(tui), "Switches the active profile: clean -> full"), strings.Index(frameBody(tui), "Confirm "); diffAt < 0 || promptAt < 0 || diffAt > promptAt {
			t.Errorf("the plan does not precede the prompt (plan=%d, prompt=%d):\n%s", diffAt, promptAt, frameBody(tui))
		}
	})

	t.Run("mode.switch row", func(t *testing.T) {
		tui, _ := newGateTUI(t)
		cfg := panelConfigPath(t)
		armChoice(t, tui, "mode.switch", 1)
		if tui.pending == nil {
			t.Fatalf("the mode.switch row did not arm the gate: %q", tui.message)
		}
		if tui.pending.item.ID != "mode.switch" {
			t.Errorf("gate item = %q; want mode.switch", tui.pending.item.ID)
		}
		assertModeSwitchPlan(t, tui.pending.body, cfg)
	})

	t.Run("a managed entry already on disk reads present", func(t *testing.T) {
		tui, _ := newGateTUI(t)
		cfg := panelConfigPath(t)
		// agents is a mode-owned entry; rules/ would not appear here at all
		// after the P5-3 partition (see TestU5bModePlanLeavesIntegrationsOwned
		// TreesOffTheManagedSet).
		if err := os.MkdirAll(filepath.Join(cfg, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		armChoice(t, tui, "current_mode", 1)
		if tui.pending == nil {
			t.Fatal("the mode switch did not arm the gate")
		}
		body := strings.Join(tui.pending.body, "\n")
		if want := managedLine("agents", "present (backed up)"); !strings.Contains(body, want) {
			t.Errorf("the plan does not mark the present entry %q:\n%s", want, body)
		}
	})
}

// TestU5bModePlanLeavesIntegrationsOwnedTreesOffTheManagedSet pins the P5-3
// destination partition on the surface a user actually approves: the mode
// switch file plan. The managed rows must be exactly mode.DefaultManagedItems,
// and rules/skills/plugins/hooks must have no managed row at all even when a
// rules/ tree is sitting in the config dir - those destinations belong to the
// integrations engine, so the mode plan must not offer to back them up or remove
// them.
//
// P5-3 T7 closed the interim gap: the embedded full tree no longer carries the
// three integrations-owned trees, so the deploy group table is the mode-owned
// set only - agents (10), config.json (1), profile.json (1), references (1), 13
// files in 4 groups, down from 64 files in 7. The agents count moved 9 -> 10 in
// P33, when both embedded profiles started shipping the native-search asset
// (agents/agypro-search/AGENT.md): the plan counts the embedded profile, so a
// content addition is expected to move this line.
func TestU5bModePlanLeavesIntegrationsOwnedTreesOffTheManagedSet(t *testing.T) {
	tui, _ := newGateTUI(t)
	cfg := panelConfigPath(t)
	if err := os.MkdirAll(filepath.Join(cfg, "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	armChoice(t, tui, "current_mode", 1)
	if tui.pending == nil {
		t.Fatal("the mode switch did not arm the gate")
	}
	body := strings.Join(tui.pending.body, "\n")

	// No managed row for an integrations-owned destination.
	for _, moved := range []string{"rules", "skills", "plugins", "hooks"} {
		for _, state := range []string{"absent", "present (backed up)"} {
			if want := managedLine(moved, state); strings.Contains(body, want) {
				t.Errorf("the switch plan still presents %q as a managed entry (%q):\n%s", moved, want, body)
			}
		}
	}
	// Every register entry does have one, in register order.
	rows := strings.Split(body, "\n")
	var managed []string
	inManaged := false
	for _, row := range rows {
		if strings.HasPrefix(row, "Deploys ") {
			inManaged = false
		}
		if inManaged {
			name := strings.Fields(row)
			if len(name) > 0 {
				managed = append(managed, name[0])
			}
			continue
		}
		if strings.HasPrefix(row, "Managed entries move to a timestamped backup") {
			inManaged = true
		}
	}
	if len(managed) != len(mode.DefaultManagedItems) {
		t.Fatalf("the plan presents %d managed entries (%v); want %d (%v)",
			len(managed), managed, len(mode.DefaultManagedItems), mode.DefaultManagedItems)
	}
	for i, name := range mode.DefaultManagedItems {
		if managed[i] != name {
			t.Errorf("managed row %d = %q; want %q", i+1, managed[i], name)
		}
	}

	// The deploy groups, pinned exactly: the mode-owned set only (P5-3 T7).
	if want := "Deploys 13 files from the embedded full profile:"; !strings.Contains(body, want) {
		t.Errorf("deployment total drifted:\n%s", body)
	}
	for _, want := range []string{
		"  agents (10 files)",
		"  config.json (1 file)",
		"  profile.json (1 file)",
		"  references (1 file)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deployment group %q missing from the plan:\n%s", want, body)
		}
	}
	// No integrations-owned group at all: after T7 the profile carries no such
	// directory, so the walk cannot offer one.
	for _, gone := range []string{"  rules (", "  skills (", "  plugins ("} {
		if strings.Contains(body, gone) {
			t.Errorf("the switch plan still deploys an integrations-owned group (%q):\n%s", gone, body)
		}
	}
}

// assertModeSwitchPlan pins the switch body line by line: intro, config dir, the
// backup sentence, one line per managed entry (all absent in an isolated config
// dir), the deployment groups, then the atomic sentence and the current_mode row.
func assertModeSwitchPlan(t *testing.T, body []string, cfg string) {
	t.Helper()
	head := []string{
		"Switches the active profile: clean -> full",
		"Config dir: " + cfg,
		"Managed entries move to a timestamped backup under the agy-pro backups directory, then the new profile is deployed:",
	}
	if len(body) < len(head)+len(mode.DefaultManagedItems)+3 {
		t.Fatalf("switch plan has %d lines, too few:\n  %s", len(body), strings.Join(body, "\n  "))
	}
	for i, want := range head {
		if body[i] != want {
			t.Errorf("plan line %d drifted:\ngot:  %q\nwant: %q", i+1, body[i], want)
		}
	}
	for i, name := range mode.DefaultManagedItems {
		if want := managedLine(name, "absent"); body[len(head)+i] != want {
			t.Errorf("managed entry line %d drifted:\ngot:  %q\nwant: %q", i+1, body[len(head)+i], want)
		}
	}
	deploy := len(head) + len(mode.DefaultManagedItems)
	if !strings.HasPrefix(body[deploy], "Deploys ") || !strings.HasSuffix(body[deploy], " files from the embedded full profile:") {
		t.Errorf("deployment intro = %q; want the U2 file-plan sentence", body[deploy])
	}
	if !strings.Contains(strings.Join(body[deploy:], "\n"), "  agents (10 files)") {
		t.Errorf("deployment groups missing the agents group:\n%s", strings.Join(body[deploy:], "\n"))
	}
	if n := len(body); body[n-2] != "state.json will be written (0600, atomic) with the mode change:" || body[n-1] != "  current_mode: clean -> full" {
		t.Errorf("plan tail drifted:\n  %q\n  %q", body[n-2], body[n-1])
	}
}

// TestU5bModeSwitchGateRequiresConsent is the W13 consent matrix over both mode
// rows: n/Esc cancel without deploying, y hands the presented target to the
// switch.
func TestU5bModeSwitchGateRequiresConsent(t *testing.T) {
	for _, id := range []string{"current_mode", "mode.switch"} {
		for _, answer := range []Key{KeyNo, KeyEsc} {
			t.Run(id+"/"+keyName(answer)+" cancels", func(t *testing.T) {
				tui, f := newGateTUI(t)
				armChoice(t, tui, id, 1)
				if tui.pending == nil {
					t.Fatalf("%s did not arm the gate", id)
				}
				tui.Step(answer)
				if len(f.switched) != 0 {
					t.Fatalf("%s deployed on %s: %v", id, keyName(answer), f.switched)
				}
				if tui.message != "cancelled" {
					t.Errorf("declined message = %q; want cancelled", tui.message)
				}
			})
		}

		t.Run(id+"/y deploys the presented target", func(t *testing.T) {
			tui, f := newGateTUI(t)
			armChoice(t, tui, id, 1)
			if tui.pending == nil {
				t.Fatalf("%s did not arm the gate", id)
			}
			tui.Step(KeyYes)
			if len(f.switched) != 1 || f.switched[0] != mode.ModeFull {
				t.Fatalf("%s handed the switch %v; want [full]", id, f.switched)
			}
			if !strings.HasPrefix(tui.message, "[SUCCESS]") {
				t.Errorf("approved gate message = %q; want the success stamp", tui.message)
			}
		})
	}
}

// TestU5bModeSwitchGateStaleBaseRefuses pins the two signed halves of the switch
// plan: the current mode and the on-disk managed set. Either moving under the
// prompt refuses before the deployment starts.
func TestU5bModeSwitchGateStaleBaseRefuses(t *testing.T) {
	t.Run("current_mode moved", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armChoice(t, tui, "current_mode", 1)
		if tui.pending == nil {
			t.Fatal("the mode switch did not arm the gate")
		}
		f.state.CurrentMode = mode.ModeFull
		tui.Step(KeyYes)
		if len(f.switched) != 0 {
			t.Fatalf("a stale switch deployed: %v", f.switched)
		}
		for _, want := range []string{"current_mode", "changed on disk", "stale", "nothing written"} {
			if !strings.Contains(tui.message, want) {
				t.Errorf("refusal message missing %q: %q", want, tui.message)
			}
		}
	})

	t.Run("managed entry appeared", func(t *testing.T) {
		tui, f := newGateTUI(t)
		cfg := panelConfigPath(t)
		armChoice(t, tui, "current_mode", 1)
		if tui.pending == nil {
			t.Fatal("the mode switch did not arm the gate")
		}
		// agents is a mode-owned entry; a rules/ tree appearing under the
		// prompt is no longer a managed-set drift (P5-3 partition).
		if err := os.MkdirAll(filepath.Join(cfg, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		tui.Step(KeyYes)
		if len(f.switched) != 0 {
			t.Fatalf("a switch over a drifted managed set deployed: %v", f.switched)
		}
		for _, want := range []string{`managed entry "agents"`, "stale", "nothing written"} {
			if !strings.Contains(tui.message, want) {
				t.Errorf("refusal message missing %q: %q", want, tui.message)
			}
		}
	})
}

// TestU5bModeSwitchGateUnrecognizedTargetStillGated: a target the plan cannot
// resolve is still gated (no ungated write path even if mode.Switch learns an
// alias this plan does not know); the body says the switch refuses after consent
// and invents no deployment list.
func TestU5bModeSwitchGateUnrecognizedTargetStillGated(t *testing.T) {
	tui, f := newGateTUI(t)
	findItem(t, tui, "current_mode")
	if err := tui.switchMode(itemAtCursor(tui), "bogus"); err != nil {
		t.Fatalf("switchMode: %v", err)
	}
	if tui.pending == nil {
		t.Fatal("an unrecognized target skipped the gate")
	}
	body := strings.Join(tui.pending.body, "\n")
	if want := "Deploys: the bogus profile (unrecognized target: the switch refuses after consent)"; !strings.Contains(body, want) {
		t.Errorf("the plan does not state the unrecognized target:\ngot:\n%s\nwant line: %q", body, want)
	}
	if strings.Contains(body, "from the embedded") {
		t.Errorf("the plan invented a deployment listing for an unrecognized target:\n%s", body)
	}
	tui.Step(KeyYes)
	if len(f.switched) != 1 || f.switched[0] != "bogus" {
		t.Fatalf("the plan did not hand the target to the switch: %v", f.switched)
	}
}

// --- W15: the mode restore --------------------------------------------------

// TestU5bModeRestoreGatePresentsFilePlan pins the W15 body: the plan names the
// resolved source directory, its file groups and the pre-restore destination, so
// the operator approves a restore rather than a row label.
//
// The fixture is a LEGACY backup (F-S9A-3): five files, of which only the two the
// managed register owns are copied back. The plan must count and group exactly
// those two and name the rest as left in place - before this pin it advertised
// "rules (2 files)" and "skills (1 file)", files mode.Restore never writes.
func TestU5bModeRestoreGatePresentsFilePlan(t *testing.T) {
	tui, f := newGateTUI(t) // isolates the home first: the seed must land in it
	source := seedPanelBackup(t, map[string]string{
		// Register-covered: the narrowed copy-back writes these.
		"agents/architect/AGENT.md": "restored agent",
		"profile.json":              `{"name":"full"}`,
		// Integrations-owned trees a pre-partition backup still carries.
		"rules/AGENTS.md":       "restored rules",
		"rules/extra.md":        "restored extra rules",
		"skills/alpha/SKILL.md": "restored skill",
	})
	cfg := panelConfigPath(t)
	armWith(t, tui, "mode.restore")
	if tui.pending == nil {
		t.Fatalf("the restore did not arm the gate: %q", tui.message)
	}
	if f.restored != 0 {
		t.Fatalf("the restore ran before consent: %d", f.restored)
	}
	want := []string{
		"Restores the newest profile backup over the live profile configuration.",
		"Current mode: clean -> restored",
		"Config dir: " + cfg,
		"Restore source: " + source + " (2 files)",
		"Left in place (not a mode-managed item): rules, skills",
		"  agents (1 file)",
		"  profile.json (1 file)",
		"Current managed entries move to a pre-restore backup first (under " + filepath.Dir(source) + "):",
	}
	for _, name := range mode.DefaultManagedItems {
		want = append(want, managedLine(name, "absent"))
	}
	want = append(want,
		"state.json will be written (0600, atomic) with the mode change:",
		"  current_mode: clean -> restored",
	)
	assertBodyLines(t, tui.pending.body, want...)
	right := frameBody(tui)
	for _, ln := range tui.pending.body {
		if !strings.Contains(right, "    "+ln) {
			t.Errorf("the frame does not render the composed line verbatim: %q\n%s", ln, right)
		}
	}
	if srcAt, promptAt := strings.Index(right, "Restore source: "), strings.Index(right, "Confirm "); srcAt < 0 || promptAt < 0 || srcAt > promptAt {
		t.Errorf("the plan does not precede the prompt (plan=%d, prompt=%d):\n%s", srcAt, promptAt, right)
	}
}

// TestU5bModeRestoreGateRefusesWithoutASource: with no resolvable backup there is
// no plan to present and no deployment to consent to, so the door refuses before
// the gate with mode.Restore's own sentence.
func TestU5bModeRestoreGateRefusesWithoutASource(t *testing.T) {
	tui, f := newGateTUI(t)
	findItem(t, tui, "mode.restore")
	tui.Step(KeyEnter)
	if tui.pending != nil {
		t.Fatal("a restore with no resolvable backup opened a consent gate")
	}
	if f.restored != 0 {
		t.Fatalf("a source-less restore ran: %d", f.restored)
	}
	if !strings.Contains(tui.message, "no previous backups found to restore") {
		t.Errorf("refusal message = %q; want mode.Restore's own sentence", tui.message)
	}
}

// TestU5bModeRestoreGateRequiresConsent is the W15 consent matrix.
func TestU5bModeRestoreGateRequiresConsent(t *testing.T) {
	for _, answer := range []Key{KeyNo, KeyEsc} {
		t.Run(keyName(answer)+" cancels", func(t *testing.T) {
			tui, f := newGateTUI(t)
			seedPanelBackup(t, map[string]string{"rules/AGENTS.md": "restored"})
			armWith(t, tui, "mode.restore")
			if tui.pending == nil {
				t.Fatal("the restore did not arm the gate")
			}
			tui.Step(answer)
			if f.restored != 0 {
				t.Fatalf("the restore ran on %s: %d", keyName(answer), f.restored)
			}
			if tui.message != "cancelled" {
				t.Errorf("declined message = %q; want cancelled", tui.message)
			}
		})
	}

	t.Run("y restores exactly once", func(t *testing.T) {
		tui, f := newGateTUI(t)
		seedPanelBackup(t, map[string]string{"rules/AGENTS.md": "restored"})
		armWith(t, tui, "mode.restore")
		if tui.pending == nil {
			t.Fatal("the restore did not arm the gate")
		}
		tui.Step(KeyYes)
		if f.restored != 1 {
			t.Fatalf("consented restore ran %d times; want 1", f.restored)
		}
		if !strings.HasPrefix(tui.message, "[SUCCESS]") {
			t.Errorf("approved gate message = %q; want the success stamp", tui.message)
		}
	})
}

// TestU5bModeRestoreGateStaleSourceRefuses: the source file list is the signed
// state, so a backup that grows under the prompt refuses before anything is
// deployed. The growth must be inside an entry the copy-back actually writes
// (F-S9A-3): the signature covers the register-covered files, so a late file in
// an integrations-owned tree is not this plan's staleness to refuse.
func TestU5bModeRestoreGateStaleSourceRefuses(t *testing.T) {
	tui, f := newGateTUI(t)
	source := seedPanelBackup(t, map[string]string{"agents/AGENT.md": "restored"})
	armWith(t, tui, "mode.restore")
	if tui.pending == nil {
		t.Fatal("the restore did not arm the gate")
	}
	if err := os.WriteFile(filepath.Join(source, "agents", "LATE.md"), []byte("late"), 0o600); err != nil {
		t.Fatal(err)
	}
	tui.Step(KeyYes)
	if f.restored != 0 {
		t.Fatalf("a stale restore ran: %d", f.restored)
	}
	for _, want := range []string{"restore source", "stale", "nothing written"} {
		if !strings.Contains(tui.message, want) {
			t.Errorf("refusal message missing %q: %q", want, tui.message)
		}
	}
}

// --- line-mode EOF on the two mode doors -------------------------------------

// TestU5bLineModeEOFRefusesTheModeDoors drives the real line-mode entry (RunTUI
// over a pipe): the panel reaches each mode gate, renders the plan, and hits EOF
// with nothing deployed.
func TestU5bLineModeEOFRefusesTheModeDoors(t *testing.T) {
	t.Run("W13 switch", func(t *testing.T) {
		isolatePanelModeDirs(t)
		f := newFakeDeps()
		f.state.ManagedItems = mode.DefaultManagedItems
		c := NewCore(f.deps())
		out := &bytes.Buffer{}
		if code := RunTUI(c, strings.NewReader("2\n1\n"), out, ""); code != 0 {
			t.Fatalf("RunTUI exit = %d; want 0", code)
		}
		if len(f.switched) != 0 {
			t.Fatalf("EOF at the switch gate deployed: %v", f.switched)
		}
		transcript := out.String()
		for _, want := range []string{
			"Switches the active profile: clean -> full",
			"Confirm Profile Mode? [y/n]",
		} {
			if !strings.Contains(transcript, want) {
				t.Errorf("line-mode transcript missing %q:\n%s", want, transcript)
			}
		}
	})

	t.Run("W15 restore", func(t *testing.T) {
		isolatePanelModeDirs(t)
		// A register-covered entry: rules/ left the mode register in the P5-3
		// partition, so a backup carrying it is not a restore source the plan may
		// count (F-S9A-3).
		source := seedPanelBackup(t, map[string]string{"agents/custom/AGENT.md": "restored"})
		f := newFakeDeps()
		f.state.ManagedItems = mode.DefaultManagedItems
		c := NewCore(f.deps())
		out := &bytes.Buffer{}
		if code := RunTUI(c, strings.NewReader("8\n"), out, ""); code != 0 {
			t.Fatalf("RunTUI exit = %d; want 0", code)
		}
		if f.restored != 0 {
			t.Fatalf("EOF at the restore gate restored: %d", f.restored)
		}
		transcript := out.String()
		for _, want := range []string{
			"Restore source: " + source + " (1 file)",
			"Confirm Restore Latest Profile Backup? [y/n]",
		} {
			if !strings.Contains(transcript, want) {
				t.Errorf("line-mode transcript missing %q:\n%s", want, transcript)
			}
		}
	})
}

// --- scope guard ------------------------------------------------------------

// TestU5bReadOnlyRowsStayUnarmed is the other half of the F3(a) contract on this
// panel: the rows with no write path keep their no-write arm. Nothing about
// U5b's gates leaks into them, and a row whose activation only PRINTS a status
// (mode.status) still writes nothing.
func TestU5bReadOnlyRowsStayUnarmed(t *testing.T) {
	for _, id := range []string{"managed_items", "last_backup", "config.show"} {
		t.Run(id, func(t *testing.T) {
			tui, f := newGateTUI(t)
			before := f.saved
			findItem(t, tui, id)
			tui.Step(KeyEnter)
			if tui.pending != nil || tui.choices != nil {
				t.Fatalf("%s armed a write surface (pending %v, choices %v)", id, tui.pending, tui.choices)
			}
			if f.saved != before {
				t.Fatalf("%s wrote to a Core seam: %d saves", id, f.saved)
			}
			if !strings.HasPrefix(tui.message, "read-only: ") {
				t.Errorf("%s message = %q; want the read-only answer", id, tui.message)
			}
		})
	}

	t.Run("mode.status prints and writes nothing", func(t *testing.T) {
		tui, f := newGateTUI(t)
		before := f.saved
		findItem(t, tui, "mode.status")
		tui.Step(KeyEnter)
		if tui.pending != nil || tui.choices != nil {
			t.Fatalf("mode.status armed a write surface (pending %v, choices %v)", tui.pending, tui.choices)
		}
		if f.saved != before {
			t.Fatalf("mode.status wrote to a Core seam: %d saves", f.saved)
		}
		if len(tui.result) == 0 {
			t.Error("mode.status printed nothing")
		}
	})
}

// --- r5 F-1: the save-gate preset NAME slots --------------------------------

// TestPanelPresetPlanMasksPresetNames pins the W24 append plan's name slots
// (paneldiff.go panelPresetPlan.diff / panelPresetDrift): the append sentence,
// the existing-preset list, the "+ preset name = numbers" line and the drift
// line all render the preset NAME through panelSlot (providers.MaskSlot), while
// the numbers keep panelOperand. r5-seat2 F-1: a preset name is a file-sourced
// registry slot, and MaskOperand alone is the IDENTITY on the underscore-head
// credential class - so the pre-fix gate printed the plant verbatim in the very
// modal the operator signs.
func TestPanelPresetPlanMasksPresetNames(t *testing.T) {
	plant := presetSinkSkLivePlant
	tail := presetSinkSkLiveTail
	plan := panelPresetPlanFor([]swarm.SavedPreset{
		{Name: plant, Numbers: "1,3"},
		{Name: "beta", Numbers: "2"},
	}, "newcomer", "4,5")
	diff := strings.Join(plan.diff(), "\n")
	if strings.Contains(diff, plant) || strings.Contains(diff, tail) {
		t.Fatalf("the append diff echoed the planted preset name raw:\n%s", diff)
	}
	if !strings.Contains(diff, presetSinkSkLiveMask) {
		t.Fatalf("the append diff is missing the masked name %q:\n%s", presetSinkSkLiveMask, diff)
	}
	// The benign sibling keeps its exact bytes next to the plant.
	if !strings.Contains(diff, "  preset beta") || !strings.Contains(diff, "+ preset newcomer = 4,5") {
		t.Fatalf("benign preset lines drifted:\n%s", diff)
	}

	// Drift names the moved row's FRESH name, so that slot takes the same
	// authority: the presented list carries a benign second row and the fresh
	// read carries the plant in its place.
	presented := panelPresetPlanFor([]swarm.SavedPreset{
		{Name: "beta", Numbers: "1,3"},
		{Name: "gamma", Numbers: "2"},
	}, "newcomer", "4,5")
	fresh := panelPresetPlanFor([]swarm.SavedPreset{
		{Name: "beta", Numbers: "1,3"},
		{Name: plant, Numbers: "2"},
	}, "newcomer", "4,5")
	drift := panelPresetDrift(presented, fresh)
	if !strings.Contains(drift, presetSinkSkLiveMask) {
		t.Fatalf("drift line missing the masked fresh name %q: %q", presetSinkSkLiveMask, drift)
	}
	if strings.Contains(drift, plant) || strings.Contains(drift, tail) {
		t.Fatalf("drift line echoed the plant raw: %q", drift)
	}
}

// TestPanelPresetPlanBenignNamesByteIdentical pins the identity half: a benign
// plan composes the exact pre-change lines (no mask marker anywhere), so the
// operator-signed diff stays byte-stable for ordinary preset names.
func TestPanelPresetPlanBenignNamesByteIdentical(t *testing.T) {
	plan := panelPresetPlanFor([]swarm.SavedPreset{
		{Name: "alpha", Numbers: "1"},
		{Name: "beta", Numbers: "2"},
	}, "gamma", "3,4")
	assertBodyLines(t, plan.diff(),
		"Appends preset gamma to swarm-state.json. No existing preset changes.",
		"  presets before: 2, after: 3",
		"  existing presets (kept byte-identical):",
		"  preset alpha",
		"  preset beta",
		"+ preset gamma = 3,4",
		"swarm-state.json will be written (0600, atomic) with this exact diff:",
	)
	for _, ln := range plan.diff() {
		if strings.Contains(ln, "****") {
			t.Fatalf("a benign plan line carries a mask marker: %q", ln)
		}
	}
}
