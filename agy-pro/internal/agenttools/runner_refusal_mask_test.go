package agenttools

// E2 pin: the scope refusal seeds its redactor with the unit's own scope
// entries (the ONE shared providers.SeedEligible rule the fanout plan-field
// seeding uses). The logged error embeds r.Scope verbatim, and the tool log's
// registry redactor knows only registered keys and generic credential shapes,
// so a bare key-shaped scope entry would persist raw without the seeding. The
// pin asserts the refusal still names the benign offending path and scope
// while the key-shaped entry renders [REDACTED] in the model-visible result
// and the on-disk log. Mutation-verified: dropping the seeding in
// refuseOutOfScope leaks the raw value and fails this pin.

import (
	"agypro/internal/testfixtures"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// keyShapedScopeEntry is a fake key-shaped dummy (the canonical AWS
// documentation example shape), never a real credential.
const keyShapedScopeEntry = testfixtures.AWSSecretExample

func TestToolRunnerScopeRefusalMasksKeyShapedScopeEntry(t *testing.T) {
	root := scopeRoot(t)
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.Scope = []string{"src/alpha", keyShapedScopeEntry}

	out, err := runner.Execute(7, "model-x", "prov-x", "read_file", `{"path":"src/beta/no.go"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil (the refusal is error-shaped)", err)
	}

	// The model-visible refusal keeps naming the benign offending path.
	if !strings.Contains(out, `Error: path "src/beta/no.go" is outside unit scope`) {
		t.Errorf("refusal lost the offending path: %q", out)
	}
	if strings.Contains(out, keyShapedScopeEntry) {
		t.Errorf("refusal leaks the raw key-shaped scope entry: %q", out)
	}

	// The logged error embeds the scope list: the key-shaped entry must be
	// masked on disk, the benign scope entry stays readable.
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), keyShapedScopeEntry) {
		t.Errorf("tool log leaks the raw key-shaped scope entry:\n%s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", data)
	}
	if !strings.Contains(string(data), "src/alpha") {
		t.Errorf("tool log lost the benign scope entry:\n%s", data)
	}
}
