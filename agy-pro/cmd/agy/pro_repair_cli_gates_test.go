package main

// P5-1 U3 gate tests: the exact-diff consent gate on the four CLI key-repair
// doors (W32 fix-keys + W33 the backup file that rides it, W34 rollback).
//
// Every case drives the door bodies through the in-memory seams the package
// already exposes (resolveRepairTargets, repairedWindowsKeyNames,
// rollbackKeyNames, repairCandidateSource) and asserts three things: the
// consent transcript, the exit code, and that the write seam was NOT reached.
// The live HKCU store is never read or written: providers' real environment
// store refuses to run under `go test`, and these tests never let a door get
// that far.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// repairDoorFixture isolates one fix-keys door run: a temp agy-pro home (so the
// backup directory assertion is real), a stubbed target resolution, a stubbed
// candidate read, and a counted repair seam.
type repairDoorFixture struct {
	names    []string
	calls    int
	gotNames []string
	home     string
}

func installRepairDoor(t *testing.T, names []string) *repairDoorFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	// The four-redirect discipline (F-S9B-03): the repair resolves the user home
	// for the protected settings path, so HOME/USERPROFILE must be sandboxed too.
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(paths.EnvGeminiConfigDir, filepath.Join(root, "gemini-config"))
	f := &repairDoorFixture{names: names, home: home}

	prevResolve, prevRepair, prevCandidates := resolveRepairTargets, repairedWindowsKeyNames, repairCandidateSource
	t.Cleanup(func() {
		resolveRepairTargets, repairedWindowsKeyNames, repairCandidateSource = prevResolve, prevRepair, prevCandidates
	})
	resolveRepairTargets = func(*providers.Registry, string) ([]string, error) {
		return names, nil
	}
	repairedWindowsKeyNames = func(_ *providers.Registry, opts providers.RepairOptions) (*providers.RepairReport, error) {
		f.calls++
		f.gotNames = append([]string(nil), opts.EnvNames...)
		return &providers.RepairReport{Repaired: len(opts.EnvNames)}, nil
	}
	repairCandidateSource = func(string) []providers.EnvCandidate { return nil }
	return f
}

// backupDirUsed is the directory the repair would create its backup under.
func (f *repairDoorFixture) backupDirUsed(t *testing.T) string {
	t.Helper()
	dir, err := paths.AgyProBackups()
	if err != nil {
		t.Fatalf("AgyProBackups: %v", err)
	}
	return dir
}

// runFixKeysDoor drives the doctor door with explicit streams.
func runFixKeysDoor(in string, target string, yes bool) (int, string, string) {
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	code := runDoctorFixKeysTo(strings.NewReader(in), out, errw, target, yes)
	return code, out.String(), errw.String()
}

// runRollbackDoor drives the doctor rollback door with explicit streams.
func runRollbackDoor(in string, target string, yes bool) (int, string, string) {
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	code := runDoctorRollbackTo(strings.NewReader(in), out, errw, target, yes)
	return code, out.String(), errw.String()
}

// writeKeyBackup writes one v2-shaped key backup (names only; the value fields
// stay empty because the gate never decodes them).
func writeKeyBackup(t *testing.T, dir string, entries string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "registry-keys.v2.json")
	body := fmt.Sprintf(`{"version":2,"createdAt":"2026-10-22T00:00:00Z","tool":"agypro/internal/providers.RepairWindowsKeyNames","entries":%s}`, entries)
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// TestKeyRepairGate_PipeRefusalNamesYes: a piped stdin without --yes prints the
// plan, refuses with a sentence naming --yes, exits 1, and touches neither the
// environment nor the backup directory. Both fix-keys doors.
func TestKeyRepairGate_PipeRefusalNamesYes(t *testing.T) {
	stubStdinIsTTY(t, false)

	t.Run("doctor --fix-keys", func(t *testing.T) {
		writeTestRegistry(t)
		f := installRepairDoor(t, []string{"W6_FIX_KEY"})
		code, out, errw := runFixKeysDoor("", "all", false)
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if f.calls != 0 {
			t.Errorf("repair ran without consent (%d calls)", f.calls)
		}
		if len(f.gotNames) != 0 {
			t.Errorf("repair received %v without consent", f.gotNames)
		}
		if !strings.Contains(out, "[agy-pro] Approve Key Repair Write") {
			t.Errorf("plan banner missing:\n%s", out)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("refusal must name --yes:\n%s", errw)
		}
		if !strings.Contains(errw, "a key-repair write") {
			t.Errorf("refusal must name the write class:\n%s", errw)
		}
		if !strings.Contains(errw, "nothing written") {
			t.Errorf("refusal must state that nothing was written:\n%s", errw)
		}
		if _, err := os.Stat(f.backupDirUsed(t)); err == nil {
			t.Errorf("backup directory was created without consent")
		}
	})

	t.Run("settings doctor fix-keys", func(t *testing.T) {
		installSettingsCore(t)
		f := installRepairDoor(t, []string{"W6_FIX_KEY"})
		code, out, errw := runSettings(t, []string{"doctor", "fix-keys", "all"}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if f.calls != 0 {
			t.Errorf("repair ran without consent (%d calls)", f.calls)
		}
		if !strings.Contains(out, "[agy-pro] Approve Key Repair Write") {
			t.Errorf("plan banner missing:\n%s", out)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("refusal must name --yes:\n%s", errw)
		}
		if _, err := os.Stat(f.backupDirUsed(t)); err == nil {
			t.Errorf("backup directory was created without consent")
		}
	})
}

// TestKeyRepairGate_YesConsentApplies: --yes is the scripted consent, and the
// repair then runs with exactly the resolved target list.
func TestKeyRepairGate_YesConsentApplies(t *testing.T) {
	writeTestRegistry(t)
	f := installRepairDoor(t, []string{"W6_FIX_KEY"})
	code, out, errw := runFixKeysDoor("", "all", true)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if f.calls != 1 {
		t.Fatalf("repair calls = %d; want 1", f.calls)
	}
	if len(f.gotNames) != 1 || f.gotNames[0] != "W6_FIX_KEY" {
		t.Errorf("repair received %v; want [W6_FIX_KEY]", f.gotNames)
	}
	if !strings.Contains(out, "[agy-pro] Approve Key Repair Write") {
		t.Errorf("--yes must still print the plan:\n%s", out)
	}
	if !strings.Contains(out, "Repaired: 1") {
		t.Errorf("repair report missing:\n%s", out)
	}
}

// TestKeyRepairGate_ConsoleConsent: a console answer of y applies, anything else
// cancels with nothing written and exit 0.
func TestKeyRepairGate_ConsoleConsent(t *testing.T) {
	stubStdinIsTTY(t, true)

	t.Run("y applies", func(t *testing.T) {
		writeTestRegistry(t)
		f := installRepairDoor(t, []string{"W6_FIX_KEY"})
		code, out, errw := runFixKeysDoor("y\n", "all", false)
		if code != 0 {
			t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
		}
		if f.calls != 1 {
			t.Fatalf("repair calls = %d; want 1", f.calls)
		}
		if !strings.Contains(out, "Apply this write to HKCU\\Environment") {
			t.Errorf("console prompt missing:\n%s", out)
		}
	})

	t.Run("n cancels", func(t *testing.T) {
		writeTestRegistry(t)
		f := installRepairDoor(t, []string{"W6_FIX_KEY"})
		code, out, _ := runFixKeysDoor("n\n", "all", false)
		if code != 0 {
			t.Fatalf("exit = %d; want 0", code)
		}
		if f.calls != 0 {
			t.Errorf("repair ran after a cancel")
		}
		if !strings.Contains(out, "[CANCELLED] nothing written") {
			t.Errorf("cancel line missing:\n%s", out)
		}
	})
}

// TestKeyRepairGate_StaleBaseDriftRefusal: the entry set is signed at
// presentation and re-read after consent. A target whose stored BOM variant
// disappeared while the diff was on screen refuses instead of writing the
// presented plan.
func TestKeyRepairGate_StaleBaseDriftRefusal(t *testing.T) {
	writeTestRegistry(t)
	f := installRepairDoor(t, []string{"W6_FIX_KEY"})

	presented := []providers.EnvCandidate{
		{Root: providers.EnvRootUser, Name: "W6_FIX_KEY", Exact: true},
		{Root: providers.EnvRootUser, Name: "\ufeffW6_FIX_KEY"},
	}
	reads := 0
	repairCandidateSource = func(string) []providers.EnvCandidate {
		reads++
		if reads == 1 {
			return presented
		}
		// The variant vanished between the presentation and the write.
		return []providers.EnvCandidate{{Root: providers.EnvRootUser, Name: "W6_FIX_KEY", Exact: true}}
	}

	code, out, errw := runFixKeysDoor("", "all", true)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if f.calls != 0 {
		t.Errorf("repair ran against a stale plan (%d calls)", f.calls)
	}
	if !strings.Contains(out, "rewrite the clean name from BOM variant <BOM>W6_FIX_KEY") {
		t.Errorf("presented plan missing the rewrite line:\n%s", out)
	}
	for _, want := range []string{"moved from rewrite to keep", "nothing written", "HKCU untouched", "no backup file written"} {
		if !strings.Contains(errw, want) {
			t.Errorf("stale-base refusal missing %q:\n%s", want, errw)
		}
	}
	if _, err := os.Stat(f.backupDirUsed(t)); err == nil {
		t.Errorf("backup directory was created for a refused write")
	}
}

// TestKeyRepairGate_DiffLineShapeMasked: the plan lines carry the environment
// variable NAME, the provider slot, and the variant NAME, never a value; a
// key-shaped name or provider renders masked, and the invisible BOM prefix is
// made visible.
func TestKeyRepairGate_DiffLineShapeMasked(t *testing.T) {
	const plantEnv = testfixtures.AnthropicPlanted
	writeTestRegistry(t)
	// W6_FIX_KEY is the env the test registry declares, so the line carries a
	// real provider slot; the planted env has no provider and must still mask.
	installRepairDoor(t, []string{"W6_FIX_KEY", plantEnv})

	repairCandidateSource = func(env string) []providers.EnvCandidate {
		if env == "W6_FIX_KEY" {
			return []providers.EnvCandidate{
				{Root: providers.EnvRootUser, Name: "W6_FIX_KEY", Exact: true},
				{Root: providers.EnvRootUser, Name: "\ufeffW6_FIX_KEY"},
			}
		}
		return []providers.EnvCandidate{{Root: providers.EnvRootUser, Name: env, Exact: true}}
	}

	code, out, errw := runFixKeysDoor("", "all", true)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if !strings.Contains(out, "  W6_FIX_KEY (provider fix): rewrite the clean name from BOM variant <BOM>W6_FIX_KEY, then delete that variant") {
		t.Errorf("rewrite line shape drifted:\n%s", out)
	}
	if !strings.Contains(out, "keep (no BOM variant stored beside the clean name)") {
		t.Errorf("keep line missing:\n%s", out)
	}
	if strings.Contains(out, "\ufeff") {
		t.Errorf("plan leaked a raw BOM byte into the transcript:\n%s", out)
	}
	if strings.Contains(out, plantEnv) {
		t.Errorf("plan leaked the planted key-shaped env name raw:\n%s", out)
	}
	if !strings.Contains(out, "values are copied, never printed") {
		t.Errorf("plan must state the value discipline:\n%s", out)
	}
}

// TestKeyRepairGate_CapsLongTargetList: the CLI mirror of the hub's bounded
// entries keeps the tail sentence and an exact hidden count.
func TestKeyRepairGate_CapsLongTargetList(t *testing.T) {
	names := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		names = append(names, fmt.Sprintf("KEY_%02d", i))
	}
	writeTestRegistry(t)
	installRepairDoor(t, names)

	code, out, errw := runFixKeysDoor("", "all", true)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
	}
	hidden := len(names) - (repairDiffEntryBudget - 1)
	if want := fmt.Sprintf("... %d more entries NOT shown (the diff above is partial)", hidden); !strings.Contains(out, want) {
		t.Errorf("bounded tail missing %q:\n%s", want, out)
	}
	// The budget bounds the DIFF entries (the informational "Targets:" line the
	// door has always printed is unchanged), so exactly budget-1 entry lines
	// render and the rest are the named tail.
	if got := strings.Count(out, "(provider -): keep"); got != repairDiffEntryBudget-1 {
		t.Errorf("rendered entry lines = %d; want %d:\n%s", got, repairDiffEntryBudget-1, out)
	}
}

// TestKeyRollbackGate_RefusesBeforeConsent: a missing or unparsable backup file
// refuses BEFORE the prompt, so no consent is ever asked for a plan that could
// not be composed, and the rollback seam never runs.
func TestKeyRollbackGate_RefusesBeforeConsent(t *testing.T) {
	t.Run("missing backup", func(t *testing.T) {
		rolled := 0
		prevRollback := rollbackKeyNames
		t.Cleanup(func() { rollbackKeyNames = prevRollback })
		rollbackKeyNames = func(string, providers.RepairOptions) error { rolled++; return nil }

		dir := t.TempDir() // exists, but records no backup file
		code, out, errw := runRollbackDoor("", dir, false)
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if rolled != 0 {
			t.Errorf("rollback ran without a backup file")
		}
		if strings.Contains(out, "Apply this write to") {
			t.Errorf("consent was asked for a plan that could not be composed:\n%s", out)
		}
		if strings.Contains(out, "[agy-pro] Approve Key Repair Write") {
			t.Errorf("gate banner printed for an unreadable backup:\n%s", out)
		}
		if !strings.Contains(errw, "Rollback failed:") {
			t.Errorf("missing-backup refusal must name the failure:\n%s", errw)
		}
		if !strings.Contains(errw, "registry-keys.v2.json") {
			t.Errorf("missing-backup refusal must name the resolved file:\n%s", errw)
		}
	})

	t.Run("unparsable backup", func(t *testing.T) {
		rolled := 0
		prevRollback := rollbackKeyNames
		t.Cleanup(func() { rollbackKeyNames = prevRollback })
		rollbackKeyNames = func(string, providers.RepairOptions) error { rolled++; return nil }

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "registry-keys.v2.json"), []byte("not a backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errw := runRollbackDoor("", dir, false)
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if rolled != 0 {
			t.Errorf("rollback ran against an unparsable backup")
		}
		if strings.Contains(out, "Apply this write to") {
			t.Errorf("consent was asked for an unparsable backup:\n%s", out)
		}
		for _, want := range []string{"Rollback refused before consent", "not a readable key backup", "nothing written"} {
			if !strings.Contains(errw, want) {
				t.Errorf("unparsable refusal missing %q:\n%s", want, errw)
			}
		}
	})
}

// TestKeyRollbackGate_YesConsentApplies: the rollback diff is composed from the
// backup file's recorded entries (names and existence only) and --yes applies
// the rollback against that exact file.
func TestKeyRollbackGate_YesConsentApplies(t *testing.T) {
	dir := t.TempDir()
	file := writeKeyBackup(t, dir, `[{"root":"user","name":"NW_API_KEY","existed":true},`+
		`{"root":"process-env","name":"OLD_TOKEN","existed":false}]`)

	var gotPath string
	prevRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = prevRollback })
	rollbackKeyNames = func(p string, _ providers.RepairOptions) error { gotPath = p; return nil }

	code, out, errw := runRollbackDoor("", dir, true)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if gotPath != file {
		t.Errorf("rollback got %q; want %q", gotPath, file)
	}
	for _, want := range []string{
		"[agy-pro] Approve Key Repair Write",
		"Backup file: " + file,
		"restore NW_API_KEY in HKCU\\Environment",
		"delete  OLD_TOKEN in the process environment",
		"The backup file is only read; nothing under the backup directory changes.",
		"[SUCCESS] Registry key names restored from backup.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rollback transcript missing %q:\n%s", want, out)
		}
	}
}

// TestKeyRollbackGate_TranscriptPinsAndStaleBase: the presented entry set is
// re-read after consent, so a backup file that moved refuses, and the rollback
// diff names an empty backup honestly instead of showing a blank plan.
func TestKeyRollbackGate_StaleBaseAndEmptyBackup(t *testing.T) {
	t.Run("entry set moved", func(t *testing.T) {
		dir := t.TempDir()
		writeKeyBackup(t, dir, `[{"root":"user","name":"NW_API_KEY","existed":true}]`)

		rolled := 0
		prevRollback := rollbackKeyNames
		t.Cleanup(func() { rollbackKeyNames = prevRollback })
		rollbackKeyNames = func(string, providers.RepairOptions) error { rolled++; return nil }

		// Rewrite the file between the presentation and the re-verify by
		// flipping it from the second read onward: the door reads once for the
		// plan, then again inside reverify.
		reads := 0
		prevSnapshot := keyBackupReadHook
		t.Cleanup(func() { keyBackupReadHook = prevSnapshot })
		keyBackupReadHook = func() {
			reads++
			if reads == 2 {
				writeKeyBackup(t, dir, `[{"root":"user","name":"OTHER_KEY","existed":true}]`)
			}
		}

		code, _, errw := runRollbackDoor("", dir, true)
		if code != 1 {
			t.Fatalf("exit = %d; want 1", code)
		}
		if rolled != 0 {
			t.Errorf("rollback ran against a stale backup")
		}
		for _, want := range []string{"the key backup changed on disk", "nothing written", "HKCU untouched"} {
			if !strings.Contains(errw, want) {
				t.Errorf("stale-base refusal missing %q:\n%s", want, errw)
			}
		}
	})

	t.Run("empty backup names the refusal", func(t *testing.T) {
		dir := t.TempDir()
		writeKeyBackup(t, dir, `[]`)

		prevRollback := rollbackKeyNames
		t.Cleanup(func() { rollbackKeyNames = prevRollback })
		rollbackKeyNames = func(string, providers.RepairOptions) error { return nil }

		code, out, _ := runRollbackDoor("", dir, true)
		if code != 0 {
			t.Fatalf("exit = %d; want 0 (the verb owns the refusal)", code)
		}
		if !strings.Contains(out, "The backup records no entries: the rollback refuses after consent (nothing written).") {
			t.Errorf("empty-backup plan line missing:\n%s", out)
		}
	})
}

// TestKeyRepairGate_SettingsDoorGatedAndHelpNamesYes pins the second door's
// consent path and the usage text both doors are documented by.
func TestKeyRepairGate_SettingsDoorGatedAndHelpNamesYes(t *testing.T) {
	stubStdinIsTTY(t, false)
	tc := installSettingsCore(t)
	// The second door resolves its targets inside the Core verb, against the
	// Core's own registry, so the fixture registry declares the variable the
	// plan is composed for.
	tc.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "nw", BaseURL: "https://nw.example/v1", APIKeyEnv: "W6_FIX_KEY"},
	}}
	installRepairDoor(t, []string{"W6_FIX_KEY"})

	code, out, errw := runSettings(t, []string{"doctor", "fix-keys", "--yes"}, "")
	if !strings.Contains(out, "[agy-pro] Approve Key Repair Write") {
		t.Errorf("settings door must print the plan:\n%s", out)
	}
	// The write path is reached only after consent: the Core verb runs, and it
	// stops at the environment store's test rail instead of reaching HKCU.
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (the store refuses the write under test) stderr=%s", code, errw)
	}
	if !strings.Contains(errw, "fix-keys finished with errors:") {
		t.Errorf("post-consent failure tail missing:\n%s", errw)
	}

	// --yes is a flag, never the fix-keys operand: the resolved target stays
	// the default "all" instead of becoming "--yes".
	var gotTarget string
	resolveRepairTargets = func(_ *providers.Registry, arg string) ([]string, error) {
		gotTarget = arg
		return []string{"W6_FIX_KEY"}, nil
	}
	runSettings(t, []string{"doctor", "fix-keys", "--yes"}, "")
	if gotTarget != "all" {
		t.Errorf("fix-keys target = %q; want %q", gotTarget, "all")
	}

	for _, want := range []string{"fix-keys [targets] [--yes]", "rollback-keys <dir> [--yes]"} {
		if !strings.Contains(proSettingsUsage, want) {
			t.Errorf("proSettingsUsage missing %q:\n%s", want, proSettingsUsage)
		}
	}
	if !strings.Contains(doctorUsage, "--yes") {
		t.Errorf("doctorUsage must name --yes:\n%s", doctorUsage)
	}
}

// TestParseDoctorArgsYesFlag pins the flag parsing: --yes sets consent, never a
// target, and combines with either action.
func TestParseDoctorArgsYesFlag(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		action     doctorAction
		wantYes    bool
		wantTarget string
	}{
		{args: []string{"--fix-keys", "--yes"}, action: doctorFixKeys, wantYes: true},
		{args: []string{"--yes", "--fix-keys=nw"}, action: doctorFixKeys, wantYes: true, wantTarget: "nw"},
		{args: []string{"--rollback-keys", "dir", "--yes"}, action: doctorRollbackKeys, wantYes: true, wantTarget: "dir"},
		{args: []string{"--yes", "--rollback-keys=dir"}, action: doctorRollbackKeys, wantYes: true, wantTarget: "dir"},
	} {
		got, err := parseDoctorArgs(tc.args)
		if err != nil {
			t.Errorf("parseDoctorArgs(%v): %v", tc.args, err)
			continue
		}
		if got.action != tc.action {
			t.Errorf("parseDoctorArgs(%v) action = %v; want %v", tc.args, got.action, tc.action)
		}
		if got.yes != tc.wantYes {
			t.Errorf("parseDoctorArgs(%v) yes = %v; want %v", tc.args, got.yes, tc.wantYes)
		}
		if got.target != tc.wantTarget {
			t.Errorf("parseDoctorArgs(%v) target = %q; want %q", tc.args, got.target, tc.wantTarget)
		}
	}
}

// TestKeyRollbackGate_BackupFileIsNamesOnly pins that the diff composition
// decodes names only: a backup carrying a value field must not surface it.
func TestKeyRollbackGate_BackupFileIsNamesOnly(t *testing.T) {
	dir := t.TempDir()
	const value = "sk-" + "live-backup-value-must-not-print"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"version":   2,
		"createdAt": "2026-10-22T00:00:00Z",
		"tool":      "agypro/internal/providers.RepairWindowsKeyNames",
		"entries": []map[string]any{
			{"root": "user", "name": "NW_API_KEY", "existed": true, "value": value},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry-keys.v2.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	prevRollback := rollbackKeyNames
	t.Cleanup(func() { rollbackKeyNames = prevRollback })
	rollbackKeyNames = func(string, providers.RepairOptions) error { return nil }

	code, out, errw := runRollbackDoor("", dir, true)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if strings.Contains(out, value) || strings.Contains(errw, value) {
		t.Errorf("rollback transcript leaked a backup VALUE:\nstdout=%s\nstderr=%s", out, errw)
	}
}
