package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// InteractiveSetupWizard guides the user through setting up or updating a provider
// in ~/.agy-pro/providers.json in a friendly, conversational manner.
func InteractiveSetupWizard(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	var console *os.File
	if f, ok := in.(*os.File); ok {
		console = f
	}

	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Multi-Model Provider Setup Wizard")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "Configure OpenAI-compatible LLM endpoints for multi-model fanout.")
	fmt.Fprintln(out, "API keys are stored securely in your environment, never in providers.json.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Known providers: "+providerLabelList())
	if aliases := providerAliasList(); aliases != "" {
		fmt.Fprintln(out, "Aliases: "+aliases)
	}
	fmt.Fprintln(out)

	// Step 1: Provider Name
	provName, isEOF := promptLine(reader, out, "Provider name", "")
	provName = strings.TrimPrefix(strings.TrimSpace(provName), "\ufeff")
	if isEOF && provName == "" {
		return errors.New("input closed (EOF); setup aborted")
	}
	if provName == "" {
		return errors.New("provider name cannot be empty")
	}
	// FIX-7B: the registry-slot authority classifies the NAME before the
	// charset does. The charset accepts the underscore key family
	// ("sk_live_0123456789abcdef"), MaskOperand is the identity on it, and the
	// wizard persists the name through its own upsert (not registry_ops.go),
	// from where it reaches the derived env name
	// ("SK_LIVE_0123456789ABCDEF_API_KEY") and every listing, doctor, and
	// report sink. A name that carries credential material is refused here,
	// fail closed, before the URL prompt, before the probe, and before any
	// write; the sentence names the field and never the value.
	if err := wizardNameRefused(provName); err != nil {
		return err
	}
	// B5 data-layer completeness: the wizard persists to the same registry as
	// the registry-ops path, so names must satisfy the same shell-safe charset
	// (a name later lands in plugin run_command lines).
	if err := ValidateProviderName(provName); err != nil {
		return err
	}
	// B2/U8: every later echo of the name routes through the registry-slot mask
	// (MaskSlot, whose first stage is MaskOperand), so even a hand-edited
	// registry value of the refused class cannot render raw here.
	provLabel := wizardProvLabel(provName)

	// A name that resolves to a catalog entry (via an alias such as "gemini")
	// keeps the name the user typed but takes the catalog base URL, key variable,
	// and presets.
	hint, knownProvider := LookupProviderHint(provName)
	if knownProvider && !strings.EqualFold(provName, hint.Name) {
		fmt.Fprintf(out, "Using %s settings for %q (base URL, %s).\n", hint.Name, provLabel, hint.KeyEnv)
	}

	// Step 2: Base URL
	defaultURL := defaultBaseURLFor(provName)
	if defaultURL == "" {
		// Unregistered provider: nothing was invented for it, so show the
		// expected shape and make the user type a real endpoint (empty input
		// is rejected below).
		fmt.Fprintf(out, "No built-in default Base URL for %q. Example: https://api.openai.com/v1 (an OpenAI-compatible /v1 endpoint)\n", provLabel)
	}
	baseURL, isEOF := promptLine(reader, out, "OpenAI-compatible Base URL", defaultURL)
	baseURL = strings.TrimPrefix(strings.TrimSpace(baseURL), "\ufeff")
	if isEOF && baseURL == "" {
		return errors.New("input closed (EOF); setup aborted")
	}
	if baseURL == "" {
		return errors.New("base URL cannot be empty")
	}
	// FIX-7B: the transport rule (validateBaseURL) is scheme and host only, so a
	// credential parked in the path, the query, the fragment, or the userinfo
	// used to pass here, echo raw at the edit prompt and the completion summary,
	// and persist. The same registry-slot authority the TUI form's base-URL arm
	// uses rejects it before the probe and before any write.
	if err := wizardBaseURLRefused(baseURL); err != nil {
		return err
	}
	if err := validateBaseURL(baseURL); err != nil {
		// B2/U8: the operand may be a misdirected key paste; never echo it raw.
		return fmt.Errorf("%w: %s", err, MaskOperand(baseURL))
	}

	// Step 3: API Key & Environment Variable Name
	defaultEnv := defaultEnvFor(provName)
	existingVal := os.Getenv(defaultEnv)
	// C5/U8 + FIX-7B: a credential-shaped provider name passes the B5 charset
	// (the name guard above now refuses that class at the prompt, so this path is
	// the hand-edited-registry case), and defaultEnvFor only case-folds the name
	// and swaps separators for "_", so the derived name embeds the pasted key
	// verbatim ("AKIAIOSFODNN7EXAMPLE" -> "AKIAIOSFODNN7EXAMPLE_API_KEY"). The
	// registry-slot mask decides the class, not MaskOperand alone: MaskOperand is
	// the identity on the underscore family ("sk_live_0123456789abcdef"), which
	// is exactly the class this mission closes. Echo sites withhold that derived
	// name instead of leaking it. A name the user typed as an env variable still
	// renders through MaskOperand.
	withholdDerivedEnv := wizardWithholdsDerivedEnv(provName)
	envEcho := func(env string) string {
		if withholdDerivedEnv && env == defaultEnv {
			return "(withheld: derived from a key-shaped name)"
		}
		return MaskOperand(env)
	}

	fmt.Fprintln(out)
	var keyValue string
	var apiKeyEnv string = defaultEnv

	if existingVal != "" {
		fmt.Fprintf(out, "Detected existing environment variable %s.\n", envEcho(defaultEnv))
		keyInput, keyErr := promptSecret(reader, out, console, fmt.Sprintf("API Key (press Enter to keep %s, or paste new key)", envEcho(defaultEnv)), "")
		if keyErr != nil {
			// B6: Ctrl+C aborts — never silently keep the existing key.
			return fmt.Errorf("setup aborted: %w", keyErr)
		}
		if keyInput == "" {
			keyValue = existingVal
			fmt.Fprintf(out, "Using existing value from %s: %s\n", envEcho(defaultEnv), MaskSecret(existingVal))
		} else {
			keyValue = keyInput
		}
	} else {
		if knownProvider && hint.KeyURL != "" {
			fmt.Fprintf(out, "Get a key at: %s\n", hint.KeyURL)
		}
		fmt.Fprintf(out, "Enter API Key for %s (will be stored in environment variable %s):\n", provLabel, envEcho(defaultEnv))
		keyInput, keyErr := promptSecret(reader, out, console, "API Key (sk-... or leave blank to skip)", "")
		if keyErr != nil {
			return fmt.Errorf("setup aborted: %w", keyErr)
		}
		keyValue = keyInput
	}

	// Smart check: Did user paste a custom environment variable name instead of a key?
	if keyValue != "" && !isLikelyAPIKey(keyValue) && envNameRe.MatchString(keyValue) {
		if envVal := os.Getenv(keyValue); envVal != "" {
			apiKeyEnv = keyValue
			keyValue = envVal
			fmt.Fprintf(out, "Using environment variable %s (loaded from current session).\n", envEcho(apiKeyEnv))
		}
	}
	// U8: every confirmation of key material shows the canonical mask only.
	if keyValue != "" {
		fmt.Fprintf(out, "API Key recorded: %s\n", MaskSecret(keyValue))
	}
	// normalizeEnvName (not a leading-only TrimPrefix) because U+FEFF is not
	// whitespace: a trailing BOM pasted with the name would otherwise survive
	// and be written back to the registry as a fresh polluted entry.
	apiKeyEnv = normalizeEnvName(apiKeyEnv)

	// Persist API key if provided
	if keyValue != "" && apiKeyEnv != "" {
		_ = os.Setenv(apiKeyEnv, keyValue)
		if runtime.GOOS == "windows" && !isRunningTests() {
			if err := persistWindowsUserEnv(apiKeyEnv, keyValue); err != nil {
				fmt.Fprintf(out, "Warning: could not persist %s to Windows user environment: %v\n", envEcho(apiKeyEnv), err)
			} else {
				fmt.Fprintf(out, "✅ Saved %s to Windows User Environment.\n", envEcho(apiKeyEnv))
			}
		} else if runtime.GOOS == "windows" {
			// In test environment, skip persisting to system registry
		} else {
			fmt.Fprintf(out, "Set %s in current session. To persist across shells, add to ~/.bashrc or ~/.zshrc:\n", envEcho(apiKeyEnv))
			fmt.Fprintf(out, "  export %s=\"...\"\n", envEcho(apiKeyEnv))
		}
	} else if apiKeyEnv != "" {
		keyValue = os.Getenv(apiKeyEnv)
	}

	// Step 4: Model Configuration
	presets := GetModelPresets(provName)
	remoteModels := FetchRemoteModels(baseURL, keyValue)

	// If no predefined presets exist, synthesize presets from discovered remote models
	if len(presets) == 0 && len(remoteModels) > 0 {
		for i, rm := range remoteModels {
			if i >= 6 {
				break
			}
			// The discovered ID is endpoint-borne (untrusted) and it is also the
			// registry ID the row will carry, so the description embeds the same
			// masked form the menu renders: a credential-shaped ID cannot ride raw
			// into the preset list through its own annotation.
			presets = append(presets, ModelPreset{
				ID:              rm,
				Family:          InferFamily(rm),
				Description:     wizardModelLabel(rm) + " (auto-discovered)",
				Recommended:     i == 0,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"general", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			})
		}
	}

	var models []Model
	configuredSet := make(map[string]bool)

	for {
		fmt.Fprintln(out)
		fmt.Fprintf(out, "--- Model Configuration (%s) ---\n", provLabel)

		var selectedModel Model
		hasSelection := false

		if len(presets) > 0 {
			fmt.Fprintln(out, "Available Model Presets:")
			for i, p := range presets {
				tag := ""
				if configuredSet[p.ID] {
					tag = " [Configured]"
				} else if p.Recommended {
					tag = " [Recommended]"
				}
				// The preset row is a registry-name render like every other model-ID
				// sink: MaskSlot is identity on a benign catalog or discovered ID and
				// first-5+**** on the underscore class (the description is already
				// masked at synthesis for a discovered row).
				fmt.Fprintf(out, "  %d) %s (%s)%s\n", i+1, wizardModelLabel(p.ID), p.Description, tag)
			}
			customOpt := len(presets) + 1
			fmt.Fprintf(out, "  %d) Custom Model ID (enter manually)\n", customOpt)

			choiceStr, isEOF := promptLine(reader, out, fmt.Sprintf("Select model (1-%d)", customOpt), "1")
			if isEOF && len(models) == 0 {
				return errors.New("input closed (EOF); setup aborted")
			}
			choiceNum, _ := strconv.Atoi(choiceStr)
			if choiceNum >= 1 && choiceNum <= len(presets) {
				p := presets[choiceNum-1]
				caps := p.Capabilities
				if caps.SupportsThinking && caps.ReasoningEffort == "" {
					caps.ReasoningEffort = "default"
				}
				selectedModel = Model{
					ID:              p.ID,
					Family:          p.Family,
					Tags:            p.Tags,
					ContextLength:   p.ContextLength,
					MaxOutputTokens: p.MaxOutputTokens,
					Capabilities:    caps,
				}
				hasSelection = true
			} else if choiceNum == customOpt || strings.EqualFold(choiceStr, "c") || strings.EqualFold(choiceStr, "custom") {
				// Proceed to custom prompt
			} else {
				// User might have typed custom model ID directly
				if choiceStr != "" && choiceStr != "1" {
					m, err := promptCustomModel(reader, out, choiceStr)
					if err != nil {
						return err
					}
					selectedModel = m
					hasSelection = true
				}
			}
		}

		if !hasSelection {
			customID, isEOF := promptLine(reader, out, "Model ID (e.g. gpt-4o, deepseek-chat)", "")
			if (isEOF || customID == "") && len(models) > 0 {
				break
			}
			if customID == "" {
				fmt.Fprintln(out, "Model ID cannot be empty.")
				continue
			}
			m, err := promptCustomModel(reader, out, customID)
			if err != nil {
				return err
			}
			selectedModel = m
			hasSelection = true
		}

		// Prevent duplicate models
		if configuredSet[selectedModel.ID] {
			fmt.Fprintf(out, "Model %q is already configured for %s.\n", wizardModelLabel(selectedModel.ID), provLabel)
			continue
		}

		// Live Connection Probe Loop
	probeLoop:
		for {
			fmt.Fprintf(out, "\n[Live Probe] Testing endpoint %s connection with model %q...\n", endpointHost(baseURL), wizardModelLabel(selectedModel.ID))
			latency, probeErr := ProbeEndpoint(baseURL, keyValue, selectedModel.ID)
			if probeErr == nil {
				fmt.Fprintf(out, "✅ Connection succeeded! Latency: %dms\n", latency)
				models = append(models, selectedModel)
				configuredSet[selectedModel.ID] = true
				break probeLoop
			}

			// FIX-M: the transcript echo is a sink of its own, so it routes
			// through the same sentence masker (identity for a benign error).
			fmt.Fprintf(out, "❌ Connection test failed: %v\n", maskProbeError(probeErr, baseURL))
			if isTimeoutErr(probeErr) {
				fmt.Fprintln(out, "[Notice] Gateway response timed out. The model may be cold-starting or queueing. You can retry immediately or save anyway.")
			}

			fmt.Fprintln(out, "Options:")
			fmt.Fprintln(out, "  1) Save model configuration anyway [Recommended]")
			fmt.Fprintln(out, "  2) Retry test immediately")
			fmt.Fprintln(out, "  3) Edit Base URL or API Key")
			fmt.Fprintln(out, "  4) Discard model")

			choice, isEOF := promptLine(reader, out, "Choice (1-4)", "1")
			choice = strings.ToLower(strings.TrimSpace(choice))
			if isEOF && choice == "" {
				choice = "1"
			}

			switch choice {
			case "1", "save":
				models = append(models, selectedModel)
				configuredSet[selectedModel.ID] = true
				fmt.Fprintf(out, "Saved model %q anyway.\n", wizardModelLabel(selectedModel.ID))
				break probeLoop
			case "2", "retry", "r":
				continue probeLoop
			case "3", "edit", "e":
				// FIX-J: the bracketed default is the CURRENT base URL, and
				// validateBaseURL accepts a userinfo URL, so the raw value used to
				// print here in captured or piped runs. The display default routes
				// through MaskBaseURL (the same authority as the completion summary,
				// FIX-G), while the accept-default return stays RAW: the wizard writes
				// what this prompt returns, so masking the returned value would
				// persist the mask. A benign URL masks to itself, so its prompt line
				// stays byte-identical.
				newURL, _ := promptLineDisplay(reader, out, "Base URL", MaskBaseURL(baseURL), baseURL)
				newURL = strings.TrimSpace(newURL)
				// FIX-7B: this prompt validated nothing, so a credential-bearing
				// replacement typed here reached the probe and the write. A TYPED
				// replacement now passes the same guard as the step-2 prompt. An
				// accepted default returns the current, already-vetted value (its
				// display copy is the mask), so accepting it re-runs nothing.
				if newURL != "" && newURL != baseURL {
					if err := wizardBaseURLRefused(newURL); err != nil {
						return err
					}
					baseURL = newURL
				}
				newKey, keyErr := promptSecret(reader, out, console, "API Key", "keep current")
				if keyErr != nil {
					// B6: Ctrl+C aborts setup instead of silently keeping.
					return fmt.Errorf("setup aborted: %w", keyErr)
				}
				newKey = strings.TrimSpace(newKey)
				if newKey != "" && !strings.EqualFold(newKey, "keep current") {
					if !isLikelyAPIKey(newKey) && envNameRe.MatchString(newKey) {
						if envVal := os.Getenv(newKey); envVal != "" {
							apiKeyEnv = newKey
							keyValue = envVal
							fmt.Fprintf(out, "Using environment variable %s (loaded from current session).\n", envEcho(apiKeyEnv))
						} else {
							keyValue = newKey
							fmt.Fprintf(out, "API Key recorded: %s\n", MaskSecret(keyValue))
							_ = os.Setenv(apiKeyEnv, keyValue)
							if runtime.GOOS == "windows" && !isRunningTests() {
								_ = persistWindowsUserEnv(apiKeyEnv, keyValue)
							}
						}
					} else {
						keyValue = newKey
						fmt.Fprintf(out, "API Key recorded: %s\n", MaskSecret(keyValue))
						_ = os.Setenv(apiKeyEnv, keyValue)
						if runtime.GOOS == "windows" && !isRunningTests() {
							_ = persistWindowsUserEnv(apiKeyEnv, keyValue)
						}
					}
				}
				continue probeLoop
			case "4", "discard", "d":
				fmt.Fprintf(out, "Discarded model %q.\n", wizardModelLabel(selectedModel.ID))
				break probeLoop
			default:
				fmt.Fprintf(out, "Discarded model %q.\n", wizardModelLabel(selectedModel.ID))
				break probeLoop
			}
		}

		more, isEOF := promptLine(reader, out, fmt.Sprintf("Add another model for %s? (y/N)", provLabel), "N")
		if isEOF || (!strings.EqualFold(more, "y") && !strings.EqualFold(more, "yes")) {
			break
		}
	}

	if len(models) == 0 {
		return errors.New("no models configured; setup aborted")
	}

	// K-2 pre-write guard: the per-prompt guard only sees the values the user
	// typed, and the wizard also collects rows from the preset catalog and from
	// the endpoint's model list. Classify every free-text field of every
	// collected model once more, before anything is marshalled, so key material
	// cannot reach providers.json through a row the prompts never showed. A
	// refusal aborts the setup with the registry untouched and the value
	// unechoed.
	if err := wizardModelFieldsRefused(models); err != nil {
		return err
	}

	// Step 5: Save to registry
	prov := Provider{
		Name:      provName,
		BaseURL:   baseURL,
		APIKeyEnv: apiKeyEnv,
		Models:    models,
	}

	targetPath, err := DefaultPath()
	if err != nil {
		return err
	}

	reg, err := LoadFrom(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			reg = &Registry{
				Defaults: Defaults{
					N:              3,
					TimeoutSeconds: 120,
					MaxTurns:       10,
					Tools:          "read,search",
				},
			}
		} else {
			return fmt.Errorf("failed to load existing registry: %w", err)
		}
	}

	// Upsert provider
	updated := false
	for i, p := range reg.Providers {
		if strings.EqualFold(p.Name, prov.Name) {
			reg.Providers[i] = prov
			updated = true
			break
		}
	}
	if !updated {
		reg.Providers = append(reg.Providers, prov)
	}

	if err := reg.Validate(); err != nil {
		return fmt.Errorf("validation error: %w", err)
	}

	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := WriteFileAtomic(targetPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to save registry to %s: %w", targetPath, err)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  ✅ Provider Setup Complete!")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintf(out, "Provider        : %s\n", provLabel)
	// FIX-G: the summary is the last echo of the URL, and validateBaseURL
	// accepts a userinfo URL ("https://user:<key>@host/v1"), so the raw value
	// used to echo in the line-mode degrade path. MaskBaseURL is the layered
	// URL mask the TUI twin uses (tui maskBaseURL, screen_providers.go, kept in
	// parity by its cross-package pin), so the wizard and the TUI render the
	// same input the same way. A benign URL has no userinfo, no
	// credential-shaped query or fragment and no credential-shaped token, so
	// the masker returns it unchanged and the summary line stays byte-identical.
	fmt.Fprintf(out, "Base URL        : %s\n", MaskBaseURL(baseURL))
	keyStatus := "not set; export it before use"
	if keyValue != "" {
		keyStatus = "saved in environment, " + MaskSecret(keyValue)
	}
	fmt.Fprintf(out, "API Key Env     : %s (%s)\n", envEcho(apiKeyEnv), keyStatus)
	fmt.Fprintf(out, "Config File     : %s\n", targetPath)
	fmt.Fprintf(out, "Configured Models (%d):\n", len(models))
	for i, m := range models {
		ctxDisplay := fmt.Sprintf("%dk", m.ContextLength/1000)
		if m.ContextLength <= 0 {
			ctxDisplay = "server-max"
		} else if m.ContextLength >= 1048576 {
			ctxDisplay = fmt.Sprintf("%dM", m.ContextLength/1048576)
		}
		fmt.Fprintf(out, "  %d. %-32s [family: %s, context: %s, capabilities: %s]\n",
			i+1, DisplayName(wizardModelLabel(m.ID), provLabel), MaskFamilyName(m), ctxDisplay, m.CapabilitySummary())
	}
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "Ready to use! The gateway bridge activates whenever an external model is targeted:")
	fmt.Fprintf(out, "  Native agy   : agy -m %q\n", DisplayName(wizardModelLabel(models[0].ID), provLabel))
	fmt.Fprintln(out, "                 (set AGY_PRO_BRIDGE=1 to expose every model in the /model picker)")
	fmt.Fprintf(out, "  Fanout swarm : agy pro fanout --models %q --prompt \"your prompt\"\n", provLabel+"/"+wizardModelLabel(models[0].ID))
	fmt.Fprintln(out, "  List models  : agy pro models")
	fmt.Fprintln(out, "===========================================================")
	return nil
}

// wizardSecretRefusal is the wizard door's non-echoing refusal sentence: it
// names the field class, never the value, in the same "(value hidden)" idiom
// the registry validators use for a misdirected key paste.
func wizardSecretRefusal(field string) error {
	return fmt.Errorf("the %s looks like secret material and cannot be saved (value hidden)", field)
}

// wizardNameRefused is the wizard door's provider-NAME guard. The wizard's name
// prompt ran the charset check only, and that charset accepts the underscore
// credential family ("sk_live_0123456789abcdef", "tok_a1b2c3d4e5f6g7h8i9j0"):
// MaskOperand is the identity on it, so the raw name was persisted through the
// wizard's own upsert and echoed at the listing, doctor, and report sinks while
// its derived env name leaked the class as upper snake case.
//
// The class is the registry-slot authority's (NameCarriesCredential: the shape
// test, or MaskOperand/MaskSlot changing the value), so the name door and the
// TUI form's name arm cannot disagree. The refusal is the existing sentence
// idiom: the field is named, the value never is.
func wizardNameRefused(name string) error {
	if !NameCarriesCredential(name) {
		return nil
	}
	return wizardSecretRefusal("provider name")
}

// wizardProvLabel renders a provider name for every transcript sink of the
// wizard. MaskSlot is the registry-slot display authority (a superset of
// MaskOperand: its first stage), so the underscore family the name prompt
// refuses still renders first-5+**** if a hand-edited registry or a future
// ingest door feeds one in. A benign name is byte-identical.
func wizardProvLabel(name string) string {
	return MaskSlot(name)
}

// wizardModelLabel renders a registry-borne model ID for every transcript sink
// of the wizard. A model ID is a registry name like the provider name: it is
// the `models[].id` value of providers.json, and the wizard carries it in its
// own models / selectedModel state (from the prompt, from the preset catalog,
// or from the endpoint's discovered model list).
//
// The eight ID sinks used MaskOperand, which is the identity on the
// underscore-head class ("sk_live_0123456789abcdef": no shared prefix, and the
// "_" breaks the opaque-run test), so a hand-edited or discovered
// credential-shaped ID rendered raw in the duplicate notice, the live-probe
// line, the saved/discarded notices, the summary row, and the two ready-to-use
// commands. MaskSlot is the registry-slot display authority (a superset of
// MaskOperand: its first stage), the same one wizardProvLabel uses and the one
// the report/status sinks render model IDs through. A benign ID is
// byte-identical.
func wizardModelLabel(id string) string {
	return MaskSlot(id)
}

// wizardWithholdsDerivedEnv reports whether the env name the wizard derives
// from the provider name must be withheld from the transcript. defaultEnvFor
// only case-folds and separator-swaps the name, so a credential-shaped name
// embeds itself verbatim in the derived variable
// ("sk_live_0123456789abcdef" -> "SK_LIVE_0123456789ABCDEF_API_KEY").
// MaskOperand is the identity on that class; the registry-slot mask is not, so
// the derived-name leak is decided by the same authority as the door guard.
func wizardWithholdsDerivedEnv(name string) bool {
	return MaskSlot(name) != name
}

// wizardBaseURLRefused is the wizard door's base-URL guard. validateBaseURL is
// the transport rule (scheme and host) and says nothing about the rest of the
// URL, so a credential-bearing endpoint passed the prompt, echoed raw at the
// edit prompt and the completion summary, rode the probe request, and was
// persisted. The mission invariant is that providers.json stores env-var NAMES
// and endpoint data, never a secret VALUE, so a URL whose parts carry
// credential material is refused before the probe and before any write.
//
// The classification is the ONE shared URL authority (BaseURLCarriesCredential,
// slotpolicy.go): the whole value, the host labels, the userinfo, the path (and
// the Opaque spelling), the query keys and values, and the fragment. The wizard
// keeps no local part list, so this door, the TUI provider form, and the
// add-json parser cannot drift apart again; the host arm and the benign-userinfo
// verdict arrive with the shared function.
func wizardBaseURLRefused(raw string) error {
	if BaseURLCarriesCredential(raw) {
		return wizardSecretRefusal("Base URL")
	}
	return nil
}

// wizardFieldRefused applies the shared NAME authority (NameCarriesCredential,
// slotpolicy.go) to one free-text model field of the wizard. The wizard writes
// straight into providers.json, so a misdirected key paste at any of these
// prompts must be refused before the value reaches the connection probe or the
// file, exactly as the TUI model form and the CLI model verbs refuse it. A
// refusal aborts the setup with nothing written and the value unechoed.
//
// FIX-8e: the guard used to be SecretShaped alone, so a key-shaped model ID of
// the underscore class ("tok_a1b2c3d4e5f6g7h8i9j0" and the rest of it) passed
// the Model ID, family, tags, and tools prompts and persisted raw into
// models[].id while the TUI model form, the CLI model verbs, and add-json all
// refused it. The authority is the union of the three shared arms, so the
// wizard door and every other ingest door answer identically on every input;
// the pre-write sweep below calls the same function, so a preset row or an
// endpoint-discovered id cannot slip in either.
func wizardFieldRefused(field, value string) error {
	if !NameCarriesCredential(value) {
		return nil
	}
	return wizardSecretRefusal(field)
}

// wizardModelFieldsRefused is the pre-write guard: it classifies every
// free-text field of every collected model (id, family, tags, tool grants)
// once more, so a row the per-prompt guard never showed the user (a preset row
// or an id discovered from the endpoint's model list) cannot carry credential
// material into the registry either. The reasoning effort cannot carry a paste:
// the prompt maps anything outside the closed set to "default".
func wizardModelFieldsRefused(models []Model) error {
	for _, m := range models {
		if err := wizardFieldRefused("model ID", m.ID); err != nil {
			return err
		}
		if err := wizardFieldRefused("model family", m.Family); err != nil {
			return err
		}
		for _, tag := range m.Tags {
			if err := wizardFieldRefused("model tag", tag); err != nil {
				return err
			}
		}
		for _, tool := range m.Capabilities.AllowedTools {
			if err := wizardFieldRefused("model tool name", tool); err != nil {
				return err
			}
		}
	}
	return nil
}

func promptCustomModel(r *bufio.Reader, w io.Writer, modelID string) (Model, error) {
	// The ID is the first free-text field of the custom row, so it is refused
	// before the family inference and before any probe request carries it.
	if err := wizardFieldRefused("model ID", modelID); err != nil {
		return Model{}, err
	}
	inferredFam := InferFamily(modelID)
	family, _ := promptLine(r, w, fmt.Sprintf("Model Family [%s]", inferredFam), inferredFam)
	if family == "" {
		family = inferredFam
	}
	if err := wizardFieldRefused("model family", family); err != nil {
		return Model{}, err
	}

	ctxStr, _ := promptLine(r, w, "Context Length in tokens [1048576, or -1 for server maximum]", "1048576")
	ctxLen, _ := strconv.Atoi(ctxStr)
	if ctxLen <= 0 && ctxLen != -1 {
		ctxLen = 1048576
	}

	maxOutStr, _ := promptLine(r, w, "Max Output Tokens [131072, or -1 for server maximum]", "131072")
	maxOut, _ := strconv.Atoi(maxOutStr)
	if maxOut <= 0 && maxOut != -1 {
		maxOut = 131072
	}

	toolsAns, _ := promptLine(r, w, "Supports Tools / Function Calling? (Y/n)", "Y")
	supportsTools := !strings.EqualFold(toolsAns, "n") && !strings.EqualFold(toolsAns, "no")

	var allowedTools []string
	if supportsTools {
		toolsListStr, _ := promptLine(r, w, "Allowed tools for this model (fanout --tools read,search,write,exec; read,search = read_file,list_dir,grep,glob; write = write_file; exec = run_command - and write/exec only take effect for tools listed here)", "read_file,list_dir,grep,glob")
		for _, t := range strings.Split(toolsListStr, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if err := wizardFieldRefused("model tool name", t); err != nil {
				return Model{}, err
			}
			allowedTools = append(allowedTools, t)
		}
	}

	thinkingAns, _ := promptLine(r, w, "Supports Reasoning / Thinking? (Y/n)", "Y")
	supportsThinking := !strings.EqualFold(thinkingAns, "n") && !strings.EqualFold(thinkingAns, "no")

	var reasoningEffort string
	if supportsThinking {
		effortInput, _ := promptLine(r, w, "Reasoning Effort [default, none, low, medium, high, xhigh, max] [default]", "default")
		effortInput = strings.ToLower(strings.TrimSpace(effortInput))
		switch effortInput {
		case "default", "none", "low", "medium", "high", "xhigh", "max":
			reasoningEffort = effortInput
		default:
			reasoningEffort = "default"
		}
	}

	visionAns, _ := promptLine(r, w, "Supports Vision / Images? (y/N)", "N")
	supportsVision := strings.EqualFold(visionAns, "y") || strings.EqualFold(visionAns, "yes")

	caps := Capabilities{
		SupportsTools:    supportsTools,
		AllowedTools:     allowedTools,
		SupportsThinking: supportsThinking,
		ReasoningEffort:  reasoningEffort,
		SupportsVision:   supportsVision,
	}

	tags := DeriveTags(caps)

	return Model{
		ID:              modelID,
		Family:          family,
		Tags:            tags,
		ContextLength:   ctxLen,
		MaxOutputTokens: maxOut,
		Capabilities:    caps,
	}, nil
}

// DeriveTags automatically assigns tags based on model capabilities:
// "general" is default, "tools" for tool support, "reasoning" for thinking/reasoning,
// and "vision" for image/multimodal support.
func DeriveTags(cap Capabilities) []string {
	tags := []string{"general"}
	if cap.SupportsTools {
		tags = append(tags, "tools")
	}
	if cap.SupportsThinking {
		tags = append(tags, "reasoning")
	}
	if cap.SupportsVision {
		tags = append(tags, "vision")
	}
	return tags
}

// MissingCapabilityTags lists the capability tags implied by Capabilities
// ("tools", "reasoning", "vision") that Tags does not carry. The gateway, the
// wizard summary, and `agy pro models` read the flags while the model list reads
// the tags, so a model whose tags lag behind its flags is displayed
// inconsistently. Empty result means the two agree.
func (m Model) MissingCapabilityTags() []string {
	var missing []string
	for _, tag := range DeriveTags(m.Capabilities) {
		if tag == "general" || m.HasTag(tag) {
			continue
		}
		missing = append(missing, tag)
	}
	return missing
}

// ProbeEndpoint sends a minimal ping completion request to verify connection & authentication.
func ProbeEndpoint(baseURL, apiKey, modelID string) (latencyMs int64, err error) {
	if strings.TrimSpace(baseURL) == "" {
		return 0, errors.New("empty baseURL")
	}
	if strings.TrimSpace(modelID) == "" {
		return 0, errors.New("empty modelID")
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	reqBody := map[string]any{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 5,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 35 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Milliseconds()
	dest := endpointHost(baseURL)
	if err != nil {
		// FIX-M: the *url.Error embeds the full endpoint, path included, so a
		// credential-bearing base URL used to reach the transcript through this
		// wrap. The sentence routes through maskProbeTranscript and the original
		// stays in the chain (probeTranscriptError.Unwrap), so isTimeoutErr's
		// errors.Is/errors.As checks are unaffected.
		return latency, &probeTranscriptError{
			msg: maskProbeTranscript(fmt.Sprintf("network error contacting %s: %v", dest, err), baseURL),
			err: err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respSnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// B1: OpenAI-compatible gateways routinely echo the offending bearer
		// key in the error body. Wrap the snippet with the same redactor the
		// sibling ProbeKeyValidity uses (seeded with the probe key) plus the
		// generic credential patterns, so a key-echoing upstream body can never
		// reach the wizard transcript (U8).
		detail := strings.TrimSpace(string(respSnippet))
		detail = redactProbeDetail(detail, apiKey)
		detail = NewRedactor()(detail)
		return latency, fmt.Errorf("HTTP %d contacting %s (%s): %s", resp.StatusCode, dest, resp.Status, detail)
	}

	return latency, nil
}

// probeTranscriptError is an error whose message has been rendered for the
// wizard transcript while the original stays in the chain, so
// isTimeoutErr (errors.Is on context.DeadlineExceeded, errors.As on net.Error)
// keeps seeing the transport error underneath. It mirrors the osError idiom in
// internal/agysettings: masking a message must never break the chain.
type probeTranscriptError struct {
	msg string
	err error
}

func (e *probeTranscriptError) Error() string { return e.msg }
func (e *probeTranscriptError) Unwrap() error { return e.err }

// maskProbeTranscript renders one probe-failure sentence for the transcript.
// FIX-M: with a credential-bearing base URL, the transport error (*url.Error)
// echoes the whole endpoint and no shared URL masker covers the path, so the
// sentence is seeded with the credential-shaped parts of the URL. The
// classification is the registry-slot authority's (a part is credential
// material when MaskSlot does not return it unchanged), the same rule the
// provider form's base-URL ingest guard applies per part.
//
// FIX-7B: the seeds were PATH-only, so a query value, a fragment, or a userinfo
// username still printed raw inside the wrapped *url.Error (the URL prompt now
// refuses those parts, and this seed set is the defense in depth for a hand-
// edited or future ingest path). The builder covers every part the door guard
// reads (probeCredentialPathSeeds).
//
// MaskSentence is the established sentence-level masker for error text; it drops
// a seed that SeedEligible declines (a separator-carrying lower-case head such
// as "sk_live_..." is neither a contiguous run nor a known prefix), so the same
// seeds are applied once more through the seeded redactor the HTTP-detail sink
// above already uses. An eligible seed is [REDACTED] by then, which makes the
// second pass a no-op, and a benign sentence has no seeds at all, so its bytes
// are unchanged (MaskSentence's no-seed fallback classifies a full sentence as
// benign).
func maskProbeTranscript(sentence, baseURL string) string {
	seeds := probeCredentialPathSeeds(baseURL)
	if len(seeds) == 0 {
		return MaskSentence(sentence)
	}
	return NewRedactor(seeds...)(MaskSentence(sentence, seeds...))
}

// probeCredentialPathSeeds returns the URL parts that read as credential
// material to the registry-slot authority: the credential-bearing HOST labels,
// every path segment (and the Opaque slot of a hostless "https:<key>"), every
// query key and value, the fragment, and the userinfo username and password.
// The transport layer splits a URL into these parts, so no whole-value
// classifier can see one; MaskSlot reads each part exactly as a registry slot,
// and the *url.Error prints every part verbatim. Ordinary parts ("v1", "chat",
// "completions", "api-version", a benign host label) are identity, so a benign
// URL yields no seeds and its transcript stays byte-identical.
//
// FIX-8f: the HOST slot was the last one missing, so a key pasted as the host
// ("https://sk_live_0123456789abcdef/v1") printed raw inside the wrapped
// *url.Error sentence. The host labels are seeded through the shared host rule
// (baseURLHostLabelCarriesCredential), and they are appended without the
// MaskSlot gate: the class the host rule exists for (the underscore head) is
// exactly the class MaskSlot still reads as a registry slot but SeedEligible
// declines, so the seeded-redactor pass is what removes it. A redactor seed is
// a literal string, so seeding one more literal can only mask.
func probeCredentialPathSeeds(baseURL string) []string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil
	}
	var seeds []string
	add := func(part string) {
		if part != "" && MaskSlot(part) != part {
			seeds = append(seeds, part)
		}
	}
	for _, label := range strings.Split(u.Hostname(), ".") {
		if baseURLHostLabelCarriesCredential(label) {
			seeds = append(seeds, label)
		}
	}
	for _, slot := range []string{u.Path, u.Opaque} {
		for _, seg := range strings.Split(slot, "/") {
			add(seg)
		}
	}
	for key, vals := range u.Query() {
		add(key)
		for _, v := range vals {
			add(v)
		}
	}
	add(strings.TrimSpace(u.Fragment))
	if u.User != nil {
		add(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			add(pw)
		}
	}
	return seeds
}

// maskProbeError renders a probe error for a transcript sink, returning it
// unchanged when nothing needs masking so a benign failure keeps its exact
// bytes.
func maskProbeError(err error, baseURL string) error {
	if err == nil {
		return nil
	}
	if msg := maskProbeTranscript(err.Error(), baseURL); msg != err.Error() {
		return &probeTranscriptError{msg: msg, err: err}
	}
	return err
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timed out")
}

func promptLine(r *bufio.Reader, w io.Writer, prompt, defaultValue string) (string, bool) {
	return promptLineDisplay(r, w, prompt, defaultValue, defaultValue)
}

// promptLineDisplay is promptLine with the bracketed default painted from
// display while an accepted default still returns defaultValue. The two differ
// only where the default is a credential-bearing value the prompt must not echo
// raw (FIX-J): the display copy is a mask of the functional value, so accepting
// the default keeps writing the RAW value the caller started from.
func promptLineDisplay(r *bufio.Reader, w io.Writer, prompt, display, defaultValue string) (string, bool) {
	if display != "" && !strings.Contains(prompt, "[") {
		fmt.Fprintf(w, "%s [%s]: ", prompt, display)
	} else {
		fmt.Fprintf(w, "%s: ", prompt)
	}

	line, err := r.ReadString('\n')
	isEOF := errors.Is(err, io.EOF) || (err != nil && len(line) == 0)
	line = strings.TrimPrefix(line, "\ufeff")
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, isEOF
	}
	return line, isEOF
}

func promptString(r *bufio.Reader, w io.Writer, prompt, defaultValue string) string {
	val, _ := promptLine(r, w, prompt, defaultValue)
	return val
}

// defaultBaseURLFor returns the OpenAI-compatible base URL suggested for a
// provider name. Cataloged providers return their known endpoint; unknown
// names return "" (no invented default) and the wizard shows an example
// format instead, so a typo like "ocn" never silently targets openai.com.
func defaultBaseURLFor(name string) string {
	if hint, ok := LookupProviderHint(name); ok {
		return hint.BaseURL
	}
	return ""
}

// defaultEnvFor returns the API key environment variable suggested for a
// provider name. Unknown names are upper-cased with every non-alphanumeric
// character replaced by "_" so the result is always a valid variable name.
func defaultEnvFor(name string) string {
	if hint, ok := LookupProviderHint(name); ok {
		return hint.KeyEnv
	}
	upper := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(name), "\ufeff"))
	var b strings.Builder
	for _, r := range upper {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String() + "_API_KEY"
}

// providerLabelList renders the known-provider catalog for the wizard banner.
func providerLabelList() string {
	hints := KnownProviders()
	labels := make([]string, 0, len(hints))
	for _, h := range hints {
		labels = append(labels, h.Label)
	}
	return strings.Join(labels, ", ")
}

// providerAliasList renders the accepted aliases as "alias -> canonical".
func providerAliasList() string {
	var parts []string
	for _, h := range KnownProviders() {
		if len(h.Aliases) > 0 {
			parts = append(parts, strings.Join(h.Aliases, ", ")+" -> "+h.Name)
		}
	}
	return strings.Join(parts, "; ")
}

// isRunningTests is a development-time safety rail, NOT a security boundary:
// it keeps `go test` away from the real Windows environment stores. Detection
// is exact (testing.Testing()) and cannot be spoofed by renaming the binary,
// unlike the old os.Args[0] ".test" suffix heuristic. Single helper shared by
// the wizard persistence, the env store, and the key-repair path.
func isRunningTests() bool {
	return testing.Testing()
}
