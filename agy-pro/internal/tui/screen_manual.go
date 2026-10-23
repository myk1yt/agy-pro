package tui

import (
	"strconv"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
)

// manualTopicKeys are the manual browser's topics in display order: the hub's
// own row order first (rows 1 to 9), then the CLI page. Each prefix carries a
// .label, a .note and the .p1.. paragraph keys; a topic's paragraphs end at the
// first key the catalog does not answer, so a page grows and shrinks with its
// text and no paragraph count is repeated in code.
var manualTopicKeys = []string{
	"manual.topic.1",  // start here: what agy pro is and the first three steps
	"manual.topic.2",  // providers
	"manual.topic.3",  // presets
	"manual.topic.4",  // prompts
	"manual.topic.5",  // settings
	"manual.topic.6",  // doctor
	"manual.topic.7",  // language
	"manual.topic.8",  // dashboard
	"manual.topic.9",  // integrations
	"manual.topic.10", // CLI basics
}

// The two chrome legends. English chrome by decision 6B, like the help
// overlay's close hint and the doctor screen's hints: no catalog key carries
// chrome, and both lines stay well inside the 80-column frame.
const (
	// manualListHint names the door into a page and the key that leaves the
	// browser. The list's own footer (stepList.FooterHint) carries the two-handled
	// navigation legend below this line.
	manualListHint = "Enter opens a page · Esc goes back"
	// manualPageHint is the page's own contract. It names the six scroll keys and
	// the three keys that leave the page, so a reader never has to guess which
	// half of the household contract applies here.
	manualPageHint = "Up/Down/PgUp/PgDn/Home/End scroll; Esc/q/Enter go back."
)

// manualTopic is one resolved topic: the list row's label, its one-line teaser,
// and the page paragraphs in reading order.
type manualTopic struct {
	label string
	note  string
	paras []string
}

// manualScreen is the manual browser: one row per agy-pro surface (and one for
// the CLI), each carrying a one-line teaser. Enter or a digit opens the topic's
// page; Esc and q leave the browser, the same two keys that close the help
// overlay and the pages below it.
//
// Reachability: the help overlay's Enter key pushes this screen (see
// helpScreen.openManual), so the hub keeps its ten rows and its own numbering.
// The rows resolve through the active bundle at construction, the same rule the
// help overlay follows: a language change rebuilds the stack from the hub.
type manualScreen struct {
	b      *i18n.Bundle
	list   *stepList
	rows   []Row
	topics []manualTopic
	app    *App
}

// NewManualScreen builds the browser over the active bundle.
func NewManualScreen() Screen {
	m := &manualScreen{b: Bundle()}
	m.rows = make([]Row, len(manualTopicKeys))
	for i, prefix := range manualTopicKeys {
		t := resolveManualTopic(m.b, prefix)
		m.topics = append(m.topics, t)
		m.rows[i] = Row{Label: t.label, Note: t.note, ID: prefix}
	}
	m.list = newStepList(m.activate)
	m.list.SetRows(m.rows)
	return m
}

// resolveManualTopic resolves one topic's label, teaser and paragraphs. The
// paragraph scan stops at the first key the catalog does not answer: Bundle.T
// hands back the key itself for a missing entry, which is the sentinel this
// loop reads, so an edited catalog can change a page's length without a code
// change.
func resolveManualTopic(b *i18n.Bundle, prefix string) manualTopic {
	t := manualTopic{label: b.T(prefix + ".label"), note: b.T(prefix + ".note")}
	for n := 1; ; n++ {
		key := prefix + ".p" + strconv.Itoa(n)
		para := b.T(key)
		if para == key {
			break
		}
		t.paras = append(t.paras, para)
	}
	return t
}

// Title resolves through the catalog in the active language.
func (m *manualScreen) Title() string { return m.b.T("manual.title") }

// Mode is ModeNav: digits and arrows both navigate, and no row edits text.
func (m *manualScreen) Mode() Mode { return ModeNav }

// Draw renders the numbered topic rows with the two legends under them. The
// chrome reserve covers the blank, the two legend lines and the frame's own
// fixed rows, so all ten rows stay on an 80x24 frame.
func (m *manualScreen) Draw(a *App) []string {
	m.app = a
	m.list.SetPageSize(pageSizeFor(a.Height(), 6))
	lines := m.list.Render(a.Width())
	lines = append(lines, "", manualListHint, m.list.FooterHint())
	return lines
}

// HandleKey routes one key: the shared stepList first (digits and arrows+Enter
// are both live), then the household close keys for anything the list did not
// take. q closes the browser exactly as it closes the help overlay and a topic
// page, so all three documents on this surface answer the same key; Esc never
// arrives here, because the App precedence pops the screen first.
func (m *manualScreen) HandleKey(k Key, a *App) {
	m.app = a
	if m.list.HandleKey(k) {
		return
	}
	if isHelpCloseKey(k) {
		if !a.Pop() {
			a.Exit()
		}
	}
}

// activate is the stepList onPick callback: it pushes the topic's page over
// this list, so Esc returns to the list with the cursor still on the topic the
// reader left.
func (m *manualScreen) activate(idx int) {
	if m.app == nil || idx < 0 || idx >= len(m.topics) {
		return
	}
	t := m.topics[idx]
	m.app.Push(newManualPageScreen(t.label, t.paras))
}

// manualPageScreen is one topic page: the topic label as the screen title and
// the paragraphs wrapped into the shared read-only scroll window.
//
// It is a stacked screen rather than a mode flag inside the browser because Esc
// is answered by the App precedence before any screen sees it (App.Step pops
// one screen per Esc). A page that lives on the stack therefore gets the
// documented "Esc goes back to the topic list" for free, and the list keeps its
// cursor without storing a page cursor of its own. Enter and q leave a page the
// same way, so the three keys the help overlay's close contract always named
// still leave a document.
type manualPageScreen struct {
	title string
	paras []string
	sc    textScroller
	page  int
}

// newManualPageScreen builds one topic page. The paragraphs arrive already
// resolved, so the page never touches the catalog.
func newManualPageScreen(title string, paras []string) Screen {
	return &manualPageScreen{title: title, paras: paras, page: 1}
}

// Title is the topic label, so the frame's title bar names the page.
func (p *manualPageScreen) Title() string { return p.title }

// Mode is ModeNav: the page only scrolls and closes.
func (p *manualPageScreen) Mode() Mode { return ModeNav }

// Draw wraps every paragraph to the frame width and renders the scroll window
// plus the page legend. wrapCells splits on the paragraph boundary the caller
// inserts and on words inside a paragraph, and breaks a word wider than the frame
// across as many rows as it needs (only an RTL word is left whole, because
// cellwidth refuses that cut, and it renders as an empty row instead of a mangled
// one), so a page never loses text to the wrapper.
func (p *manualPageScreen) Draw(a *App) []string {
	w := a.Width()
	p.page = a.Height() - 6
	if p.page < 1 {
		p.page = 1
	}
	var lines []string
	for i, para := range p.paras {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, wrapCells(para, w)...)
	}
	p.sc.replace(lines)
	out := make([]string, 0, p.page+2)
	for _, l := range p.sc.window(p.page) {
		out = append(out, cellwidth.Truncate(l, w))
	}
	return append(out, "", manualPageHint)
}

// HandleKey implements the reading contract the legend names: Up/Down scroll one
// line, PgUp/PgDn one page, Home/End jump to either edge, and Esc/q/Enter leave
// the page for the topic list (a.Pop, with a.Exit as the root-screen fallback,
// the same pair the help overlay uses). Esc normally never reaches this method:
// the App precedence has already popped the page.
func (p *manualPageScreen) HandleKey(k Key, a *App) {
	switch k {
	case KeyUp:
		p.sc.scrollLines(-1, p.page)
	case KeyDown:
		p.sc.scrollLines(1, p.page)
	case KeyPgUp:
		p.sc.scroll(-1, p.page)
	case KeyPgDn:
		p.sc.scroll(1, p.page)
	case KeyHome:
		p.sc.home()
	case KeyEnd:
		p.sc.end(p.page)
	default:
		if manualPageCloseKey(k) {
			if !a.Pop() {
				a.Exit()
			}
		}
	}
}

// manualPageCloseKey reports whether k leaves a page. It is the help overlay's
// close set plus Enter: the overlay handed Enter to the manual door, and a page
// is a document, so Enter keeps the "close the document" meaning it had there.
func manualPageCloseKey(k Key) bool {
	if k == KeyEnter {
		return true
	}
	return isHelpCloseKey(k)
}
