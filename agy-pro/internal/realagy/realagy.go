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
// the four gateway vars (launcher.go:504-509 — a native seat must never be
// diverted into gateway mode, F-ENV) and the explicit-bridge vars
// (launcher.go:443, defense-in-depth).
var fixedScrubNames = []string{
	"AGY_LLM_GATEWAY_URL",
	"AGY_LLM_GATEWAY_WIRE_PROTOCOL",
	"AGY_LLM_GATEWAY_API_KEY",
	"AGY_LLM_GATEWAY_MODELS",
	"AGY_PRO_BRIDGE",
	"AGY_BRIDGE",
}

// normalizeEnvName folds an environment-variable name to its clean form:
// every U+FEFF anywhere in the name is removed and surrounding whitespace is
// trimmed (normalizeEnvName-equivalent, providers/envkeys.go:36-39, F-Db).
func normalizeEnvName(name string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(name), envBOM, ""))
}

// CleanChildEnv builds the environment for ANY real-agy child process this
// shim spawns (native seats AND `agy models` catalog fetches — shared, F-ENV):
//
//	os.Environ()
//	MINUS AGY_LLM_GATEWAY_URL / _WIRE_PROTOCOL / _API_KEY / _MODELS
//	MINUS AGY_PRO_BRIDGE / AGY_BRIDGE
//	MINUS every name in scrubNames AND every BOM-variant of those names (F-Db):
//	      an env entry is scrubbed when normalizeEnvName(entryName) equals
//	      normalizeEnvName(scrubName) — U+FEFF stripped anywhere, whitespace
//	      trimmed (envkeys.go:36-39 pattern; BOM-polluted names are real in
//	      this codebase, envkeys.go:6-10)
//	PLUS  AGY_PRO_ACTIVE=1 (paths.go:27 latch; guard at launcher.go:147-149)
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
		if scrub[normalizeEnvName(kv[:i])] {
			continue
		}
		out = append(out, kv)
	}
	return append(out, paths.EnvAgyProActive+"=1")
}

// ApiKeyEnvNames extracts every registered provider's APIKeyEnv name — the
// scrub list that keeps provider key VALUES out of every spawned child (S-3
// precedent launcher.go:201-206). Registry may be nil/empty → nil.
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
