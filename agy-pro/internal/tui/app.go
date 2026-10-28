// app.go is the setup TUI app shell: it assembles the screen tree over the
// platform-neutral engine (tui.go), wires the hub's open factory to the data
// layers, detects the console capability once, and runs the render/step
// loop. P12's runSetup entry calls RunSetup on a raw-console host; every
// other host runs the existing line-mode wizard instead (plan v2 section 1).
//
// Language threading (plan v2 section 4): the shell resolves the stored
// language before constructing any screen, calls SetLanguage so Bundle()
// binds it, and builds the App with i18n.Dir(language). The language
// screen persists the choice through Core.Set, calls SetLanguage again,
// flips Dir for ar, and pops itself back to the hub. The hub is the only
// persistent screen: it re-reads the language through the live counts
// supplier on every frame and rebuilds its rows when the code changes, and
// every other screen is constructed lazily after the switch, so it binds
// the new bundle at construction. That is the whole stack rebuild; the
// App stack itself stays opaque (tui.go is unchanged).
package tui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	"agypro/internal/fanout"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
	"agypro/internal/settings"
)

// defaultGeometry backs the app frame when the capability layer reports no
// console size (plain cap from a non-console host).
const (
	defaultFrameWidth  = 80
	defaultFrameHeight = 24
)

// RunSetup is the setup TUI entry point. It detects the console capability
// once (restoring the console mode on exit), assembles the app, and runs the
// loop. in supplies keys: the raw console reader when the host provides one,
// else the line-mode degrade reader (identical key vocabulary, so the screen
// tree behaves the same on every host). out receives the painted frames;
// on console hosts the first frame carries the banner above the screen
// body. errw is reserved for future diagnostics (v1 emits none).
// The return value is the process exit code, always 0 for a clean exit.
func RunSetup(in io.Reader, out, errw io.Writer) int {
	cap := Detect()
	defer cap.Restore()
	return runSetup(in, out, errw, cap)
}

// runSetup runs the loop under an injected capability object. It is the test
// seam: tests drive it with a plain cap (banner suppressed, zero escape
// bytes) and a scripted reader, no console required.
func runSetup(in io.Reader, out, errw io.Writer, cap Cap) int {
	app := assembleSetupApp(cap)

	// The line-mode reader answers the `help` gesture itself, so it needs the
	// session's output stream: the word list has to land where the frames go,
	// never on a stream the operator cannot see (SESSION6 P6-2). A console
	// host replaces the whole reader below and the gesture is unreachable there
	// (mapConsoleKey produces no such key), so the wiring is line-mode only.
	var keys KeySource = NewLineKeys(in).WithHelpOut(out)
	if f, ok := in.(*os.File); ok {
		if ck, err := OpenConsoleKeys(f); err == nil {
			keys = ck
			defer ck.Close()
		}
	}

	if !cap.SuppressBanner {
		// The banner rides inside the first painted frame. A pre-loop
		// splash print was invisible on color tiers: the first Paint opens
		// with a cursor-home sequence and immediately overwrote it
		// (code review seat 3 F-03). The session owns the loop so a latched
		// console resize can reach the app before the next composition
		// (user decision 8B); the line-mode/non-console path below never
		// installs a hook and stays byte-identical.
		s := newPaintSession(app, keys, NewPainter(out, cap), out, cap, Bundle().T("banner.tagline"))
		s.wireResize()
		return s.run()
	}
	// Non-console stdout: plain Fprintln frames, never an escape byte
	// (plan v2 section 2, C3 pipe policy).
	return app.Run(keys, out)
}

// assembleSetupApp builds the screen tree for the given capability: the hub
// as the root screen plus, on a first run (state.json carries no language
// key), the language screen pushed above it so Esc returns to the hub
// instead of exiting.
func assembleSetupApp(cap Cap) *App {
	lang, firstRun := storedLanguage()
	SetLanguage(lang)

	w, h := cap.Width, cap.Height
	if w <= 0 {
		w = defaultFrameWidth
	}
	if h <= 0 {
		h = defaultFrameHeight
	}
	app := NewApp(w, h, dirFor(lang))

	shell := &setupShell{core: settings.NewCore(settings.Deps{})}
	app.Push(NewHubScreen(hubCounts, shell.open))
	if firstRun {
		app.Push(newLanguageScreen(shell.core, ""))
	}
	// A corrupt fanout-presets.json must surface now, on the first frame's
	// message line, not only when the operator opens the Presets screen:
	// the silent downgrade plus the next save destroyed every saved preset
	// with no visible warning (security review seat 4 MEDIUM 2).
	_, _ = presetStore(app)
	// The same first-frame treatment for swarm-state.json (security review
	// F-3): its corrupt-file warning went to os.Stderr, which the full-screen
	// frame cannot show, so the saved seat presets were overwritten from the
	// Settings screen with nothing said. The Settings surface re-wires the
	// warning into its own corrupt latch, so this startup read is only the
	// earliest notice.
	warnSwarmStore(app, shell.core)
	return app
}

// storedLanguage resolves the persisted UI language. firstRun is true when
// state.json has no language key (a missing file loads as a default state,
// which also counts as first run).
func storedLanguage() (lang string, firstRun bool) {
	st, err := mode.LoadState()
	if err != nil || st == nil {
		return mode.DefaultLanguage, true
	}
	if st.Language == "" {
		return mode.DefaultLanguage, true
	}
	return st.EffectiveLanguage(), false
}

// dirFor maps a language code onto the frame direction the painter mirrors.
func dirFor(lang string) Dir {
	if i18n.Dir(lang) == "rtl" {
		return DirRTL
	}
	return DirLTR
}

// hubCounts is the live supplier behind the hub summary and rows: every
// frame re-reads the stores so the counts and the language row note stay
// fresh after any write.
func hubCounts() HubCounts {
	c := HubCounts{Language: mode.DefaultLanguage, RegistryOK: true}
	if st, err := mode.LoadState(); err == nil && st != nil {
		c.Language = st.EffectiveLanguage()
	}
	reg, err := providers.Load()
	switch {
	case err == nil:
		c.Providers = len(reg.Providers)
	case errors.Is(err, os.ErrNotExist):
		// Pre-setup state: an empty registry is not a failure.
	default:
		c.RegistryOK = false
	}
	if store, serr := fanout.NewPresetFileStore(); serr == nil {
		if pf, lerr := store.LoadPresets(); lerr == nil && pf != nil {
			c.Presets = len(visiblePresets(pf))
		}
	}
	for _, slot := range promptSlots {
		if _, source, rerr := resolveSlot(slot); rerr == nil && source != fanout.PromptSourceCompiled {
			c.PromptOverrides++
		}
	}
	return c
}

// setupShell carries the shared Core and the screen factories the hub's
// open callback dispatches to.
type setupShell struct {
	core *settings.Core
}

// open maps a hub row ID onto its screen and pushes it. Factories needing
// parameters close over the parent's selection: providers -> models(name) and
// provider form(mode, name), models -> form(mode, modelID), presets ->
// editor(name), prompts -> editor(slot). The settings row opens the full
// settings grid (the only surface wiring the Appearance writers: language and
// launcher_menu, and the only MsgID-localizing renderer); the language row
// opens the language picker.
func (s *setupShell) open(id string, a *App) {
	switch id {
	case hubIDProviders:
		a.Push(NewProvidersScreen(s.core, func(name string, app *App) {
			app.Push(NewModelsScreen(s.core, name, func(modeName, modelID string, app2 *App) {
				app2.Push(NewModelFormScreen(s.core, name, modeName, modelID))
			}))
		}, func(modeName, providerName string, app *App) {
			// The Add row and every Edit row land on the provider form
			// (decision 1A: registration and repair happen inside the TUI).
			// modeName is the form screen's own "add" / "edit" contract;
			// providerName is empty in add mode.
			app.Push(NewProviderFormScreen(s.core, modeName, providerName))
		}))
	case hubIDPresets:
		if store, err := presetStore(a); err == nil {
			a.Push(NewPresetsScreen(store, func(name string, app *App) {
				app.Push(NewPresetEditorScreen(name))
			}))
		}
	case hubIDPrompts:
		a.Push(NewPromptsScreen(func(slot string, app *App) {
			app.Push(NewPromptEditorScreen(slot))
		}))
	case hubIDSettings:
		a.Push(NewSettingsScreen(s.core))
	case hubIDLanguage:
		a.Push(newLanguageScreen(s.core, storedCode()))
	case hubIDDoctor:
		a.Push(NewDoctorScreen(s.core))
	case hubIDHelp:
		a.Push(NewHelpScreen())
	case hubIDDashboard:
		a.Push(NewDashboardScreen())
	case hubIDIntegrations:
		a.Push(NewIntegrationsScreen(s.core))
	}
}

// presetStore loads the fanout preset store for the TUI and surfaces the
// corrupt-file fallback warning on the app's message line instead of the
// store's nil WarnOut sink (security review seat 4 MEDIUM 2). The warning
// rides the frame the app paints next, so it is visible on console and
// plain hosts alike; without it the empty degraded store renders as an
// honest "no presets" list and the next save silently replaces the file.
func presetStore(a *App) (*fanout.PresetFileStore, error) {
	store, err := fanout.NewPresetFileStore()
	if err != nil {
		return nil, err
	}
	var warn bytes.Buffer
	store.WarnOut = &warn
	if _, err := store.LoadPresets(); err == nil && warn.Len() > 0 {
		a.SetMessage(strings.TrimSpace(warn.String()))
	}
	return store, nil
}

// warnSwarmStore surfaces the swarm preset store's corrupt-file warning on the
// app's message line at startup (security review F-3), the same first-frame
// treatment presetStore gives the fanout preset file. Without it the store's
// default os.Stderr warning is invisible under the full-screen frame: the
// operator sees an honest "no saved presets" state and the next save in the
// Settings screen replaces the corrupt file. The sink is redirected into a
// throwaway buffer here; the Settings surface re-wires it into its own corrupt
// latch before every read, so no later warning is written into a dead buffer
// that some other surface depends on. An existing message wins: the fanout
// warning above names a file too, and the seat-preset warning is re-surfaced on
// the Saved Seat Presets screen.
func warnSwarmStore(a *App, core *settings.Core) {
	store := core.SwarmStateStore()
	if store == nil {
		return
	}
	var warn bytes.Buffer
	store.WarnOut = &warn
	if _, err := store.LoadPresets(); err == nil && warn.Len() > 0 && a.Message() == "" {
		a.SetMessage(strings.TrimSpace(warn.String()))
	}
}

// storedCode returns the raw stored language code ("" on first run), which
// the language screen uses to preselect its row and switch its footer.
func storedCode() string {
	if st, err := mode.LoadState(); err == nil && st != nil {
		return st.Language
	}
	return ""
}

// newLanguageScreen builds the language selector with the active bundle and
// the shell's choose handler: persist through Core.Set (the one write
// path), reload the active bundle, flip Dir for ar, and pop back to the
// hub. The hub re-binds its rows from the live counts supplier on the next
// frame; screens opened later construct with the new bundle, so the whole
// tree re-renders in the chosen language without a restart.
func newLanguageScreen(core *settings.Core, current string) Screen {
	return NewLanguageScreen(Bundle(), current, func(code string, a *App) {
		if err := core.Set("language", code); err != nil {
			a.SetMessage(Bundle().T("common.error") + ": " + err.Error())
			return
		}
		SetLanguage(code)
		a.SetDir(dirFor(code))
		a.Pop()
	})
}

// paintLoop drives one render/step loop through the ANSI painter. It has no
// production caller: runSetup sends a console host through paintSession.run
// (which adds the resize hook, the geometry re-pick, and the clear) and a
// non-console host through App.Run, so this helper exists for the painter
// tests, which need a frame loop without a console attached. It is the
// no-resize form of paintSession.run. It keeps App.Run's loop shape and needs
// the Frame object, which is why the loop lives here instead of behind
// io.Writer. The banner argument is painted above the screen body on the first
// frame only; every frame carries the chrome palette roles (code review F-05:
// a nil styles slice painted everything with RoleNone, so the seven authored
// chrome colors never reached the terminal).
func paintLoop(app *App, keys KeySource, p *Painter, banner []string) int {
	return (&paintSession{
		app:     app,
		keys:    keys,
		painter: p,
		out:     p.out,
		cap:     p.Cap(),
		tagline: Bundle().T("banner.tagline"),
		banner:  banner,
		first:   true,
	}).run()
}

// paintSession is the console render/step loop state: the app, the key
// source, the painter, the first-frame banner, and the resize latch.
//
// Resize wiring (user decision 8B, UX audit C2-F4/C1-F30): the console
// takeover enables ENABLE_WINDOW_INPUT, so WINDOW_BUFFER_SIZE_EVENT records
// reach the reader, which latches them and calls the installed hook from
// ReadKey. The hook only sets a flag (it runs inside the key read, the same
// goroutine as this loop); applyResize runs at the top of the next iteration,
// before composition, so exactly one full clear lands between the resize and
// the next repaint.
type paintSession struct {
	app     *App
	keys    KeySource
	painter *Painter
	out     io.Writer
	cap     Cap
	tagline string
	banner  []string // banner variant for the next frame that carries one
	first   bool     // the next frame is the first: it carries the banner
	resized bool     // a latched resize awaits application
	size    func() (w, h int, ok bool)
}

// newPaintSession builds the console loop state for one setup run.
func newPaintSession(app *App, keys KeySource, p *Painter, out io.Writer, cap Cap, tagline string) *paintSession {
	return &paintSession{
		app:     app,
		keys:    keys,
		painter: p,
		out:     out,
		cap:     cap,
		tagline: tagline,
		banner:  bannerFor(cap.Width, cap.Height, cap, tagline),
		first:   true,
		size:    consoleSizeFunc(out),
	}
}

// resizeHookSetter is the console reader's resize notification surface
// (console_windows.go SetResizeHook; the non-Windows build is a no-op with
// the same shape). The line-mode reader does not implement it, so the
// non-console path never installs a hook.
type resizeHookSetter interface {
	SetResizeHook(func())
}

// wireResize installs the latch hook on a console key source. A host whose
// key source has no hook keeps the pre-resize behavior exactly.
func (s *paintSession) wireResize() {
	if h, ok := s.keys.(resizeHookSetter); ok {
		h.SetResizeHook(s.requestResize)
	}
}

// requestResize latches one resize notification. It runs synchronously from
// ReadKey, so it only sets the flag; the geometry query, the banner re-pick,
// and the clear belong to the next loop iteration.
func (s *paintSession) requestResize() { s.resized = true }

// applyResize turns one latched notification into a new frame geometry. The
// record payload is the SCREEN BUFFER size, not the viewport
// (WINDOW_BUFFER_SIZE_RECORD.dwSize), so the geometry always comes from a
// fresh ConsoleSize query; ConsoleSize itself reports the viewport. A host
// that cannot answer leaves the frame as it was.
func (s *paintSession) applyResize() {
	s.resized = false
	if s.size == nil {
		return
	}
	w, h, ok := s.size()
	if !ok || w <= 0 || h <= 0 {
		return
	}
	if w == s.app.Width() && h == s.app.Height() {
		// A notification that reports the geometry we already paint (a buffer
		// change at the same viewport) needs no repaint and no clear.
		return
	}
	s.app.Resize(w, h)
	s.banner = bannerFor(s.app.Width(), s.app.Height(), s.cap, s.tagline)
	s.clearScreen()
}

// clearScreen drops the previous frame in one escape: after a resize the old
// geometry's lines no longer match the new width, so their wrap residue would
// otherwise survive the per-line erase of the next paint. The plain tier
// emits nothing (zero escape bytes on a NO_COLOR or VT-less console); the
// painter's ghost-row bookkeeping is reset either way, because the screen it
// was counting rows on is gone.
func (s *paintSession) clearScreen() {
	if s.painter == nil {
		return
	}
	if s.cap.Tier != TierPlain && s.out != nil {
		_, _ = io.WriteString(s.out, clearScreenSeq)
	}
	s.painter.lastRows = 0
}

// clearScreenSeq is one full clear followed by a cursor home: the next paint
// starts from a blank screen top-left.
const clearScreenSeq = "\x1b[2J\x1b[H"

// run drives the render/step loop until the app exits or the key source is
// exhausted. A latched resize is applied before composition, so the frame
// painted next uses the new geometry.
func (s *paintSession) run() int {
	for {
		if s.resized {
			s.applyResize()
		}
		var bannerBlock []string
		if s.first {
			bannerBlock = s.banner
		}
		s.first = false
		pf := composeFrame(s.app, bannerBlock)
		if err := s.painter.Paint(pf.frame, pf.styles); err != nil {
			return 1
		}
		k, ok := s.keys.ReadKey()
		if !ok {
			break
		}
		if !s.app.Step(k) {
			break
		}
	}
	return 0
}

// consoleSizeFunc returns the size query the resize path re-runs. The console
// output handle is the writer the painter draws on; a non-file writer (the
// headless tests, a redirected host) has no console buffer to measure, so the
// query degrades to the process stdout probe and reports what it finds.
func consoleSizeFunc(out io.Writer) func() (w, h int, ok bool) {
	f, _ := out.(*os.File)
	return func() (int, int, bool) {
		if f != nil {
			return ConsoleSize(f)
		}
		return ConsoleSize(os.Stdout)
	}
}

// bannerLines picks the banner variant for the detected console geometry
// and banner policy (code review seat 3 F-03).
func bannerLines(cap Cap, tagline string) []string {
	return bannerFor(cap.Width, cap.Height, cap, tagline)
}

// bannerFor picks the banner variant for one geometry through the plan v2
// sizing ladder (UX audit C2-F5/C1-F15 restored it): the full block needs
// rows>=30 and cols>=52, the compact 3-line header rows>=22 and cols>=30,
// and everything shorter or narrower gets the one-line wordmark (the ASCII
// figure for a plain tier, zero escape bytes by construction). The old
// rows>=22 gate admitted the 11-line block into a 24-row frame; the first
// frame then lost the hub rows and the footer legend to the height cut.
// A capability that suppresses the banner (pipes, non-console stdout) gets
// nothing at all, so those hosts stay byte-clean.
func bannerFor(w, h int, cap Cap, tagline string) []string {
	if cap.SuppressBanner {
		return nil
	}
	if w <= 0 {
		w = defaultFrameWidth
	}
	if h <= 0 {
		h = defaultFrameHeight
	}
	switch {
	case h >= 30 && w >= 52:
		return Render(cap.Tier, w, tagline)
	case h >= 22 && w >= 30:
		return RenderCompact(cap.Tier, w, tagline)
	default:
		return RenderWordmark(cap.Tier, w)
	}
}

// paintedFrame pairs one composed frame with the per-line palette roles the
// painter resolves against the Cap palette.
type paintedFrame struct {
	frame  Frame
	styles []Role
}

// composeFrame adapts the shared frame composer (tui.go App.ComposeFrame) to
// the console painter: same blocks, same body reservation, same priority cut,
// plus the per-line palette role.
//
// Body reservation (UX audit C1-F6, the M3/M4 blocker): the shared composer
// gives the body up first and drops it once the modal needs its rows, so a
// live modal is laid out on the frame instead of being appended past the
// height cut. At 80x24 with a list that fills the page the modal REPLACES the
// body, and the priority cut (phase 3 F-1) never removes the gate legend or
// the base footer, so the providers.json approval gate can never be answered
// blind.
//
// Role map: the screen title is RoleTitle, a list body line carrying the "▶ "
// cursor marker is RoleSelection, the modal title is RoleWarn and its legend
// RoleMuted, the message line takes its kind's role (RoleSuccess, RoleDanger,
// or RoleWarn: user decision D7-7B, see messageRole), footers are RoleMuted,
// and everything else is RoleNone.
//
// Width clamp (UX audit C1-F22/F16, phase 3 F-2): the clamp lives in the
// shared composer now (clampLines in tui.go), at the one choke point every
// path shares. It used to be this adapter's job, so App.Frame and the
// line-mode/test path composed unclamped lines while only the painted frame
// was fitted. With the clamp moved, the loop that used to sit here would be a
// second, redundant clamp over the same lines, so it is gone: every path
// clamps once, through cellwidth, and the trusted banner prefix stays exempt
// (it is our own art, composed at the frame width).
//
// Control-strip ordering (re-review seat 4): the composer strips every
// untrusted block and leaves the trusted banner intact, so the painter's SGR
// bytes survive while disk-borne content cannot smuggle an escape through.
// TestComposeFrameMatchesAppFrame pins the line assembly to App.Frame.
func composeFrame(a *App, banner []string) paintedFrame {
	c := a.ComposeFrame(banner)
	lines := c.Frame.Lines
	styles := make([]Role, len(lines))
	for i := 0; i < len(c.Kinds); {
		kind := c.Kinds[i]
		j := i
		for j < len(c.Kinds) && c.Kinds[j] == kind {
			j++
		}
		switch kind {
		case BlockTitle:
			for x := i; x < j; x++ {
				styles[x] = RoleTitle
			}
		case BlockBody:
			for x := i; x < j; x++ {
				if strings.HasPrefix(lines[x], "\u25b6 ") {
					styles[x] = RoleSelection
				}
			}
		case BlockModal:
			// A multi-line modal run is title first, legend last. When the
			// cut ladder has taken the title (degenerate geometry) the run
			// collapses to the legend alone, which is the muted line.
			if j-1 > i {
				styles[i] = RoleWarn
				styles[j-1] = RoleMuted
			} else {
				styles[i] = RoleMuted
			}
		case BlockMessage:
			role := messageRole(a.Message())
			for x := i; x < j; x++ {
				styles[x] = role
			}
		case BlockFooter:
			for x := i; x < j; x++ {
				styles[x] = RoleMuted
			}
		}
		i = j
	}
	return paintedFrame{
		frame: Frame{
			Width:     c.Frame.Width,
			Height:    c.Frame.Height,
			Dir:       c.Frame.Dir,
			Trusted:   c.Frame.Trusted,
			CaretLine: c.Frame.CaretLine,
			CaretCell: c.Frame.CaretCell,
			Lines:     lines,
		},
		styles: styles,
	}
}

// MessageKind is the semantic class of the message line. The palette authors
// a role for each one (termcap.go RoleSuccess/RoleWarn/RoleDanger); the
// painter used to stamp every message with RoleWarn, so a success confirmation
// and a write failure rendered in the same amber.
type MessageKind int

const (
	// MessageWarn is the neutral/unknown class: a cancel note, a refusal, a
	// validation hint. It keeps the historical amber.
	MessageWarn MessageKind = iota
	// MessageSuccess is a completed mutation ("saved", "deleted", a written
	// setting). It renders RoleSuccess.
	MessageSuccess
	// MessageError is a failed operation (a Core verb error, a load or write
	// failure). It renders RoleDanger.
	MessageError
)

// messageRole maps a message line onto its authored palette role.
func messageRole(msg string) Role {
	switch messageKindOf(msg) {
	case MessageSuccess:
		return RoleSuccess
	case MessageError:
		return RoleDanger
	default:
		return RoleWarn
	}
}

// messageKindOf classifies one message line. SetMessage carries only the text
// (the App field lives in tui.go), so the class travels IN the text: the
// settings grid stamps its results with the [SUCCESS] tag, and the error paths
// prefix the localized common.error label. The classifier reads both the
// legacy English tags that exist today and the catalog labels (common.success
// / common.error / common.warning) the screens are moving to, so a caller
// needs no new API and no new catalog key, and a screen that switches to the
// translated tag keeps its color.
func messageKindOf(msg string) MessageKind {
	if msg == "" {
		return MessageWarn
	}
	if hasMessageTag(msg, "[SUCCESS]") {
		return MessageSuccess
	}
	if hasMessageTag(msg, "[ERROR]") {
		return MessageError
	}
	b := Bundle()
	if b == nil {
		return MessageWarn
	}
	if label := b.T("common.success"); label != "common.success" && hasMessageTag(msg, label) {
		return MessageSuccess
	}
	if label := b.T("common.error"); label != "common.error" && hasMessageTag(msg, label) {
		return MessageError
	}
	if label := b.T("common.warning"); label != "common.warning" && hasMessageTag(msg, label) {
		return MessageWarn
	}
	return MessageWarn
}

// hasMessageTag reports whether msg opens with tag used as a label: the tag
// alone, or followed by a colon, a space, or a tab. The comparison folds case
// so the English fallbacks ("error: ...", "[SUCCESS] ...") classify the same
// way the catalog labels do.
func hasMessageTag(msg, tag string) bool {
	if tag == "" || len(msg) < len(tag) || !strings.EqualFold(msg[:len(tag)], tag) {
		return false
	}
	rest := msg[len(tag):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ':', ' ', '\t':
		return true
	}
	return false
}
