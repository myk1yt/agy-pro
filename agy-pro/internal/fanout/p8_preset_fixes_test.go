package fanout

import (
	"strings"
	"testing"

	"agypro/internal/testfixtures"
)

// standingPresetPrivilegeEgressNotice is the test-side fixed-standing-rung
// entry point to presetPrivilegeEgressNotice. R3-F-07c moved it out of cli.go:
// production calls the helper directly with its fromDefaultKey flag, so the
// wrapper lived in production code with test-only callers.
func standingPresetPrivilegeEgressNotice(preset *Preset, src map[string]string) string {
	return presetPrivilegeEgressNotice(preset, src, true)
}

func TestP8NoticeMasksEndpointAndPathComponents(t *testing.T) {
	token := testfixtures.SKLive
	for _, tc := range []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "bare scalar", endpoint: token, want: `search_endpoint="sk_li****"`},
		{name: "URL path", endpoint: "https://search.example/" + token + "/{query}", want: `search_endpoint="https://search.example/sk_li****/{query}"`},
		{name: "non-secret query name", endpoint: "https://search.example/{query}?key=" + token, want: `search_endpoint="https://search.example/{query}?key=sk_li****"`},
		{name: "dot slash costume", endpoint: "https://search.example/./" + token + "/x", want: `search_endpoint="https://search.example/./sk_li****/x"`},
		{name: "Windows costume", endpoint: `https://search.example/C:\` + token, want: `search_endpoint="https://search.example/C:\\sk_li****"`},
		{name: "userinfo", endpoint: "https://user:" + token + "@search.example/{query}", want: `search_endpoint="https://search.example/{query}"`},
		{name: "scheme costume", endpoint: "scheme://" + token, want: `search_endpoint="scheme://sk_li****"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notice := standingPresetPrivilegeEgressNotice(&Preset{Name: BuiltinPresetName, SearchEndpoint: tc.endpoint}, map[string]string{"search": "preset"})
			if strings.Contains(notice, token) {
				t.Errorf("notice leaks the planted credential: %q", notice)
			}
			if !strings.Contains(notice, tc.want) {
				t.Errorf("notice = %q; want %s", notice, tc.want)
			}
			if !strings.Contains(notice, `preset "(built-in defaults)"`) {
				t.Errorf("notice lost the byte-identical built-in label: %q", notice)
			}
		})
	}

	benign := "https://search.example/api/v1/search?query={query}"
	notice := standingPresetPrivilegeEgressNotice(&Preset{Name: BuiltinPresetName, SearchEndpoint: benign}, map[string]string{"search": "preset"})
	if !strings.Contains(notice, `search_endpoint="`+benign+`"`) {
		t.Errorf("benign endpoint was over-masked: %q", notice)
	}

	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "benign path stays readable", value: "/workspace/review", want: "/workspace/review"},
		{name: "bare path token", value: token, want: "sk_li****"},
		{name: "dot slash path costume", value: "./" + token + "/x", want: "./sk_li****/x"},
		{name: "Windows path costume", value: `C:\` + token, want: `C:\sk_li****`},
		{name: "scheme path costume", value: "scheme://" + token, want: "scheme://sk_li****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := maskPresetNoticeOperand("root", tc.value); got != tc.want {
				t.Errorf("maskPresetNoticeOperand(root) = %q; want %q", got, tc.want)
			}
			if got := maskPresetNoticeOperand("out_dir", tc.value); got != tc.want {
				t.Errorf("maskPresetNoticeOperand(out_dir) = %q; want %q", got, tc.want)
			}
		})
	}
}
