package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ModelPreset represents a recommended model configuration for a provider.
type ModelPreset struct {
	ID              string       `json:"id"`
	Family          string       `json:"family,omitempty"`
	Description     string       `json:"description,omitempty"`
	Recommended     bool         `json:"recommended,omitempty"`
	ContextLength   int          `json:"contextLength,omitempty"`
	MaxOutputTokens int          `json:"maxOutputTokens,omitempty"`
	Tags            []string     `json:"tags,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
}

// GetModelPresets returns curated popular models for well-known providers.
func GetModelPresets(providerName string) []ModelPreset {
	norm := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(providerName)), "\ufeff")
	switch norm {
	case "deepseek":
		return []ModelPreset{
			{
				ID:              "deepseek-chat",
				Family:          "deepseek",
				Description:     "DeepSeek-V3 (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-reasoner",
				Family:          "deepseek",
				Description:     "DeepSeek-R1 (128k context, server max output, reasoning/thinking)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding", "math"},
				Capabilities: Capabilities{
					SupportsThinking: true,
				},
			},
		}

	case "openrouter":
		return []ModelPreset{
			{
				ID:              "anthropic/claude-3.7-sonnet",
				Family:          "claude",
				Description:     "Claude 3.7 Sonnet (200k context, server max output, hybrid thinking & tools)",
				Recommended:     true,
				ContextLength:   200000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools:    true,
					SupportsThinking: true,
					AllowedTools:     []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek/deepseek-r1",
				Family:          "deepseek",
				Description:     "DeepSeek R1 (128k context, server max output, reasoning/thinking)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding"},
				Capabilities: Capabilities{
					SupportsThinking: true,
				},
			},
			{
				ID:              "meta-llama/llama-3.3-70b-instruct",
				Family:          "llama",
				Description:     "Llama 3.3 70B Instruct (128k context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"tools", "open-weights"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "openai/gpt-4o",
				Family:          "gpt",
				Description:     "GPT-4o (128k context, server max output, multimodal & tools)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "vision", "tools"},
				Capabilities: Capabilities{
					SupportsTools:  true,
					SupportsVision: true,
					AllowedTools:   []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	case "groq":
		return []ModelPreset{
			{
				ID:              "llama-3.3-70b-versatile",
				Family:          "llama",
				Description:     "Llama 3.3 70B Versatile (Ultra-fast, 128k context, server max output, tools)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-r1-distill-llama-70b",
				Family:          "deepseek",
				Description:     "DeepSeek R1 Distill Llama 70B (Fast reasoning, 128k context, server max output)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "reasoning"},
				Capabilities: Capabilities{
					SupportsThinking: true,
				},
			},
		}

	case "openai":
		return []ModelPreset{
			{
				ID:              "gpt-4o",
				Family:          "gpt",
				Description:     "GPT-4o (128k context, server max output, flagship multimodal & tools)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "vision", "tools"},
				Capabilities: Capabilities{
					SupportsTools:  true,
					SupportsVision: true,
					AllowedTools:   []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "gpt-4o-mini",
				Family:          "gpt",
				Description:     "GPT-4o Mini (128k context, server max output, lightweight, tools enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "o3-mini",
				Family:          "o-series",
				Description:     "o3-mini (200k context, server max output, reasoning/STEM)",
				Recommended:     false,
				ContextLength:   200000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning", "coding"},
				Capabilities: Capabilities{
					SupportsThinking: true,
				},
			},
		}

	case "nw", "neuralwatt":
		return []ModelPreset{
			{
				ID:              "nw-flash",
				Family:          "nw",
				Description:     "NW Flash (High-throughput, 1M context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-v4-flash",
				Family:          "deepseek",
				Description:     "DeepSeek V4 Flash (1M context, server max output, fast inference)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "glm-5.3-flash",
				Family:          "glm",
				Description:     "GLM 5.3 Flash (1M context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "qwen-3.8-27b",
				Family:          "qwen",
				Description:     "Qwen 3.8 27B (1M context, server max output, tools enabled)",
				Recommended:     false,
				ContextLength:   1048576,
				MaxOutputTokens: -1,
				Tags:            []string{"tools", "coding"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	case "together":
		return []ModelPreset{
			{
				ID:              "meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
				Family:          "llama",
				Description:     "Llama 3.1 70B Turbo (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"fast", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "deepseek-ai/DeepSeek-R1",
				Family:          "deepseek",
				Description:     "DeepSeek R1 (128k context, server max output, reasoning enabled)",
				Recommended:     false,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"reasoning"},
				Capabilities: Capabilities{
					SupportsThinking: true,
				},
			},
		}

	case "mistral":
		return []ModelPreset{
			{
				ID:              "mistral-large-latest",
				Family:          "mistral",
				Description:     "Mistral Large Latest (128k context, server max output, tools enabled)",
				Recommended:     true,
				ContextLength:   128000,
				MaxOutputTokens: -1,
				Tags:            []string{"flagship", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
			{
				ID:              "codestral-latest",
				Family:          "mistral",
				Description:     "Codestral Latest (256k context, server max output, specialized coding)",
				Recommended:     false,
				ContextLength:   256000,
				MaxOutputTokens: -1,
				Tags:            []string{"coding", "tools"},
				Capabilities: Capabilities{
					SupportsTools: true,
					AllowedTools:  []string{"read_file", "list_dir", "grep", "glob"},
				},
			},
		}

	default:
		return nil
	}
}

// FetchRemoteModels queries <baseURL>/models to discover available models.
// If the endpoint is unreachable or does not support /models, returns an empty list.
func FetchRemoteModels(baseURL, apiKey string) []string {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var res struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil
	}

	var models []string
	for _, m := range res.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	return models
}

// isLikelyAPIKey returns true if the input resembles an API key/secret token
// rather than an environment variable name.
func isLikelyAPIKey(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	lower := strings.ToLower(val)
	for _, p := range []string{"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Environment variable names never have hyphens or dots, and are usually uppercase.
	if strings.Contains(val, "-") || strings.Contains(val, ".") {
		return true
	}
	// Very long strings (>= 24 chars) are almost always keys.
	if len(val) >= 24 {
		return true
	}
	return false
}

// persistWindowsUserEnv safely sets a persistent user environment variable
// on Windows using base64 encoding to avoid any shell escaping issues.
func persistWindowsUserEnv(key, value string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	b64Key := base64.StdEncoding.EncodeToString([]byte(key))
	b64Val := base64.StdEncoding.EncodeToString([]byte(value))
	script := fmt.Sprintf(
		`$k = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s')); `+
			`$v = [System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String('%s')); `+
			`[Environment]::SetEnvironmentVariable($k, $v, 'User')`,
		b64Key, b64Val,
	)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}

// getWindowsUserEnv queries HKCU\Environment for key via reg.exe as a fallback
// when an environment variable was saved to User env but current process hasn't inherited it.
func getWindowsUserEnv(key string) string {
	if runtime.GOOS != "windows" || strings.TrimSpace(key) == "" {
		return ""
	}
	cmd := exec.Command("reg", "query", `HKCU\Environment`, "/v", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, key) {
			for _, typ := range []string{"REG_SZ", "REG_EXPAND_SZ"} {
				if idx := strings.Index(l, typ); idx >= 0 {
					return strings.TrimSpace(l[idx+len(typ):])
				}
			}
		}
	}
	return ""
}

