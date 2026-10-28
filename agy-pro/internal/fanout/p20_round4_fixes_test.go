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

	"agypro/internal/cellwidth"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

func TestP20FanoutUnknownOperandAndHelpPrecedence(t *testing.T) {
	plant := "sk_" + "live_1234567890abcdef"
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{name: "operand only", args: []string{"bogus-operand"}, want: 2},
		{name: "help after operand", args: []string{"bogus", "--help"}, want: 0},
		{name: "short help after operand", args: []string{"bogus", "-h"}, want: 0},
		{name: "credential operand", args: []string{plant}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errw bytes.Buffer
			if got := Run(tc.args, strings.NewReader(""), &out, &errw); got != tc.want {
				t.Fatalf("Run(%q) exit = %d, want %d; stderr=%q", tc.args, got, tc.want, errw.String())
			}
			if strings.Contains(errw.String(), plant) {
				t.Fatalf("stderr echoes the planted credential: %q", errw.String())
			}
			if tc.want == 2 && !strings.Contains(errw.String(), "takes no arguments") {
				t.Fatalf("usage refusal missing: %q", errw.String())
			}
			if tc.want == 0 && !strings.Contains(errw.String(), "Usage: agy pro fanout") {
				t.Fatalf("help missing: %q", errw.String())
			}
		})
	}
}

func TestP20FanoutNormalRunUnaffected(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	old := executeEngine
	called := 0
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		called++
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })
	var out, errw bytes.Buffer
	args := []string{"--prompt", "p", "--seats", "native:m1", "--json"}
	if got := Run(args, strings.NewReader(""), &out, &errw); got != 0 {
		t.Fatalf("normal Run exit = %d, want 0; stderr=%q", got, errw.String())
	}
	if called != 1 {
		t.Fatalf("engine calls = %d, want 1", called)
	}
}

func TestP20DefaultsNoticeCannotSplitAndBenignIsUnchanged(t *testing.T) {
	t.Run("planted separator", func(t *testing.T) {
		_, stderr := ladderEnv(t)
		writeDefaults(t, `{"tools":"read,\u2028forged transcript line"}`)
		_, _ = buildRunConfig(ladderFlags(stderr), "p")
		out := stderr.String()
		if strings.ContainsRune(out, '\u2028') || strings.ContainsRune(out, '\u2029') || strings.Contains(out, "forged transcript line") {
			t.Fatalf("defaults value forged a transcript line: %q", out)
		}
		for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			if strings.HasPrefix(line, "forged") {
				t.Fatalf("forged second line: %q", out)
			}
		}
		t.Logf("planted defaults stderr=%q", out)
	})
	t.Run("benign bytes", func(t *testing.T) {
		_, stderr := ladderEnv(t)
		writeDefaults(t, `{"tools":"read"}`)
		if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
			t.Fatal(err)
		}
		want := "fanout: using providers.json default tools=read (engine default read,search)\n"
		if got := stderr.String(); got != want {
			t.Fatalf("benign defaults notice = %q, want %q", got, want)
		}
		t.Logf("benign defaults stderr=%q", stderr.String())
	})
}

func TestP20DropNoticeIsSingleLineAndOperativeAtEightyColumns(t *testing.T) {
	st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
	name := strings.Repeat("ordinary-name-", 8) + "\u2028forged"
	raw, err := json.Marshal(&PresetsFile{Presets: []Preset{{Name: name}, {Name: "healthy"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var warning bytes.Buffer
	st.WarnOut = &warning
	if _, err := st.LoadPresets(); err != nil {
		t.Fatal(err)
	}
	out := warning.String()
	if strings.Count(out, "\n") != 1 || strings.ContainsRune(out, '\u2028') || !strings.Contains(out, `\u2028`) {
		t.Fatalf("drop notice is not one escaped line: %q", out)
	}
	if !strings.Contains(out, `\u2028`) {
		t.Fatalf("escaped separator missing: %q", out)
	}
	truncated := cellwidth.Truncate(strings.TrimSuffix(out, "\n"), 80)
	if !strings.Contains(truncated, "ignoring preset") || !strings.Contains(truncated, "name has control/format runes") {
		t.Fatalf("operative clause lost at 80 columns: %q", truncated)
	}
	t.Logf("drop notice=%q; 80-column rendering=%q", out, truncated)
}

func TestP20DuplicateNameUnrelatedSaveKeepsFirstMarker(t *testing.T) {
	home, stderr := ladderEnv(t)
	path := writeRawPresetsFile(t, home, `{"default_preset":"x","presets":[{"name":"x","seats":"native:first"},{"name":"x","seats":"native:second"},{"name":"other","n":2}]}`)
	st := &PresetFileStore{Path: path}
	loaded, err := st.LoadPresets()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := loaded.FindPreset("x"); !ok || got.Seats != "native:first" {
		t.Fatalf("resolution is not first-wins: %+v, %v", got, ok)
	}
	loaded.Presets[2].N++
	if err := st.SavePresets(loaded); err != nil {
		t.Fatal(err)
	}
	if got := loaded.Presets[0].SeatsSHA256; got != "" {
		t.Fatalf("unrelated save stamped the resolved first record: %q", got)
	}
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "NO provenance marker") {
		t.Fatalf("resolved record lost its advisory: %q", stderr.String())
	}
}

func TestP20DefaultPresetKeyControlIsDroppedAndSaveRefused(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
		raw := []byte(`{"default_preset":"ghost\u2028forged","presets":[{"name":"healthy"}]}`)
		if err := os.WriteFile(st.Path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		var warning bytes.Buffer
		st.WarnOut = &warning
		loaded, err := st.LoadPresets()
		if err != nil {
			t.Fatal(err)
		}
		if loaded.DefaultPreset != "" {
			t.Fatalf("control-carrying default key survived: %q", loaded.DefaultPreset)
		}
		out := warning.String()
		if strings.Count(out, "\n") != 1 || strings.ContainsRune(out, '\u2028') || !strings.Contains(out, "default_preset contains control") {
			t.Fatalf("default-key drop notice = %q", out)
		}
	})
	t.Run("save", func(t *testing.T) {
		st := &PresetFileStore{Path: filepath.Join(t.TempDir(), "fanout-presets.json")}
		err := st.SavePresets(&PresetsFile{DefaultPreset: "ghost\u2029forged"})
		if !errors.Is(err, ErrPresetFieldsControl) {
			t.Fatalf("SavePresets error = %v, want ErrPresetFieldsControl", err)
		}
		if strings.ContainsRune(err.Error(), '\u2029') {
			t.Fatalf("save refusal echoes the key: %q", err)
		}
		if _, statErr := os.Stat(st.Path); !os.IsNotExist(statErr) {
			t.Fatalf("refused save wrote a file: %v", statErr)
		}
	})
}

func TestP20TraversalRefusalNamesFieldAndRuleOnly(t *testing.T) {
	home, stderr := ladderEnv(t)
	path := "../outside/secret"
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{Name: "nightly", Root: path}}, DefaultPreset: "nightly"})
	_, err := buildRunConfig(ladderFlags(stderr), "p")
	if !errors.Is(err, ErrPresetPathTraversal) {
		t.Fatalf("error = %v, want ErrPresetPathTraversal", err)
	}
	if !strings.HasPrefix(err.Error(), "preset root refused: ") || !strings.Contains(err.Error(), ".. path components") {
		t.Fatalf("refusal does not name field and rule: %q", err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("refusal echoes path: %q", err)
	}
}

func TestP20MaskSlotStillMasksOperandPlant(t *testing.T) {
	plant := "sk_" + "live_1234567890abcdef"
	if got := providers.MaskSlot(plant); got == plant || !strings.Contains(got, "****") {
		t.Fatalf("MaskSlot fixture is not in-class: %q", got)
	}
}

func TestP20StripEchoControlsAtDefaultsSink(t *testing.T) {
	if got := stripEchoControls("read,\u2028forged\u2029line\u200d"); got != "read,forgedline" {
		t.Fatalf("stripEchoControls = %q", got)
	}
	if got := stripEchoControls("read,search"); got != "read,search" {
		t.Fatalf("benign value changed: %q", got)
	}
}
