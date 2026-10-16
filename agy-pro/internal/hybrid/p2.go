// Package hybrid's P2 front door (profile "subagent coexistence").
//
// This file wires profile P2 (design §2 P2,
// docs/261014_0006_session_native-coexistence-design/NATIVE_COEXISTENCE_DESIGN.md):
// agy runs in gemini mode (modelProvider:"gemini" + GOOGLE_GEMINI_BASE_URL +
// GEMINI_API_KEY) against the local mesh, under a per-session --app_data_dir so
// the global settings file is never touched (V6: --app_data_dir <relative>
// relocates the CLI app-data dir, settings.json included, to
// %USERPROFILE%\.gemini\<relative>).
//
// COSTS, STATED PLAINLY (design §5; DoD #3):
//
//   - Externals are NOT listed in agy's /model catalog. In gemini mode agy
//     lists the 11 static Gemini defaults and customModels entries never appear
//     in `agy models` (W12). Selection is the launcher picker or an explicit
//     --model <map key>; a static id is served only when the registry carries
//     a model with that exact id (the static-id policy below).
//   - Subscription Gemini is OFF in a P2 session. modelProvider:"gemini" swaps
//     the listed catalog to the static Gemini defaults and agy authenticates
//     with GEMINI_API_KEY (the install nonce here), so no Antigravity
//     subscription traffic runs. The switch is contained by the per-session
//     app-data dir: the global settings file keeps its own regime.
//
// Flow (mirrors LaunchP1, FIX-55 included):
//
//   - resolve the real agy ONCE; the SAME path goes to the wirecheck gate and
//     to the exec (no TOCTOU);
//   - WIRECHECK GATE for profile "p2": a NOGO verdict or a P2 profile that is
//     not ProfileOK prints a clear notice and falls back to a TRUE native
//     session (scrubbed env, no mesh, no settings write), never a
//     half-configured session;
//   - load the registry (zero enabled externals, or an unreadable file, is the
//     same fallback);
//   - start the per-spawn mesh and health-gate it (refuse-to-launch on
//     failure). The mesh carries a strict install nonce on /v1beta and
//     /healthz; P2 never speaks /v1internal, so RequireV1InternalAuth stays at
//     its strict default and no catalog is served;
//   - compose and write the per-session settings document into
//     <home>/.gemini/agy-hybrid-<id>/settings.json through internal/agysettings
//     (which refuses the global path by default). A write failure is a launch
//     failure: exit 1 with a clear error, no child started;
//   - exec the real agy with --app_data_dir <name>, the scrubbed child env PLUS
//     GOOGLE_GEMINI_BASE_URL=<mesh URL> and GEMINI_API_KEY=<install nonce>
//     appended AFTER the scrub (the order is load-bearing: the scrub removes
//     the user's real keys, the append installs the nonce), and --model <map
//     key> when a model was pinned. The child's lifetime is tied to the mesh
//     (mesh.RunChild).
//
// The launch-mechanics helpers shared with P1 (the exit-code map p1ExitCode, the
// stdio defaults, the child-env scrub list ChildKeyScrubNames) live in
// launch.go. The native fallback is NOT one of them: P2 carries its own
// spelling, p2NativeFallback, because the fallback notice must name P2's own
// injection (GOOGLE_GEMINI_BASE_URL) rather than P1's CLOUD_CODE_URL. It is a
// deliberate near-duplicate of launchNativeFallback; the full dedup is waived
// (it reopens reviewed P1 code for no behavioral gain), and the FIX-65
// path/notice masking applied to p2NativeFallback (FIX-6) keeps the two in sync
// on the one axis that matters.
//
// Stdlib + agypro/internal/{agysettings,mesh,paths,providers,realagy,wireadp,wirecheck}.
// It does NOT import bridge or fanout.
package hybrid

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agypro/internal/agysettings"
	"agypro/internal/mesh"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/realagy"
	"agypro/internal/wireadp"
	"agypro/internal/wirecheck"
)

// settingsWrite is the per-session settings-write seam (contract §2.1 style):
// tests swap it to force the write-failure path without touching the
// filesystem. The default is the real managed write.
var settingsWrite = writeP2Settings

// customModelSchemes are the four customModels key schemes agy v1.3.5 accepts
// (binary-surface-report.md §1.2, the scheme regex agysettings compiles
// verbatim). A --model value already carrying one of them is a map key and
// passes through untouched; anything else is treated as a registry id and
// rewritten into the blade: form below.
var customModelSchemes = []string{"gemini-api:", "blade:", "evergreen:", "beyond-api:"}

// appDataDirPrefix is the --app_data_dir name prefix for a P2 session. The
// value is a single relative segment, so it resolves under %USERPROFILE%\.gemini\
// (V6) and can never escape the user's home.
const appDataDirPrefix = "agy-hybrid-"

// appDataDirSuffixBytes is the random suffix length (4 bytes, 8 hex chars).
// Combined with the UTC timestamp it makes the name unique per spawn even for
// two launches inside the same second.
const appDataDirSuffixBytes = 4

// staticIDServedNote is the billing label StaticIDTarget returns for a servable
// static id. The launcher and the docs carry it so the user knows the traffic
// is billed to the configured provider (AI-Studio-billed in practice), not to
// the Antigravity subscription, which is off in a P2 session.
const staticIDServedNote = "served from the registry; billed to the configured provider (AI-Studio-billed in practice)"

// LaunchP2 runs the P2 (subagent profile) front door. It resolves the real agy,
// gates on wirecheck for profile "p2", starts the per-session mesh, writes the
// per-session gemini-mode settings document into a fresh --app_data_dir, and
// execs the real agy behind the mesh. On a wirecheck NOGO (or a P2 profile that
// is not ProfileOK), an unreadable registry, or zero enabled externals it prints
// a clear notice and FALLS BACK TO NATIVE: scrubbed env, no mesh, no settings
// write, never a half-configured session.
//
// Exit-code map (the mapping itself is p1ExitCode, shared with P1):
//   - the child's own exit code passes through unchanged;
//   - a mesh that dies mid-session yields ExitMeshDied (75) with a nil error
//     (the mesh already wrote the one visible fail-fast line);
//   - refuse-to-launch, a mesh start failure, a settings composition or write
//     failure, and a real-agy resolution failure all return exit 1 with the
//     error;
//   - the wirecheck/registry fallback returns the native child's own exit code.
func LaunchP2(opts LaunchOptions) (int, error) {
	// 1. Resolve the real agy ONCE. The same path is pinned into the wirecheck
	// gate below and used for the exec, so the gate can never scan one binary
	// while another runs (FIX-55).
	realAgy := strings.TrimSpace(opts.RealAgyPath)
	if realAgy == "" {
		resolved, err := realagyResolve()
		if err != nil {
			return 1, fmt.Errorf("hybrid P2: cannot resolve the real agy binary: %w", err)
		}
		realAgy = resolved
	}

	errw := stderrOr(opts.Stderr)

	// 2. WIRECHECK GATE (design §3): a NOGO verdict or a P2 profile that is not
	// ProfileOK falls back to native. A GO with version DRIFT proceeds and the
	// drift is surfaced in the notice. The gate scans the SAME binary we will
	// exec (FIX-55: realAgy is resolved once above and pinned here).
	report := wirecheckCheck(realAgy)
	if report.Verdict() == wirecheck.VerdictNOGO || !report.ProfileOK(ProfileP2.WirecheckName()) {
		reg, _ := providers.Load() // best-effort: scrub key names even on fallback
		return p2NativeFallback(realAgy, opts, reg, p2NogoNotice(report))
	}
	if report.VersionDrift {
		fmt.Fprintf(errw, "[agy-pro] wirecheck: version drift (binary reports %q, pinned %q); proceeding with P2\n",
			report.Version, wirecheck.PinnedVersion)
	}

	// 3. Registry: zero enabled externals means the mesh has nothing to serve,
	// so fall back to native with a clear notice.
	reg, err := providers.Load()
	if err != nil {
		// FIX-6/FIX-65: the load error embeds the absolute registry path (a
		// location MaskOperand exempts), so a class label replaces it rather than
		// echoing the user's directory into the fallback notice.
		return p2NativeFallback(realAgy, opts, nil,
			"the provider registry could not be read (the registry file is missing, unreadable, or invalid); the mesh has nothing to serve")
	}
	if countEnabledModels(reg) == 0 {
		return p2NativeFallback(realAgy, opts, reg,
			"no enabled external providers; the mesh has nothing to serve")
	}

	// 4. The per-spawn mesh. Strict nonce on /v1beta and /healthz (the genai
	// client presents GEMINI_API_KEY as X-Goog-Api-Key, so the nonce reaches
	// the mesh on every request), no catalog, the P2 resolver.
	home, err := paths.AgyProHome()
	if err != nil {
		return 1, fmt.Errorf("hybrid P2: cannot resolve the agy-pro home: %w", err)
	}
	cfg, err := buildP2Config(reg, home)
	if err != nil {
		return 1, err
	}
	m, err := meshStart(mesh.Config{
		Config:        cfg,
		HealthTimeout: mesh.DefaultHealthTimeout,
		ErrWriter:     errw,
	})
	if err != nil {
		return 1, fmt.Errorf("hybrid P2: refusing to launch, the mesh could not start: %w", err)
	}
	defer func() { _ = m.Stop() }()

	// Front-door re-check before anything is injected or written
	// (refuse-to-launch names the URL/port).
	if err := m.EnsureHealthy(context.Background()); err != nil {
		return 1, err
	}

	// 5. Per-session app-data dir + settings write (design §2 P2). The blade:
	// keys carry the mesh port, so the document is composed AFTER the mesh is
	// up. A failure here is a launch failure: the session must not start
	// half-configured, so the child is never exec'd.
	appDataDir, err := newAppDataDirName()
	if err != nil {
		return 1, err
	}
	externals := p2Externals(reg, m.URL(), p2DropSink(errw))
	doc, err := agysettings.BuildP2Settings(externals)
	if err != nil {
		return 1, fmt.Errorf("hybrid P2: cannot compose the session settings: %w", err)
	}
	if err := settingsWrite(appDataDir, doc); err != nil {
		return 1, fmt.Errorf("hybrid P2: cannot write the per-session settings for app-data dir %s: %w", appDataDir, err)
	}

	// 6. Child environment. The scrub removes the user's real
	// GEMINI_API_KEY/GOOGLE_API_KEY (fixed list) and every provider key name.
	// opts.ChildEnv is appended right after the scrub, and the two gemini-mode
	// vars come AFTER it, so under Go 1.22 last-wins exec dedup the mesh base URL
	// and the install nonce always win: a caller-supplied ChildEnv entry cannot
	// override them (FIX-5). NO provider key values anywhere in this env.
	childEnv := realagy.CleanChildEnvWithBase(os.Environ(), ChildKeyScrubNames(reg))
	childEnv = append(childEnv, opts.ChildEnv...)
	childEnv = append(childEnv, "GOOGLE_GEMINI_BASE_URL="+m.URL())
	childEnv = append(childEnv, "GEMINI_API_KEY="+cfg.AuthKey)

	// 7. Child argv: the front-door flags FIRST (--app_data_dir, then the
	// canonical --model when a model was pinned), then the caller's args, so
	// agy's flag parser always consumes the confinement flag (FIX-2). A bare slug
	// is rejected by agy per W11, so the pin is rewritten into the map key.
	argv := p2ChildArgs(opts.Args, appDataDir, m.URL(), reg)

	// The supervised run owns the exec, the mesh lifetime and (when armed) the
	// --model rewrite fallback. The mesh is torn down by the defer above, only
	// after the FINAL attempt.
	return p1ExitCode(p2RunChild(context.Background(), m, realAgy, argv, childEnv, opts, appDataDir, reg))
}

// buildP2Config assembles the mesh's wireadp.Config for one P2 spawn:
//
//   - AuthKey = a fresh per-spawn install nonce (32 crypto/rand bytes, hex).
//     STRICT MODE IS MANDATORY and it is the right posture on this path: agy's
//     genai client sends GEMINI_API_KEY as X-Goog-Api-Key, so the child
//     presents the nonce on every /v1beta request and strict auth closes the
//     port to everything else. A nonce failure is an error, never an empty key
//     (an empty AuthKey would put the mesh in permissive mode).
//   - RequireV1InternalAuth is left nil, which is the strict default: P2 never
//     speaks /v1internal, so the passive-receipt opt-in is not taken.
//   - CatalogFunc and RPCHooks are left unset: the genai surface lists nothing
//     (customModels entries never appear in agy's catalog, W12), so the mesh
//     serves no catalog on this path and fetchAvailableModels keeps its minimal
//     empty default behind strict auth.
//   - ResolveModel = the P2 resolver (RegistryResolver plus the static-id
//     policy).
//   - Redact = the registry redactor.
//   - AuditPath = <home>/logs/wireadp-<ts>.jsonl (the directory is created).
//   - Generate = nil (wireadp installs its default generation handler).
func buildP2Config(reg *providers.Registry, home string) (wireadp.Config, error) {
	nonce, err := newInstallNonce()
	if err != nil {
		return wireadp.Config{}, fmt.Errorf("hybrid P2: cannot generate the install nonce: %w", err)
	}
	logsDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		// FIX-6/FIX-65: name the class, not the absolute path (the agysettings
		// maskedPath discipline); the wrapped OS error is the deferred deeper
		// location-exemption fix.
		return wireadp.Config{}, fmt.Errorf("hybrid P2: cannot create the audit log directory (the logs directory): %w", err)
	}
	auditPath := filepath.Join(logsDir, "wireadp-"+time.Now().UTC().Format("20060102_150405")+".jsonl")
	return wireadp.Config{
		Registry:     reg,
		Redact:       reg.Redactor(),
		AuthKey:      nonce,
		AuditPath:    auditPath,
		ResolveModel: p2Resolver(reg),
		Generate:     nil,
	}, nil
}

// p2Resolver returns the mesh-side model allow-list seam for a P2 session
// (wireadp.Config.ResolveModel). It wraps RegistryResolver with the static-id
// policy: a requested static Gemini id is served only through StaticIDTarget,
// so an unservable static id becomes wireadp's Gemini-shaped 404 that names the
// model (never a silent substitution), and everything else keeps the ordinary
// exact-slug then terminal-segment resolution. The blade: keys agy derives on
// the wire are the bare slug (W10), so the exact rung hits.
func p2Resolver(reg *providers.Registry) func(string) (providers.Target, bool, error) {
	base := RegistryResolver(reg)
	return func(name string) (providers.Target, bool, error) {
		if isStaticGeminiID(name) {
			target, ok, _ := StaticIDTarget(reg, name)
			return target, ok, nil
		}
		return base(name)
	}
}

// StaticIDTarget applies the P2 static-id policy (design §2 P2) to one
// requested model name. The 11 static Gemini defaults agy lists in gemini mode
// are agy's own catalog entries: the mesh answers them only when they are
// genuinely servable through the user's configured providers, and never
// substitutes a different model.
//
//   - id is one of the 11 static ids (case-insensitive) and the registry
//     carries an ENABLED model with exactly that id: the target is returned
//     with ok=true and staticIDServedNote (the billing label: the traffic goes
//     to the configured provider, AI-Studio-billed in practice).
//   - id is one of the 11 but no such model is registered: ok=false with a
//     reason naming the model. The P2 resolver reports the miss so wireadp
//     answers its Gemini-shaped 404 that names the model.
//   - id is not a static id: ok=false with an empty reason, meaning "not a
//     static-id question; consult the ordinary allow-list".
//
// The match is exact (no terminal-segment rung) and case-insensitive, matching
// the deny-list the settings composer applies, so a case variant of a static id
// is served rather than silently mis-routed.
func StaticIDTarget(reg *providers.Registry, id string) (providers.Target, bool, string) {
	trimmed := stripControlChars(strings.TrimSpace(id))
	if !isStaticGeminiID(trimmed) {
		return providers.Target{}, false, ""
	}
	if reg != nil {
		for _, p := range reg.Providers {
			for _, m := range p.Models {
				if m.IsEnabled() && strings.EqualFold(stripControlChars(strings.TrimSpace(m.ID)), trimmed) {
					return providers.Target{Provider: p, Model: m}, true, staticIDServedNote
				}
			}
		}
	}
	return providers.Target{}, false, fmt.Sprintf(
		"static Gemini model %s is not servable through the configured providers; the mesh answers a clear 404 naming the model instead of substituting one",
		providers.MaskOperand(trimmed))
}

// isStaticGeminiID reports whether name is one of the 11 static Gemini defaults
// agy lists in gemini mode (case-insensitive). The list is shared through
// agysettings.StaticGeminiIDs rather than copied here.
func isStaticGeminiID(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	for _, id := range agysettings.StaticGeminiIDs() {
		if strings.EqualFold(trimmed, id) {
			return true
		}
	}
	return false
}

// newAppDataDirName returns a fresh per-session --app_data_dir value:
// agy-hybrid-<UTC timestamp>-<random hex>. The timestamp orders sessions for an
// operator reading the directory; the random suffix keeps the name unique per
// spawn even for two launches inside the same second. The name is a single
// relative segment with no ':' or '\' (agysettings rejects both inside a
// segment) and resolves to <home>/.gemini/<name> (V6).
func newAppDataDirName() (string, error) {
	var suffix [appDataDirSuffixBytes]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("hybrid P2: cannot generate the app-data-dir suffix: %w", err)
	}
	return appDataDirPrefix + time.Now().UTC().Format("20060102_150405") + "-" + hex.EncodeToString(suffix[:]), nil
}

// p2Externals builds the customModels entries for one P2 spawn: one blade:-keyed
// entry per servable enabled external.
//
// A drop is logged through logf (masked, control-stripped) instead of failing
// the launch, mirroring the P1 catalog's drop policy:
//
//   - a blank id (nothing to key an entry by);
//   - an id colliding with a reserved native or AI-Studio preset name
//     (agysettings.ReservedModelNames). Such an id is served, if at all, through
//     the static-id policy on the resolver, never as a customModels entry: agy
//     would resolve the entry to a static default and route ambiguously;
//   - a cross-provider duplicate id (the first provider wins, exactly like the
//     P1 catalog).
//
// Everything BuildP2Settings still refuses (a whitespace-bearing key, a
// duplicate key) surfaces as a launch-time error at the caller, never a silent
// half-write.
func p2Externals(reg *providers.Registry, meshURL string, logf func(format string, args ...any)) []agysettings.ExternalEntry {
	if reg == nil {
		return nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	reserved := reservedP2Names()
	seen := map[string]bool{}
	var out []agysettings.ExternalEntry
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			// One normalization point for the id (FIX-49/FIX-64 parity): trim
			// AND strip control characters, so the key, the name agy derives
			// from it, and the resolver comparison all agree.
			id := stripControlChars(strings.TrimSpace(m.ID))
			if id == "" {
				logf("hybrid P2: dropped a model from provider %q: blank id", providers.MaskOperand(p.Name))
				continue
			}
			if reserved[strings.ToLower(id)] {
				logf("hybrid P2: dropped model %q from provider %q: the id is a reserved native or AI-Studio preset name; it is served, if at all, through the static-id policy on the resolver", providers.MaskOperand(id), providers.MaskOperand(p.Name))
				continue
			}
			if seen[id] {
				logf("hybrid P2: dropped model %q from provider %q: id already registered by an earlier provider", providers.MaskOperand(id), providers.MaskOperand(p.Name))
				continue
			}
			seen[id] = true
			key := bladeKey(meshURL, id)
			out = append(out, agysettings.ExternalEntry{
				URL:           key,
				ModelName:     key,
				DisplayName:   stripControlChars(providers.DisplayName(id, p.Name)),
				ContextWindow: m.ContextLength,
				ModelFeatures: modelFeaturesFor(m.Capabilities),
			})
		}
	}
	return out
}

// p2PrevalidateMeshURL is the fixed placeholder mesh URL the opt-in gate
// composes the P2 artifact against. Only the KEY FORM is validated there (a
// blade: key the composer refuses, e.g. one derived from a whitespace-bearing
// id); the real per-spawn port is unknown at `agy pro hybrid on` time and is
// irrelevant to the refusal, so a deterministic loopback placeholder keeps the
// gate hermetic and repeatable.
const p2PrevalidateMeshURL = "http://127.0.0.1:1"

// ValidateP2Artifact composes the artifact a P2 session actually writes - the
// customModels map p2Externals derives and agysettings.BuildP2Settings validates
// - and returns the first refusal. It is the P2 twin of BuildFetchAvailableModels
// for the `agy pro hybrid on` gate (FIX-63 made profile-aware): the gate must
// validate the artifact the CHOSEN profile uses, because the two profiles serve
// different artifacts. P1's deny-list is strictly wider than P2's reserved set,
// so gating `on p2` on the P1 catalog refused registries a P2 session serves
// happily, while the artifact P2 really needs went unvalidated and a
// whitespace-bearing id passed `on p2` only to exit 1 on every invocation after.
//
// A nil registry validates nothing (there is no artifact to compose), matching
// prevalidateCatalog's treatment of an unreadable registry: LaunchP2's own
// launch-time gate owns that case.
func ValidateP2Artifact(reg *providers.Registry) error {
	if reg == nil {
		return nil
	}
	_, err := agysettings.BuildP2Settings(p2Externals(reg, p2PrevalidateMeshURL, nil))
	return err
}

// P2PickerAllows returns the per-row predicate the P2 launcher picker applies to
// the combined native+external inventory (R5B-FIX-3). It exists so the picker
// offers ONLY rows a P2 session can actually serve, instead of rows that
// dead-end at agy's --model gate or silently route to a different provider.
//
// Two filters, both mirroring the frozen core so the picker and the settings
// document can never disagree:
//
//   - a NATIVE row is allowed only when its id is one of the static Gemini ids
//     (agysettings.StaticGeminiIDs, case-insensitive). In gemini mode agy lists
//     exactly those, so any other native row (claude-*, gpt-oss-*, ...) would
//     pin a blade: key the settings document does not carry and agy would
//     reject it;
//   - a PROVIDER row is allowed only when it is the entry p2Externals would
//     actually key: enabled, non-blank id, not a reserved native/AI-Studio
//     preset name, and the FIRST occurrence of that id in registry order. A
//     cross-provider duplicate is keyed once (first provider wins), so offering
//     the second provider's row would pin the first provider's key and bill the
//     wrong vendor - the row is filtered by (id, owning provider).
//
// R5BF2-3: the keyable dedup and the owner comparison are EXACT (case-sensitive
// on the raw id, byte-equal on the provider name), mirroring p2Externals'
// `seen[id]` and `p2RegistryRawID`'s `id == trimmed` rather than folding case.
// The earlier case-folded shape was unsafe in one direction and lossy in the
// other: `EqualFold` on the provider name could offer a row under a
// case-variant provider tag whose pin resolves to the FIRST provider (the
// wrong-vendor billing class this filter exists to close), and folding the id
// hid a servable row whenever two providers carried the same id in different
// case (p2Externals keys both, because its dedup is case-sensitive). Both the
// id and the group arrive byte-equal from the same registry, so exactness costs
// nothing for legitimate rows.
//
// The predicate takes the inventory row's (Kind, Group, ModelID) rather than an
// inventory.Entry so this package does not depend on internal/inventory. It is
// a closure so the keyable map is computed once per launch, not per row.
func P2PickerAllows(reg *providers.Registry) func(kind, group, modelID string) bool {
	static := map[string]bool{}
	for _, id := range agysettings.StaticGeminiIDs() {
		if n := strings.ToLower(strings.TrimSpace(id)); n != "" {
			static[n] = true
		}
	}
	// keyable maps a RAW id to the provider name that owns its customModels
	// key, mirroring p2Externals' enabled + reserved-drop + first-wins scan
	// exactly - including its case-sensitive dedup, so a case-variant duplicate
	// id across providers yields two servable rows, not one hidden one.
	keyable := map[string]string{}
	if reg != nil {
		reserved := reservedP2Names()
		for _, p := range reg.Providers {
			for _, m := range p.Models {
				if !m.IsEnabled() {
					continue
				}
				id := stripControlChars(strings.TrimSpace(m.ID))
				if id == "" || reserved[strings.ToLower(id)] {
					continue
				}
				if _, dup := keyable[id]; dup {
					continue
				}
				keyable[id] = p.Name
			}
		}
	}
	return func(kind, group, modelID string) bool {
		id := stripControlChars(strings.TrimSpace(modelID))
		if id == "" {
			return false
		}
		switch kind {
		case "native":
			return static[strings.ToLower(id)]
		case "provider":
			owner, ok := keyable[id]
			return ok && strings.TrimSpace(group) == owner
		default:
			return false
		}
	}
}

// modelFeaturesFor maps registry capabilities onto agy's modelFeatures object.
// The registry declares vision and video only (Capabilities carries no pdf or
// audio flag), so images/video are the only flags that can be set; a model with
// neither gets a nil pointer and the key is omitted from the document.
func modelFeaturesFor(c providers.Capabilities) *agysettings.ModelFeatures {
	features := &agysettings.ModelFeatures{
		Images: c.SupportsVision,
		Video:  c.SupportsVideo,
	}
	if !features.Images && !features.Video {
		return nil
	}
	return features
}

// reservedP2Names returns the lowercase set of ids that must not become a
// customModels entry: the 11 static Gemini defaults plus the AI-Studio preset
// names, shared from agysettings so the two lists cannot drift.
func reservedP2Names() map[string]bool {
	out := map[string]bool{}
	for _, name := range agysettings.ReservedModelNames() {
		if n := strings.ToLower(strings.TrimSpace(name)); n != "" {
			out[n] = true
		}
	}
	return out
}

// writeP2Settings writes the composed P2 document into the per-session
// app-data-dir settings file and releases the lock.
//
// The path is resolved through agysettings.AppDataDirSettingsPath, which
// validates the relative name and resolves it to
// <home>/.gemini/<name>/settings.json. The protected global settings path is
// never addressed: agysettings.Open refuses it by default
// (ErrGlobalSettingsPath) and this function offers no override, so a P2
// session cannot switch the user's whole environment.
func writeP2Settings(appDataDir string, doc map[string]any) error {
	path, err := agysettings.AppDataDirSettingsPath(appDataDir)
	if err != nil {
		return err
	}
	f, err := agysettings.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, key := range agysettings.ManagedKeys() {
		v, ok := doc[key]
		if !ok || v == nil {
			continue
		}
		if err := f.Set(key, v); err != nil {
			return err
		}
	}
	return f.Write()
}

// bladeKey builds the customModels map key for one external on a P2 spawn:
// blade:127.0.0.1:<mesh port>/models/<slug>.
//
// THE MAP-KEY FORM DECISION (design gate G5-P1): blade: is the clean
// exact-match form. agy derives the path model as the segment after /models/
// (W10), so the mesh receives /v1beta/models/<slug>:streamGenerateContent and
// routes by the bare slug with an exact registry match. gemini-api:// would
// embed the host and path in the model name (and still resolve through the
// terminal-segment rung), and evergreen: carries the scheme prefix into the
// path; both work, blade: is the cleanest. The per-entry host is cosmetic for
// routing (W9: the global GOOGLE_GEMINI_BASE_URL receives all traffic) but it
// makes the key self-describing and unique per mesh port, which keeps a stale
// key from a previous session from resolving against a new mesh.
func bladeKey(meshURL, slug string) string {
	return "blade:" + strings.TrimPrefix(meshURL, "http://") + "/models/" + slug
}

// p2ChildArgs builds the real-agy child argv for one P2 spawn. The injected
// front-door flags come FIRST so agy's flag parser (Go's flag package, which
// stops at the first non-flag token or a literal "--") always consumes them:
// [--app_data_dir <dir>], then exactly one canonical [--model <map key>] when a
// model was pinned, then the caller's remaining args, then a valueless model
// marker LAST when the caller's last model selector was valueless (so agy fails
// cleanly with "flag needs an argument" instead of silently eating the
// confinement flag or dropping to a static default) (FIX-2/FIX-3). reg resolves
// the pinned model onto the exact blade: slug the settings document carries
// (FIX-1).
func p2ChildArgs(args []string, appDataDir, meshURL string, reg *providers.Registry) []string {
	model, rest, valueless := extractChildSelectors(args)
	out := make([]string, 0, len(rest)+4)
	out = append(out, "--app_data_dir", appDataDir)
	if model != "" {
		out = append(out, "--model", p2ModelKey(reg, model, meshURL))
	}
	out = append(out, rest...)
	if valueless != "" {
		out = append(out, valueless)
	}
	return out
}

// splitFlag normalizes a CLI token the way Go's flag package reads it: one or
// two leading dashes, a name, optionally "=value". It returns the name, the
// inline value, whether an inline value was present, and whether the token is a
// named flag at all (a bare "-", a "--" terminator, or a positional is not).
// Go treats -flag and --flag identically, so both dash counts fold to one name.
func splitFlag(a string) (name, value string, hasValue, isFlag bool) {
	if len(a) < 2 || a[0] != '-' {
		return "", "", false, false
	}
	numMinuses := 1
	if a[1] == '-' {
		numMinuses = 2
		if len(a) == 2 {
			return "", "", false, false // "--" terminator
		}
	}
	body := a[numMinuses:]
	if i := strings.IndexByte(body, '='); i >= 0 {
		return body[:i], body[i+1:], true, true
	}
	return body, "", false, true
}

// extractChildSelectors removes the two front-door-owned selectors from args and
// returns the resolved model pin, the remaining args in order, and a valueless
// model marker (empty when the last model selector was valued or absent).
//
// The front door OWNS both selectors for a P2 session, so every caller spelling
// is stripped and rebuilt canonically:
//
//   - --app_data_dir (all four spellings): the per-session dir IS the V6
//     confinement. A surviving caller copy would land in rest AFTER the injected
//     flag and win under Go-flag last-wins, defeating the settings isolation
//     while gemini-mode env is still injected (R5AF2-1). The flag AND its value
//     token are removed so the value never becomes a stray positional.
//   - --model / -m and the dash variants Go's flag package treats as the same
//     flags (-model, --m): the canonical pin is rebuilt from the registry
//     (FIX-1), so every caller spelling is stripped.
//
// Go's flag semantics are modeled: stripping/consuming happens only BEFORE a
// bare "--" terminator. Once "--" is seen it is emitted and every subsequent
// token is copied verbatim into rest (a positional agy will not parse), so a
// caller's post-"--" literal data is never deleted and a post-"--"
// --app_data_dir cannot become a parsed flag (R5AF3-1). The injected
// --app_data_dir/--model are emitted before rest, so agy parses them first.
//
// Last-wins for the model pin matches Go's flag package (FIX-3 / R5AF2-3): the
// LAST selector decides. A valued selector sets the pin (no valueless marker). A
// valueless selector (a trailing "--model", an explicit empty value like
// "--model=", "-m=", or an empty/whitespace-only quoted value) forwards a
// valueless marker so agy rejects it cleanly, never a silent static-default
// session and never a stale pin (R5AF3-3: a whitespace-only value is valueless,
// so agy never receives an empty --model value it would silently accept).
func extractChildSelectors(args []string) (value string, rest []string, valueless string) {
	rest = make([]string, 0, len(args))
	terminated := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if terminated {
			// Past a bare "--": everything is a positional, copied verbatim.
			rest = append(rest, a)
			continue
		}
		if a == "--" {
			terminated = true
			rest = append(rest, a)
			continue
		}
		name, inline, hasInline, isFlag := splitFlag(a)
		if !isFlag {
			rest = append(rest, a)
			continue
		}
		switch name {
		case "app_data_dir":
			// Drop the caller copy in every spelling; consume the value token of
			// the space form so it does not become a stray positional.
			if !hasInline && i+1 < len(args) {
				i++
			}
		case "model", "m":
			switch {
			case hasInline:
				if strings.TrimSpace(inline) != "" {
					value, valueless = inline, ""
				} else {
					// Bare the flag name (drop the "=value" tail) so the forwarded
					// marker is a clean valueless selector, not "--model=  ".
					value, valueless = "", a[:strings.IndexByte(a, '=')]
				}
			case i+1 < len(args) && strings.TrimSpace(args[i+1]) != "":
				value, valueless = args[i+1], ""
				i++
			default:
				// Valueless at end, or an empty / whitespace-only value token:
				// forward a valueless marker so agy rejects cleanly (never
				// "--model ''" that agy would accept as an empty value).
				if i+1 < len(args) {
					i++ // consume the empty / whitespace-only value token
				}
				value, valueless = "", a
			}
		default:
			rest = append(rest, a)
		}
	}
	return value, rest, valueless
}

// p2ModelKey maps a chosen model onto the --model value the child receives:
//
//   - a value already carrying a customModels scheme (gemini-api:/blade:/
//     evergreen:/beyond-api:) passes through verbatim: it is a map key and the
//     launcher may already hold the exact form;
//   - a value that parses to one of the 11 static Gemini ids passes through as
//     the BARE static id, so agy resolves it from its own static catalog and the
//     mesh's static-id policy (StaticIDTarget) decides whether it is servable.
//     The parse runs before the static check, so a static id hidden in a
//     provider-qualified spelling ("<id> (<provider>)" or "<provider>/<id>") is
//     still treated as a static-id question;
//   - anything else is a registry id and is rewritten into the blade: map key
//     the settings document carries. The slug MUST be byte-equal to the id
//     p2Externals keyed (FIX-1): a slash-bearing id ("openai/gpt-4o") that
//     ParseModelSpec would strip to "gpt-4o" is recovered from the registry by
//     its raw id, and a picker label / provider-slash spelling resolves to the
//     same raw id. A bare slug is rejected by agy at --model time (W11), so the
//     rewrite is what makes the pin work;
//   - a value with no registry match is a best-effort blade: key over the parsed
//     candidate (or the trimmed input when nothing parses), which agy rejects
//     with a clear message rather than the pin being silently dropped.
func p2ModelKey(reg *providers.Registry, model, meshURL string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return trimmed
	}
	if hasCustomModelScheme(trimmed) {
		return trimmed
	}
	candidate, _ := providers.ParseModelSpec(trimmed)
	candidate = stripControlChars(strings.TrimSpace(candidate))
	if isStaticGeminiID(candidate) {
		return candidate
	}
	if rawID, ok := p2RegistryRawID(reg, candidate, trimmed); ok {
		return bladeKey(meshURL, rawID)
	}
	if candidate != "" {
		return bladeKey(meshURL, candidate)
	}
	return trimmed
}

// p2RegistryRawID finds the exact raw id p2Externals would key for a chosen
// model, so p2ModelKey builds a blade: key whose slug is byte-equal to the
// customModels entry the settings document carries (FIX-1). It is THREE-PASS so
// an exact raw-id match always beats a display-label match, which always beats a
// stripped-candidate match, regardless of registry order (R5AF2-2, R5BF2-6):
//
//   - pass 1a (exact, unambiguous): the full raw id == the trimmed input;
//   - pass 1b (label convenience): the "<id> (<provider>)" label == the input;
//   - pass 2 (only if both miss): the ParseModelSpec-stripped candidate.
//
// The passes matter because ParseModelSpec strips a "provider/" prefix, so
// pinning "openai/gpt-4o" yields candidate "gpt-4o". A single OR-pass would let
// an EARLIER "gpt-4o" entry (candidate match) shadow the LATER exact
// "openai/gpt-4o", pinning the wrong provider and mis-routing a paid request.
// All passes mirror p2Externals' enabled + reserved-drop + first-wins policy.
//
// R5BF2-6 (why 1a and 1b are separate passes): they were one OR-pass, so a raw
// id that happens to equal ANOTHER model's display label competed with that
// label inside a single registry-order scan. The same input then resolved to
// different providers depending on registry order - the input's meaning changed
// with the file's row order. Splitting them makes the resolution deterministic
// and quality-ordered: an exact raw id (what agy's --model gate needs, and what
// the user typed) always wins over a label convenience spelling.
//
// DOCUMENTED RESIDUAL: when a model's raw id is byte-equal to another model's
// display label, typing that string selects the exact-id row (pass 1a). That is
// deliberate - the string IS that model's id - and the label form stays
// reachable by typing the bare id instead. Nothing validates a registry id
// against the label grammar, so a hand-edited providers.json can still create
// the ambiguity; the shim's answer is now order-independent, which is the part
// that is a defect rather than a documented policy.
func p2RegistryRawID(reg *providers.Registry, candidate, trimmed string) (string, bool) {
	if reg == nil {
		return "", false
	}
	// Pass 1a: an exact raw id wins over every label and candidate match.
	if id, ok := scanRegistryRawID(reg, func(id string, _ providers.Provider) bool {
		return id == trimmed
	}); ok {
		return id, true
	}
	// Pass 1b: the "<id> (<provider>)" display-label convenience spelling.
	if id, ok := scanRegistryRawID(reg, func(id string, p providers.Provider) bool {
		return providers.DisplayName(id, p.Name) == trimmed
	}); ok {
		return id, true
	}
	// Pass 2: the stripped candidate (a "provider/<id>" or "<id> (<provider>)"
	// spelling that no row matches verbatim).
	return scanRegistryRawID(reg, func(id string, _ providers.Provider) bool {
		return id == candidate
	})
}

// scanRegistryRawID scans the registry IN ORDER for the first ENABLED,
// non-reserved model whose normalized id satisfies match, returning its raw id.
// It mirrors p2Externals' policy exactly (same reserved set, same enabled skip,
// same first-wins), so a reserved-drop or a cross-provider duplicate that
// p2Externals dropped is never matched here.
func scanRegistryRawID(reg *providers.Registry, match func(id string, p providers.Provider) bool) (string, bool) {
	reserved := reservedP2Names()
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if !m.IsEnabled() {
				continue
			}
			id := stripControlChars(strings.TrimSpace(m.ID))
			if id == "" || reserved[strings.ToLower(id)] {
				continue
			}
			if match(id, p) {
				return id, true
			}
		}
	}
	return "", false
}

// hasCustomModelScheme reports whether model already carries one of the four
// customModels key schemes.
func hasCustomModelScheme(model string) bool {
	for _, scheme := range customModelSchemes {
		if strings.HasPrefix(model, scheme) {
			return true
		}
	}
	return false
}

// p2NativeFallback execs the real agy with NO mesh, NO gemini-mode env and NO
// settings write, after printing a clear notice to the child's stderr. It is
// the P2 spelling of launchNativeFallback: the promise made to the user names
// P2's own injection (GOOGLE_GEMINI_BASE_URL), so the notice says what is
// actually absent. The child environment is still scrubbed of provider key
// VALUES (S-3 precedent): the keys stay in this process for the mesh, never in
// the child.
//
// R5B-FIX-2: the launcher pins `--model <id>` BEFORE the wirecheck gate runs, so
// on every fallback path (NOGO, a P2 profile that is not ProfileOK, an
// unreadable registry, zero enabled externals) opts.Args may carry a
// mesh-specific pin. Native agy has no mesh and no customModels document to
// answer it, so the pin is stripped here (nativeFallbackArgs, the seam P1's
// launchNativeFallback shares) and the session runs as a TRUE native session -
// the FIX-53 posture. A pin native agy CAN resolve on its own (a static Gemini
// id) is kept, so a chosen model is never silently swapped for a different one.
func p2NativeFallback(realAgy string, opts LaunchOptions, reg *providers.Registry, notice string) (int, error) {
	if notice != "" {
		// FIX-6/FIX-65 + R5AF3-5: mask + control-strip + path-withhold the notice
		// through the shared renderFallbackNotice (same rendering as P1's
		// launchNativeFallback). The P2 sources are already class-labeled or carry
		// only public binary strings, so this is defense-in-depth against a
		// secret-shaped, control-bearing, or path-shaped fragment.
		fmt.Fprintf(stderrOr(opts.Stderr),
			"[agy-pro] %s - falling back to native agy (no mesh, no GOOGLE_GEMINI_BASE_URL, no settings write)\n",
			renderFallbackNotice(notice))
	}
	args := nativeFallbackArgs(opts.Args)
	if dropped := nativeFallbackDroppedPin(opts.Args); dropped != "" {
		// Disclosed, never silent: the user picked (or typed) a model this
		// fallback cannot serve, so the line names it and states what runs.
		// The rendering is the shared printDroppedPinNotice, so P1's twin
		// (launchNativeFallback) emits the identical line.
		printDroppedPinNotice(stderrOr(opts.Stderr), dropped)
	}
	cmd := exec.Command(realAgy, args...)
	cmd.Env = realagy.CleanChildEnv(ChildKeyScrubNames(reg))
	cmd.Env = append(cmd.Env, opts.ChildEnv...)
	cmd.Stdin = stdinOr(opts.Stdin)
	cmd.Stdout = stdoutOr(opts.Stdout)
	cmd.Stderr = stderrOr(opts.Stderr)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		// FIX-6/FIX-65: MaskOperand exempts locations, so a real-agy PATH would
		// ride raw. Report a class label instead of the path; the wrapped error
		// carries the class.
		return 1, fmt.Errorf("failed to run the real agy binary: %w", err)
	}
	return 0, nil
}

// p2FallbackArgs returns the argv the native fallback execs: the caller's args
// with the mesh-specific --model pin removed (R5B-FIX-2).
//
// It is a thin alias over the shared nativeFallbackArgs: the strip policy is
// identical for both profiles (a P1 mesh-catalog id and a P2 blade: key are both
// unresolvable by native agy), so the two fallback twins share one implementation
// instead of a copy that can drift. See nativeFallbackArgs for the full policy.
func p2FallbackArgs(args []string) []string { return nativeFallbackArgs(args) }

// p2NativePinValue returns the BARE static Gemini id a pinned --model value
// parses to, and whether it parses to one at all. It is the R5BF2-2
// normalization: "kept" must mean "native-resolvable", and native agy's --model
// gate accepts a static id only in its bare catalog spelling, so the decorated
// input spelling is never forwarded.
func p2NativePinValue(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "", false
	}
	candidate := stripControlChars(strings.TrimSpace(mustParseModelSpec(trimmed)))
	if !isStaticGeminiID(candidate) {
		return "", false
	}
	return candidate, true
}

// mustParseModelSpec is providers.ParseModelSpec's first return value; it never
// fails (an unparseable spec yields the trimmed input as the base), so the
// two-value form is noise at every call site.
func mustParseModelSpec(spec string) string {
	base, _ := providers.ParseModelSpec(spec)
	return base
}

// p2DroppedPin returns the pinned model value p2FallbackArgs removes, or "" when
// nothing was dropped. It is the P2 spelling of the shared nativeFallbackDroppedPin
// (see there for the disclosure contract); a pin that is KEPT in a normalized bare
// spelling is not a drop, so nothing is disclosed for it.
func p2DroppedPin(args []string) string { return nativeFallbackDroppedPin(args) }

// p2StripModelSelector removes every model selector (--model / -m and the dash
// variants Go's flag package folds to them, space or equals form, plus a
// valueless marker) from args and returns the remaining tokens in order. It is
// the native fallback's use of the shared selector-strip seam: the front door
// OWNS the model selector for a P2 session, so a mesh-specific pin must never
// reach an exec that has no mesh to answer it.
//
// Every other token - including a caller --app_data_dir - is preserved, so the
// fallback stays "the caller's args verbatim minus the pin". Flag semantics
// mirror extractChildSelectors: stripping stops at a bare "--" terminator, so a
// post-terminator literal is never deleted.
func p2StripModelSelector(args []string) []string {
	if !p2HasModelSelector(args) {
		return args
	}
	out := make([]string, 0, len(args))
	terminated := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if terminated {
			out = append(out, a)
			continue
		}
		if a == "--" {
			terminated = true
			out = append(out, a)
			continue
		}
		name, _, hasInline, isFlag := splitFlag(a)
		if !isFlag || (name != "model" && name != "m") {
			out = append(out, a)
			continue
		}
		if !hasInline && i+1 < len(args) {
			i++ // consume the value token so it cannot become a stray positional
		}
	}
	return out
}

// p2HasModelSelector reports whether args carry any model selector before a
// bare "--" terminator.
func p2HasModelSelector(args []string) bool {
	terminated := false
	for _, a := range args {
		if terminated {
			return false
		}
		if a == "--" {
			terminated = true
			continue
		}
		name, _, _, isFlag := splitFlag(a)
		if isFlag && (name == "model" || name == "m") {
			return true
		}
	}
	return false
}

// p2NogoNotice renders the clear fallback notice for a wirecheck report that
// gates a P2 launch. It distinguishes the two ways the gate trips (FIX-7):
//   - a VerdictNOGO report (a canary missing or the version probe hard-failed)
//     renders the NOGO notice with the public reasons;
//   - a GO report whose P2 profile is simply not ProfileOK (no P2 canaries
//     verified) renders a DISTINCT notice, because "NOGO" would be misleading:
//     the binary passed some profile's gate, just not P2's.
func p2NogoNotice(report wirecheck.Report) string {
	if report.Verdict() == wirecheck.VerdictNOGO {
		msg := "wirecheck NOGO for profile P2; refusing to start the mesh or write session settings"
		if reasons := report.Reasons(); len(reasons) > 0 {
			msg += ": " + renderWirecheckReasons(reasons)
		}
		return msg
	}
	return "wirecheck has no P2 canaries for this binary; refusing to start the mesh or write session settings"
}

// p2DropSink is the P2 spelling of p1CatalogOptions' Logf: a masked,
// control-stripped stderr sink for the customModels drop reasons, so a reserved
// or duplicate registry id is observable in production instead of silently
// vanishing from the session (FIX-50/FIX-51 parity).
func p2DropSink(errw io.Writer) func(format string, args ...any) {
	return func(format string, args ...any) {
		line := stripControlChars(providers.MaskOperand(fmt.Sprintf(format, args...)))
		fmt.Fprintln(errw, "[agy-pro] "+line)
	}
}

// ---------------------------------------------------------------------------
// --model rewrite fallback (design gate G5-P1 safety net; ADDITIVE)
//
// Contract: when the child agy rejects the pinned --model form with the exact
// signature below, retry ONCE with the alternate spelling, then surface the
// result. AT MOST ONE retry, never a loop.
//
// WHY THIS IS A SAFETY NET AND NOT THE MECHANISM: p2ModelKey already rewrites
// the pin into the exact customModels map key the session's settings document
// carries, and agy's --model gate accepts a value iff it is a static-catalog id
// or a customModels map key (binary-surface-report.md §1.3). The primary form is
// therefore the known-good one. The net exists because design §2 P2 leaves the
// exact map-key form ("plain slug vs scheme URL") to gate G5's empirical
// determination: if that assumption is wrong for the binary in the field, one
// extra attempt recovers the session instead of failing it.
//
// HONEST STATUS ON v1.3.5: the alternate spelling (the bare registry slug) is
// the form agy is recorded as REJECTING (custommodels-probe-report.md FACTS #4
// and §4: "the same rejection hits the bare modelName even in gemini mode:
// only the full URL map-key resolves"), so against the facts this net is
// DORMANT - it cannot recover, because neither spelling is a map key when the
// primary is not. It is implemented anyway, tested, and SEAMED
// (p2AltModelForm) so G5's empirical finding lands in one place instead of
// reopening the front door. See docs/261014_0007_session_hybrid-impl/
// hybrid-u9b-coder-report.md §3 for the full reasoning and the one change that
// would make it live (a second customModels key per external).
// ---------------------------------------------------------------------------

// p2RejectSignature is the EXACT agy --model rejection signature the fallback
// watches for (binary-surface-report.md §1.3, the --model gate; the live string
// from custommodels-probe-report.md §4):
//
//	error: invalid model selection (--model "<pin>" --effort ""): model <pin> is not recognized as a known model or custom model in settings
//
// Only this substring matches, so a mid-session error that merely mentions a
// model never triggers a retry.
const p2RejectSignature = "not recognized as a known model or custom model in settings"

// p2FallbackNotice is the one line printed before the single retry. It names
// the signature (a public binary string) and states that this is the last
// attempt.
const p2FallbackNotice = "[agy-pro] agy rejected the pinned --model form (%s); retrying once with the alternate spelling"

// p2SupervisePoll is mesh's exported DefaultChildPollInterval (the same value,
// referenced rather than copied so it cannot drift).
// p2SuperviseProbeTimeout / p2SuperviseReapTimeout mirror mesh's unexported
// childProbeTimeout / childReapTimeout; they are duplicated rather than exported
// because internal/mesh is outside this unit's edit scope. The values are pinned
// here so a future mesh change that moves them is visible in the diff that
// touches this file.
const (
	p2SupervisePoll         = mesh.DefaultChildPollInterval
	p2SuperviseProbeTimeout = 2 * time.Second
	p2SuperviseReapTimeout  = 5 * time.Second
)

// p2FallbackArm decides whether the rewrite fallback is armed for one launch.
// It is a seam so tests can drive the retry deterministically without depending
// on the host's stdio; the default is p2FallbackArmed.
var p2FallbackArm = p2FallbackArmed

// p2FallbackArmed arms the fallback for PRINT-SHAPED invocations only
// (-p/--print/--prompt, in every spelling agy accepts). Two reasons, both
// load-bearing:
//
//   - Detection needs a TEE, and a tee makes os/exec hand the child a PIPE
//     instead of the real console handle. agy's TUI degrades on a non-terminal
//     stdout, so an interactive launch must never be teed.
//   - A print run is exactly where a rejected pin is a total loss: the one-shot
//     exits 1 and nothing recovers. An interactive launch surfaces agy's own
//     error to the user, who can re-run.
//
// The residual is stated plainly: a bare or --model-only launch on a console
// (a TUI session) keeps the real handles and today's behavior - the child's
// error surfaces unchanged, no retry. Gate G5's e2e runs are print-shaped, so
// the armed path is the one G5 exercises.
func p2FallbackArmed(args []string) bool {
	return p2HasPromptFlag(args)
}

// p2HasPromptFlag reports whether args carry a one-shot prompt flag
// (-p/--print/--prompt, space or equals form) that agy's flag parser actually
// sees. It mirrors the launcher's decide.hasPromptFlag with ONE deliberate
// difference: it stops at a bare "--" terminator, because Go's flag package
// (agy's parser) does. A prompt flag after "--" is a positional, so the
// invocation is NOT print-shaped.
//
// The asymmetry with the launcher's copy is intentional. The launcher uses its
// answer to skip the interactive MENU, where a false positive is harmless. This
// answer decides whether the child's stdout may be wrapped in a pipe (the tee
// the detector needs), and a false positive there would hand agy's TUI a
// non-terminal stdout. The safe direction for a fidelity question is "do not
// arm".
func p2HasPromptFlag(args []string) bool {
	terminated := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if terminated {
			continue
		}
		if a == "--" {
			terminated = true
			continue
		}
		if a == "-p" || a == "--print" || a == "--prompt" {
			return true
		}
		if strings.HasPrefix(a, "-p=") || strings.HasPrefix(a, "--print=") || strings.HasPrefix(a, "--prompt=") {
			return true
		}
	}
	return false
}

// p2AltModelForm returns the ALTERNATE --model spelling for a pinned model: the
// bare registry slug, i.e. the id without the blade: rewrite p2ModelKey
// applies. It is a package-level seam so gate G5's empirical determination of
// the accepted form lands here, without touching the retry driver.
//
// It returns "" (no alternate, no retry) for every value that has no alternate
// spelling to try:
//   - a value that already carries a customModels scheme (it IS a map key);
//   - a static Gemini id (p2ModelKey passes it through verbatim, so the primary
//     is already the exact form agy resolves from its own catalog);
//   - a blank or unparseable value.
var p2AltModelForm = func(reg *providers.Registry, model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" || hasCustomModelScheme(trimmed) {
		return ""
	}
	candidate, _ := providers.ParseModelSpec(trimmed)
	candidate = stripControlChars(strings.TrimSpace(candidate))
	if candidate == "" || isStaticGeminiID(candidate) {
		return ""
	}
	if rawID, ok := p2RegistryRawID(reg, candidate, trimmed); ok {
		return rawID
	}
	return candidate
}

// p2RunChild execs the real-agy child for one P2 spawn and returns the outcome
// mesh.RunChild's contract describes (nil, *exec.ExitError, or an error wrapping
// mesh.ErrMeshDied). primaryArgv/childEnv are the argv and environment LaunchP2
// already built for the primary form; they are reused verbatim by the retry, so
// the retry differs from the first attempt in exactly one token.
//
// Disarmed (the reviewed path, byte-for-byte): meshRunChild supervises the one
// child and stops the mesh when it exits.
//
// Armed: attempt 1 runs under the SAME mesh with its STDERR teed through the
// bounded rejection detector. If the child FAILED (a non-zero exit) AND the
// exact signature streamed past, attempt 2 runs ONCE with the alternate spelling
// under the same mesh; whatever attempt 2 does is final. The mesh is never
// restarted between attempts: the blade: key embeds the mesh port and the child
// env points at it, so a restart would invalidate both.
//
// The retry reuses the caller's stdin reader as-is. That is safe on the only
// shape the fallback is armed for (print mode, where agy does not read stdin);
// a TUI launch is never armed, so an interactive stdin is never replayed into a
// second child.
func p2RunChild(ctx context.Context, m *mesh.Mesh, realAgy string, primaryArgv []string, childEnv []string, opts LaunchOptions, appDataDir string, reg *providers.Registry) error {
	errw := stderrOr(opts.Stderr)

	alt := ""
	if model, _, valueless := extractChildSelectors(opts.Args); p2FallbackArm(opts.Args) && model != "" && valueless == "" {
		if a := p2AltModelForm(reg, model); a != "" && a != p2ModelKey(reg, model, m.URL()) {
			alt = a
		}
	}
	if alt == "" {
		return meshRunChild(ctx, m, p2ChildCmd(realAgy, primaryArgv, childEnv, opts, nil))
	}

	// Attempt 1: the primary form, stderr teed through the detector.
	det := &p2RejectDetector{}
	runErr := p2SuperviseChild(ctx, m, p2ChildCmd(realAgy, primaryArgv, childEnv, opts, det), errw)
	// TWO signals are required, and the order does not matter:
	//
	//  1. the child FAILED - a non-zero exit (*exec.ExitError). A nil runErr is
	//     a SUCCESSFUL attempt 1 and must never be re-executed: the signature is
	//     public text that a correct model response can quote verbatim (the user
	//     asked for it, or injected content made agy echo it), and re-running a
	//     successful one-shot would double the output, double any provider
	//     spend, and flip a 0 exit into the retry's failure (R5B-FIX-1).
	//  2. the detector latched on agy's exact rejection signature.
	//
	// The same test makes every other outcome final: a mesh death
	// (mesh.ErrMeshDied), a context cancel/deadline and a spawn failure are not
	// *exec.ExitError, so none of them can reach the retry.
	if !det.rejected() || !p2ChildFailed(runErr) {
		return runErr
	}

	fmt.Fprintf(errw, p2FallbackNotice+"\n", p2RejectSignature)
	// Attempt 2 (final): the alternate spelling, same mesh, same env, same stdio.
	return p2SuperviseChild(ctx, m, p2ChildCmd(realAgy, p2ChildArgsPinned(opts.Args, appDataDir, alt), childEnv, opts, nil), errw)
}

// p2ChildFailed reports whether runErr is a REAL child failure: a non-zero exit
// reported as *exec.ExitError. It is the R5B-FIX-1 half of the retry gate that
// keeps a successful attempt from ever being re-executed.
//
// Every non-child outcome is deliberately excluded by the same test:
//   - nil (the child exited 0) -> not a failure;
//   - an error wrapping mesh.ErrMeshDied -> the session is dead, not rejected;
//   - context.Canceled / context.DeadlineExceeded -> the session was torn down;
//   - a spawn failure -> nothing ran, so there is no rejection to recover from.
func p2ChildFailed(runErr error) bool {
	var exitErr *exec.ExitError
	return errors.As(runErr, &exitErr)
}

// p2ChildArgsPinned builds the child argv with a VERBATIM --model value. It is
// the fallback's spelling of p2ChildArgs: the same front-door-first shape
// (--app_data_dir, then the pin, then the caller's args, then a valueless
// marker last) with NO p2ModelKey rewrite, so the alternate spelling reaches
// agy byte-for-byte. A valueless selector emits no pin at all, exactly as
// p2ChildArgs does.
func p2ChildArgsPinned(args []string, appDataDir, pinned string) []string {
	_, rest, valueless := extractChildSelectors(args)
	out := make([]string, 0, len(rest)+4)
	out = append(out, "--app_data_dir", appDataDir)
	if pinned != "" && valueless == "" {
		out = append(out, "--model", pinned)
	}
	out = append(out, rest...)
	if valueless != "" {
		out = append(out, valueless)
	}
	return out
}

// p2ChildCmd builds the child command for one attempt: the argv, the mesh
// environment and the stdio. det is the optional rejection detector.
//
// R5B-FIX-1: the detector is attached to STDERR ONLY. agy's `--model` rejection
// is a startup error line on stderr, while the model's own answer streams on
// stdout; teeing stdout as well let a correct response that merely quotes the
// public signature latch the detector. stdout therefore goes STRAIGHT to the
// real writer (no pipe, no detector), and only stderr is wrapped in the
// streaming tee. Missing a stdout-borne rejection now means "no retry", which
// is exactly the pre-fallback behavior - the safe direction.
//
// When det is nil both streams are handed over directly, which is the reviewed
// path's exact behavior.
func p2ChildCmd(realAgy string, argv []string, childEnv []string, opts LaunchOptions, det *p2RejectDetector) *exec.Cmd {
	cmd := exec.Command(realAgy, argv...)
	cmd.Env = childEnv
	cmd.Stdin = stdinOr(opts.Stdin)
	cmd.Stdout = stdoutOr(opts.Stdout)
	cmd.Stderr = p2ChildWriter(stderrOr(opts.Stderr), det)
	return cmd
}

// p2ChildWriter wraps real in the tee when a detector is attached.
func p2ChildWriter(real io.Writer, det *p2RejectDetector) io.Writer {
	if det == nil {
		return real
	}
	return &p2Tee{real: real, det: det}
}

// p2Tee streams every byte to the REAL writer AND to the bounded detector, so
// the session renders in real time exactly as it would without the fallback.
// os/exec sees a plain io.Writer, so it hands the child a pipe and copies
// concurrently; nothing is buffered for the session's lifetime.
//
// R5B-FIX-1: the tee is attached to the child's STDERR only (p2ChildCmd). The
// model's answer on stdout never passes through it, so a response that quotes
// the public signature cannot latch the detector.
type p2Tee struct {
	real io.Writer
	det  *p2RejectDetector
}

func (t *p2Tee) Write(p []byte) (int, error) {
	n, err := t.real.Write(p)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	t.det.write(p)
	return len(p), nil
}

// p2RejectDetector is a bounded streaming detector for p2RejectSignature on the
// child's STDERR. It keeps at most len(signature)-1 bytes between writes (the
// longest suffix that could still complete a signature split across two Write
// calls), so a whole session can stream through it without ever being buffered.
// Write is called from os/exec's stderr copier goroutine, so the state is
// guarded.
type p2RejectDetector struct {
	mu   sync.Mutex
	tail []byte
	hit  bool
}

// write feeds one chunk to the detector. Once the signature is seen the
// detector latches and stops scanning: there is nothing left to look for.
func (d *p2RejectDetector) write(p []byte) {
	if d == nil || len(p) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.hit {
		return
	}
	buf := append(d.tail, p...)
	if bytes.Contains(buf, []byte(p2RejectSignature)) {
		d.hit = true
		d.tail = nil
		return
	}
	keep := len(p2RejectSignature) - 1
	if keep > len(buf) {
		keep = len(buf)
	}
	d.tail = append([]byte(nil), buf[len(buf)-keep:]...)
}

// rejected reports whether the exact signature has streamed past.
func (d *p2RejectDetector) rejected() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hit
}

// p2SuperviseChild runs ONE child under a mesh that must OUTLIVE it. It is the
// fallback's spelling of mesh.RunChild: the same refuse-to-launch health gate,
// the same child-exit-wins tie-break, the same mesh-death fail-fast, the same
// context-cancel kill, and the same visible fail-fast line - with ONE
// difference that is the whole point. mesh.RunChild stops the mesh when its
// child exits and refuses a second child, so it can supervise exactly one
// attempt; this supervisor leaves the mesh UP, so a second attempt can run
// under the same mesh. The caller's deferred Stop is the single teardown point.
func p2SuperviseChild(ctx context.Context, m *mesh.Mesh, cmd *exec.Cmd, errw io.Writer) error {
	if err := m.EnsureHealthy(ctx); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		// B2/U8 + FIX-65, stated exactly (R5B-FIX-4): providers.MaskOperand masks
		// SECRET-shaped operands, so a misdirected key paste in the binary path is
		// closed here. It deliberately EXEMPTS location-shaped operands
		// (providers/keyaudit.go: "spawn errors must name the binary"), so an
		// ordinary C:\Users\...\agy.exe path still rides this error line - the
		// same posture launcher.go's passthrough exec error takes. This is NOT a
		// class-label withhold; if path-withholding is wanted here it needs the
		// noticePathShapeRe treatment, which is a follow-up decision.
		return fmt.Errorf("hybrid P2: cannot start the real agy child: %s", providers.MaskOperand(err.Error()))
	}
	// Buffered so the waiter goroutine never blocks on a send for a wedged
	// child that outlives the reap budget.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	poll := time.NewTicker(p2SupervisePoll)
	defer poll.Stop()
	for {
		select {
		case err := <-waitCh:
			// The child exited on its own (any code): its error is the outcome.
			return err
		case <-poll.C:
			// The child's own exit wins a tie, exactly as in mesh.RunChild.
			select {
			case err := <-waitCh:
				return err
			default:
			}
			probeCtx, cancel := context.WithTimeout(context.Background(), p2SuperviseProbeTimeout)
			probeErr := m.Healthz(probeCtx)
			cancel()
			if probeErr == nil {
				continue
			}
			// Re-check the child once more before declaring the session dead.
			select {
			case err := <-waitCh:
				return err
			default:
			}
			return p2FinishMeshDeath(m, cmd, waitCh, probeErr, errw)
		case <-ctx.Done():
			p2KillAndReap(cmd, waitCh)
			return fmt.Errorf("hybrid P2: the supervised child (pid %d) was killed because the session context ended: %w", p2ChildPID(cmd), ctx.Err())
		}
	}
}

// p2FinishMeshDeath handles a mesh that died while the child lived: write the
// same visible fail-fast line mesh.RunChild writes, kill the child, reap it,
// and return the error p1ExitCode maps to ExitMeshDied (75). The mesh itself is
// NOT stopped here: the caller's deferred Stop is the single teardown point and
// a mesh this supervisor never registered a child with can be stopped safely.
func p2FinishMeshDeath(m *mesh.Mesh, cmd *exec.Cmd, waitCh <-chan error, probeErr error, errw io.Writer) error {
	pid := p2ChildPID(cmd)
	if errw != nil {
		fmt.Fprintf(errw, "mesh: %s (mesh %s, port %d; health probe failed: %v); the supervised child (pid %d) was killed\n",
			mesh.ErrMeshDied.Error(), m.URL(), m.Port(), probeErr, pid)
	}
	p2KillAndReap(cmd, waitCh)
	return fmt.Errorf("%w: the health probe on %s (port %d) failed: %v", mesh.ErrMeshDied, m.URL(), m.Port(), probeErr)
}

// p2KillAndReap terminates the child and waits (bounded) for it to be reaped.
func p2KillAndReap(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-waitCh:
	case <-time.After(p2SuperviseReapTimeout):
	}
}

// p2ChildPID returns the child's pid, or 0 when it was never started.
func p2ChildPID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}
