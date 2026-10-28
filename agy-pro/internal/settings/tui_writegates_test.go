package settings

// P5-1 U5a: write-gate tests for the legacy `agy pro settings` panel's
// providers.json and HKCU repair doors (W1/W2/W3/W32/W33/W34 panel halves).
//
// Every test drives the panel's own seams: fakeDeps for the Core verbs, the
// panelEnvCandidates seam for the stored environment entries, and a temp dir for
// the key-backup file. No live ~/.agy-pro, no live HKCU, no console.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// newGateTUI builds the panel over a two-provider registry so the add / rename /
// delete gates have a record to describe.
func newGateTUI(t *testing.T) (*TUI, *fakeDeps) {
	t.Helper()
	isolatePanelModeDirs(t)
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "google",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai/",
			APIKeyEnv: "GEMINI_API_KEY",
			Models:    []providers.Model{{ID: "gemini-2.0-flash"}, {ID: "gemini-2.0-pro"}},
		},
		{Name: "acme", BaseURL: "https://gw.example/v1", APIKeyEnv: "ACME_KEY"},
	}}
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	return NewTUI(c, view, &ScriptedKeys{}, &ScriptedLines{}, &bytes.Buffer{}), f
}

// armWith points the panel at item id, feeds the given prompt lines, and presses
// Enter so the door runs up to (and not through) its consent gate.
func armWith(t *testing.T, tui *TUI, id string, lines ...string) {
	t.Helper()
	findItem(t, tui, id)
	tui.lines = &ScriptedLines{Lines: lines}
	tui.Step(KeyEnter)
}

// frameBody is the right pane of the current frame, the surface the gate body
// renders into.
func frameBody(tui *TUI) string { return tui.Frame().Right }

// --- wording parity ---------------------------------------------------------

// TestPanelDiffWordingMatchesCatalog pins the parity choice recorded at the head
// of paneldiff.go: the panel copies the en catalog sentences the hub's provider
// approvals render instead of inventing its own, so a hub wording change cannot
// leave the deprecated panel showing a different diff than the surface it routes
// to. Each const is rebuilt from the catalog entry with the same slot fill, and
// the comparison is a full equality, so a dropped or added word fails here.
func TestPanelDiffWordingMatchesCatalog(t *testing.T) {
	b := i18n.Load("en")
	fill := func(key, slot, value string) string {
		return strings.Replace(b.T(key), slot, value, 1)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "settings.approval.add",
			got:  fmt.Sprintf(panelAddSentence, "acme"),
			want: fill("settings.approval.add", "{{name}}", "acme"),
		},
		{
			name: "settings.approval.replace",
			got:  fmt.Sprintf(panelReplaceSentence, "acme"),
			want: fill("settings.approval.replace", "{{name}}", "acme"),
		},
		{
			name: "settings.approval.rename",
			got:  fmt.Sprintf(panelRenameSentence, "old", "new"),
			want: fill(fill("settings.approval.rename", "{{old}}", "old"), "{{new}}", "new"),
		},
		{
			name: "providers.delete.body",
			got:  fmt.Sprintf(panelDeleteSentence, "acme"),
			want: fill("providers.delete.body", "{{name}}", "acme"),
		},
		{
			name: "modal.approval.body",
			got:  panelAtomicLine,
			want: b.T("modal.approval.body"),
		},
		{
			name: "modal.approval.counts",
			got:  fmt.Sprintf(panelCountsLine, 2, 0),
			want: fill(fill("modal.approval.counts", "{{before}}", "2"), "{{after}}", "0"),
		},
		{
			name: "modal.approval.diff_added",
			got:  fmt.Sprintf(panelAddedLine, "X"),
			want: fill("modal.approval.diff_added", "{{line}}", "X"),
		},
		{
			name: "modal.approval.diff_removed",
			got:  fmt.Sprintf(panelRemovedLine, "X"),
			want: fill("modal.approval.diff_removed", "{{line}}", "X"),
		},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s drifted:\npanel: %q\nhub:   %q", c.name, c.got, c.want)
		}
	}
}

// --- consent matrix ---------------------------------------------------------

// TestU5aProviderGatesRequireConsent is the consent matrix for the three
// providers.json doors: arming the gate writes nothing, n and Esc cancel with
// nothing written, y is the only path to the Core verb.
func TestU5aProviderGatesRequireConsent(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(specPath, []byte(`{"name":"gamma","baseURL":"https://gamma.example/v1","apiKeyEnv":"GAMMA_KEY","models":[{"id":"g1"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	type door struct {
		id    string
		lines []string
		apply func(t *testing.T, f *fakeDeps)
	}
	doors := []door{
		{
			id:    "providers.add_json",
			lines: []string{specPath},
			apply: func(t *testing.T, f *fakeDeps) {
				t.Helper()
				if !hasProvider(f.reg, "gamma") {
					t.Fatalf("consented add did not write: %+v", f.reg.Providers)
				}
			},
		},
		{
			id:    "providers.rename",
			lines: []string{"acme", "acme2"},
			apply: func(t *testing.T, f *fakeDeps) {
				t.Helper()
				if !hasProvider(f.reg, "acme2") || hasProvider(f.reg, "acme") {
					t.Fatalf("consented rename did not write: %+v", f.reg.Providers)
				}
			},
		},
		{
			id:    "providers.delete",
			lines: []string{"acme"},
			apply: func(t *testing.T, f *fakeDeps) {
				t.Helper()
				if hasProvider(f.reg, "acme") {
					t.Fatalf("consented delete did not write: %+v", f.reg.Providers)
				}
			},
		},
	}

	for _, d := range doors {
		for _, answer := range []Key{KeyNo, KeyEsc} {
			t.Run(d.id+"/"+keyName(answer)+" cancels", func(t *testing.T) {
				tui, f := newGateTUI(t)
				armWith(t, tui, d.id, d.lines...)
				if tui.pending == nil {
					t.Fatalf("%s did not arm the consent gate", d.id)
				}
				if f.saved != 0 {
					t.Fatalf("%s wrote before consent: %d saves", d.id, f.saved)
				}
				tui.Step(answer)
				if tui.pending != nil {
					t.Fatal("the declined gate stayed armed")
				}
				if f.saved != 0 {
					t.Fatalf("%s wrote on %s: %d saves", d.id, keyName(answer), f.saved)
				}
				if tui.message != "cancelled" {
					t.Errorf("declined message = %q; want cancelled", tui.message)
				}
			})
		}

		t.Run(d.id+"/y applies", func(t *testing.T) {
			tui, f := newGateTUI(t)
			armWith(t, tui, d.id, d.lines...)
			if tui.pending == nil {
				t.Fatalf("%s did not arm the consent gate", d.id)
			}
			tui.Step(KeyYes)
			if f.saved != 1 {
				t.Fatalf("%s saves after consent = %d; want exactly 1", d.id, f.saved)
			}
			d.apply(t, f)
			if !strings.HasPrefix(tui.message, "[SUCCESS]") {
				t.Errorf("approved gate message = %q; want the success stamp", tui.message)
			}
		})
	}
}

func keyName(k Key) string {
	if k == KeyNo {
		return "n"
	}
	return "Esc"
}

func hasProvider(reg *providers.Registry, name string) bool {
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return true
		}
	}
	return false
}

// --- presentation -----------------------------------------------------------

// TestU5aProviderGateRendersExactDiff pins the presentation itself: the diff
// lines are in the frame BEFORE the y/n prompt, so the approval describes the
// write it authorizes, and the record-differs arms (replace / rename / delete)
// each carry the before/after model counts and the +/- provider lines.
func TestU5aProviderGateRendersExactDiff(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		tui, _ := newGateTUI(t)
		armWith(t, tui, "providers.delete", "google")
		right := frameBody(tui)
		for _, want := range []string{
			"Removes the registry entry for google only. Key values and files are untouched.",
			"Models before: 2, after: 0",
			"providers.json will be written (0600, atomic) with this exact diff:",
			"- provider google (https://generativelanguage.googleapis.com/v1beta/openai/, 2 models)",
			"Confirm Delete Provider? [y/n]",
		} {
			if !strings.Contains(right, want) {
				t.Errorf("delete gate frame missing %q:\n%s", want, right)
			}
		}
		diffAt := strings.Index(right, "- provider google")
		promptAt := strings.Index(right, "Confirm Delete Provider? [y/n]")
		if diffAt < 0 || promptAt < 0 || diffAt > promptAt {
			t.Errorf("the diff does not precede the prompt (diff=%d, prompt=%d):\n%s", diffAt, promptAt, right)
		}
	})

	t.Run("rename", func(t *testing.T) {
		tui, _ := newGateTUI(t)
		armWith(t, tui, "providers.rename", "acme", "acme-east")
		right := frameBody(tui)
		for _, want := range []string{
			"Rename provider acme to acme-east; the model list carries over unchanged.",
			"Models before: 0, after: 0",
			"- provider acme (https://gw.example/v1, 0 models)",
			"+ provider acme-east (https://gw.example/v1, 0 models)",
		} {
			if !strings.Contains(right, want) {
				t.Errorf("rename gate frame missing %q:\n%s", want, right)
			}
		}
	})

	t.Run("add replaces wholesale", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(path, []byte(`{"name":"acme","baseURL":"https://acme.example/v2","apiKeyEnv":"ACME_KEY","models":[{"id":"a1"},{"id":"a2"},{"id":"a3"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		tui, _ := newGateTUI(t)
		armWith(t, tui, "providers.add_json", path)
		right := frameBody(tui)
		for _, want := range []string{
			"Replace provider acme wholesale: the current entry and its whole model list are discarded.",
			"Models before: 0, after: 3",
			"- provider acme (https://gw.example/v1, 0 models)",
			"+ provider acme (https://acme.example/v2, 3 models)",
		} {
			if !strings.Contains(right, want) {
				t.Errorf("replace gate frame missing %q:\n%s", want, right)
			}
		}
	})

	t.Run("add of a new name", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(path, []byte(`{"name":"gamma","baseURL":"https://gamma.example/v1","apiKeyEnv":"GAMMA_KEY","models":[{"id":"g1"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		tui, _ := newGateTUI(t)
		armWith(t, tui, "providers.add_json", path)
		right := frameBody(tui)
		for _, want := range []string{
			"Add provider gamma to the registry.",
			"Models before: 0, after: 1",
			"+ provider gamma (https://gamma.example/v1, 1 model)",
		} {
			if !strings.Contains(right, want) {
				t.Errorf("add gate frame missing %q:\n%s", want, right)
			}
		}
	})
}

// TestU5aProviderGateDiffIsMasked: the diff is a transcript, so every slot in it
// renders through the registry-slot authority and the URL through the layered
// URL mask. A credential-shaped provider NAME and a secret inside a base URL
// never reach the frame raw (the pre-change gate printed no diff at all, so this
// is new exposure the gate must not open).
func TestU5aProviderGateDiffIsMasked(t *testing.T) {
	tui, f := newGateTUI(t)
	plant := testfixtures.SKLive
	f.reg.Providers = append(f.reg.Providers, providers.Provider{
		Name:      plant + "_row",
		BaseURL:   "https://user:" + plant + "@gw.example/v1",
		APIKeyEnv: "PLANTED_KEY",
	})
	armWith(t, tui, "providers.delete", plant+"_row")
	if tui.pending == nil {
		t.Fatal("the planted provider did not arm the delete gate")
	}
	right := frameBody(tui)
	if strings.Contains(right, plant) {
		t.Errorf("the gate frame echoes the planted secret raw:\n%s", right)
	}
	if !strings.Contains(right, panelSlot(plant+"_row")) {
		t.Errorf("the gate frame does not carry the slot-masked name:\n%s", right)
	}
}

// --- stale base -------------------------------------------------------------

// TestU5aProviderGateStaleBaseRefuses pins the write-time re-verify: the diff is
// composed from disk and then the operator may sit at the prompt, so the write
// must re-read and refuse when the presented state moved. Every arm names the
// drift, writes nothing, and leaves the external edit in place.
func TestU5aProviderGateStaleBaseRefuses(t *testing.T) {
	t.Run("delete on a record that changed", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armWith(t, tui, "providers.delete", "acme")
		f.reg.Providers[1].BaseURL = "https://moved.example/v1"
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a stale delete wrote: %d saves", f.saved)
		}
		if !strings.Contains(tui.message, "changed on disk") {
			t.Errorf("refusal message = %q; want a changed-on-disk refusal", tui.message)
		}
		if f.reg.Providers[1].BaseURL != "https://moved.example/v1" {
			t.Error("the refusal reverted the external edit")
		}
	})

	t.Run("delete on a record that vanished", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armWith(t, tui, "providers.delete", "acme")
		f.reg.Providers = f.reg.Providers[:1]
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a vanished record still wrote: %d saves", f.saved)
		}
		if !strings.Contains(tui.message, "disappeared") {
			t.Errorf("refusal message = %q; want a disappeared refusal", tui.message)
		}
	})

	t.Run("add over a name that appeared", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spec.json")
		if err := os.WriteFile(path, []byte(`{"name":"gamma","baseURL":"https://gamma.example/v1","apiKeyEnv":"GAMMA_KEY","models":[{"id":"g1"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		tui, f := newGateTUI(t)
		armWith(t, tui, "providers.add_json", path)
		f.reg.Providers = append(f.reg.Providers, providers.Provider{Name: "gamma", BaseURL: "https://external.example/v9", APIKeyEnv: "OTHER_KEY"})
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a stale add wrote: %d saves", f.saved)
		}
		if !strings.Contains(tui.message, "appeared") {
			t.Errorf("refusal message = %q; want an appeared refusal", tui.message)
		}
		if got := f.reg.Providers[len(f.reg.Providers)-1].BaseURL; got != "https://external.example/v9" {
			t.Errorf("the refused add replaced the external entry: %q", got)
		}
	})

	t.Run("rename onto a name that appeared", func(t *testing.T) {
		tui, f := newGateTUI(t)
		armWith(t, tui, "providers.rename", "acme", "acme2")
		f.reg.Providers = append(f.reg.Providers, providers.Provider{Name: "acme2", BaseURL: "https://external.example/v9", APIKeyEnv: "OTHER_KEY"})
		tui.Step(KeyYes)
		if f.saved != 0 {
			t.Fatalf("a stale rename wrote: %d saves", f.saved)
		}
		if !strings.Contains(tui.message, "appeared") {
			t.Errorf("refusal message = %q; want an appeared refusal", tui.message)
		}
	})
}

// TestU5aProviderDoorsRefuseUnknownTargetBeforeTheGate mirrors the hub's
// ordering (screen_providers.go:667-673, screen_settings.go:751-755): a name that
// is not on disk has no record to describe, so the door refuses instead of
// opening a gate over a write that cannot happen. The refusal is masked: the
// operand is a user paste and may be a decorated credential.
func TestU5aProviderDoorsRefuseUnknownTargetBeforeTheGate(t *testing.T) {
	for _, id := range []string{"providers.delete", "providers.rename"} {
		t.Run(id, func(t *testing.T) {
			tui, f := newGateTUI(t)
			operand := "./backups," + tuiAWSPlant
			armWith(t, tui, id, operand, "anywhere")
			if tui.pending != nil {
				t.Fatal("an unknown target opened a consent gate")
			}
			if f.saved != 0 {
				t.Fatalf("an unknown target wrote: %d saves", f.saved)
			}
			if strings.Contains(tui.message, tuiAWSPlant) {
				t.Errorf("the refusal echoes the planted secret: %q", tui.message)
			}
			if !strings.Contains(tui.message, "nothing") {
				t.Errorf("refusal message = %q; want an explicit nothing-written clause", tui.message)
			}
		})
	}
}

// --- repair doors -----------------------------------------------------------

// withEnvCandidates installs a fake stored-entry source for the repair plan and
// restores the production seam on cleanup.
func withEnvCandidates(t *testing.T, m map[string][]providers.EnvCandidate) {
	t.Helper()
	prev := panelEnvCandidates
	panelEnvCandidates = func(name string) []providers.EnvCandidate { return m[name] }
	t.Cleanup(func() { panelEnvCandidates = prev })
}

// TestU5aFixKeysGatePresentsNamesOnly: the repair diff states the NAMES the op
// may write and never a value, and the rewrite verdict comes from the stored
// entry set rather than a live probe.
func TestU5aFixKeysGatePresentsNamesOnly(t *testing.T) {
	plant := testfixtures.AWSSecretNoSlash
	withEnvCandidates(t, map[string][]providers.EnvCandidate{
		"GEMINI_API_KEY": {
			{Root: providers.EnvRootUser, Name: providers.BOM + "GEMINI_API_KEY", Exact: false, Value: plant},
		},
		"ACME_KEY": {
			{Root: providers.EnvRootUser, Name: "ACME_KEY", Exact: true, Value: plant},
		},
	})
	tui, f := newGateTUI(t)
	armWith(t, tui, "doctor.fix_keys", "all")
	if tui.pending == nil {
		t.Fatal("fix-keys did not arm the consent gate")
	}
	if len(f.fixed) != 0 {
		t.Fatalf("fix-keys ran before consent: %v", f.fixed)
	}
	right := frameBody(tui)
	for _, want := range []string{
		"Repairs BOM-polluted provider key names in HKCU\\Environment and this process's environment.",
		"GEMINI_API_KEY (provider google): rewrite the clean name from BOM variant <BOM>GEMINI_API_KEY, then delete that variant",
		"ACME_KEY (provider acme): keep (no BOM variant stored beside the clean name)",
		"Writes the key NAMES above; the process environment and HKCU\\Environment are the only stores it touches.",
	} {
		if !strings.Contains(right, want) {
			t.Errorf("fix-keys gate frame missing %q:\n%s", want, right)
		}
	}
	if strings.Contains(right, plant) {
		t.Errorf("the fix-keys diff prints a stored VALUE:\n%s", right)
	}

	tui.Step(KeyYes)
	if len(f.fixed) != 1 || f.fixed[0] != "all" {
		t.Fatalf("consented fix-keys did not run: %v", f.fixed)
	}
}

// TestU5aFixKeysGateStaleCandidateSetRefuses: the per-target verdict is the
// signed state, so a stored entry set that moves under the prompt refuses the
// write instead of applying a plan the operator never saw.
func TestU5aFixKeysGateStaleCandidateSetRefuses(t *testing.T) {
	candidates := map[string][]providers.EnvCandidate{
		"GEMINI_API_KEY": {
			{Root: providers.EnvRootUser, Name: providers.BOM + "GEMINI_API_KEY", Exact: false, Value: "v1"},
		},
	}
	withEnvCandidates(t, candidates)
	tui, f := newGateTUI(t)
	armWith(t, tui, "doctor.fix_keys", "all")
	if tui.pending == nil {
		t.Fatal("fix-keys did not arm the consent gate")
	}
	// The external change: the BOM variant disappears from the store.
	candidates["GEMINI_API_KEY"] = []providers.EnvCandidate{
		{Root: providers.EnvRootUser, Name: "GEMINI_API_KEY", Exact: true, Value: "v2"},
	}
	tui.Step(KeyYes)
	if len(f.fixed) != 0 {
		t.Fatalf("a stale repair plan ran: %v", f.fixed)
	}
	if !strings.Contains(tui.message, "stale") {
		t.Errorf("refusal message = %q; want a stale-plan refusal", tui.message)
	}
	if !strings.Contains(tui.message, "HKCU untouched") {
		t.Errorf("refusal message = %q; want the untouched-store clause", tui.message)
	}
}

// writeKeyBackup lays down a v2 key-backup document the rollback plan can read.
func writeKeyBackup(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "registry-keys.v2.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestU5aRollbackGatePresentsRecordedEntries: the rollback diff names the root
// and the NAME of every recorded entry and never a value (the plan's snapshot
// type has no value field, so a protected value in the file cannot reach the
// frame), and y is the only path to the rollback op.
func TestU5aRollbackGatePresentsRecordedEntries(t *testing.T) {
	dir := t.TempDir()
	plant := testfixtures.AWSSecretNoSlash
	writeKeyBackup(t, dir, `{"version":2,"createdAt":"2026-10-22T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":[`+
		`{"root":"user","name":"NW_API_KEY","existed":true,"value":"`+plant+`"},`+
		`{"root":"process-env","name":"OLD_TOKEN","existed":false}]}`)

	tui, f := newGateTUI(t)
	armWith(t, tui, "doctor.rollback_keys", dir)
	if tui.pending == nil {
		t.Fatal("rollback did not arm the consent gate")
	}
	if len(f.rolled) != 0 {
		t.Fatalf("rollback ran before consent: %v", f.rolled)
	}
	right := frameBody(tui)
	for _, want := range []string{
		"Restores the recorded key names in HKCU\\Environment and this process's environment from one key backup.",
		"restore NW_API_KEY in HKCU\\Environment",
		"delete  OLD_TOKEN in the process environment",
		"The backup file is only read; nothing under the backup directory changes.",
	} {
		if !strings.Contains(right, want) {
			t.Errorf("rollback gate frame missing %q:\n%s", want, right)
		}
	}
	if strings.Contains(right, plant) {
		t.Errorf("the rollback diff prints a backup VALUE:\n%s", right)
	}

	// n cancels, y applies.
	tui.Step(KeyNo)
	if len(f.rolled) != 0 {
		t.Fatalf("a cancelled rollback ran: %v", f.rolled)
	}
	armWith(t, tui, "doctor.rollback_keys", dir)
	tui.Step(KeyYes)
	if len(f.rolled) != 1 || f.rolled[0] != dir {
		t.Fatalf("consented rollback did not run: %v", f.rolled)
	}
}

// TestU5aRollbackGateStaleBackupRefuses: the recorded entry set is the signed
// state, so a backup rewritten under the prompt refuses and nothing is restored.
func TestU5aRollbackGateStaleBackupRefuses(t *testing.T) {
	dir := t.TempDir()
	writeKeyBackup(t, dir, `{"version":2,"createdAt":"2026-10-22T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":[{"root":"user","name":"NW_API_KEY","existed":true}]}`)
	tui, f := newGateTUI(t)
	armWith(t, tui, "doctor.rollback_keys", dir)
	if tui.pending == nil {
		t.Fatal("rollback did not arm the consent gate")
	}
	writeKeyBackup(t, dir, `{"version":2,"createdAt":"2026-10-22T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":[{"root":"user","name":"NW_API_KEY","existed":true},{"root":"user","name":"LATE_KEY","existed":true}]}`)
	tui.Step(KeyYes)
	if len(f.rolled) != 0 {
		t.Fatalf("a stale rollback ran: %v", f.rolled)
	}
	if !strings.Contains(tui.message, "stale") {
		t.Errorf("refusal message = %q; want a stale-plan refusal", tui.message)
	}
}

// TestU5aRollbackGateUnreadableBackupStillRunsTheOp: when the backup cannot be
// read at presentation time there is no plan to sign, so the body says so and
// the op still runs: the STORE owns the refusal sentence, and the forwarded
// error keeps its masking path (the F6 pins on the rollback tail).
func TestU5aRollbackGateUnreadableBackupStillRunsTheOp(t *testing.T) {
	dir := t.TempDir() // no backup file inside
	tui, f := newGateTUI(t)
	armWith(t, tui, "doctor.rollback_keys", dir)
	if tui.pending == nil {
		t.Fatal("rollback did not arm the consent gate")
	}
	if right := frameBody(tui); !strings.Contains(right, "could not be read") {
		t.Errorf("the gate body does not say the backup is unreadable:\n%s", right)
	}
	tui.Step(KeyYes)
	if len(f.rolled) != 1 {
		t.Fatalf("the op did not run after consent: %v", f.rolled)
	}
}

// --- non-interactive refusal ------------------------------------------------

// TestU5aPipeEOFRefusesAndWritesNothing drives the real line-mode entry
// (RunTUI over a pipe): the panel reaches the delete gate, renders the exact
// diff, and then hits EOF at the reader. Nothing is written and the transcript
// carries both the diff and the open prompt, so a scripted caller can see what
// it declined to answer.
func TestU5aPipeEOFRefusesAndWritesNothing(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "google", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", APIKeyEnv: "GEMINI_API_KEY"},
	}}
	c := NewCore(f.deps())
	out := &bytes.Buffer{}
	if code := RunTUI(c, strings.NewReader("right\n5\ngoogle\n"), out, ""); code != 0 {
		t.Fatalf("RunTUI exit = %d", code)
	}
	if f.saved != 0 {
		t.Fatalf("EOF at the consent gate wrote: %d saves", f.saved)
	}
	if !hasProvider(f.reg, "google") {
		t.Fatal("the EOF run removed the provider")
	}
	transcript := out.String()
	for _, want := range []string{
		"- provider google (https://generativelanguage.googleapis.com/v1beta/openai/, 0 models)",
		"Confirm Delete Provider? [y/n]",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("line-mode transcript missing %q:\n%s", want, transcript)
		}
	}
}

// TestU5aLineModeNoAnswerCancels is the same refusal one step earlier: a piped
// "n" is the documented line-mode cancel and writes nothing either.
func TestU5aLineModeNoAnswerCancels(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	f.reg = &providers.Registry{Providers: []providers.Provider{{Name: "google", BaseURL: "https://gw.example/v1", APIKeyEnv: "GEMINI_API_KEY"}}}
	c := NewCore(f.deps())
	if code := RunTUI(c, strings.NewReader("right\n5\ngoogle\nn\nquit\n"), io.Discard, ""); code != 0 {
		t.Fatalf("RunTUI exit = %d", code)
	}
	if f.saved != 0 || !hasProvider(f.reg, "google") {
		t.Fatalf("a piped n wrote: saves=%d providers=%+v", f.saved, f.reg.Providers)
	}
}
