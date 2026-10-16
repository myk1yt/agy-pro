package wireadp

import (
	"net/http"
	"strings"
	"testing"
)

func TestStrictAuth_AllParityFormsAccepted(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.AuthKey = "install-nonce-42"
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"

	cases := []struct {
		name    string
		headers map[string]string
		url     string
	}{
		{"primary X-Goog-Api-Key", map[string]string{headerAPIKey: "install-nonce-42"}, url},
		{"x-api-key parity", map[string]string{headerAPIKeyAlt: "install-nonce-42"}, url},
		{"query key parity", nil, url + "?key=install-nonce-42"},
		{"Authorization Bearer parity", map[string]string{"Authorization": "Bearer install-nonce-42"}, url},
		{"lowercase bearer prefix", map[string]string{"Authorization": "bearer install-nonce-42"}, url},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := post(t, tc.url, tc.headers, chatBody())
			// Auth passed through to the generation path, which completed a
			// real upstream turn.
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d body %s (auth must pass through to the handler)", resp.StatusCode, body)
			}
			if !strings.Contains(body, "finishReason") {
				t.Fatalf("no completed stream: %s", body)
			}
		})
	}
}

func TestStrictAuth_RejectionsNeverEchoTheKey(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "install-nonce-42"
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"

	rejections := []struct {
		name    string
		headers map[string]string
		url     string
	}{
		{"missing", nil, url},
		{"mismatch", map[string]string{headerAPIKey: "wrong-nonce-value"}, url},
		{"malformed bearer", map[string]string{"Authorization": "Bearer"}, url},
		{"bare token without scheme", map[string]string{"Authorization": "install-nonce-42"}, url},
		{"wrong query key", nil, url + "?key=wrong-nonce-value"},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := post(t, tc.url, tc.headers, chatBody())
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d body %s", resp.StatusCode, body)
			}
			env := decodeError(t, body)
			if env.Error.Code != http.StatusUnauthorized || env.Error.Status != statusUnauthenticated {
				t.Fatalf("envelope = %+v", env.Error)
			}
			if env.Error.Message != msgAuthFailed {
				t.Fatalf("message = %q, want the fixed string", env.Error.Message)
			}
			// The fixed message must never leak the presented value.
			for _, secret := range []string{"wrong-nonce-value", "install-nonce-42"} {
				if strings.Contains(body, secret) {
					t.Fatalf("401 body leaks %q: %s", secret, body)
				}
			}
		})
	}
}

func TestStrictAuth_GuardsTheRPCAndEnvelopeSurfaces(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "install-nonce-42"
	srv := startServer(t, cfg)

	for _, path := range []string{
		"/v1internal:loadCodeAssist",
		"/v1internal:streamGenerateContent?alt=sse",
		"/v1beta/models",
	} {
		resp, body := post(t, srv.URL()+path, nil, []byte(`{}`))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s status %d body %s", path, resp.StatusCode, body)
		}
		if env := decodeError(t, body); env.Error.Status != statusUnauthenticated {
			t.Fatalf("%s envelope = %+v", path, env.Error)
		}
		resp, body = post(t, srv.URL()+path, map[string]string{headerAPIKey: "install-nonce-42"}, []byte(`{}`))
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("%s rejected a valid key: %s", path, body)
		}
	}
}

func TestStrictAuth_HealthzNonce(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = "install-nonce-42"
	srv := startServer(t, cfg)

	resp, body := get(t, srv.URL()+"/healthz", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing nonce status %d body %s", resp.StatusCode, body)
	}
	if env := decodeError(t, body); env.Error.Status != statusUnauthenticated {
		t.Fatalf("envelope = %+v", env.Error)
	}

	resp, _ = get(t, srv.URL()+"/healthz", map[string]string{headerInstallNonce: "wrong-nonce"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mismatched nonce status %d", resp.StatusCode)
	}

	resp, body = get(t, srv.URL()+"/healthz", map[string]string{headerInstallNonce: "install-nonce-42"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching nonce status %d body %s", resp.StatusCode, body)
	}
	if strings.TrimSpace(body) != `{"status":"ok","service":"agy-pro-wireadp"}` {
		t.Fatalf("healthz body = %s", body)
	}
}

func TestPermissiveMode_NoKeyRequired(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	resp, body := post(t, srv.URL()+"/v1beta/models/probe-gemini:streamGenerateContent", nil, chatBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "finishReason") {
		t.Fatalf("no completed stream: %s", body)
	}
}

func TestKeyMatches_FailsClosedOnEmptyValues(t *testing.T) {
	// An empty configured key rejects everything (§10.2 fail closed).
	if keyMatches("", "") {
		t.Fatal("empty/empty must not match")
	}
	if keyMatches("presented", "") {
		t.Fatal("an empty expected key must reject everything")
	}
	if keyMatches("", "expected") {
		t.Fatal("an empty presented key must be rejected")
	}
	if !keyMatches("same", "same") {
		t.Fatal("identical values must match")
	}
	if keyMatches("same", "other") {
		t.Fatal("different values must not match")
	}
}

func TestPresentedKey_ExtractionOrder(t *testing.T) {
	req := mustRequest(t, http.MethodPost, "http://127.0.0.1:1/v1beta/models/x:generateContent?key=query-key", nil)
	if got := presentedKey(req); got != "query-key" {
		t.Fatalf("query key = %q", got)
	}
	req.Header.Set(headerAPIKeyAlt, "alt-key")
	if got := presentedKey(req); got != "alt-key" {
		t.Fatalf("x-api-key must outrank ?key=, got %q", got)
	}
	req.Header.Set(headerAPIKey, "primary-key")
	if got := presentedKey(req); got != "primary-key" {
		t.Fatalf("X-Goog-Api-Key must win, got %q", got)
	}

	// With no query key, the bearer form is the last fallback.
	req = mustRequest(t, http.MethodPost, "http://127.0.0.1:1/v1beta/models/x:generateContent", nil)
	req.Header.Set("Authorization", "Bearer bearer-key")
	if got := presentedKey(req); got != "bearer-key" {
		t.Fatalf("bearer fallback = %q", got)
	}
	req.Header.Set(headerAPIKeyAlt, "alt-key")
	if got := presentedKey(req); got != "alt-key" {
		t.Fatalf("x-api-key must outrank the bearer form, got %q", got)
	}

	// Nothing presented at all.
	if got := presentedKey(mustRequest(t, http.MethodPost, "http://127.0.0.1:1/x", nil)); got != "" {
		t.Fatalf("no credential = %q", got)
	}
}

func TestPermissiveRateLimit_BurstThen429(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"

	const attempts = 100
	var passed, limited int
	for i := 0; i < attempts; i++ {
		resp, body := post(t, url, nil, chatBody())
		switch resp.StatusCode {
		case http.StatusOK:
			passed++
		case http.StatusTooManyRequests:
			limited++
			env := decodeError(t, body)
			if env.Error.Status != statusResourceExhausted {
				t.Fatalf("429 envelope = %+v", env.Error)
			}
		default:
			t.Fatalf("attempt %d: status %d body %s", i, resp.StatusCode, body)
		}
	}
	// The bucket holds 60 tokens and refills 1/s; a fast burst must therefore
	// be cut off well before the 100th request. The margin absorbs a slow
	// machine refilling a few tokens mid-burst.
	if limited == 0 {
		t.Fatal("the burst was never limited")
	}
	if passed > rateLimitPerMinute+5 {
		t.Fatalf("%d requests passed, want at most %d", passed, rateLimitPerMinute+5)
	}
	if limited < 30 {
		t.Fatalf("only %d of %d requests were limited", limited, attempts)
	}
}

func TestPermissiveRateLimit_IsPerInstallNotPerSession(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"
	for i := 0; i < rateLimitPerMinute; i++ {
		if resp, _ := post(t, url, nil, chatBody()); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i, resp.StatusCode)
		}
	}
	// A "different session" (different User-Agent, same install) shares the
	// exhausted bucket: at most the 1/s refill sneaks through.
	limited := false
	for i := 0; i < 5 && !limited; i++ {
		resp, body := post(t, url, map[string]string{"User-Agent": "other-session/1.0"}, chatBody())
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d body %s", resp.StatusCode, body)
		}
	}
	if !limited {
		t.Fatal("a second session on the same install must share the exhausted bucket")
	}
}

func TestHealthzIsExemptFromRateLimit(t *testing.T) {
	cfg := testConfig(t)
	srv := startServer(t, cfg)
	url := srv.URL() + "/v1beta/models/probe-gemini:streamGenerateContent"
	for i := 0; i < rateLimitPerMinute+5; i++ {
		_, _ = post(t, url, nil, chatBody())
	}
	for i := 0; i < 5; i++ {
		resp, body := get(t, srv.URL()+"/healthz", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz %d after the bucket emptied: %d %s", i, resp.StatusCode, body)
		}
	}
}
