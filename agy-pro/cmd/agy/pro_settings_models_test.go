package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/settings"
)

// CLI-twin tests for the model-level providers verbs (plan-v2 phase P7):
// model-add-json, model-update, model-delete. Two harnesses, both never
// touching the live registry:
//   - the in-memory installSettingsCore fake (pro_settings_test.go) for
//     happy-path delegation, usage errors, and rejection pins;
//   - a file-backed Core whose registry seams are the REAL load/save pair
//     (providers.Load + an atomic 0600 write, same shape as
//     settings.saveRegistryFile) pointed at a t.TempDir() providers.json via
//     EnvProviders (env-var NAME AGY_PRO_PROVIDERS), for the on-disk
//     sibling-survival and disk-immutability pins through the CLI path.

func twoModelRegistry() *providers.Registry {
	return &providers.Registry{Providers: []providers.Provider{
		{
			Name:      "acme",
			BaseURL:   "https://acme.example/v1",
			APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{
				{ID: "acme-fast", ContextLength: 128000, Capabilities: providers.Capabilities{AllowedTools: []string{"read"}}},
				{ID: "acme-slow"},
			},
		},
	}}
}

// installFileBackedSettingsCore seeds reg into a temp providers.json and
// installs a Core whose LoadReg/SaveReg are the real file seams against it.
// Returns the on-disk path for byte-level assertions.
func installFileBackedSettingsCore(t *testing.T, reg *providers.Registry) string {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "providers.json")
	t.Setenv(providers.EnvProviders, path)
	reg.Path = path
	if err := saveTestRegistry(reg); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	prev := newSettingsCore
	newSettingsCore = func() *settings.Core {
		return settings.NewCore(settings.Deps{
			VersionFunc: func() string { return "0.0-test" },
			LoadReg:     providers.Load,
			SaveReg:     func(r *providers.Registry) error { return saveTestRegistry(r) },
		})
	}
	t.Cleanup(func() { newSettingsCore = prev })
	return path
}

// saveTestRegistry mirrors settings.saveRegistryFile (MarshalIndent + newline,
// atomic 0600) so the CLI path under test writes through the same shape
// production uses.
func saveTestRegistry(reg *providers.Registry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return providers.WriteFileAtomic(reg.Path, data, 0o600)
}

func reloadTestRegistry(t *testing.T, path string) *providers.Registry {
	t.Helper()
	reg, err := providers.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload registry from disk: %v", err)
	}
	return reg
}

func acmeTestModels(t *testing.T, reg *providers.Registry) []providers.Model {
	t.Helper()
	p, ok := reg.Provider("acme")
	if !ok {
		t.Fatal("provider acme missing after reload")
	}
	return p.Models
}

func TestRunSettingsCmd_ModelAddJSON_HappyPath(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = &providers.Registry{Providers: []providers.Provider{
		{Name: "acme", BaseURL: "https://acme.example/v1", Models: []providers.Model{{ID: "m1"}}},
	}}

	// From a temp file (pipe stdin, so the explicit --yes consent token is
	// required: the write gate is pinned in
	// TestRunSettingsCmd_ModelVerbs_PipeRequiresYes).
	specPath := filepath.Join(t.TempDir(), "model.json")
	spec := `{"id":"m2","contextLength":64000,"capabilities":{"supportsTools":true,"allowedTools":["read","search"]}}`
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errw := runSettings(t, []string{"providers", "model-add-json", "acme", specPath, "--yes"}, "")
	if code != 0 {
		t.Fatalf("model-add-json exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] provider acme: model m2 added") {
		t.Errorf("model-add-json success line = %q", out)
	}
	if len(tc.reg.Providers[0].Models) != 2 || tc.reg.Providers[0].Models[1].ID != "m2" {
		t.Fatalf("registry models = %+v", tc.reg.Providers[0].Models)
	}
	if tc.reg.Providers[0].Models[1].ContextLength != 64000 {
		t.Errorf("appended model = %+v", tc.reg.Providers[0].Models[1])
	}

	// From stdin ("-"); --yes consents to the write on the non-interactive path.
	code, _, errw = runSettings(t, []string{"providers", "model-add-json", "acme", "-", "--yes"}, `{"id":"m3"}`)
	if code != 0 {
		t.Fatalf("model-add-json - exit = %d stderr=%s", code, errw)
	}
	if len(tc.reg.Providers[0].Models) != 3 || tc.reg.Providers[0].Models[2].ID != "m3" {
		t.Fatalf("registry models after stdin add = %+v", tc.reg.Providers[0].Models)
	}

	// Usage error: missing file operand.
	code, _, errw = runSettings(t, []string{"providers", "model-add-json", "acme"}, "")
	if code != 2 || !strings.Contains(errw, "Usage:") {
		t.Errorf("model-add-json missing file exit = %d; want 2 with usage (stderr=%s)", code, errw)
	}
}

func TestRunSettingsCmd_ModelUpdate_HappyPath(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = twoModelRegistry()

	// stdin ("-") input; --yes consents to the write on the non-interactive path.
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","contextLength":256000,"maxOutputTokens":8192}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] provider acme: model acme-slow updated") {
		t.Errorf("model-update success line = %q", out)
	}
	models := tc.reg.Providers[0].Models
	if len(models) != 2 {
		t.Fatalf("model count changed: %+v", models)
	}
	if models[1].ContextLength != 256000 || models[1].MaxOutputTokens != 8192 {
		t.Errorf("updated model = %+v", models[1])
	}

	// Usage error: missing file operand.
	code, _, errw = runSettings(t, []string{"providers", "model-update", "acme", "acme-slow"}, "")
	if code != 2 || !strings.Contains(errw, "Usage:") {
		t.Errorf("model-update missing file exit = %d; want 2 with usage (stderr=%s)", code, errw)
	}
}

func TestRunSettingsCmd_ModelDelete_HappyPath(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = twoModelRegistry()

	code, out, errw := runSettings(t, []string{"providers", "model-delete", "acme", "acme-slow", "--yes"}, "")
	if code != 0 {
		t.Fatalf("model-delete exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] provider acme: model acme-slow removed") {
		t.Errorf("model-delete success line = %q", out)
	}
	models := tc.reg.Providers[0].Models
	if len(models) != 1 || models[0].ID != "acme-fast" {
		t.Errorf("models after delete = %+v", models)
	}

	// Usage error: missing model id.
	code, _, errw = runSettings(t, []string{"providers", "model-delete", "acme"}, "")
	if code != 2 || !strings.Contains(errw, "Usage:") {
		t.Errorf("model-delete missing id exit = %d; want 2 with usage (stderr=%s)", code, errw)
	}
}

func TestRunSettingsCmd_ModelVerbs_MissingProvider(t *testing.T) {
	installSettingsCore(t)
	cases := []struct {
		name string
		args []string
	}{
		{"add", []string{"providers", "model-add-json", "ghost", "-", "--yes"}},
		{"update", []string{"providers", "model-update", "ghost", "m", "-", "--yes"}},
		{"delete", []string{"providers", "model-delete", "ghost", "m", "--yes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errw := runSettings(t, tc.args, `{"id":"m2"}`)
			if code != 1 {
				t.Errorf("exit = %d; want 1 (stderr=%s)", code, errw)
			}
			if !strings.Contains(errw, `provider "ghost" not found`) {
				t.Errorf("stderr = %q", errw)
			}
		})
	}
}

func TestRunSettingsCmd_ModelVerbs_MissingModel(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = twoModelRegistry()

	code, _, errw := runSettings(t, []string{"providers", "model-update", "acme", "ghost", "-", "--yes"}, `{"id":"g2"}`)
	if code != 1 || !strings.Contains(errw, `model "ghost" not found`) {
		t.Errorf("model-update missing model exit = %d stderr=%s", code, errw)
	}
	// The in-memory registry must be untouched by the rejected update.
	if len(tc.reg.Providers[0].Models) != 2 {
		t.Errorf("registry mutated by rejected update: %+v", tc.reg.Providers[0].Models)
	}

	code, _, errw = runSettings(t, []string{"providers", "model-delete", "acme", "ghost", "--yes"}, "")
	if code != 1 || !strings.Contains(errw, `model "ghost" not found`) {
		t.Errorf("model-delete missing model exit = %d stderr=%s", code, errw)
	}
	if len(tc.reg.Providers[0].Models) != 2 {
		t.Errorf("registry mutated by rejected delete: %+v", tc.reg.Providers[0].Models)
	}
}

// TestRunSettingsCmd_ModelAdd_DuplicateIDRejected pins the CLI path: a
// duplicate model ID exits 1 and the on-disk file stays byte-identical.
func TestRunSettingsCmd_ModelAdd_DuplicateIDRejected(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}

	code, _, errw := runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"}, `{"id":"acme-fast"}`)
	if code != 1 {
		t.Fatalf("duplicate add exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if !strings.Contains(errw, "already exists") {
		t.Errorf("duplicate add stderr = %q", errw)
	}
	if got, _ := os.ReadFile(path); string(got) != string(before) {
		t.Error("on-disk JSON changed after duplicate-ID rejection")
	}
}

// TestRunSettingsCmd_ModelAddJSON_SiblingSurvival pins the P5/P6 in-place
// contract through the full CLI path: adding a third model to a two-model
// provider in a temp registry leaves the first two models byte-identical
// (per-element JSON) after a real save/reload cycle, proving the CLI path
// never routes through UpsertProvider (which would rebuild the provider
// and reserialize every sibling).
func TestRunSettingsCmd_ModelAddJSON_SiblingSurvival(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	before := acmeTestModels(t, reloadTestRegistry(t, path))
	if len(before) != 2 {
		t.Fatalf("fixture models = %d", len(before))
	}
	b0, _ := json.Marshal(before[0])
	b1, _ := json.Marshal(before[1])

	code, out, errw := runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-third","contextLength":32000}`)
	if code != 0 {
		t.Fatalf("model-add-json exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "model acme-third added") {
		t.Errorf("success line = %q", out)
	}

	after := acmeTestModels(t, reloadTestRegistry(t, path))
	if len(after) != 3 {
		t.Fatalf("models after add = %d", len(after))
	}
	a0, _ := json.Marshal(after[0])
	a1, _ := json.Marshal(after[1])
	if string(a0) != string(b0) {
		t.Errorf("sibling 0 changed through the CLI path:\nbefore: %s\nafter:  %s", b0, a0)
	}
	if string(a1) != string(b1) {
		t.Errorf("sibling 1 changed through the CLI path:\nbefore: %s\nafter:  %s", b1, a1)
	}

	// Same pin for model-update through the CLI path: replacing the third
	// model leaves the untouched siblings byte-identical on disk.
	c2, _ := json.Marshal(after[2])
	code, _, errw = runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-third", "-", "--yes"},
		`{"id":"acme-third","contextLength":64000}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	reloaded := acmeTestModels(t, reloadTestRegistry(t, path))
	if len(reloaded) != 3 {
		t.Fatalf("models after update = %d", len(reloaded))
	}
	r0, _ := json.Marshal(reloaded[0])
	r1, _ := json.Marshal(reloaded[1])
	r2, _ := json.Marshal(reloaded[2])
	if string(r0) != string(b0) || string(r1) != string(b1) {
		t.Errorf("siblings changed through the model-update CLI path")
	}
	if string(r2) == string(c2) {
		t.Errorf("target model did not change through model-update: %s", r2)
	}
}

// TestRunSettingsCmd_ModelVerbs_BadJSON pins parse failures: exit 1, a
// readable parser error, and no registry mutation. An empty-ID record is
// rejected by the CLI-layer guard before Core ever sees it.
func TestRunSettingsCmd_ModelVerbs_BadJSON(t *testing.T) {
	tc := installSettingsCore(t)
	tc.reg = twoModelRegistry()

	code, _, errw := runSettings(t, []string{"providers", "model-add-json", "acme", "-"}, `{not json`)
	if code != 1 || !strings.Contains(errw, "invalid model spec JSON") {
		t.Errorf("bad JSON exit = %d stderr=%s", code, errw)
	}

	code, _, errw = runSettings(t, []string{"providers", "model-add-json", "acme", "-"}, `{"contextLength":1000}`)
	if code != 1 || !strings.Contains(errw, "id is required") {
		t.Errorf("empty-id exit = %d stderr=%s", code, errw)
	}
	if len(tc.reg.Providers[0].Models) != 2 {
		t.Errorf("registry mutated by rejected parses: %+v", tc.reg.Providers[0].Models)
	}
}

// TestRunSettingsCmd_ModelVerbs_KeyEchoMasked (C2): a key paste in an operand
// slot renders masked on both the error and the success lines, never raw.
func TestRunSettingsCmd_ModelVerbs_KeyEchoMasked(t *testing.T) {
	const planted = "sk-planted-model-verb-key-0123456789abcdef"
	const masked = "sk-pl****"

	t.Run("missing model error", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = twoModelRegistry()
		code, _, errw := runSettings(t, []string{"providers", "model-delete", "acme", planted, "--yes"}, "")
		if code != 1 {
			t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if strings.Contains(errw, planted) {
			t.Errorf("stderr echoes the planted key raw:\n%s", errw)
		}
		if !strings.Contains(errw, masked) {
			t.Errorf("stderr missing the readable mask:\n%s", errw)
		}
	})

	t.Run("success line masks provider operand", func(t *testing.T) {
		tc := installSettingsCore(t)
		tc.reg = &providers.Registry{Providers: []providers.Provider{
			{Name: planted, BaseURL: "https://acme.example/v1", Models: []providers.Model{{ID: "m1"}}},
		}}
		code, out, errw := runSettings(t, []string{"providers", "model-delete", planted, "m1", "--yes"}, "")
		if code != 0 {
			t.Fatalf("exit = %d stderr=%s", code, errw)
		}
		if strings.Contains(out, planted) {
			t.Errorf("stdout echoes the planted key raw:\n%s", out)
		}
		if !strings.Contains(out, masked) {
			t.Errorf("success line missing the readable mask:\n%s", out)
		}
	})
}

// stubStdinIsTTY swaps the console-detection seam (pro_swarm.go stdinIsTTY)
// so the console confirmation path is exercised headlessly: the tests feed
// scripted answers through a plain reader, never a real console.
func stubStdinIsTTY(t *testing.T, v bool) {
	t.Helper()
	prev := stdinIsTTY
	stdinIsTTY = func(in io.Reader) bool { return v }
	t.Cleanup(func() { stdinIsTTY = prev })
}

func diskBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	return data
}

// TestRunSettingsCmd_ModelVerbs_PipeRequiresYes pins the never-silent gate on
// the non-interactive path: without --yes every model twin prints the masked
// diff, refuses with an error naming the flag, and leaves the on-disk
// providers.json byte-identical; with --yes the write proceeds.
func TestRunSettingsCmd_ModelVerbs_PipeRequiresYes(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())

	refuse := func(args []string, stdin string) string {
		t.Helper()
		before := diskBytes(t, path)
		code, out, errw := runSettings(t, args, stdin)
		if code == 0 {
			t.Fatalf("%v exit = 0; want non-zero (stdout=%s stderr=%s)", args, out, errw)
		}
		if !strings.Contains(errw, "--yes") {
			t.Errorf("%v refusal must name the --yes flag: stderr=%s", args, errw)
		}
		if !strings.Contains(out, "(0600, atomic)") {
			t.Errorf("%v must print the pre-write diff with the atomic-0600 sentence: stdout=%s", args, out)
		}
		if !bytes.Equal(diskBytes(t, path), before) {
			t.Errorf("%v wrote providers.json without consent", args)
		}
		return out
	}

	refuse([]string{"providers", "model-add-json", "acme", "-"}, `{"id":"acme-third"}`)
	refuse([]string{"providers", "model-update", "acme", "acme-slow", "-"}, `{"id":"acme-slow"}`)
	refuse([]string{"providers", "model-delete", "acme", "acme-slow"}, "")

	// The update refusal still presents the field before/after diff read from
	// the on-disk record.
	out := refuse([]string{"providers", "model-update", "acme", "acme-slow", "-"},
		`{"id":"acme-slow","contextLength":256000}`)
	if !strings.Contains(out, "contextLength: 0 -> 256000") {
		t.Errorf("update refusal diff missing the before/after line: stdout=%s", out)
	}

	// --yes consents: the add writes through the real save seam.
	code, out, errw := runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"}, `{"id":"acme-third"}`)
	if code != 0 {
		t.Fatalf("model-add-json --yes exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "[SUCCESS] provider acme: model acme-third added") {
		t.Errorf("success line = %q", out)
	}
	if n := len(acmeTestModels(t, reloadTestRegistry(t, path))); n != 3 {
		t.Fatalf("models after consented add = %d; want 3", n)
	}
}

// TestRunSettingsCmd_ModelVerbs_ConsoleConfirmation pins the console path
// through the stdinIsTTY seam: n, Esc, and EOF abort with nothing written;
// only y reaches the Core write.
func TestRunSettingsCmd_ModelVerbs_ConsoleConfirmation(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	stubStdinIsTTY(t, true)
	specPath := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(specPath, []byte(`{"id":"acme-third"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"providers", "model-add-json", "acme", specPath}

	// n declines: exit 0, cancelled note, disk untouched.
	before := diskBytes(t, path)
	code, out, errw := runSettings(t, args, "n\n")
	if code != 0 {
		t.Fatalf("declined console add exit = %d; want 0 (stderr=%s)", code, errw)
	}
	if !strings.Contains(out, "[CANCELLED] nothing written") {
		t.Errorf("decline must say nothing was written: stdout=%s", out)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("declined console add wrote providers.json")
	}

	// Esc declines the same way.
	code, _, _ = runSettings(t, args, "\x1b\n")
	if code != 0 || !bytes.Equal(diskBytes(t, path), before) {
		t.Errorf("Esc-declined console add exit = %d; want 0 with disk untouched", code)
	}

	// EOF is a refusal, not a cancel: /dev/null and NUL are character devices
	// the seam cannot tell from a console, and an immediate EOF (zero bytes)
	// must take the non-interactive path like a pipe.
	code, _, errw = runSettings(t, args, "")
	if code == 0 {
		t.Fatalf("EOF-declined console add exit = 0; want non-zero")
	}
	if !strings.Contains(errw, "--yes") {
		t.Errorf("EOF at the prompt must name the --yes flag: stderr=%s", errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("EOF-declined console add wrote providers.json")
	}

	// y consents and the write proceeds.
	code, _, errw = runSettings(t, args, "y\n")
	if code != 0 {
		t.Fatalf("consented console add exit = %d stderr=%s", code, errw)
	}
	if n := len(acmeTestModels(t, reloadTestRegistry(t, path))); n != 3 {
		t.Fatalf("models after consented console add = %d; want 3", n)
	}
}

// TestRunSettingsCmd_ModelVerbs_PromptReadBounded pins the round-6 seat2-F4
// fix: the console consent read is capped, so a newline-free character device
// (a huge 'y' stream, the /dev-zero shape) cannot buffer memory without
// limit. The over-long answer is refused like any non-y answer: exit 0,
// [CANCELLED], providers.json byte-identical. A short y answer on a longer
// stream still consents, proving the cap is a read bound, not a refusal of
// everything past it.
func TestRunSettingsCmd_ModelVerbs_PromptReadBounded(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	stubStdinIsTTY(t, true)
	specPath := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(specPath, []byte(`{"id":"acme-third"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"providers", "model-add-json", "acme", specPath}
	before := diskBytes(t, path)

	// 1 MiB of 'y' with no newline: the bounded read must return promptly and
	// refuse, not buffer the whole stream.
	code, out, _ := runSettings(t, args, strings.Repeat("y", 1<<20))
	if code != 0 {
		t.Errorf("over-long answer exit = %d; want 0 (cancelled)", code)
	}
	if !strings.Contains(out, "[CANCELLED] nothing written") {
		t.Errorf("over-long answer must cancel with nothing written: stdout=%s", out)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("over-long answer wrote providers.json")
	}

	// A normal y answer still consents.
	code, _, errw := runSettings(t, args, "y\n")
	if code != 0 {
		t.Fatalf("short y answer exit = %d stderr=%s", code, errw)
	}
	if n := len(acmeTestModels(t, reloadTestRegistry(t, path))); n != 3 {
		t.Fatalf("models after short y answer = %d; want 3", n)
	}
}

// TestRunSettingsCmd_ModelVerbs_SecretShapedSpecRejected pins the CLI-twin
// credential guard: a secret-shaped value in any free-text field of the JSON
// spec (a misdirected key paste) is refused with a masked error and nothing
// is written. The 20-rune fixture is an opaque mixed-letter-digit run with no
// separators (the keyaudit operandLooksSecret shape the TUI form guards but
// the CLI ingest lacked).
func TestRunSettingsCmd_ModelVerbs_SecretShapedSpecRejected(t *testing.T) {
	const twenty = "Ab3dEf7Gh9jK1mNpQr5t" // 20 runes, opaque, mixed letter/digit
	path := installFileBackedSettingsCore(t, twoModelRegistry())

	cases := []struct {
		name    string
		spec    string
		rawWant string
	}{
		{"opaque run in id", `{"id":"` + twenty + `"}`, twenty},
		{"known prefix in family at any length", `{"id":"m9","family":"sk-live"}`, "sk-live"},
		{"opaque run in a tag", `{"id":"m9","tags":["` + twenty + `"]}`, twenty},
		{"opaque run in a tool grant", `{"id":"m9","capabilities":{"allowedTools":["read_file","` + twenty + `"]}}`, twenty},
		{"known prefix in reasoningEffort", `{"id":"m9","capabilities":{"reasoningEffort":"sk-live"}}`, "sk-live"},
		// Round-6 F-01: a 16+ rune base64 credential carrying + / = (the AWS
		// secret class) is refused too; the widened opaque-token alphabet
		// admits those runes into the run.
		{"opaque base64 run with slash in id", `{"id":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"opaque base64 run with plus and pad in family", `{"id":"m9","family":"Ab3dEf7Gh9jK1mNp+/Qr="}`, "Ab3dEf7Gh9jK1mNp+/Qr="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := diskBytes(t, path)
			code, _, errw := runSettings(t,
				[]string{"providers", "model-add-json", "acme", "-", "--yes"}, tc.spec)
			if code != 1 {
				t.Fatalf("exit = %d; want 1 (stderr=%s)", code, errw)
			}
			if !strings.Contains(errw, "secret material (value hidden)") {
				t.Errorf("stderr missing the masked refusal: %s", errw)
			}
			if strings.Contains(errw, tc.rawWant) {
				t.Errorf("stderr echoes the secret-shaped value raw: %s", errw)
			}
			if !bytes.Equal(diskBytes(t, path), before) {
				t.Error("rejected secret-shaped spec wrote providers.json")
			}
		})
	}
}

// TestRunSettingsCmd_ModelVerbs_ToolsDiffMasking pins the round-5 CLI-twin
// fix: the consent diff masks each list item BEFORE joining (a
// credential-shaped grant cannot hide behind the join separator), and the
// spec guard accepts legitimate separator-carrying tool names and
// single-class long ids on both the CLI and the TUI-form classifier (the
// drift pin: an all-letter 16+ id is a name, not a key).
func TestRunSettingsCmd_ModelVerbs_ToolsDiffMasking(t *testing.T) {
	const planted = "Ab3dEf7Gh9jK1mNpQr5t" // 20 runes, opaque, mixed letter/digit
	const masked = "Ab3dE****"

	// 1. The on-disk before column masks a credential-shaped grant per item.
	reg := twoModelRegistry()
	reg.Providers[0].Models[0].Capabilities.AllowedTools = []string{"read", planted}
	path := installFileBackedSettingsCore(t, reg)
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-fast", "-", "--yes"},
		`{"id":"acme-fast","maxOutputTokens":4096}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if strings.Contains(out, planted) {
		t.Errorf("consent diff echoes the credential-shaped grant raw:\n%s", out)
	}
	if !strings.Contains(out, "tools: read, "+masked+" -> ") {
		t.Errorf("consent diff missing the per-item masked before column:\n%s", out)
	}

	// 2. Legitimate long tool names stay readable end to end.
	path = installFileBackedSettingsCore(t, twoModelRegistry())
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-third","capabilities":{"allowedTools":["read_url_content","list_dir"]}}`)
	if code != 0 {
		t.Fatalf("model-add-json with legitimate tools exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "+ tools: read_url_content, list_dir") {
		t.Errorf("legitimate tool names must render readable in the diff:\n%s", out)
	}
	tools := acmeTestModels(t, reloadTestRegistry(t, path))[2].Capabilities.AllowedTools
	if strings.Join(tools, ",") != "read_url_content,list_dir" {
		t.Fatalf("AllowedTools on disk = %v; want [read_url_content list_dir]", tools)
	}

	// 3. Drift pin: an all-letter 16+ rune id is a name on BOTH classifiers
	// and must be accepted (the old CLI guard refused it, the TUI form
	// accepted it).
	code, _, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"abcdefghijklmnop"}`)
	if code != 0 {
		t.Fatalf("all-letter 16+ id must be accepted; exit = %d stderr=%s", code, errw)
	}
	if got := acmeTestModels(t, reloadTestRegistry(t, path))[3].ID; got != "abcdefghijklmnop" {
		t.Fatalf("id on disk = %q; want abcdefghijklmnop", got)
	}

	// 4. Round-6 F-01 accept pin: a separator-carrying legitimate model id is
	// still accepted on both paths (the widened base64 alphabet only admits
	// + / = into the run; the '-' '.' '/' separators of a real id still break
	// it, and no contiguous run here reaches the 16-rune floor).
	const longID = "meta-llama/Llama-3.3-70B-Instruct-Turbo"
	path = installFileBackedSettingsCore(t, twoModelRegistry())
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"`+longID+`"}`)
	if code != 0 {
		t.Fatalf("separator-carrying long id must be accepted; exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "+ id: "+longID) {
		t.Errorf("consent diff must name the separator-carrying id:\n%s", out)
	}
}

// TestRunSettingsCmd_ModelVerbs_DiffPresentation pins what the pre-write
// presentation shows on the consenting path: the mutation sentence, the model
// count before/after, the atomic-0600 sentence, and per-field diff lines
// (before/after for update, added lines for add, the removed line for
// delete). Every operand is masked.
func TestRunSettingsCmd_ModelVerbs_DiffPresentation(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())

	// Update: before/after against the on-disk record.
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","contextLength":256000,"maxOutputTokens":8192}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"[agy-pro] Approve Registry Write",
		"Updates model acme-slow in provider acme. Key values are untouched.",
		"Model count: 2 -> 2",
		modelWriteAtomicSentence,
		"  contextLength: 0 -> 256000",
		"  maxOutputTokens: 0 -> 8192",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("update presentation missing %q; stdout=%s", want, out)
		}
	}

	// Add: count grows, fields render as added lines.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-third","family":"acme-1","tags":["fast"],"contextLength":32000}`)
	if code != 0 {
		t.Fatalf("model-add-json exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"Adds model acme-third to provider acme. Key values are untouched.",
		"Model count: 2 -> 3",
		"+ id: acme-third",
		"+ family: acme-1",
		"+ tags: fast",
		"+ contextLength: 32000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("add presentation missing %q; stdout=%s", want, out)
		}
	}

	// Delete: count shrinks, the removed line names the model.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-delete", "acme", "acme-fast", "--yes"}, "")
	if code != 0 {
		t.Fatalf("model-delete exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"Removes model acme-fast from provider acme. Key values are untouched.",
		"Model count: 3 -> 2",
		"- model acme-fast",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete presentation missing %q; stdout=%s", want, out)
		}
	}
	if got := len(acmeTestModels(t, reloadTestRegistry(t, path))); got != 2 {
		t.Fatalf("models after the three consented writes = %d; want 2", got)
	}
}

// TestRunSettingsCmd_ModelVerbs_NullDeviceRequiresYes pins the /dev/null
// edge: the null device IS a character device (the raw stdinIsTTY seam alone
// would prompt it), so it must take the non-interactive path and refuse
// without --yes, writing nothing.
func TestRunSettingsCmd_ModelVerbs_NullDeviceRequiresYes(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	nullIn, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		t.Skipf("open %s: %v", os.DevNull, err)
	}
	defer nullIn.Close()

	before := diskBytes(t, path)
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	code := runSettingsCmd([]string{"providers", "model-delete", "acme", "acme-slow"}, nullIn, out, errw)
	if code == 0 {
		t.Fatalf("null-device stdin exit = 0; want non-zero (stdout=%s stderr=%s)", out, errw)
	}
	if !strings.Contains(errw.String(), "--yes") {
		t.Errorf("null-device stdin refusal must name the --yes flag: stderr=%s", errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("null-device stdin without --yes wrote providers.json")
	}
}
