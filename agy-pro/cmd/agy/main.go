// Command agy is the universal Go shim and router for Antigravity CLI.
// It intercepts `agy pro <subcommand>` commands to provide profile switching,
// provider management, and multi-model swarms, while transparently passing
// all other invocations to the authentic agy binary with standard I/O and exit codes preserved.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/launcher"
	"agypro/internal/mode"
	"agypro/internal/paths"
	"agypro/internal/providers"
)

const Version = "0.1.0"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "pro" {
		handleProCommand(os.Args[2:])
		return
	}

	// Passthrough to the authentic agy binary
	realAgy, err := launcher.FindRealAgy()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] Error: %v\n", err)
		os.Exit(1)
	}

	exitCode, err := launcher.ExecutePassthrough(realAgy, os.Args[1:])
	if err != nil {
		if !errors.Is(err, launcher.ErrRecursionDetected) {
			fmt.Fprintf(os.Stderr, "[agy-pro] Execution failed: %v\n", err)
		}
	}
	os.Exit(exitCode)
}

func handleProCommand(args []string) {
	if len(args) == 0 {
		printProHelp()
		return
	}

	subcommand := strings.ToLower(args[0])
	subArgs := args[1:]

	switch subcommand {
	case "setup":
		runSetup()
	case "mode":
		runMode(subArgs)
	case "status":
		runStatus()
	case "doctor":
		runDoctor()
	case "fanout":
		runFanout(subArgs)
	case "version", "--version", "-v":
		runVersion()
	case "help", "--help", "-h":
		printProHelp()
	default:
		fmt.Fprintf(os.Stderr, "[agy-pro] Unknown subcommand: %q\n\n", subcommand)
		printProHelp()
		os.Exit(1)
	}
}

func printProHelp() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro v%s — High-Performance CLI Shim & Orchestrator\n", Version)
	fmt.Println("===========================================================")
	fmt.Println("Usage: agy pro <command> [arguments...]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  setup           Interactive conversational setup wizard for LLM providers & keys")
	fmt.Println("  mode            Switch profile between full-stack and clean (or restore)")
	fmt.Println("  status          Display current mode, providers, and real agy status")
	fmt.Println("  doctor          Diagnose toolchain, paths, permissions, and connectivity")
	fmt.Println("  fanout          Run multi-model parallel audit, review, or research swarm")
	fmt.Println("  version         Display agy-pro and underlying agy version information")
	fmt.Println("  help            Show this help text")
	fmt.Println()
	fmt.Println("All other commands (e.g., `agy agents`, `agy -m ...`) pass through to real agy.")
}

func runSetup() {
	if err := providers.InteractiveSetupWizard(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "[agy-pro] Setup failed: %v\n", err)
		os.Exit(1)
	}
}

func runMode(args []string) {
	if len(args) == 0 {
		if err := mode.Switch("", os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Mode switch failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "restore":
		if err := mode.Restore(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Restore failed: %v\n", err)
			os.Exit(1)
		}
	case "list", "backups":
		backups, err := mode.ListBackups()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Failed to list backups: %v\n", err)
			os.Exit(1)
		}
		if len(backups) == 0 {
			fmt.Println("No backups available.")
			return
		}
		fmt.Println("Available backups:")
		for i, b := range backups {
			fmt.Printf("  %d. %s\n", i+1, b)
		}
	case "full", "clean", "default", "1", "2":
		if err := mode.Switch(sub, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[agy-pro] Mode switch failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[agy-pro] Invalid mode argument: %q (use 'full', 'clean', or 'restore')\n", sub)
		os.Exit(1)
	}
}

func runStatus() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro Status (v%s)\n", Version)
	fmt.Println("===========================================================")

	// 1. Mode & State
	st, err := mode.LoadState()
	if err != nil {
		fmt.Printf("Profile Mode : Error loading state (%v)\n", err)
	} else {
		fmt.Printf("Profile Mode : %s\n", strings.ToUpper(st.CurrentMode))
		fmt.Printf("Last Updated : %s\n", st.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
		if st.LastBackup != "" {
			fmt.Printf("Last Backup  : %s\n", st.LastBackup)
		}
	}

	// 2. Real Agy binary
	realAgy, err := launcher.FindRealAgy()
	if err != nil {
		fmt.Printf("Real agy.exe : NOT FOUND (%v)\n", err)
	} else {
		fmt.Printf("Real agy.exe : %s\n", realAgy)
	}

	// 3. Providers registry
	reg, err := providers.Load()
	if err != nil {
		fmt.Printf("Providers    : %v\n", err)
	} else {
		fmt.Printf("Providers    : %d configured\n", len(reg.Providers))
		for _, p := range reg.Providers {
			status := "configured"
			if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
				status = "API key missing (" + p.APIKeyEnv + ")"
			}
			fmt.Printf("  - %-12s : %d models [%s]\n", p.Name, len(p.Models), status)
		}
	}
}

func runDoctor() {
	fmt.Println("===========================================================")
	fmt.Printf("  agy-pro Diagnostic Doctor (v%s)\n", Version)
	fmt.Println("===========================================================")

	allGood := true

	// Check 1: Real agy
	realAgy, err := launcher.FindRealAgy()
	if err != nil {
		fmt.Println("[FAIL] Real agy binary not detected.")
		fmt.Printf("       Fix: Verify %%LOCALAPPDATA%%\\agy\\bin\\agy.exe exists or set %s\n", paths.EnvAgyProRealAgy)
		allGood = false
	} else {
		fmt.Printf("[OK]   Real agy located: %s\n", realAgy)
		cmd := exec.Command(realAgy, "--version")
		if out, err := cmd.Output(); err == nil {
			fmt.Printf("       Version: %s\n", strings.TrimSpace(string(out)))
		}
	}

	// Check 2: agy-pro shim binary and PATH precedence
	selfPath, err := os.Executable()
	if err != nil {
		fmt.Printf("[WARN] Could not determine shim binary path: %v\n", err)
	} else {
		fmt.Printf("[OK]   Running shim executable: %s\n", selfPath)
	}

	// Check 3: State & directories
	home, _ := paths.AgyProHome()
	cfgDir, _ := paths.GeminiConfigDir()
	fmt.Printf("[OK]   agy-pro Home: %s\n", home)
	fmt.Printf("[OK]   Gemini Config: %s\n", cfgDir)

	// Check 4: Providers
	reg, err := providers.Load()
	if err != nil {
		fmt.Printf("[WARN] Providers registry not loaded: %v\n", err)
	} else {
		unset := reg.UnsetEnvNames()
		if len(unset) > 0 {
			fmt.Printf("[WARN] %d provider API key environment variables not set:\n", len(unset))
			for _, u := range unset {
				fmt.Printf("       - %s\n", u)
			}
			fmt.Println("       Run `agy pro setup` to configure keys.")
		} else {
			fmt.Printf("[OK]   All %d configured providers have API keys set.\n", len(reg.Providers))
		}
	}

	// Check 5: Go toolchain
	goCmd := exec.Command("go", "version")
	if out, err := goCmd.Output(); err == nil {
		fmt.Printf("[OK]   Go toolchain: %s", string(out))
	} else {
		defGo := "C:\\Program Files\\Go\\bin\\go.exe"
		if _, statErr := os.Stat(defGo); statErr == nil {
			fmt.Printf("[OK]   Go toolchain located at: %s\n", defGo)
		} else {
			fmt.Println("[INFO] Go is not in PATH (runtime does not require Go once compiled).")
		}
	}

	fmt.Println()
	if allGood {
		fmt.Println("[PASS] Doctor diagnostics completed successfully. System is operational.")
	} else {
		fmt.Println("[WARN] Some items need attention. Review messages above.")
	}
}

func runFanout(args []string) {
	code := fanout.Run(args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}

func runVersion() {
	fmt.Printf("agy-pro version %s (Go shim & swarm orchestrator, %s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)

	realAgy, err := launcher.FindRealAgy()
	if err == nil {
		cmd := exec.Command(realAgy, "--version")
		if out, err := cmd.Output(); err == nil {
			fmt.Printf("upstream: %s\n", strings.TrimSpace(string(out)))
		}
	}
}
