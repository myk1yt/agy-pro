package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

func TestBareRunAdoptsPreexistingMarkedBuiltinRecord(t *testing.T) {
	home, stderr := ladderEnv(t)
	seed := Preset{
		Name:        BuiltinPresetName,
		Seats:       "native:pre-change",
		SeatsSHA256: SeatsHash("native:pre-change"),
	}
	data, err := json.Marshal(&PresetsFile{Presets: []Preset{seed}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("a pre-existing marked record must be adopted: %v", err)
	}
	if got := seatsOf(cfg.Seats); got != "native:pre-change" {
		t.Errorf("seats = %q; want the pre-existing record's lineup", got)
	}
	if cfg.SeatLineup.Preset != BuiltinPresetName || !cfg.SeatLineup.Standing() {
		t.Errorf("SeatLineup = %+v; want the standing built-in seed origin", cfg.SeatLineup)
	}
	want := "fanout: seat lineup from preset \"(built-in defaults)\" (default_preset rung, no key set): 1 seat(s), 0 metered provider seats (native:pre-change)\n"
	if stderr.String() != want {
		t.Errorf("adoption echo drifted\n got: %q\nwant: %q", stderr.String(), want)
	}
}

func TestRunDropsControlRunesFromPresetNamesAndPaths(t *testing.T) {
	home, _ := ladderEnv(t)
	regPath, outDir := driftFixture(t)
	t.Setenv(providers.EnvHome, home)
	t.Setenv("AGY_PRO_PROVIDERS", regPath)
	t.Setenv(EnvFanoutPreset, "")

	raw := `{"presets":[
		{"name":"good","n":2},
		{"name":"planted-name\u2028","n":7},
		{"name":"planted-paths","root":"workspace\u2028escape","out_dir":"reports\u2028forge"}
	],"default_preset":"planted-paths"}`
	path := filepath.Join(home, "fanout-presets.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var loadWarn bytes.Buffer
	loaded, err := (&PresetFileStore{Path: path, WarnOut: &loadWarn}).LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Presets) != 1 || loaded.Presets[0].Name != "good" {
		t.Fatalf("loaded presets = %+v; want only the clean record", loaded.Presets)
	}
	if got := strings.Count(loadWarn.String(), "has control/format runes"); got != 2 {
		t.Fatalf("load warnings = %d; want one per planted record: %q", got, loadWarn.String())
	}

	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		if cfg.Root != "." || cfg.OutDir != "" {
			t.Errorf("engine received planted paths: root=%q out_dir=%q", cfg.Root, cfg.OutDir)
		}
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
	for _, surface := range []string{out.String(), errw.String()} {
		if strings.ContainsRune(surface, '\u2028') {
			t.Errorf("console surface carries the planted lane separator: %q", surface)
		}
		for _, fragment := range []string{"escape", "forge"} {
			if strings.Contains(surface, fragment) {
				t.Errorf("console surface leaks planted path fragment %q: %q", fragment, surface)
			}
		}
	}
	summary, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(summary), '\u2028') {
		t.Errorf("summary.json carries the planted lane separator: %q", summary)
	}
	for _, fragment := range []string{"escape", "forge"} {
		if strings.Contains(string(summary), fragment) {
			t.Errorf("summary.json leaks planted path fragment %q: %q", fragment, summary)
		}
	}
}

func TestValidatePresetSaveDoorsRefuseParentPathTraversal(t *testing.T) {
	doors := []struct {
		name     string
		preset   string
		validate func(Preset) error
	}{
		{name: "user", preset: "ordinary", validate: ValidatePresetForSave},
		{name: "seed", preset: BuiltinPresetName, validate: ValidateSeedPresetForSave},
	}
	invalid := []string{"..", "../outside", "workspace/../outside", "a/..", `..\outside`, `workspace\..\outside`}
	for _, door := range doors {
		t.Run(door.name, func(t *testing.T) {
			for _, field := range []string{"root", "out_dir"} {
				for _, path := range invalid {
					p := Preset{Name: door.preset}
					if field == "root" {
						p.Root = path
					} else {
						p.OutDir = path
					}
					err := door.validate(p)
					if !errors.Is(err, ErrPresetPathTraversal) {
						t.Errorf("%s %q = %v; want ErrPresetPathTraversal", field, path, err)
					}
					if len(path) > 2 && err != nil && strings.Contains(err.Error(), path) {
						t.Errorf("refusal echoes path operand %q: %v", path, err)
					}
				}
			}
			for _, path := range []string{"/ws", "/tmp/out", "D:/ws", `C:\ws`, `\\server\share`, "reports/nightly", "...", "..cache", "a/..b"} {
				if err := door.validate(Preset{Name: door.preset, Root: path, OutDir: path}); err != nil {
					t.Errorf("absolute or non-traversal path %q must stay legal: %v", path, err)
				}
			}
		})
	}
}

func TestPresetRungRefusesParentTraversalAndAllowsAbsolutePaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  string
		path   string
		preset string
		env    string
	}{
		{name: "default root slash", field: "root", path: "../outside"},
		{name: "named out backslash", field: "out_dir", path: `reports\..\outside`, preset: "nightly"},
		{name: "env root interior", field: "root", path: "workspace/../outside", env: "nightly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, stderr := ladderEnv(t)
			p := Preset{Name: "nightly"}
			if tc.field == "root" {
				p.Root = tc.path
			} else {
				p.OutDir = tc.path
			}
			savePresetsFile(t, home, &PresetsFile{Presets: []Preset{p}, DefaultPreset: "nightly"})
			if tc.env != "" {
				t.Setenv(EnvFanoutPreset, tc.env)
			}
			f := ladderFlags(stderr)
			f.preset = tc.preset
			_, err := buildRunConfig(f, "p")
			if !errors.Is(err, ErrPresetPathTraversal) {
				t.Fatalf("buildRunConfig = %v; want ErrPresetPathTraversal", err)
			}
			if strings.Contains(err.Error(), tc.path) {
				t.Errorf("error echoes path operand: %v", err)
			}
		})
	}

	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: "nightly", Root: "/ws", OutDir: "/tmp/out"}},
		DefaultPreset: "nightly",
	})
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatalf("absolute preset paths must stay legal: %v", err)
	}
	if cfg.Root != "/ws" || cfg.OutDir != "/tmp/out" || cfg.Sources["root"] != "preset" || cfg.Sources["out"] != "preset" {
		t.Errorf("absolute preset paths did not resolve: %+v", cfg)
	}
}

func TestStandingPresetPrivilegeEgressNotice(t *testing.T) {
	seed := Preset{
		Name:           BuiltinPresetName,
		Seats:          "native:gemini-a",
		Tools:          "read,search,write",
		SearchEndpoint: "https://search.example/{query}",
		SearchKeyEnv:   "P4_SEARCH_KEY",
		Root:           "/workspace/review",
		OutDir:         "reports/review",
	}
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{seed}})
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr lines = %d; want one disclosure plus the existing lineup echo: %q", len(lines), stderr.String())
	}
	want := `fanout: tools="read,search,write", search_endpoint="https://search.example/{query}", search_key_env="P4_SEARCH_KEY", root="/workspace/review", out_dir="reports/review" (privilege/egress knobs: standing preset "(built-in defaults)")`
	if lines[0] != want {
		t.Errorf("standing disclosure drifted\n got: %q\nwant: %q", lines[0], want)
	}
	if !strings.Contains(lines[1], `seat lineup from preset "(built-in defaults)"`) || strings.Contains(lines[1], "privilege/egress") {
		t.Errorf("lineup echo missing or duplicated: %q", lines[1])
	}

	home, stderr = ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{seed}})
	f := ladderFlags(stderr)
	f.preset = BuiltinPresetName
	if _, err := buildRunConfig(f, "p"); err != nil {
		t.Fatal(err)
	}
	namedWant := strings.Replace(want, "standing preset", "preset", 1)
	namedLines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(namedLines) != 2 || namedLines[0] != namedWant {
		t.Errorf("ad-hoc named disclosure drifted\n got: %q\nwant first line: %q", stderr.String(), namedWant)
	}
	if len(namedLines) == 2 && (!strings.Contains(namedLines[1], "(--preset rung)") || !strings.Contains(namedLines[1], "ad-hoc - confirm")) {
		t.Errorf("ad-hoc echo/confirm clause was removed: %q", namedLines[1])
	}

	for _, tc := range []struct {
		knob   string
		source string
	}{
		{knob: "tools", source: "tools"},
		{knob: "search_endpoint", source: "search"},
		{knob: "search_key_env", source: "search"},
		{knob: "root", source: "root"},
		{knob: "out_dir", source: "out"},
	} {
		t.Run("masks "+tc.knob, func(t *testing.T) {
			p := Preset{Name: BuiltinPresetName}
			switch tc.knob {
			case "tools":
				p.Tools = testfixtures.SKLive
			case "search_endpoint":
				p.SearchEndpoint = testfixtures.SKLive
			case "search_key_env":
				p.SearchKeyEnv = testfixtures.SKLive
			case "root":
				p.Root = testfixtures.SKLive
			case "out_dir":
				p.OutDir = testfixtures.SKLive
			}
			notice := standingPresetPrivilegeEgressNotice(&p, map[string]string{tc.source: "preset"})
			if strings.Contains(notice, testfixtures.SKLive) || !strings.Contains(notice, tc.knob+`="sk_li****"`) {
				t.Errorf("notice = %q; want %s masked as sk_li****", notice, tc.knob)
			}
		})
	}
	notice := standingPresetPrivilegeEgressNotice(&Preset{Name: testfixtures.SKLive, Root: "/ws"}, map[string]string{"root": "preset"})
	if strings.Contains(notice, testfixtures.SKLive) || !strings.Contains(notice, `preset "sk_li****"`) {
		t.Errorf("notice does not mask the preset name: %q", notice)
	}
}

func TestValidateSeedPresetForSaveRequiresExactSeedName(t *testing.T) {
	for _, name := range []string{"ordinary", "(built-in defaults) ", " (built-in defaults)", "(BUILT-IN DEFAULTS)"} {
		if err := ValidateSeedPresetForSave(Preset{Name: name}); !errors.Is(err, ErrPresetNameRequired) {
			t.Errorf("ValidateSeedPresetForSave(%q) = %v; want ErrPresetNameRequired", name, err)
		}
	}
	if err := ValidateSeedPresetForSave(Preset{Name: BuiltinPresetName}); err != nil {
		t.Errorf("the editor's exact seed record must keep validating: %v", err)
	}
}

func TestDanglingDefaultKeySuppressesBuiltinSeed(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		Presets:       []Preset{{Name: BuiltinPresetName, Seats: "native:seed"}},
		DefaultPreset: "ghost",
	})
	cfg, err := buildRunConfig(ladderFlags(stderr), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Seats) != 0 || cfg.Sources["seats"] != "engine" || cfg.SeatLineup.FromPreset() {
		t.Errorf("dangling key consumed the seed: seats=%q sources=%+v origin=%+v", seatsOf(cfg.Seats), cfg.Sources, cfg.SeatLineup)
	}
	want := "fanout: warning: default preset \"ghost\" not found in fanout-presets.json; running without a preset\n"
	if stderr.String() != want {
		t.Errorf("stderr drifted\n got: %q\nwant: %q", stderr.String(), want)
	}
}

func TestNearMissBuiltinNamesStayOrdinary(t *testing.T) {
	for _, name := range []string{"(built-in defaults) ", " (built-in defaults)", "(BUILT-IN DEFAULTS)", "(built-in  defaults)"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePresetForSave(Preset{Name: name}); err != nil {
				t.Errorf("the live user door refused near-miss %q: %v", name, err)
			}
			home, stderr := ladderEnv(t)
			savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: name, Seats: "native:near-miss"}}})
			cfg, err := buildRunConfig(ladderFlags(stderr), "p")
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Seats) != 0 || cfg.Sources["seats"] != "engine" || cfg.SeatLineup.FromPreset() {
				t.Errorf("near-miss %q resolved through the seed arm: %+v", name, cfg)
			}
			if stderr.Len() != 0 {
				t.Errorf("near-miss %q produced a seed notice or echo: %q", name, stderr.String())
			}
		})
	}
}

func TestSummarySeatLineupPresetMasksPlantedCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preset string
		want   string
	}{
		{name: "built-in remains byte-identical", preset: BuiltinPresetName, want: BuiltinPresetName},
		{name: "planted credential is masked", preset: testfixtures.SKLive, want: "sk_li****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			regPath, outDir := driftFixture(t)
			specs, err := ParseSeatSpecs("prov-a/model-a")
			if err != nil {
				t.Fatal(err)
			}
			var warn bytes.Buffer
			summary, err := Execute(context.Background(), driftConfig(regPath, outDir, specs,
				SeatLineupOrigin{Preset: tc.preset, Rung: RungDefaultPreset}, &warn))
			if err != nil {
				t.Fatal(err)
			}
			if summary.SeatLineup == nil || summary.SeatLineup.Preset != tc.want {
				t.Errorf("summary.SeatLineup = %+v; want preset %q", summary.SeatLineup, tc.want)
			}
			raw, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), testfixtures.SKLive) {
				t.Errorf("summary.json leaks the planted credential: %q", raw)
			}
			var decoded struct {
				SeatLineup *SeatLineupReport `json:"seat_lineup"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.SeatLineup == nil || decoded.SeatLineup.Preset != tc.want {
				t.Errorf("summary.json seat_lineup = %+v; want preset %q", decoded.SeatLineup, tc.want)
			}
		})
	}
}
