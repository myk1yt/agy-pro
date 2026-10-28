package wireadp

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"agypro/internal/geminiwire"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// Inbound route surface (§5.1): the public MLDev routes agy's genai SDK uses,
// the Cloud Code /v1internal envelope branch, the lifecycle route and the
// explicit-404 catch-all. The generation path itself is the GenerateHandler
// seam below: this unit wires everything around it.

// Route classes, recorded on the audit line.
const (
	routeGenerate      = "generate"
	routeListModels    = "list_models"
	routeInternalGen   = "internal_generate"
	routeInternalRPC   = "internal_rpc"
	routeHousekeeping  = "housekeeping"
	routeHealthz       = "healthz"
	routeUnknown       = "unknown"
	surfaceV1Beta      = "v1beta"
	surfaceV1Internal  = "v1internal"
	verbGenerate       = "generateContent"
	verbStreamGenerate = "streamGenerateContent"
)

const (
	// serviceName identifies the adapter on the healthz body (§10.4).
	serviceName = "agy-pro-wireadp"
	// modelsDescription is the §5.7 listing description.
	modelsDescription = "agy-pro adapter model"
	// defaultProject is the project id loadCodeAssist reports; agy echoes it
	// back on subsequent envelope bodies (surface-verification-report §3.2).
	// The value is the known-good probe payload's fixed mock project
	// (FIX-57): a deterministic default, overridable per deployment through
	// RPCHooks["loadCodeAssist"].
	defaultProject = "mock-project-1234"
	// freeTierID / freeTierName are the tier id and label loadCodeAssist
	// reports, byte-aligned with the known-good probe payload (FIX-57).
	freeTierID   = "free-tier"
	freeTierName = "Free"
	// defaultRegionCode is the region hint fetchUserInfo reports, byte-aligned
	// with the known-good probe payload (FIX-57).
	defaultRegionCode = "US"
	// rateLimitPerMinute is the permissive-mode budget (§10.2, §6.4): 60
	// requests per minute per install, token-bucket shaped so a burst up to
	// the budget passes instantly.
	rateLimitPerMinute = 60
)

// GenerateHandler is the generation-path seam. Unit U5b implements it:
// translate the Gemini request to the OpenAI wire, call the provider, emit
// Gemini SSE (or one JSON body) and map upstream failures onto §9.1
// envelopes. Until then the default handler answers 501 UNIMPLEMENTED.
//
// Contract:
//   - The handler owns the response once it writes to w; it returns nil after
//     a complete write (a finished stream included).
//   - It may return a *GenerateError to answer with a §9.1 envelope, or
//     errNotImplemented; any other error becomes a fixed 500 INTERNAL body.
//   - GenerateContext carries no credential. The inbound X-Goog-Api-Key value
//     is deliberately absent: it must never reach an upstream call. Read it
//     from r when a branch needs it, and never forward it (§6.1).
//   - HeaderNames lists inbound header names only (§5.2).
type GenerateHandler interface {
	Generate(w http.ResponseWriter, r *http.Request, g *GenerateContext) error
}

// GenerateContext is the resolved generation request handed to the seam. The
// §3.3 resolution order has already run: Target is the allow-list hit (or the
// zero value on the /v1internal branch when the model was unresolved, which
// cannot reach the seam), Model is the route model name verbatim (the colon
// trap preserved), and Envelope is non-nil on the /v1internal surface, where
// the handler re-wraps its response (Appendix A).
type GenerateContext struct {
	Surface     string
	Model       string
	Verb        string
	AltSSE      bool
	Target      providers.Target
	Request     *geminiwire.GenerateContentRequest
	Envelope    *geminiwire.V1InternalRequest
	HeaderNames []string
}

// GenerateError lets the seam answer with a §9.1 envelope of its choosing.
type GenerateError struct {
	Code    int
	Status  string
	Message string
}

func (e *GenerateError) Error() string { return e.Status + ": " + e.Message }

// errNotImplemented is the explicit-override placeholder's answer: the caller
// asked for the skeleton behavior, so a resolved model must not silently
// succeed.
var errNotImplemented = errors.New("generation path is not implemented yet")

// UnimplementedGenerate is the explicit opt-in placeholder handler: it answers
// 501 UNIMPLEMENTED for every resolved model. Start installs the real
// generation handler when Config.Generate is nil, so this value is the only
// way to keep the U5a skeleton behavior.
type UnimplementedGenerate struct{}

func (UnimplementedGenerate) Generate(w http.ResponseWriter, r *http.Request, g *GenerateContext) error {
	return errNotImplemented
}

// internalRPCNames are the /v1internal non-generation RPCs answered with
// minimal proven defaults (Appendix A; surface-verification-report §3.2,
// plan-model-gate-report §10).
var internalRPCNames = []string{
	"loadCodeAssist",
	"onboardUser",
	"fetchUserInfo",
	"fetchAdminControls",
	"fetchAvailableModels",
	"retrieveUserQuotaSummary",
	"listExperiments",
}

// Minimal /v1internal response bodies.
//
// FIX-57: every shape below is byte-aligned with the known-good probe payloads
// the design session served to the real agy binary and reached print-mode
// generation end-to-end (refounded-topology.md:13). Sources:
//
//	%TEMP%\agy-gate\payloads\loadCodeAssist.json
//	%TEMP%\agy-gate\payloads\fetchUserInfo.json
//	%TEMP%\agy-gate\payloads\fetchAdminControls.json
//	%TEMP%\agy-gate\payloads\listExperiments.json
//	%TEMP%\agy-gate\payloads\retrieveUserQuotaSummary.json
//
// These are the proven-compatible contract: agy 1.3.5's protobuf-JSON parser
// rejects `user_tags` as an OBJECT (`proto: syntax error (line 1:33):
// unexpected token {` — the field is a repeated value, so it must be an ARRAY),
// which is what blocked every generation flow before this alignment
// (blind-qa-g4-rerun-report.md). The shapes are pinned byte-for-byte by
// TestInternalRPC_DefaultBodiesMatchProbe; RPCHooks still override per RPC.
type loadCodeAssistResponse struct {
	CloudaicompanionProject string         `json:"cloudaicompanion_project"`
	CurrentTier             map[string]any `json:"current_tier"`
	PaidTier                map[string]any `json:"paid_tier"`
}

type fetchUserInfoResponse struct {
	UserSettings map[string]any `json:"user_settings"`
	// UserTags is a repeated field on agy's wire, so it must marshal as an
	// array ([]). An object here is the FIX-57 defect.
	UserTags []any `json:"user_tags"`
	// RegionCode is the probe's proven value; agy consumes it as the region
	// hint and tolerates any non-empty code.
	RegionCode string `json:"region_code"`
}

type emptyCatalogResponse struct {
	Models          map[string]any `json:"models"`
	AgentModelSorts []any          `json:"agent_model_sorts"`
}

type quotaSummaryResponse struct {
	UserStatus quotaUserStatus `json:"userStatus"`
}

type quotaUserStatus struct {
	Groups []any `json:"groups"`
}

type listExperimentsResponse struct {
	// ExperimentIds is int32 on agy's wire; the string form is rejected
	// (audit-gap-probe-report).
	ExperimentIds []int `json:"experiment_ids"`
}

// healthzResponse is the open lifecycle body (§10.4).
type healthzResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

// buildHandler wires the route table and wraps it in the request pipeline.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	// Prefix handlers, not {model} wildcards: the model segment may itself
	// contain ':' and '/' (the colon trap, §3.3), which a single-segment
	// wildcard cannot express.
	mux.HandleFunc("/v1beta/models/", s.serveGenerate)
	mux.HandleFunc("/v1beta/models", s.serveListModels)
	mux.HandleFunc("/v1internal:"+verbGenerate, s.serveInternalGenerate)
	mux.HandleFunc("/v1internal:"+verbStreamGenerate, s.serveInternalGenerate)
	for _, rpc := range internalRPCNames {
		mux.HandleFunc("/v1internal:"+rpc, s.serveInternalRPC)
	}
	mux.HandleFunc("/healthz", s.serveHealthz)
	mux.HandleFunc("/health", s.serveHealthz)
	mux.HandleFunc("/", s.serveNotFound)
	return s.guard(mux)
}

// guard is the request pipeline: audit capture, loopback re-check, auth and
// the permissive-mode rate limit. Exactly one audit line is written per
// request, whatever the outcome.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		ar := &auditRecord{
			method:      capRunes(oneLine(r.Method), maxFieldRunes),
			path:        capRunes(oneLine(r.URL.Path), maxFieldRunes),
			headerNames: headerNames(r),
		}
		r = r.WithContext(withAuditRecord(r.Context(), ar))
		defer func() {
			s.noteActivity()
			s.audit.log(auditEntry{
				TS:          time.Now().UTC(),
				Method:      ar.method,
				Path:        ar.path,
				Route:       ar.route,
				Verb:        ar.verb,
				RPC:         ar.rpc,
				Model:       ar.model,
				Status:      rec.status,
				DurationMS:  time.Since(start).Milliseconds(),
				HeaderNames: ar.headerNames,
				Note:        ar.note,
			})
		}()
		// FIX-39: an unforeseen handler panic must still answer with a §9.1
		// envelope instead of dropping the connection. net/http recovers per
		// connection on its own, which leaves agy with a truncated stream and
		// no status. The recovered value is never written to the wire: only a
		// fixed, redacted message is.
		defer func() {
			if recovered := recover(); recovered != nil {
				if ar != nil {
					ar.addNote("handler panic recovered")
				}
				s.writeGoogleError(rec, http.StatusInternalServerError, statusInternal, "internal adapter error")
			}
		}()
		if !isLoopbackAddr(r.RemoteAddr) {
			s.writeGoogleError(rec, http.StatusForbidden, statusPermissionDenied, "request origin is not loopback")
			return
		}
		if !isHealthPath(r.URL.Path) {
			if !s.authorize(rec, r) {
				return
			}
			// FIX-58: the browser gate runs before the rate limiter, so a
			// refused simple request never spends the victim's budget.
			if !s.admitBrowserRequest(rec, r) {
				return
			}
			if !s.allow(rec, r) {
				return
			}
		}
		next.ServeHTTP(rec, r)
	})
}

// ServeHTTP exposes the request pipeline as a plain http.Handler (tests and
// embedding). An adopted remote handle answers 503: its listener lives in
// another process and is reachable through URL().
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.http == nil || s.http.Handler == nil {
		s.writeGoogleError(w, http.StatusServiceUnavailable, statusUnavailable,
			"this adapter handle adopted a remote singleton; use URL()")
		return
	}
	s.http.Handler.ServeHTTP(w, r)
}

// serveGenerate is the primary inbound route: POST
// /v1beta/models/<name>:streamGenerateContent[?alt=sse] and
// :generateContent (§5.1, §3.3).
func (s *Server) serveGenerate(w http.ResponseWriter, r *http.Request) {
	// Parse first: an unknown-but-gemini-shaped path (:countTokens,
	// :embedContent) is a 404 with a logged capture, whatever the method
	// (§5.1); the 405 is reserved for a real generate route.
	model, verb, ok := s.parseGeneratePath(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		s.writeGoogleError(w, http.StatusMethodNotAllowed, statusInvalidArgument,
			"method not allowed; this route accepts POST")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	req, err := geminiwire.DecodeGenerateRequest(body)
	if err != nil {
		s.writeGoogleError(w, http.StatusBadRequest, statusInvalidArgument, "invalid GenerateContentRequest body")
		return
	}
	s.dispatchGenerate(w, r, surfaceV1Beta, model, verb, altSSERequested(r, verb), req, nil)
}

// parseGeneratePath runs the §3.3 suffix parse on the raw path (RawPath keeps a
// percent-encoded colon distinct from a literal one; agy sends raw colons) and
// answers the §9.1 NOT_FOUND envelope for anything without a generate verb.
func (s *Server) parseGeneratePath(w http.ResponseWriter, r *http.Request) (model, verb string, ok bool) {
	raw := r.URL.RawPath
	if raw == "" {
		raw = r.URL.Path
	}
	model, verb, err := geminiwire.ParseGenerateRoute(raw)
	if err != nil {
		if ar := auditFromContext(r.Context()); ar != nil {
			ar.setClass(routeUnknown)
		}
		s.writeGoogleError(w, http.StatusNotFound, statusNotFound,
			"route "+r.URL.Path+" is not served by this adapter")
		return "", "", false
	}
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeGenerate)
		ar.setTarget(verb, "", model)
	}
	return model, verb, true
}

// serveListModels is the defensive discovery route (§5.7, GF-30): registered
// slugs only, one page, pageSize/pageToken tolerated.
func (s *Server) serveListModels(w http.ResponseWriter, r *http.Request) {
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeListModels)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeGoogleError(w, http.StatusMethodNotAllowed, statusInvalidArgument,
			"method not allowed; this route accepts GET")
		return
	}
	out := geminiwire.ListModelsResponse{
		Models:        []geminiwire.ModelInfo{},
		NextPageToken: "",
	}
	if reg := s.cfg.Registry; reg != nil {
		for _, p := range reg.Providers {
			for _, m := range p.Models {
				if !m.IsEnabled() {
					continue
				}
				out.Models = append(out.Models, geminiwire.ModelInfo{
					Name:                       "models/" + m.ID,
					DisplayName:                providers.DisplayName(m.ID, p.Name),
					Description:                modelsDescription,
					InputTokenLimit:            m.ContextLength,
					OutputTokenLimit:           m.MaxOutputTokens,
					SupportedGenerationMethods: []string{verbGenerate, verbStreamGenerate},
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// serveInternalGenerate is the Cloud Code envelope branch (Appendix A, GF-31):
// unwrap, route on .model, dispatch to the same seam with the envelope
// context. The handler re-wraps.
func (s *Server) serveInternalGenerate(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.RawPath
	if raw == "" {
		raw = r.URL.Path
	}
	verb, err := geminiwire.ParseInternalRoute(raw)
	if err != nil {
		if ar := auditFromContext(r.Context()); ar != nil {
			ar.setClass(routeUnknown)
		}
		s.writeGoogleError(w, http.StatusNotFound, statusNotFound,
			"route "+r.URL.Path+" is not served by this adapter")
		return
	}
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeInternalGen)
	}
	if r.Method != http.MethodPost {
		s.writeGoogleError(w, http.StatusMethodNotAllowed, statusInvalidArgument,
			"method not allowed; this route accepts POST")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	env, inner, err := geminiwire.UnwrapV1Internal(body)
	if err != nil {
		s.writeGoogleError(w, http.StatusBadRequest, statusInvalidArgument, "invalid /v1internal envelope body")
		return
	}
	if strings.TrimSpace(env.Model) == "" {
		s.writeGoogleError(w, http.StatusBadRequest, statusInvalidArgument,
			"invalid /v1internal envelope: no model")
		return
	}
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setTarget(verb, "", env.Model)
	}
	s.dispatchGenerate(w, r, surfaceV1Internal, env.Model, verb, altSSERequested(r, verb), inner, env)
}

// serveInternalRPC answers the /v1internal non-generation RPCs (Appendix A).
// A configured RPCHook overrides the default per RPC; anything unregistered
// falls through to the catch-all 404.
func (s *Server) serveInternalRPC(w http.ResponseWriter, r *http.Request) {
	rpc := strings.TrimPrefix(r.URL.Path, "/v1internal:")
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeInternalRPC)
		ar.setTarget("", rpc, "")
	}
	if r.Method != http.MethodPost {
		s.writeGoogleError(w, http.StatusMethodNotAllowed, statusInvalidArgument,
			"method not allowed; this route accepts POST")
		return
	}
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	if hook, exists := s.cfg.RPCHooks[rpc]; exists && hook != nil {
		out, err := hook(body)
		if err != nil {
			s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "RPC hook failed: "+err.Error())
			return
		}
		if !json.Valid(out) {
			s.writeGoogleError(w, http.StatusInternalServerError, statusInternal,
				"RPC hook returned an unparseable body")
			return
		}
		writeRawJSON(w, out)
		return
	}
	switch rpc {
	case "loadCodeAssist":
		writeJSON(w, http.StatusOK, loadCodeAssistResponse{
			CloudaicompanionProject: defaultProject,
			CurrentTier:             map[string]any{"id": freeTierID, "name": freeTierName},
			PaidTier:                map[string]any{"id": freeTierID, "name": freeTierName},
		})
	case "fetchUserInfo":
		writeJSON(w, http.StatusOK, fetchUserInfoResponse{
			UserSettings: map[string]any{},
			UserTags:     []any{},
			RegionCode:   defaultRegionCode,
		})
	case "fetchAdminControls", "onboardUser":
		writeJSON(w, http.StatusOK, struct{}{})
	case "fetchAvailableModels":
		s.serveCatalog(w)
	case "retrieveUserQuotaSummary":
		writeJSON(w, http.StatusOK, quotaSummaryResponse{UserStatus: quotaUserStatus{Groups: []any{}}})
	case "listExperiments":
		writeJSON(w, http.StatusOK, listExperimentsResponse{ExperimentIds: []int{}})
	default:
		s.writeGoogleError(w, http.StatusNotFound, statusNotFound, "RPC "+rpc+" is not served by this adapter")
	}
}

// serveCatalog answers fetchAvailableModels with the configured catalog bytes
// or the minimal empty catalog. The picker needs both models and
// agent_model_sorts to consider a catalog ready (surface-verification-report
// §3.2), so the default carries both keys, empty.
func (s *Server) serveCatalog(w http.ResponseWriter) {
	if s.cfg.CatalogFunc == nil {
		writeJSON(w, http.StatusOK, emptyCatalogResponse{
			Models:          map[string]any{},
			AgentModelSorts: []any{},
		})
		return
	}
	out, err := s.cfg.CatalogFunc()
	if err != nil {
		s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "catalog fetch failed: "+err.Error())
		return
	}
	if !json.Valid(out) {
		s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "catalog payload is not valid JSON")
		return
	}
	writeRawJSON(w, out)
}

// serveHealthz is the lifecycle route (§10.4). The body stays open; when an
// install nonce is configured the request must carry it in X-Agy-Pro-Install,
// compared in constant time.
func (s *Server) serveHealthz(w http.ResponseWriter, r *http.Request) {
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeHealthz)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writeGoogleError(w, http.StatusMethodNotAllowed, statusInvalidArgument,
			"method not allowed; this route accepts GET")
		return
	}
	if s.cfg.AuthKey != "" && !keyMatches(r.Header.Get(headerInstallNonce), s.cfg.AuthKey) {
		s.writeGoogleError(w, http.StatusUnauthorized, statusUnauthenticated, msgAuthFailed)
		return
	}
	writeJSON(w, http.StatusOK, healthzResponse{Status: "ok", Service: serviceName})
}

// serveNotFound is the explicit-404 discipline (§5.1): every unmatched route
// answers NOT_FOUND and leaves a method/path capture on the audit line, the
// same guard the bridge keeps.
func (s *Server) serveNotFound(w http.ResponseWriter, r *http.Request) {
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.setClass(routeUnknown)
	}
	s.writeGoogleError(w, http.StatusNotFound, statusNotFound, "route "+r.URL.Path+" is not served by this adapter")
}

// dispatchGenerate runs the §3.3 resolution order and hands a resolved request
// to the generation seam: exact slug match, terminal-segment match, the
// housekeeping list (answered locally, zero provider calls, §5.8), then the
// allow-list 404 that doubles as the scan guard (§3.3 rule 4, GF-32).
//
// The conversation-title turn is intercepted FIRST, before any resolution. It is
// housekeeping whatever model name agy resolved it to, and on the P1
// external-only catalog that name is an external (observed: ext-gamma-1), so the
// name-shaped housekeeping rule below would let the turn reach a provider and
// spend quota (G10, R6). The detector is prompt-based, so it holds on both
// surfaces; a turn it does not recognize falls through to the unchanged order.
func (s *Server) dispatchGenerate(w http.ResponseWriter, r *http.Request, surface, model, verb string, altSSE bool, req *geminiwire.GenerateContentRequest, env *geminiwire.V1InternalRequest) {
	// The title-turn signal is folded ONCE here and reused by both the local
	// answer below and the drift alarm on the forwarding path.
	shape := evalTitleTurnShape(req, env)
	if shape.local() {
		if ar := auditFromContext(r.Context()); ar != nil {
			ar.setClass(routeHousekeeping)
			ar.addNote("housekeeping: conversation-title turn answered locally; no provider call")
		}
		s.serveHousekeeping(w, verb, altSSE, req, env)
		return
	}
	target, resolved, err := s.resolveModelName(model)
	if err != nil {
		s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "model resolution failed")
		return
	}
	if resolved {
		if ar := auditFromContext(r.Context()); ar != nil {
			if surface == surfaceV1Internal {
				ar.setClass(routeInternalGen)
			} else {
				ar.setClass(routeGenerate)
			}
			// G10F-6 / G10F2-2, the R6 drift alarm. A turn with the title SHAPE
			// that was NOT answered locally is going to a provider, and the audit
			// line would otherwise read exactly like a normal generation. In the
			// G4 sandbox that shape of miss cost 13 of 34 upstream calls.
			//
			// Two independent arms, so one agy change cannot disable both: the
			// prompt signature (both surfaces, defeated by a reword) and the
			// envelope requestType "checkpoint" with no tools (/v1internal only,
			// prompt-independent). The note makes the regression greppable instead
			// of silent; it changes no behavior. On the public /v1beta wire there
			// is no envelope, so the alarm there stays phrase-keyed and a full
			// reword on that surface remains undetectable by the mesh.
			if shape.forwarded() {
				ar.addNote("title-shaped turn forwarded to provider (possible R6 drift)")
			}
		}
		g := &GenerateContext{
			Surface:     surface,
			Model:       model,
			Verb:        verb,
			AltSSE:      altSSE,
			Target:      target,
			Request:     req,
			Envelope:    env,
			HeaderNames: headerNames(r),
		}
		if err := s.generate.Generate(w, r, g); err != nil {
			s.writeSeamError(w, err)
		}
		return
	}
	if IsHousekeepingName(model) {
		if ar := auditFromContext(r.Context()); ar != nil {
			ar.setClass(routeHousekeeping)
		}
		s.serveHousekeeping(w, verb, altSSE, req, env)
		return
	}
	s.writeGoogleError(w, http.StatusNotFound, statusNotFound,
		"model "+model+" is not served by this adapter")
}

// resolveModelName applies the configured allow-list seam, defaulting to
// registry slug resolution (§3.3 rules 1-2).
func (s *Server) resolveModelName(name string) (providers.Target, bool, error) {
	if s.cfg.ResolveModel != nil {
		return s.cfg.ResolveModel(name)
	}
	target, ok := resolveRegistrySlug(s.cfg.Registry, name)
	return target, ok, nil
}

// writeSeamError maps a seam error onto the wire: a *GenerateError carries its
// own §9.1 triple, errNotImplemented is the placeholder 501, anything else is
// a fixed 500 (the raw error never reaches the wire).
func (s *Server) writeSeamError(w http.ResponseWriter, err error) {
	var genErr *GenerateError
	switch {
	case errors.As(err, &genErr):
		s.writeGoogleError(w, genErr.Code, genErr.Status, genErr.Message)
	case errors.Is(err, errNotImplemented):
		s.writeGoogleError(w, http.StatusNotImplemented, statusUnimplemented, errNotImplemented.Error())
	default:
		s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "internal adapter error")
	}
}

// resolveRegistrySlug is the default §3.3 allow-list: exact slug match, then
// the terminal segment (after the last "/models/", else after the last "/").
// Enabled models only, matching what the listing route exposes (§5.7).
func resolveRegistrySlug(reg *providers.Registry, name string) (providers.Target, bool) {
	if reg == nil || name == "" {
		return providers.Target{}, false
	}
	if target, ok := findSlug(reg, name); ok {
		return target, true
	}
	if terminal := terminalSegment(name); terminal != name {
		if target, ok := findSlug(reg, terminal); ok {
			return target, true
		}
	}
	return providers.Target{}, false
}

func terminalSegment(name string) string {
	if i := strings.LastIndex(name, "/models/"); i >= 0 {
		return name[i+len("/models/"):]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func findSlug(reg *providers.Registry, slug string) (providers.Target, bool) {
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if m.ID == slug && m.IsEnabled() {
				return providers.Target{Provider: p, Model: m}, true
			}
		}
	}
	return providers.Target{}, false
}

// altSSERequested reports whether the caller wants SSE framing. The stream
// verb always does; the non-stream verb tolerates a stray ?alt=sse (§5.1:
// tolerate the parameter's absence rather than 400).
func altSSERequested(r *http.Request, verb string) bool {
	return verb == verbStreamGenerate || r.URL.Query().Get("alt") == "sse"
}

// readBody reads the request body under the 32 MB inbound cap (M6) and answers
// with the §9.1 envelope when the body must be rejected.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, openaiup.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err == nil {
		return body, true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		s.writeGoogleError(w, http.StatusBadRequest, statusInvalidArgument, "request body exceeds limit")
		return nil, false
	}
	s.writeGoogleError(w, http.StatusBadRequest, statusInvalidArgument, "request body could not be read")
	return nil, false
}

// allow applies the per-install token bucket (§10.2, §6.4). A request whose
// auth posture is passive is untrusted, so the budget applies: that covers
// fully permissive mode (no AuthKey) and, under FIX-44, the /v1internal routes
// when the caller opted into passive receipt. A strict-posture request (a valid
// nonce holder) is not rate limited, per §6.4's "permissive auth mode" wording.
func (s *Server) allow(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter == nil {
		return true
	}
	if s.cfg.AuthKey != "" && s.postureFor(r.URL.Path) != authPassive {
		return true
	}
	if s.limiter.take() {
		return true
	}
	s.writeGoogleError(w, http.StatusTooManyRequests, statusResourceExhausted,
		"adapter rate limit exceeded (60 requests per minute per install); retry later")
	return false
}

// tokenBucket is the per-install rate limiter (R8: per-install, never
// per-session).
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(perMinute, burst int) *tokenBucket {
	return &tokenBucket{
		rate:   float64(perMinute) / 60.0,
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
	}
}

func (b *tokenBucket) take() bool {
	if b == nil {
		return true
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// isLoopbackAddr reports whether a RemoteAddr is a loopback peer: defense in
// depth on top of the loopback-only bind (§10.4).
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isHealthPath lists the lifecycle routes the pipeline exempts from data auth
// and rate limiting (the shim polls them; a poll storm must not spend the
// request budget).
func isHealthPath(path string) bool {
	return path == "/healthz" || path == "/health"
}

// statusRecorder captures the response status for the audit line and forwards
// flushes so an SSE stream is never buffered by the recorder.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.wroteHeader {
		return
	}
	sr.wroteHeader = true
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.WriteHeader(http.StatusOK)
	}
	return sr.ResponseWriter.Write(b)
}

func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeRawJSON emits caller-supplied JSON bytes (an RPC hook or catalog
// payload) verbatim.
func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeSSE emits the canonical Gemini SSE frame sequence (§8.1): one
// single-line JSON payload per data frame, no [DONE] sentinel, flushed per
// frame.
func writeSSE(w http.ResponseWriter, frames []string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}
