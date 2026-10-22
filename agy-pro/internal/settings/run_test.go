package settings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
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

// snapshotRowLine returns the one snapshot line that renders the given label
// (the `  [n] label = value flags` row, not its note line).
func snapshotRowLine(t *testing.T, out, label string) string {
	t.Helper()
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "] "+label+" = ") {
			return ln
		}
	}
	t.Fatalf("snapshot carries no row for %q:\n%s", label, out)
	return ""
}

// coreFromRawRegistry builds a Core over the EXACT bytes of a temp
// providers.json, so a stored spelling ("excludeModels": null) reaches the
// loader unchanged. Both AGY_PRO_HOME and AGY_PRO_PROVIDERS point into
// t.TempDir(), so no live file is read or written.
func coreFromRawRegistry(t *testing.T, raw string) *Core {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("AGY_PRO_HOME", tmp)
	path := filepath.Join(tmp, "providers.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	t.Setenv(providers.EnvProviders, path)
	return NewCore(Deps{LoadReg: providers.Load, SaveReg: saveRegistryFile})
}

// TestWriteSnapshotMarksHubOnlyExclusionRow is S7-C6 on the `settings show`
// surface. The snapshot renders the same view as the hub but has no activation
// arm for the model exclusion row, so the row must carry the shared [ro] marker
// there instead of reading as an editable line. ReadOnly stays false (the hub
// arm keys on it and the hub does open the editor for this row), which is why
// the marker comes from HubOnly.
func TestWriteSnapshotMarksHubOnlyExclusionRow(t *testing.T) {
	c, _ := excludeCore(t, providers.Defaults{})
	var b bytes.Buffer
	if err := WriteSnapshot(&b, c, false); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	out := b.String()

	row := snapshotRowLine(t, out, "Model Exclusion List")
	if !strings.HasSuffix(row, " ["+ReadOnlyMarker+"]") {
		t.Fatalf("snapshot row does not end with the read-only marker: %q", row)
	}
	// Non-vacuity: the concurrency row IS editable from this panel, so the same
	// renderer leaves it unmarked. A marker printed on every row would satisfy
	// the assertion above while telling the operator nothing.
	if editable := snapshotRowLine(t, out, "Native Seat Concurrency"); strings.Contains(editable, "["+ReadOnlyMarker+"]") {
		t.Fatalf("an editable row also carries the read-only marker: %q", editable)
	}
	// The marker is not a lie in the other direction either: the note under the
	// row names the one surface that can write it.
	if !strings.Contains(out, "Read-only outside the hub; edit in the hub: agy pro setup.") {
		t.Fatalf("snapshot note does not name the hub route:\n%s", out)
	}
}

// TestExcludeRowValueNamesAbsentOrNullRule is S7-C5 on the rendered value. Any
// unrelated registry write materialises an unset list as `"excludeModels":
// null`, so the wording may not define the built-in default by absence alone:
// absent AND null both mean the built-in default applies, and only an explicit
// empty list means the user owns an empty list. The three shapes stay distinct.
func TestExcludeRowValueNamesAbsentOrNullRule(t *testing.T) {
	const providerJSON = `"providers":[{"name":"nw","baseURL":"https://nw.example/v1","apiKeyEnv":"NW_API_KEY","models":[{"id":"kimi-k3"}]}]`
	absent := `{` + providerJSON + `}`
	null := `{` + providerJSON + `,"defaults":{"excludeModels":null}}`
	empty := `{` + providerJSON + `,"defaults":{"excludeModels":[]}}`
	populated := `{` + providerJSON + `,"defaults":{"excludeModels":["nw"]}}`

	wantBuiltin := ExcludeModelsBuiltinRule + " (built-in default entry; the excludeModels key is absent or null)"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent key", absent, wantBuiltin},
		{"explicit null", null, wantBuiltin},
		{"explicit empty list", empty, "(empty list: nothing is excluded)"},
		{"populated list", populated, "nw"},
	}
	for _, tc := range cases {
		c := coreFromRawRegistry(t, tc.raw)
		view, err := c.View()
		if err != nil {
			t.Fatalf("%s: View: %v", tc.name, err)
		}
		it, ok := view.FindItem("swarm.exclude_models")
		if !ok {
			t.Fatalf("%s: row swarm.exclude_models missing", tc.name)
		}
		if it.Value != tc.want {
			t.Errorf("%s: Value = %q; want %q", tc.name, it.Value, tc.want)
		}
		if tc.raw == null {
			// The null spelling must not render as a user-owned empty list: it
			// loads back as nil, so the built-in default governs.
			if strings.Contains(it.Value, "empty list") {
				t.Errorf("%s: the null spelling rendered as a user-owned list: %q", tc.name, it.Value)
			}
			if !strings.Contains(it.Value, "absent or null") {
				t.Errorf("%s: the value does not name the null spelling: %q", tc.name, it.Value)
			}
		}
	}
}

// TestHubKeepsExclusionRowEditable is the C6 counter-proof: the read-only
// marker added for the two deprecated surfaces must not take the row away from
// the hub. ReadOnly (the flag the hub's generic read-only arm keys on) stays
// false, HubOnly is the flag only the two legacy renderers consume, and the row
// is the only row in the view carrying it.
func TestHubKeepsExclusionRowEditable(t *testing.T) {
	c, _ := excludeCore(t, providers.Defaults{})
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	it, ok := view.FindItem("swarm.exclude_models")
	if !ok {
		t.Fatal("row swarm.exclude_models missing")
	}
	if it.ReadOnly {
		t.Fatal("ReadOnly is true: the hub's read-only arm would shadow the exclusion editor")
	}
	if !it.HubOnly {
		t.Fatal("HubOnly is false: the deprecated surfaces would render an editable-looking row")
	}
	for _, sec := range view.Sections {
		for _, row := range sec.Items {
			if row.HubOnly && row.ID != "swarm.exclude_models" {
				t.Errorf("row %q carries HubOnly; the flag belongs to the hub-only exclusion row", row.ID)
			}
		}
	}
	// The flag is additive in `show --json`: the hub-only row says so, and no
	// other row grows the key (omitempty), so the printed JSON of the rest of
	// the surface is unchanged.
	blob, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if n := strings.Count(string(blob), `"hubOnly":true`); n != 1 {
		t.Fatalf("marshalled view carries %d hubOnly flags; want exactly 1:\n%s", n, blob)
	}
}
