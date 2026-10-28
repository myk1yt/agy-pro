// Package hybrid is the catalog core for the native-coexistence profiles
// (design §2, docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md).
//
// Profile P1 ("picker coexistence") runs the real agy behind a local /v1internal
// mesh (CLOUD_CODE_URL). agy composes its /model catalog from the mesh's
// fetchAvailableModels response, so the mesh must serve a catalog in which every
// external model is listed and selectable with correct per-model metadata. This
// package owns that composition:
//
//   - profile.go: the Profile type, its parsing, and the env/state precedence.
//   - catalog.go: BuildFetchAvailableModels, the fetchAvailableModels payload
//     builder (synthesized enums, agent_model_sorts membership, display_name,
//     max_tokens floor, deny-list, cross-provider collision policy).
//   - resolver.go: RegistryResolver, the mesh-side model allow-list seam that
//     feeds wireadp.Config.ResolveModel.
//   - launch.go / p1.go / p2.go: the launch mechanics (native fallback, exit-code
//     mapping, stdio defaults) and the two front doors that run a live per-session
//     mesh and exec the real agy behind it.
//
// The recipe is proven live (plan-model-gate-report.md §3/§10, audit-gap-probe-report.md
// §3): each entry needs a UNIQUE synthesized enum (sharing BYOM 326 silently
// mis-routes, the last entry wins), a non-empty display_name (a missing one makes
// agy silently run the default model), max_tokens >= ~25k (below ~20.5k every run
// hard-fails), and membership in an agent_model_sorts group (gates BOTH listing
// and --model selection).
//
// The package is no longer a leaf. It grew the two front doors, which pull in the
// runtime graph: p1.go and p2.go import internal/{mesh, paths, providers,
// realagy, wireadp, wirecheck} (p2.go also internal/agysettings) and each applies
// the wirecheck gate itself, so a caller only chooses the profile and hands over
// LaunchOptions. cmd/agy/pro_hybrid.go owns the persistence verbs
// (on/off/status/check); launcher owns the Decide routing. The catalog core
// (profile.go, catalog.go, resolver.go) stays stdlib + internal/providers only,
// which is what keeps it testable without a mesh.
package hybrid

import (
	"fmt"
	"strings"
)

// Profile names one native-coexistence hybrid profile.
type Profile string

// Profile values. ProfileNone is the "no hybrid profile" state.
const (
	ProfileNone Profile = "none"
	ProfileP1   Profile = "p1"
	ProfileP2   Profile = "p2"
)

// EnvHybridProfile is the environment override for the active profile. It
// accepts p1|p2|off and takes precedence over the persisted state.
const EnvHybridProfile = "AGY_PRO_HYBRID"

// ParseProfile parses a profile token. It accepts p1|picker|1 and p2|subagent|2
// and off|none|"" (the last three all yield ProfileNone). Any other value is an
// error; the caller decides whether to fail or fall back.
func ParseProfile(s string) (Profile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "p1", "picker", "1":
		return ProfileP1, nil
	case "p2", "subagent", "2":
		return ProfileP2, nil
	case "off", "none", "":
		return ProfileNone, nil
	default:
		return ProfileNone, fmt.Errorf("unknown hybrid profile %q (want p1|picker|1, p2|subagent|2, or off)", s)
	}
}

// ResolveProfile resolves the effective profile. Precedence: the AGY_PRO_HYBRID
// environment override (p1|p2|off) wins over the persisted state, which wins over
// none. getenv reads the environment (pass os.Getenv in production); a nil getenv
// skips the env layer. stateEffective is the persisted value (typically
// (*mode.State).EffectiveHybridProfile()).
//
// An environment value that is set but does not parse (a typo, not a known
// profile) is ignored, and the persisted state decides: the env is a transient
// per-launch override, so a malformed override must not silently disable a
// profile the user chose with `agy pro hybrid on`. An empty or unset env value
// also falls through to the state.
func ResolveProfile(getenv func(string) string, stateEffective string) Profile {
	if getenv != nil {
		if raw := strings.TrimSpace(getenv(EnvHybridProfile)); raw != "" {
			if p, err := ParseProfile(raw); err == nil {
				return p
			}
		}
	}
	p, _ := ParseProfile(stateEffective)
	return p
}

// Label returns the short human name for the profile.
func (p Profile) Label() string {
	switch p {
	case ProfileP1:
		return "P1 (picker coexistence)"
	case ProfileP2:
		return "P2 (subagent profile)"
	default:
		return "off (no hybrid profile)"
	}
}

// Description returns the honest cost line for the profile: what the user gets
// and what the profile cannot do. These are the lines `agy pro hybrid on` prints
// so the trade-off is stated plainly, never hidden.
func (p Profile) Description() string {
	switch p {
	case ProfileP1:
		return "externals in agy's own /model catalog; no subagent spawning on this path"
	case ProfileP2:
		return "full toolset incl. subagents; externals selected at launch; subscription Gemini off in-session"
	default:
		return "no hybrid profile; agy runs its default native path"
	}
}

// WirecheckName maps the profile onto the wirecheck canary name ("P1"/"P2"), or
// "" for none. wirecheck gates each profile independently, so the launcher and
// `agy pro hybrid check` ask report.ProfileOK(p.WirecheckName()).
func (p Profile) WirecheckName() string {
	switch p {
	case ProfileP1:
		return "P1"
	case ProfileP2:
		return "P2"
	default:
		return ""
	}
}

// String returns the canonical token ("p1"/"p2"/"none").
func (p Profile) String() string {
	return string(p)
}
