package tui

import (
	"fmt"

	"agypro/internal/cellwidth"
)

// Row is one selectable list row. Note is an optional right-side
// annotation (a source column, a count); ID lets screens map a row to an
// action without matching on the label text.
type Row struct {
	Label string
	Note  string
	ID    string
}

// listMsgPinned is the Space-pin confirmation line the three sticky-target
// screens (providers, models, presets) share: the gesture and its feedback are
// one mechanism in this widget, so they carry one sentence and one catalog key
// rather than three near-identical copies that can drift. The {{name}} slot
// takes the pinned row's own identity through the masker that screen already
// uses for its row labels.
// catalog-resolved, fallback kept: list.msg.pinned
const listMsgPinned = "Selection pinned: {{name}}. The action rows now act on this row."

// stepList is the shared two-handled list core. EVERY selectable list in
// the TUI delegates to it, so no screen can ship arrow-only or digit-only
// navigation (structural guarantee, key map requirement 2). Both input
// languages are always live:
//
//   - digits 1..9 select AND activate the absolute Nth row (1-based)
//   - on lists with more than 9 rows, 0 arms a multi-digit compose: 0 then
//     the row number reaches rows 10+ (Enter commits a pending prefix;
//     any other key cancels). The footer carries the live prefix while the
//     compose is armed, and Enter on a bare 0 with no digit typed yet has no
//     number to commit, so it cancels the prefix and activates the cursor row
//     instead of swallowing the key.
//   - arrows move a visible cursor; Enter activates the cursor row
//   - PgUp/PgDn page by pageSize rows; Home/End jump to first/last
//   - Space calls the screen's select callback (onSelect) when one is
//     registered, and falls through unhandled when none is: the list never
//     guesses what a Space means on a screen that did not ask for it
//
// Digit semantics are absolute, not per visible page: pressing 1 on a
// paged list still activates row 1 (single-key digits cover 1..9 of the
// whole list, plan open item 3).
type stepList struct {
	rows     []Row
	onPick   func(idx int)
	onSelect func(idx int) // optional Space callback (nil = Space unhandled)
	cursor   int
	offset   int
	pageSize int
	compose  bool // 0 armed a multi-digit row number (lists past 9 only)
	pending  int  // digits typed so far after the 0 (0 = none yet)
}

// newStepList builds a list; onPick is the activation callback shared by
// digit selection and Enter.
func newStepList(onPick func(idx int)) *stepList {
	return &stepList{onPick: onPick, pageSize: 10}
}

// SetOnSelect registers the Space callback. Lists that do not call this keep
// Space exactly as it was: unmapped in NAV, so HandleKey reports it as
// unhandled and the screen (or a live overlay) sees it. The callback receives
// the cursor index and is the row validator: the list never decides which
// rows are selectable, so the screen can keep its entity/verb boundary in one
// place (a verb row is then a consumed no-op, not a miss).
func (l *stepList) SetOnSelect(fn func(idx int)) { l.onSelect = fn }

// SetRows replaces the rows and resets cursor and window to the top.
func (l *stepList) SetRows(rows []Row) {
	l.rows = rows
	l.cursor = 0
	l.offset = 0
	l.compose = false
	l.pending = 0
}

// Len reports the row count.
func (l *stepList) Len() int { return len(l.rows) }

// Row returns the row at index i.
func (l *stepList) Row(i int) Row { return l.rows[i] }

// Cursor returns the absolute index of the visible cursor.
func (l *stepList) Cursor() int { return l.cursor }

// SetCursor moves the cursor, clamped to the row range, and scrolls the
// window so the cursor stays visible.
func (l *stepList) SetCursor(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(l.rows)-1 {
		i = len(l.rows) - 1
	}
	l.cursor = i
	l.ensureVisible()
}

// PageSize returns the window height in rows.
func (l *stepList) PageSize() int { return l.pageSize }

// SetPageSize sets the window height in rows (the screen derives it from
// the frame height; the default is 10).
func (l *stepList) SetPageSize(n int) {
	if n < 1 {
		n = 1
	}
	l.pageSize = n
	l.ensureVisible()
}

// Offset returns the absolute index of the first visible row.
func (l *stepList) Offset() int { return l.offset }

// VisibleEnd returns the absolute index one past the last visible row.
func (l *stepList) VisibleEnd() int {
	end := l.offset + l.pageSize
	if end > len(l.rows) {
		end = len(l.rows)
	}
	return end
}

// HandleKey runs one key through the shared two-handled path. It reports
// whether the key belonged to the list; unmapped keys return false so a
// screen can fall through.
func (l *stepList) HandleKey(k Key) bool {
	// A live compose swallows digits to extend the pending number and commits
	// on Enter. Enter on a bare 0 (no digit typed yet) has no row number to
	// commit, and swallowing it would cost the user a keystroke for an
	// accidental 0, so it cancels the prefix and falls through to Enter's
	// ordinary activate-the-cursor meaning. Any other key cancels the prefix
	// and then acts normally.
	if l.compose {
		switch {
		case k == KeyEnter && l.pending == 0:
			l.cancelCompose()
		case k == KeyEnter:
			l.commitCompose()
			l.ensureVisible()
			return true
		case k == Key("0"):
			l.extendCompose(0)
			return true
		default:
			if idx, ok := digitIndex(k); ok {
				l.extendCompose(idx + 1)
				return true
			}
			l.cancelCompose()
		}
	}
	switch k {
	case KeyUp:
		if l.cursor > 0 {
			l.cursor--
		}
	case KeyDown:
		if l.cursor < len(l.rows)-1 {
			l.cursor++
		}
	case KeyPgUp:
		l.cursor -= l.pageSize
		if l.cursor < 0 {
			l.cursor = 0
		}
	case KeyPgDn:
		l.cursor += l.pageSize
		if l.cursor > len(l.rows)-1 {
			l.cursor = len(l.rows) - 1
		}
	case KeyHome:
		l.cursor = 0
	case KeyEnd:
		l.cursor = len(l.rows) - 1
	case KeyEnter:
		if len(l.rows) > 0 {
			l.pick(l.cursor)
		}
	case KeySpace:
		// Space is the explicit SELECT gesture, never an activation: it must
		// not reach pick, so pinning a row can never open a screen or a
		// danger gate (the screens' Space callbacks only set the sticky
		// target). A list with no select callback reports the key unhandled,
		// which is what keeps every other screen and the tools overlay
		// (screen_models.go, its own Space-to-toggle) byte-identical.
		if l.onSelect == nil {
			return false
		}
		if len(l.rows) > 0 {
			l.onSelect(l.cursor)
		}
	case Key("0"):
		// 0 is not a row selector; on lists past the single-digit range it
		// arms the multi-digit compose instead. Shorter lists keep the
		// fall-through so screens see the key as before.
		if len(l.rows) > 9 {
			l.compose = true
			l.pending = 0
			return true
		}
		return false
	default:
		idx, ok := digitIndex(k)
		if !ok {
			return false
		}
		if idx < len(l.rows) {
			l.cursor = idx
			l.pick(idx)
		}
	}
	l.ensureVisible()
	return true
}

// extendCompose folds one digit into the pending row number. A number past
// the row count cancels the compose and is swallowed; a number no further
// digit could still extend into a valid row commits immediately, through the
// same cursor+pick pair every other activation uses.
func (l *stepList) extendCompose(d int) {
	n := l.pending*10 + d
	if n > len(l.rows) {
		l.cancelCompose()
		return
	}
	if n*10 > len(l.rows) {
		l.cancelCompose()
		l.cursor = n - 1
		l.pick(n - 1)
		l.ensureVisible()
		return
	}
	l.pending = n
}

// commitCompose activates the pending row number on Enter through the same
// cursor+pick pair as a bare digit and as arrow+Enter, so the screen's own
// activation handler (and any danger gate inside it) sees one identical
// door, never a second write path.
func (l *stepList) commitCompose() {
	n := l.pending
	l.cancelCompose()
	if n >= 1 && n <= len(l.rows) {
		l.cursor = n - 1
		l.pick(n - 1)
	}
}

// cancelCompose drops an armed prefix without activating anything.
func (l *stepList) cancelCompose() {
	l.compose = false
	l.pending = 0
}

func (l *stepList) pick(idx int) {
	if l.onPick != nil {
		l.onPick(idx)
	}
}

func (l *stepList) ensureVisible() {
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.pageSize {
		l.offset = l.cursor - l.pageSize + 1
	}
	maxOff := len(l.rows) - l.pageSize
	if maxOff < 0 {
		maxOff = 0
	}
	if l.offset > maxOff {
		l.offset = maxOff
	}
	if l.offset < 0 {
		l.offset = 0
	}
}

// Render returns the visible window of rows, numbered with absolute
// indices, the cursor row marked, labels padded through cellwidth. Lines
// are logical order; the P4 painter mirrors them when Frame.Dir is RTL.
// Rows are truncated to w cells at cluster boundaries. An over-wide row keeps
// a readable prefix, or the fallback marker when no complete cluster fits; a
// non-empty row never becomes an empty row at positive width.
func (l *stepList) Render(w int) []string {
	if len(l.rows) == 0 {
		return nil
	}
	labelW := 0
	for _, r := range l.rows[l.offset:l.VisibleEnd()] {
		if cw := cellwidth.String(r.Label); cw > labelW {
			labelW = cw
		}
	}
	var out []string
	for i := l.offset; i < l.VisibleEnd(); i++ {
		marker := "  "
		if i == l.cursor {
			marker = "▶ "
		}
		line := fmt.Sprintf("%s[%2d] %s", marker, i+1, cellwidth.PadRight(l.rows[i].Label, labelW))
		if note := l.rows[i].Note; note != "" {
			line += "  " + note
		}
		out = append(out, cellwidth.Truncate(line, w))
	}
	return out
}

// FooterHint is the list footer legend. Both input languages are named on
// every list; the rows-past-9 clause appears only when such rows exist and
// names the 0-prefix compose that reaches them. While a compose is armed the
// legend is replaced by the live prefix, so the pending row number is never
// invisible (F-09): the footer carries the 0, every digit typed after it, and
// the row Enter would activate. The hint uses the footer line that every list
// already draws, so the compose costs no extra frame row.
//
// A list with a select callback advertises Space in the same line: the gesture
// exists only there, so a hint naming it on every list would advertise a dead
// key, and a hint naming it nowhere would leave the fix for the arrow-only
// sticky-target defect undiscoverable. Both variants stay inside the 80-cell
// frame the screens are pinned at (a body line is clamped, so an over-long hint
// would lose exactly the Space clause).
func (l *stepList) FooterHint() string {
	if l.compose {
		if l.pending == 0 {
			return "composing 0·_ · digits extend · Enter activates the cursor row"
		}
		return fmt.Sprintf("composing 0·%d_ · Enter activates row %d · other keys cancel", l.pending, l.pending)
	}
	if l.onSelect != nil {
		if len(l.rows) > 9 {
			return "digits 1-9 select+activate · rows 10+: 0 then digits · arrows+Enter · Space pins"
		}
		return "digits 1-9 select+activate · arrows move · Enter activates · Space pins"
	}
	if len(l.rows) > 9 {
		return "digits 1-9 select+activate · rows past 9: 0 then digits · arrows + Enter"
	}
	return "digits 1-9 select+activate · arrows move · Enter activates"
}
