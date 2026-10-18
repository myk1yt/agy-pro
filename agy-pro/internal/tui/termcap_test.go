package tui

import (
	"errors"
	"testing"
)

// fakeProbes builds a probes seam with scripted answers for every ladder
// rung. Restores counts how many times setMode was called and with which
// mode, so tests can pin the restore path.
type fakeProbes struct {
	p        probes
	console  bool
	vtErr    error
	origMode uint32
	sz       [2]int
	sizeOK   bool
	restores []uint32
}

func newFakeProbes(env map[string]string) *fakeProbes {
	f := &fakeProbes{origMode: 7, sz: [2]int{100, 40}, sizeOK: true}
	getenv := func(k string) string { return env[k] }
	f.p = probes{
		isConsole: func() bool { return f.console },
		enableVT:  func() (uint32, error) { return f.origMode, f.vtErr },
		setMode:   func(m uint32) { f.restores = append(f.restores, m) },
		size:      func() (int, int, bool) { return f.sz[0], f.sz[1], f.sizeOK },
		getenv:    getenv,
	}
	return f
}

func TestDetectCap_Ladder(t *testing.T) {
	errDownLevel := errors.New("ERROR_INVALID_PARAMETER (87)")
	cases := []struct {
		name string
		env  map[string]string
		// script
		console  bool
		vtErr    error
		sizeFail bool
		// expectations
		tier           Tier
		suppressBanner bool
		w, h           int
	}{
		{
			name:           "stdout not a console forces plain and suppresses banner",
			env:            map[string]string{"WT_SESSION": "x"},
			tier:           TierPlain,
			suppressBanner: true,
		},
		{
			name:    "NO_COLOR set and non-empty forces plain",
			env:     map[string]string{"NO_COLOR": "1", "WT_SESSION": "x"},
			console: true,
			tier:    TierPlain,
			w:       100, h: 40,
		},
		{
			name:    "NO_COLOR empty string does not force plain",
			env:     map[string]string{"NO_COLOR": "", "WT_SESSION": "x"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "AGY_PRO_COLOR forces truecolor past NO_COLOR",
			env:     map[string]string{"NO_COLOR": "1", "AGY_PRO_COLOR": "truecolor"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "VT probe failure forces plain",
			env:     map[string]string{"WT_SESSION": "x"},
			console: true, vtErr: errDownLevel,
			tier: TierPlain,
		}, {
			name:    "VT probe failure beats a forced tier",
			env:     map[string]string{"AGY_PRO_COLOR": "truecolor"},
			console: true, vtErr: errDownLevel,
			tier: TierPlain,
		},
		{
			name:    "WT_SESSION picks truecolor",
			env:     map[string]string{"WT_SESSION": "abc"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "COLORTERM truecolor picks truecolor",
			env:     map[string]string{"COLORTERM": "truecolor"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "COLORTERM 24bit picks truecolor",
			env:     map[string]string{"COLORTERM": "24bit"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "ConEmuANSI ON lands on 256",
			env:     map[string]string{"ConEmuANSI": "ON"},
			console: true,
			tier:    Tier256,
			w:       100, h: 40,
		},
		{
			name:    "no env signal lands on 256 (honest conhost default)",
			env:     map[string]string{},
			console: true,
			tier:    Tier256,
			w:       100, h: 40,
		},
		{
			name:    "AGY_PRO_COLOR forces 16 past WT_SESSION",
			env:     map[string]string{"WT_SESSION": "x", "AGY_PRO_COLOR": "16"},
			console: true,
			tier:    Tier16,
			w:       100, h: 40,
		},
		{
			name:    "AGY_PRO_COLOR forces 256",
			env:     map[string]string{"AGY_PRO_COLOR": "256"},
			console: true,
			tier:    Tier256,
			w:       100, h: 40,
		},
		{
			name:    "AGY_PRO_COLOR off forces plain",
			env:     map[string]string{"WT_SESSION": "x", "AGY_PRO_COLOR": "off"},
			console: true,
			tier:    TierPlain,
			w:       100, h: 40,
		},
		{
			name:    "bogus AGY_PRO_COLOR falls back to the ladder",
			env:     map[string]string{"WT_SESSION": "x", "AGY_PRO_COLOR": "yes"},
			console: true,
			tier:    TierTruecolor,
			w:       100, h: 40,
		},
		{
			name:    "size probe failure falls back to 80x24",
			env:     map[string]string{},
			console: true, sizeFail: true,
			tier: Tier256,
			w:    80, h: 24,
		},
		{
			name:           "AGY_PRO_COLOR override does not bypass NO_CONSOLE",
			env:            map[string]string{"AGY_PRO_COLOR": "truecolor"},
			console:        false,
			tier:           TierPlain,
			suppressBanner: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeProbes(c.env)
			f.console = c.console
			f.vtErr = c.vtErr
			f.sizeOK = !c.sizeFail
			got := detectCap(f.p)
			if got.Tier != c.tier {
				t.Errorf("Tier = %v; want %v", got.Tier, c.tier)
			}
			if got.SuppressBanner != c.suppressBanner {
				t.Errorf("SuppressBanner = %v; want %v", got.SuppressBanner, c.suppressBanner)
			}
			if c.tier == TierPlain {
				return // size is only resolved on the VT-capable path
			}
			if got.Width != c.w || got.Height != c.h {
				t.Errorf("size = %dx%d; want %dx%d", got.Width, got.Height, c.w, c.h)
			}
		})
	}
}

func TestDetectCap_RestoreOriginalMode(t *testing.T) {
	f := newFakeProbes(map[string]string{"WT_SESSION": "x"})
	f.console = true
	cap := detectCap(f.p)
	if len(f.restores) != 0 {
		t.Fatalf("setMode called during detect; restores = %v", f.restores)
	}
	cap.Restore()
	cap.Restore() // must stay safe on repeat
	if len(f.restores) != 1 || f.restores[0] != f.origMode {
		t.Errorf("restores = %v; want one call with mode %d", f.restores, f.origMode)
	}
}

func TestDetectCap_PlainRestoreIsNoop(t *testing.T) {
	f := newFakeProbes(map[string]string{})
	cap := detectCap(f.p) // not a console: no VT was enabled
	cap.Restore()
	if len(f.restores) != 0 {
		t.Errorf("plain cap must not touch the console mode; restores = %v", f.restores)
	}
}

func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		TierPlain:     "plain",
		Tier16:        "16",
		Tier256:       "256",
		TierTruecolor: "truecolor",
	}
	for tier, want := range cases {
		if tier.String() != want {
			t.Errorf("Tier(%d).String() = %q; want %q", int(tier), tier.String(), want)
		}
	}
}

func TestColorEmitsOnlyActiveTierColumn(t *testing.T) {
	c := DefaultPalette().Title
	if got, want := c.FG(TierTruecolor), "\x1b[38;2;78;156;255m"; got != want {
		t.Errorf("FG(truecolor) = %q; want %q", got, want)
	}
	if got, want := c.BG(TierTruecolor), "\x1b[48;2;78;156;255m"; got != want {
		t.Errorf("BG(truecolor) = %q; want %q", got, want)
	}
	if got, want := c.FG(Tier256), "\x1b[38;5;111m"; got != want {
		t.Errorf("FG(256) = %q; want %q", got, want)
	}
	if got, want := c.BG(Tier256), "\x1b[48;5;111m"; got != want {
		t.Errorf("BG(256) = %q; want %q", got, want)
	}
	if got, want := c.FG(Tier16), "\x1b[94m"; got != want {
		t.Errorf("FG(16) = %q; want %q", got, want)
	}
	if got, want := c.BG(Tier16), "\x1b[104m"; got != want {
		t.Errorf("BG(16) = %q; want %q", got, want)
	}
	if c.FG(TierPlain) != "" || c.BG(TierPlain) != "" {
		t.Error("plain tier must emit zero escape bytes")
	}
}

func TestPaletteRolesDistinctAndComplete(t *testing.T) {
	p := DefaultPalette()
	roles := []Role{RoleTitle, RoleAccent, RoleSuccess, RoleWarn, RoleDanger, RoleMuted, RoleSelection}
	seen := map[Color]Role{}
	for _, r := range roles {
		c := p.Role(r)
		if c.SGR == 0 {
			t.Errorf("role %d has no authored 16-color SGR", int(r))
		}
		if prev, dup := seen[c]; dup {
			t.Errorf("roles %d and %d share one color triple", int(prev), int(r))
		}
		seen[c] = r
	}
	if got := p.Role(RoleNone); got != (Color{}) {
		t.Errorf("RoleNone must resolve to the zero Color; got %+v", got)
	}
}
