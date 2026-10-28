// Package paths centralizes file path resolution, atomic writes, and
// safe non-destructive backups for agy-pro. All paths support cross-platform
// resolution (Windows, macOS, Linux) without hardcoded user directories.
package paths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// EnvAgyProHome overrides the agy-pro home directory (~/.agy-pro).
	EnvAgyProHome = "AGY_PRO_HOME"

	// EnvAgyProProviders overrides the providers.json location.
	EnvAgyProProviders = "AGY_PRO_PROVIDERS"

	// EnvAgyProRealAgy specifies the absolute path to the real agy binary.
	EnvAgyProRealAgy = "AGY_PRO_REAL_AGY"

	// EnvAgyProActive is the recursion guard indicator.
	EnvAgyProActive = "AGY_PRO_ACTIVE"

	// EnvGeminiConfigDir overrides the ~/.gemini/config directory.
	EnvGeminiConfigDir = "GEMINI_CONFIG_DIR"
)

// UserHome returns the current user's home directory.
func UserHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine user home directory: %w", err)
	}
	return filepath.Clean(home), nil
}

// AgyProHome returns the base directory for agy-pro runtime data (default: ~/.agy-pro).
func AgyProHome() (string, error) {
	if env := strings.TrimSpace(os.Getenv(EnvAgyProHome)); env != "" {
		return filepath.Clean(env), nil
	}
	home, err := UserHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agy-pro"), nil
}

// AgyProBin returns ~/.agy-pro/bin where the shim agy.exe resides.
func AgyProBin() (string, error) {
	base, err := AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bin"), nil
}

// AgyProBackups returns ~/.agy-pro/backups where moved configurations are archived.
func AgyProBackups() (string, error) {
	base, err := AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "backups"), nil
}

// AgyProStateFile returns ~/.agy-pro/state.json.
func AgyProStateFile() (string, error) {
	base, err := AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "state.json"), nil
}

// AgyProProvidersFile returns ~/.agy-pro/providers.json or AGY_PRO_PROVIDERS override.
func AgyProProvidersFile() (string, error) {
	if env := strings.TrimSpace(os.Getenv(EnvAgyProProviders)); env != "" {
		return filepath.Clean(env), nil
	}
	base, err := AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "providers.json"), nil
}

// GeminiConfigDir returns ~/.gemini/config or GEMINI_CONFIG_DIR override.
func GeminiConfigDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv(EnvGeminiConfigDir)); env != "" {
		return filepath.Clean(env), nil
	}
	home, err := UserHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "config"), nil
}

// DefaultRealAgyPath resolves the platform-conventional install location of real agy.
func DefaultRealAgyPath() (string, error) {
	home, err := UserHome()
	if err != nil {
		return "", err
	}

	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			return filepath.Join(localAppData, "agy", "bin", "agy.exe"), nil
		}
		return filepath.Join(home, "AppData", "Local", "agy", "bin", "agy.exe"), nil
	}

	// macOS / Linux standard locations
	primary := filepath.Join(home, ".local", "bin", "agy")
	return primary, nil
}

// EnsureBaseDirs ensures ~/.agy-pro, ~/.agy-pro/bin, and ~/.agy-pro/backups exist.
func EnsureBaseDirs() error {
	binDir, err := AgyProBin()
	if err != nil {
		return err
	}
	backupDir, err := AgyProBackups()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("failed to create bin directory %q: %w", binDir, err)
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("failed to create backup directory %q: %w", backupDir, err)
	}
	return nil
}

// AtomicWriteFile safely writes data to a target path using a temporary file
// followed by an atomic rename, preventing partial writes or corruptions.
func AtomicWriteFile(targetPath string, data []byte, perm os.FileMode) error {
	targetPath = filepath.Clean(targetPath)
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %q: %w", dir, err)
	}
	tmpName := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to flush temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		// Non-fatal on some Windows filesystems, but try anyway
	}

	// On Windows, os.Rename fails if the destination exists and is open,
	// but standard atomic replace works when not locked.
	// For robustness on Windows, attempt Rename; if it fails due to existing file,
	// replace safely.
	if err := os.Rename(tmpName, targetPath); err != nil {
		// Fallback for Windows overwrite
		_ = os.Remove(targetPath)
		if retryErr := os.Rename(tmpName, targetPath); retryErr != nil {
			return fmt.Errorf("failed to rename %q to %q: %w", tmpName, targetPath, retryErr)
		}
	}

	cleanup = false
	return nil
}

// NewBackupTimestamp returns a formatted timestamp string for backup directories.
func NewBackupTimestamp() string {
	return time.Now().UTC().Format("20060102_150405")
}

// MoveToBackup moves an existing file or directory into a backup destination.
// It NEVER deletes files permanently.
func MoveToBackup(sourcePath string, backupDir string) (string, error) {
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			return "", nil // Nothing to move
		}
		return "", err
	}

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create backup destination %q: %w", backupDir, err)
	}

	baseName := filepath.Base(sourcePath)
	destPath := filepath.Join(backupDir, baseName)

	// Attempt rename
	if err := os.Rename(sourcePath, destPath); err == nil {
		return destPath, nil
	}

	// If rename failed (e.g. cross-volume), perform copy then remove source
	if err := CopyRecursive(sourcePath, destPath); err != nil {
		return "", fmt.Errorf("failed to copy %q to %q: %w", sourcePath, destPath, err)
	}
	_ = os.RemoveAll(sourcePath)
	return destPath, nil
}

// CopyRecursive copies a file or directory recursively.
func CopyRecursive(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			s := filepath.Join(src, entry.Name())
			d := filepath.Join(dst, entry.Name())
			if err := CopyRecursive(s, d); err != nil {
				return err
			}
		}
		return nil
	}

	// Regular file copy
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Sync()
}
