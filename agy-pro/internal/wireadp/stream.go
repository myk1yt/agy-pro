package wireadp

// Response emission (spec §8): the Gemini SSE framing, the §8.9 keep-alive
// comments, the §9.3 mid-stream error frame and the non-stream JSON twin. The
// chunk sequence itself is geminiwire.StreamChunks, so the never-textless-200
// choke point and the final-chunk finishReason rule are shared with the codec.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"agypro/internal/geminiwire"
)

// sseEmitter writes one Gemini SSE response. The 200 and the SSE headers are
// committed lazily by the first write (a keep-alive comment, a data frame or
// the §9.3 error frame), so a failure that produced nothing renderable can
// still answer with its mapped §9.1 status instead.
type sseEmitter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	begun   bool
}

func newSSEEmitter(w http.ResponseWriter) *sseEmitter {
	flusher, _ := w.(http.Flusher)
	return &sseEmitter{w: w, flusher: flusher}
}

// started reports whether anything has been written to the wire yet.
func (e *sseEmitter) started() bool { return e != nil && e.begun }

// begin commits the response: 200, text/event-stream, no cache (§8.1).
func (e *sseEmitter) begin() {
	if e == nil || e.begun {
		return
	}
	e.begun = true
	h := e.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	e.w.WriteHeader(http.StatusOK)
	e.flush()
}

// ping writes one §8.9 keep-alive comment. Every SSE parser ignores it, so it
// is safe during a long upstream silence. Writing it commits the response: a
// stream that has been kept alive can no longer answer with a mapped status.
func (e *sseEmitter) ping() {
	if e == nil {
		return
	}
	e.begin()
	_, _ = io.WriteString(e.w, geminiwire.KeepAliveComment())
	e.flush()
}

// frames writes the canonical data frames, one JSON payload per data line,
// flushed per frame (§8.1, §8.2). No frame ever carries a [DONE] sentinel.
func (e *sseEmitter) frames(frames []string) {
	if e == nil {
		return
	}
	e.begin()
	for _, frame := range frames {
		if _, err := io.WriteString(e.w, frame); err != nil {
			return
		}
		e.flush()
	}
}

// errorFrame emits the §9.3 mid-stream failure: the same Google error envelope
// the status path writes, as one data frame, then the stream closes. It is only
// reachable after begin, so it never fakes a success.
func (e *sseEmitter) errorFrame(srv *Server, code int, status, message string) {
	if e == nil || !e.begun {
		return
	}
	payload, err := json.Marshal(srv.googleErrorBody(code, status, message))
	if err != nil {
		return
	}
	_, _ = io.WriteString(e.w, "data: "+string(payload)+"\n\n")
	e.flush()
}

func (e *sseEmitter) flush() {
	if e != nil && e.flusher != nil {
		e.flusher.Flush()
	}
}

// writePartialStream emits the chunks a failed stream did produce, without the
// synthesized final chunk: §9.3 drops the success terminator and appends the
// error frame instead (GF-28 pins those bytes). It uses the session's emitter,
// so the error frame that follows sees the stream as already begun.
func (h *generateHandler) writePartialStream(emitter *sseEmitter, g *GenerateContext, resp *geminiwire.GenerateContentResponse, apology string) {
	chunks := geminiwire.StreamChunks(resp, apology)
	if len(chunks) == 0 {
		return
	}
	frames := make([]string, 0, len(chunks)-1)
	for _, chunk := range chunks[:len(chunks)-1] {
		if g.Envelope != nil {
			payload, err := geminiwire.WrapV1InternalStreamChunk(chunk)
			if err != nil {
				frames = append(frames, geminiwire.Frame(chunk))
				continue
			}
			frames = append(frames, "data: "+string(payload)+"\n\n")
			continue
		}
		frames = append(frames, geminiwire.Frame(chunk))
	}
	emitter.frames(frames)
}

// writeStreamResponse emits one aggregated turn on the stream branch. The
// envelope branch re-wraps every chunk in {"response":...} (Appendix A); the
// public surface emits the bare chunk. Both go through geminiwire's chunk
// sequence, so the choke point is identical.
func (h *generateHandler) writeStreamResponse(emitter *sseEmitter, g *GenerateContext, resp *geminiwire.GenerateContentResponse, apology string) {
	if g.Envelope != nil {
		emitter.frames(geminiwire.NewV1InternalStream(geminiwire.WrapStreamChunks).Frames(resp, apology))
		return
	}
	emitter.frames(geminiwire.StreamFrames(resp, apology))
}

// writeNonStreamResponse emits one aggregated turn as a single JSON body: the
// envelope branch re-wraps it, the public surface writes it bare. EnsureTerminal
// keeps the never-textless-200 rule on this path too.
func (h *generateHandler) writeNonStreamResponse(w http.ResponseWriter, g *GenerateContext, resp *geminiwire.GenerateContentResponse, apology string) {
	inner := geminiwire.EnsureTerminal(resp, apology)
	if g.Envelope != nil {
		out, err := geminiwire.WrapV1InternalResponse(inner)
		if err != nil {
			h.srv.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "failed to wrap the /v1internal response")
			return
		}
		writeRawJSON(w, out)
		return
	}
	writeJSON(w, http.StatusOK, inner)
}

// streamResponseIsSSE reports whether the inbound asked for SSE framing: the
// stream verb, or a tolerated ?alt=sse on either verb (§5.1).
func streamResponseIsSSE(g *GenerateContext) bool {
	return g.AltSSE || strings.EqualFold(g.Verb, verbStreamGenerate)
}
