// F-S9B-01: the post-failure transcript after a composed plan's engine step
// fails. The intent rows run AFTER the whole step loop, so a failure on the FIRST
// step means nothing landed; the sentence that says "the steps printed above
// already landed" was gated on `i > 0 || len(intent) > 0` and therefore lied on
// exactly the arm a plan with pending intent rows can reach.
package main

import (
	"strings"
	"testing"
)

// TestStepFailureNoticeNamesWhatLanded pins both arms of the transcript, plus the
// intent-row clause that only belongs to the zero-landed case.
func TestStepFailureNoticeNamesWhatLanded(t *testing.T) {
	cases := []struct {
		name       string
		index      int
		intentRows int
		want       []string
		absent     []string
	}{
		{
			name:       "first step with pending intent rows",
			index:      0,
			intentRows: 2,
			want:       []string{"nothing was written", "the remaining steps did not run", "state.json rows were not written"},
			absent:     []string{"already landed"},
		},
		{
			name:       "first step with no intent rows",
			index:      0,
			intentRows: 0,
			want:       []string{"nothing was written", "the remaining steps did not run"},
			absent:     []string{"already landed", "state.json"},
		},
		{
			name:       "later step with pending intent rows",
			index:      1,
			intentRows: 2,
			want:       []string{"the steps printed above already landed", "the steps after this one did not run"},
			absent:     []string{"nothing was written"},
		},
		{
			name:       "later step with no intent rows",
			index:      3,
			intentRows: 0,
			want:       []string{"the steps printed above already landed"},
			absent:     []string{"nothing was written"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := stepFailureNotice(tc.index, tc.intentRows)
			if !strings.HasPrefix(got, "[agy-pro] ") {
				t.Errorf("notice = %q; want the door's [agy-pro] prefix", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice = %q; missing %q", got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Errorf("notice = %q; must not claim %q", got, absent)
				}
			}
		})
	}
}
