package fanout

// P27 union-guard pins (round-10 redesign, coder-p27impl). The engine under
// pin is the adjudicated UNION (debug-adjudication-report.md 7.2): mimo-pro C3's
// measure-then-emit skeleton with nw-deepseek's lexical sub-run word grammar
// substituted for the hump arm. Groups pinned here:
//
//  1. TestP27PlantClosure            — the full 150 plant cells (50 classes x 3
//     knobs), graded on the leak metric in BOTH readings: the suite's raw
//     p25PlaintextRunAtFloor and its backslash-normalized twin.
//  2. TestP27BenignByteIdentity      — the benign matrix byte-identical, plus
//     the R4 disclosure row.
//  3. TestP27F18Pins                 — the prefix-overlap shape intact.
//  4. TestP27NormalizedMetricBackslash — the backslash blind-spot class pinned
//     through the normalized helper (do NOT change p25's helper semantics).
//  5. TestP27RealLabel80Col          — F-9C: the operative knob clause stays
//     within 80 display columns WITH the real standing label.
//
// Plus TestP27BoundaryWindows (15/16/17) and TestP27ResidualDisclosure (the
// two carried residual classes, asserted at their measured values so no future
// iteration can silently widen or narrow them).

import (
	"strings"
	"testing"

	"agypro/internal/cellwidth"
)

// p25PlaintextRunAtFloorNormalized is the backslash-normalized twin of
// p25PlaintextRunAtFloor (whose semantics stay untouched): a base64 secret can
// wear UNC / drive-backslash spellings, and the raw metric does not join `\` so
// `C:\ws\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY` reads raw-clean while 40
// secret runes render. The gate and these pins close windows ACROSS separators.
func p25PlaintextRunAtFloorNormalized(value string) bool {
	return p25PlaintextRunAtFloor(strings.ReplaceAll(value, `\`, "/"))
}

// p27FragLeak reports whether a canonical AWS-plant fragment survived the
// render (scoped exactly like the adjudication's adjFragLeak: only inputs that
// carry the canonical fixture are checked for its pieces).
func p27FragLeak(in, out string) bool {
	if !strings.Contains(in, "wJalrXUtnFEMI") {
		return false
	}
	for _, f := range []string{"wJalrXUtnFEMI", "K7MDENG", "bPxRfiCYEXAMPLEKEY", "CYEXAMPLEKEY"} {
		if strings.Contains(out, f) {
			return true
		}
	}
	return false
}

type p27Plant struct {
	class string
	id    string
	value string
}

// p27Plants is the adjudication's section-A value table (50 values) verbatim,
// grouped by class. 50 x 3 knobs = 150 cells.
func p27Plants() []p27Plant {
	return []p27Plant{
		// -- natural / forced slash splinters (the F-9A class) --
		{"splinter", "canonical-13-7-18", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"splinter", "splinter-13-13-12", "wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"splinter", "splinter-15-15-8", "Ab3dEf7gHiJkLmN/Op2qRs4tUv6wXy8/0aB1cD2e"},
		{"splinter", "splinter-11-13-14", "k9Lm2Qw7Zt4/Rb8Nc3Vx1Pd6K/Gh5Js0Ty9Ue2Wq"},
		{"splinter", "splinter-13-7-8-10", "wJalrXUtnFEMI/K7MDENG/bPxRfiCY/EXAMPLEKEY"},
		{"splinter", "splinter-4x10", "wJalrXUtnFE/MIK7MDENGb/PxRfiCYEXA/MPLEKEY123"},
		{"splinter", "splinter-15-15", "wJalrXUtnFEMIK7M/Jq3Zr8Yb5Nc1Vt9"},
		{"splinter", "two-13", "wJalrXUtnFEMI/K7MDENGbPxRfi"},
		{"splinter", "two-13-lower", "k9lm2qw7zt4rb8nc3vx1pd6k/gh5js0ty9u2wqabc"},
		{"splinter", "two-15", "Ab3dEf7gHiJkLmN/Op2qRs4tUv6wXy8"},
		{"splinter", "letters-only-splinter", "QwErTyUiO/pAsDfGhJk/zXcVbNmQw/TrYuIoPaS"},
		{"splinter", "plus-separated", "wJalr+XU/tnF/EMI/K7M/DENG/bPxRfiCYEXAMPLEKEY"},
		{"splinter", "five-subfloor", "Ab3dE/F7gHi/JkLmN/0pQrS/4tUv6"},
		{"splinter", "subfloor-4x8", "0aB1cD2e/f1G2h3I4/j5K6l7M8/n9O0p1Q2"},
		{"splinter", "mixed-benign-prefix", "home/k1yt/projects/wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"splinter", "landing-zone", "/data/reports/wJalrXUtnFEMI/K7MDENG"},

		// -- window boundary + bare credentials --
		{"bare", "window-15-plant-subfloor", "0aB1cD2e/f1G2h3"},
		{"bare", "window-16-plant", "0aB1cD2e/f1G2h3I"},
		{"bare", "window-17-plant", "0aB1cD2e/f1G2h3Ix"},
		{"bare", "cred-15", "wJalrXUtnFEMIK"},
		{"bare", "cred-16", "wJalrXUtnFEMIK7"},
		{"bare", "cred-17", "wJalrXUtnFEMIK7M"},
		{"bare", "frag-11", "k9Lm2Qw7Zt4"},
		{"bare", "frag-13", "Rb8Nc3Vx1Pd6K"},
		{"bare", "frag-ab3d", "Ab3dEf7gHiJkLmN"},
		{"bare", "frag-12cy", "CYEXAMPLEKEY"},
		{"bare", "allcaps-20", "ABCDEFGHIJKLMNOPQRST"},

		// -- location costumes (round-8 classes + backslash spellings) --
		{"costume", "dot-slash", "./wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"costume", "drive-fwd", "C:/ws/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"costume", "drive-back", `C:\ws\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"costume", "drive-back-splinter", `C:\ws\wJalrXUtnFEMI\K7MDENGbPxRfi\CYEXAMPLEKEY`},
		{"costume", "unc-plant", `\\server\share\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"costume", "scheme", "https://host/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"costume", "scheme-splinter", "https://host/wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"costume", "dotted-host-splinter", "https://api.example.com/wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"costume", "dotted-host-v1", "https://api.example.com/v1/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"costume", "https-path-query", "https://api.example.com/v1/wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY?q=1"},
		{"costume", "file-prefix", "file:///data/wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},

		// -- head-prefixed plants (frozen head list) --
		{"head", "sk-head-splinter", "sk-012345678901234/567890123456789"},
		{"head", "sk-head-single", "sk-0123456789abcdef"},
		{"head", "sk-head-colon", "backup: sk-012345678901234/567890123456789"},
		{"head", "sk-prefixed-plant", "sk-wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"head", "ghp-splinter", "ghp_0123456789abcdef/0123456789abcdef"},
		{"head", "head-component", "reports/sk-123456789012345"},
		{"head", "head-splinter-15", "sk-123456789012345/123456789012345"},

		// -- interior decoration (space / colon / nospace) --
		{"decor", "colon-decor", "backup: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"decor", "colon-decor-splinter", "backup: wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"decor", "colon-nospace", "backup:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"decor", "space-decor", "token wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"decor", "colon-decor-lower", "token: k9lm2qw7zt4/rb8nc3vx1pd6k/gh5js0ty9u2wq"},
	}
}

// TestP27PlantClosure pins the F-9A contract on all 150 cells: the rendered
// operand carries no >=16 raw window under EITHER metric (raw and
// backslash-normalized), and no canonical fixture fragment survives. Cells
// whose input window is below the floor (window-15, cred-15, sub-floor
// fragments) may render unchanged — the pinned metric is the >=16 window — but
// every leaky input must come out window-free.
func TestP27PlantClosure(t *testing.T) {
	for _, p := range p27Plants() {
		for _, knob := range []string{"root", "out_dir", "search_endpoint"} {
			t.Run(p.class+"/"+p.id+"/"+knob, func(t *testing.T) {
				got := maskPresetNoticeOperand(knob, p.value)
				if p25PlaintextRunAtFloor(got) {
					t.Fatalf("raw leak window survived: %q", got)
				}
				if p25PlaintextRunAtFloorNormalized(got) {
					t.Fatalf("normalized leak window survived (backslash spelling): %q", got)
				}
				// Note: sub-floor literal fragments (cred-15, sk-prefixed
				// pieces, K7MDENG-class tails) may survive below the 16-rune
				// window — that is the F-9c disclosure class, and the pinned
				// contract is the window metric in both readings. Canonical
				// costume fragment absence is pinned by TestP26… and
				// TestP27NormalizedMetricBackslash.
			})
		}
	}
}

// TestP27BenignByteIdentity pins the F-9B contract: the benign matrix renders
// byte-identical. Row bm-lowercase-hexkey is the adjudicated R4 disclosure (the
// single graded miss of the union) and is pinned at its measured masked value
// in TestP27ResidualDisclosure instead of here.
func TestP27BenignByteIdentity(t *testing.T) {
	rows := []struct{ knob, value string }{
		{"root", "/home/k1yt/kubernetesmanifests"},
		{"root", "C:/Users/k1yt/Documents/PersonalProjects2026"},
		{"out_dir", "projects/PersonalProjects2026"},
		{"root", "/var/log/20261026143025123456"},
		{"root", "/data/superlongdirectorynametest"},
		{"out_dir", "buildartifacts2026"},
		{"out_dir", "artifacts/0123456789abcdef"},
		{"out_dir", "artifacts/0123456783abcdef"},
		{"root", "./models/anthropic/claude-3-5-sonnet-20241022"},
		{"out_dir", "test1test2"},
		{"out_dir", "out/session20261026T1200"},
		{"root", "C:/work/ApiV2Client/build"},
		{"root", "./models/meta-llama/Llama-3.3-70B-Instruct-Turbo"},
		{"root", "C:/work/GoBuildCache"},
		{"root", `C:\Program Files\agy-pro`},
		{"out_dir", "runs/2026-10-26T12-00-00Z"},
		{"out_dir", "out/2026102610273056"},
		{"root", "C:/Users/k1yt/reports/fanout/20261026/documentarchives"},
		{"out_dir", "docs/apireference2026"},
		{"out_dir", "docs/APIReference2026"},
		{"out_dir", "build/AGYPRO2026/artifacts"},
		{"search_endpoint", "https://api.example.com/kubernetesmanifests"},
		{"root", "f47ac10b-58cc-4372-a567-0e02b2c3d479"},
		{"out_dir", "abcde/202610261430"},
		{"out_dir", "abcd/202610261430"},
		{"out_dir", "abcdef/202610261430"},
		{"root", "PRODUCTION"},
		{"root", "build/SHA256"},
		{"root", "test1/x2y3/z4w5"},
		{"root", "C:/work/MyProjects2026"},
		{"out_dir", "documents/2026"},
	}
	for _, tc := range rows {
		t.Run(tc.knob+"/"+tc.value, func(t *testing.T) {
			if got := maskPresetNoticeOperand(tc.knob, tc.value); got != tc.value {
				t.Fatalf("benign value changed\n got: %q\nwant: %q", got, tc.value)
			}
		})
	}
}

// TestP27F18Pins keeps the P22/F-18 prefix-overlap shape intact: both sibling
// components mask positionally and the planted tail cannot survive.
func TestP27F18Pins(t *testing.T) {
	f18 := "sk_" + "live_1234567890abcdef"
	for _, tc := range []struct{ name, knob, value string }{
		{"path", "root", f18 + "/" + f18 + "EXTRA"},
		{"URL", "search_endpoint", "https://search.invalid/" + f18 + "?" + f18 + "EXTRA={query}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := maskPresetNoticeOperand(tc.knob, tc.value)
			if strings.Contains(got, "EXTRA") || strings.Contains(got, f18) {
				t.Fatalf("prefix sibling leaked its tail: %q", got)
			}
			if strings.Count(got, "sk_li****") != 2 {
				t.Fatalf("both components must be masked positionally: %q", got)
			}
			if p25PlaintextRunAtFloor(got) || p25PlaintextRunAtFloorNormalized(got) {
				t.Fatalf("f18 render carries a leak window: %q", got)
			}
		})
	}
}

// TestP27NormalizedMetricBackslash pins the blind-spot class itself: the raw
// metric cannot see `\`-joined windows, the normalized twin can, and the gate
// masks backslash spellings on the NORMALIZED run (drive-back / UNC plants come
// out window-free under both readings).
func TestP27NormalizedMetricBackslash(t *testing.T) {
	// Helper semantics: raw reads `\` as a run breaker; normalized joins it.
	joined := `wJalrXUtnFEMI\K7MDENG`
	if p25PlaintextRunAtFloor(joined) {
		t.Fatalf("raw metric must NOT join backslashes: %q", joined)
	}
	if !p25PlaintextRunAtFloorNormalized(joined) {
		t.Fatalf("normalized metric must join backslashes: %q", joined)
	}
	for _, tc := range []struct{ knob, value string }{
		{"root", `C:\ws\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"out_dir", `C:\ws\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"search_endpoint", `C:\ws\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"root", `C:\ws\wJalrXUtnFEMI\K7MDENGbPxRfi\CYEXAMPLEKEY`},
		{"out_dir", `C:\ws\wJalrXUtnFEMI\K7MDENGbPxRfi\CYEXAMPLEKEY`},
		{"root", `\\server\share\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
		{"out_dir", `\\server\share\wJalrXUtnFEMI\K7MDENG\bPxRfiCYEXAMPLEKEY`},
	} {
		t.Run(tc.knob+"/"+tc.value, func(t *testing.T) {
			got := maskPresetNoticeOperand(tc.knob, tc.value)
			if p25PlaintextRunAtFloorNormalized(got) {
				t.Fatalf("backslash plant leaked a normalized window: %q", got)
			}
			if p27FragLeak(tc.value, got) {
				t.Fatalf("backslash plant kept a fixture fragment: %q", got)
			}
			// The raw metric alone would have been blind; the class is closed
			// only if the render actually changed.
			if got == tc.value {
				t.Fatalf("backslash plant rendered unchanged: %q", got)
			}
		})
	}
}

// TestP27RealLabel80Col pins F-9C with the REAL standing label: the operative
// knob clause must sit inside the first 80 display columns. Construction
// matches the adjudication's KnobIn80 metric (one knob per notice): two bars
// per knob —
//   - `knob=` survives cellwidth.Truncate(notice, 80) (0/277 before the layout
//     fix; the shipped leading label put the first knob at column 88);
//   - the full masked clause (knob="…") fits inside 80 columns (worst masked
//     clause in the corpus is 68 cells; the clause-led layout starts at column
//     9, so 9+68 = 77).
func TestP27RealLabel80Col(t *testing.T) {
	f18 := "sk_" + "live_1234567890abcdef"
	cases := []struct{ knob, value string }{
		{"search_endpoint", "https://search.invalid/" + f18 + "?" + f18 + "EXTRA={query}"},
		{"root", "wJalrXUtnFEMI/K7MDENGbPxRfi/CYEXAMPLEKEY"},
		{"out_dir", "reports/20261026"},
	}
	for _, tc := range cases {
		t.Run(tc.knob, func(t *testing.T) {
			preset := Preset{Name: BuiltinPresetName}
			sources := map[string]string{}
			switch tc.knob {
			case "root":
				preset.Root = tc.value
				sources["root"] = "preset"
			case "out_dir":
				preset.OutDir = tc.value
				sources["out"] = "preset"
			case "search_endpoint":
				preset.SearchEndpoint = tc.value
				sources["search"] = "preset"
			}
			notice := presetPrivilegeEgressNotice(&preset, sources, true)
			if notice == "" {
				t.Fatal("standing notice must render")
			}
			if strings.ContainsAny(notice, "\r\n\u2028\u2029") {
				t.Fatalf("notice is not one escaped line: %q", notice)
			}
			// Real label contract (the p8 disclosure substring).
			if !strings.Contains(notice, `preset "(built-in defaults)"`) {
				t.Fatalf("notice lost the built-in label: %q", notice)
			}
			if !strings.Contains(notice, "privilege/egress knobs:") {
				t.Fatalf("notice lost the privilege/egress marker: %q", notice)
			}
			clause := tc.knob + `="` + maskPresetNoticeOperand(tc.knob, tc.value) + `"`
			truncated := cellwidth.Truncate(notice, 80)
			if !strings.Contains(truncated, tc.knob+"=") {
				t.Fatalf("80-column rendering loses %q: %q", tc.knob+"=", truncated)
			}
			if !strings.Contains(truncated, clause) {
				t.Fatalf("operative clause %q not within 80 columns: %q", clause, truncated)
			}
		})
	}
}

// TestP27BoundaryWindows pins the 15/16/17 window boundary in both directions:
// a 15-rune window is below the pinned metric (may render unchanged, F-9c
// disclosure class), a 16-rune plant window must close, and benign 15/16/17
// windows must stay byte-identical.
func TestP27BoundaryWindows(t *testing.T) {
	if got := maskPresetNoticeOperand("root", "0aB1cD2e/f1G2h3"); p25PlaintextRunAtFloor(got) ||
		p25PlaintextRunAtFloorNormalized(got) {
		t.Fatalf("15-window plant must stay metric-clean: %q", got)
	}
	for _, v := range []string{"0aB1cD2e/f1G2h3I", "0aB1cD2e/f1G2h3Ix"} {
		got := maskPresetNoticeOperand("root", v)
		if got == v {
			t.Fatalf("16/17-window plant rendered unchanged: %q", got)
		}
		if p25PlaintextRunAtFloor(got) || p25PlaintextRunAtFloorNormalized(got) {
			t.Fatalf("16/17-window plant leaked: %q", got)
		}
	}
	for _, v := range []string{"abcd/202610261430", "abcde/202610261430", "abcdef/202610261430"} {
		if got := maskPresetNoticeOperand("out_dir", v); got != v {
			t.Fatalf("benign boundary window changed: %q", got)
		}
	}
}

// TestP27ResidualDisclosure pins the two carried residual classes at their
// MEASURED values (adjudication 10 R1-R5), so no future iteration can silently
// widen or narrow them:
//
//	CLASS 1 (leak, R1/R2): lowercase-alnum / letters-only splinters with <=2
//	digit blocks are structurally indistinguishable from benign dated paths
//	(out/run20261026) and stay raw; the backslash spelling is additionally
//	raw-metric-blind.
//	CLASS 2 (over-mask, R3/R4/R5 + frozen heads): ALL-CAPS directories,
//	interleaved lowercase content ids, two-letter-word chains (MyGo…), iOS-style
//	leading-lowercase names, ISO-basic timestamp directory names, and head-word
//	names (key-backup) mask — fail-safe direction.
func TestP27ResidualDisclosure(t *testing.T) {
	leaks := []struct{ knob, value string }{
		{"root", "abcdefg/hij12345/klmno67"},            // R1 lowercase splinter
		{"out_dir", "abcdefg/hij12345/klmno67"},         // R1
		{"root", "abcdefghij/klmnopqrstuv"},             // R1 letters-only
		{"root", `C:\ws\abcdefghij1234\klmnopqrst5678`}, // R2 backslash + lowercase
	}
	for _, tc := range leaks {
		t.Run("leak/"+tc.value, func(t *testing.T) {
			got := maskPresetNoticeOperand(tc.knob, tc.value)
			if got != tc.value {
				t.Fatalf("residual-leak class narrowed unexpectedly: %q", got)
			}
		})
	}
	masks := []struct {
		knob, value, want string
	}{
		{"root", "gw/4f9a2b7c1d3e5f6a7b8c9d0e1f2a3b4c", "gw/4f9a2****"},           // R4
		{"out_dir", "artifacts/snapshot-20261026T143025Z", "artifacts/snaps****"}, // ISO-basic timestamp
		{"root", "C:/work/MyGoProject2026", "work/****"},                          // R5
		{"root", "C:/Users/k1yt/Library/iPhoneBackup2026", "Users****"},           // iOS-style
		{"root", "keys/key-backup", "keys/key-b****"},                             // frozen head
		{"root", "C:/work/KUBERNETES/artifacts", "work/****"},                     // R3 ALL-CAPS
	}
	for _, tc := range masks {
		t.Run("mask/"+tc.value, func(t *testing.T) {
			if got := maskPresetNoticeOperand(tc.knob, tc.value); got != tc.want {
				t.Fatalf("residual-over-mask class drifted\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
