package agenttools

import "testing"

// The token set is closed and shared by fanout (--tools) and the bridge
// (bridge-tools / AGY_PRO_BRIDGE_TOOLS). R1-9: the parser moved here from
// fanout/cli.go; the fanout wrapper test keeps the CLI surface covered, this
// test covers the canonical home directly.
func TestParseToolTokensClosedSet(t *testing.T) {
	cases := []struct {
		spec    string
		want    string
		errWant bool
	}{
		{spec: "read,search", want: "read,search"},
		{spec: "read,search,write,exec", want: "read,search,write,exec"},
		{spec: "write", want: "write"},
		{spec: "exec,write", want: "write,exec"}, // canonical order
		{spec: "read, SEARCH ,write", want: "read,search,write"},
		{spec: "read,read", want: "read"}, // dedupe
		{spec: "", want: ""},
		{spec: "read,search,nowrite", errWant: true},
		{spec: "noexec", errWant: true},
		{spec: "write_file", errWant: true}, // tool names are not tokens
		{spec: "replace_in_file", errWant: true},
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
