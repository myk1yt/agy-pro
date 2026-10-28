package agysettings

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func probeEntry(url, name string) ExternalEntry {
	return ExternalEntry{
		URL:           url,
		ModelName:     name,
		DisplayName:   "Probe (" + name + ")",
		ContextWindow: 65536,
	}
}

func TestBuildP2SettingsComposesTheDocument(t *testing.T) {
	doc, err := BuildP2Settings([]ExternalEntry{
		probeEntry("gemini-api://127.0.0.1:8791/models/probe-gemini", "gemini-api://127.0.0.1:8791/models/probe-gemini"),
		probeEntry("blade:127.0.0.1:8792/models/probe-blade", "blade:127.0.0.1:8792/models/probe-blade"),
	})
	if err != nil {
		t.Fatalf("BuildP2Settings: %v", err)
	}

	if got := doc[KeyModelProvider]; got != ModelProviderGemini {
		t.Errorf("modelProvider = %v; want %q", got, ModelProviderGemini)
	}
	for key := range doc {
		if !isManagedKey(key) {
			t.Errorf("BuildP2Settings emitted an unmanaged key %q", key)
		}
	}

	models, ok := doc[KeyCustomModels].(map[string]CustomModelConfig)
	if !ok {
		t.Fatalf("customModels is %T; want map[string]CustomModelConfig", doc[KeyCustomModels])
	}
	if len(models) != 2 {
		t.Fatalf("customModels has %d entries; want 2", len(models))
	}
	cfg, ok := models["gemini-api://127.0.0.1:8791/models/probe-gemini"]
	if !ok {
		t.Fatalf("the map key is not the entry URL: %v", keysOf(models))
	}
	if cfg.ModelName != "gemini-api://127.0.0.1:8791/models/probe-gemini" {
		t.Errorf("modelName = %q; want the entry's model name", cfg.ModelName)
	}
	if cfg.DisplayName != "Probe (gemini-api://127.0.0.1:8791/models/probe-gemini)" {
		t.Errorf("displayName = %q", cfg.DisplayName)
	}
	if cfg.ContextWindow != 65536 {
		t.Errorf("context_window = %d; want 65536", cfg.ContextWindow)
	}
	if cfg.PlanTier != "" {
		t.Errorf("plan_tier = %q; want empty (semantics unverified)", cfg.PlanTier)
	}
	if cfg.ModelFeatures != nil {
		t.Errorf("modelFeatures = %+v; want nil when unset", cfg.ModelFeatures)
	}
}

func TestBuildP2SettingsCarriesModelFeaturesAndPlanTier(t *testing.T) {
	doc, err := BuildP2Settings([]ExternalEntry{{
		URL:           "beyond-api://127.0.0.1:8793/probe-beyond",
		ModelName:     "beyond-api://127.0.0.1:8793/probe-beyond",
		DisplayName:   "Beyond",
		ContextWindow: 1048576,
		PlanTier:      "paid",
		ModelFeatures: &ModelFeatures{Images: true, PDF: true},
	}})
	if err != nil {
		t.Fatalf("BuildP2Settings: %v", err)
	}
	models := doc[KeyCustomModels].(map[string]CustomModelConfig)
	cfg := models["beyond-api://127.0.0.1:8793/probe-beyond"]
	if cfg.PlanTier != "paid" {
		t.Errorf("plan_tier = %q; want paid", cfg.PlanTier)
	}
	if cfg.ModelFeatures == nil || !cfg.ModelFeatures.Images || !cfg.ModelFeatures.PDF || cfg.ModelFeatures.Video {
		t.Errorf("modelFeatures = %+v; want images+pdf only", cfg.ModelFeatures)
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"modelName"`, `"displayName"`, `"context_window"`, `"plan_tier"`, `"modelFeatures"`, `"images":true`, `"pdf":true`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("entry JSON %s is missing %s", raw, want)
		}
	}
}

// Every one of the 11 static Gemini defaults is refused as a customModels key,
// with the collision named.
func TestBuildP2SettingsRejectsEveryStaticIDCollision(t *testing.T) {
	for _, id := range StaticGeminiIDs() {
		t.Run(id, func(t *testing.T) {
			_, err := BuildP2Settings([]ExternalEntry{{
				URL:       "gemini-api://127.0.0.1:8791/models/" + id,
				ModelName: "gemini-api://127.0.0.1:8791/models/" + id,
			}})
			if err == nil {
				t.Fatalf("an entry resolving to the static default %q was accepted", id)
			}
			if !strings.Contains(err.Error(), id) {
				t.Errorf("error does not name the collision %q: %v", id, err)
			}
			if !strings.Contains(err.Error(), "static") && !strings.Contains(err.Error(), "reserved") {
				t.Errorf("error does not explain the reservation: %v", err)
			}
		})
	}
}

// The same refusal covers the beyond-api trailing-segment form and the
// modelName field, where agy would resolve to the bare static id.
func TestBuildP2SettingsRejectsStaticIDThroughOtherForms(t *testing.T) {
	cases := map[string]ExternalEntry{
		"beyond-api trailing segment": {
			URL:       "beyond-api://127.0.0.1:8793/gemini-3.8-flash-high",
			ModelName: "beyond-api://127.0.0.1:8793/gemini-3.8-flash-high",
		},
		"modelName field": {
			URL:       "gemini-api://127.0.0.1:8791/models/probe",
			ModelName: "gemini-3.1-pro-low",
		},
		"case-insensitive": {
			URL:       "gemini-api://127.0.0.1:8791/models/GEMINI-3.8-FLASH-HIGH",
			ModelName: "gemini-api://127.0.0.1:8791/models/GEMINI-3.8-FLASH-HIGH",
		},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildP2Settings([]ExternalEntry{entry}); err == nil {
				t.Errorf("an entry resolving to a static default was accepted: %+v", entry)
			}
		})
	}
}

// The AI-Studio provider preset names are reserved too, so an external slug can
// never be mistaken for agy-pro's own Google AI-Studio provider.
func TestBuildP2SettingsRejectsAIStudioPresetNames(t *testing.T) {
	for _, name := range aiStudioPresetNames() {
		t.Run(name, func(t *testing.T) {
			_, err := BuildP2Settings([]ExternalEntry{{
				URL:       "gemini-api://127.0.0.1:8791/models/" + name,
				ModelName: "gemini-api://127.0.0.1:8791/models/" + name,
			}})
			if err == nil {
				t.Fatalf("an entry resolving to the AI-Studio preset name %q was accepted", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error does not name the preset collision %q: %v", name, err)
			}
		})
	}
}

func TestReservedModelNamesCoversBothSources(t *testing.T) {
	reserved := ReservedModelNames()
	if len(reserved) != len(StaticGeminiIDs())+len(aiStudioPresetNames()) {
		t.Fatalf("ReservedModelNames() = %v; want the static ids plus the AI-Studio preset names", reserved)
	}
	for _, id := range StaticGeminiIDs() {
		if !containsFold(reserved, id) {
			t.Errorf("ReservedModelNames() is missing the static id %q", id)
		}
	}
	for _, name := range aiStudioPresetNames() {
		if !containsFold(reserved, name) {
			t.Errorf("ReservedModelNames() is missing the preset name %q", name)
		}
	}
	// The accessor hands out a copy.
	reserved[0] = "mutated"
	if ReservedModelNames()[0] == "mutated" {
		t.Error("ReservedModelNames() returned a shared slice")
	}
}

// A bare modelName is not a customModels key: agy rejects it outright.
func TestBuildP2SettingsRejectsBareModelNames(t *testing.T) {
	for _, key := range []string{
		"probe-gemini",
		"gemini-3.8-flash-high",
		"my-ext",
		"127.0.0.1:8791/models/probe",
		"http://127.0.0.1:8791/models/probe",
		"gemini-api://",
		"blade:",
		"gemini-api://host/models/",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := BuildP2Settings([]ExternalEntry{probeEntry(key, key)})
			if err == nil {
				t.Fatalf("key %q was accepted; agy rejects bare model names", key)
			}
			if !strings.Contains(err.Error(), "custom model URL") {
				t.Errorf("error does not quote agy's URL expectation: %v", err)
			}
		})
	}
}

// All four documented schemes are accepted, including the binary's own example
// id form.
func TestBuildP2SettingsAcceptsEveryDocumentedScheme(t *testing.T) {
	keys := []string{
		"gemini-api://127.0.0.1:8811/models/alpha",
		"blade:127.0.0.1:8812/models/beta",
		"evergreen://blade:gdm-aip/lmroot-v3:/jetski/gamma",
		"beyond-api://127.0.0.1:8813/delta",
		"gemini-api:/models/snowball",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			doc, err := BuildP2Settings([]ExternalEntry{probeEntry(key, key)})
			if err != nil {
				t.Fatalf("BuildP2Settings(%q): %v", key, err)
			}
			models := doc[KeyCustomModels].(map[string]CustomModelConfig)
			if _, ok := models[key]; !ok {
				t.Errorf("key %q missing from customModels: %v", key, keysOf(models))
			}
		})
	}
}

func TestBuildP2SettingsRejectsMalformedEntries(t *testing.T) {
	cases := map[string]struct {
		entries []ExternalEntry
		wantSub string
	}{
		"empty url": {
			entries: []ExternalEntry{{URL: "", ModelName: "x"}},
			wantSub: "must not be empty",
		},
		"whitespace url": {
			entries: []ExternalEntry{{URL: "   ", ModelName: "x"}},
			wantSub: "must not be empty",
		},
		"url with a space": {
			entries: []ExternalEntry{{URL: "gemini-api://host/models/a b", ModelName: "gemini-api://host/models/a b"}},
			wantSub: "whitespace",
		},
		"missing modelName": {
			entries: []ExternalEntry{{URL: "gemini-api://host/models/probe", ModelName: ""}},
			wantSub: "modelName is required",
		},
		"negative context window": {
			entries: []ExternalEntry{{URL: "gemini-api://host/models/probe", ModelName: "gemini-api://host/models/probe", ContextWindow: -1}},
			wantSub: "context_window",
		},
		"duplicate url": {
			entries: []ExternalEntry{
				probeEntry("gemini-api://host/models/probe", "gemini-api://host/models/probe"),
				probeEntry("gemini-api://host/models/probe", "gemini-api://host/models/probe"),
			},
			wantSub: "duplicate",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := BuildP2Settings(c.entries)
			if err == nil {
				t.Fatalf("BuildP2Settings accepted %s", name)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error = %v; want it to mention %q", err, c.wantSub)
			}
		})
	}
}

// The composed document can be written straight through the managed file API.
func TestBuildP2SettingsFeedsTheManagedFileAPI(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")

	doc, err := BuildP2Settings([]ExternalEntry{
		probeEntry("gemini-api://127.0.0.1:8791/models/probe-gemini", "gemini-api://127.0.0.1:8791/models/probe-gemini"),
	})
	if err != nil {
		t.Fatalf("BuildP2Settings: %v", err)
	}

	f := openEmpty(t, path)
	defer f.Close()
	for key, value := range doc {
		if err := f.Set(key, value); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	if err := f.Write(); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var written map[string]json.RawMessage
	if err := json.Unmarshal(readFileForTest(t, path), &written); err != nil {
		t.Fatalf("written document is not valid JSON: %v", err)
	}
	if string(written[KeyModelProvider]) != `"gemini"` {
		t.Errorf("modelProvider = %s; want \"gemini\"", written[KeyModelProvider])
	}
	if !strings.Contains(string(written[KeyCustomModels]), "probe-gemini") {
		t.Errorf("customModels = %s; want the probe entry", written[KeyCustomModels])
	}
}

// A pre-existing settings file that already carries a reserved entry is refused
// on Open, so a stale collision can never be written through.
func TestOpenRefusesAReservedCustomModelsEntry(t *testing.T) {
	home := sandboxHome(t)
	path := filepath.Join(home, "settings.json")
	planted := `{"customModels":{"gemini-api://127.0.0.1:8791/models/gemini-3.8-flash-high":{"modelName":"gemini-api://127.0.0.1:8791/models/gemini-3.8-flash-high"}}}`
	if err := writeFileForTest(path, []byte(planted)); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a settings file whose customModels entry resolves to a static default")
	} else if !strings.Contains(err.Error(), "gemini-3.8-flash-high") {
		t.Errorf("error does not name the collision: %v", err)
	}
}

func TestBuildP2SettingsWithNoExternals(t *testing.T) {
	doc, err := BuildP2Settings(nil)
	if err != nil {
		t.Fatalf("BuildP2Settings(nil): %v", err)
	}
	if doc[KeyModelProvider] != ModelProviderGemini {
		t.Errorf("modelProvider = %v; want %q", doc[KeyModelProvider], ModelProviderGemini)
	}
	models, ok := doc[KeyCustomModels].(map[string]CustomModelConfig)
	if !ok || len(models) != 0 {
		t.Errorf("customModels = %v; want an empty managed map", doc[KeyCustomModels])
	}
}

func keysOf(m map[string]CustomModelConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}
