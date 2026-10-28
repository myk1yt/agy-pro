package codesnap

// FIX-A2 policy pins (findings S3 + S5 plus the preemptive case-folded root row).
//
// What this file locks down:
//
//	A2-S5  secret class   - a claimed .env / key / key-store name is WITHHELD, not
//	                        copied: not one byte of it reaches the run directory,
//	                        the manifest records a skipped row naming it, Diff does
//	                        not report it as created, and Rollback neither restores
//	                        nor archives it.
//	A2-S3a governance     - a scope that claims a governance/credential file or VCS
//	                        metadata FAILS the take with a named error and no
//	                        manifest: the door must not hold a governance baseline,
//	                        and rollback must never restore one.
//	A2-S3b forged rows    - a hand-forged manifest that names a governance/VCS row,
//	                        or a secret-class Files row (which Take cannot produce),
//	                        refuses the WHOLE rollback at load, before the tree is
//	                        touched - even when the planted snapshot copy matches
//	                        its digest, so the policy and not the digest is the gate.
//	A2-S3c ledger         - a scope that could claim the run directory is refused by
//	                        Take and by Rollback, and the walk never claims it.
//	A2-root case fold     - manifest root identity is volume+case-folded on Windows
//	                        (d:/x == D:/X) while a genuinely different root stays
//	                        refused on every host.
//
// Every pin is behavioral: manifest bytes, run-directory contents and tree state.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/agenttools"
)

// a2FlipCase swaps the case of every ASCII letter, producing the other spelling
// of a path. On a case-insensitive host this names the same directory; on a
// case-sensitive host it names a different one, which is exactly the fork the
// root-identity pin measures.
func a2FlipCase(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - ('a' - 'A')
		case c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// a2TreeContainsAny reports the files under dir whose bytes contain any of the
// needles (the "was a secret copied" scan).
func a2TreeContainsAny(t *testing.T, dir string, needles []string) []string {
	t.Helper()
	hits := []string{}
	werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, needle := range needles {
			if strings.Contains(string(data), needle) {
				hits = append(hits, p+" ("+needle+")")
				return nil
			}
		}
		return nil
	})
	if werr != nil && !os.IsNotExist(werr) {
		t.Fatalf("walk %s: %v", dir, werr)
	}
	return hits
}

// a2NoManifest asserts the run directory holds no ledger: a refusal writes
// nothing that a later rollback could read.
func a2NoManifest(t *testing.T, dest string) {
	t.Helper()
	if _, serr := os.Stat(filepath.Join(dest, manifestFileName)); !os.IsNotExist(serr) {
		t.Fatalf("manifest.json exists in %s (stat err %v); a refusal must write nothing", dest, serr)
	}
}

func TestTakeWithholdsSecretClassFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"src/alpha.go":      "package alpha\n",
		"src/env_helper.go": "package alpha\n\n// env helper, not a dotenv\n",
		"src/dev.env":       "SRC_API_KEY=dotenv-in-src\n",
		"config/app.pem":    "-----BEGIN PRIVATE KEY-----\nPEM-KEYMATERIAL\n",
		"keys/server.key":   "KEYMATERIAL-SERVER\n",
		"vault/db.kdbx":     "KDBX-CONTAINER-BYTES\n",
		"id_rsa":            "PRIVATE-KEY-ID-RSA\n",
		".env":              "SECRET-DOTENV\n",
		".env.local":        "SECRET-DOTENV-LOCAL\n",
	})
	scopes := []string{"src/*", "config/*", "keys/*", "vault/*", "id_rsa", ".env", ".env.local"}
	m, err := Take(root, scopes, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	// Only the two non-secret files were captured.
	if got, want := rowPaths(m), []string{"src/alpha.go", "src/env_helper.go"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v", got, want)
	}

	// Every secret-class claim is recorded by name, with the pinned reason.
	wantSkipped := []string{".env", ".env.local", "config/app.pem", "id_rsa", "keys/server.key", "src/dev.env", "vault/db.kdbx"}
	gotSkipped := make(map[string]bool, len(m.Skipped))
	for _, skip := range m.Skipped {
		if !strings.HasPrefix(skip.Reason, ReasonSecretClass) {
			t.Errorf("skipped row %q reason = %q; want it to start with %q", skip.Path, skip.Reason, ReasonSecretClass)
		}
		gotSkipped[skip.Path] = true
	}
	for _, want := range wantSkipped {
		if !gotSkipped[want] {
			t.Errorf("no skipped row for %q (skipped: %v)", want, m.Skipped)
		}
	}
	if len(m.Skipped) != len(wantSkipped) {
		t.Errorf("skipped rows = %v; want exactly %v", m.Skipped, wantSkipped)
	}

	// The bytes were NEVER copied: no snapshot copy, and no file in the run
	// directory carries a secret line (the ledger's own manifest names the paths,
	// never the contents).
	for _, withheld := range wantSkipped {
		copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(withheld))
		if _, serr := os.Stat(copyPath); !os.IsNotExist(serr) {
			t.Errorf("snapshot copy of %q exists (stat err %v); the bytes must never be copied", withheld, serr)
		}
	}
	needles := []string{"SECRET-DOTENV", "PEM-KEYMATERIAL", "KEYMATERIAL-SERVER", "KDBX-CONTAINER-BYTES", "PRIVATE-KEY-ID-RSA", "dotenv-in-src"}
	if hits := a2TreeContainsAny(t, dest, needles); len(hits) != 0 {
		t.Fatalf("secret bytes leaked into the run directory: %v", hits)
	}

	// The manifest on disk carries the skipped rows: the ledger is honest about
	// what it did not cover.
	raw, rerr := os.ReadFile(filepath.Join(dest, manifestFileName))
	if rerr != nil {
		t.Fatalf("read manifest: %v", rerr)
	}
	var decoded Manifest
	if derr := json.Unmarshal(raw, &decoded); derr != nil {
		t.Fatalf("manifest.json does not parse: %v", derr)
	}
	if len(decoded.Skipped) != len(wantSkipped) {
		t.Errorf("manifest skipped = %v; want %d rows", decoded.Skipped, len(wantSkipped))
	}
	if !strings.Contains(string(raw), `"reason": "secret-class`) {
		t.Errorf("manifest.json does not carry the %q reason:\n%s", ReasonSecretClass, raw)
	}

	// Determinism holds with skipped rows in play.
	if _, serr := Take(root, scopes, dest); serr != nil {
		t.Fatalf("second Take: %v", serr)
	}
	again, aerr := os.ReadFile(filepath.Join(dest, manifestFileName))
	if aerr != nil {
		t.Fatalf("re-read manifest: %v", aerr)
	}
	if string(raw) != string(again) {
		t.Errorf("manifest with skipped rows is not byte-deterministic:\n--- first ---\n%s\n--- second ---\n%s", raw, again)
	}

	// Diff honesty: a withheld file was never captured, so it is not "created".
	deltas, derr := Diff(root, m)
	if derr != nil {
		t.Fatalf("Diff: %v", derr)
	}
	if len(deltas) != 0 {
		t.Errorf("Diff = %+v; want no deltas (a withheld file is neither covered nor created)", deltas)
	}

	// Rollback: the secrets stay exactly where they are and are named, never
	// archived - moving one would copy the same bytes into the run directory.
	writeTree(t, root, map[string]string{"src/late.env": "LATE_DOTENV=1\n"})
	report, rerr2 := Rollback(root, dest)
	if rerr2 != nil {
		t.Fatalf("Rollback: %v", rerr2)
	}
	if len(report.Archived) != 0 {
		t.Errorf("archived = %v; want none (a withheld file is never moved)", report.Archived)
	}
	named := make(map[string]bool, len(report.Skipped))
	for _, skip := range report.Skipped {
		named[skip.Path] = true
	}
	for _, mustStay := range []string{".env", "id_rsa", "config/app.pem", "src/late.env"} {
		if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(mustStay))); serr != nil {
			t.Errorf("%q must stay on disk (nothing is deleted): %v", mustStay, serr)
		}
		if !named[mustStay] {
			t.Errorf("rollback.json does not name %q (skipped: %v)", mustStay, report.Skipped)
		}
	}
	if hits := a2TreeContainsAny(t, dest, []string{"SECRET-DOTENV", "LATE_DOTENV"}); len(hits) != 0 {
		t.Fatalf("rollback copied secret bytes into the run directory: %v", hits)
	}
}

func TestTakeRefusesGovernanceAndVCSClaims(t *testing.T) {
	cases := []struct {
		name     string
		tree     map[string]string
		scope    string
		wantComp string
	}{
		{"governance file", map[string]string{"AGENTS.md": "# rules\n"}, "AGENTS.md", "AGENTS.md"},
		{"governance dir", map[string]string{"rules/team.md": "# team\n"}, "rules/*", "rules"},
		{"provider registry", map[string]string{"providers.json": "{}\n"}, "providers.json", "providers.json"},
		{"run state", map[string]string{".agy-pro/state.json": "{}\n"}, ".agy-pro/*", ".agy-pro"},
		{"vcs metadata", map[string]string{".git/config": "[core]\n"}, ".git/config", ".git"},
		{"vcs hooks", map[string]string{".git/hooks/pre-commit": "#!/bin/sh\n"}, ".git/hooks/*", ".git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			dest := filepath.Join(base, "run")
			writeTree(t, root, tc.tree)
			before := treeSnapshot(t, root)

			_, err := Take(root, []string{tc.scope}, dest)
			if err == nil {
				t.Fatalf("Take(%q) accepted a governance/VCS claim; want a fail-closed refusal", tc.scope)
			}
			if !errors.Is(err, ErrGovernanceRefused) {
				t.Fatalf("error = %v; want ErrGovernanceRefused", err)
			}
			if !strings.Contains(err.Error(), tc.wantComp) {
				t.Errorf("refusal does not name the offending component %q: %v", tc.wantComp, err)
			}
			// Fail closed: nothing was written and the tree is untouched.
			a2NoManifest(t, dest)
			if _, serr := os.Stat(filepath.Join(dest, snapshotDirName)); !os.IsNotExist(serr) {
				t.Errorf("a snapshot directory was created despite the refusal (stat err %v)", serr)
			}
			equalTree(t, treeSnapshot(t, root), before)
		})
	}

	// Non-vacuous control: a look-alike name that is NOT governed stays
	// snapshotable (the refusal is component-exact, not a blanket ban).
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{"docs/agents-notes.md": "# notes\n", "docs/providers.json.md": "# about the registry\n"})
	m, err := Take(root, []string{"docs/*"}, filepath.Join(base, "run"))
	if err != nil {
		t.Fatalf("Take(docs/*) refused look-alike names: %v", err)
	}
	if got, want := rowPaths(m), []string{"docs/agents-notes.md", "docs/providers.json.md"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v", got, want)
	}
}

func TestTakeRefusesScopeCoveringRunDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{
		"reports/notes.md": "# notes\n",
		"src/alpha.go":     "package alpha\n",
	})
	// The run directory sits inside the root exactly where `agy pro code` puts it.
	dest := filepath.Join(root, "reports", "code", "ts-1")

	refused := []string{
		"reports/*",
		"reports/**",
		"reports/code/*",
		"reports/code/ts-1/manifest.json",
		"reports",
	}
	for _, scope := range refused {
		t.Run(scope, func(t *testing.T) {
			_, err := Take(root, []string{scope}, dest)
			if err == nil {
				t.Fatalf("Take(%q) accepted a scope that can claim the run directory; want a refusal", scope)
			}
			if !errors.Is(err, ErrLedgerRefused) {
				t.Fatalf("Take(%q) error = %v; want ErrLedgerRefused", scope, err)
			}
			a2NoManifest(t, dest)
		})
	}

	// Non-vacuous control 1: a scope BESIDE the run directory is allowed, and the
	// ledger stays out of the claims.
	m, err := Take(root, []string{"reports/notes.md", "src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take(next to the run directory) refused: %v", err)
	}
	if got, want := rowPaths(m), []string{"reports/notes.md", "src/alpha.go"}; !equalStrings(got, want) {
		t.Fatalf("captured %v; want %v", got, want)
	}
	for _, row := range m.Files {
		if foldOff.pathUnder("reports/code/ts-1", row.Path) {
			t.Errorf("row %q is inside the run directory", row.Path)
		}
	}

	// Non-vacuous control 2: with the run directory OUTSIDE the root the same
	// reports/* scope is fine - the refusal protects the ledger, not the name. The
	// control uses a CLEAN root on purpose: since FIX-F4 (finding Q1) the rule no
	// longer depends on where THIS run writes, so the ledger that control 1 just
	// created under reports/code/ is itself unclaimable and a reports/* scope over it
	// refuses (TestTakeRefusesAScopeOverAPriorRunLedger pins that separately).
	cleanRoot := filepath.Join(base, "clean-root")
	writeTree(t, cleanRoot, map[string]string{"reports/notes.md": "# notes\n"})
	if _, err := Take(cleanRoot, []string{"reports/*"}, filepath.Join(base, "outside-run")); err != nil {
		t.Fatalf("Take(reports/*) with an out-of-root run directory refused: %v", err)
	}
}

func TestRollbackRefusesForgedManifestRows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{
		"a.go":      "package a\n",
		"AGENTS.md": "# operator-owned rules\n",
		".env":      "OPERATOR-DOTENV=1\n",
	})
	m, err := Take(root, []string{"*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	original, rerr := os.ReadFile(filepath.Join(dest, manifestFileName))
	if rerr != nil {
		t.Fatalf("read manifest: %v", rerr)
	}
	before := treeSnapshot(t, root)

	// forge appends one row for rel carrying body, and plants a snapshot copy whose
	// digest MATCHES the row: the digest preflight would pass, so only the row
	// policy stands between the forged row and the tree.
	forge := func(t *testing.T, rel, body string) {
		t.Helper()
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), original, 0o600); werr != nil {
			t.Fatalf("restore original manifest: %v", werr)
		}
		forged := *m
		forged.Files = append(append([]ManifestRow{}, m.Files...), ManifestRow{
			Path: rel, Size: int64(len(body)), SHA256: digest(body), Mode: 0o600, Existed: true,
		})
		copyPath := filepath.Join(m.SnapshotDir, filepath.FromSlash(rel))
		if werr := os.MkdirAll(filepath.Dir(copyPath), 0o755); werr != nil {
			t.Fatalf("mkdir snapshot copy: %v", werr)
		}
		if werr := os.WriteFile(copyPath, []byte(body), 0o600); werr != nil {
			t.Fatalf("plant snapshot copy: %v", werr)
		}
		raw, merr := json.MarshalIndent(forged, "", "  ")
		if merr != nil {
			t.Fatalf("marshal forged manifest: %v", merr)
		}
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), append(raw, '\n'), 0o600); werr != nil {
			t.Fatalf("write forged manifest: %v", werr)
		}
		// Re-sign: the row policy runs after the provenance check (FIX-F4), so the
		// forgery has to carry the directory's own key to reach it.
		resignManifest(t, dest)
	}

	t.Run("governance row", func(t *testing.T) {
		forge(t, "AGENTS.md", "# SEAT-INJECTED RULES\n")
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrGovernanceRefused) {
			t.Fatalf("Rollback(forged governance row) error = %v; want ErrGovernanceRefused", err)
		}
		if got := readDisk(t, root, "AGENTS.md"); got != "# operator-owned rules\n" {
			t.Errorf("AGENTS.md = %q; want the operator bytes untouched", got)
		}
	})

	t.Run("governance row in skipped list", func(t *testing.T) {
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), original, 0o600); werr != nil {
			t.Fatalf("restore original manifest: %v", werr)
		}
		forged := *m
		forged.Skipped = append(append([]SkippedFile{}, m.Skipped...), SkippedFile{Path: "rules/team.md", Reason: "looks harmless"})
		raw, merr := json.MarshalIndent(forged, "", "  ")
		if merr != nil {
			t.Fatalf("marshal: %v", merr)
		}
		if werr := os.WriteFile(filepath.Join(dest, manifestFileName), append(raw, '\n'), 0o600); werr != nil {
			t.Fatalf("write forged manifest: %v", werr)
		}
		resignManifest(t, dest)
		if _, err := Rollback(root, dest); !errors.Is(err, ErrGovernanceRefused) {
			t.Fatalf("Rollback(forged skipped governance row) error = %v; want ErrGovernanceRefused", err)
		}
	})

	t.Run("vcs row", func(t *testing.T) {
		writeTree(t, root, map[string]string{".git/config": "[core]\n\tbare = false\n"})
		forge(t, ".git/config", "[core]\n\thooksPath = ./injected\n")
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrGovernanceRefused) {
			t.Fatalf("Rollback(forged VCS row) error = %v; want ErrGovernanceRefused", err)
		}
		if got := readDisk(t, root, ".git/config"); got == "[core]\n\thooksPath = ./injected\n" {
			t.Error(".git/config was overwritten from the forged row")
		}
	})

	t.Run("secret row", func(t *testing.T) {
		forge(t, ".env", "EXFIL-DOTENV=1\n")
		_, err := Rollback(root, dest)
		if !errors.Is(err, ErrManifestCorrupt) {
			t.Fatalf("Rollback(forged secret row) error = %v; want ErrManifestCorrupt", err)
		}
		if !strings.Contains(err.Error(), ReasonSecretClass) {
			t.Errorf("refusal does not name the %q reason: %v", ReasonSecretClass, err)
		}
		if got := readDisk(t, root, ".env"); got != "OPERATOR-DOTENV=1\n" {
			t.Errorf(".env = %q; want the operator bytes untouched", got)
		}
	})

	t.Run("non-canonical row", func(t *testing.T) {
		forge(t, "a.go.", "package injected\n")
		if _, err := Rollback(root, dest); !errors.Is(err, ErrManifestCorrupt) {
			t.Fatalf("Rollback(non-canonical row) error = %v; want ErrManifestCorrupt", err)
		}
	})

	// Whatever the forged attempt, the original tree bytes are intact.
	if werr := os.WriteFile(filepath.Join(dest, manifestFileName), original, 0o600); werr != nil {
		t.Fatalf("restore original manifest: %v", werr)
	}
	after := treeSnapshot(t, root)
	if after["a.go"] != before["a.go"] || after["AGENTS.md"] != before["AGENTS.md"] || after[".env"] != before[".env"] {
		t.Errorf("tree changed during the forged-manifest pins: before %v after %v", sortedKeys(before), sortedKeys(after))
	}
}

func TestRollbackRefusesManifestScopeCoveringRunDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeTree(t, root, map[string]string{"src/alpha.go": "package alpha\n"})
	dest := filepath.Join(root, "reports", "code", "ts-1")

	// A legitimate take first: the scope does not reach the run directory.
	m, err := Take(root, []string{"src/*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	// The run then edits the recorded file, so a restore would be visible.
	edited := "package alpha\n\n// seat edit\n"
	writeTree(t, root, map[string]string{"src/alpha.go": edited})

	// Then the forgery: scopes rewritten so the ledger is inside the scope. Only
	// the manifest is edited - the bytes on disk are the ones Take wrote.
	forged := *m
	forged.Scopes = []string{"reports/**"}
	raw, merr := json.MarshalIndent(forged, "", "  ")
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if werr := os.WriteFile(filepath.Join(dest, manifestFileName), append(raw, '\n'), 0o600); werr != nil {
		t.Fatalf("write forged manifest: %v", werr)
	}
	// Re-sign the forged scope set so the ledger-claim arm is what refuses it, not
	// the provenance arm (FIX-F4: the forgery is a shape a compromised key enables).
	resignManifest(t, dest)

	if _, err := Rollback(root, dest); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Rollback(manifest scope over the run directory) error = %v; want ErrLedgerRefused", err)
	}
	// Fail closed: the refusal lands before the first write, so the edit stays.
	if got := readDisk(t, root, "src/alpha.go"); got != edited {
		t.Errorf("src/alpha.go = %q; want the run's bytes (a refusal must not restore)", got)
	}
}

func TestSameRootComparisons(t *testing.T) {
	// The two spellings one Windows directory has. On a case-sensitive host the
	// same two strings name two directories, and the answer must be false: folding
	// there would accept a manifest for the wrong tree.
	if got, want := sameRoot(`d:/x/y`, `D:/X/Y`), caseFoldedRoots; got != want {
		t.Errorf("sameRoot(d:/x/y, D:/X/Y) = %v; want %v (caseFoldedRoots=%v)", got, want, caseFoldedRoots)
	}
	// A different volume is never the same root.
	if sameRoot(`c:/x/y`, `d:/x/y`) {
		t.Error("sameRoot accepted two different volumes")
	}
	// A genuinely different path stays different on every host.
	if sameRoot(`d:/x/y`, `d:/x/z`) {
		t.Error("sameRoot accepted two different directories")
	}
	// Dot segments and a trailing separator are cleaned before the compare.
	if !sameRoot(`d:/x/./y`, `d:/x/y`) {
		t.Error("sameRoot refused the same directory spelled with a dot segment")
	}
	if !sameRoot(`d:/x/y/`, `d:/x/y`) {
		t.Error("sameRoot refused the same directory spelled with a trailing separator")
	}
}

func TestRootIdentityAcceptsTheOtherCaseSpelling(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	dest := filepath.Join(base, "run")
	writeTree(t, root, map[string]string{"a.go": "package a\n"})
	m, err := Take(root, []string{"*.go"}, dest)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	resolved := resolvedRoot(t, root)

	// (1) The manifest root spelled in the other case: the same directory on a
	// case-insensitive host, a different one elsewhere. This arm does not depend on
	// how the host canonicalizes paths, so it measures the comparison itself.
	mixed := *m
	mixed.Root = a2FlipCase(m.Root)
	if mixed.Root == m.Root {
		t.Fatalf("the case flip produced no change (%q); the arm would be vacuous", m.Root)
	}
	_, derr := Diff(resolved, &mixed)
	_, rerr := Rollback(resolved, dest)
	if caseFoldedRoots {
		if derr != nil {
			t.Fatalf("Diff(mixed-case manifest root %q) error = %v; want acceptance", mixed.Root, derr)
		}
		if rerr != nil {
			t.Fatalf("Rollback(mixed-case manifest root %q) error = %v; want acceptance", mixed.Root, rerr)
		}
	} else {
		if !errors.Is(derr, ErrRootMismatch) {
			t.Fatalf("Diff(mixed-case manifest root) error = %v; want ErrRootMismatch on a case-sensitive host", derr)
		}
		if !errors.Is(rerr, ErrRootMismatch) {
			t.Fatalf("Rollback(mixed-case manifest root) error = %v; want ErrRootMismatch on a case-sensitive host", rerr)
		}
	}

	// (2) The TARGET root spelled in the other case, live: modify a file and
	// roll it back through the other spelling of the same directory.
	writeTree(t, root, map[string]string{"a.go": "package a\n\n// seat edit\n"})
	report, rerr2 := Rollback(a2FlipCase(resolved), dest)
	if caseFoldedRoots {
		if rerr2 != nil {
			t.Fatalf("Rollback(mixed-case target root %q) error = %v; want acceptance", a2FlipCase(resolved), rerr2)
		}
		if got, want := report.Restored, []string{"a.go"}; !equalStrings(got, want) {
			t.Errorf("restored = %v; want %v", got, want)
		}
		if got := readDisk(t, root, "a.go"); got != "package a\n" {
			t.Errorf("a.go = %q; want the recorded bytes", got)
		}
	} else if !errors.Is(rerr2, ErrRootMismatch) {
		t.Fatalf("Rollback(mixed-case target root) error = %v; want ErrRootMismatch on a case-sensitive host", rerr2)
	}

	// (3) A genuinely different root is still refused by both entry points.
	other := filepath.Join(base, "other")
	writeTree(t, other, map[string]string{"a.go": "package other\n"})
	if _, err := Diff(other, m); !errors.Is(err, ErrRootMismatch) {
		t.Fatalf("Diff(other root) error = %v; want ErrRootMismatch", err)
	}
	if _, err := Rollback(other, dest); !errors.Is(err, ErrRootMismatch) {
		t.Fatalf("Rollback(other root) error = %v; want ErrRootMismatch", err)
	}
}

// TestLedgerRefusalSurvivesALinkedRoot pins the path-space rule the ledger check
// depends on: the root is resolved by canonicalRoot (symlinks and junctions
// followed), so a <destDir> spelled through the root's own link must be rendered
// in the same space before the containment test - otherwise filepath.Rel fails,
// the refusal silently switches off, and the run directory is exactly the one
// subtree a scope must never reach.
func TestLedgerRefusalSurvivesALinkedRoot(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	writeTree(t, realRoot, map[string]string{"reports/notes.md": "# notes\n"})
	if err := os.MkdirAll(filepath.Join(realRoot, "reports", "code"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linkedRoot := filepath.Join(base, "linked-root")
	makeSymlink(t, realRoot, linkedRoot)

	dest := filepath.Join(linkedRoot, "reports", "code", "ts-1")
	if _, err := Take(linkedRoot, []string{"reports/*"}, dest); !errors.Is(err, ErrLedgerRefused) {
		t.Fatalf("Take(scope reports/* through a linked root) error = %v; want ErrLedgerRefused", err)
	}
	// Non-vacuous control: a scope beside the ledger is still accepted through the
	// same link, so the refusal is the ledger and not a blanket refusal of links.
	if _, err := Take(linkedRoot, []string{"reports/notes.md"}, dest); err != nil {
		t.Fatalf("Take(reports/notes.md) through the linked root refused: %v", err)
	}
}

// TestPolicyArmsAreLoadBearing pins the counterfactual every new refusal depends
// on: the secret path, the governance path and the ledger path are each IN SCOPE
// (agenttools.PathInScope, the same predicate the walk claims through) and each
// is a readable regular file on disk. Without the arm under test, each of them is
// an ordinary claim that reaches the copy loop - which is exactly what the
// pre-fix package did with every claim it made.
func TestPolicyArmsAreLoadBearing(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	ledgerPath := "reports/code/ts-1/snapshot/x.go"
	writeTree(t, root, map[string]string{
		".env":         "SECRET-DOTENV\n",
		"AGENTS.md":    "# rules\n",
		ledgerPath:     "package x\n",
		"src/alpha.go": "package alpha\n",
	})
	cases := []struct {
		name   string
		rel    string
		scopes []string
	}{
		{"secret arm", ".env", []string{".env"}},
		{"governance arm", "AGENTS.md", []string{"AGENTS.md"}},
		{"ledger arm", ledgerPath, []string{"reports/*"}},
	}
	for _, tc := range cases {
		if !agenttools.PathInScope(tc.rel, tc.scopes) {
			t.Errorf("%s: %q is NOT in scope %v, so the pin would not be measuring the arm", tc.name, tc.rel, tc.scopes)
		}
		info, serr := os.Stat(filepath.Join(root, filepath.FromSlash(tc.rel)))
		if serr != nil || !info.Mode().IsRegular() {
			t.Errorf("%s: %q is not a readable regular file (%v), so the pin would not be measuring the arm", tc.name, tc.rel, serr)
		}
	}
}
