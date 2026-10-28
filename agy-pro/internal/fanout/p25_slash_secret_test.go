package fanout

import (
	"strings"
	"testing"

	"agypro/internal/cellwidth"
	"agypro/internal/providers"
	"agypro/internal/testfixtures"
)

func TestP25SlashCredentialMasksBeforeLocationSplit(t *testing.T) {
	plant := testfixtures.AWSSecretExample
	if got := len([]rune(plant)); got != 40 {
		t.Fatalf("plant length = %d runes, want 40", got)
	}
	if !providers.SecretShaped(plant) {
		t.Fatal("plant must satisfy the whole-value credential authority")
	}
	for _, tc := range []struct {
		name  string
		knob  string
		value string
	}{
		{name: "root", knob: "root", value: plant},
		{name: "out_dir", knob: "out_dir", value: plant},
		{name: "search_endpoint", knob: "search_endpoint", value: "https://" + plant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := maskPresetNoticeOperand(tc.knob, tc.value)
			if got != "wJalr****" {
				t.Fatalf("maskPresetNoticeOperand(%s) = %q, want whole-value mask", tc.knob, got)
			}
			if strings.Contains(got, "K7MDENG") || p25PlaintextRunAtFloor(got) {
				t.Fatalf("maskPresetNoticeOperand(%s) left a plaintext run: %q", tc.knob, got)
			}

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
			if !strings.Contains(notice, tc.knob+`="wJalr****"`) || strings.Contains(notice, "K7MDENG") {
				t.Fatalf("preset notice did not render the whole-value mask: %q", notice)
			}
		})
	}
}

func TestP25BenignLocationCorpusByteIdentity(t *testing.T) {
	const opaqueShapedDrivePath = "C:/Users/k1yt/reports/fanout/20261026"
	if !providers.SecretShaped(opaqueShapedDrivePath) {
		t.Fatal("drive-path fixture must exercise the SecretShaped over-mask guard")
	}
	for _, tc := range []struct {
		name  string
		knob  string
		value string
	}{
		{name: "workspace", knob: "root", value: "/workspace/review"},
		{name: "windows backslash", knob: "out_dir", value: `C:\Users\k1yt\proj`},
		{name: "windows forward slash opaque shape", knob: "root", value: opaqueShapedDrivePath},
		{name: "windows executable", knob: "out_dir", value: "C:/Users/test/AppData/Local/agy-pro/bin/agy.exe"},
		{name: "UNC", knob: "root", value: `\\server\share\review`},
		{name: "relative", knob: "root", value: "./models/anthropic/claude-3.5-sonnet"},
		{name: "relative output", knob: "out_dir", value: "reports/review"},
		{name: "round six URL template", knob: "search_endpoint", value: "https://api.example.com/res/v1/web/search?q={query}&count=10"},
		{name: "search template", knob: "search_endpoint", value: "https://search.example/api/v1/search?query={query}"},
		{name: "short URL", knob: "search_endpoint", value: "https://example.com/v1"},
		{name: "long URL", knob: "search_endpoint", value: "https://api.example.com/v1/completions"},
		{name: "query placeholder", knob: "search_endpoint", value: "https://search.example/{query}"},
		{name: "POSIX projects", knob: "root", value: "/data/projects/antigravity"},
		{name: "POSIX absolute opaque shape", knob: "root", value: "/data/reports/fanout/20261026"},
		{name: "relative dated output", knob: "out_dir", value: "reports/20261026"},
		{name: "relative run output", knob: "root", value: "out/run20261026"},
		{name: "Windows roaming profile", knob: "root", value: "C:/Users/k1yt/AppData/Roaming/agy"},
		{name: "dot-slash relative output", knob: "out_dir", value: "./reports/review"},
		{name: "dot-slash dated output", knob: "out_dir", value: "./reports/20261026"},
		{name: "dot-slash run output", knob: "root", value: "./out/run20261026"},
		{name: "file relative output", knob: "out_dir", value: "file://reports/review"},
		{name: "file dated output", knob: "out_dir", value: "file://reports/20261026"},
		{name: "file run output", knob: "root", value: "file://out/run20261026"},
		{name: "file POSIX workspace", knob: "root", value: "file:///workspace/review"},
		{name: "file POSIX projects", knob: "root", value: "file:///data/projects/antigravity"},
		{name: "file POSIX opaque shape", knob: "root", value: "file:///data/reports/fanout/20261026"},
		{name: "file Windows opaque shape", knob: "root", value: "file:///C:/Users/k1yt/reports/fanout/20261026"},
		{name: "file Windows roaming profile", knob: "root", value: "file:///C:/Users/k1yt/AppData/Roaming/agy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskPresetNoticeOperand(tc.knob, tc.value); got != tc.value {
				t.Fatalf("benign value changed\n got: %q\nwant: %q", got, tc.value)
			}
		})
	}
}

func TestP26LocationCostumedSlashCredentialsClosed(t *testing.T) {
	plant := testfixtures.AWSSecretExample
	for _, tc := range []struct {
		class string
		value string
	}{
		{class: "bare", value: plant},
		{class: "dot-slash", value: "./" + plant},
		{class: "drive-forward", value: "C:/ws/" + plant},
		{class: "drive-backslash", value: `C:\ws\` + strings.ReplaceAll(plant, "/", `\`)},
		{class: "scheme", value: "https://host/" + plant},
	} {
		for _, knob := range []string{"root", "out_dir", "search_endpoint"} {
			t.Run(knob+"/"+tc.class, func(t *testing.T) {
				got := maskPresetNoticeOperand(knob, tc.value)
				if !strings.Contains(got, "****") || p25PlaintextRunAtFloor(got) ||
					strings.Contains(got, "wJalrXUtnFEMI") || strings.Contains(got, "K7MDENG") ||
					strings.Contains(got, "bPxRfiCYEXAMPLEKEY") {
					t.Fatalf("credential plant survived masking: %q", got)
				}

				preset := Preset{}
				sources := map[string]string{}
				switch knob {
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
				notice := presetPrivilegeEgressNotice(&preset, sources, false)
				// P27 port: the F-9C layout moved the label clause behind the
				// knobs, so the old "knobs: " split no longer isolates the
				// operand region. Scope the leak metric to the clause region
				// (everything before the label trailer) exactly as the old
				// split did — the notice's own prose ("privilege/egress", whose
				// "/" is a metric run member) must stay outside the scan.
				clauseRegion := notice
				if i := strings.Index(notice, " (privilege/egress knobs:"); i >= 0 {
					clauseRegion = notice[:i]
				}
				if p25PlaintextRunAtFloor(clauseRegion) ||
					strings.Contains(notice, "wJalrXUtnFEMI") || strings.Contains(notice, "K7MDENG") ||
					strings.Contains(notice, "bPxRfiCYEXAMPLEKEY") {
					t.Fatalf("notice leaked a credential component: %q", notice)
				}
				if strings.ContainsAny(notice, "\r\n\u2028\u2029") {
					t.Fatalf("notice is not one escaped line: %q", notice)
				}
				truncated := cellwidth.Truncate(notice, 80)
				if operative := knob + "="; !strings.Contains(truncated, operative) {
					t.Fatalf("80-column rendering loses %q: %q", operative, truncated)
				}
				t.Logf("%s=%q notice80=%q", knob, got, truncated)
			})
		}
	}
}

func TestP25PrefixOverlapPositionalMaskingRemains(t *testing.T) {
	plant := "sk_" + "live_1234567890abcdef"
	for _, tc := range []struct {
		name  string
		knob  string
		value string
	}{
		{name: "path", knob: "root", value: plant + "/" + plant + "EXTRA"},
		{name: "URL", knob: "search_endpoint", value: "https://search.invalid/" + plant + "?" + plant + "EXTRA={query}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := maskPresetNoticeOperand(tc.knob, tc.value)
			if strings.Contains(got, "EXTRA") || strings.Contains(got, plant) {
				t.Fatalf("prefix sibling leaked its tail: %q", got)
			}
			if strings.Count(got, "sk_li****") != 2 {
				t.Fatalf("both components must be masked positionally: %q", got)
			}
		})
	}
}

func p25PlaintextRunAtFloor(value string) bool {
	n := 0
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=':
			n++
			if n >= 16 {
				return true
			}
		default:
			n = 0
		}
	}
	return false
}
