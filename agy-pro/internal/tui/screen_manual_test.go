package tui

import (
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
)

// manualTestApp builds an 80x24 app with a hub stub as the root and the manual
// browser pushed on top, the same stack shape the help overlay's Enter door
// produces.
func manualTestApp(t *testing.T) (*App, *manualScreen) {
	t.Helper()
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.Push(NewManualScreen())
	scr, ok := app.Top().(*manualScreen)
	if !ok {
		t.Fatalf("top screen is %T, want *manualScreen", app.Top())
	}
	return app, scr
}

// frameWidths measures every composed frame line in display cells.
func frameWidths(frame Frame) (widest int, widestLine string) {
	for _, line := range frame.Lines {
		if w := cellwidth.String(line); w > widest {
			widest, widestLine = w, line
		}
	}
	return widest, widestLine
}

// widestFrameWidth reports the widest line of a frame, for the measurement log.
func widestFrameWidth(frame Frame) int {
	w, _ := frameWidths(frame)
	return w
}

// TestManualTitleAndMode pins the surface contract: the catalog title, NAV mode,
// and one row per agy-pro surface plus the CLI page.
func TestManualTitleAndMode(t *testing.T) {
	app, scr := manualTestApp(t)
	if got, want := scr.Title(), i18n.Load("en").T("manual.title"); got != want {
		t.Fatalf("manual title = %q, want %q", got, want)
	}
	if scr.Mode() != ModeNav {
		t.Fatalf("manual mode = %v, want ModeNav", scr.Mode())
	}
	if got, want := scr.list.Len(), 10; got != want {
		t.Fatalf("manual rows = %d, want %d", got, want)
	}
	if len(scr.topics) != scr.list.Len() {
		t.Fatalf("topics = %d, rows = %d", len(scr.topics), scr.list.Len())
	}
	for i, topic := range scr.topics {
		if topic.note == "" || topic.label == "" {
			t.Fatalf("topic %d carries an empty label or teaser: %+v", i+1, topic)
		}
		if len(topic.paras) < 3 {
			t.Fatalf("topic %d (%q) has %d paragraphs, want at least 3", i+1, topic.label, len(topic.paras))
		}
	}
	// The two legends are the browse contract; both must reach the frame.
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{manualListHint, scr.list.FooterHint()} {
		if !strings.Contains(out, want) {
			t.Fatalf("the list frame is missing %q:\n%s", want, out)
		}
	}
}

// TestManualListFitsFrameAt80x24 pins the list frame budget in every one of the
// twelve locales, which is where the row budget actually bites: a row line is
// 7 cells of marker and number, the padded label, two spaces and the note, and
// cellwidth refuses a mid-string cut inside an RTL range, so an overlong row
// renders as an EMPTY line (the FIX-15 class, and FIX-16 P1-B in ar). The pin
// therefore asserts three things per row: the row reached the frame, the label
// is on it whole, and the note is on it whole. "Non-empty" alone would let a
// teaser truncate mid-sentence (FIX-16 P2-C).
func TestManualListFitsFrameAt80x24(t *testing.T) {
	defer SetLanguage("en")
	for _, lang := range i18n.Languages() {
		SetLanguage(lang.Code)
		dir := DirLTR
		if lang.Dir == "rtl" {
			dir = DirRTL
		}
		app := NewApp(80, 24, dir)
		app.Push(&stubScreen{title: "Hub", mode: ModeNav})
		app.Push(NewManualScreen())
		scr := app.Top().(*manualScreen)
		frame := app.Frame()
		if got := len(frame.Lines); got > 24 {
			t.Fatalf("%s: frame lines = %d, want <= 24:\n%s", lang.Code, got, frame.String())
		}
		if widest, line := frameWidths(frame); widest > 80 {
			t.Fatalf("%s: frame line %d cells wide (limit 80): %q", lang.Code, widest, line)
		}
		// All ten rows render at once: the window is the frame height minus the
		// chrome, and ten rows fit inside it.
		if page := scr.list.PageSize(); page < scr.list.Len() {
			t.Fatalf("%s: page size = %d, want >= %d rows so every topic is reachable at once",
				lang.Code, page, scr.list.Len())
		}
		visible := 0
		for i, row := range scr.rows {
			marker := "[" + pad2(i+1) + "]"
			line, found := "", false
			for _, l := range frame.Lines {
				if strings.Contains(l, marker) {
					line, found = l, true
					break
				}
			}
			if !found {
				t.Fatalf("%s: row %d (%q) rendered no line at all (marker %q missing); the row was cut to empty:\n%s",
					lang.Code, i+1, row.Label, marker, frame.String())
			}
			visible++
			if !strings.Contains(line, row.Label) {
				t.Fatalf("%s: row %d label truncated (%q not whole on %q)", lang.Code, i+1, row.Label, line)
			}
			if !strings.Contains(line, row.Note) {
				t.Fatalf("%s: row %d teaser truncated (%q not whole on %q)", lang.Code, i+1, row.Note, line)
			}
		}
		t.Logf("%s list at 80x24: %d frame rows, %d rows visible, page size %d, widest %d cells",
			lang.Code, len(frame.Lines), visible, scr.list.PageSize(), widestFrameWidth(frame))
	}
}

// pad2 renders a 1-based row number the way stepList.Render does.
func pad2(n int) string {
	if n < 10 {
		return " " + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestHelpOpensManual pins the door: Enter on the help overlay steps the overlay
// aside and pushes the manual browser, so Esc from the manual lands on the
// screen that opened help (the hub) instead of on the overlay again.
func TestHelpOpensManual(t *testing.T) {
	helpTestEnv(t)
	root := &stubScreen{title: "Hub", mode: ModeNav}
	app := newHelpApp(root)
	app.Step(KeyEnter)
	if app.Depth() != 2 {
		t.Fatalf("depth after Enter = %d, want 2 (hub + manual)", app.Depth())
	}
	if _, ok := app.Top().(*manualScreen); !ok {
		t.Fatalf("top screen is %T, want *manualScreen after Enter on the overlay", app.Top())
	}
	if app.Frame().String() == "" {
		t.Fatal("the manual painted no frame")
	}
	// Esc backs out to the hub in one press: the overlay was replaced, not
	// stacked under the manual.
	app.Step(KeyEsc)
	if app.Depth() != 1 || app.Top() != root {
		t.Fatalf("Esc from the manual list landed on depth %d, want the hub", app.Depth())
	}
}

// TestManualEndToEndThroughRunSetup drives the whole path through the real app
// shell in line mode: hub row 6 opens the overlay, Enter opens the manual, the
// topic digit opens its page, and esc walks back out. It is the real-use
// scenario check (the screen-in-isolation pins above cannot see the hub's own
// row dispatch or the shell's Esc precedence).
func TestManualEndToEndThroughRunSetup(t *testing.T) {
	appTestHome(t, "en")
	var out, errw strings.Builder
	// 6 = hub Help row, enter = the overlay's manual door, 2 = the Providers
	// topic, esc = back to the topic list, esc = back to the hub.
	if code := runSetup(strings.NewReader("6\nenter\n2\nesc\nesc\n"), &out, &errw, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	frames := out.String()
	for _, want := range []string{
		i18n.Load("en").T("help.section.guide"),
		i18n.Load("en").T("manual.title"),
		i18n.Load("en").T("manual.topic.2.label"),
		i18n.Load("en").T("manual.topic.1.label"),
		manualPageHint,
		i18n.Load("en").T("menu.title"),
	} {
		if !strings.Contains(frames, want) {
			t.Fatalf("end-to-end run never rendered %q; frames:\n%s", want, frames)
		}
	}
	// The page body resolves in the run: a sentence from the Providers page
	// reaches a frame.
	body := i18n.Load("en").T("manual.topic.2.p1")
	if words := strings.Fields(body); len(words) > 3 && !strings.Contains(frames, strings.Join(words[:3], " ")) {
		t.Fatalf("the Providers page body never reached a frame; frames:\n%s", frames)
	}
}

// TestManualEnterOpensPageAndEscReturns pins the page round trip: Enter on a
// topic pushes its page over the list, Esc returns to the list, and the list
// cursor is still on the topic the reader left.
func TestManualEnterOpensPageAndEscReturns(t *testing.T) {
	app, scr := manualTestApp(t)
	scr.list.SetCursor(2)
	want := scr.topics[2].label
	app.Step(KeyEnter)

	page, ok := app.Top().(*manualPageScreen)
	if !ok {
		t.Fatalf("top screen is %T, want *manualPageScreen", app.Top())
	}
	if page.Title() != want {
		t.Fatalf("page title = %q, want the topic label %q", page.Title(), want)
	}
	if app.Depth() != 3 {
		t.Fatalf("depth with a page open = %d, want 3 (hub + list + page)", app.Depth())
	}
	frame := app.Frame()
	if got := len(frame.Lines); got > 24 {
		t.Fatalf("page frame lines = %d, want <= 24:\n%s", got, frame.String())
	}
	if !strings.Contains(frame.String(), manualPageHint) {
		t.Fatalf("the page frame is missing its reading legend %q:\n%s", manualPageHint, frame.String())
	}

	app.Step(KeyEsc)
	if app.Depth() != 2 {
		t.Fatalf("depth after Esc = %d, want 2 (back on the topic list)", app.Depth())
	}
	if app.Top() != scr {
		t.Fatalf("Esc landed on %T, want the manual list", app.Top())
	}
	if got := scr.list.Cursor(); got != 2 {
		t.Fatalf("list cursor = %d after the round trip, want 2", got)
	}
}

// TestManualPageScrollsAndCloses pins the page contract: the six scroll keys
// move the window, clamp at both edges, and never leave the page, and Enter/q
// leave it (Esc is the App precedence's job, covered above). The frame is 80x10
// here so the 4-line window leaves every real page longer than the window; at
// the default 80x24 window a topic page fits whole, which is why the scroll math
// is pinned at the geometry where it has something to do.
func TestManualPageScrollsAndCloses(t *testing.T) {
	for _, closer := range []Key{KeyEnter, RuneKey('q')} {
		app := NewApp(80, 10, DirLTR)
		app.Push(&stubScreen{title: "Hub", mode: ModeNav})
		app.Push(NewManualScreen())
		scr := app.Top().(*manualScreen)
		scr.list.SetCursor(scr.list.Len() - 1) // the last topic: the CLI page
		app.Step(KeyEnter)
		page := app.Top().(*manualPageScreen)
		before := app.Frame().String() // one draw fills the scroll window
		if window := len(page.sc.window(page.page)); window >= len(page.sc.lines) {
			t.Fatalf("%q: the test frame did not leave the page longer than its window (%d lines)", closer, len(page.sc.lines))
		}

		app.Step(KeyPgDn)
		if app.Top() != page {
			t.Fatalf("%q: PgDn left the page", closer)
		}
		if off := page.sc.off; off == 0 {
			t.Fatalf("%q: PgDn left the scroll offset at 0:\n%s", closer, app.Frame().String())
		}
		if after := app.Frame().String(); after == before {
			t.Fatalf("%q: PgDn did not change the painted page:\n%s", closer, after)
		}
		app.Step(KeyPgDn)
		app.Step(KeyPgDn)
		app.Step(KeyPgDn)
		app.Step(KeyPgDn)
		app.Step(KeyEnd)
		if off, max := page.sc.off, len(page.sc.lines)-page.page; off != max {
			t.Fatalf("%q: End left the offset at %d, want the last-page offset %d", closer, off, max)
		}
		app.Step(KeyHome)
		if off := page.sc.off; off != 0 {
			t.Fatalf("%q: Home left the offset at %d, want 0", closer, off)
		}
		app.Step(KeyDown)
		if off := page.sc.off; off != 1 {
			t.Fatalf("%q: Down left the offset at %d, want 1", closer, off)
		}
		app.Step(KeyUp)
		app.Step(KeyUp)
		if off := page.sc.off; off != 0 {
			t.Fatalf("%q: Up did not clamp at the top (offset %d)", closer, off)
		}
		for _, k := range []Key{Digit(1), KeyYes, KeyTab} {
			app.Step(k)
			if app.Top() != page {
				t.Fatalf("%q: inert key %q left the page", closer, k)
			}
		}
		app.Step(closer)
		if app.Depth() != 2 || app.Top() != scr {
			t.Fatalf("%q did not return to the topic list (depth %d)", closer, app.Depth())
		}
	}
}

// TestManualLineModeParity pins the walk through the line-mode word vocabulary,
// so a pipe or a non-Windows TTY reaches the manual, opens a topic page and backs
// out exactly like a console does. The assertion is on PAGE-ONLY text: the topic
// labels are already on the list frame, so asserting one of those passed even
// with the page push disabled (FIX-16 P2-D). "2" selects row 2, the Providers
// topic; its first paragraph never appears on a list frame, and the check below
// proves that before relying on it.
func TestManualLineModeParity(t *testing.T) {
	app, scr := manualTestApp(t)
	topic := scr.topics[1]
	opening := strings.Join(strings.Fields(topic.paras[0])[:3], " ")

	listOnly, _ := manualTestApp(t)
	if listOnlyOut := listOnly.CaptureRun(&ScriptedKeys{}); strings.Contains(listOnlyOut, opening) {
		t.Fatalf("the probe text %q is already on the list frame, so this pin would pass without opening a page:\n%s",
			opening, listOnlyOut)
	}

	out := app.CaptureRun(NewLineKeys(strings.NewReader("2\nenter\nesc\nesc\n")))
	if !strings.Contains(out, opening) {
		t.Fatalf("line mode never rendered the row-2 page body (want the opening %q):\n%s", opening, out)
	}
	if !strings.Contains(out, manualPageHint) {
		t.Fatalf("line mode never rendered the page legend %q:\n%s", manualPageHint, out)
	}
	if app.Depth() != 1 {
		t.Fatalf("depth = %d after two esc words, want 1 (back on the hub)", app.Depth())
	}
}

// manualScrollFreeLocales are the three locales whose pages must fit the whole
// 18-line window at 80x24: en and ko are the two the product is written and read
// in, ar is the RTL script where the width math is easiest to get wrong. The
// other nine may scroll, but only just (see manualPageSlackLines).
var manualScrollFreeLocales = map[string]bool{"en": true, "ko": true, "ar": true}

// manualPageSlackLines is how far past the window a non-pinned locale's page may
// run before the pin calls it a defect: four lines, so a translated page may spill
// a little and still be one PgDn from finished, but a page that needs a second
// screen has stopped being a page. A translation that drifts past this is bloat,
// not wording.
const manualPageSlackLines = 4

// TestManualPagesWrapCleanly pins the frame budget per page in all twelve
// locales, in two tiers. Hard everywhere: every rendered line stays inside 80
// cells, the wrapped body carries the full text of the catalog paragraphs, and the
// only blank content lines are the paragraph separators (an overlong RTL stretch
// renders as an EMPTY line, the FIX-15 lesson, so a hole in the body is a
// failure). Hard per tier on length: en/ko/ar must fit the whole 18-line window
// at 80x24, so no reader has to find a scroll key to finish a page; the other
// nine may scroll but must stay within manualPageSlackLines of the window, which
// is what flags a translation that has grown into a scroll-only page. The nine
// are held to the wider ceiling rather than pinned scroll-free because translated
// length cannot be fixed by a pin: on this very page the same facts take 14
// wrapped text lines in en, 15 in ru, 16 in de and it, and 19 in fr before the
// FIX-17 trim.
func TestManualPagesWrapCleanly(t *testing.T) {
	defer SetLanguage("en")
	for _, lang := range i18n.Languages() {
		SetLanguage(lang.Code)
		code := lang.Code
		dir := DirLTR
		if lang.Dir == "rtl" {
			dir = DirRTL
		}
		app := NewApp(80, 24, dir)
		app.Push(&stubScreen{title: "Hub", mode: ModeNav})
		app.Push(NewManualScreen())
		scr := app.Top().(*manualScreen)
		if scr.Title() == "manual.title" {
			t.Fatalf("%s: manual.title did not resolve", code)
		}
		for i := range scr.topics {
			scr.list.SetCursor(i)
			app.Step(KeyEnter)
			page, ok := app.Top().(*manualPageScreen)
			if !ok {
				t.Fatalf("%s: topic %d did not open a page", code, i+1)
			}
			if page.Title() != scr.topics[i].label {
				t.Fatalf("%s: topic %d title = %q, want %q", code, i+1, page.Title(), scr.topics[i].label)
			}
			frame := app.Frame()
			if got := len(frame.Lines); got > 24 {
				t.Fatalf("%s: topic %d frame lines = %d, want <= 24", code, i+1, got)
			}
			// The scroll budget, pinned per tier. en/ko/ar must not scroll at all;
			// the rest may scroll within the slack.
			content, window := len(page.sc.lines), page.page
			if manualScrollFreeLocales[code] {
				if content > window {
					t.Fatalf("%s: topic %d needs scrolling at 80x24: %d content lines, %d-line window; this locale is pinned scroll-free, so shorten the page",
						code, i+1, content, window)
				}
			} else if limit := window + manualPageSlackLines; content > limit {
				t.Fatalf("%s: topic %d runs %d content lines, %d past the %d-line window (ceiling %d); the translation has grown into a scroll-only page",
					code, i+1, content, content-window, window, limit)
			}
			// No hole and no loss: the only blank lines are the paragraph
			// separators, and every rune of every paragraph reaches a content line
			// (a wrapper that truncates an overlong no-space run, which is what a
			// CJK sentence looks like to a word splitter, would drop text here).
			wantBlanks := len(scr.topics[i].paras) - 1
			blanks := 0
			for _, line := range page.sc.lines {
				if strings.TrimSpace(line) == "" {
					blanks++
				}
			}
			if blanks != wantBlanks {
				t.Fatalf("%s: topic %d renders %d blank lines for %d paragraphs, want %d paragraph separators and no other empty line:\n%s",
					code, i+1, blanks, len(scr.topics[i].paras), wantBlanks, frame.String())
			}
			rendered, authored := squashCells(page.sc.lines), squashCells(scr.topics[i].paras)
			if rendered != authored {
				t.Fatalf("%s: topic %d dropped catalog text: the page carries %d runes, the paragraphs have %d; the wrapper truncated a word instead of breaking it",
					code, i+1, len([]rune(rendered)), len([]rune(authored)))
			}
			if widest, line := frameWidths(frame); widest > 80 {
				t.Fatalf("%s: topic %d line %d cells wide (limit 80): %q", code, i+1, widest, line)
			}
			// The page body is the frame minus the title, the two separators,
			// the base footer, the page legend and its blank line. It must hold
			// real text: a body of blank lines is the FIX-15 failure shape.
			body := frame.Lines[2 : len(frame.Lines)-2]
			nonEmpty, blankFromTail := 0, len(body)-1
			for ; blankFromTail >= 0 && strings.TrimSpace(body[blankFromTail]) == ""; blankFromTail-- {
			}
			if blankFromTail+1 < 1 {
				t.Fatalf("%s: topic %d rendered an empty page body:\n%s", code, i+1, frame.String())
			}
			for _, line := range body[:blankFromTail+1] {
				if strings.TrimSpace(line) == "" {
					continue // the paragraph separator
				}
				nonEmpty++
			}
			if nonEmpty < 2 {
				t.Fatalf("%s: topic %d rendered %d text lines, want at least 2:\n%s", code, i+1, nonEmpty, frame.String())
			}
			t.Logf("%s topic %2d: %2d content lines, %2d-line window, %2d frame rows, %2d body rows, widest %d cells",
				code, i+1, len(page.sc.lines), page.page, len(frame.Lines), nonEmpty, widestFrameWidth(frame))
			// The first paragraph starts the page: its opening runes survive.
			// The probe is squashed and rune-based because a script without spaces
			// (zh) hands strings.Fields the whole sentence as one "word", which no
			// wrapped page can carry contiguously.
			opening := []rune(squashCells(scr.topics[i].paras[:1]))
			if len(opening) > 12 {
				opening = opening[:12]
			}
			if !strings.HasPrefix(squashCells(page.sc.lines), string(opening)) {
				t.Fatalf("%s: topic %d does not open with its first paragraph (%q):\n%s",
					code, i+1, string(opening), frame.String())
			}
			app.Step(KeyEsc)
			if app.Top() != scr {
				t.Fatalf("%s: topic %d did not return to the list", code, i+1)
			}
		}
	}
}

// squashCells joins lines into one string with every run of whitespace removed,
// so two renderings can be compared for lost text without caring where the
// wrapper put its breaks.
func squashCells(lines []string) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(strings.Join(strings.Fields(line), ""))
	}
	return b.String()
}

// TestManualEveryTopicResolvesInEveryLocale pins the catalog contract at the
// screen boundary: in all twelve languages every topic resolves to text (never
// to its own key ID), so a catalog edit that drops a paragraph cannot render a
// page of key names.
func TestManualEveryTopicResolvesInEveryLocale(t *testing.T) {
	defer SetLanguage("en")
	for _, lang := range i18n.Languages() {
		SetLanguage(lang.Code)
		scr := NewManualScreen().(*manualScreen)
		if scr.Title() == "manual.title" {
			t.Fatalf("%s: manual.title did not resolve", lang.Code)
		}
		for i, topic := range scr.topics {
			prefix := manualTopicKeys[i]
			if topic.label == prefix+".label" {
				t.Fatalf("%s: %s.label did not resolve", lang.Code, prefix)
			}
			if topic.note == prefix+".note" {
				t.Fatalf("%s: %s.note did not resolve", lang.Code, prefix)
			}
			if len(topic.paras) < 3 {
				t.Fatalf("%s: %s has %d paragraphs, want at least 3", lang.Code, prefix, len(topic.paras))
			}
			for n, para := range topic.paras {
				if strings.TrimSpace(para) == "" || para == prefix+".p"+itoa(n+1) {
					t.Fatalf("%s: %s.p%d did not resolve", lang.Code, prefix, n+1)
				}
			}
		}
	}
}

// TestManualListQCloses pins the close-key symmetry of this surface: q closes the
// topic list exactly as it closes the help overlay and a topic page, so the
// household "q closes the document" contract holds at every depth instead of
// only inside pages (FIX-16 P3). Esc is the App precedence's key and is covered
// by TestHelpOpensManual.
func TestManualListQCloses(t *testing.T) {
	for _, closer := range []Key{RuneKey('q'), Key("q"), RuneKey('Q')} {
		app, scr := manualTestApp(t)
		app.Step(closer)
		if app.Depth() != 1 || app.Top() == scr {
			t.Fatalf("%q did not close the manual list (depth %d, top %T)", closer, app.Depth(), app.Top())
		}
	}
	// The cursor keys and the narrow keys still belong to the list: q closing
	// must not have turned them into closers. (A digit does open a page, which
	// TestManualEnterOpensPageAndEscReturns pins.)
	app, scr := manualTestApp(t)
	for _, k := range []Key{KeyDown, KeyUp, KeyPgDn, KeyPgUp, KeyHome, KeyEnd, KeyYes, KeyTab} {
		app.Step(k)
		if app.Top() != scr {
			t.Fatalf("key %q left the manual list; only Esc/q close it", k)
		}
	}
}

// TestManualPageOverflowIsVisible is the bite for the scroll-free claim: a page
// long enough to leave the window MUST report more content lines than the window,
// which is the condition TestManualPagesWrapCleanly fails on for en/ko/ar, and
// also more than the window plus the slack the other nine locales are held to,
// so neither tier can go vacuous through a broken window height.
func TestManualPageOverflowIsVisible(t *testing.T) {
	paras := make([]string, 40)
	for i := range paras {
		paras[i] = "A paragraph long enough to take one full row of an 80 column frame at least."
	}
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.Push(newManualPageScreen("Overflow", paras))
	app.Frame()
	page := app.Top().(*manualPageScreen)
	content, window := len(page.sc.lines), page.page
	if content <= window {
		t.Fatalf("a 40-paragraph page reported %d content lines in a %d-line window; the scroll-free condition cannot fail",
			content, window)
	}
	if limit := window + manualPageSlackLines; content <= limit {
		t.Fatalf("a 40-paragraph page reported %d content lines, inside the %d-line ceiling (%d + %d slack); the slack tier cannot fail",
			content, limit, window, manualPageSlackLines)
	}
	if got := len(app.Frame().Lines); got > 24 {
		t.Fatalf("frame lines = %d, want <= 24 even for an overflowing page", got)
	}
}

// TestManualOverlongWordWrapsWithoutLoss is the bite for wrapCells' hard break:
// a run with no spaces inside it (what a CJK sentence looks like to a word
// splitter) is wider than the frame, and it must be broken across rows with every
// rune intact instead of being cut once and dropped. The dropped remainder is
// also what used to leave an extra blank line behind, which is why
// TestManualPagesWrapCleanly can now assert that the only blank content lines are
// the paragraph separators.
func TestManualOverlongWordWrapsWithoutLoss(t *testing.T) {
	para := strings.Repeat("統合は五つの機能を管理します", 12) // 156 runes, one unbroken word
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Hub", mode: ModeNav})
	app.Push(newManualPageScreen("Overlong", []string{para}))
	app.Frame()
	page := app.Top().(*manualPageScreen)
	if got := len(page.sc.lines); got < 2 {
		t.Fatalf("a %d-rune single word fitted %d content lines; the wrapper never broke it", len([]rune(para)), got)
	}
	for i, line := range page.sc.lines {
		if line == "" {
			t.Fatalf("line %d is empty; the break left a hole instead of text", i)
		}
		if w := cellwidth.String(line); w > 80 {
			t.Fatalf("line %d is %d cells wide (limit 80): %q", i, w, line)
		}
	}
	if got := squashCells(page.sc.lines); got != para {
		t.Fatalf("the page carries %d runes of a %d-rune paragraph; the wrapper dropped text",
			len([]rune(got)), len([]rune(para)))
	}
}
