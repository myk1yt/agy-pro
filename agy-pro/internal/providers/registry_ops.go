package providers

import (
	"fmt"
	"strings"
)

// Registry-entry mutation helpers (F-PROV, contract §2.9 / research GAP rows
// 15-17). These edit the in-memory Registry only; persistence stays with the
// caller via WriteFileAtomic (0600). Key VALUES never live in the registry —
// APIKeyEnv carries an environment-variable NAME, and a value-looking string
// is rejected here so every entry path shares one guard (isLikelyAPIKey
// precedent, presets.go).

// LooksLikeAPIKey reports whether val resembles an API key/secret token rather
// than an environment variable name. Exported so settings.Core can apply the
// same guard the wizard uses, without duplicating the heuristic.
func LooksLikeAPIKey(val string) bool {
	return isLikelyAPIKey(val)
}

// UpsertProvider inserts or replaces a provider by name (case-insensitive,
// matching the wizard's upsert at wizard.go). The provider and the resulting
// registry are validated before the mutation is kept; on error the registry is
// left unchanged. APIKeyEnv must be an environment-variable NAME: a key VALUE
// is rejected with an error that never echoes the value (A4).
func UpsertProvider(reg *Registry, p Provider) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("provider %q: name must not contain '/'", name)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("provider %q: baseURL is required", name)
	}
	if err := validateBaseURL(p.BaseURL); err != nil {
		return fmt.Errorf("provider %q: %w", name, err)
	}
	if env := strings.TrimSpace(p.APIKeyEnv); env != "" {
		if isLikelyAPIKey(env) {
			return fmt.Errorf("provider %q: apiKeyEnv must be an environment variable NAME, not a key value (value hidden)", name)
		}
		if !envNameRe.MatchString(env) {
			return fmt.Errorf("provider %q: apiKeyEnv must be an environment variable NAME, not a key (value hidden)", name)
		}
		p.APIKeyEnv = env
	}
	p.Name = name

	saved := make([]Provider, len(reg.Providers))
	copy(saved, reg.Providers)
	replaced := false
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			reg.Providers[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		reg.Providers = append(reg.Providers, p)
	}
	if err := reg.Validate(); err != nil {
		reg.Providers = saved
		return err
	}
	return nil
}

// RenameProvider renames a registry entry. Key material is untouched (the env
// var name and its value belong to the environment, not the file). Errors when
// oldName is missing or newName is already taken by a different entry.
func RenameProvider(reg *Registry, oldName, newName string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("rename requires both old and new provider names")
	}
	if strings.Contains(newName, "/") {
		return fmt.Errorf("provider %q: name must not contain '/'", newName)
	}
	idx := -1
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, oldName) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("provider %q not found", oldName)
	}
	for i := range reg.Providers {
		if i != idx && strings.EqualFold(reg.Providers[i].Name, newName) {
			return fmt.Errorf("provider %q already exists", newName)
		}
	}
	reg.Providers[idx].Name = newName
	if err := reg.Validate(); err != nil {
		reg.Providers[idx].Name = oldName
		return err
	}
	return nil
}

// DeleteProvider removes a registry entry ONLY. It never touches environment
// or registry key values, never deletes files, and leaves the env var holding
// the key in place (the user may reuse it). Deleting the last provider is
// allowed; the registry keeps a valid empty provider list.
func DeleteProvider(reg *Registry, name string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("delete requires a provider name")
	}
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			reg.Providers = append(reg.Providers[:i], reg.Providers[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("provider %q not found", name)
}
