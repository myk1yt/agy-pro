package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agypro/internal/agysettings"
)

// settingsDoorSandbox prepares a sandboxed door environment and returns the
// global settings path plus the manifest path inside it.
//
// USERPROFILE and HOME point at one temp home, the agy-pro home at a temp store,
// and the toolkit-root override is cleared. The resolved settings path is proved
// to sit inside the sandbox before any test writes it (HOST-SAFETY: no test in
// this file may touch the real ~/.gemini).
func settingsDoorSandbox(t *testing.T) (string, string) {
	t.Helper()
	home := sandboxHome(t)
	manifestPath := sandboxStore(t)
	clearToolkitEnv(t)
	settingsPath, err := agysettings.GlobalSettingsPath()
	if err != nil {
		t.Fatalf("GlobalSettingsPath: %v", err)
	}
	rel, err := filepath.Rel(home, settingsPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("the settings path %q is outside the sandbox home %q; refusing to run", settingsPath, home)
	}
	return settingsPath, manifestPath
}

// writeStatusLineLauncher creates the highest-priority launcher for this host and
// returns its absolute path.
func writeStatusLineLauncher(t *testing.T) string {
	t.Helper()
	candidates, err := StatusLineLauncherCandidates()
	if err != nil {
		t.Fatalf("StatusLineLauncherCandidates: %v", err)
	}
	writeFileRaw(t, candidates[0], []byte("#!/bin/sh\n# agy-tokens launcher\n"))
	return candidates[0]
}

// statusLineObjectJSON renders a statusLine object with a JSON-escaped command, so
// a Windows launcher path never breaks the fixture.
func statusLineObjectJSON(t *testing.T, command string, extra ...string) string {
	t.Helper()
	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	body := `"type":"command","command":` + string(encoded) + `,"enabled":true,"stack_with_default":true`
	for _, field := range extra {
		body += "," + field
	}
	return "{" + body + "}"
}

// settingsDocWithStatusLine builds a one-line settings document around one
// statusLine object.
func settingsDocWithStatusLine(t *testing.T, command string, otherKeys ...string) []byte {
	t.Helper()
	body := ""
	for _, key := range otherKeys {
		body += key + ","
	}
	return []byte("{" + body + `"statusLine":` + statusLineObjectJSON(t, command) + "}")
}

// doorManifest loads the sandboxed manifest.
func doorManifest(t *testing.T) *Manifest {
	t.Helper()
	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	return manifest
}

// recordSettingsEntry puts one entry for the settings file into the manifest.
func recordSettingsEntry(t *testing.T, settingsPath string, provenance Provenance) ManifestEntry {
	t.Helper()
	sha, err := settingsFileSHA(settingsPath)
	if err != nil {
		t.Fatalf("settingsFileSHA: %v", err)
	}
	entry := ManifestEntry{Path: settingsPath, SHA256: sha, Provenance: provenance}
	manifest := doorManifest(t)
	mustSet(t, manifest, entry)
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	return entry
}

// backupFiles returns the settings backups beside the settings file, both naming
// schemes included.
func backupFiles(t *testing.T, settingsPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(settingsPath))
	if err != nil {
		t.Fatalf("read settings dir: %v", err)
	}
	out := make([]string, 0, 4)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		out = append(out, entry.Name())
	}
	return out
}

// TestStatusLineLauncherLadderPerOS pins the per-OS ladder the door consults: the
// installers' launcher directory, the launcher name priority, and the shape a
// diagnostic names.
func TestStatusLineLauncherLadderPerOS(t *testing.T) {
	home := t.TempDir()

	wantWindows := []string{filepath.Join(home, ".gemini", "antigravity-cli", "bin")}
	if got := launcherDirsFor("windows", home); !reflect.DeepEqual(got, wantWindows) {
		t.Fatalf("launcherDirsFor(windows) = %v, want %v", got, wantWindows)
	}
	wantPosix := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".gemini", "antigravity-cli", "bin"),
	}
	if got := launcherDirsFor("linux", home); !reflect.DeepEqual(got, wantPosix) {
		t.Fatalf("launcherDirsFor(linux) = %v, want %v", got, wantPosix)
	}

	if got := statusLineLauncherNames("windows"); got[0] != "agy-tokens.bat" {
		t.Fatalf("windows launcher priority starts with %q, want agy-tokens.bat", got[0])
	}
	if got := statusLineLauncherNames("linux"); !reflect.DeepEqual(got, []string{"agy-tokens"}) {
		t.Fatalf("linux launcher names = %v, want [agy-tokens]", got)
	}

	if got := launcherShapeFor("windows"); got != launcherShapeWindows {
		t.Fatalf("launcherShapeFor(windows) = %q, want %q", got, launcherShapeWindows)
	}
	if got := launcherShapeFor("darwin"); got != launcherShapePosix {
		t.Fatalf("launcherShapeFor(darwin) = %q, want %q", got, launcherShapePosix)
	}
}

// TestResolveStatusLineLauncherPosixLadder exercises the POSIX half of the ladder
// through the runtimeGOOS seam: the install.sh wrapper wins, the
// antigravity-cli/bin shim is the fallback, and a missing launcher is the typed
// refusal.
func TestResolveStatusLineLauncherPosixLadder(t *testing.T) {
	home := sandboxHome(t)
	previousGOOS := runtimeGOOS
	runtimeGOOS = "linux"
	defer func() { runtimeGOOS = previousGOOS }()

	if _, err := ResolveStatusLineLauncher(); !errors.Is(err, ErrStatusLineLauncherMissing) {
		t.Fatalf("ResolveStatusLineLauncher() error = %v, want ErrStatusLineLauncherMissing", err)
	}

	fallback := filepath.Join(home, ".gemini", "antigravity-cli", "bin", "agy-tokens")
	writeFileRaw(t, fallback, []byte("#!/bin/sh\n"))
	got, err := ResolveStatusLineLauncher()
	if err != nil {
		t.Fatalf("ResolveStatusLineLauncher: %v", err)
	}
	if got != fallback {
		t.Fatalf("ResolveStatusLineLauncher = %q, want the fallback %q", got, fallback)
	}

	primary := filepath.Join(home, ".local", "bin", "agy-tokens")
	writeFileRaw(t, primary, []byte("#!/bin/sh\n"))
	got, err = ResolveStatusLineLauncher()
	if err != nil {
		t.Fatalf("ResolveStatusLineLauncher: %v", err)
	}
	if got != primary {
		t.Fatalf("ResolveStatusLineLauncher = %q, want the primary %q", got, primary)
	}
}

// TestStatusLinePlanRefusesWithoutLauncher pins the wrapper-missing refusal: no
// file is created, the diagnostic names the documented shape and the installer,
// and the toolkit diagnosis distinguishes a healthy tree from an unusable one.
func TestStatusLinePlanRefusesWithoutLauncher(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)

	_, err := StatusLinePlan("")
	if !errors.Is(err, ErrStatusLineLauncherMissing) {
		t.Fatalf("StatusLinePlan() error = %v, want ErrStatusLineLauncherMissing", err)
	}
	var launcherErr *StatusLineLauncherError
	if !errors.As(err, &launcherErr) {
		t.Fatalf("StatusLinePlan() error = %T, want *StatusLineLauncherError", err)
	}
	assertContains(t, "launcher refusal", err.Error(),
		launcherShapeFor(runtimeGOOS),
		ToolkitReinstallHint,
	)
	if launcherErr.ToolkitIssue == "" {
		t.Fatalf("the toolkit diagnosis is empty; want the unresolved-marker sentence")
	}

	if _, statErr := os.Stat(settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("settings.json was created by a refused plan: %v", statErr)
	}
	if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
		t.Fatalf("the manifest was created by a refused plan: %v", statErr)
	}

	// A healthy toolkit tree changes only the diagnosis, never the refusal.
	root := fakeToolkitRoot(t)
	_, err = StatusLinePlan(root)
	if !errors.Is(err, ErrStatusLineLauncherMissing) {
		t.Fatalf("StatusLinePlan(healthy root) error = %v, want the launcher refusal", err)
	}
	if !errors.As(err, &launcherErr) || launcherErr.ToolkitIssue != "" || launcherErr.ToolkitRoot == "" {
		t.Fatalf("toolkit diagnosis = %+v, want the resolved root and no issue", launcherErr)
	}

	// An unusable toolkit root is reported as such.
	writeFileRaw(t, filepath.Join(root, "bin", "agy-tokens.js"), nil)
	if err := os.Remove(filepath.Join(root, "bin", "agy-tokens.js")); err != nil {
		t.Fatalf("remove toolkit entry: %v", err)
	}
	_, err = StatusLinePlan(root)
	if !errors.As(err, &launcherErr) || !strings.Contains(launcherErr.ToolkitIssue, ToolkitEntryScript) {
		t.Fatalf("toolkit issue = %q, want it to name %q", launcherErr.ToolkitIssue, ToolkitEntryScript)
	}
}

// TestStatusLinePlanClassifiesTheSlot pins the four plan actions and the absolute
// wrapper command the door builds.
func TestStatusLinePlanClassifiesTheSlot(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	launcher := writeStatusLineLauncher(t)
	wantCommand := commandForLauncher(launcher)

	t.Run("absent slot is a create", func(t *testing.T) {
		door, err := StatusLinePlan("")
		if err != nil {
			t.Fatalf("StatusLinePlan: %v", err)
		}
		if door.Action != StatusLineActionCreate {
			t.Fatalf("Action = %q, want create", door.Action)
		}
		if door.Command != wantCommand {
			t.Fatalf("Command = %q, want %q", door.Command, wantCommand)
		}
		if !isAbsoluteLauncherPath(commandLauncher(door.Command)) {
			t.Fatalf("Command %q does not start with an absolute launcher", door.Command)
		}
		if door.BaseSHA != "" {
			t.Fatalf("BaseSHA = %q, want empty for an absent file", door.BaseSHA)
		}
		if door.Current != nil {
			t.Fatalf("Current = %+v, want nil", door.Current)
		}
	})

	nodeShaped := settingsDocWithStatusLine(t, "agy-tokens --hook --raw --write-dashboard", `"colorScheme":"dark"`)
	writeFileRaw(t, settingsPath, nodeShaped)

	t.Run("a node-era command is an update", func(t *testing.T) {
		door, err := StatusLinePlan("")
		if err != nil {
			t.Fatalf("StatusLinePlan: %v", err)
		}
		if door.Action != StatusLineActionUpdate {
			t.Fatalf("Action = %q, want update", door.Action)
		}
		if door.Current == nil || door.Current.Command != "agy-tokens --hook --raw --write-dashboard" {
			t.Fatalf("Current = %+v, want the node-era command", door.Current)
		}
		if !reflect.DeepEqual(door.UnmanagedKeys, []string{"colorScheme"}) {
			t.Fatalf("UnmanagedKeys = %v, want [colorScheme]", door.UnmanagedKeys)
		}
		if door.Recorded {
			t.Fatalf("Recorded = true, want false: nothing has recorded this file yet")
		}
	})

	t.Run("the door's own object is an adopt until it is recorded", func(t *testing.T) {
		writeFileRaw(t, settingsPath, settingsDocWithStatusLine(t, wantCommand))
		door, err := StatusLinePlan("")
		if err != nil {
			t.Fatalf("StatusLinePlan: %v", err)
		}
		if door.Action != StatusLineActionAdopt {
			t.Fatalf("Action = %q, want adopt", door.Action)
		}

		recordSettingsEntry(t, settingsPath, ProvenanceAdopted)
		door, err = StatusLinePlan("")
		if err != nil {
			t.Fatalf("StatusLinePlan: %v", err)
		}
		if door.Action != StatusLineActionIdentical {
			t.Fatalf("Action = %q, want identical", door.Action)
		}
	})
}

// TestApplyStatusLineCreatesAndPassesUnmanagedKeysThrough is the passthrough
// proof: every key the ledger does not manage survives the write byte for byte,
// the previous bytes are archived, and the manifest records the write.
func TestApplyStatusLineCreatesAndPassesUnmanagedKeysThrough(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)

	original := []byte(`{"colorScheme":"dark","telemetry":false,"trustedWorkspaces":["/test/project"]}`)
	writeFileRaw(t, settingsPath, original)

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	if door.Action != StatusLineActionCreate {
		t.Fatalf("Action = %q, want create", door.Action)
	}
	summary, err := ApplyStatusLine(door)
	if err != nil {
		t.Fatalf("ApplyStatusLine: %v", err)
	}
	if summary.Provenance != ProvenanceGoApplied || !summary.Recorded || !summary.ManifestUpdated {
		t.Fatalf("summary = %+v, want a recorded go-applied write", summary)
	}

	updated := readFileRaw(t, settingsPath)
	before := decodeRawJSON(t, original)
	after := decodeRawJSON(t, updated)
	for _, key := range []string{"colorScheme", "telemetry", "trustedWorkspaces"} {
		if !bytes.Equal(before[key], after[key]) {
			t.Fatalf("unmanaged key %q changed: %s -> %s", key, before[key], after[key])
		}
	}
	slot := decodeAny(t, after["statusLine"]).(map[string]any)
	if slot["command"] != door.Command {
		t.Fatalf("written command = %v, want %q", slot["command"], door.Command)
	}
	if slot["type"] != "command" || slot["enabled"] != true || slot["stack_with_default"] != true {
		t.Fatalf("written object = %v, want the node shape", slot)
	}

	backup := summary.BackupPath
	if backup == "" {
		t.Fatalf("BackupPath is empty; the previous bytes must be archived")
	}
	if got := readFileRaw(t, backup); !bytes.Equal(got, original) {
		t.Fatalf("backup bytes = %s, want the original bytes %s", got, original)
	}
	if !strings.Contains(filepath.Base(backup), "settings.json.bak.") {
		t.Fatalf("backup name = %q, want the ledger's settings.json.bak.<ts> scheme", filepath.Base(backup))
	}

	manifest, err := LoadManifestFile(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifestFile: %v", err)
	}
	entry, ok := manifest.Entry(settingsPath)
	if !ok {
		t.Fatalf("the manifest does not claim %q", settingsPath)
	}
	if entry.Provenance != ProvenanceGoApplied || entry.BackupRef != backup {
		t.Fatalf("entry = %+v, want go-applied with backup_ref %q", entry, backup)
	}
	if entry.SHA256 != SHA256Hex(updated) {
		t.Fatalf("entry sha = %q, want the digest of the bytes on disk", entry.SHA256)
	}
}

// TestApplyStatusLineUpdatesANodeEraSlot pins the migration write: the bare PATH
// command is replaced with the absolute wrapper, and the node-era object is
// archived first.
func TestApplyStatusLineUpdatesANodeEraSlot(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)

	original := settingsDocWithStatusLine(t, "agy-tokens --hook --raw --write-dashboard", `"trustedWorkspaces":[]`)
	writeFileRaw(t, settingsPath, original)

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	if door.Action != StatusLineActionUpdate {
		t.Fatalf("Action = %q, want update", door.Action)
	}
	summary, err := ApplyStatusLine(door)
	if err != nil {
		t.Fatalf("ApplyStatusLine: %v", err)
	}
	if summary.BackupPath == "" {
		t.Fatalf("BackupPath is empty; the node-era bytes must be archived")
	}
	if got := readFileRaw(t, summary.BackupPath); !bytes.Equal(got, original) {
		t.Fatalf("backup bytes = %s, want the node-era bytes %s", got, original)
	}
	slot := decodeAny(t, decodeRawJSON(t, readFileRaw(t, settingsPath))["statusLine"]).(map[string]any)
	if slot["command"] != door.Command {
		t.Fatalf("command = %v, want %q", slot["command"], door.Command)
	}
}

// TestApplyStatusLineIsIdenticalOnASecondRun pins idempotence down to the
// manifest's bytes: a second apply after the first one writes nothing.
func TestApplyStatusLineIsIdenticalOnASecondRun(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)
	writeFileRaw(t, settingsPath, []byte(`{"colorScheme":"dark"}`))

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	if _, err := ApplyStatusLine(door); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	firstBytes := readFileRaw(t, settingsPath)
	manifestBefore := readFileRaw(t, manifestPath)

	door, err = StatusLinePlan("")
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if door.Action != StatusLineActionIdentical {
		t.Fatalf("second plan Action = %q, want identical", door.Action)
	}
	summary, err := ApplyStatusLine(door)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if summary.ManifestUpdated || summary.BackupPath != "" {
		t.Fatalf("second apply = %+v, want no write and no bookkeeping change", summary)
	}
	if got := readFileRaw(t, settingsPath); !bytes.Equal(got, firstBytes) {
		t.Fatalf("settings.json changed on an identical apply")
	}
	if got := readFileRaw(t, manifestPath); !bytes.Equal(got, manifestBefore) {
		t.Fatalf("the manifest changed on an identical apply")
	}
}

// TestApplyStatusLineRefusesStaleBase pins the stale-base rule: a file that moved
// after the plan stops the run with zero writes.
func TestApplyStatusLineRefusesStaleBase(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)
	writeFileRaw(t, settingsPath, []byte(`{"colorScheme":"dark"}`))

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	// Another writer (agy itself, or a person) lands while the consent gate is
	// open.
	drifted := []byte(`{"colorScheme":"light","trustedWorkspaces":["/x"]}`)
	writeFileRaw(t, settingsPath, drifted)

	_, err = ApplyStatusLine(door)
	if !errors.Is(err, ErrStatusLineStale) {
		t.Fatalf("ApplyStatusLine error = %v, want ErrStatusLineStale", err)
	}
	var staleErr *StatusLineStaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("ApplyStatusLine error = %T, want *StatusLineStaleError", err)
	}
	if staleErr.WantSHA == "" || staleErr.LiveSHA == "" || staleErr.WantSHA == staleErr.LiveSHA {
		t.Fatalf("stale error = %+v, want the two distinct digests", staleErr)
	}
	if got := readFileRaw(t, settingsPath); !bytes.Equal(got, drifted) {
		t.Fatalf("settings.json = %s, want the drifted bytes untouched", got)
	}
	if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
		t.Fatalf("the manifest was written by a refused apply: %v", statErr)
	}
	for _, name := range backupFiles(t, settingsPath) {
		if strings.Contains(name, ".bak.") {
			t.Fatalf("a refused apply created a backup %q", name)
		}
	}
}

// TestAdoptStatusLineRecordsWithoutWritingBytes pins record-only adopt: the
// file's bytes do not move, no archive is made, and the provenance follows the
// command form on disk.
func TestAdoptStatusLineRecordsWithoutWritingBytes(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	launcher := writeStatusLineLauncher(t)

	cases := []struct {
		name       string
		command    string
		provenance Provenance
	}{
		{name: "node-era bare command", command: "agy-tokens --hook --raw --write-dashboard", provenance: ProvenanceNodeEra},
		{name: "wrapper absolute command", command: commandForLauncher(launcher), provenance: ProvenanceAdopted},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			original := settingsDocWithStatusLine(t, testCase.command, `"colorScheme":"dark"`)
			writeFileRaw(t, settingsPath, original)

			summary, err := AdoptStatusLine()
			if err != nil {
				t.Fatalf("AdoptStatusLine: %v", err)
			}
			if !summary.Recorded || summary.Provenance != testCase.provenance {
				t.Fatalf("summary = %+v, want provenance %q recorded", summary, testCase.provenance)
			}
			if summary.BackupPath != "" {
				t.Fatalf("BackupPath = %q, want empty: adopt writes nothing", summary.BackupPath)
			}
			if got := readFileRaw(t, settingsPath); !bytes.Equal(got, original) {
				t.Fatalf("settings.json = %s, want the bytes untouched %s", got, original)
			}
			for _, name := range backupFiles(t, settingsPath) {
				if strings.Contains(name, ".bak.") {
					t.Fatalf("adopt created a backup %q; it must not write at all", name)
				}
			}
			entry, ok := doorManifest(t).Entry(settingsPath)
			if !ok {
				t.Fatalf("the manifest does not claim %q", settingsPath)
			}
			if entry.Provenance != testCase.provenance || entry.SHA256 != SHA256Hex(original) {
				t.Fatalf("entry = %+v, want %q at the live digest", entry, testCase.provenance)
			}
		})
	}
}

// TestAdoptStatusLineAbsentRecordsNothing pins the absent case: there is no
// statusLine slot, so there is nothing to adopt and nothing is written.
func TestAdoptStatusLineAbsentRecordsNothing(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)

	summary, err := AdoptStatusLine()
	if err != nil {
		t.Fatalf("AdoptStatusLine: %v", err)
	}
	if summary.Recorded || summary.Provenance != "" {
		t.Fatalf("summary = %+v, want nothing recorded", summary)
	}
	if summary.Detail == "" {
		t.Fatalf("Detail is empty; a no-op adopt must say why")
	}
	if _, statErr := os.Stat(settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("adopt created settings.json: %v", statErr)
	}
	if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
		t.Fatalf("adopt created the manifest: %v", statErr)
	}
}

// TestAdoptStatusLineRefusesForeignShapes pins the repair-decision refusal. A
// document this build cannot read is surfaced, never normalized.
func TestAdoptStatusLineRefusesForeignShapes(t *testing.T) {
	settingsPath, manifestPath := settingsDoorSandbox(t)

	cases := []struct {
		name string
		doc  string
	}{
		{
			name: "undeclared field inside the object",
			doc:  `{"statusLine":{"type":"command","command":"x","enabled":true,"theme":"dark"}}`,
		},
		{
			name: "non-object slot",
			doc:  `{"statusLine":"agy-tokens --hook"}`,
		},
		{
			name: "object missing enabled",
			doc:  `{"statusLine":{"type":"command","command":"x"}}`,
		},
		{
			name: "unparseable document",
			doc:  `{"statusLine":`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := []byte(testCase.doc)
			writeFileRaw(t, settingsPath, document)

			if _, err := AdoptStatusLine(); !errors.Is(err, ErrStatusLineForeign) {
				t.Fatalf("AdoptStatusLine error = %v, want ErrStatusLineForeign", err)
			}
			if _, err := StatusLinePlan(""); !errors.Is(err, ErrStatusLineForeign) {
				t.Fatalf("StatusLinePlan error = %v, want ErrStatusLineForeign for a foreign document", err)
			}
			var foreignErr *StatusLineForeignError
			if _, err := AdoptStatusLine(); !errors.As(err, &foreignErr) || foreignErr.Detail == "" {
				t.Fatalf("AdoptStatusLine error = %v, want a *StatusLineForeignError with a detail", err)
			}
			if got := readFileRaw(t, settingsPath); !bytes.Equal(got, document) {
				t.Fatalf("settings.json = %s, want the refused bytes untouched", got)
			}
			if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
				t.Fatalf("the manifest was written for a refused document: %v", statErr)
			}
			for _, name := range backupFiles(t, settingsPath) {
				if strings.Contains(name, ".bak.") {
					t.Fatalf("a refused adopt created a backup %q", name)
				}
			}
		})
	}
}

// TestRetractStatusLineRemovesTheSlotOnly pins the retract: the managed slot goes,
// the unmanaged keys stay, the previous bytes are archived, and the bookkeeping
// entry is dropped.
func TestRetractStatusLineRemovesTheSlotOnly(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	launcher := writeStatusLineLauncher(t)
	original := settingsDocWithStatusLine(t, commandForLauncher(launcher), `"colorScheme":"dark"`, `"trustedWorkspaces":[]`)
	writeFileRaw(t, settingsPath, original)
	recordSettingsEntry(t, settingsPath, ProvenanceGoApplied)

	summary, err := RetractStatusLine()
	if err != nil {
		t.Fatalf("RetractStatusLine: %v", err)
	}
	if summary.BackupPath == "" {
		t.Fatalf("BackupPath is empty; retract must archive the previous bytes")
	}
	if got := readFileRaw(t, summary.BackupPath); !bytes.Equal(got, original) {
		t.Fatalf("backup bytes = %s, want %s", got, original)
	}
	after := decodeRawJSON(t, readFileRaw(t, settingsPath))
	if _, present := after["statusLine"]; present {
		t.Fatalf("statusLine survived the retract: %s", after["statusLine"])
	}
	if !bytes.Equal(after["colorScheme"], []byte(`"dark"`)) || !bytes.Equal(after["trustedWorkspaces"], []byte(`[]`)) {
		t.Fatalf("unmanaged keys changed: %s", readFileRaw(t, settingsPath))
	}
	if _, ok := doorManifest(t).Entry(settingsPath); ok {
		t.Fatalf("the manifest still claims settings.json after a retract")
	}
	if !summary.ManifestUpdated {
		t.Fatalf("ManifestUpdated = false, want the entry dropped")
	}

	// A second retract is stable and writes nothing.
	again, err := RetractStatusLine()
	if err != nil {
		t.Fatalf("second RetractStatusLine: %v", err)
	}
	if again.BackupPath != "" || again.ManifestUpdated {
		t.Fatalf("second retract = %+v, want a stable no-op", again)
	}
}

// TestRollbackStatusLineRestoresEachScheme pins the rollback over both chains: the
// newest coherent archive is restored byte for byte, the bytes it replaced are
// archived, and the archive itself is never moved or deleted.
func TestRollbackStatusLineRestoresEachScheme(t *testing.T) {
	cases := []struct {
		name   string
		scheme SettingsSnapshotScheme
		file   string
	}{
		{name: "node chain", scheme: SettingsSnapshotNode, file: "settings.bak.20260101-010101.json"},
		{name: "ledger chain", scheme: SettingsSnapshotLedger, file: "settings.json.bak.20260101-010101"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settingsPath, _ := settingsDoorSandbox(t)
			writeStatusLineLauncher(t)

			archived := []byte(`{"colorScheme":"before","trustedWorkspaces":[]}`)
			current := []byte(`{"colorScheme":"after","statusLine":{"type":"command","command":"x","enabled":true,"stack_with_default":true}}`)
			writeFileRaw(t, settingsPath, current)
			snapshotPath := filepath.Join(filepath.Dir(settingsPath), testCase.file)
			writeFileRaw(t, snapshotPath, archived)
			recordSettingsEntry(t, settingsPath, ProvenanceGoApplied)

			result, err := RollbackStatusLine()
			if err != nil {
				t.Fatalf("RollbackStatusLine: %v", err)
			}
			if result.Snapshot != snapshotPath || result.Scheme != testCase.scheme {
				t.Fatalf("result = %+v, want the %s snapshot %q", result, testCase.scheme, snapshotPath)
			}
			if got := readFileRaw(t, settingsPath); !bytes.Equal(got, archived) {
				t.Fatalf("settings.json = %s, want the archived bytes %s", got, archived)
			}
			if result.PreRestoreArchive == "" {
				t.Fatalf("PreRestoreArchive is empty; the replaced bytes must be archived")
			}
			if got := readFileRaw(t, result.PreRestoreArchive); !bytes.Equal(got, current) {
				t.Fatalf("pre-restore archive = %s, want the replaced bytes %s", got, current)
			}
			if !strings.Contains(filepath.Base(result.PreRestoreArchive), "settings.json.bak.") {
				t.Fatalf("pre-restore archive = %q, want the ledger's naming scheme", filepath.Base(result.PreRestoreArchive))
			}
			if _, statErr := os.Stat(snapshotPath); statErr != nil {
				t.Fatalf("the restored archive is gone: %v", statErr)
			}
			if result.LiveSHA != SHA256Hex(archived) {
				t.Fatalf("LiveSHA = %q, want the digest of the restored bytes", result.LiveSHA)
			}
			if _, ok := doorManifest(t).Entry(settingsPath); ok || !result.ManifestUpdated {
				t.Fatalf("the bookkeeping did not drop the entry after the rollback")
			}
		})
	}
}

// TestRollbackStatusLineSkipsIncoherentSnapshots pins the coherence rule: a torn
// archive is skipped and reported, and the next coherent one is restored.
func TestRollbackStatusLineSkipsIncoherentSnapshots(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	dir := filepath.Dir(settingsPath)

	coherent := []byte(`{"colorScheme":"good"}`)
	torn := []byte(`{"colorScheme":`)
	writeFileRaw(t, settingsPath, []byte(`{"colorScheme":"live"}`))
	writeFileRaw(t, filepath.Join(dir, "20260101-010101.json"), nil) // not a snapshot name at all
	writeFileRaw(t, filepath.Join(dir, "settings.bak.20260101-010101.json"), coherent)
	writeFileRaw(t, filepath.Join(dir, "settings.json.bak.20260102-020202"), torn)

	result, err := RollbackStatusLine()
	if err != nil {
		t.Fatalf("RollbackStatusLine: %v", err)
	}
	if got := readFileRaw(t, settingsPath); !bytes.Equal(got, coherent) {
		t.Fatalf("settings.json = %s, want the coherent snapshot %s (the torn one is newer)", got, coherent)
	}
	if result.Scheme != SettingsSnapshotNode {
		t.Fatalf("Scheme = %q, want the node chain", result.Scheme)
	}
	if len(result.Skipped) != 1 || !strings.HasSuffix(result.Skipped[0], "settings.json.bak.20260102-020202") {
		t.Fatalf("Skipped = %v, want exactly the torn snapshot", result.Skipped)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "settings.json.bak.20260102-020202")); statErr != nil {
		t.Fatalf("a skipped snapshot was deleted: %v", statErr)
	}
}

// TestRollbackStatusLineRefusesWithoutSnapshot pins the fail-closed rollback.
func TestRollbackStatusLineRefusesWithoutSnapshot(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	writeFileRaw(t, settingsPath, []byte(`{"colorScheme":"dark"}`))

	_, err := RollbackStatusLine()
	if !errors.Is(err, ErrStatusLineNoBackup) {
		t.Fatalf("RollbackStatusLine error = %v, want ErrStatusLineNoBackup", err)
	}
	assertContains(t, "rollback refusal", err.Error(), "settings.bak.<ts>.json", "settings.json.bak.<ts>")
	if got := readFileRaw(t, settingsPath); !bytes.Equal(got, []byte(`{"colorScheme":"dark"}`)) {
		t.Fatalf("settings.json changed on a refused rollback: %s", got)
	}
}

// TestStatusLineSnapshotsAreOrderedNewestFirst pins the ordering the rollback
// relies on, including the collision suffix inside one second.
func TestStatusLineSnapshotsAreOrderedNewestFirst(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	dir := filepath.Dir(settingsPath)
	for _, name := range []string{
		"settings.bak.20260101-010101.json",
		"settings.json.bak.20260101-010101",
		"settings.json.bak.20260101-010101-2",
		"settings.json.bak.20260103-030303",
		"settings.bak.not-a-timestamp.json",
		"unrelated.json",
	} {
		writeFileRaw(t, filepath.Join(dir, name), []byte(`{}`))
	}

	snapshots, err := settingsSnapshotsFor(settingsPath)
	if err != nil {
		t.Fatalf("settingsSnapshotsFor: %v", err)
	}
	got := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		got = append(got, filepath.Base(snapshot.Path))
	}
	want := []string{
		"settings.json.bak.20260103-030303",
		"settings.json.bak.20260101-010101-2",
		"settings.bak.20260101-010101.json",
		"settings.json.bak.20260101-010101",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot order = %v, want %v", got, want)
	}
	if snapshots[1].Index != 2 || snapshots[2].Scheme != SettingsSnapshotNode || snapshots[0].Scheme != SettingsSnapshotLedger {
		t.Fatalf("snapshot metadata = %+v, want the suffix and scheme read from the names", snapshots)
	}
}

// TestStatusLineDoorRefusesAForeignTarget pins the door's own range: the ledger's
// override would let a handle write any non-global path, so the door checks that
// the path it carries IS the global one.
func TestStatusLineDoorRefusesAForeignTarget(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "settings.json")
	moved := *door
	moved.SettingsPath = elsewhere
	if _, err := ApplyStatusLine(&moved); !errors.Is(err, ErrStatusLineDoorInvalid) {
		t.Fatalf("ApplyStatusLine error = %v, want ErrStatusLineDoorInvalid", err)
	}
	if _, statErr := os.Stat(elsewhere); !os.IsNotExist(statErr) {
		t.Fatalf("the door wrote %q: %v", elsewhere, statErr)
	}

	if _, err := ApplyStatusLine(nil); !errors.Is(err, ErrStatusLineDoorInvalid) {
		t.Fatalf("ApplyStatusLine(nil) error = %v, want ErrStatusLineDoorInvalid", err)
	}
	if _, statErr := os.Stat(settingsPath); !os.IsNotExist(statErr) {
		t.Fatalf("a refused door created settings.json: %v", statErr)
	}
}

// TestStatusLineDoorLeavesTheGlobalGuardClosed is the T1 invariant restated from
// this side: the door opens the global file through the deliberate override, and
// the ordinary Open still refuses it.
func TestStatusLineDoorLeavesTheGlobalGuardClosed(t *testing.T) {
	settingsPath, _ := settingsDoorSandbox(t)
	writeStatusLineLauncher(t)
	writeFileRaw(t, settingsPath, []byte(`{"colorScheme":"dark"}`))

	door, err := StatusLinePlan("")
	if err != nil {
		t.Fatalf("StatusLinePlan: %v", err)
	}
	if _, err := ApplyStatusLine(door); err != nil {
		t.Fatalf("ApplyStatusLine: %v", err)
	}

	if _, err := agysettings.Open(settingsPath); !errors.Is(err, agysettings.ErrGlobalSettingsPath) {
		t.Fatalf("agysettings.Open(global) error = %v, want ErrGlobalSettingsPath", err)
	}
	if _, err := agysettings.OpenWithOptions(settingsPath, agysettings.OpenOptions{}); !errors.Is(err, agysettings.ErrGlobalSettingsPath) {
		t.Fatalf("OpenWithOptions without AllowGlobal error = %v, want ErrGlobalSettingsPath", err)
	}
}

// TestStatusLineHookArgsIsACopy pins that a caller cannot mutate the command line.
func TestStatusLineHookArgsIsACopy(t *testing.T) {
	args := StatusLineHookArgs()
	if !reflect.DeepEqual(args, []string{"--hook", "--raw", "--write-dashboard"}) {
		t.Fatalf("StatusLineHookArgs() = %v", args)
	}
	args[0] = "--mutated"
	if StatusLineHookArgs()[0] != "--hook" {
		t.Fatalf("StatusLineHookArgs() returned the package slice, not a copy")
	}
}

// TestQuoteLauncher pins the quoting the command line needs on each OS.
func TestQuoteLauncher(t *testing.T) {
	previousGOOS := runtimeGOOS
	defer func() { runtimeGOOS = previousGOOS }()

	runtimeGOOS = "windows"
	if got := quoteLauncher(`C:\Users\a\agy-tokens.bat`); got != `C:\Users\a\agy-tokens.bat` {
		t.Fatalf("quoteLauncher(plain) = %q, want it bare", got)
	}
	if got := quoteLauncher(`C:\Users\a b\agy-tokens.bat`); got != `"C:\Users\a b\agy-tokens.bat"` {
		t.Fatalf("quoteLauncher(space) = %q, want it quoted", got)
	}

	runtimeGOOS = "linux"
	if got := quoteLauncher("/home/a/.local/bin/agy-tokens"); got != "/home/a/.local/bin/agy-tokens" {
		t.Fatalf("quoteLauncher(plain posix) = %q, want it bare", got)
	}
	if got := quoteLauncher("/home/a b/agy-tokens"); got != "'/home/a b/agy-tokens'" {
		t.Fatalf("quoteLauncher(space posix) = %q, want it single-quoted", got)
	}
	if got := quoteLauncher("/home/o'brien/agy-tokens"); got != `'/home/o'\''brien/agy-tokens'` {
		t.Fatalf("quoteLauncher(quote posix) = %q, want the embedded quote escaped", got)
	}
}

// TestStatusLineCommandLauncher pins the token extraction the provenance
// classifier reads.
func TestStatusLineCommandLauncher(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{command: "agy-tokens --hook --raw", want: "agy-tokens"},
		{command: `"C:\Users\a b\agy-tokens.bat" --hook`, want: `C:\Users\a b\agy-tokens.bat`},
		{command: `'/home/a b/agy-tokens' --hook`, want: "/home/a b/agy-tokens"},
		{command: "", want: ""},
	}
	for _, testCase := range cases {
		if got := commandLauncher(testCase.command); got != testCase.want {
			t.Fatalf("commandLauncher(%q) = %q, want %q", testCase.command, got, testCase.want)
		}
	}
	if !isAbsoluteLauncherPath(`C:\x\agy-tokens.bat`) || !isAbsoluteLauncherPath("/home/x/agy-tokens") {
		t.Fatalf("isAbsoluteLauncherPath rejected an absolute path")
	}
	if isAbsoluteLauncherPath("agy-tokens") {
		t.Fatalf("isAbsoluteLauncherPath accepted a bare PATH name")
	}
}
