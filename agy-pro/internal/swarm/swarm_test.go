package swarm

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agypro/internal/fanout"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// --- BuildFanoutArgs golden (contract U3: "spec tokens exactly inventory Spec join") ---

func TestBuildFanoutArgsGolden(t *testing.T) {
	tmpl := Templates()[0] // code-review
	seats := []inventory.Entry{
		{Number: 1, Kind: "native", ModelID: "gemini-3.8-flash-high", Spec: "native:gemini-3.8-flash-high"},
		{Number: 2, Kind: "provider", ModelID: "glm-5.3-flash-flex", Spec: "nw/glm-5.3-flash-flex"},
	}
	got := BuildFanoutArgs(tmpl, seats, "/ws", "Fix the bugs", 90)
	want := []string{
		"--task", "review",
		"--seats", "native:gemini-3.8-flash-high,nw/glm-5.3-flash-flex",
		"--root", "/ws",
		"--timeout", "90",
		"--tools", "read,search",
		"--prompt", "Fix the bugs",
		"--json",
	}
	if len(got) != len(want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// The -seats value is exactly the "," join of the entries' Spec tokens.
	var specs []string
	for _, e := range seats {
		specs = append(specs, e.Spec)
	}
	if got[3] != strings.Join(specs, ",") {
		t.Errorf("-seats = %q, want exact Spec join %q", got[3], strings.Join(specs, ","))
	}
}

func TestBuildFanoutArgsDefaults(t *testing.T) {
	tmpl := Templates()[1] // security-audit (adversarial scaffold)
	got := BuildFanoutArgs(tmpl, nil, "", "", 0)
	// -root falls back to ".", -timeout to the fanout CLI default (300s), empty
	// prompt to the template scaffold.
	want := []string{
		"--task", "security",
		"--seats", "",
		"--root", ".",
		"--timeout", "300",
		"--tools", "read,search",
		"--prompt", tmpl.PromptScaffold,
		"--json",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if !strings.Contains(got[11], "adversarial") {
		t.Errorf("security scaffold lost its adversarial framing: %q", got[11])
	}
}

func TestFormatCommandQuotesSpacedArgs(t *testing.T) {
	got := FormatCommand([]string{"--prompt", "two words", "--task", "review"})
	want := `agy pro fanout --prompt "two words" --task review`
	if got != want {
		t.Errorf("FormatCommand = %q, want %q", got, want)
	}
}

// --- preset degrade (contract: fewer available than the preset needs ->
// "all available" + printed warning, never an error) ---

func TestPresetDegradeWarns(t *testing.T) {
	// 2-entry inventory: 1 native + 1 provider. p3 nominally seats 4.
	inv := buildInventory(t, []inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash-flex"}}}})
	if n := len(inv.Entries); n != 2 {
		t.Fatalf("fixture entries = %d, want 2", n)
	}

	var warn strings.Builder
	seats, err := ResolveSeats("p3", inv, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(p3) on a 2-entry inventory must degrade, not error: %v", err)
	}
	if len(seats) != 2 {
		t.Errorf("degraded preset returned %d seats, want all 2 available", len(seats))
	}
	if !strings.Contains(warn.String(), "degraded") {
		t.Errorf("missing degrade warning; warn output = %q", warn.String())
	}

	// Empty input = default preset p1 (nominal 4) also degrades with a warning.
	warn.Reset()
	seats, err = ResolveSeats("", inv, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(\"\") must not error: %v", err)
	}
	if len(seats) != 2 || !strings.Contains(warn.String(), "degraded") {
		t.Errorf("default preset degrade: seats=%d warn=%q", len(seats), warn.String())
	}
}

// --- k3 pool policy (contract F-C/DoD 3: never auto-picked; explicit typing
// rejected) ---

func TestPresetSkipsK3(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{
			{Name: "nw", Models: []providers.Model{
				{ID: "kimi-k3"},
				{ID: "glm-5.3-flash-flex"},
			}},
			{Name: "ds", Models: []providers.Model{{ID: "deepseek-chat"}}},
		})

	var k3Entry *inventory.Entry
	for i := range inv.Entries {
		if inv.Entries[i].ModelID == "kimi-k3" {
			k3Entry = &inv.Entries[i]
		}
	}
	if k3Entry == nil {
		t.Fatal("fixture must contain a kimi-k3 entry")
	}

	// Presets never auto-pick the k3 entry.
	for _, preset := range []string{"p1", "p2", "p3"} {
		var warn strings.Builder
		seats, err := ResolveSeats(preset, inv, &warn)
		if err != nil {
			t.Fatalf("ResolveSeats(%s): %v", preset, err)
		}
		for _, e := range seats {
			if e.ModelID == "kimi-k3" {
				t.Errorf("preset %s auto-picked k3 entry %s", preset, e.Spec)
			}
		}
	}
	// p2 needs 3 externals; only 2 are eligible (k3 skipped) -> warning path.
	var warn strings.Builder
	seats, err := ResolveSeats("p2", inv, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(p2): %v", err)
	}
	if len(seats) != 2 {
		t.Errorf("p2 returned %d seats, want 2 eligible externals", len(seats))
	}
	if !strings.Contains(warn.String(), "degraded") {
		t.Errorf("p2 k3-skip should degrade with a warning; got %q", warn.String())
	}

	// Explicit typing of the k3 entry number is REJECTED.
	_, err = ResolveSeats(strconv.Itoa(k3Entry.Number), inv, &warn)
	if err == nil {
		t.Fatal("explicit k3 pick must be rejected")
	}
	if !strings.Contains(err.Error(), "excluded (pool policy)") {
		t.Errorf("k3 rejection message should carry the pool-policy marker: %v", err)
	}
	// ... including inside a range.
	_, err = ResolveSeats("1-"+strconv.Itoa(k3Entry.Number), inv, &warn)
	if err == nil || !strings.Contains(err.Error(), "excluded (pool policy)") {
		t.Errorf("k3 pick inside a range must be rejected, got %v", err)
	}
}

func TestWarnK3SpecsExplicitPickWarns(t *testing.T) {
	specs, err := fanout.ParseSeatSpecs("native:gemini-3.8-flash-high,nw/kimi-k3")
	if err != nil {
		t.Fatal(err)
	}
	entries := EntriesFromSeatSpecs(specs)
	var warn strings.Builder
	WarnK3Specs(entries, &warn)
	if !strings.Contains(warn.String(), "excluded (pool policy)") {
		t.Errorf("explicit k3 spec must warn loudly: %q", warn.String())
	}
	if !strings.Contains(warn.String(), "explicit pick honored") {
		t.Errorf("explicit spec must stay honored (pool policy covers auto-seating): %q", warn.String())
	}
}

// k3 rows render `excluded (pool policy)` in the builder's numbered inventory.
func TestRenderSeatListMarksK3(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{
			{ID: "kimi-k3"},
			{ID: "glm-5.3-flash-flex"},
		}}})
	var buf strings.Builder
	RenderSeatList(&buf, inv)
	got := buf.String()
	if !strings.Contains(got, "excluded (pool policy)") {
		t.Errorf("k3 row must render the pool-policy marker:\n%s", got)
	}
	if strings.Count(got, "excluded (pool policy)") != 1 {
		t.Errorf("exactly one k3 row expected to carry the marker:\n%s", got)
	}
	if !strings.Contains(got, "glm-5.3-flash-flex") {
		t.Errorf("non-k3 rows must render normally:\n%s", got)
	}
}

// A8 (security): a range token was expanded BEFORE any bound check ran, so
// `1-2000000000` materialized two billion ints inside rejectExplicitK3 - a
// prompt-injected exec turning into an OOM kill before ParseSelection's 40-seat
// cap or the inventory bounds check could reject it. Expansion is now bounded,
// and the whole selection fails fast (the caller maps the error to exit 2).
func TestSelectionRangeExpansionIsBounded(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})

	// The over-wide range must be rejected by the helper itself, before it
	// allocates anything.
	if _, err := tokenNumbers("1-2000000000"); err == nil {
		t.Fatal("tokenNumbers must reject an over-wide range")
	} else if !strings.Contains(err.Error(), "too wide") {
		t.Errorf("tokenNumbers error = %v; want the too-wide rejection", err)
	}
	// The cap boundary itself is still accepted (4096 numbers wide).
	nums, err := tokenNumbers("1-4096")
	if err != nil {
		t.Fatalf("a range at the cap must be accepted: %v", err)
	}
	if len(nums) != 4096 {
		t.Errorf("1-4096 expanded to %d numbers; want 4096", len(nums))
	}
	if _, err := tokenNumbers("1-4097"); err == nil {
		t.Error("one past the cap must be rejected")
	}

	// End to end through ResolveSeats: fast failure, no OOM. The caller
	// (cmd/agy resolveFlagSeats) maps this error to exit 2.
	start := time.Now()
	_, err = ResolveSeats("1-2000000000", inv, io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ResolveSeats must reject the over-wide range")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ResolveSeats(1-2000000000) took %v; the range must not be materialized", elapsed)
	}
	// The reported error still names the real problem (out of range), so the
	// user gets an actionable message rather than an internal one.
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("ResolveSeats error = %v; want the out-of-range message", err)
	}

	// A normal range still resolves.
	seats, err := ResolveSeats("1-2", inv, io.Discard)
	if err != nil {
		t.Fatalf("ResolveSeats(1-2): %v", err)
	}
	if len(seats) != 2 {
		t.Errorf("1-2 resolved to %d seats; want 2", len(seats))
	}
}

// C4 (overflow): MaxInt64 range endpoints passed the A8 width check on an
// equal pair (`end-start` == 0) and then hung the expansion loop forever -
// `n++` past math.MaxInt64 wraps instead of exceeding `end`, so
// `9223372036854775807-9223372036854775807` appended ints without end. Both
// endpoints are now bounded by maxSeatNumber BEFORE the subtraction, so the
// pair is a fast error instead of an OOM/hang.
func TestSelectionRangeEndpointsAreBounded(t *testing.T) {
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}}}})

	const maxI64 = "9223372036854775807"

	// The helper itself rejects the MaxInt64 pair fast.
	start := time.Now()
	if _, err := tokenNumbers(maxI64 + "-" + maxI64); err == nil {
		t.Fatal("tokenNumbers must reject MaxInt64 range endpoints")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("tokenNumbers(MaxInt64-MaxInt64) took %v; the expansion loop must not run", elapsed)
	}

	// End to end through ResolveSeats: fast, clean error.
	start = time.Now()
	_, err := ResolveSeats(maxI64+"-"+maxI64, inv, io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ResolveSeats must reject the MaxInt64 range")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ResolveSeats(MaxInt64-MaxInt64) took %v; must fail fast", elapsed)
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("ResolveSeats error = %v; want the out-of-range message", err)
	}

	// A huge end with a small start is rejected too (no subtract-overflow).
	if _, err := tokenNumbers("1-" + maxI64); err == nil {
		t.Error("tokenNumbers must reject a MaxInt64 range end")
	}

	// expectedSeats no longer sums overflowed widths for such tokens.
	if got := expectedSeats(maxI64 + "-" + maxI64); got != 1 {
		t.Errorf("expectedSeats(MaxInt64-MaxInt64) = %d; want 1", got)
	}
	if got := expectedSeats("1-" + maxI64); got != 1 {
		t.Errorf("expectedSeats(1-MaxInt64) = %d; want 1", got)
	}

	// Benign ranges are unaffected.
	nums, err := tokenNumbers("1-4096")
	if err != nil || len(nums) != 4096 {
		t.Errorf("tokenNumbers(1-4096) = %v, %v; want 4096 numbers", nums, err)
	}
}

// --- swarm-state.json round-trip + corrupt-file fallback ---

func TestStoreRoundTripAndCorruptFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swarm-state.json")
	store := &FileStore{Path: path}

	// Missing file -> empty list, no error.
	presets, err := store.LoadPresets()
	if err != nil || len(presets) != 0 {
		t.Fatalf("missing file: presets=%v err=%v, want empty/nil", presets, err)
	}

	// Round-trip.
	want := []SavedPreset{
		{Name: "quad-mixed", Numbers: "1,3,19,23"},
		{Name: "natives", Numbers: "1-2"},
	}
	if err := store.SavePresets(want); err != nil {
		t.Fatalf("SavePresets: %v", err)
	}
	got, err := store.LoadPresets()
	if err != nil {
		t.Fatalf("LoadPresets after save: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("round-trip presets = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("preset[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Corrupt file -> empty list with no error (fanout LoadState precedent).
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadPresets()
	if err != nil || len(got) != 0 {
		t.Fatalf("corrupt file: presets=%v err=%v, want empty/nil (fallback)", got, err)
	}

	// The on-disk shape wraps the presets list.
	if err := store.SavePresets(want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Presets []SavedPreset `json:"presets"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("swarm-state.json shape: %v\n%s", err, raw)
	}
	if len(shape.Presets) != 2 {
		t.Errorf("shape.presets = %+v, want 2 entries", shape.Presets)
	}
}

// --- helpers ---

func buildInventory(t *testing.T, native []inventory.NativeModel, provs []providers.Provider) *inventory.Inventory {
	t.Helper()
	reg := &providers.Registry{Providers: provs}
	inv, err := inventory.Load(context.Background(), reg, staticNative(native))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

type staticNative []inventory.NativeModel

func (s staticNative) Fetch(context.Context) ([]inventory.NativeModel, error) {
	return s, nil
}
