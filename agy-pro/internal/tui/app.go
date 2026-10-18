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

	"agypro/internal/cellwidth"
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

	var keys KeySource = NewLineKeys(in)
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
		// (code review seat 3 F-03).
		return paintLoop(app, keys, NewPainter(out, cap), bannerLines(cap, Bundle().T("banner.tagline")))
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
			c.Presets = len(pf.Presets)
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
// parameters close over the parent's selection: providers -> models(name),
// models -> form(mode, modelID), presets -> editor(name), prompts ->
// editor(slot). The settings row opens the full settings grid (the only
// surface wiring the Appearance writers: language and launcher_menu, and
// the only MsgID-localizing renderer); the language row opens the language
// picker.
func (s *setupShell) open(id string, a *App) {
	switch id {
	case hubIDProviders:
		a.Push(NewProvidersScreen(s.core, func(name string, app *App) {
			app.Push(NewModelsScreen(s.core, name, func(modeName, modelID string, app2 *App) {
				app2.Push(NewModelFormScreen(s.core, name, modeName, modelID))
			}))
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

// paintLoop drives the render/step loop through the ANSI painter (console
// hosts only; the capability layer already decided stdout is a console).
// It mirrors App.Run's loop shape; the painter needs the Frame object, so
// the loop lives here instead of behind io.Writer. The first frame carries
// the banner block above the screen body; every frame carries the chrome
// palette roles (code review F-05: a nil styles slice painted everything
// with RoleNone, so the seven authored chrome colors never reached the
// terminal).
func paintLoop(app *App, keys KeySource, p *Painter, banner []string) int {
	first := true
	for {
		var bannerBlock []string
		if first {
			bannerBlock = banner
		}
		first = false
		pf := composeFrame(app, bannerBlock)
		if err := p.Paint(pf.frame, pf.styles); err != nil {
			return 1
		}
		k, ok := keys.ReadKey()
		if !ok {
			break
		}
		if !app.Step(k) {
			break
		}
	}
	return 0
}

// bannerLines picks the banner variant for the detected console geometry
// and banner policy (code review seat 3 F-03): the full spark block on
// roomy consoles, the compact 3-line header when rows are short, the
// one-line wordmark on narrow ones, and nothing at all when the capability
// suppresses the banner (pipes and non-console stdout stay byte-clean). A
// plain tier on a real console (NO_COLOR, VT-enable failure) still gets a
// variant: the ASCII figure and the bare wordmark are zero escape bytes by
// construction.
func bannerLines(cap Cap, tagline string) []string {
	if cap.SuppressBanner {
		return nil
	}
	w, h := cap.Width, cap.Height
	if w <= 0 {
		w = defaultFrameWidth
	}
	if h <= 0 {
		h = defaultFrameHeight
	}
	switch {
	case h >= 22 && w >= 52:
		return Render(cap.Tier, w, tagline)
	case w >= 30:
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

// composeFrame assembles the frame the console painter writes for one loop
// iteration: the same blocks App.Frame composes (title, body, modal,
// message, footer, blank separators, height truncation), plus the banner
// block first when the caller passes one, with every line tagged with its
// chrome role: the screen title is RoleTitle, a list body line carrying the
// "▶ " cursor marker is RoleSelection, the message line and modal titles are
// RoleWarn, footers are RoleMuted, and everything else is RoleNone.
//
// Control-strip ordering (re-review seat 4): the banner is trusted content
// composed by our own art code, so its lines pass through verbatim and the
// frame marks them via Frame.Trusted; every untrusted block (title, body,
// modal, message) is stripped as it is appended. Stripping the assembled
// lines instead deleted the banner's own SGR bytes and printed visible junk
// like "[38;5;75m" on color consoles, and the painter's final sink strip
// would do the same, so the painter only re-strips the untrusted tail.
// TestComposeFrameMatchesAppFrame pins the line assembly to App.Frame so
// the two can never drift silently.
func composeFrame(a *App, banner []string) paintedFrame {
	w := a.Width()
	var blocks [][]string
	var blockRoles [][]Role
	if len(banner) > 0 {
		blocks = append(blocks, banner)
		blockRoles = append(blockRoles, make([]Role, len(banner)))
	}
	if top := a.Top(); top != nil {
		blocks = append(blocks, []string{stripControl(cellwidth.Truncate(top.Title(), w))})
		blockRoles = append(blockRoles, []Role{RoleTitle})
		body := stripFrameLines(top.Draw(a))
		bodyRoles := make([]Role, len(body))
		for i, line := range body {
			if strings.HasPrefix(line, "\u25b6 ") {
				bodyRoles[i] = RoleSelection
			}
		}
		blocks = append(blocks, body)
		blockRoles = append(blockRoles, bodyRoles)
	}
	if m := a.Modal(); m != nil {
		mb := append([]string{cellwidth.Truncate(m.Title(), w)}, m.Body(w)...)
		mb = append(mb, m.Footer())
		mb = stripFrameLines(mb)
		mr := make([]Role, len(mb))
		mr[0] = RoleWarn
		mr[len(mb)-1] = RoleMuted
		blocks = append(blocks, mb)
		blockRoles = append(blockRoles, mr)
	}
	if msg := a.Message(); msg != "" {
		blocks = append(blocks, []string{stripControl(cellwidth.Truncate(msg, w))})
		blockRoles = append(blockRoles, []Role{RoleWarn})
	}
	blocks = append(blocks, []string{baseFooter})
	blockRoles = append(blockRoles, []Role{RoleMuted})

	lines := make([]string, 0, a.Height())
	styles := make([]Role, 0, a.Height())
	for i, b := range blocks {
		if len(b) == 0 {
			continue
		}
		if i > 0 && len(lines) > 0 {
			lines = append(lines, "")
			styles = append(styles, RoleNone)
		}
		lines = append(lines, b...)
		styles = append(styles, blockRoles[i]...)
	}
	if len(lines) > a.Height() {
		lines = lines[:a.Height()]
		styles = styles[:a.Height()]
	}
	// The banner sits at the top of the frame, so the trusted prefix is the
	// banner line count clamped by the height truncation above.
	trusted := len(banner)
	if trusted > len(lines) {
		trusted = len(lines)
	}
	return paintedFrame{
		frame:  Frame{Width: w, Height: a.Height(), Dir: a.Dir(), Trusted: trusted, Lines: lines},
		styles: styles,
	}
}
