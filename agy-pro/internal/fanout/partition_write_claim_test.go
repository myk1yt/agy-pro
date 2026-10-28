package fanout

// R16 pin: the written-output claim walk (CONTRACT section 3.8). The merge
// expands every unit's scope globs over the then-current tree AFTER all
// seats finished, so a file CREATED by a write unit during the run enters
// the unit's claims: a single write unit's created file appears in the
// merged manifest's units[].files, and two write units whose scopes both
// claim one created path fail the run with the named conflict. The
// parent-era conflict pins exercised pre-existing fixture files only; these
// pins cover the run-created half of the claim.
//
// Investigation record (R16): no pre-existing pin was weakened. merge.go and
// merge_test.go are the parent-era B5/B6 artifacts and were not touched by
// B4 (impl-b4-1 unit-report: "merge.go was not touched"; the only
// pre-existing test edited in B4-1 is the agenttools write-scope pin). The
// parent-era pins never covered run-created files because write units did
// not exist before B4-1. The review finding is a MISSING pin, not a weakened
// one; this file closes the gap.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeClaimUnits is the two-unit write fixture: both units opt into write
// and both scopes claim the one output path the seats create.
func writeClaimUnits() []Unit {
	return []Unit{
		{
			ID:          "unit-alpha",
			Title:       "Alpha work",
			Scope:       []string{"out"},
			Deliverable: "Alpha deliverable",
			Seat:        "prov-a/model-a",
			Body:        "ALPHA-BODY",
			Write:       true,
		},
		{
			ID:          "unit-beta",
			Title:       "Beta work",
			Scope:       []string{"out"},
			Deliverable: "Beta deliverable",
			Seat:        "prov-b/model-b",
			Body:        "BETA-BODY",
			Write:       true,
		},
	}
}

// A write unit's freshly created file enters the merge claims: the merged
// manifest lists it under the unit's files, and two write units claiming the
// one created path fail the run with the named conflict (never silently
// merged).
func TestMergePartitionWrittenFileClaims(t *testing.T) {
	var mu sync.Mutex
	perModel := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu.Lock()
		n := perModel[req.Model]
		perModel[req.Model] = n + 1
		mu.Unlock()
		if n == 0 {
			return toolCallResponse("w1", "write_file", `{"path":"out/result.md","content":"# result","overwrite":true}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := mergePartitionRun(t, writeClaimUnits(), nil, respond)
	if run.err == nil {
		t.Fatal("two write units claiming one created path must FAIL the run; got nil error (silent merge)")
	}
	for _, want := range []string{"conflict", "out/result.md", "unit-alpha", "unit-beta"} {
		if !strings.Contains(run.err.Error(), want) {
			t.Errorf("run error = %q; want it to name %q", run.err.Error(), want)
		}
	}

	// The on-disk manifest records the conflict on the created path with
	// both units named.
	data, rerr := os.ReadFile(filepath.Join(run.root, "reports", "summary.json"))
	if rerr != nil {
		t.Fatalf("read summary.json: %v", rerr)
	}
	var summary SummaryReport
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("summary.json is not parseable: %v", err)
	}
	if summary.Partition == nil || summary.Partition.Status != partitionStatusFailed {
		t.Fatalf("manifest status = %+v; want failed", summary.Partition)
	}
	var found bool
	for _, c := range summary.Partition.Conflicts {
		if c.Path == "out/result.md" && len(c.Units) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("manifest conflicts = %+v; want out/result.md claimed by both units", summary.Partition.Conflicts)
	}
}

// Single write unit: the created file appears in the unit's claims (the
// happy path merges), so the claim walk demonstrably covers run-created
// files, not only pre-existing fixture files.
func TestMergePartitionSingleWriteUnitClaimsCreatedFile(t *testing.T) {
	units := []Unit{{
		ID:          "unit-alpha",
		Title:       "Alpha work",
		Scope:       []string{"out"},
		Deliverable: "Alpha deliverable",
		Seat:        "prov-a/model-a",
		Body:        "ALPHA-BODY",
		Write:       true,
	}}
	var mu2 sync.Mutex
	perModel2 := map[string]int{}
	respond := func(call int, req capturedChatRequest) ChatResponse {
		mu2.Lock()
		n := perModel2[req.Model]
		perModel2[req.Model] = n + 1
		mu2.Unlock()
		if n == 0 {
			return toolCallResponse("w1", "write_file", `{"path":"out/result.md","content":"# result","overwrite":true}`)
		}
		return requestShapeAnswer(call, req)
	}
	run := mergePartitionRun(t, units, nil, respond)
	if run.err != nil {
		t.Fatalf("single write unit run failed: %v", run.err)
	}
	if _, err := os.Stat(filepath.Join(run.root, "out", "result.md")); err != nil {
		t.Fatalf("the written file is not on disk: %v", err)
	}
	claimed := false
	for _, u := range run.summary.Partition.Units {
		if u.UnitID != "unit-alpha" {
			continue
		}
		for _, f := range u.Files {
			if f == "out/result.md" {
				claimed = true
			}
		}
	}
	if !claimed {
		t.Errorf("manifest unit files = %+v; want the created out/result.md claimed", run.summary.Partition.Units)
	}
}
