package bridge

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agypro/internal/providers"
)

func authTestRegistry() *providers.Registry {
	return &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "prov",
				BaseURL: "http://127.0.0.1:1",
				Models: []providers.Model{
					{ID: "m1"},
				},
			},
		},
	}
}

func TestGenerateSessionKey(t *testing.T) {
	k1, err := GenerateSessionKey()
	if err != nil {
		t.Fatalf("GenerateSessionKey: %v", err)
	}
	k2, err := GenerateSessionKey()
	if err != nil {
		t.Fatalf("GenerateSessionKey: %v", err)
	}
	if len(k1) != 64 {
		t.Errorf("session key length = %d; want 64 hex chars", len(k1))
	}
	if _, err := hex.DecodeString(k1); err != nil {
		t.Errorf("session key is not hex: %v", err)
	}
	if k1 == k2 {
		t.Error("two generated session keys must differ")
	}
}

func TestStartBridge_RequiresSessionKey(t *testing.T) {
	if _, _, err := StartBridge(authTestRegistry(), "", BridgeToolsConfig{}); err == nil {
		t.Error("StartBridge with empty session key must fail")
	}
}

func TestBearerAuth_Models(t *testing.T) {
	server := httptest.NewServer(NewHandler(authTestRegistry(), testSessionKey, BridgeToolsConfig{}))
	defer server.Close()

	cases := []struct {
		name   string
		auth   string
		status int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
		{"wrong key", "Bearer not-the-key", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + testSessionKey, http.StatusUnauthorized},
		{"garbage header", "nonsense", http.StatusUnauthorized},
		{"correct key", "Bearer " + testSessionKey, http.StatusOK},
		{"correct key case-insensitive scheme", "bearer " + testSessionKey, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, server.URL+"/models", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d; want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusUnauthorized {
				var errBody map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
					t.Fatalf("error body is not JSON: %v", err)
				}
				errObj, ok := errBody["error"].(map[string]any)
				if !ok {
					t.Fatalf("expected error object, got %v", errBody)
				}
				if errObj["type"] != "authentication_error" {
					t.Errorf("error.type = %v; want authentication_error", errObj["type"])
				}
			}
		})
	}
}

func TestBearerAuth_ChatCompletions(t *testing.T) {
	server := httptest.NewServer(NewHandler(authTestRegistry(), testSessionKey, BridgeToolsConfig{}))
	defer server.Close()
	body := `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`

	// 1. Missing key: 401 before any routing or upstream work.
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing key status = %d; want 401", resp.StatusCode)
	}

	// 2. Wrong key: 401.
	req2, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong key status = %d; want 401", resp2.StatusCode)
	}

	// 3. Correct key passes the auth layer (here: model-not-found 404, which
	// is the routing layer, not 401).
	req3, _ := http.NewRequest(http.MethodPost, server.URL+"/chat/completions", strings.NewReader(`{"model":"nope","messages":[]}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Authorization", "Bearer "+testSessionKey)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode == http.StatusUnauthorized {
		t.Error("correct key must not be rejected with 401")
	}
}

func TestBearerAuth_HealthStaysOpen(t *testing.T) {
	server := httptest.NewServer(NewHandler(authTestRegistry(), testSessionKey, BridgeToolsConfig{}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health status = %d; want 200 (diagnostics route)", resp.StatusCode)
	}
}

func TestBearerAuth_OptionsPreflight(t *testing.T) {
	server := httptest.NewServer(NewHandler(authTestRegistry(), testSessionKey, BridgeToolsConfig{}))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodOptions, server.URL+"/models", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS /models status = %d; want 204 (preflight needs no auth)", resp.StatusCode)
	}
}

func TestAuthorizeEmptyExpectedKeyFailsClosed(t *testing.T) {
	// A handler constructed with an empty expected key must reject everything.
	server := httptest.NewServer(NewHandler(authTestRegistry(), "", BridgeToolsConfig{}))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/models", nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty expected key status = %d; want 401 (fail closed)", resp.StatusCode)
	}
}
