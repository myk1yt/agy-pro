package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// testRegistryWithModels builds the registry shape the launcher routes on:
// 3 enabled models across 2 providers (one model disabled).
func testRegistryWithModels() *providers.Registry {
	disabled := false
	return &providers.Registry{
		Providers: []providers.Provider{
			{
				Name: "alpha",
				Models: []providers.Model{
					{ID: "m1"},
					{ID: "m2"},
					{ID: "m3", Enabled: &disabled},
				},
			},
			{
				Name: "beta",
				Models: []providers.Model{
					{ID: "m1"},
				},
			},
		},
	}
}

// readFromChild reads from the child stream until the first newline arrives
// (or the deadline passes) so a failing relay cannot hang the test suite.
func readFromChild(t *testing.T, r io.Reader, timeout time.Duration) string {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), "\n") {
					ch <- res{sb.String(), nil}
					return
				}
			}
			if err != nil {
				ch <- res{sb.String(), err}
				return
			}
		}
	}()
	select {
	case rr := <-ch:
		if rr.err != nil && rr.s == "" {
			t.Fatalf("child read: %v", rr.err)
		}
		return rr.s
	case <-time.After(timeout):
		t.Fatalf("child received nothing within %v", timeout)
		return ""
	}
}

func TestShowMenuLineFallbackChoices(t *testing.T) {
	cases := []struct {
		input string
		want  Route
		desc  string
	}{
		{"1\n", RouteNative, "explicit default"},
		{"\n", RouteNative, "bare Enter"},
		{"2\n", RouteBridge, "external models"},
		{"3\n", RouteFanout, "swarm"},
		{"2", RouteBridge, "choice without newline (EOF)"},
		{"  2  \n", RouteBridge, "padded choice"},
		{"9\n", RouteNative, "unknown choice falls back to native"},
		{"", RouteNative, "immediate EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			var out strings.Builder
			got := ShowMenu(&out, nil, newStdinRelay(strings.NewReader(tc.input)), 3, "alpha, beta", time.Second)
			if got != tc.want {
				t.Errorf("ShowMenu(%q) = %s; want %s", tc.input, got, tc.want)
			}
			banner := out.String()
			if !strings.Contains(banner, "[1] Native Gemini (default)") {
				t.Errorf("banner missing [1] entry:\n%s", banner)
			}
			if !strings.Contains(banner, "[2] External Models (3 models: alpha, beta)") {
				t.Errorf("banner missing [2] entry with count and providers:\n%s", banner)
			}
			if !strings.Contains(banner, "[3] Swarm (multi-model fanout)") {
				t.Errorf("banner missing [3] entry:\n%s", banner)
			}
		})
	}
}

func TestShowMenuTimesOutToNative(t *testing.T) {
	// A reader that never delivers a line: the deadline must win.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	start := time.Now()
	var out strings.Builder
	got := ShowMenu(&out, nil, newStdinRelay(pr), 3, "alpha, beta", 80*time.Millisecond)
	elapsed := time.Since(start)

	if got != RouteNative {
		t.Errorf("ShowMenu on silent input = %s; want native", got)
	}
	if elapsed < 60*time.Millisecond {
		t.Errorf("ShowMenu returned before the deadline (%v)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("ShowMenu blocked far past the deadline (%v)", elapsed)
	}
}

// F-2 regression: after a menu deadline, the next line the user types must
// reach the real agy child, never a leftover reader.
func TestRelayTimeoutThenSubsequentLineReachesChild(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer srcR.Close()

	rel := newStdinRelay(srcR)
	if _, ok := rel.readLine(60 * time.Millisecond); ok {
		t.Fatal("silent input must time out")
	}
	if !rel.Used() {
		t.Fatal("relay should report the line fallback as used")
	}

	// Typed after the deadline: belongs to the child.
	go func() {
		defer srcW.Close()
		_, _ = srcW.WriteString("hello child\n")
	}()

	child := rel.ChildStdin()
	if got := readFromChild(t, child, 2*time.Second); got != "hello child\n" {
		t.Errorf("child got %q; want %q", got, "hello child\n")
	}
}

// F-2: the partial line the deadline abandoned is re-emitted to the child
// ahead of later input (consumed-prefix replay).
func TestRelayTimeoutReemitsAbandonedPrefix(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer srcR.Close()

	rel := newStdinRelay(srcR)
	_, _ = srcW.WriteString("par") // no newline: an abandoned partial line

	if _, ok := rel.readLine(80 * time.Millisecond); ok {
		t.Fatal("partial line without newline must time out")
	}
	_, _ = srcW.WriteString("tial\n")
	_ = srcW.Close()

	child := rel.ChildStdin()
	var got strings.Builder
	got.WriteString(readFromChild(t, child, 2*time.Second))
	if s := got.String(); !strings.Contains(s, "partial\n") {
		t.Errorf("child got %q; want the abandoned prefix re-emitted as %q", s, "partial\n")
	}
}

// F-2: bytes typed after a consumed choice line (and before the child starts)
// must not stay stranded in the launcher's buffer.
func TestRelayLeftoverAfterChoiceReachesChild(t *testing.T) {
	rel := newStdinRelay(strings.NewReader("2\nhello child\n"))
	line, ok := rel.readLine(time.Second)
	if !ok || line != "2" {
		t.Fatalf("readLine = (%q, %v); want (\"2\", true)", line, ok)
	}
	child := rel.ChildStdin()
	if got := readFromChild(t, child, 2*time.Second); got != "hello child\n" {
		t.Errorf("child got %q; want %q", got, "hello child\n")
	}
}

// F-23: the relay's goroutines terminate when the source reaches EOF - what
// remains after a deadline is the relay itself (feeding the child), not an
// abandoned reader that could steal input.
func TestRelayTerminatesOnSourceEOF(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer srcR.Close()

	rel := newStdinRelay(srcR)
	if _, ok := rel.readLine(50 * time.Millisecond); ok {
		t.Fatal("expected timeout")
	}
	_, _ = srcW.WriteString("late\n")
	_ = srcW.Close()

	child := rel.ChildStdin()
	if got := readFromChild(t, child, 2*time.Second); got != "late\n" {
		t.Errorf("child got %q; want %q", got, "late\n")
	}
	if !rel.waitDone(2 * time.Second) {
		t.Fatal("relay goroutines did not terminate after source EOF")
	}
}

func TestReadLineChoiceDeadline(t *testing.T) {
	srcR, srcW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer srcR.Close()
	defer srcW.Close()

	rel := newStdinRelay(srcR)
	if got, ok := rel.readLine(50 * time.Millisecond); ok || got != "" {
		t.Errorf("readLine on silent input = (%q, %v); want (\"\", false)", got, ok)
	}
}

func TestReadLineChoicePicksUpLateWrites(t *testing.T) {
	rel := newStdinRelay(strings.NewReader("hello world\n"))
	got, ok := rel.readLine(time.Second)
	if !ok || got != "hello world" {
		t.Errorf("readLine = (%q, %v); want \"hello world\"", got, ok)
	}
}

func TestReadLineChoiceTrims(t *testing.T) {
	rel := newStdinRelay(strings.NewReader("   3   \r\n"))
	got, ok := rel.readLine(time.Second)
	if !ok || got != "3" {
		t.Errorf("readLine = (%q, %v); want \"3\"", got, ok)
	}
}

func TestIsTerminalOnPipe(t *testing.T) {
	// os.Pipe files are not character devices.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) {
		t.Error("pipe reported as terminal")
	}
	if isTerminal(nil) {
		t.Error("nil file reported as terminal")
	}
}

// ---- fanout path isolation (F3) -------------------------------------------
//
// runSwarmFromMenu feeds a real fanout.Run, which loads the providers
// registry, writes state-fanout.json and logs/ under the agy-pro home, posts
// to every registered model endpoint, and drops reports under
// reports/fanout/<ts> relative to the CWD. Without isolation, a test process
// whose environment carries real API keys (a fresh Windows terminal seeds them
// from HKCU) would run a paid multi-seat swarm and write outside the test
// tree. Every sink below is moved into t.TempDir(), and the registry exposes
// exactly one mock-server provider with a throwaway key, so no real credential
// and no real endpoint is reachable.

const swarmTestKeyName = "W6_FANOUT_TEST_KEY"

// swarmSandbox holds the isolated locations the fanout side effects land in.
type swarmSandbox struct {
	home    string // AGY_PRO_HOME: state-fanout.json and logs/
	workDir string // CWD during the test: reports/fanout/<ts>
	pkgDir  string // original CWD; must stay free of reports/
	// leakSnapshot captures the real ~/.agy-pro sinks before the run so
	// assertNoSwarmLeak can detect any change, including on machines that
	// already have fanout output from real usage.
	leakSnapshot map[string]fileFingerprint
}

// fileFingerprint is enough to tell whether a path was touched by a test run.
type fileFingerprint struct {
	exists bool
	size   int64
	modAt  time.Time
}

func fingerprint(path string) fileFingerprint {
	fi, err := os.Stat(path)
	if err != nil {
		return fileFingerprint{}
	}
	return fileFingerprint{exists: true, size: fi.Size(), modAt: fi.ModTime()}
}

// isolateSwarmTest points AGY_PRO_HOME and AGY_PRO_PROVIDERS at a sandbox and
// runs the test with a sandbox CWD. The dummy registry has one provider whose
// base URL is mockURL (must be a local stub) and whose key variable is the
// throwaway swarmTestKeyName.
func isolateSwarmTest(t *testing.T, mockURL string) *swarmSandbox {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}

	regPath := filepath.Join(root, "providers.json")
	regJSON := fmt.Sprintf(
		`{"providers":[{"name":"iso","baseURL":%q,"apiKeyEnv":%q,"models":[{"id":"iso-model","family":"iso-fam"}]}]}`,
		mockURL, swarmTestKeyName)
	if err := os.WriteFile(regPath, []byte(regJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(paths.EnvAgyProHome, home)
	t.Setenv(providers.EnvProviders, regPath)
	t.Setenv(swarmTestKeyName, "fanout-test-dummy-key")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	realHome := homeDir(t)
	return &swarmSandbox{
		home:    home,
		workDir: workDir,
		pkgDir:  oldWD,
		leakSnapshot: map[string]fileFingerprint{
			filepath.Join(realHome, "state-fanout.json"): fingerprint(filepath.Join(realHome, "state-fanout.json")),
			filepath.Join(realHome, "logs"):              fingerprint(filepath.Join(realHome, "logs")),
			filepath.Join(oldWD, "reports"):              fingerprint(filepath.Join(oldWD, "reports")),
		},
	}
}

// swarmMockServer answers every chat completion with one final finding and
// records the requests, so a test can prove the swarm really ran - against
// localhost only, with the throwaway key - instead of dying before any seat.
func swarmMockServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s %s auth=%q body=%s",
			r.Method, r.URL.Path, r.Header.Get("Authorization"), string(body)))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "## Findings\n- **[INFO]** seat finished"},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// assertSwarmCompleted requires the artifacts only a finished swarm writes:
// the rotation state with a success seat record, and the report tree. This is
// what separates "fanout ran to completion" from "fanout failed at target
// selection and the test still passed" (the old handled==true assertion).
func assertSwarmCompleted(t *testing.T, sandbox *swarmSandbox, wantModel string) {
	t.Helper()

	statePath := filepath.Join(sandbox.home, "state-fanout.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state-fanout.json missing - swarm never finished: %v", err)
	}
	var state struct {
		History []struct {
			Task  string `json:"task"`
			Seats []struct {
				Model  string `json:"model"`
				Status string `json:"status"`
			} `json:"seats"`
		} `json:"history"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("state-fanout.json unreadable: %v\n%s", err, raw)
	}
	if len(state.History) != 1 || len(state.History[0].Seats) != 1 {
		t.Fatalf("state history = %s; want one run with one seat", raw)
	}
	seat := state.History[0].Seats[0]
	if seat.Model != wantModel || seat.Status != "success" {
		t.Fatalf("seat record = %+v; want model %q status success", seat, wantModel)
	}

	matches, err := filepath.Glob(filepath.Join(sandbox.workDir, "reports", "fanout", "*", "summary.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one sandboxed summary.json (err=%v matches=%v)", err, matches)
	}
	sumRaw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	var summary struct {
		NCompleted int `json:"n_completed"`
		NRequested int `json:"n_requested"`
		Seats      []struct {
			Status string `json:"status"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(sumRaw, &summary); err != nil {
		t.Fatalf("summary.json unreadable: %v\n%s", err, sumRaw)
	}
	if summary.NCompleted != 1 || summary.NRequested != 1 {
		t.Fatalf("summary counts = completed %d / requested %d; want 1/1", summary.NCompleted, summary.NRequested)
	}
	if len(summary.Seats) != 1 || summary.Seats[0].Status != "success" {
		t.Fatalf("summary seats = %s; want one success seat", sumRaw)
	}

	// Nothing may escape the sandbox.
	assertNoSwarmLeak(t, sandbox)
}

// assertNoSwarmLeak verifies the swarm did not create or modify anything in
// the real user home or in the package tree (the original CWD). Comparison is
// against a snapshot taken before the run, so machines with pre-existing
// fanout output from real usage do not false-positive.
func assertNoSwarmLeak(t *testing.T, sandbox *swarmSandbox) {
	t.Helper()
	for path, before := range sandbox.leakSnapshot {
		after := fingerprint(path)
		if before != after {
			t.Errorf("fanout side effect escaped the sandbox: %s changed (%+v -> %+v)", path, before, after)
		}
	}
}

// homeDir resolves the home the swarm would use if isolation were missing.
func homeDir(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no user home: %v", err)
	}
	return filepath.Join(h, ".agy-pro")
}

func TestSharedReaderServesChoiceThenPrompt(t *testing.T) {
	// The menu choice and a follow-up swarm prompt must draw from one owner:
	// eagerly typed "3\nreview the diff\n" must not strand the prompt line.
	// The swarm runs to completion against a localhost mock inside a sandbox
	// (F3): the old assertions passed just as happily when fanout died at
	// target selection, so seat/state/report artifacts are required here.
	server, requests := swarmMockServer(t)
	sandbox := isolateSwarmTest(t, server.URL)

	rel := newStdinRelay(strings.NewReader("3\nreview the diff\n"))
	var out strings.Builder
	if got := ShowMenu(&out, nil, rel, 3, "alpha, beta", time.Second); got != RouteFanout {
		t.Fatalf("ShowMenu = %s; want fanout", got)
	}

	var promptOut, promptErr strings.Builder
	code, handled, err := runSwarmFromMenu(rel, &promptOut, &promptErr)
	if err != nil {
		t.Fatalf("runSwarmFromMenu: %v", err)
	}
	if !handled {
		t.Fatal("runSwarmFromMenu reported unhandled (prompt line was stranded)")
	}
	if code != 0 {
		t.Fatalf("fanout exit code = %d (instant failure, not a completed run)\nstderr:\n%s\nstdout:\n%s",
			code, promptErr.String(), promptOut.String())
	}
	if strings.Contains(promptErr.String(), "Fanout error") {
		t.Fatalf("fanout error must not be swallowed by the test:\n%s", promptErr.String())
	}
	if !strings.Contains(promptOut.String(), "Seats Evaluated:       1 / 1") {
		t.Errorf("fanout summary missing a completed seat:\n%s", promptOut.String())
	}

	// The stranded-prompt proof: the second line must reach the swarm body.
	reqs := requests()
	if len(reqs) == 0 {
		t.Fatal("mock server received no request; the swarm never ran")
	}
	if !strings.Contains(strings.Join(reqs, "\n"), "review the diff") {
		t.Errorf("swarm prompt never reached the seat requests (shared reader lost it):\n%s", strings.Join(reqs, "\n"))
	}
	if !strings.Contains(strings.Join(reqs, "\n"), "Bearer fanout-test-dummy-key") {
		t.Errorf("seat request did not use the throwaway key:\n%s", strings.Join(reqs, "\n"))
	}

	assertSwarmCompleted(t, sandbox, "iso-model")
}

func TestRunSwarmFromMenuEmptyPromptCancels(t *testing.T) {
	// Isolated (F3) and wired to a live mock: if the cancel ever stops
	// short-circuiting, the swarm would run and leave the artifacts the
	// assertions below reject.
	server, requests := swarmMockServer(t)
	sandbox := isolateSwarmTest(t, server.URL)

	rel := newStdinRelay(strings.NewReader("\n"))
	var out, errw strings.Builder
	code, handled, err := runSwarmFromMenu(rel, &out, &errw)
	if err != nil {
		t.Fatalf("runSwarmFromMenu: %v", err)
	}
	if handled {
		t.Error("empty prompt must cancel back to native (handled=false)")
	}
	if code != 0 {
		t.Errorf("code = %d; want 0", code)
	}
	if !strings.Contains(errw.String(), "empty swarm prompt") {
		t.Errorf("expected cancel notice, got %q", errw.String())
	}

	// fanout.Run must never have been reached: a completed run would write
	// state and reports even with a perfect mock.
	if n := len(requests()); n != 0 {
		t.Errorf("empty prompt reached the model endpoint (%d requests)", n)
	}
	if _, err := os.Stat(filepath.Join(sandbox.home, "state-fanout.json")); err == nil {
		t.Error("empty prompt must cancel before fanout.Run; state-fanout.json was created")
	}
	if _, err := os.Stat(filepath.Join(sandbox.workDir, "reports")); err == nil {
		t.Error("empty prompt must cancel before fanout.Run; reports/ was created")
	}
	assertNoSwarmLeak(t, sandbox)
}

func TestRunSwarmFromMenuInstantFailCreatesNoState(t *testing.T) {
	// Contrast for the completion assertions (F3 secondary): with no usable
	// key the swarm dies at target selection, fanout.Run returns 1, and
	// nothing is written. "handled == true" alone can never tell this case
	// apart from a finished run - the artifacts can.
	sandbox := isolateSwarmTest(t, "http://127.0.0.1:1") // unreachable stub; must never be dialed
	t.Setenv(swarmTestKeyName, "")                       // empty counts as unset

	rel := newStdinRelay(strings.NewReader("review the diff\n"))
	var out, errw strings.Builder
	code, handled, err := runSwarmFromMenu(rel, &out, &errw)
	if err != nil {
		t.Fatalf("runSwarmFromMenu: %v", err)
	}
	if !handled {
		t.Fatal("non-empty prompt must be handled")
	}
	if code == 0 {
		t.Fatalf("keyless registry must fail the swarm, got exit 0\nstdout:\n%s", out.String())
	}
	if !strings.Contains(errw.String(), "Fanout error") {
		t.Errorf("expected the fanout failure on stderr, got %q", errw.String())
	}
	if _, err := os.Stat(filepath.Join(sandbox.home, "state-fanout.json")); err == nil {
		t.Error("instant failure must not write state-fanout.json")
	}
	if _, err := os.Stat(filepath.Join(sandbox.workDir, "reports")); err == nil {
		t.Error("instant failure must not write reports/")
	}
	assertNoSwarmLeak(t, sandbox)
}
