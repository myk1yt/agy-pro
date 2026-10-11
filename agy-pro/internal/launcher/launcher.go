// Package launcher locates the underlying real agy binary and executes it
// transparently, preserving stdio handles, exit codes, and preventing recursion.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/paths"
)

// ErrRecursionDetected is returned when agy-pro detects an infinite call loop.
var ErrRecursionDetected = errors.New("infinite recursion detected: real agy resolved back to agy-pro shim")

// ErrRealAgyNotFound is returned when the original agy binary cannot be located.
var ErrRealAgyNotFound = errors.New("real agy binary not found")

// FindRealAgy locates the authentic agy binary using the system environment and PATH.
func FindRealAgy() (string, error) {
	selfPath, err := os.Executable()
	if err != nil {
		selfPath = ""
	} else {
		selfPath, _ = filepath.EvalSymlinks(selfPath)
	}

	envReal := os.Getenv(paths.EnvAgyProRealAgy)
	pathEnv := os.Getenv("PATH")
	fallback, _ := paths.DefaultRealAgyPath()

	return resolveRealAgy(selfPath, envReal, pathEnv, fallback)
}

// resolveRealAgy implements the resolution precedence:
// 1. AGY_PRO_REAL_AGY override
// 2. PATH search (excluding self directory and os.SameFile)
// 3. Platform default fallback location
func resolveRealAgy(selfPath, envReal, pathEnv, fallback string) (string, error) {
	// 1. AGY_PRO_REAL_AGY override
	if trimmed := strings.TrimSpace(envReal); trimmed != "" {
		cleaned := filepath.Clean(trimmed)
		if fi, err := os.Stat(cleaned); err == nil && !fi.IsDir() {
			if selfPath != "" && isSameFile(selfPath, cleaned) {
				return "", fmt.Errorf("%w: AGY_PRO_REAL_AGY points to shim itself (%s)", ErrRecursionDetected, cleaned)
			}
			return cleaned, nil
		}
		return "", fmt.Errorf("%w: configured path %q does not exist", ErrRealAgyNotFound, trimmed)
	}

	var selfDir string
	if selfPath != "" {
		selfDir = filepath.Clean(filepath.Dir(selfPath))
	}

	// Candidates to check in each PATH entry
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

	return "", fmt.Errorf("%w: please install official agy or set %s to its path", ErrRealAgyNotFound, paths.EnvAgyProRealAgy)
}

// ExecutePassthrough runs the real agy binary with the given arguments,
// connecting standard I/O streams directly to preserve full TUI and interactive CLI behavior.
func ExecutePassthrough(realAgyPath string, args []string) (int, error) {
	// Guard against recursive execution loop
	if os.Getenv(paths.EnvAgyProActive) == "1" {
		return 1, fmt.Errorf("%w: active guard triggered for %s", ErrRecursionDetected, realAgyPath)
	}

	cmd := exec.Command(realAgyPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Propagate recursion guard to child process
	cmd.Env = append(os.Environ(), paths.EnvAgyProActive+"=1")

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, fmt.Errorf("failed to run real agy at %q: %w", realAgyPath, err)
	}

	return 0, nil
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
