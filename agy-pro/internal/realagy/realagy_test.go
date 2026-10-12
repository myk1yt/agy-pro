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
		if normalizeEnvName(name) == "NW_API_KEY" {
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
	got := ApiKeyEnvNames(reg)
	if len(got) != 2 || got[0] != "A_KEY" || got[1] != "B_KEY" {
		t.Errorf("ApiKeyEnvNames = %v; want [A_KEY B_KEY] deduped, empties skipped", got)
	}
}
