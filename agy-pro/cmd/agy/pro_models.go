package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"agypro/internal/inventory"
	"agypro/internal/providers"
)

// Combined model inventory command (contract §2.6, U2):
//
//	agy pro models          legacy output, verbatim (B1-3: surface unchanged)
//	agy pro models --all    grouped numbered inventory (native + external)
//	agy pro models --json   []Entry dump
//
// Phase 1: unused-but-compilable; U-wiring flips main.go's dispatch line to
// os.Exit(runModelsCmd(subArgs)).

const proModelsUsage = "Usage: agy pro models [--all | --json]"

// loadModelsInventory builds the combined inventory with the canonical
// env-scrubbed, cached real-agy fetcher (contract §2.5). Tests replace it to
// drive runModelsCmd without spawning `agy models`.
var loadModelsInventory = func(ctx context.Context) (*inventory.Inventory, error) {
	reg, _ := providers.Load()
	nf := inventory.Cached(inventory.NewRealAgyFetcher(reg), inventory.DefaultCacheTTL)
	return inventory.Load(ctx, reg, nf)
}

// runModelsCmd executes `agy pro models [flags]` and returns the process exit
// code (0 ok, 1 inventory failure, 2 flag/usage error).
func runModelsCmd(args []string) int {
	return runModelsCmdTo(args, os.Stdout, os.Stderr)
}

// runModelsCmdTo is the writer-injectable core of runModelsCmd. The legacy
// no-flag path still renders through runModels() on os.Stdout (verbatim
// contract), so out is only consulted for --all/--json.
func runModelsCmdTo(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	// D8: stdlib flag output routes through the echo-line redactor so parse
	// errors cannot echo a planted operand.
	fs.SetOutput(&maskedFlagWriter{w: errw})
	all := fs.Bool("all", false, "show the combined native + external model inventory")
	asJSON := fs.Bool("json", false, "dump the combined inventory entries as JSON")
	fs.Usage = func() { fmt.Fprintln(errw, proModelsUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(errw, proModelsUsage)
		return 2
	}

	// No flags: legacy `agy pro models` output, byte-for-byte (B1-3 spirit).
	if !*all && !*asJSON {
		runModels()
		return 0
	}

	inv, err := loadModelsInventory(context.Background())
	if err != nil {
		fmt.Fprintf(errw, "model inventory error: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		for i := range inv.Entries {
			inv.Entries[i].ModelID = providers.MaskOperand(inv.Entries[i].ModelID)
			inv.Entries[i].Spec = providers.MaskOperand(inv.Entries[i].Spec)
		}
		if err := enc.Encode(inv.Entries); err != nil {
			fmt.Fprintf(errw, "model inventory error: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro Combined Model Inventory")
	fmt.Fprintln(out, "===========================================================")
	if inv.NativeErr != nil {
		fmt.Fprintf(out, "[native] unavailable: %v\n", inv.NativeErr)
	}
	inv.RenderNumbered(out)
	return 0
}
