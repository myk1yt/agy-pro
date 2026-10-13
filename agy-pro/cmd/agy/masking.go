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

// wholeOperandBase64Re matches a WHOLE operand that is one uninterrupted base64
// run of 16+ runes (E2 seed-eligibility shape 1): the AWS-secret shape, whose
// real instances carry "/". The classifier recognizes the same run; this shape
// is stated explicitly so a leading-"/" secret (which the classifier's
// location rule would exempt) still seeds.
var wholeOperandBase64Re = regexp.MustCompile(`^[A-Za-z0-9+/=]{16,}$`)

// jwtOperandRe matches a WHOLE operand in the 3-segment JWT shape (E2
// seed-eligibility shape 2). Together with the 16-rune total floor it covers
// JWTs whose header does not start with "eyJ" and so escapes the classifier.
var jwtOperandRe = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// operandRunRe matches an opaque base64-alphabet run inside a larger operand.
// Seeding never emits a bare run (E2): a 16-rune fragment cannot be told apart
// from a benign hash or path segment ("3f2a8b9c1d2e3f4a", "anthropic/claude"),
// so the extractor only feeds the decorated-secret test below.
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

// runIsCredentialShaped reports whether an opaque base64 run carries the
// base64-of-random-bytes signature (mixed case plus a digit) of a real secret
// such as an AWS secret access key. Lowercase-only runs are ordinary word,
// hash and path fragments ("anthropic/claude", "3f2a8b9c1d2e3f4a",
// "abcdefghijklmnop") and never seed; uppercase-only runs are access-key IDs
// the generic AKIA/ASIA patterns or the whole-operand rule already cover.
func runIsCredentialShaped(run string) bool {
	var upper, lower, digit bool
	for _, r := range run {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

// hasCredentialRun reports whether op carries a 16+ rune base64 run with the
// credential signature: the "1-<aws-secret>" / "native:<aws-secret>/x"
// decorated-operand class. Location-shaped operands are exempt (their inner
// runs are path segments and stay readable by policy).
func hasCredentialRun(op string) bool {
	if isLocationShaped(op) {
		return false
	}
	for _, run := range operandRunRe.FindAllString(op, -1) {
		if runIsCredentialShaped(run) {
			return true
		}
	}
	return false
}

// seedEligible reports whether op may seed the composite-error redactor (E2).
// Only WHOLE operands are ever seeded, never a fragment: the run extractor
// used to seed every separator-free 16+ rune fragment, which mangled benign
// tokens inside composite sentences ("native:anthropic/claude-sonnet-4-5/x"
// printed "native:[REDACTED]-sonnet-4-5/x", "prompts/3f2a8b9c1d2e3f4a.md"
// printed "[REDACTED].md"). An operand is eligible when the canonical
// classifier masks it, when the whole trimmed operand is a 16+ rune base64 run
// or a 16+ rune JWT shape (both shapes imply no whitespace), or when it carries
// a decorated credential run so the D1/D3 decorated-operand coverage survives.
func seedEligible(op string) bool {
	if providers.MaskOperand(op) != op {
		return true
	}
	t := strings.TrimSpace(op)
	if t == "" || strings.ContainsAny(t, " \t\r\n\v\f") {
		return false
	}
	if wholeOperandBase64Re.MatchString(t) {
		return true
	}
	if len([]rune(t)) >= 16 && jwtOperandRe.MatchString(t) {
		return true
	}
	return hasCredentialRun(op)
}

// maskOperandSentence renders an error message that may embed raw CLI
// operands. The redactor is seeded with every eligible operand (see
// seedEligible); token parsers may case-fold the echo
// (agenttools.ParseToolTokens lowercases the unknown-tool token), so the
// lowercase twin of every seed is armed too. With no seed the message falls
// back to plain MaskOperand, keeping benign operands readable exactly as
// before.
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
		if op == "" || !seedEligible(op) {
			continue
		}
		addSeed(op)
		addSeed(strings.ToLower(op))
	}
	if len(seeds) == 0 {
		return providers.MaskOperand(msg)
	}
	return providers.NewRedactor(seeds...)(msg)
}

// quotedEchoRe splits a flag echo line into double-quoted and unquoted spans,
// mirroring providers' quoted-segment rule: the stdlib quotes the offending
// value in its errors, and RedactEchoLine has already masked that content, so
// the unquoted scan below must not touch it.
var quotedEchoRe = regexp.MustCompile(`"[^"]*"`)

// unquotedRunRe matches a 16+ rune base64-shaped run in an unquoted span: the
// shape of an opaque credential (AWS secret keys, most carrying "/" or "+").
var unquotedRunRe = regexp.MustCompile(`[A-Za-z0-9+/=]{16,}`)

// unquotedJWTRe matches a 3-segment dotted token (JWT shape) in an unquoted
// span. The 16-rune total floor matches the operand-seeding rule so short
// dotted tokens (versions, "e.g.") stay readable.
var unquotedJWTRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+){2}`)

// maskUnquotedEcho masks credential-shaped runs in the UNQUOTED spans of a
// stdlib flag echo line (E1). The flag package prints an undefined flag name
// and a bad-syntax argument bare — `flag provided but not defined:
// -wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` echoed all 40 runes (live PoC),
// and `bad flag syntax: ----wJalr...` does the same — so the quoted-segment
// redactor cannot see them and the generic patterns only cover sk-/AKIA
// shapes. Each 16+ rune base64 run renders through the canonical classifier
// (providers.MaskOperand) and each 16+ rune JWT-shaped token renders as
// MaskSecret (U8: first 5 runes + ****). Quoted spans are left exactly as
// RedactEchoLine rendered them.
func maskUnquotedEcho(line string) string {
	var b strings.Builder
	last := 0
	for _, loc := range quotedEchoRe.FindAllStringIndex(line, -1) {
		b.WriteString(maskUnquotedSpan(line[last:loc[0]]))
		b.WriteString(line[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(maskUnquotedSpan(line[last:]))
	return b.String()
}

func maskUnquotedSpan(span string) string {
	span = unquotedJWTRe.ReplaceAllStringFunc(span, func(tok string) string {
		if len([]rune(tok)) < 16 {
			return tok
		}
		return providers.MaskSecret(tok)
	})
	return unquotedRunRe.ReplaceAllStringFunc(span, providers.MaskOperand)
}

// maskedFlagWriter is the flag.FlagSet output sink (D8): stdlib parse errors
// echo the raw operand — `invalid value "sk-..." for flag -timeout` — and the
// flag package also echoes undefined flag names unquoted. Every complete line
// therefore passes through providers.RedactEchoLine (quoted segments) and then
// providers.MaskOperand (generic credential patterns catch unquoted sk-/AKIA
// runs in flag-name echoes), and finally maskUnquotedEcho (E1: 16+ rune
// base64 runs and JWT-shaped tokens outside quotes) before it reaches stderr.
// Benign lines — usage text, `invalid value "abc"` — are unchanged by all
// three passes.
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
		if _, err := fmt.Fprintln(m.w, maskUnquotedEcho(providers.MaskOperand(providers.RedactEchoLine(line)))); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}
