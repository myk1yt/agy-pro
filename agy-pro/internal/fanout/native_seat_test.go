package fanout

// Stub-binary subprocess tests for the native-seat runner (contract §2.2/§2.3,
// U1 test list). Uses the standard Go helper-process pattern: the test binary
// re-invokes itself as `<os.Args[0]> -test.run=TestStubAgyProcess -- <runner
// args>` with AGY_PRO_STUB_HELPER=1, so a REAL subprocess exercises spawn,
// argv/stdin transports, exit mapping, env scrubbing and the concurrency
// semaphore exactly as production does.
//
// The `--` separator before the carried runner args keeps Go's flag parser
// from rejecting production spellings like `--model` (implementation detail of
// the §2.3 injection mechanism; the contract's "args carried for assertion"
// behavior is preserved).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// TestStubAgyProcess is the helper subprocess. It is skipped during a normal
// test run and only executes when the spawner sets AGY_PRO_STUB_HELPER=1.
// Behavior is driven by env vars:
//
//	AGY_PRO_STUB_ARGS_DUMP  – write NUL-separated os.Args to this path
//	AGY_PRO_STUB_ENV_DUMP   – write sorted environ lines to this path
//	AGY_PRO_STUB_STDIN_DUMP – copy stdin to this path
//	AGY_PRO_STUB_LOG        – append "S <nano>" / "E <nano>" overlap events
//	AGY_PRO_STUB_SLEEP_MS   – sleep before exiting (after logging start)
//	AGY_PRO_STUB_STDOUT     – bytes written to stdout
//	AGY_PRO_STUB_STDERR     – bytes written to stderr
//	AGY_PRO_STUB_EXIT       – process exit code (default 0)
func TestStubAgyProcess(t *testing.T) {
	if os.Getenv("AGY_PRO_STUB_HELPER") != "1" {
		t.Skip("stub helper process; spawn with AGY_PRO_STUB_HELPER=1")
	}
	if p := os.Getenv("AGY_PRO_STUB_ARGS_DUMP"); p != "" {
		_ = os.WriteFile(p, []byte(strings.Join(os.Args, "\x00")), 0o644)
	}
	if p := os.Getenv("AGY_PRO_STUB_ENV_DUMP"); p != "" {
		env := os.Environ()
		sort.Strings(env)
		_ = os.WriteFile(p, []byte(strings.Join(env, "\n")), 0o644)
	}
	if p := os.Getenv("AGY_PRO_STUB_STDIN_DUMP"); p != "" {
		data, _ := readAllStdin()
		_ = os.WriteFile(p, data, 0o644)
	}
	sleepMs, _ := strconv.Atoi(os.Getenv("AGY_PRO_STUB_SLEEP_MS"))
	if p := os.Getenv("AGY_PRO_STUB_LOG"); p != "" {
		appendLine(p, fmt.Sprintf("S %d", time.Now().UnixNano()))
		if sleepMs > 0 {
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)
		}
		appendLine(p, fmt.Sprintf("E %d", time.Now().UnixNano()))
	} else if sleepMs > 0 {
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}
	if s := os.Getenv("AGY_PRO_STUB_STDOUT"); s != "" {
		fmt.Print(s)
	}
	if s := os.Getenv("AGY_PRO_STUB_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	code, _ := strconv.Atoi(os.Getenv("AGY_PRO_STUB_EXIT"))
	os.Exit(code)
}

func readAllStdin() ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out, nil
		}
	}
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintln(f, line)
	_ = f.Close()
}

func boolPtr(b bool) *bool { return &b }

// writeRegistry serializes reg to path (same shape the fanout tests use).
func writeRegistry(t *testing.T, path string, reg *providers.Registry) {
	t.Helper()
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// newStubChatServer stands up a single-turn chat-completions stub that always
// answers with content (the existing fanout tests use inline httptest servers;
// this keeps the native/mixed tests short).
func newStubChatServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:         0,
			Message:       ChatMessage{Role: "assistant", Content: content},
			FinishReason:  "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubSpawn returns a §2.3-style SpawnCommand factory that re-invokes the
// current test binary as the helper, carrying the runner's args after `--`
// and merging extra marker env into the child environment.
func stubSpawn(extraEnv map[string]string) func(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		helper := append([]string{"-test.run=TestStubAgyProcess", "--"}, args...)
		c := exec.CommandContext(ctx, os.Args[0], helper...)
		env := os.Environ()
		env = append(env, "AGY_PRO_STUB_HELPER=1")
		keys := make([]string, 0, len(extraEnv))
		for k := range extraEnv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			env = append(env, k+"="+extraEnv[k])
		}
		c.Env = env
		return c
	}
}

// stubResolver returns a NativeResolver pinned to the test binary.
func stubResolver() func() (string, error) {
	return func() (string, error) { return os.Args[0], nil }
}

// argsDumped reads the NUL-separated argv dump written by the helper.
func argsDumped(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args dump: %v", err)
	}
	return strings.Split(string(data), "\x00")
}

// findArg returns the index of the exact arg value, or -1.
func findArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestNativeRunner_ExitZeroCapturesStdout(t *testing.T) {
	dir := t.TempDir()
	argsDump := filepath.Join(dir, "args.bin")
	answer := "## Findings\n- **[LOW]** ok from stub"

	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_ARGS_DUMP": argsDump,
			"AGY_PRO_STUB_STDOUT":    answer,
		}),
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "gemini-3.8-flash-high"}, "seat prompt", time.Now().Add(30*time.Second))
	if out.SpawnErr != nil {
		t.Fatalf("spawn error: %v", out.SpawnErr)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d; want 0 (stderr: %s)", out.ExitCode, out.StderrTail)
	}
	if out.Content != answer {
		t.Fatalf("content = %q; want %q", out.Content, answer)
	}
	res := nativeSeatResult(1, nativeTarget("gemini-3.8-flash-high"), out, nil)
	if res.Status != "success" || res.Kind != SeatKindNative {
		t.Fatalf("result = %+v; want success/native", res)
	}
	// Transport: argv carries --model <id>, -p <prompt>, --print-timeout <N>s.
	args := argsDumped(t, argsDump)
	if findArg(args, "--model") < 0 || findArg(args, "gemini-3.8-flash-high") < 0 {
		t.Fatalf("argv missing --model <id>: %q", args)
	}
	if findArg(args, "seat prompt") < 0 {
		t.Fatalf("argv missing prompt: %q", args)
	}
	pt := findArg(args, "--print-timeout")
	if pt < 0 || pt+1 >= len(args) || !strings.HasSuffix(args[pt+1], "s") {
		t.Fatalf("argv missing --print-timeout <N>s: %q", args)
	}
}

func TestNativeRunner_ExitThreeFailsWithRedactedTail(t *testing.T) {
	dir := t.TempDir()
	// Registered key value + generic shapes: the seat redactor must mask both.
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "p", APIKeyEnv: "STUB_PROV_KEY", Models: []providers.Model{{ID: "m"}}},
	}}
	t.Setenv("STUB_PROV_KEY", "sk-registered-secret-abc123")

	stderr := "AGY_ERROR: {\"status\":429}\nAuthorization: Bearer sk-registered-secret-abc123\ntoken sk-generic-shape-XYZ789abcd leaked\n"
	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_EXIT":   "3",
			"AGY_PRO_STUB_STDERR": stderr,
		}),
		scrubNames: realagy.ApiKeyEnvNames(reg),
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "gemini-3.8-pro"}, "p", time.Now().Add(30*time.Second))
	if out.ExitCode != 3 {
		t.Fatalf("exit code = %d; want 3", out.ExitCode)
	}
	res := nativeSeatResult(1, nativeTarget("gemini-3.8-pro"), out, reg.Redactor())
	if res.Status != "failed" || !res.Degraded {
		t.Fatalf("result = %+v; want failed/degraded", res)
	}
	if !strings.Contains(res.Error, "native seat exited 3") {
		t.Errorf("error = %q; want exit-code mapping", res.Error)
	}
	if !strings.HasPrefix(res.Error, "reason=quota ") {
		t.Errorf("error = %q; want reason=quota prefix (429 in stderr)", res.Error)
	}
	if strings.Contains(res.Error, "sk-registered-secret-abc123") || strings.Contains(res.Error, "sk-generic-shape-XYZ789abcd") {
		t.Errorf("error leaked key material: %q", res.Error)
	}
	if !strings.Contains(res.Error, "[REDACTED]") {
		t.Errorf("error = %q; want [REDACTED] in the stderr tail", res.Error)
	}
}

func TestNativeRunner_EmptyStdoutFails(t *testing.T) {
	dir := t.TempDir()
	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn:    stubSpawn(map[string]string{"AGY_PRO_STUB_STDOUT": "   \n  "}),
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(30*time.Second))
	if out.ExitCode != 0 {
		t.Fatalf("exit code = %d; want 0", out.ExitCode)
	}
	res := nativeSeatResult(1, nativeTarget("m"), out, nil)
	if res.Status != "failed" {
		t.Fatalf("empty stdout must fail (A2 no textless hang), got %+v", res)
	}
	if res.Error != "native seat produced empty output" {
		t.Fatalf("error = %q; want empty-output mapping", res.Error)
	}
}

func TestNativeRunner_SpawnMissingBinaryFailsSeat(t *testing.T) {
	dir := t.TempDir()
	r := &NativeRunner{
		Resolver: func() (string, error) {
			return filepath.Join(dir, "definitely-not-here-agy.exe"), nil
		},
		Spawn: func(ctx context.Context, bin string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, bin, args...)
		},
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(10*time.Second))
	if out.SpawnErr == nil {
		t.Fatalf("want spawn error for missing binary, got %+v", out)
	}
	res := nativeSeatResult(1, nativeTarget("m"), out, nil)
	if res.Status != "failed" {
		t.Fatalf("result = %+v; want failed", res)
	}
	if !strings.Contains(res.Error, "native seat spawn failed") {
		t.Fatalf("error = %q; want spawn-failed mapping", res.Error)
	}
}

func TestNativeSeat_SpawnFailureDoesNotSinkSiblings(t *testing.T) {
	// Contract §2.2.3: a spawn error surfaces once as a failed seat; sibling
	// seats are unaffected.
	dir := t.TempDir()
	var badModelSeen int32
	cfg := RunConfig{
		Task:   "review",
		Prompt: "sibling isolation",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:good-model", ModelID: "good-model"},
			{Kind: SeatKindNative, Raw: "native:bad-model", ModelID: "bad-model"},
		},
		Root:           dir,
		OutDir:         filepath.Join(dir, "reports"),
		RegistryPath:   filepath.Join(dir, "missing-providers.json"),
		TimeoutSec:     15,
		NativeResolver: stubResolver(),
		SpawnCommand: func(ctx context.Context, bin string, args ...string) *exec.Cmd {
			// The runner carries --model <id>; fail to spawn only bad-model.
			if findArg(args, "bad-model") >= 0 {
				atomic.AddInt32(&badModelSeen, 1)
				return exec.CommandContext(ctx, filepath.Join(dir, "no-such-agy.exe"), args...)
			}
			return stubSpawn(map[string]string{"AGY_PRO_STUB_STDOUT": "## Findings\n- **[LOW]** ok"})(ctx, bin, args...)
		},
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if atomic.LoadInt32(&badModelSeen) != 1 {
		t.Fatalf("bad-model should have been spawned exactly once, got %d", badModelSeen)
	}
	if len(summary.Seats) != 2 {
		t.Fatalf("expected both seats reported, got %d", len(summary.Seats))
	}
	var good, bad *SeatResult
	for i := range summary.Seats {
		s := &summary.Seats[i]
		switch s.FinalTarget.Model.ID {
		case "good-model":
			good = s
		case "bad-model":
			bad = s
		}
	}
	if good == nil || good.Status != "success" {
		t.Fatalf("sibling seat must succeed, got %+v", good)
	}
	if bad == nil || bad.Status != "failed" || !strings.Contains(bad.Error, "native seat spawn failed") {
		t.Fatalf("spawn-failed seat mapping wrong, got %+v", bad)
	}
	// A6: a seat that never started is degraded, and the summary says so.
	if bad == nil || !bad.Degraded {
		t.Fatalf("spawn-failed seat must be degraded, got %+v", bad)
	}
	if !summary.Degraded {
		t.Fatalf("summary.Degraded = false; want true when a seat failed to spawn")
	}
}

func TestNativeRunner_ResolverErrorFailsSeat(t *testing.T) {
	dir := t.TempDir()
	r := &NativeRunner{
		Resolver: func() (string, error) { return "", realagy.ErrNotFound },
		Spawn:    stubSpawn(nil),
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(10*time.Second))
	if !errors.Is(out.SpawnErr, realagy.ErrNotFound) {
		t.Fatalf("SpawnErr = %v; want realagy.ErrNotFound", out.SpawnErr)
	}
	res := nativeSeatResult(1, nativeTarget("m"), out, nil)
	if res.Status != "failed" || !strings.Contains(res.Error, "native seat spawn failed") {
		t.Fatalf("result = %+v; want failed spawn mapping", res)
	}
}

func TestNativeRunner_ViaStdinTransport(t *testing.T) {
	dir := t.TempDir()
	argsDump := filepath.Join(dir, "args.bin")
	stdinDump := filepath.Join(dir, "stdin.bin")
	prompt := "system preset\n\nmulti\nline\nprompt bytes"

	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_ARGS_DUMP":  argsDump,
			"AGY_PRO_STUB_STDIN_DUMP": stdinDump,
			"AGY_PRO_STUB_STDOUT":     "OK",
		}),
		ViaStdin: true,
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, prompt, time.Now().Add(30*time.Second))
	if out.ExitCode != 0 {
		t.Fatalf("exit = %d stderr=%s", out.ExitCode, out.StderrTail)
	}
	got, err := os.ReadFile(stdinDump)
	if err != nil {
		t.Fatalf("read stdin dump: %v", err)
	}
	if string(got) != prompt {
		t.Fatalf("stdin round-trip = %q; want prompt bytes %q", got, prompt)
	}
	args := argsDumped(t, argsDump)
	// ViaStdin: bare -p, no prompt token in argv.
	pt := findArg(args, "-p")
	if pt < 0 {
		t.Fatalf("argv missing bare -p: %q", args)
	}
	if findArg(args, prompt) >= 0 {
		t.Fatalf("prompt must not appear in argv under ViaStdin: %q", args)
	}
}

func TestNativeRunner_ChildEnvScrub(t *testing.T) {
	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "testprov", APIKeyEnv: "TESTPROV_KEY", Models: []providers.Model{{ID: "m"}}},
	}}

	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_ENV_DUMP":       envDump,
			"AGY_PRO_STUB_STDOUT":         "OK",
			"TESTPROV_KEY":                "sk-plain-secret-001",
			"\ufeffTESTPROV_KEY":          "sk-bom-secret-002",
			"AGY_LLM_GATEWAY_URL":         "http://127.0.0.1:1",
			"AGY_LLM_GATEWAY_WIRE_PROTOCOL": "openai",
			"AGY_LLM_GATEWAY_API_KEY":     "gw-secret-003",
			"AGY_LLM_GATEWAY_MODELS":      "a,b",
			"AGY_PRO_BRIDGE":              "1",
			"AGY_BRIDGE":                  "1",
		}),
		scrubNames: realagy.ApiKeyEnvNames(reg),
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(30*time.Second))
	if out.ExitCode != 0 {
		t.Fatalf("exit = %d stderr=%s", out.ExitCode, out.StderrTail)
	}
	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	dump := string(raw)

	// R4: recursion latch present exactly as AGY_PRO_ACTIVE=1.
	if !strings.Contains(dump, "AGY_PRO_ACTIVE=1") {
		t.Errorf("child env missing AGY_PRO_ACTIVE=1 latch:\n%s", dump)
	}
	// F-ENV: no gateway or bridge vars may reach the child.
	for _, banned := range []string{
		"AGY_LLM_GATEWAY_URL=", "AGY_LLM_GATEWAY_WIRE_PROTOCOL=",
		"AGY_LLM_GATEWAY_API_KEY=", "AGY_LLM_GATEWAY_MODELS=",
		"AGY_PRO_BRIDGE=", "AGY_BRIDGE=",
	} {
		if strings.Contains(dump, banned) {
			t.Errorf("child env must not contain %q:\n%s", banned, dump)
		}
	}
	// A4 / F-Db: provider key VALUES must never reach the child, under any
	// BOM/whitespace variant of the registered APIKeyEnv name.
	for _, secret := range []string{"sk-plain-secret-001", "sk-bom-secret-002", "gw-secret-003"} {
		if strings.Contains(dump, secret) {
			t.Errorf("child env leaked %q:\n%s", secret, dump)
		}
	}
	for _, line := range strings.Split(dump, "\n") {
		name := line
		if i := strings.IndexByte(line, '='); i >= 0 {
			name = line[:i]
		}
		if normalizeForTest(name) == "TESTPROV_KEY" {
			t.Errorf("TESTPROV_KEY variant survived into child env: %q", line)
		}
	}
}

// normalizeForTest mirrors realagy.normalizeEnvName for assertions.
func normalizeForTest(name string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(name), "\ufeff", ""))
}

func TestNativeRunner_TimeoutMapsToFailedSeat(t *testing.T) {
	dir := t.TempDir()
	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_SLEEP_MS": "5000",
			"AGY_PRO_STUB_STDOUT":   "should not matter",
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	out := r.Run(ctx, RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(300*time.Millisecond))
	if !out.TimedOut {
		t.Fatalf("want TimedOut, got %+v", out)
	}
	res := nativeSeatResult(1, nativeTarget("m"), out, nil)
	if res.Status != "failed" || !res.Degraded {
		t.Fatalf("result = %+v; want failed/degraded", res)
	}
	if !strings.HasPrefix(res.Error, "reason=timeout") {
		t.Fatalf("error = %q; want reason=timeout prefix", res.Error)
	}
}

// maxOverlap computes the maximum number of concurrent "S/E" intervals in the
// stub's overlap log.
func maxOverlap(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read overlap log: %v", err)
	}
	type ev struct {
		nano int64
		isS  bool
	}
	var evs []ev
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("bad overlap log line %q", line)
		}
		n, _ := strconv.ParseInt(parts[1], 10, 64)
		evs = append(evs, ev{nano: n, isS: parts[0] == "S"})
	}
	// Ends are ordered before starts at identical timestamps so a same-instant
	// end+start pair never inflates the overlap count.
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].nano == evs[j].nano {
			return !evs[i].isS && evs[j].isS
		}
		return evs[i].nano < evs[j].nano
	})
	cur, max := 0, 0
	for _, e := range evs {
		if e.isS {
			cur++
			if cur > max {
				max = cur
			}
		} else {
			cur--
		}
	}
	return max
}

func TestNativeSeatConcurrencyCap(t *testing.T) {
	// A3 (contract §2.3): 3 native seats, cap 2 → max observed overlap ≤ 2
	// (and the cap must actually bind: with 3× 400ms sleeps unbounded would
	// reach 3), and all 3 seats produce results.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "overlap.log")
	var completed int32

	cfg := RunConfig{
		Task:   "review",
		Prompt: "concurrency probe",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-a", ModelID: "gemini-a"},
			{Kind: SeatKindNative, Raw: "native:gemini-b", ModelID: "gemini-b"},
			{Kind: SeatKindNative, Raw: "native:gemini-c", ModelID: "gemini-c"},
		},
		N:                    3,
		MaxNativeConcurrency: 2,
		Root:                 dir,
		OutDir:               filepath.Join(dir, "reports"),
		RegistryPath:         filepath.Join(dir, "missing-providers.json"),
		TimeoutSec:           30,
		NativeResolver:       stubResolver(),
		SpawnCommand: stubSpawn(map[string]string{
			"AGY_PRO_STUB_LOG":      logPath,
			"AGY_PRO_STUB_SLEEP_MS": "400",
			"AGY_PRO_STUB_STDOUT":   "## Findings\n- **[LOW]** ok",
		}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if summary.NCompleted != 3 {
		t.Fatalf("expected 3 seats reported, got %d", summary.NCompleted)
	}
	for _, s := range summary.Seats {
		if s.Status == "success" {
			atomic.AddInt32(&completed, 1)
		}
	}
	if atomic.LoadInt32(&completed) != 3 {
		t.Fatalf("expected 3 successful stub seats, got %d", completed)
	}
	ov := maxOverlap(t, logPath)
	if ov > 2 {
		t.Fatalf("max concurrent native seats = %d; cap is 2", ov)
	}
	if ov != 2 {
		t.Fatalf("max concurrent native seats = %d; the cap-2 semaphore never bound (want exactly 2)", ov)
	}
}

func TestNativeSeatConcurrencyDefaultClamp(t *testing.T) {
	// MaxNativeConcurrency <= 0 falls back to mode.DefaultNativeSeatConcurrency
	// (2); a single seat still runs to completion under the default cap.
	dir := t.TempDir()
	cfg := RunConfig{
		Task:   "review",
		Prompt: "p",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-a", ModelID: "gemini-a"},
			{Kind: SeatKindNative, Raw: "native:gemini-b", ModelID: "gemini-b"},
		},
		MaxNativeConcurrency: 0,
		Root:                 dir,
		OutDir:               filepath.Join(dir, "reports"),
		RegistryPath:         filepath.Join(dir, "missing-providers.json"),
		TimeoutSec:           20,
		NativeResolver:       stubResolver(),
		SpawnCommand: stubSpawn(map[string]string{
			"AGY_PRO_STUB_STDOUT": "OK",
		}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if summary.NCompleted != 2 {
		t.Fatalf("expected 2 seats, got %d", summary.NCompleted)
	}
}

func TestMixedSwarmNativeAndProvider(t *testing.T) {
	// A1 stub: one native seat + one provider seat in one swarm produce seat
	// reports with kind provenance and a reconciliation.md with the native
	// count header (contract §2.4).
	dir := t.TempDir()
	server := newStubChatServer(t, `## Findings
- **[MEDIUM]** provider finding`)
	reg := &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "prov-a",
			BaseURL:   server.URL,
			APIKeyEnv: "MIXED_KEY",
			Models:    []providers.Model{{ID: "model-a", Family: "family-a", Enabled: boolPtr(true)}},
		},
	}}
	t.Setenv("MIXED_KEY", "key-1")

	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:   "review",
		Prompt: "mixed swarm",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-3.8-flash-high", ModelID: "gemini-3.8-flash-high"},
			{Kind: SeatKindProvider, Raw: "prov-a/model-a", ModelID: "prov-a/model-a"},
		},
		Root:           dir,
		OutDir:         outDir,
		RegistryPath:   regPath,
		TimeoutSec:     15,
		NativeResolver: stubResolver(),
		SpawnCommand: stubSpawn(map[string]string{
			"AGY_PRO_STUB_STDOUT": "## Findings\n- **[LOW]** native finding",
		}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if summary.NCompleted != 2 {
		t.Fatalf("expected 2 seats, got %d", summary.NCompleted)
	}
	kinds := map[SeatKind]int{}
	for _, s := range summary.Seats {
		kinds[s.Kind]++
	}
	if kinds[SeatKindNative] != 1 || kinds[SeatKindProvider] != 1 {
		t.Fatalf("kind provenance = %v; want 1 native + 1 provider", kinds)
	}
	// Seat report rows (§2.4).
	mdFiles, _ := filepath.Glob(filepath.Join(outDir, "seat-*.md"))
	if len(mdFiles) < 2 {
		t.Fatalf("expected per-seat reports, got %v", mdFiles)
	}
	nativeReport := ""
	for _, f := range mdFiles {
		if strings.Contains(f, "native") {
			b, _ := os.ReadFile(f)
			nativeReport = string(b)
		}
	}
	if nativeReport == "" {
		t.Fatalf("expected a seat-N-native-*.md report among %v", mdFiles)
	}
	for _, row := range []string{"| Kind | native |", "| Quota/Cost | antigravity subscription quota (rate limits apply) |"} {
		if !strings.Contains(nativeReport, row) {
			t.Errorf("native seat report missing %q:\n%s", row, nativeReport)
		}
	}
	recon, err := os.ReadFile(filepath.Join(outDir, "reconciliation.md"))
	if err != nil {
		t.Fatalf("reconciliation.md: %v", err)
	}
	if !strings.Contains(string(recon), "**Native Seats:** `1 / 2`") {
		t.Errorf("reconciliation.md missing native-seat header:\n%s", recon)
	}
	if !strings.Contains(string(recon), "native/gemini-3.8-flash-high") {
		t.Errorf("reconciliation.md matrix missing native seat cell:\n%s", recon)
	}
}

func TestFREG_AllNativeToleratesMissingRegistry(t *testing.T) {
	// F-REG (contract §2.1): all-native --seats with a nonexistent registry
	// path must run; a mixed seat list under the same condition must hard-fail.
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-providers.json")

	cfg := RunConfig{
		Task:   "review",
		Prompt: "all native",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-x", ModelID: "gemini-x"},
		},
		Root:           dir,
		OutDir:         filepath.Join(dir, "reports"),
		RegistryPath:   missing,
		TimeoutSec:     15,
		NativeResolver: stubResolver(),
		SpawnCommand: stubSpawn(map[string]string{
			"AGY_PRO_STUB_STDOUT": "## Findings\n- **[LOW]** ok",
		}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("all-native missing registry must succeed, got %v", err)
	}
	if summary.NCompleted != 1 {
		t.Fatalf("expected 1 seat, got %d", summary.NCompleted)
	}

	cfgMixed := cfg
	cfgMixed.Seats = []SeatSpec{
		{Kind: SeatKindNative, Raw: "native:gemini-x", ModelID: "gemini-x"},
		{Kind: SeatKindProvider, Raw: "prov/model", ModelID: "prov/model"},
	}
	if _, err := Execute(context.Background(), cfgMixed); err == nil {
		t.Fatal("mixed seats + missing registry must hard-fail")
	}
}

func TestA4_NoSecretsInArtifacts(t *testing.T) {
	// A4 (contract §2.2.4/§2.4): a native seat whose stderr echoes credential
	// material must end up redacted in SeatResult.Error, the seat markdown and
	// summary.json.
	dir := t.TempDir()
	const planted = "sk-planted-artifact-secret-42"
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "p", BaseURL: "https://example.invalid/v1", APIKeyEnv: "ARTIFACT_KEY", Models: []providers.Model{{ID: "m"}}},
	}}
	t.Setenv("ARTIFACT_KEY", planted)

	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:   "review",
		Prompt: "a4",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-x", ModelID: "gemini-x"},
		},
		Root:         dir,
		OutDir:       outDir,
		RegistryPath: regPath,
		TimeoutSec:   15,
		NativeResolver: stubResolver(),
		SpawnCommand: stubSpawn(map[string]string{
			"AGY_PRO_STUB_EXIT":   "3",
			"AGY_PRO_STUB_STDERR": "Bearer " + planted + " and sk-another-generic-token-99 leaked",
		}),
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(summary.Seats) != 1 || summary.Seats[0].Status != "failed" {
		t.Fatalf("expected 1 failed seat, got %+v", summary.Seats)
	}
	if strings.Contains(summary.Seats[0].Error, planted) {
		t.Fatalf("SeatResult.Error leaked the planted key: %q", summary.Seats[0].Error)
	}
	if !strings.Contains(summary.Seats[0].Error, "[REDACTED]") {
		t.Fatalf("SeatResult.Error = %q; want [REDACTED]", summary.Seats[0].Error)
	}

	for _, name := range []string{"summary.json", "reconciliation.md"} {
		b, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(b), planted) {
			t.Errorf("%s leaked the planted key", name)
		}
	}
	seatMD, _ := filepath.Glob(filepath.Join(outDir, "seat-*.md"))
	for _, f := range seatMD {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), planted) {
			t.Errorf("%s leaked the planted key", f)
		}
	}
}

// writeCmdStub writes a Windows batch stub that forwards its arguments to the
// test-binary helper via %* (the pass-2 re-parse that escapeCmdToken
// compensates for) and lets stdin flow through to the helper.
func writeCmdStub(t *testing.T, dir string) string {
	t.Helper()
	helper := os.Args[0]
	body := "@echo off\r\n\"" + helper + "\" -test.run=TestStubAgyProcess -- %*\r\n"
	p := filepath.Join(dir, "agy.cmd")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write cmd stub: %v", err)
	}
	return p
}

// TestCmdWrap_EscapeMatrixRoundTrip (A11, contract §2.2.2 F-Dc) is the fuzz
// round-trip matrix for escapeCmdToken against a REAL cmd.exe. Every token
// must reach the target byte-for-byte through the three passes the wrap
// survives: the cmd /c parse, the batch %* re-parse and the target's own
// CommandLineToArgvW tokenization.
//
// The backslash-before-% rows are the A11 regression: pending backslash runs
// used to be flushed SINGLE before the step-out quote, so `\%` arrived at the
// target as an escaped literal quote and re-framed the whole argument.
func TestCmdWrap_EscapeMatrixRoundTrip(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip(".cmd wrapping is a Windows-only spawn hazard")
	}
	dir := t.TempDir()
	cmdStub := writeCmdStub(t, dir)

	argsDump := filepath.Join(dir, "args.bin")
	t.Setenv("AGY_PRO_STUB_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ARGS_DUMP", argsDump)
	t.Setenv("AGY_PRO_STUB_STDOUT", "STUB_OK")

	passthrough := func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, bin, args...)
	}
	r := &NativeRunner{Resolver: func() (string, error) { return cmdStub, nil }, Spawn: passthrough}

	// The wrap pins delayed expansion off (A11): with it on, `!` inside the
	// seat prompt is expanded away and the prompt does not survive.
	if line := buildBatchCmdLine("bin", []string{"x"}); !strings.HasPrefix(line, `cmd.exe /d /v:off /s /c "`) {
		t.Errorf("batch wrap = %q; want the cmd.exe /d /v:off /s /c prefix", line)
	}

	cases := []string{
		`plain`,
		`with spaces`,
		`back\slash`,
		`trail\`,
		`trail\\`,
		`pct 100%`,
		`pct\%back`,       // A11: single backslash before a percent
		`pct\\%back`,      // A11: doubled backslash before a percent
		`pct\\\%back`,     // A11: tripled backslash before a percent
		`pct%%%%`,         // A11: consecutive percents
		`quote"mid`,       // literal quote
		`quote\"mid`,      // escaped quote
		`back\"quote`,     // A11: backslash before a literal quote
		`back\\"quote`,    // A11: two backslashes before a literal quote
		`back\\\"quote`,   // A11: three backslashes before a literal quote
		`amp & pipe |`,    // cmd metacharacters
		`lt < gt >`,       // cmd redirection metacharacters
		`paren (x)`,       // cmd grouping metacharacters
		`caret ^ here`,    // cmd escape character
		`%VAR%`,           // percent expansion shape
		`!delayed!`,       // delayed-expansion shape (needs /v:off)
		`mix \ & % " ^`,   // everything at once
		`end\`,            // trailing backslash after metacharacters
		`a"b%c\d`,         // quote, percent, backslash adjacent
		`a"\%b`,           // A11 hole: quote, backslash-run, percent (step-out with no quote around it)
		`back"\%end`,      // A11 hole: same, longer
		`q"\\%w`,          // A11 hole: doubled backslash before % after a quote
		`x"\&y`,           // backslash before cmd metachars after a quote
		`z"\^w`,
		`p"\!q`,
		`trail"\`,         // trailing backslash after a literal quote
		`a"\%b"c`,         // step-out emission followed by a literal quote
		`"\%`,             // quote first, then backslash-percent
		`a"\`,
		`tab	and space`, // whitespace variety
	}
	for _, prompt := range cases {
		_ = os.Remove(argsDump)
		out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, prompt, time.Now().Add(30*time.Second))
		if out.SpawnErr != nil {
			t.Errorf("prompt %q: cmd wrap spawn error: %v", prompt, out.SpawnErr)
			continue
		}
		if out.ExitCode != 0 {
			t.Errorf("prompt %q: cmd wrap exit = %d stderr=%s", prompt, out.ExitCode, out.StderrTail)
			continue
		}
		args := argsDumped(t, argsDump)
		if findArg(args, prompt) < 0 {
			t.Errorf("prompt %q did not survive the .cmd argv wrap byte-for-byte:\n got %q", prompt, args)
		}
	}
}

// TestEscapeCmdTokenDoublesBackslashesBeforeQuotes (A11) is the
// platform-independent half of the same fix: a pending backslash run is
// flushed DOUBLED before every emitted quote (step-out quote, escaped quote,
// closing quote), because CommandLineToArgvW reads a single `\` before a `"`
// as an escaped literal quote and re-frames the argument.
func TestEscapeCmdTokenDoublesBackslashesBeforeQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`plain`, `"plain"`},
		{`a"b`, `"a\"b"`},
		{`a\`, `"a\\"`},   // trailing run doubled before the closing quote
		{`a\\`, `"a\\\\"`},
		{`a\"b`, `"a\\\"b"`}, // run before an escaped quote doubled
		{`a\%b`, `"a\\"^%"b"`},
		{`a\\%b`, `"a\\\\"^%"b"`},
		{`100%`, `"100"^%""`},
		// A11: after a literal quote flips cmd parity out, `%` emits `^%`
		// with NO quote around it - the pending run must stay literal
		// (doubling it rewrites the argument at the target).
		{`a"\%b`, `"a\"\^%b"`},
		{`q"\\%w`, `"q\"\\^%w"`},
		{`back"\%end`, `"back\"\^%end"`},
		{`"\%`, `"\"\^%"`},
		{`trail"\`, `"trail\"\\"`}, // trailing run still doubled before the closing quote
	}
	for _, tc := range cases {
		if got := escapeCmdToken(tc.in); got != tc.want {
			t.Errorf("escapeCmdToken(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	// The wrap keeps the hand-escaped tokens behind cmd.exe /d /v:off /s /c
	// and its own outer quotes.
	wantWrap := `cmd.exe /d /v:off /s /c ""bin" "arg""`
	if got := buildBatchCmdLine("bin", []string{"arg"}); got != wantWrap {
		t.Errorf("buildBatchCmdLine = %q; want %q", got, wantWrap)
	}
}

// TestNativeRunner_MultiLineArgvRoundTrip (A4) drives the DEFAULT argv
// transport (engine.go's ViaStdin: false) with a REAL multi-line prompt. The
// pre-existing argv test was single-line only, so nothing asserted that the
// newlines themselves survive the spawn; this is the stub-level round trip
// (a live true-newline probe through a real agy binary is QA scope).
func TestNativeRunner_MultiLineArgvRoundTrip(t *testing.T) {
	dir := t.TempDir()
	argsDump := filepath.Join(dir, "args.bin")
	prompt := "system preset\n\n## Findings\n- **[HIGH]** line three\n- **[LOW]** with \"quotes\", 100% and \\backslash\nlast line"

	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn: stubSpawn(map[string]string{
			"AGY_PRO_STUB_ARGS_DUMP": argsDump,
			"AGY_PRO_STUB_STDOUT":    "OK",
		}),
		// ViaStdin stays false on purpose: the argv path is under test.
	}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "gemini-3.8-flash-high"}, prompt, time.Now().Add(30*time.Second))
	if out.SpawnErr != nil {
		t.Fatalf("spawn error: %v", out.SpawnErr)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit = %d stderr=%s", out.ExitCode, out.StderrTail)
	}
	args := argsDumped(t, argsDump)
	if findArg(args, prompt) < 0 {
		t.Fatalf("multi-line prompt did not survive the argv transport:\n got %q\nwant exact %q", args, prompt)
	}
	// The newlines must be REAL newlines, not escaped literals.
	if findArg(args, `\n`) >= 0 || strings.Contains(strings.Join(args, "\x00"), `\n`) {
		t.Fatalf("argv carries an escaped \\n instead of a real newline: %q", args)
	}
	// argv transport: no bare -p marker (that is the ViaStdin spelling) and
	// the prompt is NOT piped on stdin.
	if pt := findArg(args, "-p"); pt < 0 || pt+1 >= len(args) || args[pt+1] != prompt {
		t.Fatalf("argv must carry -p <prompt> verbatim: %q", args)
	}
}

// TestNativeRunner_ExpiredSpawnCtxMapsToTimeout (A5): a seat that queued
// behind the native-seat semaphore can find its own backstop already expired
// when Start runs. That used to map to "spawn failed"; it is a killed seat, so
// it must map to reason=timeout (degraded).
func TestNativeRunner_ExpiredSpawnCtxMapsToTimeout(t *testing.T) {
	dir := t.TempDir()
	r := &NativeRunner{
		Resolver: stubResolver(),
		Spawn:    stubSpawn(map[string]string{"AGY_PRO_STUB_STDOUT": "never runs"}),
	}
	// Parent ctx already expired AND the swarm deadline already in the past:
	// Start fails with the ctx error, which is NOT an *exec.ExitError.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := r.Run(ctx, RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, "p", time.Now().Add(-time.Minute))
	if !out.TimedOut {
		t.Fatalf("want TimedOut for an expired spawn ctx, got %+v", out)
	}
	res := nativeSeatResult(1, nativeTarget("m"), out, nil)
	if res.Status != "failed" || !res.Degraded {
		t.Fatalf("result = %+v; want failed/degraded", res)
	}
	if !strings.HasPrefix(res.Error, "reason=timeout") {
		t.Fatalf("error = %q; want the reason=timeout mapping (not the spawn-failed one)", res.Error)
	}
	if strings.Contains(res.Error, "spawn failed") {
		t.Fatalf("error = %q; the spawn-failed mapping must not win for a killed seat", res.Error)
	}
}

// A6: a native seat that never starts is a degraded seat. The summary used to
// show "Degraded Mode: false" for it because Degraded was only set on the
// exit-code and timeout branches.
func TestNativeSeat_SpawnFailureIsDegraded(t *testing.T) {
	dir := t.TempDir()
	cfg := RunConfig{
		Task:   "review",
		Prompt: "spawn degrade",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:bad-model", ModelID: "bad-model"},
		},
		Root:           dir,
		OutDir:         filepath.Join(dir, "reports"),
		RegistryPath:   filepath.Join(dir, "missing-providers.json"),
		TimeoutSec:     15,
		NativeResolver: stubResolver(),
		SpawnCommand: func(ctx context.Context, bin string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, filepath.Join(dir, "no-such-agy.exe"), args...)
		},
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !summary.Degraded {
		t.Fatalf("summary.Degraded = false; want true when a seat failed to spawn")
	}
	if len(summary.Seats) != 1 || !summary.Seats[0].Degraded {
		t.Fatalf("spawn-failed seat must be degraded, got %+v", summary.Seats)
	}
	if summary.Seats[0].Status != "failed" || !strings.Contains(summary.Seats[0].Error, "native seat spawn failed") {
		t.Fatalf("seat = %+v; want failed with the spawn-failed mapping", summary.Seats[0])
	}
}

// TestCmdWrap_RoundTripBothTransports (F-Dc, contract §2.2.2): prompt bytes
// must survive the cmd.exe /d /v:off /s /c wrap for BOTH transports — single-line
// argv (cmd/metacharacter escaping) and multi-line (forced ViaStdin). A
// multi-line round-trip failure is a hard failure, not a warning.
//
// The spawn seam here deliberately runs cmd.exe for real (honoring the bin
// argument) so the wrap itself is under test; the .cmd stub then forwards to
// the test-binary helper via %*. Stub markers travel through the parent
// environment (t.Setenv) because the runner scrubs/rebuilds cmd.Env from
// os.Environ() on this path.
func TestCmdWrap_RoundTripBothTransports(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip(".cmd wrapping is a Windows-only spawn hazard")
	}
	dir := t.TempDir()
	cmdStub := writeCmdStub(t, dir)

	argsDump := filepath.Join(dir, "args.bin")
	stdinDump := filepath.Join(dir, "stdin.bin")
	t.Setenv("AGY_PRO_STUB_HELPER", "1")
	t.Setenv("AGY_PRO_STUB_ARGS_DUMP", argsDump)
	t.Setenv("AGY_PRO_STUB_STDIN_DUMP", stdinDump)
	t.Setenv("AGY_PRO_STUB_STDOUT", "STUB_OK")

	passthrough := func(ctx context.Context, bin string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, bin, args...)
	}

	// (1) single-line argv transport: metacharacters + quotes must survive
	// cmd /c parse, batch %* re-parse and CommandLineToArgvW tokenization.
	single := `prompt with & | < > ^ % "quotes" and \back\slashes and (parens)`
	r := &NativeRunner{Resolver: func() (string, error) { return cmdStub, nil }, Spawn: passthrough}
	out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m1"}, single, time.Now().Add(30*time.Second))
	if out.SpawnErr != nil {
		t.Fatalf("cmd wrap spawn error: %v", out.SpawnErr)
	}
	if out.ExitCode != 0 {
		t.Fatalf("cmd wrap exit = %d stderr=%s", out.ExitCode, out.StderrTail)
	}
	args := argsDumped(t, argsDump)
	if findArg(args, single) < 0 {
		t.Fatalf("single-line prompt did not survive .cmd argv wrap:\n got %q\nwant exact %q", args, single)
	}

	// (2) multi-line prompt on a .cmd target FORCES ViaStdin (F-Dc) — the
	// bytes must arrive verbatim on the helper's stdin.
	multi := "line one with & special\nline two with \"quotes\"\nline three %percent%"
	out2 := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m2"}, multi, time.Now().Add(30*time.Second))
	if out2.SpawnErr != nil {
		t.Fatalf("cmd wrap (stdin) spawn error: %v", out2.SpawnErr)
	}
	if out2.ExitCode != 0 {
		t.Fatalf("cmd wrap (stdin) exit = %d stderr=%s", out2.ExitCode, out2.StderrTail)
	}
	got, err := os.ReadFile(stdinDump)
	if err != nil {
		t.Fatalf("read stdin dump: %v", err)
	}
	if string(got) != multi {
		t.Fatalf("multi-line prompt did not survive .cmd ViaStdin wrap:\n got %q\nwant exact %q", got, multi)
	}
}

// FuzzEscapeCmdTokenCmdRoundTrip (A11) is the fuzz half of the round-trip
// matrix: every seed - and, under `go test -fuzz`, every generated prompt -
// must reach the target byte-for-byte through the REAL cmd.exe wrap. The
// curated corpus in TestCmdWrap_EscapeMatrixRoundTrip is the seed set.
func FuzzEscapeCmdTokenCmdRoundTrip(f *testing.F) {
	for _, s := range []string{
		`plain`,
		`a"\%b`,
		`back"\%end`,
		`q"\%w`,
		`a"\%b"c`,
		`"\%`,
		`trail"\`,
		`pct\%back`,
		`pct\%back`,
		`back\"quote`,
		`a"b%c\d`,
		`mix \ & % " ^`,
		`!delayed!`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, prompt string) {
		if runtime.GOOS != "windows" {
			t.Skip(".cmd wrapping is a Windows-only spawn hazard")
		}
		if len(prompt) > 512 {
			t.Skip("keep individual prompts bounded")
		}
		dir := t.TempDir()
		cmdStub := writeCmdStub(t, dir)
		argsDump := filepath.Join(dir, "args.bin")
		t.Setenv("AGY_PRO_STUB_HELPER", "1")
		t.Setenv("AGY_PRO_STUB_ARGS_DUMP", argsDump)
		t.Setenv("AGY_PRO_STUB_STDOUT", "STUB_OK")
		r := &NativeRunner{
			Resolver: func() (string, error) { return cmdStub, nil },
			Spawn: func(ctx context.Context, bin string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, bin, args...)
			},
		}
		out := r.Run(context.Background(), RunConfig{Root: dir}, SeatSpec{Kind: SeatKindNative, ModelID: "m"}, prompt, time.Now().Add(30*time.Second))
		if out.SpawnErr != nil {
			t.Fatalf("prompt %q: spawn error: %v", prompt, out.SpawnErr)
		}
		if out.ExitCode != 0 {
			t.Fatalf("prompt %q: exit = %d stderr = %s", prompt, out.ExitCode, out.StderrTail)
		}
		args := argsDumped(t, argsDump)
		if findArg(args, prompt) < 0 {
			t.Fatalf("prompt %q did not survive the .cmd argv wrap byte-for-byte:\n got %q", prompt, args)
		}
	})
}
