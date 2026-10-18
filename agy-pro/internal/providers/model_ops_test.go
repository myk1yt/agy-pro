package providers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

// modelOpsFixture builds a two-provider registry used across the model-op
// pins. acme carries two feature-rich siblings so byte-identity of untouched
// elements is observable; globex carries one model.
func modelOpsFixture() *Registry {
	return &Registry{Providers: []Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_API_KEY",
			Models: []Model{
				{
					ID:              "acme-fast",
					Family:          "acme",
					Tags:            []string{"fast", "cheap"},
					ContextLength:   8192,
					MaxOutputTokens: 2048,
					Capabilities:    Capabilities{SupportsTools: true, AllowedTools: []string{"read", "search"}},
				},
				{
					ID:            "acme-pro",
					ContextLength: 32768,
					Capabilities:  Capabilities{SupportsVision: true},
				},
			},
		},
		{
			Name:    "globex",
			BaseURL: "https://globex.example/v1",
			Models:  []Model{{ID: "globex-1"}},
		},
	}}
}

// modelJSONs re-marshals each element so sibling byte-identity is compared
// per element, independent of slice shape.
func modelJSONs(t *testing.T, models []Model) []string {
	t.Helper()
	out := make([]string, len(models))
	for i, m := range models {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal model %d: %v", i, err)
		}
		out[i] = string(b)
	}
	return out
}

func providerJSONs(t *testing.T, reg *Registry) []string {
	t.Helper()
	out := make([]string, len(reg.Providers))
	for i, p := range reg.Providers {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal provider %d: %v", i, err)
		}
		out[i] = string(b)
	}
	return out
}

func registryJSON(t *testing.T, reg *Registry) string {
	t.Helper()
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	return string(b)
}

func TestAddModel_SiblingsByteIdentical(t *testing.T) {
	reg := modelOpsFixture()
	beforeSiblings := modelJSONs(t, reg.Providers[0].Models)
	beforeOther := providerJSONs(t, &Registry{Providers: reg.Providers[1:]})

	// Case-insensitive provider name (wizard upsert semantics).
	m := Model{ID: "acme-max", ContextLength: 1 << 20, Capabilities: Capabilities{SupportsThinking: true, ReasoningEffort: "high"}}
	if err := AddModel(reg, "ACME", m); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if len(reg.Providers[0].Models) != 3 {
		t.Fatalf("after add: %+v", reg.Providers[0].Models)
	}
	gotSiblings := modelJSONs(t, reg.Providers[0].Models[:2])
	if !reflect.DeepEqual(gotSiblings, beforeSiblings) {
		t.Errorf("sibling models changed by add:\nbefore: %v\nafter:  %v", beforeSiblings, gotSiblings)
	}
	if !reflect.DeepEqual(providerJSONs(t, &Registry{Providers: reg.Providers[1:]}), beforeOther) {
		t.Errorf("other providers changed by add")
	}
	// The provider's own scalar fields must be untouched (no wholesale rebuild).
	if reg.Providers[0].APIKeyEnv != "ACME_API_KEY" || reg.Providers[0].BaseURL != "https://acme.example/v1" {
		t.Errorf("provider scalars changed: %+v", reg.Providers[0])
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("post-add registry does not validate: %v", err)
	}
}

func TestAddModel_DuplicateIDRejected(t *testing.T) {
	reg := modelOpsFixture()
	before := registryJSON(t, reg)
	err := AddModel(reg, "acme", Model{ID: "acme-fast"})
	if err == nil {
		t.Fatalf("AddModel accepted a duplicate model ID")
	}
	if got := registryJSON(t, reg); got != before {
		t.Errorf("rejected add mutated the registry")
	}
	if len(reg.Providers[0].Models) != 2 {
		t.Errorf("rejected add changed the models slice: %+v", reg.Providers[0].Models)
	}
}

func TestDeleteModel_SiblingsByteIdentical(t *testing.T) {
	reg := modelOpsFixture()
	beforeSibling := modelJSONs(t, []Model{reg.Providers[0].Models[0]})
	beforeOther := providerJSONs(t, &Registry{Providers: reg.Providers[1:]})

	if err := DeleteModel(reg, "acme", "acme-pro"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if len(reg.Providers[0].Models) != 1 {
		t.Fatalf("after delete: %+v", reg.Providers[0].Models)
	}
	if got := modelJSONs(t, []Model{reg.Providers[0].Models[0]}); !reflect.DeepEqual(got, beforeSibling) {
		t.Errorf("surviving sibling changed by delete:\nbefore: %v\nafter:  %v", beforeSibling, got)
	}
	if !reflect.DeepEqual(providerJSONs(t, &Registry{Providers: reg.Providers[1:]}), beforeOther) {
		t.Errorf("other providers changed by delete")
	}

	if err := DeleteModel(reg, "acme", "acme-fast"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if err := DeleteModel(reg, "acme", "acme-fast"); err == nil {
		t.Errorf("deleting a missing model should fail")
	}
	if err := DeleteModel(reg, "missing", "x"); err == nil {
		t.Errorf("deleting from a missing provider should fail")
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("post-delete registry does not validate: %v", err)
	}
}

func TestDeleteModel_LastModelLeavesEmptyList(t *testing.T) {
	reg := modelOpsFixture()
	if err := DeleteModel(reg, "globex", "globex-1"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if len(reg.Providers) != 2 {
		t.Fatalf("provider must survive its last model's deletion: %+v", reg.Providers)
	}
	if reg.Providers[1].Name != "globex" || len(reg.Providers[1].Models) != 0 {
		t.Errorf("provider should keep an empty model list: %+v", reg.Providers[1])
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("empty model list must validate: %v", err)
	}
	// The empty list must round-trip through JSON as [] (matching the
	// DeleteProvider precedent), not null.
	b, err := json.Marshal(reg.Providers[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"models":[]`) {
		t.Errorf("empty models must marshal as [], got %s", b)
	}
}

func TestUpdateModel_SiblingsByteIdentical(t *testing.T) {
	reg := modelOpsFixture()
	beforeSibling := modelJSONs(t, []Model{reg.Providers[0].Models[0]})
	beforeOther := providerJSONs(t, &Registry{Providers: reg.Providers[1:]})

	repl := Model{ID: "acme-fast-v2", ContextLength: 16384, Tags: []string{"fast"}}
	if err := UpdateModel(reg, "acme", "acme-pro", repl); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	if reg.Providers[0].Models[1].ID != "acme-fast-v2" || reg.Providers[0].Models[1].ContextLength != 16384 {
		t.Errorf("update did not stick: %+v", reg.Providers[0].Models[1])
	}
	if got := modelJSONs(t, []Model{reg.Providers[0].Models[0]}); !reflect.DeepEqual(got, beforeSibling) {
		t.Errorf("sibling changed by update:\nbefore: %v\nafter:  %v", beforeSibling, got)
	}
	if !reflect.DeepEqual(providerJSONs(t, &Registry{Providers: reg.Providers[1:]}), beforeOther) {
		t.Errorf("other providers changed by update")
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("post-update registry does not validate: %v", err)
	}
}

func TestUpdateModel_IDRenameRules(t *testing.T) {
	reg := modelOpsFixture()
	// Rename onto an existing sibling ID is rejected and changes nothing.
	before := registryJSON(t, reg)
	err := UpdateModel(reg, "acme", "acme-fast", Model{ID: "acme-pro", ContextLength: 1})
	if err == nil {
		t.Fatalf("UpdateModel accepted a rename onto a duplicate ID")
	}
	if got := registryJSON(t, reg); got != before {
		t.Errorf("rejected rename mutated the registry")
	}
	// Rename to a unique ID is allowed.
	if err := UpdateModel(reg, "acme", "acme-fast", Model{ID: "acme-fast-2", ContextLength: 8192}); err != nil {
		t.Fatalf("UpdateModel rename: %v", err)
	}
	if reg.Providers[0].Models[0].ID != "acme-fast-2" {
		t.Errorf("rename did not stick: %+v", reg.Providers[0].Models[0])
	}
	// Renaming in place (same ID) must not trip the uniqueness check.
	if err := UpdateModel(reg, "acme", "acme-fast-2", Model{ID: "acme-fast-2", ContextLength: 4096}); err != nil {
		t.Fatalf("UpdateModel same-ID replace: %v", err)
	}
}

func TestModelOps_ValidateFailureRollsBack(t *testing.T) {
	// Pre-existing invalid state (duplicate provider name) makes
	// Registry.Validate fail; the op must restore the saved slice copy.
	reg := &Registry{Providers: []Provider{
		{Name: "dup", BaseURL: "https://a.example/v1", Models: []Model{{ID: "m1"}}},
		{Name: "dup", BaseURL: "https://b.example/v1", Models: []Model{{ID: "m2"}}},
	}}
	before := registryJSON(t, reg)
	err := AddModel(reg, "dup", Model{ID: "m3"})
	if err == nil {
		t.Fatalf("AddModel should fail when the registry cannot validate")
	}
	if got := registryJSON(t, reg); got != before {
		t.Errorf("failed add did not roll back the registry:\nbefore: %s\nafter:  %s", before, got)
	}

	// Same for update and rename against the invalid registry.
	err = UpdateModel(reg, "dup", "m1", Model{ID: "m1b"})
	if err == nil {
		t.Fatalf("UpdateModel should fail when the registry cannot validate")
	}
	if got := registryJSON(t, reg); got != before {
		t.Errorf("failed update did not roll back the registry")
	}
	err = RenameModel(reg, "dup", "m1", "m1c")
	if err == nil {
		t.Fatalf("RenameModel should fail when the registry cannot validate")
	}
	if got := registryJSON(t, reg); got != before {
		t.Errorf("failed rename did not roll back the registry")
	}
}

func TestSetModelEnabled(t *testing.T) {
	reg := modelOpsFixture()
	if err := SetModelEnabled(reg, "ACME", "acme-fast", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	m := reg.Providers[0].Models[0]
	if m.Enabled == nil || *m.Enabled {
		t.Errorf("enabled flag not set to false: %+v", m)
	}
	if err := SetModelEnabled(reg, "acme", "acme-fast", true); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	if m := reg.Providers[0].Models[0]; m.Enabled == nil || !*m.Enabled {
		t.Errorf("enabled flag not set to true: %+v", m)
	}
	if err := SetModelEnabled(reg, "acme", "missing", true); err == nil {
		t.Errorf("SetModelEnabled on a missing model should fail")
	}
	if err := SetModelEnabled(reg, "missing", "m", true); err == nil {
		t.Errorf("SetModelEnabled on a missing provider should fail")
	}
}

func TestSetModelTools(t *testing.T) {
	reg := modelOpsFixture()
	if err := SetModelTools(reg, "acme", "acme-fast", []string{"exec"}); err != nil {
		t.Fatalf("SetModelTools: %v", err)
	}
	m := reg.Providers[0].Models[0]
	if !reflect.DeepEqual(m.Capabilities.AllowedTools, []string{"exec"}) {
		t.Errorf("allowed tools not replaced: %+v", m.Capabilities)
	}
	// SupportsTools is the caller's choice via UpdateModel; this op must not
	// flip it.
	if !m.Capabilities.SupportsTools {
		t.Errorf("SetModelTools must not change SupportsTools: %+v", m.Capabilities)
	}
	if err := SetModelTools(reg, "acme", "missing", nil); err == nil {
		t.Errorf("SetModelTools on a missing model should fail")
	}
	if err := reg.Validate(); err != nil {
		t.Errorf("post-tools registry does not validate: %v", err)
	}
}

func TestRenameModel(t *testing.T) {
	reg := modelOpsFixture()
	if err := RenameModel(reg, "acme", "acme-fast", "acme-swift"); err != nil {
		t.Fatalf("RenameModel: %v", err)
	}
	if reg.Providers[0].Models[0].ID != "acme-swift" {
		t.Errorf("rename did not stick: %+v", reg.Providers[0].Models[0])
	}
	// Sibling fields survive the rename.
	if reg.Providers[0].Models[0].ContextLength != 8192 || len(reg.Providers[0].Models[0].Tags) != 2 {
		t.Errorf("rename dropped sibling fields: %+v", reg.Providers[0].Models[0])
	}
	if err := RenameModel(reg, "acme", "acme-swift", "acme-pro"); err == nil {
		t.Errorf("rename onto an existing sibling ID should fail")
	}
	if reg.Providers[0].Models[0].ID != "acme-swift" {
		t.Errorf("failed rename mutated the registry: %+v", reg.Providers[0].Models[0])
	}
	if err := RenameModel(reg, "acme", "missing", "x"); err == nil {
		t.Errorf("rename of a missing model should fail")
	}
	if err := RenameModel(reg, "missing", "m", "x"); err == nil {
		t.Errorf("rename in a missing provider should fail")
	}
	if err := RenameModel(reg, "acme", "acme-swift", ""); err == nil {
		t.Errorf("rename to an empty ID should fail")
	}
}

// TestModelOps_NeverWholesaleReplace is the structural trap pin: a model op
// must never route through UpsertProvider (which would replace the whole
// Provider struct and drop sibling state). The pin reads the implementation
// source next to this test.
func TestModelOps_NeverWholesaleReplace(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "model_ops.go"))
	if err != nil {
		t.Fatalf("read model_ops.go: %v", err)
	}
	// A same-package call site would read UpsertProvider(...); match call
	// syntax so doc mentions of the name do not trip the pin.
	if strings.Contains(string(data), "UpsertProvider(") {
		t.Errorf("model_ops.go must never call UpsertProvider (wholesale-replace trap)")
	}

	// Behavioral half: after every op, provider scalar fields and sibling
	// model bytes are exactly the fixture's.
	reg := modelOpsFixture()
	ops := []func() error{
		func() error { return AddModel(reg, "acme", Model{ID: "n1"}) },
		func() error { return SetModelEnabled(reg, "acme", "n1", false) },
		func() error { return SetModelTools(reg, "acme", "n1", []string{"read"}) },
		func() error { return UpdateModel(reg, "acme", "n1", Model{ID: "n1", ContextLength: 8}) },
		func() error { return RenameModel(reg, "acme", "n1", "n1r") },
		func() error { return DeleteModel(reg, "acme", "n1r") },
	}
	for i, op := range ops {
		if err := op(); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
		p := reg.Providers[0]
		if p.APIKeyEnv != "ACME_API_KEY" || p.BaseURL != "https://acme.example/v1" || p.Name != "acme" {
			t.Fatalf("op %d replaced provider scalars: %+v", i, p)
		}
	}
	if got := modelJSONs(t, reg.Providers[0].Models); !reflect.DeepEqual(got, modelJSONs(t, modelOpsFixture().Providers[0].Models)) {
		t.Errorf("sibling model bytes drifted across the op sequence:\n%v", got)
	}
}

// TestModelOps_ControlCharsRejected is the registry-boundary defense pin: a
// control rune (ESC class) in a model ID, family, tag, or tool name is
// refused before any mutation, the registry object stays byte-identical, the
// on-disk file in a temp AGY_PRO_HOME is untouched, and the error never
// echoes the offending value. Punctuation-heavy but printable IDs still
// pass.
func TestModelOps_ControlCharsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvProviders, "")
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	reg := modelOpsFixture()
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if err := WriteFileAtomic(path, data, 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// An ESC byte in the model ID is refused.
	escID := "acme-\x1b[2Jpwned"
	err = AddModel(reg, "acme", Model{ID: escID, ContextLength: 1})
	if err == nil {
		t.Fatalf("AddModel accepted a control character in the model ID")
	}
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Errorf("error echoes the raw control character: %q", err.Error())
	}
	if strings.Contains(err.Error(), escID) {
		t.Errorf("error echoes the offending value: %q", err.Error())
	}
	if got := registryJSON(t, reg); !jsonEqual(got, string(diskBefore)) {
		t.Errorf("rejected add mutated the registry object")
	}
	if diskAfter, rerr := os.ReadFile(path); rerr != nil || string(diskAfter) != string(diskBefore) {
		t.Errorf("rejected add touched the on-disk file")
	}

	// The same boundary holds for family, tags, tools, update, and rename,
	// each leaving the registry unchanged.
	before := registryJSON(t, reg)
	cases := []func() error{
		func() error { return AddModel(reg, "acme", Model{ID: "ok-x", Family: "fa\x07m"}) },
		func() error { return AddModel(reg, "acme", Model{ID: "ok-y", Tags: []string{"fine", "t\x1b[0mag"}}) },
		func() error {
			return AddModel(reg, "acme", Model{ID: "ok-z", Capabilities: Capabilities{AllowedTools: []string{"read\x00"}}})
		},
		func() error {
			return UpdateModel(reg, "acme", "acme-fast", Model{ID: "acme-fast", Family: "fa\x1b[0mm"})
		},
		func() error { return UpdateModel(reg, "acme", "acme-fast", Model{ID: "acme-fa\x7fst"}) },
		func() error { return RenameModel(reg, "acme", "acme-fast", "acme-ne\x1b[Hw") },
		func() error { return SetModelTools(reg, "acme", "acme-fast", []string{"read", "exec\x06"}) },
	}
	for i, op := range cases {
		err := op()
		if err == nil {
			t.Errorf("case %d: op accepted a control character", i)
			continue
		}
		if strings.IndexFunc(err.Error(), unicode.IsControl) >= 0 {
			t.Errorf("case %d: error echoes a control character: %q", i, err.Error())
		}
		if got := registryJSON(t, reg); got != before {
			t.Errorf("case %d: rejected op mutated the registry", i)
		}
	}

	// Printable punctuation in every checked field still passes.
	m := Model{
		ID:     "vendor/model-3.5: turbo (preview)!",
		Family: "vendor.family-2",
		Tags:   []string{"fast, cheap", "a=b&c"},
	}
	if err := AddModel(reg, "acme", m); err != nil {
		t.Fatalf("AddModel rejected printable punctuation: %v", err)
	}
	if err := SetModelTools(reg, "acme", m.ID, []string{"read", "exec", "web.fetch"}); err != nil {
		t.Fatalf("SetModelTools rejected printable punctuation: %v", err)
	}
	if got := reg.Providers[0].Models[len(reg.Providers[0].Models)-1]; got.ID != m.ID || got.Family != m.Family || len(got.Tags) != 2 {
		t.Errorf("punctuation model did not persist: %+v", got)
	}
}

// jsonEqual reports whether two JSON documents encode the same value.
func jsonEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// TestModelOps_0600AtomicRoundTrip drives the real file seams (DefaultPath +
// LoadFrom via Load, WriteFileAtomic at 0600) inside a temp AGY_PRO_HOME so
// the live providers.json is never touched.
func TestModelOps_0600AtomicRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvProviders, "")

	reg := modelOpsFixture()
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if filepath.Dir(path) != home {
		t.Fatalf("DefaultPath = %s; want it inside the temp home %s", path, home)
	}
	save := func(r *Registry) {
		t.Helper()
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		data = append(data, '\n')
		if err := WriteFileAtomic(path, data, 0o600); err != nil {
			t.Fatalf("WriteFileAtomic: %v", err)
		}
	}
	assertMode := func() {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if runtime.GOOS != "windows" {
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("registry mode = %#o; want 0600", got)
			}
		}
	}

	save(reg)
	assertMode()
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := AddModel(loaded, "acme", Model{ID: "acme-new", ContextLength: 1024}); err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if err := SetModelEnabled(loaded, "acme", "acme-fast", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	if err := SetModelTools(loaded, "acme", "acme-pro", []string{"write"}); err != nil {
		t.Fatalf("SetModelTools: %v", err)
	}
	if err := RenameModel(loaded, "globex", "globex-1", "globex-2"); err != nil {
		t.Fatalf("RenameModel: %v", err)
	}
	if err := DeleteModel(loaded, "acme", "acme-pro"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	save(loaded)
	assertMode()

	reloaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	acme, ok := reloaded.Provider("acme")
	if !ok {
		t.Fatalf("acme missing after round trip")
	}
	ids := make([]string, len(acme.Models))
	for i, m := range acme.Models {
		ids[i] = m.ID
	}
	if want := []string{"acme-fast", "acme-new"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("acme model ids after round trip = %v; want %v", ids, want)
	}
	if e := acme.Models[0].Enabled; e == nil || *e {
		t.Errorf("enabled=false did not survive the round trip: %+v", acme.Models[0])
	}
	globex, ok := reloaded.Provider("globex")
	if !ok || len(globex.Models) != 1 || globex.Models[0].ID != "globex-2" {
		t.Errorf("globex state after round trip: %+v", globex)
	}
	if err := reloaded.Validate(); err != nil {
		t.Errorf("reloaded registry does not validate: %v", err)
	}
}
