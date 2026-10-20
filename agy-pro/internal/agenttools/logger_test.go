package agenttools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FIX-13: a nil redactor used to leave the audit log unredacted. The log is a
// durable sink for tool output and upstream error text, so the fallback is now
// the generic credential-shape redactor.
// FIX-28: the JSONL line carries Model/Provider/Tool as display fields, but a
// key-shaped value misdirected into a registry model id or provider name must
// not land raw in the durable 0600 file. MaskOperand keeps benign names
// readable and masks credential-shaped ones.
func TestLogMasksKeyShapedModelProviderTool(t *testing.T) {
	const (
		modelPlant    = "sk-planted-model-id-0123456789abcdef"
		providerPlant = "sk-planted-provider-name-0123456789abcdef"
		toolPlant     = "sk-planted-tool-name-0123456789abcdef"
	)
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	logger, err := NewLogger(path, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(LogEntry{
		Model:     modelPlant,
		Provider:  providerPlant,
		Tool:      toolPlant,
		Arguments: `{"path":"a.go"}`,
		Output:    "ok",
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(raw)
	for _, plant := range []string{modelPlant, providerPlant, toolPlant} {
		if strings.Contains(text, plant) {
			t.Errorf("audit log leaked %q raw:\n%s", plant, text)
		}
	}
	// The masked form is the display mask (first-5 + ****), and the benign
	// payload fields keep their readable form.
	for _, masked := range []string{"sk-pl****"} {
		if strings.Count(text, masked) < 3 {
			t.Errorf("expected all three display fields masked as %q, got:\n%s", masked, text)
		}
	}
	if !strings.Contains(text, `"arguments":"{\"path\":\"a.go\"}"`) {
		t.Errorf("benign arguments were rewritten:\n%s", text)
	}
	if !strings.Contains(text, `"tool":"sk-pl****"`) {
		t.Errorf("tool field was not masked:\n%s", text)
	}
}

// Benign registry names must stay readable in the audit line: the mask is a
// display mask, not a blanket rewrite.
func TestLogKeepsBenignModelProviderToolReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewLogger(path, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(LogEntry{
		Model:    "tool-model",
		Provider: "tool-prov",
		Tool:     "upstream_turn",
		Output:   "ok",
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"model":"tool-model"`, `"provider":"tool-prov"`, `"tool":"upstream_turn"`} {
		if !strings.Contains(text, want) {
			t.Errorf("benign field %s missing from the audit line:\n%s", want, text)
		}
	}
	if strings.Contains(text, "****") {
		t.Errorf("benign names must not be masked:\n%s", text)
	}
}

func TestNewLoggerNilRedactorMasksCredentialShapes(t *testing.T) {
	const planted = "AIzaSyDELmE1f2g3h4i5j6k7l8m9n0o1p2q3r4s"
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	logger, err := NewLogger(path, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(LogEntry{
		Tool:      "read_file",
		Arguments: `{"path":"` + planted + `"}`,
		Output:    "leaked " + planted,
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(raw), planted) {
		t.Errorf("audit log leaked the planted credential:\n%s", raw)
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Errorf("audit log carries no redaction marker:\n%s", raw)
	}
}

// An explicit redactor keeps working, and an empty path keeps the logger
// disabled (no file, no error).
func TestNewLoggerExplicitRedactorAndDisabledPath(t *testing.T) {
	disabled, err := NewLogger("", nil)
	if err != nil {
		t.Fatalf("NewLogger with an empty path: %v", err)
	}
	if disabled.Path() != "" {
		t.Errorf("Path = %q; want empty for a disabled logger", disabled.Path())
	}
	if err := disabled.Log(LogEntry{Tool: "read_file"}); err != nil {
		t.Errorf("a disabled logger must accept Log as a no-op: %v", err)
	}

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewLogger(path, func(s string) string { return "SEEDED" })
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(LogEntry{Tool: "read_file", Output: "anything"}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(raw), `"output":"SEEDED"`) {
		t.Errorf("explicit redactor was not applied:\n%s", raw)
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the Model and Provider fields of the durable
// JSONL line are registry slots, so they render through providers.MaskSlot
// (the registry-slot authority) instead of the operand masker, whose
// underscore-head arm is the identity. The Tool field is a tool NAME, not a
// registry slot, and stays on MaskOperand.
// ---------------------------------------------------------------------------

func TestLogMasksUnderscoreHeadRegistrySlots(t *testing.T) {
	const (
		providerPlant = "sk_live_0123456789abcdef"
		providerMask  = "sk_li****"
		modelPlant    = "tok_a1b2c3d4e5f6g7h8i9j0"
		modelMask     = "tok_a****"
	)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger, err := NewLogger(path, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Log(LogEntry{
		Model:    modelPlant,
		Provider: providerPlant,
		Tool:     "upstream_turn",
		Output:   "ok",
	}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(raw)
	for _, plant := range []string{providerPlant, modelPlant} {
		if strings.Contains(text, plant) {
			t.Errorf("audit log leaked %q raw:\n%s", plant, text)
		}
	}
	for _, want := range []string{
		`"provider":"` + providerMask + `"`,
		`"model":"` + modelMask + `"`,
		`"tool":"upstream_turn"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("audit line is missing %s:\n%s", want, text)
		}
	}

	// Benign corpus: the registry fields keep their exact bytes and no mask
	// appears anywhere in the line.
	benignPath := filepath.Join(t.TempDir(), "audit.jsonl")
	benign, err := NewLogger(benignPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	for _, tc := range []struct{ provider, model string }{
		{"nw", "glm-5.3-flash-flex"},
		{"openai", "gpt-5.1"},
		{"gemini", "gemini-3.8-flash-high"},
		{"deepseek", "deepseek-chat"},
		{"charm", "claude-opus-5-5-high"},
		{"acme-corp", "meta-llama/Llama-3-70B"},
	} {
		if err := benign.Log(LogEntry{Provider: tc.provider, Model: tc.model, Tool: "read", Output: "ok"}); err != nil {
			t.Fatalf("Log(%s/%s): %v", tc.provider, tc.model, err)
		}
	}
	raw, err = os.ReadFile(benignPath)
	if err != nil {
		t.Fatalf("read benign log: %v", err)
	}
	text = string(raw)
	for _, want := range []string{
		`"provider":"nw"`, `"model":"glm-5.3-flash-flex"`,
		`"provider":"openai"`, `"model":"gpt-5.1"`,
		`"provider":"gemini"`, `"model":"gemini-3.8-flash-high"`,
		`"provider":"deepseek"`, `"model":"deepseek-chat"`,
		`"provider":"charm"`, `"model":"claude-opus-5-5-high"`,
		`"provider":"acme-corp"`, `"model":"meta-llama/Llama-3-70B"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("benign audit line lost %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "****") {
		t.Errorf("a benign registry value was masked:\n%s", text)
	}
}
