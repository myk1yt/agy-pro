package fanout

import (
	"fmt"
	"strings"
)

// TaskPreset defines system instructions and goals for a fanout task.
type TaskPreset struct {
	Name         string
	Description  string
	SystemPrompt string
}

// taskPresetNames is the closed set of compiled task names in picker display
// order: review first because it is the engine fallback an unknown name (and
// an empty preset field) resolves to. It is the single source the TUI task
// picker and the editor's save-time known-name check read; the
// TaskNamesMatchesGetTaskPresetSlots test is the drift guard pinning this
// slice against the GetTaskPreset switch below.
var taskPresetNames = []string{"review", "security", "plan-audit", "research", "ideas", "custom"}

// TaskNames returns the closed set of compiled task preset names, in picker
// display order. The slice is a copy: callers may sort or filter freely.
func TaskNames() []string {
	return append([]string(nil), taskPresetNames...)
}

// IsKnownTask reports whether name, after GetTaskPreset's normalization
// (trim + lowercase), is one of the compiled task names. An empty name is
// NOT known: in a preset it means "unset, resolve the engine default".
func IsKnownTask(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, k := range taskPresetNames {
		if k == n {
			return true
		}
	}
	return false
}

// GetTaskPreset returns the preset for a given task name.
// Unknown names deliberately fall back to the review preset: the fallback
// stays a run-time resolution rule for every non-editor writer (the --task
// CLI flag, hand-edited preset files), and the prompt store's taskSlotName
// mirrors it so an unknown name's override slot is the review slot. The
// silent-fallback defect (D13) is fixed at the preset-editor save path,
// which refuses unknown names before they persist; the engine does not
// hard-error a run that resolved a typo to review before this fix existed.
func GetTaskPreset(name string) TaskPreset {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "security":
		return TaskPreset{
			Name:        "security",
			Description: "Autonomous security review: threat modeling, injection, auth bypass, RCE, data leakage, OWASP Top 10.",
			SystemPrompt: `You are an elite Security Principal Engineer performing an independent, double-blind security audit.
Your mission is to find vulnerabilities, attack vectors, privilege escalation paths, insecure defaults, and confidentiality leaks.
Explore the workspace using available tools (read_file, grep, glob, list_dir).
Never speculate; cite exact files and line numbers.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}

	case "plan-audit":
		return TaskPreset{
			Name:        "plan-audit",
			Description: "Architectural & implementation plan audit: feasibility, risk analysis, dependency graph, hidden complexity.",
			SystemPrompt: `You are a Principal Software Architect evaluating an implementation plan or design proposal.
Assess viability, architectural risks, breaking changes, race conditions, edge cases, and dependency traps.
Inspect current code to verify assumptions.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}

	case "research":
		return TaskPreset{
			Name:        "research",
			Description: "Deep investigative research, state of the art, comparative pros/cons, and technical trade-offs.",
			SystemPrompt: `You are a Senior Staff Research Engineer conducting deep comparative technical investigation.
Evaluate trade-offs, performance characteristics, scalability boundaries, and real-world failure modes.
Inspect the existing codebase to ensure your recommendations fit the architecture.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}

	case "ideas":
		return TaskPreset{
			Name:        "ideas",
			Description: "Divergent technical brainstorming, UX innovations, and alternative paradigm proposals.",
			SystemPrompt: `You are a Technical Innovation Strategist.
Generate bold, concrete, high-leverage ideas and innovative alternative approaches that solve the user's goals.
Inspect existing patterns to ensure proposals are feasible and synergistic.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}

	case "custom":
		return TaskPreset{
			Name:        "custom",
			Description: "Custom user-directed swarm task.",
			SystemPrompt: `You are an expert autonomous software engineer participating in an independent double-blind evaluation swarm.
Inspect the workspace using tools as needed.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}

	case "review":
		fallthrough
	default:
		return TaskPreset{
			Name:        "review",
			Description: "Comprehensive code review: bugs, correctness, edge cases, error handling, performance, maintainability.",
			SystemPrompt: `You are a Staff Code Reviewer conducting a rigorous, double-blind code review.
Identify concrete bugs, nil dereferences, unhandled errors, concurrency race conditions, resource leaks, and edge cases.
Explore the workspace using available tools (read_file, grep, glob, list_dir).
Cite exact code locations and propose fixes.

You MUST end your report with a structured "## Findings" section formatted exactly as:
## Findings
- **[SEVERITY]** Title: Concise explanation (Location: path/file.ext:line)
Valid SEVERITY levels: BLOCKER, HIGH, MEDIUM, LOW, INFO.`,
		}
	}
}

// BuildUserPrompt wraps user input with task context.
func BuildUserPrompt(task, prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Sprintf("Please review the workspace files and provide your assessment for task %q.", task)
	}
	return prompt
}
