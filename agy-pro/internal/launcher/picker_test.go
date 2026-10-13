package launcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	pick := showModelPicker(&out, rel, inv)
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
			pick := showModelPicker(&out, rel, inv)
			if pick.Picked {
				t.Fatalf("input %q must cancel, got %+v", tc.input, pick.Entry)
			}
		})
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
		if gotName != "cmd.exe" {
			t.Errorf("batch target must be wrapped in cmd.exe, got spawn %q", gotName)
		}
		if len(gotArgs) != 0 {
			t.Errorf("wrap puts the whole line on the raw command line, got args %v", gotArgs)
		}
		if gotCmd == nil || gotCmd.SysProcAttr == nil || gotCmd.SysProcAttr.CmdLine == "" {
			t.Fatal("setRawCmdLine did not stamp the raw command line")
		}
		if !strings.Contains(gotCmd.SysProcAttr.CmdLine, "real-agy.cmd") {
			t.Errorf("raw line must carry the batch target: %q", gotCmd.SysProcAttr.CmdLine)
		}
		if !strings.HasPrefix(gotCmd.SysProcAttr.CmdLine, `cmd.exe /d /v:off /s /c "`) {
			t.Errorf("raw line must be the /d /v:off /s /c wrap: %q", gotCmd.SysProcAttr.CmdLine)
		}
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
