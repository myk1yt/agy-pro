package providers

import (
	"os"
	"strings"
	"testing"
)

func TestUpsertProvider_AddAndReplace(t *testing.T) {
	reg := &Registry{}
	p := Provider{
		Name:      "acme",
		BaseURL:   "https://acme.example/v1",
		APIKeyEnv: "ACME_API_KEY",
		Models:    []Model{{ID: "m1"}},
	}
	if err := UpsertProvider(reg, p); err != nil {
		t.Fatalf("UpsertProvider add: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "acme" {
		t.Fatalf("after add: %+v", reg.Providers)
	}

	// Replace by case-insensitive name (wizard upsert semantics).
	p2 := p
	p2.Models = []Model{{ID: "m2"}}
	if err := UpsertProvider(reg, p2); err != nil {
		t.Fatalf("UpsertProvider replace: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Models[0].ID != "m2" {
		t.Fatalf("after replace: %+v", reg.Providers)
	}
}

func TestUpsertProvider_RejectsKeyValues(t *testing.T) {
	reg := &Registry{}
	// A key VALUE in the apiKeyEnv slot must be rejected with an error that
	// never echoes the value (F-PROV / A4).
	secret := "sk-verysecretkey-0123456789abcdef"
	p := Provider{Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: secret}
	err := UpsertProvider(reg, p)
	if err == nil {
		t.Fatalf("UpsertProvider accepted a key VALUE in apiKeyEnv")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("rejection error echoes the key value: %v", err)
	}
	if len(reg.Providers) != 0 {
		t.Errorf("failed upsert must not mutate the registry: %+v", reg.Providers)
	}

	// Env-var NAMES stay accepted.
	p.APIKeyEnv = "ACME_API_KEY"
	if err := UpsertProvider(reg, p); err != nil {
		t.Fatalf("UpsertProvider with env NAME: %v", err)
	}
}

func TestUpsertProvider_ValidateFailureRollsBack(t *testing.T) {
	reg := &Registry{Providers: []Provider{
		{Name: "a", BaseURL: "https://a.example/v1"},
		{Name: "a", BaseURL: "https://dup.example/v1"}, // pre-existing dup breaks Validate
	}}
	before := len(reg.Providers)
	err := UpsertProvider(reg, Provider{Name: "b", BaseURL: "https://b.example/v1"})
	if err == nil {
		t.Fatalf("UpsertProvider should fail when the registry cannot validate")
	}
	if len(reg.Providers) != before {
		t.Errorf("failed upsert mutated registry: %+v", reg.Providers)
	}
}

func TestRenameProvider(t *testing.T) {
	reg := &Registry{Providers: []Provider{
		{Name: "old", BaseURL: "https://old.example/v1", APIKeyEnv: "OLD_KEY", Models: []Model{{ID: "m1"}}},
		{Name: "taken", BaseURL: "https://taken.example/v1"},
	}}
	if err := RenameProvider(reg, "old", "new"); err != nil {
		t.Fatalf("RenameProvider: %v", err)
	}
	if reg.Providers[0].Name != "new" {
		t.Errorf("rename did not stick: %+v", reg.Providers[0])
	}
	if reg.Providers[0].APIKeyEnv != "OLD_KEY" || len(reg.Providers[0].Models) != 1 {
		t.Errorf("rename must not touch key env or models: %+v", reg.Providers[0])
	}

	if err := RenameProvider(reg, "missing", "x"); err == nil {
		t.Error("rename of a missing provider should fail")
	}
	if err := RenameProvider(reg, "new", "taken"); err == nil {
		t.Error("rename onto an existing name should fail")
	}
	if reg.Providers[0].Name != "new" {
		t.Errorf("failed rename mutated the registry: %+v", reg.Providers[0])
	}
}

func TestDeleteProvider_RegistryEntryOnly(t *testing.T) {
	t.Setenv("DEL_KEY", "sk-keep-this-in-env")
	reg := &Registry{Providers: []Provider{
		{Name: "gone", BaseURL: "https://gone.example/v1", APIKeyEnv: "DEL_KEY"},
		{Name: "stay", BaseURL: "https://stay.example/v1"},
	}}
	if err := DeleteProvider(reg, "gone"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "stay" {
		t.Fatalf("after delete: %+v", reg.Providers)
	}
	if got := reg.Providers; len(got) != 1 {
		t.Errorf("unexpected providers: %+v", got)
	}
	// Registry-entry removal ONLY: the env value survives for reuse.
	if v := os.Getenv("DEL_KEY"); v != "sk-keep-this-in-env" {
		t.Errorf("delete touched the env value: %q", v)
	}
	if err := DeleteProvider(reg, "gone"); err == nil {
		t.Error("deleting a missing provider should fail")
	}
}

func TestLooksLikeAPIKey_MatchesWizardGuard(t *testing.T) {
	if !LooksLikeAPIKey("sk-abcdef0123456789") {
		t.Error("sk- key should look like a key")
	}
	if LooksLikeAPIKey("MY_API_KEY") {
		t.Error("env NAME should not look like a key")
	}
}
