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
	"regexp"
	"strings"
	"time"
	"unicode"
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
// into a session transcript. One uninterrupted base64 run of 16+ runes
// (AKIA-class length, D7) is always treated as credential material, even when
// its charset also reads as an env-var or provider name.
func MaskOperand(s string) string {
	if s == "" {
		return s
	}
	// I3: the decorated-secret and JWT classes are the single DISPLAY
	// authority in decoratedSecretShaped/isJWTShapedOperand; folding them in
	// here makes every display sink (banner, report bodies, JSON, preview)
	// mask decorated operands while locations and identifier-class model IDs
	// stay readable. Seeding keeps the wider core (seedShaped): a composite
	// error embeds multi-word operands that must never render raw.
	if operandLooksSecret(s) || isJWTShapedOperand(strings.TrimSpace(s)) || decoratedSecretShaped(s) {
		return MaskSecret(s)
	}
	return NewRedactor()(s)
}

// MaskFamilyName renders a model's family for display (C2). An explicit
// registry family is CASE-PRESERVED and masked through MaskOperand, so a
// key-shaped value pasted into the family slot renders first-5+****: the
// decorated class requires an uppercase letter, which the folded
// FamilyName() would have destroyed and let a lowercase-shaped secret ride
// raw. With no explicit family the id-inferred FamilyName() is masked
// instead (the id itself can carry a key-shaped token). Benign families
// ("gemini", "glm") keep their readable form.
func MaskFamilyName(m Model) string {
	if strings.TrimSpace(m.Family) != "" {
		return MaskOperand(m.Family)
	}
	return MaskOperand(m.FamilyName())
}

// MaskProviderForReport renders a Provider for a report/JSON sink (S4):
// every string field that can carry a registry value (Name, APIKeyEnv,
// BaseURL, and every Model string field) routes through the operand masker,
// so a nested Target.Provider serialized into summary.json or --json output
// cannot leak a key-shaped value while benign names, env-var names, URLs,
// and model IDs stay readable. The input is not mutated.
func MaskProviderForReport(p Provider) Provider {
	out := p
	out.Name = MaskOperand(p.Name)
	out.BaseURL = MaskOperand(p.BaseURL)
	out.APIKeyEnv = MaskOperand(p.APIKeyEnv)
	if p.Models != nil {
		out.Models = make([]Model, len(p.Models))
		for i, m := range p.Models {
			out.Models[i] = MaskModelForReport(m)
		}
	}
	return out
}

// MaskModelForReport renders one Model for a report/JSON sink (S4): the ID
// through MaskOperand, the family through MaskFamilyName (case-preserved so
// a key-shaped explicit family still catches the decorated class), and every
// other registry-controlled string field (tags, allowed tools, reasoning
// effort) through MaskOperand. Benign values are unchanged; the input is
// not mutated.
func MaskModelForReport(m Model) Model {
	out := m
	out.ID = MaskOperand(m.ID)
	out.Family = MaskFamilyName(m)
	out.Tags = maskStringSliceForReport(m.Tags)
	out.Capabilities.AllowedTools = maskStringSliceForReport(m.Capabilities.AllowedTools)
	out.Capabilities.ReasoningEffort = MaskOperand(m.Capabilities.ReasoningEffort)
	return out
}

// maskStringSliceForReport masks each element of a registry-controlled
// string slice for a report sink; a nil slice stays nil.
func maskStringSliceForReport(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = MaskOperand(s)
	}
	return out
}

// quotedSegmentRe matches one double-quoted segment inside a larger line. Go's
// flag package quotes the offending value in its errors, so a pasted key
// surfaces between quotes in an otherwise benign sentence.
var quotedSegmentRe = regexp.MustCompile(`"[^"]*"`)

// RedactEchoLine masks secret-shaped QUOTED segments inside a larger sentence
// (D1-helper) and returns the line otherwise unchanged. Stdlib flag errors
// embed the bad value in quotes — `invalid value "sk-..." for flag -timeout` —
// so flag-set output routes through this before printing: each quoted
// segment's content passes through MaskOperand (U8: first 5 runes + **** for
// credential shapes, benign tokens unchanged), while the surrounding sentence
// (flag names, usage text) is left exactly as-is.
func RedactEchoLine(s string) string {
	return quotedSegmentRe.ReplaceAllStringFunc(s, func(q string) string {
		return `"` + MaskOperand(q[1:len(q)-1]) + `"`
	})
}

// jwtOperandRe matches a WHOLE operand in the 3-segment JWT shape
// (header.payload.signature). Together with the per-segment and total floors
// below it covers JWTs whose header does not start with "eyJ" and so escapes
// the classifier's credentialShapeRe.
var jwtOperandRe = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

const (
	// jwtShapeFloorRunes is the whole-token floor of the JWT shape.
	jwtShapeFloorRunes = 16
	// jwtSegmentFloorRunes is the per-segment floor of the JWT shape: a real
	// header/payload/signature segment is a base64 blob, never a short word, so
	// dotted names ("config.dev.override", "claude-3.5.sonnet") stay readable
	// (r5 N1 over-mask) while blob-segment tokens seed.
	jwtSegmentFloorRunes = 6
	// decoratedSecretFloorRunes is the squash-length floor of the decorated
	// secret class. It sits ABOVE 20, not at 20: a 20-rune mixed-case squash is
	// the filename class ("MyReport2024FINAL.pdf"), which must stay readable,
	// while the 21+ rune class ("1-ASIAIOSFODNN7EXAMPLE") seeds. The bare
	// 20-rune uppercase class ("ABCDEFGHIJKLMNOPQRST") seeds through the
	// classifier's contiguous-run rule instead, so no coverage is lost.
	decoratedSecretFloorRunes = 21
)

// isJWTShapedOperand reports whether s (already trimmed) is a 3-segment JWT:
// exactly three dot-separated base64url segments, each at least
// jwtSegmentFloorRunes runes, totaling at least jwtShapeFloorRunes runes.
func isJWTShapedOperand(s string) bool {
	if !jwtOperandRe.MatchString(s) {
		return false
	}
	total := 0
	for _, seg := range strings.Split(s, ".") {
		n := len([]rune(seg))
		if n < jwtSegmentFloorRunes {
			return false
		}
		total += n
	}
	return total >= jwtShapeFloorRunes
}

// squashOperand strips the separator characters (`/ : + = . _ -`) and
// whitespace from an operand, collapsing a decorated secret
// ("1-<aws-secret>", "native:<aws-secret>/x", "native: <aws-secret>") to its
// opaque core so length and charset can be judged as one run.
func squashOperand(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', ':', '+', '=', '.', '_', '-', ' ', '\t', '\n', '\v', '\f', '\r':
			return -1
		}
		return r
	}, s)
}

// identifierSegmented reports whether operand is an identifier-class token:
// four or more separator-free segments, none of which is a long opaque run
// (16+ runes). Qwen/Qwen2.5-72B-Instruct (5 segments, longest 8) and
// meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo (9 segments, longest 7) are
// identifiers; a 16+ rune segment means a decorated secret
// (native:<aws>/x, 1-2-3-4-5-native:<aws>, wJalr+XU/tnF/.../bPxRfiCYEXAMPLEKEY),
// which must still seed. Real secrets are one opaque core with at most a slash
// or two of decoration, so the combination separates the classes where length
// and case cannot.
func identifierSegmented(operand string) bool {
	n, longest := 0, 0
	for _, seg := range strings.FieldsFunc(operand, func(r rune) bool {
		switch r {
		case '/', ':', '+', '=', '.', '_', '-', ' ', '\t':
			return true
		}
		return false
	}) {
		if seg == "" {
			continue
		}
		n++
		if l := len([]rune(seg)); l > longest {
			longest = l
		}
	}
	return n >= 4 && longest < 16
}

// seedShaped is the shared CORE of the decorated-secret class: not
// location-shaped, not identifier-class, and its separator-stripped squash
// reaches the floor with an uppercase letter. It deliberately carries NO
// whitespace gate: a composite error is a sentence a library call built
// around a raw CLI operand, so the operand can span many words, and every
// echo of it inside that sentence must still be masked. The display side
// narrows this core through decoratedSecretShaped; MaskOperand never routes
// through the core directly.
func seedShaped(operand string) bool {
	if isLocationShaped(operand) || identifierSegmented(operand) {
		return false
	}
	sq := squashOperand(operand)
	return len([]rune(sq)) >= decoratedSecretFloorRunes && strings.IndexFunc(sq, unicode.IsUpper) >= 0
}

// decoratedSecretShaped is the DISPLAY narrowing of the decorated-secret
// class and the one display-side authority: the seedShaped core plus at
// most one interior space and no tab/newline (the whitespace-decorated form
// "native: <aws>"; a sentence carries many and must never render wholly
// masked). Every display sink (MaskOperand, banners, report bodies, JSON,
// previews) masks through this function, while seeding uses the wide core so
// a composite sentence still masks the operand wherever it echoes it.
func decoratedSecretShaped(operand string) bool {
	if !seedShaped(operand) {
		return false
	}
	return strings.Count(operand, " ") <= 1 && !strings.ContainsAny(operand, "\t\n")
}

// SeedEligible reports whether operand may seed the composite-error redactor
// (MaskSentence). A composite error is a sentence a library call built from a
// raw CLI operand (*fs.PathError wraps the path into the sentence, seat-spec
// validators quote the token): MaskOperand classifies the WHOLE string, and a
// full sentence never looks secret-shaped, so the redactor is seeded with the
// operand and masks it wherever the sentence echoes it. This is the ONE
// seed-eligibility rule, shared by the cmd/agy wrapper and internal/fanout. An
// operand seeds when it is NOT location-shaped AND any of:
//
//   - the canonical classifier masks it (operandLooksSecret or the generic
//     redactor patterns): sk-/AKIA/ASIA shapes, 16+ rune contiguous base64
//     runs (D7), isLikelyAPIKey verdicts;
//   - the whole trimmed operand is a 3-segment JWT (isJWTShapedOperand),
//     covering headers the classifier cannot see;
//   - it is a decorated secret (seedShaped): the separator-carrying class
//     whose squash defeats the contiguous-run heuristic. The seed core is
//     deliberately WIDER than the display class (decoratedSecretShaped): a
//     composite error embeds multi-word operands, so the whitespace gate
//     that keeps a sentence from rendering wholly masked must not narrow
//     what seeds.
//
// Location-shaped operands (a URL scheme, an absolute path, or a leading
// ./ ../ .\ ..\ marker) never seed: spawn errors must name the binary, and
// their inner base64 runs are ordinary path segments.
func SeedEligible(operand string) bool {
	if operand == "" || isLocationShaped(operand) {
		return false
	}
	if operandLooksSecret(operand) {
		return true
	}
	if NewRedactor()(operand) != operand {
		return true
	}
	if isJWTShapedOperand(strings.TrimSpace(operand)) {
		return true
	}
	return seedShaped(operand)
}

// FlagSeedOperands returns the redactor seeds for multi-token flag values:
// each whole value plus its comma/space/semicolon-separated tokens (newlines
// and carriage returns split too, so a value pasted with a trailing newline
// still yields the bare token as a seed). Validators quote individual tokens
// ("seat 2: \"native:<plant>\""), and a whole-value seed is not a substring
// of that sentence, so the token forms must seed too (the r6 fanout/doctor
// multi-token gap).
func FlagSeedOperands(values ...string) []string {
	seen := map[string]bool{}
	var ops []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			ops = append(ops, s)
		}
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		add(v)
		for _, tok := range strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == ' ' || r == ';' || r == '\t' || r == '\n' || r == '\r'
		}) {
			add(tok)
		}
	}
	return ops
}

// MaskSentence renders an error or status message that may embed raw CLI
// operands (the composite-error class). Every SeedEligible operand seeds the
// redactor together with its trimmed form (a parser may echo the operand
// without surrounding whitespace), so a planted secret is masked wherever the
// sentence echoes it while the sentence's own wording stays intact. With no
// eligible operand the sentence falls back to MaskOperand, which keeps benign
// operands readable and still applies the generic credential patterns.
func MaskSentence(sentence string, operands ...string) string {
	seen := map[string]bool{}
	var seeds []string
	addSeed := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			seeds = append(seeds, s)
		}
	}
	for _, op := range operands {
		if !SeedEligible(op) {
			continue
		}
		addSeed(op)
		addSeed(strings.TrimSpace(op))
	}
	if len(seeds) == 0 {
		return MaskOperand(sentence)
	}
	return NewRedactor(seeds...)(sentence)
}

// operandLooksSecret decides whether an echoed operand is plausibly credential
// material. Unlike isLikelyAPIKey (which must reject hyphenated strings in the
// apiKeyEnv slot because env names never carry hyphens), this keeps ordinary
// hyphenated tokens ("my-provider", "native-seat-concurrency", model IDs like
// "claude-3-5-sonnet-20241022") and location strings readable while still
// catching real key shapes and opaque contiguous runs (C3, D7).
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
	// LOCATION-shaped strings stay readable (spawn errors must name the
	// binary): a URL scheme, an absolute path, or a leading ./ ../ .\ ..\ .
	// A bare "/" or "\" anywhere does NOT exempt (C3): roughly half of real
	// AWS secret access keys contain "/", and the old contains-any exemption
	// echoed them in full at closed sites such as
	// `agy pro settings set native-seat-concurrency '<value>'`.
	if isLocationShaped(t) {
		return false
	}
	// Length heuristic, narrowed to CONTIGUOUS secret-shaped runs (C3) and
	// lowered to the AKIA-class length of 16 runes (D7): one uninterrupted
	// base64 run of 16+ runes is an opaque credential (AWS secret keys, most
	// carrying "/" or "+"; bare alnum access-key IDs such as
	// ABCDEFGHIJKLMNOPQRST), never a name. The check runs BEFORE the env-name
	// and provider-name exemptions below because both charsets also admit
	// bare alnum runs — the old >=20 threshold let a 16-23 rune credential
	// print raw (`providers test ABCDEFGHIJKLMNOPQRST`). 15 runes stays
	// readable.
	if len(t) >= 16 && contiguousSecretRun(t) {
		return true
	}
	// Conventional environment-variable names (UPPER_SNAKE) stay readable;
	// the credential-shape pass above already caught look-alikes, and real env
	// names carry "_" separators so they never reach the contiguous-run test.
	if envNameRe.MatchString(t) && strings.ToUpper(t) == t {
		return false
	}
	// Short provider/model-style tokens stay readable.
	if len(t) < 24 && providerNameRe.MatchString(t) {
		return false
	}
	return false
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
			// The apiKeyEnv NAME is echoed here; a legacy registry can hold a
			// credential in that slot, so it routes through MaskOperand like
			// every other echo (C5/N2 class, D3-tail).
			a.Probe = KeyProbeResult{Status: ProbeSkipped, Detail: "no usable key found for " + MaskOperand(p.APIKeyEnv)}
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
