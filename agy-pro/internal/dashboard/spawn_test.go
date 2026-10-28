package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helpers ------------------------------------------------------------------

// pointDashDir aims the loaders and the intent writer at a temp dashboard
// directory (the S1 unexported seam) and restores it afterwards.
func pointDashDir(t *testing.T, dir string) {
	t.Helper()
	prev := dirOverride
	dirOverride = dir
	t.Cleanup(func() { dirOverride = prev })
}

// fixedClock pins the intent stamp so a round-trip assertion is exact.
func fixedClock(t *testing.T, ms int64) {
	t.Helper()
	prev := nowMillis
	nowMillis = func() int64 { return ms }
	t.Cleanup(func() { nowMillis = prev })
}

// spySpawn replaces the spawn seam with a counter. Nothing is executed.
func spySpawn(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := startProcess
	startProcess = func(cmd *exec.Cmd) (*os.Process, error) {
		calls++
		return nil, nil
	}
	t.Cleanup(func() { startProcess = prev })
	return &calls
}

// realStartSpy swaps the spawn seam for the real detached start and hands the
// started command back to the test, so a test can prove Start did not wait and
// then stop (and reap) the child it started.
func realStartSpy(t *testing.T) func() *exec.Cmd {
	t.Helper()
	var captured *exec.Cmd
	prev := startProcess
	startProcess = func(cmd *exec.Cmd) (*os.Process, error) {
		configureDetached(cmd)
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		captured = cmd
		return cmd.Process, nil
	}
	t.Cleanup(func() {
		startProcess = prev
		if captured != nil && captured.Process != nil {
			_ = captured.Process.Kill()
			_, _ = captured.Process.Wait()
		}
	})
	return func() *exec.Cmd { return captured }
}

// freePort returns a port the OS just handed out and is therefore very likely
// free again for the next few hundred milliseconds.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// writeServerRecord plants a live-shape port record in dir.
func writeServerRecord(t *testing.T, dir string, rec ServerRecord) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal server record: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, serverFileName), raw, 0o600); err != nil {
		t.Fatalf("write server record: %v", err)
	}
}

// serveArgs ---------------------------------------------------------------

// TestServeArgs pins the argv contract: <entry> --serve --no-open, with --port
// only when the caller chose a port explicitly. The entry slice is never
// mutated, and every entry-side hazard is refused rather than duplicated.
func TestServeArgs(t *testing.T) {
	entry := Entry{Argv: []string{"node", "/tools/bin/agy-tokens.js"}, Source: SourceNodeEntry}
	cases := []struct {
		name    string
		entry   Entry
		port    int
		want    []string
		refusal string // expected RefusedArgvError token
		notFnd  bool
		portErr bool
	}{
		{
			name: "explicit port", entry: entry, port: PreferredPort,
			want: []string{"node", "/tools/bin/agy-tokens.js", "--serve", "--no-open", "--port", "8787"},
		},
		{
			name: "no explicit port", entry: entry, port: 0,
			want: []string{"node", "/tools/bin/agy-tokens.js", "--serve", "--no-open"},
		},
		{
			name: "entry carrying --open", entry: Entry{Argv: []string{"agy-tokens", "--open"}}, port: PreferredPort,
			refusal: "--open",
		},
		{
			name: "entry carrying --serve", entry: Entry{Argv: []string{"agy-tokens", "--serve"}}, port: PreferredPort,
			refusal: "--serve",
		},
		{
			name: "entry carrying --serve=8787", entry: Entry{Argv: []string{"agy-tokens", "--serve=8787"}}, port: PreferredPort,
			refusal: "--serve",
		},
		{
			name: "entry carrying --OPEN", entry: Entry{Argv: []string{"agy-tokens", "--OPEN"}}, port: PreferredPort,
			refusal: "--open",
		},
		{
			name: "entry carrying --port", entry: Entry{Argv: []string{"agy-tokens", "--port", "9000"}}, port: PreferredPort,
			refusal: "--port",
		},
		{
			name: "empty argv", entry: Entry{}, port: PreferredPort, notFnd: true,
		},
		{
			name: "blank program", entry: Entry{Argv: []string{"   "}}, port: PreferredPort, notFnd: true,
		},
		{
			name: "port above range", entry: entry, port: 65536, portErr: true,
		},
		{
			name: "negative port", entry: entry, port: -1, portErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.entry.Argv...)
			got, err := ServeArgs(tc.entry, tc.port)
			if strings.Join(tc.entry.Argv, "\x00") != strings.Join(before, "\x00") {
				t.Fatalf("ServeArgs mutated the entry argv: %v -> %v", before, tc.entry.Argv)
			}
			switch {
			case tc.refusal != "":
				var refused *RefusedArgvError
				if !errors.As(err, &refused) {
					t.Fatalf("err = %v, want a *RefusedArgvError", err)
				}
				if refused.Token != tc.refusal {
					t.Fatalf("refused token = %q, want %q", refused.Token, tc.refusal)
				}
				if got != nil {
					t.Fatalf("a refused argv still returned %v", got)
				}
			case tc.notFnd:
				var nf *NotFoundError
				if !errors.As(err, &nf) {
					t.Fatalf("err = %v, want a *NotFoundError", err)
				}
			case tc.portErr:
				var pe *PortError
				if !errors.As(err, &pe) {
					t.Fatalf("err = %v, want a *PortError", err)
				}
			default:
				if err != nil {
					t.Fatalf("ServeArgs: %v", err)
				}
				if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
					t.Fatalf("argv = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestServeArgsCarriesExactlyOneServeVerb is the last-door belt in isolation:
// the produced argv is re-checked, and the check counts serve verbs instead of
// trusting the builder.
func TestServeArgsCarriesExactlyOneServeVerb(t *testing.T) {
	argv, err := ServeArgs(Entry{Argv: []string{"node", "x.js"}}, PreferredPort)
	if err != nil {
		t.Fatalf("ServeArgs: %v", err)
	}
	serves, opens := 0, 0
	for _, a := range argv {
		switch a {
		case "--serve":
			serves++
		case "--open":
			opens++
		}
	}
	if serves != 1 || opens != 0 {
		t.Fatalf("argv = %v (serve=%d open=%d), want exactly one --serve and no --open", argv, serves, opens)
	}
	// The same check refuses a serve-less argv at the ServeArgs door, and a
	// doubly served argv at every door.
	if err := requireServeVerb([]string{"node", "x.js", "--no-open"}); err == nil {
		t.Fatal("a serve-less argv passed the ServeArgs count check")
	}
	if err := refuseSpawnArgv([]string{"node", "x.js", "--serve", "--serve"}); err == nil {
		t.Fatal("a doubly served argv passed the last-door check")
	}
	if err := refuseSpawnArgv([]string{"node", "x.js", "--serve", "--OPEN"}); err == nil {
		t.Fatal("a browser flag passed the last-door check")
	}
}

// spawn intent ------------------------------------------------------------

// TestWriteSpawnIntentRoundTrip is the cross-process schema pin: Go writes the
// record, the S1 loader reads it back, and the bytes carry exactly the keys the
// Node fixture carries (so a Node reader and a Go reader parse the same file).
func TestWriteSpawnIntentRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pointDashDir(t, dir)
	const stamp = int64(1761156000123)
	fixedClock(t, stamp)

	if err := WriteSpawnIntent(PreferredPort); err != nil {
		t.Fatalf("WriteSpawnIntent: %v", err)
	}
	path := filepath.Join(dir, serverFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written intent: %v", err)
	}

	// 1. The S1 loader (a Go reader) decodes what Go just wrote.
	intent, status := LoadSpawnIntent()
	if status != StatusOK {
		t.Fatalf("LoadSpawnIntent status = %v, want ok", status)
	}
	if intent.Intent != "spawn" || intent.RequestedPort != PreferredPort || intent.At != stamp {
		t.Fatalf("round-trip mismatch: %+v (want spawn/%d/%d)", intent, PreferredPort, stamp)
	}

	// 2. The byte shape matches Node's own artifact: same key set, same
	// two-space indent, no trailing newline.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("the written intent is not a JSON object: %v", err)
	}
	fixtureRaw, err := os.ReadFile(filepath.Join("testdata", "spawn-intent.json"))
	if err != nil {
		t.Fatalf("read the Node-shaped fixture: %v", err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(fixtureRaw, &fixture); err != nil {
		t.Fatalf("parse the Node-shaped fixture: %v", err)
	}
	if got, want := sortedKeys(generic), sortedKeys(fixture); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Go writes keys %v, the Node fixture carries %v", got, want)
	}
	if !strings.Contains(string(raw), "\n  \"intent\": \"spawn\"") {
		t.Fatalf("the write is not two-space indented like JSON.stringify(...,null,2):\n%s", raw)
	}
	if strings.HasSuffix(string(raw), "\n") {
		t.Fatal("the write carries a trailing newline Node never emits")
	}
	if _, ok := generic["requestedPort"]; !ok {
		t.Fatalf("the write carries no requestedPort: %s", raw)
	}
	// The stamp is a JSON number, not a string: Node compares Date.now() to it.
	if got := strings.TrimSpace(string(generic["at"])); got != strconv.FormatInt(stamp, 10) {
		t.Fatalf("at = %s, want the epoch-ms number %d", got, stamp)
	}

	// 3. The same file also decodes through the Node fixture direction: planting
	// the Node-written fixture over the port file yields a usable typed record.
	if err := os.WriteFile(path, fixtureRaw, 0o600); err != nil {
		t.Fatalf("plant the Node fixture: %v", err)
	}
	fixtureIntent, fixtureStatus := LoadSpawnIntent()
	if fixtureStatus != StatusOK {
		t.Fatalf("the Node fixture does not decode: status = %v", fixtureStatus)
	}
	if fixtureIntent.Intent != "spawn" || fixtureIntent.RequestedPort != PreferredPort || fixtureIntent.At <= 0 {
		t.Fatalf("the Node fixture decoded to %+v, want spawn/%d/positive-at", fixtureIntent, PreferredPort)
	}

	// 4. No temp debris: the atomic write renamed its file away.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) != 1 || entries[0].Name() != serverFileName {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dashboard dir holds %v, want only %s", names, serverFileName)
	}
}

// sortedKeys returns a map's keys in a stable order.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// TestWriteSpawnIntentRejectsBadPort pins the range check on the writer.
func TestWriteSpawnIntentRejectsBadPort(t *testing.T) {
	dir := t.TempDir()
	pointDashDir(t, dir)
	for _, port := range []int{0, -1, 65536} {
		var pe *PortError
		if err := WriteSpawnIntent(port); !errors.As(err, &pe) {
			t.Fatalf("WriteSpawnIntent(%d) = %v, want a *PortError", port, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, serverFileName)); !os.IsNotExist(err) {
		t.Fatalf("a rejected port still wrote the intent file: %v", err)
	}
}

// StartServer -------------------------------------------------------------

// TestStartServerSkipsWhenServerAnswers: a recorded server that answers on its
// recorded port means the press has nothing to do (Node's ensureServerRunning
// step 1). No child is started.
func TestStartServerSkipsWhenServerAnswers(t *testing.T) {
	dir := t.TempDir()
	pointDashDir(t, dir)
	calls := spySpawn(t)
	port := freePort(t)
	writeServerRecord(t, dir, ServerRecord{Port: port, PID: 4242, PayloadVersion: DashboardPayloadVersion})

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen on %d: %v", port, err)
	}
	defer ln.Close()

	res, err := StartServer(Entry{Argv: []string{"node", "x.js"}}, PreferredPort)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	if res.Spawned || res.Skip != SpawnSkipRunning {
		t.Fatalf("result = %+v, want a running-server skip", res)
	}
	if res.Record.Port != port {
		t.Fatalf("result record port = %d, want the recorded %d", res.Record.Port, port)
	}
	if *calls != 0 {
		t.Fatalf("the spawn seam ran %d times, want 0", *calls)
	}
}

// TestStartServerHonorsSpawnGraceWindow pins the 15s stampede guard in both
// directions: a fresh intent suppresses the spawn (the Node hook got there
// first), an expired one does not.
func TestStartServerHonorsSpawnGraceWindow(t *testing.T) {
	const now = int64(1761156000000)
	cases := []struct {
		name       string
		intentAge  time.Duration
		wantSpawn  bool
		wantSkip   SpawnSkip
		wantIntent int // requested port the intent must be rewritten with
	}{
		{name: "fresh intent suppresses the spawn", intentAge: 2 * time.Second, wantSpawn: false, wantSkip: SpawnSkipInFlight, wantIntent: 9001},
		{name: "intent exactly at the window edge suppresses", intentAge: SpawnGraceWindow - time.Millisecond, wantSpawn: false, wantSkip: SpawnSkipInFlight, wantIntent: 9001},
		{name: "expired intent spawns", intentAge: SpawnGraceWindow + time.Second, wantSpawn: true, wantIntent: PreferredPort},
		{name: "future intent (clock skew) suppresses like Node", intentAge: -time.Minute, wantSpawn: false, wantSkip: SpawnSkipInFlight, wantIntent: 9001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pointDashDir(t, dir)
			fixedClock(t, now)
			calls := spySpawn(t)
			if err := writeIntentAt(t, 9001, now-int64(tc.intentAge.Milliseconds())); err != nil {
				t.Fatalf("plant the intent: %v", err)
			}

			res, err := StartServer(Entry{Argv: []string{"node", "x.js"}}, 0)
			if err != nil {
				t.Fatalf("StartServer: %v", err)
			}
			if res.Spawned != tc.wantSpawn {
				t.Fatalf("spawned = %v, want %v (result %+v)", res.Spawned, tc.wantSpawn, res)
			}
			if res.Skip != tc.wantSkip {
				t.Fatalf("skip = %q, want %q", res.Skip, tc.wantSkip)
			}
			if got := *calls; (got == 1) != tc.wantSpawn {
				t.Fatalf("the spawn seam ran %d times, want spawned=%v", got, tc.wantSpawn)
			}
			// Both paths leave a record a Node reader accepts; a spawn rewrites
			// the intent with its own request (port 0 -> PreferredPort).
			intent, status := LoadSpawnIntent()
			if status != StatusOK {
				t.Fatalf("the port file holds no intent after StartServer (status %v)", status)
			}
			if tc.wantSpawn && intent.RequestedPort != tc.wantIntent {
				t.Fatalf("intent requestedPort = %d, want %d", intent.RequestedPort, tc.wantIntent)
			}
		})
	}
}

// writeIntentAt is WriteSpawnIntent with the stamp forced, for the guard tests.
func writeIntentAt(t *testing.T, requestedPort int, at int64) error {
	t.Helper()
	prev := nowMillis
	nowMillis = func() int64 { return at }
	defer func() { nowMillis = prev }()
	return WriteSpawnIntent(requestedPort)
}

// TestStartServerRefusesUnsafeEntryWithoutSpawning: the belt holds even when a
// caller skips ServeArgs and calls StartServer with a hostile entry.
func TestStartServerRefusesUnsafeEntryWithoutSpawning(t *testing.T) {
	pointDashDir(t, t.TempDir())
	calls := spySpawn(t)
	for _, argv := range [][]string{
		{"agy-tokens", "--open"},
		{"agy-tokens", "--serve"},
		{"agy-tokens", "--port", "9000"},
	} {
		var refused *RefusedArgvError
		if _, err := StartServer(Entry{Argv: argv}, PreferredPort); !errors.As(err, &refused) {
			t.Fatalf("StartServer(%v) = %v, want a *RefusedArgvError", argv, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("the spawn seam ran %d times, want 0", *calls)
	}
	if _, err := os.Stat(filepath.Join(dirOverride, serverFileName)); !os.IsNotExist(err) {
		t.Fatalf("a refused argv still published an intent: %v", err)
	}
}

// Start -------------------------------------------------------------------

// TestStartNeverWaits starts a real (helper) child that sleeps for two seconds
// and pins the fire-and-forget contract: Start returns immediately, the child
// got no stdio, and its environment is the default-deny allow-list.
func TestStartNeverWaits(t *testing.T) {
	dir := t.TempDir()
	getCmd := realStartSpy(t)
	t.Setenv("OPENAI_API_KEY", "sk-planted-value-that-must-not-cross")
	t.Setenv("AGY_LLM_GATEWAY_API_KEY", "gw-planted-value-that-must-not-cross")

	start := time.Now()
	if err := Start([]string{os.Args[0], "-test.run=TestOneShotHelperProcess", "--", "sleep", dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Start took %v; it must not wait for a 2s child", elapsed)
	}

	cmd := getCmd()
	if cmd == nil {
		t.Fatal("the spawn seam never ran, so nothing was started")
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("the detached child was handed a stdio stream; it must be fire-and-forget")
	}
	env := strings.Join(cmd.Env, "\n")
	for _, key := range []string{"OPENAI_API_KEY", "AGY_LLM_GATEWAY_API_KEY"} {
		if strings.Contains(env, key) {
			t.Fatalf("the child environment carries %s", key)
		}
	}
	if !strings.Contains(env, "PATH=") {
		t.Fatalf("the child environment lost PATH:\n%s", env)
	}
	if runtime.GOOS == "windows" && !strings.Contains(strings.ToUpper(env), "SYSTEMROOT=") {
		t.Fatalf("the child environment lost SYSTEMROOT:\n%s", env)
	}
}

// TestStartRefusesEmptyAndUnsafeArgv pins the last door: nothing is executed for
// an empty or hostile argv.
func TestStartRefusesEmptyAndUnsafeArgv(t *testing.T) {
	calls := spySpawn(t)
	var nf *NotFoundError
	if err := Start(nil); !errors.As(err, &nf) {
		t.Fatalf("Start(nil) = %v, want a *NotFoundError", err)
	}
	var refused *RefusedArgvError
	if err := Start([]string{"node", "x.js", "--serve", "--open"}); !errors.As(err, &refused) {
		t.Fatalf("Start(--open) = %v, want a *RefusedArgvError", err)
	}
	if err := Start([]string{"node", "x.js", "--serve", "--serve"}); !errors.As(err, &refused) {
		t.Fatalf("Start(two serves) = %v, want a *RefusedArgvError", err)
	}
	if *calls != 0 {
		t.Fatalf("the spawn seam ran %d times, want 0", *calls)
	}
}

// Probe -------------------------------------------------------------------

// TestProbeClosedPortIsFalseAndBounded: a closed loopback port answers with a
// refusal (not a hang), well inside the 50ms budget plus scheduling slack.
func TestProbeClosedPortIsFalseAndBounded(t *testing.T) {
	port := freePort(t)
	start := time.Now()
	if Probe(port) {
		t.Fatalf("Probe(%d) reported a listener on a port this test just closed", port)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Probe took %v on a closed port; the dial must be bounded", elapsed)
	}
}

// TestProbeListenerIsTrue: a real loopback listener reads as alive.
func TestProbeListenerIsTrue(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if !Probe(port) {
		t.Fatalf("Probe(%d) missed a live loopback listener", port)
	}
}

// TestProbeInvalidPortNeverDials: an out-of-range port is refused before any
// socket work, so a corrupt record cannot turn a keypress into a network call.
func TestProbeInvalidPortNeverDials(t *testing.T) {
	calls := 0
	prev := probeDial
	probeDial = func(addr string, timeout time.Duration) (net.Conn, error) {
		calls++
		return nil, errors.New("the dialer must not be reached")
	}
	t.Cleanup(func() { probeDial = prev })

	for _, port := range []int{0, -1, 65536} {
		if Probe(port) {
			t.Fatalf("Probe(%d) reported a listener", port)
		}
	}
	if calls != 0 {
		t.Fatalf("the dialer ran %d times for invalid ports, want 0", calls)
	}
}

// TestProbeTimeoutPathIsFalse drives the timeout branch: a dial that gives up
// reads as "not alive", never as an error the caller has to handle, and the
// seam is asked for the documented budget and loopback address.
func TestProbeTimeoutPathIsFalse(t *testing.T) {
	var gotAddr string
	var gotTimeout time.Duration
	prev := probeDial
	probeDial = func(addr string, timeout time.Duration) (net.Conn, error) {
		gotAddr, gotTimeout = addr, timeout
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	t.Cleanup(func() { probeDial = prev })

	if Probe(PreferredPort) {
		t.Fatal("a timed-out dial reported a live server")
	}
	if gotTimeout != ProbeTimeout {
		t.Fatalf("dial budget = %v, want %v", gotTimeout, ProbeTimeout)
	}
	if gotAddr != "127.0.0.1:8787" {
		t.Fatalf("dial address = %q, want the loopback address of the port", gotAddr)
	}
}

// Gated live smoke --------------------------------------------------------

// TestServeRealSpawnSmoke is the gated headless live smoke: the REAL Node
// `--serve --no-open` child, spawned through the real detached path on a free
// port, with both HOME and USERPROFILE redirected so nothing lands in the
// operator's real tree. It asserts the published port file, GETs /data.json
// over loopback, then stops the child and proves the port closed and no node
// process of ours survived.
//
// Both HOME and USERPROFILE are redirected because Node's os.homedir() on
// Windows reads USERPROFILE and ignores HOME (the wave-1 blind-QA finding); a
// HOME-only redirect silently writes to the real home under Git Bash.
func TestServeRealSpawnSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the live spawn smoke needs the Node runtime")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	entryScript := filepath.Join(repoRoot, "bin", "agy-tokens.js")
	if _, err := os.Stat(entryScript); err != nil {
		t.Skipf("repo entry %s is not present: %v", entryScript, err)
	}

	// Measure the real tree (never write it) BEFORE the env redirect, so the
	// paths stay the operator's own.
	realHome, err := homeDir()
	if err != nil {
		t.Skipf("the real user home does not resolve, so there is nothing to protect: %v", err)
	}
	realDashboard := filepath.Join(realHome, ".gemini", DirName)
	agyHome := filepath.Join(realHome, ".agy-pro")
	before := snapshotRealDashboardDir(t, realHome)
	stateBefore := statFile(t, filepath.Join(agyHome, "state.json"))
	providersBefore := statFile(t, filepath.Join(agyHome, "providers.json"))
	nodesBefore, nodesErr := nodeProcesses()
	if nodesErr != nil {
		t.Logf("cannot list node processes before the run (%v); the pid assertion degrades to the port/exit proof", nodesErr)
	}
	beforePIDs := pidsOf(nodesBefore)

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv(EnvProToolsRoot, "")
	t.Setenv(EnvDashboardEntry, `"`+node+`" "`+entryScript+`"`)
	redirectedDir := filepath.Join(fakeHome, ".gemini", DirName)
	pointDashDir(t, redirectedDir)

	entry, err := ResolveEntry()
	if err != nil {
		t.Fatalf("ResolveEntry: %v", err)
	}
	if entry.Source != SourceEnvOverride {
		t.Fatalf("entry source = %q, want the env override", entry.Source)
	}

	port := freePort(t)
	argv, err := ServeArgs(entry, port)
	if err != nil {
		t.Fatalf("ServeArgs: %v", err)
	}
	for _, banned := range []string{"--open"} {
		for _, a := range argv {
			if a == banned {
				t.Fatalf("the spawn argv carries %s: %v", banned, argv)
			}
		}
	}
	if !strings.Contains(strings.Join(argv, " "), "--serve --no-open --port "+strconv.Itoa(port)) {
		t.Fatalf("argv = %v, want the headless serve tail", argv)
	}

	proc, cmd, err := startHandle(argv)
	if err != nil {
		t.Fatalf("detached spawn: %v", err)
	}
	if proc == nil || cmd == nil || cmd.Process == nil {
		t.Fatal("the detached start returned no handle")
	}
	// Cleanup runs even on a failed assertion, so the unit never leaves a server
	// listening or a node process behind.
	t.Cleanup(func() {
		if proc != nil {
			_ = proc.Kill()
			_, _ = proc.Wait()
		}
	})

	// 1. The server publishes its port record inside the REDIRECTED home within
	// 5s (bounding: bind happens after the first aggregation).
	rec, ok := awaitServerRecord(t, 5*time.Second)
	if !ok {
		t.Fatalf("the spawned server published no usable port record under %s within 5s", redirectedDir)
	}
	if rec.PID != cmd.Process.Pid {
		t.Fatalf("the published pid %d is not the child we started (%d)", rec.PID, cmd.Process.Pid)
	}

	// 2. Loopback HTTP answers 200 with a parsable v3 payload.
	url := LoopbackURL(rec.Port) + "data.json"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body starts %q)", url, resp.StatusCode, truncateForLog(string(body), 200))
	}
	var payload Payload
	if !isJSONObject(body) || json.Unmarshal(body, &payload) != nil {
		t.Fatalf("GET %s did not return a JSON payload: %q", url, truncateForLog(string(body), 200))
	}
	if payload.Version < DashboardPayloadVersion {
		t.Fatalf("served payload version = %d, want >= %d", payload.Version, DashboardPayloadVersion)
	}
	if strings.TrimSpace(payload.GeneratedAt) == "" {
		t.Fatal("the served payload carries no generatedAt")
	}
	if status := mustLoadPayloadStatus(t); status != StatusOK {
		t.Fatalf("the served snapshot does not clear the S1 version gate: status %v", status)
	}

	// 3. Nothing was written outside the redirected home.
	after := snapshotRealDashboardDir(t, realHome)
	if before.existed != after.existed || strings.Join(before.names, "\n") != strings.Join(after.names, "\n") {
		t.Fatalf("the real dashboard directory changed during a redirected spawn:\nbefore(%s):\n%s\nafter:\n%s",
			realDashboard, strings.Join(before.names, "\n"), strings.Join(after.names, "\n"))
	}
	if got := statFile(t, filepath.Join(agyHome, "state.json")); got != stateBefore {
		t.Fatalf("~/.agy-pro/state.json changed: %+v -> %+v", stateBefore, got)
	}
	if got := statFile(t, filepath.Join(agyHome, "providers.json")); got != providersBefore {
		t.Fatalf("~/.agy-pro/providers.json changed: %+v -> %+v", providersBefore, got)
	}

	// 4. Stop the child and prove it is gone: the port stops answering, the
	// handle reports an exit, and no node process of ours outlives the unit.
	//
	// The server itself spawns ONE detached, unref'd background quota-refresh
	// child (src/gemini-quota.js triggerBackgroundQuotaRefresh) while it serves a
	// payload. That grandchild is Node's own design, it is not ours to manage,
	// and it exits by itself once the fetch resolves; the assertion below
	// therefore waits for the node set to drain back to the pre-run set instead
	// of demanding it never grew.
	childPID := cmd.Process.Pid
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill the child: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("reap the child: %v", err)
	}
	if state.Success() {
		t.Logf("the child exited 0 before the kill landed (server self-termination) - acceptable")
	}
	proc = nil // the cleanup must not double-kill

	if !awaitPortClosed(rec.Port, 5*time.Second) {
		t.Fatalf("port %d still answers 5s after the child was killed", rec.Port)
	}
	if nodesErr == nil {
		nodesAfter, err := nodeProcesses()
		if err != nil {
			t.Logf("cannot list node processes after the run: %v", err)
		} else if containsPID(pidsOf(nodesAfter), childPID) {
			t.Fatalf("the spawned node process %d survived; before=%v after=%v", childPID, beforePIDs, pidsOf(nodesAfter))
		}
		orphans, foreign, waited, err := awaitNodeDrain(10*time.Second, beforePIDs)
		if err != nil {
			t.Logf("cannot re-list node processes while waiting for the tree to drain: %v", err)
		} else {
			if len(foreign) > 0 {
				// Another unit on this machine runs node too; those are not ours
				// to stop, so they are reported, never failed on.
				t.Logf("node processes owned by other work appeared during the run and are left alone: %+v", foreign)
			}
			if len(orphans) > 0 {
				t.Fatalf("the dashboard server's own detached quota-refresh child survived: %+v (before=%v)",
					orphans, beforePIDs)
			}
			t.Logf("no orphan of this run after %v (before=%v, our child %d is gone)", waited, beforePIDs, childPID)
		}
	}
}

// awaitNodeDrain waits until no NEW node process carries the server's own
// quota-refresh marker, and reports the survivors of that kind plus the
// unrelated new processes (foreign) and how long the wait took. Pids that
// existed before the run are never attributed to it.
func awaitNodeDrain(budget time.Duration, before []int) (ours, foreign []nodeProc, waited time.Duration, err error) {
	start := time.Now()
	for {
		after, err := nodeProcesses()
		if err != nil {
			return nil, nil, time.Since(start), err
		}
		var mine, theirs []nodeProc
		for _, p := range after {
			if containsPID(before, p.PID) {
				continue
			}
			if strings.Contains(p.Cmd, quotaRefreshMarker) {
				mine = append(mine, p)
				continue
			}
			theirs = append(theirs, p)
		}
		if len(mine) == 0 || time.Since(start) > budget {
			return mine, theirs, time.Since(start), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pidsOf flattens a node process list to pids.
func pidsOf(procs []nodeProc) []int {
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		pids = append(pids, p.PID)
	}
	return pids
}

// awaitServerRecord polls the directed dashboard dir for a usable port record.
func awaitServerRecord(t *testing.T, budget time.Duration) (ServerRecord, bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if rec, status := LoadServerRecord(); status == StatusOK {
			return rec, true
		}
		if time.Now().After(deadline) {
			return ServerRecord{}, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// awaitPortClosed polls until the port refuses connections.
func awaitPortClosed(port int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !Probe(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// mustLoadPayloadStatus reports the payload gate status for the live smoke.
func mustLoadPayloadStatus(t *testing.T) Status {
	t.Helper()
	_, status := LoadPayload()
	return status
}

// nodeProc is one live node process: its pid and (bounded) command line, so the
// smoke can tell its own parented tree from a neighbouring unit's node process.
type nodeProc struct {
	PID int
	Cmd string
}

// nodeProcesses lists live node processes with their command lines. An
// unavailable tool degrades the assertion, not the test.
func nodeProcesses() ([]nodeProc, error) {
	if runtime.GOOS == "windows" {
		script := `Get-CimInstance Win32_Process -Filter "Name='node.exe'" | ForEach-Object { $_.ProcessId.ToString() + ' :: ' + $_.CommandLine }`
		out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return nil, fmt.Errorf("powershell: %w", err)
		}
		var procs []nodeProc
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			pidText, cmd, ok := strings.Cut(line, "::")
			if !ok {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(pidText))
			if err != nil {
				continue
			}
			procs = append(procs, nodeProc{PID: pid, Cmd: truncateForLog(cmd, 240)})
		}
		return procs, nil
	}
	out, err := exec.Command("pgrep", "-af", "node").Output()
	if err != nil {
		return nil, fmt.Errorf("pgrep: %w", err)
	}
	var procs []nodeProc
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procs = append(procs, nodeProc{PID: pid, Cmd: truncateForLog(fields[1], 240)})
	}
	return procs, nil
}

// quotaRefreshMarker is the argv text of the ONE detached child the dashboard
// server itself spawns while serving (src/gemini-quota.js
// triggerBackgroundQuotaRefresh runs `node -e '...fetchLiveGeminiQuota...'`).
// It is the only orphan this run can legitimately be blamed for.
const quotaRefreshMarker = "fetchLiveGeminiQuota"

// containsPID reports whether pid is in pids.
func containsPID(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}

// truncateForLog bounds a body snippet so a failure message stays one line.
func truncateForLog(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestSpawnFixtureIsNodeShaped is a cheap guard that the fixture used by the
// round-trip test still carries the three keys Node writes.
func TestSpawnFixtureIsNodeShaped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "spawn-intent.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	for _, key := range []string{"intent", "requestedPort", "at"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("fixture carries no %q key: %s", key, raw)
		}
	}
}
