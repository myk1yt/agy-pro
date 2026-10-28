package wirecheck

import (
	"bytes"
	"strings"
)

// Hybrid profile names. The client-path split (design §0: one session cannot
// have both halves) gates the profiles independently, so a binary that
// satisfies P1 but not P2 still allows P1 sessions.
const (
	ProfileP1 = "P1"
	ProfileP2 = "P2"
)

// canarySet is the drift canary table. Every literal was verified live
// against agy.exe v1.3.5 in design session 261014_0006:
//
//	P1  binary-surface-report.md S3 (CLOUD_CODE_URL env var + the override
//	    log line), surface-verification-report.md Q-B (the /v1internal route
//	    literals), plan-model-gate-report.md G1 (the plan-model gate string
//	    that proves the ModelDetails.model enum requirement path exists).
//	P2  binary-surface-report.md S1/S2 (gemini-mode env/settings keys, the
//	    customModels scheme regex), custommodels-probe-report.md (the
//	    google-genai-sdk/ User-Agent family marker).
//
// The three P1 route canaries carry the FULL "/v1internal:" prefix (review
// round 1, FIX-9): a bare ":streamGenerateContent" is satisfied by the 11
// non-/v1internal surfaces (the P2 gemini format string among them), and a
// route-prefix drift (/v1internal: -> /v2internal:) would keep a
// suffix-only gate GO while the mesh 404s every RPC.
//
// Order is stable (P1 then P2); Render and Reasons walk it in this order.
func canarySet() []Canary {
	return []Canary{
		{Profile: ProfileP1, Name: "cloud_code_url_env", Literal: "CLOUD_CODE_URL"},
		{Profile: ProfileP1, Name: "cloud_code_url_override_log", Literal: "Overriding CloudCodeServerURL via CLOUD_CODE_URL environment variable"},
		{Profile: ProfileP1, Name: "route_fetch_available_models", Literal: "/v1internal:fetchAvailableModels"},
		{Profile: ProfileP1, Name: "route_stream_generate_content", Literal: "/v1internal:streamGenerateContent"},
		{Profile: ProfileP1, Name: "route_load_code_assist", Literal: "/v1internal:loadCodeAssist"},
		{Profile: ProfileP1, Name: "plan_model_gate", Literal: "plan model not specified"},

		{Profile: ProfileP2, Name: "scheme_gemini_api", Literal: "gemini-api:"},
		{Profile: ProfileP2, Name: "scheme_blade", Literal: "blade:"},
		{Profile: ProfileP2, Name: "scheme_evergreen", Literal: "evergreen:"},
		{Profile: ProfileP2, Name: "scheme_beyond_api", Literal: "beyond-api:"},
		{Profile: ProfileP2, Name: "genai_sdk_user_agent", Literal: "google-genai-sdk/"},
		{Profile: ProfileP2, Name: "env_google_gemini_base_url", Literal: "GOOGLE_GEMINI_BASE_URL"},
		{Profile: ProfileP2, Name: "env_gemini_api_key", Literal: "GEMINI_API_KEY"},
		{Profile: ProfileP2, Name: "settings_model_provider", Literal: "modelProvider"},
	}
}

// scanCanaries evaluates the table against raw binary bytes. Only presence
// booleans are kept; the bytes themselves are never retained or logged.
func scanCanaries(data []byte) []Canary {
	canaries := canarySet()
	for i := range canaries {
		canaries[i].Found = bytes.Contains(data, []byte(canaries[i].Literal))
	}
	return canaries
}

// profileOrder lists the gated profiles in canarySet order.
func profileOrder() []string {
	var order []string
	seen := map[string]bool{}
	for _, c := range canarySet() {
		p := normalizeProfile(c.Profile)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		order = append(order, p)
	}
	return order
}

// normalizeProfile folds a profile name to its canonical form so "p1" and
// " P1 " match the table's ProfileP1.
func normalizeProfile(profile string) string {
	return strings.ToUpper(strings.TrimSpace(profile))
}
