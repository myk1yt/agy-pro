package geminiwire

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// maxToolResponseBytes caps one outbound tool message payload (§6.4, §7.3: 1 MB
// plus a truncation marker).
const maxToolResponseBytes = 1 << 20

// maxStopSequences is OpenAI's limit on the stop array (§7.5).
const maxStopSequences = 4

// synthesizedChunkBytes caps one synthesized text chunk (§8.8: synthesized
// text is split through openaiup.SplitPieces at 2048-byte pieces, cut at rune
// boundaries).
const synthesizedChunkBytes = 2048

// OpenAIMessage is one chat.completions message in the outbound body. Content
// is a plain string for a text-only turn and a []any content array when the
// turn carries multimodal parts (§7.1, §7.2).
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// OpenAIToolCall is one assistant tool call. Arguments is a compact JSON string
// because that is the OpenAI spelling of Gemini's args object (§7.3).
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIToolCallFunc `json:"function"`
}

// OpenAIToolCallFunc is the function payload of one tool call.
type OpenAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool is one entry of the outbound tools array.
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction is the function payload of one tool. Parameters stays a
// json.RawMessage so a parametersJsonSchema declaration forwards byte-identical
// (§7.4).
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// OpenAIToolChoiceFunction names the one function an ANY-with-one-name
// toolConfig pins (§7.4).
type OpenAIToolChoiceFunction struct {
	Name string `json:"name"`
}

// OpenAIToolChoice is the object form of tool_choice. The string form
// ("auto"/"required"/"none") is emitted as a plain string.
type OpenAIToolChoice struct {
	Type     string                   `json:"type"`
	Function OpenAIToolChoiceFunction `json:"function"`
}

// OpenAITextPart is one text element of a multimodal content array (§7.2).
type OpenAITextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// OpenAIImageURL is the image_url payload of one multimodal element.
type OpenAIImageURL struct {
	URL string `json:"url"`
}

// OpenAIImagePart is one image element of a multimodal content array: a Gemini
// inlineData becomes an OpenAI data-URI (§7.2).
type OpenAIImagePart struct {
	Type     string         `json:"type"`
	ImageURL OpenAIImageURL `json:"image_url"`
}

// geminiOnlySchemaKeys are the Schema keys that mean nothing to a JSON Schema
// validator; §7.4 strips them defensively.
var geminiOnlySchemaKeys = []string{"behavior", "format"}

// contentSeg is one element of a multimodal content array, kept in part order
// so a mixed text/image turn survives translation in order.
type contentSeg struct {
	text  string
	image *InlineData
}

// msgAcc accumulates the OpenAI message for one run of consecutive same-role
// Gemini contents, which §7.1 merges into a single message.
type msgAcc struct {
	role  string
	segs  []contentSeg
	calls []OpenAIToolCall
}

func (a *msgAcc) empty() bool { return len(a.segs) == 0 && len(a.calls) == 0 }

// materialize renders the accumulated message. Content is a plain string when
// every segment is text and a content array only when the turn carries
// multimodal parts.
func (a *msgAcc) materialize() OpenAIMessage {
	m := OpenAIMessage{Role: a.role}
	if len(a.calls) > 0 {
		m.ToolCalls = a.calls
	}
	images := 0
	for i := range a.segs {
		if a.segs[i].image != nil {
			images++
		}
	}
	if images == 0 {
		var b strings.Builder
		first := true
		for i := range a.segs {
			if a.segs[i].text == "" {
				continue
			}
			if !first {
				b.WriteByte('\n')
			}
			b.WriteString(a.segs[i].text)
			first = false
		}
		if s := b.String(); s != "" {
			m.Content = s
		}
		return m
	}
	arr := make([]any, 0, len(a.segs))
	text := ""
	hasText := false
	flushText := func() {
		if hasText {
			arr = append(arr, OpenAITextPart{Type: "text", Text: text})
			text, hasText = "", false
		}
	}
	for i := range a.segs {
		s := &a.segs[i]
		if s.image != nil {
			flushText()
			arr = append(arr, OpenAIImagePart{
				Type:     "image_url",
				ImageURL: OpenAIImageURL{URL: "data:" + s.image.MimeType + ";base64," + s.image.Data},
			})
			continue
		}
		if s.text == "" {
			continue
		}
		if hasText {
			text += "\n"
		}
		text += s.text
		hasText = true
	}
	flushText()
	if len(arr) > 0 {
		m.Content = arr
	}
	return m
}

// droppedPart counts non-text parts the provider cannot accept, grouped by mime
// type in first-seen order so the §7.2 note is deterministic.
type droppedPart struct {
	mime string
	n    int
}

// openCall is one unresolved functionCall in the §7.3 pairing scan.
type openCall struct {
	id     string
	name   string
	popped bool
}

// callScan is the §7.3 id-pairing state for one request. It is a pure function
// of the request bytes: an id is either the one agy sent or the positional
// "call_<n>" synthesis, so a resent history always yields the same ids.
//
// The open calls are hash-indexed instead of rescanned. A linear scan of the
// whole queue per functionResponse makes translation O(calls x responses): a
// 32 MB body of minimal parts (~10^5 pairs) drives ~10^11 comparisons and
// minutes of CPU inside the synchronous decode/translate, before any upstream
// watchdog can fire (FIX-34). Each ladder rung therefore reads an index: the
// exact-id rung a per-id FIFO, the name rung a per-name FIFO, and the positional
// fallback a global FIFO with a forward-only cursor. The indexes are lazy — an
// entry popped by one rung is discarded from the other queues when they reach
// it — so every entry is skipped at most once, each rung is O(1) amortized, and
// the ladder still resolves in scan order (the rungs see exactly the calls the
// linear scan would have seen, in the same order).
type callScan struct {
	order      []*openCall
	orderHead  int
	openByID   map[string][]*openCall
	openByName map[string][]*openCall
	ordinal    int
	orphans    int
}

// registerCall returns the OpenAI tool_call id of one functionCall part and
// pushes the call onto the open queues. The ordinal counts every functionCall
// part in the history, whether or not agy gave it an id, so ids never shift
// when agy appends turns (PT-4).
func (s *callScan) registerCall(fc *FunctionCall) string {
	id := fc.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", s.ordinal)
	}
	s.ordinal++
	call := &openCall{id: id, name: fc.Name}
	s.order = append(s.order, call)
	if s.openByID == nil {
		s.openByID = map[string][]*openCall{}
		s.openByName = map[string][]*openCall{}
	}
	s.openByID[id] = append(s.openByID[id], call)
	s.openByName[fc.Name] = append(s.openByName[fc.Name], call)
	return id
}

// pair resolves the tool_call_id of one functionResponse part per the §7.3
// ladder: exact id match, then per-name FIFO, then the oldest open call of any
// name, then orphan. The returned id is always the paired call's id, never the
// response's own id.
func (s *callScan) pair(respID, respName string) (id string, orphan bool) {
	if respID != "" {
		if call := s.take(s.openByID, respID); call != nil {
			return call.id, false
		}
	}
	if respName != "" {
		if call := s.take(s.openByName, respName); call != nil {
			return call.id, false
		}
	}
	if call := s.takeOldest(); call != nil {
		return call.id, false
	}
	return "", true
}

// take pops the oldest still-open call from one keyed FIFO queue. Entries
// another rung already popped are discarded on the way, so the head is always
// the oldest call that rung would have found by scanning.
func (s *callScan) take(queue map[string][]*openCall, key string) *openCall {
	if queue == nil {
		return nil // no call has been registered yet: an orphan response.
	}
	entries := queue[key]
	for len(entries) > 0 {
		call := entries[0]
		entries = entries[1:]
		if !call.popped {
			queue[key] = entries
			call.popped = true
			return call
		}
	}
	queue[key] = nil
	return nil
}

// takeOldest pops the oldest still-open call of any name, the §7.3 rung-3
// positional fallback. The cursor only moves forward and skips entries another
// rung already popped, so the skip work is amortized over the whole scan.
func (s *callScan) takeOldest() *openCall {
	for s.orderHead < len(s.order) {
		call := s.order[s.orderHead]
		s.orderHead++
		if !call.popped {
			call.popped = true
			return call
		}
	}
	return nil
}

// orphanID is the §7.3 case-4 fallback: the response's own id when it carried
// one, otherwise a synthesized ordinal.
func (s *callScan) orphanID(respID string) string {
	if respID != "" {
		return respID
	}
	id := fmt.Sprintf("call_orphan_%d", s.orphans)
	s.orphans++
	return id
}

// ToOpenAIMessages translates one Gemini request into the OpenAI messages
// array (§7.1-§7.3):
//
//   - systemInstruction text parts merge with "\n" into messages[0];
//   - role "user" maps to user and "model" to assistant, and consecutive
//     same-role contents merge into one message;
//   - empty text parts are skipped, and a content whose parts are all empty is
//     skipped from emission while still being indexed by the id scan, so ids
//     stay resend-stable;
//   - thought parts never reach user-visible content;
//   - functionCall parts become assistant tool_calls in part order, with the
//     args object rendered as a compact JSON string;
//   - functionResponse parts become tool messages whose tool_call_id is the
//     paired call's id per the §7.3 ladder.
//
// supportsVision gates the multimodal mapping (§7.2): images become image_url
// data-URI parts only on a vision-capable model, and every dropped non-text
// part adds one line to the returned audit notes. The notes never enter the
// system message, so its bytes depend on the system instruction alone and the
// cacheable prefix head stays stable whatever the request dropped. The codec
// logs nothing itself; the caller decides where the notes go.
func ToOpenAIMessages(req *GenerateContentRequest, supportsVision bool) ([]OpenAIMessage, []string) {
	if req == nil {
		return nil, nil
	}
	var out []OpenAIMessage
	var systemNotes, notes []string
	var pending *msgAcc
	scan := &callScan{}
	var dropped []droppedPart
	droppedIdx := map[string]int{}
	drop := func(mime string) {
		if i, ok := droppedIdx[mime]; ok {
			dropped[i].n++
			return
		}
		droppedIdx[mime] = len(dropped)
		dropped = append(dropped, droppedPart{mime: mime, n: 1})
	}
	flush := func() {
		if pending != nil {
			if !pending.empty() {
				out = append(out, pending.materialize())
			}
			pending = nil
		}
	}
	// ensure opens the pending message for one run of consecutive same-role
	// contents; any role change flushes the previous run first (§7.1).
	ensure := func(role string) {
		if pending != nil && pending.role == role {
			return
		}
		flush()
		pending = &msgAcc{role: role}
	}

	systemText := ""
	if req.SystemInstruction != nil {
		systemText = joinTexts(req.SystemInstruction.Parts)
	}

	for ci := range req.Contents {
		c := &req.Contents[ci]
		role, roleNote := mapRole(c.Role)
		if roleNote != "" {
			notes = append(notes, roleNote)
		}
		for pi := range c.Parts {
			if c.Parts[pi].FunctionCall != nil {
				// Tool calls only ever ride on an assistant message, whatever
				// the enclosing content's role says (§7.3). The turn's role is
				// decided up front so the message is built as one unit.
				role = "assistant"
				break
			}
		}
		for pi := range c.Parts {
			p := &c.Parts[pi]
			switch {
			case p.FunctionCall != nil:
				args, err := json.Marshal(p.FunctionCall.Args)
				if err != nil || len(args) == 0 || string(args) == "null" {
					// Absent or empty args serialize as an empty object, never
					// as the JSON null a nil map produces.
					args = []byte("{}")
				}
				ensure(role)
				pending.calls = append(pending.calls, OpenAIToolCall{
					ID:   scan.registerCall(p.FunctionCall),
					Type: "function",
					Function: OpenAIToolCallFunc{
						Name:      p.FunctionCall.Name,
						Arguments: string(args),
					},
				})
			case p.FunctionResponse != nil:
				// §7.3: the scan is strictly part-order, so a response pairs
				// against the calls seen so far, including earlier parts of this
				// same content. Flushing first keeps the tool message after the
				// assistant message that carries this content's text and calls.
				flush()
				out = append(out, scan.toolMessage(p, &notes))
			case p.InlineData != nil:
				// The mime is request-controlled text that reaches the provider
				// in the §7.2 data URI and in the drop note, so it is sanitized
				// once here and both paths use the clean value.
				mime := sanitizeMime(p.InlineData.MimeType)
				if supportsVision && isImageMime(mime) {
					ensure(role)
					d := *p.InlineData
					d.MimeType = mime
					pending.segs = append(pending.segs, contentSeg{image: &d})
					continue
				}
				drop(mime)
			case p.Thought:
				// §7.1 and §7.10: thought text never maps to user-visible
				// content, in either direction.
			default:
				if p.Text != "" {
					ensure(role)
					pending.segs = append(pending.segs, contentSeg{text: p.Text})
				}
			}
		}
	}
	flush()

	for _, d := range dropped {
		systemNotes = append(systemNotes, fmt.Sprintf("[agy-pro] %d non-text part(s) of type %s omitted: model lacks support", d.n, d.mime))
	}
	// The notes stay off systemText on purpose: the system message is the head
	// of the cacheable prefix, so folding a request-dependent dropped-part list
	// into it would rewrite messages[0] bytes as the request grows and break
	// prefix caching. The notes reach the caller's audit surface instead, and
	// the model sees the system instruction and nothing else.
	if systemText != "" {
		out = append([]OpenAIMessage{{Role: "system", Content: systemText}}, out...)
	}
	return out, append(systemNotes, notes...)
}

// toolMessage renders one functionResponse part as an OpenAI tool message
// (§7.3): the paired call's id, the tool name, and the payload under
// response.output when it is a string.
func (s *callScan) toolMessage(p *Part, notes *[]string) OpenAIMessage {
	fr := p.FunctionResponse
	id, orphan := s.pair(fr.ID, fr.Name)
	if orphan {
		id = s.orphanID(fr.ID)
		*notes = append(*notes, fmt.Sprintf("functionResponse %q paired with no open call; emitted tool_call_id %s", fr.Name, id))
	}
	content, truncated := toolPayload(fr.Response)
	if truncated {
		*notes = append(*notes, fmt.Sprintf("functionResponse %q truncated at %d bytes", fr.Name, maxToolResponseBytes))
	}
	return OpenAIMessage{Role: "tool", ToolCallID: id, Name: fr.Name, Content: content}
}

// toolPayload renders one functionResponse as the tool message content
// (§7.3): response.output when it is a string, otherwise the compact JSON of
// the response object. A payload past maxToolResponseBytes is cut at a rune
// boundary and carries the truncation marker.
func toolPayload(resp map[string]any) (string, bool) {
	if s, ok := resp["output"].(string); ok {
		return truncateToolPayload(s)
	}
	if len(resp) == 0 {
		return truncateToolPayload("{}")
	}
	return truncateToolPayload(compactJSON(resp))
}

// truncateToolPayload caps one payload at maxToolResponseBytes. The cut backs
// off to a rune boundary so the emitted JSON never carries a half-rune, which
// json.Marshal would coerce to U+FFFD.
func truncateToolPayload(s string) (string, bool) {
	if len(s) <= maxToolResponseBytes {
		return s, false
	}
	cut := maxToolResponseBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// Defensive: only reachable for invalid UTF-8, which json.Marshal
		// coerces anyway.
		cut = maxToolResponseBytes
	}
	return s[:cut] + fmt.Sprintf("…[truncated %d bytes]", len(s)-cut), true
}

// compactJSON renders one value as compact JSON, falling back to an empty
// object when the value cannot be encoded.
func compactJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// joinTexts merges a content's text parts with "\n" (§7.1). Empty parts are
// skipped and thought parts never contribute.
func joinTexts(parts []Part) string {
	var b strings.Builder
	first := true
	for i := range parts {
		p := &parts[i]
		if p.Thought || p.Text == "" {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString(p.Text)
		first = false
	}
	return b.String()
}

// mapRole maps a Gemini content role onto the OpenAI role (§7.1). The legacy
// "function" synonym and unknown roles are reported through a note so the audit
// log sees them; functionCall parts always land on an assistant message
// regardless of the content role.
func mapRole(role string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "user", ""
	case "model":
		return "assistant", ""
	case "function":
		return "user", fmt.Sprintf("legacy Gemini role %q treated as a tool turn", role)
	case "":
		return "user", ""
	default:
		return role, fmt.Sprintf("unknown Gemini role %q passed through verbatim", role)
	}
}

// isImageMime reports whether a mime type is an image (§7.2).
func isImageMime(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "image/")
}

// maxNoteMimeRunes caps how much of a mime type reaches the §7.2 drop note.
const maxNoteMimeRunes = 64

// sanitizeMime renders one request-controlled mime type safe for the outbound
// body. The mime reaches the provider twice: inside the §7.2 data URI and inside
// the drop note, which rides the returned audit channel (never the system
// message, post-A3). The note is a single line by contract, so an allow-list
// charset (RFC 6838 tokens plus the subtype separator), a length cap and the
// implicit stripping of everything else keep a crafted mime from forging extra
// lines in the note. A mime that sanitizes to nothing becomes the literal
// "unknown".
func sanitizeMime(mime string) string {
	var b strings.Builder
	for _, r := range mime {
		if b.Len() >= maxNoteMimeRunes {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '/' || r == '+' || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// ToOpenAITools translates tools[].functionDeclarations into the OpenAI tools
// array and toolConfig into tool_choice (§7.4). A declaration with
// parametersJsonSchema forwards it byte-identical; a legacy parameters map is
// marshaled as-is; the Gemini-only schema keys "behavior" and "format" are
// stripped defensively from either spelling. The whole native tool set is
// forwarded unfiltered: whether tools go out at all is the caller's decision,
// gated on caps.CanUseTools() (§6.2).
//
// The returned tool_choice is nil when the request carried no toolConfig, a
// string for the AUTO/ANY/NONE modes, and an OpenAIToolChoice when ANY pins a
// single allowed name. The caller must drop tool_choice together with tools.
func ToOpenAITools(req *GenerateContentRequest) ([]OpenAITool, any, []string) {
	if req == nil {
		return nil, nil, nil
	}
	var tools []OpenAITool
	var notes []string
	for ti := range req.Tools {
		for di := range req.Tools[ti].FunctionDeclarations {
			d := &req.Tools[ti].FunctionDeclarations[di]
			if d.Name == "" {
				notes = append(notes, "skipped a function declaration with no name")
				continue
			}
			fn := OpenAIFunction{Name: d.Name, Description: d.Description}
			if schema := declarationSchema(d, &notes); len(schema) > 0 {
				fn.Parameters = schema
			}
			tools = append(tools, OpenAITool{Type: "function", Function: fn})
		}
	}
	return tools, toolChoice(req.ToolConfig, &notes), notes
}

// declarationSchema renders one declaration's parameters for the OpenAI side.
func declarationSchema(d *FunctionDeclaration, notes *[]string) json.RawMessage {
	var raw json.RawMessage
	switch {
	case len(d.ParametersJSONSchema) > 0:
		raw = d.ParametersJSONSchema
	case len(d.Parameters) > 0:
		b, err := json.Marshal(d.Parameters)
		if err != nil {
			*notes = append(*notes, fmt.Sprintf("declaration %q: unencodable parameters dropped", d.Name))
			return nil
		}
		raw = b
	default:
		return nil
	}
	return stripGeminiSchemaKeys(raw)
}

// stripGeminiSchemaKeys removes the Gemini-only keys from a schema object's top
// level. A schema that carries none of them is returned byte-identical, so the
// parametersJsonSchema passthrough stays verbatim in the common case.
func stripGeminiSchemaKeys(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return raw
	}
	changed := false
	for _, k := range geminiOnlySchemaKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// toolChoice maps toolConfig.functionCallingConfig onto OpenAI's tool_choice
// (§7.4). ANY with more than one allowed name degrades to "required" because
// OpenAI has no list form; that downgrade is reported through a note.
func toolChoice(tc *ToolConfig, notes *[]string) any {
	if tc == nil || tc.FunctionCallingConfig == nil {
		return nil
	}
	fc := tc.FunctionCallingConfig
	mode := strings.ToUpper(strings.TrimSpace(fc.Mode))
	switch mode {
	case "NONE":
		return "none"
	case "ANY":
		switch len(fc.AllowedFunctionNames) {
		case 0:
			return "required"
		case 1:
			return OpenAIToolChoice{Type: "function", Function: OpenAIToolChoiceFunction{Name: fc.AllowedFunctionNames[0]}}
		default:
			*notes = append(*notes, fmt.Sprintf("allowedFunctionNames carries %d names; tool_choice downgraded to required", len(fc.AllowedFunctionNames)))
			return "required"
		}
	case "AUTO", "MODE_UNSPECIFIED", "":
		return "auto"
	default:
		*notes = append(*notes, fmt.Sprintf("unknown functionCallingConfig.mode %q treated as auto", fc.Mode))
		return "auto"
	}
}

// ToOpenAIParams translates generationConfig into the OpenAI scalar parameters
// (§7.5, §7.6, §7.13). registryMaxOutputTokens is the registry's per-model cap:
// when it is > 0, agy's maxOutputTokens is clamped to it, because agy sends
// 65536 and small-context providers reject that (W4, §6.4).
//
// The bool return reports whether the caller may surface provider reasoning as
// Gemini thought parts. It is false when the model does not support thinking or
// the request did not ask for thoughts, and the caller must then drop reasoning
// deltas: they would otherwise render as user-visible text (§7.6, §7.10).
//
// The field-name choices that depend on the provider family (max_tokens versus
// max_completion_tokens, and stripping temperature/top_p on reasoning series)
// stay with the caller: the codec emits the family-neutral default and the
// family table lives in §6.3.
func ToOpenAIParams(req *GenerateContentRequest, caps providers.Capabilities, registryMaxOutputTokens int) (map[string]any, bool, []string) {
	params := map[string]any{}
	if req == nil || req.GenerationConfig == nil {
		return params, false, nil
	}
	var notes []string
	gc := req.GenerationConfig

	if gc.Temperature != nil {
		params["temperature"] = *gc.Temperature
	}
	if gc.TopP != nil {
		params["top_p"] = *gc.TopP
	}
	if gc.MaxOutputTokens > 0 {
		v := gc.MaxOutputTokens
		if registryMaxOutputTokens > 0 && v > registryMaxOutputTokens {
			notes = append(notes, fmt.Sprintf("maxOutputTokens clamped from %d to %d", v, registryMaxOutputTokens))
			v = registryMaxOutputTokens
		}
		params["max_tokens"] = v
	}
	if len(gc.StopSequences) > 0 {
		stop := gc.StopSequences
		if len(stop) > maxStopSequences {
			notes = append(notes, fmt.Sprintf("stopSequences truncated from %d to %d", len(stop), maxStopSequences))
			stop = stop[:maxStopSequences]
		}
		params["stop"] = append([]string(nil), stop...)
	}
	if gc.CandidateCount > 1 {
		params["n"] = gc.CandidateCount
	}
	if gc.Seed != nil {
		params["seed"] = *gc.Seed
	}
	if gc.PresencePenalty != nil {
		params["presence_penalty"] = *gc.PresencePenalty
	}
	if gc.FrequencyPenalty != nil {
		params["frequency_penalty"] = *gc.FrequencyPenalty
	}

	schema := gc.ResponseJsonSchema
	if schema == nil {
		// Tolerated on the public surface too: envelope.go performs the
		// Cloud Code rename on the way in, so by this point the value normally
		// already sits in ResponseJsonSchema.
		schema = gc.ResponseSchema
	}
	switch {
	case schema != nil:
		params["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"schema": schema,
				"strict": false,
			},
		}
	case isJSONMime(gc.ResponseMimeType):
		params["response_format"] = map[string]any{"type": "json_object"}
	}

	allowThoughts := caps.SupportsThinking && gc.ThinkingConfig != nil && gc.ThinkingConfig.IncludeThoughts
	if caps.SupportsThinking {
		switch effort := registryOrLadderEffort(caps, gc.ThinkingConfig); effort {
		case "":
			// Provider decides; no field.
		case "none":
			// §7.6 rows 2-3: an explicit disable. openaiup.ApplyEffort strips
			// "none" because the bridge reads it as "unset", so the adapter
			// sets it directly.
			params["reasoning_effort"] = "none"
		default:
			openaiup.ApplyEffort(params, caps, effort)
		}
	}
	return params, allowThoughts, notes
}

// registryOrLadderEffort picks the reasoning effort (§7.6): a per-model registry
// value wins and is validated plus wire-mapped through openaiup, otherwise the
// value derives from thinkingBudget.
func registryOrLadderEffort(caps providers.Capabilities, tc *ThinkingConfig) string {
	if caps.ReasoningEffort != "" {
		return openaiup.MapReasoningEffort(openaiup.NormalizeReasoningEffort(caps.ReasoningEffort))
	}
	if tc == nil || tc.ThinkingBudget == nil {
		return ""
	}
	switch b := *tc.ThinkingBudget; {
	case b < 0:
		return "" // -1 is the dynamic budget: let the provider decide.
	case b == 0:
		return "none" // thinking explicitly disabled.
	case b < 4096:
		return "low"
	case b <= 16383:
		return "medium"
	default:
		return "high"
	}
}

// isJSONMime reports whether a responseMimeType asks for JSON output (§7.13).
func isJSONMime(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "application/json")
}

// TurnToGenerateResponse maps one aggregated OpenAI turn onto the Gemini
// response shape (§7.7-§7.12): reasoning first as a single thought part when
// includeThoughts is set, then a provider refusal as a text part, then the
// content text, then one functionCall part per ordered call, then the mapped
// finish reason and usage.
//
// It is also the §9.2 never-textless-200 choke point for the canonical path: a
// turn that produced zero text, zero thought text, zero tool calls and zero
// refusal gains the fixed apology as its text parts, so a response mapped from a
// real upstream turn always carries at least one part. Use
// TurnToGenerateResponseWithCause to append the provider name and the redacted
// cause to that apology.
//
// finishReason is the provider's raw finish_reason, "" when the caller has none.
// The Turn carries its own finish_reason and choice index (openaiup records them
// while merging), so a caller that did not capture the value separately still
// gets the provider's real reason and index instead of the §7.9 default. An
// explicit parameter wins, which keeps a caller that mapped the value itself in
// control. The response never carries a responseId, because generating one needs
// randomness and translation stays a pure function of its inputs; the caller may
// assign resp.ResponseID = openaiup.NewCompletionID() when it wants one.
func TurnToGenerateResponse(turn *openaiup.Turn, includeThoughts bool, finishReason string) (*GenerateContentResponse, []string) {
	return TurnToGenerateResponseWithCause(turn, includeThoughts, finishReason, "")
}

// TurnToGenerateResponseWithCause is TurnToGenerateResponse with the §9.2
// apology cause: the "provider <name>: <redacted one-line cause>" suffix the
// adapter appends when a turn produced nothing user-visible. An empty cause
// falls back to DefaultApologyCause, so the codec never invents a provider name.
func TurnToGenerateResponseWithCause(turn *openaiup.Turn, includeThoughts bool, finishReason, cause string) (*GenerateContentResponse, []string) {
	if turn == nil {
		turn = openaiup.NewTurn()
	}
	raw := finishReason
	if raw == "" {
		raw = turn.FinishReason
	}
	var notes []string
	parts := make([]Part, 0, 4)
	if includeThoughts {
		if r := turn.Reasoning(); r != "" {
			parts = append(parts, Part{Text: r, Thought: true})
		}
	}
	if r := turn.Refusal; r != "" {
		// §7.1 response row: a provider refusal surfaces as ordinary text.
		parts = append(parts, Part{Text: r})
	}
	if c := turn.Content(); c != "" {
		parts = append(parts, Part{Text: c})
	}
	for i, call := range turn.OrderedCalls() {
		args, ok := parseToolArgs(call.Args)
		if !ok {
			notes = append(notes, fmt.Sprintf("tool call %d (%s): unparseable arguments emitted as {}", i, call.Name))
		}
		parts = append(parts, Part{FunctionCall: &FunctionCall{ID: call.ID, Name: call.Name, Args: args}})
	}
	if len(parts) == 0 {
		// §9.2: nothing user-visible came back, so the turn becomes the apology
		// before the finishReason mapping. Doing it here rather than only in the
		// emitter is what makes the guarantee hold on the canonical path, where
		// MapFinishReason always supplies a reason.
		for _, piece := range openaiup.SplitPieces(apologyText(cause), synthesizedChunkBytes) {
			parts = append(parts, Part{Text: piece})
		}
	}
	return &GenerateContentResponse{
		Candidates: []Candidate{{
			Content:      &Content{Role: "model", Parts: parts},
			FinishReason: MapFinishReason(raw),
			Index:        turn.ChoiceIndex,
		}},
		UsageMetadata: mapUsage(turn.Usage),
	}, notes
}

// parseToolArgs parses a provider arguments string into the object Gemini's
// functionCall.args expects (§7.4 response row). An empty string is a valid
// empty argument set; an unparseable one degrades to {} with a note instead of
// crashing the stream (§8.7).
func parseToolArgs(s string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return map[string]any{}, true
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return map[string]any{}, false
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, true
}

// MapFinishReason maps an OpenAI finish_reason onto the Gemini finishReason
// (§7.9). An absent, null or unknown value maps to STOP so a final chunk never
// omits the reason, and Gemini-only reasons are never fabricated.
func MapFinishReason(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		// "stop", "tool_calls", the legacy "function_call" and anything
		// unrecognized all end STOP.
		return "STOP"
	}
}

// mapUsage maps the OpenAI usage block onto usageMetadata (§7.8). A missing
// block yields nil rather than fabricated counts, and a missing total is
// recomputed as prompt plus completion. The cache count has two provider
// spellings, the DeepSeek-native top-level prompt_cache_hit_tokens and the
// OpenAI nested prompt_tokens_details.cached_tokens. NATIVE-FIRST: the explicit
// native hit wins when it is present and valid, otherwise the nested count is
// the fallback; usageInt drops a negative as absent, so neither a missing nor an
// invalid count fabricates a value. This is the same precedence the fanout
// reader's cachedTokens() applies, so both cache readers agree.
func mapUsage(u map[string]any) *UsageMetadata {
	if len(u) == 0 {
		return nil
	}
	prompt, hasPrompt := usageInt(u, "prompt_tokens")
	completion, hasCompletion := usageInt(u, "completion_tokens")
	total, hasTotal := usageInt(u, "total_tokens")
	if !hasPrompt && !hasCompletion && !hasTotal {
		return nil
	}
	if !hasTotal {
		total = prompt + completion
	}
	um := &UsageMetadata{
		PromptTokenCount:     prompt,
		CandidatesTokenCount: completion,
		TotalTokenCount:      total,
	}
	if details, ok := u["completion_tokens_details"].(map[string]any); ok {
		if v, ok := usageInt(details, "reasoning_tokens"); ok {
			um.ThoughtsTokenCount = v
		}
	}
	// NATIVE-FIRST: prefer the DeepSeek-native top-level hit when present and
	// valid (usageInt already rejects a negative); fall back to the OpenAI
	// nested prompt_tokens_details.cached_tokens only when the native hit is
	// absent or invalid.
	cached, hasCached := 0, false
	if v, ok := usageInt(u, "prompt_cache_hit_tokens"); ok {
		cached, hasCached = v, true
	}
	if !hasCached {
		if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
			if v, ok := usageInt(details, "cached_tokens"); ok {
				cached, hasCached = v, true
			}
		}
	}
	if hasCached {
		um.CachedContentTokenCount = cached
	}
	return um
}

// usageInt reads one token count out of a provider usage block. Providers spell
// numbers as JSON numbers, so float64 is the common case; a negative count is
// treated as absent rather than fabricated.
func usageInt(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	var n int64
	switch t := v.(type) {
	case float64:
		n = int64(t)
	case float32:
		n = int64(t)
	case int:
		n = int64(t)
	case int32:
		n = int64(t)
	case int64:
		n = t
	case uint64:
		if t > 1<<62 {
			return 0, false
		}
		n = int64(t)
	case json.Number:
		parsed, err := t.Int64()
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	if n < 0 {
		return 0, false
	}
	return int(n), true
}
