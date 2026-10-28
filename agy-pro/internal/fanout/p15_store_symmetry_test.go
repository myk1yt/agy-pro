package fanout

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var p15ControlFields = []string{
	"name", "task", "root", "out_dir", "seats", "tools",
	"search_endpoint", "search_key_env", "allow_hosts",
}

func p15PresetWithControl(field string, class rune) Preset {
	p := Preset{Name: "ordinary"}
	switch field {
	case "name":
		p.Name = "ordinary" + string(class) + "name"
	case "task":
		p.Task = "review" + string(class) + "task"
	case "root":
		p.Root = "work" + string(class) + "root"
	case "out_dir":
		p.OutDir = "reports" + string(class) + "out"
	case "seats":
		p.Seats = "native:one" + string(class) + ",native:two"
	case "tools":
		p.Tools = "read,search" + string(class) + "write"
	case "search_endpoint":
		p.SearchEndpoint = "https://x.invalid/a" + string(class) + "b"
	case "search_key_env":
		p.SearchKeyEnv = "P15" + string(class) + "_KEY"
	case "allow_hosts":
		p.AllowHosts = []string{"search." + string(class) + "invalid"}
	default:
		panic("unknown P15 field " + field)
	}
	return p
}

func p15FieldOperand(p Preset, field string) string {
	switch field {
	case "name":
		return p.Name
	case "task":
		return p.Task
	case "root":
		return p.Root
	case "out_dir":
		return p.OutDir
	case "seats":
		return p.Seats
	case "tools":
		return p.Tools
	case "search_endpoint":
		return p.SearchEndpoint
	case "search_key_env":
		return p.SearchKeyEnv
	case "allow_hosts":
		return strings.Join(p.AllowHosts, ",")
	}
	panic("unknown P15 field " + field)
}

func TestP15SaveRefusesEveryLoadDroppedControlField(t *testing.T) {
	for _, field := range p15ControlFields {
		for _, class := range []rune{'\u2028', '\u200d'} {
			t.Run(field+"/"+string(class), func(t *testing.T) {
				p := p15PresetWithControl(field, class)
				st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
				err := st.SavePresets(&PresetsFile{Presets: []Preset{p}})
				if err == nil {
					t.Fatalf("SavePresets accepted %s containing %U, which LoadPresets drops", field, class)
				}
				if operand := p15FieldOperand(p, field); strings.Contains(err.Error(), operand) {
					t.Errorf("refusal echoes the rejected operand: %q", err)
				}
				if _, statErr := os.Stat(st.Path); !os.IsNotExist(statErr) {
					t.Errorf("a refused save wrote the file: %v", statErr)
				}
			})
		}
	}
}

func TestP15HandAuthoredControlFieldsDropWithOperatorWarning(t *testing.T) {
	for _, field := range p15ControlFields {
		for _, class := range []rune{'\u2028', '\u200d'} {
			t.Run(field+"/"+string(class), func(t *testing.T) {
				st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
				raw, err := json.Marshal(&PresetsFile{Presets: []Preset{p15PresetWithControl(field, class)}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(st.Path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				var warning bytes.Buffer
				st.WarnOut = &warning
				loaded, err := st.LoadPresets()
				if err != nil {
					t.Fatal(err)
				}
				if len(loaded.Presets) != 0 {
					t.Fatalf("load retained a %s value carrying %U", field, class)
				}
				want := "warning: ignoring preset; " + field + " has control/format runes"
				if !strings.Contains(warning.String(), want) {
					t.Errorf("operator warning = %q; want %q", warning.String(), want)
				}
			})
		}
	}
}

func TestP15BenignFieldsStillSaveAndLoad(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	in := &PresetsFile{Presets: []Preset{
		{
			Name: "한국어 预设 العربية", Task: "review", Root: "작업 공간/루트", OutDir: "レポート/結果",
			Seats: "native:gemini,prov/model", Tools: "read,search",
			SearchEndpoint: "https://search.example/a-b/c_d/{query}", SearchKeyEnv: "P15_SEARCH_KEY",
			AllowHosts: []string{"search.example", "example-test.invalid"},
		},
		{Name: "ordinary-second", Tools: "read"},
	}}
	for _, p := range in.Presets {
		if err := ValidatePresetForSave(p); err != nil {
			t.Fatalf("benign preset validation: %v", err)
		}
	}
	if err := st.SavePresets(in); err != nil {
		t.Fatalf("benign preset save: %v", err)
	}
	var warning bytes.Buffer
	st.WarnOut = &warning
	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Presets) != len(in.Presets) {
		t.Fatalf("loaded %d presets; want %d", len(loaded.Presets), len(in.Presets))
	}
	if !samePresetContent(loaded.Presets[0], in.Presets[0]) || !samePresetContent(loaded.Presets[1], in.Presets[1]) {
		t.Errorf("benign presets did not round-trip: %+v", loaded.Presets)
	}
	if warning.Len() != 0 {
		t.Errorf("benign presets produced a warning: %q", warning.String())
	}
}
