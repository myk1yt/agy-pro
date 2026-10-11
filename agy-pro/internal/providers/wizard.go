package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// InteractiveSetupWizard guides the user through setting up or updating a provider
// in ~/.agy-pro/providers.json in a conversational, step-by-step manner.
func InteractiveSetupWizard(in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Multi-Model Provider Setup Wizard")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "Configure external OpenAI-compatible LLM endpoints for fanout.")
	fmt.Fprintln(out, "Secrets are NEVER stored in providers.json (only env variable names).")
	fmt.Fprintln(out)

	// Step 1: Provider Name
	provName := promptString(reader, out, "Provider name (e.g., openai, deepseek, groq, openrouter)", "")
	if provName == "" {
		return errors.New("provider name cannot be empty")
	}

	// Step 2: Base URL
	defaultURL := defaultBaseURLFor(provName)
	baseURL := promptString(reader, out, "OpenAI-compatible Base URL", defaultURL)
	if baseURL == "" {
		return errors.New("base URL cannot be empty")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return fmt.Errorf("baseURL must start with http:// or https://: %s", baseURL)
	}

	// Step 3: API Key Environment Variable Name
	defaultEnv := defaultEnvFor(provName)
	apiKeyEnv := promptString(reader, out, "API Key environment variable name", defaultEnv)
	if apiKeyEnv != "" && !envNameRe.MatchString(apiKeyEnv) {
		return fmt.Errorf("invalid environment variable name: %s", apiKeyEnv)
	}

	// Step 4: API Key Value (optional entry)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Optional: Enter the API Key now to store it in your User environment.")
	fmt.Fprintln(out, "(Leave blank if you prefer setting it manually in your shell profile)")
	keyValue := promptString(reader, out, fmt.Sprintf("Value for %s (leave blank to skip)", apiKeyEnv), "")
	if keyValue != "" && apiKeyEnv != "" {
		_ = os.Setenv(apiKeyEnv, keyValue)
		if runtime.GOOS == "windows" {
			psScript := fmt.Sprintf(`[Environment]::SetEnvironmentVariable('%s', '%s', 'User')`, apiKeyEnv, keyValue)
			cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", psScript)
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(out, "Warning: could not persist %s to Windows user environment: %v\n", apiKeyEnv, err)
			} else {
				fmt.Fprintf(out, "Saved %s to Windows User Environment.\n", apiKeyEnv)
			}
		} else {
			fmt.Fprintf(out, "Set in current session. To persist across shells, add to ~/.bashrc or ~/.zshrc:\n")
			fmt.Fprintf(out, "  export %s=\"...\"\n", apiKeyEnv)
		}
	} else if apiKeyEnv != "" {
		keyValue = os.Getenv(apiKeyEnv)
	}

	// Step 5: Models configuration
	var models []Model
	for {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "--- Model Configuration ---")
		modelID := promptString(reader, out, "Model ID (e.g. gpt-4o, deepseek-chat)", "")
		if modelID == "" {
			if len(models) == 0 {
				fmt.Fprintln(out, "At least one model is required.")
				continue
			}
			break
		}

		inferredFam := InferFamily(modelID)
		family := promptString(reader, out, fmt.Sprintf("Model Family [%s]", inferredFam), inferredFam)
		if family == "" {
			family = inferredFam
		}

		ctxStr := promptString(reader, out, "Context Length in tokens (e.g. 128000)", "128000")
		ctxLen, _ := strconv.Atoi(ctxStr)
		if ctxLen <= 0 {
			ctxLen = 128000
		}

		maxOutStr := promptString(reader, out, "Max Output Tokens (e.g. 4096)", "4096")
		maxOut, _ := strconv.Atoi(maxOutStr)
		if maxOut <= 0 {
			maxOut = 4096
		}

		tagsStr := promptString(reader, out, "Tags (comma-separated, e.g. flagship,tools,coding)", "general,tools")
		var tags []string
		for _, t := range strings.Split(tagsStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}

		// Model Capabilities configuration
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Configure Capabilities for "+modelID+":")
		toolsAns := promptString(reader, out, "Supports Function Calling / Agent Tools? (Y/n)", "Y")
		supportsTools := !strings.EqualFold(toolsAns, "n") && !strings.EqualFold(toolsAns, "no")

		var allowedTools []string
		if supportsTools {
			toolsListStr := promptString(reader, out, "Allowed tools (comma-separated)", "read_file,list_dir,grep,glob")
			for _, t := range strings.Split(toolsListStr, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					allowedTools = append(allowedTools, t)
				}
			}
		}

		visionAns := promptString(reader, out, "Supports Vision / Images? (y/N)", "N")
		supportsVision := strings.EqualFold(visionAns, "y") || strings.EqualFold(visionAns, "yes")

		videoAns := promptString(reader, out, "Supports Video? (y/N)", "N")
		supportsVideo := strings.EqualFold(videoAns, "y") || strings.EqualFold(videoAns, "yes")

		thinkingAns := promptString(reader, out, "Supports Reasoning / Thinking mode? (y/N)", "N")
		supportsThinking := strings.EqualFold(thinkingAns, "y") || strings.EqualFold(thinkingAns, "yes")

		// ⚡ Live Connection & Capability Test Loop
		testModel := modelID
		testBaseURL := baseURL
		testKey := keyValue

		fmt.Fprintln(out)
		fmt.Fprintln(out, "Testing connection to endpoint...")
		for {
			latency, err := ProbeEndpoint(testBaseURL, testKey, testModel)
			if err == nil {
				fmt.Fprintf(out, "✅ Connection and basic call test succeeded (latency: %dms)\n", latency)
				break
			}

			fmt.Fprintf(out, "❌ Connection test failed: %v\n", err)
			retryAns, isEOF := promptLine(reader, out, "Would you like to edit settings and retry now? (Y/n)", "Y")
			if isEOF || strings.EqualFold(retryAns, "n") || strings.EqualFold(retryAns, "no") {
				skipSave := promptString(reader, out, "Save model configuration anyway? (y/N)", "y")
				if !strings.EqualFold(skipSave, "y") && !strings.EqualFold(skipSave, "yes") {
					fmt.Fprintln(out, "Discarded model.")
					testModel = ""
				}
				break
			}

			fmt.Fprintln(out, "Select field to edit:")
			fmt.Fprintln(out, "  1) Base URL")
			fmt.Fprintln(out, "  2) API Key")
			fmt.Fprintln(out, "  3) Model ID")
			fmt.Fprintln(out, "  4) Retry with current values")
			choice := promptString(reader, out, "Choice (1-4)", "4")
			switch choice {
			case "1":
				testBaseURL = promptString(reader, out, "New Base URL", testBaseURL)
				baseURL = testBaseURL
			case "2":
				testKey = promptString(reader, out, "New API Key", "")
				if apiKeyEnv != "" && testKey != "" {
					_ = os.Setenv(apiKeyEnv, testKey)
					keyValue = testKey
				}
			case "3":
				testModel = promptString(reader, out, "New Model ID", testModel)
				modelID = testModel
			}
		}

		if testModel != "" {
			models = append(models, Model{
				ID:              modelID,
				Family:          family,
				Tags:            tags,
				ContextLength:   ctxLen,
				MaxOutputTokens: maxOut,
				Capabilities: Capabilities{
					SupportsVision:   supportsVision,
					SupportsVideo:    supportsVideo,
					SupportsThinking: supportsThinking,
					SupportsTools:    supportsTools,
					AllowedTools:     allowedTools,
				},
			})
		}

		more := promptString(reader, out, "Add another model for this provider? (y/N)", "N")
		if !strings.EqualFold(more, "y") && !strings.EqualFold(more, "yes") {
			break
		}
	}

	if len(models) == 0 {
		return errors.New("no models configured; setup aborted")
	}

	// Step 6: Save into registry
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
	fmt.Fprintf(out, "Successfully saved provider %q (%d model(s)) to %s\n", provName, len(models), targetPath)
	return nil
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 5 * time.Second}
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

func promptLine(r *bufio.Reader, w io.Writer, prompt, defaultValue string) (string, bool) {
	if defaultValue != "" {
		fmt.Fprintf(w, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(w, "%s: ", prompt)
	}

	line, err := r.ReadString('\n')
	isEOF := errors.Is(err, io.EOF) || (err != nil && len(line) == 0)
	if err != nil && len(line) == 0 {
		return defaultValue, isEOF
	}
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
	switch strings.ToLower(name) {
	case "openai":
		return "https://api.openai.com/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "groq":
		return "https://api.groq.com/openai/v1"
	case "together":
		return "https://api.together.xyz/v1"
	case "mistral":
		return "https://api.mistral.ai/v1"
	default:
		return "https://api.openai.com/v1"
	}
}

func defaultEnvFor(name string) string {
	switch strings.ToLower(name) {
	case "openai":
		return "OPENAI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "together":
		return "TOGETHER_API_KEY"
	case "mistral":
		return "MISTRAL_API_KEY"
	default:
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		return upper + "_API_KEY"
	}
}
