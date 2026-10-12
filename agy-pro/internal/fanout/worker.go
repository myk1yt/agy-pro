package fanout

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// SeatResult holds the outcome of an individual seat's execution.
type SeatResult struct {
	SeatID        int              `json:"seat_id"`
	InitialTarget providers.Target `json:"initial_target"`
	FinalTarget   providers.Target `json:"final_target"`
	Status        string           `json:"status"` // "success", "failed", "degraded"
	Degraded      bool             `json:"degraded"`
	Failovers     []FailoverRecord `json:"failovers,omitempty"`
	Content       string           `json:"content"`
	ToolCallCount int              `json:"tool_call_count"`
	DurationMs    int64            `json:"duration_ms"`
	Error         string           `json:"error,omitempty"`
}

// FailoverRecord records a failover transition.
type FailoverRecord struct {
	Step     int    `json:"step"` // 1 = same model new provider, 2 = diff family new model
	FromProv string `json:"from_prov"`
	FromMod  string `json:"from_mod"`
	ToProv   string `json:"to_prov"`
	ToMod    string `json:"to_mod"`
	Reason   string `json:"reason"`
}

// SeatWorker executes one seat in double-blind isolation with the failover policy.
type SeatWorker struct {
	SeatID               int
	InitialTarget        providers.Target
	Registry             *providers.Registry
	ExcludedFamilies     []string
	Preset               TaskPreset
	UserPrompt           string
	Runner               *agenttools.ToolRunner
	Client               *Client
	MaxTurns             int
	Tools                []agenttools.ToolDef
	AllowToolsForSession bool
}

// Run executes the worker loop with multi-step failover.
// A panic inside one seat (e.g. a tool-layer nil dereference) is recovered here
// and converted into a failed SeatResult so sibling seats keep running and the
// swarm process survives.
func (w *SeatWorker) Run(ctx context.Context) (result *SeatResult) {
	start := time.Now()
	res := &SeatResult{
		SeatID:        w.SeatID,
		InitialTarget: w.InitialTarget,
		FinalTarget:   w.InitialTarget,
		Status:        "success",
	}
	result = res

	currentTarget := w.InitialTarget

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v", r)
			if len(msg) > 200 {
				msg = msg[:200] + "..."
			}
			res.FinalTarget = currentTarget
			res.Status = "failed"
			res.Degraded = true
			res.Error = fmt.Sprintf("panic: %s", msg)
			res.DurationMs = time.Since(start).Milliseconds()
			result = res
		}
	}()

	attemptedProviders := map[string]bool{currentTarget.Provider.Name: true}
	attemptedModels := map[string]bool{currentTarget.Model.ID: true}

	for {
		// Run tool/chat loop for currentTarget
		content, toolCalls, err := w.runChatLoop(ctx, currentTarget)
		if err == nil {
			// Success!
			res.FinalTarget = currentTarget
			res.Content = content
			res.ToolCallCount = toolCalls
			res.DurationMs = time.Since(start).Milliseconds()
			return res
		}

		// Failure occurred: trigger failover policy
		reason := err.Error()

		// Step 1: Try same model ID with another provider
		alts := w.Registry.FindAlternatesForModel(currentTarget.Model.ID, currentTarget.Provider.Name)
		var nextTarget *providers.Target
		for _, alt := range alts {
			if !attemptedProviders[alt.Provider.Name] {
				t := alt
				nextTarget = &t
				attemptedProviders[alt.Provider.Name] = true
				res.Failovers = append(res.Failovers, FailoverRecord{
					Step:     1,
					FromProv: currentTarget.Provider.Name,
					FromMod:  currentTarget.Model.ID,
					ToProv:   alt.Provider.Name,
					ToMod:    alt.Model.ID,
					Reason:   reason,
				})
				res.Degraded = true
				break
			}
		}

		if nextTarget != nil {
			currentTarget = *nextTarget
			continue
		}

		// Step 2: Try different model from a different family
		var avoidFamilies []string
		avoidFamilies = append(avoidFamilies, w.ExcludedFamilies...)
		avoidFamilies = append(avoidFamilies, currentTarget.Model.FamilyName())

		diffAlts := w.Registry.FindAlternatesExcludingFamilies(avoidFamilies)
		for _, alt := range diffAlts {
			if !attemptedModels[alt.Model.ID] && !attemptedProviders[alt.Provider.Name] {
				t := alt
				nextTarget = &t
				attemptedModels[alt.Model.ID] = true
				attemptedProviders[alt.Provider.Name] = true
				res.Failovers = append(res.Failovers, FailoverRecord{
					Step:     2,
					FromProv: currentTarget.Provider.Name,
					FromMod:  currentTarget.Model.ID,
					ToProv:   alt.Provider.Name,
					ToMod:    alt.Model.ID,
					Reason:   reason,
				})
				res.Degraded = true
				break
			}
		}

		if nextTarget != nil {
			currentTarget = *nextTarget
			continue
		}

		// Step 3: All failover paths exhausted! Mark degraded and exclude this seat
		res.FinalTarget = currentTarget
		res.Status = "failed"
		res.Degraded = true
		res.Error = fmt.Sprintf("all failover attempts exhausted; last error: %s", reason)
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}
}

// runChatLoop conducts the conversational tool-calling loop until completion or max turns.
func (w *SeatWorker) runChatLoop(ctx context.Context, target providers.Target) (string, int, error) {
	messages := []ChatMessage{
		{Role: "system", Content: w.Preset.SystemPrompt},
		{Role: "user", Content: w.UserPrompt},
	}

	// Filter tools based on model capabilities
	var effectiveTools []agenttools.ToolDef
	if w.AllowToolsForSession && target.Model.Capabilities.CanUseTools() {
		for _, t := range w.Tools {
			if target.Model.Capabilities.ToolPermitted(t.Function.Name) {
				effectiveTools = append(effectiveTools, t)
			}
		}
	}

	totalToolCalls := 0
	turns := 0
	maxTurns := w.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	for turns < maxTurns {
		select {
		case <-ctx.Done():
			return "", totalToolCalls, ctx.Err()
		default:
		}

		resp, err := w.Client.Complete(ctx, target, messages, effectiveTools)
		if err != nil {
			return "", totalToolCalls, err
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// If model requested tool calls
		if len(assistantMsg.ToolCalls) > 0 {
			messages = append(messages, assistantMsg)

			for _, call := range assistantMsg.ToolCalls {
				totalToolCalls++
				toolName := call.Function.Name
				argsJSON := call.Function.Arguments

				out, _ := w.Runner.Execute(w.SeatID, target.Model.ID, target.Provider.Name, toolName, argsJSON)

				messages = append(messages, ChatMessage{
					Role:       "tool",
					Name:       toolName,
					ToolCallID: call.ID,
					Content:    out,
				})
			}
			turns++
			continue
		}

		// No tool calls: final answer
		if strings.TrimSpace(assistantMsg.Content) != "" {
			return assistantMsg.Content, totalToolCalls, nil
		}

		// If empty content and no tool calls, return error
		return "", totalToolCalls, fmt.Errorf("received empty content response from %s", target.FullName())
	}

	// If max turns reached, solicit final synthesis
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: "Maximum turns reached. Please synthesize your findings and output your final assessment with the '## Findings' section now.",
	})

	resp, err := w.Client.Complete(ctx, target, messages, nil)
	if err != nil {
		return "", totalToolCalls, err
	}
	return resp.Choices[0].Message.Content, totalToolCalls, nil
}
