package providers

import (
	"errors"
	"fmt"
	"os"
	"regexp"
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

// isKnownKeyEnvName reports whether env is one of the built-in provider hint
// catalog key-variable names (the known env-name catalog; B4).
func isKnownKeyEnvName(env string) bool {
	for _, h := range KnownProviders() {
		if h.KeyEnv != "" && h.KeyEnv == env {
			return true
		}
	}
	return false
}

// cleanEnvNameRe matches the conventional env-name shape this codebase and its
// fixtures use: UPPER_SNAKE with at least one underscore. Such short names are
// treated as forward-referenced variables (usable before they are exported).
// Anything else is ambiguous — a short env-name-shaped credential (the B4
// bypass class) fits the same charset — and must prove it is a name via
// os.LookupEnv or the known hint catalog.
var cleanEnvNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_[A-Z0-9_]+$`)

// ValidateAPIKeyEnvName vets an APIKeyEnv operand at the registry/settings
// boundary (B4). Rules, in order:
//
//  1. it must be a syntactic environment-variable NAME (envNameRe), never a
//     key value;
//  2. a candidate that PROVES it is a name is accepted as-is: it resolves in
//     the process environment (os.LookupEnv) or matches the known env-name
//     hint catalog;
//  3. credential material is then rejected outright (isLikelyAPIKey, which
//     now also matches known credential shapes such as AWS access key IDs);
//  4. short clean UPPER_SNAKE names pass without proof 2 so a provider can
//     reference a variable it is about to export; anything else ambiguous is
//     rejected.
//
// This is the fix for the isLikelyAPIKey bypass: an AWS access key ID
// (AKIA + 16 = 20 chars) fits the name charset and the old length heuristics,
// resolves as neither a variable nor a catalog name, and is therefore
// rejected at every entry path instead of being persisted as an "env name".
// Errors never echo the operand (B2/U8: a misdirected paste may be a key).
func ValidateAPIKeyEnvName(env string) error {
	env = strings.TrimSpace(env)
	if env == "" {
		return errors.New("apiKeyEnv is empty")
	}
	if !envNameRe.MatchString(env) {
		return errors.New("apiKeyEnv must be an environment variable NAME, not a key (value hidden)")
	}
	if _, ok := os.LookupEnv(env); ok {
		return nil
	}
	if isKnownKeyEnvName(env) {
		return nil
	}
	if isLikelyAPIKey(env) {
		return errors.New("apiKeyEnv must be an environment variable NAME, not a key value (value hidden)")
	}
	if len(env) < 20 && cleanEnvNameRe.MatchString(env) {
		return nil
	}
	return errors.New("apiKeyEnv must name a set environment variable or a known provider key variable (value hidden)")
}

// UpsertProvider inserts or replaces a provider by name (case-insensitive,
// matching the wizard's upsert at wizard.go). The provider and the resulting
// registry are validated before the mutation is kept; on error the registry is
// left unchanged. APIKeyEnv must be an environment-variable NAME: a key VALUE
// is rejected with an error that never echoes the value (A4). Names are
// restricted to the shell-safe charset (B5) and every echoed operand passes
// through MaskOperand (B2/U8).
func UpsertProvider(reg *Registry, p Provider) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	name := strings.TrimSpace(p.Name)
	if err := ValidateProviderName(name); err != nil {
		return err
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("provider %q: baseURL is required", MaskOperand(name))
	}
	if err := validateBaseURL(p.BaseURL); err != nil {
		return fmt.Errorf("provider %q: %w", MaskOperand(name), err)
	}
	if env := strings.TrimSpace(p.APIKeyEnv); env != "" {
		if err := ValidateAPIKeyEnvName(env); err != nil {
			return fmt.Errorf("provider %q: %w", MaskOperand(name), err)
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
// oldName is missing or newName is already taken by a different entry. newName
// must satisfy the shell-safe provider-name charset (B5); every echoed operand
// passes through MaskOperand (B2/U8).
func RenameProvider(reg *Registry, oldName, newName string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("rename requires both old and new provider names")
	}
	if err := ValidateProviderName(newName); err != nil {
		return err
	}
	idx := -1
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, oldName) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("provider %q not found", MaskOperand(oldName))
	}
	for i := range reg.Providers {
		if i != idx && strings.EqualFold(reg.Providers[i].Name, newName) {
			return fmt.Errorf("provider %q already exists", MaskOperand(newName))
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
	return fmt.Errorf("provider %q not found", MaskOperand(name))
}
