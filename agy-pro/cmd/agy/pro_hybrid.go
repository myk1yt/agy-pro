package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"agypro/internal/hybrid"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/wirecheck"
)

// Native-coexistence profile command (design §2, session 261014_0007):
//
//	agy pro hybrid on p1     persist profile P1 (picker coexistence)
//	agy pro hybrid on p2     persist profile P2 (subagent profile)
//	agy pro hybrid off       clear the persisted profile
//	agy pro hybrid status    show the effective profile + its wirecheck verdict
//	agy pro hybrid check     render the wirecheck report; exit 1 when the
//	                         effective profile is NOGO (fail closed)
//
// `on` only persists the choice and prints the honest cost line plus the launch
// instruction; the mesh front door that reads this profile is wired by the
// launcher unit. The AGY_PRO_HYBRID env override (p1|p2|off) beats the persisted
// value at launch time (hybrid.ResolveProfile).
//
// P5-1 U2 (F3(a)): `on` and `off` are state.json writers, so each presents the
// exact old -> new diff for the hybrid_profile key, requires consent (console
// y/N, or --yes on a non-interactive stdin), and re-reads the same key
// immediately before the save, refusing on drift. status/check stay read-only.

const proHybridUsage = `Usage: agy pro hybrid <on p1|on p2|off|status|check> [--yes]
  on p1 | on p2   persist the profile and print its cost line + launch instruction
  off             clear the persisted profile
  status          show the effective profile (env override vs persisted) + wirecheck verdict
  check           render the wirecheck report; exit 1 when the effective profile is NOGO
  --yes           consent to the state.json write without the console y/N prompt
                  (required when stdin is not a console)`

// wirecheckCheck is the production wirecheck entry point. Tests replace this
// seam so status/check run against a synthetic report instead of scanning the
// real agy binary (which may not resolve in a sandbox).
var wirecheckCheck = wirecheck.Check

// runHybridCmd executes `agy pro hybrid <args>` and returns the process exit
// code (0 ok, 1 runtime error or a NOGO effective profile under check, 2 usage).
func runHybridCmd(args []string) int {
	return runHybridCmdTo(args, os.Stdout, os.Stderr)
}

// runHybridCmdTo is the writer-injectable core of runHybridCmd (console stdin).
func runHybridCmdTo(args []string, out, errw io.Writer) int {
	return runHybridCmdToIn(args, os.Stdin, out, errw)
}

// runHybridCmdToIn is the reader- and writer-injectable core: the consent gate
// needs the caller's stdin, so tests drive the console y/N and pipe paths
// through this entry instead of a real terminal.
func runHybridCmdToIn(args []string, in io.Reader, out, errw io.Writer) int {
	rest, yes := splitYesFlag(args)
	if len(rest) == 0 {
		fmt.Fprintln(errw, proHybridUsage)
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(rest[0])) {
	case "on":
		if len(rest) != 2 {
			fmt.Fprintf(errw, "[agy-pro] on requires exactly one profile: p1 or p2\n%s\n", proHybridUsage)
			return 2
		}
		p, err := hybrid.ParseProfile(rest[1])
		if err != nil {
			// C2: the operand is echoed in the parse error; mask it so a
			// misdirected key paste in the profile slot never prints raw.
			fmt.Fprintf(errw, "[agy-pro] %s\n", maskOperandSentence(err.Error(), rest[1]))
			return 2
		}
		if p == hybrid.ProfileNone {
			fmt.Fprintf(errw, "[agy-pro] on requires a profile (p1 or p2), not %q\n%s\n", providers.MaskOperand(rest[1]), proHybridUsage)
			return 2
		}
		return hybridOn(in, out, errw, p, yes)
	case "off":
		if len(rest) != 1 {
			fmt.Fprintf(errw, "[agy-pro] off takes no arguments\n%s\n", proHybridUsage)
			return 2
		}
		return hybridOff(in, out, errw, yes)
	case "status":
		if len(rest) != 1 {
			fmt.Fprintf(errw, "[agy-pro] status takes no arguments\n%s\n", proHybridUsage)
			return 2
		}
		return hybridStatus(out, errw)
	case "check":
		if len(rest) != 1 {
			fmt.Fprintf(errw, "[agy-pro] check takes no arguments\n%s\n", proHybridUsage)
			return 2
		}
		return hybridCheck(out, errw)
	default:
		fmt.Fprintf(errw, "[agy-pro] unknown hybrid subcommand %q\n%s\n", providers.MaskOperand(rest[0]), proHybridUsage)
		return 2
	}
}

// hybridOn persists the profile and prints its cost line and launch instruction.
func hybridOn(in io.Reader, out, errw io.Writer, p hybrid.Profile, yes bool) int {
	// FIX-63: pre-validate the artifact THIS profile uses, so a registry that
	// profile cannot serve is caught now (refuse, profile NOT persisted) rather
	// than only at launch. An unreadable/missing registry is not a collision:
	// the launch-time gates own that case, so `on` still persists.
	if err := prevalidateProfileArtifact(p); err != nil {
		fmt.Fprintf(errw, "[agy-pro] cannot enable %s: %s; fix providers.json first (profile not saved)\n", p.Label(), err)
		return 1
	}
	// P5-1 U2: the profile is persisted state, so the exact state.json diff is
	// presented before anything is written and the same key is re-read after
	// consent (a concurrent edit refuses instead of being overwritten). The
	// artifact pre-validation above stays first: a profile whose artifact cannot
	// be built has no writable diff to approve.
	plan, err := snapshotStateScalar("hybrid_profile", p.String())
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", err)
		return 1
	}
	proceed, code := confirmStateWrite(in, out, errw, plan.diff(), yes)
	if !proceed {
		return code
	}
	st, err := plan.reverify("agy pro hybrid on")
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", err)
		return 1
	}
	st.HybridProfile = p.String()
	if err := mode.SaveState(st); err != nil {
		fmt.Fprintf(errw, "[agy-pro] failed to persist profile: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "[SUCCESS] hybrid profile set to %s\n", p.Label())
	fmt.Fprintf(out, "  Cost   : %s\n", p.Description())
	fmt.Fprintf(out, "  Launch : %s\n", launchInstruction(p))
	return 0
}

// prevalidateProfileArtifact validates the launch artifact of one profile: P1's
// fetchAvailableModels catalog (hybrid.BuildFetchAvailableModels) and P2's
// session-settings composition (p2Externals + agysettings.BuildP2Settings). The
// returned error already names the artifact, so the refusal reads
// profile-specifically instead of blaming P1's catalog on a P2 opt-in.
//
// The gate is profile-aware because the two profiles serve different artifacts.
// Gating both on the P1 catalog produced both failure directions: P1's deny-list
// (every static id plus every known-preset provider name and alias) is strictly
// wider than P2's reserved set, so `on p2` refused a registry a P2 session serves
// happily; and P2's own artifact went unvalidated, so a whitespace-bearing id
// passed `on p2` and then made every invocation exit 1 with no native fallback.
func prevalidateProfileArtifact(p hybrid.Profile) error {
	switch p {
	case hybrid.ProfileP1:
		if err := prevalidateCatalog(); err != nil {
			return fmt.Errorf("the model catalog could not be built (%w)", err)
		}
		return nil
	case hybrid.ProfileP2:
		if err := prevalidateP2Settings(); err != nil {
			return fmt.Errorf("the P2 session settings could not be composed (%w)", err)
		}
		return nil
	default:
		return nil
	}
}

// prevalidateCatalog builds the fetchAvailableModels catalog once with the
// production reserved-enum options (FIX-63). A build error (a deny-list
// collision, etc.) is returned so `hybrid on p1` refuses before persisting. An
// unreadable or missing registry returns nil: there is nothing to pre-validate,
// and LaunchP1's launch-time native fallback owns that case.
func prevalidateCatalog() error {
	reg, err := providers.Load()
	if err != nil {
		return nil
	}
	if _, err := hybrid.BuildFetchAvailableModels(reg, hybrid.DefaultCatalogOptions()); err != nil {
		return err
	}
	return nil
}

// prevalidateP2Settings composes the P2 session settings once (FIX-63
// profile-aware twin of prevalidateCatalog). A composition error (a
// whitespace-bearing customModels key, a reserved-name collision) is returned so
// `hybrid on p2` refuses before persisting, with the composer's own reason. An
// unreadable or missing registry returns nil for the same reason
// prevalidateCatalog does: LaunchP2's launch-time gate owns that case.
func prevalidateP2Settings() error {
	reg, err := providers.Load()
	if err != nil {
		return nil
	}
	return hybrid.ValidateP2Artifact(reg)
}

// hybridOff clears the persisted profile. P5-1 U2: the clear is a state.json
// write like `on`, so it presents the same old -> new diff and takes consent.
func hybridOff(in io.Reader, out, errw io.Writer, yes bool) int {
	plan, err := snapshotStateScalar("hybrid_profile", "")
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", err)
		return 1
	}
	proceed, code := confirmStateWrite(in, out, errw, plan.diff(), yes)
	if !proceed {
		return code
	}
	st, err := plan.reverify("agy pro hybrid off")
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] %v\n", err)
		return 1
	}
	st.HybridProfile = ""
	if err := mode.SaveState(st); err != nil {
		fmt.Fprintf(errw, "[agy-pro] failed to clear profile: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "[SUCCESS] hybrid profile cleared (off)")
	return 0
}

// launchInstruction is the honest how-to-launch line. It states the mechanism
// the launcher applies when it reads the persisted profile; the mesh front door
// itself is wired by the launcher unit, so the note names that boundary.
func launchInstruction(p hybrid.Profile) string {
	switch p {
	case hybrid.ProfileP1:
		return "run `agy` as usual; the launcher starts the local /v1internal mesh and execs agy with CLOUD_CODE_URL set (no settings file is written)."
	case hybrid.ProfileP2:
		return "run `agy`; the launcher creates a per-session app-data dir with modelProvider:\"gemini\" and execs agy with GOOGLE_GEMINI_BASE_URL set."
	default:
		return "run `agy` as usual; no hybrid mesh is started."
	}
}

// hybridStatus prints the effective profile (env override vs persisted) and, for
// an active profile, whether wirecheck says GO.
func hybridStatus(out, errw io.Writer) int {
	st, err := mode.LoadState()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] failed to read state: %v\n", err)
		return 1
	}
	persisted := st.EffectiveHybridProfile()
	eff := hybrid.ResolveProfile(os.Getenv, persisted)

	fmt.Fprintf(out, "Persisted profile : %s\n", persisted)
	if envRaw := strings.TrimSpace(os.Getenv(hybrid.EnvHybridProfile)); envRaw != "" {
		fmt.Fprintf(out, "Env override      : %s=%s\n", hybrid.EnvHybridProfile, providers.MaskOperand(envRaw))
	} else {
		fmt.Fprintf(out, "Env override      : (none)\n")
	}
	fmt.Fprintf(out, "Effective profile : %s\n", eff.Label())
	fmt.Fprintf(out, "  Cost            : %s\n", eff.Description())

	if eff == hybrid.ProfileNone {
		return 0
	}
	report := wirecheckCheck()
	if report.ProfileOK(eff.WirecheckName()) {
		fmt.Fprintf(out, "wirecheck         : GO for %s\n", eff.WirecheckName())
		return 0
	}
	fmt.Fprintf(out, "wirecheck         : NOGO for %s\n", eff.WirecheckName())
	for _, reason := range report.Reasons() {
		fmt.Fprintf(out, "  - %s\n", reason)
	}
	return 0
}

// hybridCheck renders the wirecheck report and prints the per-profile GO/NOGO
// verdict. It fails closed: exit 1 when the effective profile is NOGO, exit 0
// when it is GO or when no profile is set.
func hybridCheck(out, errw io.Writer) int {
	report := wirecheckCheck()
	wirecheck.Render(out, report)

	for _, p := range []hybrid.Profile{hybrid.ProfileP1, hybrid.ProfileP2} {
		printProfileVerdict(out, report, p)
	}

	eff := effectiveProfileForCheck(errw)
	if eff == hybrid.ProfileNone {
		return 0
	}
	if report.ProfileOK(eff.WirecheckName()) {
		return 0
	}
	fmt.Fprintf(errw, "[agy-pro] effective profile %s is NOGO; refusing to launch a half-configured session\n", eff.Label())
	return 1
}

// effectiveProfileForCheck resolves the effective profile for the exit-code
// decision. FIX-52: a state-read failure must NOT fail open. The persisted value
// is unknown, so it defaults to none, but the AGY_PRO_HYBRID env override is
// still honored (hybrid.ResolveProfile layers env over the empty persisted
// value): a NOGO profile under the env override still fails closed (exit 1).
func effectiveProfileForCheck(errw io.Writer) hybrid.Profile {
	st, err := mode.LoadState()
	if err != nil {
		fmt.Fprintf(errw, "[agy-pro] failed to read state: %v\n", err)
		return hybrid.ResolveProfile(os.Getenv, mode.HybridProfileNone)
	}
	return hybrid.ResolveProfile(os.Getenv, st.EffectiveHybridProfile())
}

// printProfileVerdict prints one profile's GO/NOGO line and, when NOGO, the
// missing canaries. Canary names and literals are public binary strings, safe to
// print.
func printProfileVerdict(out io.Writer, report wirecheck.Report, p hybrid.Profile) {
	name := p.WirecheckName()
	if report.ProfileOK(name) {
		fmt.Fprintf(out, "Verdict %s: GO\n", name)
		return
	}
	fmt.Fprintf(out, "Verdict %s: NOGO\n", name)
	var missing []string
	for _, c := range report.Canaries {
		if c.Profile == name && !c.Found {
			missing = append(missing, fmt.Sprintf("%s (%q)", c.Name, c.Literal))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(out, "  missing canaries: %s\n", strings.Join(missing, ", "))
	} else {
		fmt.Fprintf(out, "  no canaries verified for this profile\n")
	}
}
