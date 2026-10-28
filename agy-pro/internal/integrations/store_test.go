package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"agypro/internal/paths"
)

// capturedWrite records what the store handed to the atomic writer.
type capturedWrite struct {
	path  string
	data  []byte
	perm  os.FileMode
	calls int
}

// captureWrites swaps the atomic-writer seam so a test can assert the requested
// mode and path. Windows does not round-trip mode bits through Stat, so the
// seam is the only way to prove the 0600 request on every platform.
func captureWrites(t *testing.T) *capturedWrite {
	t.Helper()
	captured := &capturedWrite{}
	previous := atomicWriteFile
	atomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		captured.calls++
		captured.path = path
		captured.data = append([]byte(nil), data...)
		captured.perm = perm
		return previous(path, data, perm)
	}
	t.Cleanup(func() { atomicWriteFile = previous })
	return captured
}

// existsFile reports whether a path is present on disk.
func existsFile(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirListing returns the sorted names in a directory.
func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestManifestPathUsesAgyProHome pins the store location: the manifest is a
// sibling of state.json inside the relocated agy-pro home, never a
// config-dir file.
func TestManifestPathUsesAgyProHome(t *testing.T) {
	manifestPath := sandboxStore(t)
	if filepath.Base(manifestPath) != ManifestFileName {
		t.Fatalf("manifest base name = %q, want %q", filepath.Base(manifestPath), ManifestFileName)
	}
	if filepath.Base(filepath.Dir(manifestPath)) == ".gemini" {
		t.Fatalf("manifest resolved into the gemini config tree: %q", manifestPath)
	}
}

// TestManifestMissingFileLoadsEmpty pins the first-run state: no file reads as
// an empty manifest (not an error), and the read creates nothing.
func TestManifestMissingFileLoadsEmpty(t *testing.T) {
	manifestPath := sandboxStore(t)

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest on a missing file: %v", err)
	}
	if manifest.Len() != 0 {
		t.Fatalf("missing file loaded %d entries, want 0", manifest.Len())
	}
	if manifest.SchemaVersion != CurrentManifestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", manifest.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if existsFile(manifestPath) {
		t.Fatalf("LoadManifest created %q; a read must not write", manifestPath)
	}
}

// TestManifestRoundTrip saves two entries and loads them back field for field,
// and pins the on-disk document shape.
func TestManifestRoundTrip(t *testing.T) {
	manifestPath := sandboxStore(t)
	root := t.TempDir()
	rulesFile := filepath.Join(root, "rules", "AGENTS.md")
	skillsFile := filepath.Join(root, "skills", "usage", "SKILL.md")

	manifest := NewManifest()
	manifest.Stamp(sampleTime)
	mustSet(t, manifest, newEntry(rulesFile, ProvenanceGoApplied))
	mustSet(t, manifest, newEntry(skillsFile, ProvenanceAdopted))
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.SchemaVersion != CurrentManifestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", loaded.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if !loaded.UpdatedAt.Equal(sampleTime) {
		t.Fatalf("updated_at = %s, want %s", loaded.UpdatedAt, sampleTime)
	}
	if !reflect.DeepEqual(loaded.Entries, manifest.Entries) {
		t.Fatalf("entries did not round trip:\n got %#v\nwant %#v", loaded.Entries, manifest.Entries)
	}

	entry, ok := loaded.Entry(rulesFile)
	if !ok {
		t.Fatalf("entry %q missing after load", rulesFile)
	}
	if entry.SHA256 != sampleSHA || entry.Provenance != ProvenanceGoApplied || entry.BackupRef != "backups/20261022_030405" {
		t.Fatalf("entry fields did not round trip: %#v", entry)
	}

	doc := decodeRawJSON(t, readFileRaw(t, manifestPath))
	for _, key := range []string{"schema_version", "updated_at", "entries"} {
		if _, present := doc[key]; !present {
			t.Fatalf("on-disk document is missing key %q: %v", key, doc)
		}
	}
	if len(doc) != 3 {
		t.Fatalf("on-disk document has %d top-level keys, want 3: %v", len(doc), doc)
	}
}

// TestManifestWriteIsAtomicAnd0600 asserts every save goes through the atomic
// writer with the 0600 request, ends with a newline, is valid JSON, and leaves
// no temporary file behind.
func TestManifestWriteIsAtomicAnd0600(t *testing.T) {
	manifestPath := sandboxStore(t)
	captured := captureWrites(t)

	manifest := NewManifest()
	manifest.Stamp(sampleTime)
	mustSet(t, manifest, newEntry(filepath.Join(t.TempDir(), "rules", "AGENTS.md"), ProvenanceGoApplied))
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	if captured.calls != 1 {
		t.Fatalf("atomic writer calls = %d, want 1", captured.calls)
	}
	if captured.path != manifestPath {
		t.Fatalf("atomic writer target = %q, want %q", captured.path, manifestPath)
	}
	if captured.perm != manifestPerm {
		t.Fatalf("atomic writer perm = %#o, want %#o", captured.perm, manifestPerm)
	}
	if !bytes.HasSuffix(captured.data, []byte("\n")) {
		t.Fatalf("written manifest does not end with a newline: %q", string(captured.data))
	}
	if !json.Valid(captured.data) {
		t.Fatalf("written manifest is not valid JSON:\n%s", string(captured.data))
	}
	if !bytes.Contains(captured.data, []byte("\n  \"")) {
		t.Fatalf("written manifest is not indented:\n%s", string(captured.data))
	}
	for _, name := range dirListing(t, filepath.Dir(manifestPath)) {
		if strings.HasPrefix(name, ".tmp-") {
			t.Fatalf("temporary file left behind: %q", name)
		}
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(manifestPath)
		if err != nil {
			t.Fatalf("stat manifest: %v", err)
		}
		if got := info.Mode().Perm(); got != manifestPerm {
			t.Fatalf("manifest mode = %#o, want %#o", got, manifestPerm)
		}
	}
}

// TestManifestSaveStampsZeroUpdatedAt pins the friendly default: a caller that
// forgot to stamp still produces a fresh timestamp, and an explicit timestamp
// is never overwritten.
func TestManifestSaveStampsZeroUpdatedAt(t *testing.T) {
	sandboxStore(t)

	before := time.Now().UTC()
	manifest := NewManifest()
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if manifest.UpdatedAt.IsZero() {
		t.Fatal("SaveManifest left updated_at zero")
	}
	if manifest.UpdatedAt.Before(before.Add(-time.Second)) || manifest.UpdatedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("stamped updated_at = %s, want a value near %s", manifest.UpdatedAt, before)
	}

	stamped := NewManifest()
	stamped.Stamp(sampleTime)
	if err := SaveManifest(stamped); err != nil {
		t.Fatalf("SaveManifest(stamped): %v", err)
	}
	if !stamped.UpdatedAt.Equal(sampleTime) {
		t.Fatalf("explicit updated_at = %s, want it preserved as %s", stamped.UpdatedAt, sampleTime)
	}
}

// TestManifestForwardCompatPreservesUnknownKeys is the additive-key contract: a
// key this build does not model survives a load-and-save verbatim, both at the
// document root and inside an entry. Without it an older build's re-save would
// silently destroy a newer build's bookkeeping.
func TestManifestForwardCompatPreservesUnknownKeys(t *testing.T) {
	manifestPath := sandboxStore(t)
	destination := filepath.Join(t.TempDir(), "skills", "usage", "SKILL.md")

	seed := map[string]any{
		"schema_version": 1,
		"updated_at":     "2026-10-22T00:00:00Z",
		"future_top":     map[string]any{"nested": []any{1, 2, 3}, "flag": true},
		"entries": map[string]any{
			destination: map[string]any{
				"path":         destination,
				"sha256":       sampleSHA,
				"provenance":   "go-applied",
				"backup_ref":   "backups/20261022_000000",
				"future_entry": "keep-me",
			},
		},
	}
	seedBytes, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	writeFileRaw(t, manifestPath, append(seedBytes, '\n'))

	manifest, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	entry, ok := manifest.Entry(destination)
	if !ok {
		t.Fatalf("entry %q was not loaded", destination)
	}
	if entry.BackupRef != "backups/20261022_000000" {
		t.Fatalf("known field did not load: %#v", entry)
	}
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest after load: %v", err)
	}

	reloaded := decodeRawJSON(t, readFileRaw(t, manifestPath))
	seedDoc := decodeRawJSON(t, seedBytes)
	futureTop, present := reloaded["future_top"]
	if !present {
		t.Fatalf("unknown top-level key was dropped on re-save: %v", reloaded)
	}
	if got, want := decodeAny(t, futureTop), decodeAny(t, seedDoc["future_top"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("unknown top-level value changed: got %v want %v", got, want)
	}

	var entriesRaw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(reloaded["entries"], &entriesRaw); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	entryRaw, present := entriesRaw[destination]
	if !present {
		t.Fatalf("entry disappeared on re-save: %v", entriesRaw)
	}
	futureEntry, present := entryRaw["future_entry"]
	if !present {
		t.Fatalf("unknown entry key was dropped on re-save: %v", entryRaw)
	}
	if string(futureEntry) != `"keep-me"` {
		t.Fatalf("unknown entry value = %s, want %q", string(futureEntry), "keep-me")
	}
	if string(entryRaw["provenance"]) != `"go-applied"` {
		t.Fatalf("known entry field = %s, want %q", string(entryRaw["provenance"]), "go-applied")
	}
}

// TestManifestCorruptFailsClosed is the no-silent-reset rule: a file that
// exists but cannot be read as a manifest is a typed error naming the path, and
// the bytes on disk survive the failed read untouched.
func TestManifestCorruptFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "empty file", content: ""},
		{name: "whitespace only", content: "  \n\t\n"},
		{name: "not json", content: "this is not a manifest\n"},
		{name: "truncated document", content: `{"schema_version":1,"entries":{`},
		{name: "entries wrong type", content: `{"schema_version":1,"entries":[]}`},
		{name: "schema wrong type", content: `{"schema_version":"one"}`},
		{name: "top-level array", content: `[]`},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manifestPath := sandboxStore(t)
			writeFileRaw(t, manifestPath, []byte(testCase.content))
			before := readFileRaw(t, manifestPath)
			beforeListing := dirListing(t, filepath.Dir(manifestPath))

			manifest, err := LoadManifest()
			if err == nil {
				t.Fatalf("LoadManifest accepted a corrupt file and returned %#v", manifest)
			}
			if manifest != nil {
				t.Fatalf("LoadManifest returned a non-nil manifest with the error: %#v", manifest)
			}
			if !errors.Is(err, ErrManifestCorrupt) {
				t.Fatalf("error = %v, want it to wrap ErrManifestCorrupt", err)
			}
			assertContains(t, "error message", err.Error(), strconv.Quote(manifestPath))

			if after := readFileRaw(t, manifestPath); !bytes.Equal(before, after) {
				t.Fatalf("the failed read rewrote the file:\n got %q\nwant %q", string(after), string(before))
			}
			if after := dirListing(t, filepath.Dir(manifestPath)); !reflect.DeepEqual(beforeListing, after) {
				t.Fatalf("the failed read changed the directory: got %v want %v", after, beforeListing)
			}
		})
	}
}

// TestManifestUnreadableIsTypedNotMissing pins that an unreadable path is not
// mistaken for a missing one: a store that cannot be read must never look like
// an empty deployment.
func TestManifestUnreadableIsTypedNotMissing(t *testing.T) {
	manifestPath := sandboxStore(t)
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatalf("create manifest directory: %v", err)
	}

	manifest, err := LoadManifest()
	if err == nil {
		t.Fatalf("LoadManifest read a directory as a manifest: %#v", manifest)
	}
	if !errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("error = %v, want it to wrap ErrManifestCorrupt", err)
	}
	assertContains(t, "error message", err.Error(), strconv.Quote(manifestPath), "read")
	if _, statErr := os.Stat(manifestPath); statErr != nil {
		t.Fatalf("the failed read removed the path it could not read: %v", statErr)
	}
}

// TestManifestSchemaTooNew pins the downgrade refusal: a file written by a
// newer build is reported, not re-saved.
func TestManifestSchemaTooNewRefused(t *testing.T) {
	manifestPath := sandboxStore(t)
	writeFileRaw(t, manifestPath, []byte(`{"schema_version":2,"updated_at":"2026-10-22T00:00:00Z","entries":{}}`))
	before := readFileRaw(t, manifestPath)

	manifest, err := LoadManifest()
	if err == nil {
		t.Fatalf("LoadManifest accepted schema_version 2: %#v", manifest)
	}
	if !errors.Is(err, ErrManifestSchemaTooNew) {
		t.Fatalf("error = %v, want it to wrap ErrManifestSchemaTooNew", err)
	}
	if errors.Is(err, ErrManifestCorrupt) {
		t.Fatalf("a newer schema reported as corruption: %v", err)
	}
	assertContains(t, "error message", err.Error(), strconv.Quote(manifestPath), "schema_version 2")
	if after := readFileRaw(t, manifestPath); !bytes.Equal(before, after) {
		t.Fatalf("the refused load rewrote the file: %q", string(after))
	}
}

// TestManifestSaveRefusesInvalidEntries is the write-side fail-closed rule: an
// entry this engine cannot own is refused before anything reaches the disk.
func TestManifestSaveRefusesInvalidEntries(t *testing.T) {
	cases := []struct {
		name     string
		build    func(t *testing.T) *Manifest
		wantErr  error
		wantWord string
	}{
		{
			name: "relative destination",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				manifest.Entries[filepath.Join("rules", "AGENTS.md")] = newEntry(filepath.Join("rules", "AGENTS.md"), ProvenanceGoApplied)
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "is not absolute",
		},
		{
			name: "no destination at all",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				manifest.Entries[""] = ManifestEntry{SHA256: sampleSHA, Provenance: ProvenanceGoApplied}
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "empty",
		},
		{
			name: "unc destination",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				destination := `\\server\share\AGENTS.md`
				manifest.Entries[destination] = newEntry(destination, ProvenanceGoApplied)
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "UNC",
		},
		{
			name: "traversal destination",
			build: func(t *testing.T) *Manifest {
				root := t.TempDir()
				manifest := NewManifest()
				destination := root + string(filepath.Separator) + "rules" + string(filepath.Separator) + ".." +
					string(filepath.Separator) + "AGENTS.md"
				manifest.Entries[destination] = newEntry(destination, ProvenanceGoApplied)
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: `".."`,
		},
		{
			name: "filesystem root destination",
			build: func(t *testing.T) *Manifest {
				root := t.TempDir()
				manifest := NewManifest()
				destination := filepath.VolumeName(root) + string(filepath.Separator)
				manifest.Entries[destination] = newEntry(destination, ProvenanceGoApplied)
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "filesystem root",
		},
		{
			name: "short sha",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				entry := newEntry(filepath.Join(t.TempDir(), "a.md"), ProvenanceGoApplied)
				entry.SHA256 = "abc123"
				manifest.Entries[entry.Path] = entry
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "sha256",
		},
		{
			name: "uppercase sha",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				entry := newEntry(filepath.Join(t.TempDir(), "a.md"), ProvenanceGoApplied)
				entry.SHA256 = strings.ToUpper(sampleSHA)
				manifest.Entries[entry.Path] = entry
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "sha256",
		},
		{
			name: "unknown provenance",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				entry := newEntry(filepath.Join(t.TempDir(), "a.md"), Provenance("handwritten"))
				manifest.Entries[entry.Path] = entry
				return manifest
			},
			wantErr:  ErrManifestInvalid,
			wantWord: "provenance",
		},
		{
			name: "two entries claim one destination",
			build: func(t *testing.T) *Manifest {
				manifest := NewManifest()
				destination := filepath.Join(t.TempDir(), "rules", "AGENTS.md")
				manifest.Entries["first"] = newEntry(destination, ProvenanceGoApplied)
				manifest.Entries["second"] = newEntry(destination, ProvenanceAdopted)
				return manifest
			},
			wantErr:  ErrManifestDuplicateDestination,
			wantWord: "claimed twice",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manifestPath := sandboxStore(t)
			captured := captureWrites(t)
			manifest := testCase.build(t)

			err := SaveManifest(manifest)
			if err == nil {
				t.Fatalf("SaveManifest accepted an invalid manifest: %#v", manifest.Entries)
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error = %v, want it to wrap %v", err, testCase.wantErr)
			}
			assertContains(t, "error message", err.Error(), testCase.wantWord)
			if captured.calls != 0 {
				t.Fatalf("a refused save still called the atomic writer %d time(s)", captured.calls)
			}
			if existsFile(manifestPath) {
				t.Fatalf("a refused save created %q", manifestPath)
			}
		})
	}
}

// TestManifestSaveNormalizesDestinationKeys pins that the destination is the
// authority: whatever key a caller used, the stored key is the cleaned absolute
// path.
func TestManifestSaveNormalizesDestinationKeys(t *testing.T) {
	sandboxStore(t)
	destination := filepath.Join(t.TempDir(), "skills", "usage")
	uncleanKey := destination + string(filepath.Separator) + "."

	manifest := NewManifest()
	manifest.Entries[uncleanKey] = newEntry(destination, ProvenanceGoApplied)
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if _, ok := manifest.Entries[uncleanKey]; ok {
		t.Fatalf("the unclean key survived the save: %v", manifest.Paths())
	}
	if _, ok := manifest.Entries[destination]; !ok {
		t.Fatalf("the cleaned destination is missing: %v", manifest.Paths())
	}

	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if paths := loaded.Paths(); len(paths) != 1 || paths[0] != destination {
		t.Fatalf("stored paths = %v, want [%q]", paths, destination)
	}
	if entry, _ := loaded.Entry(destination); entry.Path != destination {
		t.Fatalf("entry path = %q, want %q", entry.Path, destination)
	}
}

// TestManifestSetAndLookup pins the in-memory API the deploy engine will call:
// Set validates and normalizes, Entry returns a copy, Delete reports presence,
// and Paths is sorted.
func TestManifestSetAndLookup(t *testing.T) {
	sandboxStore(t)
	root := t.TempDir()
	alpha := filepath.Join(root, "a.md")
	beta := filepath.Join(root, "b.md")
	gamma := filepath.Join(root, "c.md")

	manifest := NewManifest()
	mustSet(t, manifest, newEntry(beta, ProvenanceAdopted))
	mustSet(t, manifest, newEntry(alpha, ProvenanceNodeEra))
	mustSet(t, manifest, newEntry(gamma, ProvenanceModeEra))

	if got, want := manifest.Paths(), []string{alpha, beta, gamma}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Paths() = %v, want %v", got, want)
	}

	entry, ok := manifest.Entry(alpha)
	if !ok {
		t.Fatalf("Entry(%q) reported absent", alpha)
	}
	if entry.Provenance != ProvenanceNodeEra {
		t.Fatalf("Entry(%q).Provenance = %q, want %q", alpha, entry.Provenance, ProvenanceNodeEra)
	}
	entry.Provenance = ProvenanceAdopted
	if again, _ := manifest.Entry(alpha); again.Provenance != ProvenanceNodeEra {
		t.Fatal("Entry returned an aliased record: mutating the result changed the store")
	}

	if _, ok := manifest.Entry(filepath.Join(root, "missing.md")); ok {
		t.Fatal("Entry reported a destination that was never recorded")
	}
	if _, ok := manifest.Entry("relative/path.md"); ok {
		t.Fatal("Entry accepted a relative path")
	}

	invalid := newEntry(filepath.Join(root, "d.md"), ProvenanceGoApplied)
	invalid.SHA256 = "nope"
	if err := manifest.Set(invalid); err == nil {
		t.Fatal("Set accepted an invalid sha256")
	} else if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("Set error = %v, want it to wrap ErrManifestInvalid", err)
	}
	if manifest.Len() != 3 {
		t.Fatalf("a refused Set changed the store: %d entries", manifest.Len())
	}

	if !manifest.Delete(beta) {
		t.Fatalf("Delete(%q) reported absent", beta)
	}
	if manifest.Delete(beta) {
		t.Fatalf("Delete(%q) reported present twice", beta)
	}
	if manifest.Delete("relative/path.md") {
		t.Fatal("Delete accepted a relative path")
	}
	if manifest.Len() != 2 {
		t.Fatalf("after Delete the store holds %d entries, want 2", manifest.Len())
	}
}

// TestManifestEntriesUnder pins the subtree lookup a retract uses: children
// only, never a sibling whose name merely shares a prefix.
func TestManifestEntriesUnder(t *testing.T) {
	sandboxStore(t)
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	skillsFile := filepath.Join(skillsDir, "usage", "SKILL.md")
	skillsSelf := filepath.Join(skillsDir, "manifest.json")
	sibling := filepath.Join(root, "skills-extra", "SKILL.md")
	rulesFile := filepath.Join(root, "rules", "AGENTS.md")

	manifest := NewManifest()
	for _, destination := range []string{skillsFile, skillsSelf, sibling, rulesFile} {
		mustSet(t, manifest, newEntry(destination, ProvenanceGoApplied))
	}

	got := manifest.EntriesUnder(skillsDir)
	paths := make([]string, 0, len(got))
	for _, entry := range got {
		paths = append(paths, entry.Path)
	}
	want := []string{filepath.Join(skillsDir, "manifest.json"), skillsFile}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("EntriesUnder(%q) = %v, want %v", skillsDir, paths, want)
	}
	if got := manifest.EntriesUnder(filepath.Join(root, "nothing-here")); len(got) != 0 {
		t.Fatalf("EntriesUnder on an unrecorded subtree = %v, want none", got)
	}
}

// TestManifestNeverWritesRealHome pins the isolation rule: a sandboxed save
// leaves the real ~/.agy-pro store exactly as it found it.
func TestManifestNeverWritesRealHome(t *testing.T) {
	if realHome == "" {
		t.Skip("the real user home does not resolve, so there is nothing to protect")
	}
	realManifest := filepath.Join(realHome, ".agy-pro", ManifestFileName)
	before := existsFile(realManifest)

	manifestPath := sandboxStore(t)
	manifest := NewManifest()
	mustSet(t, manifest, newEntry(filepath.Join(t.TempDir(), "rules", "AGENTS.md"), ProvenanceGoApplied))
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	if !existsFile(manifestPath) {
		t.Fatalf("the sandboxed save did not land in %q", manifestPath)
	}
	if after := existsFile(realManifest); after != before {
		t.Fatalf("the sandboxed save changed the real store %q: before=%v after=%v", realManifest, before, after)
	}
}

// TestParseFeatureAndPreset pins the closed vocabularies and their accepted
// spellings, which the CLI verbs and the hub share.
func TestParseFeatureAndPreset(t *testing.T) {
	features := []struct {
		raw  string
		want Feature
	}{
		{raw: "statusline", want: FeatureStatusline},
		{raw: " status-line ", want: FeatureStatusline},
		{raw: "hook", want: FeatureHook},
		{raw: "hooks", want: FeatureHook},
		{raw: "rules", want: FeatureRules},
		{raw: "r", want: FeatureRules},
		{raw: "PLUGINS", want: FeaturePlugins},
		{raw: "skill", want: FeatureSkills},
	}
	for _, testCase := range features {
		got, err := ParseFeature(testCase.raw)
		if err != nil {
			t.Fatalf("ParseFeature(%q): %v", testCase.raw, err)
		}
		if got != testCase.want {
			t.Fatalf("ParseFeature(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	if got, want := len(AllFeatures()), 5; got != want {
		t.Fatalf("AllFeatures() has %d entries, want %d", got, want)
	}
	for _, raw := range []string{"", "telepathy"} {
		if _, err := ParseFeature(raw); err == nil {
			t.Fatalf("ParseFeature(%q) accepted an unknown feature", raw)
		} else if !errors.Is(err, ErrUnknownFeature) {
			t.Fatalf("ParseFeature(%q) error = %v, want it to wrap ErrUnknownFeature", raw, err)
		}
	}

	presets := []struct {
		raw  string
		want Preset
	}{
		{raw: "native", want: PresetNative},
		{raw: "TOOLS", want: PresetTools},
		{raw: " custom ", want: PresetCustom},
	}
	for _, testCase := range presets {
		got, err := ParsePreset(testCase.raw)
		if err != nil {
			t.Fatalf("ParsePreset(%q): %v", testCase.raw, err)
		}
		if got != testCase.want {
			t.Fatalf("ParsePreset(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
	if _, err := ParsePreset("everything"); err == nil {
		t.Fatal("ParsePreset accepted an unknown preset")
	} else if !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("ParsePreset error = %v, want it to wrap ErrUnknownPreset", err)
	}

	if IsKnownProvenance(Provenance("handwritten")) {
		t.Fatal("IsKnownProvenance accepted an unknown provenance")
	}
	for _, provenance := range []Provenance{ProvenanceAdopted, ProvenanceNodeEra, ProvenanceGoApplied, ProvenanceModeEra} {
		if !IsKnownProvenance(provenance) {
			t.Fatalf("IsKnownProvenance(%q) = false", provenance)
		}
	}
}

// TestManifestNilReceiversAreInert pins that the read-side helpers tolerate a
// nil manifest (a caller that forgot to check LoadManifest's error) and that a
// write is refused rather than panicking.
func TestManifestNilReceiversAreInert(t *testing.T) {
	var manifest *Manifest
	destination := filepath.Join(t.TempDir(), "rules", "AGENTS.md")

	if manifest.Len() != 0 {
		t.Fatalf("nil manifest Len() = %d, want 0", manifest.Len())
	}
	if _, ok := manifest.Entry(destination); ok {
		t.Fatal("nil manifest Entry() reported present")
	}
	if manifest.Paths() != nil {
		t.Fatalf("nil manifest Paths() = %v, want nil", manifest.Paths())
	}
	if manifest.EntriesUnder(t.TempDir()) != nil {
		t.Fatalf("nil manifest EntriesUnder() = %v, want nil", manifest.EntriesUnder(t.TempDir()))
	}
	if manifest.Delete(destination) {
		t.Fatal("nil manifest Delete() reported present")
	}
	manifest.Stamp(sampleTime)

	if err := manifest.Set(newEntry(destination, ProvenanceGoApplied)); err == nil {
		t.Fatal("nil manifest Set() accepted an entry")
	} else if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("nil manifest Set() error = %v, want it to wrap ErrManifestInvalid", err)
	}

	unwritten := filepath.Join(t.TempDir(), ManifestFileName)
	if err := SaveManifestFile(unwritten, nil); err == nil {
		t.Fatal("SaveManifestFile accepted a nil manifest")
	} else if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("SaveManifestFile(nil) error = %v, want it to wrap ErrManifestInvalid", err)
	}
	if existsFile(unwritten) {
		t.Fatalf("SaveManifestFile(nil) created %q", unwritten)
	}
}

// TestManifestErrorFormatting pins the message shape a CLI verb prints: the
// path, the failed step, the sentinel, the detail, and the destination.
func TestManifestErrorFormatting(t *testing.T) {
	var nilError *ManifestError
	if got := nilError.Error(); got == "" {
		t.Fatal("nil *ManifestError rendered an empty message")
	}
	if err := nilError.Unwrap(); err != nil {
		t.Fatalf("nil *ManifestError Unwrap() = %v, want nil", err)
	}

	thin := &ManifestError{Op: "load"}
	assertContains(t, "thin error message", thin.Error(), "integrations manifest", "load failed", "unknown error")

	full := &ManifestError{
		Path:        filepath.Join(t.TempDir(), ManifestFileName),
		Op:          "parse",
		Destination: filepath.Join(t.TempDir(), "rules", "AGENTS.md"),
		Err:         ErrManifestCorrupt,
		Detail:      "unexpected end of JSON input",
	}
	message := full.Error()
	assertContains(t, "full error message", message,
		strconv.Quote(full.Path),
		"parse failed",
		ErrManifestCorrupt.Error(),
		"unexpected end of JSON input",
		"(destination "+strconv.Quote(full.Destination)+")",
	)
	if !errors.Is(full, ErrManifestCorrupt) {
		t.Fatalf("errors.Is(full, ErrManifestCorrupt) = false")
	}
}

// TestManifestRejectsNonHexSHA pins the digest format: 64 characters of
// non-hex text is not a sha256.
func TestManifestRejectsNonHexSHA(t *testing.T) {
	sandboxStore(t)
	manifest := NewManifest()
	entry := newEntry(filepath.Join(t.TempDir(), "a.md"), ProvenanceGoApplied)
	entry.SHA256 = strings.Repeat("z", 64)
	manifest.Entries[entry.Path] = entry

	err := SaveManifest(manifest)
	if err == nil {
		t.Fatal("SaveManifest accepted a non-hex sha256")
	}
	if !errors.Is(err, ErrManifestInvalid) {
		t.Fatalf("error = %v, want it to wrap ErrManifestInvalid", err)
	}
	assertContains(t, "error message", err.Error(), "sha256")
}

// TestManifestFailsClosedWithoutHome pins that the store never falls back to a
// relative or working-directory path when the home directory does not resolve:
// it returns an error instead of inventing a location.
func TestManifestFailsClosedWithoutHome(t *testing.T) {
	for _, name := range []string{paths.EnvAgyProHome, "USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(name, "")
	}

	manifestPath, err := ManifestPath()
	if err == nil {
		t.Skipf("this host still resolves a home directory (%q) with every home variable cleared", manifestPath)
	}
	assertContains(t, "ManifestPath error", err.Error(), "home")

	manifest, loadErr := LoadManifest()
	if loadErr == nil {
		t.Fatalf("LoadManifest resolved %#v with no resolvable home", manifest)
	}
	if manifest != nil {
		t.Fatalf("LoadManifest returned a manifest alongside its error: %#v", manifest)
	}
	if saveErr := SaveManifest(NewManifest()); saveErr == nil {
		t.Fatal("SaveManifest wrote with no resolvable home")
	}
}

// TestManifestZeroValueIsUsable pins the zero-value contract a caller gets from
// a struct literal: a nil entry map is adopted on the first Set, a zero schema
// is written as the current one, an empty manifest saves with an empty entries
// object, and a file without schema_version loads at the current version.
func TestManifestZeroValueIsUsable(t *testing.T) {
	manifestPath := sandboxStore(t)
	destination := filepath.Join(t.TempDir(), "rules", "AGENTS.md")

	var manifest Manifest
	if manifest.Len() != 0 {
		t.Fatalf("zero-value manifest Len() = %d, want 0", manifest.Len())
	}
	if got := manifest.EntriesUnder("relative/dir"); got != nil {
		t.Fatalf("EntriesUnder(relative) = %v, want nil", got)
	}
	mustSet(t, &manifest, newEntry(destination, ProvenanceGoApplied))
	if manifest.Len() != 1 {
		t.Fatalf("zero-value manifest Len() after Set = %d, want 1", manifest.Len())
	}
	if err := SaveManifest(&manifest); err != nil {
		t.Fatalf("SaveManifest(zero-value): %v", err)
	}

	loaded, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.SchemaVersion != CurrentManifestSchemaVersion {
		t.Fatalf("schema version = %d, want %d", loaded.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if loaded.Len() != 1 {
		t.Fatalf("loaded %d entries, want 1", loaded.Len())
	}

	emptyPath := filepath.Join(t.TempDir(), ManifestFileName)
	if err := SaveManifestFile(emptyPath, &Manifest{}); err != nil {
		t.Fatalf("SaveManifestFile(empty zero-value): %v", err)
	}
	doc := decodeRawJSON(t, readFileRaw(t, emptyPath))
	entriesRaw, present := doc["entries"]
	if !present {
		t.Fatalf("saved manifest has no entries key: %v", doc)
	}
	if string(entriesRaw) != "{}" {
		t.Fatalf("empty entries rendered as %s, want {}", string(entriesRaw))
	}
	if string(doc["schema_version"]) != strconv.Itoa(CurrentManifestSchemaVersion) {
		t.Fatalf("schema_version rendered as %s, want %d", string(doc["schema_version"]), CurrentManifestSchemaVersion)
	}

	legacyPath := filepath.Join(t.TempDir(), ManifestFileName)
	writeFileRaw(t, legacyPath, []byte(`{"entries":{}}`))
	legacy, err := LoadManifestFile(legacyPath)
	if err != nil {
		t.Fatalf("LoadManifestFile(no schema_version): %v", err)
	}
	if legacy.SchemaVersion != CurrentManifestSchemaVersion {
		t.Fatalf("schema-less file loaded as version %d, want %d", legacy.SchemaVersion, CurrentManifestSchemaVersion)
	}
	if !existsFile(manifestPath) {
		t.Fatalf("the sandbox store %q is missing after the saves", manifestPath)
	}
}
