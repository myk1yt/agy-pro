package dashboard

// Server spawn (P5-2 S5).
//
// Go never owns the dashboard server's lifecycle. Node has managed it since
// before this client existed (30-minute idle self-termination, source-staleness
// watchdog, port auto-increment on EADDRINUSE), so the Go side asks for a
// server once and then gets out of the way:
//
//  1. HEADLESS. The argv carries --serve --no-open and never --open, because
//     bare `agy-dashboard` self-injects both (bin/agy-tokens.js: the
//     isAgyDashboard branch pushes '--serve' and '--open' when neither open
//     flag is present). Every spawn argv is re-checked at the last door.
//  2. DETACHED, FIRE-AND-FORGET. The child gets its own process group, no
//     stdio, and no waiting parent: Start returns as soon as the OS accepted
//     the exec and then releases the handle. Never waits, never reads output,
//     never holds a handle. A server that outlives the TUI is the point.
//  3. GUARDED. The 15s spawn-intent stampede window (Node's SPAWN_GRACE_MS) is
//     honored by READING the intent the Node side owns, then writing the same
//     record shape (same file, same keys, same epoch-ms stamp) so a Go reader
//     and a Node reader agree in either direction. Go never claims the
//     record's lifecycle: the server overwrites it with its own port record.
//  4. PORT-NEUTRAL. Go asks for PreferredPort and then believes the port file,
//     never its own request: the server auto-increments when the port is taken
//     (src/serve.js EADDRINUSE retry) and republishes the bound port.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agypro/internal/wirecheck"
)

const (
	// PreferredPort is the port Go asks the server to bind first. It mirrors
	// DASHBOARD_DEFAULT_PORT (src/config.js). It is a REQUEST, never an
	// assumption: the port file stays authoritative.
	PreferredPort = 8787

	// SpawnGraceWindow mirrors SPAWN_GRACE_MS (src/dashboard-link.js): inside
	// this window a recorded spawn intent means another dashboard link (the
	// statusline hook) has already started a server, so spawning again would
	// double the process count.
	SpawnGraceWindow = 15 * time.Second

	// ProbeTimeout is the liveness dial budget (contract S5: 50ms).
	ProbeTimeout = 50 * time.Millisecond

	// serveFlag / noOpenFlag are the headless serve verbs ServeArgs appends.
	serveFlag  = "--serve"
	noOpenFlag = "--no-open"

	// portFlag carries an explicitly chosen port.
	portFlag = "--port"

	// spawnIntentVerb is the intent value both sides write and read.
	spawnIntentVerb = "spawn"

	// loopbackHost is the only host this package ever dials or opens.
	loopbackHost = "127.0.0.1"
)

// PortError is a port outside 1..65535, the range the Node reader accepts.
type PortError struct {
	Port int
}

func (e *PortError) Error() string {
	return fmt.Sprintf("dashboard port %d is outside 1..65535", e.Port)
}

// nowMillis is the clock seam for the spawn-intent stamp. Node writes
// Date.now() there, so the record carries epoch milliseconds.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

// probeDial is the liveness seam. Tests replace it to assert the timeout
// budget and to drive the timeout path without a network.
var probeDial = func(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, timeout)
}

// startProcess is the spawn seam: it configures the per-OS detachment, starts
// the child, and hands back its handle WITHOUT waiting. Production Start
// releases that handle immediately; a test that must stop the child it started
// (the gated live smoke) keeps it, which is why the handle is returned at all
// instead of being released here.
var startProcess = func(cmd *exec.Cmd) (*os.Process, error) {
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// ServeArgs builds the detached server argv:
//
//	<entry...> --serve --no-open [--port <n>]
//
// --port is appended ONLY when the caller chose a port explicitly (port > 0);
// port 0 leaves the choice to the server's own default. The entry argv never
// carries --open, --serve or --port itself: those are this function's to add,
// and a pre-set one is refused rather than duplicated.
func ServeArgs(entry Entry, port int) ([]string, error) {
	if len(entry.Argv) == 0 || strings.TrimSpace(entry.Argv[0]) == "" {
		return nil, &NotFoundError{Checked: []string{"empty argv"}}
	}
	if err := refuseSpawnEntryArgv(entry.Argv); err != nil {
		return nil, err
	}
	if port != 0 && !ValidPort(port) {
		return nil, &PortError{Port: port}
	}
	out := make([]string, 0, len(entry.Argv)+4)
	out = append(out, entry.Argv...)
	out = append(out, serveFlag, noOpenFlag)
	if port != 0 {
		out = append(out, portFlag, strconv.Itoa(port))
	}
	// Belt at the door that produced the argv: the final list must carry
	// exactly one serve verb and no browser flag.
	if err := requireServeVerb(out); err != nil {
		return nil, err
	}
	if err := refuseSpawnArgv(out); err != nil {
		return nil, err
	}
	return out, nil
}

// WriteSpawnIntent publishes the cross-process stampede guard in Node's own
// schema (src/dashboard-link.js writeSpawnIntent): {intent:"spawn",
// requestedPort, at} where at is epoch ms. The bytes go through the same
// SpawnIntent type LoadSpawnIntent decodes, so the two sides cannot drift.
//
// The write is the same crash-safe shape Node uses: a sibling temp file
// (<file>.<stamp>.<pid>.tmp) renamed over the target, so a concurrent Node
// reader sees either the previous record or the new one, never a partial file.
func WriteSpawnIntent(requestedPort int) error {
	if !ValidPort(requestedPort) {
		return &PortError{Port: requestedPort}
	}
	path, err := ServerJSONPath()
	if err != nil {
		return err
	}
	record := SpawnIntent{Intent: spawnIntentVerb, RequestedPort: requestedPort, At: nowMillis()}
	// Two-space indent + no trailing newline: byte-identical in shape to
	// JSON.stringify(record, null, 2), which is what Node writes.
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}

// writeFileAtomic writes raw to path through a temp file and a rename.
func writeFileAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, nowMillis(), os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// The temp file is ours and useless now; drop it so a failed write does
		// not leave debris in the dashboard directory.
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Probe reports whether something accepts a loopback connection on port. It is
// a liveness question with a hard ProbeTimeout budget (50ms), and it is called
// ONLY from an explicit keypress path (StartServer, reached from the Start
// row), never from a screen constructor or a paint.
func Probe(port int) bool {
	if !ValidPort(port) {
		return false
	}
	conn, err := probeDial(net.JoinHostPort(loopbackHost, strconv.Itoa(port)), ProbeTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// SpawnSkip explains why StartServer did not start a child.
type SpawnSkip string

const (
	// SpawnSkipRunning: a recorded server answered on its recorded port, so it
	// is already the thing this press would have started.
	SpawnSkipRunning SpawnSkip = "already-running"

	// SpawnSkipInFlight: a fresh spawn intent says another dashboard link is
	// starting one right now (the 15s stampede guard).
	SpawnSkipInFlight SpawnSkip = "spawn-in-flight"
)

// SpawnResult is one StartServer outcome. Argv is always the argv that would
// have run (or did), so a caller can report it without rebuilding it.
type SpawnResult struct {
	Argv    []string
	Port    int          // the port asked for; 0 means the server's own default
	Record  ServerRecord // the live record when Skip is SpawnSkipRunning
	Spawned bool
	Skip    SpawnSkip
}

// StartServer performs the one guarded spawn the Start row triggers:
//
//  1. build the headless serve argv (ServeArgs);
//  2. if a recorded server answers on its recorded port, do not spawn: Node's
//     own ensureServerRunning step 1 does exactly the same (dashboard-link.js);
//  3. if a spawn intent inside SpawnGraceWindow is on disk, do not spawn: a
//     sibling link got there first. This is READ-only on Go's side;
//  4. otherwise publish the intent (best effort) and Start the detached child.
//
// The probe in step 2 is the only network call in this package and it is
// reachable only from here, i.e. only from an explicit Enter on the Start row.
func StartServer(entry Entry, port int) (SpawnResult, error) {
	argv, err := ServeArgs(entry, port)
	if err != nil {
		return SpawnResult{}, err
	}
	res := SpawnResult{Argv: argv, Port: port}

	if rec, status := LoadServerRecord(); status == StatusOK && Probe(rec.Port) {
		res.Skip, res.Record = SpawnSkipRunning, rec
		return res, nil
	}
	if intent, status := LoadSpawnIntent(); status == StatusOK && nowMillis()-intent.At < SpawnGraceWindow.Milliseconds() {
		res.Skip = SpawnSkipInFlight
		res.Port = intent.RequestedPort
		return res, nil
	}

	// Intent stamp: request the caller's port, falling back to PreferredPort
	// when the caller left the choice to the server (Node records the preferred
	// port it spawned with, not the port that ends up bound).
	intentPort := port
	if intentPort == 0 {
		intentPort = PreferredPort
	}
	// Best effort, exactly like the Node side: the intent is an advisory hint,
	// and a dashboard directory we cannot write is one the server cannot
	// publish into either. Its absence only costs a second spawn attempt.
	_ = WriteSpawnIntent(intentPort)

	if err := Start(argv); err != nil {
		return res, err
	}
	res.Spawned = true
	return res, nil
}

// Start spawns argv detached and returns as soon as the OS accepted the exec.
// It never waits for the child, never captures a stream, and never keeps the
// handle: the server is meant to outlive the TUI (Node self-terminates after
// 30 idle minutes or when its sources change).
//
// An empty argv is a NotFoundError; a --open / duplicated --serve argv is a
// RefusedArgvError. Nothing here can launch a browser.
func Start(argv []string) error {
	proc, _, err := startHandle(argv)
	if err != nil {
		return err
	}
	if proc != nil {
		// Fire-and-forget: drop the handle. Never Wait - a parent that reaps is a
		// parent that could block.
		_ = proc.Release()
	}
	return nil
}

// startHandle is Start's whole body with the child handle and command returned
// instead of released. Production goes through Start, which drops the handle;
// this variant exists so the gated live smoke can stop the child it started
// without a production kill path. Both share one body, so the smoke exercises
// the same detached spawn the Start row does.
func startHandle(argv []string) (*os.Process, *exec.Cmd, error) {
	cmd, err := detachedCommand(argv)
	if err != nil {
		return nil, nil, err
	}
	// Default-deny environment, the same allow-list the one-shot refresh uses:
	// the child needs HOME / USERPROFILE / PATH / TEMP to start, and no
	// provider key value crosses.
	cmd.Env = wirecheck.FilterChildEnv(nil)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil

	proc, err := startProcess(cmd)
	if err != nil {
		return nil, cmd, fmt.Errorf("dashboard server spawn failed: %w", err)
	}
	return proc, cmd, nil
}

// detachedCommand builds the child command for argv. No context is attached on
// purpose: cancelling the TUI's key loop must not kill a server that is
// supposed to survive it.
func detachedCommand(argv []string) (*exec.Cmd, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, &NotFoundError{Checked: []string{"empty argv"}}
	}
	if err := refuseSpawnArgv(argv); err != nil {
		return nil, err
	}
	if isBatchScript(argv[0]) {
		// An npm .cmd shim cannot be exec'd directly; the B9 wrap (oneshot.go)
		// hands cmd.exe a hand-escaped raw line. Detachment flags are ORed onto
		// the same SysProcAttr by configureDetached.
		cmd := exec.Command("cmd.exe")
		setRawCmdLine(cmd, buildBatchCmdLine(argv[0], argv[1:]))
		return cmd, nil
	}
	return exec.Command(argv[0], argv[1:]...), nil
}

// refuseSpawnEntryArgv is the entry-side belt: the resolved dashboard command
// must be the BARE command, because ServeArgs adds the serve verbs itself. A
// pre-set --serve would be duplicated, and a pre-set --open or --port would
// quietly change what the row means.
func refuseSpawnEntryArgv(argv []string) error {
	for _, a := range argv {
		low := strings.ToLower(strings.TrimSpace(a))
		switch {
		case low == "--open" || strings.HasPrefix(low, "--open="):
			return &RefusedArgvError{Token: "--open", Reason: "the spawn is headless; a browser launch is never allowed"}
		case low == "--serve" || strings.HasPrefix(low, "--serve="):
			return &RefusedArgvError{Token: "--serve", Reason: "the entry must be the bare dashboard command; ServeArgs adds the serve verb"}
		case low == "--port" || strings.HasPrefix(low, "--port="):
			return &RefusedArgvError{Token: "--port", Reason: "the port is chosen by ServeArgs, never by the entry"}
		}
	}
	return nil
}

// refuseSpawnArgv is the belt every spawn door shares: whatever produced the
// argv, the child must never be started with a browser flag, and must never
// carry the serve verb twice. A serve-less argv is tolerated HERE only because
// Start is also reachable with a plain command (the tests drive it with a
// helper binary); ServeArgs, the door that builds a server argv, additionally
// requires exactly one serve verb (requireServeVerb).
func refuseSpawnArgv(argv []string) error {
	serves := 0
	for _, a := range argv {
		low := strings.ToLower(strings.TrimSpace(a))
		if low == "--open" || strings.HasPrefix(low, "--open=") {
			return &RefusedArgvError{Token: "--open", Reason: "the spawn is headless; a browser launch is never allowed"}
		}
		if low == serveFlag || strings.HasPrefix(low, serveFlag+"=") {
			serves++
		}
	}
	if serves > 1 {
		return &RefusedArgvError{Token: serveFlag, Reason: fmt.Sprintf("the spawn argv carries %s twice", serveFlag)}
	}
	return nil
}

// requireServeVerb is the ServeArgs-specific count check: a server argv without
// the serve verb would run a one-shot report instead of a server, which is not
// what the Start row asked for.
func requireServeVerb(argv []string) error {
	for _, a := range argv {
		low := strings.ToLower(strings.TrimSpace(a))
		if low == serveFlag || strings.HasPrefix(low, serveFlag+"=") {
			return nil
		}
	}
	return &RefusedArgvError{Token: serveFlag, Reason: "the spawn argv carries no " + serveFlag}
}
