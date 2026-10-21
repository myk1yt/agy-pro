package agenttools

// B4-2a: read_url_content, the URL tool of the provider-seat tool surface.
//
// Transport rules (CONTRACT section 3.5), all enforced inside this file:
//
//   - GET only: the method is forced by http.NewRequest(MethodGet); the tool
//     schema exposes no method argument, so a model can never choose one.
//   - https only; http is accepted for loopback hosts (127.0.0.1, ::1,
//     localhost) and nothing else. The loopback exception covers the SCHEME
//     only: the allow-list below is still checked for every host, loopback
//     included, so an allow-listed site cannot be used to reach an unlisted
//     loopback service.
//   - No custom headers, no auth material, no cookies: the request is built
//     with no Header writes at all (the Go transport defaults only), and a
//     userinfo part in the URL is refused outright.
//   - Redirect cap 3: the fourth redirect is refused. Every redirect target
//     re-runs the same scheme/host/allow-list validation, so a redirect
//     cannot escape the allow-list.
//   - Body cap 512 KiB, truncated with a clear note (the truncation choice,
//     pinned in fetch_test.go).
//   - Text content types only: text/*, application/json, application/xml,
//     the +json / +xml suffix families, and the small html-ish allow-list
//     (xhtml, rss, atom, svg). An absent or unparsable content type is
//     refused: binary cannot be ruled out, so fail closed.
//   - Per-request timeout fixed at the tool layer (30s, the read_url_content
//     clamp in the style of run_command's R1-5 budget): the schema exposes
//     no timeout argument.
//
// Host allow-list: the runner carries the list (AllowHosts); an empty list
// refuses every call (fail closed). Matching is exact on the lowercased URL
// hostname with the PORT IGNORED (documented decision: a port-pinned list
// would fail the moment a service moves ports, and the threat the list
// bounds is host choice, not port choice). Entries are normalized with
// normalizeAllowHost on BOTH sides, so a library caller that skips
// NormalizeAllowHosts still gets case-insensitive, port-ignored matching
// that fails closed, never open.

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// maxFetchBodyBytes caps one fetched body; the remainder is dropped and
	// the truncation is noted in the output (the truncation choice of
	// CONTRACT section 3.5, pinned in fetch_test.go).
	maxFetchBodyBytes = 512 * 1024
	// maxFetchRedirects caps the followed redirect chain: the FOURTH
	// redirect is refused.
	maxFetchRedirects = 3
	// fetchToolTimeout is the per-request budget applied at the tool layer
	// (the read_url_content clamp in the style of run_command's R1-5 budget).
	// The schema exposes no timeout argument, so the budget is fixed.
	fetchToolTimeout = 30 * time.Second
)

// ReadURLContent fetches rawURL with GET and returns the body text, or an
// error-shaped refusal. allowHosts is the run's host allow-list: an empty
// list refuses every call (fail closed). The loopback exception allows the
// http scheme for 127.0.0.1 / ::1 / localhost only; it never bypasses the
// allow-list.
func ReadURLContent(rawURL string, allowHosts []string) (string, error) {
	return readURLContent(rawURL, allowHosts, nil)
}

// readURLContent is ReadURLContent with an injectable client: the unit tests
// pin the https path through an httptest TLS server whose certificate only
// the injected client trusts. A nil client builds the production client
// (timeout + redirect cap + redirect re-validation).
func readURLContent(rawURL string, allowHosts []string, client *http.Client) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid url %q (want https://host/path)", rawURL)
	}
	if err := validateFetchURL(u, allowHosts); err != nil {
		return "", err
	}

	if client == nil {
		client = &http.Client{Timeout: fetchToolTimeout}
	}
	// K8: the redirect cap and per-target re-validation ride EVERY client,
	// injected ones included (a bare injected client used to follow cross-
	// host redirects). An explicit caller policy wins.
	if client.CheckRedirect == nil {
		client.CheckRedirect = fetchRedirectCheck(allowHosts)
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	// No custom headers, no auth material, no cookies are set here on
	// purpose: the request carries nothing but the URL and the transport
	// defaults (the tool takes no header arguments at all).
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("refused: the server answered %s", resp.Status)
	}
	ctype := resp.Header.Get("Content-Type")
	mediatype, _, perr := mime.ParseMediaType(ctype)
	if perr != nil || !fetchContentTypeAllowed(mediatype) {
		return "", fmt.Errorf("refused: unsupported content type %q (text content only)", ctype)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("failed to read body: %w", err)
	}
	if len(body) > maxFetchBodyBytes {
		return string(body[:maxFetchBodyBytes]) +
			fmt.Sprintf("\n... (body truncated at %d KiB)", maxFetchBodyBytes/1024), nil
	}
	return string(body), nil
}

// validateFetchURL applies the B4-2a transport gate to one URL: scheme (https,
// or http for loopback only), no userinfo (auth material), and the host
// allow-list (exact match on the lowercased hostname, port ignored). Every
// refusal is an error returned before any network use of the URL.
func validateFetchURL(u *url.URL, allowHosts []string) error {
	if u.User != nil {
		return errors.New("refused: credentials in the url are not allowed (no auth material may be sent)")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("invalid url %q: missing host", u.String())
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(host) {
			// K5: echo the ORIGINAL-CASE hostname (the seeds hold the
			// original case and masking is exact-substring).
			return fmt.Errorf("refused: http is allowed for loopback hosts only (127.0.0.1, ::1, localhost), not for %q", u.Hostname())
		}
	default:
		return fmt.Errorf("refused: unsupported scheme %q (https only; http only for loopback hosts)", u.Scheme)
	}
	if !hostInAllowList(host, allowHosts) {
		return errors.New("refused: host is not in the run's allow-list (read_url_content needs an explicit --allow-host entry)")
	}
	return nil
}

// isLoopbackHost reports whether host is one of the loopback names the http
// scheme exception covers: the localhost name or an IP whose IsLoopback is
// true (127.0.0.0/8, ::1, and the v4in6 forms).
func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// hostInAllowList reports whether host matches one of the allow-list entries,
// both sides normalized (trimmed, lowercased, port stripped, brackets
// stripped). An empty list matches nothing: the tool fails closed.
func hostInAllowList(host string, allowHosts []string) bool {
	h := normalizeAllowHost(host)
	if h == "" {
		return false
	}
	for _, entry := range allowHosts {
		if normalizeAllowHost(entry) == h {
			return true
		}
	}
	return false
}

// normalizeAllowHost renders one allow-list entry or URL hostname into the
// canonical match form: trimmed, lowercased, with any port and IPv6 brackets
// removed (matching ignores the port by design; see the file header).
func normalizeAllowHost(entry string) string {
	e := strings.TrimSpace(strings.ToLower(entry))
	if e == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(e); err == nil {
		e = h
	}
	return strings.Trim(e, "[]")
}

// NormalizeAllowHosts renders the raw --allow-host flag values into the
// canonical allow-list form RunConfig carries: normalized (see
// normalizeAllowHost), empties and duplicates dropped. The match itself
// normalizes both sides anyway, so this is the canonical form, not a
// security gate; a library caller that skips it still fails closed.
func NormalizeAllowHosts(entries []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		h := normalizeAllowHost(e)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// ValidateAllowHost reports whether one raw allow-list entry is usable as
// typed: a bare hostname ("example.com"), an IP ("127.0.0.1", "::1", with
// or without IPv6 brackets) or host:port ("example.com:8443"). It refuses
// scheme prefixes ("https://example.com"), wildcards ("*.example.com"),
// path segments ("example.com/path"), any whitespace, anything with no
// hostname left after normalization, and any character outside the
// hostname/IP alphabet (so "example.com:badport" or "ex_ample.com" are
// refused too). Matching is exact after normalization (hostInAllowList), so
// an entry this predicate refuses can never match a real URL hostname: it
// would sit in the list and silently refuse every read_url_content call.
// Validation is the input-surface honesty gate (D14); it never panics and
// does not change matching, which stays fail-closed regardless.
func ValidateAllowHost(entry string) bool {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return false
	}
	if strings.ContainsAny(raw, "*\\/") || strings.Contains(raw, "://") {
		return false
	}
	// Colon handling on the RAW entry, before normalization: SplitHostPort
	// accepts a non-numeric port ("example.com:badport", "https:example.com"
	// both split fine), and a colon that is neither an IP literal nor a
	// numeric port is scheme-shaped and refused.
	core := strings.Trim(raw, "[]")
	if strings.Contains(core, ":") && net.ParseIP(core) == nil {
		_, port, err := net.SplitHostPort(raw)
		if err != nil || port == "" {
			return false
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	h := normalizeAllowHost(entry)
	if h == "" {
		return false
	}
	ip := net.ParseIP(h)
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
		case r == ':' && ip != nil:
		default:
			return false
		}
	}
	return true
}

// ValidateAllowHosts reports whether every non-empty raw entry validates
// (see ValidateAllowHost). Empty entries are skipped, matching
// NormalizeAllowHosts, which drops them.
func ValidateAllowHosts(entries []string) bool {
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			continue
		}
		if !ValidateAllowHost(e) {
			return false
		}
	}
	return true
}

// fetchContentTypeAllowed is the text-only content-type rule: text/*, the
// core structured-text types, the +json/+xml suffix families, and the small
// html-ish allow-list. An empty mediatype is refused (an absent content type
// cannot be proven text, so fail closed).
func fetchContentTypeAllowed(mediatype string) bool {
	mt := strings.ToLower(strings.TrimSpace(mediatype))
	if mt == "" {
		return false
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch mt {
	case "application/json", "application/xml",
		"application/xhtml+xml", "application/rss+xml", "application/atom+xml",
		"image/svg+xml":
		return true
	}
	return strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml")
}

// fetchRedirectCheck is the read_url_content redirect policy (R1): at most
// maxFetchRedirects FOLLOWED redirects (the fourth is refused), and every
// target re-runs the full scheme/host/allow-list validation, so a redirect
// cannot escape the allow-list or downgrade https. Installed on EVERY client
// that reaches the network, injected ones included (K8); an explicit caller
// policy wins.
func fetchRedirectCheck(allowHosts []string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		// via counts the requests already made, so its length exceeds the cap
		// exactly when the (maxFetchRedirects+1)-th redirect (the fourth)
		// would be followed.
		if len(via) > maxFetchRedirects {
			return fmt.Errorf("refused: more than %d redirects", maxFetchRedirects)
		}
		return validateFetchURL(req.URL, allowHosts)
	}
}

// urlTokenOperands splits an operand that failed URL parsing into its bare
// tokens for seeding (G2c): URL punctuation and whitespace are separators,
// so a secret glued into a malformed URL or template still yields its bare
// runs through the ONE shared SeedEligible rule.
func urlTokenOperands(s string) []string {
	seen := map[string]bool{}
	var ops []string
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '/', '?', '&', '=', '%', '#', ':', ';', ',', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	}) {
		if tok != "" && !seen[tok] {
			seen[tok] = true
			ops = append(ops, tok)
		}
	}
	return ops
}

// ReadURLContentToolDef is the OpenAI function definition for
// read_url_content. The engine appends it to a provider seat's tool list
// ONLY when the run carries a host allow-list (--allow-host); the runner
// re-checks the list on every call, so an empty list refuses even if a
// schema somehow reached a model.
func ReadURLContentToolDef() ToolDef {
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        "read_url_content",
			Description: "Fetch a text document over HTTP. GET only, https only (http allowed for loopback hosts 127.0.0.1/::1/localhost), no request headers or credentials. The host MUST be in the run's explicit allow-list or the call is refused. The body is capped at 512 KiB (truncated with a note) and binary content types are refused. Use it to read a referenced document, issue, or API endpoint the task depends on.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Absolute https URL of the document to fetch (http only for loopback hosts). The host must be in the run's allow-list.",
					},
				},
				"required": []string{"url"},
			},
		},
	}
}
