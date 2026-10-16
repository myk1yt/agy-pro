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
	"agypro/internal/openaiup"
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
	// Usage is the provider's token accounting for this call. Nil when the
	// provider reports no usage block at all.
	Usage *ChatUsage `json:"usage,omitempty"`
	Error *APIError  `json:"error,omitempty"`
}

// ChatUsage is the token accounting a provider returns alongside a completion.
// Every field is optional and absent fields stay absent: a provider that does
// not break out cached tokens leaves PromptTokensDetails nil, and a provider
// that reports no usage at all leaves ChatResponse.Usage nil. Nothing is ever
// defaulted to zero, because a missing number and a real zero mean different
// things once the numbers become a cache-hit ratio.
type ChatUsage struct {
	PromptTokens        *int64                   `json:"prompt_tokens,omitempty"`
	CompletionTokens    *int64                   `json:"completion_tokens,omitempty"`
	TotalTokens         *int64                   `json:"total_tokens,omitempty"`
	PromptTokensDetails *ChatPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// ChatPromptTokensDetails is the OpenAI prompt_tokens_details sub-object, where
// providers that serve a prompt cache report prompt_tokens_details.cached_tokens.
type ChatPromptTokensDetails struct {
	CachedTokens *int64 `json:"cached_tokens,omitempty"`
}

// cachedTokens lifts prompt_tokens_details.cached_tokens out of the nested
// sub-object so callers do not repeat the two-level nil dance.
func (u *ChatUsage) cachedTokens() *int64 {
	if u == nil || u.PromptTokensDetails == nil {
		return nil
	}
	return u.PromptTokensDetails.CachedTokens
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

// requestTimeoutFloor is the per-request HTTP timeout floor for the shared
// provider client. The historical 120s cap stays as the floor so no-budget
// construction paths (NewClient) keep a ceiling; a swarm budget longer than
// the floor raises it (NewClientWithBudget). It is a var, not a const, only so
// tests can shrink it and observe the budget wiring end-to-end through
// Execute (see TestExecuteClientTimeoutTracksSwarmBudget).
var requestTimeoutFloor = 120 * time.Second

// Client executes OpenAI-compatible requests.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new Client with the standard no-budget configuration:
// the 120s per-request timeout floor. Prefer NewClientWithBudget when the
// swarm budget is known (engine.go wires RunConfig.TimeoutSec through it).
func NewClient() *Client {
	return NewClientWithBudget(0)
}

// NewClientWithBudget creates a Client whose per-request HTTP timeout is never
// shorter than the swarm budget: budgetSec seconds when it exceeds the 120s
// floor, otherwise the floor itself. The per-request timeout used to be a flat
// 120s, which killed a single provider chat call mid-seat under any longer
// swarm budget (the 2026-10-14 "context deadline exceeded" degradations in
// reports/fanout/20261014-042933: healthy endpoints, seats dead at 120s).
// Context propagation remains the hard backstop: Execute's ctxWithTimeout
// still bounds the whole swarm, so a longer client timeout can never outlive
// the budget.
func NewClientWithBudget(budgetSec int) *Client {
	timeout := requestTimeoutFloor
	if budgetSec > 0 {
		if budget := time.Duration(budgetSec) * time.Second; budget > timeout {
			timeout = budget
		}
	}
	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
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

	// Resolved once per send: the Authorization header and the redactor seed
	// below must never disagree, which they could if the environment changed
	// between two resolutions. The value is never logged.
	key := target.Provider.KeyValue()

	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	// S-7: the error snippets below quote provider-controlled text, and some
	// providers echo the credential they were handed straight back. The redactor
	// is seeded with this provider's resolved key value - resolved per call,
	// never logged - so the snippet is masked before it reaches an error string,
	// a seat report, a summary, or the CLI. providers.NewRedactor with no seed
	// still masks the generic bearer/sk-/AWS/auth-header/PEM shapes.
	redact := providers.NewRedactor()
	if key != "" {
		redact = providers.NewRedactor(key)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("network error calling %s (%s): %w", providers.MaskOperand(target.Provider.Name), endpoint, err)
	}
	defer resp.Body.Close()

	bodyData, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(redact(string(bodyData)))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "... (truncated)"
		}
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d (%s) from provider %q: %s", resp.StatusCode, resp.Status, providers.MaskOperand(target.Provider.Name), snippet)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyData, &chatResp); err != nil {
		// Redact the whole body before cutting: a key positioned across the
		// 200-byte cut would otherwise leak its unshaped fragment.
		snippet := redact(string(bodyData))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, resp.StatusCode, fmt.Errorf("failed to parse response JSON: %w (body snippet: %s)", err, snippet)
	}

	if chatResp.Error != nil && chatResp.Error.Message != "" {
		msg := redact(chatResp.Error.Message)
		if len(msg) > 500 {
			msg = msg[:500] + "... (truncated)"
		}
		return nil, resp.StatusCode, fmt.Errorf("API error from %q: %s", providers.MaskOperand(target.Provider.Name), msg)
	}

	if len(chatResp.Choices) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("empty choices returned by provider %q", providers.MaskOperand(target.Provider.Name))
	}

	return &chatResp, resp.StatusCode, nil
}

// mapReasoningEffort standardizes the reasoning effort value for OpenAI-compatible APIs:
// "default" or empty: omitted (uses server default)
// "none": explicitly set to "none"
// "low", "medium", "high": forwarded directly
// "xhigh", "max": mapped to "high"
//
// The mapping lives in internal/openaiup so the bridge and the geminiwire
// adapter apply the identical ladder.
func mapReasoningEffort(effort string) string {
	return openaiup.MapReasoningEffort(effort)
}
