package providers

import (
	"bufio"
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// assertNoLeak fails when haystack contains any substring of secret longer
// than 5 runes (U8 hard rule: nothing beyond the first 5 runes is ever shown).
func assertNoLeak(t *testing.T, haystack, secret string) {
	t.Helper()
	runes := []rune(secret)
	for n := 6; n <= len(runes); n++ {
		for i := 0; i+n <= len(runes); i++ {
			frag := string(runes[i : i+n])
			if strings.Contains(haystack, frag) {
				t.Fatalf("output leaks %d runes of the key: %q", n, frag)
			}
		}
	}
}

func TestSecretEcho_MasksAfterFiveRunes(t *testing.T) {
	var buf bytes.Buffer
	echo := newSecretEcho(&buf)
	for _, r := range "sk-SECRET-1234567890" {
		echo.add(r)
	}
	got := buf.String()
	// First 5 runes verbatim, "*" for every later rune.
	want := "sk-SE" + strings.Repeat("*", len([]rune("sk-SECRET-1234567890"))-5)
	if got != want {
		t.Errorf("echo = %q; want %q", got, want)
	}
	assertNoLeak(t, got, "sk-SECRET-1234567890")
	if echo.masked() != "sk-SE****" {
		t.Errorf("masked() = %q; want %q", echo.masked(), "sk-SE****")
	}
}

func TestSecretEcho_Backspace(t *testing.T) {
	var buf bytes.Buffer
	echo := newSecretEcho(&buf)
	for _, r := range "abcdefg" {
		echo.add(r)
	}
	echo.backspace()
	// Echo stream: "abcde*" then the erase cell for the masked rune.
	if !strings.HasSuffix(buf.String(), "*\b \b") {
		t.Errorf("echo after backspace = %q", buf.String())
	}
	if echo.masked() != "abcde****" {
		t.Errorf("masked after backspace = %q; want %q", echo.masked(), "abcde****")
	}
}

func TestPromptSecret_DefaultOnEmpty(t *testing.T) {
	var out bytes.Buffer
	r := bufio.NewReader(strings.NewReader("\n"))
	got, isEOF := promptSecret(r, &out, nil, "API Key", "keep current")
	if got != "keep current" || isEOF {
		t.Errorf("promptSecret empty = (%q, %v); want (keep current, false)", got, isEOF)
	}
	// The prompt itself must not echo any secret (none was typed here).
	if strings.Contains(out.String(), "keep current: keep current") {
		t.Errorf("unexpected double echo: %q", out.String())
	}
}

func TestPromptSecret_NonConsoleReadsLine(t *testing.T) {
	var out bytes.Buffer
	r := bufio.NewReader(strings.NewReader("sk-planted-key-99887766\n"))
	got, isEOF := promptSecret(r, &out, nil, "API Key", "")
	if got != "sk-planted-key-99887766" || isEOF {
		t.Errorf("promptSecret = (%q, %v)", got, isEOF)
	}
	assertNoLeak(t, out.String(), "sk-planted-key-99887766")
}

// TestWizardTranscript_MasksKeyEcho drives the full wizard with a planted key
// and asserts the captured transcript never shows more than the first 5 runes
// of it (contract §2.11 wizard-transcript test).
func TestWizardTranscript_MasksKeyEcho(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv("LEAKPROV_API_KEY", "")

	planted := "sk-PLANTEDKEY-8f3a2b1c9d0e7766"
	// Scripted entry: name, base URL, key value, model answers, then the
	// failing-probe "save anyway" default and "no more models" default.
	input := strings.Join([]string{
		"leakprov",
		"http://127.0.0.1:9/v1",
		planted,
		"leak-model-1",
		"",  // family (accept inferred)
		"",  // context length default
		"",  // max output default
		"Y", // supports tools
		"",  // allowed tools default
		"N", // no thinking
		"N", // no vision
		"",  // probe choice default 1 = save anyway
		"",  // more models default N
	}, "\n") + "\n"

	out := &bytes.Buffer{}
	if err := InteractiveSetupWizard(strings.NewReader(input), out); err != nil {
		t.Fatalf("InteractiveSetupWizard returned error: %v\nOutput: %s", err, out.String())
	}
	transcript := out.String()
	assertNoLeak(t, transcript, planted)

	// The canonical mask IS shown for the confirmation.
	if !strings.Contains(transcript, MaskSecret(planted)) {
		t.Errorf("transcript lacks the canonical mask %q:\n%s", MaskSecret(planted), transcript)
	}
}
