package agysettings

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/paths"
)

const (
	// geminiDirName is the directory agy resolves under the user home. Both the
	// global settings file and every per-session app-data dir live under it.
	geminiDirName = ".gemini"

	// appDirName is the CLI app-data directory that holds the global
	// settings.json (design fact: os.UserHomeDir() + ".gemini" +
	// "antigravity-cli" + "settings.json").
	appDirName = "antigravity-cli"

	// settingsFileName is the settings file name in both locations.
	settingsFileName = "settings.json"

	// globalSettingsPathShape is the documented shape of the global settings
	// path. Errors and warnings name this shape instead of the resolved
	// absolute path, so no user directory leaks into a log or an error string.
	globalSettingsPathShape = "~/.gemini/antigravity-cli/settings.json"
)

// GlobalSettingsPath resolves the user's global agy settings file:
// <home>/.gemini/antigravity-cli/settings.json.
//
// This is a resolution and inspection helper. It is deliberately NOT a default
// write target: the global file is a protected path, and writing it would
// switch every session. The P2 regime writes AppDataDirSettingsPath instead.
//
// The home directory comes from os.UserHomeDir() (via paths.UserHome), which
// honors USERPROFILE on Windows and HOME elsewhere. GEMINI_CONFIG_DIR is not
// consulted; that claim was deleted from the design.
func GlobalSettingsPath() (string, error) {
	home, err := paths.UserHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, geminiDirName, appDirName, settingsFileName), nil
}

// AppDataDirSettingsPath resolves the settings file of a per-session app-data
// dir: <home>/.gemini/<app_data_dir>/settings.json.
//
// app_data_dir must be RELATIVE. agy itself rejects absolute values
// ("must not be absolute: ..."), and this package rejects them up front for
// the same reason: an absolute or traversing value would place the write
// outside the sandbox. Nested relative values ("nest/deep") are accepted and
// resolve to <home>/.gemini/nest/deep/settings.json.
func AppDataDirSettingsPath(appDataDir string) (string, error) {
	rel, err := normalizeAppDataDir(appDataDir)
	if err != nil {
		return "", err
	}
	home, err := paths.UserHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, geminiDirName, filepath.FromSlash(rel), settingsFileName), nil
}

// normalizeAppDataDir validates and cleans an --app_data_dir value, returning
// it as a forward-slash relative path confined under .gemini.
func normalizeAppDataDir(appDataDir string) (string, error) {
	trimmed := strings.TrimSpace(appDataDir)
	if trimmed == "" {
		return "", fmt.Errorf("agysettings: app_data_dir must not be empty")
	}

	// Absolute in any spelling agy accepts: a Windows volume ("C:\x", "C:x"),
	// a UNC share, or a POSIX root ("/etc").
	if filepath.IsAbs(trimmed) || filepath.VolumeName(trimmed) != "" {
		return "", fmt.Errorf("agysettings: app_data_dir must be relative, got %s (agy rejects absolute values)", maskOperand(appDataDir))
	}

	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." {
		return "", fmt.Errorf("agysettings: app_data_dir %s resolves to %q itself, which is the global app-data dir; use a per-session relative name", maskOperand(appDataDir), geminiDirName)
	}
	if strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("agysettings: app_data_dir must be relative, got %s", maskOperand(appDataDir))
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("agysettings: app_data_dir %s escapes %q", maskOperand(appDataDir), geminiDirName)
	}

	for _, seg := range strings.Split(cleaned, "/") {
		switch {
		case seg == "" || seg == ".":
			return "", fmt.Errorf("agysettings: app_data_dir %s has an empty path segment", maskOperand(appDataDir))
		case seg == "..":
			return "", fmt.Errorf("agysettings: app_data_dir %s escapes %q", maskOperand(appDataDir), geminiDirName)
		case strings.ContainsAny(seg, `:\`):
			return "", fmt.Errorf("agysettings: app_data_dir segment %s must not contain a volume separator or backslash", maskOperand(seg))
		}
	}
	return cleaned, nil
}

// isGlobalSettingsPath reports whether path addresses the protected global
// settings file, i.e. the one GlobalSettingsPath resolves to under the current
// home. Profile P2 must never be able to reach it: writing it would switch every
// session, and it is a protected path.
//
// The comparison is on the cleaned path (case-insensitive on Windows, where
// paths are case-insensitive) plus a best-effort symlink resolution, so a
// different spelling or a link alias cannot slip past the guard.
func isGlobalSettingsPath(path string) bool {
	global, err := GlobalSettingsPath()
	if err != nil {
		return false
	}
	if samePath(path, global) {
		return true
	}
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		return samePath(resolved, global)
	}
	return false
}

// samePath compares two paths for identity under this platform's rules.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
