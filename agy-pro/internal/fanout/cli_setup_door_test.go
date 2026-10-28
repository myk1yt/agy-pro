package fanout

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// P5-1 R1: the fourth wizard door. `agy pro fanout --setup` used to call the
// 2-arg providers.InteractiveSetupWizard (which hard-codes Console: true) and
// offered no --yes, so a piped caller saw a [y/N] prompt nothing could answer
// and the refusal hint it printed (`re-run with --yes`) exited 2 here as an
// unknown flag. These pins cover the derived console fact, the --yes flag, and
// one real wizard run against a temp AGY_PRO_HOME on both outcomes.

// consoleFileForTest returns an *os.File on a real character device, which is
// the only shape the doors' console check accepts.
func consoleFileForTest(t *testing.T) *os.File {
	t.Helper()
	name := "/dev/null"
	if runtime.GOOS == "windows" {
		name = "NUL"
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestSetupStdinIsConsoleMatchesTheOtherDoors pins the predicate this door
// derives Console from: cmd/agy's stdinIsTTY (pro_swarm.go:111), restated in
// fanout because that one is package-private. A scripted reader, a buffer and
// an anonymous pipe are not consoles; a character device is.
func TestSetupStdinIsConsoleMatchesTheOtherDoors(t *testing.T) {
	if setupStdinIsConsole(nil) {
		t.Error("a nil reader must not read as a console")
	}
	if setupStdinIsConsole(strings.NewReader("")) {
		t.Error("a strings.Reader (every scripted/piped caller) must not read as a console")
	}
	if setupStdinIsConsole(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer must not read as a console")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	if setupStdinIsConsole(pr) {
		t.Error("an anonymous pipe must not read as a console")
	}
	if !setupStdinIsConsole(consoleFileForTest(t)) {
		t.Error("a character device must read as a console")
	}
}

// TestFanoutSetupDoorPassesTheConsentContext pins the plumbing: the door hands
// the wizard a DERIVED console fact and the parsed --yes, exactly like the
// setup and add-interactive doors. Before R1 both were wrong: Console was
// hard-coded true inside the 2-arg entry point and --yes did not exist.
func TestFanoutSetupDoorPassesTheConsentContext(t *testing.T) {
	prevRunner, prevConsole := setupWizardRunner, setupStdinIsConsole
	t.Cleanup(func() { setupWizardRunner, setupStdinIsConsole = prevRunner, prevConsole })

	cases := []struct {
		name        string
		args        []string
		consoleIn   bool
		wantYes     bool
		wantConsole bool
	}{
		{name: "piped without --yes", args: []string{"--setup"}, wantYes: false, wantConsole: false},
		{name: "piped with --yes", args: []string{"--setup", "--yes"}, wantYes: true, wantConsole: false},
		{name: "console without --yes", args: []string{"--setup"}, consoleIn: true, wantYes: false, wantConsole: true},
		{name: "console with --yes", args: []string{"--setup", "--yes"}, consoleIn: true, wantYes: true, wantConsole: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotOpts providers.WizardOptions
			calls := 0
			setupWizardRunner = func(in io.Reader, out io.Writer, opts providers.WizardOptions) error {
				calls++
				gotOpts = opts
				return nil
			}
			setupStdinIsConsole = func(io.Reader) bool { return tc.consoleIn }

			var out, errw bytes.Buffer
			if code := Run(tc.args, strings.NewReader(""), &out, &errw); code != 0 {
				t.Fatalf("exit code = %d; want 0 (accepted --setup run)\nstdout: %s\nstderr: %s\nout: %s", code, out.String(), errw.String(), out.String())
			}
			if calls != 1 {
				t.Fatalf("wizard seam called %d times; want 1", calls)
			}
			if gotOpts.Yes != tc.wantYes {
				t.Errorf("WizardOptions.Yes = %v; want %v", gotOpts.Yes, tc.wantYes)
			}
			if gotOpts.Console != tc.wantConsole {
				t.Errorf("WizardOptions.Console = %v; want %v (the door must derive it from the reader)", gotOpts.Console, tc.wantConsole)
			}
			// The setup door never falls through to engine resolution: a run
			// that opened the wizard must not also resolve a swarm.
			if strings.Contains(out.String(), "Swarm") {
				t.Errorf("--setup must not start a swarm:\n%s", out.String())
			}
		})
	}
}

// TestFanoutSetupDoorCarriesTheRefusalCode pins the refusal story: a gate
// refusal (ErrWizardWriteRefused) exits 1 without a second error line, because
// the gate already printed the sentence naming --yes; any other wizard failure
// keeps the "Setup failed:" line.
func TestFanoutSetupDoorCarriesTheRefusalCode(t *testing.T) {
	prevRunner := setupWizardRunner
	t.Cleanup(func() { setupWizardRunner = prevRunner })

	setupWizardRunner = func(io.Reader, io.Writer, providers.WizardOptions) error {
		return providers.ErrWizardWriteRefused
	}
	var out, errw bytes.Buffer
	if code := Run([]string{"--setup"}, strings.NewReader(""), &out, &errw); code != 1 {
		t.Fatalf("refusal exit code = %d; want 1", code)
	}
	if strings.Contains(errw.String(), "Setup failed") {
		t.Errorf("a gate refusal must not add an error line (it would be the gate's second sentence): %q", errw.String())
	}

	setupWizardRunner = func(io.Reader, io.Writer, providers.WizardOptions) error {
		return errors.New("input closed (EOF); setup aborted")
	}
	out.Reset()
	errw.Reset()
	if code := Run([]string{"--setup"}, strings.NewReader(""), &out, &errw); code != 1 {
		t.Fatalf("failure exit code = %d; want 1", code)
	}
	if !strings.Contains(errw.String(), "Setup failed: ") {
		t.Errorf("a real wizard failure must name itself on stderr: %q", errw.String())
	}
}

// TestFanoutYesWithoutSetupFailsClosed pins the scope of the new flag: --yes
// consents to the setup wizard's write and nothing else, so a lone --yes is a
// usage error instead of a silently ignored word.
func TestFanoutYesWithoutSetupFailsClosed(t *testing.T) {
	var out, errw bytes.Buffer
	if code := Run([]string{"--yes", "--prompt", "x"}, nil, &out, &errw); code != 2 {
		t.Fatalf("exit code = %d; want 2 (usage error)", code)
	}
	if !strings.Contains(errw.String(), "--yes is only meaningful with --setup") {
		t.Errorf("missing the --yes scope error, got %q", errw.String())
	}
	if strings.Contains(out.String(), "Swarm") || strings.Contains(out.String(), "seat") {
		t.Errorf("a usage error must not start a run:\n%s", out.String())
	}
}

// wizardProbeServer answers the two requests one wizard run makes on the
// unknown-provider/custom-model path: GET /models (FetchRemoteModels -- a body
// with no data array leaves the preset list empty, so the dialog stays the
// pre-existing one) and POST /chat/completions (the live probe).
func wizardProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupDialog is the scripted transcript one wizard run consumes: provider
// name, base URL, key, model ID, the custom-model field defaults, then "N" to
// stop. It is the same dialog internal/providers' --yes pins drive
// (providers_test.go TestInteractiveSetupWizard_UnknownProviderNoDefaultURL)
// and deliberately carries NO consent line: on --yes the gate consents without
// an answer, and on the piped path nothing can answer.
func setupDialog(baseURL string) string {
	return strings.Join([]string{
		"mycustomvendor",
		baseURL + "/v1",
		"mock-dummy-key-789",
		"custom-model-x",
		"", "", "", "", "", "", "", "",
		"N",
	}, "\n") + "\n"
}

// setupDoorHome isolates one run: a private AGY_PRO_HOME owns providers.json,
// no AGY_PRO_PROVIDERS override leaks in from the caller's shell, and the key
// env var the wizard sets in-process is restored when the test ends.
func setupDoorHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(providers.EnvProviders, "")
	t.Setenv("MYCUSTOMVENDOR_API_KEY", "")
	return home
}

// TestFanoutSetupDoorPipedRefusesNamingYes is the R1 regression pin: a piped
// `fanout --setup` that reaches the write gate refuses, names --yes, never
// prints an unanswerable prompt, and writes nothing.
func TestFanoutSetupDoorPipedRefusesNamingYes(t *testing.T) {
	srv := wizardProbeServer(t)
	home := setupDoorHome(t)

	var out, errw bytes.Buffer
	code := Run([]string{"--setup"}, strings.NewReader(setupDialog(srv.URL)), &out, &errw)
	if code != 1 {
		t.Fatalf("exit code = %d; want 1 (refusal)\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "re-run with --yes") {
		t.Errorf("refusal must name --yes (the flag this door now accepts):\n%s", out.String())
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("a reader that cannot answer must not be prompted:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[agy-pro] Approve Setup Write") {
		t.Errorf("the composed diff must still print before the refusal:\n%s", out.String())
	}
	if strings.Contains(errw.String(), "Setup failed") {
		t.Errorf("the refusal is not a failure: %q", errw.String())
	}
	if _, err := os.Stat(filepath.Join(home, "providers.json")); !os.IsNotExist(err) {
		t.Errorf("refusal wrote %s (stat err = %v); nothing may be written", filepath.Join(home, "providers.json"), err)
	}
}

// TestFanoutSetupDoorYesWritesTheRegistry pins the other half: the same scripted
// dialog with --yes is accepted as a flag, consents after the diff, and writes
// providers.json under the temp AGY_PRO_HOME. Together with the pin above this
// is the whole R1 story: the refusal is actionable and the advised command
// works.
func TestFanoutSetupDoorYesWritesTheRegistry(t *testing.T) {
	srv := wizardProbeServer(t)
	home := setupDoorHome(t)

	var out, errw bytes.Buffer
	code := Run([]string{"--setup", "--yes"}, strings.NewReader(setupDialog(srv.URL)), &out, &errw)
	if code != 0 {
		t.Fatalf("exit code = %d; want 0 (--yes consent)\nstdout: %s\nstderr: %s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "[agy-pro] Approve Setup Write") {
		t.Errorf("--yes must still print the exact diff before consenting:\n%s", out.String())
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("--yes must not ask:\n%s", out.String())
	}
	if strings.Contains(errw.String(), "Setup failed") {
		t.Errorf("unexpected failure line: %q", errw.String())
	}

	path := filepath.Join(home, "providers.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("--yes run did not write %s: %v\nstdout: %s", path, err, out.String())
	}
	if !strings.Contains(string(data), "mycustomvendor") {
		t.Errorf("registry at %s does not carry the configured provider:\n%s", path, data)
	}
	if !strings.Contains(string(data), "custom-model-x") {
		t.Errorf("registry at %s does not carry the configured model:\n%s", path, data)
	}
}
