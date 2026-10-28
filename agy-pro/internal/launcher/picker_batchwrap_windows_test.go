//go:build windows

package launcher

import (
	"os/exec"
	"strings"
	"testing"
)

// assertBatchWrapStamp (B9) holds the Windows-only half of
// TestExecuteForcedNative_BatchWrapGuard: the .cmd target is wrapped in
// cmd.exe /d /v:off /s /c and the raw, hand-escaped command line is stamped
// on SysProcAttr.CmdLine. It lives behind the windows build constraint
// because SysProcAttr.CmdLine is a Windows-only field; the caller's runtime
// guard decides when it runs.
func assertBatchWrapStamp(t *testing.T, gotName string, gotArgs []string, gotCmd *exec.Cmd) {
	t.Helper()
	if gotName != "cmd.exe" {
		t.Errorf("batch target must be wrapped in cmd.exe, got spawn %q", gotName)
	}
	if len(gotArgs) != 0 {
		t.Errorf("wrap puts the whole line on the raw command line, got args %v", gotArgs)
	}
	if gotCmd == nil || gotCmd.SysProcAttr == nil || gotCmd.SysProcAttr.CmdLine == "" {
		t.Fatal("setRawCmdLine did not stamp the raw command line")
	}
	if !strings.Contains(gotCmd.SysProcAttr.CmdLine, "real-agy.cmd") {
		t.Errorf("raw line must carry the batch target: %q", gotCmd.SysProcAttr.CmdLine)
	}
	if !strings.HasPrefix(gotCmd.SysProcAttr.CmdLine, `cmd.exe /d /v:off /s /c "`) {
		t.Errorf("raw line must be the /d /v:off /s /c wrap: %q", gotCmd.SysProcAttr.CmdLine)
	}
}
