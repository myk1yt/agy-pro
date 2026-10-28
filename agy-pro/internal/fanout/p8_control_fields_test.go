package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

func TestP8ControlDropCoversEveryEchoedPresetField(t *testing.T) {
	for _, tc := range []struct {
		name     string
		field    string
		fragment string
		preset   Preset
	}{
		{name: "task", field: "task", fragment: "taskplant", preset: Preset{Name: "planted", Task: "review\u2028taskplant"}},
		{name: "search_endpoint", field: "search_endpoint", fragment: "endpointplant", preset: Preset{Name: "planted", SearchEndpoint: "https://search.invalid/\u2028endpointplant/{query}"}},
		{name: "search_key_env", field: "search_key_env", fragment: "KEYPLANT", preset: Preset{Name: "planted", SearchKeyEnv: "P8_\u2028KEYPLANT"}},
		{name: "allow_hosts", field: "allow_hosts", fragment: "hostplant", preset: Preset{Name: "planted", AllowHosts: []string{"search.invalid", "\u2028hostplant"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := ladderEnv(t)
			regPath, outDir := driftFixture(t)
			t.Setenv(providers.EnvHome, home)
			t.Setenv("AGY_PRO_PROVIDERS", regPath)
			t.Setenv(EnvFanoutPreset, "")
			raw, err := json.Marshal(&PresetsFile{Presets: []Preset{tc.preset}, DefaultPreset: "planted"})
			if err != nil {
				t.Fatal(err)
			}
			writeRawPresetsFile(t, home, string(raw))

			old := executeEngine
			executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
				cfg.Root = filepath.Dir(regPath)
				cfg.OutDir = outDir
				cfg.RegistryPath = regPath
				return Execute(ctx, cfg)
			}
			t.Cleanup(func() { executeEngine = old })

			var out, errw bytes.Buffer
			if code := Run([]string{"-prompt", "p", "-seats", "prov-a/model-a", "-timeout", "15"}, nil, &out, &errw); code != 0 {
				t.Fatalf("Run exit = %d; stderr = %s", code, errw.String())
			}
			if !strings.Contains(errw.String(), "warning: ignoring preset; "+tc.field+" has control/format runes") {
				t.Errorf("drop warning does not name %s: %q", tc.field, errw.String())
			}
			summary, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			for surface, value := range map[string]string{"stdout": out.String(), "stderr": errw.String(), "summary.json": string(summary)} {
				if strings.ContainsRune(value, '\u2028') || strings.Contains(value, `\u2028`) || strings.Contains(value, tc.fragment) {
					t.Errorf("%s received the planted %s value: %q", surface, tc.field, value)
				}
			}
		})
	}
}

func TestP8ControlDropKeepsBenignFieldsSilent(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	raw := `{"presets":[{"name":"benign","task":"review","search_endpoint":"https://search.example/검색/{query}","search_key_env":"P8_SEARCH_KEY","allow_hosts":["search.example","例え.テスト"],"root":"작업공간","out_dir":"レポート"}]}`
	if err := os.WriteFile(st.Path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	st.WarnOut = &warn
	got, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Presets) != 1 || got.Presets[0].Name != "benign" {
		t.Fatalf("benign record did not load: %+v", got.Presets)
	}
	if warn.Len() != 0 {
		t.Errorf("benign record produced a warning: %q", warn.String())
	}
}
