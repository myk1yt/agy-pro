package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agypro/internal/dashboard"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

// dashboardTestNow is the fixed clock the age line derives from, so the
// rendered age is a stable assertion rather than a moving target.
var dashboardTestNow = time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)

// dashboardTestServer is the live-shape port record the seam returns for the
// "server up" cases.
func dashboardTestServer() dashboard.ServerRecord {
	return dashboard.ServerRecord{
		Port:           8787,
		PID:            41204,
		StartedAt:      "2026-10-22T17:30:00.000Z",
		PayloadVersion: dashboard.DashboardPayloadVersion,
	}
}

// dashboardTestPayload is a v3 snapshot with two period summaries and three
// model rows, generated 90 seconds before the test clock.
func dashboardTestPayload() dashboard.Payload {
	return dashboard.Payload{
		Version:     dashboard.DashboardPayloadVersion,
		GeneratedAt: dashboardTestNow.Add(-90 * time.Second).Format(time.RFC3339),
		Summaries: dashboard.Summaries{
			Today:  dashboard.Summary{TotalSessions: 12, TotalTokens: 243000, CostUsd: 0.4231},
			Last7d: dashboard.Summary{TotalSessions: 41, TotalTokens: 1250000, CostUsd: 3.104},
		},
		Models: []dashboard.ModelRow{
			{Model: "model-alpha", TotalTokens: 120000, CostUsd: 0.2, Sessions: 7},
			{Model: "model-beta", TotalTokens: 40000, CostUsd: 0.08, Sessions: 3},
			{Model: "model-gamma", TotalTokens: 9000, CostUsd: 0.02, Sessions: 2},
		},
	}
}

// dashboardTestSource assembles a seam over fixed responses. A nil loader is
// replaced by the constructor's production default, so every caller here always
// passes both.
//
// The three action seams are stubbed here for a safety reason, not just for
// determinism: the constructor fills a nil spawn/open with the PRODUCTION ones,
// so a test that pressed the Start row without a stub would spawn a real
// detached Node server, and one that pressed Open would launch a real browser.
func dashboardTestSource(rec dashboard.ServerRecord, rs dashboard.Status, p dashboard.Payload, ps dashboard.Status) dashboardSource {
	return dashboardSource{
		loadServer:  func() (dashboard.ServerRecord, dashboard.Status) { return rec, rs },
		loadPayload: func() (dashboard.Payload, dashboard.Status) { return p, ps },
		now:         func() time.Time { return dashboardTestNow },
		spawn:       func() (dashboard.SpawnResult, error) { return dashboard.SpawnResult{Spawned: true}, nil },
		open:        func(string) error { return nil },
		pause:       func(time.Duration) {},
	}
}

// dashboardTestFrame renders the screen as the operator sees it: pushed as the
// top screen of an 80x24 app, composed without a banner.
func dashboardTestFrame(t *testing.T, scr Screen) string {
	t.Helper()
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	return app.Frame().String()
}

// TestDashboardStatusRenders is the table-driven seam matrix: every
// server/payload status pair renders its own honest line, and the numbers
// appear for exactly the one combination whose payload cleared the version
// gate. A non-OK payload must not leak a figure (no "$", no "tokens" line).
func TestDashboardStatusRenders(t *testing.T) {
	cases := []struct {
		name       string
		server     dashboard.ServerRecord
		serverSt   dashboard.Status
		payload    dashboard.Payload
		payloadSt  dashboard.Status
		want       []string
		wantAbsent []string
	}{
		{
			name: "server ok payload ok", server: dashboardTestServer(), serverSt: dashboard.StatusOK,
			payload: dashboardTestPayload(), payloadSt: dashboard.StatusOK,
			want: []string{
				"Server up on port 8787 since 2026-10-22T17:30:00.000Z",
				"URL: http://127.0.0.1:8787/",
				"Data OK",
				"age 1m",
				"Today: $0.423 · 243.0k tokens · 12 sessions",
				"Last 7d: $3.104 · 1.25M tokens · 41 sessions",
				"Top models by cost",
				"  1. model-alpha · $0.200 · 120.0k tokens",
				"  2. model-beta · $0.080 · 40.0k tokens",
				"  3. model-gamma · $0.020 · 9.0k tokens",
			},
		},
		{
			name: "server ok payload stale", server: dashboardTestServer(), serverSt: dashboard.StatusOK,
			payload: dashboard.Payload{}, payloadSt: dashboard.StatusStale,
			want:       []string{"Server up on port 8787", "URL: http://127.0.0.1:8787/", missingDashboardDataStale},
			wantAbsent: []string{"Data OK", "$", "tokens", "Top models"},
		},
		{
			name: "server ok payload missing", server: dashboard.ServerRecord{}, serverSt: dashboard.StatusMissing,
			payload: dashboard.Payload{}, payloadSt: dashboard.StatusMissing,
			want:       []string{missingDashboardServerNone, missingDashboardDataMissing},
			wantAbsent: []string{"Server up", "http://", "$", "tokens"},
		},
		{
			name: "server missing payload ok", server: dashboard.ServerRecord{}, serverSt: dashboard.StatusMissing,
			payload: dashboardTestPayload(), payloadSt: dashboard.StatusOK,
			want:       []string{missingDashboardServerNone, "Data OK", "Today: $0.423"},
			wantAbsent: []string{"Server up", "http://"},
		},
		{
			name: "server stale payload ok", server: dashboard.ServerRecord{Port: 8787, StartedAt: "2026-10-22T17:30:00.000Z", PayloadVersion: 2}, serverSt: dashboard.StatusStale,
			payload: dashboardTestPayload(), payloadSt: dashboard.StatusOK,
			// A stale record still carries a REAL port (REQ-240 gates the payload
			// contract, never the socket), so the server line and the URL render
			// and only the payload block reports the mismatch. This is the S5
			// reinterpretation the S4 handoff asked for.
			want:       []string{"Server up on port 8787", "URL: http://127.0.0.1:8787/", "Data OK"},
			wantAbsent: []string{missingDashboardServerNone},
		},
		{
			name: "server stale payload stale", server: dashboard.ServerRecord{Port: 8787}, serverSt: dashboard.StatusStale,
			payload: dashboard.Payload{}, payloadSt: dashboard.StatusStale,
			want:       []string{"Server up on port 8787", missingDashboardDataStale},
			wantAbsent: []string{missingDashboardServerNone, "$", "Today", "Top models"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(tc.server, tc.serverSt, tc.payload, tc.payloadSt)))
			for _, want := range tc.want {
				if !strings.Contains(frame, want) {
					t.Fatalf("frame missing %q:\n%s", want, frame)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(frame, absent) {
					t.Fatalf("frame carries %q where the status forbids it:\n%s", absent, frame)
				}
			}
		})
	}
}

// TestDashboardStalePayloadNeverRendersNumbers is the contract-pin test the S1
// report calls out: the loader zeroes a stale payload, but the screen must not
// depend on that. A loader that hands over a fully populated payload tagged
// StatusStale still renders no figure, because the render site switches on the
// status before it reads a field.
func TestDashboardStalePayloadNeverRendersNumbers(t *testing.T) {
	poisoned := dashboardTestPayload()
	frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, poisoned, dashboard.StatusStale)))
	for _, absent := range []string{"$", "tokens", "Today", "Last 7d", "Top models", "243.0k", "model-alpha"} {
		if strings.Contains(frame, absent) {
			t.Fatalf("stale payload leaked %q into the frame:\n%s", absent, frame)
		}
	}
	if !strings.Contains(frame, missingDashboardDataStale) {
		t.Fatalf("stale payload did not render its status line:\n%s", frame)
	}
}

// TestDashboardFrameBudget pins the compact-view budget at the smallest
// supported geometry: the whole snapshot, all four action rows, and the list
// hint fit an 80x24 frame including the three-line console banner, so no row
// is silently cut from the first frame of the screen.
func TestDashboardFrameBudget(t *testing.T) {
	scr := newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK))
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)

	if got := len(scr.Draw(app)); got > 24 {
		t.Fatalf("screen body = %d lines at 80x24, want <= 24", got)
	}
	if got := len(app.Frame().Lines); got > 24 {
		t.Fatalf("composed frame = %d lines at 80x24, want <= 24:\n%s", got, app.Frame().String())
	}
	// The console path carries the banner above the body; the budget must hold
	// there too, because that is the frame the operator actually first sees.
	pf := composeFrame(app, []string{"line1", "line2", "line3"})
	if got := len(pf.frame.Lines); got > 24 {
		t.Fatalf("banner frame = %d lines at 80x24, want <= 24:\n%s", got, pf.frame.String())
	}
	joined := pf.frame.String()
	for _, want := range []string{
		"[ 1] Start server",
		"[ 2] Refresh",
		"[ 3] Open in browser",
		"[ 4] Back",
		"digits 1-9 select+activate",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("frame lost %q:\n%s", want, joined)
		}
	}
}

// TestDashboardActionRowsLive pins the S5 flip: no row carries a "comes with
// the next build" reason any more, Start and Open each run exactly one action,
// and Back still pops. The Open row is live only because the composed snapshot
// carries a valid port.
func TestDashboardActionRowsLive(t *testing.T) {
	spawns, opens := 0, 0
	src := dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK)
	src.spawn = func() (dashboard.SpawnResult, error) {
		spawns++
		return dashboard.SpawnResult{Spawned: true}, nil
	}
	src.open = func(url string) error {
		opens++
		if url != "http://127.0.0.1:8787/" {
			t.Fatalf("the Open row handed the operator %q, want the loopback dashboard URL", url)
		}
		return nil
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	frame := app.Frame().String()
	for _, want := range []string{"[ 1] Start server", "[ 2] Refresh", "[ 3] Open in browser", "[ 4] Back"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q:\n%s", want, frame)
		}
	}
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "[") && strings.Contains(line, "comes with the next build") {
			t.Fatalf("a row still renders the retired disabled reason: %q", line)
		}
	}

	// Start: one press, one spawn, and the status line reports the record that
	// is on disk (the stubbed loader answers with the live one).
	app.Step(Digit(1))
	if spawns != 1 {
		t.Fatalf("the spawn seam ran %d times for one Start press, want 1", spawns)
	}
	if msg := app.Message(); !strings.Contains(msg, "Server up on port 8787") {
		t.Fatalf("start message = %q, want the server-up line", msg)
	}
	if got := app.Depth(); got != 2 {
		t.Fatalf("Start changed the stack depth to %d, want 2", got)
	}

	// Open: one press, one opener call, the loopback URL.
	app.Step(Digit(3))
	if opens != 1 {
		t.Fatalf("the open seam ran %d times for one Open press, want 1", opens)
	}
	if msg := app.Message(); !strings.Contains(msg, "http://127.0.0.1:8787/") {
		t.Fatalf("open message = %q, want the URL it opened", msg)
	}
	if got := app.Depth(); got != 2 {
		t.Fatalf("Open changed the stack depth to %d, want 2", got)
	}

	// Back still pops.
	app.Step(Digit(4))
	if got := app.Depth(); got != 1 {
		t.Fatalf("Back row left stack depth %d, want 1 (it must pop)", got)
	}
	if got := app.Top().Title(); got != "hub" {
		t.Fatalf("after Back the top screen is %q, want the hub", got)
	}
}

// TestDashboardOpenDisabledWithoutPort pins the one condition that can still
// disable a row: with no record there is no port, so Open renders its reason
// and a press runs nothing (and never reaches the opener).
func TestDashboardOpenDisabledWithoutPort(t *testing.T) {
	opens := 0
	src := dashboardTestSource(dashboard.ServerRecord{}, dashboard.StatusMissing, dashboard.Payload{}, dashboard.StatusMissing)
	src.open = func(string) error {
		opens++
		return nil
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	frame := app.Frame().String()
	openRow := ""
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(line, "[ 3] Open in browser") {
			openRow = line
		}
	}
	if openRow == "" {
		t.Fatalf("no Open row in the frame:\n%s", frame)
	}
	if !strings.Contains(openRow, missingOr(i18n.Load("en"), "dashboard.reason.open", missingDashboardReasonOpen)) {
		t.Fatalf("the portless Open row does not name its reason: %q", openRow)
	}
	// Digit 3, and the arrow path with the cursor parked on the Open row.
	app.Step(Digit(3))
	if opens != 0 {
		t.Fatal("digit 3 reached the opener with no port known")
	}
	if got := app.Message(); !strings.Contains(got, "disabled") {
		t.Fatalf("digit 3 message = %q, want the named reason", got)
	}
	app.Step(KeyHome)
	app.Step(KeyDown)
	app.Step(KeyDown)
	app.Step(KeyEnter)
	if opens != 0 {
		t.Fatal("Enter on the Open row reached the opener with no port known")
	}
	if got := app.Message(); !strings.Contains(got, "disabled") {
		t.Fatalf("Enter message = %q, want the named reason", got)
	}
	if got := app.Depth(); got != 2 {
		t.Fatalf("the portless Open row changed the stack depth to %d, want 2", got)
	}
}

// TestDashboardNeverSpawnsOrOpensOnPaint is the construction/paint guarantee:
// composing, drawing frames repeatedly, and refreshing the row notes must not
// reach the spawn seam, the opener seam, or the refresh seam.
func TestDashboardNeverSpawnsOrOpensOnPaint(t *testing.T) {
	spawns, opens, refreshes := 0, 0, 0
	src := dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK)
	src.spawn = func() (dashboard.SpawnResult, error) {
		spawns++
		return dashboard.SpawnResult{}, nil
	}
	src.open = func(string) error {
		opens++
		return nil
	}
	src.refresh = func(ctx context.Context) error {
		refreshes++
		return nil
	}
	scr := newDashboardScreen(src).(*dashboardScreen)
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	for i := 0; i < 5; i++ {
		app.Frame()
	}
	scr.compose()
	scr.syncRows()
	if spawns != 0 || opens != 0 || refreshes != 0 {
		t.Fatalf("paint reached an action seam: spawns=%d opens=%d refreshes=%d", spawns, opens, refreshes)
	}
}

// TestDashboardStartSkipOutcomes pins the two guarded no-spawn outcomes with
// their own honest lines: a server already answering, and a fresh spawn intent
// from a sibling dashboard link.
func TestDashboardStartSkipOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		result  dashboard.SpawnResult
		want    string
		absents []string
	}{
		{
			name:   "already running",
			result: dashboard.SpawnResult{Skip: dashboard.SpawnSkipRunning, Record: dashboardTestServer()},
			want:   "Server up on port 8787",
		},
		{
			name:   "spawn in flight",
			result: dashboard.SpawnResult{Skip: dashboard.SpawnSkipInFlight},
			want:   missingOr(i18n.Load("en"), "dashboard.start.running", missingDashboardStarting),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spawns := 0
			src := dashboardTestSource(dashboard.ServerRecord{}, dashboard.StatusMissing, dashboard.Payload{}, dashboard.StatusMissing)
			src.spawn = func() (dashboard.SpawnResult, error) {
				spawns++
				return tc.result, nil
			}
			var app *App
			app = NewApp(80, 24, DirLTR)
			app.Push(&stubScreen{title: "hub"})
			app.Push(newDashboardScreen(src))
			app.Step(Digit(1))
			if spawns != 1 {
				t.Fatalf("the spawn seam ran %d times, want 1", spawns)
			}
			if msg := app.Message(); !strings.Contains(msg, tc.want) {
				t.Fatalf("message = %q, want %q", msg, tc.want)
			}
			_ = app
		})
	}
}

// TestDashboardStartFailureIsMaskedAndNamed pins the failure half: a spawn error
// is named, masked, and never replaces the rendered snapshot.
func TestDashboardStartFailureIsMaskedAndNamed(t *testing.T) {
	secret := testfixtures.SKProjMixed
	src := dashboardTestSource(dashboard.ServerRecord{}, dashboard.StatusMissing, dashboardTestPayload(), dashboard.StatusOK)
	src.spawn = func() (dashboard.SpawnResult, error) {
		return dashboard.SpawnResult{}, errors.New("spawn refused near " + secret)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	app.Step(Digit(1))

	msg := app.Message()
	if !strings.Contains(msg, "start failed:") {
		t.Fatalf("message = %q, want the named start failure", msg)
	}
	if strings.Contains(msg, secret) {
		t.Fatalf("the failure line leaks the planted key: %q", msg)
	}
	if !strings.Contains(msg, "spawn refused near") {
		t.Fatalf("message = %q, want the underlying reason", msg)
	}
	if frame := app.Frame().String(); !strings.Contains(frame, "Today: $0.423") {
		t.Fatalf("a failed spawn dropped the last known snapshot:\n%s", frame)
	}
}

// TestDashboardStartPollsUntilThePortAppears pins the bounded follow-up: after a
// spawn the screen re-reads the port file, and the number of reads and pauses is
// bounded (never unbounded, never on paint). A record that never appears
// degrades to the honest "no port recorded yet" line.
func TestDashboardStartPollsUntilThePortAppears(t *testing.T) {
	reads, pauses := 0, 0
	src := dashboardTestSource(dashboard.ServerRecord{}, dashboard.StatusMissing, dashboard.Payload{}, dashboard.StatusMissing)
	src.loadServer = func() (dashboard.ServerRecord, dashboard.Status) {
		reads++
		if reads >= 3 {
			return dashboardTestServer(), dashboard.StatusOK
		}
		return dashboard.ServerRecord{}, dashboard.StatusMissing
	}
	src.pause = func(d time.Duration) {
		pauses++
		if d != startPollInterval {
			t.Fatalf("poll pause = %v, want %v", d, startPollInterval)
		}
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))
	readsAfterCompose := reads

	app.Step(Digit(1))

	// Two poll reads (the second one found the record) plus the recompose the
	// successful start performs.
	if got := reads - readsAfterCompose; got != 3 {
		t.Fatalf("the follow-up read the port file %d times, want 3 (two polls + one recompose)", got)
	}
	if pauses != 1 {
		t.Fatalf("the poll paused %d times, want 1 (one between the retries)", pauses)
	}
	if msg := app.Message(); !strings.Contains(msg, "Server up on port 8787") {
		t.Fatalf("message = %q, want the server-up line after the record appeared", msg)
	}

	// The timeout half: a record that never appears stops at the bound.
	misses, missPauses := 0, 0
	src2 := dashboardTestSource(dashboard.ServerRecord{}, dashboard.StatusMissing, dashboard.Payload{}, dashboard.StatusMissing)
	src2.loadServer = func() (dashboard.ServerRecord, dashboard.Status) {
		misses++
		return dashboard.ServerRecord{}, dashboard.StatusMissing
	}
	src2.pause = func(time.Duration) { missPauses++ }
	app2 := NewApp(80, 24, DirLTR)
	app2.Push(&stubScreen{title: "hub"})
	app2.Push(newDashboardScreen(src2))
	before := misses
	app2.Step(Digit(1))
	// The full poll budget plus the final recompose, and never more: an
	// unbounded loop would hang the key handler instead of degrading.
	if got := misses - before; got != startPollAttempts+1 {
		t.Fatalf("the portless follow-up read %d times, want %d", got, startPollAttempts+1)
	}
	if missPauses != startPollAttempts-1 {
		t.Fatalf("the portless poll paused %d times, want %d", missPauses, startPollAttempts-1)
	}
	if msg := app2.Message(); !strings.Contains(msg, "no port recorded yet") {
		t.Fatalf("message = %q, want the honest waiting line", msg)
	}
}

// TestDashboardOpenRefusalIsReported pins the screen half of the browser
// whitelist: the screen only ever hands a loopback URL to the opener, and a
// refusal that comes back from the opener (file://, an external host) is
// reported masked on the message line instead of being swallowed.
func TestDashboardOpenRefusalIsReported(t *testing.T) {
	var seen []string
	src := dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK)
	src.open = func(url string) error {
		seen = append(seen, url)
		return &dashboard.RefusedURLError{Reason: "only http://127.0.0.1:<port> targets are allowed"}
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	app.Step(Digit(3))

	if len(seen) != 1 {
		t.Fatalf("the opener seam ran %d times, want 1", len(seen))
	}
	if seen[0] != "http://127.0.0.1:8787/" {
		t.Fatalf("the screen built %q, want the loopback dashboard URL", seen[0])
	}
	msg := app.Message()
	if !strings.Contains(msg, "could not open a browser:") {
		t.Fatalf("message = %q, want the named open failure", msg)
	}
	if !strings.Contains(msg, "127.0.0.1") {
		t.Fatalf("message = %q, want the opener's own reason", msg)
	}
}

// TestDashboardProductionSeamsRefuseBeforeTouchingTheWorld pins the production
// wiring at both live doors without executing anything:
//
//   - defaultDashboardSpawn runs the real ResolveEntry ladder, so with no
//     override, no tools root and an empty PATH it returns the typed
//     not-found error instead of spawning anything;
//   - defaultDashboardOpen runs the real whitelist, so a file:// target and an
//     external host are refused before the OS opener is ever built. This is the
//     production path, not a stub: no browser can start from it.
func TestDashboardProductionSeamsRefuseBeforeTouchingTheWorld(t *testing.T) {
	t.Setenv(dashboard.EnvDashboardEntry, "")
	t.Setenv(dashboard.EnvProToolsRoot, "")
	t.Setenv("PATH", t.TempDir())

	if _, err := defaultDashboardSpawn(); err == nil {
		t.Fatal("the production spawn seam returned no error with no resolvable entry")
	}

	for _, target := range []string{
		"file:///C:/Users/x/.gemini/antigravity-dashboard/dashboard.html",
		"http://evil.example/dashboard",
		"http://127.0.0.1:8787@evil.example/",
	} {
		err := defaultDashboardOpen(target)
		if err == nil {
			t.Fatalf("the production open seam accepted %q", target)
		}
		if !strings.Contains(err.Error(), "refused browser target") {
			t.Fatalf("open(%q) error = %q, want the whitelist refusal", target, err.Error())
		}
	}
}

// TestDashboardOpenURLAlwaysLoopback is the whitelist proof at the screen
// boundary: for every record the loaders can produce (ok, stale, boundary
// ports), the URL handed to the opener is the loopback shape and nothing else.
func TestDashboardOpenURLAlwaysLoopback(t *testing.T) {
	cases := []struct {
		name   string
		rec    dashboard.ServerRecord
		status dashboard.Status
		want   string
	}{
		{"ok", dashboardTestServer(), dashboard.StatusOK, "http://127.0.0.1:8787/"},
		{"stale", dashboard.ServerRecord{Port: 8787, PayloadVersion: 2}, dashboard.StatusStale, "http://127.0.0.1:8787/"},
		{"boundary low", dashboard.ServerRecord{Port: 1}, dashboard.StatusOK, "http://127.0.0.1:1/"},
		{"boundary high", dashboard.ServerRecord{Port: 65535}, dashboard.StatusOK, "http://127.0.0.1:65535/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			src := dashboardTestSource(tc.rec, tc.status, dashboard.Payload{}, dashboard.StatusMissing)
			src.open = func(url string) error {
				got = url
				return nil
			}
			app := NewApp(80, 24, DirLTR)
			app.Push(newDashboardScreen(src))
			app.Step(Digit(3))
			if got != tc.want {
				t.Fatalf("opener received %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDashboardRefreshReloadsSnapshot is the S4 acceptance test for the live
// row: pressing Refresh runs the bounded seam once, the "refreshing" status is
// the message for the whole call, and a successful run recomposes so the frame
// shows the numbers that are now on disk.
func TestDashboardRefreshReloadsSnapshot(t *testing.T) {
	payload := dashboard.Payload{}
	status := dashboard.StatusMissing
	loads := 0
	var app *App
	var running, deadlineOK bool

	src := dashboardSource{
		loadServer: func() (dashboard.ServerRecord, dashboard.Status) { return dashboardTestServer(), dashboard.StatusOK },
		loadPayload: func() (dashboard.Payload, dashboard.Status) {
			loads++
			return payload, status
		},
		now: func() time.Time { return dashboardTestNow },
		refresh: func(ctx context.Context) error {
			running = app.Message() == missingOr(i18n.Load("en"), "dashboard.refresh.running", missingDashboardRefreshing)
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= dashboard.RefreshTimeout {
				deadlineOK = true
			}
			payload = dashboardTestPayload()
			status = dashboard.StatusOK
			return nil
		},
	}

	app = NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	if frame := app.Frame().String(); !strings.Contains(frame, missingDashboardDataMissing) {
		t.Fatalf("initial frame does not show the empty snapshot:\n%s", frame)
	}
	if loads != 1 {
		t.Fatalf("construction read the payload %d times, want 1", loads)
	}

	app.Step(Digit(2))

	if !running {
		t.Fatalf("the message during the exec was %q, want %q", app.Message(),
			missingOr(i18n.Load("en"), "dashboard.refresh.running", missingDashboardRefreshing))
	}
	if !deadlineOK {
		t.Fatal("the refresh seam received a context without a bounded deadline")
	}
	if got := app.Depth(); got != 2 {
		t.Fatalf("Refresh changed the stack depth to %d, want 2 (it must stay on the screen)", got)
	}
	if loads != 2 {
		t.Fatalf("the payload was read %d times, want 2 (one compose per refresh)", loads)
	}
	msg := app.Message()
	if want := missingOr(i18n.Load("en"), "dashboard.refresh.ok", missingDashboardRefreshOK); msg != want {
		t.Fatalf("message = %q, want %q", msg, want)
	}
	frame := app.Frame().String()
	for _, want := range []string{"Data OK", "Today: $0.423 · 243.0k tokens · 12 sessions"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("the refreshed frame is missing %q:\n%s", want, frame)
		}
	}
}

// TestDashboardRefreshFailureNamesMaskedError pins the failure half: the
// snapshot is NOT replaced by a half-written artifact, and the error line is
// named and masked (a key-shaped operand in the child's own error text must not
// reach the frame).
func TestDashboardRefreshFailureNamesMaskedError(t *testing.T) {
	secret := testfixtures.SKProjMixed
	src := dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK)
	src.refresh = func(ctx context.Context) error {
		return errors.New("child failed near " + secret)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "hub"})
	app.Push(newDashboardScreen(src))

	app.Step(Digit(2))

	msg := app.Message()
	if !strings.Contains(msg, "refresh failed:") {
		t.Fatalf("message = %q, want the named failure line", msg)
	}
	if strings.Contains(msg, secret) {
		t.Fatalf("the failure line leaks the planted key: %q", msg)
	}
	if !strings.Contains(msg, "child failed near") {
		t.Fatalf("message = %q, want the child's own (masked) reason", msg)
	}
	// The rendered snapshot is unchanged: the previous numbers still stand.
	frame := app.Frame().String()
	if !strings.Contains(frame, "Today: $0.423") {
		t.Fatalf("a failed refresh dropped the last known snapshot:\n%s", frame)
	}
	if got := app.Depth(); got != 2 {
		t.Fatalf("a failed refresh changed the stack depth to %d, want 2", got)
	}
}

// TestDashboardRefreshSeamDefaultsToProduction pins the wiring S4 added: a
// screen built with only loaders injected still has a refresh seam, and that
// seam is the production one-shot (it is never invoked here).
func TestDashboardRefreshSeamDefaultsToProduction(t *testing.T) {
	scr := newDashboardScreen(dashboardSource{
		loadServer: func() (dashboard.ServerRecord, dashboard.Status) {
			return dashboard.ServerRecord{}, dashboard.StatusMissing
		},
		loadPayload: func() (dashboard.Payload, dashboard.Status) {
			return dashboard.Payload{}, dashboard.StatusMissing
		},
		now: time.Now,
	}).(*dashboardScreen)
	if scr.src.refresh == nil {
		t.Fatal("the dashboard screen has no refresh seam: the live row would panic")
	}
	def := defaultDashboardSource()
	if def.refresh == nil {
		t.Fatal("the production source has no refresh seam")
	}
	if NewDashboardScreen() == nil {
		t.Fatal("NewDashboardScreen returned nil")
	}
}

// TestDashboardMasksArtifactStrings pins the D3 registry-slot discipline: the
// model name row is an artifact-derived string, so a key-shaped value renders
// masked, and the startedAt stamp goes through the operand masker. The planted
// value comes from internal/testfixtures (built by concatenation there), so this
// file spells no contiguous provider key shape.
func TestDashboardMasksArtifactStrings(t *testing.T) {
	secret := testfixtures.SKProjMixed
	p := dashboardTestPayload()
	p.Models = []dashboard.ModelRow{{Model: secret, TotalTokens: 1000, CostUsd: 0.5}}
	frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, p, dashboard.StatusOK)))
	if strings.Contains(frame, secret) {
		t.Fatalf("key-shaped model name rendered raw:\n%s", frame)
	}
	if want := providers.MaskSlot(secret); !strings.Contains(frame, want) {
		t.Fatalf("frame missing the slot-masked model name %q:\n%s", want, frame)
	}
}

// TestDashboardTopModelsByCost pins the ordering promise: the top three rows
// are the three most expensive models even when the payload arrives unsorted,
// and a fourth model never renders.
func TestDashboardTopModelsByCost(t *testing.T) {
	p := dashboardTestPayload()
	p.Models = []dashboard.ModelRow{
		{Model: "cheap", TotalTokens: 1000, CostUsd: 0.01},
		{Model: "pricey", TotalTokens: 9000, CostUsd: 0.9},
		{Model: "middle", TotalTokens: 5000, CostUsd: 0.5},
		{Model: "fourth", TotalTokens: 100, CostUsd: 0.001},
	}
	frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, p, dashboard.StatusOK)))
	if strings.Contains(frame, "fourth") {
		t.Fatalf("frame rendered a fourth model row:\n%s", frame)
	}
	pricey := strings.Index(frame, "1. pricey")
	middle := strings.Index(frame, "2. middle")
	cheap := strings.Index(frame, "3. cheap")
	if pricey < 0 || middle < 0 || cheap < 0 || !(pricey < middle && middle < cheap) {
		t.Fatalf("model rows are not ranked by cost (pricey/middle/cheap at %d/%d/%d):\n%s", pricey, middle, cheap, frame)
	}
}

// TestDashboardAgeFromGeneratedAt pins the S1 contract note: the payload's
// freshness flags are build-time values, so the age line is recomputed from
// generatedAt against the screen's own clock.
func TestDashboardAgeFromGeneratedAt(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		raw  string // overrides at when set (a literal stamp, e.g. Node's ms form)
		want string
	}{
		{"seconds", dashboardTestNow.Add(-12 * time.Second), "", "age 12s"},
		{"minutes", dashboardTestNow.Add(-5 * time.Minute), "", "age 5m"},
		{"hours", dashboardTestNow.Add(-(2*time.Hour + 7*time.Minute)), "", "age 2h07m"},
		{"days", dashboardTestNow.Add(-(49 * time.Hour)), "", "age 2d01h"},
		{"future stamp clamps to zero", dashboardTestNow.Add(time.Minute), "", "age 0s"},
		// The Node side writes new Date().toISOString(), which carries
		// milliseconds; time.RFC3339 parses that optional fraction only because
		// Go special-cases it, so the real artifact shape is pinned here.
		{"millisecond stamp", time.Time{}, "2026-10-22T17:58:30.999Z", "age 1m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := dashboardTestPayload()
			p.GeneratedAt = tc.at.Format(time.RFC3339)
			if tc.raw != "" {
				p.GeneratedAt = tc.raw
			}
			frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, p, dashboard.StatusOK)))
			if !strings.Contains(frame, tc.want) {
				t.Fatalf("frame missing %q:\n%s", tc.want, frame)
			}
		})
	}

	// An unparsable stamp degrades to the honest "age unknown", never to a
	// fabricated number.
	p := dashboardTestPayload()
	p.GeneratedAt = "not-a-timestamp"
	frame := dashboardTestFrame(t, newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, p, dashboard.StatusOK)))
	if !strings.Contains(frame, missingDashboardAgeUnknown) {
		t.Fatalf("unparsable generatedAt did not degrade to %q:\n%s", missingDashboardAgeUnknown, frame)
	}
}

// TestDashboardSurfaceContract pins mode, title, and the read-only promise: the
// screen composes at construction, so the seam is read exactly once even when
// the frame is drawn repeatedly.
func TestDashboardSurfaceContract(t *testing.T) {
	serverCalls, payloadCalls := 0, 0
	src := dashboardSource{
		loadServer: func() (dashboard.ServerRecord, dashboard.Status) {
			serverCalls++
			return dashboardTestServer(), dashboard.StatusOK
		},
		loadPayload: func() (dashboard.Payload, dashboard.Status) {
			payloadCalls++
			return dashboardTestPayload(), dashboard.StatusOK
		},
		now: func() time.Time { return dashboardTestNow },
	}
	scr := newDashboardScreen(src)
	if scr.Mode() != ModeNav {
		t.Fatalf("dashboard mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.Title(), missingOr(i18n.Load("en"), "dashboard.title", missingDashboardTitle); got != want {
		t.Fatalf("dashboard title = %q, want %q", got, want)
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(scr)
	for i := 0; i < 3; i++ {
		app.Frame()
	}
	if serverCalls != 1 || payloadCalls != 1 {
		t.Fatalf("seam read %d/%d times over four frames, want 1/1 (compose at construction)", serverCalls, payloadCalls)
	}
}

// TestLineModeWordEightReachesDashboard pins the line-mode half of the new hub
// row: on a host whose keys arrive as text lines, the word "8" plus the line
// terminator reaches the dashboard screen through the hub's own dispatch.
func TestLineModeWordEightReachesDashboard(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	app.Push(NewHubScreen(func() HubCounts { return hubFixture }, func(id string, a *App) {
		if id == hubIDDashboard {
			a.Push(newDashboardScreen(dashboardTestSource(dashboardTestServer(), dashboard.StatusOK, dashboardTestPayload(), dashboard.StatusOK)))
		}
	}))
	out := app.CaptureRun(NewLineKeys(strings.NewReader("8\n")))
	for _, want := range []string{
		missingDashboardTitle,
		"Server up on port 8787",
		"Today: $0.423 · 243.0k tokens · 12 sessions",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("line-mode run missing %q; frames:\n%s", want, out)
		}
	}
}
