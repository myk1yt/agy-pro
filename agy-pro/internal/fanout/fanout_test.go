package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

func TestFanout_ParallelDoubleBlind(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		resp := ChatResponse{
			Choices: []ChatChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role: "assistant",
						Content: `I have completed the review.
## Findings
- **[HIGH]** Nil Pointer Dereference: Missing nil check on context (Location: ctx.go:10)
- **[LOW]** Code Style: Inconsistent comment formatting
`,
					},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("MOCK_KEY_1", "key-1")
	t.Setenv("MOCK_KEY_2", "key-2")
	t.Setenv("MOCK_KEY_3", "key-3")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-a",
				BaseURL:   server.URL,
				APIKeyEnv: "MOCK_KEY_1",
				Models: []providers.Model{
					{ID: "model-a", Family: "family-a", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
			{
				Name:      "prov-b",
				BaseURL:   server.URL,
				APIKeyEnv: "MOCK_KEY_2",
				Models: []providers.Model{
					{ID: "model-b", Family: "family-b", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
			{
				Name:      "prov-c",
				BaseURL:   server.URL,
				APIKeyEnv: "MOCK_KEY_3",
				Models: []providers.Model{
					{ID: "model-c", Family: "family-c", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
		},
	}

	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	outDir := filepath.Join(tmpDir, "reports")
	cfg := RunConfig{
		Task:         "review",
		Prompt:       "Audit this module",
		N:            3,
		Root:         tmpDir,
		OutDir:       outDir,
		RegistryPath: regPath,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if summary.NCompleted != 3 {
		t.Fatalf("expected 3 completed seats, got %d", summary.NCompleted)
	}
	if summary.Degraded {
		t.Fatalf("expected degraded=false, got true")
	}
	if atomic.LoadInt32(&callCount) < 3 {
		t.Fatalf("expected at least 3 API calls, got %d", callCount)
	}

	// Verify generated reports
	if _, err := os.Stat(filepath.Join(outDir, "summary.json")); err != nil {
		t.Fatalf("summary.json not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "reconciliation.md")); err != nil {
		t.Fatalf("reconciliation.md not found: %v", err)
	}
}

func TestFanout_Failover_SameModelDifferentProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "bad-key") {
			// Fail provider 1 with 429
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
			return
		}
		// Provider 2 succeeds
		resp := ChatResponse{
			Choices: []ChatChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: "## Findings\n- **[MEDIUM]** Resolved via failover",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_BAD", "bad-key")
	t.Setenv("KEY_GOOD", "good-key")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-primary",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_BAD",
				Models: []providers.Model{
					{ID: "shared-model-42", Family: "shared", Enabled: &trueVal},
				},
			},
			{
				Name:      "prov-backup",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_GOOD",
				Models: []providers.Model{
					{ID: "shared-model-42", Family: "shared", Enabled: &trueVal},
				},
			},
		},
	}

	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "security",
		Prompt:       "Check vulnerabilities",
		N:            1,
		Models:       []string{"prov-primary/shared-model-42"},
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if summary.NCompleted != 1 {
		t.Fatalf("expected 1 completed seat, got %d", summary.NCompleted)
	}
	seat := summary.Seats[0]
	if seat.FinalTarget.Provider.Name != "prov-backup" {
		t.Fatalf("expected final provider to be prov-backup, got %s", seat.FinalTarget.Provider.Name)
	}
	if len(seat.Failovers) == 0 || seat.Failovers[0].Step != 1 {
		t.Fatalf("expected Step 1 failover record, got %+v", seat.Failovers)
	}
	if !summary.Degraded {
		t.Fatalf("expected degraded=true after failover")
	}
}

func TestFanout_Failover_DifferentFamily(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "dead-key") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": {"message": "Service Unavailable"}}`))
			return
		}
		resp := ChatResponse{
			Choices: []ChatChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: "## Findings\n- **[INFO]** Recovered via different family",
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_DEAD", "dead-key")
	t.Setenv("KEY_ALT", "alt-key")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-dead",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_DEAD",
				Models: []providers.Model{
					{ID: "dead-model", Family: "dead-fam", Enabled: &trueVal},
				},
			},
			{
				Name:      "prov-alt",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_ALT",
				Models: []providers.Model{
					{ID: "alt-model", Family: "alt-fam", Enabled: &trueVal},
				},
			},
		},
	}

	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Models:       []string{"prov-dead/dead-model"},
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	seat := summary.Seats[0]
	if seat.FinalTarget.Model.ID != "alt-model" {
		t.Fatalf("expected alt-model, got %s", seat.FinalTarget.Model.ID)
	}
	if len(seat.Failovers) == 0 || seat.Failovers[0].Step != 2 {
		t.Fatalf("expected Step 2 failover record, got %+v", seat.Failovers)
	}
}

func TestFanout_Failover_Degraded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid API key"}}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("FAIL_KEY", "fkey")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-fail",
				BaseURL:   server.URL,
				APIKeyEnv: "FAIL_KEY",
				Models: []providers.Model{
					{ID: "model-fail", Family: "fam-fail", Enabled: &trueVal},
				},
			},
		},
	}

	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}

	if summary.NCompleted != 0 {
		t.Fatalf("expected 0 completed seats, got %d", summary.NCompleted)
	}
	if !summary.Degraded {
		t.Fatalf("expected summary.Degraded == true")
	}
	if summary.Seats[0].Status != "failed" {
		t.Fatalf("expected seat status failed, got %s", summary.Seats[0].Status)
	}
}

func TestFanout_AgenticToolLoop(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "vulnerable.go"), []byte("func execute() { panic(nil) }"), 0o644)

	var step int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		currentStep := atomic.AddInt32(&step, 1)
		w.Header().Set("Content-Type", "application/json")

		if currentStep == 1 {
			// Step 1: Model requests read_file
			resp := ChatResponse{
				Choices: []ChatChoice{
					{
						Index: 0,
						Message: ChatMessage{
							Role: "assistant",
							ToolCalls: []ChatToolCall{
								{
									ID:   "call_read_1",
									Type: "function",
									Function: ChatFunctionCall{
										Name:      "read_file",
										Arguments: `{"path":"vulnerable.go"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Step 2: Model finishes with findings based on tool output
		resp := ChatResponse{
			Choices: []ChatChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role: "assistant",
						Content: `I inspected the file.
## Findings
- **[BLOCKER]** Panic On Nil: Function panics unconditionally (Location: vulnerable.go:1)
`,
					},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("TOOL_KEY", "tool-key")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-tool",
				BaseURL:   server.URL,
				APIKeyEnv: "TOOL_KEY",
				Models: []providers.Model{
					{
						ID:      "tool-model",
						Family:  "tool-fam",
						Enabled: &trueVal,
						Capabilities: providers.Capabilities{
							SupportsTools: true,
							AllowedTools:  []string{"read_file"},
						},
					},
				},
			},
		},
	}

	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "security",
		N:            1,
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		MaxTurns:     5,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	seat := summary.Seats[0]
	if seat.ToolCallCount != 1 {
		t.Fatalf("expected 1 tool call, got %d", seat.ToolCallCount)
	}
	if len(summary.Issues) != 1 || summary.Issues[0].Severity != "BLOCKER" {
		t.Fatalf("unexpected issues: %+v", summary.Issues)
	}
}

func TestFanout_ReconciliationLogic(t *testing.T) {
	seats := []*SeatResult{
		{
			SeatID: 1,
			FinalTarget: providers.Target{
				Provider: providers.Provider{Name: "openai"},
				Model:    providers.Model{ID: "gpt-4o", Family: "openai"},
			},
			Status: "success",
			Content: `## Findings
- **[HIGH]** Buffer Pool Leak: Memory leak in worker pool (Location: pool.go:42)
- **[LOW]** Style Inconsistency: Formatting mismatch
`,
		},
		{
			SeatID: 2,
			FinalTarget: providers.Target{
				Provider: providers.Provider{Name: "deepseek"},
				Model:    providers.Model{ID: "deepseek-chat", Family: "deepseek"},
			},
			Status: "success",
			Content: `## Findings
- **[HIGH]** Buffer Pool Leak: Resource leak in worker pool (Location: pool.go:42)
`,
		},
		{
			SeatID: 3,
			FinalTarget: providers.Target{
				Provider: providers.Provider{Name: "openrouter"},
				Model:    providers.Model{ID: "claude-3.5", Family: "anthropic"},
			},
			Status: "success",
			Content: `## Findings
- **[BLOCKER]** SQL Injection: Raw query construction in auth (Location: auth.go:15)
`,
		},
	}

	tmpDir := t.TempDir()
	summary, err := Reconcile("review", tmpDir, filepath.Join(tmpDir, "reports"), 3, seats)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Buffer Pool Leak should be CONFIRMED (Seat 1 and Seat 2 agreed)
	// SQL Injection should be DISPUTED (Only Seat 3 cited it, and severity is BLOCKER)
	var confirmedFound, disputedFound bool
	for _, iss := range summary.Issues {
		if strings.Contains(strings.ToLower(iss.Title), "buffer") {
			if iss.Status != StatusConfirmed {
				t.Fatalf("expected Buffer issue to be CONFIRMED, got %s", iss.Status)
			}
			confirmedFound = true
		}
		if strings.Contains(strings.ToLower(iss.Title), "sql") {
			if iss.Status != StatusDisputed {
				t.Fatalf("expected SQL issue to be DISPUTED, got %s", iss.Status)
			}
			disputedFound = true
		}
	}

	if !confirmedFound || !disputedFound {
		t.Fatalf("reconciliation failed to classify issues properly: %+v", summary.Issues)
	}
}

func TestFanout_CLI_FlagParsing(t *testing.T) {
	out := &bytes.Buffer{}
	errw := &bytes.Buffer{}

	// Test help
	code := Run([]string{"--help"}, nil, out, errw)
	if code != 0 && code != 2 {
		t.Fatalf("expected exit code 0 or 2 for help, got %d", code)
	}
	if !strings.Contains(errw.String(), "Usage: agy pro fanout") {
		t.Fatalf("expected usage output, got: %s", errw.String())
	}
}

func TestClient_TokenHandling(t *testing.T) {
	var receivedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient()

	// 1. MaxOutputTokens == -1: max_tokens must be omitted from JSON
	targetMinusOne := providers.Target{
		Provider: providers.Provider{Name: "test-prov", BaseURL: server.URL},
		Model: providers.Model{
			ID:              "test-model",
			MaxOutputTokens: -1,
			ContextLength:   -1,
		},
	}
	receivedBody = nil
	_, err := client.Complete(context.Background(), targetMinusOne, []ChatMessage{
		{Role: "user", Content: "hello server maximum"},
	}, nil)
	if err != nil {
		t.Fatalf("client.Complete failed: %v", err)
	}
	if _, ok := receivedBody["max_tokens"]; ok {
		t.Errorf("expected max_tokens to be omitted for MaxOutputTokens=-1, got: %v", receivedBody["max_tokens"])
	}

	// 2. MaxOutputTokens == 0: max_tokens must be omitted from JSON
	targetZero := providers.Target{
		Provider: providers.Provider{Name: "test-prov", BaseURL: server.URL},
		Model: providers.Model{
			ID:              "test-model",
			MaxOutputTokens: 0,
			ContextLength:   1048576,
		},
	}
	receivedBody = nil
	_, err = client.Complete(context.Background(), targetZero, []ChatMessage{
		{Role: "user", Content: "hello zero"},
	}, nil)
	if err != nil {
		t.Fatalf("client.Complete failed: %v", err)
	}
	if _, ok := receivedBody["max_tokens"]; ok {
		t.Errorf("expected max_tokens to be omitted for MaxOutputTokens=0, got: %v", receivedBody["max_tokens"])
	}

	// 3. MaxOutputTokens == 131072: max_tokens must be present in JSON
	targetExplicit := providers.Target{
		Provider: providers.Provider{Name: "test-prov", BaseURL: server.URL},
		Model: providers.Model{
			ID:              "test-model",
			MaxOutputTokens: 131072,
			ContextLength:   1048576,
		},
	}
	receivedBody = nil
	_, err = client.Complete(context.Background(), targetExplicit, []ChatMessage{
		{Role: "user", Content: "hello 128k output"},
	}, nil)
	if err != nil {
		t.Fatalf("client.Complete failed: %v", err)
	}
	maxTokensVal, ok := receivedBody["max_tokens"]
	if !ok {
		t.Fatalf("expected max_tokens to be present for MaxOutputTokens=131072")
	}
	if floatVal, ok := maxTokensVal.(float64); !ok || int(floatVal) != 131072 {
		t.Errorf("max_tokens = %v; want 131072", maxTokensVal)
	}

	// 4. Large message content with ContextLength == -1: no client truncation
	largeContent := strings.Repeat("A", 50000)
	receivedBody = nil
	_, err = client.Complete(context.Background(), targetMinusOne, []ChatMessage{
		{Role: "user", Content: largeContent},
	}, nil)
	if err != nil {
		t.Fatalf("client.Complete failed: %v", err)
	}
	msgs, ok := receivedBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("expected messages array in request body")
	}
	firstMsg := msgs[0].(map[string]any)
	if firstMsg["content"] != largeContent {
		t.Errorf("expected untruncated content of length %d, got length %d", len(largeContent), len(firstMsg["content"].(string)))
	}
}

func TestMapReasoningEffort(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"default", ""},
		{"", ""},
		{"none", "none"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "high"},
		{"UNKNOWN", ""},
	}
	for _, c := range cases {
		got := mapReasoningEffort(c.input)
		if got != c.expected {
			t.Errorf("mapReasoningEffort(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestClient_ReasoningEffort_MappingAndFallback(t *testing.T) {
	var requestBodies []map[string]any
	var return400OnReasoningEffort bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		requestBodies = append(requestBodies, parsed)

		if return400OnReasoningEffort {
			if _, hasEffort := parsed["reasoning_effort"]; hasEffort {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"message":"Unrecognized request argument: reasoning_effort"}}`))
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer server.Close()

	client := NewClient()

	// Case 1: SupportsThinking = true, ReasoningEffort = "default" -> reasoning_effort omitted
	targetDefault := providers.Target{
		Provider: providers.Provider{Name: "test-prov", BaseURL: server.URL},
		Model: providers.Model{
			ID: "model-default",
			Capabilities: providers.Capabilities{
				SupportsThinking: true,
				ReasoningEffort:  "default",
			},
		},
	}
	requestBodies = nil
	return400OnReasoningEffort = false
	resp, err := client.Complete(context.Background(), targetDefault, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed for default effort: %v", err)
	}
	if resp.Choices[0].Message.Content != "pong" {
		t.Errorf("expected pong, got %s", resp.Choices[0].Message.Content)
	}
	if len(requestBodies) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requestBodies))
	}
	if _, ok := requestBodies[0]["reasoning_effort"]; ok {
		t.Errorf("expected reasoning_effort to be omitted for 'default', but got: %v", requestBodies[0]["reasoning_effort"])
	}

	// Case 2: SupportsThinking = true, ReasoningEffort = "high" -> sends "high"
	targetHigh := providers.Target{
		Provider: providers.Provider{Name: "test-prov", BaseURL: server.URL},
		Model: providers.Model{
			ID: "model-high",
			Capabilities: providers.Capabilities{
				SupportsThinking: true,
				ReasoningEffort:  "high",
			},
		},
	}
	requestBodies = nil
	resp, err = client.Complete(context.Background(), targetHigh, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed for high effort: %v", err)
	}
	if len(requestBodies) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requestBodies))
	}
	if val, ok := requestBodies[0]["reasoning_effort"]; !ok || val != "high" {
		t.Errorf("expected reasoning_effort='high', got: %v", val)
	}

	// Case 3: SupportsThinking = true, ReasoningEffort = "high", server returns 400
	// Graceful fallback triggers: retries immediately without reasoning_effort and succeeds!
	requestBodies = nil
	return400OnReasoningEffort = true
	resp, err = client.Complete(context.Background(), targetHigh, []ChatMessage{{Role: "user", Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed during graceful fallback: %v", err)
	}
	if resp.Choices[0].Message.Content != "pong" {
		t.Errorf("expected pong after fallback, got %s", resp.Choices[0].Message.Content)
	}
	if len(requestBodies) != 2 {
		t.Fatalf("expected exactly 2 requests (initial + fallback retry), got %d", len(requestBodies))
	}
	// First request had reasoning_effort
	if _, ok := requestBodies[0]["reasoning_effort"]; !ok {
		t.Errorf("expected first request to have reasoning_effort")
	}
	// Second request stripped reasoning_effort
	if _, ok := requestBodies[1]["reasoning_effort"]; ok {
		t.Errorf("expected retry request to have reasoning_effort omitted, got: %v", requestBodies[1]["reasoning_effort"])
	}
}

func TestSeatWorker_ToolPanicConvergesToFailedSeat(t *testing.T) {
	// Regression: a tool-layer panic (historically agenttools.Grep's nil
	// DirEntry dereference on a missing root) must not kill the swarm. The
	// panicking seat converges to Status=failed with the panic excerpt, a
	// sibling seat keeps running, and reconcile/state/log outputs stay intact.
	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	_ = os.WriteFile(filepath.Join(tmpDir, "target.txt"), []byte("hello\n"), 0o644)

	var okStep int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")

		if req.Model == "panic-model" {
			// The historical panic-inducing tool input: grep on a missing path.
			resp := ChatResponse{Choices: []ChatChoice{{
				Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ChatToolCall{{
						ID:   "call_panic",
						Type: "function",
						Function: ChatFunctionCall{
							Name:      "grep",
							Arguments: `{"pattern":"x","path":"does/not/exist"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if atomic.AddInt32(&okStep, 1) == 1 {
			resp := ChatResponse{Choices: []ChatChoice{{
				Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ChatToolCall{{
						ID:   "call_read",
						Type: "function",
						Function: ChatFunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"target.txt"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[INFO]** Healthy seat finished"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logPath := filepath.Join(tmpDir, "logs", "fanout-test.jsonl")
	logger, err := agenttools.NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	trueVal := true
	panicWorker := &SeatWorker{
		SeatID: 1,
		InitialTarget: providers.Target{
			Provider: providers.Provider{Name: "prov-panic", BaseURL: server.URL, APIKeyEnv: "PANIC_KEY"},
			Model:    providers.Model{ID: "panic-model", Family: "fam-panic", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
		},
		Registry:             &providers.Registry{},
		Preset:               GetTaskPreset("review"),
		UserPrompt:           "audit this module",
		Runner:               nil, // nil runner makes tool execution panic (stands in for any tool-layer panic)
		Client:               NewClient(),
		MaxTurns:             3,
		Tools:                agenttools.BuildOpenAITools(false, false),
		AllowToolsForSession: true,
	}
	okWorker := &SeatWorker{
		SeatID: 2,
		InitialTarget: providers.Target{
			Provider: providers.Provider{Name: "prov-ok", BaseURL: server.URL, APIKeyEnv: "OK_KEY"},
			Model:    providers.Model{ID: "ok-model", Family: "fam-ok", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
		},
		Registry:             &providers.Registry{},
		Preset:               GetTaskPreset("review"),
		UserPrompt:           "audit this module",
		Runner:               agenttools.NewRunner(tmpDir, false, false, logger),
		Client:               NewClient(),
		MaxTurns:             3,
		Tools:                agenttools.BuildOpenAITools(false, false),
		AllowToolsForSession: true,
	}

	results := make([]*SeatResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = panicWorker.Run(context.Background())
	}()
	go func() {
		defer wg.Done()
		results[1] = okWorker.Run(context.Background())
	}()
	wg.Wait()

	panicRes := results[0]
	if panicRes == nil {
		t.Fatal("panic seat returned nil result")
	}
	if panicRes.Status != "failed" {
		t.Fatalf("panic seat: expected status failed, got %s", panicRes.Status)
	}
	if !panicRes.Degraded {
		t.Fatal("panic seat: expected degraded=true")
	}
	if !strings.Contains(panicRes.Error, "panic") {
		t.Fatalf("panic seat: expected error to contain panic excerpt, got %q", panicRes.Error)
	}

	okRes := results[1]
	if okRes == nil || okRes.Status != "success" {
		t.Fatalf("sibling seat must keep running and succeed, got %+v", okRes)
	}
	if okRes.ToolCallCount != 1 {
		t.Fatalf("sibling seat: expected 1 tool call, got %d", okRes.ToolCallCount)
	}

	// Reconcile must survive the panic seat and still write every report.
	outDir := filepath.Join(tmpDir, "reports")
	summary, err := Reconcile("review", tmpDir, outDir, 2, results)
	if err != nil {
		t.Fatalf("Reconcile broke on a panic seat: %v", err)
	}
	if summary.NCompleted != 1 {
		t.Fatalf("expected 1 completed seat, got %d", summary.NCompleted)
	}
	if !summary.Degraded {
		t.Fatal("expected summary.Degraded=true for a panic seat")
	}
	if len(summary.Seats) != 2 {
		t.Fatalf("expected 2 seat records in summary, got %d", len(summary.Seats))
	}
	for _, f := range []string{
		filepath.Join(outDir, "summary.json"),
		filepath.Join(outDir, "reconciliation.md"),
		filepath.Join(outDir, "seat-1-prov-panic-panic-model.md"),
		filepath.Join(outDir, "seat-2-prov-ok-ok-model.md"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected report %s: %v", f, err)
		}
	}

	// Rotation state recording must survive the panic seat too.
	state := &FanoutState{}
	state.RecordRun("review", []SeatRecord{
		{SeatID: panicRes.SeatID, Provider: "prov-panic", Model: "panic-model", Family: "fam-panic", Status: panicRes.Status},
		{SeatID: okRes.SeatID, Provider: "prov-ok", Model: "ok-model", Family: "fam-ok", Status: okRes.Status},
	})
	if err := SaveState(state); err != nil {
		t.Fatalf("SaveState broke on a panic seat: %v", err)
	}
	loaded, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if len(loaded.History) != 1 || len(loaded.History[0].Seats) != 2 {
		t.Fatalf("state history corrupted: %+v", loaded.History)
	}
	if loaded.History[0].Seats[0].Status != "failed" {
		t.Fatalf("expected failed status persisted for panic seat, got %s", loaded.History[0].Seats[0].Status)
	}

	// The shared tool log must remain valid JSONL despite the panic seat.
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("tool log missing after panic seat: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("tool log is empty; sibling seat's tool call was not recorded")
	}
	for i, line := range lines {
		var entry agenttools.LogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("tool log line %d corrupted: %v", i+1, err)
		}
	}
	if !strings.Contains(string(logData), "read_file") {
		t.Fatalf("tool log missing sibling seat entry: %s", string(logData))
	}
}

func TestFanout_GrepMissingRootToolInput_DoesNotCrashSwarm(t *testing.T) {
	// Integration regression for the historical panic input: a model tool call
	// to grep with a nonexistent root used to nil-deref inside the walk
	// callback and kill the whole swarm. It must now surface as a tool error
	// message, and the seat must converge normally.
	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)

	var mu sync.Mutex
	var requestBodies []string

	var step int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if atomic.AddInt32(&step, 1) == 1 {
			resp := ChatResponse{Choices: []ChatChoice{{
				Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ChatToolCall{{
						ID:   "call_grep_missing",
						Type: "function",
						Function: ChatFunctionCall{
							Name:      "grep",
							Arguments: `{"pattern":"x","path":"does/not/exist"}`,
						},
					}},
				},
				FinishReason: "tool_calls",
			}}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[INFO]** Finished despite tool error"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	t.Setenv("GREP_KEY", "grep-key")

	trueVal := true
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "prov-grep",
				BaseURL:   server.URL,
				APIKeyEnv: "GREP_KEY",
				Models: []providers.Model{
					{ID: "grep-model", Family: "grep-fam", Enabled: &trueVal, Capabilities: providers.Capabilities{SupportsTools: true}},
				},
			},
		},
	}
	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		Prompt:       "audit",
		N:            1,
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		MaxTurns:     5,
		TimeoutSec:   10,
	}

	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute must not fail on missing-root tool input: %v", err)
	}
	if summary.NCompleted != 1 {
		t.Fatalf("expected seat to converge to success after tool error, got %d completed", summary.NCompleted)
	}
	seat := summary.Seats[0]
	if seat.ToolCallCount != 1 {
		t.Fatalf("expected 1 tool call, got %d", seat.ToolCallCount)
	}

	// The tool error (not a panic) must be fed back to the model on the next turn.
	mu.Lock()
	captured := strings.Join(requestBodies, "\n")
	mu.Unlock()
	if !strings.Contains(captured, "does not exist") {
		t.Fatalf("expected tool error feedback in follow-up request, got: %s", captured)
	}
}

// --- F-C pool-policy exclusion (contract §2.5b) -------------------------------
// k3-family models must never be AUTO-seated via failover or rotation; explicit
// user picks stay exempt.

func TestFC_FailoverSkipsK3SameModelAlternate(t *testing.T) {
	// Step-1 failover: the only same-model alternate is a k3 id -> skipped;
	// the seat degrades on the original target (no auto-seat onto k3).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "bad-key") {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "Rate limit exceeded"}}`))
			return
		}
		// If the k3 backup were auto-seated it would answer; see assertions.
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** k3 answer"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_FC1_BAD", "bad-key")
	t.Setenv("KEY_FC1_GOOD", "good-key")

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "prov-primary",
			BaseURL:   server.URL,
			APIKeyEnv: "KEY_FC1_BAD",
			Models:    []providers.Model{{ID: "kimi-k3", Family: "kimi", Enabled: &trueVal}},
		},
		{
			Name:      "prov-backup",
			BaseURL:   server.URL,
			APIKeyEnv: "KEY_FC1_GOOD",
			Models:    []providers.Model{{ID: "kimi-k3", Family: "kimi", Enabled: &trueVal}},
		},
	}}
	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Models:       []string{"prov-primary/kimi-k3"}, // explicit pick: exempt
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	seat := summary.Seats[0]
	if seat.FinalTarget.Provider.Name != "prov-primary" {
		t.Fatalf("k3 alternate must not be auto-seated, final provider = %s", seat.FinalTarget.Provider.Name)
	}
	if len(seat.Failovers) != 0 {
		t.Fatalf("failover onto a k3 model must be skipped, got %+v", seat.Failovers)
	}
}

func TestFC_FailoverSkipsK3DifferentFamilyAlternate(t *testing.T) {
	// Step-2 failover: the only different-family alternate is a k3 id ->
	// skipped; seat exhausts without auto-seating k3.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.Contains(auth, "dead-key") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": {"message": "Service Unavailable"}}`))
			return
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** k3 answer"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_FC2_DEAD", "dead-key")
	t.Setenv("KEY_FC2_K3", "k3-key")

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "prov-dead",
			BaseURL:   server.URL,
			APIKeyEnv: "KEY_FC2_DEAD",
			Models:    []providers.Model{{ID: "dead-model", Family: "dead-fam", Enabled: &trueVal}},
		},
		{
			Name:      "prov-k3",
			BaseURL:   server.URL,
			APIKeyEnv: "KEY_FC2_K3",
			Models:    []providers.Model{{ID: "k3-agent-swarm", Family: "k", Enabled: &trueVal}},
		},
	}}
	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Models:       []string{"prov-dead/dead-model"},
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	seat := summary.Seats[0]
	if seat.FinalTarget.Model.ID != "dead-model" {
		t.Fatalf("k3 alternate must not be auto-seated, final model = %s", seat.FinalTarget.Model.ID)
	}
	if len(seat.Failovers) != 0 {
		t.Fatalf("failover onto a k3 model must be skipped, got %+v", seat.Failovers)
	}
}

func TestFC_RotationNeverPicksK3(t *testing.T) {
	// Family rotation (SelectTargets auto path) must never seat a k3 model.
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "prov-k3",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_FC3_K3",
			Models: []providers.Model{
				{ID: "kimi-k3", Family: "kimi", Enabled: &trueVal},
				{ID: "kimi-k3-flex", Family: "kimi", Enabled: &trueVal},
			},
		},
		{
			Name:      "prov-ok",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_FC3_OK",
			Models: []providers.Model{
				{ID: "glm-4.6", Family: "glm", Enabled: &trueVal},
			},
		},
	}}
	t.Setenv("KEY_FC3_K3", "k3-key")
	t.Setenv("KEY_FC3_OK", "ok-key")

	// (a) k3 + one clean model -> only the clean model is auto-seated.
	// P4-3: the seam is the policy adapter (provider name + model), and the
	// zero policy is the built-in default the engine substitutes.
	builtIn := PolicyExcluder(providers.ExclusionPolicy{})
	targets, err := SelectTargets(reg, nil, 3, nil, builtIn, nil)
	if err != nil {
		t.Fatalf("SelectTargets failed: %v", err)
	}
	for _, tg := range targets {
		if providers.IsK3Family(tg.Model) {
			t.Fatalf("rotation auto-seated a k3 model: %+v", tg)
		}
	}
	if len(targets) != 1 || targets[0].Model.ID != "glm-4.6" {
		t.Fatalf("targets = %+v; want only glm-4.6", targets)
	}

	// (b) k3-only pool -> actionable exclusion-list error, never a k3 seat.
	k3Only := &providers.Registry{Providers: []providers.Provider{reg.Providers[0]}}
	if _, err := SelectTargets(k3Only, nil, 3, nil, builtIn, nil); err == nil ||
		!strings.Contains(err.Error(), "excluded by the exclusion list") {
		t.Fatalf("k3-only pool error = %v; want an exclusion-list message", err)
	}
}

func TestFC_ExplicitPickExemptFromK3Exclusion(t *testing.T) {
	// Explicit --models picks are honored as-is (pool policy governs
	// AUTO-seating only).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** explicit k3 ok"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_FC4", "good-key")

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   server.URL,
		APIKeyEnv: "KEY_FC4",
		Models:    []providers.Model{{ID: "kimi-k3", Family: "kimi", Enabled: &trueVal}},
	}}}
	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Models:       []string{"nw/kimi-k3"},
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("explicit k3 pick must resolve, got %v", err)
	}
	if summary.NCompleted != 1 || summary.Seats[0].FinalTarget.Model.ID != "kimi-k3" {
		t.Fatalf("explicit pick not honored: %+v", summary.Seats)
	}
}

// --- SESSION7 P4-3 (D1/D2=b): the user-owned exclusion list
// (providers.json defaults.excludeModels) threaded through the fanout
// subsystem. Auto-seating and failover skip what the list names; an explicit
// pick is honored and warned about; nothing is ever rejected.

// exclusionWarningCore is the shared sentence body, pinned verbatim. The swarm
// subsystem prints the same words, so this constant is what makes a drift in
// either subsystem fail a test.
const exclusionWarningCore = " is excluded (exclusion list); explicit pick honored (the exclusion list covers auto-seating and failover only)"

// exclusionWarningSuffix is the rendered warning for one provider/model pair,
// matching warnExplicitPick's target rendering (registry-slot masking leaves a
// benign name untouched).
func exclusionWarningSuffix(providerName, modelID string) string {
	return providerName + "/" + modelID + exclusionWarningCore
}

// TestExclusion_ProviderEntrySkipsAutoSeating: a user list entry that names a
// PROVIDER excludes every model of that provider from auto-seating.
//
// Non-vacuity control: the pre-change seam (func(providers.Model) bool) could
// only read the model id, and the same list applied id-only leaves the named
// provider's models seatable. The control sub-assertion below reproduces that
// reading, so this test cannot pass against the pre-change behavior.
func TestExclusion_ProviderEntrySkipsAutoSeating(t *testing.T) {
	trueVal := true
	t.Setenv("KEY_EX1_NW", "nw-key")
	t.Setenv("KEY_EX1_OK", "ok-key")
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "nw",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_EX1_NW",
			Models: []providers.Model{
				{ID: "glm-5.3-pro", Family: "glm", Enabled: &trueVal},
				{ID: "glm-5.3-flash", Family: "glm", Enabled: &trueVal},
			},
		},
		{
			Name:      "fallback",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_EX1_OK",
			Models:    []providers.Model{{ID: "clean-model", Family: "clean", Enabled: &trueVal}},
		},
	}}

	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})
	if !policy.UserOwned() {
		t.Fatal("premise broken: a non-nil excludeModels list must be user-owned")
	}
	targets, err := SelectTargets(reg, nil, 3, nil, PolicyExcluder(policy), nil)
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	for _, tg := range targets {
		if tg.Provider.Name == "nw" {
			t.Fatalf("auto-seating seated an excluded provider: %s", tg.FullName())
		}
	}
	if len(targets) != 1 || targets[0].Model.ID != "clean-model" {
		t.Fatalf("targets = %+v; want only fallback/clean-model", targets)
	}

	// Control: the id-only reading of the SAME list (all the pre-change seam
	// could express) leaves the nw models seatable.
	idOnly := func(_ string, m providers.Model) bool { return m.ID == "nw" }
	controls, err := SelectTargets(reg, nil, 3, nil, idOnly, nil)
	if err != nil {
		t.Fatalf("control SelectTargets: %v", err)
	}
	seated := false
	for _, c := range controls {
		if c.Provider.Name == "nw" {
			seated = true
		}
	}
	if !seated {
		t.Fatal("control premise broken: an id-only predicate must leave the nw models seatable (the provider-name arm is what this test pins)")
	}
}

// TestExclusion_AbsentKeyKeepsBuiltInDefault: with NO excludeModels key in the
// registry file the loaded defaults stay nil, the policy stays user-less, and
// the built-in default (the k3 rule) still governs auto-seating.
func TestExclusion_AbsentKeyKeepsBuiltInDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	// Written as raw JSON on purpose: marshaling a Registry struct always emits
	// the defaults block (struct fields never satisfy omitempty), so only a
	// hand-written body proves the ABSENT-key case.
	body := `{"providers":[
	  {"name":"p-k3","baseURL":"https://example.invalid/v1","apiKeyEnv":"KEY_EX2_K3",
	   "models":[{"id":"kimi-k3","family":"kimi","enabled":true}]},
	  {"name":"p-ok","baseURL":"https://example.invalid/v1","apiKeyEnv":"KEY_EX2_OK",
	   "models":[{"id":"glm-4.6","family":"glm","enabled":true}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEY_EX2_K3", "k3-key")
	t.Setenv("KEY_EX2_OK", "ok-key")

	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("providers.LoadFrom: %v", err)
	}
	if reg.Defaults.ExcludeModels != nil {
		t.Fatalf("premise broken: absent key must load as nil, got %v", reg.Defaults.ExcludeModels)
	}
	policy := providers.ExclusionPolicyFromDefaults(reg.Defaults)
	if policy.UserOwned() {
		t.Fatal("an absent excludeModels key must NOT be user-owned")
	}
	targets, err := SelectTargets(reg, nil, 3, nil, PolicyExcluder(policy), nil)
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Model.ID != "glm-4.6" {
		t.Fatalf("targets = %+v; want only glm-4.6 (the built-in k3 default must still apply)", targets)
	}

	// Control: the user-owned EMPTY list (the other side of the nil-vs-empty
	// split) seats the k3 model, so the skip above is the built-in default
	// firing and not an artifact of the registry.
	emptyPolicy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{}})
	emptyTargets, err := SelectTargets(reg, nil, 3, nil, PolicyExcluder(emptyPolicy), nil)
	if err != nil {
		t.Fatalf("SelectTargets (explicit empty list): %v", err)
	}
	foundK3 := false
	for _, tg := range emptyTargets {
		if tg.Model.ID == "kimi-k3" {
			foundK3 = true
		}
	}
	if !foundK3 {
		t.Fatalf("control premise broken: an explicit empty list must exclude nothing, got %+v", emptyTargets)
	}
}

// TestExclusion_EmptyUserListExcludesNothing: an explicit empty list is
// user-owned and excludes nothing, so a k3-only pool is fully seatable (the
// pre-change behavior erroring out on the k3 default is what this pins).
func TestExclusion_EmptyUserListExcludesNothing(t *testing.T) {
	trueVal := true
	t.Setenv("KEY_EX3_K3", "k3-key")
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "p-k3",
		BaseURL:   "https://example.invalid/v1",
		APIKeyEnv: "KEY_EX3_K3",
		Models:    []providers.Model{{ID: "kimi-k3", Family: "kimi", Enabled: &trueVal}},
	}}}

	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{}})
	if !policy.UserOwned() {
		t.Fatal("premise broken: an explicit empty list must be user-owned")
	}
	targets, err := SelectTargets(reg, nil, 3, nil, PolicyExcluder(policy), nil)
	if err != nil {
		t.Fatalf("an explicit empty exclusion list must exclude nothing, got error: %v", err)
	}
	if len(targets) != 1 || targets[0].Model.ID != "kimi-k3" {
		t.Fatalf("targets = %+v; want the k3 model (nothing excluded)", targets)
	}
}

// TestExclusion_AllExcludedErrorNamesTheList pins the all-excluded error text to
// the user's own rule ("the exclusion list") and off the retired "pool policy"
// wording the user cannot inspect.
func TestExclusion_AllExcludedErrorNamesTheList(t *testing.T) {
	trueVal := true
	t.Setenv("KEY_EX4", "k")
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   "https://example.invalid/v1",
		APIKeyEnv: "KEY_EX4",
		Models:    []providers.Model{{ID: "glm-5.3-pro", Family: "glm", Enabled: &trueVal}},
	}}}

	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})
	_, err := SelectTargets(reg, nil, 3, nil, PolicyExcluder(policy), nil)
	if err == nil {
		t.Fatal("an all-excluded pool must be an error")
	}
	const want = "no models available for auto-seating: every candidate is excluded by the exclusion list"
	if err.Error() != want {
		t.Errorf("all-excluded error = %q; want %q", err.Error(), want)
	}
	if strings.Contains(err.Error(), "pool policy") {
		t.Errorf("all-excluded error still names the retired policy wording: %q", err.Error())
	}
}

// TestExclusion_ExplicitModelsPickWarns: an explicit --models pick that the
// list covers is still honored, and prints the shared warning sentence once.
// Non-vacuity: the pre-change code printed nothing and had no warning sink.
func TestExclusion_ExplicitModelsPickWarns(t *testing.T) {
	trueVal := true
	t.Setenv("KEY_EX5", "k")
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "nw",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_EX5",
			Models:    []providers.Model{{ID: "glm-5.3-pro", Family: "glm", Enabled: &trueVal}},
		},
		{
			Name:      "other",
			BaseURL:   "https://example.invalid/v1",
			APIKeyEnv: "KEY_EX5",
			Models:    []providers.Model{{ID: "clean-model", Family: "clean", Enabled: &trueVal}},
		},
	}}

	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})
	var warn bytes.Buffer
	targets, err := SelectTargets(reg, nil, 1, []string{"nw/glm-5.3-pro"}, PolicyExcluder(policy), &warn)
	if err != nil {
		t.Fatalf("an explicit excluded pick must be honored, got error: %v", err)
	}
	if len(targets) != 1 || targets[0].Model.ID != "glm-5.3-pro" {
		t.Fatalf("explicit pick not honored: %+v", targets)
	}
	if got := strings.TrimSpace(warn.String()); got != "warning: "+exclusionWarningSuffix("nw", "glm-5.3-pro") {
		t.Fatalf("warning = %q; want the shared exclusion sentence", got)
	}

	// A pick the list does NOT cover stays silent.
	warn.Reset()
	if _, err := SelectTargets(reg, nil, 1, []string{"other/clean-model"}, PolicyExcluder(policy), &warn); err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("a non-excluded pick must stay silent, got %q", warn.String())
	}
}

// TestExclusion_ExplicitSeatPickHonoredWithWarning: the --seats path honors an
// excluded provider target end-to-end (the seat runs and reports) and warns.
func TestExclusion_ExplicitSeatPickHonoredWithWarning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** excluded seat honored"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("KEY_EX6", "good-key")
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "nw",
		BaseURL:   server.URL,
		APIKeyEnv: "KEY_EX6",
		Models:    []providers.Model{{ID: "glm-5.3-pro", Family: "glm", Enabled: boolPtr(true)}},
	}}}
	regPath := filepath.Join(tmpDir, "providers.json")
	writeRegistry(t, regPath, reg)

	var warn bytes.Buffer
	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Seats:        []SeatSpec{{Kind: SeatKindProvider, Raw: "nw/glm-5.3-pro", ModelID: "nw/glm-5.3-pro"}},
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   60,
		ExcludeModel: PolicyExcluder(providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"nw"}})),
		WarnOut:      &warn,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("an explicit excluded seat must be honored, got %v", err)
	}
	if summary.NCompleted != 1 || summary.Seats[0].FinalTarget.Provider.Name != "nw" {
		t.Fatalf("excluded seat pick not honored: %+v", summary.Seats)
	}
	if got := warn.String(); !strings.Contains(got, "warning: "+exclusionWarningSuffix("nw", "glm-5.3-pro")) {
		t.Fatalf("seats warning = %q; want the shared exclusion sentence", got)
	}
}

// TestExclusion_FailoverSkipsExcludedProvider: step-1 failover (same model,
// other provider) skips an alternate whose PROVIDER the list names, and step-2
// skips an alternate whose model id the list names. Nothing is seated from the
// excluded candidates.
//
// Non-vacuity control: the id-only reading of the same list (all the
// pre-change seam could express) DOES fail over to the excluded provider, so
// the skip above is the provider-name arm and not an empty registry.
func TestExclusion_FailoverSkipsExcludedProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "dead-key") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": {"message": "Service Unavailable"}}`))
			return
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** failover answer"},
			FinishReason: "stop",
		}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	run := func(t *testing.T, seam ExcludeFunc) *SummaryReport {
		t.Helper()
		tmpDir := t.TempDir()
		t.Setenv(providers.EnvHome, tmpDir)
		t.Setenv("KEY_EX7_DEAD", "dead-key")
		t.Setenv("KEY_EX7_EXCL", "ok-key")
		t.Setenv("KEY_EX7_OTHER", "ok-key")
		reg := &providers.Registry{Providers: []providers.Provider{
			{
				Name:      "prov-dead",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_EX7_DEAD",
				Models:    []providers.Model{{ID: "dead-model", Family: "dead-fam", Enabled: boolPtr(true)}},
			},
			{
				Name:      "prov-excl",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_EX7_EXCL",
				Models:    []providers.Model{{ID: "dead-model", Family: "dead-fam", Enabled: boolPtr(true)}},
			},
			{
				Name:      "prov-other",
				BaseURL:   server.URL,
				APIKeyEnv: "KEY_EX7_OTHER",
				Models:    []providers.Model{{ID: "other-model", Family: "other-fam", Enabled: boolPtr(true)}},
			},
		}}
		regPath := filepath.Join(tmpDir, "providers.json")
		writeRegistry(t, regPath, reg)
		cfg := RunConfig{
			Task:         "review",
			N:            1,
			Models:       []string{"prov-dead/dead-model"},
			Root:         tmpDir,
			OutDir:       filepath.Join(tmpDir, "reports"),
			RegistryPath: regPath,
			TimeoutSec:   60,
			ExcludeModel: seam,
		}
		summary, err := Execute(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		return summary
	}

	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"prov-excl", "other-model"}})
	summary := run(t, PolicyExcluder(policy))
	seat := summary.Seats[0]
	for _, f := range seat.Failovers {
		if f.ToProv == "prov-excl" || f.ToMod == "other-model" {
			t.Fatalf("failover landed on an excluded candidate: %+v", f)
		}
	}
	if seat.FinalTarget.Provider.Name != "prov-dead" {
		t.Fatalf("final target = %s; want prov-dead (both alternates are excluded)", seat.FinalTarget.FullName())
	}

	// Control: the id-only reading of the same list does not cover "prov-excl"
	// as a provider name, so the step-1 alternate is taken.
	idOnly := func(_ string, m providers.Model) bool {
		return m.ID == "prov-excl" || m.ID == "other-model"
	}
	control := run(t, idOnly)
	if control.Seats[0].FinalTarget.Provider.Name != "prov-excl" {
		t.Fatalf("control premise broken: the id-only seam must fail over to prov-excl, got %s",
			control.Seats[0].FinalTarget.FullName())
	}
}
