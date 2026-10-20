package providers

import (
	"errors"
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

// TestRenameProvider_CredentialGate pins FIX-7E at the registry op: the NEW
// name is gated by the shared NAME authority before the charset check and before
// any mutation, so a key-shaped rename is never persisted, the refusal is the
// named value-free error, and the registry keeps its old name. The benign arm
// still succeeds and moves nothing but the name.
func TestRenameProvider_CredentialGate(t *testing.T) {
	base := func() *Registry {
		return &Registry{Providers: []Provider{
			{Name: "old", BaseURL: "https://old.example/v1", APIKeyEnv: "OLD_KEY", Models: []Model{{ID: "m1"}}},
		}}
	}
	probes := []string{
		"sk_live_0123456789abcdef",
		"tok_a1b2c3d4e5f6g7h8i9j0",
		"pk_live_0123456789abcdef",
		"key_0123456789abcdef",
		"AKIA_0123456789ABCDEF",
	}
	for _, probe := range probes {
		// The premise: the charset alone admits every probe, so the new gate is
		// what refuses them.
		if err := ValidateProviderName(probe); err != nil {
			t.Fatalf("premise broken: %q is refused by the charset: %v", probe, err)
		}
		reg := base()
		err := RenameProvider(reg, "old", probe)
		if !errors.Is(err, ErrProviderNameCarriesCredential) {
			t.Fatalf("RenameProvider(old, %q) = %v; want ErrProviderNameCarriesCredential", probe, err)
		}
		assertNoLeak(t, err.Error(), probe)
		if reg.Providers[0].Name != "old" || len(reg.Providers) != 1 {
			t.Errorf("a refused rename mutated the registry: %+v", reg.Providers)
		}
	}

	// A charset-invalid name still takes the charset arm: the gate must not blunt
	// the shell-safety rule (or reorder it into a wrong class report).
	if err := RenameProvider(base(), "old", "new name"); err == nil || errors.Is(err, ErrProviderNameCarriesCredential) {
		t.Errorf(`RenameProvider(old, "new name") = %v; want the charset refusal`, err)
	}

	// The benign arm: the name moves, nothing else does.
	reg := base()
	if err := RenameProvider(reg, "old", "new"); err != nil {
		t.Fatalf("benign rename: %v", err)
	}
	if reg.Providers[0].Name != "new" || reg.Providers[0].APIKeyEnv != "OLD_KEY" ||
		len(reg.Providers[0].Models) != 1 || reg.Providers[0].BaseURL != "https://old.example/v1" {
		t.Errorf("benign rename changed the record beyond the name: %+v", reg.Providers[0])
	}
}

// ---------------------------------------------------------------------------
// FIX-8b — the NAME operands of the rename/delete/upsert error compositions
// render through the registry-slot authority.
// ---------------------------------------------------------------------------

// underscoreNamePlant is the FIX-8b target class in a provider NAME slot:
// underscore-head key shapes whose head is absent from the shared prefix list
// and whose "_" breaks the opaque-run test, so providers.SecretShaped is false
// and MaskOperand is the identity on all of them while MaskSlot masks the 16+
// rune segment behind the separator. Synthetic values only.
var underscoreNamePlant = "sk_live_0123456789abcdef"

const (
	underscoreNameMasked = "sk_li****"
	underscoreNameTail   = "0123456789abcdef"
)

// maskSlotMasked reports whether the registry-slot authority actually MASKS v,
// as opposed to merely re-rendering it (a marker strip is a rewrite, not a
// mask; MaskSecret is the one masking form and it ends in "****").
func maskSlotMasked(v string) bool {
	m := MaskSlot(v)
	return m != v && strings.HasSuffix(m, "****")
}

// nameEchoSite is one error composition of registry_ops.go that echoes a
// provider NAME, with the exact sentence it produced before the fix
// (MaskOperand on the name operand).
type nameEchoSite struct {
	label string
	run   func(name string) error
	// want composes the pre-fix sentence for the given rendered name.
	want func(rendered string) string
}

// nameEchoSites is the closed list of NAME-operand compositions in this file.
// The already-exists sentence is listed last with its reachability note: the
// NEW name is gated by NameCarriesCredential before the collision check, and
// that predicate is a superset of "MaskSlot changes the value", so no value
// this site can see is maskable by the slot authority.
func nameEchoSites() []nameEchoSite {
	empty := func() *Registry { return &Registry{} }
	return []nameEchoSite{
		{
			label: "rename not found",
			run:   func(name string) error { return RenameProvider(empty(), name, "acme2") },
			want:  func(r string) string { return `provider "` + r + `" not found` },
		},
		{
			label: "delete not found",
			run:   func(name string) error { return DeleteProvider(empty(), name) },
			want:  func(r string) string { return `provider "` + r + `" not found` },
		},
		{
			label: "upsert baseURL required",
			run:   func(name string) error { return UpsertProvider(empty(), Provider{Name: name}) },
			want:  func(r string) string { return `provider "` + r + `": baseURL is required` },
		},
		{
			label: "upsert invalid baseURL",
			run: func(name string) error {
				return UpsertProvider(empty(), Provider{Name: name, BaseURL: "ftp://acme.example/v1"})
			},
			want: func(r string) string {
				return `provider "` + r + `": baseURL must start with http:// or https://`
			},
		},
		{
			label: "upsert apiKeyEnv key value",
			run: func(name string) error {
				return UpsertProvider(empty(), Provider{
					Name: name, BaseURL: "https://acme.example/v1",
					APIKeyEnv: "sk-verysecretkey-0123456789abcdef",
				})
			},
			want: func(r string) string {
				return `provider "` + r + `": apiKeyEnv must be an environment variable NAME, not a key (value hidden)`
			},
		},
		{
			label: "rename already exists",
			run: func(name string) error {
				reg := &Registry{Providers: []Provider{
					{Name: "other", BaseURL: "https://other.example/v1"},
					{Name: name, BaseURL: "https://taken.example/v1"},
				}}
				return RenameProvider(reg, "other", name)
			},
			want: func(r string) string { return `provider "` + r + `" already exists` },
		},
	}
}

// TestRegistryOps_NameOperandsSlotMasked pins every NAME-operand composition of
// registry_ops.go: the underscore-head plant renders masked with no raw value
// or tail in the sentence, while the benign corpus stays byte-identical to the
// pre-fix text. The pre-fix arm (MaskOperand) is restated as a live premise, so
// the pin fails if the plant stops exercising the gap or if a site falls back
// to the location-exempting masker.
func TestRegistryOps_NameOperandsSlotMasked(t *testing.T) {
	// Premise: this class is exactly what the pre-fix masker cannot see.
	if got := MaskOperand(underscoreNamePlant); got != underscoreNamePlant {
		t.Fatalf("premise broken: MaskOperand now masks the class (%q)", got)
	}
	if SecretShaped(underscoreNamePlant) {
		t.Fatalf("premise broken: the plant is caught by the shared shape test")
	}
	if got := MaskSlot(underscoreNamePlant); got != underscoreNameMasked {
		t.Fatalf("premise broken: MaskSlot(plant) = %q; want %q", got, underscoreNameMasked)
	}

	for _, site := range nameEchoSites() {
		t.Run(site.label, func(t *testing.T) {
			if site.label == "rename already exists" {
				// Reachability: the gate refuses the plant before the collision
				// check, so this site can never see a maskable name.
				if err := site.run(underscoreNamePlant); !errors.Is(err, ErrProviderNameCarriesCredential) {
					t.Fatalf("rename already-exists with the plant = %v; want the credential gate first", err)
				}
				if !NameCarriesCredential(underscoreNamePlant) {
					t.Fatal("the gate predicate no longer covers the plant")
				}
				return
			}
			err := site.run(underscoreNamePlant)
			if err == nil {
				t.Fatalf("%s accepted the plant", site.label)
			}
			got := err.Error()
			if want := site.want(underscoreNameMasked); got != want {
				t.Errorf("full sentence drifted:\ngot:  %q\nwant: %q", got, want)
			}
			if preFix := site.want(underscoreNamePlant); preFix == site.want(underscoreNameMasked) {
				t.Fatalf("pre-fix and post-fix sentences are identical; the site does not exercise the class")
			}
			if strings.Contains(got, underscoreNamePlant) || strings.Contains(got, underscoreNameTail) {
				t.Errorf("%s echoes the planted name: %q", site.label, got)
			}
			if !strings.Contains(got, underscoreNameMasked) {
				t.Errorf("%s is missing the masked name %q: %q", site.label, underscoreNameMasked, got)
			}
		})
	}

	// The healthy registry takes neither site: the plant is missing, so the
	// masked sentence is the not-found one and the registry is untouched.
	t.Run("no mutation on the refusal", func(t *testing.T) {
		reg := &Registry{Providers: []Provider{{Name: "acme", BaseURL: "https://acme.example/v1"}}}
		if err := DeleteProvider(reg, underscoreNamePlant); err == nil {
			t.Fatal("delete of the plant succeeded")
		}
		if err := RenameProvider(reg, underscoreNamePlant, "acme2"); err == nil {
			t.Fatal("rename of the plant succeeded")
		}
		if len(reg.Providers) != 1 || reg.Providers[0].Name != "acme" {
			t.Fatalf("a refused op mutated the registry: %+v", reg.Providers)
		}
	})

	t.Run("benign corpus byte-identical", func(t *testing.T) {
		for _, name := range []string{
			"acme", "beta", "openai", "gemini", "deepseek", "anthropic", "acme-fast",
			"acme_internal", "key_backup", "aws_prod", "gateway-eu", "my-gateway.example",
			"bedrock-us-east1", "ACME", "BeTa", "x",
		} {
			for _, site := range nameEchoSites() {
				if site.label == "rename already exists" {
					continue // the plant arm above proves the reachability; the benign arm follows
				}
				t.Run(name+"/"+site.label, func(t *testing.T) {
					// The pre-fix renderer is the identity on the corpus, so the
					// post-fix sentence must be byte-identical to it.
					if got := MaskOperand(name); got != name {
						t.Fatalf("corpus name %q is not identity under the pre-fix masker (%q)", name, got)
					}
					err := site.run(name)
					if err == nil {
						t.Fatalf("%s accepted the benign name", site.label)
					}
					if got, want := err.Error(), site.want(name); got != want {
						t.Errorf("benign sentence drifted:\ngot:  %q\nwant: %q", got, want)
					}
					if strings.Contains(err.Error(), "****") {
						t.Errorf("benign name %q was masked: %q", name, err.Error())
					}
				})
			}
		}
	})

	// The already-exists sentence keeps its exact text for a benign name.
	t.Run("already exists byte-identical", func(t *testing.T) {
		reg := &Registry{Providers: []Provider{
			{Name: "other", BaseURL: "https://other.example/v1"},
			{Name: "taken", BaseURL: "https://taken.example/v1"},
		}}
		err := RenameProvider(reg, "other", "taken")
		if err == nil {
			t.Fatal("rename onto an existing name succeeded")
		}
		if want := `provider "taken" already exists`; err.Error() != want {
			t.Errorf("already-exists sentence drifted:\ngot:  %q\nwant: %q", err.Error(), want)
		}
	})

	// Every remaining underscore probe masks too, so the fix is the class rather
	// than the one pinned string.
	t.Run("underscore class masks at every site", func(t *testing.T) {
		for _, probe := range []string{
			"sk_live_0123456789abcdef",
			"tok_a1b2c3d4e5f6g7h8i9j0",
			"pk_live_0123456789abcdef",
			"key_0123456789abcdef",
			"AKIA_0123456789ABCDEF",
		} {
			masked := MaskSlot(probe)
			if masked == probe {
				t.Fatalf("premise broken: MaskSlot(%q) is the identity", probe)
			}
			for _, site := range nameEchoSites() {
				if site.label == "rename already exists" {
					continue
				}
				err := site.run(probe)
				if err == nil {
					t.Fatalf("%s accepted %q", site.label, probe)
				}
				if !strings.Contains(err.Error(), masked) {
					t.Errorf("%s does not render %q masked: %q", site.label, probe, err.Error())
				}
				if strings.Contains(err.Error(), probe) {
					t.Errorf("%s echoes %q raw: %q", site.label, probe, err.Error())
				}
			}
		}
	})
}

// TestMaskNameOperandComposition pins the three rules of the error-sentence
// name masker, so the deliberate deviation from a bare MaskSlot call is a
// measured contract rather than an unexplained exception.
func TestMaskNameOperandComposition(t *testing.T) {
	t.Run("rule 1: a MaskOperand rewrite is kept", func(t *testing.T) {
		for _, v := range []string{
			"sk-verysecretkey-0123456789abcdef",
			"AKIAIOSFODNN7EXAMPLE",
			"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		} {
			if got, want := maskNameOperand(v), MaskOperand(v); got != want {
				t.Errorf("maskNameOperand(%q) = %q; want the MaskOperand rendering %q", v, got, want)
			}
			if maskNameOperand(v) == v {
				t.Errorf("maskNameOperand(%q) left a credential-shaped operand readable", v)
			}
		}
	})

	t.Run("rule 2: composites are caught by exactly one of the two layers", func(t *testing.T) {
		// Both doors (the settings TUI frame and the CLI verbs) pass the error
		// sentence through MaskSentence(..., FlagSeedOperands(operand)...). The
		// contract this pin measures: for every composite, the door's masker
		// either already catches it (then the sentence keeps its bytes and the
		// pins that measure those bytes stay put) or this masker masks it whole,
		// and the pair never leaves a planted token readable.
		const awsPlant = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		doorCatches := func(v string) bool {
			quoted := `"` + v + `"`
			return MaskSentence(quoted, FlagSeedOperands(v)...) != quoted
		}
		caught, maskedHere, readable := 0, 0, 0
		for _, tc := range []struct {
			raw   string
			plant []string
		}{
			{"./backups," + awsPlant, []string{awsPlant}},
			{"beta," + awsPlant, []string{awsPlant}},
			{"./backups," + underscoreNamePlant, []string{underscoreNamePlant, underscoreNameTail}},
			{"beta," + underscoreNamePlant, []string{underscoreNamePlant, underscoreNameTail}},
			{underscoreNamePlant + ";beta", []string{underscoreNamePlant, underscoreNameTail}},
			{"beta " + underscoreNamePlant, []string{underscoreNamePlant, underscoreNameTail}},
			{"read,search", nil},
		} {
			got := maskNameOperand(tc.raw)
			switch {
			case MaskOperand(tc.raw) != tc.raw:
				// Rule 1 owns it: MaskOperand's own rewrite is kept byte for byte.
				if got != MaskOperand(tc.raw) {
					t.Errorf("maskNameOperand(%q) = %q; want the MaskOperand rendering %q", tc.raw, got, MaskOperand(tc.raw))
				}
				caught++
			case doorCatches(tc.raw):
				// The door's seeded masker renders this composite, so the sentence
				// keeps its bytes (the rendering the settings and CLI pins measure).
				caught++
				if got != tc.raw {
					t.Errorf("maskNameOperand(%q) = %q; the door's masker owns this composite", tc.raw, got)
				}
			case maskSlotMasked(tc.raw):
				maskedHere++
				if got != MaskSlot(tc.raw) {
					t.Errorf("maskNameOperand(%q) = %q; want the slot mask %q", tc.raw, got, MaskSlot(tc.raw))
				}
			default:
				// Benign composite: no layer changes it.
				readable++
				if got != tc.raw {
					t.Errorf("maskNameOperand(%q) = %q; want it readable", tc.raw, got)
				}
			}
			sentence := `provider "` + got + `" not found`
			seeded := MaskSentence(sentence, FlagSeedOperands(tc.raw)...)
			for _, plant := range tc.plant {
				if strings.Contains(seeded, plant) {
					t.Errorf("the pair leaves the planted token %q readable: %q (masker output %q)", plant, seeded, got)
				}
			}
		}
		if caught == 0 || maskedHere == 0 {
			t.Fatalf("the corpus must exercise both branches (door caught %d, masked here %d, readable %d)", caught, maskedHere, readable)
		}
	})

	t.Run("rule 3: only an actual mask is taken from the slot authority", func(t *testing.T) {
		// A location operand is REWRITTEN by the slot authority (marker strip)
		// but not masked, so the sentence keeps its bytes.
		for _, v := range []string{"./backups", "../registry", `C:\data`, "https://acme.example/v1"} {
			if got := maskNameOperand(v); got != v {
				t.Errorf("maskNameOperand(%q) = %q; a benign location must keep its bytes", v, got)
			}
		}
		// The underscore class IS masked, and the accepted form is a mask.
		for _, probe := range []string{
			"sk_live_0123456789abcdef",
			"tok_a1b2c3d4e5f6g7h8i9j0",
			"pk_live_0123456789abcdef",
			"key_0123456789abcdef",
			"AKIA_0123456789ABCDEF",
		} {
			got := maskNameOperand(probe)
			if got == probe {
				t.Fatalf("maskNameOperand(%q) is the identity", probe)
			}
			if !strings.HasSuffix(got, "****") || got != MaskSlot(probe) {
				t.Errorf("maskNameOperand(%q) = %q; want the slot mask %q", probe, got, MaskSlot(probe))
			}
			if got != "" && strings.Contains(got, probe[len(probe)-8:]) {
				t.Errorf("maskNameOperand(%q) keeps a tail of the value: %q", probe, got)
			}
		}
	})
}
