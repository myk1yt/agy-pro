package fanout

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// C2: the unresolved-model errors on the seat/spec resolution paths
// (selector.go explicit picks, engine.go explicit seats) are operand echoes.
// A misdirected key paste in a model slot must come back masked, never raw.
const maskSweepKey = "sk-planted-fanout-echo-key-0123456789abcdef"

func TestSelectTargetsMasksUnresolvedExplicitModel(t *testing.T) {
	reg := &providers.Registry{Providers: []providers.Provider{{
		Name: "nw", Models: []providers.Model{{ID: "glm-5.3-flash"}},
	}}}

	_, err := SelectTargets(reg, nil, 1, []string{maskSweepKey}, nil)
	if err == nil {
		t.Fatal("SelectTargets must reject an unresolvable explicit model")
	}
	if strings.Contains(err.Error(), maskSweepKey) {
		t.Errorf("SelectTargets error echoes the planted key raw: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pl****") {
		t.Errorf("SelectTargets error should carry the readable mask: %v", err)
	}

	// Benign specs keep their readable form.
	_, err = SelectTargets(reg, nil, 1, []string{"nope-model"}, nil)
	if err == nil || !strings.Contains(err.Error(), `"nope-model"`) {
		t.Errorf("benign spec must stay readable in the error, got %v", err)
	}
}

// FIX-5 / S-7: the upstream error snippet is provider-controlled text, and some
// providers echo the credential they were handed straight back into the error
// body. Client.send must mask it before the snippet reaches an error string (and
// from there a seat report, a summary, or the CLI).
//
// FIX-19: the planted value is opaque hex on purpose. An sk-shaped plant is
// masked by the generic skRe pattern even without the per-call seeding, so the
// test could not tell whether the seed still works.
func TestClient_SendMasksUpstreamErrorSnippet(t *testing.T) {
	const planted = "9f2c7a1b4e6d8f0a3b5c7d9e1f2a4b6c8d0e2f4a"
	t.Setenv("FANOUT_SNIPPET_KEY", planted)

	target := providers.Target{
		Provider: providers.Provider{Name: "prov", BaseURL: "", APIKeyEnv: "FANOUT_SNIPPET_KEY"},
		Model:    providers.Model{ID: "m"},
	}
	client := NewClient()

	// The generic shapes must not mask this value: only the per-call seed can.
	if got := providers.NewRedactor()(planted); got != planted {
		t.Fatalf("the plant must be invisible to the generic redactor, got %q", got)
	}

	t.Run("non-2xx body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"auth failed for ` + planted + `"}}`))
		}))
		defer server.Close()
		target.Provider.BaseURL = server.URL

		_, _, err := client.send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatal("send must fail on a 500")
		}
		if strings.Contains(err.Error(), planted) {
			t.Errorf("non-2xx snippet echoes the planted key raw: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Errorf("non-2xx snippet should carry the mask: %v", err)
		}
	})

	t.Run("error object inside a 200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":{"message":"rejected token ` + planted + `"}}`))
		}))
		defer server.Close()
		target.Provider.BaseURL = server.URL

		_, _, err := client.send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatal("send must surface a provider error object")
		}
		if strings.Contains(err.Error(), planted) {
			t.Errorf("API error message echoes the planted key raw: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Errorf("API error message should carry the mask: %v", err)
		}
	})

	t.Run("unparseable body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html>gateway error ` + planted + `</html>`))
		}))
		defer server.Close()
		target.Provider.BaseURL = server.URL

		_, _, err := client.send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatal("send must fail on an unparseable body")
		}
		if strings.Contains(err.Error(), planted) {
			t.Errorf("parse-error snippet echoes the planted key raw: %v", err)
		}
		if !strings.Contains(err.Error(), "[REDACTED]") {
			t.Errorf("parse-error snippet should carry the mask: %v", err)
		}
	})

	// FIX-11: the parse-error snippet is cut at 200 bytes, so a key the provider
	// positions across that cut must still be whole when it reaches the
	// redactor.
	t.Run("straddling the parse-error cut", func(t *testing.T) {
		const cut = 200
		offset := cut - 7
		body := `<html>gateway ` + strings.Repeat("x", offset-len("<html>gateway ")) + planted + strings.Repeat("y", 256)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()
		target.Provider.BaseURL = server.URL

		_, _, err := client.send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatal("send must fail on an unparseable body")
		}
		msg := err.Error()
		if strings.Contains(msg, planted) {
			t.Errorf("parse-error snippet carries the whole planted key: %v", msg)
		}
		if strings.Contains(msg, planted[:cut-offset]) {
			t.Errorf("parse-error snippet carries the straddling fragment: %v", msg)
		}
		// The mask itself may be cut by the 200-byte boundary ("[REDACT"); what
		// matters is that no raw credential fragment survives.
		if !strings.Contains(msg, "[REDACT") {
			t.Errorf("parse-error snippet should carry the mask: %v", msg)
		}
	})

	// FIX-18: the API-error message is capped like every other snippet.
	t.Run("api error message capped", func(t *testing.T) {
		long := strings.Repeat("provider says no ", 200)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"error":{"message":"` + long + `"}}`))
		}))
		defer server.Close()
		target.Provider.BaseURL = server.URL

		_, _, err := client.send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatal("send must surface a provider error object")
		}
		msg := err.Error()
		if !strings.HasSuffix(msg, "... (truncated)") {
			t.Errorf("a long API error message must carry the truncation marker: %q", msg[max(0, len(msg)-80):])
		}
		if len(msg) > len("API error from \"prov\": ")+500+len("... (truncated)") {
			t.Errorf("API error message is %d bytes; want it capped at 500", len(msg))
		}
	})
}

func TestExecuteMasksUnresolvedSeatModel(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "providers.json")
	regJSON := `{"providers":[{"name":"nw","baseURL":"http://127.0.0.1:1","apiKeyEnv":"NW_KEY","models":[{"id":"glm-5.3-flash"}]}]}`
	if err := os.WriteFile(regPath, []byte(regJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Execute(context.Background(), RunConfig{
		Seats:        []SeatSpec{{Kind: SeatKindProvider, Raw: maskSweepKey, ModelID: maskSweepKey}},
		RegistryPath: regPath,
		TimeoutSec:   1,
	})
	if err == nil {
		t.Fatal("Execute must fail on an unresolvable provider seat")
	}
	if strings.Contains(err.Error(), maskSweepKey) {
		t.Errorf("Execute error echoes the planted key raw: %v", err)
	}
	if !strings.Contains(err.Error(), "sk-pl****") {
		t.Errorf("Execute error should carry the readable mask: %v", err)
	}
}
