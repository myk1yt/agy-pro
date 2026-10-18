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

// stepList is the shared two-handled list core. EVERY selectable list in
// the TUI delegates to it, so no screen can ship arrow-only or digit-only
// navigation (structural guarantee, key map requirement 2). Both input
// languages are always live:
//
//   - digits 1..9 select AND activate the absolute Nth row (1-based; rows
//     past 9 stay reachable through arrows + Enter, and the footer says so)
//   - arrows move a visible cursor; Enter activates the cursor row
//   - PgUp/PgDn page by pageSize rows; Home/End jump to first/last
//
// Digit semantics are absolute, not per visible page: pressing 1 on a
// paged list still activates row 1 (single-key digits cover 1..9 of the
// whole list, plan open item 3).
type stepList struct {
	rows     []Row
	onPick   func(idx int)
	cursor   int
	offset   int
	pageSize int
}

// newStepList builds a list; onPick is the activation callback shared by
// digit selection and Enter.
func newStepList(onPick func(idx int)) *stepList {
	return &stepList{onPick: onPick, pageSize: 10}
}

// SetRows replaces the rows and resets cursor and window to the top.
func (l *stepList) SetRows(rows []Row) {
	l.rows = rows
	l.cursor = 0
	l.offset = 0
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
// Rows are truncated to w cells (cellwidth refuses mid-string cuts on RTL
// scripts, so an overlong RTL row renders whole or not at all).
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
// every list; the row-past-9 clause appears only when such rows exist.
func (l *stepList) FooterHint() string {
	if len(l.rows) > 9 {
		return "digits 1-9 select+activate · rows past 9: arrows + Enter"
	}
	return "digits 1-9 select+activate · arrows move · Enter activates"
}
