package main

import (
	"bytes"
	"fmt"
	"io"
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
// Seed eligibility and the sentence masker live ONCE in providers
// (providers.SeedEligible / providers.MaskSentence, F-alpha) so this wrapper
// and internal/fanout cannot drift; only the E1 unquoted-echo scan below stays
// local to the two flag writers.

// maskOperandSentence renders an error message that may embed raw CLI
// operands: providers.MaskSentence seeds the redactor with every eligible
// operand (see providers.SeedEligible) plus its trimmed form, and falls back
// to providers.MaskOperand when no operand is eligible, keeping benign
// operands readable exactly as before.
func maskOperandSentence(msg string, operands ...string) string {
	return providers.MaskSentence(msg, operands...)
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
// span. The 16-rune total floor keeps SHORT dotted tokens readable, but it
// does not exempt 16+ rune dotted names: `--config.dev.override` prints
// "-confi****" through this scan (r5 N1, still open outside F-alpha — the
// operand-seeding rule providers.SeedEligible carries the tighter per-segment
// floor; this unquoted scan does not share it).
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
