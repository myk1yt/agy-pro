package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"agypro/internal/providers"
)

// ProviderSpecJSON is the add-json wire shape (F-PROV): APIKeyEnv is an
// environment-variable NAME — a key VALUE is rejected by ProviderAdd and by
// this parser (isLikelyAPIKey guard, contract §2.9).
type ProviderSpecJSON struct {
	Name      string            `json:"name"`
	BaseURL   string            `json:"baseURL"`
	APIKeyEnv string            `json:"apiKeyEnv"`
	Models    []providers.Model `json:"models"`
}

// ErrProviderSpecNameCarriesCredential is the add-json name gate's refusal. It
// is a named error (errors.Is-able) so the verb, a test, and any future caller
// can tell this refusal from the charset and transport ones without parsing the
// sentence. The sentence names the CLASS only: the refused value never rides in
// it (B2/U8), and it carries no catalog key (CLI errors are not localized).
var ErrProviderSpecNameCarriesCredential = errors.New("provider spec: provider name looks like secret/key material (value hidden)")

// ErrProviderSpecBaseURLCarriesCredential is the add-json base-URL gate's
// refusal (FIX-8d). The name gate above already existed; the URL slot was
// checked for non-emptiness only, so a credential-bearing base URL that the TUI
// provider form and the setup wizard both refuse this round was accepted,
// persisted raw, and rendered raw in the shareable summary.json / --json
// surfaces through its path sub-case. Named and value-free for the same reasons
// as the name sentinel.
var ErrProviderSpecBaseURLCarriesCredential = errors.New("provider spec: baseURL looks like secret/key material (value hidden)")

// ErrProviderSpecModelCarriesCredential is the add-json Models[] gate's refusal
// (FIX-8g). The Models[] array used to pass through unclassified while
// model-add-json, model-update, the TUI model form, and the wizard all refuse a
// credential-shaped model field, so a key-shaped id/family/tag persisted raw
// from this door alone. The sentence names the record class and never a field
// value (or an index that could hint at one).
var ErrProviderSpecModelCarriesCredential = errors.New("provider spec: a model record looks like secret/key material (value hidden)")

// providerSpecModelFieldCarriesCredential reports whether one free-text model
// field carries credential material. It is a delegation to the ONE shared NAME
// authority (providers.NameCarriesCredential), the same predicate
// model-add-json / model-update (cmd/agy/pro_settings.go
// modelSpecValueLooksSecret) and the TUI model form call, so the three ingest
// doors answer identically on every input. The rule set is the twin's: the ID,
// the family, every tag, every allowed tool, and the reasoning effort. The
// effort is a closed-set field at the twin doors, but add-json passes the array
// through without that gate, so a credential pasted there would persist raw;
// classifying it here cannot refuse a legal effort ("low", "high", "default"),
// which is never credential-shaped.
func providerSpecModelFieldCarriesCredential(m providers.Model) bool {
	if providers.NameCarriesCredential(m.ID) {
		return true
	}
	if providers.NameCarriesCredential(m.Family) {
		return true
	}
	for _, tag := range m.Tags {
		if providers.NameCarriesCredential(tag) {
			return true
		}
	}
	for _, tool := range m.Capabilities.AllowedTools {
		if providers.NameCarriesCredential(tool) {
			return true
		}
	}
	return providers.NameCarriesCredential(m.Capabilities.ReasoningEffort)
}

// ParseProviderSpecJSON decodes and guards one non-interactive provider spec.
// APIKeyEnv is an environment-variable NAME — a key VALUE is rejected, and the
// NAME must resolve via os.LookupEnv or match the known env-name hint catalog
// (B4). The provider name is restricted to the shell-safe charset (B5). Errors
// never echo the operands (B2/U8).
//
// The name gate has two arms and the shape arm runs first, the order the TUI
// provider form settled on (screen_provider_form.go validateName): a pasted
// value is a key before it is a naming problem, and the charset admits every
// bare alnum key id, so a charset-first order would report the wrong class for
// exactly the values this gate exists to refuse.
//
// The shape arm is providers.NameCarriesCredential, the SAME authority the TUI
// form door calls (FIX-7 closes the twin door this phase recorded): a
// key-shaped NAME (the planted fixture testfixtures.SKLive: charset-legal,
// SecretShaped false) used to be persisted raw here while every MaskOperand
// name sink echoed it raw and the add-json success line masked it, so the
// operator saw a masked name over a raw on-disk one. The predicate is the
// union of the three shared arms, not a list kept in this file, so this door
// and the TUI door cannot
// drift; the twin-parity pin drives both with one corpus. The gate sits before
// Core.ProviderAdd, so a refused name never reaches UpsertProvider and nothing
// is written.
//
// FIX-8d: the base URL is classified by providers.BaseURLCarriesCredential, the
// SAME shared URL authority the TUI provider form and the setup wizard call.
// This door used to check non-emptiness only, so a credential-bearing base URL
// that both other doors refuse was accepted here, persisted raw, and echoed raw
// by the shareable summary.json / --json surfaces (the path sub-case). The gate
// runs before Core.ProviderAdd, so nothing is written on a refusal.
//
// FIX-8g: every model record is classified with the shared NAME authority over
// the same field set the model-add-json / model-update twins refuse (id, family,
// tags, allowed tools, reasoning effort). The Models[] array used to pass
// through untouched, so a credential-shaped model field persisted raw from this
// door while every other model ingest door refused it. A benign spec is
// unchanged: the gates only read, and the returned ProviderSpec keeps the exact
// decoded bytes of the record.
func ParseProviderSpecJSON(data []byte) (ProviderSpec, error) {
	var raw ProviderSpecJSON
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&raw); err != nil {
		return ProviderSpec{}, fmt.Errorf("invalid provider spec JSON: %w", err)
	}
	env := strings.TrimSpace(raw.APIKeyEnv)
	if env != "" {
		if err := providers.ValidateAPIKeyEnvName(env); err != nil {
			return ProviderSpec{}, err
		}
	}
	spec := ProviderSpec{
		Name:      strings.TrimSpace(raw.Name),
		BaseURL:   strings.TrimSpace(raw.BaseURL),
		APIKeyEnv: env,
		Models:    raw.Models,
	}
	if providers.NameCarriesCredential(spec.Name) {
		return ProviderSpec{}, ErrProviderSpecNameCarriesCredential
	}
	if err := providers.ValidateProviderName(spec.Name); err != nil {
		return ProviderSpec{}, err
	}
	if spec.BaseURL == "" {
		return ProviderSpec{}, fmt.Errorf("provider spec: baseURL is required")
	}
	if providers.BaseURLCarriesCredential(spec.BaseURL) {
		return ProviderSpec{}, ErrProviderSpecBaseURLCarriesCredential
	}
	for _, m := range spec.Models {
		if providerSpecModelFieldCarriesCredential(m) {
			return ProviderSpec{}, ErrProviderSpecModelCarriesCredential
		}
	}
	return spec, nil
}

// readSpecSource loads add-json input from a file path. "-" is handled by the
// CLI layer (it owns stdin); the TUI requires a path.
func readSpecSource(path string, _ LineReader) ([]byte, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return nil, fmt.Errorf("add-json requires a file path or -")
	}
	if p == "-" {
		return nil, fmt.Errorf("stdin specs are read by `agy pro settings providers add-json -`; pass a file path here")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read spec file: %w", err)
	}
	return data, nil
}

// RenderSpec masks nothing here (spec carries no key VALUES) but keeps the
// JSON shape stable for show output.
func RenderSpec(spec ProviderSpec) string {
	b, err := json.Marshal(ProviderSpecJSON{
		Name: spec.Name, BaseURL: spec.BaseURL, APIKeyEnv: spec.APIKeyEnv, Models: spec.Models,
	})
	if err != nil {
		return spec.Name
	}
	return string(b)
}
