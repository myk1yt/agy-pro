package settings

// Round-12 pins (r12 F-1/F-2): the hub's constant fanout rows must print the
// numbers the engine and the CLI actually use - 500 max turns and 1 concurrent
// native seat - not the pre-delta 10/2. A view-only constant cannot see the
// fanout literals (internal/fanout/cli.go keeps defaultFanoutMaxTurns /
// defaultFanoutMaxNative unexported), so the pin has two halves: the shipped
// strings by their exact spelling, and a drift guard that re-reads the live
// `agy pro fanout --help` text these rows are advertised as truth against. A
// fanout-side default change now fails a settings test instead of shipping a
// stale row.

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"agypro/internal/fanout"
	"agypro/internal/mode"
)

// fanoutHelp runs the real fanout flag surface with --help and returns what it
// prints. --help returns at flag parsing, before any state read or report
// write, so the probe has no side effects.
func fanoutHelp(t *testing.T) string {
	t.Helper()
	var out, errw bytes.Buffer
	if code := fanout.Run([]string{"--help"}, strings.NewReader(""), &out, &errw); code != 0 {
		t.Fatalf("fanout --help exit = %d; want 0", code)
	}
	return out.String() + errw.String()
}

// The two help lines that carry the engine rung. The stdlib renders the flag
// default for a non-zero value; max-native has a 0 default and states its
// fallback in the description instead.
var (
	fanoutHelpMaxTurns  = regexp.MustCompile(`-max-turns int\s*\n\s*Maximum turns for agentic tool loop \(default (\d+)\)`)
	fanoutHelpMaxNative = regexp.MustCompile(`-max-native int[\s\S]*?or the default (\d+) \(excess`)
)

func hubRow(t *testing.T, c *Core, id string) Item {
	t.Helper()
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem(id)
	if !ok {
		t.Fatalf("row %q missing from the view", id)
	}
	return it
}

// TestHubFanoutRowsShowEngineDefaults pins the shipped display strings: the
// numbers an operator reads in the hub detail pane (value/default) are the
// engine rung, spelled out.
func TestHubFanoutRowsShowEngineDefaults(t *testing.T) {
	c := NewCore(newFakeDeps().deps())

	turns := hubRow(t, c, "fanout.max_turns")
	if turns.Value != "500" || turns.Default != "500" {
		t.Fatalf("fanout.max_turns value/default = %q/%q; want 500/500 (engine default)", turns.Value, turns.Default)
	}

	reg := hubRow(t, c, "fanout.registry_defaults")
	if want := "n=3 timeout=300 maxTurns=500 tools=read,search"; reg.Default != want {
		t.Fatalf("fanout.registry_defaults default = %q; want %q (engine rung); the retired 10/120 pair must not come back", reg.Default, want)
	}

	// newFakeDeps stores native_seat_concurrency = 2, so the value column keeps
	// rendering the STORED setting while the default column names the rung a run
	// falls back to.
	native := hubRow(t, c, "native_seat_concurrency")
	if native.Value != "2" {
		t.Fatalf("native_seat_concurrency value = %q; want the stored 2", native.Value)
	}
	if native.Default != "1" {
		t.Fatalf("native_seat_concurrency default = %q; want the fanout rung 1", native.Default)
	}
}

// TestHubNativeSeatConcurrencyValueFollowsFanoutRung pins the unset, stored and
// out-of-range cases: an absent key shows the 1 a run resolves, a stored value
// shows itself, and a legacy oversized value shows the clamp the engine applies.
// It also pins that internal/mode's stored-value contract default is still 2 -
// this row is display-only; the shared constant was NOT moved.
func TestHubNativeSeatConcurrencyValueFollowsFanoutRung(t *testing.T) {
	if mode.DefaultNativeSeatConcurrency != 2 {
		t.Fatalf("mode contract default = %d; want the untouched 2 (the hub row is the display-only side)", mode.DefaultNativeSeatConcurrency)
	}
	cases := []struct {
		name  string
		state int
		want  string
	}{
		{"unset", 0, "1"},
		{"stored", 5, "5"},
		{"legacy oversized", 12, "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDeps()
			f.state.NativeSeatConcurrency = tc.state
			it := hubRow(t, NewCore(f.deps()), "native_seat_concurrency")
			if it.Value != tc.want {
				t.Fatalf("value = %q; want %q", it.Value, tc.want)
			}
			if it.Default != "1" {
				t.Fatalf("default = %q; want the fanout rung 1", it.Default)
			}
		})
	}
}

// TestHubFanoutRungMatchesFanoutHelp is the drift guard: the numbers the hub
// prints are compared to the numbers `agy pro fanout --help` states, so a
// fanout-side default change breaks a settings test instead of leaving the row
// telling operators the old value.
func TestHubFanoutRungMatchesFanoutHelp(t *testing.T) {
	help := fanoutHelp(t)

	m := fanoutHelpMaxTurns.FindStringSubmatch(help)
	if m == nil {
		t.Fatalf("fanout --help carries no max-turns default line:\n%s", help)
	}
	if m[1] != hubFanoutMaxTurnsRung {
		t.Fatalf("fanout --help max-turns default = %s, hub row prints %s; move the row in the same change",
			m[1], hubFanoutMaxTurnsRung)
	}

	m = fanoutHelpMaxNative.FindStringSubmatch(help)
	if m == nil {
		t.Fatalf("fanout --help carries no max-native fallback line:\n%s", help)
	}
	if m[1] != hubFanoutMaxNativeRung {
		t.Fatalf("fanout --help max-native fallback = %s, hub row prints %s; move the row in the same change",
			m[1], hubFanoutMaxNativeRung)
	}

	// The composite registry-defaults row carries the same rung plus the shared
	// exported timeout constant, so it cannot go stale either.
	c := NewCore(newFakeDeps().deps())
	reg := hubRow(t, c, "fanout.registry_defaults")
	want := fmt.Sprintf("n=3 timeout=%d maxTurns=%s tools=read,search", fanout.DefaultTimeoutSec, hubFanoutMaxTurnsRung)
	if reg.Default != want {
		t.Fatalf("fanout.registry_defaults default = %q; want %q", reg.Default, want)
	}
}
