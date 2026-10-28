package i18n

import (
	"strings"
	"testing"
)

func TestNormalizeCodes(t *testing.T) {
	cases := map[string]string{
		"en":      "en",
		"EN":      "en",
		" eN ":    "en",
		"zh-hans": "zh-Hans",
		"ZH-HANT": "zh-Hant",
		"ko":      "ko",
		"AR":      "ar",
		"":        "en",
		"xx":      "en",
		"fr-FR":   "en",
		"pt":      "en",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadUnknownCodeFallsBackToEnglish(t *testing.T) {
	b := Load("xx-not-a-language")
	if b.Lang != "en" {
		t.Errorf("Lang = %q, want en", b.Lang)
	}
	if b.RTL {
		t.Error("RTL = true for unknown code, want false")
	}
}

func TestClosedSetCodesLoadNativeBundle(t *testing.T) {
	// ko.json is authored now; the bundle must resolve natively, report ko
	// as its language, and return the Korean catalog value, not the en text.
	b := Load("ko")
	if b.Lang != "ko" {
		t.Errorf("Lang = %q, want ko", b.Lang)
	}
	if got := b.T("banner.tagline"); got != "Gemini 워크스페이스, 한 번만 설정" {
		t.Errorf("T(banner.tagline) via ko = %q, want the Korean catalog value", got)
	}
	// An unknown code still falls back to en.
	if got := Load("xx-not-a-language").T("banner.tagline"); got != "Gemini workspace, configured once" {
		t.Errorf("T(banner.tagline) via unknown code = %q, want English fallback", got)
	}
}

func TestTFallbackChainNeverEmpty(t *testing.T) {
	b := Load("en")
	// Missing key in en: the key ID itself is the last fallback.
	if got := b.T("no.such.key"); got != "no.such.key" {
		t.Errorf("T(missing) = %q, want the key ID", got)
	}
	// Known key must never resolve to its own ID.
	if got := b.T("menu.title"); got == "menu.title" || got == "" {
		t.Errorf("T(menu.title) = %q, want catalog value", got)
	}
}

func TestTfSlotInterpolation(t *testing.T) {
	b := Load("en")
	got := b.Tf("providers.test.ok", map[string]string{"name": "google"})
	if got != "google: connection OK" {
		t.Errorf("Tf = %q", got)
	}
	// Multiple slots in one template.
	got = b.Tf("models.delete.body", map[string]string{"id": "gemini-pro", "provider": "google"})
	want := "Removes the registry entry for gemini-pro in google. Key values are untouched."
	if got != want {
		t.Errorf("Tf = %q, want %q", got, want)
	}
	// A slot the template does not use is ignored.
	got = b.Tf("providers.test.ok", map[string]string{"name": "google", "extra": "x"})
	if got != "google: connection OK" {
		t.Errorf("Tf with extra slot = %q", got)
	}
	// An unused slot name leaves the placeholder literal.
	got = b.Tf("providers.test.ok", map[string]string{"other": "x"})
	if !strings.Contains(got, "{{name}}") {
		t.Errorf("Tf missing slot should keep placeholder, got %q", got)
	}
}

// TestTfSlotValueNotReExpanded pins the single-pass replacement: a slot
// value carrying another {{...}} pattern is inserted verbatim and never
// rescanned, so values cannot inject new substitutions regardless of the
// slot-map iteration order.
func TestTfSlotValueNotReExpanded(t *testing.T) {
	b := Load("en")
	got := b.Tf("providers.test.ok", map[string]string{"name": "{{other}}"})
	if want := "{{other}}: connection OK"; got != want {
		t.Errorf("Tf re-expanded the slot value: got %q, want %q", got, want)
	}
	// A value shaped like a DIFFERENT slot must not consume that slot's text:
	// the id slot is replaced in place and the provider text stays literal.
	got = b.Tf("models.delete.body", map[string]string{"id": "{{provider}}", "provider": "google"})
	want := "Removes the registry entry for {{provider}} in google. Key values are untouched."
	if got != want {
		t.Errorf("Tf cross-slot injection: got %q, want %q", got, want)
	}
	// Unknown slots still keep their placeholder literally.
	got = b.Tf("providers.test.ok", map[string]string{"other": "x"})
	if !strings.Contains(got, "{{name}}") {
		t.Errorf("Tf missing slot should keep placeholder, got %q", got)
	}
}

// TestFillHubSlotsSinglePass pins the hub-counts filler (i18n.Fill, the
// single-pass core every fillHubSlots call routes through) against the
// round-2 class of bug: a slot value containing another {{...}} pattern is
// inserted verbatim and never re-expanded, and the slot-map iteration order
// cannot change the result.
func TestFillHubSlotsSinglePass(t *testing.T) {
	// Hub-summary shape: the template mixes Tn count phrases and plain slots.
	tpl := "{{providers}} | {{presets}} | {{prompts}} | {{registry}} | {{lang}}"
	slots := map[string]string{
		"providers": "1 provider",
		"presets":   "{{other}} presets", // literal {{other}} must survive
		"prompts":   "2 prompt overrides",
		"registry":  "registry OK",
		"lang":      "English",
	}
	want := "1 provider | {{other}} presets | 2 prompt overrides | registry OK | English"
	if got := Fill(tpl, slots); got != want {
		t.Errorf("Fill re-expanded a slot value: got %q, want %q", got, want)
	}
	// Cross-slot injection: a value shaped like another slot must not
	// consume that slot's text.
	got := Fill("{{providers}} | {{lang}}", map[string]string{"providers": "{{lang}}", "lang": "English"})
	if want := "{{lang}} | English"; got != want {
		t.Errorf("Fill cross-slot injection: got %q, want %q", got, want)
	}
	// A slot the template does not use is ignored; an unknown slot name
	// keeps its placeholder literally.
	if got := Fill(tpl, map[string]string{"other": "x"}); got != tpl {
		t.Errorf("Fill unknown slots should keep placeholders, got %q", got)
	}
	// No slots at all returns the template untouched.
	if got := Fill(tpl, nil); got != tpl {
		t.Errorf("Fill with nil slots = %q, want %q", got, tpl)
	}
}

func TestTnPlural(t *testing.T) {
	b := Load("en")
	if got := b.Tn("menu.count.providers", 1); got != "1 provider" {
		t.Errorf("Tn(n=1) = %q", got)
	}
	if got := b.Tn("menu.count.providers", 0); got != "0 providers" {
		t.Errorf("Tn(n=0) = %q", got)
	}
	if got := b.Tn("menu.count.providers", 7); got != "7 providers" {
		t.Errorf("Tn(n=7) = %q", got)
	}
}

func TestDir(t *testing.T) {
	if got := Dir("ar"); got != "rtl" {
		t.Errorf("Dir(ar) = %q, want rtl", got)
	}
	for _, code := range []string{"en", "ko", "zh-Hans", "hi", "he"} {
		if got := Dir(code); got != "ltr" {
			t.Errorf("Dir(%q) = %q, want ltr", code, got)
		}
	}
}

func TestEndonym(t *testing.T) {
	if got := Endonym("en"); got != "English" {
		t.Errorf("Endonym(en) = %q", got)
	}
	if got := Endonym("ko"); got != "한국어" {
		t.Errorf("Endonym(ko) = %q", got)
	}
	if got := Endonym("bogus"); got != "English" {
		t.Errorf("Endonym(bogus) = %q, want English fallback", got)
	}
}

func TestLanguagesClosedSet(t *testing.T) {
	langs := Languages()
	if len(langs) != 12 {
		t.Fatalf("Languages() len = %d, want 12", len(langs))
	}
	if langs[0].Code != "en" {
		t.Errorf("Languages()[0] = %q, want en first", langs[0].Code)
	}
	seen := map[string]bool{}
	for _, l := range langs {
		if seen[l.Code] {
			t.Errorf("duplicate code %q", l.Code)
		}
		seen[l.Code] = true
		if l.Endonym == "" || l.English == "" {
			t.Errorf("%q missing endonym/english name", l.Code)
		}
		want := "ltr"
		if l.Code == "ar" {
			want = "rtl"
		}
		if l.Dir != want {
			t.Errorf("%q Dir = %q, want %q", l.Code, l.Dir, want)
		}
	}
	// The returned slice is a copy: mutating it must not corrupt the registry.
	langs[0].Code = "MUTATED"
	if Languages()[0].Code != "en" {
		t.Error("Languages() does not return a copy")
	}
}

func TestLoadCaseInsensitive(t *testing.T) {
	if Load("EN").Lang != "en" {
		t.Error("Load(EN) did not normalize to en")
	}
	if Load("Zh-hans").Lang != "zh-Hans" {
		t.Error("Load(Zh-hans) did not normalize to zh-Hans")
	}
}
