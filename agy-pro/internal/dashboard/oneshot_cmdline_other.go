//go:build !windows

package dashboard

import "os/exec"

// setRawCmdLine is a no-op off Windows: .cmd/.bat batch wrapping (and thus the
// raw command-line override) is a Windows-only spawn hazard, and
// isBatchScript reports false there, so batchCmd never takes this path.
// Copied from internal/launcher/batchwrap_other.go - keep in sync.

func setRawCmdLine(cmd *exec.Cmd, line string) {}
