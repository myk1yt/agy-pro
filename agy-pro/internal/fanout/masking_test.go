package fanout

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agypro/internal/paths"
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

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep): the fanout client's four error sentences
// quote target.Provider.Name, a registry slot. Each routes through
// providers.MaskSlot (the registry-slot authority: MaskOperand plus the
// underscore-head credential arm), so the "sk_live_..." class that the operand
// masker renders raw masks here, and every benign name keeps its exact bytes.
// ---------------------------------------------------------------------------

func TestClient_ErrorSinksMaskRegistryProviderNameThroughSlotAuthority(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)
	newTarget := func(baseURL string) providers.Target {
		return providers.Target{
			Provider: providers.Provider{Name: plant, BaseURL: baseURL},
			Model:    providers.Model{ID: "m"},
		}
	}
	server := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}

	// "empty choices returned by provider %q" (200 with no choices).
	srv := server(http.StatusOK, `{"choices":[]}`)
	defer srv.Close()
	_, _, err := NewClient().send(context.Background(), newTarget(srv.URL), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("send must fail on an empty choices list")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("empty-choices error echoes the registry name raw: %v", err)
	}
	if want := `empty choices returned by provider "` + plantMask + `"`; err.Error() != want {
		t.Errorf("empty-choices error drifted\n got: %q\nwant: %q", err.Error(), want)
	}

	// "API error from %q" (provider error object inside a 200).
	srv2 := server(http.StatusOK, `{"error":{"message":"rejected"}}`)
	defer srv2.Close()
	_, _, err = NewClient().send(context.Background(), newTarget(srv2.URL), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("send must surface a provider error object")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("API-error sentence echoes the registry name raw: %v", err)
	}
	if want := `API error from "` + plantMask + `": rejected`; err.Error() != want {
		t.Errorf("API-error sentence drifted\n got: %q\nwant: %q", err.Error(), want)
	}

	// "HTTP %d (%s) from provider %q: %s" (non-2xx).
	srv3 := server(http.StatusInternalServerError, `{"error":{"message":"boom"}}`)
	defer srv3.Close()
	_, _, err = NewClient().send(context.Background(), newTarget(srv3.URL), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("send must fail on a 500")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("non-2xx sentence echoes the registry name raw: %v", err)
	}
	if !strings.Contains(err.Error(), `from provider "`+plantMask+`":`) {
		t.Errorf("non-2xx sentence must render the name as %q: %v", plantMask, err)
	}

	// "network error calling %s (%s): %w" (unreachable endpoint).
	_, _, err = NewClient().send(context.Background(), newTarget("http://127.0.0.1:1"), ChatRequest{Model: "m"})
	if err == nil {
		t.Fatal("send must fail against a closed port")
	}
	if strings.Contains(err.Error(), plant) {
		t.Errorf("network-error sentence echoes the registry name raw: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "network error calling "+plantMask+" (") {
		t.Errorf("network-error sentence must render the name as %q: %v", plantMask, err)
	}

	// Benign corpus: byte-identical sentences.
	for _, name := range []string{"nw", "openai", "gemini", "deepseek", "charm", "acme-corp"} {
		target := providers.Target{
			Provider: providers.Provider{Name: name, BaseURL: srv.URL},
			Model:    providers.Model{ID: "m"},
		}
		_, _, err := NewClient().send(context.Background(), target, ChatRequest{Model: "m"})
		if err == nil {
			t.Fatalf("%s: send must fail on an empty choices list", name)
		}
		if want := `empty choices returned by provider "` + name + `"`; err.Error() != want {
			t.Errorf("%s: sentence drifted\n got: %q\nwant: %q", name, err.Error(), want)
		}
	}
}

// ---------------------------------------------------------------------------
// FIX-8 (module-wide echo sweep, fanout surface): the progress-line seat spec's
// provider form ("provider/model") and the CLI banner's seat-report lines are
// built from registry slots (provider name, model id), so both render through
// providers.MaskSlot. Benign specs keep their exact bytes; the underscore-head
// credential class masks. The native form ("native:<id>") is NOT a registry
// slot (the id arrives through --seats or the native catalog), so it keeps the
// operand masker; the pin below holds its benign rendering byte-identical.
// ---------------------------------------------------------------------------

func TestSeatSpecLabelMasksRegistrySlots(t *testing.T) {
	const (
		plant     = "sk_live_0123456789abcdef"
		plantMask = "sk_li****"
	)
	provSeat := func(name, id string) providers.Target {
		return providers.Target{Provider: providers.Provider{Name: name}, Model: providers.Model{ID: id}}
	}
	cases := []struct {
		name string
		kind SeatKind
		tgt  providers.Target
		want string
	}{
		// The "native:<id>" form: byte-identical, on the unchanged authority.
		{"native benign", SeatKindNative, provSeat("", "gemini-3.8-flash-high"), "native:gemini-3.8-flash-high"},
		{"native catalog id", SeatKindNative, provSeat("", "claude-opus-5-5-high"), "native:claude-opus-5-5-high"},
		// The "provider/model" form (Target.FullName): both halves are slots.
		{"provider benign", SeatKindProvider, provSeat("openai", "gpt-5.1"), "openai/gpt-5.1"},
		{"provider nw", SeatKindProvider, provSeat("nw", "glm-5.3-flash"), "nw/glm-5.3-flash"},
		{"provider name plant", SeatKindProvider, provSeat(plant, "m"), plantMask},
		{"model id plant", SeatKindProvider, provSeat("nw", plant), "nw/sk****"},
	}
	for _, tc := range cases {
		if got := seatSpecLabel(tc.kind, tc.tgt); got != tc.want {
			t.Errorf("%s: seatSpecLabel = %q; want %q", tc.name, got, tc.want)
		}
	}
	// Either half of the composed provider form is caught whole.
	for _, bad := range []string{plant, "nw/" + plant, plant + "/glm-5.3-flash"} {
		if got := seatSpecLabel(SeatKindProvider, provSeat("nw", bad)); strings.Contains(got, plant) {
			t.Errorf("seatSpecLabel(provider, %q) = %q; the plant must not ride raw", bad, got)
		}
	}
}

// TestRunBannerSeatReportLineMasksRegistrySlots pins the banner twin of the
// seat report file name: the CLI prints the masked pair (sanitizeFilename turns
// the mask's stars into dashes, the I2 rule), and a benign seat keeps the exact
// line the banner printed before the conversion.
func TestRunBannerSeatReportLineMasksRegistrySlots(t *testing.T) {
	const plant = "sk_live_0123456789abcdef"
	t.Setenv(paths.EnvAgyProHome, t.TempDir())

	old := executeEngine
	executeEngine = func(ctx context.Context, cfg RunConfig) (*SummaryReport, error) {
		return &SummaryReport{
			Task:       cfg.Task,
			NRequested: 2,
			Seats: []SeatResult{
				{SeatID: 1, FinalTarget: providers.Target{
					Provider: providers.Provider{Name: plant},
					Model:    providers.Model{ID: plant},
				}},
				{SeatID: 2, FinalTarget: providers.Target{
					Provider: providers.Provider{Name: "openai"},
					Model:    providers.Model{ID: "gpt-5.1"},
				}},
			},
		}, nil
	}
	t.Cleanup(func() { executeEngine = old })

	var out, errw bytes.Buffer
	if code := Run([]string{"-prompt", "p"}, nil, &out, &errw); code != 0 {
		t.Fatalf("Run exit = %d\nstderr:\n%s", code, errw.String())
	}
	banner := out.String()
	if strings.Contains(banner, plant) {
		t.Errorf("banner leaks the credential-shaped registry slots:\n%s", banner)
	}
	for _, want := range []string{
		"  Seat 1 Report:       seat-1-sk_li-----sk_li----.md",
		"  Seat 2 Report:       seat-2-openai-gpt-5.1.md",
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner lost the byte-exact seat line %q:\n%s", want, banner)
		}
	}
}
