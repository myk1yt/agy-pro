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
}

// ParseSeatSpecs parses+validates the --seats value. It performs NO registry
// lookups (provider resolution stays in SelectTargets/Execute). Whitespace
// around tokens is trimmed (same as --models splitting). Bare tokens (no
// "native:" prefix) are ALWAYS provider specs, keeping `--models` semantics
// 100% backward compatible.
func ParseSeatSpecs(spec string) ([]SeatSpec, error) {
	tokens := strings.Split(spec, ",")
	out := make([]SeatSpec, 0, len(tokens))
	for i, tok := range tokens {
		tok = strings.TrimSpace(tok)
		seatNo := i + 1
		if tok == "" {
			return nil, fmt.Errorf("seat %d: empty seat token in --seats", seatNo)
		}
		if len(tok) >= len(nativePrefix) && strings.EqualFold(tok[:len(nativePrefix)], nativePrefix) {
			id := tok[len(nativePrefix):]
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("seat %d: %q: \"native:\" prefix requires a model id (e.g. native:gemini-3.8-flash-high)", seatNo, tok)
			}
			if strings.Contains(id, "/") {
				return nil, fmt.Errorf("seat %d: %q: native model id must not contain \"/\" (use provider/model for external seats)", seatNo, tok)
			}
			if !nativeModelIDRe.MatchString(id) {
				return nil, fmt.Errorf("seat %d: %q: invalid native model id (expected [A-Za-z0-9][A-Za-z0-9._-]*)", seatNo, tok)
			}
			out = append(out, SeatSpec{Kind: SeatKindNative, Raw: tok, ModelID: id})
			continue
		}
		out = append(out, SeatSpec{Kind: SeatKindProvider, Raw: tok, ModelID: tok})
	}
	return out, nil
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
