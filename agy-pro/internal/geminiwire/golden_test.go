package geminiwire

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"agypro/internal/openaiup"
	"agypro/internal/providers"
)

// Golden fixtures for the Gemini-wire contract (spec §11.1, gate G1). Every GF
// row pins the codec's output BYTE-FOR-BYTE: the inbound file is the captured
// or spec-shaped input, the expected file is the exact bytes the codec entry
// point must produce. Regenerate with -update (or GOLDEN_UPDATE=1) only when a
// deliberate wire change lands; the committed bytes are the contract.

var goldenUpdate = flag.Bool("update", false, "regenerate golden expected files instead of verifying them")

const goldenDir = "golden"

// gfKind selects which codec entry point a fixture row exercises.
type gfKind int

const (
	kindRequest       gfKind = iota // Gemini request -> OpenAI body fragment (§7.1-§7.6)
	kindStream                      // provider SSE -> Gemini SSE (§8)
	kindStreamApology               // provider 200 with zero content -> apology + STOP (§9.2)
	kindStreamError                 // mid-stream disconnect -> frames + error event (§9.3)
	kindTitle                       // title-generator request -> local title stream (§5.8)
	kindResponse                    // provider non-stream body -> Gemini response (§7.7-§7.9)
	kindEnvelope                    // /v1internal unwrap + rename + re-wrap (Appendix A)
	kindModels                      // registry table -> ListModelsResponse (§5.7)
	kindError                       // provider status+body -> Google error envelope (§9.1)
	kindRouteError                  // unknown model route -> 404 NOT_FOUND (§3.3, §9.1)
)

// gfRow is one fixture row: the inbound file plus the entry-point config the
// codec needs (vision gate, capabilities, registry cap, apology cause).
type gfRow struct {
	id       string
	kind     gfKind
	vision   bool
	caps     providers.Capabilities
	regMax   int
	multi    bool // inbound is {"cases":[...]} and expected is an array
	thoughts bool // includeThoughts: may the caller surface reasoning as thought parts
	apology  string
	cause    string // §9.2 apology cause the adapter appends for an empty turn
}

// gfRows is the §11.1 table, in spec order. File names derive from the id.
var gfRows = []gfRow{
	{id: "gf-01", kind: kindRequest},
	{id: "gf-02", kind: kindRequest},
	{id: "gf-03", kind: kindRequest},
	{id: "gf-04", kind: kindRequest},
	{id: "gf-05", kind: kindRequest},
	{id: "gf-06", kind: kindRequest},
	{id: "gf-07", kind: kindRequest, vision: true},
	{id: "gf-08", kind: kindRequest},
	{id: "gf-09", kind: kindRequest, vision: true},
	{id: "gf-10", kind: kindRequest},
	{id: "gf-11", kind: kindRequest},
	{id: "gf-12", kind: kindRequest},
	{id: "gf-13", kind: kindRequest},
	{id: "gf-14", kind: kindRequest},
	{id: "gf-15", kind: kindRequest},
	{id: "gf-13b", kind: kindRequest},
	{id: "gf-16", kind: kindRequest},
	{id: "gf-17", kind: kindRequest},
	{id: "gf-18", kind: kindRequest},
	{id: "gf-19", kind: kindRequest, multi: true, regMax: 8192},
	{id: "gf-20", kind: kindRequest, multi: true, caps: providers.Capabilities{SupportsThinking: true}},
	{id: "gf-21", kind: kindRequest},
	{id: "gf-22", kind: kindStream, caps: providers.Capabilities{SupportsThinking: true}, thoughts: true},
	{id: "gf-23", kind: kindStream, thoughts: true},
	{id: "gf-24", kind: kindResponse, multi: true},
	{id: "gf-25", kind: kindResponse, multi: true},
	{id: "gf-26", kind: kindError},
	{id: "gf-27", kind: kindStreamApology, apology: "I'm sorry, but I could not finish this turn: provider mock-provider: upstream error (HTTP 502)",
		cause: "provider mock-provider: upstream error (HTTP 502)"},
	{id: "gf-28", kind: kindStreamError, apology: "unused: the error frame carries the cause"},
	{id: "gf-29", kind: kindTitle},
	{id: "gf-30", kind: kindModels},
	{id: "gf-31", kind: kindEnvelope},
	{id: "gf-32", kind: kindRouteError},
	// Round-2b additions.
	{id: "gf-33", kind: kindResponse, multi: true},
	{id: "gf-34", kind: kindRequest},
	{id: "gf-35", kind: kindRequest},
}

// ---------------------------------------------------------------------------
// §9.1 error map, encoded as data so GF-26/GF-28/GF-32 and PT-5 share one table
// ---------------------------------------------------------------------------

// googleError is the Google API error envelope (§9.1).
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// errorMapRow is one §9.1 mapping row. Code doubles as the HTTP status the
// adapter answers with (the spec's "HTTP status" column).
type errorMapRow struct {
	code    int
	status  string
	message string
}

// errorMap mirrors the §9.1 table. The provider name is the mock-provider
// stand-in used across the fixtures; <id> stands in for the requested model.
var errorMap = map[string]errorMapRow{
	"provider-401":                 {401, "UNAUTHENTICATED", "provider mock-provider: authentication failed"},
	"provider-403":                 {403, "PERMISSION_DENIED", "provider mock-provider: permission denied"},
	"provider-404":                 {404, "NOT_FOUND", "provider mock-provider: model probe-gemini not found"},
	"provider-429":                 {429, "RESOURCE_EXHAUSTED", "provider mock-provider: rate limited (retry later)"},
	"provider-500":                 {502, "UNAVAILABLE", "provider mock-provider: upstream error (HTTP 500)"},
	"upstream-timeout":             {504, "DEADLINE_EXCEEDED", "provider mock-provider: timed out"},
	"transport-failure":            {503, "UNAVAILABLE", "provider mock-provider: transport failure"},
	"malformed-body":               {502, "UNAVAILABLE", "provider mock-provider: unparseable response"},
	"provider-400-response-format": {400, "INVALID_ARGUMENT", "provider mock-provider: rejected request (unsupported parameter: response_format)"},
}

func errorEnvelope(row errorMapRow) []byte {
	var e googleError
	e.Error.Code = row.code
	e.Error.Message = row.message
	e.Error.Status = row.status
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return b
}

func errorFrame(row errorMapRow) string {
	return "data: " + string(errorEnvelope(row)) + "\n\n"
}

// ---------------------------------------------------------------------------
// fixture IO
// ---------------------------------------------------------------------------

func goldenFile(id, suffix string) string { return filepath.Join(goldenDir, id+"-"+suffix) }

func readGolden(t *testing.T, id, suffix string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenFile(id, suffix))
	if err != nil {
		t.Fatalf("%s: %v (run with -update to generate)", goldenFile(id, suffix), err)
	}
	return b
}

// checkGolden compares got against the pinned expected file byte-for-byte, or
// regenerates the file under -update / GOLDEN_UPDATE=1.
func checkGolden(t *testing.T, id, suffix string, got []byte) {
	t.Helper()
	path := goldenFile(id, suffix)
	if *goldenUpdate || os.Getenv("GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want := readGolden(t, id, suffix)
	if !bytes.Equal(want, got) {
		t.Errorf("%s: golden mismatch\n got: %q\nwant: %q", path, got, want)
	}
}

// requestCases returns the raw Gemini request JSONs a row carries: one, or the
// cases array of a multi-case row.
func requestCases(t *testing.T, row gfRow) [][]byte {
	t.Helper()
	body := readGolden(t, row.id, "request.json")
	if !row.multi {
		return [][]byte{body}
	}
	var wrap struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatalf("%s: cases wrapper: %v", row.id, err)
	}
	if len(wrap.Cases) == 0 {
		t.Fatalf("%s: cases wrapper carries no cases", row.id)
	}
	out := make([][]byte, len(wrap.Cases))
	for i, c := range wrap.Cases {
		out[i] = c
	}
	return out
}

// ---------------------------------------------------------------------------
// per-kind runners: each returns the exact expected bytes for one case
// ---------------------------------------------------------------------------

// runRequest translates one Gemini request through the request direction and
// assembles the OpenAI body fragment the adapter would send: messages, tools,
// tool_choice, params, the surface-thoughts gate and the audit notes.
func runRequest(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	req, err := DecodeGenerateRequest(body)
	if err != nil {
		t.Fatalf("%s: DecodeGenerateRequest: %v", row.id, err)
	}
	msgs, mnotes := ToOpenAIMessages(req, row.vision)
	tools, choice, tnotes := ToOpenAITools(req)
	params, thoughts, pnotes := ToOpenAIParams(req, row.caps, row.regMax)
	notes := append(append(mnotes, tnotes...), pnotes...)
	frag := map[string]any{
		"messages":         msgs,
		"notes":            notes,
		"params":           params,
		"surface_thoughts": thoughts,
		"tool_choice":      choice,
		"tools":            tools,
	}
	out, err := json.Marshal(frag)
	if err != nil {
		t.Fatalf("%s: marshal body fragment: %v", row.id, err)
	}
	return append(out, '\n')
}

// mergeStream folds a provider SSE stream into a Turn. The Turn records the
// provider's last finish_reason and choice index while merging (openaiup), so
// the runner reads turn.FinishReason instead of hand-scanning the stream.
func mergeStream(t *testing.T, row gfRow, sse []byte) *openaiup.Turn {
	t.Helper()
	turn := openaiup.NewTurn()
	if err := turn.MergeSSE(bytes.NewReader(sse)); err != nil {
		t.Fatalf("%s: MergeSSE: %v", row.id, err)
	}
	return turn
}

// streamResponse folds a provider SSE stream into a Turn and maps it back. The
// §9.2 apology is the codec's own: TurnToGenerateResponse synthesizes it when
// the turn produced nothing user-visible, so this runner models the production
// path exactly (it passes the adapter's cause string and nothing else).
func streamResponse(t *testing.T, row gfRow, sse []byte) *GenerateContentResponse {
	t.Helper()
	turn := mergeStream(t, row, sse)
	resp, _ := TurnToGenerateResponseWithCause(turn, row.thoughts, turn.FinishReason, row.cause)
	return resp
}

// runStream folds a provider SSE stream into a Turn, maps it back and frames it
// as Gemini SSE (§8.4-§8.6).
func runStream(t *testing.T, row gfRow, sse []byte) []byte {
	t.Helper()
	return []byte(strings.Join(StreamFrames(streamResponse(t, row, sse), row.apology), ""))
}

// runStreamError models §9.3: an upstream failure after text was streamed
// closes with an error event, never a fake success terminator. The adapter
// emits the mapped part chunks, drops the synthesized final chunk and appends
// the error frame.
func runStreamError(t *testing.T, row gfRow, sse []byte) []byte {
	t.Helper()
	frames := StreamFrames(streamResponse(t, row, sse), row.apology)
	if len(frames) == 0 {
		t.Fatalf("%s: no frames to close", row.id)
	}
	var b strings.Builder
	for _, f := range frames[:len(frames)-1] {
		b.WriteString(f)
	}
	b.WriteString(errorFrame(errorMap["transport-failure"]))
	return []byte(b.String())
}

// titleResponse models the §5.8 local title handler: extract the first
// non-empty user text part, strip control characters, truncate to 60 runes,
// emit one text part with STOP and a minimal usageMetadata. Zero provider
// calls.
func titleResponse(t *testing.T, row gfRow, body []byte) *GenerateContentResponse {
	t.Helper()
	req, err := DecodeGenerateRequest(body)
	if err != nil {
		t.Fatalf("%s: DecodeGenerateRequest: %v", row.id, err)
	}
	if len(req.Tools) != 0 {
		t.Errorf("%s: a title-generator request must carry no tools", row.id)
	}
	title := localTitle(req)
	turn := openaiup.NewTurn()
	turn.SetContent(title)
	n := utf8.RuneCountInString(title)
	turn.Usage = map[string]any{"prompt_tokens": 0, "completion_tokens": n, "total_tokens": n}
	resp, _ := TurnToGenerateResponse(turn, false, "stop")
	return resp
}

func runTitle(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	return []byte(strings.Join(StreamFrames(titleResponse(t, row, body), ""), ""))
}

func localTitle(req *GenerateContentRequest) string {
	text := ""
	for ci := range req.Contents {
		for pi := range req.Contents[ci].Parts {
			if p := &req.Contents[ci].Parts[pi]; p.Text != "" {
				text = p.Text
				break
			}
		}
		if text != "" {
			break
		}
	}
	var b strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	runes := []rune(b.String())
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

// providerResponse maps one provider non-stream body onto the Gemini response.
func providerResponse(t *testing.T, row gfRow, body []byte) *GenerateContentResponse {
	t.Helper()
	var chunk openaiup.Chunk
	if err := json.Unmarshal(body, &chunk); err != nil {
		t.Fatalf("%s: decode provider body: %v", row.id, err)
	}
	turn := openaiup.NewTurn()
	turn.MergeChunk(&chunk)
	resp, _ := TurnToGenerateResponseWithCause(turn, row.thoughts, turn.FinishReason, row.cause)
	return resp
}

func runResponse(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	out, err := json.Marshal(providerResponse(t, row, body))
	if err != nil {
		t.Fatalf("%s: marshal response: %v", row.id, err)
	}
	return append(out, '\n')
}

// runEnvelope unwraps the /v1internal envelope (with the responseSchema
// rename) and re-wraps a response, pinning Appendix A end to end.
func runEnvelope(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	var inbound struct {
		Route    string          `json:"route"`
		Envelope json.RawMessage `json:"envelope"`
	}
	if err := json.Unmarshal(body, &inbound); err != nil {
		t.Fatalf("%s: decode inbound: %v", row.id, err)
	}
	verb, err := ParseInternalRoute(inbound.Route)
	if err != nil {
		t.Fatalf("%s: ParseInternalRoute(%q): %v", row.id, inbound.Route, err)
	}
	env, inner, err := UnwrapV1Internal(inbound.Envelope)
	if err != nil {
		t.Fatalf("%s: UnwrapV1Internal: %v", row.id, err)
	}
	innerBytes, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("%s: marshal inner: %v", row.id, err)
	}
	resp, _ := TurnToGenerateResponse(openaiup.NewTurn(), false, "stop")
	wrapped, err := WrapV1InternalResponse(resp)
	if err != nil {
		t.Fatalf("%s: WrapV1InternalResponse: %v", row.id, err)
	}
	out, err := json.Marshal(map[string]any{
		"inner":     json.RawMessage(innerBytes),
		"model":     env.Model,
		"rewrapped": json.RawMessage(wrapped),
		"verb":      verb,
	})
	if err != nil {
		t.Fatalf("%s: marshal envelope row: %v", row.id, err)
	}
	return append(out, '\n')
}

// runModels renders the registry listing shape (§5.7).
func runModels(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	var inbound struct {
		Models []struct {
			Name             string `json:"name"`
			DisplayName      string `json:"displayName"`
			Description      string `json:"description"`
			InputTokenLimit  int    `json:"inputTokenLimit"`
			OutputTokenLimit int    `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &inbound); err != nil {
		t.Fatalf("%s: decode registry table: %v", row.id, err)
	}
	out := ListModelsResponse{NextPageToken: ""}
	for _, m := range inbound.Models {
		out.Models = append(out.Models, ModelInfo{
			Name:                       m.Name,
			DisplayName:                m.DisplayName,
			Description:                m.Description,
			InputTokenLimit:            m.InputTokenLimit,
			OutputTokenLimit:           m.OutputTokenLimit,
			SupportedGenerationMethods: []string{"generateContent", "streamGenerateContent"},
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("%s: marshal ListModelsResponse: %v", row.id, err)
	}
	return append(b, '\n')
}

// runError maps every §9.1 row the fixture carries onto its Google error
// envelope. The mapper itself lives in wireadp (U5); this row pins the exact
// envelope bytes the §9.1 table demands so U5 has a byte contract to hit.
func runError(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	var inbound struct {
		Provider string `json:"provider"`
		Rows     []struct {
			Condition      string `json:"condition"`
			ProviderStatus int    `json:"provider_status"`
			Body           string `json:"body"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &inbound); err != nil {
		t.Fatalf("%s: decode error rows: %v", row.id, err)
	}
	if inbound.Provider == "" {
		t.Errorf("%s: fixture must name the provider stand-in", row.id)
	}
	var envelopes []json.RawMessage
	for _, r := range inbound.Rows {
		m, ok := errorMap[r.Condition]
		if !ok {
			t.Fatalf("%s: condition %q has no §9.1 row", row.id, r.Condition)
		}
		env := errorEnvelope(m)
		// §9.1 redaction: the upstream body is never echoed into the message.
		if r.Body != "" && bytes.Contains(env, []byte(r.Body)) {
			t.Errorf("%s: condition %q leaks the upstream body into the message: %s", row.id, r.Condition, env)
		}
		if !bytes.Contains(env, []byte("provider "+inbound.Provider+":")) {
			t.Errorf("%s: condition %q message must name the provider: %s", row.id, r.Condition, env)
		}
		envelopes = append(envelopes, json.RawMessage(env))
	}
	out, err := json.Marshal(envelopes)
	if err != nil {
		t.Fatalf("%s: marshal error envelopes: %v", row.id, err)
	}
	return append(out, '\n')
}

// runRouteError pins the unknown-model 404: the route parses verbatim, the
// allow-list misses, and the §9.1 NOT_FOUND envelope answers.
func runRouteError(t *testing.T, row gfRow, body []byte) []byte {
	t.Helper()
	var inbound struct {
		Route          string   `json:"route"`
		RegistryModels []string `json:"registry_models"`
	}
	if err := json.Unmarshal(body, &inbound); err != nil {
		t.Fatalf("%s: decode route row: %v", row.id, err)
	}
	model, verb, err := ParseGenerateRoute(inbound.Route)
	if err != nil {
		t.Fatalf("%s: ParseGenerateRoute(%q): %v", row.id, inbound.Route, err)
	}
	for _, served := range inbound.RegistryModels {
		if served == model || served == "models/"+model {
			t.Errorf("%s: fixture must name an unknown model, %q is served", row.id, model)
		}
	}
	out, err := json.Marshal(map[string]any{
		"error": json.RawMessage(errorEnvelope(errorMapRow{
			code:    404,
			status:  "NOT_FOUND",
			message: fmt.Sprintf("model %s is not served by this adapter", model),
		})),
		"model": model,
		"verb":  verb,
	})
	if err != nil {
		t.Fatalf("%s: marshal route-error row: %v", row.id, err)
	}
	return append(out, '\n')
}

// ---------------------------------------------------------------------------
// the golden table test
// ---------------------------------------------------------------------------

func TestGoldenFixtures(t *testing.T) {
	for _, row := range gfRows {
		t.Run(row.id, func(t *testing.T) {
			switch row.kind {
			case kindRequest:
				cases := requestCases(t, row)
				if !row.multi {
					checkGolden(t, row.id, "expected.json", runRequest(t, row, cases[0]))
					return
				}
				var frags []json.RawMessage
				for _, c := range cases {
					frags = append(frags, json.RawMessage(bytes.TrimRight(runRequest(t, row, c), "\n")))
				}
				out, err := json.Marshal(frags)
				if err != nil {
					t.Fatalf("%s: marshal cases: %v", row.id, err)
				}
				checkGolden(t, row.id, "expected.json", append(out, '\n'))

			case kindStream, kindStreamApology, kindStreamError, kindTitle:
				suffix := "request.sse"
				if row.kind == kindTitle {
					suffix = "request.json"
				}
				body := readGolden(t, row.id, suffix)
				var got []byte
				switch row.kind {
				case kindStream, kindStreamApology:
					got = runStream(t, row, body)
				case kindStreamError:
					got = runStreamError(t, row, body)
				case kindTitle:
					got = runTitle(t, row, body)
				}
				checkGolden(t, row.id, "expected.sse", got)

			case kindResponse:
				cases := requestCases(t, row)
				var resps []json.RawMessage
				for _, c := range cases {
					resps = append(resps, json.RawMessage(bytes.TrimRight(runResponse(t, row, c), "\n")))
				}
				out, err := json.Marshal(resps)
				if err != nil {
					t.Fatalf("%s: marshal cases: %v", row.id, err)
				}
				checkGolden(t, row.id, "expected.json", append(out, '\n'))

			case kindEnvelope:
				checkGolden(t, row.id, "expected.json", runEnvelope(t, row, readGolden(t, row.id, "request.json")))
			case kindModels:
				checkGolden(t, row.id, "expected.json", runModels(t, row, readGolden(t, row.id, "request.json")))
			case kindError:
				checkGolden(t, row.id, "expected.json", runError(t, row, readGolden(t, row.id, "request.json")))
			case kindRouteError:
				checkGolden(t, row.id, "expected.json", runRouteError(t, row, readGolden(t, row.id, "request.json")))
			}
		})
	}
}

// goldenBans are the key shapes no fixture file may carry, inbound or pinned, so
// a capture or a regeneration can never pin a credential into the repo.
var goldenBans = []string{
	"sk-",
	"AIza",
	"Bearer ",
	"-----BEGIN",
	"x-goog-api-key",
	"x-api-key",
	"Authorization:",
	"AKIA",
	"ASIA",
	"ghp_",
}

// sentinelBan is the [DONE] sentinel toward agy (§8 rule 3). It is banned in
// pinned expected files only: the inbound provider SSE fixtures legitimately
// carry it, because that is what the adapter consumes.
const sentinelBan = "[DONE]"

// opaqueHex matches a 32-to-64 character lowercase hex run (an API secret or a
// hash), the shape a generic literal list misses.
var opaqueHex = regexp.MustCompile(`\b[0-9a-f]{32,64}\b`)

// goldenViolations returns every ban one fixture file trips. inbound marks a
// request.* file, which may carry the provider's [DONE] sentinel.
func goldenViolations(b []byte, inbound bool) []string {
	var out []string
	if !inbound && bytes.Contains(b, []byte(sentinelBan)) {
		out = append(out, "banned literal "+strconv.Quote(sentinelBan))
	}
	for _, bad := range goldenBans {
		if bytes.Contains(b, []byte(bad)) {
			out = append(out, "banned literal "+strconv.Quote(bad))
		}
	}
	if opaqueHex.Match(b) {
		out = append(out, "opaque hex run: "+opaqueHex.FindString(string(b)))
	}
	if !utf8.Valid(b) {
		out = append(out, "invalid UTF-8")
	}
	return out
}

// TestGoldenFixtures_NoSentinelAndNoSecrets sweeps every file under golden/ —
// inbound request.* files included, not only the pinned expected.* — for the
// wire-wide bans (DoD 3, §8 rule 3). The manifest is skipped because its prose
// legitimately names the banned literals.
func TestGoldenFixtures_NoSentinelAndNoSecrets(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(goldenDir, "*"))
	if err != nil {
		t.Fatalf("glob %s: %v", goldenDir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no fixture files found under %s", goldenDir)
	}
	swept := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if filepath.Base(path) == "manifest.md" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		swept++
		for _, v := range goldenViolations(b, strings.Contains(path, "-request.")) {
			t.Errorf("%s: %s", path, v)
		}
	}
	if swept < 2*len(gfRows) {
		t.Errorf("swept %d files, want at least the %d fixture pairs", swept, len(gfRows))
	}
}

// TestGoldenFixtures_SweepCatchesPlantedSecrets proves the sweep above is live:
// each planted literal must be reported, so a future regeneration that pins a
// credential fails loudly instead of silently.
func TestGoldenFixtures_SweepCatchesPlantedSecrets(t *testing.T) {
	dir := t.TempDir()
	plants := map[string]struct {
		body    string
		inbound bool
		want    string
	}{
		"opaque-hex.json": {"9f2c7a1b4e5d6f70819a2b3c4d5e6f70", false, "opaque hex run"},
		"sk-shaped.json":  {"sk-plantedupstreamsecret0123456789", false, "sk-"},
		"goog-key.json":   {"AIzaPlantedGoogleKeyShape0123456789", false, "AIza"},
		"sentinel.sse":    {"data: [DONE]\n\n", false, "[DONE]"},
		"aws-key.json":    {"AKIAPLANTEDKEYSHAPE0000", false, "AKIA"},
		"github-pat.json": {"ghp_plantedtokenshape0123456789abcd", false, "ghp_"},
		// An inbound provider stream legitimately carries the sentinel, so the
		// ban must not fire there; the key shapes still must.
		"inbound.sse": {"data: [DONE]\n\n", true, ""},
	}
	for name, plant := range plants {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(plant.body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		violations := goldenViolations(b, plant.inbound)
		if plant.want == "" {
			if len(violations) != 0 {
				t.Errorf("%s: expected no violation, got %v", name, violations)
			}
			continue
		}
		found := false
		for _, v := range violations {
			if strings.Contains(v, plant.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: sweep missed %q, violations = %v", name, plant.want, violations)
		}
	}
	// A clean file must produce no violation at all.
	clean := filepath.Join(dir, "clean.json")
	if err := os.WriteFile(clean, []byte(`{"parts":[{"text":"<stand-in>"}]}`), 0o644); err != nil {
		t.Fatalf("write %s: %v", clean, err)
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		t.Fatalf("read %s: %v", clean, err)
	}
	if v := goldenViolations(b, false); len(v) != 0 {
		t.Errorf("a clean fixture must not trip the sweep: %v", v)
	}
}
