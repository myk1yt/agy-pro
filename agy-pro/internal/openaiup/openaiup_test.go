package openaiup

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func intPtr(i int) *int { return &i }

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Turn: index-aware tool_calls merge, reasoning alias, usage last-wins
// ---------------------------------------------------------------------------

func TestTurn_MergesContentReasoningAndUsage(t *testing.T) {
	turn := NewTurn()
	turn.MergeChunk(&Chunk{Usage: map[string]any{"total_tokens": 3}})
	turn.MergeMessage(&Message{Role: "assistant", Content: strPtr("Hello, ")})
	turn.MergeMessage(&Message{Content: strPtr("world")})
	turn.MergeMessage(&Message{ReasoningContent: strPtr("think ")})
	turn.MergeMessage(&Message{Reasoning: strPtr("more")})
	// Last-wins: a second usage block replaces the first.
	turn.MergeChunk(&Chunk{Usage: map[string]any{"total_tokens": 9}})

	if turn.Content() != "Hello, world" {
		t.Errorf("Content = %q; want %q", turn.Content(), "Hello, world")
	}
	if turn.Reasoning() != "think more" {
		t.Errorf("Reasoning = %q; want the reasoning_content and reasoning alias merged", turn.Reasoning())
	}
	if turn.Usage["total_tokens"] != 9 {
		t.Errorf("Usage = %v; want the last block (total_tokens 9)", turn.Usage)
	}
}

func TestTurn_MergeChunkFoldsDeltaAndMessage(t *testing.T) {
	turn := NewTurn()
	turn.MergeChunk(&Chunk{Choices: []Choice{
		{Index: 0, Delta: Message{Content: strPtr("delta-text ")}},
		{Index: 0, Message: Message{Content: strPtr("message-text")}},
	}})
	if turn.Content() != "delta-text message-text" {
		t.Errorf("Content = %q; want delta and message both folded", turn.Content())
	}
}

func TestTurn_ToolCallArgumentsAccumulateAcrossChunks(t *testing.T) {
	turn := NewTurn()
	turn.MergeToolCall(&ToolCall{Index: intPtr(0), ID: "call_a", Type: "function",
		Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":"hel`}})
	turn.MergeToolCall(&ToolCall{Index: intPtr(0), Function: ToolCallFunction{Arguments: `lo.txt"}`}})

	calls := turn.OrderedCalls()
	if len(calls) != 1 {
		t.Fatalf("OrderedCalls = %d; want 1", len(calls))
	}
	if calls[0].ID != "call_a" || calls[0].Type != "function" || calls[0].Name != "read_file" {
		t.Errorf("call = %+v; want id call_a / type function / name read_file", calls[0])
	}
	if calls[0].Args != `{"path":"hello.txt"}` {
		t.Errorf("Args = %q; want the fragments concatenated", calls[0].Args)
	}
}

func TestTurn_IndexLessContinuationAppendsToNewestCall(t *testing.T) {
	turn := NewTurn()
	turn.MergeToolCall(&ToolCall{ID: "call_1", Type: "function",
		Function: ToolCallFunction{Name: "list_dir", Arguments: `{"path":"`}})
	// Continuation chunk that omits index and carries only an arguments
	// fragment: it belongs to the newest call, not to a new one.
	turn.MergeToolCall(&ToolCall{Function: ToolCallFunction{Arguments: `."}`}})

	calls := turn.OrderedCalls()
	if len(calls) != 1 {
		t.Fatalf("OrderedCalls = %d; want the continuation folded into one call", len(calls))
	}
	if calls[0].Args != `{"path":"."}` {
		t.Errorf("Args = %q; want the continuation appended", calls[0].Args)
	}
}

func TestTurn_OrderPreservesArrivalOrder(t *testing.T) {
	turn := NewTurn()
	turn.MergeToolCall(&ToolCall{Index: intPtr(1), ID: "second", Function: ToolCallFunction{Name: "b", Arguments: "{}"}})
	turn.MergeToolCall(&ToolCall{Index: intPtr(0), ID: "first", Function: ToolCallFunction{Name: "a", Arguments: "{}"}})

	calls := turn.OrderedCalls()
	if len(calls) != 2 || calls[0].ID != "second" || calls[1].ID != "first" {
		t.Fatalf("OrderedCalls = %+v; want arrival order [second first]", calls)
	}
	if got := turn.CallID(1); got != "first" {
		t.Errorf("CallID(1) = %q; want first", got)
	}
}

func TestTurn_CallIDSynthesizesFallbackWhenUpstreamOmittedIt(t *testing.T) {
	turn := NewTurn()
	turn.MergeToolCall(&ToolCall{Index: intPtr(0), Function: ToolCallFunction{Name: "grep", Arguments: "{}"}})
	if got := turn.CallID(0); got != "call_0" {
		t.Errorf("CallID(0) = %q; want the synthesized call_0", got)
	}
	if got := turn.CallID(7); got != "call_7" {
		t.Errorf("CallID(7) = %q; want the synthesized call_7", got)
	}
	if got := turn.CallID(-1); got != "call_-1" {
		t.Errorf("CallID(-1) = %q; want the synthesized call_-1", got)
	}
}

func TestTurn_MergeSSEHandlesSentinelCommentsAndGarbage(t *testing.T) {
	stream := strings.Join([]string{
		": keep-alive comment",
		"",
		"event: message",
		`data: {"choices":[{"index":0,"delta":{"content":"first"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"why"}}]}`,
		`data: {not json at all`,
		`data: {"choices":[{"index":0,"delta":{"content":"-second"}}]}`,
		"data: [DONE]",
		`data: {"choices":[{"index":0,"delta":{"content":"-after-done"}}]}`,
		"",
	}, "\n")

	turn := NewTurn()
	if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
		t.Fatalf("MergeSSE: %v", err)
	}
	if turn.Content() != "first-second" {
		t.Errorf("Content = %q; want the deltas before [DONE] only", turn.Content())
	}
	if turn.Reasoning() != "why" {
		t.Errorf("Reasoning = %q; want the reasoning delta", turn.Reasoning())
	}
}

func TestTurn_MergeSSEAcceptsLineLargerThanDefaultScannerToken(t *testing.T) {
	// bufio's default token ceiling is 64 KB; the merge buffer is 16 MB, so a
	// single long delta line must still be decoded.
	huge := strings.Repeat("가", 40_000) // 120000 bytes
	payload, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": huge}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := "data: " + string(payload) + "\n\ndata: [DONE]\n\n"

	turn := NewTurn()
	if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
		t.Fatalf("MergeSSE: %v", err)
	}
	if turn.Content() != huge {
		t.Errorf("Content length = %d; want %d (the 16 MB scanner buffer must hold one long line)", len(turn.Content()), len(huge))
	}
}

// FIX-1: the zero value must be as usable as NewTurn. A Turn built as a bare
// literal used to panic with "assignment to entry in nil map" on the first
// tool_call delta, because only the constructor seeded the calls map.
func TestTurn_ZeroValueIsMergeable(t *testing.T) {
	var turn Turn
	turn.MergeChunk(&Chunk{Choices: []Choice{{
		Index: 0,
		Delta: Message{ToolCalls: []ToolCall{{
			Index:    intPtr(0),
			ID:       "call_z",
			Type:     "function",
			Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":`},
		}}},
	}}})
	// A continuation chunk must also land, proving the map survived.
	turn.MergeToolCall(&ToolCall{Function: ToolCallFunction{Arguments: `"a.txt"}`}})
	turn.MergeMessage(&Message{Content: strPtr("zero-value text")})

	calls := turn.OrderedCalls()
	if len(calls) != 1 {
		t.Fatalf("OrderedCalls = %d; want the zero value to accumulate tool calls", len(calls))
	}
	if calls[0].Args != `{"path":"a.txt"}` {
		t.Errorf("Args = %q; want the fragments merged", calls[0].Args)
	}
	if turn.Content() != "zero-value text" {
		t.Errorf("Content = %q; want the merged content", turn.Content())
	}
	if got := turn.CallID(0); got != "call_z" {
		t.Errorf("CallID(0) = %q; want call_z", got)
	}
}

func TestTurn_ZeroValueMergeSSEAndJSON(t *testing.T) {
	var sseTurn Turn
	if err := sseTurn.MergeSSE(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"s\"}}]}\n\ndata: [DONE]\n\n")); err != nil {
		t.Fatalf("MergeSSE on a zero-value Turn: %v", err)
	}
	if sseTurn.Content() != "s" {
		t.Errorf("Content = %q; want s", sseTurn.Content())
	}

	var jsonTurn Turn
	if err := jsonTurn.MergeJSON(strings.NewReader(`{"choices":[{"message":{"content":"j"}}]}`)); err != nil {
		t.Fatalf("MergeJSON on a zero-value Turn: %v", err)
	}
	if jsonTurn.Content() != "j" {
		t.Errorf("Content = %q; want j", jsonTurn.Content())
	}
}

// FIX-4: content and reasoning accumulate through builders, so a stream of many
// small deltas reassembles exactly, Korean included, without copying the whole
// buffer per delta.
func TestTurn_BuilderAccumulationRoundTrip(t *testing.T) {
	corpus := strings.Repeat("가나다라마바사아자차카타파하한글테스트😀", 400)
	turn := NewTurn()
	for _, piece := range SplitPieces(corpus, 7) {
		turn.MergeMessage(&Message{Content: strPtr(piece), ReasoningContent: strPtr(piece)})
	}
	if turn.Content() != corpus {
		t.Errorf("Content length = %d; want %d (many small deltas must reassemble)", len(turn.Content()), len(corpus))
	}
	if turn.Reasoning() != corpus {
		t.Errorf("Reasoning length = %d; want %d", len(turn.Reasoning()), len(corpus))
	}
	if !utf8.ValidString(turn.Content()) || !utf8.ValidString(turn.Reasoning()) {
		t.Error("accumulated text must stay valid UTF-8")
	}

	// SetContent/SetReasoning replace what was accumulated, which is how the
	// bridge's fallback paths synthesize a turn.
	turn.SetContent("fallback")
	turn.SetReasoning("why")
	if turn.Content() != "fallback" || turn.Reasoning() != "why" {
		t.Errorf("Content/Reasoning = %q/%q; want the replaced values", turn.Content(), turn.Reasoning())
	}
	turn.MergeMessage(&Message{Content: strPtr(" more")})
	if turn.Content() != "fallback more" {
		t.Errorf("Content = %q; want merging to continue after a replace", turn.Content())
	}
}

// FIX-16: the Turn carries the provider's own finish_reason, refusal and choice
// index, so the Gemini response mapping needs no out-of-band parameters.
func TestTurn_CapturesFinishReasonRefusalAndChoiceIndex(t *testing.T) {
	turn := NewTurn()
	if turn.FinishReason != "" || turn.Refusal != "" || turn.ChoiceIndex != 0 {
		t.Fatalf("a fresh turn must start empty: %+v", turn)
	}

	// A null finish_reason leaves the field alone.
	turn.MergeChunk(&Chunk{Choices: []Choice{{Index: 0, Delta: Message{Content: strPtr("hi")}, FinishReason: nil}}})
	if turn.FinishReason != "" {
		t.Errorf("FinishReason = %q; want \"\" for a null finish_reason", turn.FinishReason)
	}
	if turn.ChoiceIndex != 0 {
		t.Errorf("ChoiceIndex = %d; want 0", turn.ChoiceIndex)
	}

	// Last-wins across chunks, and the index follows the last choice seen.
	turn.MergeChunk(&Chunk{Choices: []Choice{{Index: 0, FinishReason: strPtr("tool_calls")}}})
	turn.MergeChunk(&Chunk{Choices: []Choice{{Index: 3, FinishReason: strPtr("length")}}})
	if turn.FinishReason != "length" {
		t.Errorf("FinishReason = %q; want the last chunk's value", turn.FinishReason)
	}
	if turn.ChoiceIndex != 3 {
		t.Errorf("ChoiceIndex = %d; want the last choice index", turn.ChoiceIndex)
	}

	// A refusal is captured, and an empty one does not clear the last value.
	turn.MergeMessage(&Message{Refusal: strPtr("I cannot help with that")})
	if turn.Refusal != "I cannot help with that" {
		t.Errorf("Refusal = %q; want the provider refusal", turn.Refusal)
	}
	turn.MergeMessage(&Message{Refusal: strPtr("")})
	if turn.Refusal != "I cannot help with that" {
		t.Errorf("Refusal = %q; an empty refusal must not clear the last non-empty one", turn.Refusal)
	}
}

// The SSE path feeds the same fields, so a streamed refusal and finish_reason
// land on the Turn without the caller tracking them.
func TestTurn_MergeSSECapturesFinishReasonAndRefusal(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"refusal":"cannot comply"}}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
		"data: [DONE]",
		"",
	}, "\n")
	turn := NewTurn()
	if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
		t.Fatalf("MergeSSE: %v", err)
	}
	if turn.FinishReason != "content_filter" {
		t.Errorf("FinishReason = %q; want content_filter", turn.FinishReason)
	}
	if turn.Refusal != "cannot comply" {
		t.Errorf("Refusal = %q; want the streamed refusal", turn.Refusal)
	}
}

// TestTurn_StreamTerminalRecordsTheTerminalSignal is the G10F-1 detection unit.
// A stream is clean when it reaches a terminal signal of EITHER kind and
// truncated when it reaches neither, whatever the reader did: an upstream that
// ends its response without a finish_reason chunk and without [DONE] hands back
// a clean EOF, so only this flag can tell a truncated turn from a complete one.
func TestTurn_StreamTerminalRecordsTheTerminalSignal(t *testing.T) {
	delta := `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`

	for _, tc := range []struct {
		name string
		stop string
		want bool
	}{
		{"finish_reason chunk", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, true},
		{"[DONE] sentinel", "data: [DONE]", true},
		{"quoted [DONE] sentinel", `data: "[DONE]"`, true},
		{"lowercase [done] sentinel", "data: [done]", true},
		{"quoted lowercase [done] sentinel", `data: "[done]"`, true},
		{"both", `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]", true},
		{"neither", "", false},
		{"null finish_reason only", `data: {"choices":[{"index":0,"delta":{},"finish_reason":null}]}`, false},
		{"empty finish_reason only", `data: {"choices":[{"index":0,"delta":{},"finish_reason":""}]}`, false},
		{"near-miss sentinel", "data: [DONE-LATER]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := delta + "\n\n"
			if tc.stop != "" {
				stream += tc.stop + "\n\n"
			}
			turn := NewTurn()
			if err := turn.MergeSSE(strings.NewReader(stream)); err != nil {
				t.Fatalf("MergeSSE: %v", err)
			}
			if turn.Content() != "partial" {
				t.Fatalf("Content = %q; want the delta", turn.Content())
			}
			if turn.StreamTerminal != tc.want {
				t.Fatalf("StreamTerminal = %v, want %v", turn.StreamTerminal, tc.want)
			}
		})
	}

	// A zero-value Turn that merged nothing is not terminal: the caller applies
	// the check only on the SSE branch, where a premature end is possible.
	if (Turn{}).StreamTerminal {
		t.Error("the zero value must read as not terminal")
	}
}

// TestIsTerminalSentinel pins the G10F2-1 sentinel spellings directly. The
// JSON-quoted form is not hypothetical here: it is the spelling this project's
// own provider-stream fixtures use (geminiwire/golden/gf-22/23/27-request.sse).
func TestIsTerminalSentinel(t *testing.T) {
	terminal := []string{
		"[DONE]",
		`"[DONE]"`,
		"[done]",
		`"[done]"`,
		"[Done]",
		`  [DONE]  `,
		`" [DONE] "`,
		`"[DONE]"`,
	}
	for _, s := range terminal {
		if !isTerminalSentinel(s) {
			t.Errorf("isTerminalSentinel(%q) = false; want true", s)
		}
	}
	notTerminal := []string{
		"",
		"DONE",
		"[DONE-LATER]",
		`"[DONE-LATER]"`,
		`"[DONE]" extra`,
		`["DONE"]`,
		`"[DONE]`,
		"done",
		`{"finish_reason":"stop"}`,
	}
	for _, s := range notTerminal {
		if isTerminalSentinel(s) {
			t.Errorf("isTerminalSentinel(%q) = true; want false", s)
		}
	}
}

func TestTurn_MergeJSON(t *testing.T) {
	body := `{"id":"u1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"listed"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`
	turn := NewTurn()
	if err := turn.MergeJSON(strings.NewReader(body)); err != nil {
		t.Fatalf("MergeJSON: %v", err)
	}
	if turn.Content() != "listed" {
		t.Errorf("Content = %q; want listed", turn.Content())
	}
	if turn.Usage["total_tokens"] != float64(7) {
		t.Errorf("Usage = %v; want total_tokens 7", turn.Usage)
	}
}

func TestTurn_MergeJSONRejectsUnparseableBody(t *testing.T) {
	turn := NewTurn()
	err := turn.MergeJSON(strings.NewReader("<html>502 Bad Gateway</html>"))
	if err == nil {
		t.Fatal("MergeJSON must fail on an unparseable body")
	}
	if !strings.Contains(err.Error(), "failed to parse upstream response") {
		t.Errorf("err = %v; want the parse failure wrapper", err)
	}
}
