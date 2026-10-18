package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round-5 R9/seat3-R3: a registry file carrying C0/C1 control bytes must fail
// at load time. Without this, disk-borne control bytes reach renderers
// outside the TUI frame strip (legacy line-mode settings panel, CLI echoes)
// and execute there as raw terminal bytes. The error names the field class
// and never echoes the offending value (embedding the control byte would
// re-introduce the rendering hazard). All registries here live in t.TempDir();
// the live ~/.agy-pro/providers.json is never touched.

func validControlTestRegistry() Registry {
	return Registry{
		Providers: []Provider{
			{
				Name:      "clean-prov",
				BaseURL:   "https://api.example.com/v1",
				APIKeyEnv: "CLEAN_API_KEY",
				Models: []Model{
					{ID: "clean-model", Family: "cleanfam", Tags: []string{"general"}},
				},
			},
		},
	}
}

func TestValidateRejectsControlRunes(t *testing.T) {
	esc := "\x1b"   // C0 ESC
	c1 := "\u0085"  // C1 NEL
	del := "\u007f" // DEL is a control rune too
	fieldClass := "control characters"

	mutate := map[string]func(*Registry, string){
		"name": func(r *Registry, ctl string) {
			r.Providers[0].Name = "bad" + ctl + "name"
		},
		"baseURL": func(r *Registry, ctl string) {
			r.Providers[0].BaseURL = "https://api.example.com" + ctl + "/v1"
		},
		"apiKeyEnv": func(r *Registry, ctl string) {
			r.Providers[0].APIKeyEnv = "BAD" + ctl + "KEY"
		},
		"model id": func(r *Registry, ctl string) {
			r.Providers[0].Models[0].ID = "m" + ctl + "1"
		},
		"family": func(r *Registry, ctl string) {
			r.Providers[0].Models[0].Family = "fam" + ctl + "x"
		},
		"tag": func(r *Registry, ctl string) {
			r.Providers[0].Models[0].Tags[0] = "tag" + ctl
		},
		// Round-6 F1: the remaining free-text carriers into CLI echoes with no
		// frame strip.
		"reasoningEffort": func(r *Registry, ctl string) {
			r.Providers[0].Models[0].Capabilities.ReasoningEffort = "high" + ctl
		},
		"defaults tools": func(r *Registry, ctl string) {
			r.Defaults.Tools = "read," + ctl + "search"
		},
		"defaults excludeFamilies": func(r *Registry, ctl string) {
			r.Defaults.ExcludeFamilies = []string{"fam" + ctl}
		},
	}

	for field, fn := range mutate {
		for _, ctl := range []string{esc, c1, del} {
			reg := validControlTestRegistry()
			fn(&reg, ctl)
			err := reg.Validate()
			if err == nil {
				t.Errorf("%s with control byte U+%04X: Validate returned nil", field, []rune(ctl)[0])
				continue
			}
			if !strings.Contains(err.Error(), fieldClass) {
				t.Errorf("%s: error %q must name the control-character field class", field, err)
			}
			if strings.Contains(err.Error(), ctl) {
				t.Errorf("%s: error echoes the raw control byte: %q", field, err)
			}
		}
	}
}

func TestValidateAcceptsCleanRegistry(t *testing.T) {
	reg := validControlTestRegistry()
	reg.Providers[0].Models[0].Capabilities.ReasoningEffort = "high"
	reg.Defaults = Defaults{Tools: "read,search", ExcludeFamilies: []string{"embed"}}
	if err := reg.Validate(); err != nil {
		t.Fatalf("clean registry (with reasoningEffort and defaults) must validate: %v", err)
	}
}

// Round-6 seat1-R8: the closed provider-name charset is enforced at load, not
// only at mutation ops and the wizard. A name with a space, a quote, or $ must
// fail Validate/LoadFrom with a non-echoing error, while the live-shaped
// registry (the real ~/.agy-pro/providers.json carries exactly the names
// "nw" and "Charm") still loads.
func TestValidateRejectsBadProviderNameCharset(t *testing.T) {
	for _, bad := range []string{"bad name", `quo"te`, "do$llar", "-lead", strings.Repeat("a", 65)} {
		reg := validControlTestRegistry()
		reg.Providers[0].Name = bad
		err := reg.Validate()
		if err == nil {
			t.Errorf("provider name %q: Validate returned nil; want a charset error", bad)
			continue
		}
		if strings.Contains(err.Error(), bad) {
			t.Errorf("provider name %q: error echoes the operand: %q", bad, err)
		}
	}

	live := validControlTestRegistry()
	live.Providers = append(live.Providers,
		Provider{Name: "nw", BaseURL: "https://api.example.com/v1", Models: []Model{{ID: "m2"}}},
		Provider{Name: "Charm", BaseURL: "https://api.example.com/v1", Models: []Model{{ID: "m3"}}},
		// A CJK provider name is legitimate (the doctor screen pins one
		// loading through the cellwidth renderer); the widened Unicode-letter
		// charset admits it while every shell-dangerous character stays banned.
		Provider{Name: "プロバイダ", BaseURL: "https://api.example.com/v1", Models: []Model{{ID: "m4"}}},
	)
	if err := live.Validate(); err != nil {
		t.Fatalf("live-shaped registry (names nw, Charm) must still validate: %v", err)
	}

	// LoadFrom level, temp dir only: a space-carrying name fails the load
	// closed, a normal file still loads.
	dir := t.TempDir()
	badPath := filepath.Join(dir, "badname.json")
	badJSON := `{"providers":[{"name":"bad name","baseURL":"https://api.example.com/v1","models":[{"id":"m1"}]}]}`
	if err := os.WriteFile(badPath, []byte(badJSON), 0o600); err != nil {
		t.Fatalf("write bad-name registry: %v", err)
	}
	if _, err := LoadFrom(badPath); err == nil {
		t.Fatal("LoadFrom must reject a provider name with a space")
	} else if strings.Contains(err.Error(), "bad name") {
		t.Errorf("LoadFrom error echoes the operand: %q", err)
	}
}

// The load path must reject a registry file carrying control bytes, and keep
// loading a normal one. Temp homes only.
func TestLoadFromControlRunes(t *testing.T) {
	dir := t.TempDir()

	badPath := filepath.Join(dir, "bad.json")
	badJSON := `{"providers":[{"name":"evil` + `\u001b` + `name","baseURL":"https://api.example.com/v1","models":[{"id":"m1"}]}]}`
	if err := os.WriteFile(badPath, []byte(badJSON), 0o600); err != nil {
		t.Fatalf("write bad registry: %v", err)
	}
	_, err := LoadFrom(badPath)
	if err == nil {
		t.Fatal("LoadFrom must reject a registry with control bytes")
	}
	if !strings.Contains(err.Error(), "control characters") {
		t.Errorf("error must name the control-character field class: %v", err)
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Errorf("error must not echo the raw control byte: %q", err)
	}

	goodPath := filepath.Join(dir, "good.json")
	goodJSON := `{"providers":[{"name":"good","baseURL":"https://api.example.com/v1","models":[{"id":"m1"}]}]}`
	if err := os.WriteFile(goodPath, []byte(goodJSON), 0o600); err != nil {
		t.Fatalf("write good registry: %v", err)
	}
	reg, err := LoadFrom(goodPath)
	if err != nil {
		t.Fatalf("normal registry must still load: %v", err)
	}
	if len(reg.Providers) != 1 || reg.Providers[0].Name != "good" {
		t.Fatalf("unexpected loaded registry: %+v", reg.Providers)
	}
}
