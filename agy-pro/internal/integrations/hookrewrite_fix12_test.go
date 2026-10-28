package integrations

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// FIX-12 / F-K3-1: the live hooks.json is user-editable, so the token
// hookCommandLauncherPath accepts is attacker-chosen on a hostile machine. Two
// limits keep the engine's claim (and the os.Stat behind it) on paths this engine
// actually deploys: the basename must BE one of the ladder's names, and a UNC token
// is refused before anything stats it.

// TestHookCommandLauncherPathRejectsLookalikes pins the exact-basename rule against
// the ladder's own vocabulary. A prefix test accepted agy-tokens-backup.bat and
// agy-tokensX as "this engine's launcher" and printed an advisory about a file this
// engine never writes.
func TestHookCommandLauncherPathRejectsLookalikes(t *testing.T) {
	t.Run("windows ladder names", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("the Windows ladder arm needs Windows path semantics (filepath.Base)")
		}
		pinRuntimeGOOS(t, "windows")
		accepted := []string{
			`C:\Users\me\.gemini\antigravity-cli\bin\agy-tokens.bat`,
			`C:\Users\me\.gemini\antigravity-cli\bin\agy-tokens.cmd`,
			`C:\Users\me\.gemini\antigravity-cli\bin\agy-tokens.exe`,
		}
		for _, value := range accepted {
			if got := hookCommandLauncherPath(value + " --hook"); got != value {
				t.Fatalf("hookCommandLauncherPath(%q) = %q, want %q (a ladder name must stay accepted)", value, got, value)
			}
		}
		rejected := []string{
			`C:\Users\me\agy-tokens-backup.bat`, // prefix of the launcher name
			`C:\Users\me\agy-tokensX`,           // prefix of the launcher name
			`C:\Users\me\agy-tokens`,            // not a Windows ladder name
			`C:\Users\me\agy-tokens.batx`,       // ladder name plus a suffix
		}
		for _, value := range rejected {
			if got := hookCommandLauncherPath(value); got != "" {
				t.Fatalf("hookCommandLauncherPath(%q) = %q, want no claim (not this engine's launcher)", value, got)
			}
		}
	})

	t.Run("posix ladder names", func(t *testing.T) {
		pinRuntimeGOOS(t, "linux")
		accepted := []string{
			`/home/me/.local/bin/agy-tokens`,
			`/home/me/.gemini/antigravity-cli/bin/agy-tokens`,
		}
		for _, value := range accepted {
			if got := hookCommandLauncherPath(value + " --hook"); got != value {
				t.Fatalf("hookCommandLauncherPath(%q) = %q, want %q (a ladder name must stay accepted)", value, got, value)
			}
		}
		rejected := []string{
			`/home/me/.local/bin/agy-tokens.bat`, // a Windows name: not on the POSIX ladder
			`/home/me/.local/bin/agy-tokens-extra`,
			`/home/me/.local/bin/agy-tokensX`,
		}
		for _, value := range rejected {
			if got := hookCommandLauncherPath(value); got != "" {
				t.Fatalf("hookCommandLauncherPath(%q) = %q, want no claim (not this engine's launcher)", value, got)
			}
		}
	})
}

// TestHookCommandLauncherPathRejectsNetworkPaths is the CWE-918/CWE-73-adjacent arm:
// \\host\share and //host/share are absolute, so isAbsoluteLauncherPath accepts them,
// and the basename of a share path IS the launcher name. Only the explicit UNC refusal
// keeps hookLiveCommandPosture from stat-ing the share, which on Windows would open an
// SMB connection and hand the attacker host the victim's NTLM challenge/response.
//
// The mixed-separator spellings are FIX-14: a token whose first two bytes are both
// separators but not the same one (/\host\share, \/host/share, /\\host) is a UNC root
// to Windows and was classified as local by the FIX-12 prefix test. The predicate is
// asserted inside the loop as well, because on a POSIX host filepath.Base would refuse
// some of these spellings at the basename gate and the pin would pass for the wrong
// reason.
func TestHookCommandLauncherPathRejectsNetworkPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		pinRuntimeGOOS(t, "windows")
	} else {
		pinRuntimeGOOS(t, "linux")
	}
	network := []string{
		`\\attacker.example\share\agy-tokens.bat`,
		`\\192.0.2.10\share\agy-tokens.cmd`,
		`\\?\C:\launchers\agy-tokens.exe`,
		`\\.\C:\launchers\agy-tokens.bat`,
		`//attacker.example/share/agy-tokens`,
		`\\attacker.example\share\agy-tokens`,
		// The four mixed-separator spellings the FIX-14 confirmation seat reproduced.
		`/\attacker.example\share\agy-tokens.bat`,
		`\/attacker.example/share/agy-tokens.bat`,
		`/\\attacker.example\share\agy-tokens.bat`,
		`"\/attacker.example/share/agy-tokens.bat"`,
	}
	for _, value := range network {
		if !isUNCPathToken(commandLauncher(value)) {
			t.Fatalf("isUNCPathToken(%q) = false, want true: two leading separators in any mixture are a network root", commandLauncher(value))
		}
		if got := hookCommandLauncherPath(value + " --hook"); got != "" {
			t.Fatalf("hookCommandLauncherPath(%q) = %q, want no claim (a network path is never this engine's launcher)", value, got)
		}
	}
}

// TestHookLiveCommandPostureMakesNoClaimOnANetworkPath pins the consequence, not just
// the predicate: a live hooks.json naming a UNC launcher produces no Detail and no
// drift claim, so the read-only Status verb never reaches a share.
//
// FIX-14 widened it to the mixed-separator roots (the FIX-14 seat's repro drove the
// engine advisory onto a share through \/host/share). The positive control runs FIRST:
// the same document shape with ONE leading separator and a ladder basename in a
// directory that does not exist must produce the "not on disk" advisory, so the
// negatives below are the refusal speaking and not a deaf harness. A stat on one of the
// hostile spellings would mean an SMB attempt; on the shipped predicate none is reached.
func TestHookLiveCommandPostureMakesNoClaimOnANetworkPath(t *testing.T) {
	goos := "linux"
	if runtime.GOOS == "windows" {
		goos = "windows"
	}
	pinRuntimeGOOS(t, goos)
	// command is the JSON-encoded command VALUE, so the document is valid whatever
	// backslashes the spelling carries.
	writeHookDocument := func(t *testing.T, command string) string {
		t.Helper()
		data := `{"hooks":{"PostInvocation":{"enabled":true,"command":` +
			hookJSONLiteral(t, command+" --hook") + `,"description":"probe"}}}`
		destination := filepath.Join(t.TempDir(), hookSourceFile)
		if err := os.WriteFile(destination, []byte(data), 0o644); err != nil {
			t.Fatalf("write the probe hook: %v", err)
		}
		return destination
	}

	control := filepath.Join(t.TempDir(), "gone", statusLineLauncherNames(goos)[0])
	detail, stale := hookLiveCommandPosture(writeHookDocument(t, control))
	if stale || !strings.Contains(detail, "which is not on disk") {
		t.Fatalf("hookLiveCommandPosture(%q) = (%q, %v), want the \"not on disk\" advisory: without it the negatives below prove nothing", control, detail, stale)
	}

	for _, command := range []string{
		`\\attacker.example\share\agy-tokens.bat`,
		`\/attacker.example/share/agy-tokens.bat`,
		`/\attacker.example\share\agy-tokens.bat`,
		`/\\attacker.example\share\agy-tokens.bat`,
	} {
		detail, stale := hookLiveCommandPosture(writeHookDocument(t, command))
		if detail != "" || stale {
			t.Fatalf("hookLiveCommandPosture(%q) = (%q, %v), want (\"\", false): a claim here stats a network share", command, detail, stale)
		}
	}
}
