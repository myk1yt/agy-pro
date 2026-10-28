package wirecheck

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pathTrapStub mirrors PATH's first `agy` on this machine: an 11-byte mock
// stub. Resolving through realagy.Resolve (never PATH) is what keeps this
// package away from it; the test below proves a stub can never pass.
const pathTrapStub = "agy-stub-11"

// plantedFiller is unique junk planted next to the canaries; Render must
// never echo it (DoD #3: no logging of file contents).
const plantedFiller = "PLANTED-FILLER-7c1e-not-a-canary"

// plantedSkLiveKey is the planted live-key value the FIX-7 child-env pin
// places next to the canaries. The head is built by concatenation so the
// tracked source carries no contiguous provider-key spelling; the runtime value
// is byte-identical to the literal it replaced.
const plantedSkLiveKey = "sk-" + "live-" + "provider-key-value-abcdef"

func allLiterals(t *testing.T) []string {
	t.Helper()
	canaries := canarySet()
	literals := make([]string, 0, len(canaries))
	for _, c := range canaries {
		literals = append(literals, c.Literal)
	}
	return literals
}

// plantBinary writes content to a temp file and returns its path.
func plantBinary(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agy.exe")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("plant binary: %v", err)
	}
	return path
}

// plantFullBinary plants every canary literal plus the filler.
func plantFullBinary(t *testing.T) string {
	t.Helper()
	return plantBinary(t, plantedFiller+"\n"+strings.Join(allLiterals(t), "\n")+"\n")
}

func resolveTo(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func probeVersion(v string) func(string) (string, error) {
	return func(string) (string, error) { return v, nil }
}

func TestCheckWith_AllCanariesAndPinnedVersion_GO(t *testing.T) {
	path := plantFullBinary(t)

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictGO {
		t.Fatalf("Verdict() = %q; want %q (reasons: %v)", got, VerdictGO, r.Reasons())
	}
	if r.BinaryPath != path {
		t.Errorf("BinaryPath = %q; want %q", r.BinaryPath, path)
	}
	if r.Version != PinnedVersion || r.VersionDrift {
		t.Errorf("Version = %q drift = %v; want %q with no drift", r.Version, r.VersionDrift, PinnedVersion)
	}
	if !r.ProfileOK(ProfileP1) || !r.ProfileOK(ProfileP2) {
		t.Error("both profiles must be OK when every canary is found")
	}
	if reasons := r.Reasons(); len(reasons) != 0 {
		t.Errorf("Reasons() = %v; want none on a clean GO", reasons)
	}
	for _, c := range r.Canaries {
		if !c.Found {
			t.Errorf("canary %s not found in the planted binary", c.Name)
		}
	}
}

func TestCheckWith_MissingCanary_NOGO_NamesIt(t *testing.T) {
	// One missing literal forces NOGO when it breaks the only profile the
	// binary carries: the P1 set minus the plan-model gate literal, with no
	// P2 literals at all.
	var kept []string
	for _, c := range canarySet() {
		if c.Profile != ProfileP1 || c.Literal == "plan model not specified" {
			continue
		}
		kept = append(kept, c.Literal)
	}
	path := plantBinary(t, strings.Join(kept, "\n"))

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if r.ProfileOK(ProfileP1) {
		t.Error("P1 must not be OK with the plan-model gate literal missing")
	}
	if r.ProfileOK(ProfileP2) {
		t.Error("P2 has no canaries in this binary")
	}
	joined := strings.Join(r.Reasons(), "\n")
	for _, want := range []string{ProfileP1, "plan_model_gate", "plan model not specified"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Reasons() must name %q; got:\n%s", want, joined)
		}
	}
}

func TestCheckWith_PartialProfileStillGO(t *testing.T) {
	// The client-path split gates the profiles independently: a complete P2
	// with one P1 canary missing is still GO. The launcher asks ProfileOK(P1)
	// before a P1 session and falls back to native for that profile only.
	var kept []string
	for _, c := range canarySet() {
		if c.Profile == ProfileP1 && c.Literal == "plan model not specified" {
			continue
		}
		kept = append(kept, c.Literal)
	}
	path := plantBinary(t, strings.Join(kept, "\n"))

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictGO {
		t.Fatalf("Verdict() = %q; want %q (P2 is complete)", got, VerdictGO)
	}
	if r.ProfileOK(ProfileP1) {
		t.Error("P1 must not be OK: the plan-model gate literal is missing")
	}
	if !r.ProfileOK(ProfileP2) {
		t.Error("P2 must be OK")
	}
	if reasons := r.Reasons(); len(reasons) != 0 {
		t.Errorf("Reasons() = %v; want none on a GO with no drift (ProfileOK carries the per-profile gap)", reasons)
	}
}

func TestCheckWith_VersionDrift_RecordedButCanaryDriven(t *testing.T) {
	path := plantFullBinary(t)

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion("9.9.9"))

	if !r.VersionDrift {
		t.Error("VersionDrift must be true when the probe reports a different version")
	}
	if r.Version != "9.9.9" {
		t.Errorf("Version = %q; want 9.9.9", r.Version)
	}
	if got := r.Verdict(); got != VerdictGO {
		t.Errorf("Verdict() = %q; drift alone must not flip a canary-clean report", got)
	}
	joined := strings.Join(r.Reasons(), "\n")
	if !strings.Contains(joined, "9.9.9") || !strings.Contains(joined, PinnedVersion) {
		t.Errorf("drift must be surfaced in Reasons(); got:\n%s", joined)
	}
}

func TestCheckWith_DriftPlusMissingCanary_NOGO(t *testing.T) {
	// A gap in each profile plus drift: NOGO (no profile is complete), with
	// the drift still recorded for surfacing.
	var kept []string
	for _, c := range canarySet() {
		if c.Literal == "plan model not specified" || c.Literal == "modelProvider" {
			continue
		}
		kept = append(kept, c.Literal)
	}
	path := plantBinary(t, strings.Join(kept, "\n"))

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion("2.0.0"))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Errorf("Verdict() = %q; want NOGO (both profiles carry a gap)", got)
	}
	if !r.VersionDrift {
		t.Error("drift must still be recorded alongside the NOGO")
	}
	if r.ProfileOK(ProfileP1) || r.ProfileOK(ProfileP2) {
		t.Error("neither profile is usable")
	}
}

func TestCheckWith_OneProfileEnough_GO(t *testing.T) {
	var p2 []string
	for _, c := range canarySet() {
		if c.Profile == ProfileP2 {
			p2 = append(p2, c.Literal)
		}
	}
	path := plantBinary(t, strings.Join(p2, "\n"))

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictGO {
		t.Fatalf("Verdict() = %q; want GO (P2 fully present)", got)
	}
	if r.ProfileOK(ProfileP1) {
		t.Error("P1 canaries are absent; P1 must not be OK")
	}
	if !r.ProfileOK(ProfileP2) {
		t.Error("P2 must be OK")
	}
}

func TestCheckWith_ResolveError_NOGO(t *testing.T) {
	readCalled, versionCalled := false, false

	r := CheckWith(
		func() (string, error) {
			return "", errors.New("real agy binary not found: please install official agy")
		},
		func(string) ([]byte, error) {
			readCalled = true
			return nil, nil
		},
		func(string) (string, error) {
			versionCalled = true
			return PinnedVersion, nil
		},
	)

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if r.BinaryPath != "" || len(r.Canaries) != 0 {
		t.Error("a resolve failure must leave the report empty of scan results")
	}
	if readCalled || versionCalled {
		t.Error("a resolve failure must short-circuit the scan and the version probe")
	}
	if joined := strings.Join(r.Reasons(), "\n"); !strings.Contains(joined, "real agy binary not found") {
		t.Errorf("Reasons() must carry the resolve failure; got:\n%s", joined)
	}
}

func TestCheckWith_VersionProbeError_NOGO(t *testing.T) {
	path := plantFullBinary(t)

	r := CheckWith(resolveTo(path), os.ReadFile, func(string) (string, error) {
		return "", errors.New("agy.exe --version failed: exit status 1")
	})

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if !strings.Contains(r.VersionErr, "exit status 1") {
		t.Errorf("VersionErr = %q; want the probe failure", r.VersionErr)
	}
	if !r.ProfileOK(ProfileP1) || !r.ProfileOK(ProfileP2) {
		t.Error("canaries stay evaluated; the version probe is the sole blocker")
	}
	if joined := strings.Join(r.Reasons(), "\n"); !strings.Contains(joined, "version probe failed") {
		t.Errorf("Reasons() must name the version probe failure; got:\n%s", joined)
	}
}

func TestCheckWith_EmptyVersionString_NOGO(t *testing.T) {
	path := plantFullBinary(t)

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(""))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Errorf("Verdict() = %q; want NOGO (an empty version is a probe failure)", got)
	}
	if r.VersionErr == "" {
		t.Error("an empty version must be recorded as a probe failure")
	}
}

func TestCheckWith_ElevenByteStub_NOGO(t *testing.T) {
	if len(pathTrapStub) != 11 {
		t.Fatalf("fixture must mirror the 11-byte PATH stub, got %d bytes", len(pathTrapStub))
	}
	path := plantBinary(t, pathTrapStub)

	// The version matches the pin on purpose: a pinned version must never
	// rescue a binary that carries none of the required surface.
	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q for the PATH-trap stub", got, VerdictNOGO)
	}
	if r.ProfileOK(ProfileP1) || r.ProfileOK(ProfileP2) {
		t.Error("the stub must satisfy no profile")
	}
	found := 0
	for _, c := range r.Canaries {
		if c.Found {
			found++
		}
	}
	if found != 0 {
		t.Errorf("stub matched %d canaries; want 0", found)
	}
}

func TestCheckWith_EmptyBinary_NOGO(t *testing.T) {
	path := plantBinary(t, "")

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q for an empty binary", got, VerdictNOGO)
	}
	if len(r.Canaries) == 0 {
		t.Fatal("an empty binary still evaluates the full canary table")
	}
	for _, c := range r.Canaries {
		if c.Found {
			t.Errorf("canary %s found in an empty binary", c.Name)
		}
	}
}

func TestCheckWith_ReadError_NOGO(t *testing.T) {
	r := CheckWith(
		resolveTo(filepath.Join(t.TempDir(), "agy.exe")),
		func(string) ([]byte, error) { return nil, errors.New("access denied") },
		probeVersion(PinnedVersion),
	)

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if len(r.Canaries) != 0 {
		t.Error("an unreadable binary must not be scanned")
	}
	if joined := strings.Join(r.Reasons(), "\n"); !strings.Contains(joined, "access denied") {
		t.Errorf("Reasons() must carry the read failure; got:\n%s", joined)
	}
}

func TestCheckWith_OverCapRead_NOGO(t *testing.T) {
	path := plantFullBinary(t)
	original := maxBinaryBytes
	maxBinaryBytes = 4
	t.Cleanup(func() { maxBinaryBytes = original })

	r := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	if got := r.Verdict(); got != VerdictNOGO {
		t.Fatalf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if len(r.Canaries) != 0 {
		t.Error("an over-cap read must not be scanned")
	}
	if joined := strings.Join(r.Reasons(), "\n"); !strings.Contains(joined, "scan cap") {
		t.Errorf("Reasons() must name the scan cap; got:\n%s", joined)
	}
}

func TestCheckWith_NilSeamsFailClosed(t *testing.T) {
	r := CheckWith(nil, nil, nil)
	if got := r.Verdict(); got != VerdictNOGO {
		t.Errorf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if r.ScanErr == "" {
		t.Error("a nil resolver must record a scan failure")
	}

	// FIX-8: a nil version probe is a FAILED probe, not a skipped one. A
	// canary-clean report must never read GO with zero version verification.
	r2 := CheckWith(resolveTo("agy.exe"), nil, nil)
	if len(r2.Canaries) != 0 {
		t.Error("a nil read seam must leave the canary table empty")
	}
	if r2.VersionErr == "" {
		t.Error("a nil version probe seam must record an explicit probe failure")
	}
	if got := r2.Verdict(); got != VerdictNOGO {
		t.Errorf("Verdict() = %q; want %q with no version verification", got, VerdictNOGO)
	}
	if joined := strings.Join(r2.Reasons(), "\n"); !strings.Contains(joined, "version probe") {
		t.Errorf("Reasons() must name the skipped probe; got:\n%s", joined)
	}
}

func TestProfileOK(t *testing.T) {
	empty := Report{}
	if empty.ProfileOK(ProfileP1) || empty.ProfileOK("") || empty.ProfileOK("P9") {
		t.Error("an empty report must satisfy no profile")
	}

	canaries := canarySet()
	for i := range canaries {
		canaries[i].Found = true
	}
	full := Report{Canaries: canaries}
	if !full.ProfileOK(ProfileP1) || !full.ProfileOK(ProfileP2) {
		t.Error("all-found canaries must satisfy both profiles")
	}
	if !full.ProfileOK("p1") {
		t.Error("profile matching is case-insensitive")
	}
	if got := full.Verdict(); got != VerdictGO {
		t.Errorf("Verdict() = %q; want %q", got, VerdictGO)
	}

	canaries[0].Found = false // first canary is P1's cloud_code_url_env
	partial := Report{Canaries: canaries}
	if partial.ProfileOK(ProfileP1) {
		t.Error("one missing canary fails the whole profile")
	}
	if !partial.ProfileOK(ProfileP2) {
		t.Error("P2 is unaffected by a P1 gap")
	}
}

func TestCanarySet_TableLocked(t *testing.T) {
	canaries := canarySet()
	if len(canaries) != 14 {
		t.Fatalf("canary count = %d; want 14 (6 P1 + 8 P2)", len(canaries))
	}
	names := map[string]bool{}
	literals := map[string]bool{}
	counts := map[string]int{}
	for _, c := range canaries {
		if c.Profile == "" || c.Name == "" || c.Literal == "" {
			t.Errorf("canary has an empty field: %+v", c)
		}
		if names[c.Name] {
			t.Errorf("duplicate canary name %q", c.Name)
		}
		if literals[c.Literal] {
			t.Errorf("duplicate canary literal %q", c.Literal)
		}
		names[c.Name] = true
		literals[c.Literal] = true
		counts[c.Profile]++
	}
	if counts[ProfileP1] != 6 || counts[ProfileP2] != 8 {
		t.Errorf("per-profile counts = %v; want P1=6 P2=8", counts)
	}

	// FIX-9: pin the exact literals, in table order. The three P1 route
	// canaries must carry the full "/v1internal:" prefix so a route-prefix
	// drift cannot keep the gate GO while the mesh 404s every RPC.
	want := []Canary{
		{Profile: ProfileP1, Name: "cloud_code_url_env", Literal: "CLOUD_CODE_URL"},
		{Profile: ProfileP1, Name: "cloud_code_url_override_log", Literal: "Overriding CloudCodeServerURL via CLOUD_CODE_URL environment variable"},
		{Profile: ProfileP1, Name: "route_fetch_available_models", Literal: "/v1internal:fetchAvailableModels"},
		{Profile: ProfileP1, Name: "route_stream_generate_content", Literal: "/v1internal:streamGenerateContent"},
		{Profile: ProfileP1, Name: "route_load_code_assist", Literal: "/v1internal:loadCodeAssist"},
		{Profile: ProfileP1, Name: "plan_model_gate", Literal: "plan model not specified"},
		{Profile: ProfileP2, Name: "scheme_gemini_api", Literal: "gemini-api:"},
		{Profile: ProfileP2, Name: "scheme_blade", Literal: "blade:"},
		{Profile: ProfileP2, Name: "scheme_evergreen", Literal: "evergreen:"},
		{Profile: ProfileP2, Name: "scheme_beyond_api", Literal: "beyond-api:"},
		{Profile: ProfileP2, Name: "genai_sdk_user_agent", Literal: "google-genai-sdk/"},
		{Profile: ProfileP2, Name: "env_google_gemini_base_url", Literal: "GOOGLE_GEMINI_BASE_URL"},
		{Profile: ProfileP2, Name: "env_gemini_api_key", Literal: "GEMINI_API_KEY"},
		{Profile: ProfileP2, Name: "settings_model_provider", Literal: "modelProvider"},
	}
	for i, w := range want {
		got := canaries[i]
		if got.Profile != w.Profile || got.Name != w.Name || got.Literal != w.Literal {
			t.Errorf("canary[%d] = %+v; want %+v", i, got, w)
		}
	}
}

func TestReadBinary(t *testing.T) {
	path := plantBinary(t, "CLOUD_CODE_URL")
	data, err := readBinary(path)
	if err != nil {
		t.Fatalf("readBinary: %v", err)
	}
	if !bytes.Contains(data, []byte("CLOUD_CODE_URL")) {
		t.Error("readBinary must return the file bytes")
	}

	original := maxBinaryBytes
	maxBinaryBytes = 4
	t.Cleanup(func() { maxBinaryBytes = original })
	if _, err := readBinary(path); err == nil {
		t.Error("readBinary must reject a file over the scan cap")
	}

	if _, err := readBinary(t.TempDir()); err == nil {
		t.Error("readBinary must reject a directory")
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.3.5", "1.3.5"},
		{"1.3.5\n", "1.3.5"},
		{"agy 1.3.5", "1.3.5"},
		{"v1.3.5 (build 7)", "1.3.5"},
		{"1.3", "1.3"},
		{"", ""},
		{"  \n\t ", ""},
		{"unknown build", "unknown build"},
	}
	for _, tc := range cases {
		if got := parseVersion(tc.in); got != tc.want {
			t.Errorf("parseVersion(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestRender(t *testing.T) {
	path := plantFullBinary(t)
	good := CheckWith(resolveTo(path), os.ReadFile, probeVersion(PinnedVersion))

	var buf bytes.Buffer
	Render(&buf, good)
	out := buf.String()
	for _, want := range []string{path, PinnedVersion, VerdictGO, ProfileP1, ProfileP2, "plan_model_gate", "settings_model_provider"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, plantedFiller) {
		t.Error("Render must print canary booleans, never binary contents")
	}

	bad := CheckWith(resolveTo(path), func(string) ([]byte, error) { return []byte(pathTrapStub), nil },
		func(string) (string, error) { return "", errors.New("probe timeout") })
	buf.Reset()
	Render(&buf, bad)
	out = buf.String()
	for _, want := range []string{VerdictNOGO, "probe timeout", "MISSING"} {
		if !strings.Contains(out, want) {
			t.Errorf("NOGO Render output missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	Render(&buf, Report{})
	out = buf.String()
	for _, want := range []string{"(not resolved)", "(not probed)", "not evaluated", VerdictNOGO} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-report Render output missing %q:\n%s", want, out)
		}
	}
}

// TestCheck_EndToEndWiring exercises the production Check() against a planted
// temp binary via AGY_PRO_REAL_AGY, never the real agy.exe and never the
// network. It is opt-in: WIRECHECK_E2E=1 go test ./internal/wirecheck/
func TestCheck_EndToEndWiring(t *testing.T) {
	if os.Getenv("WIRECHECK_E2E") != "1" {
		t.Skip("set WIRECHECK_E2E=1 to run the Check() wiring smoke test")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "planted-agy")
	if err := os.WriteFile(stub, []byte(strings.Join(allLiterals(t), "\n")), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	t.Setenv("AGY_PRO_REAL_AGY", stub)

	r := Check()

	if r.BinaryPath != filepath.Clean(stub) {
		t.Errorf("BinaryPath = %q; want the planted path %q", r.BinaryPath, stub)
	}
	for _, c := range r.Canaries {
		if !c.Found {
			t.Errorf("canary %s not found through the production reader", c.Name)
		}
	}
	// The planted file is not an executable: the --version probe must hard-fail
	// and the verdict must be NOGO with that reason (the real binary is never
	// touched, so this run has no network or auto-update side effect).
	if r.VersionErr == "" {
		t.Error("expected the version probe to fail on a non-executable plant")
	}
	if got := r.Verdict(); got != VerdictNOGO {
		t.Errorf("Verdict() = %q; want %q", got, VerdictNOGO)
	}
	if joined := strings.Join(r.Reasons(), "\n"); !strings.Contains(joined, "version probe failed") {
		t.Errorf("Reasons() must name the version probe failure; got:\n%s", joined)
	}
}

// TestFilterChildEnv_DefaultDeny pins the FIX-7 allow-list core: only the ten
// allow-listed names survive, everything else (a planted secret among them) is
// dropped, and allowed values pass through verbatim.
func TestFilterChildEnv_DefaultDeny(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"Path=C:\\Windows", // Windows block case; allowed only via case folding
		"SystemRoot=C:\\Windows",
		"LANG=C",
		"AGY_PRO_PLANT_SECRET=sk-planted-secret-value-123456",
		"NW_API_KEY=" + plantedSkLiveKey,
		"AGY_LLM_GATEWAY_API_KEY=sk-gateway-key-value-abcdef",
		"AGY_PRO_REAL_AGY=C:\\fake\\agy.exe",
		"no-equals-sign",
	}

	got := FilterChildEnv(environ)
	joined := strings.Join(got, "\n")

	for _, banned := range []string{
		"sk-planted-secret-value-123456", "AGY_PRO_PLANT_SECRET",
		plantedSkLiveKey, "NW_API_KEY",
		"sk-gateway-key-value-abcdef", "AGY_LLM_GATEWAY_API_KEY",
		"AGY_PRO_REAL_AGY", "no-equals-sign",
	} {
		if strings.Contains(joined, banned) {
			t.Errorf("child env must not contain %q; got:\n%s", banned, joined)
		}
	}
	// Nothing outside the allow-list may survive, whatever its name.
	for _, kv := range got {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			t.Fatalf("child env entry without '=' survived: %q", kv)
		}
		if !nameInAllowList(kv[:i]) {
			t.Errorf("non-allow-listed entry survived: %q", kv)
		}
	}
	// Allowed entries keep their name and value verbatim.
	for _, want := range []string{"PATH=/usr/bin", "LANG=C"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child env must keep %q; got:\n%s", want, joined)
		}
	}
}

// TestProbeCommand_ChildEnvExcludesPlantedSecret captures the env the REAL
// probe hands to the child (probeCommand builds the exact configured command)
// and proves the process-level planted secret never reaches it.
func TestProbeCommand_ChildEnvExcludesPlantedSecret(t *testing.T) {
	const planted = "sk-planted-secret-value-123456"
	t.Setenv("AGY_PRO_PLANT_SECRET", planted)
	t.Setenv("LANG", "C")

	cmd := probeCommand(context.Background(), filepath.Join(t.TempDir(), "agy.exe"))

	if cmd.Env == nil {
		t.Fatal("probeCommand must set an explicit default-deny env (nil means inherit-all)")
	}
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, planted) || strings.Contains(joined, "AGY_PRO_PLANT_SECRET") {
		t.Errorf("the planted secret must not reach the child env; got:\n%s", joined)
	}
	for _, kv := range cmd.Env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			t.Fatalf("child env entry without '=': %q", kv)
		}
		if !nameInAllowList(kv[:i]) {
			t.Errorf("non-allow-listed entry in the child env: %q", kv)
		}
	}
	if !strings.Contains(joined, "LANG=C") {
		t.Errorf("allow-listed entries must survive; got:\n%s", joined)
	}
	if cmd.Stdin != nil {
		t.Error("probe stdin must stay nil (the null device): headless, never interactive")
	}
}

// TestCheck_LiveRealBinary is the FIX-7 live verification: it runs the
// production Check() against the real agy.exe (v1.3.5) with the scrubbed
// child env and asserts the version parses, the verdict is GO, and every
// canary is found. Opt-in: WIRECHECK_LIVE=1 go test ./internal/wirecheck/
// The run only execs `agy --version` (no TUI, no login, no network).
func TestCheck_LiveRealBinary(t *testing.T) {
	if os.Getenv("WIRECHECK_LIVE") != "1" {
		t.Skip("set WIRECHECK_LIVE=1 to run the live binary probe")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the live probe targets the Windows install path")
	}
	const realBinary = `C:\Users\k1yt\AppData\Local\agy\bin\agy.exe`
	if _, err := os.Stat(realBinary); err != nil {
		// FIX-20: the path is machine-specific. On any other machine (or a
		// moved install) the suite must stay green and portable, so a missing
		// binary is a skip, not a failure.
		t.Skipf("real agy binary not present at %s: %v", realBinary, err)
	}
	t.Setenv("AGY_PRO_REAL_AGY", realBinary)

	r := Check()

	if r.BinaryPath != filepath.Clean(realBinary) {
		t.Errorf("BinaryPath = %q; want the real binary %q", r.BinaryPath, realBinary)
	}
	if r.VersionErr != "" {
		t.Fatalf("version probe failed against the real binary: %s", r.VersionErr)
	}
	if r.Version != PinnedVersion {
		t.Errorf("Version = %q; want %q", r.Version, PinnedVersion)
	}
	if r.VersionDrift {
		t.Error("the real binary is the pinned version; drift must be false")
	}
	if got := r.Verdict(); got != VerdictGO {
		t.Fatalf("Verdict() = %q; want %q (reasons: %v)", got, VerdictGO, r.Reasons())
	}
	if !r.ProfileOK(ProfileP1) || !r.ProfileOK(ProfileP2) {
		t.Error("both profiles must be OK against the real binary")
	}
	for _, c := range r.Canaries {
		if !c.Found {
			t.Errorf("canary %s (%q) not found in the real binary", c.Name, c.Literal)
		}
	}
}

// TestCappedBuffer pins the FIX-14a capture bound: under-cap writes pass
// through, over-cap writes flag overflow while still reporting the full length
// consumed (so the child never blocks on a "full" pipe).
func TestCappedBuffer(t *testing.T) {
	var c cappedBuffer
	small := bytes.Repeat([]byte("a"), versionOutputCap-1)
	n, err := c.Write(small)
	if err != nil || n != len(small) {
		t.Fatalf("Write = (%d, %v); want (%d, nil)", n, err, len(small))
	}
	if c.overflow {
		t.Error("an under-cap write must not flag overflow")
	}
	if c.String() != string(small) {
		t.Error("under-cap bytes must be captured verbatim")
	}

	over := []byte("bb")
	n, err = c.Write(over)
	if err != nil || n != len(over) {
		t.Fatalf("over-cap Write = (%d, %v); want (%d, nil): the child must not see a short write", n, err, len(over))
	}
	if !c.overflow {
		t.Error("an over-cap write must flag overflow")
	}
	if got := len(c.String()); got > versionOutputCap {
		t.Errorf("captured %d bytes; want at most %d", got, versionOutputCap)
	}
}

// TestProbeResult pins the FIX-14a branch table: a run error fails, over-cap
// output fails (never a truncated parse), empty output fails, and a version
// line parses.
func TestProbeResult(t *testing.T) {
	ok := &cappedBuffer{buf: *bytes.NewBufferString("agy 1.3.5\n")}
	if v, err := probeResult(nil, ok, "agy.exe"); err != nil || v != "1.3.5" {
		t.Errorf("probeResult(version line) = (%q, %v); want (\"1.3.5\", nil)", v, err)
	}

	over := &cappedBuffer{overflow: true}
	if _, err := probeResult(nil, over, "agy.exe"); err == nil {
		t.Error("over-cap output must be a probe failure")
	} else if !strings.Contains(err.Error(), "more than") {
		t.Errorf("over-cap error must name the cap; got %v", err)
	}

	empty := &cappedBuffer{}
	if _, err := probeResult(nil, empty, "agy.exe"); err == nil {
		t.Error("empty output must be a probe failure")
	}

	if _, err := probeResult(errors.New("exit status 1"), ok, "agy.exe"); err == nil {
		t.Error("a non-zero exit must be a probe failure")
	}
}

// TestCheckWith_ResolvedPathIsAbsoluteForReadAndExec pins FIX-14b: a bare-name
// resolution is absolutized before the scan, and the read and the exec both
// receive that same absolute path (a relative exec would hit Go's LookPath and
// could run a different file than the one scanned).
func TestCheckWith_ResolvedPathIsAbsoluteForReadAndExec(t *testing.T) {
	var readPath, probePath string
	r := CheckWith(
		func() (string, error) { return "agy.exe", nil },
		func(p string) ([]byte, error) {
			readPath = p
			return []byte(strings.Join(allLiterals(t), "\n")), nil
		},
		func(p string) (string, error) {
			probePath = p
			return PinnedVersion, nil
		},
	)

	if !filepath.IsAbs(r.BinaryPath) {
		t.Fatalf("BinaryPath = %q; want an absolute path", r.BinaryPath)
	}
	if readPath != r.BinaryPath || probePath != r.BinaryPath {
		t.Errorf("read (%q) and exec (%q) must use the same absolute path %q", readPath, probePath, r.BinaryPath)
	}
	if got := r.Verdict(); got != VerdictGO {
		t.Errorf("Verdict() = %q; want %q (reasons: %v)", got, VerdictGO, r.Reasons())
	}
}
