//go:build windows

package launcher

import (
	"os/exec"
	"syscall"
)

// setRawCmdLine overrides the process command line so cmd.exe receives the
// hand-escaped batch line verbatim (Go's default argv quoting produces \"
// escapes that cmd.exe does not understand). Copied from
// internal/fanout/native_seat_windows.go (B9 picker guard) — keep in sync.

func setRawCmdLine(cmd *exec.Cmd, line string) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
}
