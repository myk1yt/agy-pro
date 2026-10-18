package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestBannerLadderPicksVariantByGeometry pins the sizing ladder: full spark
// block on roomy consoles, the compact header when rows are short, the
// one-line wordmark on narrow ones, and nothing when the banner is
// suppressed (pipes stay byte-clean).
func TestBannerLadderPicksVariantByGeometry(t *testing.T) {
	tag := i18n.Load("en").T("banner.tagline")
	full := bannerLines(Cap{Tier: Tier256, Width: 100, Height: 30, Palette: DefaultPalette()}, tag)
	if len(full) == 0 || !strings.Contains(strings.Join(full, "\n"), "▄") {
		t.Fatalf("roomy console must get the full spark block: %q", full)
	}
	compact := bannerLines(Cap{Tier: Tier256, Width: 40, Height: 10, Palette: DefaultPalette()}, tag)
	joined := strings.Join(compact, "\n")
	if strings.Contains(joined, "▄") || !strings.Contains(joined, "█") {
		t.Fatalf("short console must get the compact gradient header: %q", compact)
	}
	word := bannerLines(Cap{Tier: Tier256, Width: 20, Height: 10, Palette: DefaultPalette()}, tag)
	if len(word) != 1 {
		t.Fatalf("narrow console must get the one-line wordmark: %q", word)
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
		f := Frame{Width: 40, Height: 2, Dir: DirLTR, Trusted: 1, Lines: []string{
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
