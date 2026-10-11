// Package bridge provides a lightweight local HTTP reverse-proxy server
// that acts as an OpenAI-compatible gateway for Antigravity CLI (agy).
// It exposes all external models configured in providers.json to native agy
// through its /models endpoint, and transparently routes /chat/completions
// requests (both SSE streaming and standard JSON) to the appropriate provider.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agypro/internal/providers"
)

// ValidReasoningEfforts enumerates the supported reasoning effort levels.
var ValidReasoningEfforts = map[string]bool{
	"default": true,
	"none":    true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

// NormalizeReasoningEffort sanitizes reasoning effort strings, defaulting to "default".
func NormalizeReasoningEffort(effort string) string {
	cleaned := strings.ToLower(strings.TrimSpace(effort))
	if cleaned == "" {
		return "default"
	}
	if ValidReasoningEfforts[cleaned] {
		return cleaned
	}
	return "default"
}

// ModelEntry represents a single model in OpenAI's /models response format.
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// ModelsResponse is the root object returned by /models.
type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// StartBridge starts an in-memory HTTP bridge server listening on 127.0.0.1 on an OS-assigned port.
// It returns the base URL (e.g. "http://127.0.0.1:54321"), a stopFunc to cleanly shut down, and any error.
func StartBridge(reg *providers.Registry) (bridgeURL string, stopFunc func(), err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("failed to bind local bridge listener: %w", err)
	}

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return "", nil, fmt.Errorf("failed to extract TCP port from listener address: %v", listener.Addr())
	}

	bridgeURL = fmt.Sprintf("http://127.0.0.1:%d", tcpAddr.Port)

	handler := NewHandler(reg)
	server := &http.Server{
		Handler: handler,
	}

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			// Server shut down or aborted
		}
	}()

	var stopOnce sync.Once
	stopFunc = func() {
		stopOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
			_ = listener.Close()
		})
	}

	return bridgeURL, stopFunc, nil
}

// NewHandler constructs an http.Handler that routes /models and /chat/completions.
func NewHandler(reg *providers.Registry) http.Handler {
	mux := http.NewServeMux()

	modelsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleModels(w, r, reg)
	})
	chatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleChatCompletions(w, r, reg)
	})

	// Handle standard OpenAI routes with and without /v1 prefix
	mux.Handle("/models", modelsHandler)
	mux.Handle("/v1/models", modelsHandler)
	mux.Handle("/models/", modelsHandler)
	mux.Handle("/v1/models/", modelsHandler)

	mux.Handle("/chat/completions", chatHandler)
	mux.Handle("/v1/chat/completions", chatHandler)

	// Health and root diagnostics
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"agy-pro-bridge"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","service":"agy-pro-bridge"}`))
			return
		}
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("path %q not found on bridge", r.URL.Path), "not_found")
	})

	return mux
}

// handleModels returns the list of all configured models in OpenAI format.
func handleModels(w http.ResponseWriter, r *http.Request, reg *providers.Registry) {
	if r.Method == http.MethodOptions {
		setCorsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}

	setCorsHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	var entries []ModelEntry
	seen := make(map[string]bool)

	if reg != nil {
		for _, p := range reg.Providers {
			for _, m := range p.Models {
				if m.IsEnabled() && !seen[m.ID] {
					seen[m.ID] = true
					entries = append(entries, ModelEntry{
						ID:      m.ID,
						Object:  "model",
						Created: time.Now().Unix(),
						OwnedBy: p.Name,
					})
				}
			}
		}
	}

	resp := ModelsResponse{
		Object: "list",
		Data:   entries,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// handleChatCompletions routes the completion request to the corresponding provider.
func handleChatCompletions(w http.ResponseWriter, r *http.Request, reg *providers.Registry) {
	if r.Method == http.MethodOptions {
		setCorsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed: POST required", "method_not_allowed")
		return
	}

	setCorsHeaders(w)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}

	var rawReq map[string]any
	if err := json.Unmarshal(bodyBytes, &rawReq); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err), "invalid_request_error")
		return
	}

	modelVal, ok := rawReq["model"].(string)
	if !ok || strings.TrimSpace(modelVal) == "" {
		writeJSONError(w, http.StatusBadRequest, "request body must include non-empty 'model' field", "invalid_request_error")
		return
	}
	modelReq := strings.TrimSpace(modelVal)

	target, err := findBestTarget(reg, modelReq)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("model %q not found in configured providers", modelReq), "model_not_found")
		return
	}

	// 1. Process and normalize reasoning effort
	// Options: default, none, low, medium, high, xhigh, max. Default is "default".
	var effort string
	if rawEffort, exists := rawReq["reasoning_effort"]; exists {
		if s, isStr := rawEffort.(string); isStr {
			effort = NormalizeReasoningEffort(s)
		} else {
			effort = "default"
		}
	} else if target.Model.Capabilities.SupportsThinking {
		configuredEffort := target.Model.Capabilities.ReasoningEffort
		if configuredEffort != "" {
			effort = NormalizeReasoningEffort(configuredEffort)
		} else {
			effort = "default"
		}
	} else {
		effort = "default"
	}

	// Adjust request body for upstream compatibility:
	// - If the model does not support thinking, remove reasoning_effort completely
	//   so strict providers (OpenAI, DeepSeek, GLM) do not reject with 400 Bad Request.
	// - If effort is "default" or "none", remove reasoning_effort from payload
	//   so the upstream server applies its native behavior without rejecting unknown "default"/"none" values.
	// - If effort is "low", "medium", "high", "xhigh", "max" and thinking is supported, retain/set it.
	if !target.Model.Capabilities.SupportsThinking || effort == "default" || effort == "none" {
		delete(rawReq, "reasoning_effort")
	} else {
		rawReq["reasoning_effort"] = effort
	}

	// Upstream expects the exact model ID declared by the provider
	rawReq["model"] = target.Model.ID

	forwardBody, err := json.Marshal(rawReq)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to serialize request: %v", err), "internal_error")
		return
	}

	targetEndpoint := target.Provider.Endpoint()
	parsedTarget, err := url.Parse(targetEndpoint)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("invalid provider baseURL: %v", err), "internal_error")
		return
	}

	apiKey := target.Provider.KeyValue()

	proxy := &httputil.ReverseProxy{
		Director: func(outReq *http.Request) {
			outReq.URL.Scheme = parsedTarget.Scheme
			outReq.URL.Host = parsedTarget.Host
			outReq.URL.Path = parsedTarget.Path
			outReq.URL.RawQuery = parsedTarget.RawQuery
			outReq.Host = parsedTarget.Host

			if apiKey != "" {
				outReq.Header.Set("Authorization", "Bearer "+apiKey)
			} else {
				outReq.Header.Del("Authorization")
			}

			outReq.Header.Set("Content-Type", "application/json")
			outReq.Body = io.NopCloser(bytes.NewReader(forwardBody))
			outReq.ContentLength = int64(len(forwardBody))
			outReq.Header.Set("Content-Length", strconv.Itoa(len(forwardBody)))
		},
		// FlushInterval = -1 guarantees immediate flushing of each SSE chunk to agy
		FlushInterval: -1,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("bridge error contacting provider %q: %v", target.Provider.Name, proxyErr),
					"type":    "bad_gateway",
					"code":    http.StatusBadGateway,
				},
			})
		},
	}

	proxy.ServeHTTP(w, r)
}

// findBestTarget looks up a model target in the registry, preferring providers with valid API keys.
func findBestTarget(reg *providers.Registry, modelSpec string) (providers.Target, error) {
	if reg == nil {
		return providers.Target{}, errors.New("registry is empty or not configured")
	}

	target, err := reg.FindTarget(modelSpec)
	if err == nil {
		if target.Provider.KeyAvailable() {
			return target, nil
		}
		// Try alternate providers offering the same model with active API keys
		alts := reg.FindAlternatesForModel(target.Model.ID, target.Provider.Name)
		for _, alt := range alts {
			if alt.Provider.KeyAvailable() {
				return alt, nil
			}
		}
		return target, nil
	}

	// Fallback case-insensitive search across all providers
	spec := strings.TrimSpace(modelSpec)
	var provFilter, modelFilter string
	if parts := strings.SplitN(spec, "/", 2); len(parts) == 2 {
		provFilter = parts[0]
		modelFilter = parts[1]
	} else {
		modelFilter = spec
	}

	for _, p := range reg.Providers {
		if provFilter != "" && !strings.EqualFold(p.Name, provFilter) {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, modelFilter) {
				return providers.Target{Provider: p, Model: m}, nil
			}
		}
	}

	return providers.Target{}, fmt.Errorf("model %q not found in registry", modelSpec)
}

func setCorsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Requested-With")
}

func writeJSONError(w http.ResponseWriter, statusCode int, message, errType string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    statusCode,
		},
	})
}
