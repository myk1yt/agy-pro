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
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// WizardOptions carries the caller's consent context for the wizard's single
// composed write gate (P5-1 U4, user decision F3(a)).
//
// One wizard run writes TWO persisted stores: the providers.json provider+model
// upsert (W12) and the Windows user-environment key persist (W31). Until P5-1
// neither had any approval; both are now presented as ONE exact diff, once,
// after the last prompt and before the first write.
//
// Console reports that the caller's stdin can answer a y/N question. The CLI
// doors pass their console probe, so a piped or scripted `agy pro setup`
// prints the diff and refuses, naming --yes, with nothing written. Yes is that
// documented --yes stand-in: it consents without reading an answer, after the
// diff has printed.
type WizardOptions struct {
	Yes     bool
	Console bool
}

// InteractiveSetupWizard guides the user through setting up or updating a provider
// in ~/.agy-pro/providers.json in a friendly, conversational manner.
//
// The reader IS the wizard's dialog, so the library entry point treats it as
// answerable (Console: true) and the gate asks y/N over the same reader. A
// caller that knows its stdin is a pipe (the CLI doors, which probe the
// handle) calls InteractiveSetupWizardWithGate with Console false.
//
// InteractiveSetupWizardWithGate is the production entry: every CLI door binds
// it (cmd/agy/main.go, cmd/agy/pro_settings.go, internal/fanout). The hub's own
// form is TUI-native (internal/tui/screen_providers.go, the "Add provider" row),
// and a console host reaches this wizard as the documented terminal alternative
// (internal/tui/screen_settings.go names `agy pro setup --wizard`). This
// 2-argument form has no production caller left, so its reach is tests and
// library parity (a caller that genuinely knows its reader is answerable); a new
// production caller must pass its consent context explicitly instead of assuming
// this function's Console: true.
func InteractiveSetupWizard(in io.Reader, out io.Writer) error {
	return InteractiveSetupWizardWithGate(in, out, WizardOptions{Console: true})
}

// InteractiveSetupWizardWithGate is InteractiveSetupWizard with the caller's
// consent context for the composed write gate.
func InteractiveSetupWizardWithGate(in io.Reader, out io.Writer, opts WizardOptions) error {
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
	// charset does. The charset accepts the underscore key family (the
	// testfixtures.SKLive fixture), MaskOperand is the identity on it, and the
	// wizard persists the name through its own upsert (not registry_ops.go),
	// from where it reaches the derived env name (that fixture case-folded to
	// upper snake case with an "_API_KEY" suffix) and every listing, doctor,
	// and report sink. A name that carries credential material is refused here,
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
	// verbatim (testfixtures.AKIAExample -> that value with an "_API_KEY"
	// suffix). The registry-slot mask decides the class, not MaskOperand alone:
	// MaskOperand is the identity on the underscore family
	// (testfixtures.SKLive), which is exactly the class this mission closes.
	// Echo sites withhold that derived name instead of leaking it. A name the
	// user typed as an env variable still renders through MaskOperand.
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

	// Persist API key if provided.
	//
	// P5-1 U4 (F3(a)): the persistent user-environment write (W31) is persisted
	// state, so it may not happen before the composed gate. The PROCESS
	// environment write stays immediate and where it always was: it is not
	// persisted, and the "is this a variable name instead of a key?" prompts
	// below read the process block, so deferring it would change what the very
	// next prompt resolves. The user-environment write is recorded and flushed
	// after consent, under the same decision as the registry upsert.
	var pendingKey wizardPendingKey
	if keyValue != "" && apiKeyEnv != "" {
		_ = os.Setenv(apiKeyEnv, keyValue)
		switch {
		case wizardPersistsUserEnv():
			pendingKey = wizardPendingKey{name: apiKeyEnv, value: keyValue, set: true}
		case runtime.GOOS == "windows":
			// In test environment, skip persisting to system registry
		default:
			fmt.Fprintf(out, "Set %s in current session. To persist across shells, add to ~/.bashrc or ~/.zshrc:\n", envEcho(apiKeyEnv))
			fmt.Fprintf(out, "  export %s=\"...\"\n", envEcho(apiKeyEnv))
		}
	} else if apiKeyEnv != "" {
		keyValue = os.Getenv(apiKeyEnv)
	}

	// Step 4: Model Configuration
	presets := GetModelPresets(provName)
	// P2-7: the fetch now returns ceilings alongside ids, but the wizard's
	// synthesized presets keep their historical constants (1M context, -1
	// max output): this call site is a caller update for the new signature,
	// not a behaviour change. Ceiling-seeded defaults live in the TUI model
	// form's model-ID picker.
	remoteModels, _ := FetchRemoteModels(baseURL, keyValue)

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
				ID:              rm.ID,
				Family:          InferFamily(rm.ID),
				Description:     wizardModelLabel(rm.ID) + " (auto-discovered)",
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
			// A closed input cannot answer this loop again, and an EOF read
			// returns instantly, so the "empty ID" branch below would re-prompt
			// forever: 100% CPU for a piped or scripted run whose dialog ends
			// before its first model (found by the P5-1 U4 gate work, where the
			// gated tail made exactly that shape reachable in a test). The
			// preset menu above already aborts on this EOF shape; this is the
			// same rule for the custom-ID door.
			if isEOF && len(models) == 0 {
				return errors.New("input closed (EOF); setup aborted")
			}
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
							// P5-1 U4: the user-environment write is deferred to the
							// composed gate; the last typed value wins, exactly as the
							// last of the repeated immediate writes did before.
							pendingKey.record(apiKeyEnv, keyValue)
						}
					} else {
						keyValue = newKey
						fmt.Fprintf(out, "API Key recorded: %s\n", MaskSecret(keyValue))
						_ = os.Setenv(apiKeyEnv, keyValue)
						// P5-1 U4: deferred user-environment write (see above).
						pendingKey.record(apiKeyEnv, keyValue)
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

	// Step 5: compose the plan, gate it, re-verify it, then write.
	//
	// P5-1 U4 (F3(a)): the wizard's two persisted writes (W12 the registry
	// upsert, W31 the user-environment key persist) were ungated. Both now sit
	// behind ONE decision: the composed exact diff prints after the last prompt
	// and before the FIRST write, the caller consents (console y/N, or the
	// documented --yes), and the plan is re-read and re-verified immediately
	// before the write so an external edit under the prompt refuses instead of
	// being overwritten under a diff that no longer describes the file.
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

	plan, err := composeWizardWritePlan(targetPath, prov, pendingKey)
	if err != nil {
		return fmt.Errorf("failed to load existing registry: %w", err)
	}

	proceed, gateErr := wizardConsent(reader, out, plan, opts)
	if gateErr != nil {
		return gateErr
	}
	if !proceed {
		return nil
	}

	verified, err := plan.reverify()
	if err != nil {
		return err
	}
	if err := plan.apply(verified, out, envEcho); err != nil {
		return err
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
// credential family (testfixtures.SKLive and testfixtures.TokSample):
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
// underscore-head class (testfixtures.SKLive: no shared prefix, and the
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
// embeds itself verbatim in the derived variable (testfixtures.SKLive -> that
// value case-folded to upper snake case with an "_API_KEY" suffix).
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
// the underscore class (testfixtures.TokSample and the rest of it) passed
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

// --- P5-1 U4: the composed-write gate (W12 upsert + W31 key persist) ---------
//
// User decision F3(a): every write path to persisted state is presented as an
// exact diff before it happens. The wizard was the last ungated front door: it
// wrote providers.json (W12) and the Windows user environment (W31) with only
// per-prompt field guards, and on a piped host (where `agy pro setup` degrades
// to this wizard by design) there was no approval of any kind.
//
// The gate is the CLI twin's mechanism (cmd/agy/pro_settings.go
// confirmPersistedWrite), restated here because the wizard lives in the
// providers package and the wizard owns its own presentation: one exact diff,
// one y/N question, `--yes` for scripted callers, and a refusal that names
// `--yes` when nothing can answer. Only the store half differs: the wizard
// writes TWO stores in one run, so both are composed into the same diff and
// decided by the same answer.

// ErrWizardWriteRefused reports that the composed write was refused because no
// human could consent to it (closed or piped stdin without --yes). The refusal
// sentence itself is already in the transcript; this error carries the exit
// code so the caller exits 1 with nothing written.
var ErrWizardWriteRefused = errors.New("setup refused: nothing was written")

const (
	// wizardConsentBanner names the class of the wizard's composed decision.
	wizardConsentBanner = "[agy-pro] Approve Setup Write"
	// wizardRegistryWriteSentence mirrors modelWriteAtomicSentence (the CLI
	// twin, cmd/agy/pro_settings.go) byte for byte, so the wizard and the CLI
	// registry gate read the same and a grep for either finds both.
	wizardRegistryWriteSentence = "providers.json will be written (0600, atomic) with this exact diff:"
	// wizardCancelSentence and wizardRefusalSentence are the CLI twin's own
	// lines (confirmPersistedWrite), with this door's noun, so the two gates
	// cannot drift into two refusal stories.
	wizardCancelSentence  = "[CANCELLED] nothing written"
	wizardRefusalSentence = "[agy-pro] refusal: non-interactive stdin cannot consent to a setup write; re-run with --yes after reviewing the diff above (nothing written)"
	// wizardPromptBytes bounds the consent read exactly as the twin does: a
	// character device with no newline would otherwise buffer without limit
	// (CWE-400), and 64 bytes is far past any y/N answer.
	wizardPromptBytes = 64
)

// wizardRegistryReload re-reads the registry at write time (after consent,
// immediately before the write), the G-C3 / CWE-367 rule the CLI registry
// verbs follow. It is a seam so tests can interleave an external edit exactly
// where production re-reads.
var wizardRegistryReload = LoadFrom

// wizardPersistUserEnv is the W31 write sink (presets.go persistWindowsUserEnv:
// HKCU\Environment through the advapi32 registry API, never a command line). A
// seam so tests can drive the composed gate without a registry write.
var wizardPersistUserEnv = persistWindowsUserEnv

// wizardPersistsUserEnv reports whether this host persists user environment
// variables at all. The pre-P5-1 wizard used the same rule inline
// (runtime.GOOS == "windows" && !isRunningTests()); it is a seam now so the
// gate's environment half is reachable on every host and in tests.
var wizardPersistsUserEnv = func() bool { return runtime.GOOS == "windows" && !isRunningTests() }

// wizardEnvProbe reads the persistent user environment (HKCU only: that is the
// root W31 writes) for the presented presence of one variable NAME. It never
// reads a value. known reports whether the store could be read at all, so the
// pre-consent state and the write-time state are only compared when both are
// observable.
var wizardEnvProbe = probeWizardUserEnv

// wizardPendingKey is the deferred user-environment write: the wizard collects
// it during the prompts and applies it after consent, under the same decision
// as the registry upsert. Only the NAME is ever rendered.
type wizardPendingKey struct {
	name  string
	value string
	set   bool
}

// record queues (or replaces) the deferred write. A later typed key replaces an
// earlier one, exactly as the last of the pre-P5-1 immediate writes won. A host
// that does not persist user environment variables queues nothing.
func (k *wizardPendingKey) record(name, value string) {
	if !wizardPersistsUserEnv() {
		return
	}
	*k = wizardPendingKey{name: name, value: value, set: true}
}

// wizardEnvState is the presented (and later re-read) presence of one variable
// name in HKCU\Environment: whether the clean name exists, and which BOM
// variants of it exist beside the clean name.
type wizardEnvState struct {
	known    bool
	exact    bool
	variants []string
}

// probeWizardUserEnv is the production body of wizardEnvProbe.
func probeWizardUserEnv(name string) wizardEnvState {
	name = strings.TrimSpace(name)
	if name == "" || defaultEnvStore == nil {
		return wizardEnvState{}
	}
	names, err := defaultEnvStore.Names(EnvRootUser)
	if err != nil {
		return wizardEnvState{}
	}
	st := wizardEnvState{known: true}
	for _, n := range names {
		if normalizeEnvName(n) != name {
			continue
		}
		if n == name {
			st.exact = true
			continue
		}
		st.variants = append(st.variants, n)
	}
	sort.Strings(st.variants)
	return st
}

// wizardEnvNameLabel renders one environment variable NAME for the diff: the
// shared operand mask, with a U+FEFF rendered as a visible "<BOM>" marker
// instead of an invisible byte (the same rendering the key repair gate uses, so
// a polluted variant is legible and not a trap). A clean name is unprefixed.
func wizardEnvNameLabel(name string) string {
	masked := MaskOperand(StripSlotControls(strings.ReplaceAll(name, BOM, "")))
	if strings.Contains(name, BOM) {
		return "<BOM>" + masked
	}
	return masked
}

// wizardWritePlan is the composed, not-yet-applied plan: the registry it was
// composed from (the signed base), the provider record to upsert, and the
// deferred key persist with its pre-consent presence.
type wizardWritePlan struct {
	targetPath string
	base       *Registry
	present    bool
	before     Provider
	prov       Provider
	key        wizardPendingKey
	env        wizardEnvState
}

// composeWizardWritePlan reads the registry on disk and composes the plan. The
// presented diff describes the write that apply() will perform, so nothing is
// rendered from a guess.
func composeWizardWritePlan(targetPath string, prov Provider, key wizardPendingKey) (*wizardWritePlan, error) {
	reg, err := loadWizardRegistry(targetPath)
	if err != nil {
		return nil, err
	}
	plan := &wizardWritePlan{targetPath: targetPath, base: reg, prov: prov, key: key}
	if p := wizardFindProvider(reg, prov.Name); p != nil {
		plan.present = true
		plan.before = *p
	}
	if key.set {
		plan.env = wizardEnvProbe(key.name)
	}
	return plan, nil
}

// loadWizardRegistry reads the registry the plan is composed from, with the
// empty-registry rule the CLI gate established: a missing file (os.ErrNotExist,
// or ErrNotConfigured from a helper) is an empty registry carrying the wizard's
// long-standing defaults; anything else is a real read error and refuses.
func loadWizardRegistry(path string) (*Registry, error) {
	reg, err := wizardRegistryReload(path)
	if err == nil {
		return reg, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNotConfigured) {
		return &Registry{
			Defaults: Defaults{
				N:              3,
				TimeoutSeconds: 120,
				MaxTurns:       10,
				Tools:          "read,search",
			},
		}, nil
	}
	return nil, err
}

// wizardFindProvider resolves one provider by name (case-folded), the same
// match the wizard's own upsert performs.
func wizardFindProvider(reg *Registry, name string) *Provider {
	if reg == nil {
		return nil
	}
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return &reg.Providers[i]
		}
	}
	return nil
}

// diff renders the composed write: the provider action sentence, the model
// count before/after, the atomic-0600 sentence, the removed/added provider
// lines (the shapes the CLI provider gate and the hub's M4 provider approvals
// use), and the key-persist lines. Every operand is delegated to the shared
// masking authorities (MaskSlot for registry slots, MaskBaseURL for the
// endpoint, MaskOperand for an environment NAME); no value is ever rendered.
func (p *wizardWritePlan) diff() []string {
	lines := []string{}
	name := wizardProvLabel(p.prov.Name)
	after := len(p.prov.Models)
	if !p.present {
		lines = append(lines,
			fmt.Sprintf("Adds provider %s to the registry. Key values are untouched.", name),
			fmt.Sprintf("Model count: 0 -> %d", after))
	} else {
		lines = append(lines,
			fmt.Sprintf("Replaces provider %s wholesale: the current entry and its whole model list are discarded.", name),
			fmt.Sprintf("Model count: %d -> %d", len(p.before.Models), after))
	}
	lines = append(lines, wizardRegistryWriteSentence)
	if p.present {
		lines = append(lines, "- "+wizardProviderLine(p.before))
	}
	lines = append(lines, "+ "+wizardProviderLine(p.prov))
	return append(lines, p.envLines()...)
}

// wizardProviderLine renders one registry entry for the diff: the masked name,
// the layered URL mask, and the model count (the shape both twins use).
func wizardProviderLine(p Provider) string {
	return fmt.Sprintf("provider %s (%s, %s)", wizardProvLabel(p.Name), MaskBaseURL(p.BaseURL), wizardCountWord(len(p.Models)))
}

// wizardCountWord renders a model count in the twins' plural form.
func wizardCountWord(n int) string {
	if n == 1 {
		return "1 model"
	}
	return strconv.Itoa(n) + " models"
}

// envLines renders the key-persist half of the plan: the variable NAME (never
// its value), any BOM variant that already sits beside it, and an honest
// statement when the store could not be read.
func (p *wizardWritePlan) envLines() []string {
	if !p.key.set {
		return nil
	}
	lines := []string{fmt.Sprintf("Sets %s in HKCU\\Environment and in this process's environment (the value is never printed).", wizardEnvNameLabel(p.key.name))}
	if len(p.env.variants) > 0 {
		lines = append(lines, fmt.Sprintf("  A BOM variant %s already exists beside the clean name; this write leaves it untouched (`agy pro doctor --fix-keys` repairs it).", wizardEnvNameLabel(p.env.variants[0])))
	}
	if !p.env.known {
		lines = append(lines, "  The user environment store could not be read, so the current presence of that name is unknown.")
	}
	return lines
}

// consentTarget names what the console prompt applies the write to.
func (p *wizardWritePlan) consentTarget() string {
	if p.key.set {
		return "providers.json and HKCU\\Environment"
	}
	return "providers.json"
}

// wizardConsent prints the composed diff and takes the decision.
//
//   - opts.Yes is the documented --yes: consent, after the diff printed.
//   - opts.Console false means nothing can answer (a pipe, or a host where
//     stdin is not a console): the diff still prints and the door refuses,
//     naming --yes, with nothing written. That is the refusal story this front
//     end did not have.
//   - a console answer of y consents; n, an empty line, or any other word
//     cancels with exit code 0 ("[CANCELLED] nothing written"); an immediate
//     EOF (zero bytes with a read error, e.g. /dev/null or NUL) takes the same
//     refusal as a pipe instead of a silent cancel, because a seam cannot tell
//     those character devices from a console.
func wizardConsent(reader *bufio.Reader, out io.Writer, plan *wizardWritePlan, opts WizardOptions) (bool, error) {
	fmt.Fprintln(out, wizardConsentBanner)
	for _, line := range plan.diff() {
		fmt.Fprintln(out, "  "+line)
	}
	if opts.Yes {
		return true, nil
	}
	if !opts.Console {
		fmt.Fprintln(out, wizardRefusalSentence)
		return false, ErrWizardWriteRefused
	}
	fmt.Fprintf(out, "Apply this write to %s? [y/N]: ", plan.consentTarget())
	answer, err := bufio.NewReader(io.LimitReader(reader, wizardPromptBytes)).ReadString('\n')
	if err != nil && answer == "" {
		fmt.Fprintln(out, wizardRefusalSentence)
		return false, ErrWizardWriteRefused
	}
	if strings.EqualFold(strings.TrimSpace(answer), "y") {
		return true, nil
	}
	fmt.Fprintln(out, wizardCancelSentence)
	return false, nil
}

// reverify re-reads the presented state immediately before the write and
// refuses on drift, returning the registry the write must use (so the write
// applies to what was verified, never to the pre-consent copy):
//
//   - add: the name is still absent;
//   - replace: the record is still the exact record the diff described;
//   - key persist: the presented presence of the variable name (clean name and
//     BOM variants) is unchanged, compared only when both reads are observable.
func (p *wizardWritePlan) reverify() (*Registry, error) {
	reg, err := loadWizardRegistry(p.targetPath)
	if err != nil {
		return nil, fmt.Errorf("re-reading providers.json before the write failed: %w (nothing written)", err)
	}
	name := wizardProvLabel(p.prov.Name)
	cur := wizardFindProvider(reg, p.prov.Name)
	switch {
	case !p.present && cur != nil:
		return nil, fmt.Errorf("provider %s appeared in providers.json between the presentation and the write, so the presented diff is stale; nothing written (re-run the setup wizard after reviewing the current registry)", name)
	case p.present && cur == nil:
		return nil, fmt.Errorf("provider %s disappeared from providers.json between the presentation and the write, so the presented diff is stale; nothing written (re-run the setup wizard after reviewing the current registry)", name)
	case p.present && !reflect.DeepEqual(*cur, p.before):
		return nil, fmt.Errorf("provider %s changed on disk between the presentation and the write, so the presented diff is stale; nothing written (re-run the setup wizard after reviewing the current record)", name)
	}
	if p.key.set {
		fresh := wizardEnvProbe(p.key.name)
		if p.env.known && fresh.known && (p.env.exact != fresh.exact || !equalWizardStrings(p.env.variants, fresh.variants)) {
			return nil, fmt.Errorf("the user environment entry %s changed between the presentation and the write, so the presented diff is stale; nothing written (HKCU untouched; re-run the setup wizard after reviewing the variable)", wizardEnvNameLabel(p.key.name))
		}
	}
	return reg, nil
}

// equalWizardStrings compares two name lists element by element.
func equalWizardStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// apply performs the composed write, in the presented order: the user
// environment persist first (a failure there is reported and does not abort the
// registry write, the pre-P5-1 contract), then the provider upsert into the
// re-verified registry, validated and written atomically.
func (p *wizardWritePlan) apply(reg *Registry, out io.Writer, envEcho func(string) string) error {
	if p.key.set {
		if err := wizardPersistUserEnv(p.key.name, p.key.value); err != nil {
			fmt.Fprintf(out, "Warning: could not persist %s to Windows user environment: %v\n", envEcho(p.key.name), err)
		} else {
			fmt.Fprintf(out, "✅ Saved %s to Windows User Environment.\n", envEcho(p.key.name))
		}
	}
	updated := false
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, p.prov.Name) {
			reg.Providers[i] = p.prov
			updated = true
			break
		}
	}
	if !updated {
		reg.Providers = append(reg.Providers, p.prov)
	}
	if err := reg.Validate(); err != nil {
		return fmt.Errorf("validation error: %w", err)
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := WriteFileAtomic(p.targetPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to save registry to %s: %w", p.targetPath, err)
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

	visionAns, _ := promptLine(r, w, "Supports Vision / Images? (Y/n)", "Y")
	supportsVision := !strings.EqualFold(visionAns, "n") && !strings.EqualFold(visionAns, "no")

	videoAns, _ := promptLine(r, w, "Supports Video? (Y/n)", "Y")
	supportsVideo := !strings.EqualFold(videoAns, "n") && !strings.EqualFold(videoAns, "no")

	caps := Capabilities{
		SupportsTools:    supportsTools,
		AllowedTools:     allowedTools,
		SupportsThinking: supportsThinking,
		ReasoningEffort:  reasoningEffort,
		SupportsVision:   supportsVision,
		SupportsVideo:    supportsVideo,
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
// (a URL whose host is the planted underscore-head fixture, testfixtures.SKLive)
// printed raw inside the wrapped
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
