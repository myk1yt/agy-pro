package codework

// FIX-F4 test-process guard.
//
// codesnap.Take binds every manifest it writes to a per-run key stored under the
// agy-pro state home (~/.agy-pro/ledger-keys). The pins in this package fabricate
// run directories to exercise the phases and the pre-consent gate, so a plain
// `go test ./internal/codework/` would otherwise leave key files in an operator's
// real home. AGY_PRO_HOME is the shipped override (providers.Home) that codework's
// own production code already honours; a value the caller set is kept, so a test
// that wants its own home still gets it through t.Setenv.

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGY_PRO_HOME") == "" {
		dir, err := os.MkdirTemp("", "agy-codework-home-")
		if err != nil {
			panic("codework tests: cannot create a temporary agy-pro home: " + err.Error())
		}
		_ = os.Setenv("AGY_PRO_HOME", dir)
		code := m.Run()
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}
