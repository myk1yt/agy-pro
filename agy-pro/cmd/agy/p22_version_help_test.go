package main

import (
	"strings"
	"testing"
)

func TestP22VersionHelpPrintsUsageAndBareVersionKeepsBanner(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dispatch  string
		wantUsage bool
	}{
		{name: "long help", dispatch: "p22-version-help", wantUsage: true},
		{name: "short help", dispatch: "p22-version-short-help", wantUsage: true},
		{name: "bare version", dispatch: "p22-version-bare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runDispatchHelperCaptureStderr(t, tc.dispatch)
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr)
			}
			hasUsage := strings.Contains(stdout, "Usage: agy pro version")
			if hasUsage != tc.wantUsage {
				t.Fatalf("usage presence = %v, want %v; stdout=%q", hasUsage, tc.wantUsage, stdout)
			}
			if !tc.wantUsage && !strings.Contains(stdout, "agy-pro version") {
				t.Fatalf("bare version banner missing: %q", stdout)
			}
			t.Logf("exit=%d stdout=%q", code, stdout)
		})
	}
}
