package geminiwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// generateVerbs lists the route verb suffixes, longest first. The model segment
// may itself contain ':' and '/' (W10: evergreen:127.0.0.1:8791/models/probe-x),
// so the verb is matched as a literal suffix of the raw path instead of by
// splitting on the first colon (§3.3).
var generateVerbs = []string{":streamGenerateContent", ":generateContent"}

// routePrefixes are the accepted models-route prefixes. The /v1beta form is
// what the genai SDK sends; the bare /models form is tolerated for callers
// that mount the adapter at a root path.
var routePrefixes = []string{"/v1beta/models/", "/models/"}

// ParseGenerateRoute parses a raw models route path (§3.3) and returns the
// model name verbatim plus the verb without its leading colon. Everything
// between the matched prefix and the matched suffix is the model name, colons
// and slashes included, so a model that embeds a whole URL still resolves.
//
// The caller passes r.URL.RawPath when it is non-empty (it preserves the
// distinction between a literal and a percent-encoded colon) and r.URL.Path
// otherwise, and checks the HTTP method: this route family is POST-only.
func ParseGenerateRoute(rawPath string) (model string, verb string, err error) {
	path := rawPath
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, v := range generateVerbs {
		if !strings.HasSuffix(path, v) {
			continue
		}
		rest := strings.TrimSuffix(path, v)
		for _, prefix := range routePrefixes {
			if !strings.HasPrefix(rest, prefix) {
				continue
			}
			model = strings.TrimPrefix(rest, prefix)
			if model == "" {
				return "", "", fmt.Errorf("route %q carries an empty model name", rawPath)
			}
			return model, strings.TrimPrefix(v, ":"), nil
		}
		return "", "", fmt.Errorf("route %q is not under a models prefix", rawPath)
	}
	return "", "", fmt.Errorf("route %q has no generate verb suffix", rawPath)
}

// DecodeGenerateRequest decodes an MLDev GenerateContentRequest body. Unknown
// keys are ignored so an SDK bump never fails a request; a malformed body is an
// error the caller maps to 400 INVALID_ARGUMENT (§5.4, §9.1).
func DecodeGenerateRequest(body []byte) (*GenerateContentRequest, error) {
	var req GenerateContentRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("invalid GenerateContentRequest body: %w", err)
	}
	return &req, nil
}
