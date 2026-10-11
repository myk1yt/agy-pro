package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"agypro/internal/providers"
)

func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		input    int
		expected string
	}{
		{-1, "max"},
		{0, "max"},
		{1048576, "1M"},
		{2097152, "2M"},
		{131072, "128k"},
		{128000, "128k"},
		{200000, "200k"},
		{4096, "4k"},
		{8192, "8k"},
		{500, "500"},
	}

	for _, c := range cases {
		got := formatTokenCount(c.input)
		if got != c.expected {
			t.Errorf("formatTokenCount(%d) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestPrintConfiguredProvidersAndModels(t *testing.T) {
	t.Setenv("MOCK_NW_KEY", "dummy-nw-token")

	reg := &providers.Registry{
		Providers: []providers.Provider{
			{
				Name:      "NW",
				BaseURL:   "https://api.neuralwatt.com/v1",
				APIKeyEnv: "MOCK_NW_KEY",
				Models: []providers.Model{
					{
						ID:              "deepseek-v4.1-flash-flex",
						Family:          "deepseek",
						ContextLength:   1048576,
						MaxOutputTokens: -1,
						Capabilities: providers.Capabilities{
							SupportsTools:    true,
							SupportsThinking: true,
							ReasoningEffort:  "high",
						},
					},
					{
						ID:              "glm-5.3-flash-flex",
						Family:          "glm",
						ContextLength:   1048576,
						MaxOutputTokens: -1,
						Capabilities: providers.Capabilities{
							SupportsTools:    true,
							SupportsThinking: true,
							ReasoningEffort:  "high",
						},
					},
				},
			},
		},
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printConfiguredProvidersAndModels(reg)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	io.Copy(&buf, r)
	output := buf.String()

	expectedProviderLine := "[1] NW (https://api.neuralwatt.com/v1) [API Key: Configured]"
	expectedModel1 := "- deepseek-v4.1-flash-flex  [Family: deepseek | Context: 1M | MaxOutput: max | Tools: YES | Thinking: YES (high)]"
	expectedModel2 := "- glm-5.3-flash-flex        [Family: glm      | Context: 1M | MaxOutput: max | Tools: YES | Thinking: YES (high)]"

	if !strings.Contains(output, expectedProviderLine) {
		t.Errorf("missing expected provider line in output:\nGot:\n%s\nWant line:\n%s", output, expectedProviderLine)
	}
	if !strings.Contains(output, expectedModel1) {
		t.Errorf("missing expected model 1 line in output:\nGot:\n%s\nWant line:\n%s", output, expectedModel1)
	}
	if !strings.Contains(output, expectedModel2) {
		t.Errorf("missing expected model 2 line in output:\nGot:\n%s\nWant line:\n%s", output, expectedModel2)
	}
}
