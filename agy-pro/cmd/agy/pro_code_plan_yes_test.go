package main

// Pins for the standalone plan verb's consent surface.
//
// `agy pro code plan` is the read-only phase, so it only gates when --seats
// names a provider (metered) seat. The gate itself lives in the library
// (internal/codework/phases.go gateConsent, keyed on CodeConfig.Yes); what this
// file pins is the VERB's half of that contract: the flag is registered, it
// reaches the gate, and it changes nothing for a native-only (free) phase.
//
// The inconsistency this closes: the door's own refusal hint says "pass --yes"
// (pro_code.go codeRefused) on every subcommand, but the plan flag set had no
// --yes, so a non-interactive caller was told to pass a flag it could not parse.
// Every vector here stubs codework.RunFanout, so nothing spawns a seat and no
// provider is charged.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planYesMeteredSeat is the synthetic provider seat these pins use to make a
// phase metered. The spelling is the repo's seat-grammar convention
// (internal/fanout and internal/codework fixtures), not a vendor:
//
//   - fanout.ParseSeatSpecs (internal/fanout/seatspec.go:102) classifies every
//     token without a "native:" prefix as SeatKindProvider, so this token makes
//     the phase metered without any registry lookup, and
//   - nothing here resolves it: codework.RunFanout is stubbed in every case, so
//     the seat is never dialled and no provider identity is needed or implied.
//
// Keeping a synthetic seat here is deliberate: an owner-specific gateway/model
// pair in a test fixture reads as a real, resolvable seat to anyone auditing the
// suite, and a pin must not need the owner's private lineup to make sense.
const planYesMeteredSeat = "prov-a/model-a"

// TestCodePlanUsageCarriesTheYesRow pins the help text to the flag surface: the
// plan section names --yes right after --seats, with the same wording the write
// and review sections use. Without this the hint and the surface can drift apart
// again in silence.
func TestCodePlanUsageCarriesTheYesRow(t *testing.T) {
	want := "  --seats A,B                    plan seats (default the native line-up below)\n" +
		"  --yes                          consent to the metered seats without asking\n"
	if !strings.Contains(proCodeUsage, want) {
		t.Errorf("proCodeUsage plan section does not carry the --yes row the refusal hint names; want:\n%s\nusage:\n%s", want, proCodeUsage)
	}
	// The two other metered verbs keep the same row: one wording, three verbs.
	if got := strings.Count(proCodeUsage, "  --yes                          consent to the metered seats without asking\n"); got != 3 {
		t.Errorf("the consent row appears %d time(s) in proCodeUsage; want 3 (plan, write, review)", got)
	}
}

// TestCodePlanConsentSurface drives the four consent shapes of the standalone
// plan verb. A test stdin is a strings.Reader, which is never a TTY, so it is
// exactly the scripted caller the refusal hint addresses.
func TestCodePlanConsentSurface(t *testing.T) {
	cases := []struct {
		name string
		// args are the plan subcommand's own flags (the verb is prepended).
		args []string
		// wantCode is the process exit code: 0 ok, 2 refused consent.
		wantCode int
		// wantFanout is how many fanout vectors were composed and executed.
		wantFanout int
		// wantStderr / wantNoStderr are substring assertions on the error stream.
		wantStderr   []string
		wantNoStderr []string
		// wantVector asserts substrings of the ONE composed fanout vector (only
		// meaningful when wantFanout is 1): the seats the phase actually asked for.
		wantVector []string
		// wantReport requires the run report block on stdout.
		wantReport bool
	}{
		{
			name:       "metered plan with --yes runs on a non-interactive stdin",
			args:       []string{"--ask", "audit this", "--seats", planYesMeteredSeat, "--yes"},
			wantCode:   0,
			wantFanout: 1,
			wantStderr: []string{
				"agy pro code plan: 1 metered seat(s) - this run spends money",
				"consent: --yes",
			},
			wantNoStderr: []string{"re-run with --yes", "cannot answer a y/N prompt"},
			wantReport:   true,
		},
		{
			name:       "metered plan without --yes is still refused",
			args:       []string{"--ask", "audit this", "--seats", planYesMeteredSeat},
			wantCode:   2,
			wantFanout: 0,
			wantStderr: []string{
				"agy pro code plan: 1 metered seat(s) - this run spends money",
				"cannot answer a y/N prompt",
				"re-run with --yes",
				"hint: pass --yes",
			},
			wantReport: false,
		},
		{
			name:       "native-only plan never asks",
			args:       []string{"--ask", "audit this"},
			wantCode:   0,
			wantFanout: 1,
			wantNoStderr: []string{
				"metered seat(s)",
				"consent:",
				"Proceed?",
			},
			wantVector: []string{"--seats " + codeDefaultPlanSeats},
			wantReport: true,
		},
		{
			name:       "native-only plan is unaffected by --yes",
			args:       []string{"--ask", "audit this", "--yes"},
			wantCode:   0,
			wantFanout: 1,
			wantNoStderr: []string{
				"metered seat(s)",
				"consent:",
				"Proceed?",
			},
			wantVector: []string{"--seats " + codeDefaultPlanSeats},
			wantReport: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withCodePhaseStreams(t)
			root, out := codeRoot(t)
			calls := withCodeFanout(t, func([]string, io.Writer) int { return 0 })

			args := append([]string{"plan"}, tc.args...)
			args = append(args, "--root", root, "--out", out)
			code, stdout, stderr := codeInvoke(t, strings.NewReader(""), args...)

			if code != tc.wantCode {
				t.Fatalf("plan %v exit = %d; want %d\nstdout:\n%s\nstderr:\n%s", tc.args, code, tc.wantCode, stdout, stderr)
			}
			if len(*calls) != tc.wantFanout {
				t.Fatalf("plan %v made %d fanout call(s); want %d\nstderr:\n%s", tc.args, len(*calls), tc.wantFanout, stderr)
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q:\n%s", want, stderr)
				}
			}
			for _, unwanted := range tc.wantNoStderr {
				if strings.Contains(stderr, unwanted) {
					t.Errorf("stderr carries %q, which must not appear here:\n%s", unwanted, stderr)
				}
			}
			if len(tc.wantVector) > 0 {
				if len(*calls) != 1 {
					t.Fatalf("wantVector needs exactly one recorded vector; got %d", len(*calls))
				}
				vector := strings.Join((*calls)[0], " ")
				for _, want := range tc.wantVector {
					if !strings.Contains(vector, want) {
						t.Errorf("composed vector missing %q:\n%s", want, vector)
					}
				}
			}
			if gotReport := strings.Contains(stdout, "agy pro code:"); gotReport != tc.wantReport {
				t.Errorf("stdout report block present = %v; want %v:\n%s", gotReport, tc.wantReport, stdout)
			}
		})
	}
}

// TestCodePlanYesReachesTheGate is the wiring pin under the surface pin: the
// registered flag has to land on CodeConfig.Yes, which is the only thing the
// library's gate reads. A dry run prints the metered echo WITHOUT gating (the
// gate sits after the dry-run return), so it cannot prove the wiring; this test
// therefore asserts the passing branch's own consent line, which the gate prints
// only when the config carries --yes.
func TestCodePlanYesReachesTheGate(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	withCodeFanout(t, func([]string, io.Writer) int { return 0 })

	// --dry-run + --yes: the dry-run branch echoes the seats and stops before the
	// gate, so no consent line is printed even though --yes was parsed.
	code, stdout, stderr := codeInvoke(t, strings.NewReader(""),
		"plan", "--ask", "audit this", "--seats", planYesMeteredSeat, "--yes", "--dry-run",
		"--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("dry-run plan exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "agy pro code plan: 1 metered seat(s)") {
		t.Errorf("dry run dropped the metered echo:\n%s", stderr)
	}
	if strings.Contains(stderr, "consent:") {
		t.Errorf("dry run passed through the consent gate (it must return before it):\n%s", stderr)
	}

	// The same flags minus --dry-run: now the gate runs, and cfg.Yes is what turns
	// the non-TTY refusal into a "consent: --yes" pass.
	code, stdout, stderr = codeInvoke(t, strings.NewReader(""),
		"plan", "--ask", "audit this", "--seats", planYesMeteredSeat, "--yes",
		"--root", root, "--out", out)
	if code != 0 {
		t.Fatalf("metered plan with --yes exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "consent: --yes") {
		t.Errorf("the gate did not see CodeConfig.Yes (no consent: --yes line):\n%s", stderr)
	}
}

// TestCodePlanHintedRepairIsTheAcceptedInvocation is the asserted-control check
// for this fix: the refusal hint tells the operator to "pass --yes", so the
// hinted repair - the refused argv with --yes appended - has to be an invocation
// the plan flag set ACCEPTS. Before --yes was registered here, the hint pointed
// at a flag this verb rejected with "flag provided but not defined", so following
// the door's own advice was a dead end.
//
// The interactive y/N branch is untouched by construction: the gate reads
// cfg.Yes first and only then tests the reader for a terminal, and the verb's
// only change is that one config assignment. That branch is pinned in
// internal/codework (phases_test.go), which owns stdlib stdio shape; this door
// cannot simulate a TTY without widening the library seam, so it pins the half it
// owns - a non-TTY reader is refused, never prompted.
func TestCodePlanHintedRepairIsTheAcceptedInvocation(t *testing.T) {
	withCodePhaseStreams(t)
	root, out := codeRoot(t)
	calls := withCodeFanout(t, func([]string, io.Writer) int { return 0 })

	refusedArgv := []string{"plan", "--ask", "audit this", "--seats", planYesMeteredSeat, "--root", root, "--out", out}
	code, stdout, stderr := codeInvoke(t, strings.NewReader("n\n"), refusedArgv...)
	if code != 2 {
		t.Fatalf("metered plan on a non-TTY reader exit = %d; want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "hint: pass --yes") {
		t.Fatalf("the refusal does not name the flag this test follows up on:\n%s", stderr)
	}
	if strings.Contains(stderr, "Proceed? [y/N]") {
		t.Errorf("a non-TTY reader was prompted instead of refused:\n%s", stderr)
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused consent spawned %d fanout call(s); want 0", len(*calls))
	}
	// A refusal runs nothing: no ask file, no phase subdirectory. The library
	// still records its own trail line (phases.go logPhase) - that is the record
	// of the refusal, not a seat run.
	if _, err := os.Stat(filepath.Join(out, "plan", "ask.txt")); err == nil {
		t.Error("a refused consent wrote the plan ask file; nothing may run on a refusal")
	}

	// The hinted repair: append --yes to the exact refused argv.
	repairedArgv := append(append([]string(nil), refusedArgv...), "--yes")
	code, stdout, stderr = codeInvoke(t, strings.NewReader(""), repairedArgv...)
	if strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("--yes is not registered on `code plan`: the refusal hint names a flag this verb rejects\nstderr:\n%s", stderr)
	}
	if code != 0 {
		t.Fatalf("hinted repair exit = %d; want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("hinted repair made %d fanout call(s); want 1", len(*calls))
	}
}
