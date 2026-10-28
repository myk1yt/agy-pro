package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"agypro/internal/agenttools"
	"agypro/internal/cellwidth"
	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/inventory"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// Editor zones: which sub-component owns the keys. The scalar fields live in
// two Form instances split around the allow_hosts sub-list, so the grid
// order is name..max_native, the host rows, then search_endpoint..json; the
// seats sub-list sits after the zone-B grid, so the key walk is
// A -> hosts -> B -> seats -> A.
const (
	editorZoneA = iota
	editorZoneHosts
	editorZoneB
	editorZoneSeats
)

// presetZoneAFieldTask is the zone-A index of the task field (name=0,
// task=1). Enter on it opens the known-task picker instead of advancing,
// mirroring the model form's picker fields; Tab/Down still advance from it.
const presetZoneAFieldTask = 1

// Row IDs of the allow_hosts sub-list. Host rows carry ID "host\x00<host>"
// and always sort before the two action rows.
const (
	hostRowPrefix = "host\x00"
	hostRowAdd    = "add"
	hostRowRemove = "remove"
)

// Row IDs of the seats sub-list. Seat rows carry ID "seat\x00<index>\x00<spec>"
// (the index keeps the IDs unique when a hand-edited lineup lists the same
// spec twice) and always sort before the four action rows, which act on the
// armed seat exactly as the host rows act on the armed host.
const (
	seatRowPrefix = "seat\x00"
	seatRowAdd    = "seat-add"
	seatRowUp     = "seat-up"
	seatRowDown   = "seat-down"
	seatRowRemove = "seat-remove"
)

// presetsEditorChrome is the non-window, non-explain row count of the composed
// frame around the two sub-lists: the screen title, the separator under it,
// the zone-A grid (7 fields), the allow_hosts header row, the zone-B grid (5
// fields), the seats header row, the hint row, the separator above the base
// footer, and the base footer itself. The editor has no other height budget
// (there is no status line and no notice block in its base shape), so this is
// the number the two sub-list windows and the explain block above the hint
// are derived from; the one-off rows this frame may add (the not-found line,
// the corrupt-file warning, a live validation message) and the app message
// block are counted per frame in editorChrome.
// At 80x24 the value leaves 5 rows: two for the explain block (the armed
// field's sentence word-wraps over them, r11 F-01) and three split between the
// host window and the seat window, so the grid rows and the hint stay on the
// frame either way.
const presetsEditorChrome = 19

// Engine-default rung values the editor uses for the new-preset pre-fill and
// the seed preview when no providers.json defaults entry supplies the knob.
// The seed display layer substitutes the effective value while the stored
// record stays empty (contract A2). These mirror the flag defaults and engine
// withDefaults literals
// in internal/fanout (cli.go defaultFanout* consts, engine.go DefaultTimeoutSec);
// they are re-declared here because those consts are unexported, and the
// full-suite fanout tests pin the engine side of the pair.
const (
	presetEngineDefaultTask      = "review"
	presetEngineDefaultN         = 3
	presetEngineDefaultTimeout   = fanout.DefaultTimeoutSec
	presetEngineDefaultTools     = "read,search"
	presetEngineDefaultMaxTurns  = 500
	presetEngineDefaultMaxNative = 1
	presetEngineDefaultRoot      = "."
	// presetEngineDefaultOutDir is the engine's per-run output directory
	// pattern (engine.go stamps reports/fanout/<ts> at run start); the
	// placeholder is rendered verbatim because the timestamp only exists
	// once a run starts.
	presetEngineDefaultOutDir = "reports/fanout/<ts>"
)

// Row roles for the preset rung of the source column (D11): the label must
// name HOW the edited row is selected, not imply it is in effect. The
// precedence mirrors loadActivePreset (cli.go): an env-named row beats the
// store's default_preset key; a row that is neither is just a saved row.
// The editor cannot see a past --preset flag, so "named" covers the env
// spelling (AGY_PRO_FANOUT_PRESET) only.
const (
	presetRowDefault = "default" // the store's default_preset key names this row
	presetRowNamed   = "named"   // AGY_PRO_FANOUT_PRESET names this row
	presetRowSaved   = "saved"   // the row exists on disk, neither of the above
)

// builtinEffectiveMaxNative is the value the engine resolves for max_native
// with no flag and no preset (resolveMaxNative: a positive state.json value,
// else the engine fallback 1). The built-in view must show THIS, not a blank cell.
func builtinEffectiveMaxNative() int {
	if st, err := mode.LoadState(); err == nil && st != nil && st.NativeSeatConcurrency > 0 {
		return st.EffectiveNativeSeatConcurrency()
	}
	return presetEngineDefaultMaxNative
}

// English fallbacks for catalog keys this screen resolves through missingOr;
// en.json now carries every key below (catalog-resolved).
const (
	// catalog-resolved, fallback kept: presets.editor.field.name
	presetsEditorFieldName = "name"
	// catalog-resolved, fallback kept: presets.editor.field.task
	presetsEditorFieldTask = "task"
	// catalog-resolved, fallback kept: presets.editor.field.n
	presetsEditorFieldN = "n"
	// catalog-resolved, fallback kept: presets.editor.field.timeout_seconds
	presetsEditorFieldTimeout = "timeout_seconds"
	// catalog-resolved, fallback kept: presets.editor.field.tools
	presetsEditorFieldTools = "tools"
	// catalog-resolved, fallback kept: presets.editor.field.max_turns
	presetsEditorFieldMaxTurns = "max_turns"
	// catalog-resolved, fallback kept: presets.editor.field.max_native
	presetsEditorFieldMaxNative = "max_native"
	// catalog-resolved, fallback kept: presets.editor.field.allow_hosts
	presetsEditorFieldAllowHosts = "allow_hosts"
	// catalog-resolved, fallback kept: presets.editor.field.search_endpoint
	presetsEditorFieldSearchEndpoint = "search_endpoint"
	// catalog-resolved, fallback kept: presets.editor.field.search_key_env
	presetsEditorFieldSearchKeyEnv = "search_key_env"
	// catalog-resolved, fallback kept: presets.editor.field.out_dir
	presetsEditorFieldOutDir = "out_dir"
	// catalog-resolved, fallback kept: presets.editor.field.root
	presetsEditorFieldRoot = "root"
	// catalog-resolved, fallback kept: presets.editor.field.seats
	presetsEditorFieldSeats = "seats"
	// catalog-resolved, fallback kept: presets.editor.field.json
	presetsEditorFieldJSON = "json"
	// catalog-resolved, fallback kept: presets.editor.hosts.add
	presetsEditorHostsAdd = "Add host"
	// catalog-resolved, fallback kept: presets.editor.hosts.remove
	presetsEditorHostsRemove = "Remove host"
	// catalog-resolved, fallback kept: presets.editor.hint
	// (P3-6 picker tie: Enter on the task field opens the known-names picker,
	// so the hint names it; at 78 cells the line fits the 80-column budget.)
	presetsEditorHint = "Tab next section · Enter next field (task: picker) · Ctrl+S save · Right close"
	// catalog-resolved, fallback kept: presets.editor.validation.reserved
	// The ordinary save door refuses rename-into with the rule only, never
	// the reserved-name operand (contract A7).
	presetsEditorValidationReserved = "This preset name is reserved for the built-in defaults row."
	// catalog-resolved, fallback kept: presets.editor.discard.title
	presetsEditorDiscardTitle = "Discard Changes?"
	// catalog-resolved, fallback kept: presets.editor.discard.body
	presetsEditorDiscardBody = "Unsaved changes will be lost."
	// catalog-resolved, fallback kept: presets.editor.saved
	presetsEditorSaved = "Preset {{name}} saved."
	// catalog-resolved, fallback kept: presets.overwrite.title
	presetsOverwriteTitle = "Overwrite Preset?"
	// catalog-resolved, fallback kept: presets.overwrite.body
	presetsOverwriteBody = "Preset {{name}} already exists. Overwrite it?"
	// catalog-resolved, fallback kept: presets.editor.validation.name
	presetsEditorValidationName = "Preset name must not be empty."
	// catalog-resolved, fallback kept: presets.editor.validation.number
	presetsEditorValidationNumber = "{{field}} must be a number or empty."
	// catalog-resolved, fallback kept: presets.editor.validation.tools
	presetsEditorValidationTools = "tools must be a comma list of read,search,write,exec."
	// catalog-resolved, fallback kept: presets.editor.validation.json
	presetsEditorValidationJSON = "json must be true, false, or empty."
	// catalog-resolved, fallback kept: presets.editor.validation.key_env
	presetsEditorValidationKeyEnv = "search_key_env must be an environment variable NAME, not a key value."
	// MISSING-KEY: presets.editor.validation.secret
	presetsEditorValidationSecret = "{{field}} looks like secret or key material and cannot be saved (value hidden). Preset fields store names and paths, never keys."
	// MISSING-KEY: presets.editor.validation.host
	presetsEditorValidationHost = "allow_hosts entries must be bare hostnames or IPs (no scheme, wildcard, or path). Remove the invalid host row and save again."
	// MISSING-KEY: presets.editor.validation.task
	presetsEditorValidationTask = "task must be one of the known task names (review, security, plan-audit, research, ideas, custom), or empty for the engine default. Unknown names silently run the review prompt, so they are refused here."
	// MISSING-KEY: presets.editor.hosts.invalid
	presetsEditorHostsInvalid = "not a valid host: enter a bare hostname or IP (no scheme, wildcard, or path). The entry was kept for editing; nothing was added."
	// MISSING-KEY: presets.editor.hosts.duplicate
	presetsEditorHostsDuplicate = "that host is already in the list. Duplicates are not added."
	// MISSING-KEY: presets.editor.hosts.none_armed
	presetsEditorHostsNoneArmed = "no host is armed for removal: press Enter on a host row first, then press Remove host."
	// catalog-resolved, fallback kept: presets.editor.seats.add
	presetsEditorSeatsAdd = "+ Add seat"
	// catalog-resolved, fallback kept: presets.editor.seats.move_up
	presetsEditorSeatsMoveUp = "▲ Move seat up"
	// catalog-resolved, fallback kept: presets.editor.seats.move_down
	presetsEditorSeatsMoveDown = "▼ Move seat down"
	// catalog-resolved, fallback kept: presets.editor.seats.remove
	presetsEditorSeatsRemove = "✕ Remove seat"
	// catalog-resolved, fallback kept: presets.editor.seats.none_armed
	presetsEditorSeatsNoneArmed = "no seat is selected: press Enter or Space on a seat row first, then press a seat action row."
	// catalog-resolved, fallback kept: presets.editor.seats.auto
	presetsEditorSeatsAuto = "none pinned: the engine picks the lineup automatically (native seats first)"
	// catalog-resolved, fallback kept: presets.editor.seats.empty_row
	presetsEditorSeatsEmptyRow = "(empty seat)"
	// catalog-resolved, fallback kept: presets.editor.seats.pick_title
	presetsEditorSeatsPickTitle = "Add a Seat"
	// catalog-resolved, fallback kept: presets.editor.seats.pick_group
	presetsEditorSeatsPickGroup = "▸ {{group}} ({{count}})"
	// catalog-resolved, fallback kept: presets.editor.seats.pick_section
	presetsEditorSeatsPickSection = "section"
	// catalog-resolved, fallback kept: presets.editor.seats.pick_empty
	presetsEditorSeatsPickEmpty = "no models found: add providers in Hub > Providers, or check the native catalog (`agy models`). Nothing was added."
	// catalog-resolved, fallback kept: presets.editor.seats.pick_failed
	presetsEditorSeatsPickFailed = "the model list could not be read ({{err}}). Nothing was added; try again."
	// catalog-resolved, fallback kept: presets.editor.seats.pick_hint
	presetsEditorSeatsPickHint = "digits or arrows move · Enter adds the model as the next seat · Esc cancels"
	// catalog-resolved, fallback kept: presets.editor.seats.invalid
	presetsEditorSeatsInvalid = "seat {{n}} is not a valid seat: a seat is native:<model id> or provider/model. Remove the row or add the model again with + Add seat."
	// catalog-resolved, fallback kept: presets.editor.validation.seats
	presetsEditorValidationSeats = "seats must be a comma list of native:<model id> or provider/model tokens. Remove or fix the offending seat row and save again."
	// catalog-resolved, fallback kept: presets.editor.seats.warn_prefix
	presetsEditorSeatsWarnPrefix = "warning: "
	// catalog-resolved, fallback kept: presets.editor.seats.warn_missing
	// (provider seats only, and only on the DEFAULT rung: the preset must BE the
	// store default AND the run must name no preset, because a run that selects
	// even the default row BY NAME is the named rung and hard-errors)
	presetsEditorSeatsWarnMissing = "seat {{n}} ({{spec}}) is not in the current model list, so a run that uses this preset as the store default and names no preset skips it and finishes marked degraded; a run that selects this preset by name stops with an error."
	// catalog-resolved, fallback kept: presets.editor.seats.warn_missing_named
	// (provider seat on a NON-default preset: a run that selects this preset by
	// name hard-errors instead of skipping, because skip tolerance is scoped to
	// the standing default-key rung)
	presetsEditorSeatsWarnMissingNamed = "seat {{n}} ({{spec}}) is not in the current model list. Only the store default preset may skip a missing seat, so a run that selects this preset by name (--preset or AGY_PRO_FANOUT_PRESET) stops with an error."
	// catalog-resolved, fallback kept: presets.editor.seats.warn_missing_native
	// (native seats are NEVER drift-checked: the engine appends a native target
	// unconditionally, so an unknown id starts and fails instead of being skipped)
	presetsEditorSeatsWarnMissingNative = "seat {{n}} is a native seat that is not in the native model list. Native seats are never checked before a run, so that seat will start and fail instead of being skipped."
	// catalog-resolved, fallback kept: presets.editor.seats.warn_duplicate
	presetsEditorSeatsWarnDuplicate = "seats {{n}} and {{m}} point at the same model, so they add no second opinion."
	// catalog-resolved, fallback kept: presets.editor.seats.warn_family
	presetsEditorSeatsWarnFamily = "seats {{n}} and {{m}} use the same model family, so they cross-check each other weakly; a different family for one of them gives a real second opinion."
	// The presets.editor.explain.* fallbacks (U8): one plain-words sentence
	// per knob, shown on the frame row above the key hint for whichever field
	// is armed. Catalog-resolved through presets.editor.explain.<field>.
	presetsEditorExplainName           = "The preset's name: how --preset, AGY_PRO_FANOUT_PRESET and Set default address this row."
	presetsEditorExplainTask           = "Which review job every seat runs (review, security, plan-audit, research, ideas, custom). Empty = review."
	presetsEditorExplainN              = "How many seats a run gets when seats below is empty. Empty = 3."
	presetsEditorExplainTimeout        = "Seconds one seat may run before the run stops it. Empty = 300."
	presetsEditorExplainTools          = "Seat tools. read and search are the base set; write adds real file writes and exec adds real command runs on this machine."
	presetsEditorExplainMaxTurns       = "How many turns one seat may take. Empty = 500."
	presetsEditorExplainMaxNative      = "Concurrent free-subscription (native) seats. Empty = the state.json setting, or 1 when unset."
	presetsEditorExplainAllowHosts     = "The URLs provider seats may read. Empty = provider seats cannot read any URL (native seats are unaffected)."
	presetsEditorExplainSearchEndpoint = "Empty=providers: agy Google search (1 native turn, sub quota); Tavily/Serper API. Official Gemini grounding: GEMINI_API_KEY, 5k free/mo, $/query; no direct."
	presetsEditorExplainSearchKeyEnv   = "Optional search API key env NAME; value not stored. Without endpoint, key alone stays native, not explicit."
	presetsEditorExplainOutDir         = "Where run reports go. Empty = the engine picks reports/fanout/<timestamp> at run start."
	presetsEditorExplainRoot           = "The workspace the seats read. Empty or . = the current workspace."
	presetsEditorExplainSeats          = "Pinned lineup; empty = the engine picks, native first. If a seat's model is missing from the model list, the default warns and skips it; a named run stops."
	presetsEditorExplainJSON           = "Also write a machine-readable summary next to the report."
	// catalog-resolved, fallback kept: presets.editor.source.state
	presetsEditorSourceState = "state.json"
	// catalog-resolved, fallback kept: presets.editor.source.none
	presetsEditorSourceNone = "(none)"
	// MISSING-KEY: presets.editor.source.preset_default
	presetsEditorSourcePresetDefault = "default preset"
	// MISSING-KEY: presets.editor.source.preset_named
	presetsEditorSourcePresetNamed = "named preset"
	// MISSING-KEY: presets.editor.source.preset_saved
	presetsEditorSourcePresetSaved = "saved preset"
	// MISSING-KEY: presets.editor.source.builtin
	presetsEditorSourceBuiltin = "built-in"
)

// presetEditorScreen is S8: the fanout preset editor (architect-report
// section 3, screen 8). A field grid over every pre-configurable knob, with
// a read-only source column per field naming the resolution rung that
// currently supplies the effective value (mirroring buildRunConfig's
// ladder). Save writes ONLY fanout-presets.json through the store (atomic
// 0600); providers.json is never written from this screen.
//
// Esc handling: the App dispatcher consumes Esc before any screen sees it
// (clears the message, then pops), so a dirty editor would silently lose
// edits. The screen therefore follows the model form pattern
// (screen_model_form.go): while the editor is dirty a transparent guard
// modal (presetEditorGuard) receives every key in CONFIRM scope before the
// App Esc precedence, forwards everything except Esc to the editor, and
// turns Esc on a dirty editor into the discard-confirm. Right stays the
// explicit close gesture; a clean Esc never arms the guard and pops
// through the plain App precedence.
type presetEditorScreen struct {
	name        string // original preset name ("" = new; fanout.BuiltinPresetName = the seed row)
	store       *fanout.PresetFileStore
	preset      *fanout.Preset // on-disk preset being edited (nil for new and for the absent seed)
	defaultName string         // the store's default_preset key at load time ("" = none)
	notFound    string
	def         providers.Defaults
	valA        []string // seeded zone-A values (name..max_native)
	valB        []string // seeded zone-B values (search_endpoint..json)
	formA       *Form    // name, task, n, timeout_seconds, tools, max_turns, max_native
	formB       *Form    // search_endpoint, search_key_env, out_dir, root, json
	hosts       *stepList
	hostVals    []string
	hostsSel    int
	pending     string // add-host inline input
	hostsDirty  bool
	seats       *stepList
	seatVals    []string // one row per seat token, in seat order (1..N)
	seatSel     int      // armed seat row the action rows act on (-1 = none)
	seatsDirty  bool
	seatInv     *inventory.Inventory // cached model inventory (nil until a picker open succeeds)
	zone        int
	b           *i18n.Bundle
	lang        string
	dropNotices []string // per-record notices from the latest load; never arm the latch
	dropMsgSent bool     // the latest notices' one-time message-line copy went out
	app         *App
	guard       *presetEditorGuard
	corruptLatch
}

// NewPresetEditorScreen builds the editor for one preset. An empty name
// edits a new preset; fanout.BuiltinPresetName edits the built-in defaults
// seed row (a real record once saved, an effective-value preview until
// then). The store resolves through fanout.DefaultPresetsPath
// (AGY_PRO_HOME aware); tests point AGY_PRO_HOME at a temp dir.
func NewPresetEditorScreen(name string) Screen {
	e := &presetEditorScreen{}
	lang := mode.DefaultLanguage
	if st, err := mode.LoadState(); err == nil && st != nil {
		lang = st.EffectiveLanguage()
	}
	e.setLang(i18n.Normalize(lang))
	e.name = name
	if st, err := fanout.NewPresetFileStore(); err == nil {
		e.store = st
		// Surface the store's corrupt-file warning instead of letting the
		// nil WarnOut discard it: saving into an empty fallback store must
		// never silently overwrite the user's presets (security F5). The
		// per-record drop/duplicate notices take their own channel
		// (NoticeOut) so a dropped record shows as what it is and never
		// arms the corrupt latch (R3-F-05).
		st.WarnOut = corruptPresetSink{count: &e.corruptLatch.corruptEvents}
		st.NoticeOut = presetDropSink{lines: &e.dropNotices}
		// The retired-models advisory is rendered from the localized catalog
		// (appended below and on every save reload), so the store's English twin
		// is discarded here instead of printing the fact twice.
		st.ModelsDropOut = io.Discard
		before := e.corruptLatch.corruptEvents
		pf, err := st.LoadPresets()
		loadErr := ""
		if err != nil {
			loadErr = err.Error()
		}
		e.corruptLatch.reconcile(e.corruptLatch.corruptEvents > before, loadErr)
		if pf != nil {
			for _, legacy := range pf.LegacyModels {
				e.dropNotices = append(e.dropNotices, presetLegacyModelsNotice(e.b, legacy))
			}
		}
		if err == nil && pf != nil {
			e.defaultName = pf.DefaultPreset
			if e.name != "" {
				if p, ok := pf.FindPreset(e.name); ok {
					cp := *p
					cp.AllowHosts = append([]string(nil), p.AllowHosts...)
					e.preset = &cp
				} else if !fanout.IsBuiltinPresetName(e.name) {
					// The seed name with no seed record on disk is the
					// honest empty editor (the first save creates the
					// record); any other missing name is a real not-found.
					e.notFound = e.name
				}
			}
		}
	}
	if reg, err := providers.Load(); err == nil && reg != nil {
		e.def = reg.Defaults // read-only ladder input; never written here
	}
	// Seed values come before the forms are built: Form keeps its fields
	// unexported, so values are baked into the Field definitions. The seed
	// row is the one case that NEVER seeds engine literals into the form:
	// an untouched field must stay empty so the save writes nothing for it
	// and the ladder keeps src="engine" (contract A2). Effective values
	// still show: the render layer substitutes defaults-rung or engine values
	// for empty seed fields, and sources() labels that same rung.
	e.valA = []string{"", "", "", "", "", "", ""}
	e.valB = []string{"", "", "", "", ""}
	if e.preset != nil {
		p := e.preset
		e.valA = []string{p.Name, p.Task, intText(p.N), intText(p.TimeoutSeconds),
			p.Tools, intText(p.MaxTurns), intText(p.MaxNative)}
		jsonVal := ""
		if p.JSON {
			jsonVal = "true"
		}
		e.valB = []string{p.SearchEndpoint, p.SearchKeyEnv, p.OutDir, p.Root, jsonVal}
		e.hostVals = append([]string(nil), p.AllowHosts...)
		e.seatVals = splitSeatSpecs(p.Seats)
	} else if e.name == "" {
		// New preset: pre-fill the engine defaults (U8) so the editor opens
		// showing the values a run would actually use, instead of an empty
		// grid the CEO could not map onto behavior. Every seed equals the
		// rung the source column already names, so the ladder behavior for a
		// never-touched field is unchanged. Two fields stay empty on purpose:
		// out_dir, because the "<ts>" pattern is a display placeholder only
		// and the engine stamps reports/fanout/<timestamp> when the field is
		// empty, so persisting the placeholder would freeze one timestamp;
		// and max_native, because the state.json rung owns that value and a
		// seed here would pin it, so the editor would silently override the
		// state setting the user already controls.
		e.valA = []string{"", presetEngineDefaultTask,
			strconv.Itoa(presetEngineDefaultN), strconv.Itoa(presetEngineDefaultTimeout),
			presetEngineDefaultTools, strconv.Itoa(presetEngineDefaultMaxTurns), ""}
		e.valB = []string{"", "", "", presetEngineDefaultRoot, ""}
	} else if e.seedEdit() {
		// The seed row with no record on disk: the name field is fixed to
		// the reserved identity (handleZoneA refuses text edits there), the
		// value fields stay empty, and the frame shows effective values
		// through the display fallback.
		e.valA[0] = fanout.BuiltinPresetName
	}
	e.formA = NewForm(e.fieldsA())
	e.formB = NewForm(e.fieldsB())
	e.hosts = newStepList(e.hostPick)
	e.hostsSel = -1
	e.syncHostRows()
	e.seats = newStepList(e.seatPick)
	e.seats.SetOnSelect(e.seatSelect)
	e.seatSel = -1
	e.syncSeatRows()
	return e
}

// seedEdit reports whether this editor edits the reserved built-in defaults
// seed row [1]: a record whose name happens to be fanout.BuiltinPresetName.
// Everything the old read-only "builtin view" did specially is now keyed on
// this predicate; the sentinel is gone.
func (e *presetEditorScreen) seedEdit() bool { return fanout.IsBuiltinPresetName(e.name) }

// seedEngineDisplayValue maps a field key onto the engine fallback used by
// seedDisplayValue when no higher rung supplies an UNSET seed field. The
// stored record stays empty (contract A2). Keys with no engine default
// (search_endpoint, search_key_env, and the identity/name slot)
// return "" and the cell renders empty, as the old built-in view did.
func seedEngineDisplayValue(key string) string {
	switch key {
	case "task":
		return presetEngineDefaultTask
	case "n":
		return strconv.Itoa(presetEngineDefaultN)
	case "timeout_seconds":
		return strconv.Itoa(presetEngineDefaultTimeout)
	case "tools":
		return presetEngineDefaultTools
	case "max_turns":
		return strconv.Itoa(presetEngineDefaultMaxTurns)
	case "max_native":
		// The state.json rung supplies the value (resolveMaxNative), so the
		// preview must show the LIVE effective concurrency, not a blank cell
		// and not a frozen constant.
		return strconv.Itoa(builtinEffectiveMaxNative())
	case "out_dir":
		return presetEngineDefaultOutDir
	case "root":
		return presetEngineDefaultRoot
	case "json":
		return "false"
	}
	return ""
}

// seedDisplayValue previews the effective value for an unset seed field.
// The four providers.json defaults knobs mirror resolvePresetSources and the
// runtime ladder; every other field keeps its engine or live-state preview.
// This is display-only: the underlying form value stays empty for A2.
func (e *presetEditorScreen) seedDisplayValue(key string) string {
	switch key {
	case "n":
		if e.def.N > 0 {
			return strconv.Itoa(e.def.N)
		}
	case "timeout_seconds":
		if e.def.TimeoutSeconds > 0 {
			return strconv.Itoa(e.def.TimeoutSeconds)
		}
	case "tools":
		if e.def.Tools != "" {
			return e.def.Tools
		}
	case "max_turns":
		if e.def.MaxTurns > 0 {
			return strconv.Itoa(e.def.MaxTurns)
		}
	}
	return seedEngineDisplayValue(key)
}

// setLang loads the bundle for lang once per language.
func (e *presetEditorScreen) setLang(lang string) {
	if lang == e.lang && e.b != nil {
		return
	}
	e.lang = lang
	e.b = i18n.Load(lang)
}

// Mode is ModeEdit: the field values accept text through EditRune.
func (e *presetEditorScreen) Mode() Mode { return ModeEdit }

// Title returns the editor title. The seed row's title is its name through
// the same slot authority the list renders row [1] with (byte-identical for
// the benign reserved name).
func (e *presetEditorScreen) Title() string {
	if e.seedEdit() {
		return presetNameOperand(fanout.BuiltinPresetName)
	}
	return e.b.T("presets.editor.title")
}

// fieldsA builds the zone-A field definitions (grid rows above the host
// sub-list), with the seeded values baked in.
func (e *presetEditorScreen) fieldsA() []Field {
	return []Field{
		{Label: presetsEditorFieldName, Value: e.valA[0], Validate: e.validateName},
		{Label: presetsEditorFieldTask, Value: e.valA[1]},
		{Label: presetsEditorFieldN, Value: e.valA[2], Validate: e.validateNumber(presetsEditorFieldN)},
		{Label: presetsEditorFieldTimeout, Value: e.valA[3], Validate: e.validateNumber(presetsEditorFieldTimeout)},
		{Label: presetsEditorFieldTools, Value: e.valA[4], Validate: e.validateTools},
		{Label: presetsEditorFieldMaxTurns, Value: e.valA[5], Validate: e.validateNumber(presetsEditorFieldMaxTurns)},
		{Label: presetsEditorFieldMaxNative, Value: e.valA[6], Validate: e.validateNumber(presetsEditorFieldMaxNative)},
	}
}

// fieldsB builds the zone-B field definitions (grid rows below the host
// sub-list), with the seeded values baked in. seats is NOT a field any more:
// the structured seat list below the grid is the only writer of the stored
// comma string, so the free-text spelling could only produce a second,
// disagreeing source of truth.
func (e *presetEditorScreen) fieldsB() []Field {
	return []Field{
		{Label: presetsEditorFieldSearchEndpoint, Value: e.valB[0]},
		{Label: presetsEditorFieldSearchKeyEnv, Value: e.valB[1], Validate: e.validateKeyEnv},
		{Label: presetsEditorFieldOutDir, Value: e.valB[2]},
		{Label: presetsEditorFieldRoot, Value: e.valB[3]},
		{Label: presetsEditorFieldJSON, Value: e.valB[4], Validate: e.validateJSON},
	}
}

// validateName rejects an empty preset name.
func (e *presetEditorScreen) validateName(s string) error {
	if strings.TrimSpace(s) == "" {
		return strError(missingOr(e.b, "presets.editor.validation.name", presetsEditorValidationName))
	}
	return nil
}

// validateNumber accepts empty (unset, ladder fall-through) or a
// non-negative integer.
func (e *presetEditorScreen) validateNumber(field string) func(string) error {
	return func(s string) error {
		if _, err := parsePresetInt(s); err != nil {
			return strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.number", presetsEditorValidationNumber),
				map[string]string{"field": field}))
		}
		return nil
	}
}

// validateTools accepts empty or a comma list of the closed tool tokens.
func (e *presetEditorScreen) validateTools(s string) error {
	if validPresetTools(s) {
		return nil
	}
	return strError(missingOr(e.b, "presets.editor.validation.tools", presetsEditorValidationTools))
}

// validateJSON accepts empty, true, or false.
func (e *presetEditorScreen) validateJSON(s string) error {
	switch strings.TrimSpace(s) {
	case "", "true", "false":
		return nil
	}
	return strError(missingOr(e.b, "presets.editor.validation.json", presetsEditorValidationJSON))
}

// validateKeyEnv accepts empty or an environment-variable NAME vetted by
// providers.ValidateAPIKeyEnvName. A key VALUE is rejected; the error text
// never echoes the operand.
func (e *presetEditorScreen) validateKeyEnv(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if err := providers.ValidateAPIKeyEnvName(s); err != nil {
		return strError(missingOr(e.b, "presets.editor.validation.key_env", presetsEditorValidationKeyEnv))
	}
	return nil
}

// parsePresetInt parses an unset-or-non-negative-integer field value.
func parsePresetInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, strError("not a number")
	}
	return n, nil
}

// validPresetTools reports whether s is empty or a comma list of the closed
// fanout tool tokens (the --tools spelling: read,search,write,exec).
func validPresetTools(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	for _, tok := range strings.Split(s, ",") {
		switch strings.TrimSpace(tok) {
		case "read", "search", "write", "exec":
		default:
			return false
		}
	}
	return true
}

// intText renders an unset-or-positive int as field text (0 = empty).
func intText(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// strError is the error adapter for the Form.Validate signatures.
type strError string

func (s strError) Error() string { return string(s) }

// syncHostRows rebuilds the allow_hosts sub-list rows: one per host, then
// Add (carrying the inline pending input) and Remove (carrying the sticky
// selection). The cursor is preserved and clamped.
func (e *presetEditorScreen) syncHostRows() {
	rows := make([]Row, 0, len(e.hostVals)+2)
	for _, h := range e.hostVals {
		rows = append(rows, Row{ID: hostRowPrefix + h, Label: h})
	}
	add := missingOr(e.b, "presets.editor.hosts.add", presetsEditorHostsAdd)
	if e.pending != "" {
		add += ": " + e.pending
	}
	rows = append(rows, Row{ID: hostRowAdd, Label: add})
	remove := missingOr(e.b, "presets.editor.hosts.remove", presetsEditorHostsRemove)
	note := ""
	if e.hostsSel >= 0 && e.hostsSel < len(e.hostVals) {
		note = e.hostVals[e.hostsSel]
	}
	rows = append(rows, Row{ID: hostRowRemove, Label: remove, Note: note})
	cursor := e.hosts.Cursor()
	e.hosts.SetRows(rows)
	e.hosts.SetCursor(cursor)
}

// hostPick is the allow_hosts activation callback. Host rows arm the sticky
// selection for Remove; the Add row commits the inline pending input; the
// Remove row drops the armed host.
func (e *presetEditorScreen) hostPick(idx int) {
	if idx < 0 || idx >= e.hosts.Len() {
		return
	}
	switch id := e.hosts.Row(idx).ID; {
	case strings.HasPrefix(id, hostRowPrefix):
		e.hostsSel = idx
	case id == hostRowAdd:
		e.commitPendingHost()
	case id == hostRowRemove:
		e.removeSelectedHost()
	}
}

// commitPendingHost validates, normalizes and appends the inline input.
// D14 (P3-5): an entry the allow-list can never match (scheme prefix,
// wildcard, path, whitespace, empty) is refused with a named message and the
// pending input is KEPT on the Add row so the offending value stays visible
// for editing; duplicates are refused the same way (the row already shows the
// value). The engine re-normalizes at build time; storing the canonical
// shape keeps the file deterministic.
func (e *presetEditorScreen) commitPendingHost() {
	if e.pending == "" {
		// Nothing typed: Enter on the empty Add row stays a quiet no-op.
		return
	}
	if !agenttools.ValidateAllowHost(e.pending) {
		if e.app != nil {
			e.app.SetMessage(missingOr(e.b, "presets.editor.hosts.invalid", presetsEditorHostsInvalid))
		}
		e.syncHostRows()
		return
	}
	norm := agenttools.NormalizeAllowHosts([]string{e.pending})
	if len(norm) == 0 {
		return
	}
	for _, h := range e.hostVals {
		if h == norm[0] {
			if e.app != nil {
				e.app.SetMessage(missingOr(e.b, "presets.editor.hosts.duplicate", presetsEditorHostsDuplicate))
			}
			e.syncHostRows()
			return
		}
	}
	e.pending = ""
	e.hostVals = append(e.hostVals, norm[0])
	e.hostsDirty = true
	e.syncHostRows()
}

// removeSelectedHost drops the armed host, if any. D15 (P3-5): with nothing
// armed the press used to no-op silently; it now names the missing arming
// step so the user knows a host row must be selected first.
func (e *presetEditorScreen) removeSelectedHost() {
	if e.hostsSel < 0 || e.hostsSel >= len(e.hostVals) {
		if e.app != nil {
			e.app.SetMessage(missingOr(e.b, "presets.editor.hosts.none_armed", presetsEditorHostsNoneArmed))
		}
		return
	}
	e.hostVals = append(e.hostVals[:e.hostsSel], e.hostVals[e.hostsSel+1:]...)
	e.hostsSel = -1
	e.hostsDirty = true
	e.syncHostRows()
}

// seatSpecOperand renders one stored seat token for display through
// providers.MaskSlot, the registry-slot authority this screen already renders
// preset names with (presetNameOperand) and the picker's own rows use. A seat
// spec is registry-borne text that reaches the same frames, so it belongs to the
// same authority: MaskSlot additionally strips control runes (Cc/Cf) that
// MaskOperand leaves in place, and it masks a credential-shaped token instead of
// relying on the location exemption.
//
// The separators a legitimate spec needs survive untouched: MaskSlot only
// rewrites a location-shaped value, so "native:<id>" (no "/") and
// "<provider>/<model>" (a single interior "/" that is not a path prefix) render
// byte-identically, and the save-time grammar stays the only thing that decides
// whether a token is legal (fix-list FL-10).
func seatSpecOperand(spec string) string { return providers.MaskSlot(spec) }

// seatTokenIsNative reports whether one stored seat token is a native seat. The
// run-time grammar owns the answer (ParseSeatSpecs resolves the case-insensitive
// "native:" prefix), and the prefix test is the fallback for a token the parser
// refuses (a malformed row, or one carrying a control rune the FL-4 gate
// rejects): such a row is refused at save anyway, but the warning scan must never
// guess a provider seat native and hand it the wrong sentence.
func seatTokenIsNative(spec string) bool {
	if specs, err := fanout.ParseSeatSpecs(spec); err == nil && len(specs) == 1 {
		return specs[0].Kind == fanout.SeatKindNative
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec)), "native:")
}

// splitSeatSpecs splits a stored seats value into one row per token. An empty
// (or whitespace-only) value yields no rows, which is the "the engine
// auto-selects" state, never an empty seat. Tokens are trimmed exactly as
// ParseSeatSpecs trims them, so the rows re-serialize to the parser's own
// normalized form; a malformed token is kept verbatim so the row shows what is
// on disk and only the save refuses it.
func splitSeatSpecs(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	toks := strings.Split(s, ",")
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		out = append(out, strings.TrimSpace(t))
	}
	return out
}

// joinSeatSpecs renders the rows back into the stored comma grammar. An empty
// row list renders "" (auto mode). A malformed row is joined as-is: nothing is
// dropped behind the user's back, so the save refusal can name its seat
// number.
func joinSeatSpecs(vals []string) string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, strings.TrimSpace(v))
	}
	return strings.Join(out, ",")
}

// syncSeatRows rebuilds the seat sub-list rows: one per seat (in seat order,
// which IS report/seat-id order) with its armed marker, then the Add and the
// three row verbs. The
// cursor is restored by ROW ID with the old index as the fallback (see the tail
// of this function), so a mutation that shifts the layout cannot leave the
// cursor on a different row than the one the user activated.
func (e *presetEditorScreen) syncSeatRows() {
	rows := make([]Row, 0, len(e.seatVals)+4)
	for i, spec := range e.seatVals {
		label := seatSpecOperand(spec)
		if strings.TrimSpace(spec) == "" {
			label = missingOr(e.b, "presets.editor.seats.empty_row", presetsEditorSeatsEmptyRow)
		}
		marker := "[ ] "
		if e.seatSel == i {
			marker = "[x] "
		}
		rows = append(rows, Row{ID: seatRowPrefix + strconv.Itoa(i) + "\x00" + spec, Label: marker + label})
	}
	rows = append(rows, Row{ID: seatRowAdd, Label: missingOr(e.b, "presets.editor.seats.add", presetsEditorSeatsAdd)})
	up := Row{ID: seatRowUp, Label: missingOr(e.b, "presets.editor.seats.move_up", presetsEditorSeatsMoveUp)}
	down := Row{ID: seatRowDown, Label: missingOr(e.b, "presets.editor.seats.move_down", presetsEditorSeatsMoveDown)}
	remove := Row{ID: seatRowRemove, Label: missingOr(e.b, "presets.editor.seats.remove", presetsEditorSeatsRemove)}
	if e.seatSel >= 0 && e.seatSel < len(e.seatVals) {
		remove.Note = seatSpecOperand(e.seatVals[e.seatSel])
	}
	rows = append(rows, up, down, remove)
	// The cursor is restored by row ID first (the action rows keep theirs across
	// every mutation, so a verb stays under the cursor), then by the old index,
	// which is what keeps Delete on a seat row from jumping to the top: the
	// removed row's ID is gone, and the clamp lands on its neighbour.
	cursor, oldID := 0, ""
	if e.seats != nil {
		cursor = e.seats.Cursor()
		if cursor >= 0 && cursor < e.seats.Len() {
			oldID = e.seats.Row(cursor).ID
		}
	}
	e.seats.SetRows(rows)
	target := cursor
	for i, r := range rows {
		if oldID != "" && r.ID == oldID {
			target = i
			break
		}
	}
	e.seats.SetCursor(target)
}

// seatRowIndex decodes the seat index a row ID carries, reporting false for
// every action row.
func (e *presetEditorScreen) seatRowIndex(cursor int) (int, bool) {
	if e.seats == nil || cursor < 0 || cursor >= e.seats.Len() {
		return 0, false
	}
	id := e.seats.Row(cursor).ID
	if !strings.HasPrefix(id, seatRowPrefix) {
		return 0, false
	}
	rest := id[len(seatRowPrefix):]
	end := strings.IndexByte(rest, 0)
	if end < 0 {
		return 0, false
	}
	i, err := strconv.Atoi(rest[:end])
	if err != nil || i < 0 || i >= len(e.seatVals) {
		return 0, false
	}
	return i, true
}

// seatSelect toggles the sticky target without activating the row. Space on an
// action row is a consumed no-op through the shared stepList select callback.
func (e *presetEditorScreen) seatSelect(idx int) {
	i, ok := e.seatRowIndex(idx)
	if !ok {
		return
	}
	if e.seatSel == i {
		e.seatSel = -1
	} else {
		e.seatSel = i
	}
	e.syncSeatRows()
}

// seatPick is the seats activation callback. A seat row arms the sticky
// selection (the host-row idiom, so Enter-then-verb transfers); the action rows
// spend it. Delete on a seat row is the same removal without the arming step
// (see handleZoneSeats).
func (e *presetEditorScreen) seatPick(idx int) {
	if e.seats == nil || idx < 0 || idx >= e.seats.Len() {
		return
	}
	id := e.seats.Row(idx).ID
	switch {
	case strings.HasPrefix(id, seatRowPrefix):
		if i, ok := e.seatRowIndex(idx); ok {
			e.seatSel = i
			e.syncSeatRows()
		}
	case id == seatRowAdd:
		e.openSeatPicker()
	case id == seatRowUp:
		e.moveSelectedSeat(-1)
	case id == seatRowDown:
		e.moveSelectedSeat(1)
	case id == seatRowRemove:
		e.removeSelectedSeat()
	}
}

// moveSelectedSeat reorders the armed seat by delta (-1 up, +1 down). The
// selection follows the seat it holds, so ▲ then ▲ again keeps walking the same
// seat up. With nothing armed the press names the missing arming step instead
// of no-oping (the host-row precedent).
func (e *presetEditorScreen) moveSelectedSeat(delta int) {
	if e.seatSel < 0 || e.seatSel >= len(e.seatVals) {
		e.seatMessage(missingOr(e.b, "presets.editor.seats.none_armed", presetsEditorSeatsNoneArmed))
		return
	}
	to := e.seatSel + delta
	if to < 0 || to >= len(e.seatVals) {
		// The edge of the list is not an error: the lineup simply does not
		// move, and the row stays where it is.
		return
	}
	e.seatVals[e.seatSel], e.seatVals[to] = e.seatVals[to], e.seatVals[e.seatSel]
	e.seatSel = to
	e.seatsDirty = true
	e.syncSeatRows()
	// Keep the cursor on the armed seat so the moved row stays under the
	// cursor when the verb ran from the focused seat row rather than an action
	// row (the action-row path leaves the cursor where the user put it).
	if i, ok := e.seatRowIndex(e.seats.Cursor()); ok && i == to-delta {
		e.seats.SetCursor(to)
	}
}

// removeSelectedSeat drops the armed seat. The stored order is the seat order,
// so removal renumbers every seat after it; that is the visible contract.
func (e *presetEditorScreen) removeSelectedSeat() {
	if e.seatSel < 0 || e.seatSel >= len(e.seatVals) {
		e.seatMessage(missingOr(e.b, "presets.editor.seats.none_armed", presetsEditorSeatsNoneArmed))
		return
	}
	e.removeSeatAt(e.seatSel)
}

// removeSeatAt drops seat index i, clears the arming and rebuilds the rows. The
// cursor clamps into the new range through SetCursor.
func (e *presetEditorScreen) removeSeatAt(i int) {
	if i < 0 || i >= len(e.seatVals) {
		return
	}
	e.seatVals = append(e.seatVals[:i], e.seatVals[i+1:]...)
	e.seatSel = -1
	e.seatsDirty = true
	e.syncSeatRows()
}

// appendSeat adds one picked model as the LAST seat (the picker's contract: the
// new seat lands at the end of the lineup, seat N+1). The value is the stored
// token verbatim, so what the picker showed is what the file carries. An
// existing identical row is still added (a duplicated seat is the user's
// choice; the save-time warning names it).
func (e *presetEditorScreen) appendSeat(spec string) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return
	}
	e.seatVals = append(e.seatVals, spec)
	e.seatSel = len(e.seatVals) - 1
	e.seatsDirty = true
	e.syncSeatRows()
	e.seats.SetCursor(len(e.seatVals) - 1)
}

// seatMessage puts one line on the app message error, when there is an app.
func (e *presetEditorScreen) seatMessage(msg string) {
	if e.app != nil {
		e.app.SetMessage(msg)
	}
}

// presetSeatInventoryTimeout bounds the one model-inventory read the seat
// picker performs. The fetcher carries its own per-invocation deadline; this is
// the wall the TUI will wait for the whole read, so an unresponsive catalog
// cannot freeze the editor for longer than this.
const presetSeatInventoryTimeout = 10 * time.Second

// presetSeatInventory is the model-inventory seam the seat picker reads. It
// resolves through the same settings.Core seam `agy pro models --all` uses
// (settings.LoadInventory -> inventory.Load with the cached real-agy native
// fetcher), so the picker offers exactly the rows the CLI inventory offers.
// Tests replace it to avoid spawning `agy`.
var presetSeatInventory = func(ctx context.Context) (*inventory.Inventory, error) {
	return settings.NewCore(settings.Deps{}).LoadInventory(ctx)
}

// seatInventory returns the cached inventory, reading it once per editor
// session on success. A failed read is not cached, so a later picker open
// retries instead of carrying the failure for the rest of the edit.
func (e *presetEditorScreen) seatInventory() (*inventory.Inventory, error) {
	if e.seatInv != nil {
		return e.seatInv, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), presetSeatInventoryTimeout)
	defer cancel()
	inv, err := presetSeatInventory(ctx)
	if err == nil && inv != nil {
		e.seatInv = inv
	}
	return inv, err
}

// openSeatPicker opens the seat picker over the combined model inventory.
// Native rows come first, then one section per configured provider, in registry
// order; each row shows the token that will be stored ("native:<id>" or
// "<provider>/<model>"). Enter appends the model as the next seat and returns
// to the editor; Esc cancels with the usual nothing-written message.
func (e *presetEditorScreen) openSeatPicker() {
	a := e.app
	if a == nil {
		return
	}
	inv, err := e.seatInventory()
	if err != nil {
		e.seatMessage(fillHubSlots(
			missingOr(e.b, "presets.editor.seats.pick_failed", presetsEditorSeatsPickFailed),
			map[string]string{"err": providers.MaskOperand(err.Error())}))
		e.rearmGuard(a)
		return
	}
	cands := seatCandidates(e.b, inv)
	if len(cands) == 0 {
		e.seatMessage(missingOr(e.b, "presets.editor.seats.pick_empty", presetsEditorSeatsPickEmpty))
		e.rearmGuard(a)
		return
	}
	p := newSeatPicker(
		missingOr(e.b, "presets.editor.seats.pick_title", presetsEditorSeatsPickTitle),
		missingOr(e.b, "presets.editor.seats.pick_hint", presetsEditorSeatsPickHint),
		cands,
		func(idx int, spec string) {
			if idx < 0 {
				a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
				e.rearmGuard(a)
				return
			}
			e.appendSeat(spec)
			e.rearmGuard(a)
		})
	p.list.SetPageSize(pageSizeFor(a.Height(), 4))
	a.ShowModal(p)
}

// seatCandidate is one row of the seat picker. A section row (ok=false) names a
// group and is not selectable; a model row carries the exact token the preset
// will store plus the model's display name as its note.
type seatCandidate struct {
	ok    bool
	label string
	note  string
	spec  string
}

// seatCandidates lays the inventory out as picker rows: the native section
// first, then one section per provider group, both in the inventory's own order
// (`agy pro models --all` renders the same grouping). Every display field is a
// registry-borne slot, so all of them mask through providers.MaskSlot, the
// single registry-slot display authority.
func seatCandidates(b *i18n.Bundle, inv *inventory.Inventory) []seatCandidate {
	if inv == nil {
		return nil
	}
	var out []seatCandidate
	for i := 0; i < len(inv.Entries); {
		group := inv.Entries[i].Group
		j := i
		for j < len(inv.Entries) && inv.Entries[j].Group == group {
			j++
		}
		header := fillHubSlots(
			missingOr(b, "presets.editor.seats.pick_group", presetsEditorSeatsPickGroup),
			map[string]string{
				"group": providers.MaskSlot(group),
				"count": strconv.Itoa(j - i),
			})
		out = append(out, seatCandidate{
			label: header,
			note:  missingOr(b, "presets.editor.seats.pick_section", presetsEditorSeatsPickSection),
		})
		for _, en := range inv.Entries[i:j] {
			out = append(out, seatCandidate{
				ok:    true,
				label: providers.MaskSlot(en.Spec),
				note:  providers.MaskSlot(en.DisplayName),
				spec:  en.Spec,
			})
		}
		i = j
	}
	return out
}

// seatPicker is the single-choice overlay over the model inventory. It is built
// on the shared stepList, so digits and arrows+Enter both work; unlike Choice it
// can carry rows that are section headers, which stay inert: activating one
// keeps the overlay open instead of closing it on a value that is not a model.
type seatPicker struct {
	title  string
	hint   string
	list   *stepList
	cands  []seatCandidate
	picked bool
	onPick func(idx int, spec string)
}

// newSeatPicker builds the overlay; onPick receives the row index (-1 on
// cancel) and the stored token of the picked model.
func newSeatPicker(title, hint string, cands []seatCandidate, onPick func(int, string)) *seatPicker {
	p := &seatPicker{title: title, hint: hint, cands: cands, onPick: onPick}
	rows := make([]Row, len(cands))
	for i, c := range cands {
		rows[i] = Row{Label: c.label, Note: c.note}
	}
	p.list = newStepList(p.pick)
	p.list.SetRows(rows)
	return p
}

func (p *seatPicker) Title() string { return p.title }

func (p *seatPicker) Body(w int) []string { return p.list.Render(w) }

func (p *seatPicker) Footer() string { return p.hint }

// Key routes one key. Esc cancels; activation of a model row closes the overlay
// through the pick callback; activation of a section row is consumed and leaves
// the overlay open, so no keystroke can pick a heading as if it were a model.
func (p *seatPicker) Key(k Key, a *App) bool {
	if k == KeyEsc {
		if !p.picked {
			p.picked = true
			if p.onPick != nil {
				p.onPick(-1, "")
			}
		}
		return false
	}
	p.list.HandleKey(k)
	return !p.picked
}

func (p *seatPicker) pick(idx int) {
	if p.picked || idx < 0 || idx >= len(p.cands) || !p.cands[idx].ok {
		return
	}
	p.picked = true
	if p.onPick != nil {
		p.onPick(idx, p.cands[idx].spec)
	}
}

// dirty reports unsaved edits across both forms and both sub-lists.
func (e *presetEditorScreen) dirty() bool {
	return e.formA.Dirty() || e.formB.Dirty() || e.hostsDirty || e.seatsDirty
}

// HandleKey runs one key with no modal live: dispatch it, then arm the
// dirty guard. Esc never arrives here (App precedence); Right requests
// close; Ctrl+S saves.
func (e *presetEditorScreen) HandleKey(k Key, a *App) {
	e.app = a
	e.dispatch(k)
	if e.dirty() && a.Modal() == nil {
		e.rearmGuard(a)
	}
}

// rearmGuard (re)opens the dirty guard. Callers running inside a modal
// key callback rely on ShowModal replacing the live modal, so the App
// stale-modal clear never removes a freshly armed one.
func (e *presetEditorScreen) rearmGuard(a *App) {
	if !e.dirty() {
		return
	}
	if e.guard == nil {
		e.guard = &presetEditorGuard{e: e}
	}
	a.ShowModal(e.guard)
}

// dispatch routes one key by zone. Ctrl+S saves; Right requests close
// (dirty editors get the discard gate); Ctrl+A on the seed row materializes
// the visible defaults into a NEW editable preset (the U8 copy gesture:
// the seed row itself is edited in place now, so Ctrl+A is a convenience,
// not the only door); Tab/Down/Up walk the four zones (A -> hosts -> B ->
// seats -> A); zone keys go to the owning component. The guard forwards
// here, so every key path runs the same routing.
func (e *presetEditorScreen) dispatch(k Key) {
	switch k {
	case KeyCtrlS:
		e.save()
		return
	case KeyRight:
		e.requestClose()
		return
	case KeyCtrlA:
		if e.seedEdit() {
			e.materializeSeed()
			return
		}
	}
	switch e.zone {
	case editorZoneA:
		e.handleZoneA(k)
	case editorZoneHosts:
		e.handleZoneHosts(k)
	case editorZoneB:
		e.handleZoneB(k)
	case editorZoneSeats:
		e.handleZoneSeats(k)
	}
}

// materializeSeed opens a new editor pre-filled with the engine defaults.
// A dirty seed first uses the same discard gate as a close: approval closes
// the seed and opens the copy in its place, so no hidden guard retains key
// scope; refusal restores the seed's guard. A clean seed keeps the original
// push behavior and remains below the copy on the screen stack.
func (e *presetEditorScreen) materializeSeed() {
	a := e.app
	if a == nil {
		return
	}
	if !e.dirty() {
		a.Push(NewPresetEditorScreen(""))
		return
	}
	a.ShowModal(NewConfirm(missingOr(e.b, "presets.editor.discard.title", presetsEditorDiscardTitle),
		[]string{missingOr(e.b, "presets.editor.discard.body", presetsEditorDiscardBody)},
		func(approved bool) {
			if !approved {
				e.rearmGuard(a)
				return
			}
			a.ShowModal(nil)
			a.Pop()
			a.Push(NewPresetEditorScreen(""))
		}))
}

// handleZoneA forwards to the zone-A form. Enter on the task field opens
// the known-task picker (D13); Enter on the last field (max_native) moves
// the focus into the host sub-list. Tab/Down advance from every field,
// including the task field. On the seed row the NAME field is fixed to the
// reserved identity: text-editing keys are swallowed there, because a typed
// different name would orphan a second preset next to the seed (rename-out
// is refused; the save also forces the name, belt and braces).
func (e *presetEditorScreen) handleZoneA(k Key) {
	before := e.formA.Cursor()
	if k == KeyEnter && before == presetZoneAFieldTask {
		e.openTaskPicker()
		return
	}
	if e.seedEdit() && before == 0 {
		if _, ok := EditRune(k); ok {
			return
		}
		if k == KeyBackspace || k == KeyDelete {
			return
		}
	}
	e.formA.HandleKey(k)
	if (k == KeyTab || k == KeyDown || k == KeyEnter) && before == e.formA.Len()-1 {
		e.zone = editorZoneHosts
	}
}

// openTaskPicker opens the shared M2 single-choice overlay over the closed
// set of compiled task names (fanout.TaskNames, derived from tasks.go, never
// a copy maintained here). Enter (or a digit) writes the chosen name back
// into the task field and keeps the field in input mode: the picker is an
// input convenience, never a replacement for the field, so free typing stays
// possible and a typed value survives an Esc cancel. The title reuses the
// field's own catalog label, so no new user-visible key is introduced; the
// row labels are the engine's canonical names, locale-neutral by
// construction. D13 (P3-6): the picker is the typo prevention; the save-time
// refusal in collect() is the backstop for typed or seeded values.
func (e *presetEditorScreen) openTaskPicker() {
	a := e.app
	if a == nil {
		return
	}
	names := fanout.TaskNames()
	ch := NewChoice(
		missingOr(e.b, "presets.editor.field.task", presetsEditorFieldTask),
		append([]string(nil), names...),
		func(idx int, _ string) {
			if idx < 0 {
				a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
				e.rearmGuard(a)
				return
			}
			e.setZoneAValue(presetZoneAFieldTask, names[idx])
			e.rearmGuard(a)
		})
	a.ShowModal(ch)
}

// setZoneAValue writes one zone-A field's value in place. The Form keeps its
// fields unexported and has no writer, so the write goes through the
// same-package fields directly (the model form instead rebuilds through
// NewForm, but its dirty() is value-based; the preset editor's dirty() is
// latch-based, so a rebuild would silently drop the discard gate). The latch
// is set by hand, the carets are re-clamped, and a write that changes
// nothing leaves the latch alone.
func (e *presetEditorScreen) setZoneAValue(i int, v string) {
	if e.formA.Value(i) == v {
		return
	}
	e.formA.fields[i].Value = v
	e.formA.dirty = true
	e.formA.clampCarets()
}

// handleZoneHosts routes keys inside the allow_hosts sub-list. On the Add
// row, printable runes (digits included) type into the inline input,
// Backspace edits it, and Enter commits; the inline input caps at
// maxLineRunes on the insertion path exactly like a form field, so a large
// paste cannot grow it without bound (seeding existing host values into
// hostVals stays uncapped, mirroring the editor/form seeding rule). On every
// other row the keys go to the shared stepList, so arrows and digits both
// navigate (digits are text only on the Add row, the mode-scoped dual
// meaning from the key map).
// Tab or Down on the last row moves into zone B; Up on the first row moves
// back to zone A.
func (e *presetEditorScreen) handleZoneHosts(k Key) {
	if e.hosts.Len() == 0 {
		return
	}
	// Tab walks the host rows and crosses into zone B on the last one (the
	// stepList itself does not consume Tab).
	if k == KeyTab {
		if e.hosts.Cursor() < e.hosts.Len()-1 {
			e.hosts.SetCursor(e.hosts.Cursor() + 1)
		} else {
			e.zone = editorZoneB
		}
		return
	}
	onAdd := e.hosts.Row(e.hosts.Cursor()).ID == hostRowAdd
	if onAdd {
		switch k {
		case KeyEnter:
			e.commitPendingHost()
			return
		case KeyBackspace:
			if e.pending != "" {
				rs := []rune(e.pending)
				e.pending = string(rs[:len(rs)-1])
				e.syncHostRows()
			}
			return
		default:
			if r, ok := EditRune(k); ok {
				if len([]rune(e.pending)) >= maxLineRunes {
					if e.app != nil {
						e.app.SetMessage(formLimitMsg)
					}
					return
				}
				e.pending += string(r)
				e.syncHostRows()
				return
			}
		}
	}
	before := e.hosts.Cursor()
	e.hosts.HandleKey(k)
	switch k {
	case KeyDown:
		if before == e.hosts.Len()-1 {
			e.zone = editorZoneB
		}
	case KeyUp:
		if before == 0 {
			e.zone = editorZoneA
		}
	}
}

// handleZoneB forwards to the zone-B form. Enter on the last field (json)
// saves, mirroring Ctrl+S; Tab or Down on the last field crosses into the seats
// sub-list (the block sits below the grid); Up on the first field moves back
// into the host sub-list.
func (e *presetEditorScreen) handleZoneB(k Key) {
	if k == KeyEnter && e.formB.Cursor() == e.formB.Len()-1 {
		e.save()
		return
	}
	before := e.formB.Cursor()
	e.formB.HandleKey(k)
	if (k == KeyTab || k == KeyDown) && before == e.formB.Len()-1 {
		e.zone = editorZoneSeats
		e.seats.SetCursor(0)
	}
	if k == KeyUp && before == 0 {
		e.zone = editorZoneHosts
	}
}

// handleZoneSeats routes keys inside the seats sub-list. Arrows and digits
// navigate through the shared stepList; Enter arms a seat row and Space toggles
// it for the action rows, while Enter on an action row runs its verb.
// Delete on a seat row is the same removal without arming, so the ✕
// gesture has a one-key spelling. Tab walks the rows and wraps to zone A on the
// last one; Down on the last row does the same, Up on the first row returns to
// the zone-B grid.
func (e *presetEditorScreen) handleZoneSeats(k Key) {
	if e.seats == nil || e.seats.Len() == 0 {
		return
	}
	if k == KeyTab {
		if e.seats.Cursor() < e.seats.Len()-1 {
			e.seats.SetCursor(e.seats.Cursor() + 1)
		} else {
			e.zone = editorZoneA
			e.formA.SetCursor(0)
		}
		return
	}
	if k == KeyDelete {
		if i, ok := e.seatRowIndex(e.seats.Cursor()); ok {
			e.removeSeatAt(i)
			return
		}
	}
	before := e.seats.Cursor()
	e.seats.HandleKey(k)
	switch k {
	case KeyDown:
		if before == e.seats.Len()-1 {
			e.zone = editorZoneA
			e.formA.SetCursor(0)
		}
	case KeyUp:
		if before == 0 {
			e.zone = editorZoneB
			e.formB.SetCursor(e.formB.Len() - 1)
		}
	}
}

// requestClose pops the editor, gating a dirty form behind the discard
// confirm. Right is the explicit close gesture; Esc reaches the same gate
// through the armed guard (see presetEditorGuard). The pop dismisses the
// editor's own modal (the transparent guard or the discard confirm): Pop
// alone would leave it live, and the ghost would swallow the next screen's
// keys until an Esc cleared it.
func (e *presetEditorScreen) requestClose() {
	a := e.app
	if a == nil {
		return
	}
	if !e.dirty() {
		a.ShowModal(nil)
		a.Pop()
		return
	}
	a.ShowModal(NewConfirm(missingOr(e.b, "presets.editor.discard.title", presetsEditorDiscardTitle),
		[]string{missingOr(e.b, "presets.editor.discard.body", presetsEditorDiscardBody)},
		func(approved bool) {
			if approved {
				a.ShowModal(nil)
				a.Pop()
				return
			}
			e.rearmGuard(a)
		}))
}

// askDiscard is the Esc-on-dirty path from the guard. y drops the edits
// and pops back to the caller (dismissing the discard confirm with it); n/Esc
// returns to the editor with field state preserved and the guard re-armed.
func (e *presetEditorScreen) askDiscard() {
	a := e.app
	if a == nil {
		return
	}
	a.ShowModal(NewConfirm(missingOr(e.b, "presets.editor.discard.title", presetsEditorDiscardTitle),
		[]string{missingOr(e.b, "presets.editor.discard.body", presetsEditorDiscardBody)},
		func(approved bool) {
			if approved {
				a.ShowModal(nil)
				a.Pop()
				return
			}
			e.rearmGuard(a)
		}))
}

// collect gathers and validates the grid into a Preset record. Parsing and the
// per-field form-shape checks live here; the shared save rules live behind the
// two fanout doors below, which delegate to validatePresetRunRules. This screen
// only renders a refusal in the editor's localized register (saveRuleMessage).
// Every error text names the field only; user operands (in particular a
// misdirected key paste) are never echoed.
func (e *presetEditorScreen) collect() (fanout.Preset, error) {
	var p fanout.Preset
	p.Name = strings.TrimSpace(e.formA.Value(0))
	if e.seedEdit() {
		// The name field is fixed on the seed row; the force guards a
		// tampered form so the record can never be written under another
		// name (rename-out would orphan a second preset).
		p.Name = fanout.BuiltinPresetName
	}
	p.Task = strings.TrimSpace(e.formA.Value(1))
	var err error
	if p.N, err = parsePresetInt(e.formA.Value(2)); err != nil {
		return p, e.numberErr(presetsEditorFieldN)
	}
	if p.TimeoutSeconds, err = parsePresetInt(e.formA.Value(3)); err != nil {
		return p, e.numberErr(presetsEditorFieldTimeout)
	}
	p.Tools = strings.TrimSpace(e.formA.Value(4))
	if !validPresetTools(p.Tools) {
		return p, strError(missingOr(e.b, "presets.editor.validation.tools", presetsEditorValidationTools))
	}
	if p.MaxTurns, err = parsePresetInt(e.formA.Value(5)); err != nil {
		return p, e.numberErr(presetsEditorFieldMaxTurns)
	}
	if p.MaxNative, err = parsePresetInt(e.formA.Value(6)); err != nil {
		return p, e.numberErr(presetsEditorFieldMaxNative)
	}
	p.SearchEndpoint = strings.TrimSpace(e.formB.Value(0))
	p.SearchKeyEnv = strings.TrimSpace(e.formB.Value(1))
	p.OutDir = strings.TrimSpace(e.formB.Value(2))
	p.Root = strings.TrimSpace(e.formB.Value(3))
	p.Seats = joinSeatSpecs(e.seatVals)
	switch strings.TrimSpace(e.formB.Value(4)) {
	case "", "false":
	case "true":
		p.JSON = true
	default:
		return p, strError(missingOr(e.b, "presets.editor.validation.json", presetsEditorValidationJSON))
	}
	// The seat rows are validated one row at a time with the run-time grammar,
	// so a malformed row is refused with the seat NUMBER it occupies (the same
	// number the list shows) instead of the whole-list parser's message. The
	// shared validator re-checks the joined value, which keeps the rule in one
	// home for every writer; this row scan exists to name the row.
	if err := e.validateSeatRows(); err != nil {
		return p, err
	}
	// The host rows are part of the record the save rules vet, so they are
	// bound before validation; the refusal leaves hostVals untouched, so the
	// offending rows stay visible for removal.
	p.AllowHosts = append([]string(nil), e.hostVals...)
	// Save-time credential guard on the free-text slots (round-6 fix): the
	// shared classifier from the model form (secretValuePrefixes +
	// credentialTokenShaped, screen_model_form.go) refuses a credential-shaped
	// value in any free-text field, so a misdirected key paste persists into
	// fanout-presets.json only after an explicit override nowhere exists; the
	// refusal names the field label and never echoes the value. Legitimate
	// values (tasks, hostnames, seat/model lists, paths) carry separators or
	// fail the token shape and save normally. tools is a closed whitelist, so
	// it is not rechecked here; the credential shape is a display-layer
	// concern (it is what the field labels render), so it stays in this
	// screen rather than moving into the store validator.
	for _, f := range []struct {
		label string
		val   string
	}{
		{presetsEditorFieldName, p.Name},
		{presetsEditorFieldTask, p.Task},
		{presetsEditorFieldSearchEndpoint, p.SearchEndpoint},
		{presetsEditorFieldOutDir, p.OutDir},
		{presetsEditorFieldRoot, p.Root},
		{presetsEditorFieldSeats, p.Seats},
	} {
		if looksSecretValue(f.val) {
			return p, strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.secret", presetsEditorValidationSecret),
				map[string]string{"field": f.label}))
		}
	}
	for _, h := range p.AllowHosts {
		if looksSecretValue(h) {
			return p, strError(fillHubSlots(
				missingOr(e.b, "presets.editor.validation.secret", presetsEditorValidationSecret),
				map[string]string{"field": presetsEditorFieldAllowHosts}))
		}
	}
	// Save rules: one shared rule core behind two doors (contract A5b).
	// The seed row's save is the ONLY
	// caller of ValidateSeedPresetForSave (the one door that accepts the
	// reserved name); every other writer keeps ValidatePresetForSave, which
	// refuses it. A refusal is rendered as the editor's named message for
	// that rule (locale key + const fallback), save() puts it on the status
	// line and returns, so the editor stays open with the offending value
	// still in the form.
	if e.seedEdit() {
		if err := fanout.ValidateSeedPresetForSave(p); err != nil {
			return p, strError(e.saveRuleMessage(err))
		}
		return p, nil
	}
	if err := fanout.ValidatePresetForSave(p); err != nil {
		return p, strError(e.saveRuleMessage(err))
	}
	return p, nil
}

// validateSeatRows parses every seat row with the run-time grammar and refuses
// a malformed one by SEAT NUMBER. Each row is one token, so the parser's own
// "seat 1" is always the row's own number; the message names the number and
// never echoes the row, because a misdirected paste lands here too.
func (e *presetEditorScreen) validateSeatRows() error {
	for i, spec := range e.seatVals {
		if _, err := fanout.ParseSeatSpecs(spec); err != nil {
			return strError(fillHubSlots(
				missingOr(e.b, "presets.editor.seats.invalid", presetsEditorSeatsInvalid),
				map[string]string{"n": strconv.Itoa(i + 1)}))
		}
	}
	return nil
}

// seatLineupWarnings reports the soft problems a saved lineup can carry. They
// never block the save: an explicit lineup is the user's decision, and a model
// can be legitimately absent from today's registry (a provider key removed for
// the day) while the preset stays the record of intent.
//
// Two warnings need nothing but the rows themselves and always run:
//   - two identical tokens (a duplicated seat adds no second opinion).
//
// Two warnings need the model inventory, so they run only when a picker open has
// already read it in this session (no extra subprocess at save time):
//   - a token that matches no inventory row (stale lineup);
//   - two tokens that resolve to the same model family (cross-verification
//     value is lost even when the ids differ).
//
// The missing-row caution is KIND-AWARE, because the two seat kinds do not share
// a failure mode (fix-list FL-6, verified): a provider token the registry cannot
// resolve is dropped before the run with a warning and counted in
// skipped_seats, while a native token is NEVER drift-checked (the engine appends
// a native target unconditionally), so an unknown native id starts and fails
// inside its own subprocess. One sentence for both promised a skip that native
// seats never get.
//
// Absence of an inventory row only counts as evidence when the surface that would
// have carried the row was actually readable:
//   - the registry was read when the inventory carries any row OR recorded a
//     native error (the settings seam only builds the native fetcher once
//     providers.Load produced a registry, so either signal means the registry was
//     read). A provider miss is evidence only then.
//   - the native catalog was read when the registry was read and the fetch did not
//     fail (a successful fetch always yields rows). A native miss is evidence only
//     then; with inv.NativeErr set, zero native rows exist and a perfectly valid
//     id would look "missing".
//   - when neither was read (an unreadable or missing providers.json: no rows and
//     no native error), the whole caution stays quiet. The provider side cannot be
//     told apart from the native side in that shape, so accusing either would mean
//     guessing.
//
// The two duplicate families carry DISTINCT wording on purpose: "the same
// model" is a wasted seat the user can fix by deleting a row, while "the same
// model family" is a weaker second opinion the user fixes by swapping one seat
// for another family. One message for both taught the wrong fix (fix-list
// FL-8(b)).
func (e *presetEditorScreen) seatLineupWarnings() []string {
	var out []string
	seen := map[string]int{}
	for i, spec := range e.seatVals {
		key := strings.ToLower(strings.TrimSpace(spec))
		if key == "" {
			continue
		}
		if first, ok := seen[key]; ok {
			out = append(out, fillHubSlots(
				missingOr(e.b, "presets.editor.seats.warn_duplicate", presetsEditorSeatsWarnDuplicate),
				map[string]string{"n": strconv.Itoa(first + 1), "m": strconv.Itoa(i + 1)}))
			continue
		}
		seen[key] = i
	}
	if e.seatInv == nil {
		return out
	}
	bySpec := make(map[string]*inventory.Entry, len(e.seatInv.Entries))
	for i := range e.seatInv.Entries {
		bySpec[strings.ToLower(e.seatInv.Entries[i].Spec)] = &e.seatInv.Entries[i]
	}
	// What the inventory is allowed to accuse (see the doc block). registryRead is
	// true exactly when providers.Load produced a registry: the settings seam then
	// builds the native fetcher too, and a successful fetch always yields native
	// rows (an empty catalog is recorded as NativeErr), so "rows exist or a native
	// error was recorded" and "the registry was read" are the same state. The
	// native catalog counts as read only when that registry was read AND the fetch
	// did not fail; otherwise zero native rows exist and a valid id would look
	// missing.
	registryRead := len(e.seatInv.Entries) > 0 || e.seatInv.NativeErr != nil
	nativeCatalogRead := registryRead && e.seatInv.NativeErr == nil
	familySeat := map[string]int{}
	for i, spec := range e.seatVals {
		key := strings.ToLower(strings.TrimSpace(spec))
		if key == "" {
			continue
		}
		en, ok := bySpec[key]
		if !ok {
			native := seatTokenIsNative(spec)
			if native && !nativeCatalogRead {
				continue
			}
			if !native && !registryRead {
				continue
			}
			if native {
				out = append(out, fillHubSlots(
					missingOr(e.b, "presets.editor.seats.warn_missing_native", presetsEditorSeatsWarnMissingNative),
					map[string]string{"n": strconv.Itoa(i + 1)}))
			} else {
				// The provider caution is rung-aware (R2-2): the engine skips a
				// missing provider seat only on the standing default-key rung; a
				// named-preset run refuses to start. One sentence for both would
				// promise a skip the named rung never performs.
				key, fallback := "presets.editor.seats.warn_missing", presetsEditorSeatsWarnMissing
				if !e.seatLineupRungIsStanding() {
					key, fallback = "presets.editor.seats.warn_missing_named", presetsEditorSeatsWarnMissingNamed
				}
				out = append(out, fillHubSlots(
					missingOr(e.b, key, fallback),
					map[string]string{"n": strconv.Itoa(i + 1), "spec": seatSpecOperand(spec)}))
			}
			continue
		}
		if en.Family == "" {
			continue
		}
		fam := strings.ToLower(en.Family)
		if first, ok := familySeat[fam]; ok {
			out = append(out, fillHubSlots(
				missingOr(e.b, "presets.editor.seats.warn_family", presetsEditorSeatsWarnFamily),
				map[string]string{"n": strconv.Itoa(first + 1), "m": strconv.Itoa(i + 1)}))
			continue
		}
		familySeat[fam] = i
	}
	return out
}

// seatLineupRungIsStanding reports whether the preset being edited is the store's
// default_preset row, the ONLY rung where fanout tolerates a missing provider seat
// (skip + degraded) instead of refusing to run (FL-3 narrowed the tolerance to
// that rung; R2-2 is this warning catching up). e.name is the on-disk identity of
// the row being edited and e.defaultName is the store key as loaded for this save,
// so a rename or a pointer moved by another surface flips the answer: a new preset
// (no name on disk) and every non-default row take the hard-error wording.
func (e *presetEditorScreen) seatLineupRungIsStanding() bool {
	name := strings.TrimSpace(e.name)
	if name == "" {
		return false
	}
	return strings.TrimSpace(e.defaultName) == name
}

// saveRuleMessage maps a fanout.ValidatePresetForSave refusal onto the
// editor's own message for that rule. This table is PRESENTATION only: it
// decides the wording (and the locale key) for a rule the shared validator
// already decided, so the rule set lives in exactly one place. A refusal the
// table does not know (a rule added to the validator later) still reaches the
// user as the validator's own text, masked, instead of vanishing.
func (e *presetEditorScreen) saveRuleMessage(err error) string {
	switch {
	case errors.Is(err, fanout.ErrPresetNameRequired):
		return missingOr(e.b, "presets.editor.validation.name", presetsEditorValidationName)
	case errors.Is(err, fanout.ErrPresetNameReserved):
		// The refusal names the rule only; the reserved operand is never
		// echoed (contract A7).
		return missingOr(e.b, "presets.editor.validation.reserved", presetsEditorValidationReserved)
	case errors.Is(err, fanout.ErrPresetSearchKeyEnv):
		return missingOr(e.b, "presets.editor.validation.key_env", presetsEditorValidationKeyEnv)
	case errors.Is(err, fanout.ErrPresetUnknownTask):
		return missingOr(e.b, "presets.editor.validation.task", presetsEditorValidationTask)
	case errors.Is(err, fanout.ErrPresetAllowHosts):
		return missingOr(e.b, "presets.editor.validation.host", presetsEditorValidationHost)
	case errors.Is(err, fanout.ErrPresetSeats):
		return missingOr(e.b, "presets.editor.validation.seats", presetsEditorValidationSeats)
	}
	return providers.MaskOperand(err.Error())
}

// numberErr composes the number-validation message for one field.
func (e *presetEditorScreen) numberErr(field string) error {
	return strError(fillHubSlots(
		missingOr(e.b, "presets.editor.validation.number", presetsEditorValidationNumber),
		map[string]string{"field": field}))
}

// save validates the grid and writes ONLY the preset file through the store
// (atomic 0600). An overwrite of an existing preset name goes through a
// plain confirm (the preset file is not providers.json, so no M4 gate). On
// success the editor re-anchors to the saved name and marks itself clean.
func (e *presetEditorScreen) save() {
	a := e.app
	if a == nil {
		return
	}
	p, err := e.collect()
	if err != nil {
		a.SetMessage(e.b.T("common.error") + ": " + err.Error())
		return
	}
	// Fresh load through the latch: if the file turned corrupt again since
	// the last observation, a NEW episode opens and the acknowledgment below
	// re-arms the gate (one ack covers one episode, never the whole session).
	e.dropNotices = e.dropNotices[:0]
	e.dropMsgSent = false
	before := e.corruptLatch.corruptEvents
	pf, err := e.store.LoadPresets()
	loadErr := ""
	if err != nil {
		loadErr = err.Error()
	}
	e.corruptLatch.reconcile(e.corruptLatch.corruptEvents > before, loadErr)
	if err != nil {
		a.SetMessage(e.b.Tf("err.load_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
		return
	}
	if pf != nil {
		for _, legacy := range pf.LegacyModels {
			e.dropNotices = append(e.dropNotices, presetLegacyModelsNotice(e.b, legacy))
		}
	}
	// Refuse the first save after a corrupt load until the user acknowledges
	// it: the in-memory store is the empty fallback, so saving now would
	// silently overwrite the corrupt file and destroy any recoverable
	// presets. The confirm names the on-disk path so it can be backed up.
	if e.corruptWarn && !e.corruptAcked {
		path := providers.MaskOperand(presetFilePath(e.store))
		a.ShowModal(NewConfirm(missingOr(e.b, "presets.corrupt.title", presetsCorruptTitle),
			[]string{fillHubSlots(missingOr(e.b, "presets.corrupt.body", presetsCorruptBody),
				map[string]string{"path": path})},
			func(approved bool) {
				if !approved {
					a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
					return
				}
				e.corruptAcked = true
				e.save()
			}))
		return
	}
	existing := -1
	for i := range pf.Presets {
		if pf.Presets[i].Name == p.Name {
			existing = i
			break
		}
	}
	if existing >= 0 && p.Name != e.name {
		masked := presetNameOperand(p.Name)
		a.ShowModal(NewConfirm(missingOr(e.b, "presets.overwrite.title", presetsOverwriteTitle),
			[]string{fillHubSlots(missingOr(e.b, "presets.overwrite.body", presetsOverwriteBody),
				map[string]string{"name": masked})},
			func(approved bool) {
				if !approved {
					a.SetMessage(missingOr(e.b, "presets.msg.cancelled", presetsMsgCancelled))
					return
				}
				e.writePreset(a, pf, p, existing)
			}))
		return
	}
	e.writePreset(a, pf, p, existing)
}

// writePreset upserts the record and persists through the store. A rename
// (e.name != p.Name) drops BOTH the old name and the target name before the
// append: the target was confirmed through the overwrite modal in save(), so
// the file must end with exactly one row for p.Name, never a duplicate. pf
// is the freshly loaded file; existing is the index of a same-named preset
// or -1.
func (e *presetEditorScreen) writePreset(a *App, pf *fanout.PresetsFile, p fanout.Preset, existing int) {
	p.SeatsSHA256 = fanout.SeatsHash(p.Seats)
	if e.name != "" && e.name != p.Name {
		keep := pf.Presets[:0]
		for i := range pf.Presets {
			n := pf.Presets[i].Name
			if n != e.name && n != p.Name {
				keep = append(keep, pf.Presets[i])
			}
		}
		pf.Presets = keep
		existing = -1
	}
	if existing >= 0 {
		pf.Presets[existing] = p
	} else {
		pf.Presets = append(pf.Presets, p)
	}
	if err := e.store.SavePresets(pf); err != nil {
		a.SetMessage(e.b.Tf("err.write_failed", map[string]string{"err": providers.MaskOperand(err.Error())}))
		return
	}
	// The file just written is healthy by construction (SavePresets
	// validated and rewrote it), so close any corrupt episode: the stale
	// notice must not outlive the save that replaced the corrupt file.
	e.corruptLatch.reconcile(false, "")
	cp := p
	cp.AllowHosts = append([]string(nil), p.AllowHosts...)
	e.preset = &cp
	e.name = p.Name
	e.defaultName = pf.DefaultPreset // rename may have stranded the old default key
	e.formA.MarkClean()
	e.formB.MarkClean()
	e.hostsDirty = false
	e.seatsDirty = false
	msg := fillHubSlots(missingOr(e.b, "presets.editor.saved", presetsEditorSaved),
		map[string]string{"name": presetNameOperand(p.Name)})
	// Soft lineup warnings ride the save confirmation: the save already
	// happened (a pinned lineup is the user's call), and the one line the user
	// is looking at after Ctrl+S is the only place a non-blocking caution can
	// land without a second modal.
	if warns := e.seatLineupWarnings(); len(warns) > 0 {
		msg += " · " + missingOr(e.b, "presets.editor.seats.warn_prefix", presetsEditorSeatsWarnPrefix) +
			strings.Join(warns, " · ")
	}
	a.SetMessage(msg)
}

// sources returns the rung label per field for the source column. It is
// computed from the ON-DISK ladder (the saved preset, the providers.json
// defaults block, and an empty flag set), mirroring buildRunConfig's
// resolveLadder; unsaved form edits do not move the column. The preset rung
// carries the edited row's selection role (D11) so the column names HOW the
// row is picked up, never implying it is in effect. The seed row labels
// every engine-supplied (unset) field "built-in" and keeps the max_native
// state.json rung, which genuinely supplies that value; fields the seed
// record actually pins label the preset rung like any other record.
func (e *presetEditorScreen) sources() map[string]string {
	if e.seedEdit() {
		src := resolvePresetSources(presetLadderInputs{
			set:     map[string]bool{},
			preset:  e.preset,
			def:     e.def,
			rowRole: e.presetRowRole(),
		})
		// Unset fields are engine-supplied on the seed row: label them
		// "built-in" (the source column keeps the honest rung name).
		for k, v := range src {
			if v == "engine" {
				src[k] = "builtin"
			}
		}
		// Knobs with no engine source stay "(none)", as the old built-in
		// view rendered them.
		for _, k := range []string{"allow_hosts", "search_endpoint", "search_key_env"} {
			if _, ok := src[k]; !ok {
				src[k] = ""
			}
		}
		return src
	}
	return resolvePresetSources(presetLadderInputs{
		set:     map[string]bool{},
		preset:  e.preset,
		def:     e.def,
		rowRole: e.presetRowRole(),
	})
}

// presetRowRole names HOW the edited row is selected (the store's default
// key, the env var, or a plain saved row): the D11 source-column input.
func (e *presetEditorScreen) presetRowRole() string {
	if e.preset == nil {
		return presetRowSaved
	}
	if envName := strings.TrimSpace(os.Getenv(fanout.EnvFanoutPreset)); envName != "" && envName == e.preset.Name {
		return presetRowNamed // explicit selection beats the default key
	}
	if e.defaultName != "" && e.defaultName == e.preset.Name {
		return presetRowDefault
	}
	return presetRowSaved
}

// sourceLabel maps a rung value onto its catalog label.
func (e *presetEditorScreen) sourceLabel(rung string) string {
	b := e.b
	switch rung {
	case "flag":
		return b.T("presets.editor.source.cli")
	case "preset_default":
		return missingOr(b, "presets.editor.source.preset_default", presetsEditorSourcePresetDefault)
	case "preset_named":
		return missingOr(b, "presets.editor.source.preset_named", presetsEditorSourcePresetNamed)
	case "preset_saved":
		return missingOr(b, "presets.editor.source.preset_saved", presetsEditorSourcePresetSaved)
	case "defaults":
		return b.T("presets.editor.source.registry")
	case "state":
		return missingOr(b, "presets.editor.source.state", presetsEditorSourceState)
	case "builtin":
		return missingOr(b, "presets.editor.source.builtin", presetsEditorSourceBuiltin)
	case "engine":
		return b.T("presets.editor.source.engine")
	default:
		return missingOr(b, "presets.editor.source.none", presetsEditorSourceNone)
	}
}

// presetSourceKeys is the field order of the source column.
var presetSourceKeys = []string{
	"name", "task", "n", "timeout_seconds", "tools", "max_turns", "max_native",
	"allow_hosts", "search_endpoint", "search_key_env", "out_dir", "root",
	"seats", "json",
}

// sourceKeyA maps zone-A field indexes onto their source keys.
var sourceKeyA = []string{"name", "task", "n", "timeout_seconds", "tools", "max_turns", "max_native"}

// sourceKeyB maps zone-B field indexes onto their source keys. seats is not
// here because the seats rung belongs to the structured sub-list below the
// grid (renderSeats), not to a field row.
var sourceKeyB = []string{"search_endpoint", "search_key_env", "out_dir", "root", "json"}

// armedFieldKey maps the focus state onto the source key of the field the
// explain line describes: the focused grid row in zones A and B, the whole
// allow_hosts block in the host zone (the sub-list rows are entries of one
// knob, not knobs), and the seats lineup in the seat zone.
func (e *presetEditorScreen) armedFieldKey() string {
	switch e.zone {
	case editorZoneHosts:
		return "allow_hosts"
	case editorZoneB:
		return sourceKeyB[e.formB.Cursor()]
	case editorZoneSeats:
		return "seats"
	default:
		return sourceKeyA[e.formA.Cursor()]
	}
}

// explainLine resolves the one-line plain-words description of the armed
// field (U8): catalog key presets.editor.explain.<field> with the English
// const fallback. It follows the focus through every zone on every row,
// the seed row included.
func (e *presetEditorScreen) explainLine() string {
	key := e.armedFieldKey()
	var fallback string
	switch key {
	case "name":
		fallback = presetsEditorExplainName
	case "task":
		fallback = presetsEditorExplainTask
	case "n":
		fallback = presetsEditorExplainN
	case "timeout_seconds":
		fallback = presetsEditorExplainTimeout
	case "tools":
		fallback = presetsEditorExplainTools
	case "max_turns":
		fallback = presetsEditorExplainMaxTurns
	case "max_native":
		fallback = presetsEditorExplainMaxNative
	case "allow_hosts":
		fallback = presetsEditorExplainAllowHosts
	case "search_endpoint":
		fallback = presetsEditorExplainSearchEndpoint
	case "search_key_env":
		fallback = presetsEditorExplainSearchKeyEnv
	case "out_dir":
		fallback = presetsEditorExplainOutDir
	case "root":
		fallback = presetsEditorExplainRoot
	case "seats":
		fallback = presetsEditorExplainSeats
	case "json":
		fallback = presetsEditorExplainJSON
	}
	return missingOr(e.b, "presets.editor.explain."+key, fallback)
}

// isMark reports whether r is a combining mark (Mn, Mc, or Me). A mark must
// never start a row: detached from its base consonant it reattaches to the
// wrong glyph (r12 F-03; Mc matras such as Devanagari ि are width 1, so the
// zero-width tables alone do not protect base|mark joints).
func isMark(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r)
}

// markSafeCut adjusts a mid-run cut index n so the boundary never splits a
// base+combining-mark pair: if runes[n] is a mark, the cut backs up before
// the pair's base (the whole cluster moves to the continuation row); when the
// cluster starts the string the cut extends over it instead, so the row
// cannot blank. Trailing zero-width marks orphaned by the back-up are
// dropped. n must already satisfy the width budget for runes[:n].
func markSafeCut(runes []rune, n int) int {
	if n > 0 && n < len(runes) && isMark(runes[n]) {
		j := n - 1
		for j > 0 && isMark(runes[j]) {
			j--
		}
		if j > 0 {
			n = j // continuation starts at the base rune
		} else {
			for n < len(runes) && isMark(runes[n]) {
				n++ // leading cluster stays whole
			}
		}
	}
	for n > 0 && cellwidth.Rune(runes[n-1]) == 0 {
		n--
	}
	return n
}

// cutCells shortens s to at most cells display cells with a first-N-rune
// width cut that is mark-safe (r12 F-03): the boundary never splits a
// base+combining-mark pair and never orphans a zero-width mark. cellwidth.
// Truncate is not used here: it cuts whole clusters and keeps a "…" fallback
// when nothing fits (the P9 contract, RTL included), while this wrap path
// needs a plain degraded cut that may open mid-cluster so a word can continue
// on the next row, with mark-pair protection as the one safety net for every
// script.
func cutCells(s string, cells int) string {
	if cellwidth.String(s) <= cells {
		return s
	}
	runes := []rune(s)
	w, n := 0, 0
	for n < len(runes) && w+cellwidth.Rune(runes[n]) <= cells {
		w += cellwidth.Rune(runes[n])
		n++
	}
	n = markSafeCut(runes, n)
	return string(runes[:n])
}

// chunkWord splits one over-long word (an unspaced CJK sentence is a single
// word to strings.Fields) into row-sized pieces of at most w display cells
// each. Cuts are cellwidth-safe and never split a base+combining-mark pair
// or orphan a zero-width mark (r12 F-03).
func chunkWord(word string, w int) []string {
	var chunks []string
	runes := []rune(word)
	for len(runes) > 0 {
		cw, n := 0, 0
		for n < len(runes) && cw+cellwidth.Rune(runes[n]) <= w {
			cw += cellwidth.Rune(runes[n])
			n++
		}
		if n == 0 {
			n = 1 // one rune wider than the row still occupies a row
		}
		n = markSafeCut(runes, n)
		if n == 0 {
			n = 1
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

// unspacedRun reports whether tok has no ASCII letters or digits: CJK and RTL
// sentences read naturally when wrapped mid-run, unlike Latin words.
func unspacedRun(tok string) bool {
	for _, r := range tok {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// wrapExplainLines breaks s into at most maxRows rows of at most w display
// cells each (the r11 F-01 two-slot explain row), breaking on word boundaries
// where they exist. Unspaced runs longer than a row are chunk-split so CJK
// and RTL sentences still wrap instead of truncating. When the text still
// does not fit, the last row carries as much as fits plus a trailing
// continuation ellipsis (…); rows are never left blank, including RTL
// strings (see cutCells).
// wrapToken is one wrap unit: a word, or one chunk of an over-long unspaced
// word. spc records whether a space separates it from the previous token
// (false for the chunks after the first: they continue the same word).
type wrapToken struct {
	tok string
	spc bool
}

func wrapExplainLines(s string, w, maxRows int) []string {
	if maxRows <= 0 || w <= 0 {
		return nil
	}
	var words []wrapToken
	for _, f := range strings.Fields(s) {
		if cw := cellwidth.String(f); cw > w {
			// An unspaced run longer than a row (CJK/RTL sentences are one
			// word to Fields) is chunk-split; only the first chunk keeps the
			// space that separated the original field.
			for i, chunk := range chunkWord(f, w) {
				words = append(words, wrapToken{chunk, i == 0})
			}
		} else {
			words = append(words, wrapToken{f, true})
		}
	}
	if len(words) == 0 {
		return nil
	}
	rows := make([]string, 0, maxRows)
	cur, curW := "", 0
	for len(words) > 0 {
		word := words[0]
		words = words[1:]
		ww := cellwidth.String(word.tok)
		sep := 0
		if cur != "" && word.spc {
			sep = 1
		}
		switch {
		case cur == "" && ww > w:
			// One word wider than a whole row: hard-cut it with an ellipsis
			// and continue with the next word on a fresh row.
			rows = append(rows, cutCells(word.tok, w-1)+"…")
			if len(rows) == maxRows {
				return rows
			}
			continue
		case cur == "":
			cur, curW = word.tok, ww
		case curW+sep+ww <= w:
			if sep == 1 {
				cur += " "
			}
			cur += word.tok
			curW += sep + ww
		default:
			if room := w - curW; unspacedRun(word.tok) && room >= 1 {
				// An unspaced CJK/RTL run reads naturally mid-cut: fill the
				// remaining cells of this row and continue the rest on the
				// next row, so bin slack never forces a needless ellipsis.
				cut := cutCells(word.tok, room)
				if cut != "" {
					cur += cut
					curW += cellwidth.String(cut)
					rest := string([]rune(word.tok)[len([]rune(cut)):])
					if rest != "" {
						words = append([]wrapToken{{rest, false}}, words...)
					}
					continue
				}
			}
			// The word does not fit on the current row: close it and retry
			// the word on a fresh row.
			rows = append(rows, cur)
			cur, curW = "", 0
			words = append([]wrapToken{word}, words...)
			if len(rows) == maxRows {
				// The row budget is spent: merge the remainder onto the last
				// row, keeping what fits and marking the cut with an
				// ellipsis.
				rest := ""
				for _, tkn := range words {
					if rest != "" && tkn.spc {
						rest += " "
					}
					rest += tkn.tok
				}
				last := rows[maxRows-1]
				if rest != "" {
					last += " " + rest
				}
				rows[maxRows-1] = cutCells(last, w-1) + "…"
				return rows
			}
		}
	}
	if cur != "" {
		rows = append(rows, cur)
	}
	return rows
}

// Draw renders the two form grids around the host sub-list, the seats sub-list
// below the zone-B grid, each row carrying its rung label, plus the validation
// message and the hint. All cell math goes through cellwidth.
func (e *presetEditorScreen) Draw(a *App) []string {
	e.app = a
	w := a.Width()
	src := e.sources()
	labelW := 0
	for _, k := range presetSourceKeys {
		if cw := cellwidth.String(k); cw > labelW {
			labelW = cw
		}
	}
	var lines []string
	if e.notFound != "" {
		lines = append(lines, cellwidth.Truncate(
			e.b.T("common.error")+": preset "+presetNameOperand(e.notFound)+" not found", w))
	}
	// The latest load's drop notices surface once, with any additional count
	// ahead of the attacker-length record name so frame truncation cannot hide it.
	if len(e.dropNotices) > 0 && !e.dropMsgSent && a.Message() == "" {
		e.dropMsgSent = true
		msg := e.dropNotices[0]
		if len(e.dropNotices) > 1 {
			msg = fmt.Sprintf("(+%d more) %s", len(e.dropNotices)-1, msg)
		}
		a.SetMessage(msg)
	}
	if e.corruptWarn {
		warn := corruptPresetWarning(e.b, presetFilePath(e.store))
		if !e.corruptMsgSent && a.Message() == "" {
			e.corruptMsgSent = true
			a.SetMessage(warn)
		}
		// The warning renders once per frame. The message line is the copy the
		// composer's tail cut keeps longest, so while it carries this exact
		// warning the body row is redundant and its row goes to the sub-list
		// windows instead: an 80x24 frame over a corrupt file has exactly one
		// row to give, and the alternative is losing the hint. Any other
		// message brings the body row back (corruptBodyRow).
		if a.Message() != warn {
			lines = append(lines, cellwidth.Truncate(warn, w))
		}
	}
	lines = append(lines, e.renderForm(e.formA, e.zone == editorZoneA, sourceKeyA, src, labelW, w)...)
	lines = append(lines, e.renderHosts(a, src, labelW, w)...)
	lines = append(lines, e.renderForm(e.formB, e.zone == editorZoneB, sourceKeyB, src, labelW, w)...)
	lines = append(lines, e.renderSeats(a, src, labelW, w)...)
	if msg := e.formA.Message(); msg != "" {
		lines = append(lines, cellwidth.Truncate(msg, w))
	}
	if msg := e.formB.Message(); msg != "" {
		lines = append(lines, cellwidth.Truncate(msg, w))
	}
	hint := missingOr(e.b, "presets.editor.hint", presetsEditorHint)
	if a.Modal() != e.guard {
		// The two sub-list windows are the only blocks that shrink, and
		// stepList never takes either below one row. frameBudget reports how
		// many rows the explain block above the hint may occupy; it carries
		// the armed field's explain sentence (U8) word-wrapped over up to two
		// rows (r11: the one-line Truncate this replaced used to hide every
		// clause past 80 cells, and blanked an over-wide RTL string entirely).
		// The explain block yields before the hint, so the hint (the only
		// line naming Ctrl+S) stays on the frame.
		_, explainRows := e.frameBudget(a)
		rows := wrapExplainLines(e.explainLine(), w, explainRows)
		for len(rows) < explainRows {
			// The block keeps its full budget however short the sentence is,
			// so the frame geometry stays stable as the focus moves.
			rows = append(rows, "")
		}
		lines = append(lines, rows...)
		lines = append(lines, cellwidth.Truncate(hint, w))
	}
	return lines
}

// renderForm renders one zone's field grid with the source column appended
// to every row. Field values render verbatim; they are the user's own
// configuration data. The seed row is the one exception: an UNSET field
// renders its effective preview (seedDisplayValue), taking a providers.json
// defaults value when that rung supplies one and otherwise the engine literal.
// The stored record stays empty (contract A2), and the source column names the
// same rung as the preview. The search_key_env slot is the
// masking exception: an in-progress value is echoed masked through
// providers.MaskSlot (SH-4 + the r4 name-slot close-out), so a
// credential-shaped paste never renders raw in a frame even before the
// save-time validation rejects it. MaskOperand was the earlier authority
// here and is the IDENTITY on the underscore-head class
// ("sk_live_<lowercase/digits>" rode raw); MaskSlot masks that class and
// strips the Cc/Cf runes, while a benign env NAME ("ACME_API_KEY") is
// byte-identical under both. The form keeps editing the raw value.
func (e *presetEditorScreen) renderForm(f *Form, active bool, keys []string, src map[string]string, labelW, w int) []string {
	out := make([]string, 0, f.Len())
	for i := 0; i < f.Len(); i++ {
		marker := "  "
		if active && f.Cursor() == i {
			marker = "▶ "
		}
		label := cellwidth.PadRight(f.Field(i).Label, labelW)
		val := f.Value(i)
		if keys[i] == "search_key_env" {
			val = providers.MaskSlot(val)
		} else if e.seedEdit() && val == "" {
			// The effective value is a display fallback only; the form
			// stays empty so the save writes no untouched field.
			val = e.seedDisplayValue(keys[i])
		}
		line := marker + label + " : " + val
		if rung, ok := src[keys[i]]; ok && keys[i] != "name" {
			line += "  " + e.sourceLabel(rung)
		}
		out = append(out, cellwidth.Truncate(line, w))
	}
	return out
}

// renderHosts renders the allow_hosts sub-list block: the field header row
// (with its rung) plus the numbered host/add/remove rows, in a window sized by
// the frame height.
func (e *presetEditorScreen) renderHosts(a *App, src map[string]string, labelW, w int) []string {
	marker := "  "
	if e.zone == editorZoneHosts {
		marker = "▶ "
	}
	line := marker + cellwidth.PadRight(presetsEditorFieldAllowHosts, labelW) + " : "
	if rung := src["allow_hosts"]; rung != "" {
		line += e.sourceLabel(rung)
	} else {
		line += missingOr(e.b, "presets.editor.source.none", presetsEditorSourceNone)
	}
	lines := []string{cellwidth.Truncate(line, w)}
	e.hosts.SetPageSize(e.hostPageSize(a))
	lines = append(lines, e.hosts.Render(w)...)
	return lines
}

// renderSeats renders the seats sub-list block: the field header row (with its
// rung, and the auto-select hint while the list is empty) plus the numbered
// seat rows and the four action rows, in a window sized by the frame height. An
// empty list is a real state, not a missing value: it means the engine picks
// the lineup, so the header says so instead of leaving a blank cell.
func (e *presetEditorScreen) renderSeats(a *App, src map[string]string, labelW, w int) []string {
	marker := "  "
	if e.zone == editorZoneSeats {
		marker = "▶ "
	}
	line := marker + cellwidth.PadRight(presetsEditorFieldSeats, labelW) + " : "
	if rung := src["seats"]; rung != "" {
		line += e.sourceLabel(rung)
	} else {
		line += missingOr(e.b, "presets.editor.source.none", presetsEditorSourceNone)
	}
	if len(e.seatVals) == 0 {
		line += "  " + missingOr(e.b, "presets.editor.seats.auto", presetsEditorSeatsAuto)
	}
	lines := []string{cellwidth.Truncate(line, w)}
	e.seats.SetPageSize(e.seatPageSize(a))
	lines = append(lines, e.seats.Render(w)...)
	return lines
}

// editorChrome counts the rows one composed frame spends outside the two
// sub-list windows and outside the decorative blank: the fixed chrome plus
// every one-off row this frame actually renders, plus the app message block
// (the composer spends a separator and a line on it, which is what the +2
// below accounts for).
func (e *presetEditorScreen) editorChrome(a *App) int {
	chrome := presetsEditorChrome
	if e.notFound != "" {
		chrome++
	}
	if e.corruptBodyRow(a) {
		chrome++
	}
	if e.formA.Message() != "" {
		chrome++
	}
	if e.formB.Message() != "" {
		chrome++
	}
	if a.Message() != "" {
		chrome += 2
	}
	return chrome
}

// corruptBodyRow reports whether this frame draws the corrupt-file warning as a
// body row. It does not while the app message line already carries that exact
// warning: one copy is enough, and the row is worth more to the sub-list
// windows on a small frame (Draw renders the message first, so both the
// decision and the chrome count read the same state).
func (e *presetEditorScreen) corruptBodyRow(a *App) bool {
	if !e.corruptWarn {
		return false
	}
	return a.Message() != corruptPresetWarning(e.b, presetFilePath(e.store))
}

// frameBudget splits the rows the frame leaves over the chrome between the two
// sub-list windows, and reports how many rows the variable explain block above
// the hint (the armed field's explain sentence since U8) may occupy: two when
// the budget allows, one on a tighter frame, zero when only the two window
// minimum fits. The give-way order the switch below implements: window surplus
// rows first (the windows shrink toward their 1/1 minimum while explain keeps
// two rows), then the explain block 2/1/0, and on a frame too small for even
// that, the composer's tail cut takes the hint instead - the honest outcome
// for a frame that small, where the hint line that names Ctrl+S is the last
// row the budget can no longer guarantee.
func (e *presetEditorScreen) frameBudget(a *App) (windows int, explainRows int) {
	avail := a.Height() - e.editorChrome(a)
	switch {
	case avail >= 4:
		return avail - 2, 2
	case avail == 3:
		return 2, 1
	default:
		if avail < 2 {
			avail = 2
		}
		return avail, 0
	}
}

// subListPages splits the window rows between the two sub-lists. The seats list
// takes half, hosts take what is left, and each window shrinks to what its list
// actually needs (so an empty seats list hands its share back to the hosts).
// Every window keeps at least one row: stepList pages, and a paged list is still
// reachable, while a zero-row window would hide the block entirely.
//
// The split rule exists because one shared list used to own the whole budget;
// with two lists a fixed owner would push the other off an 80x24 frame. The
// order (seats first) is arbitrary but stable, and the leftover pass means the
// shorter list never reserves rows it cannot use.
func (e *presetEditorScreen) subListPages(a *App) (hostPage, seatPage int) {
	avail, _ := e.frameBudget(a)
	hostNeed := len(e.hostVals) + 2 // host rows + Add + Remove
	seatNeed := len(e.seatVals) + 4 // seat rows + Add + the three verbs
	seatPage = avail / 2
	if seatPage < 1 {
		seatPage = 1
	}
	if seatPage > seatNeed {
		seatPage = seatNeed
	}
	hostPage = avail - seatPage
	if hostPage < 1 {
		hostPage = 1
	}
	if hostPage > hostNeed {
		hostPage = hostNeed
	}
	if spare := avail - hostPage - seatPage; spare > 0 && seatPage < seatNeed {
		seatPage += spare
		if seatPage > seatNeed {
			seatPage = seatNeed
		}
	}
	return hostPage, seatPage
}

// hostPageSize derives the allow_hosts window from the frame height and the
// chrome this frame carries, so the grids and the hint stay on the frame while
// the host rows page. Over-reserving is harmless: a shorter frame never loses a
// row, it only pages the list sooner. While the discard guard is live the hint
// block is not drawn, so the reservation is conservative there.
func (e *presetEditorScreen) hostPageSize(a *App) int {
	hostPage, _ := e.subListPages(a)
	return hostPage
}

// seatPageSize derives the seats window the same way (see subListPages).
func (e *presetEditorScreen) seatPageSize(a *App) int {
	_, seatPage := e.subListPages(a)
	return seatPage
}

// presetLadderInputs carries the resolution-ladder inputs the source column
// mirrors: the explicitly-set CLI flags (empty in the TUI context), the
// edited preset, and the providers.json defaults block (read-only). rowRole
// names HOW the edited row is selected (default key / env / plain save) and
// rides the preset rung into the label (D11); it is ignored when preset is
// nil, which emits no preset rung at all.
type presetLadderInputs struct {
	set     map[string]bool
	preset  *fanout.Preset
	def     providers.Defaults
	rowRole string
}

// resolvePresetSources maps every pre-configurable field onto the rung that
// currently supplies its effective value, mirroring resolveLadder in
// internal/fanout/cli.go (flag > preset > providers.json defaults > engine,
// with the max_native state.json rung and the unsourced host/search knobs).
// The preset rung is emitted role-qualified (preset_default / preset_named /
// preset_saved) so the column can distinguish the store's default_preset key,
// an env-named selection, and a plain saved row. Per-field env rungs do not
// exist (D6 reserves env spellings to the preset selector), so the env label
// in the catalog is never emitted here.
func resolvePresetSources(in presetLadderInputs) map[string]string {
	src := map[string]string{}
	p := in.preset
	has := func(k string) bool { return in.set[k] }
	role := in.rowRole
	if role == "" {
		role = presetRowSaved
	}
	pr := "preset_" + role

	// task
	switch {
	case has("task"):
		src["task"] = "flag"
	case p != nil && p.Task != "":
		src["task"] = pr
	default:
		src["task"] = "engine"
	}
	// n
	switch {
	case has("n"):
		src["n"] = "flag"
	case p != nil && p.N > 0:
		src["n"] = pr
	case in.def.N > 0:
		src["n"] = "defaults"
	default:
		src["n"] = "engine"
	}
	// timeout_seconds
	switch {
	case has("timeout"):
		src["timeout_seconds"] = "flag"
	case p != nil && p.TimeoutSeconds > 0:
		src["timeout_seconds"] = pr
	case in.def.TimeoutSeconds > 0:
		src["timeout_seconds"] = "defaults"
	default:
		src["timeout_seconds"] = "engine"
	}
	// tools
	switch {
	case has("tools"):
		src["tools"] = "flag"
	case p != nil && p.Tools != "":
		src["tools"] = pr
	case in.def.Tools != "":
		src["tools"] = "defaults"
	default:
		src["tools"] = "engine"
	}
	// max_turns
	switch {
	case has("max-turns"):
		src["max_turns"] = "flag"
	case p != nil && p.MaxTurns > 0:
		src["max_turns"] = pr
	case in.def.MaxTurns > 0:
		src["max_turns"] = "defaults"
	default:
		src["max_turns"] = "engine"
	}
	// max_native: the state.json rung supplies the value when neither flag
	// nor preset pins it (resolveMaxNative).
	switch {
	case has("max-native"):
		src["max_native"] = "flag"
	case p != nil && p.MaxNative > 0:
		src["max_native"] = pr
	default:
		src["max_native"] = "state"
	}
	// allow_hosts / search_web source: no engine default supplies these.
	if has("allow-host") {
		src["allow_hosts"] = "flag"
	} else if p != nil && len(p.AllowHosts) > 0 {
		src["allow_hosts"] = pr
	}
	if has("search-endpoint") {
		src["search_endpoint"] = "flag"
	} else if p != nil && p.SearchEndpoint != "" {
		src["search_endpoint"] = pr
	}
	if has("search-key-env") {
		src["search_key_env"] = "flag"
	} else if p != nil && p.SearchKeyEnv != "" {
		src["search_key_env"] = pr
	}
	// root / out_dir
	switch {
	case has("root"):
		src["root"] = "flag"
	case p != nil && p.Root != "":
		src["root"] = pr
	default:
		src["root"] = "engine"
	}
	switch {
	case has("out"):
		src["out_dir"] = "flag"
	case p != nil && p.OutDir != "":
		src["out_dir"] = pr
	default:
		src["out_dir"] = "engine"
	}
	// seats (the only seat source: the retired models field is gone)
	switch {
	case has("seats"):
		src["seats"] = "flag"
	case p != nil && p.Seats != "":
		src["seats"] = pr
	default:
		src["seats"] = "engine"
	}
	// json output switch
	switch {
	case has("json"):
		src["json"] = "flag"
	case p != nil && p.JSON:
		src["json"] = pr
	default:
		src["json"] = "engine"
	}
	return src
}

// presetEditorGuard is the transparent dirty-editor gate, mirroring
// formGuard in screen_model_form.go. While it is armed (the editor is
// dirty) it receives every key in CONFIRM scope before the App Esc
// precedence: it forwards everything except Esc to the editor dispatch,
// and Esc on a dirty editor opens the discard-confirm. Its body carries
// the editor hint, so the armed gate doubles as the footer.
type presetEditorGuard struct {
	e *presetEditorScreen
}

func (g *presetEditorGuard) Title() string { return "" }

func (g *presetEditorGuard) Body(w int) []string {
	return []string{missingOr(g.e.b, "presets.editor.hint", presetsEditorHint)}
}

func (g *presetEditorGuard) Footer() string { return "" }

// Key forwards one key. Esc on a dirty editor opens the discard-confirm and
// keeps the gate; Esc on a clean editor closes the gate (the Esc is
// consumed, and the App pop happens on a further Esc, plain precedence).
func (g *presetEditorGuard) Key(k Key, a *App) bool {
	e := g.e
	if k == KeyEsc {
		if !e.dirty() {
			return false
		}
		e.askDiscard()
		return true
	}
	e.dispatch(k)
	if e.dirty() && a.Modal() == nil {
		a.ShowModal(g)
	}
	return true
}
