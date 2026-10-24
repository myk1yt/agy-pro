package agenttools

// FIX-D1a (R3-1) pins: the name-class fold.
//
// Finding R3-1 claimed that a unicode case-fold homoglyph reaches a governance
// file through the denylist: write_file("ſtate.json") would create the file every
// later "state.json" open resolves to, while the lowercase map lookup never saw
// it. The live measurement on this host (probe harness scratch/fix-d1a, table in
// coder-report-fix-d1a.md) shows the FILE-SYSTEM half does not reproduce on
// either probed volume: "ſ" (U+017F) and "s" are distinct files there, so the
// finding is the spoof class here, not a live overwrite. The fold lands anyway,
// because NTFS $UpCase is a per-volume table and one fold function is the only
// way every comparison can agree.
//
// These pins hold the fold at the PREDICATE and WRITE level, which is the layer
// that must not depend on which volume a workspace sits on. The measurement of
// the $UpCase table itself is the harness's job (a test cannot format a volume
// with a different table).

import (
	"strings"
	"testing"
)

// longS is U+017F LATIN SMALL LETTER LONG S: it uppercases to "S", so a fold
// built on strings.ToUpper must merge it with the ASCII spelling.
const longS = "ſ"

// TestWin32NameFoldMergesTheUpCaseSpellings pins the direction of the fold: the
// key both spellings land on, for every pair the guard compares.
func TestWin32NameFoldMergesTheUpCaseSpellings(t *testing.T) {
	// pairs: two spellings that must fold to ONE key, with that key.
	triples := []struct{ a, b, want string }{
		{"state.json", "STATE.JSON", "state.json"},
		{longS + "tate.json", "state.json", "state.json"},
		{longS + "TATE.JSON", "state.json", "state.json"},
		{"agent" + longS + ".md", "agents.md", "agents.md"},
		{"provıders.json", "providers.json", "providers.json"}, // U+0131 dotless i: over-fold, fail closed
		{"K.txt", "k.txt", "k.txt"},                            // U+212A KELVIN SIGN: $UpCase folds it, the probed volumes did NOT collide the two files (over-fold, fail closed)
		{"café.json", "CAFÉ.JSON", "café.json"},
	}
	for _, tc := range triples {
		if got := win32NameFold(tc.a); got != tc.want {
			t.Errorf("win32NameFold(%q) = %q; want %q", tc.a, got, tc.want)
		}
		if got := win32NameFold(tc.b); got != tc.want {
			t.Errorf("win32NameFold(%q) = %q; want %q", tc.b, got, tc.want)
		}
		if win32NameFold(tc.a) != win32NameFold(tc.b) {
			t.Errorf("win32NameFold(%q) != win32NameFold(%q): the two spellings must share one key", tc.a, tc.b)
		}
	}

	// Idempotence: folding a key again must not move it (the maps are keyed by
	// folded names).
	for _, name := range []string{"state.json", "AGENTS.MD", "café.json", "日本語.md"} {
		once := win32NameFold(name)
		if twice := win32NameFold(once); twice != once {
			t.Errorf("win32NameFold is not idempotent on %q: %q -> %q", name, once, twice)
		}
	}

	// The export is the same function (D1b consumes it).
	for _, name := range []string{"state.json", longS + "tate.json", "café.json"} {
		if Win32NameFold(name) != win32NameFold(name) {
			t.Errorf("Win32NameFold(%q) disagrees with the unexported fold", name)
		}
	}
}

// TestWin32NameFoldDocumentsItsLimits pins what the fold does NOT do, so a later
// reader cannot mistake it for the NTFS table itself.
func TestWin32NameFoldDocumentsItsLimits(t *testing.T) {
	// Fullwidth forms and superscript digits keep their own identity: $UpCase
	// gives them no ASCII collision (measured in the harness collision matrix),
	// and the fold must not invent one.
	for _, name := range []string{"ＡＧＥＮＴＳ.ＭＤ", "com¹"} {
		if got := win32NameFold(name); got == "agents.md" || got == "com1" {
			t.Errorf("win32NameFold(%q) = %q: the fold must not merge a look-alike class NTFS keeps apart", name, got)
		}
	}
	// No expansion: Go's simple case mapping never turns "ß" into "SS".
	if got := win32NameFold("ß"); got != "ß" {
		t.Errorf("win32NameFold(%q) = %q; the fold must not expand a character", "ß", got)
	}
}

// TestNameClassSetsAreKeyedByTheFold pins the "keep the maps, fold on lookup"
// decision in both directions: every key is already in folded form (so folding a
// key is a no-op), and every spelling of a key is refused.
func TestNameClassSetsAreKeyedByTheFold(t *testing.T) {
	for name := range governanceWriteNames {
		if win32NameFold(name) != name {
			t.Errorf("governanceWriteNames key %q is not in folded form (fold = %q); the fold-on-lookup would miss it", name, win32NameFold(name))
		}
		if comp, refuse := GovernanceWriteRefusal(name); !refuse {
			t.Errorf("GovernanceWriteRefusal(%q) = (%q, false); want a refusal", name, comp)
		}
		if comp, refuse := GovernanceWriteRefusal(strings.ToUpper(name)); !refuse {
			t.Errorf("GovernanceWriteRefusal(%q) = (%q, false); want a refusal", strings.ToUpper(name), comp)
		}
		if spelled, ok := longSSpelling(name); ok {
			if comp, refuse := GovernanceWriteRefusal(spelled); !refuse {
				t.Errorf("GovernanceWriteRefusal(%q) = (%q, false); the long-s spelling of %q must be refused", spelled, comp, name)
			}
		}
	}
	for name := range protectedWriteNames {
		if win32NameFold(name) != name {
			t.Errorf("protectedWriteNames key %q is not in folded form (fold = %q)", name, win32NameFold(name))
		}
		if comp, refuse := VCSMetadataRefusal(name); !refuse {
			t.Errorf("VCSMetadataRefusal(%q) = (%q, false); want a refusal", name, comp)
		}
		if comp, refuse := VCSMetadataRefusal(strings.ToUpper(name)); !refuse {
			t.Errorf("VCSMetadataRefusal(%q) = (%q, false); want a refusal", strings.ToUpper(name), comp)
		}
		if spelled, ok := longSSpelling(name); ok {
			if comp, refuse := VCSMetadataRefusal(spelled); !refuse {
				t.Errorf("VCSMetadataRefusal(%q) = (%q, false); the long-s spelling of %q must be refused", spelled, comp, name)
			}
		}
	}
	// Device names take the same fold.
	for base := range reservedDeviceNames {
		if got := deviceNameOf(base + ".log"); got != base {
			t.Errorf("deviceNameOf(%q) = %q; want %q", base+".log", got, base)
		}
	}
}

// longSSpelling returns name with its first "s" replaced by U+017F, and false
// when the name has no "s" to replace.
func longSSpelling(name string) (string, bool) {
	if i := strings.IndexByte(name, 's'); i >= 0 {
		return name[:i] + longS + name[i+1:], true
	}
	return "", false
}

// TestWriteGuardRefusesHomoglyphSpellings pins the WRITE level: a homoglyph
// spelling of a governed name is refused by name, nothing lands, and ordinary
// unicode names stay writable.
func TestWriteGuardRefusesHomoglyphSpellings(t *testing.T) {
	root := t.TempDir()

	refused := []string{
		longS + "tate.json",
		longS + "TATE.JSON",
		"ſettings.json",
		"provıders.json",
		"sub/ſtate.json",
		"ſtate-fanout.json",
	}
	for _, rel := range refused {
		_, err := WriteFile(root, rel, "EVIL", true)
		if err == nil {
			t.Fatalf("WriteFile(%q): expected the name-class refusal, got nil", rel)
		}
		if !strings.Contains(err.Error(), "governance/credential file") {
			t.Fatalf("WriteFile(%q): refusal does not name the governance class: %v", rel, err)
		}
		assertNoWriteLanded(t, root, rel)
	}

	// Non-vacuity: unicode names that merely LOOK unusual are not governed and
	// must stay writable (the fold is a name-class test, not a unicode ban).
	for _, rel := range []string{"café.json", "naïve-notes.md", "日本語.md", "grüße.txt"} {
		if _, err := WriteFile(root, rel, "ok", true); err != nil {
			t.Fatalf("WriteFile(%q): benign unicode name refused: %v", rel, err)
		}
	}
}
