package openaiup

import (
	"strings"

	"agypro/internal/providers"
)

// ValidReasoningEfforts enumerates the supported reasoning effort levels.
var ValidReasoningEfforts = map[string]bool{
	"default": true,
	"none":    true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

// NormalizeReasoningEffort sanitizes reasoning effort strings, defaulting to "default".
func NormalizeReasoningEffort(effort string) string {
	cleaned := strings.ToLower(strings.TrimSpace(effort))
	if cleaned == "" {
		return "default"
	}
	if ValidReasoningEfforts[cleaned] {
		return cleaned
	}
	return "default"
}

// MapReasoningEffort standardizes the reasoning effort value for OpenAI-compatible APIs:
// "default" or empty: omitted (uses server default)
// "none": explicitly set to "none"
// "low", "medium", "high": forwarded directly
// "xhigh", "max": mapped to "high"
func MapReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none":
		return "none"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "high"
	case "default", "":
		return ""
	default:
		return ""
	}
}

// ApplyEffort gates the reasoning_effort field of an outbound body:
//   - a model that does not support thinking loses the field entirely, so
//     strict providers (OpenAI, DeepSeek, GLM) do not reject the request,
//   - effort "default" or "none" also loses it, so the upstream applies its
//     native behavior instead of rejecting an unknown value,
//   - any other effort is set as-is.
//
// effort must already be normalized (see NormalizeReasoningEffort).
func ApplyEffort(body map[string]any, caps providers.Capabilities, effort string) {
	if !caps.SupportsThinking || effort == "default" || effort == "none" {
		delete(body, "reasoning_effort")
		return
	}
	body["reasoning_effort"] = effort
}
