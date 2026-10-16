package geminiwire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"agypro/internal/openaiup"
)

// DefaultApology is the fixed §9.2 apology prefix the codec synthesizes when a
// turn produced nothing user-visible. It carries no trailing period because the
// spec's form continues with ": provider <name>: <redacted one-line cause>"; the
// adapter supplies that suffix through TurnToGenerateResponseWithCause, and
// DefaultApologyCause stands in when a caller has none.
const DefaultApology = "I'm sorry, but I could not finish this turn"

// DefaultApologyCause is the generic cause appended to DefaultApology when the
// caller supplies none. The codec knows no provider name, so its own fallback
// stays cause-shaped but unnamed rather than inventing one (§9.2).
const DefaultApologyCause = "the provider produced no content for this turn"

// apologyText composes the §9.2 apology: the fixed prefix plus the caller's
// redacted "provider <name>: <cause>" suffix, or the generic cause.
func apologyText(cause string) string {
	if strings.TrimSpace(cause) == "" {
		return DefaultApology + ": " + DefaultApologyCause
	}
	return DefaultApology + ": " + cause
}

// fallbackFrame is emitted when a response cannot be marshaled at all (a NaN
// inside a programmatically built args map). It keeps the frame parseable and
// terminal rather than letting a textless 200 reach agy.
const fallbackFrame = `data: {"candidates":[{"content":{"role":"model","parts":[]},"index":0,"finishReason":"STOP"}]}` + "\n\n"

// KeepAliveComment renders one SSE comment frame (§8.9). Every SSE parser
// ignores it, so it is safe during a long upstream silence.
func KeepAliveComment() string { return ": ping\n\n" }

// Frame renders one response as a single Gemini SSE data frame (§8.2): one
// single-line JSON payload per data line, terminated by a blank line. No frame
// ever carries a [DONE] sentinel: this wire terminates by closing the stream.
func Frame(resp *GenerateContentResponse) string {
	payload, err := json.Marshal(resp)
	if err != nil {
		return fallbackFrame
	}
	return "data: " + string(payload) + "\n\n"
}

// StreamChunks splits one response into the canonical Gemini chunk sequence
// (§8.4-§8.5): one chunk per text or thought part, then one chunk per
// functionCall part, then a final empty-parts chunk carrying finishReason and
// usageMetadata.
//
// Its apology branch is the raw §9.2 choke point for a response that arrives
// with no parts and no finishReason at all; the canonical path never reaches it,
// because TurnToGenerateResponse synthesizes the apology before mapping a finish
// reason, so it stays a defense-in-depth guard for hand-built responses. Only
// the first candidate is emitted; the adapter runs a single candidate per turn.
// The input is never modified.
func StreamChunks(resp *GenerateContentResponse, apology string) []*GenerateContentResponse {
	finish := ""
	index := 0
	var parts []Part
	var usage *UsageMetadata
	var responseID string
	if resp != nil {
		usage = resp.UsageMetadata
		responseID = resp.ResponseID
		if len(resp.Candidates) > 0 {
			c := &resp.Candidates[0]
			finish = c.FinishReason
			index = c.Index
			if c.Content != nil {
				parts = append(parts, c.Content.Parts...)
			}
		}
	}
	if len(parts) == 0 && finish == "" {
		if apology == "" {
			apology = apologyText("")
		}
		for _, piece := range openaiup.SplitPieces(apology, synthesizedChunkBytes) {
			parts = append(parts, Part{Text: piece})
		}
		finish = "STOP"
	}
	if finish == "" {
		finish = "STOP"
	}

	chunks := make([]*GenerateContentResponse, 0, len(parts)+1)
	for i := range parts {
		if parts[i].FunctionCall != nil {
			continue
		}
		chunks = append(chunks, partChunks(parts[i], index)...)
	}
	for i := range parts {
		if parts[i].FunctionCall == nil {
			continue
		}
		chunks = append(chunks, chunkFor(parts[i], index))
	}
	final := &GenerateContentResponse{
		Candidates: []Candidate{{
			Content:      &Content{Role: "model", Parts: []Part{}},
			Index:        index,
			FinishReason: finish,
		}},
	}
	if usage != nil {
		final.UsageMetadata = usage
	}
	if responseID != "" {
		final.ResponseID = responseID
	}
	return append(chunks, final)
}

// chunkFor wraps one part in a single-part chunk carrying the model role
// (§8.4).
func chunkFor(p Part, index int) *GenerateContentResponse {
	return &GenerateContentResponse{
		Candidates: []Candidate{{
			Content: &Content{Role: "model", Parts: []Part{p}},
			Index:   index,
		}},
	}
}

// maxPartTextBytes bounds the text one emitted chunk carries. §8.8 requires any
// text the adapter must split above 64 KB to go through
// openaiup.SplitPieces, so a merged turn's text never becomes one unbounded
// data line (FIX-43).
const maxPartTextBytes = 64 << 10

// partChunks renders one non-functionCall part as chunks. A text part longer
// than maxPartTextBytes is cut at rune boundaries into ≤64 KB pieces, one chunk
// each, so every emitted data line stays bounded and no half-rune ever reaches
// json.Marshal (which would coerce it to U+FFFD). Every other part is one chunk.
func partChunks(p Part, index int) []*GenerateContentResponse {
	if p.FunctionCall != nil || len(p.Text) <= maxPartTextBytes {
		return []*GenerateContentResponse{chunkFor(p, index)}
	}
	pieces := openaiup.SplitPieces(p.Text, maxPartTextBytes)
	out := make([]*GenerateContentResponse, 0, len(pieces))
	for _, piece := range pieces {
		split := p
		split.Text = piece
		out = append(out, chunkFor(split, index))
	}
	return out
}

// StreamFrames is the canonical emit path: the §8 chunk sequence rendered as
// SSE data frames. No frame carries a [DONE] sentinel and the last frame always
// carries finishReason.
func StreamFrames(resp *GenerateContentResponse, apology string) []string {
	chunks := StreamChunks(resp, apology)
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, Frame(c))
	}
	return out
}

// EnsureTerminal applies the never-textless-200 rule to a non-stream response
// (§9.2): a response whose first candidate has no parts and no finishReason
// gains the apology as final text parts and a STOP finish reason, and a
// candidate without a finishReason gets one. It is the non-stream twin of
// StreamChunks and shares its raw choke point; the canonical stream path is
// covered earlier, by TurnToGenerateResponse. The input is deep-copied, never
// modified.
func EnsureTerminal(resp *GenerateContentResponse, apology string) *GenerateContentResponse {
	if resp == nil {
		resp = &GenerateContentResponse{}
	}
	out := *resp
	out.Candidates = append([]Candidate(nil), resp.Candidates...)
	for i := range out.Candidates {
		if out.Candidates[i].Content == nil {
			continue
		}
		content := *out.Candidates[i].Content
		content.Parts = append([]Part(nil), out.Candidates[i].Content.Parts...)
		out.Candidates[i].Content = &content
	}
	if len(out.Candidates) == 0 {
		out.Candidates = []Candidate{{Content: &Content{Role: "model"}, Index: 0}}
	}
	c := &out.Candidates[0]
	if (c.Content == nil || len(c.Content.Parts) == 0) && c.FinishReason == "" {
		if apology == "" {
			apology = apologyText("")
		}
		pieces := openaiup.SplitPieces(apology, synthesizedChunkBytes)
		parts := make([]Part, 0, len(pieces))
		for _, piece := range pieces {
			parts = append(parts, Part{Text: piece})
		}
		if c.Content == nil {
			c.Content = &Content{Role: "model"}
		}
		c.Content.Parts = parts
		c.FinishReason = "STOP"
	}
	if c.FinishReason == "" {
		c.FinishReason = "STOP"
	}
	return &out
}

// ReadFrames parses a Gemini SSE byte stream back into responses (§8 frame
// reader, for tests and the later sniffer). It skips comment lines, blank lines
// and non-data fields, ignores a stray [DONE] sentinel this wire never emits,
// and parses exactly one JSON payload per data: line.
func ReadFrames(r io.Reader) ([]*GenerateContentResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var out []*GenerateContentResponse
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var resp GenerateContentResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			return out, fmt.Errorf("undecodable data frame: %w", err)
		}
		out = append(out, &resp)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read frames: %w", err)
	}
	return out, nil
}
