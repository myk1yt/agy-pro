package main

// FIX-F4 test-process guard.
//
// codesnap.Take binds every manifest it writes to a per-run key stored under the
// agy-pro state home (~/.agy-pro/ledger-keys). The CLI pins in this package drive
// the real `code write`/`rollback` phases, which fabricate run directories through
// internal/codework, so a plain `go test ./cmd/agy/` would otherwise leave key files
// in an operator's real home. AGY_PRO_HOME is the shipped override the rest of this
// package's tests already use through t.Setenv; a value the caller set is kept, and
// a test that wants its own home still overrides per test.

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGY_PRO_HOME") == "" {
		dir, err := os.MkdirTemp("", "agy-cmd-home-")
		if err != nil {
			panic("cmd/agy tests: cannot create a temporary agy-pro home: " + err.Error())
		}
		_ = os.Setenv("AGY_PRO_HOME", dir)
		code := m.Run()
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}
