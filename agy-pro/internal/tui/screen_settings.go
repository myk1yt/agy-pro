package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/swarm"
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved); each const names
// its key and stays as the fallback for a catalog lacking the translation.
// Every value avoids the em-dash (house rule: hyphens in user-facing text).
const (
	// catalog-resolved, fallback kept: settings.prompt.add_json
	settingsPromptAddJSON = "JSON file path"
	// catalog-resolved, fallback kept: settings.prompt.provider_name
	settingsPromptProviderName = "Provider name"
	// catalog-resolved, fallback kept: settings.prompt.provider_old
	settingsPromptProviderOld = "Current provider name"
	// catalog-resolved, fallback kept: settings.prompt.provider_new
	settingsPromptProviderNew = "New provider name"
	// catalog-resolved, fallback kept: settings.prompt.delete_name
	settingsPromptDeleteName = "Provider name to delete (registry entry only)"
	// catalog-resolved, fallback kept: settings.prompt.fix_targets
	settingsPromptFixTargets = "fix-keys target (all | provider list)"
	// catalog-resolved, fallback kept: settings.prompt.backup_dir
	settingsPromptBackupDir = "Backup dir printed by fix-keys"
	// catalog-resolved, fallback kept: settings.prompt.preset_name
	settingsPromptPresetName = "Preset name"
	// catalog-resolved, fallback kept: settings.prompt.preset_numbers
	settingsPromptPresetNumbers = "Numbers (e.g. 1,3,19,23)"
	// catalog-resolved, fallback kept: settings.prompt.hint
	settingsPromptHint = "Ctrl+S submits - Tab moves to OK - Esc cancels"
	// catalog-resolved, fallback kept: settings.msg.wizard
	settingsMsgWizard = "Interactive wizard: run `agy pro setup` (or `agy pro settings providers add-interactive`). Raw key echo is masked first-5 + ****."
	// catalog-resolved, fallback kept: settings.msg.cancelled
	settingsMsgCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: settings.msg.backups_none
	settingsMsgBackupsNone = "(no backups)"
	// catalog-resolved, fallback kept: settings.msg.presets_none
	settingsMsgPresetsNone = "(no saved presets; p1/p2/p3 defaults always available)"
	// catalog-resolved, fallback kept: settings.msg.readonly
	settingsMsgReadonly = "read-only: {{note}}"
	// catalog-resolved, fallback kept: settings.result.hint
	settingsResultHint = "Arrows and PgUp/PgDn scroll. Esc returns."
	// catalog-resolved, fallback kept: settings.approval.add
	settingsApprovalAdd = "Add provider {{name}} to the registry."
	// catalog-resolved, fallback kept: settings.approval.replace
	settingsApprovalReplace = "Replace provider {{name}} wholesale: the current entry and its whole model list are discarded."
	// catalog-resolved, fallback kept: settings.approval.rename
	settingsApprovalRename = "Rename provider {{old}} to {{new}}; the model list carries over unchanged."
	// catalog-resolved, fallback kept: settings.approval.fix_keys
	settingsApprovalFixKeys = "Repair BOM-variant key names for {{target}}; an entry is rewritten only when its clean name probes dead and a BOM variant probes valid."
	// catalog-resolved, fallback kept: settings.approval.fix_keys_write
	settingsApprovalFixKeysWrite = "A DPAPI-encrypted backup of every touched value is written (0600, atomic) before any change; providers.json is not modified."
	// catalog-resolved, fallback kept: settings.approval.fix_target
	settingsApprovalFixTarget = "  {{env}} (provider {{provider}})"
	// catalog-resolved, fallback kept: settings.approval.rollback
	settingsApprovalRollback = "Restore registry key names from backup {{dir}}; entries recorded as absent are deleted, the rest are rewritten."
	// catalog-resolved, fallback kept: settings.approval.rollback_write
	settingsApprovalRollbackWrite = "No file is written; the user environment store is mutated in place."
	// catalog-resolved, fallback kept: settings.approval.rollback_restore
	settingsApprovalRollbackRestore = "  {{name}}: restore recorded value"
	// catalog-resolved, fallback kept: settings.approval.rollback_delete
	settingsApprovalRollbackDelete = "- {{name}}: delete (recorded as absent)"
)

// localizeItem resolves the catalog strings for one settings row. The label
// resolves view.<MsgID>.label and the note view.<MsgID>.note; variant notes
// (the launcher env-override sentence) live at view.<MsgID>.note.env and are
// tried when the base note key is absent. Every lookup falls back to the
// compiled English Label/Note carried by the item, so an unknown key can
// never blank a row. Dynamic provider-key rows (MsgID "provider.key") fill
// the {{name}} slot from the item ID.
func localizeItem(b *i18n.Bundle, it settings.Item) (label, note string) {
	label, note = it.Label, it.Note
	if it.MsgID == "" {
		return label, note
	}
	slots := map[string]string{"name": viewItemName(it)}
	key := "view." + it.MsgID + ".label"
	if s := b.Tf(key, slots); s != key {
		label = s
	}
	noteKey := "view." + it.MsgID + ".note"
	if s := b.Tf(noteKey, slots); s != noteKey {
		note = s
	} else if it.Note != "" {
		variant := noteKey + ".env"
		if s := b.Tf(variant, slots); s != variant {
			note = s
		}
	}
	return label, note
}

// viewItemName extracts the {{name}} slot for the dynamic provider-key rows
// (item ID "provider.key.<name>"); every other row resolves without a slot.
func viewItemName(it settings.Item) string {
	const pfx = "provider.key."
	if strings.HasPrefix(it.ID, pfx) {
		return strings.TrimPrefix(it.ID, pfx)
	}
	return ""
}

// settingsScreen is the settings surface of the setup TUI: it hosts the
// Core.View() read model (the same rows the old `agy pro settings` panel and
// `show --json` render). One section at a time: Left/Right switch sections,
// the section's items render through the shared stepList with the row note
// carrying the value and source, and a detail block under the list shows the
// localized note, default, and choices of the cursor row. Item labels and
// notes resolve through the catalog (view.<MsgID>.label/.note) with the
// compiled English as the fallback. Activation mirrors the old panel map:
// choice items open the M2 numbered overlay, danger items gate behind the
// y/n confirm, free-text items open the inline editor, and every write goes
// through the same Core verbs (Core.Set for the persisted keys).
type settingsScreen struct {
	core    *settings.Core
	bundle  *i18n.Bundle
	view    settings.SettingsView
	loadErr error
	section int
	list    *stepList
	app     *App
}

// NewSettingsScreen builds the settings screen over core. A nil core builds
// one with the real defaults. The screen binds the active bundle at
// construction (lang.go Bundle), so it follows the shell's language.
func NewSettingsScreen(core *settings.Core) Screen {
	if core == nil {
		core = settings.NewCore(settings.Deps{})
	}
	s := &settingsScreen{core: core, bundle: Bundle()}
	s.list = newStepList(s.pick)
	s.reload()
	return s
}

// Title resolves through the catalog.
func (s *settingsScreen) Title() string { return s.bundle.T("settings.screen.title") }

// Mode is ModeNav: the item list drives everything through the shared
// stepList, so digits and arrows are both always live.
func (s *settingsScreen) Mode() Mode { return ModeNav }

// reload re-reads the Core view after a write verb and rebuilds the rows,
// keeping the cursor inside the (possibly shorter) section.
func (s *settingsScreen) reload() {
	view, err := s.core.View()
	if err != nil {
		s.loadErr = err
		return
	}
	s.loadErr = nil
	s.view = view
	if s.section >= len(view.Sections) {
		s.section = 0
	}
	s.rebuild()
}

// rebuild composes the stepList rows of the current section: the localized
// label in the row body, the value and source in the row note.
func (s *settingsScreen) rebuild() {
	items := s.items()
	rows := make([]Row, len(items))
	for i, it := range items {
		label, _ := localizeItem(s.bundle, it)
		rows[i] = Row{
			ID:    it.ID,
			Label: label,
			Note:  it.Value + "  (" + it.Source + ")",
		}
	}
	s.list.SetRows(rows)
}

// items returns the items of the current section (nil on a load error).
func (s *settingsScreen) items() []settings.Item {
	if s.loadErr != nil || s.section >= len(s.view.Sections) {
		return nil
	}
	return s.view.Sections[s.section].Items
}

// sectionTitle renders the current section header with the position counter.
// The title resolves through the catalog (section.pN.title) with the view's
// compiled title as the fallback.
func (s *settingsScreen) sectionTitle() string {
	if s.section >= len(s.view.Sections) {
		return ""
	}
	sec := s.view.Sections[s.section]
	title := sec.Title
	key := "section." + sec.ID + ".title"
	if t := s.bundle.T(key); t != key {
		title = t
	}
	return fmt.Sprintf("%s  (%d/%d)", title, s.section+1, len(s.view.Sections))
}

// Draw renders the section header, the item window, the cursor row detail,
// and the two-handled legend. All cell math goes through cellwidth.
func (s *settingsScreen) Draw(a *App) []string {
	s.app = a
	w := a.Width()
	if s.loadErr != nil {
		return []string{cellwidth.Truncate(s.bundle.Tf("err.load_failed",
			map[string]string{"err": providers.MaskOperand(s.loadErr.Error())}), w)}
	}
	s.list.SetPageSize(pageSizeFor(a.Height(), 12))
	lines := []string{cellwidth.Truncate(s.sectionTitle(), w)}
	lines = append(lines, s.list.Render(w)...)
	if it, ok := s.cursorItem(); ok {
		lines = append(lines, "")
		lines = append(lines, s.detailLines(it, w)...)
	}
	return append(lines, "", s.list.FooterHint())
}

// cursorItem returns the item under the list cursor, if any.
func (s *settingsScreen) cursorItem() (settings.Item, bool) {
	items := s.items()
	if len(items) == 0 || s.list.Cursor() >= len(items) {
		return settings.Item{}, false
	}
	return items[s.list.Cursor()], true
}

// detailLines renders the cursor row's localized label, value, default,
// source, note, choices, and danger flag (the old panel's right pane,
// localized through the catalog).
func (s *settingsScreen) detailLines(it settings.Item, w int) []string {
	label, note := localizeItem(s.bundle, it)
	lines := []string{
		"⎿ " + label,
		"    value   : " + it.Value,
		"    default : " + dashEmpty(it.Default),
		"    source  : " + it.Source,
	}
	if note != "" {
		lines = append(lines, "    note    : "+note)
	}
	if len(it.Choices) > 0 {
		lines = append(lines, "    choices : "+strings.Join(it.Choices, " | ")+" (numbered)")
	}
	if it.Danger {
		lines = append(lines, "    danger  : y/n confirmation required")
	}
	for i := range lines {
		lines[i] = cellwidth.Truncate(lines[i], w)
	}
	return lines
}

// HandleKey routes one key. Left/Right switch sections (the list consumes
// everything else); the app pointer is kept for the activation callbacks.
func (s *settingsScreen) HandleKey(k Key, a *App) {
	s.app = a
	switch k {
	case KeyLeft:
		if s.section > 0 {
			s.section--
			s.rebuild()
		}
		return
	case KeyRight:
		if s.section < len(s.view.Sections)-1 {
			s.section++
			s.rebuild()
		}
		return
	}
	s.list.HandleKey(k)
}

// pick is the shared stepList activation callback (digits and Enter both
// land here).
func (s *settingsScreen) pick(idx int) {
	a := s.app
	if a == nil {
		return
	}
	items := s.items()
	if idx < 0 || idx >= len(items) {
		return
	}
	s.activate(items[idx], a)
}

// activate opens the op behind one item, mirroring the old panel's item map.
// Choice items open the M2 numbered overlay; danger items gate behind the
// y/n confirm; free-text items open the inline editor; read-only items only
// set a message. Every write goes through a Core verb.
func (s *settingsScreen) activate(it settings.Item, a *App) {
	label, note := localizeItem(s.bundle, it)
	switch {
	case len(it.Choices) > 0:
		s.openChoices(it, a)
	case it.ID == "mode.restore":
		s.confirmRun(a, label, note, nil, func() error { return s.core.ModeRestore() })
	case it.ID == "mode.backups":
		s.showResult(a, label, s.backupLines())
	case it.ID == "mode.status":
		var b bytes.Buffer
		err := s.core.ModeStatus(&b)
		s.showResult(a, label, trimLines(b.String()))
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: " + err.Error()))
		}
	case it.ID == "providers.add_interactive":
		a.SetMessage(missingOr(s.bundle, "settings.msg.wizard", settingsMsgWizard))
	case it.ID == "providers.add_json":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.add_json", settingsPromptAddJSON), "", func(text string, a *App) {
			s.addJSON(label, text, a)
		})
	case it.ID == "providers.test":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.provider_name", settingsPromptProviderName), "", func(text string, a *App) {
			s.testProvider(text, a)
		})
	case it.ID == "providers.rename":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.provider_old", settingsPromptProviderOld), "", func(old string, a *App) {
			s.prompt(a, missingOr(s.bundle, "settings.prompt.provider_new", settingsPromptProviderNew), "", func(neu string, a *App) {
				s.renameProvider(label, old, neu, a)
			})
		})
	case it.ID == "providers.delete":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.delete_name", settingsPromptDeleteName), "", func(text string, a *App) {
			s.approveProviderDelete(text, a)
		})
	case it.ID == "doctor.diagnose":
		s.runDiagnose(a, label)
	case it.ID == "doctor.fix_keys":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.fix_targets", settingsPromptFixTargets), "", func(text string, a *App) {
			s.approveFixKeys(label, text, a)
		})
	case it.ID == "doctor.rollback_keys":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.backup_dir", settingsPromptBackupDir), "", func(text string, a *App) {
			s.approveRollbackKeys(label, text, a)
		})
	case it.ID == "swarm.presets":
		s.showResult(a, label, s.presetLines())
	case it.ID == "swarm.save_preset":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.preset_name", settingsPromptPresetName), "", func(name string, a *App) {
			s.prompt(a, missingOr(s.bundle, "settings.prompt.preset_numbers", settingsPromptPresetNumbers), "", func(nums string, a *App) {
				s.savePreset(label, name, nums, a)
			})
		})
	case it.ReadOnly:
		a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.readonly", settingsMsgReadonly),
			map[string]string{"note": note}))
	default:
		a.SetMessage(label + ": " + note)
	}
}

// openChoices opens the M2 numbered overlay over the item's choices. The
// appearance rows translate their display values (language endonyms map back
// to codes, show/hide maps to on/off for Core.Set); every other choice item
// writes its value verbatim.
func (s *settingsScreen) openChoices(it settings.Item, a *App) {
	label, _ := localizeItem(s.bundle, it)
	choices := append([]string(nil), it.Choices...)
	codes := []string(nil)
	switch it.ID {
	case "current_mode", "mode.switch":
		choices = []string{"full", "clean"}
	case "appearance.language":
		for _, m := range i18n.Languages() {
			choices = append(choices, m.Endonym)
			codes = append(codes, m.Code)
		}
	case "appearance.launcher_menu":
		choices = []string{
			missingOr(s.bundle, "settings.appearance.launcher_menu.show", "show"),
			missingOr(s.bundle, "settings.appearance.launcher_menu.hide", "hide"),
		}
	}
	a.ShowModal(NewChoice(label, choices, func(idx int, value string) {
		if idx < 0 {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		s.applyChoice(it, label, value, codes, idx, a)
	}))
}

// applyChoice writes one chosen value through the Core verb behind the item.
// Danger items (mode switches) gate behind the y/n confirm first, exactly
// like the old panel's choice flow.
func (s *settingsScreen) applyChoice(it settings.Item, label, value string, codes []string, idx int, a *App) {
	_, note := localizeItem(s.bundle, it)
	run := func() error {
		switch it.ID {
		case "current_mode", "mode.switch":
			return s.core.ModeSwitch(value)
		case "appearance.language":
			code := value
			if idx >= 0 && idx < len(codes) {
				code = codes[idx]
			}
			return s.core.Set("language", code)
		case "appearance.launcher_menu":
			v := "off"
			if value == missingOr(s.bundle, "settings.appearance.launcher_menu.show", "show") {
				v = "on"
			}
			return s.core.Set("launcher_menu", v)
		default:
			return s.core.Set(it.ID, value)
		}
	}
	commit := func() {
		if err := run(); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(value)...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label + " = " + value)
	}
	if it.Danger {
		a.ShowModal(NewConfirm(label, []string{note}, func(ok bool) {
			if !ok {
				a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
				return
			}
			commit()
		}))
		return
	}
	commit()
}

// confirmRun gates a destructive one-shot op (mode restore) behind the
// shared y/n confirm and stamps the outcome on the message line. Error
// echoes are masked seeded with the prompted operands. providers.json and
// key-repair writes gate behind the M4 approval modal instead (see
// approveProviderAdd, approveProviderDelete, approveFixKeys,
// approveRollbackKeys).
func (s *settingsScreen) confirmRun(a *App, label, note string, operands []string, run func() error) {
	a.ShowModal(NewConfirm(label, []string{note}, func(ok bool) {
		if !ok {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := run(); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), operands...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// approveProviderDelete opens the M4 approval modal with the exact mutation
// summary for one provider delete: the registry-entry-only sentence (name
// masked), the model count before/after (N -> 0), the atomic-0600 sentence,
// and the removed diff line (base URL masked). y is the only path to
// Core.ProviderDelete; n/Esc writes nothing.
func (s *settingsScreen) approveProviderDelete(name string, a *App) {
	reg, ok := s.approvalRegistry(a)
	if !ok {
		return
	}
	p := findProvider(reg, name)
	if p == nil {
		a.SetMessage(providers.MaskSentence("error: provider not found", providers.FlagSeedOperands(name)...))
		return
	}
	masked := providers.MaskOperand(p.Name)
	summary := []string{
		s.bundle.Tf("providers.delete.body", map[string]string{"name": masked}),
		s.countsLine(len(p.Models), 0),
		s.bundle.T("modal.approval.body"),
		s.bundle.Tf("modal.approval.diff_removed", map[string]string{
			"line": s.providerLine(p.Name, p.BaseURL, len(p.Models)),
		}),
	}
	a.ShowModal(NewApproval(s.bundle.T("providers.delete.title"), summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := s.core.ProviderDelete(p.Name); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(name)...))
			return
		}
		s.reload()
		a.SetMessage(s.bundle.Tf("providers.delete.done", map[string]string{"name": masked}))
	}))
}

// addJSON reads the prompted path and parses the provider spec (both
// read-only). The write itself opens the M4 approval modal with the exact
// mutation summary; y is the only path to Core.ProviderAdd. A read or parse
// failure is masked seeded with the path (a misdirected paste in the path
// slot must not echo raw).
func (s *settingsScreen) addJSON(label, path string, a *App) {
	data, err := os.ReadFile(path)
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(path)...))
		return
	}
	spec, err := settings.ParseProviderSpecJSON(data)
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(path)...))
		return
	}
	s.approveProviderAdd(label, spec, path, a)
}

// approveProviderAdd opens the M4 approval modal with the exact mutation
// summary for one provider add: the action sentence, the model count
// before/after, the atomic-0600 sentence, and the diff line(s). A same-named
// provider renders as a wholesale replace (removed line + added line and the
// before/after model counts), so a silent model-list wipe is visible before
// y. y runs Core.ProviderAdd; n/Esc writes nothing.
func (s *settingsScreen) approveProviderAdd(label string, spec settings.ProviderSpec, path string, a *App) {
	reg, ok := s.approvalRegistry(a)
	if !ok {
		return
	}
	masked := providers.MaskOperand(spec.Name)
	after := len(spec.Models)
	existing := findProvider(reg, spec.Name)
	var summary []string
	if existing == nil {
		summary = []string{
			fillHubSlots(missingOr(s.bundle, "settings.approval.add", settingsApprovalAdd), map[string]string{"name": masked}),
			s.countsLine(0, after),
			s.bundle.T("modal.approval.body"),
			s.bundle.Tf("modal.approval.diff_added", map[string]string{
				"line": s.providerLine(spec.Name, spec.BaseURL, after),
			}),
		}
	} else {
		summary = []string{
			fillHubSlots(missingOr(s.bundle, "settings.approval.replace", settingsApprovalReplace), map[string]string{"name": masked}),
			s.countsLine(len(existing.Models), after),
			s.bundle.T("modal.approval.body"),
			s.bundle.Tf("modal.approval.diff_removed", map[string]string{
				"line": s.providerLine(existing.Name, existing.BaseURL, len(existing.Models)),
			}),
			s.bundle.Tf("modal.approval.diff_added", map[string]string{
				"line": s.providerLine(spec.Name, spec.BaseURL, after),
			}),
		}
	}
	a.ShowModal(NewApproval(s.bundle.T("providers.add.title"), summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := s.core.ProviderAdd(spec); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(path)...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// testProvider probes one provider through Core.ProviderTest and reports the
// verdict on the message line. Operands render masked.
func (s *settingsScreen) testProvider(name string, a *App) {
	masked := providers.MaskOperand(name)
	a.SetMessage(s.bundle.Tf("providers.test.running", map[string]string{"name": masked}))
	if err := s.core.ProviderTest(name); err != nil {
		a.SetMessage(s.bundle.Tf("providers.test.fail", map[string]string{
			"name": masked,
			"err":  providers.MaskOperand(err.Error()),
		}))
		return
	}
	a.SetMessage(s.bundle.Tf("providers.test.ok", map[string]string{"name": masked}))
}

// renameProvider opens the M4 approval modal with the exact mutation summary
// for one registry rename: the action sentence, the model count before/after
// (carried over unchanged), the atomic-0600 sentence, and the removed/added
// diff lines. y is the only path to Core.ProviderRename; n/Esc writes
// nothing. The error may echo either prompted name, so both seed the mask.
func (s *settingsScreen) renameProvider(label, old, neu string, a *App) {
	seed := append(providers.FlagSeedOperands(old), providers.FlagSeedOperands(neu)...)
	reg, ok := s.approvalRegistry(a)
	if !ok {
		return
	}
	p := findProvider(reg, old)
	if p == nil {
		a.SetMessage(providers.MaskSentence("error: provider not found", providers.FlagSeedOperands(old)...))
		return
	}
	before := len(p.Models)
	summary := []string{
		fillHubSlots(missingOr(s.bundle, "settings.approval.rename", settingsApprovalRename), map[string]string{
			"old": providers.MaskOperand(p.Name),
			"new": providers.MaskOperand(neu),
		}),
		s.countsLine(before, before),
		s.bundle.T("modal.approval.body"),
		s.bundle.Tf("modal.approval.diff_removed", map[string]string{
			"line": s.providerLine(p.Name, p.BaseURL, before),
		}),
		s.bundle.Tf("modal.approval.diff_added", map[string]string{
			"line": s.providerLine(neu, p.BaseURL, before),
		}),
	}
	a.ShowModal(NewApproval(s.bundle.T("providers.rename.title"), summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := s.core.ProviderRename(old, neu); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), seed...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// approveFixKeys opens the M4 approval modal for the doctor fix-keys write.
// The summary carries the action sentence, the registry model count
// before/after (key-name repair never touches models, so the numbers are
// equal and any future drift is visible), the honest backup sentence, and
// one diff-context line per resolved target env name (provider masked). The
// exact per-entry mutation cannot be known before the live probes run; the
// sentence states the precondition so the approval is not broader than the
// op. y is the only path to Core.DoctorFixKeys; n/Esc writes nothing.
func (s *settingsScreen) approveFixKeys(label, target string, a *App) {
	reg, ok := s.approvalRegistry(a)
	if !ok {
		return
	}
	names, err := providers.ResolveRepairTargets(reg, target)
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(target)...))
		return
	}
	total := registryModelTotal(reg)
	summary := []string{
		fillHubSlots(missingOr(s.bundle, "settings.approval.fix_keys", settingsApprovalFixKeys), map[string]string{
			"target": providers.MaskOperand(target),
		}),
		s.countsLine(total, total),
		missingOr(s.bundle, "settings.approval.fix_keys_write", settingsApprovalFixKeysWrite),
	}
	for _, env := range names {
		providerName := ""
		for i := range reg.Providers {
			if strings.TrimSpace(reg.Providers[i].APIKeyEnv) == env {
				providerName = reg.Providers[i].Name
				break
			}
		}
		summary = append(summary, fillHubSlots(missingOr(s.bundle, "settings.approval.fix_target", settingsApprovalFixTarget), map[string]string{
			"env":      providers.MaskOperand(env),
			"provider": providers.MaskOperand(providerName),
		}))
	}
	a.ShowModal(NewApproval(label, summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := s.core.DoctorFixKeys(target); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(target)...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// keyBackupSnapshot mirrors the on-disk key-backup layout for the rollback
// approval diff. Only entry NAMES and existence flags are read; values never
// reach the TUI. The file is untrusted input: names render masked, and the
// write verb re-validates provenance and shape after y.
type keyBackupSnapshot struct {
	Entries []struct {
		Name    string `json:"name"`
		Existed bool   `json:"existed"`
	} `json:"entries"`
}

// approveRollbackKeys opens the M4 approval modal for the doctor
// rollback-keys write. The summary carries the action sentence, the registry
// model count before/after (equal, as for fix-keys), the honest no-file
// sentence, and one diff line per backup entry (restore or delete, name
// masked). y is the only path to Core.DoctorRollbackKeys; n/Esc writes
// nothing.
func (s *settingsScreen) approveRollbackKeys(label, backupDir string, a *App) {
	reg, ok := s.approvalRegistry(a)
	if !ok {
		return
	}
	file := providers.ResolveBackupFile(backupDir)
	raw, err := os.ReadFile(file)
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(backupDir)...))
		return
	}
	var snap keyBackupSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(backupDir)...))
		return
	}
	if len(snap.Entries) == 0 {
		a.SetMessage(providers.MaskSentence("error: backup records no entries", providers.FlagSeedOperands(backupDir)...))
		return
	}
	total := registryModelTotal(reg)
	summary := []string{
		fillHubSlots(missingOr(s.bundle, "settings.approval.rollback", settingsApprovalRollback), map[string]string{
			"dir": providers.MaskOperand(backupDir),
		}),
		s.countsLine(total, total),
		missingOr(s.bundle, "settings.approval.rollback_write", settingsApprovalRollbackWrite),
	}
	for _, e := range snap.Entries {
		if e.Existed {
			summary = append(summary, fillHubSlots(missingOr(s.bundle, "settings.approval.rollback_restore", settingsApprovalRollbackRestore), map[string]string{
				"name": providers.MaskOperand(e.Name),
			}))
		} else {
			summary = append(summary, fillHubSlots(missingOr(s.bundle, "settings.approval.rollback_delete", settingsApprovalRollbackDelete), map[string]string{
				"name": providers.MaskOperand(e.Name),
			}))
		}
	}
	a.ShowModal(NewApproval(label, summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		if err := s.core.DoctorRollbackKeys(backupDir); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(backupDir)...))
			return
		}
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// approvalRegistry loads the live registry for a pre-write diff. A missing
// providers.json is the normal pre-setup state (empty registry). Any other
// read failure aborts the approval: no gate opens on an unknown before-state
// and nothing is written.
func (s *settingsScreen) approvalRegistry(a *App) (*providers.Registry, bool) {
	reg, err := providers.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &providers.Registry{}, true
		}
		a.SetMessage(s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
		return nil, false
	}
	return reg, true
}

// countsLine renders the shared model-count before/after approval line.
func (s *settingsScreen) countsLine(before, after int) string {
	return s.bundle.Tf("modal.approval.counts", map[string]string{
		"before": strconv.Itoa(before),
		"after":  strconv.Itoa(after),
	})
}

// providerLine renders one diff-context line for a registry entry: the
// masked provider name, the userinfo-stripped base URL, and the model count.
func (s *settingsScreen) providerLine(name, baseURL string, models int) string {
	return fmt.Sprintf("provider %s (%s, %s)", providers.MaskOperand(name), maskBaseURL(baseURL), s.bundle.Tn("menu.count.models", models))
}

// findProvider returns the registry entry for name (case-insensitive, like
// the Core verbs) or nil.
func findProvider(reg *providers.Registry, name string) *providers.Provider {
	for i := range reg.Providers {
		if strings.EqualFold(reg.Providers[i].Name, name) {
			return &reg.Providers[i]
		}
	}
	return nil
}

// registryModelTotal counts every model in the registry: the honest
// before/after number for key-repair ops, which never touch model lists.
func registryModelTotal(reg *providers.Registry) int {
	total := 0
	for i := range reg.Providers {
		total += len(reg.Providers[i].Models)
	}
	return total
}

// savePreset appends one numbered seat preset through Core.SwarmSavePresets.
func (s *settingsScreen) savePreset(label, name, nums string, a *App) {
	presets, err := s.core.SwarmPresets()
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(name, nums)...))
		return
	}
	presets = append(presets, swarm.SavedPreset{Name: name, Numbers: nums})
	if err := s.core.SwarmSavePresets(presets); err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(name, nums)...))
		return
	}
	s.reload()
	a.SetMessage("[SUCCESS] " + label)
}

// runDiagnose runs the doctor report into a buffer and pushes it as a
// read-only result screen; a structural failure appends the masked error.
func (s *settingsScreen) runDiagnose(a *App, label string) {
	var b bytes.Buffer
	err := s.core.DoctorDiagnose(&b, false)
	lines := trimLines(b.String())
	if err != nil {
		lines = append(lines, s.bundle.Tf("doctor.structural_fail",
			map[string]string{"err": providers.MaskOperand(err.Error())}))
	}
	s.showResult(a, label, lines)
}

// backupLines composes the mode backup listing.
func (s *settingsScreen) backupLines() []string {
	paths, err := s.core.ModeBackups()
	if err != nil {
		return []string{s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())})}
	}
	if len(paths) == 0 {
		return []string{missingOr(s.bundle, "settings.msg.backups_none", settingsMsgBackupsNone)}
	}
	lines := make([]string, len(paths))
	for i, p := range paths {
		lines[i] = fmt.Sprintf("backup %d: %s", i+1, providers.MaskOperand(p))
	}
	return lines
}

// presetLines composes the saved seat preset listing.
func (s *settingsScreen) presetLines() []string {
	presets, err := s.core.SwarmPresets()
	if err != nil {
		return []string{s.bundle.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())})}
	}
	if len(presets) == 0 {
		return []string{missingOr(s.bundle, "settings.msg.presets_none", settingsMsgPresetsNone)}
	}
	lines := make([]string, len(presets))
	for i, p := range presets {
		lines[i] = fmt.Sprintf("%s = %s", p.Name, p.Numbers)
	}
	return lines
}

// showResult pushes the read-only scroll window for multi-line op output.
func (s *settingsScreen) showResult(a *App, title string, lines []string) {
	a.Push(newSettingsResultScreen(title, lines))
}

// prompt pushes the inline editor for one free-text value. onSubmit receives
// the trimmed buffer after the prompt screen pops.
func (s *settingsScreen) prompt(a *App, title, seed string, onSubmit func(text string, a *App)) {
	a.Push(newSettingsPromptScreen(s.bundle, title, seed, onSubmit))
}

// dashEmpty mirrors the view's empty placeholder for absent defaults.
func dashEmpty(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(none)"
	}
	return v
}

// trimLines splits captured op output into display lines (trailing newline
// dropped, empty captures yield no lines).
func trimLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// settingsResultScreen is the read-only scroll window for multi-line op
// output (mode backups, mode status, doctor report, preset listing). Esc
// pops back to the settings list through the plain App precedence.
type settingsResultScreen struct {
	title string
	scr   *textScroller
}

// newSettingsResultScreen builds the scroll window over precomposed lines.
func newSettingsResultScreen(title string, lines []string) Screen {
	return &settingsResultScreen{title: title, scr: &textScroller{lines: lines}}
}

// Title returns the window title (the originating item's label).
func (s *settingsResultScreen) Title() string { return s.title }

// Mode is ModeNav: scrolling only, nothing to pick or edit.
func (s *settingsResultScreen) Mode() Mode { return ModeNav }

// Draw renders the visible window plus the read-only scroll hint.
func (s *settingsResultScreen) Draw(a *App) []string {
	page := pageSizeFor(a.Height(), 4)
	out := make([]string, 0, page+2)
	for _, l := range s.scr.window(page) {
		out = append(out, cellwidth.Truncate(l, a.Width()))
	}
	return append(out, "", settingsResultHint)
}

// HandleKey scrolls the window.
func (s *settingsResultScreen) HandleKey(k Key, a *App) {
	page := pageSizeFor(a.Height(), 4)
	switch k {
	case KeyUp:
		s.scr.scrollLines(-1, page)
	case KeyDown:
		s.scr.scrollLines(1, page)
	case KeyPgUp:
		s.scr.scroll(-1, page)
	case KeyPgDn:
		s.scr.scroll(1, page)
	case KeyHome:
		s.scr.home()
	case KeyEnd:
		s.scr.end(page)
	}
}

// settingsPromptScreen is the inline editor host for one free-text settings
// value (provider name, JSON path, backup dir, preset numbers). Ctrl+S or
// the OK action row submits the trimmed buffer and hands it to onSubmit;
// Esc pops back through the plain App precedence (a one-line prompt loses
// nothing a retry cannot retype).
type settingsPromptScreen struct {
	title    string
	bundle   *i18n.Bundle
	ed       *Editor
	actions  *stepList
	onAction bool
	onSubmit func(text string, a *App)
	app      *App
}

// newSettingsPromptScreen builds the prompt. seed pre-fills the buffer (the
// current value for edit-like prompts; empty for name/path prompts).
func newSettingsPromptScreen(bundle *i18n.Bundle, title, seed string, onSubmit func(text string, a *App)) Screen {
	s := &settingsPromptScreen{title: title, bundle: bundle, onSubmit: onSubmit}
	s.ed = NewEditor(seed)
	s.actions = newStepList(func(idx int) { s.submit() })
	s.actions.SetRows([]Row{{ID: "ok", Label: bundle.T("common.ok")}})
	return s
}

// Title returns the prompt text (what the user must type).
func (s *settingsPromptScreen) Title() string { return s.title }

// Mode is ModeEdit while the text area owns the keys and ModeNav on the OK
// row, so digits, y/n, and space stay text in the buffer.
func (s *settingsPromptScreen) Mode() Mode {
	if s.onAction {
		return ModeNav
	}
	return ModeEdit
}

// Draw renders the prompt, the single-line buffer, the OK row, and the hint.
func (s *settingsPromptScreen) Draw(a *App) []string {
	s.app = a
	s.ed.SetPageSize(pageSizeFor(a.Height(), 6))
	var lines []string
	rows, _ := s.ed.View()
	for _, r := range rows {
		lines = append(lines, cellwidth.Truncate(r, a.Width()))
	}
	lines = append(lines, "")
	marker := "  "
	if s.onAction {
		marker = "▶ "
	}
	lines = append(lines, marker+s.actions.Row(0).Label)
	return append(lines, "", missingOr(s.bundle, "settings.prompt.hint", settingsPromptHint))
}

// HandleKey dispatches one key between the text area and the OK row, then
// arms submission on Ctrl+S or the OK pick.
func (s *settingsPromptScreen) HandleKey(k Key, a *App) {
	s.app = a
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
		s.submit()
	default:
		s.ed.HandleKey(k)
	}
}

// submit pops the prompt and hands the trimmed buffer to the callback.
func (s *settingsPromptScreen) submit() {
	a := s.app
	if a == nil {
		return
	}
	text := strings.TrimSpace(s.ed.Text())
	a.Pop()
	if s.onSubmit != nil {
		s.onSubmit(text, a)
	}
}
