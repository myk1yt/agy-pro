package launcher

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// U7 community banner (contract §2.10): one frozen constant, one render site
// (ShowMenu), exactly once per interactive launcher invocation, and NEVER in
// print mode / piped output / machine output.

const communityBannerFrozen = "Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI"

func TestCommunityBannerConstantIsFrozen(t *testing.T) {
	if CommunityBanner != communityBannerFrozen {
		t.Fatalf("CommunityBanner drifted from contract §2.10.\ngot:  %q\nwant: %q", CommunityBanner, communityBannerFrozen)
	}
}

// (a) interactive launcher transcript contains the banner exactly once.
func TestCommunityBannerExactlyOnceInShowMenuTranscript(t *testing.T) {
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("1\n"))
	got := ShowMenu(&out, nil, rel, 3, "alpha, beta", time.Second)
	if got != RouteNative {
		t.Fatalf("route = %s, want native", got)
	}
	transcript := out.String()
	if n := strings.Count(transcript, CommunityBanner); n != 1 {
		t.Fatalf("banner occurrences = %d, want exactly 1:\n%s", n, transcript)
	}
	// Placement: after the === header block, before the [1]/[2]/[3] options.
	lastRule := strings.LastIndex(transcript, "===========================================================")
	banner := strings.Index(transcript, CommunityBanner)
	firstOpt := strings.Index(transcript, "  [1] Native Gemini (default)")
	if firstOpt < 0 {
		t.Fatalf("options block missing:\n%s", transcript)
	}
	if !(lastRule < banner && banner < firstOpt) {
		t.Fatalf("banner placement wrong (rule=%d banner=%d options=%d):\n%s", lastRule, banner, firstOpt, transcript)
	}
}

// TestBannerHelperChild is the fake "real agy" child for banner routing
// tests: it dumps its environment and exits 0.
func TestBannerHelperChild(t *testing.T) {
	if os.Getenv("GO_WANT_BANNER_HELPER") != "1" {
		return
	}
	if p := os.Getenv("AGY_PRO_STUB_ENVFILE"); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}
	os.Exit(0)
}

// bannerHarness wires ExecutePassthrough's seams to the helper child and
// returns the captured stdout.
func bannerHarness(t *testing.T, args []string, tty bool, stdinLines string) string {
	t.Helper()

	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"google","baseURL":"http://127.0.0.1:1","models":[{"id":"gemini-2.5-flash"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv(paths.EnvAgyProHome, filepath.Join(tempDir, "home"))
	t.Setenv(paths.EnvAgyProActive, "")
	t.Setenv("AGY_PRO_BRIDGE", "")
	t.Setenv("AGY_BRIDGE", "")
	t.Setenv("AGY_LAUNCHER", "")
	t.Setenv("GO_WANT_BANNER_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ENVFILE", filepath.Join(tempDir, "child-env.txt"))

	oldStdin := os.Stdin
	if stdinLines != "" {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = w.WriteString(stdinLines)
			_ = w.Close()
		}()
		os.Stdin = r
		defer func() {
			os.Stdin = oldStdin
			_ = r.Close()
		}()
	}
	oldTTY := stdinIsTTY
	stdinIsTTY = func() bool { return tty }
	defer func() { stdinIsTTY = oldTTY }()

	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		return exec.Command(os.Args[0], "-test.run=TestBannerHelperChild")
	}
	defer func() { spawnRealAgy = oldSpawn }()

	var out bytes.Buffer
	oldStdout := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pw
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()

	code, err := ExecutePassthrough("dummy-real-agy", args)
	_ = pw.Close()
	os.Stdout = oldStdout
	captured := <-done
	_ = pr.Close()
	out.WriteString(captured)

	if err != nil {
		t.Fatalf("ExecutePassthrough: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	return out.String()
}

// (a) end-to-end: the interactive launcher path prints the banner exactly once.
func TestCommunityBannerExactlyOnceOnInteractiveLaunch(t *testing.T) {
	got := bannerHarness(t, nil, true, "1\n")
	if n := strings.Count(got, CommunityBanner); n != 1 {
		t.Fatalf("interactive launch banner occurrences = %d, want 1:\n%s", n, got)
	}
	if strings.Count(got, "reddit.com") != 1 {
		t.Fatalf("reddit.com occurrences = %d, want 1:\n%s", strings.Count(got, "reddit.com"), got)
	}
}

// (b) print mode (-p): zero banner hits in captured output.
func TestCommunityBannerAbsentInPrintModeOutput(t *testing.T) {
	// TTY on purpose: -p must skip the menu even on an interactive console.
	got := bannerHarness(t, []string{"-p", "reply OK"}, true, "")
	if strings.Contains(got, "reddit.com") {
		t.Fatalf("print-mode output leaked the community banner:\n%s", got)
	}
}

// (b) piped/non-TTY stdin: zero banner hits in captured output.
func TestCommunityBannerAbsentInPipedOutput(t *testing.T) {
	got := bannerHarness(t, nil, false, "")
	if strings.Contains(got, "reddit.com") {
		t.Fatalf("piped output leaked the community banner:\n%s", got)
	}
}

// (c) machine output: the banner constant is rendered ONLY inside ShowMenu.
// No other non-test source in the launcher package may reference it, and no
// route that produces machine output can reach ShowMenu (Decide rules).
func TestCommunityBannerSingleRenderSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// prompt.go holds the const declaration, its doc comment, and the
		// single render line (ShowMenu). Every other source must be clean.
		wantHits := 0
		if name == "prompt.go" {
			wantHits = 3
		}
		if got := strings.Count(string(data), "CommunityBanner"); got != wantHits {
			t.Errorf("%s references CommunityBanner %d times, want %d (sole render site = ShowMenu)", name, got, wantHits)
		}
	}
}
