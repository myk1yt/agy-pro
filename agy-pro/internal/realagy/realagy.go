// Package realagy locates the authentic agy binary and builds the scrubbed
// environment for real-agy child processes spawned by the shim (native fanout
// seats and `agy models` catalog fetches). It is a leaf package (stdlib +
// internal/paths + internal/providers) so fanout can use it without the
// launcher import cycle (contract §2.2.1, D-1).
package realagy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// ErrNotFound is returned when the original agy binary cannot be located.
var ErrNotFound = errors.New("real agy binary not found")

// ErrRecursion is returned when resolution lands back on the agy-pro shim.
var ErrRecursion = errors.New("infinite recursion detected: resolved back to agy-pro shim")

// envBOM is the UTF-8 byte order mark (U+FEFF) that text editors occasionally
// prepend to pasted environment-variable names (providers/envkeys.go:6-10).
const envBOM = "\ufeff"

// Resolve locates the real agy binary with the launcher precedence:
// AGY_PRO_REAL_AGY > PATH scan (excluding shim dir + ~/.agy-pro/bin + self)
// > paths.DefaultRealAgyPath() > Windows secondary ~/.gemini/bin/agy.exe.
func Resolve() (string, error) {
	selfPath, err := os.Executable()
	if err != nil {
		selfPath = ""
	} else {
		selfPath, _ = filepath.EvalSymlinks(selfPath)
	}

	envReal := os.Getenv(paths.EnvAgyProRealAgy)
	pathEnv := os.Getenv("PATH")
	fallback, _ := paths.DefaultRealAgyPath()

	return ResolveWith(selfPath, envReal, pathEnv, fallback)
}

// ResolveWith is the pure, injectable core of Resolve (same decomposition as
// launcher.resolveRealAgy):
//  1. AGY_PRO_REAL_AGY override
//  2. PATH search (excluding the shim directory, ~/.agy-pro/bin, and the shim
//     file itself; Windows candidate order agy.exe, agy.cmd, agy.bat, agy)
//  3. fallback path, then the Windows secondary ~/.gemini/bin/agy.exe
func ResolveWith(selfPath, envReal, pathEnv, fallback string) (string, error) {
	// 1. AGY_PRO_REAL_AGY override
	if trimmed := strings.TrimSpace(envReal); trimmed != "" {
		cleaned := filepath.Clean(trimmed)
		if fi, err := os.Stat(cleaned); err == nil && !fi.IsDir() {
			if selfPath != "" && isSameFile(selfPath, cleaned) {
				return "", fmt.Errorf("%w: AGY_PRO_REAL_AGY points to shim itself (%s)", ErrRecursion, cleaned)
			}
			return cleaned, nil
		}
		return "", fmt.Errorf("%w: configured path %q does not exist", ErrNotFound, trimmed)
	}

	var selfDir string
	if selfPath != "" {
		selfDir = filepath.Clean(filepath.Dir(selfPath))
	}

	agyProBinDir, _ := paths.AgyProBin()
	if agyProBinDir != "" {
		agyProBinDir = filepath.Clean(agyProBinDir)
	}

	// Candidates to check in each PATH entry (.exe first on Windows)
	binaryNames := []string{"agy"}
	if runtime.GOOS == "windows" {
		binaryNames = []string{"agy.exe", "agy.cmd", "agy.bat", "agy"}
	}

	// 2. PATH search
	for _, dir := range filepath.SplitList(pathEnv) {
		trimmedDir := strings.TrimSpace(dir)
		if trimmedDir == "" {
			continue
		}
		cleanDir := filepath.Clean(trimmedDir)

		// Exclude directory containing the current running shim
		if selfDir != "" && isSameDir(selfDir, cleanDir) {
			continue
		}

		// Exclude ~/.agy-pro/bin (where installed shim agy.exe resides)
		if agyProBinDir != "" && isSameDir(agyProBinDir, cleanDir) {
			continue
		}

		for _, name := range binaryNames {
			candidate := filepath.Join(cleanDir, name)
			fi, err := os.Stat(candidate)
			if err != nil || fi.IsDir() {
				continue
			}

			// Ensure candidate is not the shim binary itself
			if selfPath != "" && isSameFile(selfPath, candidate) {
				continue
			}

			return candidate, nil
		}
	}

	// 3. Fallback check
	if fallback != "" {
		cleanedFallback := filepath.Clean(fallback)
		if fi, err := os.Stat(cleanedFallback); err == nil && !fi.IsDir() {
			if selfPath == "" || !isSameFile(selfPath, cleanedFallback) {
				return cleanedFallback, nil
			}
		}
	}

	// Fallback secondary check on Windows: ~/.gemini/bin/agy.exe
	if runtime.GOOS == "windows" {
		if home, err := paths.UserHome(); err == nil {
			geminiAgy := filepath.Join(home, ".gemini", "bin", "agy.exe")
			if fi, err := os.Stat(geminiAgy); err == nil && !fi.IsDir() {
				if selfPath == "" || !isSameFile(selfPath, geminiAgy) {
					return geminiAgy, nil
				}
			}
		}
	}

	return "", fmt.Errorf("%w: please install official agy or set %s to its path", ErrNotFound, paths.EnvAgyProRealAgy)
}

// fixedScrubNames are always removed from a real-agy child environment:
// the four gateway vars (BuildGatewayEnv, launcher.go:488-493 — a native seat
// must never be diverted into gateway mode, F-ENV), the explicit-bridge vars
// (ShouldActivateBridge, launcher.go:427, defense-in-depth), the ambient Gemini
// credentials, and the two redirect URLs.
//
// GEMINI_API_KEY / GOOGLE_API_KEY: an ambient key in the user's shell must
// never ride into a native or hybrid child. Spec §10.3 mandates scrubbing both
// (agy prefers GOOGLE_API_KEY when both are set, so an inherited value
// overrides the shim's own injection); design F-16/R8 requires the clean
// credential posture. providers.GoogleAIStudioKeyEnv is the SAME name from the
// registry side (presets.go:31) — the collision is deliberate and disclosed.
//
// GOOGLE_GEMINI_BASE_URL / CLOUD_CODE_URL: an inherited value must never divert
// a "pure native" child (the native fallback at hybrid/launch.go:20-32, whose
// notice promises "no mesh, no CLOUD_CODE_URL") onto a foreign endpoint, and
// must never divert a P2 gemini-mode child off the shim's mesh. Real agy honors
// both (wirecheck canaries cloud_code_url_env / env_google_gemini_base_url).
//
// P2 INJECTION ORDER (spec §2 P2, mandatory): the P2 front door execs the child
// with GOOGLE_GEMINI_BASE_URL=<mesh URL> + GEMINI_API_KEY=<install nonce>, and
// BOTH must be appended AFTER this scrub (setEnvKV last-wins dedup,
// launcher.go:524-540). Scrub-then-append is what removes the user's real key
// and still lets the nonce win; appending before the scrub would delete the
// nonce, and scrubbing after would delete the user's key but leave the nonce
// dependent on exec's dedup. internal/hybrid has no p2.go yet, so the order is
// pinned only by TestCleanChildEnv_AppendAfterScrubInjectionLands here.
var fixedScrubNames = []string{
	"AGY_LLM_GATEWAY_URL",
	"AGY_LLM_GATEWAY_WIRE_PROTOCOL",
	"AGY_LLM_GATEWAY_API_KEY",
	"AGY_LLM_GATEWAY_MODELS",
	"AGY_PRO_BRIDGE",
	"AGY_BRIDGE",
	"GEMINI_API_KEY",
	"GOOGLE_API_KEY",
	"GOOGLE_GEMINI_BASE_URL",
	"CLOUD_CODE_URL",
}

// normalizeEnvName folds an environment-variable name to its canonical
// comparison form: every U+FEFF anywhere in the name is removed, surrounding
// whitespace is trimmed, and the name is lower-cased
// (normalizeEnvName-equivalent, providers/envkeys.go:36-39, F-Db, plus the
// case fold).
//
// The case fold is load-bearing (round-4 M-1): Windows environment names are
// case-insensitive (os.Getenv resolves "openai_api_key" for a stored "OPENAI_API_KEY")
// while os.Environ preserves the stored case, so an exact-case comparison let a
// case variant of a key name walk straight into the child, where agy and
// everything it spawns (agent tools, MCP servers — the S-3 threat model) reads
// it case-insensitively. Folding is applied to BOTH sides of the comparison
// because both go through here. It is unconditional (not Windows-gated) so the
// behavior is platform-independent and pinnable: the scrub set holds only key,
// gateway, and redirect names, so over-scrubbing a case variant of a benign
// name is impossible. Precedents: launcher.go:524-540 (setEnvKV,
// strings.EqualFold) and wirecheck/version.go:47-59 (nameInAllowList).
func normalizeEnvName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(name), envBOM, "")))
}

// apiKeyNameSuffix is the case-folded suffix that marks an environment variable
// as a provider API-key VALUE. CleanChildEnvWithBase scrubs ANY name ending in
// it, not only the registry apiKeyEnv names and the compiled presets, so the
// recurring S-3 class is closed for every current and future *_API_KEY-shaped
// provider - including an ambient key whose provider is absent from
// providers.json AND is not a compiled preset (e.g. OPENROUTER_API_KEY on a
// machine whose registry configures only deepseek). Name-based scrubbing alone
// cannot see such a key (round-7 Finding 1 / M4F-1); the shape rule can.
//
// Why it is safe to scrub every *_API_KEY from a real-agy child: the child
// never needs a provider key VALUE in any mode. Native seats use agy's keyring;
// P1/P2 use the local mesh + the install nonce; the bridge uses the per-session
// AGY_LLM_GATEWAY_API_KEY; and keys are otherwise resolved in-process from their
// env-var NAME (Provider.KeyValue). The two *_API_KEY names a child DOES need
// (GEMINI_API_KEY, AGY_LLM_GATEWAY_API_KEY) are already in fixedScrubNames and
// are re-appended AFTER this scrub by their callers (the P2 install nonce,
// BuildGatewayEnv), so the suffix rule never drops a value the child must keep.
// normalizeEnvName already lower-cases, so the suffix is compared lower-case.
const apiKeyNameSuffix = "_api_key"

// F-10B (round-12 security): the *_API_KEY shape covered only one of the
// credential naming conventions an operator shell carries. A child that reads
// no credential at all (the same argument as apiKeyNameSuffix) also never needs
// a *_TOKEN, *_SECRET or bare *_KEY value, so the shape rule now covers those
// too: GITHUB_TOKEN (gh auth), AWS_SECRET_ACCESS_KEY (the _key arm, since that
// name does not end in _secret), any *_CLIENT_SECRET, any *_ACCESS_KEY. The
// suffix list is lower-case because normalizeEnvName already folded the name.
//
// Over-scrub safety for the wider arms: the two names a child DOES need
// (GEMINI_API_KEY, AGY_LLM_GATEWAY_API_KEY) are re-appended after the scrub by
// their callers (see the P2 INJECTION ORDER note on fixedScrubNames), and the
// case fold is unconditional, so no platform-specific name reaches a child
// half-scrubbed. One disclosed side effect of the _token arm: a native child
// spawned from inside an agy session no longer inherits the parent session's
// ANTIGRAVITY_LS_CSRF_TOKEN (a real credential for the parent's loopback
// language server); a fresh print-mode child starts its own session and has no
// use for the parent's token.
const (
	tokenNameSuffix  = "_token"
	secretNameSuffix = "_secret"
	keyNameSuffix    = "_key"
)

// providerKeyNameSuffixes is the closed shape set the scrub applies. api_key is
// kept as the named original even though key subsumes it: the two names a child
// must keep are re-appended by name, so both arms behave identically.
var providerKeyNameSuffixes = []string{apiKeyNameSuffix, keyNameSuffix, tokenNameSuffix, secretNameSuffix}

// isProviderKeyName reports whether an already-normalized env-var name is a
// credential name by shape (ends in one of providerKeyNameSuffixes). It
// complements the explicit scrub set so an undeclared key, token or secret
// cannot ride into a child.
func isProviderKeyName(normalizedName string) bool {
	for _, suffix := range providerKeyNameSuffixes {
		if strings.HasSuffix(normalizedName, suffix) {
			return true
		}
	}
	return false
}

// CleanChildEnv builds the environment for ANY real-agy child process this
// shim spawns (native seats AND `agy models` catalog fetches — shared, F-ENV):
//
//	os.Environ()
//	MINUS AGY_LLM_GATEWAY_URL / _WIRE_PROTOCOL / _API_KEY / _MODELS
//	MINUS AGY_PRO_BRIDGE / AGY_BRIDGE
//	MINUS GEMINI_API_KEY / GOOGLE_API_KEY / GOOGLE_GEMINI_BASE_URL / CLOUD_CODE_URL
//	MINUS every name in scrubNames AND every BOM-variant of those names (F-Db):
//	      an env entry is scrubbed when normalizeEnvName(entryName) equals
//	      normalizeEnvName(scrubName) — U+FEFF stripped anywhere, whitespace
//	      trimmed, case folded (envkeys.go:36-39 pattern; BOM-polluted names are
//	      real in this codebase, envkeys.go:6-10; Windows env names are
//	      case-insensitive, round-4 M-1)
//	MINUS every credential-shaped name (isProviderKeyName: *_API_KEY, *_KEY,
//	      *_TOKEN, *_SECRET), so an ambient key, token or secret whose provider
//	      is neither a preset nor configured is scrubbed too (M4F-1, F-10B; see
//	      apiKeyNameSuffix and providerKeyNameSuffixes for why this is safe)
//	PLUS  AGY_PRO_ACTIVE=1 (paths.go:27 latch; guard at launcher.go:58-60)
//
// Anything a caller must inject (P1's CLOUD_CODE_URL=<mesh>, P2's
// GOOGLE_GEMINI_BASE_URL + GEMINI_API_KEY=<install nonce>) is appended AFTER
// this call: the scrub removes the inherited value, the append replaces it
// (see the P2 INJECTION ORDER note on fixedScrubNames).
func CleanChildEnv(scrubNames []string) []string {
	return CleanChildEnvWithBase(nil, scrubNames)
}

// CleanChildEnvWithBase is CleanChildEnv over an explicit base environment
// (base nil → os.Environ()). The native-seat runner uses it so a test seam's
// pre-set child env (helper-process markers) goes through the SAME scrubbing
// rules as the production base. Additive helper; the frozen API is
// CleanChildEnv.
func CleanChildEnvWithBase(base []string, scrubNames []string) []string {
	scrub := make(map[string]bool, len(fixedScrubNames)+len(scrubNames)+1)
	for _, n := range fixedScrubNames {
		scrub[normalizeEnvName(n)] = true
	}
	for _, n := range scrubNames {
		if normalized := normalizeEnvName(n); normalized != "" {
			scrub[normalized] = true
		}
	}
	scrub[normalizeEnvName(paths.EnvAgyProActive)] = true

	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			out = append(out, kv)
			continue
		}
		name := normalizeEnvName(kv[:i])
		// Scrub by explicit name (fixed set ∪ scrubNames) OR by shape: any
		// credential-shaped name (*_API_KEY / *_KEY / *_TOKEN / *_SECRET) is a
		// value the child never needs, so an undeclared key whose provider is
		// neither a preset nor configured (an unregistered vendor's *_API_KEY) and an
		// ambient token or secret (GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY) are
		// dropped too (M4F-1, F-10B).
		if scrub[name] || isProviderKeyName(name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, paths.EnvAgyProActive+"=1")
}

// ApiKeyEnvNames extracts every registered provider's APIKeyEnv name — the
// scrub list that keeps provider key VALUES out of every spawned child (S-3
// precedent launcher.go:201-206). Names come back in normalizeEnvName's folded
// form (BOM-stripped, trimmed, lower-cased), which is the form the scrub
// comparison uses, so the case of the registry entry no longer matters.
// Registry may be nil/empty → nil.
func ApiKeyEnvNames(reg *providers.Registry) []string {
	if reg == nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range reg.Providers {
		name := normalizeEnvName(p.APIKeyEnv)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// isSameDir checks if two directories refer to the same location.
func isSameDir(dirA, dirB string) bool {
	dirA = filepath.Clean(dirA)
	dirB = filepath.Clean(dirB)
	if runtime.GOOS == "windows" {
		if strings.EqualFold(dirA, dirB) {
			return true
		}
	}
	fiA, errA := os.Stat(dirA)
	fiB, errB := os.Stat(dirB)
	if errA == nil && errB == nil {
		return os.SameFile(fiA, fiB)
	}
	return false
}

// isSameFile checks if two paths point to the same physical file.
func isSameFile(pathA, pathB string) bool {
	pathA = filepath.Clean(pathA)
	pathB = filepath.Clean(pathB)
	if runtime.GOOS == "windows" && strings.EqualFold(pathA, pathB) {
		return true
	}
	fiA, errA := os.Stat(pathA)
	fiB, errB := os.Stat(pathB)
	if errA == nil && errB == nil {
		return os.SameFile(fiA, fiB)
	}
	return false
}
