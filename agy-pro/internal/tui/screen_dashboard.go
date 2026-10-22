package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"agypro/internal/cellwidth"
	"agypro/internal/dashboard"
	"agypro/internal/i18n"
	"agypro/internal/providers"
)

// Catalog keys the dashboard screen needs that are not in the catalogs yet.
// Per the mission rule the screen ships local English stand-ins and the S3
// catalog pass owns the real entries; the stand-in wins only while the catalog
// lacks the key. Every const below names the key it stands in for, so S3 can
// enumerate the list from this block alone.
const (
	// catalog key S3 must add: dashboard.title
	missingDashboardTitle = "Dashboard"
	// catalog key S3 must add: dashboard.server.up
	missingDashboardServerUp = "Server up on port {{port}} since {{since}}"
	// catalog key S3 must add: dashboard.server.none
	missingDashboardServerNone = "No server record"
	// catalog key S3 must add: dashboard.url
	missingDashboardURL = "URL: {{url}}"
	// catalog key S3 must add: dashboard.data.ok
	missingDashboardDataOK = "Data OK"
	// catalog key S3 must add: dashboard.data.stale
	missingDashboardDataStale = "Data stale: the snapshot is older than this build understands"
	// catalog key S3 must add: dashboard.data.missing
	missingDashboardDataMissing = "Data missing: no snapshot has been written yet"
	// catalog key S3 must add: dashboard.data.generated
	missingDashboardDataGenerated = "generated {{at}} (age {{age}})"
	// catalog key S3 must add: dashboard.data.age_unknown
	missingDashboardAgeUnknown = "age unknown"
	// catalog key S3 must add: dashboard.period.today
	missingDashboardToday = "Today"
	// catalog key S3 must add: dashboard.period.last7d
	missingDashboardLast7d = "Last 7d"
	// catalog key S3 must add: dashboard.period.row
	missingDashboardPeriodRow = "{{period}}: {{cost}} · {{tokens}} tokens · {{sessions}}"
	// catalog key S3 must add: dashboard.count.sessions.one
	missingDashboardSessionsOne = "{{n}} session"
	// catalog key S3 must add: dashboard.count.sessions.other
	missingDashboardSessionsOther = "{{n}} sessions"
	// catalog key S3 must add: dashboard.models.header
	missingDashboardModelsHeader = "Top models by cost"
	// catalog key S3 must add: dashboard.models.row
	missingDashboardModelsRow = "  {{rank}}. {{model}} · {{cost}} · {{tokens}} tokens"
	// catalog key S3 must add: dashboard.action.start
	missingDashboardActionStart = "Start server"
	// catalog key S3 must add: dashboard.action.refresh
	missingDashboardActionRefresh = "Refresh"
	// catalog key S3 must add: dashboard.action.open
	missingDashboardActionOpen = "Open in browser"
	// catalog key S5 re-texted: dashboard.reason.open (it used to say browser
	// open "comes with the next build"; the row is live now, so the only reason
	// left is the honest one: no port is known yet).
	missingDashboardReasonOpen = "disabled · no server port known yet"
	// catalog key S3 must add: dashboard.msg.disabled
	missingDashboardMsgDisabled = "{{action}}: {{reason}}"
	// catalog key S3 must add: dashboard.refresh.running
	missingDashboardRefreshing = "refreshing the dashboard snapshot..."
	// catalog key S3 must add: dashboard.refresh.ok
	missingDashboardRefreshOK = "refreshed: the snapshot on disk was reloaded"
	// catalog key S3 must add: dashboard.refresh.failed
	missingDashboardRefreshFailed = "refresh failed: {{err}}"
	// catalog key S5 must add: dashboard.start.running
	missingDashboardStarting = "starting the dashboard server..."
	// catalog key S5 must add: dashboard.start.failed
	missingDashboardStartFailed = "start failed: {{err}}"
	// catalog key S5 must add: dashboard.start.waiting
	missingDashboardWaiting = "no port recorded yet; press Refresh to reload the snapshot"
	// catalog key S5 must add: dashboard.open.ok
	missingDashboardOpenOK = "opened {{url}} in the browser"
	// catalog key S5 must add: dashboard.open.failed
	missingDashboardOpenFailed = "could not open a browser: {{err}}"
)

// Row IDs of the dashboard action list, in display order. Refresh is live
// (S4: a bounded one-shot exec plus a reload through the S1 loaders); the
// Start and Open rows still render disabled with a named reason because the
// spawn and browser-open units have not landed yet.
const (
	dashIDStart   = "start"
	dashIDRefresh = "refresh"
	dashIDOpen    = "open"
	dashIDBack    = "back"
)

// dashboardTopModels is the number of per-model rows the compact view renders.
const dashboardTopModels = 3

// dashboardSource is the read seam: the two artifact loaders, the clock the
// age line derives from, and the actions. Production leaves every field nil and
// the constructor binds the dashboard package's loaders, the real one-shot
// exec, the guarded detached spawn, the loopback browser opener, and a real
// sleep; tests inject fixed responses so every status combination renders
// without a fixture tree on disk, and stub every action so no test can spawn a
// child process or start a browser.
//
// The file-path half of the seam lives one package down (dashboard.dirOverride
// is unexported on purpose): the screen never touches a path itself, it only
// consumes what the loaders return, so a path change cannot diverge here.
type dashboardSource struct {
	loadServer  func() (dashboard.ServerRecord, dashboard.Status)
	loadPayload func() (dashboard.Payload, dashboard.Status)
	now         func() time.Time
	// refresh runs the bounded one-shot exec. It reports only whether the
	// artifacts were rewritten; the reload happens through loadServer /
	// loadPayload, so the screen never parses child output.
	refresh func(ctx context.Context) error
	// spawn runs the guarded detached spawn (fresh spawn intent, live record and
	// the 15s stampede window are all decided inside internal/dashboard). It
	// reports whether a child was started or why not.
	spawn func() (dashboard.SpawnResult, error)
	// open hands a loopback URL to the OS browser through the whitelist.
	open func(url string) error
	// pause is the sleep between bounded follow-up reads after a spawn. Tests
	// set a no-op so the bound costs no wall clock.
	pause func(time.Duration)
}

// defaultDashboardSource binds the real loaders, the real clock, and the three
// real actions. The loaders are bounded file reads; refresh resolves the
// dashboard entry and runs it headless through internal/dashboard (which
// appends --html --no-open and refuses --open / --serve); spawn resolves the
// same entry and hands it to the guarded detached start (--serve --no-open);
// open only ever receives a loopback URL this package built.
func defaultDashboardSource() dashboardSource {
	return dashboardSource{
		loadServer:  dashboard.LoadServerRecord,
		loadPayload: dashboard.LoadPayload,
		now:         time.Now,
		refresh:     defaultDashboardRefresh,
		spawn:       defaultDashboardSpawn,
		open:        defaultDashboardOpen,
		pause:       time.Sleep,
	}
}

// defaultDashboardRefresh is the production refresh: resolve the entry
// (env override -> PATH -> node + tools root) and run one bounded headless
// refresh. The artifacts on disk are the only output that matters; the caller
// reloads them.
func defaultDashboardRefresh(ctx context.Context) error {
	entry, err := dashboard.ResolveEntry()
	if err != nil {
		return err
	}
	_, err = dashboard.Refresh(ctx, entry.Argv, dashboard.RefreshTimeout)
	return err
}

// defaultDashboardSpawn is the production spawn: resolve the same entry and ask
// internal/dashboard for one guarded detached start. Go never waits for the
// child and never owns its lifecycle; the port file is the only answer that
// matters, and the caller re-reads it.
func defaultDashboardSpawn() (dashboard.SpawnResult, error) {
	entry, err := dashboard.ResolveEntry()
	if err != nil {
		return dashboard.SpawnResult{}, err
	}
	return dashboard.StartServer(entry, dashboard.PreferredPort)
}

// defaultDashboardOpen is the production browser open. The URL is validated
// inside internal/dashboard against the loopback whitelist, so a
// non-loopback target is refused before any process exists.
func defaultDashboardOpen(url string) error { return dashboard.OpenInBrowser(url) }

// dashboardAction is one row of the action list. reason is empty for a live row
// and carries the named reason a disabled row renders and reports; it is
// recomputed per frame because the Open row's availability depends on the
// composed snapshot (a port has to be known).
type dashboardAction struct {
	id     string
	label  string
	reason string
}

// dashboardScreen is P5-2 S2+S4+S5: the status screen for the Node dashboard.
// It composes from the artifact loaders (bounded file I/O) and renders a
// snapshot; the three live actions each touch the outside world once, on an
// explicit Enter and nowhere else:
//
//   - Refresh runs one bounded headless Node one-shot (--html --no-open);
//   - Start asks internal/dashboard for one guarded detached spawn
//     (--serve --no-open) and then re-reads the port file with a bounded poll;
//   - Open hands the loopback URL to the OS browser through a whitelist.
//
// Nothing here probes, spawns or opens during construction or paint: compose()
// and syncRows() only read what is already on disk.
//
// The numbers rule is the whole point of the screen: figures render ONLY for
// dashboard.StatusOK. The stale and missing states render a sentence, because
// a snapshot the version gate rejected must never be shown as if it were
// current truth (REQ-240).
type dashboardScreen struct {
	src     dashboardSource
	b       *i18n.Bundle
	info    []string
	actions []dashboardAction
	rows    []Row
	list    *stepList
	app     *App
	// rec / rstatus are the snapshot compose() captured, so a status message can
	// reuse the exact "Server up..." wording and the Open row can gate on a known
	// port without touching disk.
	rec     dashboard.ServerRecord
	rstatus dashboard.Status
}

// NewDashboardScreen builds the production screen over the real dashboard
// artifacts and the real clock.
func NewDashboardScreen() Screen { return newDashboardScreen(defaultDashboardSource()) }

// newDashboardScreen builds the screen over an injected seam. Nil fields fall
// back to the production loaders/actions, so a test can override only the part
// it cares about.
func newDashboardScreen(src dashboardSource) Screen {
	def := defaultDashboardSource()
	if src.loadServer == nil {
		src.loadServer = def.loadServer
	}
	if src.loadPayload == nil {
		src.loadPayload = def.loadPayload
	}
	if src.now == nil {
		src.now = def.now
	}
	if src.refresh == nil {
		src.refresh = def.refresh
	}
	if src.spawn == nil {
		src.spawn = def.spawn
	}
	if src.open == nil {
		src.open = def.open
	}
	if src.pause == nil {
		src.pause = def.pause
	}
	s := &dashboardScreen{src: src, b: Bundle()}
	// All three actions are live: no row carries a "comes with the next build"
	// reason any more. Open is the only row that can be disabled, and only while
	// no server port is known (see syncRows).
	s.actions = []dashboardAction{
		{id: dashIDStart, label: missingOr(s.b, "dashboard.action.start", missingDashboardActionStart)},
		{id: dashIDRefresh, label: missingOr(s.b, "dashboard.action.refresh", missingDashboardActionRefresh)},
		{id: dashIDOpen, label: missingOr(s.b, "dashboard.action.open", missingDashboardActionOpen)},
		{id: dashIDBack, label: s.b.T("common.back")},
	}
	s.rows = make([]Row, len(s.actions))
	for i, act := range s.actions {
		s.rows[i] = Row{Label: act.label, ID: act.id, Note: act.reason}
	}
	s.list = newStepList(s.activate)
	s.list.SetRows(s.rows)
	s.compose()
	s.syncRows()
	return s
}

// syncRows recomputes the row notes from the last composed snapshot. The Open
// row is live exactly when a usable port is known; otherwise it names the
// reason. It performs no I/O (paint must never probe, spawn or open), and it
// leaves the list untouched when nothing changed, so a repaint cannot disturb
// the cursor or an in-flight key sequence.
func (s *dashboardScreen) syncRows() {
	reason := ""
	if s.port() == 0 {
		reason = missingOr(s.b, "dashboard.reason.open", missingDashboardReasonOpen)
	}
	changed := len(s.rows) != len(s.actions)
	for i := range s.actions {
		if s.actions[i].id == dashIDOpen && s.actions[i].reason != reason {
			s.actions[i].reason = reason
			changed = true
		}
		if i < len(s.rows) && s.rows[i].Note != s.actions[i].reason {
			changed = true
		}
	}
	if !changed {
		return
	}
	rows := make([]Row, len(s.actions))
	for i, act := range s.actions {
		rows[i] = Row{Label: act.label, ID: act.id, Note: act.reason}
	}
	s.rows = rows
	cursor := s.list.Cursor()
	s.list.SetRows(s.rows)
	s.list.SetCursor(cursor)
}

// compose snapshots both artifacts once. Server and payload are independent:
// a live server with a stale snapshot, or a snapshot with no server, both
// render honestly instead of collapsing into one "everything is fine" line.
func (s *dashboardScreen) compose() {
	rec, rstatus := s.src.loadServer()
	payload, pstatus := s.src.loadPayload()
	s.rec, s.rstatus = rec, rstatus

	var info []string
	if s.port() != 0 {
		info = append(info, s.serverLine())
		if url := dashboardLoopbackURL(rec.Port); url != "" {
			// The URL is assembled from a constant loopback host and a
			// validated integer port, then masked anyway so the render path
			// stays uniform: every artifact-derived string is masked.
			info = append(info, fillHubSlots(missingOr(s.b, "dashboard.url", missingDashboardURL), map[string]string{
				"url": providers.MaskOperand(url),
			}))
		}
	} else {
		info = append(info, missingOr(s.b, "dashboard.server.none", missingDashboardServerNone))
	}
	info = append(info, s.payloadLines(payload, pstatus)...)
	s.info = info
}

// port reports the server port this screen may act on, or 0 when none is known.
//
// A STALE record still carries a real port: REQ-240 only says the payload
// contract is old, never that nothing is listening (S1 report note 13, and
// Node never deletes the record). The screen therefore renders the server line
// and allows Open, while the payload block independently says the data is
// stale - which is the honest pair. Missing means no port at all.
func (s *dashboardScreen) port() int {
	if s.rstatus == dashboard.StatusMissing || !dashboard.ValidPort(s.rec.Port) {
		return 0
	}
	return s.rec.Port
}

// serverLine renders the "server up" line from the captured snapshot, so a
// status message can reuse the exact wording the info block shows.
func (s *dashboardScreen) serverLine() string {
	if s.rstatus == dashboard.StatusMissing {
		return missingOr(s.b, "dashboard.server.none", missingDashboardServerNone)
	}
	return s.serverLineFor(s.rec)
}

// serverLineFor renders the "server up" line for one record. startedAt is an
// artifact string, so it passes the operand masker before it can reach a frame
// (the same discipline screen_doctor applies to error details).
func (s *dashboardScreen) serverLineFor(rec dashboard.ServerRecord) string {
	return fillHubSlots(missingOr(s.b, "dashboard.server.up", missingDashboardServerUp), map[string]string{
		"port":  strconv.Itoa(rec.Port),
		"since": providers.MaskOperand(rec.StartedAt),
	})
}

// payloadLines renders the snapshot block: status first, numbers only when the
// version gate passed. dashboard.LoadPayload zeroes the payload for every
// non-OK status, so this function cannot leak a stale figure even by mistake;
// the status switch makes the intent explicit at the render site.
func (s *dashboardScreen) payloadLines(p dashboard.Payload, status dashboard.Status) []string {
	switch status {
	case dashboard.StatusOK:
		out := []string{s.dataOKLine(p)}
		out = append(out,
			s.periodRow(missingOr(s.b, "dashboard.period.today", missingDashboardToday), p.Summaries.Today),
			s.periodRow(missingOr(s.b, "dashboard.period.last7d", missingDashboardLast7d), p.Summaries.Last7d),
		)
		if models := topDashboardModels(p.Models, dashboardTopModels); len(models) > 0 {
			out = append(out, missingOr(s.b, "dashboard.models.header", missingDashboardModelsHeader))
			for i, m := range models {
				out = append(out, s.modelRow(i+1, m))
			}
		}
		return out
	case dashboard.StatusStale:
		return []string{missingOr(s.b, "dashboard.data.stale", missingDashboardDataStale)}
	default:
		return []string{missingOr(s.b, "dashboard.data.missing", missingDashboardDataMissing)}
	}
}

// dataOKLine renders the status line plus the age derived from generatedAt.
// The age is recomputed from the timestamp on every compose: the Node side
// bakes its freshness flags at build time (payload geminiQuota.isFresh), so a
// stored flag is not evidence of age now.
func (s *dashboardScreen) dataOKLine(p dashboard.Payload) string {
	line := missingOr(s.b, "dashboard.data.ok", missingDashboardDataOK)
	at := strings.TrimSpace(p.GeneratedAt)
	if at == "" {
		return line
	}
	age := missingOr(s.b, "dashboard.data.age_unknown", missingDashboardAgeUnknown)
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		age = formatDashboardAge(s.src.now().Sub(t))
	}
	return line + " · " + fillHubSlots(missingOr(s.b, "dashboard.data.generated", missingDashboardDataGenerated), map[string]string{
		"at":  providers.MaskOperand(at),
		"age": age,
	})
}

// periodRow renders one period summary: cost, total tokens, session count.
func (s *dashboardScreen) periodRow(label string, sum dashboard.Summary) string {
	return fillHubSlots(missingOr(s.b, "dashboard.period.row", missingDashboardPeriodRow), map[string]string{
		"period":   label,
		"cost":     formatDashboardCost(sum.CostUsd),
		"tokens":   formatDashboardTokens(sum.TotalTokens),
		"sessions": s.sessionCount(sum.TotalSessions),
	})
}

// modelRow renders one per-model aggregate. The model name is a registry-slot
// shaped artifact string, so it routes through providers.MaskSlot: a
// key-shaped model id renders first-5+**** instead of raw.
func (s *dashboardScreen) modelRow(rank int, m dashboard.ModelRow) string {
	return fillHubSlots(missingOr(s.b, "dashboard.models.row", missingDashboardModelsRow), map[string]string{
		"rank":   strconv.Itoa(rank),
		"model":  providers.MaskSlot(m.Model),
		"cost":   formatDashboardCost(m.CostUsd),
		"tokens": formatDashboardTokens(m.TotalTokens),
	})
}

// sessionCount renders the session count through the catalog plural, falling
// back to the local stand-in pair while the catalogs lack the keys.
func (s *dashboardScreen) sessionCount(n int) string {
	key, fallback := "dashboard.count.sessions.other", missingDashboardSessionsOther
	if n == 1 {
		key, fallback = "dashboard.count.sessions.one", missingDashboardSessionsOne
	}
	return fillHubSlots(missingOr(s.b, key, fallback), map[string]string{"n": strconv.Itoa(n)})
}

// Title renders the screen title through the catalog, with the local stand-in
// while the catalog lacks the key.
func (s *dashboardScreen) Title() string {
	return missingOr(s.b, "dashboard.title", missingDashboardTitle)
}

// Mode is ModeNav: the screen selects rows, it never edits.
func (s *dashboardScreen) Mode() Mode { return ModeNav }

// Draw renders the composed snapshot, a blank line, the numbered action rows,
// and the shared list hint. Every line goes through cellwidth so a CJK model
// name cannot push a line past the frame width. syncRows is a note recompute
// over the last snapshot only: paint never probes, spawns or opens anything.
func (s *dashboardScreen) Draw(a *App) []string {
	s.syncRows()
	w := a.Width()
	lines := make([]string, 0, len(s.info)+6)
	for _, l := range s.info {
		lines = append(lines, cellwidth.Truncate(l, w))
	}
	lines = append(lines, "")
	lines = append(lines, s.list.Render(w)...)
	return append(lines, "", s.list.FooterHint())
}

// HandleKey delegates to the shared stepList, so digits and arrows+Enter are
// both live (the dual-navigation requirement). Esc never arrives here: the App
// precedence pops the screen first.
func (s *dashboardScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// activate is the stepList onPick callback. Back pops the screen; Start,
// Refresh and Open each run their one action; a row whose precondition is
// missing (Open with no known port) runs nothing and names its reason on the
// message line, so no key is dead and nothing is silently invented.
func (s *dashboardScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(s.actions) {
		return
	}
	act := s.actions[idx]
	switch act.id {
	case dashIDBack:
		s.app.Pop()
		return
	case dashIDRefresh:
		s.refresh()
		return
	case dashIDStart:
		s.start()
		return
	case dashIDOpen:
		s.openBrowser()
		return
	}
	s.app.SetMessage(fillHubSlots(missingOr(s.b, "dashboard.msg.disabled", missingDashboardMsgDisabled), map[string]string{
		"action": act.label,
		"reason": act.reason,
	}))
}

// refresh runs one bounded one-shot exec and reloads the artifacts through the
// S1 loaders. It is synchronous on purpose (D3: the key loop is a sync loop,
// no goroutine): the run is capped at dashboard.RefreshTimeout (<=3s), so the
// worst case is one bounded stall, not a wedged screen.
//
// Status reporting goes to the App message line, the same transient channel
// every other screen uses. "refreshing..." is set BEFORE the exec, so the
// running state is the live message for the whole bounded call; the outcome
// replaces it. Failure never changes the rendered snapshot: a half-written
// artifact is not a second data source, and the previous snapshot plus a named
// error is the honest pair.
func (s *dashboardScreen) refresh() {
	s.setMessage(missingOr(s.b, "dashboard.refresh.running", missingDashboardRefreshing))

	ctx, cancel := context.WithTimeout(context.Background(), dashboard.RefreshTimeout)
	defer cancel()
	if err := s.src.refresh(ctx); err != nil {
		// The error text can carry a resolved path or a child's own stderr, so
		// it passes the operand masker before it can reach a frame.
		s.setMessage(fillHubSlots(missingOr(s.b, "dashboard.refresh.failed", missingDashboardRefreshFailed), map[string]string{
			"err": providers.MaskOperand(err.Error()),
		}))
		return
	}
	s.compose()
	s.setMessage(missingOr(s.b, "dashboard.refresh.ok", missingDashboardRefreshOK))
}

// setMessage writes the transient status line; activate() is a no-op without
// an App (the seam tests drive activate directly).
func (s *dashboardScreen) setMessage(msg string) {
	if s.app != nil {
		s.app.SetMessage(msg)
	}
}

// start runs one guarded detached spawn and then reports what happened.
//
// It is synchronous on purpose (D3: the key loop is a sync loop, no goroutine).
// The spawn itself returns as soon as the OS accepted the exec, so the only
// blocking part is the bounded follow-up read of the port file
// (startPollAttempts x startPollInterval = 3s worst case). That read happens
// ONLY here, inside an explicit keypress: a paint never touches it.
//
// Four outcomes, each with its own honest line:
//
//   - the guard found a server already answering (SpawnSkipRunning): nothing was
//     started, and the message is the exact "Server up..." line the info block
//     shows;
//   - the guard found a fresh spawn intent (SpawnSkipInFlight): a sibling
//     dashboard link (the statusline hook) is starting one inside the 15s
//     window, so nothing is started and the "starting..." line stays;
//   - a child was started and the port file appeared: the message is the
//     "Server up..." line of the record that is now on disk;
//   - a child was started but no record appeared inside the bound: the honest
//     "no port recorded yet" line, with Refresh as the next step.
func (s *dashboardScreen) start() {
	s.setMessage(missingOr(s.b, "dashboard.start.running", missingDashboardStarting))

	res, err := s.src.spawn()
	if err != nil {
		// Same masking discipline as the refresh failure: the error can carry a
		// resolved path or the child's own words.
		s.setMessage(fillHubSlots(missingOr(s.b, "dashboard.start.failed", missingDashboardStartFailed), map[string]string{
			"err": providers.MaskOperand(err.Error()),
		}))
		return
	}
	switch res.Skip {
	case dashboard.SpawnSkipRunning:
		// The guard's own record is authoritative for the message: the loader's
		// snapshot may be older than the record StartServer just probed.
		s.compose()
		if dashboard.ValidPort(res.Record.Port) {
			s.setMessage(s.serverLineFor(res.Record))
		} else {
			s.setMessage(s.serverLine())
		}
		return
	case dashboard.SpawnSkipInFlight:
		s.compose()
		s.setMessage(missingOr(s.b, "dashboard.start.running", missingDashboardStarting))
		return
	}

	if _, ok := s.awaitServer(); ok {
		s.compose()
		s.setMessage(s.serverLine())
		return
	}
	s.compose()
	s.setMessage(missingOr(s.b, "dashboard.start.waiting", missingDashboardWaiting))
}

// startPollAttempts and startPollInterval bound the follow-up read after a
// spawn: 30 x 100ms = 3s, the same order as the one-shot refresh bound. A cold
// Node start that has not published its port by then degrades to the "no port
// recorded yet" line instead of freezing the key loop any longer.
const (
	startPollAttempts = 30
	startPollInterval = 100 * time.Millisecond
)

// awaitServer re-reads the port file up to the bound. Any non-missing record
// counts as "the file is there": a stale record still proves a server bound a
// port (the payload line reports the contract mismatch separately).
func (s *dashboardScreen) awaitServer() (dashboard.ServerRecord, bool) {
	for i := 0; i < startPollAttempts; i++ {
		if rec, status := s.src.loadServer(); status != dashboard.StatusMissing {
			return rec, true
		}
		if i+1 < startPollAttempts {
			s.src.pause(startPollInterval)
		}
	}
	return dashboard.ServerRecord{}, false
}

// openBrowser hands the loopback URL to the OS opener. It only runs when a
// usable port is known; the URL is built here (constant host, validated port)
// and re-validated inside internal/dashboard before any process exists.
func (s *dashboardScreen) openBrowser() {
	port := s.port()
	url := dashboardLoopbackURL(port)
	if port == 0 || url == "" {
		s.setMessage(missingOr(s.b, "dashboard.reason.open", missingDashboardReasonOpen))
		return
	}
	if err := s.src.open(url); err != nil {
		s.setMessage(fillHubSlots(missingOr(s.b, "dashboard.open.failed", missingDashboardOpenFailed), map[string]string{
			"err": providers.MaskOperand(err.Error()),
		}))
		return
	}
	s.setMessage(fillHubSlots(missingOr(s.b, "dashboard.open.ok", missingDashboardOpenOK), map[string]string{
		"url": providers.MaskOperand(url),
	}))
}

// topDashboardModels returns the first n models by cost, highest first. The
// Node payload already arrives sorted by costUsd descending, but the copy is
// sorted here so a producer reorder cannot change what "top" means in the TUI.
func topDashboardModels(models []dashboard.ModelRow, n int) []dashboard.ModelRow {
	if len(models) == 0 || n < 1 {
		return nil
	}
	sorted := make([]dashboard.ModelRow, len(models))
	copy(sorted, models)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CostUsd > sorted[j].CostUsd })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

// dashboardLoopbackURL builds the loopback URL the Open row hands to the OS
// opener. It returns "" for a port the dashboard package would have rejected, so
// the render path never prints a URL it cannot open.
func dashboardLoopbackURL(port int) string {
	if !dashboard.ValidPort(port) {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/"
}

// formatDashboardAge renders an age compactly for the status line.
func formatDashboardAge(d time.Duration) string {
	if d < 0 {
		// A future stamp means clock skew, not a negative age.
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// formatDashboardCost mirrors the Node money format (src/formatter.js
// formatCurrency): four decimals below one cent so a cheap day is not shown as
// $0.000, three decimals above it. The payload carries a currency code the S1
// subset does not decode, so this view states USD and leaves conversion to the
// browser surface.
func formatDashboardCost(usd float64) string {
	if usd < 0 {
		usd = 0
	}
	if usd > 0 && usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.3f", usd)
}

// formatDashboardTokens mirrors the Node compact number format
// (src/formatter.js formatCompact): 1.2k, 45.8k, 1.45M.
func formatDashboardTokens(n int64) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1000000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.2fM", float64(n)/1000000)
	}
}
