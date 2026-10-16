// Package bridge provides a lightweight local HTTP reverse-proxy server
// that acts as an OpenAI-compatible gateway for Antigravity CLI (agy).
// It exposes all external models configured in providers.json to native agy
// through its /models endpoint, and transparently routes /chat/completions
// requests (both SSE streaming and standard JSON) to the appropriate provider.
//
// Scope (design V8 / NATIVE_COEXISTENCE_DESIGN §2 Complements): this gateway
// path stays XOR and tool-less on agy 1.3.5. XOR means a session is EITHER
// native Gemini OR this gateway, never both, and it is an escape hatch only,
// NOT the native-coexistence path (that is the /v1internal mesh in the hybrid
// profiles). Tool-less means agy's gateway client never sends `tools` and
// discards response `tool_calls`; the bridge-side tool loop in bridge_tools.go
// is what re-attaches tools for Gemini-parity actions, and the M4 hardening
// work only makes that loop more robust (incremental streaming, cross-turn
// usage, an outbound body cap). None of it widens the gateway into the
// coexistence surface.
package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// GenerateSessionKey returns a fresh local bridge session key: 32 random
// bytes rendered as hex (64 chars). The same value is the server-side
// expectation and the AGY_LLM_GATEWAY_API_KEY handed to the child agy, so
// only this process's child can talk to the bridge.
func GenerateSessionKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate bridge session key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// authorize validates the caller's bridge session key from the
// Authorization header ("Bearer <key>"). A missing, malformed, or
// mismatching key is rejected with 401. An empty expected key rejects
// everything (fail closed).
func authorize(w http.ResponseWriter, r *http.Request, sessionKey string) bool {
	if sessionKey == "" {
		writeJSONError(w, http.StatusUnauthorized, "bridge session key is not configured", "authentication_error")
		return false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		got := []byte(strings.TrimSpace(auth[len(prefix):]))
		want := []byte(sessionKey)
		if subtle.ConstantTimeCompare(got, want) == 1 {
			return true
		}
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid or missing bridge session key", "authentication_error")
	return false
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

// BridgeToolsConfig configures the bridge-side tool execution loop (plan R1).
// Enabled is derived by the config layer as Tokens != "none"; Tools holds the
// already-normalized bridge-tools tokens (read, search, write, exec). An empty
// or unrecognized allow-list keeps the loop off (fail closed).
type BridgeToolsConfig struct {
	Enabled bool
	Tools   []string
}

// StartBridge starts an in-memory HTTP bridge server listening on 127.0.0.1 on an OS-assigned port.
// It returns the base URL (e.g. "http://127.0.0.1:54321"), a stopFunc to cleanly shut down, and any error.
// sessionKey authenticates callers (see authorize); it must be the same
// value injected as AGY_LLM_GATEWAY_API_KEY for the child process.
// toolsCfg enables the bridge-side tool loop; invalid tokens fail closed with
// an error instead of silently widening the allow-list.
func StartBridge(reg *providers.Registry, sessionKey string, toolsCfg BridgeToolsConfig) (bridgeURL string, stopFunc func(), err error) {
	if sessionKey == "" {
		return "", nil, errors.New("bridge session key must not be empty")
	}
	if toolsCfg.Enabled {
		if _, toolsErr := resolveBridgeTools(toolsCfg.Tools); toolsErr != nil {
			return "", nil, fmt.Errorf("invalid bridge tools config: %w", toolsErr)
		}
	}
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

	handler := NewHandler(reg, sessionKey, toolsCfg)
	// M6 hardening: slowloris and keep-alive hygiene. WriteTimeout is
	// deliberately left unset - an SSE response stream must be free to outlive
	// any fixed deadline. CORS and /health behavior is unchanged (agy client
	// compatibility).
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
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
// Both data routes require the Authorization bearer sessionKey; /health stays
// open for diagnostics. OPTIONS preflights are answered without auth.
// toolsCfg configures the bridge-side tool loop (see BridgeToolsConfig); the
// zero value keeps the reverse-proxy path, which forwards the request body
// verbatim (inbound headers are scrubbed; see handleChatCompletions).
func NewHandler(reg *providers.Registry, sessionKey string, toolsCfg BridgeToolsConfig) http.Handler {
	mux := http.NewServeMux()

	// Workspace root for tool execution: own-process AGY_PRO_WORKSPACE wins,
	// else the shim cwd captured at handler construction (StartBridge time).
	workspaceRoot := resolveBridgeWorkspaceRoot()

	modelsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions && !authorize(w, r, sessionKey) {
			return
		}
		handleModels(w, r, reg)
	})
	chatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions && !authorize(w, r, sessionKey) {
			return
		}
		handleChatCompletions(w, r, reg, toolsCfg, workspaceRoot)
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
		// Gemini wire arrivals (only possible when the shim's openai wire
		// injection is bypassed) get an explicit logged 404 instead of a silent
		// catch-all (R1-10).
		if isGeminiWirePath(r.URL.Path) {
			log.Printf("[bridge] rejected gemini-wire request %s %s: bridge serves the OpenAI chat wire only (set AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai)", r.Method, r.URL.Path)
			writeJSONError(w, http.StatusNotFound,
				fmt.Sprintf("gemini wire path %q is not supported by the agy-pro bridge (use /v1/chat/completions with AGY_LLM_GATEWAY_WIRE_PROTOCOL=openai)", r.URL.Path),
				"not_found")
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
				if m.IsEnabled() {
					displayName := providers.DisplayName(m.ID, p.Name)
					if !seen[displayName] {
						seen[displayName] = true
						entries = append(entries, ModelEntry{
							ID:      displayName,
							Object:  "model",
							Created: time.Now().Unix(),
							OwnedBy: p.Name,
						})
					}
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
// When the bridge-side tool loop engages (toolsCfg active, no `tools` key in the
// request, tools-capable model, non-empty allow-list) the loop serves the
// request itself; otherwise the request takes the reverse-proxy path below,
// which forwards the request body verbatim while scrubbing inbound headers and
// capping the outbound body (plan 8b R1-7; M4 items 4/5).
func handleChatCompletions(w http.ResponseWriter, r *http.Request, reg *providers.Registry, toolsCfg BridgeToolsConfig, workspaceRoot string) {
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

	// M6: cap the request body before reading it (see openaiup.MaxBodyBytes).
	r.Body = http.MaxBytesReader(w, r.Body, openaiup.MaxBodyBytes)
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

	apiKey := strings.TrimSpace(target.Provider.KeyValue())
	if target.Provider.APIKeyEnv != "" && apiKey == "" {
		writeJSONError(w, http.StatusUnauthorized,
			fmt.Sprintf("API key for provider %s (%s) is not configured or empty in environment", target.Provider.Name, target.Provider.APIKeyEnv),
			"authentication_error")
		return
	}

	// 1. Process and normalize reasoning effort
	// Options: default, none, low, medium, high, xhigh, max. Default is "default".
	var effort string
	if rawEffort, exists := rawReq["reasoning_effort"]; exists {
		if s, isStr := rawEffort.(string); isStr {
			effort = openaiup.NormalizeReasoningEffort(s)
		} else {
			effort = "default"
		}
	} else if target.Model.Capabilities.SupportsThinking {
		configuredEffort := target.Model.Capabilities.ReasoningEffort
		if configuredEffort != "" {
			effort = openaiup.NormalizeReasoningEffort(configuredEffort)
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
	openaiup.ApplyEffort(rawReq, target.Model.Capabilities, effort)

	// Bridge-side tool execution loop seam (plan 8b R1): agy never sends
	// `tools` and discards response `tool_calls`, so external providers only
	// reach Gemini-parity tool actions when the bridge attaches tools, runs the
	// calls, re-feeds results, and synthesizes a text-only reply. Conditions:
	// loop config active + request has no `tools` key + model SupportsTools +
	// non-empty bridge-tools allow-list. Any miss falls through to the
	// reverse-proxy path below (verbatim request body; inbound headers scrubbed).
	if loopTools := maybeToolLoopTools(toolsCfg, rawReq, target); loopTools != nil {
		serveToolLoop(w, r, loopEnv{
			rawReq:   rawReq,
			target:   target,
			modelReq: modelReq,
			tools:    loopTools,
			root:     workspaceRoot,
			redact:   reg.Redactor(),
		})
		return
	}

	// Upstream expects the exact model ID declared by the provider (without "(provider)" suffix)
	rawReq["model"] = target.Model.ID

	forwardBody, err := json.Marshal(rawReq)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to serialize request: %v", err), "internal_error")
		return
	}

	// M4 item 4: cap the outbound body before it reaches the provider, the
	// outbound counterpart to the inbound MaxBytesReader cap applied above. A
	// body past the cap fails loudly with 413; it is never silently truncated.
	// This is a backstop, not dead code: json.Marshal HTML-escapes <, > and &,
	// so a body sitting just under the 32 MB inbound cap can grow up to ~6x on
	// re-marshal and trip the 413. A normal (small) request is unaffected.
	if len(forwardBody) > maxOutboundBodyBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body for provider exceeds the %d byte cap", maxOutboundBodyBytes),
			"invalid_request_error")
		return
	}

	targetEndpoint := target.Provider.Endpoint()
	parsedTarget, err := url.Parse(targetEndpoint)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("invalid provider baseURL: %v", err), "internal_error")
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(outReq *http.Request) {
			outReq.URL.Scheme = parsedTarget.Scheme
			outReq.URL.Host = parsedTarget.Host
			outReq.URL.Path = parsedTarget.Path
			outReq.URL.RawQuery = parsedTarget.RawQuery
			outReq.Host = parsedTarget.Host

			// The outbound request is a copy of the inbound one, so every
			// header agy sent would otherwise ride along to the provider:
			// X-Goog-Api-Key, the agy User-Agent, Accept-Encoding, anything.
			// Drop them all and set only what this request needs.
			for name := range outReq.Header {
				delete(outReq.Header, name)
			}
			if apiKey != "" {
				outReq.Header.Set("Authorization", "Bearer "+apiKey)
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

// findBestTarget looks up a model target in the registry, resolving
// "model (provider)", "provider/model", or raw "model" specifications.
// It prefers providers with valid API keys.
func findBestTarget(reg *providers.Registry, modelSpec string) (providers.Target, error) {
	if reg == nil {
		return providers.Target{}, errors.New("registry is empty or not configured")
	}

	spec := strings.TrimSpace(modelSpec)
	if spec == "" {
		return providers.Target{}, errors.New("empty model requested")
	}

	baseModel, provFilter := providers.ParseModelSpec(spec)

	// 1. If provider was specified ("model (provider)" or "provider/model"),
	// look for an exact provider match first.
	if provFilter != "" {
		for _, p := range reg.Providers {
			if strings.EqualFold(p.Name, provFilter) {
				for _, m := range p.Models {
					if strings.EqualFold(m.ID, baseModel) {
						return providers.Target{Provider: p, Model: m}, nil
					}
				}
			}
		}
	}

	// 2. Search for baseModel with KeyAvailable() == true (respecting provFilter if specified)
	for _, p := range reg.Providers {
		if provFilter != "" && !strings.EqualFold(p.Name, provFilter) {
			continue
		}
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, baseModel) && m.IsEnabled() {
				if p.KeyAvailable() {
					return providers.Target{Provider: p, Model: m}, nil
				}
			}
		}
	}

	// 3. Fallback: try reg.FindTarget
	if target, err := reg.FindTarget(spec); err == nil {
		if target.Provider.KeyAvailable() {
			return target, nil
		}
		alts := reg.FindAlternatesForModel(target.Model.ID, target.Provider.Name)
		for _, alt := range alts {
			if alt.Provider.KeyAvailable() {
				return alt, nil
			}
		}
		return target, nil
	}

	// 4. Fallback search across all providers for baseModel regardless of provFilter,
	// preferring providers with active keys
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, baseModel) && m.IsEnabled() && p.KeyAvailable() {
				return providers.Target{Provider: p, Model: m}, nil
			}
		}
	}

	// 5. Last resort: any matching model
	for _, p := range reg.Providers {
		if provFilter != "" && strings.EqualFold(p.Name, provFilter) {
			for _, m := range p.Models {
				if strings.EqualFold(m.ID, baseModel) {
					return providers.Target{Provider: p, Model: m}, nil
				}
			}
		}
	}
	for _, p := range reg.Providers {
		for _, m := range p.Models {
			if strings.EqualFold(m.ID, baseModel) {
				return providers.Target{Provider: p, Model: m}, nil
			}
		}
	}

	return providers.Target{}, fmt.Errorf("model %q not found in configured providers", modelSpec)
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
