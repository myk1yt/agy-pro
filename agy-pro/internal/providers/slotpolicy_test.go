package providers

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// This file pins the registry-slot value policy (slotpolicy.go): the class the
// four earlier rounds each closed in one narrower form while the two enumerated
// lists drifted apart. Every refused entry is named for the class it isolates and
// carries a premise check, so a reverted fold, strip, or classifier fails the
// entry that covers it.

const slotTestOpaque = "ab3def7gh9jk1mnpqr5t"                  // 20 runes, lowercase, mixed classes
const slotTestAWS = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" // 40 runes, base64 "/" run

// TestSlotPolicyRefusesDecoratedClasses pins the refuse side: every decoration
// class the enumerated lists missed (or covered on only one side) reduces to a
// credential core. Each entry's premise is that the RAW shape test alone does
// not refuse it, so the entry isolates the policy arm; if the fold or the strip
// is reverted the entry turns red.
func TestSlotPolicyRefusesDecoratedClasses(t *testing.T) {
	cases := []struct{ name, value string }{
		{"one dot leader U+2024", "\u2024/" + slotTestOpaque},
		{"box drawing diagonal U+2571", "\u2571" + slotTestOpaque},
		{"box drawing diagonal U+2572", "\u2572" + slotTestOpaque},
		{"katakana middle dot U+30FB", "\u30FB" + slotTestOpaque},
		{"halfwidth katakana dot U+FF65", "\uFF65" + slotTestOpaque},
		{"set minus U+2216", "\u2216" + slotTestOpaque},
		{"big reverse solidus U+29F9", "\u29F9" + slotTestOpaque},
		{"interior variation selector U+FE0F", "ab3de\uFE0Ff7gh9jk1mnpqr5t"},
		{"interior combining mark U+0301", "ab3de\u0301f7gh9jk1mnpqr5t"},
		{"interior zero width space", "ab3def7\u200bgh9jk1mnpqr5t"},
		{"interior fullwidth solidus", "ab3def7\uFF0Fgh9jk1mnpqr5t"},
		{"fullwidth colon drive", "C\uFF1A\uFF0F" + slotTestOpaque},
		{"fullwidth drive letter and colon", "\uFF3A\uFF1A/" + slotTestOpaque},
		{"vertical comma colon look-alike", "\uFE10" + slotTestOpaque},
		{"bare drive backslash", `C:\` + slotTestAWS},
		{"bare drive slash", `c:/` + slotTestAWS},
		{"drive relative colon", `C:` + slotTestOpaque},
		{"relative then drive", `./C:\` + slotTestAWS},
		{"stacked drive", `C:\C:\` + slotTestAWS},
		{"leading separator then drive", `\C:\` + slotTestAWS},
		{"scheme then drive", `file:///C:/` + slotTestAWS},
		{"stacked mixed markers", "\u2025/\uFF0F" + slotTestOpaque},
		{"drive plus look-alike", `C:\` + "\u2025/" + slotTestAWS},
		{"bidi override inside marker", "./\u202E" + slotTestAWS},
		{"bidi isolate and zero width", "\u2067akia\u200bosfodnn7example\u2069"},
		{"byte order mark", "\uFEFFAKIAIOSFODNN7EXAMPLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if slotShapeLooksSecret(tc.value) {
				t.Fatalf("premise broken: %q is refused by the raw shape test alone, so it no longer isolates the policy arm", tc.value)
			}
			if !SecretShaped(tc.value) {
				t.Fatalf("SecretShaped(%q) = false; want true (decorated credential)", tc.value)
			}
			// The same value must not survive the display authority either.
			if got := MaskSlot(tc.value); got == StripSlotControls(tc.value) {
				t.Fatalf("MaskSlot(%q) = %q (identity); want a mask", tc.value, got)
			}
		})
	}

	// The undecorated credential shapes stay refused by the shape test itself.
	for _, v := range []string{slotTestOpaque, "AKIAIOSFODNN7EXAMPLE", slotTestAWS, "Ab3dEf7Gh9jK1mNp+/Qr=", "sk-abc", "bearer abc"} {
		if !SecretShaped(v) {
			t.Errorf("SecretShaped(%q) = false; want true (credential shape)", v)
		}
	}
}

// TestSlotPolicyFoldsNewSeparatorMembers pins the class members this round
// added: the two solidus forms that render as a slash and a backslash but sit
// outside every category rule the fold reads, and the NON-ASCII space class
// (Zs), which the fold admits while it keeps the ASCII space outside the class
// by design. The membership assertions come first so a reverted class rule
// fails on the predicate rather than on one value.
func TestSlotPolicyFoldsNewSeparatorMembers(t *testing.T) {
	if !isSlotSeparatorRune('\u29F6') || !isSlotSeparatorRune('\u29F7') {
		t.Fatal("U+29F6 SOLIDUS WITH OVERBAR and U+29F7 REVERSE SOLIDUS WITH HORIZONTAL STROKE must be separator-class members")
	}
	for _, r := range []rune{'\u00A0', '\u3000', '\u202F', '\u2007'} {
		if !isSlotSeparatorRune(r) {
			t.Errorf("U+%04X (Zs) must be a separator-class member", r)
		}
	}
	// ASCII parity: the ASCII space is not a member, so its ASCII twin keeps
	// the documented visible-split acceptance and no ASCII-only value can start
	// being stripped as a leading marker.
	if isSlotSeparatorRune(' ') {
		t.Error("the ASCII space must stay outside the separator class (ASCII-parity trade)")
	}

	cases := []struct{ name, value string }{
		{"solidus with overbar U+29F6", "\u29F6" + slotTestOpaque},
		{"reverse solidus with stroke U+29F7", "\u29F7" + slotTestOpaque},
		{"interior no-break space U+00A0", "ab3def7\u00A0gh9jk1mnpqr5t"},
		{"interior ideographic space U+3000", "ab3def7\u3000gh9jk1mnpqr5t"},
		{"interior narrow no-break space U+202F", "ab3def7\u202Fgh9jk1mnpqr5t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if slotShapeLooksSecret(tc.value) {
				t.Fatalf("premise broken: %q is refused by the raw shape test alone, so it no longer isolates the class rule", tc.value)
			}
			if !SecretShaped(tc.value) {
				t.Fatalf("SecretShaped(%q) = false; want true (folded separator look-alike)", tc.value)
			}
			if got := MaskSlot(tc.value); got != "ab3de****" {
				t.Fatalf("MaskSlot(%q) = %q; want ab3de****", tc.value, got)
			}
		})
	}

	// Class-based, not an entry list: a Zs member nobody named (U+2007 FIGURE
	// SPACE) reduces exactly like the two named space members.
	if !SecretShaped("ab3def7\u2007gh9jk1mnpqr5t") {
		t.Error("an unnamed Zs member inside the core must fold into the run too")
	}
}

// TestSlotPolicyAcceptsBenignCorpus pins the accept side, the over-refusal
// guard for this policy: the project's own pinned legitimate shapes must still
// pass SecretShaped, and the shared masker must never mask a benign value
// beyond what the pre-policy authority did (MaskRegistryValue over the control
// strip). A value the policy refuses here is an over-refusal of a value the
// registry legitimately stores.
func TestSlotPolicyAcceptsBenignCorpus(t *testing.T) {
	benign := []string{
		"anthropic/claude-3.5-sonnet",
		"meta-llama/Llama-3.3-70B-Instruct-Turbo", // 39 runes, separators: the mission's pinned id
		"Qwen/Qwen2.5-72B-Instruct-Turbo",
		"meta-llama/llama-3.3-70b-instruct",
		"claude-3-5-sonnet-20241022",
		"gemini-3-pro-high",
		"gpt-4.1-mini",
		"acme-fast",
		"abcdefghijklmnop",           // 16 runes but a single-class word
		"abcdefghijklmnopqrstuvwxyz", // 26 runes but a single-class word
		"1234567890123456",           // 16 runes but digits only
		"read_file",
		"read_url_content",
		"list_dir",
		"high",
		"beta, fast",
		"beta, fast, tools",
		"./models/gemini-pro", // benign relative model path (leading "./")
		"./models/anthropic/claude-3.5-sonnet",
		`.\models\gemini`,
		`C:\models\gemini-pro`, // benign Windows path
		`C:\models\anthropic\claude-3.5-sonnet`,
		`c:/models/gemini-pro`,
		"https://example.com/v1",
		"https://api.example.com/v1/completions", // long benign URL: the fold must not join it into one run
		"see https://example.com/docs for details",
		"\u2025models/gemini-pro", // look-alike dot leader ahead of a benign path
		"\u2026acme",
		"\u2025notes",
		"beta\u00A0fast",   // interior no-break space: a folded separator, but the pieces are short
		"acme\u3000fast",   // interior ideographic space, same shape
		"\u3000gemini-pro", // leading ideographic space: trimmed by the authority, not a credential
	}
	for _, v := range benign {
		if SecretShaped(v) {
			t.Errorf("SecretShaped(%q) = true; want false (legitimate registry value)", v)
		}
		if got, want := MaskSlot(v), MaskRegistryValue(StripSlotControls(v)); got != want {
			t.Errorf("MaskSlot(%q) = %q; want the authority's %q (the policy fallback must not fire on a benign value)", v, got, want)
		}
	}
}

// TestMaskSlotDisplayAuthority pins the render side of the policy: a
// look-alike-decorated, drive-decorated, or invisible-decorated plant renders
// as the deterministic first-5+**** mask, a benign slot renders byte-identically,
// and an empty slot stays empty (the omitempty fields of a serialized record).
func TestMaskSlotDisplayAuthority(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"look-alike marker and slash", "\u2025/" + slotTestOpaque, "ab3de****"},
		{"drive prefix and AWS secret", `C:\` + slotTestAWS, "wJalr****"},
		{"nested drive prefix", `./C:\` + slotTestAWS, "wJalr****"},
		{"interior look-alike separator", "ab3def7\uFF0Fgh9jk1mnpqr5t", "ab3de****"},
		{"interior variation selector", "ab3de\uFE0Ff7gh9jk1mnpqr5t", "ab3de****"},
		{"leading separator run", "\uFF0F\u2025" + slotTestOpaque, "ab3de****"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskSlot(tc.value)
			if got != tc.want {
				t.Fatalf("MaskSlot(%q) = %q; want %q", tc.value, got, tc.want)
			}
			if strings.Contains(got, slotTestOpaque) || strings.Contains(got, slotTestAWS) {
				t.Fatalf("MaskSlot(%q) = %q leaks the credential core", tc.value, got)
			}
		})
	}

	// Byte-identity for benign slots: the diff and the transcript stay stable.
	for _, v := range []string{"", "high", "meta-llama/Llama-3.3-70B-Instruct-Turbo", "beta, fast"} {
		if got := MaskSlot(v); got != v {
			t.Fatalf("MaskSlot(%q) = %q; want byte-identity", v, got)
		}
	}

	// A Cf-decorated value is stripped before the mask, so the rendered operand
	// never carries the format rune that could reorder a frame's text.
	cf := "./\u202E" + slotTestAWS
	masked := MaskSlot(cf)
	if masked == cf {
		t.Fatalf("MaskSlot(%q) = identity; want a mask", cf)
	}
	if strings.ContainsFunc(masked, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		t.Fatalf("MaskSlot(%q) = %q still carries a format rune", cf, masked)
	}

	// Composed label: the status banner and the model inventory render
	// "<id> (<provider>)" as ONE string, so the credential core sits behind an
	// annotation. The whole-value classifier never sees it (the annotation
	// breaks the run) and the pre-policy maskers left it readable, so this is
	// the display arm that pins the label case.
	label := "\u2025/" + slotTestOpaque + " (acme)"
	if got := MaskSlot(label); got != "ab3de****" {
		t.Fatalf("MaskSlot(%q) = %q; want ab3de**** (composed label)", label, got)
	}
	if got := MaskSlot("native:" + "\u2025/" + slotTestOpaque); got == "native:"+"\u2025/"+slotTestOpaque {
		t.Fatalf("MaskSlot(native: plant) = identity; want a mask")
	}
	// A benign composed label stays byte-identical.
	benignLabel := "meta-llama/Llama-3.3-70B-Instruct-Turbo (acme)"
	if got := MaskSlot(benignLabel); got != benignLabel {
		t.Fatalf("MaskSlot(%q) = %q; want byte-identity", benignLabel, got)
	}
}

// TestMaskSlotAuthorityRewriteMasked pins the order defect the review swarm
// isolated: MaskRegistryValue changes a value in two ways and only one of them
// masks. It renders the marker-stripped core of a location-shaped value, so a
// plant that still wears an ASCII marker ahead of a look-alike one, or that
// carries an interior combining mark behind the ASCII marker, came back as a
// CHANGED but readable core. The early return accepted it and the policy
// fallback (the only classifier that reads look-alike classes) never ran.
//
// Each case's premise is that the authority CHANGES the value while its
// rewrite still carries the credential core: a revert of the fallback returns
// that rewrite and the assertion fails. The last block pins the composed label
// at the display name the converted banner and models --json sinks feed to
// MaskSlot, so the display-only segment arm is pinned where it is used.
func TestMaskSlotAuthorityRewriteMasked(t *testing.T) {
	const core = slotTestOpaque
	const masked = "ab3de****"

	cases := []struct{ name, value string }{
		{"ascii dot-slash ahead of a look-alike marker", "./" + "\u2025/" + core},
		{"leading space, dot-slash, look-alike marker", " ./" + "\u2025/" + core},
		{"drive ahead of a look-alike marker", `C:\` + "\u2025/" + core},
		{"dot-slash ahead of an interior combining mark", "./ab3de\u0301f7gh9jk1mnpqr5t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := MaskRegistryValue(tc.value)
			if out == tc.value {
				t.Fatalf("premise broken: the authority returned %q unchanged, so the case no longer exercises a rewrite", tc.value)
			}
			if !strings.Contains(foldSeparators(out), core) {
				t.Fatalf("premise broken: the authority's rewrite %q no longer carries the credential core", out)
			}
			if !SecretShaped(tc.value) {
				t.Errorf("SecretShaped(%q) = false; want true (both ingest doors refuse the shape)", tc.value)
			}
			got := MaskSlot(tc.value)
			if got != masked {
				t.Errorf("MaskSlot(%q) = %q; want %q", tc.value, got, masked)
			}
			if strings.Contains(got, core) {
				t.Errorf("MaskSlot(%q) = %q leaks the credential core", tc.value, got)
			}
		})
	}

	// The composed label: the banner id column (cmd/agy/main.go) and the
	// models --json DisplayName field both render providers.DisplayName(id,
	// provider) through MaskSlot, and the authority strips the ASCII "./"
	// before its whole-value classifiers see the trailing annotation.
	label := DisplayName("./"+core, "nw")
	if label != "./"+core+" (nw)" {
		t.Fatalf("DisplayName composition moved: %q", label)
	}
	if out := MaskRegistryValue(label); out == label || !strings.Contains(foldSeparators(out), core) {
		t.Fatalf("premise broken: the authority no longer rewrites the composed label into a readable core (%q)", out)
	}
	if got := MaskSlot(label); got != masked {
		t.Errorf("MaskSlot(%q) = %q; want %q (composed label at the converted sinks)", label, got, masked)
	}
	// A benign composed label assembled the same way is untouched by the new
	// arm: the fallback declines and the authority's rewrite stands.
	benign := DisplayName("./models/gemini-pro", "acme")
	if got, want := MaskSlot(benign), MaskRegistryValue(benign); got != want {
		t.Errorf("MaskSlot(%q) = %q; want the authority's %q (no masking on a benign rewrite)", benign, got, want)
	}
}

// TestSlotPolicyStripTerminates pins the fixpoint argument mechanically: the
// strip is idempotent on every plant (a second pass changes nothing), so the
// loop cannot be re-entered with fresh decoration, and an all-markers value
// terminates with an empty core instead of looping.
func TestSlotPolicyStripTerminates(t *testing.T) {
	values := []string{
		`.//C:\C:\` + slotTestAWS,
		"file:///./\u2025/\uFF0F" + slotTestOpaque,
		`\\./../C:/` + slotTestAWS,
		strings.Repeat("/", 64) + slotTestOpaque,
		strings.Repeat("\u2025", 32),
		strings.Repeat(`\`, 32),
	}
	for _, v := range values {
		once := stripSlotMarkers(foldSeparators(v))
		twice := stripSlotMarkers(foldSeparators(once))
		if once != twice {
			t.Fatalf("strip is not idempotent:\nonce  = %q\ntwice = %q\ninput = %q", once, twice, v)
		}
	}
	if got := stripSlotMarkers(strings.Repeat("\u2025/", 40)); got != "" {
		t.Fatalf("an all-markers value stripped to %q; want the empty core", got)
	}
}

// TestNameCarriesCredentialIsTheUnion pins the shared ingest authority the
// sibling doors adopt (providers.NameCarriesCredential). The five underscore
// probes are the class the first two arms miss; the accept corpus is the
// project's legal provider-name set, whose verdicts must not move, and the
// pre-existing refusals are the shapes the arms already caught. Every verdict
// is recomputed from the pre-fix expression on each row, so the table is a live
// before/after comparison rather than a restatement of the union.
func TestNameCarriesCredentialIsTheUnion(t *testing.T) {
	probes := []string{
		"sk_live_0123456789abcdef",
		"tok_a1b2c3d4e5f6g7h8i9j0",
		"pk_live_0123456789abcdef",
		"key_0123456789abcdef",
		"AKIA_0123456789ABCDEF",
	}
	for _, p := range probes {
		// The premise: both pre-fix arms are the identity, the slot authority is
		// not, and the name charset admits the value (so a name door would have
		// persisted it raw).
		if SecretShaped(p) || MaskOperand(p) != p {
			t.Fatalf("premise broken: %q is already refused by the pre-fix arms", p)
		}
		if MaskSlot(p) == p {
			t.Fatalf("premise broken: %q is identity under the slot authority", p)
		}
		if err := ValidateProviderName(p); err != nil {
			t.Fatalf("premise broken: %q no longer passes the name charset: %v", p, err)
		}
		if !NameCarriesCredential(p) {
			t.Fatalf("NameCarriesCredential(%q) = false; want true (underscore-head key)", p)
		}
	}

	// Legal records: the union must not refuse anything the pre-fix arms accept.
	// "key_backup" and "sk_staging" are the two names a prefix list would have
	// refused ("key_", "sk_"), so they pin the union's shape against that
	// regression.
	accept := []string{
		"openai", "gemini", "deepseek", "anthropic",
		"key_backup", "sk_staging",
		"mistral", "groq", "openrouter", "together", "fireworks", "moonshot",
		"zhipu", "ollama", "lmstudio", "vllm", "litellm",
		"acme-new", "acme2", "my-provider", "my_provider", "internal-gateway",
		"eu-east-prod", "beta-fast", "fast-tier-2", "azure-eastus",
		"bedrock-us-east-1", "vertex-gemini", "qwen-local", "llama-local",
		"gateway-eu", "team-alpha-gateway", "backup-2024", "sandbox",
		"gateway", "local-llm", "corp.v2", "deepseek_v3", "kimi_sandbox",
		"openai_azure", "my.gateway.eu",
	}
	for _, v := range accept {
		if SecretShaped(v) || MaskOperand(v) != v {
			t.Fatalf("premise broken: %q is refused by a pre-fix arm", v)
		}
		if NameCarriesCredential(v) {
			t.Errorf("NameCarriesCredential(%q) = true; want false (legal provider name)", v)
		}
		if err := ValidateProviderName(v); err != nil {
			t.Fatalf("%q is no longer a legal provider name: %v", v, err)
		}
	}

	// Pre-existing refusals: the union must keep them refused, and it must not
	// widen beyond them (the accept table above covers the other direction).
	refused := []string{
		"openai2024backup",           // 16-rune opaque run
		"npm_registry",               // shared prefix head
		"qwen2point5turbo",           // 16-rune opaque run
		"eu-west1.fastproxy.example", // dotted id the operand masker refuses
		"provider0123456789",         // 19-rune opaque run
		"sk-proj-0123456789abcdef",   // "sk-" head
	}
	for _, v := range refused {
		if !SecretShaped(v) && MaskOperand(v) == v {
			t.Fatalf("premise broken: %q is not refused by a pre-fix arm", v)
		}
		if !NameCarriesCredential(v) {
			t.Errorf("NameCarriesCredential(%q) = false; want true (pre-existing refusal)", v)
		}
	}
}

// TestValidateRejectsInvisibleFormatRunes pins the load-time widening: a
// disk-borne format rune (Cf: bidi override, zero-width space, BOM) in any
// model free-text field fails Validate with the established field-class
// sentence and never echoes the value, so an invisible decoration cannot
// persist at rest either. A combining mark (Mn) is NOT refused: an
// NFD-encoded name legitimately carries one, and the policy folds it at
// classification and display time.
func TestValidateRejectsInvisibleFormatRunes(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		mutate func(*Registry, string)
	}{
		{"model id", "model id", func(r *Registry, cf string) { r.Providers[0].Models[0].ID = "m" + cf + "1" }},
		{"family", "model family", func(r *Registry, cf string) { r.Providers[0].Models[0].Family = "fam" + cf + "x" }},
		{"tag", "model tag", func(r *Registry, cf string) { r.Providers[0].Models[0].Tags[0] = "tag" + cf }},
		{"tool grant", "model tool name", func(r *Registry, cf string) {
			r.Providers[0].Models[0].Capabilities.AllowedTools = []string{"read", "tool" + cf}
		}},
		{"reasoning effort", "reasoningEffort", func(r *Registry, cf string) {
			r.Providers[0].Models[0].Capabilities.ReasoningEffort = "high" + cf
		}},
	}
	cfs := []string{"\u202E", "\u200B", "\u200D", "\uFEFF", "\u2067"}
	for _, tc := range cases {
		for _, cf := range cfs {
			t.Run(fmt.Sprintf("%s/U+%04X", tc.name, []rune(cf)[0]), func(t *testing.T) {
				reg := validControlTestRegistry()
				tc.mutate(&reg, cf)
				err := reg.Validate()
				if err == nil {
					t.Fatalf("%s with format rune U+%04X: Validate returned nil", tc.name, []rune(cf)[0])
				}
				if !strings.Contains(err.Error(), "control characters") {
					t.Errorf("%s: error %q must keep the established control-character wording", tc.name, err)
				}
				if !strings.Contains(err.Error(), tc.field) {
					t.Errorf("%s: error %q must name the field class %q", tc.name, err, tc.field)
				}
				if strings.Contains(err.Error(), cf) {
					t.Errorf("%s: error echoes the raw format rune: %q", tc.name, err)
				}
			})
		}
	}

	// A clean registry still validates, and a combining mark is not a refusal:
	// an NFD-encoded name is a legitimate family and id.
	reg := validControlTestRegistry()
	reg.Providers[0].Models[0].Family = "cafe\u0301"
	reg.Providers[0].Models[0].ID = "cafe\u0301-model"
	if err := reg.Validate(); err != nil {
		t.Fatalf("an NFD-encoded family must validate: %v", err)
	}
}

// TestSlotPolicySingleImplementation is the drift pin: it fails if any display
// sink reintroduces an enumerated marker list, keeps a local strip, or bypasses
// the shared policy. It reads every converted display witness as source text
// and checks the SHAPE of their code, not their behavior, because the failure
// mode this closes is a plausible-looking local implementation that diverges
// only for the look-alike nobody has found yet.
func TestSlotPolicySingleImplementation(t *testing.T) {
	twins := []string{
		filepath.Join("..", "tui", "screen_model_form.go"),
		filepath.Join("..", "..", "cmd", "agy", "pro_settings.go"),
	}
	displayFiles := append([]string{
		filepath.Join("..", "..", "cmd", "agy", "pro_models.go"),
		filepath.Join("..", "..", "cmd", "agy", "main.go"),
		// The TUI model screen carries a dozen converted call sites; a local
		// masker or a bare-authority bypass reintroduced there would otherwise
		// stay green (the behavioral pin only covers the planted shape).
		filepath.Join("..", "tui", "screen_models.go"),
		// The listing sinks converted with the shared masker: the screen-local
		// URL masker's last-resort arm and the two inventory listings.
		filepath.Join("..", "tui", "screen_providers.go"),
		filepath.Join("..", "inventory", "inventory.go"),
		filepath.Join("..", "swarm", "builder.go"),
	}, twins...)

	// shapeLiterals are strings that can only appear in an enumerated
	// credential-head list; a derived view (providers.SlotSecretPrefixes) has
	// none of them.
	shapeLiterals := []string{`"gsk_`, `"glpat-`, `"github_pat_`, `"dckr_pat-`, `"npm_"`, `"eyj"`}

	for _, name := range displayFiles {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		for _, lit := range shapeLiterals {
			if strings.Contains(src, lit) {
				t.Errorf("%s: credential-head literal %s is back; the list lives only in providers.slotSecretPrefixes", name, lit)
			}
		}
		if strings.Contains(src, "map[rune]rune") {
			t.Errorf("%s: a rune alias map is back; the separator class is category-based in providers", name)
		}
		if strings.Contains(src, "providers.MaskRegistryValue(") {
			t.Errorf("%s: a display site bypasses the single authority (providers.MaskRegistryValue); route it through providers.MaskSlot", name)
		}
	}

	// The twin ingest guards and slot maskers must be one-statement
	// delegations, so no local classification or local strip can hide behind
	// the same name. FIX-7A routed the TUI model form's model-ID ingest guard
	// through the shared NAME authority (providers.NameCarriesCredential);
	// FIX-7E moved the CLI model verbs' guard (twins[1]) to the same
	// authority, so both rows now name the union instead of the narrower
	// classifier they were written against. The pin follows the one-authority
	// rule it exists to protect: a door that keeps SecretShaped alone here
	// reopens the underscore-head class on that side.
	delegations := []struct{ file, fn, want string }{
		{twins[0], "looksSecretValue", "providers.NameCarriesCredential(val)"},
		{twins[0], "maskSlotOperand", "providers.MaskSlot(v)"},
		{twins[0], "stripDiffControls", "providers.StripSlotControls(s)"},
		{twins[1], "modelSpecValueLooksSecret", "providers.NameCarriesCredential(v)"},
		{twins[1], "regOperand", "providers.MaskSlot(v)"},
		{twins[1], "stripDiffControls", "providers.StripSlotControls(s)"},
	}
	for _, d := range delegations {
		body, err := funcBody(d.file, d.fn)
		if err != nil {
			t.Fatalf("%s: %v", d.file, err)
		}
		if len(body) != 1 {
			t.Fatalf("%s: %s has %d statements; want exactly the shared-policy delegation (a local implementation is back):\n%s",
				d.file, d.fn, len(body), strings.Join(body, "\n"))
		}
		if !strings.Contains(body[0], d.want) {
			t.Fatalf("%s: %s body = %q; want the shared-policy call %q", d.file, d.fn, body[0], d.want)
		}
	}
}

// funcBody returns the statements of one function declaration in path, printed
// back through go/format so the comparison is insensitive to whitespace.
func funcBody(path, name string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		body := make([]string, 0, len(fd.Body.List))
		for _, stmt := range fd.Body.List {
			var sb strings.Builder
			if err := format.Node(&sb, fset, stmt); err != nil {
				return nil, err
			}
			if text := strings.TrimSpace(sb.String()); text != "" {
				body = append(body, text)
			}
		}
		return body, nil
	}
	return nil, fmt.Errorf("function %s not found in %s", name, path)
}

// ---------------------------------------------------------------------------
// FIX-8: the ONE shared URL ingest authority (providers.BaseURLCarriesCredential)
// ---------------------------------------------------------------------------

// baseURLAuthorityCorpus is the shared URL corpus (synthetic values only; the
// live providers.json and state.json are never touched). Every URL slot is
// represented on both sides: the benign shapes the three doors must keep
// accepting (ordinary gateways, the Cloudflare 32-hex account-id path, the two
// long gateway hostnames the reviewers probe-verified, a benign userinfo pair,
// identifier parts) and one credential shape per slot (host head, host
// decoration, host JWT, userinfo username and password, path segment, Opaque
// spelling, query key, query value, fragment).
var baseURLAuthorityCorpus = []struct {
	name   string
	url    string
	refuse bool
}{
	// Benign shapes.
	{"ordinary openai", "https://api.openai.com/v1", false},
	{"ordinary openrouter", "https://openrouter.ai/api/v1", false},
	{"ordinary google", "https://generativelanguage.googleapis.com/v1beta", false},
	{"plain gateway", "https://gw.example/v1", false},
	{"chat path", "https://gw.example/v1/chat/completions", false},
	{"scheme only", "https://", false},
	{"scheme only hostless", "https:", false},
	{"hostless gateway", "https:gw.example", false},
	{"loopback", "http://127.0.0.1:8080/v1", false},
	{"benign userinfo", "https://user:pw@gw.example/v1", false},
	{"benign userinfo username", "https://user@api.example.com/v1", false},
	{"cloudflare account id", "https://gateway.ai.cloudflare.com/v1/0123456789abcdef0123456789abcdef/anthropic", false},
	{"long gateway host a", "https://mycompany2024gateway.example.com/v1", false},
	{"long gateway host b", "https://acmeprod2024gateway.example.com/v1", false},
	{"benign query", "https://gw.example/v1?api-version=2024-02-01", false},
	{"benign fragment", "https://gw.example/v1#section-2", false},
	{"benign hyphen path", "https://gw.example/v1/beta-fast", false},
	{"uuid host label", "https://550e8400-e29b-41a7-a716-446655440000.example.com/v1", false},
	{"compound host label", "https://aci-gw-2024-production.us-east-1.example.com/v1", false},
	{"single class long label", "https://verylonggatewaylabelname.example.com/v1", false},
	// The documented bare-run trade: a host label that is one uninterrupted run
	// stays with the whole-value arms (see baseURLHostLabelCarriesCredential).
	// This row is refused today by the stripped-core classifier, and it must stay
	// refused so the host rule is not an accept-side regression.
	{"bare run host", "https://0123456789abcdef/v1", true},
	// HOST slot (FIX-8f).
	{"host underscore head", "https://sk_live_0123456789abcdef/v1", true},
	{"host tok head", "https://tok_a1b2c3d4e5f6g7h8i9j0/v1", true},
	{"host ghp head", "https://ghp_a1b2c3d4e5f6g7h8i9j0.example.com/v1", true},
	{"host aws key id", "https://AKIAIOSFODNN7EXAMPLE/v1", true},
	{"host jwt", "https://eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U/v1", true},
	{"host decorated", "https://1-ASIAIOSFODNN7EXAMPLE.example.com/v1", true},
	// Userinfo slot.
	{"userinfo username", "https://sk_live_0123456789abcdef@host/v1", true},
	{"userinfo password", "https://user:sk_live_0123456789abcdef@host/v1", true},
	// Path slot, hierarchical and Opaque.
	{"path underscore head", "https://host/v1/sk_live_0123456789abcdef", true},
	{"path 16 run", "https://host/v1/0123456789abcdef", true},
	{"path jwt", "https://host/v1/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", true},
	{"opaque path", "https:sk_live_0123456789abcdef", true},
	{"path sk head", "https://gw.example/v1/sk-live-0123456789abcdefXYZ", true},
	{"path nvapi head", "https://gw.example/v1/nvapi-0123456789abcdef", true},
	// Query and fragment slots.
	{"query value", "https://host/v1?api_key=sk_live_0123456789abcdef", true},
	{"query key", "https://host/v1?sk_live_0123456789abcdef=1", true},
	{"fragment", "https://host/v1#sk_live_0123456789abcdef", true},
	// FIX-8h H-1: a query pair whose percent-escape does not decode. url.Query()
	// unwraps the decode error and drops the pair, so these three rows are the
	// regression pins for the raw-pair read: the credential rides the pair that
	// the decoded view cannot see.
	{"query malformed value", "https://gw.example/v1?api_key=sk_live_0123456789abcdef%2", true},
	{"query malformed key", "https://gw.example/v1?sk_live_0123456789abcdef=1%2", true},
	{"query malformed second pair", "https://gw.example/v1?api-version=2024-02-01&api_key=sk_live_0123456789abcdef%2", true},
	// FIX-8h H-1 accept side: escapes that are odd but legal (or that decode to a
	// benign value) keep their verdict, so the raw read narrowed nothing.
	{"query odd escape pct25", "https://gw.example/v1?q=100%25", false},
	{"query odd escape bare pct", "https://gw.example/v1?q=100%", false},
	{"query odd escape comma", "https://gw.example/v1?filter=a%2Cb", false},
	{"query odd escape space", "https://gw.example/v1?q=hello%20world", false},
	{"query malformed benign", "https://gw.example/v1?api-version=2024-02-01%2", false},
	// FIX-8h H-1 unchanged row: a well-formed encoded credential is still read
	// decoded, exactly as url.Query() read it.
	{"query encoded key", "https://gw.example/v1?api_key=sk_live%5F0123456789abcdef", true},
	// FIX-8h H-3: an invisible rune inside the host label. url.Parse keeps it in
	// Hostname and no transport rule rejects it, so the label sits in the one
	// slot the display strip never reached.
	{"host zwsp", "https://sk_live_0123456789ab\u200bcdef/v1", true},
	{"host zwj", "https://sk_live_0123456789ab\u200dcdef/v1", true},
	{"host bidi override", "https://sk_live_0123456789ab\u202ecdef/v1", true},
	{"host bom", "https://sk_live_0123456789ab\ufeffcdef/v1", true},
	{"host combining mark", "https://sk_live_0123456789ab\u0301cdef/v1", true},
	// FIX-8h H-3 accept side: the strip is the identity without an invisible
	// rune, so a benign host keeps its verdict (and the pinned long gateway
	// hosts above stay accepted).
	{"host benign zwsp", "https://mycompany2024\u200bgateway.example.com/v1", false},
	// FIX-8h H-2: a value url.Parse cannot read is REFUSED, not left to a
	// transport rule that echoed the operand into every consumer.
	{"unparseable path escape", "https://gw.example/v1/sk_live_0123456789abcdef%2", true},
	{"unparseable path escape benign", "https://gw.example/%zz", true},
	{"unparseable port", "https://gw.example:sk_live_0123456789abcdef/v1", true},
	{"unparseable fragment escape", "https://gw.example/v1#sk_live_0123456789abcdef%2", true},
	{"unparseable userinfo escape", "https://sk_live_0123456789abcdef%2@gw.example/v1", true},
	// fix8-7: a credential run split by a separator-SPACE rune (Zs, Zl, Zp) in the
	// host label. url.Parse keeps the rune in Hostname, no transport rule rejects
	// it, and the host-label strip carried the four invisible classes only, so
	// every row below was accepted at all three doors and persisted. The class is
	// read by stripHostLabelInvisibles, and the whole-value arms stay the identity
	// on these rows (TestBaseURLHostLabelSpaceSeparatorStrip recomputes that
	// premise), which is what made the host arm the only arm that could decide it.
	{"host nbsp mid run", "https://sk_live_0123456789ab\u00a0cdef/v1", true},
	{"host ideographic space mid run", "https://sk_live_0123456789ab\u3000cdef/v1", true},
	{"host figure space mid run", "https://sk_live_0123456789ab\u2007cdef/v1", true},
	{"host narrow nbsp mid run", "https://sk_live_0123456789ab\u202fcdef/v1", true},
	{"host line separator mid run", "https://sk_live_0123456789ab\u2028cdef/v1", true},
	{"host paragraph separator mid run", "https://sk_live_0123456789ab\u2029cdef/v1", true},
	{"host nbsp behind head", "https://sk_live\u00a00123456789abcdef/v1", true},
	{"host nbsp in last label", "https://gw.example.sk_live_0123456789ab\u00a0cdef/v1", true},
	{"host line separator in middle label", "https://gw.sk_live_0123456789ab\u2028cdef.example.com/v1", true},
	{"host nbsp with port", "https://sk_live_0123456789ab\u00a0cdef:8443/v1", true},
	{"host line separator behind tok head", "https://tok_a1b2c3d4e5f6g7h8i9j0\u2028xx/v1", true},
	// fix8-7 accept side: a space separator in a NON-credential position, and the
	// host shapes this unit must not move (unicode and punycode labels, an IPv6
	// loopback, the long gateway labels, the identifier-class parts).
	{"host nbsp benign position", "https://mycompany2024\u00a0gateway.example.com/v1", false},
	{"host nbsp benign split word", "https://mycompany\u00a02024gateway.example.com/v1", false},
	{"host nbsp trailing label space", "https://gw\u00a0.example.com/v1", false},
	{"host line separator benign label", "https://acmeprod2024\u2028gateway.example.com/v1", false},
	{"punycode label", "https://xn--mnchen-3ya.example.com/v1", false},
	{"unicode label", "https://m\u00fcnchen.example.com/v1", false},
	{"ipv6 loopback", "http://[::1]:8080/v1", false},
}

// TestBaseURLCarriesCredentialCorpus pins the shared authority on both sides of
// every URL slot. The same corpus drives the door-agreement pins (the TUI form,
// the setup wizard, and the add-json parser), so a door that drifts from this
// table is a red test on all three.
func TestBaseURLCarriesCredentialCorpus(t *testing.T) {
	for _, tc := range baseURLAuthorityCorpus {
		t.Run(tc.name, func(t *testing.T) {
			if got := BaseURLCarriesCredential(tc.url); got != tc.refuse {
				t.Fatalf("BaseURLCarriesCredential(%q) = %v, want %v", tc.url, got, tc.refuse)
			}
			// The authority trims like every other ingest door, so padding cannot
			// hide a credential.
			if got := BaseURLCarriesCredential("  " + tc.url + "\t"); got != tc.refuse {
				t.Fatalf("BaseURLCarriesCredential(padded %q) = %v, want %v", tc.url, got, tc.refuse)
			}
		})
	}
	for _, empty := range []string{"", "   ", "\t\n"} {
		if BaseURLCarriesCredential(empty) {
			t.Fatalf("BaseURLCarriesCredential(%q) = true; want false", empty)
		}
	}
}

// TestBaseURLHostLabelRule pins the targeted host-label test: the head, the
// decorated, and the short-head-run shapes are refused, while a bare long run,
// a readable compound, a region word that is also an AWS head ("asia" without a
// run behind it), and the identifier forms stay accepted. This is the rule that
// closes the underscore class the host slot left open without refusing every
// location-shaped gateway hostname (the false dichotomy the review rejected).
func TestBaseURLHostLabelRule(t *testing.T) {
	refuse := []string{
		"sk_live_0123456789abcdef",
		"tok_a1b2c3d4e5f6g7h8i9j0",
		"pk_live_4f9a2b7c1d3e5f6a",
		"key_0123456789abcdef",
		"ghp_a1b2c3d4e5f6g7h8i9j0",
		"xoxb-0123456789abcdef",
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiJ9",
		"1-ASIAIOSFODNN7EXAMPLE",
	}
	for _, label := range refuse {
		if !baseURLHostLabelCarriesCredential(label) {
			t.Errorf("baseURLHostLabelCarriesCredential(%q) = false; want true", label)
		}
	}
	accept := []string{
		"mycompany2024gateway",
		"acmeprod2024gateway",
		"verylonggatewaylabelname",
		"0123456789abcdef",
		"aci-gw-2024-production",
		"us-east-1",
		"mycompany2024gateway-prod",
		"asia",
		"akia",
		"gw",
		"api",
		"550e8400-e29b-41a7-a716-446655440000",
		"550E8400-E29B-41A7-A716-446655440000",
		"",
	}
	for _, label := range accept {
		if baseURLHostLabelCarriesCredential(label) {
			t.Errorf("baseURLHostLabelCarriesCredential(%q) = true; want false", label)
		}
	}
	// The bare-run trade, stated as a premise: the whole-value arms, not the host
	// rule, are what refuse "https://0123456789abcdef/v1".
	if baseURLHostLabelCarriesCredential("") {
		t.Fatal("an empty host label must stay benign")
	}
}

// TestWizardBaseURLDoorMatchesSharedAuthority pins the wizard door against the
// shared authority over the same corpus. wizardBaseURLRefused is the exact
// function both wizard URL prompts call (step 2 and the edit-branch
// replacement), so this pin plus the full-run pins
// (TestInteractiveSetupWizard_BaseURLGuard) cover the door twice: the delegation
// cannot be reverted while this table stands, and the wiring cannot be reverted
// while those runs stand.
func TestWizardBaseURLDoorMatchesSharedAuthority(t *testing.T) {
	for _, tc := range baseURLAuthorityCorpus {
		if got := wizardBaseURLRefused(tc.url) != nil; got != tc.refuse {
			t.Fatalf("wizardBaseURLRefused(%q) = %v, want %v (the wizard door drifted from the shared authority)",
				tc.url, got, tc.refuse)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-8h: the three holes the fix8 review found in the shared URL authority
// ---------------------------------------------------------------------------

// TestBaseURLQueryRawPairClassification pins H-1: a query pair whose escape does
// not decode is still classified. url.Query() unwraps the percent-decode error
// and drops the pair, so before the raw read every row below was ACCEPTED at all
// three doors and persisted verbatim. Each refuse row carries the premise that
// the decoded view sees nothing, which is what makes the entry fail if the arm
// is reverted to url.Query(); the accept rows are the odd-but-legal escapes the
// review listed, so the closure narrowed no benign query.
func TestBaseURLQueryRawPairClassification(t *testing.T) {
	refuse := []string{
		"https://gw.example/v1?api_key=sk_live_0123456789abcdef%2",
		"https://gw.example/v1?sk_live_0123456789abcdef=1%2",
		"https://gw.example/v1?api-version=2024-02-01&api_key=sk_live_0123456789abcdef%2",
		"https://gw.example/v1?a=sk_live_0123456789abcdef;x=1",
	}
	for _, raw := range refuse {
		if !BaseURLCarriesCredential(raw) {
			t.Errorf("BaseURLCarriesCredential(%q) = false; want the raw-pair refusal", raw)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("premise: %q must parse, or it is not a query-arm row: %v", raw, err)
		}
		for key, vals := range u.Query() {
			if baseURLPartCarriesCredential(key) {
				t.Fatalf("premise: the decoded view already sees the key of %q", raw)
			}
			for _, v := range vals {
				if baseURLPartCarriesCredential(v) {
					t.Fatalf("premise: the decoded view already sees a value of %q", raw)
				}
			}
		}
	}
	for _, raw := range []string{
		"https://gw.example/v1?q=100%25",
		"https://gw.example/v1?q=100%",
		"https://gw.example/v1?filter=a%2Cb",
		"https://gw.example/v1?q=hello%20world",
		"https://gw.example/v1?api-version=2024-02-01%2",
	} {
		if BaseURLCarriesCredential(raw) {
			t.Errorf("BaseURLCarriesCredential(%q) = true; the odd-but-legal escape must stay accepted", raw)
		}
	}
	// The decoded read is unchanged: a well-formed encoded credential still
	// classifies from its decoded form.
	if !BaseURLCarriesCredential("https://gw.example/v1?api_key=sk_live%5F0123456789abcdef") {
		t.Error("a well-formed encoded credential is no longer refused")
	}
	// The helper's own fallback: decoded when legal, raw when not.
	if !baseURLQueryCarriesCredential("k=sk_live_0123456789abcdef%2") {
		t.Error("the raw fallback misses a malformed pair")
	}
	if baseURLQueryCarriesCredential("k=100%") || baseURLQueryCarriesCredential("q=100%25") {
		t.Error("a benign query pair classifies as credential material")
	}
}

// TestBaseURLHostLabelInvisibleStrip pins H-3: the host label is read in its
// visible form. url.Parse keeps an invisible rune in Hostname, no transport rule
// rejects it, and the shape test splits the credential run on it, so the host
// was the one slot a zero-width space could blind. The strip is the fold's
// four-class strip (Cf members, the BOM, and a combining mark), and it is the
// identity on a label with no invisible rune, so every benign host keeps its
// verdict.
func TestBaseURLHostLabelInvisibleStrip(t *testing.T) {
	for _, r := range []rune{'\u200b', '\u200d', '\u202e', '\ufeff', '\u0301'} {
		label := "sk_live_0123456789ab" + string(r) + "cdef"
		if !baseURLHostLabelCarriesCredential(label) {
			t.Errorf("baseURLHostLabelCarriesCredential(%q) = false; want true", label)
		}
		if !BaseURLCarriesCredential("https://" + label + "/v1") {
			t.Errorf("the invisible-rune host label %q is accepted", label)
		}
	}
	for _, s := range []string{"api.example.com", "aci-gw-2024-production", "https://gw.example/v1"} {
		if got := stripSlotInvisibles(s); got != s {
			t.Errorf("stripSlotInvisibles(%q) = %q; want the input unchanged", s, got)
		}
	}
	for _, raw := range []string{
		"https://mycompany2024\u200bgateway.example.com/v1",
		"https://aci-gw-\u200b2024-production.us-east-1.example.com/v1",
	} {
		if BaseURLCarriesCredential(raw) {
			t.Errorf("the benign host %q is refused; the strip must not create a refusal", raw)
		}
	}
}

// TestBaseURLUnparseableFailsClosed pins H-2 on the authority: a value url.Parse
// cannot read is refused instead of being deferred. Each row's premise is that
// the parser really fails, so an entry cannot rot into a tautology if the
// standard library starts accepting the shape.
func TestBaseURLUnparseableFailsClosed(t *testing.T) {
	for _, raw := range []string{
		"https://gw.example/v1/sk_live_0123456789abcdef%2",
		"https://gw.example/%zz",
		"https://gw.example:sk_live_0123456789abcdef/v1",
		"https://gw.example/v1#sk_live_0123456789abcdef%2",
		"https://sk_live_0123456789abcdef%2@gw.example/v1",
		"https://gw.example/v1/%",
	} {
		if _, err := url.Parse(raw); err == nil {
			t.Fatalf("premise: %q parses, so it is not an unparseable row", raw)
		}
		if !BaseURLCarriesCredential(raw) {
			t.Errorf("BaseURLCarriesCredential(%q) = false; want the fail-closed refusal", raw)
		}
	}
	// The trade is the documented one: a merely malformed URL is refused with the
	// credential verdict rather than a syntax diagnosis. Pinning the verdict keeps
	// the trade honest rather than accidental.
	if !BaseURLCarriesCredential("https://gw.example/%zz") {
		t.Error("a benign-but-unparseable URL must still be refused (fail closed)")
	}
}

// TestBaseURLParseErrorEchoIsValueFree pins H-2 on the echo side. The three
// composition sites in this package are the loader (Registry.Validate,
// providers.go:393), the mutation op (UpsertProvider, registry_ops.go:157), and
// the wizard's transport refusal (wizard.go:110). The TUI message line
// (screen_provider_form.go:1084/:1121) renders the same sentence through
// MaskOperand, so that transform is checked here too. The premise is that
// NewRedactor's patterns are the identity on the operand (they are: skRe needs a
// hyphen), which is why the body has to be value-free rather than merely
// redacted.
func TestBaseURLParseErrorEchoIsValueFree(t *testing.T) {
	cases := []struct{ url, secret string }{
		{"https://gw.example/v1/sk_live_0123456789abcdef%2", "sk_live_0123456789abcdef"},
		{"https://gw.example:sk_live_0123456789abcdef/v1", "sk_live_0123456789abcdef"},
		{"https://gw.example/v1#sk_live_0123456789abcdef%2", "sk_live_0123456789abcdef"},
		{"https://sk_live_0123456789abcdef%2@gw.example/v1", "sk_live_0123456789abcdef"},
		{"https://gw.example/v1/tok_a1b2c3d4e5f6g7h8i9j0%2", "tok_a1b2c3d4e5f6g7h8i9j0"},
	}
	for _, tc := range cases {
		if NewRedactor()(tc.secret) != tc.secret {
			t.Fatalf("premise: the redactor already masks %q, so this pin proves nothing", tc.secret)
		}
		msgs := map[string]string{}
		if err := validateBaseURL(tc.url); err != nil {
			msgs["validateBaseURL (wizard.go:110, TUI transport mirror)"] = err.Error()
		} else {
			t.Fatalf("validateBaseURL(%q) accepted an unparseable value", tc.url)
		}
		if err := (&Registry{Providers: []Provider{{Name: "probe", BaseURL: tc.url}}}).Validate(); err != nil {
			msgs["Registry.Validate (providers.go:393)"] = err.Error()
		} else {
			t.Fatalf("the loader accepted %q", tc.url)
		}
		if err := UpsertProvider(&Registry{}, Provider{Name: "zz", BaseURL: tc.url}); err != nil {
			msgs["UpsertProvider (registry_ops.go:157)"] = err.Error()
		} else {
			t.Fatalf("UpsertProvider accepted %q", tc.url)
		}
		for site, msg := range msgs {
			if strings.Contains(msg, tc.secret) {
				t.Errorf("%s echoes the credential for %q: %q", site, tc.url, msg)
			}
			if strings.Contains(MaskOperand(msg), tc.secret) {
				t.Errorf("the MaskOperand sink (screen_provider_form.go:1084/:1121) echoes the credential for %q: %q", tc.url, msg)
			}
		}
	}
	// The benign parse error stays a refusal with no operand echo at all: no
	// "parse \"...\"" prefix, which is what carried the value before the fix.
	if err := validateBaseURL("https://gw.example/%zz"); err == nil || strings.Contains(err.Error(), `parse "`) {
		t.Errorf("validateBaseURL(benign malformed) = %v; want a value-free refusal", err)
	}
}

// ---------------------------------------------------------------------------
// fix8-7: the host-label strip carries the separator-space classes (Zs/Zl/Zp)
// ---------------------------------------------------------------------------

// slotHostSpaceSeparatorRunes is the class this unit adds to the host-label
// strip: four Zs members a label can carry (the ASCII space included, so the
// predicate is the category and not a list), plus the one member each of Zl and
// Zp. A blank renders as nothing, so a run split by one is invisible to the
// shape test exactly as a zero-width space is.
var slotHostSpaceSeparatorRunes = []rune{' ', '\u00a0', '\u2007', '\u202f', '\u3000', '\u2028', '\u2029'}

// slotHostLabelHasSpaceSeparator reports whether s carries a Zs/Zl/Zp rune. It is
// the premise helper of the pins below: a row has to put the rune inside
// Hostname, or it exercises the host slot not at all.
func slotHostLabelHasSpaceSeparator(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return true
		}
	}
	return false
}

// TestBaseURLHostLabelSpaceSeparatorStrip pins the fix8-7 boundary the fix8-6
// review swarm left open. The host-label arm stripped the four invisible classes
// (Cc, Cf, Mn, Me) only, so a credential run split by a separator-SPACE rune was
// an identity at every ingest door and persisted: url.Parse keeps U+00A0, U+3000,
// U+2028, and U+2029 in Hostname, no transport rule rejects them, and the whole
// value arms stay the identity because the "_" of the underscore-head class
// breaks every run they read.
//
// The predicate and the strip are pinned first (a reverted class rule must fail
// on the class, not on one value), then the fold's own class, so a later edit
// that "unifies" the two helpers fails here instead of silently moving a
// path/query/fragment verdict.
func TestBaseURLHostLabelSpaceSeparatorStrip(t *testing.T) {
	// 1. Membership.
	for _, r := range slotHostSpaceSeparatorRunes {
		if !isSlotHostLabelInvisibleRune(r) {
			t.Errorf("U+%04X must be a host-label strip member (Zs/Zl/Zp)", r)
		}
	}
	for _, r := range []rune{'\u200b', '\u200d', '\u202e', '\ufeff', '\u0301'} {
		if !isSlotHostLabelInvisibleRune(r) {
			t.Errorf("U+%04X must stay a host-label strip member (Cc/Cf/Mn/Me)", r)
		}
	}
	for _, r := range []rune{'a', 'Z', '0', '-', '.', '/', '\u00e9', '\u00fc'} {
		if isSlotHostLabelInvisibleRune(r) {
			t.Errorf("U+%04X must NOT be a host-label strip member (a visible rune of a label)", r)
		}
	}

	// 2. The two strips answer different questions. The fold keeps the invisible
	// class as its DELETION set and REWRITES a non-ASCII separator member to "/",
	// so it must not start deleting a Zs member, and a Zl/Zp member is outside its
	// class entirely (a line separator is not a slash look-alike).
	if got := stripSlotInvisibles("ab3def7\u00a0gh9jk1mnpqr5t"); got != "ab3def7\u00a0gh9jk1mnpqr5t" {
		t.Errorf("stripSlotInvisibles deleted a space separator (%q); the fold's class stays Cc/Cf/Mn/Me", got)
	}
	if got := stripSlotInvisibles("ab3def7\u2028gh9jk1mnpqr5t"); got != "ab3def7\u2028gh9jk1mnpqr5t" {
		t.Errorf("stripSlotInvisibles deleted a line separator (%q); the fold's class stays Cc/Cf/Mn/Me", got)
	}
	if got, want := foldSeparators("ab3def7\u00a0gh9jk1mnpqr5t"), "ab3def7/gh9jk1mnpqr5t"; got != want {
		t.Errorf("foldSeparators(space-separated core) = %q; want %q (the Zs rewrite is the fold's contract)", got, want)
	}
	if got := foldSeparators("ab3def7\u2028gh9jk1mnpqr5t"); got != "ab3def7\u2028gh9jk1mnpqr5t" {
		t.Errorf("foldSeparators rewrote a line separator (%q); Zl/Zp are not the fold's class", got)
	}

	// 3. The refuse side, through the shared authority and the wizard door (both
	// real door functions of this package). Each row's premise is recomputed: the
	// value parses, the rune really sits in Hostname, and the whole-value arms are
	// the identity on it, which is what left the host arm as the only decider.
	refuse := []struct{ name, url string }{
		{"nbsp mid run", "https://sk_live_0123456789ab\u00a0cdef/v1"},
		{"ideographic space mid run", "https://sk_live_0123456789ab\u3000cdef/v1"},
		{"figure space mid run", "https://sk_live_0123456789ab\u2007cdef/v1"},
		{"narrow nbsp mid run", "https://sk_live_0123456789ab\u202fcdef/v1"},
		{"line separator mid run", "https://sk_live_0123456789ab\u2028cdef/v1"},
		{"paragraph separator mid run", "https://sk_live_0123456789ab\u2029cdef/v1"},
		{"nbsp behind the underscore head", "https://sk_live\u00a00123456789abcdef/v1"},
		{"nbsp in the last label", "https://gw.example.sk_live_0123456789ab\u00a0cdef/v1"},
		{"line separator in a middle label", "https://gw.sk_live_0123456789ab\u2028cdef.example.com/v1"},
		{"nbsp with a port", "https://sk_live_0123456789ab\u00a0cdef:8443/v1"},
		{"line separator behind the tok head", "https://tok_a1b2c3d4e5f6g7h8i9j0\u2028xx/v1"},
	}
	for _, tc := range refuse {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("premise: %q no longer parses, so it is not a host-slot row: %v", tc.url, err)
			}
			if !slotHostLabelHasSpaceSeparator(u.Hostname()) {
				t.Fatalf("premise: the space separator is no longer in Hostname of %q (%q)", tc.url, u.Hostname())
			}
			if SecretShaped(tc.url) || MaskOperand(tc.url) != tc.url {
				t.Fatalf("premise: the whole-value arms already refuse %q, so the row no longer isolates the host arm", tc.url)
			}
			if !baseURLHostCarriesCredential(u.Hostname()) {
				t.Errorf("baseURLHostCarriesCredential(%q) = false; want true (host run split by a space separator)", u.Hostname())
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true", tc.url)
			}
			if wizardBaseURLRefused(tc.url) == nil {
				t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", tc.url)
			}
			if !BaseURLCarriesCredential("  " + tc.url + "\t") {
				t.Errorf("the padded form of %q is accepted; every door trims before it classifies", tc.url)
			}
		})
	}

	// 4. The accept side: a space separator in a non-credential position stays
	// benign, and the pinned benign label shapes keep their verdict, so the wider
	// strip does not create a refusal on a host a registry may legitimately hold.
	accept := []struct{ name, label string }{
		{"long gateway label", "mycompany2024gateway"},
		{"long gateway label b", "acmeprod2024gateway"},
		{"compound gateway label", "aci-gw-2024-production"},
		{"region label", "us-east-1"},
		{"punycode label", "xn--mnchen-3ya"},
		{"unicode label", "m\u00fcnchen"},
		{"uuid label", "550e8400-e29b-41a7-a716-446655440000"},
		{"bare run label", "0123456789abcdef"},
		{"nbsp inside a non-credential label", "mycompany2024\u00a0gateway"},
		{"nbsp splitting a benign compound", "mycompany\u00a02024gateway"},
		{"nbsp behind a short word", "gw\u00a0"},
		{"line separator inside a non-credential label", "acmeprod2024\u2028gateway"},
		{"empty label", ""},
	}
	for _, tc := range accept {
		if baseURLHostLabelCarriesCredential(tc.label) {
			t.Errorf("baseURLHostLabelCarriesCredential(%q) = true; want false (benign host label)", tc.label)
		}
	}

	// 5. The strip is the identity on a label with no class member (so every
	// pre-existing host verdict is byte-identical) and deletes exactly the member
	// runes otherwise.
	for _, s := range []string{"api.example.com", "mycompany2024gateway", "aci-gw-2024-production", "xn--mnchen-3ya", "::1"} {
		if got := stripHostLabelInvisibles(s); got != s {
			t.Errorf("stripHostLabelInvisibles(%q) = %q; want the input unchanged", s, got)
		}
	}
	if got, want := stripHostLabelInvisibles("gw\u00a0.example\u2028com\u2029"), "gw.examplecom"; got != want {
		t.Errorf("stripHostLabelInvisibles(space-separated label) = %q; want %q", got, want)
	}
}

// TestBaseURLDoorsShareTheHostStrip pins this package's two out-of-package doors
// at the DELEGATION level. The fix8-7 verdicts arrive through
// providers.BaseURLCarriesCredential, so the TUI provider form's URL arm and the
// add-json parser cannot be called from here (internal/settings imports this
// package, and internal/tui imports both, so an import would cycle or invert the
// layering; the mission's file boundary allows this test file only). The pin
// therefore asserts the shape that makes the corpus verdicts the door verdicts:
// each arm is one call to the shared authority and keeps no second rule.
func TestBaseURLDoorsShareTheHostStrip(t *testing.T) {
	formBody, err := funcBody(filepath.Join("..", "tui", "screen_provider_form.go"), "fieldCarriesCredential")
	if err != nil {
		t.Fatalf("%v", err)
	}
	form := strings.Join(formBody, "\n")
	if !strings.Contains(form, "providers.BaseURLCarriesCredential(v)") {
		t.Fatalf("the TUI provider form's base-URL arm is no longer the shared authority:\n%s", form)
	}
	for _, stale := range []string{"providers.MaskBaseURL(", "providers.SecretShaped("} {
		if strings.Contains(form, stale) {
			t.Fatalf("the TUI provider form kept a second base-URL rule beside the shared authority (%s):\n%s", stale, form)
		}
	}

	specBody, err := funcBody(filepath.Join("..", "settings", "spec.go"), "ParseProviderSpecJSON")
	if err != nil {
		t.Fatalf("%v", err)
	}
	spec := strings.Join(specBody, "\n")
	if !strings.Contains(spec, "providers.BaseURLCarriesCredential(spec.BaseURL)") {
		t.Fatalf("the add-json parser's base-URL arm is no longer the shared authority:\n%s", spec)
	}
	if !strings.Contains(spec, "ErrProviderSpecBaseURLCarriesCredential") {
		t.Fatalf("the add-json parser no longer reports the named base-URL refusal:\n%s", spec)
	}
}

// ---------------------------------------------------------------------------
// masking fix round M1: O1 (rewrite-arm cross-separator) and O2 (Zl/Zp part arm)
// The open sites come from docs/261018_0002_session_agy-pro-tui/followup/
// masking-census-2026-10-21.md; the values below are synthetic.
// ---------------------------------------------------------------------------

// TestMaskSlotRewriteArmCrossSeparator pins census O1, the two-arm word/wide
// split of the rewrite arm.
//
// The identity arm measures a core with the base64 alphabet ("+", "/", and "="
// join the run) while the rewrite arm read the same core as WORDS, where all
// three break it. A location-decorated core that reaches the 16-rune floor only
// ACROSS those separators therefore rendered raw at the status banner id column
// and the models --json DisplayName field while its undecorated twin masked.
//
// The table was measured on the pre-fix tree (the mutation revert of this fix,
// run through -overlay). Two corrections to the census's shape list came out of
// that measurement: the open class needs an annotation or a non-separator
// breaker, because without one the whole-value classifier already reads the
// rewrite's marker-stripped core; and the census's "bare slot" example
// "./x.abcd1234+efgh5678" is NOT open on this tree for exactly that reason. Both
// facts are pinned below (block 1 versus block 2), and every row recomputes the
// pre-fix read first, so the table is a live before/after comparison and not a
// restatement of the fix.
func TestMaskSlotRewriteArmCrossSeparator(t *testing.T) {
	// 1. The OPEN class, measured: every row below is raw on the pre-fix tree. The
	// authority rewrites the value, the rewrite is not yet a mask, the whole-value
	// classifier declines it (an annotation or a non-separator breaker defeats the
	// core read), and the word read declines it too (every separator-joined piece
	// is under the floor). The premises recompute all four conditions, so the row
	// fails loudly if it stops isolating the split.
	open := []struct{ name, value, want string }{
		{"composed label, plus-joined", "./abcd1234+efgh5678 (lab)", "abcd1****"},
		{"composed label, equals-joined", "./x.abcd1234=efgh5678 (lab)", "abcd1****"},
		{"composed label, mixed separators", "./x.abcd1234+efgh5678=ijkl9012 (lab)", "abcd1****"},
		{"drive-decorated composed label", `C:\x.abcd1234+efgh5678 (lab)`, "abcd1****"},
		{"look-alike marker and composed label", "./\u2025x.abcd1234+efgh5678 (lab)", "abcd1****"},
		{"suffix-annotated slot", "./x.abcd1234+efgh5678#tag", "abcd1****"},
	}
	for _, tc := range open {
		t.Run(tc.name, func(t *testing.T) {
			clean := StripSlotControls(tc.value)
			out := MaskRegistryValue(clean)
			if out == clean {
				t.Fatalf("premise broken: the authority returned %q unchanged, so the row does not exercise the rewrite arm", tc.value)
			}
			pcore := stripSlotMarkers(foldSeparators(out))
			if pcore == "" || strings.Contains(pcore, "****") {
				t.Fatalf("premise broken: the authority's rewrite %q is already a mask or empty", out)
			}
			if SecretShaped(out) {
				t.Fatalf("premise broken: the whole-value classifier already masks the rewrite %q, so the row is not part of the open class", out)
			}
			if slotRunScan(pcore, nil) {
				t.Fatalf("premise broken: the pre-fix word-bounded read already masks the rewrite %q, so the row no longer isolates the split", pcore)
			}
			if !slotRewriteShaped(pcore, clean) {
				t.Fatalf("slotRewriteShaped(%q) = false; want true (the separator-joined fragment reaches the floor)", pcore)
			}
			got := MaskSlot(tc.value)
			if got != tc.want {
				t.Errorf("MaskSlot(%q) = %q; want %q (cross-separator credential at the display authority)", tc.value, got, tc.want)
			}
			for _, leak := range []string{"abcd1234", "efgh5678", "ijkl9012"} {
				if strings.Contains(got, leak) {
					t.Errorf("MaskSlot(%q) = %q leaks the credential fragment %q", tc.value, got, leak)
				}
			}
		})
	}

	// 2. Already closed by the OTHER classifier of the same arm. The census named
	// "./x.abcd1234+efgh5678" as a second open shape; measured on this tree it is
	// not, because the whole-value classifier reads the rewrite's marker-stripped
	// core ("x." is a drive prefix, so the core is the bare run) and masks it. The
	// rows are kept as regression pins, and the premise proves which classifier
	// owns them, so a later edit cannot reclassify them silently.
	for _, tc := range []struct{ value string }{
		{"./x.abcd1234+efgh5678"},
		{"./x.abcd1234=efgh5678"},
		{"./x.abcd1234+efgh5678=ijkl9012"},
		{`C:\x.abcd1234+efgh5678`},
	} {
		clean := StripSlotControls(tc.value)
		out := MaskRegistryValue(clean)
		if out == clean {
			t.Fatalf("premise broken: the authority returned %q unchanged", tc.value)
		}
		if !SecretShaped(out) {
			t.Fatalf("premise broken: the whole-value classifier no longer masks the rewrite %q, so the row belongs in the open class", out)
		}
		if got := MaskSlot(tc.value); got != "abcd1****" {
			t.Errorf("MaskSlot(%q) = %q; want abcd1**** (already-closed cross-separator shape must stay masked)", tc.value, got)
		}
	}

	// 3. The undecorated twins keep masking, each through the arm that already owns
	// it: the bare composed label and the dotted slot reach the IDENTITY arm (the
	// authority leaves them unchanged, which is the two-arm differential the
	// census isolated), and the two bare separator runs are masked by the
	// authority itself (MaskOperand reads one 17-rune contiguous run).
	for _, twin := range []struct{ value, want string }{
		{"abcd1234+efgh5678 (lab)", "abcd1****"},
		{"x.abcd1234+efgh5678", "abcd1****"},
	} {
		if got := MaskRegistryValue(StripSlotControls(twin.value)); got != StripSlotControls(twin.value) {
			t.Fatalf("premise broken: the authority rewrote %q, so the row does not exercise the identity arm", twin.value)
		}
		if got := MaskSlot(twin.value); got != twin.want {
			t.Errorf("MaskSlot(%q) = %q; want %q (the undecorated twin keeps masking)", twin.value, got, twin.want)
		}
	}
	for _, bare := range []string{"abcd1234+efgh5678", "abcd1234=efgh5678"} {
		if MaskRegistryValue(bare) == bare {
			t.Fatalf("premise broken: the authority left %q unchanged, so the row no longer exercises the whole-value arm", bare)
		}
		if got := MaskSlot(bare); got != "abcd1****" {
			t.Errorf("MaskSlot(%q) = %q; want abcd1**** (the undecorated bare run keeps masking)", bare, got)
		}
	}

	// The accept side: the join across "/" is OFF on the URL/path rewrite path,
	// because a slash there is a path separator rather than a base64 rune. Every
	// benign row below must render exactly as the authority's rewrite.
	benign := []string{
		"https://api.example.com/v1/completions", // the pinned benign URL rewrite
		"https://gw.example/v1/chat/completions",
		"https://example.com/v1",
		"./models/gemini-pro",
		"./models/anthropic/claude-3.5-sonnet",
		`C:\models\gemini-pro`,
		"meta-llama/Llama-3.3-70B-Instruct-Turbo (acme)",
		"gpt-4.1-mini (nw)",
	}
	for _, v := range benign {
		want := MaskRegistryValue(StripSlotControls(v))
		if got := MaskSlot(v); got != want {
			t.Errorf("MaskSlot(%q) = %q; want the authority's %q byte-identical (no masking on a benign rewrite)", v, got, want)
		}
	}
	// The two URL rows would be newly masked by a slash-joining read: their
	// rewrites carry the slash-joined 18-rune and 22-rune fragments. If this
	// premise ever stops holding, the boundary is unexercised and the join
	// decision needs re-derivation rather than a silent widening.
	for _, v := range []string{"https://api.example.com/v1/completions", "https://gw.example/v1/chat/completions"} {
		pcore := stripSlotMarkers(foldSeparators(MaskRegistryValue(v)))
		if !slotSegmentShaped(pcore) {
			t.Fatalf("premise broken: the wide read no longer masks the benign URL rewrite %q, so the slash boundary is unexercised", pcore)
		}
		if !slotRewriteIsLocationStrip(v) {
			t.Fatalf("premise broken: %q is no longer read as a URL/path rewrite, so the slash join would apply", v)
		}
	}

	// The documented boundary of the census's variant A: a slash-joined plant that
	// wears a location marker stays raw, because the join across "/" is off on the
	// URL/path rewrite path by construction. These rows render exactly as the
	// authority rewrites them, which is the pre-fix rendering. The pins exist so
	// the boundary cannot move in either direction without a conscious edit (a
	// widening here, or a later fix that decides the slash class).
	for _, v := range []string{"./x.abcd1234/efgh5678 (lab)", "https://x.abcd1234/efgh5678 (lab)"} {
		clean := StripSlotControls(v)
		if MaskRegistryValue(clean) == clean {
			t.Fatalf("premise broken: the authority returned %q unchanged, so the row does not exercise the rewrite arm", v)
		}
		if got, want := MaskSlot(v), MaskRegistryValue(clean); got != want {
			t.Errorf("MaskSlot(%q) = %q; want the authority's %q (the slash join stays off on the URL/path rewrite path)", v, got, want)
		}
	}
}

// TestMaskSlotComposedLabelAtTheConvertedSinks pins the end-to-end property of
// census O1 at the two named sinks without writing a registry: the status banner
// id column (cmd/agy/main.go, providers.MaskSlot(providers.DisplayName(m.ID,
// p.Name))) and the models --json DisplayName field (internal/inventory builds
// Entry.DisplayName through providers.DisplayName, cmd/agy/pro_models.go masks
// it). The composition is driven exactly as those sinks build it, and the sink
// source is pinned at the delegation shape so a sink cannot move off the
// authority while this row stays green.
func TestMaskSlotComposedLabelAtTheConvertedSinks(t *testing.T) {
	// The banner column and the inventory mid column, composed the way the sinks
	// compose it: DisplayName(id, providerName).
	label := DisplayName("./abcd1234+efgh5678", "lab")
	if label != "./abcd1234+efgh5678 (lab)" {
		t.Fatalf("DisplayName composition moved: %q", label)
	}
	if got := MaskSlot(label); got != "abcd1****" {
		t.Errorf("MaskSlot(%q) = %q; want abcd1**** (status banner id column and models --json DisplayName)", label, got)
	}
	// The inventory id column masks the raw id (inventory.RenderNumbered), and
	// the models --json twin masks each field; the bare slot is the same plant
	// without the annotation.
	for _, sinkValue := range []string{
		"./x.abcd1234+efgh5678",       // RenderNumbered id width and cell
		"./x.abcd1234+efgh5678 (lab)", // Entry.DisplayName as inventory builds it
		DisplayName("./x.abcd1234+efgh5678", "lab"),
	} {
		if got := MaskSlot(sinkValue); got != "abcd1****" {
			t.Errorf("MaskSlot(%q) = %q; want abcd1**** (converted inventory sink value)", sinkValue, got)
		}
	}
	// Benign composed labels stay byte-identical at the same sinks.
	for _, pair := range [][2]string{
		{"./models/gemini-pro", "acme"},
		{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "acme"},
		{"gpt-4.1-mini", "nw"},
	} {
		l := DisplayName(pair[0], pair[1])
		if got, want := MaskSlot(l), MaskRegistryValue(StripSlotControls(l)); got != want {
			t.Errorf("MaskSlot(%q) = %q; want the authority's %q byte-identical", l, got, want)
		}
	}

	// The delegation shape of the three sinks this property depends on. Whitespace
	// is ignored, so a reformat (a line break inside the call) does not fail the
	// pin, while a sink that stops composing through DisplayName or stops routing
	// the composition through MaskSlot does.
	sinks := []struct{ file, want string }{
		{filepath.Join("..", "..", "cmd", "agy", "main.go"), "providers.MaskSlot(providers.DisplayName("},
		{filepath.Join("..", "inventory", "inventory.go"), "DisplayName:providers.DisplayName("},
		{filepath.Join("..", "inventory", "inventory.go"), "providers.MaskSlot(e.DisplayName)"},
		{filepath.Join("..", "..", "cmd", "agy", "pro_models.go"), "inv.Entries[i].DisplayName=providers.MaskSlot("},
	}
	for _, sink := range sinks {
		raw, err := os.ReadFile(sink.file)
		if err != nil {
			t.Fatalf("read %s: %v", sink.file, err)
		}
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, string(raw))
		if !strings.Contains(compact, sink.want) {
			t.Errorf("%s no longer contains %q: the sink stopped composing or masking through the shared authority", sink.file, sink.want)
		}
	}
}

// TestBaseURLPartArmLineSeparatorStrip pins census O2, the Zl/Zp residual of the
// URL PART arms. The host label strips Zs/Zl/Zp (fix8-7), but the path, query,
// fragment, userinfo, and Opaque arms classify through MaskSlot, whose strips
// delete Cc+Cf and Cc/Cf/Mn/Me only. A U+2028 or U+2029 inside a part therefore
// survives into the run scan and FLUSHES the run, so
// "https://<host>/v1/<head>_<12-rune run><U+2028><14-rune run>" was the identity
// of the authority at all three doors and echoed raw later (the display side
// shares the classifier).
//
// The fix is a part-arm-only Zl/Zp strip (stripURIPartLineSeparators). The fold
// helpers keep their classes, which this test pins separately so a later
// "unification" cannot move a path/query/fragment verdict through them.
func TestBaseURLPartArmLineSeparatorStrip(t *testing.T) {
	// 1. Membership: the part strip carries exactly Zl and Zp.
	for _, r := range []rune{'\u2028', '\u2029'} {
		if !isSlotURIPartLineSeparatorRune(r) {
			t.Errorf("U+%04X must be a part-arm strip member (Zl/Zp)", r)
		}
	}
	for _, r := range []rune{' ', '\u00a0', '\u2007', '\u202f', '\u3000', '\u200b', '\u202e', '\ufeff', '\u0301', 'a', '0', '/', '.', '+', '=', '\u00e9'} {
		if isSlotURIPartLineSeparatorRune(r) {
			t.Errorf("U+%04X must NOT be a part-arm strip member (the fold's classes stay separate)", r)
		}
	}
	// 2. The fold helpers keep their classes (the fix8-7 boundary): the fold
	// deletes Cc/Cf/Mn/Me, rewrites non-ASCII separator members, and leaves a line
	// or paragraph separator alone.
	if got, want := foldSeparators("ab3def7\u2028gh9jk1mnpqr5t"), "ab3def7\u2028gh9jk1mnpqr5t"; got != want {
		t.Errorf("foldSeparators rewrote a line separator (%q); the fold's class is unchanged", got)
	}
	if got, want := foldSeparators("ab3def7\u00a0gh9jk1mnpqr5t"), "ab3def7/gh9jk1mnpqr5t"; got != want {
		t.Errorf("foldSeparators(space-separated core) = %q; want %q (the Zs rewrite is the fold's contract)", got, want)
	}
	if got, want := stripURIPartLineSeparators("api.example.com"), "api.example.com"; got != want {
		t.Errorf("stripURIPartLineSeparators(%q) = %q; want the identity", "api.example.com", got)
	}
	if got, want := stripURIPartLineSeparators("ab\u2028cd\u2029ef\u00a0"), "abcdef\u00a0"; got != want {
		t.Errorf("stripURIPartLineSeparators(line-separated part) = %q; want %q", got, want)
	}

	// 3. The refuse side, one row per part arm. Each row recomputes the pre-fix
	// blindness (the part, with only its control runes stripped as MaskSlot's first
	// stage does, is the identity) and the post-fix classification (the stripped
	// part masks), so the strip is load-bearing on every row rather than incidental
	// to another arm.
	refuse := []struct{ name, url, encoded, part string }{
		{
			"path segment, raw U+2028",
			"https://gw.example/v1/sk_live_0123456789ab\u2028cdefgh56789012",
			"\u2028",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"path segment, raw U+2029",
			"https://gw.example/v1/sk_live_0123456789ab\u2029cdefgh56789012",
			"\u2029",
			"sk_live_0123456789ab\u2029cdefgh56789012",
		},
		{
			"path segment, percent-encoded U+2028",
			"https://gw.example/v1/sk_live_0123456789ab%E2%80%A8cdefgh56789012",
			"%E2%80%A8",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"query value, raw U+2028",
			"https://gw.example/v1?api_key=sk_live_0123456789ab\u2028cdefgh56789012",
			"\u2028",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"query key, raw U+2028",
			"https://gw.example/v1?sk_live_0123456789ab\u2028cdefgh56789012=1",
			"\u2028",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"fragment, raw U+2028",
			"https://gw.example/v1#sk_live_0123456789ab\u2028cdefgh56789012",
			"\u2028",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"userinfo password, percent-encoded U+2028",
			"https://user:sk_live_0123456789ab%E2%80%A8cdefgh56789012@gw.example/v1",
			"%E2%80%A8",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
		{
			"userinfo username, percent-encoded U+2029",
			"https://sk_live_0123456789ab%E2%80%A9cdefgh56789012@gw.example/v1",
			"%E2%80%A9",
			"sk_live_0123456789ab\u2029cdefgh56789012",
		},
		{
			"opaque slot, raw U+2028",
			"https:sk_live_0123456789ab\u2028cdefgh56789012",
			"\u2028",
			"sk_live_0123456789ab\u2028cdefgh56789012",
		},
	}
	for _, tc := range refuse {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.url, tc.encoded) {
				t.Fatalf("premise: %q does not carry %q", tc.url, tc.encoded)
			}
			// The whole-value arms are the identity on the row: nothing but the part
			// arm can decide it.
			if SecretShaped(tc.url) || MaskOperand(tc.url) != tc.url {
				t.Fatalf("premise: the whole-value arms already refuse %q, so the row no longer isolates a part arm", tc.url)
			}
			if u, err := url.Parse(tc.url); err == nil && baseURLHostCarriesCredential(u.Hostname()) {
				t.Fatalf("premise: the host arm already refuses %q, so the row no longer isolates a part arm", tc.url)
			}
			// Pre-fix blindness: with the line separator still in place, MaskSlot is
			// the identity on the part (its own Cc/Cf strip is the only change).
			if got := MaskSlot(StripSlotControls(tc.part)); got != StripSlotControls(tc.part) {
				t.Fatalf("premise broken: MaskSlot(%q) = %q, so the part already classified as credential material before the strip", tc.part, got)
			}
			// Post-fix: the stripped part is credential material, and the part arm
			// says so.
			if stripped := stripURIPartLineSeparators(tc.part); MaskSlot(stripped) == stripped {
				t.Fatalf("premise broken: MaskSlot(%q) is the identity, so the strip does not join the run", stripped)
			}
			if !baseURLPartCarriesCredential(tc.part) {
				t.Errorf("baseURLPartCarriesCredential(%q) = false; want true (run split by a line separator)", tc.part)
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true", tc.url)
			}
			if err := wizardBaseURLRefused(tc.url); err == nil {
				t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", tc.url)
			}
			if !BaseURLCarriesCredential("  " + tc.url + "\t") {
				t.Errorf("the padded form of %q is accepted; every door trims before it classifies", tc.url)
			}
		})
	}

	// 3b. The mixed spelling: a decoded control rune next to a line separator. The
	// part arm's value comparison changes on the control strip alone, so this row is
	// refused by the control rule rather than by the line-separator strip, and the
	// mutation revert that removes the strip leaves it green. It is kept as a
	// spelling-coverage row, with the governing rule recorded: if the control rule
	// ever stops changing the value, this row stops being refused and the assertion
	// below says so.
	mixedPart := "sk_live_0123456789ab\u2029\u0007cdefgh56789012"
	mixedURL := "https://gw.example/v1?api_key=sk_live_0123456789ab%E2%80%A9%07cdefgh56789012"
	if MaskSlot(mixedPart) == mixedPart {
		t.Fatalf("premise broken: the control rune no longer changes %q under MaskSlot, so the row now depends on the Zl/Zp strip", mixedPart)
	}
	if !baseURLPartCarriesCredential(mixedPart) {
		t.Errorf("baseURLPartCarriesCredential(%q) = false; want true (control rune in the part)", mixedPart)
	}
	if !BaseURLCarriesCredential(mixedURL) {
		t.Errorf("BaseURLCarriesCredential(%q) = false; want true", mixedURL)
	}
	if err := wizardBaseURLRefused(mixedURL); err == nil {
		t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", mixedURL)
	}

	// 4. The accept side: a line separator in a NON-credential position stays
	// benign (the strip is the identity on the classification), and benign URLs
	// without one render byte-identically.
	for _, benign := range []string{
		"https://gw.example/v1/acme\u2028gateway",
		"https://gw.example/v1/acme\u2029gateway",
		"https://gw.example/v1/\u2028",
		"https://api.openai.com/v1",
		"https://gw.example/v1",
	} {
		if BaseURLCarriesCredential(benign) {
			t.Errorf("BaseURLCarriesCredential(%q) = true; want false (benign part)", benign)
		}
		if got, want := MaskSlot(benign), MaskRegistryValue(StripSlotControls(benign)); got != want {
			t.Errorf("MaskSlot(%q) = %q; want the authority's %q byte-identical", benign, got, want)
		}
	}

	// 5. The pinned URL corpus is unchanged, structurally: no row carries a Zl/Zp
	// outside the host label, so the part-arm strip is the identity on every part
	// of every row and no verdict can move. The row count is the round's baseline;
	// a later edit that adds or drops a row must revisit this proof.
	const corpusRowsThisRound = 76
	if len(baseURLAuthorityCorpus) != corpusRowsThisRound {
		t.Fatalf("the URL corpus moved (%d rows, this round pinned %d); re-verify the part-arm strip against the new rows", len(baseURLAuthorityCorpus), corpusRowsThisRound)
	}
	for _, tc := range baseURLAuthorityCorpus {
		if got := BaseURLCarriesCredential(tc.url); got != tc.refuse {
			t.Errorf("corpus row %q moved: BaseURLCarriesCredential(%q) = %v, want %v", tc.name, tc.url, got, tc.refuse)
		}
		if !strings.ContainsAny(tc.url, "\u2028\u2029") {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(tc.url))
		if err != nil {
			t.Fatalf("corpus row %q no longer parses: %v", tc.name, err)
		}
		if !strings.ContainsAny(u.Host, "\u2028\u2029") {
			t.Fatalf("corpus row %q carries a line separator outside the host label; the part-arm strip would decide it", tc.name)
		}
	}
}

// ---------------------------------------------------------------------------
// The percent-encoded part hole: the path and Opaque segment loop classified
// every segment RAW. url.Parse decodes the path slot once, but a hostless
// "https:<value>" spelling lands in u.Opaque verbatim, so an escape-encoded
// line separator (or a fully escape-encoded credential) kept its "%" bytes and
// the run scan flushed at each of them. Each segment is now classified in its
// decoded form as well (url.PathUnescape), beside the raw read; a segment whose
// escapes are malformed stays raw-only.
// ---------------------------------------------------------------------------

// TestBaseURLPercentDecodedPartArm pins the decoded read of the path and Opaque
// segment loop in three parts: the rows where that read is the deciding arm,
// the spellings that were already refused before it, and the rows that must not
// move (malformed escapes, benign legal escapes).
func TestBaseURLPercentDecodedPartArm(t *testing.T) {
	// 1. The isolating rows: the decoded read decides them. Every row proves its
	// own premises (the raw segment is accepted, the whole-value arms and the
	// host arm are the identity, the decoded segment is credential material), so
	// a later change that closes a row through another arm says so instead of
	// passing silently.
	isolating := []struct {
		name    string
		url     string
		rawPart string
		decoded string
	}{
		{
			name:    "hostless Opaque slot, escape-encoded U+2028",
			url:     "https:sk_live_0123456789ab%e2%80%a8cdefgh5678",
			rawPart: "sk_live_0123456789ab%e2%80%a8cdefgh5678",
			decoded: "sk_live_0123456789ab\u2028cdefgh5678",
		},
		{
			name:    "hostless Opaque slot, fully escape-encoded credential",
			url:     "https:%73%6b%5f%6c%69%76%65%5f%30%31%32%33%34%35%36%37%38%39%61%62%63%64%65%66",
			rawPart: "%73%6b%5f%6c%69%76%65%5f%30%31%32%33%34%35%36%37%38%39%61%62%63%64%65%66",
			decoded: "sk_live_0123456789abcdef",
		},
		{
			name:    "path segment, double-encoded credential",
			url:     "https://gw.example/v1/%2573%256b%255f%256c%2569%2576%2565%255f%2530%2531%2532%2533%2534%2535%2536%2537%2538%2539%2561%2562%2563%2564%2565%2566",
			rawPart: "%73%6b%5f%6c%69%76%65%5f%30%31%32%33%34%35%36%37%38%39%61%62%63%64%65%66",
			decoded: "sk_live_0123456789abcdef",
		},
	}
	for _, tc := range isolating {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("premise: url.Parse rejects the row: %v", err)
			}
			if slot := u.Path + u.Opaque; !strings.Contains(slot, tc.rawPart) {
				t.Fatalf("premise: the path/Opaque slot %q does not carry the raw segment %q", slot, tc.rawPart)
			}
			if SecretShaped(tc.url) || MaskOperand(tc.url) != tc.url {
				t.Fatalf("premise: a whole-value arm already refuses the row, so it no longer isolates the decoded read")
			}
			if baseURLHostCarriesCredential(u.Hostname()) {
				t.Fatalf("premise: the host arm already refuses the row")
			}
			// The hole: the raw segment is accepted by the part arm.
			if baseURLPartCarriesCredential(tc.rawPart) {
				t.Fatalf("premise broken: the raw segment already classifies as credential material, so the decoded read is not what decides the row")
			}
			decoded, err := url.PathUnescape(tc.rawPart)
			if err != nil || decoded != tc.decoded {
				t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want %q", tc.rawPart, decoded, err, tc.decoded)
			}
			if !baseURLPartCarriesCredential(tc.decoded) {
				t.Fatalf("premise broken: the decoded part %q does not classify as credential material", tc.decoded)
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true (escape-encoded part)", tc.url)
			}
			if err := wizardBaseURLRefused(tc.url); err == nil {
				t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", tc.url)
			}
			if !BaseURLCarriesCredential("  " + tc.url + "\t") {
				t.Errorf("the padded form of %q is accepted; every door trims before it classifies", tc.url)
			}
		})
	}

	// 2. Spelling coverage: the rows the review named that were already refused
	// before the decoded read, kept so the spellings stay refused. Each row also
	// asserts the arm that owns it (the raw part read), so the row's verdict has
	// a measured owner rather than riding on another arm silently.
	//
	// The lowercase row's post-escape tail ("a8cdefgh56789012") lands exactly on
	// the 16-rune floor, which is why the raw read already caught it; the
	// isolating rows above shorten the tail to put the decoded read in charge.
	// The uppercase row is also refused by a whole-value arm (MaskOperand is not
	// the identity on it, see the premise below), which is the "different arm
	// incidentally" the round recorded.
	coverage := []struct {
		name    string
		url     string
		rawPart string
	}{
		{
			name:    "hostless Opaque slot, lowercase escapes at the run floor",
			url:     "https:sk_live_0123456789ab%e2%80%a8cdefgh56789012",
			rawPart: "sk_live_0123456789ab%e2%80%a8cdefgh56789012",
		},
		{
			name:    "hostless Opaque slot, uppercase escapes",
			url:     "https:sk_live_0123456789ab%E2%80%A8cdefgh56789012",
			rawPart: "sk_live_0123456789ab%E2%80%A8cdefgh56789012",
		},
		{
			name:    "path segment, single-encoded credential",
			url:     "https://gw.example/v1/%73%6b%5f%6c%69%76%65%5f0123456789abcdef",
			rawPart: "sk_live_0123456789abcdef",
		},
	}
	for _, tc := range coverage {
		t.Run(tc.name, func(t *testing.T) {
			if !baseURLPartCarriesCredential(tc.rawPart) {
				t.Errorf("the raw part read no longer refuses %q; re-derive the row's verdict", tc.rawPart)
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true", tc.url)
			}
		})
	}
	if MaskOperand("https:sk_live_0123456789ab%E2%80%A8cdefgh56789012") == "https:sk_live_0123456789ab%E2%80%A8cdefgh56789012" {
		t.Errorf("premise: MaskOperand became the identity on the uppercase spelling; the row's incidental owner changed")
	}

	// 3. A segment whose escapes do not decode legally is classified raw only,
	// so its verdict is exactly what it was before the decoded read: ACCEPTED
	// (the raw run stays under the floor). url.Parse accepts the hostless form
	// and keeps the malformed escape in Opaque, which is what makes this row
	// reach the part arm at all.
	const (
		malformedURL  = "https:sk_live_0123456789ab%2"
		malformedPart = "sk_live_0123456789ab%2"
	)
	if _, err := url.PathUnescape(malformedPart); err == nil {
		t.Fatalf("premise: url.PathUnescape decodes the malformed escape, so the row no longer tests the raw-only branch")
	}
	if _, err := url.Parse(malformedURL); err != nil {
		t.Fatalf("premise: url.Parse rejects the hostless spelling (%v), so the row would test the fail-closed arm", err)
	}
	if baseURLPartCarriesCredential(malformedPart) {
		t.Errorf("baseURLPartCarriesCredential(%q) = true; want false (raw classification)", malformedPart)
	}
	if BaseURLCarriesCredential(malformedURL) {
		t.Errorf("BaseURLCarriesCredential(%q) = true; want false: a malformed escape is classified raw, exactly as before the decoded read", malformedURL)
	}

	// 4. The accept side: benign legal escapes keep their exact verdicts. The
	// path rows are already decoded by url.Parse; the Opaque rows are decoded by
	// the new read, so both spellings of the same benign value are pinned.
	for _, benign := range []struct{ url, decodedPart string }{
		{"https://gw.example/v1/acme%20gateway", "acme gateway"},
		{"https:acme%20gateway", "acme gateway"},
		{"https://gw.example/v1/%61%62%63", "abc"},
		{"https:%61%62%63", "abc"},
	} {
		if baseURLPartCarriesCredential(benign.decodedPart) {
			t.Errorf("the decoded benign part %q classifies as credential material", benign.decodedPart)
		}
		if BaseURLCarriesCredential(benign.url) {
			t.Errorf("BaseURLCarriesCredential(%q) = true; want false (benign legal escape)", benign.url)
		}
	}

	// 5. The decode function choice: the segment loop decodes with
	// url.PathUnescape, which keeps a literal "+" as the base64 rune it is.
	// url.QueryUnescape would turn the same literal "+" into a space, split the
	// run below the floor, and accept the plant. The two decoders differ only on
	// a literal "+", so the row below carries one beside an escape: the raw read
	// accepts (the "%" splits the run), the path-decoded read refuses (one long
	// run), and the query-decoded read would accept (the space splits it). A
	// swap of the decoded read to the form decoder fails this row.
	const plusURL = "https:abcd1234+efgh%2b5678"
	const plusRaw = "abcd1234+efgh%2b5678"
	const plusPathDecoded = "abcd1234+efgh+5678"
	const plusQueryDecoded = "abcd1234 efgh+5678"
	if decoded, err := url.PathUnescape(plusRaw); err != nil || decoded != plusPathDecoded {
		t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want %q", plusRaw, decoded, err, plusPathDecoded)
	}
	if decoded, err := url.QueryUnescape(plusRaw); err != nil || decoded != plusQueryDecoded {
		t.Fatalf("premise: url.QueryUnescape(%q) = %q, %v; want %q (the row's discriminator)", plusRaw, decoded, err, plusQueryDecoded)
	}
	if baseURLPartCarriesCredential(plusRaw) {
		t.Fatalf("premise broken: the raw segment already classifies, so the row no longer isolates the decoded read")
	}
	if !baseURLPartCarriesCredential(plusPathDecoded) {
		t.Fatalf("premise broken: the path-decoded part %q is not credential material", plusPathDecoded)
	}
	if baseURLPartCarriesCredential(plusQueryDecoded) {
		t.Fatalf("premise broken: the query-decoded part %q classifies, so the row no longer discriminates the decode function", plusQueryDecoded)
	}
	if !BaseURLCarriesCredential(plusURL) {
		t.Errorf("BaseURLCarriesCredential(%q) = false; want true: the decoded read must keep a literal + as a base64 rune", plusURL)
	}
}

// ---------------------------------------------------------------------------
// M5: the decoded read in the host arm
// ---------------------------------------------------------------------------

// TestBaseURLHostDecodedArm pins the last layer-1 decode hole in the shared URL
// authority. url.Parse decodes a non-ASCII host escape itself, but a "%25"
// escape survives as the literal "%" it decoded to, so a doubled spelling
// reaches the host arm as escape TEXT and every percent byte breaks the run the
// label rule reads. Each closing row is an isolating row: the raw host arm must
// be the identity on it, the whole-value arms must be the identity on the URL,
// and the decoded host must be credential material, so the entry fails if the
// decoded read is removed instead of passing through some other arm.
func TestBaseURLHostDecodedArm(t *testing.T) {
	closing := []struct {
		name        string
		url         string
		rawHost     string
		decodedHost string
	}{
		{
			name:        "doubled escape-encoded underscore-head host",
			url:         "https://%2573%256b%255f%256c%2569%2576%2565%255f%2530%2531%2532%2533%2534%2535%2536%2537%2538%2539%2561%2562%2563%2564%2565%2566/v1",
			rawHost:     "%73%6b%5f%6c%69%76%65%5f%30%31%32%33%34%35%36%37%38%39%61%62%63%64%65%66",
			decodedHost: "sk_live_0123456789abcdef",
		},
		{
			name:        "doubled escape-encoded AWS key-id host",
			url:         "https://%2541%254b%2549%2541%2549%254f%2553%2546%254f%2544%254e%254e%2537%2545%2558%2541%254d%2550%254c%2545.example.com/v1",
			rawHost:     "%41%4b%49%41%49%4f%53%46%4f%44%4e%4e%37%45%58%41%4d%50%4c%45.example.com",
			decodedHost: "AKIAIOSFODNN7EXAMPLE.example.com",
		},
		{
			name:        "doubled escape-encoded line separator inside a run",
			url:         "https://sk_live_0123456789ab%25e2%2580%25a8cdefgh5678/v1",
			rawHost:     "sk_live_0123456789ab%e2%80%a8cdefgh5678",
			decodedHost: "sk_live_0123456789ab\u2028cdefgh5678",
		},
		{
			name:        "doubled escape that decodes to a dot",
			url:         "https://sk_live_0123456789abcdef%252eexample.com/v1",
			rawHost:     "sk_live_0123456789abcdef%2eexample.com",
			decodedHost: "sk_live_0123456789abcdef.example.com",
		},
	}
	for _, tc := range closing {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("premise: %q must parse, or it is not a host-arm row: %v", tc.url, err)
			}
			if got := u.Hostname(); got != tc.rawHost {
				t.Fatalf("premise: u.Hostname() = %q; want the escape text %q", got, tc.rawHost)
			}
			if baseURLHostCarriesCredential(tc.rawHost) {
				t.Fatalf("premise broken: the RAW host arm already refuses %q, so the row no longer isolates the decoded read", tc.rawHost)
			}
			decoded, derr := url.PathUnescape(tc.rawHost)
			if derr != nil || decoded != tc.decodedHost {
				t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want %q", tc.rawHost, decoded, derr, tc.decodedHost)
			}
			if !baseURLHostCarriesCredential(decoded) {
				t.Fatalf("premise broken: the decoded host %q is not credential material", decoded)
			}
			if SecretShaped(tc.url) || MaskOperand(tc.url) != tc.url {
				t.Fatalf("premise broken: a whole-value arm owns the row, so the host read is not the deciding arm")
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true (decoded host)", tc.url)
			}
			if err := wizardBaseURLRefused(tc.url); err == nil {
				t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", tc.url)
			}
			if !BaseURLCarriesCredential("  " + tc.url + "\t") {
				t.Errorf("the padded form of %q is accepted; every door trims before it classifies", tc.url)
			}
		})
	}

	// The dispatch spellings: a SINGLE-encoded ASCII host. Measured owner is the
	// fail-closed parse arm, not the decoded read: url.Parse rejects an ASCII
	// escape in the host slot ("invalid URL escape"), and the authority refuses
	// a value it cannot parse. Pinned so the refusal is recorded with its real
	// owner.
	single := []struct{ name, url string }{
		{"single escape-encoded underscore-head host", "https://%73%6b%5f%6c%69%76%65%5f%30%31%32%33%34%35%36%37%38%39%61%62%63%64%65%66/v1"},
		{"single escape-encoded AWS key-id host", "https://%41%4b%49%41%49%4f%53%46%4f%44%4e%4e%37%45%58%41%4d%50%4c%45.example.com/v1"},
	}
	for _, tc := range single {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := url.Parse(tc.url); err == nil {
				t.Fatalf("premise broken: %q parses, so it is not a parse-arm row", tc.url)
			}
			if SecretShaped(tc.url) || MaskOperand(tc.url) != tc.url {
				t.Fatalf("premise broken: a whole-value arm owns the row, so the parse arm is not the deciding one")
			}
			if !BaseURLCarriesCredential(tc.url) {
				t.Errorf("BaseURLCarriesCredential(%q) = false; want true (unparseable value fails closed)", tc.url)
			}
			if err := wizardBaseURLRefused(tc.url); err == nil {
				t.Errorf("wizardBaseURLRefused(%q) = nil; want the credential refusal", tc.url)
			}
			if !BaseURLCarriesCredential("  " + tc.url + "\t") {
				t.Errorf("the padded form of %q is accepted; every door trims before it classifies", tc.url)
			}
		})
	}
}

// TestBaseURLHostDecodedArmBounds pins the accept side and the limits of the
// host decoded read: legal escapes that decode to benign labels keep their exact
// verdict, a host whose escapes do not decode stays raw-only with its exact
// verdict, a host with no escape is untouched (the decoded form equals the raw
// one, so the second read never runs), and the decode function is
// url.PathUnescape (a literal "+" stays a base64 rune, not a form-encoded
// space).
func TestBaseURLHostDecodedArmBounds(t *testing.T) {
	accept := []struct {
		name        string
		url         string
		rawHost     string
		decodedHost string
	}{
		{
			name:        "doubled space decodes to a benign label",
			url:         "https://acme%2520gateway.example.com/v1",
			rawHost:     "acme%20gateway.example.com",
			decodedHost: "acme gateway.example.com",
		},
		{
			name:        "doubled escape decodes to a benign short label",
			url:         "https://%2561%2562%2563.example.com/v1",
			rawHost:     "%61%62%63.example.com",
			decodedHost: "abc.example.com",
		},
		{
			name:        "single non-ASCII escape arrives decoded from url.Parse",
			url:         "https://sk%c2%a0live.example.com/v1",
			rawHost:     "sk\u00a0live.example.com",
			decodedHost: "sk\u00a0live.example.com",
		},
	}
	for _, tc := range accept {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("premise: %q must parse: %v", tc.url, err)
			}
			if got := u.Hostname(); got != tc.rawHost {
				t.Fatalf("premise: u.Hostname() = %q; want %q", got, tc.rawHost)
			}
			decoded, derr := url.PathUnescape(tc.rawHost)
			if derr != nil || decoded != tc.decodedHost {
				t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want %q", tc.rawHost, decoded, derr, tc.decodedHost)
			}
			if baseURLHostCarriesCredential(tc.rawHost) {
				t.Fatalf("premise broken: the raw host %q classifies, so the row is not a benign witness", tc.rawHost)
			}
			if baseURLHostCarriesCredential(tc.decodedHost) {
				t.Fatalf("premise broken: the decoded host %q classifies, so the row is not a benign witness", tc.decodedHost)
			}
			if got := BaseURLCarriesCredential(tc.url); got != false {
				t.Errorf("BaseURLCarriesCredential(%q) = %v; want false (benign legal escape)", tc.url, got)
			}
			if err := wizardBaseURLRefused(tc.url); err != nil {
				t.Errorf("wizardBaseURLRefused(%q) = %v; want nil", tc.url, err)
			}
			if got := BaseURLCarriesCredential("  " + tc.url + "\t"); got != false {
				t.Errorf("BaseURLCarriesCredential(padded %q) = %v; want false", tc.url, got)
			}
		})
	}

	// A host whose own escapes are malformed stays raw-only. The reachable
	// spelling is the doubled one: url.Parse turns "%252" into the text "%2",
	// which url.PathUnescape rejects. The exact verdict is pinned: ACCEPTED.
	const malformedURL = "https://acme%252.example.com/v1"
	u, err := url.Parse(malformedURL)
	if err != nil {
		t.Fatalf("premise: %q must parse, or it is not a raw-only row: %v", malformedURL, err)
	}
	if got := u.Hostname(); got != "acme%2.example.com" {
		t.Fatalf("premise: u.Hostname() = %q; want %q", got, "acme%2.example.com")
	}
	if decoded, derr := url.PathUnescape("acme%2.example.com"); derr == nil {
		t.Fatalf("premise: url.PathUnescape(%q) = %q, nil; want a malformed-escape error", "acme%2.example.com", decoded)
	}
	if got := BaseURLCarriesCredential(malformedURL); got != false {
		t.Errorf("BaseURLCarriesCredential(%q) = %v; want false: a malformed escape is classified raw, exactly as before the decoded read", malformedURL, got)
	}
	if err := wizardBaseURLRefused(malformedURL); err != nil {
		t.Errorf("wizardBaseURLRefused(%q) = %v; want nil", malformedURL, err)
	}

	// A host with no escape decodes to itself, so the second read never runs and
	// every benign host keeps its verdict byte-identically.
	const plainURL = "https://mycompany2024gateway.example.com/v1"
	u, err = url.Parse(plainURL)
	if err != nil {
		t.Fatalf("premise: %q must parse: %v", plainURL, err)
	}
	if decoded, derr := url.PathUnescape(u.Hostname()); derr != nil || decoded != u.Hostname() {
		t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want the host unchanged", u.Hostname(), decoded, derr)
	}
	if got := BaseURLCarriesCredential(plainURL); got != false {
		t.Errorf("BaseURLCarriesCredential(%q) = %v; want false", plainURL, got)
	}

	// The decode function choice: url.PathUnescape, not url.QueryUnescape. A host
	// is not form-encoded, so a literal "+" stays the base64 rune it is; the form
	// decoder would turn it into a space.
	const plusHost = "abcd1234+efgh%2b5678.example.com"
	pathDecoded, pathErr := url.PathUnescape(plusHost)
	if pathErr != nil || pathDecoded != "abcd1234+efgh+5678.example.com" {
		t.Fatalf("premise: url.PathUnescape(%q) = %q, %v; want %q", plusHost, pathDecoded, pathErr, "abcd1234+efgh+5678.example.com")
	}
	queryDecoded, queryErr := url.QueryUnescape(plusHost)
	if queryErr != nil || queryDecoded != "abcd1234 efgh+5678.example.com" {
		t.Fatalf("premise: url.QueryUnescape(%q) = %q, %v; want %q (the form decoder turns the literal + into a space)", plusHost, queryDecoded, queryErr, "abcd1234 efgh+5678.example.com")
	}
}
