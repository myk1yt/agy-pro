package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A13: docs/swarm-hybrid.md claimed "other platforms never echo the raw line",
// which contradicts providers/secretinput_other.go (no raw-console
// implementation; the wizard falls back to an ordinary line read and cannot
// mask the echo). The doc also omitted that `models --json` drops NativeErr.
// Both claims are pinned here so the docs cannot drift back.
func TestSwarmHybridDocMatchesImplementedBehavior(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "swarm-hybrid.md"))
	if err != nil {
		t.Fatalf("read docs/swarm-hybrid.md: %v", err)
	}
	doc := string(data)

	// The refuted echo claim must be gone; the honest fallback must be stated.
	if strings.Contains(doc, "never echo the raw line") {
		t.Error("swarm-hybrid.md must not claim other platforms never echo the raw line")
	}
	if !strings.Contains(doc, "falls back to an ordinary line read") {
		t.Error("swarm-hybrid.md must state the non-Windows fallback (ordinary line read)")
	}
	if !strings.Contains(doc, "cannot mask the echo") {
		t.Error("swarm-hybrid.md must state that the non-Windows path cannot mask the echo")
	}

	// `models --json` drops the native-fetch error: the degrade must be documented.
	if !strings.Contains(doc, "`models --json` dumps the entry rows only and does NOT carry the native-fetch error") {
		t.Error("swarm-hybrid.md must document that models --json omits NativeErr")
	}
	if !strings.Contains(doc, "agy pro models --all") {
		t.Error("swarm-hybrid.md must point to `agy pro models --all` as the honest-degrade surface")
	}
}
