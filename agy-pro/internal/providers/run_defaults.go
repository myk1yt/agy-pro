package providers

// Run-default knobs the provider-add doors bind into the providers.json
// defaults block. The fanout CLI ladder reads that block and binds every run
// with it (fanout/cli.go rung order: preset, then defaults, then the engine
// fallback), so a registry WITHOUT the block silently falls back to the
// engine's short budget (300s wall clock) no matter how long the user intends
// a session to run.
const (
	// DefaultRunTimeoutSeconds is the registry-level fanout wall-clock budget
	// for long sessions: 2 hours (60*120 seconds).
	DefaultRunTimeoutSeconds = 7200
	// DefaultRunMaxTurns is the registry-level per-seat agent turn ceiling.
	// It matches the engine fallback (fanout defaultFanoutMaxTurns), so
	// seeding it pins the value the engine would already choose instead of
	// leaving the slot to an undocumented fallback.
	DefaultRunMaxTurns = 500
)

// EnsureRunDefaults fills a Defaults value's missing run knobs with the
// registry-level defaults above. A knob the user already set (non-zero) is
// never moved; N, Tools, TokenBudget, ExcludeFamilies, and ExcludeModels are
// left untouched so this helper cannot weaken an exclusion policy.
func EnsureRunDefaults(d Defaults) Defaults {
	if d.TimeoutSeconds <= 0 {
		d.TimeoutSeconds = DefaultRunTimeoutSeconds
	}
	if d.MaxTurns <= 0 {
		d.MaxTurns = DefaultRunMaxTurns
	}
	return d
}

// DefaultEnvNameFor returns the API key environment-variable name for a
// provider name: the catalog key variable when the name resolves to a known
// provider, otherwise the upper snake form with an "_API_KEY" suffix. It is
// the exported twin of the wizard's defaultEnvFor so every ingest door offers
// the same derivation.
func DefaultEnvNameFor(name string) string { return defaultEnvFor(name) }

// PersistUserEnvFor stores one environment variable in the persistent user
// environment where the host supports it (HKCU\Environment on Windows through
// the advapi32 registry API, never a command line). It is the exported twin
// of the wizard's persist seam so non-wizard doors can offer the same key
// persist under the same consent-gate contract. Callers gate on
// runtime.GOOS == "windows" exactly like the wizard does.
func PersistUserEnvFor(name, value string) error { return persistWindowsUserEnv(name, value) }
