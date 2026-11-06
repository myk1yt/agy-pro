package settings

import (
	"path/filepath"
	"reflect"
	"testing"

	"agypro/internal/providers"
)

type runDefaultsWriteVerb func(*Core, *providers.Registry) error

func TestCoreProviderAddSeedsRunDefaults(t *testing.T) {
	verbs := map[string]runDefaultsWriteVerb{
		"ProviderAdd": func(c *Core, _ *providers.Registry) error {
			return c.ProviderAdd(ProviderSpec{Name: "beta", BaseURL: "https://beta.example/v1", APIKeyEnv: "BETA_KEY"})
		},
	}
	cases := []struct {
		name         string
		timeout      int
		maxTurns     int
		wantTimeout  int
		wantMaxTurns int
	}{
		{name: "missing knobs seed", wantTimeout: providers.DefaultRunTimeoutSeconds, wantMaxTurns: providers.DefaultRunMaxTurns},
		{name: "partial seed keeps user timeout", timeout: 900, wantTimeout: 900, wantMaxTurns: providers.DefaultRunMaxTurns},
		{name: "present knobs never move", timeout: 901, maxTurns: 77, wantTimeout: 901, wantMaxTurns: 77},
	}
	for verbName, verb := range verbs {
		for _, tc := range cases {
			t.Run(verbName+"/"+tc.name, func(t *testing.T) {
				core, before, path := runDefaultsFileCore(t, tc.timeout, tc.maxTurns)
				if err := verb(core, before); err != nil {
					t.Fatalf("write: %v", err)
				}
				after, err := providers.LoadFrom(path)
				if err != nil {
					t.Fatalf("reload: %v", err)
				}
				want := before.Defaults
				want.TimeoutSeconds = tc.wantTimeout
				want.MaxTurns = tc.wantMaxTurns
				if !reflect.DeepEqual(after.Defaults, want) {
					t.Fatalf("defaults after write = %+v; want %+v", after.Defaults, want)
				}
			})
		}
	}
}

func TestOtherCoreRegistryWritesDoNotSeedRunDefaults(t *testing.T) {
	verbs := map[string]runDefaultsWriteVerb{
		"ProviderEdit": func(c *Core, _ *providers.Registry) error {
			return c.ProviderEdit("acme", "https://edited.example/v1", "ACME_KEY")
		},
		"ProviderRename": func(c *Core, _ *providers.Registry) error {
			return c.ProviderRename("acme", "beta")
		},
		"ProviderDelete": func(c *Core, _ *providers.Registry) error {
			return c.ProviderDelete("acme")
		},
		"ModelAdd": func(c *Core, _ *providers.Registry) error {
			return c.ModelAdd("acme", providers.Model{ID: "acme-third"})
		},
		"ModelDelete": func(c *Core, _ *providers.Registry) error {
			return c.ModelDelete("acme", "acme-slow")
		},
		"ModelUpdate": func(c *Core, _ *providers.Registry) error {
			return c.ModelUpdate("acme", "acme-fast", providers.Model{ID: "acme-fast", ContextLength: 64000})
		},
		"ModelSetEnabled": func(c *Core, _ *providers.Registry) error {
			return c.ModelSetEnabled("acme", "acme-fast", false)
		},
		"ModelSetTools": func(c *Core, _ *providers.Registry) error {
			return c.ModelSetTools("acme", "acme-fast", []string{"read", "search"})
		},
		"ModelRename": func(c *Core, _ *providers.Registry) error {
			return c.ModelRename("acme", "acme-slow", "acme-legacy")
		},
		"SetExcludeModels": func(c *Core, before *providers.Registry) error {
			return c.SetExcludeModels([]string{"acme"}, ExcludeModelsBaseFromDefaults(before.Defaults))
		},
	}
	for verbName, verb := range verbs {
		t.Run(verbName, func(t *testing.T) {
			core, before, path := runDefaultsFileCore(t, 0, 0)
			if err := verb(core, before); err != nil {
				t.Fatalf("write: %v", err)
			}
			after, err := providers.LoadFrom(path)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			want := before.Defaults
			if verbName == "SetExcludeModels" {
				want.ExcludeModels = []string{"acme"}
			}
			if !reflect.DeepEqual(after.Defaults, want) {
				t.Fatalf("defaults after raw write = %+v; want unchanged %+v", after.Defaults, want)
			}
			if after.Defaults.TimeoutSeconds != 0 || after.Defaults.MaxTurns != 0 {
				t.Fatalf("raw write seeded run defaults: %+v", after.Defaults)
			}
		})
	}
}

func runDefaultsFileCore(t *testing.T, timeout, maxTurns int) (*Core, *providers.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.json")
	t.Setenv(providers.EnvProviders, path)
	before := &providers.Registry{
		Path: path,
		Defaults: providers.Defaults{
			N:               7,
			TimeoutSeconds:  timeout,
			MaxTurns:        maxTurns,
			Tools:           "read,search",
			TokenBudget:     123456,
			ExcludeFamilies: []string{"held-family"},
			ExcludeModels:   []string{"held-model"},
		},
		Providers: []providers.Provider{{
			Name: "acme", BaseURL: "https://acme.example/v1", APIKeyEnv: "ACME_KEY",
			Models: []providers.Model{{ID: "acme-fast"}, {ID: "acme-slow"}},
		}},
	}
	if err := saveRegistryFile(before); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	return NewCore(Deps{LoadReg: providers.Load}), before, path
}
