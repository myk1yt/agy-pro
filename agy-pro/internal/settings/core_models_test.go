package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// Core model-verb tests (plan-v2 phase P6). Two harnesses, both never
// touching the live registry:
//   - the in-memory fakeDeps seam fake (from core_test.go) for rejection and
//     masking pins, asserting SaveReg is never called on failure;
//   - a file-backed Core whose LoadReg/SaveReg are the REAL seams
//     (providers.Load / saveRegistryFile) pointed at a t.TempDir()
//     providers.json via EnvProviders, for happy-path persistence, on-disk
//     immutability on failure, and sibling-survival pins.

// modelFixture returns the standard two-model provider used across the
// file-backed tests.
func modelFixture() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{ID: "acme-fast", ContextLength: 128000, Capabilities: providers.Capabilities{AllowedTools: []string{"read"}}},
				{ID: "acme-slow"},
			},
		},
	}}
}

// fileBackedCore writes initial (with Path pinned) into a temp providers.json
// and returns a Core whose registry seams are the real load/save pair plus the
// on-disk path for byte-level assertions.
func fileBackedCore(t *testing.T, initial *providers.Registry) (*Core, string) {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	initial.Path = path
	if err := saveRegistryFile(initial); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	c := NewCore(Deps{
		LoadReg: providers.Load,
		SaveReg: saveRegistryFile,
	})
	return c, path
}

func reloadFromDisk(t *testing.T, path string) *providers.Registry {
	t.Helper()
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	return reg
}

func acmeModels(t *testing.T, reg *providers.Registry) []providers.Model {
	t.Helper()
	p, ok := reg.Provider("acme")
	if !ok {
		t.Fatal("provider acme missing after reload")
	}
	return p.Models
}

func diskBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	return b
}

func TestModelAdd_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	// Provider name resolves case-insensitively through the Core verb.
	if err := c.ModelAdd("ACME", providers.Model{ID: "acme-new", ContextLength: 64000}); err != nil {
		t.Fatalf("ModelAdd: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if len(models) != 3 {
		t.Fatalf("models on disk = %d, want 3: %+v", len(models), models)
	}
	if models[2].ID != "acme-new" || models[2].ContextLength != 64000 {
		t.Errorf("appended model = %+v", models[2])
	}
	if !strings.Contains(string(diskBytes(t, path)), "acme-new") {
		t.Error("on-disk JSON lacks the new model ID")
	}
}

func TestModelDelete_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	if err := c.ModelDelete("acme", "acme-slow"); err != nil {
		t.Fatalf("ModelDelete: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if len(models) != 1 || models[0].ID != "acme-fast" {
		t.Errorf("models on disk = %+v", models)
	}
	if strings.Contains(string(diskBytes(t, path)), "acme-slow") {
		t.Error("on-disk JSON still contains the deleted model ID")
	}
}

func TestModelUpdate_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	repl := providers.Model{ID: "acme-fast", ContextLength: 256000, MaxOutputTokens: 8192}
	if err := c.ModelUpdate("acme", "acme-fast", repl); err != nil {
		t.Fatalf("ModelUpdate: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if len(models) != 2 {
		t.Fatalf("model count changed: %+v", models)
	}
	if models[0].ContextLength != 256000 || models[0].MaxOutputTokens != 8192 {
		t.Errorf("updated model = %+v", models[0])
	}
}

func TestModelSetEnabled_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	if err := c.ModelSetEnabled("acme", "acme-fast", false); err != nil {
		t.Fatalf("ModelSetEnabled: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if models[0].Enabled == nil || *models[0].Enabled {
		t.Errorf("enabled flag not persisted as explicit false: %+v", models[0])
	}
	if !strings.Contains(string(diskBytes(t, path)), `"enabled": false`) {
		t.Error("on-disk JSON lacks the explicit enabled:false")
	}
	if err := c.ModelSetEnabled("acme", "acme-fast", true); err != nil {
		t.Fatalf("ModelSetEnabled re-enable: %v", err)
	}
	models = acmeModels(t, reloadFromDisk(t, path))
	if models[0].Enabled == nil || !*models[0].Enabled {
		t.Errorf("enabled flag not persisted as explicit true: %+v", models[0])
	}
}

func TestModelSetTools_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	if err := c.ModelSetTools("acme", "acme-fast", []string{"read", "search"}); err != nil {
		t.Fatalf("ModelSetTools: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if len(models[0].Capabilities.AllowedTools) != 2 ||
		models[0].Capabilities.AllowedTools[0] != "read" ||
		models[0].Capabilities.AllowedTools[1] != "search" {
		t.Errorf("AllowedTools on disk = %+v", models[0].Capabilities.AllowedTools)
	}
	// P5 contract: SetModelTools never flips SupportsTools.
	if models[0].Capabilities.SupportsTools {
		t.Error("SetModelTools must not flip SupportsTools")
	}
}

func TestModelRename_HappyPath(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	if err := c.ModelRename("acme", "acme-slow", "acme-legacy"); err != nil {
		t.Fatalf("ModelRename: %v", err)
	}
	models := acmeModels(t, reloadFromDisk(t, path))
	if len(models) != 2 || models[1].ID != "acme-legacy" {
		t.Errorf("models on disk = %+v", models)
	}
	if strings.Contains(string(diskBytes(t, path)), "acme-slow") {
		t.Error("on-disk JSON still contains the old model ID")
	}
}

// TestModelVerbs_MissingProvider pins that every verb fails closed on an
// unknown provider and never reaches SaveReg.
func TestModelVerbs_MissingProvider(t *testing.T) {
	verbs := map[string]func(*Core) error{
		"add":        func(c *Core) error { return c.ModelAdd("ghost", providers.Model{ID: "m"}) },
		"delete":     func(c *Core) error { return c.ModelDelete("ghost", "m") },
		"update":     func(c *Core) error { return c.ModelUpdate("ghost", "m", providers.Model{ID: "m2"}) },
		"setEnabled": func(c *Core) error { return c.ModelSetEnabled("ghost", "m", false) },
		"setTools":   func(c *Core) error { return c.ModelSetTools("ghost", "m", []string{"read"}) },
		"rename":     func(c *Core) error { return c.ModelRename("ghost", "m", "m2") },
	}
	for name, verb := range verbs {
		f := newFakeDeps()
		c := NewCore(f.deps())
		if err := verb(c); err == nil {
			t.Errorf("%s: missing provider accepted", name)
		}
		if f.saved != 0 {
			t.Errorf("%s: SaveReg called on failure (%d saves)", name, f.saved)
		}
	}
}

// TestModelVerbs_MissingModel pins the same fail-closed discipline for an
// unknown model ID inside an existing provider.
func TestModelVerbs_MissingModel(t *testing.T) {
	f := newFakeDeps()
	f.reg = modelFixture()
	c := NewCore(f.deps())
	if err := c.ModelDelete("acme", "ghost"); err == nil {
		t.Error("delete: missing model accepted")
	}
	if err := c.ModelUpdate("acme", "ghost", providers.Model{ID: "g2"}); err == nil {
		t.Error("update: missing model accepted")
	}
	if err := c.ModelSetEnabled("acme", "ghost", false); err == nil {
		t.Error("setEnabled: missing model accepted")
	}
	if err := c.ModelSetTools("acme", "ghost", []string{"read"}); err == nil {
		t.Error("setTools: missing model accepted")
	}
	if err := c.ModelRename("acme", "ghost", "g2"); err == nil {
		t.Error("rename: missing model accepted")
	}
	if f.saved != 0 {
		t.Errorf("SaveReg called on failures (%d saves)", f.saved)
	}
}

func TestModelAdd_DuplicateIDRejected(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	before := diskBytes(t, path)
	if err := c.ModelAdd("acme", providers.Model{ID: "acme-fast"}); err == nil {
		t.Fatal("duplicate model ID accepted on add")
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk JSON changed after duplicate-ID rejection")
	}
}

func TestModelRename_DuplicateIDRejected(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	before := diskBytes(t, path)
	if err := c.ModelRename("acme", "acme-slow", "acme-fast"); err == nil {
		t.Fatal("duplicate model ID accepted on rename")
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk JSON changed after duplicate-ID rejection")
	}
}

// TestModelVerbs_ValidateFailureLeavesDiskUnchanged seeds a registry whose
// duplicate provider name makes Registry.Validate fail regardless of the op;
// every validating verb must roll back in memory AND leave the on-disk file
// byte-identical (SaveReg is only reached on success).
func TestModelVerbs_ValidateFailureLeavesDiskUnchanged(t *testing.T) {
	invalid := &providers.Registry{Providers: []providers.Provider{
		{Name: "dup", BaseURL: "https://a.example/v1", Models: []providers.Model{{ID: "m1"}}},
		{Name: "dup", BaseURL: "https://b.example/v1", Models: []providers.Model{{ID: "m2"}}},
	}}
	c, path := fileBackedCore(t, invalid)
	before := diskBytes(t, path)

	if err := c.ModelAdd("dup", providers.Model{ID: "m3"}); err == nil {
		t.Error("add should fail when the registry cannot validate")
	}
	if err := c.ModelRename("dup", "m1", "m1b"); err == nil {
		t.Error("rename should fail when the registry cannot validate")
	}
	if err := c.ModelUpdate("dup", "m1", providers.Model{ID: "m1c"}); err == nil {
		t.Error("update should fail when the registry cannot validate")
	}
	if got := diskBytes(t, path); string(got) != string(before) {
		t.Error("on-disk JSON changed after validate-failure rollback")
	}
	// In-memory state must be rolled back too: the first provider keeps m1.
	// LoadFrom validates and would reject the seeded duplicate-name file, so
	// inspect the raw on-disk JSON instead.
	var reg providers.Registry
	if err := json.Unmarshal(diskBytes(t, path), &reg); err != nil {
		t.Fatalf("unmarshal on-disk registry: %v", err)
	}
	if reg.Providers[0].Models[0].ID != "m1" || len(reg.Providers[0].Models) != 1 {
		t.Errorf("rolled-back state = %+v", reg.Providers[0].Models)
	}
}

// TestModelAdd_SiblingSurvival pins the P5 in-place contract at the Core
// layer: adding a third model to a two-model provider leaves the first two
// byte-identical (per-element JSON) after a full save/reload cycle.
func TestModelAdd_SiblingSurvival(t *testing.T) {
	c, path := fileBackedCore(t, modelFixture())
	before := acmeModels(t, reloadFromDisk(t, path))
	if len(before) != 2 {
		t.Fatalf("fixture models = %d", len(before))
	}
	b0, _ := json.Marshal(before[0])
	b1, _ := json.Marshal(before[1])

	if err := c.ModelAdd("acme", providers.Model{ID: "acme-third"}); err != nil {
		t.Fatalf("ModelAdd: %v", err)
	}
	after := acmeModels(t, reloadFromDisk(t, path))
	if len(after) != 3 {
		t.Fatalf("models after add = %d", len(after))
	}
	a0, _ := json.Marshal(after[0])
	a1, _ := json.Marshal(after[1])
	if string(a0) != string(b0) {
		t.Errorf("sibling 0 changed:\nbefore: %s\nafter:  %s", b0, a0)
	}
	if string(a1) != string(b1) {
		t.Errorf("sibling 1 changed:\nbefore: %s\nafter:  %s", b1, a1)
	}
}

// TestModelVerbs_MaskKeyMaterial pins the B2/U8 discipline at the Core layer:
// a credential-shaped operand echoed in a missing-model error renders as the
// canonical first-5 + **** mask, never raw.
func TestModelVerbs_MaskKeyMaterial(t *testing.T) {
	f := newFakeDeps()
	f.reg = modelFixture()
	c := NewCore(f.deps())
	secret := "sk-PLANTEDKEY-8f3a2b1c9d0e7766"
	err := c.ModelSetEnabled("acme", secret, false)
	if err == nil {
		t.Fatal("key-shaped model ID accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error echoes raw key material: %v", err)
	}
	if !strings.Contains(err.Error(), providers.MaskSecret(secret)) {
		t.Errorf("error lacks the canonical mask %q: %v", providers.MaskSecret(secret), err)
	}
	// A key-shaped provider operand masks the same way.
	err = c.ModelDelete(secret, "acme-fast")
	if err == nil {
		t.Fatal("key-shaped provider name accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("provider error echoes raw key material: %v", err)
	}
}
