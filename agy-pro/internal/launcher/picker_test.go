package launcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
