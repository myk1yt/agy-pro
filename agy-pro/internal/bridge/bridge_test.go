package bridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agypro/internal/providers"
)

// testSessionKey authenticates every request the unit tests send to the
// bridge handler. Wrong/missing-key cases assert explicitly below.
const testSessionKey = "unit-test-session-key"

// authDo issues a request carrying the bridge session bearer token.
func authDo(t *testing.T, method, url, contentType, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+testSessionKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStartBridge_Lifecycle(t *testing.T) {
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "test-prov",
				BaseURL: "http://example.com/v1",
				Models: []providers.Model{
					{ID: "test-model-1"},
				},
			},
		},
	}

	bridgeURL, stopFunc, err := StartBridge(reg, testSessionKey, BridgeToolsConfig{})
	if err != nil {
		t.Fatalf("StartBridge failed: %v", err)
	}
	defer stopFunc()

	if !strings.HasPrefix(bridgeURL, "http://127.0.0.1:") {
		t.Fatalf("expected bridgeURL starting with http://127.0.0.1:, got %q", bridgeURL)
	}

	// Verify /health
	resp, err := http.Get(bridgeURL + "/health")
	if err != nil {
		t.Fatalf("failed to query /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /health, got %d", resp.StatusCode)
	}

	// Verify /models requires the session key
	mResp := authDo(t, http.MethodGet, bridgeURL+"/models", "", "")
	defer mResp.Body.Close()
	if mResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /models, got %d", mResp.StatusCode)
	}

	var modelsResult ModelsResponse
	if err := json.NewDecoder(mResp.Body).Decode(&modelsResult); err != nil {
		t.Fatalf("failed to decode /models JSON: %v", err)
	}
	if len(modelsResult.Data) != 1 || modelsResult.Data[0].ID != "test-model-1 (test-prov)" {
		t.Fatalf("unexpected models list: %+v", modelsResult)
	}

	// Stop the bridge
	stopFunc()

	// Wait briefly and verify connection rejected
	time.Sleep(100 * time.Millisecond)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Get(bridgeURL + "/health")
	if err == nil {
		t.Errorf("expected error after stopFunc, but connection succeeded")
	}
}

func TestModelsEndpoint(t *testing.T) {
	trueVal := true
	falseVal := false

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "deepseek",
				BaseURL: "https://api.deepseek.com",
				Models: []providers.Model{
					{ID: "deepseek-chat", Enabled: &trueVal},
					{ID: "deepseek-reasoner", Enabled: &falseVal}, // disabled, should be excluded
				},
			},
			{
				Name:    "openrouter",
				BaseURL: "https://openrouter.ai/api/v1",
				Models: []providers.Model{
					{ID: "deepseek-chat", Enabled: &trueVal}, // duplicate ID, deduplicated
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	server := httptest.NewServer(handler)
	defer server.Close()

	// Test both /models and /v1/models
	for _, endpoint := range []string{"/models", "/v1/models"} {
		resp := authDo(t, http.MethodGet, server.URL+endpoint, "", "")
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d; want 200", endpoint, resp.StatusCode)
		}

		var mResp ModelsResponse
		if err := json.NewDecoder(resp.Body).Decode(&mResp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if mResp.Object != "list" {
			t.Errorf("expected object: 'list', got %q", mResp.Object)
		}

		if len(mResp.Data) != 3 {
			t.Fatalf("expected 3 active models, got %d: %+v", len(mResp.Data), mResp.Data)
		}

		modelIDs := map[string]bool{}
		for _, m := range mResp.Data {
			modelIDs[m.ID] = true
			if m.Object != "model" {
				t.Errorf("expected object 'model' for item %s, got %q", m.ID, m.Object)
			}
		}
		if !modelIDs["deepseek-chat (deepseek)"] || !modelIDs["deepseek-chat (openrouter)"] || !modelIDs["glm-5.3-flash-flex (openrouter)"] {
			t.Errorf("missing expected models: %v", modelIDs)
		}
		if modelIDs["deepseek-reasoner (deepseek)"] {
			t.Errorf("disabled model 'deepseek-reasoner' should not appear in /models")
		}
	}
}

func TestChatCompletions_StandardJSON(t *testing.T) {
	t.Setenv("TEST_KEY_STANDARD", "real-upstream-secret-key-123")

	var receivedAuth string
	var receivedBody map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("upstream got path %q, want /chat/completions", r.URL.Path)
		}
		receivedAuth = r.Header.Get("Authorization")

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test-001",
			"object": "chat.completion",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello from upstream!"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "upstream-prov",
				BaseURL:   upstream.URL,
				APIKeyEnv: "TEST_KEY_STANDARD",
				Models: []providers.Model{
					{ID: "mock-model"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	reqPayload := map[string]any{
		"model": "mock-model",
		"messages": []map[string]string{
			{"role": "user", "content": "Hi"},
		},
	}
	payloadBytes, _ := json.Marshal(reqPayload)

	req, err := http.NewRequest(http.MethodPost, bridgeServer.URL+"/chat/completions", bytes.NewReader(payloadBytes))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testSessionKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /chat/completions failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify upstream received actual bearer key
	if receivedAuth != "Bearer real-upstream-secret-key-123" {
		t.Errorf("upstream received auth %q, want 'Bearer real-upstream-secret-key-123'", receivedAuth)
	}

	// Verify upstream received model name
	if receivedBody["model"] != "mock-model" {
		t.Errorf("upstream received model %v, want 'mock-model'", receivedBody["model"])
	}

	var respJSON map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("failed to decode client response: %v", err)
	}
	if respJSON["id"] != "chatcmpl-test-001" {
		t.Errorf("client received unexpected response id: %v", respJSON["id"])
	}
}

func TestChatCompletions_SSEStreaming(t *testing.T) {
	t.Setenv("TEST_KEY_STREAM", "stream-key-456")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		chunks := []string{
			`data: {"choices":[{"delta":{"content":"Thinking..."}}]}`,
			`data: {"choices":[{"delta":{"content":" Done!"}}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "stream-prov",
				BaseURL:   upstream.URL,
				APIKeyEnv: "TEST_KEY_STREAM",
				Models: []providers.Model{
					{ID: "stream-model"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	reqPayload := `{"model":"stream-model","messages":[{"role":"user","content":"Stream"}]}`
	resp := authDo(t, http.MethodPost, bridgeServer.URL+"/v1/chat/completions", "application/json", reqPayload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}

	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			lines = append(lines, line)
		}
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 data lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != `data: {"choices":[{"delta":{"content":"Thinking..."}}]}` {
		t.Errorf("unexpected first chunk: %s", lines[0])
	}
	if lines[2] != `data: [DONE]` {
		t.Errorf("unexpected third chunk: %s", lines[2])
	}
}

func TestChatCompletions_ReasoningEffortHandling(t *testing.T) {
	var capturedBody map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "thinking-prov",
				BaseURL: upstream.URL,
				Models: []providers.Model{
					{
						ID: "non-thinking-model",
						Capabilities: providers.Capabilities{
							SupportsThinking: false,
						},
					},
					{
						ID: "thinking-model-with-default",
						Capabilities: providers.Capabilities{
							SupportsThinking: true,
							ReasoningEffort:  "default",
						},
					},
					{
						ID: "thinking-model-with-medium",
						Capabilities: providers.Capabilities{
							SupportsThinking: true,
							ReasoningEffort:  "medium",
						},
					},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	sendReq := func(model, effort string) map[string]any {
		capturedBody = nil
		payload := map[string]any{
			"model":    model,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		}
		if effort != "" {
			payload["reasoning_effort"] = effort
		}
		data, _ := json.Marshal(payload)
		resp := authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", string(data))
		_ = resp.Body.Close()
		return capturedBody
	}

	// Case 1: Non-thinking model with client passing reasoning_effort = "high"
	// Upstream MUST NOT receive reasoning_effort (prevents 400 Bad Request)
	res := sendReq("non-thinking-model", "high")
	if _, exists := res["reasoning_effort"]; exists {
		t.Errorf("non-thinking model should have reasoning_effort stripped, got %v", res["reasoning_effort"])
	}

	// Case 2: Thinking model with client passing reasoning_effort = "default"
	// Upstream MUST NOT receive literal "default" (prevents OpenAI 400 Bad Request)
	res = sendReq("thinking-model-with-default", "default")
	if _, exists := res["reasoning_effort"]; exists {
		t.Errorf("thinking model with 'default' effort should have reasoning_effort stripped, got %v", res["reasoning_effort"])
	}

	// Case 3: Thinking model with client passing reasoning_effort = "none"
	// Upstream MUST NOT receive "none"
	res = sendReq("thinking-model-with-default", "none")
	if _, exists := res["reasoning_effort"]; exists {
		t.Errorf("thinking model with 'none' effort should have reasoning_effort stripped, got %v", res["reasoning_effort"])
	}

	// Case 4: Thinking model with client passing "high", "xhigh", "max"
	for _, effortLevel := range []string{"low", "medium", "high", "xhigh", "max"} {
		res = sendReq("thinking-model-with-default", effortLevel)
		if res["reasoning_effort"] != effortLevel {
			t.Errorf("thinking model with effort %q: expected %q, got %v", effortLevel, effortLevel, res["reasoning_effort"])
		}
	}

	// Case 5: Thinking model with no client effort provided, but model preset declares "medium"
	res = sendReq("thinking-model-with-medium", "")
	if res["reasoning_effort"] != "medium" {
		t.Errorf("thinking model with configured medium effort should forward 'medium', got %v", res["reasoning_effort"])
	}
}

func TestChatCompletions_ErrorHandling(t *testing.T) {
	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:    "broken-prov",
				BaseURL: "http://127.0.0.1:59999", // unroutable / offline port
				Models: []providers.Model{
					{ID: "offline-model"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	// 1. Missing model field -> 400
	resp := authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model, got %d", resp.StatusCode)
	}

	// 2. Unknown model -> 404
	resp = authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", `{"model":"unknown-model-xyz"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown model, got %d", resp.StatusCode)
	}

	// 3. Offline provider -> 502 Bad Gateway
	resp = authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", `{"model":"offline-model"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for offline provider, got %d", resp.StatusCode)
	}
}

func TestChatCompletions_ModelWithProviderSuffix(t *testing.T) {
	t.Setenv("TEST_NW_KEY", "real-nw-secret-token-789")

	var receivedModel string
	var receivedAuth string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			receivedModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"success from NW"}}]}`))
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   upstream.URL + "/v1",
				APIKeyEnv: "TEST_NW_KEY",
				Models: []providers.Model{
					{ID: "glm-5.3-flash-flex"},
				},
			},
			{
				Name:      "Charm",
				BaseURL:   "http://127.0.0.1:59998",
				APIKeyEnv: "TEST_CHARM_KEY",
				Models: []providers.Model{
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	// 1. Client calls with "model": "glm-5.3-flash-flex (NW)"
	reqBody := `{"model":"glm-5.3-flash-flex (NW)","messages":[{"role":"user","content":"hello"}]}`
	resp := authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", reqBody)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(b))
	}

	// Verify upstream received the original model ID without " (NW)"
	if receivedModel != "glm-5.3-flash-flex" {
		t.Errorf("upstream received model %q; want 'glm-5.3-flash-flex' (suffix stripped)", receivedModel)
	}

	// Verify upstream received the correct authorization header
	if receivedAuth != "Bearer real-nw-secret-token-789" {
		t.Errorf("upstream received auth %q; want 'Bearer real-nw-secret-token-789'", receivedAuth)
	}

	// 2. Client calls with legacy raw model "glm-5.3-flash-flex" (fallback matching)
	receivedModel = ""
	legacyReqBody := `{"model":"glm-5.3-flash-flex","messages":[{"role":"user","content":"hello"}]}`
	respLegacy := authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", legacyReqBody)
	defer respLegacy.Body.Close()

	if respLegacy.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respLegacy.Body)
		t.Fatalf("expected 200 OK for legacy model, got %d: %s", respLegacy.StatusCode, string(b))
	}

	if receivedModel != "glm-5.3-flash-flex" {
		t.Errorf("legacy upstream received model %q; want 'glm-5.3-flash-flex'", receivedModel)
	}
}

func TestChatCompletions_MissingAPIKeyError(t *testing.T) {
	// Ensure the key env is unset
	t.Setenv("UNSET_NW_KEY_FOR_TEST", "")

	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   upstream.URL,
				APIKeyEnv: "UNSET_NW_KEY_FOR_TEST",
				Models: []providers.Model{
					{ID: "glm-5.3-flash-flex"},
				},
			},
		},
	}

	handler := NewHandler(reg, testSessionKey, BridgeToolsConfig{})
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	reqBody := `{"model":"glm-5.3-flash-flex (NW)","messages":[{"role":"user","content":"test"}]}`
	resp := authDo(t, http.MethodPost, bridgeServer.URL+"/chat/completions", "application/json", reqBody)
	defer resp.Body.Close()

	// Must return HTTP 401 Unauthorized
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}

	// Upstream server must NEVER be contacted
	if upstreamCalled {
		t.Errorf("upstream server was contacted even though API key was unset")
	}

	var errResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error JSON: %v", err)
	}

	errObj, ok := errResp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'error' object in response, got %+v", errResp)
	}

	expectedMsg := "API key for provider NW (UNSET_NW_KEY_FOR_TEST) is not configured or empty in environment"
	if errObj["message"] != expectedMsg {
		t.Errorf("error.message = %q; want %q", errObj["message"], expectedMsg)
	}
	if errObj["type"] != "authentication_error" {
		t.Errorf("error.type = %q; want 'authentication_error'", errObj["type"])
	}
	if errObj["code"] != float64(http.StatusUnauthorized) {
		t.Errorf("error.code = %v; want 401", errObj["code"])
	}
}

func TestParseModelSpec(t *testing.T) {
	tests := []struct {
		input    string
		wantBase string
		wantProv string
	}{
		{
			input:    "glm-5.3-flash-flex (NW)",
			wantBase: "glm-5.3-flash-flex",
			wantProv: "NW",
		},
		{
			input:    "deepseek-ai/DeepSeek-V3 (SiliconFlow)",
			wantBase: "deepseek-ai/DeepSeek-V3",
			wantProv: "SiliconFlow",
		},
		{
			input:    "NW/glm-5.3-flash-flex",
			wantBase: "glm-5.3-flash-flex",
			wantProv: "NW",
		},
		{
			input:    "glm-5.3-flash-flex",
			wantBase: "glm-5.3-flash-flex",
			wantProv: "",
		},
		{
			input:    "  model-name   ( My Provider )  ",
			wantBase: "model-name",
			wantProv: "My Provider",
		},
		{
			input:    "",
			wantBase: "",
			wantProv: "",
		},
	}

	for _, tt := range tests {
		base, prov := providers.ParseModelSpec(tt.input)
		if base != tt.wantBase || prov != tt.wantProv {
			t.Errorf("ParseModelSpec(%q) = (%q, %q); want (%q, %q)",
				tt.input, base, prov, tt.wantBase, tt.wantProv)
		}
	}
}
