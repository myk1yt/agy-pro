package geminiwire

import (
	"strings"
	"testing"

	"agypro/internal/openaiup"
)

// ---------------------------------------------------------------------------
// §7.8 cache-count spellings (DeepSeek-native alongside OpenAI nested)
// ---------------------------------------------------------------------------

// TestMapUsage_DeepSeekNativeCacheFields pins the cache-count normalization:
// a DeepSeek-native usage block reports the cache split at the TOP level
// (prompt_cache_hit_tokens / prompt_cache_miss_tokens) and never carries the
// OpenAI nested prompt_tokens_details shape, so the native hit field must land
// on CachedContentTokenCount on its own. The native hit wins when both are
// present, and neither shape fabricates a count.
func TestMapUsage_DeepSeekNativeCacheFields(t *testing.T) {
	turn := func(u map[string]any) *openaiup.Turn {
		t := openaiup.NewTurn()
		t.Usage = u
		return t
	}

	// Native only, no prompt_tokens_details anywhere: the hit count is the
	// cache count. (Deleting the native fallback in mapUsage fails here.)
	resp, _ := TurnToGenerateResponse(turn(map[string]any{
		"prompt_tokens":            float64(100),
		"completion_tokens":        float64(20),
		"total_tokens":             float64(120),
		"prompt_cache_hit_tokens":  float64(64),
		"prompt_cache_miss_tokens": float64(36),
	}), false, "stop")
	wantJSON(t, resp.UsageMetadata, `{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120,"cachedContentTokenCount":64}`)

	// Nested shape still maps, unchanged.
	resp, _ = TurnToGenerateResponse(turn(map[string]any{
		"prompt_tokens":         float64(12),
		"completion_tokens":     float64(5),
		"total_tokens":          float64(17),
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(4)},
	}), false, "stop")
	wantJSON(t, resp.UsageMetadata, `{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17,"cachedContentTokenCount":4}`)

	// Both shapes at once normalize to ONE count, NATIVE-FIRST: the explicit
	// DeepSeek-native hit (64) wins over the nested OpenAI count (4), matching
	// the fanout reader's cachedTokens(). Flipping mapUsage back to nested-first
	// fails here.
	resp, _ = TurnToGenerateResponse(turn(map[string]any{
		"prompt_tokens":           float64(12),
		"completion_tokens":       float64(5),
		"total_tokens":            float64(17),
		"prompt_tokens_details":   map[string]any{"cached_tokens": float64(4)},
		"prompt_cache_hit_tokens": float64(64),
	}), false, "stop")
	wantJSON(t, resp.UsageMetadata, `{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17,"cachedContentTokenCount":64}`)

	// Neither shape: no cache count is fabricated (the field stays omitted).
	for _, u := range []map[string]any{
		{"prompt_tokens": 10, "completion_tokens": 4},
		{"prompt_tokens": 10, "completion_tokens": 4, "prompt_cache_hit_tokens": "not-a-number"},
		{"prompt_tokens": 10, "completion_tokens": 4, "prompt_cache_hit_tokens": float64(-1)},
	} {
		resp, _ = TurnToGenerateResponse(turn(u), false, "stop")
		if s := mustJSON(t, resp.UsageMetadata); strings.Contains(s, "cachedContentTokenCount") {
			t.Errorf("usage %v fabricated a cache count: %s", u, s)
		}
	}
}

// Dual-shape precedence: a provider that reports BOTH the DeepSeek-native
// top-level prompt_cache_hit_tokens and the OpenAI nested
// prompt_tokens_details.cached_tokens with DIFFERENT values (960 vs 512) must
// resolve to the NATIVE hit on CachedContentTokenCount. This is the explicit
// native-first pin; flipping mapUsage back to nested-first fails here.
func TestMapUsage_NativeFirstBeatsNestedCacheCount(t *testing.T) {
	turn := openaiup.NewTurn()
	turn.Usage = map[string]any{
		"prompt_tokens":            float64(1000),
		"completion_tokens":        float64(200),
		"total_tokens":             float64(1200),
		"prompt_cache_hit_tokens":  float64(960),
		"prompt_cache_miss_tokens": float64(40),
		"prompt_tokens_details":    map[string]any{"cached_tokens": float64(512)},
	}
	resp, _ := TurnToGenerateResponse(turn, false, "stop")
	wantJSON(t, resp.UsageMetadata, `{"promptTokenCount":1000,"candidatesTokenCount":200,"totalTokenCount":1200,"cachedContentTokenCount":960}`)
}

// Native-zero-vs-nested precedence, the geminiwire mirror of the fanout
// reader's "native zero beats a nested count" case. mapUsage applies the SAME
// native-first rule, and this pins both sides of it:
//   - an explicit native hit of ZERO is a reported zero, not a missing number,
//     so it wins over a non-zero nested count and the mapped cache count is 0
//     (being zero, it is omitted from the serialized block, never emitted as the
//     nested value);
//   - a native hit that is ABSENT falls back to the nested count.
//
// Flipping mapUsage to nested-first, or treating a zero native hit as absent,
// fails the first case; deleting the nested fallback fails the second.
func TestMapUsage_NativeZeroWinsOverNestedAndAbsentFallsBack(t *testing.T) {
	turn := func(u map[string]any) *openaiup.Turn {
		tt := openaiup.NewTurn()
		tt.Usage = u
		return tt
	}

	// Native zero wins over a non-zero nested count: the mapped cache count is
	// 0, not the nested 512. usageInt returns the explicit zero as
	// present-and-valid, so the native-first branch takes it.
	resp, _ := TurnToGenerateResponse(turn(map[string]any{
		"prompt_tokens":            float64(100),
		"completion_tokens":        float64(20),
		"total_tokens":             float64(120),
		"prompt_cache_hit_tokens":  float64(0),
		"prompt_cache_miss_tokens": float64(100),
		"prompt_tokens_details":    map[string]any{"cached_tokens": float64(512)},
	}), false, "stop")
	if resp.UsageMetadata == nil {
		t.Fatal("usage metadata = nil; want a block")
	}
	if resp.UsageMetadata.CachedContentTokenCount != 0 {
		t.Errorf("cachedContentTokenCount = %d; want 0 (the explicit native zero wins over the nested 512)", resp.UsageMetadata.CachedContentTokenCount)
	}
	// Being zero, the count is omitted from the serialized block, so it never
	// masquerades as the nested value on the wire.
	if s := mustJSON(t, resp.UsageMetadata); strings.Contains(s, "cachedContentTokenCount") {
		t.Errorf("serialized usage carried cachedContentTokenCount; the zero native hit must omit it, not emit the nested value:\n%s", s)
	}

	// Native absent falls back to the nested count (512).
	resp, _ = TurnToGenerateResponse(turn(map[string]any{
		"prompt_tokens":         float64(100),
		"completion_tokens":     float64(20),
		"total_tokens":          float64(120),
		"prompt_tokens_details": map[string]any{"cached_tokens": float64(512)},
	}), false, "stop")
	if resp.UsageMetadata == nil {
		t.Fatal("usage metadata = nil; want a block")
	}
	if resp.UsageMetadata.CachedContentTokenCount != 512 {
		t.Errorf("cachedContentTokenCount = %d; want 512 (no native hit present, the nested count is the fallback)", resp.UsageMetadata.CachedContentTokenCount)
	}
}

// ---------------------------------------------------------------------------
// §7.1 system-message prefix stability across dropped parts
// ---------------------------------------------------------------------------

// TestToOpenAIMessages_DroppedPartsStayOffTheSystemPrefix pins the cacheable
// prefix head: the dropped-part notes used to be folded into systemText, so a
// request that dropped more non-text parts rewrote messages[0] bytes and broke
// prefix caching. The system message must stay byte-identical whatever the
// request dropped, while the notes still reach the returned audit surface.
func TestToOpenAIMessages_DroppedPartsStayOffTheSystemPrefix(t *testing.T) {
	img := func() *InlineData { return &InlineData{MimeType: "image/png", Data: "iVBORw0KGgo="} }
	audio := func() *InlineData { return &InlineData{MimeType: "audio/mpeg", Data: "//uQx"} }

	// Two requests that differ only in how much they drop.
	few := &GenerateContentRequest{
		SystemInstruction: &Content{Parts: []Part{{Text: "SYS"}}},
		Contents: []Content{
			{Role: "user", Parts: []Part{{Text: "look"}, {InlineData: img()}}},
		},
	}
	many := &GenerateContentRequest{
		SystemInstruction: &Content{Parts: []Part{{Text: "SYS"}}},
		Contents: []Content{
			{Role: "user", Parts: []Part{{Text: "look"}, {InlineData: img()}, {InlineData: img()}, {InlineData: audio()}, {InlineData: audio()}}},
		},
	}

	msgsFew, notesFew := ToOpenAIMessages(few, false)
	msgsMany, notesMany := ToOpenAIMessages(many, false)

	if len(msgsFew) != 2 || len(msgsMany) != 2 {
		t.Fatalf("want the system message plus one user message each, got %d and %d: %s / %s",
			len(msgsFew), len(msgsMany), mustJSON(t, msgsFew), mustJSON(t, msgsMany))
	}
	// The head of the cacheable prefix is byte-identical. (Folding the notes
	// back into systemText fails here on the second request.)
	if mustJSON(t, msgsFew[0]) != mustJSON(t, msgsMany[0]) {
		t.Errorf("system message moved with the dropped-part set\n few: %s\nmany: %s",
			mustJSON(t, msgsFew[0]), mustJSON(t, msgsMany[0]))
	}
	wantJSON(t, msgsMany[0], `{"role":"system","content":"SYS"}`)
	// The user message is untouched by the drop set too.
	if mustJSON(t, msgsFew[1]) != mustJSON(t, msgsMany[1]) {
		t.Errorf("user message moved with the dropped-part set\n few: %s\nmany: %s",
			mustJSON(t, msgsFew[1]), mustJSON(t, msgsMany[1]))
	}

	// The notes still reach the audit surface, and they still describe every
	// dropped group.
	if len(notesFew) != 1 {
		t.Errorf("want one audit note for the single dropped mime, got %v", notesFew)
	}
	if len(notesMany) != 2 {
		t.Errorf("want one audit note per dropped mime group, got %v", notesMany)
	}
	for _, n := range notesMany {
		if !strings.Contains(n, "non-text part(s) of type") || !strings.Contains(n, "omitted: model lacks support") {
			t.Errorf("audit note lost its drop description: %q", n)
		}
	}
	if mustJSON(t, notesFew) == mustJSON(t, notesMany) {
		t.Errorf("the audit surface must reflect the larger dropped-part set, got %v", notesMany)
	}

	// Without a system instruction no system message is synthesized from the
	// notes: messages[0] stays the first content message, byte-identical
	// across drop counts.
	bare := &GenerateContentRequest{Contents: []Content{
		{Role: "user", Parts: []Part{{Text: "look"}}},
	}}
	bareDropped := &GenerateContentRequest{Contents: []Content{
		{Role: "user", Parts: []Part{{Text: "look"}, {InlineData: img()}, {InlineData: audio()}}},
	}}
	msgsBare, notesBare := ToOpenAIMessages(bare, false)
	msgsBareDropped, _ := ToOpenAIMessages(bareDropped, false)
	if len(msgsBare) != 1 || len(msgsBareDropped) != 1 {
		t.Fatalf("want exactly one message without a system instruction, got %d and %d: %s / %s",
			len(msgsBare), len(msgsBareDropped), mustJSON(t, msgsBare), mustJSON(t, msgsBareDropped))
	}
	if mustJSON(t, msgsBare[0]) != mustJSON(t, msgsBareDropped[0]) {
		t.Errorf("the first content message moved with the dropped-part set\n bare: %s\ndropped: %s",
			mustJSON(t, msgsBare[0]), mustJSON(t, msgsBareDropped[0]))
	}
	if len(notesBare) != 0 {
		t.Errorf("a request that drops nothing must carry no notes, got %v", notesBare)
	}
}
