package fanout

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"agypro/internal/agenttools"
	"agypro/internal/providers"
)

// SeatResult holds the outcome of an individual seat's execution.
type SeatResult struct {
	SeatID        int              `json:"seat_id"`
	Kind          SeatKind         `json:"kind"` // "native" | "provider" (contract §2.4)
	InitialTarget providers.Target `json:"initial_target"`
	FinalTarget   providers.Target `json:"final_target"`
	Status        string           `json:"status"` // "success", "failed", "degraded"
	Degraded      bool             `json:"degraded"`
	Failovers     []FailoverRecord `json:"failovers,omitempty"`
	Content       string           `json:"content"`
	ToolCallCount int              `json:"tool_call_count"`
	// Usage is the token accounting the seat's provider reported, accumulated
	// across every chat turn (and every failover attempt) of the seat. It is
	// JSON null when nothing was reported: a native seat prints only its answer
	// (agy -p carries no usage block) and a provider that omits usage reports
	// nothing. The numbers are never defaulted to zero, so "no usage reported"
	// can never be misread as "zero tokens spent".
	Usage *SeatUsage `json:"usage"`
	// Thin marks a seat that returned success with no findings signal (see
	// seatIsThin). Reconcile computes it, and a thin seat is excluded from the
	// consensus denominator. It is omitted from JSON when false, so a non-thin
	// seat's summary entry stays byte-identical to before the field existed.
	Thin       bool   `json:"thin,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	// UnitID names the partition unit this seat worked on (B7). Populated only
	// in partition mode for per-unit attribution; it NEVER enters a seat request
	// or prompt (a per-unit string in the shared preamble would break cross-unit
	// prefix caching). Omitted from JSON when empty, so a cross-verification
	// seat's summary entry stays byte-identical to before the field existed.
	UnitID string `json:"unit_id,omitempty"`
}

// SeatUsage is one seat's token accounting. Every field is optional and is
// omitted from JSON when the provider did not report it, so a provider without
// a prompt-cache breakdown carries no cached_tokens key at all.
type SeatUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64 `json:"completion_tokens,omitempty"`
	CachedTokens     *int64 `json:"cached_tokens,omitempty"`
}

// usageTotals accumulates the token counts reported across the chat turns of
// one seat. A turn whose response carries no usage block adds nothing, so a
// provider that never reports usage leaves every total nil instead of zero.
type usageTotals struct {
	prompt     *int64
	completion *int64
	cached     *int64
}

// add folds one response's usage block into the totals. A nil response or a
// response without a usage block contributes nothing.
func (u *usageTotals) add(resp *ChatResponse) {
	if resp == nil || resp.Usage == nil {
		return
	}
	u.prompt = addCount(u.prompt, resp.Usage.PromptTokens)
	u.completion = addCount(u.completion, resp.Usage.CompletionTokens)
	u.cached = addCount(u.cached, resp.Usage.cachedTokens())
}

// addSeat folds one seat's already-accumulated usage into run totals.
func (u *usageTotals) addSeat(seat *SeatUsage) {
	if seat == nil {
		return
	}
	u.prompt = addCount(u.prompt, seat.PromptTokens)
	u.completion = addCount(u.completion, seat.CompletionTokens)
	u.cached = addCount(u.cached, seat.CachedTokens)
}

// addCount returns total+add, or total unchanged when add is absent or negative.
// A negative count is treated as absent (never accumulated), and the sum
// saturates at math.MaxInt64 instead of wrapping negative on int64 overflow, so
// a huge or summed count can never corrupt the totals. The missing-vs-zero
// semantics are unchanged: a nil add leaves total exactly as it was.
func addCount(total, add *int64) *int64 {
	if add == nil || *add < 0 {
		return total
	}
	sum := int64(0)
	if total != nil {
		sum = *total
	}
	if *add > math.MaxInt64-sum {
		sum = math.MaxInt64
	} else {
		sum += *add
	}
	return &sum
}

// seatUsage renders the totals for one seat: nil when nothing was reported, so
// the seat entry keeps "usage": null instead of a block of invented zeros.
func (u usageTotals) seatUsage() *SeatUsage {
	if u.prompt == nil && u.completion == nil && u.cached == nil {
		return nil
	}
	return &SeatUsage{
		PromptTokens:     u.prompt,
		CompletionTokens: u.completion,
		CachedTokens:     u.cached,
	}
}

// runUsage renders the totals for the whole run: nil when no seat reported any
// usage (an all-native run). cache_hit_ratio is cached/prompt and appears only
// when at least one seat's provider reported a cached-token count and the
// prompt total is non-zero.
func (u usageTotals) runUsage() *RunUsage {
	if u.prompt == nil && u.completion == nil && u.cached == nil {
		return nil
	}
	ru := &RunUsage{
		PromptTokens:     u.prompt,
		CompletionTokens: u.completion,
		CachedTokens:     u.cached,
	}
	if u.cached != nil && u.prompt != nil && *u.prompt > 0 {
		ratio := float64(*u.cached) / float64(*u.prompt)
		ru.CacheHitRatio = &ratio
	}
	return ru
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
	Exclude              ExcludeFunc // F-C failover exclusion seam (contract §2.5b)
	Preset               TaskPreset
	UserPrompt           string
	UnitID               string // partition unit this seat works on (B2); empty on the cross-verification path (attribution only, never enters the request)
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
		Kind:          SeatKindProvider,
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

	// Usage is accumulated across every attempt of this seat, so a seat that
	// burned tokens on a failing provider before failing over still reports
	// what it spent.
	usage := &usageTotals{}

	for {
		// Run tool/chat loop for currentTarget
		content, toolCalls, err := w.runChatLoop(ctx, currentTarget, usage)
		if err == nil {
			// Success!
			res.FinalTarget = currentTarget
			res.Content = content
			res.ToolCallCount = toolCalls
			res.Usage = usage.seatUsage()
			res.DurationMs = time.Since(start).Milliseconds()
			return res
		}

		// Failure occurred: trigger failover policy
		reason := err.Error()

		// Step 1: Try same model ID with another provider
		alts := w.Registry.FindAlternatesForModel(currentTarget.Model.ID, currentTarget.Provider.Name)
		var nextTarget *providers.Target
		for _, alt := range alts {
			if w.excluded(alt.Provider.Name, alt.Model) {
				continue // F-C: the exclusion list governs AUTO-seating; skip an excluded alternate
			}
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
			if w.excluded(alt.Provider.Name, alt.Model) {
				continue // F-C: never auto-fail onto an excluded model
			}
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

// excluded reports whether a failover candidate is barred from AUTO-seating by
// the pool-exclusion seam (F-C, contract §2.5b), which matches on the provider
// name, the model id and the family. A nil seam excludes nothing (the engine
// substitutes the built-in default policy).
func (w *SeatWorker) excluded(providerName string, m providers.Model) bool {
	return w.Exclude != nil && w.Exclude(providerName, m)
}

// runChatLoop conducts the conversational tool-calling loop until completion or max turns.
// usage accumulates the token accounting of every chat turn of this attempt; it
// is never reset here, so a seat's totals survive across failover attempts.
func (w *SeatWorker) runChatLoop(ctx context.Context, target providers.Target, usage *usageTotals) (string, int, error) {
	if usage == nil {
		usage = &usageTotals{}
	}
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
		usage.add(resp)

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

		// If empty content and no tool calls, return error. The operand is a
		// composed registry slot ("<provider>/<model>", both halves from
		// providers.json), so it renders through the registry-slot authority
		// exactly like seatSpecLabel does. This text becomes SeatResult.Error
		// and, on the failover path, Failovers[].Reason, which the report
		// writers only seed-redact; that redaction is the identity on the
		// underscore-head credential class, so the mask has to happen here. A
		// benign name keeps its exact bytes.
		return "", totalToolCalls, fmt.Errorf("received empty content response from %s", providers.MaskSlot(target.FullName()))
	}

	// If max turns reached, solicit final synthesis. The synthesis carries the
	// SAME effectiveTools the loop turns carried: it is the seat's largest
	// request (full history plus the synthesis instruction), and a nil tool
	// block here would make that request a guaranteed full cache miss on a
	// provider that caches the prefix. Complete applies the identical
	// tool_choice wiring ("auto" when the model can use tools and the list is
	// non-empty), so the synthesis request matches the loop requests
	// byte-for-byte on the tools block.
	messages = append(messages, ChatMessage{
		Role:    "user",
		Content: "Maximum turns reached. Please synthesize your findings and output your final assessment with the '## Findings' section now.",
	})

	resp, err := w.Client.Complete(ctx, target, messages, effectiveTools)
	if err != nil {
		return "", totalToolCalls, err
	}
	usage.add(resp)

	// A2 attached effectiveTools to the synthesis, so the model can now answer
	// the synthesis request with a tool call (finish_reason "tool_calls") and
	// empty content. Returning that empty content as seat "success" makes
	// reconcile's seatIsThin drop the seat silently from consensus. Retry the
	// synthesis EXACTLY ONCE with tools=nil (the same tool_choice-free request)
	// to force a textual answer, accumulating that retry's usage too; a
	// still-empty answer fails the seat visibly instead of going thin. One
	// bounded retry, never a loop.
	choice := resp.Choices[0]
	if len(choice.Message.ToolCalls) > 0 || strings.TrimSpace(choice.Message.Content) == "" {
		retry, err := w.Client.Complete(ctx, target, messages, nil)
		if err != nil {
			return "", totalToolCalls, err
		}
		usage.add(retry)
		retryChoice := retry.Choices[0]
		if strings.TrimSpace(retryChoice.Message.Content) == "" {
			// Same registry-slot operand as the loop error above, and the same
			// reason: this string reaches SeatResult.Error and
			// Failovers[].Reason, whose later redaction misses the
			// underscore-head credential class.
			return "", totalToolCalls, fmt.Errorf("synthesis produced empty content from %s", providers.MaskSlot(target.FullName()))
		}
		return retryChoice.Message.Content, totalToolCalls, nil
	}
	return choice.Message.Content, totalToolCalls, nil
}
