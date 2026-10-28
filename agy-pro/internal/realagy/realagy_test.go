package realagy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/providers"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveWith_EnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-agy")
	writeFile(t, real)
	got, err := ResolveWith("", real, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(real) {
		t.Errorf("ResolveWith = %q; want %q", got, real)
	}
}

func TestResolveWith_EnvMissingIsNotFound(t *testing.T) {
	_, err := ResolveWith("", filepath.Join(t.TempDir(), "nope"), "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v; want ErrNotFound", err)
	}
}

func TestResolveWith_EnvPointsToSelfIsRecursion(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "agy-pro")
	writeFile(t, self)
	_, err := ResolveWith(self, self, "", "")
	if !errors.Is(err, ErrRecursion) {
		t.Errorf("error = %v; want ErrRecursion", err)
	}
}

func TestResolveWith_PathScanPrefersExeOnWindows(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agy.exe")
	cmd := filepath.Join(dir, "agy.cmd")
	bat := filepath.Join(dir, "agy.bat")
	writeFile(t, exe)
	writeFile(t, cmd)
	writeFile(t, bat)

	got, err := ResolveWith("", "", dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runtime.GOOS == "windows" {
		if filepath.Clean(got) != filepath.Clean(exe) {
			t.Errorf("ResolveWith = %q; want agy.exe preferred over .cmd/.bat", got)
		}
	}
}

func TestResolveWith_SkipsSelfDir(t *testing.T) {
	selfDir := t.TempDir()
	self := filepath.Join(selfDir, "agy")
	writeFile(t, self)
	shimInSameDir := filepath.Join(selfDir, "agy.exe")
	writeFile(t, shimInSameDir)

	other := t.TempDir()
	realOther := filepath.Join(other, "agy.exe")
	writeFile(t, realOther)

	pathEnv := selfDir + string(os.PathListSeparator) + other
	got, err := ResolveWith(self, "", pathEnv, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(realOther) {
		t.Errorf("ResolveWith = %q; want the other dir's binary (self dir excluded)", got)
	}
}

func TestResolveWith_FallbackUsed(t *testing.T) {
	dir := t.TempDir()
	fb := filepath.Join(dir, "fallback-agy")
	writeFile(t, fb)
	got, err := ResolveWith("", "", "", fb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(fb) {
		t.Errorf("ResolveWith = %q; want fallback %q", got, fb)
	}
}

func TestResolveWith_NoneFound(t *testing.T) {
	// Isolate from the real user profile: on Windows the secondary fallback
	// (~/.gemini/bin/agy.exe) would otherwise match a machine-installed agy
	// and make this test environment-dependent.
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	_, err := ResolveWith("", "", "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v; want ErrNotFound", err)
	}
}

func TestCleanChildEnv_ScrubsFixedAndCustomNames(t *testing.T) {
	// CleanChildEnvWithBase is the pure core; feed a synthetic base so the
	// test is deterministic and platform-independent.
	base := []string{
		"PATH=/usr/bin",
		"AGY_LLM_GATEWAY_URL=http://127.0.0.1:1",
		"AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai",
		"AGY_LLM_GATEWAY_API_KEY=session-secret",
		"AGY_LLM_GATEWAY_MODELS=a,b",
		"AGY_PRO_BRIDGE=1",
		"AGY_BRIDGE=1",
		"NW_API_KEY=real-secret-value",
		"\ufeffNW_API_KEY=bom-secret-value",
		"  NW_API_KEY  =ws-secret-value",
		"AGY_PRO_ACTIVE=stale",
		"KEEP_ME=please",
	}
	got := CleanChildEnvWithBase(base, []string{"NW_API_KEY"})
	joined := strings.Join(got, "\n")

	// Must be scrubbed.
	for _, banned := range []string{
		"AGY_LLM_GATEWAY_URL", "AGY_LLM_GATEWAY_WIRE_PROTOCOL",
		"AGY_LLM_GATEWAY_API_KEY", "AGY_LLM_GATEWAY_MODELS",
		"AGY_PRO_BRIDGE", "AGY_BRIDGE",
		"real-secret-value", "bom-secret-value", "ws-secret-value",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	// No env entry may normalize to NW_API_KEY (BOM + whitespace variants).
	for _, kv := range got {
		name := kv[:strings.IndexByte(kv, '=')]
		if normalizeEnvName(name) == "nw_api_key" {
			t.Errorf("NW_API_KEY variant survived: %q", kv)
		}
	}
	// Latch set exactly once to 1.
	count := 0
	for _, kv := range got {
		if kv == "AGY_PRO_ACTIVE=1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("AGY_PRO_ACTIVE=1 must appear exactly once, got %d in:\n%s", count, joined)
	}
	// Unrelated var preserved.
	if !strings.Contains(joined, "KEEP_ME=please") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("unrelated vars must be preserved; got:\n%s", joined)
	}
}

// TestCleanChildEnv_ScrubsAmbientGeminiCredsAndRedirects pins the four names
// added to fixedScrubNames by FIX-46: an ambient GEMINI_API_KEY /
// GOOGLE_API_KEY / GOOGLE_GEMINI_BASE_URL / CLOUD_CODE_URL in the parent shell
// must never ride into a real-agy child, even when NO scrubNames argument is
// supplied (an externals-only registry declares none of them). Case and BOM
// variants are covered too; benign siblings survive.
func TestCleanChildEnv_ScrubsAmbientGeminiCredsAndRedirects(t *testing.T) {
	base := []string{
		"GEMINI_API_KEY=AIzaSyUSER-REAL-KEY",
		"GOOGLE_API_KEY=AIzaSyUSER-REAL-KEY-2",
		"gemini_api_key=AIzaSyLOWER-VARIANT",
		"google_api_key=AIzaSyLOWER-VARIANT-2",
		"GOOGLE_GEMINI_BASE_URL=https://foreign.example/v1beta",
		"CLOUD_CODE_URL=https://attacker.example",
		"cloud_code_url=https://attacker.example",
		"\ufeffCLOUD_CODE_URL=https://attacker.example",
		"PATH=/usr/bin",
		"HOME=/home/u",
		"AGY_PRO_HOME=/home/u/.agy-pro",
	}
	// nil scrubNames: nothing but the fixed set + the latch is applied.
	got := CleanChildEnvWithBase(base, nil)
	joined := strings.Join(got, "\n")

	for _, banned := range []string{
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "CLOUD_CODE_URL",
		"AIzaSyUSER-REAL-KEY", "AIzaSyUSER-REAL-KEY-2",
		"AIzaSyLOWER-VARIANT", "AIzaSyLOWER-VARIANT-2",
		"foreign.example", "attacker.example",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/u", "AGY_PRO_HOME=/home/u/.agy-pro"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("benign var %q must survive; got:\n%s", keep, joined)
		}
	}
}

// TestCleanChildEnv_CaseFoldsScrubComparison pins round-4 M-1: Windows env
// names are case-insensitive while os.Environ preserves the stored case, so a
// case variant of a scrub name must be scrubbed too. The provider key name is
// passed in canonical case; the base carries exact, lower, mixed, BOM-padded,
// and whitespace-padded variants of it.
func TestCleanChildEnv_CaseFoldsScrubComparison(t *testing.T) {
	base := []string{
		"NW_API_KEY=exact-secret",
		"nw_api_key=lower-secret",
		"Nw_Api_Key=mixed-secret",
		"\ufeffnW_aPI_kEY=bom-lower-secret",
		"  nw_api_key  =ws-lower-secret",
		"KEEP_ME=please",
	}
	got := CleanChildEnvWithBase(base, []string{"NW_API_KEY"})
	joined := strings.Join(got, "\n")

	for _, banned := range []string{
		"exact-secret", "lower-secret", "mixed-secret",
		"bom-lower-secret", "ws-lower-secret",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	for _, kv := range got {
		name := kv[:strings.IndexByte(kv, '=')]
		if normalizeEnvName(name) == "nw_api_key" {
			t.Errorf("a case variant of NW_API_KEY survived: %q", kv)
		}
	}
	if !strings.Contains(joined, "KEEP_ME=please") {
		t.Errorf("unrelated var must be preserved; got:\n%s", joined)
	}
}

// TestCleanChildEnv_BenignNamesSurvive pins that the widened scrub set drops
// only its own names: PATH, HOME and AGY_PRO_HOME pass through verbatim, and
// the AGY_PRO_ACTIVE latch is re-pinned to 1 (a stale inherited value is
// replaced, not duplicated).
func TestCleanChildEnv_BenignNamesSurvive(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"AGY_PRO_HOME=/home/u/.agy-pro",
		"AGY_PRO_ACTIVE=stale",
		"AGY_PRO_PROVIDERS=/home/u/.agy-pro/providers.json",
	}
	got := CleanChildEnvWithBase(base, nil)
	joined := strings.Join(got, "\n")

	for _, keep := range []string{
		"PATH=/usr/bin", "HOME=/home/u", "AGY_PRO_HOME=/home/u/.agy-pro",
		"AGY_PRO_PROVIDERS=/home/u/.agy-pro/providers.json",
	} {
		if !strings.Contains(joined, keep) {
			t.Errorf("benign var %q must survive; got:\n%s", keep, joined)
		}
	}
	if strings.Contains(joined, "AGY_PRO_ACTIVE=stale") {
		t.Errorf("a stale latch value must be replaced; got:\n%s", joined)
	}
	count := 0
	for _, kv := range got {
		if kv == "AGY_PRO_ACTIVE=1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("AGY_PRO_ACTIVE=1 must appear exactly once, got %d in:\n%s", count, joined)
	}
}

// TestCleanChildEnv_AppendAfterScrubInjectionLands pins the P2 injection order
// (spec §2 P2 / fixedScrubNames): the scrub removes the user's real
// GEMINI_API_KEY / GOOGLE_GEMINI_BASE_URL, and an injection appended AFTER the
// scrub still lands through the same last-wins dedup launcher.setEnvKV uses
// (launcher.go:524-540). Without this order the P2 install nonce would either
// be scrubbed away or lose to a poisoned parent value.
func TestCleanChildEnv_AppendAfterScrubInjectionLands(t *testing.T) {
	base := []string{
		"GEMINI_API_KEY=AIzaSyUSER-REAL-KEY",
		"GOOGLE_GEMINI_BASE_URL=https://foreign.example/v1beta",
		"PATH=/usr/bin",
	}
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	meshURL := "http://127.0.0.1:8790"

	got := CleanChildEnvWithBase(base, nil)
	got = appendEnvKV(got, "GEMINI_API_KEY="+nonce)
	got = appendEnvKV(got, "GOOGLE_GEMINI_BASE_URL="+meshURL)

	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "GEMINI_API_KEY="+nonce) {
		t.Errorf("the post-scrub nonce injection must land; got:\n%s", joined)
	}
	if !strings.Contains(joined, "GOOGLE_GEMINI_BASE_URL="+meshURL) {
		t.Errorf("the post-scrub mesh URL injection must land; got:\n%s", joined)
	}
	for _, banned := range []string{"AIzaSyUSER-REAL-KEY", "foreign.example"} {
		if strings.Contains(joined, banned) {
			t.Errorf("the scrubbed parent value must not survive the injection; got %q in:\n%s", banned, joined)
		}
	}
	// Exactly one entry per injected name: the dedup replaced, not appended.
	for _, name := range []string{"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"} {
		n := 0
		for _, kv := range got {
			if normalizeEnvName(kv[:strings.IndexByte(kv, '=')]) == normalizeEnvName(name) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s must appear exactly once after the injection, got %d in:\n%s", name, n, joined)
		}
	}
}

// appendEnvKV mirrors launcher.setEnvKV (launcher.go:524-540): the entry
// replaces every case-insensitive name match and is appended last, so the last
// write wins.
func appendEnvKV(env []string, entry string) []string {
	parts := strings.SplitN(entry, "=", 2)
	if len(parts) != 2 {
		return append(env, entry)
	}
	key := parts[0]
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if idx := strings.Index(e, "="); idx >= 0 {
			if strings.EqualFold(e[:idx], key) {
				continue
			}
		}
		out = append(out, e)
	}
	return append(out, entry)
}

func TestApiKeyEnvNames(t *testing.T) {
	if got := ApiKeyEnvNames(nil); got != nil {
		t.Errorf("nil registry -> %v; want nil", got)
	}
	if got := ApiKeyEnvNames(&providers.Registry{}); got != nil {
		t.Errorf("empty registry -> %v; want nil", got)
	}
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "a", APIKeyEnv: "A_KEY"},
		{Name: "b", APIKeyEnv: "B_KEY"},
		{Name: "c", APIKeyEnv: "A_KEY"}, // dup
		{Name: "d", APIKeyEnv: ""},      // no key
	}}
	// Names come back in normalizeEnvName's folded form, which is the form the
	// scrub comparison uses (FIX-46).
	got := ApiKeyEnvNames(reg)
	if len(got) != 2 || got[0] != "a_key" || got[1] != "b_key" {
		t.Errorf("ApiKeyEnvNames = %v; want [a_key b_key] deduped, empties skipped", got)
	}
}

// M4F-1: the scrub is name-based AND shape-based. Any *_API_KEY-shaped name is
// dropped even when its provider is neither a compiled preset nor configured in
// the registry (CHARM_API_KEY on a charm-less machine, a generic
// SOMEVENDOR_API_KEY), closing the recurring S-3 ambient-key class. Case and
// BOM variants are covered; benign non-*_API_KEY vars survive; and the P2
// install nonce appended AFTER the scrub still lands (the suffix rule never
// touches the append order).
func TestCleanChildEnv_ScrubsUndeclaredAPIKeyShapedNames(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		// CHARM_API_KEY: undeclared and NOT a compiled preset; its case and BOM
		// variants prove the shape rule is case/BOM-insensitive.
		"CHARM_API_KEY=charm-secret-value",
		"charm_api_key=charm-secret-lower",
		"\ufeffCHARM_API_KEY=charm-secret-bom",
		// A generic future *_API_KEY provider, absent from every list.
		"SOMEVENDOR_API_KEY=vendor-secret-value",
		"KEEP_ME=please",
	}
	// nil scrubNames: only the fixed set + the shape rule apply, exactly like a
	// native seat that carries no registry.
	got := CleanChildEnvWithBase(base, nil)
	joined := strings.Join(got, "\n")

	for _, banned := range []string{
		"CHARM_API_KEY", "SOMEVENDOR_API_KEY",
		"charm-secret-value", "charm-secret-lower", "charm-secret-bom",
		"vendor-secret-value",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	// No surviving entry may be *_API_KEY-shaped.
	for _, kv := range got {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if isProviderKeyName(normalizeEnvName(kv[:eq])) {
			t.Errorf("a *_API_KEY-shaped name survived the scrub: %q", kv)
		}
	}
	// Benign, non-*_API_KEY vars survive untouched.
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/u", "KEEP_ME=please"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("benign var %q must survive; got:\n%s", keep, joined)
		}
	}

	// P2 injection order (M4F-1 invariant a): the scrub drops the ambient
	// GEMINI_API_KEY, and a nonce appended AFTER the scrub still lands.
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	withNonce := appendEnvKV(got, "GEMINI_API_KEY="+nonce)
	if !strings.Contains(strings.Join(withNonce, "\n"), "GEMINI_API_KEY="+nonce) {
		t.Errorf("the post-scrub P2 nonce GEMINI_API_KEY must survive the suffix scrub")
	}
}

// M4F-1 boundary (updated by F-10B): the shape rule matches on a SUFFIX, never
// on a substring. "api_key" now matches through the _key arm (a bare API_KEY IS
// a credential name the child never needs), while names that merely contain
// "key" without an underscore boundary (myapikey, keyring_path, monkey) still
// survive untouched.
func TestIsProviderKeyName_ShapeBoundary(t *testing.T) {
	scrub := []string{"charm_api_key", "somevendor_api_key", "agy_llm_gateway_api_key", "gemini_api_key", "api_key", "myapikey", "keyring_path", "monkey"}
	want := []bool{true, true, true, true, true, false, false, false}
	for i, n := range scrub {
		if got := isProviderKeyName(n); got != want[i] {
			t.Errorf("isProviderKeyName(%q) = %v; want %v", n, got, want[i])
		}
	}
}

// F-10B (round-12 security, kimigw F-2): the shape rule covers the credential
// naming conventions beyond *_API_KEY. GITHUB_TOKEN proves the _token arm;
// AWS_SECRET_ACCESS_KEY proves the _key arm is what catches a name that does
// NOT end in _secret (the reason the _key arm exists at all); a *_CLIENT_SECRET
// covers the _secret arm. Case and BOM variants ride the same normalizeEnvName
// fold the existing pins exercise, the fixed gateway set and a registry name
// still scrub through the explicit arm, and benign names survive.
func TestCleanChildEnv_ScrubsTokenAndSecretShapedNames(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"KEEP_ME=please",
		// _token arm, plain + case + BOM variants.
		"GITHUB_TOKEN=ghp_planted_token_value",
		"github_token=ghp_planted_lower_value",
		"\ufeffGITHUB_TOKEN=ghp_planted_bom_value",
		// _key arm: AWS's secret access key does not end in _secret.
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIplantedsecretaccesskey",
		// _secret arm.
		"OAUTH_CLIENT_SECRET=planted-client-secret-value",
		// The fixed gateway set still scrubs through the explicit arm, and a
		// registry-named key still scrubs through scrubNames.
		"AGY_LLM_GATEWAY_URL=http://127.0.0.1:1",
		"AGY_LLM_GATEWAY_API_KEY=gw-planted-value",
		"NW_API_KEY=nw-planted-value",
	}
	got := CleanChildEnvWithBase(base, []string{"NW_API_KEY"})
	joined := strings.Join(got, "\n")

	for _, banned := range []string{
		"GITHUB_TOKEN", "ghp_planted_token_value", "ghp_planted_lower_value",
		"ghp_planted_bom_value", "AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMIplantedsecretaccesskey",
		"OAUTH_CLIENT_SECRET", "planted-client-secret-value",
		"AGY_LLM_GATEWAY_URL", "AGY_LLM_GATEWAY_API_KEY", "gw-planted-value",
		"NW_API_KEY", "nw-planted-value",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	// No surviving entry may be credential-shaped.
	for _, kv := range got {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if isProviderKeyName(normalizeEnvName(kv[:eq])) {
			t.Errorf("a credential-shaped name survived the scrub: %q", kv)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "KEEP_ME=please"} {
		if !strings.Contains(joined, keep) {
			t.Errorf("benign var %q must survive; got:\n%s", keep, joined)
		}
	}
	// The recursion latch is still appended exactly once.
	if strings.Count(joined, "AGY_PRO_ACTIVE=1") != 1 {
		t.Errorf("AGY_PRO_ACTIVE latch = %d occurrences; want 1", strings.Count(joined, "AGY_PRO_ACTIVE=1"))
	}
}
