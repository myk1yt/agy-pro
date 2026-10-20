package agenttools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// O7 (masking census 2026-10-21, MEDIUM): the model-level tool refusal composes
// the registry model id and the tool name into one sentence that is returned as
// the tool result AND persisted in the 0600 log's Error field. The logger masks
// its own Model/Provider/Tool columns (logger.go:74-76), but the Error field
// only passes the registry redactor, whose generic patterns do not cover the
// underscore-head class, so both raw operands survived in the durable line.
// Both now mask at the composition site through the same authorities the
// converted sibling logger uses: the model id is a registry slot (MaskSlot) and
// the tool name is an operand (MaskOperand). The decorated row holds the
// authority choice too: MaskOperand is location-exempt, so only the slot
// authority masks a marker-decorated id.
func TestToolRunnerModelToolsRefusalMasksRegistryOperands(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		tool      string
		wantModel string
		wantTool  string
	}{
		{
			name:      "underscore-head model id and key-shaped tool name",
			model:     "sk_live_0123456789abcdef",
			tool:      "Ab3dE5gH7jK9mN1pQ3sT",
			wantModel: "sk_li****",
			wantTool:  "Ab3dE****",
		},
		{
			name:      "location-decorated model id",
			model:     "./sk_live_0123456789abcdef",
			tool:      "Ab3dE5gH7jK9mN1pQ3sT",
			wantModel: "sk_li****",
			wantTool:  "Ab3dE****",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "tool.jsonl")
			logger, err := NewLogger(logPath, nil)
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			runner := NewRunner(dir, false, false, logger)
			runner.AllowedModelTools = []string{"read_file"}

			out, err := runner.Execute(4, tc.model, "prov-x", tc.tool, `{"path":"src/alpha/go.mod"}`)
			if err != nil {
				t.Fatalf("Execute error = %v; the refusal is error-shaped with a nil error", err)
			}
			wantMsg := fmt.Sprintf("tool %q is not in the allowed tools list for model %q", tc.wantTool, tc.wantModel)
			if out != "Error: "+wantMsg {
				t.Fatalf("refusal = %q; want %q", out, "Error: "+wantMsg)
			}

			entry := readSingleToolLogEntry(t, logPath)
			if entry.Error != wantMsg {
				t.Errorf("log Error = %q; want %q", entry.Error, wantMsg)
			}
			if entry.Model != tc.wantModel {
				t.Errorf("log Model = %q; want %q", entry.Model, tc.wantModel)
			}
			if entry.Tool != tc.wantTool {
				t.Errorf("log Tool = %q; want %q", entry.Tool, tc.wantTool)
			}

			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			for _, plant := range []string{tc.model, tc.tool} {
				if strings.Contains(string(raw), plant) {
					t.Errorf("tool log leaks the raw operand %q:\n%s", plant, raw)
				}
				if strings.Contains(out, plant) {
					t.Errorf("model-visible refusal leaks the raw operand %q: %q", plant, out)
				}
			}
		})
	}
}

// O7 benign side: both maskers are the identity on ordinary identifiers, so the
// refusal sentence and its log line stay byte-identical to the pre-fix form.
func TestToolRunnerModelToolsRefusalBenignByteIdentical(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tool.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(dir, false, false, logger)
	runner.AllowedModelTools = []string{"read_file"}

	out, err := runner.Execute(4, "model-x", "prov-x", "write_file", `{"path":"out.txt","content":"x"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; the refusal is error-shaped with a nil error", err)
	}
	const wantMsg = `tool "write_file" is not in the allowed tools list for model "model-x"`
	if out != "Error: "+wantMsg {
		t.Fatalf("refusal = %q; want %q", out, "Error: "+wantMsg)
	}

	entry := readSingleToolLogEntry(t, logPath)
	if entry.Error != wantMsg {
		t.Errorf("log Error = %q; want %q", entry.Error, wantMsg)
	}
	if entry.Model != "model-x" || entry.Provider != "prov-x" || entry.Tool != "write_file" {
		t.Errorf("log columns = (%q, %q, %q); want (model-x, prov-x, write_file)",
			entry.Model, entry.Provider, entry.Tool)
	}
}

// O7, same raw-composition class on the dispatch default branch: an unknown tool
// name is composed raw into an error that reaches the same log Error field
// (logger.go masks only the Tool column). MaskOperand keeps an ordinary token
// readable, matching the sibling --tools refusal (tokens.go:54) and the CLI
// assertions downstream.
func TestToolRunnerUnknownToolRefusalMasksOperand(t *testing.T) {
	cases := []struct {
		name string
		tool string
		want string
	}{
		{name: "key-shaped tool name", tool: "Ab3dE5gH7jK9mN1pQ3sT", want: `Error: unknown tool "Ab3dE****"`},
		{name: "benign tool name", tool: "nowrite", want: `Error: unknown tool "nowrite"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "tool.jsonl")
			logger, err := NewLogger(logPath, nil)
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			runner := NewRunner(dir, false, false, logger)

			out, err := runner.Execute(5, "model-x", "prov-x", tc.tool, `{}`)
			if err != nil {
				t.Fatalf("Execute error = %v; the refusal is error-shaped with a nil error", err)
			}
			if out != tc.want {
				t.Fatalf("refusal = %q; want %q", out, tc.want)
			}
			entry := readSingleToolLogEntry(t, logPath)
			if wantErr := strings.TrimPrefix(tc.want, "Error: "); entry.Error != wantErr {
				t.Errorf("log Error = %q; want %q", entry.Error, wantErr)
			}
		})
	}
}

// readSingleToolLogEntry decodes the one JSONL line a single-dispatch pin wrote.
func readSingleToolLogEntry(t *testing.T, path string) LogEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d; want exactly one:\n%s", len(lines), raw)
	}
	var entry LogEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode log line: %v\n%s", err, lines[0])
	}
	return entry
}
