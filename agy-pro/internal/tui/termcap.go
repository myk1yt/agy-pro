// termcap.go is the capability core of the setup TUI: the color tier enum,
// the palette role table, the Cap object, and the detection ladder. The
// ladder is platform-neutral; every probe behind it is injected through the
// probes seam so tests can drive each rung without a live console. The
// per-GOOS default probes live in console_windows.go / console_other.go.
//
// Detection order (decided once per process by the TUI entry, plan v2 §2):
//  1. stdout not a console -> TierPlain and SuppressBanner (zero banner and
//     zero ANSI bytes on pipes and files).
//  2. NO_COLOR set and non-empty -> TierPlain, unless AGY_PRO_COLOR forces
//     a tier (the standard's override clause).
//  3. VT probe: GetConsoleMode then SetConsoleMode(+VT); failure (down-level
//     host, ERROR_INVALID_PARAMETER) -> TierPlain. On success the original
//     mode is remembered and restored through Cap.Restore on exit.
//  4. Tier pick: WT_SESSION -> truecolor; else COLORTERM in {truecolor,
//     24bit} -> truecolor; else (ConEmuANSI=ON or plain conhost) -> 256.
//  5. AGY_PRO_COLOR in {truecolor,256,16,off} forces the tier after step 2.

package tui

import (
	"errors"
	"strconv"
	"sync"
)

// ErrNotConsole reports that a file is not a usable Windows console handle.
// OpenConsoleKeys returns it (and Detect degrades through it) when raw
// console access is unavailable; callers treat it as the line-mode degrade
// signal, never as a crash.
var ErrNotConsole = errors.New("tui: not a console")

// Tier is a color capability tier. The renderer emits only the emission
// column the tier names; plain means no escape bytes of any kind.
type Tier int

const (
	TierPlain Tier = iota
	Tier16
	Tier256
	TierTruecolor
)

// String names the tier for diagnostics and the AGY_PRO_COLOR=off log line.
func (t Tier) String() string {
	switch t {
	case Tier16:
		return "16"
	case Tier256:
		return "256"
	case TierTruecolor:
		return "truecolor"
	default:
		return "plain"
	}
}

// Color is one authored color in all three emission columns: 24-bit RGB,
// 256-palette index, and the 16-color SGR foreground parameter (30-37 or
// 90-97). Every palette entry is authored once as this triple; FG and BG
// emit only the active tier's column and return "" for TierPlain, so a
// plain-tier caller can compose unconditionally and still emit zero bytes.
type Color struct {
	R, G, B uint8
	X256    uint8
	SGR     uint8 // foreground parameter, e.g. 94 (bright blue)
}

// FG returns the SGR foreground sequence for the tier, or "" for TierPlain.
func (c Color) FG(t Tier) string {
	switch t {
	case TierTruecolor:
		return "\x1b[38;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B)) + "m"
	case Tier256:
		return "\x1b[38;5;" + strconv.Itoa(int(c.X256)) + "m"
	case Tier16:
		return "\x1b[" + strconv.Itoa(int(c.SGR)) + "m"
	default:
		return ""
	}
}

// BG returns the SGR background sequence for the tier, or "" for TierPlain.
// The 16-color background parameter is the foreground parameter + 10
// (30-37 -> 40-47, 90-97 -> 100-107).
func (c Color) BG(t Tier) string {
	switch t {
	case TierTruecolor:
		return "\x1b[48;2;" + strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B)) + "m"
	case Tier256:
		return "\x1b[48;5;" + strconv.Itoa(int(c.X256)) + "m"
	case Tier16:
		return "\x1b[" + strconv.Itoa(int(c.SGR)+10) + "m"
	default:
		return ""
	}
}

// Role names one chrome color slot of the TUI frame. Screens (P11) tag frame
// lines with roles; the painter resolves them against the active palette.
type Role int

const (
	RoleNone Role = iota
	RoleTitle
	RoleAccent
	RoleSuccess
	RoleWarn
	RoleDanger
	RoleMuted
	RoleSelection
)

// Palette is the chrome color table: every role authored once as a
// {truecolor RGB, 256 index, 16 SGR} triple (plan v2 §2 degrade ladder).
type Palette struct {
	Title     Color
	Accent    Color
	Success   Color
	Warn      Color
	Danger    Color
	Muted     Color
	Selection Color
}

// Role resolves one role constant to its authored triple. RoleNone resolves
// to the zero Color (emits nothing).
func (p Palette) Role(r Role) Color {
	switch r {
	case RoleTitle:
		return p.Title
	case RoleAccent:
		return p.Accent
	case RoleSuccess:
		return p.Success
	case RoleWarn:
		return p.Warn
	case RoleDanger:
		return p.Danger
	case RoleMuted:
		return p.Muted
	case RoleSelection:
		return p.Selection
	default:
		return Color{}
	}
}

// DefaultPalette returns the chrome palette. The 256-column entries are the
// nearest standard-cube/grayscale matches computed once at authoring time;
// the 16-column entries are hand-picked bright variants so chrome stays
// legible on conservative hosts.
func DefaultPalette() Palette {
	return Palette{
		Title:     Color{R: 78, G: 156, B: 255, X256: 111, SGR: 94},  // blue
		Accent:    Color{R: 168, G: 85, B: 247, X256: 141, SGR: 95},  // violet
		Success:   Color{R: 46, G: 204, B: 113, X256: 78, SGR: 92},   // green
		Warn:      Color{R: 245, G: 171, B: 66, X256: 215, SGR: 93},  // amber
		Danger:    Color{R: 239, G: 68, B: 68, X256: 203, SGR: 91},   // red
		Muted:     Color{R: 148, G: 163, B: 184, X256: 247, SGR: 90}, // gray
		Selection: Color{R: 34, G: 211, B: 238, X256: 81, SGR: 96},   // cyan
	}
}

// Cap is the once-per-process capability object: the tier, the console size
// in cells, the chrome palette, and whether the banner must be suppressed
// entirely (non-console stdout; zero banner bytes in pipes). When detection
// enabled VT processing on the console, Restore puts the original console
// mode back; it is a no-op on plain non-console caps.
type Cap struct {
	Tier           Tier
	Width          int
	Height         int
	Palette        Palette
	SuppressBanner bool

	restore func()
}

// Restore returns the console output mode to its pre-detection state. Safe
// to call on any Cap and more than once.
func (c Cap) Restore() {
	if c.restore != nil {
		c.restore()
	}
}

// probes is the injectable detection seam. Each field is one rung of the
// ladder; tests replace them with fakes (console_windows_test.go,
// termcap_test.go).
type probes struct {
	// isConsole reports whether the output handle is a real console.
	isConsole func() bool
	// enableVT opts the console into VT processing and returns the
	// original mode for later restore. Any error (down-level hosts answer
	// ERROR_INVALID_PARAMETER) means no VT.
	enableVT func() (orig uint32, err error)
	// setMode restores a previously remembered console mode.
	setMode func(mode uint32)
	// size queries the console window size in cells.
	size func() (w, h int, ok bool)
	// getenv reads one environment variable ("" when unset).
	getenv func(string) string
}

// detectCap runs the ladder once against the given probes. It is pure with
// respect to the process environment: every OS contact goes through p.
func detectCap(p probes) Cap {
	pal := DefaultPalette()
	if p.isConsole == nil || !p.isConsole() {
		return Cap{Tier: TierPlain, Palette: pal, SuppressBanner: true}
	}
	forced, hasForced := forcedTier(p.getenv("AGY_PRO_COLOR"))
	if p.getenv("NO_COLOR") != "" && !hasForced {
		return Cap{Tier: TierPlain, Palette: pal}
	}
	orig, err := p.enableVT()
	if err != nil {
		// Down-level host (ERROR_INVALID_PARAMETER) or mode change lost:
		// no VT processing, so no escape bytes may be emitted at all.
		return Cap{Tier: TierPlain, Palette: pal}
	}
	restored := false
	cap := Cap{Tier: Tier256, Palette: pal, restore: func() {
		if restored {
			return
		}
		restored = true
		p.setMode(orig)
	}}
	switch {
	case hasForced:
		cap.Tier = forced
	case p.getenv("WT_SESSION") != "":
		cap.Tier = TierTruecolor
	default:
		switch p.getenv("COLORTERM") {
		case "truecolor", "24bit":
			cap.Tier = TierTruecolor
		default:
			// ConEmuANSI=ON and plain modern conhost both land here:
			// VT-capable but no verified truecolor signal, so 256 is the
			// honest default (AGY_PRO_COLOR=truecolor can force tier 4).
			cap.Tier = Tier256
		}
	}
	if w, h, ok := p.size(); ok && w > 0 && h > 0 {
		cap.Width, cap.Height = w, h
	} else {
		cap.Width, cap.Height = 80, 24
	}
	return cap
}

// forcedTier parses the AGY_PRO_COLOR override. Unknown values are ignored
// so a typo falls back to the normal ladder instead of forcing plain.
func forcedTier(v string) (Tier, bool) {
	switch v {
	case "truecolor":
		return TierTruecolor, true
	case "256":
		return Tier256, true
	case "16":
		return Tier16, true
	case "off":
		return TierPlain, true
	}
	return TierPlain, false
}

var (
	detectOnce   sync.Once
	detectResult Cap
)

// Detect runs the ladder once per process against the platform's default
// probes and returns the cached Cap. The TUI entry calls it exactly once and
// passes the result down (seat processes run scrubbed env, so the decision
// must not be re-derived per child).
func Detect() Cap {
	detectOnce.Do(func() { detectResult = detectCap(defaultProbes) })
	return detectResult
}

// defaultProbes is the platform probe set (console_windows.go /
// console_other.go).
