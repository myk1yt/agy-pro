package agenttools

// B4-2a pins for read_url_content (fetch.go).
//
// Transport pins (CONTRACT section 3.5): GET only (the tool takes no method
// argument), http refused for non-loopback hosts and allowed on loopback,
// host allow-list fail-closed (empty refuses, unlisted refuses,
// case-insensitive, port ignored), redirect cap 3 (4th refused, every
// redirect target re-validated against the allow-list), body cap 512 KiB
// truncated with a note, text content types only (binary and absent refused),
// userinfo refused (no auth material). The audit pin holds the ONE redaction
// discipline: a key-shaped query value in the URL is masked in the on-disk
// tool log through the shared providers.SeedEligible seeding, and both the
// allowed and the refused call are recorded.
//
// Mutation-verified: bypassing hostInAllowList fails the allow-list pins, and
// dropping the fetchArgSeeds block in runner.go fails the audit pin (see the
// unit report).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// fetchDummySecret is the canonical AWS documentation example shape, never a
// real credential (same dummy the scope-refusal pin uses).
const fetchDummySecret = testfixtures.AWSSecretExample

// stubHost renders the loopback hostname of an httptest server (127.0.0.1).
func stubHost(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub url %q: %v", srv.URL, err)
	}
	return u.Hostname()
}

// stubPort renders the TCP port of an httptest server.
func stubPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub url %q: %v", srv.URL, err)
	}
	return u.Port()
}

// textStub serves one fixed text body on loopback.
func textStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The tool forces GET and takes no method argument: a normal fetch works, the
// stub sees GET, and the request carries no cookie or authorization material.
func TestReadURLContentFetchesWithGETOnly(t *testing.T) {
	var mu sync.Mutex
	var gotMethod string
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		gotHeader = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "doc-body")
	}))
	t.Cleanup(srv.Close)

	out, err := ReadURLContent(srv.URL+"/doc", []string{stubHost(t, srv)})
	if err != nil {
		t.Fatalf("ReadURLContent: %v", err)
	}
	if out != "doc-body" {
		t.Errorf("body = %q; want %q", out, "doc-body")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q; want GET (the tool forces GET and takes no method argument)", gotMethod)
	}
	if gotHeader.Get("Cookie") != "" || gotHeader.Get("Authorization") != "" {
		t.Errorf("the request carried auth material: Cookie=%q Authorization=%q", gotHeader.Get("Cookie"), gotHeader.Get("Authorization"))
	}
}

// Scheme rules: http non-loopback refused BEFORE any network use, unsupported
// schemes refused, and the https path works (TLS stub, injected client).
func TestReadURLContentSchemeRules(t *testing.T) {
	// http for a non-loopback host: refused at the scheme gate, no dial.
	if _, err := ReadURLContent("http://example.com/doc", []string{"example.com"}); err == nil ||
		!strings.Contains(err.Error(), "loopback") {
		t.Errorf("http non-loopback: err = %v; want the loopback refusal (no network)", err)
	}
	// Unsupported scheme refused even for a listed host.
	if _, err := ReadURLContent("ftp://example.com/doc", []string{"example.com"}); err == nil ||
		!strings.Contains(err.Error(), "https only") {
		t.Errorf("ftp: err = %v; want the https-only refusal", err)
	}
	// https on a listed host is fetched (TLS test server; the injected client
	// is the only one trusting its certificate).
	tlsSrv := textStub(t, "secure-doc")
	out, err := readURLContent(tlsSrv.URL+"/doc", []string{stubHost(t, tlsSrv)}, tlsSrv.Client())
	if err != nil {
		t.Fatalf("https fetch: %v", err)
	}
	if out != "secure-doc" {
		t.Errorf("https body = %q; want %q", out, "secure-doc")
	}
}

// Allow-list: listed host allowed, unlisted refused, EMPTY list refuses
// (fail closed), match is case-insensitive, and the port is ignored.
func TestReadURLContentAllowList(t *testing.T) {
	srv := textStub(t, "allow-doc")
	host := stubHost(t, srv)

	if out, err := ReadURLContent(srv.URL+"/doc", []string{host}); err != nil || out != "allow-doc" {
		t.Errorf("listed host: out=%q err=%v; want the doc body", out, err)
	}
	if out, err := ReadURLContent(srv.URL+"/doc", []string{"other.dev", host}); err != nil || out != "allow-doc" {
		t.Errorf("listed among others: out=%q err=%v; want the doc body", out, err)
	}
	if _, err := ReadURLContent(srv.URL+"/doc", []string{"other.dev"}); err == nil ||
		!strings.Contains(err.Error(), "allow-list") {
		t.Errorf("unlisted host: err = %v; want the allow-list refusal", err)
	}
	if _, err := ReadURLContent(srv.URL+"/doc", nil); err == nil ||
		!strings.Contains(err.Error(), "allow-list") {
		t.Errorf("empty list: err = %v; want the allow-list refusal (fail closed)", err)
	}

	// Case-insensitive: the entry is uppercase LOCALHOST, the url says
	// localhost, both name the same loopback service.
	localURL := "http://localhost:" + stubPort(t, srv) + "/doc"
	if out, err := ReadURLContent(localURL, []string{"LOCALHOST"}); err != nil || out != "allow-doc" {
		t.Errorf("case-insensitive loopback: out=%q err=%v; want the doc body", out, err)
	}

	// Port ignored: the entry pins a different port than the service uses.
	if out, err := ReadURLContent(srv.URL+"/doc", []string{host + ":1"}); err != nil || out != "allow-doc" {
		t.Errorf("port-ignored entry: out=%q err=%v; want the doc body", out, err)
	}
}

// Redirect cap: a chain of 3 is followed to the final body, a chain of 4 is
// refused, and a redirect onto an unlisted host (even loopback) is refused
// by the per-redirect re-validation.
func TestReadURLContentRedirectCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/c0":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "chain-done")
		case "/c1", "/c2", "/c3", "/c4":
			n := int(r.URL.Path[2] - '0')
			http.Redirect(w, r, fmt.Sprintf("/c%d", n-1), http.StatusFound)
		case "/escape":
			// Same server, but named localhost while the allow-list pins
			// only the 127.0.0.1 spelling: the redirect must not escape.
			http.Redirect(w, r, "http://localhost:"+stubPort(t, srv)+"/c0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host := stubHost(t, srv)

	if out, err := ReadURLContent(srv.URL+"/c3", []string{host}); err != nil || out != "chain-done" {
		t.Errorf("chain of 3: out=%q err=%v; want the final body", out, err)
	}
	if _, err := ReadURLContent(srv.URL+"/c4", []string{host}); err == nil ||
		!strings.Contains(err.Error(), "redirect") {
		t.Errorf("chain of 4: err = %v; want the redirect-cap refusal", err)
	}
	if _, err := ReadURLContent(srv.URL+"/escape", []string{host}); err == nil ||
		!strings.Contains(err.Error(), "allow-list") {
		t.Errorf("redirect escape: err = %v; want the allow-list refusal", err)
	}
}

// Body cap: a body larger than 512 KiB is truncated (the documented choice)
// with a clear note, and the kept prefix is exact.
func TestReadURLContentBodyCapTruncatesWithNote(t *testing.T) {
	payload := strings.Repeat("a", maxFetchBodyBytes) + strings.Repeat("Z", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)

	out, err := ReadURLContent(srv.URL+"/big", []string{stubHost(t, srv)})
	if err != nil {
		t.Fatalf("ReadURLContent: %v", err)
	}
	if !strings.HasPrefix(out, payload[:maxFetchBodyBytes]) {
		t.Errorf("the kept prefix lost bytes (len %d)", len(out))
	}
	note := fmt.Sprintf("\n... (body truncated at %d KiB)", maxFetchBodyBytes/1024)
	if !strings.HasSuffix(out, note) {
		t.Errorf("truncation note = %q; want the pinned note %q", out[len(out)-60:], note)
	}
	if strings.Contains(out, "ZZZZZ") {
		t.Error("bytes beyond the cap leaked into the output")
	}
}

// Content types: text/plain, text/html, application/json and the +xml family
// are accepted; application/octet-stream, application/pdf and an absent
// content type are refused.
func TestReadURLContentContentTypes(t *testing.T) {
	cases := []struct {
		ctype string
		want  bool
	}{
		{"text/plain", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"application/rss+xml", true},
		{"application/octet-stream", false},
		{"application/pdf", false},
	}
	for _, tc := range cases {
		t.Run(tc.ctype, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				fmt.Fprint(w, "typed-doc")
			}))
			t.Cleanup(srv.Close)
			out, err := ReadURLContent(srv.URL+"/doc", []string{stubHost(t, srv)})
			if tc.want {
				if err != nil || out != "typed-doc" {
					t.Errorf("out=%q err=%v; want the doc body", out, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "content type") {
				t.Errorf("err = %v; want the content-type refusal", err)
			}
		})
	}

	// The mediatype rule itself, including the fail-closed empty case (the
	// wire server sniffs a missing header, so the empty case is pinned here).
	for mt, want := range map[string]bool{
		"":                         false,
		"text/csv":                 true,
		"application/problem+json": true,
		"application/xhtml+xml":    true,
		"image/svg+xml":            true,
		"application/zip":          false,
		"application/octet-stream": false,
	} {
		if got := fetchContentTypeAllowed(mt); got != want {
			t.Errorf("fetchContentTypeAllowed(%q) = %v; want %v", mt, got, want)
		}
	}
}

// No auth material: a userinfo part in the URL is refused outright, before
// any network use.
func TestReadURLContentRefusesUserInfo(t *testing.T) {
	_, err := ReadURLContent("http://user:pass@127.0.0.1:1/x", []string{"127.0.0.1"})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("err = %v; want the credentials refusal", err)
	}
}

// Audit pin (E2 discipline): both the allowed and the refused call go through
// the ONE redacting tool log; a key-shaped query value in the URL is masked
// on disk through the shared providers.SeedEligible seeding (the log's
// generic redactor alone does NOT cover the bare long token).
func TestToolRunnerReadURLContentAuditLog(t *testing.T) {
	srv := textStub(t, "audit-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}

	// Allowed call: the host is listed, the fetch succeeds.
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	target := srv.URL + "/doc?token=" + fetchDummySecret
	out, err := runner.Execute(3, "model-x", "prov-x", "read_url_content", `{"url":"`+target+`"}`)
	if err != nil {
		t.Fatalf("Execute error = %v; want nil", err)
	}
	if !strings.Contains(out, "audit-doc") {
		t.Errorf("model-visible result = %q; want the fetched body", out)
	}

	// Refused call: empty allow-list runner, the refusal is error-shaped and
	// the call is still logged.
	closed := NewRunner(root, false, false, logger)
	refusal, err := closed.Execute(4, "model-x", "prov-x", "read_url_content", `{"url":"http://127.0.0.1:1/x"}`)
	if err != nil {
		t.Fatalf("refusal must be error-shaped (nil Go error): %v", err)
	}
	if !strings.Contains(refusal, "allow-list") {
		t.Errorf("refusal = %q; want the allow-list refusal", refusal)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, fetchDummySecret) {
		t.Errorf("tool log leaks the raw key-shaped query value:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
	if !strings.Contains(log, "read_url_content") {
		t.Errorf("tool log recorded no read_url_content call:\n%s", log)
	}
	if !strings.Contains(log, "allow-list") {
		t.Errorf("tool log recorded no refusal entry:\n%s", log)
	}
	if !strings.Contains(log, "audit-doc") {
		t.Errorf("tool log lost the allowed call's fetched body:\n%s", log)
	}
}

// The schema exposure unit: the definition names read_url_content and
// requires exactly the url argument (no method, no headers, no timeout).
func TestReadURLContentToolDef(t *testing.T) {
	def := ReadURLContentToolDef()
	if def.Type != "function" || def.Function.Name != "read_url_content" {
		t.Errorf("def = %+v; want the read_url_content function definition", def)
	}
	required, ok := def.Function.Parameters["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "url" {
		t.Errorf("required = %v; want exactly [url]", def.Function.Parameters["required"])
	}
	if _, hasMethod := def.Function.Parameters["properties"].(map[string]any)["method"]; hasMethod {
		t.Error("the schema must expose no method argument (GET is forced)")
	}
}

// Malformed-arguments seeding (R13): a key-shaped dummy inside a truncated
// url argument renders [REDACTED] in the persisted log entry. The raw JSON
// tokens alone cannot seed (a token still carrying "://" is location-shaped
// and SeedEligible-exempt by design), so the fallback component-parses the
// URL candidate out of the raw arguments string.
func TestToolRunnerReadURLContentMalformedArgsMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"127.0.0.1"}

	// Truncated JSON: the typed unmarshal fails; the URL-embedded dummy must
	// still render [REDACTED] on disk.
	out, err := runner.Execute(3, "model-x", "prov-x", "read_url_content", `{"url":"http://127.0.0.1:1/x?token=`+fetchDummySecret)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid arguments") {
		t.Errorf("result = %q; want the malformed-arguments error", out)
	}

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, fetchDummySecret) {
		t.Errorf("tool log leaks the key-shaped dummy in the malformed url argument:\n%s", log)
	}
	if !strings.Contains(log, "[REDACTED]") {
		t.Errorf("tool log carries no redaction marker:\n%s", log)
	}
}

// G3: the fetch Output-sink reflection pin. A stub that echoes a
// secret-shaped query value into the text body would persist it raw in the
// log's Output field; the per-call seeds must render it [REDACTED] while
// the benign body text stays readable.
func TestToolRunnerReadURLContentOutputReflection(t *testing.T) {
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "echo: "+r.URL.Query().Get("token")+", doc-body")
	}))
	t.Cleanup(srv.Close)

	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	out, err := runner.Execute(5, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc?token=`+fetchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, fetchDummySecret) {
		t.Errorf("model-visible result leaks the reflected query value: %q", out)
	}

	assertLogMasked(t, logPath, fetchDummySecret)
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "doc-body") {
		t.Errorf("tool log lost the benign body text:\n%s", data)
	}
}

// G2b: a non-string url slot still seeds the log.
func TestToolRunnerReadURLContentNestedOperandMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"127.0.0.1"}
	out, err := runner.Execute(6, "model-x", "prov-x", "read_url_content",
		`{"url":["http://127.0.0.1:1/x?token=`+fetchDummySecret+`"]}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid arguments") {
		t.Errorf("result = %q; want the malformed-shape error", out)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
}

// G2c: a url operand that fails URL parsing is quoted verbatim by the
// invalid-url error; its separator-split forms must still seed the log.
func TestToolRunnerReadURLContentUnparseableURLOperandMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"127.0.0.1"}
	// "%zz" in the PATH makes url.Parse fail right after the secret-bearing
	// query, so the invalid-url error quotes the raw operand verbatim.
	out, err := runner.Execute(7, "model-x", "prov-x", "read_url_content",
		`{"url":"http://127.0.0.1:1/x%zz?token=`+fetchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "invalid url") {
		t.Errorf("result = %q; want the invalid-url error quoting the operand", out)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
}

// H1: a key-shaped run in a URL PATH operand seeds through the component
// collector (read_url_content side).
func TestToolRunnerReadURLContentPathTokenMasking(t *testing.T) {
	srv := textStub(t, "path-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(8, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/files/`+fetchDummySecret+`/dl"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
}

// H1: a key-shaped run in a URL FRAGMENT operand seeds too.
func TestToolRunnerReadURLContentFragmentTokenMasking(t *testing.T) {
	srv := textStub(t, "fragment-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(9, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc#section=`+fetchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
}

// H3: a key-shaped value in ANY argument field seeds; the url-only fast path
// used to return early with no seeds whenever the URL was benign, letting
// other fields persist raw AND skipping per-call redaction entirely.
func TestToolRunnerReadURLContentOtherFieldMasking(t *testing.T) {
	srv := textStub(t, "other-field-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(10, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc","note":"token `+fetchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
}

// H5a: the fetch side seeds the URL-escaped forms too. A transport error
// quotes the escaped request URL; the escaped dummy must not persist.
func TestToolRunnerReadURLContentEscapedFormMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"127.0.0.1"}
	// Nothing listens on 127.0.0.1:1: the call fails at the transport layer
	// with the escaped request URL in the error text.
	if _, err := runner.Execute(11, "model-x", "prov-x", "read_url_content",
		`{"url":"http://127.0.0.1:1/x?token=`+fetchDummySecret+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The raw dummy cannot reach this log at all (the Arguments field is
	// fully masked and the transport error quotes the escaped URL only), so
	// asserting its absence is vacuous; the escaped form is the real pin.
	assertLogMasked(t, logPath, url.QueryEscape(fetchDummySecret))
}

// K1 composite pin (fetch): adjacent sub-floor fragments of ONE credential
// in a non-schema argument field; fragment-level assertion (no key-shaped
// run survives).
func TestToolRunnerReadURLContentCompositeMasking(t *testing.T) {
	const composite = "ab,cd,ef,gh,ij,kl,mn,op,qr,st,uv,wx,yz,01,23,45,67"
	srv := textStub(t, "composite-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(12, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc","note":"token `+composite+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), composite) {
		t.Errorf("the full composite survives in the log:\n%s", data)
	}
	assertNoSecretRun(t, string(data))
}

// K2: a percent-encoded query value seeds its DECODED spelling, so a stub
// backend reflecting the decoded parameter renders [REDACTED] in the log
// Output (the wire and the error quote only the encoded form).
func TestToolRunnerReadURLContentDecodedReflectionMasking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "echo: "+r.URL.Query().Get("token")+", decoded-doc")
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(13, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/echo?token=`+url.QueryEscape(fetchDummySecret)+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertLogMasked(t, logPath, fetchDummySecret)
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if !strings.Contains(string(data), "decoded-doc") {
		t.Errorf("tool log lost the benign body text:\n%s", data)
	}
}

// K4 end to end: a credential paste in the scheme position is quoted alone
// by the unsupported-scheme refusal; the scheme spelling is what masks it.
func TestToolRunnerReadURLContentSchemePasteMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"host.example"}
	if _, err := runner.Execute(14, "model-x", "prov-x", "read_url_content",
		`{"url":"`+testfixtures.AWSSecretNoSlash+`://host.example/x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	log := string(data)
	if strings.Contains(log, testfixtures.AWSSecretNoSlash) {
		t.Errorf("the scheme-positioned paste survives in the log:\n%s", log)
	}
	assertNoSecretRun(t, log)
}

// K3 end to end (fetch): a backslash-marked operand renders [REDACTED] and
// no key-shaped run survives.
func TestToolRunnerReadURLContentRelativeMarkerMasking(t *testing.T) {
	srv := textStub(t, "marker-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(15, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc","note":"..\\`+testfixtures.AWSSecretNoSlash+`"}`); err != nil {
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

// K9 fetch parity: the host-token arm. A key-shaped host run renders
// [REDACTED] in the persisted log.
func TestToolRunnerReadURLContentHostTokenMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"host.example"}
	if _, err := runner.Execute(16, "model-x", "prov-x", "read_url_content",
		`{"url":"https://wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY.host.example/x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	assertNoSecretRun(t, string(data))
}

// K9 fetch parity: a key-shaped JSON object key renders [REDACTED].
func TestToolRunnerReadURLContentObjectKeyMasking(t *testing.T) {
	srv := textStub(t, "key-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(17, "model-x", "prov-x", "read_url_content",
		`{"token `+fetchDummySecret+`":"x","url":"`+srv.URL+`/doc"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), fetchDummySecret) {
		t.Errorf("the key-shaped object key survives in the log:\n%s", data)
	}
	assertNoSecretRun(t, string(data))
}

// K9 fetch parity: a backslash-u JSON-escaped operand renders [REDACTED] in
// the recorded (escaped) form and no fragment survives.
func TestToolRunnerReadURLContentJSONEscapedMasking(t *testing.T) {
	srv := textStub(t, "escaped-doc")
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(18, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/doc","note":"token wJalrXUtnFEMI\u002fK7MDENG\u002fbPxRfiCYEXAMPLEKEY"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), "wJalrXUtnFEMI") {
		t.Errorf("the backslash-u escaped operand survives in the log:\n%s", data)
	}
	assertNoSecretRun(t, string(data))
}

// K8: the fetch redirect policy is installed on INJECTED clients too. A bare
// injected client must refuse a cross-host redirect (the production client
// always did; before this fix an injected client followed it).
func TestReadURLContentRedirectPolicyOnInjectedClient(t *testing.T) {
	var localhostHits int
	var mu sync.Mutex
	localhost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		localhostHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "localhost-doc")
	}))
	t.Cleanup(localhost.Close)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/c0":
			fmt.Fprint(w, "chain-done")
		case "/rd":
			// Same service named localhost while the allow-list pins the
			// 127.0.0.1 spelling: the redirect must not escape (matching
			// is host-based and the port is ignored by design).
			http.Redirect(w, r, "http://localhost:"+stubPort(t, srv)+"/c0", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/c0", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host := stubHost(t, srv)

	// Cross-host redirect with an injected bare client: refused, and the
	// localhost-named stub is never dialed.
	if _, err := readURLContent(srv.URL+"/rd", []string{host}, &http.Client{}); err == nil ||
		!strings.Contains(err.Error(), "allow-list") {
		t.Errorf("injected client cross-host redirect: err = %v; want the allow-list refusal", err)
	}
	mu.Lock()
	if localhostHits != 0 {
		t.Errorf("the cross-host stub was dialed %d times; the redirect must be refused before any network use", localhostHits)
	}
	mu.Unlock()

	// Same-host redirect with an injected bare client: still followed.
	if out, err := readURLContent(srv.URL+"/hop", []string{host}, &http.Client{}); err != nil ||
		out != "chain-done" {
		t.Errorf("injected client same-host redirect: out=%q err=%v; want the final body", out, err)
	}
}

// M1 (fetch): the lowercase key-shaped host label renders [REDACTED] in the
// scheme-refusal echo because the host spellings seed unconditionally.
func TestToolRunnerReadURLContentLowercaseHostMasking(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{"mjalrxutnfemik7mdengbpxrficyexamplekey01.api.example"}
	if _, err := runner.Execute(19, "model-x", "prov-x", "read_url_content",
		`{"url":"http://mjalrxutnfemik7mdengbpxrficyexamplekey01.api.example/x"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	assertNoSecretRun(t, string(data))
}

// decodeReflectStub echoes the DOUBLE-DECODED m parameter: the arguments
// carry a single-encoded value, the wire form is double-encoded, the stub
// decodes once more, so the body carries the decoded spelling a reflecting
// backend would persist (M2 pins).
func decodeReflectStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("m")
		dec, err := url.QueryUnescape(raw)
		if err != nil {
			dec = raw
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "decoded: "+dec+", tail")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// M2 (encoded location marker): an operand ..%2F<run> reflects from the
// backend as ../<run>; the decoded spelling acquires location shape (vetoed
// by the rule) and its stripped core must seed unconditionally.
func TestToolRunnerReadURLContentDecodedMarkerMasking(t *testing.T) {
	srv := decodeReflectStub(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(20, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/echo?m=..%2F`+testfixtures.AWSSecretNoSlash+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), testfixtures.AWSSecretNoSlash) {
		t.Errorf("the decoded marker-shaped value survives in the log:\n%.200s", data)
	}
	assertNoSecretRun(t, string(data))
}

// M2 (lowercase split value): an operand token%20<lowercase run> reflects as
// "token <run>"; the decoded spelling fails the rule (lowercase, space) and
// must seed unconditionally.
func TestToolRunnerReadURLContentDecodedSplitMasking(t *testing.T) {
	srv := decodeReflectStub(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(21, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/echo?m=token%20mjalrxutnfemik7mdengbpxrficyexamplekey01"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), "mjalrxutnfemik7mdengbpxrficyexamplekey01") {
		t.Errorf("the decoded split value survives in the log:\n%.200s", data)
	}
	assertNoSecretRun(t, string(data))
}

// M2 (encoded comma composite): an operand aa%2Cbb%2C... reflects decoded as
// the comma composite; the decoded spelling fails the rule and must seed
// unconditionally.
func TestToolRunnerReadURLContentDecodedCompositeMasking(t *testing.T) {
	const encoded = "aa%2Cbb%2Ccc%2Cdd%2Cee%2Cff%2Cgg%2Chh%2Cii%2Cjj%2Ckk%2Cll%2Cmm%2Cnn%2Coo"
	srv := decodeReflectStub(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	if _, err := runner.Execute(22, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/echo?m=`+encoded+`"}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), "aa,bb,cc,dd,ee,ff,gg,hh,ii,jj,kk,ll,mm,nn,oo") {
		t.Errorf("the decoded composite survives in the log:\n%.200s", data)
	}
	assertNoSecretRun(t, string(data))
}

// M6: a seeded value echoed by the backend and cut by the 512 KiB body cap
// leaves a 16+ rune prefix that full-seed exact-substring masking cannot
// match; the first-32-runes prefix seeds mask the surviving run down to a
// sub-floor tail. The dummy is an OPERAND (a URL query parameter) the stub
// echoes at the cap boundary.
func TestToolRunnerReadURLContentTruncatedPrefixMasking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// Filler with no 16+ run; the echoed operand starts 35 bytes before
		// the cap so the cut keeps 35 bytes of it (32 masked by the prefix
		// seed, 3 below the run floor).
		filler := strings.Repeat("ab,", (maxFetchBodyBytes-35)/3)
		fmt.Fprint(w, filler+r.URL.Query().Get("token"))
	}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	logPath := filepath.Join(root, "fanout.jsonl")
	logger, err := NewLogger(logPath, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	runner := NewRunner(root, false, false, logger)
	runner.AllowHosts = []string{stubHost(t, srv)}
	out, err := runner.Execute(23, "model-x", "prov-x", "read_url_content",
		`{"url":"`+srv.URL+`/big?token=`+fetchDummySecret+`"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("result carries no truncation note")
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), testfixtures.AWSSecretShort) {
		t.Errorf("the 32-rune truncated prefix survives in the log:\n%.200s", data)
	}
	assertNoSecretRun(t, string(data))
}

// M8: jsonReencode emits a surrogate pair for non-BMP runes.
func TestJSONReencodeSurrogatePair(t *testing.T) {
	if got := jsonReencode("a\U0001F600b"); got != `a\ud83d\ude00b` {
		t.Errorf("jsonReencode surrogate = %q; want %q", got, `a\ud83d\ude00b`)
	}
	if got := jsonReencode("a/b"); got != `a\u002fb` {
		t.Errorf("jsonReencode = %q; want %q", got, `a\u002fb`)
	}
}

// N3: the seed pipeline is map-deduped and budget-capped (256 seeds; the
// byte budget applies from the truncation tier onward and never drops whole
// operands or the verbatim arguments text): a 180 KB argument set with
// thousands of operand values builds its seed set well under a generous
// wall bound and inside the count budget. (The round-6 blowup measured
// 68,012 seeds and 10.9s CPU for 353 KB; the budget caps the emitted set.)
func TestNetworkSeedBudgetPerf(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"url":"http://127.0.0.1:1/x","pad":{`)
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&sb, `"k%04d":"operand-value-%04d-abcdefghij",`, i, i)
	}
	sb.WriteString(`"z":"zzzz"}}`)

	start := time.Now()
	seeds := fetchArgSeeds(sb.String())
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("seed build took %v for 180 KB arguments; the budget/dedup regressed", elapsed)
	}
	if len(seeds) > maxNetworkSeeds {
		t.Errorf("seed budget exceeded: %d seeds > %d", len(seeds), maxNetworkSeeds)
	}
}

// N3: whole operands are exempt from the byte budget (dropping them would
// leave the raw operand in the log): a 40 KB single operand seeds whole and
// its truncation prefixes still mask.
func TestNetworkSeedWholeExemption(t *testing.T) {
	const chunk = "abcdefghij0123456789"
	blob := strings.Repeat(chunk, 2000) // 40 KB single operand
	args := `{"url":"http://127.0.0.1:1/x","blob":"` + blob + `"}`
	seeds := fetchArgSeeds(args)
	if !containsString(seeds, blob) {
		t.Error("the 40 KB whole operand was dropped; the byte budget must not drop whole operands")
	}
	redact := providers.NewRedactor(seeds...)
	if got := redact("echo " + blob[:24]); strings.Contains(got, blob[:24]) {
		t.Errorf("the 24-rune truncation prefix is not masked")
	}
}

// D14 (P3-5): the input-surface validation gate. Bare hostnames, IPs and
// host:port validate; scheme prefixes, wildcards, path segments, whitespace
// and empty-after-normalize entries do not, because exact matching after
// normalization can never match them and they would silently refuse every
// read_url_content call.
func TestValidateAllowHost(t *testing.T) {
	valid := []string{
		"example.com",
		"EXAMPLE.COM",
		" example.com ", // trimmed, as typed with stray spaces
		"sub.a-b.example.com",
		"localhost",
		"127.0.0.1",
		"example.com:8443", // port stripped at match time, valid as typed
		"::1",
		"[::1]",
	}
	for _, v := range valid {
		if !ValidateAllowHost(v) {
			t.Errorf("ValidateAllowHost(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"",                     // empty
		"   ",                  // empty after normalization
		"https://example.com",  // scheme prefix
		"http://example.com/x", // scheme prefix + path
		"example.com/path",     // path segment
		"example.com/",         // trailing slash is a path
		`example.com\path`,     // backslash path separator
		"*.example.com",        // wildcard
		"exam*le.com",          // wildcard anywhere
		"ex ample.com",         // internal space
		"example.com:badport",  // colon that is not an IP
		"ex_ample.com",         // outside the hostname alphabet
		"https:example.com",    // scheme-ish colon, not an IP
	}
	for _, v := range invalid {
		if ValidateAllowHost(v) {
			t.Errorf("ValidateAllowHost(%q) = true, want false", v)
		}
	}
}

// D14 (P3-5): the list predicate skips empties (NormalizeAllowHosts drops
// them, so they are not an error) and refuses on the first bad entry.
func TestValidateAllowHosts(t *testing.T) {
	if !ValidateAllowHosts(nil) {
		t.Error("ValidateAllowHosts(nil) = false, want true")
	}
	if !ValidateAllowHosts([]string{"", "  ", "example.com", "127.0.0.1:9"}) {
		t.Error("ValidateAllowHosts with only valid + empty entries = false, want true")
	}
	for _, entries := range [][]string{
		{"example.com", "https://evil.example"},
		{"*.example.com"},
		{"ok.example", "bad path.example"},
	} {
		if ValidateAllowHosts(entries) {
			t.Errorf("ValidateAllowHosts(%q) = true, want false", entries)
		}
	}
}
