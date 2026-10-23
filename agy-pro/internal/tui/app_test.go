package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agypro/internal/cellwidth"
	"agypro/internal/i18n"
	"agypro/internal/mode"
	"agypro/internal/providers"
)

// plainTestCap is the headless capability: plain tier, no console size,
// banner suppressed (zero banner bytes, zero escape bytes).
func plainTestCap() Cap {
	return Cap{Tier: TierPlain, Palette: DefaultPalette(), SuppressBanner: true}
}

// appTestHome points AGY_PRO_HOME at a temp dir and writes state.json (with
// the given language code; "" writes no language key, simulating a first
// run) plus a two-provider registry. The live stores are never touched.
func appTestHome(t *testing.T, lang string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGY_PRO_HOME", home)
	t.Setenv(providers.EnvProviders, "")
	t.Setenv("AGY_PRO_SEAT_PREAMBLE", "")

	st := mode.State{
		CurrentMode:    "full",
		DefaultProfile: "native",
		ManagedItems:   append([]string(nil), mode.DefaultManagedItems...),
		UpdatedAt:      time.Now().UTC(),
	}
	if lang != "" {
		st.Language = lang
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "state.json"), data, 0o600); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	reg := twoProviderRegistry()
	data, err = json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "providers.json"), data, 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
	return home
}

// langDigit returns the digit key selecting code on the language screen
// (digits cover rows 1-9; every language the tests pick sits in that
// range by construction).
func langDigit(t *testing.T, code string) Key {
	t.Helper()
	langs := i18n.Languages()
	for i, m := range langs {
		if m.Code == code {
			if i > 8 {
				t.Fatalf("language %q is row %d, past the single-digit range", code, i+1)
			}
			return Digit(i + 1)
		}
	}
	t.Fatalf("language %q not in the closed set", code)
	return ""
}

func TestRunSetupPlainSuppressesBanner(t *testing.T) {
	appTestHome(t, "en")
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain run emitted escape bytes:\n%q", out.String())
	}
	if strings.Contains(out.String(), i18n.Load("en").T("banner.tagline")) {
		t.Fatalf("plain run rendered the banner:\n%s", out.String())
	}
	if !strings.Contains(out.String(), i18n.Load("en").T("menu.title")) {
		t.Fatalf("plain run missing the hub frame:\n%s", out.String())
	}
}

func TestRunSetupConsolePaintsBannerAndFrames(t *testing.T) {
	appTestHome(t, "en")
	cap := Cap{Tier: Tier256, Width: 100, Height: 30, Palette: DefaultPalette()}
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, cap); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), i18n.Load("en").T("banner.tagline")) {
		t.Fatalf("console run missing the banner tagline:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("console run emitted no ANSI frames:\n%q", out.String())
	}
}

func TestSetupHubRendersCounts(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{})
	for _, want := range []string{
		i18n.Load("en").T("menu.title"),
		"2 providers",
		"0 presets",
		i18n.Endonym("en"),
		// The dashboard row is part of the assembled shell's hub, so the first
		// frame of the real setup flow proves the row is reachable. The pin is
		// the catalog value, not the screen's stand-in, so it fails if the row
		// ever falls back instead of resolving.
		i18n.Load("en").T("menu.dashboard"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("hub frame missing %q; frames:\n%s", want, out)
		}
	}
}

func TestSetupDigitOneOpensProviders(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1)}})
	if !strings.Contains(out, "gemini") {
		t.Fatalf("providers screen did not open; frames:\n%s", out)
	}
}

func TestSetupEnterOpensModels(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(1), KeyEnter}})
	if !strings.Contains(out, "Models: gemini") {
		t.Fatalf("models screen did not open; frames:\n%s", out)
	}
}

// TestSetupProvidersWiringOpensProviderForm pins the app.go providers case
// (decision 1A) end to end: the hub row opens the providers list over the real
// Core, the Add row pushes the provider form screen the shell wires
// (NewProviderFormScreen), and a save on that form (a registry write) is
// visible on the list the operator pops back to. With two providers in the test
// home the Add row is row 3 (1 gemini, 2 anthropic, 3 Add), so this also pins
// that the entity rows still own digits 1 and 2.
func TestSetupProvidersWiringOpensProviderForm(t *testing.T) {
	home := appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	// The loop paints before it reads (paintLoop order), and the hub builds its
	// rows on the first Draw, so compose once before the first key.
	_ = composeFrame(app, nil)
	app.Step(Digit(1))
	if _, ok := app.Top().(*ProvidersScreen); !ok {
		t.Fatalf("hub row 1 did not open the providers screen; top = %T", app.Top())
	}
	app.Step(Digit(3))
	top := app.Top()
	if _, ok := top.(*ProvidersScreen); ok {
		t.Fatalf("the provider add row did not push a screen above the list; top = %T", top)
	}
	if top == nil || top.Title() == "" {
		t.Fatalf("the provider form screen has no title; top = %T", top)
	}
	lines := app.Frame().Lines
	if !frameLineMatches(lines, func(l string) bool { return l == top.Title() }) {
		t.Fatalf("the provider form title %q is not on the frame:\n%s", top.Title(), strings.Join(lines, "\n"))
	}

	// Stand in for the form's approved save (the form unit owns that write):
	// the registry grows, the form pops back, and the list has to render the new
	// provider on the next frame instead of the snapshot it was built from.
	writeProviderRegistry(t, home, 3)
	for i := 0; i < 3; i++ {
		if _, ok := app.Top().(*ProvidersScreen); ok {
			break
		}
		app.Step(KeyEsc) // a live form message takes the first Esc (App clears it first)
	}
	if _, ok := app.Top().(*ProvidersScreen); !ok {
		t.Fatalf("Esc did not return to the providers list; top = %T", app.Top())
	}
	out := strings.Join(app.Frame().Lines, "\n")
	if !strings.Contains(out, "provider-03") {
		t.Fatalf("the list did not pick up the saved provider after the form popped:\n%s", out)
	}
	// The provider verbs are sticky-target rows (unit P1-1 inverted the layout),
	// so the saved provider renders as an entity row and the fixed verb block
	// follows it: 3 providers + Add + Test all + Test + Edit + Delete = 8 rows.
	if !strings.Contains(out, "[ 8] Delete") {
		t.Fatalf("the inverted action rows are missing after the save:\n%s", out)
	}
}

func TestSetupAddRowOpensForm(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	keys := []Key{Digit(1), KeyEnter, Digit(3)} // gemini has 2 models; row 3 is Add
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	if !strings.Contains(out, i18n.Load("en").T("models.form.title.add")) {
		t.Fatalf("model form did not open; frames:\n%s", out)
	}
}

func TestSetupEscPopsBackToModels(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	keys := []Key{Digit(1), KeyEnter, Digit(3), KeyEsc}
	out := app.CaptureRun(&ScriptedKeys{Keys: keys})
	i := strings.LastIndex(out, i18n.Load("en").T("models.form.title.add"))
	if i < 0 {
		t.Fatalf("model form never rendered; frames:\n%s", out)
	}
	if tail := out[i:]; !strings.Contains(tail, "Models: gemini") {
		t.Fatalf("Esc did not pop the clean form back to models; frames:\n%s", out)
	}
}

func TestSetupFirstRunPushesLanguageScreen(t *testing.T) {
	appTestHome(t, "") // no language key: first run
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{})
	if !strings.Contains(out, i18n.Load("en").T("lang.screen.title")) {
		t.Fatalf("first run missing the language screen; frames:\n%s", out)
	}
	if strings.Contains(out, i18n.Load("en").T("menu.title")) {
		t.Fatalf("language screen must cover the hub on first run; frames:\n%s", out)
	}
}

func TestSetupLanguageScreenEscReturnsToHub(t *testing.T) {
	appTestHome(t, "")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{KeyEsc}})
	i := strings.LastIndex(out, i18n.Load("en").T("lang.screen.title"))
	if i < 0 {
		t.Fatalf("language screen never rendered; frames:\n%s", out)
	}
	if tail := out[i:]; !strings.Contains(tail, i18n.Load("en").T("menu.title")) {
		t.Fatalf("Esc on the language screen must return to the hub; frames:\n%s", out)
	}
}

func TestSetupLanguageChoicePersistsAndRerenders(t *testing.T) {
	home := appTestHome(t, "")
	// The language screen's choose handler calls SetLanguage, so this test moves
	// the package-level bundle. It must hand English back: the other screens'
	// tests assert English catalog text, and without the restore a shuffled run
	// (go test -shuffle=on) fails in whatever test happens to run next.
	defer SetLanguage("en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{langDigit(t, "ko")}})

	data, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if !strings.Contains(string(data), `"language": "ko"`) {
		t.Fatalf("state.json missing the persisted language; got:\n%s", data)
	}

	// The language screen frame itself is rendered pre-choice, in the
	// default (en) bundle; locate it, then demand the hub re-renders in ko.
	i := strings.LastIndex(out, i18n.Load("en").T("lang.screen.title"))
	if i < 0 {
		t.Fatalf("language screen never rendered; frames:\n%s", out)
	}
	tail := out[i:]
	if !strings.Contains(tail, i18n.Load("ko").T("menu.title")) {
		t.Fatalf("choosing a language must pop back to the hub; frames:\n%s", out)
	}
	if !strings.Contains(tail, i18n.Endonym("ko")) {
		t.Fatalf("hub did not re-render with the new language endonym; frames:\n%s", out)
	}
}

func TestSetupReturningSkipsLanguageScreen(t *testing.T) {
	appTestHome(t, "ko")
	// assembleSetupApp binds the stored language through SetLanguage, so this
	// test moves the package-level bundle exactly like the choice test above.
	// It must hand English back: without the restore every later screen test
	// that asserts English catalog text (dashboard, doctor, help, manual) reads
	// Korean and fails in whatever test happens to run next.
	defer SetLanguage("en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{})
	if strings.Contains(out, i18n.Load("ko").T("lang.screen.title")) {
		t.Fatalf("returning run must skip the language screen; frames:\n%s", out)
	}
	if !strings.Contains(out, i18n.Load("ko").T("menu.title")) {
		t.Fatalf("returning run missing the hub; frames:\n%s", out)
	}
	if !strings.Contains(out, i18n.Endonym("ko")) {
		t.Fatalf("hub missing the stored language endonym; frames:\n%s", out)
	}
}

func TestSetupHubSettingsRowOpensSettingsScreen(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(4)}})
	title := i18n.Load("en").T("settings.screen.title")
	i := strings.LastIndex(out, title)
	if i < 0 {
		t.Fatalf("hub settings row did not open the settings screen; frames:\n%s", out)
	}
	// The settings grid's cursor-row detail block is unique to this screen;
	// the hub frame carries no such lines.
	if tail := out[i:]; !strings.Contains(tail, "value   :") {
		t.Fatalf("settings screen body missing the cursor-row detail; frames:\n%s", out)
	}
}

func TestSetupHubLanguageRowOpensLanguageScreen(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	out := app.CaptureRun(&ScriptedKeys{Keys: []Key{Digit(7)}})
	if !strings.Contains(out, i18n.Load("en").T("lang.screen.title")) {
		t.Fatalf("hub language row did not open the language screen; frames:\n%s", out)
	}
}

// TestRunSetupConsoleFirstFramePaintsBanner pins the banner placement fix
// (code review seat 3 F-03): the banner lines must be part of the first
// painted frame, above the screen body, not a pre-loop splash that the
// cursor-home sequence overwrites. The re-review (seat 4) found the strip
// ordering bug with this same shape of test: the glyph/tagline checks pass
// even when the control-char strip has deleted the banner's own escapes,
// so this pin also asserts the actual SGR bytes survive composition and
// painting (a stripped banner would leave only the printable junk
// "[38;5;...", never the "\x1b[38;5;" prefix).
func TestRunSetupConsoleFirstFramePaintsBanner(t *testing.T) {
	appTestHome(t, "en")
	cap := Cap{Tier: Tier256, Width: 100, Height: 30, Palette: DefaultPalette()}
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, cap); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	s := out.String()
	tag := i18n.Load("en").T("banner.tagline")
	menu := i18n.Load("en").T("menu.title")
	if strings.Index(s, tag) < 0 {
		t.Fatalf("console run missing the banner tagline:\n%q", s)
	}
	if strings.Index(s, tag) > strings.Index(s, menu) {
		t.Fatalf("banner must paint above the screen body in the first frame:\n%q", s)
	}
	if !strings.Contains(s, "▄") {
		t.Fatalf("roomy console must get the full spark block banner:\n%q", s)
	}
	if !strings.Contains(s, "\x1b[38;5;") && !strings.Contains(s, "\x1b[38;2;") {
		t.Fatalf("banner SGR escapes were stripped from the first frame (visible junk like %q):\n%q", "[38;5;75m", s)
	}
}

// TestBannerLadderPicksVariantByGeometry pins the plan v2 sizing ladder
// (UX audit C2-F5/C1-F15): the full spark block needs rows>=30 and cols>=52,
// the compact 3-line header rows>=22 and cols>=30, the one-line wordmark below
// that, and nothing when the banner is suppressed (pipes stay byte-clean).
// The old rows>=22 gate admitted the 11-line block into a 24-row frame, whose
// first-frame height cut then ate the hub rows and the footer legend.
func TestBannerLadderPicksVariantByGeometry(t *testing.T) {
	tag := i18n.Load("en").T("banner.tagline")
	full := bannerLines(Cap{Tier: Tier256, Width: 100, Height: 30, Palette: DefaultPalette()}, tag)
	if len(full) == 0 || !strings.Contains(strings.Join(full, "\n"), "▄") {
		t.Fatalf("roomy console must get the full spark block: %q", full)
	}
	compact := bannerLines(Cap{Tier: Tier256, Width: 40, Height: 24, Palette: DefaultPalette()}, tag)
	joined := strings.Join(compact, "\n")
	if strings.Contains(joined, "▄") || !strings.Contains(joined, "█") {
		t.Fatalf("short console must get the compact gradient header: %q", compact)
	}
	if len(compact) != 3 {
		t.Fatalf("compact header = %d lines, want 3: %q", len(compact), compact)
	}
	word := bannerLines(Cap{Tier: Tier256, Width: 20, Height: 10, Palette: DefaultPalette()}, tag)
	if len(word) != 1 {
		t.Fatalf("narrow console must get the one-line wordmark: %q", word)
	}
	// 29 rows is one row short of the full block's budget: the 11-line figure
	// there is what starved the first frame before the ladder was restored.
	if got := bannerLines(Cap{Tier: Tier256, Width: 100, Height: 29, Palette: DefaultPalette()}, tag); len(got) != 3 {
		t.Fatalf("29 rows must get the compact header, got %d lines: %q", len(got), got)
	}
	if got := bannerLines(Cap{Tier: Tier256, Width: 100, Height: 21, Palette: DefaultPalette()}, tag); len(got) != 1 {
		t.Fatalf("21 rows must get the one-line wordmark, got %d lines: %q", len(got), got)
	}
	if got := bannerLines(plainTestCap(), tag); got != nil {
		t.Fatalf("suppressed banner must render nothing: %q", got)
	}
}

// TestRunSetupPlainRunHasNoBannerBytes pins the C3 pipe policy on the new
// first-frame banner: a suppressed (plain, non-console) run must contain
// neither the tagline nor any escape byte.
func TestRunSetupPlainRunHasNoBannerBytes(t *testing.T) {
	appTestHome(t, "en")
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain run emitted escape bytes:\n%q", out.String())
	}
	if strings.Contains(out.String(), i18n.Load("en").T("banner.tagline")) {
		t.Fatalf("plain run rendered the banner:\n%s", out.String())
	}
}

// TestRunSetupCorruptPresetsWarnsOnMessageLine pins the preset-store WarnOut
// fix (security review seat 4 MEDIUM 2): a corrupt fanout-presets.json must
// surface its warning on the user-visible message line of the first frame,
// not vanish into the nil WarnOut sink.
func TestRunSetupCorruptPresetsWarnsOnMessageLine(t *testing.T) {
	home := appTestHome(t, "en")
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt presets: %v", err)
	}
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "fanout-presets.json is corrupt") {
		t.Fatalf("corrupt preset warning missing from the message line:\n%s", out.String())
	}
}

// TestPaintLoopAppliesPaletteRoles pins the chrome palette wiring (code
// review F-05): a color-tier paint must emit the role escapes (title,
// selection, muted footer) and a plain-tier paint must stay byte-clean even
// though the same styles slice is passed.
func TestPaintLoopAppliesPaletteRoles(t *testing.T) {
	pal := DefaultPalette()
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: Tier256, Width: 100, Height: 30, Palette: pal})
	if code := paintLoop(app, &ScriptedKeys{}, p, nil); code != 0 {
		t.Fatalf("paintLoop exit = %d, want 0", code)
	}
	for _, seq := range []string{
		pal.Title.FG(Tier256),     // title bar
		pal.Selection.FG(Tier256), // list cursor row
		pal.Muted.FG(Tier256),     // footer legend
	} {
		if !strings.Contains(out.String(), seq) {
			t.Fatalf("color-tier paint missing role escape %q:\n%q", seq, out.String())
		}
	}

	appTestHome(t, "en")
	app = assembleSetupApp(plainTestCap())
	out.Reset()
	p = NewPainter(&out, plainTestCap())
	if code := paintLoop(app, &ScriptedKeys{}, p, nil); code != 0 {
		t.Fatalf("plain paintLoop exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain-tier paint emitted escape bytes:\n%q", out.String())
	}
}

// TestComposeFrameMatchesAppFrame pins the painted-frame composer to
// App.Frame's block assembly (title, body, modal, message, footer,
// separators, height truncation). If the frame model ever changes, this
// fails loudly instead of the console painter silently painting a stale
// layout with misaligned roles.
func TestComposeFrameMatchesAppFrame(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())
	assertSame := func(name string) {
		t.Helper()
		got := composeFrame(app, nil)
		want := app.Frame()
		if got.frame.String() != want.String() {
			t.Fatalf("%s: composed frame drifted from App.Frame:\n got: %q\nwant: %q", name, got.frame.String(), want.String())
		}
		if len(got.styles) != len(got.frame.Lines) {
			t.Fatalf("%s: styles length %d != line count %d", name, len(got.styles), len(got.frame.Lines))
		}
	}
	assertSame("hub")
	app.SetMessage("saved")
	assertSame("message")
	app.ShowModal(NewConfirm("Title", []string{"body"}, func(bool) {}))
	assertSame("modal")
}

// TestComposeFrameTrustedBannerStripsUntrusted pins the round-5 strip
// ordering fix (re-review seat 4): the banner block is trusted (our own art
// code) and must pass through composeFrame verbatim with its SGR sequences
// intact, while every untrusted block (title, body, modal, message) is
// stripped as it is appended. Before the fix the final stripFrameLines ran
// over the assembled lines and deleted the banner's own escape bytes,
// printing visible junk like "[38;5;75m" on color consoles.
func TestComposeFrameTrustedBannerStripsUntrusted(t *testing.T) {
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{
		title: "Pro\x1b[31mviders",
		body:  []string{"row\x1b[7mone", "▶ \x1b[1mmarked"},
	})
	app.SetMessage("sa\x1b[5mved")
	banner := []string{"\x1b[38;5;75m▄\x1b[0m", "wordmark"}

	pf := composeFrame(app, banner)
	if pf.frame.Trusted != len(banner) {
		t.Fatalf("Trusted = %d, want %d (banner line count)", pf.frame.Trusted, len(banner))
	}
	for i, want := range banner {
		if pf.frame.Lines[i] != want {
			t.Fatalf("banner line %d altered by the strip:\n got %q\nwant %q", i, pf.frame.Lines[i], want)
		}
	}
	tail := strings.Join(pf.frame.Lines[len(banner):], "\n")
	if strings.Contains(tail, "\x1b") {
		t.Fatalf("untrusted content kept escape bytes:\n%q", tail)
	}
	// The printable text of the dirty lines survives (stripControl removes
	// only control bytes, so the bracket junk of a killed escape stays
	// visible); only the escape bytes go.
	for _, want := range []string{"Pro[31mviders", "row[7mone", "sa[5mved"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("strip deleted printable content %q:\n%q", want, tail)
		}
	}
	// Height truncation clamps the trusted prefix, never exceeds it.
	short := NewApp(80, 1, DirLTR)
	short.Push(&stubScreen{title: "t"})
	pf = composeFrame(short, banner)
	if pf.frame.Trusted != 1 {
		t.Fatalf("truncated frame Trusted = %d, want 1", pf.frame.Trusted)
	}
}

// TestPainterKeepsTrustedBannerEscapes pins the painter half of the
// round-5 fix: Frame.Trusted leading lines pass through the final sink
// strip verbatim, the untrusted tail is still stripped at the sink even
// when the composer is bypassed, and the plain tier stays byte-clean.
func TestPainterKeepsTrustedBannerEscapes(t *testing.T) {
	for _, tier := range []Tier{TierTruecolor, Tier256, Tier16} {
		var out bytes.Buffer
		p := NewPainter(&out, Cap{Tier: tier, Palette: DefaultPalette()})
		f := Frame{Width: 40, Height: 2, Dir: DirLTR, Trusted: 1, CaretLine: -1, CaretCell: -1, Lines: []string{
			"\x1b[38;5;75m▄\x1b[0m",
			"evil\x1b[31m injection",
		}}
		if err := p.Paint(f, nil); err != nil {
			t.Fatalf("Paint tier %v: %v", tier, err)
		}
		if !strings.Contains(out.String(), "\x1b[38;5;75m") {
			t.Fatalf("tier %v: trusted banner escape stripped:\n%q", tier, out.String())
		}
		if strings.Contains(out.String(), "\x1b[31m") {
			t.Fatalf("tier %v: untrusted escape reached the terminal:\n%q", tier, out.String())
		}
	}
	// The bannerLines contract that makes the trusted prefix safe on a
	// plain-tier console: the banner art at TierPlain is the ASCII figure,
	// zero escape bytes by construction.
	for _, l := range bannerLines(Cap{Tier: TierPlain, Width: 100, Height: 30, Palette: DefaultPalette()}, "tag") {
		if strings.Contains(l, "\x1b") {
			t.Fatalf("plain-tier banner carries escapes: %q", l)
		}
	}
}

// TestRunSetupSuppressedBannerColorCapNoEscapes pins the C3 pipe policy for
// a host that reports color support but suppresses the banner (non-console
// stdout): the banner never reaches composition, so the output carries zero
// escape bytes even though a color tier was detected.
func TestRunSetupSuppressedBannerColorCapNoEscapes(t *testing.T) {
	appTestHome(t, "en")
	cap := Cap{Tier: Tier256, Width: 100, Height: 30, Palette: DefaultPalette(), SuppressBanner: true}
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, cap); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("suppressed-banner run emitted escape bytes:\n%q", out.String())
	}
	if strings.Contains(out.String(), i18n.Load("en").T("banner.tagline")) {
		t.Fatalf("suppressed-banner run rendered the banner:\n%s", out.String())
	}
}

// hubRowLabels returns the nine hub row labels of the en bundle, in display
// order, for the first-frame completeness pins. The dashboard row has no
// catalog entry yet (S3 owns the catalog pass), so its label is the hub's own
// English stand-in.
func hubRowLabels() []string {
	b := i18n.Load("en")
	return []string{
		b.T("menu.providers"),
		b.T("menu.presets"),
		b.T("menu.prompts"),
		b.T("menu.settings"),
		b.T("menu.doctor"),
		b.T("menu.help"),
		b.T("view.appearance.language.label"),
		missingOr(b, "menu.dashboard", missingMenuDashboard),
		b.T("menu.quit"),
	}
}

// roleOfLine returns the palette role painted for one exact frame line.
func roleOfLine(t *testing.T, pf paintedFrame, line string) Role {
	t.Helper()
	for i, l := range pf.frame.Lines {
		if l == line {
			if i >= len(pf.styles) {
				t.Fatalf("line %q has no style slot", line)
			}
			return pf.styles[i]
		}
	}
	t.Fatalf("line %q is not in the composed frame:\n%s", line, pf.frame.String())
	return RoleNone
}

// TestFirstFrameKeepsHubRowsAndFooter pins the first-frame height budget
// (UX audit C2-F5/C1-F15): at 80x24 and 100x22 the banner must be the compact
// header and the frame must carry every hub row, the list hint, and the base
// footer legend. Before the ladder fix the 11-line block pushed the tail off
// the frame and the height cut ate the chrome, so the very first screen of the
// setup flow shipped without the exit hint.
func TestFirstFrameKeepsHubRowsAndFooter(t *testing.T) {
	for _, geom := range []struct{ w, h int }{{80, 24}, {100, 22}} {
		t.Run(fmt.Sprintf("%dx%d", geom.w, geom.h), func(t *testing.T) {
			appTestHome(t, "en")
			cap := Cap{Tier: Tier256, Width: geom.w, Height: geom.h, Palette: DefaultPalette()}
			app := assembleSetupApp(cap)
			banner := bannerLines(cap, i18n.Load("en").T("banner.tagline"))
			if len(banner) != 3 {
				t.Fatalf("%dx%d must use the compact banner, got %d lines", geom.w, geom.h, len(banner))
			}
			pf := composeFrame(app, banner)
			if got := len(pf.frame.Lines); got > geom.h {
				t.Fatalf("first frame = %d lines, want <= %d:\n%s", got, geom.h, pf.frame.String())
			}
			joined := pf.frame.String()
			for _, label := range hubRowLabels() {
				if !strings.Contains(joined, label) {
					t.Fatalf("%dx%d first frame lost hub row %q:\n%s", geom.w, geom.h, label, joined)
				}
			}
			if !strings.Contains(joined, baseFooter) {
				t.Fatalf("%dx%d first frame lost the footer legend:\n%s", geom.w, geom.h, joined)
			}
			if !strings.Contains(joined, "digits 1-9 select+activate") {
				t.Fatalf("%dx%d first frame lost the list hint:\n%s", geom.w, geom.h, joined)
			}
		})
	}
}

// TestComposeFrameKeepsLiveModalOnFrame pins the C1-F6 blocker at the
// composition level: with a body that fills the page, a live M4 gate must be
// laid out on the frame with its title, its whole summary, and its legend.
// Before the body reservation the modal was appended after the full body and
// then cut from the bottom, so the only providers.json write gate was
// invisible while it swallowed every key except y/n/Esc.
func TestComposeFrameKeepsLiveModalOnFrame(t *testing.T) {
	for _, geom := range []struct{ w, h int }{{80, 24}, {80, 25}, {80, 30}, {80, 34}} {
		t.Run(fmt.Sprintf("%dx%d", geom.w, geom.h), func(t *testing.T) {
			app := NewApp(geom.w, geom.h, DirLTR)
			body := make([]string, 31)
			for i := range body {
				body[i] = fmt.Sprintf("[%2d] provider-%d", i+1, i+1)
			}
			app.Push(&stubScreen{title: "Providers", body: body})
			summary := []string{
				"registry entry provider-01",
				"models 3 -> 0",
				"atomic write, mode 0600",
				"removed: provider provider-01 (https://example.com, 3 models)",
			}
			m := NewApproval("Delete provider", summary, func(bool) {})
			app.ShowModal(m)
			pf := composeFrame(app, nil)
			if got := len(pf.frame.Lines); got > geom.h {
				t.Fatalf("frame = %d lines, want <= %d", got, geom.h)
			}
			joined := pf.frame.String()
			want := append([]string{m.Title()}, append(append([]string{}, summary...), m.Footer())...)
			for _, line := range want {
				if !strings.Contains(joined, line) {
					t.Fatalf("modal line %q is off-frame:\n%s", line, joined)
				}
			}
			if got := roleOfLine(t, pf, m.Title()); got != RoleWarn {
				t.Fatalf("modal title role = %v, want RoleWarn", got)
			}
			if got := roleOfLine(t, pf, m.Footer()); got != RoleMuted {
				t.Fatalf("modal legend role = %v, want RoleMuted", got)
			}
		})
	}
}

// writeProviderRegistry rewrites providers.json in the test home with n
// providers of three models each, enough rows to fill the providers page at
// 80x24 (the audit's repro shape).
func writeProviderRegistry(t *testing.T, home string, n int) {
	t.Helper()
	reg := &providers.Registry{}
	for i := 1; i <= n; i++ {
		reg.Providers = append(reg.Providers, providers.Provider{
			Name:      fmt.Sprintf("provider-%02d", i),
			BaseURL:   "https://example.com",
			APIKeyEnv: fmt.Sprintf("PROVIDER_%02d_KEY", i),
			Models:    []providers.Model{{ID: "model-a"}, {ID: "model-b"}, {ID: "model-c"}},
		})
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "providers.json"), data, 0o600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
}

// TestProvidersApprovalGateVisibleAt80x24 pins the audit's exact provider
// repro (C1-F6): ten providers fill the page at 80x24, End+Enter opens the M4
// delete gate, and the composed frame must carry the gate title, every summary
// line, and the legend. Before the fix the frame held no modal line at all.
// Since unit P1-1 inverted the row layout the Delete row acts on the sticky
// target, so the run first rests the cursor on provider 1 (KeyHome) and only
// then jumps to the last row.
func TestProvidersApprovalGateVisibleAt80x24(t *testing.T) {
	home := appTestHome(t, "en")
	writeProviderRegistry(t, home, 10)

	app := assembleSetupApp(Cap{Tier: TierPlain, Width: 80, Height: 24, Palette: DefaultPalette()})
	// The loop paints before it reads (paintLoop order), and the hub builds its
	// rows on the first Draw, so compose once before the first key.
	_ = composeFrame(app, nil)
	app.Step(Digit(1))
	if _, ok := app.Top().(*ProvidersScreen); !ok {
		t.Fatalf("hub row 1 did not open the providers screen; top = %T", app.Top())
	}
	app.Step(KeyHome) // rest the cursor on provider 1: the sticky target
	app.Step(KeyEnd)  // jump to the last row: the Delete verb
	app.Step(KeyEnter)
	m := app.Modal()
	if m == nil {
		t.Fatal("Enter on the last delete row did not open the approval gate")
	}
	pf := composeFrame(app, nil)
	if got := len(pf.frame.Lines); got > 24 {
		t.Fatalf("gate frame = %d lines, want <= 24", got)
	}
	joined := pf.frame.String()
	if !strings.Contains(joined, m.Title()) {
		t.Fatalf("providers.json gate title is off-frame:\n%s", joined)
	}
	if !strings.Contains(joined, m.Footer()) {
		t.Fatalf("providers.json gate legend is off-frame:\n%s", joined)
	}
	// The shared base footer is the frame's last line on every variant, the
	// approval gate included (phase 3 review item 4: only the Confirm twin
	// pinned it).
	if !strings.Contains(joined, baseFooter) {
		t.Fatalf("providers.json gate frame lost the shared base footer:\n%s", joined)
	}
	for _, line := range m.Body(80) {
		// The composition choke point clamps an over-wide summary line to the
		// frame (F-2), so the frame carries the clamped form of every body line.
		if want := cellwidth.Truncate(line, 80); !strings.Contains(joined, want) {
			t.Fatalf("approval summary line %q is off-frame:\n%s", want, joined)
		}
	}
}

// TestSettingsConfirmGateVisibleAt80x24 pins the settings half of C1-F6: the
// 11-item Launch section fills 80x24, the Profile Mode row opens its numbered
// choice, and picking a value arms the danger confirm. The confirm must be
// fully on frame.
func TestSettingsConfirmGateVisibleAt80x24(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(Cap{Tier: TierPlain, Width: 80, Height: 24, Palette: DefaultPalette()})
	_ = composeFrame(app, nil) // first paint builds the hub rows (paintLoop order)
	app.Step(Digit(4))
	if _, ok := app.Top().(*settingsScreen); !ok {
		t.Fatalf("hub row 4 did not open the settings screen; top = %T", app.Top())
	}
	app.Step(Digit(2)) // Profile Mode: a danger item with choices
	if app.Modal() == nil {
		t.Fatal("the Profile Mode row did not open its choice overlay")
	}
	app.Step(Digit(1)) // pick a value: the danger item arms the y/n confirm
	m := app.Modal()
	if m == nil {
		t.Fatal("the danger item did not arm the confirm gate")
	}
	pf := composeFrame(app, nil)
	if got := len(pf.frame.Lines); got > 24 {
		t.Fatalf("gate frame = %d lines, want <= 24", got)
	}
	joined := pf.frame.String()
	if !strings.Contains(joined, m.Title()) {
		t.Fatalf("settings confirm title is off-frame:\n%s", joined)
	}
	if !strings.Contains(joined, m.Footer()) {
		t.Fatalf("settings confirm legend is off-frame:\n%s", joined)
	}
}

// TestComposeFrameClampsModalWidth pins UX audit C1-F22: a modal summary line
// is caller text and arrives verbatim (the approval summary is byte-stable by
// contract), so the painter adapter must fit it to the frame. At width 80 no
// composed line may exceed 80 cells, and benign short lines must survive
// unchanged.
func TestComposeFrameClampsModalWidth(t *testing.T) {
	const w, h = 80, 30
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: "Providers"})
	short := "models 3 -> 0"
	long := "provider provider-01 (https://example.com, 3 models) " + strings.Repeat("x", 100)
	summary := []string{short, long}
	m := NewApproval("Delete provider", summary, func(bool) {})
	app.ShowModal(m)

	// The pre-truncation slice stays byte-stable: "the diff the user approved
	// is the diff shown" is a confirm.go contract, and the pin lives on the
	// slice, not on the frame.
	if got := m.Body(w)[1]; got != long {
		t.Fatalf("Approval.Body mutated the summary:\n got %q\nwant %q", got, long)
	}

	pf := composeFrame(app, nil)
	for i, line := range pf.frame.Lines {
		if cw := cellwidth.String(line); cw > w {
			t.Fatalf("line %d = %d cells, want <= %d: %q", i, cw, w, line)
		}
	}
	joined := pf.frame.String()
	if !strings.Contains(joined, short) {
		t.Fatalf("benign short modal line changed:\n%s", joined)
	}
	if strings.Contains(joined, long) {
		t.Fatalf("over-wide modal line reached the frame:\n%s", joined)
	}
	if !strings.Contains(joined, cellwidth.Truncate(long, w)) {
		t.Fatalf("over-wide modal line was not truncated to the frame:\n%s", joined)
	}
}

// TestMessageKindRoles pins user decision D7-7B: the palette authors Success
// and Danger roles, and a success message, an error message, and a neutral
// note must compose with three different roles. The plain tier still emits
// zero escape bytes while classifying the same way.
func TestMessageKindRoles(t *testing.T) {
	appTestHome(t, "en")
	app := assembleSetupApp(plainTestCap())

	successLine := "[SUCCESS] Language = English"
	errorLine := "Error: state.json is unreadable"
	neutralLine := "Delete canceled. providers.json unchanged."

	app.SetMessage(successLine)
	if got := roleOfLine(t, composeFrame(app, nil), successLine); got != RoleSuccess {
		t.Fatalf("success message role = %v, want RoleSuccess", got)
	}
	app.SetMessage(errorLine)
	if got := roleOfLine(t, composeFrame(app, nil), errorLine); got != RoleDanger {
		t.Fatalf("error message role = %v, want RoleDanger", got)
	}
	app.SetMessage(neutralLine)
	if got := roleOfLine(t, composeFrame(app, nil), neutralLine); got != RoleWarn {
		t.Fatalf("neutral message role = %v, want RoleWarn", got)
	}

	// The translated catalog labels classify too, so a screen that switches
	// from the English fallbacks to common.success/common.error keeps its
	// color.
	SetLanguage("ko")
	defer SetLanguage("en")
	ko := i18n.Load("ko")
	koSuccess := ko.T("common.success") + ": 언어 = 한국어"
	koError := ko.T("common.error") + ": 상태 파일을 읽을 수 없습니다"
	app.SetMessage(koSuccess)
	if got := roleOfLine(t, composeFrame(app, nil), koSuccess); got != RoleSuccess {
		t.Fatalf("localized success role = %v, want RoleSuccess", got)
	}
	app.SetMessage(koError)
	if got := roleOfLine(t, composeFrame(app, nil), koError); got != RoleDanger {
		t.Fatalf("localized error role = %v, want RoleDanger", got)
	}

	// Plain tier: the same roles resolve to no bytes at all.
	app.SetMessage(successLine)
	var out bytes.Buffer
	p := NewPainter(&out, plainTestCap())
	if code := paintLoop(app, &ScriptedKeys{}, p, nil); code != 0 {
		t.Fatalf("paintLoop exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain-tier paint emitted escape bytes:\n%q", out.String())
	}
}

// resizeFixtureKeys is a KeySource that implements the console resize hook and
// fires it between deliveries, at the point ConsoleKeys.ReadKey reports a
// latched WINDOW_BUFFER_SIZE_EVENT (the record is consumed and reading
// continues, so the loop sees the next key in the same call).
type resizeFixtureKeys struct {
	keys   []Key
	pos    int
	fireAt int
	hook   func()
	fired  bool
}

// SetResizeHook installs the hook the production console reader exposes.
func (k *resizeFixtureKeys) SetResizeHook(f func()) { k.hook = f }

// ReadKey delivers the next key, firing the latched resize first.
func (k *resizeFixtureKeys) ReadKey() (Key, bool) {
	if !k.fired && k.pos == k.fireAt {
		k.fired = true
		if k.hook != nil {
			k.hook()
		}
	}
	if k.pos >= len(k.keys) {
		return "", false
	}
	key := k.keys[k.pos]
	k.pos++
	return key, true
}

// TestResizeHookAppliesGeometryAndClears pins user decision 8B: a latched
// WINDOW_BUFFER_SIZE_EVENT re-queries the console size, updates the app
// geometry, re-picks the banner variant, and emits exactly one full clear
// before the next repaint. The trigger is the hook ConsoleKeys fires from
// ReadKey; the payload is never trusted as geometry, only the re-query.
func TestResizeHookAppliesGeometryAndClears(t *testing.T) {
	appTestHome(t, "en")
	app := NewApp(80, 24, DirLTR)
	wide := strings.Repeat("w", 100)
	app.Push(&stubScreen{title: "Resize", body: []string{wide}})

	var out bytes.Buffer
	p := NewPainter(&out, Cap{Tier: Tier256, Width: 80, Height: 24, Palette: DefaultPalette()})
	keys := &resizeFixtureKeys{keys: []Key{RuneKey('x'), KeyCtrlC}, fireAt: 0}
	s := newPaintSession(app, keys, p, &out, p.Cap(), i18n.Load("en").T("banner.tagline"))
	s.size = func() (int, int, bool) { return 120, 40, true }
	s.wireResize()
	if keys.hook == nil {
		t.Fatal("the console key source never received a resize hook")
	}
	if got := len(s.banner); got != 3 {
		t.Fatalf("pre-resize banner = %d lines, want the 3-line compact header", got)
	}
	if code := s.run(); code != 0 {
		t.Fatalf("paint session exit = %d, want 0", code)
	}
	if app.Width() != 120 || app.Height() != 40 {
		t.Fatalf("geometry after resize = %dx%d, want 120x40", app.Width(), app.Height())
	}
	if !strings.Contains(strings.Join(s.banner, "\n"), "▄") {
		t.Fatalf("banner variant was not re-picked for the new geometry: %q", s.banner)
	}
	if got := strings.Count(out.String(), clearScreenSeq); got != 1 {
		t.Fatalf("full clears = %d, want exactly 1", got)
	}
	if strings.Index(out.String(), clearScreenSeq) > strings.Index(out.String(), wide) {
		t.Fatalf("clear landed after the new frame instead of before it:\n%q", out.String())
	}
	if !strings.Contains(out.String(), wide) {
		t.Fatalf("the frame after the resize did not use the new width:\n%q", out.String())
	}
}

// TestResizeOnPlainTierEmitsNoEscapes pins the tier guard on the resize clear:
// a plain-tier console (NO_COLOR, VT-enable failure) may not receive the
// clear's escape bytes even though the geometry still updates.
func TestResizeOnPlainTierEmitsNoEscapes(t *testing.T) {
	appTestHome(t, "en")
	app := NewApp(80, 24, DirLTR)
	app.Push(&stubScreen{title: "Resize"})

	var out bytes.Buffer
	cap := Cap{Tier: TierPlain, Width: 80, Height: 24, Palette: DefaultPalette()}
	p := NewPainter(&out, cap)
	keys := &resizeFixtureKeys{keys: []Key{RuneKey('x'), KeyCtrlC}, fireAt: 0}
	s := newPaintSession(app, keys, p, &out, cap, "")
	s.size = func() (int, int, bool) { return 100, 30, true }
	s.wireResize()
	if code := s.run(); code != 0 {
		t.Fatalf("paint session exit = %d, want 0", code)
	}
	if app.Width() != 100 || app.Height() != 30 {
		t.Fatalf("geometry after resize = %dx%d, want 100x30", app.Width(), app.Height())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain-tier resize emitted escape bytes:\n%q", out.String())
	}
}

// TestPlainRunHasNoResizeArtifacts pins the non-console path: a suppressed
// banner run never installs a resize hook (its key source has none) and never
// writes the clear sequence.
func TestPlainRunHasNoResizeArtifacts(t *testing.T) {
	appTestHome(t, "en")
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if strings.Contains(out.String(), clearScreenSeq) || strings.Contains(out.String(), "\x1b") {
		t.Fatalf("plain run emitted console control bytes:\n%q", out.String())
	}
}

// TestFirstFrameKeepsStartupWarningAt80x24 pins the corrupt-preset warning
// half of the height budget (security review seat 4 MEDIUM 2 x UX audit
// C2-F5): the message line rides the same first frame as the banner, and at
// 80x24 it must survive with the chrome instead of being cut off the bottom.
func TestFirstFrameKeepsStartupWarningAt80x24(t *testing.T) {
	home := appTestHome(t, "en")
	if err := os.WriteFile(filepath.Join(home, "fanout-presets.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt presets: %v", err)
	}
	cap := Cap{Tier: Tier256, Width: 80, Height: 24, Palette: DefaultPalette()}
	app := assembleSetupApp(cap)
	pf := composeFrame(app, bannerLines(cap, i18n.Load("en").T("banner.tagline")))
	if got := len(pf.frame.Lines); got > 24 {
		t.Fatalf("first frame = %d lines, want <= 24", got)
	}
	joined := pf.frame.String()
	if !strings.Contains(joined, "fanout-presets.json is corrupt") {
		t.Fatalf("startup warning is off-frame:\n%s", joined)
	}
	if !strings.Contains(joined, baseFooter) {
		t.Fatalf("startup warning frame lost the footer legend:\n%s", joined)
	}
}

// TestComposeFrameModalReplacesBodyWhenNothingFits pins the other half of the
// C1-F6 fix direction: when the modal needs the whole non-chrome budget, the
// body is dropped and the modal takes its place, instead of the modal falling
// off the frame. The limited body is what the audit demands ("the modal is
// always fully visible with its title and legend"), not a body remnant.
func TestComposeFrameModalReplacesBodyWhenNothingFits(t *testing.T) {
	const w, h = 80, 24
	app := NewApp(w, h, DirLTR)
	body := []string{"BODY-ROW-ONE", "BODY-ROW-TWO"}
	app.Push(&stubScreen{title: "Providers", body: body})
	summary := make([]string, 0, 18)
	for i := 1; i <= 18; i++ {
		summary = append(summary, fmt.Sprintf("summary line %02d", i))
	}
	// 18 summary lines + title + legend = 20; with the title bar, the footer,
	// and two separators that is the full 24-row frame, so nothing is left for
	// the body.
	m := NewApproval("Delete provider", summary, func(bool) {})
	app.ShowModal(m)
	pf := composeFrame(app, nil)
	if got := len(pf.frame.Lines); got > h {
		t.Fatalf("frame = %d lines, want <= %d", got, h)
	}
	joined := pf.frame.String()
	for _, line := range body {
		if strings.Contains(joined, line) {
			t.Fatalf("body line %q survived although the modal needed its rows:\n%s", line, joined)
		}
	}
	want := append([]string{m.Title()}, append(append([]string{}, summary...), m.Footer())...)
	for _, line := range want {
		if !strings.Contains(joined, line) {
			t.Fatalf("modal line %q is off-frame:\n%s", line, joined)
		}
	}
	if !strings.Contains(joined, baseFooter) {
		t.Fatalf("modal frame lost the base footer:\n%s", joined)
	}
}

// TestCollapsedModalLegendKeepsMutedRole pins the degenerate-geometry half of
// the modal role map: when the cut ladder has taken the modal title, the
// BlockModal run collapses to the legend alone, and that lone line must
// compose RoleMuted (the legend), not the title's RoleWarn. Content stays
// visible either way; only the role is pinned.
func TestCollapsedModalLegendKeepsMutedRole(t *testing.T) {
	const w, h = 80, 3
	app := NewApp(w, h, DirLTR)
	app.Push(&stubScreen{title: "Providers", body: []string{"BODY-ROW-ONE", "BODY-ROW-TWO"}})
	summary := make([]string, 0, 18)
	for i := 1; i <= 18; i++ {
		summary = append(summary, fmt.Sprintf("summary line %02d", i))
	}
	m := NewApproval("Delete provider", summary, func(bool) {})
	app.ShowModal(m)
	pf := composeFrame(app, nil)
	if got := len(pf.frame.Lines); got > h {
		t.Fatalf("frame = %d lines, want <= %d", got, h)
	}
	joined := pf.frame.String()
	if !strings.Contains(joined, m.Footer()) {
		t.Fatalf("collapsed modal lost the legend:\n%s", joined)
	}
	legendIdx := -1
	for i, line := range pf.frame.Lines {
		if line == m.Footer() {
			legendIdx = i
		}
	}
	if legendIdx < 0 {
		t.Fatalf("legend line not found verbatim:\n%s", joined)
	}
	if role := pf.styles[legendIdx]; role != RoleMuted {
		t.Fatalf("collapsed legend role = %v, want RoleMuted", role)
	}
}

// TestOverHeightFrameTruncatesBodyNotChrome pins the second half of the
// first-frame budget rule (UX audit C2-F5): when a frame cannot hold
// everything, the body absorbs the cut and the title and footer legend survive.
// Two geometries cover it since the hub inventory grew to nine rows (P5-2 S2
// added the Dashboard row):
//
//   - 80x16, the one-line wordmark leaving 15 rows: the cut reaches the body
//     tail, so the hint AND the last rows go, while the chrome stays.
//   - 80x18, the smallest wordmark frame that still holds the whole inventory:
//     the cut takes only the body hint, so every row survives.
func TestOverHeightFrameTruncatesBodyNotChrome(t *testing.T) {
	for _, geom := range []struct {
		h       int
		allRows bool
	}{{16, false}, {18, true}} {
		t.Run(fmt.Sprintf("80x%d", geom.h), func(t *testing.T) {
			appTestHome(t, "en")
			cap := Cap{Tier: TierPlain, Width: 80, Height: geom.h, Palette: DefaultPalette()}
			app := assembleSetupApp(cap)
			banner := bannerFor(80, geom.h, cap, i18n.Load("en").T("banner.tagline"))
			if len(banner) != 1 {
				t.Fatalf("%d rows must use the one-line wordmark, got %d lines", geom.h, len(banner))
			}
			pf := composeFrame(app, banner)
			if got := len(pf.frame.Lines); got > geom.h {
				t.Fatalf("frame = %d lines, want <= %d", got, geom.h)
			}
			joined := pf.frame.String()
			if !strings.Contains(joined, i18n.Load("en").T("menu.title")) {
				t.Fatalf("over-height frame lost the screen title:\n%s", joined)
			}
			if !strings.Contains(joined, baseFooter) {
				t.Fatalf("over-height frame lost the footer legend (chrome must survive):\n%s", joined)
			}
			labels := hubRowLabels()
			if geom.allRows {
				for _, label := range labels {
					if !strings.Contains(joined, label) {
						t.Fatalf("over-height frame lost hub row %q before the hint:\n%s", label, joined)
					}
				}
			} else {
				// The 15-row body cannot hold nine rows plus the summary, so the
				// tail goes; the head rows must still be there.
				for _, label := range labels[:5] {
					if !strings.Contains(joined, label) {
						t.Fatalf("over-height frame lost hub row %q:\n%s", label, joined)
					}
				}
			}
			if strings.Contains(joined, "digits 1-9 select+activate") {
				t.Fatalf("the body hint survived although the frame is over height:\n%s", joined)
			}
		})
	}
}

// TestComposeFrameCarriesCaretToPainter pins the adapter hop the production
// console path takes: composeFrame rebuilds the Frame field by field, so a
// caret field dropped there would never reach the painter, and the whole
// in-line caret would be dead code on a real console. The test composes a
// caret screen, checks the adapter kept the coordinates, and paints the
// result to prove the glyph lands on the composed line.
func TestComposeFrameCarriesCaretToPainter(t *testing.T) {
	app := NewApp(20, 6, DirLTR)
	app.Push(&caretTestScreen{title: "Editor", body: []string{"aa", "bb"}, line: 1, cell: 1, live: true})
	pf := composeFrame(app, nil)

	if pf.frame.CaretLine != 3 || pf.frame.CaretCell != 1 {
		t.Fatalf("adapter frame caret = (%d,%d), want (3,1):\n%s",
			pf.frame.CaretLine, pf.frame.CaretCell, pf.frame.String())
	}
	if got := pf.styles[pf.frame.CaretLine]; got != RoleNone {
		t.Fatalf("caret line role = %v, want RoleNone for a plain body row", got)
	}

	var out bytes.Buffer
	if err := NewPainter(&out, Cap{Tier: Tier16, Palette: DefaultPalette()}).Paint(pf.frame, pf.styles); err != nil {
		t.Fatalf("Paint: %v", err)
	}
	if want := "b\x1b[7mb\x1b[27m"; !strings.Contains(out.String(), want) {
		t.Fatalf("painted frame lost the caret glyph %q:\n%q", want, out.String())
	}
	if got := strings.Count(out.String(), "\x1b[7m"); got != 1 {
		t.Fatalf("caret SGR count = %d, want exactly 1:\n%q", got, out.String())
	}
}

// TestRunSetupCorruptSwarmStoreWarnsOnMessageLine pins the F-3 startup wiring:
// a corrupt swarm-state.json must surface its warning on the user-visible
// message line of the first frame, the same first-frame treatment the fanout
// preset file gets, instead of vanishing into the store's os.Stderr sink where
// the full-screen frame cannot show it.
func TestRunSetupCorruptSwarmStoreWarnsOnMessageLine(t *testing.T) {
	home := appTestHome(t, "en")
	if err := os.WriteFile(filepath.Join(home, "swarm-state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatalf("write corrupt swarm state: %v", err)
	}
	var out, errb bytes.Buffer
	if code := runSetup(strings.NewReader(""), &out, &errb, plainTestCap()); code != 0 {
		t.Fatalf("runSetup exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "swarm-state.json is corrupt") {
		t.Fatalf("corrupt swarm-state warning missing from the message line:\n%s", out.String())
	}
}
