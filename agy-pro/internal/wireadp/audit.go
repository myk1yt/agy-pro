package wireadp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agypro/internal/paths"
)

// Masked JSONL audit (§5.2, §9.1, §10.2): one line per request carrying the
// method, the route class, the model name when the request carried one, the
// status and the duration, plus inbound header NAMES only. No header value,
// key value or request content is ever written; every line passes the
// configured redactor before it lands on disk.

// auditEntry is one JSONL line.
type auditEntry struct {
	TS          time.Time `json:"ts"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	Route       string    `json:"route"`
	Verb        string    `json:"verb,omitempty"`
	RPC         string    `json:"rpc,omitempty"`
	Model       string    `json:"model,omitempty"`
	Status      int       `json:"status"`
	DurationMS  int64     `json:"duration_ms"`
	HeaderNames []string  `json:"header_names,omitempty"`
	// Note carries adapter-side diagnostics (codec notes, repair-retry
	// attempts). It never carries a credential, a header value or an upstream
	// body: the whole line passes the redactor before it lands on disk.
	Note string `json:"note,omitempty"`
}

// auditRecord is the per-request audit state: the guard creates it, the route
// handlers enrich it with the route class, verb/rpc and model name. It holds no
// credential of any kind (see the package doc on hygiene).
type auditRecord struct {
	method      string
	path        string
	headerNames []string
	route       string
	verb        string
	rpc         string
	model       string
	note        string
}

// maxFieldRunes caps every request-derived audit field (note, model, path, rpc,
// verb, header name). The cap exists because those fields are request- or
// registry-controlled: an unresolved /v1internal envelope with a multi-MB model
// string would otherwise append a ~32 MB JSONL line per request.
const maxFieldRunes = 240

func (ar *auditRecord) setClass(class string) {
	if ar != nil {
		ar.route = capRunes(oneLine(class), maxFieldRunes)
	}
}

func (ar *auditRecord) setTarget(verb, rpc, model string) {
	if ar == nil {
		return
	}
	if verb != "" {
		ar.verb = capRunes(oneLine(verb), maxFieldRunes)
	}
	if rpc != "" {
		ar.rpc = capRunes(oneLine(rpc), maxFieldRunes)
	}
	if model != "" {
		ar.model = capRunes(oneLine(model), maxFieldRunes)
	}
}

// addNote appends a diagnostic note. Notes are joined with "; " and capped so
// one line stays one line; the redactor still runs over the whole line.
func (ar *auditRecord) addNote(note string) {
	if ar == nil {
		return
	}
	note = capRunes(oneLine(strings.TrimSpace(note)), maxFieldRunes)
	if note == "" {
		return
	}
	if ar.note == "" {
		ar.note = note
		return
	}
	ar.note = capRunes(ar.note+"; "+note, maxFieldRunes)
}

// capRunes truncates on rune boundaries (FIX-40). A byte cut could split a
// multi-byte rune, which json.Marshal would coerce to U+FFFD and silently
// mangle the audit line.
func capRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// auditContextKey is the only context value the request pipeline installs. It
// carries names and counters, never credentials, so a context that later
// reaches an upstream call cannot leak a key (the U5b outbound builder must
// not receive the inbound X-Goog-Api-Key value at all).
type auditContextKey struct{}

func withAuditRecord(ctx context.Context, ar *auditRecord) context.Context {
	return context.WithValue(ctx, auditContextKey{}, ar)
}

func auditFromContext(ctx context.Context) *auditRecord {
	ar, _ := ctx.Value(auditContextKey{}).(*auditRecord)
	return ar
}

// auditLogger appends masked JSONL lines. The file is created lazily on the
// first write so an idle adapter leaves no empty logs, and every failure is
// swallowed: audit must never affect a response.
type auditLogger struct {
	path   string
	redact func(string) string

	mu   sync.Mutex
	file *os.File
}

// newAuditLogger resolves the audit path. An explicit path wins; otherwise the
// line lands in <agy-pro home>/logs/wireadp-<start ts>.jsonl, the same
// convention the bridge audit uses. When the home cannot be resolved, auditing
// is disabled (there is nowhere safe to write).
func newAuditLogger(path string, redact func(string) string) *auditLogger {
	l := &auditLogger{redact: redact}
	if path == "" {
		if home, err := paths.AgyProHome(); err == nil {
			path = filepath.Join(home, "logs", "wireadp-"+time.Now().UTC().Format("20060102-150405")+".jsonl")
		}
	}
	l.path = path
	return l
}

func (l *auditLogger) log(entry auditEntry) {
	if l == nil || l.path == "" {
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
			return
		}
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		l.file = f
	}
	text := string(line)
	if l.redact != nil {
		text = l.redact(text)
	}
	if _, err := io.WriteString(l.file, text+"\n"); err != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func (l *auditLogger) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// headerNames lists the inbound header names, sorted. Values are never read
// into the audit path (§5.2). Names are capped too: a hostile peer can send an
// arbitrary header name, and the audit line must stay bounded.
func headerNames(r *http.Request) []string {
	if len(r.Header) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.Header))
	for name := range r.Header {
		names = append(names, capRunes(oneLine(name), maxFieldRunes))
	}
	sort.Strings(names)
	return names
}

// oneLine strips control characters. A percent-encoded newline in a path would
// otherwise break the one-line-per-request invariant and could forge audit
// lines.
func oneLine(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
