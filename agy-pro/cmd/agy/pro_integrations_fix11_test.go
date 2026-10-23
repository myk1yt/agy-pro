package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/integrations"
)

// removeStatusLineLauncher deletes the launcher installStatusLineLauncher created,
// so a test can take a machine from "has a launcher" back to "has none" (an
// uninstall, a home move, a manual delete).
func removeStatusLineLauncher(t *testing.T) {
	t.Helper()
	candidates, err := integrations.StatusLineLauncherCandidates()
	if err != nil {
		t.Fatalf("StatusLineLauncherCandidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no launcher candidate on this host")
	}
	if err := os.Remove(candidates[0]); err != nil {
		t.Fatalf("remove the launcher %q: %v", candidates[0], err)
	}
}

// TestIntegrationsStatusRendersTheEngineDetail pins the door half of the final
// review's visibility gap: the engine's per-destination Detail used to be computed
// and never printed, so a user saw "1 drifted" with no reason and no next step. The
// status verb now prints it as a continuation line under the feature row.
func TestIntegrationsStatusRendersTheEngineDetail(t *testing.T) {
	t.Run("drifted bare command names the pin verb", func(t *testing.T) {
		sandboxIntegrations(t)
		// No launcher yet: the hook deploys the portable bare form.
		if code, _, errw := runIntegrations(t, "apply", "hook", "--yes"); code != 0 {
			t.Fatalf("apply hook exit = %d, stderr:\n%s", code, errw)
		}
		// The launcher lands, which makes the deployed bare command stale.
		installStatusLineLauncher(t)

		code, out, errw := runIntegrations(t, "status")
		if code != 0 {
			t.Fatalf("status exit = %d, stderr:\n%s", code, errw)
		}
		if !strings.Contains(out, "1 drifted") {
			t.Fatalf("status does not report the drift:\n%s", out)
		}
		detailLine := ""
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "re-run apply to pin it") {
				detailLine = line
			}
		}
		if detailLine == "" {
			t.Fatalf("the status output carries no drift detail:\n%s", out)
		}
		if !strings.HasPrefix(detailLine, "          ") {
			t.Fatalf("the detail line is not a continuation under its feature row: %q", detailLine)
		}
	})

	t.Run("pinned command whose launcher is gone stays clean and says so", func(t *testing.T) {
		sandboxIntegrations(t)
		installStatusLineLauncher(t)
		if code, _, errw := runIntegrations(t, "apply", "hook", "--yes"); code != 0 {
			t.Fatalf("apply hook exit = %d, stderr:\n%s", code, errw)
		}
		deployed := filepath.Join(os.Getenv("GEMINI_CONFIG_DIR"), "hooks", "hooks.json")
		before, err := os.ReadFile(deployed)
		if err != nil {
			t.Fatalf("read the deployed hook: %v", err)
		}
		removeStatusLineLauncher(t)

		code, out, errw := runIntegrations(t, "status")
		if code != 0 {
			t.Fatalf("status exit = %d, stderr:\n%s", code, errw)
		}
		if !strings.Contains(out, "1 clean") {
			t.Fatalf("status is not clean with the launcher gone:\n%s", out)
		}
		if strings.Contains(out, "drifted") {
			t.Fatalf("status reports drift for a pinned command whose launcher is gone (repair would unpin it):\n%s", out)
		}
		if !strings.Contains(out, "which is not on disk") {
			t.Fatalf("status does not name the unresolvable command:\n%s", out)
		}
		if !strings.Contains(out, "will not unpin it") {
			t.Fatalf("status does not say agy-pro refuses to unpin:\n%s", out)
		}
		// The status verb is read-only: the deployed bytes never move.
		after, err := os.ReadFile(deployed)
		if err != nil {
			t.Fatalf("re-read the deployed hook: %v", err)
		}
		if string(before) != string(after) {
			t.Fatalf("status rewrote the deployed hook:\n%q\n->\n%q", string(before), string(after))
		}
	})
}

// TestIntegrationsRepairDoesNotUnpinAGoneLauncher is the CLI-level pin for the CEO
// ruling: repair must never turn a pinned command back into a PATH-resolved one.
// With the launcher gone the hook reads clean, so repair composes no hook step; the
// deployed bytes are untouched and the command still names the absolute path.
func TestIntegrationsRepairDoesNotUnpinAGoneLauncher(t *testing.T) {
	sandboxIntegrations(t)
	installStatusLineLauncher(t)
	if code, _, errw := runIntegrations(t, "apply", "hook", "--yes"); code != 0 {
		t.Fatalf("apply hook exit = %d, stderr:\n%s", code, errw)
	}
	deployed := filepath.Join(os.Getenv("GEMINI_CONFIG_DIR"), "hooks", "hooks.json")
	before, err := os.ReadFile(deployed)
	if err != nil {
		t.Fatalf("read the deployed hook: %v", err)
	}
	removeStatusLineLauncher(t)

	code, out, errw := runIntegrations(t, "repair")
	if code != 0 {
		t.Fatalf("repair exit = %d, stderr:\n%s", code, errw)
	}
	if strings.Contains(out, "hook") {
		t.Fatalf("repair composed something for the hook while its launcher was gone:\n%s", out)
	}
	after, err := os.ReadFile(deployed)
	if err != nil {
		t.Fatalf("re-read the deployed hook: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("repair unpinned the command:\n%q\n->\n%q", string(before), string(after))
	}
	if !strings.Contains(string(after), "agy-tokens") {
		t.Fatal("the deployed hook lost the launcher name")
	}
}
