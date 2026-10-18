package i18n

import (
	"encoding/json"
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

// TestArabicNoPresentationFormsOrOverrides: ar must not use legacy Arabic
// Presentation Forms (U+FE70-U+FEFF) nor bidi override characters. Only the
// embedding marks RLM (U+200F) and LRM (U+200E) are allowed.
func TestArabicNoPresentationFormsOrOverrides(t *testing.T) {
	m, ok := readAllCatalogs(t)["ar"]
	if !ok {
		t.Skip("ar.json not authored yet")
	}
	banned := map[rune]bool{
		0x202A: true, // LRE
		0x202B: true, // RLE
		0x202C: true, // PDF
		0x202D: true, // LRO
		0x202E: true, // RLO
		0x2066: true, // LRI
		0x2067: true, // RLI
		0x2068: true, // FSI
		0x2069: true, // PDI
	}
	for k, v := range m {
		for _, r := range v {
			if r >= 0xFE70 && r <= 0xFEFF {
				t.Errorf("ar.json: key %q contains Arabic Presentation Form U+%04X", k, r)
			}
			if banned[r] {
				t.Errorf("ar.json: key %q contains bidi override U+%04X; only RLM/LRM are allowed", k, r)
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
		"providers.list": true, "providers.add_interactive": true,
		"providers.add_json": true, "providers.test": true,
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
		"swarm.k3_exclusion": true, "swarm.templates": true,
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
		"env.AGY_LAUNCHER": true, "appearance.language": true, "providers.list": true,
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
		"swarm.k3_exclusion": true, "swarm.templates": true,
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
