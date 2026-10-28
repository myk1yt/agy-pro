package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/providers"
	"agypro/internal/settings"
	"agypro/internal/swarm"
)

// English fallbacks for catalog keys this screen resolves through missingOr;
// every key below resolves in all twelve locales, so the consts never render
// unless a locale file loses a translation again. Each const names its key and
// stays as the fallback for a catalog lacking it. Every value avoids the
// em-dash (house rule: hyphens in user-facing text).
const (
	// catalog-resolved, fallback kept: settings.prompt.add_json
	settingsPromptAddJSON = "JSON file path"
	// catalog-resolved, fallback kept: settings.prompt.provider_old
	settingsPromptProviderOld = "Current provider name"
	// catalog-resolved, fallback kept: settings.prompt.provider_new
	settingsPromptProviderNew = "New provider name"
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
	// catalog-resolved, fallback kept: settings.msg.add_provider
	//
	// English chrome (decision 6B). The row routes into the providers screen
	// (survey-1 debt item 3): the message opens that screen and names the Add
	// row, with the terminal pair kept as the documented alternative for
	// scripts and non-console hosts. Resolves through
	// settings.msg.add_provider in all twelve locales, with the const below
	// as the fallback (the app.exit_confirm pattern).
	settingsMsgAddProvider = "Opens Hub > Providers. Rest the cursor on Add provider and press Enter. Terminal alternative: `agy pro setup --wizard` or `agy pro settings providers add-interactive`. Raw key echo is masked first-5 + ****."
	// catalog-resolved, fallback kept: settings.msg.cancelled
	settingsMsgCancelled = "Cancelled. Nothing was written."
	// catalog-resolved, fallback kept: settings.msg.backups_none
	settingsMsgBackupsNone = "(no backups)"
	// catalog-resolved, fallback kept: settings.msg.presets_none
	settingsMsgPresetsNone = "(no saved presets; p1/p2/p3 defaults always available)"
	// catalog-resolved, fallback kept: settings.msg.readonly
	settingsMsgReadonly = "read-only: {{note}}"
	// catalog-resolved, fallback kept: settings.legend.readonly
	//
	// P1-4: one legend line names the [ro] badge every read-only row note
	// carries (the shared spelling lives in settings.ReadOnlyMarker), so the
	// read-only state is visible before the press. Only the legacy panel
	// prints settings.ReadOnlyLegend verbatim (its footer); the `show`
	// snapshot composes its own "Flags: [ro] ... · [danger] ..." line from the
	// same sentence, and this screen resolves settings.legend.readonly.
	settingsLegendReadonly = "ro = read-only (display only; pressing the row changes nothing)"
	// catalog-resolved, fallback kept: settings.msg.provider_route
	//
	// The retired typed-name prompts for the provider test and delete rows
	// (settings.prompt.provider_name / settings.prompt.delete_name) went away
	// with the rows' new routing into the providers screen; the route message
	// names the verb to press there, and the {{verb}} slot carries the localized
	// verb label (providers.action.test / providers.action.delete).
	settingsMsgProviderRoute = "Opens Hub > Providers. Rest the cursor on the provider row, then press {{verb}}."
	// catalog-resolved, fallback kept: settings.section.position
	//
	// The section header counter. The title beside it is localized
	// (section.pN.title), so a counter hardcoded in Go ("section 2 of 8") left
	// the header half-translated on every non-English locale (code review M-5).
	// Slots: {{title}} the localized section title, {{n}} the 1-based position,
	// {{m}} the section count. The English value is byte-equal to this const, so
	// the header pins hold in en and a translation replaces the whole sentence.
	settingsSectionPosition = "{{title}} · section {{n}} of {{m}}"
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
	// catalog-resolved, fallback kept: settings.presets.selectability
	//
	// F04 decision (a), selectable variant: saved preset names ARE selectable.
	// The swarm-side grammar wrapper (internal/swarm/select.go resolves a name
	// to its stored Numbers), the builder seat prompt, the --seats flag path,
	// and the launcher model picker all accept the name, so the management
	// screen states that instead of the old refusal note.
	settingsPresetsSelectability = "selectable by name: the swarm builder, the launcher model picker, and --seats accept a saved preset name."
	// catalog-resolved, fallback kept: settings.presets.edit
	settingsPresetsEdit = "Edit preset"
	// catalog-resolved, fallback kept: settings.presets.delete
	settingsPresetsDelete = "Delete preset"
	// catalog-resolved, fallback kept: settings.presets.hint
	settingsPresetsHint = "Enter on a preset arms it - Enter on Edit/Delete acts on the armed preset - Esc returns."
	// catalog-resolved, fallback kept: settings.presets.armed
	settingsPresetsArmed = "(armed)"
	// catalog-resolved, fallback kept: settings.msg.preset_name_invalid
	settingsMsgPresetNameInvalid = "invalid preset name: use letters, digits, '-' or '_'; no spaces, commas, or semicolons; not a bare number and not p1/p2/p3."
	// catalog-resolved, fallback kept: settings.msg.preset_exists
	settingsMsgPresetExists = "a saved preset named {{name}} already exists. Open Saved Seat Presets to edit or delete it."
	// catalog-resolved, fallback kept: settings.msg.preset_numbers_empty
	settingsMsgPresetNumbersEmpty = "numbers must not be empty: enter a numbered selection like 1,3,19,23."
	// catalog-resolved, fallback kept: settings.msg.preset_numbers_invalid
	//
	// r7 F-01: the numbers field is §2.7 selection text, and the validator (the
	// selection expander's own parser) now vets it before the write. The reason
	// names the token POSITION and the rule; the entered value is never echoed.
	settingsMsgPresetNumbersInvalid = "numbers must be a numbered selection (seat numbers, ranges like 3-5, or p1/p2/p3): {{reason}}"
	// catalog-resolved, fallback kept: settings.msg.preset_none_armed
	settingsMsgPresetNoneArmed = "no preset selected: press Enter on a preset row first, then Edit or Delete."
	// catalog-resolved, fallback kept: settings.msg.preset_vanished
	//
	// The armed preset can disappear between the listing and the Edit/Delete
	// verb (a hand edit or another session rewrote swarm-state.json). That path
	// used to reuse the ADD-path message "a saved preset named {{name}} already
	// exists", which says the opposite of what happened. This message names the
	// vanished target instead, and it carries a catalog entry in all twelve
	// locales (landed with the locale owner, code review F03 follow-up), so the
	// const below is the fallback rather than the shipped text. The sentence
	// stays truthfully about a reload with nothing written, which is what the
	// code does.
	settingsMsgPresetVanished = "the saved preset {{name}} is no longer in the store (a hand edit or another session changed it); the list was reloaded and nothing was written."
	// settings.corrupt.* reuse (security review F-3): swarm-state.json shares
	// the fanout preset screens' corrupt-file vocabulary (presets.corrupt.title
	// / presets.corrupt.body / presets.corrupt.warning + corruptLatch) instead
	// of inventing a second wording for the same episode shape. Both files are
	// saved-preset stores, both are rewritten whole on the next save, and both
	// gates must read the same, so the strings are shared by key, not copied.
	// catalog-resolved, fallback kept: settings.approval.preset_delete
	settingsApprovalPresetDelete = "Delete saved seat preset {{name}}."
	// catalog-resolved, fallback kept: settings.exclude.builtin_absent
	//
	// The exclusion editor's two state sentences. The built-in default is a
	// PREDICATE (family "k3" or a k3 id token), and since the family:<name> entry
	// form landed the seeded draft is that predicate written as one entry, so
	// this sentence says saving the untouched draft KEEPS the rule instead of
	// warning that it replaces it.
	//
	// This sentence fires on a nil list, and nil has TWO spellings on disk: an
	// absent excludeModels key and an explicit "excludeModels": null (an
	// unrelated registry write marshals a nil slice that way). Both leave the
	// built-in default in force, so the sentence names both; an explicit empty
	// list [] is the third shape and means the user owns an empty list.
	settingsExcludeBuiltinAbsent = "the excludeModels key is absent or null, so the built-in default applies: the k3 family rule (family k3 or a k3 id token). An explicit empty list excludes nothing. The list below is that same rule written as the single entry family:k3, so saving it unchanged keeps the rule; add or remove entries to change it."
	// catalog-resolved, fallback kept: settings.exclude.owned
	settingsExcludeOwned = "this list is user-owned: it replaces the built-in k3 default entirely."
	// catalog-resolved, fallback kept: settings.exclude.entries_empty
	settingsExcludeEntriesEmpty = "(no entries: nothing is excluded while the list stays user-owned)"
	// catalog-resolved, fallback kept: settings.exclude.add
	settingsExcludeAdd = "Add entry"
	// catalog-resolved, fallback kept: settings.exclude.remove
	settingsExcludeRemove = "Remove entry"
	// catalog-resolved, fallback kept: settings.exclude.save
	settingsExcludeSave = "Save list"
	// catalog-resolved, fallback kept: settings.exclude.armed
	settingsExcludeArmed = "(armed)"
	// catalog-resolved, fallback kept: settings.exclude.hint
	settingsExcludeHint = "Enter on an entry arms it. Enter on Add, Remove, or Save acts on the list. Esc returns."
	// catalog-resolved, fallback kept: settings.prompt.exclude_entry
	settingsPromptExcludeEntry = "Exclusion entry (provider name, model id, provider/model, or family:<name>)"
	// catalog-resolved, fallback kept: settings.msg.exclude_invalid
	settingsMsgExcludeInvalid = "invalid exclusion entry"
	// catalog-resolved, fallback kept: settings.msg.exclude_none_armed
	settingsMsgExcludeNoneArmed = "no entry selected: press Enter on an entry row first, then Remove entry."
	// catalog-resolved, fallback kept: settings.msg.exclude_unchanged
	settingsMsgExcludeUnchanged = "providers.json defaults.excludeModels already holds this list, so nothing was written."
	// catalog-resolved, fallback kept: settings.approval.exclude_models
	settingsApprovalExcludeModels = "Write the model exclusion list to providers.json defaults.excludeModels; auto-seating and failover read it on the next run."
	// catalog-resolved, fallback kept: settings.approval.exclude_counts
	//
	// A dedicated counts line: the shared modal.approval.counts sentence counts
	// MODELS, and reusing it here would label an entry count as a model count.
	settingsApprovalExcludeCounts = "Entries before: {{before}}, after: {{after}}"
	// catalog-resolved, fallback kept: settings.approval.exclude_absent
	//
	// The approval modal's nil-list line. Like settings.exclude.builtin_absent it
	// names both nil spellings, because "excludeModels": null on disk means the
	// same thing as a missing key: the built-in default governs until this write
	// replaces the slot with the drafted list.
	settingsApprovalExcludeAbsent = "(the excludeModels key is absent or null today, so the built-in default applies and this write replaces it with the list below: the draft is the rule that applies from then on, and the entry family:k3 reproduces the built-in k3 rule)"
	// catalog-resolved, fallback kept: settings.exclude.stale_base
	settingsExcludeStaleBase = "providers.json defaults.excludeModels changed on disk since this editor opened. Nothing was written; the list was reloaded, so review it and save again."
	// catalog-resolved, fallback kept: settings.exclude.disk_check_failed
	settingsExcludeDiskCheckFailed = "providers.json could not be re-read, so the on-disk check did not run. Nothing was written; retry."
	// excludeModelsHubRoute is the one route sentence every surface that cannot
	// edit the list points at (P1-7=(a): the hub is the maintained surface). It
	// is Chrome English on purpose: the route is a command, and a translated
	// command would not run. The hub row's own note carries the localized
	// sentence; this const is what the providers form's read-only defaults row
	// prints beside excludeModels.
	excludeModelsHubRoute = "edit in the hub: agy pro setup"
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
	// corruptLatch is the swarm-state.json corrupt-file episode state of this
	// settings surface (security review F-3). The grid owns it and hands the
	// address to the Saved Seat Presets child screen, so ONE acknowledgment
	// covers the episode for both the append row and the management verbs: the
	// user answered about one file, not about one screen.
	corruptLatch
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
// keeping the cursor inside the (possibly shorter) section. The swarm store's
// corrupt-file warning is redirected into this surface's latch counter before
// the read (security review F-3): Core.View reads the saved presets, and the
// store's default os.Stderr sink is invisible under the full-screen frame.
func (s *settingsScreen) reload() {
	wireSwarmStoreSink(s.core, &s.corruptLatch)
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
// label in the row body, the value and source in the row note. Read-only
// rows carry the shared [ro] badge in the note (P1-4), so the read-only
// state is visible on the row before any press.
func (s *settingsScreen) rebuild() {
	items := s.items()
	rows := make([]Row, len(items))
	for i, it := range items {
		label, _ := localizeItem(s.bundle, it)
		note := it.Value + "  (" + it.Source + ")"
		if it.ReadOnly {
			note += "  [" + settings.ReadOnlyMarker + "]"
		}
		rows[i] = Row{
			ID:    it.ID,
			Label: label,
			Note:  note,
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

// sectionTitle renders the current section header. The title resolves through
// the catalog (section.pN.title) with the view's compiled title as the fallback.
// P1-6: the hub is the only numbered NAVIGATION surface (its rows render
// [1]..[8] from stepList); in-section row badges are the shared list's own
// numbering, not a second navigation scheme. So the section HEADER carries no
// bracketed index and states the position in words instead. A "(n/8)" counter
// read as a second numbered navigation space beside the hub's; "section n of m"
// cannot be typed and cannot be mistaken for a row.
//
// The counter sentence resolves through the catalog (settings.section.position)
// with the English const as the fallback (code review M-5): the title beside it
// is localized, so a counter hardcoded in Go printed English words on every
// non-English locale. The {{title}}/{{n}}/{{m}} slots fill through fillHubSlots,
// the same pair providersEmptyLine and the hub summary use.
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
	return fillHubSlots(missingOr(s.bundle, "settings.section.position", settingsSectionPosition),
		map[string]string{
			"title": title,
			"n":     strconv.Itoa(s.section + 1),
			"m":     strconv.Itoa(len(s.view.Sections)),
		})
}

// Draw renders the section header, the item window, the cursor row detail,
// and the two-handled legend. All cell math goes through cellwidth. The
// chrome reserve (14) covers the header, the detail block (up to 8 lines:
// label, value, default, source, ro, note, choices, danger), the list
// footer, and the read-only legend line added for P1-4; it was 12 before
// the ro detail line and the legend existed.
func (s *settingsScreen) Draw(a *App) []string {
	s.app = a
	w := a.Width()
	if s.loadErr != nil {
		return []string{cellwidth.Truncate(s.bundle.Tf("err.load_failed",
			map[string]string{"err": providers.MaskOperand(s.loadErr.Error())}), w)}
	}
	s.list.SetPageSize(pageSizeFor(a.Height(), 14))
	lines := []string{cellwidth.Truncate(s.sectionTitle(), w)}
	lines = append(lines, s.list.Render(w)...)
	if it, ok := s.cursorItem(); ok {
		lines = append(lines, "")
		lines = append(lines, s.detailLines(it, w)...)
	}
	legend := missingOr(s.bundle, "settings.legend.readonly", settingsLegendReadonly)
	return append(lines, "", s.list.FooterHint(), "", cellwidth.Truncate(legend, w))
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
	if it.ReadOnly {
		lines = append(lines, "    ro      : read-only (display only)")
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
		// Survey-1 debt item 3: the row used to tell a user already inside
		// `agy pro setup` to run `agy pro setup`. The Add verb lives on the
		// providers screen, so the row opens it (same routing family as
		// providers.test/delete) and the message names the Add row.
		pushProvidersScreen(s.core, a)
		a.SetMessage(missingOr(s.bundle, "settings.msg.add_provider", settingsMsgAddProvider))
	case it.ID == "providers.add_json":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.add_json", settingsPromptAddJSON), "", func(text string, a *App) {
			s.addJSON(label, text, a)
		})
	case it.ID == "providers.test":
		// The typed-name prompt is retired: the action exists as the
		// row-picked Test verb on the providers screen, so the row routes
		// there (survey-1 debt item 18).
		s.routeToProviders(a, "providers.action.test")
	case it.ID == "providers.rename":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.provider_old", settingsPromptProviderOld), "", func(old string, a *App) {
			s.prompt(a, missingOr(s.bundle, "settings.prompt.provider_new", settingsPromptProviderNew), "", func(neu string, a *App) {
				s.renameProvider(label, old, neu, a)
			})
		})
	case it.ID == "providers.delete":
		// Same routing as providers.test: the M4-gated Delete verb on the
		// providers screen replaces the typed-name prompt.
		s.routeToProviders(a, "providers.action.delete")
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
		s.managePresets(a, label)
	case it.ID == "swarm.exclude_models":
		// D2=(b)/P1-7=(a): the model exclusion list is edited here and only
		// here. The arm sits above the generic ReadOnly arm because the row is
		// editable in the hub (ReadOnly false) while the legacy panel and
		// `show` render it read-only; the write itself is the M4 gate inside
		// the editor (approveExcludeModels), never this arm.
		s.openExcludeModels(a, label)
	case it.ID == "swarm.save_preset":
		s.prompt(a, missingOr(s.bundle, "settings.prompt.preset_name", settingsPromptPresetName), "", func(name string, a *App) {
			s.savePreset(label, name, a)
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
// approveProviderAdd, approveFixKeys, approveRollbackKeys; the provider
// delete approval lives on the providers screen, which the providers.test
// and providers.delete rows here route into).
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

// routeToProviders replaces the retired typed-name prompts behind the
// providers.test and providers.delete rows: both actions exist as row-picked
// verbs on the providers screen (Test and the M4-gated Delete), so the row
// opens that screen instead of asking for a typed name (survey-1 debt item
// 18). The message names the verb to press; the target is chosen by resting
// the cursor on the provider row. providers.rename keeps its typed old/new
// prompts: no rename verb exists on the providers screen (the provider form
// treats the name as fixed identity, so Edit can never rename), and the
// legacy panel drives the same prompts.
func (s *settingsScreen) routeToProviders(a *App, verbKey string) {
	pushProvidersScreen(s.core, a)
	a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.provider_route", settingsMsgProviderRoute),
		map[string]string{"verb": s.bundle.T(verbKey)}))
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
	masked := providers.MaskSlot(spec.Name)
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
			"old": providers.MaskSlot(p.Name),
			"new": providers.MaskSlot(neu),
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

// approvalFrameChrome is the frame height a live M4 gate shares with the rest
// of the frame: the screen title above the gate, the gate title, the gate
// legend, the shared footer, and the blank line joinBlocks (tui.go) inserts
// between each pair of adjacent sections. Four fixed rows plus two separators.
const approvalFrameChrome = 6

// approvalMessageRows is the extra height a live message line costs: the
// message row itself plus the extra separator, because the message becomes a
// fourth section between the gate and the shared footer.
const approvalMessageRows = 2

// approvalEntriesHidden is the bounded-summary tail line (see
// capApprovalEntries): the exact number of per-entry diff lines the gate could
// not show, so a partial diff says so instead of ending mid-list. Chrome i18n
// is decision 6B (documented English), like the gate legends, so this line
// carries no catalog key.
const approvalEntriesHidden = "  ... %d more entries NOT shown (the diff above is partial)"

// approvalBodyBudget returns how many modal body lines the live M4 gate may
// carry and still keep the gate title, the visible diff lines, the gate
// legend, and the shared footer inside a.Height() rows. The composer's cut
// ladder (ComposeFrame in tui.go) gives up the screen body, the message, and
// the screen title before it reaches the gate body, and its last stage trims
// that body from the bottom without a marker: an unbounded summary is
// therefore cut silently and the operator cannot tell the diff is partial.
// The rows the gate can never share are the chrome above plus a live message
// line, so the bound follows the frame geometry at composition time instead of
// a fixed entry count. The caller composes the gate in the same step that
// paints it, so the geometry read here is the painted one.
func approvalBodyBudget(a *App) int {
	chrome := approvalFrameChrome
	if a.Message() != "" {
		chrome += approvalMessageRows
	}
	return a.Height() - chrome
}

// capApprovalEntries fits an approval gate's per-entry diff lines into the
// modal body budget. Entries that fit are returned exactly as composed, so a
// small gate renders byte-identically to the pre-bound gate and carries no
// tail line. Entries that do not fit are cut to the leading lines the budget
// holds, followed by ONE tail line naming exactly how many entries are not
// shown: the operator reads an explicitly partial diff with an exact count
// instead of a silently truncated one that hides the rest of the write scope.
// The cut only fires when at least two entries are hidden, so the tail line's
// plural always reads correctly. fixed is the line count of the summary prefix
// the caller composed before the entries.
func capApprovalEntries(entries []string, fixed, budget int) []string {
	if fixed+len(entries) <= budget {
		return entries
	}
	visible := budget - fixed - 1 // one modal body line is the tail
	if visible < 0 {
		visible = 0
	}
	out := make([]string, 0, visible+1)
	out = append(out, entries[:visible]...)
	out = append(out, fmt.Sprintf(approvalEntriesHidden, len(entries)-visible))
	return out
}

// approveFixKeys opens the M4 approval modal for the doctor fix-keys write.
// The summary carries the action sentence, the registry model count
// before/after (key-name repair never touches models, so the numbers are
// equal and any future drift is visible), the honest backup sentence, and
// one diff-context line per resolved target env name (provider masked). The
// per-entry diff is bounded to the frame budget (capApprovalEntries), so at
// 80x24 a 20+ target repair still shows the gate legend and an exact
// "N more entries NOT shown" tail instead of losing the legend off-frame. The
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
	entries := make([]string, 0, len(names))
	for _, env := range names {
		providerName := ""
		for i := range reg.Providers {
			if strings.TrimSpace(reg.Providers[i].APIKeyEnv) == env {
				providerName = reg.Providers[i].Name
				break
			}
		}
		entries = append(entries, fillHubSlots(missingOr(s.bundle, "settings.approval.fix_target", settingsApprovalFixTarget), map[string]string{
			"env":      providers.MaskOperand(env),
			"provider": providers.MaskSlot(providerName),
		}))
	}
	summary = append(summary, capApprovalEntries(entries, len(summary), approvalBodyBudget(a))...)
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
// masked). The per-entry diff is bounded to the frame budget
// (capApprovalEntries), so a long backup listing still shows the gate legend
// and an exact "N more entries NOT shown" tail at 80x24. y is the only path to
// Core.DoctorRollbackKeys; n/Esc writes nothing.
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
	entries := make([]string, 0, len(snap.Entries))
	for _, e := range snap.Entries {
		if e.Existed {
			entries = append(entries, fillHubSlots(missingOr(s.bundle, "settings.approval.rollback_restore", settingsApprovalRollbackRestore), map[string]string{
				"name": providers.MaskOperand(e.Name),
			}))
		} else {
			entries = append(entries, fillHubSlots(missingOr(s.bundle, "settings.approval.rollback_delete", settingsApprovalRollbackDelete), map[string]string{
				"name": providers.MaskOperand(e.Name),
			}))
		}
	}
	summary = append(summary, capApprovalEntries(entries, len(summary), approvalBodyBudget(a))...)
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
// slot-masked provider name, the userinfo-stripped base URL, and the model
// count.
func (s *settingsScreen) providerLine(name, baseURL string, models int) string {
	return fmt.Sprintf("provider %s (%s, %s)", providers.MaskSlot(name), maskBaseURL(baseURL), s.bundle.Tn("menu.count.models", models))
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

// wireSwarmStoreSink redirects the on-disk swarm store's one-line corrupt-file
// warning into this surface's latch counter (the fanout corruptPresetSink
// precedent, screen_presets.go). The store defaults WarnOut to os.Stderr, which
// the full-screen frame cannot show: under the console painter the line lands
// on the alternate screen or garbles a frame row, so the warning must be
// counted here and rendered on the message line instead (security review F-3).
// A non-file store (the test fakes, an embedder) has no sink to redirect and is
// left alone.
func wireSwarmStoreSink(core *settings.Core, l *corruptLatch) {
	if fs := core.SwarmStateStore(); fs != nil {
		fs.WarnOut = corruptPresetSink{count: &l.corruptEvents}
	}
}

// swarmStatePath resolves the display path of swarm-state.json (AGY_PRO_HOME
// aware), matching the store's own path resolution, so the corrupt gate can
// name the file to back up. A non-file store falls back to the default path.
func swarmStatePath(core *settings.Core) string {
	if fs := core.SwarmStateStore(); fs != nil && fs.Path != "" {
		return fs.Path
	}
	if p, err := swarm.DefaultStatePath(); err == nil && p != "" {
		return p
	}
	return "swarm-state.json"
}

// loadSavedPresets reads the saved seat presets through the corrupt-episode
// latch (the fanout presetsScreen.loadPresets precedent). The sink is
// (re)installed on the store before the read, so a corrupt load moves the
// counter, and the counter is snapshotted around the read so the latch can tell
// a new corrupt event from a repeated observation of the same one. Every read
// on the write path goes through here: a file that turned corrupt after the
// listing was painted must gate the very next save.
func loadSavedPresets(core *settings.Core, l *corruptLatch) ([]swarm.SavedPreset, error) {
	wireSwarmStoreSink(core, l)
	before := l.corruptEvents
	presets, err := core.SwarmPresets()
	loadErr := ""
	if err != nil {
		loadErr = providers.MaskOperand(err.Error())
	}
	l.reconcile(l.corruptEvents > before, loadErr)
	return presets, err
}

// gateCorruptSwarmSave refuses the first swarm-state.json write after a corrupt
// load until the user acknowledges it through an explicit confirm naming the
// on-disk path (back it up before saving), the fanout presetsScreen
// gateCorruptSave precedent. Afterwards the write proceeds directly; one
// acknowledgment covers exactly one corrupt episode, and a later healthy load
// closes it, so a new corrupt event re-arms the gate.
//
// Reachability, stated honestly: with the swarm file store a corrupt load
// returns an EMPTY list, so the management screen's vanished-target refusal
// fires before this gate on the edit/delete verbs (the corrupt file has no rows
// to address). The append row is the path that exercises the gate today, and
// the gate sits on every swarm write THIS screen makes, so a store that ever
// reports rows alongside a corruption event cannot slip an unacknowledged
// overwrite past the same latch.
//
// Coverage, stated equally honestly (code review M-2): the legacy line-mode
// panel (`agy pro settings`, internal/settings/tui.go swarm.save_preset) writes
// the same swarm-state.json and used to do it with no gate and no on-screen
// warning. It now carries its own ack on its own prompt surface: a fresh probe
// of the file it is about to replace (count-delta on the store's warning sink,
// which still mirrors to stderr for line mode) arms the panel's y/n gate with
// the identical presets.corrupt.body sentence. So the rule holds for BOTH
// shipped writers of this file: no surface overwrites an unreadable
// swarm-state.json without an explicit acknowledgment.
func gateCorruptSwarmSave(a *App, b *i18n.Bundle, core *settings.Core, l *corruptLatch, proceed func()) {
	if a == nil || !l.corruptWarn || l.corruptAcked {
		proceed()
		return
	}
	path := providers.MaskOperand(swarmStatePath(core))
	a.ShowModal(NewConfirm(missingOr(b, "presets.corrupt.title", presetsCorruptTitle),
		[]string{fillHubSlots(missingOr(b, "presets.corrupt.body", presetsCorruptBody),
			map[string]string{"path": path})},
		func(approved bool) {
			if !approved {
				a.SetMessage(missingOr(b, "settings.msg.cancelled", settingsMsgCancelled))
				return
			}
			l.corruptAcked = true
			proceed()
		}))
}

// --- P5-1 U6: the swarm preset write gates (W21 append, W22 edit) -----------
//
// Both writers used to reach Core.SwarmSavePresets behind the corrupt-file
// acknowledgment alone, so a HEALTHY swarm-state.json was rewritten with no
// consent over what the write applies. They now open the M4 approval modal with
// the exact append/edit diff, the same door the registry writers use, and
// re-verify the presented entry set against a fresh read before the write runs.
//
// The store is swarm-state.json, not providers.json, so the catalog's
// modal.approval.body sentence ("providers.json will be written ...") does not
// apply. The store sentences below are documented English chrome (decision 6B,
// the approvalEntriesHidden precedent): they are new keys, and a new key costs
// twelve catalog edits (research-p51-writers.md section 6.2). The +/- markers
// and the bounded tail reuse their catalog/const spellings, and the line SHAPES
// mirror the legacy panel's append diff (internal/settings/paneldiff.go,
// panelPreset* consts), so one store keeps one diff shape across both of its
// shipped writers.
const (
	// swarmAppendSentence names the append, the store (base name: the full path
	// gets its own line so a deep AGY_PRO_HOME cannot clamp this sentence away),
	// and its no-collateral promise.
	swarmAppendSentence = "Appends preset %s to %s. No existing preset changes."
	// swarmEditSentence names the rewrite and its no-collateral promise.
	swarmEditSentence = "Rewrites preset %s in %s. No other preset changes."
	// swarmEditFieldLine is the changed-field line of an edit: field name, old
	// value, new value (both masked).
	swarmEditFieldLine = "  %s: %s -> %s"
	// swarmCountsLine is the preset count before/after.
	swarmCountsLine = "  presets before: %d, after: %d"
	// swarmExistingLine introduces the entries the write keeps byte-identical.
	swarmExistingLine = "  existing presets (kept byte-identical):"
	// swarmExistingFmt renders one preserved entry by NAME only: another
	// preset's numbers are that preset's business and this write changes none.
	swarmExistingFmt = "  preset %s"
	// swarmEntryFmt is the inner text of one +/- diff entry.
	swarmEntryFmt = "preset %s = %s"
	// swarmAtomicLine names the file (base name) and the write mode the diff
	// applies to.
	swarmAtomicLine = "%s will be written (0600, atomic) with this exact diff:"
	// swarmDriftRefusal is the write-time stale-base refusal: it names the store
	// and the first difference, so a refusal says WHAT moved. The base name is
	// used, not the full path: the drift detail has to survive the width clamp.
	swarmDriftRefusal = "refused: %s changed on disk between the diff and the write (%s), so the presented diff is stale; nothing was written."
	// swarmNumbersField is the field name the edit diff line prints.
	swarmNumbersField = "numbers"
)

// presetNameOperand renders a preset name through the registry-slot display
// authority (providers.MaskSlot), the discipline the registry approvals apply to
// their identity slots: a credential-shaped name typed into the preset-name
// prompt renders first-5 + **** in the diff and in the drift sentence, while a
// benign name stays readable. MaskSlot's one extra branch over MaskOperand (the
// registry-LOCATION rewrite, "C:\x" -> "x") can only fire on a value no write
// this gate guards can store: validPresetName refuses ':' and '/'. A
// hand-edited swarm-state.json may still hold such an old-shape name, in which
// case the gate renders its marker-stripped core while the list row
// (MaskOperand) keeps the prefix: a display normalization of one name, never a
// hidden change to what is written, since the stored bytes are the name itself.
func presetNameOperand(name string) string { return providers.MaskSlot(name) }

// presetNumbersOperand renders a numbers operand through the seat-token authority
// (per token). The field is §2.7 selection text by grammar - r7 F-01 enforces that
// at this screen's save prompt and at the store load - but a legacy or
// force-written record can still carry a credential, and MaskOperand (which the
// rows used to share) is the identity on the underscore-head class. Benign numbers
// keep their exact bytes, so the gate body and the listing still agree.
func presetNumbersOperand(numbers string) string { return fanout.MaskSeatTokens(numbers) }

// swarmPresetBase is the signed state of one swarm preset write: the ordered
// preset NAMES the diff listed plus, for an edit, the numbers of the ONE entry
// whose field the diff shows changing.
//
// The other presets' numbers are deliberately NOT signed: both writers rebuild
// the file from a FRESH read (swarmPresetReverify), so an external edit to
// another preset's numbers is carried over instead of reverted, and the name set
// is the whole claim the diff makes about the rest of the file (the panel's
// panelPresetDrift signs the same shape).
type swarmPresetBase struct {
	names   []string
	target  string // edit only: the entry whose numbers the diff shows changing
	numbers string // edit only: that entry's numbers at presentation time
}

// swarmPresetBaseFrom signs the list a gate presents.
func swarmPresetBaseFrom(presets []swarm.SavedPreset) swarmPresetBase {
	b := swarmPresetBase{names: make([]string, 0, len(presets))}
	for _, p := range presets {
		b.names = append(b.names, p.Name)
	}
	return b
}

// swarmEditBaseFrom signs the same list PLUS the edited entry's current numbers,
// so a concurrent edit of the very field this write changes is caught even when
// the name set still matches.
func swarmEditBaseFrom(presets []swarm.SavedPreset, target swarm.SavedPreset) swarmPresetBase {
	b := swarmPresetBaseFrom(presets)
	b.target, b.numbers = target.Name, target.Numbers
	return b
}

// swarmPresetDrift names the first difference between the presented base and a
// fresh read, or "" when they agree. Operands render masked.
func swarmPresetDrift(presented swarmPresetBase, live []swarm.SavedPreset) string {
	if len(presented.names) != len(live) {
		return fmt.Sprintf("the preset count moved from %d to %d", len(presented.names), len(live))
	}
	for i := range presented.names {
		if presented.names[i] != live[i].Name {
			return fmt.Sprintf("preset %d moved from %s to %s", i+1,
				presetNameOperand(presented.names[i]), presetNameOperand(live[i].Name))
		}
	}
	if presented.target == "" {
		return ""
	}
	idx := indexPresetFold(live, presented.target)
	if idx < 0 {
		return fmt.Sprintf("preset %s vanished", presetNameOperand(presented.target))
	}
	if live[idx].Numbers != presented.numbers {
		return fmt.Sprintf("preset %s %s moved from %s to %s", presetNameOperand(presented.target), swarmNumbersField,
			presetNumbersOperand(presented.numbers), presetNumbersOperand(live[idx].Numbers))
	}
	return ""
}

// swarmPresetReverify is the write-time stale-base check both swarm writers run
// after consent: it re-reads swarm-state.json through the corrupt latch and
// reports the fresh list, the first drift ("" when the presented diff still
// describes the file), or the read error. The caller writes the FRESH list, so
// the file state the gate signed is the file state the write extends.
func swarmPresetReverify(core *settings.Core, latch *corruptLatch, base swarmPresetBase) ([]swarm.SavedPreset, string, error) {
	fresh, err := loadSavedPresets(core, latch)
	if err != nil {
		return nil, "", err
	}
	return fresh, swarmPresetDrift(base, fresh), nil
}

// replaceSwarmPreset returns a copy of fresh with the named entry's numbers
// rewritten and every other entry untouched, or ok=false when the entry is gone
// (the write-time half of the vanished-target refusal).
func replaceSwarmPreset(fresh []swarm.SavedPreset, name, numbers string) ([]swarm.SavedPreset, bool) {
	idx := indexPresetFold(fresh, name)
	if idx < 0 {
		return nil, false
	}
	out := append([]swarm.SavedPreset(nil), fresh...)
	out[idx].Numbers = numbers
	return out, true
}

// approveSwarmAppend opens the M4 approval modal for one saved-preset append
// (W21): the append sentence naming the store, the preset counts, the added diff
// line (name and numbers masked), the atomic sentence, and the existing entries
// the write keeps byte-identical. The entry list is bounded to the frame budget
// through capApprovalEntries, so a long preset list still shows the gate legend
// and an exact "N more entries NOT shown" tail. y re-verifies the presented name
// list against a fresh read and then writes the fresh list plus the new entry;
// n/Esc writes nothing, and a moved list is refused by name.
func (s *settingsScreen) approveSwarmAppend(a *App, label string, presets []swarm.SavedPreset, add swarm.SavedPreset) {
	storePath := swarmStatePath(s.core)
	storeBase := providers.MaskOperand(filepath.Base(storePath))
	store := providers.MaskOperand(storePath)
	base := swarmPresetBaseFrom(presets)
	seeds := providers.FlagSeedOperands(add.Name, add.Numbers)
	summary := []string{
		fmt.Sprintf(swarmAppendSentence, presetNameOperand(add.Name), storeBase),
		fmt.Sprintf(swarmCountsLine, len(presets), len(presets)+1),
		s.bundle.Tf("presets.explain.file", map[string]string{"path": store}),
		fmt.Sprintf(swarmAtomicLine, storeBase),
		s.bundle.Tf("modal.approval.diff_added", map[string]string{
			"line": fmt.Sprintf(swarmEntryFmt, presetNameOperand(add.Name), presetNumbersOperand(add.Numbers)),
		}),
	}
	if len(presets) > 0 {
		summary = append(summary, swarmExistingLine)
		entries := make([]string, 0, len(presets))
		for _, p := range presets {
			entries = append(entries, fmt.Sprintf(swarmExistingFmt, presetNameOperand(p.Name)))
		}
		summary = append(summary, capApprovalEntries(entries, len(summary), approvalBodyBudget(a))...)
	}
	a.ShowModal(NewApproval(label, summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		fresh, drift, err := swarmPresetReverify(s.core, &s.corruptLatch, base)
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), seeds...))
			return
		}
		if drift != "" {
			s.reload()
			a.SetMessage(providers.MaskSentence(fmt.Sprintf(swarmDriftRefusal, storeBase, drift), seeds...))
			return
		}
		if err := s.core.SwarmSavePresets(append(fresh, add)); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), seeds...))
			return
		}
		// The file was just rewritten healthy, so the open corrupt episode is
		// over (the fanout writePreset precedent): the notice must not outlive
		// the save that replaced the corrupt file.
		s.corruptLatch.reconcile(false, "")
		s.reload()
		a.SetMessage("[SUCCESS] " + label)
	}))
}

// savePreset appends one numbered seat preset through Core.SwarmSavePresets
// after the D17 validation the old append-only path lacked: the name must be
// a usable preset name and must not collide with a saved preset (edit/delete
// live on the Saved Seat Presets management screen), and the numbers must
// not be empty. Every refusal names the reason on the message line; nothing
// is written on a refusal. The write itself is the M4 gate in
// approveSwarmAppend (F3(a): a healthy path used to rewrite the file with no
// consent over its content).
func (s *settingsScreen) savePreset(label, name string, a *App) {
	if !validPresetName(name) {
		a.SetMessage(missingOr(s.bundle, "settings.msg.preset_name_invalid", settingsMsgPresetNameInvalid))
		return
	}
	presets, err := s.core.SwarmPresets()
	if err != nil {
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(name)...))
		return
	}
	if idx := indexPresetFold(presets, name); idx >= 0 {
		a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_exists", settingsMsgPresetExists),
			map[string]string{"name": presetNameOperand(name)}))
		return
	}
	s.prompt(a, missingOr(s.bundle, "settings.prompt.preset_numbers", settingsPromptPresetNumbers), "", func(nums string, a *App) {
		if strings.TrimSpace(nums) == "" {
			a.SetMessage(missingOr(s.bundle, "settings.msg.preset_numbers_empty", settingsMsgPresetNumbersEmpty))
			return
		}
		// r7 F-01: the input accepted ANY non-empty string, so a misdirected paste
		// landed in swarm-state.json and echoed raw on four surfaces. The grammar
		// (§2.7 selection text) is now enforced HERE and at the store load, through
		// the ONE validator the selection expander's own parser backs.
		if err := swarm.ValidateSavedPresetNumbers(nums); err != nil {
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_numbers_invalid", settingsMsgPresetNumbersInvalid),
				map[string]string{"reason": err.Error()}))
			return
		}
		// Fresh load through the latch: the read above is only the early
		// duplicate refusal. The write path must observe the CURRENT file, so
		// a corruption that appeared while the prompt was open gates this
		// write instead of being silently overwritten, and a preset another
		// session saved meanwhile is not reverted by this append (security
		// review F-3).
		presets, err := loadSavedPresets(s.core, &s.corruptLatch)
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(name, nums)...))
			return
		}
		if idx := indexPresetFold(presets, name); idx >= 0 {
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_exists", settingsMsgPresetExists),
				map[string]string{"name": presetNameOperand(name)}))
			return
		}
		// The corrupt acknowledgment stays the OUTERMOST gate (it is a
		// precondition about the file's readability), and the exact-diff consent
		// it releases is the decision that runs the write. Both are per-write:
		// one acknowledgment covers one episode, one approval covers one append.
		gateCorruptSwarmSave(a, s.bundle, s.core, &s.corruptLatch, func() {
			s.approveSwarmAppend(a, label, presets, swarm.SavedPreset{Name: name, Numbers: nums})
		})
	})
}

// containsControlRunes reports whether s carries a control (Cc) or format (Cf)
// rune. The preset-name input surface needs the wider class: a name is stored in
// swarm-state.json and printed on several surfaces, and the final painter already
// drops Cc plus Cf, so a name carrying either could be saved, listed, and never
// typed back. (The registry boundary rules on Cc only; this is the input gate,
// not a boundary rule.)
func containsControlRunes(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0
}

// validPresetName reports whether name can join the saved-preset namespace AND
// can later be selected by that name.
//
// The namespace rules (unchanged): a name containing a selection separator
// (space, comma, semicolon) could never be one token in the §2.7 grammar, and a
// bare number or p1/p2/p3 already means something in that grammar, so both are
// refused at the input surface instead of planting a name the listing shows next
// to p1/p2/p3 but can never be typed.
//
// The selectability rules (P4-5 fold-in, the warmup unit's residual): the
// selection grammar resolves a saved name ONLY when the token is not already
// selection syntax (swarm.savedPresetNameShape -> isSelectionSyntaxToken). A
// token that STARTS WITH A DIGIT is a number or a range, so a saved name such as
// "3fast" could be stored and listed yet never resolve; the same holds for a
// name carrying ':' or '/', which the grammar reads as a seat spec
// ("native:id", "prov/model"). Those spellings are refused here now, so every
// name this path accepts is a name the selection path can reach. Control runes
// are refused as well: a name is stored in swarm-state.json and printed on
// several surfaces, none of which should carry a terminal control byte.
//
// Pre-existing saved presets are NOT re-validated or deleted anywhere: this
// function only gates NEW input, so a store written before this rule keeps
// listing its old-shape names (the management screen shows them and the delete
// verb stays available).
func validPresetName(name string) bool {
	if name == "" {
		return false
	}
	if strings.ContainsAny(name, " \t\r\n,;") {
		return false
	}
	if containsControlRunes(name) || strings.ContainsAny(name, ":/") {
		return false
	}
	switch strings.ToLower(name) {
	case "p1", "p2", "p3":
		return false
	}
	if name[0] >= '0' && name[0] <= '9' {
		// Numbers, ranges, and any other digit-leading token are selection
		// syntax before they are ever a name, so such a name is unselectable.
		return false
	}
	if _, err := strconv.Atoi(name); err == nil {
		return false
	}
	return true
}

// indexPresetFold returns the first preset index whose name equals name
// case-insensitively (the Core verbs' case-insensitive precedent), or -1.
func indexPresetFold(presets []swarm.SavedPreset, name string) int {
	for i := range presets {
		if strings.EqualFold(presets[i].Name, name) {
			return i
		}
	}
	return -1
}

// managePresets pushes the saved seat preset management screen (defect D17):
// the old read-only listing had no edit or delete. The screen lists every
// saved preset, arms one with Enter, and edits or deletes the armed preset
// through the same Core.SwarmSavePresets verb the append path uses. onChange
// reloads the settings rows underneath so the listing row value never goes
// stale after a mutation. The parent's corrupt latch is handed down, so one
// acknowledgment covers the whole settings surface (security review F-3).
func (s *settingsScreen) managePresets(a *App, label string) {
	a.Push(newSettingsPresetsScreen(s.bundle, s.core, label, s.reload, &s.corruptLatch))
}

// settingsPresetsScreen is the saved seat preset management surface behind
// the swarm.presets row: preset rows plus Edit/Delete action rows (the
// preset editor's armed-row idiom). Esc pops back through the plain App
// precedence. latch is the parent settings screen's corrupt-file episode
// state, shared by pointer so the notice and the gate cannot disagree between
// the two screens.
type settingsPresetsScreen struct {
	title    string
	bundle   *i18n.Bundle
	core     *settings.Core
	onChange func()
	list     *stepList
	rows     []Row
	presets  []swarm.SavedPreset
	armed    int // index into presets, -1 = none
	app      *App
	latch    *corruptLatch
}

func newSettingsPresetsScreen(b *i18n.Bundle, core *settings.Core, title string, onChange func(), latch *corruptLatch) *settingsPresetsScreen {
	if latch == nil {
		latch = &corruptLatch{}
	}
	s := &settingsPresetsScreen{bundle: b, core: core, title: title, onChange: onChange, armed: -1, latch: latch}
	s.list = newStepList(s.pick)
	s.reload()
	return s
}

// Title returns the originating row's label.
func (s *settingsPresetsScreen) Title() string { return s.title }

// Mode is ModeNav: the stepList drives arming and the action rows.
func (s *settingsPresetsScreen) Mode() Mode { return ModeNav }

// reload re-reads the presets through the Core seam after a mutation (or at
// open) and clamps the armed index inside the (possibly shorter) list. The read
// goes through loadSavedPresets, so the store's corrupt-file warning is counted
// into the shared latch instead of vanishing into the os.Stderr sink
// (security review F-3).
func (s *settingsPresetsScreen) reload() {
	presets, err := loadSavedPresets(s.core, s.latch)
	if err != nil {
		presets = nil
	}
	s.presets = presets
	if s.armed >= len(presets) {
		s.armed = len(presets) - 1
	}
	s.rebuild()
}

// settingsSavedPresetRowPrefix namespaces a saved preset row ID. Row IDs are
// never rendered; they exist so rebuild can restore the cursor by IDENTITY
// (the modelsScreen/presetsScreen convention), which is what keeps the armed
// row under the cursor when arming rebuilds the list (code review C6).
const settingsSavedPresetRowPrefix = "saved-preset:"

// rebuild composes the preset rows and the two action rows. Names and
// numbers are user-set operands of the same class the old presetLines
// listing masked (survey-5 D2 display sites), so a credential-shaped value
// renders first-5+**** here too; benign pairs keep their exact bytes.
//
// The cursor is restored by ROW ID: arming a preset row rebuilds the list, and
// SetRows resets the cursor to the top, so without the restore the row the user
// just armed scrolled out from under them (code review C6). Preset rows carry
// the preset's name in the ID, so a row keeps its identity across an insert or
// a delete elsewhere in the list; a vanished row falls back to the old index,
// which SetCursor clamps into the new range.
func (s *settingsPresetsScreen) rebuild() {
	oldCursor := s.list.Cursor()
	oldID := ""
	if oldCursor >= 0 && oldCursor < len(s.rows) {
		oldID = s.rows[oldCursor].ID
	}
	rows := make([]Row, 0, len(s.presets)+2)
	for i, p := range s.presets {
		label := fmt.Sprintf("%s = %s", presetNameOperand(p.Name), presetNumbersOperand(p.Numbers))
		if i == s.armed {
			label += " " + missingOr(s.bundle, "settings.presets.armed", settingsPresetsArmed)
		}
		rows = append(rows, Row{ID: settingsSavedPresetRowPrefix + p.Name, Label: label})
	}
	if len(s.presets) > 0 {
		rows = append(rows,
			Row{ID: "edit", Label: missingOr(s.bundle, "settings.presets.edit", settingsPresetsEdit)},
			Row{ID: "delete", Label: missingOr(s.bundle, "settings.presets.delete", settingsPresetsDelete)},
		)
	}
	s.rows = rows
	s.list.SetRows(rows)
	if len(rows) == 0 {
		return
	}
	cursor := oldCursor
	for i, r := range rows {
		if r.ID == oldID {
			cursor = i
			break
		}
	}
	s.list.SetCursor(cursor)
}

// Draw renders the corrupt-file notice (when the store fell back to empty), the
// selectability refusal line (the D2 honesty note), the preset window (or the
// honest empty state), and the action hint. The notice is also pushed onto the
// app message line once per corrupt episode, so the warning is visible the way
// the fanout preset screens show it (security review F-3).
func (s *settingsPresetsScreen) Draw(a *App) []string {
	s.app = a
	w := a.Width()
	lines := []string{cellwidth.Truncate(missingOr(s.bundle, "settings.presets.selectability", settingsPresetsSelectability), w)}
	if s.latch.corruptWarn {
		warn := corruptPresetWarning(s.bundle, swarmStatePath(s.core))
		lines = append(lines, cellwidth.Truncate(warn, w))
		if !s.latch.corruptMsgSent && a.Message() == "" {
			s.latch.corruptMsgSent = true
			a.SetMessage(warn)
		}
	}
	if len(s.presets) == 0 {
		lines = append(lines, "", missingOr(s.bundle, "settings.msg.presets_none", settingsMsgPresetsNone))
	} else {
		s.list.SetPageSize(pageSizeFor(a.Height(), 6))
		lines = append(lines, s.list.Render(w)...)
	}
	return append(lines, "", missingOr(s.bundle, "settings.presets.hint", settingsPresetsHint))
}

// HandleKey routes one key to the stepList.
func (s *settingsPresetsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// pick arms a preset row or runs the Edit/Delete action rows.
func (s *settingsPresetsScreen) pick(idx int) {
	a := s.app
	if a == nil {
		return
	}
	n := len(s.presets)
	switch {
	case idx >= 0 && idx < n:
		s.armed = idx
		s.rebuild()
	case idx == n:
		s.editArmed(a)
	case idx == n+1:
		s.deleteArmed(a)
	}
}

// editArmed rewrites the armed preset's numbers (the edit half of D17). The
// prompt seeds the current numbers so an edit is a correction, not a
// retype; an empty answer is refused by name and writes nothing. The write
// sits behind the shared corrupt-file gate (security review F-3): the first
// save after a corrupt load must not replace the unreadable file unannounced,
// and behind the M4 exact-diff gate (P5-1 U6, approveSwarmEdit): the healthy
// path used to rewrite the file while the operator saw only a prompt.
func (s *settingsPresetsScreen) editArmed(a *App) {
	if s.armed < 0 || s.armed >= len(s.presets) {
		a.SetMessage(missingOr(s.bundle, "settings.msg.preset_none_armed", settingsMsgPresetNoneArmed))
		return
	}
	cur := s.presets[s.armed]
	promptPresetValue(a, s.bundle, missingOr(s.bundle, "settings.prompt.preset_numbers", settingsPromptPresetNumbers), cur.Numbers, func(nums string, a *App) {
		if strings.TrimSpace(nums) == "" {
			a.SetMessage(missingOr(s.bundle, "settings.msg.preset_numbers_empty", settingsMsgPresetNumbersEmpty))
			return
		}
		// r8 P1-B: the edit door armed the M4 gate for ANY non-empty answer,
		// so a misdirected paste rewrote swarm-state.json with numbers the
		// §2.7 selection expander would reject (the append door's r7 F-01
		// gate never covered this half). The same ONE validator now refuses
		// here before the consent arms; the message names the rule, never the
		// value.
		if err := swarm.ValidateSavedPresetNumbers(nums); err != nil {
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_numbers_invalid", settingsMsgPresetNumbersInvalid),
				map[string]string{"reason": err.Error()}))
			return
		}
		presets, err := loadSavedPresets(s.core, s.latch)
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(cur.Name, nums)...))
			return
		}
		idx := indexPresetFold(presets, cur.Name)
		if idx < 0 {
			// The preset vanished underneath (a hand edit or another session):
			// reload and say so instead of writing at the wrong index. The
			// message names the vanished target, not the add path's "already
			// exists" refusal, which says the opposite of what happened (code
			// review F03).
			s.reload()
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_vanished", settingsMsgPresetVanished),
				map[string]string{"name": presetNameOperand(cur.Name)}))
			return
		}
		gateCorruptSwarmSave(a, s.bundle, s.core, s.latch, func() {
			// The presented "old" numbers are the FRESH read's value, not the one
			// the armed row was painted with: if the two differ (a hand edit
			// between the listing and the prompt), the diff has to describe the
			// file, and the write-time drift check signs that same value.
			s.approveSwarmEdit(a, presets[idx], nums, presets)
		})
	})
}

// approveSwarmEdit opens the M4 approval modal for one saved-preset edit (W22):
// the rewrite sentence naming the store, the changed-field line (numbers old ->
// new, both masked), the preset counts, the atomic sentence, and the removed and
// added diff lines. y re-verifies the presented name list AND the edited entry's
// numbers against a fresh read, then writes the fresh list with that one entry
// rewritten, so an external numbers edit of another preset is carried over
// instead of reverted; n/Esc writes nothing, and a moved store is refused by
// name.
//
// Frame budget: the body is a FIXED six lines with no per-entry list, so there is
// nothing for capApprovalEntries to bound (an edit touches exactly one entry, and
// both its old and new spelling are already two of those six lines). The six fit
// every console the composer keeps a gate on; at the 80x24 reference it uses 6 of
// the 18 rows approvalBodyBudget reserves, which is the same fixed-prefix limit
// the excludeModels gate carries.
func (s *settingsPresetsScreen) approveSwarmEdit(a *App, cur swarm.SavedPreset, nums string, presets []swarm.SavedPreset) {
	storePath := swarmStatePath(s.core)
	storeBase := providers.MaskOperand(filepath.Base(storePath))
	store := providers.MaskOperand(storePath)
	base := swarmEditBaseFrom(presets, cur)
	seeds := providers.FlagSeedOperands(cur.Name, nums)
	title := missingOr(s.bundle, "settings.presets.edit", settingsPresetsEdit)
	summary := []string{
		fmt.Sprintf(swarmEditSentence, presetNameOperand(cur.Name), storeBase),
		fmt.Sprintf(swarmEditFieldLine, swarmNumbersField, presetNumbersOperand(cur.Numbers), presetNumbersOperand(nums)),
		fmt.Sprintf(swarmCountsLine, len(presets), len(presets)),
		s.bundle.Tf("presets.explain.file", map[string]string{"path": store}),
		fmt.Sprintf(swarmAtomicLine, storeBase),
		s.bundle.Tf("modal.approval.diff_removed", map[string]string{
			"line": fmt.Sprintf(swarmEntryFmt, presetNameOperand(cur.Name), presetNumbersOperand(cur.Numbers)),
		}),
		s.bundle.Tf("modal.approval.diff_added", map[string]string{
			"line": fmt.Sprintf(swarmEntryFmt, presetNameOperand(cur.Name), presetNumbersOperand(nums)),
		}),
	}
	a.ShowModal(NewApproval(title, summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		fresh, drift, err := swarmPresetReverify(s.core, s.latch, base)
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), seeds...))
			return
		}
		if drift != "" {
			s.reload()
			if s.onChange != nil {
				s.onChange()
			}
			a.SetMessage(providers.MaskSentence(fmt.Sprintf(swarmDriftRefusal, storeBase, drift), seeds...))
			return
		}
		next, found := replaceSwarmPreset(fresh, cur.Name, nums)
		if !found {
			// The name list matched, so this is a defensive arm rather than a
			// reachable one; it still refuses by name instead of writing.
			s.reload()
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_vanished", settingsMsgPresetVanished),
				map[string]string{"name": presetNameOperand(cur.Name)}))
			return
		}
		if err := s.core.SwarmSavePresets(next); err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), seeds...))
			return
		}
		// The file was just rewritten healthy: the corrupt episode is over.
		s.latch.reconcile(false, "")
		s.reload()
		if s.onChange != nil {
			s.onChange()
		}
		a.SetMessage("[SUCCESS] " + title + " = " + presetNameOperand(cur.Name))
	}))
}

// deleteArmed removes the armed preset behind the shared y/n confirm (the
// delete half of D17). n/Esc writes nothing; y re-reads through the Core
// seam, drops the preset, and persists the shortened list behind the same
// corrupt-file gate the edit path uses (security review F-3).
func (s *settingsPresetsScreen) deleteArmed(a *App) {
	if s.armed < 0 || s.armed >= len(s.presets) {
		a.SetMessage(missingOr(s.bundle, "settings.msg.preset_none_armed", settingsMsgPresetNoneArmed))
		return
	}
	cur := s.presets[s.armed]
	del := missingOr(s.bundle, "settings.presets.delete", settingsPresetsDelete)
	a.ShowModal(NewConfirm(del, []string{fillHubSlots(missingOr(s.bundle, "settings.approval.preset_delete", settingsApprovalPresetDelete),
		map[string]string{"name": presetNameOperand(cur.Name)})}, func(ok bool) {
		if !ok {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		presets, err := loadSavedPresets(s.core, s.latch)
		if err != nil {
			a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(cur.Name)...))
			return
		}
		idx := indexPresetFold(presets, cur.Name)
		if idx < 0 {
			// Same vanished-target refusal as the edit path (code review F03).
			s.reload()
			a.SetMessage(fillHubSlots(missingOr(s.bundle, "settings.msg.preset_vanished", settingsMsgPresetVanished),
				map[string]string{"name": presetNameOperand(cur.Name)}))
			return
		}
		presets = append(presets[:idx], presets[idx+1:]...)
		gateCorruptSwarmSave(a, s.bundle, s.core, s.latch, func() {
			if err := s.core.SwarmSavePresets(presets); err != nil {
				a.SetMessage(providers.MaskSentence("error: "+err.Error(), providers.FlagSeedOperands(cur.Name)...))
				return
			}
			s.latch.reconcile(false, "")
			s.armed = -1
			s.reload()
			if s.onChange != nil {
				s.onChange()
			}
			a.SetMessage("[SUCCESS] " + del + " = " + presetNameOperand(cur.Name))
		})
	}))
}

// promptPresetValue pushes the inline editor for one preset value, seeded
// with the current text for edit-like prompts. It is the management
// screen's twin of settingsScreen.prompt: same prompt screen, same
// Ctrl+S/Esc contract.
func promptPresetValue(a *App, bundle *i18n.Bundle, title, seed string, onSubmit func(text string, a *App)) {
	a.Push(newSettingsPromptScreen(bundle, title, seed, onSubmit))
}

// openExcludeModels pushes the model exclusion list editor (D1=(a): the list is
// providers.json defaults.excludeModels; D2=(b): one rule for every exclusion).
// onChange reloads the settings rows underneath after a save, so the
// swarm.exclude_models row value never goes stale (the managePresets
// precedent). The editor holds the child of the only write path: nothing here
// writes, and the M4 gate inside the editor is the single door.
func (s *settingsScreen) openExcludeModels(a *App, label string) {
	a.Push(newExcludeModelsScreen(s.bundle, s.core, label, s.reload))
}

// excludeModelsScreen is the editable model exclusion list behind the
// swarm.exclude_models row. It lists the EFFECTIVE list: the user's entries
// verbatim, or, when the key is absent, the built-in default rule in its list
// spelling (settings.ExcludeModelsBuiltinRule) so that a first save writes the
// SAME policy out explicitly instead of starting from nothing or weakening it. It
// lets the operator add and remove entries, and writes only through the M4
// approval modal (approveExcludeModels -> Core.SetExcludeModels). Esc pops back
// through the plain App precedence.
//
// entries is the draft the write is composed from; base is the at-open snapshot
// of the on-disk defaults.excludeModels, which the approval diff is signed
// against and the write re-checks (stale-base, the model form's F-M3 pattern).
type excludeModelsScreen struct {
	title    string
	bundle   *i18n.Bundle
	core     *settings.Core
	onChange func()
	list     *stepList
	rows     []Row
	entries  []string
	base     settings.ExcludeModelsBase
	armed    int
	loadErr  string
	app      *App
}

func newExcludeModelsScreen(b *i18n.Bundle, core *settings.Core, title string, onChange func()) *excludeModelsScreen {
	s := &excludeModelsScreen{title: title, bundle: b, core: core, onChange: onChange, armed: -1}
	s.list = newStepList(s.pick)
	s.reload()
	return s
}

// Title returns the originating row's label.
func (s *excludeModelsScreen) Title() string { return s.title }

// Mode is ModeNav: the stepList drives arming and the three action rows.
func (s *excludeModelsScreen) Mode() Mode { return ModeNav }

// reload re-reads the live providers.json (the same source Core.SetExcludeModels
// writes) and reseeds the draft from the effective list. It runs at open and on
// every path that ends in a refusal-after-reload (stale base), so the screen
// never shows a list the next save would silently replace.
func (s *excludeModelsScreen) reload() {
	s.loadErr = ""
	s.base = settings.ExcludeModelsBase{}
	defaults := providers.Defaults{}
	reg, err := providers.Load()
	switch {
	case err == nil && reg != nil:
		defaults = reg.Defaults
	case err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, providers.ErrNotConfigured):
		s.loadErr = providers.MaskOperand(err.Error())
	}
	s.base = settings.ExcludeModelsBaseFromDefaults(defaults)
	// settings.ExcludeModelsSeed is the one seed source (no local copy of the
	// seed spelling lives here): it returns the stored entries verbatim, or, when
	// the key is absent, the single entry settings.ExcludeModelsBuiltinRule
	// ("family:k3"), the exact list spelling of the built-in predicate
	// (providers.IsK3Family). The pre-fix bare "k3" is NOT that reproduction: it
	// matched only a provider or a model literally named k3, so a first save of it
	// dropped the family-declared and id-token rows from the excluded pool.
	s.entries = settings.ExcludeModelsSeed(defaults)
	s.armed = -1
	s.rebuild()
}

// excludeModelsRowPrefix namespaces an entry row ID. Row IDs are never
// rendered; they exist so rebuild can restore the cursor by identity when the
// list is rebuilt after arming (the settingsSavedPresetRowPrefix convention).
// FIX-G F4 (round-2 LOW): the ID carries the row's POSITION as well as its
// entry text. A list may legitimately hold the same entry twice (the loader and
// the save door keep order and duplicates exactly as the user typed them), and
// an entry-text-only ID made the restore below land on the FIRST row carrying
// that text: arming row 2 of ["a","a"] rebuilt the list with the cursor on row
// 1 while the "(armed)" suffix stayed on row 2. The position qualifier makes
// every row unique, so the cursor and the armed marker cannot diverge.
const excludeModelsRowPrefix = "exclude-entry:"

// rebuild composes the entry rows plus the three action rows and restores the
// cursor by row identity, so arming a row does not scroll it away (code review
// C6).
func (s *excludeModelsScreen) rebuild() {
	oldCursor := s.list.Cursor()
	oldID := ""
	if oldCursor >= 0 && oldCursor < len(s.rows) {
		oldID = s.rows[oldCursor].ID
	}
	rows := make([]Row, 0, len(s.entries)+3)
	for i, entry := range s.entries {
		// The entries are user-set operands naming registry slots (a provider
		// name, a model id, or provider/model), so each renders through
		// providers.MaskSlot, the one registry-slot display authority the
		// sibling preset rows and the view row's value use. Benign entries
		// render byte-identically.
		label := providers.MaskSlot(entry)
		if i == s.armed {
			label += " " + missingOr(s.bundle, "settings.exclude.armed", settingsExcludeArmed)
		}
		rows = append(rows, Row{ID: excludeModelsRowPrefix + strconv.Itoa(i) + ":" + entry, Label: label})
	}
	rows = append(rows,
		Row{ID: "add", Label: missingOr(s.bundle, "settings.exclude.add", settingsExcludeAdd)},
		Row{ID: "remove", Label: missingOr(s.bundle, "settings.exclude.remove", settingsExcludeRemove)},
		Row{ID: "save", Label: missingOr(s.bundle, "settings.exclude.save", settingsExcludeSave)},
	)
	s.rows = rows
	s.list.SetRows(rows)
	cursor := oldCursor
	for i, r := range rows {
		if r.ID == oldID {
			cursor = i
			break
		}
	}
	s.list.SetCursor(cursor)
}

// stateLine is the ownership sentence above the list: whether the stored key is
// absent (the built-in k3 rule is in force and the seeded list is that same rule
// in list form, so saving it changes nothing about the pool) or the list is
// already user-owned, which decides what the operator is changing.
func (s *excludeModelsScreen) stateLine() string {
	if s.base.Owned {
		return missingOr(s.bundle, "settings.exclude.owned", settingsExcludeOwned)
	}
	return missingOr(s.bundle, "settings.exclude.builtin_absent", settingsExcludeBuiltinAbsent)
}

// Draw renders the ownership line, the entry window (or the honest empty state),
// and the action hint. The window is sized through pageSizeFor against the frame
// height, so a long list pages inside the frame instead of pushing the action
// rows or the hint off the tail.
func (s *excludeModelsScreen) Draw(a *App) []string {
	s.app = a
	w := a.Width()
	if s.loadErr != "" {
		return []string{cellwidth.Truncate(s.bundle.Tf("err.load_failed", map[string]string{"err": s.loadErr}), w)}
	}
	lines := []string{cellwidth.Truncate(s.stateLine(), w)}
	if len(s.entries) == 0 {
		lines = append(lines, "", cellwidth.Truncate(missingOr(s.bundle, "settings.exclude.entries_empty", settingsExcludeEntriesEmpty), w))
		s.list.SetPageSize(pageSizeFor(a.Height(), 10))
		lines = append(lines, s.list.Render(w)...)
	} else {
		s.list.SetPageSize(pageSizeFor(a.Height(), 10))
		lines = append(lines, s.list.Render(w)...)
	}
	return append(lines, "", cellwidth.Truncate(missingOr(s.bundle, "settings.exclude.hint", settingsExcludeHint), w))
}

// HandleKey routes one key to the stepList.
func (s *excludeModelsScreen) HandleKey(k Key, a *App) {
	s.app = a
	s.list.HandleKey(k)
}

// pick arms an entry row or runs one of the three action rows.
func (s *excludeModelsScreen) pick(idx int) {
	a := s.app
	if a == nil {
		return
	}
	n := len(s.entries)
	switch {
	case idx >= 0 && idx < n:
		s.armed = idx
		s.rebuild()
	case idx == n:
		s.addEntry(a)
	case idx == n+1:
		s.removeArmed(a)
	case idx == n+2:
		s.saveList(a)
	}
}

// addEntry appends one entry after the registry loader's own per-entry rule
// (providers.ValidateExcludeEntry): the same control-rune, trim, empty,
// interior-whitespace and family:-remainder rules the loader applies to
// defaults.excludeModels, so the editor cannot compose a list the loader would
// refuse. The loader's authority is called directly here rather than mirrored,
// because the grammar now carries a form (family:<name>) whose refusal depends on
// the shape of the entry, not only on its character class. A refusal names the
// reason and writes nothing.
func (s *excludeModelsScreen) addEntry(a *App) {
	s.prompt(a, missingOr(s.bundle, "settings.prompt.exclude_entry", settingsPromptExcludeEntry), "", func(text string, a *App) {
		entry, err := providers.ValidateExcludeEntry(text)
		if err != nil {
			a.SetMessage(providers.MaskSentence(
				missingOr(s.bundle, "settings.msg.exclude_invalid", settingsMsgExcludeInvalid)+": "+err.Error(),
				providers.FlagSeedOperands(text)...))
			return
		}
		s.entries = append(s.entries, entry)
		s.armed = len(s.entries) - 1
		s.rebuild()
	})
}

// removeArmed drops the armed entry from the draft. Nothing on disk changes
// until the save gate is approved.
func (s *excludeModelsScreen) removeArmed(a *App) {
	if s.armed < 0 || s.armed >= len(s.entries) {
		a.SetMessage(missingOr(s.bundle, "settings.msg.exclude_none_armed", settingsMsgExcludeNoneArmed))
		return
	}
	s.entries = append(s.entries[:s.armed], s.entries[s.armed+1:]...)
	s.armed = -1
	s.rebuild()
}

// sameExcludeList compares two entry lists in order. Order is part of the stored
// shape (the file keeps it), so a reorder is a change even when the membership is
// identical.
func sameExcludeList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// saveList opens the M4 approval modal over the exact write: the action
// sentence, the entry counts before/after, and one diff line per entry that
// leaves or joins the list (each masked). The diff is bounded to the frame
// budget through capApprovalEntries, so a long list still shows the gate legend
// and an exact "N more entries NOT shown" tail. y is the only path to
// Core.SetExcludeModels; n/Esc writes nothing.
func (s *excludeModelsScreen) saveList(a *App) {
	if s.base.Owned && sameExcludeList(s.entries, s.base.Entries) {
		a.SetMessage(missingOr(s.bundle, "settings.msg.exclude_unchanged", settingsMsgExcludeUnchanged))
		return
	}
	next := append([]string(nil), s.entries...)
	summary := s.approvalSummary(next, a)
	a.ShowModal(NewApproval(s.title, summary, func(approved bool) {
		if !approved {
			a.SetMessage(missingOr(s.bundle, "settings.msg.cancelled", settingsMsgCancelled))
			return
		}
		s.commit(next, a)
	}))
}

// approvalSummary composes the M4 gate body: the action, the counts, the shared
// approval body line, the honest absent-key note (when this write creates the
// key), and the membership diff.
func (s *excludeModelsScreen) approvalSummary(next []string, a *App) []string {
	summary := []string{
		missingOr(s.bundle, "settings.approval.exclude_models", settingsApprovalExcludeModels),
		fillHubSlots(missingOr(s.bundle, "settings.approval.exclude_counts", settingsApprovalExcludeCounts),
			map[string]string{"before": strconv.Itoa(len(s.base.Entries)), "after": strconv.Itoa(len(next))}),
		s.bundle.T("modal.approval.body"),
	}
	if !s.base.Owned {
		summary = append(summary, missingOr(s.bundle, "settings.approval.exclude_absent", settingsApprovalExcludeAbsent))
	}
	entries := make([]string, 0, len(next)+len(s.base.Entries))
	for _, old := range s.base.Entries {
		if !containsExcludeEntry(next, old) {
			entries = append(entries, s.bundle.Tf("modal.approval.diff_removed", map[string]string{
				"line": providers.MaskSlot(old),
			}))
		}
	}
	for _, neu := range next {
		if !containsExcludeEntry(s.base.Entries, neu) {
			entries = append(entries, s.bundle.Tf("modal.approval.diff_added", map[string]string{
				"line": providers.MaskSlot(neu),
			}))
		}
	}
	return append(summary, capApprovalEntries(entries, len(summary), approvalBodyBudget(a))...)
}

// containsExcludeEntry reports whether list holds entry (exact match, the
// comparison the policy itself makes).
func containsExcludeEntry(list []string, entry string) bool {
	for _, e := range list {
		if e == entry {
			return true
		}
	}
	return false
}

// commit runs the approved write. The base is re-verified against a FRESH read
// before Core.SetExcludeModels runs (the model form's F-M3 stale-base gate, and
// Core re-checks the same base again): a list that moved while the diff was on
// screen is refused and the editor reloads, so an external edit is never
// silently reverted. A read failure refuses too, because the write would
// otherwise run without the check. Every error echo is masked, seeded with the
// entry operands.
func (s *excludeModelsScreen) commit(next []string, a *App) {
	seeds := s.seedOperands()
	defaults := providers.Defaults{}
	reg, err := providers.Load()
	switch {
	case err == nil && reg != nil:
		defaults = reg.Defaults
	case err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, providers.ErrNotConfigured):
		a.SetMessage(providers.MaskSentence("error: "+missingOr(s.bundle, "settings.exclude.disk_check_failed", settingsExcludeDiskCheckFailed), seeds...))
		return
	}
	if !s.base.Matches(defaults) {
		s.reload()
		a.SetMessage(missingOr(s.bundle, "settings.exclude.stale_base", settingsExcludeStaleBase))
		if s.onChange != nil {
			s.onChange()
		}
		return
	}
	if err := s.core.SetExcludeModels(next, s.base); err != nil {
		if errors.Is(err, settings.ErrExcludeModelsStale) {
			s.reload()
			if s.onChange != nil {
				s.onChange()
			}
			a.SetMessage(missingOr(s.bundle, "settings.exclude.stale_base", settingsExcludeStaleBase))
			return
		}
		a.SetMessage(providers.MaskSentence("error: "+err.Error(), seeds...))
		return
	}
	s.reload()
	if s.onChange != nil {
		s.onChange()
	}
	a.SetMessage("[SUCCESS] " + s.title)
}

// seedOperands returns the draft entries as mask seeds for error echoes (the
// per-token composition the sibling prompt tails use).
func (s *excludeModelsScreen) seedOperands() []string {
	var seeds []string
	for _, e := range s.entries {
		seeds = append(seeds, providers.FlagSeedOperands(e)...)
	}
	return seeds
}

// prompt pushes the inline editor for one draft entry (the settingsScreen.prompt
// twin, resolved inside this screen so the callback keeps its own draft state).
func (s *excludeModelsScreen) prompt(a *App, title, seed string, onSubmit func(text string, a *App)) {
	a.Push(newSettingsPromptScreen(s.bundle, title, seed, onSubmit))
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
	if seed != "" {
		// Edit-like prompts start the caret at the end so typing extends the
		// seeded value instead of inserting at the top-left.
		s.ed.row = len(s.ed.lines) - 1
		s.ed.col = s.ed.lineLen(s.ed.row)
		s.ed.ensureVisible()
	}
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
