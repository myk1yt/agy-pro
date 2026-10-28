package geminiwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"agypro/internal/openaiup"
)

// Property tests PT-1..PT-8 (spec §11.2, gate G1). They mutate and re-split the
// real codec output instead of pinning bytes: the golden fixtures pin the
// contract, these prove the contract survives round trips, chunk boundaries,
// rune splits, resends, fuzzed provider answers and envelope wrapping.

// ---------------------------------------------------------------------------
// echo providers: replay one wire's content as the other wire's input
// ---------------------------------------------------------------------------

// echoOpenAIToGemini replays an OpenAI message list as a Gemini request, the
// way an echo provider hands the conversation back: system becomes the
// systemInstruction, user/assistant/tool become contents, tool calls become
// functionCall parts and tool messages become functionResponse parts carrying
// the paired call's id.
func echoOpenAIToGemini(msgs []OpenAIMessage) *GenerateContentRequest {
	req := &GenerateContentRequest{}
	for _, m := range msgs {
		switch m.Role {
		case "system":
			text, _ := m.Content.(string)
			var parts []Part
			for _, line := range strings.Split(text, "\n") {
				parts = append(parts, Part{Text: line})
			}
			req.SystemInstruction = &Content{Role: "user", Parts: parts}
		case "user":
			req.Contents = append(req.Contents, Content{Role: "user", Parts: echoContentParts(m.Content)})
		case "assistant":
			c := Content{Role: "model"}
			if text, ok := m.Content.(string); ok && text != "" {
				c.Parts = append(c.Parts, Part{Text: text})
			}
			for _, tc := range m.ToolCalls {
				var args map[string]any
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				if args == nil {
					args = map[string]any{}
				}
				c.Parts = append(c.Parts, Part{FunctionCall: &FunctionCall{
					ID:   tc.ID,
					Name: tc.Function.Name,
					Args: args,
				}})
			}
			req.Contents = append(req.Contents, c)
		case "tool":
			content, _ := m.Content.(string)
			req.Contents = append(req.Contents, Content{Role: "model", Parts: []Part{{
				FunctionResponse: &FunctionResponse{
					ID:       m.ToolCallID,
					Name:     m.Name,
					Response: map[string]any{"output": content},
				},
			}}})
		}
	}
	return req
}

// echoContentParts turns one OpenAI message content (string or multimodal
// array) back into Gemini parts, inverting the §7.2 data-URI mapping. Both the
// struct form (straight from ToOpenAIMessages) and the map form (a
// JSON-decoded message list) are accepted.
func echoContentParts(content any) []Part {
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []Part{{Text: v}}
	case []any:
		var parts []Part
		for _, e := range v {
			switch seg := e.(type) {
			case OpenAITextPart:
				if seg.Text != "" {
					parts = append(parts, Part{Text: seg.Text})
				}
			case OpenAIImagePart:
				if p, ok := dataURIToInlineData(seg.ImageURL.URL); ok {
					parts = append(parts, p)
				}
			case map[string]any:
				switch seg["type"] {
				case "text":
					if text, ok := seg["text"].(string); ok && text != "" {
						parts = append(parts, Part{Text: text})
					}
				case "image_url":
					if url, ok := seg["image_url"].(map[string]any)["url"].(string); ok {
						if p, ok := dataURIToInlineData(url); ok {
							parts = append(parts, p)
						}
					}
				}
			}
		}
		return parts
	}
	return nil
}

// dataURIToInlineData inverts the §7.2 data-URI image mapping.
func dataURIToInlineData(url string) (Part, bool) {
	rest := strings.TrimPrefix(url, "data:")
	mime, b64, found := strings.Cut(rest, ";base64,")
	if !found {
		return Part{}, false
	}
	return Part{InlineData: &InlineData{MimeType: mime, Data: b64}}, true
}

// echoGeminiToTurn replays a Gemini response as an OpenAI turn, the way an echo
// provider answers with exactly what the adapter emitted: thought parts become
// reasoning, text parts become content, functionCall parts become tool calls
// and usageMetadata becomes the OpenAI usage block.
func echoGeminiToTurn(resp *GenerateContentResponse) *openaiup.Turn {
	turn := openaiup.NewTurn()
	var text, reasoning strings.Builder
	if resp != nil {
		for _, cand := range resp.Candidates {
			if cand.Content == nil {
				continue
			}
			for _, p := range cand.Content.Parts {
				switch {
				case p.FunctionCall != nil:
					turn.MergeToolCall(&openaiup.ToolCall{
						ID:   p.FunctionCall.ID,
						Type: "function",
						Function: openaiup.ToolCallFunction{
							Name:      p.FunctionCall.Name,
							Arguments: compactJSON(p.FunctionCall.Args),
						},
					})
				case p.Thought:
					reasoning.WriteString(p.Text)
				default:
					text.WriteString(p.Text)
				}
			}
		}
		if u := resp.UsageMetadata; u != nil {
			turn.Usage = usageToOpenAI(u)
		}
	}
	turn.SetContent(text.String())
	turn.SetReasoning(reasoning.String())
	return turn
}

// usageToOpenAI inverts mapUsage so an echoed usage block survives the round
// trip unchanged.
func usageToOpenAI(u *UsageMetadata) map[string]any {
	m := map[string]any{
		"prompt_tokens":     u.PromptTokenCount,
		"completion_tokens": u.CandidatesTokenCount,
		"total_tokens":      u.TotalTokenCount,
	}
	if u.ThoughtsTokenCount != 0 {
		m["completion_tokens_details"] = map[string]any{"reasoning_tokens": u.ThoughtsTokenCount}
	}
	if u.CachedContentTokenCount != 0 {
		m["prompt_tokens_details"] = map[string]any{"cached_tokens": u.CachedContentTokenCount}
	}
	return m
}

// respPair is one provider answer: the mapped Gemini response plus the raw
// finish_reason captured while merging.
type respPair struct {
	resp   *GenerateContentResponse
	finish string
}

// rowResponses runs the response direction for every fixture row that carries a
// provider answer.
func rowResponses(t *testing.T, row gfRow) []respPair {
	t.Helper()
	switch row.kind {
	case kindStream, kindStreamApology, kindStreamError:
		body := readGolden(t, row.id, "request.sse")
		return []respPair{{streamResponse(t, row, body), mergeStream(t, row, body).FinishReason}}
	case kindTitle:
		body := readGolden(t, row.id, "request.json")
		return []respPair{{titleResponse(t, row, body), "stop"}}
	case kindResponse:
		var out []respPair
		for _, c := range requestCases(t, row) {
			var chunk openaiup.Chunk
			if err := json.Unmarshal(c, &chunk); err != nil {
				t.Fatalf("%s: decode provider body: %v", row.id, err)
			}
			turn := openaiup.NewTurn()
			turn.MergeChunk(&chunk)
			out = append(out, respPair{providerResponse(t, row, c), turn.FinishReason})
		}
		return out
	}
	return nil
}

// ---------------------------------------------------------------------------
// PT-1 round-trip idempotence
// ---------------------------------------------------------------------------

func TestProperty_PT1_RoundTripIdempotence(t *testing.T) {
	// (a) request direction: gemini -> openai -> echo provider -> gemini ->
	// openai must be a fixpoint on messages (roles, merged text) and tool
	// ids/names/args.
	for _, row := range gfRows {
		var bodies [][]byte
		switch row.kind {
		case kindRequest:
			bodies = requestCases(t, row)
		case kindTitle:
			bodies = [][]byte{readGolden(t, row.id, "request.json")}
		case kindEnvelope:
			var inbound struct {
				Envelope json.RawMessage `json:"envelope"`
			}
			if err := json.Unmarshal(readGolden(t, row.id, "request.json"), &inbound); err != nil {
				t.Fatalf("%s: decode inbound: %v", row.id, err)
			}
			_, inner, err := UnwrapV1Internal(inbound.Envelope)
			if err != nil {
				t.Fatalf("%s: UnwrapV1Internal: %v", row.id, err)
			}
			b, err := json.Marshal(inner)
			if err != nil {
				t.Fatalf("%s: marshal inner: %v", row.id, err)
			}
			bodies = [][]byte{b}
		default:
			continue
		}
		for _, body := range bodies {
			req, err := DecodeGenerateRequest(body)
			if err != nil {
				t.Fatalf("%s: DecodeGenerateRequest: %v", row.id, err)
			}
			msgs1, _ := ToOpenAIMessages(req, row.vision)
			msgs2, _ := ToOpenAIMessages(echoOpenAIToGemini(msgs1), row.vision)
			if !messagesFixpointEqual(t, msgs1, msgs2) {
				t.Errorf("%s: request-direction fixpoint broken\nfirst:  %s\nsecond: %s",
					row.id, mustJSON(t, msgs1), mustJSON(t, msgs2))
			}
		}
	}

	// (b) response direction: provider -> gemini -> echo provider -> provider ->
	// gemini must be a fixpoint on the aggregate (thought text, plain text,
	// ordered calls, finish reason, usage). The echo replays the same upstream
	// answer, so the raw finish_reason is the one captured from the provider
	// side. The comparison is aggregate because the echo is lossy on text-part
	// boundaries (an OpenAI content field is one string); the part structure
	// itself is pinned byte-for-byte by the golden rows and the unit tests.
	for _, row := range gfRows {
		for _, pair := range rowResponses(t, row) {
			turn2 := echoGeminiToTurn(pair.resp)
			resp2, _ := TurnToGenerateResponse(turn2, row.thoughts, pair.finish)
			if k1, k2 := responseKey(pair.resp), responseKey(resp2); k1 != k2 {
				t.Errorf("%s: response-direction fixpoint broken\nfirst:  %s\nsecond: %s",
					row.id, k1, k2)
			}
		}
	}
}

// responseKey renders one response as the aggregate PT-1(b) compares: per
// candidate the thought text, the plain text, the ordered tool calls, the finish
// reason and the index, then the usage block.
func responseKey(resp *GenerateContentResponse) string {
	if resp == nil {
		return "<nil>"
	}
	var b strings.Builder
	for _, cand := range resp.Candidates {
		var thought, text strings.Builder
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				switch {
				case p.FunctionCall != nil:
					text.WriteString("\x00call:" + p.FunctionCall.ID + ":" + p.FunctionCall.Name + ":" + compactJSON(p.FunctionCall.Args))
				case p.Thought:
					thought.WriteString(p.Text)
				default:
					text.WriteString(p.Text)
				}
			}
		}
		fmt.Fprintf(&b, "|cand%d thought=%q text=%q finish=%q", cand.Index, thought.String(), text.String(), cand.FinishReason)
	}
	if resp.UsageMetadata == nil {
		b.WriteString("|usage <nil>")
	} else {
		u := resp.UsageMetadata
		fmt.Fprintf(&b, "|usage %d/%d/%d/%d/%d", u.PromptTokenCount, u.CandidatesTokenCount, u.TotalTokenCount, u.ThoughtsTokenCount, u.CachedContentTokenCount)
	}
	return b.String()
}

// messagesFixpointEqual compares two message lists. The >1 MB tool payload is
// the one allowed difference: the truncation marker records the ORIGINAL
// length, so a second pass re-truncates with a different count while the
// pre-marker payload stays identical.
func messagesFixpointEqual(t *testing.T, a, b []OpenAIMessage) bool {
	t.Helper()
	if mustJSON(t, a) == mustJSON(t, b) {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	const marker = "…[truncated "
	for i := range a {
		if a[i].Role != b[i].Role || a[i].ToolCallID != b[i].ToolCallID || a[i].Name != b[i].Name {
			return false
		}
		if mustJSON(t, a[i].ToolCalls) != mustJSON(t, b[i].ToolCalls) {
			return false
		}
		ca, cb := contentKey(a[i].Content), contentKey(b[i].Content)
		if ca == cb {
			continue
		}
		ia, ib := strings.Index(ca, marker), strings.Index(cb, marker)
		if ia < 0 || ib < 0 || ia != ib || ca[:ia] != cb[:ib] {
			return false
		}
	}
	return true
}

// contentKey renders one message content for comparison. A string stays a
// string; anything else (a multimodal content array) round-trips through JSON so
// the struct form and the decoded map form compare equal instead of both
// collapsing to "" the way a string type-assert would.
func contentKey(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "<unencodable>"
		}
		var norm any
		if err := json.Unmarshal(b, &norm); err != nil {
			return string(b)
		}
		nb, err := json.Marshal(norm)
		if err != nil {
			return string(b)
		}
		return "array:" + string(nb)
	}
}

// ---------------------------------------------------------------------------
// PT-2 SSE chunk reassembly
// ---------------------------------------------------------------------------

// sseAccumulator is an incremental SSE frame reassembler: it buffers arbitrary
// byte writes and emits a frame only once its terminating blank line arrived,
// so a transport that splits the stream anywhere produces the same frames.
type sseAccumulator struct {
	buf    strings.Builder
	frames []string
}

func (a *sseAccumulator) write(p string) {
	a.buf.WriteString(p)
	s := a.buf.String()
	for {
		i := strings.Index(s, "\n\n")
		if i < 0 {
			break
		}
		a.frames = append(a.frames, s[:i+2])
		s = s[i+2:]
	}
	a.buf.Reset()
	a.buf.WriteString(s)
}

// sseAggregate is the semantic content of a Gemini SSE stream. It carries a
// slice, so comparisons go through equalAggregate.
type sseAggregate struct {
	text    string
	thought string
	calls   []string
	finish  string
	usage   string
}

func (a sseAggregate) equal(b sseAggregate) bool {
	if a.text != b.text || a.thought != b.thought || a.finish != b.finish || a.usage != b.usage {
		return false
	}
	if len(a.calls) != len(b.calls) {
		return false
	}
	for i := range a.calls {
		if a.calls[i] != b.calls[i] {
			return false
		}
	}
	return true
}

func aggregateSSE(t *testing.T, stream []byte) sseAggregate {
	t.Helper()
	frames, err := ReadFrames(strings.NewReader(string(stream)))
	if err != nil {
		t.Fatalf("ReadFrames: %v", err)
	}
	var agg sseAggregate
	for _, f := range frames {
		for _, cand := range f.Candidates {
			if cand.FinishReason != "" {
				agg.finish = cand.FinishReason
			}
			if cand.Content == nil {
				continue
			}
			for _, p := range cand.Content.Parts {
				switch {
				case p.FunctionCall != nil:
					agg.calls = append(agg.calls, mustJSON(t, p.FunctionCall))
				case p.Thought:
					agg.thought += p.Text
				default:
					agg.text += p.Text
				}
			}
		}
		if f.UsageMetadata != nil {
			agg.usage = mustJSON(t, f.UsageMetadata)
		}
	}
	return agg
}

// shortStreamLimit splits the PT-2 strategy: streams up to this size are split
// at EVERY byte offset, longer ones at 1000 seeded-random offsets.
const shortStreamLimit = 4096

func TestProperty_PT2_SSEChunkReassembly(t *testing.T) {
	streams := map[string][]byte{}
	for _, row := range gfRows {
		switch row.kind {
		case kindStream, kindStreamApology, kindStreamError, kindTitle:
			streams[row.id] = readGolden(t, row.id, "expected.sse")
		}
	}
	// A long synthesized stream (> shortStreamLimit) exercises the
	// 1000-random-offset branch: a 5500-byte apology splits into 2048-byte
	// rune-safe pieces, so the aggregate spans several frames.
	longApology := strings.Repeat("한국어 🚀 ", 500)
	streams["synthesized-long"] = []byte(strings.Join(StreamFrames(&GenerateContentResponse{}, longApology), ""))

	// A giant text turn (> maxPartTextBytes) exercises the §8.8 split path: the
	// merged text becomes several frames, and splitting the stream anywhere must
	// still reassemble to the identical text (FIX-43).
	giant := strings.Repeat("한국어 🚀 ", 200*1024/len("한국어 🚀 ")+1)
	giantTurn := openaiup.NewTurn()
	giantTurn.SetContent(giant)
	giantResp, _ := TurnToGenerateResponse(giantTurn, false, "stop")
	streams["synthesized-giant"] = []byte(strings.Join(StreamFrames(giantResp, ""), ""))

	if len(streams) == 0 {
		t.Fatal("no SSE streams to test")
	}
	for id, stream := range streams {
		want := aggregateSSE(t, stream)
		wantFrames := strings.Count(string(stream), "\n\n")
		if len(stream) <= shortStreamLimit {
			// Every split offset, two writes.
			for i := 0; i <= len(stream); i++ {
				var acc sseAccumulator
				acc.write(string(stream[:i]))
				acc.write(string(stream[i:]))
				if len(acc.frames) != wantFrames {
					t.Fatalf("%s: split at %d yields %d frames, want %d", id, i, len(acc.frames), wantFrames)
				}
				if got := aggregateSSE(t, []byte(strings.Join(acc.frames, ""))); !got.equal(want) {
					t.Fatalf("%s: split at %d changed the aggregate:\n got: %+v\nwant: %+v", id, i, got, want)
				}
			}
			// Byte-at-a-time delivery: the strongest chunking.
			var acc sseAccumulator
			for i := 0; i < len(stream); i++ {
				acc.write(string(stream[i : i+1]))
			}
			if len(acc.frames) != wantFrames {
				t.Fatalf("%s: byte-at-a-time yields %d frames, want %d", id, len(acc.frames), wantFrames)
			}
			if got := aggregateSSE(t, []byte(strings.Join(acc.frames, ""))); !got.equal(want) {
				t.Fatalf("%s: byte-at-a-time changed the aggregate:\n got: %+v\nwant: %+v", id, got, want)
			}
			continue
		}
		// 1000 seeded-random offsets (deterministic across runs).
		rng := rand.New(rand.NewSource(261014))
		for k := 0; k < 1000; k++ {
			i := rng.Intn(len(stream) + 1)
			var acc sseAccumulator
			acc.write(string(stream[:i]))
			acc.write(string(stream[i:]))
			if len(acc.frames) != wantFrames {
				t.Fatalf("%s: random split at %d yields %d frames, want %d", id, i, len(acc.frames), wantFrames)
			}
			if got := aggregateSSE(t, []byte(strings.Join(acc.frames, ""))); !got.equal(want) {
				t.Fatalf("%s: random split at %d changed the aggregate:\n got: %+v\nwant: %+v", id, i, got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// PT-3 rune safety
// ---------------------------------------------------------------------------

func TestProperty_PT3_RuneSafety(t *testing.T) {
	corpus := "abc한국어🚀🎉テスト João café 한🇰🇷" + strings.Repeat("가", 200) + strings.Repeat("🎯", 40) + " end"
	check := func(s string, size int) {
		t.Helper()
		pieces := openaiup.SplitPieces(s, size)
		for _, p := range pieces {
			if !utf8.ValidString(p) {
				t.Fatalf("SplitPieces(%q, %d) produced an invalid UTF-8 piece %q", s, size, p)
			}
		}
		if got := strings.Join(pieces, ""); got != s {
			t.Fatalf("SplitPieces(%q, %d) rejoin = %q, want the original", s, size, got)
		}
	}
	// Every piece size: the cut walks every byte offset of the corpus.
	for size := 1; size <= len(corpus); size++ {
		check(corpus, size)
	}
	// Every rune-boundary offset, both slice directions, at awkward sizes.
	sizes := []int{1, 2, 3, 4, 5, 7, 11, 2048}
	for offset := 0; offset <= len(corpus); {
		if offset < len(corpus) && !utf8.RuneStart(corpus[offset]) {
			offset++
			continue
		}
		for _, size := range sizes {
			check(corpus[offset:], size)
			check(corpus[:offset], size)
		}
		if offset == len(corpus) {
			break
		}
		_, n := utf8.DecodeRuneInString(corpus[offset:])
		offset += n
	}

	// The same property on the codec's own emit path: synthesized text longer
	// than one chunk splits through SplitPieces, so every emitted text part is
	// valid UTF-8 and the parts rejoin to the original apology.
	apology := strings.Repeat("한국어 🚀 ", 500) // 5500 bytes
	if len(apology) <= synthesizedChunkBytes {
		t.Fatalf("test apology must exceed one synthesized chunk (%d bytes)", synthesizedChunkBytes)
	}
	chunks := StreamChunks(&GenerateContentResponse{}, apology)
	var text strings.Builder
	parts := 0
	for _, c := range chunks {
		for _, cand := range c.Candidates {
			if cand.Content == nil {
				continue
			}
			for _, p := range cand.Content.Parts {
				if p.Text == "" {
					continue
				}
				parts++
				if !utf8.ValidString(p.Text) {
					t.Fatalf("emitted text part is not valid UTF-8: %q", p.Text)
				}
				if len(p.Text) > synthesizedChunkBytes {
					t.Fatalf("emitted text part is %d bytes, over the %d-byte chunk cap", len(p.Text), synthesizedChunkBytes)
				}
				text.WriteString(p.Text)
			}
		}
	}
	if parts < 2 {
		t.Fatalf("a %d-byte apology must split into several chunks, got %d", len(apology), parts)
	}
	if text.String() != apology {
		t.Error("rejoined emitted text does not equal the apology")
	}
}

// ---------------------------------------------------------------------------
// PT-4 resend stability
// ---------------------------------------------------------------------------

// collectToolIDs extracts the ordered tool-call ids and tool_call_ids of a
// translated message list.
func collectToolIDs(msgs []OpenAIMessage) []string {
	var ids []string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			ids = append(ids, tc.ID)
		}
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
		}
	}
	return ids
}

func TestProperty_PT4_ResendStability(t *testing.T) {
	call := func(id, name, path string) Content {
		return Content{Role: "model", Parts: []Part{{FunctionCall: &FunctionCall{ID: id, Name: name, Args: map[string]any{"path": path}}}}}
	}
	response := func(id, name, out string) Content {
		return Content{Role: "model", Parts: []Part{{FunctionResponse: &FunctionResponse{ID: id, Name: name, Response: map[string]any{"output": out}}}}}
	}
	user := func(text string) Content {
		return Content{Role: "user", Parts: []Part{{Text: text}}}
	}

	histories := []*GenerateContentRequest{
		// H1: W8 shape, call side carries no id.
		{Contents: []Content{user("h1"), call("", "view_file", "a.go"), response("w8-1", "view_file", "body-a"), user("h1 again")}},
		// H2: parallel same-name calls, no ids anywhere.
		{Contents: []Content{user("h2"), call("", "view_file", "a.go"), call("", "view_file", "b.go"),
			response("", "view_file", "ra"), response("", "view_file", "rb")}},
		// H3: mixed: an id-carrying call, then an id-less call.
		{Contents: []Content{user("h3"), call("c-a", "view_file", "a.go"), response("c-a", "view_file", "ra"),
			call("", "run_command", "ls"), response("", "run_command", "rb")}},
		// H4: an orphan response.
		{Contents: []Content{user("h4"), response("", "view_file", "orphan")}},
	}
	appends := [][]Content{
		{user("appended question")},
		{user("appended question"), call("", "view_file", "c.go"), response("", "view_file", "rc")},
		{call("", "search_web", "q")},
	}

	for hi, h := range histories {
		msgs1, _ := ToOpenAIMessages(h, false)
		ids1 := collectToolIDs(msgs1)
		for ai, extra := range appends {
			grown := &GenerateContentRequest{
				SystemInstruction: h.SystemInstruction,
				Contents:          append(append([]Content{}, h.Contents...), extra...),
			}
			msgs2, _ := ToOpenAIMessages(grown, false)
			ids2 := collectToolIDs(msgs2)
			if len(ids2) < len(ids1) {
				t.Fatalf("H%d+append%d: id count shrank from %d to %d", hi, ai, len(ids1), len(ids2))
			}
			for i := range ids1 {
				if ids2[i] != ids1[i] {
					t.Errorf("H%d+append%d: id %d shifted from %q to %q (all: %v -> %v)",
						hi, ai, i, ids1[i], ids2[i], ids1, ids2)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// PT-5 error-map completeness
// ---------------------------------------------------------------------------

// validGoogleStatuses are the RPC status strings the §9.1 table may emit.
var validGoogleStatuses = map[string]bool{
	"UNAUTHENTICATED":    true,
	"NOT_FOUND":          true,
	"INVALID_ARGUMENT":   true,
	"PERMISSION_DENIED":  true,
	"RESOURCE_EXHAUSTED": true,
	"ABORTED":            true,
	"UNAVAILABLE":        true,
	"DEADLINE_EXCEEDED":  true,
	"INTERNAL":           true,
}

func TestProperty_PT5_ErrorMapCompleteness(t *testing.T) {
	var inbound struct {
		Provider string `json:"provider"`
		Rows     []struct {
			Condition string `json:"condition"`
			Body      string `json:"body"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(readGolden(t, "gf-26", "request.json"), &inbound); err != nil {
		t.Fatalf("decode gf-26 inbound: %v", err)
	}
	var envelopes []googleError
	if err := json.Unmarshal(readGolden(t, "gf-26", "expected.json"), &envelopes); err != nil {
		t.Fatalf("decode gf-26 expected: %v", err)
	}
	if len(envelopes) != len(inbound.Rows) {
		t.Fatalf("gf-26 pins %d envelopes for %d inbound rows", len(envelopes), len(inbound.Rows))
	}
	for i, env := range envelopes {
		row := inbound.Rows[i]
		want, ok := errorMap[row.Condition]
		if !ok {
			t.Fatalf("row %d: condition %q has no §9.1 mapping", i, row.Condition)
		}
		if env.Error.Code != want.code || env.Error.Status != want.status || env.Error.Message != want.message {
			t.Errorf("row %d (%s): envelope %+v, want the §9.1 row %+v", i, row.Condition, env.Error, want)
		}
		if env.Error.Code <= 0 {
			t.Errorf("row %d (%s): code %d is not a valid Google error code", i, row.Condition, env.Error.Code)
		}
		if !validGoogleStatuses[env.Error.Status] {
			t.Errorf("row %d (%s): status %q is not a Google RPC status", i, row.Condition, env.Error.Status)
		}
		msg := env.Error.Message
		if msg == "" || len(msg) > 240 {
			t.Errorf("row %d (%s): message length %d violates the ≤240 one-line rule", i, row.Condition, len(msg))
		}
		for _, r := range msg {
			if r == '\n' || r == '\r' || unicode.IsControl(r) {
				t.Errorf("row %d (%s): message carries control characters", i, row.Condition)
				break
			}
		}
		// Redaction: no upstream body echo, no key-shaped literal.
		if row.Body != "" && strings.Contains(msg, row.Body) {
			t.Errorf("row %d (%s): message echoes the upstream body", i, row.Condition)
		}
		for _, bad := range []string{"sk-", "AIza", "Bearer ", "-----BEGIN"} {
			if strings.Contains(msg, bad) {
				t.Errorf("row %d (%s): message carries key-shaped literal %q", i, row.Condition, bad)
			}
		}
	}

	// Completeness: the GF-26 row list must cover every §9.1 class the spec's
	// fixture row enumerates.
	wantConditions := []string{
		"provider-401", "provider-403", "provider-404", "provider-429", "provider-500",
		"upstream-timeout", "transport-failure", "malformed-body", "provider-400-response-format",
	}
	if len(inbound.Rows) != len(wantConditions) {
		t.Fatalf("gf-26 carries %d rows, want the %d spec classes", len(inbound.Rows), len(wantConditions))
	}
	for i, c := range wantConditions {
		if inbound.Rows[i].Condition != c {
			t.Errorf("gf-26 row %d = %q, want %q", i, inbound.Rows[i].Condition, c)
		}
	}
}

// ---------------------------------------------------------------------------
// PT-6 no-textless-200
// ---------------------------------------------------------------------------

// assertNeverTextless checks the §9.2 wire invariant on one emitted chunk list:
// at least one part or a finishReason, and the final chunk always carries a
// finishReason.
func assertNeverTextless(t *testing.T, id string, chunks []*GenerateContentResponse) {
	t.Helper()
	if len(chunks) == 0 {
		t.Fatalf("%s: the adapter emitted no chunks at all", id)
	}
	parts := 0
	for _, c := range chunks {
		for _, cand := range c.Candidates {
			if cand.Content != nil {
				parts += len(cand.Content.Parts)
			}
		}
	}
	last := chunks[len(chunks)-1]
	finish := ""
	if len(last.Candidates) > 0 {
		finish = last.Candidates[0].FinishReason
	}
	if finish == "" {
		t.Errorf("%s: the final chunk carries no finishReason", id)
	}
	if parts == 0 && finish == "" {
		t.Errorf("%s: textless 200: no parts and no finishReason", id)
	}
}

// assertCodecNeverTextless checks the stronger canonical-path guarantee: a
// response mapped from a provider turn always carries at least one part,
// because TurnToGenerateResponse synthesizes the §9.2 apology for a turn that
// produced zero text, zero thought text, zero tool calls and zero refusal.
func assertCodecNeverTextless(t *testing.T, id string, resp *GenerateContentResponse) {
	t.Helper()
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		t.Errorf("%s: the codec mapped a partless response: %s", id, mustJSON(t, resp))
		return
	}
	if resp.Candidates[0].FinishReason == "" {
		t.Errorf("%s: the mapped response carries no finishReason", id)
	}
}

func TestProperty_PT6_NoTextless200(t *testing.T) {
	degenerate := []string{
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`,
		`{"choices":[]}`,
		`{"choices":[{"index":0,"delta":{"content":"   \n\t"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"message":null,"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"stop"}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":null}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`,
		`{}`,
		`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"length"}]}`,
		// A refusal-only turn: user-visible text, so no apology on top of it.
		`{"choices":[{"index":0,"delta":{"role":"assistant","refusal":"cannot comply"},"finish_reason":"stop"}]}`,
		// A refusal that arrives with a null content: same mapping.
		`{"choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"cannot comply"},"finish_reason":"content_filter"}]}`,
	}
	apology := "provider mock-provider: <redacted one-line cause>"
	for i, body := range degenerate {
		id := "degenerate-" + strconv.Itoa(i)
		stream := "data: " + body + "\n\ndata: [DONE]\n\n"
		turn := openaiup.NewTurn()
		if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
			t.Fatalf("%s: MergeSSE: %v", id, err)
		}
		resp, _ := TurnToGenerateResponse(turn, false, turn.FinishReason)
		assertCodecNeverTextless(t, id, resp)
		assertNeverTextless(t, id, StreamChunks(resp, apology))
	}

	// The same wire invariant on hand-built degenerate responses, including the
	// raw choke point and the non-stream twin. These never pass through the
	// response mapper, so the raw choke point is what protects them.
	rawCases := map[string]*GenerateContentResponse{
		"nil":                  nil,
		"empty":                {},
		"candidate-nil":        {Candidates: []Candidate{{}}},
		"empty-parts":          {Candidates: []Candidate{{Content: &Content{Role: "model"}}}},
		"partless-with-reason": {Candidates: []Candidate{{FinishReason: "MAX_TOKENS"}}},
	}
	for name, resp := range rawCases {
		id := "raw-" + name
		assertNeverTextless(t, id, StreamChunks(resp, apology))
		term := EnsureTerminal(resp, apology)
		if len(term.Candidates) == 0 {
			t.Errorf("%s: EnsureTerminal dropped the candidate", id)
			continue
		}
		c := term.Candidates[0]
		if c.FinishReason == "" {
			t.Errorf("%s: EnsureTerminal left no finishReason", id)
		}
		parts := 0
		if c.Content != nil {
			parts = len(c.Content.Parts)
		}
		switch name {
		case "partless-with-reason":
			// An explicit terminal reason stands alone; no apology is invented.
		default:
			if parts == 0 {
				t.Errorf("%s: EnsureTerminal synthesized no apology part", id)
			}
		}
	}

	// Seeded fuzz: random degenerate provider answers, refusals included.
	rng := rand.New(rand.NewSource(261014))
	texts := []string{"", "   ", "\n\t", "답변", "한국어 🚀", "x"}
	refusals := []string{"", "", "", "cannot comply"}
	finishes := []string{"", "stop", "length", "tool_calls", "content_filter"}
	for i := 0; i < 300; i++ {
		var b strings.Builder
		nChoices := rng.Intn(3)
		b.WriteString(`{"choices":[`)
		for c := 0; c < nChoices; c++ {
			if c > 0 {
				b.WriteString(",")
			}
			text := texts[rng.Intn(len(texts))]
			tb, _ := json.Marshal(text)
			refusal := refusals[rng.Intn(len(refusals))]
			rb, _ := json.Marshal(refusal)
			finish := finishes[rng.Intn(len(finishes))]
			delta := `"role":"assistant"`
			if text != "" {
				delta += `,"content":` + string(tb)
			}
			if refusal != "" {
				delta += `,"refusal":` + string(rb)
			}
			b.WriteString(fmt.Sprintf(`{"index":%d,"delta":{%s},"finish_reason":`, c, delta))
			if finish == "" {
				b.WriteString(`null}`)
			} else {
				fb, _ := json.Marshal(finish)
				b.WriteString(string(fb) + `}`)
			}
		}
		b.WriteString(`]`)
		if rng.Intn(2) == 0 {
			b.WriteString(`,"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}`)
		}
		b.WriteString(`}`)
		stream := "data: " + b.String() + "\n\ndata: [DONE]\n\n"
		turn := openaiup.NewTurn()
		if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
			t.Fatalf("fuzz %d: MergeSSE: %v", i, err)
		}
		resp, _ := TurnToGenerateResponse(turn, false, turn.FinishReason)
		assertCodecNeverTextless(t, "fuzz", resp)
		assertNeverTextless(t, "fuzz", StreamChunks(resp, apology))
	}
}

// ---------------------------------------------------------------------------
// PT-7 thought leak
// ---------------------------------------------------------------------------

func TestProperty_PT7_ThoughtLeak(t *testing.T) {
	// deltaOp is one provider delta. Both OpenAI reasoning keys are exercised
	// alongside plain content; the marker prefixes keep reasoning and content
	// text distinguishable.
	type deltaOp struct {
		key  string // "reasoning_content", "reasoning" or "content"
		text string
	}
	matrices := [][]deltaOp{
		{{"reasoning_content", "REASON-1"}, {"content", "CONTENT-1"}},
		{{"content", "CONTENT-1"}, {"reasoning_content", "REASON-1"}},
		{{"reasoning_content", "REASON-1"}, {"content", "CONTENT-1"}, {"reasoning_content", "REASON-2"}, {"content", "CONTENT-2"}},
		{{"reasoning", "REASON-1"}, {"content", "CONTENT-1"}, {"reasoning", "REASON-2"}},
		{{"content", "CONTENT-1"}, {"reasoning", "REASON-1"}, {"content", "CONTENT-2"}, {"reasoning_content", "REASON-2"}, {"content", "CONTENT-3"}},
	}

	for mi, ops := range matrices {
		var wantReasoning, wantContent strings.Builder
		var stream strings.Builder
		stream.WriteString(`data: {"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n")
		for _, op := range ops {
			tb, _ := json.Marshal(op.text)
			stream.WriteString(fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"%s":%s},"finish_reason":null}]}`+"\n\n",
				op.key, string(tb)))
			if op.key == "content" {
				wantContent.WriteString(op.text)
			} else {
				wantReasoning.WriteString(op.text)
			}
		}
		stream.WriteString(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
		stream.WriteString("data: [DONE]\n\n")

		turn := openaiup.NewTurn()
		if err := turn.MergeSSE(strings.NewReader(stream.String())); err != nil {
			t.Fatalf("matrix %d: MergeSSE: %v", mi, err)
		}
		for _, includeThoughts := range []bool{true, false} {
			resp, _ := TurnToGenerateResponse(turn, includeThoughts, "stop")
			var plain, thought strings.Builder
			calls := 0
			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, p := range cand.Content.Parts {
					switch {
					case p.FunctionCall != nil:
						calls++
					case p.Thought:
						thought.WriteString(p.Text)
					default:
						plain.WriteString(p.Text)
					}
				}
			}
			if includeThoughts {
				if thought.String() != wantReasoning.String() {
					t.Errorf("matrix %d: thought parts = %q, want the reasoning deltas %q", mi, thought.String(), wantReasoning.String())
				}
			} else if thought.Len() != 0 {
				t.Errorf("matrix %d: includeThoughts=false still emitted a thought part %q", mi, thought.String())
			}
			if plain.String() != wantContent.String() {
				t.Errorf("matrix %d: plain text = %q, want the content deltas %q", mi, plain.String(), wantContent.String())
			}
			if strings.Contains(plain.String(), "REASON") {
				t.Errorf("matrix %d: reasoning text leaked into a plain text part", mi)
			}
			if calls != 0 {
				t.Errorf("matrix %d: unexpected tool calls", mi)
			}
		}
	}

	// Request direction: thought parts never reach user-visible content.
	req := &GenerateContentRequest{
		SystemInstruction: &Content{Parts: []Part{
			{Text: "SYS-PLAIN"},
			{Text: "SYS-THOUGHT", Thought: true},
		}},
		Contents: []Content{
			{Role: "user", Parts: []Part{{Text: "USER-PLAIN"}, {Text: "USER-THOUGHT", Thought: true}}},
			{Role: "model", Parts: []Part{{Text: "MODEL-PLAIN"}, {Text: "MODEL-THOUGHT", Thought: true}}},
		},
	}
	msgs, _ := ToOpenAIMessages(req, false)
	if len(msgs) == 0 {
		t.Fatal("no messages translated")
	}
	if s, _ := msgs[0].Content.(string); s != "SYS-PLAIN" {
		t.Errorf("system message = %q, want the thought part dropped", s)
	}
	for _, m := range msgs {
		blob := mustJSON(t, m)
		if strings.Contains(blob, "THOUGHT") {
			t.Errorf("message %s leaked thought text", blob)
		}
	}
}

// ---------------------------------------------------------------------------
// PT-8 envelope symmetry
// ---------------------------------------------------------------------------

func TestProperty_PT8_EnvelopeSymmetry(t *testing.T) {
	// The captured /v1internal request envelope (gf-31) unwraps to inner; the
	// rename is idempotent and the inner payload survives a response-envelope
	// round trip byte-semantically.
	var inbound struct {
		Envelope json.RawMessage `json:"envelope"`
	}
	if err := json.Unmarshal(readGolden(t, "gf-31", "request.json"), &inbound); err != nil {
		t.Fatalf("decode gf-31 inbound: %v", err)
	}
	env, inner, err := UnwrapV1Internal(inbound.Envelope)
	if err != nil {
		t.Fatalf("UnwrapV1Internal: %v", err)
	}
	if env.Model == "" {
		t.Error("the captured envelope must carry a model for routing")
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	// Idempotent unwrap: decoding the inner again changes nothing.
	inner2, err := DecodeGenerateRequest(innerBytes)
	if err != nil {
		t.Fatalf("re-decode inner: %v", err)
	}
	if mustJSON(t, inner2) != string(innerBytes) {
		t.Errorf("unwrap is not idempotent:\nfirst:  %s\nsecond: %s", innerBytes, mustJSON(t, inner2))
	}

	// Response shapes to wrap: plain, thought+text+call with usage and a
	// responseId, and the partless choke-point shape.
	rich := &GenerateContentResponse{
		Candidates: []Candidate{{
			Content: &Content{Role: "model", Parts: []Part{
				{Text: "REASONING", Thought: true},
				{Text: "답변 🚀"},
				{FunctionCall: &FunctionCall{ID: "c1", Name: "view_file", Args: map[string]any{"path": "한국어.md"}}},
			}},
			FinishReason: "STOP",
			Index:        0,
		}},
		UsageMetadata: &UsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 20, TotalTokenCount: 30, ThoughtsTokenCount: 2, CachedContentTokenCount: 3},
		ResponseID:    "resp-gf31",
	}
	responses := []*GenerateContentResponse{
		{Candidates: []Candidate{{Content: &Content{Role: "model", Parts: []Part{{Text: "PROBE_OK"}}}, FinishReason: "STOP", Index: 0}}},
		rich,
		{},
	}

	for i, resp := range responses {
		wrapped, err := WrapV1InternalResponse(resp)
		if err != nil {
			t.Fatalf("shape %d: WrapV1InternalResponse: %v", i, err)
		}
		// wrap(unwrap(x)) == x, byte-semantically.
		var envOut V1InternalResponse
		if err := json.Unmarshal(wrapped, &envOut); err != nil {
			t.Fatalf("shape %d: unwrap wrapped envelope: %v", i, err)
		}
		var back GenerateContentResponse
		if err := json.Unmarshal(envOut.Response, &back); err != nil {
			t.Fatalf("shape %d: decode inner response: %v", i, err)
		}
		if mustJSON(t, &back) != mustJSON(t, resp) {
			t.Errorf("shape %d: wrap(unwrap(x)) != x\n got: %s\nwant: %s", i, mustJSON(t, &back), mustJSON(t, resp))
		}
		// The stream-chunk entry point emits the identical wire shape.
		chunk, err := WrapV1InternalStreamChunk(resp)
		if err != nil {
			t.Fatalf("shape %d: WrapV1InternalStreamChunk: %v", i, err)
		}
		if !bytes.Equal(chunk, wrapped) {
			t.Errorf("shape %d: stream wrap = %s, want %s", i, chunk, wrapped)
		}
	}

	// Stream wrap modes: the wrapped mode nests exactly the StreamChunks
	// output, the raw mode equals the public frames, and both share the choke
	// point (the final chunk always carries finishReason).
	for i, resp := range responses {
		plain := StreamFrames(resp, "")
		for _, mode := range []StreamWrapMode{WrapStreamChunks, RawStreamChunks} {
			frames := NewV1InternalStream(mode).Frames(resp, "")
			if len(frames) != len(plain) {
				t.Fatalf("shape %d mode %d: %d frames, want %d", i, mode, len(frames), len(plain))
			}
			for j, f := range frames {
				payload := strings.TrimSuffix(strings.TrimPrefix(f, "data: "), "\n\n")
				if mode == WrapStreamChunks {
					var envOut V1InternalResponse
					if err := json.Unmarshal([]byte(payload), &envOut); err != nil {
						t.Fatalf("shape %d mode %d frame %d: not an envelope: %v", i, mode, j, err)
					}
					want := strings.TrimSuffix(strings.TrimPrefix(plain[j], "data: "), "\n\n")
					if string(envOut.Response) != want {
						t.Errorf("shape %d mode %d frame %d: nested chunk = %s, want %s", i, mode, j, envOut.Response, want)
					}
					continue
				}
				if f != plain[j] {
					t.Errorf("shape %d mode %d frame %d: raw frame = %s, want %s", i, mode, j, f, plain[j])
				}
			}
			last := frames[len(frames)-1]
			if !strings.Contains(last, `"finishReason":"STOP"`) {
				t.Errorf("shape %d mode %d: final frame must carry finishReason: %s", i, mode, last)
			}
		}
	}
}
