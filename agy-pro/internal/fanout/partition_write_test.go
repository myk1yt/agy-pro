package fanout

// B4-1 pins: the engine wiring of the per-unit write opt-in.
//
// A plan unit with "write": true gets a seat whose ToolRunner has AllowWrite
// set AND whose tool list carries write_file / replace_in_file; a unit
// without the flag keeps a fully read-only seat (the write tools are absent
// from its tool list and the runner refuses them if called anyway). The
// run-level --tools write/exec clamp stays in force for every partition seat:
// the opt-in comes ONLY from the plan. Native partition seats stay refused,
// also for write units. The cross-verification path (nil Units) carries no
// write anywhere.
//
// Mutation-verified: forcing seatAllowWrite to false fails the write-unit
// half of the end-to-end pin, and dropping the scopedWriteTools check in the
// runner fails the cross-unit refusal pin (see the coder report).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// writeUnits is the two-unit wiring fixture: unit-alpha opts into write,
// unit-beta stays on the read-only default. Each names its own provider seat
// so the captured requests attribute per unit.
func writeUnits() []Unit {
	return []Unit{
		{
			ID:          "unit-alpha",
			Title:       "Alpha work",
			Scope:       []string{"src/alpha"},
			Deliverable: "Alpha deliverable",
			Seat:        "prov-a/model-a",
			Body:        "ALPHA-BODY",
			Write:       true,
		},
		{
			ID:          "unit-beta",
			Title:       "Beta work",
			Scope:       []string{"src/beta"},
			Deliverable: "Beta deliverable",
			Seat:        "prov-a/model-b",
			Body:        "BETA-BODY",
		},
	}
}

// End-to-end through the REAL engine (Execute) against the stub provider: the
// write unit's seat accepts an in-scope write and is refused a cross-unit
// write, the read-only unit's seat is refused a write inside its own scope,
// and only the write unit's request carries the write tools.
func TestPartitionModeWriteUnitEndToEnd(t *testing.T) {
	units := writeUnits()

	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		switch req.Model {
		case "model-a":
			// unit-alpha (write:true): an in-scope write, then a cross-unit
			// write attempt, then the final answer.
			switch n {
			case 0:
				return toolCallResponse("a1", "write_file", `{"path":"src/alpha/new.go","content":"package written_by_alpha","overwrite":true}`)
			case 1:
				return toolCallResponse("a2", "write_file", `{"path":"src/beta/cross.go","content":"package cross","overwrite":true}`)
			}
		case "model-b":
			// unit-beta (default): a write INSIDE its own scope must still be
			// refused (no opt-in), then the final answer.
			if n == 0 {
				return toolCallResponse("b1", "write_file", `{"path":"src/beta/new.go","content":"package beta_write","overwrite":true}`)
			}
		}
		return requestShapeAnswer(call, req)
	}
	run := partitionRun(t, units, respond)

	// The write unit's in-scope write landed on disk.
	data, err := os.ReadFile(filepath.Join(run.root, "src", "alpha", "new.go"))
	if err != nil {
		t.Fatalf("the write unit's in-scope write did not reach disk: %v", err)
	}
	if !strings.Contains(string(data), "written_by_alpha") {
		t.Fatalf("written content = %q; want the seat's bytes", data)
	}

	// Neither the cross-unit write nor the read-only unit's write happened.
	if _, err := os.Stat(filepath.Join(run.root, "src", "beta", "cross.go")); !os.IsNotExist(err) {
		t.Errorf("the cross-unit write was refused yet the file exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.root, "src", "beta", "new.go")); !os.IsNotExist(err) {
		t.Errorf("the read-only unit's write was refused yet the file exists: %v", err)
	}

	byModel := requestsByModel(run.rec.snapshot())
	alphaReqs, betaReqs := byModel["model-a"], byModel["model-b"]
	if len(alphaReqs) < 3 {
		t.Fatalf("captured %d unit-alpha requests; want at least 3 (two tool turns + final)", len(alphaReqs))
	}
	if len(betaReqs) < 2 {
		t.Fatalf("captured %d unit-beta requests; want at least 2 (tool turn + final)", len(betaReqs))
	}

	// unit-alpha turn 2: the in-scope write succeeded.
	if res := lastToolResult(alphaReqs[1]); !strings.Contains(res, "Successfully wrote") {
		t.Errorf("unit-alpha in-scope write result = %q; want the success message", res)
	}
	// unit-alpha turn 3: the cross-unit write was refused error-shaped.
	if res := lastToolResult(alphaReqs[2]); !strings.Contains(res, "outside unit scope") {
		t.Errorf("unit-alpha cross-unit write result = %q; want the scope refusal", res)
	}
	// unit-beta turn 2: the write was refused (write flag not enabled).
	if res := lastToolResult(betaReqs[1]); !strings.Contains(res, "prohibited") {
		t.Errorf("unit-beta write result = %q; want the write-flag prohibition", res)
	}

	// The tool lists: ONLY the write unit's seat carries the write tools.
	for _, r := range alphaReqs {
		if !strings.Contains(r.ToolsJSON, "write_file") || !strings.Contains(r.ToolsJSON, "replace_in_file") {
			t.Errorf("the write unit's request lost the write tools: %s", r.ToolsJSON)
		}
	}
	for _, r := range betaReqs {
		if strings.Contains(r.ToolsJSON, "write_file") || strings.Contains(r.ToolsJSON, "replace_in_file") || strings.Contains(r.ToolsJSON, "run_command") {
			t.Errorf("the read-only unit's request exposed a write/exec tool: %s", r.ToolsJSON)
		}
	}

	// Both seats still completed and the merge produced the run summary.
	if len(run.summary.Seats) != 2 {
		t.Errorf("summary seats = %d; want 2", len(run.summary.Seats))
	}
}

// The engine backstop stays fail-closed for a library caller: even with
// RunConfig.Tools set to a write-capable spec, the run-level clamp still
// forces partition seats read-only UNLESS a unit opted in per plan. Here no
// unit opts in, so the seat keeps the read-only tool list (this is the
// pre-existing B6 pin scenario; B4-1 must not weaken it).
func TestPartitionModeWriteClampWithoutOptInStaysReadOnly(t *testing.T) {
	units := []Unit{{
		ID:          "unit-alpha",
		Title:       "Alpha work",
		Scope:       []string{"src/alpha"},
		Deliverable: "Alpha deliverable",
		Seat:        "prov-a/model-a",
	}}
	run := partitionRun(t, units, requestShapeAnswer)
	for _, r := range run.rec.snapshot() {
		if strings.Contains(r.ToolsJSON, "write_file") || strings.Contains(r.ToolsJSON, "run_command") {
			t.Errorf("a partition seat without the write opt-in exposed a write/exec tool: %s", r.ToolsJSON)
		}
	}
}

// Native partition seats stay refused even when the unit opts into write: a
// native seat has no code-level scope boundary, so the opt-in cannot confine
// it and the run must fail closed before any subprocess spawns.
func TestPartitionModeNativeWriteUnitRefused(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	reg := &providers.Registry{}
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	units := []Unit{
		{ID: "unit-alpha", Title: "Alpha work", Scope: []string{"src/alpha"}, Deliverable: "Alpha deliverable", Seat: "native:gemini-alpha", Write: true},
		{ID: "unit-beta", Title: "Beta work", Scope: []string{"src/beta"}, Deliverable: "Beta deliverable", Seat: "prov-a/model-b"},
	}
	cfg := RunConfig{
		Task:           partTask,
		Root:           dir,
		OutDir:         filepath.Join(dir, "reports"),
		Units:          units,
		RegistryPath:   regPath,
		NativeResolver: stubResolver(),
		TimeoutSec:     60,
	}
	if _, err := Execute(context.Background(), cfg); err == nil {
		t.Fatal("Execute (native write unit) must refuse, got nil error (fail-open)")
	} else if !strings.Contains(err.Error(), "provider seats only") {
		t.Errorf("error = %q; want the provider-seats-only refusal", err)
	}
}

// The CLI surface binds the parsed write field into RunConfig.Units verbatim,
// so an engine called through Run() sees the same opt-in a library caller
// builds by hand.
func TestRunUnitsFlagBindsWriteField(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	dir := t.TempDir()
	plan := writePlanFile(t, dir, "plan.json",
		`[{"id":"unit-alpha","title":"Alpha","scope":["src/alpha"],"deliverable":"do alpha","write":true},`+
			`{"id":"unit-beta","title":"Beta","scope":["src/beta"],"deliverable":"do beta"}]`)

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
	if !got[0].Units[0].Write {
		t.Errorf("Units[0].Write = false; want true (\"write\":true in the plan)")
	}
	if got[0].Units[1].Write {
		t.Errorf("Units[1].Write = true; want false (the default)")
	}
}
