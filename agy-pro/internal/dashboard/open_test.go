package dashboard

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestLoopbackURL pins the one URL shape the package produces.
func TestLoopbackURL(t *testing.T) {
	if got, want := LoopbackURL(PreferredPort), "http://127.0.0.1:8787/"; got != want {
		t.Fatalf("LoopbackURL(%d) = %q, want %q", PreferredPort, got, want)
	}
	for _, port := range []int{0, -1, 65536} {
		if got := LoopbackURL(port); got != "" {
			t.Fatalf("LoopbackURL(%d) = %q, want the empty string", port, got)
		}
	}
}

// TestValidateLoopbackURL is the whitelist matrix: the loopback dashboard URL
// (bare or with a plain path) passes, and every other shape is refused before a
// process is built.
func TestValidateLoopbackURL(t *testing.T) {
	accepted := []string{
		"http://127.0.0.1:8787",
		"http://127.0.0.1:8787/",
		"http://127.0.0.1:8787/data.json",
		"http://127.0.0.1:1/",
		"http://127.0.0.1:65535/",
	}
	for _, u := range accepted {
		t.Run("accept "+u, func(t *testing.T) {
			got, err := validateLoopbackURL(u)
			if err != nil {
				t.Fatalf("validateLoopbackURL(%q) = %v, want it accepted", u, err)
			}
			if got != u {
				t.Fatalf("validateLoopbackURL(%q) rewrote the target to %q", u, got)
			}
		})
	}

	refused := []struct {
		url  string
		kind string // "url" for RefusedURLError, "port" for PortError
	}{
		{"file:///C:/Users/x/dashboard.html", "url"},
		{"file://127.0.0.1:8787/", "url"},
		{"http://evil.com/", "url"},
		{"http://evil.com:8787/", "url"},
		{"http://127.0.0.1:8787@evil.com/", "url"},
		{"http://localhost:8787/", "url"},
		{"http://127.0.0.1.evil.com:8787/", "url"},
		{"https://127.0.0.1:8787/", "url"},
		{"HTTP://127.0.0.1:8787/", "url"},
		{"http://127.0.0.1:8787/?q=1", "url"},
		{"http://127.0.0.1:8787/#frag", "url"},
		{"http://127.0.0.1:8787/a&b", "url"},
		{"http://127.0.0.1:8787/../../etc/passwd", "url"},
		{"http://127.0.0.1:8787/%2e%2e/", "url"},
		{"http://127.0.0.1:8787/a b", "url"},
		{" http://127.0.0.1:8787/", "url"},
		{"http://127.0.0.1:8787/\n", "url"},
		{"http://127.0.0.1:/", "url"},
		{"http://127.0.0.1:87a7/", "url"},
		{"http://127.0.0.1:123456/", "url"},
		{"", "url"},
		{"http://127.0.0.1:0/", "port"},
		{"http://127.0.0.1:65536/", "port"},
	}
	for _, tc := range refused {
		t.Run("refuse "+tc.url, func(t *testing.T) {
			if _, err := validateLoopbackURL(tc.url); err == nil {
				t.Fatalf("validateLoopbackURL(%q) accepted a target outside the whitelist", tc.url)
			}
			var refusedErr *RefusedURLError
			var portErr *PortError
			switch tc.kind {
			case "port":
				if !errors.As(errFor(t, tc.url), &portErr) {
					t.Fatalf("%q did not fail as a *PortError: %v", tc.url, errFor(t, tc.url))
				}
			default:
				if !errors.As(errFor(t, tc.url), &refusedErr) {
					t.Fatalf("%q did not fail as a *RefusedURLError: %v", tc.url, errFor(t, tc.url))
				}
			}
		})
	}
}

// errFor runs the validator once and returns its error (the assertions above
// read it twice for readability).
func errFor(t *testing.T, url string) error {
	t.Helper()
	_, err := validateLoopbackURL(url)
	return err
}

// TestRefusedURLErrorEchoesNoTarget: a refused target is untrusted text, so the
// error names the reason and never the value.
func TestRefusedURLErrorEchoesNoTarget(t *testing.T) {
	hostile := "http://evil.example/leak-me"
	err := errFor(t, hostile)
	if err == nil {
		t.Fatalf("validateLoopbackURL(%q) accepted a hostile target", hostile)
	}
	if strings.Contains(err.Error(), "evil.example") || strings.Contains(err.Error(), "leak-me") {
		t.Fatalf("the refusal echoes the rejected target: %q", err.Error())
	}
}

// TestLaunchCommandArgv pins the per-OS opener argv. Pure string assertions: no
// opener is ever executed by this test.
func TestLaunchCommandArgv(t *testing.T) {
	const target = "http://127.0.0.1:8787/"
	cases := []struct {
		goos string
		want []string
	}{
		{"windows", []string{"cmd.exe", "/c", "start", "", target}},
		{"darwin", []string{"open", target}},
		{"linux", []string{"xdg-open", target}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			cmd, err := launchCommand(tc.goos, target)
			if err != nil {
				t.Fatalf("launchCommand(%s): %v", tc.goos, err)
			}
			if got := strings.Join(cmd.Args, "\x00"); got != strings.Join(tc.want, "\x00") {
				t.Fatalf("argv = %v, want %v", cmd.Args, tc.want)
			}
			// The title slot is what keeps `start` from treating the URL as one.
			if tc.goos == "windows" && cmd.Args[3] != "" {
				t.Fatalf("the windows opener lost its empty title slot: %v", cmd.Args)
			}
		})
	}
	if _, err := launchCommand("plan9", target); err == nil {
		t.Fatal("launchCommand accepted an OS with no known opener")
	}
}

// TestOpenInBrowserHandsTheWhitelistedArgvToTheSeam: the production entry point
// validates, builds the host opener and hands it to the exec edge. The seam
// records the argv, so this test never starts a browser.
func TestOpenInBrowserHandsTheWhitelistedArgvToTheSeam(t *testing.T) {
	var captured *exec.Cmd
	prev := runLauncher
	runLauncher = func(cmd *exec.Cmd) error {
		captured = cmd
		return nil
	}
	t.Cleanup(func() { runLauncher = prev })

	if err := OpenInBrowser(LoopbackURL(PreferredPort)); err != nil {
		t.Fatalf("OpenInBrowser: %v", err)
	}
	if captured == nil {
		t.Fatal("the launcher seam was never called")
	}
	want, err := launchCommand(runtime.GOOS, LoopbackURL(PreferredPort))
	if err != nil {
		t.Fatalf("launchCommand: %v", err)
	}
	if strings.Join(captured.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Fatalf("launched %v, want %v", captured.Args, want.Args)
	}
}

// TestOpenInBrowserRefusesWithoutLaunching is the no-browser proof: for every
// refused target the launcher seam is never reached, so no browser can start.
func TestOpenInBrowserRefusesWithoutLaunching(t *testing.T) {
	calls := 0
	prev := runLauncher
	runLauncher = func(cmd *exec.Cmd) error {
		calls++
		t.Fatalf("the launcher ran for a refused target: %v", cmd.Args)
		return nil
	}
	t.Cleanup(func() { runLauncher = prev })

	for _, target := range []string{
		"file:///C:/Users/x/.gemini/antigravity-dashboard/dashboard.html",
		"http://evil.com/",
		"http://127.0.0.1:8787@evil.com/",
		"https://127.0.0.1:8787/",
		"http://127.0.0.1:0/",
		"",
	} {
		if err := OpenInBrowser(target); err == nil {
			t.Fatalf("OpenInBrowser(%q) accepted a target outside the whitelist", target)
		}
	}
	if calls != 0 {
		t.Fatalf("the launcher ran %d times for refused targets, want 0", calls)
	}
}

// TestOpenInBrowserSurfacesLauncherFailure: a failing opener is reported, not
// swallowed (the screen turns it into a message line).
func TestOpenInBrowserSurfacesLauncherFailure(t *testing.T) {
	prev := runLauncher
	runLauncher = func(cmd *exec.Cmd) error { return errors.New("no browser registered for this protocol") }
	t.Cleanup(func() { runLauncher = prev })

	err := OpenInBrowser(LoopbackURL(PreferredPort))
	if err == nil {
		t.Fatal("a failed launch returned no error")
	}
	if !strings.Contains(err.Error(), "browser launch failed") {
		t.Fatalf("error = %q, want the named launch failure", err.Error())
	}
	if !strings.Contains(err.Error(), "no browser registered") {
		t.Fatalf("error = %q, want the opener's own reason", err.Error())
	}
}
