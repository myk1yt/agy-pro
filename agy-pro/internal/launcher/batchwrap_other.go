//go:build !windows

package launcher

import "os/exec"

// setRawCmdLine is a no-op off Windows: .cmd/.bat batch wrapping (and thus
// raw command-line override) is a Windows-only spawn hazard. Copied from
// internal/fanout/native_seat_other.go (B9 picker guard) — keep in sync.

func setRawCmdLine(cmd *exec.Cmd, line string) {}
