package fanout

import "testing"

func TestP28EndpointPreSchemeCredentialMasks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"template-prefix", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY https://host/{query}"},
		{"scheme-position", "Tr7Um2Vk9Xw4/Qd8Bn5Jz1Ls6/Gh3Pf0Cy7Rv2://host"},
		{"single-token-prefix", "Tr7Um2Vk9Xw4Qd8B https://host/{query}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := maskPresetNoticeOperand("search_endpoint", tc.value)
			if got == tc.value {
				t.Fatalf("pre-scheme credential rendered unchanged: %q", got)
			}
			if p25PlaintextRunAtFloor(got) {
				t.Fatalf("raw leak window survived: %q", got)
			}
			if p25PlaintextRunAtFloorNormalized(got) {
				t.Fatalf("normalized leak window survived: %q", got)
			}
			if p27FragLeak(tc.value, got) {
				t.Fatalf("credential fragment survived: %q", got)
			}
		})
	}
}

func TestP28EndpointLegacyRendersUnchanged(t *testing.T) {
	f18 := "sk_" + "live_1234567890abcdef"
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"benign-kubernetes", "https://api.example.com/kubernetesmanifests", "https://api.example.com/kubernetesmanifests"},
		{"benign-query", "https://search.example/{query}", "https://search.example/{query}"},
		{"benign-invalid-query", "https://search.invalid/{query}", "https://search.invalid/{query}"},
		{"benign-completions", "https://api.example.com/v1/completions", "https://api.example.com/v1/completions"},
		{"masked-path", "https://search.example/" + f18 + "/{query}", "https://search.example/sk_li****/{query}"},
		{"masked-query-pair", "https://search.invalid/" + f18 + "?" + f18 + "EXTRA={query}", "https://search.invalid/sk_li****?sk_li****={query}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertEqual(t, maskPresetNoticeOperand("search_endpoint", tc.value), tc.want)
		})
	}
}

func TestP28DisclosedResidual_R11MaskedButLeakyEndpoint(t *testing.T) {
	const value = `C:\ws\abcdefghij1234\klmnopqrst5678`
	got := maskPresetNoticeOperand("search_endpoint", value)
	assertEqual(t, got, `ws\abcdefghij1234\klmnopqrst5678`)
	if !p25PlaintextRunAtFloorNormalized(got) {
		t.Fatalf("R11 pin must retain its measured normalized 32-rune window: %q", got)
	}
}

func TestP28DisclosedResidual_Class1EndpointAndInteriorDecoration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		knob  string
		value string
	}{
		{"interior-decoration", "out_dir", "vault: kqmxzptvrw/wqnrbxykum"},
		{"endpoint", "search_endpoint", "https://api.example.com/qmxzptvrwqnbx/wqnbxykumvtrs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertEqual(t, maskPresetNoticeOperand(tc.knob, tc.value), tc.value)
		})
	}
}

func TestP28DisclosedResidual_F9CSubFloorFragments(t *testing.T) {
	const value = "key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	for _, tc := range []struct {
		knob string
		want string
	}{
		{"root", "key=w****/K7MDENG/bPxRf****"},
		{"out_dir", "key=w****/K7MDENG/bPxRf****"},
		{"search_endpoint", "key=w****"},
	} {
		t.Run(tc.knob, func(t *testing.T) {
			assertEqual(t, maskPresetNoticeOperand(tc.knob, value), tc.want)
		})
	}
}

func TestP28DefaultPresetEchoRecordsAbsentKey(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{Presets: []Preset{{
		Name: BuiltinPresetName, Seats: "native:no-key",
	}}})
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	want := "fanout: seat lineup from preset \"(built-in defaults)\" (default_preset rung, no key set): 1 seat(s), 0 metered provider seats (native:no-key)\n"
	assertEqual(t, stderr.String(), want)
}

func TestP28DefaultPresetEchoKeySetUnchanged(t *testing.T) {
	home, stderr := ladderEnv(t)
	savePresetsFile(t, home, &PresetsFile{
		DefaultPreset: "keyed",
		Presets:       []Preset{{Name: "keyed", Seats: "native:keyed"}},
	})
	if _, err := buildRunConfig(ladderFlags(stderr), "p"); err != nil {
		t.Fatalf("buildRunConfig: %v", err)
	}
	want := "fanout: seat lineup from preset \"keyed\" (default_preset rung): 1 seat(s), 0 metered provider seats (native:keyed)\n"
	assertEqual(t, stderr.String(), want)
}

func assertEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("render drifted\n got: %q\nwant: %q", got, want)
	}
}
