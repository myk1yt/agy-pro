package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"agypro/internal/inventory"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
	"agypro/internal/realagy"
)

// Default Deps implementations. Every one delegates to an existing op
// (contract §2.9 storage discipline): mode.SaveState for state,
// providers.WriteFileAtomic for the registry, AuditProviderKeys for key
// probes, RepairWindowsKeyNames/RollbackWindowsKeyNames for key repair.

func saveRegistryFile(reg *providers.Registry) error {
	if reg == nil {
		return errors.New("registry is nil")
	}
	path := reg.Path
	if path == "" {
		var err error
		path, err = providers.DefaultPath()
		if err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return providers.WriteFileAtomic(path, data, 0o600)
}

// FixKeysReport is the report-returning fix-keys op (additive to the frozen
// Deps.FixKeys seam, which is error-only). Front-ends that need the printed
// backup dir use this; the quiet seam shares the same implementation.
func (c *Core) FixKeysReport(target string) (*providers.RepairReport, error) {
	return runFixKeys(c.deps.LoadReg, target)
}

// RollbackKeysReport mirrors FixKeysReport for the rollback op.
func (c *Core) RollbackKeysReport(backupDir string) error {
	return runRollback(backupDir)
}

func runFixKeys(loadReg func() (*providers.Registry, error), target string) (*providers.RepairReport, error) {
	reg, err := loadReg()
	if err != nil {
		return nil, fmt.Errorf("providers registry not loaded: %w", err)
	}
	names, err := providers.ResolveRepairTargets(reg, target)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve repair targets: %w", err)
	}
	report, repairErr := providers.RepairWindowsKeyNames(reg, providers.RepairOptions{EnvNames: names})
	if report == nil {
		return nil, fmt.Errorf("key repair failed: %w", repairErr)
	}
	return report, repairErr
}

func runRollback(backupDir string) error {
	file := providers.ResolveBackupFile(backupDir)
	return providers.RollbackWindowsKeyNames(file, providers.RepairOptions{})
}

func defaultFixKeys(target string) error {
	_, err := runFixKeys(providers.Load, target)
	return err
}

func defaultRollbackKeys(backupDir string) error {
	return runRollback(backupDir)
}

func defaultInventory(ctx context.Context) (*inventory.Inventory, error) {
	reg, err := providers.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		reg = nil
	}
	var nf inventory.NativeFetcher
	if reg != nil {
		nf = inventory.Cached(inventory.NewRealAgyFetcher(reg), inventory.DefaultCacheTTL)
	}
	return inventory.Load(ctx, reg, nf)
}

// defaultDoctor writes the doctor report through the Core seam. It runs the
// same checks as `agy pro doctor` (main.go:538-644) on the shared ops:
// real-agy resolution (realagy.Resolve), shim location (os.Executable +
// paths.AgyProBin), state/dirs (paths + mode.LoadState), and the provider key
// audit (providers.AuditProviderKeys — masked via MaskSecret). strict mirrors
// the CLI gate: structural failures (real agy missing) return an error.
func defaultDoctor(out io.Writer, strict bool) error {
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Diagnostic Doctor (settings Core)")
	fmt.Fprintln(out, "===========================================================")

	structuralFail := false

	realAgy, err := realagy.Resolve()
	if err != nil {
		fmt.Fprintln(out, "[FAIL] Real agy binary not detected.")
		fmt.Fprintf(out, "       Fix: set %s to the authentic Antigravity CLI path\n", paths.EnvAgyProRealAgy)
		structuralFail = true
	} else {
		fmt.Fprintf(out, "[OK]   Real agy located: %s\n", realAgy)
	}

	selfPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(out, "[WARN] Could not determine shim binary path: %v\n", err)
		if strict {
			structuralFail = true
		}
	} else if binDir, binErr := paths.AgyProBin(); binErr != nil {
		fmt.Fprintf(out, "[WARN] Could not determine installed shim location: %v\n", binErr)
		if strict {
			structuralFail = true
		}
	} else if shimInInstalledBin(selfPath, binDir) {
		fmt.Fprintf(out, "[OK]   Running shim executable: %s\n", selfPath)
	} else {
		fmt.Fprintf(out, "[WARN] Running shim executable: %s\n", selfPath)
		fmt.Fprintf(out, "       Installed shim location is %s; this copy is not first on PATH.\n", binDir)
		if strict {
			structuralFail = true
		}
	}

	home, _ := paths.AgyProHome()
	cfgDir, _ := paths.GeminiConfigDir()
	fmt.Fprintf(out, "[OK]   agy-pro Home: %s\n", home)
	fmt.Fprintf(out, "[OK]   Gemini Config: %s\n", cfgDir)
	if st, stErr := mode.LoadState(); stErr == nil {
		fmt.Fprintf(out, "[OK]   Default launch profile: %s\n", st.EffectiveDefaultProfile())
	}

	reg, err := providers.Load()
	if err != nil {
		fmt.Fprintf(out, "[WARN] Providers registry not loaded: %v\n", err)
	} else {
		audits := providers.AuditProviderKeys(reg, providers.AuditOptions{})
		if len(audits) == 0 {
			fmt.Fprintln(out, "[INFO] No provider API keys to audit.")
		}
		for _, a := range audits {
			printKeyAuditLine(out, a)
		}
	}

	if structuralFail {
		return errors.New("doctor found structural failures")
	}
	fmt.Fprintln(out, "[SUCCESS] Doctor finished with no structural failures.")
	return nil
}

// printKeyAuditLine renders one masked key-audit verdict (values only ever
// appear as providers.MaskSecret output — §2.11). Both echoed names route
// through providers.MaskOperand (C5/U8, security finding N2, D4): a legacy
// registry can hold a credential in the apiKeyEnv slot, and the same is true
// of the provider NAME slot, so neither may print raw.
func printKeyAuditLine(out io.Writer, a providers.KeyAudit) {
	hint := a.MaskedHint
	if hint == "" {
		hint = "(not set)"
	}
	env := providers.MaskOperand(a.Env)
	provider := providers.MaskSlot(a.Provider)
	switch {
	case !a.Resolved:
		fmt.Fprintf(out, "[WARN] %s (%s): %s\n", provider, env, hint)
	case a.Conflict:
		fmt.Fprintf(out, "[WARN] %s (%s): %s (name variants conflict)\n", provider, env, hint)
	case a.Probe.Status == providers.ProbeValid:
		fmt.Fprintf(out, "[OK]   %s (%s): %s (probe ok)\n", provider, env, hint)
	default:
		fmt.Fprintf(out, "[WARN] %s (%s): %s (probe %s)\n", provider, env, hint, a.Probe.Status)
	}
}

// shimInInstalledBin reports whether the running shim executable sits directly
// inside the installed shim bin directory (B3). paths.AgyProBin() ALREADY
// returns the bin DIRECTORY (paths.go:54-61), so the comparison is
// filepath.Dir(selfPath) == binDir — the earlier filepath.Dir(binDir) form
// compared one level too high and made the OK branch unreachable on every
// standard install (false "not first on PATH" WARN, and under --strict a false
// structural failure). Mirrors baseline `agy pro doctor` sameDirPath
// (cmd/agy/main.go:655-662): case-insensitive on Windows, exact elsewhere.
func shimInInstalledBin(selfPath, binDir string) bool {
	selfPath = filepath.Clean(selfPath)
	binDir = filepath.Clean(binDir)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Dir(selfPath), binDir)
	}
	return filepath.Dir(selfPath) == binDir
}
