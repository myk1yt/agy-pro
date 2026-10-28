package swarm

import (
	"strings"
	"testing"
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
