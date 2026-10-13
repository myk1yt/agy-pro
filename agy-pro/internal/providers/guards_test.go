package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// B1 — ProbeEndpoint must never embed a key-echoing upstream error body raw.
// ---------------------------------------------------------------------------

// TestProbeEndpoint_RedactsKeyEchoing401Body is the B1 transcript test: an
// OpenAI-compatible gateway that echoes the offending bearer key in its 401
// body must not have that key reach the wizard transcript. Only the first 5
// characters may appear, and the redacted form must be visible.
func TestProbeEndpoint_RedactsKeyEchoing401Body(t *testing.T) {
	planted := "sk-PROBELEAK-0ddba11cade42024"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"Incorrect API key provided: %s","type":"invalid_request_error"}}`, planted)
	}))
	defer server.Close()

	_, err := ProbeEndpoint(server.URL, planted, "test-model")
	if err == nil {
		t.Fatal("expected 401 error")
	}
	assertNoLeak(t, err.Error(), planted)
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("status code lost from error: %v", err)
	}
}

// TestProbeEndpoint_RedactsBearerEcho covers a body that echoes the key under
// a "Bearer" prefix (still the probe key, different shape).
func TestProbeEndpoint_RedactsBearerEcho(t *testing.T) {
	planted := "sk-PROBELEAK-b1946ac92492d234"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `unauthorized: bearer token %s rejected`, planted)
	}))
	defer server.Close()

	_, err := ProbeEndpoint(server.URL, planted, "test-model")
	if err == nil {
		t.Fatal("expected 403 error")
	}
	assertNoLeak(t, err.Error(), planted)
}

// ---------------------------------------------------------------------------
// B4 — apiKeyEnv: short env-name-shaped credential values must not pass as
// environment-variable names (isLikelyAPIKey bypass).
// ---------------------------------------------------------------------------

func TestValidateAPIKeyEnvName_AKIARejected(t *testing.T) {
	// AWS access key ID: AKIA + 16 = 20 chars, env-name-shaped charset, no
	// hyphen/dot, under the old 24-char length rule — the documented bypass.
	akia := "AKIAABCDEFGHIJKLMNOP"
	if !envNameRe.MatchString(akia) {
		t.Fatal("fixture no longer exercises the bypass class")
	}
	if err := ValidateAPIKeyEnvName(akia); err == nil {
		t.Fatal("AKIA-style credential accepted as an apiKeyEnv NAME")
	} else if strings.Contains(err.Error(), akia) {
		t.Errorf("rejection echoes the credential: %v", err)
	}
	// Same class through the registry boundary.
	reg := &Registry{}
	err := UpsertProvider(reg, Provider{Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: akia})
	if err == nil {
		t.Fatal("UpsertProvider accepted an AKIA-style apiKeyEnv")
	}
	if strings.Contains(err.Error(), akia) {
		t.Errorf("UpsertProvider rejection echoes the credential: %v", err)
	}
	if len(reg.Providers) != 0 {
		t.Errorf("failed upsert mutated the registry: %+v", reg.Providers)
	}
}

func TestValidateAPIKeyEnvName_HintCatalogAccepted(t *testing.T) {
	// A known hint-catalog name is accepted even when not exported yet (the
	// catalog proves it is a name, not a value).
	for _, name := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "NW_API_KEY"} {
		if err := ValidateAPIKeyEnvName(name); err != nil {
			t.Errorf("catalog name %q rejected: %v", name, err)
		}
	}
}

func TestValidateAPIKeyEnvName_LookupEnvAccepted(t *testing.T) {
	// A name that resolves in the process environment is accepted even when
	// it is not in the catalog.
	t.Setenv("MY_CUSTOM_LONG_KEY_NAME_HERE", "sk-resolved-value-1234567890")
	if err := ValidateAPIKeyEnvName("MY_CUSTOM_LONG_KEY_NAME_HERE"); err != nil {
		t.Errorf("resolving env name rejected: %v", err)
	}
}

func TestValidateAPIKeyEnvName_KeyValuesRejected(t *testing.T) {
	for _, secret := range []string{
		"sk-verysecretkey-0123456789abcdef",
		"ghp_0123456789abcdef0123",
		"a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5",
	} {
		err := ValidateAPIKeyEnvName(secret)
		if err == nil {
			t.Errorf("key value %q accepted as apiKeyEnv", MaskSecret(secret))
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("rejection echoes the key value: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// B5 — provider names are a shell-safe token, nothing else.
// ---------------------------------------------------------------------------

func TestValidateProviderName_Charset(t *testing.T) {
	for _, ok := range []string{"acme", "openai", "my-provider", "prov.v2", "a_b", "x", "A1"} {
		if err := ValidateProviderName(ok); err != nil {
			t.Errorf("valid name %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"",
		"-leading",
		".dotfirst",
		"has space",
		"foo/bar",
		"foo;rm -rf",
		"$(whoami)",
		"foo`id`",
		"foo&&bar",
		"foo|bar",
		"foo'quote",
		"foo\"quote",
		"foo\nbar",
		strings.Repeat("a", 65),
	} {
		if err := ValidateProviderName(bad); err == nil {
			t.Errorf("invalid name %q accepted", bad)
		}
	}
}

func TestUpsertProvider_RejectsShellMetacharNames(t *testing.T) {
	reg := &Registry{}
	err := UpsertProvider(reg, Provider{Name: "x; curl evil.sh", BaseURL: "https://x.example/v1"})
	if err == nil {
		t.Fatal("shell-metachar provider name accepted by UpsertProvider")
	}
	if strings.Contains(err.Error(), "curl") {
		t.Errorf("rejection echoes the operand raw: %v", err)
	}
	if len(reg.Providers) != 0 {
		t.Errorf("failed upsert mutated the registry: %+v", reg.Providers)
	}
	// Rename shares the same gate.
	reg = &Registry{Providers: []Provider{{Name: "old", BaseURL: "https://old.example/v1"}}}
	if err := RenameProvider(reg, "old", "new name"); err == nil {
		t.Fatal("shell-metachar rename target accepted")
	}
}

// ---------------------------------------------------------------------------
// B2 — every echoed operand goes through MaskOperand / MaskSecret.
// ---------------------------------------------------------------------------

// TestMaskOperand_U8 is the U8 contract for operand echo: nothing beyond the
// first 5 characters of a credential-shaped operand is ever rendered, while
// benign operands (env names, short tokens, URLs, paths) stay readable so
// error messages remain actionable.
func TestMaskOperand_U8(t *testing.T) {
	secret := "sk-verysecretkey-0123456789abcdef"
	got := MaskOperand(secret)
	if got != "sk-ve****" {
		t.Errorf("MaskOperand(secret) = %q; want %q", got, "sk-ve****")
	}
	assertNoLeak(t, got, secret)

	if got := MaskOperand("AKIAABCDEFGHIJKLMNOP"); got != "AKIAA****" {
		t.Errorf("MaskOperand(AKIA...) = %q; want AKIAA****", got)
	}
	for _, benign := range []string{"nowrite", "acme", "my-provider", "native-seat-concurrency", "OPENAI_API_KEY", "https://api.openai.com/v1"} {
		if got := MaskOperand(benign); got != benign {
			t.Errorf("MaskOperand(%q) = %q; want unchanged", benign, got)
		}
	}
}

// ---------------------------------------------------------------------------
// D1-helper — RedactEchoLine masks secret-shaped quoted segments inside a
// larger sentence (stdlib flag errors embed the bad value in quotes).
// ---------------------------------------------------------------------------

// TestRedactEchoLine_MasksQuotedSecrets: a flag error line such as
// `invalid value "sk-..." for flag -timeout` must have its quoted segment
// masked through MaskOperand while the surrounding sentence is untouched.
func TestRedactEchoLine_MasksQuotedSecrets(t *testing.T) {
	sk := "sk-flagleak-0123456789abcdef"
	line := `invalid value "` + sk + `" for flag -timeout`
	got := RedactEchoLine(line)
	want := `invalid value "sk-fl****" for flag -timeout`
	if got != want {
		t.Errorf("RedactEchoLine(sk plant) = %q; want %q", got, want)
	}
	assertNoLeak(t, got, sk)

	// AWS-shaped plant (contiguous run, no known prefix) inside quotes.
	aws := "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	line = `invalid value "` + aws + `" for flag -model`
	got = RedactEchoLine(line)
	want = `invalid value "wJalr****" for flag -model`
	if got != want {
		t.Errorf("RedactEchoLine(AWS plant) = %q; want %q", got, want)
	}
	assertNoLeak(t, got, aws)

	// Multiple quoted segments on one line: each is masked independently.
	got = RedactEchoLine(`flags "` + sk + `" and "` + aws + `" rejected`)
	if strings.Contains(got, sk) || strings.Contains(got, aws) {
		t.Errorf("RedactEchoLine leaks a plant: %q", got)
	}
	if !strings.Contains(got, `"sk-fl****"`) || !strings.Contains(got, `"wJalr****"`) {
		t.Errorf("RedactEchoLine did not mask both segments: %q", got)
	}
}

// TestRedactEchoLine_BenignLinesUnchanged: usage text, flag values, and
// quoted benign operands must come back byte-identical.
func TestRedactEchoLine_BenignLinesUnchanged(t *testing.T) {
	for _, benign := range []string{
		"usage: agy [flags]",
		"--tools read,search",
		`invalid value "abc" for flag -timeout`,
		`provider "my-provider" not found`,
		"no quotes at all",
		"",
	} {
		if got := RedactEchoLine(benign); got != benign {
			t.Errorf("RedactEchoLine(%q) = %q; want unchanged", benign, got)
		}
	}
}

// ---------------------------------------------------------------------------
// D3-tail — the probe-skip Detail echoes the apiKeyEnv name, so a
// credential parked in that slot (legacy registry) must render masked.
// ---------------------------------------------------------------------------

// TestAuditProviderKeys_UnresolvedDetailMasksKeyShapedEnv: an unresolved
// provider's skip Detail names the env variable it looked for. A legacy
// registry can hold a credential in the apiKeyEnv slot, so that name routes
// through MaskOperand (first 5 + ****); a conventional env name stays
// readable, and no probe runs for an unresolved key.
func TestAuditProviderKeys_UnresolvedDetailMasksKeyShapedEnv(t *testing.T) {
	store := newMockEnvStore()
	akia := "AKIAIOSFODNN7EXAMPLE"
	reg := &Registry{Providers: []Provider{
		{Name: "legacy", BaseURL: "https://legacy.example/v1", APIKeyEnv: akia},
		{Name: "clean", BaseURL: "https://clean.example/v1", APIKeyEnv: "CLEAN_API_KEY"},
	}}
	audits := AuditProviderKeys(reg, AuditOptions{
		Probe: func(baseURL, apiKey string) KeyProbeResult {
			t.Error("probe must not run for an unresolved key")
			return KeyProbeResult{}
		},
		Store: store,
	})
	if len(audits) != 2 {
		t.Fatalf("expected 2 audits, got %d", len(audits))
	}
	legacy := audits[0]
	if legacy.Probe.Status != ProbeSkipped {
		t.Fatalf("legacy probe = %+v; want skipped", legacy.Probe)
	}
	assertNoLeak(t, legacy.Probe.Detail, akia)
	if want := "no usable key found for " + MaskOperand(akia); legacy.Probe.Detail != want {
		t.Errorf("skip detail = %q; want %q", legacy.Probe.Detail, want)
	}
	clean := audits[1]
	if !strings.Contains(clean.Probe.Detail, "CLEAN_API_KEY") {
		t.Errorf("conventional env name must stay readable in the skip detail: %q", clean.Probe.Detail)
	}
}

// ---------------------------------------------------------------------------
// C3 — MaskOperand heuristic carve-outs (security re-review, arbiter-ruled
// FIX): the path/URL exemption used to exempt ANY string containing "/" or
// "\", so AWS-secret-shaped values (about half of real AWS secrets carry "/")
// echoed in full; and the length mask used to catch every >=20/24-rune token,
// mangling benign model IDs such as claude-3-5-sonnet-20241022. D7 then
// lowered the contiguous-run threshold to the AKIA-class length of 16 runes
// and moved it ahead of the env-name/provider-name exemptions, so a 16-23
// rune bare-alnum credential (ABCDEFGHIJKLMNOPQRST) no longer prints raw.
// ---------------------------------------------------------------------------

// TestMaskOperand_C3_AWSSecretWithSlashMasked: a "/" alone must no longer read
// as a path. The classic AWS documentation example secret carries two.
func TestMaskOperand_C3_AWSSecretWithSlashMasked(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	got := MaskOperand(secret)
	if got != "wJalr****" {
		t.Errorf("MaskOperand(AWS secret with /) = %q; want %q", got, "wJalr****")
	}
	assertNoLeak(t, got, secret)
}

// TestMaskOperand_C3_AWSSecretWithoutSlashMasked: the no-separator shape was
// already masked; the C3 narrowing must not regress it.
func TestMaskOperand_C3_AWSSecretWithoutSlashMasked(t *testing.T) {
	secret := "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	got := MaskOperand(secret)
	if got != "wJalr****" {
		t.Errorf("MaskOperand(AWS secret without /) = %q; want %q", got, "wJalr****")
	}
	assertNoLeak(t, got, secret)
}

// TestMaskOperand_C3_KnownShapesStillMasked: prefix- and shape-matched
// credentials keep rendering first-5 + **** (U8 contract unchanged).
func TestMaskOperand_C3_KnownShapesStillMasked(t *testing.T) {
	for _, secret := range []string{
		"sk-verysecretkey-0123456789abcdef",
		"ghp_A16CharTokenHere1234567890abcd",
		"AKIAIOSFODNN7EXAMPLE",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	} {
		got := MaskOperand(secret)
		if got != MaskSecret(secret) {
			t.Errorf("MaskOperand(%q...) = %q; want %q", secret[:5], got, MaskSecret(secret))
		}
		assertNoLeak(t, got, secret)
	}
}

// TestMaskOperand_C3_ModelIDsStayReadable: the length mask now applies only to
// CONTIGUOUS secret-shaped runs, so separator-carrying model IDs (>=24 runes)
// survive for the wizard's copy-paste completion hints.
func TestMaskOperand_C3_ModelIDsStayReadable(t *testing.T) {
	for _, benign := range []string{
		"claude-3-5-sonnet-20241022",
		"gemini-2.5-flash-preview-05-20",
		"deepseek-v3-0324-671b-full",
		"qwen3-8b-instruct-262144-context",
		"gpt-4o",
	} {
		if got := MaskOperand(benign); got != benign {
			t.Errorf("MaskOperand(%q) = %q; want unchanged", benign, got)
		}
	}
}

// TestMaskOperand_C3_LocationsStayReadable: only LOCATION-shaped strings keep
// the exemption — a URL scheme, an absolute path, or a leading ./ ../ .\ ..\ .
func TestMaskOperand_C3_LocationsStayReadable(t *testing.T) {
	for _, loc := range []string{
		"https://host/path",
		"http://127.0.0.1:9/v1",
		`C:\Users\test\AppData\Local\agy-pro\bin\agy.exe`,
		`C:/Users/test/AppData/Local/agy-pro/bin/agy.exe`,
		"/usr/local/bin/agy-pro",
		"./relative/script.sh",
		"../parent/script.sh",
		`.\relative\script.cmd`,
		`..\parent\script.cmd`,
	} {
		if got := MaskOperand(loc); got != loc {
			t.Errorf("MaskOperand(%q) = %q; want unchanged", loc, got)
		}
	}
}

// TestMaskOperand_C3_BoundaryLengths: the contiguous-run mask fires at 16
// runes (D7, AKIA-class length): a 15-rune contiguous run stays readable, a
// 16-rune one is masked — including the bare UPPER alnum shape that used to
// pass as an env-var name (the live `providers test
// ABCDEFGHIJKLMNOPQRST` class). Separator-carrying tokens (model IDs, env
// names, paths) never hit the length mask at any length.
func TestMaskOperand_C3_BoundaryLengths(t *testing.T) {
	if got := MaskOperand("abcdefghij12345"); got != "abcdefghij12345" {
		t.Errorf("15-rune contiguous run = %q; want unchanged", got)
	}
	secret16 := "abcdefghij123456"
	if got := MaskOperand(secret16); got != "abcde****" {
		t.Errorf("16-rune contiguous run = %q; want %q", got, "abcde****")
	}
	assertNoLeak(t, MaskOperand(secret16), secret16)

	// The D7 live class: a bare UPPER alnum run that reads as an env-var name
	// but is an access-key-ID-length credential.
	upper := "ABCDEFGHIJKLMNOPQRST"
	if got := MaskOperand(upper); got != "ABCDE****" {
		t.Errorf("20-rune UPPER alnum run = %q; want %q", got, "ABCDE****")
	}
	assertNoLeak(t, MaskOperand(upper), upper)

	// The former 20-24 alnum readable window is gone: 23 runes masks now.
	if got := MaskOperand("abcdefghij1234567890123"); got != "abcde****" {
		t.Errorf("23-rune alnum run = %q; want %q", got, "abcde****")
	}

	long := "claude-3-5-sonnet-20241022-with-a-very-long-suffix-indeed"
	if got := MaskOperand(long); got != long {
		t.Errorf("long separator-carrying token = %q; want unchanged", got)
	}
}

func TestRegistryOps_ErrorsMaskKeyOperands(t *testing.T) {
	secret := "sk-rename-leak-0123456789abcd"
	reg := &Registry{}
	err := RenameProvider(reg, secret, "x")
	if err == nil {
		t.Fatal("rename of a missing provider should fail")
	}
	assertNoLeak(t, err.Error(), secret)

	err = DeleteProvider(reg, secret)
	if err == nil {
		t.Fatal("delete of a missing provider should fail")
	}
	assertNoLeak(t, err.Error(), secret)

	// registry_ops "already exists" site: the NEW name is the pasted key
	// (charset-valid, so it reaches the collision check).
	reg2 := &Registry{Providers: []Provider{
		{Name: "other", BaseURL: "https://other.example/v1"},
		{Name: secret, BaseURL: "https://taken.example/v1"},
	}}
	err = RenameProvider(reg2, "other", secret)
	if err == nil {
		t.Fatal("rename onto an existing name should fail")
	}
	assertNoLeak(t, err.Error(), secret)
}

func TestResolveRepairTargets_MasksKeyOperand(t *testing.T) {
	secret := "sk-XYZZYQUUX-89abcdef01234567"
	reg := &Registry{Providers: []Provider{{Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY"}}}
	_, err := ResolveRepairTargets(reg, secret)
	if err == nil {
		t.Fatal("unknown repair target should fail")
	}
	assertNoLeak(t, err.Error(), secret)

	// The "declares no API key variable" site: a provider named like the key.
	reg2 := &Registry{Providers: []Provider{{Name: secret, BaseURL: "https://taken.example/v1"}}}
	_, err = ResolveRepairTargets(reg2, secret)
	if err == nil {
		t.Fatal("provider without a key env must be a repair-target error")
	}
	assertNoLeak(t, err.Error(), secret)
}

// TestValidateBaseURL_ParseErrorNoKeyEcho: a misdirected key paste in the
// baseURL slot must not ride along through url.Error's embedded operand.
func TestValidateBaseURL_ParseErrorNoKeyEcho(t *testing.T) {
	secret := "sk-baseurl-leak-0123456789ab"
	// A value that makes url.Parse fail while embedding the operand text.
	bad := "http://[::1]:namedport/" + secret
	err := validateBaseURL(bad)
	if err == nil {
		t.Skip("url.Parse accepted the fixture; cannot exercise the error path")
	}
	assertNoLeak(t, err.Error(), secret)
}

// TestSecretInterruptSentinel pins the B6 error contract: the interrupt
// signal is distinct from the console-fallback signal and names the abort.
func TestSecretInterruptSentinel(t *testing.T) {
	if errors.Is(errNotConsole, errSecretInterrupted) {
		t.Error("fallback signal must not alias the interrupt signal")
	}
	if !strings.Contains(errSecretInterrupted.Error(), "aborted") {
		t.Errorf("interrupt error message = %q", errSecretInterrupted)
	}
}

// ---------------------------------------------------------------------------
// F-alpha — the unified seed-eligibility rule (SeedEligible) and the shared
// composite-error masker (MaskSentence). Both twins (cmd/agy/masking.go and
// internal/fanout/cli.go) delegate here; the pinned table below is the
// acceptance contract for the rule, and the twin E2 tests mirror it.
// ---------------------------------------------------------------------------

// TestSeedEligible_PinnedTable pins the F-alpha acceptance cases. Seeds:
// classifier-flagged operands (sk-, AKIA, the AWS-secret contiguous run) and
// the decorated class whose separators defeat the contiguous-run heuristic
// ("native: <aws>" with whitespace, "1-<ASIA>"), plus the bare uppercase run
// the classifier flags on length. Readable: lowercase hash/model/filename
// shapes, dotted config names, and every location-shaped operand.
func TestSeedEligible_PinnedTable(t *testing.T) {
	for _, op := range []string{
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"native: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"1-ASIAIOSFODNN7EXAMPLE",
		"ABCDEFGHIJKLMNOPQRST",
		"sk-verysecretkey-0123456789abcdef",
		"AKIAIOSFODNN7EXAMPLE",
	} {
		if !SeedEligible(op) {
			t.Errorf("SeedEligible(%.20q...) = false; want true", op)
		}
	}
	for _, op := range []string{
		"native:anthropic/claude-sonnet-4-5/x",
		"prompts/3f2a8b9c1d2e3f4a.md",
		"config.dev.override",
		"MyReport2024FINAL.pdf",
		"claude-3.5.sonnet",
		"https://host/p",
		`C:\x\y`,
		"./keys/x.txt",
	} {
		if SeedEligible(op) {
			t.Errorf("SeedEligible(%q) = true; want false", op)
		}
	}
}

// TestSeedEligible_JWTShape: 3-segment tokens whose segments are blob-length
// seed (the shape covers headers the classifier's eyJ rule cannot see);
// short-segment dotted names stay readable (the r5 N1 over-mask class).
func TestSeedEligible_JWTShape(t *testing.T) {
	for _, op := range []string{
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"abcdefghijklmnop.qrstuvwxyz0123.456789",
	} {
		if !SeedEligible(op) {
			t.Errorf("SeedEligible(JWT %.20q...) = false; want true", op)
		}
	}
	for _, op := range []string{"config.dev.override", "claude-3.5.sonnet", "v1.2.3"} {
		if SeedEligible(op) {
			t.Errorf("SeedEligible(%q) = true; want false", op)
		}
	}
}

// TestNewRedactor_AWSKeyIDTwinsMasked: the generic redactor pattern covers the
// ASIA twin of the AKIA access-key-id class (F3) inside sentences, and the
// classifier still renders a bare id as the first-5 readable mask (U8).
func TestNewRedactor_AWSKeyIDTwinsMasked(t *testing.T) {
	redact := NewRedactor()
	for _, key := range []string{"AKIAIOSFODNN7EXAMPLE", "ASIAIOSFODNN7EXAMPLE"} {
		got := redact("key " + key + " rejected")
		if strings.Contains(got, key) {
			t.Errorf("redactor leaks the AWS key id %q:\n%s", key, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("redactor output missing the marker for %q:\n%s", key, got)
		}
	}
	if got := MaskOperand("ASIAIOSFODNN7EXAMPLE"); got != "ASIAI****" {
		t.Errorf("MaskOperand(ASIA id) = %q; want %q", got, "ASIAI****")
	}
}

// TestMaskSentence_MasksPlantedOperand: a secret embedded in a composite
// sentence is masked wherever the sentence echoes the operand, while the
// sentence's own wording survives.
func TestMaskSentence_MasksPlantedOperand(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	msg := "open " + secret + ": The system cannot find the file specified."
	got := MaskSentence(msg, secret)
	assertNoLeak(t, got, secret)
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("MaskSentence output missing the marker:\n%s", got)
	}
	if !strings.Contains(got, "The system cannot find the file specified.") {
		t.Errorf("sentence wording lost:\n%s", got)
	}
}

// TestMaskSentence_BenignSentenceUnchanged: with no eligible operand the
// sentence falls back to MaskOperand, so benign operands stay readable.
func TestMaskSentence_BenignSentenceUnchanged(t *testing.T) {
	op := "native:anthropic/claude-sonnet-4-5/x"
	msg := `seat 1: "` + op + `": native model id must not contain "/"`
	if got := MaskSentence(msg, op); got != msg {
		t.Errorf("MaskSentence changed a benign sentence:\n got %q\nwant %q", got, msg)
	}
}

// TestMaskSentence_SeedsTrimmedForm: a parser may echo the operand without
// surrounding whitespace, so the trimmed twin of every seed is armed too.
func TestMaskSentence_SeedsTrimmedForm(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	msg := "open " + secret + ": no such file"
	got := MaskSentence(msg, "  "+secret+"  ")
	assertNoLeak(t, got, secret)
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("trimmed-form seed missing:\n%s", got)
	}
}

// TestMaskSentence_DecoratedOperandMasked: the decorated class
// ("1-<aws>", "native:<aws>/x", "native: <aws>") seeds through the squash rule
// so the composite sentence cannot echo the inner secret raw.
func TestMaskSentence_DecoratedOperandMasked(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	for _, op := range []string{"1-" + secret, "native:" + secret + "/x", "native: " + secret} {
		msg := fmt.Sprintf("invalid selection token %q", op)
		got := MaskSentence(msg, op)
		assertNoLeak(t, got, secret)
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("decorated operand %q must carry the marker:\n%s", op, got)
		}
	}
}

// TestMaskSentence_NoSeedFallbackAppliesGenericPatterns: with no eligible
// operand the fallback still masks generic credential patterns in the
// sentence (the seeded path applies the same patterns via NewRedactor).
func TestMaskSentence_NoSeedFallbackAppliesGenericPatterns(t *testing.T) {
	got := MaskSentence("unauthorized: bearer sk-planted-c2-sweep-key-0123456789abcdef rejected", "bogus")
	if strings.Contains(got, "sk-planted-c2-sweep-key-0123456789abcdef") {
		t.Errorf("fallback must still mask generic credential patterns:\n%s", got)
	}
}

func TestFlagSeedOperands_WholePlusTokens(t *testing.T) {
	got := FlagSeedOperands("1,native:abc", "read search")
	want := []string{"1,native:abc", "1", "native:abc", "read search", "read", "search"}
	if len(got) != len(want) {
		t.Fatalf("FlagSeedOperands = %q; want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FlagSeedOperands[%d] = %q; want %q", i, got[i], want[i])
		}
	}
	if ops := FlagSeedOperands("", "x"); len(ops) != 1 || ops[0] != "x" {
		t.Errorf("FlagSeedOperands with empty value = %q; want [x]", ops)
	}
}

func TestSeedEligible_IdentifierSegmentedReadable(t *testing.T) {
	// r6: the shipped preset model ID is separator-dense; the squash branch
	// must not seed it ( SeatLine/selection errors would render [REDACTED]).
	readable := []string{
		"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
		"native:anthropic/claude-sonnet-4-5/x",
	}
	for _, id := range readable {
		if SeedEligible(id) {
			t.Errorf("SeedEligible(%q) = true; want false (identifier class stays readable)", id)
		}
	}
	// Opaque secrets (few separators) still seed.
	seeds := []string{
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"native:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"native: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"1-ASIAIOSFODNN7EXAMPLE",
	}
	for _, s := range seeds {
		if !SeedEligible(s) {
			t.Errorf("SeedEligible(%q) = false; want true (decorated secret must seed)", s)
		}
	}
}

func TestIdentifierSegmented_ContentAware(t *testing.T) {
	// H7: real 4-5 segment org-slash IDs stay readable; a decorated secret
	// whose segments include a 16+ rune opaque run must still seed.
	readable := []string{
		"Qwen/Qwen2.5-72B-Instruct",
		"mistralai/Mistral-7B-Instruct",
		"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
	}
	for _, id := range readable {
		if SeedEligible(id) {
			t.Errorf("SeedEligible(%q) = true; want false (identifier class stays readable)", id)
		}
	}
	seeds := []string{
		"native:wJalr+XU/tnF/EMI/K7M/DENG/bPxRfiCYEXAMPLEKEY",
		"1-2-3-4-5-native:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	for _, s := range seeds {
		if !SeedEligible(s) {
			t.Errorf("SeedEligible(%q) = false; want true (decorated secret with long segment must seed)", s)
		}
	}
}

// ---------------------------------------------------------------------------
// C5/S2 — the seed class stays WIDE, the display class stays NARROW. The
// batch-I display narrowing (at most one interior space) leaked into
// SeedEligible's final branch, so a composite error that embeds a multi-word
// operand (a library wraps the raw CLI operand into the sentence) no longer
// seeded and whole-line sinks echoed the secret raw. seedShaped restores the
// pre-diff wide core; decoratedSecretShaped keeps the display narrowing.
// ---------------------------------------------------------------------------

// TestSeedShaped_WideCore_DisplayNarrowing pins the core/narrowing split:
// a >=2-space or tab/newline-carrying operand SEEDS (pre-diff parity) but
// never renders wholly masked (the display class rejects it).
func TestSeedShaped_WideCore_DisplayNarrowing(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	wide := []string{
		"here is the key " + secret,
		"the key is " + secret + " and it failed",
		"native: " + secret + " (seat 1, retry)",
		"native:\t" + secret,
		"native:\n" + secret,
	}
	for _, op := range wide {
		if !seedShaped(op) {
			t.Errorf("seedShaped(%.30q...) = false; want true (wide seed core)", op)
		}
		if !SeedEligible(op) {
			t.Errorf("SeedEligible(%.30q...) = false; want true (wide seed class, pre-diff parity)", op)
		}
		if decoratedSecretShaped(op) {
			t.Errorf("decoratedSecretShaped(%.30q...) = true; want false (display narrowing: a sentence must never render wholly masked)", op)
		}
	}
	// The whitespace-decorated single-space form stays in BOTH classes.
	single := "native: " + secret
	if !seedShaped(single) || !decoratedSecretShaped(single) {
		t.Errorf("single-space decorated form must be in both classes: seed=%v display=%v",
			seedShaped(single), decoratedSecretShaped(single))
	}
	// MaskOperand keeps the multi-word sentence readable (display narrow)
	// while MaskSentence masks the embedded secret (seed wide).
	multi := "here is the key " + secret
	if got := MaskOperand(multi); got != multi {
		t.Errorf("MaskOperand(multi-word sentence) = %q; want unchanged (display class stays narrow)", got)
	}
	got := MaskSentence("error: "+multi+" (seat 1)", multi)
	assertNoLeak(t, got, secret)
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("multi-word operand must seed the composite masker:\n%s", got)
	}
	if !strings.Contains(got, "error:") || !strings.Contains(got, "(seat 1)") {
		t.Errorf("sentence wording lost:\n%s", got)
	}
}

// TestSeedEligible_WideClass_PinnedTable is the C5/S2 acceptance table: the
// wide seed class (>=2 interior spaces, embedded bare secret) seeds;
// identifier-class model IDs and location-shaped operands never do.
func TestSeedEligible_WideClass_PinnedTable(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	for _, op := range []string{
		"here is the key " + secret,
		"invalid selection token \"" + secret + "\" rejected by the parser",
		"please export THE_KEY=" + secret + " before running",
	} {
		if !SeedEligible(op) {
			t.Errorf("SeedEligible(%.30q...) = false; want true (multi-word operand with an embedded bare secret)", op)
		}
	}
	for _, op := range []string{
		"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
		"Qwen/Qwen2.5-72B-Instruct",
		"native:anthropic/claude-sonnet-4-5/x",
		"https://host/path",
		`C:\Users\test\bin`,
		"./relative/path",
		"../parent/path",
	} {
		if SeedEligible(op) {
			t.Errorf("SeedEligible(%q) = true; want false (identifier/location class stays out of the wide rule)", op)
		}
	}
}

// TestSeedEligible_WideClass_ShortWordSentenceStaysOut pins the identifier
// guard the wide rule must not defeat: a sentence whose every segment is
// short is identifier-class and stays out of the seed rule (pre-diff
// parity), so ordinary composite errors never arm the redactor. The pin runs
// unconditionally: a guard that skipped when the fixture stopped exercising
// the identifier class could silently disable the pin exactly when the
// classifier regressed (K4).
func TestSeedEligible_WideClass_ShortWordSentenceStaysOut(t *testing.T) {
	op := "the quick brown fox jumps over the lazy dog AB"
	if SeedEligible(op) {
		t.Errorf("SeedEligible(%q) = true; want false (all-short-word sentence is identifier-class)", op)
	}
}

// ---------------------------------------------------------------------------
// S6 — FlagSeedOperands must split on newline/CR too: a value pasted with a
// trailing newline must still yield the bare token as a seed, because sinks
// echo the token without the newline.
// ---------------------------------------------------------------------------

func TestFlagSeedOperands_NewlineDecoratedValue(t *testing.T) {
	secret := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	want := "native:" + secret
	for _, v := range []string{
		want + "\n",
		want + "\r\n",
		"1," + want + "\n",
	} {
		seeds := FlagSeedOperands(v)
		found := false
		for _, s := range seeds {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("FlagSeedOperands(%.20q...) = %q; want the bare token %q", v, seeds, want)
		}
	}
	// A sink that echoes the bare token (no trailing newline) must mask.
	msg := "seat spec \"native:" + secret + "\" rejected"
	got := MaskSentence(msg, FlagSeedOperands(want+"\n")...)
	assertNoLeak(t, got, secret)
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("newline-decorated seed must mask the bare echo:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// C2 — MaskFamilyName: the case-preserved explicit family feeds the
// uppercase-requiring decorated class; the folded FamilyName() rode a
// lowercase-shaped secret raw.
// ---------------------------------------------------------------------------

func TestMaskFamilyName(t *testing.T) {
	// Key-shaped explicit families render first-5+**** (mixed case AND
	// all-lowercase forms).
	keyShaped := []struct{ family, want string }{
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalr****"},
		{"wjalrxutnfemi/k7mdeng/bpxrficyexamplekey", "wjalr****"},
		{"sk-familyplant-0123456789abcdef", "sk-fa****"},
	}
	for _, tc := range keyShaped {
		got := MaskFamilyName(Model{ID: "some-model", Family: tc.family})
		if got != tc.want {
			t.Errorf("MaskFamilyName(key-shaped family) = %q; want %q", got, tc.want)
		}
		assertNoLeak(t, got, tc.family)
	}
	// K2: a whitespace/newline-PADDED key-shaped family must not defeat the
	// classifier. The plants are separator-carrying (the contiguous-run rule
	// never sees them), and the padding is what defeated the display class:
	// its interior-space gate rejects a space-padded operand and its
	// tab/newline gate rejects a trailing newline, so before the trim these
	// echoed raw. A trailing-newline and a space-padded plant are pinned.
	for _, tc := range []struct{ family, want string }{
		{"wJalr.XUtnFEMI-K7MDENG.bPxRfiCYEXAMPLEKEY\n", "wJalr****"},
		{" wJalr.XUtnFEMI-K7MDENG.bPxRfiCYEXAMPLEKEY ", "wJalr****"},
		{" wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n", "wJalr****"},
	} {
		got := MaskFamilyName(Model{ID: "m", Family: tc.family})
		if got != tc.want {
			t.Errorf("MaskFamilyName(padded family) = %q; want %q", got, tc.want)
		}
		assertNoLeak(t, got, tc.family)
	}
	// A padded benign family renders trimmed; the padding never survives.
	if got := MaskFamilyName(Model{ID: "m", Family: " Gemini "}); got != "Gemini" {
		t.Errorf("MaskFamilyName(padded benign family) = %q; want Gemini", got)
	}
	// K1b: a LOCATION-shaped key-shaped family is not a legitimate family and
	// the slot has no readability requirement, so it still masks (before, the
	// location exemption echoed it raw).
	for _, tc := range []struct{ family, want string }{
		{"./wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY", "wJalr****"},
		{`..\wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY`, "wJalr****"},
		// L1/F-1: a URL scheme is location-shaped on every platform, so the
		// scheme-shaped key family pins the same first-5+**** as the relative
		// markers. Before batch L maskRegistryValue stripped only the leading
		// ./ \ class, so this rode raw at every sink.
		{"https://wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY", "wJalr****"},
		// Round-4 P1 (batch M): a marker-decorated JWT family masks. The
		// MaskRegistryValue mask branch dropped isJWTShapedOperand, so the
		// stripped JWT core rode raw at this sink (3f9ce5a inherited the class
		// by re-running full MaskOperand on the core).
		{"./abcDef.ghiJkl.mnopqr", "abcDe****"},
		{"../aaaabcdef.gggghijk.iiiijjjj", "aaaab****"},
		{"https://abcDef.ghiJkl.mnopqr", "abcDe****"},
		// Round-5 P1 (batch N): a non-leading scheme is not stripped for
		// rendering, but a secret-shaped tail behind the last mid-string
		// "://" masks the family (batch M's leading-only strip dropped the
		// old last-"://"-anywhere coverage, so this rode raw).
		{"note https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalr****"},
		// Batch-N addendum: the mirror head check — a secret-shaped segment
		// BEFORE the first "://" with a benign tail masks the family.
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY://x", "wJalr****"},
		// Batch O (round-6 M-1): the segment behind the mid-string "://"
		// carries its own marker; the stripped segment classifies secret and
		// the family masks (render = MaskSecret of the raw segment).
		{"note https://./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "./wJa****"},
	} {
		got := MaskFamilyName(Model{ID: "m", Family: tc.family})
		if got != tc.want {
			t.Errorf("MaskFamilyName(location-shaped family) = %q; want %q", got, tc.want)
		}
		assertNoLeak(t, got, tc.family)
	}
	// L1/F-1: a Windows-drive- or abs-path-shaped key family masks too. The
	// exact form is platform-dependent (filepath.IsAbs gates the location
	// exemption), so these pin the no-leak mask rather than a fixed string.
	for _, fam := range []string{`C:\wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY`, "/wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"} {
		got := MaskFamilyName(Model{ID: "m", Family: fam})
		if strings.Contains(got, "wJalrXUtnFEMI") {
			t.Errorf("MaskFamilyName(%q) = %q; leaks the key core", fam, got)
		}
	}
	// A location-shaped benign family renders with its markers stripped; the
	// family slot never legitimately carries a path.
	if got := MaskFamilyName(Model{ID: "m", Family: "./gemini"}); got != "gemini" {
		t.Errorf("MaskFamilyName(location-shaped benign family) = %q; want gemini", got)
	}
	// Benign explicit families keep their readable form (case included).
	for _, family := range []string{"gemini", "glm", "Gemini", "GLM"} {
		if got := MaskFamilyName(Model{ID: "m", Family: family}); got != family {
			t.Errorf("MaskFamilyName(%q) = %q; want unchanged", family, got)
		}
	}
	// Id-inferred families stay readable.
	for _, tc := range []struct{ id, want string }{
		{"gemini-2.5-flash", "gemini"},
		{"Qwen/Qwen2.5-72B-Instruct", "qwen"},
	} {
		if got := MaskFamilyName(Model{ID: tc.id}); got != tc.want {
			t.Errorf("MaskFamilyName(id %q) = %q; want %q", tc.id, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// L1/F-1 + M1 — MaskRegistryValue, the registry-slot display authority: a
// location-shaped (URL scheme, Windows drive, absolute path, relative marker)
// or padded key-shaped registry value must mask, while benign values stay
// readable. Before batch L it stripped only the leading ./\ class and did not
// trim, so a URL- or drive-shaped plant rode raw and a padded plant defeated
// the classifier at Summary().
// ---------------------------------------------------------------------------

func TestMaskRegistryValue(t *testing.T) {
	key := "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	// URL scheme and relative markers are location-shaped on every platform,
	// so they pin the same first-5+**** as a bare key (these rode raw before
	// the fix).
	for _, v := range []string{
		"https://" + key,
		"./" + key,
		`..\` + key,
	} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr****", v, got)
		}
		assertNoLeak(t, MaskRegistryValue(v), v)
	}
	// Drive- and abs-path-shaped values mask; the exact form is platform-
	// dependent (filepath.IsAbs gates the location exemption), so they pin the
	// no-leak mask. These rode raw on Windows before the fix (the old
	// TrimLeft(`./\`) never stripped a "C:" drive).
	for _, v := range []string{`C:\` + key, "C:/" + key, "/" + key, `\` + key} {
		got := MaskRegistryValue(v)
		if strings.Contains(got, "wJalrXUtnFEMI") {
			t.Errorf("MaskRegistryValue(%q) = %q; leaks the key core", v, got)
		}
		if !strings.Contains(got, "****") {
			t.Errorf("MaskRegistryValue(%q) = %q; want a mask", v, got)
		}
	}
	// Round-4 P1 (batch M): a marker-decorated JWT core must mask. The mask
	// branch classified the stripped core with operandLooksSecret ∨ seedShaped
	// ∨ redactor, dropping isJWTShapedOperand — the class 3f9ce5a inherited by
	// re-running full MaskOperand on the stripped core — so a relative- or
	// scheme-decorated JWT rode raw at every sink. The plants carry no "eyJ"
	// header, so the classifier's closed shapes cannot see them either.
	for _, tc := range []struct{ decorated, want string }{
		{"./abcDef.ghiJkl.mnopqr", "abcDe****"},
		{"../aaaabcdef.gggghijk.iiiijjjj", "aaaab****"},
		{"https://abcDef.ghiJkl.mnopqr", "abcDe****"},
		{"./aaaabcdef.gggghijk.iiiijjjj", "aaaab****"},
	} {
		if got := MaskRegistryValue(tc.decorated); got != tc.want {
			t.Errorf("MaskRegistryValue(%q) = %q; want %q (JWT class restored)", tc.decorated, got, tc.want)
		}
		assertNoLeak(t, MaskRegistryValue(tc.decorated), tc.decorated)
	}
	// Round-4 P2 (batch M): stacked markers strip to a FIXPOINT. The old
	// single drive pass left an IsAbs core ("C:\<key>") that every classifier
	// location-exempts, so "./C:\<key>", "file:///C:\<key>" and the
	// double-volume "D:\D:\<key>" each stripped one marker and rode raw.
	for _, v := range []string{`./C:\` + key, `file:///C:\` + key, `D:\D:\` + key} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr**** (stacked markers stripped to a fixpoint)", v, got)
		}
		assertNoLeak(t, MaskRegistryValue(v), v)
	}
	// A bare and a PADDED key-shaped value mask (the top trim is the M1 fix: a
	// padded plant defeated the display class's space/newline gates).
	for _, v := range []string{key, key + "\n", " " + key + " ", "\t" + key} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr****", v, got)
		}
	}
	// Benign values stay readable: marker-stripped for a location-shaped value,
	// byte-identical otherwise, and a benign URL renders scheme-stripped.
	if got := MaskRegistryValue("./gemini"); got != "gemini" {
		t.Errorf("MaskRegistryValue(./gemini) = %q; want gemini", got)
	}
	if got := MaskRegistryValue("high"); got != "high" {
		t.Errorf("MaskRegistryValue(high) = %q; want high", got)
	}
	if got := MaskRegistryValue("https://api.example.com/v1"); got != "api.example.com/v1" {
		t.Errorf("MaskRegistryValue(benign URL) = %q; want api.example.com/v1 (scheme-stripped)", got)
	}
	// Round-4 P3 (batch M): a scheme is stripped only when it LEADS the
	// value. The old stripper cut at the LAST "://" anywhere, so a benign
	// sentence that merely contains a URL was truncated to its tail.
	if got := MaskRegistryValue("see https://example.com docs"); got != "see https://example.com docs" {
		t.Errorf("MaskRegistryValue(benign sentence containing a URL) = %q; want unchanged", got)
	}
	// Round-4 LOW (batch M): a scheme-only value strips to an empty core and
	// rendered as "" before; the empty core now renders as the input.
	if got := MaskRegistryValue("https://"); got != "https://" {
		t.Errorf("MaskRegistryValue(scheme-only) = %q; want https:// (an empty core renders the input)", got)
	}
	// Round-5 P1 (batch N): a NON-LEADING scheme is not stripped for
	// rendering (M3 keeps a benign sentence containing a URL intact), but the
	// tail behind the last mid-string "://" is still classified for the mask
	// DECISION. Batch M's leading-only strip dropped the old
	// last-"://"-anywhere coverage, so a registry value with a non-leading
	// scheme plus a bare secret tail (first-5+**** at cc6a5e4) rode raw at
	// every sink at 04f1500. The render stays the batch-M core form:
	// first-5+**** of the secret-shaped tail.
	for _, v := range []string{
		"note https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"note https://wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY",
		// The remaining enumerated shapes of the round-5 P1 class, pinned as
		// direct rows (batch O): a two-word sentence head, and a value that
		// opens with the bare "://" separator (the head is empty, so the tail
		// check owns it).
		"x y://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr**** (non-leading scheme + secret tail)", v, got)
		}
		assertNoLeak(t, MaskRegistryValue(v), v)
	}
	// The fifth enumerated shape's tail is DOTTED ("example.com/<key>"): the
	// slash-and-dot decoration makes identifierSegmented see 5 segments, but
	// the longest ("bPxRfiCYEXAMPLEKEY", 18 runes) clears the 16-rune floor,
	// so the segment is NOT identifier-class and seedShaped's squash (49
	// runes, uppercase) fires. The render is therefore first-5 of the tail,
	// "examp****" — the dotted host prefix, not the key, fills the mask
	// window; no key rune beyond the window survives.
	if got := MaskRegistryValue("see https://example.com/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"); got != "examp****" {
		t.Errorf("MaskRegistryValue(dotted tail) = %q; want examp**** (seedShaped path)", got)
	}
	assertNoLeak(t, MaskRegistryValue("see https://example.com/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"), "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	// The same rule covers a scheme that survives the leading strip (a
	// doubled scheme leaves a mid-string "://" in the core) with a JWT
	// behind it, and a marker-decorated sentence whose tail is the secret.
	if got := MaskRegistryValue("https://host/https://abcDef.ghiJkl.mnopqr"); got != "abcDe****" {
		t.Errorf("MaskRegistryValue(doubled scheme + JWT tail) = %q; want abcDe****", got)
	}
	if got := MaskRegistryValue("./note https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"); got != "wJalr****" {
		t.Errorf("MaskRegistryValue(marker-decorated sentence + secret tail) = %q; want wJalr****", got)
	}
	// Batch-N addendum: the MIRROR of the tail check — the segment BEFORE the
	// first "://" — closes the sixth shape of the same class: a secret-shaped
	// head with a benign tail ("<key>://x"). MaskOperand location-exempts it
	// (contains "://"), the leading-only strip finds no marker (core == s),
	// and the tail "x" is benign, so the tail check alone left it raw at every
	// sink (it rendered masked-by-elision at cc6a5e4). Same contract as the
	// tail rule: classify the head for the mask decision only, never render
	// it; the render is first-5+**** of the head.
	for _, v := range []string{
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY://x",
		"./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY://x",
	} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr**** (secret-shaped head before \"://\")", v, got)
		}
		assertNoLeak(t, MaskRegistryValue(v), v)
	}
	// The tail path still owns the shapes whose head is benign: a
	// user:password-decorated value and a sentence both mask through the tail
	// check (regression guards for the other enumerated shapes).
	for _, v := range []string{
		"user:pass://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"note https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	} {
		if got := MaskRegistryValue(v); got != "wJalr****" {
			t.Errorf("MaskRegistryValue(%q) = %q; want wJalr**** (tail path)", v, got)
		}
		assertNoLeak(t, MaskRegistryValue(v), v)
	}
	// Batch O (round-6 M-1): a segment behind a mid-string "://" can itself
	// carry a location marker, and the marker location-exempts the RAW
	// segment while its stripped core is the bare secret — the doubly
	// decorated sub-shape rode raw at every sink (it rendered wJalr**** at
	// cc6a5e4, whose stripper both cut at the last "://" and iteratively
	// stripped markers). Each "://" segment is now classified BOTH raw and
	// marker-stripped, so these mask. The render stays MaskSecret of the RAW
	// segment: the marker characters occupy the first-5 mask window and no
	// key rune beyond it survives ("note https:///<key>" masked on Windows
	// through the raw contiguous-run arm before; the stripped arm makes the
	// render identical on Linux, closing the platform split).
	for _, tc := range []struct{ v, want string }{
		{"note https://./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "./wJa****"},
		{"note https:///wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "/wJal****"},
		{"https://x/https://C:\\wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "C:\\wJ****"},
	} {
		if got := MaskRegistryValue(tc.v); got != tc.want {
			t.Errorf("MaskRegistryValue(%q) = %q; want %q (marker-decorated \"://\" segment)", tc.v, got, tc.want)
		}
		assertNoLeak(t, MaskRegistryValue(tc.v), tc.v)
	}
	// Batch-N LOW hardening: a scheme-only value strips to an empty core and
	// was echoed whole; when the stripped scheme segment is itself
	// secret-shaped ("abcdefghijklmnop://" is a bare 16-rune credential
	// wearing a scheme costume) it masks instead of echoing the secret. A
	// benign scheme-only value ("https://") still renders unchanged (the
	// batch-M M4 pin above).
	for _, tc := range []struct{ v, want string }{
		{"abcdefghijklmnop://", "abcde****"},
		{"./abcdefghijklmnop://", "abcde****"},
		{"wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY://", "wJalr****"},
	} {
		if got := MaskRegistryValue(tc.v); got != tc.want {
			t.Errorf("MaskRegistryValue(%q) = %q; want %q (secret-shaped scheme segment masks)", tc.v, got, tc.want)
		}
		assertNoLeak(t, MaskRegistryValue(tc.v), tc.v)
	}
}

// TestStripLocationMarkers pins the marker stripper directly, platform-
// independently: every location marker (URL scheme, Windows drive, absolute
// path, relative marker) is removed, and a marker-free value is unchanged.
func TestStripLocationMarkers(t *testing.T) {
	key := "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	cases := []struct{ in, want string }{
		{"https://" + key, key},
		{"http://" + key, key},
		{"https://api.example.com/v1", "api.example.com/v1"},
		{`C:\` + key, key},
		{"C:/" + key, key},
		{"/" + key, key},
		{`\` + key, key},
		{"./" + key, key},
		{"../" + key, key},
		{`.\` + key, key},
		{`..\` + key, key},
		// Round-4 P2 (batch M): stacked markers reduce to the bare core only
		// when the passes repeat to a fixpoint. The old single drive pass left
		// "C:\<key>" (an IsAbs core every classifier location-exempts) for the
		// first two, and stripped only the first volume of the double-volume
		// form.
		{`./C:\` + key, key},
		{"file:///C:\\" + key, key},
		{`D:\D:\` + key, key},
		// Round-4 P3 (batch M): a scheme is stripped only when it leads the
		// value; a sentence that merely contains a URL is unchanged (the old
		// stripper cut at the last "://" anywhere and truncated it).
		{"see https://example.com docs", "see https://example.com docs"},
		// Round-4 LOW (batch M): a scheme-only value strips to an empty core;
		// MaskRegistryValue renders such a value as the input.
		{"https://", ""},
		// Batch N: the stripper stays PURE for a secret-shaped scheme-only
		// value too — it still returns the empty core, and the scheme-segment
		// classification that masks "abcdefghijklmnop://" lives at the render
		// authority (MaskRegistryValue), pinned in TestMaskRegistryValue.
		{"abcdefghijklmnop://", ""},
		{key, key},
		{"./gemini", "gemini"},
		{"high", "high"},
	}
	for _, tc := range cases {
		if got := stripLocationMarkers(tc.in); got != tc.want {
			t.Errorf("stripLocationMarkers(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// S4 — whole-object report masking: a nested Target.Provider/Model
// serialized into summary.json or --json must not leak a key-shaped value
// in any string field, while benign values stay readable.
// ---------------------------------------------------------------------------

func TestMaskProviderForReport_KeyShapedMasked(t *testing.T) {
	keyName := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	p := Provider{
		Name:      keyName,
		BaseURL:   "https://legacy.example/v1",
		APIKeyEnv: "AKIAIOSFODNN7EXAMPLE",
		Models: []Model{{
			ID:     "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY",
			Family: "native:" + keyName,
			Tags:   []string{"tools", "sk-tagplant-0123456789abcdef"},
			Capabilities: Capabilities{
				SupportsTools:   true,
				ReasoningEffort: "high",
				AllowedTools:    []string{"read_file", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"},
			},
		}},
	}
	got := MaskProviderForReport(p)
	if got.Name != "wJalr****" {
		t.Errorf("Name = %q; want wJalr****", got.Name)
	}
	if got.APIKeyEnv != "AKIAI****" {
		t.Errorf("APIKeyEnv = %q; want AKIAI****", got.APIKeyEnv)
	}
	if got.BaseURL != p.BaseURL {
		t.Errorf("BaseURL = %q; want unchanged (https URLs stay readable)", got.BaseURL)
	}
	m := got.Models[0]
	if m.ID != "wJalr****" {
		t.Errorf("Model.ID = %q; want wJalr****", m.ID)
	}
	if m.Family != "nativ****" {
		t.Errorf("Model.Family = %q; want nativ**** (case-preserved family feeds the decorated class)", m.Family)
	}
	if m.Tags[0] != "tools" || m.Tags[1] != "sk-ta****" {
		t.Errorf("Tags = %q; want [tools sk-ta****]", m.Tags)
	}
	if m.Capabilities.AllowedTools[0] != "read_file" || m.Capabilities.AllowedTools[1] != "wJalr****" {
		t.Errorf("AllowedTools = %q; want [read_file wJalr****]", m.Capabilities.AllowedTools)
	}
	if m.Capabilities.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q; want unchanged", m.Capabilities.ReasoningEffort)
	}
	// The caller's registry value is not mutated.
	if p.Name != keyName || p.APIKeyEnv != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("MaskProviderForReport mutated the input provider: %+v", p)
	}
	if p.Models[0].ID != "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY" || p.Models[0].Tags[1] != "sk-tagplant-0123456789abcdef" {
		t.Errorf("MaskProviderForReport mutated the input model: %+v", p.Models[0])
	}
}

func TestMaskProviderForReport_BenignUnchanged(t *testing.T) {
	p := Provider{
		Name:      "google",
		BaseURL:   "https://api.openai.com/v1",
		APIKeyEnv: "OPENAI_API_KEY",
		Models: []Model{{
			ID:           "gemini-2.5-flash",
			Tags:         []string{"general", "tools"},
			Capabilities: Capabilities{ReasoningEffort: "high", AllowedTools: []string{"read_file", "list_dir"}},
		}},
	}
	got := MaskProviderForReport(p)
	if got.Name != "google" || got.BaseURL != p.BaseURL || got.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("benign provider fields changed: %+v", got)
	}
	m := got.Models[0]
	// K3: an empty explicit family stays empty — the id-inferred FamilyName()
	// is no longer fabricated into the report copy, so the serialized model
	// keeps its omitempty shape (summary.json must not gain a family key the
	// registry never declared).
	if m.ID != "gemini-2.5-flash" || m.Family != "" {
		t.Errorf("benign model id/family changed: %+v", m)
	}
	if raw, err := json.Marshal(m); err != nil {
		t.Fatalf("marshal masked model: %v", err)
	} else if strings.Contains(string(raw), `"family"`) {
		t.Errorf("masked model serialized a fabricated family key (omitempty shape broken): %s", raw)
	}
	if m.Tags[0] != "general" || m.Tags[1] != "tools" {
		t.Errorf("benign tags changed: %q", m.Tags)
	}
	if m.Capabilities.AllowedTools[0] != "read_file" || m.Capabilities.ReasoningEffort != "high" {
		t.Errorf("benign capability fields changed: %+v", m.Capabilities)
	}
	// An empty provider round-trips with a nil model slice.
	empty := MaskProviderForReport(Provider{})
	if empty.Name != "" || empty.Models != nil {
		t.Errorf("empty provider = %+v; want zero value", empty)
	}
}

// L1/F-2 — MaskModelForReport routes the reasoningEffort through the
// registry-slot display authority (MaskRegistryValue), not plain MaskOperand,
// so a location-shaped key-shaped effort masks in the report copy instead of
// riding raw under MaskOperand's location exemption. Before batch L this field
// serialized raw into summary.json / --json.
func TestMaskModelForReport_LocationShapedEffortMasked(t *testing.T) {
	key := "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
	for _, effort := range []string{"https://" + key, `C:\` + key, "/" + key, "./" + key} {
		m := MaskModelForReport(Model{ID: "m", Capabilities: Capabilities{SupportsThinking: true, ReasoningEffort: effort}})
		got := m.Capabilities.ReasoningEffort
		if strings.Contains(got, "wJalrXUtnFEMI") {
			t.Errorf("MaskModelForReport(effort %q) = %q; leaks the key core", effort, got)
		}
		if got == effort {
			t.Errorf("MaskModelForReport(effort %q) = %q; want masked", effort, got)
		}
	}
	// A benign effort stays unchanged.
	m := MaskModelForReport(Model{ID: "m", Capabilities: Capabilities{SupportsThinking: true, ReasoningEffort: "high"}})
	if m.Capabilities.ReasoningEffort != "high" {
		t.Errorf("MaskModelForReport(benign effort) = %q; want high", m.Capabilities.ReasoningEffort)
	}
	// Round-4 P1 (batch M): the JWT class is restored at the mask branch, so a
	// marker-decorated JWT masks in BOTH report paths (the explicit family
	// through MaskFamilyName, the reasoning effort through MaskRegistryValue).
	// Before the fix the stripped JWT core serialized raw into the report copy.
	for _, tc := range []struct{ plant, want string }{
		{"./abcDef.ghiJkl.mnopqr", "abcDe****"},
		{"../aaaabcdef.gggghijk.iiiijjjj", "aaaab****"},
		// Round-5 P1 (batch N): the non-leading-scheme tail class masks in
		// BOTH report paths too (the explicit family through MaskFamilyName,
		// the reasoning effort through MaskRegistryValue). Before the fix the
		// stripped sentence core serialized raw into the report copy.
		{"note https://wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalr****"},
		// Batch-N addendum: the head segment before the first "://" is
		// classified the same way, so the "<key>://x" shape masks in both
		// report paths too.
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY://x", "wJalr****"},
		// Batch O (round-6 M-1): the marker-decorated segment sub-shape masks
		// in both report paths too (stripped-segment classification).
		{"note https://./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "./wJa****"},
	} {
		got := MaskModelForReport(Model{ID: "m", Family: tc.plant, Capabilities: Capabilities{SupportsThinking: true, ReasoningEffort: tc.plant}})
		if got.Family != tc.want {
			t.Errorf("MaskModelForReport(family %q) = %q; want %q (JWT class restored)", tc.plant, got.Family, tc.want)
		}
		if got.Capabilities.ReasoningEffort != tc.want {
			t.Errorf("MaskModelForReport(effort %q) = %q; want %q (JWT class restored)", tc.plant, got.Capabilities.ReasoningEffort, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// C6 — the decorated class over-masks benign >=21-squash mixed-case display
// values. EVALUATED, NOT NARROWED (report: coder-report-fix-j.md): the squash
// merges separators, so a benign filename and a real secret both present as
// one long run and no run-length condition separates them; the hard
// constraint (unmasked only when no >=16-rune alphanumeric run survives)
// already bars "MyReport2024FINALV2.pdf" (19-rune run
// "MyReport2024FINALV2"), and the remaining eligible shape
// ("Qwen3.8FlashMaxOutputX") shares no structure with the secret class that
// a documented pattern could admit without also admitting camel-case-spelled
// base64 fragments. The over-mask is display-only and fails safe, so it
// stands as an accepted trade-off. Pinned here so any future narrowing must
// redo the evaluation.
// ---------------------------------------------------------------------------

func TestDecoratedClass_BenignOverMaskTradeOffPinned(t *testing.T) {
	// Over-mask direction (accepted): benign >=21-squash mixed-case values
	// render first-5+****.
	for _, benign := range []string{"MyReport2024FINALV2.pdf", "Qwen3.8FlashMaxOutputX"} {
		if got := MaskOperand(benign); got != MaskSecret(benign) {
			t.Errorf("MaskOperand(%q) = %q; want the documented over-mask %q (C6 accepted trade-off)", benign, got, MaskSecret(benign))
		}
	}
	// No-over-mask direction: the 20-rune filename floor and the identifier
	// class keep benign values readable in both the seed and display rules.
	for _, benign := range []string{
		"MyReport2024FINAL.pdf",
		"claude-3.5.sonnet",
		"config.dev.override",
		"meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
	} {
		if SeedEligible(benign) {
			t.Errorf("SeedEligible(%q) = true; want false (below-floor/identifier benign)", benign)
		}
		if got := MaskOperand(benign); got != benign {
			t.Errorf("MaskOperand(%q) = %q; want unchanged", benign, got)
		}
	}
}
