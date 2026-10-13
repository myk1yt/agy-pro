package agenttools

import (
	"fmt"
	"strings"

	"agypro/internal/providers"
)

// toolTokenSet is the closed capability-token set shared by the fanout CLI
// (--tools) and the bridge tool loop (bridge-tools config / AGY_PRO_BRIDGE_TOOLS):
//
//	read  -> read_file, list_dir (always on)
//	search-> grep, glob (always on)
//	write -> write_file, replace_in_file (opt-in)
//	exec  -> run_command (opt-in)
//
// The mapping must stay in sync with the wizard's per-model AllowedTools
// guidance (read_file,list_dir,grep,glob by default; write_file/run_command
// only when a model lists them).
var toolTokenSet = []string{"read", "search", "write", "exec"}

// ParseToolTokens validates a tool-capability spec fail-closed: every token
// must be one of toolTokenSet, and the result is the canonical joined form.
// Unknown tokens are an error - "nowrite" or "exec-disabled" must never be
// silently ignored and then substring-matched into a capability (S-8).
//
// Relocated here from fanout/cli.go so the bridge tool loop can share the
// parser without importing fanout (R1-9); the error wording is preserved
// verbatim for CLI-output compatibility.
func ParseToolTokens(spec string) (string, error) {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(strings.ToLower(tok))
		if tok == "" {
			continue
		}
		known := false
		for _, allowed := range toolTokenSet {
			if tok == allowed {
				known = true
				break
			}
		}
		if !known {
			// B2/U8: the operand may be a misdirected key paste; mask it so no
			// key value beyond the first 5 characters ever reaches a transcript.
			return "", fmt.Errorf("unknown tool %q in --tools (want read,search,write,exec)", providers.MaskOperand(tok))
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	// Canonical order, so the effective allowlist is always readable.
	var ordered []string
	for _, allowed := range toolTokenSet {
		if seen[allowed] {
			ordered = append(ordered, allowed)
		}
	}
	return strings.Join(ordered, ","), nil
}
