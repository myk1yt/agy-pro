package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// q6Catalog is the live-verified plain `agy models` output (E4/Q6 fixture,
// qa-baseline-report.md Q6): one banner line then 18 id<TAB>display rows.
const q6Catalog = "Fetching available models...\n" +
	"gemini-3.8-flash-high\tGemini 3.8 Flash (High)\n" +
	"gemini-3.8-flash-medium\tGemini 3.8 Flash (Medium)\n" +
	"gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n" +
	"gemini-3.7-flash-high\tGemini 3.7 Flash (High)\n" +
	"gemini-3.7-flash-medium\tGemini 3.7 Flash (Medium)\n" +
	"gemini-3.7-flash-low\tGemini 3.7 Flash (Low)\n" +
	"gemini-3.6-flash-high\tGemini 3.6 Flash (High)\n" +
	"gemini-3.6-flash-medium\tGemini 3.6 Flash (Medium)\n" +
	"gemini-3.6-flash-low\tGemini 3.6 Flash (Low)\n" +
	"gemini-3.1-pro-high\tGemini 3.1 Pro (High)\n" +
	"gemini-3.1-pro-low\tGemini 3.1 Pro (Low)\n" +
	"claude-opus-5-5-low\tClaude Opus 5.5 (Low)\n" +
	"claude-opus-5-5-medium\tClaude Opus 5.5 (Medium)\n" +
	"claude-opus-5-5-high\tClaude Opus 5.5 (High)\n" +
	"claude-sonnet-5-5-low\tClaude Sonnet 5.5 (Low)\n" +
	"claude-sonnet-5-5-medium\tClaude Sonnet 5.5 (Medium)\n" +
	"claude-sonnet-5-5-high\tClaude Sonnet 5.5 (High)\n" +
	"gpt-oss-120b-medium\tGPT-OSS 120B (Medium)\n"

// q7GatewayCatalog is the gateway-contaminated `agy models` output (Q7): when
// AGY_LLM_GATEWAY_MODELS is visible to the child, the native catalog is
// replaced by this 8-entry gateway list. The fetcher's scrubbed child env is
// what keeps this fixture from ever appearing in B1-1.
const q7GatewayCatalog = "Fetching available models...\n" +
	"glm-5.3-flash-flex (NW)\tglm-5.3-flash-flex (NW)\n" +
	"glm-5.3-flash-flex\tglm-5.3-flash-flex\n" +
	"glm-5.3-flex (NW)\tglm-5.3-flex (NW)\n" +
	"glm-5.3-flex\tglm-5.3-flex\n" +
	"glm-5.3-flash (Charm)\tglm-5.3-flash (Charm)\n" +
	"glm-5.3-flash\tglm-5.3-flash\n" +
	"qwen3.8-flash (Charm)\tqwen3.8-flash (Charm)\n" +
	"qwen3.8-flash\tqwen3.8-flash\n"

// stubFetcher is an in-memory NativeFetcher with a call counter.
type stubFetcher struct {
	models []NativeModel
	err    error
	calls  int
	delay  time.Duration
}

func (s *stubFetcher) Fetch(ctx context.Context) ([]NativeModel, error) {
	s.calls++
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.models, s.err
}

func q6Models() []NativeModel {
	return parseModelsOutput(q6Catalog)
}

func TestParseModelsOutputQ6Fixture(t *testing.T) {
	models := parseModelsOutput(q6Catalog)
	if len(models) != 18 {
		t.Fatalf("Q6 fixture parsed %d rows, want 18", len(models))
	}
	if models[0].ID != "gemini-3.8-flash-high" || models[0].DisplayName != "Gemini 3.8 Flash (High)" {
		t.Errorf("first row = %+v, want gemini-3.8-flash-high / Gemini 3.8 Flash (High)", models[0])
	}
	if models[17].ID != "gpt-oss-120b-medium" {
		t.Errorf("last row id = %q, want gpt-oss-120b-medium", models[17].ID)
	}
	// Banner line "Fetching available models..." must not become a model row.
	for _, m := range models {
		if strings.Contains(m.ID, "Fetching") {
			t.Fatalf("banner line leaked into catalog: %+v", m)
		}
	}
}

func TestParseModelsOutputQ7GatewayFixture(t *testing.T) {
	models := parseModelsOutput(q7GatewayCatalog)
	if len(models) != 8 {
		t.Fatalf("Q7 fixture parsed %d rows, want 8", len(models))
	}
	if models[0].ID != "glm-5.3-flash-flex (NW)" {
		t.Errorf("first gateway row = %q", models[0].ID)
	}
}

func TestParseModelsOutputEdgeCases(t *testing.T) {
	if got := parseModelsOutput(""); len(got) != 0 {
		t.Errorf("empty output parsed %d rows, want 0", len(got))
	}
	// Lines without TAB are ignored; empty display falls back to id.
	got := parseModelsOutput("banner only\nid1\thello\nid2\t\nplain\n")
	if len(got) != 2 {
		t.Fatalf("parsed %d rows, want 2: %+v", len(got), got)
	}
	if got[0].DisplayName != "hello" || got[1].DisplayName != "id2" {
		t.Errorf("display fallback wrong: %+v", got)
	}
}

func TestLoadDedupeNativeAndCrossKind(t *testing.T) {
	// Same native id twice collapses; a provider model with the same id as a
	// native model stays BOTH (different seats: free quota vs API cost).
	native := []NativeModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
		{ID: "gemini-3.8-flash-high", DisplayName: "dup"},
	}
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "google",
		Models: []providers.Model{
			{ID: "gemini-3.8-flash-high"},
			{ID: "gemini-2.5-flash"},
		},
	}}}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: native})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (1 native + 2 provider): %+v", len(inv.Entries), inv.Entries)
	}
	if inv.Entries[0].Kind != "native" || inv.Entries[0].DisplayName != "Gemini 3.8 Flash (High)" {
		t.Errorf("native dup kept the wrong row: %+v", inv.Entries[0])
	}
	if inv.Entries[1].Spec != "google/gemini-3.8-flash-high" {
		t.Errorf("cross-kind provider row missing: %+v", inv.Entries[1])
	}
	// Numbers are 1-based and stable in render order.
	for i, e := range inv.Entries {
		if e.Number != i+1 {
			t.Errorf("entry %d has Number %d", i, e.Number)
		}
	}
}

func TestLoadDisabledModelsExcludedAndKeyMissing(t *testing.T) {
	disabled := false
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		APIKeyEnv: "NW_KEY_NOT_SET_IN_TESTS",
		Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Capabilities: providers.Capabilities{SupportsTools: true, SupportsThinking: true}},
			{ID: "off-model", Enabled: &disabled},
		},
	}}}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: q6Models()})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range inv.Entries {
		if e.ModelID == "off-model" {
			t.Fatalf("disabled model leaked into inventory: %+v", e)
		}
	}
	if len(inv.Entries) != 19 {
		t.Fatalf("entries = %d, want 19 (18 native + 1 provider)", len(inv.Entries))
	}
	last := inv.Entries[18]
	if !last.KeyMissing {
		t.Errorf("provider with unset key env not flagged: %+v", last)
	}
	if last.Group != "nw" || last.Spec != "nw/glm-5.3-flash-flex" {
		t.Errorf("provider row shape wrong: %+v", last)
	}
	if last.Capabilities != "tools, thinking(default)" {
		t.Errorf("capabilities = %q, want tools, thinking(default)", last.Capabilities)
	}
	// A provider that needs no key is never flagged.
	reg2 := &providers.Registry{Providers: []providers.Provider{{
		Name: "local", Models: []providers.Model{{ID: "m1"}},
	}}}
	inv2, _ := Load(context.Background(), reg2, nil)
	if inv2.Entries[0].KeyMissing {
		t.Errorf("keyless provider flagged as KeyMissing: %+v", inv2.Entries[0])
	}
}

func TestLoadNativeFetchFailureDegradesHonest(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash"}},
	}}}
	inv, err := Load(context.Background(), reg, &stubFetcher{err: errors.New("boom")})
	if err != nil {
		t.Fatalf("Load must not fail on native-fetch error: %v", err)
	}
	if inv.NativeErr == nil || !strings.Contains(inv.NativeErr.Error(), "boom") {
		t.Fatalf("NativeErr = %v, want boom", inv.NativeErr)
	}
	if len(inv.Entries) != 1 || inv.Entries[0].Kind != "provider" {
		t.Fatalf("external rows must survive native degrade: %+v", inv.Entries)
	}
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	if !strings.Contains(buf.String(), "[google]") {
		t.Errorf("render lost provider rows:\n%s", buf.String())
	}
}

func TestGroupsOrderNativeFirstThenRegistryOrder(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flex"}}},
		{Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash"}}},
	}}
	inv, _ := Load(context.Background(), reg, &stubFetcher{models: q6Models()})
	groups := inv.Groups()
	want := []string{"native", "nw", "google"}
	if len(groups) != len(want) {
		t.Fatalf("groups = %d, want %d", len(groups), len(want))
	}
	for i, g := range groups {
		if g.Name != want[i] {
			t.Errorf("group[%d] = %q, want %q", i, g.Name, want[i])
		}
	}
	if len(groups[0].Entries) != 18 {
		t.Errorf("native group has %d entries, want 18", len(groups[0].Entries))
	}
}

func TestRenderNumberedFormat(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash", Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true}}}},
		{Name: "nw", APIKeyEnv: "NW_KEY_NOT_SET_IN_TESTS", Models: []providers.Model{{ID: "glm-5.3-flash-flex", Capabilities: providers.Capabilities{SupportsTools: true, SupportsThinking: true}}}},
	}}
	inv, _ := Load(context.Background(), reg, &stubFetcher{models: q6Models()[:2]})
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	out := buf.String()
	t.Logf("render:\n%s", out)
	if !strings.Contains(out, "[ 1] gemini-3.8-flash-high") {
		t.Errorf("missing padded number / native id row:\n%s", out)
	}
	if !strings.Contains(out, "Gemini 3.8 Flash (High)") {
		t.Errorf("native row must show display name:\n%s", out)
	}
	if !strings.Contains(out, "tools, vision") || !strings.Contains(out, "[google]") {
		t.Errorf("provider row must show capability column and group tag:\n%s", out)
	}
	if !strings.Contains(out, "tools, thinking(default)") || !strings.Contains(out, "[nw]") {
		t.Errorf("nw row missing capabilities/tag:\n%s", out)
	}
	if !strings.Contains(out, "(key missing)") {
		t.Errorf("KeyMissing marker missing:\n%s", out)
	}
	// Global numbering across groups: 2 native rows then provider rows at 3,4.
	if !strings.Contains(out, "[ 3] gemini-2.5-flash") {
		t.Errorf("numbering not global across groups:\n%s", out)
	}
}

// C2: Entry.Family carries the CASE-PRESERVING explicit registry family so
// the models --json display sink can mask a key-shaped family through the
// uppercase-requiring decorated class (the folded FamilyName() rode a
// lowercase-shaped secret raw). Rotation re-folds (entryFamily), so fanout
// alignment is unchanged; id-inferred rows are unaffected.
func TestLoadCarriesCasePreservedFamily(t *testing.T) {
	// A separator-carrying mixed-case family: the contiguous-run rule cannot
	// see it (the ":" breaks the run), so only the case-preserved decorated
	// class catches it. This is the exact C2 leak shape.
	keyFamily := "Native:" + testfixtures.AWSSecretExample
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "acme", Models: []providers.Model{
			{ID: "m1", Family: "Gemini"},
			{ID: "m2", Family: keyFamily},
			{ID: "m3"},
		},
	}}}
	inv, err := Load(context.Background(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Entries[0].Family != "Gemini" {
		t.Errorf("Entry.Family = %q; want the case-preserved explicit family %q", inv.Entries[0].Family, "Gemini")
	}
	if inv.Entries[2].Family != "m" {
		t.Errorf("id-inferred family = %q; want m (providers.InferFamily)", inv.Entries[2].Family)
	}
	// Rotation folds the carried family: a mixed-case family still collides
	// with its lowercase twin, so fanout alignment is unchanged.
	if got := entryFamily(inv.Entries[0]); got != "gemini" {
		t.Errorf("entryFamily(case-preserved family) = %q; want the folded gemini", got)
	}
	// The models --json sink masks Entry.Family through MaskRegistryValue
	// (the registry-slot authority); the case-preserved feed is what lets the
	// decorated class catch it (the folded form rode raw).
	masked := providers.MaskOperand(inv.Entries[1].Family)
	if masked != "Nativ****" {
		t.Errorf("MaskOperand(key-shaped family) = %q; want Nativ**** (C2 display fix)", masked)
	}
	if strings.Contains(masked, "wJalrXUtnFEMI") {
		t.Errorf("masked family still carries the secret core: %q", masked)
	}
	// A benign family survives the same seam readable.
	if got := providers.MaskOperand(inv.Entries[0].Family); got != "Gemini" {
		t.Errorf("MaskOperand(benign family) = %q; want unchanged", got)
	}
}

// S5: RenderNumbered masks the Group (provider name) and the native
// DisplayName column through the registry-slot masker; the benign model ID of
// the same row stays readable (the pinned format test above keeps its
// fixtures).
func TestRenderNumberedMasksKeyShapedDisplayFields(t *testing.T) {
	keyGroup := testfixtures.AKIAExample
	keyDisplay := testfixtures.AWSSecretExample
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   keyGroup,
		Models: []providers.Model{{ID: "gemini-2.5-flash"}},
	}}}
	native := []NativeModel{{ID: "gpt-oss-120b-medium", DisplayName: keyDisplay}}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: native})
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	out := buf.String()
	if strings.Contains(out, keyGroup) || strings.Contains(out, keyDisplay) {
		t.Errorf("render leaks a key-shaped Group/DisplayName:\n%s", out)
	}
	if !strings.Contains(out, "[AKIAI****]") {
		t.Errorf("key-shaped provider name (Group) not masked:\n%s", out)
	}
	if !strings.Contains(out, "wJalr****") {
		t.Errorf("key-shaped native DisplayName not masked:\n%s", out)
	}
	if !strings.Contains(out, "gemini-2.5-flash") {
		t.Errorf("benign model ID must stay readable:\n%s", out)
	}
}

// K1/K5 — the provider capability column embeds the registry-persisted
// reasoningEffort (Capabilities.Summary composes thinking(<effort>) and
// nothing on the add-json / hand-edit write path validates that field), so
// RenderNumbered masks it; the column widths are computed from the MASKED
// strings so every rendered cell stays aligned with its column.
func TestRenderNumbered_MasksCapabilityColumnAndAlignsMaskedWidths(t *testing.T) {
	keyEffort := testfixtures.AWSSecretExample
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "google",
		Models: []providers.Model{
			{ID: "gemini-2.5-flash", Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true}},
			{ID: keyEffort, Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true, SupportsThinking: true, ReasoningEffort: keyEffort}},
		},
	}}}
	inv, err := Load(context.Background(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	out := buf.String()
	if strings.Contains(out, keyEffort) {
		t.Errorf("render leaks the key-shaped reasoningEffort:\n%s", out)
	}
	if !strings.Contains(out, "thinking(wJalr****)") {
		t.Errorf("capability column not masked:\n%s", out)
	}
	// Widths from the masked strings: idW = 16 (len "gemini-2.5-flash"),
	// midW = 34 (len "tools, vision, thinking(wJalr****)"). With raw-width
	// computation the benign row would pad to the raw 40-rune key id and the
	// columns would not line up with the rendered (masked) cells.
	wantLine1 := fmt.Sprintf("[ 1] %-16s  %-34s  [google]", "gemini-2.5-flash", "tools, vision")
	wantLine2 := fmt.Sprintf("[ 2] %-16s  %-34s  [google]", "wJalr****", "tools, vision, thinking(wJalr****)")
	for _, want := range []string{wantLine1, wantLine2} {
		if !strings.Contains(out, want) {
			t.Errorf("row not aligned to the masked column widths:\nwant %q\ngot:\n%s", want, out)
		}
	}
}

// Round-3 LOW: the capability-column mask (maskMidColumn's registry-slot
// mask) is defense in depth on top of the composition mask inside
// Capabilities.Summary. A separator-carrying key effort is already masked at
// the composition, so the sink mask is redundant for it and reverting the sink
// does not fail the pin above. This fixture discriminates the SINK: an
// IDENTIFIER-SEGMENTED plant (>=4 short segments) evades the composition's
// display class, but the composed 1-space summary "tools, thinking(<plant>)"
// becomes secret-shaped once the "thinking(" prefix merges the first segment
// past the 16-rune identifier floor, so ONLY the RenderNumbered sink mask
// catches it. Dropping the sink mask leaks the plant raw.
func TestRenderNumbered_MaskMidColumnSinkDiscriminatesComposite(t *testing.T) {
	plant := "abcdefgh.IJKL.mnop.QRSTUVWX"
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "google",
		Models: []providers.Model{
			{ID: "gemini-2.5-flash", Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true}},
			{ID: "m2", Capabilities: providers.Capabilities{SupportsTools: true, SupportsThinking: true, ReasoningEffort: plant}},
		},
	}}}
	inv, err := Load(context.Background(), reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The composition leaves the plant raw (the identifier-segmented class
	// evades the display mask); only the RenderNumbered sink masks it. If a
	// future composition change starts masking this shape, the fixture needs a
	// new plant to keep discriminating the sink.
	if got := inv.Entries[1].Capabilities; !strings.Contains(got, plant) {
		t.Fatalf("fixture no longer exercises the composition gap (plant masked at composition): %q", got)
	}
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	out := buf.String()
	if strings.Contains(out, plant) {
		t.Errorf("sink mask reverted: the plant leaks raw:\n%s", out)
	}
	if !strings.Contains(out, "tools****") {
		t.Errorf("sink must mask the 1-space composite to tools****:\n%s", out)
	}
	// The benign row stays readable.
	if !strings.Contains(out, "tools, vision") {
		t.Errorf("benign capability row must stay readable:\n%s", out)
	}
}

// TestRenderNumberedMasksLookAlikeRegistrySlot pins the listing conversion: the
// model ID and the mid column are registry-derived display slots, so they route
// through providers.MaskSlot. The plant is the discriminating shape for the
// pre-policy masker: providers.MaskOperand is identity on it (the ASCII-marker
// location exemption covers the "./" forms and the look-alike rune breaks the
// contiguous run), so the pre-conversion sink printed the credential core raw
// in `agy pro models --all` while the models --json twin masked the same record.
func TestRenderNumberedMasksLookAlikeRegistrySlot(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	const masked = "ab3de****"
	plant := "\u2025/" + core
	if got := providers.MaskOperand(plant); got != plant {
		t.Fatalf("premise broken: MaskOperand is not identity on the plant (%q)", got)
	}

	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:   "nw",
		Models: []providers.Model{{ID: plant}, {ID: "glm-5.3-flash-flex"}},
	}}}
	native := []NativeModel{
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"},
		{ID: "gpt-oss-120b-medium", DisplayName: plant},
	}
	inv, err := Load(context.Background(), reg, &stubFetcher{models: native})
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	inv.RenderNumbered(&buf)
	out := buf.String()
	if strings.Contains(out, core) || strings.Contains(out, plant) {
		t.Errorf("numbered listing leaks the look-alike plant raw:\n%s", out)
	}
	if strings.Count(out, masked) != 2 {
		t.Errorf("numbered listing must mask the id column and the mid column to %q:\n%s", masked, out)
	}
	if !strings.Contains(out, "glm-5.3-flash-flex") || !strings.Contains(out, "Gemini 3.8 Flash (High)") {
		t.Errorf("benign rows must stay readable:\n%s", out)
	}
}

func TestIsK3ExcludedDelegationTable(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"kimi-k3", true},
		{"kimi-k3-flex", true},
		{"k3-agent-swarm", true},
		{"vendor-prefixed-k3", true},
		{"K3", true},
		{"kimi-k2", false},
		{"k3000", false},
		{"glm-k3x", false},
		{"glm-4.6", false},
		{"gemini-3.8-flash-high", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsK3Excluded(tc.id); got != tc.want {
			t.Errorf("IsK3Excluded(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
