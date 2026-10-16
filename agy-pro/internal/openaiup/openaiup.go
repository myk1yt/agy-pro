// Package openaiup holds the OpenAI-compatible client core shared by the
// agy-pro bridge and the geminiwire/wireadp adapter: the wire types, the
// index-aware delta aggregator, the reasoning-effort ladder, and the
// upstream-turn transport discipline. Nothing here knows about either caller's
// own protocol; it speaks the OpenAI chat.completions wire upstream and hands
// back one aggregated Turn.
package openaiup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ToolCallFunction carries one tool call's name plus its arguments fragment.
// Providers routinely split the arguments JSON over several deltas, so only the
// concatenation is meaningful.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is one OpenAI tool_call as it appears in stream deltas and in
// non-streaming messages; `index` is optional and drives the delta merge.
type ToolCall struct {
	Index    *int             `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// Message covers both stream `delta` and non-stream `message` shapes.
// Reasoning is the alias some families (deepseek/glm variants) send instead of
// ReasoningContent; both merge into Turn.Reasoning.
type Message struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content"`
	Reasoning        *string    `json:"reasoning"`
	Refusal          *string    `json:"refusal"`
	ToolCalls        []ToolCall `json:"tool_calls"`
}

// Choice is one entry of a chunk's `choices` array. Exactly one of Delta
// (streaming) or Message (non-streaming) is populated by a well-behaved
// provider; both are merged so a provider that fills both is still correct.
type Choice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	Message      Message `json:"message"`
	FinishReason *string `json:"finish_reason"`
}

// Chunk is one chat.completion.chunk, and equally the non-streaming
// chat.completion body (same shape with `message` populated instead of
// `delta`).
type Chunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []Choice       `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

// toolCallAgg accumulates one tool call across index-aware deltas.
type toolCallAgg struct {
	id   string
	typ  string
	name string
	args strings.Builder
}

// Turn is the aggregated result of one upstream turn. The zero value is ready
// to use: every merge method lazily initializes its accumulators, so callers
// may start from &Turn{} as well as from NewTurn.
type Turn struct {
	// Usage is the last usage block the provider sent; nil when it sent none.
	Usage map[string]any
	// FinishReason is the provider's last finish_reason, "" when it sent none
	// or sent null. Last-wins across merged chunks.
	FinishReason string
	// Refusal is the last non-empty refusal text the provider sent, "" when it
	// sent none.
	Refusal string
	// ChoiceIndex is the last choice index the provider sent. Defaults to 0,
	// which is also what a provider that omits `index` yields.
	ChoiceIndex int

	// StreamTerminal reports whether an upstream SSE stream reached a terminal
	// signal before it ended: a chunk carrying a finish_reason, or the [DONE]
	// sentinel. A stream that ended with neither was cut short (the provider
	// dropped the connection mid-body, or a proxy ended the response), and the
	// folded turn is a TRUNCATED answer that must never be presented as a
	// complete one. The zero value is false, so a Turn that never merged a
	// stream reads as "not terminal"; callers apply the check only on the SSE
	// branch, where a premature end is possible.
	StreamTerminal bool

	// Content and reasoning accumulate through builders: a 32 MB stream of
	// small deltas would copy the whole buffer on every += otherwise.
	content   strings.Builder
	reasoning strings.Builder
	calls     map[int]*toolCallAgg
	order     []int
}

// NewTurn returns an empty Turn ready to merge deltas into.
func NewTurn() *Turn {
	return &Turn{}
}

// Content returns the concatenation of every non-nil content delta. Reading it
// is cheap and does not disturb further merging.
func (t *Turn) Content() string { return t.content.String() }

// Reasoning returns the concatenation of every reasoning delta, kept separate
// from Content so thought text can never leak into user-visible content.
func (t *Turn) Reasoning() string { return t.reasoning.String() }

// SetContent replaces the accumulated content. Fallback paths that synthesize
// text instead of merging upstream deltas use it.
func (t *Turn) SetContent(s string) {
	t.content.Reset()
	t.content.WriteString(s)
}

// SetReasoning replaces the accumulated reasoning trace.
func (t *Turn) SetReasoning(s string) {
	t.reasoning.Reset()
	t.reasoning.WriteString(s)
}

// OrderedCall is one aggregated tool call in provider arrival order.
type OrderedCall struct {
	// ID is the wire id, empty when the upstream never sent one; CallID
	// renders the stable fallback for that case.
	ID string
	// Type is the tool call type, empty when the upstream omitted it.
	Type string
	// Name is the function name, empty when the upstream omitted it.
	Name string
	// Args is the accumulated arguments string: a JSON object once every
	// fragment has arrived, possibly still partial mid-stream.
	Args string
}

// OrderedCalls returns the aggregated tool calls in arrival order.
func (t *Turn) OrderedCalls() []OrderedCall {
	out := make([]OrderedCall, 0, len(t.order))
	for _, idx := range t.order {
		call := t.calls[idx]
		if call == nil {
			continue
		}
		out = append(out, OrderedCall{ID: call.id, Type: call.typ, Name: call.name, Args: call.args.String()})
	}
	return out
}

// MergeChunk folds one decoded chunk into the turn. Usage is last-wins: a
// provider that reports usage on an early chunk and again on the final one
// ends with the final block. So are finish_reason and the choice index.
func (t *Turn) MergeChunk(c *Chunk) {
	if c.Usage != nil {
		t.Usage = c.Usage
	}
	for i := range c.Choices {
		ch := &c.Choices[i]
		if ch.FinishReason != nil {
			t.FinishReason = *ch.FinishReason
			if *ch.FinishReason != "" {
				// A NAMED finish reason is the provider's terminal signal, so a
				// stream carrying one did not end prematurely. An empty or null
				// finish_reason is not: it is what every non-final chunk sends.
				t.StreamTerminal = true
			}
		}
		t.ChoiceIndex = ch.Index
		t.MergeMessage(&ch.Delta)
		t.MergeMessage(&ch.Message)
	}
}

// MergeMessage folds one delta or message into the turn. Both reasoning keys
// are accepted so a family that spells the field `reasoning` still lands in
// Turn.Reasoning instead of being dropped.
func (t *Turn) MergeMessage(m *Message) {
	if m == nil {
		return
	}
	if m.Content != nil {
		t.content.WriteString(*m.Content)
	}
	if m.ReasoningContent != nil {
		t.reasoning.WriteString(*m.ReasoningContent)
	}
	if m.Reasoning != nil {
		t.reasoning.WriteString(*m.Reasoning)
	}
	if m.Refusal != nil && *m.Refusal != "" {
		t.Refusal = *m.Refusal
	}
	for i := range m.ToolCalls {
		t.MergeToolCall(&m.ToolCalls[i])
	}
}

// MergeToolCall folds one tool_call delta into its slot. The slot is chosen by
// `index` when present; a continuation chunk that omits `index` (carrying only
// an arguments fragment) appends to the newest call.
func (t *Turn) MergeToolCall(tc *ToolCall) {
	if t.calls == nil {
		// Lazy init keeps the zero value usable: a Turn built as a bare literal
		// must accumulate tool calls exactly like one from NewTurn.
		t.calls = map[int]*toolCallAgg{}
	}
	var slot *toolCallAgg
	if tc.Index != nil {
		slot = t.calls[*tc.Index]
	} else if tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments != "" && len(t.order) > 0 {
		// Continuation chunk that omitted index: append to the newest call.
		slot = t.calls[t.order[len(t.order)-1]]
	}
	if slot == nil {
		idx := len(t.order)
		if tc.Index != nil {
			idx = *tc.Index
		}
		for {
			if _, exists := t.calls[idx]; !exists {
				break
			}
			idx++
		}
		slot = &toolCallAgg{}
		t.calls[idx] = slot
		t.order = append(t.order, idx)
	}
	if tc.ID != "" {
		slot.id = tc.ID
	}
	if tc.Type != "" {
		slot.typ = tc.Type
	}
	if tc.Function.Name != "" {
		slot.name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		slot.args.WriteString(tc.Function.Arguments)
	}
}

// CallID returns the wire id of the i-th aggregated call, synthesizing a stable
// fallback when the upstream omitted it (assistant and tool messages must
// agree on the same string).
func (t *Turn) CallID(i int) string {
	if i >= 0 && i < len(t.order) {
		if call := t.calls[t.order[i]]; call != nil && call.id != "" {
			return call.id
		}
	}
	return fmt.Sprintf("call_%d", i)
}

// MergeSSE folds an OpenAI SSE stream into the turn. It stops at the terminal
// sentinel, skips blank and comment lines, and tolerates undecodable data
// lines. The scanner buffer is 16 MB so one very long delta line survives.
//
// A stream that ends with neither a finish_reason chunk nor the terminal
// sentinel is NOT an error here: the reader may well have ended cleanly. It is
// recorded instead, as StreamTerminal == false, so the caller can refuse to
// present the folded turn as a complete answer (see ErrStreamTruncated).
func (t *Turn) MergeSSE(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if isTerminalSentinel(data) {
			// The sentinel is the provider's terminal signal, so a stream that
			// reaches it ended cleanly whatever its chunks carried.
			t.StreamTerminal = true
			break
		}
		var chunk Chunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		t.MergeChunk(&chunk)
	}
	return sc.Err()
}

// isTerminalSentinel reports whether an SSE data payload is the provider's
// terminal sentinel. The wire carries it in more than one spelling, and every
// one of them is an explicit "the stream is over" from the provider, so all are
// accepted:
//
//   - the bare form, `data: [DONE]`, which the bridge and the wireadp mocks use;
//   - the JSON-quoted form, `data: "[DONE]"`, which this project's own
//     provider-stream fixtures use (geminiwire/golden/gf-22/23/27-request.sse,
//     and manifest.md calls those bytes "the provider's own data: [DONE]
//     sentinel");
//   - either form in any case (`[done]`, `"[Done]"`).
//
// Before G10F2-1 only the bare form counted. A provider whose ONLY terminal
// signal was the quoted form therefore read as truncated and was answered
// 503/UNAVAILABLE on a complete answer, which is the exact inverse of the
// defect the flag exists to catch: agy would then spend its bounded 9-attempt
// retry budget on a turn that had already succeeded. Widening the comparison
// cannot manufacture a false-clean, because it only accepts a sentinel the
// provider actually sent.
func isTerminalSentinel(data string) bool {
	s := strings.TrimSpace(data)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return strings.EqualFold(s, "[DONE]")
}

// MergeJSON folds one non-streaming chat.completion body into the turn.
func (t *Turn) MergeJSON(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes))
	if err != nil {
		return fmt.Errorf("failed to read upstream response: %w", err)
	}
	var chunk Chunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return fmt.Errorf("failed to parse upstream response: %w", err)
	}
	t.MergeChunk(&chunk)
	return nil
}
