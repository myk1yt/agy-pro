package providers

import (
	"agypro/internal/testfixtures"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// mockEnvStore is an in-memory EnvStore so every test below runs without
// touching the real Windows registry (the real windowsEnvStore refuses to run
// under tests anyway, mirroring the wizard's registry-persistence skip pattern).
type mockEnvStore struct {
	data        map[string]map[string]string
	setCalls    [][2]string // (root, name) of every Set
	deleteCalls [][2]string // (root, name) of every Delete
}

func newMockEnvStore() *mockEnvStore {
	return &mockEnvStore{data: map[string]map[string]string{
		EnvRootUser:    {},
		EnvRootMachine: {},
	}}
}

func (m *mockEnvStore) Names(root string) ([]string, error) {
	vars, ok := m.data[root]
	if !ok {
		return nil, fmt.Errorf("mock store: unknown root %q", root)
	}
	names := make([]string, 0, len(vars))
	for n := range vars {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (m *mockEnvStore) Get(root, name string) (string, bool, error) {
	vars, ok := m.data[root]
	if !ok {
		return "", false, fmt.Errorf("mock store: unknown root %q", root)
	}
	v, ok := vars[name]
	return v, ok, nil
}

func (m *mockEnvStore) Set(root, name, value string) error {
	vars, ok := m.data[root]
	if !ok {
		return fmt.Errorf("mock store: unknown root %q", root)
	}
	vars[name] = value
	m.setCalls = append(m.setCalls, [2]string{root, name})
	return nil
}

func (m *mockEnvStore) Delete(root, name string) error {
	vars, ok := m.data[root]
	if !ok {
		return fmt.Errorf("mock store: unknown root %q", root)
	}
	delete(vars, name)
	m.deleteCalls = append(m.deleteCalls, [2]string{root, name})
	return nil
}

// useMockEnvStore swaps the package default store for the mock and restores it
// on cleanup.
func useMockEnvStore(t *testing.T, m *mockEnvStore) {
	t.Helper()
	old := defaultEnvStore
	defaultEnvStore = m
	t.Cleanup(func() { defaultEnvStore = old })
}

func TestNormalizeEnvName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"NW_API_KEY", "NW_API_KEY"},
		{"\ufeffNW_API_KEY", "NW_API_KEY"},
		{"NW_API_KEY\ufeff", "NW_API_KEY"},
		{"N\ufeffW_API_KEY", "NW_API_KEY"},
		{"\ufeff\ufeffNW_API_KEY", "NW_API_KEY"},
		{"  NW_API_KEY  ", "NW_API_KEY"},
		{" \ufeff NW_API_KEY ", "NW_API_KEY"},
		{"", ""},
		{"\ufeff", ""},
	}
	for _, c := range cases {
		if got := normalizeEnvName(c.in); got != c.want {
			t.Errorf("normalizeEnvName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestResolveEnvCandidates(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"NW_API_KEY":       "mock-nw-key-999",
		"\ufeffNW_API_KEY": "sk-real-nw-key",
		"OTHER_KEY":        "other",
	}
	store.data[EnvRootMachine] = map[string]string{
		"\ufeffNW_API_KEY": "sk-machine-nw-key",
	}
	cands := resolveEnvCandidatesWithStore("NW_API_KEY", store)
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(cands), cands)
	}
	byRootName := map[string]EnvCandidate{}
	for _, c := range cands {
		byRootName[c.Root+"|"+c.Name] = c
	}
	if c := byRootName[EnvRootUser+"|NW_API_KEY"]; !c.Exact || c.Value != "mock-nw-key-999" {
		t.Errorf("user exact candidate wrong: %+v", c)
	}
	if c := byRootName[EnvRootUser+"|\ufeffNW_API_KEY"]; c.Exact || c.Value != "sk-real-nw-key" {
		t.Errorf("user BOM candidate wrong: %+v", c)
	}
	if c := byRootName[EnvRootMachine+"|\ufeffNW_API_KEY"]; c.Exact || c.Value != "sk-machine-nw-key" {
		t.Errorf("machine BOM candidate wrong: %+v", c)
	}

	if got := resolveEnvCandidatesWithStore("", store); got != nil {
		t.Errorf("empty name must yield no candidates, got %+v", got)
	}
	if got := resolveEnvCandidatesWithStore("NW_API_KEY", nil); got != nil {
		t.Errorf("nil store must yield no candidates, got %+v", got)
	}
}

func TestFindEnvNameConflicts(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_CONFLICT_KEY":       "dead",
		"\ufeffW6_CONFLICT_KEY": "real",
		"W6_CLEAN_KEY":          "only-exact",
		"\ufeffW6_ORPHAN_KEY":   "only-variant",
	}
	useMockEnvStore(t, store)

	conflicts := FindEnvNameConflicts("W6_CONFLICT_KEY", "W6_CLEAN_KEY", "W6_ORPHAN_KEY", "W6_UNKNOWN_KEY")
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %+v", conflicts)
	}
	if conflicts[0].Env != "W6_CONFLICT_KEY" {
		t.Errorf("conflict env = %q; want W6_CONFLICT_KEY", conflicts[0].Env)
	}
	if len(conflicts[0].Exact) != 1 || len(conflicts[0].Variants) != 1 {
		t.Errorf("conflict partition wrong: %+v", conflicts[0])
	}
}

func TestResolveEnvValue_Order(t *testing.T) {
	t.Run("exact process env wins", func(t *testing.T) {
		store := newMockEnvStore()
		store.data[EnvRootUser] = map[string]string{
			"W6_K":       "from-registry",
			"\ufeffW6_K": "from-bom",
		}
		useMockEnvStore(t, store)
		t.Setenv("W6_K", "from-env")
		v, origin, ok := resolveEnvValueWithStore("W6_K", store)
		if !ok || v != "from-env" || origin != KeyOriginProcessEnv {
			t.Errorf("got (%q, %q, %v); want (from-env, process-env, true)", v, origin, ok)
		}
	})

	t.Run("registry exact beats process BOM variant", func(t *testing.T) {
		store := newMockEnvStore()
		store.data[EnvRootUser] = map[string]string{"W6_K": "from-registry"}
		useMockEnvStore(t, store)
		t.Setenv("\ufeffW6_K", "from-bom-env")
		v, origin, ok := resolveEnvValueWithStore("W6_K", store)
		if !ok || v != "from-registry" || origin != KeyOriginUserExact {
			t.Errorf("got (%q, %q, %v); want (from-registry, hkcu-exact, true)", v, origin, ok)
		}
	})

	t.Run("BOM registry variant fallback", func(t *testing.T) {
		store := newMockEnvStore()
		store.data[EnvRootUser] = map[string]string{"\ufeffW6_K": "from-bom"}
		useMockEnvStore(t, store)
		v, origin, ok := resolveEnvValueWithStore("W6_K", store)
		if !ok || v != "from-bom" || origin != KeyOriginUserBOM {
			t.Errorf("got (%q, %q, %v); want (from-bom, hkcu-bom-variant, true)", v, origin, ok)
		}
	})

	t.Run("process BOM variant beats registry BOM variant", func(t *testing.T) {
		store := newMockEnvStore()
		store.data[EnvRootMachine] = map[string]string{"\ufeffW6_K": "from-machine-bom"}
		useMockEnvStore(t, store)
		t.Setenv("\ufeffW6_K", "from-process-bom")
		v, origin, ok := resolveEnvValueWithStore("W6_K", store)
		if !ok || v != "from-process-bom" || origin != KeyOriginProcessBOM {
			t.Errorf("got (%q, %q, %v); want (from-process-bom, process-env-bom-variant, true)", v, origin, ok)
		}
	})

	t.Run("machine exact", func(t *testing.T) {
		store := newMockEnvStore()
		store.data[EnvRootMachine] = map[string]string{"W6_K": "from-machine"}
		useMockEnvStore(t, store)
		v, origin, ok := resolveEnvValueWithStore("W6_K", store)
		if !ok || v != "from-machine" || origin != KeyOriginMachineExact {
			t.Errorf("got (%q, %q, %v); want (from-machine, hklm-exact, true)", v, origin, ok)
		}
	})

	t.Run("unresolved", func(t *testing.T) {
		store := newMockEnvStore()
		useMockEnvStore(t, store)
		if _, _, ok := resolveEnvValueWithStore("W6_MISSING", store); ok {
			t.Error("expected unresolved")
		}
		if _, _, ok := resolveEnvValueWithStore("", store); ok {
			t.Error("empty name must not resolve")
		}
	})
}

// S-3: resolving a key must never promote it into the process environment.
// The old KeyValue cached non-process values under the declared name via
// os.Setenv, which leaked store-only keys into every child process spawned
// from this shim (os.Environ inheritance). BOM-variant resolution still works;
// it just stays in-process.
func TestKeyValue_BOMFallbackDoesNotPromoteIntoProcessEnv(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{"\ufeffW6_KV_KEY": "sk-only-bom-value"}
	useMockEnvStore(t, store)
	t.Cleanup(func() { _ = os.Unsetenv("W6_KV_KEY") })
	_ = os.Unsetenv("W6_KV_KEY")

	p := Provider{Name: "nw", BaseURL: "https://nw.example/v1", APIKeyEnv: "W6_KV_KEY"}
	if got := p.KeyValue(); got != "sk-only-bom-value" {
		t.Fatalf("KeyValue = %q; want sk-only-bom-value", got)
	}
	if _, leaked := os.LookupEnv("W6_KV_KEY"); leaked {
		t.Error("KeyValue promoted a store-only key into the process environment")
	}
	// Subsequent lookups still resolve (through the store), without caching
	// into os.Environ.
	v, origin := p.KeyValueWithOrigin()
	if v != "sk-only-bom-value" || origin != KeyOriginUserBOM {
		t.Errorf("second lookup = (%q, %q); want (sk-only-bom-value, hkcu-bom-variant)", v, origin)
	}
	if _, leaked := os.LookupEnv("W6_KV_KEY"); leaked {
		t.Error("KeyValueWithOrigin promoted a store-only key into the process environment")
	}
}

func TestProbeKeyValidity(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer good-key" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"data":[]}`))
		}))
		defer server.Close()
		res := ProbeKeyValidity(server.URL, "good-key")
		if res.Status != ProbeValid || res.HTTPStatus != http.StatusOK {
			t.Errorf("got %+v; want valid/200", res)
		}
		// S-6: every probe names its destination host.
		wantHost := strings.TrimPrefix(server.URL, "http://")
		if res.Host != wantHost {
			t.Errorf("probe host = %q; want %q", res.Host, wantHost)
		}
		if !strings.Contains(res.Detail, "dest "+wantHost) {
			t.Errorf("probe detail must name the destination host: %q", res.Detail)
		}
	})

	t.Run("invalid redacts key from detail", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"Invalid API key %s"}}`, "sk-secret-value-9876")
		}))
		defer server.Close()
		res := ProbeKeyValidity(server.URL, "sk-secret-value-9876")
		if res.Status != ProbeInvalid || res.HTTPStatus != http.StatusUnauthorized {
			t.Errorf("got %+v; want invalid/401", res)
		}
		if strings.Contains(res.Detail, "sk-secret-value-9876") {
			t.Errorf("probe detail leaks the key: %q", res.Detail)
		}
	})

	t.Run("indeterminate on 404", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()
		res := ProbeKeyValidity(server.URL, "k")
		if res.Status != ProbeIndeterminate {
			t.Errorf("got %+v; want indeterminate", res)
		}
	})

	t.Run("skipped on unreachable network", func(t *testing.T) {
		res := ProbeKeyValidity("http://127.0.0.1:59997", "k")
		if res.Status != ProbeSkipped {
			t.Errorf("got %+v; want skipped", res)
		}
	})

	t.Run("skipped on empty inputs", func(t *testing.T) {
		if res := ProbeKeyValidity("", "k"); res.Status != ProbeSkipped {
			t.Errorf("empty baseURL: got %+v", res)
		}
		if res := ProbeKeyValidity("https://x.example", ""); res.Status != ProbeSkipped {
			t.Errorf("empty key: got %+v", res)
		}
	})
}

// TestMaskSecret pins the canonical U8 mask shape (contract §2.11): first 5
// RUNES + "****", values of <= 5 runes fully hidden, no trailing suffix ever.
// The old prefix6…suffix4 shape is deliberately gone (R-12: suffix exposure
// violates the first-5 rule).
func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "****"},            // empty → full mask
		{"a", "****"},           // shorter than 5
		{"abcde", "****"},       // exactly 5 runes → full mask
		{"abcdef", "abcde****"}, // 6 runes → first 5 + mask
		{testfixtures.SKProjMixed, "sk-pr****"},
		{"sk-a83a3c2abcdef1234567890b7569", "sk-a8****"},
		{"12345678", "12345****"},
		{"日本語キーX", "日本語キー****"}, // 6 runes, multi-byte → rune boundary
		{"日本語キー", "****"},       // exactly 5 multi-byte runes
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q) = %q; want %q", c.in, got, c.want)
		}
	}
	// A short key is never echoed even partially (contract: <= 5 runes → "****").
	if got := MaskSecret("sk-12"); got != "****" {
		t.Errorf("MaskSecret of a 5-rune key = %q; want \"****\"", got)
	}
	long := "sk-a83a3c2abcdef1234567890b7569"
	got := MaskSecret(long)
	if strings.Contains(got, long) {
		t.Errorf("MaskSecret leaks the full secret: %q", got)
	}
	// No suffix exposure: nothing after the first 5 runes survives.
	for _, suf := range []string{"7569", "b7569", "90b7569", "3c2abcdef"} {
		if strings.Contains(got, suf) {
			t.Errorf("MaskSecret(%q) = %q leaks suffix material %q", long, got, suf)
		}
	}
	if got != "sk-a8****" {
		t.Errorf("MaskSecret(%q) = %q; want canonical first5+\"****\"", long, got)
	}
}

func TestAuditProviderKeys(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_NW_KEY":       "mock-nw-key-999",
		"\ufeffW6_NW_KEY": "sk-real-nw-valid-key",
		"W6_CHARM_KEY":    "sk-charm-fine-key",
	}
	useMockEnvStore(t, store)
	t.Cleanup(func() { _ = os.Unsetenv("W6_NW_KEY"); _ = os.Unsetenv("W6_CHARM_KEY") })
	_ = os.Unsetenv("W6_NW_KEY")
	_ = os.Unsetenv("W6_CHARM_KEY")

	reg := &Registry{Providers: []Provider{
		{Name: "nw", BaseURL: "https://nw.example/v1", APIKeyEnv: "W6_NW_KEY"},
		{Name: "charm", BaseURL: "https://charm.example/v1", APIKeyEnv: "W6_CHARM_KEY"},
		{Name: "ghost", BaseURL: "https://ghost.example/v1", APIKeyEnv: "W6_GHOST_KEY"},
		{Name: "nokey", BaseURL: "https://x.example/v1"},
	}}

	probe := func(baseURL, apiKey string) KeyProbeResult {
		switch apiKey {
		case "mock-nw-key-999":
			return KeyProbeResult{Status: ProbeInvalid, HTTPStatus: 401, Detail: "invalid_api_key"}
		case "sk-real-nw-valid-key", "sk-charm-fine-key":
			return KeyProbeResult{Status: ProbeValid, HTTPStatus: 200}
		default:
			return KeyProbeResult{Status: ProbeIndeterminate, Detail: "unexpected key in probe"}
		}
	}

	audits := AuditProviderKeys(reg, AuditOptions{Probe: probe, Store: store})
	if len(audits) != 3 {
		t.Fatalf("expected audits for 3 keyed providers, got %d", len(audits))
	}
	byProvider := map[string]KeyAudit{}
	for _, a := range audits {
		byProvider[a.Provider] = a
	}

	nw := byProvider["nw"]
	if !nw.Resolved || nw.Origin != KeyOriginUserExact {
		t.Errorf("nw origin = (%v, %q); want resolved/hkcu-exact (exact wins)", nw.Resolved, nw.Origin)
	}
	if !nw.ExactPresent || !nw.VariantPresent || !nw.Conflict {
		t.Errorf("nw conflict flags wrong: exact=%v variant=%v conflict=%v", nw.ExactPresent, nw.VariantPresent, nw.Conflict)
	}
	if nw.Probe.Status != ProbeInvalid {
		t.Errorf("nw probe = %+v; want invalid", nw.Probe)
	}
	if nw.MaskedHint != MaskSecret("mock-nw-key-999") || strings.Contains(nw.MaskedHint, "mock-nw-key-999") {
		t.Errorf("nw masked hint wrong: %q", nw.MaskedHint)
	}
	for _, c := range nw.Candidates {
		if strings.Contains(c.MaskedValue, "mock-nw-key-999") || strings.Contains(c.MaskedValue, "sk-real-nw-valid-key") {
			t.Errorf("candidate leaks a raw value: %+v", c)
		}
	}

	charm := byProvider["charm"]
	if !charm.Resolved || charm.Conflict || charm.VariantPresent {
		t.Errorf("charm flags wrong: %+v", charm)
	}
	if charm.Probe.Status != ProbeValid {
		t.Errorf("charm probe = %+v; want valid", charm.Probe)
	}

	ghost := byProvider["ghost"]
	if ghost.Resolved || ghost.Probe.Status != ProbeSkipped {
		t.Errorf("ghost audit wrong: %+v", ghost)
	}
}

func TestAuditProviderKeys_NilRegistry(t *testing.T) {
	if got := AuditProviderKeys(nil, AuditOptions{}); got != nil {
		t.Errorf("nil registry must yield no audits, got %+v", got)
	}
}

func TestWindowsEnvStoreRefusesUnderTests(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only guard")
	}
	var s windowsEnvStore
	if _, err := s.Names(EnvRootUser); err == nil {
		t.Error("real store must refuse to enumerate under tests")
	}
	if err := s.Set(EnvRootUser, "X", "y"); err == nil {
		t.Error("real store must refuse to write under tests")
	}
	if err := s.Delete(EnvRootUser, "X"); err == nil {
		t.Error("real store must refuse to delete under tests")
	}
}

// S-11: the test guard is exact (testing.Testing()), not a binary-name
// heuristic, and stays a single shared helper for the wizard persistence,
// the env store and the key-repair path.
func TestIsRunningTestsIsExact(t *testing.T) {
	if !isRunningTests() {
		t.Error("isRunningTests() must be true under `go test`")
	}
}

// fakeRegBackend is an in-memory registryBackend: it proves the store path
// carries names and values as data (never as a process command line) and lets
// tests drive Set/Delete without the real hive.
type fakeRegBackend struct {
	data map[string]map[string]string
}

func newFakeRegBackend() *fakeRegBackend {
	return &fakeRegBackend{data: map[string]map[string]string{EnvRootUser: {}, EnvRootMachine: {}}}
}

func (f *fakeRegBackend) enumerate(root string) (map[string]string, error) {
	vars, ok := f.data[root]
	if !ok {
		return nil, fmt.Errorf("fake registry: unknown root %q", root)
	}
	out := make(map[string]string, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	return out, nil
}

func (f *fakeRegBackend) query(root, name string) (string, bool, error) {
	vars, ok := f.data[root]
	if !ok {
		return "", false, fmt.Errorf("fake registry: unknown root %q", root)
	}
	v, ok := vars[name]
	return v, ok, nil
}

func (f *fakeRegBackend) set(root, name, value string) error {
	vars, ok := f.data[root]
	if !ok {
		return fmt.Errorf("fake registry: unknown root %q", root)
	}
	vars[name] = value
	return nil
}

func (f *fakeRegBackend) delete(root, name string) error {
	vars, ok := f.data[root]
	if !ok {
		return fmt.Errorf("fake registry: unknown root %q", root)
	}
	delete(vars, name)
	return nil
}

// S-2: names and values reach the registry backend as data. The old
// powershell.exe path put both on the argv base64-encoded; the backend seam
// shows they now travel as plain arguments to a Go interface (the win32
// implementation passes them as UTF-16 pointers the same way).
func TestWindowsEnvStoreRegistryBackendRoundTrip(t *testing.T) {
	backend := newFakeRegBackend()
	s := &windowsEnvStore{reg: backend}

	bomName := BOM + "W6_S2_KEY"
	if err := s.Set(EnvRootUser, bomName, "sk-s2-value-0001"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := backend.data[EnvRootUser][bomName]; got != "sk-s2-value-0001" {
		t.Fatalf("backend stored %q (want the exact value, no encoding step)", got)
	}
	if v, ok, err := s.Get(EnvRootUser, bomName); err != nil || !ok || v != "sk-s2-value-0001" {
		t.Fatalf("Get after Set = (%q, %v, %v); want the stored value", v, ok, err)
	}
	names, err := s.Names(EnvRootUser)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 1 || names[0] != bomName {
		t.Fatalf("Names = %v; want exactly [%q] (BOM-polluted name must survive)", names, bomName)
	}

	// A write invalidates the snapshot cache: the next read must see it.
	if err := s.Set(EnvRootUser, bomName, "sk-s2-value-0002"); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	if v, _, _ := s.Get(EnvRootUser, bomName); v != "sk-s2-value-0002" {
		t.Fatalf("stale snapshot served %q after Set", v)
	}

	if err := s.Delete(EnvRootUser, bomName); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(EnvRootUser, bomName); ok {
		t.Fatal("value still visible after Delete")
	}
	if err := s.Set(EnvRootMachine, "X", "y"); err != nil {
		t.Fatalf("machine-root Set must reach the backend: %v", err)
	}
	if err := s.Set("bogus-root", "X", "y"); err == nil {
		t.Error("unsupported root must be rejected")
	}
}

// S-2: the wizard's persistence path writes through the environment store
// (the registry backend), not through a powershell.exe command line.
func TestPersistWindowsUserEnvGoesThroughStore(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	m := newMockEnvStore()
	useMockEnvStore(t, m)
	if err := persistWindowsUserEnv("W6_S2_PERSIST", "sk-never-on-a-cmdline"); err != nil {
		t.Fatalf("persistWindowsUserEnv: %v", err)
	}
	if got := m.data[EnvRootUser]["W6_S2_PERSIST"]; got != "sk-never-on-a-cmdline" {
		t.Fatalf("value did not reach the registry store: %q", got)
	}
}

// S-2: one read-only smoke of the real advapi32 path (explicitly allowed;
// writes stay forbidden). Values are read into memory only and never printed.
func TestWin32RegistryReadOnlySmoke(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	s := &windowsEnvStore{reg: win32Registry{}}
	names, err := s.Names(EnvRootUser)
	if err != nil {
		t.Fatalf("read-only enumerate of HKCU\\Environment failed: %v", err)
	}
	if len(names) == 0 {
		t.Error("expected at least one user environment variable")
	}
	// Deliberately no value assertions: nothing sensitive may enter test output.
}
