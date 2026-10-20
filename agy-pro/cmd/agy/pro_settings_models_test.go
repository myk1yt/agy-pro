package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/openaiup"
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
// credential-shaped grant cannot hide behind the join separator), the spec
// guard accepts legitimate separator-carrying tool names, and the 16+ rune
// single-class id class (an all-letter word) is REFUSED, which is the verdict
// the TUI model form has carried since FIX-7A. That last arm moved in FIX-7E:
// this pin used to assert acceptance on the premise that the TUI form accepted
// the same id, and the TUI form's authority (providers.MaskOperand) masks a
// 16+ single-class word, so the two doors now refuse it together.
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

	// 3. Parity pin (FIX-7E, moved): a 16+ rune single-class id (an all-letter
	// word) is refused, because the shared NAME authority's operand arm masks
	// it and the TUI model form has refused the same class since FIX-7A. This
	// arm used to assert acceptance; the premise it rested on ("the TUI form
	// accepted it") no longer holds, and a CLI door that accepts a value its own
	// transcripts mask is the twin divergence this unit closes.
	before := diskBytes(t, path)
	code, _, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"abcdefghijklmnop"}`)
	if code != 1 {
		t.Fatalf("16+ single-class id exit = %d, want 1; stderr=%s", code, errw)
	}
	if !strings.Contains(errw, "secret material (value hidden)") {
		t.Errorf("stderr missing the masked refusal: %s", errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("a refused single-class id wrote providers.json")
	}
	if n := len(acmeTestModels(t, reloadTestRegistry(t, path))); n != 3 {
		t.Fatalf("model count = %d after the refusal; want 3", n)
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

// TestRunSettingsCmd_ModelVerbs_CapabilityDiffLines pins the phase-2 CLI-twin
// diff parity with the TUI form: the model capability fields (thinking,
// reasoningEffort, vision, video, and the supportsTools grant) appear in the
// pre-write presentation when set/changed and stay silent when untouched,
// so an unchanged update still shows the byte-stable "(no field changes)"
// marker.
func TestRunSettingsCmd_ModelVerbs_CapabilityDiffLines(t *testing.T) {
	// (a) model-add-json with a thinking model: the new lines render in the
	// approval presentation, masked through the same MaskOperand discipline as
	// the neighboring capability lines.
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	code, out, errw := runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-think","capabilities":{"supportsThinking":true,"reasoningEffort":"high","supportsVision":true,"supportsVideo":true}}`)
	if code != 0 {
		t.Fatalf("model-add-json exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"+ thinking: true",
		"+ reasoningEffort: high",
		"+ vision: true",
		"+ video: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("add presentation missing %q; stdout=%s", want, out)
		}
	}
	added := acmeTestModels(t, reloadTestRegistry(t, path))[2].Capabilities
	if !added.SupportsThinking || added.ReasoningEffort != "high" || !added.SupportsVision || !added.SupportsVideo {
		t.Errorf("persisted capabilities = %+v", added)
	}

	// A model without any capability flag renders none of the new lines.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"}, `{"id":"acme-plain"}`)
	if code != 0 {
		t.Fatalf("plain model-add-json exit = %d stderr=%s", code, errw)
	}
	for _, absent := range []string{"thinking", "reasoningEffort", "vision", "video"} {
		if strings.Contains(out, absent) {
			t.Errorf("plain add must not emit %q; stdout=%s", absent, out)
		}
	}

	// (b) model-update changing the effort low -> high: the before -> after
	// line reads from the on-disk record.
	reg := twoModelRegistry()
	reg.Providers[0].Models[1].Capabilities = providers.Capabilities{
		ReasoningEffort: "low",
	}
	path = installFileBackedSettingsCore(t, reg)
	code, out, errw = runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","capabilities":{"supportsThinking":true,"reasoningEffort":"high"}}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"  reasoningEffort: low -> high",
		"  thinking: false -> true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("update presentation missing %q; stdout=%s", want, out)
		}
	}
	updated := acmeTestModels(t, reloadTestRegistry(t, path))[1].Capabilities
	if updated.ReasoningEffort != "high" || !updated.SupportsThinking {
		t.Errorf("persisted capabilities = %+v", updated)
	}

	// (c) An update that changes nothing capability-related emits no new
	// lines: the unchanged marker stays byte-stable.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","capabilities":{"supportsThinking":true,"reasoningEffort":"high"}}`)
	if code != 0 {
		t.Fatalf("no-op model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "(no field changes)") {
		t.Errorf("no-op update must show the unchanged marker; stdout=%s", out)
	}
	for _, absent := range []string{"thinking:", "reasoningEffort:", "vision:", "video:"} {
		if strings.Contains(out, absent) {
			t.Errorf("no-op update must not emit %q; stdout=%s", absent, out)
		}
	}
}

// TestRunSettingsCmd_ModelVerbs_CapabilityFixRound pins the reconciled
// review findings F-C1..F-C4 on the CLI twin:
//
//   - F-C1: a marker-decorated credential in a capability slot renders
//     masked (MaskRegistryValue discipline) in the approval presentation,
//     never raw, on both the add-side renderer and the update before
//     column read from a hand-edited disk record.
//   - F-C2: a supportsTools flip can no longer hide behind "(no field
//     changes)": the whole-record update prints the grant line.
//   - F-C3: the closed effort set is enforced at parse: "HIGH" normalizes
//     and persists as "high"; "extreme" is refused non-echoing with nothing
//     written.
//   - F-C4: an ESC byte in the effort operand is stripped before the value
//     reaches the consent transcript (the CLI printer has no painter).
//
// G-C4 replaces the plant this pin originally used. The old plant was
// "./AKIAIOSFODNN7EXAMPLE", which the generic redactor (awsKeyIDRe) renders as
// "./[REDACTED]" even under plain MaskOperand: every assertion below passed
// against the unfixed renderer, so the pin proved nothing. The plant is now a
// lowercase decorated opaque run with no credential prefix, no uppercase and
// no AKIA shape, i.e. invisible to awsKeyIDRe, skRe and the prefix list. Only
// the registry-slot masker (MaskRegistryValue -> MaskSecret of the
// marker-stripped core) renders it masked, so the pin separates the two
// maskers. Verified by reverting the masker in a scratch copy: the assertions
// fail there.
func TestRunSettingsCmd_ModelVerbs_CapabilityFixRound(t *testing.T) {
	const decorated = "./ab3def7gh9jk1mnpqr5t"
	const maskedDecorated = "ab3de****"

	// F-C1, add side (renderer-level pin: the decorated value is refused by
	// the F-C3 set validation before it could ever reach the CLI ingest, so
	// the add-side masking discipline is pinned at the renderer, which is
	// also the seam the disk-borne before column flows through on update).
	addLines := modelAddDiffLines(providers.Model{ID: "m9",
		Capabilities: providers.Capabilities{ReasoningEffort: decorated, SupportsThinking: true}})
	joined := strings.Join(addLines, "\n")
	if !strings.Contains(joined, "+ reasoningEffort: "+maskedDecorated) {
		t.Errorf("add-side capability line must mask the decorated credential:\n%s", joined)
	}
	if strings.Contains(joined, decorated) {
		t.Errorf("add-side capability line echoes the decorated credential raw:\n%s", joined)
	}
	if !strings.Contains(joined, "+ thinking: true") {
		t.Errorf("benign capability operands stay readable:\n%s", joined)
	}

	// F-C1, update side end to end: the on-disk record carries the decorated
	// value (hand-edit path, no value-domain validation on load); the update
	// to a benign effort renders the masked head in the before column.
	reg := twoModelRegistry()
	reg.Providers[0].Models[1].Capabilities = providers.Capabilities{ReasoningEffort: decorated}
	path := installFileBackedSettingsCore(t, reg)
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","capabilities":{"reasoningEffort":"high"}}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "reasoningEffort: "+maskedDecorated+" -> high") {
		t.Errorf("update diff must mask the decorated before operand: stdout=%s", out)
	}
	if strings.Contains(out, decorated) {
		t.Errorf("update diff echoes the decorated credential raw: stdout=%s", out)
	}

	// F-C2: flipping supportsTools on a record whose grant list is empty
	// must print the grant line, not "(no field changes)".
	path = installFileBackedSettingsCore(t, twoModelRegistry())
	code, out, errw = runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","capabilities":{"supportsTools":true}}`)
	if code != 0 {
		t.Fatalf("supportsTools update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "  supportsTools: false -> true") {
		t.Errorf("supportsTools flip must show the change line: stdout=%s", out)
	}
	if strings.Contains(out, "(no field changes)") {
		t.Errorf("supportsTools flip must not hide behind the unchanged marker: stdout=%s", out)
	}
	if !acmeTestModels(t, reloadTestRegistry(t, path))[1].Capabilities.CanUseTools() {
		t.Error("supportsTools flip did not persist")
	}

	// F-C3 accept pin: "HIGH" normalizes to "high" and persists.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-effort","capabilities":{"reasoningEffort":"HIGH"}}`)
	if code != 0 {
		t.Fatalf("uppercase effort exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "+ reasoningEffort: high") {
		t.Errorf("normalized effort must render in the diff: stdout=%s", out)
	}
	if got := acmeTestModels(t, reloadTestRegistry(t, path))[2].Capabilities.ReasoningEffort; got != "high" {
		t.Errorf("persisted effort = %q; want high", got)
	}

	// F-C3 refusal pin: "extreme" is out of set, refused non-echoing,
	// nothing written.
	before := diskBytes(t, path)
	code, _, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-effort2","capabilities":{"reasoningEffort":"extreme"}}`)
	if code != 1 {
		t.Fatalf("out-of-set effort exit = %d; want 1 (stderr=%s)", code, errw)
	}
	if !strings.Contains(errw, "not a supported reasoning effort") {
		t.Errorf("refusal must name the effort validation: stderr=%s", errw)
	}
	if strings.Contains(errw, "extreme") {
		t.Errorf("refusal must not echo the value: stderr=%s", errw)
	}
	if !bytes.Equal(diskBytes(t, path), before) {
		t.Error("out-of-set effort wrote providers.json")
	}

	// F-C4: an ESC byte inside the effort operand is stripped before the
	// consent transcript ("hi<ESC>gh" normalizes to "high"); no escape byte
	// reaches stdout. The spec carries the byte as the JSON \u001b escape,
	// the only wire-legal form encoding/json accepts.
	code, out, errw = runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-esc","capabilities":{"reasoningEffort":"hi`+`\u001b`+`gh"}}`)
	if code != 0 {
		t.Fatalf("ESC-carrying effort exit = %d stderr=%s", code, errw)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("escape byte reached the consent transcript: stdout=%q", out)
	}
	if !strings.Contains(out, "+ reasoningEffort: high") {
		t.Errorf("stripped effort must render clean in the diff: stdout=%s", out)
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

// assertDiffLines compares a rendered diff line by line, so a masking change
// that moves even one benign operand fails with the exact line.
func assertDiffLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("diff line count = %d; want %d\ngot:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("diff line %d = %q; want %q", i, got[i], want[i])
		}
	}
}

// TestRunSettingsCmd_ModelVerbs_RegistrySlotDiffMasking pins G-C1: the
// id/family/tags/allowedTools operands of both diff builders are registry
// slots, so they route through the registry-slot masker (MaskRegistryValue +
// stripDiffControls), not through MaskOperand and its location exemption.
//
// The plant is the G-C4 lowercase decorated opaque run: it starts with "./"
// (so MaskOperand's location exemption keeps it readable) and defeats
// awsKeyIDRe and the credential prefixes (so the generic redactor leaves it
// alone too). Only the registry-slot masker renders it masked, which is what
// makes this pin fail against the previous renderer. The second half pins the
// benign transcripts line by line: widening the discipline must not move a
// single byte of a benign presentation.
func TestRunSettingsCmd_ModelVerbs_RegistrySlotDiffMasking(t *testing.T) {
	const plant = "./ab3def7gh9jk1mnpqr5t"
	const core = "ab3def7gh9jk1mnpqr5t"
	const masked = "ab3de****"

	t.Run("add side masks every registry slot", func(t *testing.T) {
		lines := modelAddDiffLines(providers.Model{
			ID:     plant,
			Family: plant,
			Tags:   []string{"fast", plant},
			Capabilities: providers.Capabilities{
				AllowedTools: []string{"read", plant},
			},
		})
		joined := strings.Join(lines, "\n")
		for _, want := range []string{
			"+ id: " + masked,
			"+ family: " + masked,
			"+ tags: fast, " + masked,
			"+ tools: read, " + masked,
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("add-side diff missing %q:\n%s", want, joined)
			}
		}
		if strings.Contains(joined, core) {
			t.Errorf("add-side diff echoes the credential core raw:\n%s", joined)
		}
	})

	t.Run("changed side masks both columns", func(t *testing.T) {
		before := providers.Model{
			ID:     plant,
			Family: plant,
			Tags:   []string{plant},
			Capabilities: providers.Capabilities{
				AllowedTools: []string{plant},
			},
		}
		after := providers.Model{
			ID:     "m1",
			Family: "acme-1",
			Tags:   []string{"fast"},
			Capabilities: providers.Capabilities{
				AllowedTools: []string{"read"},
			},
		}
		lines := modelChangedDiffLines(before, after)
		assertDiffLines(t, lines, []string{
			"  id: " + masked + " -> m1",
			"  family: " + masked + " -> acme-1",
			"  tags: " + masked + " -> fast",
			"  tools: " + masked + " -> read",
		})
		if strings.Contains(strings.Join(lines, "\n"), core) {
			t.Errorf("changed diff echoes the credential core raw:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("benign add transcript unchanged", func(t *testing.T) {
		disabled := false
		lines := modelAddDiffLines(providers.Model{
			ID:              "acme-third",
			Family:          "acme-1",
			Tags:            []string{"fast", "vision-capable"},
			Enabled:         &disabled,
			ContextLength:   32000,
			MaxOutputTokens: 8192,
			Capabilities: providers.Capabilities{
				SupportsTools:    true,
				SupportsThinking: true,
				ReasoningEffort:  "high",
				SupportsVision:   true,
				SupportsVideo:    true,
				AllowedTools:     []string{"read", "list_dir"},
			},
		})
		assertDiffLines(t, lines, []string{
			"+ id: acme-third",
			"+ family: acme-1",
			"+ tags: fast, vision-capable",
			"+ enabled: false",
			"+ contextLength: 32000",
			"+ maxOutputTokens: 8192",
			"+ tools: read, list_dir",
			"+ supportsTools: true",
			"+ thinking: true",
			"+ reasoningEffort: high",
			"+ vision: true",
			"+ video: true",
		})
	})

	t.Run("benign changed transcript unchanged", func(t *testing.T) {
		before := providers.Model{
			ID:              "acme-slow",
			Family:          "acme-1",
			Tags:            []string{"fast"},
			ContextLength:   128000,
			MaxOutputTokens: 4096,
			Capabilities: providers.Capabilities{
				SupportsTools: true,
				AllowedTools:  []string{"read"},
			},
		}
		after := providers.Model{
			ID:              "acme-slow",
			Family:          "acme-2",
			Tags:            []string{"fast", "vision"},
			ContextLength:   256000,
			MaxOutputTokens: 4096,
			Capabilities: providers.Capabilities{
				SupportsTools:    true,
				SupportsThinking: true,
				ReasoningEffort:  "high",
				AllowedTools:     []string{"read", "list_dir"},
			},
		}
		assertDiffLines(t, modelChangedDiffLines(before, after), []string{
			"  family: acme-1 -> acme-2",
			"  tags: fast -> fast, vision",
			"  contextLength: 128000 -> 256000",
			"  tools: read -> read, list_dir",
			"  thinking: false -> true",
			"  reasoningEffort:  -> high",
		})
	})

	t.Run("no-change marker unchanged", func(t *testing.T) {
		m := providers.Model{ID: "acme-slow", Family: "acme-1", ContextLength: 1000}
		assertDiffLines(t, modelChangedDiffLines(m, m), []string{"(no field changes)"})
	})
}

// TestRunSettingsCmd_ModelVerbs_StaleDiskFamilyMasked pins G-C1 end to end on
// the live path: the add-side ingest now refuses a decorated family (G-C2), so
// the record that can still carry one is a hand-edited providers.json. The
// update presentation reads that record for its before column, and the column
// must mask it.
func TestRunSettingsCmd_ModelVerbs_StaleDiskFamilyMasked(t *testing.T) {
	const plant = "./ab3def7gh9jk1mnpqr5t"

	reg := twoModelRegistry()
	reg.Providers[0].Models[1].Family = plant
	path := installFileBackedSettingsCore(t, reg)

	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","family":"acme-1"}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "  family: ab3de**** -> acme-1") {
		t.Errorf("before column must mask the decorated disk family: stdout=%s", out)
	}
	if strings.Contains(out, "ab3def7gh9jk1mnpqr5t") {
		t.Errorf("before column echoes the disk family core raw: stdout=%s", out)
	}
	if got := acmeTestModels(t, reloadTestRegistry(t, path))[1].Family; got != "acme-1" {
		t.Errorf("family on disk = %q; want acme-1", got)
	}
}

// TestRunSettingsCmd_ModelVerbs_DecoratedSecretSpecRejected pins G-C2: the
// ingest classifier now also classifies the location-marker-stripped core, so a
// single leading marker cannot smuggle a credential past the shape test. Before
// the fix the first case below passed ingest, persisted into providers.json,
// and printed raw in the consent transcript (the two-security-seat PoC).
func TestRunSettingsCmd_ModelVerbs_DecoratedSecretSpecRejected(t *testing.T) {
	const awsShaped = "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	const lowerAwsShaped = "wjalrxutnfemik7mdengbpxrficyexamplekey"
	const opaque = "ab3def7gh9jk1mnpqr5t"
	path := installFileBackedSettingsCore(t, twoModelRegistry())

	cases := []struct {
		name    string
		spec    string
		rawWant string
	}{
		{"PoC: dot-slash decorated family", `{"id":"m1","family":"./` + awsShaped + `"}`, awsShaped},
		{"dot-slash decorated lowercase family", `{"id":"m1","family":"./` + lowerAwsShaped + `"}`, lowerAwsShaped},
		{"backslash decorated id", `{"id":".\\` + opaque + `"}`, opaque},
		{"scheme-decorated family", `{"id":"m1","family":"https://` + awsShaped + `"}`, awsShaped},
		{"stacked markers in a tag", `{"id":"m9","tags":["./C:\\` + opaque + `"]}`, opaque},
		{"decorated tool grant", `{"id":"m9","capabilities":{"allowedTools":["./` + opaque + `"]}}`, opaque},
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
				t.Errorf("stderr echoes the decorated value raw: %s", errw)
			}
			if !bytes.Equal(diskBytes(t, path), before) {
				t.Error("rejected decorated spec wrote providers.json")
			}
		})
	}

	// Location-shaped model fields are refused on this door exactly as the TUI
	// model form refuses them. The shared authority's rewrite arm is why:
	// MaskSlot renders the marker-stripped core, so a frame would show bytes the
	// record does not carry. FIX-7E moved this arm (it asserted acceptance and a
	// verbatim persist of "./models/acme-third" plus "https://acme.example/v1");
	// the TUI form has pinned the same refusal for a path, a Windows path and a
	// URL since FIX-7A, and the class is a held product decision (whether a model
	// field may legally be a path or a URL). This door carries no local
	// exemption for it.
	t.Run("location-shaped values are refused like the TUI door", func(t *testing.T) {
		path := installFileBackedSettingsCore(t, twoModelRegistry())
		specs := []string{
			`{"id":"./models/acme-third"}`,
			`{"id":"acme-third","family":"https://acme.example/v1"}`,
			`{"id":"acme-third","family":"C:\\models\\gemini-pro"}`,
		}
		for _, spec := range specs {
			before := diskBytes(t, path)
			code, _, errw := runSettings(t,
				[]string{"providers", "model-add-json", "acme", "-", "--yes"}, spec)
			if code != 1 {
				t.Fatalf("%s: exit = %d, want 1 (stderr=%s)", spec, code, errw)
			}
			if !strings.Contains(errw, "secret material (value hidden)") {
				t.Errorf("%s: stderr missing the masked refusal: %s", spec, errw)
			}
			if !bytes.Equal(diskBytes(t, path), before) {
				t.Errorf("%s: a refused location-shaped spec wrote providers.json", spec)
			}
		}

		// The accept side, pinned next to the refusal: a benign id and family are
		// accepted and persist verbatim (MaskSlot is the identity on both).
		code, out, errw := runSettings(t,
			[]string{"providers", "model-add-json", "acme", "-", "--yes"},
			`{"id":"acme-third","family":"llama"}`)
		if code != 0 {
			t.Fatalf("benign spec exit = %d stderr=%s", code, errw)
		}
		if !strings.Contains(out, "+ id: acme-third") {
			t.Errorf("diff must name the benign id: stdout=%s", out)
		}
		added := acmeTestModels(t, reloadTestRegistry(t, path))[2]
		if added.ID != "acme-third" || added.Family != "llama" {
			t.Errorf("persisted record = %+v; want id acme-third and family llama", added)
		}
	})

	// The pre-existing accept set is unchanged by the union for a
	// separator-carrying model id: '-' '.' '/' break the opaque run, no
	// contiguous run reaches the floor, and MaskSlot is the identity.
	for _, id := range []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo"} {
		if modelSpecValueLooksSecret(id) {
			t.Errorf("benign id %q is classified as secret material", id)
		}
	}
}

// TestRunSettingsCmd_ModelVerbs_UpdateRefusesStalePresentation pins G-C3 on
// both consent paths: the CLI presents a diff read from disk, then waits for
// consent (or returns instantly under --yes), then Core.ModelUpdate re-loads
// and replaces the whole record. An external edit landing in that window must
// refuse the write, not be clobbered.
//
// Console path: the reader performs the external edit on its first Read, i.e.
// exactly while the prompt is waiting, so the interleaving is a real one and no
// seam is involved.
func TestRunSettingsCmd_ModelVerbs_UpdateRefusesStalePresentation_Console(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	stubStdinIsTTY(t, true)
	specPath := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(specPath, []byte(`{"id":"acme-slow","contextLength":256000}`), 0o600); err != nil {
		t.Fatal(err)
	}

	in := &mutateOnRead{
		answer: "y\n",
		mutate: func() {
			reg := reloadTestRegistry(t, path)
			reg.Path = path
			reg.Providers[0].Models[1].ContextLength = 777
			if err := saveTestRegistry(reg); err != nil {
				t.Fatalf("external edit: %v", err)
			}
		},
	}
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	code := runSettingsCmd([]string{"providers", "model-update", "acme", "acme-slow", specPath}, in, out, errw)
	if code != 1 {
		t.Fatalf("stale update exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
	}
	if !strings.Contains(errw.String(), "changed on disk between the presentation and the write") {
		t.Errorf("refusal must name the stale presentation: stderr=%s", errw)
	}
	if !strings.Contains(errw.String(), "nothing written") {
		t.Errorf("refusal must say nothing was written: stderr=%s", errw)
	}
	// The presentation still showed the pre-edit record, so the operator saw the
	// diff that was refused rather than a silent no-op.
	if !strings.Contains(out.String(), "  contextLength: 0 -> 256000") {
		t.Errorf("presentation must show the pre-edit before column: stdout=%s", out)
	}
	// Nothing was clobbered: the external edit survives, the requested value
	// never landed.
	got := acmeTestModels(t, reloadTestRegistry(t, path))[1]
	if got.ContextLength != 777 {
		t.Errorf("on-disk contextLength = %d; want the external edit (777) preserved", got.ContextLength)
	}
	if got.MaxOutputTokens != 0 {
		t.Errorf("on-disk maxOutputTokens = %d; the refused write landed", got.MaxOutputTokens)
	}
}

// TestRunSettingsCmd_ModelVerbs_UpdateRefusesStalePresentation_Yes pins the
// same refusal on the --yes path, where there is no prompt to hang the
// interleaving on: the write-time re-read seam performs the external edit, so
// the check runs and refuses before Core is reached.
func TestRunSettingsCmd_ModelVerbs_UpdateRefusesStalePresentation_Yes(t *testing.T) {
	path := installFileBackedSettingsCore(t, twoModelRegistry())
	updateArgs := []string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"}
	const spec = `{"id":"acme-slow","contextLength":256000}`

	// staleEdit runs one --yes update whose write-time re-read applies edit to
	// the on-disk provider first.
	staleEdit := func(edit func(*providers.Provider)) (int, string, string, int) {
		prev := registryReload
		calls := 0
		registryReload = func() (*providers.Registry, error) {
			calls++
			reg := reloadTestRegistry(t, path)
			reg.Path = path
			edit(&reg.Providers[0])
			if err := saveTestRegistry(reg); err != nil {
				t.Fatalf("external edit: %v", err)
			}
			return providers.Load()
		}
		defer func() { registryReload = prev }()
		code, out, errw := runSettings(t, updateArgs, spec)
		return code, out, errw, calls
	}

	// (a) a signed scalar drifts (contextLength).
	code, out, errw, calls := staleEdit(func(p *providers.Provider) { p.Models[1].ContextLength = 777 })
	if code != 1 {
		t.Fatalf("stale --yes update exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
	}
	if calls != 1 {
		t.Errorf("write-time re-read ran %d times; want 1", calls)
	}
	if !strings.Contains(errw, "changed on disk between the presentation and the write") {
		t.Errorf("refusal must name the stale presentation: stderr=%s", errw)
	}
	if got := acmeTestModels(t, reloadTestRegistry(t, path))[1].ContextLength; got != 777 {
		t.Errorf("on-disk contextLength = %d; want the external edit (777) preserved", got)
	}

	// (b) a signed capability flag drifts (supportsTools, the flag the CLI diff
	// signs as its own line). Also proves the refused write from (a) left the
	// other fields alone.
	code, out, errw, _ = staleEdit(func(p *providers.Provider) { p.Models[1].Capabilities.SupportsTools = true })
	if code != 1 {
		t.Fatalf("stale supportsTools update exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
	}
	if !strings.Contains(errw, "changed on disk between the presentation and the write") {
		t.Errorf("supportsTools drift must refuse too: stderr=%s", errw)
	}
	stale := acmeTestModels(t, reloadTestRegistry(t, path))[1]
	if !stale.Capabilities.SupportsTools {
		t.Error("refused update clobbered the edited supportsTools flag")
	}
	if stale.ContextLength != 777 {
		t.Errorf("on-disk contextLength = %d; the refused write clobbered it", stale.ContextLength)
	}

	// (c) the normal --yes path still writes: the re-verify is not a blanket
	// refusal. The seam now reports the unchanged registry.
	code, out, errw = runSettings(t, updateArgs, spec)
	if code != 0 {
		t.Fatalf("consented update exit = %d stderr=%s", code, errw)
	}
	if got := acmeTestModels(t, reloadTestRegistry(t, path))[1].ContextLength; got != 256000 {
		t.Errorf("consented update contextLength = %d; want 256000", got)
	}
}

// mutateOnRead performs the external edit on its first Read and then serves the
// scripted console answer: the interleaving an operator's editor performs while
// the consent prompt waits for input.
type mutateOnRead struct {
	mutate func()
	answer string
	done   bool
}

func (r *mutateOnRead) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.mutate()
	}
	if r.answer == "" {
		return 0, io.EOF
	}
	n := copy(p, r.answer)
	r.answer = r.answer[n:]
	return n, nil
}

// TestReasoningEffortAllowedListMatchesCanonicalSet pins G-C5: the out-of-set
// refusal message renders its allowed list from openaiup.ValidReasoningEfforts
// (sorted, deterministic), so a set change moves the message and a
// hand-maintained stale list cannot come back.
func TestReasoningEffortAllowedListMatchesCanonicalSet(t *testing.T) {
	const want = "default, high, low, max, medium, none, xhigh"
	got := reasoningEffortAllowedList()
	if got != want {
		t.Fatalf("allowed list = %q; want %q (canonical set, sorted)", got, want)
	}
	parts := strings.Split(got, ", ")
	if len(parts) != len(openaiup.ValidReasoningEfforts) {
		t.Errorf("allowed list has %d entries; want %d", len(parts), len(openaiup.ValidReasoningEfforts))
	}
	for effort := range openaiup.ValidReasoningEfforts {
		if !strings.Contains(got, effort) {
			t.Errorf("allowed list %q is missing canonical effort %q", got, effort)
		}
	}
	for i := 1; i < len(parts); i++ {
		if parts[i-1] >= parts[i] {
			t.Errorf("allowed list is not sorted: %q", got)
		}
	}

	// And the refusal sentence actually carries it.
	installFileBackedSettingsCore(t, twoModelRegistry())
	_, _, errw := runSettings(t,
		[]string{"providers", "model-add-json", "acme", "-", "--yes"},
		`{"id":"acme-effort","capabilities":{"reasoningEffort":"extreme"}}`)
	if !strings.Contains(errw, "(allowed: "+want+"; value hidden)") {
		t.Errorf("refusal does not carry the derived allowed list: stderr=%s", errw)
	}
}

// mustModelByID returns the named model from a reloaded registry, failing the
// test when the provider or the model is missing.
func mustModelByID(t *testing.T, reg *providers.Registry, provider, modelID string) providers.Model {
	t.Helper()
	p, ok := reg.Provider(provider)
	if !ok {
		t.Fatalf("provider %q missing from the reloaded registry", provider)
	}
	for _, m := range p.Models {
		if m.ID == modelID {
			return m
		}
	}
	t.Fatalf("model %q missing from provider %q after reload", modelID, provider)
	return providers.Model{}
}

// TestRunSettingsCmd_ModelVerbs_ChangedDiffEmitsOnRawDifference pins H-C1: the
// changed builder decides emission on the RAW operands and renders the masked
// forms. The previous rule compared the rendered columns, so a marker-only or
// case-only difference printed "(no field changes)" while Core.ModelUpdate still
// replaced the record: a positive claim the write falsifies.
//
// FIX-7E narrowed case (c): the pair of credential-shaped families that folded
// to one first-5+**** mask is unreachable now (the ingest door refuses every
// value the slot masker rewrites), so the scalar arm pins the marker-only class,
// which is the fold-invisible difference that still reaches the write.
func TestRunSettingsCmd_ModelVerbs_ChangedDiffEmitsOnRawDifference(t *testing.T) {
	// (a) Marker-only difference on a list slot: both columns render "fast"
	// because the marker is stripped for display, yet the bytes differ.
	assertDiffLines(t, modelChangedDiffLines(
		providers.Model{ID: "m1", Tags: []string{"fast"}},
		providers.Model{ID: "m1", Tags: []string{"./fast"}},
	), []string{"  tags: fast -> fast"})

	// (b) Case-only difference on a scalar slot.
	assertDiffLines(t, modelChangedDiffLines(
		providers.Model{ID: "m1", Family: "Acme"},
		providers.Model{ID: "m1", Family: "acme"},
	), []string{"  family: Acme -> acme"})

	// (c) Marker-only difference on a SCALAR slot: the before value is on disk
	// with a leading location marker, so both columns render the marker-stripped
	// core while the bytes differ. FIX-7E moved this case: it used to pair two
	// credential-shaped families that mask identically, which is unreachable on
	// the accept side now that the ingest door refuses every value the slot
	// masker rewrites (that door and the column renderer are one authority). The
	// fold-invisible class that still reaches the write is a marker on the value
	// already on disk.
	const beforeFamily = "./acme-fork"
	const afterFamily = "acme-fork"
	if regOperand(beforeFamily) != regOperand(afterFamily) {
		t.Fatalf("premise: the two families must render identically (got %q and %q)",
			regOperand(beforeFamily), regOperand(afterFamily))
	}
	if modelSpecValueLooksSecret(afterFamily) {
		t.Fatalf("premise: the after family must pass the ingest classifier")
	}
	assertDiffLines(t, modelChangedDiffLines(
		providers.Model{ID: "acme-slow", Family: beforeFamily},
		providers.Model{ID: "acme-slow", Family: afterFamily},
	), []string{"  family: acme-fork -> acme-fork"})

	// (d) End to end: the line prints and the on-disk bytes really move.
	reg := twoModelRegistry()
	reg.Providers[0].Models[1].Family = beforeFamily
	path := installFileBackedSettingsCore(t, reg)
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","family":"`+afterFamily+`"}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "  family: acme-fork -> acme-fork") {
		t.Errorf("raw-difference change must print its line: stdout=%s", out)
	}
	if strings.Contains(out, "(no field changes)") {
		t.Errorf("raw-difference change must not print the no-changes marker: stdout=%s", out)
	}
	if strings.Contains(out, beforeFamily) {
		t.Errorf("the raw marker-decorated family echoed into the transcript: stdout=%s", out)
	}
	if got := mustModelByID(t, reloadTestRegistry(t, path), "acme", "acme-slow").Family; got != afterFamily {
		t.Errorf("on-disk family = %q; want the spec value (the write moves bytes)", got)
	}

	// (e) A true no-change stays byte-identical: raw equality on every field
	// still prints the byte-stable marker and nothing else.
	same := providers.Model{ID: "acme-slow", Family: "acme-1", Tags: []string{"fast"}, ContextLength: 1000}
	assertDiffLines(t, modelChangedDiffLines(same, same), []string{"(no field changes)"})
}

// TestRunSettingsCmd_ModelVerbs_DecorationClassSpecRejected pins H-C2: the
// ingest classifier folds the format class (Cf) and the unicode look-alike
// separators into its strip fixpoint, so a decoration outside ASCII cannot
// smuggle a credential into a registry slot. Every named rune is covered on the
// classifier directly, then three shapes are driven end to end (refused
// non-echoing, nothing written).
func TestRunSettingsCmd_ModelVerbs_DecorationClassSpecRejected(t *testing.T) {
	const opaque = "ab3def7gh9jk1mnpqr5t"

	// Premise: the bare run is refused, so the decoration is the only variable.
	if !modelSpecValueLooksSecret(opaque) {
		t.Fatalf("premise: the bare opaque run must already be refused")
	}

	decorations := []struct{ name, value string }{
		{"bidi LRE U+202A", "\u202a./" + opaque},
		{"bidi RLE U+202B", "\u202b./" + opaque},
		{"bidi PDF U+202C", "\u202c./" + opaque},
		{"bidi LRO U+202D", "\u202d./" + opaque},
		{"bidi RLO U+202E", "\u202e./" + opaque},
		{"bidi LRI U+2066", "\u2066./" + opaque},
		{"bidi RLI U+2067", "\u2067./" + opaque},
		{"bidi FSI U+2068", "\u2068./" + opaque},
		{"bidi PDI U+2069", "\u2069./" + opaque},
		{"zero-width space U+200B", "\u200b./" + opaque},
		{"zero-width joiner U+200D", "\u200d./" + opaque},
		{"BOM U+FEFF", "\ufeff./" + opaque},
		{"two dot leader U+2025", "\u2025/" + opaque},
		{"horizontal ellipsis U+2026", "\u2026/" + opaque},
		{"one dot leader U+2024", "\u2024/" + opaque},
		{"ideographic full stop U+3002", "\u3002/" + opaque},
		{"halfwidth ideographic full stop U+FF61", "\uff61/" + opaque},
		{"fullwidth solidus U+FF0F", "\uff0f" + opaque},
		{"fullwidth reverse solidus U+FF3C", "\uff3c" + opaque},
		{"fraction slash U+2044", "\u2044" + opaque},
		{"division slash U+2215", "\u2215" + opaque},
		{"interior zero-width space", "ab3def7\u200bgh9jk1mnpqr5t"},
		{"interior fullwidth solidus", "ab3def7\uff0fgh9jk1mnpqr5t"},
		{"interior control rune", "ab3def7\u0001gh9jk1mnpqr5t"},
	}
	for _, tc := range decorations {
		if !modelSpecValueLooksSecret(tc.value) {
			t.Errorf("%s defeats the classifier: %q", tc.name, tc.value)
		}
	}

	path := installFileBackedSettingsCore(t, twoModelRegistry())
	cases := []struct{ name, spec string }{
		{"Cf-decorated family", fmt.Sprintf(`{"id":"m1","family":%q}`, "\u202e./"+opaque)},
		{"look-alike-decorated tag", fmt.Sprintf(`{"id":"m9","tags":[%q]}`, "\u2025/"+opaque)},
		{"interior zero-width id", fmt.Sprintf(`{"id":%q}`, "ab3def7\u200bgh9jk1mnpqr5t")},
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
				t.Errorf("stderr missing the non-echoing refusal: %s", errw)
			}
			if strings.Contains(errw, opaque) {
				t.Errorf("stderr echoes the credential core raw: %s", errw)
			}
			if !bytes.Equal(diskBytes(t, path), before) {
				t.Error("rejected decoration-class spec wrote providers.json")
			}
		})
	}

	// The decoration is classification-only: a benign value carrying one is
	// still accepted, and the stored value is the spec value verbatim.
	t.Run("benign decoration-carrying values stay accepted", func(t *testing.T) {
		path := installFileBackedSettingsCore(t, twoModelRegistry())
		code, _, errw := runSettings(t,
			[]string{"providers", "model-add-json", "acme", "-", "--yes"},
			fmt.Sprintf(`{"id":"acme-deco","family":%q,"tags":[%q]}`, "\u2026acme", "\u2025notes"))
		if code != 0 {
			t.Fatalf("benign decoration-carrying spec exit = %d stderr=%s", code, errw)
		}
		added := mustModelByID(t, reloadTestRegistry(t, path), "acme", "acme-deco")
		if added.Family != "\u2026acme" {
			t.Errorf("persisted family = %q; want the spec value verbatim", added.Family)
		}
		if len(added.Tags) != 1 || added.Tags[0] != "\u2025notes" {
			t.Errorf("persisted tags = %q; want the spec value verbatim", added.Tags)
		}
	})
}

// TestRunSettingsCmd_ModelVerbs_CreatedDuringConsentRefused pins H-C3: when the
// presentation was after-only (no before record), the signed state is the
// model's ABSENCE. A model created by an external edit while the consent window
// was open must refuse the write, because Core.ModelUpdate would find the new
// record and replace it under a consent rendered before it existed.
func TestRunSettingsCmd_ModelVerbs_CreatedDuringConsentRefused(t *testing.T) {
	const specJSON = `{"id":"acme-new","contextLength":256000}`

	// createDuring tunes an on-disk registry to carry the model the presentation
	// did not see.
	createDuring := func(t *testing.T, path string) {
		t.Helper()
		reg := reloadTestRegistry(t, path)
		reg.Path = path
		p, ok := reg.Provider("acme")
		if !ok {
			t.Fatal("provider acme missing from the registry")
		}
		p.Models = append(p.Models, providers.Model{ID: "acme-new", ContextLength: 7})
		if err := saveTestRegistry(reg); err != nil {
			t.Fatalf("external creation: %v", err)
		}
	}

	// Console path: the creation happens on the reader's first Read, i.e.
	// exactly while the consent prompt waits, so the interleaving is a real one
	// and no seam is involved. On the unfixed code the write proceeds and
	// replaces the just-created record.
	t.Run("console", func(t *testing.T) {
		path := installFileBackedSettingsCore(t, twoModelRegistry())
		stubStdinIsTTY(t, true)
		specPath := filepath.Join(t.TempDir(), "model.json")
		if err := os.WriteFile(specPath, []byte(specJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		in := &mutateOnRead{answer: "y\n", mutate: func() { createDuring(t, path) }}
		out, errw := &bytes.Buffer{}, &bytes.Buffer{}
		code := runSettingsCmd([]string{"providers", "model-update", "acme", "acme-new", specPath}, in, out, errw)
		if code != 1 {
			t.Fatalf("created-during-consent update exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
		}
		if !strings.Contains(errw.String(), "changed on disk between the presentation and the write") {
			t.Errorf("refusal must name the stale presentation: stderr=%s", errw)
		}
		if !strings.Contains(errw.String(), "nothing written") {
			t.Errorf("refusal must say nothing was written: stderr=%s", errw)
		}
		// The presentation was after-only: the operator saw the record as a new one.
		if !strings.Contains(out.String(), "+ id: acme-new") {
			t.Errorf("after-only presentation must render the added lines: stdout=%s", out)
		}
		// The created record survives untouched.
		if got := mustModelByID(t, reloadTestRegistry(t, path), "acme", "acme-new").ContextLength; got != 7 {
			t.Errorf("created record contextLength = %d; want the created value (7) preserved", got)
		}
	})

	// --yes path: the write-time re-read seam performs the creation, so the same
	// refusal is reachable without a console (the --yes path never skips the
	// check).
	t.Run("yes", func(t *testing.T) {
		path := installFileBackedSettingsCore(t, twoModelRegistry())
		updateArgs := []string{"providers", "model-update", "acme", "acme-new", "-", "--yes"}

		prev := registryReload
		defer func() { registryReload = prev }()
		calls := 0
		registryReload = func() (*providers.Registry, error) {
			calls++
			createDuring(t, path)
			return providers.Load()
		}

		code, out, errw := runSettings(t, updateArgs, specJSON)
		if code != 1 {
			t.Fatalf("created-during-consent update exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
		}
		if calls != 1 {
			t.Errorf("write-time re-read ran %d times; want 1", calls)
		}
		if !strings.Contains(errw, "changed on disk between the presentation and the write") {
			t.Errorf("refusal must name the stale presentation: stderr=%s", errw)
		}
		if got := mustModelByID(t, reloadTestRegistry(t, path), "acme", "acme-new").ContextLength; got != 7 {
			t.Errorf("created record contextLength = %d; want the created value (7) preserved", got)
		}
	})

	// Control: a still-absent model is not refused as stale. The absence check
	// ran and passed, so Core's own not-found refusal is what the operator sees
	// (the check is not a blanket refusal).
	t.Run("still absent", func(t *testing.T) {
		installFileBackedSettingsCore(t, twoModelRegistry())
		code, _, errw := runSettings(t,
			[]string{"providers", "model-update", "acme", "acme-new", "-", "--yes"}, specJSON)
		if code != 1 {
			t.Fatalf("still-absent update exit = %d; want 1 (stderr=%s)", code, errw)
		}
		if strings.Contains(errw, "changed on disk between the presentation and the write") {
			t.Errorf("a still-absent model must not be refused as a stale presentation: stderr=%s", errw)
		}
		if !strings.Contains(errw, `model "acme-new" not found`) {
			t.Errorf("want Core's not-found refusal on the pass-through path: stderr=%s", errw)
		}
	})
}

// TestRunSettingsCmd_ModelVerbs_ReverifyFailClosed pins H-C4: each fail-closed
// branch of reverifyPresentedModel (unreadable registry, vanished provider,
// vanished model) is exercised with a presentation that really carried a before
// record. Replacing any refusal with a silent return lets the write land, so
// the disk-byte assertion fails.
func TestRunSettingsCmd_ModelVerbs_ReverifyFailClosed(t *testing.T) {
	updateArgs := []string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"}
	const spec = `{"id":"acme-slow","contextLength":256000}`

	cases := []struct {
		name   string
		reload func(t *testing.T, path string) (*providers.Registry, error)
		want   string
	}{
		{
			name: "unreadable registry",
			reload: func(t *testing.T, path string) (*providers.Registry, error) {
				return nil, errors.New("simulated read failure")
			},
			want: "registry re-read failed before the write",
		},
		{
			name: "nil registry",
			reload: func(t *testing.T, path string) (*providers.Registry, error) {
				return nil, nil
			},
			want: "registry re-read failed before the write",
		},
		{
			name: "vanished provider",
			reload: func(t *testing.T, path string) (*providers.Registry, error) {
				return &providers.Registry{Providers: []providers.Provider{
					{Name: "other", BaseURL: "https://other.example/v1", APIKeyEnv: "OTHER_KEY"},
				}}, nil
			},
			want: "provider acme disappeared from providers.json",
		},
		{
			name: "vanished model",
			reload: func(t *testing.T, path string) (*providers.Registry, error) {
				reg := reloadTestRegistry(t, path)
				reg.Providers[0].Models = reg.Providers[0].Models[:1]
				return reg, nil
			},
			want: "model acme-slow disappeared from providers.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := installFileBackedSettingsCore(t, twoModelRegistry())
			before := diskBytes(t, path)

			prev := registryReload
			registryReload = func() (*providers.Registry, error) { return tc.reload(t, path) }
			defer func() { registryReload = prev }()

			code, out, errw := runSettings(t, updateArgs, spec)
			if code != 1 {
				t.Fatalf("exit = %d; want 1 (stdout=%s stderr=%s)", code, out, errw)
			}
			if !strings.Contains(errw, tc.want) {
				t.Errorf("refusal = %q; want it to contain %q", errw, tc.want)
			}
			if !strings.Contains(errw, "nothing written") {
				t.Errorf("refusal must say nothing was written: stderr=%s", errw)
			}
			// The presentation was presentable: the operator saw the before column.
			if !strings.Contains(out, "  contextLength: 0 -> 256000") {
				t.Errorf("presentation must show the before column: stdout=%s", out)
			}
			if !bytes.Equal(diskBytes(t, path), before) {
				t.Error("a refused re-verify wrote providers.json")
			}
		})
	}
}

// TestRunSettingsCmd_ModelVerbs_RegistrySlotSentencesMasked pins H-C6 (and the
// H-C6 sentence class): the mutation headlines and the model-delete "- model"
// line render registry-slot values through the registry-slot masker, not
// through the location-exempt MaskOperand. The add/update header IDs are
// spec-borne and ingest-gated, so the decorated shape is pinned at the renderer
// (the same seam); the delete line is pinned end to end from a hand-edited
// record, which is the reachable disk-borne path.
func TestRunSettingsCmd_ModelVerbs_RegistrySlotSentencesMasked(t *testing.T) {
	const plant = "./ab3def7gh9jk1mnpqr5t"
	const core = "ab3def7gh9jk1mnpqr5t"
	const masked = "ab3de****"

	reg := twoModelRegistry()
	reg.Providers[0].Models[1].ID = plant
	path := installFileBackedSettingsCore(t, reg)

	// Headlines: the model ID and the provider name are registry slots.
	addHeader := modelAddDiff("acme", providers.Model{ID: plant})[0]
	if !strings.Contains(addHeader, "Adds model "+masked+" to provider acme.") {
		t.Errorf("add header must mask the model ID: %q", addHeader)
	}
	if strings.Contains(addHeader, core) {
		t.Errorf("add header echoes the decorated ID raw: %q", addHeader)
	}
	addHeader = modelAddDiff(plant, providers.Model{ID: "m1"})[0]
	if !strings.Contains(addHeader, "Adds model m1 to provider "+masked+".") {
		t.Errorf("add header must mask the provider name: %q", addHeader)
	}
	updHeader := modelUpdateDiff(presentedModel{provider: "acme"}, providers.Model{ID: plant})[0]
	if !strings.Contains(updHeader, "Updates model "+masked+" in provider acme.") {
		t.Errorf("update header must mask the model ID: %q", updHeader)
	}
	delLines := modelDeleteDiff("acme", plant)
	if !strings.Contains(delLines[0], "Removes model "+masked+" from provider acme.") {
		t.Errorf("delete header must mask the model ID: %q", delLines[0])
	}
	if got := delLines[len(delLines)-1]; got != "- model "+masked {
		t.Errorf("delete line = %q; want %q", got, "- model "+masked)
	}
	// A Cf rune in front of the marker is stripped by the same renderer (the
	// stripDiffControls arm), so the display side of that decoration class is
	// closed too.
	if got := modelDeleteDiff("acme", "\u202e"+plant); got[len(got)-1] != "- model "+masked {
		t.Errorf("Cf-decorated delete line = %q; want %q", got[len(got)-1], "- model "+masked)
	}

	// Benign sentences stay byte-identical.
	if got := modelAddDiff("acme", providers.Model{ID: "acme-third"})[0]; got != "Adds model acme-third to provider acme. Key values are untouched." {
		t.Errorf("benign add header = %q", got)
	}
	if got := modelUpdateDiff(presentedModel{provider: "acme"}, providers.Model{ID: "acme-slow"})[0]; got != "Updates model acme-slow in provider acme. Key values are untouched." {
		t.Errorf("benign update header = %q", got)
	}
	benignDel := modelDeleteDiff("acme", "acme-slow")
	if benignDel[0] != "Removes model acme-slow from provider acme. Key values are untouched." || benignDel[len(benignDel)-1] != "- model acme-slow" {
		t.Errorf("benign delete transcript moved: %q", benignDel)
	}

	// End to end: the hand-edited record carries the plant as its model ID; the
	// whole delete transcript masks every slot and the removal really happens.
	code, out, errw := runSettings(t,
		[]string{"providers", "model-delete", "acme", plant, "--yes"}, "")
	if code != 0 {
		t.Fatalf("model-delete exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"Removes model " + masked + " from provider acme. Key values are untouched.",
		"- model " + masked,
		"[SUCCESS] provider acme: model " + masked + " removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete transcript missing %q: stdout=%s", want, out)
		}
	}
	if strings.Contains(out, core) {
		t.Errorf("delete transcript echoes the decorated ID raw: stdout=%s", out)
	}
	if models := acmeTestModels(t, reloadTestRegistry(t, path)); len(models) != 1 || models[0].ID != "acme-fast" {
		t.Errorf("models after delete = %+v; want only acme-fast", models)
	}
}

// TestRunSettingsCmd_ModelVerbs_LookAlikeSlotMasked pins the closed F-1 class:
// a look-alike-decorated plant (U+2025 TWO DOT LEADER + "/" + a lowercase 20-rune
// core) sits in a hand-edited record and must render MASKED at every CLI
// registry-slot sink: the update before column, the delete headline, the
// "- model" line, the success line, and a re-verify refusal. Before the policy
// round regOperand was the bare authority, whose ASCII marker set cannot see
// U+2025, so the plant printed raw in the CLI transcript while the TUI masked
// the same value; the two twins disagreed on the class both ingest doors already
// refused. The shared policy's identity-fallback closes it on this side, and the
// TUI pins the same plant.
func TestRunSettingsCmd_ModelVerbs_LookAlikeSlotMasked(t *testing.T) {
	const core = "ab3def7gh9jk1mnpqr5t"
	const plant = "\u2025/" + core
	const masked = "ab3de****"

	// The plant is refused at both ingest doors (premise), so only a hand-edited
	// record can carry it: nothing on the write path persisted it.
	if !modelSpecValueLooksSecret(plant) {
		t.Fatalf("premise: the ingest guard must refuse the look-alike plant %q", plant)
	}

	// 1. Update before column: the on-disk family is the plant, the spec moves it
	// to a benign value, so the family line renders the planted BEFORE operand.
	reg := twoModelRegistry()
	reg.Providers[0].Models[1].Family = plant
	path := installFileBackedSettingsCore(t, reg)
	code, out, errw := runSettings(t,
		[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
		`{"id":"acme-slow","family":"acme-1"}`)
	if code != 0 {
		t.Fatalf("model-update exit = %d stderr=%s", code, errw)
	}
	if !strings.Contains(out, "  family: "+masked+" -> acme-1") {
		t.Errorf("update before column must mask the look-alike plant:\n%s", out)
	}
	if strings.Contains(out, core) || strings.Contains(out, plant) {
		t.Errorf("update transcript echoes the decorated plant raw:\n%s", out)
	}
	if got := mustModelByID(t, reloadTestRegistry(t, path), "acme", "acme-slow").Family; got != "acme-1" {
		t.Errorf("on-disk family = %q; want the spec value (the write moves bytes)", got)
	}

	// 2. Delete headline, "- model" line, and the success line: the record's ID
	// is the plant (a hand-edited record; the delete verb takes it as an operand
	// and every rendered slot must mask it).
	reg = twoModelRegistry()
	reg.Providers[0].Models[1].ID = plant
	path = installFileBackedSettingsCore(t, reg)
	code, out, errw = runSettings(t,
		[]string{"providers", "model-delete", "acme", plant, "--yes"}, "")
	if code != 0 {
		t.Fatalf("model-delete exit = %d stderr=%s", code, errw)
	}
	for _, want := range []string{
		"Removes model " + masked + " from provider acme. Key values are untouched.",
		"- model " + masked,
		"[SUCCESS] provider acme: model " + masked + " removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("delete transcript missing %q: stdout=%s", want, out)
		}
	}
	if strings.Contains(out, core) || strings.Contains(out, plant) {
		t.Errorf("delete transcript echoes the decorated plant raw: stdout=%s", out)
	}

	// 3. Re-verify refusal: the sentence's operand is the presented (disk-borne)
	// model id, so it masks through the same authority.
	reg = twoModelRegistry()
	reg.Providers[0].Models[1].ID = plant
	installFileBackedSettingsCore(t, reg)
	presented := snapshotRegistryModel("acme", plant)
	if !presented.presentable() {
		t.Fatalf("test premise broken: the presentation carried no before record")
	}
	prev := registryReload
	registryReload = func() (*providers.Registry, error) {
		reload := twoModelRegistry() // the plant is gone: an external edit landed
		return reload, nil
	}
	t.Cleanup(func() { registryReload = prev })
	err := reverifyPresentedModel(presented)
	if err == nil {
		t.Fatal("the re-verify must refuse a model that disappeared")
	}
	if !strings.Contains(err.Error(), "model "+masked+" disappeared") {
		t.Errorf("re-verify refusal = %q; want the masked model id", err)
	}
	if strings.Contains(err.Error(), core) || strings.Contains(err.Error(), plant) {
		t.Errorf("re-verify refusal echoes the decorated plant raw: %q", err)
	}
}

// TestRunSettingsCmd_ModelVerbs_NameHeadSpecRejected pins FIX-7E end to end:
// the five underscore-head key probes (the class SecretShaped alone misses and
// providers.NameCarriesCredential catches) are refused as model fields on both
// model verbs, with the established masked sentence, no echoed value, and
// providers.json byte-identical. The classifier is shared with the TUI model
// form, so this door can no longer persist a value its own consent transcript
// renders as "sk_li****".
func TestRunSettingsCmd_ModelVerbs_NameHeadSpecRejected(t *testing.T) {
	probes := []string{
		"sk_live_0123456789abcdef",
		"tok_a1b2c3d4e5f6g7h8i9j0",
		"pk_live_0123456789abcdef",
		"key_0123456789abcdef",
		"AKIA_0123456789ABCDEF",
	}

	// Every free-text field of the spec routes through the same predicate. A
	// slice (not a map) keeps the subtest order deterministic.
	specs := func(p string) [][2]string {
		return [][2]string{
			{"id", `{"id":"` + p + `"}`},
			{"family", `{"id":"m9","family":"` + p + `"}`},
			{"tag", `{"id":"m9","tags":["` + p + `"]}`},
			{"tool grant", `{"id":"m9","capabilities":{"allowedTools":["read","` + p + `"]}}`},
			{"reasoningEffort", `{"id":"m9","capabilities":{"reasoningEffort":"` + p + `"}}`},
		}
	}

	for _, probe := range probes {
		probe := probe
		for _, fieldSpec := range specs(probe) {
			field, spec := fieldSpec[0], fieldSpec[1]
			t.Run(field+"/"+probe, func(t *testing.T) {
				path := installFileBackedSettingsCore(t, twoModelRegistry())
				before := diskBytes(t, path)
				code, _, errw := runSettings(t,
					[]string{"providers", "model-add-json", "acme", "-", "--yes"}, spec)
				if code != 1 {
					t.Fatalf("exit = %d, want 1 (stderr=%s)", code, errw)
				}
				if !strings.Contains(errw, "secret material (value hidden)") {
					t.Errorf("stderr missing the masked refusal: %s", errw)
				}
				if strings.Contains(errw, probe) {
					t.Errorf("stderr echoes the value raw: %s", errw)
				}
				if !bytes.Equal(diskBytes(t, path), before) {
					t.Error("a refused spec wrote providers.json")
				}
			})
		}
	}

	// model-update shares the classifier, so the same probe is refused there. The
	// refusal lands before the consent prompt and before the re-verify, so the
	// on-disk record is untouched.
	t.Run("model-update shares the door", func(t *testing.T) {
		path := installFileBackedSettingsCore(t, twoModelRegistry())
		before := diskBytes(t, path)
		code, out, errw := runSettings(t,
			[]string{"providers", "model-update", "acme", "acme-slow", "-", "--yes"},
			`{"id":"acme-slow","family":"sk_live_0123456789abcdef"}`)
		if code != 1 {
			t.Fatalf("model-update exit = %d, want 1 (stderr=%s)", code, errw)
		}
		if !strings.Contains(errw, "secret material (value hidden)") {
			t.Errorf("stderr missing the masked refusal: %s", errw)
		}
		if strings.Contains(out, "Approve Registry Write") {
			t.Errorf("a refused spec must not reach the consent presentation: %s", out)
		}
		if strings.Contains(errw, "sk_live_0123456789abcdef") {
			t.Errorf("stderr echoes the value raw: %s", errw)
		}
		if !bytes.Equal(diskBytes(t, path), before) {
			t.Error("a refused update wrote providers.json")
		}
	})
}

// TestRunSettingsCmd_ModelVerbs_NameDoorParityWithModelForm is the twin-parity
// pin for the model-field door (FIX-7E): one corpus is driven through the CLI
// model verbs' classifier (modelSpecValueLooksSecret) and through the predicate
// the TUI model form's looksSecretValue delegates to
// (providers.NameCarriesCredential), and the two must return the same verdict on
// every row, in both directions.
//
// The TUI side of the equality is pinned, not assumed: the form's guard is a
// one-statement delegation to that predicate, and TestSlotPolicySingleImplementation
// in internal/providers fails if it ever becomes anything else. The shared rows
// are the exact corpus the form pins in
// TestModelFormIDAuthorityAddsOnlyCredentialClass, and the containment check
// below fails if a row disappears from that pin, so the two corpora cannot drift
// apart silently.
//
// Each row also asserts the display-agreement rule the form pins: the verdict is
// "refused" exactly when the slot authority would re-render the value, so the CLI
// transcript can never show different bytes than the record it wrote.
func TestRunSettingsCmd_ModelVerbs_NameDoorParityWithModelForm(t *testing.T) {
	type row struct {
		id     string
		refuse bool
	}

	// Shared corpus: mirrored from the TUI model-form pin (same ids, same after
	// verdicts).
	shared := []row{
		// Realistic model IDs: the accept set both doors protect.
		{"anthropic/claude-3.5-sonnet", false},
		{"meta-llama/Llama-3.3-70B-Instruct-Turbo", false},
		{"Qwen/Qwen2.5-72B-Instruct-Turbo", false},
		{"meta-llama/llama-3.3-70b-instruct", false},
		{"claude-3-5-sonnet-20241022", false},
		{"acme-fast", false},
		{"gpt-4.1-mini", false},
		{"deepseek-v4-flash", false},
		{"qwen2.5-72b-instruct", false},
		{"glm-5.3-flash", false},
		{"gemini-3-pro-high", false},
		{"gemini-3.8-flash-high", false},
		{"claude-opus-5-5-high", false},
		{"claude-sonnet-5-5-high", false},
		{"kimi-k2-thinking", false},
		{"gpt-5-codex", false},
		{"o4-mini", false},
		{"mistral-large-latest", false},
		{"llama-4-scout-17b-16e-instruct", false},
		{"gemma-3-27b-it", false},
		{"phi-4-reasoning", false},
		{"command-r-plus-08-2025", false},
		{"nvidia/llama-3.3-nemotron-super-49b-v1", false},
		{"x-ai/grok-4-fast", false},
		{"moonshotai/kimi-k2-0905-preview", false},
		{"zhipuai/glm-5.3", false},
		{"qwen3-coder-480b-a35b", false},
		{"deepseek-r1-distill-qwen-32b", false},
		{"internlm3-8b-instruct", false},
		{"acme-large", false},
		{"us.anthropic.claude-3-5-sonnet-20241022-v2:0", false},
		{"models/gemini-2.5-pro", false},
		{"text-embedding-3-large", false},
		{"whisper-1", false},
		{"tts-1", false},
		{"gpt-4o-audio-preview", false},
		// The underscore-head key class (this unit's target).
		{"sk_live_0123456789abcdef", true},
		{"tok_a1b2c3d4e5f6g7h8i9j0", true},
		{"pk_live_0123456789abcdef", true},
		{"key_0123456789abcdef", true},
		{"AKIA_0123456789ABCDEF", true},
		// 16+ rune single-class values.
		{"abcdefghijklmnopqrstuvwxyz", true},
		{"1234567890123456", true},
		// Locations: the authority's documented rewrite arm, a held product
		// decision (whether a model field may legally be a path or a URL).
		{`C:\models\gemini-pro`, true},
		{"./models/gemini-pro", true},
		{"https://example.com/v1", true},
		// Pre-existing refusals: unchanged.
		{"AKIAIOSFODNN7EXAMPLE", true},
		{"AbCdEfGh0123456jKlMnoP", true},
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", true},
		{"Ab3dEf7Gh9jK1mNp+/Qr=", true},
		{"sk-abc", true},
		{"gsk_abcdefghijklm", true},
		{"dckr_pat-abcdef0123456789", true},
		{"bearer abc", true},
		{"eyJhbGciOiJIUzI1NiIsInR5cCI6kpXVCJ9", true},
		{"ghp_abcdefghij0123456789", true},
	}

	// Extra realistic shapes the CLI-side corpus carries (Ollama tags, quant
	// suffixes, a Bedrock ARN, an MLX path, an HF path). They are not rows of the
	// TUI pin, so their verdict is stated here against the same authority; the HF
	// reference is a location-shaped value, which the authority re-renders and
	// therefore refuses, and it is listed as a refusal so the class stays visible
	// instead of being discovered by a user.
	extra := []row{
		{"llama3.2:latest", false},
		{"qwen2.5-coder:32b-instruct-q8_0", false},
		{"mistral:7b-instruct-v0.3-q4_K_M", false},
		{"deepseek-r1:14b-q5_k_m", false},
		{"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-3-5-sonnet-20241022-v2:0", false},
		{"mlx-community/Llama-3.3-70B-Instruct-4bit", false},
		{"hf.co/bartowski/Qwen2.5-72B-Instruct-GGUF", true},
	}

	accepted, refused := 0, 0
	drive := func(rows []row, label string) {
		for _, r := range rows {
			cli := modelSpecValueLooksSecret(r.id)
			tui := providers.NameCarriesCredential(r.id)
			if cli != tui {
				t.Fatalf("%s: door verdicts disagree on %q: CLI model verbs = %v, TUI model form = %v",
					label, r.id, cli, tui)
			}
			if cli != r.refuse {
				t.Errorf("%s: %q verdict = %v, table says %v", label, r.id, cli, r.refuse)
			}
			if got := providers.MaskSlot(r.id) != r.id; got != r.refuse {
				t.Errorf("%s: %q slot authority re-renders = %v, door verdict = %v (display and validation disagree)",
					label, r.id, got, r.refuse)
			}
			if cli {
				refused++
			} else {
				accepted++
			}
		}
	}
	drive(shared, "shared")
	sharedAccepted, sharedRefused := accepted, refused
	drive(extra, "extra")

	// The sets, not just the rows: the shared corpus splits 36 accept / 20 refuse
	// (the TUI pin's split) and the full corpus 42 / 21 on both doors, so a corpus
	// edit that moves a verdict fails here instead of passing silently.
	if sharedAccepted != 36 || sharedRefused != 20 {
		t.Fatalf("shared corpus verdicts = %d accept / %d refuse; want 36 / 20", sharedAccepted, sharedRefused)
	}
	if accepted != 42 || refused != 21 {
		t.Fatalf("full corpus verdicts = %d accept / %d refuse; want 42 / 21", accepted, refused)
	}

	// One corpus: every shared row must still appear in the TUI pin's source.
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "tui", "screen_model_form_test.go"))
	if err != nil {
		t.Fatalf("read the TUI model-form pin source: %v", err)
	}
	for _, r := range shared {
		if !strings.Contains(string(src), r.id) {
			t.Errorf("shared corpus row %q is gone from the TUI model-form pin; the two corpora have diverged", r.id)
		}
	}
}
