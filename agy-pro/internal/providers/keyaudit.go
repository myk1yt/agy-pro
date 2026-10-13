package providers

// Key audit: zero-token upstream probe, secret masking, and per-provider key
// source auditing. These are the building blocks for a doctor check that must
// never report "presence-only OK": a key that resolves but fails authentication
// is reported as invalid, and an unreachable network is reported as SKIP, not
// as an error.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// KeyProbeStatus is the verdict of one upstream credential probe.
type KeyProbeStatus string

const (
	// ProbeValid: the upstream accepted the credential (HTTP 2xx).
	ProbeValid KeyProbeStatus = "valid"
	// ProbeInvalid: the upstream rejected the credential (HTTP 401/403).
	ProbeInvalid KeyProbeStatus = "invalid"
	// ProbeIndeterminate: the upstream answered but the status code carries no
	// auth verdict (404 endpoint missing, 429 rate limit, 5xx, ...). Never
	// treat this as OK.
	ProbeIndeterminate KeyProbeStatus = "indeterminate"
	// ProbeSkipped: no verdict was possible (no key, no base URL, or the
	// network was unreachable). SKIP is not an error.
	ProbeSkipped KeyProbeStatus = "skipped"
)

// KeyProbeResult is the outcome of probing one credential against one endpoint.
// Detail is safe to print: the probed key is redacted from it.
type KeyProbeResult struct {
	Status     KeyProbeStatus `json:"status"`
	HTTPStatus int            `json:"httpStatus,omitempty"`
	// Host is the destination host[:port] the probe (and every later chat
	// request) talks to. It is surfaced in Detail and in doctor output so an
	// unexpected endpoint cannot hide behind a generic error (S-6).
	Host   string `json:"host,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// probeTimeout bounds one ProbeKeyValidity call. The QA baseline used the same
// zero-token GET /models pattern (qa-baseline-report.md S4).
const probeTimeout = 5 * time.Second

// endpointHost extracts the destination host[:port] of a provider base URL so
// probes and diagnostics can always name where credentials are being sent.
func endpointHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return u.Host
}

// ProbeKeyValidity performs a zero-token-cost probe: GET <baseURL>/models with
// a Bearer credential. 2xx validates the key, 401/403 invalidates it, any other
// HTTP answer is indeterminate, and a transport failure (DNS, dial, timeout) is
// a SKIP so an offline machine is not reported as having broken keys. Every
// result names its destination host (S-6).
func ProbeKeyValidity(baseURL, apiKey string) KeyProbeResult {
	host := endpointHost(baseURL)
	if strings.TrimSpace(baseURL) == "" {
		return KeyProbeResult{Status: ProbeSkipped, Detail: "no baseURL configured; nothing to probe"}
	}
	if strings.TrimSpace(apiKey) == "" {
		return KeyProbeResult{Status: ProbeSkipped, Host: host, Detail: "no API key resolved; nothing to probe"}
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return KeyProbeResult{Status: ProbeSkipped, Host: host, Detail: redactProbeDetail("dest "+host+": "+err.Error(), apiKey)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// Transport-level failure: treat as network-blocked SKIP.
		return KeyProbeResult{Status: ProbeSkipped, Host: host, Detail: redactProbeDetail("dest "+host+": network unreachable: "+err.Error(), apiKey)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return KeyProbeResult{Status: ProbeValid, HTTPStatus: resp.StatusCode, Host: host, Detail: "dest " + host + ": credential accepted by upstream"}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		detail := strings.TrimSpace(string(snippet))
		if detail == "" {
			detail = "credential rejected by upstream"
		}
		return KeyProbeResult{Status: ProbeInvalid, HTTPStatus: resp.StatusCode, Host: host, Detail: redactProbeDetail("dest "+host+": "+detail, apiKey)}
	default:
		return KeyProbeResult{
			Status:     ProbeIndeterminate,
			HTTPStatus: resp.StatusCode,
			Host:       host,
			Detail:     fmt.Sprintf("dest %s: upstream answered HTTP %d; no auth verdict (endpoint may not support GET /models)", host, resp.StatusCode),
		}
	}
}

func redactProbeDetail(detail, apiKey string) string {
	if apiKey == "" {
		return detail
	}
	return strings.ReplaceAll(detail, apiKey, "[REDACTED]")
}

// MaskSecret renders any API-key value for display: first 5 RUNES + "****"
// (contract §2.11 / U8). Values of length <= 5 runes render as "****" (a short
// key is never echoed, not even partially). Nothing beyond the 5 leading
// characters is ever exposed — specifically NO trailing suffix. The result
// counts runes, not bytes, so multi-byte keys mask at the same boundary.
func MaskSecret(secret string) string {
	r := []rune(strings.TrimSpace(secret))
	if len(r) <= 5 {
		return "****"
	}
	return string(r[:5]) + "****"
}

// MaskOperand renders an untrusted CLI operand inside an error or status
// message (B2 / U8). A misdirected run_command can carry a pasted key in any
// operand slot, so credential-looking operands render as MaskSecret (first-5 +
// **** and never more). Benign operands — conventional env-var names, short
// provider/model tokens, model IDs, location strings (URLs, absolute or
// ./-prefixed paths) — keep their readable form so errors stay actionable, but
// still pass through the generic redactor so pattern-shaped secrets (bearer
// tokens, sk- IDs, AWS key IDs, PEM blocks) embedded in them cannot survive
// into a session transcript.
func MaskOperand(s string) string {
	if s == "" {
		return s
	}
	if operandLooksSecret(s) {
		return MaskSecret(s)
	}
	return NewRedactor()(s)
}

// operandLooksSecret decides whether an echoed operand is plausibly credential
// material. Unlike isLikelyAPIKey (which must reject hyphenated strings in the
// apiKeyEnv slot because env names never carry hyphens), this keeps ordinary
// hyphenated tokens ("my-provider", "native-seat-concurrency", model IDs like
// "claude-3-5-sonnet-20241022") and location strings readable while still
// catching real key shapes and opaque contiguous runs (C3).
func operandLooksSecret(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	// Known credential shapes are secrets in every slot.
	if credentialShapeRe.MatchString(t) {
		return true
	}
	lower := strings.ToLower(t)
	for _, p := range []string{
		"sk-", "key-", "gsk_", "nvapi-", "live_", "secret_",
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
		"xox", "glpat-", "npm_", "dckr_pat_", "eyj",
	} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Conventional environment-variable names (UPPER_SNAKE) stay readable;
	// the credential-shape pass above already caught look-alikes.
	if envNameRe.MatchString(t) && strings.ToUpper(t) == t {
		return false
	}
	// LOCATION-shaped strings stay readable (spawn errors must name the
	// binary): a URL scheme, an absolute path, or a leading ./ ../ .\ ..\ .
	// A bare "/" or "\" anywhere does NOT exempt (C3): roughly half of real
	// AWS secret access keys contain "/", and the old contains-any exemption
	// echoed them in full at closed sites such as
	// `agy pro settings set native-seat-concurrency '<value>'`.
	if isLocationShaped(t) {
		return false
	}
	// Short provider/model-style tokens stay readable.
	if len(t) < 24 && providerNameRe.MatchString(t) {
		return false
	}
	// Length heuristic, narrowed to CONTIGUOUS secret-shaped runs (C3): opaque
	// credentials (AWS secret keys, most carrying "/" or "+") are one
	// uninterrupted base64 run, while separator-carrying tokens (model IDs,
	// dotted versions, multi-word operands) stay readable so error text and
	// the wizard's copy-paste completion hints survive.
	return len(t) >= 20 && contiguousSecretRun(t)
}

// isLocationShaped reports whether s names a location rather than carrying a
// value: a URL scheme ("://"), an absolute path (filepath.IsAbs), or a leading
// ./ ../ .\ ..\ relative marker. A string that merely contains "/" or "\"
// elsewhere is not a location: AWS secret access keys do (C3).
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

// contiguousSecretRun reports whether s is one uninterrupted run of the
// base64 alphabet (A-Za-z0-9+/=) with no "-", "_", or "." separators: the
// shape of an opaque credential. Separator-carrying tokens (model IDs such as
// claude-3-5-sonnet-20241022, env-var names, multi-word operands) fail the
// test and stay readable (C3).
func contiguousSecretRun(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return true
}

// EnvCandidateMasked is the printable view of an EnvCandidate: same location
// information, value replaced by its masked form.
type EnvCandidateMasked struct {
	Root        string `json:"root"`
	Name        string `json:"name"`
	Exact       bool   `json:"exact"`
	MaskedValue string `json:"maskedValue"`
}

// KeyAudit is the doctor-facing verdict for one provider credential.
type KeyAudit struct {
	Provider string `json:"provider"`
	Env      string `json:"env"`
	// Resolved reports whether a non-empty key value was found at all.
	Resolved bool `json:"resolved"`
	// Origin names the source the resolved value came from: process-env,
	// hkcu-exact, hklm-exact, or one of the -bom-variant origins when only a
	// polluted name carried a value.
	Origin KeyOrigin `json:"origin,omitempty"`
	// MaskedHint is MaskSecret of the resolved value ("" when unresolved).
	MaskedHint string `json:"maskedHint,omitempty"`
	// ExactPresent / VariantPresent report whether clean-name and polluted-name
	// entries exist (process block and persistent stores combined).
	ExactPresent   bool `json:"exactPresent"`
	VariantPresent bool `json:"variantPresent"`
	// Conflict is true when both forms exist and at least one pair of values
	// differs: exactly the state that makes exact-first resolution pick an
	// unverified value while a (possibly valid) variant sits beside it.
	Conflict   bool                 `json:"conflict"`
	Candidates []EnvCandidateMasked `json:"candidates,omitempty"`
	// Probe is the upstream verdict for the resolved value. Unresolved keys are
	// reported as ProbeSkipped, never probed.
	Probe KeyProbeResult `json:"probe"`
}

// AuditOptions configures AuditProviderKeys. Probe and Store are optional test
// seams; nil selects ProbeKeyValidity and the default platform store.
type AuditOptions struct {
	Probe func(baseURL, apiKey string) KeyProbeResult
	Store EnvStore
}

// AuditProviderKeys audits every provider that declares an APIKeyEnv: where the
// key resolved from, whether clean/BOM name variants coexist, and whether the
// resolved value actually authenticates upstream. It never prints or logs
// secret values; every value it exposes passes through MaskSecret.
func AuditProviderKeys(reg *Registry, opts AuditOptions) []KeyAudit {
	var out []KeyAudit
	if reg == nil {
		return out
	}
	probe := opts.Probe
	if probe == nil {
		probe = ProbeKeyValidity
	}
	store := opts.Store
	if store == nil {
		store = defaultEnvStore
	}
	for _, p := range reg.Providers {
		if p.APIKeyEnv == "" {
			continue
		}
		a := KeyAudit{Provider: p.Name, Env: p.APIKeyEnv}
		val, origin, _ := resolveEnvValueWithStore(p.APIKeyEnv, store)
		a.Resolved = val != ""
		a.Origin = origin
		if a.Resolved {
			a.MaskedHint = MaskSecret(val)
		}
		cands := append(processEnvCandidates(p.APIKeyEnv), resolveEnvCandidatesWithStore(p.APIKeyEnv, store)...)
		for _, c := range cands {
			a.Candidates = append(a.Candidates, EnvCandidateMasked{
				Root:        c.Root,
				Name:        c.Name,
				Exact:       c.Exact,
				MaskedValue: MaskSecret(c.Value),
			})
			if c.Exact {
				a.ExactPresent = true
			} else {
				a.VariantPresent = true
			}
		}
		a.Conflict = a.ExactPresent && a.VariantPresent && candidateValuesDiffer(cands)
		if !a.Resolved {
			a.Probe = KeyProbeResult{Status: ProbeSkipped, Detail: "no usable key found for " + p.APIKeyEnv}
		} else {
			a.Probe = probe(p.BaseURL, val)
		}
		out = append(out, a)
	}
	return out
}

func candidateValuesDiffer(cands []EnvCandidate) bool {
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[i].Value != cands[j].Value {
				return true
			}
		}
	}
	return false
}
