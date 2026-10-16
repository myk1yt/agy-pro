// Package mesh is the per-spawn supervisor for the hybrid profiles' loopback
// mesh.
//
// Both hybrid profiles (design §2, P1 "picker" and P2 "subagent") route agy's
// model traffic through a local wireadp server. This package owns that
// server's lifetime and ties it to the child agy process:
//
//   - Per-spawn, never shared. Every Start binds its own wireadp instance on
//     an ephemeral loopback port. The supervisor never adopts a foreign
//     instance: wireadp's adopt/refuse machinery exists for the library
//     singleton use case, while a hybrid session always runs a mesh this
//     supervisor started itself. Start refuses while another mesh it owns is
//     still live, so one process holds at most one live mesh.
//   - Health gate before env injection. Start waits (bounded, default 5s) for
//     the instance's /healthz to answer 200 and fails with a clear error
//     otherwise. The caller then refuses to launch the child instead of
//     injecting a CLOUD_CODE_URL or GOOGLE_GEMINI_BASE_URL that points at a
//     dead mesh. EnsureHealthy is the explicit front-door re-check to run
//     before any environment is injected.
//   - Lifetime tied to the child. RunChild starts the child, watches both the
//     child and the mesh, and stops the mesh when the child exits (any exit
//     code). While a child lives the supervisor holds the mesh open: there is
//     no idle-TTL self-exit, and a library must never exit the process it is
//     linked into. IdleFor exposes the idle measurement so the shim (the
//     process owner) can apply its own TTL policy.
//   - Fail-fast, never a silent hang. If the mesh dies while the child lives,
//     RunChild kills the child, writes one visible line to the configured
//     error writer, and returns an error wrapping ErrMeshDied. A mesh-down
//     session is a dead session in both profiles (global routing), so a
//     half-dead mesh must never leave a TUI waiting on a port nobody answers.
//   - Stop while a child lives is refused. The caller cancels the child's
//     context (RunChild then kills the child and stops the mesh) or lets the
//     child exit first. Stop is idempotent once no child is alive.
//   - No key values. The Config carries the providers registry, which holds
//     env-var NAMES only; provider keys are resolved inside wireadp at
//     request time. Nothing in this package reads, stores or logs a key value.
//
// Sandbox posture: wireadp records its singleton in
// <AGY_PRO_HOME>/wireadp.json. Start pins AGY_PRO_HOME to ONE deterministic
// scope directory per agy-pro home, <AGY_PRO_HOME>/mesh-current, so every
// record file stays inside the sandbox and nothing accumulates: one spawn
// reuses that path and overwrites its record in place, atomically (FIX-54).
// Two properties keep the deterministic name as safe as the per-spawn random
// name it replaced:
//
//   - A foreign library-singleton record at the home root is never read: the
//     pin points at mesh-current, which only this package writes.
//   - A record left in mesh-current by another spawn (a live sibling in
//     another process, or a dead predecessor) is reserved over before the
//     ownership probe, so it can neither be adopted nor refuse this spawn:
//     per-spawn means every spawn binds its own listener. A dead record is
//     also ignored by wireadp's own stale-record path, which the real bind
//     still exercises and the tests pin.
//
// The probe's throwaway listener always runs the strict /v1internal posture,
// whatever the session opted into (FIX-66): it serves nothing and lives for
// milliseconds, so the passive window belongs to the session's own mesh only.
//
// Tests set AGY_PRO_HOME (and USERPROFILE or HOME) to temp dirs.
package mesh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"agypro/internal/paths"
	"agypro/internal/wireadp"
)

const (
	// DefaultHealthTimeout bounds the healthz gate in Start. A bound listener
	// answers the first probe; the budget exists for a slow machine, not for
	// a dead instance (that fails at the deadline with a clear error).
	DefaultHealthTimeout = 5 * time.Second

	// DefaultChildPollInterval is how often RunChild re-probes the mesh while
	// the child lives. The child's own exit is event-driven (cmd.Wait); the
	// poll only watches the mesh.
	DefaultChildPollInterval = 250 * time.Millisecond

	// healthGatePoll is the pause between healthz probes inside the gate.
	healthGatePoll = 20 * time.Millisecond

	// wireadpRecordFile is the singleton discovery record wireadp writes for
	// every instance it binds itself. The supervisor never needs to read it
	// (ownership is proven by the phase-1 probe, see the package doc); the
	// name is pinned here so the tests can simulate the silent record-write
	// failure the round-3b review described.
	wireadpRecordFile = "wireadp.json"

	// meshScopeDirName is the ONE deterministic scope directory per agy-pro
	// home (FIX-54). Every spawn reuses <AGY_PRO_HOME>/mesh-current instead
	// of creating a fresh mesh-<random> directory, so nothing accumulates:
	// the discovery record is overwritten in place, atomically, once per
	// spawn. Nothing in this package ever deletes a file or directory.
	meshScopeDirName = "mesh-current"

	// meshAuditFileName is the audit file used when the caller leaves
	// AuditPath empty: one deterministic file per install, appended across
	// spawns, so an unset path cannot accumulate one file per spawn either.
	meshAuditFileName = "wireadp-audit.jsonl"

	// meshProbeAuditFileName is the ownership probe's audit file. The probe
	// serves no request, so the file is never written; the fixed name keeps
	// even a future write from accumulating.
	meshProbeAuditFileName = "probe-audit.jsonl"
)

// startServer is the wireadp.Start seam (test injection, contract §2.1).
var startServer = wireadp.Start

var (
	// startMu serializes Start: the one-live-mesh check and the AGY_PRO_HOME
	// pin must not interleave. A Start is rare (one per spawn) and short (a
	// bind plus a bounded health gate), so a coarse lock is the right trade.
	startMu sync.Mutex

	// liveMu guards the live-mesh slot.
	liveMu sync.Mutex
	// live is the one mesh this package is holding open, if any.
	live *Mesh
)

// Config configures one per-spawn mesh.
type Config struct {
	// wireadp.Config is embedded, so Registry, AuthKey, CatalogFunc,
	// RPCHooks, AuditPath, ResolveModel and Generate are set directly on the
	// mesh Config and reach wireadp verbatim. The mesh adds no auth policy of
	// its own: strict versus permissive is the front door's call.
	//
	// Set AuditPath explicitly for a per-session audit trail. An empty value
	// resolves to <AGY_PRO_HOME>/mesh-current/wireadp-audit.jsonl: one
	// deterministic file per install, appended across spawns, so an unset
	// path cannot accumulate one file per spawn (FIX-54).
	wireadp.Config

	// HealthTimeout bounds the healthz gate in Start (default 5s).
	HealthTimeout time.Duration

	// ChildPollInterval is the mesh-health poll interval while RunChild
	// watches a live child (default 250ms).
	ChildPollInterval time.Duration

	// ErrWriter receives the one visible line written when the mesh dies
	// mid-session (default os.Stderr).
	ErrWriter io.Writer
}

// withDefaults returns cfg with the supervisor defaults applied.
func (cfg Config) withDefaults() Config {
	if cfg.HealthTimeout <= 0 {
		cfg.HealthTimeout = DefaultHealthTimeout
	}
	if cfg.ChildPollInterval <= 0 {
		cfg.ChildPollInterval = DefaultChildPollInterval
	}
	if cfg.ErrWriter == nil {
		cfg.ErrWriter = os.Stderr
	}
	return cfg
}

// Mesh is one running per-spawn mesh plus the child it supervises.
type Mesh struct {
	cfg  Config
	srv  *wireadp.Server
	url  string
	port int

	mu        sync.Mutex
	stopped   bool
	child     *exec.Cmd
	childLive bool
}

// Start binds a per-spawn mesh on an ephemeral loopback port, waits for the
// healthz gate, and returns the handle. On any failure it returns a clear
// error and no live mesh: a failed spawn must never leave the caller with a
// half-configured session, an owned-but-unreferenced listener, or a wedged
// supervisor.
func Start(cfg Config) (*Mesh, error) {
	cfg = cfg.withDefaults()

	startMu.Lock()
	defer startMu.Unlock()

	liveMu.Lock()
	existing := live
	liveMu.Unlock()
	if existing != nil && !existing.isStopped() {
		return nil, fmt.Errorf("mesh: a mesh is already serving %s (port %d) in this process; the per-spawn posture allows one live mesh at a time, stop it before starting another", existing.URL(), existing.Port())
	}

	// Phase 1: the ownership probe (see the package doc). It proves the
	// adoption channels are clear and closes the listener it opened, so a
	// refused spawn never leaks one.
	if err := probeOwnership(cfg); err != nil {
		return nil, err
	}

	// Phase 2: the real per-spawn bind. The probe cleared the in-process
	// channel and reserved the scope record, so a returned server is
	// provably this supervisor's own bind: a refusal below may stop it.
	scope, err := pinSingletonScope()
	if err != nil {
		return nil, err
	}
	defer scope.restore()

	wireCfg := cfg.Config
	if wireCfg.AuditPath == "" {
		// Deterministic and appended: an unset path must not accumulate one
		// audit file per spawn inside the scope directory (FIX-54).
		wireCfg.AuditPath = filepath.Join(scope.dir, meshAuditFileName)
	}
	srv, err := startServer(wireCfg)
	if err != nil {
		return nil, fmt.Errorf("mesh: wireadp start failed: %w", err)
	}
	if srv == nil {
		return nil, errors.New("mesh: wireadp start returned no server")
	}
	if err := waitHealthy(cfg.HealthTimeout, srv); err != nil {
		// Provably ours, so closing it leaks nothing and wedges nothing.
		_ = srv.Stop()
		return nil, fmt.Errorf("mesh: refusing to launch, the health gate failed for %s (port %d): %w", srv.URL(), srv.Port(), err)
	}

	m := &Mesh{cfg: cfg, srv: srv, url: srv.URL(), port: srv.Port()}
	liveMu.Lock()
	live = m
	liveMu.Unlock()
	return m, nil
}

// probeOwnership runs phase 1 of Start: a wireadp.Start under the
// deterministic scope directory with a per-attempt random nonce, closed as
// soon as it returns.
//
// wireadp.Start can only hand back a server it did not bind for this call
// through its two adoption channels: the in-process singleton (an exact
// nonce match, otherwise an error) and the discovery record in the resolved
// agy-pro home. The scope record is reserved first, so that channel holds
// nothing adoptable, and the random nonce makes the in-process channel
// return an error instead of a server. A server that comes back is therefore
// provably a fresh bind owned by this supervisor, and it is closed
// immediately: the probe never leaves an owned-but-unreferenced listener,
// and a foreign instance is never adopted, contacted or signaled.
func probeOwnership(cfg Config) error {
	scope, err := pinSingletonScope()
	if err != nil {
		return err
	}
	defer scope.restore()

	// Whatever a sibling spawn left in the shared scope directory must not
	// decide this spawn's fate (see reserveScopeRecord).
	if err := reserveScopeRecord(scope); err != nil {
		return err
	}

	nonce, err := randomNonce()
	if err != nil {
		return err
	}
	probeCfg := buildProbeConfig(cfg, nonce, filepath.Join(scope.dir, meshProbeAuditFileName))

	srv, err := startServer(probeCfg)
	if err != nil {
		// A refused adopt (a live foreign in-process singleton with a
		// different nonce) lands here as an error: nothing was bound, nothing
		// to close, and the foreign instance was never contacted.
		return fmt.Errorf("mesh: refusing to launch, the per-spawn ownership probe failed: %w", err)
	}
	if srv == nil {
		return errors.New("mesh: refusing to launch, the per-spawn ownership probe returned no server")
	}
	if err := srv.Stop(); err != nil {
		return fmt.Errorf("mesh: refusing to launch, the ownership probe could not close its own listener on %s (port %d): %w", srv.URL(), srv.Port(), err)
	}
	return nil
}

// buildProbeConfig derives the ownership probe's wireadp.Config from the
// caller's config. Exactly three fields differ from the session's config:
//
//   - AuthKey is the per-attempt random nonce (the in-process adoption
//     channel requires an exact nonce match, so a live foreign singleton
//     returns an error instead of a server).
//   - AuditPath points at the probe's own file inside the scope directory.
//   - RequireV1InternalAuth is forced to TRUE (FIX-66). The probe's listener
//     lives for milliseconds and serves nothing, so it must not run the
//     passive /v1internal posture even when the session opted into it: that
//     documented exposure window belongs to the session's own mesh, never to
//     a throwaway listener. The pointer is set explicitly rather than left
//     nil so the intent is unambiguous in the code.
//
// Every other field is carried over verbatim.
func buildProbeConfig(cfg Config, nonce, auditPath string) wireadp.Config {
	probe := cfg.Config
	probe.AuthKey = nonce
	probe.AuditPath = auditPath
	probe.RequireV1InternalAuth = wireadp.BoolPtr(true)
	return probe
}

// scopeRecordReserved is the reservation record written into the scope
// directory before the ownership probe. It is valid JSON with an empty URL,
// which wireadp's adoption path treats as "nothing to adopt".
var scopeRecordReserved = []byte(`{"url":"","port":0}`)

// reserveScopeRecord overwrites the scope directory's discovery record with
// the reservation, atomically (temp file plus rename inside the directory,
// via the shared atomic-write helper; nothing is deleted).
//
// This is what preserves the cross-process isolation the per-spawn random
// scope directory used to give for free (FIX-54): under one shared name, a
// record left by another spawn must not be adoptable by this one. A live
// sibling's record would otherwise make this spawn refuse to launch, which
// would serialize hybrid sessions per agy-pro home; a dead predecessor's
// record is ignored by wireadp's stale-record path either way. Per-spawn
// means every spawn binds its own listener.
func reserveScopeRecord(scope *singletonScope) error {
	path := filepath.Join(scope.dir, wireadpRecordFile)
	if err := paths.AtomicWriteFile(path, scopeRecordReserved, 0o600); err != nil {
		return fmt.Errorf("mesh: cannot reserve the scope record %s: %w", path, err)
	}
	return nil
}

// randomNonce returns a fresh per-attempt nonce for the ownership probe.
func randomNonce() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("mesh: cannot generate the ownership-probe nonce: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// waitHealthy polls the instance's healthz until it answers 200 or the
// timeout expires, returning the last probe error on failure.
func waitHealthy(timeout time.Duration, srv *wireadp.Server) error {
	if timeout <= 0 {
		timeout = DefaultHealthTimeout
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		probeCtx, cancel := context.WithTimeout(context.Background(), timeout)
		err := srv.Healthz(probeCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !time.Now().Add(healthGatePoll).Before(deadline) {
			return lastErr
		}
		time.Sleep(healthGatePoll)
	}
}

// singletonScope pins AGY_PRO_HOME to the deterministic per-install scope
// directory for the duration of a wireadp.Start call. The pin keeps wireadp's
// discovery record inside that one directory (FIX-54: reused and overwritten
// in place, never accumulated) and away from the home root, where a foreign
// library-singleton record lives and is never read.
type singletonScope struct {
	dir     string
	restore func()
}

func pinSingletonScope() (*singletonScope, error) {
	home, err := paths.AgyProHome()
	if err != nil {
		return nil, fmt.Errorf("mesh: cannot resolve the agy-pro home: %w", err)
	}
	dir := filepath.Join(home, meshScopeDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mesh: cannot create the scope directory %s: %w", dir, err)
	}
	prev, had := os.LookupEnv(paths.EnvAgyProHome)
	if err := os.Setenv(paths.EnvAgyProHome, dir); err != nil {
		return nil, fmt.Errorf("mesh: cannot pin %s: %w", paths.EnvAgyProHome, err)
	}
	return &singletonScope{
		dir: dir,
		restore: func() {
			if had {
				_ = os.Setenv(paths.EnvAgyProHome, prev)
				return
			}
			_ = os.Unsetenv(paths.EnvAgyProHome)
		},
	}, nil
}

// URL returns the base URL agy's CLOUD_CODE_URL (P1) or
// GOOGLE_GEMINI_BASE_URL (P2) must point at.
func (m *Mesh) URL() string { return m.url }

// Port returns the ephemeral loopback port the mesh listens on.
func (m *Mesh) Port() int { return m.port }

// Healthz probes the mesh. It is the raw passthrough; EnsureHealthy adds the
// refuse-to-launch wording the front door needs.
func (m *Mesh) Healthz(ctx context.Context) error {
	m.mu.Lock()
	stopped, srv := m.stopped, m.srv
	m.mu.Unlock()
	if stopped {
		return fmt.Errorf("mesh: the mesh on %s (port %d) is stopped", m.url, m.port)
	}
	if err := srv.Healthz(ctx); err != nil {
		return fmt.Errorf("mesh: the mesh on %s (port %d) is not answering: %w", m.url, m.port, err)
	}
	return nil
}

// EnsureHealthy is the front door: the caller invokes it before injecting any
// environment into the child. A dead or stopped mesh returns an error naming
// the port and the URL, so the session is refused instead of launched
// half-configured.
func (m *Mesh) EnsureHealthy(ctx context.Context) error {
	if err := m.Healthz(ctx); err != nil {
		return fmt.Errorf("mesh: refusing to launch, %w", err)
	}
	return nil
}

// IdleFor reports how long the mesh has been idle. The supervisor never acts
// on it: there is no idle-TTL self-exit, and a library must not exit the
// process it is linked into. It is exposed for the shim's TTL policy only.
func (m *Mesh) IdleFor() time.Duration { return m.srv.IdleFor() }

// Stop shuts the mesh down. It is idempotent.
//
// While a supervised child is alive, Stop is REFUSED: cancel the child's
// context (RunChild then kills the child and stops the mesh) or let the child
// exit first. That keeps a stray Stop from stranding a live session on a dead
// port.
func (m *Mesh) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.childLive {
		if m.child != nil && m.child.Process != nil {
			return fmt.Errorf("mesh: refusing to stop while the supervised child (pid %d) is alive; cancel its context or let it exit", m.child.Process.Pid)
		}
		// The child is registered before it is started (RunChild), so a Stop
		// that lands inside that window must also be refused.
		return errors.New("mesh: refusing to stop while a child is being started; cancel its context or let it exit")
	}
	return m.stopLocked()
}

// stopInternal stops the mesh without the child-liveness refusal. It runs on
// RunChild's exit paths, where the child has already been reaped or never
// started.
func (m *Mesh) stopInternal() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

// stopLocked stops the server and clears the process-wide live-mesh slot.
// m.mu must be held.
func (m *Mesh) stopLocked() error {
	if m.stopped {
		return nil
	}
	m.stopped = true
	err := m.srv.Stop()
	liveMu.Lock()
	if live == m {
		live = nil
	}
	liveMu.Unlock()
	return err
}

func (m *Mesh) isStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}
