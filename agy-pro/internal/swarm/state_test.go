package swarm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/inventory"
)

// TestFileStoreCorruptWarnsOnce covers defect D16: a corrupt swarm-state.json
// must print exactly ONE warning line on the user-visible WarnOut surface
// (the fanout preset-store precedent), fall back to an empty list, and not
// error; a healthy file must stay silent.
func TestFileStoreCorruptWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "swarm-state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	st := &FileStore{Path: path, WarnOut: &stderr}
	presets, err := st.LoadPresets()
	if err != nil {
		t.Fatalf("corrupt file must not error: %v", err)
	}
	if len(presets) != 0 {
		t.Fatalf("corrupt file must fall back to empty, got %+v", presets)
	}
	got := stderr.String()
	want := "warning: swarm-state.json is corrupt; ignoring it and starting with no saved presets\n"
	if got != want {
		t.Fatalf("warning = %q; want exactly %q", got, want)
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("warning must be exactly one line, got %d", strings.Count(got, "\n"))
	}

	// A healthy file round-trips silently on the same store.
	if err := st.SavePresets([]SavedPreset{{Name: "deep", Numbers: "1,3"}}); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	presets, err = st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 1 || presets[0].Name != "deep" || presets[0].Numbers != "1,3" {
		t.Fatalf("round trip = %+v", presets)
	}
	if stderr.Len() != 0 {
		t.Fatalf("healthy file must stay silent, got %q", stderr.String())
	}
}

// TestNewFileStoreDefaultsWarnOutToStderr pins the wiring that makes the D16
// warning real on the console: the constructor every production site uses
// (settings Core default, swarm builder, CLI) defaults WarnOut to os.Stderr,
// so no per-site wiring is required for the one-line warning to reach a
// user-visible surface.
func TestNewFileStoreDefaultsWarnOutToStderr(t *testing.T) {
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	st, err := NewFileStore()
	if err != nil {
		t.Fatal(err)
	}
	if st.WarnOut == nil {
		t.Fatal("NewFileStore must default WarnOut to os.Stderr (nil would silence the corrupt warning)")
	}
	if f, ok := st.WarnOut.(*os.File); !ok || f != os.Stderr {
		t.Fatalf("WarnOut = %T %v; want os.Stderr", st.WarnOut, st.WarnOut)
	}
}

// TestNilFileStoreLoadsWithoutPanic pins the reachability justification for
// the s != nil guard in LoadPresets (code review C7): a typed-nil *FileStore
// held in a swarm.Store interface calls the nil-safe path() and reaches the
// corrupt branch, where s.WarnOut would panic without the guard.
func TestNilFileStoreLoadsWithoutPanic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGY_PRO_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "swarm-state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var st *FileStore
	var store Store = st
	presets, err := store.LoadPresets()
	if err != nil {
		t.Fatalf("nil receiver must not error on a corrupt file: %v", err)
	}
	if len(presets) != 0 {
		t.Fatalf("nil receiver = %+v; want the empty corrupt fallback", presets)
	}
}

// TestFileStoreMissingFileSilent covers the non-corrupt fallback: an absent
// file is the normal pre-setup state, silent and empty.
func TestFileStoreMissingFileSilent(t *testing.T) {
	st := &FileStore{Path: filepath.Join(t.TempDir(), "absent.json"), WarnOut: &bytes.Buffer{}}
	presets, err := st.LoadPresets()
	if err != nil || len(presets) != 0 {
		t.Fatalf("missing file = %+v, %v; want silent empty", presets, err)
	}
}

// TestSavedPresetNameRejectedBySelectionGrammar documents the P3-7 route
// decision (defect D2, refusal variant): a saved preset NAME is not a
// selection token today. The numbered grammar rejects it with the standard
// pinned error, so the builder and --seats name the grammar instead of
// silently resolving; name selection needs swarm/select.go, builder.go,
// pro_swarm.go, and launcher/picker.go wiring (outside the P3-7 file
// boundary) and belongs to a later phase.
func TestSavedPresetNameRejectedBySelectionGrammar(t *testing.T) {
	inv := &inventory.Inventory{Entries: []inventory.Entry{
		{Number: 1, Kind: "native", ModelID: "native:flash", Spec: "native:flash"},
		{Number: 2, Kind: "provider", ModelID: "nw/kimi", Spec: "nw/kimi"},
	}}
	_, err := inventory.ParseSelection("deep-review", inv)
	if err == nil {
		t.Fatal("a saved preset name must not resolve through the numbered grammar yet")
	}
	want := `invalid selection token "deep-review"`
	if err.Error() != want {
		t.Fatalf("error = %q; want the pinned grammar refusal %q", err.Error(), want)
	}
	// The grammar's real tokens still resolve, proving the refusal is
	// name-specific, not a broken parser.
	if _, err := inventory.ParseSelection("1-2", inv); err != nil {
		t.Fatalf("range token must still resolve: %v", err)
	}
}
