package agenttools

// B4-2b pins for search_web (search.go).
//
// Behavior pins (CONTRACT section 3.6): a zero source returns the honest
// not-configured error at the function level and the runner level (never a
// fake empty success); a configured source substitutes the query into the
// endpoint template and renders the backend's JSON for the model; the key
// VALUE is read from the environment by NAME at call time (present works,
// unset errors naming the variable) and never reaches the tool log (pinned
// with the canonical key-shaped dummy, never a real credential); non-2xx,
// invalid JSON, over-cap, and timeout failures are clear errors; the schema
// exposes exactly one query argument.
//
// Mutation-verified: dropping the searchLogSeeds block in runner.go fails
// the audit pin, and a fake empty success on the not-configured path fails
// the honest-degrade pin (see the unit report).

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// searchDummySecret is the canonical AWS documentation example shape, never
// a real credential (the same dummy the fetch and scope pins use).
const searchDummySecret = testfixtures.AWSSecretExample

// searchDummySecretQuery is a SECOND key-shaped dummy (same documentation
// example family, never a real credential). G3: the output-reflection pin
// uses it for the QUERY while the env-var key VALUE carries
// searchDummySecret, so deleting the key-value seed operand fails the pin
// instead of hiding behind the query seed.
const searchDummySecretQuery = testfixtures.AWSSecretQuery

// searchJSONBody is the generic backend shape the renderer must handle: a
// top-level results array with title/url/snippet fields.
const searchJSONBody = `{"results":[{"title":"Doc One","url":"https://a.example/1","snippet":"first hit"},{"title":"Doc Two","url":"https://b.example/2","snippet":"second hit"}]}`

// searchStub is a loopback search backend recording the request it received
// and answering with one fixed JSON body.
func searchStub(t *testing.T, body string) (*httptest.Server, *sync.Mutex, *string) {
	t.Helper()
	var mu sync.Mutex
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastPath = r.URL.String()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &mu, &lastPath
}

// Not configured: the function level AND the runner level return the clear
// not-configured error (honest degrade, never a fake empty result).
func TestSearchWebNotConfigured(t *testing.T) {
	if _, err := SearchWeb("anything", SearchSource{}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Errorf("function level: err = %v; want the not-configured error", err)
	}

	root := t.TempDir()
	logger, err := NewLogger(filepath.Join(root, "fanout.jsonl"), nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	out, err := runner.Execute(1, "model-x", "prov-x", "search_web", `{"query":"hello"}`)
	if err != nil {
		t.Fatalf("runner level must be error-shaped (nil Go error): %v", err)
	}
	if !strings.Contains(out, "not configured") {
		t.Errorf("runner result = %q; want the not-configured error", out)
	}
}

// Happy path: the query is substituted into the endpoint template (escaped),
// the Authorization header carries the env key on the wire, and the results
// are rendered for the model. The stub records the request under a mutex
// (G5: the previous handler-swap plus a plain capture had no happens-before
// edge; -race is not runnable on this host, so the pattern is fixed
// structurally).
func TestSearchWebHappyPath(t *testing.T) {
	var mu sync.Mutex
	var lastPath string
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastPath = r.URL.String()
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchJSONBody)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("SEARCH_STUB_KEY", "stub-key-value")

	source := SearchSource{Endpoint: srv.URL + "/search?q={query}", KeyEnv: "SEARCH_STUB_KEY"}
	out, err := SearchWeb("hello world", source)
	if err != nil {
		t.Fatalf("SearchWeb: %v", err)
	}
	if !strings.Contains(out, "Doc One") || !strings.Contains(out, "https://a.example/1") ||
		!strings.Contains(out, "first hit") || !strings.Contains(out, "Doc Two") {
		t.Errorf("rendered results = %q; want both hits with title, url and snippet", out)
	}

	mu.Lock()
	gotPath := lastPath
	auth := lastAuth
	mu.Unlock()
	if !strings.Contains(gotPath, "q=hello+world") {
		t.Errorf("stub saw path %q; want the query substituted and escaped", gotPath)
	}

	// The key traveled in the Authorization header only.
	if auth != "Bearer stub-key-value" {
		t.Errorf("Authorization header = %q; want the Bearer key from the env var", auth)
	}
}

// Key handling: present works (covered by the happy path); an unset or empty
// env var errors NAMING the variable; a source with an empty KeyEnv works
// without any key.
func TestSearchWebKeyFromEnvByName(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)

	// Unset (t.Setenv with "" models the unset-or-empty shape and restores
	// the original afterwards).
	t.Setenv("SEARCH_STUB_UNSET_KEY", "")
	_, err := SearchWeb("q", SearchSource{Endpoint: srv.URL + "/s?q={query}", KeyEnv: "SEARCH_STUB_UNSET_KEY"})
	if err == nil || !strings.Contains(err.Error(), "SEARCH_STUB_UNSET_KEY") {
		t.Errorf("unset key env: err = %v; want an error naming the env var", err)
	}

	// No key required: an empty KeyEnv calls without auth and succeeds.
	out, err := SearchWeb("q", SearchSource{Endpoint: srv.URL + "/s?q={query}"})
	if err != nil || !strings.Contains(out, "Doc One") {
		t.Errorf("keyless source: out=%q err=%v; want the rendered results", out, err)
	}
}

// Audit pin (E2 discipline): a call with a key-shaped env value and a
// key-shaped query is logged by the ONE redacting tool log; neither the key
// VALUE nor the secret-shaped query value persists raw (both render
// [REDACTED]).
func TestToolRunnerSearchWebAuditLog(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	t.Setenv("SEARCH_AUDIT_KEY", searchDummySecret)

	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/search?q={query}", KeyEnv: "SEARCH_AUDIT_KEY"}
	out, err := runner.Execute(2, "model-x", "prov-x", "search_web", `{"query":"token `+searchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil", err)
	}
	if !strings.Contains(out, "Doc One") {
		t.Errorf("model-visible result = %q; want the rendered results", out)
	}

	// The not-configured call is recorded too (the honest degrade is visible
	// in the audit trail, not silent).
	closed := NewRunner(root, false, false, logger)
	refusal, err := closed.Execute(3, "model-x", "prov-x", "search_web", `{"query":"plain query"}`)
	if err != nil {
		t.Fatalf("not-configured call must be error-shaped (nil Go error): %v", err)
	}
	if !strings.Contains(refusal, "not configured") {
		t.Errorf("not-configured result = %q; want the clear error", refusal)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, searchDummySecret) {
		t.Errorf("tool log leaks the raw key-shaped value:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
	if !strings.Contains(log, "search_web") {
		t.Errorf("tool log recorded no search_web call:\n%s", log)
	}
	if !strings.Contains(log, "not configured") {
		t.Errorf("tool log recorded no not-configured entry:\n%s", log)
	}
	if !strings.Contains(log, "Doc One") {
		t.Errorf("tool log lost the allowed call's rendered results:\n%s", log)
	}
	// Over-seeding cost (round-5 design): whole operands seed
	// unconditionally, so the benign query renders [REDACTED] in the
	// Arguments field; the benign rendered results above stay readable.
	if strings.Contains(log, "plain query") {
		t.Errorf("the benign query should be masked as a whole operand:\n%s", log)
	}
}

// Failure shapes: non-2xx names the status, invalid JSON names the parse,
// and an over-cap body is refused (never a truncated fake parse).
func TestSearchWebFailureShapes(t *testing.T) {
	srcOf := func(code int, body string) SearchSource {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		return SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	}

	if _, err := SearchWeb("q", srcOf(500, `{"error":"boom"}`)); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Errorf("non-2xx: err = %v; want an error naming the status", err)
	}
	if _, err := SearchWeb("q", srcOf(200, "this is not json")); err == nil ||
		!strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("bad JSON: err = %v; want the invalid-JSON error", err)
	}
	if _, err := SearchWeb("q", srcOf(200, strings.Repeat(" ", maxSearchBodyBytes+100))); err == nil ||
		!strings.Contains(err.Error(), "larger than") {
		t.Errorf("over-cap body: err = %v; want the size refusal", err)
	}
}

// Timeout shape: a backend slower than the injected client's budget fails
// with a clear timeout-flavored error (the production budget is the fixed
// tool-layer constant, injected short only by tests).
func TestSearchWebTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchJSONBody)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 50 * time.Millisecond}
	_, err := searchWeb("q", SearchSource{Endpoint: srv.URL + "/s?q={query}"}, client)
	if err == nil || (!strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout")) {
		t.Errorf("timeout: err = %v; want the deadline/timeout error", err)
	}
}

// Endpoint gate: a template without the {query} placeholder, an http
// non-loopback endpoint, and a non-http scheme are all refused before any
// network use.
func TestSearchWebEndpointGate(t *testing.T) {
	if _, err := SearchWeb("q", SearchSource{Endpoint: "https://api.example.com/search"}); err == nil ||
		!strings.Contains(err.Error(), "{query}") {
		t.Errorf("no placeholder: err = %v; want the template refusal", err)
	}
	if _, err := SearchWeb("q", SearchSource{Endpoint: "http://api.example.com/s?q={query}"}); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Errorf("http non-loopback: err = %v; want the scheme refusal (no network)", err)
	}
	if _, err := SearchWeb("q", SearchSource{Endpoint: "ftp://api.example.com/s?q={query}"}); err == nil ||
		!strings.Contains(err.Error(), "https only") {
		t.Errorf("ftp: err = %v; want the https-only refusal", err)
	}
}

// Generic rendering: a vendor-wrapped array and a document without any
// result array both render for the model (fallback is the indented JSON).
func TestSearchWebGenericRendering(t *testing.T) {
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"pages":[{"name":"Wrapped Hit","link":"https://w.example/","description":"wrapped snippet"}]}}`)
	}))
	t.Cleanup(wrapped.Close)
	out, err := SearchWeb("q", SearchSource{Endpoint: wrapped.URL + "/s?q={query}"})
	if err != nil || !strings.Contains(out, "Wrapped Hit") || !strings.Contains(out, "wrapped snippet") {
		t.Errorf("wrapped results: out=%q err=%v; want the aliased fields rendered", out, err)
	}

	scalar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"answer":42}`)
	}))
	t.Cleanup(scalar.Close)
	out, err = SearchWeb("q", SearchSource{Endpoint: scalar.URL + "/s?q={query}"})
	if err != nil || !strings.Contains(out, `"answer": 42`) {
		t.Errorf("scalar fallback: out=%q err=%v; want the indented JSON", out, err)
	}

	// The item cap renders at most maxSearchResults blocks.
	many := "["
	for i := 0; i < maxSearchResults+5; i++ {
		if i > 0 {
			many += ","
		}
		many += fmt.Sprintf(`{"title":"Hit %d","url":"https://x.example/%d"}`, i, i)
	}
	many += "]"
	bigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, many)
	}))
	t.Cleanup(bigSrv.Close)
	out, err = SearchWeb("q", SearchSource{Endpoint: bigSrv.URL + "/s?q={query}"})
	if err != nil {
		t.Fatalf("many results: %v", err)
	}
	if strings.Contains(out, fmt.Sprintf("Hit %d", maxSearchResults)) {
		t.Errorf("the render leaked past the %d-item cap:\n%s", maxSearchResults, out)
	}
	if !strings.Contains(out, "more results omitted") {
		t.Errorf("the render carries no omission note:\n%s", out)
	}
}

// The schema exposure unit: the definition names search_web and requires
// exactly the query argument (no endpoint, no key, no timeout).
func TestSearchWebToolDef(t *testing.T) {
	def := SearchWebToolDef()
	if def.Type != "function" || def.Function.Name != "search_web" {
		t.Errorf("def = %+v; want the search_web function definition", def)
	}
	required, ok := def.Function.Parameters["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "query" {
		t.Errorf("required = %v; want exactly [query]", def.Function.Parameters["required"])
	}
	props, ok := def.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing")
	}
	for _, banned := range []string{"endpoint", "key", "timeout", "method"} {
		if _, present := props[banned]; present {
			t.Errorf("the schema must expose no %q argument (the source and budget are fixed by the run)", banned)
		}
	}
}

// Redirect policy (R1): the production client caps the chain at 3 followed
// redirects, re-runs the scheme/userinfo gate on every target, and keeps the
// request host fixed, so the Authorization-bearing request can neither be
// downgraded nor moved. The end-to-end pins ride the loopback stub through
// the PRODUCTION client (http loopback is allowed so the stubs work); the
// downgrade and cross-host pins exercise the policy closure directly (no TLS
// stub exists that the production client trusts).
func TestSearchWebRedirectPolicy(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/c0":
			fmt.Fprint(w, searchJSONBody)
		case "/c1", "/c2", "/c3", "/c4":
			n := int(r.URL.Path[2] - '0')
			http.Redirect(w, r, fmt.Sprintf("/c%d", n-1), http.StatusFound)
		case "/escape":
			// The same service named localhost while the request host is the
			// 127.0.0.1 spelling: the redirect must not move the request.
			http.Redirect(w, r, "http://localhost:"+stubPort(t, srv)+"/c0", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/c0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	src := func(path string) SearchSource {
		return SearchSource{Endpoint: srv.URL + path + "?q={query}"}
	}

	// Happy path without redirect (kept for contrast).
	if out, err := SearchWeb("q", src("/c0")); err != nil || !strings.Contains(out, "Doc One") {
		t.Errorf("no redirect: out=%q err=%v; want the rendered results", out, err)
	}
	// An allowed same-host redirect is followed.
	if out, err := SearchWeb("q", src("/hop")); err != nil || !strings.Contains(out, "Doc One") {
		t.Errorf("same-host redirect: out=%q err=%v; want the rendered results", out, err)
	}
	// A chain of 3 followed redirects lands on the final body.
	if out, err := SearchWeb("q", src("/c3")); err != nil || !strings.Contains(out, "Doc One") {
		t.Errorf("chain of 3: out=%q err=%v; want the final body", out, err)
	}
	// The fourth redirect is refused.
	if _, err := SearchWeb("q", src("/c4")); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Errorf("chain of 4: err = %v; want the redirect-cap refusal", err)
	}
	// A redirect to another host spelling is refused by the host-invariance rule.
	if _, err := SearchWeb("q", src("/escape")); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("cross-host redirect: err = %v; want the host-invariance refusal", err)
	}

	// The policy closure directly: a same-host https-to-http downgrade is
	// refused by the scheme gate; a cross-host https target by the
	// host-invariance rule; an allowed same-host https target passes.
	policy := searchRedirectCheck("api.example.com")
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", raw, err)
		}
		return req
	}
	if err := policy(mkReq("http://api.example.com/x"), nil); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Errorf("https-to-http downgrade: err = %v; want the scheme refusal", err)
	}
	if err := policy(mkReq("https://other.example.com/x"), nil); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("cross-host target: err = %v; want the host-invariance refusal", err)
	}
	if err := policy(mkReq("https://api.example.com/y"), []*http.Request{mkReq("https://api.example.com/x")}); err != nil {
		t.Errorf("same-host https target: err = %v; want nil (allowed)", err)
	}
}

// Template placement (R4/R18): exactly one {query} placeholder, and the
// placeholder may not sit in the scheme or the authority (substitution runs
// before the scheme gate, so an authority placement would let the query
// choose the request host and receive the key). Refusals happen before any
// network use; the path and the raw query stay valid positions.
func TestSearchWebTemplatePlacement(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{"https://api.example.com/s?q={query}&r={query}", "exactly one"},
		{"https://{query}.example.com/s", "authority"},
		{"https://user{query}@api.example.com/s", "authority"},
		{"{query}://api.example.com/s", "scheme"},
	}
	for _, tc := range cases {
		if _, err := SearchWeb("x", SearchSource{Endpoint: tc.endpoint}); err == nil ||
			!strings.Contains(err.Error(), tc.want) {
			t.Errorf("endpoint %q: err = %v; want the %q refusal", tc.endpoint, err, tc.want)
		}
	}

	// The path position keeps working end to end, and the substituted request
	// host equals the template host.
	srv, mu, lastPath := searchStub(t, searchJSONBody)
	if out, err := SearchWeb("hello world", SearchSource{Endpoint: srv.URL + "/search/{query}"}); err != nil ||
		!strings.Contains(out, "Doc One") {
		t.Errorf("path placement: out=%q err=%v; want the rendered results", out, err)
	}
	mu.Lock()
	gotPath := *lastPath
	mu.Unlock()
	if !strings.Contains(gotPath, "/search/hello+world") {
		t.Errorf("stub saw path %q; want the query substituted into the path", gotPath)
	}
}

// Deterministic rendering (R5): with more than one array of objects (or
// case-colliding alias keys), the rendered result is byte-identical across
// calls: map keys iterate in sorted order and the alias priority is fixed.
func TestSearchWebDeterministicRendering(t *testing.T) {
	body := `{"zeta":[{"title":"Zed","url":"https://z.example/1"}],"alpha":[{"title":"Alpha","url":"https://a.example/1"}]}`
	first, err := renderSearchResults([]byte(body))
	if err != nil {
		t.Fatalf("renderSearchResults: %v", err)
	}
	if !strings.Contains(first, "Alpha") || strings.Contains(first, "Zed") {
		t.Errorf("sorted-key selection: first render = %q; want the alpha array only", first)
	}

	collide := `{"results":[{"Name":"Caps Title","name":"lower title","URL":"https://u.example/1","url":"https://u.example/2","snippet":"s"}]}`
	second, err := renderSearchResults([]byte(collide))
	if err != nil {
		t.Fatalf("renderSearchResults (alias collision): %v", err)
	}
	if !strings.Contains(second, "Caps Title") || strings.Contains(second, "lower title") ||
		!strings.Contains(second, "https://u.example/1") || strings.Contains(second, "https://u.example/2") {
		t.Errorf("sorted alias selection: second render = %q; want the sorted-first keys", second)
	}

	for i := 0; i < 20; i++ {
		again, err := renderSearchResults([]byte(body))
		if err != nil || again != first {
			t.Fatalf("iteration %d: nondeterministic render (err=%v)", i, err)
		}
		again2, err := renderSearchResults([]byte(collide))
		if err != nil || again2 != second {
			t.Fatalf("iteration %d: nondeterministic alias render (err=%v)", i, err)
		}
	}
}

// Escaped-form masking (R2): on a transport error the error text quotes the
// URL-escaped substitution, so the seeds must include the escaped forms of
// the query. A key-shaped query containing "/" renders [REDACTED] in the
// persisted log entry AND in the model-visible result, in neither the raw
// nor the escaped form.
func TestToolRunnerSearchWebTransportErrorMasking(t *testing.T) {
	t.Setenv("SEARCH_TRANSPORT_KEY", "transport-stub-key")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	// Nothing listens on 127.0.0.1:1: the call fails at the transport layer
	// with the escaped request URL in the error text.
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/s?q={query}", KeyEnv: "SEARCH_TRANSPORT_KEY"}
	out, err := runner.Execute(5, "model-x", "prov-x", "search_web", `{"query":"token `+searchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	escaped := url.QueryEscape(searchDummySecret)
	if strings.Contains(out, searchDummySecret) || strings.Contains(out, escaped) {
		t.Errorf("model-visible result leaks the raw or escaped dummy: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("model-visible result carries no redaction marker: %q", out)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, searchDummySecret) || strings.Contains(log, escaped) {
		t.Errorf("tool log leaks the raw or escaped dummy:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
}

// Output-sink masking (R3, G3): a stub that reflects the query and the
// resolved key VALUE into its JSON body would persist them raw in the log's
// Output field. The QUERY and the key VALUE carry DIFFERENT key-shaped
// dummies, so deleting the key-value seed operand (or the query seed) fails
// this pin; both render [REDACTED] on disk while the benign result fields
// stay readable.
func TestToolRunnerSearchWebOutputReflection(t *testing.T) {
	t.Setenv("SEARCH_REFLECT_KEY", searchDummySecret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body := `{"results":[{"title":"echo ` + key + `","url":"https://x.example/1","snippet":"query was ` + r.URL.Query().Get("q") + `"}]}`
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}", KeyEnv: "SEARCH_REFLECT_KEY"}
	out, err := runner.Execute(6, "model-x", "prov-x", "search_web", `{"query":"token `+searchDummySecretQuery+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, searchDummySecret) || strings.Contains(out, searchDummySecretQuery) {
		t.Errorf("model-visible result leaks a reflected dummy: %q", out)
	}

	assertLogMasked(t, logPath, searchDummySecret, searchDummySecretQuery)
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "https://x.example/1") {
		t.Errorf("tool log lost the benign result fields:\n%s", data)
	}
}

// Endpoint component seeding (R9): a key-shaped value inside an endpoint
// query parameter is component-parsed like fetchLogSeeds (the whole template
// is location-shaped and SeedEligible-exempt), so the transport-error text
// and the log render it [REDACTED] instead of letting it ride raw.
func TestToolRunnerSearchWebEndpointParamSeeded(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/s?appid=" + searchDummySecret + "&q={query}"}
	out, err := runner.Execute(7, "model-x", "prov-x", "search_web", `{"query":"plain query"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, searchDummySecret) {
		t.Errorf("model-visible result leaks the endpoint key parameter: %q", out)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, searchDummySecret) {
		t.Errorf("tool log leaks the endpoint key parameter:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
	// Over-seeding cost (round-5 design): whole operands seed
	// unconditionally, so the benign query renders [REDACTED] in the
	// Arguments field; the benign rendered results above stay readable.
	if strings.Contains(log, "plain query") {
		t.Errorf("the benign query should be masked as a whole operand:\n%s", log)
	}
}

// Malformed-arguments and model-tools-refusal seeding (R13): the operands
// seed the log regardless of the typed-unmarshal outcome, so a key-shaped
// dummy inside a truncated arguments string renders [REDACTED] in the
// persisted entry, and so does a call refused by the model-tools filter.
func TestToolRunnerSearchWebUnmarshalIndependentMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)

	// Truncated JSON: the typed unmarshal fails; the raw operands still seed.
	out, err := runner.Execute(8, "model-x", "prov-x", "search_web", `{"query":"token `+searchDummySecret)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid arguments") {
		t.Errorf("result = %q; want the malformed-arguments error", out)
	}

	// Model-tools refusal: the tool is not in the seat's allowed list; the
	// refusal logs the seeded arguments too.
	runner.AllowedModelTools = []string{"read_file"}
	out, err = runner.Execute(9, "model-x", "prov-x", "search_web", `{"query":"token `+searchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "not in the allowed tools list") {
		t.Errorf("result = %q; want the model-tools refusal", out)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, searchDummySecret) {
		t.Errorf("tool log leaks the key-shaped dummy on the unmarshal-independent paths:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
	if !strings.Contains(log, "invalid arguments") || !strings.Contains(log, "not in the allowed tools list") {
		t.Errorf("tool log lost the refusal entries:\n%s", log)
	}
}

// Empty-seed redaction (S-4/F2, round-13 residual): the search_web redactor is
// applied UNCONDITIONALLY, so the generic credential-shape arms still run on a
// call whose arguments seed nothing. The native fallback is the reachable case:
// the answer is CHILD-PRODUCED text that can quote a page carrying an
// `Authorization: Bearer ...` line, while `{}` arguments seed no operand at all.
// The old `if len(seeds) > 0` guard skipped every arm there, leaving such a line
// raw in the model-visible result and error text (the audit log kept the generic
// arms only because NewLogger defaults to them). Restoring that guard fails this
// pin.
func TestToolRunnerSearchWebEmptySeedStillRedacts(t *testing.T) {
	const bearerLine = "Authorization: Bearer xyzSearchChildLeak0001"
	const bearerToken = "xyzSearchChildLeak0001"

	// Premise, asserted so the pin cannot go quiet: these arguments carry no
	// seedable operand, so the redactor is reached with an EMPTY seed list. If a
	// future seed-builder change makes `{}` seed, re-derive another empty-seed
	// shape instead of deleting the pin.
	if seeds := searchLogSeeds(`{}`, SearchSource{}, ""); len(seeds) != 0 {
		t.Fatalf("premise broken: `{}` seeds %d value(s) (%q); the empty-seed argument shape needs re-deriving", len(seeds), seeds)
	}

	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	// Answer path: the child answers with the bearer line inside its prose. The
	// source has neither an endpoint nor a key env var (the native route).
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{NativeSearch: func(string) (string, error) {
		return "- finding one\n" + bearerLine + "\n- finding two", nil
	}}
	out, err := runner.Execute(11, "model-x", "prov-x", "search_web", `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, bearerToken) {
		t.Errorf("model-visible result leaks the child's bearer line: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("model-visible result carries no redaction marker: %q", out)
	}

	// Error path: the child fails with the same line in its error text, which
	// reaches the model as the tool result's Error sentence.
	runner.SearchSource = SearchSource{NativeSearch: func(string) (string, error) {
		return "", fmt.Errorf("real agy exited 1: %s", bearerLine)
	}}
	out, err = runner.Execute(12, "model-x", "prov-x", "search_web", `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "native agy fallback failed") {
		t.Errorf("result = %q; want the native-failure sentence", out)
	}
	if strings.Contains(out, bearerToken) {
		t.Errorf("model-visible error leaks the child's bearer line: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("model-visible error carries no redaction marker: %q", out)
	}

	assertLogMasked(t, logPath, bearerToken)
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "native agy fallback failed") {
		t.Errorf("tool log lost the native-failure entry:\n%s", data)
	}
}

// assertLogMasked asserts the persisted tool log carries [REDACTED] and none
// of the given key-shaped dummies (shared by the search and fetch pins).
func assertLogMasked(t *testing.T, logPath string, dummies ...string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(data)
	for _, d := range dummies {
		if strings.Contains(log, d) {
			t.Errorf("tool log leaks %q:\n%s", d, log)
		}
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
}

// roundTripFunc adapts a function to http.RoundTripper (used to prove a
// refusal happens before any network use).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// G1: path-less templates must not skip the placement refusal. The authority
// region ends at "/", "?", "#", or end-of-string; every shape below is
// refused BEFORE any network use (the injected transport records whether it
// was dialed at all).
func TestSearchWebPathLessAuthorityRefused(t *testing.T) {
	t.Setenv("SEARCH_G1_KEY", "g1-stub-key")
	cases := []string{
		"https://{query}",
		"https://{query}?x=1",
		"https://{query}:8443",
		"https://host:{query}?q=1",
	}
	for _, endpoint := range cases {
		var called bool
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("must not dial")
		})}
		_, err := searchWeb("attacker.example", SearchSource{Endpoint: endpoint, KeyEnv: "SEARCH_G1_KEY"}, client)
		if err == nil || !strings.Contains(err.Error(), "authority") {
			t.Errorf("endpoint %q: err = %v; want the authority refusal", endpoint, err)
		}
		if called {
			t.Errorf("endpoint %q: the transport was dialed; the refusal must happen before any network use", endpoint)
		}
	}
}

// G1 fail-closed backstop: a template that passes the placement check but
// cannot be parsed for the host-invariance comparison is refused, never
// silently skipped. The "%2{query}" shape is unparseable before substitution
// ("%2{") and valid after ("%20"), the reachable shape for this branch.
func TestSearchWebHostBackstopFailsClosed(t *testing.T) {
	t.Setenv("SEARCH_G1_KEY", "g1-stub-key")
	var called bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not dial")
	})}
	_, err := searchWeb("0", SearchSource{Endpoint: "https://h.example/a%2{query}b?q=x", KeyEnv: "SEARCH_G1_KEY"}, client)
	if err == nil || !strings.Contains(err.Error(), "host-invariance") {
		t.Errorf("err = %v; want the fail-closed host-invariance refusal", err)
	}
	if called {
		t.Error("the transport was dialed; the refusal must happen before any network use")
	}

	// The backstop itself: a parseable template with a moved host is refused,
	// an equal host passes, an unparseable template fails closed.
	moved, err := url.Parse("https://other.example/s?q=x")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSearchHostInvariance("https://h.example/s?q={query}", moved); err == nil {
		t.Error("moved host: want the host-invariance refusal")
	}
	same, err := url.Parse("https://h.example/s?q=x")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSearchHostInvariance("https://h.example/s?q={query}", same); err != nil {
		t.Errorf("same host: err = %v; want nil", err)
	}
	if err := checkSearchHostInvariance("https://{query}/s", same); err == nil {
		t.Error("unparseable template: want the fail-closed refusal")
	}
}

// G4: the redirect host-invariance rule compares hostnames case-
// insensitively and ports with DEFAULT-PORT EQUIVALENCE (80 for http, 443
// for https): a default-port normalization is allowed in both directions, a
// real port move or a hostname move is still refused.
func TestSearchWebRedirectHostNormalization(t *testing.T) {
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", raw, err)
		}
		return req
	}
	// https://x:443 == https://x, both directions.
	policy := searchRedirectCheck("api.example.com:443")
	if err := policy(mkReq("https://api.example.com/y"), nil); err != nil {
		t.Errorf("explicit 443 vs default: err = %v; want nil (allowed)", err)
	}
	policyBare := searchRedirectCheck("api.example.com")
	if err := policyBare(mkReq("https://api.example.com:443/y"), nil); err != nil {
		t.Errorf("bare host vs explicit 443: err = %v; want nil (allowed)", err)
	}
	// A real port move is still refused.
	if err := policy(mkReq("https://api.example.com:8443/y"), nil); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("port move: err = %v; want the host-invariance refusal", err)
	}
	// A hostname move carrying the default port is still refused.
	if err := policyBare(mkReq("https://other.example.com:443/y"), nil); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("host move: err = %v; want the host-invariance refusal", err)
	}
	// http default-port equivalence, allowed and refused.
	policyHTTP := searchRedirectCheck("127.0.0.1:80")
	if err := policyHTTP(mkReq("http://127.0.0.1/x"), nil); err != nil {
		t.Errorf("http :80 vs default: err = %v; want nil (allowed)", err)
	}
	if err := policyHTTP(mkReq("http://127.0.0.1:8080/x"), nil); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("http real port move: err = %v; want the host-invariance refusal", err)
	}
}

// G2a: a URL-shaped query operand is location-shaped as a whole and
// SeedEligible-exempt; its own components must seed the log.
func TestToolRunnerSearchWebURLOperandMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(1, "model-x", "prov-x", "search_web",
		`{"query":"https://host.example/?token=`+searchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// G2b: a non-string operand slot still seeds the log.
func TestToolRunnerSearchWebNestedOperandMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	out, err := runner.Execute(2, "model-x", "prov-x", "search_web",
		`{"query":["token `+searchDummySecret+`"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid arguments") {
		t.Errorf("result = %q; want the malformed-shape error", out)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// G6: the render cap cuts at a rune boundary; the truncated output stays
// valid UTF-8 and keeps the note, on both the fallback path and the
// item-render path.
func TestSearchWebRenderTruncationRuneBoundary(t *testing.T) {
	const note = "... (render truncated at 64 KiB)"
	// Three-byte runes: the byte cap is not rune-aligned, so a naive slice
	// would split a sequence.
	long := strings.Repeat("€", maxSearchRenderBytes)

	fallback, err := renderSearchResults([]byte(`{"note":"` + long + `"}`))
	if err != nil {
		t.Fatalf("renderSearchResults (fallback): %v", err)
	}
	if !utf8.ValidString(fallback) {
		t.Error("fallback render is not valid UTF-8 (a rune was split)")
	}
	if !strings.HasSuffix(fallback, note) {
		t.Errorf("fallback render lost the truncation note: %q", fallback[len(fallback)-60:])
	}

	items := renderResultItems([]map[string]any{{"title": long, "url": "https://x.example/1"}})
	if !utf8.ValidString(items) {
		t.Error("item render is not valid UTF-8 (a rune was split)")
	}
	if !strings.HasSuffix(items, note) {
		t.Errorf("item render lost the truncation note: %q", items[len(items)-60:])
	}
}

// H1: a key-shaped run in a URL PATH operand seeds through the component
// collector (the whole operand is location-shaped and exempt).
func TestToolRunnerSearchWebPathTokenMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(1, "model-x", "prov-x", "search_web",
		`{"query":"https://host.example/files/`+searchDummySecret+`/dl"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H1: a key-shaped run in a URL FRAGMENT operand seeds too.
func TestToolRunnerSearchWebFragmentTokenMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(2, "model-x", "prov-x", "search_web",
		`{"query":"https://host.example/x#section=`+searchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H1: a key-shaped run in a URL HOST operand seeds.
func TestToolRunnerSearchWebHostTokenMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(3, "model-x", "prov-x", "search_web",
		`{"query":"https://`+searchDummySecret+`.host.example/x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H2 end-to-end through the real runner+Logger, the reviewer probe shape: a
// MALFORMED arguments string carrying a scheme-relative URL
// ("//host/?token=<secret>"). jsonTokenOperands splits on ':', so no token
// can contain "://", and the surviving "//host/..." token is location-shaped
// and SeedEligible-exempt; only the rawURLInArgs recovery seeds it.
func TestToolRunnerSearchWebSchemeRelativeURLOperandMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	out, err := runner.Execute(4, "model-x", "prov-x", "search_web",
		`{"query":"see //host.example/?token=`+searchDummySecret+`",`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid arguments") {
		t.Errorf("result = %q; want the malformed-arguments error", out)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H5b: JSON object KEYS are operands too; a key-shaped key renders
// [REDACTED].
func TestToolRunnerSearchWebObjectKeyMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(5, "model-x", "prov-x", "search_web",
		`{"token `+searchDummySecret+`":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H5c: an operand written with JSON backslash-u escapes seeds in the form
// the log records: the raw arguments text contributes the escaped token,
// the decoded value contributes the decoded form.
func TestToolRunnerSearchWebJSONEscapedOperandMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(6, "model-x", "prov-x", "search_web",
		`{"query":"token wJalrXUtnFEMI\u002fK7MDENG\u002fbPxRfiCYEXAMPLEKEY"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, "wJalrXUtnFEMI")
}

// H4: the redirect scheme is fixed. An https-implicit-443 to
// http-implicit-80 loopback same-host hop is refused (each side normalizes
// its port under its OWN scheme now), while a same-scheme default-port
// normalization still passes (pinned in
// TestSearchWebRedirectHostNormalization).
func TestSearchWebRedirectCrossSchemeRefused(t *testing.T) {
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", raw, err)
		}
		return req
	}
	policy := searchRedirectCheck("https://127.0.0.1")
	if err := policy(mkReq("http://127.0.0.1/x"), nil); err == nil ||
		!strings.Contains(err.Error(), "scheme") {
		t.Errorf("cross-scheme hop: err = %v; want the scheme refusal", err)
	}
	// Same-scheme normalization still passes.
	if err := policy(mkReq("https://127.0.0.1/y"), nil); err != nil {
		t.Errorf("same-scheme target: err = %v; want nil (allowed)", err)
	}
}

// H6a: the redirect hostname comparison is case-insensitive.
func TestSearchWebRedirectHostnameCaseInsensitive(t *testing.T) {
	policy := searchRedirectCheck("https://API.EXAMPLE.COM")
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy(req, nil); err != nil {
		t.Errorf("case-differing hostname: err = %v; want nil (allowed)", err)
	}
}

// H6b: the search unparseable-endpoint seed branch, pinned at runner level:
// the template cannot be parsed, the matching error quotes the substituted
// URL verbatim, and the endpoint's punctuation-split forms must seed.
func TestToolRunnerSearchWebUnparseableEndpointMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "https://%zz.example/s?q={query}&appid=" + searchDummySecret}
	out, err := runner.Execute(7, "model-x", "prov-x", "search_web", `{"query":"plain"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "not a valid url") {
		t.Errorf("result = %q; want the invalid-url error quoting the endpoint", out)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// H6c: the redirect policy is installed on INJECTED clients too. A bare
// injected client must refuse a cross-host redirect (before this fix only
// the nil-client production path carried the policy).
func TestSearchWebRedirectPolicyOnInjectedClient(t *testing.T) {
	var otherHits int
	var mu sync.Mutex
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchJSONBody)
	}))
	t.Cleanup(other.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/c0":
			fmt.Fprint(w, searchJSONBody)
		case "/rd":
			http.Redirect(w, r, other.URL+"/c0", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/c0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	source := func(path string) SearchSource {
		return SearchSource{Endpoint: srv.URL + path + "?q={query}"}
	}

	// Cross-host redirect with an injected bare client: refused, and the
	// other stub is never dialed.
	if _, err := searchWeb("q", source("/rd"), &http.Client{}); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("injected client cross-host redirect: err = %v; want the host-invariance refusal", err)
	}
	mu.Lock()
	if otherHits != 0 {
		t.Errorf("the cross-host stub was dialed %d times; the redirect must be refused before any network use", otherHits)
	}
	mu.Unlock()

	// Same-host redirect with an injected bare client: still followed.
	if out, err := searchWeb("q", source("/hop"), &http.Client{}); err != nil ||
		!strings.Contains(out, "Doc One") {
		t.Errorf("injected client same-host redirect: out=%q err=%v; want the rendered results", out, err)
	}
}

// H7: the resolved key VALUE seeds UNCONDITIONALLY (not through
// SeedEligible), so even a BENIGN-shaped key reflected by the configured
// backend is masked in the log's Output field, while benign result fields
// stay readable.
func TestToolRunnerSearchWebBenignKeyValueMasked(t *testing.T) {
	const benignKey = "totally-benign-key-value"
	t.Setenv("SEARCH_BENIGN_KEY", benignKey)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body := `{"results":[{"title":"echo ` + key + `","url":"https://x.example/1","snippet":"s"}]}`
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}", KeyEnv: "SEARCH_BENIGN_KEY"}
	if _, err := runner.Execute(8, "model-x", "prov-x", "search_web", `{"query":"plain query"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, benignKey)
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "https://x.example/1") {
		t.Errorf("tool log lost the benign result fields:\n%s", data)
	}
}

// H1 (arm-isolating): a key-shaped run in the endpoint template's PATH seeds
// through the endpoint component collection. The arguments carry no URL, so
// the raw-arguments token arm cannot fire here: only the endpoint's
// path/host/fragment collection can mask the transport-error quote. (The
// args-carried path/fragment/host pins above pin the class end to end; this
// pin isolates the component arm that the mutation evidence removes.)
func TestToolRunnerSearchWebEndpointPathTokenMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/files/" + searchDummySecret + "?q={query}"}
	if _, err := runner.Execute(9, "model-x", "prov-x", "search_web", `{"query":"plain"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// secretRunRe is K1's fragment-level standard: a contiguous credential-
// shaped run (16+ base64 alphabet runes) must not survive ANYWHERE in the
// persisted log entry, not only as the full dummy string.
var secretRunRe = regexp.MustCompile(`[A-Za-z0-9+/=]{16,}`)

// assertNoSecretRun asserts K1's fragment-level standard on a log text.
func assertNoSecretRun(t *testing.T, log string) {
	t.Helper()
	if m := secretRunRe.FindString(log); m != "" {
		t.Errorf("a key-shaped run survives in the log: %q\n%s", m, log)
	}
}

// K1 composite pin (search): adjacent sub-floor fragments of ONE credential
// (comma-separated, every run below the classifier floors) persist raw when
// only split tokens seed. Whole operands seed unconditionally now, so no
// fragment of the composite may survive.
func TestToolRunnerSearchWebCompositeMasking(t *testing.T) {
	const composite = "ab,cd,ef,gh,ij,kl,mn,op,qr,st,uv,wx,yz,01,23,45,67"
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(1, "model-x", "prov-x", "search_web",
		`{"query":"token `+composite+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, composite) {
		t.Errorf("the full composite survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// K3 (unit): the relative-marker-stripped core is seeded whole, so a
// credential glued behind "./", "../", ".\" or "..\" cannot ride the
// SeedEligible location exemption. Whole-operand seeding masks the marked
// form transitively; the stripped core is the defense-in-depth arm.
func TestStripRelativeMarkerSeedsCore(t *testing.T) {
	const run = testfixtures.AWSSecretNoSlash
	for _, marked := range []string{"./" + run, "../" + run, ".\\" + run, "..\\" + run} {
		seeds := networkSeeds(appendNetworkCores([]string{marked}), nil)
		found := false
		for _, s := range seeds {
			if s == run {
				found = true
			}
		}
		if !found {
			t.Errorf("operand %q: the stripped core %q is not seeded", marked, run)
		}
	}
	if got := stripRelativeMarker(run); got != run {
		t.Errorf("stripRelativeMarker(%q) = %q; want the operand unchanged", run, got)
	}
}

// K3 end to end: a backslash-marked operand ("..\\<run>") is a single glued
// token (no splitter boundary) and location-exempt; the marked form still
// renders [REDACTED] and no key-shaped run survives.
func TestToolRunnerSearchWebRelativeMarkerMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(2, "model-x", "prov-x", "search_web",
		`{"query":"..\\`+testfixtures.AWSSecretNoSlash+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), testfixtures.AWSSecretNoSlash) {
		t.Errorf("the marked credential survives in the log:\n%s", data)
	}
	assertNoSecretRun(t, string(data))
}

// K5 (unit): the scheme/host refusals echo the ORIGINAL-CASE hostname; the
// folded spelling would evade the exact-substring masking of the seeds.
func TestValidateSearchURLEchoesOriginalCase(t *testing.T) {
	const mixed = "WjAlRXUtNFEMiK7MdENGbPxRFiCYEXAMPLEKEY.api.example"
	u, err := url.Parse("http://" + mixed + "/x")
	if err != nil {
		t.Fatal(err)
	}
	err = validateSearchURL(u)
	if err == nil {
		t.Fatal("want the http non-loopback refusal")
	}
	if !strings.Contains(err.Error(), mixed) {
		t.Errorf("refusal folded the hostname: %v", err)
	}
	if folded := strings.ToLower(mixed); folded != mixed && strings.Contains(err.Error(), folded) {
		t.Errorf("refusal echoes the folded hostname: %v", err)
	}
}

// K5 end to end: a mixed-case key-shaped host run renders [REDACTED] in the
// log Error field, in its original case AND folded (the lower/upper host
// variants seed too).
func TestToolRunnerSearchWebMixedCaseHostMasking(t *testing.T) {
	const mixed = "WjAlRXUtNFEMiK7MdENGbPxRFiCYEXAMPLEKEY.api.example"
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://" + mixed + "/s?q={query}"}
	if _, err := runner.Execute(3, "model-x", "prov-x", "search_web", `{"query":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, mixed) || strings.Contains(log, strings.ToLower(mixed)) {
		t.Errorf("the mixed-case host run survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// K6 (unit, re-pointed at the live arm in round 7): the re-encoded
// backslash-u spelling of an eligible decoded value seeds, so a log line
// that recorded the escaped form of a lowercase key-shaped value still
// masks. (The exact escaped spelling arriving in the raw arguments is
// covered by the whole raw-text seed; this arm covers the re-encoding
// direction.)
func TestExpandNetworkWholeReencodesEligibleDecoded(t *testing.T) {
	const decoded = "mjalrxutnfemi/k7mdengbpxrficyexamplekey01"
	cands := expandNetworkWhole(decoded)
	if !containsString(cands, jsonReencode(decoded)) {
		t.Errorf("expandNetworkWhole(%q) lacks the re-encoded spelling %q: %v", decoded, jsonReencode(decoded), cands)
	}
	// The escaped-operand direction: the JSON-unescaped decoded form is
	// offered, and its re-encoded spelling too.
	escaped := `mjalrxutnfemi\u002fk7mdengbpxrficyexamplekey01`
	cands = expandNetworkWhole(escaped)
	if !containsString(cands, decoded) {
		t.Errorf("expandNetworkWhole(%q) lacks the decoded spelling %q: %v", escaped, decoded, cands)
	}
}

// K6 end to end: the JSON-escaped spelling of a LOWERCASE key-shaped value
// (no uppercase rune, one backslash-u escape, fails the classifier floors in
// escaped form) renders [REDACTED] in the persisted log.
func TestToolRunnerSearchWebLowercaseEscapedMasking(t *testing.T) {
	srv, _, _ := searchStub(t, searchJSONBody)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(4, "model-x", "prov-x", "search_web",
		`{"query":"token mjalrxutnfemi\u002fk7mdengbpxrficyexamplekey01"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, "mjalrxutnfemi") {
		t.Errorf("the lowercase escaped value survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// K7: an endpoint embedding a credential whose punctuation-fragment runs are
// each under the classifier floors (percent-fragmented lowercase composite)
// persists the FULL value raw in the substituted-URL error quote; the whole
// operand set (template and query-substituted spelling, unconditional) is
// what masks it. The eligible-gated set is empty for this shape.
func TestToolRunnerSearchWebSubFloorEndpointComposite(t *testing.T) {
	const composite = "aa%62%63%64%65%66%67%68%69%6a%6b%6c%6d%6e%6f%70%71%72%73%74%75%76%77%78%79%7a%30%31%32%33"
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/s?k=" + composite + "&q={query}"}
	if _, err := runner.Execute(5, "model-x", "prov-x", "search_web", `{"query":"plain"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, composite) {
		t.Errorf("the sub-floor composite survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// K2 (search endpoint arm): a static endpoint query parameter seeded in
// percent-encoded form also seeds its DECODED spelling, so a backend
// reflecting the decoded parameter masks in the log Output.
func TestToolRunnerSearchWebEndpointDecodedParamMasking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"results":[{"title":"echo ` + r.URL.Query().Get("appid") + `","url":"https://x.example/1","snippet":"s"}]}`
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?appid=" + url.QueryEscape(searchDummySecret) + "&q={query}"}
	if _, err := runner.Execute(6, "model-x", "prov-x", "search_web", `{"query":"plain"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, searchDummySecret)
}

// K4 (unit): opaque and scheme positions are collected spellings of a parsed
// URL, so a paste in either position seeds through the shared rule.
// K4 (unit, re-pointed at the live arm in round 7): a credential paste in
// the scheme or opaque position is covered by the live pipeline (whole
// operand + the N2 truncation ladder): a scheme echo of the paste is masked
// down to a sub-floor tail.
func TestBuildNetworkSeedsCoversSchemeAndOpaquePaste(t *testing.T) {
	const run = testfixtures.AWSSecretNoSlash
	for _, op := range []string{run + "://host.example/x", "http:" + run} {
		seeds := buildNetworkSeeds(nil, []string{op}, "", nil)
		echo := "unsupported scheme " + run
		if !strings.HasPrefix(op, run+"://") { // opaque form: quotes the URL
			echo = "invalid url " + op
		}
		redacted := providers.NewRedactor(seeds...)(echo)
		if m := secretRunRe.FindString(redacted); m != "" {
			t.Errorf("operand %q: the scheme/opaque run survives the echo: %q", op, m)
		}
	}
}

// The spelling-complete design record (rounds 5-7, design items 1-5): whole
// operands seed unconditionally even when benign; the URL component
// spellings (host case variants, decoded query values) ride the component
// tier; decoded spellings of encoded operands are offered.
func TestNetworkSeedsSpellingComplete(t *testing.T) {
	// Item 1: T itself, unconditionally, even when benign.
	seeds := networkSeeds([]string{"benign words"}, nil)
	found := false
	for _, s := range seeds {
		if s == "benign words" {
			found = true
		}
	}
	if !found {
		t.Error("design item 1: the whole operand T must seed unconditionally")
	}
	// Item 4: host case variants and component spellings of a parseable URL.
	seeds = buildNetworkSeeds(nil, []string{"https://Host.Example/a%2Fb?q=xk%2Fyk#f%2Fg"}, "", nil)
	for _, want := range []string{"Host.Example", "host.example", "HOST.EXAMPLE", "xk/yk"} {
		if !containsString(seeds, want) {
			t.Errorf("design item 4: operand spellings lack %q: %v", want, seeds)
		}
	}
	// Item 3: decoded spelling of an encoded operand.
	seeds = buildNetworkSeeds(nil, []string{"aa%2Fbb"}, "", nil)
	if !containsString(seeds, "aa/bb") {
		t.Errorf("design item 3: decoded spelling missing: %v", seeds)
	}
}

// N4: isolating pins for the live arms (single-arm removal must fail the
// pin). Each input is chosen so the target element is produced by exactly
// one arm.
func TestNetworkArmIsolationPins(t *testing.T) {
	const run = testfixtures.AWSSecretNoSlash
	cases := []struct {
		name  string
		texts []string
		want  []string
	}{
		// QueryEscape arm (M1): the generated escaped spelling of a value
		// with a space exists only in that arm.
		{"queryescape arm", []string{"ab cd token"}, []string{"ab+cd+token"}},
		// Host case variants (M1/M7): the folded host spellings exist only
		// in the urlWholeSpellings host block.
		{"host variants arm", []string{"https://Host.Example/x"}, []string{"host.example", "HOST.EXAMPLE"}},
		// Decoded + core arms (M2): the decoded spelling and its stripped
		// core exist only in the decoded arm.
		{"decoded core arms", []string{"..%2F" + run}, []string{"../" + run, run}},
		// jsonReencode arm (K6): the re-encoded spelling of an eligible run.
		{"reencode arm", []string{run}, []string{jsonReencode(run)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seeds := buildNetworkSeeds(nil, tc.texts, "", nil)
			for _, want := range tc.want {
				if !containsString(seeds, want) {
					t.Errorf("arm pin: seeds lack %q (arm wiring deleted?): %v", want, seeds)
				}
			}
		})
	}
}

// N4: the endpoint-template arm and the verbatim arguments-text arm
// (search collector): each seeds exactly its element.
func TestSearchLogSeedsArmIsolation(t *testing.T) {
	const endpoint = "https://api.example/s?q={query}"
	args := `{"query":"hello"}`
	seeds := searchLogSeeds(args, SearchSource{Endpoint: endpoint}, "")
	for _, want := range []string{endpoint, args} {
		if !containsString(seeds, want) {
			t.Errorf("seeds lack %q (arm wiring deleted?)", want)
		}
	}
	// The endpoint arm is source-dependent: without a source it contributes
	// nothing.
	seeds = searchLogSeeds(args, SearchSource{}, "")
	if containsString(seeds, endpoint) {
		t.Errorf("the endpoint element seeds without a configured source")
	}
}

// N4: the raw-URL-recovery wiring in BOTH collectors (H2) and the recovered
// URL element: single-arm removal fails the pin.
func TestRawURLRecoveryArmIsolation(t *testing.T) {
	const want = "//host.example/?token=" + testfixtures.AWSSecretExample
	malformed := `{"query":"see ` + want + `",`
	if got := recoverRawURLCandidates(malformed); len(got) != 1 || got[0] != want {
		t.Fatalf("recoverRawURLCandidates = %v; want [%q]", got, want)
	}
	if seeds := fetchArgSeeds(malformed); !containsString(seeds, want) {
		t.Errorf("fetchArgSeeds lacks the recovered URL element (recovery wiring deleted?)")
	}
	if seeds := searchLogSeeds(malformed, SearchSource{}, ""); !containsString(seeds, want) {
		t.Errorf("searchLogSeeds lacks the recovered URL element (recovery wiring deleted?)")
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// M1 (search): an all-lowercase key-shaped HOST label dot-glued to a suffix
// is vetoed by the eligibility rule (no uppercase rune, dot-broken run) in
// every variant; the scheme-refusal echo of the hostname renders [REDACTED]
// because the host spellings (original/lower/upper) seed unconditionally.
func TestToolRunnerSearchWebLowercaseHostMasking(t *testing.T) {
	// 50 runes: longer than the 32-rune template prefix (7 for "http://" +
	// 25 of the label), so the M6 prefix arm alone cannot mask the echo and
	// the pin isolates the unconditional host spellings (mutation (a)).
	const host = "mjalrxutnfemik7mdengbpxrficyexamplekey0123456789abcd.api.example"
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://" + host + "/s?q={query}"}
	if _, err := runner.Execute(1, "model-x", "prov-x", "search_web", `{"query":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	assertNoSecretRun(t, string(data))
}

// M1 (escaped-query arm): the tool-generated QueryEscape spelling of a
// lowercase-only query seeds unconditionally, so the transport-error quote
// of the escaped substitution renders [REDACTED].
func TestToolRunnerSearchWebLowercaseEscapedQueryMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/s?q={query}"}
	if _, err := runner.Execute(2, "model-x", "prov-x", "search_web",
		`{"query":"token mjalrxutnfemik7mdengbpxrficyexamplekey01"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	assertNoSecretRun(t, string(data))
}

// M3: {} and {"query":null} unmarshal the query to "" and the call runs; the
// verbatim refusal errors quote the SUBSTITUTED URL, so the substituted
// spelling must seed for every query shape, not only JSON strings.
func TestToolRunnerSearchWebSubstitutedEndpointEmptyQuery(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"absent query", `{}`},
		{"null query", `{"query":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "fanout.jsonl")
			logger, err := NewLogger(logPath, nil)
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			runner := NewRunner(root, false, false, logger)
			runner.SearchSource = SearchSource{Endpoint: "http://127.0.0.1:1/s?appid=" + searchDummySecret + "&q={query}"}
			out, err := runner.Execute(3, "model-x", "prov-x", "search_web", tc.args)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(out, "not a valid url") && !strings.Contains(out, "search failed") {
				t.Errorf("result = %q; want the substituted-URL error shape", out)
			}
			assertLogMasked(t, logPath, searchDummySecret)
		})
	}
}

// M4: the fixed schema key "query" rides the eligibility rule (like the
// fetch side), so the word "query" survives in a benign search result while
// a key-shaped object key still renders [REDACTED].
func TestToolRunnerSearchWebSchemaKeySurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"query echo","url":"https://x.example/1","snippet":"s"}]}`)
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(4, "model-x", "prov-x", "search_web",
		`{"query":"hello","token `+searchDummySecret+`":"x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if !strings.Contains(log, "query echo") {
		t.Errorf("the word \"query\" must survive in the benign result:\n%s", log)
	}
	if strings.Contains(log, searchDummySecret) {
		t.Errorf("the key-shaped object key survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// M7: a case-folding backend reflection is masked because the lower and
// upper case variants of every whole operand seed unconditionally.
func TestToolRunnerSearchWebCaseFoldedReflectionMasking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"results":[{"title":"upper ` + strings.ToUpper(r.URL.Query().Get("q")) + `","url":"https://x.example/1","snippet":"s"}]}`
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(5, "model-x", "prov-x", "search_web",
		`{"query":"token `+searchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, strings.ToUpper(searchDummySecret))
}

// M8 (unit): the raw-URL recovery arm, isolated from the whole-arguments
// seed that dominates its end-to-end pins.
func TestRecoverRawURLCandidates(t *testing.T) {
	got := recoverRawURLCandidates(`{"query":"see //host.example/?token=` + testfixtures.AWSSecretExample + `",`)
	if len(got) != 1 || got[0] != "//host.example/?token="+testfixtures.AWSSecretExample {
		t.Errorf("recoverRawURLCandidates = %v; want the trimmed scheme-relative URL", got)
	}
	if got := recoverRawURLCandidates(`{"query":"nothing here"}`); len(got) != 0 {
		t.Errorf("recoverRawURLCandidates = %v; want no candidates", got)
	}
}

// M8 (unit): the object-key arm of the text collector: a key-shaped key
// seeds whole, the fixed schema keys ride the gated list only.
func TestNetworkArgTextsObjectKeys(t *testing.T) {
	whole, gated, _ := networkArgTexts(`{"query":"hello","token ` + testfixtures.AWSSecretExample + `":"x"}`)
	if !containsString(whole, "token "+testfixtures.AWSSecretExample) {
		t.Errorf("the key-shaped key must seed whole: whole=%v", whole)
	}
	if containsString(whole, "query") {
		t.Errorf("the schema key \"query\" must NOT seed whole: whole=%v", whole)
	}
	if !containsString(gated, "query") {
		t.Errorf("the schema key \"query\" must ride the gated list: gated=%v", gated)
	}
}

// M3 (unit): the query-substituted endpoint spelling seeds for EVERY query
// shape (string, null, absent): null and absent unmarshal to "", the call
// runs, and the verbatim refusal errors quote the substituted URL. The
// end-to-end pins are dominated by the static query-pair spellings, so this
// unit pin isolates the arm the mutation evidence removes (mutation (c)).
func TestSearchLogSeedsSubstitutedEndpointEveryQueryShape(t *testing.T) {
	const endpoint = "http://127.0.0.1:1/s?appid=" + testfixtures.AWSSecretExample + "&q={query}"
	cases := []struct {
		name string
		args string
		q    string
	}{
		{"string query", `{"query":"plain"}`, "plain"},
		{"null query", `{"query":null}`, ""},
		{"absent query", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seeds := searchLogSeeds(tc.args, SearchSource{Endpoint: endpoint}, "")
			want := strings.Replace(endpoint, "{query}", url.QueryEscape(tc.q), 1)
			if !containsString(seeds, want) {
				t.Errorf("the substituted endpoint spelling %q is not seeded for %s", want, tc.args)
			}
		})
	}
}

// N1: the resolved key VALUE joins the pipeline BEFORE the build, so every
// transformation arm applies to it. A stub reflecting the key uppercased,
// lowercased, percent-encoded, and cut at 20 runes must render [REDACTED]
// in the log Output and the model-visible result on every form.
func TestToolRunnerSearchWebKeyTransformReflectionMasking(t *testing.T) {
	t.Setenv("SEARCH_KEY_T_KEY", searchDummySecret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body := `{"results":[{"title":"u ` + strings.ToUpper(key) + ` l ` + strings.ToLower(key) +
			`","url":"e ` + url.QueryEscape(key) + `","snippet":"c ` + key[:20] + `"}]}`
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}", KeyEnv: "SEARCH_KEY_T_KEY"}
	out, err := runner.Execute(1, "model-x", "prov-x", "search_web", `{"query":"plain"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, form := range []string{
		strings.ToUpper(searchDummySecret),
		strings.ToLower(searchDummySecret),
		url.QueryEscape(searchDummySecret),
		searchDummySecret[:20],
	} {
		if strings.Contains(out, form) {
			t.Errorf("the model-visible result leaks the key form %q", form)
		}
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, searchDummySecret) {
		t.Errorf("the log leaks the raw key value:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// N2: the truncation ladder (16/20/24/28/32 prefixes plus sliding windows,
// stride 16 width 32) leaves no surviving key-shaped run for ANY survivor
// size at the fetch 512 KiB body cap. The operand travels as a query value
// the stub echoes at the cap boundary; the truncated model-visible body is
// redacted with the call's seed set exactly as the runner does.
func TestTruncationLadderFetchCap(t *testing.T) {
	const operand = testfixtures.AWSSecretLong
	if len(operand) < 70 {
		t.Fatal("operand must cover the 64-rune survivor case")
	}
	for _, survivor := range []int{16, 20, 28, 31, 48, 64} {
		t.Run(fmt.Sprintf("survivor-%d", survivor), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				filler := strings.Repeat("ab,", (maxFetchBodyBytes-survivor)/3+1)[:maxFetchBodyBytes-survivor]
				fmt.Fprint(w, filler+r.URL.Query().Get("token"))
			}))
			t.Cleanup(srv.Close)
			argsJSON := `{"url":"` + srv.URL + `/big?token=` + operand + `"}`
			out, err := ReadURLContent(srv.URL+"/big?token="+operand, []string{stubHost(t, srv)})
			if err != nil {
				t.Fatalf("ReadURLContent: %v", err)
			}
			if !strings.Contains(out, "truncated") {
				t.Fatal("the body was not truncated")
			}
			redacted := providers.NewRedactor(fetchArgSeeds(argsJSON)...)(out)
			if m := secretRunRe.FindString(redacted); m != "" {
				t.Errorf("a %d-rune survivor of the cap cut persists raw: %q", survivor, m)
			}
		})
	}
}

// N2 (search render cap): the same ladder pins at the 64 KiB render cap;
// the operand is the query value the backend echoes inside a giant snippet
// that the renderer cuts at every adversarial offset.
func TestTruncationLadderRenderCap(t *testing.T) {
	const operand = testfixtures.AWSSecretLong
	for _, survivor := range []int{16, 20, 28, 31, 48, 64} {
		t.Run(fmt.Sprintf("survivor-%d", survivor), func(t *testing.T) {
			filler := strings.Repeat("ab,", (maxSearchRenderBytes-3-survivor)/3+1)[:maxSearchRenderBytes-3-survivor]
			rendered := renderResultItems([]map[string]any{{"title": filler + operand}})
			if !strings.Contains(rendered, "render truncated") {
				t.Fatal("the render was not truncated")
			}
			seeds := searchLogSeeds(`{"query":"`+operand+`"}`, SearchSource{Endpoint: "http://127.0.0.1:1/s?q={query}"}, "")
			redacted := providers.NewRedactor(seeds...)(rendered)
			if m := secretRunRe.FindString(redacted); m != "" {
				t.Errorf("a %d-rune survivor of the render cut persists raw: %q", survivor, m)
			}
		})
	}
}

// N2 (log entry): the persisted log entry is run-free for a render-cap cut
// through the real runner+Logger; the operand is the query value echoed at
// a 20-rune survivor offset.
func TestToolRunnerSearchWebRenderCapLogEntry(t *testing.T) {
	const operand = testfixtures.AWSSecretLong
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The render prefix "1. " is 3 bytes; the filler leaves a 20-rune
		// survivor of the echoed query value.
		filler := strings.Repeat("a,", (maxSearchRenderBytes-3-20)/2+1)[:maxSearchRenderBytes-3-20]
		body := `{"results":[{"title":"` + filler + r.URL.Query().Get("q") + `","url":"https://x.example/1"}]}`
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.SearchSource = SearchSource{Endpoint: srv.URL + "/s?q={query}"}
	if _, err := runner.Execute(2, "model-x", "prov-x", "search_web", `{"query":"`+operand+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	assertNoSecretRun(t, string(data))
}
