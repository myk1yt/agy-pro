package mesh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/paths"
	"agypro/internal/wireadp"
)

// ---------------------------------------------------------------------------
// helper child processes (bounded, headless, never interactive)
// ---------------------------------------------------------------------------

// helperChild is a spawned helper process plus the files it uses to prove it
// ran and stayed alive.
type helperChild struct {
	cmd  *exec.Cmd
	hb   string // heartbeat file (sleep modes)
	file string // marker file (touch mode)
}

// newHelperChild builds the helper command for one mode. Every helper is
// bounded (a hard deadline plus a -test.timeout) and holds no terminal: it is
// a plain child of the test binary, so no window is ever created.
func newHelperChild(t *testing.T, mode string, dur time.Duration) helperChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMeshHelperChild$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "GO_MESH_HELPER_MODE="+mode)
	h := helperChild{cmd: cmd}
	switch mode {
	case "touch":
		h.file = filepath.Join(t.TempDir(), "marker")
		cmd.Env = append(cmd.Env, "GO_MESH_HELPER_FILE="+h.file)
	case "sleep":
		h.hb = filepath.Join(t.TempDir(), "heartbeat")
		cmd.Env = append(cmd.Env, "GO_MESH_HELPER_HB="+h.hb)
	case "sleep-for":
		h.hb = filepath.Join(t.TempDir(), "heartbeat")
		cmd.Env = append(cmd.Env,
			"GO_MESH_HELPER_HB="+h.hb,
			"GO_MESH_HELPER_SECONDS="+strconv.Itoa(int(dur.Seconds())),
		)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return h
}

// newForeignAdapterChild builds the "another process owns a live adapter"
// helper: it starts a real wireadp under childHome, records its URL, and
// holds it open until killed. It returns the command and the URL file to
// wait on. childHome is where the helper's discovery record lands, so a
// caller can plant a live sibling record inside the shared scope directory.
func newForeignAdapterChild(t *testing.T, childHome string) (*exec.Cmd, string) {
	t.Helper()
	urlFile := filepath.Join(t.TempDir(), "foreign.url")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMeshHelperChild$", "-test.timeout=120s")
	cmd.Env = append(envWithout(paths.EnvAgyProHome),
		"GO_MESH_HELPER_MODE=wireadp-foreign",
		"GO_MESH_FOREIGN_URLFILE="+urlFile,
		"GO_MESH_FOREIGN_AUDIT="+filepath.Join(childHome, "foreign-audit.jsonl"),
		paths.EnvAgyProHome+"="+childHome,
	)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd, urlFile
}

// envWithout returns the process environment with every entry named in names
// dropped, so an override appended afterwards is the only one the child sees.
func envWithout(names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		if eq := strings.IndexByte(kv, '='); eq > 0 && drop[kv[:eq]] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// TestMeshHelperChild is the bounded helper process the supervision tests
// spawn. It is a no-op unless GO_MESH_HELPER_MODE selects a behavior, so the
// parent's own test run never enters it.
func TestMeshHelperChild(t *testing.T) {
	switch os.Getenv("GO_MESH_HELPER_MODE") {
	case "":
		return
	case "exit0":
		os.Exit(0)
	case "exit3":
		os.Exit(3)
	case "touch":
		if p := os.Getenv("GO_MESH_HELPER_FILE"); p != "" {
			_ = os.WriteFile(p, []byte("ran"), 0o600)
		}
		os.Exit(0)
	case "sleep":
		sleepWithHeartbeat(os.Getenv("GO_MESH_HELPER_HB"), 90*time.Second)
		os.Exit(0)
	case "sleep-for":
		seconds, err := strconv.Atoi(os.Getenv("GO_MESH_HELPER_SECONDS"))
		if err != nil || seconds <= 0 {
			fmt.Fprintln(os.Stderr, "mesh helper: bad GO_MESH_HELPER_SECONDS")
			os.Exit(2)
		}
		sleepWithHeartbeat(os.Getenv("GO_MESH_HELPER_HB"), time.Duration(seconds)*time.Second)
		os.Exit(0)
	case "wireadp-foreign":
		runForeignAdapter(t)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "mesh helper: unknown mode %q\n", os.Getenv("GO_MESH_HELPER_MODE"))
		os.Exit(2)
	}
}

// sleepWithHeartbeat keeps the helper alive for a bounded duration, touching
// the heartbeat file every 100ms so a test can prove the process lives (and,
// after a kill, that it stopped).
func sleepWithHeartbeat(hb string, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if hb != "" {
			_ = os.WriteFile(hb, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runForeignAdapter is the helper body that stands in for "another process
// owns a live adapter": a real wireadp instance recorded in the sandbox home.
func runForeignAdapter(t *testing.T) {
	srv, err := wireadp.Start(wireadp.Config{
		Registry:  testRegistry(),
		AuditPath: os.Getenv("GO_MESH_FOREIGN_AUDIT"),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mesh helper: foreign adapter start:", err)
		os.Exit(3)
	}
	if p := os.Getenv("GO_MESH_FOREIGN_URLFILE"); p != "" {
		_ = os.WriteFile(p, []byte(srv.URL()), 0o600)
	}
	deadline := time.Now().Add(85 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	_ = srv.Stop()
	os.Exit(0)
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

// swapStartChild replaces the cmd.Start seam for one test and returns a
// function that puts the real one back.
func swapStartChild(t *testing.T, fn func(*exec.Cmd) error) func() {
	t.Helper()
	prev := startChild
	startChild = fn
	restore := func() { startChild = prev }
	t.Cleanup(restore)
	return restore
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

func hbMtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("heartbeat stat: %v", err)
	}
	return info.ModTime()
}

func hbFresh(t *testing.T, path string, maxAge time.Duration) bool {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("heartbeat stat: %v", err)
	}
	return time.Since(info.ModTime()) <= maxAge
}

// ---------------------------------------------------------------------------
// RunChild
// ---------------------------------------------------------------------------

func TestRunChild_ChildExitStopsTheMesh(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))

	// The supervisor must not touch the child's stdio: the TUI child owns the
	// terminal.
	var stdout strings.Builder
	child := newHelperChild(t, "touch", 0)
	child.cmd.Stdout = &stdout
	if err := m.RunChild(context.Background(), child.cmd); err != nil {
		t.Fatalf("RunChild (exit 0 child): %v", err)
	}
	if child.cmd.Stdout != &stdout {
		t.Fatal("the supervisor replaced the child's stdout")
	}
	if _, err := os.Stat(child.file); err != nil {
		t.Fatalf("the child did not run under supervision: %v", err)
	}

	if !m.isStopped() {
		t.Fatal("the mesh must stop when the child exits")
	}
	if err := m.Healthz(context.Background()); err == nil {
		t.Fatal("the mesh port must be closed after the child exits")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop after the child exited must be idempotent: %v", err)
	}
}

func TestRunChild_ChildExitCodePropagates(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))

	child := newHelperChild(t, "exit3", 0)
	err := m.RunChild(context.Background(), child.cmd)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", exitErr.ExitCode())
	}
	if !m.isStopped() {
		t.Fatal("the mesh must stop whatever the child's exit code is")
	}
}

func TestRunChild_MeshDeathKillsTheChildWithAVisibleError(t *testing.T) {
	home, _ := sandboxHome(t)
	var visible bytes.Buffer
	cfg := meshConfig(t, home)
	cfg.ErrWriter = &visible
	cfg.ChildPollInterval = 50 * time.Millisecond
	m := startMesh(t, cfg)

	child := newHelperChild(t, "sleep", 0)
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = m.srv.Stop() // the mesh dies while the child lives
	}()

	err := m.RunChild(context.Background(), child.cmd)
	if !errors.Is(err, ErrMeshDied) {
		t.Fatalf("want ErrMeshDied, got %v", err)
	}
	if !strings.Contains(err.Error(), m.URL()) {
		t.Errorf("the error must name the mesh URL: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("port %d", m.Port())) {
		t.Errorf("the error must name the mesh port: %v", err)
	}

	line := visible.String()
	if !strings.Contains(line, "mesh died mid-session") {
		t.Errorf("the visible line must state the mid-session death: %q", line)
	}
	if !strings.Contains(line, fmt.Sprintf("port %d", m.Port())) {
		t.Errorf("the visible line must name the mesh port: %q", line)
	}

	// The child was killed: its heartbeat stops advancing.
	last := hbMtime(t, child.hb)
	time.Sleep(400 * time.Millisecond)
	if hbMtime(t, child.hb).After(last) {
		t.Fatal("the child must be killed when the mesh dies")
	}

	if !m.isStopped() {
		t.Fatal("the mesh must be stopped after a mid-session death")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop must stay idempotent: %v", err)
	}
}

func TestRunChild_NoTTLSelfExitWhileTheChildLives(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	// A rare poll lets the idle measurement grow past any plausible TTL while
	// the child lives.
	cfg.ChildPollInterval = 3 * time.Second
	m := startMesh(t, cfg)

	child := newHelperChild(t, "sleep-for", 2500*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- m.RunChild(context.Background(), child.cmd) }()

	time.Sleep(1200 * time.Millisecond)
	idle := m.IdleFor()
	if idle < 800*time.Millisecond {
		t.Fatalf("IdleFor = %v, want it to advance while the child lives", idle)
	}
	if err := m.Healthz(context.Background()); err != nil {
		t.Fatalf("the mesh must stay up while the child lives, idle or not: %v", err)
	}
	if !hbFresh(t, child.hb, 500*time.Millisecond) {
		t.Fatal("the child must stay alive while its mesh lives")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunChild: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunChild did not return after the child finished")
	}
	if !m.isStopped() {
		t.Fatal("the mesh must stop once the child finishes")
	}
}

func TestRunChild_RefusesToLaunchOnADeadMesh(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))
	_ = m.srv.Stop() // the mesh is dead before the launch decision

	child := newHelperChild(t, "touch", 0)
	err := m.RunChild(context.Background(), child.cmd)
	if err == nil {
		t.Fatal("RunChild must refuse to launch on a dead mesh")
	}
	if !strings.Contains(err.Error(), "refusing to launch") {
		t.Errorf("the error must read as a refuse-to-launch: %v", err)
	}
	if _, statErr := os.Stat(child.file); statErr == nil {
		t.Fatal("the child must never start on a dead mesh")
	}
	if !m.isStopped() {
		t.Fatal("a dead mesh must be stopped, not left half-alive")
	}
}

func TestRunChild_RejectsBadCommands(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))
	ctx := context.Background()

	if err := m.RunChild(ctx, nil); err == nil {
		t.Fatal("a nil command must be refused")
	}

	started := newHelperChild(t, "sleep", 0)
	if err := started.cmd.Start(); err != nil {
		t.Fatalf("helper start: %v", err)
	}
	if err := m.RunChild(ctx, started.cmd); err == nil {
		t.Fatal("an already-started command must be refused")
	}
	if err := m.EnsureHealthy(ctx); err != nil {
		t.Fatalf("a refused RunChild must leave the mesh alone: %v", err)
	}

	missing := exec.Command("agy-pro-no-such-binary-xyz")
	err := m.RunChild(ctx, missing)
	if err == nil {
		t.Fatal("a command that cannot start must error")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("the error must name the start failure: %v", err)
	}
	if !m.isStopped() {
		t.Fatal("the mesh must stop when the child cannot start")
	}
}

func TestRunChild_RejectsASecondChild(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	cfg.ChildPollInterval = 250 * time.Millisecond
	m := startMesh(t, cfg)

	first := newHelperChild(t, "sleep-for", 2500*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- m.RunChild(context.Background(), first.cmd) }()

	time.Sleep(600 * time.Millisecond)
	second := newHelperChild(t, "exit0", 0)
	err := m.RunChild(context.Background(), second.cmd)
	if err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("a second child must be refused, got %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunChild: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunChild did not return after the first child finished")
	}
}

func TestStop_IsRefusedWhileAChildLives(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	cfg.ChildPollInterval = 250 * time.Millisecond
	m := startMesh(t, cfg)

	child := newHelperChild(t, "sleep-for", 2500*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- m.RunChild(context.Background(), child.cmd) }()

	time.Sleep(600 * time.Millisecond)
	err := m.Stop()
	if err == nil {
		t.Fatal("Stop must be refused while the child lives")
	}
	if !strings.Contains(err.Error(), "refusing to stop") {
		t.Errorf("the error must state the refusal: %v", err)
	}
	if healthErr := m.Healthz(context.Background()); healthErr != nil {
		t.Fatalf("the mesh must stay up after a refused Stop: %v", healthErr)
	}
	if !hbFresh(t, child.hb, 500*time.Millisecond) {
		t.Fatal("the child must stay alive after a refused Stop")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunChild: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunChild did not return after the child finished")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop after the child exits must succeed: %v", err)
	}
}

// TestStop_CannotRaceRunChildsStartWindow pins FIX-42b: the child is
// registered in the supervisor slot before it is started, so a Stop that
// lands inside the launch window is refused instead of stopping the mesh
// under a live child. The startChild seam holds the window open, which makes
// the race deterministic rather than statistical.
func TestStop_CannotRaceRunChildsStartWindow(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	cfg.ChildPollInterval = 250 * time.Millisecond
	m := startMesh(t, cfg)

	inStart := make(chan struct{})
	release := make(chan struct{})
	// Released exactly once, and always (the defer is the safety net): an
	// early failure must unblock the seam so a regression fails fast instead
	// of hanging the suite on a stuck child.
	var releaseOnce sync.Once
	releaseChild := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseChild()
	swapStartChild(t, func(cmd *exec.Cmd) error {
		close(inStart) // the child is registered; the process does not exist yet
		<-release
		return cmd.Start()
	})

	child := newHelperChild(t, "sleep-for", 1500*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- m.RunChild(context.Background(), child.cmd) }()

	<-inStart
	err := m.Stop()
	if err == nil {
		t.Fatal("Stop must be refused inside RunChild's start window")
	}
	if !strings.Contains(err.Error(), "refusing to stop") {
		t.Errorf("the error must state the refusal: %v", err)
	}
	if healthErr := m.Healthz(context.Background()); healthErr != nil {
		t.Fatalf("the mesh must stay up after the refused Stop: %v", healthErr)
	}

	releaseChild()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunChild: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunChild did not return after the child finished")
	}
	if !m.isStopped() {
		t.Fatal("the mesh must stop once the child exits")
	}
}

func TestRunChild_ContextCancelKillsTheChild(t *testing.T) {
	home, _ := sandboxHome(t)
	cfg := meshConfig(t, home)
	cfg.ChildPollInterval = 250 * time.Millisecond
	m := startMesh(t, cfg)

	child := newHelperChild(t, "sleep", 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.RunChild(ctx, child.cmd) }()

	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("RunChild did not return after the context was canceled")
	}

	if !m.isStopped() {
		t.Fatal("the mesh must stop when the session context ends")
	}
	last := hbMtime(t, child.hb)
	time.Sleep(400 * time.Millisecond)
	if hbMtime(t, child.hb).After(last) {
		t.Fatal("the child must be killed when the context is canceled")
	}
}

func TestRunChild_ChildStdioIsNeverRedirected(t *testing.T) {
	home, _ := sandboxHome(t)
	m := startMesh(t, meshConfig(t, home))

	child := newHelperChild(t, "exit0", 0)
	var out, errOut strings.Builder
	child.cmd.Stdout = &out
	child.cmd.Stderr = &errOut
	if err := m.RunChild(context.Background(), child.cmd); err != nil {
		t.Fatalf("RunChild: %v", err)
	}
	if child.cmd.Stdout != &out || child.cmd.Stderr != &errOut {
		t.Fatal("the supervisor must leave the child's stdio wired as the caller set it")
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("unexpected child output: %q / %q", out.String(), errOut.String())
	}
}
