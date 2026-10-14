package swarm

import (
	"strings"
	"testing"

	"agypro/internal/inventory"
)

func TestFormatCommandMasksKeyShapedArgs(t *testing.T) {
	args := []string{"--task", "review", "--seats", "nw/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	got := FormatCommand(args)
	if strings.Contains(got, "wJalrXUtnFEMI") {
		t.Errorf("FormatCommand leaked a key-shaped arg: %q", got)
	}
	if !strings.Contains(got, "--task review") {
		t.Errorf("FormatCommand must keep benign args readable: %q", got)
	}
}

// Fail-without-fix pin (security gate, the dry-run echo hazard): when the
// prompt arrives through --prompt-file, the echoed fanout command references
// the file instead of inlining the prompt text, so a prompt carrying shell
// metacharacters can never ride a copy-pasteable command line.
func TestBuildFanoutArgsFileReferencesFileNotText(t *testing.T) {
	tmpl := Templates()[0]
	seats := []inventory.Entry{{Kind: "native", ModelID: "gemini-3.8-flash-high", Spec: "native:gemini-3.8-flash-high"}}
	got := BuildFanoutArgsFile(tmpl, seats, "/ws", "/tmp/prompt.txt", 90)
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "--prompt ") {
		t.Errorf("file-path echo still inlines prompt text: %q", joined)
	}
	if !strings.Contains(joined, "--prompt-file /tmp/prompt.txt") {
		t.Errorf("file-path echo missing --prompt-file: %q", joined)
	}
	if !strings.Contains(joined, "--seats native:gemini-3.8-flash-high") {
		t.Errorf("seat spec lost: %q", joined)
	}
}
