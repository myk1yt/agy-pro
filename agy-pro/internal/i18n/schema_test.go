package i18n

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// readAllCatalogs parses every embedded locale file once per test run.
func readAllCatalogs(t *testing.T) map[string]map[string]string {
	t.Helper()
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		t.Fatalf("read locales: %v", err)
	}
	out := map[string]map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out[code] = m
	}
	if len(out) == 0 {
		t.Fatal("no locale files embedded")
	}
	return out
}

func enKeys(t *testing.T, catalogs map[string]map[string]string) map[string]bool {
	t.Helper()
	en, ok := catalogs["en"]
	if !ok {
		t.Fatal("en.json is missing; it is the schema and must exist")
	}
	keys := map[string]bool{}
	for k := range en {
		keys[k] = true
	}
	return keys
}

// TestKeySetsIdentical: every locale carries EXACTLY the en key set. This
// stays green when the other 11 catalogs land (P13) and fails on any drift.
func TestKeySetsIdentical(t *testing.T) {
	catalogs := readAllCatalogs(t)
	keys := enKeys(t, catalogs)
	for code, m := range catalogs {
		if code == "en" {
			continue
		}
		var missing, extra []string
		for k := range keys {
			if _, ok := m[k]; !ok {
				missing = append(missing, k)
			}
		}
		for k := range m {
			if !keys[k] {
				extra = append(extra, k)
			}
		}
		if len(missing) > 0 || len(extra) > 0 {
			sort.Strings(missing)
			sort.Strings(extra)
			t.Errorf("%s.json drift: missing %d keys %v, extra %d keys %v",
				code, len(missing), missing, len(extra), extra)
		}
	}
}

// TestNoEmptyValues: every value in every locale is non-empty.
func TestNoEmptyValues(t *testing.T) {
	for code, m := range readAllCatalogs(t) {
		for k, v := range m {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s.json: key %q has an empty value", code, k)
			}
		}
	}
}

var slotRe = regexp.MustCompile(`\{\{([a-z0-9_]+)\}\}`)

func slotNames(v string) []string {
	found := slotRe.FindAllStringSubmatch(v, -1)
	names := make([]string, 0, len(found))
	for _, f := range found {
		names = append(names, f[1])
	}
	sort.Strings(names)
	return names
}

// TestPlaceholderParity: the multiset of {{slot}} names per key is identical
// to en in every locale; a translator cannot drop or invent a slot.
func TestPlaceholderParity(t *testing.T) {
	catalogs := readAllCatalogs(t)
	en := catalogs["en"]
	for code, m := range catalogs {
		if code == "en" {
			continue
		}
		for k, ev := range en {
			lv, ok := m[k]
			if !ok {
				continue // key-set test already reports this
			}
			es, ls := slotNames(ev), slotNames(lv)
			if strings.Join(es, ",") != strings.Join(ls, ",") {
				t.Errorf("%s.json: key %q slot mismatch: en %v, %s %v", code, k, es, code, ls)
			}
		}
	}
}

// authoringTokenRe matches a SINGLE-brace authoring token: the shape a catalog
// author writes for a value a template fills in ({iapply}, {drefresh}, {eight}).
// Bundle.T and Bundle.Tf fill only the {{double}} slot form, so a single-brace
// token is always a defect: it reaches the frame verbatim, and in en that means
// every other locale inherits the raw token through the fallback chain. The
// pattern is deliberately tight (lowercase start, then lowercase, digits and
// underscore) so ordinary prose cannot trip it, and it is applied to the value
// AFTER every {{slot}} span is blanked: "{{n}}" contains the substring "{n}",
// which would otherwise read as a token.
var authoringTokenRe = regexp.MustCompile(`\{[a-z][a-z0-9_]*\}`)

// authoringTokenIn returns the first single-brace authoring token in v, or "".
func authoringTokenIn(v string) string {
	return authoringTokenRe.FindString(slotRe.ReplaceAllString(v, ""))
}

// TestNoAuthoringTokens: no catalog value may ship a single-brace authoring
// token. The defect this pins is real history (FIX-16 P1-A): a value written as
// a template string landed in en.json with the tokens intact, so the rendered
// page read "the {iapply} row deploys ...", and en is the fallback locale every
// other catalog leans on.
func TestNoAuthoringTokens(t *testing.T) {
	for code, m := range readAllCatalogs(t) {
		for k, v := range m {
			if tok := authoringTokenIn(v); tok != "" {
				t.Errorf("%s.json: key %q ships the authoring token %s; T and Tf fill only the {{name}} slot form, so this value would render verbatim",
					code, k, tok)
			}
		}
	}
}

// TestAuthoringTokenLintBites pins the lint itself, so a future refactor cannot
// turn TestNoAuthoringTokens into a no-op: a planted token is caught, and the
// four near-miss shapes (a real {{slot}}, an uppercase body, an unterminated
// brace, plain prose) stay green.
func TestAuthoringTokenLintBites(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"The {iapply} row deploys every feature.", "{iapply}"},
		{"{drefresh} runs one bounded refresh.", "{drefresh}"},
		{"eight sections: {eight}", "{eight}"},
		{"rows: {a} and {b_2}", "{a}"},
		{"{{n}} providers", ""},
		{"{{providers}} | {{presets}} | {{lang}}", ""},
		{"no braces here at all", ""},
		{"{Apply} is not the token shape", ""},
		{"a {b unterminated and c} d", ""},
	}
	for _, c := range cases {
		if got := authoringTokenIn(c.value); got != c.want {
			t.Errorf("authoringTokenIn(%q) = %q, want %q", c.value, got, c.want)
		}
	}
}

// TestValueHygiene: no ESC, CR, LF, em-dash, or community banner URL inside
// any value. Values are single-line by contract.
func TestValueHygiene(t *testing.T) {
	for code, m := range readAllCatalogs(t) {
		for k, v := range m {
			if strings.ContainsRune(v, 0x1B) {
				t.Errorf("%s.json: key %q contains ESC (0x1B)", code, k)
			}
			if strings.ContainsAny(v, "\r\n") {
				t.Errorf("%s.json: key %q contains CR/LF; values must be single-line", code, k)
			}
			if strings.ContainsRune(v, '\u2014') {
				t.Errorf("%s.json: key %q contains an em-dash character", code, k)
			}
			if strings.Contains(v, "reddit.com") {
				t.Errorf("%s.json: key %q contains the community banner URL", code, k)
			}
		}
	}
}

// TestNoCombiningDiacritics: vi/de/fr/it must be authored NFC-precomposed.
// Stdlib has no normalizer, so this is enforced as an authoring rule: no
// combining marks (Mn/Me) at all in these locales.
func TestNoCombiningDiacritics(t *testing.T) {
	catalogs := readAllCatalogs(t)
	for _, code := range []string{"vi", "de", "fr", "it"} {
		m, ok := catalogs[code]
		if !ok {
			continue // catalog not authored yet; key-set test gates its arrival
		}
		for k, v := range m {
			for _, r := range v {
				if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
					t.Errorf("%s.json: key %q contains combining mark U+%04X; author NFC-precomposed text",
						code, k, r)
				}
			}
		}
	}
}

// bannedInvisible holds every character no catalog value may carry. Two groups
// live here: bidi overrides/isolates (they reorder a rendered line without a
// visible glyph) and zero-width/format characters (ZWSP/ZWNJ/ZWJ, BOM, word
// joiner, soft hyphen) plus the Unicode tag block. A translation PR is
// supply-chain input; this ban is the layer that keeps a payload out of the
// tree even though the TUI already strips Cf+Cc at paint.
//
// LRM (U+200E) and RLM (U+200F) stay ALLOWED on purpose: they are the
// embedding marks a right-to-left translation may legitimately carry, and
// unlike an override they cannot reverse the rest of a line.
var bannedInvisible = map[rune]string{
	0x200B: "zero width space",
	0x200C: "zero width non-joiner",
	0x200D: "zero width joiner",
	0x202A: "bidi override LRE",
	0x202B: "bidi override RLE",
	0x202C: "bidi override PDF",
	0x202D: "bidi override LRO",
	0x202E: "bidi override RLO",
	0x2060: "word joiner",
	0x2066: "bidi isolate LRI",
	0x2067: "bidi isolate RLI",
	0x2068: "bidi isolate FSI",
	0x2069: "bidi isolate PDI",
	0xFEFF: "byte order mark / zero width no-break space",
	0x00AD: "soft hyphen",
}

// bannedInvisibleReason names the character at r, or returns "" when r is
// allowed. Tag characters (U+E0000-U+E007F) are invisible and have no
// legitimate use in a UI string, so the whole block is banned.
func bannedInvisibleReason(r rune) string {
	if r >= 0xE0000 && r <= 0xE007F {
		return "Unicode tag character"
	}
	return bannedInvisible[r]
}

// TestEveryLocaleBansInvisibleAndBidiChars: the ban covers ALL twelve
// catalogs, not just ar. Before this test the bidi-override ban ran over
// ar.json alone and zero-width characters were banned nowhere, so a crafted
// ru.json or de.json kept the whole suite green.
func TestEveryLocaleBansInvisibleAndBidiChars(t *testing.T) {
	for code, m := range readAllCatalogs(t) {
		for k, v := range m {
			for _, r := range v {
				if reason := bannedInvisibleReason(r); reason != "" {
					t.Errorf("%s.json: key %q contains U+%04X (%s); catalog values carry no invisible or bidi-override characters",
						code, k, r, reason)
				}
			}
		}
	}
}

// TestArabicNoPresentationForms: ar must not use legacy Arabic Presentation
// Forms (U+FE70-U+FEFF); the modern block is the authored form. The bidi half
// of the old test moved to TestEveryLocaleBansInvisibleAndBidiChars, which
// covers this locale too.
func TestArabicNoPresentationForms(t *testing.T) {
	m, ok := readAllCatalogs(t)["ar"]
	if !ok {
		t.Skip("ar.json not authored yet")
	}
	for k, v := range m {
		for _, r := range v {
			if r >= 0xFE70 && r <= 0xFEFF {
				t.Errorf("ar.json: key %q contains Arabic Presentation Form U+%04X", k, r)
			}
		}
	}
}

// TestEnCatalogCoversViewItems: every static Item ID the settings View()
// builds today must have a view.<id>.label key (and .note where view.go
// carries a Note literal). This pins the transcription at authoring time.
func TestEnCatalogCoversViewItems(t *testing.T) {
	en := readAllCatalogs(t)["en"]
	items := map[string]bool{
		// sectionLaunch (p1)
		"default_profile": true, "current_mode": true, "managed_items": true,
		"last_backup": true, "updated_at": true, "config.show": true,
		"mode.switch": true, "mode.restore": true, "mode.backups": true,
		"mode.status": true, "env.AGY_LAUNCHER": true,
		// sectionAppearance (p8, landed concurrently with the P10 unit)
		"appearance.language": true, "appearance.launcher_menu": true,
		// sectionProviders (p2)
		"providers.add_interactive": true, "providers.add_json": true, "providers.test": true,
		"providers.rename": true, "providers.delete": true,
		"models.inventory": true, "providers.presets": true,
		"providers.model_capabilities": true, "providers.key_policy": true,
		"providers.registry_error": true,
		// sectionBridge (p3)
		"bridge_tools": true, "env.AGY_PRO_BRIDGE_TOOLS": true,
		"budget.iterations": true, "budget.wall": true, "budget.turn": true,
		"budget.tool": true, "budget.tool_calls_per_turn": true, "budget.fallback": true,
		// sectionSwarm (p4)
		"native_seat_concurrency": true, "fanout.n": true, "fanout.timeout": true,
		"fanout.max_turns": true, "fanout.tools": true, "fanout.task": true,
		"fanout.flags": true, "fanout.registry_defaults": true,
		"fanout.consensus": true, "fanout.failover": true,
		"swarm.exclude_models": true, "swarm.templates": true,
		"swarm.presets": true, "swarm.save_preset": true,
		// sectionDiagnostics (p5)
		"doctor.diagnose": true, "doctor.fix_keys": true,
		"doctor.rollback_keys": true, "doctor.exit_semantics": true,
		// sectionMaintenance (p6)
		"version.info": true, "version.shim_path": true, "version.real_agy": true,
		"update.info": true, "uninstall.info": true, "install.info": true,
		// sectionEnvDocs (p7)
		"env.AGY_PRO_HOME": true, "env.AGY_PRO_PROVIDERS": true,
		"env.AGY_PRO_REAL_AGY": true, "env.AGY_PRO_ACTIVE": true,
		"env.GEMINI_CONFIG_DIR": true, "env.AGY_PRO_WORKSPACE": true,
		"env.AGY_PRO_BRIDGE": true,
	}
	// IDs whose view.go Item carries a Note literal and thus need .note.
	withNote := map[string]bool{
		"default_profile": true, "current_mode": true, "managed_items": true,
		"last_backup": true, "config.show": true, "mode.switch": true,
		"mode.restore": true, "mode.backups": true, "mode.status": true,
		"env.AGY_LAUNCHER": true, "appearance.language": true,
		"providers.add_interactive": true, "providers.add_json": true,
		"providers.test": true, "providers.rename": true, "providers.delete": true,
		"models.inventory": true, "providers.presets": true,
		"providers.model_capabilities": true, "providers.key_policy": true,
		"bridge_tools": true, "env.AGY_PRO_BRIDGE_TOOLS": true,
		"budget.iterations": true, "budget.wall": true, "budget.turn": true,
		"budget.tool": true, "budget.tool_calls_per_turn": true, "budget.fallback": true,
		"native_seat_concurrency": true, "fanout.n": true, "fanout.timeout": true,
		"fanout.max_turns": true, "fanout.tools": true, "fanout.task": true,
		"fanout.flags": true, "fanout.registry_defaults": true,
		"fanout.consensus": true, "fanout.failover": true,
		"swarm.exclude_models": true, "swarm.templates": true,
		"swarm.presets": true, "swarm.save_preset": true,
		"doctor.diagnose": true, "doctor.fix_keys": true,
		"doctor.rollback_keys": true, "doctor.exit_semantics": true,
		"version.info": true, "version.real_agy": true, "update.info": true,
		"uninstall.info": true, "install.info": true,
		"env.AGY_PRO_HOME": true, "env.AGY_PRO_PROVIDERS": true,
		"env.AGY_PRO_REAL_AGY": true, "env.AGY_PRO_ACTIVE": true,
		"env.GEMINI_CONFIG_DIR": true, "env.AGY_PRO_WORKSPACE": true,
		"env.AGY_PRO_BRIDGE": true,
	}
	for id := range items {
		if _, ok := en["view."+id+".label"]; !ok {
			t.Errorf("en.json missing view.%s.label", id)
		}
		if withNote[id] {
			if _, ok := en["view."+id+".note"]; !ok {
				t.Errorf("en.json missing view.%s.note (view.go carries a Note literal)", id)
			}
		}
	}
}

// TestSectionTitlesCarryNoIndex (P1-6): the setup hub owns the numbered
// navigation surface (its rows render [1]..[8] from one renderer). Section
// titles named the same digits as a second, independent numbering scheme, so a
// header read "[2] Models & Providers (2/8)" inside a hub row numbered [4]. The
// pinned invariant is that no section title in any catalog starts with a
// bracketed index; the symbol is a plain title string.
func TestSectionTitlesCarryNoIndex(t *testing.T) {
	for code, cat := range readAllCatalogs(t) {
		for n := 1; n <= 8; n++ {
			key := fmt.Sprintf("section.p%d.title", n)
			val, ok := cat[key]
			if !ok {
				continue // key-set parity is TestKeySetsIdentical's job
			}
			if strings.HasPrefix(val, "[") {
				t.Errorf("%s.json: %s = %q carries its own bracketed index; the hub is the only numbered surface", code, key, val)
			}
		}
	}
}

// TestExclusionListCatalogCoverage (P4-5) pins the exclusion-list key set: the
// retired read-only k3 row's keys are gone from EVERY catalog, the new editor
// keys exist in all twelve, and the two facts a translation must never lose
// survive: the hub route command (untranslated, because a translated command
// would not run) and the k3 name of the built-in default.
func TestExclusionListCatalogCoverage(t *testing.T) {
	catalogs := readAllCatalogs(t)
	required := []string{
		"view.swarm.exclude_models.label",
		"view.swarm.exclude_models.note",
		"view.fanout.failover.note",
		"settings.exclude.builtin_absent",
		"settings.exclude.owned",
		"settings.exclude.entries_empty",
		"settings.exclude.add",
		"settings.exclude.remove",
		"settings.exclude.save",
		"settings.exclude.armed",
		"settings.exclude.hint",
		"settings.exclude.stale_base",
		"settings.exclude.disk_check_failed",
		"settings.prompt.exclude_entry",
		"settings.msg.exclude_invalid",
		"settings.msg.exclude_none_armed",
		"settings.msg.exclude_unchanged",
		"settings.approval.exclude_models",
		"settings.approval.exclude_counts",
		"settings.approval.exclude_absent",
	}
	retired := []string{
		"view.swarm.k3_exclusion.label",
		"view.swarm.k3_exclusion.note",
	}
	locales := 0
	for code, cat := range catalogs {
		locales++
		for _, k := range required {
			if _, ok := cat[k]; !ok {
				t.Errorf("%s.json missing the exclusion key %s", code, k)
			}
		}
		for _, k := range retired {
			if _, ok := cat[k]; ok {
				t.Errorf("%s.json still carries the retired key %s", code, k)
			}
		}
		note := cat["view.swarm.exclude_models.note"]
		if !strings.Contains(note, "agy pro setup") {
			t.Errorf("%s.json: the exclusion note lost the hub route command: %q", code, note)
		}
		if !strings.Contains(note, "k3") {
			t.Errorf("%s.json: the exclusion note lost the k3 name of the built-in default: %q", code, note)
		}
		// The built-in default is reproduced by the FAMILY form of the grammar,
		// so the note must name family:k3 (the entry the row shows and the editor
		// seeds), not leave each locale to paraphrase a bare k3 entry: a bare
		// entry does not reproduce the built-in predicate.
		if !strings.Contains(note, "family:k3") {
			t.Errorf("%s.json: the exclusion note does not name family:k3 (the exact list spelling of the built-in default): %q", code, note)
		}
		if strings.Contains(cat["settings.approval.exclude_counts"], "Models before") {
			t.Errorf("%s.json: the entry counts line reuses the model counts sentence", code)
		}
	}
	if locales != 12 {
		t.Fatalf("catalogs read = %d; want the closed 12-locale set", locales)
	}
}
