package wireadp

// Round-3b fix pins: FIX-29 (json_schema repair trigger), FIX-30 (negative
// Config knobs disable), FIX-31 (keep-alive ticker stopped), FIX-32 (idle
// watchdog re-armed on headers), FIX-33 (discovery-record loopback guard).

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"agypro/internal/paths"
)

// ---------------------------------------------------------------------------
// FIX-29 — json_schema-only 400 licenses the bounded repair
// ---------------------------------------------------------------------------

func TestGenerate_RepairRetry_JsonSchemaOnly(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	srv := startServer(t, cfg)

	// A structured-output request, so response_format is on the wire.
	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "json 줘"}}}},
		"generationConfig": map[string]any{
			"responseMimeType":   "application/json",
			"responseJsonSchema": map[string]any{"type": "object"},
		},
	}
	body, _ := json.Marshal(req)

	// The provider names ONLY json_schema (the schema riding inside
	// response_format), never the field itself.
	m.setScript(func(n int, w http.ResponseWriter) {
		if n == 0 {
			if m.call(0).Body["response_format"] == nil {
				t.Error("the first attempt should have carried response_format")
			}
			writeStatus(w, 400, `{"error":"json_schema is not supported by this deployment"}`)
			return
		}
		writeSSECompletion(w, []string{"스키마 없이 성공"}, defaultUsage)
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if m.count() != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2 (one repair retry)", m.count())
	}
	if _, exists := m.call(1).Body["response_format"]; exists {
		t.Fatalf("the retry must drop response_format: %v", m.call(1).Body)
	}
	if !strings.Contains(raw, "스키마 없이 성공") {
		t.Fatalf("retry response missing: %s", raw)
	}
}

func TestRepairFields_TriggerMapping(t *testing.T) {
	cases := []struct {
		name    string
		snippet string
		want    []string
	}{
		{"response_format drops response_format", "unsupported parameter: response_format", []string{"response_format"}},
		{"json_schema also drops response_format", "json_schema is not supported", []string{"response_format"}},
		{"both names collapse to one drop", "response_format json_schema rejected", []string{"response_format"}},
		{"reasoning_effort drops itself", "unsupported parameter: reasoning_effort", []string{"reasoning_effort"}},
		{"a JSON-key n drops n", `invalid value for field "n"`, []string{"n"}},
		{"a single-quoted n drops n", "field 'n' must be 1", []string{"n"}},
		{"prose containing a standalone n does not trigger", "context length exceeded for this model", nil},
		{"an unrelated 400 does not trigger", "model not found", nil},
		{"an empty snippet does not trigger", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &upstreamFailure{kind: failureStatus, status: http.StatusBadRequest, snippet: tc.snippet}
			got := f.repairFields()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("repairFields(%q) = %v, want %v", tc.snippet, got, tc.want)
			}
		})
	}
}

func TestRepairFields_OnlyLicensedOn400(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		f := &upstreamFailure{kind: failureStatus, status: status, snippet: "unsupported parameter: response_format"}
		if got := f.repairFields(); got != nil {
			t.Fatalf("status %d licensed a repair: %v", status, got)
		}
	}
	f := &upstreamFailure{kind: failureTransport, snippet: "response_format"}
	if got := f.repairFields(); got != nil {
		t.Fatalf("a transport failure licensed a repair: %v", got)
	}
}

// ---------------------------------------------------------------------------
// FIX-30 — a non-positive Config value disables the watchdog / keep-alive
// ---------------------------------------------------------------------------

// TestGenerate_NegativeConfigDisablesWatchdogs pins the documented contract:
// with all three knobs negative the stream completes normally. Before the fix a
// negative first-byte/idle fired instantly (every request 504) and a negative
// keep-alive panicked time.NewTicker on the first 200 stream.
func TestGenerate_NegativeConfigDisablesWatchdogs(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.UpstreamFirstByteTimeout = -1 * time.Second
	cfg.UpstreamIdleTimeout = -1 * time.Second
	cfg.StreamKeepAliveInterval = -1 * time.Second
	srv := startServer(t, cfg)

	// A slow-but-completing upstream: with the watchdogs disabled the request
	// relies on the inbound context and must still finish.
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEFrame(w, contentChunk("느리지만"))
		time.Sleep(300 * time.Millisecond)
		writeSSEFrame(w, contentChunk("완료됩니다"))
		writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   defaultUsage})
	})

	done := make(chan struct{})
	var status int
	var raw string
	go func() {
		defer close(done)
		resp, body := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		status = resp.StatusCode
		raw = body
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never completed: a disabled watchdog must not block the stream")
	}
	if status != http.StatusOK {
		t.Fatalf("status %d body %s", status, raw)
	}
	if !strings.Contains(raw, "느리지만완료됩니다") {
		t.Fatalf("stream content missing: %s", raw)
	}
	// No keep-alive comments when pings are disabled.
	if strings.Contains(raw, ": ping") {
		t.Fatalf("disabled keep-alive still pinged: %s", raw)
	}
}

func TestWatchdogTimeoutResolvers(t *testing.T) {
	srv := &Server{cfg: Config{}}
	if got := srv.firstByteTimeout(); got != defaultFirstByteTimeout {
		t.Fatalf("zero first-byte = %v, want the 300s default", got)
	}
	if got := srv.idleTimeout(); got != defaultIdleTimeout {
		t.Fatalf("zero idle = %v, want the 120s default", got)
	}
	if got := srv.keepAliveInterval(); got != defaultKeepAliveInterval {
		t.Fatalf("zero keep-alive = %v, want the 15s default", got)
	}
	// Explicit positives pass through.
	srv.cfg.UpstreamFirstByteTimeout = 42 * time.Second
	srv.cfg.UpstreamIdleTimeout = 43 * time.Second
	srv.cfg.StreamKeepAliveInterval = 44 * time.Second
	if got := srv.firstByteTimeout(); got != 42*time.Second {
		t.Fatalf("first-byte = %v", got)
	}
	if got := srv.idleTimeout(); got != 43*time.Second {
		t.Fatalf("idle = %v", got)
	}
	if got := srv.keepAliveInterval(); got != 44*time.Second {
		t.Fatalf("keep-alive = %v", got)
	}
	// Negatives resolve to the disabled sentinel (0), never a negative.
	srv.cfg.UpstreamFirstByteTimeout = -time.Second
	srv.cfg.UpstreamIdleTimeout = -time.Second
	srv.cfg.StreamKeepAliveInterval = -time.Second
	if got := srv.firstByteTimeout(); got != 0 {
		t.Fatalf("negative first-byte = %v, want the disabled sentinel 0", got)
	}
	if got := srv.idleTimeout(); got != 0 {
		t.Fatalf("negative idle = %v, want the disabled sentinel 0", got)
	}
	if got := srv.keepAliveInterval(); got != 0 {
		t.Fatalf("negative keep-alive = %v, want the disabled sentinel 0", got)
	}
}

func TestWatchdogTimer_DisabledNeverFires(t *testing.T) {
	w := newWatchdogTimer(-time.Second)
	if w.c != nil || w.timer != nil {
		t.Fatal("a disabled watchdog must not arm a timer")
	}
	// Rearm and stop on a disabled watchdog are no-ops, not panics.
	w.rearm()
	w.stop()
	zero := newWatchdogTimer(0)
	if zero.timer != nil {
		t.Fatal("a zero watchdog must not arm a timer")
	}

	// A positive watchdog still fires and can be stopped and re-armed.
	live := newWatchdogTimer(20 * time.Millisecond)
	select {
	case <-live.c:
		t.Fatal("the timer fired before its budget")
	case <-time.After(5 * time.Millisecond):
	}
	live.stop()
	if live.c != nil {
		t.Fatal("a stopped watchdog must be disarmed")
	}
	live.rearm()
	select {
	case <-live.c:
	case <-time.After(time.Second):
		t.Fatal("a re-armed watchdog must fire")
	}
}

// ---------------------------------------------------------------------------
// FIX-32 — the idle budget starts at the response headers, not at the request
// ---------------------------------------------------------------------------

// TestGenerate_IdleBudgetStartsAtHeaders pins that a provider slow to produce
// headers is governed by the first-byte budget (larger), not the idle budget
// (smaller). Before the fix the idle timer armed at request start, so a
// headers delay between the two budgets was killed with a misleading 504.
func TestGenerate_IdleBudgetStartsAtHeaders(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	// The headers land after the idle budget but inside the first-byte budget.
	cfg.UpstreamFirstByteTimeout = 3 * time.Second
	cfg.UpstreamIdleTimeout = 200 * time.Millisecond
	srv := startServer(t, cfg)

	m.setScript(func(n int, w http.ResponseWriter) {
		time.Sleep(700 * time.Millisecond) // > idle budget, < first-byte budget
		writeSSECompletion(w, []string{"헤더가 느려도"}, defaultUsage)
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: the idle budget must start at the headers", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "헤더가 느려도") {
		t.Fatalf("stream content missing: %s", raw)
	}
}

// TestGenerate_SlowButProgressingSurvivesIdle closes the round-3 coverage gap
// (F-06): body reads arriving under the idle budget keep the stream alive.
func TestGenerate_SlowButProgressingSurvivesIdle(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.UpstreamIdleTimeout = 400 * time.Millisecond
	cfg.StreamKeepAliveInterval = 60 * time.Millisecond
	srv := startServer(t, cfg)

	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Four deltas spaced under the idle budget: each read re-arms it.
		for i, delta := range []string{"하나", "둘", "셋", "넷"} {
			writeSSEFrame(w, contentChunk(delta))
			if i < 3 {
				time.Sleep(250 * time.Millisecond)
			}
		}
		writeSSEFrame(w, map[string]any{"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   defaultUsage})
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s: a progressing stream must survive the idle watchdog", resp.StatusCode, raw)
	}
	if !strings.Contains(raw, "하나둘셋넷") {
		t.Fatalf("stream content missing: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// FIX-33 — a non-loopback discovery record is never probed
// ---------------------------------------------------------------------------

// TestAdoptRemote_NonLoopbackRecordIsRefusedWithoutAnyProbe plants a discovery
// record pointing at an httptest server and asserts that server receives ZERO
// requests: the install nonce must never leave the loopback.
func TestAdoptRemote_NonLoopbackRecordIsRefusedWithoutAnyProbe(t *testing.T) {
	// A canary on loopback that must never be probed. One case points at it
	// through a host spelling the guard refuses ("localhost." with a trailing
	// dot), which is the revert proof: without the loopback guard the probe
	// reaches this server and the install nonce leaves the process.
	var probeHits int
	var nonceSeen string
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeHits++
		if v := r.Header.Get(headerInstallNonce); v != "" {
			nonceSeen = v
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer canary.Close()
	_, canaryPort, err := net.SplitHostPort(canary.Listener.Addr().String())
	if err != nil {
		t.Fatalf("canary port: %v", err)
	}

	cases := []struct {
		name string
		url  string
	}{
		{"a non-loopback URL aimed at the canary", "http://localhost.:" + canaryPort},
		{"an off-box http URL", "http://attacker.example:4444"},
		{"an off-box https URL", "https://attacker.example"},
		{"a non-http scheme", "ftp://127.0.0.1:4444/x"},
		{"a file scheme", "file:///etc/passwd"},
		{"a scheme-relative target", "//attacker.example:4444"},
		{"an empty host", "http://"},
		{"a non-loopback IP literal", "http://93.184.216.34:4444"},
		{"a hostname that is not provably loopback", "http://localhost.example:4444"},
		{"garbage", "not a url at all"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(paths.EnvAgyProHome, home)
			before := probeHits

			rec := discoveryRecord{URL: tc.url, Port: 4444, PID: 1, Started: "2026-10-15T00:00:00Z"}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("marshal record: %v", err)
			}
			if err := os.WriteFile(filepath.Join(home, discoveryFile), data, 0o600); err != nil {
				t.Fatalf("write record: %v", err)
			}

			resetSingletonForTest(t)
			cfg := Config{AuthKey: "install-nonce-secret", AuditPath: filepath.Join(home, "audit.jsonl")}
			srv, err := Start(cfg)
			if err != nil {
				t.Fatalf("Start must bind fresh on a hostile record, got %v", err)
			}
			defer srv.Stop()
			if srv.adopted {
				t.Fatal("a non-loopback record must never be adopted")
			}
			if probeHits != before {
				t.Fatalf("the canary received %d probe requests; the record must be refused before any probe", probeHits-before)
			}
			if nonceSeen != "" {
				t.Fatalf("the install nonce reached %q", nonceSeen)
			}
		})
	}
}

func TestIsLoopbackURL(t *testing.T) {
	loopback := []string{
		"http://127.0.0.1:8790",
		"http://127.0.0.1",
		"https://127.0.0.1:8790",
		"http://[::1]:8790",
		"http://localhost:8790",
		"http://LocalHost:8790",
		"http://127.5.5.5:8790",
	}
	for _, u := range loopback {
		if !isLoopbackURL(u) {
			t.Fatalf("%q must be accepted as loopback", u)
		}
	}
	refused := []string{
		"",
		"http://",
		"http://8.8.8.8:8790",
		"https://example.com",
		"ftp://127.0.0.1",
		"file:///tmp/x",
		"//127.0.0.1:8790",
		"127.0.0.1:8790",
		"gopher://127.0.0.1",
	}
	for _, u := range refused {
		if isLoopbackURL(u) {
			t.Fatalf("%q must be refused", u)
		}
	}
}

// TestHealthzClient_RefusesRedirects pins that the adoption probe never follows
// a redirect (FIX-33): a hostile loopback record could otherwise 302 the
// nonce-bearing probe off-box.
func TestHealthzClient_RefusesRedirects(t *testing.T) {
	if healthzClient.CheckRedirect == nil {
		t.Fatal("the healthz probe client must refuse redirects")
	}
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := healthzClient.CheckRedirect(req, []*http.Request{req}); err == nil {
		t.Fatal("CheckRedirect must return an error so the client stops")
	}
}

// TestAdoptRemote_LoopbackRecordStillAdopts guards the fix against
// over-refusal: a genuine loopback record is still adopted through the nonce.
func TestAdoptRemote_LoopbackRecordStillAdopts(t *testing.T) {
	home := t.TempDir()
	t.Setenv(paths.EnvAgyProHome, home)
	cfg := Config{Registry: testRegistry(), AuthKey: "shared-nonce", AuditPath: filepath.Join(home, "audit.jsonl")}
	srv1 := startServer(t, cfg)

	resetSingletonForTest(t)
	cfg2 := Config{Registry: testRegistry(), AuthKey: "shared-nonce", AuditPath: filepath.Join(home, "audit2.jsonl")}
	srv2, err := Start(cfg2)
	if err != nil {
		t.Fatalf("cross-process Start: %v", err)
	}
	if !srv2.adopted {
		t.Fatal("a genuine loopback record must still be adopted")
	}
	if srv2.URL() != srv1.URL() {
		t.Fatalf("adopted URL %s != owner URL %s", srv2.URL(), srv1.URL())
	}
}

// ---------------------------------------------------------------------------
// FIX-31 — the keep-alive ticker is stopped on every return path
// ---------------------------------------------------------------------------

// countingPinger records the create/stop pairing so the lifecycle contract is
// observable (FIX-31).
type countingPinger struct {
	inner  pinger
	onStop func()
}

func (p *countingPinger) c() <-chan time.Time { return p.inner.c() }
func (p *countingPinger) stop() {
	p.onStop()
	p.inner.stop()
}

// TestGenerate_KeepAliveTickerIsStopped drives many streams through the same
// server and asserts every keep-alive ticker that was created is also stopped.
// Before the fix the ticker was never Stop()ed, so a long-lived adapter
// accumulated one runtime timer per streaming request.
func TestGenerate_KeepAliveTickerIsStopped(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.StreamKeepAliveInterval = 10 * time.Millisecond
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEFrame(w, contentChunk("시작"))
		time.Sleep(60 * time.Millisecond) // a few pings
		writeSSECompletion(w, []string{"끝"}, defaultUsage)
	})

	var mu sync.Mutex
	var created, stopped int
	original := newPinger
	newPinger = func(d time.Duration) pinger {
		mu.Lock()
		created++
		mu.Unlock()
		return &countingPinger{inner: original(d), onStop: func() {
			mu.Lock()
			stopped++
			mu.Unlock()
		}}
	}
	defer func() { newPinger = original }()

	const streams = 25
	for i := 0; i < streams; i++ {
		resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: status %d body %s", i, resp.StatusCode, raw)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if created == 0 {
		t.Fatal("no keep-alive ticker was created; the test is not exercising the path")
	}
	if stopped != created {
		t.Fatalf("keep-alive tickers: %d created, %d stopped; every return path must stop it", created, stopped)
	}
}

// ---------------------------------------------------------------------------
// FIX-35 / FIX-40 — audit fields are rune-capped
// ---------------------------------------------------------------------------

func TestAuditRecord_FieldsAreRuneCapped(t *testing.T) {
	ar := &auditRecord{}
	// A multi-MB model string must not become a multi-MB audit line.
	huge := strings.Repeat("가", 1<<20)
	ar.setTarget("streamGenerateContent", "loadCodeAssist", huge)
	if got := utf8.RuneCountInString(ar.model); got != maxFieldRunes {
		t.Fatalf("model runes = %d, want %d", got, maxFieldRunes)
	}
	if got := utf8.RuneCountInString(ar.verb); got != len("streamGenerateContent") {
		t.Fatalf("verb = %q", ar.verb)
	}
	if got := utf8.RuneCountInString(ar.rpc); got != len("loadCodeAssist") {
		t.Fatalf("rpc = %q", ar.rpc)
	}
	ar.setClass(huge)
	if got := utf8.RuneCountInString(ar.route); got != maxFieldRunes {
		t.Fatalf("route runes = %d, want %d", got, maxFieldRunes)
	}
	// The capped model is still valid UTF-8 (no U+FFFD from a mid-rune cut).
	if !utf8.ValidString(ar.model) {
		t.Fatal("the capped model is not valid UTF-8")
	}
}

func TestAuditRecord_NoteCutIsRuneSafe(t *testing.T) {
	ar := &auditRecord{}
	// Append notes whose tails are multi-byte runes so a byte cut would land
	// mid-rune.
	for i := 0; i < 40; i++ {
		ar.addNote(strings.Repeat("한", 30))
	}
	if got := utf8.RuneCountInString(ar.note); got != maxFieldRunes {
		t.Fatalf("note runes = %d, want %d", got, maxFieldRunes)
	}
	if !utf8.ValidString(ar.note) {
		t.Fatal("the capped note is not valid UTF-8")
	}
	if strings.ContainsRune(ar.note, '\uFFFD') {
		t.Fatal("the capped note contains a replacement character")
	}
}

// TestAudit_HugeEnvelopeModelStaysBounded drives a real request whose envelope
// model fails resolution: the audit line must stay a single bounded line.
func TestAudit_HugeEnvelopeModelStaysBounded(t *testing.T) {
	cfg := testConfig(t)
	srv := startServer(t, cfg)
	huge := strings.Repeat("가", 1<<20)
	body := []byte(`{"project":"p","model":"` + huge + `","request":{"contents":[]}}`)
	resp, raw := post(t, srv.URL()+"/v1internal:generateContent", nil, body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d body %.200s", resp.StatusCode, raw)
	}
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if got := utf8.RuneCountInString(lines[0].Model); got != maxFieldRunes {
		t.Fatalf("audit model runes = %d, want %d", got, maxFieldRunes)
	}
	info, err := os.Stat(cfg.AuditPath)
	if err != nil {
		t.Fatalf("stat audit: %v", err)
	}
	if info.Size() > 1<<16 {
		t.Fatalf("audit file grew to %d bytes for one request", info.Size())
	}
}

// ---------------------------------------------------------------------------
// FIX-36 — the apology masks a key-shaped provider name
// ---------------------------------------------------------------------------

func TestGenerate_ApologyMasksTheProviderName(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	// A key-shaped registry provider name: the §9.2 apology is the one path
	// that bypasses sanitizeMessage, so it must mask on its own.
	reg.Providers[0].Name = "sk-live-abcdef0123456789"
	srv := startServer(t, cfg)
	// A 200 with no content at all: the codec synthesizes the apology.
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readGolden(t, "gf-27-request.sse"))
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "sk-live-abcdef0123456789") {
		t.Fatalf("the apology leaked the raw provider name: %s", raw)
	}
	// MaskSecret renders first-5 + ****.
	if !strings.Contains(raw, "sk-li****") {
		t.Fatalf("the apology must carry the masked provider name: %s", raw)
	}
}

func TestGenerate_ApologyStripsControlCharacters(t *testing.T) {
	m := newMockUpstream(t)
	cfg, reg := testUpstreamConfig(t, m)
	// An escape-bearing registry provider name: the apology must not hand raw
	// terminal escapes to agy's renderer.
	reg.Providers[0].Name = "prov\u001b]0;pwned\u0007ider"
	srv := startServer(t, cfg)
	m.setScript(func(n int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(readGolden(t, "gf-27-request.sse"))
	})

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatRequestBody(nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	if strings.Contains(raw, "\u001b") || strings.Contains(raw, "\u0007") {
		t.Fatalf("the apology leaked control characters: %q", raw)
	}
	if !strings.Contains(raw, "provider prov") {
		t.Fatalf("the apology lost the provider name: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// FIX-39 — a handler panic answers with a §9.1 500 instead of dropping
// ---------------------------------------------------------------------------

// panicHandler panics after the pipeline has admitted the request, modelling a
// future handler defect.
type panicHandler struct{}

func (panicHandler) Generate(w http.ResponseWriter, r *http.Request, g *GenerateContext) error {
	panic("boom: an unforeseen handler defect")
}

func TestGuard_HandlerPanicAnswers500(t *testing.T) {
	m := newMockUpstream(t)
	cfg, _ := testUpstreamConfig(t, m)
	cfg.Generate = panicHandler{}
	srv := startServer(t, cfg)

	resp, raw := post(t, streamURL(srv, "probe-gemini"), nil, chatBody())
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", resp.StatusCode, raw)
	}
	env := decodeError(t, raw)
	if env.Error.Status != statusInternal {
		t.Fatalf("envelope = %+v", env.Error)
	}
	// The recovered value never reaches the wire.
	if strings.Contains(raw, "boom") {
		t.Fatalf("the panic text leaked into the response: %s", raw)
	}
	// The audit line still records the request.
	lines := readAuditLines(t, cfg.AuditPath, 1)
	if lines[0].Status != http.StatusInternalServerError {
		t.Fatalf("audit status = %d", lines[0].Status)
	}
	if !strings.Contains(lines[0].Note, "panic") {
		t.Fatalf("audit note = %q, want the panic record", lines[0].Note)
	}
	if m.count() != 0 {
		t.Fatalf("the panicking handler must not reach the upstream: %d", m.count())
	}
}
