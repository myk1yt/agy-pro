package integrations

import (
	"strings"
	"testing"

	"agypro/internal/agysettings"
)

// doorForDiff builds a hand-written door for the renderer tests. Lines is pure,
// so nothing here touches the disk.
func doorForDiff(action StatusLineAction, current *agysettings.StatusLineConfig, unmanaged ...string) *StatusLineDoor {
	command := "/home/someone/.local/bin/agy-tokens --hook --raw --write-dashboard"
	return &StatusLineDoor{
		SettingsPath:  "/home/someone/.gemini/antigravity-cli/settings.json",
		Command:       command,
		Desired:       *agysettings.NewStatusLine(command),
		Current:       current,
		Action:        action,
		UnmanagedKeys: unmanaged,
	}
}

// TestStatusLineDoorLinesCreate pins the create rendering: the file, the action,
// every declared slot, and the untouched-keys sentence.
func TestStatusLineDoorLinesCreate(t *testing.T) {
	door := doorForDiff(StatusLineActionCreate, nil, "colorScheme", "trustedWorkspaces")
	want := []string{
		"file       /home/someone/.gemini/antigravity-cli/settings.json",
		"create     statusLine",
		"             type: command",
		"             command: /home/someone/.local/bin/agy-tokens --hook --raw --write-dashboard",
		"             enabled: true",
		"             stack_with_default: true",
		"unchanged  2 unmanaged top-level key(s) left byte-for-byte alone: colorScheme, trustedWorkspaces",
	}
	got := door.Lines()
	if len(got) != len(want) {
		t.Fatalf("Lines() = %#v, want %d lines %#v", got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestStatusLineDoorLinesUpdate pins the update rendering: only the slots that
// change are named, old value first.
func TestStatusLineDoorLinesUpdate(t *testing.T) {
	current := agysettings.NewStatusLine("agy-tokens --hook --raw --write-dashboard")
	current.Enabled = false
	door := doorForDiff(StatusLineActionUpdate, current, "colorScheme")

	lines := door.Lines()
	want := []string{
		"file       /home/someone/.gemini/antigravity-cli/settings.json",
		"update     statusLine",
		"             command: agy-tokens --hook --raw --write-dashboard -> /home/someone/.local/bin/agy-tokens --hook --raw --write-dashboard",
		"             enabled: false -> true",
		"unchanged  1 unmanaged top-level key(s) left byte-for-byte alone: colorScheme",
	}
	if len(lines) != len(want) {
		t.Fatalf("Lines() = %#v, want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if strings.Contains(strings.Join(lines, "\n"), "stack_with_default:") {
		t.Fatalf("an unchanged slot was rendered: %#v", lines)
	}
}

// TestStatusLineDoorLinesAdoptAndIdentical pins the two no-write renderings: the
// object is shown, and the row says why nothing changes.
func TestStatusLineDoorLinesAdoptAndIdentical(t *testing.T) {
	current := agysettings.NewStatusLine("/home/someone/.local/bin/agy-tokens --hook --raw --write-dashboard")

	adopt := doorForDiff(StatusLineActionAdopt, current).Lines()
	if !strings.Contains(adopt[1], "adopt") || !strings.Contains(adopt[1], "bytes untouched") {
		t.Fatalf("adopt row = %q, want it to say the bytes are untouched", adopt[1])
	}
	if adopt[len(adopt)-1] != "unchanged  (no unmanaged top-level keys)" {
		t.Fatalf("unchanged row = %q, want the no-unmanaged-keys sentence", adopt[len(adopt)-1])
	}

	identical := doorForDiff(StatusLineActionIdentical, current, "colorScheme", "telemetry").Lines()
	if !strings.Contains(identical[1], "identical") || !strings.Contains(identical[1], "nothing to write") {
		t.Fatalf("identical row = %q, want it to say there is nothing to write", identical[1])
	}
	if !strings.Contains(identical[len(identical)-1], "2 unmanaged top-level key(s)") {
		t.Fatalf("unchanged row = %q, want the counted sentence", identical[len(identical)-1])
	}
}

// TestStatusLineDoorLinesUpdateWithoutADifference pins the honest rendering of a
// hand-built door whose action claims a change its slots do not describe.
func TestStatusLineDoorLinesUpdateWithoutADifference(t *testing.T) {
	current := agysettings.NewStatusLine("/home/someone/.local/bin/agy-tokens --hook --raw --write-dashboard")
	lines := doorForDiff(StatusLineActionUpdate, current).Lines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "no declared slot differs") {
		t.Fatalf("Lines() = %#v, want the no-difference row", lines)
	}
}

// TestStatusLineDoorLinesAreBounded pins the cap on the unmanaged-key sentence.
func TestStatusLineDoorLinesAreBounded(t *testing.T) {
	many := make([]string, 0, 12)
	for _, key := range []string{
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l",
	} {
		many = append(many, key)
	}
	lines := doorForDiff(StatusLineActionCreate, nil, many...).Lines()
	last := lines[len(lines)-1]
	if !strings.Contains(last, "+4 more") {
		t.Fatalf("unchanged row = %q, want the bounded preview with +4 more", last)
	}
	if strings.Contains(last, ", i,") || strings.Contains(last, ", j,") {
		t.Fatalf("unchanged row = %q, want at most %d keys named", last, statusLineUnmanagedPreview)
	}
}

// TestStatusLineRemovalLines pins the retract rendering, present and absent.
func TestStatusLineRemovalLines(t *testing.T) {
	current := agysettings.NewStatusLine("agy-tokens --hook --raw --write-dashboard")
	lines := StatusLineRemovalLines("/home/someone/.gemini/antigravity-cli/settings.json", current, []string{"colorScheme"})
	want := []string{
		"file       /home/someone/.gemini/antigravity-cli/settings.json",
		"retract    statusLine  (the ledger archives the previous bytes first)",
		"             type: command",
		"             command: agy-tokens --hook --raw --write-dashboard",
		"             enabled: true",
		"             stack_with_default: true",
		"unchanged  1 unmanaged top-level key(s) left byte-for-byte alone: colorScheme",
	}
	if len(lines) != len(want) {
		t.Fatalf("StatusLineRemovalLines() = %#v, want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}

	absent := StatusLineRemovalLines("/home/someone/.gemini/antigravity-cli/settings.json", nil, nil)
	if !strings.Contains(absent[1], "absent: nothing to remove") {
		t.Fatalf("absent retract row = %q", absent[1])
	}
}

// TestStatusLineDoorLinesMaskAKeyShapedCommand pins the display authority: a
// key-shaped value that reached a command slot never renders raw, while the file
// path (a location) stays readable.
//
// The value is assembled from fragments so this source file carries no contiguous
// key-shaped spelling (agy-pro/docs/provider-pattern-gate.md).
func TestStatusLineDoorLinesMaskAKeyShapedCommand(t *testing.T) {
	secret := "sk-" + "live-" + "abcdef123456"
	current := agysettings.NewStatusLine(secret)
	door := doorForDiff(StatusLineActionUpdate, current, "colorScheme")

	joined := strings.Join(door.Lines(), "\n")
	if strings.Contains(joined, secret) {
		t.Fatalf("the diff rendered the key-shaped command raw: %s", joined)
	}
	if !strings.Contains(joined, "/home/someone/.gemini/antigravity-cli/settings.json") {
		t.Fatalf("the diff masked the file path: %s", joined)
	}
}

// TestStatusLineDoorLinesNilAndZero pins the defensive shapes.
func TestStatusLineDoorLinesNilAndZero(t *testing.T) {
	var door *StatusLineDoor
	if lines := door.Lines(); lines != nil {
		t.Fatalf("nil door Lines() = %#v, want nil", lines)
	}
	if lines := StatusLineRemovalLines("/x/settings.json", nil, []string{"a"}); len(lines) != 3 {
		t.Fatalf("StatusLineRemovalLines(absent) = %#v, want three lines", lines)
	}
}
