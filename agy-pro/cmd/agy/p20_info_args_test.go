package main

import (
	"strings"
	"testing"
)

func TestP20VersionAndHelpRejectUnknownOperands(t *testing.T) {
	for _, verb := range []string{"version", "help"} {
		t.Run(verb, func(t *testing.T) {
			code, _, stderr := runDispatchHelperCaptureStderr(t, "p20-"+verb+"-bad-flag")
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr)
			}
			if !strings.Contains(stderr, verb+" takes no arguments") {
				t.Fatalf("usage refusal missing: %q", stderr)
			}
		})
	}
}
