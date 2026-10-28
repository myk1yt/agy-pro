package tui

import (
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
)

// promptsListChrome is the non-list line count of the composed frame around
// the slot list: the screen title, the separator under it, the status line,
// the blank under the status line, the blank above the list hint, the hint,
// the separator above the base footer, and the base footer itself (the same
// body shape the presets list uses; the models screen has no status line and
// needs 6).
const promptsListChrome = 8

// promptSlots is the fixed inventory of the prompt library, in display
// order: the six task presets (matching fanout.GetTaskPreset slots) plus
// the native seat preamble last. The slot ID doubles as the store file
// stem (slot + ".txt") because the IDs are the canonical slot names.
var promptSlots = []string{"review", "security", "plan-audit", "research", "ideas", "custom", "seat"}

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved).
const (
	// catalog-resolved, fallback kept: prompts.status.count
	promptsStatusCount = "{{active}} of {{total}} slots have an active override."
	// catalog-resolved, fallback kept: prompts.slot.unreadable
	promptsSlotUnreadable = "unreadable"
)

// promptsScreen is S9: the prompt library (architect-report section 3,
// screen 9). One row per prompt slot (six task presets plus the native
// seat preamble), each carrying the source badge of the resolution rung
// currently in force (compiled default / file / env, per the P9 store
// ladder flag > env > file > compiled default). A status line counts the
// slots with an active override. Enter (or a digit) opens the editor for
// that slot through the shell-supplied openEditor callback. Nothing is
// written on this screen; reads go straight to the store, re-resolved on
// every frame so a badge flips the moment an editor action lands.
// Navigation is the shared stepList, so digits and arrows+Enter are both
// always live (requirement 2).
type promptsScreen struct {
	openEditor func(slot string, a *App)
	b          *i18n.Bundle
	lang       string
	list       *stepList
	rows       []Row
	rowsSig    string
	app        *App
}

// NewPromptsScreen builds the prompt library. openEditor is the shell
// callback that pushes the prompt editor; it receives the canonical slot
// ID (one of promptSlots). The UI language resolves from the persisted
// state (AGY_PRO_HOME state.json), defaulting to English.
func NewPromptsScreen(openEditor func(slot string, a *App)) Screen {
	s := &promptsScreen{openEditor: openEditor}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))
	s.list = newStepList(s.activate)
	s.syncRows()
	return s
}

// setLang loads the bundle for lang once per language.
func (s *promptsScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeNav: digits and arrows both navigate.
func (s *promptsScreen) Mode() Mode { return ModeNav }

// Title resolves through the catalog.
func (s *promptsScreen) Title() string { return s.b.T("prompts.screen.title") }

// slotLabel resolves the display name of a slot through the catalog.
func (s *promptsScreen) slotLabel(slot string) string {
	return s.b.T("prompts.slot." + promptSlotKey(slot))
}

// promptSlotKey maps a canonical slot ID to its catalog key suffix.
func promptSlotKey(slot string) string {
	if slot == "plan-audit" {
		return "plan_audit"
	}
	return slot
}

// sourceBadge maps the store rung label to the catalog badge text.
func sourceBadge(b *i18n.Bundle, src string) string {
	switch src {
	case fanout.PromptSourceEnv:
		return b.T("prompts.source.badge.env")
	case fanout.PromptSourceFile:
		return b.T("prompts.source.badge.file")
	case fanout.PromptSourceCompiled:
		return b.T("prompts.source.badge.default")
	}
	return src
}

// resolveSlot resolves one slot through the P9 store: the effective text,
// the source rung label, and any resolution error (a corrupt slot file
// errors instead of degrading silently, so the badge can say so).
func resolveSlot(slot string) (text, source string, err error) {
	if slot == "seat" {
		text, err = fanout.ResolveSeatPreamble()
		return text, fanout.SeatPreambleSource(), err
	}
	text, err = fanout.ResolveTaskPreset(slot)
	return text, fanout.TaskPromptSource(slot), err
}

// syncRows re-resolves every slot and rebuilds the row list when the
// signature changed (a badge flip after an editor write). The cursor is
// kept and clamped into the new row range.
func (s *promptsScreen) syncRows() {
	rows := make([]Row, len(promptSlots))
	active := 0
	sigParts := make([]string, len(promptSlots))
	for i, slot := range promptSlots {
		_, source, err := resolveSlot(slot)
		note := sourceBadge(s.b, source)
		if err != nil {
			note = missingOr(s.b, "prompts.slot.unreadable", promptsSlotUnreadable)
		} else if source != fanout.PromptSourceCompiled {
			active++
		}
		rows[i] = Row{ID: slot, Label: s.slotLabel(slot), Note: note}
		sigParts[i] = slot + "\x00" + note
	}
	sig := strings.Join(sigParts, "\x01")
	if sig == s.rowsSig && s.rows != nil {
		return
	}
	cursor := s.list.Cursor()
	s.rows = rows
	s.rowsSig = sig
	s.list.SetRows(rows)
	s.list.SetCursor(cursor)
}

// statusLine counts the slots with an active override; with none the
// honest empty marker from the catalog renders instead.
func (s *promptsScreen) statusLine() string {
	active := 0
	for i := range promptSlots {
		if _, source, err := resolveSlot(promptSlots[i]); err == nil && source != fanout.PromptSourceCompiled {
			active++
		}
	}
	if active == 0 {
		return s.b.T("prompts.empty")
	}
	return fillHubSlots(missingOr(s.b, "prompts.status.count", promptsStatusCount), map[string]string{
		"active": strconv.Itoa(active),
		"total":  strconv.Itoa(len(promptSlots)),
	})
}

// activate is the stepList onPick callback, shared by digit selection and
// arrows+Enter. It forwards the slot ID to the shell-supplied editor
// factory; the editor screen itself re-resolves the effective text.
func (s *promptsScreen) activate(idx int) {
	if s.app == nil || idx < 0 || idx >= len(promptSlots) {
		return
	}
	if s.openEditor != nil {
		s.openEditor(promptSlots[idx], s.app)
	}
}

// Draw renders the override-count status line, the numbered slot rows
// with source badges, and the two-handled footer hint. All cell math goes
// through cellwidth. The window is sized against the real chrome of this
// frame: the fixed chrome rows plus the message block the composer appends
// under the body when a message is live, so the body fits the frame and the
// overflow path (which shrinks the body from its tail) never runs on a
// full-page list.
func (s *promptsScreen) Draw(a *App) []string {
	s.app = a
	s.syncRows()
	// The app message line is its own block under the body (a blank separator
	// plus the line) and the composer appends it after the body. The library
	// inherits the message a slot save leaves behind, so a full-page list has
	// to leave those two rows out of the window as well, or that frame cuts
	// the list tail.
	chrome := promptsListChrome
	if a.Message() != "" {
		chrome += 2
	}
	s.list.SetPageSize(pageSizeFor(a.Height(), chrome))
	lines := []string{cellwidth.Truncate(s.statusLine(), a.Width()), ""}
	lines = append(lines, s.list.Render(a.Width())...)
	lines = append(lines, "", s.list.FooterHint())
	return lines
}

// HandleKey delegates to the stepList. Esc never arrives here: the App
// precedence handles it first.
func (s *promptsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}
