//go:build !windows

package dashboard

import (
	"os/exec"
	"syscall"
)

// configureDetached starts the child in a new session so it survives the parent
// (setsid detaches it from the controlling terminal's process group and signal
// delivery). Off Windows there is no console-hiding or creation-flag equivalent
// to set, so this is the whole detachment.
func configureDetached(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
