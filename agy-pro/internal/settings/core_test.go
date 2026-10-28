package settings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/integrations"
	"agypro/internal/inventory"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/swarm"
)

// fakeStore is an in-memory swarm.Store for tests.
type fakeStore struct{ presets []swarm.SavedPreset }

func (f *fakeStore) LoadPresets() ([]swarm.SavedPreset, error) { return f.presets, nil }
func (f *fakeStore) SavePresets(p []swarm.SavedPreset) error {
	f.presets = p
	return nil
}

// fakeDeps builds Deps around in-memory state/registry, no live files/network.
type fakeDeps struct {
	state    *mode.State
	reg      *providers.Registry
	saved    int
	store    *fakeStore
	audits   []providers.KeyAudit
	fixed    []string
	rolled   []string
	doctorN  int
	switched []string
	restored int
}

func newFakeDeps() *fakeDeps {
	return &fakeDeps{
		state: &mode.State{CurrentMode: mode.ModeClean, DefaultProfile: mode.ProfilePrompt, BridgeTools: "read,search", NativeSeatConcurrency: 2},
		reg:   &providers.Registry{},
		store: &fakeStore{},
	}
}

func (f *fakeDeps) deps() Deps {
	return Deps{
		LoadState: func() (*mode.State, error) { return f.state, nil },
		SaveState: func(st *mode.State) error {
			f.state = st
			f.saved++
			return nil
		},
		LoadReg: func() (*providers.Registry, error) { return f.reg, nil },
		SaveReg: func(reg *providers.Registry) error {
			f.reg = reg
			f.saved++
			return nil
		},
		AuditKeys: func(reg *providers.Registry, opts providers.AuditOptions) []providers.KeyAudit {
			return f.audits
		},
		FixKeys:      func(target string) error { f.fixed = append(f.fixed, target); return nil },
		RollbackKeys: func(dir string) error { f.rolled = append(f.rolled, dir); return nil },
		Doctor:       func(out io.Writer, strict bool) error { f.doctorN++; return nil },
		Inventory: func(ctx context.Context) (*inventory.Inventory, error) {
			return &inventory.Inventory{}, nil
		},
		SwarmStore:  f.store,
		VersionFunc: func() string { return "0.0-test" },
		SwitchMode: func(target string, in io.Reader, out io.Writer) error {
			f.switched = append(f.switched, target)
			return nil
		},
		RestoreMode: func(out io.Writer) error {
			f.restored++
			return nil
		},
	}
}

func TestCoreSet(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())

	if err := c.Set("default-profile", "gateway"); err != nil {
		t.Fatalf("set default-profile: %v", err)
	}
	if f.state.DefaultProfile != mode.ProfileGateway {
		t.Errorf("state.DefaultProfile = %q", f.state.DefaultProfile)
	}
	if err := c.Set("bridge-tools", "read,search,write"); err != nil {
		t.Fatalf("set bridge-tools: %v", err)
	}
	if f.state.BridgeTools != "read,search,write" {
		t.Errorf("state.BridgeTools = %q", f.state.BridgeTools)
	}
	if err := c.Set("native_seat_concurrency", "5"); err != nil {
		t.Fatalf("set concurrency: %v", err)
	}
	if f.state.NativeSeatConcurrency != 5 {
		t.Errorf("concurrency = %d", f.state.NativeSeatConcurrency)
	}

	// Rejections: bad profile, bad tools, out-of-range concurrency, unknown key.
	if err := c.Set("default-profile", "nope"); err == nil {
		t.Error("bad profile accepted")
	}
	if err := c.Set("bridge-tools", "read,destroy"); err == nil {
		t.Error("bad bridge tools accepted")
	}
	if err := c.Set("native-seat-concurrency", "9"); err == nil {
		t.Error("concurrency 9 accepted")
	}
	if err := c.Set("nope", "x"); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestCoreProviderRoundTrip(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())

	if err := c.ProviderAdd(ProviderSpec{
		Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY",
		Models: []providers.Model{{ID: "m1"}},
	}); err != nil {
		t.Fatalf("ProviderAdd: %v", err)
	}
	if err := c.ProviderRename("acme", "acme2"); err != nil {
		t.Fatalf("ProviderRename: %v", err)
	}
	if err := c.ProviderRename("acme2", "acme3"); err != nil {
		t.Fatalf("ProviderRename 2: %v", err)
	}
	if err := c.ProviderDelete("acme3"); err != nil {
		t.Fatalf("ProviderDelete: %v", err)
	}
	if len(f.reg.Providers) != 0 {
		t.Errorf("registry not empty after delete: %+v", f.reg.Providers)
	}
	if err := c.ProviderDelete("ghost"); err == nil {
		t.Error("deleting a missing provider should fail")
	}
}

func TestCoreProviderAdd_RejectsKeyValues(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	secret := "sk-plaintext-key-should-not-land-here"
	err := c.ProviderAdd(ProviderSpec{Name: "x", BaseURL: "https://x.example/v1", APIKeyEnv: secret})
	if err == nil {
		t.Fatal("key VALUE accepted in apiKeyEnv")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error echoes the key: %v", err)
	}
	if len(f.reg.Providers) != 0 {
		t.Errorf("registry mutated on rejection: %+v", f.reg.Providers)
	}
}

func TestCoreProviderAddJSON_RejectsKeyValues(t *testing.T) {
	secret := "sk-json-key-value-abcdef123456"
	spec, err := ParseProviderSpecJSON([]byte(`{"name":"x","baseURL":"https://x.example/v1","apiKeyEnv":"` + secret + `","models":[]}`))
	if err == nil {
		t.Fatal("ParseProviderSpecJSON accepted a key VALUE")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error echoes the key: %v", err)
	}
	spec, err = ParseProviderSpecJSON([]byte(`{"name":"x","baseURL":"https://x.example/v1","apiKeyEnv":"X_KEY","models":[{"id":"m1"}]}`))
	if err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if spec.APIKeyEnv != "X_KEY" || spec.Name != "x" {
		t.Errorf("spec = %+v", spec)
	}
}

func TestCoreProviderTest(t *testing.T) {
	f := newFakeDeps()
	f.audits = []providers.KeyAudit{{
		Provider: "acme", Env: "ACME_KEY", Resolved: true,
		Probe: providers.KeyProbeResult{Status: providers.ProbeValid},
	}}
	c := NewCore(f.deps())
	if err := c.ProviderAdd(ProviderSpec{Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := c.ProviderTest("acme"); err != nil {
		t.Fatalf("ProviderTest: %v", err)
	}
	f.audits[0].Probe = providers.KeyProbeResult{Status: providers.ProbeInvalid, Detail: "401"}
	if err := c.ProviderTest("acme"); err == nil {
		t.Error("invalid probe should fail ProviderTest")
	}
	if err := c.ProviderTest("ghost"); err == nil {
		t.Error("unknown provider should fail ProviderTest")
	}
}

func TestCoreDoctorSeams(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	var b bytes.Buffer
	if err := c.DoctorDiagnose(&b, true); err != nil {
		t.Fatalf("DoctorDiagnose: %v", err)
	}
	if f.doctorN != 1 {
		t.Errorf("doctor seam not called")
	}
	if err := c.DoctorFixKeys("all"); err != nil {
		t.Fatalf("DoctorFixKeys: %v", err)
	}
	if err := c.DoctorRollbackKeys("/tmp/backup"); err != nil {
		t.Fatalf("DoctorRollbackKeys: %v", err)
	}
	if len(f.fixed) != 1 || f.fixed[0] != "all" {
		t.Errorf("fix seam calls = %v", f.fixed)
	}
	if len(f.rolled) != 1 || f.rolled[0] != "/tmp/backup" {
		t.Errorf("rollback seam calls = %v", f.rolled)
	}
}

// TestViewD2Coverage is the D2 acceptance test (contract §2.9 / Appendix C):
// every settings surface from the research §2 inventory must be reachable
// through Core verbs or View() items — nothing may be flags-only.
func TestViewD2Coverage(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "google", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", APIKeyEnv: "GEMINI_API_KEY"},
	}}
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}

	// Canonical D2 surface (contract U6 test list) + every research §2 row.
	required := []string{
		// §2.1 core config (rows 1-7)
		"default_profile", "bridge_tools", "config.show", "current_mode",
		"managed_items", "last_backup", "updated_at", "env.AGY_PRO_BRIDGE_TOOLS",
		// §2.2 profile mode ops (rows 8-11)
		"mode.switch", "mode.restore", "mode.backups", "mode.status",
		// §2.3 providers (rows 12-20, GAP rows 15-17 = F-PROV)
		"providers.add_interactive", "providers.add_json", "providers.test",
		"providers.rename", "providers.delete",
		"providers.model_capabilities", "providers.presets", "providers.key_policy",
		"provider.key.google",
		// §2.4 fanout/swarm (rows 21-30)
		"fanout.n", "fanout.timeout", "fanout.max_turns", "fanout.tools",
		"fanout.task", "fanout.flags", "fanout.registry_defaults",
		"native_seat_concurrency", "fanout.consensus", "fanout.failover",
		"swarm.exclude_models", "swarm.templates", "swarm.presets", "swarm.save_preset",
		// §2.5 budgets (read-only rows)
		"budget.iterations", "budget.wall", "budget.turn", "budget.tool",
		"budget.tool_calls_per_turn", "budget.fallback",
		// §2.6 doctor/keys/update/uninstall (rows 31-37)
		"doctor.diagnose", "doctor.fix_keys", "doctor.rollback_keys",
		"doctor.exit_semantics", "update.info", "uninstall.info", "install.info",
		"version.info", "version.shim_path", "version.real_agy",
		// §2.7 environment variables docs panel
		"env.AGY_PRO_HOME", "env.AGY_PRO_PROVIDERS", "env.AGY_PRO_REAL_AGY",
		"env.AGY_PRO_ACTIVE", "env.GEMINI_CONFIG_DIR", "env.AGY_PRO_WORKSPACE",
		"env.AGY_PRO_BRIDGE", "env.AGY_LAUNCHER",
	}
	present := map[string]bool{}
	for _, id := range view.ItemIDs() {
		present[id] = true
	}
	var missing []string
	for _, id := range required {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("D2 gap: %d surfaces missing from View(): %v", len(missing), missing)
	}

	// The F-PROV verbs must be invocable through Core (not flags-only).
	if !hasItem(view, "providers.rename") || !hasItem(view, "providers.delete") || !hasItem(view, "providers.add_json") {
		t.Error("F-PROV verb surface missing from view")
	}

	// Every view item must be non-empty-identified and carry a source label.
	for _, sec := range view.Sections {
		if sec.ID == "" || sec.Title == "" {
			t.Errorf("section missing id/title: %+v", sec)
		}
		// P1-6: the hub is the only numbered surface. The compiled title is
		// the fallback the renderers paint when a catalog key is missing, so it
		// must not carry a bracketed index either.
		if strings.HasPrefix(sec.Title, "[") {
			t.Errorf("section %s title %q carries a bracketed index; the hub is the only numbered surface", sec.ID, sec.Title)
		}
		for _, it := range sec.Items {
			if it.ID == "" || it.Label == "" {
				t.Errorf("item missing id/label in %s: %+v", sec.ID, it)
			}
			switch it.Source {
			case "env", "state", "registry", "default", "constant",
				// Appearance rows (plan §4/§5) carry display-ready source
				// text instead of the enum shorthand.
				"state.json", "default: English", "default (off)", "env AGY_LAUNCHER (override)":
			default:
				t.Errorf("item %s has bogus source %q", it.ID, it.Source)
			}
		}
	}
}

func hasItem(v SettingsView, id string) bool {
	_, ok := v.FindItem(id)
	return ok
}

// TestViewFanoutTimeoutRendersSharedConstant (2026-10-14, residual round): the
// "Fanout Wall Timeout (--timeout)" row must render the shared
// fanout.DefaultTimeoutSec (300s), not a hardcoded "120s" string, so the
// settings panel cannot drift from the CLI flag truth again.
func TestViewFanoutTimeoutRendersSharedConstant(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("fanout.timeout")
	if !ok {
		t.Fatal("fanout.timeout item missing from View()")
	}
	want := fmt.Sprintf("%ds", fanout.DefaultTimeoutSec)
	if it.Value != want || it.Default != want {
		t.Errorf("fanout.timeout = Value:%q Default:%q; want %q (the shared constant)", it.Value, it.Default, want)
	}
	if it.Value == "120s" {
		t.Error("fanout.timeout still renders the stale 120s string")
	}
}

// TestViewEnvBridgeRowReadsBothVariables (P6-4): the env docs row documents
// AGY_PRO_BRIDGE and its AGY_BRIDGE legacy alias on one row, and launcher.go
// enables the bridge when EITHER is "1", so the row value must show both. It
// used to read only the first variable, which made the legacy one uninspectable
// from the panel whose job is env documentation.
func TestViewEnvBridgeRowReadsBothVariables(t *testing.T) {
	t.Setenv("AGY_PRO_BRIDGE", "")
	t.Setenv("AGY_BRIDGE", "1")
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("env.AGY_PRO_BRIDGE")
	if !ok {
		t.Fatal("env.AGY_PRO_BRIDGE item missing from View()")
	}
	for _, want := range []string{"AGY_PRO_BRIDGE=(unset)", "AGY_BRIDGE=1"} {
		if !strings.Contains(it.Value, want) {
			t.Errorf("env.AGY_PRO_BRIDGE Value %q does not carry %q (both variables must be readable)", it.Value, want)
		}
	}
}

// TestViewMasksSecrets (A4/U8): a planted key value never appears in the view
// or its JSON form; the canonical mask does.
func TestViewMasksSecrets(t *testing.T) {
	f := newFakeDeps()
	secret := "sk-PLANTED-view-0123456789abcdef"
	t.Setenv("VIEW_KEY", secret)
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "leak", BaseURL: "https://leak.example/v1", APIKeyEnv: "VIEW_KEY"},
	}}
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var b strings.Builder
	for _, sec := range view.Sections {
		b.WriteString(sec.Title)
		for _, it := range sec.Items {
			b.WriteString(it.ID + it.Label + it.Value + it.Note)
		}
	}
	all := b.String()
	if strings.Contains(all, secret) {
		t.Error("View leaks the raw key value")
	}
	if !strings.Contains(all, providers.MaskSecret(secret)) {
		t.Errorf("View lacks the canonical mask %q", providers.MaskSecret(secret))
	}

	// JSON snapshot path too.
	var jb bytes.Buffer
	if err := WriteSnapshot(&jb, c, true); err != nil {
		t.Fatalf("WriteSnapshot json: %v", err)
	}
	if strings.Contains(jb.String(), secret) {
		t.Error("show --json leaks the raw key value")
	}
}

func TestWriteSnapshotText(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	var b bytes.Buffer
	if err := WriteSnapshot(&b, c, false); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"agy-pro Settings Snapshot",
		"Default Launch Profile",
		"All secret values render as first-5 + ****",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q", want)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("snapshot must be ANSI-free (C3)")
	}
}

func TestParseBridgeToolsSpec(t *testing.T) {
	if v, err := ParseBridgeToolsSpec("READ, SEARCH"); err != nil || v != "read,search" {
		t.Errorf("canonicalize = (%q, %v)", v, err)
	}
	if v, err := ParseBridgeToolsSpec("none"); err != nil || v != "none" {
		t.Errorf("none = (%q, %v)", v, err)
	}
	if _, err := ParseBridgeToolsSpec("read,destroy"); err == nil {
		t.Error("unknown token accepted")
	}
}

func TestNewCore_FillsDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGY_PRO_HOME", tmp)
	t.Setenv("AGY_PRO_PROVIDERS", tmp+"/providers.json")
	c := NewCore(Deps{})
	if c == nil {
		t.Fatal("NewCore returned nil")
	}
	// Default seams must not panic on basic use.
	var b bytes.Buffer
	if err := WriteSnapshot(&b, c, false); err != nil {
		t.Fatalf("default-Core snapshot: %v", err)
	}
	if b.Len() == 0 {
		t.Error("default-Core snapshot is empty")
	}
}

// TestNewCoreSwarmStateStoreSeam pins the F-3 wiring seam: the default Core
// builds the on-disk swarm store and hands it back so a front-end can redirect
// the corrupt-file warning, while an injected store (the test fakes, an
// embedder) reports nil instead of pretending to be a file the caller could
// re-sink.
func TestNewCoreSwarmStateStoreSeam(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("AGY_PRO_HOME", tmp)

	def := NewCore(Deps{})
	fs := def.SwarmStateStore()
	if fs == nil {
		t.Fatal("default Core must expose the file-backed swarm store")
	}
	want := tmp + string(os.PathSeparator) + "swarm-state.json"
	if fs.Path != want {
		t.Fatalf("store path = %q; want %q", fs.Path, want)
	}
	if fs.WarnOut == nil {
		t.Fatal("default store must keep its os.Stderr warning sink until a front-end redirects it")
	}

	if injected := NewCore(Deps{SwarmStore: &fakeStore{}}); injected.SwarmStateStore() != nil {
		t.Fatal("an injected non-file store must not report a *swarm.FileStore")
	}
}

// --- P4-5: the model exclusion list (D1=(a) providers.json
// defaults.excludeModels; D2=(b) one rule: auto-seating and failover skip an
// excluded entry, an explicit pick is honored with a warning, nothing is ever
// rejected). Every test here runs on a temp AGY_PRO_HOME / providers path, so
// the live registry is never read or written.

// excludeCore seeds a registry holding two models (one k3-token id) and returns
// the Core plus the temp path.
func excludeCore(t *testing.T, d providers.Defaults) (*Core, string) {
	t.Helper()
	return fileBackedCore(t, &providers.Registry{
		Defaults: d,
		Providers: []providers.Provider{{
			Name:      "nw",
			BaseURL:   "https://nw.example/v1",
			APIKeyEnv: "NW_API_KEY",
			Models:    []providers.Model{{ID: "kimi-k3"}, {ID: "glm-5"}},
		}},
	})
}

// TestExcludeModelsSeed pins the editor's starting list, the rule that makes a
// first save write the built-in default rule out explicitly instead of starting
// from nothing. The seed is pinned by its LITERAL spelling and against the
// shipped constant, because the whole point of the entry is which spelling it
// is: the bare "k3" entry does not reproduce the built-in family predicate, so a
// seed spelled that way would weaken the policy on first save.
func TestExcludeModelsSeed(t *testing.T) {
	absent := ExcludeModelsSeed(providers.Defaults{})
	if len(absent) != 1 || absent[0] != "family:k3" {
		t.Fatalf("absent-key seed = %v; want exactly the built-in default entry family:k3", absent)
	}
	if absent[0] != ExcludeModelsBuiltinRule {
		t.Fatalf("absent-key seed = %q; want the shipped constant %q", absent[0], ExcludeModelsBuiltinRule)
	}
	empty := ExcludeModelsSeed(providers.Defaults{ExcludeModels: []string{}})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("owned-empty seed = %v; want an empty non-nil list (the user owns it)", empty)
	}
	src := []string{"nw", "glm-5"}
	seeded := ExcludeModelsSeed(providers.Defaults{ExcludeModels: src})
	if strings.Join(seeded, ",") != "nw,glm-5" {
		t.Fatalf("populated seed = %v", seeded)
	}
	seeded[0] = "mutated"
	if src[0] != "nw" {
		t.Fatal("ExcludeModelsSeed aliased the caller's slice; an editor draft would mutate the registry")
	}
}

// excludeVerdictTable is the pool the built-in rule and its list spelling are
// compared over: a family-declared k3 row, two id-token rows, and the boundary
// rows the token rule must NOT catch. It is the same shape the TUI trap test
// walks, kept here so the Core surface is pinned independently of the UI.
var excludeVerdictTable = []struct{ provider, model, family string }{
	{"nw", "kimi-latest", "k3"},
	{"nw", "kimi-k3", "kimi"},
	{"nw", "k3-agent-swarm", "k"},
	{"nw", "KIMI-K3", ""},
	{"nw", "k3000", "k"},
	{"nw", "glm-k3x", "glm"},
	{"nw", "kimi-k2", "kimi"},
	{"nw", "glm-5", "glm"},
}

// TestExcludeModelsBuiltinRuleReproducesPredicate is the Core-side proof that the
// constant the row and the seed carry is the EXACT reproduction of the built-in
// default: the policy built from the seeded list answers every row of the pool
// exactly as the absent-key policy does. The bare "k3" pre-fix spelling is the
// non-vacuity half: it must disagree, or the pin would pass on any spelling.
func TestExcludeModelsBuiltinRuleReproducesPredicate(t *testing.T) {
	if ExcludeModelsBuiltinRule != "family:k3" {
		t.Fatalf("ExcludeModelsBuiltinRule = %q; want family:k3 (the family form the grammar reproduces the predicate with)", ExcludeModelsBuiltinRule)
	}
	// preFixEntry is the bare spelling that shipped before the family form: it
	// names a provider or a model literally called k3 and does NOT reproduce
	// the built-in rule. It is the non-vacuity half of this pin and of the
	// internal/tui first-save trap probe, both of which inline it.
	const preFixEntry = "k3"
	if preFixEntry == ExcludeModelsBuiltinRule {
		t.Fatal("the pre-fix bare entry equals the shipped rule; the trap probe in internal/tui would no longer prove anything")
	}
	builtin := providers.ExclusionPolicyFromDefaults(providers.Defaults{})
	seeded := providers.ExclusionPolicy{User: ExcludeModelsSeed(providers.Defaults{})}
	if !seeded.UserOwned() {
		t.Fatal("the seeded list is not user-owned; the predicate comparison would read the built-in rule twice")
	}
	agreed := 0
	for _, row := range excludeVerdictTable {
		want := builtin.Excludes(row.provider, row.model, row.family)
		if got := seeded.Excludes(row.provider, row.model, row.family); got != want {
			t.Errorf("seeded list on %s/%s (family %q) = %v; the built-in rule says %v", row.provider, row.model, row.family, got, want)
		}
		if want {
			agreed++
		}
	}
	if agreed == 0 || agreed == len(excludeVerdictTable) {
		t.Fatalf("the pool does not discriminate (%d of %d excluded); the comparison is vacuous", agreed, len(excludeVerdictTable))
	}
	preFix := providers.ExclusionPolicy{User: []string{preFixEntry}}
	if preFix.Excludes("nw", "kimi-latest", "k3") {
		t.Fatal("the pre-fix bare entry already reproduced the family-declared row; the spelling would not matter")
	}
}

// TestParseExcludeModelsMirrorsLoaderRules pins the row shapes the editor door
// and the registry loader must agree on (S7-C2, S7-C3, S7-C9). ParseExcludeModels
// now DELEGATES to the shared authority (providers.ValidateExcludeModels, the
// write-door half of the code Registry.Validate runs), so this table is the
// door-level statement of the agreement and
// TestParseExcludeModelsDelegatesToProviderAuthority is the mechanical one. The
// two doors differ in exactly one documented way: the loader DROPS an entry that
// is blank after the trim and this door REFUSES it.
func TestParseExcludeModelsMirrorsLoaderRules(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantNil bool   // the accepted result must stay nil (the absent-key shape)
		wantErr string // substring of the refusal; "" = accepted
	}{
		{name: "trims surroundings", in: []string{" nw ", " nw/glm-5 "}, want: []string{"nw", "nw/glm-5"}},
		// The nil-vs-empty distinction rides through this door: nil stays nil (the
		// key is absent, the built-in default governs) and [] stays a non-nil empty
		// list (the user owns the list and excludes nothing).
		{name: "nil list stays nil", in: nil, want: nil, wantNil: true},
		{name: "explicit empty stays non-nil empty", in: []string{}, want: []string{}},
		{name: "case is preserved", in: []string{"NW"}, want: []string{"NW"}},
		{name: "valid provider", in: []string{"nw"}, want: []string{"nw"}},
		{name: "valid model id", in: []string{"glm-5"}, want: []string{"glm-5"}},
		{name: "composite provider/model", in: []string{"nw/glm-5"}, want: []string{"nw/glm-5"}},
		{name: "family form accepted", in: []string{"family:k3"}, want: []string{"family:k3"}},
		// S7-C2: the loader's family: remainder rule was missing here, so a nameless
		// entry slipped through this door and only failed later as a different
		// sentinel from Registry.Validate.
		{name: "nameless family refused", in: []string{"family:"}, wantErr: "needs a name"},
		{name: "blank family remainder refused", in: []string{"family:   "}, wantErr: "needs a name"},
		{name: "blank entry refused", in: []string{"nw", "   "}, wantErr: "must not be empty"},
		// S7-C3: an all-blank list must not reach the file as the user-owned empty
		// list, which would silently drop the built-in k3 default. This door refuses
		// at the FIRST blank entry (an editor must not discard a typed row), so the
		// wording is the per-entry one; the loader's list-level wording is pinned in
		// the providers table (TestNormalizeExcludeModels / TestExcludeModelsDoorsAgree).
		{name: "blank-only list refused", in: []string{"", "  "}, wantErr: "must not be empty"},
		{name: "interior space refused", in: []string{"nw glm-5"}, wantErr: "must not contain whitespace"},
		{name: "ESC refused", in: []string{"\x1bnw"}, wantErr: "control characters"},
		{name: "leading tab refused before the trim", in: []string{"\tnw"}, wantErr: "control characters"},
		{name: "NEL refused, not trimmed away", in: []string{"\u0085nw"}, wantErr: "control characters"},
		// S7-C9: the Cf class (bidi, zero-width, BOM) is refused at this door too.
		{name: "zero-width joiner refused", in: []string{"nw\u200d"}, wantErr: "control characters"},
		{name: "bidi override in a family name refused", in: []string{"family:k\u202e3"}, wantErr: "control characters"},
		{name: "BOM refused", in: []string{"\ufeffnw"}, wantErr: "control characters"},
	}
	accepted, refused := 0, 0
	for _, tc := range cases {
		got, err := ParseExcludeModels(tc.in)
		if tc.wantErr != "" {
			refused++
			if err == nil {
				t.Errorf("%s: accepted %v; want a refusal", tc.name, tc.in)
				continue
			}
			if !errors.Is(err, ErrExcludeModelsInvalid) {
				t.Errorf("%s: error %v is not ErrExcludeModelsInvalid", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: error %q does not name %q", tc.name, err.Error(), tc.wantErr)
			}
			if got != nil {
				t.Errorf("%s: a refusal returned entries %v", tc.name, got)
			}
			continue
		}
		accepted++
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.wantNil {
			if got != nil {
				t.Errorf("%s: got %v; want nil (the absent-key shape must survive the door)", tc.name, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: got nil; want a non-nil list (an explicit list is user-owned)", tc.name)
			continue
		}
		if strings.Join(got, "\x1f") != strings.Join(tc.want, "\x1f") {
			t.Errorf("%s: got %v; want %v", tc.name, got, tc.want)
		}
	}
	// Non-vacuity: the table must exercise both outcomes, or a parser that
	// accepted everything (or refused everything) would pass it.
	if accepted == 0 || refused == 0 {
		t.Fatalf("table is vacuous: %d accepted, %d refused", accepted, refused)
	}
}

// TestParseExcludeModelsDelegatesToProviderAuthority is the S7-C2/S7-C3/S7-C9
// mechanism proof: settings.ParseExcludeModels must not re-implement the entry
// rule, it must DELEGATE to the authority the loader itself runs. For every row
// shape the two functions answer identically (same verdict, same normalised
// list, same nil-ness), and the sentinel the editor branches on wraps the
// authority's own refusal instead of inventing its own wording.
func TestParseExcludeModelsDelegatesToProviderAuthority(t *testing.T) {
	rows := [][]string{
		{"nw"},
		{"glm-5"},
		{"nw/glm-5"},
		{"family:k3"},
		{"Family:K3"},
		{"  nw  ", "  nw/glm-5  "},
		{"family:"},
		{"family:   "},
		{"nw glm-5"},
		{"nw\x1b"},
		{"nw\u0085"},
		{"nw\u200d"},
		{"family:k\u202e3"},
		{"", "  "},
		{"nw", "   "},
		nil,
		{},
	}
	identical, diverged := 0, 0
	for i, in := range rows {
		got, err := ParseExcludeModels(in)
		want, wantErr := providers.ValidateExcludeModels(in)
		if (err != nil) != (wantErr != nil) {
			t.Errorf("row %d (%q): door err=%v authority err=%v", i, in, err, wantErr)
			continue
		}
		if err != nil {
			diverged++
			if !errors.Is(err, ErrExcludeModelsInvalid) {
				t.Errorf("row %d (%q): %v is not ErrExcludeModelsInvalid", i, in, err)
			}
			if got != nil {
				t.Errorf("row %d (%q): a refusal returned %v", i, in, got)
			}
			// The authority's refusal is carried inside the sentinel's error, so the
			// detail a front-end shows names the same field class the loader would.
			if !strings.Contains(err.Error(), wantErr.Error()) {
				t.Errorf("row %d (%q): %q does not carry the authority refusal %q", i, in, err, wantErr)
			}
			// Refusal-class register: `go test -run TestParseExcludeModelsDelegates
			// -v` lists the exact sentence each class produces, which is what a
			// reviewer reads instead of re-deriving the wrapped text by hand.
			t.Logf("refused row %d: %v", i, err)
			continue
		}
		identical++
		if (got == nil) != (want == nil) {
			t.Errorf("row %d (%q): nil-ness diverged: door=%v authority=%v", i, in, got, want)
			continue
		}
		if strings.Join(got, "\x1f") != strings.Join(want, "\x1f") {
			t.Errorf("row %d (%q): door=%q authority=%q", i, in, got, want)
		}
	}
	// Non-vacuity: a door that returned the input untouched, or one that refused
	// everything, would not produce both outcomes across these rows.
	if identical == 0 || diverged == 0 {
		t.Fatalf("table is vacuous: %d identical, %d refused", identical, diverged)
	}
}

// TestCoreSurfaceRefusesAllBlankExcludeModelsAtLoad is S7-C3 at the surface the
// operator actually sees: a providers.json under a temp AGY_PRO_HOME whose
// defaults.excludeModels is all blank must make the settings Core's own load
// fail, instead of rendering a row that reports "empty list: nothing is
// excluded" (the silent policy bypass, where a typo'd list dropped the built-in
// k3 default with no signal). The sibling case proves a valid list still loads
// and renders, so the refusal is about the blank shape only. Both paths resolve
// through $AGY_PRO_HOME/providers.json with EnvProviders cleared, exactly like
// the shipped binary; the live ~/.agy-pro home is never read or written.
func TestCoreSurfaceRefusesAllBlankExcludeModelsAtLoad(t *testing.T) {
	writeHome := func(t *testing.T, defaults string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv(providers.EnvHome, home)
		t.Setenv(providers.EnvProviders, "")
		body := `{"providers":[{"name":"nw","baseURL":"https://nw.example/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"kimi-k3"}]}],"defaults":` + defaults + `}`
		if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write providers.json: %v", err)
		}
	}

	t.Run("all-blank list refuses the load", func(t *testing.T) {
		writeHome(t, `{"excludeModels":["","  "]}`)
		c := NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})
		view, err := c.View()
		if err == nil {
			t.Fatalf("View accepted an all-blank excludeModels list and rendered it: %+v", view)
		}
		if !strings.Contains(err.Error(), "excludeModels") {
			t.Fatalf("error %q must name the excludeModels field", err)
		}
		// The write door refuses the same shape, so the operator cannot reach the
		// broken state through the editor either.
		if werr := c.SetExcludeModels([]string{"", "  "}, ExcludeModelsBase{}); !errors.Is(werr, ErrExcludeModelsInvalid) {
			t.Fatalf("SetExcludeModels(all blank) = %v; want ErrExcludeModelsInvalid", werr)
		}
	})

	t.Run("valid non-empty list still loads", func(t *testing.T) {
		writeHome(t, `{"excludeModels":[" nw ","family:k3"]}`)
		c := NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})
		view, err := c.View()
		if err != nil {
			t.Fatalf("View refused a valid list: %v", err)
		}
		it, ok := view.FindItem("swarm.exclude_models")
		if !ok {
			t.Fatal("row swarm.exclude_models missing")
		}
		for _, want := range []string{"nw", "family:k3"} {
			if !strings.Contains(it.Value, want) {
				t.Errorf("row value %q does not render the loaded entry %q", it.Value, want)
			}
		}
		if strings.Contains(strings.ToLower(it.Value), "built-in") {
			t.Errorf("row value %q still claims the built-in default applies: the key is present", it.Value)
		}
	})
}

// TestExcludeModelsBaseMatchesKeepsNilVsEmpty pins the write-time base check:
// the absent key and the user-owned empty list must never be treated as the
// same shape, or a stale write could resurrect the built-in default.
func TestExcludeModelsBaseMatchesKeepsNilVsEmpty(t *testing.T) {
	absent := ExcludeModelsBaseFromDefaults(providers.Defaults{})
	empty := ExcludeModelsBaseFromDefaults(providers.Defaults{ExcludeModels: []string{}})
	held := ExcludeModelsBaseFromDefaults(providers.Defaults{ExcludeModels: []string{"nw", "glm-5"}})

	if absent.Owned || empty.Owned && len(empty.Entries) != 0 {
		t.Fatalf("snapshot ownership wrong: absent=%v empty=%v", absent.Owned, empty.Entries)
	}
	rows := []struct {
		name string
		base ExcludeModelsBase
		d    providers.Defaults
		want bool
	}{
		{"absent base matches an absent key", absent, providers.Defaults{}, true},
		{"absent base rejects an owned empty list", absent, providers.Defaults{ExcludeModels: []string{}}, false},
		{"empty base matches an owned empty list", empty, providers.Defaults{ExcludeModels: []string{}}, true},
		{"empty base rejects the absent key", empty, providers.Defaults{}, false},
		{"held base matches the same entries", held, providers.Defaults{ExcludeModels: []string{"nw", "glm-5"}}, true},
		{"held base rejects a reorder", held, providers.Defaults{ExcludeModels: []string{"glm-5", "nw"}}, false},
		{"held base rejects a shorter list", held, providers.Defaults{ExcludeModels: []string{"nw"}}, false},
		{"held base rejects the absent key", held, providers.Defaults{}, false},
	}
	hits := 0
	for _, tc := range rows {
		if got := tc.base.Matches(tc.d); got != tc.want {
			t.Errorf("%s: Matches = %v; want %v", tc.name, got, tc.want)
		}
		if got := tc.base.Matches(tc.d); got {
			hits++
		}
	}
	if hits != 3 {
		t.Fatalf("table is vacuous: %d matching rows, want 3", hits)
	}

	// The snapshot copies, so a later registry mutation cannot move the base.
	d := providers.Defaults{ExcludeModels: []string{"nw"}}
	base := ExcludeModelsBaseFromDefaults(d)
	d.ExcludeModels[0] = "mutated"
	if base.Entries[0] != "nw" {
		t.Fatal("the base aliased the loaded list")
	}
}

// TestSetExcludeModelsEmptyListPersistsAndK3DefaultDoesNotReturn is the
// nil-vs-empty round trip the D1=(a) contract depends on: saving an empty list
// persists an explicit [], a reload keeps the user-owned empty policy, and the
// built-in k3 default does NOT silently come back.
func TestSetExcludeModelsEmptyListPersistsAndK3DefaultDoesNotReturn(t *testing.T) {
	c, path := excludeCore(t, providers.Defaults{})

	// Premise: with the key absent the built-in default governs and a k3-token
	// model is excluded. Without this the assertions below could pass on a
	// build where nothing was ever excluded.
	seed := reloadFromDisk(t, path)
	seedPolicy := providers.ExclusionPolicyFromDefaults(seed.Defaults)
	if seedPolicy.UserOwned() {
		t.Fatal("premise broken: an absent excludeModels key must leave the policy built-in")
	}
	if !seedPolicy.Excludes("nw", "kimi-k3", "kimi") {
		t.Fatal("premise broken: the built-in default must exclude a k3-token model id")
	}

	if err := c.SetExcludeModels(nil, ExcludeModelsBase{}); err != nil {
		t.Fatalf("SetExcludeModels(nil): %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	if !strings.Contains(string(raw), `"excludeModels": []`) {
		t.Fatalf("providers.json does not carry an explicit empty list:\n%s", raw)
	}

	again := reloadFromDisk(t, path)
	if again.Defaults.ExcludeModels == nil {
		t.Fatal("the absent shape came back on reload: the built-in k3 default would govern again")
	}
	if len(again.Defaults.ExcludeModels) != 0 {
		t.Fatalf("reloaded list = %v; want empty", again.Defaults.ExcludeModels)
	}
	policy := providers.ExclusionPolicyFromDefaults(again.Defaults)
	if !policy.UserOwned() {
		t.Fatal("the reloaded policy is not user-owned")
	}
	if policy.Excludes("nw", "kimi-k3", "kimi") {
		t.Fatal("the built-in k3 default silently returned after the user saved an empty list")
	}
	// The rest of the registry is untouched.
	if len(again.Providers) != 1 || again.Providers[0].Name != "nw" || len(again.Providers[0].Models) != 2 {
		t.Fatalf("sibling registry content changed: %+v", again.Providers)
	}
}

// TestSetExcludeModelsWritesTrimmedEntriesAndIsIdempotent pins the populated
// write and the no-op rule: a repeated save of the same list performs no write
// at all.
func TestSetExcludeModelsWritesTrimmedEntriesAndIsIdempotent(t *testing.T) {
	c, path := excludeCore(t, providers.Defaults{})
	if err := c.SetExcludeModels([]string{" nw ", "glm-5"}, ExcludeModelsBase{}); err != nil {
		t.Fatalf("SetExcludeModels: %v", err)
	}
	reg := reloadFromDisk(t, path)
	if got := strings.Join(reg.Defaults.ExcludeModels, ","); got != "nw,glm-5" {
		t.Fatalf("stored entries = %q; want the trimmed pair", got)
	}

	saves := 0
	c2 := NewCore(Deps{
		LoadReg: providers.Load,
		SaveReg: func(r *providers.Registry) error { saves++; return saveRegistryFile(r) },
	})
	base := ExcludeModelsBaseFromDefaults(reg.Defaults)
	if err := c2.SetExcludeModels([]string{"nw", "glm-5"}, base); err != nil {
		t.Fatalf("idempotent SetExcludeModels: %v", err)
	}
	if saves != 0 {
		t.Fatalf("an idempotent save called SaveReg %d times; want 0 writes", saves)
	}
}

// TestSetExcludeModelsRefusalsWriteNothing pins the two refusal classes at the
// write door: an invalid entry and a stale base. Neither may touch the file.
func TestSetExcludeModelsRefusalsWriteNothing(t *testing.T) {
	c, path := excludeCore(t, providers.Defaults{ExcludeModels: []string{"nw"}})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.SetExcludeModels([]string{"nw glm-5"}, ExcludeModelsBaseFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})); !errors.Is(err, ErrExcludeModelsInvalid) {
		t.Fatalf("invalid entry error = %v; want ErrExcludeModelsInvalid", err)
	}
	// The base claims the key is absent while the file holds a list: stale.
	if err := c.SetExcludeModels([]string{"charm"}, ExcludeModelsBase{}); !errors.Is(err, ErrExcludeModelsStale) {
		t.Fatalf("stale base error = %v; want ErrExcludeModelsStale", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("a refused write changed providers.json:\nbefore %s\nafter  %s", before, after)
	}
}

// TestViewExcludeModelsRowShape is the row pin for the replaced read-only k3
// row: the ID, the registry source, the editable flag, and the ONE rule in the
// note, across the three stored shapes.
func TestViewExcludeModelsRowShape(t *testing.T) {
	cases := []struct {
		name     string
		defaults providers.Defaults
		want     string
	}{
		{"absent key shows the built-in entry and says so", providers.Defaults{}, "family:k3 (built-in default entry; the excludeModels key is absent or null)"},
		{"owned empty list says nothing is excluded", providers.Defaults{ExcludeModels: []string{}}, "(empty list: nothing is excluded)"},
		{"populated list renders its entries", providers.Defaults{ExcludeModels: []string{"nw", "glm-5"}}, "nw, glm-5"},
	}
	values := map[string]bool{}
	for _, tc := range cases {
		c, _ := excludeCore(t, tc.defaults)
		view, err := c.View()
		if err != nil {
			t.Fatalf("%s: View: %v", tc.name, err)
		}
		it, ok := view.FindItem("swarm.exclude_models")
		if !ok {
			t.Fatalf("%s: row swarm.exclude_models missing", tc.name)
		}
		if it.Value != tc.want {
			t.Errorf("%s: Value = %q; want %q", tc.name, it.Value, tc.want)
		}
		values[it.Value] = true
		if it.Source != "registry" {
			t.Errorf("%s: Source = %q; want registry", tc.name, it.Source)
		}
		if it.ReadOnly {
			t.Errorf("%s: the row must be EDITABLE in the hub (ReadOnly true)", tc.name)
		}
		for _, want := range []string{
			"auto-seating and failover skip an excluded entry",
			"nothing is ever rejected",
			"edit in the hub: agy pro setup",
		} {
			if !strings.Contains(strings.ToLower(it.Note), strings.ToLower(want)) {
				t.Errorf("%s: note does not name %q: %q", tc.name, want, it.Note)
			}
		}
		if _, ok := view.FindItem("swarm.k3_exclusion"); ok {
			t.Fatalf("%s: the retired swarm.k3_exclusion row is still in the view", tc.name)
		}
	}
	// Non-vacuity: the three shapes must produce three distinct renderings,
	// or the value composer is ignoring the stored shape.
	if len(values) != 3 {
		t.Fatalf("the three stored shapes rendered %d distinct values: %v", len(values), values)
	}
}

// TestExcludeModelsRowAndSeedTellOneStory pins the cross-surface agreement the
// unit exists for: the row's absent-key VALUE and the editor's seeded list name
// the SAME entry, in the family form, and the row's note says the rule survives
// saving the seeded list unchanged. A bare "k3" anywhere on either surface is a
// failure, because the list grammar does not reproduce the built-in predicate
// with it.
func TestExcludeModelsRowAndSeedTellOneStory(t *testing.T) {
	seed := ExcludeModelsSeed(providers.Defaults{})
	if len(seed) != 1 {
		t.Fatalf("absent-key seed = %v; want one entry", seed)
	}
	value := excludeModelsValue(providers.Defaults{})
	if !strings.HasPrefix(value, seed[0]+" ") {
		t.Fatalf("row value %q does not start with the seeded entry %q; the row and the editor disagree", value, seed[0])
	}
	if entry, _, _ := strings.Cut(value, " "); entry != "family:k3" {
		t.Fatalf("row value entry = %q; want family:k3 (the list spelling of the built-in family predicate)", entry)
	}
	// The shipped surface, not just the composer helper: the row a Core builds
	// on an absent key carries the same value and the same rule in its note.
	c, _ := excludeCore(t, providers.Defaults{})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("swarm.exclude_models")
	if !ok {
		t.Fatal("row swarm.exclude_models missing")
	}
	if it.Value != value {
		t.Fatalf("view row value = %q; want %q", it.Value, value)
	}
	for _, want := range []string{
		"family:k3",
		"auto-seating and failover skip an excluded entry",
		"an explicit --seats pick is honored with the warning that it is excluded",
		"nothing is ever rejected",
		"keeps the same rule",
	} {
		if !strings.Contains(strings.ToLower(it.Note), strings.ToLower(want)) {
			t.Errorf("row note does not name %q: %q", want, it.Note)
		}
	}
	if strings.Contains(it.Note, "built-in k3 default entry") {
		t.Errorf("row note still names the bare k3 default entry: %q", it.Note)
	}
}

// TestViewExcludeModelsEntryMasked pins that a credential-shaped entry (the
// list is registry-derived text) renders masked on the row, like every other
// registry-slot display.
func TestViewExcludeModelsEntryMasked(t *testing.T) {
	// S7-C11: built by concatenation, like every planted credential value in this
	// tree (internal/testfixtures): the contiguous hyphenated head is what secret
	// scanners and push protection match on, so it is never spelled in one piece.
	// No constant in internal/testfixtures covers this exact shape (the package
	// carries the underscore head and the "-proj-" variant, not "-live-"), so the
	// value is composed here instead of widening that read-only file.
	secret := "sk" + "-live-0123456789abcdefXYZ"
	c, _ := excludeCore(t, providers.Defaults{ExcludeModels: []string{secret}})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("swarm.exclude_models")
	if !ok {
		t.Fatal("row missing")
	}
	if strings.Contains(it.Value, secret) {
		t.Fatalf("the row renders the raw entry: %q", it.Value)
	}
	if !strings.Contains(it.Value, "****") {
		t.Fatalf("the row value carries no mask marker: %q", it.Value)
	}
}

// TestViewExcludeModelsNoteLocalizedInEveryLanguage closes the gap the en-only
// byte-identity test leaves: the row's note resolves from EVERY one of the 12
// catalogs, each rendering is a real translation (twelve distinct strings, no
// fallback to en), and the two facts a translation must never lose survive in
// all of them: the family:k3 spelling of the built-in default and the hub route
// command. The en rendering must equal the compiled literal byte-for-byte, the
// same contract internal/tui pins for the whole view.
func TestViewExcludeModelsNoteLocalizedInEveryLanguage(t *testing.T) {
	c, _ := excludeCore(t, providers.Defaults{})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("swarm.exclude_models")
	if !ok {
		t.Fatal("row missing")
	}
	key := "view." + it.MsgID + ".note"
	metas := i18n.Languages()
	if len(metas) != 12 {
		t.Fatalf("languages = %d; want the closed 12-locale set", len(metas))
	}
	rendered := map[string]string{}
	for _, m := range metas {
		got := i18n.Load(m.Code).T(key)
		if got == key || strings.TrimSpace(got) == "" {
			t.Errorf("%s: note did not resolve from the catalog (%q)", m.Code, got)
			continue
		}
		for _, want := range []string{"family:k3", "agy pro setup"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: note lost %q: %q", m.Code, want, got)
			}
		}
		if m.Code == "en" && got != it.Note {
			t.Errorf("en catalog note %q != compiled literal %q", got, it.Note)
		}
		rendered[m.Code] = got
	}
	if len(rendered) != 12 {
		t.Fatalf("resolved %d of 12 language notes; the catalog set drifted", len(rendered))
	}
	distinct := map[string]bool{}
	for _, v := range rendered {
		distinct[v] = true
	}
	if len(distinct) != 12 {
		t.Fatalf("the 12 language notes produced %d distinct renderings; at least one locale was left in English", len(distinct))
	}
}

// TestLegacyPanelExclusionRowIsReadOnlyWithHubRoute is P1-7=(a) on the legacy
// line-mode panel: the panel renders view.go rows generically, and the exclusion
// row must read honestly there as a display-only row that routes to the hub,
// with no activation arm and no write.
func TestLegacyPanelExclusionRowIsReadOnlyWithHubRoute(t *testing.T) {
	tui, f, _ := newTestTUI(t)
	selectItemRow(t, tui, "swarm.exclude_models")

	before := f.saved
	tui.Step(KeyEnter)
	if f.saved != before {
		t.Fatal("the legacy panel activation wrote to a Core seam")
	}
	if tui.pending != nil || tui.choices != nil {
		t.Fatalf("the row armed a write surface: pending %v, choices %v", tui.pending, tui.choices)
	}
	if !strings.Contains(tui.message, "edit in the hub: agy pro setup") {
		t.Fatalf("panel message = %q; want the hub route", tui.message)
	}
	if strings.Contains(tui.message, "[SUCCESS]") {
		t.Fatalf("panel message claims a write: %q", tui.message)
	}
	// The same sentence rides the detail pane, so the row is honest before the
	// press as well.
	s := tui.Frame()
	if !strings.Contains(s.Right, "edit in the hub: agy pro setup") {
		t.Fatalf("panel detail pane does not carry the hub route:\n%s", s.Right)
	}
	if !strings.Contains(s.Right, "auto-seating and failover skip an excluded entry") {
		t.Fatalf("panel detail pane does not carry the one rule:\n%s", s.Right)
	}

	// Non-vacuity: another row's activation produces a DIFFERENT message, so the
	// assertions above are not satisfied by every row.
	selectItemRow(t, tui, "providers.add_interactive")
	tui.Step(KeyEnter)
	if strings.Contains(tui.message, "edit in the hub: agy pro setup") {
		t.Fatalf("control row carries the exclusion route: %q", tui.message)
	}
}

// --- P5-3 T6: integrations intent keys (state.json additive rows) ----------
//
// Six additive keys carry the integrations INTENT half through the existing
// settings door: integration-preset plus the five per-feature toggles. These
// tests pin the accept/reject matrix, the exact state.json row each key writes
// (and that no sibling row moves), the hand-enumerated refusal sentence, and
// the agreement between the state-layer spellings (internal/mode) and the
// engine vocabulary (internal/integrations).

// integrationIntentNames labels the six persisted integration rows in the order
// integrationIntentRows returns them.
var integrationIntentNames = [6]string{
	"integration_preset",
	"integration_statusline",
	"integration_hook",
	"integration_rules",
	"integration_plugins",
	"integration_skills",
}

// integrationIntentRows returns the six persisted integration rows in a fixed
// order, so a write can be proven to touch exactly one of them.
func integrationIntentRows(st *mode.State) [6]string {
	return [6]string{
		st.IntegrationPreset,
		st.IntegrationStatusline,
		st.IntegrationHook,
		st.IntegrationRules,
		st.IntegrationPlugins,
		st.IntegrationSkills,
	}
}

// TestCoreSetIntegrationPreset pins the preset door: both key spellings accept
// the three engine preset words (case-folded, padded), store the canonical
// word, and refuse everything else without writing.
func TestCoreSetIntegrationPreset(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())

	for _, key := range []string{"integration-preset", "integration_preset", "  Integration-Preset  "} {
		for _, tc := range []struct{ operand, want string }{
			{"native", string(integrations.PresetNative)},
			{"TOOLS", string(integrations.PresetTools)},
			{" Custom ", string(integrations.PresetCustom)},
		} {
			before := integrationIntentRows(f.state)
			if err := c.Set(key, tc.operand); err != nil {
				t.Fatalf("Set(%q, %q) = %v", key, tc.operand, err)
			}
			after := integrationIntentRows(f.state)
			if after[0] != tc.want {
				t.Fatalf("Set(%q, %q) stored %q; want %q", key, tc.operand, after[0], tc.want)
			}
			for i := 1; i < len(after); i++ {
				if after[i] != before[i] {
					t.Fatalf("Set(%q, %q) moved %s as well", key, tc.operand, integrationIntentNames[i])
				}
			}
		}

		for _, bad := range []string{"", "bogus", "on", "all"} {
			saved := f.saved
			before := integrationIntentRows(f.state)
			err := c.Set(key, bad)
			if err == nil {
				t.Fatalf("Set(%q, %q) accepted", key, bad)
			}
			if !strings.Contains(err.Error(), "use native, tools, or custom") {
				t.Errorf("the refusal must name the valid presets: %v", err)
			}
			if f.saved != saved || integrationIntentRows(f.state) != before {
				t.Errorf("a refused preset write reached the state: %v", integrationIntentRows(f.state))
			}
		}
	}
}

// TestCoreSetIntegrationFeatureToggles pins the five per-feature doors: each
// closed key spelling writes exactly its own row, the on/off alias set is the
// launcher menu's, and an out-of-set operand (including a preset word, which is
// the easy typo) is refused without writing.
func TestCoreSetIntegrationFeatureToggles(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())

	rows := []struct {
		keys    []string
		index   int
		feature integrations.Feature
	}{
		{[]string{"integration-statusline", "integration_statusline"}, 1, integrations.FeatureStatusline},
		{[]string{"integration-hook", "integration_hook"}, 2, integrations.FeatureHook},
		{[]string{"integration-rules", "integration_rules"}, 3, integrations.FeatureRules},
		{[]string{"integration-plugins", "integration_plugins"}, 4, integrations.FeaturePlugins},
		{[]string{"integration-skills", "integration_skills"}, 5, integrations.FeatureSkills},
	}

	for _, row := range rows {
		// The key suffix IS the feature word the engine vocabulary declares, so
		// the store's closed table cannot drift away from internal/integrations.
		if got, err := integrations.ParseFeature(strings.TrimPrefix(row.keys[0], "integration-")); err != nil || got != row.feature {
			t.Fatalf("ParseFeature(%q) = %q, %v; want %q", row.keys[0], got, err, row.feature)
		}

		for _, key := range row.keys {
			for _, tc := range []struct{ operand, want string }{
				{"on", mode.IntegrationToggleOn},
				{"OFF", mode.IntegrationToggleOff},
				{"1", mode.IntegrationToggleOn},
				{"0", mode.IntegrationToggleOff},
				{"true", mode.IntegrationToggleOn},
				{"false", mode.IntegrationToggleOff},
			} {
				before := integrationIntentRows(f.state)
				if err := c.Set(key, tc.operand); err != nil {
					t.Fatalf("Set(%q, %q) = %v", key, tc.operand, err)
				}
				after := integrationIntentRows(f.state)
				if after[row.index] != tc.want {
					t.Fatalf("Set(%q, %q) stored %q; want %q", key, tc.operand, after[row.index], tc.want)
				}
				for i := range after {
					if i != row.index && after[i] != before[i] {
						t.Fatalf("Set(%q, %q) moved %s as well", key, tc.operand, integrationIntentNames[i])
					}
				}
			}

			for _, bad := range []string{"enabled", "2", "native", ""} {
				saved := f.saved
				before := integrationIntentRows(f.state)
				err := c.Set(key, bad)
				if err == nil {
					t.Fatalf("Set(%q, %q) accepted", key, bad)
				}
				if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "use on, off, 1, or 0") {
					t.Errorf("the refusal must name the key and the valid values: %v", err)
				}
				if f.saved != saved || integrationIntentRows(f.state) != before {
					t.Errorf("a refused toggle write reached the state: %v", integrationIntentRows(f.state))
				}
			}
		}
	}
}

// TestCoreSetRefusalNamesEveryIntegrationKey pins the hand-enumerated refusal
// sentence (constraint 9: one closed key table, one sentence) so adding a key
// without naming it here fails a test.
func TestCoreSetRefusalNamesEveryIntegrationKey(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	err := c.Set("integration-nope", "on")
	if err == nil {
		t.Fatal("an unknown integration key was accepted")
	}
	for _, key := range []string{
		"default-profile", "bridge-tools", "native-seat-concurrency", "language", "launcher_menu",
		"integration-preset", "integration-statusline", "integration-hook",
		"integration-rules", "integration-plugins", "integration-skills",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal must name %q: %v", key, err)
		}
	}
	if f.saved != 0 {
		t.Errorf("a refused key wrote the state %d times", f.saved)
	}
}

// TestIntegrationVocabularyAgreesAcrossPackages pins the one place where the
// state layer repeats the engine's words (internal/mode declares them so it
// need not import internal/integrations): the preset words agree in both
// directions, and the preset and toggle value domains stay disjoint.
func TestIntegrationVocabularyAgreesAcrossPackages(t *testing.T) {
	for _, preset := range []integrations.Preset{integrations.PresetNative, integrations.PresetTools, integrations.PresetCustom} {
		if got := mode.NormalizeIntegrationPreset(string(preset)); got != string(preset) {
			t.Errorf("mode.NormalizeIntegrationPreset(%q) = %q; the state and engine preset words drifted", preset, got)
		}
	}
	for _, word := range []string{mode.IntegrationPresetNative, mode.IntegrationPresetTools, mode.IntegrationPresetCustom} {
		if _, err := integrations.ParsePreset(word); err != nil {
			t.Errorf("the engine refuses the state-layer preset word %q: %v", word, err)
		}
	}
	for _, word := range []string{mode.IntegrationPresetNative, mode.IntegrationPresetTools, mode.IntegrationPresetCustom} {
		if got := mode.NormalizeIntegrationToggle(word); got != "" {
			t.Errorf("NormalizeIntegrationToggle(%q) = %q; a preset word must not read as a toggle", word, got)
		}
	}
	for _, word := range []string{mode.IntegrationToggleOn, mode.IntegrationToggleOff} {
		if got := mode.NormalizeIntegrationPreset(word); got != "" {
			t.Errorf("NormalizeIntegrationPreset(%q) = %q; a toggle word must not read as a preset", word, got)
		}
		if got := mode.NormalizeIntegrationToggle(word); got != word {
			t.Errorf("NormalizeIntegrationToggle(%q) = %q; want itself", word, got)
		}
	}
	// The closed Core.Set table carries one toggle per feature the engine
	// declares, so a sixth feature cannot appear without this test failing.
	if got := len(integrations.AllFeatures()); got != 5 {
		t.Fatalf("the engine declares %d features; the closed Core.Set table carries five toggles", got)
	}
}
