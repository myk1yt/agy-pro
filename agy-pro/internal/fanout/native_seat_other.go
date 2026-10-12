//go:build !windows

package fanout

import "os/exec"

// setRawCmdLine is a no-op off Windows: .cmd/.bat batch wrapping (and thus
// raw command-line override) is a Windows-only spawn hazard. A batch target
// resolved on a non-Windows platform fails at spawn time as a failed seat.
func setRawCmdLine(cmd *exec.Cmd, line string) {}
