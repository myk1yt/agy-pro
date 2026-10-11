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

	bridgeURL, stopFunc, err := StartBridge(reg)
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

	// Verify /models
	mResp, err := http.Get(bridgeURL + "/models")
	if err != nil {
		t.Fatalf("failed to query /models: %v", err)
	}
	defer mResp.Body.Close()
	if mResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /models, got %d", mResp.StatusCode)
	}

	var modelsResult ModelsResponse
	if err := json.NewDecoder(mResp.Body).Decode(&modelsResult); err != nil {
		t.Fatalf("failed to decode /models JSON: %v", err)
	}
	if len(modelsResult.Data) != 1 || modelsResult.Data[0].ID != "test-model-1" {
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

	handler := NewHandler(reg)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Test both /models and /v1/models
	for _, endpoint := range []string{"/models", "/v1/models"} {
		resp, err := http.Get(server.URL + endpoint)
		if err != nil {
			t.Fatalf("GET %s failed: %v", endpoint, err)
		}
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

		if len(mResp.Data) != 2 {
			t.Fatalf("expected 2 active models (deepseek-chat, glm-5.3-flash-flex), got %d: %+v", len(mResp.Data), mResp.Data)
		}

		modelIDs := map[string]bool{}
		for _, m := range mResp.Data {
			modelIDs[m.ID] = true
			if m.Object != "model" {
				t.Errorf("expected object 'model' for item %s, got %q", m.ID, m.Object)
			}
		}
		if !modelIDs["deepseek-chat"] || !modelIDs["glm-5.3-flash-flex"] {
			t.Errorf("missing expected models: %v", modelIDs)
		}
		if modelIDs["deepseek-reasoner"] {
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

	handler := NewHandler(reg)
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
	req.Header.Set("Authorization", "Bearer agy-pro-local-key")
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

	handler := NewHandler(reg)
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	reqPayload := `{"model":"stream-model","messages":[{"role":"user","content":"Stream"}]}`
	resp, err := http.Post(bridgeServer.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqPayload))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
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

	handler := NewHandler(reg)
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	client := &http.Client{}

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
		resp, err := client.Post(bridgeServer.URL+"/chat/completions", "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
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

	handler := NewHandler(reg)
	bridgeServer := httptest.NewServer(handler)
	defer bridgeServer.Close()

	// 1. Missing model field -> 400
	resp, err := http.Post(bridgeServer.URL+"/chat/completions", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing model, got %d", resp.StatusCode)
	}

	// 2. Unknown model -> 404
	resp, err = http.Post(bridgeServer.URL+"/chat/completions", "application/json", strings.NewReader(`{"model":"unknown-model-xyz"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown model, got %d", resp.StatusCode)
	}

	// 3. Offline provider -> 502 Bad Gateway
	resp, err = http.Post(bridgeServer.URL+"/chat/completions", "application/json", strings.NewReader(`{"model":"offline-model"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway for offline provider, got %d", resp.StatusCode)
	}
}
