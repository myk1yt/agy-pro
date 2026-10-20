package settings

// Blind-qa gate D regression pins (fix-panic-and-fixture): a registry that
// FAILS to load - the round-6 control-rune guard rejects a disk-borne ESC byte
// in a provider name at the registry boundary - must surface from Core.View as
// the same clean, masked error the model verbs emit, never as a nil-pointer
// panic in sectionProviders. A clean registry (CJK provider name included)
// still renders every section. All fixtures live under t.TempDir() with
// AGY_PRO_HOME / EnvProviders pointed at the temp dir; the live registry is
// never touched.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"agypro/internal/providers"
)

// viewRegistryHome isolates the Core's default seams (state load, swarm store)
// under a temp AGY_PRO_HOME and returns it.
func viewRegistryHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("AGY_PRO_HOME", tmp)
	return tmp
}

// poisonedRegistry builds a hand-poisoned providers.json: the provider name
// carries a C0 ESC byte (json.Marshal renders it as the JSON unicode escape,
// which decodes back to the ESC rune at load), which Registry.Validate refuses
// at load time with a field-class-only error ("providers[0]: name contains
// control characters").
func poisonedRegistry(t *testing.T) []byte {
	t.Helper()
	name, err := json.Marshal("bad\x1bname")
	if err != nil {
		t.Fatalf("marshal poisoned name: %v", err)
	}
	return []byte(`{"providers":[{"name":` + string(name) + `,"baseURL":"https://poison.example/v1","apiKeyEnv":"POISON_KEY"}]}`)
}

func TestView_RegistryLoadFailure_CleanErrorNoPanic(t *testing.T) {
	tmp := viewRegistryHome(t)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	if err := os.WriteFile(path, poisonedRegistry(t), 0o600); err != nil {
		t.Fatalf("seed poisoned registry: %v", err)
	}
	c := NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})

	view, err := c.View()
	if err == nil {
		t.Fatalf("View on a registry that fails to load returned nil error (sections=%d)", len(view.Sections))
	}
	if !strings.Contains(err.Error(), "control characters") {
		t.Errorf("View error = %q; want the loader field-class error", err.Error())
	}
	if strings.IndexFunc(err.Error(), unicode.IsControl) >= 0 {
		t.Error("View error echoes a control rune; the loader error must name the field class only")
	}
}

func TestView_RegistrySeamError_Propagates(t *testing.T) {
	viewRegistryHome(t)
	loadErr := errors.New("providers file /tmp/x/providers.json: providers[0]: name contains control characters")
	c := NewCore(Deps{LoadReg: func() (*providers.Registry, error) { return nil, loadErr }})
	if _, err := c.View(); !errors.Is(err, loadErr) {
		t.Fatalf("View error = %v; want the load error wrapped", err)
	}
}

func TestView_NilRegistryWithoutError_RendersEmpty(t *testing.T) {
	viewRegistryHome(t)
	c := NewCore(Deps{LoadReg: func() (*providers.Registry, error) { return nil, nil }})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View with a nil registry and nil error: %v", err)
	}
	sec, ok := view.SectionByID("p2")
	if !ok {
		t.Fatal("p2 section missing")
	}
	for _, it := range sec.Items {
		if it.ID == "providers.list" && it.Value != "0 registered" {
			t.Errorf("providers.list = %q; want %q", it.Value, "0 registered")
		}
	}
}

func TestView_CJKProviderName_RendersNormally(t *testing.T) {
	tmp := viewRegistryHome(t)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	clean := `{"providers":[{"name":"모델공급자","baseURL":"https://cjk.example/v1","apiKeyEnv":"CJK_KEY"}]}`
	if err := os.WriteFile(path, []byte(clean), 0o600); err != nil {
		t.Fatalf("seed clean CJK registry: %v", err)
	}
	c := NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View on a clean CJK registry: %v", err)
	}
	it, ok := view.FindItem("providers.list")
	if !ok || it.Value != "1 registered" {
		t.Errorf("providers.list = %+v (found %v); want 1 registered", it, ok)
	}
	sec, _ := view.SectionByID("p2")
	found := false
	for _, it := range sec.Items {
		if strings.HasPrefix(it.Label, "Key status - ") && strings.Contains(it.Label, "모델공급자") {
			found = true
		}
	}
	if !found {
		t.Error("p2 missing the CJK provider key-status row")
	}
}

// TestViewProviderNameSinkUsesSlotAuthority (fix7-D) pins the settings view's
// provider-NAME sink against the registry-slot display authority: the
// key-status row's ID and Label both carry the name. Identity half: the benign
// corpus (openai, gemini, deepseek, nw, charm) renders byte-identically, so
// the ID and the Label keep their exact pre-change text. Credential half: the
// underscore-headed plant, which providers.MaskOperand leaves raw (its "_"
// breaks the contiguous-run test and its head is not in the shared prefix
// list), renders masked first-5 + **** in both fields and nowhere raw.
func TestViewProviderNameSinkUsesSlotAuthority(t *testing.T) {
	const (
		plant = "sk_live_0123456789abcdef"
		mask  = "sk_li****"
	)
	// The required names plus the names the pre-existing key-status pins use
	// (acme, acme-corp, beta, dup) and the separator-bearing forms the view's
	// CJK/alignment pins exercise.
	corpus := []string{
		"openai", "gemini", "deepseek", "nw", "charm",
		"anthropic", "acme", "acme-corp", "beta", "dup",
		"my_provider", "my-provider", "cedar.2024", "x",
	}

	tmp := viewRegistryHome(t)
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg := &providers.Registry{}
	for _, n := range append(corpus, plant) {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name: n, BaseURL: "https://gw.example/v1", APIKeyEnv: "NAME_SINK_KEY",
		})
	}
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	c := NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	sec, ok := view.SectionByID("p2")
	if !ok {
		t.Fatal("p2 section missing")
	}
	byID := make(map[string]Item, len(sec.Items))
	var all strings.Builder
	for _, it := range sec.Items {
		byID[it.ID] = it
		all.WriteString(it.ID + it.Label + it.Value + it.Note)
	}

	for _, n := range corpus {
		it, ok := byID["provider.key."+n]
		if !ok {
			t.Fatalf("key-status row for %q missing", n)
		}
		if it.Label != "Key status - "+n {
			t.Errorf("key-status label for %q = %q; want the name unchanged", n, it.Label)
		}
	}
	it, ok := byID["provider.key."+mask]
	if !ok {
		t.Fatalf("key-status row for the plant missing (the ID must carry the masked name)")
	}
	if it.Label != "Key status - "+mask {
		t.Errorf("plant key-status label = %q; want %q", it.Label, "Key status - "+mask)
	}
	if strings.Contains(all.String(), plant) {
		t.Error("the settings view renders the credential-shaped provider name raw")
	}
}
