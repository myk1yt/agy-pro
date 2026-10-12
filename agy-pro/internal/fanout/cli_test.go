package fanout

import (
	"bytes"
	"strings"
	"testing"
)

// S-8: --tools parsing is fail-closed. A "restrictive-looking" value must not
// silently grant write/exec through a substring match ("nowrite" used to
// enable write_file).
func TestParseToolTokensFailClosed(t *testing.T) {
	cases := []struct {
		spec    string
		want    string
		errWant bool
	}{
		{spec: "read,search", want: "read,search"},
		{spec: "read,search,write,exec", want: "read,search,write,exec"},
		{spec: "exec,write", want: "write,exec"}, // canonical order
		{spec: "read, SEARCH ,write", want: "read,search,write"},
		{spec: "read,read", want: "read"}, // dedupe
		{spec: "", want: ""},
		{spec: "read,search,nowrite", errWant: true},
		{spec: "noexec", errWant: true},
		{spec: "exec-disabled", errWant: true},
		{spec: "read,search,write_file", errWant: true},
		{spec: "read;search", errWant: true},
	}
	for _, tc := range cases {
		got, err := ParseToolTokens(tc.spec)
		if tc.errWant {
			if err == nil {
				t.Errorf("ParseToolTokens(%q) = %q; want error", tc.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseToolTokens(%q) unexpected error: %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseToolTokens(%q) = %q; want %q", tc.spec, got, tc.want)
		}
	}
}

// S-8: the CLI surface rejects unknown tokens before anything else runs.
func TestRunRejectsUnknownToolToken(t *testing.T) {
	var out, errw bytes.Buffer
	code := Run([]string{"-tools", "read,search,nowrite", "-prompt", "x"}, nil, &out, &errw)
	if code != 2 {
		t.Fatalf("exit code = %d; want 2 (usage error)", code)
	}
	if !strings.Contains(errw.String(), `unknown tool "nowrite"`) {
		t.Errorf("expected the unknown-token error, got %q", errw.String())
	}
	if strings.Contains(out.String(), "Swarm") {
		t.Errorf("no swarm output must be produced on a usage error:\n%s", out.String())
	}

	// The default tool spec still parses.
	if _, err := ParseToolTokens("read,search"); err != nil {
		t.Errorf("default --tools rejected: %v", err)
	}
}
