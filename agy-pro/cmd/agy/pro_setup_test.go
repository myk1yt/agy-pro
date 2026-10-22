// P12: dispatch and degrade tests for `agy pro setup`. Every branch is
// driven through the package-level seams (setupTUIRunner, setupWizardRunner,
// setupConsoleCap, setupStdinConsole, setupExit), so no test needs an
// interactive console, the live providers registry, or a real process exit.
// AGY_PRO_HOME points at a temp dir wherever state could be touched.
package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
	"agypro/internal/tui"
)

// swapSetupSeams replaces the setup seams for the duration of one test and
// returns a recorder the test can inspect.
func swapSetupSeams(t *testing.T) (rec *setupRec) {
	t.Helper()
	root := t.TempDir()
	// The four-redirect discipline (F-S9B-03): every home-derived variable points
	// inside the sandbox, so no setup door can reach the operator's real home.
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	t.Setenv(paths.EnvAgyProHome, root)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(root, "gemini-config"))
	// The harness models a raw-console host by default: both the stdout and
	// the stdin probe report a console, which is the shape the pre-P4-5 tests
	// were written for. A pin that needs the piped-stdin shape flips
	// stdinConsole to false.
	rec = &setupRec{stdinConsole: true, stdinTTY: true}
	oldTUI, oldWizard, oldCap, oldStdin, oldExit := setupTUIRunner, setupWizardRunner, setupConsoleCap, setupStdinConsole, setupExit
	// P5-1 U4: the wizard doors hand the wizard their consent context, whose
	// console half is stdinIsTTY. The console-gate pins count setupStdinConsole
	// probes, so the two seams must stay separable; this one is stubbed here so
	// the context is deterministic whatever handle the test binary runs on.
	oldTTY := stdinIsTTY
	setupTUIRunner = func(in io.Reader, out, errw io.Writer) int {
		rec.tuiCalled = true
		rec.tuiIn, rec.tuiOut, rec.tuiErrw = in, out, errw
		return rec.tuiCode
	}
	setupWizardRunner = func(in io.Reader, out io.Writer, opts providers.WizardOptions) error {
		rec.wizardCalled = true
		rec.wizardIn, rec.wizardOut = in, out
		rec.wizardOpts = opts
		return rec.wizardErr
	}
	setupConsoleCap = func() tui.Cap { return rec.cap }
	setupStdinConsole = func() bool {
		rec.stdinProbes++
		return rec.stdinConsole
	}
	setupExit = func(code int) { rec.exitCode = code }
	stdinIsTTY = func(in io.Reader) bool { return rec.stdinTTY }
	t.Cleanup(func() {
		setupTUIRunner, setupWizardRunner, setupConsoleCap, setupStdinConsole, setupExit = oldTUI, oldWizard, oldCap, oldStdin, oldExit
		stdinIsTTY = oldTTY
	})
	return rec
}

type setupRec struct {
	cap          tui.Cap
	stdinConsole bool // value the stdin-console probe reports
	stdinTTY     bool // value the wizard consent context's console probe reports
	stdinProbes  int  // how many times the gate queried the stdin probe
	tuiCalled    bool
	tuiIn        io.Reader
	tuiOut       io.Writer
	tuiErrw      io.Writer
	tuiCode      int
	wizardCalled bool
	wizardIn     io.Reader
	wizardOut    io.Writer
	wizardOpts   providers.WizardOptions // P5-1 U4: the consent context the door passed
	wizardErr    error
	exitCode     int // -1 sentinel: never called
}

// TestRunSetupNoArgsOnConsoleRunsTUI: no arguments on a raw-console host
// (SuppressBanner false) open the setup TUI with the process stdio handles,
// and the TUI exit code becomes the process exit code.
func TestRunSetupNoArgsOnConsoleRunsTUI(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.tuiCode = 0

	runSetup(nil)

	if !rec.tuiCalled {
		t.Fatal("no-args on console: setup TUI was not called")
	}
	if rec.wizardCalled {
		t.Fatal("no-args on console: line wizard must not run")
	}
	if rec.tuiIn != os.Stdin || rec.tuiOut != os.Stdout || rec.tuiErrw != os.Stderr {
		t.Error("setup TUI must receive the process stdio handles")
	}
	if rec.exitCode != 0 {
		t.Errorf("exit code = %d; want 0", rec.exitCode)
	}
}

// TestRunSetupTUIExitCodePropagates: a nonzero TUI result is propagated as
// the process exit code (same contract as `agy pro models`).
func TestRunSetupTUIExitCodePropagates(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.tuiCode = 1

	runSetup(nil)

	if !rec.tuiCalled {
		t.Fatal("setup TUI was not called")
	}
	if rec.exitCode != 1 {
		t.Errorf("exit code = %d; want 1 (TUI result propagated)", rec.exitCode)
	}
}

// TestRunSetupWizardFlagForcesWizard: `--wizard` runs the line-mode wizard on
// any host, console or not.
func TestRunSetupWizardFlagForcesWizard(t *testing.T) {
	rec := swapSetupSeams(t)

	runSetup([]string{"--wizard"})

	if !rec.wizardCalled {
		t.Fatal("--wizard: line wizard was not called")
	}
	if rec.tuiCalled {
		t.Fatal("--wizard: setup TUI must not run")
	}
	if rec.wizardIn != os.Stdin || rec.wizardOut != os.Stdout {
		t.Error("line wizard must receive stdin/stdout")
	}
}

// TestRunSetupWizardAliasForcesWizard: the bare word `wizard` is the hidden
// alias for --wizard.
func TestRunSetupWizardAliasForcesWizard(t *testing.T) {
	rec := swapSetupSeams(t)

	runSetup([]string{"wizard"})

	if !rec.wizardCalled {
		t.Fatal("wizard alias: line wizard was not called")
	}
	if rec.tuiCalled {
		t.Fatal("wizard alias: setup TUI must not run")
	}
}

// TestRunSetupNonConsoleDegradesToWizard: no arguments on a non-console host
// (pipe, non-Windows TTY, mintty-class; signaled by Cap.SuppressBanner) run
// the byte-stable line wizard and never reach the TUI.
func TestRunSetupNonConsoleDegradesToWizard(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.cap = tui.Cap{SuppressBanner: true}

	runSetup(nil)

	if !rec.wizardCalled {
		t.Fatal("non-console degrade: line wizard was not called")
	}
	if rec.tuiCalled {
		t.Fatal("non-console degrade: setup TUI must not run")
	}
}

// TestRunSetupUnknownArgFailsClosed: any other argument prints the masked
// operand to stderr, prints the help text, and exits 2.
func TestRunSetupUnknownArgFailsClosed(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.exitCode = -1

	stdout := captureStdout(t, func() {
		recStderr := captureStderr(t, func() {
			runSetup([]string{"--bogus"})
		})
		if !strings.Contains(recStderr, `Unknown setup argument: "--bogus"`) {
			t.Errorf("stderr missing masked operand line:\n%s", recStderr)
		}
	})

	if rec.exitCode != 2 {
		t.Errorf("exit code = %d; want 2", rec.exitCode)
	}
	if !strings.Contains(stdout, "Usage: agy pro <command>") {
		t.Errorf("help text was not printed after the unknown argument:\n%s", stdout)
	}
	if rec.tuiCalled || rec.wizardCalled {
		t.Error("unknown argument must not run the TUI or the wizard")
	}
}

// TestRunSetupUnknownArgMasksCredential: a key-shaped operand is masked
// before it reaches stderr (fail closed, D2 original-case rule).
func TestRunSetupUnknownArgMasksCredential(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.exitCode = -1

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runSetup([]string{testfixtures.AKIAExample})
		})
	})

	if strings.Contains(stderr, testfixtures.AKIAExample) {
		t.Errorf("credential-shaped operand echoed raw:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Unknown setup argument") {
		t.Errorf("stderr missing error line:\n%s", stderr)
	}
	if rec.exitCode != 2 {
		t.Errorf("exit code = %d; want 2", rec.exitCode)
	}
}

// TestRunSetupWizardErrorExitsOne: a wizard failure keeps the pre-TUI
// contract (stderr line, exit 1).
func TestRunSetupWizardErrorExitsOne(t *testing.T) {
	rec := swapSetupSeams(t)
	rec.exitCode = -1
	rec.wizardErr = errors.New("registry locked")

	var stderr string
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runSetup([]string{"--wizard"})
		})
	})

	if rec.exitCode != 1 {
		t.Errorf("exit code = %d; want 1", rec.exitCode)
	}
	if !strings.Contains(stderr, "Setup failed") || !strings.Contains(stderr, "registry locked") {
		t.Errorf("stderr missing wizard failure line:\n%s", stderr)
	}
}

// TestHandleProCommandDispatchesSetupSubArgs: handleProCommand passes the
// setup subArgs through (today they were dropped), so `--wizard` reaches the
// wizard branch.
func TestHandleProCommandDispatchesSetupSubArgs(t *testing.T) {
	rec := swapSetupSeams(t)

	handleProCommand([]string{"setup", "--wizard"})

	if !rec.wizardCalled {
		t.Fatal("handleProCommand(setup --wizard): line wizard was not called")
	}
	if rec.tuiCalled {
		t.Fatal("handleProCommand(setup --wizard): setup TUI must not run")
	}
}

// captureStderr lives in main_test.go (shared test helper).
