//go:build windows

package dashboard

import (
	"os/exec"
	"syscall"
)

// detachedProcess is CreateProcess's DETACHED_PROCESS flag: the child gets no
// console at all. syscall exports CREATE_NEW_PROCESS_GROUP but not this one, so
// it is spelled here with its documented value (winbase.h).
const detachedProcess = 0x00000008

// configureDetached puts the child in its own process group with no console and
// no window, so closing the TUI (or its console) cannot take the dashboard
// server down with it. The flag block is ORed onto whatever SysProcAttr the
// caller already set (the batch-shim path sets CmdLine), which is the same
// mutation idiom internal/fanout/native_seat_windows.go and
// internal/launcher/batchwrap_windows.go use for their raw command lines.
func configureDetached(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}
