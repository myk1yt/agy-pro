package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
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
	akia := testfixtures.AKIAAlphabet
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

// TestPrintKeyAuditLine_MasksKeyShapedEnv (C5/U8, security finding N2, D4): a
// legacy registry can hold a credential in the apiKeyEnv slot (an AWS key ID
// here), and the same legacy state can park one in the provider NAME slot.
// Every doctor branch must render both names through providers.MaskOperand
// (first 5 + ****), never raw, while conventional names stay readable.
func TestPrintKeyAuditLine_MasksKeyShapedEnv(t *testing.T) {
	akia := testfixtures.AKIAExample
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
	// D4: the provider NAME gets the same treatment (same legacy-registry
	// class, opposite slot).
	for _, a := range cases {
		a.Provider, a.Env = akia, "GEMINI_API_KEY"
		var buf bytes.Buffer
		printKeyAuditLine(&buf, a)
		line := buf.String()
		assertNoLeak(t, line, akia)
		if !strings.Contains(line, providers.MaskOperand(akia)) {
			t.Errorf("doctor line lacks the masked provider name %q: %q", providers.MaskOperand(akia), line)
		}
		if !strings.Contains(line, "GEMINI_API_KEY") {
			t.Errorf("conventional env name must stay readable: %q", line)
		}
	}
	// Conventional names stay readable (no UX regression).
	var buf bytes.Buffer
	printKeyAuditLine(&buf, providers.KeyAudit{Provider: "p5", Env: "GEMINI_API_KEY", Resolved: true, MaskedHint: "AIza****", Probe: providers.KeyProbeResult{Status: providers.ProbeValid}})
	if !strings.Contains(buf.String(), "GEMINI_API_KEY") {
		t.Errorf("conventional env name must stay readable: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "p5") {
		t.Errorf("conventional provider name must stay readable: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// B4/B5 — ParseProviderSpecJSON guards (add-json path).
// ---------------------------------------------------------------------------

func TestParseProviderSpecJSON_AKIAAndNameCharset(t *testing.T) {
	akia := testfixtures.AKIAAlphabet
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

// ---------------------------------------------------------------------------
// FIX-7 — the add-json name gate (the CLI twin door of the provider-name arm).
// ---------------------------------------------------------------------------

// addJSONUnderscoreNameProbes is the FIX-7 target class at the CLI door: key
// shapes whose head is absent from the shared prefix list and whose "_" breaks
// the opaque-run test, so providers.SecretShaped alone is false for all five
// while the registry-slot display authority masks them. Synthetic values only.
var addJSONUnderscoreNameProbes = []string{
	testfixtures.SKLive,
	testfixtures.TokSample,
	testfixtures.PKLive,
	testfixtures.KeyHex,
	testfixtures.AKIAUnderscore,
}

// TestParseProviderSpecJSON_RefusesCredentialName pins the FIX-7 CLI twin gate:
// a credential-shaped NAME fails the parser with the NAMED error before Core
// ever sees the spec, so nothing is written and the temp-home providers.json
// stays byte-identical. The sentence is value-free (no rune of the refused name
// rides in it), and the benign side keeps the accept verdict and the exact
// on-disk bytes the pre-fix door wrote (the added arm is the identity on every
// benign name, which the same test asserts against the charset-legal corpus).
func TestParseProviderSpecJSON_RefusesCredentialName(t *testing.T) {
	for _, probe := range addJSONUnderscoreNameProbes {
		t.Run(probe, func(t *testing.T) {
			// Restate the hole from the pre-fix arm so this pin proves the gap
			// instead of assuming it: SecretShaped is false, the charset admits
			// the value, and only the shared name authority sees it.
			if providers.SecretShaped(probe) {
				t.Fatalf("%q is caught by the pre-fix arm; it no longer exercises the gap", probe)
			}
			if err := providers.ValidateProviderName(probe); err != nil {
				t.Fatalf("%q fails the name charset, so the refusal would be the charset's: %v", probe, err)
			}
			if !providers.NameCarriesCredential(probe) {
				t.Fatalf("%q is identity under the shared name authority; the gate cannot catch it", probe)
			}

			_, path := fileBackedCore(t, editFixture())
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read seeded providers.json: %v", err)
			}

			spec := []byte(`{"name":"` + probe + `","baseURL":"https://gw.example/v1","apiKeyEnv":"GW_KEY","models":[{"id":"m1"}]}`)
			got, err := ParseProviderSpecJSON(spec)
			if !errors.Is(err, ErrProviderSpecNameCarriesCredential) {
				t.Fatalf("ParseProviderSpecJSON(%q name) err = %v, want ErrProviderSpecNameCarriesCredential", probe, err)
			}
			if got.Name != "" || len(got.Models) != 0 {
				t.Fatalf("a refused spec still carries fields: %+v", got)
			}
			assertNoLeak(t, err.Error(), probe)
			if strings.Contains(err.Error(), probe) {
				t.Fatalf("the refusal echoes the name: %v", err)
			}

			// The gate sits ahead of the write path: the verb's next step
			// (Core.ProviderAdd) is never reached, so the file cannot move.
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json after the refusal: %v", err)
			}
			if string(after) != string(before) {
				t.Fatalf("providers.json changed on a refused name:\nbefore: %s\nafter:  %s", before, after)
			}
			if reg := reloadFromDisk(t, path); len(reg.Providers) != len(editFixture().Providers) {
				t.Fatalf("registry on disk = %d providers, want %d", len(reg.Providers), len(editFixture().Providers))
			}
		})
	}
}

// TestParseProviderSpecJSON_BenignNameBytesUnchanged pins the no-over-refusal
// side of the FIX-7 gate: every charset-legal benign name still parses, and the
// full add flow writes the exact bytes the pre-fix door wrote. The golden bytes
// are composed with the same serialisation the save seam uses
// (json.MarshalIndent + a trailing newline), so a message change, a
// normalization, an extra field, or a new arm that touched a benign name would
// move them.
func TestParseProviderSpecJSON_BenignNameBytesUnchanged(t *testing.T) {
	benign := []string{
		"acme", "beta", "openai", "gemini", "deepseek", "anthropic", "acme-fast",
		"acme_internal", "key_backup", "aws_prod", "gateway-eu", "my-gateway.example",
		"bedrock-us-east1",
	}
	const specJSON = `{"name":"%s","baseURL":"https://gw.example/v1","apiKeyEnv":"GW_KEY","models":[{"id":"m1"}]}`
	for _, name := range benign {
		t.Run(name, func(t *testing.T) {
			if providers.NameCarriesCredential(name) {
				t.Fatalf("%q is refused by the shared name authority; the benign corpus is wrong", name)
			}
			spec, err := ParseProviderSpecJSON([]byte(fmt.Sprintf(specJSON, name)))
			if err != nil {
				t.Fatalf("benign spec %q refused: %v", name, err)
			}
			if spec.Name != name || spec.APIKeyEnv != "GW_KEY" || len(spec.Models) != 1 || spec.Models[0].ID != "m1" {
				t.Fatalf("parsed spec moved: %+v", spec)
			}

			c, path := fileBackedCore(t, &providers.Registry{})
			if err := c.ProviderAdd(spec); err != nil {
				t.Fatalf("ProviderAdd: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read providers.json: %v", err)
			}
			wantBody, err := json.MarshalIndent(&providers.Registry{Providers: []providers.Provider{{
				Name: name, BaseURL: "https://gw.example/v1", APIKeyEnv: "GW_KEY",
				Models: []providers.Model{{ID: "m1"}},
			}}}, "", "  ")
			if err != nil {
				t.Fatalf("marshal the golden registry: %v", err)
			}
			want := append(wantBody, '\n')
			if !bytes.Equal(got, want) {
				t.Fatalf("add-json wrote different bytes for a benign name:\ngot:  %s\nwant: %s", got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fix7 - provider NAME operands in the probe/identity sentences route through
// the registry-slot authority (providers.MaskSlot).
// ---------------------------------------------------------------------------

// probePlantName is the underscore-head key class in a provider NAME slot.
// The value is synthetic; no live credential is involved.
const (
	probePlantName   = testfixtures.SKLive
	probePlantMasked = "sk_li****"
	probePlantTail   = "0123456789abcdef"
)

// probeNameCorpus is the identity corpus: the fix must leave every one of
// these byte-identical (MaskSlot's first stage is MaskOperand), and every
// name the existing pins in this package use is represented.
var probeNameCorpus = []string{
	"openai", "gemini", "deepseek", "nw", "charm", "acme-corp",
	"acme", "alpha", "beta", "ghost", "x", "nope", "read,search",
	"ACME", "BeTa",
}

// probeCase is one composition branch of Core.ProviderTest, together with the
// message the pre-fix code produced for it (MaskOperand on the name operand).
type probeCase struct {
	label string
	probe *providers.KeyProbeResult
	seed  []string
	query string
	want  func(name string) string
}

// probeCases builds the four branches for one requested name.
func probeCases(name string, skipped, invalid providers.KeyProbeResult) []probeCase {
	return []probeCase{
		{
			label: "not found", seed: nil, query: name,
			want: func(n string) string { return fmt.Sprintf("provider %q not found", providers.MaskOperand(n)) },
		},
		{
			label: "no audit", seed: []string{name}, query: name,
			want: func(n string) string {
				return fmt.Sprintf("provider %q has no API key env to probe", providers.MaskOperand(n))
			},
		},
		{
			label: "probe skipped", probe: &skipped, seed: []string{name}, query: name,
			want: func(n string) string { return fmt.Sprintf("provider %q: %s", providers.MaskOperand(n), skipped.Detail) },
		},
		{
			label: "probe invalid", probe: &invalid, seed: []string{name}, query: name,
			want: func(n string) string {
				return fmt.Sprintf("provider %q: key probe status %s (%s)", providers.MaskOperand(n), invalid.Status, invalid.Detail)
			},
		},
	}
}

// newProbeCore seeds a registry with names and, when probe is non-nil, an
// audit seam that answers with it.
func newProbeCore(names []string, probe *providers.KeyProbeResult) *Core {
	f := newFakeDeps()
	for _, n := range names {
		f.reg.Providers = append(f.reg.Providers, providers.Provider{
			Name: n, BaseURL: "https://probe.example/v1", APIKeyEnv: "PROBE_KEY",
		})
	}
	if probe != nil {
		f.audits = []providers.KeyAudit{{
			Provider: names[0], Env: "PROBE_KEY", Resolved: true, Probe: *probe,
		}}
	}
	return NewCore(f.deps())
}

// TestCoreProviderTest_NameOperandSlotMasked pins the probe-failure sentences:
// the NAME operand renders through MaskSlot, so the underscore-head key class
// masks in all four branches while the sentence text, the probe detail, and
// the sentinel messages stay unchanged.
func TestCoreProviderTest_NameOperandSlotMasked(t *testing.T) {
	skipped := providers.KeyProbeResult{Status: providers.ProbeSkipped, Detail: "dest plant.example: unreachable"}
	invalid := providers.KeyProbeResult{Status: providers.ProbeInvalid, Detail: "dest plant.example: rejected"}

	t.Run("credential-shaped name masks", func(t *testing.T) {
		for _, pc := range probeCases(probePlantName, skipped, invalid) {
			t.Run(pc.label, func(t *testing.T) {
				err := newProbeCore(pc.seed, pc.probe).ProviderTest(pc.query)
				if err == nil {
					t.Fatalf("ProviderTest(%q) succeeded; want the failure branch", pc.query)
				}
				wantRaw := pc.want(probePlantName)
				want := strings.ReplaceAll(wantRaw, probePlantName, probePlantMasked)
				if want == wantRaw {
					t.Fatalf("the pin did not move off the pre-fix text: %q", want)
				}
				got := err.Error()
				if got != want {
					t.Errorf("full message drifted:\ngot:  %q\nwant: %q", got, want)
				}
				if strings.Contains(got, probePlantName) || strings.Contains(got, probePlantTail) {
					t.Errorf("message leaks the planted name raw: %q", got)
				}
				if !strings.Contains(got, probePlantMasked) {
					t.Errorf("message is missing the masked name %q: %q", probePlantMasked, got)
				}
			})
		}
	})

	t.Run("benign corpus byte-identical", func(t *testing.T) {
		for _, name := range probeNameCorpus {
			for _, pc := range probeCases(name, skipped, invalid) {
				t.Run(name+"/"+pc.label, func(t *testing.T) {
					err := newProbeCore(pc.seed, pc.probe).ProviderTest(pc.query)
					if err == nil {
						t.Fatalf("ProviderTest(%q) succeeded; want the failure branch", pc.query)
					}
					if got, want := err.Error(), pc.want(name); got != want {
						t.Errorf("benign name stopped rendering byte-identically:\ngot:  %q\nwant: %q", got, want)
					}
					if got := err.Error(); strings.Contains(got, "****") {
						t.Errorf("benign name %q was masked: %q", name, got)
					}
					if providers.MaskSlot(name) != name {
						t.Errorf("MaskSlot(%q) = %q; the identity corpus must stay readable", name, providers.MaskSlot(name))
					}
				})
			}
		}
	})
}

// TestResolveProviderEditTarget_NameOperandSlotMasked pins the two identity
// sentences of the shared edit resolver: the refused NAME operand renders
// through MaskSlot while the sentence text and the sentinel class stay.
func TestResolveProviderEditTarget_NameOperandSlotMasked(t *testing.T) {
	t.Run("not found masks the planted name", func(t *testing.T) {
		reg := &providers.Registry{Providers: []providers.Provider{{Name: "alpha", BaseURL: "https://a.example/v1"}}}
		_, err := ResolveProviderEditTarget(reg, probePlantName)
		if !errors.Is(err, ErrProviderEditNotFound) {
			t.Fatalf("error = %v; want ErrProviderEditNotFound", err)
		}
		want := `provider edit: provider not found: ` + probePlantMasked
		got := err.Error()
		if got != want {
			t.Errorf("full message drifted:\ngot:  %q\nwant: %q", got, want)
		}
		if strings.Contains(got, probePlantName) || strings.Contains(got, probePlantTail) {
			t.Errorf("message leaks the planted name raw: %q", got)
		}
	})

	t.Run("ambiguous masks the planted query", func(t *testing.T) {
		// Two records that fold together; the query matches neither exactly,
		// so the resolver refuses with the ambiguous class and echoes the
		// query operand.
		reg := &providers.Registry{Providers: []providers.Provider{
			{Name: testfixtures.SKLiveMixed, BaseURL: "https://a.example/v1"},
			{Name: testfixtures.SKLiveUpper, BaseURL: "https://b.example/v1"},
		}}
		_, err := ResolveProviderEditTarget(reg, probePlantName)
		if !errors.Is(err, ErrProviderEditAmbiguous) {
			t.Fatalf("error = %v; want ErrProviderEditAmbiguous", err)
		}
		want := `provider edit: provider name is ambiguous: ` + probePlantMasked +
			` matches more than one provider under case folding (use the exact spelling)`
		got := err.Error()
		if got != want {
			t.Errorf("full message drifted:\ngot:  %q\nwant: %q", got, want)
		}
		if strings.Contains(got, probePlantName) || strings.Contains(got, probePlantTail) {
			t.Errorf("message leaks the planted name raw: %q", got)
		}
	})

	t.Run("benign corpus byte-identical", func(t *testing.T) {
		reg := &providers.Registry{Providers: []providers.Provider{{Name: "alpha", BaseURL: "https://a.example/v1"}}}
		for _, name := range probeNameCorpus {
			if name == "alpha" || name == "ACME" {
				continue // alpha is registered; ACME is the fold pair below
			}
			_, err := ResolveProviderEditTarget(reg, name)
			if !errors.Is(err, ErrProviderEditNotFound) {
				t.Fatalf("ResolveProviderEditTarget(%q) error = %v; want ErrProviderEditNotFound", name, err)
			}
			want := "provider edit: provider not found: " + name
			if got := err.Error(); got != want {
				t.Errorf("benign name stopped rendering byte-identically:\ngot:  %q\nwant: %q", got, want)
			}
		}

		dup := &providers.Registry{Providers: []providers.Provider{
			{Name: "acme", BaseURL: "https://a.example/v1"},
			{Name: "ACME", BaseURL: "https://b.example/v1"},
		}}
		_, err := ResolveProviderEditTarget(dup, "Acme")
		if !errors.Is(err, ErrProviderEditAmbiguous) {
			t.Fatalf("error = %v; want ErrProviderEditAmbiguous", err)
		}
		want := "provider edit: provider name is ambiguous: Acme matches more than one provider under case folding (use the exact spelling)"
		if got := err.Error(); got != want {
			t.Errorf("benign ambiguous message drifted:\ngot:  %q\nwant: %q", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// fix8 - the doctor line's provider NAME operand routes through the registry-
// slot authority (providers.MaskSlot), pinned at its distinguishing class.
// ---------------------------------------------------------------------------

// auditNamePlant is the underscore-head key class in the doctor line's provider
// NAME slot. Synthetic value only.
const (
	auditNamePlant  = testfixtures.SKLive
	auditNameMasked = "sk_li****"
	auditNameTail   = "0123456789abcdef"
)

// TestPrintKeyAuditLine_UnderscoreNameSlotMasked pins the seams.go name-operand
// change at the class that distinguishes it: MaskOperand is the identity on the
// underscore-head family (no prefix head, and the "_" breaks the opaque-run
// test), so the pre-existing AKIA pin passes with either masker. This pin fails
// if the name operand falls back to MaskOperand, in every doctor branch.
func TestPrintKeyAuditLine_UnderscoreNameSlotMasked(t *testing.T) {
	// Premise: the pre-fix masker cannot see this class, the slot authority can.
	if got := providers.MaskOperand(auditNamePlant); got != auditNamePlant {
		t.Fatalf("premise broken: MaskOperand now masks the class (%q)", got)
	}
	if providers.SecretShaped(auditNamePlant) {
		t.Fatalf("premise broken: the shape test catches the plant")
	}
	if got := providers.MaskSlot(auditNamePlant); got != auditNameMasked {
		t.Fatalf("premise broken: MaskSlot(plant) = %q; want %q", got, auditNameMasked)
	}

	cases := []struct {
		label string
		audit providers.KeyAudit
		want  string
	}{
		{
			label: "unresolved",
			audit: providers.KeyAudit{Provider: auditNamePlant, Env: "GEMINI_API_KEY"},
			want:  "[WARN] " + auditNameMasked + " (GEMINI_API_KEY): (not set)",
		},
		{
			label: "conflict",
			audit: providers.KeyAudit{
				Provider: auditNamePlant, Env: "GEMINI_API_KEY", Resolved: true,
				MaskedHint: "AIza****", Conflict: true,
			},
			want: "[WARN] " + auditNameMasked + " (GEMINI_API_KEY): AIza**** (name variants conflict)",
		},
		{
			label: "probe ok",
			audit: providers.KeyAudit{
				Provider: auditNamePlant, Env: "GEMINI_API_KEY", Resolved: true,
				MaskedHint: "AIza****", Probe: providers.KeyProbeResult{Status: providers.ProbeValid},
			},
			want: "[OK]   " + auditNameMasked + " (GEMINI_API_KEY): AIza**** (probe ok)",
		},
		{
			label: "probe invalid",
			audit: providers.KeyAudit{
				Provider: auditNamePlant, Env: "GEMINI_API_KEY", Resolved: true,
				MaskedHint: "AIza****", Probe: providers.KeyProbeResult{Status: providers.ProbeInvalid},
			},
			want: "[WARN] " + auditNameMasked + " (GEMINI_API_KEY): AIza**** (probe invalid)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var buf bytes.Buffer
			printKeyAuditLine(&buf, tc.audit)
			line := buf.String()
			if got := strings.TrimSuffix(line, "\n"); got != tc.want {
				t.Errorf("doctor line drifted:\ngot:  %q\nwant: %q", got, tc.want)
			}
			if strings.Contains(line, auditNamePlant) || strings.Contains(line, auditNameTail) {
				t.Errorf("doctor line leaks the planted provider name: %q", line)
			}
			assertNoLeak(t, line, auditNamePlant)
			if !strings.Contains(line, "GEMINI_API_KEY") {
				t.Errorf("the env NAME slot must stay readable: %q", line)
			}
		})
	}

	// The benign corpus: the sentence text and a conventional name stay
	// byte-identical (the slot authority is the identity on every one of these).
	for _, name := range []string{"p5", "acme", "gemini", "acme-corp", "acme_internal", "key_backup"} {
		if providers.MaskSlot(name) != name {
			t.Fatalf("corpus name %q is not identity under the slot authority (%q)", name, providers.MaskSlot(name))
		}
		var buf bytes.Buffer
		printKeyAuditLine(&buf, providers.KeyAudit{Provider: name, Env: "GEMINI_API_KEY"})
		want := "[WARN] " + name + " (GEMINI_API_KEY): (not set)"
		if got := strings.TrimSuffix(buf.String(), "\n"); got != want {
			t.Errorf("benign doctor line drifted:\ngot:  %q\nwant: %q", got, want)
		}
	}
}
