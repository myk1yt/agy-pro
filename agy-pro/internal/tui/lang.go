package tui

import "agypro/internal/i18n"

// lang.go holds the package-level active bundle of the setup TUI. The app
// shell (app.go) loads it once at startup through SetLanguage and again on
// every language-screen choice; screens that bind their bundle at
// construction (providers, doctor, help, language) read it through Bundle so
// they follow the shell's language without importing the shell state.

// activeBundle is the bundle every freshly constructed screen binds. nil
// until the first SetLanguage; Bundle resolves the nil case to English.
var activeBundle *i18n.Bundle

// SetLanguage loads the bundle for code, stores it as the active bundle, and
// returns it. Unknown codes follow the i18n fallback chain to English. The
// language screen's onChoose calls this after persisting the choice so the
// next constructed screen binds the new language.
func SetLanguage(code string) *i18n.Bundle {
	activeBundle = i18n.Load(code)
	return activeBundle
}

// Bundle returns the active bundle, defaulting to English before the first
// SetLanguage (standalone screen construction in tests and the settings
// panel degrade path).
func Bundle() *i18n.Bundle {
	if activeBundle == nil {
		return i18n.Load("en")
	}
	return activeBundle
}
