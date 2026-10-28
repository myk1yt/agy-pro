// Package wireadp is the loopback adapter server for agy's native Gemini wire.
//
// agy speaks the public MLDev Gemini wire (POST
// /v1beta/models/<model>:streamGenerateContent?alt=sse, header X-Goog-Api-Key)
// and, on the Cloud Code branch, the /v1internal envelope. This package answers
// both dialects on 127.0.0.1 so external OpenAI-compatible providers can be
// used inside the native agy TUI with no agent mediation. The Gemini<->OpenAI
// translation lives in internal/geminiwire and internal/openaiup; this package
// owns everything with I/O: lifecycle, routing, auth, audit, housekeeping and
// the /v1internal RPC surface.
//
// The generation path is built in: translate (geminiwire) -> upstream call
// under the §6.4 watchdogs -> Gemini SSE or one JSON body -> §9 error mapping,
// with the §6.3 family quirks and the §7.13 bounded repair retry. It is
// reached through the GenerateHandler seam (Config.Generate); the built-in
// handler is installed by default, and UnimplementedGenerate keeps the 501
// skeleton behavior as an explicit override.
//
// Auth modes (spec §10.2):
//
//   - Strict (AuthKey set): every route except the lifecycle probe requires
//     the install nonce via X-Goog-Api-Key, x-api-key, ?key= or
//     Authorization: Bearer, compared in constant time. Missing, malformed or
//     mismatching credentials answer 401 UNAUTHENTICATED with a fixed message;
//     an empty configured key rejects everything (fail closed).
//   - Permissive (AuthKey empty): loopback-only bind plus the registry
//     allow-list plus a 60 req/min per-install token bucket plus the masked
//     audit line. RESIDUAL RISK: any local process that learns the ephemeral
//     port can spend the registry's provider keys, the same exposure class as
//     any local dev proxy. The strict mode exists for exactly this reason and
//     is the default whenever a launcher configures the install nonce.
//
// Per-route posture (FIX-44): the strict nonce guards /v1beta/* and /healthz
// in EVERY mode. The /v1internal:* data routes are strict too unless the
// caller sets Config.RequireV1InternalAuth to a non-nil false, which admits any
// loopback caller there (passive receipt) because agy's CodeAssist client
// presents only its keyring bearer and no shim-controlled channel can carry a
// nonce on that path. The bearer is received and discarded; the loopback bind,
// the JSON content-type gate, the Sec-Fetch rejection, the 60 req/min budget
// and the names-only audit stay active on those routes (FIX-58). The accepted
// residual is spec §10.2's permissive row: a co-resident local process can
// spend provider keys while a P1 session lives.
// See Config.RequireV1InternalAuth for the full posture text.
//
// Hygiene invariants (spec §5.2, §9.1, §10):
//
//   - Header VALUES are never logged, audited, echoed in an error, or placed
//     in a context value: names only.
//   - The inbound X-Goog-Api-Key value never reaches an upstream call; the
//     GenerateContext deliberately does not carry it.
//   - Every error message passes the redaction pipeline (redact, strip control
//     characters, one line, ≤240 runes).
//   - Bind is 127.0.0.1:0 only. No configuration selects another interface.
//
// Singleton semantics: Start adopts an existing healthy singleton when the
// healthz install nonce matches (in-process, or a live instance recorded in
// <agy-pro home>/wireadp.json for a later process). It never signals a process
// it does not own: a nonce mismatch refuses to adopt and leaves the running
// instance untouched.
//
// Spec: docs/261014_0006_session_native-coexistence-design/wire-translation-spec.md
package wireadp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"agypro/internal/paths"
	"agypro/internal/providers"
)

// loopbackBind is the only address the adapter ever listens on: 127.0.0.1 with
// an ephemeral port (§10.4).
const loopbackBind = "127.0.0.1:0"

const (
	// singletonProbeTimeout bounds the healthz probe that decides adoption.
	singletonProbeTimeout = 2 * time.Second
	// shutdownTimeout bounds the graceful shutdown wait.
	shutdownTimeout = 5 * time.Second
	// discoveryFile is the cross-process singleton record, relative to the
	// agy-pro home. It carries the URL, port, pid and start time only: no
	// credential is ever written to disk (§10.4).
	discoveryFile = "wireadp.json"
)

// Config configures one adapter instance.
type Config struct {
	// Registry is the parsed providers file: the model allow-list and the
	// listing route's source of truth. May be nil (nothing resolves, the
	// listing is empty).
	Registry *providers.Registry
	// Redact masks every audit and error line. Defaults to
	// providers.NewRedactor(), which masks registered key values plus the
	// bearer/sk-/AIza/AWS/auth-header/PEM shapes.
	Redact func(string) string
	// AuthKey is the install nonce. Empty means permissive mode (see the
	// package doc); any other value enables strict mode.
	AuthKey string
	// RequireV1InternalAuth selects the auth posture on the /v1internal:* data
	// routes (Appendix A). Nil is TRUE — the zero value — so every route,
	// /v1internal included, requires the install nonce exactly as before and
	// no existing caller's behavior changes. The passive posture is therefore
	// an explicit opt-in: pass a non-nil false.
	//
	// When false, the /v1internal:* data routes admit any loopback caller
	// (passive receipt): agy's CodeAssist client presents only its keyring
	// bearer, which no shim-controlled channel can carry a nonce on; the
	// bearer is received and discarded — never logged, never forwarded
	// upstream (GenerateContext carries no credential). Loopback bind, the
	// per-spawn ephemeral port, the 60 req/min rate limit, and the names-only
	// audit remain active; the residual (a co-resident local process can spend
	// provider keys while a P1 session lives) is spec §10.2's documented
	// permissive row. /v1beta and /healthz keep strict nonce auth in ALL
	// modes.
	//
	// Browser CSRF: the passive routes are additionally gated by a JSON
	// content-type requirement and a Sec-Fetch-* rejection (FIX-58), because a
	// CORS-safelisted simple request (Content-Type: text/plain, or none) needs
	// no preflight and the adapter answers no CORS headers. Those two layers
	// plus the loopback bind, the allow-list, the 60 req/min budget, the
	// session lifetime and the names-only audit are the real defense; a
	// cross-origin response stays unreadable either way. The loopback
	// RemoteAddr re-check stays in force.
	//
	// The type is a pointer because the default is TRUE: a plain bool's zero
	// value is false, which would make the passive posture the default for
	// every caller that does not set the field — exactly the silent weakening
	// this posture must avoid.
	RequireV1InternalAuth *bool
	// CatalogFunc returns the fetchAvailableModels catalog bytes. Nil means
	// the minimal empty catalog.
	CatalogFunc func() ([]byte, error)
	// RPCHooks overrides individual /v1internal RPC defaults, keyed by RPC
	// name (loadCodeAssist, fetchUserInfo, fetchAdminControls,
	// fetchAvailableModels, retrieveUserQuotaSummary, listExperiments).
	RPCHooks map[string]func(body []byte) ([]byte, error)
	// AuditPath is the masked JSONL audit file. Empty defaults to
	// <agy-pro home>/logs/wireadp-<start ts>.jsonl.
	AuditPath string
	// ResolveModel is the model allow-list seam: it maps a route model name
	// onto a registry target. U5b/hybrid supply their own (catalog
	// composition, tier mapping); the default is registry slug resolution
	// per spec §3.3. Returning (zero, false, nil) is an allow-list miss (404);
	// an error is a resolution failure (500).
	ResolveModel func(name string) (providers.Target, bool, error)
	// Generate is the generation-path seam. Nil installs the built-in
	// generation handler (translate -> upstream -> SSE -> error mapping); set
	// it to UnimplementedGenerate to keep the 501 skeleton behavior.
	Generate GenerateHandler
	// UpstreamFirstByteTimeout is the §6.4 first-byte watchdog. Zero uses the
	// 300s default; a value <= 0 disables the watchdog entirely (no timer is
	// armed), leaving the request bounded only by the inbound context and
	// agy's own --print-timeout.
	UpstreamFirstByteTimeout time.Duration
	// UpstreamIdleTimeout is the §6.4 idle-between-bytes watchdog, re-armed on
	// every upstream read. Zero uses the 120s default; a value <= 0 disables
	// the watchdog entirely.
	UpstreamIdleTimeout time.Duration
	// StreamKeepAliveInterval is the §8.9 SSE comment-ping interval, emitted
	// while the upstream is silent after the first byte. Zero uses the 15s
	// default; a value <= 0 disables pings.
	StreamKeepAliveInterval time.Duration
}

// Server is one running (or adopted) adapter instance.
type Server struct {
	cfg      Config
	redact   func(string) string
	generate GenerateHandler
	audit    *auditLogger
	limiter  *tokenBucket
	http     *http.Server
	listener net.Listener
	url      string
	port     int
	// adopted marks a handle whose listener lives in another process: Stop
	// must not signal it.
	adopted bool

	activityMu sync.Mutex
	lastActive time.Time

	stopOnce sync.Once
	stopErr  error
}

var (
	singletonMu sync.Mutex
	singleton   *Server
)

// Start binds the loopback listener and serves. It adopts an existing healthy
// singleton when the healthz install nonce matches (in-process first, then a
// live instance recorded in the discovery file), refuses to adopt on a nonce
// mismatch, and never signals a process it does not own.
func Start(cfg Config) (*Server, error) {
	if cfg.Redact == nil {
		cfg.Redact = providers.NewRedactor()
	}
	if existing := currentSingleton(); existing != nil {
		switch existing.adoptability(cfg) {
		case adoptOK:
			return existing, nil
		case adoptRefused:
			return nil, fmt.Errorf("wireadp: an adapter singleton is already serving %s and its install nonce does not match; reuse that key or stop that instance first", existing.URL())
		default:
			// Ours and in-process: replace it. An unknown process is never
			// signaled.
			_ = existing.Stop()
		}
	}
	if remote, err := adoptRemoteSingleton(cfg); err != nil {
		return nil, err
	} else if remote != nil {
		registerSingleton(remote)
		return remote, nil
	}
	srv, err := bindAndServe(cfg)
	if err != nil {
		return nil, err
	}
	registerSingleton(srv)
	return srv, nil
}

// bindAndServe binds 127.0.0.1:0, verifies the bound address is loopback, and
// starts serving.
func bindAndServe(cfg Config) (*Server, error) {
	listener, err := net.Listen("tcp", loopbackBind)
	if err != nil {
		return nil, fmt.Errorf("wireadp: failed to bind loopback listener: %w", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.IP == nil || !addr.IP.IsLoopback() {
		_ = listener.Close()
		return nil, fmt.Errorf("wireadp: refusing to serve on non-loopback address %v", listener.Addr())
	}
	srv := newServer(cfg, listener, addr.Port)
	srv.writeDiscovery()
	go func() {
		if serveErr := srv.http.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// Stop closes the listener; nothing else to report.
		}
	}()
	return srv, nil
}

// newServer builds an owner server around a live listener. WriteTimeout is
// deliberately unset so an SSE stream may outlive any fixed deadline (§6.4).
func newServer(cfg Config, listener net.Listener, port int) *Server {
	s := &Server{
		cfg:        cfg,
		redact:     cfg.Redact,
		port:       port,
		url:        fmt.Sprintf("http://127.0.0.1:%d", port),
		listener:   listener,
		audit:      newAuditLogger(cfg.AuditPath, cfg.Redact),
		limiter:    newTokenBucket(rateLimitPerMinute, rateLimitPerMinute),
		lastActive: time.Now(),
	}
	if cfg.Generate != nil {
		s.generate = cfg.Generate
	} else {
		// The built-in generation handler is the installed default; the 501
		// placeholder stays reachable only as an explicit override.
		s.generate = &generateHandler{srv: s}
	}
	s.http = &http.Server{
		Handler:           s.buildHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// newAdoptedServer builds a handle for a singleton owned by another process.
func newAdoptedServer(cfg Config, url string, port int) *Server {
	s := &Server{
		cfg:     cfg,
		redact:  cfg.Redact,
		url:     url,
		port:    port,
		adopted: true,
	}
	return s
}

// URL returns the base URL agy's GOOGLE_GEMINI_BASE_URL (or CLOUD_CODE_URL)
// should point at.
func (s *Server) URL() string { return s.url }

// Port returns the listening port.
func (s *Server) Port() int { return s.port }

// Healthz probes this instance's own /healthz route, carrying the install
// nonce when one is configured (§10.4). It returns nil on 200.
func (s *Server) Healthz(ctx context.Context) error {
	code, err := probeHealthz(ctx, s.URL(), s.cfg.AuthKey)
	if err != nil {
		return fmt.Errorf("wireadp: healthz probe failed: %w", err)
	}
	if code != http.StatusOK {
		return fmt.Errorf("wireadp: healthz returned %d", code)
	}
	return nil
}

// IdleFor reports how long the adapter has been idle (§10.4 idle TTL). The
// self-exit policy belongs to the process owner (the shim): a library never
// exits the process it is linked into.
func (s *Server) IdleFor() time.Duration {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.lastActive.IsZero() {
		return 0
	}
	return time.Since(s.lastActive)
}

func (s *Server) noteActivity() {
	s.activityMu.Lock()
	s.lastActive = time.Now()
	s.activityMu.Unlock()
}

// Stop shuts the server down gracefully. It is idempotent, and it never signals
// a process this handle does not own: an adopted remote singleton is left
// running (§10.4 squatting defense).
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		if s.adopted {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if s.http != nil {
			s.stopErr = s.http.Shutdown(ctx)
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.audit.close()
		clearSingleton(s)
	})
	return s.stopErr
}

func (s *Server) redactFunc() func(string) string {
	if s.redact != nil {
		return s.redact
	}
	return providers.NewRedactor()
}

// ---------------------------------------------------------------------------
// singleton adoption
// ---------------------------------------------------------------------------

type adoptKind int

const (
	adoptStale adoptKind = iota
	adoptOK
	adoptRefused
)

func currentSingleton() *Server {
	singletonMu.Lock()
	defer singletonMu.Unlock()
	return singleton
}

func registerSingleton(s *Server) {
	singletonMu.Lock()
	singleton = s
	singletonMu.Unlock()
}

func clearSingleton(s *Server) {
	singletonMu.Lock()
	if singleton == s {
		singleton = nil
	}
	singletonMu.Unlock()
}

// adoptability decides whether this in-process singleton can serve the
// candidate config. The configured nonce is known exactly here, so the compare
// is exact: a strict caller never adopts a permissive singleton (that would
// silently downgrade the auth mode) and vice versa. When the nonces match, the
// healthz probe confirms the instance is still alive.
func (s *Server) adoptability(cfg Config) adoptKind {
	if !sameSingletonConfig(s.cfg, cfg) {
		return adoptRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), singletonProbeTimeout)
	defer cancel()
	code, err := probeHealthz(ctx, s.URL(), cfg.AuthKey)
	if err != nil {
		return adoptStale
	}
	if code == http.StatusOK {
		return adoptOK
	}
	return adoptStale
}

// sameNonce reports whether two configured install nonces are the same. Two
// empty nonces are the same (both permissive); an empty and a non-empty nonce
// are not. Unlike keyMatches this is an adoption comparison, not an
// authentication check, so the empty/empty case must not fail closed.
func sameNonce(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sameSingletonConfig reports whether an existing instance can serve a new
// caller's config (FIX-59). Two fields decide it: the install nonce AND the
// /v1internal auth posture. Comparing only the nonce let a strict-default
// caller silently adopt a PASSIVE singleton (or vice versa) — the same
// auth-mode downgrade the nonce comparison exists to prevent, on the one route
// family where the posture differs.
func sameSingletonConfig(existing, candidate Config) bool {
	if !sameNonce(existing.AuthKey, candidate.AuthKey) {
		return false
	}
	return v1InternalAuthRequired(existing) == v1InternalAuthRequired(candidate)
}

// v1InternalAuthRequired resolves the /v1internal posture for a config: the nil
// default is strict (true).
func v1InternalAuthRequired(cfg Config) bool {
	return cfg.RequireV1InternalAuth == nil || *cfg.RequireV1InternalAuth
}

// adoptRemoteSingleton probes the discovery file for a live adapter owned by
// another process. The remote's auth mode is probed first (an open healthz is a
// permissive instance), so a strict caller never silently adopts a permissive
// singleton; a nonce mismatch refuses to start (the conflict is reported,
// nothing is killed); a dead record is stale and ignored.
func adoptRemoteSingleton(cfg Config) (*Server, error) {
	path, err := discoveryPath()
	if err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var rec discoveryRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.URL == "" {
		return nil, nil
	}
	// FIX-33: the record is advisory input from disk, so it is validated like
	// any other untrusted input before a single byte leaves the process. Only
	// an http/https loopback URL may be probed or adopted, which preserves the
	// package's documented loopback-only invariant and keeps the install nonce
	// (carried by the probe header) on the loopback. A hostile record is
	// ignored: Start binds fresh instead.
	if !isLoopbackURL(rec.URL) {
		logAdoption("refused", "discovery record URL is not a loopback http(s) URL", rec.URL)
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), singletonProbeTimeout)
	defer cancel()
	// Probe without a nonce first: 200 means the remote runs permissive.
	code, err := probeHealthz(ctx, rec.URL, "")
	if err != nil {
		return nil, nil
	}
	remotePermissive := code == http.StatusOK
	if cfg.AuthKey != "" && remotePermissive {
		logAdoption("refused", "the recorded instance runs in permissive mode", rec.URL)
		return nil, fmt.Errorf("wireadp: another adapter instance is already serving %s in permissive mode; stop it before starting a key-protected one", rec.URL)
	}
	code, err = probeHealthz(ctx, rec.URL, cfg.AuthKey)
	if err != nil {
		return nil, nil
	}
	switch code {
	case http.StatusOK:
		port := rec.Port
		if port <= 0 {
			if u, parseErr := url.Parse(rec.URL); parseErr == nil {
				if n, convErr := strconv.Atoi(u.Port()); convErr == nil {
					port = n
				}
			}
		}
		logAdoption("adopted", "healthz accepted the install nonce", rec.URL)
		return newAdoptedServer(cfg, rec.URL, port), nil
	case http.StatusUnauthorized:
		logAdoption("refused", "the recorded instance's install nonce does not match", rec.URL)
		return nil, fmt.Errorf("wireadp: another adapter instance is already serving %s and its install nonce does not match; reuse that key or stop that instance first", rec.URL)
	default:
		logAdoption("ignored", "the recorded instance did not answer healthz", rec.URL)
		return nil, nil
	}
}

// isLoopbackURL reports whether raw is an http(s) URL whose host is a loopback
// address (FIX-33). It is the URL-shaped twin of isLoopbackAddr and refuses
// everything else: a non-http scheme, a missing host, a hostname that is not an
// IP literal, and any non-loopback IP.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// "localhost" resolves to a loopback address; any other hostname is not
	// provably loopback without a DNS lookup, so it is refused.
	return strings.EqualFold(host, "localhost")
}

// logAdoption records an adoption decision (FIX-33). Only the decision, the
// reason and the URL are written; the URL is one-lined and capped because it
// comes from an on-disk record, and no credential is ever included.
func logAdoption(decision, reason, url string) {
	log.Printf("[wireadp] singleton adoption %s: %s (%s)", decision, reason, capRunes(oneLine(url), maxFieldRunes))
}

// discoveryRecord is the cross-process singleton record. It never carries a
// credential: the install nonce is only ever presented in the healthz header.
type discoveryRecord struct {
	URL     string `json:"url"`
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Started string `json:"started"`
}

func discoveryPath() (string, error) {
	home, err := paths.AgyProHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, discoveryFile), nil
}

// writeDiscovery records this instance so a later process can adopt it instead
// of binding a second listener. The file is advisory: a stale record is probed,
// found dead and overwritten by the next owner. Failures are ignored, the
// in-process singleton does not depend on it.
func (s *Server) writeDiscovery() {
	if s.adopted {
		return
	}
	path, err := discoveryPath()
	if err != nil {
		return
	}
	rec := discoveryRecord{
		URL:     s.url,
		Port:    s.port,
		PID:     os.Getpid(),
		Started: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = paths.AtomicWriteFile(path, data, 0o600)
}

// probeHealthz performs the GET /healthz lifecycle probe, carrying the install
// nonce when one is configured (§10.4).
func probeHealthz(ctx context.Context, baseURL, nonce string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/healthz", nil)
	if err != nil {
		return 0, err
	}
	if nonce != "" {
		req.Header.Set(headerInstallNonce, nonce)
	}
	resp, err := healthzClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return resp.StatusCode, nil
}

// healthzClient bounds the probe so a hung instance cannot hang Start.
// CheckRedirect refuses every redirect (FIX-33): a hostile loopback record
// could otherwise 302 the nonce-bearing probe to an off-box URL, so a redirect
// is treated as a failed probe rather than followed.
var healthzClient = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}
