// Package-level auth policy (FIX-44): §10.2's branches are now route-aware.
// The strict install nonce guards /v1beta/* and /healthz in every mode, and
// guards /v1internal/* unless the caller opts into passive receipt (see
// Config.RequireV1InternalAuth). The header VALUE is used for the comparison
// only: it is never logged, never audited, never echoed in an error and never
// forwarded upstream.

package wireadp

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// internalPathPrefix is the Cloud Code envelope branch's path prefix
// (Appendix A). Every /v1internal:* route, including ones this build does not
// serve, shares the auth posture.
const internalPathPrefix = "/v1internal:"

// authPosture is the credential requirement for one route.
type authPosture int

const (
	// authStrict requires the install nonce (§10.2 strict row).
	authStrict authPosture = iota
	// authPassive admits any loopback caller: a presented credential is read
	// and discarded (FIX-44).
	authPassive
)

// postureFor classifies a request path under the configured policy. /v1beta/*
// and the catch-all stay strict in every mode; only /v1internal/* can relax,
// and only on an explicit opt-in.
func (s *Server) postureFor(path string) authPosture {
	if strings.HasPrefix(path, internalPathPrefix) && !s.requireV1InternalAuth() {
		return authPassive
	}
	return authStrict
}

// requireV1InternalAuth reports the /v1internal posture. The nil default is
// TRUE (strict), so the passive mode is opt-in per Config.
func (s *Server) requireV1InternalAuth() bool {
	return v1InternalAuthRequired(s.cfg)
}

const (
	// headerAPIKey is the primary credential header: what agy's genai SDK
	// sends (W2), carrying the value agy resolved from GEMINI_API_KEY /
	// GOOGLE_API_KEY.
	headerAPIKey = "X-Goog-Api-Key"
	// headerAPIKeyAlt is the parity spelling (W23, the reference proxy's
	// accepted forms).
	headerAPIKeyAlt = "X-Api-Key"
	// headerInstallNonce carries the install nonce on the healthz route
	// (§10.4).
	headerInstallNonce = "X-Agy-Pro-Install"
)

// msgAuthFailed is the fixed 401 message (§9.1): it never echoes the presented
// key, so a caller probing the port learns nothing about the expected value.
const msgAuthFailed = "invalid or missing adapter install key"

// BoolPtr returns a pointer to b, for the optional bool Config fields whose
// default is not the zero value (RequireV1InternalAuth defaults to TRUE, so
// the passive posture is requested with BoolPtr(false)).
func BoolPtr(b bool) *bool { return &b }

// authorize applies the route's posture and answers 401 itself when the
// request must not proceed. Permissive mode (no AuthKey) admits every loopback
// caller; the loopback-only bind, the registry allow-list, the per-install rate
// limit and the audit line are its guards (see the package doc for the
// residual risk).
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.AuthKey == "" {
		return true
	}
	if s.postureFor(r.URL.Path) == authPassive {
		// Passive receipt: the caller is admitted without a credential. A
		// credential that IS presented (agy's keyring bearer on the CodeAssist
		// path) is read and dropped — noted by NAME only, never by value, and
		// never forwarded: the outbound request is built from scratch and
		// GenerateContext carries no credential at all.
		if key := presentedKey(r); key != "" {
			if ar := auditFromContext(r.Context()); ar != nil {
				ar.addNote("passive /v1internal: presented credential discarded")
			}
		}
		return true
	}
	if keyMatches(presentedKey(r), s.cfg.AuthKey) {
		return true
	}
	s.writeGoogleError(w, http.StatusUnauthorized, statusUnauthenticated, msgAuthFailed)
	return false
}

// admitBrowserRequest applies the passive-posture browser gate (FIX-58).
//
// A cross-origin fetch reaches a CORS-safelisted "simple request" — a POST with
// Content-Type: text/plain, or no Content-Type at all — WITHOUT any preflight,
// and this adapter answers no CORS headers, so the response is unreadable but
// the request still resolves a model and spends the registry provider key. Two
// layers close that path on the passive /v1internal surface:
//
//   - the request must carry a JSON content type. agy's CodeAssist client
//     always sends application/json (G4 capture and the design probes), so the
//     gate costs the real child nothing;
//   - any Sec-Fetch-Site header naming a fetch context is refused. Browsers set
//     Sec-Fetch-* on every request and scripts cannot forge or suppress them
//     (they are forbidden header names), and agy — a Go binary — never sends
//     them. "none" (a user-initiated navigation) and "same-origin" are allowed:
//     a navigation cannot POST a JSON body from another origin, and a
//     same-origin page would have to be served by the adapter itself.
//
// Strict mode needs neither layer: a nonce-bearing request must present a
// custom header (or a ?key= query), which forces a preflight the adapter never
// answers, so browsers are already blocked there. The gate therefore applies
// only where the route's posture is passive — /v1internal:* under the explicit
// opt-in. Fully permissive mode (no AuthKey at all) keeps its pre-existing
// §10.2 residual unchanged.
func (s *Server) admitBrowserRequest(w http.ResponseWriter, r *http.Request) bool {
	if s.postureFor(r.URL.Path) != authPassive {
		return true
	}
	if r.Method != http.MethodPost {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "cross-site", "same-site":
		s.writeGoogleError(w, http.StatusForbidden, statusPermissionDenied,
			"browser fetch metadata is not accepted on this route")
		return false
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		s.writeGoogleError(w, http.StatusUnsupportedMediaType, statusInvalidArgument,
			"this route requires Content-Type: application/json")
		return false
	}
	return true
}

// isJSONContentType reports whether a Content-Type header names JSON, ignoring
// parameters such as "; charset=utf-8".
func isJSONContentType(ct string) bool {
	media := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = strings.TrimSpace(media[:i])
	}
	return media == "application/json"
}

// presentedKey extracts the caller's credential from the four accepted forms
// (W23): X-Goog-Api-Key (primary), x-api-key, ?key= and Authorization: Bearer.
func presentedKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(headerAPIKey)); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get(headerAPIKeyAlt)); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.URL.Query().Get("key")); v != "" {
		return v
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		if v := strings.TrimSpace(auth[len(prefix):]); v != "" {
			return v
		}
	}
	return ""
}

// keyMatches compares a presented credential with the expected one in constant
// time. An empty value on either side never matches, which is the fail-closed
// rule: an empty configured key rejects everything (§10.2).
func keyMatches(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}
