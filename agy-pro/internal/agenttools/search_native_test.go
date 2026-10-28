package agenttools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSearchWebNativeFallbackAndKeyOnly(t *testing.T) {
	calls := 0
	source := SearchSource{
		KeyEnv: "KEY_WITHOUT_ENDPOINT",
		NativeSearch: func(query string) (string, error) {
			calls++
			if query != "native query" {
				t.Fatalf("native query = %q", query)
			}
			return "- finding https://native.example/1", nil
		},
	}
	out, err := SearchWeb("native query", source)
	if err != nil || calls != 1 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
	// F-10A: the native answer arrives inside the untrusted-content fence, with
	// the preamble naming the block; the answer bytes themselves survive.
	want := nativeSearchPreamble + nativeSearchFenceOpen + "\n- finding https://native.example/1\n" + nativeSearchFenceClose
	if out != want {
		t.Fatalf("out=%q; want the fenced form %q", out, want)
	}
	if !strings.Contains(out, "- finding https://native.example/1") {
		t.Errorf("the native answer must survive inside the fence: %q", out)
	}
}

// F-10A clamp half: a native answer used to enter the seat at up to the 4 MiB
// stdout cap while an endpoint render was held to maxSearchRenderBytes (64 KiB).
// The native branch now enforces the SAME render budget over the WHOLE result,
// fence included, and cuts at a rune boundary with the shared truncation note.
// The 4 MiB stdout cap stays the spawn-side guard (native_seat.go).
func TestNativeSearchAnswerIsClampedToTheRenderBudget(t *testing.T) {
	// A 200 KiB answer: 3x the render budget, far below the 4 MiB spawn cap.
	answer := strings.Repeat("A", 200*1024)
	source := SearchSource{NativeSearch: func(string) (string, error) { return answer, nil }}
	out, err := SearchWeb("q", source)
	if err != nil {
		t.Fatalf("SearchWeb: %v", err)
	}
	if len(out) > maxSearchRenderBytes {
		t.Fatalf("native tool result = %d bytes; the render budget is %d", len(out), maxSearchRenderBytes)
	}
	if !strings.Contains(out, nativeSearchFenceOpen) || !strings.HasSuffix(out, nativeSearchFenceClose) {
		t.Fatalf("fence lost on the over-budget path: head=%q tail=%q", out[:80], out[len(out)-40:])
	}
	if !strings.Contains(out, "... (render truncated at 64 KiB)") {
		t.Errorf("an over-budget native answer must carry the truncation note")
	}
	// The payload is cut at a rune boundary, not mid-rune: a multi-byte answer
	// must remain valid UTF-8.
	multi := SearchSource{NativeSearch: func(string) (string, error) {
		return strings.Repeat("가", 40*1024), nil // 3 bytes per rune
	}}
	mout, err := SearchWeb("q", multi)
	if err != nil {
		t.Fatalf("SearchWeb: %v", err)
	}
	if len(mout) > maxSearchRenderBytes {
		t.Fatalf("multi-byte native result = %d bytes; want <= %d", len(mout), maxSearchRenderBytes)
	}
	if !utf8.ValidString(mout) {
		t.Errorf("the clamped native result is not valid UTF-8 (mid-rune cut)")
	}
}

// F-10A separation pin: the fence is a NATIVE-branch control only. An endpoint
// render keeps its byte-for-byte pre-fix shape (no markers, no preamble), so the
// endpoint and read_url_content paths are unchanged by this fix.
func TestEndpointRenderIsUnchangedByTheNativeFence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchJSONBody)
	}))
	t.Cleanup(srv.Close)
	source := SearchSource{
		Endpoint:     srv.URL + "/s?q={query}",
		NativeSearch: func(string) (string, error) { t.Fatal("native fallback ran for an explicit endpoint"); return "", nil },
	}
	out, err := SearchWeb("doc", source)
	if err != nil {
		t.Fatalf("SearchWeb: %v", err)
	}
	for _, marker := range []string{"<untrusted-web-content", nativeSearchPreamble} {
		if strings.Contains(out, marker) {
			t.Errorf("the endpoint render gained the native fence %q:\n%s", marker, out)
		}
	}
	// The renderer's own shape is untouched: numbered item, url line, snippet.
	if !strings.HasPrefix(out, "1. Doc One\n") || !strings.Contains(out, "\n   https://a.example/1\n") {
		t.Errorf("endpoint render shape changed:\n%s", out)
	}

	// The read path is untouched by this fix: read_url_content returns the raw
	// body with no fence and no preamble (fetch.go carries no diff).
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "raw page body with <angle> brackets")
	}))
	t.Cleanup(pageSrv.Close)
	pageURL, err := url.Parse(pageSrv.URL)
	if err != nil {
		t.Fatalf("parse stub url: %v", err)
	}
	body, err := ReadURLContent(pageSrv.URL, []string{pageURL.Hostname()})
	if err != nil {
		t.Fatalf("ReadURLContent: %v", err)
	}
	if !strings.Contains(body, "raw page body with <angle> brackets") {
		t.Errorf("read_url_content body changed: %q", body)
	}
	for _, marker := range []string{"<untrusted-web-content", nativeSearchPreamble} {
		if strings.Contains(body, marker) {
			t.Errorf("read_url_content gained the native fence %q: %q", marker, body)
		}
	}
}

func TestSearchWebExplicitEndpointPreemptsNative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searchJSONBody)
	}))
	t.Cleanup(srv.Close)
	source := SearchSource{
		Endpoint: srv.URL + "/s?q={query}",
		NativeSearch: func(string) (string, error) {
			t.Fatal("native fallback ran despite an explicit endpoint")
			return "", nil
		},
	}
	out, err := SearchWeb("explicit", source)
	if err != nil || !strings.Contains(out, "Doc One") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestToolRunnerNativeFailureIsToolResult(t *testing.T) {
	runner := NewRunner(t.TempDir(), false, false, nil)
	runner.SearchSource = SearchSource{NativeSearch: func(string) (string, error) {
		return "", fmt.Errorf("stub failed twice")
	}}
	out, err := runner.Execute(1, "m", "p", "search_web", `{"query":"q"}`)
	if err != nil {
		t.Fatalf("seat-level Go error = %v; want an error-shaped tool result", err)
	}
	if !strings.Contains(out, "native agy fallback failed") || !strings.Contains(out, "stub failed twice") {
		t.Fatalf("tool result = %q", out)
	}
}
