package openaiup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agypro/internal/providers"
)

// MaxBodyBytes caps an upstream response body (32 MB). It is the same ceiling
// the bridge applies to its inbound request body, so a runaway or hostile
// provider cannot exhaust memory on either side.
const MaxBodyBytes = 32 << 20 // 32 MB

// errorSnippetBytes caps how much of a non-200 body is carried into the error
// string. The snippet is the only raw upstream text that ever reaches a log or
// audit sink, and it goes through a redactor first.
const errorSnippetBytes = 2048

// errorSnippetOverlapBytes is how far past the snippet cap the body is read
// before redaction. Redacting before cutting is what keeps a credential the
// provider positioned across the cut from leaking its unshaped fragment, and
// that only works if the whole secret is inside the text handed to the
// redactor. 16 KiB covers every shape the redactor masks (bearer tokens, sk-
// tokens, Google keys, AWS material, PEM blocks) plus any realistic registered
// key value.
const errorSnippetOverlapBytes = 16 << 10

// cappedReader bounds how much of an upstream body a caller may pull, and
// reports whether the body outgrew the bound. Reading one byte past the cap is
// the discriminator: io.LimitReader alone answers a clean EOF at the cap, which
// would let a truncated stream look like a successful turn.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// exceeded reports whether the underlying body was larger than the cap. The
// reader is allowed MaxBodyBytes+1 bytes, so a body of exactly MaxBodyBytes
// never trips this and a body past it always does.
func (c *cappedReader) exceeded() bool { return c.left <= 0 }

// ErrStreamTruncated is returned when an upstream SSE response ended WITHOUT a
// terminal signal: no chunk carried a finish_reason and no [DONE] sentinel
// arrived. The folded turn is a truncated answer, so presenting it as a
// complete one would be a fake success. Callers map it onto their own error
// surface (the wire adapter answers its §9.1 UNAVAILABLE row).
var ErrStreamTruncated = errors.New("upstream stream ended before a terminal frame")

// Call performs one upstream chat.completions turn against target and returns
// the aggregated result.
//
// Transport discipline, identical for every caller:
//   - POST target.Provider.Endpoint() with Content-Type: application/json,
//   - Authorization: Bearer <key> only when the resolved key is non-empty. The
//     key is read per call from its environment-variable name and is never
//     logged,
//   - no client-level timeout: the caller's context carries the deadline,
//   - a non-200 status becomes an error carrying a redacted snippet of the body
//     (io.LimitReader, never the whole body),
//   - text/event-stream responses are folded with MergeSSE, everything else
//     with MergeJSON; both are capped at MaxBodyBytes and a stream that
//     outgrows the cap fails loudly instead of returning a truncated turn,
//   - an SSE stream that ends without a terminal signal fails with
//     ErrStreamTruncated instead of returning a truncated turn that looks
//     complete.
func Call(ctx context.Context, client *http.Client, target providers.Target, body []byte, redact func(string) string) (*Turn, error) {
	if client == nil {
		client = &http.Client{}
	}
	if redact == nil {
		// Fail closed: the upstream error snippet is the one place
		// provider-controlled text reaches a log or audit sink, so a caller
		// that passes no redactor still gets the generic masks (bearer, sk-,
		// AWS, auth-header and PEM shapes).
		redact = providers.NewRedactor()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.Provider.Endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(target.Provider.KeyValue()); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read past the snippet cap so a secret straddling the cut is whole
		// when it reaches the redactor, then redact, then cut.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorSnippetBytes+errorSnippetOverlapBytes))
		snippet := redact(string(raw))
		if len(snippet) > errorSnippetBytes {
			snippet = snippet[:errorSnippetBytes]
		}
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, snippet)
	}

	turn := NewTurn()
	capped := &cappedReader{r: resp.Body, left: MaxBodyBytes + 1}
	eventStream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if eventStream {
		if err := turn.MergeSSE(capped); err != nil {
			return nil, err
		}
	} else if err := turn.MergeJSON(capped); err != nil {
		return nil, err
	}
	if capped.exceeded() {
		return nil, fmt.Errorf("upstream stream exceeded %d byte cap", MaxBodyBytes)
	}
	// An SSE stream that ended with no terminal signal was cut short, so the
	// folded turn is a truncated answer and must not come back as a complete
	// one. The cap check runs first: a body that outgrew the cap is a cap
	// failure, whatever else it did.
	if eventStream && !turn.StreamTerminal {
		return nil, ErrStreamTruncated
	}
	return turn, nil
}

// NewCompletionID returns a fresh OpenAI-style completion id ("chatcmpl-"
// followed by 24 hex characters). It falls back to a timestamp only when the
// system random source is unavailable.
func NewCompletionID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return "chatcmpl-" + hex.EncodeToString(buf)
}
