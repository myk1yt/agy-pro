//go:build windows

package dashboard

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestConfigureDetachedWindowsFlags pins the Windows detachment: the child must
// get DETACHED_PROCESS (no console to inherit or lose) plus
// CREATE_NEW_PROCESS_GROUP (Ctrl-C in the TUI console must not reach it), and
// must never paint a window. The assertions live in a windows-tagged file
// because SysProcAttr.CreationFlags does not exist off Windows; the cross-OS
// shape (stdio nil, allow-list env, no waiting) is asserted in spawn_test.go.
func TestConfigureDetachedWindowsFlags(t *testing.T) {
	cmd := exec.Command("node", "x.js")
	configureDetached(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("configureDetached left SysProcAttr nil")
	}
	if cmd.SysProcAttr.CreationFlags&detachedProcess == 0 {
		t.Fatalf("CreationFlags = %#x, want DETACHED_PROCESS set", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", cmd.SysProcAttr.CreationFlags)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("configureDetached did not hide the window")
	}
}

// TestConfigureDetachedKeepsRawCmdLine pins the interaction with the batch-shim
// path: the B9 raw command line and the detachment flags live on the same
// SysProcAttr, so setting one must not clear the other.
func TestConfigureDetachedKeepsRawCmdLine(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	setRawCmdLine(cmd, buildBatchCmdLine("agy-tokens.cmd", []string{"--serve"}))
	configureDetached(cmd)
	if cmd.SysProcAttr.CmdLine == "" {
		t.Fatal("configureDetached dropped the raw command line of a batch shim")
	}
	if cmd.SysProcAttr.CreationFlags&detachedProcess == 0 {
		t.Fatalf("CreationFlags = %#x, want DETACHED_PROCESS set", cmd.SysProcAttr.CreationFlags)
	}
}
