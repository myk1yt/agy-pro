package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"agypro/internal/integrations"
)

// FIX-14 / F-04 of the confirmation seat: the last four transcript sinks of the
// integrations door printed engine text without the terminal strip. The engine's own
// line is not exempt - a repair note carries the settings ledger's sentence, which
// names a field out of the live settings.json, and a failed step prints its own error
// - so all four now go through the same rules the other sinks use (transcriptRow for
// a composed row, stepFailureRow for a failed step). Like the FIX-12 pins, these bite
// on the rendering expression the sinks call rather than on a live config that can
// carry a raw control character today.

// TestTranscriptRowStripsTheComposedArms pins the row rendering behind the three
// gate-less arms: "Nothing to write", "Nothing to roll back" and "Nothing to repair".
func TestTranscriptRowStripsTheComposedArms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "diff row with a CSI escape",
			in:   "hook: command C:\\Users\\me\\agy-tokens\x1b[2J --hook",
			want: "  hook: command C:\\Users\\me\\agy-tokens[2J --hook",
		},
		{
			name: "repair note with a bidi override",
			in:   "note settings: the document names the field forge\u202Ed",
			want: "  note settings: the document names the field forged",
		},
		{
			name: "rollback line with a newline injection",
			in:   "rollback hook: 0 restored\n[SUCCESS] hook deployed: 9 created",
			want: "  rollback hook: 0 restored[SUCCESS] hook deployed: 9 created",
		},
		{
			name: "clean row keeps its indent and its text",
			in:   "hook: nothing to roll back: the manifest records no destination for this feature",
			want: "  hook: nothing to roll back: the manifest records no destination for this feature",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transcriptRow(tc.in)
			if got != tc.want {
				t.Fatalf("transcriptRow(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("transcriptRow(%q) kept an ESC byte: %q", tc.in, got)
			}
		})
	}
}

// TestStepFailureRowStripsALiveErrorText pins the failed-step rendering: the feature
// word and the action are engine vocabulary, the error text is not (the settings
// ledger refuses an undeclared field by naming it, and that name comes out of the live
// settings.json).
func TestStepFailureRowStripsALiveErrorText(t *testing.T) {
	feature := integrations.FeatureHook
	live := fmt.Errorf("read %s: %w", "C:\\tmp\\agy\x1b[2J\\hooks.json", errors.New("access denied"))

	failed := stepFailureRow(feature, "failed", live)
	wantFailed := "[agy-pro] " + string(feature) + " failed: read C:\\tmp\\agy[2J\\hooks.json: access denied"
	if failed != wantFailed {
		t.Fatalf("stepFailureRow(failed) = %q, want %q", failed, wantFailed)
	}
	for _, row := range []string{failed, stepFailureRow(feature, "rollback failed", live)} {
		if strings.ContainsRune(row, 0x1b) {
			t.Fatalf("the failed-step row carries a raw ESC byte: %q", row)
		}
	}
	if prefix := "[agy-pro] " + string(feature) + " rollback failed: "; !strings.HasPrefix(stepFailureRow(feature, "rollback failed", live), prefix) {
		t.Fatalf("the rollback arm lost its own sentence: %q", stepFailureRow(feature, "rollback failed", live))
	}

	// The clean direction: an ordinary error keeps its bytes and the row keeps the
	// shape both verbs have always printed.
	plain := errors.New("copy: permission denied")
	if got, want := stepFailureRow(feature, "failed", plain), "[agy-pro] "+string(feature)+" failed: copy: permission denied"; got != want {
		t.Fatalf("stepFailureRow(plain) = %q, want %q", got, want)
	}
}
