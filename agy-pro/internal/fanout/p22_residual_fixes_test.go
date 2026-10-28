package fanout

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/paths"
)

func TestP22DropNoticeOperativeAtEightyColumnsForEveryField(t *testing.T) {
	const operand = "offending-operand-value"
	for _, tc := range []struct {
		field  string
		preset Preset
	}{
		{field: "name", preset: Preset{Name: "name-\u2028" + operand}},
		{field: "task", preset: Preset{Name: "ordinary", Task: "review\u2028" + operand}},
		{field: "root", preset: Preset{Name: "ordinary", Root: "/workspace/\u2028" + operand}},
		{field: "out_dir", preset: Preset{Name: "ordinary", OutDir: "reports/\u2028" + operand}},
		{field: "seats", preset: Preset{Name: "ordinary", Seats: "native:m1\u2028" + operand}},
		{field: "tools", preset: Preset{Name: "ordinary", Tools: "read\u2028" + operand}},
		{field: "search_endpoint", preset: Preset{Name: "ordinary", SearchEndpoint: "https://search.invalid/\u2028" + operand}},
		{field: "search_key_env", preset: Preset{Name: "ordinary", SearchKeyEnv: "KEY_\u2028" + operand}},
		{field: "allow_hosts", preset: Preset{Name: "ordinary", AllowHosts: []string{"search.invalid", "\u2028" + operand}}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
			raw, err := json.Marshal(&PresetsFile{Presets: []Preset{tc.preset}})
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
				t.Fatalf("control-carrying record survived: %+v", loaded.Presets)
			}
			line := strings.TrimSuffix(warning.String(), "\n")
			rendered := cellwidth.Truncate(line, 80)
			operative := "warning: ignoring preset; " + tc.field + " has control/format runes;"
			if !strings.Contains(rendered, operative) {
				t.Fatalf("80-column rendering loses the operative clause: %q", rendered)
			}
			if strings.Count(warning.String(), "\n") != 1 || strings.ContainsRune(warning.String(), '\u2028') {
				t.Fatalf("drop notice is not one escaped line: %q", warning.String())
			}
			if tc.field != "name" && strings.Contains(warning.String(), operand) {
				t.Fatalf("drop notice echoes the offending field value: %q", warning.String())
			}
			t.Logf("%s 80-column rendering=%q", tc.field, rendered)
		})
	}
}

func TestP22FanoutHelpOnlyFromPostParseOperands(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	t.Setenv(EnvFanoutPreset, "")
	old := executeEngine
	called := 0
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		called++
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	for _, tc := range []struct {
		name       string
		args       []string
		want       int
		wantEngine int
	}{
		{name: "preset help spelling is a value", args: []string{"--preset", "-h"}, want: 2},
		{name: "prompt long help is a value", args: []string{"--prompt", "--help", "--seats", "native:m1", "--json"}, want: 0, wantEngine: 1},
		{name: "prompt equals help is the same value", args: []string{"--prompt=-h", "--seats", "native:m1", "--json"}, want: 0, wantEngine: 1},
		{name: "terminator makes help an operand", args: []string{"--", "--help"}, want: 2},
		{name: "stray operand", args: []string{"bogus-operand"}, want: 2},
		{name: "help after stray operand still wins", args: []string{"bogus", "--help"}, want: 0},
		{name: "short help after stray operand still wins", args: []string{"bogus", "-h"}, want: 0},
		{name: "double dash as prompt value is not a terminator", args: []string{"--prompt", "--", "bogus", "--help"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = 0
			var out, errw bytes.Buffer
			if got := Run(tc.args, strings.NewReader(""), &out, &errw); got != tc.want {
				t.Fatalf("Run(%q) exit = %d, want %d; stderr=%q", tc.args, got, tc.want, errw.String())
			}
			if called != tc.wantEngine {
				t.Fatalf("Run(%q) engine calls = %d, want %d; stderr=%q", tc.args, called, tc.wantEngine, errw.String())
			}
			if tc.want == 2 && !strings.Contains(errw.String(), "Error:") {
				t.Fatalf("usage refusal missing for %q: %q", tc.args, errw.String())
			}
			t.Logf("Run(%q) exit=%d engine=%d stderr=%q", tc.args, tc.want, called, errw.String())
		})
	}
}

func TestP22MaskLocationComponentsPositional(t *testing.T) {
	plant := "sk_" + "live_1234567890abcdef"
	for _, tc := range []struct {
		name       string
		knob       string
		value      string
		separators string
	}{
		{name: "path prefix overlap", knob: "root", value: plant + "/" + plant + "EXTRA", separators: "/\\"},
		{name: "URL prefix overlap", knob: "search_endpoint", value: "https://search.invalid/" + plant + "?" + plant + "EXTRA={query}", separators: "/\\?&=#"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := maskPresetLocationComponents(tc.value, tc.separators)
			if strings.Contains(got, "EXTRA") || strings.Contains(got, plant) {
				t.Fatalf("prefix sibling leaked its tail: %q", got)
			}
			if strings.Count(got, "sk_li****") != 2 {
				t.Fatalf("both components must be masked positionally: %q", got)
			}
			if gotDirect := maskPresetNoticeOperand(tc.knob, tc.value); strings.Contains(gotDirect, "EXTRA") || strings.Contains(gotDirect, plant) {
				t.Fatalf("notice operand leaked through %s: %q", tc.knob, gotDirect)
			}
			t.Logf("%s=%q", tc.knob, got)
		})
	}
}

func TestP22MaskLocationComponentsBenignByteIdentity(t *testing.T) {
	for _, tc := range []struct {
		knob  string
		value string
	}{
		{knob: "root", value: "/workspace/review"},
		{knob: "out_dir", value: `C:\Users\k1yt\proj`},
		{knob: "root", value: `\\server\share\review`},
		{knob: "search_endpoint", value: "https://api.example.com/res/v1/web/search?q={query}&count=10"},
	} {
		t.Run(tc.knob+tc.value, func(t *testing.T) {
			if got := maskPresetNoticeOperand(tc.knob, tc.value); got != tc.value {
				t.Fatalf("benign value changed\n got: %q\nwant: %q", got, tc.value)
			}
		})
	}
}
