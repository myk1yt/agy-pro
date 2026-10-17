package fanout

// B4-2b pins: the --search-endpoint / --search-key-env plumbing and the
// engine wiring of search_web.
//
// Plumbing: the two flags bind into RunConfig.SearchSource through the ONE
// choke point buildRunConfig (endpoint trimmed), and a run without them
// keeps a zero source (the behavior gate: nothing changes on the wire).
//
// Engine wiring: a non-empty source endpoint appends the search_web schema
// to EVERY provider seat's tool list (cross-verification and partition
// alike, write units and allow-listed runs included) and plumbs the source
// into every provider seat's runner; native seats are untouched. The
// model's search_web call then really hits the loopback stub through the
// runner (results rendered); without a source the tool list is byte-identical
// to pre-B4-2b and the tool is absent.
//
// Mutation-verified: forcing the engine's exposure gate on fails the
// no-source pin (see the unit report).

import (
	"context"
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

// searchStubBody is the fixed loopback search answer the scripted seat calls
// receive; the rendered result must surface to the model.
const searchStubBody = `{"results":[{"title":"Stub Hit","url":"https://stub.example/1","snippet":"stub snippet"}]}`

// searchNetStub is the loopback search backend the runs point at.
func searchNetStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchStubBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// searchSource builds the B4-2b source for one loopback stub: the endpoint
// template names the stub, the key is referenced by NAME and set to a test
// dummy (never a real credential).
func searchSource(t *testing.T, srv *httptest.Server) agenttools.SearchSource {
	t.Helper()
	t.Setenv("NET_SEARCH_KEY", "stub-search-key")
	return agenttools.SearchSource{
		Endpoint: srv.URL + "/search?q={query}",
		KeyEnv:   "NET_SEARCH_KEY",
	}
}

// searchRun is netRun (net_fetch_test.go) plus the B4-2b source on the
// config, so the engine wiring can be pinned over the REAL engine.
func searchRun(t *testing.T, source agenttools.SearchSource, respond func(call int, req capturedChatRequest) ChatResponse) *requestShapeRunResult {
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
		SearchSource: source,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return &requestShapeRunResult{rec: rec, summary: summary, outDir: cfg.OutDir, root: dir}
}

// partitionSearchRun is partitionNetRun plus the B4-2b source on the config.
func partitionSearchRun(t *testing.T, units []Unit, source agenttools.SearchSource, respond func(call int, req capturedChatRequest) ChatResponse) *partitionRunResult {
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
		SearchSource: source,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute (partition) failed: %v", err)
	}
	return &partitionRunResult{rec: rec, summary: summary, root: dir}
}

// The exposure gate: WITH a source every provider seat's tool list carries
// search_web (last, after the read tools and read_url_content) and the
// scripted call really hits the loopback stub through the seat runner.
// WITHOUT a source the offered tool list is exactly the pre-B4-2b shape and
// search_web is absent (byte-identical exposure pin).
func TestSearchToolExposedOnlyWithSource(t *testing.T) {
	stub := searchNetStub(t)
	source := searchSource(t, stub)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if n == 0 {
			return toolCallResponse("s1", "search_web", `{"query":"fanout wiring"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := searchRun(t, source, respond)

	snapshot := run.rec.snapshot()
	if len(snapshot) == 0 {
		t.Fatal("no requests captured")
	}
	for _, r := range snapshot {
		if !strings.Contains(r.ToolsJSON, "search_web") {
			t.Errorf("a seat's tool list lost search_web: %s", r.ToolsJSON)
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
		if res := lastToolResult(reqs[1]); !strings.Contains(res, "Stub Hit") {
			t.Errorf("seat %s tool result = %q; want the rendered stub hit (the runner really searched)", model, res)
		}
	}
	if got, want := strings.Join(toolOrder(t, snapshot[0].ToolsJSON), ","),
		"read_file,list_dir,grep,glob,search_web"; got != want {
		t.Errorf("tool order = %s; want %s", got, want)
	}

	// No source: the offered tool list is exactly the pre-B4-2b shape.
	plain := searchRun(t, agenttools.SearchSource{}, requestShapeAnswer)
	plainSnapshot := plain.rec.snapshot()
	if len(plainSnapshot) == 0 {
		t.Fatal("no requests captured in the no-source run")
	}
	for _, r := range plainSnapshot {
		if strings.Contains(r.ToolsJSON, "search_web") {
			t.Errorf("a no-source run exposed search_web: %s", r.ToolsJSON)
		}
	}
	if got, want := strings.Join(toolOrder(t, plainSnapshot[0].ToolsJSON), ","),
		"read_file,list_dir,grep,glob"; got != want {
		t.Errorf("no-source tool order = %s; want %s (byte-identical to pre-B4-2b)", got, want)
	}
}

// A run that carries BOTH the B4-2a allow-list and the B4-2b source exposes
// both net tools in the pinned order, after the four read tools.
func TestSearchToolCoexistsWithNetTool(t *testing.T) {
	stub := searchNetStub(t)
	source := searchSource(t, stub)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if n == 0 {
			return toolCallResponse("s2", "search_web", `{"query":"both gates"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := netSearchRun(t, []string{"127.0.0.1"}, source, respond)

	snapshot := run.rec.snapshot()
	if len(snapshot) == 0 {
		t.Fatal("no requests captured")
	}
	if got, want := strings.Join(toolOrder(t, snapshot[0].ToolsJSON), ","),
		"read_file,list_dir,grep,glob,read_url_content,search_web"; got != want {
		t.Errorf("combined tool order = %s; want %s", got, want)
	}
}

// netSearchRun is netRun plus the B4-2b source, for the combined-gate pin.
func netSearchRun(t *testing.T, allowHosts []string, source agenttools.SearchSource, respond func(call int, req capturedChatRequest) ChatResponse) *requestShapeRunResult {
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
		SearchSource: source,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return &requestShapeRunResult{rec: rec, summary: summary, outDir: cfg.OutDir, root: dir}
}

// Partition mode: the source reaches EVERY provider seat (both units' tool
// lists carry the tool) and a scripted call really hits the stub.
func TestPartitionModeSearchToolReachesEveryProviderSeat(t *testing.T) {
	stub := searchNetStub(t)
	source := searchSource(t, stub)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if n == 0 {
			return toolCallResponse("ps", "search_web", `{"query":"partition wiring"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := partitionSearchRun(t, partitionUnits(), source, respond)

	snapshot := run.rec.snapshot()
	for _, r := range snapshot {
		if !strings.Contains(r.ToolsJSON, "search_web") {
			t.Errorf("a partition seat's tool list lost search_web: %s", r.ToolsJSON)
		}
	}
	byModel := requestsByModel(snapshot)
	for _, model := range []string{"model-a", "model-b"} {
		reqs := byModel[model]
		if len(reqs) < 2 {
			t.Fatalf("captured %d requests for %s; want at least 2 (tool turn + final)", len(reqs), model)
		}
		if res := lastToolResult(reqs[1]); !strings.Contains(res, "Stub Hit") {
			t.Errorf("seat %s search result = %q; want the rendered stub hit", model, res)
		}
	}
}

// The B4-1 write carve-out rebuilds a write unit's tool list; the B4-2b
// search tool must survive that rebuild: a write unit in a sourced partition
// run carries write_file AND search_web, and the search works.
func TestPartitionModeWriteUnitKeepsSearchTool(t *testing.T) {
	stub := searchNetStub(t)
	source := searchSource(t, stub)

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if req.Model == "model-a" && n == 0 {
			return toolCallResponse("ws", "search_web", `{"query":"write unit search"}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := partitionSearchRun(t, writeUnits(), source, respond)

	byModel := requestsByModel(run.rec.snapshot())
	for _, r := range byModel["model-a"] {
		if !strings.Contains(r.ToolsJSON, "write_file") || !strings.Contains(r.ToolsJSON, "search_web") {
			t.Errorf("the write unit's seat lost a tool: %s", r.ToolsJSON)
		}
	}
	alpha := byModel["model-a"]
	if len(alpha) < 2 {
		t.Fatalf("captured %d unit-alpha requests; want at least 2", len(alpha))
	}
	if res := lastToolResult(alpha[1]); !strings.Contains(res, "Stub Hit") {
		t.Errorf("unit-alpha search result = %q; want the rendered stub hit", res)
	}
}

// The CLI surface binds --search-endpoint / --search-key-env into
// RunConfig.SearchSource through buildRunConfig (endpoint trimmed); without
// the flags the source stays zero (the byte-identical behavior gate at the
// config level).
func TestRunSearchFlagsBindsRunConfig(t *testing.T) {
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
		"--search-endpoint", "  https://api.example.com/s?q={query}  ",
		"--search-key-env", "MY_SEARCH_KEY"}
	if code := Run(args, nil, &out, &errw); code != 0 {
		t.Fatalf("Run(%v) exit = %d, stderr = %s", args, code, errw.String())
	}
	if len(got) != 1 {
		t.Fatalf("engine called %d times; want 1", len(got))
	}
	if got[0].SearchSource.Endpoint != "https://api.example.com/s?q={query}" {
		t.Errorf("SearchSource.Endpoint = %q; want the trimmed endpoint template", got[0].SearchSource.Endpoint)
	}
	if got[0].SearchSource.KeyEnv != "MY_SEARCH_KEY" {
		t.Errorf("SearchSource.KeyEnv = %q; want MY_SEARCH_KEY (the NAME, never a value)", got[0].SearchSource.KeyEnv)
	}

	// No flags: the source stays zero, so the engine sees the pre-B4-2b
	// config shape.
	var out2, errw2 strings.Builder
	if code := Run([]string{"--prompt", "p", "--json"}, nil, &out2, &errw2); code != 0 {
		t.Fatalf("Run(no search flags) exit = %d, stderr = %s", code, errw2.String())
	}
	if len(got) != 2 {
		t.Fatalf("engine called %d times; want 2", len(got))
	}
	if got[1].SearchSource.Endpoint != "" || got[1].SearchSource.KeyEnv != "" {
		t.Errorf("SearchSource without the flags = %+v; want zero", got[1].SearchSource)
	}
}
