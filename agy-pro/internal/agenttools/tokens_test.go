package agenttools

import (
	"agypro/internal/testfixtures"
	"strings"
	"testing"
)

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

// TestParseToolTokens_MasksKeyOperand (B2): a misdirected
// run_command that pastes a key into --tools must not write the full key into
// the session transcript — only the first 5 characters. Benign unknown tokens
// stay readable (`unknown tool "nowrite"` is a real CLI assertion downstream).
func TestParseToolTokens_MasksKeyOperand(t *testing.T) {
	secret := "sk-XYZZYQUUX-89abcdef01234567"
	_, err := ParseToolTokens("read," + secret)
	if err == nil {
		t.Fatal("key-shaped tool token accepted")
	}
	runes := []rune(secret)
	for n := 6; n <= len(runes); n++ {
		for i := 0; i+n <= len(runes); i++ {
			frag := string(runes[i : i+n])
			if strings.Contains(err.Error(), frag) {
				t.Fatalf("error leaks %d runes of the key: %q in %q", n, frag, err)
			}
		}
	}
	// Benign operands stay actionable.
	_, err = ParseToolTokens("read,search,nowrite")
	if err == nil || !strings.Contains(err.Error(), `"nowrite"`) {
		t.Errorf("benign token must stay readable, got %v", err)
	}
}

// TestParseToolTokens_MasksOriginalCaseOperand (E9): the error mask is derived
// from the ORIGINAL-case token, not the case-folded copy. A case-folded AIza
// token loses its credential-shape prefix and, when it carries -/_ separators,
// also fails the contiguous-run heuristic — so masking the folded copy echoed
// the key (folded) instead of a mask.
func TestParseToolTokens_MasksOriginalCaseOperand(t *testing.T) {
	// AIza + exactly 35 chars of [0-9A-Za-z_-]: the Google key shape in
	// credentialShapeRe, with a separator to defeat the contiguous-run pass.
	const aiza = testfixtures.AIzaAltBody
	_, err := ParseToolTokens("read," + aiza)
	if err == nil {
		t.Fatal("AIza-shaped tool token accepted")
	}
	if !strings.Contains(err.Error(), `"AIzaS****"`) {
		t.Errorf("error must carry the original-case mask AIzaS****, got %q", err)
	}
	if strings.Contains(err.Error(), "azias") {
		t.Errorf("error must not echo the case-folded copy: %q", err)
	}
	runes := []rune(aiza)
	for n := 6; n <= len(runes); n++ {
		for i := 0; i+n <= len(runes); i++ {
			if frag := string(runes[i : i+n]); strings.Contains(err.Error(), frag) {
				t.Fatalf("error leaks %d runes of the key: %q in %q", n, frag, err)
			}
		}
	}
	// Benign mixed-case unknown tokens keep their readable form (no masking of
	// ordinary operands — the fold for membership stays).
	_, err = ParseToolTokens("NOWRITE")
	if err == nil || !strings.Contains(err.Error(), `"NOWRITE"`) {
		t.Errorf("benign mixed-case token must stay readable, got %v", err)
	}
}
