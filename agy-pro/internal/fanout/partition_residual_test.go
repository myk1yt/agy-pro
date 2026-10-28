package fanout

// Residual close-out pins (E1 HIGH-1, E2, E3) for the partition merge.
//
// E1: the D3 fix masked every summary.json sink EXCEPT the manifest's
// conflicts block, which scanPartitionConflicts builds from the RAW unit ids
// and claim paths; a key-shaped unit id on a CONFLICT run therefore persisted
// verbatim into partition.conflicts[].units on disk. The pins below re-read
// the FAILED run's summary.json from disk and assert the mask.
//
// E2: plan-authored id/title/seat/scope values were echoed raw by the
// parse-time validation errors and the seat-resolution errors, which run
// BEFORE the merge redactor exists, and by the runner scope refusal persisted
// to the tool log; the CLI error mask seeds only --seats (empty in
// --units mode). The pins assert a key-shaped value renders masked in a
// ParsePlan duplicate-id/overlap error, a partitionTargets seat-resolution
// error, the CLI stderr path, and the logged runner refusal, while benign ids
// stay readable.
//
// E3: a symlinked/junctioned workspace root made WalkDir visit only the root
// link, leaving zero claims and a vacuous disjointness audit while the run
// still reported "merged". The pins assert a real overlap is still detected
// through a symlinked root.
//
// Every pin is mutation-verified (see impl-b5/fix-b5e-report.md): bypassing
// redactPartitionConflicts, maskPlanError/refuseOutOfScope seeding, or the
// EvalSymlinks walk root fails the corresponding pin.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// keyShapedUnitID is a fake key-shaped dummy (the canonical AWS documentation
// example shape): 40 base64-alphabet runes with the "/" real keys carry. It is
// a stand-in for a misdirected key paste, never a real credential.
const keyShapedUnitID = testfixtures.AWSSecretExample

// --- E1: the on-disk summary.json of a CONFLICT run masks the conflicts -----

// E1 (HIGH-1): a partition merge that FAILS on overlap with a key-shaped unit
// id writes summary.json with partition.conflicts[].units MASKED. The engine
// returns a nil summary on a failed run, so the pin RE-READS the on-disk
// summary.json (the D3 pin asserted the error string and the success-path
// summary only). Benign ids in the same block stay readable.
func TestMergePartitionConflictSummaryMasksKeyShapedUnitID(t *testing.T) {
	units := []Unit{
		{ID: keyShapedUnitID, Title: "A", Scope: []string{"src/a*.go"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-b", Title: "B", Scope: []string{"src/*a.go"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err == nil {
		t.Fatal("overlapping scopes must FAIL the run; got nil error (silent merge)")
	}
	if strings.Contains(run.err.Error(), keyShapedUnitID) {
		t.Errorf("failure error leaks the key-shaped unit id: %v", run.err)
	}

	// The disk-level pin: the written summary.json of the FAILED run.
	raw, err := os.ReadFile(filepath.Join(mergeOutDir(run), "summary.json"))
	if err != nil {
		t.Fatalf("summary.json not written on a failed run: %v", err)
	}
	if strings.Contains(string(raw), keyShapedUnitID) {
		t.Errorf("summary.json leaks the raw key id in the conflicts block:\n%s", raw)
	}
	s := readMergeSummaryJSON(t, run)
	if s.Partition == nil || s.Partition.Status != partitionStatusFailed {
		t.Fatalf("manifest = %+v; want a failed partition block", s.Partition)
	}
	if len(s.Partition.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v; want exactly one", s.Partition.Conflicts)
	}
	c := s.Partition.Conflicts[0]
	if c.Path != "src/aba.go" {
		t.Errorf("conflict path = %q; want src/aba.go (benign paths stay readable)", c.Path)
	}
	// Sorted raw ids are [unit-b keyID]; masked, the benign id must stay
	// readable and the key-shaped id must render [REDACTED].
	if len(c.Units) != 2 || c.Units[0] != "unit-b" || c.Units[1] != "[REDACTED]" {
		t.Errorf("conflict units = %v; want [unit-b [REDACTED]] (benign readable, key masked)", c.Units)
	}
}

// E1: a credential-shaped run inside the conflict path renders masked in the
// on-disk summary.json too. The fixture workspace carries
// keys/AKIAExample.txt (the contract's AWS-key-ID-shaped file NAME: the
// canonical documentation example shape), claimed by two units.
func TestMergePartitionConflictSummaryMasksSecretShapedPath(t *testing.T) {
	units := []Unit{
		{ID: "unit-a", Title: "A", Scope: []string{"keys"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "unit-b", Title: "B", Scope: []string{"keys/*.txt"}, Deliverable: "d", Seat: "prov-b/model-b"},
	}
	run := mergePartitionRun(t, units, nil, proseByModel)
	if run.err == nil {
		t.Fatal("overlapping scopes must FAIL the run; got nil error (silent merge)")
	}
	raw, err := os.ReadFile(filepath.Join(mergeOutDir(run), "summary.json"))
	if err != nil {
		t.Fatalf("summary.json not written on a failed run: %v", err)
	}
	if strings.Contains(string(raw), testfixtures.AKIAExample) {
		t.Errorf("summary.json leaks the credential-shaped file name in the conflicts block:\n%s", raw)
	}
	s := readMergeSummaryJSON(t, run)
	if len(s.Partition.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v; want exactly one", s.Partition.Conflicts)
	}
	if got := s.Partition.Conflicts[0].Path; got != "keys/[REDACTED].txt" {
		t.Errorf("conflict path = %q; want keys/[REDACTED].txt (credential-shaped run masked)", got)
	}

	// The human-readable conflict section rides the same masked manifest.
	report, err := os.ReadFile(filepath.Join(mergeOutDir(run), "reconciliation.md"))
	if err != nil {
		t.Fatalf("reconciliation.md not written: %v", err)
	}
	if strings.Contains(string(report), testfixtures.AKIAExample) {
		t.Errorf("reconciliation.md leaks the credential-shaped file name:\n%s", report)
	}
}

// --- E2: plan-field seeds in the pre-merge error sinks -----------------------

// E2: a key-shaped unit id is masked in the ParsePlan duplicate-id error, and
// the benign ids/positions around it stay readable.
func TestParsePlanMasksKeyShapedIDInDuplicateIDError(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["src/a/*.go"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["src/b/*.go"],"deliverable":"db"},
      {"id":"` + keyShapedUnitID + `","title":"c","scope":["src/c/*.go"],"deliverable":"dc"},
      {"id":"` + keyShapedUnitID + `","title":"d","scope":["src/d/*.go"],"deliverable":"dd"}
    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected a duplicate-id error, got units %+v", units)
	}
	if strings.Contains(err.Error(), keyShapedUnitID) {
		t.Errorf("duplicate-id error leaks the raw key id: %v", err)
	}
	// The id slot now renders through the registry-slot authority (class closure,
	// L-2), so the marker is either the seeded [REDACTED] substitution or the
	// authority's first-5 mask; both are masked renderings.
	for _, want := range []string{"units 3 and 4", `duplicate unit id`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") && !strings.Contains(err.Error(), "****") {
		t.Errorf("error %q carries no masked id", err)
	}
}

// E2: a key-shaped SCOPE entry is masked in the ParsePlan scope-overlap error
// (and in the validatePartitionUnits backstop that re-checks a library-built
// plan), while the quoted benign scope entries beside it stay readable.
func TestParsePlanMasksKeyShapedScopeEntryInOverlapError(t *testing.T) {
	doc := []byte(`[
      {"id":"u1","title":"a","scope":["` + keyShapedUnitID + `"],"deliverable":"da"},
      {"id":"u2","title":"b","scope":["` + keyShapedUnitID + `/x"],"deliverable":"db"}
    ]`)
	units, err := ParsePlan(doc)
	if err == nil {
		t.Fatalf("expected a scope-overlap error, got units %+v", units)
	}
	if strings.Contains(err.Error(), keyShapedUnitID) {
		t.Errorf("scope-overlap error leaks the raw key-shaped scope entry: %v", err)
	}
	if !strings.Contains(err.Error(), "scope overlap") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("error %q must name the overlap with the secret masked", err)
	}

	// The engine-side validator (B4 backstop) quotes the unit id via unitLabel:
	// a key-shaped id on a rejected unit masks there too.
	bad := []Unit{
		{ID: keyShapedUnitID, Title: "t", Scope: []string{"."}, Deliverable: "d"},
	}
	verr := validatePartitionUnits(bad)
	if verr == nil {
		t.Fatal("validatePartitionUnits must reject a root-wide scope")
	}
	if strings.Contains(verr.Error(), keyShapedUnitID) {
		t.Errorf("validatePartitionUnits error leaks the raw key id: %v", verr)
	}
	if !strings.Contains(verr.Error(), "root-wide") {
		t.Errorf("error %q must name the root-wide rejection", verr)
	}
}

// E2: a key-shaped unit id is masked in the partitionTargets seat-resolution
// errors (unparsable seat, multi-seat token, native-seat refusal, unresolvable
// model), which reach the CLI engine-error path that seeds only --seats.
// Benign ids in the same messages stay readable.
func TestPartitionTargetsMasksKeyShapedIDInSeatResolutionErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	t.Setenv("PT_MASK_KEY", "pt-mask-key")
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: "http://127.0.0.1:1", APIKeyEnv: "PT_MASK_KEY",
			Models: []providers.Model{
				{ID: "model-a", Family: "family-a", Enabled: &trueVal},
			}},
	}}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	unit := func(seat string) []Unit {
		return []Unit{{ID: keyShapedUnitID, Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: seat}}
	}
	cases := []struct {
		name    string
		seat    string
		wantSub string
	}{
		{"unparsable-seat", "native:", `unit "[REDACTED]":`},
		{"multi-seat", "prov-a/model-a,prov-a/model-a", `unit "[REDACTED]":`},
		{"native-refusal", "native:gemini-x", "provider seats only"},
		{"unresolvable-model", "nope/model-x", `failed to resolve model "nope/model-x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, terr := partitionTargets(RunConfig{Task: partTask, Root: dir, Units: unit(tc.seat)}, reg, state, nil)
			if terr == nil {
				t.Fatalf("seat %q: expected a seat-resolution error, got nil", tc.seat)
			}
			if strings.Contains(terr.Error(), keyShapedUnitID) {
				t.Errorf("seat-resolution error leaks the raw key id: %v", terr)
			}
			if !strings.Contains(terr.Error(), tc.wantSub) {
				t.Errorf("error %q must contain %q", terr, tc.wantSub)
			}
		})
	}

	// Readability control: benign ids stay readable in the same error.
	_, _, _, berr := partitionTargets(RunConfig{Task: partTask, Root: dir, Units: []Unit{
		{ID: "auth-review", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "native:gemini-x"},
	}}, reg, state, nil)
	if berr == nil || !strings.Contains(berr.Error(), `unit "auth-review":`) {
		t.Errorf("benign id must stay readable in the seat-resolution error: %v", berr)
	}
}

// E2 end-to-end at the CLI surface: a plan whose duplicate unit id is
// key-shaped exits 2 with the id masked on stderr. Pre-E2 the ParsePlan error
// reached cli.go's usage-error print unmasked (its seed list is --seats/
// --tools/--units, and the plan-file path seeds nothing).
func TestRunUnitsMasksKeyShapedIDInPlanErrorStderr(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	dir := t.TempDir()
	plan := writePlanFile(t, dir, "plan.json", `[
      {"id":"u1","title":"a","scope":["src/a/*.go"],"deliverable":"da"},
      {"id":"`+keyShapedUnitID+`","title":"c","scope":["src/c/*.go"],"deliverable":"dc"},
      {"id":"`+keyShapedUnitID+`","title":"d","scope":["src/d/*.go"],"deliverable":"dd"}
    ]`)
	var out, errw strings.Builder
	code := Run([]string{"--units", plan}, nil, &out, &errw)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (usage error), stderr = %s", code, errw.String())
	}
	if strings.Contains(errw.String(), keyShapedUnitID) {
		t.Errorf("stderr leaks the raw key id:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "[REDACTED]") && !strings.Contains(errw.String(), "****") {
		t.Errorf("stderr carries no redaction marker:\n%s", errw.String())
	}
}

// --- E3: a symlinked workspace root must not vacate the claim walk -----------

// Unit-level pin: with the workspace root itself symlinked, expandClaimedFiles
// still claims the real files under LOGICAL-root-relative paths (no "../.."
// prefix) and the overlap scan still fires.
func TestExpandClaimedFilesSymlinkedRootStillClaims(t *testing.T) {
	base := t.TempDir()
	realWS := filepath.Join(base, "realws")
	linkWS := filepath.Join(base, "linkws")
	for _, rel := range []string{filepath.Join("src", "alpha", "ok.go"), "src/aba.go"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(realWS, rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(realWS, rel), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realWS, linkWS); err != nil {
		t.Skipf("symlink creation not permitted on this host: %v", err)
	}

	units := []Unit{
		{ID: "unit-a", Title: "A", Scope: []string{"src/a*.go"}, Deliverable: "d"},
		{ID: "unit-b", Title: "B", Scope: []string{"src/*a.go"}, Deliverable: "d"},
	}
	claims, err := expandClaimedFiles(linkWS, units)
	if err != nil {
		t.Fatalf("expandClaimedFiles through a symlinked root: %v", err)
	}
	if len(claims[0]) == 0 || len(claims[1]) == 0 {
		t.Fatalf("claims = %v; want both units claiming through the symlinked root (vacuous walk)", claims)
	}
	for _, list := range claims {
		for _, f := range list {
			if strings.HasPrefix(f, "..") || filepath.IsAbs(f) {
				t.Errorf("claim path %q is not a logical-root-relative path", f)
			}
		}
	}
	conflicts := scanPartitionConflicts(units, claims)
	if len(conflicts) != 1 || conflicts[0].Path != "src/aba.go" {
		t.Errorf("conflicts = %+v; want src/aba.go claimed by both units", conflicts)
	}
}

// Engine-level pin: a partition run whose workspace ROOT is a symlink still
// FAILS on a real overlap (pre-E3 the walk visited only the root link, the
// audit was vacuous, and the run reported "merged").
func TestMergePartitionSymlinkedRootOverlapFailsRun(t *testing.T) {
	setup := func(t *testing.T, rootSymlink bool) *mergeRunResult {
		t.Helper()
		rec := &chatRecorder{respond: proseByModel}
		server := httptest.NewServer(rec)
		t.Cleanup(server.Close)

		base := t.TempDir()
		realWS := filepath.Join(base, "realws")
		for _, rel := range []string{
			filepath.Join("src", "alpha", "ok.go"),
			filepath.Join("src", "beta", "no.go"),
			filepath.Join("src", "aba.go"),
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(realWS, rel)), 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", rel, err)
			}
			if err := os.WriteFile(filepath.Join(realWS, rel), []byte("package x\n"), 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}
		root := realWS
		if rootSymlink {
			link := filepath.Join(base, "linkws")
			if err := os.Symlink(realWS, link); err != nil {
				t.Skipf("symlink creation not permitted on this host: %v", err)
			}
			root = link
		}
		t.Setenv(providers.EnvHome, base)

		trueVal := true
		reg := &providers.Registry{Providers: []providers.Provider{
			{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "E3_KEY_A",
				Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
					Capabilities: providers.Capabilities{SupportsTools: true}}}},
			{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "E3_KEY_B",
				Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
					Capabilities: providers.Capabilities{SupportsTools: true}}}},
		}}
		t.Setenv("E3_KEY_A", "key-a")
		t.Setenv("E3_KEY_B", "key-b")
		regPath := filepath.Join(base, "providers.json")
		writeRegistry(t, regPath, reg)

		units := []Unit{
			{ID: "unit-a", Title: "A", Scope: []string{"src/a*.go"}, Deliverable: "d", Seat: "prov-a/model-a"},
			{ID: "unit-b", Title: "B", Scope: []string{"src/*a.go"}, Deliverable: "d", Seat: "prov-b/model-b"},
		}
		cfg := RunConfig{
			Task:         partTask,
			Root:         root,
			OutDir:       filepath.Join(base, "reports"),
			Units:        units,
			RegistryPath: regPath,
			MaxTurns:     6,
			TimeoutSec:   60,
		}
		summary, err := Execute(context.Background(), cfg)
		return &mergeRunResult{rec: rec, summary: summary, root: base, err: err}
	}

	// Control: same plan through the real root fails as always.
	control := setup(t, false)
	if control.err == nil {
		t.Fatal("control: overlapping scopes must FAIL the run; got nil error")
	}

	// E3: through the symlinked root the overlap must STILL fail the run (a
	// vacuous walk would report "merged" with a nil error).
	run := setup(t, true)
	if run.err == nil {
		t.Fatal("symlinked root: overlapping scopes must FAIL the run; got a vacuous merged run")
	}
	for _, want := range []string{"conflict", "src/aba.go", "unit-a", "unit-b"} {
		if !strings.Contains(run.err.Error(), want) {
			t.Errorf("run error = %q; want it to name %q", run.err.Error(), want)
		}
	}
	s := readMergeSummaryJSON(t, run)
	if s.Partition.Status != partitionStatusFailed || len(s.Partition.Conflicts) != 1 {
		t.Fatalf("manifest = %+v; want failed with exactly one conflict", s.Partition)
	}
	c := s.Partition.Conflicts[0]
	if c.Path != "src/aba.go" || len(c.Units) != 2 {
		t.Errorf("conflict = %+v; want src/aba.go claimed by both units", c)
	}
	// Claim paths on disk are logical-root-relative (no "../.." leak).
	for _, u := range s.Partition.Units {
		for _, f := range u.Files {
			if strings.HasPrefix(f, "..") {
				t.Errorf("manifest file %q escaped the logical root", f)
			}
		}
	}
}
