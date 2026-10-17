package agenttools

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"agypro/internal/providers"
)

// ToolRunner dispatches tool calls with security boundaries and logging.
type ToolRunner struct {
	Root              string
	AllowWrite        bool
	AllowExec         bool
	Logger            *Logger
	AllowedModelTools []string // optional model-level allowed tools filter
	// Scope is the optional partition-unit working boundary (B3, widened by
	// B4-1). When it is non-empty, the path-reading tools (read_file / list_dir
	// / grep / glob) are refused unless the resolved path matches one of these
	// patterns (PathInScope), the directory walks of list_dir/grep/glob apply
	// the same check to EVERY discovered entry before it is opened or reported
	// (C1), and the write tools (write_file / replace_in_file) are refused on
	// the SAME dual-view rule whenever they are addressed at a path outside
	// the scope (a write unit's runner additionally needs AllowWrite=true, the
	// per-unit opt-in, before a write can proceed at all). nil/empty leaves
	// the dispatch byte-identical to an unscoped runner, so non-partition
	// runs are unaffected.
	Scope []string
	// AllowHosts is the B4-2a host allow-list for read_url_content: hostnames
	// matched exactly against the lowercased URL host (the port is ignored;
	// see fetch.go). The loopback exception covers the http SCHEME only,
	// never this list. nil/empty refuses every read_url_content call (fail
	// closed), and the tool also stays out of the seat's advertised tool
	// list unless a run enabled it (the fanout --allow-host flag plumbs this
	// field into every provider seat, cross-verification and partition
	// alike; native seats never build a runner with it).
	AllowHosts []string
	// SearchSource is the B4-2b search_web source configuration (endpoint
	// template + env-var NAME key reference, see search.go). A zero source
	// (empty endpoint) means the capability is OFF: the tool is not exposed
	// to any seat (the engine gate) and every call returns the honest
	// not-configured error. The key VALUE is read from the environment at
	// call time and never logged; only the env-var NAME may appear in errors
	// and logs. Provider seats receive this from the fanout --search-endpoint
	// / --search-key-env flags (cross-verification and partition alike);
	// native seats never build a runner with it.
	SearchSource SearchSource
}

// NewRunner creates a configured ToolRunner.
func NewRunner(root string, allowWrite, allowExec bool, logger *Logger) *ToolRunner {
	if root == "" {
		root = "."
	}
	return &ToolRunner{
		Root:       root,
		AllowWrite: allowWrite,
		AllowExec:  allowExec,
		Logger:     logger,
	}
}

// Execute parses the tool call JSON arguments, verifies permissions, runs the tool,
// logs the execution, and returns the output string.
func (r *ToolRunner) Execute(seatID int, model, provider, toolName, argsJSON string) (string, error) {
	start := time.Now()
	var out string
	var execErr error

	// 1. Model-level allowed tools check (if specified)
	if len(r.AllowedModelTools) > 0 {
		permitted := false
		for _, t := range r.AllowedModelTools {
			if strings.EqualFold(t, toolName) {
				permitted = true
				break
			}
		}
		if !permitted {
			execErr = fmt.Errorf("tool %q is not in the allowed tools list for model %q", toolName, model)
			// R13: the refusal logs the raw arguments JSON, so the network
			// tools' operands seed the log on this exit path too.
			argsJSON = redactNetToolArgs(toolName, argsJSON, r.SearchSource)
			r.log(seatID, model, provider, toolName, argsJSON, "", execErr, time.Since(start))
			return fmt.Sprintf("Error: %v", execErr), nil
		}
	}

	// 2. Partition-unit scope check (B3/B4-1): a scoped runner refuses a
	// path-reading OR path-writing tool whose resolved path matches none of
	// the unit's scope patterns. The refusal is error-shaped and returns a nil
	// error, exactly like the model-tools refusal above: the model sees it as
	// the tool result and the seat continues. A nil Scope skips this block
	// entirely.
	if refusal, scopeErr := r.scopeRefusal(toolName, argsJSON); scopeErr != nil {
		r.log(seatID, model, provider, toolName, argsJSON, refusal, scopeErr, time.Since(start))
		return refusal, nil
	}

	// 3. Dispatch based on tool name
	switch toolName {
	case "read_file":
		var args struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for read_file: %w", err)
		} else {
			out, execErr = ReadFile(r.Root, args.Path, args.StartLine, args.EndLine)
		}

	case "list_dir":
		var args struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
			MaxDepth  int    `json:"max_depth"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for list_dir: %w", err)
		} else {
			out, execErr = ListDirScoped(r.Root, args.Path, args.Recursive, args.MaxDepth, r.Scope)
		}

	case "grep":
		var args struct {
			Pattern         string `json:"pattern"`
			Path            string `json:"path"`
			CaseInsensitive bool   `json:"case_insensitive"`
			MaxMatches      int    `json:"max_matches"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for grep: %w", err)
		} else {
			out, execErr = GrepScoped(r.Root, args.Pattern, args.Path, args.CaseInsensitive, args.MaxMatches, r.Scope)
		}

	case "glob":
		var args struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for glob: %w", err)
		} else {
			out, execErr = GlobScoped(r.Root, args.Pattern, args.Path, r.Scope)
		}

	case "write_file":
		if !r.AllowWrite {
			execErr = fmt.Errorf("write_file is prohibited (write flag not enabled)")
		} else {
			var args struct {
				Path      string `json:"path"`
				Content   string `json:"content"`
				Overwrite bool   `json:"overwrite"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				execErr = fmt.Errorf("invalid arguments for write_file: %w", err)
			} else {
				out, execErr = WriteFile(r.Root, args.Path, args.Content, args.Overwrite)
			}
		}

	case "replace_in_file":
		if !r.AllowWrite {
			execErr = fmt.Errorf("replace_in_file is prohibited (write flag not enabled)")
		} else {
			var args struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				execErr = fmt.Errorf("invalid arguments for replace_in_file: %w", err)
			} else {
				out, execErr = ReplaceInFile(r.Root, args.Path, args.OldString, args.NewString, args.ReplaceAll)
			}
		}

	case "run_command":
		if !r.AllowExec {
			execErr = fmt.Errorf("run_command is prohibited (exec flag not enabled)")
		} else {
			var args struct {
				Command        string   `json:"command"`
				Args           []string `json:"args"`
				Cwd            string   `json:"cwd"`
				TimeoutSeconds int      `json:"timeout_seconds"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				execErr = fmt.Errorf("invalid arguments for run_command: %w", err)
			} else {
				// R1-5: the per-tool budget for loop-driven seats is 30s, so
				// the model-supplied argument is clamped at the tool layer.
				// RunCommand's own 120s hard cap stays as the absolute ceiling
				// for direct callers.
				args.TimeoutSeconds = clampToolTimeout(args.TimeoutSeconds)
				// RunCommand applies the sanitized env allow-list itself
				// (S-4/F4 root fix), so the dispatch needs no second copy.
				out, execErr = RunCommand(r.Root, args.Command, args.Args, args.Cwd, args.TimeoutSeconds)
			}
		}

	case "read_url_content":
		// B4-2a: GET-only, https-only (http loopback only), host
		// allow-listed, text content types only, redirect cap 3, body
		// cap 512 KiB, no headers/auth/cookies, 30s tool-layer timeout
		// (fetch.go). An empty allow-list refuses every call.
		//
		// E2 discipline, same as the scope refusal and the search case
		// below: the seeds come from the RAW arguments JSON BEFORE the
		// typed unmarshal (R13: the malformed-arguments call is seeded
		// too), and every sink of the log entry is covered: the arguments
		// JSON, the model-visible output (R3: a backend reflecting a
		// secret-shaped query value into the body persists in the log's
		// Output field unless seeded), and any URL-echoing error text.
		seeds := fetchArgSeeds(argsJSON)
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for read_url_content: %w", err)
		} else {
			out, execErr = ReadURLContent(args.URL, r.AllowHosts)
		}
		if len(seeds) > 0 {
			redact := providers.NewRedactor(seeds...)
			argsJSON = redact(argsJSON)
			out = redact(out)
			if execErr != nil {
				execErr = errors.New(redact(execErr.Error()))
			}
		}

	case "search_web":
		// B4-2b: GET-only, https-only (http loopback only), fixed 30s
		// tool-layer timeout, 512 KiB body cap (refusal, not
		// truncation), generic JSON rendering (search.go). A zero
		// source returns the honest not-configured error; a source
		// whose key env var is unset returns an error naming the var.
		//
		// E2 discipline, same as read_url_content: the seeds come from the
		// RAW arguments JSON before the typed unmarshal (R13), and they
		// cover every sink of the log entry: the arguments JSON, the
		// model-visible output (R3: a backend echoing the query or
		// reflecting the key value persists in the log's Output field
		// unless seeded), and any endpoint-echoing error text (R2: the
		// escaped query forms are seeded too, because the transport error
		// quotes the URL-escaped substitution).
		//
		// M8: the key VALUE is resolved from the environment by NAME ONCE
		// per dispatch and passed to both the seeds and the call.
		key := resolveSearchKey(r.SearchSource)
		seeds := searchLogSeeds(argsJSON, r.SearchSource, key)
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for search_web: %w", err)
		} else {
			out, execErr = searchWebKeyed(args.Query, r.SearchSource, nil, key)
		}
		if len(seeds) > 0 {
			redact := providers.NewRedactor(seeds...)
			argsJSON = redact(argsJSON)
			out = redact(out)
			if execErr != nil {
				execErr = errors.New(redact(execErr.Error()))
			}
		}

	default:
		execErr = fmt.Errorf("unknown tool %q", toolName)
	}

	duration := time.Since(start)
	r.log(seatID, model, provider, toolName, argsJSON, out, execErr, duration)

	if execErr != nil {
		return fmt.Sprintf("Error: %v", execErr), nil
	}
	return out, nil
}

// netToolSeeds renders the per-call redaction seeds for the two network tools
// from the RAW arguments JSON, before any typed unmarshal (R13): the decoded
// operand on the happy path, the raw JSON tokens on the malformed-arguments
// path. Every other tool returns nil here, so its log treatment is unchanged.
func netToolSeeds(toolName, argsJSON string, source SearchSource) []string {
	switch toolName {
	case "read_url_content":
		return fetchArgSeeds(argsJSON)
	case "search_web":
		return searchLogSeeds(argsJSON, source, resolveSearchKey(source))
	}
	return nil
}

// redactNetToolArgs applies the network tools' per-call seeds to the raw
// arguments JSON for the exit paths that log before the dispatch runs (the
// model-tools refusal, R13). Other tools get the string back unchanged.
func redactNetToolArgs(toolName, argsJSON string, source SearchSource) string {
	if seeds := netToolSeeds(toolName, argsJSON, source); len(seeds) > 0 {
		return providers.NewRedactor(seeds...)(argsJSON)
	}
	return argsJSON
}

// jsonStringValues walks a decoded JSON value and collects every string
// scalar it carries at any depth (G2b): a non-string operand slot
// ({"query": ["token <dummy>"]}) or a nested one still seeds the log.
// Numbers and booleans are never secret-shaped, so only strings collect.
func jsonStringValues(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case map[string]any:
		for _, vv := range t {
			jsonStringValues(vv, out)
		}
	case []any:
		for _, vv := range t {
			jsonStringValues(vv, out)
		}
	}
}

// jsonTokenOperands splits raw tool-call JSON into its bare tokens for
// seeding when the document does not parse: JSON punctuation and whitespace
// are separators, so a secret embedded in a malformed arguments string yields
// the bare token (a token still containing "://" stays location-shaped and
// SeedEligible-exempt by design; URL components are parsed separately).
func jsonTokenOperands(argsJSON string) []string {
	seen := map[string]bool{}
	var ops []string
	for _, tok := range strings.FieldsFunc(argsJSON, func(r rune) bool {
		switch r {
		case '"', ',', ':', '{', '}', '[', ']', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	}) {
		if tok != "" && !seen[tok] {
			seen[tok] = true
			ops = append(ops, tok)
		}
	}
	return ops
}

// rawURLInArgsRe finds an absolute or scheme-relative URL inside a raw
// arguments string (the URL-candidate recovery of fetchArgSeeds and the
// search collector): JSON punctuation may truncate or glue the value, but
// the URL itself is still component-parsable.
var rawURLInArgsRe = regexp.MustCompile(`(?:https?://|//)[^\s"'<>]+`)

// networkSchemaKeys are the fixed tool-schema field names of the two network
// tools. They are never model- or operator-shaped text, and seeding them
// whole would mask the tools' own error vocabulary ("invalid url"), so they
// ride the eligibility rule like any other operand; a key-SHAPED key still
// seeds through SeedEligible.
var networkSchemaKeys = map[string]bool{"url": true, "query": true}

// networkSeeds renders a seed set from an explicit whole list and an
// eligibility-gated operand list (the unit-pinned primitive behind
// buildNetworkSeeds). Whole operands under 4 runes (a one-character value)
// carry no credential-shaped run and would mask ordinary single letters
// across the output; the verbatim arguments text seed still covers them
// inside the Arguments quote. Dedup is map-based (N3a).
func networkSeeds(whole, operands []string) []string {
	seeds := providers.SeedList(operands)
	seen := make(map[string]bool, len(seeds)*2)
	for _, s := range seeds {
		seen[s] = true
	}
	for _, w := range whole {
		if utf8.RuneCountInString(w) < 4 || w == "{query}" || seen[w] {
			continue
		}
		seen[w] = true
		seeds = append(seeds, w)
	}
	return seeds
}

// networkArgTexts collects one call's operand texts from the raw arguments
// JSON: every argument value at any JSON depth seeds whole, every object key
// seeds whole EXCEPT the fixed schema keys (networkSchemaKeys: seeding "url"
// or "query" whole would mask the tools' own error vocabulary; a key-SHAPED
// key still seeds through the shared rule from the operand list). The raw
// arguments text contributes its punctuation tokens to the operand list (so
// benign words like "http" are not masked inside unrelated error text) and
// seeds the malformed-arguments path (R13); the text ITSELF is handed to
// buildNetworkSeeds as a verbatim whole by the callers (never cut by a cap,
// so no transformations: a prefix of the JSON text would carry the bare key
// names and mask words like "query" inside results, the M4 vocabulary). The
// decoded map (nil when malformed) is returned for callers that need a typed
// slot (the search collector's query-substituted endpoint).
func networkArgTexts(argsJSON string) (whole, operands []string, decoded map[string]any) {
	if err := json.Unmarshal([]byte(argsJSON), &decoded); err == nil {
		for _, v := range decoded {
			jsonStringValues(v, &whole)
		}
		for k := range decoded {
			if networkSchemaKeys[k] {
				operands = append(operands, k)
			} else {
				whole = append(whole, k)
			}
		}
	}
	operands = append(operands, jsonTokenOperands(argsJSON)...)
	return whole, operands, decoded
}

// recoverRawURLCandidates returns the URL candidates of a raw arguments
// string, trimmed of JSON punctuation (H2/M8: named so the recovery arm is
// unit-pinnable; its end-to-end pins are dominated by the whole-arguments
// seed).
func recoverRawURLCandidates(argsJSON string) []string {
	var out []string
	for _, m := range rawURLInArgsRe.FindAllString(argsJSON, -1) {
		out = append(out, strings.Trim(m, `"'{}[],;)`))
	}
	return out
}

// Seed budget per network-tool log entry (N3): generation is map-deduped
// (O(1) lookups) and the emitted set is capped, so pathological argument
// sizes cannot blow up the per-seed ReplaceAll masking. The COUNT budget
// (maxNetworkSeeds) applies to every tier. The BYTE budget
// (maxNetworkSeedBytes) applies from the truncation tier onward: exact
// whole operands, the verbatim arguments text, and the resolved key are
// never dropped by it (a dropped whole would leave the raw operand or key
// in the log). Filling follows the documented priority order when the
// budget is exceeded:
//
//  1. exact whole operands (argument values, object keys, recovered URLs,
//     the endpoint template and its substituted spelling, the verbatim
//     arguments text) and their marker-stripped cores;
//  2. the resolved key VALUE (whole);
//  3. truncation prefixes/windows;
//  4. case variants;
//  5. component spellings (decoded, URL-escaped, URL components, re-encoded);
//  6. the eligibility-gated punctuation tokens (lowest; dropped first).
const (
	maxNetworkSeeds     = 256
	maxNetworkSeedBytes = 32 * 1024
	maxExpandedOperand  = 4096 // runes; larger operands keep whole+ladder only (N3c)
)

// truncationLadderRunes: the exact left-anchored prefix lengths seeded for
// every operand. A body/render-cap cut keeps a PREFIX of an echoed operand;
// the ladder masks any survivor of 16..47 runes exactly (prefix-16 plus the
// 16..32 run exact prefixes leave a sub-floor tail), the 48 and 64 prefixes
// cover the 48..79 rune range the same way, and the whole seed covers the
// full echo. SLIDING WINDOWS WERE TRIED AND REMOVED (round-7 probe): a
// container operand's shifted window masked a misaligned chunk of a
// component echo and broke the aligned ladder (exact-substring ReplaceAll
// cannot order-compensate), while left-anchored prefixes can never
// substring-match a pure component echo (they carry the container's own
// "http://"/"token=" prefix text). Coverage bound: echoes up to 79 runes.
var truncationLadderRunes = []int{64, 48, 32, 31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16}

// buildNetworkSeeds assembles the spelling-complete seed set for one call
// from its operand texts (rounds 5-7 design principle: everything that can
// appear in the log lines or model-visible results as a TRANSFORMATION of an
// operand seeds unconditionally, within the seed budget above). verbatim
// carries texts that seed whole exactly (the raw arguments text: never cut
// by a cap, so no transformations). texts carries the operand texts (values,
// keys, recovered URLs, endpoint spellings); key is the resolved key VALUE
// (N1: joined BEFORE the pipeline so every transformation arm applies to
// it); gated carries the candidates behind the SeedEligible rule (schema
// keys, the key NAME, the raw-text punctuation tokens).
func buildNetworkSeeds(verbatim, texts []string, key string, gated []string) []string {
	seen := map[string]bool{}
	var tierWhole, tierKey, tierTrunc, tierCase, tierComp, tokenOperands []string
	add := func(dst *[]string, v string) {
		// The 4-rune floor applies to every tier: short components (a query
		// key "q", a path segment "s") would otherwise mask ordinary
		// single letters across the output.
		if v == "" || v == "{query}" || utf8.RuneCountInString(v) < 4 || seen[v] {
			return
		}
		seen[v] = true
		*dst = append(*dst, v)
	}
	// Priority 1: exact whole operands (the verbatim arguments text rides
	// here too).
	for _, v := range verbatim {
		add(&tierWhole, v)
	}
	for _, t := range texts {
		add(&tierWhole, t)
	}
	for _, c := range appendNetworkCores(texts) {
		add(&tierWhole, c)
	}
	// Priority 2: the resolved key VALUE, whole and then through the same
	// pipeline as every operand (N1).
	if key != "" {
		add(&tierKey, key)
	}
	for _, op := range append(append([]string(nil), texts...), key) {
		if op == "" {
			continue
		}
		var comps []string
		if utf8.RuneCountInString(op) <= maxExpandedOperand {
			// Priority 5: component spellings.
			comps = expandNetworkWhole(op)
			for _, v := range comps {
				add(&tierComp, v)
			}
			// Priority 4: case variants (M7).
			if lo := strings.ToLower(op); lo != op {
				add(&tierCase, lo)
			}
			if up := strings.ToUpper(op); up != op {
				add(&tierCase, up)
			}
		}
		// Priority 3: the truncation ladder runs over the operand AND its
		// component spellings (a query value is an operand echo even though
		// it reaches the log only as a component of the URL).
		for _, l := range append([]string{op}, comps...) {
			for _, w := range truncationLadder(l) {
				add(&tierTrunc, w)
			}
		}
	}
	// Priority 6: the eligibility-gated tokens (texts only; the expansions
	// contribute no additional masking the whole spellings do not cover).
	for _, op := range texts {
		tokenOperands = append(tokenOperands, urlTokenOperands(op)...)
	}
	tokenOperands = append(tokenOperands, gated...)

	seeds := make([]string, 0, maxNetworkSeeds)
	total, budgetHit := 0, false
	// exactWhole emits the never-dropped tiers (whole operands, the key):
	// neither the byte budget nor the never-dropped tiers' size counts
	// against the byte budget (see the budget doc above); the count budget
	// applies to everything.
	exactWhole := func(list []string) bool {
		for _, s := range list {
			if len(seeds) >= maxNetworkSeeds {
				return false
			}
			seeds = append(seeds, s)
		}
		return true
	}
	emit := func(list []string) {
		for _, s := range list {
			if budgetHit {
				return
			}
			if len(seeds) >= maxNetworkSeeds || total+len(s) > maxNetworkSeedBytes {
				budgetHit = true
				return
			}
			seeds = append(seeds, s)
			total += len(s)
		}
	}
	if !exactWhole(tierWhole) || !exactWhole(tierKey) {
		budgetHit = true
	}
	emit(tierTrunc)
	emit(tierCase)
	emit(tierComp)
	if !budgetHit {
		seeds = append(seeds, providers.SeedList(tokenOperands)...)
	}
	return seeds
}

// truncationLadder returns the exact left-anchored prefix seeds of one
// operand (see truncationLadderRunes for the coverage argument and the
// removal of the sliding windows). The one-hop boundary applies: this arms
// the masking against the tools' OWN caps; a backend that decodes twice
// before reflecting is out of scope by design (N5, docs/fanout.md).
func truncationLadder(op string) []string {
	runes := []rune(op)
	var out []string
	for _, n := range truncationLadderRunes {
		if len(runes) > n {
			out = append(out, string(runes[:n]))
		}
	}
	return out
}

// expandNetworkWhole returns the UNCONDITIONAL spelling expansions of one
// operand text (round-6 design principle): the decoded spellings
// (url.QueryUnescape, JSON-unescape) WITH their marker-stripped cores (M2:
// a decoded spelling that acquires location shape is vetoed by the
// eligibility rule, and its core must still seed), the URL component
// spellings that carry operand text (host case variants, path/fragment
// forms, opaque, userinfo, query pairs; the scheme is structural and stays
// gated, like the schema keys), the tool-generated URL-escaped spelling
// (M1), and the re-encoded spelling of eligible values (K6).
func expandNetworkWhole(op string) []string {
	if op == "" {
		return nil
	}
	var out []string
	if dec, err := url.QueryUnescape(op); err == nil && dec != op {
		out = append(out, dec, stripRelativeMarker(dec))
	}
	if dec := jsonUnescapeString(op); dec != "" && dec != op {
		out = append(out, dec, stripRelativeMarker(dec))
	}
	if esc := url.QueryEscape(op); esc != op {
		out = append(out, esc)
	}
	if u, err := url.Parse(strings.TrimSpace(op)); err == nil {
		out = append(out, urlWholeSpellings(u)...)
	}
	if providers.SeedEligible(op) {
		out = append(out, jsonReencode(op))
	}
	return out
}

// urlWholeSpellings returns the component spellings of a parsed URL that
// round 6 promotes into the unconditional whole set: host (original, lower,
// upper), path (RawPath, EscapedPath), fragment (raw, EscapedFragment,
// decoded), opaque, userinfo, and query pairs (raw, escaped, decoded). The
// http(s) scheme is structural text, like the schema keys: seeding it would
// mask every URL in every log line. A NON-structural scheme (a credential
// paste, K4) is operand text and seeds whole with case variants: net/url
// FOLDS the scheme to lowercase at parse time, so the refusal echoes the
// folded run and the original-case operand alone cannot mask it.
func urlWholeSpellings(u *url.URL) []string {
	var out []string
	if u.Scheme != "" && !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		out = append(out, u.Scheme, strings.ToLower(u.Scheme), strings.ToUpper(u.Scheme))
	}
	if u.Opaque != "" {
		out = append(out, u.Opaque)
		out = append(out, urlTokenOperands(u.Opaque)...)
	}
	if u.User != nil {
		out = append(out, u.User.String())
	}
	if h := u.Hostname(); h != "" {
		out = append(out, h, strings.ToLower(h), strings.ToUpper(h))
	}
	if u.Path != "" {
		out = append(out, u.Path, u.EscapedPath())
		if u.RawPath != "" {
			out = append(out, u.RawPath)
		}
	}
	if u.Fragment != "" {
		out = append(out, u.Fragment, u.EscapedFragment())
		if dec, err := url.QueryUnescape(u.EscapedFragment()); err == nil && dec != u.Fragment {
			out = append(out, dec)
		}
	}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		out = append(out, pair)
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			out = append(out, kv[0], kv[1], url.QueryEscape(kv[0]), url.QueryEscape(kv[1]))
			if dec, err := url.QueryUnescape(kv[1]); err == nil && dec != kv[1] {
				out = append(out, dec)
			}
			if dec, err := url.QueryUnescape(kv[0]); err == nil && dec != kv[0] {
				out = append(out, dec)
			}
		}
	}
	return out
}

// relativeMarkers are the leading location markers SeedEligible exempts
// (isLocationShaped). The stripped core is seeded whole so a credential
// glued behind a marker cannot ride the exemption (K3).
var relativeMarkers = []string{"../", "..\\", "./", ".\\"}

// stripRelativeMarker strips ONE leading relative location marker.
func stripRelativeMarker(op string) string {
	for _, m := range relativeMarkers {
		if strings.HasPrefix(op, m) {
			return op[len(m):]
		}
	}
	return op
}

// appendNetworkCores appends the relative-marker-stripped cores of the whole
// operands (K3), for the unconditional whole-operand seed set.
func appendNetworkCores(whole []string) []string {
	out := whole
	for _, op := range whole {
		if core := stripRelativeMarker(op); core != op {
			out = append(out, core)
		}
	}
	return out
}

// jsonUnescapeString returns the decoded form of an operand that carries
// JSON backslash-u escapes, or "" when it does not (or decoding fails).
func jsonUnescapeString(s string) string {
	if !strings.Contains(s, `\u`) {
		return ""
	}
	var dec string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &dec); err != nil {
		return ""
	}
	return dec
}

// jsonReencode spells every non-alphanumeric rune of s as a backslash-u
// escape (lowercase hex): the re-encoded spelling of an eligible decoded
// value, for logs that recorded the escaped form (K6). Non-BMP runes emit a
// surrogate pair (M8).
func jsonReencode(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

// fetchArgSeeds renders the read_url_content seeds from the raw arguments
// JSON: the collected operand texts (networkArgTexts) plus the recovered URL
// candidates (recoverRawURLCandidates), assembled by buildNetworkSeeds.
func fetchArgSeeds(argsJSON string) []string {
	whole, gated, _ := networkArgTexts(argsJSON)
	whole = append(whole, recoverRawURLCandidates(argsJSON)...)
	return buildNetworkSeeds([]string{argsJSON}, whole, "", gated)
}

// scopedPathTools are the path-reading tools a partition unit's scope bounds
// (B3). Every one of them takes a "path" argument (see the dispatch cases
// below). The write tools are bounded separately in scopedWriteTools (B4-1):
// they need their own resolved-view handling because a write target may not
// exist yet. Exec tools stay outside the scope filter (partition mode never
// enables them).
var scopedPathTools = map[string]bool{
	"read_file": true,
	"list_dir":  true,
	"grep":      true,
	"glob":      true,
}

// scopedWriteTools are the write tools a partition unit's scope bounds
// (B4-1): a scoped runner refuses them at paths outside the unit scope under
// the SAME dual-view rule as the reads. The AllowWrite flag is the separate
// per-unit opt-in gate: a scoped runner WITHOUT it still refuses write_file /
// replace_in_file, and an in-scope path then surfaces the existing
// "write flag not enabled" prohibition from the dispatch.
var scopedWriteTools = map[string]bool{
	"write_file":      true,
	"replace_in_file": true,
}

// scopeRefusal applies the runner's unit scope to one tool call. It returns an
// error-shaped refusal string plus a non-nil error when the call addresses a
// path outside the unit scope, and ("", nil) when the call may proceed. A nil
// Scope and a non-path tool pass through untouched; an unresolvable or
// root-escaping path is left to ResolveSafePath (and the tool) to refuse by
// name. A blank/omitted path is resolved to the workspace root "." (the default
// Grep/Glob/ListDir apply to an omitted path) and scoped like any other path,
// so a scoped unit cannot read the whole workspace by omitting the argument.
// Every addressed path is matched both lexically and through symlinks and
// refused when either lands outside the scope: the read tools use
// resolvedPathInScope directly (their target must exist to be read), the
// write tools use resolvedWriteTargetInScope, the write counterpart that
// resolves the closest existing ancestor because a write target may not exist
// yet. Both fail closed on any resolution error.
func (r *ToolRunner) scopeRefusal(toolName, argsJSON string) (string, error) {
	if len(r.Scope) == 0 || (!scopedPathTools[toolName] && !scopedWriteTools[toolName]) {
		return "", nil
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", nil // let the tool's own argument parser report the shape
	}
	// FIX A1: Grep/Glob/ListDir default a blank/omitted path to the workspace
	// root "." (their schemas mark path optional). Resolve the blank to "." here
	// so the scope check sees the exact target the tool reads instead of letting
	// the omission escape the boundary. A real unit scope is never root-wide, so
	// "." is refused for any scoped unit.
	if strings.TrimSpace(args.Path) == "" {
		args.Path = "."
	}
	abs, err := ResolveSafePath(r.Root, args.Path)
	if err != nil {
		return "", nil // escape / traversal is already refused, by name
	}
	rootAbs := filepath.Clean(r.rootAbs())
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", fmt.Errorf("path %q is outside unit scope (not relative to the workspace root)", args.Path)
	}
	// FIX A2: the path tools FOLLOW symlinks, but ResolveSafePath returns the
	// lexical path and the check used to match only that, so an in-scope symlink
	// to an out-of-scope in-root target leaked cross-unit content. Match the
	// lexical path AND the symlink-resolved real path; refuse if either is out of
	// scope. resolvedPathInScope fails closed on any resolution error.
	if !PathInScope(rel, r.Scope) {
		return r.refuseOutOfScope(args.Path)
	}
	if scopedWriteTools[toolName] {
		if !resolvedWriteTargetInScope(rootAbs, abs, r.Scope) {
			return r.refuseOutOfScope(args.Path)
		}
		return "", nil
	}
	if !resolvedPathInScope(rootAbs, abs, r.Scope) {
		return r.refuseOutOfScope(args.Path)
	}
	return "", nil
}

// refuseOutOfScope builds the error-shaped refusal for a path outside the unit
// scope: the model-visible string and the error persisted to the tool log. E2:
// the logged error embeds the unit's scope entries verbatim, and they are
// plan-authored strings a misdirected key paste could land in; the tool log's
// registry redactor knows only registered keys and generic credential shapes,
// so a bare base64 run would persist raw. The refusal therefore seeds the
// redactor with the scope entries themselves through the ONE shared
// providers.SeedEligible rule (the same rule the merge's planFieldSeeds uses):
// a secret-shaped entry renders [REDACTED] in both sinks, while location-shaped
// scope globs never seed, so the refusal keeps naming benign paths and scopes.
func (r *ToolRunner) refuseOutOfScope(argPath string) (string, error) {
	msg := fmt.Sprintf("Error: path %q is outside unit scope", argPath)
	detail := fmt.Sprintf("path %q is outside unit scope %v", argPath, r.Scope)
	if seeds := scopeRefusalSeeds(r.Scope); len(seeds) > 0 {
		redact := providers.NewRedactor(seeds...)
		msg = redact(msg)
		detail = redact(detail)
	}
	return msg, errors.New(detail)
}

// scopeRefusalSeeds renders the redaction seeds for the scope refusal: the
// unit's own scope entries that pass the shared SeedEligible rule, each plus
// its trimmed form (mirroring the fanout planFieldSeeds shape).
func scopeRefusalSeeds(scope []string) []string {
	return providers.SeedList(providers.FlagSeedOperands(scope...))
}

// resolvedWriteTargetInScope is the write-tool counterpart of
// resolvedPathInScope (guard.go, the SAME dual-view rule): it reports whether
// the symlink-resolved REAL path of a write target stays inside the unit
// scope. A write target may NOT exist yet (write_file creates new files) and
// filepath.EvalSymlinks fails on a nonexistent path, so the resolved view is
// built by resolving the closest EXISTING ancestor and appending the
// not-yet-existing tail verbatim: that ancestor chain is the real directory
// path the write will traverse, so a symlinked in-scope-looking directory that
// resolves into another unit is still caught. It fails closed on purpose: a
// resolution error or a resolved path outside the scope returns false and the
// caller refuses. rootAbs must be the cleaned absolute workspace root, target
// the ResolveSafePath output (absolute and root-contained), scope non-empty.
func resolvedWriteTargetInScope(rootAbs, target string, scope []string) bool {
	rel, err := resolvedWriteRelPath(rootAbs, target)
	if err != nil {
		return false
	}
	return PathInScope(rel, scope)
}

// resolvedWriteRelPath renders the real path a write to target will land on,
// as a path relative to the symlink-resolved root: the closest existing
// ancestor of target is EvalSymlinks'd and the nonexistent tail is appended
// component by component. Both sides end up compared in the same real-path
// space, mirroring resolvedRelPath; it fails closed on any resolution error
// or a resolved path that escapes the root.
func resolvedWriteRelPath(root, target string) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	cur := target
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", target)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	realAncestor, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	resolved := realAncestor
	for _, comp := range tail {
		resolved = filepath.Join(resolved, comp)
	}
	rel, err := filepath.Rel(rootReal, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved write target escapes root: %q", rel)
	}
	return rel, nil
}

// rootAbs is the runner root in absolute form, shared by the scope check and
// ResolveSafePath so both sides compare the same base.
func (r *ToolRunner) rootAbs() string {
	root := r.Root
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return root
	}
	return abs
}

func (r *ToolRunner) log(seatID int, model, provider, tool, args, out string, err error, dur time.Duration) {
	if r.Logger == nil {
		return
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	_ = r.Logger.Log(LogEntry{
		SeatID:     seatID,
		Model:      model,
		Provider:   provider,
		Tool:       tool,
		Arguments:  args,
		Output:     out,
		Error:      errMsg,
		DurationMs: dur.Milliseconds(),
	})
}
