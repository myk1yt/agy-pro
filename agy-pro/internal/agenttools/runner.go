package agenttools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolRunner dispatches tool calls with security boundaries and logging.
type ToolRunner struct {
	Root              string
	AllowWrite        bool
	AllowExec         bool
	Logger            *Logger
	AllowedModelTools []string // optional model-level allowed tools filter
}

// NewRunner creates a configured ToolRunner.
func NewRunner(root string, allowWrite, allowExec bool, logger *Logger) *ToolRunner {
	if root == "" {
		root = "."
	}
	return &ToolRunner{
		Root:       root,
		AllowWrite: allowWrite,
		AllowExec:  allowExec,
		Logger:     logger,
	}
}

// Execute parses the tool call JSON arguments, verifies permissions, runs the tool,
// logs the execution, and returns the output string.
func (r *ToolRunner) Execute(seatID int, model, provider, toolName, argsJSON string) (string, error) {
	start := time.Now()
	var out string
	var execErr error

	// 1. Model-level allowed tools check (if specified)
	if len(r.AllowedModelTools) > 0 {
		permitted := false
		for _, t := range r.AllowedModelTools {
			if strings.EqualFold(t, toolName) {
				permitted = true
				break
			}
		}
		if !permitted {
			execErr = fmt.Errorf("tool %q is not in the allowed tools list for model %q", toolName, model)
			r.log(seatID, model, provider, toolName, argsJSON, "", execErr, time.Since(start))
			return fmt.Sprintf("Error: %v", execErr), nil
		}
	}

	// 2. Dispatch based on tool name
	switch toolName {
	case "read_file":
		var args struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for read_file: %w", err)
		} else {
			out, execErr = ReadFile(r.Root, args.Path, args.StartLine, args.EndLine)
		}

	case "list_dir":
		var args struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
			MaxDepth  int    `json:"max_depth"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for list_dir: %w", err)
		} else {
			out, execErr = ListDir(r.Root, args.Path, args.Recursive, args.MaxDepth)
		}

	case "grep":
		var args struct {
			Pattern         string `json:"pattern"`
			Path            string `json:"path"`
			CaseInsensitive bool   `json:"case_insensitive"`
			MaxMatches      int    `json:"max_matches"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for grep: %w", err)
		} else {
			out, execErr = Grep(r.Root, args.Pattern, args.Path, args.CaseInsensitive, args.MaxMatches)
		}

	case "glob":
		var args struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			execErr = fmt.Errorf("invalid arguments for glob: %w", err)
		} else {
			out, execErr = Glob(r.Root, args.Pattern, args.Path)
		}

	case "write_file":
		if !r.AllowWrite {
			execErr = fmt.Errorf("write_file is prohibited (write flag not enabled)")
		} else {
			var args struct {
				Path      string `json:"path"`
				Content   string `json:"content"`
				Overwrite bool   `json:"overwrite"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				execErr = fmt.Errorf("invalid arguments for write_file: %w", err)
			} else {
				out, execErr = WriteFile(r.Root, args.Path, args.Content, args.Overwrite)
			}
		}

	case "run_command":
		if !r.AllowExec {
			execErr = fmt.Errorf("run_command is prohibited (exec flag not enabled)")
		} else {
			var args struct {
				Command        string   `json:"command"`
				Args           []string `json:"args"`
				Cwd            string   `json:"cwd"`
				TimeoutSeconds int      `json:"timeout_seconds"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				execErr = fmt.Errorf("invalid arguments for run_command: %w", err)
			} else {
				out, execErr = RunCommand(r.Root, args.Command, args.Args, args.Cwd, args.TimeoutSeconds)
			}
		}

	default:
		execErr = fmt.Errorf("unknown tool %q", toolName)
	}

	duration := time.Since(start)
	r.log(seatID, model, provider, toolName, argsJSON, out, execErr, duration)

	if execErr != nil {
		return fmt.Sprintf("Error: %v", execErr), nil
	}
	return out, nil
}

func (r *ToolRunner) log(seatID int, model, provider, tool, args, out string, err error, dur time.Duration) {
	if r.Logger == nil {
		return
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	_ = r.Logger.Log(LogEntry{
		SeatID:     seatID,
		Model:      model,
		Provider:   provider,
		Tool:       tool,
		Arguments:  args,
		Output:     out,
		Error:      errMsg,
		DurationMs: dur.Milliseconds(),
	})
}
