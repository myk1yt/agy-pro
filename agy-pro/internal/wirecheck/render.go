package wirecheck

import (
	"fmt"
	"io"
)

// Render writes a CLI-agnostic view of r to out: the resolved binary, the
// version verdict, per-canary booleans, and the NOGO/DRIFT reasons. It prints
// canary names and booleans only, never binary contents. `agy pro hybrid
// check` (a later unit) is the intended caller; the launcher uses the same
// report for its fall-back notice.
func Render(out io.Writer, r Report) {
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy-pro wirecheck (hybrid binary surface canaries)")
	fmt.Fprintln(out, "===========================================================")

	if r.BinaryPath == "" {
		fmt.Fprintln(out, "Binary  : (not resolved)")
	} else {
		fmt.Fprintf(out, "Binary  : %s\n", r.BinaryPath)
	}

	switch {
	case r.VersionErr != "":
		fmt.Fprintf(out, "Version : probe failed (%s)\n", r.VersionErr)
	case r.VersionDrift:
		fmt.Fprintf(out, "Version : %s (DRIFT, pinned %s)\n", r.Version, PinnedVersion)
	case r.Version != "":
		fmt.Fprintf(out, "Version : %s (pinned)\n", r.Version)
	default:
		fmt.Fprintln(out, "Version : (not probed)")
	}

	if len(r.Canaries) == 0 {
		fmt.Fprintln(out, "Canaries: not evaluated (the binary scan never ran)")
	}
	for _, profile := range profileOrder() {
		canaries := r.canariesFor(profile)
		if len(canaries) == 0 {
			continue
		}
		found := 0
		for _, c := range canaries {
			if c.Found {
				found++
			}
		}
		status := "OK"
		if !r.ProfileOK(profile) {
			status = "INCOMPLETE"
		}
		fmt.Fprintf(out, "Profile %s: %s (%d/%d canaries found)\n", profile, status, found, len(canaries))
		for _, c := range canaries {
			mark := "ok"
			if !c.Found {
				mark = "MISSING"
			}
			fmt.Fprintf(out, "  %-8s %s\n", mark, c.Name)
		}
	}

	fmt.Fprintf(out, "Verdict : %s\n", r.Verdict())
	for _, reason := range r.Reasons() {
		fmt.Fprintf(out, "  - %s\n", reason)
	}
}
