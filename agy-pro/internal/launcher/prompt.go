package launcher

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// MenuTimeout is how long the fast launcher waits before taking the native
// default. A quiet `agy` session must never feel slower than this.
const MenuTimeout = 2 * time.Second

// CommunityBanner is the frozen community notice (contract §2.10, U7).
// SINGLE SOURCE OF TRUTH: one constant, one render site (ShowMenu). It is
// printed exactly once per interactive launcher invocation and NEVER in print
// mode, piped/non-TTY output, or any machine output - those routes never
// reach ShowMenu (Decide rules, decide.go:58-81).
const CommunityBanner = "Visit https://www.reddit.com/r/GoogleAntigravityCLI/! A space for antigravity-cli users. Remember r/GoogleAntigravityCLI"

// consoleRead classifies one raw-console read attempt.
type consoleRead int

const (
	// consoleUnavailable: no console behind the reader, use line input.
	consoleUnavailable consoleRead = iota
	// consoleTimeout: console present but no choice key within the deadline.
	consoleTimeout
	// consoleChoice: a launcher key was received.
	consoleChoice
)

// mapConsoleKey maps a Windows console key event to a launcher choice string
// ("", "1", "2", "3"). ok is false for keys that are not launcher input.
// An empty choice with ok=true is Enter, which resolves to the native default.
func mapConsoleKey(char uint16, vk uint16) (string, bool) {
	switch char {
	case '\r', '\n':
		return "", true
	case '1', '2', '3':
		return string(rune(char)), true
	}
	if vk == 0x0d { // VK_RETURN, e.g. numpad Enter
		return "", true
	}
	return "", false
}

// lineSource delivers launcher lines (menu choice, swarm prompt) without ever
// giving up ownership of the underlying input stream.
type lineSource interface {
	// readLine returns one trimmed input line. ok is false when the timeout
	// expired first, or on EOF with nothing pending. timeout <= 0 blocks until
	// a line or EOF.
	readLine(timeout time.Duration) (string, bool)
}

// lineResult is one readLine outcome.
type lineResult struct {
	line string
	ok   bool
}

// lineRequest is one outstanding readLine call.
type lineRequest struct {
	resp  chan lineResult
	timer *time.Timer // nil = no deadline
}

// stdinRelay is the single owner of the launcher's interactive input (F-2).
//
// Ownership rule: one reader goroutine touches the source for the relay's
// whole lifetime, and the real agy child never reads that source directly -
// it reads the pipe the relay feeds. Bytes the menu claims are delivered as
// lines; every other byte (including a partial line abandoned by a deadline,
// and anything typed after the last claimed line) is written to the child
// pipe in arrival order. A leftover menu reader can therefore never steal the
// child's first input, and the child can never miss a byte that was typed
// before it started.
type stdinRelay struct {
	src io.Reader

	childR *os.File // read end handed to the child
	childW *os.File // write end, owned by the pump

	askC   chan *lineRequest
	relayC chan struct{} // closed by ChildStdin
	chunkC chan []byte
	done   chan struct{} // closed when the pump exits (F-23 observability)

	startOnce sync.Once
	readMu    sync.Mutex // serializes readLine calls
	mu        sync.Mutex
	used      bool
	relayOnce sync.Once
}

// newStdinRelay wraps src. Nothing is read until the first readLine call, so
// a route that never shows the launcher can still hand the untouched source
// to the child.
func newStdinRelay(src io.Reader) *stdinRelay {
	// os.Pipe fails only on handle exhaustion; one retry is the last resort.
	childR, childW, err := os.Pipe()
	if err != nil {
		childR, childW, err = os.Pipe()
	}
	_ = err
	return &stdinRelay{
		src:    src,
		childR: childR,
		childW: childW,
		askC:   make(chan *lineRequest),
		relayC: make(chan struct{}),
		chunkC: make(chan []byte, 8),
		done:   make(chan struct{}),
	}
}

// start launches the source reader and the pump exactly once.
func (r *stdinRelay) start() {
	r.startOnce.Do(func() {
		go r.readLoop()
		go r.run()
	})
}

// readLoop is the only goroutine that reads the source. It forwards raw
// chunks to the pump and exits at EOF; it can never deliver input anywhere
// except the pump, so a menu deadline cannot leave a thief behind.
func (r *stdinRelay) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := r.src.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			r.chunkC <- chunk
		}
		if err != nil {
			close(r.chunkC)
			return
		}
	}
}

// writeChild forwards raw bytes to the child pipe. The pump is the only
// writer, so arrival order is preserved end to end.
func (r *stdinRelay) writeChild(b []byte) {
	if r.childW == nil || len(b) == 0 {
		return
	}
	_, _ = r.childW.Write(b)
}

// run routes source bytes: into the pending line request, or to the child.
// After a deadline (or ChildStdin) it becomes a pure relay and never stops
// forwarding, so no byte can fall into an abandoned reader. The source ending
// (EOF) does not end the pump while launcher lines are still possible or the
// handoff to the child has not happened yet; it exits once everything is with
// the child (F-23).
func (r *stdinRelay) run() {
	defer close(r.done)
	defer func() {
		if r.childW != nil {
			r.childW.Close()
		}
	}()

	var (
		acc     []byte // bytes held but not yet claimed by a line
		pending *lineRequest
		relay   bool
		eof     bool
	)
	flush := func() {
		if len(acc) > 0 {
			r.writeChild(acc)
			acc = nil
		}
	}
	deliver := func(line string) {
		if pending == nil {
			return
		}
		if pending.timer != nil {
			pending.timer.Stop()
		}
		pending.resp <- lineResult{line: line, ok: true}
		pending = nil
	}

	chunkC := r.chunkC
	for {
		if eof && relay && len(acc) == 0 && pending == nil {
			return
		}
		var timeoutC <-chan time.Time
		if pending != nil && pending.timer != nil {
			timeoutC = pending.timer.C
		}
		select {
		case chunk, ok := <-chunkC:
			if !ok {
				// Source EOF: stop selecting on it, but keep serving launcher
				// lines and the child handoff that may still be requested.
				chunkC = nil
				eof = true
				if pending != nil {
					deliver(strings.TrimSpace(string(acc)))
					acc = nil
				}
				continue
			}
			if relay {
				r.writeChild(chunk)
				continue
			}
			acc = append(acc, chunk...)
			for pending != nil {
				i := bytes.IndexByte(acc, '\n')
				if i < 0 {
					break
				}
				line := strings.TrimSpace(string(acc[:i+1]))
				acc = acc[i+1:]
				deliver(line)
			}
		case <-timeoutC:
			// Deadline: the abandoned partial line and every later byte
			// belong to the child, re-emitted in arrival order.
			pending.resp <- lineResult{}
			pending = nil
			relay = true
			flush()
		case req := <-r.askC:
			if i := bytes.IndexByte(acc, '\n'); i >= 0 {
				line := strings.TrimSpace(string(acc[:i+1]))
				acc = acc[i+1:]
				if req.timer != nil {
					req.timer.Stop()
				}
				req.resp <- lineResult{line: line, ok: true}
				continue
			}
			if eof {
				// No more input can arrive: answer with the last partial.
				if req.timer != nil {
					req.timer.Stop()
				}
				req.resp <- lineResult{line: strings.TrimSpace(string(acc)), ok: true}
				acc = nil
				continue
			}
			pending = req
		case <-r.relayC:
			relay = true
			flush()
		}
	}
}

// readLine assembles one launcher line under a deadline (see lineSource).
func (r *stdinRelay) readLine(timeout time.Duration) (string, bool) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	r.start()
	r.mu.Lock()
	r.used = true
	r.mu.Unlock()

	req := &lineRequest{resp: make(chan lineResult, 1)}
	if timeout > 0 {
		req.timer = time.NewTimer(timeout)
		defer req.timer.Stop()
	}
	select {
	case r.askC <- req:
	case <-r.done:
		return "", false
	}
	select {
	case res := <-req.resp:
		return res.line, res.ok
	case <-r.done:
		return "", false
	}
}

// Used reports whether the line fallback touched the input at all. When it
// did not (the Windows console raw path handled the menu), the child can
// inherit the original console stream unchanged.
func (r *stdinRelay) Used() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}

// ChildStdin returns the stream the real agy child must read instead of the
// original source. Everything the launcher read but did not claim - the
// deadline-abandoned partial line, bytes typed after the last claimed line,
// and all later input - is relayed here in arrival order. Idempotent.
func (r *stdinRelay) ChildStdin() io.Reader {
	r.start()
	r.relayOnce.Do(func() { close(r.relayC) })
	if r.childR == nil {
		return strings.NewReader("")
	}
	return r.childR
}

// waitDone blocks until the pump exits (source EOF) or the deadline passes.
// Used by tests to prove the relay's goroutines terminate (F-23).
func (r *stdinRelay) waitDone(timeout time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ShowMenu writes the fast launcher banner and returns the chosen route.
// consoleIn is the console file used for raw single-key input (nil in tests
// and on non-consoles); lines is the shared input relay, so a choice line and
// a follow-up swarm prompt never strand bytes in competing buffers, and any
// input the menu did not claim reaches the real agy child.
//
// Input handling degrades in three stages:
//  1. Windows console: raw single-key poll (kernel32 PeekConsoleInput) under
//     a deadline, without a blocked read that could outlive the menu.
//  2. Anything else: line input through the relay with the same deadline;
//     Enter submits.
//  3. Deadline reached in either stage: the native default, so the menu can
//     never stall a session past MenuTimeout.
func ShowMenu(out io.Writer, consoleIn *os.File, lines lineSource, modelCount int, providerSummary string, timeout time.Duration) Route {
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, "  agy Fast Launcher - default in 2s: [1] Native Gemini")
	fmt.Fprintln(out, "===========================================================")
	fmt.Fprintln(out, CommunityBanner)
	fmt.Fprintln(out, "  [1] Native Gemini (default)")
	fmt.Fprintf(out, "  [2] External Models (%d models: %s)\n", modelCount, providerSummary)
	fmt.Fprintln(out, "  [3] Swarm (multi-model fanout)")
	fmt.Fprint(out, "Select [1/2/3] (Enter=default): ")

	if consoleIn != nil {
		if choice, status := readConsoleChoice(consoleIn, timeout); status != consoleUnavailable {
			if status == consoleChoice {
				return ResolveMenuChoice(choice)
			}
			return RouteNative
		}
	}
	if line, ok := lines.readLine(timeout); ok {
		return ResolveMenuChoice(line)
	}
	return RouteNative
}

// isTerminal reports whether f is attached to a character device (console or
// TTY). Used by the caller to keep pipes and CI on the zero-delay path.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ProviderSummary renders the launcher "External Models" provider list.
func ProviderSummary(providerNames []string) string {
	seen := make(map[string]bool, len(providerNames))
	var uniq []string
	for _, n := range providerNames {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		uniq = append(uniq, n)
	}
	if len(uniq) == 0 {
		return "none"
	}
	return strings.Join(uniq, ", ")
}
