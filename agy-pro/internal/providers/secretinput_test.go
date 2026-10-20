package providers

import (
	"bufio"
	"bytes"
	"errors"
	"os"
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
	got, err := promptSecret(r, &out, nil, "API Key", "keep current")
	if got != "keep current" || err != nil {
		t.Errorf("promptSecret empty = (%q, %v); want (keep current, nil)", got, err)
	}
	// The prompt itself must not echo any secret (none was typed here).
	if strings.Contains(out.String(), "keep current: keep current") {
		t.Errorf("unexpected double echo: %q", out.String())
	}
}

func TestPromptSecret_NonConsoleReadsLine(t *testing.T) {
	var out bytes.Buffer
	r := bufio.NewReader(strings.NewReader("sk-planted-key-99887766\n"))
	got, err := promptSecret(r, &out, nil, "API Key", "")
	if got != "sk-planted-key-99887766" || err != nil {
		t.Errorf("promptSecret = (%q, %v)", got, err)
	}
	assertNoLeak(t, out.String(), "sk-planted-key-99887766")
}

// TestPromptSecret_InterruptAbortsNotSilentKeep (B6): Ctrl+C during masked
// key entry must abort — it must NOT fall through to the empty-value default
// path that silently keeps the existing key.
func TestPromptSecret_InterruptAbortsNotSilentKeep(t *testing.T) {
	old := readConsoleSecretFn
	readConsoleSecretFn = func(f *os.File, echo *secretEcho) (string, error) {
		return "", errSecretInterrupted
	}
	defer func() { readConsoleSecretFn = old }()

	var out bytes.Buffer
	// Buffered()==0 routes the read through the console seam.
	r := bufio.NewReader(strings.NewReader(""))
	got, err := promptSecret(r, &out, nil, "API Key (press Enter to keep OLD_KEY, or paste new key)", "")
	if !errors.Is(err, errSecretInterrupted) {
		t.Fatalf("interrupt must return errSecretInterrupted, got (value=%q, err=%v)", got, err)
	}
	if got != "" {
		t.Errorf("interrupt must not return a value (would be treated as a key), got %q", got)
	}
}

// TestWizardTranscript_InterruptDoesNotKeepExistingKey (B6): a Ctrl+C during
// the "press Enter to keep" prompt aborts the wizard instead of silently
// keeping the existing key.
func TestWizardTranscript_InterruptDoesNotKeepExistingKey(t *testing.T) {
	old := readConsoleSecretFn
	readConsoleSecretFn = func(f *os.File, echo *secretEcho) (string, error) {
		return "", errSecretInterrupted
	}
	defer func() { readConsoleSecretFn = old }()

	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	existing := "sk-EXISTINGKEY-should-not-be-kept-1234"
	t.Setenv("INTPROV_API_KEY", existing)

	// Name + base URL lines are consumed; the key prompt then hits the
	// console seam (buffer drained) and returns the interrupt.
	input := "intprov\nhttp://127.0.0.1:9/v1\n"
	out := &bytes.Buffer{}
	err := InteractiveSetupWizard(strings.NewReader(input), out)
	if err == nil {
		t.Fatal("wizard must abort on Ctrl+C during key entry")
	}
	if !errors.Is(err, errSecretInterrupted) {
		t.Errorf("abort must carry errSecretInterrupted, got %v", err)
	}
	transcript := out.String()
	if strings.Contains(transcript, "Using existing value") {
		t.Errorf("interrupt silently kept the existing key:\n%s", transcript)
	}
	assertNoLeak(t, transcript, existing)
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

// TestWizardTranscript_KeyShapedNameRefusedAtPrompt (C5/U8 + FIX-7B): a pasted
// key in the provider-name slot passes the B5 name charset, and FIX-7B closes
// the door the C5 pin used to watch from the transcript side: the name is
// refused at the prompt, before the base-URL prompt and before any write, so the
// derived env name that embedded it verbatim
// ("AKIAIOSFODNN7EXAMPLE" -> "AKIAIOSFODNN7EXAMPLE_API_KEY") is never computed
// for this run at all. The refusal names the field and never the value.
func TestWizardTranscript_KeyShapedNameRefusedAtPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv("AKIAIOSFODNN7EXAMPLE_API_KEY", "")

	pasted := "AKIAIOSFODNN7EXAMPLE"
	derived := "AKIAIOSFODNN7EXAMPLE_API_KEY"
	keyValue := "sk-REALKEY-0011223344556677"
	input := strings.Join([]string{
		pasted,
		"http://127.0.0.1:9/v1",
		keyValue,
		"model-x",
	}, "\n") + "\n"

	out := &bytes.Buffer{}
	err := InteractiveSetupWizard(strings.NewReader(input), out)
	if err == nil {
		t.Fatalf("the wizard accepted a key-shaped provider name:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "provider name") || !strings.Contains(err.Error(), "value hidden") {
		t.Errorf("refusal = %q; want the value-free provider-name refusal", err)
	}
	transcript := out.String()
	assertNoLeak(t, err.Error(), pasted)
	assertNoLeak(t, transcript, pasted)
	assertNoLeak(t, transcript, keyValue)
	if strings.Contains(transcript, derived) {
		t.Errorf("transcript echoes the derived env name that embeds the pasted key:\n%s", transcript)
	}
	// Fail closed at the name prompt: the base-URL prompt was never reached.
	if strings.Contains(transcript, "OpenAI-compatible Base URL") {
		t.Errorf("the name refusal landed after the URL prompt:\n%s", transcript)
	}
	if _, statErr := os.Stat(provPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a refused name must write nothing; stat error = %v", statErr)
	}
}

// TestWizardNameLabelWithholdsDerivedEnv is the C5 display half after FIX-7B.
// The door now refuses this class, so the remaining exposure is a hand-edited
// registry (or a future ingest path): the wizard's name label must render
// through the registry-slot mask, and the derived env name must be withheld
// whenever that same mask changes the name. MaskOperand alone is the identity on
// the underscore family ("sk_live_0123456789abcdef"), which is why the rule now
// reads MaskSlot.
func TestWizardNameLabelWithholdsDerivedEnv(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withhold bool
	}{
		{"AKIAIOSFODNN7EXAMPLE", true},
		{"sk_live_0123456789abcdef", true},
		{"tok_a1b2c3d4e5f6g7h8i9j0", true},
		{"pk_live_4f9a2b7c1d3e5f6a", true},
		{"key_0123456789abcdef", true},
		{"acme", false},
		{"nw", false},
		{"my-provider", false},
		{"key_backup", false},
		{"ANTHROPIC_API_KEY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := wizardWithholdsDerivedEnv(tc.name); got != tc.withhold {
				t.Fatalf("wizardWithholdsDerivedEnv(%q) = %v; want %v", tc.name, got, tc.withhold)
			}
			label := wizardProvLabel(tc.name)
			derived := defaultEnvFor(tc.name)
			if tc.withhold {
				// defaultEnvFor embeds the name in the derived variable, which is
				// exactly why the wizard withholds it. The label must neither
				// render the name raw nor carry the derived variable.
				if !strings.Contains(derived, strings.ToUpper(tc.name)) {
					t.Fatalf("fixture: defaultEnvFor(%q) = %q does not embed the name", tc.name, derived)
				}
				if strings.Contains(label, tc.name) || strings.Contains(label, derived) {
					t.Fatalf("wizardProvLabel(%q) = %q; the credential-shaped name rendered raw", tc.name, label)
				}
				if !strings.Contains(label, "****") {
					t.Fatalf("wizardProvLabel(%q) = %q; want the canonical mask", tc.name, label)
				}
				return
			}
			if label != tc.name {
				t.Fatalf("wizardProvLabel(%q) = %q; a benign name must stay byte-identical", tc.name, label)
			}
			if strings.Contains(label, "****") {
				t.Fatalf("wizardProvLabel(%q) = %q; a benign name must not be masked", tc.name, label)
			}
		})
	}
}

// TestWizardTranscript_BenignNameKeepsDerivedEnvReadable (C5 UX guard): a
// benign provider name still shows its derived env name and long model IDs
// stay verbatim, so the copy-paste completion hints keep working.
func TestWizardTranscript_BenignNameKeepsDerivedEnvReadable(t *testing.T) {
	tmpDir := t.TempDir()
	provPath := filepath.Join(tmpDir, "providers.json")
	t.Setenv(EnvProviders, provPath)
	t.Setenv("ACME_API_KEY", "")

	modelID := "claude-3-5-sonnet-20241022"
	input := strings.Join([]string{
		"acme",
		"http://127.0.0.1:9/v1",
		"sk-BENIGNKEY-00112233445566",
		modelID,
		"", "", "", "Y", "", "N", "N",
		"", "",
	}, "\n") + "\n"

	out := &bytes.Buffer{}
	if err := InteractiveSetupWizard(strings.NewReader(input), out); err != nil {
		t.Fatalf("InteractiveSetupWizard returned error: %v\nOutput: %s", err, out.String())
	}
	transcript := out.String()
	if !strings.Contains(transcript, "ACME_API_KEY") {
		t.Errorf("benign derived env name must stay readable:\n%s", transcript)
	}
	if !strings.Contains(transcript, modelID) {
		t.Errorf("long model ID must stay readable (C3 UX):\n%s", transcript)
	}
}
