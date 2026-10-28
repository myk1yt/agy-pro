// Package hybrid's P1 front door (profile "picker coexistence").
//
// This file wires the proven catalog core (catalog.go, resolver.go) into a live
// per-session mesh and execs the REAL agy behind it (design §2 P1,
// docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md):
//
//   - resolve the real agy (realagy.Resolve; AGY_PRO_REAL_AGY > PATH scan >
//     default path, full path everywhere, never PATH's first agy which is an
//     11-byte mock stub on this machine);
//   - WIRECHECK GATE: a NOGO verdict (or a P1 profile that is not ProfileOK)
//     falls back to native with a clear notice, never a half-configured session;
//   - build the mesh config (strict-mode install nonce, the catalog bytes, the
//     resolver seam, the registry redactor, an audit path under the agy-pro
//     home) and start the per-spawn mesh (mesh.Start), health-gated;
//   - exec the real agy as a CHILD with CLOUD_CODE_URL=<mesh URL> and a scrubbed
//     environment (no provider keys, no GEMINI_API_KEY injection); the child's
//     lifetime is tied to the mesh (mesh.RunChild).
//
// The launch-mechanics helpers (native fallback, exit-code mapping, stdio
// defaults) live in launch.go.
//
// Stdlib + agypro/internal/{mesh,paths,providers,realagy,wireadp,wirecheck}. It
// does NOT import bridge or fanout.
package hybrid

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"agypro/internal/mesh"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/realagy"
	"agypro/internal/wireadp"
	"agypro/internal/wirecheck"
)

// Seams (contract §2.1 style): tests replace these so the front door can be
// driven without scanning or execing the real agy binary.
var (
	// wirecheckCheck runs the drift-canary gate against a PINNED real-agy path.
	// FIX-55: LaunchP1 resolves the binary ONCE and scans the same path it will
	// exec, so there is no TOCTOU where the gate scans one binary and the exec
	// runs another. The default runs wirecheck.CheckWith with a resolve closure
	// returning the pinned path (wirecheck.Check would re-resolve internally).
	wirecheckCheck = func(pinnedPath string) wirecheck.Report {
		return wirecheck.CheckWith(
			func() (string, error) { return pinnedPath, nil },
			os.ReadFile,
			p1VersionProbe,
		)
	}
	// realagyResolve resolves the real agy binary.
	realagyResolve = realagy.Resolve
	// meshStart binds the per-spawn mesh.
	meshStart = func(cfg mesh.Config) (*mesh.Mesh, error) { return mesh.Start(cfg) }
	// meshRunChild ties the child's lifetime to the mesh.
	meshRunChild = func(ctx context.Context, m *mesh.Mesh, cmd *exec.Cmd) error {
		return m.RunChild(ctx, cmd)
	}
)

// p1VersionToken mirrors wirecheck's version-token rule so a decorated
// `--version` banner ("agy 1.3.5 (build 7)") still yields the pinned token.
// Pinned against wirecheck's literal in TestP1VersionTokenMirrorsWirecheck
// (FIX-62); a cross-package export of wirecheck's rule is the follow-up that
// would pin the other direction.
var p1VersionToken = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.-]+)?`)

// versionProbeOutputCap mirrors wirecheck's versionOutputCap (4 KiB): a chatty
// `--version` banner is a probe failure, never a truncated parse (FIX-62).
const versionProbeOutputCap = 4 << 10

// cappedOutput captures up to versionProbeOutputCap bytes and records overflow,
// mirroring wirecheck's cappedBuffer. Write always reports the full length
// consumed so the probed child never blocks on a "full" pipe.
type cappedOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > versionProbeOutputCap {
		c.overflow = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

// p1VersionProbe runs `<path> --version` under the exported default-deny env
// filter (wirecheck.FilterChildEnv) and returns the parsed version token, with
// the same timeout and 4 KiB output cap wirecheck's own probe uses (FIX-62). It
// is the runVersion seam the pinned-path gate (FIX-55) hands to
// wirecheck.CheckWith; wirecheck exposes no path-taking Check, so the probe is
// replicated here using its exported env filter and the same token rule.
func p1VersionProbe(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = wirecheck.FilterChildEnv(nil)
	out := &cappedOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s --version failed: %v", filepath.Base(path), err)
	}
	if out.overflow {
		return "", fmt.Errorf("%s --version produced more than %d bytes of output", filepath.Base(path), versionProbeOutputCap)
	}
	s := strings.TrimSpace(out.buf.String())
	if s == "" {
		return "", fmt.Errorf("%s --version produced no version output", filepath.Base(path))
	}
	if m := p1VersionToken.FindString(s); m != "" {
		return m, nil
	}
	return s, nil
}

// LaunchOptions configures one P1 launch.
type LaunchOptions struct {
	// RealAgyPath is the resolved real-agy binary. When empty, LaunchP1
	// resolves it with realagy.Resolve() (the production front-door contract:
	// AGY_PRO_REAL_AGY > PATH scan > default path, never PATH's first agy).
	RealAgyPath string
	// Args are forwarded verbatim to the real-agy child.
	Args []string
	// Stdin/Stdout/Stderr wire the child's stdio. Nil defaults to the parent's
	// os.Stdin/os.Stdout/os.Stderr, giving full TUI fidelity: the TUI opens only
	// when the USER runs agy; this path never opens a window itself.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// ChildEnv is extra environment appended to the child (both the mesh path
	// and the native fallback). Production leaves it nil; tests use it to drive
	// a stub child without polluting the parent environment that the wirecheck
	// version probe reads.
	ChildEnv []string
}

// LaunchP1 runs the P1 (picker coexistence) front door. It resolves the real
// agy, gates on wirecheck, starts the per-session /v1internal mesh, and execs
// the real agy with CLOUD_CODE_URL pointing at the mesh. On a wirecheck NOGO or
// with zero enabled externals it prints a clear notice and FALLS BACK TO NATIVE
// (exec real agy with no mesh, no CLOUD_CODE_URL), never a half-configured
// session.
//
// Exit-code map (see p1ExitCode):
//   - the child's own exit code passes through unchanged;
//   - a mesh that dies mid-session yields ExitMeshDied (the mesh already wrote
//     the one visible fail-fast line, so no second line is printed);
//   - a refuse-to-launch / start failure returns exit 1 with the error.
func LaunchP1(opts LaunchOptions) (int, error) {
	realAgy := strings.TrimSpace(opts.RealAgyPath)
	if realAgy == "" {
		resolved, err := realagyResolve()
		if err != nil {
			return 1, fmt.Errorf("hybrid P1: cannot resolve the real agy binary: %w", err)
		}
		realAgy = resolved
	}

	errw := stderrOr(opts.Stderr)

	// WIRECHECK GATE (design §3): a NOGO verdict or a P1 profile that is not
	// ProfileOK falls back to native. A GO with version DRIFT proceeds and the
	// drift is surfaced in the notice. The gate scans the SAME binary we will
	// exec (FIX-55: realAgy is resolved once above and pinned here).
	report := wirecheckCheck(realAgy)
	if report.Verdict() == wirecheck.VerdictNOGO || !report.ProfileOK(ProfileP1.WirecheckName()) {
		reg, _ := providers.Load() // best-effort: scrub key names even on fallback
		return launchNativeFallback(realAgy, opts, reg, nogoNotice(report))
	}
	if report.VersionDrift {
		fmt.Fprintf(errw, "[agy-pro] wirecheck: version drift (binary reports %q, pinned %q); proceeding with P1\n",
			report.Version, wirecheck.PinnedVersion)
	}

	// Registry: zero enabled externals means the mesh has nothing to serve, so
	// fall back to native with a clear notice.
	reg, err := providers.Load()
	if err != nil {
		return launchNativeFallback(realAgy, opts, nil,
			fmt.Sprintf("the provider registry could not be read (%v); the mesh has nothing to serve", err))
	}
	if countEnabledModels(reg) == 0 {
		return launchNativeFallback(realAgy, opts, reg,
			"no enabled external providers; the mesh has nothing to serve")
	}

	// FIX-48: build the catalog ONCE at launch. A build error (a deny-list
	// collision, etc.) is a deterministic, launch-time failure: fall back to
	// native with a notice, never start a live mesh whose catalog would 500
	// every request mid-session (a half-configured session).
	catalogBytes, catErr := BuildFetchAvailableModels(reg, p1CatalogOptions(errw))
	if catErr != nil {
		return launchNativeFallback(realAgy, opts, reg,
			fmt.Sprintf("the model catalog could not be built (%v); falling back rather than start a mesh whose catalog would fail every request", catErr))
	}

	home, err := paths.AgyProHome()
	if err != nil {
		return 1, fmt.Errorf("hybrid P1: cannot resolve the agy-pro home: %w", err)
	}
	cfg, err := buildP1Config(reg, home, catalogBytes)
	if err != nil {
		return 1, err
	}

	m, err := meshStart(mesh.Config{
		Config:        cfg,
		HealthTimeout: mesh.DefaultHealthTimeout,
		ErrWriter:     errw,
	})
	if err != nil {
		return 1, fmt.Errorf("hybrid P1: refusing to launch, the mesh could not start: %w", err)
	}
	defer func() { _ = m.Stop() }()

	// Front-door re-check before any environment is injected (refuse-to-launch
	// names the URL/port).
	if err := m.EnsureHealthy(context.Background()); err != nil {
		return 1, err
	}

	// Child environment: scrubbed (no provider keys, no gateway vars) PLUS the
	// CLOUD_CODE_URL override. NO GEMINI_API_KEY injection on this path: the
	// CodeAssist client authenticates via agy's keyring bearer, which the mesh
	// receives passively and never forwards upstream. opts.ChildEnv is appended
	// BEFORE CLOUD_CODE_URL so the mesh URL always wins under Go 1.22 last-wins
	// exec dedup (FIX-5 parity with P2). The scrub uses ChildKeyScrubNames (the
	// registry names UNION the known presets), so an exported preset key for a
	// provider absent from providers.json cannot ride into the P1 mesh child
	// (FIX-4 / R5AF2-4 parity).
	childEnv := realagy.CleanChildEnvWithBase(os.Environ(), ChildKeyScrubNames(reg))
	childEnv = append(childEnv, opts.ChildEnv...)
	childEnv = append(childEnv, "CLOUD_CODE_URL="+m.URL())

	cmd := exec.Command(realAgy, opts.Args...)
	cmd.Env = childEnv
	cmd.Stdin = stdinOr(opts.Stdin)
	cmd.Stdout = stdoutOr(opts.Stdout)
	cmd.Stderr = stderrOr(opts.Stderr)

	return p1ExitCode(meshRunChild(context.Background(), m, cmd))
}

// buildP1Config assembles the mesh's wireadp.Config for one P1 spawn:
//   - AuthKey = a fresh per-spawn install nonce (32 random bytes, hex). STRICT
//     MODE IS MANDATORY: a nonce failure is an error, never an empty key.
//   - CatalogFunc serves the PRE-BUILT catalog bytes (built once at launch,
//     FIX-48, so a deny-list collision fails before the mesh starts, not
//     mid-session).
//   - ResolveModel = the registry allow-list seam (RegistryResolver).
//   - Redact = the registry redactor.
//   - AuditPath = <home>/logs/wireadp-<ts>.jsonl (the directory is created).
//   - Generate = nil (wireadp installs its default generation handler).
func buildP1Config(reg *providers.Registry, home string, catalogBytes []byte) (wireadp.Config, error) {
	nonce, err := newInstallNonce()
	if err != nil {
		return wireadp.Config{}, fmt.Errorf("hybrid P1: cannot generate the install nonce: %w", err)
	}
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		// FIX-6/FIX-65: name the class, not the absolute path (the agysettings
		// maskedPath discipline), exactly as buildP2Config does; the wrapped OS
		// error is the deferred deeper location-exemption fix.
		return wireadp.Config{}, fmt.Errorf("hybrid P1: cannot create the audit log directory (the logs directory): %w", err)
	}
	auditPath := filepath.Join(logsDir, "wireadp-"+time.Now().UTC().Format("20060102_150405")+".jsonl")
	// FIX-45: the CodeAssist-path child presents only its keyring bearer (proven
	// live in the G4 report), and no shim-controlled channel can carry the install
	// nonce on /v1internal, so the strict nonce there made the mesh unreachable
	// (every request 401'd at loadCodeAssist). Opt into the documented passive-
	// receipt posture on /v1internal (a non-nil false): loopback + ephemeral port
	// + 60 req/min + names-only audit stay active and the bearer is received and
	// discarded. The strict nonce still guards /v1beta and /healthz.
	passiveV1Internal := false
	return wireadp.Config{
		Registry:              reg,
		Redact:                reg.Redactor(),
		AuthKey:               nonce,
		CatalogFunc:           func() ([]byte, error) { return catalogBytes, nil },
		AuditPath:             auditPath,
		ResolveModel:          RegistryResolver(reg),
		Generate:              nil,
		RequireV1InternalAuth: &passiveV1Internal,
	}, nil
}

// p1CatalogOptions returns the production catalog options: the reserved-enum
// set plus a masked, control-char-stripped stderr sink for the fail-closed drop
// reasons (FIX-50: the cross-provider duplicate drop must be observable in
// production, not silently discarded as it was when Logf was nil). The sink
// masks operands (defense in depth; catalog.go already masks its own operands)
// and strips control characters (FIX-51 hygiene).
func p1CatalogOptions(errw io.Writer) CatalogOptions {
	return CatalogOptions{
		ReservedEnums: DefaultReservedEnums(),
		Logf: func(format string, args ...any) {
			line := stripControlChars(providers.MaskOperand(fmt.Sprintf(format, args...)))
			fmt.Fprintln(errw, "[agy-pro] catalog: "+line)
		},
	}
}

// DefaultCatalogOptions returns the reserved-enum catalog options with NO sink.
// The bytes are identical to p1CatalogOptions' (Logf only affects drop-reason
// logging, never the payload), so tests use it for byte-equality assertions
// against the mesh's served catalog.
func DefaultCatalogOptions() CatalogOptions {
	return CatalogOptions{ReservedEnums: DefaultReservedEnums()}
}

// newInstallNonce returns 32 crypto/rand bytes as a 64-char hex string. A
// crypto/rand failure is returned, never swallowed: an empty AuthKey would put
// the mesh in permissive mode, which the front door must never reach.
func newInstallNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand unavailable: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// nogoNotice renders the clear fallback notice for a NOGO report. The reasons
// are rendered location-safely through the shared renderWirecheckReasons (FIX-65
// / R5AF2-5): a ScanErr that embeds the real-agy absolute path is withheld, not
// interpolated.
func nogoNotice(report wirecheck.Report) string {
	msg := "wirecheck NOGO for profile P1; refusing to start the mesh"
	if reasons := report.Reasons(); len(reasons) > 0 {
		msg += ": " + renderWirecheckReasons(reasons)
	}
	return msg
}

// countEnabledModels counts enabled model entries across all providers. A nil
// or empty registry is zero.
func countEnabledModels(reg *providers.Registry) int {
	if reg == nil {
		return 0
	}
	n := 0
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if m.IsEnabled() {
				n++
			}
		}
	}
	return n
}
