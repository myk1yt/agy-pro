package mesh

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// ErrMeshDied marks a session whose mesh died while the child was still
// alive. The front door turns it into a visible failure: a mesh-down session
// is a dead session in both profiles (global routing), never a silent hang.
var ErrMeshDied = errors.New("mesh died mid-session; the session cannot continue")

const (
	// childProbeTimeout bounds one mesh-health probe inside the watch loop.
	// The probe uses its own context so a canceled RunChild context cannot be
	// misread as a mesh death.
	childProbeTimeout = 2 * time.Second

	// childReapTimeout bounds the wait for a killed child to be reaped. A
	// process that survives SIGKILL/TerminateProcess this long is wedged in
	// the kernel; the supervisor reports the outcome and moves on.
	childReapTimeout = 5 * time.Second
)

// startChild is the cmd.Start seam (test injection, contract §2.1). RunChild
// registers the child in the supervisor slot BEFORE this call, so a
// concurrent Stop can never observe an unregistered live child.
var startChild = func(cmd *exec.Cmd) error { return cmd.Start() }

// RunChild starts cmd under the mesh's supervision and blocks until the child
// exits, the mesh dies, or ctx is canceled. cmd must not be started yet, and
// its stdio is left exactly as the caller wired it (the TUI child owns the
// terminal).
//
// The child is registered in the supervisor slot before it is started, so
// Stop is refused for the whole launch window, not only once the process
// exists.
//
// Refuse-to-launch: the child is started only after a fresh health check
// passes. A dead mesh returns an error and the child never starts, so no
// half-configured session exists; the mesh is stopped in that case.
//
// Outcomes:
//
//   - The child exits (any code): the mesh is stopped and the child's error
//     is returned (nil for exit 0, *exec.ExitError otherwise, so the caller
//     can map the exit code).
//   - The mesh dies while the child lives: the child is killed, one visible
//     line is written to the configured error writer, the mesh is stopped,
//     and an error wrapping ErrMeshDied is returned.
//   - ctx is canceled: the child is killed, the mesh is stopped, and the
//     context error is returned.
//   - cmd fails to start: the mesh is stopped and the start error is returned.
func (m *Mesh) RunChild(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("mesh: RunChild requires a command")
	}
	if cmd.Process != nil {
		return errors.New("mesh: RunChild requires a command that has not been started yet")
	}
	m.mu.Lock()
	switch {
	case m.stopped:
		m.mu.Unlock()
		return fmt.Errorf("mesh: refusing to launch, the mesh on %s (port %d) is stopped", m.url, m.port)
	case m.child != nil:
		m.mu.Unlock()
		return errors.New("mesh: a child is already attached to this mesh")
	}
	m.mu.Unlock()

	if err := m.EnsureHealthy(ctx); err != nil {
		_ = m.stopInternal()
		return err
	}

	// Register the child BEFORE it is started: a concurrent Stop (or a second
	// RunChild) must never observe a live child that is not in the slot yet
	// (round-3b FIX-42b). A failed start unregisters again.
	m.mu.Lock()
	m.child = cmd
	m.childLive = true
	m.mu.Unlock()

	if err := startChild(cmd); err != nil {
		m.mu.Lock()
		m.child = nil
		m.childLive = false
		m.mu.Unlock()
		_ = m.stopInternal()
		return fmt.Errorf("mesh: failed to start the child %q: %w", cmd.Path, err)
	}

	// Buffered so the waiter goroutine never blocks on send if a wedged child
	// outlives the reap budget.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	poll := time.NewTicker(m.cfg.ChildPollInterval)
	defer poll.Stop()

	for {
		select {
		case childErr := <-waitCh:
			// The child exited on its own (any code).
			return m.finishChildExit(cmd, childErr)
		case <-poll.C:
			// The child's own exit wins a tie: if both happened inside one
			// tick, the session ended on the child's terms.
			select {
			case childErr := <-waitCh:
				return m.finishChildExit(cmd, childErr)
			default:
			}
			probeCtx, cancel := context.WithTimeout(context.Background(), childProbeTimeout)
			probeErr := m.srv.Healthz(probeCtx)
			cancel()
			if probeErr == nil {
				continue
			}
			// Re-check the child once more before declaring the session
			// dead: a child that exited inside the probe window must report
			// its own outcome, not the mesh's.
			select {
			case childErr := <-waitCh:
				return m.finishChildExit(cmd, childErr)
			default:
			}
			return m.finishMeshDeath(cmd, waitCh, probeErr)
		case <-ctx.Done():
			m.killChild(cmd)
			_, _ = m.reapAndStop(cmd, waitCh)
			return fmt.Errorf("mesh: the supervised child (pid %d) was killed because the session context ended: %w", childPID(cmd), ctx.Err())
		}
	}
}

// finishChildExit handles the child's own exit (any code) with the wait
// result already in hand: the mesh's reason to live is gone, so it is stopped
// and the child's error is returned.
func (m *Mesh) finishChildExit(cmd *exec.Cmd, childErr error) error {
	m.mu.Lock()
	m.childLive = false
	m.mu.Unlock()
	stopErr := m.stopInternal()
	if childErr != nil {
		return childErr
	}
	if stopErr != nil {
		return fmt.Errorf("mesh: the child exited cleanly but stopping the mesh failed: %w", stopErr)
	}
	return nil
}

// finishMeshDeath handles a mesh that died while the child lived: kill the
// child, write the visible fail-fast line, stop what is left of the mesh, and
// return an error wrapping ErrMeshDied.
func (m *Mesh) finishMeshDeath(cmd *exec.Cmd, waitCh <-chan error, probeErr error) error {
	pid := childPID(cmd)
	m.writeVisible(probeErr, pid)
	m.killChild(cmd)
	_, _ = m.reapAndStop(cmd, waitCh)
	return fmt.Errorf("%w: the health probe on %s (port %d) failed: %v", ErrMeshDied, m.url, m.port, probeErr)
}

// reapAndStop waits for the child to be reaped, marks it exited and stops the
// mesh. It returns the child's exit error and the mesh's stop error.
func (m *Mesh) reapAndStop(cmd *exec.Cmd, waitCh <-chan error) (childErr error, stopErr error) {
	select {
	case childErr = <-waitCh:
	case <-time.After(childReapTimeout):
	}
	m.mu.Lock()
	m.childLive = false
	m.mu.Unlock()
	stopErr = m.stopInternal()
	return childErr, stopErr
}

// killChild terminates the child process. It is best-effort: the reap path
// stays bounded either way.
func (m *Mesh) killChild(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// writeVisible puts the fail-fast line on the configured error writer. It
// names the mesh URL and port, the probe error and the killed pid, and it
// carries no credential.
func (m *Mesh) writeVisible(probeErr error, pid int) {
	if m.cfg.ErrWriter == nil {
		return
	}
	fmt.Fprintf(m.cfg.ErrWriter, "mesh: %s (mesh %s, port %d; health probe failed: %v); the supervised child (pid %d) was killed\n",
		ErrMeshDied.Error(), m.url, m.port, probeErr, pid)
}

func childPID(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}
