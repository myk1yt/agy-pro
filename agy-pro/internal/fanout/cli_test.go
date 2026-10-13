package fanout

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
)

// S-8: --tools parsing is fail-closed. A "restrictive-looking" value must not
// silently grant write/exec through a substring match ("nowrite" used to
// enable write_file).
func TestParseToolTokensFailClosed(t *testing.T) {
	cases := []struct {
		spec    string
		want    string
		errWant bool
	}{
		{spec: "read,search", want: "read,search"},
		{spec: "read,search,write,exec", want: "read,search,write,exec"},
		{spec: "exec,write", want: "write,exec"}, // canonical order
		{spec: "read, SEARCH ,write", want: "read,search,write"},
		{spec: "read,read", want: "read"}, // dedupe
		{spec: "", want: ""},
		{spec: "read,search,nowrite", errWant: true},
		{spec: "noexec", errWant: true},
		{spec: "exec-disabled", errWant: true},
		{spec: "read,search,write_file", errWant: true},
		{spec: "read;search", errWant: true},
	}
	for _, tc := range cases {
		got, err := ParseToolTokens(tc.spec)
		if tc.errWant {
			if err == nil {
				t.Errorf("ParseToolTokens(%q) = %q; want error", tc.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseToolTokens(%q) unexpected error: %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseToolTokens(%q) = %q; want %q", tc.spec, got, tc.want)
		}
	}
}

// S-8: the CLI surface rejects unknown tokens before anything else runs.
func TestRunRejectsUnknownToolToken(t *testing.T) {
	var out, errw bytes.Buffer
	code := Run([]string{"-tools", "read,search,nowrite", "-prompt", "x"}, nil, &out, &errw)
	if code != 2 {
		t.Fatalf("exit code = %d; want 2 (usage error)", code)
	}
	if !strings.Contains(errw.String(), `unknown tool "nowrite"`) {
		t.Errorf("expected the unknown-token error, got %q", errw.String())
	}
	if strings.Contains(out.String(), "Swarm") {
		t.Errorf("no swarm output must be produced on a usage error:\n%s", out.String())
	}

	// The default tool spec still parses.
	if _, err := ParseToolTokens("read,search"); err != nil {
		t.Errorf("default --tools rejected: %v", err)
	}
}

// A1 (contract §2.3): --max-native used to be taken verbatim, so
// `--max-native 100` widened the native-seat pool past the 1..8 clamp that only
// the state.json path applied. The flag path now routes through the same
// mode.ClampNativeSeatConcurrency choke point.
func TestBuildRunConfigClampsMaxNative(t *testing.T) {
	// A private AGY_PRO_HOME keeps the state.json branch deterministic: a
	// missing state file means "default 2".
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)

	cases := []struct {
		flag int
		want int
	}{
		{100, 8}, // absurd high value clamps to the contract maximum
		{9, 8},
		{8, 8}, // upper bound passes through
		{3, 3}, // in-range value passes through
		{1, 1}, // lower bound passes through
		{0, 2}, // 0 = "not set" -> state path -> default
		{-5, 2},
	}
	for _, tc := range cases {
		cfg, err := buildRunConfig(fanoutFlags{maxNative: tc.flag}, "")
		if err != nil {
			t.Fatalf("buildRunConfig(max-native=%d): %v", tc.flag, err)
		}
		if cfg.MaxNativeConcurrency != tc.want {
			t.Errorf("--max-native %d bound MaxNativeConcurrency = %d; want %d", tc.flag, cfg.MaxNativeConcurrency, tc.want)
		}
	}

	// Precedence: the flag beats a stored value, and the stored value is used
	// (clamped) when the flag is absent.
	if err := os.WriteFile(filepath.Join(home, "state.json"),
		[]byte(`{"current_mode":"full","native_seat_concurrency":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := buildRunConfig(fanoutFlags{maxNative: 3}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != 3 {
		t.Errorf("flag 3 over stored 5 = %d; want 3 (flag wins)", cfg.MaxNativeConcurrency)
	}
	cfg, err = buildRunConfig(fanoutFlags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != 5 {
		t.Errorf("no flag over stored 5 = %d; want 5 (state.json path)", cfg.MaxNativeConcurrency)
	}
	cfg, err = buildRunConfig(fanoutFlags{maxNative: 99}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != 8 {
		t.Errorf("flag 99 over stored 5 = %d; want 8 (clamped)", cfg.MaxNativeConcurrency)
	}
	// A corrupt state file must not widen anything: the default applies.
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = buildRunConfig(fanoutFlags{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxNativeConcurrency != mode.DefaultNativeSeatConcurrency {
		t.Errorf("corrupt state = %d; want the default %d", cfg.MaxNativeConcurrency, mode.DefaultNativeSeatConcurrency)
	}
}

// A3: --max-native precedence and the --seats/--n override are asserted at
// Run() level (the CLI surface, not just the buildRunConfig choke point), by
// replacing the engine seam with a recorder. The exit-2 gates themselves are
// covered by TestRunSeatSpecGatesExitTwo.
func TestRunFlagSurfaceBindsEngineConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)

	var got []RunConfig
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		got = append(got, cfg)
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	run := func(args ...string) RunConfig {
		t.Helper()
		var out, errw bytes.Buffer
		full := append([]string{"-prompt", "p", "-json"}, args...)
		if code := Run(full, nil, &out, &errw); code != 0 {
			t.Fatalf("Run(%v) exit = %d, stderr = %s", full, code, errw.String())
		}
		return got[len(got)-1]
	}

	// --seats wins over --n at Run() level: 2 seat specs, not 7.
	cfg := run("-seats", "native:m1,prov/m2", "-n", "7")
	if cfg.N != 2 {
		t.Errorf("N = %d; want 2 (--seats overrides --n 7)", cfg.N)
	}

	// --max-native precedence at Run() level: flag > state.json > default.
	if cfg := run("-max-native", "100"); cfg.MaxNativeConcurrency != 8 {
		t.Errorf("MaxNativeConcurrency = %d; want 8 (clamped from 100)", cfg.MaxNativeConcurrency)
	}
	if cfg := run("-max-native", "0"); cfg.MaxNativeConcurrency != 2 {
		t.Errorf("MaxNativeConcurrency = %d; want 2 (default)", cfg.MaxNativeConcurrency)
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"),
		[]byte(`{"current_mode":"full","native_seat_concurrency":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg := run("-max-native", "3"); cfg.MaxNativeConcurrency != 3 {
		t.Errorf("MaxNativeConcurrency = %d; want 3 (flag beats state 5)", cfg.MaxNativeConcurrency)
	}
	if cfg := run(); cfg.MaxNativeConcurrency != 5 {
		t.Errorf("MaxNativeConcurrency = %d; want 5 (state path)", cfg.MaxNativeConcurrency)
	}
	if cfg := run("-max-native", "99"); cfg.MaxNativeConcurrency != 8 {
		t.Errorf("MaxNativeConcurrency = %d; want 8 (clamped)", cfg.MaxNativeConcurrency)
	}
}

// A3: the exit-2 gates of the fanout CLI surface, driven at the Run() entry
// point (the flag path used to be untested below --help/--tools).
func TestRunSeatSpecGatesExitTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "seats+models mutual exclusion",
			args: []string{"--seats", "native:gemini-x", "--models", "prov/m"},
			want: "--seats and --models cannot be combined",
		},
		{
			name: "seat spec parse error",
			args: []string{"--seats", "native:"},
			want: `"native:" prefix requires a model id`,
		},
		{
			name: "seat spec with slash in native id",
			args: []string{"--seats", "native:prov/model"},
			want: `native model id must not contain "/"`,
		},
		{
			name: "empty seat token",
			args: []string{"--seats", "native:a,,native:b"},
			want: "empty seat token",
		},
		{
			name: "unknown tool token still gated",
			args: []string{"--tools", "read,nowrite"},
			want: `unknown tool "nowrite"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errw bytes.Buffer
			code := Run(tc.args, nil, &out, &errw)
			if code != 2 {
				t.Fatalf("exit code = %d; want 2 (usage error)", code)
			}
			if !strings.Contains(errw.String(), tc.want) {
				t.Errorf("stderr = %q; want it to contain %q", errw.String(), tc.want)
			}
			if strings.Contains(out.String(), "Swarm") {
				t.Errorf("no swarm output must be produced on a usage error:\n%s", out.String())
			}
		})
	}
}

// A3: --seats overrides --n for the seat count (the spec list is the truth),
// and the rest of the flag surface still lands in the RunConfig.
func TestBuildRunConfigSeatsOverrideNAndFlagSurface(t *testing.T) {
	cfg, err := buildRunConfig(fanoutFlags{
		task:     "review",
		n:        7,
		seats:    "native:gemini-a,prov-a/model-b",
		root:     "/ws",
		tools:    "read,search,write",
		maxTurns: 4,
		timeout:  90,
		out:      "/tmp/out",
	}, "the prompt")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if cfg.N != 2 {
		t.Errorf("N = %d; want 2 (--seats wins over --n 7)", cfg.N)
	}
	if len(cfg.Seats) != 2 || cfg.Seats[0].Kind != SeatKindNative || cfg.Seats[1].Kind != SeatKindProvider {
		t.Errorf("seats = %+v; want the two parsed specs in order", cfg.Seats)
	}
	if cfg.Models != nil {
		t.Errorf("models = %v; want nil when --seats is used", cfg.Models)
	}
	if cfg.Task != "review" || cfg.Root != "/ws" || cfg.MaxTurns != 4 || cfg.TimeoutSec != 90 || cfg.OutDir != "/tmp/out" {
		t.Errorf("flag surface lost: %+v", cfg)
	}
	if cfg.Tools != "read,search,write" || cfg.Prompt != "the prompt" {
		t.Errorf("tools/prompt = %q/%q; want read,search,write/the prompt", cfg.Tools, cfg.Prompt)
	}

	// The legacy --models path keeps N as given and splits the list.
	cfg, err = buildRunConfig(fanoutFlags{n: 4, models: "prov/m1, prov/m2 ,"}, "p")
	if err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	if cfg.N != 4 || len(cfg.Models) != 2 || cfg.Models[1] != "prov/m2" {
		t.Errorf("legacy models path = N:%d models:%v; want N:4 [prov/m1 prov/m2]", cfg.N, cfg.Models)
	}
}
