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
// C3 — MaskOperand heuristic carve-outs (security re-review, arbiter-ruled
// FIX): the path/URL exemption used to exempt ANY string containing "/" or
// "\", so AWS-secret-shaped values (about half of real AWS secrets carry "/")
// echoed in full; and the length mask used to catch every >=20/24-rune token,
// mangling benign model IDs such as claude-3-5-sonnet-20241022.
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

// TestMaskOperand_C3_BoundaryLengths: the length mask fires at 20 runes for
// contiguous secret-shaped runs (19 stays readable); the short
// provider/model-token exemption covers <24, so the alnum boundary is 24;
// separator-carrying tokens never hit the length mask at any length.
func TestMaskOperand_C3_BoundaryLengths(t *testing.T) {
	if got := MaskOperand("abcdefghij1234567/8"); got != "abcdefghij1234567/8" {
		t.Errorf("19-rune contiguous run = %q; want unchanged", got)
	}
	secret20 := "abcdefghij12345678/9"
	if got := MaskOperand(secret20); got != "abcde****" {
		t.Errorf("20-rune contiguous run = %q; want %q", got, "abcde****")
	}
	assertNoLeak(t, MaskOperand(secret20), secret20)

	if got := MaskOperand("abcdefghij1234567890123"); got != "abcdefghij1234567890123" {
		t.Errorf("23-rune alnum token = %q; want unchanged", got)
	}
	if got := MaskOperand("abcdefghij12345678901234"); got != "abcde****" {
		t.Errorf("24-rune alnum run = %q; want %q", got, "abcde****")
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
