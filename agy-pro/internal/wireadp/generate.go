package wireadp

// The default generation handler: translate the Gemini request to the OpenAI
// wire, call the provider under the §6.4 watchdogs, and answer in Gemini
// dialect (§6, §7, §8, §9). The §3.3 model resolution, the allow-list 404 and
// the housekeeping short-circuit already ran in routes.go's dispatchGenerate,
// so this handler always receives a resolved target.

import (
	"net/http"
	"sync"
	"time"

	"agypro/internal/geminiwire"
	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// generateHandler is the installed default GenerateHandler.
type generateHandler struct {
	srv *Server

	maxTokensOnce   sync.Once
	maxTokensFields maxTokensFieldIndex
}

// Generate runs one generation request end to end.
func (h *generateHandler) Generate(w http.ResponseWriter, r *http.Request, g *GenerateContext) error {
	body, allowThoughts, notes, err := h.buildUpstreamBody(g)
	if err != nil {
		return &GenerateError{Code: http.StatusInternalServerError, Status: statusInternal, Message: "failed to build the upstream request"}
	}
	if ar := auditFromContext(r.Context()); ar != nil {
		for _, note := range notes {
			ar.addNote(note)
		}
	}

	provider := g.Target.Provider.Name
	emitter := (*sseEmitter)(nil)
	if streamResponseIsSSE(g) {
		emitter = newSSEEmitter(w)
	}
	sess := &streamSession{
		h:         h,
		r:         r,
		emitter:   emitter,
		firstByte: h.srv.firstByteTimeout(),
		idle:      h.srv.idleTimeout(),
		keepAlive: h.srv.keepAliveInterval(),
	}

	turn, fail := h.callUpstream(r.Context(), sess, g.Target, body)
	if fail != nil {
		return h.answerFailure(r, g, sess, turn, fail, allowThoughts)
	}

	// §9.2: the never-textless-200 guarantee comes from the codec's choke
	// point, so the adapter only supplies the cause. A turn agy could not
	// render becomes the fixed apology with the provider name appended, and a
	// 200 is never textless. The name is a registry slot, so it masks through
	// MaskSlot (FIX-8: the registry-slot authority, MaskOperand plus the
	// underscore-head credential arm) and is stripped of control characters
	// (FIX-36): this success path bypasses sanitizeMessage, and a key-shaped or
	// escape-bearing registry provider name must not reach agy's terminal raw.
	cause := "provider " + oneLine(providers.MaskSlot(provider)) + ": empty upstream response"
	resp, mapNotes := geminiwire.TurnToGenerateResponseWithCause(turn, allowThoughts, turn.FinishReason, cause)
	if ar := auditFromContext(r.Context()); ar != nil {
		for _, note := range mapNotes {
			ar.addNote(note)
		}
	}

	if streamResponseIsSSE(g) {
		h.writeStreamResponse(emitter, g, resp, "")
		return nil
	}
	h.writeNonStreamResponse(w, g, resp, "")
	return nil
}

// answerFailure maps an upstream failure onto the wire. When the stream
// produced text agy could render, that text is emitted first and the same
// envelope rides the §9.3 error frame, never a fake success. When the stream
// produced nothing but was already committed by a keep-alive ping, the fixed
// §9.2 apology is emitted before the error frame so a committed 200 is never
// textless. When nothing was written yet, the mapped §9.1 status answers
// instead. agy walking away writes nothing at all.
func (h *generateHandler) answerFailure(r *http.Request, g *GenerateContext, sess *streamSession, turn *openaiup.Turn, fail *upstreamFailure, allowThoughts bool) error {
	if fail.kind == failureClientGone {
		return nil
	}
	// FIX-8: the §9.1 envelope table (errorwriter.go's providerErrorRows)
	// interpolates the provider NAME and, on the 404 row, the model ID straight
	// into the message, so this call site is the composition point for both
	// values. Both are registry slots, so each is masked through MaskSlot (the
	// registry-slot authority) before the template sees it: an underscore-head
	// credential class the operand masker cannot classify would otherwise reach
	// agy's terminal raw inside "provider <name>: ...". Benign names and ids
	// render byte-identically.
	code, status, message := fail.envelope(providers.MaskSlot(g.Target.Provider.Name), providers.MaskSlot(g.Target.Model.ID))
	if ar := auditFromContext(r.Context()); ar != nil {
		ar.addNote("upstream failed: " + status)
	}
	if sess.emitter == nil {
		// The non-stream branch writes one body at the end, so a failure is
		// always a mapped status.
		return &GenerateError{Code: code, Status: status, Message: message}
	}
	if !turnIsEmpty(turn, allowThoughts) {
		// §9.3: the text that did arrive is emitted, then the error frame.
		resp, _ := geminiwire.TurnToGenerateResponse(turn, allowThoughts, turn.FinishReason)
		h.writePartialStream(sess.emitter, g, resp, "")
		sess.emitter.errorFrame(h.srv, code, status, message)
		return nil
	}
	if sess.emitter.started() {
		// Keep-alive comments already committed the stream, so the mapped status
		// is no longer available and the §9.3 error frame answers instead.
		//
		// G10F2-3: an error-frame-only 200 is §9.2's textless shape, and after
		// G10F-1 it became the COMMON truncation output (a role-only delta, then
		// a stall past the keep-alive interval, then a clean close with no
		// terminal frame). agy's reaction to that shape has never been measured,
		// so the SAME apology the clean-empty path synthesizes is emitted first:
		// an empty turn is exactly what TurnToGenerateResponseWithCause maps onto
		// it, and writePartialStream drops only the synthesized terminator, so no
		// finishReason is added. GF-28's bytes are untouched, because that path
		// carries real partial text and takes the branch above. The error frame
		// that follows is what names the specific cause.
		cause := "provider " + oneLine(providers.MaskSlot(g.Target.Provider.Name)) + ": empty upstream response"
		apology, _ := geminiwire.TurnToGenerateResponseWithCause(openaiup.NewTurn(), allowThoughts, "", cause)
		h.writePartialStream(sess.emitter, g, apology, "")
		sess.emitter.errorFrame(h.srv, code, status, message)
		return nil
	}
	return &GenerateError{Code: code, Status: status, Message: message}
}

// watchdogs and keep-alive, resolved from Config with the §6.4 / §8.9 defaults.
// A value <= 0 disables the corresponding watchdog or the keep-alive entirely
// (FIX-30): the request then relies on the inbound context plus agy's own
// --print-timeout. Returning the negative verbatim would arm an instantly
// firing timer (every request 504) or panic time.NewTicker.

func (s *Server) firstByteTimeout() time.Duration {
	if s.cfg.UpstreamFirstByteTimeout < 0 {
		return 0
	}
	if s.cfg.UpstreamFirstByteTimeout > 0 {
		return s.cfg.UpstreamFirstByteTimeout
	}
	return defaultFirstByteTimeout
}

func (s *Server) idleTimeout() time.Duration {
	if s.cfg.UpstreamIdleTimeout < 0 {
		return 0
	}
	if s.cfg.UpstreamIdleTimeout > 0 {
		return s.cfg.UpstreamIdleTimeout
	}
	return defaultIdleTimeout
}

func (s *Server) keepAliveInterval() time.Duration {
	if s.cfg.StreamKeepAliveInterval < 0 {
		return 0
	}
	if s.cfg.StreamKeepAliveInterval > 0 {
		return s.cfg.StreamKeepAliveInterval
	}
	return defaultKeepAliveInterval
}
