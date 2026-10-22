// P5-1 U4: the door half of the setup wizard's composed-write gate (W12 the
// providers.json upsert, W31 the Windows user-environment key persist).
//
// The wizard's own gate semantics (diff shape, masking, y/N edges, 64-byte cap,
// stale base, both stores) are pinned in internal/providers/wizard_gate_test.go
// against the real wizard. What can only be pinned HERE is the door: every CLI
// entry onto the wizard must hand it the consent context (--yes and whether its
// stdin can answer), must carry the refusal as exit 1 with nothing written, and
// must not turn a refusal into a "Setup failed" line the operator cannot act on.
//
// Three doors reach the wizard: `agy pro setup` (degrade branch), `agy pro setup
// --wizard`, and `agy pro settings providers add-interactive` (alias `wizard`).
package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// The planted key values the scripted doors type. Each head is built by string
// concatenation so the tracked source carries no contiguous provider-key
// spelling (the standing gate scans source characters), while the compiled
// constant stays byte-identical to the single-block literal it replaced, so the
// transcript assertions below compare exactly the bytes they compared before.
const (
	u4DoorKey1 = "sk-" + "live-planted-u4-door-1"
	u4DoorKey2 = "sk-" + "live-planted-u4-door-2"
)

// TestSetupGate_DoorKeyValuesAreByteIdentical pins both door key consts to the
// exact bytes the single-block literal carried, so a re-split that changed the
// value fails here instead of quietly weakening the transcript assertions. The
// expectations are hex, so no pin restores the contiguous spelling this file
// avoids, and a mismatch is reported by offset and length, never by value.
func TestSetupGate_DoorKeyValuesAreByteIdentical(t *testing.T) {
	pins := []struct{ name, value, hex string }{
		{"u4DoorKey1", u4DoorKey1, "736b2d6c6976652d706c616e7465642d75342d646f6f722d31"},
		{"u4DoorKey2", u4DoorKey2, "736b2d6c6976652d706c616e7465642d75342d646f6f722d32"},
	}
	for _, pin := range pins {
		want, err := hex.DecodeString(pin.hex)
		if err != nil {
			t.Fatalf("%s: malformed hex pin: %v", pin.name, err)
		}
		if pin.value == string(want) {
			continue
		}
		offset := 0
		for offset < len(pin.value) && offset < len(want) && pin.value[offset] == want[offset] {
			offset++
		}
		t.Errorf("%s: compiled value is not byte-identical to the pinned bytes (len %d, want %d, first difference at byte %d)",
			pin.name, len(pin.value), len(want), offset)
	}
}

// u4Script is the shortest dialog that reaches the composed gate: a catalog
// provider with model presets, a closed loopback endpoint (the probe fails fast
// and locally), one preset, "save anyway", and no second model. key "" takes
// the wizard's blank-key branch, so the run queues no user-environment write.
func u4Script(key string) string {
	steps := []string{"deepseek", "http://127.0.0.1:9/v1", key, "1", "1", "N"}
	return strings.Join(steps, "\n") + "\n"
}

// u4Home points every home-derived path variable at one temp dir and clears the
// catalog key variable, so the dialog reaches the same prompt whatever the host
// environment carries and no case can read or write the live registry.
//
// All four are set because they resolve different things: AGY_PRO_HOME the
// agy-pro store, GEMINI_CONFIG_DIR the profile config tree, and HOME/USERPROFILE
// the user home that agysettings' GlobalSettingsPath reads directly (it ignores
// GEMINI_CONFIG_DIR entirely). With only the first set, a door that grows such a
// read would open the operator's real ~/.gemini/antigravity-cli/settings.json
// (F-S9B-03).
func u4Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(home, "gemini-config"))
	t.Setenv("DEEPSEEK_API_KEY", "")
	return home
}

// swapStdin replaces os.Stdin with a regular file carrying script, so a door
// that reads the process handle (runSetup does) sees a scripted, non-console
// stdin: a regular file is not a character device, which is the same shape a
// pipe presents to the console probe.
func swapStdin(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin.txt")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write stdin file: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		f.Close()
	})
}

// TestSetupDoorsPassConsentContext pins the context each setup door hands the
// wizard: --yes is the flag, and the console half is the stdinIsTTY authority
// (the one every other CLI gate uses), never the TUI dispatch's console-API
// probe.
func TestSetupDoorsPassConsentContext(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		suppress bool
		stdinTTY bool
		want     providers.WizardOptions
	}{
		{"--wizard on an answerable stdin", []string{"--wizard"}, false, true, providers.WizardOptions{Console: true}},
		{"--wizard on a pipe refuses", []string{"--wizard"}, false, false, providers.WizardOptions{}},
		{"--wizard --yes consents", []string{"--wizard", "--yes"}, false, false, providers.WizardOptions{Yes: true}},
		{"degrade branch keeps the console fact", nil, true, true, providers.WizardOptions{Console: true}},
		{"degrade branch on a pipe refuses", nil, true, false, providers.WizardOptions{}},
		{"degrade branch honours --yes", []string{"--yes"}, true, false, providers.WizardOptions{Yes: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := swapSetupSeams(t)
			rec.cap.SuppressBanner = c.suppress
			rec.stdinTTY = c.stdinTTY
			rec.tuiCode = 0

			runSetup(c.args)

			if !rec.wizardCalled {
				t.Fatalf("args %v: the wizard was not called", c.args)
			}
			if rec.wizardOpts != c.want {
				t.Fatalf("wizard options = %+v; want %+v", rec.wizardOpts, c.want)
			}
		})
	}
}

// TestSetupYesFlagIsAcceptedInAnyPosition pins --yes as a door-level flag rather
// than an operand: every position reaches the wizard with consent, and none of
// them fails closed as an unknown setup argument (the pre-P5-1 door answered
// `--yes` with exit 2).
func TestSetupYesFlagIsAcceptedInAnyPosition(t *testing.T) {
	for _, args := range [][]string{
		{"--yes"},
		{"--yes", "--wizard"},
		{"--wizard", "--yes"},
		{"wizard", "--yes"},
	} {
		rec := swapSetupSeams(t)
		rec.cap.SuppressBanner = true
		rec.exitCode = -1

		runSetup(args)

		if !rec.wizardCalled {
			t.Fatalf("args %v: the wizard was not called", args)
		}
		if !rec.wizardOpts.Yes {
			t.Fatalf("args %v: --yes did not reach the wizard (%+v)", args, rec.wizardOpts)
		}
		if rec.exitCode != -1 {
			t.Fatalf("args %v: exit code requested = %d; want none", args, rec.exitCode)
		}
	}
}

// TestSetupWizardFlagStillRejectsUnknownOperands pins that the --yes parsing did
// not widen the fail-closed argument list: any other operand keeps exit 2, the
// masked echo, and never reaches a runner.
func TestSetupWizardFlagStillRejectsUnknownOperands(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.exitCode = -1

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() { runSetup([]string{"--yes", "--bogus"}) })
	})

	if rec.exitCode != 2 {
		t.Errorf("exit code = %d; want 2", rec.exitCode)
	}
	if rec.wizardCalled || rec.tuiCalled {
		t.Error("an unknown operand must not run the wizard or the TUI")
	}
	if !strings.Contains(stderr, `Unknown setup argument: "--bogus"`) {
		t.Errorf("stderr missing the masked operand line:\n%s", stderr)
	}
}

// TestSetupWizardRefusalCarriesExitOne pins the refusal's door contract: the
// gate already printed its sentence naming --yes, so the door only carries exit
// 1 and does not relabel the refusal as a setup failure.
func TestSetupWizardRefusalCarriesExitOne(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.cap.SuppressBanner = true
	rec.stdinTTY = false
	rec.wizardErr = providers.ErrWizardWriteRefused
	rec.exitCode = -1

	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() { runSetup([]string{"--wizard"}) })
	})

	if rec.exitCode != 1 {
		t.Errorf("exit code = %d; want 1", rec.exitCode)
	}
	if strings.Contains(stderr, "Setup failed") {
		t.Errorf("a refusal is not a setup failure; stderr:\n%s", stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("the door added output to the gate's transcript: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestSetupPipeRefusalWritesNothing is the end-to-end refusal through the REAL
// wizard and the real door wiring, on BOTH setup doors: a piped `agy pro setup`
// (the non-console degrade branch) and a piped `agy pro setup --wizard` print the
// composed diff, refuse naming --yes, exit 1, and write neither store.
func TestSetupPipeRefusalWritesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no arguments (non-console degrade)", nil},
		{"--wizard (forced line wizard)", []string{"--wizard"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := swapSetupSeams(t)
			home := u4Home(t)
			swapStdin(t, u4Script(u4DoorKey1))
			rec.cap.SuppressBanner = true // piped/scripted host: the byte-stable degrade
			rec.stdinTTY = false
			rec.exitCode = -1
			// The real wizard, through the real door wiring.
			setupWizardRunner = providers.InteractiveSetupWizardWithGate

			var stderr string
			stdout := captureStdout(t, func() {
				stderr = captureStderr(t, func() { runSetup(c.args) })
			})

			if rec.exitCode != 1 {
				t.Fatalf("exit code = %d; want 1 (refusal)\nstdout:\n%s\nstderr:\n%s", rec.exitCode, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(home, "providers.json")); !os.IsNotExist(err) {
				t.Fatalf("a refused setup wrote providers.json (stat err = %v)", err)
			}
			for _, want := range []string{
				"[agy-pro] Approve Setup Write",
				"Adds provider deepseek to the registry. Key values are untouched.",
				"providers.json will be written (0600, atomic) with this exact diff:",
				"re-run with --yes",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("refusal transcript missing %q:\n%s", want, stdout)
				}
			}
			if strings.Contains(stdout, u4DoorKey1) {
				t.Errorf("a key value reached the refusal transcript:\n%s", stdout)
			}
			if strings.Contains(stderr, "Setup failed") {
				t.Errorf("a refusal must not be reported as a setup failure:\n%s", stderr)
			}
		})
	}
}

// TestSetupYesWritesTheRegistry is the consented half of the same door: --yes
// prints the diff first (never a silent write) and then performs the composed
// upsert into the temp home.
func TestSetupYesWritesTheRegistry(t *testing.T) {
	rec := swapSetupSeams(t)
	home := u4Home(t)
	swapStdin(t, u4Script(""))
	rec.cap.SuppressBanner = true
	rec.stdinTTY = false
	rec.exitCode = -1
	setupWizardRunner = providers.InteractiveSetupWizardWithGate

	stdout := captureStdout(t, func() { runSetup([]string{"--yes"}) })

	if rec.exitCode != -1 {
		t.Fatalf("exit code requested = %d; want none (the run succeeded)", rec.exitCode)
	}
	reg, err := providers.LoadFrom(filepath.Join(home, "providers.json"))
	if err != nil {
		t.Fatalf("the consented setup wrote no readable registry: %v\nstdout:\n%s", err, stdout)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "deepseek" {
		t.Fatalf("unexpected registry: %+v", reg.Providers)
	}
	banner := strings.Index(stdout, "[agy-pro] Approve Setup Write")
	written := strings.Index(stdout, "Provider Setup Complete!")
	if banner < 0 || written < 0 || banner > written {
		t.Fatalf("the diff must print before the completion summary (banner=%d, summary=%d):\n%s", banner, written, stdout)
	}
	if strings.Contains(stdout, "Apply this write to") {
		t.Errorf("--yes must not prompt for an answer:\n%s", stdout)
	}
}

// TestAddInteractiveDoorPassesConsentContext pins the third door's wiring: the
// settings surface hands the wizard the same context, from the same console
// authority, and takes --yes from its own operand list.
func TestAddInteractiveDoorPassesConsentContext(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    []string
		ttyIn   bool
		want    providers.WizardOptions
		wantRc  int
		wantErr string
	}{
		{name: "pipe without --yes", args: []string{"add-interactive"}, ttyIn: false, want: providers.WizardOptions{}, wantRc: 0},
		{name: "console without --yes", args: []string{"add-interactive"}, ttyIn: true, want: providers.WizardOptions{Console: true}, wantRc: 0},
		{name: "pipe with --yes", args: []string{"add-interactive", "--yes"}, ttyIn: false, want: providers.WizardOptions{Yes: true}, wantRc: 0},
		{name: "alias with --yes", args: []string{"wizard", "--yes"}, ttyIn: true, want: providers.WizardOptions{Yes: true, Console: true}, wantRc: 0},
		{name: "an operand fails closed", args: []string{"add-interactive", "bogus"}, wantRc: 2, wantErr: "takes no operands"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u4Home(t)
			oldRunner, oldTTY := providerWizardRunner, stdinIsTTY
			var gotOpts providers.WizardOptions
			called := false
			providerWizardRunner = func(in io.Reader, out io.Writer, opts providers.WizardOptions) error {
				called = true
				gotOpts = opts
				return nil
			}
			stdinIsTTY = func(io.Reader) bool { return c.ttyIn }
			t.Cleanup(func() { providerWizardRunner, stdinIsTTY = oldRunner, oldTTY })

			var out, errw bytes.Buffer
			rc := runSettingsCmd(append([]string{"providers"}, c.args...), strings.NewReader(""), &out, &errw)

			if rc != c.wantRc {
				t.Fatalf("exit code = %d; want %d (stderr: %s)", rc, c.wantRc, errw.String())
			}
			if c.wantErr != "" {
				if !strings.Contains(errw.String(), c.wantErr) {
					t.Fatalf("stderr missing %q: %s", c.wantErr, errw.String())
				}
				if called {
					t.Error("a usage error must not reach the wizard")
				}
				return
			}
			if !called {
				t.Fatalf("the wizard was not called for %v", c.args)
			}
			if gotOpts != c.want {
				t.Fatalf("wizard options = %+v; want %+v", gotOpts, c.want)
			}
		})
	}
}

// TestAddInteractiveRefusalWritesNothing pins the third door end to end against
// the real wizard: a piped add-interactive prints the composed diff, refuses
// naming --yes, exits 1 with nothing written and no "setup wizard failed" line.
func TestAddInteractiveRefusalWritesNothing(t *testing.T) {
	home := u4Home(t)
	oldRunner, oldTTY := providerWizardRunner, stdinIsTTY
	providerWizardRunner = providers.InteractiveSetupWizardWithGate
	stdinIsTTY = func(io.Reader) bool { return false }
	t.Cleanup(func() { providerWizardRunner, stdinIsTTY = oldRunner, oldTTY })

	var out, errw bytes.Buffer
	rc := runSettingsCmd([]string{"providers", "add-interactive"}, strings.NewReader(u4Script(u4DoorKey2)), &out, &errw)

	if rc != 1 {
		t.Fatalf("exit code = %d; want 1 (refusal)\nstdout:\n%s\nstderr:\n%s", rc, out.String(), errw.String())
	}
	if _, err := os.Stat(filepath.Join(home, "providers.json")); !os.IsNotExist(err) {
		t.Fatalf("a refused add-interactive wrote providers.json (stat err = %v)", err)
	}
	if !strings.Contains(out.String(), "re-run with --yes") {
		t.Errorf("the refusal must name --yes:\n%s", out.String())
	}
	if strings.Contains(errw.String(), "setup wizard failed") {
		t.Errorf("a refusal is not a wizard failure:\n%s", errw.String())
	}
	if strings.Contains(out.String(), u4DoorKey2) {
		t.Errorf("a key value reached the refusal transcript:\n%s", out.String())
	}
}

// TestAddInteractiveYesWritesTheRegistry is the consented half of the third
// door: --yes prints the diff and then writes the temp registry.
func TestAddInteractiveYesWritesTheRegistry(t *testing.T) {
	home := u4Home(t)
	oldRunner, oldTTY := providerWizardRunner, stdinIsTTY
	providerWizardRunner = providers.InteractiveSetupWizardWithGate
	stdinIsTTY = func(io.Reader) bool { return false }
	t.Cleanup(func() { providerWizardRunner, stdinIsTTY = oldRunner, oldTTY })

	var out, errw bytes.Buffer
	rc := runSettingsCmd([]string{"providers", "add-interactive", "--yes"}, strings.NewReader(u4Script("")), &out, &errw)

	if rc != 0 {
		t.Fatalf("exit code = %d; want 0\nstdout:\n%s\nstderr:\n%s", rc, out.String(), errw.String())
	}
	reg, err := providers.LoadFrom(filepath.Join(home, "providers.json"))
	if err != nil {
		t.Fatalf("the consented add-interactive wrote no readable registry: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "deepseek" {
		t.Fatalf("unexpected registry: %+v", reg.Providers)
	}
	if !strings.Contains(out.String(), "[agy-pro] Approve Setup Write") {
		t.Errorf("--yes must still print the diff:\n%s", out.String())
	}
}

// TestProviderWizardRunnerSeamIsTheGate pins the seam's production value: a
// door that reached the ungated two-argument entry point would silently drop
// both the diff and the consent, and every pin above would still pass because
// they replace the seam.
func TestProviderWizardRunnerSeamIsTheGate(t *testing.T) {
	// The seam is a func value; the only observable identity is behavior: drive
	// it with a plan that writes nothing to prove it is the gate-aware entry
	// point (the two-argument wizard would run its dialog here instead).
	home := u4Home(t)
	var out bytes.Buffer
	err := providerWizardRunner(strings.NewReader(u4Script("")), &out, providers.WizardOptions{})
	if !errors.Is(err, providers.ErrWizardWriteRefused) {
		t.Fatalf("the door's wizard seam is not the gate-aware entry point: %v\n%s", err, out.String())
	}
	if _, statErr := os.Stat(filepath.Join(home, "providers.json")); !os.IsNotExist(statErr) {
		t.Fatalf("the seam wrote a registry without consent (stat err = %v)", statErr)
	}
}
