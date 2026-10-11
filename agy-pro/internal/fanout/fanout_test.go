package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
