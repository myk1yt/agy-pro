package fanout

// Thin-seat classification + usage/cache stats pins.
//
// Part 1 (thin seats): a seat that returns success but produces NO findings
// signal used to count toward the consensus denominator and mislead the run
// (the Charm qwen seat whose summary body was missing, the native seat that
// "succeeded" with "Waiting for the subagent swarm"). These pins hold the
// classification, the denominator exclusion and the reporting surfaces.
//
// Part 2 (usage/cache): provider responses carry usage
// (prompt_tokens/completion_tokens, and prompt_tokens_details.cached_tokens on
// providers with a prompt cache) and the engine used to discard it, so the real
// cache-hit ratio per run was invisible. These pins hold the per-seat
// accounting, the run totals and the "absent stays absent" rule.

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

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// The two shapes the classification must separate: a findings-free success body
// (thin) and the preset-mandated findings section (real).
const (
	thinBody = "I explored the workspace and have nothing to report."
	realBody = "## Findings\n- **[HIGH]** Nil deref: missing nil check (Location: a.go:1)\n"
)

func int64Ptr(v int64) *int64 { return &v }

// --- Part 1: classification ------------------------------------------------

func TestSeatIsThinClassifiesFindingsSignal(t *testing.T) {
	cases := []struct {
		name   string
		status string
		body   string
		thin   bool
	}{
		{"empty body", "success", "", true},
		{"prose without findings", "success", thinBody, true},
		{"heading with no items", "success", "## Findings", true},
		{"heading plus severity item", "success", realBody, false},
		{"severity item without heading", "success", "- **[MEDIUM]** config drift\n", false},
		{"bullet under the findings section", "success", "## Findings\n- plain prose bullet\n", false},
		{"failed seat is not thin", "failed", thinBody, false},
		{"degraded seat is not thin", "degraded", thinBody, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seat := &SeatResult{SeatID: 1, Status: tc.status, Content: tc.body}
			if got := seatIsThin(seat); got != tc.thin {
				t.Errorf("seatIsThin(%q) = %v; want %v", tc.body, got, tc.thin)
			}
		})
	}
	if seatIsThin(nil) {
		t.Error("seatIsThin(nil) = true; want false")
	}
}

// Parser reuse: the classifier must read the SAME findings parser
// reconciliation, the seat reports and the progress line read. A second
// severity regex or a heading-only check would drift from the consensus matrix,
// so thin is re-derived from parseFindings for every body shape here.
func TestSeatIsThinReusesTheReconcileFindingsParser(t *testing.T) {
	for _, body := range []string{
		"",
		thinBody,
		"## Findings",
		realBody,
		"- **[MEDIUM]** config drift\n",
		"## Findings\n- plain prose bullet\n",
		"## findings\n* [LOW] lowercase heading, star bullet\n",
		"1. **[BLOCKER]** numbered item\n",
	} {
		seat := &SeatResult{SeatID: 7, Status: "success", Content: body}
		parsed := len(parseFindings(seat.SeatID, "", "", seat.Content))
		if got, want := seatIsThin(seat), parsed == 0; got != want {
			t.Errorf("seatIsThin(%q) = %v; want %v (parseFindings returned %d findings)",
				body, got, want, parsed)
		}
	}
}

// The consensus denominator: 1 real seat + 2 thin seats must report
// "Seats Evaluated: 1 / 3" with a line naming the excluded seats, and the
// exclusion must reach summary.json and the seat reports.
func TestReconcileExcludesThinSeatsFromConsensusDenominator(t *testing.T) {
	outDir := t.TempDir()
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	seats := []*SeatResult{
		{SeatID: 1, InitialTarget: target, FinalTarget: target, Status: "success", Content: realBody},
		{SeatID: 2, InitialTarget: target, FinalTarget: target, Status: "success", Content: thinBody},
		{SeatID: 3, InitialTarget: target, FinalTarget: target, Status: "success", Content: ""},
	}

	summary, err := Reconcile("review", t.TempDir(), outDir, 3, seats)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if summary.NCompleted != 1 || summary.NRequested != 3 {
		t.Fatalf("Seats Evaluated = %d / %d; want 1 / 3 (the 2 thin seats excluded)", summary.NCompleted, summary.NRequested)
	}
	if summary.ThinSeats != 2 {
		t.Errorf("ThinSeats = %d; want 2", summary.ThinSeats)
	}
	if len(summary.ThinSeatIDs) != 2 || summary.ThinSeatIDs[0] != 2 || summary.ThinSeatIDs[1] != 3 {
		t.Errorf("ThinSeatIDs = %v; want [2 3]", summary.ThinSeatIDs)
	}
	if !summary.Seats[1].Thin || !summary.Seats[2].Thin {
		t.Errorf("thin seats must carry thin=true: seat2=%v seat3=%v", summary.Seats[1].Thin, summary.Seats[2].Thin)
	}
	if summary.Seats[0].Thin {
		t.Error("a seat with findings must not be marked thin")
	}
	// C7: Reconcile must not mutate its input. The thin flag is derived for the
	// seat report and the summary copy, never stamped onto the caller's seats.
	for _, s := range seats {
		if s.Thin {
			t.Errorf("Reconcile mutated the caller's seat %d (Thin=true)", s.SeatID)
		}
	}

	md := readOutFile(t, outDir, "reconciliation.md")
	for _, want := range []string{
		"Seats Evaluated:** `1 / 3`",
		"**Thin Seats Excluded:** `2 / 3` (seats 2, 3)",
		"- **Thin Seats Excluded:** 2 (seats 2, 3)",
		"excluded from the consensus denominator",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("reconciliation.md missing %q:\n%s", want, md)
		}
	}

	sumRaw := readOutFile(t, outDir, "summary.json")
	if n := strings.Count(sumRaw, `"thin": true`); n != 2 {
		t.Errorf("summary.json carries %d thin=true seats; want 2:\n%s", n, sumRaw)
	}
	if !strings.Contains(sumRaw, `"thin_seats": 2`) {
		t.Errorf("summary.json missing the thin_seats total:\n%s", sumRaw)
	}
	if !strings.Contains(sumRaw, `"thin_seat_ids"`) {
		t.Errorf("summary.json missing the thin_seat_ids list:\n%s", sumRaw)
	}

	// The seat report header carries the row, and only for a thin seat.
	for _, id := range []int{2, 3} {
		report := readOutFile(t, outDir, "seat-"+string(rune('0'+id))+"-*.md")
		if !strings.Contains(report, "| Thin | true |") {
			t.Errorf("seat %d report missing the Thin row:\n%s", id, report)
		}
	}
	if report := readOutFile(t, outDir, "seat-1-*.md"); strings.Contains(report, "| Thin |") {
		t.Errorf("a non-thin seat report must not carry the Thin row:\n%s", report)
	}
}

// A seat with findings keeps today's behavior: counted, not marked, and no
// thin surface anywhere in the artifacts.
func TestReconcileSeatWithFindingsIsCountedAndNotMarkedThin(t *testing.T) {
	outDir := t.TempDir()
	target := providers.Target{
		Provider: providers.Provider{Name: "prov"},
		Model:    providers.Model{ID: "mod"},
	}
	var seats []*SeatResult
	for id := 1; id <= 3; id++ {
		seats = append(seats, &SeatResult{
			SeatID: id, InitialTarget: target, FinalTarget: target,
			Status: "success", Content: realBody,
		})
	}

	summary, err := Reconcile("review", t.TempDir(), outDir, 3, seats)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if summary.NCompleted != 3 || summary.NRequested != 3 {
		t.Fatalf("Seats Evaluated = %d / %d; want 3 / 3", summary.NCompleted, summary.NRequested)
	}
	if summary.ThinSeats != 0 || len(summary.ThinSeatIDs) != 0 {
		t.Errorf("no seat is thin, but ThinSeats=%d ids=%v", summary.ThinSeats, summary.ThinSeatIDs)
	}
	for _, s := range summary.Seats {
		if s.Thin {
			t.Errorf("seat %d marked thin with findings present", s.SeatID)
		}
	}

	sumRaw := readOutFile(t, outDir, "summary.json")
	for _, unwanted := range []string{`"thin"`, `"thin_seats"`, `"thin_seat_ids"`} {
		if strings.Contains(sumRaw, unwanted) {
			t.Errorf("summary.json must not carry %s for a clean run:\n%s", unwanted, sumRaw)
		}
	}
	md := readOutFile(t, outDir, "reconciliation.md")
	if strings.Contains(md, "Thin") {
		t.Errorf("reconciliation.md must not mention thin seats for a clean run:\n%s", md)
	}
	for i := 1; i <= 3; i++ {
		if report := readOutFile(t, outDir, "seat-"+string(rune('0'+i))+"-*.md"); strings.Contains(report, "| Thin |") {
			t.Errorf("seat %d report must not carry the Thin row:\n%s", i, report)
		}
	}
}

// mixedThinRun stands up two provider seats on one stub server: model-a answers
// with findings, model-b answers with a findings-free body. It returns the run
// summary, the artifact dir and the progress stream.
func mixedThinRun(t *testing.T) (summary *SummaryReport, outDir string, stream string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := thinBody
		if req.Model == "model-a" {
			content = realBody
		}
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: content},
			FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "MIXED_THIN_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "MIXED_THIN_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal}}},
	}}
	t.Setenv("MIXED_THIN_KEY_A", "key-a")
	t.Setenv("MIXED_THIN_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir = filepath.Join(dir, "reports")
	sink := &bytes.Buffer{}
	cfg := RunConfig{
		Task:         "review",
		Prompt:       "thin pin",
		Seats:        []SeatSpec{{Kind: SeatKindProvider, Raw: "prov-a/model-a", ModelID: "model-a"}, {Kind: SeatKindProvider, Raw: "prov-b/model-b", ModelID: "model-b"}},
		Root:         dir,
		OutDir:       outDir,
		RegistryPath: regPath,
		TimeoutSec:   30,
		ProgressOut:  sink,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return summary, outDir, sink.String()
}

// End-to-end: the progress line's thin marker, the summary flag and the
// consensus denominator must all agree, because they read one classifier.
func TestThinSeatProgressMarkerAgreesWithReconcileClassification(t *testing.T) {
	summary, outDir, stream := mixedThinRun(t)

	var thinLine, realLine string
	for _, line := range strings.Split(strings.TrimSuffix(stream, "\n"), "\n") {
		switch {
		case strings.Contains(line, "model-a ->"):
			realLine = line
		case strings.Contains(line, "model-b ->"):
			thinLine = line
		}
	}
	if realLine == "" || thinLine == "" {
		t.Fatalf("missing finish lines:\n%s", stream)
	}
	if !strings.Contains(realLine, "findings: 1") || strings.Contains(realLine, "thin") {
		t.Errorf("real seat finish line = %q; want findings: 1 and no thin marker", realLine)
	}
	if !strings.Contains(thinLine, ", thin)") || strings.Contains(thinLine, "findings:") {
		t.Errorf("thin seat finish line = %q; want the thin marker and no findings count", thinLine)
	}

	if summary.NCompleted != 1 || summary.NRequested != 2 {
		t.Fatalf("Seats Evaluated = %d / %d; want 1 / 2", summary.NCompleted, summary.NRequested)
	}
	for _, s := range summary.Seats {
		want := s.FinalTarget.Model.ID == "model-b"
		if s.Thin != want {
			t.Errorf("seat %d (%s) Thin = %v; want %v", s.SeatID, s.FinalTarget.Model.ID, s.Thin, want)
		}
	}
	if md := readOutFile(t, outDir, "reconciliation.md"); !strings.Contains(md, "**Thin Seats Excluded:** `1 / 2`") {
		t.Errorf("reconciliation.md missing the exclusion line:\n%s", md)
	}
}

// The console banner names the excluded seats instead of silently shrinking
// "Seats Evaluated".
func TestConsoleBannerNamesExcludedThinSeats(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		return &SummaryReport{
			Task: cfg.Task, NRequested: 3, NCompleted: 1,
			ThinSeats: 2, ThinSeatIDs: []int{2, 3},
		}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-n", "3"}, nil, &out, &errw); code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr:\n%s", code, errw.String())
	}
	for _, want := range []string{
		"Seats Evaluated:       1 / 3",
		"Thin Seats Excluded:   2 (seats 2, 3)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("banner missing %q:\n%s", want, out.String())
		}
	}

	// A clean run keeps the banner byte-identical (no thin line).
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		return &SummaryReport{Task: cfg.Task, NRequested: 3, NCompleted: 3}, nil
	}
	var out2, errw2 bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-n", "3"}, nil, &out2, &errw2); code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr:\n%s", code, errw2.String())
	}
	if strings.Contains(out2.String(), "Thin") {
		t.Errorf("a clean run must not print a thin line:\n%s", out2.String())
	}
	if !strings.Contains(out2.String(), "Seats Evaluated:       3 / 3") {
		t.Errorf("clean banner lost the seat count:\n%s", out2.String())
	}
}

// --- Part 2: usage / cache stats -------------------------------------------

func TestChatUsageParsesOptionalFields(t *testing.T) {
	var full ChatResponse
	if err := json.Unmarshal([]byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_tokens_details":{"cached_tokens":960}}}`), &full); err != nil {
		t.Fatalf("unmarshal full usage: %v", err)
	}
	if full.Usage == nil {
		t.Fatal("full usage block parsed as nil")
	}
	if full.Usage.PromptTokens == nil || *full.Usage.PromptTokens != 1000 {
		t.Errorf("prompt_tokens = %v; want 1000", full.Usage.PromptTokens)
	}
	if full.Usage.CompletionTokens == nil || *full.Usage.CompletionTokens != 200 {
		t.Errorf("completion_tokens = %v; want 200", full.Usage.CompletionTokens)
	}
	if c := full.Usage.cachedTokens(); c == nil || *c != 960 {
		t.Errorf("cached_tokens = %v; want 960", c)
	}

	// Partial: usage without the prompt_tokens_details sub-object.
	var partial ChatResponse
	if err := json.Unmarshal([]byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`), &partial); err != nil {
		t.Fatalf("unmarshal partial usage: %v", err)
	}
	if partial.Usage == nil {
		t.Fatal("partial usage block parsed as nil")
	}
	if c := partial.Usage.cachedTokens(); c != nil {
		t.Errorf("cached_tokens = %v; want nil (the provider reported none)", c)
	}

	// Absent: no usage key at all -> nil, never a zeroed struct.
	var absent ChatResponse
	if err := json.Unmarshal([]byte(`{"id":"x","choices":[]}`), &absent); err != nil {
		t.Fatalf("unmarshal absent usage: %v", err)
	}
	if absent.Usage != nil {
		t.Errorf("Usage = %+v; want nil", absent.Usage)
	}

	// The wire shape of a usage-less response must not gain a "usage" key, so
	// every existing stub server keeps encoding the same bytes.
	encoded, err := json.Marshal(ChatResponse{ID: "x", Choices: []ChatChoice{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "usage") {
		t.Errorf("a nil usage must not serialize:\n%s", encoded)
	}
}

// providerUsageRun stands up one tool-capable provider seat whose responses are
// produced by respond, runs the swarm and returns the summary plus artifact dir.
func providerUsageRun(t *testing.T, respond func(call int) ChatResponse) (*SummaryReport, string) {
	t.Helper()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := respond(int(atomic.AddInt32(&calls, 1)))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}
	t.Setenv(providers.EnvHome, dir)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "USAGE_KEY",
		Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal,
			Capabilities: providers.Capabilities{SupportsTools: true, AllowedTools: []string{"read_file"}}}},
	}}}
	t.Setenv("USAGE_KEY", "key-a")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task: "review", Prompt: "usage pin", N: 1, Root: dir, OutDir: outDir,
		RegistryPath: regPath, MaxTurns: 5, TimeoutSec: 30,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	return summary, outDir
}

// A provider that reports usage (incl. the cached-token breakdown) must surface
// per-seat usage, run totals and the cache-hit ratio in summary.json. The seat
// makes two chat turns, so the totals prove accumulation, not last-turn
// overwrite.
func TestProviderSeatUsageAccumulatesIntoSummary(t *testing.T) {
	summary, outDir := providerUsageRun(t, func(call int) ChatResponse {
		usage := &ChatUsage{PromptTokens: int64Ptr(1000), CompletionTokens: int64Ptr(120)}
		usage.PromptTokensDetails = &ChatPromptTokensDetails{CachedTokens: int64Ptr(900)}
		if call == 1 {
			return ChatResponse{
				Choices: []ChatChoice{{Index: 0, FinishReason: "tool_calls", Message: ChatMessage{
					Role: "assistant",
					ToolCalls: []ChatToolCall{{ID: "c1", Type: "function",
						Function: ChatFunctionCall{Name: "read_file", Arguments: `{"path":"target.go"}`}}},
				}}},
				Usage: usage,
			}
		}
		return ChatResponse{
			Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
				Message: ChatMessage{Role: "assistant", Content: realBody}}},
			Usage: usage,
		}
	})

	if len(summary.Seats) != 1 {
		t.Fatalf("seats = %d; want 1", len(summary.Seats))
	}
	seat := summary.Seats[0]
	if seat.Usage == nil {
		t.Fatal("seat usage = nil; want the accumulated totals")
	}
	if seat.Usage.PromptTokens == nil || *seat.Usage.PromptTokens != 2000 {
		t.Errorf("seat prompt_tokens = %v; want 2000 (two turns accumulated)", seat.Usage.PromptTokens)
	}
	if seat.Usage.CompletionTokens == nil || *seat.Usage.CompletionTokens != 240 {
		t.Errorf("seat completion_tokens = %v; want 240", seat.Usage.CompletionTokens)
	}
	if seat.Usage.CachedTokens == nil || *seat.Usage.CachedTokens != 1800 {
		t.Errorf("seat cached_tokens = %v; want 1800", seat.Usage.CachedTokens)
	}
	if seat.ToolCallCount != 1 {
		t.Errorf("tool calls = %d; want 1 (the two-turn pin)", seat.ToolCallCount)
	}

	if summary.Usage == nil {
		t.Fatal("run usage = nil; want the totals block")
	}
	if summary.Usage.PromptTokens == nil || *summary.Usage.PromptTokens != 2000 {
		t.Errorf("run prompt_tokens = %v; want 2000", summary.Usage.PromptTokens)
	}
	if summary.Usage.CacheHitRatio == nil || *summary.Usage.CacheHitRatio != 0.9 {
		t.Errorf("cache_hit_ratio = %v; want 0.9 (1800/2000)", summary.Usage.CacheHitRatio)
	}

	sumRaw := readOutFile(t, outDir, "summary.json")
	for _, want := range []string{
		`"usage": {`,
		`"prompt_tokens": 2000`,
		`"completion_tokens": 240`,
		`"cached_tokens": 1800`,
		`"cache_hit_ratio": 0.9`,
	} {
		if !strings.Contains(sumRaw, want) {
			t.Errorf("summary.json missing %s:\n%s", want, sumRaw)
		}
	}
}

// A provider that reports no usage must leave the fields absent: the seat entry
// stays "usage": null and the run carries no totals and no invented zeros.
func TestProviderResponseWithoutUsageLeavesFieldsAbsent(t *testing.T) {
	summary, outDir := providerUsageRun(t, func(call int) ChatResponse {
		return ChatResponse{Choices: []ChatChoice{{Index: 0, FinishReason: "stop",
			Message: ChatMessage{Role: "assistant", Content: realBody}}}}
	})

	if len(summary.Seats) != 1 {
		t.Fatalf("seats = %d; want 1", len(summary.Seats))
	}
	if summary.Seats[0].Usage != nil {
		t.Errorf("seat usage = %+v; want nil (the provider reported none)", summary.Seats[0].Usage)
	}
	if summary.Usage != nil {
		t.Errorf("run usage = %+v; want nil", summary.Usage)
	}

	sumRaw := readOutFile(t, outDir, "summary.json")
	if !strings.Contains(sumRaw, `"usage": null`) {
		t.Errorf("summary.json must carry \"usage\": null for a usage-less seat:\n%s", sumRaw)
	}
	for _, unwanted := range []string{"prompt_tokens", "completion_tokens", "cached_tokens", "cache_hit_ratio", "total_tokens"} {
		if strings.Contains(sumRaw, unwanted) {
			t.Errorf("summary.json must not carry %s when nothing was reported:\n%s", unwanted, sumRaw)
		}
	}
}

// A native seat prints only its answer (agy -p carries no usage block), so its
// usage is null and the run totals stay absent. No number is faked.
func TestNativeSeatUsageIsNull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:           "review",
		Prompt:         "native usage pin",
		Seats:          []SeatSpec{{Kind: SeatKindNative, Raw: "native:gemini-x", ModelID: "gemini-x"}},
		Root:           dir,
		OutDir:         outDir,
		RegistryPath:   filepath.Join(dir, "no-such-providers.json"),
		TimeoutSec:     30,
		NativeResolver: stubResolver(),
		SpawnCommand:   stubSpawn(map[string]string{"AGY_PRO_STUB_STDOUT": realBody}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(summary.Seats) != 1 {
		t.Fatalf("seats = %d; want 1", len(summary.Seats))
	}
	if summary.Seats[0].Usage != nil {
		t.Errorf("native seat usage = %+v; want nil", summary.Seats[0].Usage)
	}
	if summary.Usage != nil {
		t.Errorf("run usage = %+v; want nil for an all-native run", summary.Usage)
	}
	sumRaw := readOutFile(t, outDir, "summary.json")
	if !strings.Contains(sumRaw, `"usage": null`) {
		t.Errorf("summary.json must mark the native seat \"usage\": null:\n%s", sumRaw)
	}
	if strings.Contains(sumRaw, "cache_hit_ratio") {
		t.Errorf("an all-native run must not carry a cache ratio:\n%s", sumRaw)
	}
}

// The totals renderer keeps the "absent stays absent" rule: no block when
// nothing was reported, no cached/ratio fields without a cached count, and no
// ratio when the prompt total is zero (a division the engine refuses to do).
func TestUsageTotalsRenderingKeepsAbsentFieldsAbsent(t *testing.T) {
	var empty usageTotals
	if got := empty.seatUsage(); got != nil {
		t.Errorf("seatUsage() = %+v; want nil", got)
	}
	if got := empty.runUsage(); got != nil {
		t.Errorf("runUsage() = %+v; want nil", got)
	}

	var promptOnly usageTotals
	promptOnly.add(&ChatResponse{Usage: &ChatUsage{PromptTokens: int64Ptr(500), CompletionTokens: int64Ptr(50)}})
	if got := promptOnly.seatUsage(); got == nil || got.CachedTokens != nil {
		t.Errorf("seatUsage() = %+v; want no cached_tokens", got)
	}
	ru := promptOnly.runUsage()
	if ru == nil || ru.CachedTokens != nil || ru.CacheHitRatio != nil {
		t.Errorf("runUsage() = %+v; want no cached_tokens and no cache_hit_ratio", ru)
	}

	var zeroPrompt usageTotals
	zeroPrompt.add(&ChatResponse{Usage: &ChatUsage{
		PromptTokens: int64Ptr(0), CompletionTokens: int64Ptr(7),
		PromptTokensDetails: &ChatPromptTokensDetails{CachedTokens: int64Ptr(5)},
	}})
	zeroRu := zeroPrompt.runUsage()
	if zeroRu == nil || zeroRu.CacheHitRatio != nil {
		t.Errorf("runUsage() = %+v; want no ratio on a zero prompt total", zeroRu)
	}
	if zeroRu.CachedTokens == nil || *zeroRu.CachedTokens != 5 {
		t.Errorf("runUsage().CachedTokens = %v; want 5", zeroRu.CachedTokens)
	}
}
