// Package i18n holds the embedded string catalogs for the agy-pro setup TUI
// and the lookup bundle every surface resolves its text through.
//
// Catalogs are flat JSON objects (key -> single-line string) embedded with
// go:embed. en.json is the schema: every other locale must carry exactly the
// same key set (schema_test.go gates that). Fallback chain per key:
// requested language -> en -> the key ID itself, so T never returns "".
package i18n

import (
	"embed"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

//go:embed locales/*.json
var localeFS embed.FS

const defaultLang = "en"

// LangMeta describes one selectable UI language. Endonyms are code constants
// (not catalog entries) so every language can read every selector row.
type LangMeta struct {
	Code    string
	Endonym string
	English string
	Dir     string // "ltr" or "rtl"
}

// registry is the closed 12-code set (plan-v2 section 3).
var registry = []LangMeta{
	{Code: "en", Endonym: "English", English: "English", Dir: "ltr"},
	{Code: "zh-Hans", Endonym: "简体中文", English: "Chinese (Simplified)", Dir: "ltr"},
	{Code: "zh-Hant", Endonym: "繁體中文", English: "Chinese (Traditional)", Dir: "ltr"},
	{Code: "ko", Endonym: "한국어", English: "Korean", Dir: "ltr"},
	{Code: "ja", Endonym: "日本語", English: "Japanese", Dir: "ltr"},
	{Code: "ru", Endonym: "Русский", English: "Russian", Dir: "ltr"},
	{Code: "hi", Endonym: "हिन्दी", English: "Hindi", Dir: "ltr"},
	{Code: "de", Endonym: "Deutsch", English: "German", Dir: "ltr"},
	{Code: "fr", Endonym: "Français", English: "French", Dir: "ltr"},
	{Code: "it", Endonym: "Italiano", English: "Italian", Dir: "ltr"},
	{Code: "vi", Endonym: "Tiếng Việt", English: "Vietnamese", Dir: "ltr"},
	{Code: "ar", Endonym: "العربية", English: "Arabic", Dir: "rtl"},
}

var (
	loadOnce sync.Once
	catalogs map[string]map[string]string
)

// Load returns the bundle for lang. The code is normalized
// case-insensitively against the closed 12-code set; an unknown code yields
// the English bundle. Catalogs are parsed once per process.
func Load(lang string) *Bundle {
	loadOnce.Do(loadCatalogs)
	code := Normalize(lang)
	b := &Bundle{Lang: code, Dir: Dir(code), RTL: Dir(code) == "rtl"}
	b.m = catalogs[code]
	b.en = catalogs[defaultLang]
	return b
}

// Bundle resolves display strings for one UI language.
type Bundle struct {
	Lang string
	Dir  string // "ltr" or "rtl" (layout mirroring flag for the painter)
	RTL  bool   // true only for ar in v1
	m    map[string]string
	en   map[string]string
}

// T resolves key: requested language, then en, then the key ID itself.
func (b *Bundle) T(key string) string {
	if s, ok := b.m[key]; ok && s != "" {
		return s
	}
	if b.Lang != defaultLang {
		if s, ok := b.en[key]; ok && s != "" {
			return s
		}
	}
	return key
}

// Tf resolves key and replaces literal {{name}} placeholders with the slot
// values. The replacement is a single pass over the template: an inserted
// value is copied into the result verbatim and the scan never re-enters it,
// so a slot value containing another {{...}} pattern cannot inject a new
// substitution, and the slot-map iteration order cannot change the result.
// Unknown slots stay untouched; extra slot values are ignored, so word order
// stays free per language.
func (b *Bundle) Tf(key string, slots map[string]string) string {
	return Fill(b.T(key), slots)
}

// Fill replaces literal {{name}} placeholders in an already-resolved
// template string, using the same single-pass scan as Tf: an inserted value
// is copied into the result verbatim and the scan never re-enters it, so a
// slot value containing another {{...}} pattern cannot inject a new
// substitution, and the slot-map iteration order cannot change the result.
// Unknown slots stay untouched; extra slot values are ignored. It exists
// for templates that come from a local fallback const rather than a catalog
// entry, where Tf's key lookup cannot be used directly (the hub summary is
// the canonical caller).
func Fill(s string, slots map[string]string) string {
	if len(slots) == 0 {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '{' && i+1 < len(s) && s[i+1] == '{' {
			if j := strings.Index(s[i+2:], "}}"); j >= 0 {
				if val, ok := slots[s[i+2:i+2+j]]; ok {
					sb.WriteString(val)
					i += 2 + j + 2
					continue
				}
			}
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// Tn resolves the plural form: key.one for n == 1, key.other otherwise,
// with the {{n}} slot replaced by the decimal count (the count itself is
// never locale-formatted in v1). The scheme reserves .few/.many for a later
// additive step.
func (b *Bundle) Tn(key string, n int) string {
	var s string
	if n == 1 {
		s = b.T(key + ".one")
	} else {
		s = b.T(key + ".other")
	}
	return strings.ReplaceAll(s, "{{n}}", strconv.Itoa(n))
}

// Normalize maps a user-supplied code onto the closed set, case-insensitive.
// Unknown codes normalize to "en".
func Normalize(code string) string {
	code = strings.TrimSpace(code)
	for _, l := range registry {
		if strings.EqualFold(l.Code, code) {
			return l.Code
		}
	}
	return defaultLang
}

// Dir returns the text direction for lang: "rtl" only for ar, else "ltr".
// Unknown codes follow the en fallback and return "ltr".
func Dir(lang string) string {
	if Normalize(lang) == "ar" {
		return "rtl"
	}
	return "ltr"
}

// Endonym returns the language's own name for itself. Unknown codes follow
// the en fallback and return "English".
func Endonym(lang string) string {
	code := Normalize(lang)
	for _, l := range registry {
		if l.Code == code {
			return l.Endonym
		}
	}
	return defaultLang
}

// Languages returns the full closed-set metadata list, en first. The caller
// may not mutate the result.
func Languages() []LangMeta {
	out := make([]LangMeta, len(registry))
	copy(out, registry)
	return out
}

func loadCatalogs() {
	catalogs = map[string]map[string]string{}
	entries, err := localeFS.ReadDir("locales")
	if err != nil {
		log.Printf("i18n: read locales: %v (English-only fallback)", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		code := strings.TrimSuffix(e.Name(), ".json")
		data, err := localeFS.ReadFile("locales/" + e.Name())
		if err != nil {
			log.Printf("i18n: read %s: %v", e.Name(), err)
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			log.Printf("i18n: parse %s: %v (catalog skipped)", e.Name(), err)
			continue
		}
		for k, v := range m {
			if !looksNFC(v) {
				log.Printf("i18n: %s: value for %q is not NFC-precomposed; falling back to en", e.Name(), k)
				delete(m, k)
			}
		}
		catalogs[code] = m
	}
	if _, ok := catalogs[defaultLang]; !ok {
		log.Printf("i18n: en.json missing or unreadable; T will return key IDs")
		catalogs[defaultLang] = map[string]string{}
	}
}

// looksNFC is the stdlib-only NFC guard. Full normalization needs
// golang.org/x/text, which the zero-dependency rule forbids, so this checks
// the class the authoring rule actually cares about: a Latin base letter
// followed by a combining diacritic from the U+0300-U+036F block, the
// decomposed pattern Vietnamese/French/German/Italian must never ship.
// Indic and Arabic vowel signs live outside that block and are untouched.
func looksNFC(s string) bool {
	var prev rune
	for _, r := range s {
		if r >= 0x0300 && r <= 0x036F && unicode.IsLetter(prev) {
			return false
		}
		prev = r
	}
	return true
}
