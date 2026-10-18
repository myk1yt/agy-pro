package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// Action row indices of the prompt editor, in display order below the
// text area. Save also answers to Ctrl+S anywhere in the edit zone.
const (
	promptActionSave = iota
	promptActionReset
	promptActionCount
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved). The credential
// warning and gate lines never echo buffer content (names and shapes
// only), and no code path here logs the buffer.
const (
	// catalog-resolved, fallback kept: prompts.editor.status
	promptsEditorStatus = "Slot: {{slot}} · Source: {{badge}}"
	// catalog-resolved, fallback kept: prompts.editor.default_view
	promptsEditorDefaultView = "No override file exists. The text below is the compiled default, shown as a read-only reference. Saving writes a new override file."
	// catalog-resolved, fallback kept: prompts.editor.guardrail.seat
	promptsEditorSeatGuardrail = "The compiled seat preamble is anti-nesting control text for native seats. Editing it changes native seat behavior in every fanout run."
	// catalog-resolved, fallback kept: prompts.editor.save.title
	promptsEditorSaveTitle = "Save prompt?"
	// catalog-resolved, fallback kept: prompts.editor.save.body
	promptsEditorSaveBody = "Write {{path}} ({{bytes}} bytes) and make it the active override?"
	// catalog-resolved, fallback kept: prompts.editor.env_note
	promptsEditorEnvNote = "Note: env AGY_PRO_SEAT_PREAMBLE is set and still wins over the file layer."
	// catalog-resolved, fallback kept: prompts.editor.saved
	promptsEditorSaved = "Prompt saved: {{path}}"
	// catalog-resolved, fallback kept: prompts.editor.cred.warn
	promptsEditorCredWarn = "Warning: the buffer looks like secret or key material (content hidden). Prompt files are plain text."
	// catalog-resolved, fallback kept: prompts.editor.cred.title
	promptsEditorCredTitle = "Save secret-looking text?"
	// catalog-resolved, fallback kept: prompts.editor.cred.body
	promptsEditorCredBody = "The buffer matches credential shapes (a key prefix or a long opaque token). Prompt files are plain text. Save it anyway? The buffer content is never logged."
	// catalog-resolved, fallback kept: prompts.editor.reset.none
	promptsEditorResetNone = "No override file exists for this slot."
	// catalog-resolved, fallback kept: prompts.editor.reset.done
	promptsEditorResetDone = "Override file deleted: {{path}}. The compiled default is back in force."
	// catalog-resolved, fallback kept: prompts.editor.discard.title
	promptsEditorDiscardTitle = "Discard unsaved edits?"
	// catalog-resolved, fallback kept: prompts.editor.discard.body
	promptsEditorDiscardBody = "The edits on this prompt are dropped and nothing is written."
	// catalog-resolved, fallback kept: prompts.msg.cancelled
	promptsMsgCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: prompts.editor.save.note
	promptsEditorSaveNote = "(Ctrl+S)"
)

// promptTextLooksSecret is the conservative credential-shaped check for a
// whole multi-line prompt buffer. providers.LooksLikeAPIKey cannot be
// used: it flags any operand containing "-" or ".", which every natural
// language prompt contains. The local check scans line by line and flags
// only credential-shaped material, mirroring the model form classifier
// (screen_model_form.go) exactly: a known credential prefix head at any
// length (the shared secretValuePrefixes list), or one uninterrupted run
// of ASCII letters and digits, 16+ runes, with both classes present (the
// shared credentialTokenShaped shape, the keyaudit AKIA-class precedent:
// the old 24-rune floor here let a 16-23 rune credential skip the gate).
// Any separator rune (- / . _ : and friends) fails the shape, so ordinary
// prompt sentences, URLs, paths, and identifier-shaped model ids never
// match. The matching line is never returned or logged.
func promptTextLooksSecret(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		v := strings.TrimSpace(line)
		if v == "" {
			continue
		}
		lower := strings.ToLower(v)
		for _, p := range secretValuePrefixes {
			if strings.HasPrefix(lower, p) {
				return true
			}
		}
		if credentialTokenShaped(v) {
			return true
		}
	}
	return false
}

// normalizePromptSlot canonicalizes a slot ID the way the store
// normalizes task names (lowercase, trimmed, unknown names fall back to
// review), so a screen built from any alias edits the slot that would
// actually run.
func normalizePromptSlot(slot string) string {
	switch strings.ToLower(strings.TrimSpace(slot)) {
	case "seat":
		return "seat"
	case "security":
		return "security"
	case "plan-audit", "plan_audit":
		return "plan-audit"
	case "research":
		return "research"
	case "ideas":
		return "ideas"
	case "custom":
		return "custom"
	default:
		return "review"
	}
}

// promptEditorScreen is S10: the prompt editor (architect-report section
// 3, screen 10). The multi-line editor seeds with the EFFECTIVE text of
// one slot (the file or env override when one is in force, else the
// compiled default, labeled as a read-only reference because the
// compiled constant itself cannot be edited in place; saving writes a new
// override file). Save (Ctrl+S or the Save row) gates behind a confirm
// naming the path and byte count, and behind an extra credential gate
// when the buffer looks like secret material. Reset-to-default deletes
// the slot file behind the shared y/n gate and restores the compiled
// constant. All writes go through the P9 store (atomic 0600); reset uses
// os.Remove on the slot file because the store exposes no delete verb.
//
// Esc handling: the App dispatcher consumes Esc before any screen sees
// it (clears the message, then pops), so a dirty buffer would silently
// lose edits. The screen therefore follows the model form pattern: while
// the editor is dirty a transparent guard modal (promptEditorGuard)
// receives every key in CONFIRM scope before the App Esc precedence,
// forwards everything except Esc to the editor, and turns Esc on a dirty
// buffer into the discard-confirm. A clean Esc never arms the guard and
// pops through the plain App precedence.
type promptEditorScreen struct {
	slot     string
	lang     string
	b        *i18n.Bundle
	ed       *Editor
	actions  *stepList
	onAction bool
	source   string
	filePath string
	hasFile  bool
	loadErr  string
	app      *App
	guard    *promptEditorGuard
}

// NewPromptEditorScreen builds the editor for one prompt slot, seeded
// with the current effective text resolved through the P9 store. The UI
// language resolves from the persisted state (AGY_PRO_HOME state.json),
// defaulting to English.
func NewPromptEditorScreen(slot string) Screen {
	s := &promptEditorScreen{slot: normalizePromptSlot(slot)}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	s.setLang(i18n.Normalize(lang))

	text, source, rerr := resolveSlot(s.slot)
	if rerr != nil {
		s.loadErr = providers.MaskOperand(rerr.Error())
	}
	s.source = source
	s.ed = NewEditor(text)
	s.actions = newStepList(s.activateAction)
	s.actions.SetRows([]Row{
		{ID: "save", Label: s.b.T("common.save"), Note: missingOr(s.b, "prompts.editor.save.note", promptsEditorSaveNote)},
		{ID: "reset", Label: s.b.T("prompts.action.reset")},
	})

	if dir, derr := fanout.PromptsDir(); derr == nil {
		s.filePath = filepath.Join(dir, s.slot+".txt")
		if fi, ferr := os.Stat(s.filePath); ferr == nil && !fi.IsDir() {
			s.hasFile = true
		}
	} else {
		s.loadErr = providers.MaskOperand(derr.Error())
	}
	return s
}

// setLang loads the bundle for lang once per language.
func (s *promptEditorScreen) setLang(lang string) {
	if lang == s.lang && s.b != nil {
		return
	}
	s.lang = lang
	s.b = i18n.Load(lang)
}

// Mode is ModeEdit while the text area owns the keys and ModeNav on the
// action rows (Save / Reset to default).
func (s *promptEditorScreen) Mode() Mode {
	if s.onAction {
		return ModeNav
	}
	return ModeEdit
}

// Title resolves through the catalog.
func (s *promptEditorScreen) Title() string { return s.b.T("prompts.editor.title") }

// slotLabel resolves the display name of the edited slot.
func (s *promptEditorScreen) slotLabel() string {
	return s.b.T("prompts.slot." + promptSlotKey(s.slot))
}

// seatEnvSet reports whether the inline seat preamble env rung is in
// force (the file layer stays shadowed while it is set).
func seatEnvSet() bool {
	return os.Getenv(fanout.EnvSeatPreamble) != ""
}

// activateAction is the action-row onPick callback, shared by digits and
// arrows+Enter.
func (s *promptEditorScreen) activateAction(idx int) {
	switch idx {
	case promptActionSave:
		s.trySave()
	case promptActionReset:
		s.askReset()
	}
}

// dispatch routes one key between the action rows and the text area.
func (s *promptEditorScreen) dispatch(k Key) {
	if s.onAction {
		switch k {
		case KeyUp, KeyLeft:
			s.onAction = false
		default:
			s.actions.HandleKey(k)
		}
		return
	}
	switch k {
	case KeyTab:
		s.onAction = true
	case KeyCtrlS:
		s.trySave()
	default:
		s.ed.HandleKey(k)
	}
}

// HandleKey runs one key with no modal live: dispatch it, then arm the
// dirty guard. Esc never arrives here (App precedence).
func (s *promptEditorScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.dispatch(k)
	if s.ed.Dirty() && a.Modal() == nil {
		s.rearmGuard(a)
	}
}

// rearmGuard (re)opens the dirty guard. Callers running inside a modal
// key callback rely on ShowModal replacing the live modal, so the App
// stale-modal clear never removes a freshly armed one.
func (s *promptEditorScreen) rearmGuard(a *App) {
	if !s.ed.Dirty() {
		return
	}
	if s.guard == nil {
		s.guard = &promptEditorGuard{s: s}
	}
	a.ShowModal(s.guard)
}

// askDiscard opens the discard-confirm over a dirty buffer. y drops the
// edits and pops back to the library; n/Esc returns to the editor with
// the buffer preserved and the guard re-armed.
func (s *promptEditorScreen) askDiscard() {
	a := s.app
	if a == nil {
		return
	}
	a.ShowModal(NewConfirm(
		missingOr(s.b, "prompts.editor.discard.title", promptsEditorDiscardTitle),
		[]string{missingOr(s.b, "prompts.editor.discard.body", promptsEditorDiscardBody)},
		func(ok bool) {
			if !ok {
				s.rearmGuard(a)
				return
			}
			a.Pop()
			a.SetMessage(missingOr(s.b, "prompts.msg.cancelled", promptsMsgCancelled))
		}))
}

// trySave starts the save flow. A credential-shaped buffer first gets a
// warning on the message line plus its own y/n gate; either way the write
// itself is confirmed with the path and byte count before the store sees
// the text.
func (s *promptEditorScreen) trySave() {
	a := s.app
	if a == nil {
		return
	}
	if s.filePath == "" {
		a.SetMessage(s.b.T("common.error") + ": prompt directory unavailable")
		return
	}
	if promptTextLooksSecret(s.ed.Text()) {
		a.SetMessage(missingOr(s.b, "prompts.editor.cred.warn", promptsEditorCredWarn))
		a.ShowModal(NewConfirm(
			missingOr(s.b, "prompts.editor.cred.title", promptsEditorCredTitle),
			[]string{missingOr(s.b, "prompts.editor.cred.body", promptsEditorCredBody)},
			func(ok bool) {
				if !ok {
					a.SetMessage(missingOr(s.b, "prompts.msg.cancelled", promptsMsgCancelled))
					s.rearmGuard(a)
					return
				}
				s.askSaveConfirm()
			}))
		return
	}
	s.askSaveConfirm()
}

// askSaveConfirm opens the write gate: the exact target path and the
// byte count of the buffer (never the content). y writes through the
// store (atomic 0600) and pops back to the library with the saved
// message; n/Esc cancels.
func (s *promptEditorScreen) askSaveConfirm() {
	a := s.app
	if a == nil {
		return
	}
	text := s.ed.Text()
	body := []string{fillHubSlots(missingOr(s.b, "prompts.editor.save.body", promptsEditorSaveBody), map[string]string{
		"path":  providers.MaskOperand(s.filePath),
		"bytes": strconv.Itoa(len([]byte(text))),
	})}
	if s.slot == "seat" && seatEnvSet() {
		body = append(body, missingOr(s.b, "prompts.editor.env_note", promptsEditorEnvNote))
	}
	a.ShowModal(NewConfirm(
		missingOr(s.b, "prompts.editor.save.title", promptsEditorSaveTitle),
		body,
		func(ok bool) {
			if !ok {
				a.SetMessage(missingOr(s.b, "prompts.msg.cancelled", promptsMsgCancelled))
				s.rearmGuard(a)
				return
			}
			s.commit(a, text)
		}))
}

// commit runs the approved write through the P9 store (the only write
// path out of this screen) and pops back to the library.
func (s *promptEditorScreen) commit(a *App, text string) {
	var (
		path string
		err  error
	)
	if s.slot == "seat" {
		path, err = fanout.SaveSeatPrompt(text)
	} else {
		path, err = fanout.SaveTaskPrompt(s.slot, text)
	}
	if err != nil {
		a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
		s.rearmGuard(a)
		return
	}
	a.Pop()
	a.SetMessage(fillHubSlots(missingOr(s.b, "prompts.editor.saved", promptsEditorSaved),
		map[string]string{"path": providers.MaskOperand(path)}))
}

// askReset gates the reset-to-default action behind the shared y/n gate.
// With no slot file on disk there is nothing to delete and the user gets
// an explicit message instead of a silent no-op. y deletes the file (the
// compiled constant comes back in force) and pops back to the library.
func (s *promptEditorScreen) askReset() {
	a := s.app
	if a == nil {
		return
	}
	if !s.hasFile {
		a.SetMessage(missingOr(s.b, "prompts.editor.reset.none", promptsEditorResetNone))
		return
	}
	body := []string{s.b.Tf("prompts.reset.body", map[string]string{"slot": s.slotLabel()})}
	if s.slot == "seat" && seatEnvSet() {
		body = append(body, missingOr(s.b, "prompts.editor.env_note", promptsEditorEnvNote))
	}
	a.ShowModal(NewConfirm(
		s.b.T("prompts.reset.title"),
		body,
		func(ok bool) {
			if !ok {
				a.SetMessage(missingOr(s.b, "prompts.msg.cancelled", promptsMsgCancelled))
				return
			}
			s.resetFile(a)
		}))
}

// resetFile deletes the slot file. A missing file at delete time is not
// an error (the goal state already holds); anything else is reported
// honestly with the path.
func (s *promptEditorScreen) resetFile(a *App) {
	if err := os.Remove(s.filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.SetMessage(s.b.T("common.error") + ": " + providers.MaskOperand(err.Error()))
		return
	}
	a.Pop()
	a.SetMessage(fillHubSlots(missingOr(s.b, "prompts.editor.reset.done", promptsEditorResetDone),
		map[string]string{"path": providers.MaskOperand(s.filePath)}))
}

// Draw renders the slot status (display name plus source badge), the
// read-only-reference label when no override exists, the seat guardrail,
// the editor viewport, the action rows, and the hint. The hint is
// rendered by the guard modal while it is armed, so it is skipped here in
// that case. All cell math goes through cellwidth.
func (s *promptEditorScreen) Draw(a *App) []string {
	s.app = a
	s.ed.SetPageSize(pageSizeFor(a.Height(), 9))
	var lines []string
	if s.loadErr != "" {
		lines = append(lines, cellwidth.Truncate(s.b.T("common.error")+": "+s.loadErr, a.Width()))
	}
	status := fillHubSlots(missingOr(s.b, "prompts.editor.status", promptsEditorStatus), map[string]string{
		"slot":  s.slotLabel(),
		"badge": sourceBadge(s.b, s.source),
	})
	lines = append(lines, cellwidth.Truncate(status, a.Width()))
	if !s.hasFile && s.source == fanout.PromptSourceCompiled {
		lines = append(lines, cellwidth.Truncate(missingOr(s.b, "prompts.editor.default_view", promptsEditorDefaultView), a.Width()))
	}
	if s.slot == "seat" {
		lines = append(lines, cellwidth.Truncate(missingOr(s.b, "prompts.editor.guardrail.seat", promptsEditorSeatGuardrail), a.Width()))
	}
	lines = append(lines, "")
	rows, _ := s.ed.View()
	for _, r := range rows {
		lines = append(lines, cellwidth.Truncate(r, a.Width()))
	}
	lines = append(lines, "")
	for i := 0; i < promptActionCount; i++ {
		marker := "  "
		if s.onAction && s.actions.Cursor() == i {
			marker = "▶ "
		}
		row := s.actions.Row(i)
		line := marker + row.Label
		if row.Note != "" {
			line += "  " + row.Note
		}
		lines = append(lines, cellwidth.Truncate(line, a.Width()))
	}
	if a.Modal() != s.guard {
		lines = append(lines, "", cellwidth.Truncate(s.b.T("prompts.editor.hint"), a.Width()))
	}
	return lines
}

// promptEditorGuard is the transparent dirty-buffer gate. While it is
// armed (the buffer is dirty) it receives every key in CONFIRM scope
// before the App Esc precedence: it forwards everything except Esc to the
// editor dispatch, and Esc on a dirty buffer opens the discard-confirm.
// Its body carries the editor hint, so the armed gate doubles as the
// footer, exactly like the model form guard.
type promptEditorGuard struct {
	s *promptEditorScreen
}

func (g *promptEditorGuard) Title() string { return "" }

func (g *promptEditorGuard) Body(w int) []string {
	return []string{g.s.b.T("prompts.editor.hint")}
}

func (g *promptEditorGuard) Footer() string { return "" }

// Key forwards one key. Esc on a dirty buffer opens the discard-confirm
// and keeps the gate; Esc on a clean buffer closes the gate (the Esc is
// consumed, and the App pop happens on a further Esc, plain precedence).
func (g *promptEditorGuard) Key(k Key, a *App) bool {
	s := g.s
	if k == KeyEsc {
		if !s.ed.Dirty() {
			return false
		}
		s.askDiscard()
		return true
	}
	s.dispatch(k)
	if s.ed.Dirty() && a.Modal() == nil {
		a.ShowModal(g)
	}
	return true
}
