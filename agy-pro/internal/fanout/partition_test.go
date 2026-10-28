package fanout

// Partition-mode pins (work items B2 + B3).
//
// B2 (per-unit prompt plumbing): every unit prompt is physically
// [shared preamble][unit body]. The system message IS the preamble, so it must
// stay byte-identical across units while only the user body varies; anything
// unit-shaped in the system message would destroy cross-unit prefix caching.
//
// B3 (per-unit working boundary): a unit's ToolRunner refuses path-reading
// tools that address a path outside the unit's scope, and an unscoped runner
// (the non-partition path) behaves exactly as before.
//
// Every pin here is mutation-verified: leaking a unit value into the system
// message breaks the preamble pin, and removing the scope check breaks the
// refusal pin (see the coder report).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// partTask is the task preset every partition pin runs under.
const partTask = "review"

// partitionUnits are two disjoint units with different deliverables and
// bodies, each naming its own provider seat so the captured requests can be
// attributed per unit.
func partitionUnits() []Unit {
	return []Unit{
		{
			ID:          "unit-alpha",
			Title:       "Alpha work",
			Scope:       []string{"src/alpha"},
			Deliverable: "Alpha deliverable",
			Seat:        "prov-a/model-a",
			Body:        "ALPHA-BODY: audit src/alpha only",
		},
		{
			ID:          "unit-beta",
			Title:       "Beta work",
			Scope:       []string{"src/beta"},
			Deliverable: "Beta deliverable",
			Seat:        "prov-b/model-b",
			Body:        "BETA-BODY: audit src/beta only",
		},
	}
}

// partitionRunResult is one partition-mode engine run: the recorded requests,
// the reconciled summary and the workspace root.
type partitionRunResult struct {
	rec     *chatRecorder
	summary *SummaryReport
	root    string
}

// partitionRun stands up one provider seat per unit on one stub server and runs
// the REAL engine in partition mode, so the captured requests are the ones
// production sends. The responder is scripted per call.
func partitionRun(t *testing.T, units []Unit, respond func(call int, req capturedChatRequest) ChatResponse) *partitionRunResult {
	t.Helper()
	rec := &chatRecorder{respond: respond}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	for _, rel := range []string{
		filepath.Join("src", "alpha", "ok.go"),
		filepath.Join("src", "beta", "no.go"),
		"loose.go",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		body := "package " + strings.ReplaceAll(rel, string(filepath.Separator), "_") + "\n"
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	t.Setenv(providers.EnvHome, dir)

	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "PART_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "PART_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("PART_KEY_A", "key-a")
	t.Setenv("PART_KEY_B", "key-b")
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
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute (partition) failed: %v", err)
	}
	return &partitionRunResult{rec: rec, summary: summary, root: dir}
}

// requestsByModel buckets the captured requests per model id, preserving order.
func requestsByModel(reqs []capturedChatRequest) map[string][]capturedChatRequest {
	byModel := map[string][]capturedChatRequest{}
	for _, r := range reqs {
		byModel[r.Model] = append(byModel[r.Model], r)
	}
	return byModel
}

// --- B2a: per-unit body, byte-identical preamble ---------------------------

// Two units with different bodies must produce DIFFERENT user messages but a
// BYTE-IDENTICAL system message, and nothing unit-shaped may ride in the
// system message (that is the whole point of the preamble/body split).
func TestPartitionModePerUnitBodySharedPreamble(t *testing.T) {
	units := partitionUnits()
	run := partitionRun(t, units, requestShapeAnswer)

	reqs := run.rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests; want one per unit", len(reqs))
	}
	byModel := requestsByModel(reqs)
	seatA, okA := byModel["model-a"]
	seatB, okB := byModel["model-b"]
	if !okA || len(seatA) != 1 || !okB || len(seatB) != 1 {
		t.Fatalf("want exactly one request per unit seat, got %v", func() map[string]int {
			out := map[string]int{}
			for k, v := range byModel {
				out[k] = len(v)
			}
			return out
		}())
	}
	first, second := seatA[0], seatB[0]

	if first.SystemMsg != second.SystemMsg {
		t.Errorf("system messages differ across units:\nunit-alpha: %q\nunit-beta: %q", first.SystemMsg, second.SystemMsg)
	}
	wantSystem := GetTaskPreset(partTask).SystemPrompt
	if first.SystemMsg != wantSystem {
		t.Errorf("system message = %q; want the %q preset system prompt byte-identically", first.SystemMsg, partTask)
	}
	if first.FirstUser == second.FirstUser {
		t.Errorf("both units got the same user body %q; partition mode must give each unit its own body", first.FirstUser)
	}
	if first.FirstUser != UnitBody(units[0]) {
		t.Errorf("unit-alpha user body = %q; want %q", first.FirstUser, UnitBody(units[0]))
	}
	if second.FirstUser != UnitBody(units[1]) {
		t.Errorf("unit-beta user body = %q; want %q", second.FirstUser, UnitBody(units[1]))
	}

	// The preamble carries no unit-shaped value: no unit id, no scope entry, no
	// deliverable, no body fragment.
	unitShaped := []string{
		units[0].ID, units[1].ID,
		units[0].Title, units[1].Title,
		units[0].Scope[0], units[1].Scope[0],
		units[0].Deliverable, units[1].Deliverable,
		units[0].Body, units[1].Body,
	}
	for _, bad := range unitShaped {
		if strings.Contains(first.SystemMsg, bad) {
			t.Errorf("system message leaked the unit-shaped value %q into the shared preamble", bad)
		}
	}

	// Request shape: system first, unit body second, nothing else.
	for _, r := range []capturedChatRequest{first, second} {
		if len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" {
			t.Errorf("messages = %+v; want exactly [system, user]", r.Messages)
		}
	}
}

// Native seats get the same split inside one argv string: the shared preamble
// (seat-role control + task preset) is byte-identical across units and only the
// tail carries the unit body.
func TestPartitionModeNativePromptSharesPreambleAcrossUnits(t *testing.T) {
	isolatePromptEnv(t) // pins compare against the compiled constants
	units := partitionUnits()
	pa, err := buildNativeSeatPrompt(partTask, UnitBody(units[0]))
	if err != nil {
		t.Fatalf("buildNativeSeatPrompt unit-alpha: %v", err)
	}
	pb, err := buildNativeSeatPrompt(partTask, UnitBody(units[1]))
	if err != nil {
		t.Fatalf("buildNativeSeatPrompt unit-beta: %v", err)
	}

	sharedPrefix := nativeSeatPreamble + "\n\n" + GetTaskPreset(partTask).SystemPrompt + "\n\n"
	if !strings.HasPrefix(pa, sharedPrefix) || !strings.HasPrefix(pb, sharedPrefix) {
		t.Fatalf("a native unit prompt lost the shared preamble:\nunit-alpha: %q\nunit-beta: %q", pa, pb)
	}
	if pa == pb {
		t.Error("both native units got the same prompt; the unit body must vary")
	}
	if !strings.HasSuffix(pa, UnitBody(units[0])) || !strings.HasSuffix(pb, UnitBody(units[1])) {
		t.Errorf("native prompt tail is not the unit body:\nunit-alpha: %q\nunit-beta: %q", pa, pb)
	}
	// The anti-orchestration preamble must not be weakened by partition mode.
	if !strings.Contains(pa, "NEVER spawn subagents") {
		t.Error("native unit prompt lost the anti-orchestration preamble")
	}
}

// --- B2b: the --units flag gates -------------------------------------------

// writePlanFile writes a plan file and returns its path.
func writePlanFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write plan %s: %v", name, err)
	}
	return p
}

const validPlanBody = `[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha"}]`

// --units is mutually exclusive with the cross-verification seat sources and
// with the single-prompt sources, and a bad plan file is a clear usage error
// (exit 2), never a panic.
func TestRunUnitsFlagGates(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	dir := t.TempDir()
	plan := writePlanFile(t, dir, "plan.json", validPlanBody)
	malformed := writePlanFile(t, dir, "malformed.json", "{not json")
	empty := writePlanFile(t, dir, "empty.json", "[]")
	missingField := writePlanFile(t, dir, "missing.json", `[{"id":"u","title":"t","scope":["a"]}]`)
	overlap := writePlanFile(t, dir, "overlap.json",
		`[{"id":"u1","title":"t","scope":["src"],"deliverable":"d"},{"id":"u2","title":"t","scope":["src/a.go"],"deliverable":"d"}]`)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "units + seats",
			args: []string{"--units", plan, "--seats", "native:gemini-x"},
			want: "--units cannot be combined with --seats (partition mode assigns one seat per unit)",
		},
		{
			name: "units + prompt",
			args: []string{"--units", plan, "--prompt", "p"},
			want: "--units cannot be combined with --prompt or --prompt-file",
		},
		{
			name: "units + prompt-file",
			args: []string{"--units", plan, "--prompt-file", plan},
			want: "--units cannot be combined with --prompt or --prompt-file",
		},
		{
			name: "missing plan file",
			args: []string{"--units", filepath.Join(dir, "nope.json")},
			want: "failed to read units plan file",
		},
		{
			name: "malformed plan",
			args: []string{"--units", malformed},
			want: "invalid JSON",
		},
		{
			name: "plan with zero units",
			args: []string{"--units", empty},
			want: "contains no units",
		},
		{
			name: "plan missing a required field",
			args: []string{"--units", missingField},
			want: "missing required field",
		},
		{
			name: "plan with overlapping scopes",
			args: []string{"--units", overlap},
			want: "scope overlap",
		},
		{
			// B6: partition mode is read-only; write/exec tools sit outside the
			// scope filter, so the combination is refused (exit 2).
			name: "units + write/exec tools",
			args: []string{"--units", plan, "--tools", "read,search,exec"},
			want: "read-only",
		},
		{
			name: "units + write tools",
			args: []string{"--units", plan, "--tools", "read,write"},
			want: "read-only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errw strings.Builder
			code := Run(tc.args, nil, &out, &errw)
			if code != 2 {
				t.Fatalf("exit code = %d; want 2 (usage error), stderr = %s", code, errw.String())
			}
			if !strings.Contains(errw.String(), tc.want) {
				t.Errorf("stderr = %q; want it to contain %q", errw.String(), tc.want)
			}
			if strings.Contains(out.String(), "Swarm") {
				t.Errorf("no swarm output must be produced on a usage error:\n%s", out.String())
			}
		})
	}
}

// A3: a valid --units run binds the parsed plan into RunConfig.Units (and the
// seat count follows the plan), driven at the Run() entry point through the
// engine recorder seam.
func TestRunUnitsFlagBindsEngineConfig(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	dir := t.TempDir()
	plan := writePlanFile(t, dir, "plan.json",
		`[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha"},`+
			`{"id":"unit-beta","title":"Beta","scope":["src/beta"],"deliverable":"do beta","seat":"native:gemini-x"}]`)

	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw strings.Builder
	if code := Run([]string{"--units", plan, "--json"}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run(--units) exit = %d, stderr = %s", code, errw.String())
	}
	if len(got) != 1 {
		t.Fatalf("engine called %d times; want 1", len(got))
	}
	cfg := got[0]
	if len(cfg.Units) != 2 {
		t.Fatalf("Units = %+v; want the two parsed plan units", cfg.Units)
	}
	if cfg.Units[0].ID != "unit-alpha" || cfg.Units[1].Seat != "native:gemini-x" {
		t.Errorf("units = %+v; want the plan values verbatim", cfg.Units)
	}
	if cfg.N != 2 {
		t.Errorf("N = %d; want 2 (the plan is the truth)", cfg.N)
	}
	if cfg.Prompt != "" || cfg.Seats != nil || cfg.Models != nil {
		t.Errorf("partition config leaked the single-prompt/cross-verification surface: prompt=%q seats=%v models=%v", cfg.Prompt, cfg.Seats, cfg.Models)
	}
}

// --- B2c: the non-partition path stays byte-identical -----------------------

// A run with no --units keeps the exact pre-partition request shape: the shared
// preset as the system message, the run prompt as the user message, two
// messages and nothing else, and no unit-shaped value anywhere.
func TestLegacyRunWithoutUnitsKeepsRequestShape(t *testing.T) {
	run := requestShapeRun(t, requestShapeAnswer)

	reqs := run.rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("captured %d requests; want one per seat", len(reqs))
	}
	for _, r := range reqs {
		if r.SystemMsg != GetTaskPreset(reqShapeTask).SystemPrompt {
			t.Errorf("system message = %q; want the %q preset system prompt", r.SystemMsg, reqShapeTask)
		}
		if r.FirstUser != reqShapePrompt {
			t.Errorf("first user message = %q; want the run prompt %q", r.FirstUser, reqShapePrompt)
		}
		if len(r.Messages) != 2 || r.Messages[0].Role != "system" || r.Messages[1].Role != "user" {
			t.Errorf("messages = %+v; want exactly [system, user]", r.Messages)
		}
		if r.ToolsJSON == "" {
			t.Error("per-seat tools block is empty; the shape pin is vacuous without it")
		}
		for _, bad := range []string{"unit-alpha", "unit-beta", "Unit:", "Deliverable:"} {
			if strings.Contains(r.SystemMsg, bad) {
				t.Errorf("system message leaked the partition-shaped value %q", bad)
			}
		}
	}
}

// The engine seam pin: a flag run without --units leaves RunConfig.Units nil,
// so the partition branch can never trigger by accident.
func TestRunWithoutUnitsLeavesUnitsNil(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw strings.Builder
	if code := Run([]string{"-prompt", "p", "-json"}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run exit = %d, stderr = %s", code, errw.String())
	}
	if len(got) != 1 {
		t.Fatalf("engine called %d times; want 1", len(got))
	}
	if got[0].Units != nil {
		t.Errorf("Units = %+v; want nil without --units", got[0].Units)
	}
}

// --- B3 engine wiring ------------------------------------------------------

// partitionToolResponder scripts one seat: an out-of-scope read, an in-scope
// read, then the final findings. It is written for a SINGLE-unit run so the
// call sequence is deterministic.
func partitionToolResponder(call int, req capturedChatRequest) ChatResponse {
	switch call {
	case 1:
		return toolCallResponse("call-1", "read_file", `{"path":"src/beta/no.go"}`)
	case 2:
		return toolCallResponse("call-2", "read_file", `{"path":"src/alpha/ok.go"}`)
	default:
		return requestShapeAnswer(call, req)
	}
}

func toolCallResponse(id, tool, args string) ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{
		Index:        0,
		FinishReason: "tool_calls",
		Message: ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{
			{ID: id, Type: "function", Function: ChatFunctionCall{Name: tool, Arguments: args}},
		}},
	}}}
}

// toolResults returns the tool-role message contents of one captured request.
func toolResults(req capturedChatRequest) []string {
	var out []string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

// lastToolResult returns the most recent tool result in one captured request.
func lastToolResult(req capturedChatRequest) string {
	results := toolResults(req)
	if len(results) == 0 {
		return ""
	}
	return results[len(results)-1]
}

// In partition mode the provider seat's ToolRunner is bounded to its unit's
// scope: the out-of-scope read is refused with the error-shaped result (and the
// file's content never reaches the model), while the in-scope read proceeds.
func TestPartitionModeToolRunnerEnforcesUnitScope(t *testing.T) {
	units := []Unit{{
		ID:          "unit-alpha",
		Title:       "Alpha work",
		Scope:       []string{"src/alpha"},
		Deliverable: "Alpha deliverable",
		Seat:        "prov-a/model-a",
	}}
	run := partitionRun(t, units, partitionToolResponder)

	reqs := run.rec.snapshot()
	if len(reqs) < 3 {
		t.Fatalf("captured %d requests; want at least 3 (two tool turns + final)", len(reqs))
	}
	// Request 2 carries the out-of-scope refusal, request 3 the in-scope read.
	if got := toolResults(reqs[1]); len(got) != 1 {
		t.Fatalf("turn 2 tool results = %v; want one refusal", got)
	}
	refused := lastToolResult(reqs[1])
	if !strings.Contains(refused, "outside unit scope") {
		t.Errorf("out-of-scope read was not refused: %q", refused)
	}
	if !strings.Contains(refused, "src/beta/no.go") {
		t.Errorf("refusal does not name the offending path: %q", refused)
	}
	if strings.Contains(refused, "package src_beta_no.go") {
		t.Errorf("out-of-scope file content reached the model: %q", refused)
	}

	allowed := lastToolResult(reqs[2])
	if !strings.Contains(allowed, "package src_alpha_ok.go") {
		t.Errorf("in-scope read did not return the file content: %q", allowed)
	}
	if strings.Contains(allowed, "outside unit scope") {
		t.Errorf("in-scope read was refused: %q", allowed)
	}
}

// The non-partition engine path attaches NO scope: the same out-of-scope read
// that partition mode refuses must return the file content, byte-identical to
// the pre-change runner behavior.
func TestLegacyEngineRunAttachesNoScope(t *testing.T) {
	rec := &chatRecorder{respond: func(call int, req capturedChatRequest) ChatResponse {
		if call == 1 {
			return toolCallResponse("call-1", "read_file", `{"path":"loose.go"}`)
		}
		return requestShapeAnswer(call, req)
	}}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "loose.go"), []byte("package loose\n"), 0o600); err != nil {
		t.Fatalf("write loose.go: %v", err)
	}
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "LEGACY_SCOPE_KEY",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("LEGACY_SCOPE_KEY", "key-a")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cfg := RunConfig{
		Task:         partTask,
		Prompt:       "legacy run",
		Root:         dir,
		OutDir:       filepath.Join(dir, "reports"),
		Seats:        []SeatSpec{{Kind: SeatKindProvider, Raw: "prov-a/model-a", ModelID: "model-a"}},
		RegistryPath: regPath,
		MaxTurns:     6,
		TimeoutSec:   60,
	}
	if _, err := Execute(context.Background(), cfg); err != nil {
		t.Fatalf("Execute (legacy) failed: %v", err)
	}

	reqs := rec.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("captured %d requests; want at least 2 (tool turn + final)", len(reqs))
	}
	results := toolResults(reqs[1])
	if len(results) != 1 {
		t.Fatalf("turn 2 tool results = %v; want one read", results)
	}
	if strings.Contains(results[0], "outside unit scope") {
		t.Errorf("the legacy path refused a read: %q", results[0])
	}
	if !strings.Contains(results[0], "package loose") {
		t.Errorf("legacy read did not return the file content: %q", results[0])
	}
}

// --- Native partition seats: refused (B5) -----------------------------------

// B5: a native partition seat has NO code-level scope boundary (it runs real agy
// at the workspace root and can read/write across units), and the read-only
// slice cannot confine it. The engine therefore REFUSES a plan that names a
// native seat instead of running an unconfined seat, and no subprocess is
// spawned. (The per-unit native prompt split itself is still pinned by
// TestPartitionModeNativePromptSharesPreambleAcrossUnits, which builds the
// prompt directly.)
func TestPartitionModeNativeSeatsRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	reg := &providers.Registry{}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	var mu sync.Mutex
	spawned := 0
	spawn := func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		mu.Lock()
		spawned++
		mu.Unlock()
		return stubSpawn(map[string]string{"AGY_PRO_STUB_STDOUT": "## Findings\n- **[LOW]** native ok\n"})(ctx, bin, args...)
	}

	units := []Unit{
		{ID: "unit-alpha", Title: "Alpha work", Scope: []string{"src/alpha"}, Deliverable: "Alpha deliverable", Seat: "native:gemini-alpha"},
		{ID: "unit-beta", Title: "Beta work", Scope: []string{"src/beta"}, Deliverable: "Beta deliverable", Seat: "native:gemini-beta"},
	}
	cfg := RunConfig{
		Task:                 partTask,
		Root:                 dir,
		OutDir:               filepath.Join(dir, "reports"),
		Units:                units,
		RegistryPath:         regPath,
		NativeResolver:       stubResolver(),
		SpawnCommand:         spawn,
		MaxNativeConcurrency: 4,
		TimeoutSec:           120,
	}
	if _, err := Execute(context.Background(), cfg); err == nil {
		t.Fatal("Execute (native partition) must refuse, got nil error (fail-open)")
	} else if !strings.Contains(err.Error(), "provider seats only") {
		t.Errorf("error = %q; want the provider-seats-only refusal", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if spawned != 0 {
		t.Errorf("spawned %d native subprocesses; want 0 (refuse before spawning)", spawned)
	}
}

// partitionTargets maps units to seats: an explicit seat is resolved through
// the same grammar as --seats, and a seatless unit gets one shared run default.
// B5: a native seat is refused (partition mode is provider-only).
func TestPartitionTargetsSeatResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	t.Setenv("PT_KEY", "pt-key")
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: "http://127.0.0.1:1", APIKeyEnv: "PT_KEY",
			Models: []providers.Model{
				{ID: "model-a", Family: "family-a", Enabled: &trueVal},
				{ID: "model-b", Family: "family-b", Enabled: &trueVal},
			}},
	}}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	units := []Unit{
		{ID: "u1", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "u2", Title: "t", Scope: []string{"b"}, Deliverable: "d", Seat: "prov-a/model-b"},
		{ID: "u3", Title: "t", Scope: []string{"c"}, Deliverable: "d"}, // no seat: run default
		{ID: "u4", Title: "t", Scope: []string{"d"}, Deliverable: "d"}, // no seat: same default
	}
	cfg := RunConfig{Task: partTask, Root: dir, Units: units}
	targets, nativeSeats, gotUnits, err := partitionTargets(cfg, reg, state, PolicyExcluder(providers.ExclusionPolicy{}))
	if err != nil {
		t.Fatalf("partitionTargets: %v", err)
	}
	if len(targets) != 4 || len(nativeSeats) != 4 || len(gotUnits) != 4 {
		t.Fatalf("targets/nativeSeats/units = %d/%d/%d; want 4 each", len(targets), len(nativeSeats), len(gotUnits))
	}
	// B5: partition mode refuses native seats, so every seat here is a provider.
	for i, ns := range nativeSeats {
		if ns {
			t.Errorf("nativeSeats[%d] = true; want false (partition mode is provider-only)", i)
		}
	}
	if targets[0].Model.ID != "model-a" || targets[0].Provider.Name != "prov-a" {
		t.Errorf("explicit provider target = %s/%s; want prov-a/model-a", targets[0].Provider.Name, targets[0].Model.ID)
	}
	if targets[1].Model.ID != "model-b" || targets[1].Provider.Name != "prov-a" {
		t.Errorf("provider target = %s/%s; want prov-a/model-b", targets[1].Provider.Name, targets[1].Model.ID)
	}
	// The seatless units share ONE deterministic run default.
	if targets[2].Provider.Name != "prov-a" || targets[2].Model.ID == "" {
		t.Errorf("default seat = %s/%s; want a resolved prov-a model", targets[2].Provider.Name, targets[2].Model.ID)
	}
	if targets[3].Provider.Name != targets[2].Provider.Name || targets[3].Model.ID != targets[2].Model.ID {
		t.Errorf("seatless units got different defaults: %s/%s vs %s/%s",
			targets[2].Provider.Name, targets[2].Model.ID, targets[3].Provider.Name, targets[3].Model.ID)
	}
	for i, u := range units {
		if gotUnits[i].ID != u.ID {
			t.Errorf("units[%d] = %q; want %q (parallel to targets)", i, gotUnits[i].ID, u.ID)
		}
	}

	// A unit seat that names two seats, or an unparsable token, is a clear error.
	if _, _, _, err := partitionTargets(RunConfig{Units: []Unit{{ID: "u", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "prov-a/model-a,prov-a/model-b"}}}, reg, state, nil); err == nil {
		t.Error("a multi-token unit seat must be rejected")
	}
	if _, _, _, err := partitionTargets(RunConfig{Units: []Unit{{ID: "u", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "nope/model-x"}}}, reg, state, nil); err == nil {
		t.Error("an unresolvable unit seat must be rejected")
	}
	// B5: a unit that names a native seat is refused outright.
	if _, _, _, nerr := partitionTargets(RunConfig{Units: []Unit{{ID: "u", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "native:gemini-x"}}}, reg, state, nil); nerr == nil {
		t.Error("a native unit seat must be refused in partition mode")
	} else if !strings.Contains(nerr.Error(), "provider seats only") {
		t.Errorf("native refusal error = %q; want the provider-seats-only message", nerr)
	}
}

// unitRunConfig is the per-unit native config seam: the copy carries the
// workspace root today and is taken per unit so per-unit root scoping can land
// in one place later.
func TestUnitRunConfigKeepsWorkspaceRoot(t *testing.T) {
	cfg := RunConfig{Root: "/ws", Task: partTask, Prompt: "run prompt", MaxTurns: 7}
	u := Unit{ID: "u1", Title: "t", Scope: []string{"src/alpha"}, Deliverable: "d"}
	got := unitRunConfig(cfg, u)
	if got.Root != "/ws" {
		t.Errorf("Root = %q; want the workspace root /ws", got.Root)
	}
	if got.MaxTurns != 7 || got.Prompt != "run prompt" || got.Task != partTask {
		t.Errorf("the unit copy dropped run-level fields: %+v", got)
	}
	// A copy, not an alias: mutating it must not reach the run config.
	got.MaxTurns = 99
	if cfg.MaxTurns != 7 {
		t.Error("unitRunConfig returned an alias, not a copy")
	}
	if empty := unitRunConfig(RunConfig{}, u); empty.Root != "." {
		t.Errorf("Root = %q; want \".\" for an empty run root", empty.Root)
	}
}

// jsonRoundTrip keeps the plan-file shape honest: ParsePlan accepts the exact
// JSON a partition plan is authored in.
func TestPartitionPlanFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	plan := writePlanFile(t, dir, "plan.json",
		`[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha","src/alpha/*.go"],"deliverable":"do alpha","seat":"prov-a/model-a"},`+
			`{"id":"unit-beta","title":"Beta","scope":["src/beta"],"deliverable":"do beta","body":"BETA-BODY"}]`)
	data, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	units, err := ParsePlan(data)
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(units) != 2 {
		t.Fatalf("units = %d; want 2", len(units))
	}
	if units[0].Seat != "prov-a/model-a" || units[1].Body != "BETA-BODY" {
		t.Errorf("units = %+v; want the seat and body verbatim", units)
	}
	// The JSON tags are the contract; guard against a silent rename.
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "title", "scope", "deliverable"} {
		if _, ok := raw[0][key]; !ok {
			t.Errorf("plan object is missing the %q key", key)
		}
	}
}

// End-to-end: the real CLI entry point (Run -> flags -> buildRunConfig ->
// executeEngine -> Execute -> HTTP) in partition mode against a stub provider,
// so the --units path is proven outside any engine seam.
func TestRunUnitsEndToEndAgainstStubProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			FinishReason: "stop",
			Message:      ChatMessage{Role: "assistant", Content: reqShapeFindings},
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	for _, rel := range []string{filepath.Join("src", "alpha", "a.go"), filepath.Join("src", "beta", "b.go")} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("package x\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	t.Setenv(providers.EnvHome, dir)
	t.Setenv("E2E_PART_KEY", "e2e-key")
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "E2E_PART_KEY",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	plan := writePlanFile(t, dir, "plan.json",
		`[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha"},`+
			`{"id":"unit-beta","title":"Beta","scope":["src/beta"],"deliverable":"do beta"}]`)
	outDir := filepath.Join(dir, "reports")

	var out, errw strings.Builder
	code := Run([]string{
		"--units", plan,
		"--root", dir,
		"--out", outDir,
		"--timeout", "60",
		"--json",
	}, nil, &out, &errw)
	if code != 0 {
		t.Fatalf("Run(--units) exit = %d, stderr = %s", code, errw.String())
	}

	var summary SummaryReport
	if err := json.Unmarshal([]byte(out.String()), &summary); err != nil {
		t.Fatalf("stdout is not the summary JSON: %v\n%s", err, out.String())
	}
	if summary.NRequested != 2 {
		t.Errorf("NRequested = %d; want 2 (one seat per unit)", summary.NRequested)
	}
	if summary.NCompleted != 2 {
		t.Errorf("NCompleted = %d; want 2 (both unit seats answered)", summary.NCompleted)
	}
	for _, seat := range summary.Seats {
		if !strings.Contains(seat.Content, "## Findings") {
			t.Errorf("seat %d content = %q; want the stub findings body", seat.SeatID, seat.Content)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "reconciliation.md")); err != nil {
		t.Errorf("partition run left no reconciliation.md: %v", err)
	}
}

// B4: the engine must validate cfg.Units and fail closed. A library caller that
// builds RunConfig.Units directly (bypassing the CLI's ParsePlan) with an
// empty, blank, or root-wide scope must get a clear error, NOT an unrestricted
// seat (a nil ToolRunner scope filter = fail-open). Mutation-verified.
func TestPartitionModeEngineRejectsUnscopedUnit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	t.Setenv("B4_KEY", "b4-key")
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: "http://127.0.0.1:1", APIKeyEnv: "B4_KEY",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal}}},
	}}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cases := map[string][]Unit{
		"empty-scope-array": {{ID: "u1", Title: "t", Deliverable: "d", Seat: "prov-a/model-a"}},
		"blank-scope-entry": {{ID: "u1", Title: "t", Scope: []string{"  "}, Deliverable: "d", Seat: "prov-a/model-a"}},
		"root-wide-scope":   {{ID: "u1", Title: "t", Scope: []string{"."}, Deliverable: "d", Seat: "prov-a/model-a"}},
	}
	for name, units := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := RunConfig{
				Task:         partTask,
				Root:         dir,
				OutDir:       filepath.Join(dir, "reports", name),
				Units:        units,
				RegistryPath: regPath,
				TimeoutSec:   30,
			}
			if _, err := Execute(context.Background(), cfg); err == nil {
				t.Fatal("Execute must reject an unscoped/root-wide unit; got nil error (fail-open)")
			} else if !strings.Contains(err.Error(), "scope") {
				t.Errorf("error = %q; want a scope rejection", err)
			}
		})
	}
}

// B7: per-unit attribution reaches the seat result and summary.json. Each
// partition seat carries its unit id, and that id must NOT leak into the request
// (system preamble or user body): it is attribution only. Mutation-verified.
func TestPartitionModeSeatResultCarriesUnitID(t *testing.T) {
	units := partitionUnits()
	run := partitionRun(t, units, requestShapeAnswer)

	// The unit id is attribution only: it must never enter the request/prompt.
	for _, r := range run.rec.snapshot() {
		for _, u := range units {
			if strings.Contains(r.SystemMsg, u.ID) {
				t.Errorf("unit id %q leaked into the shared system preamble: %q", u.ID, r.SystemMsg)
			}
			if strings.Contains(r.FirstUser, u.ID) {
				t.Errorf("unit id %q leaked into the user body: %q", u.ID, r.FirstUser)
			}
		}
	}

	// The summary carries the correct per-unit id on every seat.
	got := map[string]bool{}
	byModel := map[string]string{}
	for _, seat := range run.summary.Seats {
		if seat.UnitID == "" {
			t.Errorf("seat %d (%s/%s) carries no UnitID; want its unit id", seat.SeatID, seat.FinalTarget.Provider.Name, seat.FinalTarget.Model.ID)
			continue
		}
		got[seat.UnitID] = true
		byModel[seat.FinalTarget.Model.ID] = seat.UnitID
	}
	for _, u := range units {
		if !got[u.ID] {
			t.Errorf("summary has no seat attributed to unit %q; got %v", u.ID, got)
		}
	}
	// Cross-check the mapping by model: unit-alpha runs model-a, unit-beta model-b.
	if byModel["model-a"] != "unit-alpha" || byModel["model-b"] != "unit-beta" {
		t.Errorf("per-unit attribution wrong: model-a->%q model-b->%q; want unit-alpha/unit-beta", byModel["model-a"], byModel["model-b"])
	}
}

// B6 (engine backstop): even a library caller that bypasses the CLI gate and
// sets RunConfig.Tools to a write-capable spec gets a READ-ONLY partition seat.
// The engine clamps allowWrite/allowExec in partition mode, so write_file /
// run_command never reach the model (they sit OUTSIDE the per-unit scope
// filter). Mutation-verified: removing the clamp re-exposes write_file.
func TestPartitionModeEngineForcesReadOnlyTools(t *testing.T) {
	units := []Unit{{
		ID:          "unit-alpha",
		Title:       "Alpha work",
		Scope:       []string{"src/alpha"},
		Deliverable: "Alpha deliverable",
		Seat:        "prov-a/model-a",
	}}
	rec := &chatRecorder{respond: requestShapeAnswer}
	server := httptest.NewServer(rec)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "alpha", "ok.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "RO_KEY",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
				Capabilities: providers.Capabilities{SupportsTools: true}}}},
	}}
	t.Setenv("RO_KEY", "ro-key")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	cfg := RunConfig{
		Task:         partTask,
		Root:         dir,
		OutDir:       filepath.Join(dir, "reports"),
		Units:        units,
		Tools:        "read,search,write", // library caller tries to enable write
		RegistryPath: regPath,
		MaxTurns:     4,
		TimeoutSec:   60,
	}
	if _, err := Execute(context.Background(), cfg); err != nil {
		t.Fatalf("Execute (partition) failed: %v", err)
	}
	reqs := rec.snapshot()
	if len(reqs) == 0 {
		t.Fatal("no requests captured")
	}
	for _, r := range reqs {
		if strings.Contains(r.ToolsJSON, "write_file") || strings.Contains(r.ToolsJSON, "run_command") {
			t.Errorf("partition seat exposed a write/exec tool despite read-only mode: %s", r.ToolsJSON)
		}
	}
}

// TestPartitionExplicitExcludedSeatHonoredWithWarning (SESSION7 P4-3, D2=b):
// a plan unit that NAMES a seat keeps it even when the user's exclusion list
// covers that seat; the shared warning is the only consequence, and a seatless
// unit's run default is auto-seated from the non-excluded pool.
func TestPartitionExplicitExcludedSeatHonoredWithWarning(t *testing.T) {
	trueVal := true
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	t.Setenv("KEY_PEXC", "pexc-key")
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "prov-a",
			BaseURL:   "http://127.0.0.1:1",
			APIKeyEnv: "KEY_PEXC",
			Models: []providers.Model{
				{ID: "model-a", Family: "family-a", Enabled: &trueVal},
			},
		},
		{
			Name:      "prov-b",
			BaseURL:   "http://127.0.0.1:1",
			APIKeyEnv: "KEY_PEXC",
			Models: []providers.Model{
				{ID: "model-b", Family: "family-b", Enabled: &trueVal},
			},
		},
	}}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)
	state, err := LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	units := []Unit{
		{ID: "u1", Title: "t", Scope: []string{"a"}, Deliverable: "d", Seat: "prov-a/model-a"},
		{ID: "u2", Title: "t", Scope: []string{"b"}, Deliverable: "d"}, // no seat: run default
	}
	var warn bytes.Buffer
	cfg := RunConfig{Task: partTask, Root: dir, Units: units, WarnOut: &warn}
	policy := providers.ExclusionPolicyFromDefaults(providers.Defaults{ExcludeModels: []string{"prov-a"}})
	targets, nativeSeats, gotUnits, err := partitionTargets(cfg, reg, state, PolicyExcluder(policy))
	if err != nil {
		t.Fatalf("partitionTargets: %v", err)
	}
	if len(targets) != 2 || len(nativeSeats) != 2 || len(gotUnits) != 2 {
		t.Fatalf("targets/nativeSeats/units = %d/%d/%d; want 2 each", len(targets), len(nativeSeats), len(gotUnits))
	}
	if targets[0].Provider.Name != "prov-a" || targets[0].Model.ID != "model-a" {
		t.Fatalf("explicit excluded unit seat = %s; want prov-a/model-a (the plan is the user's instruction)", targets[0].FullName())
	}
	if targets[1].Provider.Name != "prov-b" {
		t.Fatalf("seatless unit default = %s; want prov-b (auto-seating skips the excluded provider)", targets[1].FullName())
	}
	if got := warn.String(); !strings.Contains(got, "warning: "+exclusionWarningSuffix("prov-a", "model-a")) {
		t.Fatalf("partition warning = %q; want the shared exclusion sentence", got)
	}
	if strings.Count(warn.String(), "is excluded (exclusion list)") != 1 {
		t.Fatalf("warning count = %q; want exactly one (only the explicit pick is warned)", warn.String())
	}
}
