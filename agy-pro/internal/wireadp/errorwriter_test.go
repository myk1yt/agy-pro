package wireadp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newErrorTestServer builds a Server without a listener, for exercising the
// error writer against an httptest recorder.
func newErrorTestServer(redact func(string) string) *Server {
	return &Server{redact: redact, cfg: Config{Redact: redact}}
}

func TestWriteGoogleError_EnvelopeShape(t *testing.T) {
	srv := newErrorTestServer(nil)
	rec := httptest.NewRecorder()
	srv.writeGoogleError(rec, http.StatusNotFound, statusNotFound, "model probe-x is not served by this adapter")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	want := `{"error":{"code":404,"message":"model probe-x is not served by this adapter","status":"NOT_FOUND"}}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

// TestWriteGoogleError_GF26Golden compares every §9.1 provider row against the
// pinned golden bytes. The fixture's condition keys are the table's keys; the
// cause for the repair-retry row is the extracted one-line cause (upstream body
// parsing is U5b's job).
func TestWriteGoogleError_GF26Golden(t *testing.T) {
	srv := newErrorTestServer(nil)
	request := readGolden(t, "gf-26-request.json")
	var inbound struct {
		Provider string `json:"provider"`
		Rows     []struct {
			Condition      string `json:"condition"`
			ProviderStatus int    `json:"provider_status"`
			Body           string `json:"body"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(request, &inbound); err != nil {
		t.Fatalf("decode GF-26 request: %v", err)
	}
	var expected []json.RawMessage
	if err := json.Unmarshal(readGolden(t, "gf-26-expected.json"), &expected); err != nil {
		t.Fatalf("decode GF-26 expected: %v", err)
	}
	if len(expected) != len(inbound.Rows) {
		t.Fatalf("fixture rows = %d, expected envelopes = %d", len(inbound.Rows), len(expected))
	}
	for i, row := range inbound.Rows {
		cause := ""
		switch {
		case row.Condition == "provider-400-response-format":
			cause = "unsupported parameter: response_format"
		case row.Condition == "provider-404":
			// §9.1: "provider <name>: model <id> not found" - the id is the
			// requested model.
			cause = "probe-gemini"
		case row.ProviderStatus >= 500:
			cause = strconv.Itoa(row.ProviderStatus)
		}
		code, status, message, ok := lookupProviderError(row.Condition, inbound.Provider, cause)
		if !ok {
			t.Fatalf("condition %q has no §9.1 row", row.Condition)
		}
		rec := httptest.NewRecorder()
		srv.writeGoogleError(rec, code, status, message)
		got := bytes.TrimSpace(rec.Body.Bytes())
		if !bytes.Equal(got, bytes.TrimSpace(expected[i])) {
			t.Fatalf("row %q:\n got %s\nwant %s", row.Condition, got, expected[i])
		}
		if row.Body != "" && bytes.Contains(got, []byte(row.Body)) {
			t.Fatalf("row %q leaks the upstream body: %s", row.Condition, got)
		}
	}
}

func TestWriteGoogleError_GF32Golden(t *testing.T) {
	srv := newErrorTestServer(nil)
	// The GF-32 fixture pins the unknown-model envelope bytes: the golden
	// file nests the full envelope under its own "error" key.
	var pinned struct {
		Error json.RawMessage `json:"error"`
		Model string          `json:"model"`
		Verb  string          `json:"verb"`
	}
	if err := json.Unmarshal(readGolden(t, "gf-32-expected.json"), &pinned); err != nil {
		t.Fatalf("decode GF-32 expected: %v", err)
	}
	var want struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(pinned.Error, &want); err != nil {
		t.Fatalf("decode GF-32 envelope: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.writeGoogleError(rec, want.Error.Code, want.Error.Status, want.Error.Message)
	var got struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if got.Error.Code != want.Error.Code || got.Error.Status != want.Error.Status || got.Error.Message != want.Error.Message {
		t.Fatalf("envelope = %+v, want %+v", got.Error, want.Error)
	}
}

func TestSanitizeMessage_Pipeline(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "SEKRET", "[REDACTED]") }

	// Redaction happens before truncation: a secret straddling the cut is
	// still whole when the redactor sees it.
	long := "prefix SEKRET" + strings.Repeat("x", 300) + "SEKRET suffix"
	got := sanitizeMessage(long, redact)
	if strings.Contains(got, "SEKRET") {
		t.Fatalf("secret survived the pipeline: %q", got)
	}
	if n := len([]rune(got)); n > maxMessageRunes {
		t.Fatalf("message length = %d runes, want <= %d", n, maxMessageRunes)
	}

	// Control characters and newlines are stripped: one line, always.
	got = sanitizeMessage("line1\nline2\r\n\ttab\u0007bell", nil)
	if got != "line1line2tabbell" {
		t.Fatalf("control strip = %q", got)
	}

	// A nil redactor is tolerated.
	if got := sanitizeMessage("plain", nil); got != "plain" {
		t.Fatalf("plain message = %q", got)
	}
}

func TestMapProviderStatus_Table(t *testing.T) {
	cases := []struct {
		status     int
		wantCode   int
		wantStatus string
	}{
		{http.StatusBadRequest, 400, statusInvalidArgument},
		{http.StatusUnauthorized, 401, statusUnauthenticated},
		{http.StatusForbidden, 403, statusPermissionDenied},
		{http.StatusNotFound, 404, statusNotFound},
		{http.StatusRequestTimeout, 504, statusDeadlineExceeded},
		{http.StatusConflict, 409, statusAborted},
		{http.StatusRequestEntityTooLarge, 400, statusInvalidArgument},
		{http.StatusTooManyRequests, 429, statusResourceExhausted},
		{http.StatusInternalServerError, 502, statusUnavailable},
		{http.StatusBadGateway, 502, statusUnavailable},
		{402, 502, statusUnavailable}, // unmapped degrades to the 5xx row
	}
	for _, tc := range cases {
		code, status, message := mapProviderStatus("mock-provider", tc.status, "")
		if code != tc.wantCode || status != tc.wantStatus {
			t.Fatalf("status %d -> (%d, %s), want (%d, %s)", tc.status, code, status, tc.wantCode, tc.wantStatus)
		}
		if !strings.Contains(message, "mock-provider") {
			t.Fatalf("status %d message = %q", tc.status, message)
		}
	}
	// The 5xx message carries the upstream code.
	_, _, message := mapProviderStatus("mock-provider", http.StatusInternalServerError, "")
	if message != "provider mock-provider: upstream error (HTTP 500)" {
		t.Fatalf("5xx message = %q", message)
	}
}

// G10F-1: the stream-truncated row is the new §9.1 class. It is the only row
// that answers for an upstream that DID reach 200 and DID produce bytes, so it
// must not be confusable with the transport-failure or malformed-body rows.
func TestStreamTruncatedRow(t *testing.T) {
	code, status, message, ok := lookupProviderError("stream-truncated", "mockp", "")
	if !ok {
		t.Fatal("the stream-truncated condition has no §9.1 row")
	}
	if code != http.StatusServiceUnavailable || status != statusUnavailable {
		t.Fatalf("row = (%d, %s), want (503, UNAVAILABLE)", code, status)
	}
	if want := "provider mockp: upstream stream ended before a terminal frame"; message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
	// The cause argument is ignored: the row names no upstream detail.
	_, _, other, _ := lookupProviderError("stream-truncated", "mockp", "HTTP 200")
	if other != message {
		t.Fatalf("the row must ignore its cause argument: %q vs %q", other, message)
	}
	// And it stays distinct from the two rows it could be confused with.
	for _, condition := range []string{"transport-failure", "malformed-body"} {
		_, _, text, _ := lookupProviderError(condition, "mockp", "")
		if text == message {
			t.Fatalf("%q renders the same message as the truncated row", condition)
		}
	}
}
