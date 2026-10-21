package settings

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"agypro/internal/fanout"
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
	state   *mode.State
	reg     *providers.Registry
	saved   int
	store   *fakeStore
	audits  []providers.KeyAudit
	fixed   []string
	rolled  []string
	doctorN int
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
		RunWizard:    func(in io.Reader, out io.Writer) error { return nil },
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
		"swarm.k3_exclusion", "swarm.templates", "swarm.presets", "swarm.save_preset",
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
