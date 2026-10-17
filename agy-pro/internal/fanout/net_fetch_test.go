package fanout

// B4-2a pins: the --allow-host plumbing and the engine wiring of
// read_url_content.
//
// Plumbing: the repeatable --allow-host flag accumulates into RunConfig.
// AllowHosts (normalized: lowercase, port-stripped, deduped) through the ONE
// choke point buildRunConfig, and a run without the flag keeps an empty list
// (the behavior gate: nothing changes on the wire).
//
// Engine wiring: a non-empty allow-list appends the read_url_content schema
// to EVERY provider seat's tool list (cross-verification and partition
// alike, write units included) and plumbs the list into every provider
// seat's runner; native seats are untouched. The model's read_url_content
// call then really fetches through the runner (allowed host succeeds,
// unlisted host refused error-shaped before any network use).
//
// Mutation-verified: forcing the engine's exposure gate on fails the
// no-allow-list pin (see the unit report).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agypro/internal/agenttools"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

// netDocStub is the loopback document server the scripted seat tool calls
// fetch; it answers every path with one fixed text body.
func netDocStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "net-doc-body")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// netRun is a cross-verification run over the REAL engine against the stub
// provider, with the B4-2a allow-list on the config; the captured requests
// are the ones production sends.
func netRun(t *testing.T, allowHosts []string, respond func(call int, req capturedChatRequest) ChatResponse) *requestShapeRunResult {
	t.Helper()
	rec := &chatRecorder{respond: respond}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "NET_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "NET_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("NET_KEY_A", "key-a")
	t.Setenv("NET_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cfg := RunConfig{
		Task:   reqShapeTask,
		Prompt: reqShapePrompt,
		Root:   dir,
		OutDir: filepath.Join(dir, "reports"),
		Seats: []SeatSpec{
			{Kind: SeatKindProvider, Raw: "prov-a/model-a", ModelID: "model-a"},
			{Kind: SeatKindProvider, Raw: "prov-b/model-b", ModelID: "model-b"},
		},
		RegistryPath: regPath,
		MaxTurns:     5,
		TimeoutSec:   30,
		AllowHosts:   allowHosts,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return &requestShapeRunResult{rec: rec, summary: summary, outDir: cfg.OutDir, root: dir}
}

// partitionNetRun is partitionRun (partition_test.go) plus the B4-2a
// allow-list on the config, so the engine wiring can be pinned over the REAL
// partition path without touching the pre-existing helper.
func partitionNetRun(t *testing.T, units []Unit, allowHosts []string, respond func(call int, req capturedChatRequest) ChatResponse) *partitionRunResult {
	t.Helper()
	rec := &chatRecorder{respond: respond}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "NET_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "NET_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("NET_KEY_A", "key-a")
	t.Setenv("NET_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cfg := RunConfig{
		Task:         partTask,
		Root:         dir,
		OutDir:       filepath.Join(dir, "reports"),
		Units:        units,
		RegistryPath: regPath,
		MaxTurns:     6,
		TimeoutSec:   60,
		AllowHosts:   allowHosts,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute (partition) failed: %v", err)
	}
	return &partitionRunResult{rec: rec, summary: summary, root: dir}
}

// toolOrder decodes one captured request's tools array into its ordered tool
// names.
func toolOrder(t *testing.T, toolsJSON string) []string {
	t.Helper()
	var defs []agenttools.ToolDef
	if err := json.Unmarshal([]byte(toolsJSON), &defs); err != nil {
		t.Fatalf("decode tools array: %v", err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	return names
}

// The exposure gate: WITH the allow-list every provider seat's tool list
// carries read_url_content (last, after the four read tools), the scripted
// call really fetches the loopback document through the seat runner, and no
// seat gains run_command. WITHOUT the allow-list the offered tool list is
// exactly the pre-B4-2a shape (byte-identical exposure pin).
func TestNetToolExposedOnlyWithAllowHosts(t *testing.T) {
	doc := netDocStub(t)
	docURL := doc.URL + "/doc"

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if n == 0 {
			return toolCallResponse("n1", "read_url_content", `{"url":"`+docURL+`"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := netRun(t, []string{"127.0.0.1"}, respond)

	snapshot := run.rec.snapshot()
	if len(snapshot) == 0 {
		t.Fatal("no requests captured")
	}
	for _, r := range snapshot {
		if !strings.Contains(r.ToolsJSON, "read_url_content") {
			t.Errorf("a seat's tool list lost read_url_content: %s", r.ToolsJSON)
		}
		if strings.Contains(r.ToolsJSON, "run_command") {
			t.Errorf("a seat's tool list gained run_command: %s", r.ToolsJSON)
		}
	}
	byModel := requestsByModel(snapshot)
	for _, model := range []string{"model-a", "model-b"} {
		reqs := byModel[model]
		if len(reqs) < 2 {
			t.Fatalf("captured %d requests for %s; want at least 2 (tool turn + final)", len(reqs), model)
		}
		if res := lastToolResult(reqs[1]); !strings.Contains(res, "net-doc-body") {
			t.Errorf("seat %s tool result = %q; want the fetched doc body (the runner really fetched)", model, res)
		}
	}
	if got, want := strings.Join(toolOrder(t, snapshot[0].ToolsJSON), ","),
		"read_file,list_dir,grep,glob,read_url_content"; got != want {
		t.Errorf("tool order = %s; want %s", got, want)
	}

	// No allow-list: the offered tool list is exactly the pre-B4-2a shape.
	plain := netRun(t, nil, requestShapeAnswer)
	plainSnapshot := plain.rec.snapshot()
	if len(plainSnapshot) == 0 {
		t.Fatal("no requests captured in the no-allow-list run")
	}
	for _, r := range plainSnapshot {
		if strings.Contains(r.ToolsJSON, "read_url_content") {
			t.Errorf("a no-allow-list run exposed read_url_content: %s", r.ToolsJSON)
		}
	}
	if got, want := strings.Join(toolOrder(t, plainSnapshot[0].ToolsJSON), ","),
		"read_file,list_dir,grep,glob"; got != want {
		t.Errorf("no-allow-list tool order = %s; want %s (byte-identical to pre-B4-2a)", got, want)
	}
}

// Partition mode: the allow-list reaches EVERY provider seat (both units'
// tool lists carry the tool), the allowed unit's call fetches the loopback
// document, and the unlisted-host call is refused error-shaped before any
// network use.
func TestPartitionModeNetToolReachesEveryProviderSeat(t *testing.T) {
	doc := netDocStub(t)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		switch req.Model {
		case "model-a":
			if n == 0 {
				return toolCallResponse("pa", "read_url_content", `{"url":"`+doc.URL+`/doc"}`)
			}
		case "model-b":
			if n == 0 {
				// Unlisted https host: the allow-list refusal happens
				// before any dial.
				return toolCallResponse("pb", "read_url_content", `{"url":"https://unlisted.example/x"}`)
			}
		}
		return requestShapeAnswer(call, req)
	}
	run := partitionNetRun(t, partitionUnits(), []string{"127.0.0.1"}, respond)

	snapshot := run.rec.snapshot()
	for _, r := range snapshot {
		if !strings.Contains(r.ToolsJSON, "read_url_content") {
			t.Errorf("a partition seat's tool list lost read_url_content: %s", r.ToolsJSON)
		}
		if strings.Contains(r.ToolsJSON, "run_command") {
			t.Errorf("a partition seat's tool list gained run_command: %s", r.ToolsJSON)
		}
	}
	byModel := requestsByModel(snapshot)
	alpha, beta := byModel["model-a"], byModel["model-b"]
	if len(alpha) < 2 || len(beta) < 2 {
		t.Fatalf("captured alpha=%d beta=%d requests; want at least 2 each (tool turn + final)", len(alpha), len(beta))
	}
	if res := lastToolResult(alpha[1]); !strings.Contains(res, "net-doc-body") {
		t.Errorf("unit-alpha fetch result = %q; want the fetched doc body", res)
	}
	if res := lastToolResult(beta[1]); !strings.Contains(res, "allow-list") {
		t.Errorf("unit-beta fetch result = %q; want the allow-list refusal", res)
	}
}

// The B4-1 write carve-out rebuilds a write unit's tool list; the B4-2a net
// tool must survive that rebuild: a write unit in an allow-listed partition
// run carries write_file AND read_url_content, and the fetch works.
func TestPartitionModeWriteUnitKeepsNetTool(t *testing.T) {
	doc := netDocStub(t)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if req.Model == "model-a" && n == 0 {
			return toolCallResponse("wa", "read_url_content", `{"url":"`+doc.URL+`/doc"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := partitionNetRun(t, writeUnits(), []string{"127.0.0.1"}, respond)

	byModel := requestsByModel(run.rec.snapshot())
	for _, r := range byModel["model-a"] {
		if !strings.Contains(r.ToolsJSON, "write_file") || !strings.Contains(r.ToolsJSON, "read_url_content") {
			t.Errorf("the write unit's seat lost a tool: %s", r.ToolsJSON)
		}
	}
	alpha := byModel["model-a"]
	if len(alpha) < 2 {
		t.Fatalf("captured %d unit-alpha requests; want at least 2", len(alpha))
	}
	if res := lastToolResult(alpha[1]); !strings.Contains(res, "net-doc-body") {
		t.Errorf("unit-alpha fetch result = %q; want the fetched doc body", res)
	}
}

// The CLI surface binds the repeatable --allow-host flag into RunConfig.
// AllowHosts through buildRunConfig: values are normalized (lowercased,
// port-stripped, deduped, empties dropped), and no flag leaves the list
// empty (the byte-identical behavior gate at the config level).
func TestRunAllowHostFlagBindsRunConfig(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw strings.Builder
	args := []string{"--prompt", "p", "--json",
		"--allow-host", "Example.COM:8443",
		"--allow-host", "second.dev",
		"--allow-host", "EXAMPLE.COM:8443",
		"--allow-host", "  Third.Dev  ",
		"--allow-host", ""}
	if code := Run(args, nil, &out, &errw); code != 0 {
		t.Fatalf("Run(%v) exit = %d, stderr = %s", args, code, errw.String())
	}
	if len(got) != 1 {
		t.Fatalf("engine called %d times; want 1", len(got))
	}
	want := []string{"example.com", "second.dev", "third.dev"}
	if gotStr, wantStr := strings.Join(got[0].AllowHosts, ","), strings.Join(want, ","); gotStr != wantStr {
		t.Errorf("AllowHosts = %q; want %q (normalized, deduped, port-stripped, empties dropped)", gotStr, wantStr)
	}

	// No flag: the list stays empty, so the engine sees the pre-B4-2a
	// config shape.
	var out2, errw2 strings.Builder
	if code := Run([]string{"--prompt", "p", "--json"}, nil, &out2, &errw2); code != 0 {
		t.Fatalf("Run(no --allow-host) exit = %d, stderr = %s", code, errw2.String())
	}
	if len(got) != 2 {
		t.Fatalf("engine called %d times; want 2", len(got))
	}
	if len(got[1].AllowHosts) != 0 {
		t.Errorf("AllowHosts without the flag = %v; want empty", got[1].AllowHosts)
	}
}
