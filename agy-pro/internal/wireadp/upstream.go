package wireadp

// Upstream dispatch (spec §4.1 upstream.go): the outbound request build with
// the §6.3 family quirks, the §6.4 watchdogs, the §9.1 error mapping and the
// §7.13 bounded repair retry. The Gemini<->OpenAI translation itself lives in
// geminiwire; this file only assembles what the codec produced, drives the
// call and classifies what came back.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agypro/internal/geminiwire"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// §6.4 watchdog defaults. Both are per request and start when the request
// leaves; agy's own --print-timeout/Esc cancellation rides on the request
// context and always wins.
const (
	defaultFirstByteTimeout = 300 * time.Second
	defaultIdleTimeout      = 120 * time.Second
	// defaultKeepAliveInterval is the §8.9 comment-ping cadence.
	defaultKeepAliveInterval = 15 * time.Second
	// snippetBytes is the §9.1 diagnostic budget for an upstream error body:
	// read a little past the cut so a secret straddling it is whole when the
	// redactor sees it, then redact, then cut.
	snippetBytes   = 2048
	snippetOverlap = 16 << 10
)

// repairTriggers maps a §7.13 trigger onto the adapter-added body field the
// single repair retry drops. The trigger is the keyword a provider's 400 body
// names; the field is what the adapter added and can remove. A provider that
// rejects the structured-output payload names either "response_format" (the
// field itself) or "json_schema" (the schema riding inside it), and both
// complaints are repaired by dropping response_format. The "n" trigger requires
// the JSON-key form so ordinary prose containing a standalone "n" cannot
// license a duplicate upstream call.
var repairTriggers = []struct {
	pattern *regexp.Regexp
	field   string
}{
	{regexp.MustCompile(`response_format`), "response_format"},
	{regexp.MustCompile(`json_schema`), "response_format"},
	{regexp.MustCompile(`["']n["']`), "n"},
	{regexp.MustCompile(`reasoning_effort`), "reasoning_effort"},
}

// failureKind classifies why an upstream attempt did not produce a turn.
type failureKind int

const (
	// failureTransport is a dial/reset/TLS/DNS failure, or a mid-stream
	// disconnect after the upstream answered 200 (§9.1, §9.3).
	failureTransport failureKind = iota
	// failureStatus is a non-200 upstream answer.
	failureStatus
	// failureMalformed is a 200 whose body could not be folded at all.
	failureMalformed
	// failureTimeout is a §6.4 watchdog.
	failureTimeout
	// failureTruncated is an upstream SSE stream that ended WITHOUT a terminal
	// signal: no chunk carried a finish_reason and no [DONE] sentinel arrived.
	// The folded turn is a truncated answer, so the adapter must surface an
	// error instead of synthesizing a complete-looking 200 (G10 finding 1).
	failureTruncated
	// failureClientGone means agy canceled the request: nothing may be
	// written, the peer is no longer reading.
	failureClientGone
	// failureOutboundTooLarge is the mesh-side outbound request-body cap: the
	// translated OpenAI body grew past openaiup.MaxBodyBytes, so the provider is
	// never contacted. It mirrors the bridge's maxOutboundBodyBytes gate (M4 item
	// 4), which the mesh path lacked: the inbound body is capped at 32 MB
	// (routes.go) and the upstream response is capped at 32 MB, but the
	// re-marshaled OUTBOUND body was sent uncapped, and json.Marshal HTML-escapes
	// <, >, & so a body at the inbound cap can inflate several-fold before it
	// reaches the provider. An over-cap body fails loudly; it is never silently
	// truncated.
	failureOutboundTooLarge
)

// upstreamFailure is one classified upstream failure. The snippet is already
// redacted and one-lined; the raw error is kept for the audit note only and
// never reaches the wire.
type upstreamFailure struct {
	kind     failureKind
	status   int
	snippet  string
	watchdog string
	err      error
}

// envelope maps the failure onto its §9.1 triple. provider names the upstream,
// modelID the requested provider-side model id (the 404 row's <id>).
func (f *upstreamFailure) envelope(provider, modelID string) (int, string, string) {
	switch f.kind {
	case failureTimeout:
		code, status, message, _ := lookupProviderError("upstream-timeout", provider, "")
		return code, status, message
	case failureMalformed:
		code, status, message, _ := lookupProviderError("malformed-body", provider, "")
		return code, status, message
	case failureTruncated:
		code, status, message, _ := lookupProviderError("stream-truncated", provider, "")
		return code, status, message
	case failureOutboundTooLarge:
		// The 413 row already in the §9.1 table: an over-cap request is a
		// too-large request, and the provider name keeps the row specific.
		code, status, message, _ := lookupProviderError("provider-413", provider, "")
		return code, status, message
	case failureStatus:
		return mapProviderStatus(provider, f.status, f.cause(modelID))
	default:
		code, status, message, _ := lookupProviderError("transport-failure", provider, "")
		return code, status, message
	}
}

// cause is the row-specific detail the §9.1 message templates take: the model
// id for 404, the redacted one-line cause for 400, the HTTP code for 5xx.
func (f *upstreamFailure) cause(modelID string) string {
	switch f.status {
	case http.StatusNotFound:
		return modelID
	case http.StatusBadRequest:
		return upstreamCause(f.snippet)
	}
	return ""
}

// upstreamCause extracts the redacted one-line cause from an upstream error
// body: the provider's own error / message / detail string when the body is
// JSON, otherwise the trimmed body. The result is capped so a chatty provider
// cannot bloat the message, and the snippet it reads was already redacted.
func upstreamCause(snippet string) string {
	trimmed := strings.TrimSpace(snippet)
	if trimmed == "" {
		return ""
	}
	var doc struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if err := json.Unmarshal([]byte(trimmed), &doc); err == nil {
		switch e := doc.Error.(type) {
		case string:
			if s := strings.TrimSpace(e); s != "" {
				return capCause(s)
			}
		case map[string]any:
			if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
				return capCause(m)
			}
		}
		if s := strings.TrimSpace(doc.Message); s != "" {
			return capCause(s)
		}
		switch d := doc.Detail.(type) {
		case string:
			if s := strings.TrimSpace(d); s != "" {
				return capCause(s)
			}
		}
	}
	return capCause(trimmed)
}

// maxCauseRunes caps the extracted cause; the whole message is capped again by
// sanitizeMessage.
const maxCauseRunes = 160

func capCause(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCauseRunes {
		s = string([]rune(s)[:maxCauseRunes])
	}
	return s
}

// repairFields returns the adapter-added fields the §7.13 retry should drop for
// this failure's redacted snippet. Several triggers can map onto the same field
// (response_format and json_schema), so the result is de-duplicated.
func (f *upstreamFailure) repairFields() []string {
	if f.kind != failureStatus || f.status != http.StatusBadRequest || f.snippet == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, trigger := range repairTriggers {
		if !trigger.pattern.MatchString(f.snippet) {
			continue
		}
		if seen[trigger.field] {
			continue
		}
		seen[trigger.field] = true
		out = append(out, trigger.field)
	}
	return out
}

// ---------------------------------------------------------------------------
// outbound body build (§6.2, §6.3)
// ---------------------------------------------------------------------------

// buildUpstreamBody assembles the OpenAI request body from the codec's output
// and applies the §6.3 family quirks. It returns the marshaled body, whether
// the caller may surface provider reasoning as thought parts, and the audit
// notes the codec and the quirk table produced.
func (h *generateHandler) buildUpstreamBody(g *GenerateContext) ([]byte, bool, []string, error) {
	caps := g.Target.Model.Capabilities
	messages, notes := geminiwire.ToOpenAIMessages(g.Request, caps.SupportsVision)
	tools, toolChoice, toolNotes := geminiwire.ToOpenAITools(g.Request)
	notes = append(notes, toolNotes...)
	params, allowThoughts, paramNotes := geminiwire.ToOpenAIParams(g.Request, caps, g.Target.Model.MaxOutputTokens)
	notes = append(notes, paramNotes...)

	// §7.4: the whole native tool set is forwarded, but only when the model
	// can use tools at all.
	var outboundTools []geminiwire.OpenAITool
	switch {
	case len(tools) == 0:
	case caps.CanUseTools():
		outboundTools = tools
	default:
		notes = append(notes, "tools dropped: the model does not support function calling")
	}

	body := map[string]any{
		"model":    g.Target.Model.ID,
		"messages": messages,
	}
	for k, v := range params {
		body[k] = v
	}
	if len(outboundTools) > 0 {
		body["tools"] = outboundTools
		if toolChoice != nil {
			body["tool_choice"] = toolChoice
		}
	}
	if g.AltSSE {
		body["stream"] = true
	}

	h.applyFamilyQuirks(body, g.Target, len(outboundTools) > 0, &notes)

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, false, notes, fmt.Errorf("marshal upstream body: %w", err)
	}
	return payload, allowThoughts, notes, nil
}

// applyFamilyQuirks applies the §6.3 table: the max-tokens field name, the
// reasoning-series sampling strip, stream_options and the n>1-with-tools clamp.
func (h *generateHandler) applyFamilyQuirks(body map[string]any, target providers.Target, hasTools bool, notes *[]string) {
	quirks := familyQuirksFor(target)

	if v, ok := body["max_tokens"]; ok {
		field := "max_tokens"
		if override := h.maxTokensFieldFor(target); override != "" {
			field = override
		} else if quirks.maxCompletionTokens {
			field = "max_completion_tokens"
		}
		if field != "max_tokens" {
			delete(body, "max_tokens")
			body[field] = v
			*notes = append(*notes, "max tokens sent as "+field)
		}
	}

	if quirks.stripSampling {
		if _, ok := body["temperature"]; ok {
			delete(body, "temperature")
			*notes = append(*notes, "temperature stripped for the reasoning family")
		}
		if _, ok := body["top_p"]; ok {
			delete(body, "top_p")
			*notes = append(*notes, "top_p stripped for the reasoning family")
		}
	}

	if body["stream"] == true && !quirks.omitStreamOptions {
		body["stream_options"] = map[string]any{"include_usage": true}
	}

	// §7.12: most providers reject n>1 together with tools.
	if n, ok := body["n"].(int); ok && n > 1 && hasTools {
		body["n"] = 1
		*notes = append(*notes, fmt.Sprintf("n=%d clamped to 1 because tools are attached", n))
	}
}

// familyQuirks is one §6.3 table row. The zero value is the conservative
// OpenAI-compatible default.
type familyQuirks struct {
	// maxCompletionTokens selects the max_completion_tokens field name.
	maxCompletionTokens bool
	// stripSampling drops non-default temperature/top_p.
	stripSampling bool
	// omitStreamOptions drops stream_options entirely.
	omitStreamOptions bool
}

// familyQuirksFor resolves the §6.3 row for one target. The id patterns are the
// spec's named families (openai new-gen o*/gpt-5* for the first two rows,
// Anthropic-compat/OpenRouter for the third); everything else takes the
// conservative default.
func familyQuirksFor(target providers.Target) familyQuirks {
	id := strings.ToLower(strings.TrimSpace(target.Model.ID))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	newGen := strings.HasPrefix(id, "gpt-5") || (len(id) > 1 && id[0] == 'o' && id[1] >= '0' && id[1] <= '9')
	if newGen {
		return familyQuirks{maxCompletionTokens: true, stripSampling: true}
	}
	switch target.Model.FamilyName() {
	case "anthropic", "claude", "openrouter":
		return familyQuirks{omitStreamOptions: true}
	}
	return familyQuirks{}
}

// maxTokensFieldFor reads the optional per-model "maxTokensField" key from
// providers.json (§6.3). providers.Model carries no such field and this unit
// may not edit the providers package, so the file is re-parsed tolerantly:
// unknown keys are ignored and an unreadable file yields no overrides.
func (h *generateHandler) maxTokensFieldFor(target providers.Target) string {
	h.maxTokensOnce.Do(func() {
		h.maxTokensFields = loadMaxTokensFields(h.srv.cfg.Registry)
	})
	return h.maxTokensFields[target.Provider.Name+"\x00"+target.Model.ID]
}

type maxTokensFieldIndex map[string]string

func loadMaxTokensFields(reg *providers.Registry) maxTokensFieldIndex {
	idx := maxTokensFieldIndex{}
	if reg == nil || reg.Path == "" {
		return idx
	}
	data, err := os.ReadFile(reg.Path)
	if err != nil {
		return idx
	}
	var doc struct {
		Providers []struct {
			Name   string `json:"name"`
			Models []struct {
				ID             string `json:"id"`
				MaxTokensField string `json:"maxTokensField"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return idx
	}
	for _, p := range doc.Providers {
		for _, m := range p.Models {
			if field := strings.TrimSpace(m.MaxTokensField); field != "" {
				idx[p.Name+"\x00"+m.ID] = field
			}
		}
	}
	return idx
}

// ---------------------------------------------------------------------------
// instrumented transport (§6.4 watchdogs, §9.1 diagnostics)
// ---------------------------------------------------------------------------

// upstreamSignal is a progress event from the instrumented transport.
type upstreamSignal struct {
	headers bool
	status  int
}

// watchdogTransport wraps the shared transport and reports progress so the
// caller can run the §6.4 watchdogs around openaiup.Call, which otherwise
// hides the response lifecycle. It also records the status and a bounded
// prefix of a non-200 body for the §9.1 mapping.
type watchdogTransport struct {
	base http.RoundTripper
	sig  chan<- upstreamSignal

	mu     sync.Mutex
	status int
	body   *progressReader
}

func (t *watchdogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.status = resp.StatusCode
	t.mu.Unlock()
	t.emit(upstreamSignal{headers: true, status: resp.StatusCode})
	reader := &progressReader{
		inner:  resp.Body,
		onRead: func() { t.emit(upstreamSignal{}) },
	}
	t.mu.Lock()
	t.body = reader
	t.mu.Unlock()
	resp.Body = reader
	return resp, nil
}

// emit never blocks: the transport runs on the upstream call's goroutine and
// must not stall because the caller is busy writing.
func (t *watchdogTransport) emit(sig upstreamSignal) {
	select {
	case t.sig <- sig:
	default:
	}
}

// snapshot reports the response lifecycle for classification. It is called
// after the attempt finished, so the reads are stable.
func (t *watchdogTransport) snapshot() (status, reads int, readErr error, snippet string) {
	t.mu.Lock()
	reader := t.body
	t.mu.Unlock()
	if reader == nil {
		return t.statusLocked(), 0, nil, ""
	}
	reads, readErr, snippet = reader.snapshot()
	return t.statusLocked(), reads, readErr, snippet
}

func (t *watchdogTransport) statusLocked() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// progressReader counts reads (idle-watchdog resets), records the first read
// error and keeps a bounded prefix of the body for the §9.1 diagnostic path.
// Every field is guarded by its own mutex because the upstream goroutine writes
// while the caller reads the snapshot.
type progressReader struct {
	inner  io.Reader
	onRead func()

	mu      sync.Mutex
	count   int
	readErr error
	snippet bytes.Buffer
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.inner.Read(b)
	if n > 0 {
		p.mu.Lock()
		p.count++
		if room := snippetBytes + snippetOverlap - p.snippet.Len(); room > 0 {
			if n < room {
				room = n
			}
			p.snippet.Write(b[:room])
		}
		p.mu.Unlock()
		if p.onRead != nil {
			p.onRead()
		}
	}
	if err != nil && err != io.EOF {
		p.mu.Lock()
		if p.readErr == nil {
			p.readErr = err
		}
		p.mu.Unlock()
	}
	return n, err
}

func (p *progressReader) Close() error {
	if c, ok := p.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (p *progressReader) snapshot() (reads int, readErr error, snippet string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count, p.readErr, p.snippet.String()
}

// sharedTransport is the connection pool every attempt shares; the per-attempt
// watchdogTransport wraps it.
var sharedTransport http.RoundTripper = http.DefaultTransport

// ---------------------------------------------------------------------------
// one upstream attempt (§6.4)
// ---------------------------------------------------------------------------

// streamSession drives one upstream attempt: the §6.4 watchdogs and the §8.9
// keep-alive pings. The response is committed lazily on the first write (a
// ping, a data frame or the §9.3 error frame), so a failure that produced
// nothing renderable still answers with its mapped §9.1 status.
type streamSession struct {
	h       *generateHandler
	r       *http.Request
	emitter *sseEmitter // nil on the non-stream branch

	firstByte time.Duration
	idle      time.Duration
	keepAlive time.Duration
}

// upstreamResult is one completed attempt. turn may be a PARTIAL turn: the
// stream branch keeps whatever it folded before a mid-body failure so the §9.3
// error frame can follow the text that did arrive.
type upstreamResult struct {
	turn *openaiup.Turn
	fail *upstreamFailure
}

// run performs a single upstream attempt and returns either the aggregated
// (possibly partial) turn or a classified failure.
func (sess *streamSession) run(parent context.Context, target providers.Target, body []byte) (*openaiup.Turn, *upstreamFailure) {
	callCtx, cancel := context.WithCancel(parent)
	defer cancel()

	signals := make(chan upstreamSignal, 64)
	transport := &watchdogTransport{base: sharedTransport, sig: signals}
	client := &http.Client{Transport: transport}
	redact := sess.h.srv.redactFunc()

	done := make(chan upstreamResult, 1)
	go func() {
		if sess.emitter != nil {
			turn, fail := runStream(callCtx, client, transport, target, body, redact)
			done <- upstreamResult{turn: turn, fail: fail}
			return
		}
		turn, err := openaiup.Call(callCtx, client, target, body, redact)
		done <- upstreamResult{turn: turn, fail: classifyAggregate(transport, err, redact)}
	}()

	firstByte := newWatchdogTimer(sess.firstByte)
	defer firstByte.stop()
	// FIX-32: the idle watchdog is not armed until the response headers
	// arrive. Arming it at request start would govern the pre-headers phase
	// with the (shorter) idle budget instead of the first-byte budget, killing
	// a slow-to-headers provider with a misleading 504.
	idle := newWatchdogTimer(sess.idle)
	idle.stop()
	defer idle.stop()

	// sawStatus is the last upstream status the transport reported; a body read
	// only starts the keep-alive once the upstream answered 200.
	sawStatus := 0
	var keepAlive pinger
	var pingC <-chan time.Time
	// FIX-31: the pinger is stopped on every return path, not only the happy
	// one, so a long-lived adapter does not accumulate runtime timers.
	defer func() {
		if keepAlive != nil {
			keepAlive.stop()
		}
	}()

	for {
		select {
		case <-parent.Done():
			// agy canceled (--print-timeout / Esc): the peer is gone.
			return nil, &upstreamFailure{kind: failureClientGone, err: parent.Err()}
		case res := <-done:
			return res.turn, res.fail
		case sig := <-signals:
			if sig.headers {
				sawStatus = sig.status
				// The response headers satisfy the first-byte budget, and the
				// idle budget starts counting from here.
				firstByte.stop()
				idle.rearm()
				continue
			}
			idle.rearm()
			if sawStatus == http.StatusOK && sess.emitter != nil && keepAlive == nil && sess.keepAlive > 0 {
				keepAlive = newPinger(sess.keepAlive)
				pingC = keepAlive.c()
			}
		case <-firstByte.c:
			cancel()
			return collectPartial(done), &upstreamFailure{kind: failureTimeout, watchdog: "first-byte", err: context.DeadlineExceeded}
		case <-idle.c:
			cancel()
			return collectPartial(done), &upstreamFailure{kind: failureTimeout, watchdog: "idle", err: context.DeadlineExceeded}
		case <-pingC:
			sess.emitter.ping()
		}
	}
}

// pinger is the §8.9 keep-alive ticker seam. It exists so the lifecycle is
// observable in tests (FIX-31): a leaked ticker is invisible through the
// stdlib API, but a test can replace the factory and count stop() calls.
type pinger interface {
	c() <-chan time.Time
	stop()
}

type timePinger struct {
	t *time.Ticker
}

func newTimePinger(d time.Duration) pinger { return &timePinger{t: time.NewTicker(d)} }

func (p *timePinger) c() <-chan time.Time { return p.t.C }
func (p *timePinger) stop()               { p.t.Stop() }

// newPinger is the keep-alive factory. Production uses time.NewTicker; tests
// replace it to observe the create/stop pairing.
var newPinger = func(d time.Duration) pinger { return newTimePinger(d) }

// watchdogTimer is a §6.4 watchdog that can be disabled and re-armed. A
// non-positive budget means "no watchdog" (FIX-30): the Config documents a
// negative value as disabling it, and a disabled timer must never be armed,
// because time.NewTimer with a non-positive duration fires immediately and
// would fail every request with a 504.
type watchdogTimer struct {
	c     <-chan time.Time
	timer *time.Timer
	d     time.Duration
}

func newWatchdogTimer(d time.Duration) *watchdogTimer {
	w := &watchdogTimer{d: d}
	if d > 0 {
		w.rearm()
	}
	return w
}

// rearm starts a fresh budget. It is a no-op for a disabled watchdog, so a
// disabled watchdog can never fire.
func (w *watchdogTimer) rearm() {
	if w == nil || w.d <= 0 {
		return
	}
	w.stop()
	w.timer = time.NewTimer(w.d)
	w.c = w.timer.C
}

// stop disarms the timer permanently and drains a fire that is already pending.
func (w *watchdogTimer) stop() {
	if w == nil || w.timer == nil {
		return
	}
	if !w.timer.Stop() {
		drainTimer(w.timer)
	}
	w.timer = nil
	w.c = nil
}

// partialDrainTimeout bounds how long a fired watchdog waits for the aborted
// call to hand back whatever it folded, so text that did arrive can still be
// emitted before the §9.3 error frame.
const partialDrainTimeout = 500 * time.Millisecond

// collectPartial waits briefly for the aborted upstream call to return its
// partial turn.
func collectPartial(done <-chan upstreamResult) *openaiup.Turn {
	select {
	case res := <-done:
		return res.turn
	case <-time.After(partialDrainTimeout):
		return nil
	}
}

// runStream performs the streaming upstream call. It folds the provider SSE
// itself instead of calling openaiup.Call, because Call returns nil on a
// mid-body failure and discards the partial turn: the §9.3 error frame has to
// follow the text that did arrive (GF-28 pins those bytes). The transport
// discipline is the same one Call applies: no client timeout, a bounded
// redacted error snippet, the 32 MB body cap, and the SSE/JSON content-type
// branch.
func runStream(ctx context.Context, client *http.Client, transport *watchdogTransport, target providers.Target, body []byte, redact func(string) string) (*openaiup.Turn, *upstreamFailure) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Provider.Endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, &upstreamFailure{kind: failureTransport, err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(target.Provider.KeyValue()); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &upstreamFailure{kind: failureClientGone, err: ctx.Err()}
		}
		return nil, &upstreamFailure{kind: failureTransport, err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a bounded prefix so the redactor sees whole secrets, then
		// classify from the recorded status and the captured snippet.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, snippetBytes+snippetOverlap))
		status, _, _, snippet := transport.snapshot()
		return nil, &upstreamFailure{
			kind:    failureStatus,
			status:  status,
			snippet: sanitizeMessage(snippet, redact),
			err:     fmt.Errorf("upstream returned status %d", status),
		}
	}

	turn := openaiup.NewTurn()
	capped := &cappedReader{r: resp.Body, left: openaiup.MaxBodyBytes + 1}
	eventStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	var mergeErr error
	if eventStream {
		mergeErr = turn.MergeSSE(capped)
	} else {
		mergeErr = turn.MergeJSON(capped)
	}
	if capped.exceeded() {
		return turn, &upstreamFailure{kind: failureMalformed, err: fmt.Errorf("upstream stream exceeded the %d byte cap", openaiup.MaxBodyBytes)}
	}
	if mergeErr != nil {
		// A read error mid-body is a transport failure (§9.1); an unparseable
		// body is the malformed row. Both keep the partial turn.
		if isReadError(mergeErr) || ctx.Err() != nil {
			return turn, &upstreamFailure{kind: failureTransport, err: mergeErr}
		}
		return turn, &upstreamFailure{kind: failureMalformed, err: mergeErr}
	}
	// G10F-1: a clean read is not the same as a complete stream. An SSE
	// response that ended with no finish_reason chunk and no [DONE] sentinel was
	// cut short, and the folded turn is a TRUNCATED answer. Surfacing it as a
	// 200 with a synthesized finishReason STOP would make the truncation
	// invisible to agy (it prints the partial text and exits 0), so the §9.1
	// stream-truncated row answers instead and the partial text still rides the
	// §9.3 error frame. The cap check above runs first: a body that outgrew the
	// cap is a cap failure, whatever else it did.
	if eventStream && !turn.StreamTerminal {
		return turn, &upstreamFailure{kind: failureTruncated, err: openaiup.ErrStreamTruncated}
	}
	return turn, nil
}

// isReadError reports whether an error came from reading the body rather than
// from parsing it. MergeSSE surfaces the scanner's read error; MergeJSON wraps
// a read failure and a parse failure with different words.
func isReadError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return !strings.Contains(msg, "parse") && !strings.Contains(msg, "read upstream")
}

// cappedReader bounds an upstream body at MaxBodyBytes plus one byte, so a
// stream that outgrows the cap fails loudly instead of returning a truncated
// turn (the same discipline openaiup.Call applies).
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errBodyCapExceeded
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		c.left = 0
	}
	return n, err
}

func (c *cappedReader) exceeded() bool { return c.left <= 0 }

var errBodyCapExceeded = errors.New("upstream body exceeded the byte cap")

// classifyAggregate turns one finished openaiup.Call (the non-stream branch)
// into a typed failure.
func classifyAggregate(transport *watchdogTransport, err error, redact func(string) string) *upstreamFailure {
	if err == nil {
		return nil
	}
	// G10F-1: the non-stream branch routes through openaiup.Call, which now
	// refuses an SSE body that ended without a terminal signal. Its sentinel is
	// matched before the status/read/malformed ladder so the truncation keeps
	// its own §9.1 row instead of degrading to "unparseable response".
	if errors.Is(err, openaiup.ErrStreamTruncated) {
		return &upstreamFailure{kind: failureTruncated, err: err}
	}
	status, reads, readErr, snippet := transport.snapshot()
	redacted := ""
	if snippet != "" {
		redacted = sanitizeMessage(snippet, redact)
	}
	switch {
	case status == 0 && reads == 0:
		// The request never produced a response.
		return &upstreamFailure{kind: failureTransport, err: err}
	case status != http.StatusOK:
		return &upstreamFailure{kind: failureStatus, status: status, snippet: redacted, err: err}
	case readErr != nil:
		// 200, then the connection dropped mid-body (§9.3).
		return &upstreamFailure{kind: failureTransport, snippet: redacted, err: readErr}
	default:
		return &upstreamFailure{kind: failureMalformed, snippet: redacted, err: err}
	}
}

// drainTimer empties a fired timer's channel so a Reset is well-defined.
func drainTimer(t *time.Timer) {
	select {
	case <-t.C:
	default:
	}
}

// callUpstream runs the §7.13 bounded repair retry around one session: a 400
// whose redacted body names an adapter-added field is retried exactly once with
// that field removed. Nothing is retried once the 200 is committed, and the
// loop never iterates a second time.
func (h *generateHandler) callUpstream(parent context.Context, sess *streamSession, target providers.Target, body []byte) (*openaiup.Turn, *upstreamFailure) {
	// M4-item-4 parity: cap the OUTBOUND body before the provider is contacted.
	// The check lives here rather than in buildUpstreamBody because that path's
	// error is a fixed 500 (generate.go's build failure), while this one maps
	// onto the §9.1 413 row through answerFailure. It runs before the retry loop
	// so an over-cap body is never re-sent.
	if len(body) > openaiup.MaxBodyBytes {
		return nil, &upstreamFailure{
			kind: failureOutboundTooLarge,
			err:  fmt.Errorf("outbound request body is %d bytes, over the %d byte cap", len(body), openaiup.MaxBodyBytes),
		}
	}
	for attempt := 0; ; attempt++ {
		turn, fail := sess.run(parent, target, body)
		if fail == nil {
			return turn, nil
		}
		if fail.kind == failureClientGone {
			return nil, fail
		}
		// A failure keeps whatever the attempt folded: the §9.3 error frame
		// follows the text that did arrive.
		if attempt > 0 || sess.emitter.started() {
			return turn, fail
		}
		dropped := fail.repairFields()
		if len(dropped) == 0 {
			return turn, fail
		}
		ar := auditFromContext(sess.r.Context())
		ar.addNote(fmt.Sprintf("upstream %d; repair retry dropped %s", fail.status, strings.Join(dropped, ",")))
		trimmed, err := dropFields(body, dropped)
		if err != nil {
			ar.addNote("repair retry skipped: " + err.Error())
			return turn, fail
		}
		body = trimmed
	}
}

// dropFields removes the named keys from a JSON object body.
func dropFields(body []byte, fields []string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("body is not a JSON object: %w", err)
	}
	for _, f := range fields {
		delete(doc, f)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshal body: %w", err)
	}
	return out, nil
}

// turnIsEmpty reports whether a turn carries nothing agy could render: no
// content, no tool calls, no refusal and (when surfaced) no reasoning. A
// refusal counts as content because the codec emits it as a text part.
func turnIsEmpty(turn *openaiup.Turn, allowThoughts bool) bool {
	if turn == nil {
		return true
	}
	if turn.Content() != "" || turn.Refusal != "" || len(turn.OrderedCalls()) > 0 {
		return false
	}
	if allowThoughts && turn.Reasoning() != "" {
		return false
	}
	return true
}
