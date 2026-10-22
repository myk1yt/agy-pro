package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/testfixtures"
)

// CLI-twin gate tests for the PROVIDER-level registry verbs (P5-1 U1, user
// decision F3(a)): add-json, rename, delete now reuse the same
// confirmRegistryMutation gate the model twins carry, with provider-level diff
// renderers and a per-verb stale-base re-verify. The harnesses are the ones the
// model consent tests established (pro_settings_models_test.go, read-only from
// here):
//   - installFileBackedSettingsCore: a temp providers.json driven through the
//     REAL load/save seams (AGY_PRO_PROVIDERS), so "wrote nothing" is a
//     byte-level assertion on disk and the presentation/re-verify reads see the
//     same file the write touches;
//   - installSettingsCore: the in-memory Core for pure transcript-shape pins.

// providerVerbSpecFile writes one add-json spec into a temp file.
func providerVerbSpecFile(t *testing.T, spec string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "provider.json")
	if err := os.WriteFile(path, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunSettingsCmd_ProviderVerbs_PipeRequiresYes pins the never-silent gate on
// the non-interactive path for all three provider verbs: without --yes each verb
// prints the masked diff, refuses with an error naming the flag, and leaves the
// on-disk providers.json byte-identical; with --yes the write proceeds.
func TestRunSettingsCmd_ProviderVerbs_PipeRequiresYes(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	specPath := providerVerbSpecFile(t,
		`{"name":"delta","baseURL":"https://delta.example/v1","apiKeyEnv":"DELTA_KEY","models":[{"id":"d1"},{"id":"d2"}]}`)

	refuse := func(args []string, stdin string) string {
		t.Helper()
		before := diskBytes(t, path)
		code, out, errw := runSettings(t, args, stdin)
		if code == 0 {
			t.Fatalf("%v exit = 0; want non-zero (stdout=%s stderr=%s)", args, out, errw)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("%v refusal must name the --yes flag: stderr=%s", args, errw)
		}
		if !strings.Contains(out, "(0600, atomic)") {
			t.Errorf("%v must print the pre-write diff with the atomic-0600 sentence: stdout=%s", args, out)
		}
		if !strings.Contains(out, "[agy-pro] Approve Registry Write") {
			t.Errorf("%v must print the shared gate banner: stdout=%s", args, out)
		}
		if !bytes.Equal(diskBytes(t, path), before) {
			t.Errorf("%v wrote providers.json without consent", args)
		}
		return out
	}

	if out := refuse([]string{"providers", "add-json", specPath}, ""); !strings.Contains(out, "Adds provider delta to the registry.") {
		t.Errorf("add-json refusal diff missing the add sentence: stdout=%s", out)
	}
	if out := refuse([]string{"providers", "rename", "acme", "acme-main"}, ""); !strings.Contains(out, "Renames provider acme to acme-main") {
		t.Errorf("rename refusal diff missing the rename sentence: stdout=%s", out)
	}
	if out := refuse([]string{"providers", "delete", "acme"}, ""); !strings.Contains(out, "Removes provider acme from the registry.") {
		t.Errorf("delete refusal diff missing the removal sentence: stdout=%s", out)
	}

	// --yes consents: each verb writes through the real save seam.
	code, out, errw := runSettings(t, []string{"providers", "add-json", specPath, "--yes"}, "")
	if code != 0 {
		t.Fatalf("add-json --yes exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] provider delta saved") {
		t.Errorf("add-json success line = %q", out)
	}
	reg := reloadTestRegistry(t, path)
	if len(reg.Providers) != 2 || reg.Providers[1].Name != "delta" || len(reg.Providers[1].Models) != 2 {
		t.Fatalf("registry after consented add = %+v", reg.Providers)
	}

	code, _, errw = runSettings(t, []string{"providers", "rename", "acme", "acme-main", "--yes"}, "")
	if code != 0 {
		t.Fatalf("rename --yes exit = %d stderr=%s", code, errw)
	}
	reg = reloadTestRegistry(t, path)
	if reg.Providers[0].Name != "acme-main" || len(reg.Providers[0].Models) != 2 {
		t.Fatalf("registry after consented rename = %+v", reg.Providers[0])
	}

	code, _, errw = runSettings(t, []string{"providers", "delete", "acme-main", "--yes"}, "")
	if code != 0 {
		t.Fatalf("delete --yes exit = %d stderr=%s", code, errw)
	}
	reg = reloadTestRegistry(t, path)
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "delta" {
		t.Fatalf("registry after consented delete = %+v", reg.Providers)
	}
}

// TestRunSettingsCmd_ProviderVerbs_ConsoleConfirmation pins the console path
// through the stdinIsTTY seam: n, Esc, and EOF refuse with nothing written; only
// y reaches the Core write. The semantics are the model twins' (declining is a
// cancel, an immediate EOF is a refusal naming --yes).
func TestRunSettingsCmd_ProviderVerbs_ConsoleConfirmation(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	stubStdinIsTTY(t, true)
	args := []string{"providers", "rename", "acme", "acme-main"}

	before := diskBytes(t, path)
	code, out, errw := runSettings(t, args, "n\n")
	if code != 0 {
		t.Fatalf("declined console rename exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if !strings.Contains(out, "[CANCELLED] nothing written") {
		t.Errorf("decline must say nothing was written: stdout=%s", out)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("declined console rename wrote providers.json")
	}

	// Esc declines the same way.
	code, _, _ = runSettings(t, args, "\x1b\n")
	if code != 0 || !bytes.Equal(diskBytes(t, path), before) {
		t.Errorf("Esc-declined console rename exit = %d; want 0 with disk untouched", code)
	}

	// EOF is a refusal, not a cancel (a character device the seam cannot tell
	// from a console).
	code, _, errw = runSettings(t, args, "")
	if code == 0 {
		t.Fatalf("EOF-declined console rename exit = 0; want non-zero")
	}
	if !strings.Contains(errw, "--yes") {
		t.Errorf("EOF at the prompt must name the --yes flag: stderr=%s", errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("EOF-declined console rename wrote providers.json")
	}

	// y consents and the write proceeds.
	code, _, errw = runSettings(t, args, "y\n")
	if code != 0 {
		t.Fatalf("consented console rename exit = %d stderr=%s", code, errw)
	}
	if got := reloadTestRegistry(t, path).Providers[0].Name; got != "acme-main" {
		t.Fatalf("provider name after consented console rename = %q; want acme-main", got)
	}

	// The delete door takes the same answer: y removes, n keeps the record.
	code, _, errw = runSettings(t, []string{"providers", "delete", "acme-main"}, "n\n")
	if code != 0 || len(reloadTestRegistry(t, path).Providers) != 1 {
		t.Fatalf("declined console delete exit = %d; want 0 with the provider kept (stderr=%s)", code, errw)
	}
	code, _, errw = runSettings(t, []string{"providers", "delete", "acme-main"}, "y\n")
	if code != 0 || len(reloadTestRegistry(t, path).Providers) != 0 {
		t.Fatalf("consented console delete exit = %d; want 0 with the provider removed (stderr=%s)", code, errw)
	}
}

// TestRunSettingsCmd_ProviderVerbs_StaleBaseRefused pins the per-verb stale-base
// rule (G-C3, the CWE-367 class the model twins closed) on the --yes path, where
// there is no prompt to hang the interleaving on: the write-time re-read seam
// performs the external edit, so the check runs and refuses before Core is
// reached. Each verb's signed state is its own:
//   - delete/rename: the presented provider record (field by field);
//   - add: "the name was not on disk";
//   - rename: additionally "the new name was absent" (or was this exact record).
func TestRunSettingsCmd_ProviderVerbs_StaleBaseRefused(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	specPath := providerVerbSpecFile(t,
		`{"name":"delta","baseURL":"https://delta.example/v1","apiKeyEnv":"DELTA_KEY","models":[{"id":"d1"},{"id":"d2"}]}`)

	// staleWrite runs one --yes verb whose write-time re-read applies edit to
	// the on-disk registry first, then reloads.
	staleWrite := func(args []string, edit func(*providers.Registry)) (int, string, string) {
		t.Helper()
		prev := registryReload
		registryReload = func() (*providers.Registry, error) {
			reg := reloadTestRegistry(t, path)
			reg.Path = path
			edit(reg)
			if err := saveTestRegistry(reg); err != nil {
				t.Fatalf("external edit: %v", err)
			}
			return providers.Load()
		}
		t.Cleanup(func() { registryReload = prev })
		code, out, errw := runSettings(t, args, "")
		registryReload = prev
		return code, out, errw
	}

	// (a) delete: a signed field drifts (the record the operator approved is no
	// longer the record on disk).
	code, _, errw := staleWrite([]string{"providers", "delete", "acme", "--yes"}, func(reg *providers.Registry) {
		reg.Providers[0].BaseURL = "https://moved.example/v1"
	})
	if code != 1 {
		t.Fatalf("stale delete exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if !strings.Contains(errw, "changed on disk between the presentation and the write") {
		t.Errorf("stale delete must name the moved record: stderr=%s", errw)
	}
	reg := reloadTestRegistry(t, path)
	if len(reg.Providers) != 1 || reg.Providers[0].BaseURL != "https://moved.example/v1" {
		t.Fatalf("refused stale delete touched the file: %+v", reg.Providers)
	}

	// (b) delete: the record vanished under the prompt.
	code, _, errw = staleWrite([]string{"providers", "delete", "acme", "--yes"}, func(reg *providers.Registry) {
		reg.Providers = nil
	})
	if code != 1 || !strings.Contains(errw, "disappeared from providers.json") {
		t.Fatalf("vanished delete exit = %d stderr=%s; want the disappeared refusal", code, errw)
	}
	if got := reloadTestRegistry(t, path); len(got.Providers) != 0 {
		t.Fatalf("refused vanished delete wrote the file: %+v", got.Providers)
	}

	// Re-seed the SAME file for the add/rename arms (a second
	// installFileBackedSettingsCore would point the env var at a new temp path
	// while this variable kept the old one, so the re-read seam would edit a
	// file the verb never reads).
	seed := func(reg *providers.Registry) {
		t.Helper()
		reg.Path = path
		if err := saveTestRegistry(reg); err != nil {
			t.Fatalf("re-seed registry: %v", err)
		}
	}
	seed(twoModelRegistry())

	// (c) add: the name appeared while the prompt waited, so the presented
	// "new entry" diff would have become a wholesale replace of a record the
	// operator never saw (H-C3).
	code, _, errw = staleWrite([]string{"providers", "add-json", specPath, "--yes"}, func(reg *providers.Registry) {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name: "delta", BaseURL: "https://external.example/v1", APIKeyEnv: "EXT_KEY",
			Models: []providers.Model{{ID: "external"}},
		})
	})
	if code != 1 {
		t.Fatalf("stale add exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if !strings.Contains(errw, "appeared in providers.json between the presentation and the write") {
		t.Errorf("stale add must name the appeared record: stderr=%s", errw)
	}
	reg = reloadTestRegistry(t, path)
	if len(reg.Providers) != 2 || reg.Providers[1].BaseURL != "https://external.example/v1" ||
		len(reg.Providers[1].Models) != 1 || reg.Providers[1].Models[0].ID != "external" {
		t.Fatalf("refused stale add clobbered the external record: %+v", reg.Providers)
	}

	// (d) rename: the new name appeared under the prompt.
	code, _, errw = staleWrite([]string{"providers", "rename", "acme", "acme-main", "--yes"}, func(reg *providers.Registry) {
		reg.Providers = append(reg.Providers, providers.Provider{Name: "acme-main", BaseURL: "https://other.example/v1"})
	})
	if code != 1 || !strings.Contains(errw, "appeared in providers.json between the presentation and the write") {
		t.Fatalf("stale rename exit = %d stderr=%s; want the appeared refusal", code, errw)
	}
	// The refused rename left both records untouched: the old entry keeps its
	// name and the externally created one keeps its endpoint.
	if p, ok := reloadTestRegistry(t, path).Provider("acme"); !ok || p.Name != "acme" {
		t.Fatalf("refused stale rename wrote the file: acme = %+v (found=%v)", p, ok)
	}
	if p, ok := reloadTestRegistry(t, path).Provider("acme-main"); !ok || p.BaseURL != "https://other.example/v1" {
		t.Fatalf("refused stale rename clobbered the external record: acme-main = %+v (found=%v)", p, ok)
	}

	// (e) rename: the old record moved.
	code, _, errw = staleWrite([]string{"providers", "rename", "acme", "acme-main", "--yes"}, func(reg *providers.Registry) {
		reg.Providers[0].Models = append(reg.Providers[0].Models, providers.Model{ID: "acme-third"})
	})
	if code != 1 || !strings.Contains(errw, "changed on disk between the presentation and the write") {
		t.Fatalf("moved rename exit = %d stderr=%s; want the changed refusal", code, errw)
	}
	if got := reloadTestRegistry(t, path).Providers[0]; got.Name != "acme" || len(got.Models) != 3 {
		t.Fatalf("refused moved rename wrote the file: %+v", got)
	}
}

// TestRunSettingsCmd_ProviderVerbs_DiffShape pins the rendered line shapes
// against the TUI provider approvals (screen_settings.go providerLine /
// countsLine / approveProviderAdd / renameProvider, screen_providers.go
// confirmDelete): the provider name and model count on one diff-context line,
// the model-count before/after line, and the +/- prefix per added/removed
// entry. Every provider NAME is a registry slot and renders masked.
func TestRunSettingsCmd_ProviderVerbs_DiffShape(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = &providers.Registry{Providers: []providers.Provider{
		{
			Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{{ID: "acme-fast"}, {ID: "acme-slow"}},
		},
	}}

	// Remove: the whole entry and its model list leave.
	code, out, _ := runSettings(t, []string{"providers", "delete", "acme"}, "")
	if code == 0 {
		t.Fatal("pipe delete without --yes must refuse")
	}
	for _, want := range []string{
		"Removes provider acme from the registry. Key values are untouched.",
		"Model count: 2 -> 0",
		"- provider acme (https://acme.example/v1, 2 models)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete diff missing %q: stdout=%s", want, out)
		}
	}

	// Rename: the model list carries over, the name moves.
	code, out, _ = runSettings(t, []string{"providers", "rename", "acme", "acme-main"}, "")
	if code == 0 {
		t.Fatal("pipe rename without --yes must refuse")
	}
	for _, want := range []string{
		"Renames provider acme to acme-main; the model list carries over unchanged.",
		"Model count: 2 -> 2",
		"- provider acme (https://acme.example/v1, 2 models)",
		"+ provider acme-main (https://acme.example/v1, 2 models)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rename diff missing %q: stdout=%s", want, out)
		}
	}

	// Add of a new name: the count starts at 0 and one line is added.
	specPath := providerVerbSpecFile(t,
		`{"name":"delta","baseURL":"https://delta.example/v1","apiKeyEnv":"DELTA_KEY","models":[{"id":"d1"}]}`)
	code, out, _ = runSettings(t, []string{"providers", "add-json", specPath}, "")
	if code == 0 {
		t.Fatal("pipe add-json without --yes must refuse")
	}
	for _, want := range []string{
		"Adds provider delta to the registry. Key values are untouched.",
		"Model count: 0 -> 1",
		"+ provider delta (https://delta.example/v1, 1 model)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("add diff missing %q: stdout=%s", want, out)
		}
	}

	// A same-named add is a wholesale replace: the old entry is shown leaving
	// (with its model count) before the new one arrives.
	replacePath := providerVerbSpecFile(t,
		`{"name":"acme","baseURL":"https://acme.example/v2","apiKeyEnv":"ACME_KEY","models":[{"id":"acme-fast"}]}`)
	code, out, _ = runSettings(t, []string{"providers", "add-json", replacePath}, "")
	if code == 0 {
		t.Fatal("pipe add-json replace without --yes must refuse")
	}
	for _, want := range []string{
		"Replaces provider acme wholesale: the current entry and its whole model list are discarded.",
		"Model count: 2 -> 1",
		"- provider acme (https://acme.example/v1, 2 models)",
		"+ provider acme (https://acme.example/v2, 1 model)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replace diff missing %q: stdout=%s", want, out)
		}
	}

	// Registry-slot masking: a credential-shaped provider name on disk renders
	// masked in every line that carries it (the same authority the model diffs
	// use), and never raw.
	const plant = testfixtures.SKLive
	masked := providers.MaskSlot(plant)
	if masked == plant {
		t.Fatalf("test premise broken: MaskSlot(%q) did not mask", plant)
	}
	tc = installSettingsCore(t)
	tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: plant, BaseURL: "https://planted.example/v1"}}}
	code, out, errw := runSettings(t, []string{"providers", "delete", plant}, "")
	if code == 0 {
		t.Fatalf("pipe delete of a key-shaped name exit = 0; want a refusal (stderr=%s)", errw)
	}
	if strings.Contains(out, plant) || strings.Contains(errw, plant) {
		t.Errorf("delete transcript echoes the planted name raw:\nstdout=%s\nstderr=%s", out, errw)
	}
	if !strings.Contains(out, "- provider "+masked+" (https://planted.example/v1, 0 models)") {
		t.Errorf("delete diff must render the masked provider name: stdout=%s", out)
	}

	// Same for the rename door on both name slots.
	tc = installSettingsCore(t)
	tc.reg = &providers.Registry{Providers: []providers.Provider{{Name: plant, BaseURL: "https://planted.example/v1"}}}
	code, out, _ = runSettings(t, []string{"providers", "rename", plant, "renamed-main"}, "")
	if code == 0 {
		t.Fatal("pipe rename of a key-shaped name must refuse")
	}
	if !strings.Contains(out, "Renames provider "+masked+" to renamed-main") ||
		!strings.Contains(out, "- provider "+masked+" (https://planted.example/v1, 0 models)") {
		t.Errorf("rename diff must render the masked old name: stdout=%s", out)
	}
}

// TestRunSettingsCmd_ProviderVerbs_MissingTargetRefused pins the not-found// door: rename and delete refuse before the gate when the addressed record is
// not on disk (the TUI twins refuse before their modal for the same reason), so
// no diff is presented for a write that would address nothing.
func TestRunSettingsCmd_ProviderVerbs_MissingTargetRefused(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	before := diskBytes(t, path)

	for _, args := range [][]string{
		{"providers", "delete", "ghost", "--yes"},
		{"providers", "rename", "ghost", "ghost-main", "--yes"},
	} {
		code, out, errw := runSettings(t, args, "")
		if code != 1 {
			t.Fatalf("%v exit = %d; want 1 (stdout=%s stderr=%s)", args, code, out, errw)
		}
		if !strings.Contains(errw, "not found in providers.json; nothing written") {
			t.Errorf("%v must name the missing record: stderr=%s", args, errw)
		}
		if strings.Contains(out, "Approve Registry Write") {
			t.Errorf("%v must not present a diff for a non-existent record: stdout=%s", args, out)
		}
		if !bytes.Equal(diskBytes(t, path), before) {
			t.Errorf("%v wrote providers.json", args)
		}
	}

	// The rename new-name credential gate keeps its established class sentence
	// and refuses before any diff, so the refused value never reaches the
	// transcript.
	code, out, errw := runSettings(t, []string{"providers", "rename", "acme", testfixtures.SKLive, "--yes"}, "")
	if code != 1 {
		t.Fatalf("key-shaped new name exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if !strings.Contains(errw, "looks like secret/key material (value hidden)") {
		t.Errorf("refusal must be the named value-free sentence: stderr=%s", errw)
	}
	if strings.Contains(out, testfixtures.SKLive) || strings.Contains(out, "Approve Registry Write") {
		t.Errorf("a value-refused rename must not present a diff: stdout=%s", out)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("a refused rename wrote providers.json")
	}
}

// TestRunSettingsCmd_ProviderVerbs_FreshHomeAdd pins the factory-reset home
// (live ~/.agy-pro has no providers.json): the presentation renders the
// "new entry" diff against an empty registry, and the write-time re-verify
// treats the still-missing file as an empty registry instead of refusing every
// first write. Any other load error stays fail-closed.
func TestRunSettingsCmd_ProviderVerbs_FreshHomeAdd(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	prev := newSettingsCore
	newSettingsCore = func() *settings.Core {
		return settings.NewCore(settings.Deps{
			VersionFunc: func() string { return "0.0-test" },
			LoadReg:     providers.Load,
			SaveReg: func(r *providers.Registry) error {
				r.Path = path
				return saveTestRegistry(r)
			},
		})
	}
	t.Cleanup(func() { newSettingsCore = prev })

	specPath := providerVerbSpecFile(t,
		`{"name":"first","baseURL":"https://first.example/v1","apiKeyEnv":"FIRST_KEY","models":[{"id":"f1"}]}`)

	// Pipe refusal: the diff still presents, and no file appears.
	code, out, errw := runSettings(t, []string{"providers", "add-json", specPath}, "")
	if code == 0 {
		t.Fatalf("fresh-home add without --yes exit = 0; want a refusal (stderr=%s)", errw)
	}
	if !strings.Contains(out, "Adds provider first to the registry.") || !strings.Contains(out, "Model count: 0 -> 1") {
		t.Errorf("fresh-home refusal diff wrong: stdout=%s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused fresh-home add created providers.json (err=%v)", err)
	}

	// --yes consents: the re-verify accepts the still-absent file and the first
	// entry is created.
	code, out, errw = runSettings(t, []string{"providers", "add-json", specPath, "--yes"}, "")
	if code != 0 {
		t.Fatalf("fresh-home add --yes exit = %d stderr=%s (stdout=%s)", code, errw, out)
	}
	reg := reloadTestRegistry(t, path)
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "first" {
		t.Fatalf("registry after fresh-home add = %+v", reg.Providers)
	}

	// delete/rename still refuse a name that is not on disk, with nothing
	// written.
	before := diskBytes(t, path)
	code, _, errw = runSettings(t, []string{"providers", "delete", "ghost", "--yes"}, "")
	if code != 1 || !strings.Contains(errw, "not found in providers.json; nothing written") {
		t.Fatalf("fresh-home delete of a missing name exit = %d stderr=%s", code, errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("fresh-home delete of a missing name wrote providers.json")
	}
}

// TestProSettingsUsage_ProviderVerbsNameYes pins the help-surface parity: the
// three gated provider verbs advertise --yes exactly like the model twins, so
// the non-interactive recovery step is discoverable from the usage text.
func TestProSettingsUsage_ProviderVerbsNameYes(t *testing.T) {
	for _, want := range []string{
		"add-json <file|-> [--yes]",
		"rename <old> <new> [--yes]",
		"delete <name> [--yes]",
		"model-add-json <name> <file|-> [--yes]",
		"model-update <name> <model-id> <file|-> [--yes]",
		"model-delete <name> <model-id> [--yes]",
	} {
		if !strings.Contains(proSettingsUsage, want) {
			t.Errorf("usage text missing %q:\n%s", want, proSettingsUsage)
		}
	}
}
