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

// DefaultAllowedTools is the grant list a NEWLY added model starts with: the
// runtime tool universe minus run_command. It backs both manual-add doors (the
// setup hub's model form and the CLI wizard's custom-model prompt), so a fresh
// model can read, search and write without a first trip into the tool picker,
// while shell execution stays an explicit opt-in. Order is the picker's row
// order: read (read_file, list_dir), search (grep, glob), write (write_file,
// replace_in_file), then the two per-run network tools. The hub pin
// TestDefaultAllowedToolsMatchesKnownToolList compares this list against the
// form's knownToolList minus run_command, so a tool the runtime grows cannot
// land here by drift alone.
//
// Scope is the ADD path only. An edited model keeps its stored grants
// byte-identically, including an empty list: empty AllowedTools still means
// "every tool permitted" at runtime (Capabilities.ToolPermitted), and this
// default must never be written over an existing record. Provider catalog and
// preset rows keep their own curated read-only sets (presets.go, and the
// auto-discovered rows in wizard.go); they are not derived from this value.
//
// Treat as read-only. Callers copy before they mutate: normalizeTools on the
// form side already returns a fresh slice, and the wizard only joins.
var DefaultAllowedTools = []string{
	"read_file", "list_dir", "grep", "glob",
	"write_file", "replace_in_file",
	"search_web", "read_url_content",
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
//
// ExcludeModels is the user-owned pool-exclusion list (D1): the persisted
// sibling of ExcludeFamilies, consumed through ExclusionPolicy, the single
// exclusion authority. Its nil-vs-empty shape is semantic, not cosmetic: a nil
// slice means the key is ABSENT, so the built-in default policy (IsK3Family)
// still governs the pool; a non-nil empty slice means the user owns an empty
// list, which excludes nothing. For that reason the tag carries no `omitempty`
// (which would collapse the two shapes on write) and the load-time normaliser
// (NormalizeExcludeModels) preserves the shape it read.
//
// A non-nil list must also carry at least one entry that survives the trim: an
// ALL-BLANK list (`["", "  "]`) is refused at load and at the write door, because
// it is a typo'd list rather than a decision, and loading it as the user-owned
// empty list would silently drop the built-in default while excluding nothing.
// "Exclude nothing" is written as an explicit `[]`, which still loads.
//
// ExcludeFamilies keeps its own meaning and stays validated-but-unconsumed this
// session: the fanout ladder still declares it reserved (internal/fanout/cli.go
// rung 4).
type Defaults struct {
	N               int      `json:"n,omitempty"`
	TimeoutSeconds  int      `json:"timeoutSeconds,omitempty"`
	MaxTurns        int      `json:"maxTurns,omitempty"`
	Tools           string   `json:"tools,omitempty"`
	TokenBudget     int      `json:"tokenBudget,omitempty"`
	ExcludeFamilies []string `json:"excludeFamilies,omitempty"`
	// ExcludeModels entries take one of four forms, matched by
	// ExclusionPolicy.Excludes:
	//   - a provider name ("openrouter") excludes every model of that provider;
	//   - a model id ("glm-5") excludes that model under any provider;
	//   - a provider/model pair ("openrouter/glm-5") must match both sides;
	//   - a family entry ("family:<name>") excludes every model whose family
	//     matches <name> case-insensitively. This is the form that makes the
	//     BUILT-IN DEFAULT policy expressible as a list entry, so a first save
	//     of the seeded list cannot silently weaken it: "family:k3" reproduces
	//     the built-in k3 rule (family "k3" OR a boundary-safe "k3" id token)
	//     entry for entry, while a bare "k3" matches only a provider or model
	//     literally named k3 and therefore excludes nothing the built-in rule
	//     excluded. The prefix before the first ':' must be exactly "family"
	//     (case-insensitive) and the remainder must be non-empty; every other
	//     entry keeps its older meaning, including a provider/model pair whose
	//     model id happens to carry a ':'.
	ExcludeModels []string `json:"excludeModels"`
}

// ExclusionPolicy is the single exclusion authority: it answers "is this model
// out of the selectable pool?" for every surface that renders or seats models
// (the swarm seat list, `agy pro models --all`). User is the user-owned list
// copied from Defaults.ExcludeModels; nil means the key was absent and the
// built-in default policy applies, while any non-nil slice (including an empty
// one) means the user owns the list and the built-in default no longer runs.
type ExclusionPolicy struct {
	User []string
}

// ExclusionPolicyFromDefaults builds the exclusion policy from a registry
// defaults block. It copies the slice reference, so it does not normalise: the
// load path already did (normalizeExcludeModels), and Excludes trims every
// entry again so a policy built from a hand-made Defaults behaves the same.
func ExclusionPolicyFromDefaults(d Defaults) ExclusionPolicy {
	return ExclusionPolicy{User: d.ExcludeModels}
}

// UserOwned reports whether the policy reads the user's list instead of the
// built-in default policy.
func (p ExclusionPolicy) UserOwned() bool { return p.User != nil }

// Excludes reports whether the model is excluded from the pool.
//
// With a nil User list it defers to the built-in default predicate
// IsK3Family, reproducing today's dual rule exactly: an explicit family "k3"
// (honored by Model.FamilyName before any id inference) OR a boundary-safe
// "k3" id token.
//
// With a non-nil User list it matches the entries exactly after trimming
// surrounding space:
//   - an entry of the form provider/model (split at the FIRST '/') must match
//     BOTH sides, so a half match excludes nothing. This branch runs first, so
//     every slash-bearing entry keeps the meaning it had before the family form
//     existed;
//   - an entry of the form family:<name> (the prefix before the first ':'
//     equal to "family", case-insensitive) excludes a model whose family
//     matches <name> case-insensitively after trimming, AND ALSO a model whose
//     id carries <name> as a boundary-safe token. The second half is not
//     decoration: the built-in default entry is a FAMILY PREDICATE, and the
//     family operand every caller passes (Model.FamilyName, or an explicit
//     registry family) is inferred from the id when no family is declared, so
//     kimi-k3 reaches this method as family "kimi". Family matching alone would
//     therefore make "family:k3" reproduce only half of the built-in rule, and
//     the first save of the seeded list would drop kimi-k3 and k3-agent-swarm
//     from the pool. With both halves, "family:k3" is the exact list spelling
//     of IsK3Family (pinned by TestExcludeFamilyEntryReproducesBuiltinDefault);
//     the token half only fires when the name is itself a single lowercase
//     alphanumeric token, because a name carrying other runes can never be a
//     token of the id;
//   - an entry equal to providerName excludes every model of that provider;
//   - an entry equal to modelID excludes that model under any provider.
//
// Matching is case-SENSITIVE and exact for provider names and model ids:
// registry model ids and provider names are stored verbatim and are already
// validated to a closed charset at load (ValidateProviderName), so folding case
// here would silently widen a rule the user wrote against an exact slot. Only
// the family form folds case, because model families are folded everywhere else
// (Model.FamilyName, IsK3Family). A slash-bearing model id can only be targeted
// through the provider-qualified form, since a bare entry containing '/' is read
// as provider/model.
func (p ExclusionPolicy) Excludes(providerName, modelID, family string) bool {
	if p.User == nil {
		return IsK3Family(Model{ID: modelID, Family: family})
	}
	for _, raw := range p.User {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if i := strings.Index(entry, "/"); i >= 0 {
			if entry[:i] == providerName && entry[i+1:] == modelID {
				return true
			}
			continue
		}
		if name, ok := familyEntryName(entry); ok {
			if familyEntryMatches(name, modelID, family) {
				return true
			}
			continue
		}
		if entry == providerName || entry == modelID {
			return true
		}
	}
	return false
}

// familyEntryPrefix is the literal prefix that turns a slash-free excludeModels
// entry into a family entry. The comparison is case-insensitive, so "Family:k3"
// and "FAMILY:k3" read as the same form.
const familyEntryPrefix = "family"

// familyEntryName splits a slash-free entry into the family-entry form: it
// returns the name after the first ':' and true when the prefix before that ':'
// is exactly "family" (case-insensitive). The name is returned verbatim; a
// caller that matches must fold it. The empty-name case is the loader's refusal
// (checkExcludeEntry), so callers that already passed the load see a non-empty
// name.
//
// The prefix is RESERVED: a model id literally spelled "family:<anything>"
// cannot be targeted by a bare entry any more (it was excluded by an exact
// model-id match before this form existed); the provider-qualified form
// ("openrouter/family:<anything>") still targets it, since the slash branch runs first.
func familyEntryName(entry string) (string, bool) {
	i := strings.Index(entry, ":")
	if i < 0 || !strings.EqualFold(entry[:i], familyEntryPrefix) {
		return "", false
	}
	return entry[i+1:], true
}

// familyEntryMatches answers one family entry against one model: the family
// operand matches case-insensitively after trimming, or the model id carries the
// name as a boundary-safe token (see ExclusionPolicy.Excludes). A name that is
// not a single lowercase alphanumeric token can only ever match by family,
// because the id tokenizer emits nothing else.
func familyEntryMatches(name, modelID, family string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(family), name) {
		return true
	}
	return exclusionTokenMatches(modelID, strings.ToLower(name))
}

// exclusionTokenMatches reports whether id carries token as a boundary-safe
// token: the lowercase id is split on every non-[a-z0-9] rune and the EXACT
// token is compared, so "k3" matches kimi-k3 and k3-agent-swarm but neither
// k3000 nor glm-k3x. token is expected already lowercased.
//
// It is the generalised twin of HasK3Token (k3.go), which stays the built-in
// default predicate's own mouth: k3.go is outside this unit's file boundary, so
// the two implementations are kept in step by a test that walks the whole
// k3_test.go table and requires "family:k3" to answer exactly what IsK3Family
// answers (TestExcludeFamilyEntryReproducesBuiltinDefault).
func exclusionTokenMatches(id, token string) bool {
	if token == "" {
		return false
	}
	for _, tok := range strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if tok == token {
			return true
		}
	}
	return false
}

// ExcludedMarker is the single user-visible suffix an excluded row carries. The
// swarm seat list and `agy pro models --all` render this exact string, so it is
// the one place the wording lives; it stays short because it renders as a row
// suffix.
//
// Catalog boundary (S7-C7 convention, decisions.md 2026-10-22 S7-C7 entry):
// the wording stays outside the twelve i18n catalogs on purpose. Revisit when
// the P5 unified settings surface exists.
func ExcludedMarker() string { return "excluded (exclusion list)" }

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

// checkExcludeEntry applies the per-entry rule of the ONE exclusion-entry
// authority (excludeEntries) to one excludeModels entry: a Cc or Cf rune in the
// RAW value, interior whitespace in the trimmed value, and the family: form's
// non-empty remainder. It returns the trimmed entry; an entry that trims to ""
// is not an error here, because the two doors treat a blank entry differently
// (the registry loader drops it, the write door refuses it), so that decision
// belongs to the caller (excludeEntries).
//
// The control-rune check reads the RAW entry, before the trim, exactly like the
// sibling defaults-field checks (tools, excludeFamilies) read their stored
// value. Order matters for the C1 whitespace controls: U+0085 NEL is both a Cc
// rune and White_Space, so a trim-first order would silently erase it instead of
// refusing the file (the terminal-rendering hazard the boundary rule exists for
// is about the stored bytes, not about whatever survives the trim).
//
// The Cf half (S7-C9) is the same widening the model fields carry
// (hasInvisibleFormatRunes): the bidi overrides and isolates, the zero-width
// joiner, and the BOM have no visible identity, so an entry decorated with one
// is an entry the operator cannot read back in the editor and cannot match on
// purpose. Refusing it at BOTH doors keeps an invisible entry from loading as a
// rule that silently excludes nothing while looking non-empty.
//
// The error texts name the field class and the accepted entry forms only and
// never embed the offending entry: a misdirected paste may be a key.
func checkExcludeEntry(raw string) (string, error) {
	if hasControlRunes(raw) || hasInvisibleFormatRunes(raw) {
		return "", fmt.Errorf("defaults: excludeModels contains control characters")
	}
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return "", nil
	}
	if strings.ContainsFunc(entry, unicode.IsSpace) {
		return "", fmt.Errorf("defaults: excludeModels entries must not contain whitespace (name one provider, one model id, provider/model, or family:<name>)")
	}
	// The family form needs a name after the colon: "family:" would otherwise
	// load as an entry that can never match anything, which reads as a rule the
	// user wrote and silently excludes nothing.
	if !strings.Contains(entry, "/") {
		if name, ok := familyEntryName(entry); ok && strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("defaults: excludeModels: a family entry needs a name after the colon")
		}
	}
	return entry, nil
}

// ValidateExcludeEntry vets ONE proposed excludeModels entry with the per-entry
// rules of the shared authority (checkExcludeEntry) and returns the trimmed entry
// a write would store. It exists so a front-end door (the hub's model exclusion
// editor) cannot compose a list the next load would refuse: unlike
// NormalizeExcludeModels it REFUSES an entry that is empty after the trim
// instead of dropping it, because an editor must not silently discard what the
// user typed. The errors are value-free for the same reason the loader's are.
func ValidateExcludeEntry(raw string) (string, error) {
	entry, err := checkExcludeEntry(raw)
	if err != nil {
		return "", err
	}
	if entry == "" {
		return "", fmt.Errorf("defaults: excludeModels entries must not be empty")
	}
	return entry, nil
}

// excludeEntries is the ONE exclusion-entry authority: the registry loader
// (NormalizeExcludeModels, called by Registry.Validate) and the settings write
// door (ValidateExcludeModels, called by settings.ParseExcludeModels) both run
// this function, so the two doors cannot disagree about which entry shapes are
// legal.
//
// Per entry: a Cc or Cf rune in the RAW value is refused, the entry is trimmed,
// interior whitespace is refused, and a family: entry with no name after the
// colon is refused. Each entry names a provider, a model id, a provider/model
// pair, or a family (family:<name>), so interior whitespace can never match and
// a nameless family entry can never match either.
//
// The two doors differ in exactly one way, and refuseBlank is that difference:
// the loader DROPS an entry that is blank after the trim (a trailing comma in a
// hand-edited file loads as if it were not there), while the write door REFUSES
// it (an editor must not silently discard what the user typed).
//
// Both doors refuse a NON-NIL list whose every entry is blank (S7-C3). Such a
// list used to load as a user-owned EMPTY list, which silently dropped the
// built-in k3 default while excluding nothing, so a typo'd list weakened the pool
// policy with no load-time signal. An explicit `[]` is not that shape: it is a
// decision to exclude nothing and still loads. The nil-vs-empty distinction is
// preserved exactly as read: a nil list stays nil (the key was absent, so the
// built-in default governs) and a non-nil list stays non-nil (the user owns it),
// because a write that cannot tell the two apart would resurrect the default.
//
// The list-level check below is the loader's wording of that refusal; the write
// door refuses the FIRST blank entry on its way there, so its message is the
// per-entry one. Both refuse the list, which is what a caller observes.
func excludeEntries(entries []string, refuseBlank bool) ([]string, error) {
	if entries == nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, err := checkExcludeEntry(raw)
		if err != nil {
			return nil, err
		}
		if entry == "" {
			if refuseBlank {
				return nil, fmt.Errorf("defaults: excludeModels entries must not be empty")
			}
			continue
		}
		out = append(out, entry)
	}
	if len(entries) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("defaults: excludeModels: every entry is empty after trimming whitespace; name a provider, a model id, provider/model, or family:<name>, or drop the key to use the built-in default")
	}
	return out, nil
}

// NormalizeExcludeModels is the registry LOADER's door to the exclusion-entry
// authority (excludeEntries): trim each entry, drop the entries that are blank
// after the trim, and refuse a Cc or Cf rune, an interior whitespace rune, a
// family: entry with no name, and a non-nil list whose every entry is blank.
// The nil-vs-empty shape is preserved (see excludeEntries). Registry.Validate
// calls it, so a registry file can never carry an entry the write door refuses.
func NormalizeExcludeModels(entries []string) ([]string, error) {
	return excludeEntries(entries, false)
}

// ValidateExcludeModels is the WRITE DOOR's door to the same authority: the rules
// of NormalizeExcludeModels, except that an entry that is blank after the trim is
// REFUSED instead of dropped, because an editor must not silently discard what
// the user typed. settings.ParseExcludeModels delegates here, which is what makes
// the hub's exclusion editor and the next registry load agree on every row shape
// (S7-C2: the family: remainder rule; S7-C3: the all-blank list; S7-C9: the Cf
// runes).
func ValidateExcludeModels(entries []string) ([]string, error) {
	return excludeEntries(entries, true)
}

// hasDefaultsDisplayControlRunes reports runes that could hide, split, or
// reorder a defaults value when it is rendered outside the TUI frame strip.
func hasDefaultsDisplayControlRunes(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) >= 0
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
// defaults block strings (tools, excludeFamilies, excludeModels), and the
// closed provider name charset (ValidateProviderName) are checked below. No
// message may embed the offending value: a control byte in the error text would
// re-introduce the exact rendering hazard.
func (r *Registry) Validate() error {
	// Defaults strings also refuse Cf/Zl/Zp: the fanout defaults notice has no
	// TUI frame strip, and those classes can hide or split a transcript line.
	// The errors name the field class only and never embed the offending value.
	if hasDefaultsDisplayControlRunes(r.Defaults.Tools) {
		return fmt.Errorf("defaults: tools contains control characters")
	}
	for _, fam := range r.Defaults.ExcludeFamilies {
		if hasDefaultsDisplayControlRunes(fam) {
			return fmt.Errorf("defaults: excludeFamilies contains control characters")
		}
	}
	// excludeModels (D1): the user-owned pool-exclusion list. The loader door of
	// the shared exclusion-entry authority (NormalizeExcludeModels) trims each
	// entry, drops the ones that are empty after the trim (a trailing comma in a
	// hand-edited file), and refuses the load on a Cc or Cf rune, an internal
	// space, a family: entry with no name, or a non-nil list whose every entry is
	// blank, since an entry is a single token naming a provider, a model id, a
	// provider/model pair, or a family. Errors name the field class only and never
	// embed the offending entry (a misdirected paste may be a key). The
	// nil-vs-empty shape survives the normalisation on purpose: nil (absent key)
	// keeps the built-in default policy, non-nil empty is the user's own empty
	// list. See Defaults.ExcludeModels.
	exclModels, err := NormalizeExcludeModels(r.Defaults.ExcludeModels)
	if err != nil {
		return err
	}
	r.Defaults.ExcludeModels = exclModels

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
// letters after stripping any "vendor/" prefix (e.g. "deepseek-chat" -> "deepseek").
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
