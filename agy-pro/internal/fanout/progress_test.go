package fanout

// Streaming per-seat progress (engine seatProgressEmitter / progress.log).
// Pins:
//   - one start line and one finish line per seat, seat ids and a parseable
//     grammar (a mid-line interleave from the concurrent seats fails the match);
//   - progress.log in the artifact dir carries the SAME lines, and exists from
//     run start (streaming, not buffered to the end);
//   - --json stdout stays pure JSON (progress rides stderr), terminal stdout
//     streams the lines;
//   - thin / degraded / findings markers render per the documented format.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/providers"
)

// progressStartRe / progressFinishRe are the pinned line grammar. Any garbled
// or interleaved write (the seats run concurrently) fails to match.
var (
	progressStartRe  = regexp.MustCompile(`^\[seat (\d+)/(\d+)\] (\S+) running$`)
	progressFinishRe = regexp.MustCompile(`^\[seat (\d+)/(\d+)\] (\S+) -> (success|failed|degraded) \(\d+\.\d+s(, findings: \d+)?(, thin)?(, degraded)?\)$`)
)

// progressSink records every Write it receives. The engine must serialize whole
// lines under its mutex, so each Write has to be exactly one complete line.
type progressSink struct {
	mu      sync.Mutex
	writes  []string
	badLine string
}

func (s *progressSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := string(p)
	if s.badLine == "" {
		if !strings.HasSuffix(w, "\n") || strings.Contains(strings.TrimSuffix(w, "\n"), "\n") {
			s.badLine = w
		}
	}
	s.writes = append(s.writes, w)
	return len(p), nil
}

func (s *progressSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.writes, "")
}

// splitProgressLines classifies the recorded lines and fails the test on any
// line that matches neither grammar.
func splitProgressLines(t *testing.T, content string) (starts, finishes []string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
		if line == "" {
			continue
		}
		switch {
		case progressStartRe.MatchString(line):
			starts = append(starts, line)
		case progressFinishRe.MatchString(line):
			finishes = append(finishes, line)
		default:
			t.Errorf("progress line does not match the pinned grammar: %q", line)
		}
	}
	return starts, finishes
}

// seatIDsOf extracts the seat ids from lines matching re.
func seatIDsOf(t *testing.T, lines []string, re *regexp.Regexp) []int {
	t.Helper()
	var ids []int
	for _, line := range lines {
		m := re.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %q does not match %v", line, re)
		}
		id, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("seat id in %q: %v", line, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// assertEachSeatOnce checks that ids contains each of 1..total exactly once.
func assertEachSeatOnce(t *testing.T, what string, ids []int, total int) {
	t.Helper()
	seen := map[int]int{}
	for _, id := range ids {
		seen[id]++
	}
	for id := 1; id <= total; id++ {
		if seen[id] != 1 {
			t.Errorf("%s: seat %d appears %d times; want exactly 1 (%v)", what, id, seen[id], ids)
		}
	}
}

// stubProgressRun stands up a 3-provider registry on one stub chat server and
// runs a legacy (rotation-selected) provider swarm with the given progress
// sink. Returns the run's artifact directory.
func stubProgressRun(t *testing.T, content string, sink io.Writer) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "PROG_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "PROG_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal}}},
		{Name: "prov-c", BaseURL: server.URL, APIKeyEnv: "PROG_KEY_C",
			Models: []providers.Model{{ID: "model-c", Family: "family-c", Enabled: &trueVal}}},
	}}
	t.Setenv("PROG_KEY_A", "key-a")
	t.Setenv("PROG_KEY_B", "key-b")
	t.Setenv("PROG_KEY_C", "key-c")

	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:         "review",
		Prompt:       "progress pin",
		N:            3,
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
	if summary.NCompleted != 3 {
		t.Fatalf("NCompleted = %d; want 3", summary.NCompleted)
	}
	return outDir
}

func TestProgressStreamsOneStartAndOneFinishPerSeat(t *testing.T) {
	sink := &progressSink{}
	stubProgressRun(t, "## Findings\n- **[LOW]** one finding", sink)

	if sink.badLine != "" {
		t.Fatalf("engine wrote a non-atomic line (mutex gap): %q", sink.badLine)
	}

	starts, finishes := splitProgressLines(t, sink.String())
	if len(starts) != 3 {
		t.Fatalf("start lines = %d (%q); want exactly 3", len(starts), sink.String())
	}
	if len(finishes) != 3 {
		t.Fatalf("finish lines = %d (%q); want exactly 3", len(finishes), sink.String())
	}
	assertEachSeatOnce(t, "start lines", seatIDsOf(t, starts, progressStartRe), 3)
	assertEachSeatOnce(t, "finish lines", seatIDsOf(t, finishes, progressFinishRe), 3)
	// The stub answer carries one finding per seat, so every finish line
	// reports it (and none is "thin").
	for _, line := range finishes {
		if !strings.Contains(line, "findings: 1") {
			t.Errorf("finish line missing findings count: %q", line)
		}
		if strings.Contains(line, "thin") {
			t.Errorf("finish line must not be thin with a finding present: %q", line)
		}
		if strings.Contains(line, "degraded") {
			t.Errorf("finish line must not be degraded on a healthy seat: %q", line)
		}
	}
}

func TestProgressLogMirrorsStreamAndExistsAtRunStart(t *testing.T) {
	sink := &progressSink{}
	outDir := stubProgressRun(t, "## Findings\n- **[LOW]** one finding", sink)

	logPath := filepath.Join(outDir, "progress.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("progress.log not created in the artifact dir: %v", err)
	}
	if string(data) != sink.String() {
		t.Fatalf("progress.log does not mirror the stream:\nlog:\n%s\nstream:\n%s", data, sink.String())
	}
	if !strings.Contains(string(data), "[seat 1/3] ") || !strings.Contains(string(data), "running") {
		t.Errorf("progress.log missing start lines:\n%s", data)
	}
}

// TestProgressLogStreamsBeforeSeatsFinish pins the streaming requirement: with
// every seat blocked mid-run, progress.log already holds the start lines. A
// buffered-to-the-end implementation writes nothing until wg.Wait returns and
// fails this pin.
func TestProgressLogStreamsBeforeSeatsFinish(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[LOW]** late"},
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
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "STREAM_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "STREAM_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal}}},
	}}
	t.Setenv("STREAM_KEY_A", "key-a")
	t.Setenv("STREAM_KEY_B", "key-b")
	regPath := filepath.Join(dir, "providers.json")
	writeRegistry(t, regPath, reg)

	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:         "review",
		Prompt:       "streaming pin",
		N:            2,
		Root:         dir,
		OutDir:       outDir,
		RegistryPath: regPath,
		TimeoutSec:   30,
	}

	done := make(chan error, 1)
	go func() {
		_, err := Execute(context.Background(), cfg)
		done <- err
	}()

	// Both seats are blocked inside the chat handler: only start lines can
	// exist yet, and they must already be on disk.
	logPath := filepath.Join(outDir, "progress.log")
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := os.ReadFile(logPath)
		if err == nil && strings.Count(string(data), " running") == 2 {
			break
		}
		if time.Now().After(deadline) {
			unblock()
			t.Fatalf("progress.log did not stream start lines while seats were still running (last read err=%v)", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	unblock()
	if err := <-done; err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("progress.log after the run: %v", err)
	}
	if strings.Count(string(data), "running") != 2 || strings.Count(string(data), "-> success") != 2 {
		t.Fatalf("progress.log missing finish lines after the seats completed:\n%s", data)
	}
}

// nativeProgressRun runs an all-native --seats swarm through the stub
// subprocess seams with the given stub env and progress sink.
func nativeProgressRun(t *testing.T, stubEnv map[string]string, sink io.Writer) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(providers.EnvHome, dir)
	outDir := filepath.Join(dir, "reports")
	cfg := RunConfig{
		Task:   "review",
		Prompt: "native progress",
		Seats: []SeatSpec{
			{Kind: SeatKindNative, Raw: "native:gemini-x", ModelID: "gemini-x"},
		},
		Root:           dir,
		OutDir:         outDir,
		RegistryPath:   filepath.Join(dir, "no-such-providers.json"), // F-REG tolerant
		TimeoutSec:     30,
		NativeResolver: stubResolver(),
		SpawnCommand:   stubSpawn(stubEnv),
		ProgressOut:    sink,
	}
	summary, err := Execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if summary.NRequested != 1 {
		t.Fatalf("seats requested = %d; want 1", summary.NRequested)
	}
	return outDir
}

func TestProgressStreamsNativeSeatLines(t *testing.T) {
	sink := &progressSink{}
	nativeProgressRun(t, map[string]string{
		"AGY_PRO_STUB_STDOUT": "## Findings\n- **[HIGH]** native finding",
	}, sink)

	if sink.badLine != "" {
		t.Fatalf("engine wrote a non-atomic line (mutex gap): %q", sink.badLine)
	}
	if !strings.Contains(sink.String(), "[seat 1/1] native:gemini-x running\n") {
		t.Errorf("missing native start line:\n%s", sink.String())
	}
	if !strings.Contains(sink.String(), "[seat 1/1] native:gemini-x -> success (") {
		t.Errorf("missing native finish line:\n%s", sink.String())
	}
	starts, finishes := splitProgressLines(t, sink.String())
	if len(starts) != 1 || len(finishes) != 1 {
		t.Fatalf("native progress lines = %d starts / %d finishes; want 1/1:\n%s",
			len(starts), len(finishes), sink.String())
	}
	if !strings.Contains(finishes[0], "findings: 1") {
		t.Errorf("native finish line missing findings: %q", finishes[0])
	}
}

// overlapWriter widens the write window and counts concurrent entries: the
// emitter must serialize whole lines under its mutex, so a second writer
// inside one Write is a mutex gap.
type overlapWriter struct {
	mu       sync.Mutex
	inWrite  int
	overlaps int
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.inWrite++
	if w.inWrite > 1 {
		w.overlaps++
	}
	w.mu.Unlock()
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	w.inWrite--
	w.mu.Unlock()
	return len(p), nil
}

// TestProgressEmitterSerializesConcurrentEmits pins requirement 4 (mutex
// around the writer): seats run concurrently, so N goroutines released by a
// barrier must still reach the sink one complete line at a time, and the log
// file must carry every line exactly once.
func TestProgressEmitterSerializesConcurrentEmits(t *testing.T) {
	dir := t.TempDir()
	w := &overlapWriter{}
	p := newProgressEmitter(filepath.Join(dir, "reports"), w)
	defer p.close()

	const seats = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 1; i <= seats; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			tgt := providers.Target{
				Provider: providers.Provider{Name: "prov-a"},
				Model:    providers.Model{ID: "model-a"},
			}
			p.seatStarted(id, seats, SeatKindProvider, tgt)
			p.seatFinished(id, seats, SeatKindProvider, tgt, &SeatResult{
				SeatID:     id,
				Kind:       SeatKindProvider,
				Status:     "success",
				Content:    "## Findings\n- **[LOW]** x",
				DurationMs: int64(id),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	if w.overlaps > 0 {
		t.Errorf("%d writes reached the sink concurrently; the emitter must serialize lines under its mutex", w.overlaps)
	}
	data, err := os.ReadFile(filepath.Join(dir, "reports", "progress.log"))
	if err != nil {
		t.Fatalf("progress.log: %v", err)
	}
	if got := strings.Count(string(data), " running"); got != seats {
		t.Errorf("progress.log start lines = %d; want %d", got, seats)
	}
	if got := strings.Count(string(data), "-> success"); got != seats {
		t.Errorf("progress.log finish lines = %d; want %d", got, seats)
	}
	starts, finishes := splitProgressLines(t, string(data))
	if len(starts) != seats || len(finishes) != seats {
		t.Errorf("progress.log grammar: %d starts / %d finishes; want %d/%d", len(starts), len(finishes), seats, seats)
	}
}

func TestProgressThinAndDegradedMarkers(t *testing.T) {
	// Thin: the seat succeeded but reported zero findings.
	sink := &progressSink{}
	nativeProgressRun(t, map[string]string{
		"AGY_PRO_STUB_STDOUT": "I explored the workspace and have nothing to report.",
	}, sink)
	_, finishes := splitProgressLines(t, sink.String())
	if len(finishes) != 1 {
		t.Fatalf("finish lines = %d; want 1:\n%s", len(finishes), sink.String())
	}
	if !strings.Contains(finishes[0], "-> success (") || !strings.HasSuffix(finishes[0], ", thin)") {
		t.Errorf("thin seat finish line = %q; want success with the thin marker", finishes[0])
	}
	if strings.Contains(finishes[0], "findings:") {
		t.Errorf("thin seat must not claim findings: %q", finishes[0])
	}

	// Degraded: the seat failed (nonzero exit).
	sink = &progressSink{}
	nativeProgressRun(t, map[string]string{
		"AGY_PRO_STUB_EXIT":   "3",
		"AGY_PRO_STUB_STDERR": "boom",
	}, sink)
	_, finishes = splitProgressLines(t, sink.String())
	if len(finishes) != 1 {
		t.Fatalf("finish lines = %d; want 1:\n%s", len(finishes), sink.String())
	}
	if !strings.Contains(finishes[0], "-> failed (") || !strings.HasSuffix(finishes[0], ", degraded)") {
		t.Errorf("degraded seat finish line = %q; want failed with the degraded marker", finishes[0])
	}
}

// TestRunJSONModeKeepsStdoutPure drives the real CLI entry (Run) with -json:
// the swarm builder consumes that stdout as JSON, so progress must ride stderr.
func TestRunJSONModeKeepsStdoutPure(t *testing.T) {
	out, errw := runFanoutCLIWithStub(t, true)
	if !strings.Contains(errw.String(), "[seat 1/2] prov-a/model-a running") {
		t.Errorf("--json stderr missing the progress stream:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "[seat 2/2] prov-b/model-b -> success (") {
		t.Errorf("--json stderr missing a finish line:\n%s", errw.String())
	}

	var summary SummaryReport
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("--json stdout is not parseable JSON: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "[seat ") {
		t.Errorf("progress lines leaked into --json stdout:\n%s", out.String())
	}
	if summary.NCompleted != 2 {
		t.Errorf("NCompleted = %d; want 2", summary.NCompleted)
	}
}

// TestRunTerminalModeStreamsProgressToStdout: the default (no --json) run
// streams the same lines to stdout next to the final banner.
func TestRunTerminalModeStreamsProgressToStdout(t *testing.T) {
	out, _ := runFanoutCLIWithStub(t, false)
	if !strings.Contains(out.String(), "[seat 1/2] prov-a/model-a running") {
		t.Errorf("terminal stdout missing the progress stream:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[seat 2/2] prov-b/model-b -> success (") {
		t.Errorf("terminal stdout missing a finish line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Fanout Swarm Complete") {
		t.Errorf("terminal stdout missing the final banner:\n%s", out.String())
	}
}

// runFanoutCLIWithStub runs the fanout CLI (Run) against a 2-provider stub
// registry placed in the agy-pro home, returning the captured stdout/stderr.
func runFanoutCLIWithStub(t *testing.T, jsonMode bool) (out, errw *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{Choices: []ChatChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: "## Findings\n- **[MEDIUM]** cli finding"},
			FinishReason: "stop",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(server.Close)

	home := t.TempDir()
	t.Setenv(providers.EnvHome, home)
	trueVal := true
	reg := &providers.Registry{Providers: []providers.Provider{
		{Name: "prov-a", BaseURL: server.URL, APIKeyEnv: "CLI_KEY_A",
			Models: []providers.Model{{ID: "model-a", Family: "family-a", Enabled: &trueVal}}},
		{Name: "prov-b", BaseURL: server.URL, APIKeyEnv: "CLI_KEY_B",
			Models: []providers.Model{{ID: "model-b", Family: "family-b", Enabled: &trueVal}}},
	}}
	t.Setenv("CLI_KEY_A", "key-a")
	t.Setenv("CLI_KEY_B", "key-b")
	writeRegistry(t, filepath.Join(home, "providers.json"), reg)

	args := []string{
		"-prompt", "cli progress",
		"-seats", "prov-a/model-a,prov-b/model-b",
		"-root", home,
		"-timeout", "30",
		"-out", filepath.Join(home, "reports"),
	}
	if jsonMode {
		args = append(args, "-json")
	}
	out, errw = &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(args, nil, out, errw); code != 0 {
		t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
	}
	return out, errw
}
