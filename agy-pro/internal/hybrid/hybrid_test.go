package hybrid

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// --- fixtures ---------------------------------------------------------------

// boolPtr returns a pointer to b, for Model.Enabled (nil means enabled).
func boolPtr(b bool) *bool { return &b }

// enabled builds an enabled model with the given id and context length.
func enabled(id string, ctx int) providers.Model {
	return providers.Model{ID: id, ContextLength: ctx}
}

// mustBuild runs BuildFetchAvailableModels and fails the test on error.
func mustBuild(t *testing.T, reg *providers.Registry, opts CatalogOptions) []byte {
	t.Helper()
	data, err := BuildFetchAvailableModels(reg, opts)
	if err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	return data
}

// catalogShape mirrors the minimal fetchAvailableModels payload for assertions.
type catalogShape struct {
	Models          map[string]map[string]any `json:"models"`
	AgentModelSorts []struct {
		DisplayName string `json:"display_name"`
		Groups      []struct {
			DisplayName string   `json:"display_name"`
			ModelIDs    []string `json:"model_ids"`
		} `json:"groups"`
	} `json:"agent_model_sorts"`
}

func parseCatalog(t *testing.T, data []byte) catalogShape {
	t.Helper()
	var shape catalogShape
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatalf("catalog is not valid JSON: %v\n%s", err, data)
	}
	return shape
}

// twoProviderRegistry is a small deterministic registry: provider alpha has two
// enabled models plus one disabled; provider beta has one.
func twoProviderRegistry() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{Name: "alpha", Models: []providers.Model{
			enabled("ext-one", 4096),
			enabled("ext-two", 300000),
			{ID: "ext-off", Enabled: boolPtr(false)},
		}},
		{Name: "beta", Models: []providers.Model{
			enabled("ext-three", 0),
		}},
	}}
}

// --- enum allocation --------------------------------------------------------

func TestEnumAllocation_DistinctSkipsReservedMonotonic(t *testing.T) {
	reg := twoProviderRegistry()
	// EnumBase sits in the static range so the reserved skips (326, 330) fire.
	opts := CatalogOptions{EnumBase: 326, ReservedEnums: DefaultReservedEnums()}
	shape := parseCatalog(t, mustBuild(t, reg, opts))

	// Registry order of ENABLED models: ext-one, ext-two, ext-three.
	wantOrder := []string{"ext-one", "ext-two", "ext-three"}
	wantEnums := []float64{327, 328, 329} // 326 and 330 are reserved and skipped
	// Note: ext-three would be 331 only if a fourth model crossed 330; with three
	// models the sequence is 327,328,329 (330 is reserved but not yet reached).
	got := make([]float64, 0, len(wantOrder))
	seen := map[float64]bool{}
	for _, id := range wantOrder {
		entry, ok := shape.Models[id]
		if !ok {
			t.Fatalf("model %q missing from catalog", id)
		}
		e, ok := entry["model"].(float64)
		if !ok {
			t.Fatalf("model %q enum is not a JSON number: %T", id, entry["model"])
		}
		if seen[e] {
			t.Fatalf("enum %v allocated twice", e)
		}
		seen[e] = true
		if DefaultReservedEnums()[int(e)] {
			t.Fatalf("enum %v collides with a reserved static value", int(e))
		}
		got = append(got, e)
	}
	if !reflect.DeepEqual(got, wantEnums) {
		t.Fatalf("enums = %v; want %v (reserved 326/330 skipped, monotonic)", got, wantEnums)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("enums not monotonic: %v", got)
		}
	}
}

func TestEnumAllocation_SkipsReservedAcrossManyModels(t *testing.T) {
	// Enough models to walk the allocator past reserved 330.
	var models []providers.Model
	for i := 0; i < 5; i++ {
		models = append(models, enabled("ext-"+string(rune('a'+i)), 300000))
	}
	reg := &providers.Registry{Providers: []providers.Provider{{Name: "p", Models: models}}}
	opts := CatalogOptions{EnumBase: 326, ReservedEnums: DefaultReservedEnums()}
	shape := parseCatalog(t, mustBuild(t, reg, opts))

	want := []float64{327, 328, 329, 331, 332} // 326 and 330 skipped
	for i, id := range []string{"ext-a", "ext-b", "ext-c", "ext-d", "ext-e"} {
		e := shape.Models[id]["model"].(float64)
		if e != want[i] {
			t.Fatalf("model %q enum = %v; want %v", id, e, want[i])
		}
	}
}

func TestEnumAllocation_DeterministicAcrossBuilds(t *testing.T) {
	reg := twoProviderRegistry()
	opts := CatalogOptions{ReservedEnums: DefaultReservedEnums()}
	a := mustBuild(t, reg, opts)
	b := mustBuild(t, reg, opts)
	if string(a) != string(b) {
		t.Fatalf("two builds diverged:\n%s\n---\n%s", a, b)
	}
}

// --- display_name + limits --------------------------------------------------

func TestDisplayNameFormatAndLimits(t *testing.T) {
	reg := twoProviderRegistry()
	shape := parseCatalog(t, mustBuild(t, reg, CatalogOptions{ReservedEnums: DefaultReservedEnums()}))

	one := shape.Models["ext-one"]
	dn, _ := one["display_name"].(string)
	if dn != "ext-one (alpha)" {
		t.Fatalf("ext-one display_name = %q; want %q", dn, "ext-one (alpha)")
	}
	if dn == "" {
		t.Fatal("display_name must be non-empty")
	}
	// 4096 context -> generous default 131072.
	if mt, _ := one["max_tokens"].(float64); mt != 131072 {
		t.Fatalf("ext-one max_tokens = %v; want 131072 (4096 < 25000 floor)", mt)
	}
	// MaxOutputTokens unset (0) -> default 16384.
	if mo, _ := one["max_output_tokens"].(float64); mo != 16384 {
		t.Fatalf("ext-one max_output_tokens = %v; want 16384", mo)
	}
	// 300000 context (>= floor) -> kept verbatim.
	two := shape.Models["ext-two"]
	if mt, _ := two["max_tokens"].(float64); mt != 300000 {
		t.Fatalf("ext-two max_tokens = %v; want 300000", mt)
	}
	// api_provider is present on every entry.
	if ap, _ := two["api_provider"].(string); ap != "API_PROVIDER_GOOGLE" {
		t.Fatalf("ext-two api_provider = %q; want API_PROVIDER_GOOGLE", ap)
	}
}

func TestMaxOutputTokensServedWhenPositive(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "p",
		Models: []providers.Model{{ID: "m", ContextLength: 300000, MaxOutputTokens: 65536}},
	}}}
	shape := parseCatalog(t, mustBuild(t, reg, CatalogOptions{}))
	if mo, _ := shape.Models["m"]["max_output_tokens"].(float64); mo != 65536 {
		t.Fatalf("max_output_tokens = %v; want 65536", mo)
	}
}

// --- sorts membership -------------------------------------------------------

func TestSortsMembership_EveryIDInExactlyOneGroup(t *testing.T) {
	reg := twoProviderRegistry()
	shape := parseCatalog(t, mustBuild(t, reg, CatalogOptions{ReservedEnums: DefaultReservedEnums()}))

	if len(shape.AgentModelSorts) != 1 {
		t.Fatalf("want exactly one sort, got %d", len(shape.AgentModelSorts))
	}
	counts := map[string]int{}
	for _, g := range shape.AgentModelSorts[0].Groups {
		for _, id := range g.ModelIDs {
			counts[id]++
		}
	}
	for id := range shape.Models {
		if counts[id] != 1 {
			t.Fatalf("id %q appears in %d groups; want exactly 1", id, counts[id])
		}
	}
	// The disabled model is never listed nor grouped.
	if _, ok := shape.Models["ext-off"]; ok {
		t.Fatal("disabled model ext-off must not be listed")
	}
	if counts["ext-off"] != 0 {
		t.Fatal("disabled model ext-off must not be in any group")
	}
}

// --- collision fail-closed --------------------------------------------------

func TestCollision_DropsLaterDuplicateAndLogs(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "first", Models: []providers.Model{enabled("shared", 300000)}},
		{Name: "second", Models: []providers.Model{
			enabled("shared", 300000), // duplicate id: dropped
			enabled("solo", 300000),
		}},
	}}
	var logs []string
	opts := CatalogOptions{
		ReservedEnums: DefaultReservedEnums(),
		Logf:          func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	}
	shape := parseCatalog(t, mustBuild(t, reg, opts))

	// One entry survives for "shared"; total entries = 2 (shared + solo), which
	// is one fewer than the 3 enabled models -> one enum not allocated.
	if len(shape.Models) != 2 {
		t.Fatalf("models = %d; want 2 (duplicate dropped)", len(shape.Models))
	}
	if _, ok := shape.Models["shared"]; !ok {
		t.Fatal("shared id must survive (first provider wins)")
	}
	// "shared" keeps the FIRST provider's display name.
	if dn, _ := shape.Models["shared"]["display_name"].(string); dn != "shared (first)" {
		t.Fatalf("shared display_name = %q; want the first provider's label", dn)
	}
	// The drop was logged with the id and a reason.
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "shared") || !strings.Contains(joined, "dropped") {
		t.Fatalf("expected a logged drop reason naming the id; got %q", joined)
	}
	// The enum set has exactly 2 distinct values (no duplicate enum).
	enums := map[float64]bool{}
	for _, e := range shape.Models {
		enums[e["model"].(float64)] = true
	}
	if len(enums) != 2 {
		t.Fatalf("distinct enums = %d; want 2", len(enums))
	}
}

// --- deny-list fail-closed --------------------------------------------------

func TestDenyList_StaticIDAndPresetName(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"static gemini id", "gemini-3.8-flash-high"},
		{"static gemini pro id", "gemini-3.1-pro-low"},
		{"preset provider name", "deepseek"},
		{"preset alias", "gemini"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := &providers.Registry{Providers: []providers.Provider{{
				Name:   "prov",
				Models: []providers.Model{enabled(tc.id, 300000)},
			}}}
			_, err := BuildFetchAvailableModels(reg, CatalogOptions{})
			if err == nil {
				t.Fatalf("id %q must be refused by the deny-list", tc.id)
			}
			if !strings.Contains(err.Error(), tc.id) {
				t.Fatalf("error must name the id %q; got %v", tc.id, err)
			}
		})
	}
}

func TestDenyList_CaseInsensitive(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "prov",
		Models: []providers.Model{enabled("DeepSeek", 300000)},
	}}}
	if _, err := BuildFetchAvailableModels(reg, CatalogOptions{}); err == nil {
		t.Fatal("a case variant of a preset name must still be refused")
	}
}

func TestDenyList_BenignIDNotRefused(t *testing.T) {
	// "glm-5.3-flash-flex" contains the alias "glm" as a prefix but is not equal
	// to it, so it must be served.
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "bigmodel",
		Models: []providers.Model{enabled("glm-5.3-flash-flex", 300000)},
	}}}
	if _, err := BuildFetchAvailableModels(reg, CatalogOptions{}); err != nil {
		t.Fatalf("a benign id must not be refused: %v", err)
	}
}

// --- minimal shape ----------------------------------------------------------

func TestMinimalShape_OnlyModelsAndSorts_EnumsAreNumbers(t *testing.T) {
	data := mustBuild(t, twoProviderRegistry(), CatalogOptions{ReservedEnums: DefaultReservedEnums()})

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("top-level keys = %d (%v); want exactly models + agent_model_sorts", len(top), keysOf(top))
	}
	for _, k := range []string{"models", "agent_model_sorts"} {
		if _, ok := top[k]; !ok {
			t.Fatalf("missing required key %q", k)
		}
	}
	for _, forbidden := range []string{"default_agent_model_id", "tiered_model_ids", "command_model_ids"} {
		if _, ok := top[forbidden]; ok {
			t.Fatalf("payload must not carry %q", forbidden)
		}
	}
	// Every enum is a JSON number, not a quoted string.
	shape := parseCatalog(t, data)
	for id, entry := range shape.Models {
		if _, ok := entry["model"].(float64); !ok {
			t.Fatalf("model %q enum is not a JSON number", id)
		}
	}
}

func TestEmptyRegistry_MinimalEmptyCatalog(t *testing.T) {
	for _, reg := range []*providers.Registry{nil, {Providers: []providers.Provider{{Name: "p", Models: []providers.Model{{ID: "x", Enabled: boolPtr(false)}}}}}} {
		data := mustBuild(t, reg, CatalogOptions{})
		if got := strings.ReplaceAll(string(data), " ", ""); got != `{"models":{},"agent_model_sorts":[]}` {
			t.Fatalf("empty catalog = %s; want the minimal empty shape", data)
		}
	}
}

// --- resolver ---------------------------------------------------------------

func TestResolver_ExactTerminalNotFound(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "prov", Models: []providers.Model{
			enabled("probe-gemini", 300000),
			enabled("probe-evergreen", 300000),
			enabled("probe-blade", 300000),
			{ID: "probe-disabled", Enabled: boolPtr(false)},
		},
	}}}
	resolve := RegistryResolver(reg)

	cases := []struct {
		name   string
		wantID string
		wantOK bool
	}{
		{"probe-gemini", "probe-gemini", true},                                       // exact
		{"127.0.0.1:8791/models/probe-gemini", "probe-gemini", true},                 // /models/ terminal
		{"evergreen:127.0.0.1:8791/models/probe-evergreen", "probe-evergreen", true}, // evergreen: scheme
		{"blade:127.0.0.1:8791/models/probe-blade", "probe-blade", true},             // blade: scheme
		{"gemini-api://127.0.0.1:8817/models/probe-gemini", "probe-gemini", true},    // gemini-api:// scheme
		{"prov/probe-gemini", "probe-gemini", true},                                  // bare "/" terminal
		{"probe-unknown", "", false},                                                 // not found
		{"", "", false},                                                              // empty
		{"probe-disabled", "", false},                                                // disabled: not resolved
	}
	for _, tc := range cases {
		target, ok, err := resolve(tc.name)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.name, err)
		}
		if ok != tc.wantOK {
			t.Fatalf("%q: ok = %v; want %v", tc.name, ok, tc.wantOK)
		}
		if ok && target.Model.ID != tc.wantID {
			t.Fatalf("%q: resolved %q; want %q", tc.name, target.Model.ID, tc.wantID)
		}
	}

	// A nil registry resolves nothing.
	if _, ok, _ := RegistryResolver(nil)("probe-gemini"); ok {
		t.Fatal("a nil registry must resolve nothing")
	}
}

// --- profile parsing + precedence ------------------------------------------

func TestParseProfile(t *testing.T) {
	cases := []struct {
		in   string
		want Profile
		ok   bool
	}{
		{"p1", ProfileP1, true}, {"picker", ProfileP1, true}, {"1", ProfileP1, true}, {"P1", ProfileP1, true},
		{"p2", ProfileP2, true}, {"subagent", ProfileP2, true}, {"2", ProfileP2, true},
		{"off", ProfileNone, true}, {"none", ProfileNone, true}, {"", ProfileNone, true},
		{"garbage", ProfileNone, false}, {"p3", ProfileNone, false},
	}
	for _, tc := range cases {
		got, err := ParseProfile(tc.in)
		if tc.ok && err != nil {
			t.Fatalf("ParseProfile(%q) unexpected error %v", tc.in, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("ParseProfile(%q) expected an error", tc.in)
		}
		if got != tc.want {
			t.Fatalf("ParseProfile(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveProfile_Precedence(t *testing.T) {
	env := func(v string) func(string) string {
		return func(string) string { return v }
	}
	cases := []struct {
		name   string
		env    string
		state  string
		getenv func(string) string
		want   Profile
	}{
		{"env p1 beats state p2", "p1", "p2", env("p1"), ProfileP1},
		{"env off beats state p1", "off", "p1", env("off"), ProfileNone},
		{"env p2 beats state none", "p2", "none", env("p2"), ProfileP2},
		{"empty env falls to state", "", "p2", env(""), ProfileP2},
		{"unset env falls to state", "", "p1", env(""), ProfileP1},
		{"invalid env ignored, state wins", "typo", "p2", env("typo"), ProfileP2},
		{"nil getenv uses state", "", "p1", nil, ProfileP1},
		{"nil getenv + none state", "", "none", nil, ProfileNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveProfile(tc.getenv, tc.state); got != tc.want {
				t.Fatalf("ResolveProfile(env=%q, state=%q) = %q; want %q", tc.env, tc.state, got, tc.want)
			}
		})
	}
}

func TestProfileLabelDescriptionWirecheckName(t *testing.T) {
	if ProfileP1.WirecheckName() != "P1" || ProfileP2.WirecheckName() != "P2" || ProfileNone.WirecheckName() != "" {
		t.Fatal("WirecheckName mapping is wrong")
	}
	if ProfileP1.Label() == "" || ProfileP2.Label() == "" || ProfileNone.Label() == "" {
		t.Fatal("every profile needs a non-empty label")
	}
	if !strings.Contains(ProfileP1.Description(), "no subagent") {
		t.Fatalf("P1 cost line must state the no-subagent cost: %q", ProfileP1.Description())
	}
	if !strings.Contains(ProfileP2.Description(), "subscription Gemini off") {
		t.Fatalf("P2 cost line must state the subscription-off cost: %q", ProfileP2.Description())
	}
}

// --- helpers ----------------------------------------------------------------

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the P1 catalog's drop reasons quote the
// registry provider NAME and model ID. Both are registry slots, so each
// renders through providers.MaskSlot (the registry-slot authority) before the
// sink sees it; benign values keep their exact bytes.
// ---------------------------------------------------------------------------

func TestCatalogDropReasonsMaskRegistrySlots(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)
	collect := func() (*strings.Builder, CatalogOptions) {
		var log strings.Builder
		opts := DefaultCatalogOptions()
		opts.Logf = func(format string, args ...any) {
			log.WriteString(fmt.Sprintf(format, args...))
			log.WriteString("\n")
		}
		return &log, opts
	}

	// A blank id (the provider name is the operand) and a cross-provider
	// duplicate (the id is the operand).
	var log strings.Builder
	opts := DefaultCatalogOptions()
	opts.Logf = func(format string, args ...any) {
		log.WriteString(fmt.Sprintf(format, args...))
		log.WriteString("\n")
	}
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: plant, BaseURL: "https://plant.example/v1", Models: []providers.Model{
			{ID: plant, ContextLength: 32768},
			{ID: "", ContextLength: 32768},
		}},
		{Name: "acme", BaseURL: "https://acme.example/v1", Models: []providers.Model{
			{ID: plant, ContextLength: 32768},
		}},
	}}
	if _, err := BuildFetchAvailableModels(reg, opts); err != nil {
		t.Fatalf("BuildFetchAvailableModels: %v", err)
	}
	lines := log.String()
	if strings.Contains(lines, plant) {
		t.Errorf("catalog drop reason leaks a registry slot raw:\n%s", lines)
	}
	if want := `hybrid catalog: dropped a model from provider "` + plantMask + `": blank id`; !strings.Contains(lines, want) {
		t.Errorf("blank-id drop reason must mask the provider name as %q:\n%s", plantMask, lines)
	}
	if want := `dropped model "` + plantMask + `" from provider "acme": id already listed by an earlier provider`; !strings.Contains(lines, want) {
		t.Errorf("duplicate-id drop reason must mask the model id as %q:\n%s", plantMask, lines)
	}

	// Benign corpus: both operands keep their exact bytes.
	blog, bopts := collect()
	benign := &providers.Registry{Providers: []providers.Provider{
		{Name: "nw", BaseURL: "https://api.neuralwatt.example/v1", Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", ContextLength: 32768},
		}},
		{Name: "acme-corp", BaseURL: "https://api.acme-corp.example/v1", Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", ContextLength: 32768},
			{ID: "", ContextLength: 32768},
		}},
	}}
	if _, err := BuildFetchAvailableModels(benign, bopts); err != nil {
		t.Fatalf("BuildFetchAvailableModels(benign): %v", err)
	}
	for _, want := range []string{
		`hybrid catalog: dropped a model from provider "acme-corp": blank id`,
		`dropped model "glm-5.3-flash-flex" from provider "acme-corp": id already listed by an earlier provider`,
	} {
		if !strings.Contains(blog.String(), want) {
			t.Errorf("benign drop reason lost its exact bytes (%q):\n%s", want, blog.String())
		}
	}
	if strings.Contains(blog.String(), "****") {
		t.Errorf("a benign drop reason was masked:\n%s", blog.String())
	}
}
