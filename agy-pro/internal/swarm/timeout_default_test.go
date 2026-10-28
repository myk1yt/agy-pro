package swarm

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
)

// Pinned timeout default (2026-10-14, residual round): the swarm builder path
// (`agy pro swarm`, the surface the /agypro_swarm plugin command drives) used
// to carry its own DefaultTimeoutSec = 120 and emit `--timeout 120` on every
// generated fanout command, defeating the fanout CLI's 300s default. The
// second constant is gone; every default emission now resolves through
// fanout.DefaultTimeoutSec, and these pins hold that: fail-without-fix if a
// local 120 (or any other divergent constant) comes back.

// Every no-override emission site must produce the fanout CLI default (300s),
// and the swarm package must not own a second timeout constant.
func TestSwarmTimeoutDefaultIsFanoutConstant300(t *testing.T) {
	if fanout.DefaultTimeoutSec != 300 {
		t.Fatalf("fanout.DefaultTimeoutSec = %d; want 300 (the shared CLI default)", fanout.DefaultTimeoutSec)
	}
	want := strconv.Itoa(fanout.DefaultTimeoutSec)
	if want != "300" {
		t.Fatalf("shared default rendered %q; want \"300\"", want)
	}

	tmpl := Templates()[0]
	seats := []inventory.Entry{{Kind: "native", ModelID: "m", Spec: "native:m"}}

	// build.go site 1: BuildFanoutArgs with no timeout override.
	got := BuildFanoutArgs(tmpl, seats, ".", "p", 0)
	if !containsPair(got, "--timeout", want) {
		t.Errorf("BuildFanoutArgs default emission = %q; want --timeout %s", got, want)
	}
	// build.go site 2: BuildFanoutArgsFile with no timeout override.
	got = BuildFanoutArgsFile(tmpl, seats, ".", "p.txt", 0)
	if !containsPair(got, "--timeout", want) {
		t.Errorf("BuildFanoutArgsFile default emission = %q; want --timeout %s", got, want)
	}

	// flags.go: the usage text renders the shared constant, not a literal.
	if !strings.Contains(SwarmUsage, fmt.Sprintf("(default %d)", fanout.DefaultTimeoutSec)) {
		t.Errorf("SwarmUsage must document the shared default (default %d):\n%s", fanout.DefaultTimeoutSec, SwarmUsage)
	}
	if strings.Contains(SwarmUsage, "default 120") {
		t.Error("SwarmUsage still documents the stale 120s default")
	}
}

// builder.go site: the interactive builder seeds its RunSpec timeout from the
// shared constant, so the step-3 budget line and the previewed command both
// carry 300s before any user edit.
func TestBuilderBudgetStepRendersDefaultTimeout300(t *testing.T) {
	out := runBuilderToPrompt(t, builderInv(t), "x")
	if !strings.Contains(out, "  3. timeout  300s") {
		t.Errorf("builder budget step must render the 300s default:\n%s", out)
	}
	if strings.Contains(out, "  3. timeout  120s") {
		t.Errorf("builder budget step still renders the stale 120s default:\n%s", out)
	}
	if !strings.Contains(out, "--timeout 300") {
		t.Errorf("previewed fanout command must carry --timeout 300:\n%s", out)
	}
}

// containsPair reports whether args carries the flag/value pair in order.
func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
