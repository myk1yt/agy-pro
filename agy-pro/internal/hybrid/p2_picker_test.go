package hybrid

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/agysettings"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// R5B-FIX-2 — the native fallback must not receive the mesh-specific pin
//
// The launcher appends `--model <id>` BEFORE LaunchP2's wirecheck gate, so every
// fallback path (NOGO, !ProfileOK, zero externals, an unreadable registry) can
// carry a pin native agy cannot answer. Forwarding it made a session that used
// to fall back cleanly hard-fail at agy's --model gate with exit 1.
// ---------------------------------------------------------------------------

// TestLaunchP2_NativeFallbackStripsThePinnedExternal is the end-to-end shape the
// reviewer described: profile p2, a bare interactive pick that pinned an
// external id, and a stub real agy whose wirecheck is NOGO. The fallback must
// exec WITHOUT any --model, and the exit code must be the child's own.
func TestLaunchP2_NativeFallbackStripsThePinnedExternal(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	// NOGO for P2: the gate falls back to a true native session.
	swapWirecheck(t, p2NogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		// The shape the launcher produces after a picker pick.
		Args:     p2SelfHelperArgsModel("ext-nw"),
		ChildEnv: childEnv,
		Stderr:   &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil) - the fallback must run the native child\nstderr: %s", code, err, errw.String())
	}
	argv := readArgsFile(t, argsFile)
	if hasArg(argv, "--model") {
		t.Errorf("the native fallback must not forward the mesh pin; argv = %v", argv)
	}
	if got := argValue(argv, "--app_data_dir"); got != "" {
		t.Errorf("the native fallback must not inject --app_data_dir (no per-session settings are written); got %q", got)
	}
	if !strings.Contains(errw.String(), "falling back to native agy") {
		t.Errorf("the fallback notice is missing:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "is not resolvable on this native fallback") {
		t.Errorf("the dropped pin must be disclosed, never silent:\n%s", errw.String())
	}
}

// TestLaunchP2_NativeFallbackKeepsANativeServablePin is the other half: a pin
// native agy CAN resolve from its own static catalog survives the fallback, so a
// chosen model is never silently swapped for a different one.
func TestLaunchP2_NativeFallbackKeepsANativeServablePin(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, staticOnlyRegistryJSON)
	swapWirecheck(t, p2NogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel("gemini-3.8-flash-high"),
		ChildEnv:    childEnv,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if got := argValue(readArgsFile(t, argsFile), "--model"); got != "gemini-3.8-flash-high" {
		t.Errorf("--model = %q; want the static id kept (native agy resolves it itself)", got)
	}
}

// TestLaunchP2_NativeFallbackDroppedPinIsMasked proves the disclosure line goes
// through providers.MaskOperand, so a key-shaped id in the registry cannot ride
// the notice.
func TestLaunchP2_NativeFallbackDroppedPinIsMasked(t *testing.T) {
	sandboxP2(t)
	// A key-shaped registry id: the pin is stripped (it is not a static id) and
	// the disclosure must mask it.
	const keyShaped = "sk-antapi03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provshape","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"SHAPE_API_KEY","models":[{"id":%q}]}]}`, keyShaped))
	swapWirecheck(t, p2NogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel(keyShaped),
		ChildEnv:    childEnv,
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if hasArg(readArgsFile(t, argsFile), "--model") {
		t.Error("the key-shaped pin must be stripped from the native fallback argv")
	}
	if strings.Contains(errw.String(), keyShaped) {
		t.Errorf("the dropped-pin disclosure must mask a key-shaped id:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "is not resolvable on this native fallback") {
		t.Errorf("the disclosure line is missing:\n%s", errw.String())
	}
}

// R5BF2-1: the dropped-pin disclosure strips CONTROL CHARACTERS the way every
// sibling sink does (p2DropSink, renderFallbackNotice, StaticIDTarget).
// MaskOperand masks secret-shaped operands only, so without the strip a crafted
// registry id or argv token carrying ESC/CR could repaint the terminal or forge
// a "[agy-pro]" line into this very disclosure.
func TestLaunchP2_NativeFallbackDroppedPinStripsControlChars(t *testing.T) {
	sandboxP2(t)
	// ESC[2J ESC[H then a forged line, then a CR that rewinds over it. No
	// whitespace, so nothing in the registry validation or the customModels key
	// rules objects to the id itself.
	const evil = "x\x1b[2J\x1b[H[agy-pro] session verified\r"
	writeRegistry(t, fmt.Sprintf(`{"providers":[{"name":"provctl","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"CTL_API_KEY","models":[{"id":%q}]}]}`, evil))
	swapWirecheck(t, p2NogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel(evil),
		ChildEnv:    childEnv,
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if hasArg(readArgsFile(t, argsFile), "--model") {
		t.Error("the control-bearing pin must be stripped from the native fallback argv")
	}
	out := errw.String()
	for _, banned := range []string{"\x1b", "\r"} {
		if strings.Contains(out, banned) {
			t.Errorf("the disclosure must not emit the raw control byte %q:\n%q", banned, out)
		}
	}
	if !strings.Contains(out, "is not resolvable on this native fallback") {
		t.Errorf("the disclosure line is missing:\n%s", out)
	}
	// The masked id still reaches the line (the drop stays disclosed, not silent).
	if !strings.Contains(out, "x") {
		t.Errorf("the disclosure should still name the (stripped) pin:\n%s", out)
	}
}

// R5BF2-2: a DECORATED static spelling is kept but forwarded as the BARE static
// id, so "kept" means "native-resolvable" instead of "forwarded verbatim".
func TestLaunchP2_NativeFallbackNormalizesAStaticPinToBareID(t *testing.T) {
	sandboxP2(t)
	writeRegistry(t, twoExtRegistryJSON)
	swapWirecheck(t, p2NogoReport())

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childEnv, _, argsFile := p2HelperChildEnv(t)
	var errw bytes.Buffer
	code, err := LaunchP2(LaunchOptions{
		RealAgyPath: self,
		Args:        p2SelfHelperArgsModel("nw/gemini-3.8-flash-high"),
		ChildEnv:    childEnv,
		Stderr:      &errw,
	})
	if err != nil || code != 0 {
		t.Fatalf("LaunchP2 = (%d, %v); want (0, nil)", code, err)
	}
	if got := argValue(readArgsFile(t, argsFile), "--model"); got != "gemini-3.8-flash-high" {
		t.Errorf("--model = %q; want the BARE static id (a decorated spelling is not natively resolvable)", got)
	}
	// A kept-and-normalized pin is not a drop: no disclosure line may print.
	if strings.Contains(errw.String(), "is not resolvable on this native fallback") {
		t.Errorf("a normalized static pin is still the user's choice, so nothing may be disclosed as dropped:\n%s", errw.String())
	}
}

// ---------------------------------------------------------------------------
// R5B-FIX-3 — the picker must only offer rows that resolve
// ---------------------------------------------------------------------------

// p2PickerRegistryJSON is the drift-guard fixture: a non-static native-shaped
// id, a static id, a cross-provider duplicate id (two providers, same id), a
// CASE-VARIANT duplicate id (two providers, same id in different case), a
// reserved-name id and a disabled model. Only some of these may be offered, and
// only the ones p2Externals actually keys. (A blank id cannot be planted here:
// providers.LoadFrom rejects it, which is its own fail-closed guarantee; the
// blank-id row is covered directly through the predicate.)
//
// R5BF2-3: the case-variant pair (Case-Dup / case-dup) is the row that pins the
// exact-case alignment between this filter's dedup and p2Externals'. p2Externals
// keys BOTH (its `seen[id]` is case-sensitive), so the filter must offer BOTH
// rows; a case-folded dedup would hide the second provider's servable row.
const p2PickerRegistryJSON = `{"providers":[
  {"name":"provfirst","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"FIRST_API_KEY","models":[
    {"id":"dup-id"},{"id":"gemini-3.8-flash-high"},{"id":"claude-opus-5-5-high"},
    {"id":"Case-Dup"},{"id":"off-disabled","enabled":false}]},
  {"name":"provsecond","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"SECOND_API_KEY","models":[
    {"id":"dup-id"},{"id":"only-second"},{"id":"case-dup"}]}
]}`

// TestP2PickerAllows_MirrorsTheSettingsDocument is the drift guard, stated as
// the two invariants that must both hold:
//
//	offered  => servable : every offered row's pin is a customModels key the
//	                        settings document actually carries;
//	servable => offered  : every customModels key has at least one offered row
//	                        that pins it (nothing servable is hidden).
//
// If p2Externals' policy and the picker's filter ever diverge, one of the two
// directions fails.
func TestP2PickerAllows_MirrorsTheSettingsDocument(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2PickerRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	allows := P2PickerAllows(reg)
	const meshURL = "http://127.0.0.1:4444"

	// The keys the session's settings document will actually carry.
	entries := p2Externals(reg, meshURL, nil)
	keys := map[string]bool{}
	for _, e := range entries {
		keys[e.URL] = true
	}
	if len(keys) == 0 {
		t.Fatal("fixture broken: the settings document carries no customModels entries")
	}

	// Direction 1: offered => servable.
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if !allows("provider", p.Name, m.ID) {
				continue
			}
			if pin := p2ModelKey(reg, m.ID, meshURL); !keys[pin] {
				t.Errorf("provider %q model %q is OFFERED but its pin %q is not a customModels key the settings document carries", p.Name, m.ID, pin)
			}
		}
	}

	// Direction 2: servable => offered. Every key needs at least one row whose
	// pin resolves to it, otherwise the picker hides something the session can
	// actually serve.
	for _, e := range entries {
		offered := false
		for _, p := range reg.Providers {
			for _, m := range p.Models {
				if !allows("provider", p.Name, m.ID) {
					continue
				}
				if p2ModelKey(reg, m.ID, meshURL) == e.URL {
					offered = true
				}
			}
		}
		if !offered {
			t.Errorf("customModels key %q is servable but no picker row pins it", e.URL)
		}
	}

	// Native rows: only the static Gemini ids.
	if !allows("native", "native", "gemini-3.8-flash-high") {
		t.Error("a static native row must be offered")
	}
	for _, id := range []string{"claude-opus-5-5-high", "gpt-oss-120b-medium", "native-a"} {
		if allows("native", "native", id) {
			t.Errorf("native row %q must NOT be offered (gemini mode never lists it)", id)
		}
	}
	// Case-insensitive static match.
	if !allows("native", "native", "GEMINI-3.8-FLASH-HIGH") {
		t.Error("the static-id match must be case-insensitive")
	}
	// An unknown row kind is never offered.
	if allows("mystery", "native", "gemini-3.8-flash-high") {
		t.Error("an unknown row kind must not be offered")
	}
	// A blank id is never offered.
	if allows("provider", "provfirst", "   ") {
		t.Error("a blank id must not be offered")
	}
	// The cross-provider duplicate: only the FIRST provider's row.
	if !allows("provider", "provfirst", "dup-id") {
		t.Error("the first provider's row for a duplicate id must be offered (it owns the key)")
	}
	if allows("provider", "provsecond", "dup-id") {
		t.Error("the second provider's row for a duplicate id must NOT be offered (it would bill the first provider)")
	}
	// A reserved-name external is not offered (p2Externals drops it).
	if allows("provider", "provfirst", "gemini-3.8-flash-high") {
		t.Error("a reserved static-id external must not be offered as a customModels row (it is served, if at all, by the static-id policy)")
	}
	// A second-provider-only id is offered under its own provider.
	if !allows("provider", "provsecond", "only-second") {
		t.Error("an id only the second provider carries must be offered under that provider")
	}
	// A disabled model is never offered.
	if allows("provider", "provfirst", "off-disabled") {
		t.Error("a disabled model must not be offered")
	}
	// R5BF2-3: the CASE-VARIANT duplicate. p2Externals' dedup is case-sensitive,
	// so it keys BOTH Case-Dup and case-dup as distinct customModels entries; the
	// filter must offer both rows (each under its own provider), or it hides a
	// servable model. A case-folded dedup fails this and direction 2 above.
	if !allows("provider", "provfirst", "Case-Dup") {
		t.Error("the first provider's case-variant row must be offered")
	}
	if !allows("provider", "provsecond", "case-dup") {
		t.Error("the second provider's case-variant row must be offered (p2Externals keys it too)")
	}
	// The owner check is EXACT, not EqualFold: a case-variant provider name must
	// not be able to claim another provider's row (the wrong-vendor class).
	if allows("provider", "PROVFIRST", "Case-Dup") {
		t.Error("a case-variant provider name must not claim another provider's row")
	}
	if allows("provider", "provsecond", "Case-Dup") {
		t.Error("a case-variant id under the wrong provider must not be offered")
	}
}

// TestP2PickerAllows_NilRegistryOffersNoProviderRows pins the nil-registry
// shape: native rows still filter to the static ids, provider rows are all
// rejected, and nothing panics.
func TestP2PickerAllows_NilRegistryOffersNoProviderRows(t *testing.T) {
	allows := P2PickerAllows(nil)
	if !allows("native", "native", "gemini-3.8-flash-high") {
		t.Error("a static native row must still be offered with a nil registry")
	}
	if allows("native", "native", "claude-opus-5-5-high") {
		t.Error("a non-static native row must not be offered with a nil registry")
	}
	if allows("provider", "any", "ext-nw") {
		t.Error("no provider row may be offered with a nil registry")
	}
}

// TestP2PickerAllows_MatchesAgysettingsStaticIDs pins the native allow-list to
// agysettings' own list, so the two cannot drift.
func TestP2PickerAllows_MatchesAgysettingsStaticIDs(t *testing.T) {
	allows := P2PickerAllows(nil)
	for _, id := range agysettings.StaticGeminiIDs() {
		if !allows("native", "native", id) {
			t.Errorf("static id %q must be offered", id)
		}
	}
	if len(agysettings.StaticGeminiIDs()) == 0 {
		t.Fatal("fixture broken: agysettings reports no static Gemini ids")
	}
}

// TestP2PickerInventoryFilteringCrossCheck is the launcher-side contract stated
// here as a package-level pin: the picker's row set and the settings document
// must agree. It re-derives the launcher's filter shape (kind/group/id triples)
// against the same policy the launcher applies, so a change to either side is
// visible in this package.
func TestP2PickerInventoryFilteringCrossCheck(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, p2PickerRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	allows := P2PickerAllows(reg)
	dir := t.TempDir()
	docPath := filepath.Join(dir, "settings.json")

	// What the session would serve.
	entries := p2Externals(reg, "http://127.0.0.1:4444", nil)
	if len(entries) == 0 {
		t.Fatal("fixture broken")
	}
	// Every offered provider row's pin must be one of those entries' keys.
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if !allows("provider", p.Name, m.ID) {
				continue
			}
			pin := p2ModelKey(reg, m.ID, "http://127.0.0.1:4444")
			found := false
			for _, e := range entries {
				if e.URL == pin {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("offered row %s/%s pins %q, which the settings document does not carry", p.Name, m.ID, pin)
			}
		}
	}
	// The document itself is only written on the real path; assert the shape the
	// composer would accept so the cross-check stays honest about the source.
	doc, err := agysettings.BuildP2Settings(entries)
	if err != nil {
		t.Fatalf("BuildP2Settings: %v", err)
	}
	if _, ok := doc["modelProvider"]; !ok {
		t.Error("the composed document must carry modelProvider")
	}
	_ = docPath
}

// ---------------------------------------------------------------------------
// R5BF2-6 — p2RegistryRawID pass-1: an exact raw id beats a display label,
// regardless of registry order
// ---------------------------------------------------------------------------

// labelCollisionRegistryJSON is the pathological shape R5BF2-6 addresses: a
// model whose RAW id is byte-equal to ANOTHER model's display label.
//
//	provA "alpha"        -> DisplayName "alpha (provA)"
//	provB "alpha (provA)" -> a raw id that IS provA's label
//
// Typing "alpha (provA)" is ambiguous by construction. The shim's rule is now
// deterministic: the exact raw id (provB's row) wins, because that string IS
// provB's model id, and the label form stays reachable by typing "alpha".
const labelCollisionRegistryJSON = `{"providers":[
  {"name":"provA","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"A_KEY","models":[{"id":"alpha"}]},
  {"name":"provB","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"B_KEY","models":[{"id":"alpha (provA)"}]}
]}`

// labelCollisionReversedJSON is the same two rows in the opposite provider
// order. The whole point of the fix is that the answer does NOT change.
const labelCollisionReversedJSON = `{"providers":[
  {"name":"provB","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"B_KEY","models":[{"id":"alpha (provA)"}]},
  {"name":"provA","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"A_KEY","models":[{"id":"alpha"}]}
]}`

// TestP2RegistryRawID_ExactIDBeatsLabelOrderIndependently pins R5BF2-6: the
// same input resolves to the SAME provider no matter which order the two rows
// appear in. Before the split the raw-id match and the label match competed
// inside one registry-order scan, so the input's meaning changed with the file.
func TestP2RegistryRawID_ExactIDBeatsLabelOrderIndependently(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry string
	}{
		{"label row second", labelCollisionRegistryJSON},
		{"label row first", labelCollisionReversedJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := providers.LoadFrom(mustWriteRegistry(t, tc.registry))
			if err != nil {
				t.Fatalf("load registry: %v", err)
			}
			// The ambiguous string: provB's exact raw id AND provA's label.
			got, ok := p2RegistryRawID(reg, "alpha (provA)", "alpha (provA)")
			if !ok {
				t.Fatal("p2RegistryRawID found no match for the ambiguous token")
			}
			if got != "alpha (provA)" {
				t.Errorf("p2RegistryRawID = %q; want the exact raw id %q (an exact id beats a label, in either order)", got, "alpha (provA)")
			}
			// The label form is still reachable by typing the bare id.
			if got, ok := p2RegistryRawID(reg, "alpha", "alpha"); !ok || got != "alpha" {
				t.Errorf("p2RegistryRawID(alpha) = (%q, %v); want (alpha, true)", got, ok)
			}
		})
	}
}

// TestP2RegistryRawID_LabelMatchStillWorks pins that splitting pass 1 did not
// lose the label convenience spelling the picker/CLI rely on.
func TestP2RegistryRawID_LabelMatchStillWorks(t *testing.T) {
	reg, err := providers.LoadFrom(mustWriteRegistry(t, twoExtRegistryJSON))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	// The "<id> (<provider>)" label form (no raw id equals it in this registry).
	if got, ok := p2RegistryRawID(reg, "ext-nw (provnw)", "ext-nw (provnw)"); !ok || got != "ext-nw" {
		t.Errorf("p2RegistryRawID(label) = (%q, %v); want (ext-nw, true)", got, ok)
	}
	// The provider-qualified form falls through to the candidate pass.
	if got, ok := p2RegistryRawID(reg, "ext-nw", "provnw/ext-nw"); !ok || got != "ext-nw" {
		t.Errorf("p2RegistryRawID(provnw/ext-nw) = (%q, %v); want (ext-nw, true)", got, ok)
	}
	// The exact raw id still wins.
	if got, ok := p2RegistryRawID(reg, "ext-nw", "ext-nw"); !ok || got != "ext-nw" {
		t.Errorf("p2RegistryRawID(ext-nw) = (%q, %v); want (ext-nw, true)", got, ok)
	}
}

// TestP2RegistryRawID_ExactIDBeatsEarlierCandidate pins the original R5AF2-2
// guarantee survives the three-pass split: an exact raw id anywhere in the
// registry beats an earlier entry that only matches the stripped candidate.
func TestP2RegistryRawID_ExactIDBeatsEarlierCandidate(t *testing.T) {
	// "gpt-4o" comes FIRST (a candidate match for "openai/gpt-4o"), and the
	// exact "openai/gpt-4o" comes later: the exact one must win.
	reg, err := providers.LoadFrom(mustWriteRegistry(t, `{"providers":[
  {"name":"provfirst","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"F_KEY","models":[{"id":"gpt-4o"}]},
  {"name":"provopenai","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"O_KEY","models":[{"id":"openai/gpt-4o"}]}
]}`))
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if got, ok := p2RegistryRawID(reg, "gpt-4o", "openai/gpt-4o"); !ok || got != "openai/gpt-4o" {
		t.Errorf("p2RegistryRawID(openai/gpt-4o) = (%q, %v); want (openai/gpt-4o, true) - the exact id must beat the earlier candidate", got, ok)
	}
}
