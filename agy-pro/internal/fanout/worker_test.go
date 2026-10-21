package fanout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// O3: the worker's two error compositions name the seat target through
// Target.FullName, and that text is handed to SeatResult.Error and, on the
// failover path, to Failovers[].Reason. The report writers only seed-redact
// those fields (reconcile.go, merge.go), and the seed set is the registry key
// values plus generic shapes whose patterns key off a dash vendor prefix
// ("sk-", "pk-", "tok-"), so the underscore-head class and a bare base64 run
// joined over '+', '/', '=' ride through raw. The operand is therefore masked
// at composition time, through the same registry-slot authority (MaskSlot) the
// sibling seatSpecLabel uses. The plants below are the two classes the census
// named, plus the vendor underscore head.
const (
	workerSlotPlantVendor     = testfixtures.SKLive
	workerSlotPlantUnderscore = "_Ab3dE6gH9jK2mN5pQ8sT1vW4yZ7"
	workerSlotPlantDecorated  = "AbCdEf0123+GhIjKl4567=MmNoPq"
)

// workerSlotServer answers the seat's chat completions with one canned response
// per call, so a pin can drive the loop arm and the synthesis arm separately.
func workerSlotServer(t *testing.T, respond func(call int) ChatResponse) string {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls++
		resp := respond(calls)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// workerSlotEmptyAnswer is the no-content, no-tool-call completion: the loop
// arm fails on it, and so does the tools-nil synthesis retry.
func workerSlotEmptyAnswer() ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
		Message: ChatMessage{Role: "assistant", Content: ""}}}}
}

// workerSlotToolCallAnswer asks for one read_file, so maxTurns=1 consumes the
// only loop turn and the seat moves on to the synthesis arm.
func workerSlotToolCallAnswer() ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{
		Index:        0,
		FinishReason: "tool_calls",
		Message: ChatMessage{Role: "assistant", ToolCalls: []ChatToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"probe.go"}`},
		}}},
	}}}
}

// workerSlotFindingsAnswer is a one-turn findings body, so a failover target
// succeeds and the seat reports the hop it took to get there.
func workerSlotFindingsAnswer() ChatResponse {
	return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
		Message: ChatMessage{Role: "assistant", Content: "## Findings\n- ok"}}}}
}

// workerSlotSeat builds the worker under test: one provider seat against the
// canned server, with a runner rooted in a temp dir holding the one file the
// tool-call turn reads.
func workerSlotSeat(t *testing.T, target providers.Target, reg *providers.Registry, maxTurns int) *SeatWorker {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write runner root: %v", err)
	}
	return &SeatWorker{
		SeatID:        1,
		InitialTarget: target,
		Registry:      reg,
		Preset:        GetTaskPreset("review"),
		UserPrompt:    "probe",
		Runner:        agenttools.NewRunner(root, false, false, nil),
		Client:        NewClient(),
		MaxTurns:      maxTurns,
	}
}

// workerSlotTarget composes the seat target. FullName is "<provider>/<model>",
// so a plant in either half has to mask in the composed form.
func workerSlotTarget(providerName, modelID, serverURL string) providers.Target {
	return providers.Target{
		Provider: providers.Provider{Name: providerName, BaseURL: serverURL},
		Model:    providers.Model{ID: modelID, Family: "family-a"},
	}
}

// TestWorkerLoopErrorMasksTargetSlot pins the empty-content arm (the loop
// error): a credential-shaped provider name is masked in the text that reaches
// SeatResult.Error, and a benign target renders byte-identically.
func TestWorkerLoopErrorMasksTargetSlot(t *testing.T) {
	cases := []struct{ name, providerName, wantMask string }{
		{"vendor underscore head", workerSlotPlantVendor, "sk_li****"},
		{"bare underscore head", workerSlotPlantUnderscore, "_Ab3d****"},
		{"decorated base64 run", workerSlotPlantDecorated, "AbCdE****"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := workerSlotServer(t, func(int) ChatResponse { return workerSlotEmptyAnswer() })
			w := workerSlotSeat(t, workerSlotTarget(tc.providerName, "model-a", srv), &providers.Registry{}, 0)
			res := w.Run(context.Background())
			if res.Status != "failed" {
				t.Fatalf("seat status = %q; want failed (error %q)", res.Status, res.Error)
			}
			want := "all failover attempts exhausted; last error: received empty content response from " + tc.wantMask
			if res.Error != want {
				t.Errorf("SeatResult.Error = %q; want %q", res.Error, want)
			}
			if strings.Contains(res.Error, tc.providerName) {
				t.Errorf("SeatResult.Error carries the raw slot: %q", res.Error)
			}
		})
	}
	t.Run("benign target keeps its bytes", func(t *testing.T) {
		srv := workerSlotServer(t, func(int) ChatResponse { return workerSlotEmptyAnswer() })
		w := workerSlotSeat(t, workerSlotTarget("prov-a", "model-a", srv), &providers.Registry{}, 0)
		res := w.Run(context.Background())
		const want = "all failover attempts exhausted; last error: received empty content response from prov-a/model-a"
		if res.Error != want {
			t.Errorf("SeatResult.Error = %q; want %q", res.Error, want)
		}
	})
}

// TestWorkerSynthesisErrorMasksTargetSlot pins the second composition site (the
// empty synthesis retry): the same operand, the same authority, the same
// reachable sink.
func TestWorkerSynthesisErrorMasksTargetSlot(t *testing.T) {
	cases := []struct{ name, providerName, wantMask string }{
		{"vendor underscore head", workerSlotPlantVendor, "sk_li****"},
		{"bare underscore head", workerSlotPlantUnderscore, "_Ab3d****"},
		{"decorated base64 run", workerSlotPlantDecorated, "AbCdE****"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := workerSlotServer(t, func(call int) ChatResponse {
				if call == 1 {
					return workerSlotToolCallAnswer()
				}
				return workerSlotEmptyAnswer()
			})
			w := workerSlotSeat(t, workerSlotTarget(tc.providerName, "model-a", srv), &providers.Registry{}, 1)
			res := w.Run(context.Background())
			if res.Status != "failed" {
				t.Fatalf("seat status = %q; want failed (error %q)", res.Status, res.Error)
			}
			want := "all failover attempts exhausted; last error: synthesis produced empty content from " + tc.wantMask
			if res.Error != want {
				t.Errorf("SeatResult.Error = %q; want %q", res.Error, want)
			}
			if strings.Contains(res.Error, tc.providerName) {
				t.Errorf("SeatResult.Error carries the raw slot: %q", res.Error)
			}
		})
	}
	t.Run("benign target keeps its bytes", func(t *testing.T) {
		srv := workerSlotServer(t, func(call int) ChatResponse {
			if call == 1 {
				return workerSlotToolCallAnswer()
			}
			return workerSlotEmptyAnswer()
		})
		w := workerSlotSeat(t, workerSlotTarget("prov-a", "model-a", srv), &providers.Registry{}, 1)
		res := w.Run(context.Background())
		const want = "all failover attempts exhausted; last error: synthesis produced empty content from prov-a/model-a"
		if res.Error != want {
			t.Errorf("SeatResult.Error = %q; want %q", res.Error, want)
		}
	})
}

// TestWorkerFailoverReasonMasksTargetSlot pins the second sink: the same error
// text is copied onto Failovers[].Reason, so a plant in either half of the
// composed target has to arrive there masked too. The hop's name fields
// (from_prov/from_mod/to_prov/to_mod) are masked by the report writers
// downstream and are out of this pin's scope.
func TestWorkerFailoverReasonMasksTargetSlot(t *testing.T) {
	cases := []struct{ name, providerName, modelID, plant, wantMask string }{
		{"provider-name plant", workerSlotPlantUnderscore, "model-a", workerSlotPlantUnderscore, "_Ab3d****"},
		{"model-id plant", "prov-a", workerSlotPlantDecorated, workerSlotPlantDecorated, "prov-****"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := workerSlotServer(t, func(call int) ChatResponse {
				if call == 1 {
					return workerSlotEmptyAnswer()
				}
				return workerSlotFindingsAnswer()
			})
			// Step 1 failover needs a second provider offering the same model id.
			reg := &providers.Registry{Providers: []providers.Provider{
				{Name: tc.providerName, BaseURL: srv, Models: []providers.Model{{ID: tc.modelID, Family: "family-a"}}},
				{Name: "prov-b", BaseURL: srv, Models: []providers.Model{{ID: tc.modelID, Family: "family-a"}}},
			}}
			w := workerSlotSeat(t, workerSlotTarget(tc.providerName, tc.modelID, srv), reg, 0)
			res := w.Run(context.Background())
			if res.Status != "success" {
				t.Fatalf("seat status = %q; want success after the failover hop (error %q)", res.Status, res.Error)
			}
			if len(res.Failovers) != 1 {
				t.Fatalf("failover records = %d; want 1", len(res.Failovers))
			}
			hop := res.Failovers[0]
			if hop.Step != 1 {
				t.Fatalf("failover step = %d; want 1", hop.Step)
			}
			want := "received empty content response from " + tc.wantMask
			if hop.Reason != want {
				t.Errorf("Failovers[0].Reason = %q; want %q", hop.Reason, want)
			}
			if strings.Contains(hop.Reason, tc.plant) {
				t.Errorf("Failovers[0].Reason carries the raw slot: %q", hop.Reason)
			}
		})
	}
	t.Run("benign target keeps its bytes", func(t *testing.T) {
		srv := workerSlotServer(t, func(call int) ChatResponse {
			if call == 1 {
				return workerSlotEmptyAnswer()
			}
			return workerSlotFindingsAnswer()
		})
		reg := &providers.Registry{Providers: []providers.Provider{
			{Name: "prov-a", BaseURL: srv, Models: []providers.Model{{ID: "model-a", Family: "family-a"}}},
			{Name: "prov-b", BaseURL: srv, Models: []providers.Model{{ID: "model-a", Family: "family-a"}}},
		}}
		w := workerSlotSeat(t, workerSlotTarget("prov-a", "model-a", srv), reg, 0)
		res := w.Run(context.Background())
		if len(res.Failovers) != 1 {
			t.Fatalf("failover records = %d; want 1", len(res.Failovers))
		}
		const want = "received empty content response from prov-a/model-a"
		if got := res.Failovers[0].Reason; got != want {
			t.Errorf("Failovers[0].Reason = %q; want %q", got, want)
		}
	})
}
