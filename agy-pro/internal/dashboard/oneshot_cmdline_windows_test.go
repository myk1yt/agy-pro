//go:build windows

package dashboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestBatchCmdRawCmdLineWindows pins the Windows half of the batch wrap: a
// .cmd target MUST go through cmd.exe with a raw command line, because Go's
// default argv quoting produces \" escapes cmd.exe does not understand. The
// field only exists on Windows, so this assertion lives in a tagged file.
func TestBatchCmdRawCmdLineWindows(t *testing.T) {
	shim := `C:\Users\example\AppData\Roaming\npm\agy-tokens.cmd`
	wrapped := batchCmd(context.Background(), shim, refreshHTMLFlag, refreshNoOpenFlag)
	if !strings.EqualFold(filepath.Base(wrapped.Path), "cmd.exe") {
		t.Fatalf("wrap path = %q, want cmd.exe", wrapped.Path)
	}
	if wrapped.SysProcAttr == nil || wrapped.SysProcAttr.CmdLine == "" {
		t.Fatal("the wrap carries no raw command line (Go's argv quoting breaks cmd.exe)")
	}
	line := wrapped.SysProcAttr.CmdLine
	for _, want := range []string{"/d", "/v:off", "/s", "/c", "agy-tokens.cmd", refreshHTMLFlag, refreshNoOpenFlag} {
		if !strings.Contains(line, want) {
			t.Fatalf("raw command line %q is missing %q", line, want)
		}
	}
}
