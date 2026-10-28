package launcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"agypro/internal/bridge"
	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// Model sub-menu (plan §4.2 B1) and the F-BRIDGE provenance routing
// (contract §2.7): native pick -> forced-native exec, NO bridge; external
// pick -> existing gateway+bridge path; abort -> legacy [2] bridge behavior.

// pickerFixture builds a combined inventory with enough rows to exercise
// multi-digit selection: 11 native (10 stub rows + the colliding id) + 4
// provider rows, including an id-collision pair on gemini-3.8-flash-high.
func pickerFixture() *inventory.Inventory {
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "google", Models: []providers.Model{
			{ID: "gemini-2.5-flash", Capabilities: providers.Capabilities{SupportsTools: true, SupportsVision: true}},
			{ID: "gemini-3.8-flash-high"}, // id collides with a native catalog id
		}},
		{Name: "nw", Models: []providers.Model{
			{ID: "glm-5.3-flash-flex", Capabilities: providers.Capabilities{SupportsTools: true}},
			{ID: "glm-5.3-flex"},
		}},
	}}
	var native []inventory.NativeModel
	for i := 0; i < 10; i++ {
		native = append(native, inventory.NativeModel{
			ID:          "native-" + string(rune('a'+i)),
			DisplayName: "Native " + string(rune('A'+i)),
		})
	}
	native = append(native, inventory.NativeModel{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash (High)"})
	inv, err := inventory.Load(context.Background(), reg, stubNative(native))
	if err != nil {
		panic(err)
	}
	return inv
}

type stubNative []inventory.NativeModel

func (s stubNative) Fetch(context.Context) ([]inventory.NativeModel, error) {
	return s, nil
}

// TestShowModelPickerScriptedTranscript drives the sub-menu through the
// shared stdinRelay (E16 pattern): the transcript shows the numbered combined
// inventory and a multi-digit pick resolves to that entry.
func TestShowModelPickerScriptedTranscript(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("11\n"))
	pick := showModelPicker(&out, nil, rel, inv)
	transcript := out.String()
	t.Logf("picker transcript:\n%s", transcript)

	if !strings.Contains(transcript, "[ 1] native-a") {
		t.Errorf("transcript missing numbered rows:\n%s", transcript)
	}
	if !strings.Contains(transcript, "[11] gemini-3.8-flash-high") {
		t.Errorf("transcript missing multi-digit row 11:\n%s", transcript)
	}
	if !strings.Contains(transcript, "[google]") || !strings.Contains(transcript, "[nw]") {
		t.Errorf("transcript missing provider group tags:\n%s", transcript)
	}
	if !pick.Picked {
		t.Fatalf("selection 11 was not accepted:\n%s", transcript)
	}
	if pick.Entry.Number != 11 || pick.Entry.ModelID != "gemini-3.8-flash-high" {
		t.Errorf("pick = %+v, want entry #11 gemini-3.8-flash-high", pick.Entry)
	}
}

func TestShowModelPickerAbortPaths(t *testing.T) {
	inv := pickerFixture()
	cases := []struct {
		name  string
		input string
	}{
		{"empty input cancels", "\n"},
		{"invalid token cancels", "zzz\n"},
		{"out of range cancels", "99\n"},
		{"immediate EOF cancels", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			rel := newStdinRelay(strings.NewReader(tc.input))
			pick := showModelPicker(&out, nil, rel, inv)
			if pick.Picked {
				t.Fatalf("input %q must cancel, got %+v", tc.input, pick.Entry)
			}
		})
	}
}

// The console-path picker must never touch the shared relay: the real-agy child
// inherits the console directly, which is the ONLY stdin configuration the agy
// TUI renders under (a relay pipe plus a console stdout blocks agy's startup
// read forever). Fail-without-fix pin for the [2] hang (2026-10-14).
func TestShowModelPickerConsolePathLeavesRelayUntouched(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("1\n")) // would pick seat 1 if used

	old := readConsoleLineFn
	readConsoleLineFn = func(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
		return "11", consoleChoice
	}
	defer func() { readConsoleLineFn = old }()

	pick := showModelPicker(&out, os.Stdin, rel, inv)
	if !pick.Picked || pick.Entry.Number != 11 {
		t.Fatalf("console pick = %+v, want entry #11", pick)
	}
	if rel.Used() {
		t.Fatal("console-path picker used the relay; the child would get a relay pipe as stdin (agy hang)")
	}
	if !strings.Contains(out.String(), "[11] gemini-3.8-flash-high") {
		t.Errorf("transcript missing row 11:\n%s", out.String())
	}
}

// A console timeout cancels with a stated outcome and consumes nothing, so a
// partial line stays queued for the child.
func TestShowModelPickerConsoleTimeoutCancelsWithMessage(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("1\n"))

	old := readConsoleLineFn
	readConsoleLineFn = func(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
		return "", consoleTimeout
	}
	defer func() { readConsoleLineFn = old }()

	pick := showModelPicker(&out, os.Stdin, rel, inv)
	if pick.Picked {
		t.Fatalf("timeout must cancel, got %+v", pick.Entry)
	}
	if rel.Used() {
		t.Fatal("timeout path used the relay")
	}
	if !strings.Contains(out.String(), "no model selected (timed out)") {
		t.Errorf("timeout cancel is silent:\n%s", out.String())
	}
}

// Every cancel states what happens next: an empty answer names the fallback,
// and a truly unparseable answer echoes the grammar.
func TestShowModelPickerCancelAndInvalidMessages(t *testing.T) {
	inv := pickerFixture()
	for _, tc := range []struct{ name, input, want string }{
		{"empty cancels", "\n", "no model selected (cancelled); continuing with External Models (bridge)"},
		{"invalid token names grammar", "zzz\n", "invalid selection"},
		{"out of range names grammar", "99\n", "invalid selection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			rel := newStdinRelay(strings.NewReader(tc.input))
			pick := showModelPicker(&out, nil, rel, inv)
			if pick.Picked {
				t.Fatalf("input %q must cancel, got %+v", tc.input, pick.Entry)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("cancel message missing %q:\n%s", tc.want, out.String())
			}
		})
	}
}

// A dot-typed selection ("1.11.12.14" — the shape a user naturally types for
// seats 1, 11, 12, 14) stays ONE token so the seeded masker keeps whole-shape
// coverage (U8); the picker answers with the grammar instead of failing
// silently. Fail-without-fix pin for the silent-cancel UX (2026-10-14).
func TestShowModelPickerDotTypedSelectionNamesGrammar(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("1.11.12.14\n"))
	pick := showModelPicker(&out, nil, rel, inv)
	if pick.Picked {
		t.Fatalf("dot-typed selection must not silently pick, got %+v", pick.Entry)
	}
	msg := out.String()
	if !strings.Contains(msg, "invalid selection") {
		t.Errorf("dot-typed selection failed without the grammar message:\n%s", msg)
	}
	if !strings.Contains(msg, "want a number 1-") {
		t.Errorf("grammar hint missing:\n%s", msg)
	}
}

// planFromPick routes by ENTRY PROVENANCE (F-BRIDGE). This is the core of the
// id-collision defense: the same id routes differently depending on whether
// the row came from the native catalog or the user's registry.
func TestPlanFromPickRoutesByProvenance(t *testing.T) {
	inv := pickerFixture()
	var nativeRow, providerRow *inventory.Entry
	for i := range inv.Entries {
		e := &inv.Entries[i]
		if e.ModelID != "gemini-3.8-flash-high" {
			continue
		}
		if e.Kind == "native" {
			nativeRow = e
		} else {
			providerRow = e
		}
	}
	if nativeRow == nil || providerRow == nil {
		t.Fatalf("collision pair missing from fixture: %+v", inv.Entries)
	}

	nativePlan := planFromPick(ModelPick{Entry: *nativeRow, Picked: true})
	if nativePlan.ForcedNativeID != "gemini-3.8-flash-high" || nativePlan.ForceBridge {
		t.Errorf("native pick plan = %+v, want forced-native and no bridge", nativePlan)
	}

	externalPlan := planFromPick(ModelPick{Entry: *providerRow, Picked: true})
	if !externalPlan.ForceBridge || externalPlan.ForcedNativeID != "" {
		t.Errorf("external pick plan = %+v, want forceBridge", externalPlan)
	}
	wantArgs := []string{"--model", "gemini-3.8-flash-high (google)"}
	if strings.Join(externalPlan.ModelArgs, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("external pick args = %v, want %v", externalPlan.ModelArgs, wantArgs)
	}

	abortPlan := planFromPick(ModelPick{})
	if !abortPlan.ForceBridge || abortPlan.ForcedNativeID != "" {
		t.Errorf("abort plan = %+v, want legacy [2] bridge fallback", abortPlan)
	}
}

// F-BRIDGE test (a): a native sub-menu pick under an id collision must start
// NO bridge and hand the child an env free of AGY_LLM_GATEWAY_* and provider
// key values - even though the legacy ShouldActivateBridge gate WOULD have
// hijacked the same id into gateway mode.
func TestForcedNativePickBypassesBridgeUnderIdCollision(t *testing.T) {
	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"google","baseURL":"http://127.0.0.1:1","apiKeyEnv":"GOOGLE_TEST_KEY","models":[{"id":"gemini-3.8-flash-high"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	t.Setenv("GOOGLE_TEST_KEY", "sk-planted-collision-secret")
	t.Setenv("AGY_LLM_GATEWAY_URL", "http://127.0.0.1:1")
	t.Setenv("AGY_LLM_GATEWAY_MODELS", "hijack-list")
	t.Setenv("AGY_PRO_BRIDGE", "")
	t.Setenv("AGY_BRIDGE", "")

	reg, err := providers.Load()
	if err != nil {
		t.Fatal(err)
	}

	// The collision trap: the legacy gate WOULD activate the bridge for this id.
	if activate, _ := ShouldActivateBridge([]string{"--model", "gemini-3.8-flash-high"}, reg); !activate {
		t.Fatal("fixture broken: ShouldActivateBridge must fire on the colliding id (that is the trap)")
	}

	// The provenance plan must NOT.
	nativeEntry := inventory.Entry{
		Kind: "native", Group: "native", ModelID: "gemini-3.8-flash-high",
		DisplayName: "Gemini 3.8 Flash (High)", Spec: "native:gemini-3.8-flash-high",
	}
	plan := planFromPick(ModelPick{Entry: nativeEntry, Picked: true})
	if plan.ForceBridge || plan.ForcedNativeID == "" {
		t.Fatalf("native pick must force native, got %+v", plan)
	}

	// No bridge may start on the forced-native path.
	bridgeCalls := 0
	oldBridge := startBridgeFunc
	startBridgeFunc = func(reg *providers.Registry, sessionKey string, toolsCfg bridge.BridgeToolsConfig) (string, func(), error) {
		bridgeCalls++
		return "", nil, nil
	}
	defer func() { startBridgeFunc = oldBridge }()

	envFile := filepath.Join(tempDir, "child-env.txt")
	t.Setenv("GO_WANT_BANNER_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)

	var gotArgs []string
	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		gotArgs = append([]string{}, args...)
		return exec.Command(os.Args[0], "-test.run=TestBannerHelperChild")
	}
	defer func() { spawnRealAgy = oldSpawn }()

	oldStdout := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pw
	code, runErr := executeForcedNative("dummy-real-agy", plan.ForcedNativeID, nil, reg)
	_ = pw.Close()
	os.Stdout = oldStdout
	drainDone := make(chan struct{})
	go func() {
		_, _ = pr.Read(make([]byte, 1<<20))
		_ = pr.Close()
		close(drainDone)
	}()

	if runErr != nil {
		t.Fatal(runErr)
	}
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Join(gotArgs, " ") != "--model gemini-3.8-flash-high" {
		t.Errorf("child args = %v, want [--model gemini-3.8-flash-high]", gotArgs)
	}
	if bridgeCalls != 0 {
		t.Errorf("bridge started %d times on a native pick (F-BRIDGE violation)", bridgeCalls)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("child env not recorded: %v", err)
	}
	dump := string(data)
	for _, banned := range []string{
		"AGY_LLM_GATEWAY_URL=",
		"AGY_LLM_GATEWAY_MODELS=",
		"sk-planted-collision-secret",
		"GOOGLE_TEST_KEY=",
	} {
		if strings.Contains(dump, banned) {
			t.Errorf("forced-native child env leaks %q:\n%s", banned, dump)
		}
	}
	if !strings.Contains(dump, "AGY_PRO_ACTIVE=1") {
		t.Errorf("forced-native child missing recursion latch:\n%s", dump)
	}
	<-drainDone
}

// R5BF2-4 (adjacent-pattern scan of the S-3 class R5B-FIX-4 closed on the
// inventory fetch): executeForcedNative - the menu-[2] native pick path - must
// scrub with the FIX-4 PRESET UNION, not the registry-only list. An exported
// preset key for a provider ABSENT from providers.json used to ride into this
// native child; the P2 session child, the P2 native fallback and the `agy
// models` fetch already close that hole.
func TestExecuteForcedNative_ScrubsPresetKeysAbsentFromRegistry(t *testing.T) {
	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	// A registry that declares ONE provider with a bespoke key env name, so
	// every preset key below belongs to a provider that is NOT registered.
	provJSON := `{"providers":[{"name":"onlyprov","baseURL":"http://127.0.0.1:1","apiKeyEnv":"ONLYPROV_TEST_KEY","models":[{"id":"ext-one"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	// Planted FAKE preset keys (never real values).
	t.Setenv("GROQ_API_KEY", "sk-planted-groq-fake-secret")
	t.Setenv("OPENAI_API_KEY", "sk-planted-openai-fake-secret")
	t.Setenv("ONLYPROV_TEST_KEY", "sk-planted-onlyprov-fake-secret")

	reg, err := providers.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	envFile := filepath.Join(tempDir, "child-env.txt")
	t.Setenv("GO_WANT_BANNER_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ENVFILE", envFile)

	var gotArgs []string
	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		gotArgs = append([]string{}, args...)
		return exec.Command(os.Args[0], "-test.run=TestBannerHelperChild")
	}
	defer func() { spawnRealAgy = oldSpawn }()

	oldStdout := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pw
	code, runErr := executeForcedNative("dummy-real-agy", "ext-one", nil, reg)
	_ = pw.Close()
	os.Stdout = oldStdout
	drainDone := make(chan struct{})
	go func() {
		_, _ = pr.Read(make([]byte, 1<<20))
		_ = pr.Close()
		close(drainDone)
	}()

	if runErr != nil {
		t.Fatal(runErr)
	}
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.Join(gotArgs, " ") != "--model ext-one" {
		t.Errorf("child args = %v; want [--model ext-one]", gotArgs)
	}
	<-drainDone

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("child env not recorded: %v", err)
	}
	dump := string(data)
	for _, banned := range []string{
		"GROQ_API_KEY=",
		"OPENAI_API_KEY=",
		"ONLYPROV_TEST_KEY=",
		"sk-planted-groq-fake-secret",
		"sk-planted-openai-fake-secret",
		"sk-planted-onlyprov-fake-secret",
	} {
		if strings.Contains(dump, banned) {
			t.Errorf("forced-native child env leaks %q (R5BF2-4 preset-union gap):\n%s", banned, dump)
		}
	}
	if !strings.Contains(dump, "AGY_PRO_ACTIVE=1") {
		t.Errorf("forced-native child missing recursion latch:\n%s", dump)
	}
}

// F-BRIDGE test (b): an external pick under the same collision routes to the
// gateway+bridge path with the DisplayName model spec.
func TestExternalPickActivatesBridgeUnderIdCollision(t *testing.T) {
	tempDir := t.TempDir()
	provFile := filepath.Join(tempDir, "providers.json")
	provJSON := `{"providers":[{"name":"google","baseURL":"http://127.0.0.1:1","models":[{"id":"gemini-3.8-flash-high"}]}]}`
	if err := os.WriteFile(provFile, []byte(provJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvProviders, provFile)
	reg, err := providers.Load()
	if err != nil {
		t.Fatal(err)
	}

	providerEntry := inventory.Entry{
		Kind: "provider", Group: "google", ModelID: "gemini-3.8-flash-high",
		DisplayName: "gemini-3.8-flash-high (google)", Spec: "google/gemini-3.8-flash-high",
	}
	plan := planFromPick(ModelPick{Entry: providerEntry, Picked: true})
	if !plan.ForceBridge || plan.ForcedNativeID != "" {
		t.Fatalf("external pick must force the bridge, got %+v", plan)
	}
	args := append([]string{}, plan.ModelArgs...)
	if activate, cleaned := ShouldActivateBridge(args, reg); !activate {
		t.Errorf("external pick args %v did not activate the bridge (cleaned=%v)", args, cleaned)
	}
}

// F-BRIDGE test (c): the legacy id-matching gate is UNCHANGED for direct CLI
// invocations (`agy -m ...` passthrough).
func TestShouldActivateBridgeLegacySemanticsUnchanged(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "google",
		Models: []providers.Model{
			{ID: "gemini-3.8-flash-high"},
			{ID: "off", Enabled: boolPtr(false)},
		},
	}}}
	cases := []struct {
		args     []string
		activate bool
	}{
		{[]string{"--model", "gemini-3.8-flash-high"}, true},
		{[]string{"-m", "gemini-3.8-flash-high (google)"}, true},
		{[]string{"--model", "gemini-3.8-flash-high (google)"}, true},
		{[]string{"--model", "not-registered"}, false},
		{[]string{}, false},
		{[]string{"--model", "off"}, false}, // disabled model never matches
	}
	for _, tc := range cases {
		got, _ := ShouldActivateBridge(tc.args, reg)
		if got != tc.activate {
			t.Errorf("ShouldActivateBridge(%v) = %v, want %v", tc.args, got, tc.activate)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// B9 — executeForcedNative carries the same .cmd/.bat wrap+escape guard as the
// native seats (internal/fanout/native_seat.go).
// ---------------------------------------------------------------------------

func TestIsBatchScriptAndBuildBatchCmdLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !isBatchScript(`C:\bin\real-agy.cmd`) || !isBatchScript(`C:\bin\REAL-AGY.BAT`) {
			t.Error("Windows .cmd/.bat targets must be detected as batch scripts")
		}
	}
	if isBatchScript("/usr/bin/agy") || isBatchScript(`C:\bin\agy.exe`) {
		t.Error("non-batch targets must not take the wrap path")
	}
	got := buildBatchCmdLine(`C:\bin\real-agy.cmd`, []string{"--model", "m1"})
	// cmd /s /c strips the first and last quote of the raw line and takes the
	// rest verbatim, so the line carries its own outer quotes around the
	// individually quoted tokens (same shape as the fanout guard; A11 adds
	// /v:off so delayed expansion cannot eat `!` in a token).
	want := `cmd.exe /d /v:off /s /c ""C:\bin\real-agy.cmd" "--model" "m1""`
	if got != want {
		t.Errorf("buildBatchCmdLine = %q; want %q", got, want)
	}
	// Every token is double-quoted; cmd metacharacters stay literal inside
	// quotes (the fanout escape rules).
	wrapped := buildBatchCmdLine(`C:\bin\real-agy.cmd`, []string{"--model", `m&1`})
	if !strings.Contains(wrapped, `"m&1"`) {
		t.Errorf("argument must be quoted as one token: %q", wrapped)
	}
}

func TestEscapeCmdToken_RoundTripShape(t *testing.T) {
	// Quotes, percent, carets and backslash runs follow the fanout rules.
	if got, want := escapeCmdToken("plain"), `"plain"`; got != want {
		t.Errorf("escapeCmdToken(plain) = %q; want %q", got, want)
	}
	if got := escapeCmdToken(`say "hi"`); !strings.Contains(got, `\"hi\"`) {
		t.Errorf("embedded quotes not escaped: %q", got)
	}
	if got := escapeCmdToken("100%"); !strings.Contains(got, `^%`) {
		t.Errorf("percent not escaped: %q", got)
	}
	if got := escapeCmdToken(`C:\dir\`); !strings.HasSuffix(got, `\\"`) {
		t.Errorf("trailing backslashes not doubled before closing quote: %q", got)
	}
}

// TestExecuteForcedNative_BatchWrapGuard (B9): a .cmd real-agy target is
// spawned via cmd.exe /d /v:off /s /c with a raw, hand-escaped command line —
// the guard that used to exist only on the native-seat path.
func TestExecuteForcedNative_BatchWrapGuard(t *testing.T) {
	batch := filepath.Join(t.TempDir(), "real-agy.cmd")
	if err := os.WriteFile(batch, []byte("@echo off\r\nexit /b 0\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var gotName string
	var gotArgs []string
	var gotCmd *exec.Cmd
	oldSpawn := spawnRealAgy
	spawnRealAgy = func(name string, args ...string) *exec.Cmd {
		gotName, gotArgs = name, append([]string{}, args...)
		if name == "cmd.exe" {
			gotCmd = exec.Command("cmd.exe", "/c", "exit 0")
			return gotCmd
		}
		gotCmd = exec.Command(os.Args[0], "-test.run=TestBannerHelperChild")
		return gotCmd
	}
	defer func() { spawnRealAgy = oldSpawn }()

	code, err := executeForcedNative(batch, "m1", nil, &providers.Registry{})
	if err != nil {
		t.Fatalf("executeForcedNative: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	if runtime.GOOS == "windows" {
		assertBatchWrapStamp(t, gotName, gotArgs, gotCmd)
	} else {
		if gotName != batch {
			t.Errorf("non-Windows must spawn the target directly, got %q", gotName)
		}
		if strings.Join(gotArgs, " ") != "--model m1" {
			t.Errorf("args = %v; want [--model m1]", gotArgs)
		}
	}
}

// TestPickerArgsHelperChild is the fake "real agy" child for the picker's
// cmd.exe parity round-trip: it dumps its NUL-separated argv and exits 0.
func TestPickerArgsHelperChild(t *testing.T) {
	if os.Getenv("GO_WANT_PICKER_ARGS_HELPER") != "1" {
		return
	}
	if p := os.Getenv("AGY_PRO_PICKER_ARGS_DUMP"); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(os.Args, "\x00")), 0o644)
	}
	os.Exit(0)
}

// TestEscapeCmdTokenDoublesBackslashesBeforeStepOutQuote (A11 parity with
// internal/fanout/native_seat_test.go): a pending backslash run is flushed
// DOUBLED before the `%` step-out quote. The pre-A11 picker copy flushed it
// single, so `a"\%b` reached the target as a re-framed argument.
func TestEscapeCmdTokenDoublesBackslashesBeforeStepOutQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`a\%b`, `"a\\"^%"b"`},
		{`a\\%b`, `"a\\\\"^%"b"`},
		// A11 hole rows (identical to fanout's matrix): quote flips cmd parity
		// out, then `%` emits `^%` with NO quote around it - the pending run
		// stays literal there, but the step-out quote itself doubles the run.
		{`a"\%b`, `"a\"\^%b"`},
		{`back"\%end`, `"back\"\^%end"`},
		{`q"\\%w`, `"q\"\\^%w"`},
		{`"\%`, `"\"\^%"`},
	}
	for _, tc := range cases {
		if got := escapeCmdToken(tc.in); got != tc.want {
			t.Errorf("escapeCmdToken(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestCmdWrapPickerParityRows (C1) runs the A11 hole rows through a REAL
// cmd.exe on the forced-native picker's wrap, mirroring fanout's
// TestCmdWrap_EscapeMatrixRoundTrip: the token must survive the cmd /c parse,
// the batch %* re-parse and CommandLineToArgvW byte-for-byte.
func TestCmdWrapPickerParityRows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip(".cmd wrapping is a Windows-only spawn hazard")
	}
	dir := t.TempDir()
	helper := os.Args[0]
	body := "@echo off\r\n\"" + helper + "\" -test.run=TestPickerArgsHelperChild -- %*\r\n"
	stub := filepath.Join(dir, "real-agy.cmd")
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	argsDump := filepath.Join(dir, "args.bin")
	t.Setenv("GO_WANT_PICKER_ARGS_HELPER", "1")
	t.Setenv("AGY_PRO_PICKER_ARGS_DUMP", argsDump)

	cases := []string{
		`plain`,
		`with spaces`,
		`pct 100%`,
		`pct\%back`, // A11: single backslash before a percent
		`a"\%b`,     // A11 hole: quote, backslash-run, percent
		`back"\%end`,
		// U9b: the P2 blade: map key the front door pins carries ':' and '/'
		// (and a registry id may carry a slash). The existing wrap must keep it
		// byte-for-byte, so a .cmd/.bat real-agy target stays safe on the P2
		// route too.
		`blade:127.0.0.1:52566/models/ext-nw`,
		`blade:127.0.0.1:52566/models/vendor/model-x`,
	}
	for _, tok := range cases {
		_ = os.Remove(argsDump)
		cmd := spawnRealAgy("cmd.exe")
		setRawCmdLine(cmd, buildBatchCmdLine(stub, []string{"--model", tok}))
		if err := cmd.Run(); err != nil {
			t.Errorf("token %q: cmd wrap run error: %v", tok, err)
			continue
		}
		data, err := os.ReadFile(argsDump)
		if err != nil {
			t.Fatalf("token %q: helper argv dump missing: %v", tok, err)
		}
		var found bool
		for _, a := range strings.Split(string(data), "\x00") {
			if a == tok {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("token %q did not survive the picker .cmd argv wrap byte-for-byte:\n got %q", tok, string(data))
		}
	}
}

// Fail-without-fix pin (gate round 4, F-R4-01): when the console path is
// unavailable (non-console stdin), the picker falls back to the shared relay
// and the relay IS used - the branch that keeps piped/CI stdin working.
func TestShowModelPickerConsoleUnavailableFallsBackToRelay(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("11\n"))

	old := readConsoleLineFn
	readConsoleLineFn = func(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
		return "", consoleUnavailable
	}
	defer func() { readConsoleLineFn = old }()

	pick := showModelPicker(&out, os.Stdin, rel, inv)
	if !pick.Picked || pick.Entry.Number != 11 {
		t.Fatalf("relay fallback pick = %+v, want entry #11", pick)
	}
	if !rel.Used() {
		t.Fatal("consoleUnavailable must fall back to the relay (non-console stdin)")
	}
}

// The [3] prompt has the same fallback and the same console-timeout cancel.
func TestRunSwarmFromMenuFallbackAndTimeout(t *testing.T) {
	t.Run("console unavailable falls back to relay", func(t *testing.T) {
		isolateSwarmTest(t, "http://127.0.0.1:1") // dead stub; the engine must never dial a real provider
		var out, errw strings.Builder
		rel := newStdinRelay(strings.NewReader("review this\n"))
		old := readConsoleLineFn
		readConsoleLineFn = func(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
			return "", consoleUnavailable
		}
		defer func() { readConsoleLineFn = old }()
		_, handled, err := runSwarmFromMenu(os.Stdin, rel, &out, &errw)
		if err != nil {
			t.Fatal(err)
		}
		if !handled {
			t.Fatal("relay fallback must handle the prompt")
		}
		if !rel.Used() {
			t.Fatal("consoleUnavailable must fall back to the relay")
		}
	})
	t.Run("console timeout cancels with message", func(t *testing.T) {
		var out, errw strings.Builder
		rel := newStdinRelay(strings.NewReader(""))
		old := readConsoleLineFn
		readConsoleLineFn = func(consoleIn *os.File, timeout time.Duration) (string, consoleRead) {
			return "", consoleTimeout
		}
		defer func() { readConsoleLineFn = old }()
		code, handled, err := runSwarmFromMenu(os.Stdin, rel, &out, &errw)
		if err != nil {
			t.Fatal(err)
		}
		if handled || code != 0 {
			t.Fatalf("timeout must fall through (handled=%v code=%d)", handled, code)
		}
		if rel.Used() {
			t.Fatal("timeout path must not touch the relay")
		}
		if !strings.Contains(errw.String(), "timed out or was too long") {
			t.Errorf("timeout message missing:\n%s", errw.String())
		}
	})
}

// ---------------------------------------------------------------------------
// FIX-8 disposition pin: the picker's "invalid selection" echo quotes the line
// the USER TYPED, which is not a registry slot. It therefore stays on
// providers.MaskOperand, and the registry-slot authority (providers.MaskSlot)
// must not be routed here: MaskSlot rewrites a location-shaped operand to its
// marker-stripped core ("./backups" -> "backups"), which would change the
// bytes of a benign non-answer. The registry-borne names in this menu are
// rendered by inventory.RenderNumbered, not by this line.
// ---------------------------------------------------------------------------

func TestPickerInvalidSelectionEchoKeepsTypedOperandRendering(t *testing.T) {
	inv := pickerFixture()
	var out strings.Builder
	rel := newStdinRelay(strings.NewReader("./backups\n"))
	pick := showModelPicker(&out, nil, rel, inv)
	if pick.Picked {
		t.Fatalf("a path-shaped selection must not pick, got %+v", pick.Entry)
	}
	msg := out.String()
	if !strings.Contains(msg, `invalid selection "./backups"`) {
		t.Errorf("typed operand rendering changed (MaskSlot would strip the location markers):\n%s", msg)
	}
	if !strings.Contains(msg, "want a number 1-") {
		t.Errorf("grammar hint missing:\n%s", msg)
	}
}
// ---------------------------------------------------------------------------
// R5-1: the invalid-selection echo applies the seat-token authority on top of
// the operand masker's location exemption, so a pasted underscore-head token
// cannot ride raw while a benign location-shaped answer keeps its exact bytes.
// ---------------------------------------------------------------------------

func TestPickerInvalidSelectionMasksUnderscoreHeadToken(t *testing.T) {
	inv := pickerFixture()
	const plant = "sk_live_0123456789abcdef"

	for _, tc := range []struct{ name, line string }{
		{name: "native token", line: "native:" + plant},
		{name: "provider token", line: "nw/" + plant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			pick := showModelPicker(&out, nil, newStdinRelay(strings.NewReader(tc.line+"\n")), inv)
			if pick.Picked {
				t.Fatalf("a token-shaped selection must not pick, got %+v", pick.Entry)
			}
			msg := out.String()
			if strings.Contains(msg, plant) {
				t.Errorf("invalid-selection echo leaks the underscore-head token raw:\n%s", msg)
			}
			if !strings.Contains(msg, "****") {
				t.Errorf("invalid-selection echo must be masked:\n%s", msg)
			}
			if !strings.Contains(msg, `invalid selection "`) || !strings.Contains(msg, "want a number 1-") {
				t.Errorf("sentence frame drifted:\n%s", msg)
			}
		})
	}
}

// The FIX-8 rationale survives: every benign location-shaped answer renders
// byte-identically (the seat-token authority would strip its markers), and a
// benign non-location answer is unchanged by either masker.
func TestPickerInvalidSelectionKeepsBenignLocationBytes(t *testing.T) {
	inv := pickerFixture()
	for _, line := range []string{
		"./backups",
		"./prompts/3f2a8b9c1d2e3f4a.md",
		"../notes",
		"native:gemini-3.8-flash-high",
		"a/b",
		"99",
	} {
		t.Run(line, func(t *testing.T) {
			var out strings.Builder
			pick := showModelPicker(&out, nil, newStdinRelay(strings.NewReader(line+"\n")), inv)
			if pick.Picked {
				t.Fatalf("a non-selection answer must not pick, got %+v", pick.Entry)
			}
			if want := `invalid selection ` + strconv.Quote(line); !strings.Contains(out.String(), want) {
				t.Errorf("benign answer drifted\n got: %q\nwant it to contain: %q", out.String(), want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// r6: a credential DRESSED as a location must still take the seat-token
// rendering. r5 gated the seat authority on "is this location-shaped?", so
// "./sk_live_.../x" and its siblings were treated as benign paths and echoed raw.
// Now a location is only allowed to keep the operand rendering when NONE of its
// segments carries credential material (providers.NameCarriesCredential, with the
// authority's identifier-run exemption).
// ---------------------------------------------------------------------------

func TestPickerInvalidSelectionMasksCredentialDressedAsLocation(t *testing.T) {
	inv := pickerFixture()
	const plant = "sk_live_0123456789abcdef"

	for _, tc := range []struct{ name, line string }{
		{name: "dot-slash prefix + trailing segment", line: "./" + plant + "/x"},
		{name: "dot-dot prefix", line: "../" + plant},
		{name: "windows drive", line: `C:\` + plant},
		{name: "url scheme", line: "scheme://" + plant},
		{name: "credential dressed as a filename", line: "./" + plant + ".md"},
		// r7 F-02: a BARE lowercase-hex run carries no file evidence, so it is
		// indistinguishable from an opaque credential segment and masks.
		{name: "bare lowercase-hex segment", line: "./3f2a8b9c1d2e3f4a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			pick := showModelPicker(&out, nil, newStdinRelay(strings.NewReader(tc.line+"\n")), inv)
			if pick.Picked {
				t.Fatalf("a credential-shaped answer must not pick, got %+v", pick.Entry)
			}
			msg := out.String()
			if strings.Contains(msg, plant) {
				t.Errorf("dressed credential rides the invalid-selection echo raw:\n%s", msg)
			}
			if !strings.Contains(msg, "****") {
				t.Errorf("dressed credential must be masked:\n%s", msg)
			}
			if !strings.Contains(msg, `invalid selection "`) {
				t.Errorf("sentence frame drifted:\n%s", msg)
			}
		})
	}
}

// The benign side of the same predicate: a location whose segments are plain
// names, identifier runs, or filenames keeps its exact bytes.
func TestPickerLocationPredicateKeepsBenignAnswersByteIdentical(t *testing.T) {
	inv := pickerFixture()
	for _, line := range []string{
		"./backups",
		"../notes",
		"./prompts/3f2a8b9c1d2e3f4a.md",
		"./8f2d6c1b-1234-5678-9abc-def012345678",
		`C:\notes`,
		"https://user:pass@host/x",
		"./src/pkg/file.go",
	} {
		t.Run(line, func(t *testing.T) {
			var out strings.Builder
			pick := showModelPicker(&out, nil, newStdinRelay(strings.NewReader(line+"\n")), inv)
			if pick.Picked {
				t.Fatalf("a non-selection answer must not pick, got %+v", pick.Entry)
			}
			if want := `invalid selection ` + strconv.Quote(line); !strings.Contains(out.String(), want) {
				t.Errorf("benign location drifted\n got: %q\nwant it to contain: %q", out.String(), want)
			}
		})
	}
}

// Unit pin for the predicate itself, so the two properties are stated together
// and a future edit to either half fails here first.
func TestPickerLocationShapedCredentialCore(t *testing.T) {
	const plant = "sk_live_0123456789abcdef"
	for _, shaped := range []string{
		"./backups",
		"../notes",
		"./prompts/3f2a8b9c1d2e3f4a.md",
		`C:\notes`,
		"https://user:pass@host/x",
	} {
		if !pickerLocationShaped(shaped) {
			t.Errorf("pickerLocationShaped(%q) = false; a benign location must keep the operand rendering", shaped)
		}
	}
	for _, notShaped := range []string{
		"./" + plant + "/x",
		"../" + plant,
		`C:\` + plant,
		"scheme://" + plant,
		"./" + plant + ".md",
		"./3f2a8b9c1d2e3f4a", // r7 F-02: bare hex, no filename evidence
		plant,
		"nw/" + plant,
	} {
		if pickerLocationShaped(notShaped) {
			t.Errorf("pickerLocationShaped(%q) = true; credential material must take the seat-token rendering", notShaped)
		}
	}
}

// ---------------------------------------------------------------------------
// r7 F-03 (drift guard, cheap): pickerLocationDecorated restates providers'
// unexported isLocationShaped. The two are equivalent today; this table fails if
// the picker copy is edited without re-checking the authority (a future NARROWING
// of the private predicate would otherwise leave the picker's exemption wider than
// the authority's, silently re-opening the raw-echo window r6 closed).
// ---------------------------------------------------------------------------

func TestPickerLocationDecoratedShapeTable(t *testing.T) {
	// An OS-absolute path is decoration on every platform; computed rather than
	// spelled "/abs/path", which Windows does not read as absolute (and the
	// authority's filepath.IsAbs does not either - the two agree by construction).
	abs, aerr := filepath.Abs("x")
	if aerr != nil {
		t.Fatalf("filepath.Abs: %v", aerr)
	}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"https://gw.example/v1", true},
		{"scheme://sk_live_0123456789abcdef", true},
		{"./backups", true},
		{"../notes", true},
		{`.\win\dir`, true},
		{`..\up`, true},
		{abs, true},
		{`C:\notes`, true},
		{abs, true},
		{"a/b", false},
		{"nw/glm-5.3-flash", false},
		{"native:gemini-3.8-flash-high", false},
		{"1,3,5", false},
		{"", false},
	} {
		if got := pickerLocationDecorated(tc.in); got != tc.want {
			t.Errorf("pickerLocationDecorated(%q) = %v; want %v (the authority's isLocationShaped shape)", tc.in, got, tc.want)
		}
	}
}
