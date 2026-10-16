package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// Pinned budget-surface regression (2026-10-14): provider seats degraded with
// "all failover attempts exhausted; last error: context deadline exceeded"
// (reports/fanout/20261014-042933/summary.json) while the provider endpoints
// were healthy, because two independent 120s ceilings capped the run: the
// --timeout flag default and the shared client's flat per-request timeout.
// These pins hold the fix: the flag default is 300s, the engine fallback
// matches it, and the client's per-request timeout is never shorter than the
// swarm budget (120s floor when no budget is set).

// The --timeout flag default is the single source of truth the settings panel
// surfaces ("Fanout Wall Timeout (--timeout)"). It must read 300s.
func TestRunTimeoutFlagDefaultIs300(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	var got RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = cfg
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	run := func(args ...string) RunConfig {
		t.Helper()
		var out, errw bytes.Buffer
		full := append([]string{"-prompt", "p", "-json"}, args...)
		if code := Run(full, nil, &out, &errw); code != 0 {
			t.Fatalf("Run(%v) exit = %d, stderr = %s", full, code, errw.String())
		}
		return got
	}

	if cfg := run(); cfg.TimeoutSec != 300 {
		t.Errorf("default --timeout bound TimeoutSec = %d; want 300", cfg.TimeoutSec)
	}
	if cfg := run("-timeout", "900"); cfg.TimeoutSec != 900 {
		t.Errorf("--timeout 900 bound TimeoutSec = %d; want 900 (explicit override)", cfg.TimeoutSec)
	}
}

// The engine fallback for a budget-less RunConfig (library callers, tests,
// swarm delegation) matches the CLI default: 300s, not the historical 120s.
func TestRunConfigDefaultsTimeoutIs300(t *testing.T) {
	cfg := RunConfig{}.withDefaults()
	if cfg.TimeoutSec != 300 {
		t.Errorf("RunConfig{}.withDefaults().TimeoutSec = %d; want 300", cfg.TimeoutSec)
	}
	// An explicit budget is never overridden by the default.
	cfg = RunConfig{TimeoutSec: 600}.withDefaults()
	if cfg.TimeoutSec != 600 {
		t.Errorf("withDefaults() overrode an explicit TimeoutSec: got %d; want 600", cfg.TimeoutSec)
	}
}

// The shared provider client's per-request timeout must never be shorter than
// the swarm budget; with no budget set, the 120s floor applies.
func TestNewClientPerRequestTimeoutNeverBelowBudget(t *testing.T) {
	if got := NewClient().httpClient.Timeout; got != 120*time.Second {
		t.Errorf("NewClient() per-request timeout = %v; want the 120s floor", got)
	}
	for _, tc := range []struct {
		budget int
		want   time.Duration
	}{
		{0, 120 * time.Second},   // no budget -> floor
		{-30, 120 * time.Second}, // nonsense budget -> floor
		{30, 120 * time.Second},  // budget below the floor clamps up to the floor
		{120, 120 * time.Second}, // budget equal to the floor
		{300, 300 * time.Second}, // the new CLI default passes through
		{900, 900 * time.Second}, // large budget passes through
	} {
		c := NewClientWithBudget(tc.budget)
		if c.httpClient.Timeout != tc.want {
			t.Errorf("NewClientWithBudget(%d) per-request timeout = %v; want %v", tc.budget, c.httpClient.Timeout, tc.want)
		}
		if tc.budget > 0 && c.httpClient.Timeout < time.Duration(tc.budget)*time.Second {
			t.Errorf("NewClientWithBudget(%d) per-request timeout %v is shorter than the swarm budget", tc.budget, c.httpClient.Timeout)
		}
	}
}

// The engine wires the swarm budget into the shared client (the second 120s
// ceiling), and an injected client (RunConfig.Client test seam) is honored.
func TestNewSwarmClientWiresSwarmBudget(t *testing.T) {
	c := newSwarmClient(RunConfig{TimeoutSec: 600})
	if c.httpClient.Timeout != 600*time.Second {
		t.Errorf("newSwarmClient(budget 600s) per-request timeout = %v; want 10m0s", c.httpClient.Timeout)
	}
	injected := NewClient()
	if got := newSwarmClient(RunConfig{Client: injected, TimeoutSec: 600}); got != injected {
		t.Error("newSwarmClient must return the injected RunConfig.Client unchanged")
	}
}

// End-to-end pin for the engine wiring (the call site, not just the helper):
// Execute must build the shared client from the swarm budget, so a seat's
// single chat call may run for the whole budget instead of dying at the floor.
// The floor is shrunk to 60ms for the test; a 400ms handler then separates
// "budget-aware client" (seat succeeds) from "flat-floor client" (seat fails
// with a client timeout and degrades).
func TestExecuteClientTimeoutTracksSwarmBudget(t *testing.T) {
	old := requestTimeoutFloor
	requestTimeoutFloor = 60 * time.Millisecond
	t.Cleanup(func() { requestTimeoutFloor = old })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[INFO]** slow but finished"},
			FinishReason: "stop",
		}}})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	t.Setenv(providers.EnvHome, tmpDir)
	t.Setenv("BUDGET_KEY", "k")

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name:      "prov-slow",
		BaseURL:   server.URL,
		APIKeyEnv: "BUDGET_KEY",
		Models:    []providers.Model{{ID: "slow-model", Family: "slow-fam", Enabled: &trueVal}},
	}}}
	regPath := filepath.Join(tmpDir, "providers.json")
	regData, _ := json.Marshal(reg)
	_ = os.WriteFile(regPath, regData, 0o600)

	cfg := RunConfig{
		Task:         "review",
		N:            1,
		Root:         tmpDir,
		OutDir:       filepath.Join(tmpDir, "reports"),
		RegistryPath: regPath,
		TimeoutSec:   10, // swarm budget 10s: the per-request timeout must reach it
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if summary.NCompleted != 1 {
		t.Fatalf("seat must finish inside the swarm budget: completed=%d seats=%+v", summary.NCompleted, summary.Seats)
	}
}
