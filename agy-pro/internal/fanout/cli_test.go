package fanout

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
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

// Planted-key sweep for the fanout CLI surface (D1/D3 class, mirrors cmd/agy's
// TestWrapperPlantedKeySweep): prompt-file errors, seat-spec token errors and
// tool-token errors must never echo a planted key raw, while benign operands
// stay readable. sk- plants are caught by the generic credential patterns, the
// AWS-secret-shaped plant (contiguous base64 with "/") by the operand-seeded
// redactor, including through the case-folding tool-token parser.
func TestFanoutPlantedKeySweep(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	const (
		skPlant  = "sk-planted-c2-sweep-key-0123456789abcdef"
		skMask   = "sk-pl****"
		awsPlant = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		awsMask  = "wJalr****"
		// E1 plants: a JWT (3-segment dotted token) and an ASIA-prefixed AWS
		// access key id (the credentialShapeRe sibling of AKIA; F3 added the
		// ASIA twin to the generic redactor pattern, so flag echoes of the id
		// now carry the full marker instead of the first-5 mask).
		jwtPlant  = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
		jwtMask   = "eyJhb****"
		asiaPlant = "ASIAIOSFODNN7EXAMPLE"
	)

	type site struct {
		name     string
		args     []string
		wantCode int
		want     string // substring that must survive (mask or benign text)
		wantNot  string // substring that must not appear ("" = unchecked)
	}
	sites := []site{
		{
			name: "prompt-file sk- plant", args: []string{"--prompt-file", skPlant},
			wantCode: 1, want: skMask,
		},
		{
			name: "prompt-file AWS plant", args: []string{"--prompt-file", awsPlant},
			wantCode: 1, want: awsMask,
		},
		{
			name: "prompt-file benign path stays readable", args: []string{"--prompt-file", "./prompts/missing.md"},
			wantCode: 1, want: "./prompts/missing.md", wantNot: "[REDACTED]",
		},
		{
			name: "seats native sk- plant", args: []string{"--seats", "native:" + skPlant + "/x"},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			name: "seats native AWS plant", args: []string{"--seats", "native:" + awsPlant + "/x"},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			// F-alpha: a decorated operand whose echo carries interior
			// whitespace ("native: <aws>") seeds through the shared squash rule.
			name: "seats native AWS plant with whitespace", args: []string{"--seats", "native: " + awsPlant},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			// G1: multi-token --seats — the seat-spec validator quotes the
			// INDIVIDUAL token, so the whole-value seed cannot match the
			// sentence; per-token seeds (providers.FlagSeedOperands) close it
			// (r6 CONFIRMED across 5 reviewer instances).
			name: "seats multi-token native AWS plant", args: []string{"--seats", "1,native:" + awsPlant},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			name: "seats multi-token 1-<AWS> plant", args: []string{"--seats", "1,1-" + awsPlant},
			wantCode: 1, wantNot: awsPlant,
		},
		{
			name: "seats benign token error stays readable", args: []string{"--seats", "native:"},
			wantCode: 2, want: `"native:" prefix requires a model id`, wantNot: "[REDACTED]",
		},
		{
			// agenttools.ParseToolTokens masks the unknown token itself (B2/U8);
			// the CLI must not un-mask or duplicate it.
			name: "tools sk- plant", args: []string{"--tools", "read," + skPlant},
			wantCode: 2, want: skMask,
		},
		{
			// E9: agenttools.ParseToolTokens masks the unknown token in its
			// ORIGINAL case (the folded copy destroyed shape prefixes and let
			// separator-carrying keys echo); the CLI must not un-mask it.
			name: "tools AWS plant (original-case echo)", args: []string{"--tools", "read," + awsPlant},
			wantCode: 2, want: awsMask,
		},
		{
			name: "tools benign token error stays readable", args: []string{"--tools", "read,nowrite"},
			wantCode: 2, want: `unknown tool "nowrite"`, wantNot: "[REDACTED]",
		},
		{
			// F-alpha: the uppercase-only decorated class (r5 N2). A
			// "1-<aws>" / "1-<ASIA>" token is an unknown tool whose source echo
			// is raw (the classifier keeps the decorated form readable), so the
			// composite sentence must be seeded by the shared rule.
			name: "tools 1-<AWS> plant", args: []string{"--tools", "1-" + awsPlant},
			wantCode: 2, want: "1-wJa****",
		},
		{
			name: "tools 1-<ASIA> plant", args: []string{"--tools", "1-" + asiaPlant},
			wantCode: 2, want: "1-ASI****",
		},
		{
			// D8: the stdlib invalid-value echo for -timeout routes through the
			// echo-line redactor (quoted segment) and the generic pass.
			name: "timeout sk- plant", args: []string{"-prompt", "x", "-timeout", skPlant},
			wantCode: 2, want: skMask,
		},
		{
			name: "timeout AWS plant", args: []string{"-prompt", "x", "-timeout", awsPlant},
			wantCode: 2, want: awsMask,
		},
		{
			name: "timeout benign value stays readable", args: []string{"-prompt", "x", "-timeout", "abc"},
			wantCode: 2, want: `invalid value "abc"`,
		},
		{
			// E1: the stdlib echoes an undefined flag name unquoted, so the
			// quoted-segment redactor cannot see it; the unquoted-run scan
			// masks the 16+ rune base64 run / JWT-shaped token.
			name: "undefined flag AWS plant", args: []string{"--" + awsPlant},
			wantCode: 2, want: awsMask,
		},
		{
			name: "undefined flag JWT plant", args: []string{"--" + jwtPlant},
			wantCode: 2, want: jwtMask,
		},
		{
			// F3: the ASIA twin in the generic redactor pattern now masks the
			// id in the whole line before the unquoted scan sees it (the AKIA
			// class has always rendered this way), so the echo carries the full
			// marker; the first-5 form stays pinned in TestE2 below.
			name: "undefined flag ASIA plant", args: []string{"--" + asiaPlant},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			// E1: "bad flag syntax: <arg>" echoes the raw argument unquoted.
			name: "bad flag syntax AWS plant", args: []string{"----" + awsPlant},
			wantCode: 2, want: awsMask,
		},
		{
			name: "bad flag syntax JWT plant", args: []string{"----" + jwtPlant},
			wantCode: 2, want: jwtMask,
		},
		{
			// F3: same as the undefined-flag row above — the generic pattern
			// redacts the ASIA id in the whole line first.
			name: "bad flag syntax ASIA plant", args: []string{"----" + asiaPlant},
			wantCode: 2, want: "[REDACTED]",
		},
		{
			// E2: a hash-named relative path is not location-shaped, so the
			// old fragment extractor seeded its 16-rune hash and the composite
			// sentence printed "[REDACTED].md". Whole-operand seeding keeps it
			// readable in both the slot and the sentence.
			name: "prompt-file hash-named path stays readable", args: []string{"--prompt-file", "prompts/3f2a8b9c1d2e3f4a.md"},
			wantCode: 1, want: "prompts/3f2a8b9c1d2e3f4a.md", wantNot: "[REDACTED]",
		},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			var out, errw bytes.Buffer
			code := Run(s.args, nil, &out, &errw)
			if code != s.wantCode {
				t.Fatalf("exit code = %d; want %d\nstdout:\n%s\nstderr:\n%s", code, s.wantCode, out.String(), errw.String())
			}
			for _, plant := range []string{skPlant, awsPlant, jwtPlant, asiaPlant} {
				if strings.Contains(out.String(), plant) || strings.Contains(errw.String(), plant) {
					t.Errorf("fanout output leaks the planted key:\nstdout:\n%s\nstderr:\n%s", out.String(), errw.String())
				}
			}
			if !strings.Contains(out.String()+errw.String(), s.want) {
				t.Errorf("output missing %q:\nstdout:\n%s\nstderr:\n%s", s.want, out.String(), errw.String())
			}
			if s.wantNot != "" && strings.Contains(out.String()+errw.String(), s.wantNot) {
				t.Errorf("output must not contain %q:\nstdout:\n%s\nstderr:\n%s", s.wantNot, out.String(), errw.String())
			}
		})
	}
}

// E2: composite-error seeding follows the ONE shared rule
// (providers.SeedEligible / providers.MaskSentence, F-alpha) and mirrors
// cmd/agy's TestE2SeedEligibilityWholeOperandOnly. The old run extractor
// seeded every separator-free 16+ rune fragment, which mangled benign tokens
// inside composite sentences ("native:anthropic/claude-sonnet-4-5/x" printed
// "native:[REDACTED]-sonnet-4-5/x", "prompts/3f2a8b9c1d2e3f4a.md" printed
// "[REDACTED].md"). Seeds now require the WHOLE operand to be secret-shaped;
// the trimmed form of every seed stays armed for parsers that echo the operand
// without surrounding whitespace.
func TestE2SeedEligibilityWholeOperandOnly(t *testing.T) {
	awsPlant := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	asiaPlant := "ASIAIOSFODNN7EXAMPLE"
	asiaMask := "ASIAI****"

	// AWS whole-operand secrets stay masked (classifier verdict seeds the
	// redactor, which renders the seed as [REDACTED]).
	msg := "open " + awsPlant + ": The system cannot find the file specified."
	if got := maskOperandSentence(msg, awsPlant); strings.Contains(got, awsPlant) || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("AWS whole-operand must stay masked:\n%s", got)
	}

	// Benign operands stay readable, fragment seeds gone.
	for _, op := range []string{
		"native:anthropic/claude-sonnet-4-5/x",
		"prompts/3f2a8b9c1d2e3f4a.md",
		"./prompts/3f2a8b9c1d2e3f4a.md",
		"/var/lib/agy/providers.json",
		"https://acme.example/v1",
		// F-alpha benign pins: dotted names and filenames the JWT/squash shapes
		// must not claim (r5 N1 over-mask class, MyReport filename class).
		"config.dev.override",
		"claude-3.5.sonnet",
		"MyReport2024FINAL.pdf",
	} {
		msg := fmt.Sprintf("seat %d: %q: native model id must not contain %q", 1, op, "/")
		got := maskOperandSentence(msg, op)
		if !strings.Contains(got, op) {
			t.Errorf("benign operand %q must stay readable:\n%s", op, got)
		}
		if strings.Contains(got, "[REDACTED]") {
			t.Errorf("benign operand %q must not trigger any redaction:\n%s", op, got)
		}
	}

	// The classifier's first-5 form for the ASIA twin stays pinned: a bare
	// operand echo renders first-5 + **** (U8) even though the generic
	// redactor pattern (F3) renders the full marker inside sentences.
	if got := providers.MaskOperand(asiaPlant); got != asiaMask {
		t.Errorf("MaskOperand(ASIA id) = %q; want %q", got, asiaMask)
	}

	// JWT-shaped operands are masked: the eyJ header classifies, the shape
	// rule covers headers the classifier cannot see.
	for _, jwt := range []string{
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		"abcdefghijklmnop.qrstuvwxyz0123.456789",
	} {
		msg := fmt.Sprintf("invalid selection token %q", jwt)
		got := maskOperandSentence(msg, jwt)
		if strings.Contains(got, jwt) {
			t.Errorf("JWT operand must be masked:\n%s", got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("JWT operand should carry the redaction marker:\n%s", got)
		}
	}

	// Decorated secrets keep the D1/D3 coverage: the whole operand seeds, so
	// "1-<aws-secret>" and "native:<aws-secret>/x" stay masked in composite
	// sentences without any fragment seed.
	for _, op := range []string{"1-" + awsPlant, "native:" + awsPlant + "/x"} {
		msg := fmt.Sprintf("invalid selection token %q", op)
		got := maskOperandSentence(msg, op)
		if strings.Contains(got, awsPlant) {
			t.Errorf("decorated AWS operand %q must stay masked:\n%s", op, got)
		}
	}

	// F-alpha regression pins (r5 N2): decorated operands whose inner run is
	// NOT mixed-case-plus-digit used to print raw. The uppercase-only class
	// (ASIA id, bare upper run) and the whitespace-decorated AWS secret seed
	// through the shared squash rule now.
	for _, op := range []string{
		"1-" + asiaPlant,
		"1-ABCDEFGHIJKLMNOPQRST",
		"native: " + awsPlant,
	} {
		msg := fmt.Sprintf("invalid selection token %q", op)
		got := maskOperandSentence(msg, op)
		if strings.Contains(got, asiaPlant) || strings.Contains(got, "ABCDEFGHIJKLMNOPQRST") || strings.Contains(got, awsPlant) {
			t.Errorf("decorated operand %q must stay masked:\n%s", op, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("decorated operand %q should carry the redaction marker:\n%s", op, got)
		}
	}
}

// E4: the completion banner echoes the raw --task/--out flag values, so a
// misdirected paste in either slot must be masked on the way out while benign
// values stay readable.
func TestE4CompletionBannerMasksTaskAndOut(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		return &SummaryReport{Task: cfg.Task, NRequested: cfg.N, NCompleted: cfg.N}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	const (
		plant  = "sk-planted-c2-sweep-key-0123456789abcdef"
		masked = "sk-pl****"
	)
	var out, errw bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-task", plant, "-out", plant}, nil, &out, &errw); code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr:\n%s", code, errw.String())
	}
	if strings.Contains(out.String(), plant) {
		t.Errorf("banner leaks the planted flag value:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Task:                  "+masked) {
		t.Errorf("banner task line should carry the readable mask %q:\n%s", masked, out.String())
	}
	if !strings.Contains(out.String(), masked) {
		t.Errorf("banner artifact lines should carry the readable mask %q:\n%s", masked, out.String())
	}

	// Benign flag values stay byte-identical in the banner.
	var out2, errw2 bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-task", "review", "-out", "reports/fanout"}, nil, &out2, &errw2); code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr:\n%s", code, errw2.String())
	}
	for _, want := range []string{
		"Task:                  review",
		"  Reconciliation:      " + filepath.Join("reports/fanout", "reconciliation.md"),
		"  Machine Summary:     " + filepath.Join("reports/fanout", "summary.json"),
	} {
		if !strings.Contains(out2.String(), want) {
			t.Errorf("benign banner lost %q:\n%s", want, out2.String())
		}
	}
}

// E2-adjacent (S-7 console line): the engine error names the seat/model spec
// that failed to resolve, so a secret planted in --seats/--models that passes
// validation but fails resolution must not reach stderr. The line routes
// through the composite-error masker; benign engine errors stay readable.
func TestFanoutEngineErrorMasksSeatSpecs(t *testing.T) {
	t.Setenv(paths.EnvAgyProHome, t.TempDir())
	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		spec := ""
		switch {
		case len(cfg.Seats) > 0:
			spec = cfg.Seats[0].ModelID
		case len(cfg.Models) > 0:
			spec = cfg.Models[0]
		}
		return nil, fmt.Errorf("failed to resolve model %q: model %q not found in registry", spec, spec)
	}
	t.Cleanup(func() { executeEngine = old })

	const (
		awsPlant  = "1-wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
		asiaPlant = "1-ASIAIOSFODNN7EXAMPLE"
	)
	for _, plant := range []string{awsPlant, asiaPlant} {
		var out, errw bytes.Buffer
		if code := Run([]string{"-prompt", "p", "-seats", plant}, nil, &out, &errw); code != 1 {
			t.Fatalf("exit = %d; want 1\nstderr:\n%s", code, errw.String())
		}
		if strings.Contains(errw.String(), plant) {
			t.Errorf("engine error leaks the planted seat spec %q:\n%s", plant, errw.String())
		}
		if !strings.Contains(errw.String(), "[REDACTED]") {
			t.Errorf("engine error should carry the redaction marker for %q:\n%s", plant, errw.String())
		}
	}

	// A benign engine error stays readable (no seed, generic pass only).
	var out2, errw2 bytes.Buffer
	if code := Run([]string{"-prompt", "p", "-models", "prov/m1"}, nil, &out2, &errw2); code != 1 {
		t.Fatalf("exit = %d; want 1\nstderr:\n%s", code, errw2.String())
	}
	if !strings.Contains(errw2.String(), `failed to resolve model "prov/m1"`) {
		t.Errorf("benign engine error must stay readable:\n%s", errw2.String())
	}
	if strings.Contains(errw2.String(), "[REDACTED]") {
		t.Errorf("benign engine error must not be redacted:\n%s", errw2.String())
	}
}
