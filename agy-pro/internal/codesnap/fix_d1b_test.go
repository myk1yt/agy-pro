package codesnap

// FIX-D1b pins (fix-list-3 rows R3-7, R3-8 and the codesnap half of R3-3).
//
// What this file locks down:
//
//	R3-8 bare secret names - a file whose WHOLE name is a secret-class suffix
//	                        (".pem", ".key") is withheld like "server.pem" is,
//	                        and the class comparison runs through the guard's
//	                        NTFS name fold rather than strings.ToLower.
//	R3-7 ledger name fold   - the run-directory refusal and the walk exclusion
//	                        tolerate a case-variant --out or scope spelling, so
//	                        the S3 shape defense is not re-opened by spelling.
//	                        FIX-E1 REVISED the mechanism this row shipped: those
//	                        two comparisons use the ASCII case fold
//	                        (namePolicy.dirKey), probed from the root's volume,
//	                        while the ledger's recorded/withheld lookups are now
//	                        EXACT paths (N1). TestRollbackKeepsCaseVariantRecordedRows
//	                        below pins the revised answer.
//	R3-3 row arms           - the manifest row policy refuses a row that carries
//	                        the Win32 stream syntax or the 8.3 alias shape WHOLE
//	                        at load, and the archive destination is gated by the
//	                        same predicate.
//
// The platform fork is asserted, not skipped: on a host whose filesystem is
// case-sensitive the same two spellings name two directories, and folding there
// would exclude a directory the ledger is not in (a silent under-capture), so the
// POSIX branch asserts the byte-exact behaviour. The fork is decided by the
// runtime probe, which is why the branch reads the resolved policy rather than a
// GOOS constant.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// windowsHost reports whether this host has the Win32 name semantics the two
// shape arms are gated on. It matches the build tags of
// agenttools/guard_win32_windows.go and guard_win32_other.go, which are not
// reachable from here.
func windowsHost() bool { return runtime.GOOS == "windows" }

// d1bForgeManifest writes m to dest/manifest.json (the forgery every
// row-policy pin starts from).
//
// Since FIX-F4 the forgery is re-signed with the ledger's own key: the row-policy
// and identity arms run AFTER the provenance check, so an unsigned forgery would
// never reach them. Re-signing is what a compromised key would buy, which is why
// those arms are pinned as defense in depth.
func d1bForgeManifest(t *testing.T, dest string, m Manifest) {
	t.Helper()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatalf("marshal forged manifest: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dest, manifestFileName), append(raw, '\n'), 0o600); werr != nil {
		t.Fatalf("write forged manifest: %v", werr)
	}
	resignManifest(t, dest)
}

// TestSecretClassNameCoversBareSuffixNames pins the class predicate itself
// (R3-8 plus the folded compare D1a exported): the name THAT IS the suffix is a
// member, a case variant is the same member, and the `>=` widening did not turn
// the suffix rule into a loose match.
func TestSecretClassNameCoversBareSuffixNames(t *testing.T) {
	cases := []struct {
		rel    string
		family string
		hit    bool
	}{
		// R3-8: the whole name is the suffix (the pre-fix `len > len` rule let
		// exactly these through).
		{".pem", "*.pem", true},
		{".key", "*.key", true},
		{".pfx", "*.pfx", true},
		{".p12", "*.p12", true},
		{".kdbx", "*.kdbx", true},
		{".ppk", "*.ppk", true},
		// The fold: a case variant is the same class member.
		{".PEM", "*.pem", true},
		{".Key", "*.key", true},
		{"config/SERVER.PEM", "*.pem", true},
		// The canonical component: trailing dots and spaces are stripped first.
		{".pem.", "*.pem", true},
		{".key ", "*.key", true},
		// A longer name keeps matching (the pre-fix behaviour, unchanged).
		{"keys/server.key", "*.key", true},
		{"src/dev.env", "*.env", true},
		// The prefix arms.
		{".env", ".env*", true},
		{"id_rsa", "id_rsa*", true},
		// Non-members: the `>=` widening is a whole-name arm, not a loose suffix.
		{"pem", "", false},
		{"pem.txt", "", false},
		{".pe", "", false},
		{".pemx", "", false},
		{"notes.md", "", false},
		{"archive.tar.gz", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		family, hit := secretClassName(c.rel)
		if hit != c.hit {
			t.Errorf("secretClassName(%q) hit = %v; want %v", c.rel, hit, c.hit)
			continue
		}
		if c.hit && family != c.family {
			t.Errorf("secretClassName(%q) family = %q; want %q", c.rel, family, c.family)
		}
	}
}

// TestTakeWithholdsBareSuffixSecretNames is the behavioral half of R3-8: the
// bare-name files are WITHHELD (no bytes copied, no ledger row) rather than
// captured, and nothing downstream pretends coverage.
func TestTakeWithholdsBareSuffixSecretNames(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"config/.pem":       "BARE-PEM-MATERIAL\n",
		"config/.key":       "BARE-KEY-MATERIAL\n",
		"config/.PFX":       "BARE-PFX-MATERIAL\n",
		"config/notes.md":   "# notes\n",
		"config/server.pem": "LONG-PEM-MATERIAL\n",
	})
	m, err := Take(root, []string{"config/*"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got, want := rowPaths(m), []string{"config/notes.md"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v (a bare suffix name is withheld, not copied)", got, want)
	}

	reasons := make(map[string]string, len(m.Skipped))
	for _, skip := range m.Skipped {
		reasons[skip.Path] = skip.Reason
	}
	for _, want := range []string{"config/.pem", "config/.key", "config/.PFX", "config/server.pem"} {
		reason, ok := reasons[want]
		if !ok {
			t.Errorf("no skipped row for %q (skipped: %v)", want, m.Skipped)
			continue
		}
		if !strings.HasPrefix(reason, ReasonSecretClass) {
			t.Errorf("skipped row %q reason = %q; want it to start with %q", want, reason, ReasonSecretClass)
		}
	}
	if got := reasons["config/.pem"]; !strings.Contains(got, "(*.pem)") {
		t.Errorf("skipped row for a bare .pem = %q; want the *.pem family named", got)
	}

	// No byte of any of them reached the run directory, by any route.
	for _, withheld := range []string{"config/.pem", "config/.key", "config/.PFX", "config/server.pem"} {
		copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(withheld))
		if _, serr := os.Stat(copyPath); !os.IsNotExist(serr) {
			t.Errorf("snapshot copy of %q exists (stat err %v); the bytes must never be copied", withheld, serr)
		}
	}
	needles := []string{"BARE-PEM-MATERIAL", "BARE-KEY-MATERIAL", "BARE-PFX-MATERIAL", "LONG-PEM-MATERIAL"}
	if hits := a2TreeContainsAny(t, dest, needles); len(hits) != 0 {
		t.Fatalf("secret bytes leaked into the run directory: %v", hits)
	}

	// Diff honesty and rollback restraint, exactly as for a longer secret name.
	deltas, derr := Diff(root, m)
	if derr != nil {
		t.Fatalf("Diff: %v", derr)
	}
	if len(deltas) != 0 {
		t.Errorf("Diff = %+v; want no deltas (a withheld file is neither covered nor created)", deltas)
	}
	report, rerr := Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("Rollback: %v", rerr)
	}
	if len(report.Archived) != 0 {
		t.Errorf("archived = %v; want none (a withheld file is never moved)", report.Archived)
	}
	for _, mustStay := range []string{"config/.pem", "config/.key", "config/.PFX", "config/server.pem"} {
		if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(mustStay))); serr != nil {
			t.Errorf("%q must stay on disk (nothing is deleted): %v", mustStay, serr)
		}
	}
}

// TestLedgerRefusalFoldsCaseVariantSpellings is R3-7's take-side pin: the run
// directory is refused under a case-variant scope AND under a case-variant --out
// spelling, and the compare form is the folded one on a case-insensitive host
// only.
func TestLedgerRefusalFoldsCaseVariantSpellings(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{
		"reports/notes.md":  "# notes\n",
		"reports/code/keep": "not the ledger\n",
		"src/alpha.go":      "package alpha\n",
	})
	dest := filepath.Join(root, "reports", "code", "ts-1")

	// The rule itself is unchanged on every host.
	if _, err := Take(root, []string{"reports/**"}, dest); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(exact case) error = %v; want ErrLedgerRefused", err)
	}

	if !policyFor(root).foldCase {
		// POSIX fork: "Reports" and "reports" are two directories, so this scope
		// claims neither the ledger nor anything else under the run directory.
		if _, err := Take(root, []string{a2FlipCase("reports") + "/**"}, dest); err != nil {
			t.Fatalf("Take(case-variant scope) on a case-sensitive host error = %v; want acceptance", err)
		}
		if foldOff.pathUnder(a2FlipCase("reports/code/ts-1"), "reports/code/ts-1/manifest.json") {
			t.Error("pathUnder folded two names on a case-sensitive host; folding there excludes a directory the ledger is not in")
		}
		return
	}

	if _, err := Take(root, []string{a2FlipCase("reports") + "/**"}, dest); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(case-variant scope) error = %v; want ErrLedgerRefused (a spelling variant is the same directory on this host)", err)
	}
	a2NoManifest(t, dest)

	if _, err := Take(root, []string{"reports/**"}, a2FlipCase(dest)); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(case-variant --out) error = %v; want ErrLedgerRefused (the --out spelling is not a way around the refusal)", err)
	}
	a2NoManifest(t, dest)

	// The comparison form the walk exclusion uses is the same folded one, in both
	// directions. (The exclusion is defense in depth: a scope that reaches the run
	// directory is refused before the walk, so this arm is pinned at the function
	// the walk calls.)
	if !foldOn.pathUnder(a2FlipCase("reports/code/ts-1"), "reports/code/ts-1/manifest.json") {
		t.Error("pathUnder did not fold the directory side; the walk exclusion would miss the ledger under another --out spelling")
	}
	if !foldOn.pathUnder("reports/code/ts-1", a2FlipCase("reports/code/ts-1/snapshot/src/alpha.go")) {
		t.Error("pathUnder did not fold the visited side; a case-variant walk path would escape the exclusion")
	}

	// Control: a scope beside the ledger is still accepted, and the claims do not
	// reach into it - with the --out spelled in the other case.
	m, err := Take(root, []string{"reports/notes.md", "src/*.go"}, a2FlipCase(dest))
	if err != nil {
		t.Fatalf("Take(beside the ledger, case-variant --out) refused: %v", err)
	}
	for _, row := range m.Files {
		if foldOff.pathUnder("reports/code/ts-1", row.Path) {
			t.Errorf("row %q is inside the run directory", row.Path)
		}
	}
}

// TestRollbackRefusesCaseVariantManifestScope is R3-7's restore-side pin: a
// manifest whose scopes name the run directory in another case is refused before
// the tree is touched.
func TestRollbackRefusesCaseVariantManifestScope(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})
	dest := filepath.Join(root, "reports", "code", "ts-1")

	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	edited := "package alpha\n\n// seat edit\n"
	writeTree(t, root, map[string]string{"src/alpha.go": edited})

	forged := *m
	forged.Scopes = []string{a2FlipCase("reports/**")}
	d1bForgeManifest(t, dest, forged)

	_, rerr := Rollback(root, dest)
	if !policyFor(root).foldCase {
		// POSIX: the flipped scope names a directory that is not the ledger's, so
		// the manifest is accepted and the restore proceeds.
		if rerr != nil {
			t.Fatalf("Rollback(case-variant scope) on a case-sensitive host error = %v; want acceptance", rerr)
		}
		return
	}
	if !errors.Is(rerr, ErrLedgerRefused) {
		t.Fatalf("Rollback(case-variant manifest scope) error = %v; want ErrLedgerRefused", rerr)
	}
	if got := readDisk(t, root, "src/alpha.go"); got != edited {
		t.Errorf("src/alpha.go = %q; want the run's bytes (a refusal must not restore)", got)
	}
}

// TestRollbackKeepsCaseVariantRecordedRows pins what a forged row spelled in
// ANOTHER CASE does now that identity is exact (FIX-E1 revised D1b's choice here).
//
// The ledger's recorded/withheld lookups key on the EXACT root-relative path, so
// such a row does not "cover" the file the walk reports by name. What keeps the
// file safe is FILE identity instead: the archive arm asks os.SameFile before it
// moves anything, so a spelling difference can never make this package move an
// original - the failure D1b measured and fixed with a name fold, now fixed without
// one. The file is left in place and NAMED in skipped[], and Diff reports the claim
// as created (the manifest genuinely does not record that spelling).
func TestRollbackKeepsCaseVariantRecordedRows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	if !policyFor(root).foldCase {
		t.Skip("a case-variant row names a different file on this host; the exact path is the correct identity there")
	}
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})

	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	forged := *m
	forged.Files = []ManifestRow{{
		Path:    a2FlipCase(m.Files[0].Path),
		Size:    m.Files[0].Size,
		SHA256:  m.Files[0].SHA256,
		Mode:    m.Files[0].Mode,
		Existed: true,
	}}
	if forged.Files[0].Path == m.Files[0].Path {
		t.Fatalf("the case flip produced no change (%q); the pin would be vacuous", m.Files[0].Path)
	}
	d1bForgeManifest(t, dest, forged)

	report, rerr := Rollback(root, dest)
	if rerr != nil {
		t.Fatalf("Rollback: %v", rerr)
	}
	if len(report.Archived) != 0 {
		t.Fatalf("archived = %v; want none (the row names this FILE on this host, so it must not be moved)", report.Archived)
	}
	if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(m.Files[0].Path))); serr != nil {
		t.Errorf("%q must still be in place: %v", m.Files[0].Path, serr)
	}
	named := false
	for _, skip := range report.Skipped {
		if skip.Path == m.Files[0].Path && strings.Contains(skip.Reason, "another spelling") {
			named = true
		}
	}
	if !named {
		t.Errorf("rollback.json does not name the retained file (skipped: %v); a restraint is never silent", report.Skipped)
	}
	if len(report.Unchanged) != 1 || report.Unchanged[0] != forged.Files[0].Path {
		t.Errorf("unchanged = %v; want the forged row reported as unchanged", report.Unchanged)
	}

	// Diff keeps the exact-identity answer: the row covers its own spelling, the
	// walk's spelling is a creation. One delta, not two, and the row itself is not
	// reported twice.
	deltas, derr := Diff(root, &forged)
	if derr != nil {
		t.Fatalf("Diff: %v", derr)
	}
	if len(deltas) != 1 || deltas[0].Path != m.Files[0].Path || deltas[0].Status != StatusCreated {
		t.Errorf("Diff = %+v; want one created delta for %q", deltas, m.Files[0].Path)
	}
}

// TestManifestRowPolicyRefusesShapeAndStreamRows is the codesnap half of R3-3
// (plus R3-2): a manifest row carrying the Win32 stream syntax or the 8.3 alias
// shape is refused WHOLE at load, before the tree is touched, and a created file
// whose archive destination carries one of those shapes is left in place.
func TestManifestRowPolicyRefusesShapeAndStreamRows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"a.go":      "package a\n",
		"AGENTS.md": "# operator-owned rules\n",
	})
	m, err := Take(root, []string{"*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	winnerRow := append([]ManifestRow{}, m.Files...)

	forgeRow := func(t *testing.T, rel, body string) {
		t.Helper()
		forged := *m
		forged.Files = append(append([]ManifestRow{}, winnerRow...), ManifestRow{
			Path: rel, Size: int64(len(body)), SHA256: digest(body), Mode: 0o600, Existed: true,
		})
		copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(rel))
		if werr := os.MkdirAll(filepath.Dir(copyPath), 0o755); werr != nil {
			t.Fatalf("mkdir snapshot copy: %v", werr)
		}
		if werr := os.WriteFile(copyPath, []byte(body), 0o600); werr != nil {
			t.Fatalf("plant snapshot copy: %v", werr)
		}
		d1bForgeManifest(t, dest, forged)
	}

	cases := []struct {
		name        string
		rel         string
		windowsOnly bool
	}{
		{"8.3 alias row", "PROVID~1.JSON", true},
		{"8.3 alias in an intermediate component", ".AGY-PR~1/x.md", true},
		{"ordinary stream row", "notes:2026.md", true},
		{"stream row on a governance name", "AGENTS.md:evil", false},
		{"data-stream row on a governance name", "sub/AGENTS.md::$DATA", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.windowsOnly && !windowsHost() {
				t.Skip("the stream and 8.3 arms follow the host: this name is ordinary on a POSIX filesystem")
			}
			body := "SEAT-INJECTED-" + strings.ToUpper(c.name) + "\n"
			forgeRow(t, c.rel, body)

			// The load refuses, and it refuses the MANIFEST, not the row: a
			// governance/credential or corrupt-row sentinel, not a skip list.
			_, lerr := LoadManifest(dest)
			if lerr == nil {
				t.Fatalf("LoadManifest accepted a row named %q", c.rel)
			}
			if !errors.Is(lerr, ErrManifestCorrupt) && !errors.Is(lerr, ErrGovernanceRefused) {
				t.Fatalf("LoadManifest(%q) error = %v; want ErrManifestCorrupt or ErrGovernanceRefused", c.rel, lerr)
			}

			// And the rollback never reaches the tree.
			edited := "package a\n\n// seat edit\n"
			writeTree(t, root, map[string]string{"a.go": edited})
			if _, rerr := Rollback(root, dest); rerr == nil {
				t.Fatalf("Rollback accepted a manifest with row %q", c.rel)
			}
			if got := readDisk(t, root, "a.go"); got != edited {
				t.Errorf("a.go = %q; want the run's bytes (a load refusal must not restore)", got)
			}
			writeTree(t, root, map[string]string{"a.go": "package a\n"})
		})
	}

	// The archive TARGET is gated by the same predicate (rollback.go archiveMove):
	// a file the run created with an 8.3-shaped name is left exactly where it is.
	if !windowsHost() {
		return
	}
	t.Run("archive destination refuses a shape-named created file", func(t *testing.T) {
		fresh := t.TempDir()
		root2 := filepath.Join(fresh, "root")
		dest2 := filepath.Join(fresh, "run")
		writeTree(t, root2, map[string]string{"a.go": "package a\n"})
		if _, terr := Take(root2, []string{"*.go", "*.JSON"}, dest2); terr != nil {
			t.Fatalf("Take: %v", terr)
		}
		writeTree(t, root2, map[string]string{"PROVID~1.JSON": "SEAT-CREATED\n"})
		report, rerr := Rollback(root2, dest2)
		if rerr != nil {
			t.Fatalf("Rollback: %v", rerr)
		}
		if len(report.Archived) != 0 {
			t.Fatalf("archived = %v; want none (an archive destination with the 8.3 shape is refused)", report.Archived)
		}
		if got := readDisk(t, root2, "PROVID~1.JSON"); got != "SEAT-CREATED\n" {
			t.Errorf("PROVID~1.JSON = %q; want the file left exactly where it is", got)
		}
		named := false
		for _, skip := range report.Skipped {
			if skip.Path == "PROVID~1.JSON" {
				named = true
			}
		}
		if !named {
			t.Errorf("rollback.json does not name PROVID~1.JSON (skipped: %v)", report.Skipped)
		}
	})
}
