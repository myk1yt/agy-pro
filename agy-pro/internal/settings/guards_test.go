package settings

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/providers"
)

// assertNoLeak fails when haystack contains any substring of secret longer
// than 5 runes (U8 hard rule: nothing beyond the first 5 runes is ever shown).
// Same helper as providers/secretinput_test.go, kept local to the package.
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

// ---------------------------------------------------------------------------
// B2 — operand echoes at the settings Core boundary are masked.
// ---------------------------------------------------------------------------

// TestCoreSet_MasksKeyOperands (B2, core.go:123/135/142): a misdirected
// run_command with a pasted key in any operand slot must never write the full
// key into the session transcript. Only the first 5 characters may appear.
func TestCoreSet_MasksKeyOperands(t *testing.T) {
	secret := "sk-core-leak-89abcdef01234567"
	f := newFakeDeps()
	c := NewCore(f.deps())

	// core.go:123 — invalid default profile echoes the value.
	err := c.Set("default-profile", secret)
	if err == nil {
		t.Fatal("key-shaped profile accepted")
	}
	assertNoLeak(t, err.Error(), secret)

	// core.go:135 — native-seat-concurrency echoes the value.
	err = c.Set("native-seat-concurrency", secret)
	if err == nil {
		t.Fatal("key-shaped integer accepted")
	}
	assertNoLeak(t, err.Error(), secret)

	// core.go:142 — unknown settings key echoes the key operand.
	err = c.Set(secret, "x")
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	assertNoLeak(t, err.Error(), secret)

	// ProviderTest name operand (same echo class).
	err = c.ProviderTest(secret)
	if err == nil {
		t.Fatal("unknown provider accepted")
	}
	assertNoLeak(t, err.Error(), secret)
}

// TestCoreSet_BenignOperandsStayReadable: masking must not destroy ordinary
// error text — "unknown settings key \"nope\"" still shows what was typed.
func TestCoreSet_BenignOperandsStayReadable(t *testing.T) {
	f := newFakeDeps()
	c := NewCore(f.deps())
	err := c.Set("nope", "x")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("benign operand must stay readable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// B3 — defaultDoctor compares filepath.Dir(selfPath) against the bin DIR.
// ---------------------------------------------------------------------------

// TestShimInInstalledBin (B3): paths.AgyProBin() returns the bin directory, so
// a shim running from inside it must hit the OK branch. The old
// filepath.Dir(binDir) comparison made that branch unreachable on every
// standard install.
func TestShimInInstalledBin(t *testing.T) {
	binDir := filepath.Join("home", ".agy-pro", "bin")
	self := filepath.Join(binDir, "agy.exe")
	if !shimInInstalledBin(self, binDir) {
		t.Errorf("shim %q inside %q must match (OK branch reachable)", self, binDir)
	}
	// The one-level-off comparison (the B3 bug) must not be what we do.
	if shimInInstalledBin(self, filepath.Dir(binDir)) {
		t.Errorf("shim %q must NOT match parent dir %q", self, filepath.Dir(binDir))
	}
	// A copy outside the installed bin dir stays a WARN.
	elsewhere := filepath.Join("other", "agy.exe")
	if shimInInstalledBin(elsewhere, binDir) {
		t.Errorf("shim %q must not match %q", elsewhere, binDir)
	}
	// Case-insensitive on Windows (sameDirPath semantics, main.go:655-662).
	if runtime.GOOS == "windows" {
		if !shimInInstalledBin(self, strings.ToUpper(binDir)) {
			t.Error("Windows comparison must be case-insensitive")
		}
	}
}

// ---------------------------------------------------------------------------
// B4 — display-side defense: legacy registries can hold a credential in the
// apiKeyEnv / name slot; the view must never render it raw.
// ---------------------------------------------------------------------------

// TestView_MasksKeyShapedEnvName (B4, view.go:198/200): a key-shaped "env
// name" (e.g. an AWS access key ID persisted before the guard) renders masked.
func TestView_MasksKeyShapedEnvName(t *testing.T) {
	akia := "AKIAABCDEFGHIJKLMNOP"
	secret := "sk-XYZZYQUUX-89abcdef01234567"
	f := newFakeDeps()
	f.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "p1", BaseURL: "https://legacy1.example/v1", APIKeyEnv: akia},
		{Name: "p2", BaseURL: "https://legacy2.example/v1", APIKeyEnv: secret},
	}}
	c := NewCore(f.deps())
	view, err := c.View()
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var dumped strings.Builder
	for _, sec := range view.Sections {
		for _, it := range sec.Items {
			dumped.WriteString(it.ID + "\n" + it.Label + "\n" + it.Value + "\n" + it.Note + "\n")
		}
	}
	body := dumped.String()
	assertNoLeak(t, body, akia)
	assertNoLeak(t, body, secret)
}

// ---------------------------------------------------------------------------
// C5 — the doctor line must never echo a key-shaped env NAME raw.
// ---------------------------------------------------------------------------

// TestPrintKeyAuditLine_MasksKeyShapedEnv (C5/U8, security finding N2): a
// legacy registry can hold a credential in the apiKeyEnv slot (an AWS key ID
// here). Every doctor branch must render that name through
// providers.MaskOperand (first 5 + ****), never raw, while a conventional
// env name stays readable.
func TestPrintKeyAuditLine_MasksKeyShapedEnv(t *testing.T) {
	akia := "AKIAIOSFODNN7EXAMPLE"
	cases := []providers.KeyAudit{
		{Provider: "p1", Env: akia, Resolved: false},
		{Provider: "p2", Env: akia, Resolved: true, MaskedHint: "wJalr****", Probe: providers.KeyProbeResult{Status: providers.ProbeValid}},
		{Provider: "p3", Env: akia, Resolved: true, MaskedHint: "wJalr****", Conflict: true},
		{Provider: "p4", Env: akia, Resolved: true, MaskedHint: "wJalr****", Probe: providers.KeyProbeResult{Status: providers.ProbeInvalid}},
	}
	for _, a := range cases {
		var buf bytes.Buffer
		printKeyAuditLine(&buf, a)
		line := buf.String()
		assertNoLeak(t, line, akia)
		if !strings.Contains(line, providers.MaskOperand(akia)) {
			t.Errorf("doctor line lacks the masked env name %q: %q", providers.MaskOperand(akia), line)
		}
	}
	// Conventional env names stay readable (no UX regression).
	var buf bytes.Buffer
	printKeyAuditLine(&buf, providers.KeyAudit{Provider: "p5", Env: "GEMINI_API_KEY", Resolved: true, MaskedHint: "AIza****", Probe: providers.KeyProbeResult{Status: providers.ProbeValid}})
	if !strings.Contains(buf.String(), "GEMINI_API_KEY") {
		t.Errorf("conventional env name must stay readable: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// B4/B5 — ParseProviderSpecJSON guards (add-json path).
// ---------------------------------------------------------------------------

func TestParseProviderSpecJSON_AKIAAndNameCharset(t *testing.T) {
	akia := "AKIAABCDEFGHIJKLMNOP"
	_, err := ParseProviderSpecJSON([]byte(`{"name":"x","baseURL":"https://x.example/v1","apiKeyEnv":"` + akia + `","models":[]}`))
	if err == nil {
		t.Fatal("AKIA-style apiKeyEnv accepted by add-json parser")
	}
	if strings.Contains(err.Error(), akia) {
		t.Errorf("rejection echoes the credential: %v", err)
	}

	// B5: shell-metachar provider names are rejected at the data layer.
	for _, bad := range []string{"x; evil", "$(whoami)", "a/b", "-x"} {
		_, err := ParseProviderSpecJSON([]byte(`{"name":"` + bad + `","baseURL":"https://x.example/v1","models":[]}`))
		if err == nil {
			t.Errorf("shell-unsafe provider name %q accepted", bad)
		}
	}
	// Clean name + catalog env name still works end to end.
	spec, err := ParseProviderSpecJSON([]byte(`{"name":"acme","baseURL":"https://acme.example/v1","apiKeyEnv":"OPENAI_API_KEY","models":[{"id":"m1"}]}`))
	if err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if spec.Name != "acme" || spec.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("spec = %+v", spec)
	}
}

// ---------------------------------------------------------------------------
// B7 — non-Windows interactive TTYs get the line-mode UI, not a one-frame exit.
// ---------------------------------------------------------------------------

// TestUseRawConsoleKeys_Degrade (B7): raw console keys require BOTH a TTY and
// a build that implements them. A non-Windows TTY (rawConsoleOK=false) must
// degrade to line-mode — the old `isTTY`-only check rendered one frame and
// exited because readConsoleKey answers nothing off Windows.
func TestUseRawConsoleKeys_Degrade(t *testing.T) {
	if wantRawConsole(true, false) {
		t.Error("TTY without a raw console reader must degrade to line-mode (B7)")
	}
	if wantRawConsole(false, true) {
		t.Error("non-TTY must degrade to line-mode")
	}
	if !wantRawConsole(true, true) {
		t.Error("TTY with a raw console reader must take the raw path")
	}
	if rawConsoleKeysSupported != (runtime.GOOS == "windows") {
		t.Errorf("rawConsoleKeysSupported = %v for GOOS=%s", rawConsoleKeysSupported, runtime.GOOS)
	}
	if useRawConsoleKeys(true) != rawConsoleKeysSupported {
		t.Error("useRawConsoleKeys must follow the build capability")
	}
}

// TestRunTUI_LineModeDrivesMultipleFrames (B7): the degraded line-mode path is
// a live multi-frame UI (not the one-frame-and-exit stub) — down/quit drives
// two renders through the same Core verbs.
func TestRunTUI_LineModeDrivesMultipleFrames(t *testing.T) {
	f := newFakeDeps()
	f.state.ManagedItems = mode.DefaultManagedItems
	c := NewCore(f.deps())
	in := strings.NewReader("down\nquit\n")
	out := &bytes.Buffer{}
	if code := RunTUI(c, in, out, ""); code != 0 {
		t.Fatalf("RunTUI exit = %d", code)
	}
	if n := strings.Count(out.String(), "agy-pro Settings"); n < 2 {
		t.Errorf("line-mode render count = %d; want >= 2 frames:\n%s", n, out.String())
	}
}

// ---------------------------------------------------------------------------
// B8 — [SUCCESS] is stamped only after the danger y/n gate resolves.
// ---------------------------------------------------------------------------

// TestTUIRunPrompt_DangerGateNoEarlySuccess (B8): a prompt whose callback arms
// the danger confirm gate emits "confirm to proceed" — [SUCCESS] appears only
// when the confirm runner actually runs the op.
func TestTUIRunPrompt_DangerGateNoEarlySuccess(t *testing.T) {
	tui, f, _ := newTestTUI(t)
	tui.section = 1
	var idx int
	found := false
	for i, it := range tui.view.Sections[1].Items {
		if it.ID == "providers.delete" {
			idx = i
			found = true
		}
	}
	if !found {
		t.Fatal("providers.delete missing from P2")
	}
	tui.lines = &ScriptedLines{Lines: []string{"google"}}
	tui.row = idx
	tui.Step(KeyEnter)

	if tui.pending == nil {
		t.Fatal("delete must arm the danger y/n gate")
	}
	if strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("SUCCESS stamped before the gate resolved: %q", tui.message)
	}
	if !strings.Contains(tui.message, "confirm to proceed") {
		t.Errorf("message must ask for confirmation, got %q", tui.message)
	}
	if len(f.reg.Providers) != 1 {
		t.Fatalf("delete ran before confirmation: %+v", f.reg.Providers)
	}

	// The confirm runner stamps SUCCESS — and only then.
	tui.Step(KeyYes)
	if !strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("confirm runner must stamp SUCCESS, got %q", tui.message)
	}
	if len(f.reg.Providers) != 0 {
		t.Errorf("confirmed delete did not run: %+v", f.reg.Providers)
	}
}

// TestTUIRunPrompt_NonDangerStillSucceedsImmediately (regression guard for B8):
// rename has no gate and keeps its immediate SUCCESS stamp.
func TestTUIRunPrompt_NonDangerStillSucceedsImmediately(t *testing.T) {
	tui, _, _ := newTestTUI(t)
	tui.section = 1
	var idx int
	for i, it := range tui.view.Sections[1].Items {
		if it.ID == "providers.rename" {
			idx = i
		}
	}
	tui.lines = &ScriptedLines{Lines: []string{"google", "gemini"}}
	tui.row = idx
	tui.Step(KeyEnter)
	if !strings.Contains(tui.message, "[SUCCESS]") {
		t.Errorf("rename message = %q", tui.message)
	}
}
