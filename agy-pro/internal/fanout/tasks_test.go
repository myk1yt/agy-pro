package fanout

import (
	"strings"
	"testing"
)

// TestTaskNamesMatchesGetTaskPresetSlots is the drift guard between the
// picker source of truth (TaskNames / IsKnownTask) and the engine switch
// (GetTaskPreset): every reported name must resolve through GetTaskPreset to
// itself, no name may be missing or duplicated, and IsKnownTask must apply
// the same trim+lowercase normalization the engine applies at resolve time.
// The TUI task picker and the editor's save-time refusal both derive from
// these functions, so this test IS the pin that keeps the picker listing
// exactly the engine's known set.
func TestTaskNamesMatchesGetTaskPresetSlots(t *testing.T) {
	names := TaskNames()
	if len(names) == 0 {
		t.Fatal("TaskNames() is empty")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("TaskNames() has duplicate %q", n)
		}
		seen[n] = true
		if got := GetTaskPreset(n); got.Name != n {
			t.Fatalf("TaskNames() entry %q resolves through GetTaskPreset to %q", n, got.Name)
		}
		if !IsKnownTask(n) {
			t.Fatalf("IsKnownTask(%q) = false, want true", n)
		}
		// The engine normalizes trim + lowercase; the known-name check must
		// accept the same spellings so "Security" is not refused as a typo.
		if !IsKnownTask("  " + strings.ToUpper(n) + "\t") {
			t.Fatalf("IsKnownTask(%q) = false, want true (normalization)", n)
		}
	}
	for _, unknown := range []string{"", "secuirty", "reviews", "plan audit", "customs"} {
		if IsKnownTask(unknown) {
			t.Fatalf("IsKnownTask(%q) = true, want false", unknown)
		}
	}
}

// TestGetTaskPresetUnknownStillFallsBack pins the DELIBERATE engine fallback
// (the D13 decision recorded at GetTaskPreset): unknown names resolve to the
// review preset at run time. The silent-fallback defect is fixed at the
// preset-editor save path, which refuses unknown names before they persist;
// the engine keeps the fallback so non-editor writers (the --task CLI flag,
// hand-edited preset files, older saved rows) keep resolving exactly as
// before this fix.
func TestGetTaskPresetUnknownStillFallsBack(t *testing.T) {
	for _, name := range []string{"secuirty", "nonsense", ""} {
		if got := GetTaskPreset(name).Name; got != "review" {
			t.Fatalf("GetTaskPreset(%q).Name = %q, want the review fallback", name, got)
		}
	}
}

// TestTaskNamesReturnsACopy pins the copy contract: mutating the returned
// slice must not corrupt the canonical set the picker and the known-name
// check share.
func TestTaskNamesReturnsACopy(t *testing.T) {
	names := TaskNames()
	for i := range names {
		names[i] = "corrupted"
	}
	for _, n := range TaskNames() {
		if n == "corrupted" {
			t.Fatal("TaskNames() shares its backing array with the canonical set")
		}
	}
}
