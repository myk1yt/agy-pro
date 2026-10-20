package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repairProbe is a deterministic stand-in for ProbeKeyValidity: the live QA
// verdicts (mock value rejected, real value accepted) as a table.
func repairProbe(baseURL, apiKey string) KeyProbeResult {
	switch apiKey {
	case "mock-nw-key-999", "dead-key-value-0002":
		return KeyProbeResult{Status: ProbeInvalid, HTTPStatus: 401, Detail: "invalid_api_key"}
	case "sk-real-nw-valid-key-0001", "sk-valid-promoted-00003":
		return KeyProbeResult{Status: ProbeValid, HTTPStatus: 200}
	default:
		return KeyProbeResult{Status: ProbeSkipped, Detail: "network unreachable (test stand-in)"}
	}
}

func nwRegistry() *Registry {
	return &Registry{Providers: []Provider{
		{Name: "nw", BaseURL: "https://api.neuralwatt.example/v1", APIKeyEnv: "W6_REPAIR_NW_KEY"},
		{Name: "charm", BaseURL: "https://charm.example/v1", APIKeyEnv: "W6_REPAIR_CHARM_KEY"},
		{Name: "nokey", BaseURL: "https://x.example/v1"},
	}}
}

// fakeProtector is a reversible stand-in for DPAPI so backup tests never need
// the real user key (and stay deterministic about the stored bytes).
type fakeProtector struct{}

func (fakeProtector) Protect(plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

func (fakeProtector) Unprotect(ciphertext []byte) ([]byte, error) {
	out := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

// useFakeProtector swaps the backup protector for the test and restores it.
func useFakeProtector(t *testing.T) {
	t.Helper()
	old := backupProtector
	backupProtector = fakeProtector{}
	t.Cleanup(func() { backupProtector = old })
}

func TestResolveRepairTargets(t *testing.T) {
	reg := nwRegistry()
	cases := []struct {
		arg     string
		want    []string
		errWant bool
	}{
		{arg: "", want: []string{"W6_REPAIR_NW_KEY", "W6_REPAIR_CHARM_KEY"}},
		{arg: "all", want: []string{"W6_REPAIR_NW_KEY", "W6_REPAIR_CHARM_KEY"}},
		{arg: "ALL", want: []string{"W6_REPAIR_NW_KEY", "W6_REPAIR_CHARM_KEY"}},
		{arg: "nw", want: []string{"W6_REPAIR_NW_KEY"}},
		{arg: "W6_REPAIR_CHARM_KEY", want: []string{"W6_REPAIR_CHARM_KEY"}},
		{arg: "nw,charm", want: []string{"W6_REPAIR_NW_KEY", "W6_REPAIR_CHARM_KEY"}},
		{arg: "nw , nw", want: []string{"W6_REPAIR_NW_KEY"}},
		{arg: "nokey", errWant: true},
		{arg: "does-not-exist", errWant: true},
	}
	for _, c := range cases {
		got, err := ResolveRepairTargets(reg, c.arg)
		if c.errWant {
			if err == nil {
				t.Errorf("ResolveRepairTargets(%q) expected error, got %v", c.arg, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveRepairTargets(%q) unexpected error: %v", c.arg, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ResolveRepairTargets(%q) = %v; want %v", c.arg, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ResolveRepairTargets(%q)[%d] = %q; want %q", c.arg, i, got[i], c.want[i])
			}
		}
	}
	if _, err := ResolveRepairTargets(nil, "all"); err == nil {
		t.Error("nil registry must error")
	}
}

func TestRepairWindowsKeyNames_RewritesAndBackups(t *testing.T) {
	useFakeProtector(t)
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_REPAIR_NW_KEY":       "mock-nw-key-999",
		"\ufeffW6_REPAIR_NW_KEY": "sk-real-nw-valid-key-0001",
	}
	backupDir := filepath.Join(t.TempDir(), "backups", "run1")

	report, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{
		EnvNames:  []string{"W6_REPAIR_NW_KEY"},
		Store:     store,
		Probe:     repairProbe,
		BackupDir: backupDir,
	})
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	if report.Repaired != 1 || report.Skipped != 0 || report.Failed != 0 {
		t.Errorf("report counters = %+v", report)
	}
	if v, ok, _ := store.Get(EnvRootUser, "W6_REPAIR_NW_KEY"); !ok || v != "sk-real-nw-valid-key-0001" {
		t.Errorf("clean name not rewritten: (%q, %v)", v, ok)
	}
	if _, ok, _ := store.Get(EnvRootUser, "\ufeffW6_REPAIR_NW_KEY"); ok {
		t.Error("BOM variant entry was not removed")
	}
	if os.Getenv("W6_REPAIR_NW_KEY") != "sk-real-nw-valid-key-0001" {
		t.Errorf("process env not refreshed: %q", os.Getenv("W6_REPAIR_NW_KEY"))
	}
	t.Cleanup(func() { _ = os.Unsetenv("W6_REPAIR_NW_KEY") })

	if !strings.HasSuffix(report.BackupPath, backupFileNameV2) ||
		!strings.Contains(report.BackupPath, "run1") {
		t.Errorf("backup path = %q", report.BackupPath)
	}
	raw, err := os.ReadFile(report.BackupPath)
	if err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	// S-5: the backup must be restorable but not plaintext - values are
	// protected at rest and the file carries the format version.
	var backup keyBackupFile
	if err := json.Unmarshal(raw, &backup); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if backup.Version != backupFormatVersion {
		t.Errorf("backup version = %d; want %d", backup.Version, backupFormatVersion)
	}
	if backup.Tool != repairToolName {
		t.Errorf("backup tool = %q; want %q", backup.Tool, repairToolName)
	}
	if strings.Contains(string(raw), "mock-nw-key-999") || strings.Contains(string(raw), "sk-real-nw-valid-key-0001") {
		t.Error("backup file contains key values in plaintext")
	}
	found := map[string]keyBackupEntry{}
	for _, e := range backup.Entries {
		found[e.Root+"|"+e.Name] = e
	}
	checkValue := func(key, want string) {
		e := found[key]
		if !e.Existed {
			t.Errorf("backup entry %s missing or not marked as existing: %+v", key, e)
			return
		}
		got, err := decodeBackupValue(backup.Version, e.Value)
		if err != nil {
			t.Errorf("backup entry %s does not decode: %v", key, err)
			return
		}
		if got != want {
			t.Errorf("backup entry %s = %q; want %q", key, got, want)
		}
	}
	checkValue(EnvRootUser+"|W6_REPAIR_NW_KEY", "mock-nw-key-999")
	checkValue(EnvRootUser+"|\ufeffW6_REPAIR_NW_KEY", "sk-real-nw-valid-key-0001")
	e := found[EnvRootProcess+"|W6_REPAIR_NW_KEY"]
	if e.Existed {
		t.Errorf("process env should have been recorded as absent before repair: %+v", e)
	}

	actions := map[RepairAction]int{}
	for _, entry := range report.Entries {
		if entry.Action != "" {
			actions[entry.Action]++
		}
	}
	if actions[RepairActionRewrite] != 1 || actions[RepairActionRemoveVariant] != 1 {
		t.Errorf("expected one rewrite and one removal entry, got %v", actions)
	}
}

func TestRepairWindowsKeyNames_PromotesVariantWhenCleanAbsent(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"\ufeffW6_REPAIR_NW_KEY": "sk-valid-promoted-00003",
	}
	report, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{
		EnvNames: []string{"W6_REPAIR_NW_KEY"}, Store: store, Probe: repairProbe,
		BackupDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.Repaired != 1 {
		t.Fatalf("expected 1 repair, got %+v", report)
	}
	if v, ok, _ := store.Get(EnvRootUser, "W6_REPAIR_NW_KEY"); !ok || v != "sk-valid-promoted-00003" {
		t.Errorf("clean name not created: (%q, %v)", v, ok)
	}
	if _, ok, _ := store.Get(EnvRootUser, "\ufeffW6_REPAIR_NW_KEY"); ok {
		t.Error("BOM variant not removed")
	}
	t.Cleanup(func() { _ = os.Unsetenv("W6_REPAIR_NW_KEY") })
}

func TestRepairWindowsKeyNames_SkipPreconditions(t *testing.T) {
	cases := []struct {
		name      string
		storeVals map[string]string
		probe     func(baseURL, apiKey string) KeyProbeResult
		wantNote  string
	}{
		{
			name: "clean value already valid",
			storeVals: map[string]string{
				"W6_REPAIR_NW_KEY":       "sk-real-nw-valid-key-0001",
				"\ufeffW6_REPAIR_NW_KEY": "mock-nw-key-999",
			},
			probe:    repairProbe,
			wantNote: "already validates",
		},
		{
			name: "network blocked on clean value",
			storeVals: map[string]string{
				"W6_REPAIR_NW_KEY":       "unknown-value-not-in-table",
				"\ufeffW6_REPAIR_NW_KEY": "sk-real-nw-valid-key-0001",
			},
			probe:    repairProbe,
			wantNote: "cannot verify",
		},
		{
			name: "no valid variant",
			storeVals: map[string]string{
				"W6_REPAIR_NW_KEY":       "mock-nw-key-999",
				"\ufeffW6_REPAIR_NW_KEY": "dead-key-value-0002",
			},
			probe:    repairProbe,
			wantNote: "no BOM variant carries a probe-valid value",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newMockEnvStore()
			store.data[EnvRootUser] = c.storeVals
			backupDir := t.TempDir()
			report, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{
				EnvNames: []string{"W6_REPAIR_NW_KEY"}, Store: store, Probe: c.probe,
				BackupDir: backupDir,
			})
			if err != nil {
				t.Fatalf("repair returned error: %v", err)
			}
			if report.Repaired != 0 || report.Skipped != 1 {
				t.Fatalf("expected skip, got %+v", report)
			}
			if !strings.Contains(report.Entries[0].Note, c.wantNote) {
				t.Errorf("note = %q; want it to contain %q", report.Entries[0].Note, c.wantNote)
			}
			// Nothing may have changed.
			for name, want := range c.storeVals {
				if v, ok, _ := store.Get(EnvRootUser, name); !ok || v != want {
					t.Errorf("store mutated despite skip: %q -> (%q, %v)", name, v, ok)
				}
			}
			if _, err := os.Stat(filepath.Join(backupDir, "registry-keys.json")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("backup must not be written when nothing is repaired: %v", err)
			}
			t.Cleanup(func() { _ = os.Unsetenv("W6_REPAIR_NW_KEY") })
		})
	}
}

func TestRepairWindowsKeyNames_RefusesRealStoreUnderTests(t *testing.T) {
	_, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{})
	if err == nil || !strings.Contains(err.Error(), "tests") {
		t.Errorf("expected test-safety refusal, got %v", err)
	}
}

func TestRepairWindowsKeyNames_BackupFailureAborts(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_REPAIR_NW_KEY":       "mock-nw-key-999",
		"\ufeffW6_REPAIR_NW_KEY": "sk-real-nw-valid-key-0001",
	}
	// A regular file cannot become a backup directory: the backup write fails
	// and the repair must abort before touching the store.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{
		EnvNames: []string{"W6_REPAIR_NW_KEY"}, Store: store, Probe: repairProbe,
		BackupDir: blocker,
	})
	if err == nil {
		t.Fatal("expected abort error when the backup cannot be written")
	}
	if v, ok, _ := store.Get(EnvRootUser, "W6_REPAIR_NW_KEY"); !ok || v != "mock-nw-key-999" {
		t.Errorf("store was mutated despite backup failure: (%q, %v)", v, ok)
	}
	if _, ok, _ := store.Get(EnvRootUser, "\ufeffW6_REPAIR_NW_KEY"); !ok {
		t.Error("BOM variant was deleted despite backup failure")
	}
}

func TestRollbackWindowsKeyNames(t *testing.T) {
	useFakeProtector(t)
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_REPAIR_NW_KEY":       "mock-nw-key-999",
		"\ufeffW6_REPAIR_NW_KEY": "sk-real-nw-valid-key-0001",
	}
	opts := RepairOptions{
		EnvNames: []string{"W6_REPAIR_NW_KEY"}, Store: store, Probe: repairProbe,
		BackupDir: filepath.Join(t.TempDir(), "ts"),
	}
	report, err := RepairWindowsKeyNames(nwRegistry(), opts)
	if err != nil || report.Repaired != 1 {
		t.Fatalf("repair did not run: %+v, %v", report, err)
	}
	if os.Getenv("W6_REPAIR_NW_KEY") == "" {
		t.Fatal("expected process env refresh after repair")
	}

	if err := RollbackWindowsKeyNames(report.BackupPath, opts); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if v, ok, _ := store.Get(EnvRootUser, "W6_REPAIR_NW_KEY"); !ok || v != "mock-nw-key-999" {
		t.Errorf("clean name not restored: (%q, %v)", v, ok)
	}
	if v, ok, _ := store.Get(EnvRootUser, "\ufeffW6_REPAIR_NW_KEY"); !ok || v != "sk-real-nw-valid-key-0001" {
		t.Errorf("BOM variant not restored: (%q, %v)", v, ok)
	}
	// The process env had no W6_REPAIR_NW_KEY before the repair; rollback must
	// return it to that state.
	if _, still := os.LookupEnv("W6_REPAIR_NW_KEY"); still {
		t.Error("process env not restored (variable should be unset again)")
	}

	if err := RollbackWindowsKeyNames(filepath.Join(t.TempDir(), "missing.json"), opts); err == nil {
		t.Error("missing backup file must error")
	}
}

func TestRepairAllTargetsFromRegistry(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{
		"W6_REPAIR_NW_KEY":          "mock-nw-key-999",
		"\ufeffW6_REPAIR_NW_KEY":    "sk-real-nw-valid-key-0001",
		"W6_REPAIR_CHARM_KEY":       "sk-charm-already-fine",
		"\ufeffW6_REPAIR_CHARM_KEY": "sk-charm-twin-value",
	}
	probe := func(baseURL, apiKey string) KeyProbeResult {
		switch apiKey {
		case "mock-nw-key-999":
			return KeyProbeResult{Status: ProbeInvalid, HTTPStatus: 401}
		case "sk-real-nw-valid-key-0001", "sk-charm-already-fine":
			return KeyProbeResult{Status: ProbeValid, HTTPStatus: 200}
		default:
			return KeyProbeResult{Status: ProbeInvalid, HTTPStatus: 401}
		}
	}
	report, err := RepairWindowsKeyNames(nwRegistry(), RepairOptions{
		Store: store, Probe: probe, BackupDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if report.Repaired != 1 || report.Skipped != 1 {
		t.Fatalf("expected 1 repaired + 1 skipped, got %+v", report)
	}
	if v, _, _ := store.Get(EnvRootUser, "W6_REPAIR_CHARM_KEY"); v != "sk-charm-already-fine" {
		t.Errorf("valid clean value must be untouched, got %q", v)
	}
	t.Cleanup(func() { _ = os.Unsetenv("W6_REPAIR_NW_KEY"); _ = os.Unsetenv("W6_REPAIR_CHARM_KEY") })
}

// writeCraftedBackup writes a raw backup file for the rollback validation
// tests (S-5 / S-9).
func writeCraftedBackup(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), backupFileNameV2)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// S-5 / S-9: a backup file is untrusted input. Dangerous names, machine-root
// entries and foreign provenance must be refused before any mutation.
func TestRollbackRejectsUnsafeEntries(t *testing.T) {
	store := newMockEnvStore()
	store.data[EnvRootUser] = map[string]string{"PATH": "C:\\safe"}
	opts := RepairOptions{Store: store, Probe: repairProbe}

	cases := []struct {
		name    string
		content string
	}{
		{
			name: "PATH name",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"user","name":"PATH","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "NODE_OPTIONS name",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"user","name":"NODE_OPTIONS","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "machine root",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"machine","name":"EVIL_TOKEN","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "unknown root",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"hive","name":"EVIL_TOKEN","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "foreign provenance",
			content: `{"version":2,"tool":"someone-else","entries":[
				{"root":"user","name":"W6_OK_KEY","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "non-credential name",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"user","name":"PYTHONSTARTUP","existed":true,"value":"Qw=="}]}`,
		},
		{
			name: "BOM-wrapped dangerous name",
			content: `{"version":2,"tool":"` + repairToolName + `","entries":[
				{"root":"user","name":"\ufeffPATH","existed":true,"value":"Qw=="}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeCraftedBackup(t, tc.content)
			err := RollbackWindowsKeyNames(path, opts)
			if err == nil {
				t.Fatal("crafted backup was applied; expected refusal")
			}
			// Nothing may have been mutated.
			if v, _, _ := store.Get(EnvRootUser, "PATH"); v != "C:\\safe" {
				t.Errorf("PATH was mutated: %q", v)
			}
		})
	}

	// Mixed file: one good entry plus one dangerous one must be refused whole
	// (fail-closed, never partially applied).
	mixed := writeCraftedBackup(t, `{"version":2,"tool":"`+repairToolName+`","entries":[
		{"root":"user","name":"W6_GOOD_KEY","existed":true,"value":"Qw=="},
		{"root":"user","name":"PATH","existed":true,"value":"Qw=="}]}`)
	if err := RollbackWindowsKeyNames(mixed, opts); err == nil {
		t.Fatal("mixed backup with a dangerous entry must be refused whole")
	}
	if _, ok, _ := store.Get(EnvRootUser, "W6_GOOD_KEY"); ok {
		t.Error("refused backup must not be partially applied")
	}
}

// S-5: legacy plaintext backups (version 1) still roll back, but only through
// the same whitelist.
func TestRollbackAcceptsLegacyPlaintextV1(t *testing.T) {
	store := newMockEnvStore()
	useFakeProtector(t)
	path := filepath.Join(t.TempDir(), backupFileNameV1)
	content := `{"tool":"` + repairToolName + `","entries":[
		{"root":"user","name":"W6_LEGACY_KEY","existed":true,"value":"sk-legacy-plaintext"}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RollbackWindowsKeyNames(path, RepairOptions{Store: store, Probe: repairProbe}); err != nil {
		t.Fatalf("legacy rollback failed: %v", err)
	}
	if v, ok, _ := store.Get(EnvRootUser, "W6_LEGACY_KEY"); !ok || v != "sk-legacy-plaintext" {
		t.Errorf("legacy value not restored: (%q, %v)", v, ok)
	}
}

// S-5: retention pruning keeps the newest backupRetention key-backup
// directories and never touches other backup directories.
func TestPruneOldKeyBackups(t *testing.T) {
	useFakeProtector(t)
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	base := filepath.Join(home, "backups")
	// A non-key backup directory (mode-style) that must survive.
	other := filepath.Join(base, "profile-backup")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 12 key backups in a fixed order.
	for i := 1; i <= 12; i++ {
		ts := time.Date(2026, 1, 2, 3, 4, i, 0, time.UTC)
		_, err := writeRepairBackup([]*repairPlan{{
			env:     "W6_PRUNE_KEY",
			clean:   &EnvCandidate{Root: EnvRootUser, Name: "W6_PRUNE_KEY", Exact: true, Value: "v"},
			variant: EnvCandidate{Root: EnvRootUser, Name: "\ufeffW6_PRUNE_KEY", Value: "v2"},
			rewrite: true,
		}}, "", func() time.Time { return ts })
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}

	var kept []string
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			kept = append(kept, e.Name())
		}
	}
	// 10 key backups + the profile-backup directory.
	if len(kept) != backupRetention+1 {
		t.Fatalf("kept %d dirs %v; want %d key backups + 1 unrelated", len(kept), kept, backupRetention+1)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated backup directory was pruned: %v", err)
	}
	// Oldest two (03:04:01, 03:04:02) must be gone, newest must survive.
	for _, gone := range []string{"20260102_030401", "20260102_030402"} {
		if _, err := os.Stat(filepath.Join(base, gone)); err == nil {
			t.Errorf("old backup %s was not pruned", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "20260102_030412", backupFileNameV2)); err != nil {
		t.Errorf("newest backup missing: %v", err)
	}
}

// S-5: ResolveBackupFile accepts a directory or a file and prefers the
// versioned layout.
func TestResolveBackupFile(t *testing.T) {
	dir := t.TempDir()
	if got := ResolveBackupFile(dir); got != filepath.Join(dir, backupFileNameV2) {
		t.Errorf("empty dir resolve = %q; want %s", got, backupFileNameV2)
	}
	if err := os.WriteFile(filepath.Join(dir, backupFileNameV1), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveBackupFile(dir); got != filepath.Join(dir, backupFileNameV1) {
		t.Errorf("v1-only dir resolve = %q; want %s", got, backupFileNameV1)
	}
	if err := os.WriteFile(filepath.Join(dir, backupFileNameV2), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveBackupFile(dir); got != filepath.Join(dir, backupFileNameV2) {
		t.Errorf("v2 dir resolve = %q; want %s", got, backupFileNameV2)
	}
	file := filepath.Join(t.TempDir(), "custom.json")
	if got := ResolveBackupFile(file); got != file {
		t.Errorf("direct file resolve = %q; want %q", got, file)
	}
}

// S-5: one real DPAPI round trip (read/write of the user key store, not the
// registry) proves the production protector works.
func TestDPAPIProtectorRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only")
	}
	p := dpapiProtector{}
	blob, err := p.Protect([]byte("sk-dpapi-roundtrip-value"))
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("Protect returned an empty blob")
	}
	if bytes.Contains(blob, []byte("sk-dpapi-roundtrip-value")) {
		t.Error("ciphertext contains the plaintext")
	}
	back, err := p.Unprotect(blob)
	if err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if string(back) != "sk-dpapi-roundtrip-value" {
		t.Errorf("round trip = %q; want the original value", back)
	}
}

// TestWriteRepairBackup_SameSecondGetsUniqueDir is the F-8 regression: two
// repair runs within the same second must not share one backup directory (the
// first backup used to be overwritten). The suffix pattern (_2, _3, ...)
// matches mode.uniqueBackupDir so lexical order stays chronological.
func TestWriteRepairBackup_SameSecondGetsUniqueDir(t *testing.T) {
	useFakeProtector(t)
	t.Setenv(EnvHome, t.TempDir())
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := func() time.Time { return fixed }

	plans := []*repairPlan{{
		env:           "W6_UNIQ_KEY",
		provider:      "uniq",
		clean:         &EnvCandidate{Root: EnvRootUser, Name: "W6_UNIQ_KEY", Exact: true, Value: "stale-value-0001"},
		variant:       EnvCandidate{Root: EnvRootUser, Name: "\ufeffW6_UNIQ_KEY", Exact: false, Value: "live-value-00002"},
		rewrite:       true,
		deleteVariant: true,
	}}

	p1, err := writeRepairBackup(plans, "", now)
	if err != nil {
		t.Fatalf("first backup: %v", err)
	}
	firstData, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("reading first backup: %v", err)
	}

	p2, err := writeRepairBackup(plans, "", now)
	if err != nil {
		t.Fatalf("second backup: %v", err)
	}
	p3, err := writeRepairBackup(plans, "", now)
	if err != nil {
		t.Fatalf("third backup: %v", err)
	}

	if p1 == p2 || p1 == p3 || p2 == p3 {
		t.Fatalf("same-second backups collided: %q, %q, %q", p1, p2, p3)
	}
	if !strings.HasSuffix(p1, filepath.Join("20260102_030405", backupFileNameV2)) {
		t.Errorf("first backup path = %q; want 20260102_030405/%s", p1, backupFileNameV2)
	}
	base := filepath.Dir(p1)
	if want := filepath.Join(base+"_2", backupFileNameV2); p2 != want {
		t.Errorf("second backup path = %q; want %q", p2, want)
	}
	if want := filepath.Join(base+"_3", backupFileNameV2); p3 != want {
		t.Errorf("third backup path = %q; want %q", p3, want)
	}

	// The first backup must still hold its own snapshot, not the last write.
	afterData, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("reading first backup after later runs: %v", err)
	}
	if string(firstData) != string(afterData) {
		t.Errorf("first backup was overwritten by a later same-second run")
	}
	for _, p := range []string{p1, p2, p3} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("backup %s missing: %v", p, err)
		}
	}
}

// failingSetStore fails every Set (the rewrite) while Get/Names/Delete behave.
type failingSetStore struct{ *mockEnvStore }

func (f *failingSetStore) Set(root, name, value string) error {
	return fmt.Errorf("access is denied (test)")
}

// failingDeleteStore fails every Delete (the BOM variant removal).
type failingDeleteStore struct{ *mockEnvStore }

func (f *failingDeleteStore) Delete(root, name string) error {
	return fmt.Errorf("access is denied (test)")
}

// TestRepairWindowsKeyNames_AggregateErrorMasksKeyShapedEnv (E6): the
// aggregate repair errors embed the env name; a key-shaped env (misdirected
// paste in the user-editable registry) must reach the CLI tail masked, for
// both the rewrite-failure and the remove-variant-failure branches.
func TestRepairWindowsKeyNames_AggregateErrorMasksKeyShapedEnv(t *testing.T) {
	useFakeProtector(t)

	t.Run("rewrite failure masks the env prefix", func(t *testing.T) {
		const skPlant = "sk-ant-api03-PLANT0123456789abcdef"
		store := newMockEnvStore()
		store.data[EnvRootUser] = map[string]string{
			"\ufeff" + skPlant: "sk-real-nw-valid-key-0001", // BOM variant probes VALID
		}
		report, err := RepairWindowsKeyNames(&Registry{Providers: []Provider{
			{Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: skPlant},
		}}, RepairOptions{
			EnvNames:  []string{skPlant},
			Store:     &failingSetStore{store},
			Probe:     repairProbe,
			BackupDir: filepath.Join(t.TempDir(), "backups"),
		})
		if report == nil || report.Failed != 1 {
			t.Fatalf("report = %+v, err = %v; want one failed rewrite", report, err)
		}
		if err == nil {
			t.Fatal("want an aggregate error from the failed rewrite")
		}
		if !strings.Contains(err.Error(), "sk-an****") {
			t.Errorf("aggregate error must carry the masked env prefix, got %q", err)
		}
		if strings.Contains(err.Error(), skPlant) {
			t.Errorf("aggregate error leaks the planted env raw: %q", err)
		}
		for _, e := range report.Entries {
			if strings.Contains(e.Note, skPlant) {
				t.Errorf("entry note leaks the planted env raw: %q", e.Note)
			}
		}
	})

	t.Run("remove-variant failure masks the env prefix", func(t *testing.T) {
		const akiaPlant = "AKIAIOSFODNN7EXAMPLE"
		store := newMockEnvStore()
		store.data[EnvRootUser] = map[string]string{
			akiaPlant:            "mock-nw-key-999",           // clean present, probe INVALID
			"\ufeff" + akiaPlant: "sk-real-nw-valid-key-0001", // variant probes VALID
		}
		report, err := RepairWindowsKeyNames(&Registry{Providers: []Provider{
			{Name: "awsish", BaseURL: "https://aws.example/v1", APIKeyEnv: akiaPlant},
		}}, RepairOptions{
			EnvNames:  []string{akiaPlant},
			Store:     &failingDeleteStore{store},
			Probe:     repairProbe,
			BackupDir: filepath.Join(t.TempDir(), "backups"),
		})
		t.Cleanup(func() { _ = os.Unsetenv(akiaPlant) })
		// The remove-variant branch counts the rewrite as Repaired; only the
		// Set failure increments Failed.
		if report == nil || report.Repaired != 1 || report.Failed != 0 {
			t.Fatalf("report = %+v, err = %v; want one repaired rewrite with a failed remove", report, err)
		}
		if err == nil {
			t.Fatal("want an aggregate error from the failed variant removal")
		}
		if !strings.Contains(err.Error(), "AKIAI****: remove variant") {
			t.Errorf("aggregate error must carry the masked env prefix, got %q", err)
		}
		if strings.Contains(err.Error(), akiaPlant) {
			t.Errorf("aggregate error leaks the planted env raw: %q", err)
		}
	})
}

// F-beta F7: the rollback refusal sentences echo backup-file-derived entry
// names (S-9 untrusted input); a crafted file planting a credential in the
// name slot must not survive the error text, benign names stay readable.
func TestRollbackRefusalErrorsMaskPlantedEntryNames(t *testing.T) {
	store := newMockEnvStore()
	opts := RepairOptions{Store: store, Probe: repairProbe}

	t.Run("root refusal masks the planted name", func(t *testing.T) {
		const skPlant = "sk-ant-api03-PLANT0123456789abcdef"
		path := writeCraftedBackup(t, `{"version":2,"tool":"`+repairToolName+`","entries":[
			{"root":"machine","name":"`+skPlant+`","existed":true,"value":"Qw=="}]}`)
		err := RollbackWindowsKeyNames(path, opts)
		if err == nil {
			t.Fatal("machine-root backup was applied; expected refusal")
		}
		if strings.Contains(err.Error(), skPlant) {
			t.Errorf("root refusal echoes the planted name raw: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "is not allowed on rollback") {
			t.Errorf("root refusal missing the mask or wording: %v", err)
		}
	})

	t.Run("shape refusal masks the planted name", func(t *testing.T) {
		const skPlant = "sk-ant-api03-PLANT0123456789abcdef"
		path := writeCraftedBackup(t, `{"version":2,"tool":"`+repairToolName+`","entries":[
			{"root":"user","name":"`+skPlant+`","existed":true,"value":"Qw=="}]}`)
		err := RollbackWindowsKeyNames(path, opts)
		if err == nil {
			t.Fatal("key-shaped non-credential name was applied; expected refusal")
		}
		if strings.Contains(err.Error(), skPlant) {
			t.Errorf("shape refusal echoes the planted name raw: %v", err)
		}
		if !strings.Contains(err.Error(), `entry "[REDACTED]"`) || !strings.Contains(err.Error(), "does not look like an API key variable") {
			t.Errorf("shape refusal missing the mask or wording: %v", err)
		}
	})

	t.Run("decode refusal masks the planted name", func(t *testing.T) {
		const plant = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
		path := writeCraftedBackup(t, `{"version":2,"tool":"`+repairToolName+`","entries":[
			{"root":"user","name":"`+plant+`","existed":true,"value":"!!!not-base64!!!"}]}`)
		err := RollbackWindowsKeyNames(path, opts)
		if err == nil {
			t.Fatal("invalid base64 value must refuse the backup")
		}
		if strings.Contains(err.Error(), plant) {
			t.Errorf("decode refusal echoes the planted name raw: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "backup value is not valid base64") {
			t.Errorf("decode refusal missing the mask or wording: %v", err)
		}
	})

	t.Run("benign names stay readable", func(t *testing.T) {
		path := writeCraftedBackup(t, `{"version":2,"tool":"`+repairToolName+`","entries":[
			{"root":"user","name":"W6_OK_KEY","existed":true,"value":"!!!not-base64!!!"}]}`)
		err := RollbackWindowsKeyNames(path, opts)
		if err == nil {
			t.Fatal("invalid base64 value must refuse the backup")
		}
		if !strings.Contains(err.Error(), `entry "W6_OK_KEY"`) {
			t.Errorf("benign entry name must stay readable: %v", err)
		}
		if strings.Contains(err.Error(), "[REDACTED]") {
			t.Errorf("benign refusal must not be redacted: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the "--fix-keys" target resolver quotes the
// registry provider NAME it found. It is a registry slot, so it renders
// through MaskSlot; benign names keep their exact bytes.
// ---------------------------------------------------------------------------

func TestResolveRepairTargetsEchoesRegistryNameThroughSlotAuthority(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)

	reg := &Registry{Providers: []Provider{{Name: plant, BaseURL: "https://plant.example/v1"}}}
	if _, err := ResolveRepairTargets(reg, plant); err == nil {
		t.Fatal("a provider without an apiKeyEnv must be a repair-target error")
	} else if strings.Contains(err.Error(), plant) {
		t.Errorf("repair-target error echoes the registry name raw: %v", err)
	} else if want := `provider "` + plantMask + `" declares no API key variable; nothing to repair`; err.Error() != want {
		t.Errorf("repair-target error drifted\n got: %q\nwant: %q", err.Error(), want)
	}

	// Benign corpus: the sentence is byte-identical.
	benign := &Registry{Providers: []Provider{
		{Name: "acme", BaseURL: "https://acme.example/v1"},
		{Name: "acme-corp", BaseURL: "https://acme-corp.example/v1"},
	}}
	for _, name := range []string{"acme", "acme-corp"} {
		_, err := ResolveRepairTargets(benign, name)
		if err == nil {
			t.Fatalf("%s: expected a repair-target error", name)
		}
		if want := `provider "` + name + `" declares no API key variable; nothing to repair`; err.Error() != want {
			t.Errorf("%s: sentence drifted\n got: %q\nwant: %q", name, err.Error(), want)
		}
	}
}
