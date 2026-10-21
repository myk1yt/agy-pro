package settings

import (
	"bytes"
	"strings"
	"testing"
)

// TestWriteSnapshotReadOnlyLegend (P1-4): the snapshot explains its [ro] flag
// on the printed surface, before the rows that carry it, and the per-row
// badge uses the shared marker spelling.
func TestWriteSnapshotReadOnlyLegend(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	var b bytes.Buffer
	if err := WriteSnapshot(&b, c, false); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "Flags: ["+ReadOnlyMarker+"] = read-only") {
		t.Errorf("snapshot missing the read-only flag legend:\n%s", out)
	}
	if !strings.Contains(out, " ["+ReadOnlyMarker+"]") {
		t.Errorf("snapshot rows missing the [ro] badge:\n%s", out)
	}
}
