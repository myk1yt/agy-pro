//go:build windows

package agenttools

// FIX-A1 (S2) pins, Windows half: the 8.3 short-name vector.
//
// Two arms cover the vector, and they are pinned separately because they fail
// differently by volume:
//
//   - the exact arm, win32ExpandWriteTarget (+ canonicalWriteRefusal's expansion
//     branch), driven here through the win32ExpandWriteTargetFn hook that
//     production code never assigns, so the branch is exercised even on a volume
//     that mints no aliases;
//   - the approximate shape arm, win32ShortAliasShape, which refuses the
//     NAME~N.EXT shape outright and is testable on any volume.
//
// A third pin proves the expansion really talks to kernel32 rather than being a
// stub, and a fourth runs the LIVE vector: a real alias for a real governed file
// on a volume that generates one (this host's temp volume does; `fsutil
// 8dot3name query D:` needs elevation, so the volume state is read from the
// OS's own answer instead).

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

var getShortPathNameWProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")

// shortPathName is kernel32!GetShortPathNameW, used only by these pins to ask the
// volume for a real 8.3 alias. It returns the input unchanged when the volume
// generates no short names.
func shortPathName(p string) (string, bool) {
	p16, err := syscall.UTF16FromString(p)
	if err != nil || len(p16) == 0 {
		return "", false
	}
	n, _, _ := getShortPathNameWProc.Call(uintptr(unsafe.Pointer(&p16[0])), 0, 0)
	if n == 0 {
		return "", false
	}
	buf := make([]uint16, n+1)
	n, _, _ = getShortPathNameWProc.Call(uintptr(unsafe.Pointer(&p16[0])), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || int(n) > len(buf) {
		return "", false
	}
	return syscall.UTF16ToString(buf[:n]), true
}

// TestWin32ExpansionAddressesKernel32 proves the platform hook is wired to the
// real API: given a real 8.3 alias, the expansion must return the long name,
// which only GetLongPathNameW can supply. It also pins the not-yet-existing
// final component path (expansion through the parent directory).
func TestWin32ExpansionAddressesKernel32(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "ProvidersRegistryBackup")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	alias, ok := shortPathName(longDir)
	if !ok || !strings.Contains(alias, "~") {
		t.Skipf("volume generates no 8.3 aliases (GetShortPathNameW(%q) = %q, ok=%v); the synthetic expansion pin below covers the arm", longDir, alias, ok)
	}
	t.Logf("volume alias: %q -> %q", alias, longDir)

	got, ok := win32ExpandWriteTarget(alias)
	if !ok {
		t.Fatalf("win32ExpandWriteTarget(%q) reported no expansion for a real alias", alias)
	}
	if !strings.EqualFold(got, longDir) {
		t.Fatalf("win32ExpandWriteTarget(%q) = %q; want the long name %q", alias, got, longDir)
	}

	// A not-yet-existing final component expands through its parent directory, so
	// the write case (write_file creating a new file) sees the long chain too.
	newFile := filepath.Join(alias, "new-file.json")
	got, ok = win32ExpandWriteTarget(newFile)
	if !ok {
		t.Fatalf("win32ExpandWriteTarget(%q) reported no expansion for an existing parent", newFile)
	}
	want := filepath.Join(longDir, "new-file.json")
	if !strings.EqualFold(got, want) {
		t.Fatalf("win32ExpandWriteTarget(%q) = %q; want %q", newFile, got, want)
	}
}

// TestWin32ShortAliasShape pins the approximate arm's shape rule.
func TestWin32ShortAliasShape(t *testing.T) {
	for _, comp := range []string{"PROVID~1.JSON", "provid~1", "PROVID~12.JSON", "a~1", "file~1.tar.gz", "a~12345"} {
		if !win32ShortAliasShape(comp) {
			t.Errorf("win32ShortAliasShape(%q) = false; want true", comp)
		}
	}
	for _, comp := range []string{"providers.json", "provid~.JSON", "~1.JSON", "a~b.JSON", "notes.md", "report~20261024", "a~123456"} {
		if win32ShortAliasShape(comp) {
			t.Errorf("win32ShortAliasShape(%q) = true; want false", comp)
		}
	}

	// The arm is write-gated: the shape is refused by BOTH write tools, and no
	// file appears under either name.
	root := t.TempDir()
	for _, rel := range []string{"PROVID~1.JSON", "sub/PROVID~1.JSON", "providers~2.json", "NEWDIR~1/sub/x.md", "foo~1/sub/x.md"} {
		_, err := WriteFile(root, rel, "EVIL", true)
		if err == nil {
			t.Fatalf("WriteFile(%q): expected the 8.3 shape refusal, got nil", rel)
		}
		if !strings.Contains(err.Error(), "8.3") {
			t.Fatalf("WriteFile(%q): refusal does not name the 8.3 shape: %v", rel, err)
		}
		assertNoWriteLanded(t, root, rel)
	}
}

// TestWin32IntermediateAliasRefused pins R3-3 on a volume that really mints a
// short name: an alias in a NON-FINAL component, with directories behind it that
// do not exist yet, is refused, and nothing is created through the alias.
//
// BEFORE the fix this probe WROTE: "PROVID~1/newdir/sub/x.md" made MkdirAll
// create newdir and sub through the alias, so x.md landed inside
// ProvidersRegistryBackup (live, volume C:). The shape arm tested the leaf only
// and the expansion arm looked one directory up, at a parent that did not exist.
func TestWin32IntermediateAliasRefused(t *testing.T) {
	root := t.TempDir()
	longDir := filepath.Join(root, "ProvidersRegistryBackup")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasPath, ok := shortPathName(longDir)
	if !ok || !strings.Contains(aliasPath, "~") {
		t.Skipf("volume generates no 8.3 alias for %q (GetShortPathNameW = %q): the live intermediate-alias case cannot be created here; the shape table above and the synthetic expansion pin cover the arm", filepath.Base(longDir), aliasPath)
	}
	alias := filepath.Base(aliasPath)
	t.Logf("volume minted %q for %q", alias, filepath.Base(longDir))

	for _, rel := range []string{
		alias + "/newdir/sub/x.md",
		alias + "/newdir2/deeper/x.md",
	} {
		_, err := WriteFile(root, rel, "EVIL", true)
		if err == nil {
			t.Fatalf("write_file(%q) landed through an intermediate 8.3 alias", rel)
		}
		if !strings.Contains(err.Error(), "8.3") {
			t.Fatalf("write_file(%q): refusal does not name the 8.3 shape: %v", rel, err)
		}
	}
	entries, err := os.ReadDir(longDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a refused alias write created %v inside %q", len(entries), filepath.Base(longDir))
	}

	// Non-vacuity: the LONG name of the same directory, with the same
	// not-yet-existing tail, is an ordinary write and must still land.
	if _, err := WriteFile(root, "ProvidersRegistryBackup/newdir/sub/x.md", "ok", true); err != nil {
		t.Fatalf("the long-name control write was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(longDir, "newdir", "sub", "x.md")); err != nil {
		t.Fatalf("the long-name control write did not land: %v", err)
	}
}

// TestWin32ExpansionWalksLongestExistingAncestor pins the widened expansion arm:
// the walk climbs past the immediate parent to the nearest ancestor the OS can
// resolve, so a deep not-yet-existing tail is expanded too.
//
// Measured nuance this pin records: GetLongPathNameW expands 8.3 COMPONENTS but
// does not re-case a path it was handed, so a caller-written spelling comes back
// as written when only casing differs. The guard's expansion arm therefore
// resolves aliases, and the case-class it compares is the FOLD's job
// (win32NameFold), not the API's.
func TestWin32ExpansionWalksLongestExistingAncestor(t *testing.T) {
	root := t.TempDir()

	// Deep tail, nothing but the root exists.
	target := filepath.Join(root, "alpha", "beta", "gamma", "x.md")
	got, ok := win32ExpandWriteTarget(target)
	if !ok {
		t.Fatalf("win32ExpandWriteTarget(%q) reported no expansion; the walk must reach the workspace root", target)
	}
	if !strings.EqualFold(got, target) {
		t.Fatalf("win32ExpandWriteTarget(%q) = %q; want the same path", target, got)
	}

	// A caller-written spelling of an existing ancestor is preserved, not
	// re-cased: the API expands aliases, it does not normalize case.
	mixed := filepath.Join(root, "MixedCaseAncestor")
	if err := os.MkdirAll(mixed, 0o755); err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(root, "mixedcaseancestor", "newdir", "deeper", "x.md")
	got, ok = win32ExpandWriteTarget(written)
	if !ok {
		t.Fatalf("win32ExpandWriteTarget(%q) reported no expansion for an existing ancestor", written)
	}
	if !strings.EqualFold(got, written) {
		t.Fatalf("win32ExpandWriteTarget(%q) = %q; want the same path modulo case", written, got)
	}

	// A real alias ancestor: the walk expands the alias even when the tail
	// creates two new directory levels. This is the arm the pre-fix code could not
	// reach (it only asked the immediate parent, which did not exist).
	longDir := filepath.Join(root, "ProvidersRegistryBackup")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasPath, aliasOK := shortPathName(longDir)
	if !aliasOK || !strings.Contains(aliasPath, "~") {
		t.Skipf("volume generates no 8.3 alias for %q; the synthetic pin covers the expansion branch", filepath.Base(longDir))
	}
	viaAlias := filepath.Join(aliasPath, "newdir", "sub", "x.md")
	got, ok = win32ExpandWriteTarget(viaAlias)
	if !ok {
		t.Fatalf("win32ExpandWriteTarget(%q) reported no expansion: the walk must expand the alias ancestor", viaAlias)
	}
	want := filepath.Join(longDir, "newdir", "sub", "x.md")
	if !strings.EqualFold(got, want) {
		t.Fatalf("win32ExpandWriteTarget(%q) = %q; want %q", viaAlias, got, want)
	}
}

// TestWin32ExpansionRefusesAliasToGovernance pins the EXACT arm: an expansion
// that lands on a governance or VCS-protected component is refused, and an
// expansion that escapes the root is refused. The expansion is synthetic (see the
// file comment): the hook returns what GetLongPathNameW would return on a volume
// that mints the alias.
func TestWin32ExpansionRefusesAliasToGovernance(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name     string
		rel      string // what the seat writes (a canonical, alias-free name)
		expanded func(root string) string
		wantErr  string
	}{
		{
			name:     "alias expands to a governance file",
			rel:      "scratch/reg.json",
			expanded: func(root string) string { return filepath.Join(root, "providers.json") },
			wantErr:  "governance/credential file",
		},
		{
			name:     "alias expands to a denylisted directory",
			rel:      "scratch/entry.json",
			expanded: func(root string) string { return filepath.Join(root, ".agy-pro", "entry.json") },
			wantErr:  "governance/credential file",
		},
		{
			name:     "alias expands into VCS metadata",
			rel:      "scratch/hook",
			expanded: func(root string) string { return filepath.Join(root, ".git", "hooks", "hook") },
			wantErr:  "protected VCS metadata",
		},
		{
			name:     "alias expands outside the root",
			rel:      "scratch/sibling.txt",
			expanded: func(root string) string { return filepath.Join(root, "..", "sibling.txt") },
			wantErr:  "outside the workspace root",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := win32ExpandWriteTargetFn
			win32ExpandWriteTargetFn = func(string) (string, bool) { return tc.expanded(root), true }
			t.Cleanup(func() { win32ExpandWriteTargetFn = original })

			_, err := WriteFile(root, tc.rel, "EVIL", true)
			if err == nil {
				t.Fatalf("WriteFile(%q): expected an 8.3-expansion refusal, got nil", tc.rel)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("WriteFile(%q): error %q does not contain %q", tc.rel, err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), "8.3") {
				t.Fatalf("WriteFile(%q): refusal does not name the 8.3 resolution: %v", tc.rel, err)
			}
		})
	}

	// Non-vacuity: an expansion that names the same path refuses nothing.
	original := win32ExpandWriteTargetFn
	t.Cleanup(func() { win32ExpandWriteTargetFn = original })
	benign := filepath.Join(root, "scratch", "plain.json")
	win32ExpandWriteTargetFn = func(string) (string, bool) { return benign, true }
	if _, err := WriteFile(root, "scratch/plain.json", "ok", true); err != nil {
		t.Fatalf("an expansion that lands on the written path must stay writable: %v", err)
	}
}

// TestWin32RealShortAliasRefused runs the LIVE vector: a real 8.3 alias of a real
// governed file on a volume that mints one. The write must be refused and the
// governed file's bytes must be untouched.
//
// "integrations.json" is chosen because it is LONGER than 8.3, so the volume has
// to mint an alias for it; a short governed name ("rules", ".agy-pro") never gets
// one, which is why the exact expansion arm can only be reached live through a
// long governed name.
func TestWin32RealShortAliasRefused(t *testing.T) {
	root := t.TempDir()

	// Plant the governed file directly (the guard would refuse the write itself,
	// which is the whole point of the file being here).
	governed := filepath.Join(root, "integrations.json")
	const originalBody = "{\"operator\":\"owned\"}\n"
	if err := os.WriteFile(governed, []byte(originalBody), 0o600); err != nil {
		t.Fatal(err)
	}

	aliasPath, ok := shortPathName(governed)
	if !ok || strings.EqualFold(aliasPath, governed) || !strings.Contains(aliasPath, "~") {
		t.Skipf("volume generates no 8.3 alias for %q (GetShortPathNameW = %q, ok=%v): the live alias case cannot be created here; the synthetic expansion pin above covers the arm", filepath.Base(governed), filepath.Base(aliasPath), ok)
	}
	alias := filepath.Base(aliasPath)
	t.Logf("volume minted the 8.3 alias %q for %q: probing write_file(%q)", alias, filepath.Base(governed), alias)

	_, err := WriteFile(root, alias, "EVIL", true)
	if err == nil {
		t.Fatalf("write_file(%q) landed through the 8.3 alias of a governed file", alias)
	}
	t.Logf("refusal: %v", err)

	body, readErr := os.ReadFile(governed)
	if readErr != nil {
		t.Fatalf("the governed file is gone after a refused alias write: %v", readErr)
	}
	if string(body) != originalBody {
		t.Fatalf("a refused 8.3-alias write modified the governed file: %q", string(body))
	}

	// The same alias through replace_in_file must be refused too.
	if _, err := ReplaceInFile(root, alias, "operator", "seat", false); err == nil {
		t.Fatalf("replace_in_file(%q) went through the 8.3 alias of a governed file", alias)
	}
	body, _ = os.ReadFile(governed)
	if string(body) != originalBody {
		t.Fatalf("a refused 8.3-alias replace modified the governed file: %q", string(body))
	}
}
