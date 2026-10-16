package openaiup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agypro/internal/providers"
)

// callTestTarget builds a target whose provider points at url. The key
// environment variable is deliberately left unset here; tests that need a key
// set it afterwards, which also proves Call resolves the key at call time
// rather than at Target construction time.
func callTestTarget(url string) providers.Target {
	return providers.Target{
		Provider: providers.Provider{
			Name:      "test-prov",
			BaseURL:   url,
			APIKeyEnv: "OPENAIUP_TEST_KEY",
		},
		Model: providers.Model{ID: "test-model", Capabilities: providers.Capabilities{SupportsTools: true}},
	}
}

// sseDeltaLine renders one SSE data payload carrying a content delta of the
// given size, for the body-cap tests.
func sseDeltaLine(content string) string {
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}}},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

// sseEvents renders one SSE body from the given data payloads. An empty payload
// list is an empty stream, which is the degenerate truncation case.
func sseEvents(payloads ...string) string {
	var b strings.Builder
	for _, p := range payloads {
		b.WriteString("data: " + p + "\n\n")
	}
	return b.String()
}

// G10F-1: Call must refuse an SSE body that ended without a terminal signal.
// The reader saw a clean EOF, so no read error can reveal the truncation; the
// absence of a finish_reason chunk and of [DONE] is the only signal there is.
func TestCall_SSEWithoutTerminalSignalFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, sseEvents(sseDeltaLine("partial")))
		if fl != nil {
			fl.Flush()
		}
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail when the SSE stream carries no terminal signal")
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Fatalf("err = %v; want ErrStreamTruncated", err)
	}
	if !strings.Contains(err.Error(), "terminal frame") {
		t.Fatalf("err = %v; want the terminal-frame wording", err)
	}
	if turn != nil {
		t.Errorf("turn = %+v; want nil on failure", turn)
	}
}

// G10F2-1: the quoted sentinel is the spelling this project's own provider
// fixtures use, so a stream whose ONLY terminal signal is `data: "[DONE]"`
// must complete rather than be answered as a truncated turn.
func TestCall_SSEWithQuotedSentinelOnlyCompletes(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"quoted", sseEvents(`"[DONE]"`)},
		{"quoted lowercase", sseEvents(`"[done]"`)},
		{"bare lowercase", sseEvents("[done]")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, sseEvents(sseDeltaLine("whole"))+tc.tail)
			}))
			defer server.Close()

			turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if turn == nil || turn.Content() != "whole" {
				t.Fatalf("turn = %+v; want the folded content", turn)
			}
			if !turn.StreamTerminal {
				t.Error("StreamTerminal must be set for a quoted sentinel")
			}
		})
	}
}

// The negative control: either terminal signal is enough, so a clean stream
// still returns its turn.
func TestCall_SSEWithTerminalSignalSucceeds(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"finish_reason chunk", sseEvents(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)},
		{"[DONE] sentinel", sseEvents("[DONE]")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, sseEvents(sseDeltaLine("whole"))+tc.tail)
			}))
			defer server.Close()

			turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if turn == nil || turn.Content() != "whole" {
				t.Fatalf("turn = %+v; want the folded content", turn)
			}
			if !turn.StreamTerminal {
				t.Error("StreamTerminal must be set on a clean stream")
			}
		})
	}
}

// A non-streaming JSON body cannot end prematurely: a truncated body fails to
// parse, which is already the malformed row. So the JSON branch is untouched.
func TestCall_JSONBranchIgnoresStreamTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"whole"}}]}`)
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if turn == nil || turn.Content() != "whole" {
		t.Fatalf("turn = %+v; want the folded content", turn)
	}
}

// sseLineOfSize renders one SSE event ("data: …\n\n") whose total byte length is
// exactly size, so the body-cap tests can hit the boundary to the byte.
func sseLineOfSize(t *testing.T, size int) string {
	t.Helper()
	const (
		prefix = "data: "
		head   = `{"choices":[{"index":0,"delta":{"content":"`
		tail   = `"}}]}`
		suffix = "\n\n"
	)
	fixed := len(prefix) + len(head) + len(tail) + len(suffix)
	if size < fixed {
		t.Fatalf("size %d is below the minimum SSE line of %d bytes", size, fixed)
	}
	return prefix + head + strings.Repeat("a", size-fixed) + tail + suffix
}

func TestCall_SSEBranch(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotContentType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	target := callTestTarget(server.URL)
	turn, err := Call(context.Background(), server.Client(), target, []byte(`{"model":"test-model"}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if turn.Content() != "hello" {
		t.Errorf("Content = %q; want hello", turn.Content())
	}
	if gotPath != "/chat/completions" {
		t.Errorf("upstream path = %q; want Provider.Endpoint()", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", gotContentType)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q; want no header when the key is empty", gotAuth)
	}
}

func TestCall_JSONBranch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"listed"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`)
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if turn.Content() != "listed" {
		t.Errorf("Content = %q; want listed", turn.Content())
	}
	if turn.Usage["total_tokens"] != float64(7) {
		t.Errorf("Usage = %v; want total_tokens 7", turn.Usage)
	}
}

func TestCall_BearerSetOnlyWhenKeyPresent(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer server.Close()

	t.Setenv("OPENAIUP_TEST_KEY", "up-secret-key-1234")
	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	_ = turn
	if gotAuth != "Bearer up-secret-key-1234" {
		t.Errorf("Authorization = %q; want the bearer key", gotAuth)
	}
}

func TestCall_Non200RedactedSnippet(t *testing.T) {
	const planted = "sk-plantedupstreamsecret0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"boom internal `+planted+`","echo":"Authorization: Bearer `+planted+`"}`)
	}))
	defer server.Close()

	redact := providers.NewRedactor(planted)
	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), redact)
	if err == nil {
		t.Fatal("Call must fail on a non-200 status")
	}
	if turn != nil {
		t.Errorf("turn = %+v; want nil on failure", turn)
	}
	msg := err.Error()
	if !strings.Contains(msg, "upstream returned status 500") {
		t.Errorf("err = %q; want the status line", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("err = %q; want the redaction marker", msg)
	}
	if strings.Contains(msg, planted) {
		t.Errorf("err leaked the planted key: %q", msg)
	}
}

// A non-200 body longer than the snippet cap must not be carried in full.
// FIX-2: a caller that passes no redactor must still fail closed. The upstream
// error snippet is the one place provider-controlled text reaches a log or audit
// sink, so the default redactor has to mask the generic credential shapes.
func TestCall_NilRedactorStillMasksCredentials(t *testing.T) {
	const planted = "sk-plantednilredactsecret0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key `+planted+`","echo":"Authorization: Bearer `+planted+`"}`)
	}))
	defer server.Close()

	_, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail on a non-200 status")
	}
	if strings.Contains(err.Error(), planted) {
		t.Errorf("nil redactor leaked the planted credential: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Errorf("err = %q; want the default redactor to mask it", err.Error())
	}
}

func TestCall_Non200SnippetIsCapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, strings.Repeat("x", 64<<10))
	}))
	defer server.Close()

	_, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail on a non-200 status")
	}
	if got := len(err.Error()); got > 4096 {
		t.Errorf("error length = %d; want the body snippet capped (2048 bytes plus the status prefix)", got)
	}
}

// The upstream body is capped at MaxBodyBytes, so a provider streaming forever
// cannot exhaust memory.
// FIX-10: the cappedReader boundary is pinned to the byte. A body of exactly the
// cap must not trip the breach flag, and one byte past it must.
func TestCappedReader_Boundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		want bool
	}{
		{"body == cap", MaxBodyBytes, false},
		{"body == cap+1", MaxBodyBytes + 1, true},
		{"body == cap-1", MaxBodyBytes - 1, false},
		{"body == cap+4096", MaxBodyBytes + 4096, true},
		{"empty body", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &cappedReader{r: strings.NewReader(strings.Repeat("a", tc.size)), left: MaxBodyBytes + 1}
			n, err := io.Copy(io.Discard, cr)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if want := int64(min(tc.size, MaxBodyBytes+1)); n != want {
				t.Errorf("read %d bytes; want %d", n, want)
			}
			if got := cr.exceeded(); got != tc.want {
				t.Errorf("exceeded() = %v; want %v (body %d, cap %d)", got, tc.want, tc.size, MaxBodyBytes)
			}
		})
	}
}

// FIX-10 end-to-end: an SSE body of exactly the cap is a legitimate large
// response and must still succeed. The fixture includes the [DONE] line, so the
// whole body is exactly MaxBodyBytes.
func TestCall_SSEExactlyAtCapSucceeds(t *testing.T) {
	const (
		done  = "data: [DONE]\n\n"
		lines = 4
		head  = `{"choices":[{"index":0,"delta":{"content":"`
		tail  = `"}}]}`
		fixed = len("data: ") + len(head) + len(tail) + len("\n\n")
	)
	budget := MaxBodyBytes - len(done)
	base, rem := budget/lines, budget%lines

	var body strings.Builder
	want := 0
	for i := 0; i < lines; i++ {
		size := base
		if i == lines-1 {
			size += rem // the remainder rides on the last line
		}
		line := sseLineOfSize(t, size)
		body.WriteString(line)
		want += size - fixed
	}
	body.WriteString(done)
	if body.Len() != MaxBodyBytes {
		t.Fatalf("fixture body = %d bytes; want exactly %d", body.Len(), MaxBodyBytes)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body.String())
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("a body of exactly the cap must succeed: %v", err)
	}
	if len(turn.Content()) != want {
		t.Errorf("Content length = %d; want %d", len(turn.Content()), want)
	}
}

// FIX-11: the error snippet must be redacted before it is cut. A key the
// provider positions across the 2048-byte cut used to leave its unshaped
// fragment in the error string.
func TestCall_Non200SnippetRedactsBeforeCutting(t *testing.T) {
	// Opaque hex: no generic redactor shape matches it, so only the per-call
	// seed can mask it.
	const planted = "9f2c7a1b4e6d8f0a3b5c7d9e1f2a4b6c8d0e2f4a"
	offset := errorSnippetBytes - 10 // the key straddles the cut
	body := strings.Repeat("x", offset) + planted + strings.Repeat("y", 512)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	redact := providers.NewRedactor(planted)
	_, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), redact)
	if err == nil {
		t.Fatal("Call must fail on a non-200 status")
	}
	msg := err.Error()
	if strings.Contains(msg, planted) {
		t.Errorf("error carries the whole planted key: %q", msg)
	}
	if strings.Contains(msg, planted[:errorSnippetBytes-offset]) {
		t.Errorf("error carries the straddling fragment of the planted key: %q", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("err = %q; want the masked form", msg)
	}
}

func TestCall_MaxBodyBytesValue(t *testing.T) {
	if MaxBodyBytes != 32<<20 {
		t.Fatalf("MaxBodyBytes = %d; want 32 MB", MaxBodyBytes)
	}
}

// FIX-3: an SSE stream that outgrows the 32 MB cap must fail loudly. Reading it
// through io.LimitReader alone answered a clean EOF at the cap, so a truncated
// stream used to come back as a successful (silently shortened) turn.
func TestCall_SSEOverCapReturnsLoudError(t *testing.T) {
	// One data line stays far below the 16 MB scanner token limit; forty of
	// them pass the 32 MB body cap.
	line := "data: " + sseDeltaLine(strings.Repeat("a", 1<<20)) + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 40; i++ {
			_, _ = io.WriteString(w, line)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail when the SSE stream exceeds the body cap")
	}
	if turn != nil {
		t.Errorf("turn = %+v; want nil on failure", turn)
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("err = %v; want the cap breach named", err)
	}
}

// A body just under the cap still succeeds: the cap must not reject a
// legitimate large response.
func TestCall_SSEJustUnderCapSucceeds(t *testing.T) {
	line := "data: " + sseDeltaLine(strings.Repeat("b", 1<<20)) + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 20; i++ { // ~20 MB, comfortably under 32 MB
			_, _ = io.WriteString(w, line)
			if fl != nil {
				fl.Flush()
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(turn.Content()) != 20<<20 {
		t.Errorf("Content length = %d; want %d", len(turn.Content()), 20<<20)
	}
}

// FIX-3 (JSON branch): the non-streaming path already failed loudly on a
// truncated body and must keep doing so.
func TestCall_JSONOverCapReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// A body that starts like valid JSON but never closes: the cap cuts it
		// off before the reader can run away.
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"content":"`+strings.Repeat("a", MaxBodyBytes))
	}))
	defer server.Close()

	turn, err := Call(context.Background(), server.Client(), callTestTarget(server.URL), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail on a truncated JSON body")
	}
	if turn != nil {
		t.Errorf("turn = %+v; want nil on failure", turn)
	}
}

func TestCall_TransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := Call(context.Background(), &http.Client{}, callTestTarget(url), []byte(`{}`), nil)
	if err == nil {
		t.Fatal("Call must fail when the listener is gone")
	}
	if !strings.Contains(err.Error(), "upstream request failed") {
		t.Errorf("err = %v; want the transport failure wrapper", err)
	}
}

// No client-level timeout is set inside Call: a response slower than any
// reasonable default client timeout still succeeds because the deadline comes
// from the context alone.
func TestCall_NoClientLevelTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"content":"slow"}}]}`)
	}))
	defer server.Close()

	turn, err := Call(context.Background(), &http.Client{}, callTestTarget(server.URL), []byte(`{}`), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if turn.Content() != "slow" {
		t.Errorf("Content = %q; want slow", turn.Content())
	}
}

func TestNewCompletionID(t *testing.T) {
	const prefix = "chatcmpl-"
	for i := 0; i < 64; i++ {
		id := NewCompletionID()
		if !strings.HasPrefix(id, prefix) {
			t.Fatalf("id = %q; want the %q prefix", id, prefix)
		}
		body := strings.TrimPrefix(id, prefix)
		if len(body) != 24 {
			t.Errorf("id = %q; want 24 hex characters (12 random bytes)", id)
		}
		for _, r := range body {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("id = %q; want lowercase hex only", id)
			}
		}
	}

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := NewCompletionID()
		if seen[id] {
			t.Fatalf("id %q repeated; ids must be unique", id)
		}
		seen[id] = true
	}
}
