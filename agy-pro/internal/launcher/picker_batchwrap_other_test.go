//go:build !windows

package launcher

import (
	"os/exec"
	"testing"
)

// assertBatchWrapStamp is a documented no-op on non-Windows platforms: the
// runtime guard in TestExecuteForcedNative_BatchWrapGuard never calls it
// here. The symbol exists so the caller compiles on every platform; the
// Windows-only assertions (SysProcAttr.CmdLine) live in the windows build
// file.
func assertBatchWrapStamp(t *testing.T, gotName string, gotArgs []string, gotCmd *exec.Cmd) {
	t.Helper()
}
