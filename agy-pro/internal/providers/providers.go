// Package providers loads the external-model provider registry used by
// `agy pro fanout`. The registry stores only environment-variable NAMES for
// API keys; actual secrets are read from the process environment at call time
// and are never persisted or logged.
package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	SupportsTools    bool     `json:"supportsTools,omitempty"`
	AllowedTools     []string `json:"allowedTools,omitempty"`
}

// CanUseTools reports whether function calling / tools are enabled for this model.
func (c Capabilities) CanUseTools() bool {
	return c.SupportsTools
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
func (p Provider) KeyValue() string {
	if p.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(p.APIKeyEnv)
}

// KeyAvailable reports whether the provider can authenticate: either it
// declares no key variable, or the variable is set and non-empty.
func (p Provider) KeyAvailable() bool {
	return p.APIKeyEnv == "" || os.Getenv(p.APIKeyEnv) != ""
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

// Validate checks structural rules. It never echoes apiKeyEnv values because a
// mistaken paste of a real key must not leak into logs.
func (r *Registry) Validate() error {
	seen := map[string]bool{}
	for i, p := range r.Providers {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("providers[%d]: name is required", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("provider %q is defined twice", p.Name)
		}
		seen[p.Name] = true
		if strings.Contains(p.Name, "/") {
			return fmt.Errorf("provider %q: name must not contain '/'", p.Name)
		}
		if strings.TrimSpace(p.BaseURL) == "" {
			return fmt.Errorf("provider %q: baseURL is required", p.Name)
		}
		if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
			return fmt.Errorf("provider %q: baseURL must start with http:// or https://", p.Name)
		}
		if p.APIKeyEnv != "" && !envNameRe.MatchString(p.APIKeyEnv) {
			return fmt.Errorf("provider %q: apiKeyEnv must be an environment variable NAME, not a key (value hidden)", p.Name)
		}
		mseen := map[string]bool{}
		for j, m := range p.Models {
			if strings.TrimSpace(m.ID) == "" {
				return fmt.Errorf("provider %q: models[%d].id is required", p.Name, j)
			}
			if mseen[m.ID] {
				return fmt.Errorf("provider %q: model %q is listed twice", p.Name, m.ID)
			}
			mseen[m.ID] = true
		}
	}
	return nil
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
		if p.APIKeyEnv == "" || os.Getenv(p.APIKeyEnv) != "" {
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
