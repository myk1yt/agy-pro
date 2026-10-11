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
	"time"
)

// InteractiveSetupWizard guides the user through setting up or updating a provider
// in ~/.agy-pro/providers.json in a friendly, conversational manner.
func InteractiveSetupWizard(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Multi-Model Provider Setup Wizard")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "Configure OpenAI-compatible LLM endpoints for multi-model fanout.")
	fmt.Fprintln(out, "API keys are stored securely in your environment, never in providers.json.")
	fmt.Fprintln(out)

	// Step 1: Provider Name
	provName, isEOF := promptLine(reader, out, "Provider name (e.g., deepseek, openrouter, groq, openai, NW)", "")
	provName = strings.TrimPrefix(strings.TrimSpace(provName), "\ufeff")
	if isEOF && provName == "" {
		return errors.New("input closed (EOF); setup aborted")
	}
	if provName == "" {
		return errors.New("provider name cannot be empty")
	}

	// Step 2: Base URL
	defaultURL := defaultBaseURLFor(provName)
	baseURL, isEOF := promptLine(reader, out, "OpenAI-compatible Base URL", defaultURL)
	baseURL = strings.TrimPrefix(strings.TrimSpace(baseURL), "\ufeff")
	if isEOF && baseURL == "" {
		return errors.New("input closed (EOF); setup aborted")
	}
	if baseURL == "" {
		return errors.New("base URL cannot be empty")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("baseURL must start with http:// or https://: %s", baseURL)
	}

	// Step 3: API Key & Environment Variable Name
	defaultEnv := defaultEnvFor(provName)
	existingVal := os.Getenv(defaultEnv)

	fmt.Fprintln(out)
	var keyValue string
	var apiKeyEnv string = defaultEnv

	if existingVal != "" {
		fmt.Fprintf(out, "Detected existing environment variable %s.\n", defaultEnv)
		keyInput, _ := promptLine(reader, out, fmt.Sprintf("API Key (press Enter to keep %s, or paste new key)", defaultEnv), "")
		if keyInput == "" {
			keyValue = existingVal
			fmt.Fprintf(out, "Using existing value from %s.\n", defaultEnv)
		} else {
			keyValue = keyInput
		}
	} else {
		fmt.Fprintf(out, "Enter API Key for %s (will be stored in environment variable %s):\n", provName, defaultEnv)
		keyInput, _ := promptLine(reader, out, "API Key (sk-... or leave blank to skip)", "")
		keyValue = keyInput
	}

	// Smart check: Did user paste a custom environment variable name instead of a key?
	if keyValue != "" && !isLikelyAPIKey(keyValue) && envNameRe.MatchString(keyValue) {
		if envVal := os.Getenv(keyValue); envVal != "" {
			apiKeyEnv = keyValue
			keyValue = envVal
			fmt.Fprintf(out, "Using environment variable %s (loaded from current session).\n", apiKeyEnv)
		}
	}
	apiKeyEnv = strings.TrimPrefix(strings.TrimSpace(apiKeyEnv), "\ufeff")

	// Persist API key if provided
	if keyValue != "" && apiKeyEnv != "" {
		_ = os.Setenv(apiKeyEnv, keyValue)
		if runtime.GOOS == "windows" {
			if err := persistWindowsUserEnv(apiKeyEnv, keyValue); err != nil {
				fmt.Fprintf(out, "Warning: could not persist %s to Windows user environment: %v\n", apiKeyEnv, err)
			} else {
				fmt.Fprintf(out, "✅ Saved %s to Windows User Environment.\n", apiKeyEnv)
			}
		} else {
			fmt.Fprintf(out, "Set %s in current session. To persist across shells, add to ~/.bashrc or ~/.zshrc:\n", apiKeyEnv)
			fmt.Fprintf(out, "  export %s=\"...\"\n", apiKeyEnv)
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
		fmt.Fprintf(out, "--- Model Configuration (%s) ---\n", provName)

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
				selectedModel = Model{
					ID:              p.ID,
					Family:          p.Family,
					Tags:            p.Tags,
					ContextLength:   p.ContextLength,
					MaxOutputTokens: p.MaxOutputTokens,
					Capabilities:    p.Capabilities,
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
			fmt.Fprintf(out, "Model %q is already configured for %s.\n", selectedModel.ID, provName)
			continue
		}

		// Live Connection Probe Loop
	probeLoop:
		for {
			fmt.Fprintf(out, "\n[Live Probe] Testing endpoint connection with model %q...\n", selectedModel.ID)
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
				fmt.Fprintf(out, "Saved model %q anyway.\n", selectedModel.ID)
				break probeLoop
			case "2", "retry", "r":
				continue probeLoop
			case "3", "edit", "e":
				newURL, _ := promptLine(reader, out, "Base URL", baseURL)
				newURL = strings.TrimSpace(newURL)
				if newURL != "" {
					baseURL = newURL
				}
				newKey, _ := promptLine(reader, out, "API Key", "keep current")
				newKey = strings.TrimSpace(newKey)
				if newKey != "" && !strings.EqualFold(newKey, "keep current") {
					if !isLikelyAPIKey(newKey) && envNameRe.MatchString(newKey) {
						if envVal := os.Getenv(newKey); envVal != "" {
							apiKeyEnv = newKey
							keyValue = envVal
							fmt.Fprintf(out, "Using environment variable %s (loaded from current session).\n", apiKeyEnv)
						} else {
							keyValue = newKey
							_ = os.Setenv(apiKeyEnv, keyValue)
							if runtime.GOOS == "windows" {
								_ = persistWindowsUserEnv(apiKeyEnv, keyValue)
							}
						}
					} else {
						keyValue = newKey
						_ = os.Setenv(apiKeyEnv, keyValue)
						if runtime.GOOS == "windows" {
							_ = persistWindowsUserEnv(apiKeyEnv, keyValue)
						}
					}
				}
				continue probeLoop
			case "4", "discard", "d":
				fmt.Fprintf(out, "Discarded model %q.\n", selectedModel.ID)
				break probeLoop
			default:
				fmt.Fprintf(out, "Discarded model %q.\n", selectedModel.ID)
				break probeLoop
			}
		}

		more, isEOF := promptLine(reader, out, fmt.Sprintf("Add another model for %s? (y/N)", provName), "N")
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
	fmt.Fprintf(out, "Provider        : %s\n", provName)
	fmt.Fprintf(out, "Base URL        : %s\n", baseURL)
	fmt.Fprintf(out, "API Key Env     : %s (saved in environment)\n", apiKeyEnv)
	fmt.Fprintf(out, "Config File     : %s\n", targetPath)
	fmt.Fprintf(out, "Configured Models (%d):\n", len(models))
	for i, m := range models {
		toolsStr := "no"
		if m.Capabilities.SupportsTools {
			toolsStr = "yes"
		}
		ctxDisplay := fmt.Sprintf("%dk", m.ContextLength/1000)
		if m.ContextLength <= 0 {
			ctxDisplay = "server-max"
		} else if m.ContextLength >= 1048576 {
			ctxDisplay = fmt.Sprintf("%dM", m.ContextLength/1048576)
		}
		fmt.Fprintf(out, "  %d. %-24s [family: %s, context: %s, tools: %s]\n", i+1, m.ID, m.FamilyName(), ctxDisplay, toolsStr)
	}
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintf(out, "Ready to use! Example:\n  agy pro fanout -m %s/%s \"your prompt\"\n", provName, models[0].ID)
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
		toolsListStr, _ := promptLine(r, w, "Allowed tools", "read_file,list_dir,grep,glob")
		for _, t := range strings.Split(toolsListStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				allowedTools = append(allowedTools, t)
			}
		}
	}

	thinkingAns, _ := promptLine(r, w, "Supports Reasoning / Thinking? (y/N)", "N")
	supportsThinking := strings.EqualFold(thinkingAns, "y") || strings.EqualFold(thinkingAns, "yes")

	visionAns, _ := promptLine(r, w, "Supports Vision / Images? (y/N)", "N")
	supportsVision := strings.EqualFold(visionAns, "y") || strings.EqualFold(visionAns, "yes")

	caps := Capabilities{
		SupportsTools:    supportsTools,
		AllowedTools:     allowedTools,
		SupportsThinking: supportsThinking,
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
	if err != nil {
		return latency, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respSnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return latency, fmt.Errorf("HTTP %d (%s): %s", resp.StatusCode, resp.Status, strings.TrimSpace(string(respSnippet)))
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

func defaultBaseURLFor(name string) string {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff")
	switch norm {
	case "openai":
		return "https://api.openai.com/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "nw", "neuralwatt":
		return "https://api.neuralwatt.com/v1"
	case "together":
		return "https://api.together.xyz/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	default:
		return "https://api.openai.com/v1"
	}
}

func defaultEnvFor(name string) string {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff")
	switch norm {
	case "openai":
		return "OPENAI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "nw", "neuralwatt":
		return "NW_API_KEY"
	case "together":
		return "TOGETHER_API_KEY"
	case "mistral":
		return "MISTRAL_API_KEY"
	default:
		upper := strings.ToUpper(strings.ReplaceAll(norm, "-", "_"))
		return upper + "_API_KEY"
	}
}
