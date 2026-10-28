package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// P4-4b: the explicit-spec family blind spot. A model whose exclusion comes
// from a REGISTRY-DECLARED family ("k3" on a benign id) is flagged in the
// inventory list, but the spec path used to infer the family from the id, so
// the declared family was invisible and the D2 warning never printed. The pick
// was still honored (nothing is rejected anywhere); the gap was silence.
//
// The fixture is the same on both halves of the proof: one model whose family
// is declared "k3" while the id is benign (InferFamily -> "kimi", no k3 id
// token), one model that is only excluded by its k3 ID TOKEN with a benign
// declared family, and one plain model. Written as the real providers.json
// shape so the loader itself is part of the fixture.
const declaredFamilyRegistryJSON = `{
  "providers": [
    {
      "name": "nw",
      "baseURL": "https://api.example.invalid/v1",
      "models": [
        {"id": "kimi-latest", "family": "k3"},
        {"id": "kimi-k3", "family": "kimi"},
        {"id": "glm-5.3-flash-flex"}
      ]
    }
  ]
}`

// specFamilyRegistry is that fixture in memory, for the tests that inject the
// registry through EntriesFromSeatSpecsWithRegistry instead of through the
// loader seam.
func specFamilyRegistry() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example.invalid/v1",
		Models: []providers.Model{
			{ID: "kimi-latest", Family: "k3"},
			{ID: "kimi-k3", Family: "kimi"},
			{ID: "glm-5.3-flash-flex"},
		},
	}}}
}

// declaredFamilyRegistry loads that fixture through the production loader under
// a temp AGY_PRO_HOME, so no test touches the live ~/.agy-pro/providers.json.
func declaredFamilyRegistry(t *testing.T) *providers.Registry {
	t.Helper()
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	path := filepath.Join(os.Getenv("AGY_PRO_HOME"), "providers.json")
	if err := os.WriteFile(path, []byte(declaredFamilyRegistryJSON), 0o644); err != nil {
		t.Fatalf("write fixture registry: %v", err)
	}
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("fixture registry must load: %v", err)
	}
	return reg
}

// specEntries is the seat-spec path with the fixture registry injected, plus the
// warning text it printed.
func specEntries(t *testing.T, reg *providers.Registry, policy providers.ExclusionPolicy, seats string) ([]inventory.Entry, string) {
	t.Helper()
	specs, err := fanout.ParseSeatSpecs(seats)
	if err != nil {
		t.Fatalf("ParseSeatSpecs(%q): %v", seats, err)
	}
	entries := EntriesFromSeatSpecsWithRegistry(specs, policy, reg)
	var warn strings.Builder
	WarnExcludedSpecs(entries, &warn)
	return entries, warn.String()
}

// TestSpecDeclaredFamilyExcludedWarnsEndToEnd is the regression on the
// PRODUCTION spelling: nothing is stubbed, the registry comes from
// providers.Load under a temp home, exactly as `agy pro swarm --seats
// nw/kimi-latest` runs. The seat must carry Entry.Excluded and the shared
// explicit-pick sentence must print once.
func TestSpecDeclaredFamilyExcludedWarnsEndToEnd(t *testing.T) {
	declaredFamilyRegistry(t)
	specs, err := fanout.ParseSeatSpecs("nw/kimi-latest")
	if err != nil {
		t.Fatal(err)
	}
	entries := EntriesFromSeatSpecs(specs, providers.ExclusionPolicy{})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !entries[0].Excluded {
		t.Fatalf("seat %q declares family %q in the registry, so the spec path must carry Entry.Excluded: %+v",
			entries[0].Spec, "k3", entries[0])
	}
	var warn strings.Builder
	WarnExcludedSpecs(entries, &warn)
	if want := sharedPickWarning(1, "nw/kimi-latest"); !strings.Contains(warn.String(), want) {
		t.Errorf("warning = %q; want the shared sentence %q", warn.String(), want)
	}
	if got := strings.Count(warn.String(), "warning:"); got != 1 {
		t.Errorf("exactly one warning expected; got %d: %q", got, warn.String())
	}
}

// TestSpecDeclaredFamilyInjectedRegistry pins the injected half of the same
// rule, plus the Family operand the entry now carries, plus the deliberate
// non-behavior: the pick is HONORED (kept in the returned rows), never dropped.
func TestSpecDeclaredFamilyInjectedRegistry(t *testing.T) {
	entries, warn := specEntries(t, specFamilyRegistry(), providers.ExclusionPolicy{}, "nw/kimi-latest")
	if len(entries) != 1 {
		t.Fatalf("an excluded explicit pick must still be returned (honored, never rejected): %+v", entries)
	}
	if !entries[0].Excluded {
		t.Errorf("declared family k3 must reach the verdict: %+v", entries[0])
	}
	if entries[0].Family != "k3" {
		t.Errorf("Family = %q, want the registry-declared %q", entries[0].Family, "k3")
	}
	if want := sharedPickWarning(1, "nw/kimi-latest"); !strings.Contains(warn, want) {
		t.Errorf("warning = %q; want %q", warn, want)
	}
	// Non-vacuity: the plain model in the same registry stays silent.
	quiet, quietWarn := specEntries(t, specFamilyRegistry(), providers.ExclusionPolicy{}, "nw/glm-5.3-flash-flex")
	if quiet[0].Excluded {
		t.Errorf("a registry model with no declared family must not be excluded: %+v", quiet[0])
	}
	if quietWarn != "" {
		t.Errorf("an eligible spec must print no warning: %q", quietWarn)
	}
}

// TestSpecWithNoRegistryMatchKeepsInferredFamily is the first fallback arm: an
// unknown spec (and a nil registry) keeps the pre-P4-4b id-prefix inference and
// the pre-P4-4b silence.
func TestSpecWithNoRegistryMatchKeepsInferredFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		reg  *providers.Registry
	}{
		{"registry holds no such model", specFamilyRegistry()},
		{"no registry loaded", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, warn := specEntries(t, tc.reg, providers.ExclusionPolicy{}, "nw/unknown-latest")
			if entries[0].Family != "unknown" {
				t.Errorf("Family = %q, want the inferred %q", entries[0].Family, "unknown")
			}
			if entries[0].Excluded {
				t.Errorf("an unknown spec must not be excluded: %+v", entries[0])
			}
			if warn != "" {
				t.Errorf("an eligible spec must print no warning: %q", warn)
			}
			// The id-token half of the built-in rule still works with no
			// registry at all, which is what keeps this arm behavioral.
			if got, _ := specEntries(t, tc.reg, providers.ExclusionPolicy{}, "nw/kimi-k3"); !got[0].Excluded {
				t.Errorf("the k3 id token must still exclude with no registry match: %+v", got[0])
			}
		})
	}
}

// TestNativeSpecKeepsInferredFamily is the second fallback arm: a native seat is
// not a registry model, so even a registry that declares the SAME id as family
// k3 must not change its verdict, while the native id-token rule keeps working.
func TestNativeSpecKeepsInferredFamily(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:    "nw",
		BaseURL: "https://api.example.invalid/v1",
		Models:  []providers.Model{{ID: "gemini-3.8-flash-high", Family: "k3"}},
	}}}
	entries, warn := specEntries(t, reg, providers.ExclusionPolicy{}, "native:gemini-3.8-flash-high")
	if entries[0].Excluded {
		t.Errorf("a native seat must keep the id-inferred family (the native catalog carries no family): %+v", entries[0])
	}
	if entries[0].Family != "gemini" {
		t.Errorf("Family = %q, want the inferred %q", entries[0].Family, "gemini")
	}
	if warn != "" {
		t.Errorf("an eligible native seat must print no warning: %q", warn)
	}

	token, tokenWarn := specEntries(t, reg, providers.ExclusionPolicy{}, "native:k3-agent-swarm")
	if !token[0].Excluded {
		t.Errorf("a native id carrying the k3 token must stay excluded: %+v", token[0])
	}
	if want := sharedPickWarning(1, "native:k3-agent-swarm"); !strings.Contains(tokenWarn, want) {
		t.Errorf("warning = %q; want %q", tokenWarn, want)
	}
}

// TestSpecExcludedByIdStillWarns is the non-regression for the rule the declared
// family must never CANCEL: a model excluded by its id token stays excluded when
// the registry declares a benign family for it, named by id or provider/model.
func TestSpecExcludedByIdStillWarns(t *testing.T) {
	for _, seats := range []string{"nw/kimi-k3", "kimi-k3"} {
		entries, warn := specEntries(t, specFamilyRegistry(), providers.ExclusionPolicy{}, seats)
		if !entries[0].Excluded {
			t.Errorf("%q: a benign declared family must not clear the k3 id-token rule: %+v", seats, entries[0])
		}
		if want := sharedPickWarning(1, seats); !strings.Contains(warn, want) {
			t.Errorf("%q: warning = %q; want %q", seats, warn, want)
		}
	}
}

// TestSpecDeclaredFamilyHonoredKeepsFanoutVector pins that the new verdict does
// not touch the dry-run vector: same 7 flags, same order, the honored spec
// verbatim (13 elements = 7 flags, 6 of them with a value).
func TestSpecDeclaredFamilyHonoredKeepsFanoutVector(t *testing.T) {
	entries, _ := specEntries(t, specFamilyRegistry(), providers.ExclusionPolicy{}, "nw/kimi-latest")
	got := BuildFanoutArgs(Templates()[0], entries, "/ws", "p", 300)
	wantFlags := []string{"--task", "--seats", "--root", "--timeout", "--tools", "--prompt", "--json"}
	if len(got) != 2*len(wantFlags)-1 {
		t.Fatalf("args = %q; want %d elements (7 flags, 6 with values)", got, 2*len(wantFlags)-1)
	}
	for i, f := range wantFlags {
		if got[2*i] != f {
			t.Errorf("args[%d] = %q, want %q (full vector %q)", 2*i, got[2*i], f, got)
		}
	}
	if got[3] != "nw/kimi-latest" {
		t.Errorf("--seats = %q, want the honored declared-family spec %q", got[3], "nw/kimi-latest")
	}
}

// TestSpecAndSeatListAgreeOnDeclaredFamily pins the reason the fix belongs on
// this path at all: the seat list already flags the model at Load, so the spec
// path must produce the same verdict and the same Family for the same model,
// otherwise the warning and the listing disagree about one row.
func TestSpecAndSeatListAgreeOnDeclaredFamily(t *testing.T) {
	reg := specFamilyRegistry()
	inv := buildInventory(t, nil, reg.Providers)
	var row *inventory.Entry
	for i := range inv.Entries {
		if inv.Entries[i].ModelID == "kimi-latest" {
			row = &inv.Entries[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("fixture inventory must hold kimi-latest: %+v", inv.Entries)
	}
	if !row.Excluded {
		t.Fatalf("the seat list must flag the declared-family model: %+v", *row)
	}
	entries, _ := specEntries(t, reg, providers.ExclusionPolicy{}, "nw/kimi-latest")
	if entries[0].Excluded != row.Excluded {
		t.Errorf("verdict drift: spec path %v, seat list %v", entries[0].Excluded, row.Excluded)
	}
	if entries[0].Family != row.Family {
		t.Errorf("family drift: spec path %q, seat list %q", entries[0].Family, row.Family)
	}
}
