package openaiup

import (
	"testing"

	"agypro/internal/providers"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "default"},
		{"   ", "default"},
		{"  HIGH ", "high"},
		{"none", "none"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"max", "max"},
		{"bogus", "default"},
	}
	for _, c := range cases {
		if got := NormalizeReasoningEffort(c.in); got != c.want {
			t.Errorf("NormalizeReasoningEffort(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestValidReasoningEfforts(t *testing.T) {
	for _, effort := range []string{"default", "none", "low", "medium", "high", "xhigh", "max"} {
		if !ValidReasoningEfforts[effort] {
			t.Errorf("ValidReasoningEfforts[%q] = false; want true", effort)
		}
	}
	for _, effort := range []string{"", "off", "minimal"} {
		if ValidReasoningEfforts[effort] {
			t.Errorf("ValidReasoningEfforts[%q] = true; want false", effort)
		}
	}
}

// TestMapReasoningEffort is the fanout table, moved here with the mapping.
func TestMapReasoningEffort(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"default", ""},
		{"", ""},
		{"none", "none"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "high"},
		{"UNKNOWN", ""},
	}
	for _, c := range cases {
		got := MapReasoningEffort(c.input)
		if got != c.expected {
			t.Errorf("MapReasoningEffort(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestApplyEffort(t *testing.T) {
	thinking := providers.Capabilities{SupportsThinking: true}
	noThinking := providers.Capabilities{}

	cases := []struct {
		name   string
		caps   providers.Capabilities
		effort string // already normalized by the caller
		start  any    // pre-existing body value; nil means the key is absent
		want   any    // expected body value; nil means the key must be absent
	}{
		{"thinking off strips the field", noThinking, "high", nil, nil},
		{"thinking off strips a stale value", noThinking, "high", "high", nil},
		{"default strips the field", thinking, "default", nil, nil},
		{"none strips the field", thinking, "none", nil, nil},
		{"low is kept", thinking, "low", nil, "low"},
		{"medium is kept", thinking, "medium", nil, "medium"},
		{"high is kept", thinking, "high", nil, "high"},
		{"stale value replaced", thinking, "xhigh", "low", "xhigh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := map[string]any{"model": "m"}
			if c.start != nil {
				body["reasoning_effort"] = c.start
			}
			ApplyEffort(body, c.caps, c.effort)
			got, present := body["reasoning_effort"]
			if c.want == nil {
				if present {
					t.Errorf("reasoning_effort = %v; want the key deleted", got)
				}
				return
			}
			if !present {
				t.Fatalf("reasoning_effort missing; want %v", c.want)
			}
			if got != c.want {
				t.Errorf("reasoning_effort = %v; want %v", got, c.want)
			}
			if body["model"] != "m" {
				t.Error("ApplyEffort must not touch unrelated body fields")
			}
		})
	}
}
