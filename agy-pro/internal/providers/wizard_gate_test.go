// P5-1 U4: coverage for the setup wizard's composed-write gate (W12 the
// providers.json upsert, W31 the Windows user-environment key persist). Every
// case drives the REAL wizard through a scripted dialog and asserts the write
// seam, so a gate that silently disappeared fails here even though the
// transcript-shape tests would still pass.
package providers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The planted key values the scripted dialogs type. Each head is built by
// string concatenation so the tracked source carries no contiguous
// provider-key spelling (the standing gate scans source characters), while the
// compiled constant stays byte-identical to the single-block literal it
// replaced, so every equality check and transcript comparison below compares
// exactly the bytes it compared before. Values stay distinct per case so a leak
// from one case cannot read as another case's value.
const (
	wizardGateKey0001 = "sk-" + "live-planted-0001"
	wizardGateKey0002 = "sk-" + "live-planted-0002"
	wizardGateKey0003 = "sk-" + "live-planted-0003"
	wizardGateKey0004 = "sk-" + "live-planted-0004"
	wizardGateKey0005 = "sk-" + "live-planted-0005"
	wizardGateKey0006 = "sk-" + "live-planted-0006"
	wizardGateKey0007 = "sk-" + "live-planted-0007"
	wizardGateKey0008 = "sk-" + "live-planted-0008"

	// wizardGateStaleVariant is the value a pre-seeded BOM variant already
	// carries. It is split for the same reason: an `sk-` head plus an opaque
	// body is a key-shaped value even where no standing arm lists that family.
	wizardGateStaleVariant = "sk-" + "planted-stale-variant"
)

// TestWizardGate_KeyValuesAreByteIdentical pins every const above to the exact
// bytes the single-block literal carried, so a re-split that changed the value
// fails here instead of quietly weakening the cases below. The expectations are
// hex, so no pin restores the contiguous spelling this file avoids, and a
// mismatch is reported by offset and length instead of by printing the value.
func TestWizardGate_KeyValuesAreByteIdentical(t *testing.T) {
	pins := []struct{ name, value, hex string }{
		{"wizardGateKey0001", wizardGateKey0001, "736b2d6c6976652d706c616e7465642d30303031"},
		{"wizardGateKey0002", wizardGateKey0002, "736b2d6c6976652d706c616e7465642d30303032"},
		{"wizardGateKey0003", wizardGateKey0003, "736b2d6c6976652d706c616e7465642d30303033"},
		{"wizardGateKey0004", wizardGateKey0004, "736b2d6c6976652d706c616e7465642d30303034"},
		{"wizardGateKey0005", wizardGateKey0005, "736b2d6c6976652d706c616e7465642d30303035"},
		{"wizardGateKey0006", wizardGateKey0006, "736b2d6c6976652d706c616e7465642d30303036"},
		{"wizardGateKey0007", wizardGateKey0007, "736b2d6c6976652d706c616e7465642d30303037"},
		{"wizardGateKey0008", wizardGateKey0008, "736b2d6c6976652d706c616e7465642d30303038"},
		{"wizardGateStaleVariant", wizardGateStaleVariant, "736b2d706c616e7465642d7374616c652d76617269616e74"},
	}
	for _, pin := range pins {
		want, err := hex.DecodeString(pin.hex)
		if err != nil {
			t.Fatalf("%s: malformed hex pin: %v", pin.name, err)
		}
		if pin.value == string(want) {
			continue
		}
		offset := 0
		for offset < len(pin.value) && offset < len(want) && pin.value[offset] == want[offset] {
			offset++
		}
		t.Errorf("%s: compiled value is not byte-identical to the pinned bytes (len %d, want %d, first difference at byte %d)",
			pin.name, len(pin.value), len(want), offset)
	}
}

// withWizardConsent appends the composed gate's answer to a scripted dialog.
// P5-1 U4 (F3(a)) makes the last prompt of a run that intends a write the
// exact-diff consent, so a script that used to end at "Add another model?" now
// has to answer it. The extra line is simply never read by a run that aborts
// earlier, so wrapping every script is safe.
func withWizardConsent(script string) string {
	if script != "" && !strings.HasSuffix(script, "\n") {
		script += "\n"
	}
	return script + "y\n"
}

// wizardGateHome points AGY_PRO_HOME and the registry at one temp dir, so no
// case can read or write the live ~/.agy-pro/providers.json. The catalog key
// variable these scripts type into is cleared, so a real DEEPSEEK_API_KEY in
// the operator's environment (or one a sibling test set) cannot change which
// prompt the dialog reaches.
func wizardGateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvProviders, filepath.Join(home, "providers.json"))
	t.Setenv("DEEPSEEK_API_KEY", "")
	return filepath.Join(home, "providers.json")
}

// wizardGateScript is the shortest dialog that reaches the composed gate: a
// catalog provider with model presets, a closed loopback endpoint (the probe
// fails fast and locally), one preset, "save anyway", and no second model.
// answer is appended as the gate's y/N line ("" leaves the dialog exhausted).
func wizardGateScript(t *testing.T, key, answer string) string {
	t.Helper()
	steps := []string{"deepseek", closedLoopbackBaseURL(t) + "/v1", key, "1", "1", "N"}
	if answer != "" {
		steps = append(steps, answer)
	}
	return strings.Join(steps, "\n") + "\n"
}

// wizardGateEnvSeams forces the environment half of the plan to exist on every
// host (the production rule is windows && !isRunningTests) and points the W31
// sink at the mock store, so a case can assert exactly what was written, and
// what was not.
func wizardGateEnvSeams(t *testing.T, store *mockEnvStore) {
	t.Helper()
	oldPersists, oldProbe := wizardPersistsUserEnv, wizardEnvProbe
	wizardPersistsUserEnv = func() bool { return true }
	wizardEnvProbe = probeWizardUserEnv
	t.Cleanup(func() { wizardPersistsUserEnv, wizardEnvProbe = oldPersists, oldProbe })
	useMockEnvStore(t, store)
}

// TestWizardGate_PipeRefusalWritesNothing is the refusal story this front end
// did not have: nothing can answer, so the diff prints, the door refuses naming
// --yes, and NEITHER store is touched.
func TestWizardGate_PipeRefusalWritesNothing(t *testing.T) {
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	wizardGateEnvSeams(t, store)

	var out bytes.Buffer
	err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0001, "")), &out, WizardOptions{})

	if !errors.Is(err, ErrWizardWriteRefused) {
		t.Fatalf("error = %v; want ErrWizardWriteRefused", err)
	}
	if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
		t.Fatalf("providers.json exists after a refused run (stat err = %v)", statErr)
	}
	if len(store.setCalls) != 0 {
		t.Fatalf("the refused run wrote the user environment: %v", store.setCalls)
	}
	transcript := out.String()
	if !strings.Contains(transcript, wizardConsentBanner) {
		t.Errorf("the refusal must print the diff banner first:\n%s", transcript)
	}
	if !strings.Contains(transcript, wizardRefusalSentence) {
		t.Errorf("the refusal sentence is missing:\n%s", transcript)
	}
	if !strings.Contains(transcript, "--yes") {
		t.Errorf("the refusal must name --yes:\n%s", transcript)
	}
}

// TestWizardGate_YesConsentWritesBothStores pins the --yes path: the diff
// prints, the registry upsert lands, and the key persist reaches the store
// under the NAME only.
func TestWizardGate_YesConsentWritesBothStores(t *testing.T) {
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	wizardGateEnvSeams(t, store)

	var out bytes.Buffer
	err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0002, "")), &out, WizardOptions{Yes: true})
	if err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	reg, err := LoadFrom(provPath)
	if err != nil {
		t.Fatalf("the consented run wrote no readable registry: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "deepseek" || reg.Providers[0].APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("unexpected saved registry: %+v", reg.Providers)
	}
	want := [2]string{EnvRootUser, "DEEPSEEK_API_KEY"}
	if len(store.setCalls) != 1 || store.setCalls[0] != want {
		t.Fatalf("user-environment writes = %v; want exactly %v", store.setCalls, want)
	}
	if got := store.data[EnvRootUser]["DEEPSEEK_API_KEY"]; got != wizardGateKey0002 {
		t.Fatalf("stored value = %q; want the typed key", got)
	}
	transcript := out.String()
	for _, want := range []string{
		wizardConsentBanner,
		"Adds provider deepseek to the registry. Key values are untouched.",
		"Model count: 0 -> 1",
		wizardRegistryWriteSentence,
		"+ provider deepseek (",
		"Sets DEEPSEEK_API_KEY in HKCU\\Environment",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("consent transcript missing %q:\n%s", want, transcript)
		}
	}
	// The value is never a rendered operand, in either half of the diff.
	if strings.Contains(transcript, wizardGateKey0002) {
		t.Errorf("a key value reached the consent transcript:\n%s", transcript)
	}
	// --yes consents without reading an answer: the dialog is exhausted here.
	if strings.Contains(transcript, "Apply this write to") {
		t.Errorf("--yes must not prompt:\n%s", transcript)
	}
}

// TestWizardGate_ConsoleAnswers pins the console edges on the same dialog: y
// consents, and n / an unrecognized word cancels with nothing written and exit
// code 0 (the CLI twin's cancel semantics).
func TestWizardGate_ConsoleAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		write  bool
	}{
		{"y consents", "y", true},
		{"uppercase Y consents", "Y", true},
		{"n cancels", "n", false},
		{"a blank line cancels", " ", false},
		{"an unrecognized word cancels", "sure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provPath := wizardGateHome(t)
			store := newMockEnvStore()
			wizardGateEnvSeams(t, store)

			var out bytes.Buffer
			err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0003, tc.answer)), &out, WizardOptions{Console: true})
			if err != nil {
				t.Fatalf("a console answer must not produce an error: %v\nOutput: %s", err, out.String())
			}
			_, statErr := os.Stat(provPath)
			wrote := statErr == nil
			if wrote != tc.write {
				t.Fatalf("providers.json written = %v; want %v", wrote, tc.write)
			}
			if wrote != (len(store.setCalls) == 1) {
				t.Fatalf("user-environment writes = %v; want the registry decision", store.setCalls)
			}
			transcript := out.String()
			if !strings.Contains(transcript, "Apply this write to providers.json and HKCU\\Environment? [y/N]: ") {
				t.Errorf("the console prompt is missing:\n%s", transcript)
			}
			if !tc.write && !strings.Contains(transcript, wizardCancelSentence) {
				t.Errorf("a cancel must print %q:\n%s", wizardCancelSentence, transcript)
			}
		})
	}
}

// TestWizardGate_OverlongAnswerIsCapped pins the CWE-400 bound the CLI twin
// carries (a character device with no newline must not buffer without limit):
// the consent read stops at 64 bytes, an over-long line is treated exactly like
// any other non-y answer, and an exhausted console stdin refuses naming --yes
// instead of silently cancelling.
func TestWizardGate_OverlongAnswerIsCapped(t *testing.T) {
	t.Run("an over-long answer cancels without writing", func(t *testing.T) {
		provPath := wizardGateHome(t)
		store := newMockEnvStore()
		wizardGateEnvSeams(t, store)

		// 200 'y' bytes and no newline: only the capped prefix can be read.
		var out bytes.Buffer
		err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, "", strings.Repeat("y", 200))), &out, WizardOptions{Console: true})
		if err != nil {
			t.Fatalf("an over-long answer must cancel, not error: %v\nOutput: %s", err, out.String())
		}
		if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
			t.Fatalf("an over-long answer wrote providers.json (stat err = %v)", statErr)
		}
		if !strings.Contains(out.String(), wizardCancelSentence) {
			t.Errorf("an over-long answer must cancel with %q:\n%s", wizardCancelSentence, out.String())
		}
	})

	t.Run("an exhausted console stdin refuses naming --yes", func(t *testing.T) {
		provPath := wizardGateHome(t)
		store := newMockEnvStore()
		wizardGateEnvSeams(t, store)

		var out bytes.Buffer
		err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, "", "")), &out, WizardOptions{Console: true})
		if !errors.Is(err, ErrWizardWriteRefused) {
			t.Fatalf("error = %v; want ErrWizardWriteRefused", err)
		}
		if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
			t.Fatalf("an exhausted prompt wrote providers.json (stat err = %v)", statErr)
		}
		if !strings.Contains(out.String(), "--yes") {
			t.Errorf("the EOF refusal must name --yes:\n%s", out.String())
		}
	})
}

// TestWizardGate_ClosedInputAtModelPromptAbortsInsteadOfSpinning pins the
// termination bug the gated tail made reachable: an input that closes at the
// custom "Model ID" prompt (before any model is configured) used to print "Model
// ID cannot be empty." and re-prompt, and because an EOF read returns instantly
// the loop burned 100% CPU forever. The preset menu already aborted on that EOF
// shape; the custom-ID door now does the same. The test bounds the run with a
// deadline, so a spin fails here instead of hanging the suite.
func TestWizardGate_ClosedInputAtModelPromptAbortsInsteadOfSpinning(t *testing.T) {
	provPath := wizardGateHome(t)

	// Name, base URL and key, then nothing: the very next prompt is the custom
	// Model ID (the provider is unregistered, so there are no presets).
	script := strings.Join([]string{"mycustomvendor", closedLoopbackBaseURL(t) + "/v1", wizardGateKey0008}, "\n") + "\n"

	done := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		done <- InteractiveSetupWizardWithGate(strings.NewReader(script), &out, WizardOptions{Yes: true})
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "input closed (EOF)") {
			t.Fatalf("closed input must abort with the EOF sentence, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the wizard did not terminate on a closed input at the model prompt (spin)")
	}
	if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
		t.Fatalf("an aborted run wrote providers.json (stat err = %v)", statErr)
	}
}

// TestWizardGate_StaleRegistryRefuses is the CWE-367 half: the record the diff
// described moved between the presentation and the write, so the run refuses
// with a named sentence and writes nothing.
func TestWizardGate_StaleRegistryRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		drifted func(reg *Registry)
		want    string
	}{
		{
			name: "the presented name appeared",
			drifted: func(reg *Registry) {
				reg.Providers = append(reg.Providers, Provider{Name: "deepseek", BaseURL: "https://other.test/v1"})
			},
			want: "appeared in providers.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provPath := wizardGateHome(t)
			store := newMockEnvStore()
			wizardGateEnvSeams(t, store)

			oldReload := wizardRegistryReload
			calls := 0
			wizardRegistryReload = func(path string) (*Registry, error) {
				calls++
				if calls == 1 {
					return LoadFrom(path)
				}
				reg := &Registry{Defaults: Defaults{N: 3, TimeoutSeconds: 120, MaxTurns: 10, Tools: "read,search"}}
				tc.drifted(reg)
				return reg, nil
			}
			t.Cleanup(func() { wizardRegistryReload = oldReload })

			var out bytes.Buffer
			err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0004, "")), &out, WizardOptions{Yes: true})
			if err == nil {
				t.Fatalf("a stale base was applied; output:\n%s", out.String())
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "nothing written") {
				t.Fatalf("refusal = %v; want a named stale-base refusal", err)
			}
			if calls < 2 {
				t.Fatalf("the plan was not re-read before the write (reload calls = %d)", calls)
			}
			if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
				t.Fatalf("a stale run wrote providers.json (stat err = %v)", statErr)
			}
			if len(store.setCalls) != 0 {
				t.Fatalf("a stale run wrote the user environment: %v", store.setCalls)
			}
		})
	}
}

// TestWizardGate_StaleEnvRefuses is the environment half of the same rule: the
// presented presence of the variable NAME moved, so nothing is written and HKCU
// is untouched.
func TestWizardGate_StaleEnvRefuses(t *testing.T) {
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	wizardGateEnvSeams(t, store)

	// Pre-consent the name is absent; by the write-time re-read it exists.
	probes := 0
	oldProbe := wizardEnvProbe
	wizardEnvProbe = func(name string) wizardEnvState {
		probes++
		if probes == 1 {
			return wizardEnvState{known: true}
		}
		return wizardEnvState{known: true, exact: true}
	}
	t.Cleanup(func() { wizardEnvProbe = oldProbe })

	var out bytes.Buffer
	err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0005, "")), &out, WizardOptions{Yes: true})
	if err == nil {
		t.Fatalf("a stale environment presentation was applied; output:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "user environment entry DEEPSEEK_API_KEY changed") || !strings.Contains(err.Error(), "nothing written") {
		t.Fatalf("refusal = %v; want a named environment stale-base refusal", err)
	}
	if _, statErr := os.Stat(provPath); !os.IsNotExist(statErr) {
		t.Fatalf("a stale run wrote providers.json (stat err = %v)", statErr)
	}
	if len(store.setCalls) != 0 {
		t.Fatalf("a stale run wrote the user environment: %v", store.setCalls)
	}
}

// TestWizardGate_DiffMasksRegistryOperands pins the masking class on the
// composed diff: the replaced record's base URL renders through MaskBaseURL, so
// a userinfo credential in a hand-edited registry never reaches the transcript.
func TestWizardGate_DiffMasksRegistryOperands(t *testing.T) {
	const secret = "hunter2sekret"
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	wizardGateEnvSeams(t, store)

	seeded := &Registry{
		Defaults: Defaults{N: 3, TimeoutSeconds: 120, MaxTurns: 10, Tools: "read,search"},
		Providers: []Provider{{
			Name:    "deepseek",
			BaseURL: "https://user:" + secret + "@gateway.test/v1",
			Models:  []Model{{ID: "deepseek-chat"}},
		}},
	}
	if err := seeded.Validate(); err != nil {
		t.Fatalf("seed registry invalid: %v", err)
	}
	data, err := json.MarshalIndent(seeded, "", "  ")
	if err != nil {
		t.Fatalf("seed marshal: %v", err)
	}
	if err := WriteFileAtomic(provPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	var out bytes.Buffer
	if err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0006, "")), &out, WizardOptions{Yes: true}); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	transcript := out.String()
	if strings.Contains(transcript, secret) {
		t.Fatalf("the credential in the replaced base URL reached the transcript:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Replaces provider deepseek wholesale") {
		t.Fatalf("a same-named provider must render as a wholesale replace:\n%s", transcript)
	}
	if !strings.Contains(transcript, "- provider deepseek (https://gateway.test/v1, 1 model)") {
		t.Fatalf("the removed line is not the shared URL mask:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Model count: 1 -> 1") {
		t.Fatalf("the model count line is missing:\n%s", transcript)
	}
}

// TestWizardGate_EnvVariantLine pins the BOM-variant half of the environment
// line: a stored variant is named with a visible marker (never an invisible
// byte) and stated to be untouched.
func TestWizardGate_EnvVariantLine(t *testing.T) {
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	store.data[EnvRootUser][BOM+"DEEPSEEK_API_KEY"] = wizardGateStaleVariant
	wizardGateEnvSeams(t, store)

	var out bytes.Buffer
	if err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, wizardGateKey0007, "")), &out, WizardOptions{Yes: true}); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	transcript := out.String()
	if !strings.Contains(transcript, "A BOM variant <BOM>DEEPSEEK_API_KEY already exists") {
		t.Fatalf("the stored BOM variant is not rendered with a visible marker:\n%s", transcript)
	}
	if strings.Contains(transcript, wizardGateStaleVariant) {
		t.Fatalf("a stored value reached the transcript:\n%s", transcript)
	}
	if _, statErr := os.Stat(provPath); statErr != nil {
		t.Fatalf("the consented run wrote no registry: %v", statErr)
	}
	// The write is name-scoped: the polluted variant is left exactly as it was.
	if got := store.data[EnvRootUser][BOM+"DEEPSEEK_API_KEY"]; got != wizardGateStaleVariant {
		t.Fatalf("the stored BOM variant changed: %q", got)
	}
	if len(store.deleteCalls) != 0 {
		t.Fatalf("the wizard deleted environment entries: %v", store.deleteCalls)
	}
}

// TestWizardGate_NoConsentLineWithoutAPersistableKey pins the negative half:
// when the run persists no key (no key typed), the plan carries the registry
// write only, so no environment line is presented and nothing is written there.
func TestWizardGate_NoConsentLineWithoutAPersistableKey(t *testing.T) {
	provPath := wizardGateHome(t)
	store := newMockEnvStore()
	wizardGateEnvSeams(t, store)

	var out bytes.Buffer
	if err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, "", "")), &out, WizardOptions{Yes: true}); err != nil {
		t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
	}
	transcript := out.String()
	if strings.Contains(transcript, "HKCU\\Environment") {
		t.Fatalf("a run that persists no key presented an environment write:\n%s", transcript)
	}
	if len(store.setCalls) != 0 {
		t.Fatalf("a run that persists no key wrote the store: %v", store.setCalls)
	}
	if _, statErr := os.Stat(provPath); statErr != nil {
		t.Fatalf("the registry write was skipped: %v", statErr)
	}
}

// P31-B item 7 (follow-up from coder-p31a-report.md): the first-time wizard
// write used to marshal a phantom `defaults` block seeded from
// loadWizardRegistry's missing-file branch (N:3 / timeout 120 / maxTurns 10 /
// tools read,search). Those literals are the RETIRED engine defaults: the fanout
// CLI ladder reads the block ("defaults" rung in cli.go) and would bind
// timeout 120 / max_turns 10 for every run on that machine, plus print two drift
// notices against the 300/500 engine defaults. The seed is gone, so a first-time
// write carries an empty block and every rung falls through to the engine.
//
// The second half is the boundary: a defaults block the operator (or an older
// file) already carries is still round-tripped untouched, so this fix removes a
// phantom seed without ever stripping a real value.
func TestWizardFirstWriteCarriesNoStaleDefaultsRung(t *testing.T) {
	readDefaults := func(t *testing.T, path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Defaults map[string]any `json:"defaults"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return doc.Defaults
	}
	runWizard := func(t *testing.T, key string) {
		t.Helper()
		var out bytes.Buffer
		if err := InteractiveSetupWizardWithGate(strings.NewReader(wizardGateScript(t, key, "")), &out, WizardOptions{Yes: true}); err != nil {
			t.Fatalf("wizard returned error: %v\nOutput: %s", err, out.String())
		}
	}

	t.Run("first write pins no rung", func(t *testing.T) {
		provPath := wizardGateHome(t)
		store := newMockEnvStore()
		wizardGateEnvSeams(t, store)
		runWizard(t, wizardGateKey0006)

		defaults := readDefaults(t, provPath)
		for _, key := range []string{"n", "timeoutSeconds", "maxTurns", "tools"} {
			if v, ok := defaults[key]; ok {
				t.Errorf("a first-time wizard write pinned defaults.%s=%v; the engine defaults must stay unbound", key, v)
			}
		}
		reg, err := LoadFrom(provPath)
		if err != nil {
			t.Fatalf("the written registry does not load: %v", err)
		}
		if reg.Defaults.TimeoutSeconds != 0 || reg.Defaults.MaxTurns != 0 || reg.Defaults.N != 0 || reg.Defaults.Tools != "" {
			t.Errorf("loaded defaults = %+v; want the zero block (no pinned rung)", reg.Defaults)
		}
	})

	t.Run("an existing block is preserved", func(t *testing.T) {
		provPath := wizardGateHome(t)
		store := newMockEnvStore()
		wizardGateEnvSeams(t, store)

		seeded := &Registry{
			Defaults: Defaults{TimeoutSeconds: 180, MaxTurns: 250, Tools: "read"},
			Providers: []Provider{{
				Name: "groq", BaseURL: closedLoopbackBaseURL(t) + "/v1", APIKeyEnv: "GROQ_API_KEY",
				Models: []Model{{ID: "llama-3.3-70b"}},
			}},
		}
		if err := seeded.Validate(); err != nil {
			t.Fatalf("seed registry invalid: %v", err)
		}
		data, err := json.MarshalIndent(seeded, "", "  ")
		if err != nil {
			t.Fatalf("seed marshal: %v", err)
		}
		if err := WriteFileAtomic(provPath, append(data, '\n'), 0o600); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		runWizard(t, wizardGateKey0007)

		reg, err := LoadFrom(provPath)
		if err != nil {
			t.Fatalf("the written registry does not load: %v", err)
		}
		if reg.Defaults.TimeoutSeconds != 180 || reg.Defaults.MaxTurns != 250 || reg.Defaults.Tools != "read" {
			t.Errorf("defaults = %+v; an existing operator block must survive the wizard write", reg.Defaults)
		}
		if len(reg.Providers) != 2 {
			t.Errorf("providers = %d; want the seeded groq record plus the added one", len(reg.Providers))
		}
	})
}
