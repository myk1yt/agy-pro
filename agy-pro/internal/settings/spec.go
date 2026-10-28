package settings

import (
	"encoding/json"
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

// ParseProviderSpecJSON decodes and guards one non-interactive provider spec.
// APIKeyEnv is an environment-variable NAME — a key VALUE is rejected, and the
// NAME must resolve via os.LookupEnv or match the known env-name hint catalog
// (B4). The provider name is restricted to the shell-safe charset (B5). Errors
// never echo the operands (B2/U8).
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
	if err := providers.ValidateProviderName(spec.Name); err != nil {
		return ProviderSpec{}, err
	}
	if spec.BaseURL == "" {
		return ProviderSpec{}, fmt.Errorf("provider spec: baseURL is required")
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
