package geminiwire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// internalPrefix is the Cloud Code RPC path prefix (Appendix A).
const internalPrefix = "/v1internal"

// UnwrapV1Internal decodes a /v1internal request body and returns the envelope
// plus the inner GenerateContentRequest (Appendix A). The Cloud Code
// generationConfig.responseSchema is renamed onto the public responseJsonSchema
// on the way in, so every downstream mapping row sees exactly one field and the
// §7 tables stay envelope-independent.
func UnwrapV1Internal(body []byte) (*V1InternalRequest, *GenerateContentRequest, error) {
	var env V1InternalRequest
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, nil, fmt.Errorf("invalid /v1internal envelope: %w", err)
	}
	if len(env.Request) == 0 {
		return nil, nil, fmt.Errorf("invalid /v1internal envelope: no request payload")
	}
	inner, err := DecodeGenerateRequest(env.Request)
	if err != nil {
		return nil, nil, err
	}
	renameResponseSchema(inner)
	return &env, inner, nil
}

// renameResponseSchema moves generationConfig.responseSchema (the Cloud Code
// spelling) onto responseJsonSchema (the public spelling). A public value that
// is already present wins, and the Cloud Code field is cleared so the inner
// request carries the rename rather than both spellings.
func renameResponseSchema(req *GenerateContentRequest) {
	if req == nil || req.GenerationConfig == nil {
		return
	}
	gc := req.GenerationConfig
	if gc.ResponseJsonSchema == nil {
		gc.ResponseJsonSchema = gc.ResponseSchema
	}
	gc.ResponseSchema = nil
}

// WrapV1InternalResponse wraps a non-stream GenerateContentResponse in the Cloud
// Code envelope: {"response": <inner>} (Appendix A).
func WrapV1InternalResponse(inner *GenerateContentResponse) ([]byte, error) {
	return wrapEnvelope(inner)
}

// WrapV1InternalStreamChunk wraps one stream chunk in the Cloud Code envelope.
// The wire shape is identical to the non-stream wrap; the separate entry point
// exists because the stream wrap mode is a constructor flag on the stream path
// (Appendix A, [P? QO]).
func WrapV1InternalStreamChunk(inner *GenerateContentResponse) ([]byte, error) {
	return wrapEnvelope(inner)
}

func wrapEnvelope(inner *GenerateContentResponse) ([]byte, error) {
	payload, err := json.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("marshal inner response: %w", err)
	}
	out, err := json.Marshal(V1InternalResponse{Response: payload})
	if err != nil {
		return nil, fmt.Errorf("marshal /v1internal envelope: %w", err)
	}
	return out, nil
}

// StreamWrapMode selects how the /v1internal stream branch wraps each chunk
// (Appendix A). Whether agy's Cloud Code client wants {"response":…} per chunk
// or the bare chunk is [P? QO], so the mode is a flag rather than a hard-coded
// choice.
type StreamWrapMode int

const (
	// WrapStreamChunks nests every chunk in {"response": <chunk>}.
	WrapStreamChunks StreamWrapMode = iota
	// RawStreamChunks emits the bare GenerateContentResponse chunk.
	RawStreamChunks
)

// V1InternalStream frames the envelope branch's SSE output in one wrap mode.
type V1InternalStream struct {
	mode StreamWrapMode
}

// NewV1InternalStream returns a stream framer for the given wrap mode.
func NewV1InternalStream(mode StreamWrapMode) *V1InternalStream {
	return &V1InternalStream{mode: mode}
}

// Frames renders resp as envelope-branch SSE frames. Both wrap modes run
// through StreamChunks, so the never-textless-200 choke point and the
// final-chunk finishReason rule are identical on the envelope and the public
// surface.
func (s *V1InternalStream) Frames(resp *GenerateContentResponse, apology string) []string {
	chunks := StreamChunks(resp, apology)
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if s.mode == RawStreamChunks {
			out = append(out, Frame(c))
			continue
		}
		payload, err := WrapV1InternalStreamChunk(c)
		if err != nil {
			out = append(out, Frame(c))
			continue
		}
		out = append(out, "data: "+string(payload)+"\n\n")
	}
	return out
}

// ParseInternalRoute parses a /v1internal RPC path and returns the verb without
// its leading colon (Appendix A). The envelope branch shares the codec; only
// the wrapper differs.
func ParseInternalRoute(rawPath string) (string, error) {
	path := rawPath
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, v := range generateVerbs {
		if !strings.HasSuffix(path, v) {
			continue
		}
		if strings.TrimSuffix(path, v) != internalPrefix {
			return "", fmt.Errorf("route %q is not a /v1internal RPC", rawPath)
		}
		return strings.TrimPrefix(v, ":"), nil
	}
	return "", fmt.Errorf("route %q has no generate verb suffix", rawPath)
}
