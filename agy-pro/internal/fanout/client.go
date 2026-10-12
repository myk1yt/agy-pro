package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// ChatMessage represents a single message in the OpenAI chat completions schema.
type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatToolCall represents a function call request by the model.
type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

// ChatFunctionCall describes the specific function name and JSON arguments string.
type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is the payload sent to /chat/completions.
type ChatRequest struct {
	Model           string               `json:"model"`
	Messages        []ChatMessage        `json:"messages"`
	Tools           []agenttools.ToolDef `json:"tools,omitempty"`
	ToolChoice      any                  `json:"tool_choice,omitempty"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
}

// ChatResponse is the parsed response from /chat/completions.
type ChatResponse struct {
	ID      string       `json:"id"`
	Choices []ChatChoice `json:"choices"`
	Error   *APIError    `json:"error,omitempty"`
}

// ChatChoice represents an individual choice in the completion response.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// APIError represents an OpenAI API error object.
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
}

// Client executes OpenAI-compatible requests.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new Client with standard configuration.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Complete calls /chat/completions on the target provider.
func (c *Client) Complete(ctx context.Context, target providers.Target, messages []ChatMessage, tools []agenttools.ToolDef) (*ChatResponse, error) {
	reqBody := ChatRequest{
		Model:    target.Model.ID,
		Messages: messages,
	}

	// Apply MaxOutputTokens if explicitly positive (> 0).
	// When MaxOutputTokens is -1 or <= 0, max_tokens is omitted from the JSON
	// payload so the provider server utilizes its own maximum output capacity.
	// When ContextLength is -1 or <= 0, prompt/history messages are forwarded
	// completely without artificial client-side truncation.
	if target.Model.MaxOutputTokens > 0 {
		reqBody.MaxTokens = target.Model.MaxOutputTokens
	}

	// If model supports thinking, inject reasoning_effort according to configuration
	if target.Model.Capabilities.SupportsThinking {
		effort := mapReasoningEffort(target.Model.Capabilities.ReasoningEffort)
		if effort != "" {
			reqBody.ReasoningEffort = effort
		}
	}

	// Only attach tools if model supports tools and tools are provided
	if target.Model.Capabilities.CanUseTools() && len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}

	resp, statusCode, err := c.send(ctx, target, reqBody)
	if err != nil && reqBody.ReasoningEffort != "" {
		// If server failed with 400 (Bad Request) or 422, or mentions reasoning_effort,
		// gracefully fallback by stripping reasoning_effort and retrying immediately.
		isReasoningError := statusCode == http.StatusBadRequest || statusCode == 422 ||
			strings.Contains(strings.ToLower(err.Error()), "reasoning_effort")
		if isReasoningError {
			reqBody.ReasoningEffort = ""
			fallbackResp, _, fallbackErr := c.send(ctx, target, reqBody)
			if fallbackErr == nil {
				return fallbackResp, nil
			}
		}
	}

	return resp, err
}

func (c *Client) send(ctx context.Context, target providers.Target, reqBody ChatRequest) (*ChatResponse, int, error) {
	endpoint := target.Provider.Endpoint()

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if key := target.Provider.KeyValue(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("network error calling %s (%s): %w", target.Provider.Name, endpoint, err)
	}
	defer resp.Body.Close()

	bodyData, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(bodyData))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "... (truncated)"
		}
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d (%s) from provider %q: %s", resp.StatusCode, resp.Status, target.Provider.Name, snippet)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyData, &chatResp); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to parse response JSON: %w (body snippet: %s)", err, string(bodyData[:minInt(len(bodyData), 200)]))
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		return nil, resp.StatusCode, fmt.Errorf("API error from %q: %s", target.Provider.Name, chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("empty choices returned by provider %q", target.Provider.Name)
	}

	return &chatResp, resp.StatusCode, nil
}

// mapReasoningEffort standardizes the reasoning effort value for OpenAI-compatible APIs:
// "default" or empty: omitted (uses server default)
// "none": explicitly set to "none"
// "low", "medium", "high": forwarded directly
// "xhigh", "max": mapped to "high"
func mapReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none":
		return "none"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "high"
	case "default", "":
		return ""
	default:
		return ""
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
