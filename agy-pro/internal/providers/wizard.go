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
	// B5 data-layer completeness: the wizard persists to the same registry as
	// the registry-ops path, so names must satisfy the same shell-safe charset
	// (a name later lands in plugin run_command lines).
	if err := ValidateProviderName(provName); err != nil {
		return err
	}
	// B2/U8: every later echo of the name goes through MaskOperand — a
	// misdirected key paste in the name slot must never reach the transcript.
	provLabel := MaskOperand(provName)

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
	if err := validateBaseURL(baseURL); err != nil {
		// B2/U8: the operand may be a misdirected key paste; never echo it raw.
		return fmt.Errorf("%w: %s", err, MaskOperand(baseURL))
	}

	// Step 3: API Key & Environment Variable Name
	defaultEnv := defaultEnvFor(provName)
	existingVal := os.Getenv(defaultEnv)
	// C5/U8: a credential-shaped provider name passes the B5 charset and
	// defaultEnvFor only case-folds it and swaps separators for "_", so the
	// derived name embeds the pasted key verbatim
	// ("AKIAIOSFODNN7EXAMPLE" -> "AKIAIOSFODNN7EXAMPLE_API_KEY"). MaskOperand
	// keeps UPPER_SNAKE names readable by design, so echo sites withhold that
	// derived name instead of leaking it. A name the user typed as an env
	// variable still renders through MaskOperand.
	withholdDerivedEnv := MaskOperand(provName) != provName
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
			presets = append(presets, ModelPreset{
				ID:              rm,
				Family:          InferFamily(rm),
				Description:     fmt.Sprintf("%s (auto-discovered)", rm),
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
				fmt.Fprintf(out, "  %d) %s (%s)%s\n", i+1, p.ID, p.Description, tag)
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
					selectedModel = promptCustomModel(reader, out, choiceStr)
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
			selectedModel = promptCustomModel(reader, out, customID)
			hasSelection = true
		}

		// Prevent duplicate models
		if configuredSet[selectedModel.ID] {
			fmt.Fprintf(out, "Model %q is already configured for %s.\n", MaskOperand(selectedModel.ID), provLabel)
			continue
		}

		// Live Connection Probe Loop
	probeLoop:
		for {
			fmt.Fprintf(out, "\n[Live Probe] Testing endpoint %s connection with model %q...\n", endpointHost(baseURL), MaskOperand(selectedModel.ID))
			latency, probeErr := ProbeEndpoint(baseURL, keyValue, selectedModel.ID)
			if probeErr == nil {
				fmt.Fprintf(out, "✅ Connection succeeded! Latency: %dms\n", latency)
				models = append(models, selectedModel)
				configuredSet[selectedModel.ID] = true
				break probeLoop
			}

			fmt.Fprintf(out, "❌ Connection test failed: %v\n", probeErr)
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
				fmt.Fprintf(out, "Saved model %q anyway.\n", MaskOperand(selectedModel.ID))
				break probeLoop
			case "2", "retry", "r":
				continue probeLoop
			case "3", "edit", "e":
				newURL, _ := promptLine(reader, out, "Base URL", baseURL)
				newURL = strings.TrimSpace(newURL)
				if newURL != "" {
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
				fmt.Fprintf(out, "Discarded model %q.\n", MaskOperand(selectedModel.ID))
				break probeLoop
			default:
				fmt.Fprintf(out, "Discarded model %q.\n", MaskOperand(selectedModel.ID))
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
	fmt.Fprintf(out, "Base URL        : %s\n", baseURL)
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
			i+1, DisplayName(MaskOperand(m.ID), provLabel), MaskFamilyName(m), ctxDisplay, m.CapabilitySummary())
	}
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "Ready to use! The gateway bridge activates whenever an external model is targeted:")
	fmt.Fprintf(out, "  Native agy   : agy -m %q\n", DisplayName(MaskOperand(models[0].ID), provLabel))
	fmt.Fprintln(out, "                 (set AGY_PRO_BRIDGE=1 to expose every model in the /model picker)")
	fmt.Fprintf(out, "  Fanout swarm : agy pro fanout --models %q --prompt \"your prompt\"\n", provLabel+"/"+MaskOperand(models[0].ID))
	fmt.Fprintln(out, "  List models  : agy pro models")
	fmt.Fprintln(out, "===========================================================")
	return nil
}

func promptCustomModel(r *bufio.Reader, w io.Writer, modelID string) Model {
	inferredFam := InferFamily(modelID)
	family, _ := promptLine(r, w, fmt.Sprintf("Model Family [%s]", inferredFam), inferredFam)
	if family == "" {
		family = inferredFam
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
			if t != "" {
				allowedTools = append(allowedTools, t)
			}
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
	}
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
		return latency, fmt.Errorf("network error contacting %s: %w", dest, err)
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
	if defaultValue != "" && !strings.Contains(prompt, "[") {
		fmt.Fprintf(w, "%s [%s]: ", prompt, defaultValue)
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
