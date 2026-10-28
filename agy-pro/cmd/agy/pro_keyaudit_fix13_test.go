package main

import (
	"strings"
	"testing"

	"agypro/internal/providers"
)

// TestPrintKeyAuditsNeutralisesTheProbeRow is the O-2 pin (SESSION9 FIX-13): the
// doctor key-audit line's `probe: %s` row is the same transcript class the FIX-12
// sinks cover, so it prints under stripTerminalControl. The row's source is
// registry/provider data (the probe's Host and error text), not a live
// hooks.json/settings.json read, which is why FIX-12 left it out; it is still one
// escape sequence away from clearing the operator's screen.
func TestPrintKeyAuditsNeutralisesTheProbeRow(t *testing.T) {
	detail := "GET https://plant.example/v1/models\x1b[2J\x1b[H\u202eevil failed"

	out := captureStdout(t, func() {
		printKeyAudits([]providers.KeyAudit{{
			Provider: "plant",
			Env:      "AGY_PRO_TEST_ECHO_ENV",
			Probe: providers.KeyProbeResult{
				Status: providers.ProbeIndeterminate,
				Host:   "plant.example",
				Detail: detail,
			},
		}})
	})

	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("the probe row carries a raw ESC byte:\n%q", out)
	}
	if strings.ContainsRune(out, '\u202e') {
		t.Fatalf("the probe row carries a raw bidi override:\n%q", out)
	}
	if !strings.Contains(out, "probe: GET https://plant.example/v1/models[2J[H") {
		t.Fatalf("the probe row lost its readable text:\n%s", out)
	}
	if !strings.Contains(out, "evil failed") {
		t.Fatalf("the probe row lost the text after the escape:\n%s", out)
	}
	// The row still prints: neutralising is not dropping.
	if !strings.Contains(out, "probe: ") {
		t.Fatalf("the neutralised probe row disappeared:\n%s", out)
	}
}
