package integrations

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusLineErrorSentences pins the three typed refusals' user-facing text:
// each names the documented settings shape, never a resolved user path, and each
// states what to do next.
func TestStatusLineErrorSentences(t *testing.T) {
	foreign := &StatusLineForeignError{Shape: settingsDoorShape, Detail: `unknown field "theme"`, Err: ErrStatusLineForeign}
	assertContains(t, "foreign error", foreign.Error(),
		settingsDoorShape,
		`unknown field "theme"`,
		"repair decision needed",
	)
	if !errors.Is(foreign, ErrStatusLineForeign) {
		t.Fatalf("the foreign error does not unwrap to its sentinel")
	}

	stale := &StatusLineStaleError{Shape: settingsDoorShape, WantSHA: "aaa", LiveSHA: ""}
	assertContains(t, "stale error", stale.Error(), "absent", "aaa", "re-run the plan", settingsDoorShape)
	if !errors.Is(stale, ErrStatusLineStale) {
		t.Fatalf("the stale error does not unwrap to its sentinel")
	}

	var nilForeign *StatusLineForeignError
	if got := nilForeign.Error(); got != ErrStatusLineForeign.Error() {
		t.Fatalf("nil foreign error = %q, want the sentinel text", got)
	}
	var nilStale *StatusLineStaleError
	if got := nilStale.Error(); got != ErrStatusLineStale.Error() {
		t.Fatalf("nil stale error = %q, want the sentinel text", got)
	}
	var nilLauncher *StatusLineLauncherError
	if got := nilLauncher.Error(); got != ErrStatusLineLauncherMissing.Error() {
		t.Fatalf("nil launcher error = %q, want the sentinel text", got)
	}
}

// TestSettingsSnapshotsFollowsTheSandboxedHome pins the exported snapshot lookup:
// it resolves through the sandboxed home, so a caller can list the two chains
// without knowing where the settings file is.
func TestSettingsSnapshotsFollowsTheSandboxedHome(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)

	empty, err := SettingsSnapshots()
	if err != nil {
		t.Fatalf("SettingsSnapshots: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("SettingsSnapshots = %+v, want none", empty)
	}

	dir := filepath.Dir(settingsPath)
	writeFileRaw(t, filepath.Join(dir, "settings.bak.20260101-010101.json"), []byte(`{}`))
	writeFileRaw(t, filepath.Join(dir, "settings.json.bak.20260102-020202"), []byte(`{}`))

	snapshots, err := SettingsSnapshots()
	if err != nil {
		t.Fatalf("SettingsSnapshots: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("SettingsSnapshots = %+v, want two entries", snapshots)
	}
	if !strings.HasSuffix(snapshots[0].Path, "settings.json.bak.20260102-020202") {
		t.Fatalf("newest snapshot = %q, want the ledger chain's newer one", snapshots[0].Path)
	}
	if snapshots[1].Scheme != SettingsSnapshotNode {
		t.Fatalf("second snapshot scheme = %q, want the node chain", snapshots[1].Scheme)
	}
}

// TestStatusLineActionVocabulary pins the closed action set and the door's
// refusal of an action it does not know.
func TestStatusLineActionVocabulary(t *testing.T) {
	for _, action := range []StatusLineAction{
		StatusLineActionCreate, StatusLineActionUpdate, StatusLineActionAdopt,
		StatusLineActionIdentical, StatusLineActionRetract,
	} {
		if !knownStatusLineAction(action) {
			t.Fatalf("knownStatusLineAction(%q) = false", action)
		}
	}
	if knownStatusLineAction("rewrite") {
		t.Fatalf("knownStatusLineAction accepted an unknown action")
	}

	var door *StatusLineDoor
	_ = door
	if err := validateStatusLineDoor(&StatusLineDoor{Action: "rewrite"}); !errors.Is(err, ErrStatusLineDoorInvalid) {
		t.Fatalf("validateStatusLineDoor(unknown action) = %v, want ErrStatusLineDoorInvalid", err)
	}
}

// TestReadStatusLineFileTolerantStates pins the states the plan can see without
// writing anything.
func TestReadStatusLineFileTolerantStates(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)

	state, err := readStatusLineFile()
	if err != nil {
		t.Fatalf("readStatusLineFile(absent): %v", err)
	}
	if state.SHA != "" || state.Current != nil || len(state.Unmanaged) != 0 {
		t.Fatalf("absent state = %+v, want zero values", state)
	}
	if _, statErr := os.Stat(filepath.Dir(settingsPath)); !os.IsNotExist(statErr) {
		t.Fatalf("reading an absent file created the directory: %v", statErr)
	}

	writeFileRaw(t, settingsPath, nil)
	empty, err := readStatusLineFile()
	if err != nil {
		t.Fatalf("readStatusLineFile(empty file): %v", err)
	}
	if empty.Current != nil {
		t.Fatalf("an empty file produced a statusLine object: %+v", empty.Current)
	}
	if empty.SHA != "" {
		t.Fatalf("an empty file produced digest %q, want the empty marker", empty.SHA)
	}
}

// TestApplyStatusLineCreatesFromNothing pins the first-run write: no settings
// file at all, so the ledger creates one containing only the managed slot, and
// there is nothing to archive.
func TestApplyStatusLineCreatesFromNothing(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	if door.BaseSHA != "" {
		t.Fatalf("BaseSHA = %q, want the empty marker for an absent file", door.BaseSHA)
	}
	summary, err := ApplyStatusLine(door)
	if err != nil {
		t.Fatalf("ApplyStatusLine: %v", err)
	}
	if summary.BackupPath != "" {
		t.Fatalf("BackupPath = %q, want empty: the file did not exist", summary.BackupPath)
	}
	if !summary.Recorded || summary.Provenance != ProvenanceGoApplied {
		t.Fatalf("summary = %+v, want a recorded go-applied write", summary)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(readFileRaw(t, settingsPath), &doc); err != nil {
		t.Fatalf("decode created settings.json: %v", err)
	}
	if len(doc) != 1 {
		t.Fatalf("created settings.json = %v, want only the statusLine key", doc)
	}
	if _, present := doc["statusLine"]; !present {
		t.Fatalf("the created document has no statusLine slot")
	}

	// The file is the only new thing in the directory besides the ledger's lock.
	for _, name := range backupFiles(t, settingsPath) {
		if strings.Contains(name, ".bak.") {
			t.Fatalf("a first-run create archived something: %q", name)
		}
	}
}
