package agenttools

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// S-4/F4: the environment allow-list is default-deny, so credential variables
// can never reach a command subprocess.
func TestSanitizedCommandEnvAllowList(t *testing.T) {
	t.Setenv("W6_S3B_SECRET", "sk-must-not-escape-0001")
	t.Setenv("W6_S3B_API_KEY", "sk-must-not-escape-0002")
	t.Setenv("AGY_LLM_GATEWAY_API_KEY", "session-key-must-not-escape")
	t.Setenv("NODE_OPTIONS", "--require ./evil.js")
	t.Setenv("PYTHONSTARTUP", "C:\\evil.py")

	joined := strings.Join(sanitizedCommandEnv(), "\n")
	for _, forbidden := range []string{
		"W6_S3B_SECRET", "sk-must-not-escape-0001",
		"W6_S3B_API_KEY", "sk-must-not-escape-0002",
		"AGY_LLM_GATEWAY_API_KEY", "session-key-must-not-escape",
		"NODE_OPTIONS", "PYTHONSTARTUP",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("credential/unsafe variable %q survived the allow-list:\n%s", forbidden, joined)
		}
	}

	// Runtime essentials must survive (PATH casing differs per platform).
	lower := strings.ToLower(joined)
	if !strings.Contains(lower, "path=") {
		t.Errorf("PATH missing from the sanitized environment:\n%s", joined)
	}
	if runtime.GOOS == "windows" && !strings.Contains(lower, "systemroot=") {
		t.Errorf("SystemRoot missing from the sanitized environment:\n%s", joined)
	}
}

// S-4/F4 (root fix): RunCommand itself scrubs the child environment, so every
// caller - including the tool dispatch - gets the protection.
func TestRunCommandDropsCredentials(t *testing.T) {
	t.Setenv("W6_S3B_CHILD_SECRET", "sk-child-must-not-see-0001")

	command, args := "cmd", []string{"/c", "set"}
	if runtime.GOOS != "windows" {
		command, args = "env", nil
	}
	out, err := RunCommand(t.TempDir(), command, args, ".", 15)
	if err != nil {
		t.Fatalf("RunCommand: %v\n%s", err, out)
	}
	if strings.Contains(out, "W6_S3B_CHILD_SECRET") || strings.Contains(out, "sk-child-must-not-see-0001") {
		t.Errorf("child process saw a credential variable:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "path=") {
		t.Errorf("child process lost PATH:\n%s", out)
	}
}

// S-4/F4: the sanitized executor keeps the path guard.
func TestRunCommandKeepsPathGuard(t *testing.T) {
	if _, err := RunCommand(t.TempDir(), "cmd", []string{"/c", "echo hi"}, "..", 5); err == nil {
		t.Error("cwd outside the workspace root must be rejected")
	}
}

// S-4/F4: the tool dispatch reaches the guarded RunCommand (no separate copy).
func TestRunnerDispatchUsesGuardedRunCommand(t *testing.T) {
	t.Setenv("W6_S3B_DISPATCH_SECRET", "sk-dispatch-must-not-see-0001")
	runner := NewRunner(t.TempDir(), false, true, nil)

	argsJSON := `{"command":"cmd","args":["/c","set"],"timeout_seconds":15}`
	if runtime.GOOS != "windows" {
		argsJSON = `{"command":"env","args":[],"timeout_seconds":15}`
	}
	out, err := runner.Execute(1, "m", "p", "run_command", argsJSON)
	if err != nil {
		t.Fatalf("run_command dispatch: %v", err)
	}
	if strings.Contains(out, "W6_S3B_DISPATCH_SECRET") || strings.Contains(out, "sk-dispatch-must-not-see-0001") {
		t.Errorf("dispatched command saw a credential variable:\n%s", out)
	}
}

// R1-5: the per-tool budget is 30s. The clamp arithmetic is pure, so it is
// tested exhaustively here; the budget constant is pinned so any future
// change is a conscious decision.
func TestClampToolTimeout(t *testing.T) {
	if maxToolTimeoutSeconds != 30 {
		t.Fatalf("maxToolTimeoutSeconds = %d; R1-5 pins the per-tool budget at 30", maxToolTimeoutSeconds)
	}
	cases := []struct {
		in   int
		want int
	}{
		{in: -5, want: -5}, // non-positive passes through (RunCommand default wait)
		{in: 0, want: 0},
		{in: 1, want: 1},
		{in: 29, want: 29},
		{in: 30, want: 30},
		{in: 31, want: 30},
		{in: 120, want: 30}, // the old hard-cap value must clamp down now
		{in: 999, want: 30},
	}
	for _, tc := range cases {
		if got := clampToolTimeout(tc.in); got != tc.want {
			t.Errorf("clampToolTimeout(%d) = %d; want %d", tc.in, got, tc.want)
		}
	}
}

// R1-5 behavioral: the value forwarded through the dispatch reaches
// RunCommand unchanged (the effective timeout appears in the timeout error),
// and the call returns promptly instead of waiting out a long budget.
func TestRunnerRunCommandTimeoutPassThrough(t *testing.T) {
	runner := NewRunner(t.TempDir(), false, true, nil)

	// A command sleeping ~2s against a 1s budget must time out after 1s.
	var argsJSON string
	if runtime.GOOS == "windows" {
		argsJSON = `{"command":"cmd","args":["/c","ping","-n","3","127.0.0.1"],"timeout_seconds":1}`
	} else {
		argsJSON = `{"command":"sleep","args":["2"],"timeout_seconds":1}`
	}

	start := time.Now()
	out, err := runner.Execute(1, "m", "p", "run_command", argsJSON)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("run_command dispatch: %v", err)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Fatalf("expected a 1s effective timeout in the error, got: %s", out)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("dispatch waited %v; the 1s budget was not honored", elapsed)
	}
}
