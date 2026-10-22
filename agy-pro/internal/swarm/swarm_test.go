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

// --- exclusion list (user decision D2 = b: auto-seating and failover SKIP an
// excluded row, an explicit pick - numbered OR spec - is HONORED with the
// shared warning, and NOTHING is ever rejected) ---

// excludedFixture is the D2 fixture: 1 eligible native row, 1 excluded provider
// row (the built-in default entry: the k3 id token), plus 2 eligible provider
// rows in distinct families so presets have something to rotate over.
func excludedFixture(t *testing.T) (*inventory.Inventory, inventory.Entry) {
	t.Helper()
	inv := buildInventory(t,
		[]inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}},
		[]providers.Provider{
			{Name: "nw", Models: []providers.Model{
				{ID: "kimi-k3"},
				{ID: "glm-5.3-flash-flex"},
			}},
			{Name: "ds", Models: []providers.Model{{ID: "deepseek-chat"}}},
		})
	for _, e := range inv.Entries {
		if e.ModelID == "kimi-k3" {
			if !e.Excluded {
				t.Fatalf("fixture broken: %s must carry Entry.Excluded", e.Spec)
			}
			return inv, e
		}
	}
	t.Fatal("fixture must contain a kimi-k3 entry")
	return nil, inventory.Entry{}
}

func TestPresetSkipsExcludedRows(t *testing.T) {
	inv, excluded := excludedFixture(t)

	// Presets never auto-pick an excluded row.
	for _, preset := range []string{"p1", "p2", "p3"} {
		var warn strings.Builder
		seats, err := ResolveSeats(preset, inv, &warn)
		if err != nil {
			t.Fatalf("ResolveSeats(%s): %v", preset, err)
		}
		for _, e := range seats {
			if e.Excluded {
				t.Errorf("preset %s auto-picked the excluded row %s", preset, e.Spec)
			}
		}
	}
	// p2 needs 3 externals; only 2 are eligible (the excluded row is skipped)
	// -> the degrade warning fires instead of an error.
	var warn strings.Builder
	seats, err := ResolveSeats("p2", inv, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(p2): %v", err)
	}
	if len(seats) != 2 {
		t.Errorf("p2 returned %d seats, want the 2 eligible externals", len(seats))
	}
	if !strings.Contains(warn.String(), "degraded") {
		t.Errorf("p2 exclusion skip should degrade with a warning; got %q", warn.String())
	}
	// The auto-pick arm never prints the explicit-pick sentence: a preset is not
	// an explicit pick.
	if strings.Contains(warn.String(), "explicit pick honored") {
		t.Errorf("preset skip must not claim an explicit pick: %q", warn.String())
	}
	_ = excluded
}

// TestPurePresetLineStillReportsSkippedRow is the non-vacuity control for the
// S7-C8 fix: excluding the explicit-number set from the preset-skip report must
// not silence a PURE preset line, whose only reason for dropping the row is the
// auto-pick skip.
func TestPurePresetLineStillReportsSkippedRow(t *testing.T) {
	inv, excluded := excludedFixture(t)

	var warn strings.Builder
	if _, err := ResolveSeats("p2", inv, &warn); err != nil {
		t.Fatalf("ResolveSeats(p2): %v", err)
	}
	want := "skipped seat " + strconv.Itoa(excluded.Number) + " (" + excluded.Spec + "): " + providers.ExcludedMarker()
	if !strings.Contains(warn.String(), want) {
		t.Errorf("a pure preset line must still report the skipped row %q: %q", want, warn.String())
	}
	if strings.Contains(warn.String(), "explicit pick honored") {
		t.Errorf("a pure preset line names no explicit pick: %q", warn.String())
	}
}

// TestMixedSelectionReportsExcludedRowOnce closes S7-C8: a line that mixes a
// preset with an explicit number naming the excluded row reported that kept row
// TWICE (the honored-pick warning and the preset-skip warning). The explicit
// number is a deliberate pick, so the honored-pick sentence is the right reason
// and the preset-skip report must not repeat it. The row is still seated, and no
// OTHER row loses its report.
func TestMixedSelectionReportsExcludedRowOnce(t *testing.T) {
	inv, excluded := excludedFixture(t)
	line := "p2," + strconv.Itoa(excluded.Number)

	var warn strings.Builder
	seats, err := ResolveSeats(line, inv, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(%q): %v", line, err)
	}
	seated := false
	for _, e := range seats {
		if e.Spec == excluded.Spec {
			seated = true
		}
	}
	if !seated {
		t.Fatalf("the explicitly named excluded row must stay seated: %+v", seats)
	}

	out := warn.String()
	if !strings.Contains(out, sharedPickWarning(excluded.Number, excluded.Spec)) {
		t.Errorf("the kept row must be reported with the honored-pick sentence: %q", out)
	}
	if got := strings.Count(out, "skipped seat "+strconv.Itoa(excluded.Number)+" ("); got != 0 {
		t.Errorf("the kept row must not also be reported as skipped (got %d): %q", got, out)
	}
	// One row, one report: the fixture holds exactly one excluded row.
	if got := strings.Count(out, providers.ExcludedMarker()); got != 1 {
		t.Errorf("the excluded row must be reported exactly once; got %d reports: %q", got, out)
	}
}

// sharedPickWarning is the one sentence the numbered-pick and the spec warnings
// both print (D2 = b), composed here from the marker authority so the test
// pins the wording the code produces rather than a copy that can drift.
func sharedPickWarning(seat int, spec string) string {
	return "warning: seat " + strconv.Itoa(seat) + " (" + spec + ") is " + providers.ExcludedMarker() +
		"; explicit pick honored (the exclusion list covers auto-seating and failover only)"
}

// TestExplicitNumberedPickOfExcludedRowIsHonored is the deliberate behavior
// change: the old code REJECTED a numbered pick of a k3 row (exit 2). D2 = b
// honors it WITH the shared warning, because the exclusion list governs
// auto-seating and failover only.
func TestExplicitNumberedPickOfExcludedRowIsHonored(t *testing.T) {
	inv, excluded := excludedFixture(t)

	var warn strings.Builder
	seats, err := ResolveSeats(strconv.Itoa(excluded.Number), inv, &warn)
	if err != nil {
		t.Fatalf("explicit numbered pick of an excluded row must be honored, not rejected: %v", err)
	}
	if len(seats) != 1 || seats[0].Spec != excluded.Spec {
		t.Fatalf("numbered pick seated %+v, want exactly the named row %s", seats, excluded.Spec)
	}
	if want := sharedPickWarning(excluded.Number, excluded.Spec); !strings.Contains(warn.String(), want) {
		t.Errorf("numbered pick warning = %q; want the shared sentence %q", warn.String(), want)
	}

	// A range containing the excluded row is honored too: the row is seated and
	// one warning names it.
	warn.Reset()
	rangeTok := "1-" + strconv.Itoa(excluded.Number)
	seats, err = ResolveSeats(rangeTok, inv, &warn)
	if err != nil {
		t.Fatalf("range %q naming an excluded row must be honored: %v", rangeTok, err)
	}
	if len(seats) != excluded.Number {
		t.Errorf("range %q seated %d rows, want %d", rangeTok, len(seats), excluded.Number)
	}
	found := false
	for _, e := range seats {
		if e.Spec == excluded.Spec {
			found = true
		}
	}
	if !found {
		t.Errorf("range %q dropped the excluded row %s: %+v", rangeTok, excluded.Spec, seats)
	}
	if !strings.Contains(warn.String(), "explicit pick honored") {
		t.Errorf("range pick of an excluded row must warn: %q", warn.String())
	}

	// Non-vacuity, second row: a numbered pick of an ELIGIBLE row stays silent,
	// so the warning is driven by Entry.Excluded and not by "any numbered pick".
	var quiet strings.Builder
	eligible := inv.Entries[0]
	if eligible.Excluded {
		t.Fatalf("fixture broken: row 1 (%s) must be eligible", eligible.Spec)
	}
	if _, err := ResolveSeats(strconv.Itoa(eligible.Number), inv, &quiet); err != nil {
		t.Fatalf("ResolveSeats(%d): %v", eligible.Number, err)
	}
	if quiet.Len() != 0 {
		t.Errorf("an eligible numbered pick must not warn: %q", quiet.String())
	}
}

// TestWarnExcludedSpecsExplicitPickWarns pins the spec path: EntriesFromSeatSpecs
// stamps the SAME verdict the inventory computes at Load, and the warning is the
// shared sentence.
func TestWarnExcludedSpecsExplicitPickWarns(t *testing.T) {
	// P4-4b: the spec path now reads the registry for a model's DECLARED family,
	// so this test must not depend on the live ~/.agy-pro/providers.json. Its
	// arms are all id-token/user-list arms, which a temp (empty) home leaves
	// exactly as they were.
	t.Setenv("AGY_PRO_HOME", t.TempDir())
	specs, err := fanout.ParseSeatSpecs("native:gemini-3.8-flash-high,nw/kimi-k3")
	if err != nil {
		t.Fatal(err)
	}
	entries := EntriesFromSeatSpecs(specs, providers.ExclusionPolicy{})
	// Non-vacuity on two rows: the native row is eligible, the k3 spec is not.
	if entries[0].Excluded {
		t.Errorf("%s must not be excluded: %+v", entries[0].Spec, entries[0])
	}
	if !entries[1].Excluded {
		t.Fatalf("%s must carry Entry.Excluded: %+v", entries[1].Spec, entries[1])
	}

	var warn strings.Builder
	WarnExcludedSpecs(entries, &warn)
	if want := sharedPickWarning(2, "nw/kimi-k3"); !strings.Contains(warn.String(), want) {
		t.Errorf("spec warning = %q; want the shared sentence %q", warn.String(), want)
	}
	if got := strings.Count(warn.String(), "\n"); got != 1 {
		t.Errorf("exactly one warning expected (the native row is eligible); got %d lines: %q", got, warn.String())
	}

	// A user-owned list REPLACES the built-in default (D1 = a): naming the
	// provider excludes its models, and the k3 default no longer runs.
	userEntries := EntriesFromSeatSpecs(specs, providers.ExclusionPolicy{User: []string{"nw"}})
	if !userEntries[1].Excluded {
		t.Errorf("a user entry naming the provider must exclude %s: %+v", userEntries[1].Spec, userEntries[1])
	}
	if userEntries[0].Excluded {
		t.Errorf("a user entry naming \"nw\" must not touch the native row: %+v", userEntries[0])
	}
	// An EMPTY user list excludes nothing (including the k3 default entry).
	for _, e := range EntriesFromSeatSpecs(specs, providers.ExclusionPolicy{User: []string{}}) {
		if e.Excluded {
			t.Errorf("an empty user list must exclude nothing: %+v", e)
		}
	}

	// A bare provider spec is read through the registry's own spec grammar, so
	// the model half matches even without a provider qualifier.
	bare, err := fanout.ParseSeatSpecs("kimi-k3")
	if err != nil {
		t.Fatal(err)
	}
	if got := EntriesFromSeatSpecs(bare, providers.ExclusionPolicy{}); !got[0].Excluded {
		t.Errorf("a bare excluded model id must be flagged: %+v", got[0])
	}
}

// TestApplyExclusionPolicyArms proves the auto-pick arm directly. The inventory's
// own preset filter normally drops an excluded row before the swarm sees it, so
// this arm cannot be reached through ResolveSeats alone; keeping it tested is
// what makes "auto-seating skips" true at this layer too.
func TestApplyExclusionPolicyArms(t *testing.T) {
	rows := []inventory.Entry{
		{Number: 1, Spec: "native:flash", ModelID: "flash"},
		{Number: 2, Spec: "nw/auto-dropped", ModelID: "auto-dropped", Excluded: true},
		{Number: 3, Spec: "nw/explicit-kept", ModelID: "explicit-kept", Excluded: true},
	}
	var warn strings.Builder
	kept := applyExclusionPolicy(rows, map[int]bool{1: true, 3: true}, &warn)
	if len(kept) != 2 || kept[0].Number != 1 || kept[1].Number != 3 {
		t.Fatalf("kept = %+v, want rows 1 and 3 (row 2 is an auto-pick)", kept)
	}
	out := warn.String()
	if !strings.Contains(out, "skipped seat 2 (nw/auto-dropped): "+providers.ExcludedMarker()) {
		t.Errorf("the auto-pick arm must drop row 2 with the marker: %q", out)
	}
	if !strings.Contains(out, sharedPickWarning(3, "nw/explicit-kept")) {
		t.Errorf("the explicit arm must print the shared sentence: %q", out)
	}
	if got := strings.Count(out, "warning:"); got != 1 {
		t.Errorf("exactly one shared warning expected (row 1 is eligible); got %d: %q", got, out)
	}
}

// TestHonoredExcludedSeatKeepsFanoutVector pins that an honored excluded seat
// leaves the dry-run vector untouched: the exclusion list never adds, drops or
// reorders an argument. 13 elements = the 7 pinned flags (6 flag/value pairs
// plus --json).
func TestHonoredExcludedSeatKeepsFanoutVector(t *testing.T) {
	inv, excluded := excludedFixture(t)
	seats, err := ResolveSeats(strconv.Itoa(excluded.Number), inv, nil)
	if err != nil {
		t.Fatalf("ResolveSeats(%d): %v", excluded.Number, err)
	}
	got := BuildFanoutArgs(Templates()[0], seats, "/ws", "p", 300)
	wantFlags := []string{"--task", "--seats", "--root", "--timeout", "--tools", "--prompt", "--json"}
	if len(got) != 2*len(wantFlags)-1 {
		t.Fatalf("args = %q; want %d elements (7 flags, 6 with values)", got, 2*len(wantFlags)-1)
	}
	for i, f := range wantFlags {
		if got[2*i] != f {
			t.Errorf("args[%d] = %q, want %q (full vector %q)", 2*i, got[2*i], f, got)
		}
	}
	if got[3] != excluded.Spec {
		t.Errorf("--seats = %q, want the honored excluded spec %q", got[3], excluded.Spec)
	}
}

// TestUserExclusionListGovernsPresets pins the user-owned list end to end: a
// defaults.excludeModels entry keeps its row out of preset expansion, and an
// EMPTY user list excludes nothing at all (nil = built-in default, non-nil
// empty = user owns an empty list).
func TestUserExclusionListGovernsPresets(t *testing.T) {
	provs := []providers.Provider{{Name: "nw", Models: []providers.Model{
		{ID: "glm-5.3-flash-flex"},
		{ID: "m-two"},
	}}}
	native := []inventory.NativeModel{{ID: "gemini-3.8-flash-high", DisplayName: "Flash"}}

	load := func(t *testing.T, list []string) *inventory.Inventory {
		t.Helper()
		reg := &providers.Registry{Providers: provs, Defaults: providers.Defaults{ExcludeModels: list}}
		inv, err := inventory.Load(context.Background(), reg, staticNative(native))
		if err != nil {
			t.Fatalf("inventory.Load: %v", err)
		}
		return inv
	}

	userList := load(t, []string{"glm-5.3-flash-flex"})
	if !userList.Entries[1].Excluded || userList.Entries[2].Excluded {
		t.Fatalf("user list must exclude row 2 only: %+v", userList.Entries)
	}
	var warn strings.Builder
	seats, err := ResolveSeats("p2", userList, &warn)
	if err != nil {
		t.Fatalf("ResolveSeats(p2): %v", err)
	}
	if len(seats) != 1 || seats[0].ModelID != "m-two" {
		t.Fatalf("p2 seated %+v, want only the row the user list left eligible (m-two)", seats)
	}
	if !strings.Contains(warn.String(), "degraded") {
		t.Errorf("a preset short of its frozen size must warn: %q", warn.String())
	}

	emptyList := load(t, []string{})
	for _, e := range emptyList.Entries {
		if e.Excluded {
			t.Fatalf("an empty user list must exclude nothing: %+v", e)
		}
	}
	seats, err = ResolveSeats("p2", emptyList, nil)
	if err != nil {
		t.Fatalf("ResolveSeats(p2) with an empty user list: %v", err)
	}
	if len(seats) != 2 {
		t.Fatalf("p2 seated %d rows under an empty user list, want both externals: %+v", len(seats), seats)
	}
}

// Excluded rows render the one marker providers.ExcludedMarker owns in the
// builder's numbered inventory.
func TestRenderSeatListMarksExcludedRows(t *testing.T) {
	inv, _ := excludedFixture(t)
	var buf strings.Builder
	RenderSeatList(&buf, inv)
	got := buf.String()
	if !strings.Contains(got, providers.ExcludedMarker()) {
		t.Errorf("the excluded row must render %q:\n%s", providers.ExcludedMarker(), got)
	}
	if strings.Count(got, providers.ExcludedMarker()) != 1 {
		t.Errorf("exactly one excluded row expected to carry the marker:\n%s", got)
	}
	if strings.Contains(got, "pool policy") {
		t.Errorf("the retired pool-policy wording must be gone:\n%s", got)
	}
	if !strings.Contains(got, "glm-5.3-flash-flex") {
		t.Errorf("eligible rows must render normally:\n%s", got)
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
