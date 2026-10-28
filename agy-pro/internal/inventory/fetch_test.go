package inventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agypro/internal/hybrid"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

// The Spawn seam receives a command whose Env is then overwritten by Fetch
// with cleanChildEnv(...). Stub values therefore travel through the PARENT
// process environment (t.Setenv), which cleanChildEnv copies (minus the
// scrubbed names), and the helper child reports its FINAL environment to
// AGY_PRO_STUB_ENVFILE so the test can assert what the fetch actually
// delivered to `agy models`.

// spawnStub returns a Spawn seam that runs this test binary as the child
// helper (TestSpawnStubHelperProcess).
func spawnStub() func(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestSpawnStubHelperProcess")
	}
}

// TestSpawnStubHelperProcess is the child-side helper: writes its full
// environment to AGY_PRO_STUB_ENVFILE, optionally sleeps, prints
// AGY_PRO_STUB_STDOUT, exits 0.
func TestSpawnStubHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if p := os.Getenv("AGY_PRO_STUB_ENVFILE"); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	if ms, err := strconv.Atoi(os.Getenv("AGY_PRO_STUB_SLEEP_MS")); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if s := os.Getenv("AGY_PRO_STUB_STDERR"); s != "" {
		_, _ = os.Stderr.WriteString(s)
	}
	if code, err := strconv.Atoi(os.Getenv("AGY_PRO_STUB_EXIT")); err == nil && code != 0 {
		os.Exit(code)
	}
	_, _ = os.Stdout.WriteString(os.Getenv("AGY_PRO_STUB_STDOUT"))
	os.Exit(0)
}

// newStubFetcher wires a RealAgyModels against the helper child.
func newStubFetcher(t *testing.T, stdout string) (*RealAgyModels, string) {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("AGY_PRO_STUB_STDOUT", stdout)
	envFile := filepath.Join(t.TempDir(), "child-env.txt")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)
	t.Setenv("AGY_PRO_STUB_SLEEP_MS", "")
	return &RealAgyModels{
		BinPath:       "fake-agy",
		FetchDeadline: DefaultFetchDeadline,
		Spawn:         spawnStub(),
	}, envFile
}

func readChildEnv(t *testing.T, envFile string) string {
	t.Helper()
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("child env dump not written (fetch child never ran helper): %v", err)
	}
	return string(data)
}

func TestRealAgyModelsParsesQ6ViaSpawnSeam(t *testing.T) {
	f, envFile := newStubFetcher(t, q6Catalog)
	f.ScrubEnvNames = []string{"TESTPROV_KEY"}
	models, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 18 {
		t.Fatalf("parsed %d models, want 18", len(models))
	}
	if !strings.Contains(readChildEnv(t, envFile), paths.EnvAgyProActive+"=1") {
		t.Error("child env missing recursion latch")
	}
}

// F-ENV/F-Db: the catalog fetch child must never see gateway vars, bridge
// overrides, provider key names (or their BOM-polluted variants), and must
// carry the recursion latch. A nested gateway shell would otherwise make
// `agy models` return the 8-entry gateway list (E4 caveat, Q7).
func TestFetchChildEnvScrubbed(t *testing.T) {
	f, envFile := newStubFetcher(t, q6Catalog)
	f.ScrubEnvNames = []string{"TESTPROV_KEY"}
	t.Setenv("AGY_LLM_GATEWAY_URL", "http://127.0.0.1:1")
	t.Setenv("AGY_LLM_GATEWAY_WIRE_PROTOCOL", "openai")
	t.Setenv("AGY_LLM_GATEWAY_API_KEY", "session-secret-value")
	t.Setenv("AGY_LLM_GATEWAY_MODELS", "glm-5.3-flash-flex")
	t.Setenv("AGY_PRO_BRIDGE", "1")
	t.Setenv("AGY_BRIDGE", "1")
	t.Setenv("TESTPROV_KEY", "sk-planted-value")
	t.Setenv("\ufeffTESTPROV_KEY", "sk-planted-bom-value")

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	dump := readChildEnv(t, envFile)
	for _, banned := range []string{
		"AGY_LLM_GATEWAY_URL=",
		"AGY_LLM_GATEWAY_WIRE_PROTOCOL=",
		"AGY_LLM_GATEWAY_API_KEY=",
		"AGY_LLM_GATEWAY_MODELS=",
		"AGY_PRO_BRIDGE=",
		"AGY_BRIDGE=",
		"sk-planted-value",
		"sk-planted-bom-value",
	} {
		if strings.Contains(dump, banned) {
			t.Errorf("child env leaks %q:\n%s", banned, dump)
		}
	}
	for _, e := range strings.Split(dump, "\n") {
		name := e
		if i := strings.Index(e, "="); i >= 0 {
			name = e[:i]
		}
		// normalizeEnvName-equivalent (F-Db): U+FEFF stripped anywhere.
		if strings.TrimSpace(strings.ReplaceAll(name, "\ufeff", "")) == "TESTPROV_KEY" {
			t.Errorf("provider key env name survived scrub: %q (entry %q)", name, e)
		}
	}
	if !strings.Contains(dump, paths.EnvAgyProActive+"=1") {
		t.Errorf("child env missing recursion latch %s=1:\n%s", paths.EnvAgyProActive, dump)
	}
}

// Q7 fixture + the scrubbed fetcher: the gateway-contaminated CATALOG shape
// parses (8 rows), and the env assertion above is what keeps the real fetch
// from ever returning it. Both halves are pinned so the pair cannot drift.
func TestGatewayContaminatedCatalogNeverReachableWithScrub(t *testing.T) {
	models := parseModelsOutput(q7GatewayCatalog)
	if len(models) != 8 {
		t.Fatalf("Q7 fixture must parse as 8 rows (contamination symptom), got %d", len(models))
	}
	f, envFile := newStubFetcher(t, q7GatewayCatalog)
	t.Setenv("AGY_LLM_GATEWAY_MODELS", "glm-5.3-flash-flex")
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, e := range strings.Split(readChildEnv(t, envFile), "\n") {
		if strings.HasPrefix(e, "AGY_LLM_GATEWAY_MODELS=") {
			t.Fatalf("gateway models env reached the catalog child: %q", e)
		}
	}
}

func TestCachedServesWithinTTLAndRefetchesAfter(t *testing.T) {
	stub := &stubFetcher{models: q6Models()}
	c := Cached(stub, 80*time.Millisecond)

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("second fetch inside TTL hit the source: calls = %d, want 1", stub.calls)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 2 {
		t.Fatalf("fetch after TTL not refreshed: calls = %d, want 2", stub.calls)
	}
}

// F-Da: the fetch deadline (15s) and the cache TTL (5s) are DISTINCT values
// (R-11). A fetch that is slower than the TTL but well under the deadline must
// succeed AND its result must then be served from cache; conflating the two
// made a slow successful fetch expire its own result.
func TestFetchDeadlineDistinctFromCacheTTL(t *testing.T) {
	if DefaultFetchDeadline != 15*time.Second {
		t.Errorf("FetchDeadline = %v, want 15s", DefaultFetchDeadline)
	}
	if DefaultCacheTTL != 5*time.Second {
		t.Errorf("CacheTTL = %v, want 5s", DefaultCacheTTL)
	}
	if DefaultFetchDeadline <= DefaultCacheTTL {
		t.Fatalf("deadline %v must exceed cache TTL %v", DefaultFetchDeadline, DefaultCacheTTL)
	}

	// Scaled-down reproduction (80ms fetch vs 40ms TTL, 2s deadline): the
	// TTL clock starts at fetch COMPLETION (the F-Da fix), so the follow-up
	// call is cache-served with exactly one source hit.
	stub := &stubFetcher{models: q6Models(), delay: 80 * time.Millisecond}
	c := Cached(stub, 40*time.Millisecond)

	start := time.Now()
	models, err := c.Fetch(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("slow-but-under-deadline fetch failed: %v", err)
	}
	if elapsed < 70*time.Millisecond {
		t.Fatalf("stub delay not observed (%v); test is not exercising the slow path", elapsed)
	}
	if len(models) != 18 {
		t.Fatalf("slow fetch returned %d models, want 18", len(models))
	}
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 {
		t.Fatalf("slow successful fetch expired its own result: calls = %d, want 1 (cache served)", stub.calls)
	}
}

// FetchDeadline is a real per-fetch bound: a child that outlives it fails the
// fetch instead of hanging the catalog.
func TestFetchDeadlineKillsSlowChild(t *testing.T) {
	f, _ := newStubFetcher(t, q6Catalog)
	f.FetchDeadline = 100 * time.Millisecond
	t.Setenv("AGY_PRO_STUB_SLEEP_MS", "1500")
	start := time.Now()
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("fetch must fail when the child exceeds FetchDeadline")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("deadline not enforced: fetch ran %v", time.Since(start))
	}
}

func TestFetchEmptyOutputIsError(t *testing.T) {
	f, _ := newStubFetcher(t, "Fetching available models...\nno tabs here\n")
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("empty parseable catalog must error")
	}
}

// A9: the `agy models` stderr tail used to be interpolated into the fetch
// error unredacted. That error text reaches the console AND
// Inventory.NativeErr (rendered by `agy pro models --all`, the launcher picker
// and the swarm builder), so a credential fragment echoed by a failing agy
// build leaked. It now goes through the same Redactor the fanout CLI applies
// to provider error snippets.
func TestFetchFailureRedactsStderrTail(t *testing.T) {
	const planted = "sk-planted-models-stderr-secret-42"
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("AGY_PRO_STUB_STDOUT", "")
	t.Setenv("AGY_PRO_STUB_STDERR", "AGY_ERROR: unauthorized\nAuthorization: Bearer "+planted+"\n")
	t.Setenv("AGY_PRO_STUB_EXIT", "3")
	envFile := filepath.Join(t.TempDir(), "child-env.txt")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)
	t.Setenv("AGY_PRO_STUB_SLEEP_MS", "")

	f := &RealAgyModels{BinPath: "fake-agy", FetchDeadline: DefaultFetchDeadline, Spawn: spawnStub()}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("a failing `agy models` child must fail the fetch")
	}
	if strings.Contains(err.Error(), planted) {
		t.Errorf("fetch error leaked the credential fragment: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("fetch error = %v; want a [REDACTED] marker", err)
	}
	if !strings.Contains(err.Error(), "agy models failed") {
		t.Errorf("fetch error = %v; want the agy-models-failed wording", err)
	}

	// The redacted text also survives into Inventory.NativeErr, which the
	// consumers render.
	inv, lerr := Load(context.Background(), nil, f)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if inv.NativeErr == nil {
		t.Fatal("NativeErr must record the failed fetch (honest degrade)")
	}
	if strings.Contains(inv.NativeErr.Error(), planted) {
		t.Errorf("Inventory.NativeErr leaked the credential fragment: %v", inv.NativeErr)
	}
	if !strings.Contains(inv.NativeErr.Error(), "[REDACTED]") {
		t.Errorf("Inventory.NativeErr = %v; want a [REDACTED] marker", inv.NativeErr)
	}
}

// R5B-FIX-4: NewRealAgyFetcher must scrub with the FIX-4 UNION (registry
// apiKeyEnv names UNION the compiled-in known-preset KeyEnv names), not the
// registry-only list. An exported preset key for a provider that is ABSENT from
// providers.json used to ride into the `agy models` subprocess - the S-3 class.
func TestNewRealAgyFetcherWiresScrubNames(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "a", APIKeyEnv: "A_KEY"},
		{Name: "b", APIKeyEnv: "B_KEY"},
		{Name: "c", APIKeyEnv: "A_KEY"}, // duplicate name collapses
		{Name: "d"},
	}}
	f := NewRealAgyFetcher(reg)
	if f.FetchDeadline != DefaultFetchDeadline {
		t.Errorf("FetchDeadline = %v, want %v", f.FetchDeadline, DefaultFetchDeadline)
	}
	// Scrub comparison is case-insensitive by construction (CleanChildEnv folds
	// both sides through normalizeEnvName), so the assertion folds too.
	got := map[string]bool{}
	for _, n := range f.ScrubEnvNames {
		got[strings.ToLower(strings.TrimSpace(n))] = true
	}
	// The registry names are all present (and only once each).
	for _, want := range []string{"a_key", "b_key"} {
		if !got[want] {
			t.Errorf("ScrubEnvNames = %v; want the registry name %q", f.ScrubEnvNames, want)
		}
	}
	// The preset union is present: GROQ_API_KEY is a known preset whose provider
	// is NOT in this registry, which is exactly the hole FIX-4 closes.
	for _, want := range []string{"groq_api_key", "openai_api_key", "deepseek_api_key"} {
		if !got[want] {
			t.Errorf("ScrubEnvNames = %v; want the preset name %q (FIX-4 union)", f.ScrubEnvNames, want)
		}
	}
	if len(f.ScrubEnvNames) < 2+len(providers.KnownProviders()) {
		t.Errorf("ScrubEnvNames = %v; want at least the registry names plus every preset", f.ScrubEnvNames)
	}
	// A nil registry still yields the preset union: the presets are independent
	// of providers.json, so an unreadable registry must not widen the hole.
	if NewRealAgyFetcher(nil).ScrubEnvNames == nil {
		t.Error("a nil registry must still yield the preset union, not nil")
	}
}

// R5B-FIX-4 (end to end): with a planted EXPORTED preset key whose provider is
// absent from the registry, the `agy models` fetch child's environment must not
// contain that key NAME (so its value cannot ride into the subprocess).
func TestFetchChildEnvScrubsPresetKeysAbsentFromRegistry(t *testing.T) {
	f, envFile := newStubFetcher(t, q6Catalog)
	// The constructor's union is what Fetch hands to CleanChildEnv.
	f.ScrubEnvNames = KeyScrubNames(&providers.Registry{Providers: []providers.Provider{
		{Name: "onlyprov", APIKeyEnv: "ONLYPROV_KEY"},
	}})
	// A planted FAKE preset key for a provider the registry does not carry.
	const planted = "sk-planted-groq-fake-secret"
	t.Setenv("GROQ_API_KEY", planted)
	t.Setenv("ONLYPROV_KEY", planted)

	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	dump := readChildEnv(t, envFile)
	if strings.Contains(dump, "GROQ_API_KEY=") {
		t.Errorf("the fetch child env carries the preset key NAME (FIX-4 union gap):\n%s", dump)
	}
	if strings.Contains(dump, planted) {
		t.Errorf("the fetch child env carries a planted key VALUE:\n%s", dump)
	}
	if strings.Contains(dump, "ONLYPROV_KEY=") {
		t.Errorf("the fetch child env carries the registry key NAME:\n%s", dump)
	}
	if !strings.Contains(dump, paths.EnvAgyProActive+"=1") {
		t.Errorf("child env missing recursion latch:\n%s", dump)
	}
}

// TestKeyScrubNamesMatchesHybridUnion is the drift guard: internal/inventory
// cannot import internal/hybrid in production (it would drag the
// mesh/wireadp/wirecheck graph into every inventory consumer), so the union is
// mirrored here. This test imports hybrid to pin the two lists equal as SETS,
// so the mirror cannot silently drift.
func TestKeyScrubNamesMatchesHybridUnion(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "a", APIKeyEnv: "A_KEY"},
		{Name: "b", APIKeyEnv: "B_KEY"},
		{Name: "c", APIKeyEnv: "A_KEY"},
		{Name: "d"},
	}}
	fold := func(names []string) map[string]bool {
		out := map[string]bool{}
		for _, n := range names {
			out[strings.ToLower(strings.TrimSpace(n))] = true
		}
		return out
	}
	mine := fold(KeyScrubNames(reg))
	theirs := fold(hybrid.ChildKeyScrubNames(reg))
	if len(mine) != len(theirs) {
		t.Fatalf("scrub union sizes differ: inventory %d vs hybrid %d", len(mine), len(theirs))
	}
	for n := range theirs {
		if !mine[n] {
			t.Errorf("inventory's KeyScrubNames is missing %q, which hybrid.ChildKeyScrubNames carries", n)
		}
	}
	// The nil-registry shape must agree too (the FIX-4 always-on preset half).
	if len(fold(KeyScrubNames(nil))) != len(fold(hybrid.ChildKeyScrubNames(nil))) {
		t.Error("the nil-registry scrub unions differ")
	}
}

// A9/F-1 (ordering half): the stderr tail must be REDACTED BEFORE it is cut to
// its last 200 bytes. Cutting first deleted the "sk-" head of a credential the
// child positioned across the cut and left the unshaped remainder in the fetch
// error and in Inventory.NativeErr. openaiup.Call and wireadp.sanitizeMessage
// document the opposite order; this sink was the one place that inverted it.
func TestFetchFailureRedactsBeforeCuttingStderrTail(t *testing.T) {
	// A shaped secret whose head ("sk-") is what a cut-first implementation
	// drops: the surviving fragment matches no redactor pattern, so it can only
	// be masked while the secret is still whole.
	const planted = "sk-planted-inventory-straddle-secret-42"
	// The secret sits early and 166 bytes of filler follow it, so the 200-byte
	// cut lands INSIDE the secret: the head is dropped and the remainder
	// straddles the boundary.
	stderr := "AGY_ERROR: unauthorized\n" + planted + strings.Repeat("y", 166)
	if got := len(stderr) - 200; got <= len("AGY_ERROR: unauthorized\n") || got >= len(planted) {
		t.Fatalf("fixture does not straddle the cut: cut at %d, secret spans [23,%d)", got, len(planted))
	}
	frag := planted[7:] // the part a cut-first implementation carried raw

	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("AGY_PRO_STUB_STDOUT", "")
	t.Setenv("AGY_PRO_STUB_STDERR", stderr)
	t.Setenv("AGY_PRO_STUB_EXIT", "3")
	t.Setenv("AGY_PRO_STUB_ENVFILE", filepath.Join(t.TempDir(), "child-env.txt"))
	t.Setenv("AGY_PRO_STUB_SLEEP_MS", "")

	f := &RealAgyModels{
		BinPath:       "fake-agy",
		FetchDeadline: DefaultFetchDeadline,
		Spawn:         spawnStub(),
	}
	_, err := f.Fetch(context.Background())
	if err == nil {
		t.Fatal("a failing `agy models` child must fail the fetch")
	}
	msg := err.Error()
	if strings.Contains(msg, planted) {
		t.Errorf("fetch error leaked the whole planted secret: %v", msg)
	}
	// The straddling fragment: what a cut-first implementation carried raw.
	if strings.Contains(msg, frag) {
		t.Errorf("fetch error leaked the straddling fragment %q (the tail must be redacted before the cut): %v", frag, msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("fetch error = %v; want a [REDACTED] marker", msg)
	}

	// The same text must not survive into Inventory.NativeErr either.
	inv, lerr := Load(context.Background(), nil, f)
	if lerr != nil {
		t.Fatalf("Load: %v", lerr)
	}
	if inv.NativeErr == nil {
		t.Fatal("NativeErr must record the failed fetch (honest degrade)")
	}
	if strings.Contains(inv.NativeErr.Error(), frag) {
		t.Errorf("Inventory.NativeErr leaked the straddling fragment %q: %v", frag, inv.NativeErr)
	}
}

// A9/F-1 (seeding half): the failure sink must use the SEEDED redactor, not the
// unseeded generic one. A registered key value that matches no generic shape
// (opaque hex) is only maskable by literal replacement, which is exactly what
// the fanout CLI's reg.Redactor() provides and NewRedactor() does not.
func TestNewRealAgyFetcherSeedsTheFailureRedactor(t *testing.T) {
	const planted = "9f2c7a1b4e6d8f0a3b5c7d9e1f2a4b6c8d0e2f4a"
	t.Setenv("TEST_INVENTORY_SEED_KEY", planted)
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "seedprov", BaseURL: "http://127.0.0.1:9/v1", APIKeyEnv: "TEST_INVENTORY_SEED_KEY",
			Models: []providers.Model{{ID: "seed-model", ContextLength: 300000}}},
	}}

	f := NewRealAgyFetcher(reg)
	if f.Redact == nil {
		t.Fatal("NewRealAgyFetcher must wire a redactor (the seeded one, not the generic shapes)")
	}
	// The constructor's seeded redactor masks the registered key value.
	if got := f.Redact("upstream echoed " + planted); strings.Contains(got, planted) {
		t.Errorf("seeded redactor leaked the registered key value: %q", got)
	} else if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("seeded redactor did not mask the registered key value: %q", got)
	}

	// End to end through Fetch: the opaque value in the child's stderr never
	// reaches the error string, and the child env never carries the key name.
	f.Spawn = spawnStub()
	f.BinPath = "fake-agy"
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("AGY_PRO_STUB_STDOUT", "")
	t.Setenv("AGY_PRO_STUB_STDERR", "AGY_ERROR: upstream rejected "+planted+"\n")
	t.Setenv("AGY_PRO_STUB_EXIT", "3")
	envFile := filepath.Join(t.TempDir(), "child-env.txt")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)
	t.Setenv("AGY_PRO_STUB_SLEEP_MS", "")

	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("a failing `agy models` child must fail the fetch")
	} else if strings.Contains(err.Error(), planted) {
		t.Errorf("fetch error leaked the registered key value: %v", err)
	}
	if env := readChildEnv(t, envFile); strings.Contains(env, "TEST_INVENTORY_SEED_KEY="+planted) {
		t.Error("the fetch child env carries the provider key value (the scrub must drop the name)")
	}
}
