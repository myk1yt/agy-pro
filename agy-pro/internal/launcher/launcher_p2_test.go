package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agypro/internal/hybrid"
	"agypro/internal/inventory"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

// ---------------------------------------------------------------------------
// P2 (subagent profile) launcher front door (unit U9b)
//
// The end-to-end tests drive the REAL chain: Decide -> RouteHybridP2 ->
// runHybridP2 -> hybrid.LaunchP2, with the REAL wirecheck gate and a REAL
// per-spawn mesh, against a canary-carrying stub agy. Nothing interactive is
// started: stdinIsTTY and the console reader are seams, the child is a stub, and
// no provider is dialed.
// ---------------------------------------------------------------------------

// p2LauncherRegistryJSON mirrors pickerFixture's providers so a picker row and
// the sandbox registry agree on the model ids. gemini-3.8-flash-high is a
// reserved static id: p2Externals must drop it, and a native pick of it must
// reach the child as the bare static id (the static-id policy).
const p2LauncherRegistryJSON = `{"providers":[
  {"name":"google","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"GOOGLE_TEST_KEY","models":[
    {"id":"gemini-2.5-flash"},{"id":"gemini-3.8-flash-high"}]},
  {"name":"nw","baseURL":"http://127.0.0.1:9/v1","apiKeyEnv":"NW_TEST_KEY","models":[
    {"id":"glm-5.3-flash-flex"},{"id":"glm-5.3-flex"}]}
]}`

// buildP2CanaryStubAgy compiles a stub agy that carries every P2 canary literal
// (so the REAL wirecheck gate reads GO for P2), answers --version with the
// pinned version, and appends its argv as one JSON line to AGY_PRO_P2_ARGSDUMP.
func buildP2CanaryStubAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const stubSrc = `package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// canaryBlob carries every wirecheck P2 canary literal as a substring so the
// real scan finds them all. It is referenced by a reachable branch: a blank
// assignment is dead-code-eliminated and the literals would not survive.
const canaryBlob = "gemini-api:blade:evergreen:beyond-api:google-genai-sdk/GOOGLE_GEMINI_BASE_URLGEMINI_API_KEYmodelProvider"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Fprintln(os.Stdout, "agy 1.3.5")
			return
		}
	}
	if os.Getenv("AGY_PRO_P2_PRINT_CANARIES") != "" {
		fmt.Fprintln(os.Stdout, canaryBlob)
		return
	}
	if p := os.Getenv("AGY_PRO_P2_ARGSDUMP"); p != "" {
		if data, err := json.Marshal(os.Args); err == nil {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = f.Write(append(data, '\n'))
				_ = f.Close()
			}
		}
	}
	if p := os.Getenv("AGY_PRO_P2_ENVDUMP"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			for _, e := range os.Environ() {
				_, _ = f.WriteString(e + "\n")
			}
			_ = f.Close()
		}
	}
	fmt.Fprintln(os.Stdout, "STUB_OK")
}
`
	if err := os.WriteFile(src, []byte(stubSrc), 0o600); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	out := filepath.Join(dir, "agy")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	build := exec.Command("go", "build", "-o", out, src)
	build.Env = append(os.Environ(), "GO111MODULE=off")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, b)
	}
	return out
}

// p2StubArgv reads the appended argv dump and returns the first attempt's argv.
func p2StubArgv(t *testing.T, dump string) []string {
	t.Helper()
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read argv dump: %v", err)
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if line == "" {
		t.Fatalf("the stub child never ran (empty argv dump)")
	}
	var args []string
	if err := json.Unmarshal([]byte(line), &args); err != nil {
		t.Fatalf("parse argv dump %q: %v", line, err)
	}
	return args
}

// stubP2PickerInventory points the picker inventory seam at the combined
// fixture and returns it, so the P2 picker tests never resolve the real agy.
func stubP2PickerInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	inv := pickerFixture()
	prev := pickerInventory
	pickerInventory = func(*providers.Registry) *inventory.Inventory { return inv }
	t.Cleanup(func() { pickerInventory = prev })
	return inv
}

// p2FilteredNumber returns the picker number a row has AFTER the P2 filter, so a
// console stub selects the row the user would actually see. The filter drops
// rows and renumbers, so a PRE-filter number is never selectable: this is the
// only number source the P2 picker tests may use (R5BF2-5 - the previous
// pre-filter helper was deleted rather than left as a trap).
func p2FilteredNumber(t *testing.T, reg *providers.Registry, kind, modelID string) string {
	t.Helper()
	for _, e := range p2PickerInventory(reg).Entries {
		if e.Kind == kind && e.ModelID == modelID {
			return fmt.Sprintf("%d", e.Number)
		}
	}
	t.Fatalf("no %s entry %q survives the P2 picker filter", kind, modelID)
	return ""
}

// stubP2TTYAndConsole makes the launcher believe stdin is a TTY and answers the
// console read with line.
func stubP2TTYAndConsole(t *testing.T, line string) {
	t.Helper()
	prevTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = prevTTY })

	prevConsole := readConsoleLineFn
	readConsoleLineFn = func(*os.File, time.Duration) (string, consoleRead) {
		return line, consoleChoice
	}
	t.Cleanup(func() { readConsoleLineFn = prevConsole })
}

// failIfCalled installs seams that fail the test when the P2 picker runs, for
// the non-bare invocation shapes where it must not.
func failIfPickerRuns(t *testing.T) {
	t.Helper()
	stubP2BareNonInteractive(t)

	prevConsole := readConsoleLineFn
	readConsoleLineFn = func(*os.File, time.Duration) (string, consoleRead) {
		t.Error("the P2 picker must not read the console for a non-bare invocation")
		return "", consoleUnavailable
	}
	t.Cleanup(func() { readConsoleLineFn = prevConsole })
}

// stubP2BareNonInteractive makes a BARE invocation hermetic: stdin is not a TTY
// and the picker inventory seam fails the test if it is touched. A bare hybrid
// test that leaves these seams at their defaults can, on a developer's console,
// run the real picker - which resolves the real agy, spawns `agy models`, and
// can block on the console read for up to pickerTimeout (60s). R5B-FIX-5.
func stubP2BareNonInteractive(t *testing.T) {
	t.Helper()
	prevTTY := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = prevTTY })

	prevInv := pickerInventory
	pickerInventory = func(*providers.Registry) *inventory.Inventory {
		t.Error("the P2 picker must not run on a non-interactive invocation")
		return &inventory.Inventory{}
	}
	t.Cleanup(func() { pickerInventory = prevInv })
}

// captureStdout replaces os.Stdout with a pipe for the duration of fn and
// returns everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	drained := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		_ = r.Close()
		drained <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-drained
}

// captureStderr is captureStdout for os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	drained := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		_ = r.Close()
		drained <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-drained
}

// ---------------------------------------------------------------------------
// pure units
// ---------------------------------------------------------------------------

// TestP2ShowsPicker_BareTTYOnly pins the picker's gate: a bare session on a TTY
// and nothing else. A -p/--print one-shot, an explicit --model/-m, a subcommand,
// any argument at all, and a pipe/CI run must never be interrupted by a menu.
func TestP2ShowsPicker_BareTTYOnly(t *testing.T) {
	prev := stdinIsTTY
	t.Cleanup(func() { stdinIsTTY = prev })
	for _, tc := range []struct {
		name string
		args []string
		tty  bool
		want bool
	}{
		{"bare interactive session", nil, true, true},
		{"bare piped session", nil, false, false},
		{"print one-shot", []string{"-p", "hi"}, true, false},
		{"print long form", []string{"--print", "hi"}, true, false},
		{"explicit model pin", []string{"--model", "ext-nw"}, true, false},
		{"short model pin", []string{"-m", "ext-nw"}, true, false},
		{"models subcommand", []string{"models"}, true, false},
		{"agents subcommand", []string{"agents"}, true, false},
		{"version flag", []string{"--version"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdinIsTTY = func() bool { return tc.tty }
			if got := p2ShowsPicker(tc.args); got != tc.want {
				t.Errorf("p2ShowsPicker(%v, tty=%v) = %v; want %v", tc.args, tc.tty, got, tc.want)
			}
		})
	}
}

// TestP2PinArgs_UniformForNativeAndExternal pins the pin's shape: Entry.ModelID
// for both provenances, appended to the caller's args, and nothing appended on
// an abort. The blade: rewrite happens inside hybrid.LaunchP2 (p2ModelKey), not
// here.
func TestP2PinArgs_UniformForNativeAndExternal(t *testing.T) {
	external := ModelPick{Entry: inventory.Entry{Kind: "provider", ModelID: "glm-5.3-flash-flex"}, Picked: true}
	if got := p2PinArgs(nil, external); len(got) != 2 || got[0] != "--model" || got[1] != "glm-5.3-flash-flex" {
		t.Errorf("external pick args = %v; want [--model glm-5.3-flash-flex]", got)
	}
	native := ModelPick{Entry: inventory.Entry{Kind: "native", ModelID: "gemini-3.8-flash-high"}, Picked: true}
	if got := p2PinArgs(nil, native); len(got) != 2 || got[1] != "gemini-3.8-flash-high" {
		t.Errorf("native pick args = %v; want [--model gemini-3.8-flash-high]", got)
	}
	if got := p2PinArgs([]string{"-p", "hi"}, external); strings.Join(got, " ") != "-p hi --model glm-5.3-flash-flex" {
		t.Errorf("pinned args = %v; want the caller's args preserved", got)
	}
	if got := p2PinArgs([]string{"-p", "hi"}, ModelPick{}); strings.Join(got, " ") != "-p hi" {
		t.Errorf("abort args = %v; want the caller's args untouched (no --model)", got)
	}
	// The caller's slice is never mutated in place.
	base := []string{"-p", "hi"}
	_ = p2PinArgs(base, external)
	if strings.Join(base, " ") != "-p hi" {
		t.Errorf("the caller's args slice was mutated: %v", base)
	}
}

// TestP2CostPreamble_StatesHonestCosts pins the three P2 consequences the
// picker rows cannot show, plus the profile's own cost line.
func TestP2CostPreamble_StatesHonestCosts(t *testing.T) {
	var out strings.Builder
	printP2CostPreamble(&out)
	got := out.String()
	for _, want := range []string{
		hybrid.ProfileP2.Description(),
		"externals are selected HERE, at launch",
		"/model lists only its static Gemini defaults",
		"subscription Gemini is OFF in this session",
		"only agy's static Gemini ids are offered as native choices",
		"static-id policy",
		"never a silent substitution",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the P2 cost preamble is missing %q:\n%s", want, got)
		}
	}
}

// TestP2PickerInventory_FiltersUnservableRows pins R5B-FIX-3 on the launcher
// side: the picker the P2 arm renders offers only the rows a P2 session can
// serve, renumbered 1..N so a typed number still seats the right row.
//
// The fixture's combined inventory has 11 native rows (10 non-static + the
// static gemini-3.8-flash-high) and 4 provider rows, including a reserved-id
// collision on gemini-3.8-flash-high.
func TestP2PickerInventory_FiltersUnservableRows(t *testing.T) {
	// The registry the P2 session loads decides which provider rows are
	// keyable; it matches pickerFixture's providers so the ids line up.
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{{ID: "gemini-2.5-flash"}, {ID: "gemini-3.8-flash-high"}}},
		{Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash-flex"}, {ID: "glm-5.3-flex"}}},
	}}
	stubP2PickerInventory(t)
	inv := p2PickerInventory(reg)

	var native, provider []inventory.Entry
	for _, e := range inv.Entries {
		switch e.Kind {
		case "native":
			native = append(native, e)
		case "provider":
			provider = append(provider, e)
		}
	}
	// Only the static Gemini id survives from the native group.
	if len(native) != 1 || native[0].ModelID != "gemini-3.8-flash-high" {
		t.Errorf("native rows = %+v; want exactly [gemini-3.8-flash-high] (the only static id in the fixture)", native)
	}
	// The reserved-id collision row is dropped (p2Externals drops it too), the
	// other three provider rows are kept.
	if len(provider) != 3 {
		t.Fatalf("provider rows = %d (%+v); want 3", len(provider), provider)
	}
	for _, e := range provider {
		if e.ModelID == "gemini-3.8-flash-high" {
			t.Errorf("the reserved-id row %+v must not be offered", e)
		}
	}
	// Renumbering is contiguous from 1: ParseSelection indexes the slice.
	for i, e := range inv.Entries {
		if e.Number != i+1 {
			t.Fatalf("entry %d has Number %d; the filtered inventory must be renumbered 1..N", i, e.Number)
		}
	}
	if len(inv.Entries) == 0 {
		t.Fatal("fixture broken: the filtered inventory is empty")
	}
	// A typed number must still resolve to the rendered row.
	last := inv.Entries[len(inv.Entries)-1]
	pick := resolvePickerLine(fmt.Sprintf("%d", last.Number), inv, &strings.Builder{})
	if !pick.Picked || pick.Entry.ModelID != last.ModelID {
		t.Errorf("selecting %d = %+v; want entry %q", last.Number, pick, last.ModelID)
	}
}

// TestP2PickerInventory_CrossProviderDuplicateKeepsTheFirstProvider pins the
// billing-provenance half of R5B-FIX-3: when two providers carry the same id,
// only the first provider's row is offered, because p2Externals keys that
// provider's entry and the pin would resolve to it either way.
func TestP2PickerInventory_CrossProviderDuplicateKeepsTheFirstProvider(t *testing.T) {
	dupReg := &providers.Registry{Providers: []providers.Provider{
		{Name: "provfirst", Models: []providers.Model{{ID: "dup-id"}}},
		{Name: "provsecond", Models: []providers.Model{{ID: "dup-id"}}},
	}}
	// A combined inventory whose provider rows follow registry order.
	prev := pickerInventory
	pickerInventory = func(*providers.Registry) *inventory.Inventory {
		return &inventory.Inventory{Entries: []inventory.Entry{
			{Kind: "provider", Group: "provfirst", ModelID: "dup-id", Number: 1},
			{Kind: "provider", Group: "provsecond", ModelID: "dup-id", Number: 2},
		}}
	}
	t.Cleanup(func() { pickerInventory = prev })

	got := p2PickerInventory(dupReg)
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %+v; want exactly 1 (the first provider's row)", got.Entries)
	}
	if got.Entries[0].Group != "provfirst" {
		t.Errorf("offered row group = %q; want %q (the provider that owns the customModels key)", got.Entries[0].Group, "provfirst")
	}
	if got.Entries[0].Number != 1 {
		t.Errorf("offered row number = %d; want 1 (renumbered after filtering)", got.Entries[0].Number)
	}
}

// TestP2PickerInventory_NativeOnlyRegistryKeepsStaticRows is the degenerate
// shape: no provider rows at all. The static native rows still have to be
// selectable, and the picker must not dead-end.
func TestP2PickerInventory_NativeOnlyRegistryKeepsStaticRows(t *testing.T) {
	stubP2PickerInventory(t)
	inv := p2PickerInventory(nil)
	if len(inv.Entries) == 0 {
		t.Fatal("a nil registry must still offer the static native rows")
	}
	for _, e := range inv.Entries {
		if e.Kind != "native" {
			t.Errorf("unexpected provider row with a nil registry: %+v", e)
		}
	}
}

// TestP2PickerInventory_EmptyInventoryIsSafe pins the nil/empty seam: a failed
// inventory fetch must not panic, and the picker cancels (which the abort notice
// then explains).
func TestP2PickerInventory_EmptyInventoryIsSafe(t *testing.T) {
	prev := pickerInventory
	pickerInventory = func(*providers.Registry) *inventory.Inventory { return nil }
	t.Cleanup(func() { pickerInventory = prev })

	inv := p2PickerInventory(&providers.Registry{})
	if inv == nil {
		t.Fatal("a nil inventory must degrade to an empty one, not nil")
	}
	if len(inv.Entries) != 0 {
		t.Errorf("entries = %+v; want none", inv.Entries)
	}
}

// TestP2AbortNotice_NamesTheStaticIDPolicy pins the abort line: it states that
// the session runs agy's default static id and that the mesh answers it under
// the static-id policy.
func TestP2AbortNotice_NamesTheStaticIDPolicy(t *testing.T) {
	var errw strings.Builder
	printP2AbortNotice(&errw)
	got := errw.String()
	for _, want := range []string{
		"no model selected",
		"default static id",
		"static-id policy",
		"never a substitution",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the P2 abort notice is missing %q:\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// end to end: ExecutePassthrough -> RouteHybridP2 -> hybrid.LaunchP2
// ---------------------------------------------------------------------------

// TestExecutePassthrough_HybridP2BareTTYPickerPinsExternal drives the whole
// chain for a bare interactive session: the picker runs, an EXTERNAL pick is
// pinned as --model <registry id>, and hybrid.LaunchP2 rewrites it into the
// blade: map key on the way to the child.
func TestExecutePassthrough_HybridP2BareTTYPickerPinsExternal(t *testing.T) {
	sandboxHybridP1(t, p2LauncherRegistryJSON)
	stub := buildP2CanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	reg := p2SandboxRegistry(t)
	stubP2PickerInventory(t)
	// The number the FILTERED picker shows, not the raw fixture's.
	stubP2TTYAndConsole(t, p2FilteredNumber(t, reg, "provider", "glm-5.3-flash-flex"))
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("AGY_PRO_P2_ARGSDUMP", dump)

	var code int
	var err error
	transcript := captureStdout(t, func() {
		code, err = ExecutePassthrough(stub, nil)
	})
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	if !strings.Contains(transcript, "P2 (subagent profile)") {
		t.Errorf("the P2 cost preamble must print before the picker:\n%s", transcript)
	}
	argv := p2StubArgv(t, dump)
	pin := argValueLauncher(argv, "--model")
	if !strings.HasPrefix(pin, "blade:127.0.0.1:") || !strings.HasSuffix(pin, "/models/glm-5.3-flash-flex") {
		t.Errorf("--model = %q; want the blade: map key for the external pick", pin)
	}
	if argValueLauncher(argv, "--app_data_dir") == "" {
		t.Errorf("the P2 child must receive the per-session --app_data_dir: %v", argv)
	}
}

// p2SandboxRegistry loads the registry the sandbox test wrote, so the picker
// filter can be driven with exactly the ids the P2 session will see.
func p2SandboxRegistry(t *testing.T) *providers.Registry {
	t.Helper()
	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load the sandbox registry: %v", err)
	}
	return reg
}

// TestExecutePassthrough_HybridP2BareTTYPickerPinsNative is the native
// provenance: the pin is the bare static id, so agy resolves it from its own
// static catalog and the mesh's static-id policy decides whether it is
// servable (no blade: rewrite for a static id).
func TestExecutePassthrough_HybridP2BareTTYPickerPinsNative(t *testing.T) {
	sandboxHybridP1(t, p2LauncherRegistryJSON)
	stub := buildP2CanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	reg := p2SandboxRegistry(t)
	stubP2PickerInventory(t)
	stubP2TTYAndConsole(t, p2FilteredNumber(t, reg, "native", "gemini-3.8-flash-high"))
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("AGY_PRO_P2_ARGSDUMP", dump)

	code, err := ExecutePassthrough(stub, nil)
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	argv := p2StubArgv(t, dump)
	if got := argValueLauncher(argv, "--model"); got != "gemini-3.8-flash-high" {
		t.Errorf("--model = %q; want the native static id verbatim (the static-id policy answers it)", got)
	}
}

// TestExecutePassthrough_HybridP2BareTTYAbortRunsDefaultStaticID proves an
// aborted pick sends NO --model and prints the clear line that names what the
// session will run on.
func TestExecutePassthrough_HybridP2BareTTYAbortRunsDefaultStaticID(t *testing.T) {
	sandboxHybridP1(t, p2LauncherRegistryJSON)
	stub := buildP2CanaryStubAgy(t)
	t.Setenv(paths.EnvAgyProRealAgy, stub)
	t.Setenv(hybrid.EnvHybridProfile, "p2")
	stubP2PickerInventory(t)
	stubP2TTYAndConsole(t, "") // Enter = cancel
	dump := filepath.Join(t.TempDir(), "argv.jsonl")
	t.Setenv("AGY_PRO_P2_ARGSDUMP", dump)

	var code int
	var err error
	stderr := captureStderr(t, func() {
		code, err = ExecutePassthrough(stub, nil)
	})
	if err != nil || code != 0 {
		t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
	}
	argv := p2StubArgv(t, dump)
	if argValueLauncher(argv, "--model") != "" {
		t.Errorf("an aborted pick must send no --model: %v", argv)
	}
	for _, want := range []string{"no model selected", "default static id"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the abort notice is missing %q:\n%s", want, stderr)
		}
	}
}

// TestExecutePassthrough_HybridP2NonBareInvocationSkipsPicker proves every
// non-bare shape passes its args straight to the front door: no picker, no
// console read, and the explicit --model reaches the child as the blade: key.
// It also carries FIX-53 forward on the new path: a P2 session with a pinned
// external must NOT divert into the gateway bridge, so the child env carries no
// AGY_LLM_GATEWAY_* and no provider key VALUE.
func TestExecutePassthrough_HybridP2NonBareInvocationSkipsPicker(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"print one-shot", []string{"-p", "hi", "--model", "glm-5.3-flash-flex"}},
		{"explicit model pin", []string{"--model", "glm-5.3-flash-flex"}},
		{"models subcommand", []string{"models"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxHybridP1(t, p2LauncherRegistryJSON)
			stub := buildP2CanaryStubAgy(t)
			t.Setenv(paths.EnvAgyProRealAgy, stub)
			t.Setenv(hybrid.EnvHybridProfile, "p2")
			t.Setenv("NW_TEST_KEY", "sk-planted-p2-launcher-secret")
			failIfPickerRuns(t)
			dump := filepath.Join(t.TempDir(), "argv.jsonl")
			envDump := filepath.Join(t.TempDir(), "child.env")
			t.Setenv("AGY_PRO_P2_ARGSDUMP", dump)
			t.Setenv("AGY_PRO_P2_ENVDUMP", envDump)

			code, err := ExecutePassthrough(stub, tc.args)
			if err != nil || code != 0 {
				t.Fatalf("ExecutePassthrough = (%d, %v); want (0, nil)", code, err)
			}
			argv := p2StubArgv(t, dump)
			if hasLauncherModelFlag(tc.args) {
				pin := argValueLauncher(argv, "--model")
				if !strings.HasPrefix(pin, "blade:127.0.0.1:") || !strings.HasSuffix(pin, "/models/glm-5.3-flash-flex") {
					t.Errorf("--model = %q; want the blade: map key for the explicit pin", pin)
				}
			}
			// FIX-53 on the live path: no bridge, no key values, and the mesh's
			// gemini-mode injection instead.
			childEnv := readP2EnvDump(t, envDump)
			for _, banned := range []string{
				"AGY_LLM_GATEWAY_URL", "AGY_LLM_GATEWAY_WIRE_PROTOCOL",
				"AGY_LLM_GATEWAY_API_KEY", "AGY_LLM_GATEWAY_MODELS",
				"NW_TEST_KEY", "GOOGLE_TEST_KEY",
			} {
				if v, ok := childEnv[banned]; ok {
					t.Errorf("the P2 child env must not carry %s (FIX-53/S-3); got %q", banned, v)
				}
			}
			if !strings.HasPrefix(childEnv["GOOGLE_GEMINI_BASE_URL"], "http://127.0.0.1:") {
				t.Errorf("the P2 child must receive GOOGLE_GEMINI_BASE_URL=<mesh URL>; got %q", childEnv["GOOGLE_GEMINI_BASE_URL"])
			}
		})
	}
}

// readP2EnvDump parses a KEY=VALUE dump (one per line) into a map.
func readP2EnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '='); i >= 0 {
			out[line[:i]] = line[i+1:]
		}
	}
	return out
}

// argValueLauncher returns the value following flag in args, or "" when absent.
func argValueLauncher(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// hasLauncherModelFlag reports whether args carry a model selector.
func hasLauncherModelFlag(args []string) bool {
	for _, a := range args {
		if a == "--model" || a == "-m" || strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "-m=") {
			return true
		}
	}
	return false
}
