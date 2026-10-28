package fanout

// Native search execution (P30 fallback, P32 tool restriction).
//
// The search child is one real agy print-mode process per search_web call. Two
// routes exist:
//
//   - Restricted (P32). `--agent agypro-search` selects a file-declared agent
//     asset whose frontmatter carries `tools: [search_web, read_url_content]`.
//     agy enforces that structurally: the live binary hands the child 4 tools
//     instead of 12 (no run_command, no write_file, no edit_file, no subagent
//     tools), the restriction survives --dangerously-skip-permissions, and it is
//     model-family independent. Evidence:
//     docs/261026_0001_session_editable-builtin-defaults/probe-agent-restriction-report.md.
//     The asset is machine scope (~/.gemini/config/agents/agypro-search/AGENT.md,
//     deployed from assets/agents/agypro-search/AGENT.md by scripts/install.ps1),
//     so its presence is a RUNTIME question, answered once per run.
//   - Fallback. No --agent. This is the pre-P32 argv: the prompt asks the child
//     to stay on web reading, which is a soft control only.
//
// The name binding is fail-closed: an unknown agent name exits 1 and never
// degrades into an unrestricted child. That property is what makes the
// restricted route safe to default to, and it is also why a vanished asset needs
// explicit handling (one downgrade, one notice, exactly one retry).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agypro/internal/realagy"
)

const (
	// searchAgentName is the restricted child's agent name. The repo source of
	// truth for the deployed asset is assets/agents/agypro-search/AGENT.md.
	searchAgentName = "agypro-search"

	// agentListTimeout bounds the `agy agents` presence probe. The command only
	// prints the discovered agent set; the bound keeps a wedged binary from
	// stalling a run before its first search.
	agentListTimeout = 30 * time.Second

	// unknownAgentScanBytes bounds the "unknown agent" scan of a child payload.
	// The error object rides near the front of agy's JSON envelope (probe P4), so
	// an 8 KiB window is enough and a 4 MiB answer is never lower-cased wholesale.
	unknownAgentScanBytes = 8 << 10

	// nativeSearchStatusSuccess is the only envelope status whose answer is
	// served unmarked. Any other non-empty status is prefixed with its own
	// marker (see nativeSearchStatusMarker), so an ERROR-envelope answer can
	// never pass for ordinary findings.
	nativeSearchStatusSuccess = "SUCCESS"

	// nativeSearchStatusClamped is the marker spelling for a status that is not
	// status-shaped. It is not a status agy reports; it only says "agy flagged
	// this answer and the flag's spelling could not be trusted".
	nativeSearchStatusClamped = "NONSUCCESS"

	// nativeSearchStatusMaxLen bounds an accepted status spelling: 16 characters
	// covers every enum agy has used (SUCCESS, ERROR, INELIGIBLE, ...) with room
	// to spare, and keeps the marker a marker rather than a paragraph.
	nativeSearchStatusMaxLen = 16
)

// nativeSearchStatusMarker renders the prefix an accepted answer carries when
// agy reported a non-SUCCESS status. The status travels from the child's JSON
// envelope into the fenced answer, so it is clamped before rendering (r14sec
// R14-1): an enum-shaped spelling (upper case, digits and `_` after the first
// rune, `^[A-Z][A-Z0-9_]{0,15}$`) is kept as-is (only surrounding whitespace is
// trimmed), which keeps the probe
// payloads reading `[agy status:ERROR] `, and anything else (lower case,
// punctuation, embedded newlines, an over-long or non-ASCII token) renders the
// fixed NONSUCCESS spelling. Unclamped, a crafted status made its own prose the
// answer's first lines while wearing the tool-voice prefix. The trailing space
// keeps the marker separate from the answer's first word.
func nativeSearchStatusMarker(status string) string {
	status = strings.TrimSpace(status)
	if !statusShaped(status) {
		status = nativeSearchStatusClamped
	}
	return "[agy status:" + status + "] "
}

// statusShaped reports whether s is an enum-shaped status token: a leading
// upper-case letter, then up to nativeSearchStatusMaxLen-1 upper-case letters,
// digits or underscores. ASCII-only by construction, so no rune outside that set
// (a newline, a combining mark, a byte-order trick) survives.
func statusShaped(s string) bool {
	if s == "" || len(s) > nativeSearchStatusMaxLen {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// searchFallbackNotice is the one-line downgrade disclosure emitted when a
// restricted spawn proves the asset absent mid-run.
const searchFallbackNotice = "fanout: search=agy-native restricted agent " + searchAgentName +
	" is not deployed (child exited 1: unknown agent); fell back to the full agy agent child for the rest of this run"

// searchAgentProbe is the CLI-side presence probe factory. It builds the same
// resolver/spawn pair the engine's native runner will use, so the run notice and
// the child cannot disagree about which binary was asked. Tests swap it to fake
// `agy agents` output; the production value spawns nothing until it is called.
var searchAgentProbe = func(cfg RunConfig) func(context.Context) (string, error) {
	runner := &NativeRunner{Resolver: cfg.NativeResolver, Spawn: cfg.SpawnCommand}
	return runner.agentList
}

// searchAgentPresenceFor returns the run's presence object: the CLI's resolved
// answer when one was threaded through RunConfig, otherwise a fresh lazily
// probed object whose downgrade notice rides the run's warning sink.
func searchAgentPresenceFor(cfg RunConfig) *SearchAgentPresence {
	if cfg.SearchAgentPresence != nil {
		return cfg.SearchAgentPresence
	}
	p := &SearchAgentPresence{}
	if cfg.WarnOut != nil {
		warnOut := cfg.WarnOut
		p.FallbackNotice = func(line string) { fmt.Fprintln(warnOut, line) }
	}
	return p
}

// SearchAgentPresence answers, at most once per run, whether `agy agents` lists
// the restricted search agent (P32). The engine passes --agent only when it
// does; otherwise the run keeps the prompt-restricted fallback route.
//
// One object per run. The CLI resolves it before the run notice (so the
// disclosed route is the executed route) and threads it into RunConfig; the
// engine hands the same object to its native runner, which therefore never
// re-probes. A library caller that supplies none gets a lazily-probed one at the
// first search.
type SearchAgentPresence struct {
	// Name overrides the deployed agent name; "" -> searchAgentName.
	Name string
	// Probe returns the `agy agents` listing. nil -> the caller-supplied probe
	// (NativeRunner.agentList for the engine path).
	Probe func(ctx context.Context) (string, error)
	// FallbackNotice receives the single downgrade line when a restricted spawn
	// discovers the asset absent mid-run. nil = discard.
	FallbackNotice func(line string)

	mu       sync.Mutex
	resolved bool
	agent    string
	notified bool
}

// Agent returns the agent name to pass to --agent for this run, or "" for the
// prompt-restricted fallback route. The probe runs at most once per run, and a
// probe failure (unresolvable binary, non-zero exit) resolves to the fallback
// route rather than an error: the fallback is the pre-P32 argv, so no run can
// fail because the asset could not be checked.
func (p *SearchAgentPresence) Agent(ctx context.Context, probe func(context.Context) (string, error)) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved {
		return p.agent
	}
	p.resolved = true
	fn := p.Probe
	if fn == nil {
		fn = probe
	}
	if fn == nil {
		return ""
	}
	listing, err := fn(ctx)
	if err != nil {
		return ""
	}
	if agentListed(listing, p.name()) {
		p.agent = p.name()
	}
	return p.agent
}

// downgrade flips the run onto the fallback route after a restricted spawn
// proved the asset absent, and emits the disclosure exactly once. The flip is
// sticky: every later search in the same run takes the fallback argv, so a
// vanished asset costs one failed spawn, not one per query.
func (p *SearchAgentPresence) downgrade() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.resolved = true
	p.agent = ""
	fn := p.FallbackNotice
	emit := fn != nil && !p.notified
	p.notified = true
	p.mu.Unlock()
	if emit {
		fn(searchFallbackNotice)
	}
}

func (p *SearchAgentPresence) name() string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	return searchAgentName
}

// agentListed reports whether an `agy agents` listing names the agent. Matching
// is by whole token, not substring: the probe asset `agypro-search-probe` was
// still deployed beside it while P32 was verified, and a substring test would
// select the restricted route on a machine where the production asset is
// missing. Read-only and allocation-light: the listing is one name per line or a
// comma/colon separated run.
func agentListed(listing, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, field := range strings.FieldsFunc(listing, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) {
		if field == name {
			return true
		}
	}
	return false
}

// searchAgentPresence returns this run's presence object, creating one on first
// use for callers that did not thread a resolved answer through RunConfig.
func (r *NativeRunner) searchAgentPresence() *SearchAgentPresence {
	if r.SearchAgent != nil {
		return r.SearchAgent
	}
	r.agentMu.Lock()
	defer r.agentMu.Unlock()
	if r.agentPresence == nil {
		r.agentPresence = &SearchAgentPresence{}
	}
	return r.agentPresence
}

// agentList runs `agy agents` and returns its stdout: the presence probe. The
// child is bounded, non-interactive and scrubbed exactly like a seat (the
// listing needs no provider key).
func (r *NativeRunner) agentList(ctx context.Context) (string, error) {
	bin, err := r.resolve()
	if err != nil {
		return "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, agentListTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if isBatchScript(bin) {
		cmd = r.spawn(probeCtx, "cmd.exe")
		setRawCmdLine(cmd, buildBatchCmdLine(bin, []string{"agents"}))
	} else {
		cmd = r.spawn(probeCtx, bin, "agents")
	}
	cmd.Env = realagy.CleanChildEnvWithBase(cmd.Env, r.scrubNames)
	var stdout cappedBuffer
	var stderr tailBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		return "", fmt.Errorf("agy agents probe failed: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runNativeSearch runs one search with at most one retry, and never more when
// the restricted route already spent the retry (see NativeRunner.RunSearch).
func runNativeSearch(ctx context.Context, cfg RunConfig, runner *NativeRunner, query string, deadline time.Time) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		outcome := runner.RunSearch(ctx, cfg, query, deadline)
		answer, err := nativeSearchAnswer(outcome)
		if err == nil {
			return answer, nil
		}
		lastErr = err
		// The restricted -> fallback downgrade already spawned the run's one
		// retry for this query (P32 pin: exit 1 + `unknown agent` is exactly one
		// fallback spawn, never a second attempt on top).
		if outcome.fallbackUsed {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return "", fmt.Errorf("real agy search failed after retry: %w", lastErr)
}

// isUnknownAgentOutcome reports the fail-closed shape the probe captured: exit 1
// with `unknown agent "<name>" (available: ...)` in the child's payload or
// stderr. It is the ONLY shape that triggers the fallback retry: any other
// failure keeps its normal retry budget.
func isUnknownAgentOutcome(o NativeOutcome) bool {
	if o.ExitCode != 1 {
		return false
	}
	return containsUnknownAgent(o.Content) || containsUnknownAgent(o.StderrTail)
}

func containsUnknownAgent(s string) bool {
	if len(s) > unknownAgentScanBytes {
		s = s[:unknownAgentScanBytes]
	}
	return strings.Contains(strings.ToLower(s), "unknown agent")
}

// nativeSearchAnswer parses one child answer. A non-empty response is accepted
// even when status is ERROR (the child can answer and still report a status, and
// that answer is a valid search result), but an accepted answer from a
// non-SUCCESS envelope is MARKED: the text opens with `[agy status:ERROR] ` so
// the seat can tell a normal answer from one agy itself flagged (r13 M-1: an
// attacker-influenced page can make the child return prose while agy reports
// ERROR, and unmarked error text then reads as findings). The ERROR payload's
// `error` field is folded into the surfaced message: it is where agy states the
// actual cause (`unknown agent "x" (available: ...)`, eligibility refusals), and
// dropping it left a missing or renamed asset diagnosable only from the raw
// payload.
func nativeSearchAnswer(outcome NativeOutcome) (string, error) {
	var payload struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(outcome.Content), &payload); err == nil {
		if answer := strings.TrimSpace(payload.Response); answer != "" {
			if status := strings.TrimSpace(payload.Status); status != "" && !strings.EqualFold(status, nativeSearchStatusSuccess) {
				answer = nativeSearchStatusMarker(status) + answer
			}
			return answer, nil
		}
		detail := strings.TrimSpace(payload.Error)
		if status := strings.TrimSpace(payload.Status); status != "" {
			msg := fmt.Sprintf("real agy returned status %s with an empty response", status)
			if detail != "" {
				msg += ": " + detail
			}
			return "", errors.New(msg)
		}
		if detail != "" {
			return "", fmt.Errorf("real agy reported an error: %s", detail)
		}
	}
	if outcome.SpawnErr != nil {
		return "", outcome.SpawnErr
	}
	if outcome.TimedOut {
		return "", fmt.Errorf("real agy search exceeded its deadline: %s", strings.TrimSpace(outcome.StderrTail))
	}
	if outcome.ExitCode != 0 {
		return "", fmt.Errorf("real agy exited %d: %s", outcome.ExitCode, strings.TrimSpace(outcome.StderrTail))
	}
	return "", fmt.Errorf("real agy returned no JSON response: %s", strings.TrimSpace(outcome.StderrTail))
}
