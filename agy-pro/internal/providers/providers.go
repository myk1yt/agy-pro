// Package providers loads the external-model provider registry used by
// `agy pro fanout`. The registry stores only environment-variable NAMES for
// API keys; actual secrets are read from the process environment at call time
// and are never persisted or logged.
package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// EnvProviders overrides the registry file location.
const EnvProviders = "AGY_PRO_PROVIDERS"

// EnvHome overrides the agy-pro state directory (default ~/.agy-pro).
const EnvHome = "AGY_PRO_HOME"

// Capabilities describes features supported by a model.
type Capabilities struct {
	SupportsVision   bool     `json:"supportsVision,omitempty"`
	SupportsVideo    bool     `json:"supportsVideo,omitempty"`
	SupportsThinking bool     `json:"supportsThinking,omitempty"`
	ReasoningEffort  string   `json:"reasoningEffort,omitempty"`
	SupportsTools    bool     `json:"supportsTools,omitempty"`
	AllowedTools     []string `json:"allowedTools,omitempty"`
}

// EffectiveReasoningEffort returns the configured reasoning effort, defaulting to "default"
// if thinking is enabled and no explicit effort is set.
func (c Capabilities) EffectiveReasoningEffort() string {
	if !c.SupportsThinking {
		return ""
	}
	if c.ReasoningEffort != "" {
		return c.ReasoningEffort
	}
	return "default"
}

// CanUseTools reports whether function calling / tools are enabled for this model.
func (c Capabilities) CanUseTools() bool {
	return c.SupportsTools
}

// Summary renders the enabled capability flags as a comma-separated list, e.g.
// "tools, vision, thinking(high)". It is the canonical renderer for the wizard
// summary, `agy pro models`, and the launcher menu so that every surface shows
// the same flags the gateway acts on. A model with no optional capability
// renders as "none".
//
// K1: the reasoning effort is registry-persisted free-form text (nothing on
// the add-json / hand-edit write path validates it), so the effort routes
// through the registry-slot display authority MaskRegistryValue here at the
// composition: a key-shaped value pasted into the reasoningEffort slot renders
// first-5+**** at every sink that calls this renderer, while benign efforts
// render byte-identical. A LOCATION-shaped effort ("./<key>", a URL or
// Windows-drive shape) is not a legitimate effort and the slot has no
// path-readability requirement, so it masks too (MaskRegistryValue strips the
// markers and masks the secret-shaped core), and the top trim defeats a padded
// plant. The API path is unaffected — it reads the raw Capabilities.ReasoningEffort
// field directly (fanout mapReasoningEffort, bridge NormalizeReasoningEffort),
// never Summary().
func (c Capabilities) Summary() string {
	parts := make([]string, 0, 4)
	if c.SupportsTools {
		parts = append(parts, "tools")
	}
	if c.SupportsVision {
		parts = append(parts, "vision")
	}
	if c.SupportsVideo {
		parts = append(parts, "video")
	}
	if c.SupportsThinking {
		parts = append(parts, "thinking("+MaskRegistryValue(c.EffectiveReasoningEffort())+")")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// CapabilitySummary renders this model's capability flags; see Capabilities.Summary.
func (m Model) CapabilitySummary() string {
	return m.Capabilities.Summary()
}

// ToolPermitted reports whether a specific tool name is allowed for this model.
func (c Capabilities) ToolPermitted(toolName string) bool {
	if !c.SupportsTools {
		return false
	}
	if len(c.AllowedTools) == 0 {
		return true
	}
	for _, t := range c.AllowedTools {
		if strings.EqualFold(t, toolName) {
			return true
		}
	}
	return false
}

// Model is one model offered by a provider.
type Model struct {
	ID              string       `json:"id"`
	Family          string       `json:"family,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	Enabled         *bool        `json:"enabled,omitempty"` // nil means enabled
	ContextLength   int          `json:"contextLength,omitempty"`
	MaxOutputTokens int          `json:"maxOutputTokens,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
}

// IsEnabled reports whether the model participates in routine rotation.
func (m Model) IsEnabled() bool { return m.Enabled == nil || *m.Enabled }

// HasTag reports whether the model carries the tag (case-insensitive).
func (m Model) HasTag(tag string) bool {
	for _, t := range m.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// FamilyName returns the explicit family or one inferred from the model id.
func (m Model) FamilyName() string {
	if strings.TrimSpace(m.Family) != "" {
		return strings.ToLower(strings.TrimSpace(m.Family))
	}
	return InferFamily(m.ID)
}

// Provider is an OpenAI-compatible endpoint.
type Provider struct {
	Name      string  `json:"name"`
	BaseURL   string  `json:"baseURL"`
	APIKeyEnv string  `json:"apiKeyEnv,omitempty"`
	Models    []Model `json:"models"`
}

// Endpoint returns the chat completions URL.
func (p Provider) Endpoint() string {
	return strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
}

// KeyValue returns the API key read from the environment ("" if unset or if
// the provider needs no key). The value must never be logged.
//
// Resolution order: exact process env, exact persistent entries (HKCU then
// HKLM), then BOM-polluted name variants (see envkeys.go). An exact match
// always wins, so a clean name holding a stale value keeps winning; use
// AuditProviderKeys to surface that conflict. Resolved values are returned
// as-is and never promoted into the process environment: caching them there
// used to leak store-only keys into every child process this shim spawns
// (security finding S-3).
func (p Provider) KeyValue() string {
	if p.APIKeyEnv == "" {
		return ""
	}
	val, _ := p.KeyValueWithOrigin()
	return val
}

// KeyValueWithOrigin resolves the provider's key like KeyValue and additionally
// reports where the value came from (KeyOriginNone when unresolved). It performs
// no caching, so it is safe for audit paths that must observe the persistent
// stores directly.
func (p Provider) KeyValueWithOrigin() (string, KeyOrigin) {
	if p.APIKeyEnv == "" {
		return "", KeyOriginNone
	}
	v, origin, _ := resolveEnvValueWithStore(p.APIKeyEnv, defaultEnvStore)
	return v, origin
}

// KeyAvailable reports whether the provider can authenticate: either it
// declares no key variable, or the variable is set and non-empty.
func (p Provider) KeyAvailable() bool {
	return p.APIKeyEnv == "" || p.KeyValue() != ""
}

// Defaults are registry-level default settings for fanout runs.
type Defaults struct {
	N               int      `json:"n,omitempty"`
	TimeoutSeconds  int      `json:"timeoutSeconds,omitempty"`
	MaxTurns        int      `json:"maxTurns,omitempty"`
	Tools           string   `json:"tools,omitempty"`
	TokenBudget     int      `json:"tokenBudget,omitempty"`
	ExcludeFamilies []string `json:"excludeFamilies,omitempty"`
}

// Registry is the parsed providers file.
type Registry struct {
	Providers []Provider `json:"providers"`
	Defaults  Defaults   `json:"defaults,omitempty"`
	// Path is the file the registry was loaded from (not serialised).
	Path string `json:"-"`
}

// UnsetVar names a provider whose key environment variable is not set.
type UnsetVar struct {
	Provider string `json:"provider"`
	Env      string `json:"env"`
}

// Home returns the agy-pro state directory.
func Home() (string, error) {
	if v := os.Getenv(EnvHome); v != "" {
		return v, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".agy-pro"), nil
}

// DefaultPath returns the registry path (AGY_PRO_PROVIDERS or
// ~/.agy-pro/providers.json).
func DefaultPath() (string, error) {
	if v := os.Getenv(EnvProviders); v != "" {
		return v, nil
	}
	h, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "providers.json"), nil
}

// Load reads the registry from DefaultPath. A missing file yields an error
// satisfying errors.Is(err, os.ErrNotExist).
func Load() (*Registry, error) {
	p, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(p)
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// providerNameRe is the closed provider-name charset (B5): a name is one
// shell-safe token starting with an alphanumeric, so a name interpolated into
// a run_command line cannot smuggle quotes, spaces, $-expansions, or other
// metacharacters (indirect prompt injection / confused-deputy class). Letters
// and digits are Unicode classes (a CJK provider name is legitimate and the
// doctor screen pins one loading; the shell-dangerous characters are all
// ASCII, so the ban list is unchanged).
var providerNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._-]{0,63}$`)

// ValidateProviderName vets a provider NAME operand (registry ops + settings +
// the load-time name loop in Registry.Validate). The error never echoes the
// operand (B2/U8: a misdirected paste may be a key).
func ValidateProviderName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if !providerNameRe.MatchString(name) {
		return fmt.Errorf("provider name must be 1-64 runes, start with a letter or digit, then only letters, digits, dot, underscore, hyphen (no spaces or shell metacharacters)")
	}
	return nil
}

// LoadFrom reads and validates a registry file.
func LoadFrom(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("providers file %s: invalid JSON: %w", path, err)
	}
	r.Path = path
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("providers file %s: %w", path, err)
	}
	return &r, nil
}

// validateBaseURL enforces the endpoint transport rule (security finding S-6):
// bearer keys transit this URL on every probe and chat request, so https:// is
// required except for loopback hosts (127.0.0.1, localhost, ::1) where http://
// is acceptable for local development servers.
func validateBaseURL(baseURL string) error {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		// FIX-8h: this sentence is VALUE-FREE, and it has to be. url.Error
		// embeds the operand twice over: %q quotes the whole parsed string, and
		// the inner error can embed a fragment of it as well ("invalid port
		// \":...\" after host"). Redacting the text through NewRedactor only
		// looked safe: its patterns are the identity on the underscore-head
		// credential class (skRe requires a hyphen, and "sk_live_<16>" carries an
		// underscore), so a key pasted into an unparseable URL rode into every
		// consumer of this error: the wizard refusal (wizard.go:110 appends
		// MaskOperand, also identity on that class), the loader
		// (providers.go:393), the mutation op (registry_ops.go:157), and the TUI
		// message line (screen_provider_form.go:1084/:1121, MaskOperand of the
		// sentence). The shared URL authority now refuses every unparseable value
		// at the three ingest doors, so this branch fires only for a record read
		// back from disk or handed to the op layer directly; dropping the reason
		// text costs that operator a syntax hint and guarantees no
		// operand-derived byte can ride along.
		return errors.New("baseURL is not a valid URL (value hidden)")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("baseURL must use https:// (http:// is only allowed for loopback hosts)")
	default:
		return errors.New("baseURL must start with http:// or https://")
	}
}

// isLoopbackHost reports whether host names a loopback address.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Validate checks structural rules. It never echoes apiKeyEnv values because a
// mistaken paste of a real key must not leak into logs; provider names and model
// IDs are registry slots and pass through MaskSlot, the single registry-slot
// display authority, for the same reason (B2/U8). MaskSlot's first stage is
// MaskOperand, so a benign name or id renders byte-identically, while the
// underscore-head credential class ("sk_live_...", "tok_...", "AKIA_...") that
// the operand masker cannot classify no longer rides raw into a load error.
//
// Load-time control-rune rule (round-5 R9/seat3-R3, extended round-6 F1): a
// registry file with disk-borne C0/C1 control bytes must fail here, at the
// registry boundary, rather than load junk that later renders raw through
// sinks outside the TUI frame strip (the legacy line-mode settings panel, CLI
// echoes). Model fields reuse checkModelFields and the model loop also checks
// capabilities.reasoningEffort; the provider-level free-text fields, the
// defaults block strings (tools, excludeFamilies), and the closed provider
// name charset (ValidateProviderName) are checked below. No message may embed
// the offending value: a control byte in the error text would re-introduce
// the exact rendering hazard.
func (r *Registry) Validate() error {
	// Round-6 F1: the defaults block strings ride CLI echoes that have no TUI
	// frame strip (the fanout defaults-drift notice, the legacy panel), so a
	// disk-borne control rune there would execute in the operator's terminal.
	// The errors name the field class only and never embed the offending byte.
	if hasControlRunes(r.Defaults.Tools) {
		return fmt.Errorf("defaults: tools contains control characters")
	}
	for _, fam := range r.Defaults.ExcludeFamilies {
		if hasControlRunes(fam) {
			return fmt.Errorf("defaults: excludeFamilies contains control characters")
		}
	}
	seen := map[string]bool{}
	for i, p := range r.Providers {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("providers[%d]: name is required", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("provider %q is defined twice", MaskSlot(p.Name))
		}
		seen[p.Name] = true
		if strings.Contains(p.Name, "/") {
			return fmt.Errorf("provider %q: name must not contain '/'", MaskSlot(p.Name))
		}
		if hasControlRunes(p.Name) {
			return fmt.Errorf("providers[%d]: name contains control characters", i)
		}
		// Round-6 seat1-R8: enforce the closed provider-name charset at load,
		// not only at mutation ops and the wizard. The helper's error never
		// echoes the operand. Without this, a name with spaces, quotes, or $
		// loads and reaches non-TUI sinks.
		if err := ValidateProviderName(p.Name); err != nil {
			return fmt.Errorf("providers[%d]: %w", i, err)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("provider %q: baseURL is required", MaskSlot(p.Name))
		}
		if hasControlRunes(p.BaseURL) {
			return fmt.Errorf("provider %q: baseURL contains control characters", MaskSlot(p.Name))
		}
		if err := validateBaseURL(p.BaseURL); err != nil {
			return fmt.Errorf("provider %q: %w", MaskSlot(p.Name), err)
		}
		if p.APIKeyEnv != "" && hasControlRunes(p.APIKeyEnv) {
			return fmt.Errorf("provider %q: apiKeyEnv contains control characters", MaskSlot(p.Name))
		}
		if p.APIKeyEnv != "" && !envNameRe.MatchString(p.APIKeyEnv) {
			return fmt.Errorf("provider %q: apiKeyEnv must be an environment variable NAME, not a key (value hidden)", MaskSlot(p.Name))
		}
		mseen := map[string]bool{}
		for j, m := range p.Models {
			if strings.TrimSpace(m.ID) == "" {
				return fmt.Errorf("provider %q: models[%d].id is required", MaskSlot(p.Name), j)
			}
			if mseen[m.ID] {
				return fmt.Errorf("provider %q: model %q is listed twice", MaskSlot(p.Name), MaskSlot(m.ID))
			}
			mseen[m.ID] = true
			if err := checkModelFields(p.Name, m); err != nil {
				return err
			}
			// Convergence round: the load-time rule covers the whole invisible
			// class (Cc + Cf), not only Cc. A Cf rune carries no visible
			// identity (bidi overrides and isolates, zero-width spaces and
			// joiners, the BOM), so a value decorated with one must not persist
			// at rest either; checkModelFields above already reports the Cc
			// class, so this check reports only the shape it cannot see. The
			// wording stays the established field-class sentence and never
			// echoes the offending value.
			if kind := invisibleModelField(m); kind != "" {
				return fmt.Errorf("provider %q: %s contains control characters", MaskSlot(p.Name), kind)
			}
			// Round-6 F1: reasoningEffort is registry-persisted free text that
			// reaches CLI echoes with no frame strip (CapabilitySummary via
			// `agy pro status` / `agy pro models --all`, the wizard list, the
			// fanout defaults notice), so a disk-borne control rune there must
			// fail the load, with the same non-echoing error style as
			// checkModelFields. The check carries the same Cc + Cf widening as
			// the model fields above.
			if hasControlRunes(m.Capabilities.ReasoningEffort) || hasInvisibleFormatRunes(m.Capabilities.ReasoningEffort) {
				return fmt.Errorf("provider %q: reasoningEffort contains control characters", MaskSlot(p.Name))
			}
		}
	}
	return nil
}

// hasInvisibleFormatRunes reports whether s carries an invisible FORMAT rune
// (the Cf class: the bidi overrides and isolates, the zero-width space and
// joiners, the byte order mark). It is the load-time companion of the policy's
// invisible class: a Cf rune carries no visible identity, so a registry value
// decorated with one cannot persist at rest. The combining marks (Mn, Me) are
// deliberately NOT covered here: an NFD-encoded name legitimately carries them,
// and the policy folds them at classification and display time.
func hasInvisibleFormatRunes(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) >= 0
}

// invisibleModelField returns the field class of the first model field that
// carries an invisible FORMAT rune (see hasInvisibleFormatRunes), or "" when
// the model is clean. It names exactly the fields checkModelFields names, so
// the two checks report the same classes; reasoningEffort is checked by the
// model loop separately.
func invisibleModelField(m Model) string {
	switch {
	case hasInvisibleFormatRunes(m.ID):
		return "model id"
	case hasInvisibleFormatRunes(m.Family):
		return "model family"
	}
	for _, t := range m.Tags {
		if hasInvisibleFormatRunes(t) {
			return "model tag"
		}
	}
	for _, tool := range m.Capabilities.AllowedTools {
		if hasInvisibleFormatRunes(tool) {
			return "model tool name"
		}
	}
	return ""
}

// Provider returns the named provider.
func (r *Registry) Provider(name string) (*Provider, bool) {
	for i := range r.Providers {
		if r.Providers[i].Name == name {
			return &r.Providers[i], true
		}
	}
	return nil, false
}

// UnsetEnvVars lists providers (that have at least one enabled model) whose
// API key environment variable is unset or empty. Used by setup/doctor.
func (r *Registry) UnsetEnvVars() []UnsetVar {
	var out []UnsetVar
	for _, p := range r.Providers {
		if p.APIKeyEnv == "" || p.KeyValue() != "" {
			continue
		}
		out = append(out, UnsetVar{Provider: p.Name, Env: p.APIKeyEnv})
	}
	return out
}

// UnsetEnvNames returns the sorted, de-duplicated unset variable names.
func (r *Registry) UnsetEnvNames() []string {
	set := map[string]bool{}
	for _, u := range r.UnsetEnvVars() {
		set[u.Env] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Redactor returns a function that masks every API key value currently set in
// the environment for this registry, plus common bearer/sk- token shapes.
func (r *Registry) Redactor() func(string) string {
	var secrets []string
	if r != nil {
		for _, p := range r.Providers {
			if v := p.KeyValue(); len(v) >= 6 {
				secrets = append(secrets, v)
			}
		}
	}
	return NewRedactor(secrets...)
}

var (
	bearerRe = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	skRe     = regexp.MustCompile(`\b(sk|pk|key|tok)-[A-Za-z0-9_-]{12,}`)
	// googleAPIKeyRe is the Google API key shape ("AIza" + 35 chars). agy itself
	// hands a GEMINI_API_KEY/GOOGLE_API_KEY value to whatever endpoint it talks
	// to, so this shape shows up in loopback captures and provider error echoes
	// as often as the OpenAI-family shapes below.
	googleAPIKeyRe = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	// Generic credential shapes (S-4/F2 extension): material the registry
	// does not know about - AWS access key IDs (the AKIA and ASIA twins),
	// secret-key assignments, Authorization header values and PEM private key
	// blocks - so a leaked file or provider error body still leaves the
	// process masked. These extend, never replace, the registered-key masking
	// above.
	awsKeyIDRe   = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	awsSecretRe  = regexp.MustCompile(`(?i)(aws_secret_access_key\s*[=:]\s*)[^\s'"` + "`" + `]+`)
	authValueRe  = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(?:bearer\s+)?[A-Za-z0-9._~+/=-]{8,}`)
	privateKeyRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
)

// NewRedactor builds a redaction function for the given secret values.
func NewRedactor(secrets ...string) func(string) string {
	// Longest first so overlapping secrets are masked fully.
	list := append([]string(nil), secrets...)
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	return func(s string) string {
		for _, sec := range list {
			if sec != "" {
				s = strings.ReplaceAll(s, sec, "[REDACTED]")
			}
		}
		s = bearerRe.ReplaceAllString(s, "${1}[REDACTED]")
		s = skRe.ReplaceAllString(s, "[REDACTED]")
		s = googleAPIKeyRe.ReplaceAllString(s, "[REDACTED]")
		s = awsKeyIDRe.ReplaceAllString(s, "[REDACTED]")
		s = awsSecretRe.ReplaceAllString(s, "${1}[REDACTED]")
		s = authValueRe.ReplaceAllString(s, "${1}[REDACTED]")
		s = privateKeyRe.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
		return s
	}
}

// InferFamily derives a model family from a model id: the lowercase leading
// letters after stripping any "vendor/" prefix (e.g. "qwen3.8-flash" -> "qwen").
func InferFamily(id string) string {
	id = strings.ToLower(id)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	end := 0
	for end < len(id) && id[end] >= 'a' && id[end] <= 'z' {
		end++
	}
	if end == 0 {
		return id
	}
	return id[:end]
}

// WriteFileAtomic writes data to path via a temp file in the same directory
// followed by a rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	_ = os.Chmod(tmpName, perm)
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ErrNotConfigured is returned by helpers when no registry file exists.
var ErrNotConfigured = errors.New("providers registry not found; run `agy pro setup` or copy providers.example.json to ~/.agy-pro/providers.json")
