package providers

import (
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
