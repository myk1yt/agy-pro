package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

// maskNamePlant is the credential-class plant for the fix7-D name-sink pins: an
// underscore-headed key shape a hand edit (or a registry written before the
// ingest doors closed) can leave in a provider NAME. MaskOperand is identity on
// it (the "_" breaks its contiguous-run test and its head is not in the shared
// prefix list); the registry-slot display authority MaskSlot masks it.
const (
	maskNamePlant = "sk_live_0123456789abcdef"
	maskNameMask  = "sk_li****"
)

// TestMaskProviderForReportNameUsesSlotAuthority (fix7-D) pins the report
// sink's NAME field against the registry-slot display authority. Identity
// half: the benign corpus renders byte-identically, exactly as the previous
// MaskOperand rendering did (MaskSlot's first stage IS MaskOperand). Credential
// half: the plant renders masked in the masked copy AND in the serialized
// report body, which is the surface fanout's seat copy and summary.json read.
func TestMaskProviderForReportNameUsesSlotAuthority(t *testing.T) {
	// The required names plus the names the pre-existing report pins use.
	corpus := []string{
		"openai", "gemini", "deepseek", "nw", "charm",
		"google", "anthropic", "acme-corp", "test-all",
	}
	names := append(append([]string(nil), corpus...), maskNamePlant)

	for _, n := range names {
		got := MaskProviderForReport(Provider{Name: n})
		if got.Name != MaskSlot(n) {
			t.Errorf("MaskProviderForReport(Name=%q).Name = %q; want the slot authority rendering %q", n, got.Name, MaskSlot(n))
		}
		want := n
		if n == maskNamePlant {
			want = maskNameMask
		}
		if got.Name != want {
			t.Errorf("MaskProviderForReport(Name=%q).Name = %q; want %q", n, got.Name, want)
		}
		if n != maskNamePlant && got.Name != MaskOperand(n) {
			t.Errorf("benign name %q changed rendering: %q; want the operand rendering %q", n, got.Name, MaskOperand(n))
		}
	}

	// The report's other fields keep their own maskers and their benign
	// renderings: only the name rendering tightened.
	p := Provider{
		Name:      maskNamePlant,
		BaseURL:   "https://gw.example/v1",
		APIKeyEnv: "OPENAI_API_KEY",
		Models:    []Model{{ID: "gemini-2.5-flash"}},
	}
	got := MaskProviderForReport(p)
	if got.BaseURL != p.BaseURL {
		t.Errorf("BaseURL = %q; want the benign URL unchanged", got.BaseURL)
	}
	if got.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("APIKeyEnv = %q; want the env-var name unchanged", got.APIKeyEnv)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gemini-2.5-flash" {
		t.Errorf("Models = %+v; want the benign model id unchanged", got.Models)
	}

	// The serialized report body (summary.json / fanout seat copy) carries the
	// mask and never the raw name.
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal masked provider: %v", err)
	}
	if strings.Contains(string(raw), maskNamePlant) {
		t.Errorf("serialized report leaks the credential-shaped name: %s", raw)
	}
	if !strings.Contains(string(raw), maskNameMask) {
		t.Errorf("serialized report lacks the masked name: %s", raw)
	}

	// The caller's registry value is not mutated.
	if p.Name != maskNamePlant {
		t.Errorf("MaskProviderForReport mutated the input name: %q", p.Name)
	}
}
