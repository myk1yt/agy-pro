package wireadp

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"agypro/internal/geminiwire"
)

// §5.8 housekeeping: agy's title generator fires before every print run at a
// gemini-*-flash-lite-preview model with no tools. The adapter answers it
// locally and deterministically with zero provider calls (R6: housekeeping
// traffic must never silently spend provider quota).
//
// G10 (blind-qa-g4-rerun2-report.md finding 1) added the second entry: on the
// P1 external-only catalog agy resolves the title model to an EXTERNAL session
// id (observed: ext-gamma-1), so the name-shaped fast-path below never fires
// and the title turn is forwarded to the provider. The title turn is therefore
// ALSO detected by its system-prompt signature (isTitleTurn), which is
// model-name independent and works on both wire surfaces.

const (
	// titleMaxRunes is the §5.8 title cap.
	titleMaxRunes = 60
	// fallbackTitle is emitted when the request carries no usable text: the
	// response still needs at least one part so it is never a textless 200.
	fallbackTitle = "New session"
	// housekeepingPrefix / housekeepingSuffix are the pattern match for the
	// seeded gemini-*-flash-lite-preview family (W5).
	housekeepingPrefix = "gemini-"
	housekeepingSuffix = "-flash-lite-preview"
)

// housekeepingNames is the explicit seed list. The observed title-generator id
// (custommodels-probe-report §5, audit-gap-probe-report #1) is pattern-covered
// too; the list exists for ids that do not match the pattern but are proven
// housekeeping, and is extended by sniff captures (§5.8).
var housekeepingNames = []string{
	"gemini-3.1-flash-lite-preview",
}

// IsHousekeepingName reports whether a model name belongs to the housekeeping
// family (§5.8). An unknown housekeeping-SHAPED name is not guessed: the
// resolution order falls through to the allow-list 404 (§3.3 rule 4).
func IsHousekeepingName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, known := range housekeepingNames {
		if name == known {
			return true
		}
	}
	return strings.HasPrefix(name, housekeepingPrefix) && strings.HasSuffix(name, housekeepingSuffix)
}

// titleGeneratorSignature is the fragment of the system prompt agy's
// conversation-title turn carries. Captured verbatim on the mesh wire on both
// surfaces: the public /v1beta body in
// %TEMP%\agy-auditgap\logs\mock-q1c-gemini-baseline.log #9 and the /v1internal
// envelope in mock-q1a-mesh-plain.log #135 (the same prompt the G4 mock logged
// as the ext-gamma-1 title turn). agy authors this prompt itself, so it is the
// one title-turn signal that does not depend on the model name.
const titleGeneratorSignature = "conversation title generator"

// checkpointRequestType is the /v1internal envelope's requestType on agy's
// conversation-title turn (44/44 captured title envelopes, against
// requestType "agent" on every captured agentic generation). It is the
// G10F2-2 alarm's second, phrase-independent arm: it survives a prompt reword
// because it says nothing about the prompt's text.
const checkpointRequestType = "checkpoint"

// titleTurnShape is the title-turn signal for one request, folded ONCE. The
// dispatch path needs it twice (behind the tools guard for the local answer,
// and with the envelope arm for the drift alarm), and folding an
// attacker-sized SystemInstruction a second time per forwarded turn is pure
// waste.
type titleTurnShape struct {
	// signature is the raw prompt-signature match (G10F-3/G10F2-4).
	signature bool
	// toolsFree is the request declaring no tools (G10F-2).
	toolsFree bool
	// checkpoint is the /v1internal envelope saying requestType "checkpoint"
	// (G10F2-2). Always false on the public /v1beta wire, which carries no
	// envelope.
	checkpoint bool
}

// evalTitleTurnShape folds the title-turn signals for one request.
func evalTitleTurnShape(req *geminiwire.GenerateContentRequest, env *geminiwire.V1InternalRequest) titleTurnShape {
	return titleTurnShape{
		signature:  titleTurnSignature(req),
		toolsFree:  req == nil || len(req.Tools) == 0,
		checkpoint: env != nil && env.RequestType == checkpointRequestType,
	}
}

// local reports whether the turn is agy's conversation-title turn and must be
// answered locally, never forwarded (G10F-2: a tools-bearing turn is a
// generation, whatever its system prompt says).
func (s titleTurnShape) local() bool { return s.signature && s.toolsFree }

// forwarded reports whether a turn that is being sent to a provider has the
// SHAPE of a conversation-title turn. This is the G10F-6 / G10F2-2 drift
// alarm's trigger.
//
// It has two INDEPENDENT arms on purpose, so one agy change cannot disable
// both. The prompt signature works on both wire surfaces but is defeated by a
// prompt reword, which is precisely the drift class the alarm exists to catch;
// the envelope requestType is independent of the prompt's text but exists only
// on /v1internal, because the public /v1beta wire carries no envelope. So the
// alarm is phrase-keyed on /v1beta and doubly-keyed on /v1internal, and a
// reworded checkpoint title turn is still greppable on the surface where agy
// actually resolves the title model to an external.
//
// The asymmetry is deliberate: a false alarm costs one string on an
// already-masked audit line, while a missed one costs silent provider quota on
// housekeeping traffic.
func (s titleTurnShape) forwarded() bool {
	return s.signature || (s.checkpoint && s.toolsFree)
}

// titleTurnSignature reports whether the request's system instruction carries
// the title-generator signature. It is the RAW signal: the tools guard and the
// envelope arm are applied by titleTurnShape on top of it, and keeping them
// apart is what makes the local answer and the drift alarm independently
// testable.
//
// Only SystemInstruction is scanned. The user's transcript rides in Contents,
// so a user who pastes this phrase into a prompt cannot turn a real agentic
// turn into a local title answer. The parts are JOINED with a space before the
// match (G10F-3): agy serializing the prompt across two parts would otherwise
// split the phrase at the boundary and silently re-open the G4-4 quota spend.
// The joined text is then whitespace-COLLAPSED (G10F2-4), so a reformatted
// phrase (a double space, a newline where the capture had a space) and a split
// that keeps the boundary space in the left part both still match.
func titleTurnSignature(req *geminiwire.GenerateContentRequest) bool {
	if req == nil || req.SystemInstruction == nil {
		return false
	}
	var joined strings.Builder
	for i := range req.SystemInstruction.Parts {
		if i > 0 {
			joined.WriteString(" ")
		}
		joined.WriteString(req.SystemInstruction.Parts[i].Text)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(joined.String())), " ")
	return strings.Contains(normalized, titleGeneratorSignature)
}

// isTitleTurn reports whether req is agy's conversation-title turn (design §2
// P1/P2 housekeeping policy; R6: housekeeping traffic must never silently spend
// provider quota).
//
// The match is the system-prompt signature plus the tools-empty guard. The
// signature is model-name independent, which is the point: on the P1
// external-only catalog agy resolves the title model to an external session id
// (observed: ext-gamma-1), so IsHousekeepingName, which keys on the model name,
// never fires and the title turn would be forwarded to the provider. No
// model-name rule can close that hole; a prompt rule can.
//
// The tools guard (G10F-2) is what keeps the rule from over-matching. Every
// captured title turn carries ZERO tools (44/44) and every captured agentic
// turn carries 7-13, so a turn that declares tools is a generation and must
// never be answered locally, whatever its system prompt says. That closes the
// denial-of-real-generation vector the round-6 reviewers raised: agy composes
// its agentic system instruction from environment-derived text (a workspace
// path, and on some path rules text), so the phrase can appear in a
// tools-bearing turn's system prompt. The trade is deliberate and one-sided: a
// future tool-bearing title turn is missed and spends quota, which the
// drift alarm makes visible, instead of starving a real turn silently.
//
// This is the single-request convenience seam. dispatchGenerate evaluates
// evalTitleTurnShape itself, because it needs the same fold for the drift alarm
// on the forwarding path and folding an attacker-sized SystemInstruction twice
// per turn is pure waste.
func isTitleTurn(req *geminiwire.GenerateContentRequest) bool {
	return evalTitleTurnShape(req, nil).local()
}

// serveHousekeeping answers a housekeeping request locally (§5.8, GF-29): the
// stream verbs emit the canonical SSE sequence, the non-stream verb one JSON
// body. Both run through the geminiwire emitters, so the never-textless-200
// choke point and the final-chunk finishReason rule are shared with the
// generation path. On the /v1internal surface every frame is re-wrapped in the
// Cloud Code envelope (Appendix A), exactly as the generation path does: agy's
// CodeAssist client parses nothing else there, and P1 is the surface the title
// turn actually arrives on (G10).
//
// The framing decision mirrors the generation path's streamResponseIsSSE
// (stream.go): the stream verb OR a stray ?alt=sse on either verb asks for SSE
// (G10F-4). Threading the already-computed altSSE in is what keeps a
// :generateContent?alt=sse housekeeping turn framed like the generation path
// instead of answering a JSON body to an SSE client.
func (s *Server) serveHousekeeping(w http.ResponseWriter, verb string, altSSE bool, req *geminiwire.GenerateContentRequest, env *geminiwire.V1InternalRequest) {
	resp := housekeepingResponse(req)
	if verb == verbStreamGenerate || altSSE {
		if env != nil {
			writeSSE(w, geminiwire.NewV1InternalStream(geminiwire.WrapStreamChunks).Frames(resp, ""))
			return
		}
		writeSSE(w, geminiwire.StreamFrames(resp, ""))
		return
	}
	terminal := geminiwire.EnsureTerminal(resp, "")
	if env != nil {
		out, err := geminiwire.WrapV1InternalResponse(terminal)
		if err != nil {
			s.writeGoogleError(w, http.StatusInternalServerError, statusInternal, "failed to wrap the /v1internal response")
			return
		}
		writeRawJSON(w, out)
		return
	}
	writeJSON(w, http.StatusOK, terminal)
}

// housekeepingResponse builds the §5.8 local title response: one text part
// with finishReason STOP and a minimal usageMetadata whose candidate count is
// the title's rune length (the GF-29 pinned bytes).
func housekeepingResponse(req *geminiwire.GenerateContentRequest) *geminiwire.GenerateContentResponse {
	title := localTitle(req)
	runes := utf8.RuneCountInString(title)
	return &geminiwire.GenerateContentResponse{
		Candidates: []geminiwire.Candidate{{
			Content:      &geminiwire.Content{Role: "model", Parts: []geminiwire.Part{{Text: title}}},
			Index:        0,
			FinishReason: "STOP",
		}},
		UsageMetadata: &geminiwire.UsageMetadata{
			PromptTokenCount:     0,
			CandidatesTokenCount: runes,
			TotalTokenCount:      runes,
		},
	}
}

// localTitle extracts the first non-empty user text part, strips control
// characters and truncates to 60 runes (§5.8). The scan order matches the
// GF-29 golden runner exactly: contents in order, parts in order, first
// non-empty text wins.
func localTitle(req *geminiwire.GenerateContentRequest) string {
	if req == nil {
		return fallbackTitle
	}
	text := ""
	for ci := range req.Contents {
		for pi := range req.Contents[ci].Parts {
			if p := &req.Contents[ci].Parts[pi]; p.Text != "" {
				text = p.Text
				break
			}
		}
		if text != "" {
			break
		}
	}
	var b strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	runes := []rune(b.String())
	if len(runes) > titleMaxRunes {
		runes = runes[:titleMaxRunes]
	}
	title := string(runes)
	if title == "" {
		return fallbackTitle
	}
	return title
}
