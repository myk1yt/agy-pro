package fanout

// Seat spec grammar for `--seats` (contract §2.1):
//
//	seats-spec     := seat-token ( "," seat-token )*
//	seat-token     := native-token | provider-token
//	native-token   := "native:" model-id            ; case-insensitive prefix
//	model-id       := [A-Za-z0-9][A-Za-z0-9._-]*    ; NO "/", no spaces, no ":" after prefix
//	provider-token := whatever providers.ParseModelSpec/Registry.FindTarget accept today:
//	                  "provider/model" | "model (provider)" | "model"

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"agypro/internal/providers"
)

// SeatKind distinguishes a REAL agy subprocess seat from an external provider seat.
type SeatKind string

const (
	SeatKindNative   SeatKind = "native"
	SeatKindProvider SeatKind = "provider"
)

// nativePrefix is the case-insensitive prefix marking a native seat token.
const nativePrefix = "native:"

// nativeModelIDRe is the model-id grammar of §2.1.
var nativeModelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SeatSpec is one parsed `--seats` token. Seat ORDER = report/seat-id order
// (seat 1..N); duplicate tokens are allowed (builder UI dedupes, CLI stays
// permissive).
type SeatSpec struct {
	Kind    SeatKind // native | provider
	Raw     string   // original token, for reports/errors
	ModelID string   // native: bare id; provider: the raw spec resolved later via FindTarget

	// UnitID names the partition unit this seat works on (B2). Empty on the
	// cross-verification path, where seats share one prompt instead of one
	// unit each. It is carried for per-unit attribution only: it must NEVER
	// reach a seat prompt, because a per-unit string in the shared preamble
	// would break cross-unit prefix caching.
	UnitID string
}

// ParseSeatSpecs parses+validates the --seats value. It performs NO registry
// lookups (provider resolution stays in SelectTargets/Execute). Whitespace
// around tokens is trimmed (the same comma-list convention --n and --tools
// follow). Bare tokens (no "native:" prefix) are ALWAYS provider specs, which
// is the grammar the retired `models` flag used to carry.
//
// FL-4: a token carrying a control (Cc) or invisible format (Cf) rune is
// REJECTED, never stripped. A seat token ends up in progress.log, the console
// banner, reconciliation.md and the summary, and a rune such as ESC or a
// right-to-left override has no visible identity there: it repaints the line or
// reorders the text an operator reads back. Stripping would silently change the
// token the user asked to run, so the parse refuses instead, with an error that
// names the seat NUMBER and the class and never echoes the rune. The native
// branch's id grammar already excludes both classes; the provider branch is
// free-form, which is the hole this closes.
func ParseSeatSpecs(spec string) ([]SeatSpec, error) {
	tokens := strings.Split(spec, ",")
	out := make([]SeatSpec, 0, len(tokens))
	for i, tok := range tokens {
		tok = strings.TrimSpace(tok)
		seatNo := i + 1
		if tok == "" {
			return nil, fmt.Errorf("seat %d: empty seat token in --seats", seatNo)
		}
		if hasSeatControlRune(tok) {
			return nil, fmt.Errorf("seat %d: seat token contains control or invisible format characters (remove them; they cannot be displayed or resolved)", seatNo)
		}
		if len(tok) >= len(nativePrefix) && strings.EqualFold(tok[:len(nativePrefix)], nativePrefix) {
			id := tok[len(nativePrefix):]
			// R3 item 3 (F-01): every quoted operand below is a seat TOKEN, so it
			// renders through the registry-slot authority (MaskSeatToken). The
			// seeded MaskSentence arm in front of these errors is NOT enough: a
			// lowercase underscore-head plant fails SeedEligible, and MaskOperand
			// is the identity on that class, so the raw token used to ride the
			// usage error, the swarm resolver warning and the plan error. When the
			// key-shaped mask fires the whole token renders opaque (the "native:"
			// prefix goes with it, which is intended: a masked token is opaque);
			// benign tokens keep their exact bytes.
			quoted := MaskSeatToken(tok)
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("seat %d: %q: \"native:\" prefix requires a model id (e.g. native:gemini-3.8-flash-high)", seatNo, quoted)
			}
			if strings.Contains(id, "/") {
				return nil, fmt.Errorf("seat %d: %q: native model id must not contain \"/\" (use provider/model for external seats)", seatNo, quoted)
			}
			if !nativeModelIDRe.MatchString(id) {
				return nil, fmt.Errorf("seat %d: %q: invalid native model id (expected [A-Za-z0-9][A-Za-z0-9._-]*)", seatNo, quoted)
			}
			out = append(out, SeatSpec{Kind: SeatKindNative, Raw: tok, ModelID: id})
			continue
		}
		out = append(out, SeatSpec{Kind: SeatKindProvider, Raw: tok, ModelID: tok})
	}
	return out, nil
}

// hasSeatControlRune reports whether s carries a control (Cc), invisible format
// (Cf) or line/paragraph-separator (Zl/Zp) rune. The first two are the pair
// providers.json's model fields refuse (hasControlRunes +
// hasInvisibleFormatRunes); Zl/Zp join them here (r4 L-1) because a seat token is
// an echo value as well as a run value: no masking authority rewrites U+2028/
// U+2029, so a token decorated with one can lane-split a log or an editor view
// even though an ANSI terminal ignores it. EDGE separators never reach this
// predicate: both runes carry the Unicode White_Space property, so ParseSeatSpecs'
// TrimSpace already removes them and the token is refused as EMPTY; an interior
// one is what lands here (r5 code-seat3 F-02 corrected the earlier "neither
// trimmed" wording). Refusal is the same either way, and no legitimate model id
// contains one, so refusing costs nothing.
func hasSeatControlRune(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) ||
			unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
	}) >= 0
}

// MaskSeatToken renders ONE seat or model token for display through the
// registry-slot authority (providers.MaskSlot), which is MaskOperand PLUS the
// underscore-head credential arm and internally strips the Cc/Cf class. The
// seat/model token class needs that authority: `sk_live_...`-shaped ids are
// provably identity under MaskOperand (slotpolicy.go), and a value that reaches
// summary.json, the progress log or the console is shareable long after the run
// (R2-1).
//
// Benign tokens keep their exact bytes, so every pinned echo stays readable.
func MaskSeatToken(token string) string {
	return providers.MaskSlot(strings.TrimSpace(token))
}

// MaskSeatTokens is MaskSeatToken over a comma-separated seat/models token list
// (the spelling presets store and `--seats` accepts). Each token is classified on
// its own: MaskSlot on the joined string folds the commas into one label and
// misses a credential planted inside a list, which is exactly how a hand-edited
// preset carries one.
//
// The original SEPARATOR SPANS are preserved, so a stored lineup spelled
// "model-a, model-b" echoes with its spaces intact instead of collapsing to
// "model-a,model-b" (R3 item 6): only the token text inside a segment can change,
// and only when the authority rewrites it.
func MaskSeatTokens(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	var b strings.Builder
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != ',' {
			continue
		}
		b.WriteString(maskSeatSegment(raw[start:i]))
		b.WriteByte(',')
		start = i + 1
	}
	b.WriteString(maskSeatSegment(raw[start:]))
	return b.String()
}

// maskSeatSegment masks one comma-separated segment while keeping the whitespace
// that pads it, so re-joining reproduces the stored spelling. A segment that is
// blank after the trim is returned untouched (an empty list slot stays empty).
func maskSeatSegment(seg string) string {
	trimmed := strings.TrimSpace(seg)
	if trimmed == "" {
		return seg
	}
	lead := strings.Index(seg, trimmed)
	return seg[:lead] + providers.MaskSlot(trimmed) + seg[lead+len(trimmed):]
}

// MaskQuotedSeatTokens renders every double-quoted span inside a FINISHED
// sentence through the registry-slot authority (MaskSeatToken). It is the
// display-boundary closer for token echoes the layer below owns: the seat
// selection grammar in internal/inventory quotes the offending token with
// MaskOperand, which is the identity on the underscore-head credential class, and
// that package is outside this lane's edit scope - so the CLI/console wrappers
// that PRINT those sentences mask the quoted span here instead (r4 H-2 residual).
//
// Benign NON-SLOT text is byte-identical: the prose outside the spans is never
// touched, and a benign token inside a span is the identity (MaskSlot only
// rewrites a value it classifies). Quoted spans do take the FULL slot authority,
// which includes the location-marker rewrite MaskRegistryValue performs on a
// location-shaped value: a refusal echoing "./backups" or "https://gw.example/v1"
// renders "backups" or "gw.example/v1" once this filter sees it. That is a
// deliberate over-mask (fail-safe: markers are stripped, never unmasked) and it
// is PINNED as such by TestMaskQuotedSeatTokensBenignLocationShaped (r5 code-seat3
// P2, DISCLOSE option): the alternative - exempting location-rewritten spans -
// would leave a "benign" rendering path that the credential arms share.
// Escaped quotes are respected, and an unbalanced quote simply leaves the tail
// alone: this is a display filter, never a rewrite of the message.
func MaskQuotedSeatTokens(sentence string) string {
	if !strings.Contains(sentence, `"`) {
		return sentence
	}
	var b strings.Builder
	rest := sentence
	for {
		i := strings.IndexByte(rest, '"')
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+1])
		rest = rest[i+1:]
		// Find the closing quote, skipping backslash-escaped characters.
		end := -1
		for k := 0; k < len(rest); k++ {
			if rest[k] == '\\' {
				k++
				continue
			}
			if rest[k] == '"' {
				end = k
				break
			}
		}
		if end < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(MaskSeatToken(rest[:end]))
		b.WriteByte('"')
		rest = rest[end+1:]
	}
}

// allNative reports whether every seat is a native seat (F-REG tolerance rule).
func allNative(seats []SeatSpec) bool {
	if len(seats) == 0 {
		return false
	}
	for _, s := range seats {
		if s.Kind != SeatKindNative {
			return false
		}
	}
	return true
}
