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
	if len(f.ScrubEnvNames) != 2 {
		t.Errorf("ScrubEnvNames = %v, want exactly A_KEY,B_KEY", f.ScrubEnvNames)
	}
	if NewRealAgyFetcher(nil).ScrubEnvNames != nil {
		t.Error("nil registry must yield nil scrub names")
	}
}
