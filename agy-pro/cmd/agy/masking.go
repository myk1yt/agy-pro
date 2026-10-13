package main

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"agypro/internal/providers"
)

// Composite-error masking (D1/D3 delta batch): several error paths print a
// message that a library call built from a raw CLI operand (e.g. *fs.PathError
// wraps the path into the sentence, seat-spec validators quote the token).
// MaskOperand cannot mask those alone: it classifies the WHOLE string, and a
// full sentence never looks secret-shaped, so only the generic credential
// patterns apply and an AWS-secret-shaped operand (contiguous base64 with "/")
// survives (live PoC: `--prompt-file wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`
// echoed all 40 runes). Following the ProbeEndpoint probe-key pattern, the
// redactor is seeded with the RAW operand so the planted secret is masked
// wherever it appears in the sentence, while operands the classifier keeps
// readable (paths, env names, model IDs) are not seeded and stay readable.

// operandRunRe matches an opaque base64-alphabet run inside a larger operand.
// The operand classifier (providers.operandLooksSecret) only sees the whole
// string, so a secret embedded in a prefixed or decorated operand
// ("1-<aws-secret>", "native:<aws-secret>/x") classifies readable; seeding its
// inner runs closes that gap. The 16-rune floor mirrors the classifier's
// contiguousSecretRun threshold (D7), so ordinary tokens, model IDs and path
// fragments never produce seeds.
var operandRunRe = regexp.MustCompile(`[A-Za-z0-9+/=]{16,}`)

// isLocationShaped mirrors the operand classifier's location rule: URLs,
// absolute paths, and leading ./ ../ .\ ..\ markers name a location rather
// than carrying a value. Such operands are readable by policy, so their inner
// base64 runs (ordinary path segments contain "/") must not become seeds.
func isLocationShaped(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	if filepath.IsAbs(s) {
		return true
	}
	for _, p := range []string{"./", "../", `.\`, `..\`} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// maskOperandSentence renders an error message that may embed raw CLI
// operands. The redactor is seeded with every operand that MaskOperand masks
// (credential-classified), plus each 16+ rune base64 run inside them; token
// parsers may case-fold the echo (agenttools.ParseToolTokens lowercases the
// unknown-tool token), so the lowercase twin of every seed is armed too. With
// no seed the message falls back to plain MaskOperand, keeping benign operands
// readable exactly as before.
func maskOperandSentence(msg string, operands ...string) string {
	seen := map[string]bool{}
	var seeds []string
	addSeed := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			seeds = append(seeds, s)
		}
	}
	for _, op := range operands {
		if op == "" {
			continue
		}
		if providers.MaskOperand(op) != op {
			addSeed(op)
			addSeed(strings.ToLower(op))
		}
		if isLocationShaped(op) {
			continue
		}
		for _, run := range operandRunRe.FindAllString(op, -1) {
			addSeed(run)
			addSeed(strings.ToLower(run))
		}
	}
	if len(seeds) == 0 {
		return providers.MaskOperand(msg)
	}
	return providers.NewRedactor(seeds...)(msg)
}

// maskedFlagWriter is the flag.FlagSet output sink (D8): stdlib parse errors
// echo the raw operand — `invalid value "sk-..." for flag -timeout` — and the
// flag package also echoes undefined flag names unquoted. Every complete line
// therefore passes through providers.RedactEchoLine (quoted segments) and then
// providers.MaskOperand (generic credential patterns catch unquoted sk-/AKIA
// runs in flag-name echoes) before it reaches stderr. Benign lines — usage
// text, `invalid value "abc"` — are unchanged by both passes.
type maskedFlagWriter struct {
	w    io.Writer
	tail []byte
}

func (m *maskedFlagWriter) Write(p []byte) (int, error) {
	m.tail = append(m.tail, p...)
	for {
		i := bytes.IndexByte(m.tail, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(m.tail[:i]), "\r")
		m.tail = m.tail[i+1:]
		if _, err := fmt.Fprintln(m.w, providers.MaskOperand(providers.RedactEchoLine(line))); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}
