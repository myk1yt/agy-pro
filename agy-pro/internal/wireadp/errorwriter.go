package wireadp

import (
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// §9.1 error surface: the Google API error envelope, the fixed messages, and
// the provider-side mapping rows encoded as data for the U5b upstream mapper.

const (
	// maxMessageRunes caps every error message (§9.1: one line, ≤240 chars).
	maxMessageRunes = 240

	// Google RPC status strings (§9.1 table, "status" column).
	statusInvalidArgument   = "INVALID_ARGUMENT"
	statusUnauthenticated   = "UNAUTHENTICATED"
	statusPermissionDenied  = "PERMISSION_DENIED"
	statusNotFound          = "NOT_FOUND"
	statusResourceExhausted = "RESOURCE_EXHAUSTED"
	statusAborted           = "ABORTED"
	statusDeadlineExceeded  = "DEADLINE_EXCEEDED"
	statusUnavailable       = "UNAVAILABLE"
	statusInternal          = "INTERNAL"
	statusUnimplemented     = "UNIMPLEMENTED"
)

// googleErrorBody is the Google API error envelope (§9.1). Field order is part
// of the pinned bytes: internal/geminiwire/golden/gf-26-expected.json and
// gf-32-expected.json compare this shape byte-for-byte.
type googleErrorBody struct {
	Error googleErrorDetail `json:"error"`
}

type googleErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// writeGoogleError answers with the §9.1 envelope. The message passes the
// redaction pipeline, so no key value, header value, request content or
// upstream body ever reaches the wire.
func (s *Server) writeGoogleError(w http.ResponseWriter, code int, status, message string) {
	writeJSON(w, code, s.googleErrorBody(code, status, message))
}

// googleErrorBody renders the §9.1 envelope for the given triple, running the
// message through the redaction pipeline. The mid-stream error frame (§9.3)
// uses the same renderer, so the bytes on the wire are identical whichever
// path produced them.
func (s *Server) googleErrorBody(code int, status, message string) googleErrorBody {
	var body googleErrorBody
	body.Error.Code = code
	body.Error.Message = sanitizeMessage(message, s.redactFunc())
	body.Error.Status = status
	return body
}

// sanitizeMessage runs the §9.1 message pipeline. Redaction happens BEFORE
// truncation: the spec lists strip -> truncate -> redact, but truncating first
// could cut a long secret in half and leave a fragment the redactor can no
// longer match. Redacting first keeps every secret whole and matchable, and
// the pinned fixture messages (all short, secret-free) are byte-identical
// under either order.
func sanitizeMessage(message string, redact func(string) string) string {
	if redact != nil {
		message = redact(message)
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, message)
	if utf8.RuneCountInString(message) > maxMessageRunes {
		message = string([]rune(message)[:maxMessageRunes])
	}
	return message
}

// providerErrorRow is one §9.1 provider-side row. Code doubles as the HTTP
// status the adapter answers with. Message templates take the provider name
// and, where the row carries one, the redacted one-line cause: the upstream
// HTTP code for the 5xx row, the provider-side model id for the 404 row, the
// extracted cause for the 400 rows. The upstream body is never echoed into a
// message (§9.1).
type providerErrorRow struct {
	code    int
	status  string
	message func(provider, cause string) string
}

// providerErrorRows is the §9.1 table as data. Keys are stable condition ids:
// the ones the golden fixture uses (gf-26-request.json conditions) plus the
// generic status-derived rows the U5b mapper needs. U5b consumes this table
// through lookupProviderError / mapProviderStatus.
var providerErrorRows = map[string]providerErrorRow{
	"provider-400": {400, statusInvalidArgument, func(p, cause string) string {
		if cause == "" {
			return "provider " + p + ": rejected request"
		}
		return "provider " + p + ": rejected request (" + cause + ")"
	}},
	"provider-400-response-format": {400, statusInvalidArgument, func(p, cause string) string {
		return "provider " + p + ": rejected request (" + cause + ")"
	}},
	"provider-401": {401, statusUnauthenticated, func(p, _ string) string {
		return "provider " + p + ": authentication failed"
	}},
	"provider-403": {403, statusPermissionDenied, func(p, _ string) string {
		return "provider " + p + ": permission denied"
	}},
	"provider-404": {404, statusNotFound, func(p, cause string) string {
		if cause == "" {
			return "provider " + p + ": model not found"
		}
		return "provider " + p + ": model " + cause + " not found"
	}},
	"provider-408": {504, statusDeadlineExceeded, func(p, _ string) string {
		return "provider " + p + ": timed out"
	}},
	"provider-409": {409, statusAborted, func(p, _ string) string {
		return "provider " + p + ": request conflict"
	}},
	"provider-413": {400, statusInvalidArgument, func(p, _ string) string {
		return "request too large for provider " + p
	}},
	"provider-429": {429, statusResourceExhausted, func(p, _ string) string {
		return "provider " + p + ": rate limited (retry later)"
	}},
	"provider-500": {502, statusUnavailable, func(p, cause string) string {
		return "provider " + p + ": upstream error (HTTP " + cause + ")"
	}},
	"upstream-timeout": {504, statusDeadlineExceeded, func(p, _ string) string {
		return "provider " + p + ": timed out"
	}},
	"transport-failure": {503, statusUnavailable, func(p, _ string) string {
		return "provider " + p + ": transport failure"
	}},
	"malformed-body": {502, statusUnavailable, func(p, _ string) string {
		return "provider " + p + ": unparseable response"
	}},
	// G10F-1: an upstream SSE stream that ended without a terminal frame. The
	// folded turn is a truncated answer, so the adapter refuses to synthesize a
	// complete 200 for it. 503/UNAVAILABLE matches the transport-failure row,
	// which is the retryable class agy already handles with bounded backoff.
	"stream-truncated": {503, statusUnavailable, func(p, _ string) string {
		return "provider " + p + ": upstream stream ended before a terminal frame"
	}},
}

// lookupProviderError resolves a named §9.1 condition onto its envelope triple.
func lookupProviderError(condition, provider, cause string) (int, string, string, bool) {
	row, ok := providerErrorRows[condition]
	if !ok {
		return 0, "", "", false
	}
	return row.code, row.status, row.message(provider, cause), true
}

// mapProviderStatus maps an upstream HTTP status onto the §9.1 envelope
// triple. cause carries the row detail (the HTTP code for 5xx, the redacted
// one-line cause for 400). Any unmapped status degrades to the 5xx row.
func mapProviderStatus(provider string, status int, cause string) (int, string, string) {
	condition := ""
	switch status {
	case http.StatusBadRequest:
		condition = "provider-400"
	case http.StatusUnauthorized:
		condition = "provider-401"
	case http.StatusForbidden:
		condition = "provider-403"
	case http.StatusNotFound:
		condition = "provider-404"
	case http.StatusRequestTimeout:
		condition = "provider-408"
	case http.StatusConflict:
		condition = "provider-409"
	case http.StatusRequestEntityTooLarge:
		condition = "provider-413"
	case http.StatusTooManyRequests:
		condition = "provider-429"
	}
	if condition == "" {
		condition = "provider-500"
		if cause == "" {
			cause = strconv.Itoa(status)
		}
	}
	code, statusText, message, ok := lookupProviderError(condition, provider, cause)
	if !ok {
		return http.StatusBadGateway, statusUnavailable, "provider " + provider + ": upstream error (HTTP " + strconv.Itoa(status) + ")"
	}
	return code, statusText, message
}
