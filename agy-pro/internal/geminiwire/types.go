// Package geminiwire is the pure codec for the local Gemini-wire adapter. It
// decodes agy's native Gemini wire (the public /v1beta genai-SDK surface and
// the /v1internal Cloud-Code envelope), translates it to the OpenAI-compatible
// provider wire and back, and frames provider answers as Gemini SSE.
//
// The package performs no I/O: no HTTP, no goroutines, no clock, no randomness.
// Every function is a pure function of its inputs, and the only ids the codec
// ever synthesizes are history-derived (the §7.3 pairing scan), so the same
// request bytes always produce the same output bytes. All I/O, routing, auth
// and audit live in the adapter that consumes this package.
//
// Spec: docs/261014_0006_session_native-coexistence-design/wire-translation-spec.md
package geminiwire

import "encoding/json"

// GenerateContentRequest is the MLDev GenerateContentRequest body agy sends to
// /v1beta/models/<model>:generateContent. The decoder ignores unknown keys, so
// an SDK bump that adds a field never fails a request (§5.4).
type GenerateContentRequest struct {
	Contents          []Content         `json:"contents,omitempty"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	SafetySettings    []SafetySetting   `json:"safetySettings,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
}

// Content is one conversation turn. Role is "user", "model" or the legacy
// "function" synonym.
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part is one content part. The part-kind discriminator is pointer-nil
// discipline: a part is a text part unless exactly one of InlineData,
// FunctionCall or FunctionResponse is non-nil, and those three pointers are
// mutually exclusive by construction. Thought marks a text part as model
// reasoning: such a part never maps to user-visible content in either
// direction (§7.1, §7.10). ThoughtSignature is decoded and re-emitted verbatim
// but is never fabricated and is dropped on the provider side (§7.10).
type Part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

// InlineData is one multimodal part (image, audio, video or PDF).
type InlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// FunctionCall is a Gemini tool call. Args is a JSON object, which is why the
// OpenAI side needs it as a compact string and back (§7.3, §7.4).
type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name,omitempty"`
	Args map[string]any `json:"args"`
}

// FunctionResponse is agy's tool answer. The captured shape carries both an id
// and the tool name, with the payload under key "output" as a string (W8).
type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

// FunctionDeclaration is one native tool declaration. ParametersJSONSchema is
// the JSON-Schema-shaped spelling agy's SDK emits; Parameters is the legacy
// Gemini Schema spelling.
type FunctionDeclaration struct {
	Name                 string          `json:"name,omitempty"`
	Description          string          `json:"description,omitempty"`
	Parameters           map[string]any  `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

// Tool is one tools[] entry. agy attaches its whole 13-tool native set and the
// adapter forwards it unfiltered (§7.4).
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// GenerationConfig carries the sampling knobs. Pointer fields keep the
// "absent" state distinct from a meaningful zero.
type GenerationConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"topP,omitempty"`
	TopK               int             `json:"topK,omitempty"`
	StopSequences      []string        `json:"stopSequences,omitempty"`
	CandidateCount     int             `json:"candidateCount,omitempty"`
	ResponseMimeType   string          `json:"responseMimeType,omitempty"`
	ResponseSchema     map[string]any  `json:"responseSchema,omitempty"`
	ResponseJsonSchema map[string]any  `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *ThinkingConfig `json:"thinkingConfig,omitempty"`
	Seed               *int            `json:"seed,omitempty"`
	PresencePenalty    *float64        `json:"presencePenalty,omitempty"`
	FrequencyPenalty   *float64        `json:"frequencyPenalty,omitempty"`
}

// ThinkingConfig is agy's thinking knob. ThinkingBudget is a pointer because
// three states are distinct: absent (provider decides), 0 (thinking disabled)
// and a positive magnitude (the §7.6 ladder input).
type ThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int `json:"thinkingBudget,omitempty"`
}

// ToolConfig carries the function-calling mode.
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// FunctionCallingConfig is the toolConfig payload: Mode is AUTO, ANY or NONE,
// and AllowedFunctionNames narrows ANY to specific tools.
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

// SafetySetting is one safetySettings[] entry. The adapter drops them
// silently: there is no OpenAI equivalent (§7.11).
type SafetySetting struct {
	Category  string `json:"category,omitempty"`
	Threshold string `json:"threshold,omitempty"`
}

// UsageMetadata is the Gemini usage block. ThoughtsTokenCount and
// CachedContentTokenCount are omitted when zero so the common shape stays the
// canonical three counts (§7.8).
type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
}

// GenerateContentResponse is one Gemini response body, and equally one SSE
// chunk body (§7.7, §8.4).
type GenerateContentResponse struct {
	Candidates    []Candidate    `json:"candidates"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}

// Candidate is one candidate. Content is nil only when a chunk carries nothing
// but a finishReason; the emitter always sets it so the canonical final chunk
// keeps its empty parts array (§8.5).
type Candidate struct {
	Content      *Content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	Index        int      `json:"index"`
}

// ListModelsResponse is the GET /v1beta/models body shape (§5.7). The codec
// owns the shape only; the registry-backed population of it lives in the
// adapter.
type ListModelsResponse struct {
	Models        []ModelInfo `json:"models"`
	NextPageToken string      `json:"nextPageToken"`
}

// ModelInfo is one entry of ListModelsResponse.
type ModelInfo struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
}

// V1InternalRequest is the Cloud Code /v1internal request envelope agy sends
// when CLOUD_CODE_URL points at the adapter (Appendix A). The inner Gemini
// payload rides in Request; routing keys off Model.
type V1InternalRequest struct {
	Project     string          `json:"project,omitempty"`
	Model       string          `json:"model,omitempty"`
	UserAgent   string          `json:"userAgent,omitempty"`
	RequestType string          `json:"requestType,omitempty"`
	RequestID   string          `json:"requestId,omitempty"`
	Request     json.RawMessage `json:"request,omitempty"`
}

// V1InternalResponse is the Cloud Code response envelope: the inner
// GenerateContentResponse, or one stream chunk, rides in Response.
type V1InternalResponse struct {
	Response json.RawMessage `json:"response"`
}
