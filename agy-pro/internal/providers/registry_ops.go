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

// maskNameOperand renders one provider NAME operand for an error sentence.
// Three rules, in order, and every branch is mask-only:
//
//  1. MaskOperand keeps its rendering whenever it REWRITES the value. Its
//     location-shaped and embedded-pattern arms are the module-wide echo
//     contract the settings and CLI pins measure byte-for-byte, so a name
//     operand must not lose that rendering.
//  2. A separator-carrying operand (",", ";", whitespace) is a composite list,
//     not a provider name: the charset rejects those, and the batch-N seed
//     layer (FlagSeedOperands + MaskSentence) renders a composite at both doors.
//     A composite that layer already catches keeps its bytes here, so one
//     renderer owns it and the sentences the settings and CLI echo pins measure
//     do not move. A composite it does NOT catch is masked whole here: that is
//     the underscore-head class again, which the seed layer's predicate does not
//     cover, and MaskSlot is mask-or-identity, so a benign composite still
//     renders readable.
//  3. Otherwise the value is a single name token that MaskOperand read as
//     benign. It is read a second time through the registry-slot authority
//     MaskSlot, the only arm that sees the underscore-head key class (the "_"
//     breaks the opaque-run test and no prefix head matches). The result is
//     taken only when it is an actual mask (MaskSecret is the one masking form
//     and it ends in "****"); a slot authority REWRITE that only strips a marker
//     ("./backups" renders "backups") is not a mask, so a benign location
//     operand keeps its bytes.
//
// Measured on the class: MaskOperand is the identity on
// "sk_live_0123456789abcdef", "tok_a1b2c3d4e5f6g7h8i9j0", "pk_live_...",
// "key_..." and "AKIA_...", while MaskSlot renders each first-5 + "****"
// ("sk_li****"), so before FIX-8b every one of them printed raw from these
// sentences.
func maskNameOperand(name string) string {
	if out := MaskOperand(name); out != name {
		return out
	}
	if strings.ContainsAny(name, ",; \t\n\r") {
		// A composite: keep its bytes only when the door's own masker already
		// catches it, so the two layers stay one renderer per value and the
		// sentences the settings and CLI pins measure do not move. The probe is
		// the door's own call shape (Sentence, seeded with the operand's tokens).
		if quoted := `"` + name + `"`; MaskSentence(quoted, FlagSeedOperands(name)...) != quoted {
			return name
		}
		return MaskSlot(name)
	}
	if masked := MaskSlot(name); masked != name && strings.HasSuffix(masked, "****") {
		return masked
	}
	return name
}

// UpsertProvider inserts or replaces a provider by name (case-insensitive,
// matching the wizard's upsert at wizard.go). The provider and the resulting
// registry are validated before the mutation is kept; on error the registry is
// left unchanged. APIKeyEnv must be an environment-variable NAME: a key VALUE
// is rejected with an error that never echoes the value (A4). Names are
// restricted to the shell-safe charset (B5), and every NAME operand in these
// compositions renders through maskNameOperand (B2/U8): this door has no
// credential gate ahead of it, so an underscore-head key reaches these
// sentences and masks only on the slot-authority read (FIX-8b).
func UpsertProvider(reg *Registry, p Provider) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	name := strings.TrimSpace(p.Name)
	if err := ValidateProviderName(name); err != nil {
		return err
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("provider %q: baseURL is required", maskNameOperand(name))
	}
	if err := validateBaseURL(p.BaseURL); err != nil {
		return fmt.Errorf("provider %q: %w", maskNameOperand(name), err)
	}
	if env := strings.TrimSpace(p.APIKeyEnv); env != "" {
		if err := ValidateAPIKeyEnvName(env); err != nil {
			return fmt.Errorf("provider %q: %w", maskNameOperand(name), err)
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

// ErrProviderNameCarriesCredential is RenameProvider's new-name gate refusal.
// It is a named error (errors.Is-able) so the CLI verb, the settings Front-end,
// a test, and any future caller can tell this refusal from the charset and
// not-found ones without parsing the sentence. The sentence names the CLASS
// only: the refused value never rides in it (B2/U8), and it carries no catalog
// key (CLI errors are not localized).
var ErrProviderNameCarriesCredential = errors.New("provider name gate: new provider name looks like secret/key material (value hidden)")

// RenameProvider renames a registry entry. Key material is untouched (the env
// var name and its value belong to the environment, not the file). Errors when
// oldName is missing or newName is already taken by a different entry. newName
// must not carry credential material (NameCarriesCredential) and must satisfy
// the shell-safe provider-name charset (B5); every NAME operand in the
// not-found and already-exists sentences renders through maskNameOperand
// (B2/U8, FIX-8b). oldName is ungated, so a pasted key in the old-name slot
// reaches the not-found sentence, and MaskOperand is the identity on the
// underscore-head class; the already-exists slot cannot see that class at all
// (the credential gate admits no value MaskSlot would change), so its rendering
// is the identity by construction and the pin records that reachability.
//
// The credential gate is FIX-7E: the charset alone admits the whole
// underscore-head key family ("sk_live_0123456789abcdef",
// "tok_a1b2c3d4e5f6g7h8i9j0"), and MaskOperand is the identity on it, so
// `providers rename old sk_live_0123456789abcdef` persisted the key-shaped name
// raw and every name sink rendered it raw. The gate runs BEFORE the charset
// check, the order settings.ParseProviderSpecJSON and the TUI provider form's
// validateName settled on: a pasted value is a key before it is a naming
// problem, and the charset admits every bare alnum key id, so a charset-first
// order would report the wrong class for exactly the values this gate exists to
// refuse. The predicate is the shared NAME authority, so this door and the TUI
// form, the wizard, the add-json parser and the model verbs refuse one set. A
// refused rename returns before any mutation, so the registry keeps its old name.
func RenameProvider(reg *Registry, oldName, newName string) error {
	if reg == nil {
		return fmt.Errorf("registry is nil")
	}
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("rename requires both old and new provider names")
	}
	if NameCarriesCredential(newName) {
		return ErrProviderNameCarriesCredential
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
		return fmt.Errorf("provider %q not found", maskNameOperand(oldName))
	}
	for i := range reg.Providers {
		if i != idx && strings.EqualFold(reg.Providers[i].Name, newName) {
			return fmt.Errorf("provider %q already exists", maskNameOperand(newName))
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
// allowed; the registry keeps a valid empty provider list. The name operand is
// ungated like RenameProvider's oldName, so the not-found sentence renders it
// through maskNameOperand (B2/U8, FIX-8b).
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
	return fmt.Errorf("provider %q not found", maskNameOperand(name))
}
